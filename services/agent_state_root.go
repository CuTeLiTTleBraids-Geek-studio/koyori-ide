package services

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"runtime"
	"strings"
)

func validAgentStateLeaf(name string) bool {
	return name != "" && name != "." && fs.ValidPath(name) && !strings.ContainsAny(name, `/\:`)
}

// openAgentStateRegularFile is the shared trusted opener for state-bound
// persistence. It never returns a handle until the opened object and the
// root-relative directory entry have the same regular, single-link identity.
func openAgentStateRegularFile(root *os.Root, name string, flags int, perm os.FileMode) (*os.File, error) {
	if root == nil || !validAgentStateLeaf(name) || flags&os.O_TRUNC != 0 {
		return nil, fmt.Errorf("agent state opener arguments are invalid: %w", ErrInvalidInput)
	}
	file, err := root.OpenFile(name, flags, perm)
	if err != nil {
		return nil, err
	}
	valid := false
	defer func() {
		if !valid {
			_ = file.Close()
		}
	}()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() {
		return nil, fmt.Errorf("agent state leaf is not a regular file: %w", ErrUsagePersistencePoisoned)
	}
	named, err := root.Lstat(name)
	if err != nil || named.Mode()&fs.ModeSymlink != 0 || !os.SameFile(opened, named) {
		return nil, fmt.Errorf("agent state leaf identity changed: %w", ErrUsagePersistencePoisoned)
	}
	owned, err := agentFileOwnedByCurrentUser(file)
	if err != nil || !owned {
		return nil, fmt.Errorf("agent state leaf owner is not trusted: %w", ErrUsagePersistencePoisoned)
	}
	multipleLinks, err := agentFileHasMultipleLinks(file)
	if err != nil || multipleLinks {
		return nil, fmt.Errorf("agent state leaf link identity is unsafe: %w", ErrUsagePersistencePoisoned)
	}
	if perm != 0 {
		if err := file.Chmod(perm); err != nil {
			return nil, fmt.Errorf("protect agent state leaf: %w", err)
		}
	}
	valid = true
	return file, nil
}

