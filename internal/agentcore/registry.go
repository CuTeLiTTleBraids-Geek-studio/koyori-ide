// Package agentcore contains the headless, transport-independent foundation
// for Agent tool discovery and invocation. It deliberately has no dependency
// on Wails, renderer state, or a particular AI provider so desktop, CLI, and
// CI callers can consume exactly the same catalog contract.
package agentcore

import (
	"bytes"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

var (
	// ErrInvalidToolDef means a ToolDef is malformed or has a schema the
	// core cannot prove safe to accept. Callers must not publish it anyway.
	ErrInvalidToolDef = errors.New("invalid agent tool definition")
	// ErrDuplicateTool protects the single catalog namespace. In particular a
	// dynamic source must never replace a builtin by reusing its logical ID or
	// provider wire name.
	ErrDuplicateTool = errors.New("duplicate agent tool")
	// ErrStaleCatalog means a tool list changed after a provider response was
	// prepared. Retrying against the newly fetched catalog is required; using a
	// stale invocation is not an acceptable fallback.
	ErrStaleCatalog = errors.New("stale agent tool catalog")
	// ErrUnknownTool is deliberately distinct from malformed input so callers
	// can show a useful model observation without accidentally invoking an
	// unregistered dynamic tool.
	ErrUnknownTool = errors.New("unknown agent tool")
	// ErrInvalidArguments covers invalid JSON and JSON that does not satisfy
	// the tool's closed input schema.
	ErrInvalidArguments = errors.New("invalid agent tool arguments")
)

const (
	maxProviderToolNameLength = 64
	maxToolIDLength           = 512
)

var (
	providerToolNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	toolIDPattern           = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
)

// ToolSource identifies the owning subsystem. A source replacement is atomic:
// a connection refresh cannot leave half the MCP tools from an old server in
// the catalog.
type ToolSource string

const (
	SourceBuiltin     ToolSource = "builtin"
	SourceMCP         ToolSource = "mcp"
	SourceWorkflow    ToolSource = "workflow"
	SourceSkill       ToolSource = "skill"
	SourceComputerUse ToolSource = "computer-use"
)

// Risk describes the highest impact of a tool. It is catalog metadata, not a
// renderer decision; the executor still performs its own capability checks.
type Risk string

const (
	RiskReadOnly  Risk = "read-only"
	RiskElevated  Risk = "elevated"
	RiskDangerous Risk = "dangerous"
)

// ApprovalMode describes how the headless executor obtains approval. Every
// mode still produces a one-time backend capability; "backend-policy" lets a
// trusted host policy grant read-only operations without trusting renderer
// state.
type ApprovalMode string

const (
	ApprovalManual        ApprovalMode = "manual"
	ApprovalBackendPolicy ApprovalMode = "backend-policy"
)

// MutationMode tells adapters whether a tool must be routed through the
// workspace edit transaction. Tool registration cannot declare a mutating tool
// as a plain no-op and expect the executor to infer safety later.
type MutationMode string

const (
	MutationNone                 MutationMode = "none"
	MutationWorkspaceTransaction MutationMode = "workspace-transaction"
	// MutationExternal represents a side effect outside the workspace edit
	// transaction, such as an MCP deployment or remote issue update. It must be
	// explicit so adapters cannot disguise those effects as read-only.
	MutationExternal MutationMode = "external"
)

// ToolDef is the one catalog shape shared by native provider definitions,
// legacy fence parsing, and backend execution. Logical IDs may contain dots
// (for example mcp.server.tool); WireName is provider-safe and resolves back
// to the exact logical ID through the catalog revision.
//
// ExecuteKey and Metadata are intentionally part of the headless definition:
// transports can serialize the public metadata while trusted adapters use the
// opaque execution key and metadata to invoke their service without parsing a
// logical ID string.
type ToolDef struct {
	ID          string            `json:"id"`
	WireName    string            `json:"wireName"`
	Description string            `json:"description"`
	InputSchema json.RawMessage   `json:"inputSchema"`
	Source      ToolSource        `json:"source"`
	Risk        Risk              `json:"risk"`
	Approval    ApprovalMode      `json:"approval"`
	Mutation    MutationMode      `json:"mutation"`
	ExecuteKey  string            `json:"-"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// Catalog is an immutable snapshot. Its monotonically increasing Revision is
// sent with every provider tool call and checked again before capability
// issuance, making dynamic MCP/workflow/skill changes fail closed.
type Catalog struct {
	Revision uint64    `json:"revision"`
	Tools    []ToolDef `json:"tools"`
}

// Invocation is the canonical, validated form of a provider/legacy tool call.
// Arguments is stable JSON (object keys sorted by encoding/json) and the hash
// can therefore bind a capability to precisely the arguments the user saw.
type Invocation struct {
	SessionID       string          `json:"sessionId"`
	CatalogRevision uint64          `json:"catalogRevision"`
	Tool            ToolDef         `json:"tool"`
	Arguments       json.RawMessage `json:"arguments"`
	ArgumentsHash   string          `json:"argumentsHash"`
}

// Registry owns all ToolDefs. Source updates are transactional: validation is
// performed on a candidate catalog under one lock and only then is revision
// advanced and the candidate published.
type Registry struct {
	mu       sync.RWMutex
	revision uint64
	builtins map[string]ToolDef
	sources  map[ToolSource]map[string]ToolDef
	byID     map[string]ToolDef
	byWire   map[string]string
}

// NewRegistry creates a catalog from immutable builtins. Dynamic callers must
// use ReplaceSource, which prevents replacing a builtin name accidentally.
func NewRegistry(builtins []ToolDef) (*Registry, error) {
	r := &Registry{
		revision: 1,
		builtins: make(map[string]ToolDef, len(builtins)),
		sources:  make(map[ToolSource]map[string]ToolDef),
		byID:     make(map[string]ToolDef, len(builtins)),
		byWire:   make(map[string]string, len(builtins)),
	}
	for _, raw := range builtins {
		if raw.Source != SourceBuiltin {
			return nil, fmt.Errorf("builtin %q has source %q: %w", raw.ID, raw.Source, ErrInvalidToolDef)
		}
		def, err := normalizeToolDef(raw)
		if err != nil {
			return nil, err
		}
		if _, exists := r.byID[def.ID]; exists {
			return nil, fmt.Errorf("tool ID %q: %w", def.ID, ErrDuplicateTool)
		}
		if previous, exists := r.byWire[def.WireName]; exists {
			return nil, fmt.Errorf("wire name %q already belongs to %q: %w", def.WireName, previous, ErrDuplicateTool)
		}
		r.builtins[def.ID] = cloneToolDef(def)
		r.byID[def.ID] = cloneToolDef(def)
		r.byWire[def.WireName] = def.ID
	}
	return r, nil
}

// Snapshot returns a deterministic deep copy sorted by logical ID.
func (r *Registry) Snapshot() Catalog {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.snapshotLocked()
}

func (r *Registry) snapshotLocked() Catalog {
	ids := make([]string, 0, len(r.byID))
	for id := range r.byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	tools := make([]ToolDef, 0, len(ids))
	for _, id := range ids {
		tools = append(tools, cloneToolDef(r.byID[id]))
	}
	return Catalog{Revision: r.revision, Tools: tools}
}

// ReplaceSource atomically replaces every definition owned by source. Builtins
// are immutable and cannot be passed here.
func (r *Registry) ReplaceSource(source ToolSource, defs []ToolDef) (Catalog, error) {
	return r.ReplaceSources(map[ToolSource][]ToolDef{source: defs})
}

// ReplaceSources validates and publishes several dynamic sources as one
// catalog revision. It is used when definitions from MCP, workflows, and
// skills belong to the same refresh snapshot; readers must never observe a
// mixture of old and new sources.
func (r *Registry) ReplaceSources(replacements map[ToolSource][]ToolDef) (Catalog, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	sourceNames := make([]string, 0, len(replacements))
	for source := range replacements {
		sourceNames = append(sourceNames, string(source))
	}
	sort.Strings(sourceNames)

	candidateSources := cloneSources(r.sources)
	changed := false
	for _, sourceName := range sourceNames {
		source := ToolSource(sourceName)
		if source == "" || source == SourceBuiltin || !isKnownToolSource(source) {
			return Catalog{}, fmt.Errorf("source %q cannot be dynamically replaced: %w", source, ErrInvalidToolDef)
		}
		defs := replacements[source]
		candidate := make(map[string]ToolDef, len(defs))
		for _, raw := range defs {
			if raw.Source != source {
				return Catalog{}, fmt.Errorf("tool %q source %q does not match replacement source %q: %w", raw.ID, raw.Source, source, ErrInvalidToolDef)
			}
			def, err := normalizeToolDef(raw)
			if err != nil {
				return Catalog{}, err
			}
			if _, exists := candidate[def.ID]; exists {
				return Catalog{}, fmt.Errorf("tool ID %q appears twice in %q: %w", def.ID, source, ErrDuplicateTool)
			}
			candidate[def.ID] = def
		}
		candidateSources[source] = candidate
		if !toolDefMapsEqual(r.sources[source], candidate) {
			changed = true
		}
	}
	if !changed {
		return r.snapshotLocked(), nil
	}

	byID, byWire, err := rebuildIndex(r.builtins, candidateSources)
	if err != nil {
		return Catalog{}, err
	}
	r.sources = candidateSources
	r.byID = byID
	r.byWire = byWire
	r.revision++
	return r.snapshotLocked(), nil
}

func toolDefMapsEqual(left, right map[string]ToolDef) bool {
	if len(left) != len(right) {
		return false
	}
	for id, leftDef := range left {
		rightDef, ok := right[id]
		if !ok || leftDef.ID != rightDef.ID || leftDef.WireName != rightDef.WireName ||
			leftDef.Description != rightDef.Description || leftDef.Source != rightDef.Source ||
			leftDef.Risk != rightDef.Risk || leftDef.Approval != rightDef.Approval ||
			leftDef.Mutation != rightDef.Mutation || leftDef.ExecuteKey != rightDef.ExecuteKey ||
			!bytes.Equal(leftDef.InputSchema, rightDef.InputSchema) || !stringMapsEqual(leftDef.Metadata, rightDef.Metadata) {
			return false
		}
	}
	return true
}

func stringMapsEqual(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

// ToolByWireName resolves a provider-safe name in exactly one catalog revision.
func (r *Registry) ToolByWireName(revision uint64, wireName string) (ToolDef, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if revision != r.revision {
		return ToolDef{}, false
	}
	id, ok := r.byWire[wireName]
	if !ok {
		return ToolDef{}, false
	}
	return cloneToolDef(r.byID[id]), true
}

// Resolve validates and canonicalizes a logical-ID invocation.
func (r *Registry) Resolve(revision uint64, toolID string, rawArguments json.RawMessage) (Invocation, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.resolveLocked(revision, toolID, rawArguments)
}

// ResolveWire validates and canonicalizes a provider-safe-name invocation.
func (r *Registry) ResolveWire(revision uint64, wireName string, rawArguments json.RawMessage) (Invocation, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if revision != r.revision {
		return Invocation{}, fmt.Errorf("requested %d, current %d: %w", revision, r.revision, ErrStaleCatalog)
	}
	id, ok := r.byWire[wireName]
	if !ok {
		return Invocation{}, fmt.Errorf("wire name %q: %w", wireName, ErrUnknownTool)
	}
	return r.resolveLocked(revision, id, rawArguments)
}

func (r *Registry) resolveLocked(revision uint64, toolID string, rawArguments json.RawMessage) (Invocation, error) {
	if revision != r.revision {
		return Invocation{}, fmt.Errorf("requested %d, current %d: %w", revision, r.revision, ErrStaleCatalog)
	}
	def, ok := r.byID[toolID]
	if !ok {
		return Invocation{}, fmt.Errorf("tool ID %q: %w", toolID, ErrUnknownTool)
	}
	args, err := canonicalArguments(rawArguments)
	if err != nil {
		return Invocation{}, err
	}
	if err := validateArguments(def.InputSchema, args); err != nil {
		return Invocation{}, err
	}
	sum := sha256.Sum256(args)
	return Invocation{
		CatalogRevision: r.revision,
		Tool:            cloneToolDef(def),
		Arguments:       args,
		ArgumentsHash:   hex.EncodeToString(sum[:]),
	}, nil
}

func rebuildIndex(builtins map[string]ToolDef, sources map[ToolSource]map[string]ToolDef) (map[string]ToolDef, map[string]string, error) {
	byID := make(map[string]ToolDef, len(builtins))
	byWire := make(map[string]string, len(builtins))
	add := func(def ToolDef) error {
		if prior, exists := byID[def.ID]; exists {
			return fmt.Errorf("tool ID %q already owned by %q: %w", def.ID, prior.Source, ErrDuplicateTool)
		}
		if prior, exists := byWire[def.WireName]; exists {
			return fmt.Errorf("wire name %q already belongs to %q: %w", def.WireName, prior, ErrDuplicateTool)
		}
		byID[def.ID] = cloneToolDef(def)
		byWire[def.WireName] = def.ID
		return nil
	}
	for _, def := range builtins {
		if err := add(def); err != nil {
			return nil, nil, err
		}
	}
	sourceNames := make([]string, 0, len(sources))
	for source := range sources {
		sourceNames = append(sourceNames, string(source))
	}
	sort.Strings(sourceNames)
	for _, sourceName := range sourceNames {
		defs := sources[ToolSource(sourceName)]
		ids := make([]string, 0, len(defs))
		for id := range defs {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			if err := add(defs[id]); err != nil {
				return nil, nil, err
			}
		}
	}
	return byID, byWire, nil
}

func normalizeToolDef(raw ToolDef) (ToolDef, error) {
	def := cloneToolDef(raw)
	def.ID = strings.TrimSpace(def.ID)
	def.Description = strings.TrimSpace(def.Description)
	def.ExecuteKey = strings.TrimSpace(def.ExecuteKey)
	if def.ID == "" || len(def.ID) > maxToolIDLength || !toolIDPattern.MatchString(def.ID) {
		return ToolDef{}, fmt.Errorf("invalid tool ID %q: %w", raw.ID, ErrInvalidToolDef)
	}
	if def.Description == "" || def.ExecuteKey == "" {
		return ToolDef{}, fmt.Errorf("tool %q needs description and execution key: %w", def.ID, ErrInvalidToolDef)
	}
	if def.Source == "" {
		return ToolDef{}, fmt.Errorf("tool %q has no source: %w", def.ID, ErrInvalidToolDef)
	}
	if !isKnownToolSource(def.Source) {
		return ToolDef{}, fmt.Errorf("tool %q has unknown source %q: %w", def.ID, def.Source, ErrInvalidToolDef)
	}
	if def.Risk != RiskReadOnly && def.Risk != RiskElevated && def.Risk != RiskDangerous {
		return ToolDef{}, fmt.Errorf("tool %q has invalid risk %q: %w", def.ID, def.Risk, ErrInvalidToolDef)
	}
	if def.Approval != ApprovalManual && def.Approval != ApprovalBackendPolicy {
		return ToolDef{}, fmt.Errorf("tool %q has invalid approval %q: %w", def.ID, def.Approval, ErrInvalidToolDef)
	}
	if def.Mutation != MutationNone && def.Mutation != MutationWorkspaceTransaction && def.Mutation != MutationExternal {
		return ToolDef{}, fmt.Errorf("tool %q has invalid mutation %q: %w", def.ID, def.Mutation, ErrInvalidToolDef)
	}
	if def.Approval == ApprovalBackendPolicy && (def.Risk != RiskReadOnly || def.Mutation != MutationNone) {
		return ToolDef{}, fmt.Errorf("tool %q may use backend-policy approval only when read-only and non-mutating: %w", def.ID, ErrInvalidToolDef)
	}
	if def.Risk == RiskReadOnly && def.Mutation != MutationNone {
		return ToolDef{}, fmt.Errorf("tool %q declares mutation %q with read-only risk: %w", def.ID, def.Mutation, ErrInvalidToolDef)
	}
	if def.WireName == "" {
		def.WireName = providerSafeName(def.ID)
	}
	if !providerToolNamePattern.MatchString(def.WireName) {
		return ToolDef{}, fmt.Errorf("tool %q has invalid provider wire name %q: %w", def.ID, def.WireName, ErrInvalidToolDef)
	}
	if _, err := compileSchema(def.InputSchema); err != nil {
		return ToolDef{}, fmt.Errorf("tool %q: %w", def.ID, err)
	}
	return def, nil
}

func isKnownToolSource(source ToolSource) bool {
	switch source {
	case SourceBuiltin, SourceMCP, SourceWorkflow, SourceSkill, SourceComputerUse:
		return true
	default:
		return false
	}
}

func providerSafeName(id string) string {
	if providerToolNamePattern.MatchString(id) {
		return id
	}
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	prefix := strings.Trim(b.String(), "_")
	if prefix == "" {
		prefix = "tool"
	}
	sum := sha256.Sum256([]byte(id))
	// Base32's alphabet is provider-safe. Eight bytes gives a compact suffix
	// while the registry still detects the astronomically unlikely collision.
	suffix := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:8]))
	maxPrefix := maxProviderToolNameLength - len(suffix) - 1
	if len(prefix) > maxPrefix {
		prefix = prefix[:maxPrefix]
	}
	return prefix + "_" + suffix
}

func cloneToolDef(def ToolDef) ToolDef {
	copy := def
	copy.InputSchema = append(json.RawMessage(nil), def.InputSchema...)
	if def.Metadata != nil {
		copy.Metadata = make(map[string]string, len(def.Metadata))
		for k, v := range def.Metadata {
			copy.Metadata[k] = v
		}
	}
	return copy
}

func cloneSources(sources map[ToolSource]map[string]ToolDef) map[ToolSource]map[string]ToolDef {
	clone := make(map[ToolSource]map[string]ToolDef, len(sources))
	for source, defs := range sources {
		copiedDefs := make(map[string]ToolDef, len(defs))
		for id, def := range defs {
			copiedDefs[id] = cloneToolDef(def)
		}
		clone[source] = copiedDefs
	}
	return clone
}
