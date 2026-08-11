package services

// F-9 (prompt-2.md 第 537-586 行): 远程开发 SSH 最小版本。
//
// 本文件实现：
//   - FileSystem 接口（统一抽象本地与远程文件操作）
//   - LocalFileSystem 实现（封装 os 包的本地文件操作）
//   - SSHFileSystem 实现（基于 golang.org/x/crypto/ssh + github.com/pkg/sftp）
//   - RemoteService 服务层（管理多个 SSH 会话，注册到 Wails）
//
// 重要约定：
//   1. SSH 连接必须经过 known_hosts 校验，防止中间人攻击。
//   2. 密码和密钥路径不记录到日志（G-SEC-07 安全合规）。
//   3. 远程会话通过 host 标识索引，并发安全（sync.Mutex 保护 sessions）。

import (
	"bytes"
	"context"
	"crypto/hmac"
	crypto_rand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/pkg/sftp"
	"github.com/wailsapp/wails/v3/pkg/application"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// FileSystem 是统一的文件系统抽象接口，本地与远程实现都满足此接口。
// F-9 阶段1：抽象层使前端与上层服务对本地/远程操作无感知。
type FileSystem interface {
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte) error
	ListDirectory(path string) ([]FileInfo, error)
	CreateFile(path string) error
	CreateDirectory(path string) error
	DeletePath(path string) error
	RenamePath(old, new string) error
	Stat(path string) (FileInfo, error)
	Watch(ctx context.Context, path string) (<-chan FileEvent, error)
}

// FileInfo 描述文件或目录的元信息，跨本地与远程使用。
type FileInfo struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	IsDir    bool   `json:"isDir"`
	Size     int64  `json:"size"`
	Modified int64  `json:"modified"` // Unix timestamp (毫秒)
}

// FileEvent 是文件系统变更事件。Type 取值：
//   - "create"  新建文件/目录
//   - "modify"  内容修改
//   - "delete"  删除
//   - "rename"  重命名（OldPath 保存旧路径）
type FileEvent struct {
	Type    string `json:"type"`
	Path    string `json:"path"`
	OldPath string `json:"oldPath,omitempty"`
}

// SSHConfig 描述一次 SSH 连接所需的全部参数。
// KeyPath 与 Password 二选一：KeyPath 优先；都为空时返回错误。
type SSHConfig struct {
	Host           string `json:"host"`
	Port           int    `json:"port"`
	User           string `json:"user"`
	KeyPath        string `json:"keyPath,omitempty"`
	Password       string `json:"password,omitempty"`
	KnownHostsPath string `json:"knownHostsPath,omitempty"`
}

// RemoteConfig 是项目级别的远程配置：包含 SSH 连接信息和远程项目名。
// 嵌入 Project 结构体的 Remote 字段（指针，omitempty）。
type RemoteConfig struct {
	Config SSHConfig `json:"config"`
	Name   string    `json:"name"` // 远程项目名
}

// ============================================================================
// LocalFileSystem：本地文件系统实现
// ============================================================================

// LocalFileSystem 使用 os 标准库实现 FileSystem。
// 不做沙箱校验（沙箱校验在 FileService 中完成），仅做纯粹的 IO 委托。
type LocalFileSystem struct{}

// NewLocalFileSystem 创建一个本地文件系统实例。
func NewLocalFileSystem() *LocalFileSystem {
	return &LocalFileSystem{}
}

// ReadFile 读取文件全部内容。
func (l *LocalFileSystem) ReadFile(path string) ([]byte, error) {
	if path == "" {
		return nil, errors.New("path is empty")
	}
	return readFileLimited(path, maxReadableFileBytes)
}

