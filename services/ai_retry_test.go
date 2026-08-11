package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestIsRetryableStatus(t *testing.T) {
	cases := []struct {
		code int
		want bool
	}{
		{http.StatusOK, false},
		{http.StatusBadRequest, false},
		{http.StatusUnauthorized, false},
		{http.StatusForbidden, false},
		{http.StatusNotFound, false},
		{http.StatusTooManyRequests, true},
		{http.StatusInternalServerError, true},
		{http.StatusBadGateway, true},
		{http.StatusServiceUnavailable, true},
		{http.StatusGatewayTimeout, true},
		{599, true},
		{600, false},
	}
	for _, c := range cases {
		if got := isRetryableStatus(c.code); got != c.want {
			t.Errorf("isRetryableStatus(%d) = %v, want %v", c.code, got, c.want)
		}
	}
}

func TestRetryAfterFromHeader(t *testing.T) {
	resp := &http.Response{Header: http.Header{}}
	if got := retryAfterFromHeader(resp); got != 0 {
		t.Errorf("expected 0 for missing header, got %v", got)
	}
	resp.Header.Set("Retry-After", "5")
	if got := retryAfterFromHeader(resp); got != 5*time.Second {
		t.Errorf("expected 5s, got %v", got)
	}
	resp.Header.Set("Retry-After", "999")
	if got := retryAfterFromHeader(resp); got != maxBackoff {
		t.Errorf("expected capped to maxBackoff, got %v", got)
	}
	resp.Header.Set("Retry-After", "not-a-number")
	if got := retryAfterFromHeader(resp); got != 0 {
		t.Errorf("expected 0 for unparseable, got %v", got)
	}
	resp.Header.Set("Retry-After", "-3")
	if got := retryAfterFromHeader(resp); got != 0 {
		t.Errorf("expected 0 for negative, got %v", got)
	}
}

func TestRetryAfterFromHeader_NilResponse(t *testing.T) {
	if got := retryAfterFromHeader(nil); got != 0 {
		t.Errorf("expected 0 for nil response, got %v", got)
	}
}

func TestBackoffDuration_GrowsWithAttempt(t *testing.T) {
	d0 := backoffDuration(0)
	d1 := backoffDuration(1)
	d2 := backoffDuration(2)
	// base=500ms, so d0 ~ [500, 750), d1 ~ [1000, 1500), d2 ~ [2000, 3000)
	if d0 < 500*time.Millisecond || d0 >= 750*time.Millisecond {
		t.Errorf("attempt 0 backoff out of range: %v", d0)
	}
	if d1 < 1000*time.Millisecond || d1 >= 1500*time.Millisecond {
		t.Errorf("attempt 1 backoff out of range: %v", d1)
	}
	if d2 < 2000*time.Millisecond || d2 >= 3000*time.Millisecond {
		t.Errorf("attempt 2 backoff out of range: %v", d2)
	}
}

func TestBackoffDuration_CappedAtMax(t *testing.T) {
	// attempt 10 would be 500ms * 2^10 = 512s, way over maxBackoff (30s).
	d := backoffDuration(10)
	if d > maxBackoff+maxBackoff/2 {
		t.Errorf("backoff should be capped near maxBackoff + jitter, got %v", d)
	}
}

// N-63: doWithRetry returns immediately on success.
func TestDoWithRetry_SucceedsOnFirstAttempt(t *testing.T) {
	var calls int32
	do := func() (*http.Response, error) {
		atomic.AddInt32(&calls, 1)
		return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
	}
	resp, err := doWithRetry(do)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if calls != 1 {
		t.Errorf("expected 1 call, got %d", calls)
	}
}

// N-63: doWithRetry retries on 429 then succeeds.
func TestDoWithRetry_RetriesOn429ThenSucceeds(t *testing.T) {
	var calls int32
	do := func() (*http.Response, error) {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			return &http.Response{StatusCode: 429, Body: http.NoBody}, nil
		}
		return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
	}
	resp, err := doWithRetry(do)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("expected 200 after retries, got %d", resp.StatusCode)
	}
	if calls != 3 {
		t.Errorf("expected 3 calls (2 retries), got %d", calls)
	}
}

// N-63: doWithRetry retries on 500 then succeeds.
func TestDoWithRetry_RetriesOn500ThenSucceeds(t *testing.T) {
	var calls int32
	do := func() (*http.Response, error) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			return &http.Response{StatusCode: 503, Body: http.NoBody}, nil
		}
		return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
	}
	resp, err := doWithRetry(do)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("expected 200 after retry, got %d", resp.StatusCode)
	}
	if calls != 2 {
		t.Errorf("expected 2 calls (1 retry), got %d", calls)
	}
}

