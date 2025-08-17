package rardecode

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"hash"
	"io"
	"io/fs"
	"sync"
	"time"
)

// FileHeader HostOS types
const (
	HostOSUnknown = 0
	HostOSMSDOS   = 1
	HostOSOS2     = 2
	HostOSWindows = 3
	HostOSUnix    = 4
	HostOSMacOS   = 5
	HostOSBeOS    = 6
)

const (
	maxPassword = int(128)
)

var (
	ErrShortFile        = errors.New("rardecode: decoded file too short")
	ErrInvalidFileBlock = errors.New("rardecode: invalid file block")
	ErrUnexpectedArcEnd = errors.New("rardecode: unexpected end of archive")
	ErrBadFileChecksum  = errors.New("rardecode: bad file checksum")
	ErrSolidOpen        = errors.New("rardecode: solid files don't support Open")
	ErrUnknownVersion   = errors.New("rardecode: unknown archive version")
)

// FileHeader represents a single file in a RAR archive.
type FileHeader struct {
	Name             string    // file name using '/' as the directory separator
	IsDir            bool      // is a directory
	Solid            bool      // is a solid file
	Encrypted        bool      // file contents are encrypted
	HeaderEncrypted  bool      // file header is encrypted
	HostOS           byte      // Host OS the archive was created on
	Attributes       int64     // Host OS specific file attributes
	PackedSize       int64     // packed file size (or first block if the file spans volumes)
	UnPackedSize     int64     // unpacked file size
	UnKnownSize      bool      // unpacked file size is not known
	ModificationTime time.Time // modification time (non-zero if set)
	CreationTime     time.Time // creation time (non-zero if set)
	AccessTime       time.Time // access time (non-zero if set)
	Version          int       // file version
	Offset           int64     // file offset in the archive
	VolumeNumber     int       // which volume this header came from
	PartNumber       int       // for files spanning multiple volumes (0-based)
	TotalParts       int       // total parts for this file across volumes
}

// Mode returns an fs.FileMode for the file, calculated from the Attributes field.
func (f *FileHeader) Mode() fs.FileMode {
	var m fs.FileMode

	if f.IsDir {
		m = fs.ModeDir
	}
	if f.HostOS == HostOSWindows {
		if f.IsDir {
			m |= 0777
		} else if f.Attributes&1 > 0 {
			m |= 0444 // readonly
		} else {
			m |= 0666
		}
		return m
	}
	// assume unix perms for all remaining os types
	m |= fs.FileMode(f.Attributes) & fs.ModePerm

	// only check other bits on unix host created archives
	if f.HostOS != HostOSUnix {
		return m
	}

	if f.Attributes&0x200 != 0 {
		m |= fs.ModeSticky
	}
	if f.Attributes&0x400 != 0 {
		m |= fs.ModeSetgid
	}
	if f.Attributes&0x800 != 0 {
		m |= fs.ModeSetuid
	}

	// Check for additional file types.
	if f.Attributes&0xF000 == 0xA000 {
		m |= fs.ModeSymlink
	}
	return m
}

type byteReader interface {
	io.Reader
	io.ByteReader
}

type archiveFile interface {
	byteReader
	currFile() *fileBlockHeader
	nextFile() (*fileBlockList, error)
	newArchiveFile(blocks *fileBlockList) (archiveFile, error)
	Stat() (fs.FileInfo, error)
}

type archiveFileSeeker interface {
	archiveFile
	io.Seeker
}

type fileCloser struct {
	archiveFile
	io.Closer
}

type fileSeekCloser struct {
	archiveFileSeeker
	io.Closer
}

type errorFile struct {
	archiveFile
	err error
}

func (ef *errorFile) ReadByte() (byte, error)    { return 0, ef.err }
func (ef *errorFile) Read(p []byte) (int, error) { return 0, ef.err }

type fileBlockList struct {
	mu     sync.RWMutex
	blocks []*fileBlockHeader
}

