//go:build !windows

package services

import "os"

func replaceFileAtomically(source, target string) error {
	return os.Rename(source, target)
}
