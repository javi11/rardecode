package main

import (
	"fmt"
	"log"
	"os"

	"github.com/javi11/rardecode/v2"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: headers <rar_file> [--all-blocks]")
		fmt.Println("Example: headers archive.part1.rar")
		fmt.Println("         headers archive.part1.rar --all-blocks")
		os.Exit(1)
	}

	filename := os.Args[1]
	showAllBlocks := len(os.Args) > 2 && os.Args[2] == "--all-blocks"

	if showAllBlocks {
		// Use ReadAllHeaders to get EVERY block header from ALL volumes
		fmt.Println("=== ALL BLOCK HEADERS (every block/part) ===")
		allHeaders, err := rardecode.ReadAllHeaders(filename)
		if err != nil {
			log.Fatalf("Error reading all headers: %v", err)
		}

		fmt.Printf("Archive: %s\n", filename)
		fmt.Printf("Total blocks: %d\n\n", len(allHeaders))

		// Group blocks by volume
		volumeBlocks := make(map[int][]*rardecode.FileHeader)
		for _, header := range allHeaders {
			volumeBlocks[header.VolumeNumber] = append(volumeBlocks[header.VolumeNumber], header)
		}

		// Display volume information
		for volumeNum := range volumeBlocks {
			fmt.Printf("Volume %d: %d blocks\n", volumeNum, len(volumeBlocks[volumeNum]))
		}
		fmt.Println()

		// Display detailed block information
		for _, header := range allHeaders {
			fmt.Printf("Block: %s (Part %d/%d)\n", header.Name, header.PartNumber+1, header.TotalParts)
			fmt.Printf("  Volume: %d, Offset: %d\n", header.VolumeNumber, header.Offset)
			if !header.IsDir {
				fmt.Printf("  Size: %d bytes (packed: %d)\n", header.UnPackedSize, header.PackedSize)
			}
			fmt.Println()
		}
	} else {
		// Use the regular ReadHeaders API to get one header per file
		fmt.Println("=== FILE HEADERS (one per file) ===")
		headers, err := rardecode.ReadHeaders(filename)
		if err != nil {
			log.Fatalf("Error reading headers: %v", err)
		}

		fmt.Printf("Archive: %s\n", filename)
		fmt.Printf("Total files: %d\n\n", len(headers))

		// Group files by volume
		volumeFiles := make(map[int][]*rardecode.FileHeader)
		for _, header := range headers {
			volumeFiles[header.VolumeNumber] = append(volumeFiles[header.VolumeNumber], header)
		}

		// Display volume information
		for volumeNum := range volumeFiles {
			fmt.Printf("Volume %d: %d files\n", volumeNum, len(volumeFiles[volumeNum]))
		}
		fmt.Println()

		// Display detailed file information
		for _, header := range headers {
			fmt.Printf("File: %s\n", header.Name)
			if header.IsDir {
				fmt.Printf("  Type: Directory\n")
			} else {
				fmt.Printf("  Type: File\n")
				fmt.Printf("  Size: %d bytes (packed: %d)\n", header.UnPackedSize, header.PackedSize)
			}
			fmt.Printf("  Volume: %d, Part: %d/%d\n", 
				header.VolumeNumber, header.PartNumber+1, header.TotalParts)
			fmt.Printf("  Modified: %s\n", header.ModificationTime.Format("2006-01-02 15:04:05"))
			if header.Encrypted {
				fmt.Printf("  Encrypted: Yes\n")
			}
			fmt.Println()
		}
	}
}