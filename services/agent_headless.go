package services

// The headless host is a deliberately small consumer of the desktop agent
// authority.  It is used by the local CLI/CI probe; it is not a second tool
// registry and it does not expose a renderer-callable root setter.

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	pathpkg "path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
)

// HeadlessAgentTool is the intentionally narrow catalog projection exposed to
// a non-desktop consumer.  The first consumer only supports the production
// backend-owned read tool; capability tokens and executable dispatch keys are
// never part of this projection.
type HeadlessAgentTool struct {
	ID       string `json:"id"`
	Source   string `json:"source"`
	Risk     string `json:"risk"`
	Approval string `json:"approval"`
}

// HeadlessAgentCatalog retains the authoritative revision while exposing only
// the tool subset this consumer is prepared to execute.
type HeadlessAgentCatalog struct {
	Revision uint64              `json:"revision"`
	Tools    []HeadlessAgentTool `json:"tools"`
}

// HeadlessReadResult contains metadata only.  In particular, file contents,
// absolute paths, capability tokens, and usage internals are not returned.
type HeadlessReadResult struct {
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
}

const maxHeadlessRelativePathBytes = 4096

// HeadlessAgentHost owns the trusted services for one short-lived CLI
// process.  The workspace and state paths are fixed at construction and are
// never accepted from a renderer or from a tool argument.
type HeadlessAgentHost struct {
	lock       *InstanceLock
	stateRoot  *os.Root
	agent      *AgentService
	file       *FileService
	permission *AIPermissionService
	lifecycle  *AgentLifecycle

	operationMu sync.Mutex
	operations  sync.WaitGroup
	closed      bool
	closeOnce   sync.Once
	closeErr    error

	beforeReadExecutionForTest func()
	beforeSessionCloseForTest  func() error
}

// NewHeadlessAgentHost wires the existing production AgentService runtime,
// FileService root capability, and durable usage lifecycle for a standalone
// consumer.  Both directories must be explicit absolute existing directories;
// the state directory may not be the workspace or one of its descendants.
func NewHeadlessAgentHost(workspaceDir, stateDir string) (*HeadlessAgentHost, error) {
	workspaceRoot, err := headlessExistingDirectory(workspaceDir, "workspace")
	if err != nil {
		return nil, err
	}
	persistenceDir, err := headlessStateDirectory(stateDir)
	if err != nil {
		return nil, err
	}
	if headlessPathInside(workspaceRoot, persistenceDir) {
		return nil, fmt.Errorf("state directory must be outside the workspace: %w", ErrInvalidInput)
	}
	stateRoot, _, err := openHeadlessStateRoot(persistenceDir)
	if err != nil {
		return nil, err
	}
	if err := validateHeadlessStateLeavesWithinRoot(stateRoot); err != nil {
		_ = stateRoot.Close()
		return nil, err
	}

	lock := NewInstanceLockWithRoot(persistenceDir, stateRoot)
	if err := lock.Acquire(); err != nil {
		_ = stateRoot.Close()
		return nil, headlessStateSetupError("acquire headless state lock", err)
	}

	cleanup := func() {
		_ = lock.Release()
		_ = stateRoot.Close()
	}
	workspace := NewWorkspaceContext()
	if err := workspace.Set(workspaceRoot); err != nil {
		cleanup()
		return nil, fmt.Errorf("publish headless workspace: %w", ErrInvalidInput)
	}

	// These constructors and wiring functions are the same production
	// authority used by the desktop bootstrap.  Optional MCP/Skill/search/AI
	// systems are intentionally left unwired for this first CLI slice.
	auditFile, err := openHeadlessAuditLog(stateRoot)
	if err != nil {
		cleanup()
		return nil, err
	}
	agent := newAgentServiceWithAuditRoot(workspace, auditFile, stateRoot, "agent-audit.log")
	file := NewFileServiceWithWorkspaceContext(workspace)
	if agent.auditLog == nil {
		cleanup()
		return nil, fmt.Errorf("headless audit log is unavailable: %w", ErrUsagePersistence)
	}
	if err := agent.setWorkspaceRoot(workspaceRoot); err != nil {
		_ = agent.Close()
		cleanup()
		return nil, fmt.Errorf("bind headless agent workspace: %w", ErrInvalidInput)
	}
	if err := file.setWorkspaceRoot(workspaceRoot); err != nil {
		_ = agent.Close()
		cleanup()
		return nil, fmt.Errorf("bind headless file workspace: %w", ErrInvalidInput)
	}
	permission := newAIPermissionService(persistenceDir, stateRoot)
	if err := WireAgentExecutionCore(agent, file, nil, nil, nil, permission); err != nil {
		_ = file.close()
		_ = agent.Close()
		cleanup()
		return nil, fmt.Errorf("wire headless agent execution core: %w", ErrNotAllowed)
	}
	lifecycle, err := wireAgentLifecycleWithStateRoot(
		agent,
		NewAIService(),
		NewAIPlanService(),
		NewAIGoalService(),
		permission,
		stateRoot,
	)
	if err != nil {
		_ = file.close()
		_ = agent.Close()
		cleanup()
		// The root-backed lifecycle constructor only consumes durable state before
		// publication. Unclassified decode/shape failures are therefore poisoned
		// persistence, never a generic permission denial.
		return nil, headlessStateSetupError(
			"wire headless agent lifecycle", errors.Join(ErrUsagePersistencePoisoned, err),
		)
	}
	if err := validateHeadlessStateLeavesWithinRoot(stateRoot); err != nil {
		_ = file.close()
		_ = agent.Close()
		cleanup()
		return nil, err
	}
	return &HeadlessAgentHost{
		lock: lock, stateRoot: stateRoot, agent: agent, file: file, permission: permission,
		lifecycle: lifecycle,
	}, nil
}

