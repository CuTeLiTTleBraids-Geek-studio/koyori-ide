//go:build windows

package services

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestHeadlessAgentHostRejectsStateDirectoryJunction(t *testing.T) {
	target := t.TempDir()
	junction := filepath.Join(t.TempDir(), "state-junction")
	createWindowsJunction(t, junction, target)
	host, err := NewHeadlessAgentHost(t.TempDir(), junction)
	if host != nil {
		_ = host.Close()
	}
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("junction state directory error = %v, want ErrInvalidInput", err)
	}
}
