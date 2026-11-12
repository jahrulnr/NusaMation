package notify

import (
	"bytes"
	"os"
	"testing"
)

func TestTemporaryIcon(t *testing.T) {
	path, cleanup, err := temporaryIcon("mark.png")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	data, err := os.ReadFile(path)
	if err != nil || !bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatalf("temporary icon is not a PNG: %v", err)
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("temporary icon was not removed: %v", err)
	}
}