// WriteFile 写入文件（创建或截断）。文件权限 0644。
func (l *LocalFileSystem) WriteFile(path string, data []byte) error {
	if path == "" {
		return errors.New("path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}
	return os.WriteFile(path, data, 0644)
}

// ListDirectory 列出目录直接子项，目录在前，按名字排序。
func (l *LocalFileSystem) ListDirectory(path string) ([]FileInfo, error) {
	if path == "" {
		return nil, errors.New("path is empty")
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	result := make([]FileInfo, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		result = append(result, FileInfo{
			Name:     entry.Name(),
			Path:     filepath.Join(path, entry.Name()),
			IsDir:    entry.IsDir(),
			Size:     info.Size(),
			Modified: info.ModTime().UnixMilli(),
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].IsDir != result[j].IsDir {
			return result[i].IsDir
		}
		return result[i].Name < result[j].Name
	})
	return result, nil
}

// CreateFile 创建空文件（已存在则截断）。父目录必须存在。
func (l *LocalFileSystem) CreateFile(path string) error {
	if path == "" {
		return errors.New("path is empty")
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	return f.Close()
}

// CreateDirectory 创建目录（含父目录）。
func (l *LocalFileSystem) CreateDirectory(path string) error {
	if path == "" {
		return errors.New("path is empty")
	}
	return os.MkdirAll(path, 0755)
}

// DeletePath 删除文件或目录（递归）。
func (l *LocalFileSystem) DeletePath(path string) error {
	if path == "" {
		return errors.New("path is empty")
	}
	return os.RemoveAll(path)
}

// RenamePath 重命名/移动文件或目录。
func (l *LocalFileSystem) RenamePath(old, new string) error {
	if old == "" || new == "" {
		return errors.New("path is empty")
	}
	return os.Rename(old, new)
}

// Stat 返回文件或目录的元信息。
func (l *LocalFileSystem) Stat(path string) (FileInfo, error) {
	if path == "" {
		return FileInfo{}, errors.New("path is empty")
	}
	info, err := os.Stat(path)
	if err != nil {
		return FileInfo{}, err
	}
	return FileInfo{
		Name:     filepath.Base(path),
		Path:     path,
		IsDir:    info.IsDir(),
		Size:     info.Size(),
		Modified: info.ModTime().UnixMilli(),
	}, nil
}

// Watch 使用原生文件系统通知递归监视本地目录。SSHFileSystem 仍保留
// SFTP 轮询，因为远端协议不提供可移植的原生 watch。
func (l *LocalFileSystem) Watch(ctx context.Context, path string) (<-chan FileEvent, error) {
	if path == "" {
		return nil, errors.New("path is empty")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("watch target must be a directory: %s", path)
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("create local file watcher: %w", err)
	}
	if err := addLocalWatchTree(watcher, path); err != nil {
		return nil, errors.Join(err, watcher.Close())
	}

	ch := make(chan FileEvent, 64)
	go localFSNotifyLoop(ctx, watcher, filepath.Clean(path), ch)
	return ch, nil
}

func addLocalWatchTree(watcher *fsnotify.Watcher, root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			return nil
		}
		if err := watcher.Add(path); err != nil {
			return fmt.Errorf("watch local directory %s: %w", path, err)
		}
		return nil
	})
}

const (
	localWatchEventSendTimeout = 500 * time.Millisecond
	localWatchDedupeWindow     = 75 * time.Millisecond
	localWatchDedupeMaxEntries = 2048
)

type localWatchDeduper struct {
	window time.Duration
	last   map[string]time.Time
}

func newLocalWatchDeduper(window time.Duration) *localWatchDeduper {
	return &localWatchDeduper{window: window, last: make(map[string]time.Time)}
}

func (d *localWatchDeduper) shouldPublish(event FileEvent, now time.Time) bool {
	oldPath := event.OldPath
	if oldPath != "" {
		oldPath = filepath.Clean(oldPath)
	}
	key := event.Type + "\x00" + filepath.Clean(event.Path) + "\x00" + oldPath
	if previous, ok := d.last[key]; ok {
		elapsed := now.Sub(previous)
		if elapsed >= 0 && elapsed < d.window {
			return false
		}
	}
	d.last[key] = now
	if len(d.last) > localWatchDedupeMaxEntries {
		cutoff := now.Add(-d.window)
		for entryKey, publishedAt := range d.last {
			if publishedAt.Before(cutoff) {
				delete(d.last, entryKey)
			}
		}
		if len(d.last) > localWatchDedupeMaxEntries {
			var oldestKey string
			var oldestTime time.Time
			for entryKey, publishedAt := range d.last {
				if oldestKey == "" || publishedAt.Before(oldestTime) {
					oldestKey = entryKey
					oldestTime = publishedAt
				}
			}
			delete(d.last, oldestKey)
		}
	}
	return true
}

func localFSNotifyLoop(
	ctx context.Context,
	watcher *fsnotify.Watcher,
	root string,
	ch chan<- FileEvent,
) {
	defer close(ch)
	defer func() {
		if err := watcher.Close(); err != nil {
			slog.Warn("close local file watcher", "root", root, "error", err)
		}
	}()
	deduper := newLocalWatchDeduper(localWatchDedupeWindow)

	for {
		select {
		case <-ctx.Done():
			return
		case watchErr, ok := <-watcher.Errors:
			if !ok {
				return
			}
			slog.Warn("local file watch error", "root", root, "error", watchErr)
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if !publishLocalFSNotifyEvent(ctx, watcher, root, ch, event, deduper, time.Now()) {
				return
			}
		}
	}
}

func publishLocalFSNotifyEvent(
	ctx context.Context,
	watcher *fsnotify.Watcher,
	root string,
	ch chan<- FileEvent,
	event fsnotify.Event,
	deduper *localWatchDeduper,
	now time.Time,
) bool {
	path := filepath.Clean(event.Name)
	publish := func(fileEvent FileEvent) bool {
		if !deduper.shouldPublish(fileEvent, now) {
			return true
		}
		return sendWatchEvent(ctx, ch, fileEvent)
	}
	if event.Has(fsnotify.Create) {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			if err := addLocalWatchTree(watcher, path); err != nil {
				slog.Warn("add created directory to local watcher", "path", path, "error", err)
			}
		}
		if !publish(FileEvent{Type: "create", Path: path}) {
			return false
		}
	}
	if event.Has(fsnotify.Write) {
		if !publish(FileEvent{Type: "modify", Path: path}) {
			return false
		}
	}
	if event.Has(fsnotify.Rename) {
		if !publish(FileEvent{Type: "rename", Path: path, OldPath: path}) {
			return false
		}
	}
	if event.Has(fsnotify.Remove) {
		if !publish(FileEvent{Type: "delete", Path: path}) {
			return false
		}
	}

	if path == root && (event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename)) {
		return false
	}
	return true
}

