package rardecode

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"reflect"
	"strings"
	"testing"
)

var errTailGone = errors.New("tail article missing")

// tailBrokenFS serves testdata volumes but refuses to read the trailing
// bytes of the volumes named in broken (every volume after the first when
// nil), the way a Usenet-backed filesystem behaves when a volume's last
// article is gone from the provider.
type tailBrokenFS struct {
	tailBytes int64
	broken    map[string]bool
}

type tailBrokenFile struct {
	*os.File
	size int64
	tail int64
}

func (t tailBrokenFS) Open(name string) (fs.File, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	if t.broken == nil && strings.HasSuffix(name, "part1.rar") {
		return f, nil
	}
	if t.broken != nil && !t.broken[name] {
		return f, nil
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &tailBrokenFile{File: f, size: st.Size(), tail: t.tailBytes}, nil
}

func (f *tailBrokenFile) ReadAt(p []byte, off int64) (int, error) {
	if off+int64(len(p)) > f.size-f.tail {
		return 0, errTailGone
	}
	return f.File.ReadAt(p, off)
}

func (f *tailBrokenFile) Read(p []byte) (int, error) {
	pos, err := f.File.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	if pos+int64(len(p)) > f.size-f.tail {
		return 0, errTailGone
	}
	return f.File.Read(p)
}

func TestListArchiveInfoTolerateVolumeTailError(t *testing.T) {
	name := "testdata/multi.part1.rar"
	want, err := ListArchiveInfo(name)
	if err != nil {
		t.Fatalf("clean ListArchiveInfo: %v", err)
	}

	// The last volume ends with a completed file, so its tail must be read
	// (another file could follow); an unreadable tail there is fatal unless
	// tolerated.
	broken := tailBrokenFS{tailBytes: 8}

	if _, err := ListArchiveInfo(name, FileSystem(broken)); err == nil {
		t.Fatal("expected an error when volume tails are unreadable and no tolerance is set")
	}

	got, err := ListArchiveInfo(name, FileSystem(broken),
		TolerateVolumeTailError(func(err error) bool { return errors.Is(err, errTailGone) }))
	if err != nil {
		t.Fatalf("ListArchiveInfo with TolerateVolumeTailError: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("file layout differs with tolerated tails:\n got %+v\nwant %+v", got, want)
	}

	// A predicate that rejects the error keeps the failure.
	if _, err := ListArchiveInfo(name, FileSystem(broken),
		TolerateVolumeTailError(func(error) bool { return false })); err == nil {
		t.Fatal("expected the error to propagate when the predicate rejects it")
	}
}

// A volume whose last file block continues into the next volume is finished
// as soon as that header is read: its trailing service and end blocks are
// never fetched, so an unreadable tail there is not even noticed. The fixture's
// part2 ends mid-file2; part3 ends with file2 complete and must still be read.
func TestListArchiveInfoSkipsTailOfContinuedVolumes(t *testing.T) {
	name := "testdata/multi.part1.rar"
	want, err := ListArchiveInfo(name)
	if err != nil {
		t.Fatalf("clean ListArchiveInfo: %v", err)
	}
	continued := tailBrokenFS{tailBytes: 4096, broken: map[string]bool{"testdata/multi.part1.rar": true, "testdata/multi.part2.rar": true}}
	got, err := ListArchiveInfo(name, FileSystem(continued))
	if err != nil {
		t.Fatalf("ListArchiveInfo with unreadable tails on continued volumes: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("file layout differs:\n got %+v\nwant %+v", got, want)
	}

	final := tailBrokenFS{tailBytes: 8, broken: map[string]bool{"testdata/multi.part3.rar": true}}
	if _, err := ListArchiveInfo(name, FileSystem(final)); err == nil {
		t.Fatal("expected an error: the last volume's tail may hold another file header and must be read")
	}
}
