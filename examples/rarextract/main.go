package main

import (
	"crypto/aes"
	"crypto/cipher"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/javi11/rardecode/v2"
)

// aesDecryptReader implements AES-CBC decryption with seeking support
type aesDecryptReader struct {
	source    io.ReadSeeker
	key       []byte
	iv        []byte
	decrypter cipher.BlockMode
	blockSize int
	buffer    []byte // buffer for partial blocks
	offset    int64  // current position in decrypted stream
	size      int64  // total size of encrypted data
}

func newAesDecryptReader(source io.ReadSeeker, key, iv []byte, size int64) (*aesDecryptReader, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("failed to create AES cipher: %w", err)
	}

	reader := &aesDecryptReader{
		source:    source,
		key:       key,
		iv:        make([]byte, len(iv)),
		blockSize: aes.BlockSize,
		buffer:    make([]byte, 0),
		offset:    0,
		size:      size,
	}
	copy(reader.iv, iv)
	reader.decrypter = cipher.NewCBCDecrypter(block, reader.iv)

	return reader, nil
}

func (r *aesDecryptReader) Read(p []byte) (n int, err error) {
	if r.offset >= r.size {
		return 0, io.EOF
	}

	// First, drain any buffered data
	if len(r.buffer) > 0 {
		n = copy(p, r.buffer)
		r.buffer = r.buffer[n:]
		r.offset += int64(n)
		return n, nil
	}

	// Calculate how much we can read (respecting the size limit)
	remaining := r.size - r.offset
	if int64(len(p)) > remaining {
		p = p[:remaining]
	}

	// Round down to block size
	readSize := (len(p) / r.blockSize) * r.blockSize
	if readSize == 0 && len(p) > 0 {
		// Need to read at least one block for small reads
		readSize = r.blockSize
	}

	if readSize == 0 {
		return 0, io.EOF
	}

	// Read encrypted data
	encrypted := make([]byte, readSize)
	n, err = io.ReadFull(r.source, encrypted)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return 0, fmt.Errorf("failed to read encrypted data: %w", err)
	}

	// Adjust to block boundary
	n = (n / r.blockSize) * r.blockSize
	if n == 0 {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return 0, io.EOF
		}
		return 0, err
	}

	encrypted = encrypted[:n]

	// Decrypt in place
	r.decrypter.CryptBlocks(encrypted, encrypted)

	// Handle the case where we decrypted more than needed
	toCopy := len(p)
	if toCopy > n {
		toCopy = n
	}

	copied := copy(p, encrypted[:toCopy])
	r.offset += int64(copied)

	// Buffer any remaining decrypted data
	if toCopy < n {
		r.buffer = append(r.buffer, encrypted[toCopy:]...)
	}

	return copied, nil
}

func (r *aesDecryptReader) Seek(offset int64, whence int) (int64, error) {
	var newOffset int64

	switch whence {
	case io.SeekStart:
		newOffset = offset
	case io.SeekCurrent:
		newOffset = r.offset + offset
	case io.SeekEnd:
		newOffset = r.size + offset
	default:
		return 0, fmt.Errorf("invalid whence: %d", whence)
	}

	if newOffset < 0 {
		return 0, fmt.Errorf("negative position: %d", newOffset)
	}

	if newOffset > r.size {
		newOffset = r.size
	}

	// Clear buffer when seeking
	r.buffer = r.buffer[:0]

	// Calculate which block we need to be at
	blockIndex := newOffset / int64(r.blockSize)
	blockOffset := newOffset % int64(r.blockSize)

	// Seek to the start of the block in source
	sourceOffset := blockIndex * int64(r.blockSize)
	if _, err := r.source.Seek(sourceOffset, io.SeekStart); err != nil {
		return 0, fmt.Errorf("failed to seek source: %w", err)
	}

	// Recalculate IV for CBC mode
	// For CBC, the IV for block N is the ciphertext of block N-1
	if blockIndex == 0 {
		// First block uses original IV
		copy(r.iv, r.iv[:r.blockSize])
	} else {
		// Read the previous block as the new IV
		prevBlockOffset := (blockIndex - 1) * int64(r.blockSize)
		if _, err := r.source.Seek(prevBlockOffset, io.SeekStart); err != nil {
			return 0, fmt.Errorf("failed to seek to previous block: %w", err)
		}

		newIV := make([]byte, r.blockSize)
		if _, err := io.ReadFull(r.source, newIV); err != nil {
			return 0, fmt.Errorf("failed to read previous block for IV: %w", err)
		}

		copy(r.iv, newIV)

		// Seek back to the target block
		if _, err := r.source.Seek(sourceOffset, io.SeekStart); err != nil {
			return 0, fmt.Errorf("failed to seek back to target block: %w", err)
		}
	}

	// Recreate decrypter with new IV
	block, err := aes.NewCipher(r.key)
	if err != nil {
		return 0, fmt.Errorf("failed to recreate cipher: %w", err)
	}
	r.decrypter = cipher.NewCBCDecrypter(block, r.iv)

	r.offset = blockIndex * int64(r.blockSize)

	// If we need to be at an offset within the block, read and discard
	if blockOffset > 0 {
		discard := make([]byte, blockOffset)
		if _, err := io.ReadFull(r, discard); err != nil {
			return 0, fmt.Errorf("failed to read to offset within block: %w", err)
		}
	}

	return r.offset, nil
}

