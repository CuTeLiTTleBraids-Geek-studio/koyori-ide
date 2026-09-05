package services

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
)

type headlessCLIEnvelope struct {
	Success bool `json:"success"`
	Catalog *struct {
		Revision uint64 `json:"revision"`
		Tools    []struct {
			ID       string `json:"id"`
			Source   string `json:"source"`
			Risk     string `json:"risk"`
			Approval string `json:"approval"`
		} `json:"tools"`
	} `json:"catalog,omitempty"`
	Read *struct {
		Bytes  int    `json:"bytes"`
		SHA256 string `json:"sha256"`
	} `json:"read,omitempty"`
	ExternalReceipts []struct {
		Handle    string    `json:"handle"`
		Status    string    `json:"status"`
		StartedAt time.Time `json:"startedAt"`
		UpdatedAt time.Time `json:"updatedAt"`
	} `json:"externalReceipts,omitempty"`
	ExternalReceipt *struct {
		Handle      string    `json:"handle"`
		Status      string    `json:"status"`
		Disposition string    `json:"disposition"`
		CompletedAt time.Time `json:"completedAt"`
	} `json:"externalReceipt,omitempty"`
	Code string `json:"code,omitempty"`
}

func TestHeadlessAgentCLIProcess(t *testing.T) {
	repoRoot := headlessRepoRoot(t)
	workspace := t.TempDir()
	state := headlessPrivateStateDir(t)
	fixtureName := "fixture.txt"
	fixtureContent := "child-process-headless-secret-marker"
	fixturePath := filepath.Join(workspace, fixtureName)
	if err := os.WriteFile(fixturePath, []byte(fixtureContent), 0o600); err != nil {
		t.Fatal(err)
	}

	binDir := t.TempDir()
	binName := "agentcli"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	binPath := filepath.Join(binDir, binName)
	buildArgs := []string{"build", "-buildvcs=false", "-o", binPath}
	if headlessCLIRaceBuildSupported() {
		buildArgs = append(buildArgs, "-race")
	}
	buildArgs = append(buildArgs, "./internal/agentcli")
	build := exec.Command("go", buildArgs...)
	build.Dir = repoRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build agentcli: %v\n%s", err, output)
	}

	// Hold the production state lock in this process while the real CLI child
	// attempts to acquire the same directory. The child must fail closed before
	// it can create a session or append a usage receipt.
	holder, err := NewHeadlessAgentHost(workspace, state)
	if err != nil {
		t.Fatalf("hold headless state lock: %v", err)
	}
	stdout, stderr, err := runHeadlessCLIProcess(binPath, workspace, state, "catalog")
	if err == nil {
		_ = holder.Close()
		t.Fatalf("concurrent child unexpectedly acquired state lock: stdout=%s", stdout)
	}
	if !strings.Contains(stdout, "operation-rejected") {
		_ = holder.Close()
		t.Fatalf("concurrent child output = %q, want operation-rejected", stdout)
	}
	assertHeadlessOutputRedacted(t, stdout, stderr, fixtureContent, workspace)
	if err := holder.Close(); err != nil {
		t.Fatalf("release held headless state lock: %v", err)
	}

	stdout, stderr, err = runHeadlessCLIProcess(binPath, workspace, state, "catalog")
	if err != nil {
		t.Fatalf("catalog: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var catalog headlessCLIEnvelope
	if err := json.Unmarshal([]byte(stdout), &catalog); err != nil {
		t.Fatalf("decode catalog output %q: %v", stdout, err)
	}
	if !catalog.Success || catalog.Catalog == nil || len(catalog.Catalog.Tools) != 1 || catalog.Catalog.Tools[0].ID != "read" {
		t.Fatalf("catalog output = %+v, want one read tool", catalog)
	}

	stdout, stderr, err = runHeadlessCLIProcess(binPath, workspace, state, "read", "--path", fixtureName)
	if err != nil {
		t.Fatalf("read: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var read headlessCLIEnvelope
	if err := json.Unmarshal([]byte(stdout), &read); err != nil {
		t.Fatalf("decode read output %q: %v", stdout, err)
	}
	if !read.Success || read.Read == nil || read.Read.Bytes != len(fixtureContent) || read.Read.SHA256 == "" {
		t.Fatalf("read output = %+v, want metadata-only success", read)
	}
	assertHeadlessOutputRedacted(t, stdout, stderr, fixtureContent, workspace)
	assertHeadlessStateRedacted(t, state, fixtureContent, workspace, state)

	// A second process with the same state directory proves that the durable
	// usage ledger is reloadable, while the instance lock still serializes the
	// two short-lived consumers.
	stdout, stderr, err = runHeadlessCLIProcess(binPath, workspace, state, "read", "--path", fixtureName)
	if err != nil {
		t.Fatalf("second read: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	assertHeadlessOutputRedacted(t, stdout, stderr, fixtureContent, workspace)
	terminalUnits := headlessTerminalUsageUnits(t, filepath.Join(state, "usage_log.jsonl"))
	if len(terminalUnits) != 2 {
		t.Fatalf("terminal usage units = %d (%v), want two durable reads", len(terminalUnits), terminalUnits)
	}

	absolutePath := filepath.Join(workspace, fixtureName)
	stdout, stderr, err = runHeadlessCLIProcess(binPath, workspace, state, "read", "--path", absolutePath)
	if err == nil {
		t.Fatalf("absolute path unexpectedly succeeded: stdout=%s", stdout)
	}
	assertHeadlessOutputRedacted(t, stdout, stderr, fixtureContent, workspace)

	stdout, stderr, err = runHeadlessCLIProcess(binPath, workspace, state, "read", "--path", "../"+fixtureName)
	if err == nil {
		t.Fatalf("traversal path unexpectedly succeeded: stdout=%s", stdout)
	}
	assertHeadlessOutputRedacted(t, stdout, stderr, fixtureContent, workspace)

	stdout, stderr, err = runHeadlessCLIProcess(binPath, workspace, state, "run")
	if err == nil {
		t.Fatalf("unsupported run command unexpectedly succeeded: stdout=%s", stdout)
	}
	assertHeadlessOutputRedacted(t, stdout, stderr, fixtureContent, workspace)

	// A directory at the append-only usage path poisons the permission service.
	// The child must reject before the production read handler can return any
	// observation, and the diagnostic must remain category-only.
	poisonedState := headlessPrivateStateDir(t)
	if err := os.Mkdir(filepath.Join(poisonedState, "usage_log.jsonl"), 0o700); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err = runHeadlessCLIProcess(binPath, workspace, poisonedState, "read", "--path", fixtureName)
	if err == nil {
		t.Fatalf("poisoned ledger unexpectedly succeeded: stdout=%s", stdout)
	}
	assertHeadlessOutputRedacted(t, stdout, stderr, fixtureContent, workspace)
	if !strings.Contains(stdout, "usage-unavailable") {
		t.Fatalf("poisoned ledger output = %q, want usage-unavailable category", stdout)
	}
	assertHeadlessStateRedacted(t, poisonedState, fixtureContent, workspace, poisonedState)

	// A malformed identity key used to enter the generic AES recovery path,
	// which logged the absolute state/backup paths to stderr and rewrote the
	// caller's key. Headless construction must reject it without either effect.
	corruptState := headlessPrivateStateDir(t)
	corruptKey := filepath.Join(corruptState, "agent_lifecycle_identity.key")
	corruptBytes := []byte("not-a-valid-headless-identity-key")
	if err := os.WriteFile(corruptKey, corruptBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err = runHeadlessCLIProcess(binPath, workspace, corruptState, "catalog")
	if err == nil {
		t.Fatalf("corrupt identity key unexpectedly succeeded: stdout=%s", stdout)
	}
	if !strings.Contains(stdout, "usage-unavailable") {
		t.Fatalf("corrupt identity output = %q, want usage-unavailable category; stderr=%q", stdout, stderr)
	}
	assertHeadlessOutputRedacted(t, stdout, stderr, fixtureContent, workspace, corruptState)
	afterKey, err := os.ReadFile(corruptKey)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterKey, corruptBytes) {
		t.Fatalf("corrupt identity key was rewritten: got %q want %q", afterKey, corruptBytes)
	}
	backups, err := filepath.Glob(corruptKey + ".corrupt-*")
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 0 {
		t.Fatalf("corrupt identity key created backups: %v", backups)
	}
}

func TestHeadlessAgentExternalReceiptRecoveryCLIProcess(t *testing.T) {
	repoRoot := headlessRepoRoot(t)
	workspace := t.TempDir()
	state := headlessPrivateStateDir(t)
	binDir := t.TempDir()
	binName := "agentcli-recovery"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	binPath := filepath.Join(binDir, binName)
	build := exec.Command("go", "build", "-buildvcs=false", "-o", binPath, "./internal/agentcli")
	build.Dir = repoRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build agentcli recovery consumer: %v\n%s", err, output)
	}

	// Seed a pending irreversible receipt through the production lifecycle, then
	// close that incarnation. The child process must prove the old owner is
	// trusted after reload before it can expose or dispose the row.
	host, err := NewHeadlessAgentHost(workspace, state)
	if err != nil {
		t.Fatalf("seed NewHeadlessAgentHost: %v", err)
	}
	seedSession, err := host.lifecycle.Begin(agentcore.SessionChat, "cli-receipt-recovery")
	if err != nil {
		_ = host.Close()
		t.Fatalf("seed lifecycle session: %v", err)
	}
	started := time.Date(2026, time.August, 18, 2, 0, 0, 0, time.UTC)
	if _, err := host.lifecycle.BeginUsage(agentcore.UsageRecord{
		UnitID: "unit-cli-receipt-recovery", SessionID: seedSession.ID,
		UnitKind: agentcore.UsageUnitTool, Operation: "mcp.call",
		CostBasis: agentcore.CostNotApplicable, StartedAt: started,
		CompletedAt: started, Pending: true, Success: false,
		ExternalReceiptID: "mcp:cli-receipt-recovery", ExternalReceiptReversible: true,
		ExternalCompensation: agentcore.ExternalCompensationPending,
	}); err != nil {
		_ = host.Close()
		t.Fatalf("seed pending external receipt: %v", err)
	}
	if err := host.Close(); err != nil {
		t.Fatalf("close seed host: %v", err)
	}

	stdout, stderr, err := runHeadlessCLIProcess(binPath, workspace, state, "external-receipts")
	if err != nil {
		t.Fatalf("external-receipts: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var inventory headlessCLIEnvelope
	if err := json.Unmarshal([]byte(stdout), &inventory); err != nil {
		t.Fatalf("decode external receipt inventory %q: %v", stdout, err)
	}
	if !inventory.Success || len(inventory.ExternalReceipts) != 1 || inventory.ExternalReceipts[0].Status != "pending" {
		t.Fatalf("external receipt inventory = %+v, want one pending opaque row", inventory)
	}
	handle := inventory.ExternalReceipts[0].Handle
	if !strings.HasPrefix(handle, "receipt-recovery-v1:") {
		t.Fatalf("external receipt handle = %q, want opaque recovery prefix", handle)
	}
	assertHeadlessOutputRedacted(t, stdout, stderr, "unit-cli-receipt-recovery", "mcp:cli-receipt-recovery", state, workspace)

	stdout, stderr, err = runHeadlessCLIProcess(
		binPath, workspace, state, "external-receipt-dispose", "--handle", handle, "--disposition", "resume",
	)
	if err == nil {
		t.Fatalf("unsupported external receipt disposition unexpectedly succeeded: stdout=%s", stdout)
	}
	if !strings.Contains(stdout, `"code":"invalid-input"`) {
		t.Fatalf("unsupported disposition output = %q, want invalid-input", stdout)
	}
	assertHeadlessOutputRedacted(t, stdout, stderr, "unit-cli-receipt-recovery", "mcp:cli-receipt-recovery", state, workspace)

	stdout, stderr, err = runHeadlessCLIProcess(
		binPath, workspace, state, "external-receipt-dispose", "--handle", handle, "--disposition", "manual-unknown",
	)
	if err != nil {
		t.Fatalf("manual-unknown external receipt disposition: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var disposition headlessCLIEnvelope
	if err := json.Unmarshal([]byte(stdout), &disposition); err != nil {
		t.Fatalf("decode external receipt disposition %q: %v", stdout, err)
	}
	if !disposition.Success || disposition.ExternalReceipt == nil ||
		disposition.ExternalReceipt.Handle != handle || disposition.ExternalReceipt.Status != "completed" ||
		disposition.ExternalReceipt.Disposition != "manual-unknown" || disposition.ExternalReceipt.CompletedAt.IsZero() {
		t.Fatalf("external receipt disposition = %+v, want completed manual-unknown", disposition)
	}
	assertHeadlessOutputRedacted(t, stdout, stderr, "unit-cli-receipt-recovery", "mcp:cli-receipt-recovery", state, workspace)

	stdout, stderr, err = runHeadlessCLIProcess(binPath, workspace, state, "external-receipts")
	if err != nil {
		t.Fatalf("external-receipts after disposition: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var emptyInventory headlessCLIEnvelope
	if err := json.Unmarshal([]byte(stdout), &emptyInventory); err != nil {
		t.Fatalf("decode empty external receipt inventory %q: %v", stdout, err)
	}
	if !emptyInventory.Success || len(emptyInventory.ExternalReceipts) != 0 {
		t.Fatalf("external receipt inventory after disposition = %+v, want empty", emptyInventory)
	}

	// A fresh process must be able to replay the same terminal result from the
	// durable ledger, while still returning only the opaque handle and status.
	stdout, stderr, err = runHeadlessCLIProcess(
		binPath, workspace, state, "external-receipt-dispose", "--handle", handle, "--disposition", "manual-unknown",
	)
	if err != nil {
		t.Fatalf("external receipt disposition replay: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	var replay headlessCLIEnvelope
	if err := json.Unmarshal([]byte(stdout), &replay); err != nil {
		t.Fatalf("decode external receipt replay %q: %v", stdout, err)
	}
	if !replay.Success || replay.ExternalReceipt == nil || replay.ExternalReceipt.Handle != handle ||
		replay.ExternalReceipt.Status != disposition.ExternalReceipt.Status ||
		replay.ExternalReceipt.Disposition != disposition.ExternalReceipt.Disposition ||
		!replay.ExternalReceipt.CompletedAt.Equal(disposition.ExternalReceipt.CompletedAt) {
		t.Fatalf("external receipt replay = %+v, want stable terminal result %+v", replay, disposition)
	}
	assertHeadlessOutputRedacted(t, stdout, stderr, "unit-cli-receipt-recovery", "mcp:cli-receipt-recovery", state, workspace)

	terminalUnits := headlessTerminalUsageUnits(t, filepath.Join(state, "usage_log.jsonl"))
	if len(terminalUnits) != 1 {
		t.Fatalf("terminal usage units after CLI recovery = %d (%v), want one durable unit", len(terminalUnits), terminalUnits)
	}
}

func runHeadlessCLIProcess(binary, workspace, state, command string, commandArgs ...string) (string, string, error) {
	args := []string{"--workspace", workspace, "--state-dir", state, command}
	args = append(args, commandArgs...)
	process := exec.Command(binary, args...)
	var stdout, stderr bytes.Buffer
	process.Stdout = &stdout
	process.Stderr = &stderr
	err := process.Run()
	return stdout.String(), stderr.String(), err
}

func assertHeadlessOutputRedacted(t *testing.T, stdout, stderr string, forbidden ...string) {
	t.Helper()
	combined := stdout + "\n" + stderr
	for _, value := range forbidden {
		if value != "" && (strings.Contains(combined, value) || strings.Contains(combined, filepath.ToSlash(value))) {
			t.Fatalf("headless output leaked a forbidden value: %q", combined)
		}
	}
}

func assertHeadlessStateRedacted(t *testing.T, state string, forbidden ...string) {
	t.Helper()
	entries, err := os.ReadDir(state)
	if err != nil {
		t.Fatalf("read headless state directory: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(state, name)
		info, statErr := os.Lstat(path)
		if statErr != nil {
			t.Fatalf("lstat headless state file %q: %v", name, statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("headless state file %q is a symlink", name)
		}
		if info.IsDir() {
			continue
		}
		if !info.Mode().IsRegular() {
			t.Fatalf("headless state file %q is not regular", name)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read headless state file %q: %v", name, err)
		}
		text := string(data)
		for _, value := range forbidden {
			if value == "" {
				continue
			}
			if strings.Contains(text, value) || strings.Contains(text, filepath.ToSlash(value)) {
				t.Fatalf("headless state file %q leaked forbidden value", name)
			}
		}
	}
}

func headlessTerminalUsageUnits(t *testing.T, path string) map[string]struct{} {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read usage ledger: %v", err)
	}
	units := make(map[string]struct{})
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var record struct {
			UnitID  string `json:"unitId"`
			Pending bool   `json:"pending"`
		}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode usage ledger line %q: %v", line, err)
		}
		if record.UnitID != "" && !record.Pending {
			units[record.UnitID] = struct{}{}
		}
	}
	return units
}

func headlessRepoRoot(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Dir(filepath.Dir(source))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repo root %q has no go.mod: %v", root, err)
	}
	return root
}

func headlessCLIRaceBuildSupported() bool {
	switch runtime.GOOS {
	case "windows":
		return runtime.GOARCH == "amd64"
	case "linux":
		return runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64" || runtime.GOARCH == "ppc64le" || runtime.GOARCH == "s390x"
	case "darwin", "freebsd":
		return runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64"
	default:
		return false
	}
}
