//go:build e2e

package services

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
)

// AgentNativeApprovalToolKindForE2E identifies the native approval callback
// exercised by an Agent packaged-e2e round.
type AgentNativeApprovalToolKindForE2E string

const (
	AgentNativeApprovalToolWriteForE2E AgentNativeApprovalToolKindForE2E = "write"
	AgentNativeApprovalToolRunForE2E   AgentNativeApprovalToolKindForE2E = "run"
)

// AgentNativeApprovalExpectationForE2E binds one decision to the exact fields
// observable by the production native callback. Write approval does not expose
// content bytes or a content hash, so this helper deliberately cannot attest to
// either; callers must verify content independently at the execution boundary.
type AgentNativeApprovalExpectationForE2E struct {
	ToolKind AgentNativeApprovalToolKindForE2E
	Decision bool

	WritePath string
	WriteSize int64

	RunCommand   string
	RunCwd       string
	RunRiskLevel RiskLevel
}

// AgentNativeApprovalCallForE2E is one ordered native-callback observation.
// Consumed is true only when the call matched the current expectation. A
// matched rejection therefore has Consumed=true and Decision=false, while an
// identity mismatch or exhausted sequence has both fields false.
type AgentNativeApprovalCallForE2E struct {
	Sequence         int
	ToolKind         AgentNativeApprovalToolKindForE2E
	ExpectedToolKind AgentNativeApprovalToolKindForE2E
	Matched          bool
	Consumed         bool
	Decision         bool

	WritePath string
	WriteSize int64

	RunCommand   string
	RunCwd       string
	RunRiskLevel RiskLevel
}

// AgentNativeApprovalSnapshotForE2E is an immutable point-in-time copy of a
// probe. Calls is copied on every Snapshot invocation.
type AgentNativeApprovalSnapshotForE2E struct {
	Expected  int
	Consumed  int
	Remaining int
	Complete  bool
	Restored  bool
	Calls     []AgentNativeApprovalCallForE2E
}

// AgentNativeApprovalProbeForE2E records deterministic native approval calls.
// Its decision sequence and snapshots are safe for concurrent use.
type AgentNativeApprovalProbeForE2E struct {
	mu           sync.Mutex
	expectations []AgentNativeApprovalExpectationForE2E
	next         int
	calls        []AgentNativeApprovalCallForE2E
	restored     bool
}

