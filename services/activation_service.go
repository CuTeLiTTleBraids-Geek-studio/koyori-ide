package services

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const activationReservationTTL = time.Minute

// ActivationService 管理 VS Code 扩展的 activationEvents 触发（F-3, prompt-2.md）。
// 它跟踪每个已安装扩展声明的 activationEvents，并在对应时机（打开文件、
// 工作区加载、命令调用、调试启动）返回需要激活的扩展 ID 列表。
//
// 支持的 activationEvent 类型：
//   - onCommand:<command>     — 命令被调用时激活
//   - onLanguage:<language>   — 打开对应语言文件时激活
//   - workspaceContains:<glob>— 工作区含匹配文件时激活
//   - onDebug                 — 调试会话启动时激活
//   - onDebugResolve:<type>   — 调试解析特定类型时激活
//   - *                       — 启动时 eager 激活（性能敏感，需用户确认）
type ActivationService struct {
	mu sync.Mutex
	// extensions 映射扩展 ID → 其声明的 activationEvents。
	// 扩展 ID 格式为 "publisher.name"。
	extensions map[string][]string
	// activated 记录已激活的扩展 ID，避免重复激活。
	activated map[string]bool
	// activating reserves candidates returned to the frontend so concurrent
	// triggers do not start the same extension twice before completion.
	activating map[string]time.Time
}

// NewActivationService 创建一个空的 ActivationService。
func NewActivationService() *ActivationService {
	return &ActivationService{
		extensions: make(map[string][]string),
		activated:  make(map[string]bool),
		activating: make(map[string]time.Time),
	}
}

// RegisterExtension 注册一个扩展的 activationEvents。extensionID 格式为
// "publisher.name"。重复注册会覆盖旧值。F-3 (prompt-2.md)。
func (a *ActivationService) RegisterExtension(extensionID string, events []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.extensions[extensionID] = events
}

// UnregisterExtension 移除一个扩展的注册信息。
func (a *ActivationService) UnregisterExtension(extensionID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.extensions, extensionID)
	delete(a.activated, extensionID)
	delete(a.activating, extensionID)
}

// extensionEventsSnapshot returns a copy of one registration for transactional
// marketplace updates. The activation service remains the owner of its maps.
func (a *ActivationService) extensionEventsSnapshot(extensionID string) ([]string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	events, ok := a.extensions[extensionID]
	if !ok {
		return nil, false
	}
	return append([]string(nil), events...), true
}

// restoreExtensionEvents restores only the registration, not Worker activation
// state. The renderer reports Worker activation separately after rollback.
func (a *ActivationService) restoreExtensionEvents(extensionID string, events []string, registered bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !registered {
		delete(a.extensions, extensionID)
		return
	}
	a.extensions[extensionID] = append([]string(nil), events...)
}

// TriggerOnLanguage 返回因 onLanguage:<language> 事件而需要激活的扩展 ID 列表
// （尚未激活或正在激活的除外）。F-3 (prompt-2.md)。
func (a *ActivationService) TriggerOnLanguage(language string) []string {
	prefix := "onLanguage:" + language
	return a.triggerMatching(func(event string) bool {
		return event == prefix
	})
}

// TriggerOnCommand 返回因 onCommand:<command> 事件而需要激活的扩展 ID 列表。
// 返回的扩展会保留到前端报告激活结果。F-3 (prompt-2.md)。
func (a *ActivationService) TriggerOnCommand(command string) []string {
	prefix := "onCommand:" + command
	return a.triggerMatching(func(event string) bool {
		return event == prefix
	})
}

// TriggerOnDebug 返回因 onDebug 事件而需要激活的扩展 ID 列表。
// 返回的扩展会保留到前端报告激活结果。F-3 (prompt-2.md)。
func (a *ActivationService) TriggerOnDebug() []string {
	return a.triggerMatching(func(event string) bool {
		return event == "onDebug"
	})
}

// TriggerOnDebugResolve 返回因 onDebugResolve:<type> 事件而需要激活的扩展列表。
func (a *ActivationService) TriggerOnDebugResolve(debugType string) []string {
	prefix := "onDebugResolve:" + debugType
	return a.triggerMatching(func(event string) bool {
		return event == prefix
	})
}

