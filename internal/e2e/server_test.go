//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services"
)

// newTestServiceSet builds the minimal real service graph the endpoint needs:
// the shared WorkspaceContext links Project/File/Terminal so AddProject's
// two-phase root switch works exactly as it does in the packaged app (the
// main package wires the same five services into e2e.ServiceSet).
func newTestServiceSet(t *testing.T) ServiceSet {
	t.Helper()
	configDir := t.TempDir()
	ctx := services.NewWorkspaceContext()
	file := &services.FileService{}
	terminal := services.NewTerminalServiceWithWorkspaceContext(ctx)
	agent := services.NewAgentServiceWithWorkspaceContext(ctx)
	ai := services.NewAIService()
	project := services.NewProjectService(file, terminal, agent, ai)
	services.SetProjectWorkspaceContext(project, ctx)
	lsp := services.NewLSPService("")
	services.SetLSPFileService(lsp, file)
	recovery := services.NewRecoveryService(configDir)
	services.SetRecoveryWorkspaceContext(recovery, ctx)
	window := services.NewWindowServiceWithWorkspaceContext(ctx)
	services.WireRecoveryGuards(project, window, recovery)
	t.Cleanup(func() {
		lsp.StopAll()
		terminal.Shutdown()
	})
	return ServiceSet{
		Project:  project,
		File:     file,
		Terminal: terminal,
		LSP:      lsp,
		Recovery: recovery,
		Window:   window,
	}
}

func TestStartRequiresBuildTagAndExplicitEnvironmentOptIn(t *testing.T) {
	set := newTestServiceSet(t)
	t.Setenv(envOptIn, "")
	t.Setenv(envToken, strings.Repeat("ab", 32))
	handshakePath := filepath.Join(t.TempDir(), "handshake.json")
	t.Setenv(envHandshake, handshakePath)

	cleanup, err := Start(set)
	if err != nil {
		t.Fatalf("disabled automation returned an error: %v", err)
	}
	if cleanup != nil {
		t.Fatal("automation returned cleanup without KOYORI_IDE_E2E=1")
	}
	if _, err := os.Stat(handshakePath); !os.IsNotExist(err) {
		t.Fatalf("disabled automation wrote a handshake: %v", err)
	}
}

func TestStartRejectsMissingMalformedOrZeroRunID(t *testing.T) {
	for _, runID := range []string{"", strings.Repeat("A", 64), strings.Repeat("0", 64)} {
		t.Run(runID, func(t *testing.T) {
			set := newTestServiceSet(t)
			t.Setenv(envOptIn, "1")
			t.Setenv(envToken, strings.Repeat("ab", 32))
			t.Setenv(envRunID, runID)
			t.Setenv(envHandshake, filepath.Join(t.TempDir(), "handshake.json"))

			cleanup, err := Start(set)
			if err == nil || !strings.Contains(err.Error(), "KOYORI_IDE_E2E_RUN_ID") {
				t.Fatalf("Start runID=%q error = %v, want run ID rejection", runID, err)
			}
			if cleanup != nil {
				t.Fatal("rejected run ID returned cleanup")
			}
		})
	}
}

