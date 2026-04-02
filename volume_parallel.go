package rardecode

import (
	ctx "context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"runtime"
	"sync"
)

var (
	ErrParallelReadFailed = errors.New("rardecode: parallel read failed")
)

// parallelVolumeReader coordinates reading headers from multiple volumes concurrently
type parallelVolumeReader struct {
	vm              *volumeManager
	opt             *options
	maxConcurrent   int
	volumeCount     int
	headersByVolume map[int][]*fileBlockHeader
	mu              sync.RWMutex
}

// volumeWorkerResult represents the result from a single volume worker
type volumeWorkerResult struct {
	volnum  int
	headers []*fileBlockHeader
	err     error
}

// newParallelVolumeReader creates a new parallel volume reader
func newParallelVolumeReader(vm *volumeManager, opt *options) *parallelVolumeReader {
	maxConcurrent := opt.maxConcurrentVolumes
	if maxConcurrent <= 0 {
		maxConcurrent = runtime.NumCPU() // default
	}
	return &parallelVolumeReader{
		vm:              vm,
		opt:             opt,
		maxConcurrent:   maxConcurrent,
		headersByVolume: make(map[int][]*fileBlockHeader),
	}
}

// discoverVolumeCount attempts to determine how many volumes exist
// Returns the count or -1 if cannot be determined
func (pvr *parallelVolumeReader) discoverVolumeCount() int {
	// Try to open volumes sequentially until one fails
	count := 0
	for {
		if count >= 1000 { // safety limit
			break
		}
		_, err := pvr.vm.openVolumeFile(count)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				break
			}
			// Other errors - cannot determine count reliably
			return -1
		}
		count++
	}
	return count
}

// readVolumeHeaders reads all headers from a single volume
func (pvr *parallelVolumeReader) readVolumeHeaders(c ctx.Context, volnum int) ([]*fileBlockHeader, error) {
	// Open the volume
	v, err := pvr.vm.newVolume(volnum)
	if err != nil {
		return nil, err
	}
	defer v.Close()

	headers := []*fileBlockHeader{}

	// Read all headers from this volume
	for {
		select {
		case <-c.Done():
			return nil, c.Err()
		default:
		}

		h, err := v.readerVolume.nextBlockHeaderOnly()
		if err != nil {
			if err == io.EOF {
				break
			}
			if err == errVolumeOrArchiveEnd {
				// This volume is done
				break
			}
			if err == ErrMultiVolume {
				// File continues in next volume - this is expected
				// We still want to return the headers we've collected so far
				break
			}
			return nil, err
		}

		headers = append(headers, h)
	}

	return headers, nil
}

// safeReadVolumeHeaders wraps readVolumeHeaders with panic recovery.
// This prevents malformed archive data from crashing the entire process
// when read in a worker goroutine.
func (pvr *parallelVolumeReader) safeReadVolumeHeaders(c ctx.Context, volnum int) (headers []*fileBlockHeader, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("rardecode: panic reading volume %d: %v", volnum, r)
		}
	}()
	return pvr.readVolumeHeaders(c, volnum)
}

// worker processes volumes from the work channel
func (pvr *parallelVolumeReader) worker(c ctx.Context, workCh <-chan int, resultCh chan<- volumeWorkerResult, wg *sync.WaitGroup) {
	defer wg.Done()

	for {
		select {
		case <-c.Done():
			return
		case volnum, ok := <-workCh:
			if !ok {
				return
			}

			headers, err := pvr.safeReadVolumeHeaders(c, volnum)
			select {
			case <-c.Done():
				return
			case resultCh <- volumeWorkerResult{volnum: volnum, headers: headers, err: err}:
			}
		}
	}
}

