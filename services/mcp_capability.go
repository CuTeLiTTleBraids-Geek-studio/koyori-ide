package services

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync/atomic"
	"time"
)

// P1-03-A: the real MCP capability model.
//
// The client used to send an empty capabilities object and discard the
// initialize response. These types make the 2024-11-05 initialize exchange
// explicit and fail-closed: the client declares only capabilities it really
// implements, and the server response is parsed, validated, and kept as a
// snapshot bound to exactly one client run.

// mcpRunCounter issues globally monotonic client run identities so every
// successful initialize handshake produces a snapshot that can never be
// confused with an older run of the same or another client.
var mcpRunCounter atomic.Uint64

// mcpContentByteLimit bounds any single resource text or prompt message the
// client accepts from a server. Server content is untrusted input; oversized
// payloads fail closed instead of being truncated silently. The limit is
// deliberately below the HTTP transport's 1MiB read bound so an oversized
// payload reaches this validation with its envelope intact.
const mcpContentByteLimit = 512 << 10

// MCPResourceContent is one validated resource content block returned by
// resources/read. Only text content is supported; other types are rejected
// with an explicit unsupported error.
type MCPResourceContent struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text"`
}

// MCPPromptMessage preserves the role and content provenance of a prompt
// template message. Prompt content is untrusted context and must not be
// promoted to system authority by the caller.
type MCPPromptMessage struct {
	Role    string     `json:"role"`
	Content MCPContent `json:"content"`
}

