package services

// Plan 11 Task 6 — Computer Use（屏幕截图 + 鼠标键盘控制）。
//
// 提供 5 个工具供 AI agent 调用：
//   - Screenshot：截取屏幕或指定区域，返回 base64 PNG
//   - MouseMove：移动鼠标到指定坐标
//   - MouseClick：点击鼠标按钮
//   - KeyboardType：输入文本
//   - KeyboardHotkey：按下组合键
//
// 安全模型（G-SEC-02 / G-SEC-06 / G-SEC-12）：
//   - 所有操作默认 RiskDangerous，需用户显式确认（Step 3）
//   - 禁止 OS 级快捷键黑名单（Ctrl+Alt+Del / Cmd+Q / Alt+F4 等）（Step 6）
//   - 应用白名单：仅允许在白名单进程内操作（Step 5）
//   - 禁止区域：屏幕坐标不可落入禁止区域（密码管理器等）（Step 5/6）
//   - 操作日志审计：每次不可逆操作记录到审计日志（Step 7）
//   - G-SEC-12：默认 Enabled=false，启用需 explicitApproval（Step 8）
//
// 原生操作通过平台特定文件实现：
//   - computer_use_windows.go：Windows 截图/鼠标键盘（gdi32/user32）
//   - computer_use_unix.go：Linux/macOS stub（返回 ErrPlatformUnsupported）
//
// 配置持久化用 atomicWriteJSON（0600），复用既有原子写实现。

