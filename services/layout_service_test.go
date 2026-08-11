package services

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLayoutService_LoadLayout_FileNotExists(t *testing.T) {
	dir := t.TempDir()
	s := NewLayoutServiceWithPath(filepath.Join(dir, "layout.json"))

	got, err := s.LoadLayout()
	if err != nil {
		t.Fatalf("LoadLayout returned error for missing file: %v", err)
	}
	if got != "" {
		t.Fatalf("LoadLayout returned %q, want empty string for missing file", got)
	}
}

func TestLayoutService_SaveAndLoad_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewLayoutServiceWithPath(filepath.Join(dir, "layout.json"))

	layoutJSON := `{"root":{"type":"leaf","id":"leaf1","viewId":"editor"},"activeLeafId":"leaf1"}`
	if err := s.SaveLayout(layoutJSON); err != nil {
		t.Fatalf("SaveLayout failed: %v", err)
	}

	got, err := s.LoadLayout()
	if err != nil {
		t.Fatalf("LoadLayout failed: %v", err)
	}
	if got != layoutJSON {
		t.Fatalf("LoadLayout returned %q, want %q", got, layoutJSON)
	}
}

func TestLayoutService_SaveLayout_CreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	nestedPath := filepath.Join(dir, "sub", "dir", "layout.json")
	s := NewLayoutServiceWithPath(nestedPath)

	if err := s.SaveLayout(`{}`); err != nil {
		t.Fatalf("SaveLayout failed with nested path: %v", err)
	}

	if _, err := os.Stat(nestedPath); err != nil {
		t.Fatalf("expected layout.json to exist at %s: %v", nestedPath, err)
	}
}

func TestLayoutService_SaveLayout_RejectsInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	s := NewLayoutServiceWithPath(filepath.Join(dir, "layout.json"))

	err := s.SaveLayout(`{invalid json}`)
	if err == nil {
		t.Fatal("SaveLayout should reject invalid JSON")
	}

	// Verify no file was written.
	if _, err := os.Stat(filepath.Join(dir, "layout.json")); !os.IsNotExist(err) {
		t.Fatal("layout.json should not exist after failed save")
	}
}

func TestLayoutService_LoadLayout_CorruptFileReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "layout.json")
	os.WriteFile(path, []byte(`{corrupt}`), 0o644)

	s := NewLayoutServiceWithPath(path)
	got, err := s.LoadLayout()
	if err != nil {
		t.Fatalf("LoadLayout should not return error for corrupt file: %v", err)
	}
	if got != "" {
		t.Fatalf("LoadLayout returned %q, want empty string for corrupt file", got)
	}
}

func TestLayoutService_SetLayoutPath(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	s := NewLayoutServiceWithPath(filepath.Join(dir1, "layout.json"))

	// Save to dir1.
	s.SaveLayout(`{"v":1}`)

	// Switch to dir2 and verify LoadLayout returns empty.
	s.setLayoutPath(filepath.Join(dir2, "layout.json"))
	got, err := s.LoadLayout()
	if err != nil {
		t.Fatalf("LoadLayout failed: %v", err)
	}
	if got != "" {
		t.Fatalf("LoadLayout after SetLayoutPath returned %q, want empty", got)
	}

	// Save to dir2.
	s.SaveLayout(`{"v":2}`)
	got, err = s.LoadLayout()
	if err != nil {
		t.Fatalf("LoadLayout failed: %v", err)
	}
	if got != `{"v":2}` {
		t.Fatalf("LoadLayout returned %q, want {\"v\":2}", got)
	}

	// Verify dir1 still has v:1.
	got1, _ := os.ReadFile(filepath.Join(dir1, "layout.json"))
	if string(got1) != `{"v":1}` {
		t.Fatalf("dir1 layout.json = %q, want {\"v\":1}", string(got1))
	}
}

func TestLayoutService_SaveLayout_EmptyJSON(t *testing.T) {
	dir := t.TempDir()
	s := NewLayoutServiceWithPath(filepath.Join(dir, "layout.json"))

	// Empty JSON object is valid.
	if err := s.SaveLayout(`{}`); err != nil {
		t.Fatalf("SaveLayout failed for empty JSON object: %v", err)
	}

	got, err := s.LoadLayout()
	if err != nil {
		t.Fatalf("LoadLayout failed: %v", err)
	}
	if got != `{}` {
		t.Fatalf("LoadLayout returned %q, want {}", got)
	}
}

