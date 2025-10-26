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
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [options] <rar-file>\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "\nOptions:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  %s archive.rar\n", os.Args[0])
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

	// Get archive information
	fileInfos, err := rardecode.ListArchiveInfo(filename, opts...)
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
