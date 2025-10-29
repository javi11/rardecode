package rardecode

import "io"

// FilePartInfo represents a single volume part of a file in a RAR archive.
type FilePartInfo struct {
	Path          string `json:"path"`                    // Full path to the volume file
	DataOffset    int64  `json:"dataOffset"`              // Byte offset where the file data starts in the volume
	PackedSize        int64  `json:"packedSize"`              // Size of packed data in this volume part
	UnpackedSize      int64  `json:"unpackedSize"`            // Total unpacked size of the complete file
	Stored            bool   `json:"stored"`                  // True if data is stored (not compressed)
	Compressed        bool   `json:"compressed"`              // True if data is compressed
	CompressionMethod string `json:"compressionMethod"`       // Compression method used (stored, rar2.0, rar2.9, rar5.0, rar7.0)
	Encrypted         bool   `json:"encrypted"`               // True if this part is encrypted
	Salt          []byte `json:"salt,omitempty"`          // Salt for key derivation (only if encrypted and password provided)
	AesKey        []byte `json:"aesKey,omitempty"`        // AES-256 key (32 bytes, only if encrypted and password provided)
	AesIV         []byte `json:"aesIV,omitempty"`         // AES IV (16 bytes, only if encrypted and password provided)
	KdfIterations int    `json:"kdfIterations,omitempty"` // PBKDF2 iterations (RAR5: 2^n, RAR3/4: 0x40000, only if encrypted)
}

// ArchiveFileInfo represents a complete file in a RAR archive with all its volume parts.
type ArchiveFileInfo struct {
	Name              string         `json:"name"`              // File name
	TotalPackedSize   int64          `json:"totalPackedSize"`   // Sum of packed sizes across all parts
	TotalUnpackedSize int64          `json:"totalUnpackedSize"` // Total unpacked size of the file
	Parts             []FilePartInfo `json:"parts"`             // Information about each volume part
	AnyEncrypted      bool           `json:"anyEncrypted"`      // True if any part is encrypted
	AllStored         bool           `json:"allStored"`         // True if all parts are stored (not compressed)
	Compressed        bool           `json:"compressed"`        // True if file is compressed
	CompressionMethod string         `json:"compressionMethod"` // Compression method used (stored, rar2.0, rar2.9, rar5.0, rar7.0)
}

// compressionMethodName returns a human-readable name for the compression method
// based on the decoder version.
func compressionMethodName(decVer int) string {
	switch decVer {
	case 0:
		return "stored"
	case 1:
		return "rar2.0"
	case 2:
		return "rar2.9"
	case 3:
		return "rar5.0"
	case 4:
		return "rar7.0"
	default:
		return "unknown"
	}
}

// ListArchiveInfo returns detailed information about files in a RAR archive,
// including volume paths, offsets, and sizes for each part of multi-volume files.
//
// This function is useful for understanding the structure of RAR archives,
// especially multi-volume archives, without extracting the files.
//
// Note: This works best with stored (uncompressed) files. For compressed or
// encrypted files, the metadata will be provided but validation may not be possible.
func ListArchiveInfo(name string, opts ...Option) ([]ArchiveFileInfo, error) {
	vm, fileBlocks, err := listFileBlocks(name, opts)
	if err != nil {
		return nil, err
	}

	result := make([]ArchiveFileInfo, 0, len(fileBlocks))

	for _, blocks := range fileBlocks {
		blocks.mu.RLock()
		blockList := blocks.blocks
		blocks.mu.RUnlock()

		if len(blockList) == 0 {
			continue
		}

		firstBlock := blockList[0]

		// Initialize file info
		fileInfo := ArchiveFileInfo{
			Name:              firstBlock.Name,
			TotalUnpackedSize: firstBlock.UnPackedSize,
			Parts:             make([]FilePartInfo, 0, len(blockList)),
			AllStored:         true,
			Compressed:        firstBlock.decVer != 0,
			CompressionMethod: compressionMethodName(firstBlock.decVer),
		}

		// Process each block (volume part)
		for _, block := range blockList {
			// Get the full path to the volume file
			volumePath := vm.GetVolumePath(block.volnum)

			// Determine if this part is stored (not compressed)
			stored := block.decVer == 0
			compressed := block.decVer != 0
			compressionMethod := compressionMethodName(block.decVer)

			// Check encryption
			encrypted := block.Encrypted

			// Create part info
			partInfo := FilePartInfo{
				Path:              volumePath,
				DataOffset:        block.dataOff,
				PackedSize:        block.PackedSize,
				UnpackedSize:      block.UnPackedSize,
				Stored:            stored,
				Compressed:        compressed,
				CompressionMethod: compressionMethod,
				Encrypted:         encrypted,
			}

			// Add encryption parameters if available (password was provided and file is encrypted)
			if encrypted && len(block.key) > 0 {
				partInfo.Salt = block.salt
				partInfo.AesKey = block.key
				partInfo.AesIV = block.iv
				partInfo.KdfIterations = block.kdfCount
			}

			fileInfo.Parts = append(fileInfo.Parts, partInfo)
			fileInfo.TotalPackedSize += block.PackedSize

			// Update aggregate flags
			if !stored {
				fileInfo.AllStored = false
			}
			if encrypted {
				fileInfo.AnyEncrypted = true
			}
		}

		// ignore files with unknown size
		if fileInfo.TotalUnpackedSize > 0 {
			result = append(result, fileInfo)
		}
	}

	return result, nil
}