// TriggerWorkspaceContains 扫描 workspaceRoot 下的文件，返回因
// workspaceContains:<glob> 事件而需要激活的扩展列表。glob 匹配使用
// filepath.Match（支持 * ? [...]）。仅扫描根目录直接子文件 + 递归匹配
// 常见 manifest 文件名（如 go.mod, package.json, Cargo.toml）。
// F-3 (prompt-2.md)。
func (a *ActivationService) TriggerWorkspaceContains(workspaceRoot string) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var toActivate []string
	for extID, events := range a.extensions {
		if a.activated[extID] || a.isActivatingLocked(extID) {
			continue
		}
		for _, event := range events {
			if !strings.HasPrefix(event, "workspaceContains:") {
				continue
			}
			pattern := strings.TrimPrefix(event, "workspaceContains:")
			if pattern == "" {
				continue
			}
			if matchWorkspacePattern(workspaceRoot, pattern) {
				toActivate = append(toActivate, extID)
				a.activating[extID] = time.Now()
				break
			}
		}
	}
	return toActivate
}

// TriggerEager 返回声明了 "*" activationEvent 的扩展列表（启动时 eager 激活）。
// 性能敏感：调用方应提示用户确认是否启用这些扩展。F-3 (prompt-2.md)。
func (a *ActivationService) TriggerEager() []string {
	return a.triggerMatching(func(event string) bool {
		return event == "*"
	})
}

// IsActivated 返回扩展是否已激活。
func (a *ActivationService) IsActivated(extensionID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.activated[extensionID]
}

// ReportActivationResult commits a successful frontend/Worker activation or
// releases a failed candidate so a later matching event can retry it.
func (a *ActivationService) ReportActivationResult(extensionID string, activated bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.isActivatingLocked(extensionID) {
		return
	}
	delete(a.activating, extensionID)
	if activated {
		a.activated[extensionID] = true
	}
}

// ReportDeactivated clears committed and in-flight activation state so a
// later matching event can activate the extension again.
func (a *ActivationService) ReportDeactivated(extensionID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.activating, extensionID)
	delete(a.activated, extensionID)
}

// ResetActivated 重置所有扩展的激活状态（不删除注册信息）。用于测试。
func (a *ActivationService) ResetActivated() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.activated = make(map[string]bool)
	a.activating = make(map[string]time.Time)
}

// triggerMatching 是内部 helper：遍历所有注册的扩展，对每个扩展检查其
// activationEvents 是否有匹配 match 函数的事件。匹配且空闲的扩展被收集
// 并标记为正在激活。调用方必须不持有 a.mu。
func (a *ActivationService) triggerMatching(match func(event string) bool) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var toActivate []string
	for extID, events := range a.extensions {
		if a.activated[extID] || a.isActivatingLocked(extID) {
			continue
		}
		for _, event := range events {
			if match(event) {
				toActivate = append(toActivate, extID)
				a.activating[extID] = time.Now()
				break
			}
		}
	}
	return toActivate
}

func (a *ActivationService) isActivatingLocked(extensionID string) bool {
	startedAt, ok := a.activating[extensionID]
	if !ok {
		return false
	}
	if time.Since(startedAt) < activationReservationTTL {
		return true
	}
	delete(a.activating, extensionID)
	return false
}

// matchWorkspacePattern 检查 workspaceRoot 下是否存在匹配 pattern 的文件。
// pattern 可以是 glob（如 "*.go"）或具体文件名（如 "go.mod"）。
// 为了性能，仅扫描根目录的直接子文件 + 递归查找常见 manifest 文件。
// F-3 (prompt-2.md)。
func matchWorkspacePattern(workspaceRoot, pattern string) bool {
	if workspaceRoot == "" {
		return false
	}
	// 直接检查根目录下的文件
	entries, err := os.ReadDir(workspaceRoot)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		name := entry.Name()
		matched, _ := filepath.Match(pattern, name)
		if matched {
			return true
		}
	}
	// 递归查找（限制深度为 3 层，避免大工作区扫描过慢）
	return recursiveMatch(workspaceRoot, pattern, 3)
}

// recursiveMatch 递归查找匹配 pattern 的文件，最多递归 maxDepth 层。
func recursiveMatch(dir, pattern string, maxDepth int) bool {
	if maxDepth <= 0 {
		return false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() {
			// 跳过隐藏目录和 vendor/node_modules
			name := entry.Name()
			if strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules" {
				continue
			}
			if recursiveMatch(filepath.Join(dir, name), pattern, maxDepth-1) {
				return true
			}
		} else {
			matched, _ := filepath.Match(pattern, entry.Name())
			if matched {
				return true
			}
		}
	}
	return false
}
