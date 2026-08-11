package services

// F-9 (prompt-2.md 第 537-586 行): 远程开发 SSH 最小版本的测试。
//
// 测试覆盖：
//   - LocalFileSystem: ReadWrite / ListDirectory / Stat / DeletePath / RenamePath
//   - SSHConfig 校验: 空 Host、无效 Port、空 User、缺少认证
//   - RemoteService: 无效主机连接失败、断开未连接会话、空连接列表
//
// 注意：完整的 SSH 连接测试需要 mock SSH server；task-4.md 要求通过
// skipIfNoSSHServer 跳过此类测试。本测试集仅做配置校验与失败路径测试，
// 不依赖外部 SSH 服务器，可在 CI 中无网络环境下稳定运行。

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/binary"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// skipIfNoSSHServer 跳过依赖真实 SSH 服务器的测试。
// 当前测试集不使用此 helper（所有测试均为离线测试），保留以便未来扩展。
func skipIfNoSSHServer(t *testing.T) {
	t.Helper()
	if os.Getenv("KOYORI_IDE_SSH_TEST_HOST") == "" {
		t.Skip("skipping SSH server test; set KOYORI_IDE_SSH_TEST_HOST to enable")
	}
}

func TestLocalFileSystem_WatchCancellationClosesChannel(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	events, err := NewLocalFileSystem().Watch(ctx, dir)
	if err != nil {
		cancel()
		t.Fatalf("watch directory: %v", err)
	}
	cancel()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case _, open := <-events:
			if !open {
				return
			}
		case <-timer.C:
			t.Fatal("watch channel did not close after cancellation")
		}
	}
}

func TestSendWatchEventCancellationUnblocksFullChannel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan FileEvent, 1)
	events <- FileEvent{Type: "create", Path: "already-full"}
	result := make(chan bool, 1)
	go func() {
		result <- sendWatchEvent(ctx, events, FileEvent{Type: "create", Path: "blocked"})
	}()

	cancel()
	select {
	case sent := <-result:
		if sent {
			t.Fatal("event send succeeded after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("event send remained blocked after cancellation")
	}
}

// ============================================================================
// LocalFileSystem 测试
// ============================================================================

// TestLocalFileSystem_ReadWrite 验证本地文件系统的读写往返。
func TestLocalFileSystem_ReadWrite(t *testing.T) {
	tmp := t.TempDir()
	fs := NewLocalFileSystem()

	path := filepath.Join(tmp, "sub", "file.txt")
	want := []byte("hello remote world")
	if err := fs.WriteFile(path, want); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	got, err := fs.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("content mismatch: got %q, want %q", got, want)
	}

	// 覆盖写入应截断旧内容。
	want2 := []byte("short")
	if err := fs.WriteFile(path, want2); err != nil {
		t.Fatalf("WriteFile (overwrite) failed: %v", err)
	}
	got2, err := fs.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile (overwrite) failed: %v", err)
	}
	if string(got2) != string(want2) {
		t.Fatalf("overwrite mismatch: got %q, want %q", got2, want2)
	}
}

// TestLocalFileSystem_ListDirectory 验证列目录返回正确条目且排序符合预期
// （目录在前，按名字字母序）。
func TestLocalFileSystem_ListDirectory(t *testing.T) {
	tmp := t.TempDir()
	fs := NewLocalFileSystem()

	// 准备：tmp/
	//          ├── aaa.txt   (file)
	//          ├── zdir/     (dir)
	//          └── mmm.txt   (file)
	if err := fs.CreateFile(filepath.Join(tmp, "aaa.txt")); err != nil {
		t.Fatalf("create aaa.txt: %v", err)
	}
	if err := fs.CreateFile(filepath.Join(tmp, "mmm.txt")); err != nil {
		t.Fatalf("create mmm.txt: %v", err)
	}
	if err := fs.CreateDirectory(filepath.Join(tmp, "zdir")); err != nil {
		t.Fatalf("create zdir: %v", err)
	}

	entries, err := fs.ListDirectory(tmp)
	if err != nil {
		t.Fatalf("ListDirectory failed: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d: %+v", len(entries), entries)
	}

	// 验证目录在前。
	if !entries[0].IsDir {
		t.Fatalf("expected first entry to be a directory, got file %q", entries[0].Name)
	}
	// 验证其余按名字排序。
	names := []string{entries[1].Name, entries[2].Name}
	if !sort.StringsAreSorted(names) {
		t.Fatalf("file names not sorted: %v", names)
	}

	// 验证每个条目的 Path 字段是绝对路径且包含 Name。
	for _, e := range entries {
		if !strings.HasSuffix(e.Path, e.Name) {
			t.Fatalf("entry path %q does not end with name %q", e.Path, e.Name)
		}
	}
}