import (
	"context"
	"crypto/hmac"
	crypto_rand "crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// 配置 schema（Step 5 / Step 8）
// ---------------------------------------------------------------------------

// ComputerUseConfig 是 Computer Use 的持久化配置。
type ComputerUseConfig struct {
	// Enabled 控制整个 Computer Use 功能开关。
	// G-SEC-12：默认 false，启用需 explicitApproval（视同 Restricted）。
	Enabled bool `yaml:"enabled" json:"enabled"`
	// ConfirmationRequired 为 true 时，每次操作前必须截图 + AI 规划 + 用户确认。
	// Step 3：默认 true。后端 capability 签发始终要求原生确认；此偏好不能
	// 禁用 RequestOperationApproval 的安全边界。
	ConfirmationRequired bool `yaml:"confirmationRequired" json:"confirmationRequired"`
	// ScreenshotQuality 0-100，控制 JPEG/PNG 压缩质量（Step 2）。
	ScreenshotQuality int `yaml:"screenshotQuality,omitempty" json:"screenshotQuality,omitempty"`
	// ScreenshotScale 截图缩放比例（0.1-1.0），降低分辨率以节省 token（Step 2）。
	ScreenshotScale float64 `yaml:"screenshotScale,omitempty" json:"screenshotScale,omitempty"`
	// AppWhitelist 允许操作的应用进程名白名单。空表示不限制。
	AppWhitelist []string `yaml:"appWhitelist,omitempty" json:"appWhitelist,omitempty"`
	// ForbiddenZones 屏幕上禁止操作的矩形区域（密码管理器等）（Step 5/6）。
	ForbiddenZones []ForbiddenZone `yaml:"forbiddenZones,omitempty" json:"forbiddenZones,omitempty"`
	// ForbiddenHotkeys 禁止的快捷键组合黑名单（Step 6）。
	// 默认包含 OS 级危险快捷键。
	ForbiddenHotkeys []string `yaml:"forbiddenHotkeys,omitempty" json:"forbiddenHotkeys,omitempty"`
	// RecordingEnabled 录制模式开关（Step 4）。
	RecordingEnabled bool `yaml:"recordingEnabled,omitempty" json:"recordingEnabled,omitempty"`
}

func cloneComputerUseConfig(cfg ComputerUseConfig) ComputerUseConfig {
	cloned := cfg
	cloned.AppWhitelist = append([]string(nil), cfg.AppWhitelist...)
	cloned.ForbiddenZones = append([]ForbiddenZone(nil), cfg.ForbiddenZones...)
	cloned.ForbiddenHotkeys = append([]string(nil), cfg.ForbiddenHotkeys...)
	return cloned
}

// ForbiddenZone 是屏幕上的禁止操作矩形区域。
type ForbiddenZone struct {
	Name string `yaml:"name" json:"name"`
	X    int    `yaml:"x" json:"x"`
	Y    int    `yaml:"y" json:"y"`
	W    int    `yaml:"w" json:"w"`
	H    int    `yaml:"h" json:"h"`
}

// 默认禁止快捷键黑名单（Step 6）：
// Ctrl+Alt+Del（Windows 安全屏幕）、Cmd+Q（macOS 退出）、
// Alt+F4（关闭窗口）、Ctrl+Shift+Esc（任务管理器）等。
var defaultForbiddenHotkeys = []string{
	"ctrl+alt+del",
	"ctrl+shift+esc",
	"alt+f4",
	"cmd+q",
	"cmd+option+esc",
	"ctrl+alt+backspace",
	"super+l",
}

// defaultComputerUseConfig 返回安全默认配置。
func defaultComputerUseConfig() ComputerUseConfig {
	return ComputerUseConfig{
		Enabled:              false, // G-SEC-12：默认禁用
		ConfirmationRequired: true,  // Step 3：默认需确认
		ScreenshotQuality:    80,
		ScreenshotScale:      1.0,
		AppWhitelist:         nil, // 不限制
		ForbiddenZones:       nil,
		ForbiddenHotkeys:     append([]string{}, defaultForbiddenHotkeys...),
		RecordingEnabled:     false,
	}
}

// ---------------------------------------------------------------------------
// 操作日志审计（Step 7）
// ---------------------------------------------------------------------------

// AuditAction 是审计日志中的单条操作记录。
type AuditAction struct {
	Timestamp time.Time `json:"timestamp"`
	Action    string    `json:"action"`  // screenshot/mouse_move/mouse_click/keyboard_type/keyboard_hotkey
	Args      string    `json:"args"`    // 操作参数摘要（脱敏）
	Success   bool      `json:"success"` // 是否执行成功
	Error     string    `json:"error,omitempty"`
	// ConfirmedByUser 标记该操作是否经用户确认（Step 3）。
	ConfirmedByUser bool `json:"confirmedByUser"`
}

// ---------------------------------------------------------------------------
// ComputerUseService
// ---------------------------------------------------------------------------

// ComputerUseService 管理屏幕截图与鼠标键盘控制（Step 1）。
type ComputerUseService struct {
	mu            sync.RWMutex
	config        ComputerUseConfig
	configDir     string
	persistConfig func([]byte) error
	// persistTail orders configuration writes without holding mu while waiting,
	// serializing, or touching the filesystem. It is protected by mu.
	persistTail <-chan struct{}
	// auditLog 是操作审计日志的环形缓冲（H-10: 最近 N 条）。
	auditLog auditRingBuffer
	// platform 是平台特定的操作执行器（截图/鼠标/键盘）。
	// 由平台文件（computer_use_windows.go / computer_use_unix.go）注入。
	platform platformExecutor
	// approveOperation is backend-owned. Renderer input can describe an
	// operation but can never stand in for the native approval result.
	approveOperation func(action, details string) bool
	approvalMu       sync.Mutex
	approvals        map[string]computerUseApproval
	approvalKey      [sha256.Size]byte
	approvalKeyReady bool
	configGeneration uint64
	now              func() time.Time
	// recording 是录制模式的内存缓冲（H-10: 从模块级 var 改为实例字段）。
	recording recordingSession
}

type computerUseApproval struct {
	action           string
	details          string
	configGeneration uint64
	expiresAt        time.Time
	confirmedByUser  bool
	binding          [sha256.Size]byte
}

const (
	computerUseApprovalTTL   = 2 * time.Minute
	computerUseApprovalLimit = 128
)

// auditRingBuffer 是审计日志的固定容量环形缓冲（H-10）。
// 避免了 slice append + 截断导致的底层数组无法 GC 问题。
const auditRingCapacity = 500

type auditRingBuffer struct {
	data  [auditRingCapacity]AuditAction
	head  int // 下一个写入位置
	count int // 已写入的元素数（最多 auditRingCapacity）
}

// push 添加一条审计日志。
func (r *auditRingBuffer) push(entry AuditAction) {
	r.data[r.head] = entry
	r.head = (r.head + 1) % auditRingCapacity
	if r.count < auditRingCapacity {
		r.count++
	}
}

// latest 返回最近 n 条记录（按时间顺序，最新的在最后）。
func (r *auditRingBuffer) latest(n int) []AuditAction {
	if n <= 0 || n > r.count {
		n = r.count
	}
	out := make([]AuditAction, n)
	start := (r.head - n + auditRingCapacity) % auditRingCapacity
	for i := 0; i < n; i++ {
		out[i] = r.data[(start+i)%auditRingCapacity]
	}
	return out
}

// platformExecutor 是平台特定操作的接口。
// 由 computer_use_windows.go / computer_use_unix.go 实现。
type platformExecutor interface {
	// Screenshot 截取屏幕或指定区域。region 为 nil 表示全屏。
	Screenshot(region *image.Rectangle) ([]byte, error)
	// MouseMove 移动鼠标到 (x, y)。
	MouseMove(x, y int) error
	// MouseClick 点击鼠标按钮（left/right/middle）。
	MouseClick(button string) error
	// KeyboardType 输入文本。
	KeyboardType(text string) error
	// KeyboardHotkey 按下组合键（如 "ctrl+c"）。
	KeyboardHotkey(keys string) error
}

// NewComputerUseService 创建服务。configDir 用于配置文件路径。
func NewComputerUseService(configDir string) *ComputerUseService {
	svc := &ComputerUseService{
		config:           defaultComputerUseConfig(),
		configDir:        configDir,
		platform:         newPlatformExecutor(), // 平台 stub
		approveOperation: nativeComputerUseApproval,
		approvals:        make(map[string]computerUseApproval),
		configGeneration: 1,
		now:              time.Now,
	}
	if _, err := crypto_rand.Read(svc.approvalKey[:]); err == nil {
		svc.approvalKeyReady = true
	} else {
		slog.Error("computer use approval key initialization failed", "err", err)
	}
	// Best-effort loading keeps the service available with defaults, while
	// still making a corrupt or unreadable configuration diagnosable.
	if err := svc.loadConfig(); err != nil {
		slog.Debug("computer use config load failed; using defaults", "err", err)
	}
	return svc
}

func nativeComputerUseApproval(action, details string) bool {
	app := application.Get()
	if app == nil {
		return false
	}
	result := make(chan bool, 1)
	dialog := app.Dialog.Question().SetTitle("Approve computer control").SetMessage(
		fmt.Sprintf("Action: %s\n\n%s", action, details),
	)
	dialog.AddButton("Yes").SetAsDefault().OnClick(func() { result <- true })
	dialog.AddButton("No").SetAsCancel().OnClick(func() { result <- false })
	dialog.Show()
	select {
	case approved := <-result:
		return approved
	case <-time.After(5 * time.Minute):
		return false
	}
}

// configPath 返回配置文件路径。
func (s *ComputerUseService) configPath() string {
	return filepath.Join(s.configDir, "koyori-ide", "computer_use.yaml")
}

// loadConfig 从磁盘加载配置。文件不存在时用默认配置（不报错）。
func (s *ComputerUseService) loadConfig() error {
	data, err := os.ReadFile(s.configPath())
	if err != nil {
		if os.IsNotExist(err) {
			s.mu.Lock()
			s.config = defaultComputerUseConfig()
			s.mu.Unlock()
			return nil
		}
		return fmt.Errorf("read computer_use config: %w", err)
	}
	var cfg ComputerUseConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("parse computer_use config: %w", err)
	}
	// 合并默认禁止快捷键：用户自定义 + 默认黑名单（取并集）。
	if len(cfg.ForbiddenHotkeys) == 0 {
		cfg.ForbiddenHotkeys = append([]string{}, defaultForbiddenHotkeys...)
	}
	s.mu.Lock()
	s.config = cloneComputerUseConfig(cfg)
	s.mu.Unlock()
	return nil
}