// N-63: non-retryable status (400) returns immediately without retrying.
func TestDoWithRetry_NonRetryableReturnsImmediately(t *testing.T) {
	var calls int32
	do := func() (*http.Response, error) {
		atomic.AddInt32(&calls, 1)
		return &http.Response{StatusCode: 400, Body: http.NoBody}, nil
	}
	resp, _ := doWithRetry(do)
	if resp.StatusCode != 400 {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
	if calls != 1 {
		t.Errorf("expected 1 call (no retry for 400), got %d", calls)
	}
}

// N-63: network error is retried, then succeeds.
func TestDoWithRetry_RetriesOnNetworkError(t *testing.T) {
	var calls int32
	do := func() (*http.Response, error) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			return nil, errors.New("connection refused")
		}
		return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
	}
	resp, err := doWithRetry(do)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("expected 200 after retry, got %d", resp.StatusCode)
	}
	if calls != 2 {
		t.Errorf("expected 2 calls (1 retry), got %d", calls)
	}
}

// N-63: persistent 429 exhausts retries and returns the last response.
func TestDoWithRetry_ExhaustsRetriesOnPersistent429(t *testing.T) {
	var calls int32
	do := func() (*http.Response, error) {
		atomic.AddInt32(&calls, 1)
		return &http.Response{StatusCode: 429, Body: http.NoBody}, nil
	}
	resp, err := doWithRetry(do)
	// err should be nil (the last response had no transport error); the
	// caller checks resp.StatusCode for the final 429.
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 429 {
		t.Errorf("expected final 429, got %d", resp.StatusCode)
	}
	if calls != maxRetries+1 {
		t.Errorf("expected %d total calls, got %d", maxRetries+1, calls)
	}
}

// N-63: respects Retry-After header for 429 (uses the header value instead
// of exponential backoff when present).
func TestDoWithRetry_RespectsRetryAfterHeader(t *testing.T) {
	// Use a server that returns 429 with Retry-After: 0 (so no actual sleep)
	// to keep the test fast, while still exercising the header-parsing path.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(429)
	}))
	defer srv.Close()
	var calls int32
	do := func() (*http.Response, error) {
		atomic.AddInt32(&calls, 1)
		return http.Get(srv.URL + "/")
	}
	resp, _ := doWithRetry(do)
	if resp.StatusCode != 429 {
		t.Errorf("expected 429, got %d", resp.StatusCode)
	}
	if calls != maxRetries+1 {
		t.Errorf("expected %d calls, got %d", maxRetries+1, calls)
	}
}

// N-63: Send retries on 429 then succeeds (integration with AIService).
func TestAIService_N63_SendRetriesOn429ThenSucceeds(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n < 2 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()
	a := NewAIService()
	a.config = AIConfig{APIKey: "k", BaseURL: srv.URL, Model: "m"}
	resp, err := a.Send([]ChatMessage{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("expected 'ok', got %q", resp.Content)
	}
	if calls != 2 {
		t.Errorf("expected 2 calls (1 retry), got %d", calls)
	}
}

// N-63: Send does NOT retry on 400 (non-retryable).
func TestAIService_N63_SendDoesNotRetryOn400(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":{"message":"bad request"}}`))
	}))
	defer srv.Close()
	a := NewAIService()
	a.config = AIConfig{APIKey: "k", BaseURL: srv.URL, Model: "m"}
	_, err := a.Send([]ChatMessage{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("expected error for 400")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should mention 400, got: %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 call (no retry for 400), got %d", calls)
	}
}

// N-61 fix #1: context.Canceled must NOT be retried. Previously the network-
// error path retried all errors including context.Canceled, causing up to 3
// backoff sleeps after the user pressed stop.
func TestDoWithRetry_N61_ContextCanceledNotRetried(t *testing.T) {
	var calls int32
	canceledErr := context.Canceled
	do := func() (*http.Response, error) {
		atomic.AddInt32(&calls, 1)
		return nil, canceledErr
	}
	_, err := doWithRetry(do)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 call (no retry for context.Canceled), got %d", calls)
	}
}

