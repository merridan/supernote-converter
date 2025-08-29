package logging

import (
	"testing"
)

func TestSetLevel(t *testing.T) {
	levels := []string{"debug", "info", "warn", "error", "invalid"}
	for _, lvl := range levels {
		SetLevel(lvl)
	}
}
