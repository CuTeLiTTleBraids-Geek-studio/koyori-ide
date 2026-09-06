//go:build windows

package main

import (
	"os/exec"
	"strconv"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	backendJobMu sync.Mutex
	backendJob   windows.Handle
)

func configureBackendProcess(_ *exec.Cmd) {}

func trackBackendProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return err
	}
	process, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION,
		false,
		uint32(cmd.Process.Pid),
	)
	if err != nil {
		_ = windows.CloseHandle(job)
		return err
	}
	defer windows.CloseHandle(process)
	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		_ = windows.CloseHandle(job)
		return err
	}
	backendJobMu.Lock()
	backendJob = job
	backendJobMu.Unlock()
	return nil
}

func releaseBackendProcess(_ *exec.Cmd) {
	backendJobMu.Lock()
	job := backendJob
	backendJob = 0
	backendJobMu.Unlock()
	if job != 0 {
		_ = windows.CloseHandle(job)
	}
}

func signalBackend(cmd *exec.Cmd) error {
	return killBackend(cmd)
}

func killBackend(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	backendJobMu.Lock()
	job := backendJob
	backendJob = 0
	backendJobMu.Unlock()
	if job != 0 {
		err := windows.TerminateJobObject(job, 1)
		_ = windows.CloseHandle(job)
		return err
	}
	// Fallback for tests or callers that did not attach a job. Process.Kill
	// alone is insufficient, so taskkill's /T flag tears down descendants too.
	return exec.Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F").Run()
}