func TestLayoutService_SaveLayout_ComplexTree(t *testing.T) {
	dir := t.TempDir()
	s := NewLayoutServiceWithPath(filepath.Join(dir, "layout.json"))

	// A complex split tree with nested splits and leaves.
	layoutJSON := `{
  "root": {
    "type": "split",
    "id": "split1",
    "orientation": "horizontal",
    "children": [
      {"type": "leaf", "id": "leaf1", "viewId": "explorer"},
      {
        "type": "split",
        "id": "split2",
        "orientation": "vertical",
        "children": [
          {"type": "leaf", "id": "leaf2", "viewId": "editor"},
          {"type": "leaf", "id": "leaf3", "viewId": "preview"}
        ],
        "sizes": [70, 30]
      }
    ],
    "sizes": [25, 75]
  },
  "activeLeafId": "leaf2"
}`

	if err := s.SaveLayout(layoutJSON); err != nil {
		t.Fatalf("SaveLayout failed: %v", err)
	}

	got, err := s.LoadLayout()
	if err != nil {
		t.Fatalf("LoadLayout failed: %v", err)
	}
	if got != layoutJSON {
		t.Fatalf("LoadLayout round-trip mismatch")
	}
}

// Proposal H (prompt-4.md): ResetLayout removes the persisted layout file.
func TestLayoutService_ResetLayout_RemovesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "layout.json")
	s := NewLayoutServiceWithPath(path)

	// Save a layout.
	if err := s.SaveLayout(`{"root":{"type":"leaf","id":"l1","viewId":"editor"}}`); err != nil {
		t.Fatalf("SaveLayout failed: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("layout.json should exist after save: %v", err)
	}

	// Reset — file should be gone.
	if err := s.ResetLayout(); err != nil {
		t.Fatalf("ResetLayout failed: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("layout.json should not exist after reset, got err=%v", err)
	}

	// LoadLayout should return empty string.
	got, err := s.LoadLayout()
	if err != nil {
		t.Fatalf("LoadLayout after reset failed: %v", err)
	}
	if got != "" {
		t.Fatalf("LoadLayout after reset returned %q, want empty", got)
	}
}

func TestLayoutService_ResetLayout_IdempotentOnMissingFile(t *testing.T) {
	dir := t.TempDir()
	s := NewLayoutServiceWithPath(filepath.Join(dir, "layout.json"))

	// Reset on a non-existent file should not error.
	if err := s.ResetLayout(); err != nil {
		t.Fatalf("ResetLayout on missing file should not error: %v", err)
	}
}

// N-48 (Proposal O): SaveLayout should not leave a .tmp file after success.
func TestLayoutService_SaveLayout_NoTempFileLeftover(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "layout.json")
	s := NewLayoutServiceWithPath(path)

	if err := s.SaveLayout(`{"v":1}`); err != nil {
		t.Fatalf("SaveLayout failed: %v", err)
	}

	// The .tmp file should not exist after a successful save.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temp file should not exist after successful save, got err=%v", err)
	}
}

// N-48: ResetLayout should also clean up any leftover .tmp file.
func TestLayoutService_ResetLayout_CleansUpTempFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "layout.json")
	s := NewLayoutServiceWithPath(path)

	// Simulate a leftover .tmp file from a crashed SaveLayout.
	if err := os.WriteFile(path+".tmp", []byte(`partial`), 0o644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	if err := s.ResetLayout(); err != nil {
		t.Fatalf("ResetLayout failed: %v", err)
	}

	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temp file should be removed by ResetLayout, got err=%v", err)
	}
}

// N-48: Concurrent SaveLayout calls should not corrupt the file.
// This test fires multiple goroutines writing different layouts and
// verifies the final file is valid JSON matching one of the writes.
func TestLayoutService_SaveLayout_ConcurrentWritesNoCorruption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "layout.json")
	s := NewLayoutServiceWithPath(path)

	layouts := []string{
		`{"version":1,"data":"first"}`,
		`{"version":2,"data":"second"}`,
		`{"version":3,"data":"third"}`,
		`{"version":4,"data":"fourth"}`,
		`{"version":5,"data":"fifth"}`,
	}

	// Fire all writes concurrently.
	done := make(chan error, len(layouts))
	for _, l := range layouts {
		go func(layout string) {
			done <- s.SaveLayout(layout)
		}(l)
	}
	for range layouts {
		if err := <-done; err != nil {
			t.Fatalf("concurrent SaveLayout failed: %v", err)
		}
	}

	// The file should exist and be valid JSON (one of the written values).
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read layout.json: %v", err)
	}
	got := string(data)

	found := false
	for _, expected := range layouts {
		if got == expected {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("layout.json content %q does not match any of the written layouts", got)
	}

	// No .tmp file should be left.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temp file should not exist after concurrent saves, got err=%v", err)
	}
}