func (fl *fileBlockList) firstBlock() *fileBlockHeader {
	fl.mu.RLock()
	defer fl.mu.RUnlock()
	return fl.blocks[0]
}

func (fl *fileBlockList) lastBlock() *fileBlockHeader {
	fl.mu.RLock()
	defer fl.mu.RUnlock()
	return fl.blocks[len(fl.blocks)-1]
}

func (fl *fileBlockList) findBlock(offset int64) *fileBlockHeader {
	fl.mu.RLock()
	defer fl.mu.RUnlock()
	for _, h := range fl.blocks {
		if offset < h.PackedSize || (offset == h.PackedSize && h.last) {
			return h
		}
		offset -= h.PackedSize
	}
	return nil
}

func (fl *fileBlockList) addBlock(h *fileBlockHeader) {
	fl.mu.Lock()
	defer fl.mu.Unlock()
	if len(fl.blocks) == h.blocknum {
		fl.blocks = append(fl.blocks, h)
	}
}

func (fl *fileBlockList) isDir() bool {
	fl.mu.RLock()
	defer fl.mu.RUnlock()
	return fl.blocks[0].IsDir
}

func (fl *fileBlockList) hasFileHash() bool {
	fl.mu.RLock()
	defer fl.mu.RUnlock()
	return fl.blocks[0].hash != nil
}

func (fl *fileBlockList) removeFileHash() {
	fl.mu.Lock()
	defer fl.mu.Unlock()
	h := *fl.blocks[0]
	h.hash = nil
	fl.blocks[0] = &h
}

func newFileBlockList(blocks ...*fileBlockHeader) *fileBlockList {
	return &fileBlockList{blocks: blocks}
}

// packedFileReader provides sequential access to packed files in a RAR archive.
type packedFileReader struct {
	v      volume
	h      *fileBlockHeader // current file header
	dr     *decodeReader
	offset int64
	blocks *fileBlockList
	opt    *options
}

func (f *packedFileReader) init(blocks *fileBlockList) error {
	h := blocks.firstBlock()
	f.h = h
	f.blocks = blocks
	f.offset = 0
	return nil
}

func (f *packedFileReader) Stat() (fs.FileInfo, error) {
	if f.h == nil {
		return nil, fs.ErrInvalid
	}
	return fileInfo{h: f.h}, nil
}

// nextBlock reads the next file block in the current file at the current
// archive file position, or returns an error if there is a problem.
// It is invalid to call this when already at the last block in the current file.
func (f *packedFileReader) nextBlock() error {
	if f.h == nil {
		return io.EOF
	}
	if f.h.last {
		return io.EOF
	}
	h, err := f.v.nextBlock()
	if err != nil {
		if err == io.EOF {
			// archive ended, but file hasn't
			return ErrUnexpectedArcEnd
		} else if err == errVolumeOrArchiveEnd {
			return ErrMultiVolume
		}
		return err
	}
	if h.first || h.Name != f.h.Name {
		return ErrInvalidFileBlock
	}
	h.packedOff = f.h.packedOff + f.h.PackedSize
	h.blocknum = f.h.blocknum + 1
	f.h = h
	f.offset = h.dataOff
	f.blocks.addBlock(h)
	return nil
}

// next advances to the next packed file in the RAR archive.
func (f *packedFileReader) nextFile() (*fileBlockList, error) {
	// skip to last block in current file
	var err error
	for err == nil {
		err = f.nextBlock()
	}
	if err != io.EOF {
		return nil, err
	}
	h, err := f.v.nextBlock() // get next file block
	if err != nil {
		if err == errVolumeOrArchiveEnd {
			err = io.EOF
		}
		return nil, err
	}
	if !h.first {
		return nil, ErrInvalidFileBlock
	}
	blocks := newFileBlockList(h)
	err = f.init(blocks)
	if err != nil {
		return nil, err
	}
	return blocks, nil
}

func (f *packedFileReader) currFile() *fileBlockHeader { return f.h }

