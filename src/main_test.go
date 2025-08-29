package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindNoteFiles(t *testing.T) {
	dir := "../example_notes"
	files, err := findNoteFiles(dir, true)
	if err != nil {
		t.Fatalf("findNoteFiles failed: %v", err)
	}
	if len(files) == 0 {
		t.Errorf("Expected to find .note files in %s, found none", dir)
	}
	for _, f := range files {
		if filepath.Ext(f) != ".note" {
			t.Errorf("File %s does not have .note extension", f)
		}
	}
}

func TestProcessNoteFile(t *testing.T) {
	notePath := "../example_notes/example.note"
	outDir := "../build/test_output"
	os.RemoveAll(outDir)
	err := processNoteFile(notePath, outDir)
	if err != nil {
		t.Fatalf("processNoteFile failed: %v", err)
	}
	// Check output dir exists
	baseName := filepath.Base(notePath)
	baseName = baseName[:len(baseName)-len(filepath.Ext(baseName))]
	noteOutDir := filepath.Join(outDir, baseName)
	if _, err := os.Stat(noteOutDir); err != nil {
		t.Errorf("Output directory %s not created", noteOutDir)
	}
}
