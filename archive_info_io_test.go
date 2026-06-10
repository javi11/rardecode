package rardecode

import (
	"bytes"
	"io"
	"io/fs"
	"os"
	"reflect"
	"sync"
	"testing"
)

// countingReaderAt wraps a bytes.Reader, counting Read and ReadAt calls.
type countingReaderAt struct {
	r       *bytes.Reader
	reads   int
	readAts int
}

func (c *countingReaderAt) Read(p []byte) (int, error) {
	c.reads++
	return c.r.Read(p)
}

func (c *countingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	c.readAts++
	return c.r.ReadAt(p, off)
}

// countingFile wraps an fs.File, counting Read/ReadAt calls and tracking Close.
type countingFile struct {
	f     *os.File
	fsys  *countingFS
	path  string
	state *fileIOStats
}

type fileIOStats struct {
	opens   int
	closes  int
	reads   int
	readAts int
}

func (c *countingFile) Stat() (fs.FileInfo, error) { return c.f.Stat() }

func (c *countingFile) Read(p []byte) (int, error) {
	c.fsys.mu.Lock()
	c.state.reads++
	c.fsys.mu.Unlock()
	return c.f.Read(p)
}

func (c *countingFile) ReadAt(p []byte, off int64) (int, error) {
	c.fsys.mu.Lock()
	c.state.readAts++
	c.fsys.mu.Unlock()
	return c.f.ReadAt(p, off)
}

func (c *countingFile) Close() error {
	c.fsys.mu.Lock()
	c.state.closes++
	c.fsys.mu.Unlock()
	return c.f.Close()
}

// countingFS wraps the OS filesystem, tracking per-path opens, closes and reads.
type countingFS struct {
	mu    sync.Mutex
	stats map[string]*fileIOStats
}

func newCountingFS() *countingFS {
	return &countingFS{stats: make(map[string]*fileIOStats)}
}

func (c *countingFS) Open(name string) (fs.File, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	st := c.stats[name]
	if st == nil {
		st = &fileIOStats{}
		c.stats[name] = st
	}
	st.opens++
	c.mu.Unlock()
	return &countingFile{f: f, fsys: c, path: name, state: st}, nil
}

// TestListArchiveInfoReadCounts verifies that the parallel metadata scan does
// the minimal number of opens and ReadAt calls per volume, leaks no file
// handles, and returns the same result as the sequential scan.
func TestListArchiveInfoReadCounts(t *testing.T) {
	name := "testdata/multi.part1.rar"
	if _, err := os.Stat(name); err != nil {
		t.Skipf("missing fixture: %v", err)
	}

	cfs := newCountingFS()
	infos, err := ListArchiveInfo(name, FileSystem(cfs))
	if err != nil {
		t.Fatalf("ListArchiveInfo: %v", err)
	}
	if len(infos) == 0 {
		t.Fatal("no files listed")
	}

	// Sequential golden comparison.
	seqInfos, err := ListArchiveInfo(name, ParallelRead(false))
	if err != nil {
		t.Fatalf("sequential ListArchiveInfo: %v", err)
	}
	if !reflect.DeepEqual(infos, seqInfos) {
		t.Errorf("parallel result differs from sequential:\nparallel:   %+v\nsequential: %+v", infos, seqInfos)
	}

	cfs.mu.Lock()
	defer cfs.mu.Unlock()
	for path, st := range cfs.stats {
		t.Logf("%s: opens=%d closes=%d reads=%d readAts=%d", path, st.opens, st.closes, st.reads, st.readAts)
		if st.opens != st.closes {
			t.Errorf("%s: leaked handles: opens=%d closes=%d", path, st.opens, st.closes)
		}
		if st.reads != 0 {
			t.Errorf("%s: expected all I/O via ReadAt, got %d Read calls", path, st.reads)
		}
	}

	// Volume 0 is opened once (by openVolume) and must not be reopened by the
	// discovery probe or a worker. Other volumes are opened twice: once by the
	// discovery probe (no reads) and once by the worker that scans them.
	if st := cfs.stats["testdata/multi.part1.rar"]; st == nil || st.opens != 1 {
		t.Errorf("volume 0: expected exactly 1 open, got %+v", st)
	}
	for _, path := range []string{"testdata/multi.part2.rar", "testdata/multi.part3.rar"} {
		if st := cfs.stats[path]; st == nil || st.opens != 2 {
			t.Errorf("%s: expected exactly 2 opens (probe + worker), got %+v", path, st)
		}
	}

	// One ReadAt per header cluster is optimal: the signature, archive header
	// and first file header all fit in a single buffer fill at the start of
	// each volume, and one more fill is needed per header region that follows
	// packed data. For this fixture (file1 spans part1-2, file2 spans part2-3):
	//   part1: start headers + end block                      = 2
	//   part2: start headers + file2 header mid-volume + end  = 3
	//   part3: start headers + end block                      = 2
	want := map[string]int{
		"testdata/multi.part1.rar": 2,
		"testdata/multi.part2.rar": 3,
		"testdata/multi.part3.rar": 2,
	}
	for path, n := range want {
		if st := cfs.stats[path]; st == nil || st.readAts != n {
			t.Errorf("%s: expected exactly %d ReadAt calls, got %+v", path, n, st)
		}
	}
}