// multiPartReader reads sequentially from multiple RAR volume parts
type multiPartReader struct {
	parts       []rardecode.FilePartInfo
	currentPart int
	currentFile *os.File
	partOffset  int64  // offset within current part
	totalOffset int64  // total offset across all parts
	totalSize   int64  // total size across all parts
}

func newMultiPartReader(parts []rardecode.FilePartInfo) (*multiPartReader, error) {
	if len(parts) == 0 {
		return nil, fmt.Errorf("no parts provided")
	}

	var totalSize int64
	for _, part := range parts {
		totalSize += part.PackedSize
	}

	r := &multiPartReader{
		parts:       parts,
		currentPart: -1,
		totalSize:   totalSize,
	}

	// Open first part
	if err := r.openPart(0); err != nil {
		return nil, err
	}

	return r, nil
}

func (r *multiPartReader) openPart(partIndex int) error {
	// Close current file if open
	if r.currentFile != nil {
		r.currentFile.Close()
		r.currentFile = nil
	}

	if partIndex < 0 || partIndex >= len(r.parts) {
		return io.EOF
	}

	part := r.parts[partIndex]

	// Open volume file
	f, err := os.Open(part.Path)
	if err != nil {
		return fmt.Errorf("failed to open volume %s: %w", part.Path, err)
	}

	// Seek to data offset
	if _, err := f.Seek(part.DataOffset, io.SeekStart); err != nil {
		f.Close()
		return fmt.Errorf("failed to seek to data offset: %w", err)
	}

	r.currentFile = f
	r.currentPart = partIndex
	r.partOffset = 0

	return nil
}

func (r *multiPartReader) Read(p []byte) (n int, err error) {
	if r.currentPart >= len(r.parts) {
		return 0, io.EOF
	}

	for n < len(p) && r.currentPart < len(r.parts) {
		part := r.parts[r.currentPart]
		remaining := part.PackedSize - r.partOffset

		if remaining <= 0 {
			// Move to next part
			if err := r.openPart(r.currentPart + 1); err != nil {
				if err == io.EOF && n > 0 {
					return n, nil
				}
				return n, err
			}
			continue
		}

		// Read from current part
		toRead := int64(len(p) - n)
		if toRead > remaining {
			toRead = remaining
		}

		readBuf := p[n : n+int(toRead)]
		nr, err := r.currentFile.Read(readBuf)
		n += nr
		r.partOffset += int64(nr)
		r.totalOffset += int64(nr)

		if err != nil {
			if err == io.EOF {
				// End of current part, try next part
				if r.currentPart+1 < len(r.parts) {
					if openErr := r.openPart(r.currentPart + 1); openErr != nil {
						return n, openErr
					}
					continue
				}
			}
			return n, err
		}
	}

	return n, nil
}

func (r *multiPartReader) Seek(offset int64, whence int) (int64, error) {
	var newOffset int64

	switch whence {
	case io.SeekStart:
		newOffset = offset
	case io.SeekCurrent:
		newOffset = r.totalOffset + offset
	case io.SeekEnd:
		newOffset = r.totalSize + offset
	default:
		return 0, fmt.Errorf("invalid whence: %d", whence)
	}

	if newOffset < 0 {
		return 0, fmt.Errorf("negative position: %d", newOffset)
	}

	if newOffset > r.totalSize {
		newOffset = r.totalSize
	}

	// Find which part contains this offset
	var partStartOffset int64
	targetPart := -1
	for i, part := range r.parts {
		if newOffset >= partStartOffset && newOffset < partStartOffset+part.PackedSize {
			targetPart = i
			break
		}
		partStartOffset += part.PackedSize
	}

	if targetPart == -1 {
		// Seeking to end
		if err := r.openPart(len(r.parts)); err != nil && err != io.EOF {
			return 0, err
		}
		r.totalOffset = newOffset
		return r.totalOffset, nil
	}

	// Open the target part if not already open
	if r.currentPart != targetPart {
		if err := r.openPart(targetPart); err != nil {
			return 0, err
		}
	}

	// Seek within the part
	offsetInPart := newOffset - partStartOffset
	part := r.parts[targetPart]
	absoluteOffset := part.DataOffset + offsetInPart

	if _, err := r.currentFile.Seek(absoluteOffset, io.SeekStart); err != nil {
		return 0, fmt.Errorf("failed to seek within part: %w", err)
	}

	r.partOffset = offsetInPart
	r.totalOffset = newOffset

	return r.totalOffset, nil
}

