//go:build e2e

package services

import (
	"path/filepath"
	"testing"
)

func TestInstallDebugExecutableApprovalForE2EIsExactSingleUseAndRestorable(t *testing.T) {
	d := NewDebugService()
	previousCalls := 0
	d.approveProjectExecutable = func(string, string) bool {
		previousCalls++
		return true
	}
	expected := filepath.Join(t.TempDir(), "debug.js")
	probe, restore, err := InstallDebugExecutableApprovalForE2E(d, "program", expected)
	if err != nil {
		t.Fatalf("install e2e approval: %v", err)
	}

	if d.approveProjectExecutable("executable", expected) {
		t.Fatal("approval accepted the wrong executable kind")
	}
	if d.approveProjectExecutable("program", filepath.Join(filepath.Dir(expected), "other.js")) {
		t.Fatal("approval accepted the wrong path")
	}
	if !d.approveProjectExecutable("program", expected) {
		t.Fatal("approval rejected the exact kind and path")
	}
	if !probe.Consumed() {
		t.Fatal("successful approval was not recorded")
	}
	if d.approveProjectExecutable("program", expected) {
		t.Fatal("approval was reusable")
	}

	restore()
	if !d.approveProjectExecutable("program", expected) || previousCalls != 1 {
		t.Fatal("restore did not reinstate the previous approver")
	}
}
