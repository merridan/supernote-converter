package main

import (
	"flag"
	"fmt"
	"image"
	"image/png"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/merridan/sngo/internal/config"
	"github.com/merridan/sngo/internal/logging"
	"github.com/merridan/sngo/internal/note"
)

// findNoteFiles finds all .note files in a directory
func findNoteFiles(dir string, recursive bool) ([]string, error) {
	var noteFiles []string

	if recursive {
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && strings.HasSuffix(strings.ToLower(path), ".note") {
				noteFiles = append(noteFiles, path)
			}
			return nil
		})
		return noteFiles, err
	} else {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(strings.ToLower(entry.Name()), ".note") {
				noteFiles = append(noteFiles, filepath.Join(dir, entry.Name()))
			}
		}
		return noteFiles, nil
	}
}

func main() {
	in := flag.String("in", "", "input directory containing .note files (uses supernote_path from config.json if blank)")
	outDir := flag.String("out-dir", "", "output directory for all generated PNG files")
	logLevel := flag.String("log-level", "info", "logging level: debug, info, warn, error")
	numWorkers := flag.Int("workers", 8, "number of parallel workers for processing notes")
	flag.Parse()

	logging.SetLevel(*logLevel)

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	// If no input directory specified, use supernote_path from config
	resolvedInput := *in
	if resolvedInput == "" {
		if cfg.SupernotePath == "" {
			log.Fatal("supernote_path not configured in config.json and no input directory provided")
		}
		resolvedInput = cfg.SupernotePath
	}

	// Check that the input directory exists
	if _, err := os.Stat(resolvedInput); err != nil {
		log.Fatal(err)
	}

	// Always process recursively
	noteFiles, err := findNoteFiles(resolvedInput, true)
	if err != nil {
		log.Fatalf("failed to find .note files in directory: %v", err)
	}
	if len(noteFiles) == 0 {
		log.Fatalf("no .note files found in directory: %s", resolvedInput)
	}
	logging.Info("Found %d .note file(s) in directory", len(noteFiles))

	// Parallel worker pool
	jobs := make(chan string, *numWorkers)
	results := make(chan error, len(noteFiles))

	// Worker function
	worker := func(id int) {
		for noteFile := range jobs {
			logging.Info("Worker %d processing: %s", id, filepath.Base(noteFile))
			err := processNoteFile(noteFile, *outDir)
			if err != nil {
				logging.Error("failed to process %s: %v", noteFile, err)
			}
			results <- err
		}
	}

	// Start workers
	for w := 0; w < *numWorkers; w++ {
		go worker(w)
	}

	// Send jobs
	for _, noteFile := range noteFiles {
		jobs <- noteFile
	}
	close(jobs)

	// Wait for all results
	for i := 0; i < len(noteFiles); i++ {
		<-results
	}
}

// processNoteFile processes a single .note file for PNG generation (all pages)
func processNoteFile(inputPath string, outDir string) error {
	f, err := os.Open(inputPath)
	if err != nil {
		return fmt.Errorf("failed to open %s: %v", inputPath, err)
	}
	defer f.Close()

	nb, err := note.Parse(f)
	if err != nil {
		return fmt.Errorf("failed to parse %s: %v", inputPath, err)
	}

	// Create a subdirectory for this .note file
	baseName := strings.TrimSuffix(filepath.Base(inputPath), ".note")
	noteDir := baseName
	if outDir != "" {
		noteDir = filepath.Join(outDir, baseName)
	}

	if err := os.MkdirAll(noteDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %v", noteDir, err)
	}

	// Process all pages
	for pageNum := range nb.Pages {
		pageFileName := fmt.Sprintf("page_%03d.png", pageNum)
		pageOutputPath := filepath.Join(noteDir, pageFileName)
		img, err := convertPageToImage(nb, pageNum)
		if err != nil {
			logging.Error("failed to convert page %d in %s: %v", pageNum, inputPath, err)
			continue
		}
		if err := saveImage(img, pageOutputPath); err != nil {
			logging.Error("failed to save page %d in %s: %v", pageNum, inputPath, err)
			continue
		}
		logging.Info("wrote %s", pageOutputPath)
	}
	return nil
}

// convertPageToImage converts a single page from a parsed note to an image
func convertPageToImage(nb *note.Notebook, pageNum int) (image.Image, error) {
	if pageNum < 0 || pageNum >= len(nb.Pages) {
		return nil, fmt.Errorf("page %d out of range (0-%d)", pageNum, len(nb.Pages)-1)
	}

	// Convert the page
	mainImg, bgImg, err := nb.DecodeLayers(pageNum)
	if err != nil {
		return nil, fmt.Errorf("decode page: %w", err)
	}

	// Composite background under main layer if present
	if bgImg != nil {
		note.Composite(mainImg, bgImg)
	}

	return mainImg, nil
}

// parsePageSpec parses a page specification and returns a slice of page numbers
func parsePageSpec(spec string, totalPages int) ([]int, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" || spec == "*" || spec == "all" {
		// Return all pages
		pages := make([]int, totalPages)
		for i := range pages {
			pages[i] = i
		}
		return pages, nil
	}

	var pages []int

	// Split by commas for multiple ranges/numbers
	parts := strings.Split(spec, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)

		if strings.Contains(part, "-") {
			// Handle range like "1-3"
			rangeParts := strings.Split(part, "-")
			if len(rangeParts) != 2 {
				return nil, fmt.Errorf("invalid range format: %s", part)
			}

			start, err := strconv.Atoi(strings.TrimSpace(rangeParts[0]))
			if err != nil {
				return nil, fmt.Errorf("invalid start page: %s", rangeParts[0])
			}

			end, err := strconv.Atoi(strings.TrimSpace(rangeParts[1]))
			if err != nil {
				return nil, fmt.Errorf("invalid end page: %s", rangeParts[1])
			}

			if start > end {
				return nil, fmt.Errorf("start page %d is greater than end page %d", start, end)
			}

			for i := start; i <= end; i++ {
				if i < 0 || i >= totalPages {
					return nil, fmt.Errorf("page %d out of range (0-%d)", i, totalPages-1)
				}
				pages = append(pages, i)
			}
		} else {
			// Handle single page number
			pageNum, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("invalid page number: %s", part)
			}

			if pageNum < 0 || pageNum >= totalPages {
				return nil, fmt.Errorf("page %d out of range (0-%d)", pageNum, totalPages-1)
			}

			pages = append(pages, pageNum)
		}
	}

	return pages, nil
}

// saveImage saves an image to a file
func saveImage(img image.Image, filename string) error {
	outF, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer outF.Close()

	return png.Encode(outF, img)
}