// readAllVolumesParallel reads headers from all volumes in parallel
func (pvr *parallelVolumeReader) readAllVolumesParallel() error {
	// First, discover how many volumes we have
	volumeCount := pvr.discoverVolumeCount()
	if volumeCount <= 0 {
		return ErrParallelReadFailed
	}
	pvr.volumeCount = volumeCount

	// If only one volume, fall back to sequential
	if volumeCount == 1 {
		headers, err := pvr.readVolumeHeaders(ctx.Background(), 0)
		if err != nil {
			return err
		}
		pvr.headersByVolume[0] = headers
		return nil
	}

	// Create context for cancellation
	c, cancel := ctx.WithCancel(ctx.Background())
	defer cancel()

	// Create channels
	workCh := make(chan int, volumeCount)
	resultCh := make(chan volumeWorkerResult, volumeCount)

	// Queue all volume numbers
	for i := 0; i < volumeCount; i++ {
		workCh <- i
	}
	close(workCh)

	// Start workers (limited by maxConcurrent)
	numWorkers := min(pvr.maxConcurrent, volumeCount)
	var wg sync.WaitGroup
	wg.Add(numWorkers)

	for i := 0; i < numWorkers; i++ {
		go pvr.worker(c, workCh, resultCh, &wg)
	}

	// Wait for all workers to complete
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	// Collect results
	var firstErr error
	for result := range resultCh {
		if result.err != nil {
			if firstErr == nil {
				firstErr = result.err
				cancel() // Cancel remaining work
			}
			continue
		}

		pvr.mu.Lock()
		pvr.headersByVolume[result.volnum] = result.headers
		pvr.mu.Unlock()
	}

	if firstErr != nil {
		return firstErr
	}

	// Verify we got all volumes
	pvr.mu.RLock()
	defer pvr.mu.RUnlock()
	if len(pvr.headersByVolume) != volumeCount {
		return ErrParallelReadFailed
	}

	return nil
}

// assembleFileBlocks assembles file blocks from headers across all volumes
// This handles files that span multiple volumes
func (pvr *parallelVolumeReader) assembleFileBlocks() []*fileBlockList {
	pvr.mu.RLock()
	defer pvr.mu.RUnlock()

	// Map to track files by name
	fileMap := make(map[string]*fileBlockList)
	fileOrder := []string{} // Track insertion order

	// Process volumes in order
	for volnum := 0; volnum < pvr.volumeCount; volnum++ {
		headers := pvr.headersByVolume[volnum]

		for _, h := range headers {
			fileName := h.Name

			if h.first {
				// First block of a new file
				if _, exists := fileMap[fileName]; !exists {
					fileMap[fileName] = newFileBlockList(h)
					fileOrder = append(fileOrder, fileName)
				} else {
					// This is a new version of an existing file
					// Keep the one with higher version
					existing := fileMap[fileName].firstBlock()
					if h.Version > existing.Version {
						fileMap[fileName] = newFileBlockList(h)
					}
				}
			} else {
				// Continuation block
				if blocks, exists := fileMap[fileName]; exists {
					// Add to existing file
					h.blocknum = len(blocks.blocks)
					blocks.addBlock(h)
				}
				// If file doesn't exist, this is an error in the archive
				// but we'll skip it to be resilient
			}
		}
	}

	// Convert to ordered slice
	result := make([]*fileBlockList, 0, len(fileOrder))
	for _, fileName := range fileOrder {
		result = append(result, fileMap[fileName])
	}

	return result
}

// listFileBlocksParallel reads file blocks from a multi-volume archive in parallel
func listFileBlocksParallel(name string, opts []Option) (*volumeManager, []*fileBlockList, error) {
	options := getOptions(opts)
	if options.openCheck {
		options.skipCheck = false
	}

	// Open the first volume to get the volume manager
	v, err := openVolume(name, options)
	if err != nil {
		return nil, nil, err
	}
	defer v.Close()

	// Create parallel reader
	pvr := newParallelVolumeReader(v.vm, options)

	// Read all volumes in parallel
	err = pvr.readAllVolumesParallel()
	if err != nil {
		return nil, nil, err
	}

	// Assemble file blocks
	fileBlocks := pvr.assembleFileBlocks()

	// If openCheck is enabled, we need to validate the files
	// This requires sequential processing as we need to decompress/decrypt
	if options.openCheck {
		// Re-open for validation
		vCheck, err := openVolume(name, options)
		if err != nil {
			return nil, nil, err
		}
		defer vCheck.Close()

		pr := newPackedFileReader(vCheck, options)
		for _, blocks := range fileBlocks {
			if blocks.hasFileHash() {
				f, err := pr.newArchiveFile(blocks)
				if err != nil {
					return nil, nil, err
				}
				_, err = io.Copy(io.Discard, f)
				if err != nil {
					return nil, nil, err
				}
			}
		}
	}

	return v.vm, fileBlocks, nil
}
