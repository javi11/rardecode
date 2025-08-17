# Multivolume RAR Headers API

This document describes the new API for reading all file headers from multivolume RAR archives, inspired by the sharpcompress `headerFactory.ReadHeaders` method.

## New Features

### Extended FileHeader

The `FileHeader` struct now includes volume metadata:

```go
type FileHeader struct {
    // ... existing fields ...
    VolumeNumber int  // which volume this header came from
    PartNumber   int  // for files spanning multiple volumes (0-based)
    TotalParts   int  // total parts for this file across volumes
}
```

### Standalone Functions

#### ReadHeaders - One Header Per File
Read all file headers from all volumes (one header per file):

```go
headers, err := rardecode.ReadHeaders("archive.part1.rar")
if err != nil {
    log.Fatal(err)
}

for _, header := range headers {
    fmt.Printf("File: %s (Volume %d, Part %d/%d)\n", 
        header.Name, header.VolumeNumber, 
        header.PartNumber+1, header.TotalParts)
}
```

#### ReadAllHeaders - All Block Headers
Read ALL block headers from ALL volumes (one header per block/part):

```go
allHeaders, err := rardecode.ReadAllHeaders("archive.part1.rar")
if err != nil {
    log.Fatal(err)
}

for _, header := range allHeaders {
    fmt.Printf("Block: %s (Volume %d, Part %d/%d, Offset %d)\n", 
        header.Name, header.VolumeNumber, 
        header.PartNumber+1, header.TotalParts, header.Offset)
}
```

### ReadCloser Methods

For more advanced scenarios with open archives:

```go
rc, err := rardecode.OpenReader("archive.part1.rar")
if err != nil {
    log.Fatal(err)
}
defer rc.Close()

// Get file headers (one per file)
headers, err := rc.ReadHeaders()
if err != nil {
    log.Fatal(err)
}

// Get ALL block headers (one per block/part)
allHeaders, err := rc.ReadAllHeaders()
if err != nil {
    log.Fatal(err)
}

// Get file headers grouped by volume
volumeHeaders, err := rc.VolumeHeaders()
if err != nil {
    log.Fatal(err)
}

// Get ALL block headers grouped by volume
volumeAllHeaders, err := rc.VolumeAllHeaders()
if err != nil {
    log.Fatal(err)
}

for volumeNum, headers := range volumeAllHeaders {
    fmt.Printf("Volume %d contains %d blocks\n", volumeNum, len(headers))
}
```

### Reader.ReadHeaders

For single volume archives (returns error for multivolume):

```go
reader, err := rardecode.NewReader(file)
if err != nil {
    log.Fatal(err)
}

// This will return an error suggesting to use OpenReader for multivolume
headers, err := reader.ReadHeaders()
```

## Benefits

- **Efficient**: Reads only headers, no file content extraction
- **Complete**: Access to all files across all volumes
- **Metadata-rich**: Includes volume and part information
- **Compatible**: Similar API to sharpcompress headerFactory.ReadHeaders
- **Flexible**: Multiple ways to access header information

## Use Cases

- Archive analysis and cataloging
- Building file indexes
- Checking archive integrity metadata
- Volume management utilities
- Archive exploration tools