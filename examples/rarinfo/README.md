# rarinfo

A command-line tool to extract metadata from RAR archives, including file offsets and volume information.

## Usage

```bash
rarinfo <rar-file>
```

The tool outputs JSON with detailed information about each file in the archive.

## Building

```bash
go build -o rarinfo ./examples/rarinfo
```

Or install directly:

```bash
go install github.com/nwaples/rardecode/v2/cmd/rarinfo@latest
```

## Example Output

```json
[
  {
    "name": "example.mkv",
    "totalPackedSize": 186315332,
    "totalUnpackedSize": 2463549442,
    "parts": [
      {
        "path": "/path/to/archive.rar",
        "dataOffset": 102,
        "packedSize": 19999878,
        "unpackedSize": 2463549442,
        "stored": true,
        "encrypted": false
      },
      {
        "path": "/path/to/archive.r00",
        "dataOffset": 102,
        "packedSize": 19999878,
        "unpackedSize": 2463549442,
        "stored": true,
        "encrypted": false
      }
    ],
    "anyEncrypted": false,
    "allStored": true
  }
]
```

## Output Fields

### File Level

- `name`: Filename inside the archive
- `totalPackedSize`: Sum of compressed sizes across all volume parts
- `totalUnpackedSize`: Total uncompressed size
- `parts`: Array of volume parts (see below)
- `anyEncrypted`: True if any part is encrypted
- `allStored`: True if file is stored without compression

### Part Level (each volume)

- `path`: Full path to the volume file
- `dataOffset`: Byte offset where file data begins in the volume
- `packedSize`: Compressed data size in this volume
- `unpackedSize`: Total uncompressed file size (from file header)
- `stored`: True if not compressed (stored)
- `encrypted`: True if encrypted

## Use Cases

- Analyze multi-volume RAR archives
- Get exact file offsets for direct access
- Determine compression and encryption status
- Understand archive structure without extraction

## Notes

- Works best with stored (uncompressed) files
- For compressed/encrypted files, metadata is provided but validation may not be possible
- Multi-volume archives are fully supported