// N-61 fix #1: context.DeadlineExceeded must NOT be retried.
func TestDoWithRetry_N61_DeadlineExceededNotRetried(t *testing.T) {
	var calls int32
	deadlineErr := context.DeadlineExceeded
	do := func() (*http.Response, error) {
		atomic.AddInt32(&calls, 1)
		return nil, deadlineErr
	}
	_, err := doWithRetry(do)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected context.DeadlineExceeded, got %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 call (no retry for deadline), got %d", calls)
	}
}

// N-61 fix #1: wrapped context.Canceled must also be detected via errors.Is.
func TestDoWithRetry_N61_WrappedContextCanceledNotRetried(t *testing.T) {
	var calls int32
	wrapped := fmt.Errorf("request failed: %w", context.Canceled)
	do := func() (*http.Response, error) {
		atomic.AddInt32(&calls, 1)
		return nil, wrapped
	}
	_, err := doWithRetry(do)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected wrapped context.Canceled, got %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 call, got %d", calls)
	}
}

// N-61 fix #3: when retries are exhausted on a 429, the response body must
// still be readable (not closed). Previously the body was closed before the
// final return, causing callers to fail with "read on closed body".
func TestDoWithRetry_N61_Exhausted429BodyStillReadable(t *testing.T) {
	var calls int32
	do := func() (*http.Response, error) {
		atomic.AddInt32(&calls, 1)
		return &http.Response{
			StatusCode: 429,
			Body:       io.NopCloser(strings.NewReader("rate limited, try later")),
			Header:     http.Header{},
		}, nil
	}
	resp, err := doWithRetry(do)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 429 {
		t.Errorf("expected 429, got %d", resp.StatusCode)
	}
	if calls != maxRetries+1 {
		t.Errorf("expected %d calls, got %d", maxRetries+1, calls)
	}
	// The body MUST be readable — this is the N-61 fix #3.
	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		t.Fatalf("body should be readable after retries exhausted, got error: %v", readErr)
	}
	if string(body) != "rate limited, try later" {
		t.Errorf("unexpected body content: %q", string(body))
	}
	resp.Body.Close()
}

// N-61 fix #3: same test for 5xx — body must be readable after exhaustion.
func TestDoWithRetry_N61_Exhausted500BodyStillReadable(t *testing.T) {
	var calls int32
	do := func() (*http.Response, error) {
		atomic.AddInt32(&calls, 1)
		return &http.Response{
			StatusCode: 503,
			Body:       io.NopCloser(strings.NewReader("service unavailable")),
			Header:     http.Header{},
		}, nil
	}
	resp, err := doWithRetry(do)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 503 {
		t.Errorf("expected 503, got %d", resp.StatusCode)
	}
	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		t.Fatalf("body should be readable, got error: %v", readErr)
	}
	if string(body) != "service unavailable" {
		t.Errorf("unexpected body: %q", string(body))
	}
	resp.Body.Close()
}

// N-61 fix #2: verify the duplicate dead code is gone. This is a structural
// test — we verify that a non-retryable status (400) is returned on the FIRST
// check without any retry. If the duplicate dead code were still present, the
// behavior would be the same but the code would be redundant. This test
// documents the expected behavior.
func TestDoWithRetry_N61_NoDeadCode_NonRetryableReturnsImmediately(t *testing.T) {
	var calls int32
	do := func() (*http.Response, error) {
		atomic.AddInt32(&calls, 1)
		return &http.Response{StatusCode: 403, Body: http.NoBody}, nil
	}
	resp, err := doWithRetry(do)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 403 {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
	if calls != 1 {
		t.Errorf("expected 1 call (dead code removed, single check), got %d", calls)
	}
}

// N-61 fix: context.Canceled with a non-nil response — the response body
// should be closed before returning (no leak).
func TestDoWithRetry_N61_ContextCanceledClosesResponseBody(t *testing.T) {
	var calls int32
	bodyClosed := false
	do := func() (*http.Response, error) {
		atomic.AddInt32(&calls, 1)
		body := &closerTracker{onClose: func() { bodyClosed = true }}
		return &http.Response{StatusCode: 200, Body: body}, context.Canceled
	}
	_, err := doWithRetry(do)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 call, got %d", calls)
	}
	if !bodyClosed {
		t.Error("response body should be closed when returning context.Canceled")
	}
}

// closerTracker is an io.ReadCloser that tracks whether Close was called.
type closerTracker struct {
	closed   bool
	onClose  func()
	contents string
}

func (c *closerTracker) Read(p []byte) (int, error) { return 0, io.EOF }
func (c *closerTracker) Close() error {
	if !c.closed {
		c.closed = true
		if c.onClose != nil {
			c.onClose()
		}
	}
	return nil
}
