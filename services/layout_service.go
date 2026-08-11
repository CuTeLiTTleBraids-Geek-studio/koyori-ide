package services

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/adrg/xdg"
)

// LayoutService saves and loads the IDE layout tree as JSON in the
// config/profile directory (N-25).
//
// The layout tree describes the split/leaf structure of the main editor
// area: which views are open, how they're split, and their relative sizes.
// Storing it separately from settings.json keeps UI layout state isolated
// from application configuration.
//
// Profile-aware (Plan 50): the layoutPath points at the active profile's
// layout.json. ProfileService.SetActiveProfile calls SetLayoutPath to
// redirect this service to the new profile's layout file.
type LayoutService struct {
	mu         sync.RWMutex
	layoutPath string
}

// NewLayoutService creates a LayoutService using the XDG config path.
func NewLayoutService() *LayoutService {
	return &LayoutService{
		layoutPath: filepath.Join(xdg.ConfigHome, "koyori-ide", "layout.json"),
	}
}

// NewLayoutServiceWithPath creates a LayoutService that reads and
// writes layout data at the given absolute path.
func NewLayoutServiceWithPath(path string) *LayoutService {
	return &LayoutService{layoutPath: path}
}

// setLayoutPath redirects the service to read/write layout data at the
// given path. Called by ProfileService (via the onSwitch callback) when
// the active profile changes.
//
//wails:ignore
func (s *LayoutService) setLayoutPath(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.layoutPath = path
}

// LoadLayout reads the layout JSON from disk. Returns an empty string
// (not an error) if the file doesn't exist yet — the frontend will
// initialize a default layout tree in that case.
func (s *LayoutService) LoadLayout() (string, error) {
	s.mu.RLock()
	path := s.layoutPath
	s.mu.RUnlock()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	// Validate that the file contains valid JSON before returning it.
	// If corrupt, return empty string so the frontend falls back to
	// the default layout rather than crashing.
	var raw json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return "", nil
	}
	return string(data), nil
}

// SaveLayout writes the layout JSON to disk. The JSON string is validated
// before writing; invalid JSON returns an error to avoid persisting
// corrupt state.
//
// N-48 (Proposal O): Uses a write lock (Lock) covering the entire
// "read path + write file" process to prevent concurrent SaveLayout calls
// from racing on os.WriteFile. Writes to a temp file first, then renames
// atomically — this prevents partial writes from corrupting the layout
// file even if the process crashes mid-write.
//
// G-SEC-09: this already writes atomically (temp file + rename), so it
// satisfies the atomic-write requirement directly. It is intentionally
// not routed through atomicWriteJSON because that helper re-indents its
// payload, and the layout JSON must be preserved verbatim (existing
// round-trip tests assert byte-for-byte fidelity).
func (s *LayoutService) SaveLayout(layoutJSON string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.layoutPath

	// Validate JSON before writing.
	var raw json.RawMessage
	if err := json.Unmarshal([]byte(layoutJSON), &raw); err != nil {
		return err
	}

	// Ensure the parent directory exists.
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	// N-48: Atomic write — write to a temp file, then rename. Rename is
	// atomic on most filesystems (POSIX and NTFS), so a crash mid-write
	// leaves either the old file or the new file intact, never a partial
	// write. This also prevents concurrent SaveLayout calls from
	// interleaving writes.
	tmpPath := path + ".tmp"
	data := []byte(layoutJSON)
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		// Best-effort cleanup of the temp file on rename failure.
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

// ResetLayout removes the persisted layout file (Proposal H / prompt-4.md).
// After calling this, the next LoadLayout returns an empty string and the
// frontend initializes a fresh default layout tree.
//
// This is the "panic button" for users whose layout has become unusable
// (e.g. overly nested splits, lost panels). The frontend pairs this with
// resetLayout() + saveLayoutToBackend() to persist the fresh default.
//
// Returns nil if the layout file does not exist (idempotent).
//
// N-48 (Proposal O): Uses a write lock (Lock) to prevent a concurrent
// SaveLayout from racing with this removal (e.g. writing the file back
// after ResetLayout deletes it).
func (s *LayoutService) ResetLayout() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.layoutPath

	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	// Also clean up any leftover temp file from a failed SaveLayout.
	_ = os.Remove(path + ".tmp")
	return nil
}
