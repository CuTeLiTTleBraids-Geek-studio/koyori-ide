package services

// Plan 11 Task 6 Step 9 — Computer Use service tests.
//
// 覆盖：
//   - 配置加载/保存（默认值 + G-SEC-12 默认禁用）
//   - 安全边界：禁止快捷键黑名单（Step 6）
//   - 安全边界：坐标落入禁止区域（Step 5/6）
//   - 审计日志记录（Step 7）
//   - ConfirmationRequired 强制确认（Step 3）
//   - 录制模式 start/stop（Step 4）
//   - 平台 stub 返回 ErrPlatformUnsupported

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"image"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newTestComputerUseService(t *testing.T) *ComputerUseService {
	t.Helper()
	dir := t.TempDir()
	svc := NewComputerUseService(dir)
	svc.approveOperation = func(string, string) bool { return true }
	svc.platform = &recordingComputerUsePlatform{}
	return svc
}

func expectComputerUsePlatformReachable(t *testing.T, err error) {
	t.Helper()
	if err != nil && !errors.Is(err, ErrPlatformUnsupported) {
		t.Fatalf("operation should pass safety; got %v", err)
	}
}

type recordingComputerUsePlatform struct {
	mu    sync.Mutex
	calls []string
}

func (p *recordingComputerUsePlatform) record(call string) {
	p.mu.Lock()
	p.calls = append(p.calls, call)
	p.mu.Unlock()
}

func (p *recordingComputerUsePlatform) Screenshot(region *image.Rectangle) ([]byte, error) {
	p.record(fmt.Sprintf("screenshot:%v", region))
	return []byte("png"), nil
}

func (p *recordingComputerUsePlatform) MouseMove(x, y int) error {
	p.record(fmt.Sprintf("mouse_move:%d,%d", x, y))
	return nil
}

func (p *recordingComputerUsePlatform) MouseClick(button string) error {
	p.record("mouse_click:" + button)
	return nil
}

func (p *recordingComputerUsePlatform) KeyboardType(text string) error {
	p.record("keyboard_type:" + text)
	return nil
}

func (p *recordingComputerUsePlatform) KeyboardHotkey(keys string) error {
	p.record("keyboard_hotkey:" + keys)
	return nil
}

func (p *recordingComputerUsePlatform) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

func (p *recordingComputerUsePlatform) recordedCalls() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.calls...)
}

func executeApprovedComputerUseOperation(t *testing.T, svc *ComputerUseService, action, details string) (ComputerUseOperationResult, error) {
	t.Helper()
	token, err := svc.RequestOperationApproval(action, details)
	if err != nil {
		t.Fatalf("RequestOperationApproval(%q) failed: %v", action, err)
	}
	return svc.ExecuteApprovedOperation(context.Background(), token)
}

func TestComputerUseService_RendererCannotDirectlyExecuteOperations(t *testing.T) {
	svc := newTestComputerUseService(t)
	platform := &recordingComputerUsePlatform{}
	svc.platform = platform
	svc.mu.Lock()
	svc.config.Enabled = true
	svc.mu.Unlock()
	ctx := context.Background()

	if _, err := svc.screenshot(ctx, nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("direct Screenshot error = %v, want ErrInvalidInput", err)
	}
	if err := svc.mouseMove(ctx, 10, 10); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("direct MouseMove error = %v, want ErrInvalidInput", err)
	}
	if err := svc.mouseClick(ctx, "left"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("direct MouseClick error = %v, want ErrInvalidInput", err)
	}
	if err := svc.keyboardType(ctx, "text"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("direct KeyboardType error = %v, want ErrInvalidInput", err)
	}
	if err := svc.keyboardHotkey(ctx, "ctrl+c"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("direct KeyboardHotkey error = %v, want ErrInvalidInput", err)
	}
	if _, err := svc.ExecuteApprovedOperation(ctx, "renderer-forged-token"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("malformed token error = %v, want ErrInvalidInput", err)
	}
	if _, err := svc.ExecuteApprovedOperation(ctx, strings.Repeat("0", 64)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unknown canonical token error = %v, want ErrInvalidInput", err)
	}
	if got := platform.callCount(); got != 0 {
		t.Fatalf("direct renderer calls reached platform %d times", got)
	}

	typeOfService := reflect.TypeOf(svc)
	for _, methodName := range []string{
		"Screenshot", "MouseMove", "MouseClick", "KeyboardType", "KeyboardHotkey",
	} {
		if _, exposed := typeOfService.MethodByName(methodName); exposed {
			t.Errorf("ComputerUseService.%s remains exported to Wails reflection", methodName)
		}
	}
}

func TestComputerUseService_DirectOperationsHiddenFromWails(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "computer_use_service.go", nil, 0)
	if err != nil {
		t.Fatalf("parse computer_use_service.go: %v", err)
	}

	wantInternal := map[string]bool{
		"screenshot":     false,
		"mouseMove":      false,
		"mouseClick":     false,
		"keyboardType":   false,
		"keyboardHotkey": false,
	}
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Recv == nil {
			continue
		}
		if _, tracked := wantInternal[function.Name.Name]; !tracked {
			continue
		}
		wantInternal[function.Name.Name] = true
	}
	for method, found := range wantInternal {
		if !found {
			t.Errorf("ComputerUseService.%s internal fail-closed operation is missing", method)
		}
	}
}

func TestComputerUseService_PublicAPIHasNoApprovalBoolean(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "computer_use_service.go", nil, 0)
	if err != nil {
		t.Fatalf("parse computer_use_service.go: %v", err)
	}

	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Recv == nil || !function.Name.IsExported() || function.Type.Params == nil {
			continue
		}
		for _, parameter := range function.Type.Params.List {
			identifier, ok := parameter.Type.(*ast.Ident)
			if ok && identifier.Name == "bool" {
				t.Errorf("exported ComputerUseService.%s accepts a renderer-forgeable boolean", function.Name.Name)
			}
		}
	}
}