// Snapshot returns a deep-enough copy for callers to retain or mutate without
// changing the probe's evidence.
func (p *AgentNativeApprovalProbeForE2E) Snapshot() AgentNativeApprovalSnapshotForE2E {
	if p == nil {
		return AgentNativeApprovalSnapshotForE2E{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	calls := append([]AgentNativeApprovalCallForE2E(nil), p.calls...)
	return AgentNativeApprovalSnapshotForE2E{
		Expected:  len(p.expectations),
		Consumed:  p.next,
		Remaining: len(p.expectations) - p.next,
		Complete:  p.next == len(p.expectations),
		Restored:  p.restored,
		Calls:     calls,
	}
}

// InstallAgentNativeApprovalSequenceForE2E replaces the write and run native
// callbacks with a deterministic, exact-identity, one-shot decision sequence.
// A wrong kind or identity and any call after exhaustion fail closed without
// consuming the current expectation. Call restore only after approval calls
// have quiesced; it is idempotent and restores both previous callbacks.
func InstallAgentNativeApprovalSequenceForE2E(
	agent *AgentService,
	expectations []AgentNativeApprovalExpectationForE2E,
) (*AgentNativeApprovalProbeForE2E, func(), error) {
	if agent == nil {
		return nil, nil, fmt.Errorf("agent service is required")
	}
	if len(expectations) == 0 {
		return nil, nil, fmt.Errorf("at least one native approval expectation is required")
	}

	normalized := make([]AgentNativeApprovalExpectationForE2E, len(expectations))
	for index, expectation := range expectations {
		value, err := normalizeAgentNativeApprovalExpectationForE2E(expectation)
		if err != nil {
			return nil, nil, fmt.Errorf("native approval expectation %d: %w", index, err)
		}
		normalized[index] = value
	}

	probe := &AgentNativeApprovalProbeForE2E{expectations: normalized}
	previousCommand := agent.approveCommand
	previousWrite := agent.approveWrite
	agent.approveWrite = func(targetPath string, size int64) bool {
		return probe.observeWrite(targetPath, size)
	}
	agent.approveCommand = func(command, cwd string, risk RiskLevel) bool {
		return probe.observeRun(command, cwd, risk)
	}

	var restoreOnce sync.Once
	restore := func() {
		restoreOnce.Do(func() {
			agent.approveCommand = previousCommand
			agent.approveWrite = previousWrite
			probe.mu.Lock()
			probe.restored = true
			probe.mu.Unlock()
		})
	}
	return probe, restore, nil
}

func normalizeAgentNativeApprovalExpectationForE2E(
	expectation AgentNativeApprovalExpectationForE2E,
) (AgentNativeApprovalExpectationForE2E, error) {
	switch expectation.ToolKind {
	case AgentNativeApprovalToolWriteForE2E:
		if expectation.RunCommand != "" || expectation.RunCwd != "" || expectation.RunRiskLevel != "" {
			return AgentNativeApprovalExpectationForE2E{}, fmt.Errorf("write expectation must not contain run identity")
		}
		path, err := canonicalAgentApprovalPathForE2E("write path", expectation.WritePath)
		if err != nil {
			return AgentNativeApprovalExpectationForE2E{}, err
		}
		if expectation.WriteSize < 0 {
			return AgentNativeApprovalExpectationForE2E{}, fmt.Errorf("write size must not be negative")
		}
		expectation.WritePath = path
		return expectation, nil
	case AgentNativeApprovalToolRunForE2E:
		if expectation.WritePath != "" || expectation.WriteSize != 0 {
			return AgentNativeApprovalExpectationForE2E{}, fmt.Errorf("run expectation must not contain write identity")
		}
		if strings.TrimSpace(expectation.RunCommand) == "" {
			return AgentNativeApprovalExpectationForE2E{}, fmt.Errorf("run command is required")
		}
		cwd, err := canonicalAgentApprovalPathForE2E("run cwd", expectation.RunCwd)
		if err != nil {
			return AgentNativeApprovalExpectationForE2E{}, err
		}
		switch expectation.RunRiskLevel {
		case RiskSafe, RiskElevated, RiskDangerous:
		default:
			return AgentNativeApprovalExpectationForE2E{}, fmt.Errorf("run risk level %q is invalid", expectation.RunRiskLevel)
		}
		expectation.RunCwd = cwd
		return expectation, nil
	default:
		return AgentNativeApprovalExpectationForE2E{}, fmt.Errorf("tool kind %q is invalid", expectation.ToolKind)
	}
}

func canonicalAgentApprovalPathForE2E(label, value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s is required", label)
	}
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("%s must be absolute", label)
	}
	return filepath.Clean(value), nil
}

func (p *AgentNativeApprovalProbeForE2E) observeWrite(targetPath string, size int64) bool {
	path := filepath.Clean(targetPath)
	return p.observe(AgentNativeApprovalCallForE2E{
		ToolKind:  AgentNativeApprovalToolWriteForE2E,
		WritePath: path,
		WriteSize: size,
	}, filepath.IsAbs(targetPath))
}

func (p *AgentNativeApprovalProbeForE2E) observeRun(command, cwd string, risk RiskLevel) bool {
	cleanCwd := filepath.Clean(cwd)
	return p.observe(AgentNativeApprovalCallForE2E{
		ToolKind:     AgentNativeApprovalToolRunForE2E,
		RunCommand:   command,
		RunCwd:       cleanCwd,
		RunRiskLevel: risk,
	}, filepath.IsAbs(cwd))
}

func (p *AgentNativeApprovalProbeForE2E) observe(call AgentNativeApprovalCallForE2E, pathIsAbsolute bool) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	call.Sequence = len(p.calls) + 1
	if p.next >= len(p.expectations) {
		p.calls = append(p.calls, call)
		return false
	}

	expected := p.expectations[p.next]
	call.ExpectedToolKind = expected.ToolKind
	matched := pathIsAbsolute && call.ToolKind == expected.ToolKind
	if matched {
		switch call.ToolKind {
		case AgentNativeApprovalToolWriteForE2E:
			matched = call.WritePath == expected.WritePath && call.WriteSize == expected.WriteSize
		case AgentNativeApprovalToolRunForE2E:
			matched = call.RunCommand == expected.RunCommand && call.RunCwd == expected.RunCwd && call.RunRiskLevel == expected.RunRiskLevel
		}
	}
	if !matched {
		p.calls = append(p.calls, call)
		return false
	}

	call.Matched = true
	call.Consumed = true
	call.Decision = expected.Decision
	p.next++
	p.calls = append(p.calls, call)
	return call.Decision
}
