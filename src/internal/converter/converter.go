package converter

import (
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/merridan/sngo/internal/logging"
	"github.com/merridan/sngo/internal/note"
)

// ConvertPage converts a single page to PNG and saves it
func ConvertPage(nb *note.Notebook, pageNum int, outPath string, totalPages int) error {
	img, err := ConvertPageToImage(nb, pageNum)
	if err != nil {
		return err
	}

	// Generate unique filename if multiple pages
	finalPath := outPath
	if totalPages > 1 {
		ext := filepath.Ext(outPath)
		base := outPath[:len(outPath)-len(ext)]
		finalPath = fmt.Sprintf("%s_page_%d%s", base, pageNum, ext)
	}

	if err := SaveImage(img, finalPath); err != nil {
		return err
	}

	logging.Info("wrote %s", finalPath)
	return nil
}

// ConvertPageToImage converts a single page to an image.Image
func ConvertPageToImage(nb *note.Notebook, pageNum int) (image.Image, error) {
	if pageNum >= len(nb.Pages) {
		return nil, fmt.Errorf("page %d does not exist (total pages: %d)", pageNum, len(nb.Pages))
	}

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

// MergeImagesVertically merges multiple images into a single vertical image
func MergeImagesVertically(images []image.Image) image.Image {
	if len(images) == 0 {
		return nil
	}
	if len(images) == 1 {
		return images[0]
	}

	// Calculate total height and max width
	var totalHeight int
	var maxWidth int
	for _, img := range images {
		bounds := img.Bounds()
		totalHeight += bounds.Dy()
		if bounds.Dx() > maxWidth {
			maxWidth = bounds.Dx()
		}
	}

	// Create the merged image
	merged := image.NewRGBA(image.Rect(0, 0, maxWidth, totalHeight))

	// Draw each image
	currentY := 0
	for _, img := range images {
		bounds := img.Bounds()
		draw.Draw(merged, image.Rect(0, currentY, bounds.Dx(), currentY+bounds.Dy()), img, bounds.Min, draw.Src)
		currentY += bounds.Dy()
	}

	return merged
}

// SaveImage saves an image to a file
func SaveImage(img image.Image, filename string) error {
	file, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	return png.Encode(file, img)
}

// ParsePageSpec parses page specification strings like "0", "*", "0-5", "2,6,7"
func ParsePageSpec(spec string, totalPages int) ([]int, error) {
	spec = strings.TrimSpace(spec)

	if spec == "*" {
		// All pages
		var pages []int
		for i := 0; i < totalPages; i++ {
			pages = append(pages, i)
		}
		return pages, nil
	}

	if strings.Contains(spec, ",") {
		// List of pages: "2,6,7"
		parts := strings.Split(spec, ",")
		var pages []int
		for _, part := range parts {
			part = strings.TrimSpace(part)
			pageNum, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("invalid page number: %s", part)
			}
			if pageNum < 0 || pageNum >= totalPages {
				return nil, fmt.Errorf("page %d is out of range (0-%d)", pageNum, totalPages-1)
			}
			pages = append(pages, pageNum)
		}
		return pages, nil
	}

	if strings.Contains(spec, "-") {
		// Range: "0-5"
		parts := strings.Split(spec, "-")
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid range format: %s", spec)
		}

		start, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil {
			return nil, fmt.Errorf("invalid start page: %s", parts[0])
		}

		end, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil {
			return nil, fmt.Errorf("invalid end page: %s", parts[1])
		}

		if start < 0 || start >= totalPages {
			return nil, fmt.Errorf("start page %d is out of range (0-%d)", start, totalPages-1)
		}
		if end < 0 || end >= totalPages {
			return nil, fmt.Errorf("end page %d is out of range (0-%d)", end, totalPages-1)
		}
		if start > end {
			return nil, fmt.Errorf("start page %d is greater than end page %d", start, end)
		}

		var pages []int
		for i := start; i <= end; i++ {
			pages = append(pages, i)
		}
		return pages, nil
	}

	// Single page
	pageNum, err := strconv.Atoi(spec)
	if err != nil {
		return nil, fmt.Errorf("invalid page number: %s", spec)
	}
	if pageNum < 0 || pageNum >= totalPages {
		return nil, fmt.Errorf("page %d is out of range (0-%d)", pageNum, totalPages-1)
	}

	return []int{pageNum}, nil
}