func TestComputerUseService_BackendApprovalCannotBeForged(t *testing.T) {
	svc := newTestComputerUseService(t)
	platform := &recordingComputerUsePlatform{}
	svc.platform = platform
	svc.approveOperation = func(string, string) bool { return false }
	svc.mu.Lock()
	svc.config.Enabled = true
	svc.config.ConfirmationRequired = true
	svc.mu.Unlock()

	if _, err := svc.RequestOperationApproval("mouse_move", `{"x":10,"y":10}`); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("rejected native approval error = %v, want ErrNotAllowed", err)
	}
	if got := platform.callCount(); got != 0 {
		t.Fatalf("rejected approval reached platform %d times", got)
	}
}

func TestComputerUseService_ApprovalTokenBindsExactActionAndParameters(t *testing.T) {
	svc := newTestComputerUseService(t)
	platform := &recordingComputerUsePlatform{}
	svc.platform = platform
	svc.mu.Lock()
	svc.config.Enabled = true
	svc.config.ConfirmationRequired = true
	svc.mu.Unlock()

	var approvedAction, approvedDetails string
	svc.approveOperation = func(action, details string) bool {
		approvedAction = action
		approvedDetails = details
		return true
	}
	token, err := svc.RequestOperationApproval(" mouse_move ", `{ "y": 22, "x": 11 }`)
	if err != nil {
		t.Fatalf("RequestOperationApproval failed: %v", err)
	}
	if approvedAction != "mouse_move" || approvedDetails != `{"x":11,"y":22}` {
		t.Fatalf("native approval payload = (%q, %q), want canonical exact operation", approvedAction, approvedDetails)
	}
	svc.approvalMu.Lock()
	approval := svc.approvals[token]
	svc.approvalMu.Unlock()
	if approval.action != "mouse_move" || approval.details != `{"x":11,"y":22}` || approval.configGeneration == 0 {
		t.Fatalf("stored approval is not bound to action/details/generation: %+v", approval)
	}
	if !isCanonicalComputerUseApprovalToken(token) {
		t.Fatalf("approval token %q is not canonical 256-bit hex", token)
	}
	if approval.binding == ([32]byte{}) {
		t.Fatal("approval action/details/generation/expiry were not cryptographically bound")
	}
	if got := approval.expiresAt.Sub(svc.currentTime()); got <= 0 || got > computerUseApprovalTTL {
		t.Fatalf("approval TTL = %v, want (0, %v]", got, computerUseApprovalTTL)
	}

	if _, err := svc.ExecuteApprovedOperation(context.Background(), token); err != nil {
		t.Fatalf("ExecuteApprovedOperation failed: %v", err)
	}
	if calls := platform.recordedCalls(); !reflect.DeepEqual(calls, []string{"mouse_move:11,22"}) {
		t.Fatalf("platform calls = %v, want exact approved parameters", calls)
	}
}

func TestComputerUseService_ApprovalTokenRejectsTamperedActionAndDetails(t *testing.T) {
	tests := []struct {
		name   string
		tamper func(*computerUseApproval)
	}{
		{
			name: "action",
			tamper: func(approval *computerUseApproval) {
				approval.action = "keyboard_type"
			},
		},
		{
			name: "details",
			tamper: func(approval *computerUseApproval) {
				approval.details = `{"x":999,"y":2}`
			},
		},
		{
			name: "expiry",
			tamper: func(approval *computerUseApproval) {
				approval.expiresAt = approval.expiresAt.Add(time.Hour)
			},
		},
		{
			name: "generation",
			tamper: func(approval *computerUseApproval) {
				approval.configGeneration++
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newTestComputerUseService(t)
			platform := &recordingComputerUsePlatform{}
			svc.platform = platform
			svc.mu.Lock()
			svc.config.Enabled = true
			svc.mu.Unlock()

			token, err := svc.RequestOperationApproval("mouse_move", `{"x":1,"y":2}`)
			if err != nil {
				t.Fatal(err)
			}
			svc.approvalMu.Lock()
			approval := svc.approvals[token]
			tt.tamper(&approval)
			svc.approvals[token] = approval
			svc.approvalMu.Unlock()

			if _, err := svc.ExecuteApprovedOperation(context.Background(), token); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("tampered %s error = %v, want ErrInvalidInput", tt.name, err)
			}
			if got := platform.callCount(); got != 0 {
				t.Fatalf("tampered %s reached platform %d times", tt.name, got)
			}
			if _, err := svc.ExecuteApprovedOperation(context.Background(), token); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("tampered %s replay error = %v, want ErrInvalidInput", tt.name, err)
			}
		})
	}
}

