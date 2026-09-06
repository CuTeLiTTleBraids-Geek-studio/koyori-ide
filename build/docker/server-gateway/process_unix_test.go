//go:build !windows

package main

import (
	"bufio"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestStopBackendTerminatesProcessGroup(t *testing.T) {
	cmd, childPID := startBackendFixture(t, "sleep 30 & echo $!; wait")
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	stopBackendWithTimeout(cmd, done, 2*time.Second)
	assertProcessGone(t, childPID)
}

func TestKillBackendCleansDescendantsAfterParentExit(t *testing.T) {
	cmd, childPID := startBackendFixture(t, "sleep 30 & echo $!; exit 0")
	if err := cmd.Wait(); err != nil {
		t.Fatalf("backend parent wait: %v", err)
	}
	if err := killBackend(cmd); err != nil {
		t.Fatalf("kill backend process group: %v", err)
	}
	assertProcessGone(t, childPID)
}

func startBackendFixture(t *testing.T, script string) (*exec.Cmd, int) {
	t.Helper()
	cmd := exec.Command("sh", "-c", script)
	configureBackendProcess(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = killBackend(cmd) })
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("read child PID: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("parse child PID %q: %v", line, err)
	}
	return cmd, pid
}

func assertProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("child process %d still exists after backend cleanup", pid)
}
