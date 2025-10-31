package rardecode

import (
	"testing"
)

// TestParallelVolumeReaderBasic tests basic parallel reading functionality
func TestParallelVolumeReaderBasic(t *testing.T) {
	// Note: This test requires actual multi-volume RAR archives to test
	// In a real-world scenario, you would have test fixtures
	t.Skip("Requires multi-volume test fixtures")

	name := "testdata/multi.part1.rar"

	// Test with parallel reading
	infosParallel, err := ListArchiveInfoParallel(name)
	if err != nil {
		t.Fatalf("ListArchiveInfoParallel failed: %v", err)
	}

	// Test with sequential reading
	infosSequential, err := ListArchiveInfo(name)
	if err != nil {
		t.Fatalf("ListArchiveInfo failed: %v", err)
	}

	// Compare results
	if len(infosParallel) != len(infosSequential) {
		t.Errorf("File count mismatch: parallel=%d, sequential=%d",
			len(infosParallel), len(infosSequential))
	}

	// Verify each file matches
	for i := range infosParallel {
		if i >= len(infosSequential) {
			break
		}

		p := infosParallel[i]
		s := infosSequential[i]

		if p.Name != s.Name {
			t.Errorf("File %d name mismatch: parallel=%s, sequential=%s", i, p.Name, s.Name)
		}
		if p.TotalPackedSize != s.TotalPackedSize {
			t.Errorf("File %s packed size mismatch: parallel=%d, sequential=%d",
				p.Name, p.TotalPackedSize, s.TotalPackedSize)
		}
		if p.TotalUnpackedSize != s.TotalUnpackedSize {
			t.Errorf("File %s unpacked size mismatch: parallel=%d, sequential=%d",
				p.Name, p.TotalUnpackedSize, s.TotalUnpackedSize)
		}
		if len(p.Parts) != len(s.Parts) {
			t.Errorf("File %s parts count mismatch: parallel=%d, sequential=%d",
				p.Name, len(p.Parts), len(s.Parts))
		}
	}
}

// TestParallelWithMaxConcurrentVolumes tests the MaxConcurrentVolumes option
func TestParallelWithMaxConcurrentVolumes(t *testing.T) {
	t.Skip("Requires multi-volume test fixtures")

	name := "testdata/multi.part1.rar"

	// Test with different concurrency levels
	concurrencyLevels := []int{1, 3, 5, 10}

	var baseResult []ArchiveFileInfo
	for i, level := range concurrencyLevels {
		infos, err := ListArchiveInfoParallel(name, MaxConcurrentVolumes(level))
		if err != nil {
			t.Fatalf("ListArchiveInfoParallel with concurrency %d failed: %v", level, err)
		}

		if i == 0 {
			baseResult = infos
		} else {
			// Verify all concurrency levels produce same results
			if len(infos) != len(baseResult) {
				t.Errorf("Concurrency %d produced different file count: got %d, want %d",
					level, len(infos), len(baseResult))
			}
		}
	}
}

// TestParallelFallbackToSequential tests that parallel reading gracefully falls back
func TestParallelFallbackToSequential(t *testing.T) {
	t.Skip("Requires test fixtures")

	// Test with a single-volume archive (should handle gracefully)
	name := "testdata/single.rar"

	_, err := ListArchiveInfoParallel(name)
	if err != nil {
		t.Fatalf("ListArchiveInfoParallel should handle single-volume archives: %v", err)
	}
}

// TestParallelWithPassword tests parallel reading with encrypted archives
func TestParallelWithPassword(t *testing.T) {
	t.Skip("Requires encrypted multi-volume test fixtures")

	name := "testdata/encrypted.part1.rar"
	password := "test123"

	infos, err := ListArchiveInfoParallel(name, Password(password))
	if err != nil {
		t.Fatalf("ListArchiveInfoParallel with password failed: %v", err)
	}

	if len(infos) == 0 {
		t.Error("Expected files in encrypted archive")
	}

	// Verify encryption info is populated
	for _, info := range infos {
		if info.AnyEncrypted {
			for _, part := range info.Parts {
				if part.Encrypted && len(part.AesKey) == 0 {
					t.Errorf("File %s part has no AES key despite being encrypted", info.Name)
				}
			}
		}
	}
}

// TestParallelVolumeReaderErrorHandling tests error scenarios
func TestParallelVolumeReaderErrorHandling(t *testing.T) {
	// Test with non-existent file
	_, err := ListArchiveInfoParallel("nonexistent.rar")
	if err == nil {
		t.Error("Expected error for non-existent file")
	}

	// Test with invalid archive
	t.Skip("Requires invalid archive test fixture")
}

// TestParallelVolumeDiscovery tests volume count discovery
func TestParallelVolumeDiscovery(t *testing.T) {
	t.Skip("Requires multi-volume test fixtures")

	name := "testdata/multi.part1.rar"

	opts := getOptions([]Option{ParallelRead(true)})
	v, err := openVolume(name, opts)
	if err != nil {
		t.Fatalf("Failed to open volume: %v", err)
	}
	defer v.Close()

	pvr := newParallelVolumeReader(v.vm, opts)
	count := pvr.discoverVolumeCount()

	if count <= 0 {
		t.Errorf("Expected positive volume count, got %d", count)
	}

	t.Logf("Discovered %d volumes", count)
}

// BenchmarkParallelVsSequential benchmarks parallel vs sequential reading
func BenchmarkParallelVsSequential(b *testing.B) {
	b.Skip("Requires multi-volume test fixtures")

	name := "testdata/multi.part1.rar"

	b.Run("Sequential", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_, err := ListArchiveInfo(name)
			if err != nil {
				b.Fatalf("ListArchiveInfo failed: %v", err)
			}
		}
	})

	b.Run("Parallel", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_, err := ListArchiveInfoParallel(name)
			if err != nil {
				b.Fatalf("ListArchiveInfoParallel failed: %v", err)
			}
		}
	})
}

// BenchmarkParallelConcurrencyLevels benchmarks different concurrency levels
func BenchmarkParallelConcurrencyLevels(b *testing.B) {
	b.Skip("Requires multi-volume test fixtures")

	name := "testdata/multi.part1.rar"
	levels := []int{1, 2, 5, 10, 20}

	for _, level := range levels {
		b.Run(string(rune(level)), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_, err := ListArchiveInfoParallel(name, MaxConcurrentVolumes(level))
				if err != nil {
					b.Fatalf("ListArchiveInfoParallel failed: %v", err)
				}
			}
		})
	}
}