// saveConfig 持久化配置（G-SEC-09：atomicWriteFile 0600）。
func (s *ComputerUseService) saveConfig() error {
	previous, done := s.reserveConfigWrite()
	defer close(done)
	<-previous

	s.mu.RLock()
	configCopy := cloneComputerUseConfig(s.config)
	s.mu.RUnlock()

	data, err := yaml.Marshal(configCopy)
	if err != nil {
		return fmt.Errorf("marshal computer_use config: %w", err)
	}
	return s.persistConfigData(data)
}

// reserveConfigWrite reserves a position in the persistence sequence. Callers
// wait on previous and close done after their write and state commit complete.
func (s *ComputerUseService) reserveConfigWrite() (<-chan struct{}, chan struct{}) {
	s.mu.Lock()
	previous := s.persistTail
	if previous == nil {
		ready := make(chan struct{})
		close(ready)
		previous = ready
	}
	done := make(chan struct{})
	s.persistTail = done
	s.mu.Unlock()
	return previous, done
}

// persistConfigData writes a serialized snapshot without holding s.mu.
func (s *ComputerUseService) persistConfigData(data []byte) error {
	s.mu.RLock()
	persist := s.persistConfig
	s.mu.RUnlock()
	if persist != nil {
		return persist(data)
	}
	return atomicWriteFile(s.configPath(), data, 0600)
}

// GetConfig 返回当前配置的副本。
func (s *ComputerUseService) GetConfig() ComputerUseConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneComputerUseConfig(s.config)
}

