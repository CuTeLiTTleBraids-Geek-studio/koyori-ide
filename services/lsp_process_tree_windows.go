//go:build windows

package services

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	lspJobAttachTimeout = 2 * time.Second
	lspJobDrainTimeout  = 30 * time.Second
)

var isProcessInJobProc = windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")

type windowsLSPProcessTree struct {
	job       windows.Handle
	startOnce sync.Once
	done      chan struct{}
	err       error
}

type lspJobBasicAccountingInformation struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
}

func attachLSPProcessTree(cmd *exec.Cmd) (lspProcessTree, error) {
	if cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 0 {
		return nil, fmt.Errorf("LSP process is not running")
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create LSP job: %w", err)
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
		return nil, fmt.Errorf("configure LSP job: %w", err)
	}
	var rootStart int64
	assignErr := withLSPProcessHandle(cmd.Process, func(process windows.Handle) error {
		var identityErr error
		rootStart, identityErr = lspProcessStartTime(process)
		if identityErr != nil {
			return fmt.Errorf("read LSP process identity: %w", identityErr)
		}
		image, imageErr := lspProcessImage(process)
		if imageErr != nil {
			return fmt.Errorf("read LSP process image: %w", imageErr)
		}
		if !lspProcessImageMatches(image, cmd.Path) {
			return fmt.Errorf("LSP process identity changed before job assignment: image %q, expected %q", image, cmd.Path)
		}
		if err := windows.AssignProcessToJobObject(job, process); err != nil {
			return fmt.Errorf("assign LSP process to job: %w", err)
		}
		return nil
	})
	if assignErr != nil {
		return nil, cleanupFailedLSPJobAttach(job, assignErr)
	}
	if err := adoptExistingLSPDescendants(job, uint32(cmd.Process.Pid), rootStart, lspJobAttachTimeout); err != nil {
		return nil, cleanupFailedLSPJobAttach(job, err)
	}
	return &windowsLSPProcessTree{job: job, done: make(chan struct{})}, nil
}

// withLSPProcessHandle uses os.Process.WithHandle when the running Go
// runtime provides it, while retaining a Go 1.25-compatible PID fallback.
// The interface assertion avoids a compile-time dependency on the newer API.
func withLSPProcessHandle(process *os.Process, fn func(windows.Handle) error) error {
	if process == nil {
		return fmt.Errorf("LSP process is nil")
	}
	method := reflect.ValueOf(process).MethodByName("WithHandle")
	if method.IsValid() && method.Type().NumIn() == 1 {
		var callbackErr error
		callback := reflect.MakeFunc(method.Type().In(0), func(args []reflect.Value) []reflect.Value {
			if len(args) == 1 {
				if raw, ok := args[0].Interface().(uintptr); ok {
					callbackErr = fn(windows.Handle(raw))
				}
			}
			return nil
		})
		result := method.Call([]reflect.Value{callback})
		var callErr error
		if len(result) == 1 && !result[0].IsNil() {
			callErr, _ = result[0].Interface().(error)
		}
		return errors.Join(callbackErr, callErr)
	}
	handle, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION,
		false,
		uint32(process.Pid),
	)
	if err != nil {
		return fmt.Errorf("open LSP process for job assignment: %w", err)
	}
	defer windows.CloseHandle(handle)
	return fn(handle)
}

func lspProcessStartTime(process windows.Handle) (int64, error) {
	var creation, exitTime, kernelTime, userTime windows.Filetime
	if err := windows.GetProcessTimes(process, &creation, &exitTime, &kernelTime, &userTime); err != nil {
		return 0, err
	}
	return int64(uint64(creation.HighDateTime)<<32 | uint64(creation.LowDateTime)), nil
}

func lspProcessImage(process windows.Handle) (string, error) {
	buffer := make([]uint16, 1024)
	length := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(process, 0, &buffer[0], &length); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buffer[:length]), nil
}

func lspProcessImageMatches(actual, expected string) bool {
	actual = normalizeLSPImagePath(actual)
	expected = normalizeLSPImagePath(expected)
	if actual == "" || expected == "" {
		return false
	}
	if strings.EqualFold(actual, expected) {
		return true
	}
	actualInfo, actualErr := os.Stat(actual)
	expectedInfo, expectedErr := os.Stat(expected)
	return actualErr == nil && expectedErr == nil && os.SameFile(actualInfo, expectedInfo)
}

func normalizeLSPImagePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if resolved, err := exec.LookPath(path); err == nil {
		path = resolved
	}
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	if evaluated, err := filepath.EvalSymlinks(path); err == nil {
		path = evaluated
	}
	return filepath.Clean(path)
}

func cleanupFailedLSPJobAttach(job windows.Handle, attachErr error) error {
	terminateErr := windows.TerminateJobObject(job, 1)
	drainErr := waitForLSPJobDrain(job, lspJobDrainTimeout)
	closeErr := windows.CloseHandle(job)
	return errors.Join(attachErr, terminateErr, drainErr, closeErr)
}

func adoptExistingLSPDescendants(job windows.Handle, rootPID uint32, rootStart int64, timeout time.Duration) error {
	knownParents := map[uint32]struct{}{rootPID: {}}
	accounted := map[uint32]struct{}{rootPID: {}}
	expectedStarts := make(map[uint32]int64)
	deadline := time.Now().Add(timeout)
	stablePasses := 0
	for {
		// A short-lived cmd shim can exit after handing work to its child. If
		// Windows has already reused the PID, following the old parent PID can
		// adopt an unrelated process (for example, a WebView child). The Job
		// already owns descendants inherited before the shim exited, so stop
		// discovery when the root identity is gone or changed.
		currentStart, _, identityErr := processInfo(int(rootPID))
		if identityErr != nil {
			if errors.Is(identityErr, windows.ERROR_INVALID_PARAMETER) {
				return nil
			}
			return fmt.Errorf("read LSP root identity during adoption: %w", identityErr)
		}
		if currentStart != rootStart {
			return nil
		}
		parents, err := snapshotLSPProcessParents()
		if err != nil {
			return err
		}
		for changed := true; changed; {
			changed = false
			for pid := range parents {
				if _, known := knownParents[pid]; known {
					continue
				}
				current, valid, start, err := currentLSPDescendant(pid, rootPID, rootStart, parents)
				if err != nil {
					return err
				}
				if current && valid {
					knownParents[pid] = struct{}{}
					expectedStarts[pid] = start
					changed = true
				}
			}
		}

		pending := make([]uint32, 0)
		for pid := range knownParents {
			if _, ok := accounted[pid]; !ok {
				pending = append(pending, pid)
			}
		}
		sort.Slice(pending, func(i, j int) bool { return pending[i] < pending[j] })
		for _, pid := range pending {
			if currentStart, _, identityErr := processInfo(int(rootPID)); identityErr != nil {
				if errors.Is(identityErr, windows.ERROR_INVALID_PARAMETER) {
					return nil
				}
				return fmt.Errorf("read LSP root identity before descendant assignment: %w", identityErr)
			} else if currentStart != rootStart {
				return nil
			}
			// The initial process snapshot can become stale while candidates wait
			// behind an earlier assignment. Re-check the entire parent chain just
			// before opening/assigning this PID so a reparented or reused process
			// cannot be adopted merely because its old PID was once a descendant.
			parents, err := snapshotLSPProcessParents()
			if err != nil {
				return err
			}
			current, valid, start, err := currentLSPDescendant(pid, rootPID, rootStart, parents)
			if err != nil {
				return err
			}
			if !current || !valid || start != expectedStarts[pid] {
				delete(knownParents, pid)
				delete(expectedStarts, pid)
				continue
			}
			attached, err := ensureLSPProcessInJob(job, pid, expectedStarts[pid])
			if err != nil {
				return err
			}
			if attached {
				accounted[pid] = struct{}{}
			} else {
				delete(knownParents, pid)
				delete(expectedStarts, pid)
			}
		}
		if len(pending) == 0 {
			stablePasses++
			if stablePasses == 2 {
				return nil
			}
		} else {
			stablePasses = 0
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out attaching LSP descendants to process job")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// currentLSPDescendant verifies a PID parent chain against the root process
// incarnation. Parent PIDs are reusable on Windows; creation-time checks stop
// a stale chain from turning an unrelated process into a managed descendant.
func currentLSPDescendant(pid, rootPID uint32, rootStart int64, parents map[uint32]uint32) (current, valid bool, start int64, err error) {
	if pid == rootPID {
		return false, false, 0, nil
	}
	seen := make(map[uint32]struct{})
	chain := make([]uint32, 0, 8)
	currentPID := pid
	for currentPID != rootPID {
		if _, cycle := seen[currentPID]; cycle {
			return false, false, 0, nil
		}
		seen[currentPID] = struct{}{}
		chain = append(chain, currentPID)
		parentPID, ok := parents[currentPID]
		if !ok {
			return false, false, 0, nil
		}
		currentPID = parentPID
	}

	// Only query process handles after the parent snapshot proves that this
	// candidate reaches the managed root. This avoids requesting terminate or
	// query rights from unrelated protected services merely because they appear
	// in the global process snapshot.
	var childStart, candidateStart int64
	for _, currentPID := range chain {
		start, _, infoErr := processInfo(int(currentPID))
		if infoErr != nil {
			if errors.Is(infoErr, windows.ERROR_INVALID_PARAMETER) {
				return false, false, 0, nil
			}
			return false, false, 0, fmt.Errorf("read LSP descendant %d identity: %w", currentPID, infoErr)
		}
		if start < rootStart || (childStart != 0 && start > childStart) {
			return false, false, 0, nil
		}
		if candidateStart == 0 {
			candidateStart = start
		}
		childStart = start
	}
	return true, true, candidateStart, nil
}

func snapshotLSPProcessParents() (map[uint32]uint32, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("snapshot LSP process tree: %w", err)
	}
	defer windows.CloseHandle(snapshot)

	parents := make(map[uint32]uint32)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return nil, fmt.Errorf("read LSP process snapshot: %w", err)
	}
	for {
		parents[entry.ProcessID] = entry.ParentProcessID
		entry.Size = uint32(unsafe.Sizeof(entry))
		err = windows.Process32Next(snapshot, &entry)
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			return parents, nil
		}
		if err != nil {
			return nil, fmt.Errorf("read LSP process snapshot: %w", err)
		}
	}
}

