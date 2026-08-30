package services

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"
)

const (
	WorkspaceURIScheme = "koyori-workspace"
	LocalHostID        = "local"
)

var (
	ErrInvalidWorkspaceURI   = errors.New("invalid workspace URI")
	ErrStaleWorkspaceScope   = errors.New("stale workspace scope")
	ErrUnsupportedWorkspace  = errors.New("unsupported workspace")
	ErrDisconnectedWorkspace = errors.New("workspace disconnected")
)

// WorkspaceURI is the transport-neutral identity of a workspace resource.
// HostID is an opaque, case-sensitive application identity, not a DNS host
// name. It must be preserved byte-for-byte and must never be lower-cased.
type WorkspaceURI struct {
	HostID      string
	WorkspaceID string
	relative    string
}

func NewWorkspaceURI(hostID, workspaceID, relativePath string) (WorkspaceURI, error) {
	// HostID is intentionally validated as an opaque application identifier;
	// URL host parsing must not apply DNS case-folding to it.
	if err := validateIdentity(hostID, "host ID", false); err != nil {
		return WorkspaceURI{}, err
	}
	if err := validateIdentity(workspaceID, "workspace ID", false); err != nil {
		return WorkspaceURI{}, err
	}
	if err := validateRelativePath(relativePath); err != nil {
		return WorkspaceURI{}, err
	}
	return WorkspaceURI{HostID: hostID, WorkspaceID: workspaceID, relative: relativePath}, nil
}

func NewLocalWorkspaceURI(workspaceID, relativePath string) (WorkspaceURI, error) {
	return NewWorkspaceURI(LocalHostID, workspaceID, relativePath)
}

