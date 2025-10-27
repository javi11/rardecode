package rardecode

// FilePartInfo represents a single volume part of a file in a RAR archive.
type FilePartInfo struct {
	Path          string `json:"path"`                    // Full path to the volume file
	DataOffset    int64  `json:"dataOffset"`              // Byte offset where the file data starts in the volume
	PackedSize    int64  `json:"packedSize"`              // Size of packed data in this volume part
	UnpackedSize  int64  `json:"unpackedSize"`            // Total unpacked size of the complete file
	Stored        bool   `json:"stored"`                  // True if data is stored (not compressed)
	Encrypted     bool   `json:"encrypted"`               // True if this part is encrypted
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
		}

		// Process each block (volume part)
		for _, block := range blockList {
			// Get the full path to the volume file
			volumePath := vm.GetVolumePath(block.volnum)

			// Determine if this part is stored (not compressed)
			stored := block.decVer == 0

			// Check encryption
			encrypted := block.Encrypted

			// Create part info
			partInfo := FilePartInfo{
				Path:         volumePath,
				DataOffset:   block.dataOff,
				PackedSize:   block.PackedSize,
				UnpackedSize: block.UnPackedSize,
				Stored:       stored,
				Encrypted:    encrypted,
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
