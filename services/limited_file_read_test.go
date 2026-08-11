package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFileLimitedRejectsFilesOverLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "asset.js")
	if err := os.WriteFile(path, []byte("12345"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := readFileLimited(path, 4)
	if err == nil || !strings.Contains(err.Error(), "maximum size") {
		t.Fatalf("readFileLimited error = %v, want maximum-size error", err)
	}
}

func TestReadFileLimitedReadsAtMostConfiguredSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "asset.js")
	if err := os.WriteFile(path, []byte("1234"), 0644); err != nil {
		t.Fatal(err)
	}

	data, err := readFileLimited(path, 4)
	if err != nil {
		t.Fatalf("readFileLimited failed: %v", err)
	}
	if string(data) != "1234" {
		t.Fatalf("readFileLimited = %q, want 1234", data)
	}
}
