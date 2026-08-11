package services

import "os/exec"

// lookPathExists reports whether name resolves on PATH.
func lookPathExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
