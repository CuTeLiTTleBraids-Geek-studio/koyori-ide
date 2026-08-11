//go:build e2e

package services

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ConfigureHTTPClientE2EApprovalSequence installs a deterministic approval
// decision source for the real Wails HTTP-client probe. The helper is compiled
// only into explicit e2e builds and is never a production capability.
func ConfigureHTTPClientE2EApprovalSequence(service *HTTPClientService, decisions []bool) func() {
	if service == nil {
		return func() {}
	}
	sequence := append([]bool(nil), decisions...)
	var sequenceMu sync.Mutex
	index := 0
	service.mu.Lock()
	previous := service.approvePrivateNetwork
	service.approvePrivateNetwork = func(_ string) bool {
		sequenceMu.Lock()
		defer sequenceMu.Unlock()
		if index >= len(sequence) {
			return false
		}
		decision := sequence[index]
		index++
		return decision
	}
	service.mu.Unlock()
	return func() {
		service.mu.Lock()
		service.approvePrivateNetwork = previous
		service.mu.Unlock()
	}
}

// IssueExpiredHTTPClientE2EToken creates a capability that is structurally
// valid but already expired. It is used through the real generated binding to
// prove the backend rejects expired private-network authorization.
func IssueExpiredHTTPClientE2EToken(service *HTTPClientService, targetOrigin, requestID string) (string, error) {
	if service == nil {
		return "", fmt.Errorf("HTTP client service is unavailable")
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return "", fmt.Errorf("request id is required")
	}
	origin, err := canonicalHTTPOrigin(targetOrigin, true)
	if err != nil {
		return "", fmt.Errorf("invalid private-network origin: %w", err)
	}
	if _, err := ValidateNonPrivateURL(origin); err == nil {
		return "", fmt.Errorf("origin %q is public and does not require private-network approval", origin)
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("create expired private-network token: %w", err)
	}
	token := hex.EncodeToString(raw[:])
	now := service.privateNetworkNow
	if now == nil {
		now = time.Now
	}
	service.mu.Lock()
	service.privateNetworkApprovals[token] = privateNetworkApproval{
		origin: origin, requestID: requestID, expiresAt: now().Add(-time.Second),
	}
	service.mu.Unlock()
	return token, nil
}