// UpdateConfig 更新配置并持久化。Enabling Computer Use or changing the
// confirmation policy requires backend-native approval.
func (s *ComputerUseService) UpdateConfig(cfg ComputerUseConfig) error {
	configCopy := cloneComputerUseConfig(cfg)
	// 确保 ForbiddenHotkeys 始终包含默认黑名单。
	for _, def := range defaultForbiddenHotkeys {
		if !containsString(configCopy.ForbiddenHotkeys, def) {
			configCopy.ForbiddenHotkeys = append(configCopy.ForbiddenHotkeys, def)
		}
	}
	data, err := yaml.Marshal(configCopy)
	if err != nil {
		return fmt.Errorf("marshal computer_use config: %w", err)
	}

	previous, done := s.reserveConfigWrite()
	defer close(done)
	<-previous

	s.mu.RLock()
	current := cloneComputerUseConfig(s.config)
	approve := s.approveOperation
	s.mu.RUnlock()
	if (!current.Enabled && configCopy.Enabled) || current.ConfirmationRequired != configCopy.ConfirmationRequired {
		details := fmt.Sprintf(
			"enabled: %t -> %t\nconfirmation required: %t -> %t",
			current.Enabled,
			configCopy.Enabled,
			current.ConfirmationRequired,
			configCopy.ConfirmationRequired,
		)
		if approve == nil || !approve("update computer use settings", details) {
			return fmt.Errorf("computer use settings were not approved: %w", ErrNotAllowed)
		}
	}

	if err := s.persistConfigData(data); err != nil {
		return err
	}
	s.mu.Lock()
	s.config = configCopy
	s.configGeneration++
	s.mu.Unlock()
	return nil
}

// IsEnabled 返回 Computer Use 是否已启用。
func (s *ComputerUseService) IsEnabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config.Enabled
}

// ---------------------------------------------------------------------------
// 安全边界检查（Step 6）
// ---------------------------------------------------------------------------

// isHotkeyForbidden 检查快捷键是否在禁止黑名单中。
// 大小写不敏感比较。
func isHotkeyForbidden(forbidden []string, keys string) bool {
	normalized := strings.ToLower(strings.TrimSpace(keys))
	for _, f := range forbidden {
		if strings.ToLower(strings.TrimSpace(f)) == normalized {
			return true
		}
	}
	return false
}

// isPointInForbiddenZone 检查坐标是否落入禁止区域。
func isPointInForbiddenZone(zones []ForbiddenZone, x, y int) bool {
	for _, z := range zones {
		if x >= z.X && x < z.X+z.W && y >= z.Y && y < z.Y+z.H {
			return true
		}
	}
	return false
}

// checkSafety 检查操作是否通过安全边界。
// 返回 error 如果操作被禁止。
func (s *ComputerUseService) checkSafety(action string, args interface{}) (ComputerUseConfig, error) {
	cfg, _, err := s.checkSafetyAtGeneration(action, args)
	return cfg, err
}

func (s *ComputerUseService) checkSafetyAtGeneration(action string, args interface{}) (ComputerUseConfig, uint64, error) {
	s.mu.RLock()
	// N-8: 深拷贝 cfg，避免 TOCTOU — UpdateConfig 在 checkSafety 释放锁后
	// 修改 s.config，导致后续 Screenshot 等方法读到被篡改的 ConfirmationRequired。
	// 调用者基于返回的快照判断 ConfirmationRequired，不再直接读 s.config。
	cfg := s.cloneConfigLocked()
	generation := s.configGeneration
	s.mu.RUnlock()
	return cfg, generation, validateComputerUseSafety(cfg, action, args)
}

func validateComputerUseSafety(cfg ComputerUseConfig, action string, args interface{}) error {
	if !cfg.Enabled {
		return fmt.Errorf("computer use is disabled (G-SEC-12): %w", ErrNotAllowed)
	}

	switch action {
	case "mouse_move", "mouse_click":
		// 检查坐标是否在禁止区域。
		if coords, ok := args.(coordsArg); ok {
			if isPointInForbiddenZone(cfg.ForbiddenZones, coords.X, coords.Y) {
				return fmt.Errorf("coordinates (%d,%d) fall in forbidden zone (Step 6): %w",
					coords.X, coords.Y, ErrNotAllowed)
			}
		}
	case "keyboard_hotkey":
		// 检查快捷键是否在黑名单。
		if keys, ok := args.(string); ok {
			if isHotkeyForbidden(cfg.ForbiddenHotkeys, keys) {
				return fmt.Errorf("hotkey %q is forbidden (Step 6 OS safety): %w",
					keys, ErrNotAllowed)
			}
		}
	}
	return nil
}

