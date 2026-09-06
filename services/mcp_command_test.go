package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestResolveMCPStdioCommand_BindsWorkspaceRoot(t *testing.T) {
	root := canonicalTestPath(t, t.TempDir())
	commandPath := filepath.Join(root, "tools", "server")
	if err := os.MkdirAll(filepath.Dir(commandPath), 0o700); err != nil {
		t.Fatalf("mkdir command directory: %v", err)
	}
	if err := os.WriteFile(commandPath, []byte("placeholder"), 0o700); err != nil {
		t.Fatalf("write command: %v", err)
	}

	got, workDir, err := resolveMCPStdioCommand(root, filepath.Join("tools", "server"))
	if err != nil {
		t.Fatalf("resolve workspace command: %v", err)
	}
	if got != filepath.Clean(commandPath) {
		t.Fatalf("resolved command = %q, want %q", got, filepath.Clean(commandPath))
	}
	if workDir != filepath.Clean(root) {
		t.Fatalf("workDir = %q, want %q", workDir, filepath.Clean(root))
	}

	outside := filepath.Join(t.TempDir(), "server")
	if _, _, err := resolveMCPStdioCommand(root, outside); err == nil {
		t.Fatal("workspace-bound command outside root was accepted")
	}
}

func TestResolveMCPStdioCommand_UnscopedCanonicalizesExecutable(t *testing.T) {
	command := "echo"
	if runtime.GOOS == "windows" {
		command = "cmd.exe"
	}
	got, workDir, err := resolveMCPStdioCommand("", command)
	if err != nil {
		t.Fatalf("resolve unscoped command: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("resolved command = %q, want absolute path", got)
	}
	if workDir != "" {
		t.Fatalf("unscoped workDir = %q, want empty", workDir)
	}
	if strings.Contains(strings.ReplaceAll(got, "\\", "/"), "//?/") {
		t.Fatalf("resolved device namespace unexpectedly: %q", got)
	}
}

func TestMCPChildEnvironment_DoesNotInheritAmbientSecrets(t *testing.T) {
	t.Setenv("KOYORI_MCP_AMBIENT_SECRET", "must-not-leak")
	t.Setenv("PATH", os.Getenv("PATH"))
	env := mcpChildEnvironment(map[string]string{"KOYORI_MCP_EXPLICIT": "allowed"})
	joined := "\n" + strings.Join(env, "\n") + "\n"
	if strings.Contains(joined, "KOYORI_MCP_AMBIENT_SECRET=must-not-leak") {
		t.Fatal("ambient secret leaked into MCP child environment")
	}
	if !strings.Contains(joined, "KOYORI_MCP_EXPLICIT=allowed") {
		t.Fatal("explicit MCP environment override was dropped")
	}
	if !strings.Contains(joined, "PATH=") {
		t.Fatal("MCP child environment lost PATH")
	}
}

func TestValidateMCPEnvironmentRejectsExecutionOverrides(t *testing.T) {
	for _, key := range []string{"PATH", "COMSPEC", "LD_PRELOAD", "DYLD_INSERT_LIBRARIES", "NODE_OPTIONS"} {
		if err := validateMCPEnvironment(map[string]string{key: "attacker-controlled"}); err == nil {
			t.Errorf("validateMCPEnvironment accepted execution override %q", key)
		}
	}
	if err := validateMCPEnvironment(map[string]string{"MCP_API_TOKEN": "explicit-secret"}); err != nil {
		t.Fatalf("explicit application secret was rejected: %v", err)
	}
}

func TestMCPServiceSaveServerDetachesClientWhenConnectionIdentityChanges(t *testing.T) {
	service, transport := newMCPPersistenceFailureFixture(t)
	if err := service.SaveServer(MCPServerConfig{
		Name:      "srv",
		Transport: "stdio",
		Command:   "different-command",
	}); err != nil {
		t.Fatalf("SaveServer: %v", err)
	}
	service.mu.RLock()
	_, connected := service.clients["srv"]
	service.mu.RUnlock()
	if connected {
		t.Fatal("connection remained installed after endpoint identity changed")
	}
	if got := transport.closes(); got != 1 {
		t.Fatalf("old transport close count = %d, want 1", got)
	}
}

func TestMCPServiceRejectsExecutableReplacementAfterNativeApproval(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(t.TempDir(), "replacement-started")
	launcher := filepath.Join(root, "mcp-server")
	if runtime.GOOS == "windows" {
		launcher += ".cmd"
	}
	writeMCPTestLauncher(t, launcher, false)

	service := newTestMCPService(t)
	if err := service.setWorkspaceRoot(root); err != nil {
		t.Fatalf("set workspace root: %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	service.approveServer = func(cfg MCPServerConfig) bool {
		if !filepath.IsAbs(cfg.Command) {
			t.Errorf("native approval command = %q, want absolute path", cfg.Command)
		}
		return true
	}
	server := MCPServerConfig{
		Name: "replacement", Transport: "stdio", Command: filepath.Base(launcher),
		Env: map[string]string{
			"KOYORI_IDE_FAKE_MCP":               "1",
			"KOYORI_IDE_MCP_TEST_BINARY":        os.Args[0],
			"KOYORI_IDE_MCP_REPLACEMENT_MARKER": marker,
		},
	}
	if err := service.SaveServer(server); err != nil {
		t.Fatalf("save server: %v", err)
	}
	if err := service.SetServerEnabled(server.Name, true); err != nil {
		t.Fatalf("enable server: %v", err)
	}

	replacement := launcher + ".replacement"
	writeMCPTestLauncher(t, replacement, true)
	if runtime.GOOS == "windows" {
		if err := os.Remove(launcher); err != nil {
			t.Fatalf("remove approved launcher: %v", err)
		}
	}
	if err := os.Rename(replacement, launcher); err != nil {
		t.Fatalf("replace approved launcher: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := service.ConnectServer(ctx, server.Name)
	if err == nil {
		_ = service.DisconnectServer(server.Name)
		t.Fatal("ConnectServer executed an executable replaced after native approval")
	}
	if !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("ConnectServer error = %v, want ErrNotAllowed", err)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("replacement executable started; marker stat error = %v", statErr)
	}
}

func writeMCPTestLauncher(t *testing.T, path string, writesMarker bool) {
	t.Helper()
	var script string
	if runtime.GOOS == "windows" {
		script = "@echo off\r\n"
		if writesMarker {
			script += ">\"%KOYORI_IDE_MCP_REPLACEMENT_MARKER%\" echo replacement\r\n"
		}
		script += "\"%KOYORI_IDE_MCP_TEST_BINARY%\" -test.run=^TestHelperFakeMCPServer$ -test.timeout=60s\r\n"
	} else {
		script = "#!/bin/sh\n"
		if writesMarker {
			script += "printf replacement > \"$KOYORI_IDE_MCP_REPLACEMENT_MARKER\"\n"
		}
		script += "exec \"$KOYORI_IDE_MCP_TEST_BINARY\" -test.run='^TestHelperFakeMCPServer$' -test.timeout=60s\n"
	}
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write MCP test launcher: %v", err)
	}
}
