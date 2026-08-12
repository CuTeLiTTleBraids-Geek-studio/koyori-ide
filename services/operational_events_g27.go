package services

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	OperationalSchemaVersionV1 = "v1"

	OperationalEventSessionStarted      = "session.started"
	OperationalEventAppReady            = "app.ready"
	OperationalEventSessionEnded        = "session.ended"
	OperationalEventCrashMarker         = "crash.marker"
	OperationalEventWorkspaceEdit       = "workspace.edit"
	OperationalEventRecovery            = "recovery"
	OperationalEventLSPRequestLatency   = "lsp.request.latency"
	OperationalEventExtensionCrash      = "extension.crash"
	OperationalEventUpdateCheck         = "update.check"
	OperationalEventUpdateDownload      = "update.download"
	OperationalEventUpdateHash          = "update.hash"
	OperationalEventUpdateManualInstall = "update.manual-install"

	OperationalOutcomeSucceeded   = "succeeded"
	OperationalOutcomeFailed      = "failed"
	OperationalOutcomeCancelled   = "cancelled"
	OperationalOutcomeSkipped     = "skipped"
	OperationalOutcomeUnavailable = "unavailable"
	OperationalOutcomeRecovered   = "recovered"
	OperationalOutcomeNotNeeded   = "not_needed"
	OperationalOutcomeVerified    = "verified"
	OperationalOutcomeMismatch    = "mismatch"

	OperationalCategoryCompletion  = "completion"
	OperationalCategoryHover       = "hover"
	OperationalCategoryDefinition  = "definition"
	OperationalCategoryReferences  = "references"
	OperationalCategorySymbols     = "symbols"
	OperationalCategoryDiagnostics = "diagnostics"
	OperationalCategoryFormatting  = "formatting"
	OperationalCategoryRename      = "rename"
	OperationalCategoryCodeAction  = "code_action"
	OperationalCategoryOther       = "other"

	OperationalDurationInstant  = "instant"
	OperationalDurationFast     = "fast"
	OperationalDurationModerate = "moderate"
	OperationalDurationSlow     = "slow"
	OperationalDurationVerySlow = "very_slow"
	OperationalDurationUnknown  = "unknown"

	DefaultOperationalBufferMaxEvents = 1000
	DefaultOperationalBufferMaxBytes  = 1 << 20
	operationalMaxStringBytes         = 256
)