// TestResetSingleReadAt verifies that resetting on a ReaderAt source costs a
// single ReadAt that also leaves the bytes after the signature buffered, so
// the archive header reads that follow need no additional I/O.
func TestResetSingleReadAt(t *testing.T) {
	data, err := os.ReadFile("testdata/single.rar")
	if err != nil {
		t.Skipf("missing fixture: %v", err)
	}
	cra := &countingReaderAt{r: bytes.NewReader(data)}
	br := &bufVolumeReader{buf: make([]byte, defaultBufSize)}
	if err := br.Reset(cra); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if cra.readAts != 1 {
		t.Errorf("Reset cost %d ReadAt calls, want 1", cra.readAts)
	}
	if cra.reads != 0 {
		t.Errorf("Reset cost %d Read calls, want 0", cra.reads)
	}
	// The rest of the first buffer must be served without further I/O.
	p := make([]byte, 64)
	if _, err := io.ReadFull(br, p); err != nil {
		t.Fatalf("ReadFull after Reset: %v", err)
	}
	if cra.readAts != 1 {
		t.Errorf("reading buffered header bytes cost extra ReadAt calls: got %d, want 1", cra.readAts)
	}
	if !bytes.Equal(p, data[br.off-64:br.off]) {
		t.Error("bytes after signature do not match file content")
	}
}

// TestFindSigReaderAtBufferBoundary pins the findSig refill path for ReaderAt
// sources: when the signature straddles the end of the first buffer fill, the
// refill must read at the correct file offset (the underlying stream position
// is never advanced by ReadAt-based fills).
func TestFindSigReaderAtBufferBoundary(t *testing.T) {
	sig := []byte(sigPrefix + "\x01\x00") // RAR5 signature
	for _, sigOff := range []int{defaultBufSize - 6, defaultBufSize - 1, defaultBufSize + 100, 3 * defaultBufSize} {
		// Garbage prefix with no signature bytes, then the signature.
		data := bytes.Repeat([]byte{'x'}, sigOff)
		data = append(data, sig...)
		data = append(data, bytes.Repeat([]byte{'y'}, 32)...)

		cra := &countingReaderAt{r: bytes.NewReader(data)}
		br := &bufVolumeReader{buf: make([]byte, defaultBufSize)}
		if err := br.Reset(cra); err != nil {
			t.Errorf("sigOff=%d: Reset: %v", sigOff, err)
			continue
		}
		if br.ver != 1 {
			t.Errorf("sigOff=%d: ver = %d, want 1", sigOff, br.ver)
		}
		if want := int64(sigOff + len(sig)); br.off != want {
			t.Errorf("sigOff=%d: off = %d, want %d", sigOff, br.off, want)
		}
		if cra.reads != 0 {
			t.Errorf("sigOff=%d: %d stream Read calls, want 0 (ReaderAt source)", sigOff, cra.reads)
		}
	}
}

// TestSeekWithinBufferNoRead verifies that seeks landing inside the buffered
// window cost no additional reads on a ReaderAt source.
func TestSeekWithinBufferNoRead(t *testing.T) {
	data := []byte(sigPrefix + "\x01\x00")
	data = append(data, bytes.Repeat([]byte{'z'}, 2*defaultBufSize)...)

	cra := &countingReaderAt{r: bytes.NewReader(data)}
	br := &bufVolumeReader{buf: make([]byte, defaultBufSize)}
	if err := br.Reset(cra); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	base := cra.readAts

	// Forward, backward and edge seeks within the buffered window [0, bufsize).
	for _, off := range []int64{100, 5, 0, int64(defaultBufSize)} {
		if err := br.seek(off); err != nil {
			t.Fatalf("seek(%d): %v", off, err)
		}
		if br.off != off {
			t.Errorf("seek(%d): off = %d", off, br.off)
		}
	}
	if cra.readAts != base {
		t.Errorf("in-window seeks cost %d extra ReadAt calls, want 0", cra.readAts-base)
	}

	// Reading after an in-window seek returns the right data without I/O.
	if err := br.seek(int64(len(sigPrefix) + 2)); err != nil {
		t.Fatalf("seek: %v", err)
	}
	b, err := br.ReadByte()
	if err != nil || b != 'z' {
		t.Errorf("ReadByte after seek = %q, %v; want 'z', nil", b, err)
	}
	if cra.readAts != base {
		t.Errorf("read of buffered byte cost extra ReadAt calls")
	}

	// A seek outside the window drops the buffer; the next read fetches at the
	// new offset.
	target := int64(defaultBufSize + 512)
	if err := br.seek(target); err != nil {
		t.Fatalf("seek out of window: %v", err)
	}
	if _, err := br.ReadByte(); err != nil {
		t.Fatalf("ReadByte after out-of-window seek: %v", err)
	}
	if cra.readAts != base+1 {
		t.Errorf("out-of-window seek+read cost %d ReadAt calls, want 1", cra.readAts-base)
	}
}