func sendWatchEvent(ctx context.Context, ch chan<- FileEvent, event FileEvent) bool {
	select {
	case <-ctx.Done():
		return false
	case ch <- event:
		return true
	default:
	}

	timer := time.NewTimer(localWatchEventSendTimeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case ch <- event:
		return true
	case <-timer.C:
		slog.Warn(
			"file watch event channel remained full; stopping watcher",
			"eventType", event.Type,
			"path", event.Path,
			"timeout", localWatchEventSendTimeout,
		)
		return false
	}
}

// ============================================================================
// SSHFileSystem：远程文件系统实现
// ============================================================================

// SSHFileSystem 通过 SFTP 协议操作远程文件系统。
// 一个 SSHFileSystem 绑定到一个 SSH 会话（*ssh.Client）。
type SSHFileSystem struct {
	client *ssh.Client
	sftp   *sftp.Client
}

const maxSSHReadFileSize = int64(64 * 1024 * 1024)

// newSSHFileSystem 基于 *ssh.Client 创建一个 SSHFileSystem。
// 同时初始化 SFTP 子系统客户端。
func newSSHFileSystem(client *ssh.Client) (*SSHFileSystem, error) {
	sc, err := sftp.NewClient(client)
	if err != nil {
		return nil, fmt.Errorf("open sftp session: %w", err)
	}
	return &SSHFileSystem{client: client, sftp: sc}, nil
}

// ReadFile 通过 SFTP 读取远程文件。
func (s *SSHFileSystem) ReadFile(path string) ([]byte, error) {
	if path == "" {
		return nil, errors.New("path is empty")
	}
	f, err := s.sftp.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readRemoteFileLimited(f, maxSSHReadFileSize)
}

func readRemoteFileLimited(r io.Reader, maxSize int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxSize+1))
	if err != nil {
		return nil, fmt.Errorf("read remote file: %w", err)
	}
	if int64(len(data)) > maxSize {
		return nil, fmt.Errorf("remote file exceeds maximum size of %d bytes", maxSize)
	}
	return data, nil
}

// WriteFile 通过 SFTP 写入远程文件。
func (s *SSHFileSystem) WriteFile(path string, data []byte) error {
	if path == "" {
		return errors.New("path is empty")
	}
	dir := sftpDir(path)
	if dir != "" && dir != path {
		if err := s.sftp.MkdirAll(dir); err != nil {
			return fmt.Errorf("create remote parent dir: %w", err)
		}
	}
	f, err := s.sftp.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(data)
	return err
}

// ListDirectory 列出远程目录直接子项。
func (s *SSHFileSystem) ListDirectory(path string) ([]FileInfo, error) {
	if path == "" {
		return nil, errors.New("path is empty")
	}
	entries, err := s.sftp.ReadDir(path)
	if err != nil {
		return nil, err
	}
	result := make([]FileInfo, 0, len(entries))
	for _, entry := range entries {
		result = append(result, FileInfo{
			Name:     entry.Name(),
			Path:     sftpJoin(path, entry.Name()),
			IsDir:    entry.IsDir(),
			Size:     entry.Size(),
			Modified: entry.ModTime().UnixMilli(),
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].IsDir != result[j].IsDir {
			return result[i].IsDir
		}
		return result[i].Name < result[j].Name
	})
	return result, nil
}

// CreateFile 在远程创建空文件。
func (s *SSHFileSystem) CreateFile(path string) error {
	if path == "" {
		return errors.New("path is empty")
	}
	f, err := s.sftp.Create(path)
	if err != nil {
		return err
	}
	return f.Close()
}

// CreateDirectory 在远程递归创建目录。
// sftp.Client.MkdirAll 在 pkg/sftp v1.10+ 可用。
func (s *SSHFileSystem) CreateDirectory(path string) error {
	if path == "" {
		return errors.New("path is empty")
	}
	return s.sftp.MkdirAll(path)
}

// DeletePath 删除远程文件或目录（递归）。
// SFTP 没有递归删除 API，需手动遍历。
func (s *SSHFileSystem) DeletePath(path string) error {
	if path == "" {
		return errors.New("path is empty")
	}
	info, err := s.sftp.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return s.sftp.Remove(path)
	}
	return sftpRemoveAll(s.sftp, path)
}

// sftpRemoveAll 递归删除远程目录。
func sftpRemoveAll(c *sftp.Client, path string) error {
	entries, err := c.ReadDir(path)
	if err != nil {
		return err
	}
	for _, e := range entries {
		full := sftpJoin(path, e.Name())
		if e.IsDir() {
			if err := sftpRemoveAll(c, full); err != nil {
				return err
			}
		} else {
			if err := c.Remove(full); err != nil {
				return err
			}
		}
	}
	return c.RemoveDirectory(path)
}

// RenamePath 在远程重命名/移动文件或目录。
func (s *SSHFileSystem) RenamePath(old, new string) error {
	if old == "" || new == "" {
		return errors.New("path is empty")
	}
	return s.sftp.Rename(old, new)
}