// TestLocalFileSystem_Stat 验证 Stat 返回的元信息。
func TestLocalFileSystem_Stat(t *testing.T) {
	tmp := t.TempDir()
	fs := NewLocalFileSystem()

	filePath := filepath.Join(tmp, "stat.txt")
	if err := fs.WriteFile(filePath, []byte("stat-content")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	info, err := fs.Stat(filePath)
	if err != nil {
		t.Fatalf("Stat(file) failed: %v", err)
	}
	if info.IsDir {
		t.Fatalf("file reported as dir")
	}
	if info.Size != int64(len("stat-content")) {
		t.Fatalf("size mismatch: got %d, want %d", info.Size, len("stat-content"))
	}
	if info.Modified == 0 {
		t.Fatalf("modified timestamp is zero")
	}

	dirInfo, err := fs.Stat(tmp)
	if err != nil {
		t.Fatalf("Stat(dir) failed: %v", err)
	}
	if !dirInfo.IsDir {
		t.Fatalf("dir reported as file")
	}
}

// TestLocalFileSystem_DeletePath 验证删除文件和递归删除目录。
func TestLocalFileSystem_DeletePath(t *testing.T) {
	tmp := t.TempDir()
	fs := NewLocalFileSystem()

	// 删除文件。
	filePath := filepath.Join(tmp, "to-delete.txt")
	if err := fs.WriteFile(filePath, []byte("x")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := fs.DeletePath(filePath); err != nil {
		t.Fatalf("DeletePath(file) failed: %v", err)
	}
	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Fatalf("file still exists after delete: %v", err)
	}

	// 递归删除非空目录。
	dirPath := filepath.Join(tmp, "to-delete-dir")
	if err := fs.CreateDirectory(dirPath); err != nil {
		t.Fatalf("CreateDirectory: %v", err)
	}
	if err := fs.WriteFile(filepath.Join(dirPath, "nested.txt"), []byte("x")); err != nil {
		t.Fatalf("write nested: %v", err)
	}
	if err := fs.DeletePath(dirPath); err != nil {
		t.Fatalf("DeletePath(dir) failed: %v", err)
	}
	if _, err := os.Stat(dirPath); !os.IsNotExist(err) {
		t.Fatalf("dir still exists after delete: %v", err)
	}
}

// TestLocalFileSystem_RenamePath 验证重命名文件和目录。
func TestLocalFileSystem_RenamePath(t *testing.T) {
	tmp := t.TempDir()
	fs := NewLocalFileSystem()

	// 重命名文件。
	oldPath := filepath.Join(tmp, "old.txt")
	newPath := filepath.Join(tmp, "new.txt")
	if err := fs.WriteFile(oldPath, []byte("rename-me")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := fs.RenamePath(oldPath, newPath); err != nil {
		t.Fatalf("RenamePath(file) failed: %v", err)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("old file still exists after rename")
	}
	got, err := fs.ReadFile(newPath)
	if err != nil {
		t.Fatalf("read renamed file: %v", err)
	}
	if string(got) != "rename-me" {
		t.Fatalf("content mismatch after rename: %q", got)
	}

	// 重命名目录。
	oldDir := filepath.Join(tmp, "old-dir")
	newDir := filepath.Join(tmp, "new-dir")
	if err := fs.CreateDirectory(oldDir); err != nil {
		t.Fatalf("CreateDirectory: %v", err)
	}
	if err := fs.WriteFile(filepath.Join(oldDir, "inner.txt"), []byte("x")); err != nil {
		t.Fatalf("write inner: %v", err)
	}
	if err := fs.RenamePath(oldDir, newDir); err != nil {
		t.Fatalf("RenamePath(dir) failed: %v", err)
	}
	inner, err := fs.ReadFile(filepath.Join(newDir, "inner.txt"))
	if err != nil {
		t.Fatalf("read inner after dir rename: %v", err)
	}
	if string(inner) != "x" {
		t.Fatalf("inner content mismatch: %q", inner)
	}
}

// ============================================================================
// SSHConfig 校验测试
// ============================================================================

// TestSSHConfig_Validation 验证 SSHConfig 校验逻辑覆盖所有边界：
// 空 Host、无效 Port、空 User、缺少认证方法、合法配置。
func TestSSHConfig_Validation(t *testing.T) {
	cases := []struct {
		name    string
		config  SSHConfig
		wantErr string // 期望的错误子串；为空表示期望成功
	}{
		{
			name: "empty host",
			config: SSHConfig{
				Host:     "",
				Port:     22,
				User:     "u",
				Password: "p",
			},
			wantErr: "host is empty",
		},
		{
			name: "whitespace host",
			config: SSHConfig{
				Host:     "   ",
				Port:     22,
				User:     "u",
				Password: "p",
			},
			wantErr: "host is empty",
		},
		{
			name: "port zero",
			config: SSHConfig{
				Host:     "h",
				Port:     0,
				User:     "u",
				Password: "p",
			},
			wantErr: "invalid port",
		},
		{
			name: "port too large",
			config: SSHConfig{
				Host:     "h",
				Port:     70000,
				User:     "u",
				Password: "p",
			},
			wantErr: "invalid port",
		},
		{
			name: "negative port",
			config: SSHConfig{
				Host:     "h",
				Port:     -1,
				User:     "u",
				Password: "p",
			},
			wantErr: "invalid port",
		},
		{
			name: "empty user",
			config: SSHConfig{
				Host:     "h",
				Port:     22,
				User:     "",
				Password: "p",
			},
			wantErr: "user is empty",
		},
		{
			name: "no auth method",
			config: SSHConfig{
				Host: "h",
				Port: 22,
				User: "u",
			},
			wantErr: "keyPath or password",
		},
		{
			name: "valid with password",
			config: SSHConfig{
				Host:     "h",
				Port:     22,
				User:     "u",
				Password: "p",
			},
			wantErr: "",
		},
		{
			name: "valid with keypath",
			config: SSHConfig{
				Host:    "h",
				Port:    22,
				User:    "u",
				KeyPath: "/tmp/key",
			},
			wantErr: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSSHConfig(tc.config)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected success, got error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// ============================================================================
// RemoteService 测试
// ============================================================================

// TestRemoteService_Connect_InvalidHost 验证连接到无效主机时返回错误。
// 使用一个保证不可达的地址（RFC 5737 TEST-NET 地址 192.0.2.x）。
func TestRemoteService_Connect_InvalidHost(t *testing.T) {
	svc := NewRemoteService()
	knownHostsPath := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(knownHostsPath, nil, 0600); err != nil {
		t.Fatalf("write temporary known_hosts: %v", err)
	}
	config := SSHConfig{
		Host:           "192.0.2.1", // RFC 5737 TEST-NET，保证不可达
		Port:           22,
		User:           "test",
		Password:       "test",
		KnownHostsPath: knownHostsPath,
	}
	err := svc.Connect("invalid", config)
	if err == nil {
		// 即使意外连接成功，也要清理。
		_ = svc.Disconnect("invalid")
		t.Fatalf("expected Connect to fail for invalid host, got nil")
	}
	if svc.IsConnected("invalid") {
		t.Fatalf("session should not be marked connected after failed Connect")
	}
}

func TestBuildHostKeyCallback_RejectsEmptyKnownHosts(t *testing.T) {
	for _, path := range []string{"", " \t\r\n "} {
		if _, err := buildHostKeyCallback(path); err == nil {
			t.Errorf("buildHostKeyCallback(%q) expected an error", path)
		}
	}
}

// TestRemoteService_Disconnect_NotConnected 验证 Disconnect 对未连接会话幂等。
func TestRemoteService_Disconnect_NotConnected(t *testing.T) {
	svc := NewRemoteService()
	// 断开一个从未连接的会话应返回 nil（幂等）。
	if err := svc.Disconnect("nope"); err != nil {
		t.Fatalf("Disconnect on not-connected session should be nil, got %v", err)
	}
}

// TestRemoteService_ListConnections_Empty 验证空 RemoteService 的连接列表为空切片。
func TestRemoteService_ListConnections_Empty(t *testing.T) {
	svc := NewRemoteService()
	got := svc.ListConnections()
	if got == nil {
		t.Fatalf("ListConnections should return non-nil empty slice, got nil")
	}
	if len(got) != 0 {
		t.Fatalf("expected empty list, got %v", got)
	}
}

// TestRemoteService_Connect_InvalidConfig 验证无效配置在 dial 之前被拦截。
func TestRemoteService_Connect_InvalidConfig(t *testing.T) {
	svc := NewRemoteService()
	// 空 Host：validateSSHConfig 在 dial 之前返回错误。
	if err := svc.Connect("bad", SSHConfig{}); err == nil {
		t.Fatalf("expected error for empty config")
	}
	if svc.IsConnected("bad") {
		t.Fatalf("session should not be connected after invalid config")
	}
}

// TestRemoteService_Connect_DuplicateName 验证同名会话不能重复连接。
//
// 由于 Connect 包含真实 SSH 拨号，本测试通过预填充 sessions map 模拟
// 已连接状态，绕过网络层，仅验证幂等性检查逻辑。
func TestRemoteService_Connect_DuplicateName(t *testing.T) {
	svc := NewRemoteService()
	// 直接预填充 sessions 模拟已连接状态。
	svc.mu.Lock()
	svc.sessions["dup"] = &SSHSession{}
	svc.mu.Unlock()

	err := svc.Connect("dup", SSHConfig{
		Host:     "h",
		Port:     22,
		User:     "u",
		Password: "p",
	})
	if err == nil {
		t.Fatalf("expected error when connecting with duplicate name")
	}
	if !strings.Contains(err.Error(), "already connected") {
		t.Fatalf("expected 'already connected' error, got: %v", err)
	}
}

// TestRemoteService_GetFileSystem_NotConnected 验证从未连接会话获取 FileSystem 返回错误。
func TestRemoteService_GetFileSystem_NotConnected(t *testing.T) {
	svc := NewRemoteService()
	_, err := svc.GetFileSystem("missing")
	if err == nil {
		t.Fatalf("expected error for GetFileSystem on missing session")
	}
	if !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("expected 'not connected' error, got: %v", err)
	}
}

// TestRemoteService_ExecuteCommand_NotConnected 验证在未连接会话上执行命令返回错误。
func TestRemoteService_ExecuteCommand_NotConnected(t *testing.T) {
	svc := NewRemoteService()
	_, err := svc.ExecuteCommand("missing", []string{"ls"}, "")
	if err == nil {
		t.Fatalf("expected error for ExecuteCommand on missing session")
	}
	if !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("expected 'not connected' error, got: %v", err)
	}
}

func TestRemoteService_CommandApprovalFailClosed(t *testing.T) {
	svc := NewRemoteService()
	svc.sessions["session"] = &SSHSession{generation: 7}
	svc.approveCommand = nil

	if _, err := svc.RequestCommandApproval("session", []string{"ls"}); err == nil {
		t.Fatal("headless backend approved a remote command")
	}
	if len(svc.approvals) != 0 {
		t.Fatal("denied approval left a usable token")
	}
}

func TestRemoteService_CommandApprovalBindsPayloadAndIsSingleUse(t *testing.T) {
	svc := NewRemoteService()
	svc.sessions["session"] = &SSHSession{generation: 7}
	svc.approveCommand = func(string, []string) bool { return true }

	token, err := svc.RequestCommandApproval("session", []string{"ls", "-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.executeCommandContext(context.Background(), "session", []string{"ls", "-a"}, token); err == nil {
		t.Fatal("approval token authorized different argv")
	}
	if _, err := svc.executeCommandContext(context.Background(), "session", []string{"ls", "-1"}, token); err == nil {
		t.Fatal("consumed approval token was replayable")
	}
}

func TestRemoteService_CommandApprovalBindsGenerationAndTTL(t *testing.T) {
	now := time.Now()
	svc := NewRemoteService()
	svc.now = func() time.Time { return now }
	svc.sessions["session"] = &SSHSession{generation: 7}
	svc.approveCommand = func(string, []string) bool { return true }

	token, err := svc.RequestCommandApproval("session", []string{"ls"})
	if err != nil {
		t.Fatal(err)
	}
	svc.sessions["session"] = &SSHSession{generation: 8}
	if _, err := svc.executeCommandContext(context.Background(), "session", []string{"ls"}, token); err == nil {
		t.Fatal("approval token survived a connection generation change")
	}

	svc.sessions["session"] = &SSHSession{generation: 9}
	token, err = svc.RequestCommandApproval("session", []string{"pwd"})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(remoteCommandApprovalTTL)
	if _, err := svc.executeCommandContext(context.Background(), "session", []string{"pwd"}, token); err == nil {
		t.Fatal("expired approval token was accepted")
	}
}

func TestRemoteService_CommandSecurityAudit(t *testing.T) {
	logs := captureSecurityAudit(t)
	client, cleanup := newAuditTestSSHClient(t)
	defer cleanup()

	svc := NewRemoteService()
	svc.sessions["audit-session"] = &SSHSession{client: client}
	svc.approveCommand = func(string, []string) bool { return true }

	successArgv := []string{"audit-success-secret"}
	successToken, err := svc.RequestCommandApproval("audit-session", successArgv)
	if err != nil {
		t.Fatal(err)
	}
	if output, err := svc.executeCommandContext(context.Background(), "audit-session", successArgv, successToken); err != nil || output != "success-output-secret" {
		t.Fatalf("successful command output=%q err=%v", output, err)
	}
	failureArgv := []string{"audit-failure-secret"}
	failureToken, err := svc.RequestCommandApproval("audit-session", failureArgv)
	if err != nil {
		t.Fatal(err)
	}
	if output, err := svc.executeCommandContext(context.Background(), "audit-session", failureArgv, failureToken); err == nil || output != "failure-output-secret" {
		t.Fatalf("failed command output=%q err=%v", output, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelArgv := []string{"audit-cancel-secret"}
	cancelToken, err := svc.RequestCommandApproval("audit-session", cancelArgv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.executeCommandContext(ctx, "audit-session", cancelArgv, cancelToken); err == nil {
		t.Fatal("cancelled command returned no error")
	}

	text := logs.String()
	for _, event := range []string{"remote.command.start", "remote.command.result", "remote.command.failure", "remote.command.cancel"} {
		if !strings.Contains(text, `"event":"`+event+`"`) {
			t.Errorf("missing audit event %q in logs: %s", event, text)
		}
	}
	for _, sensitive := range []string{
		"audit-success-secret", "audit-failure-secret", "audit-cancel-secret",
		"success-output-secret", "failure-output-secret",
	} {
		if strings.Contains(text, sensitive) {
			t.Errorf("security audit leaked sensitive command data %q: %s", sensitive, text)
		}
	}
	if !strings.Contains(text, `"command_sha256":`) || !strings.Contains(text, `"output_bytes":`) {
		t.Errorf("audit lacks command digest or output length: %s", text)
	}
}

func newAuditTestSSHClient(t *testing.T) (*ssh.Client, func()) {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serverConfig := &ssh.ServerConfig{NoClientAuth: true}
	serverConfig.AddHostKey(signer)
	serverDone := make(chan struct{})
	go func() {
		var sessionWG sync.WaitGroup
		defer func() {
			sessionWG.Wait()
			close(serverDone)
		}()
		serverConn, err := listener.Accept()
		if err != nil {
			return
		}
		defer serverConn.Close()
		conn, channels, requests, err := ssh.NewServerConn(serverConn, serverConfig)
		if err != nil {
			return
		}
		defer conn.Close()
		go ssh.DiscardRequests(requests)
		for incoming := range channels {
			if incoming.ChannelType() != "session" {
				_ = incoming.Reject(ssh.UnknownChannelType, "session required")
				continue
			}
			channel, channelRequests, err := incoming.Accept()
			if err != nil {
				continue
			}
			sessionWG.Add(1)
			go func() {
				defer sessionWG.Done()
				serveAuditTestSSHSession(channel, channelRequests)
			}()
		}
	}()

	clientConn, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		_ = listener.Close()
		<-serverDone
		t.Fatal(err)
	}
	connection, channels, requests, err := ssh.NewClientConn(clientConn, "audit-test", &ssh.ClientConfig{
		User:            "audit",
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // In-memory test transport only.
	})
	if err != nil {
		_ = clientConn.Close()
		_ = listener.Close()
		<-serverDone
		t.Fatal(err)
	}
	client := ssh.NewClient(connection, channels, requests)
	return client, func() {
		_ = client.Close()
		_ = listener.Close()
		select {
		case <-serverDone:
		case <-time.After(time.Second):
			t.Error("audit SSH server goroutine did not exit")
		}
	}
}

func serveAuditTestSSHSession(channel ssh.Channel, requests <-chan *ssh.Request) {
	defer channel.Close()
	for request := range requests {
		switch request.Type {
		case "exec":
			_ = request.Reply(true, nil)
			command := string(request.Payload[4:])
			switch {
			case strings.Contains(command, "audit-success-secret"):
				_, _ = io.WriteString(channel, "success-output-secret")
				_ = channel.CloseWrite()
				sendAuditTestExitStatus(channel, 0)
				return
			case strings.Contains(command, "audit-failure-secret"):
				_, _ = io.WriteString(channel, "failure-output-secret")
				_ = channel.CloseWrite()
				sendAuditTestExitStatus(channel, 1)
				return
			}
		case "signal":
			_ = request.Reply(true, nil)
			return
		default:
			_ = request.Reply(false, nil)
		}
	}
}

func sendAuditTestExitStatus(channel ssh.Channel, status uint32) {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, status)
	_, _ = channel.SendRequest("exit-status", false, payload)
}

// TestRemoteService_ExecuteCommand_EmptyArgv 验证空 argv 被拒绝。
func TestRemoteService_ExecuteCommand_EmptyArgv(t *testing.T) {
	svc := NewRemoteService()
	// 预填充 sessions 模拟已连接（避免触发真实拨号）。
	svc.mu.Lock()
	svc.sessions["conn"] = &SSHSession{}
	svc.mu.Unlock()
	defer svc.Disconnect("conn")

	_, err := svc.ExecuteCommand("conn", nil, "")
	if err == nil {
		t.Fatalf("expected error for empty argv")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Fatalf("expected 'empty' error, got: %v", err)
	}
}

func TestExecuteCommand_RejectsInvalidArgv(t *testing.T) {
	tests := []struct {
		name    string
		argv    []string
		wantErr string
	}{
		{name: "empty argv", argv: nil, wantErr: "argv is empty"},
		{name: "empty executable", argv: []string{""}, wantErr: "argv[0] is empty"},
		{name: "nul", argv: []string{"printf", "bad\x00arg"}, wantErr: "control character"},
		{name: "carriage return", argv: []string{"printf", "bad\rarg"}, wantErr: "control character"},
		{name: "line feed", argv: []string{"printf", "bad\narg"}, wantErr: "control character"},
	}

	svc := NewRemoteService()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.ExecuteCommand("missing", tt.argv, "")
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ExecuteCommand(%q) error = %v, want error containing %q", tt.argv, err, tt.wantErr)
			}
			if strings.Contains(err.Error(), "not connected") {
				t.Fatalf("argv validation must happen before session lookup: %v", err)
			}
		})
	}
}

func TestExecuteCommand_RejectsShellSyntax(t *testing.T) {
	tests := []struct {
		name string
		arg  string
	}{
		{name: "semicolon", arg: "one;two"},
		{name: "pipe", arg: "one|two"},
		{name: "and", arg: "one&&two"},
		{name: "backtick", arg: "`whoami`"},
		{name: "command substitution", arg: "$(whoami)"},
	}

	svc := NewRemoteService()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.ExecuteCommand("missing", []string{"printf", "%s", tt.arg}, "")
			if err == nil || !strings.Contains(err.Error(), "shell syntax") {
				t.Fatalf("ExecuteCommand(%q) error = %v, want shell syntax error", tt.arg, err)
			}
			if strings.Contains(err.Error(), "not connected") {
				t.Fatalf("shell syntax validation must happen before session lookup: %v", err)
			}
		})
	}
}