func TestComputerUseService_ApprovalTokenRejectsReplay(t *testing.T) {
	svc := newTestComputerUseService(t)
	platform := &recordingComputerUsePlatform{}
	svc.platform = platform
	svc.mu.Lock()
	svc.config.Enabled = true
	svc.config.ConfirmationRequired = false
	svc.mu.Unlock()

	token, err := svc.RequestOperationApproval("mouse_click", `{"button":"LEFT"}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ExecuteApprovedOperation(context.Background(), token); err != nil {
		t.Fatalf("first execution failed: %v", err)
	}
	if _, err := svc.ExecuteApprovedOperation(context.Background(), token); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("replay error = %v, want ErrInvalidInput", err)
	}
	if got := platform.callCount(); got != 1 {
		t.Fatalf("replayed token reached platform: call count = %d", got)
	}
}

func TestComputerUseService_ApprovalTokenRejectsConcurrentReplay(t *testing.T) {
	svc := newTestComputerUseService(t)
	platform := &recordingComputerUsePlatform{}
	svc.platform = platform
	svc.mu.Lock()
	svc.config.Enabled = true
	svc.config.ConfirmationRequired = false
	svc.mu.Unlock()

	token, err := svc.RequestOperationApproval("keyboard_type", `{"text":"one use"}`)
	if err != nil {
		t.Fatal(err)
	}
	const contenders = 32
	start := make(chan struct{})
	results := make(chan error, contenders)
	for i := 0; i < contenders; i++ {
		go func() {
			<-start
			_, err := svc.ExecuteApprovedOperation(context.Background(), token)
			results <- err
		}()
	}
	close(start)
	var successes, rejections int
	for i := 0; i < contenders; i++ {
		if err := <-results; err == nil {
			successes++
		} else if errors.Is(err, ErrInvalidInput) {
			rejections++
		} else {
			t.Fatalf("unexpected concurrent execution error: %v", err)
		}
	}
	if successes != 1 || rejections != contenders-1 || platform.callCount() != 1 {
		t.Fatalf("successes=%d rejections=%d platformCalls=%d, want 1/%d/1", successes, rejections, platform.callCount(), contenders-1)
	}
}

func TestComputerUseService_ApprovalTokenRejectsExpiryAndGenerationChange(t *testing.T) {
	t.Run("expiry", func(t *testing.T) {
		svc := newTestComputerUseService(t)
		platform := &recordingComputerUsePlatform{}
		svc.platform = platform
		current := time.Unix(1_700_000_000, 0)
		svc.mu.Lock()
		svc.config.Enabled = true
		svc.config.ConfirmationRequired = false
		svc.now = func() time.Time { return current }
		svc.mu.Unlock()
		token, err := svc.RequestOperationApproval("mouse_move", `{"x":1,"y":2}`)
		if err != nil {
			t.Fatal(err)
		}
		current = current.Add(computerUseApprovalTTL + time.Nanosecond)
		if _, err := svc.ExecuteApprovedOperation(context.Background(), token); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("expired token error = %v, want ErrInvalidInput", err)
		}
		if platform.callCount() != 0 {
			t.Fatal("expired token reached platform")
		}
		if _, err := svc.ExecuteApprovedOperation(context.Background(), token); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("expired token replay error = %v, want ErrInvalidInput", err)
		}
	})

	t.Run("config generation", func(t *testing.T) {
		svc := newTestComputerUseService(t)
		platform := &recordingComputerUsePlatform{}
		svc.platform = platform
		svc.mu.Lock()
		svc.config.Enabled = true
		svc.config.ConfirmationRequired = false
		svc.mu.Unlock()
		token, err := svc.RequestOperationApproval("mouse_move", `{"x":1,"y":2}`)
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.UpdateConfig(ComputerUseConfig{Enabled: true, ConfirmationRequired: false, AppWhitelist: []string{"changed"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.ExecuteApprovedOperation(context.Background(), token); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("stale-generation token error = %v, want ErrInvalidInput", err)
		}
		if platform.callCount() != 0 {
			t.Fatal("stale-generation token reached platform")
		}
	})
}

func TestComputerUseService_ApprovalDetailsFailClosed(t *testing.T) {
	svc := newTestComputerUseService(t)
	svc.mu.Lock()
	svc.config.Enabled = true
	svc.config.ConfirmationRequired = false
	svc.mu.Unlock()

	tests := []struct {
		action  string
		details string
	}{
		{"", `{}`},
		{"shell", `{}`},
		{"mouse_move", `{"x":1,"y":2,"confirmedByUser":true}`},
		{"mouse_click", `{"button":"primary"}`},
		{"keyboard_type", `{"text":""}`},
		{"keyboard_hotkey", `{"keys":""}`},
		{"screenshot", `{"region":null} {}`},
	}
	for _, tt := range tests {
		if _, err := svc.RequestOperationApproval(tt.action, tt.details); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("RequestOperationApproval(%q, %q) error = %v, want ErrInvalidInput", tt.action, tt.details, err)
		}
	}
}

func TestComputerUseService_RendererCannotEnableWithoutBackendApproval(t *testing.T) {
	svc := newTestComputerUseService(t)
	svc.approveOperation = func(string, string) bool { return false }
	err := svc.UpdateConfig(ComputerUseConfig{Enabled: true, ConfirmationRequired: true})
	if !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("renderer enablement must fail closed, got %v", err)
	}
	if svc.IsEnabled() {
		t.Fatal("rejected enablement must not be published")
	}
}

func TestComputerUseService_RendererCannotDisableConfirmationWithoutBackendApproval(t *testing.T) {
	svc := newTestComputerUseService(t)
	svc.mu.Lock()
	svc.config.Enabled = true
	svc.config.ConfirmationRequired = true
	svc.mu.Unlock()
	svc.approveOperation = func(string, string) bool { return false }

	err := svc.UpdateConfig(ComputerUseConfig{Enabled: true, ConfirmationRequired: false})
	if !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("confirmation reduction must fail closed, got %v", err)
	}
	if !svc.GetConfig().ConfirmationRequired {
		t.Fatal("rejected confirmation reduction was published")
	}
}

func TestComputerUseService_RendererCannotChangeConfirmationPolicyWithoutBackendApproval(t *testing.T) {
	svc := newTestComputerUseService(t)
	svc.mu.Lock()
	svc.config.Enabled = true
	svc.config.ConfirmationRequired = false
	svc.mu.Unlock()
	svc.approveOperation = func(string, string) bool { return false }

	err := svc.UpdateConfig(ComputerUseConfig{Enabled: true, ConfirmationRequired: true})
	if !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("confirmation policy change must fail closed, got %v", err)
	}
	if svc.GetConfig().ConfirmationRequired {
		t.Fatal("rejected confirmation policy change was published")
	}
}

// --- Step 1/2: 配置与默认值 ---

func TestComputerUseService_DefaultConfig(t *testing.T) {
	svc := newTestComputerUseService(t)
	cfg := svc.GetConfig()
	// G-SEC-12：默认禁用。
	if cfg.Enabled {
		t.Error("default Enabled should be false (G-SEC-12)")
	}
	// Step 3：默认需确认。
	if !cfg.ConfirmationRequired {
		t.Error("default ConfirmationRequired should be true")
	}
	// Step 6：默认禁止快捷键黑名单应包含 OS 级危险快捷键。
	required := []string{"ctrl+alt+del", "alt+f4", "cmd+q"}
	for _, r := range required {
		if !containsString(cfg.ForbiddenHotkeys, r) {
			t.Errorf("default ForbiddenHotkeys should contain %q, got %v", r, cfg.ForbiddenHotkeys)
		}
	}
}

func TestComputerUseService_UpdateConfig_PersistsForbiddenHotkeys(t *testing.T) {
	svc := newTestComputerUseService(t)
	// 用户自定义快捷键，不应丢失默认黑名单。
	err := svc.UpdateConfig(ComputerUseConfig{
		Enabled:              true,
		ConfirmationRequired: false,
		ForbiddenHotkeys:     []string{"ctrl+c"}, // 自定义
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := svc.GetConfig()
	// 默认黑名单应被合并保留。
	if !containsString(cfg.ForbiddenHotkeys, "ctrl+alt+del") {
		t.Error("default forbidden hotkeys should be preserved after UpdateConfig")
	}
	if !containsString(cfg.ForbiddenHotkeys, "ctrl+c") {
		t.Error("user-defined hotkey should be preserved")
	}
	// 重新加载验证持久化。
	svc2 := NewComputerUseService(svc.configDir)
	cfg2 := svc2.GetConfig()
	if !cfg2.Enabled {
		t.Error("Enabled should persist across reloads")
	}
	if !containsString(cfg2.ForbiddenHotkeys, "ctrl+c") {
		t.Error("user hotkey should persist")
	}
}

func TestComputerUseService_ConfigOwnershipIsolation(t *testing.T) {
	svc := newTestComputerUseService(t)
	cfg := ComputerUseConfig{
		Enabled:          true,
		AppWhitelist:     []string{"owned-app"},
		ForbiddenZones:   []ForbiddenZone{{Name: "owned-zone", X: 1, Y: 2, W: 3, H: 4}},
		ForbiddenHotkeys: []string{"ctrl+c"},
	}
	if err := svc.UpdateConfig(cfg); err != nil {
		t.Fatalf("UpdateConfig failed: %v", err)
	}

	cfg.AppWhitelist[0] = "caller-mutated-app"
	cfg.ForbiddenZones[0].Name = "caller-mutated-zone"
	cfg.ForbiddenHotkeys[0] = "caller-mutated-hotkey"

	first := svc.GetConfig()
	if first.AppWhitelist[0] != "owned-app" || first.ForbiddenZones[0].Name != "owned-zone" || first.ForbiddenHotkeys[0] != "ctrl+c" {
		t.Fatalf("caller input mutation changed service config: %+v", first)
	}

	first.AppWhitelist[0] = "returned-mutated-app"
	first.ForbiddenZones[0].Name = "returned-mutated-zone"
	first.ForbiddenHotkeys[0] = "returned-mutated-hotkey"
	second := svc.GetConfig()
	if second.AppWhitelist[0] != "owned-app" || second.ForbiddenZones[0].Name != "owned-zone" || second.ForbiddenHotkeys[0] != "ctrl+c" {
		t.Fatalf("GetConfig returned aliases to service config: %+v", second)
	}
}

func TestComputerUseService_SaveConfigPersistsOutsideLockAndSerializesWrites(t *testing.T) {
	svc := newTestComputerUseService(t)
	type persistenceEntry struct {
		ordinal       int32
		lockAvailable bool
	}
	enteredPersistence := make(chan persistenceEntry, 2)
	releaseFirst := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseFirst) }) }
	t.Cleanup(release)
	var persistenceCalls atomic.Int32
	svc.persistConfig = func([]byte) error {
		lockAvailable := svc.mu.TryLock()
		if lockAvailable {
			svc.mu.Unlock()
		}
		ordinal := persistenceCalls.Add(1)
		enteredPersistence <- persistenceEntry{ordinal: ordinal, lockAvailable: lockAvailable}
		if ordinal == 1 {
			<-releaseFirst
		}
		return nil
	}

	firstResult := make(chan error, 1)
	go func() {
		firstResult <- svc.saveConfig()
	}()
	select {
	case entry := <-enteredPersistence:
		if entry.ordinal != 1 {
			t.Fatalf("first persistence ordinal = %d, want 1", entry.ordinal)
		}
		if !entry.lockAvailable {
			t.Fatal("saveConfig called persistence while holding the service state lock")
		}
	case <-time.After(time.Second):
		t.Fatal("first saveConfig did not enter persistence")
	}

	svc.mu.RLock()
	firstTail := svc.persistTail
	svc.mu.RUnlock()
	secondResult := make(chan error, 1)
	go func() {
		secondResult <- svc.saveConfig()
	}()
	deadline := time.Now().Add(time.Second)
	for {
		if svc.mu.TryRLock() {
			secondReserved := svc.persistTail != firstTail
			svc.mu.RUnlock()
			if secondReserved {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("second saveConfig did not reserve its persistence slot")
		}
		runtime.Gosched()
	}
	select {
	case entry := <-enteredPersistence:
		release()
		t.Fatalf("second persistence entered before the first completed: ordinal=%d", entry.ordinal)
	case <-time.After(100 * time.Millisecond):
	}
	release()

	select {
	case entry := <-enteredPersistence:
		if entry.ordinal != 2 {
			t.Fatalf("second persistence ordinal = %d, want 2", entry.ordinal)
		}
		if !entry.lockAvailable {
			t.Fatal("queued saveConfig called persistence while holding the service state lock")
		}
	case <-time.After(time.Second):
		t.Fatal("second saveConfig did not enter persistence after the first completed")
	}
	if err := <-firstResult; err != nil {
		t.Fatalf("first saveConfig failed: %v", err)
	}
	if err := <-secondResult; err != nil {
		t.Fatalf("second saveConfig failed: %v", err)
	}
}

func TestComputerUseService_UpdateConfigPersistenceFailureDoesNotPublish(t *testing.T) {
	svc := newTestComputerUseService(t)
	before := svc.GetConfig()
	persistErr := errors.New("persistence failed")
	enteredPersistence := make(chan bool, 1)
	releasePersistence := make(chan struct{})
	svc.persistConfig = func([]byte) error {
		lockAvailable := svc.mu.TryLock()
		if lockAvailable {
			svc.mu.Unlock()
		}
		enteredPersistence <- lockAvailable
		<-releasePersistence
		return persistErr
	}

	result := make(chan error, 1)
	go func() {
		result <- svc.UpdateConfig(ComputerUseConfig{
			Enabled:          true,
			AppWhitelist:     []string{"new-app"},
			ForbiddenHotkeys: []string{"ctrl+c"},
		})
	}()
	select {
	case lockAvailable := <-enteredPersistence:
		if !lockAvailable {
			close(releasePersistence)
			<-result
			t.Fatal("UpdateConfig called persistence while holding the service state lock")
		}
	case <-time.After(time.Second):
		close(releasePersistence)
		t.Fatal("UpdateConfig did not enter persistence")
	}

	duringResult := make(chan ComputerUseConfig, 1)
	go func() { duringResult <- svc.GetConfig() }()
	select {
	case during := <-duringResult:
		if !reflect.DeepEqual(during, before) {
			close(releasePersistence)
			<-result
			t.Fatalf("config published before persistence completed: got %+v, want %+v", during, before)
		}
	case <-time.After(time.Second):
		close(releasePersistence)
		<-result
		t.Fatal("GetConfig blocked while persistence was in progress")
	}
	close(releasePersistence)
	err := <-result
	if !errors.Is(err, persistErr) {
		t.Fatalf("UpdateConfig error = %v, want %v", err, persistErr)
	}
	after := svc.GetConfig()
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("config changed after failed persistence: got %+v, want %+v", after, before)
	}
}

func TestComputerUseService_ConcurrentCallerMutationIsIsolated(t *testing.T) {
	svc := newTestComputerUseService(t)
	cfg := ComputerUseConfig{
		Enabled:          true,
		AppWhitelist:     []string{"stable-app"},
		ForbiddenZones:   []ForbiddenZone{{Name: "stable-zone"}},
		ForbiddenHotkeys: []string{"ctrl+c"},
	}
	if err := svc.UpdateConfig(cfg); err != nil {
		t.Fatalf("UpdateConfig failed: %v", err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 1000; i++ {
			cfg.AppWhitelist[0] = "caller"
			cfg.ForbiddenZones[0].Name = "caller"
			cfg.ForbiddenHotkeys[0] = "caller"
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 50; i++ {
			_ = svc.GetConfig()
			if err := svc.saveConfig(); err != nil {
				t.Errorf("saveConfig failed: %v", err)
				return
			}
		}
	}()
	close(start)
	wg.Wait()

	got := svc.GetConfig()
	if got.AppWhitelist[0] != "stable-app" || got.ForbiddenZones[0].Name != "stable-zone" || got.ForbiddenHotkeys[0] != "ctrl+c" {
		t.Fatalf("caller-owned slices leaked into service state: %+v", got)
	}
}

// --- Step 6: 安全边界 — 禁止快捷键 ---

func TestIsHotkeyForbidden(t *testing.T) {
	forbidden := []string{"Ctrl+Alt+Del", "Alt+F4"}
	tests := []struct {
		keys string
		want bool
	}{
		{"ctrl+alt+del", true}, // 大小写不敏感
		{"Ctrl+Alt+Del", true}, // 原样
		{"alt+f4", true},
		{"ALT+F4", true},
		{"ctrl+c", false},  // 不在黑名单
		{"ctrl+c ", false}, // 空格 trim
		{"", false},
	}
	for _, tt := range tests {
		if got := isHotkeyForbidden(forbidden, tt.keys); got != tt.want {
			t.Errorf("isHotkeyForbidden(%q) = %v, want %v", tt.keys, got, tt.want)
		}
	}
}

func TestComputerUseService_KeyboardHotkey_ForbiddenRejected(t *testing.T) {
	svc := newTestComputerUseService(t)
	_ = svc.UpdateConfig(ComputerUseConfig{
		Enabled:              true,
		ConfirmationRequired: false, // 跳过确认，测试黑名单
	})
	// 禁止快捷键应被拒绝（Step 6）。
	_, err := svc.RequestOperationApproval("keyboard_hotkey", `{"keys":"ctrl+alt+del"}`)
	if !errors.Is(err, ErrNotAllowed) {
		t.Errorf("ctrl+alt+del should be rejected, got %v", err)
	}
	// 非禁止快捷键应通过安全检查（平台 stub 返回 ErrPlatformUnsupported）。
	_, err = executeApprovedComputerUseOperation(t, svc, "keyboard_hotkey", `{"keys":"ctrl+c"}`)
	expectComputerUsePlatformReachable(t, err)
}

// --- Step 5/6: 安全边界 — 禁止区域 ---

func TestIsPointInForbiddenZone(t *testing.T) {
	zones := []ForbiddenZone{
		{Name: "password-manager", X: 100, Y: 100, W: 200, H: 100},
	}
	tests := []struct {
		x, y int
		want bool
	}{
		{150, 150, true},  // 中心
		{100, 100, true},  // 左上角（包含）
		{299, 199, true},  // 右下角（包含）
		{300, 200, false}, // 右下角外（不包含）
		{99, 100, false},  // 左侧外
		{0, 0, false},     // 原点外
	}
	for _, tt := range tests {
		if got := isPointInForbiddenZone(zones, tt.x, tt.y); got != tt.want {
			t.Errorf("isPointInForbiddenZone(%d,%d) = %v, want %v", tt.x, tt.y, got, tt.want)
		}
	}
}

func TestComputerUseService_MouseMove_ForbiddenZoneRejected(t *testing.T) {
	svc := newTestComputerUseService(t)
	_ = svc.UpdateConfig(ComputerUseConfig{
		Enabled:              true,
		ConfirmationRequired: false,
		ForbiddenZones: []ForbiddenZone{
			{Name: "password-manager", X: 100, Y: 100, W: 200, H: 100},
		},
	})
	// 坐标在禁止区域内应被拒绝。
	_, err := svc.RequestOperationApproval("mouse_move", `{"x":150,"y":150}`)
	if !errors.Is(err, ErrNotAllowed) {
		t.Errorf("move to forbidden zone should be rejected, got %v", err)
	}
	// 坐标在禁止区域外应通过安全检查。
	_, err = executeApprovedComputerUseOperation(t, svc, "mouse_move", `{"x":500,"y":500}`)
	expectComputerUsePlatformReachable(t, err)
}

// --- Step 8: G-SEC-12 默认禁用 ---

func TestComputerUseService_DisabledByDefault(t *testing.T) {
	svc := newTestComputerUseService(t)
	if svc.IsEnabled() {
		t.Error("Computer Use should be disabled by default (G-SEC-12)")
	}
	// 未启用时不会签发任何操作 token。
	_, err := svc.RequestOperationApproval("screenshot", `{"region":null}`)
	if !errors.Is(err, ErrNotAllowed) {
		t.Errorf("screenshot when disabled should be ErrNotAllowed, got %v", err)
	}
	_, err = svc.RequestOperationApproval("mouse_move", `{"x":10,"y":10}`)
	if !errors.Is(err, ErrNotAllowed) {
		t.Errorf("mouse_move when disabled should be ErrNotAllowed, got %v", err)
	}
}

// --- Step 3: ConfirmationRequired ---

func TestComputerUseService_ConfirmationRequired(t *testing.T) {
	svc := newTestComputerUseService(t)
	_ = svc.UpdateConfig(ComputerUseConfig{
		Enabled:              true,
		ConfirmationRequired: true, // 默认值
	})
	svc.approveOperation = func(string, string) bool { return false }
	// 原生确认拒绝时后端不得签发 token。
	_, err := svc.RequestOperationApproval("mouse_move", `{"x":10,"y":10}`)
	if !errors.Is(err, ErrNotAllowed) {
		t.Errorf("mouse_move without backend approval should be ErrNotAllowed, got %v", err)
	}
	// 确认后应通过安全检查（平台 stub 失败）。
	svc.approveOperation = func(string, string) bool { return true }
	_, err = executeApprovedComputerUseOperation(t, svc, "mouse_move", `{"x":10,"y":10}`)
	expectComputerUsePlatformReachable(t, err)
}

func TestComputerUseService_ConfirmationDisabledStillRequiresNativeApprovalAndToken(t *testing.T) {
	svc := newTestComputerUseService(t)
	platform := &recordingComputerUsePlatform{}
	svc.platform = platform
	approvalCalls := 0
	svc.approveOperation = func(string, string) bool {
		approvalCalls++
		return true
	}
	if err := svc.UpdateConfig(ComputerUseConfig{Enabled: true, ConfirmationRequired: false}); err != nil {
		t.Fatal(err)
	}
	approvalCalls = 0
	svc.approveOperation = func(string, string) bool {
		approvalCalls++
		return false
	}

	if err := svc.mouseMove(context.Background(), 10, 10); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("direct execution error = %v, want ErrInvalidInput", err)
	}
	if _, err := svc.RequestOperationApproval("mouse_move", `{"x":10,"y":10}`); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("unapproved RequestOperationApproval error = %v, want ErrNotAllowed", err)
	}
	if approvalCalls != 1 {
		t.Fatalf("ConfirmationRequired=false native approval calls = %d, want 1", approvalCalls)
	}
	svc.approveOperation = func(string, string) bool { return true }
	token, err := svc.RequestOperationApproval("mouse_move", `{"x":10,"y":10}`)
	if err != nil {
		t.Fatalf("approved RequestOperationApproval failed: %v", err)
	}
	if _, err := svc.ExecuteApprovedOperation(context.Background(), token); err != nil {
		t.Fatalf("ExecuteApprovedOperation failed: %v", err)
	}
	if got := platform.callCount(); got != 1 {
		t.Fatalf("platform call count = %d, want 1 token-approved call", got)
	}
}

// --- Step 7: 审计日志 ---

func TestComputerUseService_AuditLog(t *testing.T) {
	svc := newTestComputerUseService(t)
	_ = svc.UpdateConfig(ComputerUseConfig{
		Enabled:              true,
		ConfirmationRequired: false,
	})
	// 执行几个操作（安全检查通过，平台 stub 失败）。
	_, _ = executeApprovedComputerUseOperation(t, svc, "mouse_move", `{"x":10,"y":10}`)
	_, _ = executeApprovedComputerUseOperation(t, svc, "keyboard_hotkey", `{"keys":"ctrl+c"}`)
	// 执行一个被安全检查拒绝的操作。
	_, _ = svc.RequestOperationApproval("keyboard_hotkey", `{"keys":"ctrl+alt+del"}`)

	log := svc.GetAuditLog(10)
	if len(log) < 3 {
		t.Fatalf("expected at least 3 audit entries, got %d", len(log))
	}
	// 验证被拒绝的操作也被记录。
	var foundForbidden, foundMove, foundHotkey bool
	for _, e := range log {
		if e.Action == "mouse_move" && e.Args == "(10,10)" {
			foundMove = true
		}
		if e.Action == "keyboard_hotkey" && e.Args == "ctrl+c" {
			foundHotkey = true
		}
		if e.Action == "keyboard_hotkey" && strings.Contains(e.Error, "forbidden") {
			foundForbidden = true
		}
	}
	if !foundMove {
		t.Error("audit log should record mouse_move")
	}
	if !foundHotkey {
		t.Error("audit log should record keyboard_hotkey ctrl+c")
	}
	if !foundForbidden {
		t.Error("audit log should record rejected forbidden hotkey")
	}
}

func TestComputerUseService_AuditLog_Limit(t *testing.T) {
	svc := newTestComputerUseService(t)
	_ = svc.UpdateConfig(ComputerUseConfig{
		Enabled:              true,
		ConfirmationRequired: false,
	})
	// 记录超过 500 条，验证截断。
	for i := 0; i < 550; i++ {
		svc.recordAudit("mouse_move", "(0,0)", true, true, "")
	}
	log := svc.GetAuditLog(0) // 0 = 全部
	if len(log) != 500 {
		t.Errorf("audit log should be capped at 500, got %d", len(log))
	}
	// 限制返回数量。
	log = svc.GetAuditLog(10)
	if len(log) != 10 {
		t.Errorf("GetAuditLog(10) should return 10 entries, got %d", len(log))
	}
}

// --- Step 4: 录制模式 ---

func TestComputerUseService_Recording(t *testing.T) {
	svc := newTestComputerUseService(t)
	_ = svc.UpdateConfig(ComputerUseConfig{
		Enabled:              true,
		ConfirmationRequired: false,
	})
	if svc.IsRecording() {
		t.Error("should not be recording by default")
	}
	if err := svc.StartRecording(); err != nil {
		t.Fatal(err)
	}
	if !svc.IsRecording() {
		t.Error("should be recording after StartRecording")
	}
	// 模拟录制操作。
	svc.recordAction("mouse_move", "(10,10)")
	svc.recordAction("keyboard_type", "hello")
	actions := svc.StopRecording()
	if len(actions) != 2 {
		t.Fatalf("expected 2 recorded actions, got %d", len(actions))
	}
	if actions[0].Action != "mouse_move" || actions[0].Args != "(10,10)" {
		t.Errorf("first action = %+v", actions[0])
	}
	if svc.IsRecording() {
		t.Error("should not be recording after StopRecording")
	}
}

func TestComputerUseService_Recording_DisabledRejected(t *testing.T) {
	svc := newTestComputerUseService(t)
	// 未启用时不能录制。
	err := svc.StartRecording()
	if !errors.Is(err, ErrNotAllowed) {
		t.Errorf("StartRecording when disabled should be ErrNotAllowed, got %v", err)
	}
}

// --- Step 1/2: 5 个工具接口验证 ---

func TestComputerUseService_FiveToolsExist(t *testing.T) {
	svc := newTestComputerUseService(t)
	_ = svc.UpdateConfig(ComputerUseConfig{
		Enabled:              true,
		ConfirmationRequired: false,
	})
	// 所有 5 个工具都只能通过 token 到达平台 stub。
	for _, item := range []struct{ action, details string }{
		{"screenshot", `{"region":null}`},
		{"mouse_move", `{"x":10,"y":10}`},
		{"mouse_click", `{"button":"left"}`},
		{"keyboard_type", `{"text":"test"}`},
		{"keyboard_hotkey", `{"keys":"ctrl+c"}`},
	} {
		_, err := executeApprovedComputerUseOperation(t, svc, item.action, item.details)
		expectComputerUsePlatformReachable(t, err)
	}
}

// --- Step 2: 截图区域参数 ---

func TestComputerUseService_Screenshot_WithRegion(t *testing.T) {
	svc := newTestComputerUseService(t)
	_ = svc.UpdateConfig(ComputerUseConfig{
		Enabled:              true,
		ConfirmationRequired: false,
	})
	// 指定区域截图。
	_, err := executeApprovedComputerUseOperation(t, svc, "screenshot", `{"region":{"Min":{"X":0,"Y":0},"Max":{"X":100,"Y":100}}}`)
	expectComputerUsePlatformReachable(t, err)
	// 验证审计日志记录了 region 参数。
	log := svc.GetAuditLog(1)
	if len(log) != 1 {
		t.Fatalf("expected 1 audit entry, got %d", len(log))
	}
	if !strings.Contains(log[0].Args, "region=") {
		t.Errorf("audit args should contain region, got %q", log[0].Args)
	}
}

// --- 辅助 ---

func TestComputerUseService_ConfigPath(t *testing.T) {
	dir := t.TempDir()
	svc := NewComputerUseService(dir)
	p := svc.configPath()
	expected := filepath.Join(dir, "koyori-ide", "computer_use.yaml")
	if p != expected {
		t.Errorf("configPath = %q, want %q", p, expected)
	}
}

// TestComputerUseService_H10_RecordingMultiInstanceNotShared 验证 H-10：
// 多个 ComputerUseService 实例的录制状态互不污染。
// 修复前 recording 是模块级 var，两个实例共享同一个录制会话。
func TestComputerUseService_H10_RecordingMultiInstanceNotShared(t *testing.T) {
	svc1 := newTestComputerUseService(t)
	svc2 := newTestComputerUseService(t)
	_ = svc1.UpdateConfig(ComputerUseConfig{Enabled: true, ConfirmationRequired: false})
	_ = svc2.UpdateConfig(ComputerUseConfig{Enabled: true, ConfirmationRequired: false})

	// svc1 开始录制，svc2 不录制。
	if err := svc1.StartRecording(); err != nil {
		t.Fatal(err)
	}
	if !svc1.IsRecording() {
		t.Error("svc1 should be recording")
	}
	if svc2.IsRecording() {
		t.Error("svc2 should NOT be recording (H-10: instance isolation)")
	}

	// svc1 录制操作，svc2 不会捕获。
	svc1.recordAction("mouse_move", "(10,10)")
	actions1 := svc1.StopRecording()
	if len(actions1) != 1 {
		t.Errorf("svc1 expected 1 action, got %d", len(actions1))
	}

	// svc2 开始录制，不应看到 svc1 的操作。
	if err := svc2.StartRecording(); err != nil {
		t.Fatal(err)
	}
	actions2 := svc2.StopRecording()
	if len(actions2) != 0 {
		t.Errorf("svc2 expected 0 actions (H-10: no cross-instance pollution), got %d", len(actions2))
	}
}

// TestComputerUseService_H10_AuditLogMultiInstanceNotShared 验证 H-10：
// 多个实例的审计日志互不污染。
func TestComputerUseService_H10_AuditLogMultiInstanceNotShared(t *testing.T) {
	svc1 := newTestComputerUseService(t)
	svc2 := newTestComputerUseService(t)
	_ = svc1.UpdateConfig(ComputerUseConfig{Enabled: true, ConfirmationRequired: false})
	_ = svc2.UpdateConfig(ComputerUseConfig{Enabled: true, ConfirmationRequired: false})

	// svc1 记录 3 条审计日志。
	svc1.recordAudit("screenshot", "full", true, true, "")
	svc1.recordAudit("mouse_click", "left", true, true, "")
	svc1.recordAudit("keyboard_type", "hello", true, true, "")

	// svc2 应该有 0 条审计日志（实例隔离）。
	log1 := svc1.GetAuditLog(0)
	log2 := svc2.GetAuditLog(0)
	if len(log1) != 3 {
		t.Errorf("svc1 expected 3 audit entries, got %d", len(log1))
	}
	if len(log2) != 0 {
		t.Errorf("svc2 expected 0 audit entries (H-10: no cross-instance pollution), got %d", len(log2))
	}
}

// --- N-8: checkSafety TOCTOU 修复验证 ---

// TestCheckSafetyTOCTOU 验证 N-8 修复：checkSafety 返回的 cfg 是 s.config 的深拷贝，
// 后续 UpdateConfig 不会修改已返回的快照。
//
// 修复前：checkSafety 仅短暂持 RLock 读取 cfg（浅拷贝），RUnlock 后 UpdateConfig
// 修改 s.config.ConfirmationRequired = false，Screenshot 后续读 s.config.ConfirmationRequired
// 读到 false 而绕过确认机制（confirmedByUser=false 时本应被拒绝）。
//
// 修复后：checkSafety 在锁内深拷贝 cfg（slice 字段独立底层数组），返回的快照
// 与 s.config 完全解耦，Screenshot 基于快照判断 ConfirmationRequired。
func TestCheckSafetyTOCTOU(t *testing.T) {
	previousForegroundProcess := computerUseForegroundProcess
	computerUseForegroundProcess = func() (string, error) { return "app1", nil }
	t.Cleanup(func() { computerUseForegroundProcess = previousForegroundProcess })
	svc := newTestComputerUseService(t)
	_ = svc.UpdateConfig(ComputerUseConfig{
		Enabled:              true,
		ConfirmationRequired: true,
		ForbiddenZones:       []ForbiddenZone{{Name: "z", X: 0, Y: 0, W: 10, H: 10}},
		ForbiddenHotkeys:     []string{"ctrl+c"},
		AppWhitelist:         []string{"app1"},
	})

	// 获取快照（此时 ConfirmationRequired=true）。
	cfg, err := svc.checkSafety("screenshot", nil)
	if err != nil {
		t.Fatalf("checkSafety failed: %v", err)
	}
	if !cfg.ConfirmationRequired {
		t.Fatal("snapshot should have ConfirmationRequired=true before UpdateConfig")
	}

	// 篡改 s.config：把 ConfirmationRequired 改为 false，清空所有 slice 字段。
	// 修复前若 checkSafety 返回的是浅拷贝（slice 共享底层数组），此处对 s.config
	// 的修改会影响已返回的 cfg；且 Screenshot 后续读 s.config.ConfirmationRequired
	// 会读到 false 而绕过确认。
	_ = svc.UpdateConfig(ComputerUseConfig{
		Enabled:              true,
		ConfirmationRequired: false,
		ForbiddenZones:       nil,
		ForbiddenHotkeys:     nil,
		AppWhitelist:         nil,
	})

	// 验证快照未被修改（深拷贝独立性）。
	if !cfg.ConfirmationRequired {
		t.Error("N-8: snapshot ConfirmationRequired should still be true after UpdateConfig (deep copy)")
	}
	if len(cfg.ForbiddenZones) != 1 || cfg.ForbiddenZones[0].Name != "z" {
		t.Errorf("N-8: snapshot ForbiddenZones should be preserved, got %+v", cfg.ForbiddenZones)
	}
	// UpdateConfig 会合并默认黑名单，所以快照中除 "ctrl+c" 外还有默认黑名单。
	// 只需验证 "ctrl+c" 仍在（证明快照未被第二次 UpdateConfig 的 nil 清空）。
	if !containsString(cfg.ForbiddenHotkeys, "ctrl+c") {
		t.Errorf("N-8: snapshot ForbiddenHotkeys should still contain ctrl+c, got %+v", cfg.ForbiddenHotkeys)
	}
	if len(cfg.AppWhitelist) != 1 || cfg.AppWhitelist[0] != "app1" {
		t.Errorf("N-8: snapshot AppWhitelist should be preserved, got %+v", cfg.AppWhitelist)
	}
}

// TestCheckSafetyTOCTOU_Concurrent 验证 N-8 修复在并发场景下的正确性：
// 配置更新与 token 请求/消费并发执行时，旧 token 必须 fail closed，且不能
// 触发数据竞争或 panic（go test -race 验证）。
func TestCheckSafetyTOCTOU_Concurrent(t *testing.T) {
	svc := newTestComputerUseService(t)
	svc.platform = &recordingComputerUsePlatform{}
	// 初始状态：启用 + 需确认。
	_ = svc.UpdateConfig(ComputerUseConfig{
		Enabled:              true,
		ConfirmationRequired: true,
	})

	var wg sync.WaitGroup

	// writer goroutine：在 ConfirmationRequired true/false 之间高频切换（固定 400 次）。
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 400; i++ {
			val := (i % 2) == 0
			_ = svc.UpdateConfig(ComputerUseConfig{
				Enabled:              true,
				ConfirmationRequired: val,
			})
		}
	}()

	// reader goroutine：请求并消费截图 token。每个 token 都在同一配置快照
	// 下签发；配置切换后，执行旧 token 必须拒绝而不能到达平台。
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			token, err := svc.RequestOperationApproval("screenshot", `{"region":null}`)
			if err != nil {
				if !errors.Is(err, ErrNotAllowed) {
					t.Errorf("N-8: unexpected approval error: %v", err)
				}
				continue
			}
			_, err = svc.ExecuteApprovedOperation(context.Background(), token)
			if err != nil && !errors.Is(err, ErrInvalidInput) && !errors.Is(err, ErrPlatformUnsupported) {
				t.Errorf("N-8: unexpected execution error: %v", err)
				return
			}
		}
	}()

	// reader goroutine 2：对鼠标操作执行同样的 token 流程，增加竞争面。
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			token, err := svc.RequestOperationApproval("mouse_move", `{"x":50,"y":50}`)
			if err != nil {
				if !errors.Is(err, ErrNotAllowed) {
					t.Errorf("N-8: unexpected approval error: %v", err)
				}
				continue
			}
			_, err = svc.ExecuteApprovedOperation(context.Background(), token)
			if err != nil && !errors.Is(err, ErrInvalidInput) && !errors.Is(err, ErrPlatformUnsupported) {
				t.Errorf("N-8: unexpected execution error: %v", err)
				return
			}
		}
	}()

	wg.Wait()
}

func TestComputerUseServiceHeaderDescribesWindowsNativeAndUnixStub(t *testing.T) {
	src, err := os.ReadFile("computer_use_service.go")
	if err != nil {
		t.Fatalf("read computer_use_service.go: %v", err)
	}
	header := string(src)
	if idx := strings.Index(header, "import ("); idx >= 0 {
		header = header[:idx]
	}
	if !strings.Contains(header, "gdi32/user32") {
		t.Fatal("computer_use_service.go header must mention Windows gdi32/user32")
	}
	if !strings.Contains(header, "Unix 保持 stub") && !strings.Contains(strings.ToLower(header), "unix") {
		t.Fatal("computer_use_service.go header must keep Unix stub honesty")
	}
	if strings.Contains(header, "三平台均为 stub") {
		t.Fatal("computer_use_service.go header still claims every platform is a stub")
	}
}