// Close releases the root capability, audit handle, and state lock.  It is
// idempotent so callers can defer it while also handling an early error.
func (h *HeadlessAgentHost) Close() error {
	if h == nil {
		return nil
	}
	h.closeOnce.Do(func() {
		h.operationMu.Lock()
		h.closed = true
		h.operationMu.Unlock()
		h.operations.Wait()

		var errs []error
		if h.file != nil {
			if err := h.file.close(); err != nil {
				errs = append(errs, err)
			}
		}
		if h.agent != nil {
			if err := h.agent.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		if h.lock != nil {
			if err := h.lock.Release(); err != nil {
				errs = append(errs, err)
			}
		}
		if h.stateRoot != nil {
			if err := h.stateRoot.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		h.closeErr = errors.Join(errs...)
	})
	return h.closeErr
}

func (h *HeadlessAgentHost) beginOperation() (func(), error) {
	if h == nil {
		return nil, fmt.Errorf("headless agent is unavailable: %w", ErrNotAllowed)
	}
	h.operationMu.Lock()
	defer h.operationMu.Unlock()
	if h.closed {
		return nil, fmt.Errorf("headless agent is closed: %w", ErrNotAllowed)
	}
	h.operations.Add(1)
	return h.operations.Done, nil
}

// Catalog refreshes the authoritative production catalog and returns only the
// backend-owned read definition.  A missing or altered read definition is a
// hard failure rather than an invitation to install a substitute ToolDef.
func (h *HeadlessAgentHost) Catalog(ctx context.Context) (HeadlessAgentCatalog, error) {
	done, err := h.beginOperation()
	if err != nil {
		return HeadlessAgentCatalog{}, err
	}
	defer done()
	return h.catalog(ctx)
}

// PendingExternalReceiptDispositions exposes only the trusted, content-free
// recovery inventory to a short-lived headless consumer. The operation lease
// keeps the root-backed lifecycle and permission state alive until the lookup
// is complete, so Close cannot race a ledger read.
func (h *HeadlessAgentHost) PendingExternalReceiptDispositions() ([]AgentExternalReceiptRecoveryEntry, error) {
	done, err := h.beginOperation()
	if err != nil {
		return nil, err
	}
	defer done()
	if h.lifecycle == nil {
		return nil, fmt.Errorf("headless recovery lifecycle is unavailable: %w", ErrNotAllowed)
	}
	return h.lifecycle.PendingExternalReceiptDispositions()
}

// DispatchExternalReceiptDisposition is the trusted headless bridge for the
// generic external-receipt recovery dispatcher. The lifecycle enforces the
// exact manual-unknown disposition and never calls an adapter or restores
// runtime authority.
func (h *HeadlessAgentHost) DispatchExternalReceiptDisposition(
	request AgentExternalReceiptDispositionRequest,
) (AgentExternalReceiptDispositionResult, error) {
	done, err := h.beginOperation()
	if err != nil {
		return AgentExternalReceiptDispositionResult{}, err
	}
	defer done()
	if h.lifecycle == nil {
		return AgentExternalReceiptDispositionResult{}, fmt.Errorf("headless recovery lifecycle is unavailable: %w", ErrNotAllowed)
	}
	return h.lifecycle.DispatchExternalReceiptDisposition(request)
}

func (h *HeadlessAgentHost) catalog(ctx context.Context) (HeadlessAgentCatalog, error) {
	if h == nil || h.agent == nil {
		return HeadlessAgentCatalog{}, fmt.Errorf("headless agent is unavailable: %w", ErrNotAllowed)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	catalog, err := h.agent.GetAgentToolCatalog(ctx)
	if err != nil {
		return HeadlessAgentCatalog{}, headlessExecutionError(err)
	}
	for _, tool := range catalog.Tools {
		if tool.ID != "read" {
			continue
		}
		if tool.Source != string(agentcore.SourceBuiltin) ||
			tool.Risk != string(agentcore.RiskReadOnly) ||
			tool.Approval != string(agentcore.ApprovalBackendPolicy) ||
			tool.Mutation != string(agentcore.MutationNone) {
			return HeadlessAgentCatalog{}, fmt.Errorf("production read tool policy is not read-only: %w", ErrNotAllowed)
		}
		return HeadlessAgentCatalog{
			Revision: catalog.Revision,
			Tools: []HeadlessAgentTool{{
				ID: tool.ID, Source: tool.Source, Risk: tool.Risk, Approval: tool.Approval,
			}},
		}, nil
	}
	return HeadlessAgentCatalog{}, fmt.Errorf("production read tool is unavailable: %w", ErrNotAllowed)
}

// Read executes the production read ToolDef through the normal session,
// catalog revision, capability, handler, and durable usage boundaries. The
// metadata comes from that same authorized read; no second path-based read is
// performed outside the capability.
func (h *HeadlessAgentHost) Read(ctx context.Context, relativePath string) (result HeadlessReadResult, retErr error) {
	done, err := h.beginOperation()
	if err != nil {
		return HeadlessReadResult{}, err
	}
	defer done()
	if h == nil || h.agent == nil || h.file == nil {
		return HeadlessReadResult{}, fmt.Errorf("headless agent is unavailable: %w", ErrNotAllowed)
	}
	if err := validateHeadlessRelativePath(relativePath); err != nil {
		return HeadlessReadResult{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if h.beforeReadExecutionForTest != nil {
		h.beforeReadExecutionForTest()
	}
	catalog, err := h.catalog(ctx)
	if err != nil {
		return HeadlessReadResult{}, err
	}
	if len(catalog.Tools) != 1 || catalog.Tools[0].ID != "read" {
		return HeadlessReadResult{}, fmt.Errorf("headless catalog does not authorize read: %w", ErrNotAllowed)
	}
	sessionID, err := h.agent.createAgentSessionTrusted("chat")
	if err != nil {
		return HeadlessReadResult{}, headlessExecutionError(err)
	}
	defer func() {
		var closeErr error
		if h.beforeSessionCloseForTest != nil {
			closeErr = h.beforeSessionCloseForTest()
		}
		closeErr = errors.Join(closeErr, h.agent.closeAgentSessionTrusted(sessionID))
		if closeErr != nil {
			result = HeadlessReadResult{}
			retErr = errors.Join(retErr, headlessLifecycleCloseError(closeErr))
		}
	}()

	grant, err := h.agent.RequestAgentToolCapability(ctx, AgentToolExecutionRequest{
		SessionID: sessionID, CatalogRevision: catalog.Revision, ToolID: "read",
		Arguments: map[string]interface{}{"path": relativePath},
	})
	if err != nil {
		return HeadlessReadResult{}, headlessExecutionError(err)
	}
	executionResult, err := h.agent.ExecuteApprovedAgentTool(ctx, AgentToolCapabilityExecution{
		Token: grant.Token, SessionID: sessionID, CatalogRevision: catalog.Revision,
		ToolID: "read", Arguments: map[string]interface{}{"path": relativePath},
	})
	// Never retain or return result.Observation: it contains the file content.
	if err != nil {
		return HeadlessReadResult{}, headlessExecutionError(err)
	}
	if !executionResult.Usage.Success || executionResult.Usage.Pending {
		return HeadlessReadResult{}, fmt.Errorf("headless read did not reach a terminal usage state: %w", ErrUsageReceiptState)
	}
	byteCount, err := strconv.Atoi(executionResult.Metadata["bytes"])
	if err != nil || byteCount < 0 {
		return HeadlessReadResult{}, fmt.Errorf("headless read byte metadata is invalid: %w", ErrUsageReceiptState)
	}
	digest := strings.TrimSpace(executionResult.Metadata["sha256"])
	if len(digest) != sha256.Size*2 || !isLowerHex(digest) {
		return HeadlessReadResult{}, fmt.Errorf("headless read digest metadata is invalid: %w", ErrUsageReceiptState)
	}
	return HeadlessReadResult{Bytes: byteCount, SHA256: digest}, nil
}

func isLowerHex(value string) bool {
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func headlessExistingDirectory(value, label string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s directory is required: %w", label, ErrInvalidInput)
	}
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("%s directory must be absolute: %w", label, ErrInvalidInput)
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("resolve %s directory: %w", label, ErrInvalidInput)
	}
	abs = filepath.Clean(abs)
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("%s directory is unavailable: %w", label, ErrInvalidInput)
	}
	resolved, resolveErr := filepath.EvalSymlinks(abs)
	if resolveErr != nil {
		return "", fmt.Errorf("resolve %s directory identity: %w", label, ErrInvalidInput)
	}
	return filepath.Clean(resolved), nil
}

func headlessStateDirectory(value string) (string, error) {
	if strings.TrimSpace(value) == "" || !filepath.IsAbs(value) {
		return "", fmt.Errorf("state directory must be an absolute real directory: %w", ErrInvalidInput)
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("resolve state directory: %w", ErrInvalidInput)
	}
	abs = filepath.Clean(abs)
	inputInfo, err := os.Lstat(abs)
	if err != nil || !inputInfo.IsDir() || inputInfo.Mode()&fs.ModeSymlink != 0 {
		return "", fmt.Errorf("state directory must be a real directory: %w", ErrInvalidInput)
	}
	linked, err := headlessPathHasLinkBoundary(abs)
	if err != nil || linked {
		return "", fmt.Errorf("state directory may not be a link or reparse point: %w", ErrInvalidInput)
	}
	state, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve state directory identity: %w", ErrInvalidInput)
	}
	state = filepath.Clean(state)
	info, err := os.Lstat(state)
	if err != nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return "", fmt.Errorf("state directory must be a real directory: %w", ErrInvalidInput)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("state directory permissions must be owner-only: %w", ErrInvalidInput)
	}
	root, bound, err := openHeadlessStateRoot(state)
	if err != nil {
		return "", err
	}
	_ = bound
	if err := root.Close(); err != nil {
		return "", fmt.Errorf("close state root capability: %w", ErrUsagePersistence)
	}
	return state, nil
}

func openHeadlessStateRoot(state string) (*os.Root, os.FileInfo, error) {
	before, err := os.Lstat(state)
	if err != nil || !before.IsDir() || before.Mode()&fs.ModeSymlink != 0 {
		return nil, nil, fmt.Errorf("bind state directory identity: %w", ErrInvalidInput)
	}
	root, err := os.OpenRoot(state)
	if err != nil {
		return nil, nil, fmt.Errorf("open state root capability: %w", ErrUsagePersistence)
	}
	bound, err := root.Stat(".")
	if err != nil || !os.SameFile(before, bound) {
		_ = root.Close()
		return nil, nil, fmt.Errorf("state directory identity changed: %w", ErrUsagePersistencePoisoned)
	}
	// Recheck the security properties on the bound capability itself. The
	// pathname validation above can race with a same-path directory replacement
	// before OpenRoot, while this handle is the authority retained by the host.
	if runtime.GOOS != "windows" {
		if bound.Mode().Perm()&0o077 != 0 {
			_ = root.Close()
			return nil, nil, fmt.Errorf("bound state directory permissions must be owner-only: %w", ErrInvalidInput)
		}
		directory, openErr := root.Open(".")
		if openErr != nil {
			_ = root.Close()
			return nil, nil, fmt.Errorf("open bound state directory: %w", ErrUsagePersistencePoisoned)
		}
		owned, ownerErr := agentFileOwnedByCurrentUser(directory)
		closeErr := directory.Close()
		if ownerErr != nil || closeErr != nil || !owned {
			_ = root.Close()
			return nil, nil, fmt.Errorf("bound state directory owner is not trusted: %w", ErrInvalidInput)
		}
	}
	after, err := os.Lstat(state)
	if err != nil || after.Mode()&fs.ModeSymlink != 0 || !os.SameFile(bound, after) {
		_ = root.Close()
		return nil, nil, fmt.Errorf("state directory path changed: %w", ErrUsagePersistencePoisoned)
	}
	return root, bound, nil
}

func validateHeadlessStateLeavesWithinRoot(root *os.Root) error {
	if root == nil {
		return fmt.Errorf("headless state root is unavailable: %w", ErrUsagePersistence)
	}
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return fmt.Errorf("enumerate headless state: %w", ErrUsagePersistence)
	}
	for _, entry := range entries {
		if err := validateHeadlessStateLeaf(root, entry.Name()); err != nil {
			return err
		}
	}
	for _, name := range []string{"agent_lifecycle_identity.key", "agent_external_receipt_identity.key"} {
		if err := validateHeadlessIdentityKey(root, name); err != nil {
			return err
		}
	}
	return nil
}

