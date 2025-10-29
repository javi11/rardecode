package rardecode

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

// TestFindSig_NonRARFile tests that non-RAR files return ErrNoSig instead of hanging
func TestFindSig_NonRARFile(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr error
	}{
		{
			name:    "plain text file",
			content: "This is not a RAR file, just some plain text content.",
			wantErr: ErrNoSig,
		},
		{
			name:    "empty file",
			content: "",
			wantErr: ErrNoSig,
		},
		{
			name:    "file with partial signature (only R)",
			content: "Random content with the letter R but not the full RAR signature",
			wantErr: ErrNoSig,
		},
		{
			name:    "file with Rar prefix but wrong suffix",
			content: "Rar!XXXX this has Rar! but not the correct signature",
			wantErr: ErrNoSig,
		},
		{
			name:    "large file without signature",
			content: strings.Repeat("Not a RAR file content. ", 10000), // ~250KB
			wantErr: ErrNoSig,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := strings.NewReader(tt.content)
			br := &bufVolumeReader{
				buf: make([]byte, defaultBufSize),
			}

			err := br.Reset(r)

			if !errors.Is(err, tt.wantErr) {
				t.Errorf("Reset() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

// TestFindSig_ValidRAR tests that valid RAR signatures are correctly detected
// Note: These tests use minimal valid signatures. Real RAR files have more structure.
func TestFindSig_ValidRAR(t *testing.T) {
	tests := []struct {
		name       string
		content    []byte
		wantVer    int
		wantErr    bool
	}{
		{
			name:    "RAR v5.0 signature (minimal valid)",
			content: []byte("Rar!\x1A\x07\x01\x00"),
			wantVer: 1,
			wantErr: false,
		},
		{
			name:    "RAR signature with SFX prefix",
			content: append(bytes.Repeat([]byte("X"), 1000), []byte("Rar!\x1A\x07\x01\x00")...),
			wantVer: 1,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := bytes.NewReader(tt.content)
			br := &bufVolumeReader{
				buf: make([]byte, defaultBufSize),
			}

			err := br.Reset(r)

			if tt.wantErr && err == nil {
				t.Error("Reset() expected error but got none")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("Reset() unexpected error = %v", err)
			}
			if err == nil && br.ver != tt.wantVer {
				t.Errorf("Reset() version = %d, want %d", br.ver, tt.wantVer)
			}
		})
	}
}

// TestBufVolumeReader_Read tests basic read functionality
func TestBufVolumeReader_Read(t *testing.T) {
	content := []byte("Rar!\x1A\x07\x01\x00Test content after signature")
	r := bytes.NewReader(content)

	br, err := newBufVolumeReader(r, 0)
	if err != nil {
		t.Fatalf("newBufVolumeReader() error = %v", err)
	}

	// Read some bytes
	buf := make([]byte, 4)
	n, err := br.Read(buf)
	if err != nil {
		t.Errorf("Read() error = %v", err)
	}
	if n != 4 {
		t.Errorf("Read() read %d bytes, want 4", n)
	}
}

// TestBufVolumeReader_ReadByte tests byte-by-byte reading
func TestBufVolumeReader_ReadByte(t *testing.T) {
	content := []byte("Rar!\x1A\x07\x01\x00ABC")
	r := bytes.NewReader(content)

	br, err := newBufVolumeReader(r, 0)
	if err != nil {
		t.Fatalf("newBufVolumeReader() error = %v", err)
	}

	// Read first byte after signature
	b, err := br.ReadByte()
	if err != nil {
		t.Errorf("ReadByte() error = %v", err)
	}
	if b != 'A' {
		t.Errorf("ReadByte() = %c, want A", b)
	}
}

// TestBufVolumeReader_Discard tests the discard functionality
func TestBufVolumeReader_Discard(t *testing.T) {
	content := []byte("Rar!\x1A\x07\x01\x00" + strings.Repeat("X", 1000))
	r := bytes.NewReader(content)

	br, err := newBufVolumeReader(r, 0)
	if err != nil {
		t.Fatalf("newBufVolumeReader() error = %v", err)
	}

	// Discard 100 bytes
	err = br.Discard(100)
	if err != nil {
		t.Errorf("Discard() error = %v", err)
	}

	// Verify position advanced
	b, err := br.ReadByte()
	if err != nil {
		t.Errorf("ReadByte() after Discard() error = %v", err)
	}
	if b != 'X' {
		t.Errorf("ReadByte() after Discard() = %c, want X", b)
	}
}

// TestBufVolumeReader_WriteToN tests writing to another writer
func TestBufVolumeReader_WriteToN(t *testing.T) {
	content := []byte("Rar!\x1A\x07\x01\x00TestData")
	r := bytes.NewReader(content)

	br, err := newBufVolumeReader(r, 0)
	if err != nil {
		t.Fatalf("newBufVolumeReader() error = %v", err)
	}

	// Write 4 bytes to buffer
	var buf bytes.Buffer
	n, err := br.writeToN(&buf, 4)
	if err != nil {
		t.Errorf("writeToN() error = %v", err)
	}
	if n != 4 {
		t.Errorf("writeToN() wrote %d bytes, want 4", n)
	}
	if buf.String() != "Test" {
		t.Errorf("writeToN() content = %q, want %q", buf.String(), "Test")
	}
}

// TestBufVolumeReader_Seek tests seeking functionality
func TestBufVolumeReader_Seek(t *testing.T) {
	content := []byte("Rar!\x1A\x07\x01\x00" + strings.Repeat("ABCD", 100))
	r := bytes.NewReader(content)

	br, err := newBufVolumeReader(r, 0)
	if err != nil {
		t.Fatalf("newBufVolumeReader() error = %v", err)
	}

	if !br.canSeek() {
		t.Skip("Reader doesn't support seeking")
	}

	// Seek to position 10
	err = br.seek(10)
	if err != nil {
		t.Errorf("seek() error = %v", err)
	}

	// Verify we're at the right position
	b, err := br.ReadByte()
	if err != nil {
		t.Errorf("ReadByte() after seek() error = %v", err)
	}
	// Position 10 should be in the repeated "ABCD" pattern after signature
	expected := content[10]
	if b != expected {
		t.Errorf("ReadByte() after seek(10) = %c, want %c", b, expected)
	}
}

// TestFindSig_MaxSfxSize tests that signature search respects maxSfxSize limit
func TestFindSig_MaxSfxSize(t *testing.T) {
	// Test 1: Signature within maxSfxSize should be found
	t.Run("signature within limit", func(t *testing.T) {
		content := make([]byte, maxSfxSize-100)
		for i := range content {
			content[i] = 'X'
		}
		// Put signature near end but within maxSfxSize
		content = append(content, []byte("Rar!\x1A\x07\x01\x00")...)

		r := bytes.NewReader(content)
		br := &bufVolumeReader{
			buf: make([]byte, defaultBufSize),
		}

		err := br.Reset(r)
		if err != nil {
			t.Errorf("Reset() with signature within maxSfxSize: unexpected error = %v", err)
		}
	})

	// Test 2: No signature in first maxSfxSize bytes should return ErrNoSig
	t.Run("no signature within limit", func(t *testing.T) {
		// Create content larger than maxSfxSize with no valid signature
		largeContent := make([]byte, maxSfxSize+1000)
		for i := range largeContent {
			largeContent[i] = 'X'
		}

		r := bytes.NewReader(largeContent)
		br := &bufVolumeReader{
			buf: make([]byte, defaultBufSize),
		}

		err := br.Reset(r)

		// Should return ErrNoSig because no signature found within maxSfxSize
		if !errors.Is(err, ErrNoSig) {
			t.Errorf("Reset() with no signature in maxSfxSize: error = %v, want %v", err, ErrNoSig)
		}
	})
}

// TestFindSig_EOFHandling tests EOF handling during signature search
func TestFindSig_EOFHandling(t *testing.T) {
	// File with incomplete signature at EOF
	content := []byte("Some content and then Rar!\x1A")
	r := bytes.NewReader(content)
	br := &bufVolumeReader{
		buf: make([]byte, defaultBufSize),
	}

	err := br.Reset(r)

	// Should return ErrNoSig for incomplete signature
	if !errors.Is(err, ErrNoSig) {
		t.Errorf("Reset() with incomplete signature: error = %v, want %v", err, ErrNoSig)
	}
}

// BenchmarkFindSig_NonRARFile benchmarks the performance of rejecting non-RAR files
func BenchmarkFindSig_NonRARFile(b *testing.B) {
	content := strings.Repeat("Not a RAR file. ", 1000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := strings.NewReader(content)
		br := &bufVolumeReader{
			buf: make([]byte, defaultBufSize),
		}
		_ = br.Reset(r)
	}
}

// BenchmarkFindSig_ValidRAR benchmarks signature detection for valid RAR files
func BenchmarkFindSig_ValidRAR(b *testing.B) {
	content := []byte("Rar!\x1A\x07\x00")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := bytes.NewReader(content)
		br := &bufVolumeReader{
			buf: make([]byte, defaultBufSize),
		}
		_ = br.Reset(r)
	}
}

// TestReadErr tests error handling in readErr
func TestReadErr(t *testing.T) {
	br := &bufVolumeReader{
		buf: make([]byte, defaultBufSize),
		err: io.EOF,
	}

	err := br.readErr()
	if !errors.Is(err, io.EOF) {
		t.Errorf("readErr() = %v, want EOF", err)
	}

	// Second call should return nil (error is consumed)
	err = br.readErr()
	if err != nil {
		t.Errorf("readErr() second call = %v, want nil", err)
	}
}
