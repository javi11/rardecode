package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/javi11/rardecode/v2"
)

func main() {
	// Define flags
	password := flag.String("password", "", "Password for encrypted archives")
	search := flag.String("search", "", "Search for a specific file by name (case-sensitive)")
	maxSize := flag.Int64("max-size", 0, "Only show files larger than this size in bytes")
	minSize := flag.Int64("min-size", 0, "Only show files smaller than this size in bytes")
	jsonOutput := flag.Bool("json", false, "Output in JSON format")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [options] <rar-file>\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "\nDescription:\n")
		fmt.Fprintf(os.Stderr, "  Iterates through files in a RAR archive using memory-efficient iterator.\n")
		fmt.Fprintf(os.Stderr, "  Useful for large archives or when searching for specific files.\n")
		fmt.Fprintf(os.Stderr, "\nOptions:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  # List all files in archive\n")
		fmt.Fprintf(os.Stderr, "  %s archive.rar\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  # Search for a specific file\n")
		fmt.Fprintf(os.Stderr, "  %s -search document.txt archive.rar\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  # Find files larger than 10MB\n")
		fmt.Fprintf(os.Stderr, "  %s -max-size 10485760 archive.rar\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  # Output as JSON\n")
		fmt.Fprintf(os.Stderr, "  %s -json archive.rar\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  # Encrypted archive with password\n")
		fmt.Fprintf(os.Stderr, "  %s -password secret archive.part1.rar\n", os.Args[0])
	}
	flag.Parse()

	if flag.NArg() < 1 {
		flag.Usage()
		os.Exit(1)
	}

	filename := flag.Arg(0)

	// Prepare options
	var opts []rardecode.Option
	if *password != "" {
		opts = append(opts, rardecode.Password(*password))
	}

	// Create iterator
	iter, err := rardecode.NewArchiveIterator(filename, opts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening archive: %v\n", err)
		os.Exit(1)
	}
	defer iter.Close()

	// Collect matching files
	var matchingFiles []rardecode.ArchiveFileInfo
	totalFiles := 0
	totalSize := int64(0)

	// Iterate through files
	for iter.Next() {
		info := iter.FileInfo()
		totalFiles++

		// Apply filters
		if *search != "" && info.Name != *search {
			continue
		}

		if *maxSize > 0 && info.TotalUnpackedSize < *maxSize {
			continue
		}

		if *minSize > 0 && info.TotalUnpackedSize > *minSize {
			continue
		}

		matchingFiles = append(matchingFiles, *info)
		totalSize += info.TotalUnpackedSize
	}

	// Check for iteration errors
	if err := iter.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "Error during iteration: %v\n", err)
		os.Exit(1)
	}

	// Output results
	if *jsonOutput {
		outputJSON(matchingFiles)
	} else {
		outputText(matchingFiles, totalFiles, totalSize)
	}
}

func outputJSON(files []rardecode.ArchiveFileInfo) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(files); err != nil {
		fmt.Fprintf(os.Stderr, "Error encoding JSON: %v\n", err)
		os.Exit(1)
	}
}

func outputText(files []rardecode.ArchiveFileInfo, totalFiles int, totalSize int64) {
	if len(files) == 0 {
		fmt.Println("No matching files found")
		fmt.Printf("Total files in archive: %d\n", totalFiles)
		return
	}

	fmt.Printf("Found %d matching file(s) (out of %d total):\n\n", len(files), totalFiles)

	for i, info := range files {
		fmt.Printf("[%d] %s\n", i+1, info.Name)
		fmt.Printf("    Size: %d bytes (%.2f MB)\n", info.TotalUnpackedSize, float64(info.TotalUnpackedSize)/(1024*1024))
		fmt.Printf("    Packed: %d bytes (%.1f%% compression)\n",
			info.TotalPackedSize,
			100*(1-float64(info.TotalPackedSize)/float64(info.TotalUnpackedSize)))
		fmt.Printf("    Compression: %s\n", info.CompressionMethod)

		if info.AnyEncrypted {
			fmt.Printf("    🔒 Encrypted: Yes\n")
		}

		if info.AllStored {
			fmt.Printf("    📦 Stored: Yes (no compression)\n")
		}

		fmt.Printf("    Parts: %d volume(s)\n", len(info.Parts))

		// Show part details if multi-volume
		if len(info.Parts) > 1 {
			for j, part := range info.Parts {
				fmt.Printf("      Part %d: %s (offset: %d, size: %d bytes)\n",
					j+1, part.Path, part.DataOffset, part.PackedSize)
			}
		}

		fmt.Println()
	}

	fmt.Printf("=== Summary ===\n")
	fmt.Printf("Matching files: %d\n", len(files))
	fmt.Printf("Total size: %d bytes (%.2f MB)\n", totalSize, float64(totalSize)/(1024*1024))
}