func validateHeadlessIdentityKey(root *os.Root, name string) error {
	file, err := root.Open(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open headless identity key: %w", ErrUsagePersistencePoisoned)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() {
		return fmt.Errorf("stat headless identity key: %w", ErrUsagePersistencePoisoned)
	}
	named, err := root.Lstat(name)
	if err != nil || named.Mode()&fs.ModeSymlink != 0 || !os.SameFile(opened, named) {
		return fmt.Errorf("headless identity key changed identity: %w", ErrUsagePersistencePoisoned)
	}
	data, err := io.ReadAll(io.LimitReader(file, 65))
	if err != nil || len(data) > 64 {
		return fmt.Errorf("read headless identity key: %w", ErrUsagePersistencePoisoned)
	}
	if _, err := decodeAESKey(data); err != nil {
		return fmt.Errorf("headless identity key is invalid: %w", ErrUsagePersistencePoisoned)
	}
	return nil
}

func validateHeadlessStateLeaf(root *os.Root, name string) error {
	named, err := root.Lstat(name)
	if err != nil {
		return fmt.Errorf("inspect headless state entry: %w", ErrUsagePersistencePoisoned)
	}
	if !named.Mode().IsRegular() || named.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("headless state entry %q is not a regular file: %w", name, ErrUsagePersistencePoisoned)
	}
	file, err := root.Open(name)
	if err != nil {
		return fmt.Errorf("open headless state entry %q: %w", name, ErrUsagePersistencePoisoned)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() {
		return fmt.Errorf("stat headless state entry %q: %w", name, ErrUsagePersistencePoisoned)
	}
	namedAfter, err := root.Lstat(name)
	if err != nil || namedAfter.Mode()&fs.ModeSymlink != 0 || !os.SameFile(opened, namedAfter) {
		return fmt.Errorf("headless state entry %q changed identity: %w", name, ErrUsagePersistencePoisoned)
	}
	multipleLinks, err := agentFileHasMultipleLinks(file)
	if err != nil || multipleLinks {
		return fmt.Errorf("headless state entry %q has an unsafe link identity: %w", name, ErrUsagePersistencePoisoned)
	}
	if runtime.GOOS != "windows" && opened.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("headless state entry %q permissions are too broad: %w", name, ErrUsagePersistencePoisoned)
	}
	return nil
}