func TestRemoteCommand_QuotesArgv(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want string
	}{
		{name: "ordinary", argv: []string{"git", "status"}, want: "'git' 'status'"},
		{name: "spaces preserve boundary", argv: []string{"printf", "%s", "two words"}, want: "'printf' '%s' 'two words'"},
		{name: "single quote", argv: []string{"printf", "%s", "it's"}, want: "'printf' '%s' 'it'\\''s'"},
		{name: "leading hyphen", argv: []string{"ls", "-1", "--", "/tmp"}, want: "'ls' '-1' '--' '/tmp'"},
		{name: "empty argument", argv: []string{"printf", "%s", ""}, want: "'printf' '%s' ''"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := quoteRemoteCommand(tt.argv); got != tt.want {
				t.Fatalf("quoteRemoteCommand(%q) = %q, want %q", tt.argv, got, tt.want)
			}
		})
	}
}

// TestRemoteService_DisconnectAll 验证批量断开会话。
func TestRemoteService_DisconnectAll(t *testing.T) {
	svc := NewRemoteService()
	// 预填充多个会话（模拟已连接状态）。
	svc.mu.Lock()
	svc.sessions["a"] = &SSHSession{}
	svc.sessions["b"] = &SSHSession{}
	svc.mu.Unlock()

	svc.DisconnectAll()
	if got := svc.ListConnections(); len(got) != 0 {
		t.Fatalf("expected empty after DisconnectAll, got %v", got)
	}
}

