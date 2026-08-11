package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	debugAdapterMaxArgs      = 128
	debugAdapterMaxArgLength = 32 * 1024
)

// debugAdapterLaunchPolicy is intentionally deny-by-default. Extension
// contribution parsing must supply an exact executable allowlist explicitly.
type debugAdapterLaunchPolicy struct {
	WorkspaceRoot      string
	AllowedExecutables []string
}

type debugAdapterLaunchRequest struct {
	Executable string
	Args       []string
	Cwd        string
}

type debugAdapterProcess struct {
	Cmd    *exec.Cmd
	Stdin  io.WriteCloser
	Stdout io.ReadCloser
	Stderr io.ReadCloser
}

type debugAdapterPipeAddr string

func (a debugAdapterPipeAddr) Network() string { return "stdio" }
func (a debugAdapterPipeAddr) String() string  { return string(a) }

// stdioDAPConn adapts an adapter's stdio pipes to the existing DAP transport.
// The write deadline is enforced during best-effort disconnect so an adapter
// that stops reading cannot hang shutdown.
type stdioDAPConn struct {
	reader io.ReadCloser
	writer io.WriteCloser

	writeMu       sync.Mutex
	deadlineMu    sync.Mutex
	writeDeadline time.Time
	closeOnce     sync.Once
	closeErr      error
}

func newStdioDAPConn(reader io.ReadCloser, writer io.WriteCloser) net.Conn {
	return &stdioDAPConn{reader: reader, writer: writer}
}

func (c *stdioDAPConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

func (c *stdioDAPConn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.deadlineMu.Lock()
	deadline := c.writeDeadline
	c.deadlineMu.Unlock()
	if deadline.IsZero() {
		return c.writer.Write(p)
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return 0, os.ErrDeadlineExceeded
	}
	type writeResult struct {
		n   int
		err error
	}
	done := make(chan writeResult, 1)
	go func() {
		n, err := c.writer.Write(p)
		done <- writeResult{n: n, err: err}
	}()
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case result := <-done:
		return result.n, result.err
	case <-timer.C:
		_ = c.writer.Close()
		return 0, os.ErrDeadlineExceeded
	}
}

func (c *stdioDAPConn) Close() error {
	c.closeOnce.Do(func() {
		c.closeErr = errors.Join(c.writer.Close(), c.reader.Close())
	})
	return c.closeErr
}

func (c *stdioDAPConn) LocalAddr() net.Addr  { return debugAdapterPipeAddr("koyori-ide") }
func (c *stdioDAPConn) RemoteAddr() net.Addr { return debugAdapterPipeAddr("language-pack-adapter") }
func (c *stdioDAPConn) SetDeadline(deadline time.Time) error {
	if err := c.SetWriteDeadline(deadline); err != nil {
		return err
	}
	return c.SetReadDeadline(deadline)
}
func (c *stdioDAPConn) SetReadDeadline(deadline time.Time) error {
	if setter, ok := c.reader.(interface{ SetReadDeadline(time.Time) error }); ok {
		return setter.SetReadDeadline(deadline)
	}
	return nil
}
func (c *stdioDAPConn) SetWriteDeadline(deadline time.Time) error {
	c.deadlineMu.Lock()
	c.writeDeadline = deadline
	c.deadlineMu.Unlock()
	return nil
}

// startDebugAdapter starts an allowlisted adapter directly with stdio DAP
// pipes. It never invokes a command shell or resolves an executable through
// PATH.
func startDebugAdapter(
	ctx context.Context,
	policy debugAdapterLaunchPolicy,
	request debugAdapterLaunchRequest,
) (*debugAdapterProcess, error) {
	cmd, err := newDebugAdapterCommand(ctx, policy, request)
	if err != nil {
		return nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open debug adapter stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("open debug adapter stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("open debug adapter stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, fmt.Errorf("start debug adapter: %w", err)
	}
	return &debugAdapterProcess{Cmd: cmd, Stdin: stdin, Stdout: stdout, Stderr: stderr}, nil
}

func newDebugAdapterCommand(
	ctx context.Context,
	policy debugAdapterLaunchPolicy,
	request debugAdapterLaunchRequest,
) (*exec.Cmd, error) {
	if ctx == nil {
		return nil, fmt.Errorf("debug adapter context is required")
	}
	if strings.TrimSpace(policy.WorkspaceRoot) == "" {
		return nil, fmt.Errorf("debug adapter workspace root is required")
	}
	if len(policy.AllowedExecutables) == 0 {
		return nil, fmt.Errorf("debug adapter executable allowlist is empty")
	}
	if request.Executable == "" || !filepath.IsAbs(request.Executable) {
		return nil, fmt.Errorf("debug adapter executable must be an absolute path")
	}
	if len(request.Args) > debugAdapterMaxArgs {
		return nil, fmt.Errorf("debug adapter has too many arguments")
	}
	for _, arg := range request.Args {
		if len(arg) > debugAdapterMaxArgLength || strings.IndexByte(arg, 0) >= 0 {
			return nil, fmt.Errorf("debug adapter argument is invalid")
		}
	}

	root, err := canonicalDebugAdapterPath(policy.WorkspaceRoot, true)
	if err != nil {
		return nil, fmt.Errorf("resolve debug adapter workspace root: %w", err)
	}
	cwd := request.Cwd
	if cwd == "" {
		cwd = root
	} else if !filepath.IsAbs(cwd) {
		cwd = filepath.Join(root, cwd)
	}
	cwd, err = canonicalDebugAdapterPath(cwd, true)
	if err != nil {
		return nil, fmt.Errorf("resolve debug adapter cwd: %w", err)
	}
	if !debugAdapterPathWithin(root, cwd) {
		return nil, fmt.Errorf("debug adapter cwd is outside workspace")
	}

	executable, err := canonicalDebugAdapterPath(request.Executable, false)
	if err != nil {
		return nil, fmt.Errorf("resolve debug adapter executable: %w", err)
	}
	allowed := false
	for _, candidate := range policy.AllowedExecutables {
		if !filepath.IsAbs(candidate) {
			continue
		}
		canonical, candidateErr := canonicalDebugAdapterPath(candidate, false)
		if candidateErr == nil && debugAdapterPathsEqual(canonical, executable) {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, fmt.Errorf("debug adapter executable is not allowlisted")
	}

	cmd := exec.CommandContext(ctx, executable, request.Args...)
	cmd.Dir = cwd
	return cmd, nil
}

func canonicalDebugAdapterPath(path string, directory bool) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if directory && !info.IsDir() {
		return "", fmt.Errorf("path is not a directory")
	}
	if !directory && !info.Mode().IsRegular() {
		return "", fmt.Errorf("path is not a regular file")
	}
	return filepath.Clean(canonical), nil
}

func debugAdapterPathWithin(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func debugAdapterPathsEqual(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}
