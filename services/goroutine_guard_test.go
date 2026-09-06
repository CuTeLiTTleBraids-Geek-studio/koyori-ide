package services

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// P20 P1-05: a panic inside a guarded long-lived goroutine must not kill the
// process and must be reported through the sink with the guard's scope, the
// panic value and a non-empty stack.

type goroutinePanicCapture struct {
	mu      sync.Mutex
	reports []goroutinePanicReport
}

type goroutinePanicReport struct {
	Scope      string
	PanicValue any
	Stack      string
}

func (c *goroutinePanicCapture) sink(scope string, panicValue any, stack []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reports = append(c.reports, goroutinePanicReport{
		Scope:      scope,
		PanicValue: panicValue,
		Stack:      string(stack),
	})
}

func (c *goroutinePanicCapture) snapshot() []goroutinePanicReport {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]goroutinePanicReport(nil), c.reports...)
}

func captureGoroutinePanics(t *testing.T) *goroutinePanicCapture {
	t.Helper()
	capture := &goroutinePanicCapture{}
	previous := SetGoroutinePanicSink(capture.sink)
	t.Cleanup(func() { SetGoroutinePanicSink(previous) })
	return capture
}

func TestRecoverGoroutinePanic_ReportedWithoutKillingProcess(t *testing.T) {
	capture := captureGoroutinePanics(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer RecoverGoroutinePanic("test:guarded-worker")
		defer func() { /* existing cleanup semantics still run */ }()
		panic("boom in guarded goroutine")
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("guarded goroutine did not return after panic")
	}
	// The process is still alive — the test itself would have crashed otherwise.
	reports := capture.snapshot()
	if len(reports) != 1 {
		t.Fatalf("expected exactly 1 panic report, got %d", len(reports))
	}
	report := reports[0]
	if report.Scope != "test:guarded-worker" {
		t.Errorf("scope = %q, want test:guarded-worker", report.Scope)
	}
	if report.PanicValue != "boom in guarded goroutine" {
		t.Errorf("panic value = %v, want the original panic payload", report.PanicValue)
	}
	if !strings.Contains(report.Stack, "TestRecoverGoroutinePanic_ReportedWithoutKillingProcess") {
		t.Errorf("stack should identify the panicking goroutine, got: %s", report.Stack)
	}
}

func TestRecoverGoroutinePanic_NoPanicNoReport(t *testing.T) {
	capture := captureGoroutinePanics(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer RecoverGoroutinePanic("test:clean-worker")
	}()
	<-done
	if reports := capture.snapshot(); len(reports) != 0 {
		t.Fatalf("clean goroutine must not produce a report, got %d", len(reports))
	}
}

func TestSetGoroutinePanicSink_ReturnsPreviousSink(t *testing.T) {
	capture := &goroutinePanicCapture{}
	previous := SetGoroutinePanicSink(capture.sink)
	t.Cleanup(func() { SetGoroutinePanicSink(previous) })

	gotBack := SetGoroutinePanicSink(nil)
	if gotBack == nil {
		t.Fatal("SetGoroutinePanicSink must return the previously installed sink")
	}
	// Restoring nil keeps the guard silent instead of panicking on a nil call.
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer RecoverGoroutinePanic("test:nil-sink")
		panic("unreported")
	}()
	<-done
}