// cloneConfigLocked 返回 s.config 的深拷贝（必须在持 RLock/Lock 时调用）。
// N-8: 深拷贝 slice 字段，避免与 s.config 共享底层数组，消除 TOCTOU 窗口。
func (s *ComputerUseService) cloneConfigLocked() ComputerUseConfig {
	return cloneComputerUseConfig(s.config)
}

// coordsArg 是 checkSafety 的坐标参数辅助类型。
type coordsArg struct {
	X, Y int
}

// ---------------------------------------------------------------------------
// 审计日志（Step 7）
// ---------------------------------------------------------------------------

// recordAudit 记录一条操作审计日志。
func (s *ComputerUseService) recordAudit(action, argsSummary string, success bool, confirmed bool, errMsg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := AuditAction{
		Timestamp:       time.Now().UTC(),
		Action:          action,
		Args:            argsSummary,
		Success:         success,
		ConfirmedByUser: confirmed,
		Error:           errMsg,
	}
	// H-10: 使用环形缓冲，避免 slice 截断导致底层数组无法 GC。
	s.auditLog.push(entry)
}

// GetAuditLog 返回审计日志副本（最近 N 条）。
func (s *ComputerUseService) GetAuditLog(limit int) []AuditAction {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.auditLog.latest(limit)
}

// ---------------------------------------------------------------------------
// Capability-approved tools (Step 1 / Step 2 / Step 3)
// ---------------------------------------------------------------------------

type screenshotOperationDetails struct {
	Region *image.Rectangle `json:"region"`
}

type mouseMoveOperationDetails struct {
	X int `json:"x"`
	Y int `json:"y"`
}

type mouseClickOperationDetails struct {
	Button string `json:"button"`
}

type keyboardTypeOperationDetails struct {
	Text string `json:"text"`
}

type keyboardHotkeyOperationDetails struct {
	Keys string `json:"keys"`
}

// ComputerUseOperationResult carries the optional screenshot payload. Other
// operations return an empty result object.
type ComputerUseOperationResult struct {
	Screenshot string `json:"screenshot,omitempty"`
}

// screenshot is a deny-only compatibility endpoint. Renderer execution must
// use RequestOperationApproval followed by ExecuteApprovedOperation.
//
//wails:ignore
func (s *ComputerUseService) screenshot(ctx context.Context, region *image.Rectangle) (string, error) {
	return "", fmt.Errorf("backend computer use approval token required: %w", ErrInvalidInput)
}

// mouseMove is a deny-only compatibility endpoint.
//
//wails:ignore
func (s *ComputerUseService) mouseMove(ctx context.Context, x, y int) error {
	return fmt.Errorf("backend computer use approval token required: %w", ErrInvalidInput)
}

// mouseClick is a deny-only compatibility endpoint.
//
//wails:ignore
func (s *ComputerUseService) mouseClick(ctx context.Context, button string) error {
	return fmt.Errorf("backend computer use approval token required: %w", ErrInvalidInput)
}

// keyboardType is a deny-only compatibility endpoint.
//
//wails:ignore
func (s *ComputerUseService) keyboardType(ctx context.Context, text string) error {
	return fmt.Errorf("backend computer use approval token required: %w", ErrInvalidInput)
}

// keyboardHotkey is a deny-only compatibility endpoint.
//
//wails:ignore
func (s *ComputerUseService) keyboardHotkey(ctx context.Context, keys string) error {
	return fmt.Errorf("backend computer use approval token required: %w", ErrInvalidInput)
}

