//go:build !windows

package services

import "os/exec"

func attachLSPProcessTree(_ *exec.Cmd) (lspProcessTree, error) {
	return nil, nil
}
