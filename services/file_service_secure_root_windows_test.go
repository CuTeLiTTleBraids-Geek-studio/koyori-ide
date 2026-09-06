//go:build windows

package services

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func createWindowsJunction(t *testing.T, junction, target string) {
	t.Helper()
	output, err := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", junction, target).CombinedOutput()
	if err != nil {
		t.Fatalf("create junction %q -> %q: %v\n%s", junction, target, err, output)
	}
}

func TestFileService_H1_WindowsJunctionParentSwap_AllOperationsFailClosed(t *testing.T) {
	operations := []struct {
		name string
		run  func(*FileService, string) error
	}{
		{"ReadFile", func(s *FileService, parent string) error {
			_, err := s.ReadFile(filepath.Join(parent, "item.txt"))
			return err
		}},
		{"WriteFile", func(s *FileService, parent string) error {
			return s.WriteFile(filepath.Join(parent, "item.txt"), "changed")
		}},
		{"CreateFile", func(s *FileService, parent string) error {
			return s.CreateFile(filepath.Join(parent, "created.txt"))
		}},
		{"CreateDirectory", func(s *FileService, parent string) error {
			return s.CreateDirectory(filepath.Join(parent, "created-dir"))
		}},
		{"DeletePath", func(s *FileService, parent string) error {
			return s.DeletePath(filepath.Join(parent, "item.txt"))
		}},
		{"RenamePath", func(s *FileService, parent string) error {
			return s.RenamePath(filepath.Join(parent, "item.txt"), filepath.Join(parent, "renamed.txt"))
		}},
		{"ListDirectory", func(s *FileService, parent string) error {
			_, err := s.ListDirectory(parent)
			return err
		}},
		{"ListAllFiles", func(s *FileService, parent string) error {
			_, err := s.ListAllFiles(parent)
			return err
		}},
		{"WriteFileIfUnchanged", func(s *FileService, parent string) error {
			return s.WriteFileIfUnchanged(filepath.Join(parent, "item.txt"), "changed", contentHash([]byte("inside")))
		}},
	}

	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			base := t.TempDir()
			workspace := filepath.Join(base, "workspace")
			outside := filepath.Join(base, "outside")
			parent := filepath.Join(workspace, "parent")
			if err := os.MkdirAll(parent, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(outside, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(parent, "item.txt"), []byte("inside"), 0o600); err != nil {
				t.Fatal(err)
			}
			outsideFile := filepath.Join(outside, "item.txt")
			if err := os.WriteFile(outsideFile, []byte("outside-sentinel"), 0o600); err != nil {
				t.Fatal(err)
			}

			detached := parent + "-detached"
			service := newFileServiceAt(t, workspace)
			service.rootOperationHook = func(got string) error {
				if got != operation.name {
					return nil
				}
				if err := os.Rename(parent, detached); err != nil {
					return err
				}
				createWindowsJunction(t, parent, outside)
				return nil
			}

			if err := operation.run(service, parent); err == nil {
				t.Fatalf("%s succeeded after parent junction swap", operation.name)
			}
			assertFileContent(t, outsideFile, "outside-sentinel")
			if _, err := os.Stat(filepath.Join(outside, "created.txt")); !os.IsNotExist(err) {
				t.Fatalf("outside create result changed: %v", err)
			}
			if _, err := os.Stat(filepath.Join(outside, "created-dir")); !os.IsNotExist(err) {
				t.Fatalf("outside mkdir result changed: %v", err)
			}
		})
	}
}