func readAgentStateFile(root *os.Root, name string, limit int64) ([]byte, error) {
	file, err := openAgentStateRegularFile(root, name, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader := io.Reader(file)
	if limit >= 0 {
		reader = io.LimitReader(file, limit+1)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if limit >= 0 && int64(len(data)) > limit {
		return nil, fmt.Errorf("agent state leaf exceeds its read limit: %w", ErrUsagePersistencePoisoned)
	}
	return data, nil
}

func removeAgentStateFileIfIdentity(root *os.Root, name string, expected os.FileInfo) error {
	if root == nil || !validAgentStateLeaf(name) {
		return fmt.Errorf("agent state removal arguments are invalid: %w", ErrInvalidInput)
	}
	file, err := openAgentStateRegularFile(root, name, os.O_RDONLY, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	current, statErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil || closeErr != nil {
		return errors.Join(statErr, closeErr)
	}
	if expected != nil && !os.SameFile(expected, current) {
		return fmt.Errorf("agent state removal identity changed: %w", ErrUsagePersistencePoisoned)
	}
	named, err := root.Lstat(name)
	if err != nil || !os.SameFile(current, named) {
		return fmt.Errorf("agent state removal name changed: %w", ErrUsagePersistencePoisoned)
	}
	return root.Remove(name)
}

func atomicWriteAgentStateFile(root *os.Root, name string, data []byte, perm os.FileMode) error {
	if root == nil || !validAgentStateLeaf(name) {
		return fmt.Errorf("agent state atomic write arguments are invalid: %w", ErrInvalidInput)
	}
	if perm == 0 {
		perm = 0o600
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return fmt.Errorf("create agent state temp identity: %w", err)
	}
	tempName := ".agent-state-" + hex.EncodeToString(nonce[:]) + ".tmp"
	file, err := openAgentStateRegularFile(root, tempName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create agent state temp file: %w", err)
	}
	closed := false
	published := false
	var identity os.FileInfo
	defer func() {
		if !closed {
			_ = file.Close()
		}
		if !published && identity != nil {
			_ = removeAgentStateFileIfIdentity(root, tempName, identity)
		}
	}()
	identity, err = file.Stat()
	if err != nil {
		return fmt.Errorf("identify agent state temp file: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write agent state temp file: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync agent state temp file: %w", err)
	}
	if err := file.Chmod(perm); err != nil {
		return fmt.Errorf("protect agent state temp file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close agent state temp file: %w", err)
	}
	closed = true
	if named, err := root.Lstat(tempName); err != nil || !os.SameFile(identity, named) {
		return fmt.Errorf("agent state temp identity changed: %w", ErrUsagePersistencePoisoned)
	}
	if err := root.Rename(tempName, name); err != nil {
		return fmt.Errorf("publish agent state file: %w", err)
	}
	published = true
	publishedInfo, err := root.Lstat(name)
	if err != nil || !os.SameFile(identity, publishedInfo) {
		return fmt.Errorf("published agent state identity changed: %w", ErrUsagePersistenceIndeterminate)
	}
	if err := syncAgentStateRoot(root); err != nil {
		return fmt.Errorf("sync agent state directory: %w", errors.Join(ErrUsagePersistenceIndeterminate, err))
	}
	return nil
}

func syncAgentStateRoot(root *os.Root) error {
	if root == nil || runtime.GOOS == "windows" {
		return nil
	}
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil && !errors.Is(err, os.ErrInvalid) {
		return err
	}
	return nil
}

func loadOrCreateAgentStateKey(root *os.Root, name string, mustExist bool) ([]byte, error) {
	data, err := readAgentStateFile(root, name, 64)
	if errors.Is(err, os.ErrNotExist) {
		if mustExist {
			return nil, fmt.Errorf("required agent state identity key is missing: %w", ErrUsagePersistencePoisoned)
		}
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		if err := atomicWriteAgentStateFile(root, name, []byte(hex.EncodeToString(key)), 0o600); err != nil {
			return nil, err
		}
		return key, nil
	}
	if err != nil {
		return nil, err
	}
	key, err := decodeAESKey(data)
	if err != nil {
		return nil, fmt.Errorf("decode agent state identity key: %w", ErrUsagePersistencePoisoned)
	}
	return key, nil
}

// agentSessionPersistenceRoot adapts the same trusted state opener to
// agentcore's transport-neutral session persistence contract.
type agentSessionPersistenceRoot struct {
	root *os.Root
}

func (r agentSessionPersistenceRoot) OpenRegular(name string) (*os.File, error) {
	return openAgentStateRegularFile(r.root, name, os.O_RDONLY, 0)
}

func (r agentSessionPersistenceRoot) CreateExclusive(name string, perm os.FileMode) (*os.File, error) {
	return openAgentStateRegularFile(r.root, name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
}

func (r agentSessionPersistenceRoot) VerifyIdentity(name string, expected os.FileInfo) error {
	file, err := openAgentStateRegularFile(r.root, name, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	current, err := file.Stat()
	if err != nil {
		return err
	}
	if expected == nil || !os.SameFile(expected, current) {
		return fmt.Errorf("agent session state identity changed: %w", ErrUsagePersistencePoisoned)
	}
	return nil
}

func (r agentSessionPersistenceRoot) Rename(oldName, newName string) error {
	if r.root == nil || !validAgentStateLeaf(oldName) || !validAgentStateLeaf(newName) {
		return fmt.Errorf("agent session rename arguments are invalid: %w", ErrInvalidInput)
	}
	return r.root.Rename(oldName, newName)
}

func (r agentSessionPersistenceRoot) RemoveIfIdentity(name string, expected os.FileInfo) error {
	if r.root == nil || !validAgentStateLeaf(name) {
		return fmt.Errorf("agent session remove arguments are invalid: %w", ErrInvalidInput)
	}
	return removeAgentStateFileIfIdentity(r.root, name, expected)
}

func (r agentSessionPersistenceRoot) Sync() error {
	return syncAgentStateRoot(r.root)
}
