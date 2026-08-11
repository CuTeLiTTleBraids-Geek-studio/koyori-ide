package services

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
)

// N-63: Retry on transient errors (429 rate limit, 5xx server errors).
// Non-retryable: 4xx (except 429), client errors, malformed responses,
// context cancellation (N-61 fix).

const (
	// maxRetries is the maximum number of retry attempts for a transient error.
	// Total attempts = 1 + maxRetries (e.g. maxRetries=3 → up to 4 attempts).
	maxRetries = 3
	// baseBackoff is the initial backoff duration. Subsequent retries use
	// baseBackoff * 2^attempt with up to 50% jitter.
	baseBackoff = 500 * time.Millisecond
	// maxBackoff caps the backoff so a long Retry-After doesn't stall the
	// request indefinitely.
	maxBackoff = 30 * time.Second
)

// isRetryableStatus returns true for HTTP status codes that indicate a
// transient server-side or rate-limit condition worth retrying.
func isRetryableStatus(statusCode int) bool {
	return statusCode == http.StatusTooManyRequests || // 429
		(statusCode >= 500 && statusCode < 600) // 5xx
}

// retryAfterFromHeader extracts the Retry-After header value (seconds) from
// the response. Returns 0 if absent or unparseable. Capped at maxBackoff.
func retryAfterFromHeader(resp *http.Response) time.Duration {
	if resp == nil {
		return 0
	}
	v := resp.Header.Get("Retry-After")
	if v == "" {
		return 0
	}
	// Retry-After can be either seconds (integer) or an HTTP-date.
	// We only support the seconds form for simplicity and predictability.
	secs, err := strconv.Atoi(v)
	if err != nil || secs < 0 {
		return 0
	}
	d := time.Duration(secs) * time.Second
	if d > maxBackoff {
		return maxBackoff
	}
	return d
}

// backoffDuration computes the backoff for a given attempt (0-indexed).
// Uses exponential growth: base * 2^attempt, capped at maxBackoff, with
// up to 50% jitter (random multiplier in [1.0, 1.5)) to avoid thundering
// herds when many clients retry simultaneously.
func backoffDuration(attempt int) time.Duration {
	d := baseBackoff << attempt // base * 2^attempt
	if d <= 0 || d > maxBackoff {
		d = maxBackoff
	}
	// Jitter: add [0, 0.5*base) random delay.
	jitter := time.Duration(rand.Int64N(int64(d / 2)))
	return d + jitter
}

// isContextError reports whether err is a context cancellation or deadline
// exceeded error (possibly wrapped). These are non-retryable: the user
// explicitly cancelled the request, or the overall deadline expired.
// N-61: previously the network-error path retried ALL errors including
// context.Canceled, causing up to 3 backoff sleeps after the user pressed
// stop.
func isContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// doWithRetry executes the given request function with retry logic for
// transient errors (N-63). The function should return the HTTP response
// (with body unread) and an error. On retryable status codes, the response
// body is drained and closed before sleeping/backoff.
//
// The caller is responsible for closing the final returned response body.
//
// Retryable conditions:
//   - HTTP 429 (Too Many Requests) — respects Retry-After header
//   - HTTP 5xx (Internal Server Error, Bad Gateway, etc.)
//   - Network-level errors (DNS, connection refused, TLS handshake) that
//     may be transient
//
// Non-retryable:
//   - HTTP 4xx (except 429) — returned immediately with body readable
//   - context.Canceled / context.DeadlineExceeded (N-61) — returned immediately
//   - Non-transient request errors
//
// N-61 fixes applied:
//  1. Removed duplicate dead code (the second `if err == nil && !isRetryableStatus`
//     block that could never execute).
//  2. context.Canceled and context.DeadlineExceeded are now returned immediately
//     without retrying (via isContextError check).
//  3. When retries are exhausted on a retryable status (429/5xx), the response
//     is returned with its body STILL OPEN so the caller can read the error
//     message. Previously the body was closed before the final return, causing
//     parseAIError to fail with "read on closed body".
func doWithRetry(do func() (*http.Response, error)) (*http.Response, error) {
	for attempt := 0; attempt <= maxRetries; attempt++ {
		resp, err := do()

		// Success or non-retryable HTTP status — return immediately with
		// body readable. This single check replaces the previous duplicate
		// (N-61 dead-code fix).
		if err == nil && !isRetryableStatus(resp.StatusCode) {
			return resp, nil
		}

		// N-61: context cancellation/deadline is non-retryable. The user
		// pressed stop, or the overall deadline expired — retrying would
		// just sleep and fail again. Close any partial response body and
		// return the error immediately.
		if err != nil && isContextError(err) {
			if resp != nil {
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
			return nil, err
		}

		// Retryable HTTP status (429 or 5xx).
		if err == nil && resp != nil {
			// On the last attempt, return the response WITHOUT closing the
			// body so the caller can read the error message (N-61 fix #3).
			if attempt == maxRetries {
				return resp, nil
			}
			// Not the last attempt: drain, close, back off, and retry.
			backoff := backoffDuration(attempt)
			if resp.StatusCode == http.StatusTooManyRequests {
				if ra := retryAfterFromHeader(resp); ra > 0 {
					backoff = ra
				}
				slog.Warn("ai retry: 429 rate limited",
					"attempt", attempt+1, "maxAttempts", maxRetries+1,
					"backoffMs", backoff.Milliseconds())
			} else {
				slog.Warn("ai retry: server error",
					"attempt", attempt+1, "maxAttempts", maxRetries+1,
					"status", resp.StatusCode, "backoffMs", backoff.Milliseconds())
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			time.Sleep(backoff)
			continue
		}

		// Network error path (non-context errors like DNS, connection refused,
		// TLS handshake — these may be transient).
		if err != nil {
			if resp != nil {
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
			slog.Warn("ai retry: network error",
				"attempt", attempt+1, "maxAttempts", maxRetries+1, "err", err)
			// On the last attempt, return the error.
			if attempt == maxRetries {
				return nil, err
			}
			time.Sleep(backoffDuration(attempt))
			continue
		}
	}
	// Unreachable: every loop path either returns or continues. Kept as a
	// safety net for static analysis.
	return nil, nil
}
