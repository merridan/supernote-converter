package config

import (
	"io/ioutil"
	"os"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	// Save current directory
	originalDir, _ := os.Getwd()

	// Create a temporary directory
	tempDir, err := ioutil.TempDir("", "test_config")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Change to temp directory
	os.Chdir(tempDir)
	defer os.Chdir(originalDir)

	// Create config.json in temp directory
	configContent := `{"supernote_path": "test_path"}`
	err = ioutil.WriteFile("config.json", []byte(configContent), 0644)
	if err != nil {
		t.Fatalf("failed to write config.json: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.SupernotePath != "test_path" {
		t.Errorf("Expected supernote_path to be 'test_path', got '%s'", cfg.SupernotePath)
	}
}

func TestLoadConfigNoFile(t *testing.T) {
	// Save current directory
	originalDir, _ := os.Getwd()

	// Create a temporary directory
	tempDir, err := ioutil.TempDir("", "test_config_empty")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Change to temp directory (no config.json)
	os.Chdir(tempDir)
	defer os.Chdir(originalDir)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.SupernotePath != "" {
		t.Errorf("Expected empty supernote_path for missing config, got '%s'", cfg.SupernotePath)
	}
}
