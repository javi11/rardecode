package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/javi11/rardecode/v2"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <rar-file>\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "\nExample:\n")
		fmt.Fprintf(os.Stderr, "  %s archive.rar\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s archive.part1.rar\n", os.Args[0])
		os.Exit(1)
	}

	filename := os.Args[1]

	// Get archive information
	fileInfos, err := rardecode.ListArchiveInfo(filename)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Marshal to pretty JSON
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(fileInfos); err != nil {
		fmt.Fprintf(os.Stderr, "Error encoding JSON: %v\n", err)
		os.Exit(1)
	}
}
