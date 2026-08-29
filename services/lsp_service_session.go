package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"time"
)

type pendingDocumentChange struct {
	content    string
	generation uint64
	done       chan struct{}
	err        error
	opening    bool
	flushing   bool
	once       sync.Once
}

func (p *pendingDocumentChange) complete(err error) {
	if p == nil {
		return
	}
	p.once.Do(func() {
		p.err = err
		close(p.done)
	})
}

type lspProcess struct {
	cmd               *exec.Cmd
	done              chan struct{}
	tree              lspProcessTree
	mu                sync.Mutex
	waitErr           error
	triggerCharacters []string
}

type lspProcessTree interface {
	terminateAndWait(time.Duration) error
}

func newLSPProcess(cmd *exec.Cmd, trees ...lspProcessTree) *lspProcess {
	var tree lspProcessTree
	if len(trees) > 0 {
		tree = trees[0]
	}
	process := &lspProcess{cmd: cmd, done: make(chan struct{}), tree: tree}
	go func() {
		err := cmd.Wait()
		if tree != nil {
			err = errors.Join(err, tree.terminateAndWait(lspProcessStopTimeout))
		}
		process.mu.Lock()
		process.waitErr = err
		process.mu.Unlock()
		close(process.done)
	}()
	return process
}

func (p *lspProcess) result() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.waitErr
}

func (p *lspProcess) setTriggerCharacters(chars []string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.triggerCharacters = append([]string(nil), chars...)
	p.mu.Unlock()
}

func (p *lspProcess) getTriggerCharacters() []string {
	if p == nil {
		return []string{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.triggerCharacters...)
}

func (p *lspProcess) stop(timeout time.Duration) error {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	if timeout <= 0 {
		timeout = lspProcessStopTimeout
	}
	select {
	case <-p.done:
		return nil
	default:
	}
	var killErr error
	if p.tree != nil {
		killErr = p.tree.terminateAndWait(timeout)
	} else if err := p.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		select {
		case <-p.done:
			return nil
		default:
			return fmt.Errorf("kill LSP process: %w", err)
		}
	}
	return waitForLSPProcessExit(p.done, timeout, killErr)
}

func waitForLSPProcessExit(done <-chan struct{}, timeout time.Duration, killErr error) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return killErr
	case <-timer.C:
		waitErr := fmt.Errorf("timed out waiting for LSP process to exit")
		return errors.Join(killErr, waitErr)
	}
}

// LSPService manages language server processes (gopls, typescript-language-server).
type LSPService struct {
	mu               sync.Mutex
	lifecycleMu      sync.RWMutex
	languageLocks    map[string]*sync.Mutex
	workspaceRoot    string
	workspaceContext *WorkspaceContext
	// workspaceRoots 是多根工作区列表（Priority 4）。当列表非空时，
	// LSP initialize 会以多根形式向服务器声明 workspaceFolders，
	// 并通过 workspace/didChangeWorkspaceFolders 通知变更。当列表为空时
	// 退化到单根行为，保持向后兼容。
	workspaceRoots []string
	servers        map[string]*lspServer // keyed by language: "go", "typescript", "javascript"
	// lastErrors records the last start/init failure per language (8-D).
	lastErrors map[string]string
	// switching is protected by mu and remains true across stop/restart phases.
	switching bool

	// F-2 (prompt-2.md): lspConfig 存储 per-section 配置（如 "gopls"、
	// "typescript"），用于响应 workspace/configuration 请求。key 为 section
	// 名，value 为配置对象（任意 JSON 可序列化结构）。由 SetLSPConfig 注入。
	lspConfig   map[string]interface{}
	lspConfigMu sync.RWMutex
	// F-2: fileSvc 用于 workspace/applyEdit 应用 WorkspaceEdit
	// （TextDocumentEdit / CreateFile / DeleteFile / RenameFile）。可选注入。
	fileSvc *FileService
	// diagnosticRefreshVersions lets headless callers observe refresh requests
	// even when no Wails application is attached. Protected by mu.
	diagnosticRefreshVersions map[string]uint64
}

// NewLSPService creates a new LSPService with the given workspace root.
func NewLSPService(workspaceRoot string) *LSPService {
	return newLSPService(workspaceRoot, nil)
}

// NewLSPServiceWithWorkspaceContext creates the renderer-facing LSP service.
// Process launches are bound to the active root and generation at call time.
func NewLSPServiceWithWorkspaceContext(workspaceContext *WorkspaceContext) *LSPService {
	return newLSPService("", workspaceContext)
}

func newLSPService(workspaceRoot string, workspaceContext *WorkspaceContext) *LSPService {
	return &LSPService{
		workspaceRoot:             workspaceRoot,
		workspaceContext:          workspaceContext,
		servers:                   make(map[string]*lspServer),
		languageLocks:             make(map[string]*sync.Mutex),
		lastErrors:                make(map[string]string),
		lspConfig:                 make(map[string]interface{}),
		diagnosticRefreshVersions: make(map[string]uint64),
	}
}

