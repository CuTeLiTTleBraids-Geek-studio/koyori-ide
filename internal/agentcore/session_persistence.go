package agentcore

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

const sessionSnapshotVersion = 1

const maxSessionSnapshotBytes = 16 << 20

type sessionSnapshot struct {
	Version  int       `json:"version"`
	Sessions []Session `json:"sessions"`
}

// FileSessionStorePersistence stores lifecycle rows in a private, atomically
// replaced JSON snapshot. It reports the post-rename directory-sync window
// explicitly so callers never roll memory back behind a published snapshot.
// It is kept in the headless package so CLI/CI hosts can share this contract.
type FileSessionStorePersistence struct {
	Path string
	// Root is a trusted, lifetime-bound persistence capability. When set,
	// Name is used instead of Path and every open/rename/remove stays inside
	// the bound root. The caller owns the Root lifetime.
	Root SessionPersistenceRoot
	Name string

	// The hooks are package-private fault-injection points for verifying the
	// publication boundary. Production callers always use the defaults.
	replaceForTest       func(temp, target string) error
	syncDirectoryForTest func(dir string) error
}

// SessionPersistenceRoot is the minimal capability required by the shared
// session snapshot implementation. Trusted hosts can enforce no-follow,
// owner and link identity checks without exposing a host pathname here.
type SessionPersistenceRoot interface {
	OpenRegular(name string) (*os.File, error)
	CreateExclusive(name string, perm os.FileMode) (*os.File, error)
	VerifyIdentity(name string, expected os.FileInfo) error
	Rename(oldName, newName string) error
	RemoveIfIdentity(name string, expected os.FileInfo) error
	Sync() error
}

