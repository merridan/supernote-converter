package note

import (
	"os"
	"testing"
)

func TestParse(t *testing.T) {
	f, err := os.Open("../../../example_notes/example.note")
	if err != nil {
		t.Fatalf("failed to open example.note: %v", err)
	}
	nb, err := Parse(f)
	f.Close()
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if len(nb.Pages) == 0 {
		t.Errorf("Expected at least one page in parsed note")
	}
}
