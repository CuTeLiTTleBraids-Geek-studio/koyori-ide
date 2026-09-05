package services

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDebugAdapterBridgeDeniesMissingPolicy(t *testing.T) {
	_, err := newDebugAdapterCommand(context.Background(), debugAdapterLaunchPolicy{}, debugAdapterLaunchRequest{
		Executable: os.Args[0],
	})
	if err == nil || !strings.Contains(err.Error(), "workspace root is required") {
		t.Fatalf("error = %v, want missing workspace rejection", err)
	}
}

func TestDebugAdapterBridgeRequiresExactAllowlistedExecutable(t *testing.T) {
	workspace := t.TempDir()
	_, err := newDebugAdapterCommand(context.Background(), debugAdapterLaunchPolicy{
		WorkspaceRoot:      workspace,
		AllowedExecutables: []string{filepath.Join(workspace, "missing-adapter")},
	}, debugAdapterLaunchRequest{Executable: os.Args[0]})
	if err == nil || !strings.Contains(err.Error(), "not allowlisted") {
		t.Fatalf("error = %v, want allowlist rejection", err)
	}
}

func TestDebugAdapterBridgeRejectsAllowlistPrefixBypass(t *testing.T) {
	workspace := t.TempDir()
	_, err := newDebugAdapterCommand(context.Background(), debugAdapterLaunchPolicy{
		WorkspaceRoot:      workspace,
		AllowedExecutables: []string{os.Args[0] + "-trusted"},
	}, debugAdapterLaunchRequest{Executable: os.Args[0]})
	if err == nil || !strings.Contains(err.Error(), "not allowlisted") {
		t.Fatalf("error = %v, want exact allowlist rejection", err)
	}
}

func TestDebugAdapterBridgeRejectsOutsideWorkspaceCwd(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	_, err := newDebugAdapterCommand(context.Background(), debugAdapterLaunchPolicy{
		WorkspaceRoot:      workspace,
		AllowedExecutables: []string{os.Args[0]},
	}, debugAdapterLaunchRequest{Executable: os.Args[0], Cwd: outside})
	if err == nil || !strings.Contains(err.Error(), "outside workspace") {
		t.Fatalf("error = %v, want cwd rejection", err)
	}
}

func TestDebugAdapterBridgeStartsDirectProcessWithLiteralArguments(t *testing.T) {
	workspace := canonicalTestPath(t, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	process, err := startDebugAdapter(ctx, debugAdapterLaunchPolicy{
		WorkspaceRoot:      workspace,
		AllowedExecutables: []string{os.Args[0]},
	}, debugAdapterLaunchRequest{
		Executable: os.Args[0],
		Args:       []string{"-test.run=^TestDebugAdapterProcessHelper$", "--", "$(not-a-shell)"},
		Cwd:        ".",
	})
	if err != nil {
		t.Fatalf("startDebugAdapter: %v", err)
	}
	_ = process.Stdin.Close()
	output, readErr := io.ReadAll(process.Stdout)
	if readErr != nil {
		t.Fatalf("read adapter stdout: %v", readErr)
	}
	if err := process.Cmd.Wait(); err != nil {
		stderr, _ := io.ReadAll(process.Stderr)
		t.Fatalf("wait for adapter: %v: %s", err, stderr)
	}
	if strings.TrimSpace(string(output)) != workspace+"\n$(not-a-shell)" {
		t.Fatalf("adapter output = %q", output)
	}
}

func TestDebugAdapterProcessHelper(t *testing.T) {
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 {
		return
	}
	cwd, err := os.Getwd()
	if err != nil {
		os.Exit(2)
	}
	_, _ = os.Stdout.WriteString(cwd + "\n" + os.Args[separator+1])
	os.Exit(0)
}
