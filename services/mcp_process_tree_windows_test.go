//go:build windows

package services

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

const (
	mcpTreeHelperMode    = "KOYORI_IDE_MCP_TREE_HELPER"
	mcpTreeParentPIDPath = "KOYORI_IDE_MCP_TREE_PARENT_PID"
	mcpTreeChildPIDPath  = "KOYORI_IDE_MCP_TREE_CHILD_PID"
	mcpTreeHelperTimeout = 15 * time.Second
)

func TestMCPWindowsProcessTreeChild(t *testing.T) {
	if os.Getenv(mcpTreeHelperMode) != "child" {
		t.Skip("helper only runs from the MCP process-tree server")
	}
	select {}
}

func TestMCPWindowsProcessTreeServer(t *testing.T) {
	if os.Getenv(mcpTreeHelperMode) != "server" {
		t.Skip("helper only runs from the MCP process-tree launcher")
	}
	if err := os.WriteFile(os.Getenv(mcpTreeParentPIDPath), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "write MCP parent PID: %v\n", err)
		os.Exit(2)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestMCPWindowsProcessTreeChild$", "-test.timeout=60s")
	child.Env = append(os.Environ(), mcpTreeHelperMode+"=child")
	if err := child.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "start MCP child: %v\n", err)
		os.Exit(3)
	}
	if err := os.WriteFile(os.Getenv(mcpTreeChildPIDPath), []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
		_ = child.Process.Kill()
		_ = child.Wait()
		fmt.Fprintf(os.Stderr, "write MCP child PID: %v\n", err)
		os.Exit(4)
	}
	TestHelperFakeMCPServer(t)
}

func TestMCPWindowsCmdTransportStopsDescendantTree(t *testing.T) {
	tests := []struct {
		name     string
		teardown func(*MCPService, string, string) error
	}{
		{
			name: "disconnect",
			teardown: func(service *MCPService, name, _ string) error {
				return service.DisconnectServer(name)
			},
		},
		{
			name: "delete",
			teardown: func(service *MCPService, name, _ string) error {
				return service.DeleteServer(name)
			},
		},
		{
			name: "disable",
			teardown: func(service *MCPService, name, _ string) error {
				return service.SetServerEnabled(name, false)
			},
		},
		{
			name: "workspace-switch",
			teardown: func(service *MCPService, _, nextRoot string) error {
				return service.setWorkspaceRoot(nextRoot)
			},
		},
		{
			name: "close",
			teardown: func(service *MCPService, _, _ string) error {
				return service.Close()
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, serverName, nextRoot, parentPID, childPID := startMCPWindowsTree(t)
			if err := test.teardown(service, serverName, nextRoot); err != nil {
				t.Fatalf("%s MCP tree teardown: %v", test.name, err)
			}
			waitForMCPProcessExit(t, parentPID)
			waitForMCPProcessExit(t, childPID)
		})
	}
}

func startMCPWindowsTree(t *testing.T) (*MCPService, string, string, int, int) {
	t.Helper()
	root := t.TempDir()
	nextRoot := t.TempDir()
	parentPIDPath := filepath.Join(t.TempDir(), "parent.pid")
	childPIDPath := filepath.Join(t.TempDir(), "child.pid")
	launcher := filepath.Join(root, "mcp-tree.cmd")
	script := "@echo off\r\n\"%KOYORI_IDE_MCP_TEST_BINARY%\" -test.run=^TestMCPWindowsProcessTreeServer$ -test.timeout=60s\r\n"
	if err := os.WriteFile(launcher, []byte(script), 0o700); err != nil {
		t.Fatalf("write MCP tree launcher: %v", err)
	}

	service := newTestMCPService(t)
	if err := service.setWorkspaceRoot(root); err != nil {
		t.Fatalf("set MCP workspace root: %v", err)
	}
	server := MCPServerConfig{
		Name: "tree", Transport: "stdio", Command: filepath.Base(launcher),
		Env: map[string]string{
			"KOYORI_IDE_FAKE_MCP":        "1",
			"KOYORI_IDE_MCP_TEST_BINARY": os.Args[0],
			mcpTreeHelperMode:            "server",
			mcpTreeParentPIDPath:         parentPIDPath,
			mcpTreeChildPIDPath:          childPIDPath,
		},
	}
	if err := service.SaveServer(server); err != nil {
		t.Fatalf("save MCP tree server: %v", err)
	}
	if err := service.SetServerEnabled(server.Name, true); err != nil {
		t.Fatalf("enable MCP tree server: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), mcpTreeHelperTimeout)
	defer cancel()
	if err := service.ConnectServer(ctx, server.Name); err != nil {
		t.Fatalf("connect MCP tree server: %v", err)
	}
	parentPID := waitForMCPProcessPID(t, parentPIDPath)
	childPID := waitForMCPProcessPID(t, childPIDPath)
	t.Cleanup(func() {
		_ = service.Close()
		killMCPTestProcess(parentPID)
		killMCPTestProcess(childPID)
	})
	return service, server.Name, nextRoot, parentPID, childPID
}

func waitForMCPProcessPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(mcpTreeHelperTimeout)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(string(raw))
			if parseErr != nil || pid <= 0 {
				t.Fatalf("invalid MCP helper PID %q: %v", raw, parseErr)
			}
			return pid
		}
		if !os.IsNotExist(err) {
			t.Fatalf("read MCP helper PID: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for MCP helper PID file %s", path)
	return 0
}

func waitForMCPProcessExit(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(mcpTreeHelperTimeout)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("MCP process PID %d survived teardown", pid)
}

func killMCPTestProcess(pid int) {
	if pid <= 0 || !processAlive(pid) {
		return
	}
	if process, err := os.FindProcess(pid); err == nil {
		_ = process.Kill()
	}
}
