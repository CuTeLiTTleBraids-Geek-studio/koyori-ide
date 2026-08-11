package services

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestAtomicWriteJSON_WritesValidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")
	data := map[string]string{"key": "value"}
	if err := atomicWriteJSON(path, data, 0600); err != nil {
		t.Fatalf("atomicWriteJSON: %v", err)
	}
	var got map[string]string
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("written file is not valid JSON: %v", err)
	}
	if got["key"] != "value" {
		t.Errorf("expected key=value, got %v", got)
	}
}

func TestAtomicWriteJSON_FilePermissions(t *testing.T) {
	// Windows does not honor Unix permission bits: os.Chmod only toggles
	// the read-only attribute, and os.Stat reports 0666 for writable
	// files. The 0600 contract is therefore unverifiable there.
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are not enforced on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.json")
	if err := atomicWriteJSON(path, map[string]string{}, 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("expected 0600, got %v", info.Mode().Perm())
	}
}

func TestAtomicWriteJSON_NoTempFilesLeft(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.json")
	if err := atomicWriteJSON(path, map[string]string{}, 0600); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "out.json" {
			t.Errorf("unexpected file left behind: %s", e.Name())
		}
	}
}