func openHeadlessAuditLog(root *os.Root) (*os.File, error) {
	file, err := openAgentStateRegularFile(root, "agent-audit.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		if errors.Is(err, ErrUsagePersistencePoisoned) {
			return nil, fmt.Errorf("open headless audit log: %w", ErrUsagePersistencePoisoned)
		}
		if errors.Is(err, ErrUsagePersistenceIndeterminate) {
			return nil, fmt.Errorf("open headless audit log: %w", ErrUsagePersistenceIndeterminate)
		}
		return nil, fmt.Errorf("open headless audit log: %w", ErrUsagePersistence)
	}
	return file, nil
}

func headlessPathInside(parent, candidate string) bool {
	if sameWorkspaceIdentityPath(parent, candidate) {
		return true
	}
	rel, err := filepath.Rel(parent, candidate)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func validateHeadlessRelativePath(value string) error {
	if strings.TrimSpace(value) == "" || len([]byte(value)) > maxHeadlessRelativePathBytes ||
		!IsRelativePathSafe(value) || strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("path must be workspace-relative: %w", ErrInvalidInput)
	}
	// Backslashes are rejected even on Unix so the same CLI contract cannot be
	// reinterpreted as a Windows traversal when a state is moved cross-platform.
	if strings.ContainsRune(value, '\\') {
		return fmt.Errorf("path must use workspace-relative slash notation: %w", ErrInvalidInput)
	}
	if pathpkg.IsAbs(value) || pathpkg.Clean(value) != value || value == "." {
		return fmt.Errorf("path must use canonical workspace-relative notation: %w", ErrInvalidInput)
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || component == "." || component == ".." || strings.ContainsRune(component, ':') {
			return fmt.Errorf("path contains an unsafe component: %w", ErrInvalidInput)
		}
	}
	return nil
}

