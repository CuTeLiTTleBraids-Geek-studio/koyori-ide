package agentcore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type recordingSessionPersistenceRoot struct {
	dir                  string
	createdTemp          string
	publishedTemp        string
	removedPublishedTemp bool
	replaceBeforeVerify  bool
	replacement          []byte
}

func (r *recordingSessionPersistenceRoot) OpenRegular(name string) (*os.File, error) {
	return os.Open(filepath.Join(r.dir, name))
}

func (r *recordingSessionPersistenceRoot) CreateExclusive(name string, perm os.FileMode) (*os.File, error) {
	r.createdTemp = name
	return os.OpenFile(filepath.Join(r.dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
}

func (r *recordingSessionPersistenceRoot) VerifyIdentity(name string, expected os.FileInfo) error {
	if r.replaceBeforeVerify && name == r.createdTemp {
		r.replaceBeforeVerify = false
		if err := os.Remove(filepath.Join(r.dir, name)); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(r.dir, name), r.replacement, 0o600); err != nil {
			return err
		}
		return errors.New("injected session temp identity replacement")
	}
	current, err := os.Stat(filepath.Join(r.dir, name))
	if err != nil {
		return err
	}
	if expected == nil || !os.SameFile(expected, current) {
		return errors.New("session persistence identity changed")
	}
	return nil
}

func (r *recordingSessionPersistenceRoot) Rename(oldName, newName string) error {
	if err := os.Rename(filepath.Join(r.dir, oldName), filepath.Join(r.dir, newName)); err != nil {
		return err
	}
	r.publishedTemp = oldName
	return nil
}

func (r *recordingSessionPersistenceRoot) RemoveIfIdentity(name string, expected os.FileInfo) error {
	if r.publishedTemp != "" && name == r.publishedTemp {
		r.removedPublishedTemp = true
	}
	current, statErr := os.Stat(filepath.Join(r.dir, name))
	if statErr != nil {
		return statErr
	}
	if expected == nil || !os.SameFile(expected, current) {
		return errors.New("session persistence cleanup identity changed")
	}
	err := os.Remove(filepath.Join(r.dir, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (r *recordingSessionPersistenceRoot) Sync() error { return nil }

func TestFileSessionStorePersistenceDoesNotRemovePublishedTempName(t *testing.T) {
	root := &recordingSessionPersistenceRoot{dir: t.TempDir()}
	state, err := (FileSessionStorePersistence{
		Root: root,
		Name: "sessions.json",
	}).Save(nil)
	if err != nil || state != PersistenceDurable {
		t.Fatalf("Save state = %v, err = %v, want durable", state, err)
	}
	if root.removedPublishedTemp {
		t.Fatal("successful Save removed the published temp name during deferred cleanup")
	}
}

func TestFileSessionStorePersistenceDoesNotRemoveReplacedFailedTemp(t *testing.T) {
	replacement := []byte("replacement-temp-must-survive")
	root := &recordingSessionPersistenceRoot{
		dir:                 t.TempDir(),
		replaceBeforeVerify: true,
		replacement:         replacement,
	}
	state, err := (FileSessionStorePersistence{
		Root: root,
		Name: "sessions.json",
	}).Save(nil)
	if err == nil || state != PersistenceNotPublished {
		t.Fatalf("Save state = %v, err = %v, want not-published identity failure", state, err)
	}
	after, readErr := os.ReadFile(filepath.Join(root.dir, root.createdTemp))
	if readErr != nil {
		t.Fatalf("read replacement temp: %v", readErr)
	}
	if string(after) != string(replacement) {
		t.Fatalf("replacement temp = %q, want %q", after, replacement)
	}
}