func TestStartListensOnLoopbackAndRejectsTokenReplay(t *testing.T) {
	set := newTestServiceSet(t)
	initialToken := strings.Repeat("cd", 32)
	handshakePath := filepath.Join(t.TempDir(), "handshake.json")
	t.Setenv(envOptIn, "1")
	t.Setenv(envToken, initialToken)
	runID := strings.Repeat("ef", 32)
	t.Setenv(envRunID, runID)
	t.Setenv(envHandshake, handshakePath)

	cleanup, err := Start(set)
	if err != nil {
		t.Fatalf("start automation: %v", err)
	}
	if cleanup == nil {
		t.Fatal("enabled automation did not return cleanup")
	}
	t.Cleanup(cleanup)

	data, err := os.ReadFile(handshakePath)
	if err != nil {
		t.Fatalf("read handshake: %v", err)
	}
	var hs handshake
	if err := json.Unmarshal(data, &hs); err != nil {
		t.Fatalf("decode handshake: %v", err)
	}
	if !strings.HasPrefix(hs.URL, "http://127.0.0.1:") {
		t.Fatalf("automation URL %q is not loopback-only", hs.URL)
	}
	if hs.RunID != runID {
		t.Fatalf("automation run ID = %q, want %q", hs.RunID, runID)
	}

	workspace := t.TempDir()
	filePath := filepath.Join(workspace, "main.go")
	if err := os.WriteFile(filePath, []byte("package fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	next, status := sendCommand(t, hs.URL, initialToken, command{
		Action:    "open-workspace",
		Workspace: workspace,
	})
	if status != http.StatusOK || next == "" || next == initialToken {
		t.Fatalf("first command status=%d nextTokenChanged=%v", status, next != "" && next != initialToken)
	}

	_, replayStatus := sendCommand(t, hs.URL, initialToken, command{
		Action: "open-file",
		Path:   filePath,
	})
	if replayStatus != http.StatusUnauthorized {
		t.Fatalf("replayed token status=%d, want %d", replayStatus, http.StatusUnauthorized)
	}
	_, currentStatus := sendCommand(t, hs.URL, next, command{
		Action: "open-file",
		Path:   filePath,
	})
	if currentStatus != http.StatusOK {
		t.Fatalf("rotated token status=%d, want %d", currentStatus, http.StatusOK)
	}
}

func sendCommand(
	t *testing.T,
	url, token string,
	cmd command,
) (string, int) {
	t.Helper()
	body, err := json.Marshal(cmd)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, url+"/v1/command", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("send command: %v", err)
	}
	defer response.Body.Close()
	return response.Header.Get("X-Koyori-IDE-E2E-Token"), response.StatusCode
}

