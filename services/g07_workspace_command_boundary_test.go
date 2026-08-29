package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func setWorkspaceContextRoot(t *testing.T, ctx *WorkspaceContext, root string) {
	t.Helper()
	if err := ctx.Set(root); err != nil {
		t.Fatalf("set workspace context root: %v", err)
	}
}

func assertNotAllowed(t *testing.T, err error) {
	t.Helper()
	if err == nil || !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("expected ErrNotAllowed, got %v", err)
	}
}

func TestG07CommandEntrypointsRejectEmptySharedWorkspace(t *testing.T) {
	ctx := NewWorkspaceContext()
	outside := t.TempDir()
	program := filepath.Join(outside, "main.js")
	if err := os.WriteFile(program, []byte("console.log('no')\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	coverage := NewCoverageServiceWithWorkspaceContext(ctx)
	if _, err := coverage.RunPackageCoverage(""); err == nil {
		t.Fatal("coverage command accepted an empty shared workspace")
	}

	toolchain := NewToolchainServiceWithWorkspaceContext(ctx)
	if _, err := toolchain.RunToolchainCommand("go-build", ""); err == nil {
		t.Fatal("toolchain command accepted an empty shared workspace")
	}

	eslint := NewEslintServiceWithWorkspaceContext(ctx)
	if _, err := eslint.LintFile(program, "", ""); err == nil {
		t.Fatal("eslint command accepted an empty shared workspace")
	}

	debug := NewDebugServiceWithWorkspaceContext(ctx)
	if _, err := debug.LaunchWithConfig(DebugLaunchConfig{Kind: "node", Program: program}); err == nil {
		t.Fatal("debug launch accepted an empty shared workspace")
	}

	terminal := NewTerminalServiceWithWorkspaceContext(ctx)
	if err := terminal.StartSession("empty-root", outside, ""); err == nil {
		t.Fatal("terminal launch accepted an empty shared workspace")
	}

	agent := NewAgentServiceWithWorkspaceContext(ctx)
	t.Cleanup(func() { _ = agent.Close() })
	agent.approveCommand = func(string, string, RiskLevel) bool { return true }
	if _, err := agent.requestCommandApprovalLegacy("go version", outside); err == nil {
		t.Fatal("agent command approval accepted an empty shared workspace")
	}
}

func TestG07CommandEntrypointsRejectWorkspaceEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "outside.js")
	if err := os.WriteFile(outsideFile, []byte("console.log('no')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := NewWorkspaceContext()
	setWorkspaceContextRoot(t, ctx, root)

	coverage := NewCoverageServiceWithWorkspaceContext(ctx)
	if _, err := coverage.RunPackageCoverage(outside); err == nil {
		t.Fatal("coverage accepted a directory outside the workspace")
	}

	toolchain := NewToolchainServiceWithWorkspaceContext(ctx)
	if _, err := toolchain.RunToolchainCommand("go-build", outsideFile); err == nil {
		t.Fatal("toolchain accepted a file outside the workspace")
	}

	eslint := NewEslintServiceWithWorkspaceContext(ctx)
	if _, err := eslint.LintFile(outsideFile, "", ""); err == nil {
		t.Fatal("eslint accepted a file outside the workspace")
	}

	debug := NewDebugServiceWithWorkspaceContext(ctx)
	debug.approveProjectExecutable = func(string, string) bool { return true }
	if _, err := debug.LaunchWithConfig(DebugLaunchConfig{Kind: "node", Program: outsideFile}); err == nil {
		t.Fatal("debug accepted a program outside the workspace")
	}

	terminal := NewTerminalServiceWithWorkspaceContext(ctx)
	if err := terminal.StartSession("escape", outside, ""); err == nil {
		t.Fatal("terminal accepted a working directory outside the workspace")
	}

	agent := NewAgentServiceWithWorkspaceContext(ctx)
	t.Cleanup(func() { _ = agent.Close() })
	agent.approveCommand = func(string, string, RiskLevel) bool { return true }
	if _, err := agent.requestCommandApprovalLegacy("go version", outside); err == nil {
		t.Fatal("agent accepted a working directory outside the workspace")
	}
}

func TestG07CommandEntrypointsRejectWorkspaceGenerationChangeBeforeStart(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	program := filepath.Join(rootA, "main.js")
	if err := os.WriteFile(program, []byte("console.log('test')\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootA, "package.json"), []byte(`{"name":"g07"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("coverage", func(t *testing.T) {
		ctx := NewWorkspaceContext()
		setWorkspaceContextRoot(t, ctx, rootA)
		svc := NewCoverageServiceWithWorkspaceContext(ctx)
		svc.lookPath = func(string) (string, error) { return "npm", nil }
		runnerCalled := false
		svc.runVitest = func(context.Context, CoverageCommand) ([]byte, error) {
			runnerCalled = true
			return nil, nil
		}
		svc.beforeWorkspaceCommandStart = func() { setWorkspaceContextRoot(t, ctx, rootB) }
		_, err := svc.RunVitestCoverage(context.Background(), rootA, 5)
		assertNotAllowed(t, err)
		if runnerCalled {
			t.Fatal("coverage runner started after workspace generation changed")
		}
	})

	t.Run("toolchain", func(t *testing.T) {
		ctx := NewWorkspaceContext()
		setWorkspaceContextRoot(t, ctx, rootA)
		svc := NewToolchainServiceWithWorkspaceContext(ctx)
		svc.beforeWorkspaceCommandStart = func() { setWorkspaceContextRoot(t, ctx, rootB) }
		_, err := svc.RunToolchainCommand("go-build", "")
		assertNotAllowed(t, err)
	})

	t.Run("eslint", func(t *testing.T) {
		ctx := NewWorkspaceContext()
		setWorkspaceContextRoot(t, ctx, rootA)
		binDir := filepath.Join(rootA, "node_modules", ".bin")
		if err := os.MkdirAll(binDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(binDir, "eslint"), []byte("#!/bin/sh\nprintf '[]'\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		svc := NewEslintServiceWithWorkspaceContext(ctx)
		svc.beforeWorkspaceCommandStart = func() { setWorkspaceContextRoot(t, ctx, rootB) }
		_, err := svc.LintFile(program, "", "")
		assertNotAllowed(t, err)
	})

	t.Run("debug", func(t *testing.T) {
		ctx := NewWorkspaceContext()
		setWorkspaceContextRoot(t, ctx, rootA)
		svc := NewDebugServiceWithWorkspaceContext(ctx)
		svc.approveProjectExecutable = func(string, string) bool { return true }
		svc.beforeWorkspaceCommandStart = func() { setWorkspaceContextRoot(t, ctx, rootB) }
		_, err := svc.LaunchWithConfig(DebugLaunchConfig{Kind: "node", Program: program})
		assertNotAllowed(t, err)
	})

	t.Run("terminal", func(t *testing.T) {
		ctx := NewWorkspaceContext()
		setWorkspaceContextRoot(t, ctx, rootA)
		svc := NewTerminalServiceWithWorkspaceContext(ctx)
		svc.beforeWorkspaceCommandStart = func() { setWorkspaceContextRoot(t, ctx, rootB) }
		err := svc.StartSession("generation", rootA, "")
		assertNotAllowed(t, err)
	})

	t.Run("agent", func(t *testing.T) {
		ctx := NewWorkspaceContext()
		setWorkspaceContextRoot(t, ctx, rootA)
		svc := NewAgentServiceWithWorkspaceContext(ctx)
		t.Cleanup(func() { _ = svc.Close() })
		svc.approveCommand = func(string, string, RiskLevel) bool { return true }
		token, err := svc.requestCommandApprovalLegacy("go version", rootA)
		if err != nil {
			t.Fatal(err)
		}
		var once sync.Once
		svc.beforeWorkspaceCommandStart = func() {
			once.Do(func() { setWorkspaceContextRoot(t, ctx, rootB) })
		}
		_, err = svc.executeApprovedCommandLegacy("go version", rootA, token)
		assertNotAllowed(t, err)
	})
}

func TestG07DebugProjectExecutableRequiresBackendConfirmation(t *testing.T) {
	root := t.TempDir()
	program := filepath.Join(root, "project-script.js")
	if err := os.WriteFile(program, []byte("console.log('blocked')\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx := NewWorkspaceContext()
	setWorkspaceContextRoot(t, ctx, root)
	svc := NewDebugServiceWithWorkspaceContext(ctx)
	requested := ""
	svc.approveProjectExecutable = func(kind, path string) bool {
		requested = kind + ":" + path
		return false
	}

	_, err := svc.LaunchWithConfig(DebugLaunchConfig{Kind: "node", Program: program})
	assertNotAllowed(t, err)
	if !strings.Contains(requested, program) {
		t.Fatalf("backend confirmation did not receive the exact program path: %q", requested)
	}
}

func TestG07DebugValidatesEveryWorkspacePathAndExplicitExecutable(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	ctx := NewWorkspaceContext()
	setWorkspaceContextRoot(t, ctx, root)
	svc := NewDebugServiceWithWorkspaceContext(ctx)
	svc.approveProjectExecutable = func(string, string) bool { return false }

	for name, cfg := range map[string]DebugLaunchConfig{
		"working directory": {Kind: "package", Dir: outside},
		"web root":          {Kind: "browser", WebRoot: outside},
		"path mapping": {
			Kind: "browser", WebRoot: root,
			PathMappings: map[string]string{"/src": outside},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := svc.LaunchWithConfig(cfg); err == nil || !strings.Contains(err.Error(), "outside") {
				t.Fatalf("expected workspace escape rejection, got %v", err)
			}
		})
	}

	executable := filepath.Join(outside, "project-browser")
	if err := os.WriteFile(executable, []byte("not executed\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	requested := ""
	svc.approveProjectExecutable = func(kind, path string) bool {
		requested = kind + ":" + path
		return false
	}
	_, err := svc.LaunchWithConfig(DebugLaunchConfig{
		Kind: "browser", Request: "launch", WebRoot: root, ExecutablePath: executable,
	})
	assertNotAllowed(t, err)
	if !strings.Contains(requested, "executable:") || !strings.Contains(requested, executable) {
		t.Fatalf("explicit executable was not bound to backend confirmation: %q", requested)
	}
}

func TestG07WorkspaceLeaseResolvesRelativePathsAndExpires(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	child := filepath.Join(rootA, "src")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx := NewWorkspaceContext()
	setWorkspaceContextRoot(t, ctx, rootA)
	lease, err := acquireWorkspaceLease(ctx, "ignored", 99)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := lease.resolve(filepath.Join("src"))
	if err != nil || resolved != child {
		t.Fatalf("relative path did not resolve from lease root: path=%q err=%v", resolved, err)
	}
	if err := lease.validateCurrent(); err != nil {
		t.Fatalf("current lease rejected: %v", err)
	}
	setWorkspaceContextRoot(t, ctx, rootB)
	assertNotAllowed(t, lease.validateCurrent())
}

func TestG07ToolchainTargetsAreBoundToSharedWorkspace(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	ctx := NewWorkspaceContext()
	setWorkspaceContextRoot(t, ctx, rootA)
	svc := NewToolchainServiceWithWorkspaceContext(ctx)
	host := GoTarget{GOOS: "linux", GOARCH: "amd64"}
	wanted := GoTarget{GOOS: "windows", GOARCH: "amd64"}
	svc.goTargetProvider = func() (GoTarget, []GoTarget, error) {
		return host, []GoTarget{host, wanted}, nil
	}

	state, err := svc.SetGoTarget(wanted.GOOS, wanted.GOARCH)
	if err != nil || state.Current != wanted || !state.Overridden {
		t.Fatalf("set target failed: state=%+v err=%v", state, err)
	}
	state, err = svc.GetGoTarget()
	if err != nil || state.Current != wanted {
		t.Fatalf("get target failed: state=%+v err=%v", state, err)
	}
	setWorkspaceContextRoot(t, ctx, rootB)
	state, err = svc.GetGoTarget()
	if err != nil || state.Current != host || state.Overridden {
		t.Fatalf("target leaked across workspaces: state=%+v err=%v", state, err)
	}
	state, err = svc.ResetGoTarget()
	if err != nil || state.Current != host || state.Overridden {
		t.Fatalf("reset target failed: state=%+v err=%v", state, err)
	}
}

func TestG07AgentApprovalRejectsWorkspaceChangeDuringNativePrompt(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	ctx := NewWorkspaceContext()
	setWorkspaceContextRoot(t, ctx, rootA)
	svc := NewAgentServiceWithWorkspaceContext(ctx)
	t.Cleanup(func() { _ = svc.Close() })
	svc.approveCommand = func(string, string, RiskLevel) bool {
		setWorkspaceContextRoot(t, ctx, rootB)
		return true
	}
	_, err := svc.requestCommandApprovalLegacy("go version", rootA)
	assertNotAllowed(t, err)
}