// Read reads the packed data for the current file into p.
func (f *packedFileReader) Read(p []byte) (int, error) {
	for {
		n, err := f.v.Read(p)
		if err == io.EOF {
			err = f.nextBlock()
		}
		if n > 0 || err != nil {
			f.offset += int64(n)
			return n, err
		}
	}
}

func (f *packedFileReader) ReadByte() (byte, error) {
	for {
		b, err := f.v.ReadByte()
		if err == nil {
			f.offset++
			return b, nil
		}
		if err == io.EOF {
			err = f.nextBlock()
			if err == nil {
				continue
			}
		}
		return b, err
	}
}

func (pr *packedFileReader) newArchiveFileFrom(r archiveFile, blocks *fileBlockList) (archiveFile, error) {
	h := blocks.firstBlock()
	err := pr.init(blocks)
	if err != nil {
		return nil, err
	}
	if h.Encrypted {
		if h.key == nil {
			r = &errorFile{archiveFile: r, err: ErrArchivedFileEncrypted}
		} else {
			r, err = newAesDecryptFileReader(r, h.key, h.iv) // decrypt
			if err != nil {
				return nil, err
			}
		}
	}
	// check for compression
	if h.decVer > 0 {
		if pr.dr == nil {
			pr.dr = new(decodeReader)
		}
		err := pr.dr.init(r, h.decVer, h.winSize, !h.Solid, h.arcSolid, h.UnPackedSize)
		if err != nil {
			return nil, err
		}
		r = pr.dr
	}
	if h.UnPackedSize >= 0 && !h.UnKnownSize {
		// Limit reading to UnPackedSize as there may be padding
		r = newLimitedReader(r, h.UnPackedSize)
	}
	if h.hash != nil && !pr.opt.skipCheck {
		r = newChecksumReader(r, h.hash(), blocks.removeFileHash)
	}
	return r, nil
}

func (pr *packedFileReader) newArchiveFile(blocks *fileBlockList) (archiveFile, error) {
	return pr.newArchiveFileFrom(pr, blocks)
}

type packedFileReadSeeker struct {
	packedFileReader
}

func (f *packedFileReadSeeker) openBlock(h *fileBlockHeader, offset int64) (int64, error) {
	err := f.v.openBlock(h.volnum, h.dataOff+offset, h.PackedSize-offset)
	if err != nil {
		return 0, err
	}
	f.h = h
	f.offset = h.packedOff + offset
	return f.offset, nil
}

func (f *packedFileReadSeeker) openNextBlock(h *fileBlockHeader) error {
	_, err := f.openBlock(h, h.PackedSize)
	if err != nil {
		return err
	}
	return f.nextBlock()
}

func (f *packedFileReadSeeker) packedSize() (int64, error) {
	h := f.blocks.lastBlock()
	if h.last {
		return h.packedOff + h.PackedSize, nil
	}
	err := f.openNextBlock(h)
	for err == nil {
		err = f.nextBlock()
	}
	if err != io.EOF {
		return 0, err
	}
	if !f.h.last {
		return 0, ErrInvalidFileBlock
	}
	return f.h.packedOff + f.h.PackedSize, nil
}

func (f *packedFileReadSeeker) Seek(offset int64, whence int) (int64, error) {
	// calculate absolute offset
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		offset += f.offset
	case io.SeekEnd:
		size, err := f.packedSize()
		if err != nil {
			return 0, err
		}
		offset += size
	default:
		return 0, fs.ErrInvalid
	}
	if offset < 0 {
		return 0, fs.ErrInvalid
	}
	// find block in existing list
	h := f.blocks.findBlock(offset)
	if h != nil {
		offset -= h.packedOff
		return f.openBlock(h, offset)
	}
	h = f.blocks.lastBlock()
	if h.last {
		return 0, fs.ErrInvalid
	}
	err := f.openNextBlock(h)
	for err == nil {
		off := offset - h.packedOff
		if off < f.h.PackedSize || (off == f.h.PackedSize && f.h.last) {
			return f.openBlock(f.h, off)
		} else if h.last {
			return 0, fs.ErrInvalid
		}
		err = f.nextBlock()
	}
	if err == io.EOF {
		return 0, fs.ErrInvalid
	}
	return 0, err
}

