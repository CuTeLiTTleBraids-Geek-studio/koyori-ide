//go:build windows

package services

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const lspProcessTreeTestTimeout = 10 * time.Second

func TestLSPWindowsProcessTreeHelper(t *testing.T) {
	mode := os.Getenv("KOYORI_IDE_LSP_TREE_HELPER")
	if mode == "" {
		return
	}
	if mode == "child" {
		for {
			time.Sleep(time.Second)
		}
	}
	if mode != "parent" {
		t.Fatalf("unknown process-tree helper mode %q", mode)
	}

	releasePath := os.Getenv("KOYORI_IDE_LSP_TREE_RELEASE")
	childPIDPath := os.Getenv("KOYORI_IDE_LSP_TREE_CHILD_PID")
	deadline := time.Now().Add(lspProcessTreeTestTimeout)
	for {
		if _, err := os.Stat(releasePath); err == nil {
			break
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for process-tree release")
		}
		time.Sleep(10 * time.Millisecond)
	}

	child := exec.Command(os.Args[0], "-test.run=^TestLSPWindowsProcessTreeHelper$")
	child.Env = append(os.Environ(), "KOYORI_IDE_LSP_TREE_HELPER=child")
	if err := child.Start(); err != nil {
		t.Fatalf("start child helper: %v", err)
	}
	if err := os.WriteFile(childPIDPath, []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
		_ = child.Process.Kill()
		_ = child.Wait()
		t.Fatalf("write child PID: %v", err)
	}
	if err := child.Wait(); err != nil {
		os.Exit(0)
	}
}

func TestLSPWindowsProcessTreeStopsDescendantsAndPreservesUnrelated(t *testing.T) {
	testRoot := t.TempDir()
	releasePath := filepath.Join(testRoot, "release")
	childPIDPath := filepath.Join(testRoot, "child.pid")

	parent := exec.Command(os.Args[0], "-test.run=^TestLSPWindowsProcessTreeHelper$")
	parent.Env = append(
		os.Environ(),
		"KOYORI_IDE_LSP_TREE_HELPER=parent",
		"KOYORI_IDE_LSP_TREE_RELEASE="+releasePath,
		"KOYORI_IDE_LSP_TREE_CHILD_PID="+childPIDPath,
	)
	if err := parent.Start(); err != nil {
		t.Fatalf("start parent helper: %v", err)
	}
	tree, err := attachLSPProcessTree(parent)
	if err != nil {
		_ = parent.Process.Kill()
		_ = parent.Wait()
		t.Fatalf("attach process tree: %v", err)
	}
	process := newLSPProcess(parent, tree)
	t.Cleanup(func() { _ = process.stop(lspProcessTreeTestTimeout) })

	unrelated := exec.Command(os.Args[0], "-test.run=^TestLSPWindowsProcessTreeHelper$")
	unrelated.Env = append(os.Environ(), "KOYORI_IDE_LSP_TREE_HELPER=child")
	if err := unrelated.Start(); err != nil {
		t.Fatalf("start unrelated helper: %v", err)
	}
	t.Cleanup(func() {
		_ = unrelated.Process.Kill()
		_ = unrelated.Wait()
	})

	if err := os.WriteFile(releasePath, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	childPID := waitForLSPChildPID(t, childPIDPath)
	if !processAlive(childPID) {
		t.Fatalf("managed child PID %d exited before stop", childPID)
	}

	if err := process.stop(lspProcessTreeTestTimeout); err != nil {
		t.Fatalf("stop managed process tree: %v", err)
	}
	if processAlive(parent.Process.Pid) {
		t.Fatalf("managed parent PID %d is still alive", parent.Process.Pid)
	}
	if processAlive(childPID) {
		t.Fatalf("managed child PID %d is still alive", childPID)
	}
	if !processAlive(unrelated.Process.Pid) {
		t.Fatalf("unrelated PID %d was terminated", unrelated.Process.Pid)
	}
}

func TestLSPWindowsProcessTreeAdoptsPreexistingDescendant(t *testing.T) {
	testRoot := t.TempDir()
	releasePath := filepath.Join(testRoot, "release")
	childPIDPath := filepath.Join(testRoot, "child.pid")
	if err := os.WriteFile(releasePath, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}

	parent := exec.Command(os.Args[0], "-test.run=^TestLSPWindowsProcessTreeHelper$")
	parent.Env = append(
		os.Environ(),
		"KOYORI_IDE_LSP_TREE_HELPER=parent",
		"KOYORI_IDE_LSP_TREE_RELEASE="+releasePath,
		"KOYORI_IDE_LSP_TREE_CHILD_PID="+childPIDPath,
	)
	if err := parent.Start(); err != nil {
		t.Fatalf("start parent helper: %v", err)
	}
	childPID := waitForLSPChildPID(t, childPIDPath)
	t.Cleanup(func() {
		if child, err := os.FindProcess(childPID); err == nil {
			_ = child.Kill()
		}
	})

	tree, err := attachLSPProcessTree(parent)
	if err != nil {
		_ = parent.Process.Kill()
		_ = parent.Wait()
		t.Fatalf("attach process tree: %v", err)
	}
	process := newLSPProcess(parent, tree)
	t.Cleanup(func() { _ = process.stop(lspProcessTreeTestTimeout) })

	if err := process.stop(lspProcessTreeTestTimeout); err != nil {
		t.Fatalf("stop managed process tree: %v", err)
	}
	if processAlive(childPID) {
		t.Fatalf("preexisting managed child PID %d escaped the process job", childPID)
	}
}

func TestLSPWindowsProcessTreeRejectsStaleRootIdentityWithoutAdopting(t *testing.T) {
	testRoot := t.TempDir()
	releasePath := filepath.Join(testRoot, "release")
	childPIDPath := filepath.Join(testRoot, "child.pid")
	parent := exec.Command(os.Args[0], "-test.run=^TestLSPWindowsProcessTreeHelper$")
	parent.Env = append(
		os.Environ(),
		"KOYORI_IDE_LSP_TREE_HELPER=parent",
		"KOYORI_IDE_LSP_TREE_RELEASE="+releasePath,
		"KOYORI_IDE_LSP_TREE_CHILD_PID="+childPIDPath,
	)
	if err := parent.Start(); err != nil {
		t.Fatalf("start parent helper: %v", err)
	}
	t.Cleanup(func() {
		_ = parent.Process.Kill()
		_ = parent.Wait()
	})
	start, _, err := processInfo(parent.Process.Pid)
	if err != nil {
		t.Fatalf("read parent identity: %v", err)
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		t.Fatalf("create process identity test job: %v", err)
	}
	t.Cleanup(func() { _ = windows.CloseHandle(job) })

	// A stale incarnation must not trigger a global PID walk. The helper
	// remains outside the test Job and remains alive until cleanup, proving that
	// no unrelated/reused PID was assigned or terminated.
	if err := adoptExistingLSPDescendants(job, uint32(parent.Process.Pid), start+1, 100*time.Millisecond); err != nil {
		t.Fatalf("stale root adoption should fail closed without an operational error: %v", err)
	}
	if !processAlive(parent.Process.Pid) {
		t.Fatal("managed helper was terminated while rejecting stale root identity")
	}
}

func waitForLSPChildPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(lspProcessTreeTestTimeout)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(string(raw))
			if parseErr != nil || pid <= 0 {
				t.Fatalf("invalid child PID %q: %v", raw, parseErr)
			}
			return pid
		}
		if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for child PID file %s", path)
	return 0
}