// Stat 返回远程文件或目录的元信息。
func (s *SSHFileSystem) Stat(path string) (FileInfo, error) {
	if path == "" {
		return FileInfo{}, errors.New("path is empty")
	}
	info, err := s.sftp.Stat(path)
	if err != nil {
		return FileInfo{}, err
	}
	return FileInfo{
		Name:     sftpBase(path),
		Path:     path,
		IsDir:    info.IsDir(),
		Size:     info.Size(),
		Modified: info.ModTime().UnixMilli(),
	}, nil
}

// Watch 远程文件系统监视的最小实现。
//
// 远程 SFTP 没有 inotify 等机制，统一采用轮询：每 2 秒 ReadDir 一次，
// 对比 mtime 快照，向通道推送 create/modify/delete 事件。
// 重命名事件通过 (delete old, create new) 的组合近似表达。
func (s *SSHFileSystem) Watch(ctx context.Context, path string) (<-chan FileEvent, error) {
	if path == "" {
		return nil, errors.New("path is empty")
	}
	info, err := s.sftp.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("watch target must be a directory: %s", path)
	}
	ch := make(chan FileEvent, 16)
	go sftpWatchLoop(ctx, s.sftp, path, ch)
	return ch, nil
}

// sftpWatchLoop 是 SSHFileSystem.Watch 的轮询循环。
func sftpWatchLoop(ctx context.Context, c *sftp.Client, dir string, ch chan<- FileEvent) {
	defer close(ch)
	const interval = 2 * time.Second
	snapshot := make(map[string]int64)
	if entries, err := c.ReadDir(dir); err == nil {
		for _, e := range entries {
			snapshot[sftpJoin(dir, e.Name())] = e.ModTime().UnixMilli()
		}
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		entries, err := c.ReadDir(dir)
		if err != nil {
			sendWatchEvent(ctx, ch, FileEvent{Type: "delete", Path: dir})
			return
		}
		seen := make(map[string]bool, len(entries))
		for _, e := range entries {
			full := sftpJoin(dir, e.Name())
			seen[full] = true
			mtime := e.ModTime().UnixMilli()
			old, existed := snapshot[full]
			if !existed {
				if !sendWatchEvent(ctx, ch, FileEvent{Type: "create", Path: full}) {
					return
				}
			} else if old != mtime {
				if !sendWatchEvent(ctx, ch, FileEvent{Type: "modify", Path: full}) {
					return
				}
			}
			snapshot[full] = mtime
		}
		for p := range snapshot {
			if !seen[p] {
				if !sendWatchEvent(ctx, ch, FileEvent{Type: "delete", Path: p}) {
					return
				}
				delete(snapshot, p)
			}
		}
	}
}

// sftpJoin 用正斜杠连接远程路径（SFTP 始终使用 POSIX 路径）。
func sftpJoin(base, name string) string {
	if base == "" {
		return name
	}
	if strings.HasSuffix(base, "/") {
		return base + name
	}
	return base + "/" + name
}

// sftpBase 返回远程路径的最后一节（等价于 filepath.Base，但仅用 /）。
func sftpBase(path string) string {
	path = strings.TrimRight(path, "/")
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}

// sftpDir 返回远程路径的父目录（等价于 filepath.Dir，但仅用 /）。
func sftpDir(path string) string {
	path = strings.TrimRight(path, "/")
	if i := strings.LastIndex(path, "/"); i >= 0 {
		if i == 0 {
			return "/"
		}
		return path[:i]
	}
	return ""
}

// ============================================================================
// RemoteService：服务层
// ============================================================================

// SSHSession 封装一个已建立的 SSH+SFTP 会话。
type SSHSession struct {
	client            *ssh.Client
	sftp              *sftp.Client
	fs                *SSHFileSystem
	generation        uint64
	HostID            string
	HostInstanceNonce string
	WorkspaceID       string
}

// RemoteHostInfo is a read-only snapshot of the verified remote host identity.
type RemoteHostInfo struct {
	HostID            string
	HostInstanceNonce string
}

// RemoteScope is the complete authorization scope of a remote session.
type RemoteScope struct {
	HostID            string
	HostInstanceNonce string
	WorkspaceID       string
	Generation        uint64
}

// RemoteService 管理多个命名 SSH 会话，注册为 Wails 服务。
//
// 并发安全：sessions map 由 mu 保护，所有公开方法都先取锁。
// 会话生命周期：Connect 创建 → GetFileSystem/ExecuteCommand 使用 → Disconnect 关闭。
type RemoteService struct {
	mu              sync.Mutex
	sessions        map[string]*SSHSession
	closed          bool
	nextGeneration  uint64
	approvalKey     [sha256.Size]byte
	approvalKeyGood bool
	approvals       map[string]remoteCommandApproval
	approveCommand  func(name string, argv []string) bool
	now             func() time.Time
}

type remoteCommandApproval struct {
	session     string
	commandHash string
	scope       RemoteScope
	expiresAt   time.Time
	binding     [sha256.Size]byte
}

