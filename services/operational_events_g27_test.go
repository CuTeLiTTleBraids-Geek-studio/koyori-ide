package services

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

func validOperationalEvent(t *testing.T) OperationalEvent {
	t.Helper()
	id, err := NewOperationalSessionID()
	if err != nil {
		t.Fatal(err)
	}
	return OperationalEvent{
		SchemaVersion:  OperationalSchemaVersionV1,
		Event:          OperationalEventWorkspaceEdit,
		Timestamp:      time.Now().UTC(),
		ReleaseVersion: "v1.2.3",
		Commit:         "0123456789abcdef",
		Channel:        "stable",
		OS:             "linux",
		Arch:           "amd64",
		SessionID:      id,
		Outcome:        OperationalOutcomeSucceeded,
	}
}

func TestOperationalBufferDisabledByDefault(t *testing.T) {
	b := NewOperationalBuffer()
	if b.Enabled() {
		t.Fatal("buffer must default to disabled")
	}
	if err := b.Record(validOperationalEvent(t)); err == nil {
		t.Fatal("disabled Record unexpectedly succeeded")
	}
	if len(b.Snapshot()) != 0 {
		t.Fatal("disabled buffer retained an event")
	}
}

func TestOperationalBufferEnableBoundsDropAndClear(t *testing.T) {
	b := NewOperationalBufferWithLimits(2, DefaultOperationalBufferMaxBytes)
	b.Enable()
	for i := 0; i < 3; i++ {
		if err := b.Record(validOperationalEvent(t)); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(b.Snapshot()); got != 2 {
		t.Fatalf("snapshot length = %d, want 2", got)
	}
	if got := b.Dropped(); got != 1 {
		t.Fatalf("drop count = %d, want 1", got)
	}
	exported, err := b.ExportJSON()
	if err != nil || strings.Contains(string(exported), "path") || strings.Contains(string(exported), "content") {
		t.Fatalf("unsafe export %q, err=%v", exported, err)
	}
	b.Clear()
	if len(b.Snapshot()) != 0 || b.Dropped() != 0 {
		t.Fatal("Clear did not reset events and drop count")
	}
}

func TestOperationalEventRejectsUnknownEventCategoryAndField(t *testing.T) {
	e := validOperationalEvent(t)
	e.Event = "unknown.event"
	if err := e.Validate(); err == nil {
		t.Fatal("unknown event accepted")
	}
	e = validOperationalEvent(t)
	e.Outcome = "unknown"
	if err := e.Validate(); err == nil {
		t.Fatal("unknown outcome accepted")
	}
	e = validOperationalEvent(t)
	e.Event = OperationalEventLSPRequestLatency
	e.Outcome = ""
	e.Category = "unknown"
	e.DurationBucket = OperationalDurationFast
	if err := e.Validate(); err == nil {
		t.Fatal("unknown category accepted")
	}
	raw, _ := json.Marshal(validOperationalEvent(t))
	raw = append(raw[:len(raw)-1], []byte(`,"metadata":{"x":"y"}}`)...)
	var decoded OperationalEvent
	if err := json.Unmarshal(raw, &decoded); err == nil {
		t.Fatal("unknown JSON field accepted")
	}
}

func TestOperationalEventPrivacyPayloadRejected(t *testing.T) {
	values := []string{
		"/home/alice/project",
		`C:\\Users\\alice\\project`,
		"file:///tmp/source.go",
		"https://example.com/path",
		"192.168.1.12",
		"192.168.1.12:443",
		"[2001:db8::1]:8443",
		"2001:db8::1",
		"host.office.internal",
		"release/../../secret",
		"git+ssh://host/repo",
		"password=hunter2",
		"access_token = hunter2",
		"prompt: source text",
		"content=source text",
		strings.Repeat("a", operationalMaxStringBytes+1),
	}
	for _, value := range values {
		e := validOperationalEvent(t)
		e.ReleaseVersion = value
		if err := e.Validate(); err == nil {
			t.Errorf("private value accepted: %q", value)
		}
	}
}

func TestOperationalEventReleaseVersionStrictSemver(t *testing.T) {
	for _, version := range []string{"1.2.3", "v1.2.3", "1.2.3-rc.1", "v1.2.3+build.7"} {
		e := validOperationalEvent(t)
		e.ReleaseVersion = version
		if err := e.Validate(); err != nil {
			t.Errorf("valid releaseVersion %q rejected: %v", version, err)
		}
	}
	for _, version := range []string{"host.office.internal", "abcdef123456", "token_value", "V1.2.3", "vv1.2.3", "1.2", "01.2.3", "1.2.3-01", "https://host/1.2.3"} {
		e := validOperationalEvent(t)
		e.ReleaseVersion = version
		if err := e.Validate(); err == nil {
			t.Errorf("non-semver releaseVersion accepted: %q", version)
		}
	}
}

func TestOperationalBufferInvalidLimitsAreClamped(t *testing.T) {
	tests := []struct {
		name                string
		maxEvents, maxBytes int
	}{
		{"negative", -1, -1},
		{"zero event", 0, 128},
		{"zero bytes", 2, 0},
		{"huge", int(^uint(0) >> 1), int(^uint(0) >> 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			b := NewOperationalBufferWithLimits(test.maxEvents, test.maxBytes)
			if b.eventLimit() <= 0 || b.eventLimit() > DefaultOperationalBufferMaxEvents || b.byteLimit() <= 0 || b.byteLimit() > DefaultOperationalBufferMaxBytes {
				t.Fatalf("unsafe limits retained: events=%d bytes=%d", b.eventLimit(), b.byteLimit())
			}
			b.Enable()
			_ = b.Record(validOperationalEvent(t))
		})
	}

	b := &OperationalBuffer{enabled: true, maxEvents: 1, maxBytes: 1, bytes: 1}
	if err := b.Record(validOperationalEvent(t)); err == nil {
		t.Fatal("impossible empty-buffer bounds unexpectedly accepted event")
	}
	if len(b.events) != 0 {
		t.Fatal("empty buffer defense created an event")
	}
}

func TestSessionIDRandomOpaque(t *testing.T) {
	a, err := NewOperationalSessionID()
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewOperationalSessionID()
	if err != nil {
		t.Fatal(err)
	}
	if a == b || !validSessionID(a) || !validSessionID(b) {
		t.Fatalf("session IDs are not distinct valid opaque IDs: %q %q", a, b)
	}
}

func TestOperationalBufferConcurrency(t *testing.T) {
	b := NewOperationalBufferWithLimits(64, DefaultOperationalBufferMaxBytes)
	b.Enable()
	event := validOperationalEvent(t)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = b.Record(event)
				_ = b.Snapshot()
				_ = b.Dropped()
			}
		}()
	}
	wg.Wait()
	if got := len(b.Snapshot()); got > 64 {
		t.Fatalf("concurrent buffer exceeded bound: %d", got)
	}
	b.Disable()
}