func (pr *packedFileReadSeeker) newArchiveFile(blocks *fileBlockList) (archiveFile, error) {
	return pr.newArchiveFileFrom(pr, blocks)
}

func newPackedFileReader(v volume, opts *options) archiveFile {
	pr := packedFileReader{v: v, opt: opts}
	if v.canSeek() {
		return &packedFileReadSeeker{pr}
	}
	return &pr
}

type limitedReader struct {
	archiveFile
	size   int64
	offset int64
}

func (l *limitedReader) Read(p []byte) (int, error) {
	diff := l.size - l.offset
	if diff <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > diff {
		p = p[0:diff]
	}
	n, err := l.archiveFile.Read(p)
	l.offset += int64(n)
	if err == io.EOF {
		if l.offset < l.size {
			return n, ErrShortFile
		}
		return n, nil
	}
	return n, err
}

func (l *limitedReader) ReadByte() (byte, error) {
	if l.offset >= l.size {
		return 0, io.EOF
	}
	b, err := l.archiveFile.ReadByte()
	if err != nil {
		if err == io.EOF {
			return 0, ErrShortFile
		}
		return 0, err
	}
	l.offset++
	return b, nil
}

type limitedReadSeeker struct {
	limitedReader
	sr io.Seeker
}

func (l *limitedReadSeeker) Seek(offset int64, whence int) (int64, error) {
	// calculate absolute offset
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		offset += l.offset
	case io.SeekEnd:
		offset += l.size
	default:
		return 0, fs.ErrInvalid
	}
	if offset < 0 || offset > l.size {
		return 0, fs.ErrInvalid
	}
	return l.sr.Seek(offset, io.SeekStart)
}

func newLimitedReader(f archiveFile, size int64) archiveFile {
	lr := limitedReader{archiveFile: f, size: size}
	if sr, ok := f.(io.Seeker); ok {
		return &limitedReadSeeker{limitedReader: lr, sr: sr}
	}
	return &lr
}

type checksumReader struct {
	archiveFile
	hash    hash.Hash
	success func()
	eofErr  error
}

func (cr *checksumReader) eofError() error {
	if cr.eofErr != nil {
		return cr.eofErr
	}
	// calculate file checksum
	h := cr.currFile()
	sum := cr.hash.Sum(nil)
	if len(h.hashKey) > 0 {
		mac := hmac.New(sha256.New, h.hashKey)
		_, _ = mac.Write(sum) // ignore error, should always succeed
		sum = mac.Sum(sum[:0])
		if len(h.sum) == 4 {
			// CRC32
			for i, v := range sum[4:] {
				sum[i&3] ^= v
			}
			sum = sum[:4]
		}
	}
	if !bytes.Equal(sum, h.sum) {
		cr.eofErr = ErrBadFileChecksum
	} else {
		cr.eofErr = io.EOF
		if cr.success != nil {
			cr.success()
		}
	}
	return cr.eofErr
}

func (cr *checksumReader) Read(p []byte) (int, error) {
	n, err := cr.archiveFile.Read(p)
	if n > 0 {
		if n, err = cr.hash.Write(p[:n]); err != nil {
			return n, err
		}
	}
	if err != io.EOF {
		return n, err
	}
	return n, cr.eofError()
}

func (cr *checksumReader) ReadByte() (byte, error) {
	b, err := cr.archiveFile.ReadByte()
	if err != nil {
		if err != io.EOF {
			return 0, err
		}
		return 0, cr.eofError()
	}
	_, err = cr.hash.Write([]byte{b})
	if err != nil {
		return 0, err
	}
	return b, err
}

func newChecksumReader(f archiveFile, h hash.Hash, success func()) *checksumReader {
	return &checksumReader{archiveFile: f, hash: h, success: success}
}

// Reader provides sequential access to files in a RAR archive.
type Reader struct {
	f archiveFile
}