const (
	remoteCommandApprovalTTL   = 2 * time.Minute
	remoteCommandApprovalLimit = 128
	// SSHConfig currently has no remote root. This sentinel produces only a
	// connection-local session scope; it is not a host-issued workspace ID.
	remoteWorkspaceRootSentinel = "remote-root-unspecified:session-scope-only"
)

// NewRemoteService 创建一个空的 RemoteService。
func NewRemoteService() *RemoteService {
	r := &RemoteService{
		sessions:       make(map[string]*SSHSession),
		approvals:      make(map[string]remoteCommandApproval),
		approveCommand: nativeRemoteCommandApproval,
		now:            time.Now,
	}
	if _, err := crypto_rand.Read(r.approvalKey[:]); err == nil {
		r.approvalKeyGood = true
	} else {
		slog.Error("remote command approval key initialization failed", "err", err)
	}
	return r
}

func nativeRemoteCommandApproval(name string, argv []string) bool {
	app := application.Get()
	if app == nil {
		return false
	}
	result := make(chan bool, 1)
	dialog := app.Dialog.Question().SetTitle("Approve remote command").SetMessage(
		fmt.Sprintf("Session: %s\n\nCommand:\n%s", name, quoteRemoteCommand(argv)),
	)
	dialog.AddButton("Run").SetAsDefault().OnClick(func() { result <- true })
	dialog.AddButton("Cancel").SetAsCancel().OnClick(func() { result <- false })
	dialog.Show()
	select {
	case approved := <-result:
		return approved
	case <-time.After(5 * time.Minute):
		return false
	}
}

// Connect 建立到远程主机的 SSH 连接，并初始化 SFTP 子系统。
// name 是会话标识（通常是 host 或用户自定义名称）。
//
// 安全：
//   - 必须有 known_hosts 校验（KnownHostsPath 为空时拒绝连接，防止 MITM）
//   - 密码/密钥路径绝不写入日志
func (r *RemoteService) Connect(name string, config SSHConfig) error {
	if name == "" {
		return errors.New("session name is empty")
	}
	if err := validateSSHConfig(config); err != nil {
		return err
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return errors.New("remote service is shut down")
	}
	if _, exists := r.sessions[name]; exists {
		r.mu.Unlock()
		return fmt.Errorf("session %q already connected; disconnect first", name)
	}
	r.mu.Unlock()

	client, hostID, err := dialSSHWithHostIdentity(config)
	if err != nil {
		return err
	}
	sc, err := sftp.NewClient(client)
	if err != nil {
		_ = client.Close()
		return fmt.Errorf("open sftp session: %w", err)
	}
	fs := &SSHFileSystem{client: client, sftp: sc}
	nonce, err := newRemoteOpaqueID()
	if err != nil {
		_ = sc.Close()
		_ = client.Close()
		return fmt.Errorf("create remote host instance nonce: %w", err)
	}
	session := &SSHSession{
		client: client, sftp: sc, fs: fs, HostID: hostID,
		HostInstanceNonce: nonce, WorkspaceID: remoteWorkspaceID(remoteWorkspaceRootSentinel),
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		_ = sc.Close()
		_ = client.Close()
		return errors.New("remote service was shut down during connect")
	}
	// 二次检查：并发场景下另一 goroutine 可能已注册同名会话。
	if _, exists := r.sessions[name]; exists {
		_ = sc.Close()
		_ = client.Close()
		return fmt.Errorf("session %q already connected", name)
	}
	r.nextGeneration++
	session.generation = r.nextGeneration
	r.sessions[name] = session
	// 仅记录非敏感信息：host、user、port。绝不记录密码/密钥路径。
	slog.Info("remote ssh connected",
		"name", name,
		"host", config.Host,
		"port", config.Port,
		"user", config.User)
	return nil
}

// Disconnect 关闭指定会话。未连接的会话返回 nil（幂等）。
func (r *RemoteService) Disconnect(name string) error {
	r.mu.Lock()
	session, ok := r.sessions[name]
	if ok {
		delete(r.sessions, name)
		for token, approval := range r.approvals {
			if approval.scope == session.remoteScope() {
				delete(r.approvals, token)
			}
		}
	}
	r.mu.Unlock()
	if !ok {
		return nil
	}
	if session.sftp != nil {
		_ = session.sftp.Close()
	}
	if session.client != nil {
		_ = session.client.Close()
	}
	slog.Info("remote ssh disconnected", "name", name)
	return nil
}

func (s *SSHSession) remoteScope() RemoteScope {
	if s == nil {
		return RemoteScope{}
	}
	return RemoteScope{
		HostID: s.HostID, HostInstanceNonce: s.HostInstanceNonce,
		WorkspaceID: s.WorkspaceID, Generation: s.generation,
	}
}

// HostInfo returns a read-only snapshot and is not a Wails API.
//
//wails:ignore
func (r *RemoteService) HostInfo(name string) (RemoteHostInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, ok := r.sessions[name]
	if !ok {
		return RemoteHostInfo{}, fmt.Errorf("session %q not connected", name)
	}
	return RemoteHostInfo{HostID: session.HostID, HostInstanceNonce: session.HostInstanceNonce}, nil
}