// RequestOperationApproval creates a short-lived, single-use capability bound
// to one canonical action payload and the current configuration generation.
func (s *ComputerUseService) RequestOperationApproval(action, details string) (string, error) {
	action = strings.TrimSpace(action)
	canonicalDetails, safetyArg, _, err := parseComputerUseOperation(action, details)
	if err != nil {
		return "", err
	}
	_, generation, err := s.checkSafetyAtGeneration(action, safetyArg)
	if err != nil {
		s.recordAudit(action, computerUseAuditSummary(action, canonicalDetails), false, false, err.Error())
		return "", err
	}

	s.mu.RLock()
	approve := s.approveOperation
	s.mu.RUnlock()
	if approve == nil || !approve(action, canonicalDetails) {
		err := fmt.Errorf("computer use operation was not approved: %w", ErrNotAllowed)
		s.recordAudit(action, computerUseAuditSummary(action, canonicalDetails), false, false, err.Error())
		return "", err
	}

	if !s.approvalKeyReady {
		return "", fmt.Errorf("computer use approval key is unavailable: %w", ErrNotAllowed)
	}
	now := s.currentTime()
	approval := computerUseApproval{
		action:           action,
		details:          canonicalDetails,
		configGeneration: generation,
		expiresAt:        now.Add(computerUseApprovalTTL),
		confirmedByUser:  true,
	}
	for attempts := 0; attempts < 4; attempts++ {
		token, err := newComputerUseApprovalToken()
		if err != nil {
			return "", err
		}
		approval.binding = s.bindComputerUseApproval(token, approval)

		s.approvalMu.Lock()
		if s.approvals == nil {
			s.approvals = make(map[string]computerUseApproval)
		}
		for pendingToken, pendingApproval := range s.approvals {
			if !pendingApproval.expiresAt.After(now) {
				delete(s.approvals, pendingToken)
			}
		}
		if len(s.approvals) >= computerUseApprovalLimit {
			s.approvalMu.Unlock()
			return "", fmt.Errorf("too many pending computer use approvals: %w", ErrInvalidInput)
		}
		if _, exists := s.approvals[token]; exists {
			s.approvalMu.Unlock()
			continue
		}
		s.approvals[token] = approval
		s.approvalMu.Unlock()
		return token, nil
	}
	return "", fmt.Errorf("create unique computer use approval token: %w", ErrInvalidInput)
}

// ExecuteApprovedOperation atomically consumes a backend-issued capability and
// dispatches only the exact action and parameters stored in that capability.
func (s *ComputerUseService) ExecuteApprovedOperation(ctx context.Context, token string) (ComputerUseOperationResult, error) {
	if !isCanonicalComputerUseApprovalToken(token) {
		return ComputerUseOperationResult{}, fmt.Errorf("malformed computer use approval token: %w", ErrInvalidInput)
	}
	s.approvalMu.Lock()
	approval, ok := s.approvals[token]
	if ok {
		delete(s.approvals, token)
	}
	s.approvalMu.Unlock()
	if !ok {
		return ComputerUseOperationResult{}, fmt.Errorf("invalid or replayed computer use approval: %w", ErrInvalidInput)
	}
	if !s.approvalKeyReady || !s.computerUseApprovalBindingValid(token, approval) {
		return ComputerUseOperationResult{}, fmt.Errorf("tampered computer use approval: %w", ErrInvalidInput)
	}

	return s.executeComputerUseOperation(ctx, approval)
}