func (r *multiPartReader) Close() error {
	if r.currentFile != nil {
		err := r.currentFile.Close()
		r.currentFile = nil
		return err
	}
	return nil
}

func main() {
	// Define flags
	password := flag.String("password", "", "Password for encrypted archives")
	output := flag.String("output", ".", "Output directory for extracted files")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [options] <rar-file>\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "\nOptions:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  %s archive.rar\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s --password secret archive.rar\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s --output ./extracted --password secret archive.part1.rar\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "\nNote: This example only supports stored (uncompressed) files.\n")
		fmt.Fprintf(os.Stderr, "Compressed files will be skipped with a warning.\n")
	}
	flag.Parse()

	if flag.NArg() < 1 {
		flag.Usage()
		os.Exit(1)
	}

	filename := flag.Arg(0)

	// Create output directory if it doesn't exist
	if err := os.MkdirAll(*output, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating output directory: %v\n", err)
		os.Exit(1)
	}

	// Prepare options
	var opts []rardecode.Option
	if *password != "" {
		opts = append(opts, rardecode.Password(*password))
	}

	// Get archive information using ListArchiveInfo
	fmt.Println("Reading archive metadata...")
	fileInfos, err := rardecode.ListArchiveInfo(filename, opts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading archive info: %v\n", err)
		os.Exit(1)
	}

	if len(fileInfos) == 0 {
		fmt.Println("No files found in archive")
		return
	}

	// Extract files
	filesExtracted := 0
	filesSkipped := 0
	var totalBytes int64

	for _, fileInfo := range fileInfos {
		fmt.Printf("\nProcessing: %s\n", fileInfo.Name)

		// Check if file is stored (not compressed)
		if !fileInfo.AllStored {
			fmt.Printf("  ⚠️  Skipping (compressed files not supported in this example)\n")
			filesSkipped++
			continue
		}

		// Check if file is encrypted
		if fileInfo.AnyEncrypted {
			// Verify we have encryption keys
			hasKeys := false
			for _, part := range fileInfo.Parts {
				if part.Encrypted && len(part.AesKey) > 0 {
					hasKeys = true
					break
				}
			}
			if !hasKeys {
				fmt.Printf("  ⚠️  Skipping (encrypted but no password provided)\n")
				filesSkipped++
				continue
			}
			fmt.Printf("  🔒 Encrypted (will decrypt)\n")
		}

		// Prepare output file
		outputPath := filepath.Join(*output, filepath.Base(fileInfo.Name))
		outFile, err := os.Create(outputPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ❌ Error creating file: %v\n", err)
			continue
		}

		// Print parts info
		for i, part := range fileInfo.Parts {
			fmt.Printf("  Part %d/%d: %s (offset: %d, size: %d bytes)\n",
				i+1, len(fileInfo.Parts), filepath.Base(part.Path), part.DataOffset, part.PackedSize)
		}

		// Create multi-part reader for all parts
		multiReader, err := newMultiPartReader(fileInfo.Parts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ❌ Error creating multi-part reader: %v\n", err)
			outFile.Close()
			os.Remove(outputPath)
			continue
		}

		var dataReader io.Reader = multiReader

		// If encrypted, wrap with single decrypt reader
		if fileInfo.AnyEncrypted {
			// Use the first part's key and IV (all parts have the same key/IV)
			firstPart := fileInfo.Parts[0]
			var totalSize int64
			for _, part := range fileInfo.Parts {
				totalSize += part.PackedSize
			}

			decryptReader, err := newAesDecryptReader(multiReader, firstPart.AesKey, firstPart.AesIV, totalSize)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  ❌ Error creating decrypt reader: %v\n", err)
				multiReader.Close()
				outFile.Close()
				os.Remove(outputPath)
				continue
			}
			dataReader = decryptReader
		}

		// Copy data to output
		fileBytes, err := io.Copy(outFile, dataReader)
		multiReader.Close()
		outFile.Close()

		if err != nil {
			fmt.Fprintf(os.Stderr, "  ❌ Error extracting file: %v\n", err)
			os.Remove(outputPath)
			continue
		}

		fmt.Printf("  ✅ Extracted: %d bytes\n", fileBytes)
		filesExtracted++
		totalBytes += fileBytes
	}

	// Print summary
	fmt.Printf("\n" + "=== Summary ===\n")
	fmt.Printf("Files extracted: %d\n", filesExtracted)
	fmt.Printf("Files skipped: %d\n", filesSkipped)
	fmt.Printf("Total bytes: %d\n", totalBytes)
}