// Scope returns a read-only authorization scope and is not a Wails API.
//
//wails:ignore
func (r *RemoteService) Scope(name string) (RemoteScope, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, ok := r.sessions[name]
	if !ok {
		return RemoteScope{}, fmt.Errorf("session %q not connected", name)
	}
	return session.remoteScope(), nil
}

// IsConnected 报告指定会话是否已连接。
func (r *RemoteService) IsConnected(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.sessions[name]
	return ok
}

// GetFileSystem 返回指定会话的 FileSystem 抽象。
// 调用方拿到 FileSystem 后可执行任意读写操作；会话关闭后再调用会返回错误。
func (r *RemoteService) GetFileSystem(name string) (FileSystem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, ok := r.sessions[name]
	if !ok {
		return nil, fmt.Errorf("session %q not connected", name)
	}
	return session.fs, nil
}

// RequestCommandApproval asks the backend-native approval UI to authorize one
// exact command on the current connection generation.
func (r *RemoteService) RequestCommandApproval(name string, argv []string) (string, error) {
	if err := validateRemoteCommandArgv(argv); err != nil {
		return "", err
	}
	commandHash, _ := remoteCommandAuditSummary(argv)

	r.mu.Lock()
	session, ok := r.sessions[name]
	approve := r.approveCommand
	r.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("session %q not connected", name)
	}
	if approve == nil || !approve(name, append([]string(nil), argv...)) {
		return "", fmt.Errorf("remote command was not approved: %w", ErrNotAllowed)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	current, ok := r.sessions[name]
	if !ok || current != session || current.remoteScope() != session.remoteScope() {
		return "", fmt.Errorf("session %q changed while awaiting approval: %w", name, ErrNotAllowed)
	}
	if !r.approvalKeyGood {
		return "", fmt.Errorf("remote command approval key is unavailable: %w", ErrNotAllowed)
	}
	now := r.currentTimeLocked()
	for token, approval := range r.approvals {
		if !approval.expiresAt.After(now) {
			delete(r.approvals, token)
		}
	}
	if len(r.approvals) >= remoteCommandApprovalLimit {
		return "", fmt.Errorf("too many pending remote command approvals: %w", ErrInvalidInput)
	}
	for attempts := 0; attempts < 4; attempts++ {
		token, err := newRemoteCommandApprovalToken()
		if err != nil {
			return "", err
		}
		if _, exists := r.approvals[token]; exists {
			continue
		}
		approval := remoteCommandApproval{
			session: name, commandHash: commandHash, scope: session.remoteScope(),
			expiresAt: now.Add(remoteCommandApprovalTTL),
		}
		approval.binding = r.bindRemoteCommandApproval(token, approval)
		r.approvals[token] = approval
		return token, nil
	}
	return "", fmt.Errorf("create unique remote command approval token: %w", ErrInvalidInput)
}

// ExecuteCommand consumes a backend-issued single-use approval token and runs
// only the session and argv to which that token was bound.
func (r *RemoteService) ExecuteCommand(name string, argv []string, approvalToken string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return r.executeCommandContext(ctx, name, argv, approvalToken)
}

// executeCommandContext bounds remote execution and allows callers to cancel
// a lost connection or a command waiting for input.
//
//wails:ignore
func (r *RemoteService) executeCommandContext(ctx context.Context, name string, argv []string, approvalToken string) (string, error) {
	commandHash, commandBytes := remoteCommandAuditSummary(argv)
	if err := validateRemoteCommandArgv(argv); err != nil {
		securityAudit("remote.command.failure", "failure",
			"session", name, "command_sha256", commandHash, "command_bytes", commandBytes,
			"argc", len(argv), "failure_stage", "validation")
		return "", err
	}
	r.mu.Lock()
	session, ok := r.sessions[name]
	approval, approved := r.approvals[approvalToken]
	if approved {
		delete(r.approvals, approvalToken)
	}
	r.mu.Unlock()
	if !ok {
		securityAudit("remote.command.failure", "failure",
			"session", name, "command_sha256", commandHash, "command_bytes", commandBytes,
			"argc", len(argv), "failure_stage", "session_lookup")
		if approved {
			return "", fmt.Errorf("approved remote session is no longer connected: %w", ErrNotAllowed)
		}
		return "", fmt.Errorf("session %q not connected", name)
	}
	if !approved || !isCanonicalRemoteCommandApprovalToken(approvalToken) ||
		!r.remoteCommandApprovalBindingValid(approvalToken, approval) ||
		approval.session != name || approval.commandHash != commandHash ||
		approval.scope != session.remoteScope() || !approval.expiresAt.After(r.currentTime()) {
		securityAudit("remote.command.failure", "failure",
			"session", name, "command_sha256", commandHash, "command_bytes", commandBytes,
			"argc", len(argv), "failure_stage", "approval")
		return "", fmt.Errorf("invalid, expired, or replayed remote command approval: %w", ErrNotAllowed)
	}
	securityAudit("remote.command.start", "started",
		"session", name, "command_sha256", commandHash, "command_bytes", commandBytes, "argc", len(argv))
	sess, err := session.client.NewSession()
	if err != nil {
		securityAudit("remote.command.failure", "failure",
			"session", name, "command_sha256", commandHash, "command_bytes", commandBytes,
			"argc", len(argv), "failure_stage", "session_open")
		return "", fmt.Errorf("open ssh session: %w", err)
	}
	defer sess.Close()
	var stdout, stderr bytes.Buffer
	sess.Stdout = &stdout
	sess.Stderr = &stderr
	done := make(chan error, 1)
	go func() { done <- sess.Run(quoteRemoteCommand(argv)) }()
	select {
	case err := <-done:
		output := stdout.String() + stderr.String()
		if err != nil {
			securityAudit("remote.command.failure", "failure",
				"session", name, "command_sha256", commandHash, "command_bytes", commandBytes,
				"argc", len(argv), "output_bytes", len(output), "failure_stage", "execution")
			return output, fmt.Errorf("run command: %w", err)
		}
		securityAudit("remote.command.result", "success",
			"session", name, "command_sha256", commandHash, "command_bytes", commandBytes,
			"argc", len(argv), "output_bytes", len(output))
		return output, nil
	case <-ctx.Done():
		_ = sess.Signal(ssh.SIGKILL)
		_ = sess.Close()
		<-done
		output := stdout.String() + stderr.String()
		securityAudit("remote.command.cancel", "cancelled",
			"session", name, "command_sha256", commandHash, "command_bytes", commandBytes,
			"argc", len(argv), "output_bytes", len(output))
		return output, fmt.Errorf("run command: %w", ctx.Err())
	}
}