func newComputerUseApprovalToken() (string, error) {
	var raw [32]byte
	if _, err := crypto_rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("create computer use approval token: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func isCanonicalComputerUseApprovalToken(token string) bool {
	if len(token) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(token)
	return err == nil && hex.EncodeToString(decoded) == token
}

type computerUseApprovalBinding struct {
	Token             string `json:"token"`
	Action            string `json:"action"`
	Details           string `json:"details"`
	ConfigGeneration  uint64 `json:"configGeneration"`
	ExpiresAtUnixNano int64  `json:"expiresAtUnixNano"`
}

func (s *ComputerUseService) bindComputerUseApproval(token string, approval computerUseApproval) [sha256.Size]byte {
	payload, _ := json.Marshal(computerUseApprovalBinding{
		Token:             token,
		Action:            approval.action,
		Details:           approval.details,
		ConfigGeneration:  approval.configGeneration,
		ExpiresAtUnixNano: approval.expiresAt.UnixNano(),
	})
	mac := hmac.New(sha256.New, s.approvalKey[:])
	_, _ = mac.Write(payload)
	var binding [sha256.Size]byte
	copy(binding[:], mac.Sum(nil))
	return binding
}

func (s *ComputerUseService) computerUseApprovalBindingValid(token string, approval computerUseApproval) bool {
	expected := s.bindComputerUseApproval(token, approval)
	return hmac.Equal(approval.binding[:], expected[:])
}

func (s *ComputerUseService) currentTime() time.Time {
	s.mu.RLock()
	now := s.now
	s.mu.RUnlock()
	if now == nil {
		return time.Now()
	}
	return now()
}

func parseComputerUseOperation(action, details string) (canonical string, safetyArg interface{}, display string, err error) {
	action = strings.TrimSpace(action)
	switch action {
	case "screenshot":
		var parsed screenshotOperationDetails
		if err := decodeComputerUseDetails(details, &parsed); err != nil {
			return "", nil, "", err
		}
		canonical, err = marshalComputerUseDetails(parsed)
		return canonical, nil, fmt.Sprintf("region=%v", parsed.Region), err
	case "mouse_move":
		var parsed mouseMoveOperationDetails
		if err := decodeComputerUseDetails(details, &parsed); err != nil {
			return "", nil, "", err
		}
		canonical, err = marshalComputerUseDetails(parsed)
		return canonical, coordsArg(parsed), fmt.Sprintf("coordinates=(%d,%d)", parsed.X, parsed.Y), err
	case "mouse_click":
		var parsed mouseClickOperationDetails
		if err := decodeComputerUseDetails(details, &parsed); err != nil {
			return "", nil, "", err
		}
		parsed.Button = strings.ToLower(strings.TrimSpace(parsed.Button))
		if parsed.Button != "left" && parsed.Button != "right" && parsed.Button != "middle" {
			return "", nil, "", fmt.Errorf("mouse_click button must be left, right, or middle: %w", ErrInvalidInput)
		}
		canonical, err = marshalComputerUseDetails(parsed)
		return canonical, nil, "button=" + parsed.Button, err
	case "keyboard_type":
		var parsed keyboardTypeOperationDetails
		if err := decodeComputerUseDetails(details, &parsed); err != nil {
			return "", nil, "", err
		}
		if parsed.Text == "" {
			return "", nil, "", fmt.Errorf("keyboard_type text is required: %w", ErrInvalidInput)
		}
		canonical, err = marshalComputerUseDetails(parsed)
		return canonical, nil, fmt.Sprintf("text length=%d", len(parsed.Text)), err
	case "keyboard_hotkey":
		var parsed keyboardHotkeyOperationDetails
		if err := decodeComputerUseDetails(details, &parsed); err != nil {
			return "", nil, "", err
		}
		parsed.Keys = strings.TrimSpace(parsed.Keys)
		if parsed.Keys == "" {
			return "", nil, "", fmt.Errorf("keyboard_hotkey keys are required: %w", ErrInvalidInput)
		}
		canonical, err = marshalComputerUseDetails(parsed)
		return canonical, parsed.Keys, "keys=" + parsed.Keys, err
	default:
		return "", nil, "", fmt.Errorf("unsupported computer use action %q: %w", action, ErrInvalidInput)
	}
}

func decodeComputerUseDetails(details string, target interface{}) error {
	decoder := json.NewDecoder(strings.NewReader(details))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid computer use operation details: %v: %w", err, ErrInvalidInput)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("multiple JSON values")
		}
		return fmt.Errorf("invalid computer use operation details: %v: %w", err, ErrInvalidInput)
	}
	return nil
}

func marshalComputerUseDetails(details interface{}) (string, error) {
	encoded, err := json.Marshal(details)
	if err != nil {
		return "", fmt.Errorf("encode computer use operation details: %w", err)
	}
	return string(encoded), nil
}

func (s *ComputerUseService) executeComputerUseOperation(ctx context.Context, approval computerUseApproval) (ComputerUseOperationResult, error) {
	canonicalDetails, safetyArg, _, err := parseComputerUseOperation(approval.action, approval.details)
	if err != nil || canonicalDetails != approval.details {
		if err == nil {
			err = fmt.Errorf("computer use approval details are not canonical: %w", ErrInvalidInput)
		}
		return ComputerUseOperationResult{}, err
	}

	s.mu.RLock()
	now := s.now
	if now == nil {
		now = time.Now
	}
	if !approval.expiresAt.After(now()) || approval.configGeneration != s.configGeneration {
		s.mu.RUnlock()
		return ComputerUseOperationResult{}, fmt.Errorf("expired or stale computer use approval: %w", ErrInvalidInput)
	}
	cfg := s.cloneConfigLocked()
	if err := validateComputerUseSafety(cfg, approval.action, safetyArg); err != nil {
		s.mu.RUnlock()
		s.recordAudit(approval.action, computerUseAuditSummary(approval.action, approval.details), false, approval.confirmedByUser, err.Error())
		return ComputerUseOperationResult{}, err
	}

	var result ComputerUseOperationResult
	var operationErr error
	var auditSummary string
	switch approval.action {
	case "screenshot":
		var parsed screenshotOperationDetails
		_ = json.Unmarshal([]byte(approval.details), &parsed)
		imgBytes, platformErr := s.platform.Screenshot(parsed.Region)
		auditSummary = fmt.Sprintf("region=%v", parsed.Region)
		if platformErr != nil {
			operationErr = fmt.Errorf("screenshot: %w", platformErr)
		} else {
			result.Screenshot = base64.StdEncoding.EncodeToString(imgBytes)
			auditSummary = fmt.Sprintf("region=%v bytes=%d", parsed.Region, len(imgBytes))
		}
	case "mouse_move":
		var parsed mouseMoveOperationDetails
		_ = json.Unmarshal([]byte(approval.details), &parsed)
		auditSummary = fmt.Sprintf("(%d,%d)", parsed.X, parsed.Y)
		operationErr = s.platform.MouseMove(parsed.X, parsed.Y)
	case "mouse_click":
		var parsed mouseClickOperationDetails
		_ = json.Unmarshal([]byte(approval.details), &parsed)
		auditSummary = parsed.Button
		operationErr = s.platform.MouseClick(parsed.Button)
	case "keyboard_type":
		var parsed keyboardTypeOperationDetails
		_ = json.Unmarshal([]byte(approval.details), &parsed)
		auditSummary = fmt.Sprintf("len=%d", len(parsed.Text))
		operationErr = s.platform.KeyboardType(parsed.Text)
	case "keyboard_hotkey":
		var parsed keyboardHotkeyOperationDetails
		_ = json.Unmarshal([]byte(approval.details), &parsed)
		auditSummary = parsed.Keys
		operationErr = s.platform.KeyboardHotkey(parsed.Keys)
	}
	s.mu.RUnlock()
	if operationErr != nil {
		s.recordAudit(approval.action, auditSummary, false, approval.confirmedByUser, operationErr.Error())
		return ComputerUseOperationResult{}, operationErr
	}
	s.recordAudit(approval.action, auditSummary, true, approval.confirmedByUser, "")
	return result, nil
}

