package services

import (
	"errors"
	"fmt"
	"io"
	"os"
)

var ErrFileConflict = errors.New("file changed on disk since it was opened")

type atomicFileWriter func() error

// WriteFileIfUnchanged replaces a file only when its current SHA-256 hash
// matches baselineHash. Editor saves use this method to avoid overwriting an
// external or second-window update made after the buffer was opened.
func (f *FileService) WriteFileIfUnchanged(path, content, baselineHash string) error {
	capability, err := f.acquireCapability(path, true)
	if err != nil {
		return err
	}
	defer capability.releaseCapability()
	displayPath := capability.displayPath()
	f.saveMu.Lock()
	defer f.saveMu.Unlock()
	if err := f.runRootOperationHook("WriteFileIfUnchanged"); err != nil {
		return err
	}
	err = capability.withCurrent(func() error {
		resolved, resolveErr := capability.resolvedRelative(true)
		if resolveErr != nil {
			return resolveErr
		}
		capability.relative = resolved
		var baselineIdentity os.FileInfo
		targetExisted := true
		file, openErr := capability.root.root.Open(resolved)
		if openErr != nil {
			if !os.IsNotExist(openErr) {
				return fmt.Errorf("read file save baseline: %w", openErr)
			}
			targetExisted = false
			if baselineHash != contentHash(nil) {
				return fmt.Errorf("%w: %s", ErrFileConflict, capability.displayPath())
			}
		} else {
			var statErr error
			baselineIdentity, statErr = file.Stat()
			if statErr != nil {
				_ = file.Close()
				return fmt.Errorf("identify file save baseline: %w", statErr)
			}
			current, readErr := io.ReadAll(io.LimitReader(file, maxReadableFileBytes+1))
			closeErr := file.Close()
			if readErr != nil {
				return fmt.Errorf("read file save baseline: %w", readErr)
			}
			if closeErr != nil {
				return fmt.Errorf("close file save baseline: %w", closeErr)
			}
			if int64(len(current)) > maxReadableFileBytes {
				return fmt.Errorf("file is too large to compare (%d byte limit): %w", maxReadableFileBytes, ErrInvalidInput)
			}
			if contentHash(current) != baselineHash {
				return fmt.Errorf("%w: %s", ErrFileConflict, capability.displayPath())
			}
		}
		return f.writeCapabilityFile(capability, content, func() error {
			if !targetExisted {
				_, statErr := capability.root.root.Stat(capability.relative)
				if statErr == nil {
					return fmt.Errorf("%w: %s", ErrFileConflict, capability.displayPath())
				}
				if !os.IsNotExist(statErr) {
					return fmt.Errorf("%w: %s: %v", ErrFileConflict, capability.displayPath(), statErr)
				}
				return nil
			}
			return validateCapabilityBaseline(capability, baselineIdentity, baselineHash)
		})
	})
	if err != nil {
		return err
	}
	f.emitFileSaved(displayPath)
	return nil
}

func validateCapabilityBaseline(capability fileCapability, baselineIdentity os.FileInfo, baselineHash string) error {
	file, err := capability.root.root.Open(capability.relative)
	if err != nil {
		return fmt.Errorf("%w: reopen %s before save: %v", ErrFileConflict, capability.displayPath(), err)
	}
	currentIdentity, statErr := file.Stat()
	if statErr != nil {
		_ = file.Close()
		return fmt.Errorf("%w: identify %s before save: %v", ErrFileConflict, capability.displayPath(), statErr)
	}
	current, readErr := io.ReadAll(io.LimitReader(file, maxReadableFileBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		return fmt.Errorf("%w: reread %s before save: %v", ErrFileConflict, capability.displayPath(), readErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close file before save: %w", closeErr)
	}
	if int64(len(current)) > maxReadableFileBytes {
		return fmt.Errorf("%w: %s exceeds the comparison limit", ErrFileConflict, capability.displayPath())
	}
	if !os.SameFile(baselineIdentity, currentIdentity) || contentHash(current) != baselineHash {
		return fmt.Errorf("%w: %s", ErrFileConflict, capability.displayPath())
	}
	return nil
}

func (f *FileService) writeCapabilityFile(capability fileCapability, content string, beforeCommit func() error) error {
	resolved, err := capability.resolvedRelative(true)
	if err != nil {
		return err
	}
	capability.relative = resolved
	perm := os.FileMode(0o644)
	info, err := capability.root.root.Stat(resolved)
	if err == nil {
		perm = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat file before save: %w", err)
	}
	if f.writeAtomic != nil {
		if err := f.writeAtomic(); err != nil {
			return err
		}
	}
	if err := atomicWriteFileWithinRoot(capability, []byte(content), perm, beforeCommit); err != nil {
		return err
	}
	return nil
}
