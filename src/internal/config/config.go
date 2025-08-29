package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Config represents the configuration file structure
type Config struct {
	SupernotePath string `json:"supernote_path"`
}

// Load loads configuration from config.json file
func Load() (*Config, error) {
	configPath := "config.json"

	// Check if config file exists
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		// Return default config if file doesn't exist
		return &Config{}, nil
	}

	file, err := os.Open(configPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var config Config
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&config); err != nil {
		return nil, err
	}

	return &config, nil
}

// ResolveInputPath resolves the input path, using supernote_path from config if needed
func ResolveInputPath(inputPath string, config *Config) string {
	// If it's an absolute path or contains path separators, use as-is
	if filepath.IsAbs(inputPath) || strings.Contains(inputPath, string(filepath.Separator)) {
		return inputPath
	}

	// If config has supernote_path and input looks like just a filename, combine them
	if config != nil && config.SupernotePath != "" {
		return filepath.Join(config.SupernotePath, inputPath)
	}

	// Otherwise use as-is
	return inputPath
}