func newRemoteCommandApprovalToken() (string, error) {
	var raw [32]byte
	if _, err := crypto_rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("create remote command approval token: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func isCanonicalRemoteCommandApprovalToken(token string) bool {
	if len(token) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(token)
	return err == nil && hex.EncodeToString(decoded) == token
}

type remoteCommandApprovalBinding struct {
	Token             string `json:"token"`
	Session           string `json:"session"`
	CommandHash       string `json:"commandHash"`
	HostID            string `json:"hostID"`
	HostInstanceNonce string `json:"hostInstanceNonce"`
	WorkspaceID       string `json:"workspaceID"`
	Generation        uint64 `json:"generation"`
	ExpiresAtUnixNano int64  `json:"expiresAtUnixNano"`
}

func (r *RemoteService) bindRemoteCommandApproval(token string, approval remoteCommandApproval) [sha256.Size]byte {
	payload, _ := json.Marshal(remoteCommandApprovalBinding{
		Token: token, Session: approval.session, CommandHash: approval.commandHash,
		HostID: approval.scope.HostID, HostInstanceNonce: approval.scope.HostInstanceNonce,
		WorkspaceID: approval.scope.WorkspaceID, Generation: approval.scope.Generation,
		ExpiresAtUnixNano: approval.expiresAt.UnixNano(),
	})
	mac := hmac.New(sha256.New, r.approvalKey[:])
	_, _ = mac.Write(payload)
	var binding [sha256.Size]byte
	copy(binding[:], mac.Sum(nil))
	return binding
}

func (r *RemoteService) remoteCommandApprovalBindingValid(token string, approval remoteCommandApproval) bool {
	if !r.approvalKeyGood {
		return false
	}
	expected := r.bindRemoteCommandApproval(token, approval)
	return hmac.Equal(approval.binding[:], expected[:])
}

func (r *RemoteService) currentTime() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.currentTimeLocked()
}

func (r *RemoteService) currentTimeLocked() time.Time {
	if r.now == nil {
		return time.Now()
	}
	return r.now()
}

func remoteCommandAuditSummary(argv []string) (string, int) {
	hash := sha256.New()
	totalBytes := 0
	for _, arg := range argv {
		totalBytes += len(arg)
		_, _ = hash.Write([]byte{byte(len(arg) >> 24), byte(len(arg) >> 16), byte(len(arg) >> 8), byte(len(arg))})
		_, _ = hash.Write([]byte(arg))
	}
	return hex.EncodeToString(hash.Sum(nil)), totalBytes
}

func validateRemoteCommandArgv(argv []string) error {
	if len(argv) == 0 {
		return errors.New("remote command blocked: argv is empty")
	}
	if strings.TrimSpace(argv[0]) == "" {
		return errors.New("remote command blocked: argv[0] is empty")
	}
	for i, arg := range argv {
		if strings.ContainsAny(arg, "\x00\r\n") {
			return fmt.Errorf("remote command blocked: argv[%d] contains a control character", i)
		}
		for _, syntax := range []string{";", "|", "&&", "`", "$("} {
			if strings.Contains(arg, syntax) {
				return fmt.Errorf("remote command blocked: argv[%d] contains shell syntax %q", i, syntax)
			}
		}
	}
	return nil
}

func quoteRemoteCommand(argv []string) string {
	quoted := make([]string, len(argv))
	for i, arg := range argv {
		quoted[i] = "'" + strings.ReplaceAll(arg, "'", "'\\''") + "'"
	}
	return strings.Join(quoted, " ")
}