func ensureLSPProcessInJob(job windows.Handle, pid uint32, expectedStart int64) (bool, error) {
	process, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION,
		false,
		pid,
	)
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return false, nil
		}
		return false, fmt.Errorf("open LSP descendant %d for job assignment: %w", pid, err)
	}
	defer windows.CloseHandle(process)
	actualStart, err := lspProcessStartTime(process)
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return false, nil
		}
		return false, fmt.Errorf("read LSP descendant %d identity before assignment: %w", pid, err)
	}
	if expectedStart == 0 || actualStart != expectedStart {
		return false, nil
	}
	inJob, err := lspProcessInJob(process, job)
	if err != nil {
		return false, fmt.Errorf("query LSP descendant %d job: %w", pid, err)
	}
	if inJob {
		return true, nil
	}
	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		return false, fmt.Errorf("assign LSP descendant %d to job: %w", pid, err)
	}
	return true, nil
}

func lspProcessInJob(process, job windows.Handle) (bool, error) {
	var result int32
	ok, _, callErr := isProcessInJobProc.Call(
		uintptr(process),
		uintptr(job),
		uintptr(unsafe.Pointer(&result)),
	)
	if ok == 0 {
		if errno, isErrno := callErr.(syscall.Errno); isErrno && errno != 0 {
			return false, errno
		}
		return false, fmt.Errorf("IsProcessInJob failed")
	}
	return result != 0, nil
}

func (tree *windowsLSPProcessTree) terminateAndWait(timeout time.Duration) error {
	if tree == nil {
		return nil
	}
	tree.startOnce.Do(func() {
		go tree.terminate()
	})
	if timeout <= 0 {
		timeout = lspProcessStopTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-tree.done:
		return tree.err
	case <-timer.C:
		return fmt.Errorf("timed out waiting for LSP process tree to exit")
	}
}

func (tree *windowsLSPProcessTree) terminate() {
	defer close(tree.done)
	if tree.job == 0 {
		return
	}
	terminateErr := windows.TerminateJobObject(tree.job, 1)
	drainErr := waitForLSPJobDrain(tree.job, lspJobDrainTimeout)
	closeErr := windows.CloseHandle(tree.job)
	tree.job = 0
	tree.err = errors.Join(terminateErr, drainErr, closeErr)
}

func waitForLSPJobDrain(job windows.Handle, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		var info lspJobBasicAccountingInformation
		if err := windows.QueryInformationJobObject(
			job,
			windows.JobObjectBasicAccountingInformation,
			uintptr(unsafe.Pointer(&info)),
			uint32(unsafe.Sizeof(info)),
			nil,
		); err != nil {
			return fmt.Errorf("query LSP job: %w", err)
		}
		if info.ActiveProcesses == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out draining LSP job with %d active processes", info.ActiveProcesses)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