func TestRemoteService_DisconnectIsolatesNamedSession(t *testing.T) {
	svc := NewRemoteService()
	svc.mu.Lock()
	svc.sessions["keep"] = &SSHSession{}
	svc.sessions["remove"] = &SSHSession{}
	svc.mu.Unlock()

	if err := svc.Disconnect("remove"); err != nil {
		t.Fatalf("Disconnect(remove): %v", err)
	}
	if svc.IsConnected("remove") {
		t.Fatal("disconnected session remains registered")
	}
	if !svc.IsConnected("keep") {
		t.Fatal("disconnecting one session removed an unrelated session")
	}
}

func TestRemoteService_CloseClearsSessionsAndPreventsReconnect(t *testing.T) {
	svc := NewRemoteService()
	svc.mu.Lock()
	svc.sessions["existing"] = &SSHSession{}
	svc.mu.Unlock()

	if err := svc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := svc.ListConnections(); len(got) != 0 {
		t.Fatalf("connections after Close = %v, want none", got)
	}

	err := svc.Connect("new", SSHConfig{
		Host:     "example.invalid",
		Port:     22,
		User:     "user",
		Password: "password",
	})
	if err == nil || !strings.Contains(err.Error(), "shut down") {
		t.Fatalf("Connect after Close error = %v, want shut down error", err)
	}
	if svc.IsConnected("new") {
		t.Fatal("Connect after Close registered a session")
	}
}