// validateMCPResourceContents parses and validates a resources/read result.
// Malformed JSON, an empty contents array, a missing URI, an unknown content
// type, or an oversized text all return stable errors; none can masquerade
// as an empty success.
func validateMCPResourceContents(raw json.RawMessage) ([]MCPResourceContent, error) {
	var result struct {
		Contents []struct {
			URI      string `json:"uri"`
			MimeType string `json:"mimeType"`
			Type     string `json:"type"`
			Text     string `json:"text"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("malformed resource contents: %w", ErrInvalidInput)
	}
	if len(result.Contents) == 0 {
		return nil, fmt.Errorf("resource read returned no contents: %w", ErrNotFound)
	}
	contents := make([]MCPResourceContent, 0, len(result.Contents))
	for _, entry := range result.Contents {
		if entry.URI == "" {
			return nil, fmt.Errorf("resource content is missing its URI: %w", ErrInvalidInput)
		}
		if entry.Type != "" && entry.Type != "text" {
			return nil, fmt.Errorf("resource content type %q is not supported: %w", entry.Type, ErrNotAllowed)
		}
		if entry.Type == "" && entry.Text == "" {
			return nil, fmt.Errorf("resource content %q has no text and no type: %w", entry.URI, ErrInvalidInput)
		}
		if len(entry.Text) > mcpContentByteLimit {
			return nil, fmt.Errorf("resource content %q exceeds %d bytes: %w", entry.URI, mcpContentByteLimit, ErrInvalidInput)
		}
		contents = append(contents, MCPResourceContent{URI: entry.URI, MimeType: entry.MimeType, Text: entry.Text})
	}
	return contents, nil
}

// validateMCPPromptMessages parses and validates a prompts/get result,
// preserving role/content provenance. Unknown roles and oversized text fail
// closed.
func validateMCPPromptMessages(raw json.RawMessage) ([]MCPPromptMessage, error) {
	var result struct {
		Messages []MCPPromptMessage `json:"messages"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("malformed prompt messages: %w", ErrInvalidInput)
	}
	if len(result.Messages) == 0 {
		return nil, fmt.Errorf("prompt returned no messages: %w", ErrNotFound)
	}
	for _, message := range result.Messages {
		if message.Role != "user" && message.Role != "assistant" {
			return nil, fmt.Errorf("prompt message has unsupported role %q: %w", message.Role, ErrNotAllowed)
		}
		if message.Content.Type != "text" {
			return nil, fmt.Errorf("prompt message content type %q is not supported: %w", message.Content.Type, ErrNotAllowed)
		}
		if len(message.Content.Text) > mcpContentByteLimit {
			return nil, fmt.Errorf("prompt message exceeds %d bytes: %w", mcpContentByteLimit, ErrInvalidInput)
		}
	}
	return result.Messages, nil
}

// mcpProtocolVersion is the only MCP protocol version this client speaks.
// A server answering with any other version fails the handshake.
const mcpProtocolVersion = "2024-11-05"

// mcpClientIdentity is the clientInfo advertised during initialize.
type mcpClientIdentity struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// mcpRootsClientCapability declares the roots/list server request handler.
// This client implements a controlled roots/list response that returns only
// the workspace root the connection was opened under, so the declaration is
// backed by a real handler.
type mcpRootsClientCapability struct {
	// ListChanged stays false: this client's connections do not survive a
	// workspace switch (the backend detaches them), so a change notification
	// can never fire.
	ListChanged bool `json:"listChanged,omitempty"`
}

// mcpClientCapabilities is the client's own capability declaration. Every
// field must map to an implemented handler; unimplemented features stay nil.
// sampling is intentionally absent: this client never answers
// sampling/createMessage server requests.
type mcpClientCapabilities struct {
	Roots *mcpRootsClientCapability `json:"roots,omitempty"`
}

// mcpInitializeRequestParams is the typed initialize request payload.
type mcpInitializeRequestParams struct {
	ProtocolVersion string                `json:"protocolVersion"`
	Capabilities    mcpClientCapabilities `json:"capabilities"`
	ClientInfo      mcpClientIdentity     `json:"clientInfo"`
}

// clientMCPInitializeParams is the single source of truth for what this
// client declares about itself. Roots are declared because the client really
// answers the roots/list server request with the connection's committed
// workspace root (P1-03-H).
func clientMCPInitializeParams() mcpInitializeRequestParams {
	return mcpInitializeRequestParams{
		ProtocolVersion: mcpProtocolVersion,
		Capabilities: mcpClientCapabilities{
			Roots: &mcpRootsClientCapability{},
		},
		ClientInfo: mcpClientIdentity{
			Name:    "koyori-ide",
			Version: "1.0",
		},
	}
}

// MCPCapabilityState is how a capability from the initialize exchange was
// resolved for this client.
type MCPCapabilityState string

const (
	// MCPCapabilitySupported: the client implements the feature and the
	// server declared it, so the corresponding API may be used.
	MCPCapabilitySupported MCPCapabilityState = "supported"
	// MCPCapabilityMissing: the server did not declare the feature. The
	// corresponding API fails closed with an explicit error instead of
	// silently succeeding.
	MCPCapabilityMissing MCPCapabilityState = "missing"
	// MCPCapabilityUnsupported: the client does not implement the feature.
	// Server-to-client features (sampling, elicitation, logging) are always
	// unsupported here, whether or not the server declared them.
	MCPCapabilityUnsupported MCPCapabilityState = "unsupported"
	// MCPCapabilityUnknown: the server sent a capability key this client
	// does not recognize. It is recorded verbatim, never silently dropped.
	MCPCapabilityUnknown MCPCapabilityState = "unknown"
)

// MCPCapabilityFeature is one capability of the initialize exchange.
type MCPCapabilityFeature struct {
	State MCPCapabilityState `json:"state"`
	// Declared reports whether the server announced the feature.
	Declared bool `json:"declared"`
	// ListChanged mirrors a tools/prompts/resources listChanged declaration.
	ListChanged bool `json:"listChanged,omitempty"`
	// Subscribe mirrors the resources subscribe declaration. This client
	// does not implement resources/subscribe; the flag is diagnostic only.
	Subscribe bool `json:"subscribe,omitempty"`
}

// MCPCapabilityReport is the parsed server capability object plus explicit
// records for known-but-unimplemented features.
type MCPCapabilityReport struct {
	Tools       MCPCapabilityFeature `json:"tools"`
	Resources   MCPCapabilityFeature `json:"resources"`
	Prompts     MCPCapabilityFeature `json:"prompts"`
	Sampling    MCPCapabilityFeature `json:"sampling"`
	Elicitation MCPCapabilityFeature `json:"elicitation"`
	Logging     MCPCapabilityFeature `json:"logging"`
	// Unknown lists capability keys this client does not recognize.
	Unknown []string `json:"unknown,omitempty"`
}

// feature returns the report entry for a client API family ("tools",
// "resources", or "prompts").
func (r MCPCapabilityReport) feature(family string) MCPCapabilityFeature {
	switch family {
	case "tools":
		return r.Tools
	case "resources":
		return r.Resources
	case "prompts":
		return r.Prompts
	}
	return MCPCapabilityFeature{State: MCPCapabilityUnknown}
}

// MCPServerIdentity is the validated serverInfo from the initialize result.
type MCPServerIdentity struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// MCPCapabilitySnapshot is the validated initialize result. It belongs to
// exactly one client run: StopServer, reconnect, config mutation, and
// workspace switches invalidate it.
type MCPCapabilitySnapshot struct {
	ProtocolVersion string              `json:"protocolVersion"`
	ServerInfo      MCPServerIdentity   `json:"serverInfo"`
	Instructions    string              `json:"instructions,omitempty"`
	Capabilities    MCPCapabilityReport `json:"capabilities"`

	// ServerName is the MCP server config identity this run was started for.
	ServerName string `json:"serverName"`
	// WorkspaceRoot, RootGeneration, and LifecycleGeneration are stamped by
	// MCPService when the client is installed; they fail the snapshot closed
	// against the workspace and lifecycle state it was established under.
	WorkspaceRoot       string `json:"workspaceRoot,omitempty"`
	RootGeneration      uint64 `json:"rootGeneration"`
	LifecycleGeneration uint64 `json:"lifecycleGeneration"`
	// Run identifies the client run globally and monotonically, so a
	// reconnect always produces a different snapshot identity.
	Run uint64 `json:"run"`
	// EstablishedAt is when this run's initialize response was validated.
	EstablishedAt time.Time `json:"establishedAt"`
}

// clone returns a deep copy so callers cannot mutate shared report state.
func (s MCPCapabilitySnapshot) clone() MCPCapabilitySnapshot {
	copied := s
	copied.Capabilities.Unknown = append([]string(nil), s.Capabilities.Unknown...)
	return copied
}

// mcpInitializeResult is the subset of the InitializeResult this client
// validates. Capabilities stays raw so unrecognized keys can be recorded.
type mcpInitializeResult struct {
	ProtocolVersion string          `json:"protocolVersion"`
	Capabilities    json.RawMessage `json:"capabilities"`
	ServerInfo      struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"serverInfo"`
	Instructions string `json:"instructions"`
}

// parseMCPInitializeResult validates the initialize result and returns the
// protocol-level snapshot. A missing capabilities object is honest empty
// state (every feature stays undeclared); a malformed capabilities object, a
// missing or mismatched protocolVersion, or a missing serverInfo fail closed.
func parseMCPInitializeResult(raw json.RawMessage) (*MCPCapabilitySnapshot, error) {
	var result mcpInitializeResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("malformed initialize result: %w", ErrInvalidInput)
	}
	if result.ProtocolVersion == "" {
		return nil, fmt.Errorf("initialize result is missing protocolVersion: %w", ErrInvalidInput)
	}
	if result.ProtocolVersion != mcpProtocolVersion {
		return nil, fmt.Errorf("server protocol version %q is not supported (client supports %q): %w", result.ProtocolVersion, mcpProtocolVersion, ErrNotAllowed)
	}
	if result.ServerInfo.Name == "" {
		return nil, fmt.Errorf("initialize result is missing serverInfo.name: %w", ErrInvalidInput)
	}
	if result.ServerInfo.Version == "" {
		return nil, fmt.Errorf("initialize result is missing serverInfo.version: %w", ErrInvalidInput)
	}
	report, err := parseMCPCapabilityReport(result.Capabilities)
	if err != nil {
		return nil, err
	}
	return &MCPCapabilitySnapshot{
		ProtocolVersion: result.ProtocolVersion,
		ServerInfo: MCPServerIdentity{
			Name:    result.ServerInfo.Name,
			Version: result.ServerInfo.Version,
		},
		Instructions: result.Instructions,
		Capabilities: report,
	}, nil
}

// parseMCPCapabilityReport resolves the server capability object. An absent
// key means the feature was not declared; an unrecognized key is recorded as
// unknown; tools/resources/prompts declarations must be well-formed objects
// because they gate real client APIs.
func parseMCPCapabilityReport(raw json.RawMessage) (MCPCapabilityReport, error) {
	report := MCPCapabilityReport{
		Tools:       MCPCapabilityFeature{State: MCPCapabilityMissing},
		Resources:   MCPCapabilityFeature{State: MCPCapabilityMissing},
		Prompts:     MCPCapabilityFeature{State: MCPCapabilityMissing},
		Sampling:    MCPCapabilityFeature{State: MCPCapabilityUnsupported},
		Elicitation: MCPCapabilityFeature{State: MCPCapabilityUnsupported},
		Logging:     MCPCapabilityFeature{State: MCPCapabilityUnsupported},
	}
	if len(raw) == 0 || string(raw) == "null" {
		// An absent capabilities object declares nothing; every feature stays
		// missing/unsupported and the corresponding APIs fail closed.
		return report, nil
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return report, fmt.Errorf("malformed initialize capabilities: %w", ErrInvalidInput)
	}
	for key, value := range entries {
		switch key {
		case "tools":
			feature, err := parseMCPListedCapability(value)
			if err != nil {
				return report, fmt.Errorf("malformed tools capability: %w", err)
			}
			report.Tools = feature
		case "resources":
			feature, err := parseMCPResourceCapability(value)
			if err != nil {
				return report, fmt.Errorf("malformed resources capability: %w", err)
			}
			report.Resources = feature
		case "prompts":
			feature, err := parseMCPListedCapability(value)
			if err != nil {
				return report, fmt.Errorf("malformed prompts capability: %w", err)
			}
			report.Prompts = feature
		case "sampling", "elicitation", "logging":
			// Known server-to-client features this client does not implement.
			// Their declaration is recorded for diagnostics only; the state
			// stays unsupported and no client API is ever gated on them.
			switch key {
			case "sampling":
				report.Sampling.Declared = true
			case "elicitation":
				report.Elicitation.Declared = true
			case "logging":
				report.Logging.Declared = true
			}
		default:
			report.Unknown = append(report.Unknown, key)
		}
	}
	sort.Strings(report.Unknown)
	return report, nil
}

// parseMCPListedCapability parses a tools/prompts capability object. A null
// declaration is treated as absent; anything that is not an object fails.
func parseMCPListedCapability(raw json.RawMessage) (MCPCapabilityFeature, error) {
	feature := MCPCapabilityFeature{State: MCPCapabilityMissing}
	if len(raw) == 0 || string(raw) == "null" {
		return feature, nil
	}
	var declared struct {
		ListChanged bool `json:"listChanged"`
	}
	if err := json.Unmarshal(raw, &declared); err != nil {
		return feature, fmt.Errorf("%w", ErrInvalidInput)
	}
	feature.State = MCPCapabilitySupported
	feature.Declared = true
	feature.ListChanged = declared.ListChanged
	return feature, nil
}

// parseMCPResourceCapability parses the resources capability object,
// including the subscribe sub-declaration this client does not implement.
func parseMCPResourceCapability(raw json.RawMessage) (MCPCapabilityFeature, error) {
	feature := MCPCapabilityFeature{State: MCPCapabilityMissing}
	if len(raw) == 0 || string(raw) == "null" {
		return feature, nil
	}
	var declared struct {
		Subscribe   bool `json:"subscribe"`
		ListChanged bool `json:"listChanged"`
	}
	if err := json.Unmarshal(raw, &declared); err != nil {
		return feature, fmt.Errorf("%w", ErrInvalidInput)
	}
	feature.State = MCPCapabilitySupported
	feature.Declared = true
	feature.Subscribe = declared.Subscribe
	feature.ListChanged = declared.ListChanged
	return feature, nil
}
