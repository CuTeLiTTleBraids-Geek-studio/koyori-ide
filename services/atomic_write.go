package services

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// atomicWriteJSON marshals data to JSON and writes it atomically by first
// writing to a temporary file in the same directory, then renaming it to
// the target path. This prevents half-written files if the process crashes
// mid-write (G-SEC-09 / M-5).
// If perm is 0, it defaults to 0600 for sensitive files.
func atomicWriteJSON(path string, data interface{}, perm os.FileMode) error {
	if perm == 0 {
		perm = 0600
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	// Clean up temp file on any failure path.
	defer func() {
		if tmp != nil {
			tmp.Close()
		}
		os.Remove(tmpName)
	}()
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(data); err != nil {
		return fmt.Errorf("encode json: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	tmp = nil // prevent deferred Close
	if err := os.Chmod(tmpName, perm); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := replaceFileAtomically(tmpName, path); err != nil {
		return fmt.Errorf("rename temp to target: %w", err)
	}
	return nil
}

// atomicWriteFile writes raw bytes to path atomically by first writing to a
// temporary file in the same directory, then renaming it to the target path.
// This prevents half-written files if the process crashes mid-write
// (G-SEC-09 / M-5). Unlike atomicWriteJSON, it does not marshal the data -
// use it for raw text/binary content such as source file replacements.
// If perm is 0, it defaults to 0600.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	if perm == 0 {
		perm = 0600
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if tmp != nil {
			tmp.Close()
		}
		os.Remove(tmpName)
	}()
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	tmp = nil
	if err := os.Chmod(tmpName, perm); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := replaceFileAtomically(tmpName, path); err != nil {
		return fmt.Errorf("rename temp to target: %w", err)
	}
	return nil
}

func atomicWriteFileWithinRoot(
	capability fileCapability,
	data []byte,
	perm os.FileMode,
	beforeCommit func() error,
) error {
	if perm == 0 {
		perm = 0o600
	}
	parent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(capability.relative)))
	if parent == "" {
		parent = "."
	}
	if err := capability.root.root.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create atomic write parent: %w", err)
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return fmt.Errorf("create atomic temp name: %w", err)
	}
	temp := filepath.ToSlash(filepath.Join(filepath.FromSlash(parent), ".gugacode-save-"+hex.EncodeToString(random[:])+
		".tmp"))
	file, err := capability.root.root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create atomic temp file: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
		_ = capability.root.root.Remove(temp)
	}()
	tempIdentity, err := file.Stat()
	if err != nil {
		return fmt.Errorf("identify atomic temp file: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write atomic temp file: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync atomic temp file: %w", err)
	}
	if err := file.Chmod(perm); err != nil {
		return fmt.Errorf("chmod atomic temp file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close atomic temp file: %w", err)
	}
	closed = true
	publishedIdentity, err := capability.root.root.Lstat(temp)
	if err != nil {
		return fmt.Errorf("verify atomic temp file: %w", err)
	}
	if !os.SameFile(tempIdentity, publishedIdentity) {
		return fmt.Errorf("atomic temp file identity changed: %w", ErrNotAllowed)
	}
	if beforeCommit != nil {
		if err := beforeCommit(); err != nil {
			return err
		}
	}
	if err := capability.root.root.Rename(temp, capability.relative); err != nil {
		return fmt.Errorf("rename atomic temp file: %w", err)
	}
	return nil
}