func (s *LSPService) languageLifecycleLock(language string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.languageLocks == nil {
		s.languageLocks = make(map[string]*sync.Mutex)
	}
	lock := s.languageLocks[language]
	if lock == nil {
		lock = &sync.Mutex{}
		s.languageLocks[language] = lock
	}
	return lock
}

func (s *LSPService) observeLSPProcess(language string, srv *lspServer) {
	if srv == nil || srv.process == nil {
		return
	}
	<-srv.process.done
	waitErr := srv.process.result()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.servers[language] != srv {
		return
	}
	srv.running = false
	delete(s.servers, language)
	if waitErr != nil {
		if s.lastErrors == nil {
			s.lastErrors = make(map[string]string)
		}
		s.lastErrors[language] = fmt.Sprintf("language server exited: %v", waitErr)
	}
}

// rebuildLSPServerAfterWriteFailures replaces a managed server whose client
// has observed repeated write failures. The server is detached while holding
// the service lock, then stopped and restarted without that lock.
func (s *LSPService) rebuildLSPServerAfterWriteFailures(language string, failedClient *jsonRPCClient, cause error) {
	language = lspServerKey(language)
	if language == "" || failedClient == nil {
		return
	}
	s.lifecycleMu.RLock()
	defer s.lifecycleMu.RUnlock()
	languageLock := s.languageLifecycleLock(language)
	languageLock.Lock()
	defer languageLock.Unlock()

	s.mu.Lock()
	if s.switching {
		s.mu.Unlock()
		return
	}
	srv := s.servers[language]
	if srv == nil || srv.client != failedClient || !srv.managed || !srv.running {
		s.mu.Unlock()
		return
	}
	delete(s.servers, language)
	srv.running = false
	if s.lastErrors == nil {
		s.lastErrors = make(map[string]string)
	}
	s.lastErrors[language] = fmt.Sprintf("rebuilding after repeated LSP write failures: %v", cause)
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), lspProcessStopTimeout+time.Second)
	stopErr := stopLSPServerProcess(ctx, srv, cause)
	cancel()
	if stopErr != nil {
		slog.Warn("failed to stop LSP server for write-failure rebuild", "language", language, "err", stopErr)
		return
	}
	if err := s.startLSPServer(language, false); err != nil {
		slog.Warn("failed to rebuild LSP server after repeated write failures", "language", language, "err", err)
		return
	}
	slog.Warn("rebuilt LSP server after repeated write failures", "language", language, "cause", cause)
}

func stopLSPServerProcess(ctx context.Context, srv *lspServer, reason error) error {
	if srv == nil {
		return nil
	}
	srv.beginClosing(reason)
	if srv.process == nil {
		if srv.cmd != nil && srv.cmd.Process != nil {
			return fmt.Errorf("LSP process has no Wait owner")
		}
		return nil
	}
	done := make(chan error, 1)
	go func() {
		done <- srv.process.stop(lspProcessStopTimeout)
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// FIX A9: stop processes after detaching them from the service map so no
// service-wide mutex is held while Kill/Wait blocks. Each process receives an
// independent timeout and all stops run concurrently.
func stopLSPServersConcurrently(servers map[string]*lspServer, reason error) {
	var wg sync.WaitGroup
	for language, srv := range servers {
		language, srv := language, srv
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), lspProcessStopTimeout)
			defer cancel()
			if err := stopLSPServerProcess(ctx, srv, reason); err != nil {
				slog.Warn("failed to stop LSP server", "language", language, "err", err)
			}
		}()
	}
	wg.Wait()
}

func (s *LSPService) setLastError(language string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastErrors == nil {
		s.lastErrors = make(map[string]string)
	}
	if err == nil {
		delete(s.lastErrors, language)
		return
	}
	s.lastErrors[language] = err.Error()
}

// SetLSPConfig 注入 per-section LSP 配置，用于响应 workspace/configuration
// 请求。section 如 "gopls" / "typescript"；config 为任意 JSON 可序列化对象
// （如 {"buildFlags":["-tags=integration"]}）。F-2 (prompt-2.md)。
func (s *LSPService) SetLSPConfig(section string, config interface{}) {
	s.lspConfigMu.Lock()
	defer s.lspConfigMu.Unlock()
	if s.lspConfig == nil {
		s.lspConfig = make(map[string]interface{})
	}
	s.lspConfig[section] = config
}

// setFileService 注入 FileService，用于 workspace/applyEdit 应用 WorkspaceEdit。
// 未注入时 applyEdit 返回 applied=false。F-2 (prompt-2.md)。
//
//wails:ignore
func (s *LSPService) setFileService(fsvc *FileService) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fileSvc = fsvc
}