var (
	errOperationalDisabled = errors.New("operational events are disabled")
	privacyURI             = regexp.MustCompile(`(?i)(?:file://|[a-z][a-z0-9+.-]*://)`)
	privacyWindowsPath     = regexp.MustCompile(`(?i)(?:^|\s)(?:[a-z]:[\\/]|\\\\)`)
	privacyMAC             = regexp.MustCompile(`(?i)(?:^|[^0-9a-f])(?:[0-9a-f]{2}[:-]){5}[0-9a-f]{2}(?:$|[^0-9a-f])`)
	privacyHostname        = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+(?:local|lan|internal|home|corp|com|net|org)(?:$|[^a-z0-9])`)
	privacySecret          = regexp.MustCompile(`(?i)(?:password|passwd|secret|credential|authorization|bearer|api[_-]?key|access[_-]?token|refresh[_-]?token|private[_-]?key)\s*[:=]`)
	privacyContent         = regexp.MustCompile(`(?i)(?:^|[^a-z])(command|prompt|content)\s*[:=]`)
	safeRelease            = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-(?:0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*))*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)
	safeCommit             = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)
	safePlatform           = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	privacyIPv4            = regexp.MustCompile(`(?:^|[^0-9])(?:[0-9]{1,3}\.){3}[0-9]{1,3}(?::[0-9]{1,5})?(?:$|[^0-9])`)
)

// OperationalEvent is the closed, local-only G27 v1 event schema. It has no
// metadata or arbitrary payload field by design.
type OperationalEvent struct {
	SchemaVersion  string    `json:"schemaVersion"`
	Event          string    `json:"event"`
	Timestamp      time.Time `json:"timestamp"`
	ReleaseVersion string    `json:"releaseVersion"`
	Commit         string    `json:"commit"`
	Channel        string    `json:"channel"`
	OS             string    `json:"os"`
	Arch           string    `json:"arch"`
	SessionID      string    `json:"sessionId"`
	Outcome        string    `json:"outcome,omitempty"`
	Category       string    `json:"category,omitempty"`
	DurationBucket string    `json:"durationBucket,omitempty"`
}

// UnmarshalJSON rejects unknown fields before accepting an event.
func (e *OperationalEvent) UnmarshalJSON(data []byte) error {
	type wire OperationalEvent
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var decoded wire
	if err := dec.Decode(&decoded); err != nil {
		return err
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("multiple JSON values are not allowed")
	}
	*e = OperationalEvent(decoded)
	return e.Validate()
}

// NewOperationalSessionID returns a random opaque identifier with no machine,
// account, or path input.
func NewOperationalSessionID() (string, error) {
	raw := make([]byte, 18)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate operational session ID: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func (e OperationalEvent) Validate() error {
	if e.SchemaVersion != OperationalSchemaVersionV1 {
		return fmt.Errorf("unknown schema version %q", e.SchemaVersion)
	}
	if e.Timestamp.IsZero() {
		return errors.New("timestamp is required")
	}
	if !safeRelease.MatchString(e.ReleaseVersion) {
		return errors.New("invalid releaseVersion")
	}
	if !safeCommit.MatchString(e.Commit) {
		return errors.New("invalid commit")
	}
	if !safePlatform.MatchString(e.Channel) || !safePlatform.MatchString(e.OS) || !safePlatform.MatchString(e.Arch) {
		return errors.New("invalid channel, os, or arch")
	}
	if !validSessionID(e.SessionID) {
		return errors.New("invalid sessionId")
	}
	for _, value := range []string{e.SchemaVersion, e.Event, e.ReleaseVersion, e.Commit, e.Channel, e.OS, e.Arch, e.SessionID, e.Outcome, e.Category, e.DurationBucket} {
		if err := rejectPrivateString(value); err != nil {
			return err
		}
	}

	switch e.Event {
	case OperationalEventSessionStarted, OperationalEventAppReady, OperationalEventCrashMarker, OperationalEventExtensionCrash:
		return requireEventFields(e, false, false, false)
	case OperationalEventSessionEnded:
		if err := requireEventFields(e, true, false, false); err != nil {
			return err
		}
		return requireAllowed("outcome", e.Outcome, OperationalOutcomeSucceeded, OperationalOutcomeFailed)
	case OperationalEventWorkspaceEdit, OperationalEventUpdateCheck, OperationalEventUpdateDownload, OperationalEventUpdateManualInstall:
		if err := requireEventFields(e, true, false, false); err != nil {
			return err
		}
		return requireAllowed("outcome", e.Outcome, OperationalOutcomeSucceeded, OperationalOutcomeFailed, OperationalOutcomeCancelled, OperationalOutcomeSkipped, OperationalOutcomeUnavailable)
	case OperationalEventRecovery:
		if err := requireEventFields(e, true, false, false); err != nil {
			return err
		}
		return requireAllowed("outcome", e.Outcome, OperationalOutcomeRecovered, OperationalOutcomeFailed, OperationalOutcomeNotNeeded)
	case OperationalEventUpdateHash:
		if err := requireEventFields(e, true, false, false); err != nil {
			return err
		}
		return requireAllowed("outcome", e.Outcome, OperationalOutcomeVerified, OperationalOutcomeMismatch, OperationalOutcomeFailed, OperationalOutcomeSkipped)
	case OperationalEventLSPRequestLatency:
		if err := requireEventFields(e, false, true, true); err != nil {
			return err
		}
		if err := requireAllowed("category", e.Category, OperationalCategoryCompletion, OperationalCategoryHover, OperationalCategoryDefinition, OperationalCategoryReferences, OperationalCategorySymbols, OperationalCategoryDiagnostics, OperationalCategoryFormatting, OperationalCategoryRename, OperationalCategoryCodeAction, OperationalCategoryOther); err != nil {
			return err
		}
		return requireAllowed("durationBucket", e.DurationBucket, OperationalDurationInstant, OperationalDurationFast, OperationalDurationModerate, OperationalDurationSlow, OperationalDurationVerySlow, OperationalDurationUnknown)
	default:
		return fmt.Errorf("unknown event %q", e.Event)
	}
}

func requireEventFields(e OperationalEvent, outcome, category, duration bool) error {
	if (e.Outcome != "") != outcome || (e.Category != "") != category || (e.DurationBucket != "") != duration {
		return errors.New("event has missing or inapplicable outcome, category, or durationBucket")
	}
	return nil
}

func requireAllowed(field, value string, allowed ...string) error {
	for _, candidate := range allowed {
		if value == candidate {
			return nil
		}
	}
	return fmt.Errorf("unknown %s %q", field, value)
}

func validSessionID(id string) bool {
	if len(id) != 24 {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(id)
	return err == nil && len(raw) == 18
}

func rejectPrivateString(value string) error {
	if len(value) > operationalMaxStringBytes {
		return errors.New("string exceeds privacy length limit")
	}
	if value == "" {
		return nil
	}
	if strings.ContainsAny(value, `/\`) || strings.HasPrefix(value, "~/") || privacyWindowsPath.MatchString(value) || privacyURI.MatchString(value) {
		return errors.New("path or URI is not allowed")
	}
	if privacyMAC.MatchString(value) || privacyHostname.MatchString(value) || privacyIPv4.MatchString(value) || containsIPAddress(value) {
		return errors.New("host fingerprint is not allowed")
	}
	if privacySecret.MatchString(value) || privacyContent.MatchString(value) {
		return errors.New("content or credential-like value is not allowed")
	}
	for _, token := range strings.FieldsFunc(value, func(r rune) bool {
		return !(r == '.' || r == ':' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F'))
	}) {
		if net.ParseIP(strings.Trim(token, "[]")) != nil {
			return errors.New("IP address is not allowed")
		}
	}
	return nil
}

func containsIPAddress(value string) bool {
	for _, field := range strings.Fields(value) {
		candidate := strings.Trim(field, `,;(){}<>"'`)
		if host, _, err := net.SplitHostPort(candidate); err == nil {
			candidate = strings.Trim(host, "[]")
		} else {
			candidate = strings.Trim(candidate, "[]")
		}
		if net.ParseIP(candidate) != nil {
			return true
		}
	}
	return false
}

