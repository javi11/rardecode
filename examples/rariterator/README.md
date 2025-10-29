# RAR Iterator Example

A command-line tool demonstrating the use of `ArchiveIterator` for memory-efficient sequential access to RAR archive files.

## Features

- **Memory Efficient**: Uses iterator pattern - only loads one file at a time
- **Search Support**: Find specific files by name
- **Size Filtering**: Filter files by minimum or maximum size
- **JSON Output**: Machine-readable JSON output option
- **Password Support**: Works with encrypted archives
- **Multi-Volume Support**: Handles split/multi-volume archives
- **Detailed Information**: Shows compression, encryption, and volume details

## Building

```bash
go build
```

## Usage

### List all files in an archive

```bash
./rariterator archive.rar
```

### Search for a specific file

```bash
./rariterator -search document.txt archive.rar
```

### Find large files (> 10MB)

```bash
./rariterator -max-size 10485760 archive.rar
```

### Find small files (< 100KB)

```bash
./rariterator -min-size 102400 archive.rar
```

### Output as JSON

```bash
./rariterator -json archive.rar
```

### Encrypted archives

```bash
./rariterator -password mysecret encrypted.rar
```

### Combine filters

```bash
./rariterator -password secret -max-size 5242880 -json archive.rar
```

## Options

- `-password string`: Password for encrypted archives
- `-search string`: Search for a specific file by name (case-sensitive)
- `-max-size int`: Only show files larger than this size in bytes
- `-min-size int`: Only show files smaller than this size in bytes
- `-json`: Output in JSON format

## Why Use Iterator Instead of ListArchiveInfo?

The `ArchiveIterator` is ideal when:

1. **Large Archives**: Archives with hundreds or thousands of files where loading all metadata at once is wasteful
2. **Early Exit**: You're searching for a specific file and can stop once found
3. **Filtering**: You only need files matching certain criteria
4. **Memory Constraints**: Running on systems with limited memory
5. **Streaming**: Processing files as they're discovered rather than after full scan

For small archives or when you need all files anyway, `ListArchiveInfo()` may be more convenient.

## Example Output

```
Found 3 matching file(s) (out of 10 total):

[1] documents/report.pdf
    Size: 2458624 bytes (2.34 MB)
    Packed: 2401856 bytes (2.3% compression)
    Compression: rar5.0
    Parts: 1 volume(s)

[2] images/photo.jpg
    Size: 5242880 bytes (5.00 MB)
    Packed: 5238142 bytes (0.1% compression)
    Compression: stored
    📦 Stored: Yes (no compression)
    Parts: 1 volume(s)

[3] encrypted/secret.txt
    Size: 1024 bytes (0.00 MB)
    Packed: 1024 bytes (0.0% compression)
    Compression: stored
    🔒 Encrypted: Yes
    Parts: 1 volume(s)

=== Summary ===
Matching files: 3
Total size: 7702528 bytes (7.35 MB)
```

## See Also

- `../rarinfo` - Lists all files using `ListArchiveInfo()` (loads all at once)
- `../rarextract` - Extracts files from RAR archives
