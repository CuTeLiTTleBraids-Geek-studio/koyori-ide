package services

import "time"

// HTTPEnvironment contains non-sensitive values and opaque references to the
// backend secrets service. Secret plaintext is deliberately not represented.
type HTTPEnvironment struct {
	Values     map[string]string `json:"values"`
	SecretRefs map[string]string `json:"secretRefs"`
}

// HTTPRequest is one parsed request from a .http document. Lines are 1-based
// and inclusive so the editor can select the request at the cursor.
type HTTPRequest struct {
	Name       string            `json:"name"`
	Method     string            `json:"method"`
	URL        string            `json:"url"`
	Headers    map[string]string `json:"headers"`
	Body       string            `json:"body"`
	StartLine  int               `json:"startLine"`
	EndLine    int               `json:"endLine"`
	SecretRefs map[string]string `json:"secretRefs"`
}

// HTTPRequestOptions makes every security-sensitive send policy explicit.
// Zero values select conservative defaults.
type HTTPRequestOptions struct {
	RequestID           string `json:"requestId"`
	TimeoutMs           int    `json:"timeoutMs"`
	MaxResponseBytes    int64  `json:"maxResponseBytes"`
	MaxRedirects        int    `json:"maxRedirects"`
	PrivateNetworkToken string `json:"privateNetworkToken"`
}

// HTTPResponse is safe to return to the frontend. Sensitive response header
// values are redacted before this value crosses the binding.
type HTTPResponse struct {
	RequestID  string            `json:"requestId"`
	Status     int               `json:"status"`
	StatusText string            `json:"statusText"`
	Headers    map[string]string `json:"headers"`
	Body       string            `json:"body"`
	DurationMs int64             `json:"durationMs"`
}

// HTTPHistoryEntry is the persisted, sanitized request/response summary.
type HTTPHistoryEntry struct {
	ID              string            `json:"id"`
	Name            string            `json:"name,omitempty"`
	Method          string            `json:"method"`
	URL             string            `json:"url"`
	RequestHeaders  map[string]string `json:"requestHeaders,omitempty"`
	Status          int               `json:"status,omitempty"`
	StatusText      string            `json:"statusText,omitempty"`
	ResponseHeaders map[string]string `json:"responseHeaders,omitempty"`
	ResponseBody    string            `json:"responseBody,omitempty"`
	DurationMs      int64             `json:"durationMs"`
	CreatedAt       time.Time         `json:"createdAt"`
	Error           string            `json:"error,omitempty"`
}