func headlessLifecycleCloseError(err error) error {
	switch {
	case errors.Is(err, agentcore.ErrSessionPersistenceIndeterminate):
		return fmt.Errorf("headless session terminal state is indeterminate: %w", ErrUsagePersistenceIndeterminate)
	case errors.Is(err, agentcore.ErrSessionPersistencePoisoned):
		return fmt.Errorf("headless session persistence is poisoned: %w", ErrUsagePersistencePoisoned)
	default:
		return fmt.Errorf("headless session terminal state was not persisted: %w", ErrUsagePersistence)
	}
}

func headlessStateSetupError(label string, err error) error {
	switch {
	case errors.Is(err, ErrUsagePersistenceIndeterminate),
		errors.Is(err, agentcore.ErrSessionPersistenceIndeterminate):
		return fmt.Errorf("%s is indeterminate: %w", label, ErrUsagePersistenceIndeterminate)
	case errors.Is(err, ErrUsagePersistencePoisoned),
		errors.Is(err, agentcore.ErrSessionPersistencePoisoned):
		return fmt.Errorf("%s is poisoned: %w", label, ErrUsagePersistencePoisoned)
	case errors.Is(err, ErrUsagePersistence):
		return fmt.Errorf("%s is unavailable: %w", label, ErrUsagePersistence)
	default:
		return fmt.Errorf("%s was rejected: %w", label, ErrNotAllowed)
	}
}

func headlessExecutionError(err error) error {
	if err == nil {
		return nil
	}
	for _, sentinel := range []error{
		ErrUsagePersistence, ErrUsagePersistenceIndeterminate, ErrUsagePersistencePoisoned,
		ErrUsageReceiptState, ErrNotAllowed, ErrInvalidInput,
	} {
		if errors.Is(err, sentinel) {
			return fmt.Errorf("headless operation rejected: %w", sentinel)
		}
	}
	if errors.Is(err, agentcore.ErrSessionPersistenceIndeterminate) {
		return fmt.Errorf("headless operation rejected: %w", ErrUsagePersistenceIndeterminate)
	}
	if errors.Is(err, agentcore.ErrSessionPersistencePoisoned) {
		return fmt.Errorf("headless operation rejected: %w", ErrUsagePersistencePoisoned)
	}
	return fmt.Errorf("headless operation failed: %w", ErrNotAllowed)
}