// ============================================================================
// SSHFileSystem 辅助函数测试（不需要真实 SSH 连接）
// ============================================================================

// TestSSHFileSystem_PathHelpers 验证远程路径辅助函数（sftpJoin/sftpBase/sftpDir）。
// 这些函数独立于 SSH 连接，可直接测试。
func TestSSHFileSystem_PathHelpers(t *testing.T) {
	cases := []struct {
		name string
		fn   func() string
		want string
	}{
		{"sftpJoin basic", func() string { return sftpJoin("/home", "user") }, "/home/user"},
		{"sftpJoin trailing slash", func() string { return sftpJoin("/home/", "user") }, "/home/user"},
		{"sftpJoin empty base", func() string { return sftpJoin("", "user") }, "user"},
		{"sftpBase simple", func() string { return sftpBase("/home/user") }, "user"},
		{"sftpBase root", func() string { return sftpBase("/user") }, "user"},
		{"sftpBase trailing slash", func() string { return sftpBase("/home/user/") }, "user"},
		{"sftpBase no slash", func() string { return sftpBase("user") }, "user"},
		{"sftpDir simple", func() string { return sftpDir("/home/user") }, "/home"},
		{"sftpDir root child", func() string { return sftpDir("/user") }, "/"},
		{"sftpDir no slash", func() string { return sftpDir("user") }, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.fn(); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestLocalFileSystem_EmptyPath 验证所有方法对空路径返回错误。
func TestSSHFileSystem_ReadFileUsesBoundedReader(t *testing.T) {
	source, err := os.ReadFile("remote_service.go")
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(source), "func (s *SSHFileSystem) ReadFile")
	if start < 0 {
		t.Fatal("SSHFileSystem.ReadFile source not found")
	}
	end := strings.Index(string(source[start:]), "func (s *SSHFileSystem) WriteFile")
	if end < 0 {
		t.Fatal("SSHFileSystem.WriteFile source not found")
	}
	body := string(source[start : start+end])
	if strings.Contains(body, "io.ReadAll(f)") || !strings.Contains(body, "readRemoteFileLimited(f, maxSSHReadFileSize)") {
		t.Fatalf("SSHFileSystem.ReadFile must use the explicit bounded reader path; body:\n%s", body)
	}
}

func TestReadRemoteFileLimitedDetectsOneByteOverLimit(t *testing.T) {
	got, err := readRemoteFileLimited(bytes.NewReader([]byte("four")), 4)
	if err != nil {
		t.Fatalf("exact limit: %v", err)
	}
	if string(got) != "four" {
		t.Fatalf("exact limit data = %q, want %q", got, "four")
	}

	got, err = readRemoteFileLimited(bytes.NewReader([]byte("five!")), 4)
	if err == nil || !strings.Contains(err.Error(), "4 bytes") {
		t.Fatalf("one byte over limit error = %v, want explicit 4 byte limit", err)
	}
	if got != nil {
		t.Fatalf("one byte over limit returned truncated data %q", got)
	}
}

func TestLocalFileSystem_EmptyPath(t *testing.T) {
	fs := NewLocalFileSystem()
	checks := []struct {
		name string
		fn   func() error
	}{
		{"ReadFile", func() error { _, err := fs.ReadFile(""); return err }},
		{"WriteFile", func() error { return fs.WriteFile("", []byte("x")) }},
		{"ListDirectory", func() error { _, err := fs.ListDirectory(""); return err }},
		{"CreateFile", func() error { return fs.CreateFile("") }},
		{"CreateDirectory", func() error { return fs.CreateDirectory("") }},
		{"DeletePath", func() error { return fs.DeletePath("") }},
		{"RenamePath", func() error { return fs.RenamePath("", "x") }},
		{"Stat", func() error { _, err := fs.Stat(""); return err }},
		{"Watch", func() error { _, err := fs.Watch(context.Background(), ""); return err }},
	}
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			if err := c.fn(); err == nil {
				t.Fatalf("expected error for empty path in %s", c.name)
			}
		})
	}
}

// TestLocalFileSystem_CreateFileAndDirectory 验证 CreateFile / CreateDirectory。
func TestLocalFileSystem_CreateFileAndDirectory(t *testing.T) {
	tmp := t.TempDir()
	fs := NewLocalFileSystem()

	// CreateDirectory 应递归创建。
	nested := filepath.Join(tmp, "a", "b", "c")
	if err := fs.CreateDirectory(nested); err != nil {
		t.Fatalf("CreateDirectory nested: %v", err)
	}
	info, err := os.Stat(nested)
	if err != nil {
		t.Fatalf("stat nested dir: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("nested path is not a directory")
	}

	// CreateFile 应创建空文件。
	filePath := filepath.Join(tmp, "empty.txt")
	if err := fs.CreateFile(filePath); err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	got, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read empty file: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty file, got %d bytes", len(got))
	}
}