func (p FileSessionStorePersistence) Load() ([]Session, error) {
	if p.Root == nil && p.Path == "" {
		return nil, fmt.Errorf("session snapshot path is required")
	}
	var file *os.File
	var err error
	if p.Root != nil {
		if p.Name == "" {
			return nil, fmt.Errorf("session snapshot root-relative name is required")
		}
		file, err = p.Root.OpenRegular(p.Name)
	} else {
		file, err = os.Open(p.Path)
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("open session snapshot: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat session snapshot: %w", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("session snapshot permissions %o are too broad", info.Mode().Perm())
	}
	data, err := io.ReadAll(io.LimitReader(file, maxSessionSnapshotBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read session snapshot: %w", err)
	}
	if len(data) > maxSessionSnapshotBytes {
		return nil, fmt.Errorf("session snapshot exceeds %d bytes", maxSessionSnapshotBytes)
	}
	var snapshot sessionSnapshot
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return nil, fmt.Errorf("decode session snapshot: %w", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = fmt.Errorf("multiple JSON values")
		}
		return nil, fmt.Errorf("decode session snapshot trailing data: %w", err)
	}
	if snapshot.Version != sessionSnapshotVersion {
		return nil, fmt.Errorf("unsupported session snapshot version %d", snapshot.Version)
	}
	return snapshot.Sessions, nil
}

func (p FileSessionStorePersistence) Save(sessions []Session) (PersistenceCommitState, error) {
	if p.Root != nil {
		return p.saveWithinRoot(sessions)
	}
	if p.Path == "" {
		return PersistenceNotPublished, fmt.Errorf("session snapshot path is required")
	}
	dir := filepath.Dir(p.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return PersistenceNotPublished, fmt.Errorf("create session snapshot directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".agent-sessions-*")
	if err != nil {
		return PersistenceNotPublished, fmt.Errorf("create session snapshot temp: %w", err)
	}
	tmpName := tmp.Name()
	closed := false
	defer func() {
		if !closed {
			_ = tmp.Close()
		}
		_ = os.Remove(tmpName)
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return PersistenceNotPublished, fmt.Errorf("protect session snapshot temp: %w", err)
	}
	limited := &sessionSnapshotLimitWriter{writer: tmp, remaining: maxSessionSnapshotBytes}
	encoder := json.NewEncoder(limited)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(sessionSnapshot{Version: sessionSnapshotVersion, Sessions: sessions}); err != nil {
		return PersistenceNotPublished, fmt.Errorf("encode session snapshot: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return PersistenceNotPublished, fmt.Errorf("sync session snapshot: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return PersistenceNotPublished, fmt.Errorf("close session snapshot: %w", err)
	}
	closed = true
	replace := replaceSessionSnapshot
	if p.replaceForTest != nil {
		replace = p.replaceForTest
	}
	if err := replace(tmpName, p.Path); err != nil {
		return PersistenceNotPublished, err
	}
	syncDirectory := syncSessionSnapshotDirectory
	if p.syncDirectoryForTest != nil {
		syncDirectory = p.syncDirectoryForTest
	}
	if err := syncDirectory(dir); err != nil {
		return PersistencePublishedDurabilityUnknown, err
	}
	return PersistenceDurable, nil
}

func (p FileSessionStorePersistence) saveWithinRoot(sessions []Session) (PersistenceCommitState, error) {
	if p.Root == nil || p.Name == "" {
		return PersistenceNotPublished, fmt.Errorf("session snapshot root and name are required")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return PersistenceNotPublished, fmt.Errorf("create session snapshot temp identity: %w", err)
	}
	tempName := ".agent-sessions-" + hex.EncodeToString(nonce[:]) + ".tmp"
	tmp, err := p.Root.CreateExclusive(tempName, 0o600)
	if err != nil {
		return PersistenceNotPublished, fmt.Errorf("create session snapshot temp: %w", err)
	}
	closed := false
	published := false
	var identity os.FileInfo
	defer func() {
		if !closed {
			_ = tmp.Close()
		}
		if !published && identity != nil {
			_ = p.Root.RemoveIfIdentity(tempName, identity)
		}
	}()
	identity, err = tmp.Stat()
	if err != nil {
		return PersistenceNotPublished, fmt.Errorf("identify session snapshot temp: %w", err)
	}
	if err := encodeSessionSnapshot(tmp, sessions); err != nil {
		return PersistenceNotPublished, err
	}
	if err := tmp.Sync(); err != nil {
		return PersistenceNotPublished, fmt.Errorf("sync session snapshot temp: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		return PersistenceNotPublished, fmt.Errorf("protect session snapshot temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return PersistenceNotPublished, fmt.Errorf("close session snapshot temp: %w", err)
	}
	closed = true
	if err := p.Root.VerifyIdentity(tempName, identity); err != nil {
		return PersistenceNotPublished, fmt.Errorf("verify session snapshot temp: %w", err)
	}
	if err := p.Root.Rename(tempName, p.Name); err != nil {
		return PersistenceNotPublished, fmt.Errorf("publish session snapshot: %w", err)
	}
	published = true
	if err := p.Root.VerifyIdentity(p.Name, identity); err != nil {
		return PersistencePublishedDurabilityUnknown, fmt.Errorf("verify published session snapshot: %w", err)
	}
	if err := p.Root.Sync(); err != nil {
		return PersistencePublishedDurabilityUnknown, fmt.Errorf("sync session snapshot root: %w", err)
	}
	return PersistenceDurable, nil
}

func encodeSessionSnapshot(writer io.Writer, sessions []Session) error {
	limited := &sessionSnapshotLimitWriter{writer: writer, remaining: maxSessionSnapshotBytes}
	encoder := json.NewEncoder(limited)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(sessionSnapshot{Version: sessionSnapshotVersion, Sessions: sessions}); err != nil {
		return fmt.Errorf("encode session snapshot: %w", err)
	}
	return nil
}

type sessionSnapshotLimitWriter struct {
	writer    io.Writer
	remaining int
}

func (w *sessionSnapshotLimitWriter) Write(data []byte) (int, error) {
	if len(data) > w.remaining {
		return 0, fmt.Errorf("session snapshot exceeds %d bytes", maxSessionSnapshotBytes)
	}
	written, err := w.writer.Write(data)
	w.remaining -= written
	return written, err
}

func replaceSessionSnapshot(temp, target string) error {
	if err := os.Rename(temp, target); err != nil {
		return fmt.Errorf("publish session snapshot: %w", err)
	}
	return nil
}

func syncSessionSnapshotDirectory(dir string) error {
	if runtime.GOOS == "windows" {
		// Windows does not expose a generally usable directory fsync handle;
		// the temporary file was already flushed before Replace/Rename.
		return nil
	}
	file, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open session snapshot directory: %w", err)
	}
	defer file.Close()
	if err := file.Sync(); err != nil && !errors.Is(err, os.ErrInvalid) {
		return fmt.Errorf("sync session snapshot directory: %w", err)
	}
	return nil
}