func TestRendererProbeExecutorFailureIsImmediate(t *testing.T) {
	automation := &server{probeResults: make(map[string]chan map[string]interface{})}
	configuration, err := json.Marshal(map[string]string{"runId": "run_executor_failure"})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, err = automation.runRendererProbeWithExecutor(
		func(string) bool { return false },
		"__missingProbe",
		"e2e:missing-result",
		"unavailable AI window",
		configuration,
	)
	if err == nil || !strings.Contains(err.Error(), "executor rejected") {
		t.Fatalf("executor failure = %v, want immediate rejection", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("executor failure waited for renderer timeout: %s", elapsed)
	}
}

func TestEditSaveAndRecoveryUseRealServices(t *testing.T) {
	set := newTestServiceSet(t)
	workspace := t.TempDir()
	filePath := filepath.Join(workspace, "main.go")
	initial := "package fixture\n"
	saved := "package fixture\n\nfunc Saved() {}\n"
	dirty := saved + "\n// unsaved\n"
	if err := os.WriteFile(filePath, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := set.Project.AddProject(workspace); err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	if _, err := set.Recovery.ScanRecoverable(); err != nil {
		t.Fatalf("initial recovery scan: %v", err)
	}
	automation := &server{services: set}

	editResult, err := automation.execute(command{
		Action:  "edit",
		Path:    filePath,
		Content: saved,
	})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	baselineHash, _ := editResult.(map[string]interface{})["baselineHash"].(string)
	if baselineHash == "" {
		t.Fatal("edit did not capture a baseline hash")
	}
	if _, err := automation.execute(command{
		Action:       "save",
		Path:         filePath,
		Content:      saved,
		BaselineHash: baselineHash,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	written, err := os.ReadFile(filePath)
	if err != nil || string(written) != saved {
		t.Fatalf("saved bytes=%q err=%v", written, err)
	}

	if _, err := automation.execute(command{
		Action:  "edit",
		Path:    filePath,
		Content: dirty,
	}); err != nil {
		t.Fatalf("dirty edit: %v", err)
	}
	scanResult, err := automation.execute(command{Action: "recovery-scan"})
	if err != nil {
		t.Fatalf("recovery scan: %v", err)
	}
	scan := scanResult.(services.RecoveryScan)
	if len(scan.Files) != 1 || scan.Files[0].Content != dirty || scan.Files[0].Status != services.RecoveryStatusClean {
		t.Fatalf("unexpected recovery scan: %+v", scan)
	}
}

func TestRecoveryGuardProbeRejectsEveryPendingCleanupPath(t *testing.T) {
	set := newTestServiceSet(t)
	workspace := t.TempDir()
	otherWorkspace := t.TempDir()
	filePath := filepath.Join(workspace, "main.go")
	if err := os.WriteFile(filePath, []byte("package fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := set.Project.AddProject(workspace); err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	if _, err := set.Recovery.ScanRecoverable(); err != nil {
		t.Fatalf("resolve initial scan: %v", err)
	}
	if _, err := (&server{services: set}).execute(command{
		Action:   "edit",
		Path:     filePath,
		Content:  "package fixture\n// dirty\n",
		WindowID: "crashed",
	}); err != nil {
		t.Fatalf("journal crash record: %v", err)
	}
	if _, err := set.Recovery.ScanRecoverable(); err != nil {
		t.Fatalf("scan pending record: %v", err)
	}

	automation := &server{services: set}
	result, err := automation.execute(command{
		Action:    "recovery-guard-probe",
		Workspace: otherWorkspace,
		WindowID:  "crashed",
		Path:      filePath,
	})
	if err != nil {
		t.Fatalf("guard probe: %v", err)
	}
	guard, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("guard result type = %T", result)
	}
	rejections, ok := guard["rejections"].(map[string]string)
	if !ok || len(rejections) != 7 {
		t.Fatalf("guard rejections = %#v", guard["rejections"])
	}
	if enabled, _ := guard["journalEnabled"].(bool); !enabled {
		t.Fatal("guard probe disabled the recovery journal")
	}
	if state := set.Recovery.GetRecoveryState(); state.Phase != services.RecoveryPhasePending {
		t.Fatalf("recovery phase after guard probe = %q", state.Phase)
	}
	if _, err := os.Stat(filePath); err != nil {
		t.Fatalf("workspace file changed during guard probe: %v", err)
	}
}

func TestNativeWindowCloseProbeInvokesInjectedWindowClose(t *testing.T) {
	set := newTestServiceSet(t)
	workspace := t.TempDir()
	filePath := filepath.Join(workspace, "main.go")
	if err := os.WriteFile(filePath, []byte("package fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := set.Project.AddProject(workspace); err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	if _, err := set.Recovery.ScanRecoverable(); err != nil {
		t.Fatalf("resolve initial scan: %v", err)
	}
	if err := set.Recovery.SaveDirtyBuffer(
		"crashed", filePath, "dirty", "utf-8", "lf", 0, "",
	); err != nil {
		t.Fatalf("save recovery record: %v", err)
	}
	if _, err := set.Recovery.ScanRecoverable(); err != nil {
		t.Fatalf("scan pending record: %v", err)
	}
	closeCalls := 0
	set.CloseWindow = func() { closeCalls++ }

	if _, err := (&server{services: set}).execute(command{Action: "native-window-close-probe"}); err != nil {
		t.Fatalf("native close probe: %v", err)
	}
	if closeCalls != 1 {
		t.Fatalf("native close calls = %d, want 1", closeCalls)
	}
}

func validAgentToolRoundRendererEvidence(spec agentToolRoundSpec, marker string) map[string]interface{} {
	return map[string]interface{}{
		"ok": true, "rendererSubmitted": true, "agentModeConfigured": true,
		"storedProviderLoaded": true, "nativeToolCallObserved": true,
		"decisionObserved": true, "approvalMode": spec.ApprovalMode,
		"expectedDecision": spec.ExpectedDecision, "outcome": spec.ExpectedOutcome,
		"approvalObserved": true, "approvalPrecededExecution": true,
		"backendExecutionObserved": true, "executionUsageObserved": true,
		"usageSuccess": true, "usageSessionMatchesRequest": true,
		"usageObservationMatchesResult": true, "observationSubmitted": true,
		"nativeProtocolResultSubmitted": true,
		"finalAssistantObserved":        true,
		"rejectionSubmitted":            false,
		"manualControlRequired":         false,
		"manualControlRendered":         false,
		"manualControlClicked":          false,
		"toolCallId":                    spec.ToolCallID,
		"toolKind":                      spec.ToolKind,
		"usageUnitId":                   "unit-packaged-agent-" + spec.ToolKind,
		"usageSessionId":                "chat-packaged-agent",
		"usageOperation":                spec.ToolKind,
		"usagePending":                  false,
		"observation":                   spec.Observation("agent-tool-round.txt", marker),
		"assistantContent":              spec.FinalAssistant,
	}
}

func TestValidateOpenAINativeToolResultRequest(t *testing.T) {
	const (
		callID    = "call_packaged_agent_read"
		arguments = `{"path":"agent-tool-round.txt"}`
		marker    = "PACKAGED_AGENT_TOOL_OBSERVATION"
	)
	valid := map[string]interface{}{
		"messages": []interface{}{
			map[string]interface{}{"role": "user", "content": "inspect"},
			map[string]interface{}{
				"role": "assistant",
				"tool_calls": []interface{}{map[string]interface{}{
					"id": callID, "type": "function",
					"function": map[string]interface{}{"name": "read", "arguments": arguments},
				}},
			},
			map[string]interface{}{
				"role": "tool", "tool_call_id": callID, "content": "Read: " + marker,
			},
		},
	}
	if err := validateOpenAINativeToolResultRequest(valid, callID, "read", arguments, marker); err != nil {
		t.Fatalf("valid native tool result request: %v", err)
	}

	legacy := map[string]interface{}{"messages": []interface{}{
		map[string]interface{}{"role": "user", "content": "[Observation]\n" + marker},
	}}
	if err := validateOpenAINativeToolResultRequest(legacy, callID, "read", arguments, marker); err == nil {
		t.Fatal("legacy text observation was accepted")
	}

	wrongID := map[string]interface{}{"messages": []interface{}{
		map[string]interface{}{
			"role": "assistant",
			"tool_calls": []interface{}{map[string]interface{}{
				"id": "call_wrong", "type": "function",
				"function": map[string]interface{}{"name": "read", "arguments": arguments},
			}},
		},
		map[string]interface{}{"role": "tool", "tool_call_id": callID, "content": marker},
	}}
	if err := validateOpenAINativeToolResultRequest(wrongID, callID, "read", arguments, marker); err == nil {
		t.Fatal("mismatched provider tool-call ID was accepted")
	}
}

func TestValidateAgentToolRoundRendererRequiresTerminalUsageAndOrderedApproval(t *testing.T) {
	const marker = "PACKAGED_AGENT_TOOL_OBSERVATION"
	spec := readAgentToolRoundSpec()
	valid := validAgentToolRoundRendererEvidence(spec, marker)
	unitID, sessionID, err := validateAgentToolRoundRenderer(valid, spec, marker)
	if err != nil {
		t.Fatalf("valid renderer evidence: %v", err)
	}
	if unitID != "unit-packaged-agent-read" || sessionID != "chat-packaged-agent" {
		t.Fatalf("usage identity = %q/%q", unitID, sessionID)
	}

	tests := []struct {
		name      string
		field     string
		value     interface{}
		wantError string
	}{
		{name: "approval missing", field: "approvalObserved", value: false, wantError: "approvalObserved"},
		{name: "approval ordering missing", field: "approvalPrecededExecution", value: false, wantError: "approvalPrecededExecution"},
		{name: "backend execution missing", field: "backendExecutionObserved", value: false, wantError: "backendExecutionObserved"},
		{name: "usage missing", field: "executionUsageObserved", value: false, wantError: "executionUsageObserved"},
		{name: "empty unit", field: "usageUnitId", value: "", wantError: "usage identity"},
		{name: "empty session", field: "usageSessionId", value: "", wantError: "usage identity"},
		{name: "wrong operation", field: "usageOperation", value: "write", wantError: "invalid terminal usage"},
		{name: "unsuccessful", field: "usageSuccess", value: false, wantError: "usageSuccess"},
		{name: "pending", field: "usagePending", value: true, wantError: "invalid terminal usage"},
		{name: "wrong session", field: "usageSessionMatchesRequest", value: false, wantError: "usageSessionMatchesRequest"},
		{name: "wrong observation receipt", field: "usageObservationMatchesResult", value: false, wantError: "usageObservationMatchesResult"},
		{name: "missing marker", field: "observation", value: "other file", wantError: "result marker"},
		{name: "missing second completion", field: "assistantContent", value: "", wantError: "second provider completion"},
		{name: "wrong tool kind", field: "toolKind", value: "search", wantError: "unexpected tool call ID"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evidence := validAgentToolRoundRendererEvidence(spec, marker)
			evidence[test.field] = test.value
			_, _, err := validateAgentToolRoundRenderer(evidence, spec, marker)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want substring %q", err, test.wantError)
			}
		})
	}

	searchSpec := searchAgentToolRoundSpec()
	searchEvidence := validAgentToolRoundRendererEvidence(searchSpec, marker)
	unitID, _, err = validateAgentToolRoundRenderer(searchEvidence, searchSpec, marker)
	if err != nil {
		t.Fatalf("valid search renderer evidence: %v", err)
	}
	if unitID != "unit-packaged-agent-search" {
		t.Fatalf("search usage identity = %q", unitID)
	}
}

func TestValidateAgentNativeApprovalProbeAcceptsExactWriteAndRunEvidence(t *testing.T) {
	writePath := filepath.Join(t.TempDir(), "approved.txt")
	runCwd := t.TempDir()
	tests := []struct {
		name        string
		expectation services.AgentNativeApprovalExpectationForE2E
		call        services.AgentNativeApprovalCallForE2E
		want        map[string]interface{}
	}{
		{
			name: "write approve",
			expectation: services.AgentNativeApprovalExpectationForE2E{
				ToolKind: services.AgentNativeApprovalToolWriteForE2E,
				Decision: true, WritePath: writePath, WriteSize: 17,
			},
			call: services.AgentNativeApprovalCallForE2E{
				Sequence: 1, ToolKind: services.AgentNativeApprovalToolWriteForE2E,
				ExpectedToolKind: services.AgentNativeApprovalToolWriteForE2E,
				Matched:          true, Consumed: true, Decision: true,
				WritePath: writePath, WriteSize: 17,
			},
			want: map[string]interface{}{
				"approvedPath": writePath, "approvedBytes": int64(17),
				"backendNativeApprovalDecision": true,
			},
		},
		{
			name: "run reject",
			expectation: services.AgentNativeApprovalExpectationForE2E{
				ToolKind: services.AgentNativeApprovalToolRunForE2E,
				Decision: false, RunCommand: "tool --check", RunCwd: runCwd, RunRiskLevel: services.RiskDangerous,
			},
			call: services.AgentNativeApprovalCallForE2E{
				Sequence: 1, ToolKind: services.AgentNativeApprovalToolRunForE2E,
				ExpectedToolKind: services.AgentNativeApprovalToolRunForE2E,
				Matched:          true, Consumed: true, Decision: false,
				RunCommand: "tool --check", RunCwd: runCwd, RunRiskLevel: services.RiskDangerous,
			},
			want: map[string]interface{}{
				"approvedCommand": "tool --check", "approvedCwd": runCwd,
				"approvedRisk":                  services.RiskDangerous,
				"backendNativeApprovalDecision": false,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := services.AgentNativeApprovalSnapshotForE2E{
				Expected: 1, Consumed: 1, Remaining: 0, Complete: true, Restored: true,
				Calls: []services.AgentNativeApprovalCallForE2E{test.call},
			}
			result, err := validateAgentNativeApprovalProbe(snapshot, test.expectation, true)
			if err != nil {
				t.Fatalf("validate exact native approval: %v", err)
			}
			if result["backendApprovalSource"] != "e2e-exact-native-approver" ||
				result["backendNativeApprovalObserved"] != true ||
				result["backendNativeApprovalCallCount"] != 1 ||
				result["backendNativeApprovalExpectedCalls"] != 1 ||
				result["backendNativeApprovalSequence"] != 1 {
				t.Fatalf("invalid common approval evidence: %+v", result)
			}
			for key, value := range test.want {
				if result[key] != value {
					t.Fatalf("%s = %#v, want %#v; evidence=%+v", key, result[key], value, result)
				}
			}
		})
	}
}

func TestValidateAgentNativeApprovalProbeAcceptsRendererRejectWithoutBackendCall(t *testing.T) {
	expectation := services.AgentNativeApprovalExpectationForE2E{
		ToolKind: services.AgentNativeApprovalToolWriteForE2E,
		Decision: false, WritePath: filepath.Join(t.TempDir(), "rejected.txt"), WriteSize: 8,
	}
	snapshot := services.AgentNativeApprovalSnapshotForE2E{
		Expected: 1, Consumed: 0, Remaining: 1, Complete: false, Restored: true,
	}
	result, err := validateAgentNativeApprovalProbe(snapshot, expectation, false)
	if err != nil {
		t.Fatalf("validate renderer rejection without backend call: %v", err)
	}
	if result["backendNativeApprovalObserved"] != false ||
		result["backendNativeApprovalCallCount"] != 0 ||
		result["backendNativeApprovalExpectedCalls"] != 0 {
		t.Fatalf("invalid zero-call approval evidence: %+v", result)
	}
}

func TestValidateAgentNativeApprovalProbeFailsClosed(t *testing.T) {
	expectation := services.AgentNativeApprovalExpectationForE2E{
		ToolKind: services.AgentNativeApprovalToolWriteForE2E,
		Decision: true, WritePath: filepath.Join(t.TempDir(), "approved.txt"), WriteSize: 4,
	}
	validCall := services.AgentNativeApprovalCallForE2E{
		Sequence: 1, ToolKind: services.AgentNativeApprovalToolWriteForE2E,
		ExpectedToolKind: services.AgentNativeApprovalToolWriteForE2E,
		Matched:          true, Consumed: true, Decision: true,
		WritePath: expectation.WritePath, WriteSize: expectation.WriteSize,
	}
	validSnapshot := func() services.AgentNativeApprovalSnapshotForE2E {
		return services.AgentNativeApprovalSnapshotForE2E{
			Expected: 1, Consumed: 1, Remaining: 0, Complete: true, Restored: true,
			Calls: []services.AgentNativeApprovalCallForE2E{validCall},
		}
	}
	tests := []struct {
		name       string
		expectCall bool
		mutate     func(*services.AgentNativeApprovalSnapshotForE2E)
		wantError  string
	}{
		{name: "wrong expected count", expectCall: true, mutate: func(s *services.AgentNativeApprovalSnapshotForE2E) { s.Expected = 2 }, wantError: "lifecycle is incomplete"},
		{name: "not restored", expectCall: true, mutate: func(s *services.AgentNativeApprovalSnapshotForE2E) { s.Restored = false }, wantError: "lifecycle is incomplete"},
		{name: "missing call", expectCall: true, mutate: func(s *services.AgentNativeApprovalSnapshotForE2E) {
			s.Consumed = 0
			s.Remaining = 1
			s.Complete = false
			s.Calls = nil
		}, wantError: "not consumed exactly once"},
		{name: "duplicate call", expectCall: true, mutate: func(s *services.AgentNativeApprovalSnapshotForE2E) { s.Calls = append(s.Calls, validCall) }, wantError: "not consumed exactly once"},
		{name: "identity mismatch", expectCall: true, mutate: func(s *services.AgentNativeApprovalSnapshotForE2E) { s.Calls[0].Matched = false }, wantError: "identity or decision changed"},
		{name: "decision mismatch", expectCall: true, mutate: func(s *services.AgentNativeApprovalSnapshotForE2E) { s.Calls[0].Decision = false }, wantError: "identity or decision changed"},
		{name: "tool kind mismatch", expectCall: true, mutate: func(s *services.AgentNativeApprovalSnapshotForE2E) {
			s.Calls[0].ToolKind = services.AgentNativeApprovalToolRunForE2E
		}, wantError: "identity or decision changed"},
		{name: "renderer reject reached backend", expectCall: false, mutate: func(s *services.AgentNativeApprovalSnapshotForE2E) {}, wantError: "reached backend native approval"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := validSnapshot()
			test.mutate(&snapshot)
			_, err := validateAgentNativeApprovalProbe(snapshot, expectation, test.expectCall)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want substring %q", err, test.wantError)
			}
		})
	}
}

func TestValidateAgentToolRoundRendererRequiresIrreversibleRunReceipt(t *testing.T) {
	const marker = "PACKAGED_AGENT_RUN_OUTPUT"
	spec := runAgentToolRoundSpec("runManualApprove", "approve", "executed", "tool --check")
	validEvidence := func() map[string]interface{} {
		evidence := validAgentToolRoundRendererEvidence(spec, marker)
		evidence["manualControlRequired"] = true
		evidence["manualControlRendered"] = true
		evidence["manualControlClicked"] = true
		evidence["manualControlClickEventObserved"] = true
		evidence["manualControlWasEnabled"] = true
		evidence["manualControlAction"] = spec.ExpectedDecision
		evidence["manualControlCallId"] = spec.ToolCallID
		evidence["manualControlKind"] = spec.ToolKind
		evidence["externalReceiptId"] = "receipt-run-1"
		evidence["externalReceiptReversible"] = false
		evidence["externalCompensation"] = "not-needed"
		return evidence
	}

	unitID, sessionID, err := validateAgentToolRoundRenderer(validEvidence(), spec, marker)
	if err != nil {
		t.Fatalf("valid run receipt evidence: %v", err)
	}
	if unitID != "unit-packaged-agent-run" || sessionID != "chat-packaged-agent" {
		t.Fatalf("run usage identity = %q/%q", unitID, sessionID)
	}

	tests := []struct {
		name      string
		field     string
		value     interface{}
		wantError string
	}{
		{name: "missing receipt id", field: "externalReceiptId", value: "", wantError: "irreversible external receipt"},
		{name: "reversible receipt", field: "externalReceiptReversible", value: true, wantError: "irreversible external receipt"},
		{name: "wrong compensation", field: "externalCompensation", value: "pending", wantError: "irreversible external receipt"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evidence := validEvidence()
			evidence[test.field] = test.value
			_, _, err := validateAgentToolRoundRenderer(evidence, spec, marker)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want substring %q", err, test.wantError)
			}
		})
	}
}

