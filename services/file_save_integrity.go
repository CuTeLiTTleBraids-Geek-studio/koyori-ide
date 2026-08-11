package services

import (
	"errors"
	"fmt"
	"os"
)

var ErrFileConflict = errors.New("file changed on disk since it was opened")

type atomicFileWriter func(path string, data []byte, perm os.FileMode) error

// WriteFileIfUnchanged replaces a file only when its current SHA-256 hash
// matches baselineHash. Editor saves use this method to avoid overwriting an
// external or second-window update made after the buffer was opened.
func (f *FileService) WriteFileIfUnchanged(path, content, baselineHash string) error {
	abs, err := f.validateMutatingPath(path)
	if err != nil {
		return err
	}
	f.saveMu.Lock()
	defer f.saveMu.Unlock()

	current, err := os.ReadFile(abs)
	if err != nil {
		return fmt.Errorf("read file save baseline: %w", err)
	}
	if contentHash(current) != baselineHash {
		return fmt.Errorf("%w: %s", ErrFileConflict, abs)
	}
	return f.writeValidatedFile(abs, content)
}

func (f *FileService) writeValidatedFile(path, content string) error {
	perm := os.FileMode(0o644)
	info, err := os.Stat(path)
	if err == nil {
		perm = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat file before save: %w", err)
	}
	writer := f.writeAtomic
	if writer == nil {
		writer = atomicWriteFile
	}
	if err := writer(path, []byte(content), perm); err != nil {
		return err
	}
	f.emitFileSaved(path)
	return nil
}