// ListConnections 返回所有已连接会话的名称（按字母序）。
func (r *RemoteService) ListConnections() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	names := make([]string, 0, len(r.sessions))
	for name := range r.sessions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// DisconnectAll 关闭全部会话。供应用关闭时调用。
func (r *RemoteService) DisconnectAll() {
	r.mu.Lock()
	names := make([]string, 0, len(r.sessions))
	for name := range r.sessions {
		names = append(names, name)
	}
	r.mu.Unlock()
	for _, name := range names {
		_ = r.Disconnect(name)
	}
}

// Close prevents new SSH sessions and closes all currently owned sessions.
func (r *RemoteService) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	sessions := r.sessions
	r.sessions = make(map[string]*SSHSession)
	r.approvals = make(map[string]remoteCommandApproval)
	r.mu.Unlock()

	var errs []error
	for _, session := range sessions {
		if session.sftp != nil {
			if err := session.sftp.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		if session.client != nil {
			if err := session.client.Close(); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// ============================================================================
// SSH 连接辅助
// ============================================================================

// validateSSHConfig 校验 SSH 配置基本完整性。
//   - Host 必须非空
//   - Port 必须在 1..65535
//   - User 必须非空
//   - KeyPath 与 Password 至少有一个
func validateSSHConfig(c SSHConfig) error {
	if strings.TrimSpace(c.Host) == "" {
		return errors.New("ssh config: host is empty")
	}
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("ssh config: invalid port %d (must be 1..65535)", c.Port)
	}
	if strings.TrimSpace(c.User) == "" {
		return errors.New("ssh config: user is empty")
	}
	if c.KeyPath == "" && c.Password == "" {
		return errors.New("ssh config: must provide keyPath or password")
	}
	return nil
}

// dialSSH 根据 SSHConfig 建立 SSH 连接。
//
// 认证策略：
//   - 优先 KeyPath（私钥文件）
//   - 其次 Password
//   - 二者都不可用返回错误（已在 validateSSHConfig 拦截）
//
// known_hosts 校验：KnownHostsPath 必须非空，并加载该文件启用严格校验。
//
// 安全：本函数不打印任何敏感字段。
func dialSSH(config SSHConfig) (*ssh.Client, error) {
	client, _, err := dialSSHWithHostIdentity(config)
	return client, err
}

func dialSSHWithHostIdentity(config SSHConfig) (*ssh.Client, string, error) {
	authMethods, err := buildAuthMethods(config)
	if err != nil {
		return nil, "", err
	}
	hostKeyCallback, err := buildHostKeyCallback(config.KnownHostsPath)
	if err != nil {
		return nil, "", err
	}
	var hostID string
	verifiedHostKeyCallback := func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		if err := hostKeyCallback(hostname, remote, key); err != nil {
			return err
		}
		hostID = remoteHostID(key)
		return nil
	}
	addr := fmt.Sprintf("%s:%d", config.Host, config.Port)
	clientConfig := &ssh.ClientConfig{
		User:            config.User,
		Auth:            authMethods,
		HostKeyCallback: verifiedHostKeyCallback,
		Timeout:         15 * time.Second,
	}
	client, err := ssh.Dial("tcp", addr, clientConfig)
	if err != nil {
		return nil, "", fmt.Errorf("ssh dial %s: %w", addr, err)
	}
	if hostID == "" {
		_ = client.Close()
		return nil, "", errors.New("ssh host identity was not captured after verification")
	}
	return client, hostID, nil
}

func remoteHostID(key ssh.PublicKey) string {
	digest := sha256.Sum256(key.Marshal())
	return hex.EncodeToString(digest[:])
}

func remoteWorkspaceID(canonicalRemoteRoot string) string {
	digest := sha256.Sum256([]byte("remote-workspace-root-v1\x00" + canonicalRemoteRoot))
	return hex.EncodeToString(digest[:])
}

func newRemoteOpaqueID() (string, error) {
	var raw [32]byte
	if _, err := crypto_rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

// buildAuthMethods 构造认证方法列表。私钥优先，密码次之。
func buildAuthMethods(config SSHConfig) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod
	if config.KeyPath != "" {
		keyBytes, err := os.ReadFile(config.KeyPath)
		if err != nil {
			return nil, fmt.Errorf("read ssh key: %w", err)
		}
		signer, err := ssh.ParsePrivateKey(keyBytes)
		if err != nil {
			return nil, fmt.Errorf("parse ssh key: %w", err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if config.Password != "" {
		methods = append(methods, ssh.Password(config.Password))
	}
	if len(methods) == 0 {
		return nil, errors.New("no ssh auth method available")
	}
	return methods, nil
}

// buildHostKeyCallback 构造 host key 校验回调。
//   - knownHostsPath 非空：加载并严格校验
//   - knownHostsPath 为空：拒绝连接
func buildHostKeyCallback(knownHostsPath string) (ssh.HostKeyCallback, error) {
	knownHostsPath = strings.TrimSpace(knownHostsPath)
	if knownHostsPath == "" {
		return nil, errors.New("known_hosts path is empty")
	}
	cb, err := knownhosts.New(knownHostsPath)
	if err != nil {
		return nil, fmt.Errorf("load known_hosts: %w", err)
	}
	return cb, nil
}
