package services

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseGoDistList(t *testing.T) {
	targets := parseGoDistList("linux/amd64\nwindows/amd64\nlinux/arm64\ninvalid\nlinux/amd64\n")
	if len(targets) != 3 {
		t.Fatalf("expected 3 unique targets, got %d: %+v", len(targets), targets)
	}
	if targets[2] != (GoTarget{GOOS: "linux", GOARCH: "arm64"}) {
		t.Fatalf("unexpected final target: %+v", targets[2])
	}
}

func TestToolchainServiceGoTargetWorkspaceIsolationAndReset(t *testing.T) {
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	svc := NewToolchainService()
	svc.goTargetProvider = func() (GoTarget, []GoTarget, error) {
		return GoTarget{GOOS: "windows", GOARCH: "amd64"}, []GoTarget{
			{GOOS: "windows", GOARCH: "amd64"},
			{GOOS: "linux", GOARCH: "arm64"},
		}, nil
	}

	if err := svc.setWorkspaceRoot(workspaceA); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetGoTarget("linux", "arm64"); err != nil {
		t.Fatalf("set workspace A target: %v", err)
	}
	state, err := svc.GetGoTarget()
	if err != nil {
		t.Fatal(err)
	}
	if !state.Overridden || state.Current != (GoTarget{GOOS: "linux", GOARCH: "arm64"}) {
		t.Fatalf("workspace A target = %+v", state)
	}

	if err := svc.setWorkspaceRoot(workspaceB); err != nil {
		t.Fatal(err)
	}
	state, err = svc.GetGoTarget()
	if err != nil {
		t.Fatal(err)
	}
	if state.Overridden || state.Current != state.Host {
		t.Fatalf("workspace B should start on host target: %+v", state)
	}

	if err := svc.setWorkspaceRoot(workspaceA); err != nil {
		t.Fatal(err)
	}
	state, err = svc.ResetGoTarget()
	if err != nil {
		t.Fatal(err)
	}
	if state.Overridden || state.Current != state.Host {
		t.Fatalf("reset should restore host target: %+v", state)
	}

	if err := svc.setWorkspaceRoot(workspaceB); err != nil {
		t.Fatal(err)
	}
	state, err = svc.GetGoTarget()
	if err != nil {
		t.Fatal(err)
	}
	if state.Overridden {
		t.Fatalf("reset in workspace A must not affect workspace B: %+v", state)
	}
}

func TestToolchainServiceRejectsUnsupportedGoTarget(t *testing.T) {
	svc := NewToolchainService()
	svc.goTargetProvider = func() (GoTarget, []GoTarget, error) {
		return GoTarget{GOOS: "windows", GOARCH: "amd64"}, []GoTarget{
			{GOOS: "windows", GOARCH: "amd64"},
			{GOOS: "linux", GOARCH: "arm64"},
		}, nil
	}
	if err := svc.setWorkspaceRoot(t.TempDir()); err != nil {
		t.Fatal(err)
	}

	_, err := svc.SetGoTarget("linux", "not-real")
	if err == nil || !strings.Contains(err.Error(), `unsupported Go target "linux/not-real"`) {
		t.Fatalf("expected explicit unsupported-target error, got %v", err)
	}
	state, stateErr := svc.GetGoTarget()
	if stateErr != nil {
		t.Fatal(stateErr)
	}
	if state.Overridden {
		t.Fatalf("invalid target must not mutate current target: %+v", state)
	}
}

func TestToolchainServiceGoCommandCarriesTargetEnvAndArgv(t *testing.T) {
	workspace := t.TempDir()
	svc := NewToolchainService()
	svc.goTargetProvider = func() (GoTarget, []GoTarget, error) {
		return GoTarget{GOOS: "windows", GOARCH: "amd64"}, []GoTarget{
			{GOOS: "windows", GOARCH: "amd64"},
			{GOOS: "linux", GOARCH: "arm64"},
		}, nil
	}
	if err := svc.setWorkspaceRoot(workspace); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetGoTarget("linux", "arm64"); err != nil {
		t.Fatal(err)
	}

	cmd, err := svc.newToolchainCommand(context.Background(), "go", []string{"build", "./..."}, filepath.Clean(workspace))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cmd.Args[1:], " "); got != "build ./..." {
		t.Fatalf("argv = %q, want %q", got, "build ./...")
	}
	if got := envValue(cmd.Env, "GOOS"); got != "linux" {
		t.Fatalf("GOOS = %q, want linux", got)
	}
	if got := envValue(cmd.Env, "GOARCH"); got != "arm64" {
		t.Fatalf("GOARCH = %q, want arm64", got)
	}
}

func TestToolchainServiceHostTargetIgnoresProcessCrossCompileEnv(t *testing.T) {
	if runtime.GOOS == "linux" && runtime.GOARCH == "arm64" {
		t.Setenv("GOOS", "windows")
		t.Setenv("GOARCH", "amd64")
	} else {
		t.Setenv("GOOS", "linux")
		t.Setenv("GOARCH", "arm64")
	}
	svc := NewToolchainService()
	state, err := svc.GetGoTarget()
	if err != nil {
		t.Skipf("Go toolchain unavailable: %v", err)
	}
	if state.Host != (GoTarget{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}) {
		t.Fatalf("host target = %+v, want runtime host %s/%s", state.Host, runtime.GOOS, runtime.GOARCH)
	}
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for i := len(env) - 1; i >= 0; i-- {
		if strings.HasPrefix(env[i], prefix) {
			return strings.TrimPrefix(env[i], prefix)
		}
	}
	return ""
}