func ParseWorkspaceURI(raw string) (WorkspaceURI, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return WorkspaceURI{}, invalidURI(err)
	}
	if u.Scheme != WorkspaceURIScheme || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Host == "" {
		return WorkspaceURI{}, invalidURI(errors.New("unsupported or non-canonical URI form"))
	}
	if u.Host != u.Hostname() || strings.Contains(u.Host, ":") {
		return WorkspaceURI{}, invalidURI(errors.New("host must not contain a port or colon"))
	}
	if err := validateIdentity(u.Host, "host ID", false); err != nil {
		return WorkspaceURI{}, err
	}
	escapedPath := u.EscapedPath()
	if !strings.HasPrefix(escapedPath, "/") {
		return WorkspaceURI{}, invalidURI(errors.New("path must be absolute"))
	}
	if strings.HasSuffix(escapedPath, "/") {
		return WorkspaceURI{}, invalidURI(errors.New("path contains an empty segment"))
	}
	escapedParts := strings.Split(strings.TrimPrefix(escapedPath, "/"), "/")
	if len(escapedParts) == 0 || escapedParts[0] == "" {
		return WorkspaceURI{}, invalidURI(errors.New("workspace ID is required"))
	}
	parts := make([]string, len(escapedParts))
	for i, escapedPart := range escapedParts {
		part, err := url.PathUnescape(escapedPart)
		if err != nil {
			return WorkspaceURI{}, invalidURI(errors.New("path contains invalid percent-encoding"))
		}
		if strings.ContainsAny(part, `/\`) {
			return WorkspaceURI{}, invalidURI(errors.New("encoded path separator is not allowed"))
		}
		parts[i] = part
	}
	if err := validateIdentity(parts[0], "workspace ID", false); err != nil {
		return WorkspaceURI{}, err
	}
	relative := strings.Join(parts[1:], "/")
	if err := validateRelativePath(relative); err != nil {
		return WorkspaceURI{}, err
	}
	workspaceURI := WorkspaceURI{HostID: u.Host, WorkspaceID: parts[0], relative: relative}
	if raw != workspaceURI.String() {
		return WorkspaceURI{}, invalidURI(errors.New("URI is not canonical"))
	}
	return workspaceURI, nil
}

func (u WorkspaceURI) RelativePath() string { return u.relative }

func (u WorkspaceURI) String() string {
	if u.HostID == "" || u.WorkspaceID == "" {
		return ""
	}
	path := "/" + u.WorkspaceID
	if u.relative != "" {
		parts := strings.Split(u.relative, "/")
		for i := range parts {
			parts[i] = url.PathEscape(parts[i])
		}
		path += "/" + strings.Join(parts, "/")
	}
	return WorkspaceURIScheme + "://" + u.HostID + path
}

type WorkspaceRef struct {
	HostID            string
	WorkspaceID       string
	Generation        uint64
	HostInstanceNonce string
	URI               WorkspaceURI
}

type WorkspaceScope struct {
	HostID            string
	WorkspaceID       string
	Generation        uint64
	HostInstanceNonce string
	URI               WorkspaceURI
}

func NewWorkspaceRef(hostID, workspaceID string, generation uint64, nonce string) (WorkspaceRef, error) {
	uri, err := NewWorkspaceURI(hostID, workspaceID, "")
	if err != nil {
		return WorkspaceRef{}, err
	}
	ref := WorkspaceRef{hostID, workspaceID, generation, nonce, uri}
	if err := ref.Validate(); err != nil {
		return WorkspaceRef{}, err
	}
	return ref, nil
}

func NewLocalWorkspaceRef(workspaceID string, generation uint64, nonce string) (WorkspaceRef, error) {
	return NewWorkspaceRef(LocalHostID, workspaceID, generation, nonce)
}

func (r WorkspaceRef) Scope() WorkspaceScope {
	return WorkspaceScope(r)
}

func (r WorkspaceRef) Validate() error {
	return validateScopeFields(r.HostID, r.WorkspaceID, r.Generation, r.HostInstanceNonce, r.URI)
}
func (s WorkspaceScope) Validate() error {
	return validateScopeFields(s.HostID, s.WorkspaceID, s.Generation, s.HostInstanceNonce, s.URI)
}
func (s WorkspaceScope) Equal(other WorkspaceScope) bool {
	return s.HostID == other.HostID && s.WorkspaceID == other.WorkspaceID && s.Generation == other.Generation && s.HostInstanceNonce == other.HostInstanceNonce && s.URI.String() == other.URI.String()
}

func (r WorkspaceRef) MatchesScope(s WorkspaceScope) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if err := s.Validate(); err != nil {
		return err
	}
	if r.Scope().Equal(s) {
		return nil
	}
	return fmt.Errorf("workspace identity or lease changed: %w", ErrStaleWorkspaceScope)
}

func validateScopeFields(hostID, workspaceID string, generation uint64, nonce string, uri WorkspaceURI) error {
	if err := validateIdentity(hostID, "host ID", false); err != nil {
		return err
	}
	if err := validateIdentity(workspaceID, "workspace ID", false); err != nil {
		return err
	}
	if err := validateNonce(nonce); err != nil {
		return err
	}
	if generation == 0 {
		return fmt.Errorf("generation must be positive: %w", ErrInvalidWorkspaceURI)
	}
	if uri.HostID != hostID || uri.WorkspaceID != workspaceID {
		return fmt.Errorf("URI does not match scope: %w", ErrStaleWorkspaceScope)
	}
	if uri.RelativePath() != "" {
		return fmt.Errorf("scope URI must identify the workspace root: %w", ErrInvalidWorkspaceURI)
	}
	parsed, err := ParseWorkspaceURI(uri.String())
	if err != nil || parsed.String() != uri.String() {
		return fmt.Errorf("scope URI is invalid: %w", ErrInvalidWorkspaceURI)
	}
	return nil
}

func validateIdentity(value, label string, nonce bool) error {
	if value == "" {
		return fmt.Errorf("%s is required: %w", label, ErrInvalidWorkspaceURI)
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) || r == '/' || r == ':' || (nonce && r == '\\') {
			return fmt.Errorf("invalid %s: %w", label, ErrInvalidWorkspaceURI)
		}
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("-._~", r)) {
			return fmt.Errorf("invalid %s character: %w", label, ErrInvalidWorkspaceURI)
		}
	}
	if value == "." || value == ".." {
		return fmt.Errorf("invalid %s dot segment: %w", label, ErrInvalidWorkspaceURI)
	}
	return nil
}

func validateNonce(value string) error {
	return validateIdentity(value, "host instance nonce", false)
}

func validateRelativePath(path string) error {
	if path == "" {
		return nil
	}
	if strings.HasPrefix(path, "/") || strings.HasPrefix(path, `\`) || isWindowsDrivePath(path) {
		return invalidURI(errors.New("relative path must not be absolute"))
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." {
			return invalidURI(errors.New("path contains an empty or dot segment"))
		}
		for _, r := range part {
			if r == '\\' || r == 0 || unicode.IsControl(r) {
				return invalidURI(errors.New("path segment contains an unsafe character"))
			}
		}
	}
	return nil
}

func isWindowsDrivePath(path string) bool {
	return len(path) >= 2 && ((path[0] >= 'a' && path[0] <= 'z') || (path[0] >= 'A' && path[0] <= 'Z')) && path[1] == ':'
}

func invalidURI(err error) error { return fmt.Errorf("%w: %v", ErrInvalidWorkspaceURI, err) }