func computerUseAuditSummary(action, details string) string {
	switch action {
	case "keyboard_type":
		var parsed keyboardTypeOperationDetails
		if json.Unmarshal([]byte(details), &parsed) == nil {
			return fmt.Sprintf("len=%d", len(parsed.Text))
		}
	case "mouse_move":
		var parsed mouseMoveOperationDetails
		if json.Unmarshal([]byte(details), &parsed) == nil {
			return fmt.Sprintf("(%d,%d)", parsed.X, parsed.Y)
		}
	case "mouse_click":
		var parsed mouseClickOperationDetails
		if json.Unmarshal([]byte(details), &parsed) == nil {
			return parsed.Button
		}
	case "keyboard_hotkey":
		var parsed keyboardHotkeyOperationDetails
		if json.Unmarshal([]byte(details), &parsed) == nil {
			return parsed.Keys
		}
	case "screenshot":
		var parsed screenshotOperationDetails
		if json.Unmarshal([]byte(details), &parsed) == nil {
			return fmt.Sprintf("region=%v", parsed.Region)
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// 录制模式（Step 4）
// ---------------------------------------------------------------------------

// RecordedAction 是录制模式下捕获的用户操作。
type RecordedAction struct {
	Timestamp time.Time `json:"timestamp"`
	Action    string    `json:"action"`
	Args      string    `json:"args"`
}

// recordingSession 是录制模式的内存缓冲。
type recordingSession struct {
	mu      sync.Mutex
	actions []RecordedAction
	active  bool
}

// H-10: recording 从模块级 var 改为 ComputerUseService 的实例字段，
// 消除多实例间共享状态污染。

// StartRecording 开始录制模式（Step 4）。
func (s *ComputerUseService) StartRecording() error {
	s.mu.RLock()
	enabled := s.config.Enabled
	s.mu.RUnlock()
	if !enabled {
		return fmt.Errorf("computer use disabled, cannot record: %w", ErrNotAllowed)
	}
	s.recording.mu.Lock()
	defer s.recording.mu.Unlock()
	s.recording.active = true
	s.recording.actions = nil
	return nil
}

// StopRecording 停止录制并返回捕获的操作序列。
func (s *ComputerUseService) StopRecording() []RecordedAction {
	s.recording.mu.Lock()
	defer s.recording.mu.Unlock()
	s.recording.active = false
	out := make([]RecordedAction, len(s.recording.actions))
	copy(out, s.recording.actions)
	return out
}

// IsRecording 返回录制模式是否激活。
func (s *ComputerUseService) IsRecording() bool {
	s.recording.mu.Lock()
	defer s.recording.mu.Unlock()
	return s.recording.active
}

// recordAction 在录制模式下捕获操作（内部调用）。
// H-10: 接收 ComputerUseService 指针，使用实例级 recording 字段。
func (s *ComputerUseService) recordAction(action, args string) {
	s.recording.mu.Lock()
	defer s.recording.mu.Unlock()
	if !s.recording.active {
		return
	}
	s.recording.actions = append(s.recording.actions, RecordedAction{
		Timestamp: time.Now().UTC(),
		Action:    action,
		Args:      args,
	})
}
