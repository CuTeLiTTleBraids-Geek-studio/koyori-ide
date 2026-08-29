//go:build windows

package services

import (
	"os"

	"golang.org/x/sys/windows"
)

func agentFileHasMultipleLinks(file *os.File) (bool, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil {
		return false, err
	}
	return info.NumberOfLinks > 1, nil
}

func agentFileOwnedByCurrentUser(_ *os.File) (bool, error) {
	// Windows ownership/DACL validation requires a separate ACL contract. The
	// root/link identity remains enforced here; ACL evidence stays explicitly U.
	return true, nil
}

func headlessPathHasLinkBoundary(path string) (bool, error) {
	path16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false, err
	}
	attributes, err := windows.GetFileAttributes(path16)
	if err != nil {
		return false, err
	}
	return attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0, nil
}
