package rardecode

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// TestReadBlockHeader_MalformedSize tests that malformed RAR5 block headers
// with invalid size values return ErrCorruptBlockHeader instead of panicking.
// This test verifies the fix for the panic that occurred when size < len(b),
// which would cause the buffer allocation to create a buffer smaller than 3 bytes,
// leading to a panic when accessing buf[3:].
func TestReadBlockHeader_MalformedSize(t *testing.T) {
	tests := []struct {
		name        string
		createBytes func() []byte
		wantErr     error
	}{
		{
			name: "size=1 with uvarint consuming 1 byte causes size < len(b)",
			createBytes: func() []byte {
				// After reading CRC (4 bytes), we have 3 bytes left.
				// uvarint of 0x01 consumes 1 byte, leaving len(b)=2
				// With size=1 and len(b)=2, we have size < len(b)
				// This should trigger our validation check
				buf := make([]byte, 7)
				binary.LittleEndian.PutUint32(buf[0:4], 0x12345678) // CRC
				buf[4] = 0x01                                        // size=1 (uvarint)
				buf[5] = 0x00
				buf[6] = 0x00
				return buf
			},
			wantErr: ErrCorruptBlockHeader,
		},
		{
			name: "size=0 when uvarint returns 0",
			createBytes: func() []byte {
				// When uvarint finds only continuation bytes (0x80+), it returns 0
				// With size=0 and len(b)=0, size < len(b) is false, so this won't
				// trigger our validation. But it will fail later for other reasons.
				// This test case verifies we handle size=0 gracefully.
				buf := make([]byte, 7)
				binary.LittleEndian.PutUint32(buf[0:4], 0xABCDEF00) // CRC
				buf[4] = 0x80 // All continuation bytes
				buf[5] = 0x80
				buf[6] = 0x80
				return buf
			},
			wantErr: ErrBadHeaderCRC, // Will fail CRC check since size=0 means empty header
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create an archive50 instance
			a := &archive50{}

			// Create a reader from the malformed bytes
			data := tt.createBytes()
			r := &bufVolumeReader{
				buf: make([]byte, defaultBufSize),
			}
			r.r = bytes.NewReader(data)

			// This should not panic, but return an error
			_, err := a.readBlockHeader(r)

			// Verify we get an error (the specific error may vary)
			if err != tt.wantErr {
				t.Errorf("readBlockHeader() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

// TestReadBlockHeader_NonPanicOnLargeSize verifies that headers with valid
// size values (size >= len(b)) don't panic, even if other aspects are invalid.
func TestReadBlockHeader_NonPanicOnLargeSize(t *testing.T) {
	// This test verifies the fix doesn't break normal operation.
	// We create a header where size >= len(b), so the validation passes.
	// Even if the CRC is wrong, it should return an error, not panic.

	// Create header data: CRC(4) + size(1) + some bytes
	// After reading CRC, we have 3 bytes left in sizeBuf.
	// After reading size=5 (1 byte), len(b)=2.
	// Since size(5) >= len(b)(2), validation passes.
	data := make([]byte, 20) // Plenty of data to avoid EOF
	binary.LittleEndian.PutUint32(data[0:4], 0x12345678) // CRC (will be wrong)
	data[4] = 0x05                                        // size=5
	data[5] = 0x01                                        // htype
	data[6] = 0x00                                        // flags
	// Rest is zeros

	// Create reader
	a := &archive50{}
	r := &bufVolumeReader{
		buf: make([]byte, defaultBufSize),
	}
	r.r = bytes.NewReader(data)

	// This should NOT panic. It may return an error (like ErrBadHeaderCRC),
	// but it should not crash.
	h, err := a.readBlockHeader(r)

	// We expect an error (bad CRC), but the important thing is no panic
	if err == nil {
		t.Logf("Got header: htype=%v, flags=%v (unexpected success with dummy CRC)", h.htype, h.flags)
	} else if err != ErrBadHeaderCRC {
		t.Logf("Got error: %v (expected ErrBadHeaderCRC, but no panic is good)", err)
	}

	// The test passes as long as we didn't panic
}
