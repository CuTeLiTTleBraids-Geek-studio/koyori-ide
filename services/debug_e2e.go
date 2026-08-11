//go:build e2e

package services

import (
	"fmt"
	"path/filepath"
	"sync/atomic"
)

// DebugExecutableApprovalProbe records whether an e2e-only, exact-path debug
// approval was consumed. It does not exist in ordinary production builds.
type DebugExecutableApprovalProbe struct {
	consumed atomic.Bool
}

func (p *DebugExecutableApprovalProbe) Consumed() bool {
	return p != nil && p.consumed.Load()
}

// InstallDebugExecutableApprovalForE2E replaces the native dialog with a
// single-use approver bound to one kind and one absolute path. The caller must
// restore the previous approver after the real packaged launch completes.
func InstallDebugExecutableApprovalForE2E(
	d *DebugService,
	expectedKind string,
	expectedPath string,
) (*DebugExecutableApprovalProbe, func(), error) {
	if d == nil {
		return nil, nil, fmt.Errorf("debug service is required")
	}
	if expectedKind == "" {
		return nil, nil, fmt.Errorf("debug approval kind is required")
	}
	abs, err := filepath.Abs(expectedPath)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve debug approval path: %w", err)
	}
	expectedPath = filepath.Clean(abs)
	probe := &DebugExecutableApprovalProbe{}
	previous := d.approveProjectExecutable
	d.approveProjectExecutable = func(kind, path string) bool {
		if kind != expectedKind || filepath.Clean(path) != expectedPath {
			return false
		}
		return probe.consumed.CompareAndSwap(false, true)
	}
	return probe, func() { d.approveProjectExecutable = previous }, nil
}