// ArchiveIterator provides sequential access to files in a RAR archive
// without loading all files into memory at once.
//
// The iterator must be closed when done to release resources:
//
//	iter, err := rardecode.NewArchiveIterator("archive.rar")
//	if err != nil {
//	    return err
//	}
//	defer iter.Close()
//
//	for iter.Next() {
//	    info := iter.FileInfo()
//	    fmt.Printf("File: %s, Size: %d\n", info.Name, info.TotalUnpackedSize)
//	}
//	if err := iter.Err(); err != nil {
//	    return err
//	}
//
// ArchiveIterator is not safe for concurrent use.
type ArchiveIterator struct {
	v       volume         // underlying volume interface
	pr      archiveFile    // packed file reader
	vm      *volumeManager // volume manager for multi-volume archives
	opts    *options       // archive options
	current *ArchiveFileInfo // current file info (nil before first Next())
	err     error          // last error encountered
	closed  bool           // whether Close() has been called
}

// NewArchiveIterator creates an iterator for sequential access to archive files.
// The iterator must be closed when done to release resources.
//
// Example:
//
//	iter, err := rardecode.NewArchiveIterator("archive.rar")
//	if err != nil {
//	    return err
//	}
//	defer iter.Close()
//
//	for iter.Next() {
//	    info := iter.FileInfo()
//	    fmt.Printf("File: %s, Size: %d\n", info.Name, info.TotalUnpackedSize)
//	}
//	if err := iter.Err(); err != nil {
//	    return err
//	}
func NewArchiveIterator(name string, opts ...Option) (*ArchiveIterator, error) {
	options := getOptions(opts)
	if options.openCheck {
		options.skipCheck = false
	}

	v, err := openVolume(name, options)
	if err != nil {
		return nil, err
	}

	pr := newPackedFileReader(v, options)

	return &ArchiveIterator{
		v:    v,
		pr:   pr,
		vm:   v.vm,
		opts: options,
	}, nil
}

// Next advances the iterator to the next file in the archive.
// It returns true if a file is available, false if the end of archive
// is reached or an error occurred. Use Err() to check for errors.
//
// Example:
//
//	for iter.Next() {
//	    info := iter.FileInfo()
//	    // process file info
//	}
//	if err := iter.Err(); err != nil {
//	    // handle error
//	}
func (it *ArchiveIterator) Next() bool {
	if it.closed {
		it.err = io.ErrClosedPipe
		return false
	}

	if it.err != nil {
		return false
	}

	// Get next file blocks
	blocks, err := it.pr.nextFile()
	if err != nil {
		if err == io.EOF {
			// Normal end of archive
			it.err = nil
			return false
		}
		it.err = err
		return false
	}

	// Build ArchiveFileInfo from blocks
	fileInfo, err := it.buildFileInfo(blocks)
	if err != nil {
		it.err = err
		return false
	}

	// Skip files with unknown size (consistent with ListArchiveInfo)
	if fileInfo.TotalUnpackedSize <= 0 {
		// Recursively call Next() to skip this file
		return it.Next()
	}

	it.current = fileInfo
	return true
}

// FileInfo returns the current file info.
// It returns nil if Next() hasn't been called or if there are no more files.
func (it *ArchiveIterator) FileInfo() *ArchiveFileInfo {
	return it.current
}

// Err returns the last error encountered during iteration.
// It returns nil at the end of a successful iteration.
func (it *ArchiveIterator) Err() error {
	return it.err
}

// Close releases resources associated with the iterator.
// It should be called when iteration is complete.
// Close is safe to call multiple times.
func (it *ArchiveIterator) Close() error {
	if it.closed {
		return nil
	}
	it.closed = true
	it.current = nil
	if closer, ok := it.v.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

// buildFileInfo constructs ArchiveFileInfo from fileBlockList.
func (it *ArchiveIterator) buildFileInfo(blocks *fileBlockList) (*ArchiveFileInfo, error) {
	blocks.mu.RLock()
	blockList := blocks.blocks
	blocks.mu.RUnlock()

	if len(blockList) == 0 {
		return nil, io.EOF
	}

	firstBlock := blockList[0]

	// Initialize file info
	fileInfo := &ArchiveFileInfo{
		Name:              firstBlock.Name,
		TotalUnpackedSize: firstBlock.UnPackedSize,
		Parts:             make([]FilePartInfo, 0, len(blockList)),
		AllStored:         true,
		Compressed:        firstBlock.decVer != 0,
		CompressionMethod: compressionMethodName(firstBlock.decVer),
	}

	// Process each block (volume part)
	for _, block := range blockList {
		// Get the full path to the volume file
		volumePath := it.vm.GetVolumePath(block.volnum)

		// Determine if this part is stored (not compressed)
		stored := block.decVer == 0
		compressed := block.decVer != 0
		compressionMethod := compressionMethodName(block.decVer)

		// Check encryption
		encrypted := block.Encrypted

		// Create part info
		partInfo := FilePartInfo{
			Path:              volumePath,
			DataOffset:        block.dataOff,
			PackedSize:        block.PackedSize,
			UnpackedSize:      block.UnPackedSize,
			Stored:            stored,
			Compressed:        compressed,
			CompressionMethod: compressionMethod,
			Encrypted:         encrypted,
		}

		// Add encryption parameters if available (password was provided and file is encrypted)
		if encrypted && len(block.key) > 0 {
			partInfo.Salt = block.salt
			partInfo.AesKey = block.key
			partInfo.AesIV = block.iv
			partInfo.KdfIterations = block.kdfCount
		}

		fileInfo.Parts = append(fileInfo.Parts, partInfo)
		fileInfo.TotalPackedSize += block.PackedSize

		// Update aggregate flags
		if !stored {
			fileInfo.AllStored = false
		}
		if encrypted {
			fileInfo.AnyEncrypted = true
		}
	}

	return fileInfo, nil
}
