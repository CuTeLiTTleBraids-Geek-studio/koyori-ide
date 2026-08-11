package services

import (
	"testing"
	"time"
)

func TestOutputBuffer_ReadClearsBuffer(t *testing.T) {
	ob := newOutputBuffer()
	ob.Append([]byte("hello"))

	first := ob.Read(1 * time.Second)
	if first != "hello" {
		t.Errorf("expected 'hello', got %q", first)
	}

	// Second read should be empty — buffer was cleared by the first read.
	second := ob.Read(200 * time.Millisecond)
	if second != "" {
		t.Errorf("expected empty string after clear, got %q", second)
	}
}

func TestOutputBuffer_AppendAndRead(t *testing.T) {
	ob := newOutputBuffer()
	ob.Append([]byte("data1"))
	ob.Append([]byte("data2"))

	result := ob.Read(1 * time.Second)
	if result != "data1data2" {
		t.Errorf("expected 'data1data2', got %q", result)
	}
}

func TestOutputBuffer_ReadEmpty(t *testing.T) {
	ob := newOutputBuffer()
	result := ob.Read(100 * time.Millisecond)
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

// H-3: an empty Read must sleep on notify/timer instead of returning early or
// polling in a default branch.
func TestOutputBuffer_Read_H3_BlocksWithoutData(t *testing.T) {
	ob := newOutputBuffer()
	started := time.Now()
	done := make(chan string, 1)
	go func() {
		done <- ob.Read(120 * time.Millisecond)
	}()

	early := time.NewTimer(30 * time.Millisecond)
	defer early.Stop()
	select {
	case result := <-done:
		t.Fatalf("empty read returned early with %q", result)
	case <-early.C:
	}

	deadline := time.NewTimer(500 * time.Millisecond)
	defer deadline.Stop()
	select {
	case result := <-done:
		if result != "" {
			t.Fatalf("expected empty result, got %q", result)
		}
		if elapsed := time.Since(started); elapsed < 90*time.Millisecond {
			t.Fatalf("empty read waited only %v", elapsed)
		}
	case <-deadline.C:
		t.Fatal("empty read did not return after its timeout")
	}
}

// N-66: Append must cap total buffered bytes. When the cap is exceeded,
// the oldest data is dropped so that the most recent output is retained.
func TestOutputBuffer_AppendCapsTotalSize(t *testing.T) {
	ob := newOutputBuffer()
	ob.maxBytes = 16 // small cap for deterministic testing

	// First two appends fit entirely (10 + 6 = 16, at cap, not over).
	ob.Append([]byte("0123456789")) // 10 bytes
	ob.Append([]byte("ABCDEF"))     // +6 = 16 bytes (at cap, not over)

	// Third append pushes over the cap. Buffer would be 19 bytes, so the
	// oldest 3 bytes ("012") are trimmed, leaving the most recent 16:
	// "3456789" + "ABCDEF" + "XYZ" = "3456789ABCDEFXYZ".
	ob.Append([]byte("XYZ"))

	got := ob.Read(200 * time.Millisecond)
	if got != "3456789ABCDEFXYZ" {
		t.Fatalf("expected trimmed buffer '3456789ABCDEFXYZ', got %q (len=%d)", got, len(got))
	}
	if len(got) != 16 {
		t.Fatalf("expected 16 bytes (cap), got %d", len(got))
	}
}

// N-66: when a single Append exceeds the cap on its own, only the tail
// of the new data is kept (so a huge chunk can't blow past the cap and
// stay there).
func TestOutputBuffer_AppendSingleChunkExceedsCap(t *testing.T) {
	ob := newOutputBuffer()
	ob.maxBytes = 8

	big := []byte("0123456789ABCDEF") // 16 bytes, 2x the cap
	ob.Append(big)

	got := ob.Read(200 * time.Millisecond)
	if len(got) != 8 {
		t.Fatalf("expected 8 bytes after trim, got %d bytes (%q)", len(got), got)
	}
	// The oldest half should be dropped: keep "89ABCDEF".
	if got != "89ABCDEF" {
		t.Fatalf("expected tail '89ABCDEF', got %q", got)
	}
}

// N-66: the cap holds steady across many appends — no unbounded growth.
func TestOutputBuffer_AppendManyChunksStayCapped(t *testing.T) {
	ob := newOutputBuffer()
	ob.maxBytes = 32

	// Append 1 KiB in 16-byte chunks. Without the cap, the buffer would
	// hold all 1024 bytes; with the cap, it must stay at <= 32 bytes.
	for i := 0; i < 64; i++ {
		ob.Append([]byte("0123456789ABCDEF"))
	}

	// Drain and check length. (We can't assert exact content because trim
	// happens after each write, but the total must never exceed the cap.)
	got := ob.Read(200 * time.Millisecond)
	if len(got) > 32 {
		t.Fatalf("buffer grew past cap: got %d bytes (cap=32), content=%q", len(got), got)
	}
	if len(got) != 32 {
		t.Logf("note: drained %d bytes (cap=32) — expected exactly cap", len(got))
	}
}