// OperationalBuffer is an in-memory, bounded, local-only event store. Its
// zero value is disabled and ready to use.
type OperationalBuffer struct {
	mu        sync.RWMutex
	enabled   bool
	events    []OperationalEvent
	bytes     int
	dropped   uint64
	maxEvents int
	maxBytes  int
}

func NewOperationalBuffer() *OperationalBuffer {
	return NewOperationalBufferWithLimits(DefaultOperationalBufferMaxEvents, DefaultOperationalBufferMaxBytes)
}

func NewOperationalBufferWithLimits(maxEvents, maxBytes int) *OperationalBuffer {
	if maxEvents <= 0 || maxEvents > DefaultOperationalBufferMaxEvents {
		maxEvents = DefaultOperationalBufferMaxEvents
	}
	if maxBytes <= 0 || maxBytes > DefaultOperationalBufferMaxBytes {
		maxBytes = DefaultOperationalBufferMaxBytes
	}
	return &OperationalBuffer{maxEvents: maxEvents, maxBytes: maxBytes}
}

func (b *OperationalBuffer) Enable() {
	b.mu.Lock()
	b.enabled = true
	b.mu.Unlock()
}

func (b *OperationalBuffer) Disable() {
	b.mu.Lock()
	b.enabled = false
	b.mu.Unlock()
}

func (b *OperationalBuffer) Enabled() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.enabled
}

// Record validates and stores an event only when explicitly enabled.
func (b *OperationalBuffer) Record(event OperationalEvent) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.enabled {
		return errOperationalDisabled
	}
	if err := event.Validate(); err != nil {
		return err
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		return err
	}
	size := len(encoded)
	if b.eventLimit() == 0 || b.byteLimit() == 0 || size > b.byteLimit() {
		b.dropped++
		return errors.New("operational event exceeds buffer bounds")
	}
	for len(b.events) >= b.eventLimit() || size > b.byteLimit()-b.bytes {
		if !b.dropOldest() {
			b.dropped++
			return errors.New("operational buffer cannot make room for event")
		}
	}
	b.events = append(b.events, event)
	b.bytes += size
	return nil
}

func (b *OperationalBuffer) eventLimit() int {
	if b.maxEvents <= 0 || b.maxEvents > DefaultOperationalBufferMaxEvents {
		return DefaultOperationalBufferMaxEvents
	}
	return b.maxEvents
}

func (b *OperationalBuffer) byteLimit() int {
	if b.maxBytes <= 0 || b.maxBytes > DefaultOperationalBufferMaxBytes {
		return DefaultOperationalBufferMaxBytes
	}
	return b.maxBytes
}

func (b *OperationalBuffer) dropOldest() bool {
	if len(b.events) == 0 {
		b.bytes = 0
		return false
	}
	encoded, _ := json.Marshal(b.events[0])
	b.bytes -= len(encoded)
	if b.bytes < 0 {
		b.bytes = 0
	}
	copy(b.events, b.events[1:])
	b.events = b.events[:len(b.events)-1]
	b.dropped++
	return true
}

func (b *OperationalBuffer) Snapshot() []OperationalEvent {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return append([]OperationalEvent(nil), b.events...)
}

func (b *OperationalBuffer) ExportJSON() ([]byte, error) {
	return json.Marshal(b.Snapshot())
}

func (b *OperationalBuffer) Dropped() uint64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.dropped
}

func (b *OperationalBuffer) Clear() {
	b.mu.Lock()
	b.events = nil
	b.bytes = 0
	b.dropped = 0
	b.mu.Unlock()
}