func (r *Reader) Read(p []byte) (int, error) { return r.f.Read(p) }
func (r *Reader) ReadByte() (byte, error)    { return r.f.ReadByte() }

// Next advances to the next file in the archive.
func (r *Reader) Next() (*FileHeader, error) {
	blocks, err := r.f.nextFile()
	if err != nil {
		return nil, err
	}
	r.f, err = r.f.newArchiveFile(blocks)
	if err != nil {
		return nil, err
	}
	h := blocks.firstBlock()
	fh := h.FileHeader
	fh.Offset = h.dataOff
	// Set volume metadata for single volume archive
	fh.VolumeNumber = h.volnum
	fh.PartNumber = h.blocknum
	fh.TotalParts = 1 // Single volume archives have only one part per file
	return &fh, nil
}

// ReadHeaders returns all file headers from the archive.
// For single volume archives only. Use ReadCloser.ReadHeaders() for multivolume archives.
func (r *Reader) ReadHeaders() ([]*FileHeader, error) {
	var headers []*FileHeader
	
	// Reset to beginning by creating a new reader instance
	// Since Reader doesn't support seeking, we can't implement this efficiently
	// Return an error suggesting to use ReadCloser for multivolume support
	return headers, errors.New("rardecode: ReadHeaders not supported on Reader, use OpenReader for multivolume archives")
}

func newReader(v volume, opts *options) Reader {
	pr := newPackedFileReader(v, opts)
	return Reader{f: pr}
}

// NewReader creates a Reader reading from r.
// NewReader only supports single volume archives.
// Multi-volume archives must use OpenReader.
func NewReader(r io.Reader, opts ...Option) (*Reader, error) {
	options := getOptions(opts)
	v, err := newVolume(r, options, 0)
	if err != nil {
		return nil, err
	}
	rdr := newReader(v, options)
	return &rdr, nil
}

// ReadCloser is a Reader that allows closing of the rar archive.
type ReadCloser struct {
	Reader
	cl io.Closer
	vm *volumeManager
}

// Close closes the rar file.
func (rc *ReadCloser) Close() error { return rc.cl.Close() }

// Volumes returns the volume filenames that have been used in decoding the archive
// up to this point. This will include the current open volume if the archive is still
// being processed.
func (rc *ReadCloser) Volumes() []string {
	return rc.vm.Files()
}

// ReadHeaders returns all file headers from all volumes in the multivolume archive.
// This reads all headers without extracting file contents, making it efficient for
// getting archive metadata.
func (rc *ReadCloser) ReadHeaders() ([]*FileHeader, error) {
	// We need to get the volume manager's directory and first file to reconstruct the path
	files := rc.vm.Files()
	if len(files) == 0 {
		return nil, errors.New("rardecode: no volumes available")
	}
	
	// Get the full path to the first volume
	firstVolumePath := rc.vm.dir + files[0]
	
	// Use the readAllFileBlocks function to get all blocks from all volumes
	_, fileBlocks, err := readAllFileBlocks(firstVolumePath, nil)
	if err != nil {
		return nil, err
	}
	
	return convertToFileHeaders(fileBlocks), nil
}

// ReadAllHeaders returns ALL file block headers from all volumes in the multivolume archive.
// Unlike ReadHeaders which returns one header per file, this returns one header per block/part,
// giving you complete visibility into every volume/part combination.
func (rc *ReadCloser) ReadAllHeaders() ([]*FileHeader, error) {
	// We need to get the volume manager's directory and first file to reconstruct the path
	files := rc.vm.Files()
	if len(files) == 0 {
		return nil, errors.New("rardecode: no volumes available")
	}
	
	// Get the full path to the first volume
	firstVolumePath := rc.vm.dir + files[0]
	
	// Use the readAllFileBlocks function to get all blocks from all volumes
	_, fileBlocks, err := readAllFileBlocks(firstVolumePath, nil)
	if err != nil {
		return nil, err
	}
	
	return convertToAllBlockHeaders(fileBlocks), nil
}