func TestValidateAgentToolRoundRendererRequiresManualRejectWithoutExecution(t *testing.T) {
	const expectedRejection = "User rejected the write action"
	spec := agentToolRoundSpec{
		Name: "writeManualReject", ToolKind: "write", ApprovalMode: "ask",
		ExpectedDecision: "reject", ExpectedOutcome: "rejected",
		ToolCallID: "call_packaged_agent_write_reject", FinalAssistant: "PACKAGED_AGENT_WRITE_REJECT_ROUND_COMPLETE",
	}
	valid := map[string]interface{}{
		"ok": true, "rendererSubmitted": true, "agentModeConfigured": true,
		"storedProviderLoaded": true, "nativeToolCallObserved": true, "decisionObserved": true,
		"nativeProtocolResultSubmitted": true, "finalAssistantObserved": true,
		"toolCallId": spec.ToolCallID, "toolKind": spec.ToolKind,
		"approvalMode": spec.ApprovalMode, "expectedDecision": spec.ExpectedDecision, "outcome": spec.ExpectedOutcome,
		"assistantContent": spec.FinalAssistant, "rejection": expectedRejection,
		"approvalObserved": false, "approvalPrecededExecution": false,
		"backendExecutionObserved": false, "executionUsageObserved": false,
		"observationSubmitted": false, "rejectionSubmitted": true,
		"manualControlRequired": true, "manualControlRendered": true,
		"manualControlClicked": true, "manualControlClickEventObserved": true,
		"manualControlWasEnabled": true, "manualControlAction": "reject",
		"manualControlCallId": spec.ToolCallID, "manualControlKind": spec.ToolKind,
	}
	if unitID, sessionID, err := validateAgentToolRoundRenderer(valid, spec, expectedRejection); err != nil {
		t.Fatalf("valid manual reject renderer evidence: %v", err)
	} else if unitID != "" || sessionID != "" {
		t.Fatalf("rejected renderer returned usage identity %q/%q", unitID, sessionID)
	}

	tests := []struct {
		name      string
		field     string
		value     interface{}
		wantError string
	}{
		{name: "manual click missing", field: "manualControlClicked", value: false, wantError: "manualControlClicked"},
		{name: "wrong action", field: "manualControlAction", value: "approve", wantError: "wrong manual control"},
		{name: "backend execution occurred", field: "backendExecutionObserved", value: true, wantError: "unexpectedly reported backendExecutionObserved"},
		{name: "usage identity leaked", field: "usageUnitId", value: "unexpected", wantError: "unexpectedly returned usageUnitId"},
		{name: "missing rejection", field: "rejectionSubmitted", value: false, wantError: "did not submit a native rejection"},
		{name: "wrong rejection", field: "rejection", value: "other", wantError: "lost the rejection observation"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evidence := make(map[string]interface{}, len(valid)+1)
			for key, value := range valid {
				evidence[key] = value
			}
			evidence[test.field] = test.value
			_, _, err := validateAgentToolRoundRenderer(evidence, spec, expectedRejection)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want substring %q", err, test.wantError)
			}
		})
	}
}

func TestFingerprintAgentToolRoundWorkspaceDetectsContentAndPathChanges(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "fixture.txt")
	if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := fingerprintAgentToolRoundWorkspace(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("after"), 0o600); err != nil {
		t.Fatal(err)
	}
	afterContent, err := fingerprintAgentToolRoundWorkspace(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if before == afterContent {
		t.Fatal("content change did not change workspace fingerprint")
	}
	if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, filepath.Join(workspace, "renamed.txt")); err != nil {
		t.Fatal(err)
	}
	afterRename, err := fingerprintAgentToolRoundWorkspace(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if before == afterRename {
		t.Fatal("path change did not change workspace fingerprint")
	}
	ignored, err := fingerprintAgentToolRoundWorkspace(workspace, filepath.Join(workspace, "renamed.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "renamed.txt"), []byte("ignored change"), 0o600); err != nil {
		t.Fatal(err)
	}
	ignoredAfter, err := fingerprintAgentToolRoundWorkspace(workspace, "renamed.txt")
	if err != nil {
		t.Fatal(err)
	}
	if ignored != ignoredAfter {
		t.Fatal("ignored target changed workspace fingerprint")
	}
}
