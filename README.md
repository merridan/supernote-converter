# Supernote Converter

A command-line tool for converting Supernote `.note` files to PNG images. 

## Features

- **Batch Processing**: Automatically finds and processes all `.note` files recursively
- **All Pages**: Converts every page in every note file automatically
- **Organized Output**: Creates subdirectories for each note file with numbered PNG pages

## Installation

### Download Pre-built Binaries

Download the latest release for your platform from the [Releases page](https://github.com/merridan/supernote-converter/releases):

- **Linux**: `supernote-tool-linux-amd64` or `supernote-tool-linux-arm64`
- **macOS**: `supernote-tool-darwin-amd64` or `supernote-tool-darwin-arm64`
- **Windows**: `supernote-tool-windows-amd64.exe`

Make the binary executable on Unix systems:
```bash
chmod +x supernote-tool-linux-amd64
```

### Build from Source

Prerequisites:
- Go 1.21 or later

```bash
git clone https://github.com/USERNAME/supernote-tool.git
cd supernote-tool/src
go build -o ../supernote-tool .
```

## Configuration

Create a `config.json` file in the same directory as the executable:

```json
{
  "supernote_path": "/path/to/your/supernote/files"
}
```

This allows you to run the tool without specifying input directories each time.

## Usage

### Basic Usage

Process all `.note` files in a directory:
```bash
./supernote-tool -in /path/to/notes -out-dir /path/to/output
```

### Using Configuration File

If you have `config.json` configured, you can omit the input directory:
```bash
./supernote-tool -out-dir /path/to/output
```

### Performance Tuning

Adjust the number of parallel workers (default: 8):
```bash
./supernote-tool -in /path/to/notes -out-dir /path/to/output -workers 16
```

### Command Line Options

- `-in`: Input directory containing .note files (optional if configured in config.json)
- `-out-dir`: Output directory for PNG files (required)
- `-workers`: Number of parallel processing workers (default: 8)
- `-log-level`: Logging verbosity: debug, info, warn, error (default: info)

## Output Structure

The tool creates an organized directory structure:

```
output/
├── my_note_1/
│   ├── page_000.png
│   ├── page_001.png
│   └── page_002.png
├── my_note_2/
│   ├── page_000.png
│   └── page_001.png
└── subfolder_note/
    ├── page_000.png
    ├── page_001.png
    ├── page_002.png
    └── page_003.png
```

Each `.note` file gets its own subdirectory containing numbered PNG files for each page.

## Examples

### Convert all notes with default settings
```bash
./supernote-tool -in ~/Supernote -out-dir ~/converted_notes
```

### High-performance conversion with 16 workers
```bash
./supernote-tool -in ~/Supernote -out-dir ~/converted_notes -workers 16
```

### Verbose logging for troubleshooting
```bash
./supernote-tool -in ~/Supernote -out-dir ~/converted_notes -log-level debug
```

### Using with config.json
```bash
# First, create config.json:
echo '{"supernote_path": "/Users/myname/Supernote"}' > config.json

# Then run without -in flag:
./supernote-tool -out-dir ~/converted_notes
```
