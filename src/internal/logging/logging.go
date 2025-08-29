package logging

import (
	"fmt"
	"log"
)

var currentLogLevel string = "info"

// Log level constants
const (
	LevelDebug = "debug"
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
)

// SetLevel sets the global logging level
func SetLevel(level string) {
	currentLogLevel = level
}

// Debug logs a debug message
func Debug(format string, args ...interface{}) {
	if shouldLog(LevelDebug) {
		log.Printf("[DEBUG] "+format, args...)
	}
}

// Info logs an info message
func Info(format string, args ...interface{}) {
	if shouldLog(LevelInfo) {
		fmt.Printf(format+"\n", args...)
	}
}

// Warn logs a warning message
func Warn(format string, args ...interface{}) {
	if shouldLog(LevelWarn) {
		log.Printf("[WARN] "+format, args...)
	}
}

// Error logs an error message
func Error(format string, args ...interface{}) {
	if shouldLog(LevelError) {
		log.Printf("[ERROR] "+format, args...)
	}
}

func shouldLog(level string) bool {
	levels := []string{LevelDebug, LevelInfo, LevelWarn, LevelError}
	currentIndex := -1
	levelIndex := -1

	for i, l := range levels {
		if l == currentLogLevel {
			currentIndex = i
		}
		if l == level {
			levelIndex = i
		}
	}

	return levelIndex >= currentIndex
}