// VolumeHeaders returns file headers grouped by volume number.
// This provides visibility into which files are in which volumes.
func (rc *ReadCloser) VolumeHeaders() (map[int][]*FileHeader, error) {
	headers, err := rc.ReadHeaders()
	if err != nil {
		return nil, err
	}
	
	volumeMap := make(map[int][]*FileHeader)
	for _, header := range headers {
		volumeMap[header.VolumeNumber] = append(volumeMap[header.VolumeNumber], header)
	}
	
	return volumeMap, nil
}

// VolumeAllHeaders returns ALL file block headers grouped by volume number.
// Unlike VolumeHeaders which groups files, this groups individual blocks/parts.
func (rc *ReadCloser) VolumeAllHeaders() (map[int][]*FileHeader, error) {
	headers, err := rc.ReadAllHeaders()
	if err != nil {
		return nil, err
	}
	
	volumeMap := make(map[int][]*FileHeader)
	for _, header := range headers {
		volumeMap[header.VolumeNumber] = append(volumeMap[header.VolumeNumber], header)
	}
	
	return volumeMap, nil
}

// OpenReader opens a RAR archive specified by the name and returns a ReadCloser.
func OpenReader(name string, opts ...Option) (*ReadCloser, error) {
	options := getOptions(opts)
	v, err := openVolume(name, options)
	if err != nil {
		return nil, err
	}
	rc := &ReadCloser{vm: v.vm, cl: v}
	rc.Reader = newReader(v, options)
	return rc, nil
}

// File represents a file in a RAR archive
type File struct {
	FileHeader
	blocks *fileBlockList
	vm     *volumeManager
}

// Open returns an io.ReadCloser that provides access to the File's contents.
// Open is not supported on Solid File's as their contents depend on the decoding
// of the preceding files in the archive. Use OpenReader and Next to access Solid file
// contents instead.
func (f *File) Open() (io.ReadCloser, error) {
	return f.vm.openArchiveFile(f.blocks)
}

// ReadHeaders reads all file headers from a multivolume RAR archive.
// This is similar to List but returns only headers without File objects,
// making it more efficient when only metadata is needed.
// This function provides functionality similar to sharpcompress headerFactory.ReadHeaders.
func ReadHeaders(name string, opts ...Option) ([]*FileHeader, error) {
	_, fileBlocks, err := readAllFileBlocks(name, opts)
	if err != nil {
		return nil, err
	}
	
	return convertToFileHeaders(fileBlocks), nil
}

// ReadAllHeaders reads ALL file block headers from a multivolume RAR archive.
// Unlike ReadHeaders which returns one header per file, this returns one header 
// per block/part, giving you visibility into every volume/part combination.
func ReadAllHeaders(name string, opts ...Option) ([]*FileHeader, error) {
	_, fileBlocks, err := readAllFileBlocks(name, opts)
	if err != nil {
		return nil, err
	}
	
	return convertToAllBlockHeaders(fileBlocks), nil
}

// List returns a list of File's in the RAR archive specified by name.
func List(name string, opts ...Option) ([]*File, error) {
	vm, fileBlocks, err := listFileBlocks(name, opts)
	if err != nil {
		return nil, err
	}
	var fl []*File
	for _, blocks := range fileBlocks {
		h := blocks.firstBlock()
		fh := h.FileHeader
		fh.Offset = h.dataOff
		// Set volume metadata
		fh.VolumeNumber = h.volnum
		fh.PartNumber = h.blocknum
		// Calculate total parts
		maxBlockNum := h.blocknum
		blocks.mu.RLock()
		for _, block := range blocks.blocks {
			if block.blocknum > maxBlockNum {
				maxBlockNum = block.blocknum
			}
		}
		blocks.mu.RUnlock()
		fh.TotalParts = maxBlockNum + 1
		
		f := &File{
			FileHeader: fh,
			blocks:     blocks,
			vm:         vm,
		}
		fl = append(fl, f)
	}
	return fl, nil
}
