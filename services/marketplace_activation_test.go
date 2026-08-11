package services

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// marketplace_activation_test.go — F-3 (prompt-2.md) 测试。
//
// 覆盖：
//   - ActivationService 的 6 种触发类型（onLanguage/onCommand/workspaceContains/
//     onDebug/onDebugResolve/*）
//   - marketplace 安装扩展后自动注册 activationEvents
//   - marketplace 卸载扩展后自动注销
//   - parseVSIXManifest 填充 ParsedContributes（含 views/menus/keybindings 等新字段）
//   - GetInstalledExtensionManifests 返回带 ParsedContributes 的 manifest
//   - marketplace 转发 TriggerActivationOnLanguage/OnCommand/WorkspaceContains 等
//   - ActivationService 重复触发不重复激活（幂等）
//   - ExtensionContributes 新增字段（Views/Menus/Keybindings/Themes）解析

// --- ActivationService 基础触发 ---

// TestF3_ActivationService_OnLanguage 验证 onLanguage:<lang> 事件触发激活。
func TestF3_ActivationService_OnLanguage(t *testing.T) {
	as := NewActivationService()
	as.RegisterExtension("acme.go", []string{"onLanguage:go", "onCommand:acme.run"})
	as.RegisterExtension("acme.ts", []string{"onLanguage:typescript"})

	activated := as.TriggerOnLanguage("go")
	if len(activated) != 1 || activated[0] != "acme.go" {
		t.Fatalf("TriggerOnLanguage(go) = %v, want [acme.go]", activated)
	}
	if as.IsActivated("acme.go") {
		t.Fatal("candidate should not be activated before frontend completion")
	}
	as.ReportActivationResult("acme.go", true)
	// 再次触发应返回空（已激活）。
	if got := as.TriggerOnLanguage("go"); len(got) != 0 {
		t.Fatalf("second TriggerOnLanguage(go) = %v, want []", got)
	}
	// typescript 扩展未被 go 触发。
	if as.IsActivated("acme.ts") {
		t.Fatal("acme.ts should not be activated by onLanguage:go")
	}
	// 触发 typescript。
	if got := as.TriggerOnLanguage("typescript"); len(got) != 1 || got[0] != "acme.ts" {
		t.Fatalf("TriggerOnLanguage(typescript) = %v, want [acme.ts]", got)
	}
}

// TestF3_ActivationService_OnCommand 验证 onCommand:<cmd> 事件触发激活。
func TestF3_ActivationService_OnCommand(t *testing.T) {
	as := NewActivationService()
	as.RegisterExtension("acme.hello", []string{"onCommand:acme.hello.sayHi"})

	if got := as.TriggerOnCommand("acme.hello.sayHi"); len(got) != 1 || got[0] != "acme.hello" {
		t.Fatalf("TriggerOnCommand = %v, want [acme.hello]", got)
	}
	// 不匹配的命令不激活。
	if got := as.TriggerOnCommand("other.cmd"); len(got) != 0 {
		t.Fatalf("TriggerOnCommand(other) = %v, want []", got)
	}
}

// TestF3_ActivationService_OnDebug 验证 onDebug 事件触发激活。
func TestF3_ActivationService_OnDebug(t *testing.T) {
	as := NewActivationService()
	as.RegisterExtension("acme.debugger", []string{"onDebug"})
	as.RegisterExtension("acme.other", []string{"onLanguage:go"})

	if got := as.TriggerOnDebug(); len(got) != 1 || got[0] != "acme.debugger" {
		t.Fatalf("TriggerOnDebug = %v, want [acme.debugger]", got)
	}
	if as.IsActivated("acme.other") {
		t.Fatal("acme.other should not be activated by onDebug")
	}
}

// TestF3_ActivationService_OnDebugResolve 验证 onDebugResolve:<type> 事件。
func TestF3_ActivationService_OnDebugResolve(t *testing.T) {
	as := NewActivationService()
	as.RegisterExtension("acme.go-debug", []string{"onDebugResolve:go"})

	if got := as.TriggerOnDebugResolve("go"); len(got) != 1 || got[0] != "acme.go-debug" {
		t.Fatalf("TriggerOnDebugResolve(go) = %v, want [acme.go-debug]", got)
	}
	if got := as.TriggerOnDebugResolve("python"); len(got) != 0 {
		t.Fatalf("TriggerOnDebugResolve(python) = %v, want []", got)
	}
}

// TestF3_ActivationService_Eager 验证 "*" activationEvent 触发 eager 激活。
func TestF3_ActivationService_Eager(t *testing.T) {
	as := NewActivationService()
	as.RegisterExtension("acme.eager", []string{"*"})
	as.RegisterExtension("acme.lazy", []string{"onLanguage:go"})

	if got := as.TriggerEager(); len(got) != 1 || got[0] != "acme.eager" {
		t.Fatalf("TriggerEager = %v, want [acme.eager]", got)
	}
	if as.IsActivated("acme.lazy") {
		t.Fatal("acme.lazy should not be activated by TriggerEager")
	}
}

// TestF3_ActivationService_WorkspaceContains 验证 workspaceContains:<glob>
// 在工作区根目录匹配文件时触发激活。
func TestF3_ActivationService_WorkspaceContains(t *testing.T) {
	tmp := t.TempDir()
	// 创建 go.mod 触发 workspaceContains:go.mod。
	if err := os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module test"), 0o644); err != nil {
		t.Fatalf("create go.mod: %v", err)
	}

	as := NewActivationService()
	as.RegisterExtension("acme.go-ext", []string{"workspaceContains:go.mod"})
	as.RegisterExtension("acme.py-ext", []string{"workspaceContains:requirements.txt"})

	if got := as.TriggerWorkspaceContains(tmp); len(got) != 1 || got[0] != "acme.go-ext" {
		t.Fatalf("TriggerWorkspaceContains = %v, want [acme.go-ext]", got)
	}
	if as.IsActivated("acme.py-ext") {
		t.Fatal("acme.py-ext should not be activated (no requirements.txt)")
	}
}

// TestF3_ActivationService_WorkspaceContains_Recursive 验证递归匹配子目录文件。
func TestF3_ActivationService_WorkspaceContains_Recursive(t *testing.T) {
	tmp := t.TempDir()
	sub := filepath.Join(tmp, "subdir")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// 在子目录创建 package.json，workspaceContains:package.json 应递归匹配。
	if err := os.WriteFile(filepath.Join(sub, "package.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("create package.json: %v", err)
	}

	as := NewActivationService()
	as.RegisterExtension("acme.node", []string{"workspaceContains:package.json"})

	if got := as.TriggerWorkspaceContains(tmp); len(got) != 1 || got[0] != "acme.node" {
		t.Fatalf("TriggerWorkspaceContains = %v, want [acme.node]", got)
	}
}

// TestF3_ActivationService_Unregister 验证注销后不再触发激活。
func TestF3_ActivationService_Unregister(t *testing.T) {
	as := NewActivationService()
	as.RegisterExtension("acme.ext", []string{"onLanguage:go"})
	as.UnregisterExtension("acme.ext")

	if got := as.TriggerOnLanguage("go"); len(got) != 0 {
		t.Fatalf("after Unregister, TriggerOnLanguage = %v, want []", got)
	}
}

// TestF3_ActivationService_ResetActivated 验证重置激活状态后可重新触发。
func TestF3_ActivationService_ResetActivated(t *testing.T) {
	as := NewActivationService()
	as.RegisterExtension("acme.ext", []string{"onLanguage:go"})

	as.TriggerOnLanguage("go")
	as.ReportActivationResult("acme.ext", true)
	if !as.IsActivated("acme.ext") {
		t.Fatal("should be activated after successful completion")
	}
	as.ResetActivated()
	if as.IsActivated("acme.ext") {
		t.Fatal("should not be activated after reset")
	}
	// 重置后可再次触发。
	if got := as.TriggerOnLanguage("go"); len(got) != 1 || got[0] != "acme.ext" {
		t.Fatalf("after reset, TriggerOnLanguage = %v, want [acme.ext]", got)
	}
}

// --- marketplace 集成：安装/卸载时自动注册/注销 ---

// TestF3_Marketplace_Install_RegistersActivationEvents 验证安装扩展后
// ActivationService 收到 activationEvents 注册。
func TestF3_Marketplace_Install_RegistersActivationEvents(t *testing.T) {
	svc, _ := newTestMarketplaceService(t)
	as := NewActivationService()
	svc.setActivationService(as)

	// 用含 activationEvents 的 package.json 构造 VSIX。
	pkgJSON := `{
		"name": "hello",
		"publisher": "acme",
		"version": "1.0.0",
		"activationEvents": ["onLanguage:go", "onCommand:acme.hello"],
		"contributes": { "commands": [{ "command": "acme.hello", "title": "Hello" }] }
	}`
	vsix, wantHash := buildVSIX(t, []zipEntry{
		{Name: "extension/package.json", Data: []byte(pkgJSON)},
	})
	if err := svc.installFromVSIXData(vsix, wantHash, "acme", "hello", "1.0.0"); err != nil {
		t.Fatalf("install failed: %v", err)
	}

	// onLanguage:go 应触发激活 acme.hello。
	if got := as.TriggerOnLanguage("go"); len(got) != 1 || got[0] != "acme.hello" {
		t.Fatalf("TriggerOnLanguage(go) = %v, want [acme.hello]", got)
	}
	// onCommand:acme.hello 应不触发（已激活）。
	if got := as.TriggerOnCommand("acme.hello"); len(got) != 0 {
		t.Fatalf("TriggerOnCommand after activation = %v, want []", got)
	}
}

// TestF3_Marketplace_Uninstall_UnregistersActivationEvents 验证卸载扩展后
// ActivationService 注销其 activationEvents。
func TestF3_Marketplace_Uninstall_UnregistersActivationEvents(t *testing.T) {
	svc, _ := newTestMarketplaceService(t)
	as := NewActivationService()
	svc.setActivationService(as)

	pkgJSON := `{
		"name": "hello", "publisher": "acme", "version": "1.0.0",
		"activationEvents": ["onLanguage:go"]
	}`
	vsix, wantHash := buildVSIX(t, []zipEntry{
		{Name: "extension/package.json", Data: []byte(pkgJSON)},
	})
	if err := svc.installFromVSIXData(vsix, wantHash, "acme", "hello", "1.0.0"); err != nil {
		t.Fatalf("install: %v", err)
	}
	// 卸载。
	if err := svc.UninstallExtension("acme", "hello"); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	// 卸载后 TriggerOnLanguage 应返回空。
	if got := as.TriggerOnLanguage("go"); len(got) != 0 {
		t.Fatalf("after uninstall, TriggerOnLanguage = %v, want []", got)
	}
}

// TestF3_Marketplace_TriggerActivationForwards 验证 marketplace 转发
// TriggerActivationOnLanguage/OnCommand/OnDebug 等便捷方法，且未注入
// ActivationService 时返回 nil（向后兼容）。
func TestF3_Marketplace_TriggerActivationForwards(t *testing.T) {
	svc, _ := newTestMarketplaceService(t)
	// 未注入 ActivationService —— 应返回 nil 不 panic。
	if got := svc.TriggerActivationOnLanguage("go"); got != nil {
		t.Fatalf("without ActivationService, expected nil, got %v", got)
	}
	if got := svc.TriggerActivationEager(); got != nil {
		t.Fatalf("without ActivationService, expected nil, got %v", got)
	}
	if svc.IsExtensionActivated("any") {
		t.Fatal("without ActivationService, IsExtensionActivated should be false")
	}

	// 注入后转发。
	as := NewActivationService()
	svc.setActivationService(as)
	as.RegisterExtension("acme.ext", []string{"onLanguage:go", "onDebug", "*"})

	if got := svc.TriggerActivationOnLanguage("go"); len(got) != 1 || got[0] != "acme.ext" {
		t.Fatalf("TriggerActivationOnLanguage = %v, want [acme.ext]", got)
	}
	svc.ReportExtensionActivation("acme.ext", true)
	if got := svc.TriggerActivationOnDebug(); len(got) != 0 {
		t.Fatalf("after language activation, TriggerActivationOnDebug = %v, want []", got)
	}
}

func TestF3_ActivationService_FailedActivationCanRetry(t *testing.T) {
	as := NewActivationService()
	as.RegisterExtension("acme.retry", []string{"onLanguage:go"})

	if got := as.TriggerOnLanguage("go"); len(got) != 1 {
		t.Fatalf("first trigger = %v, want candidate", got)
	}
	if got := as.TriggerOnLanguage("go"); len(got) != 0 {
		t.Fatalf("concurrent trigger = %v, want reserved candidate", got)
	}
	as.ReportActivationResult("acme.retry", false)
	if got := as.TriggerOnLanguage("go"); len(got) != 1 || got[0] != "acme.retry" {
		t.Fatalf("trigger after failure = %v, want retry candidate", got)
	}
}

func TestF3_ActivationService_DeactivatedExtensionCanReactivate(t *testing.T) {
	as := NewActivationService()
	as.RegisterExtension("acme.retry", []string{"onLanguage:go"})

	as.TriggerOnLanguage("go")
	as.ReportActivationResult("acme.retry", true)
	as.ReportDeactivated("acme.retry")
	if got := as.TriggerOnLanguage("go"); len(got) != 1 || got[0] != "acme.retry" {
		t.Fatalf("trigger after deactivation = %v, want reactivation candidate", got)
	}
}

func TestF3_ActivationService_IgnoresUnsolicitedActivationResult(t *testing.T) {
	as := NewActivationService()
	as.RegisterExtension("acme.ext", []string{"onLanguage:go"})

	as.ReportActivationResult("acme.ext", true)
	if as.IsActivated("acme.ext") {
		t.Fatal("unsolicited result should not mark extension activated")
	}
}

func TestF3_ActivationService_InterruptedActivationReservationExpires(t *testing.T) {
	as := NewActivationService()
	as.RegisterExtension("acme.retry", []string{"onLanguage:go"})
	as.TriggerOnLanguage("go")

	as.mu.Lock()
	as.activating["acme.retry"] = time.Now().Add(-activationReservationTTL)
	as.mu.Unlock()
	if got := as.TriggerOnLanguage("go"); len(got) != 1 || got[0] != "acme.retry" {
		t.Fatalf("trigger after reservation expiry = %v, want retry candidate", got)
	}
}

// --- parseVSIXManifest 填充 ParsedContributes ---

// TestF3_ParseVSIXManifest_ParsedContributes 验证 parseVSIXManifest 把
// contributes 解析为 ParsedContributes，含 commands/views/menus/keybindings
// 等字段。
func TestF3_ParseVSIXManifest_ParsedContributes(t *testing.T) {
	tmp := t.TempDir()
	extDir := filepath.Join(tmp, "acme.hello")
	pkgDir := filepath.Join(extDir, "extension")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	pkgJSON := `{
		"name": "hello", "publisher": "acme", "version": "1.0.0",
		"activationEvents": ["onLanguage:go"],
		"contributes": {
			"commands": [{ "command": "acme.hello", "title": "Hello" }],
			"views": { "explorer": [{ "id": "acme.helloView", "name": "Hello" }] },
			"menus": { "commandPalette": [{ "command": "acme.hello" }] },
			"keybindings": [{ "command": "acme.hello", "key": "ctrl+f1" }],
			"grammars": [{ "scopeName": "source.go", "path": "./syntaxes/go.json" }],
			"snippets": [{ "language": "go", "path": "./snippets/go.json" }]
		}
	}`
	if err := os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(pkgJSON), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}

	m, err := parseVSIXManifest(extDir)
	if err != nil {
		t.Fatalf("parseVSIXManifest: %v", err)
	}
	if m.ExtensionID() != "acme.hello" {
		t.Errorf("ExtensionID = %q, want acme.hello", m.ExtensionID())
	}
	pc := m.ParsedContributes
	if len(pc.Commands) != 1 || pc.Commands[0].Command != "acme.hello" {
		t.Errorf("Commands = %+v, want [{acme.hello}]", pc.Commands)
	}
	if len(pc.Views["explorer"]) != 1 || pc.Views["explorer"][0].ID != "acme.helloView" {
		t.Errorf("Views[explorer] = %+v, want [{acme.helloView}]", pc.Views["explorer"])
	}
	if len(pc.Menus["commandPalette"]) != 1 || pc.Menus["commandPalette"][0].Command != "acme.hello" {
		t.Errorf("Menus[commandPalette] = %+v, want [{acme.hello}]", pc.Menus["commandPalette"])
	}
	if len(pc.Keybindings) != 1 || pc.Keybindings[0].Key != "ctrl+f1" {
		t.Errorf("Keybindings = %+v, want [{ctrl+f1}]", pc.Keybindings)
	}
	if len(pc.Grammars) != 1 || pc.Grammars[0].ScopeName != "source.go" {
		t.Errorf("Grammars = %+v, want [{source.go}]", pc.Grammars)
	}
	if len(pc.Snippets) != 1 || pc.Snippets[0].Language != "go" {
		t.Errorf("Snippets = %+v, want [{go}]", pc.Snippets)
	}
}

// TestF3_GetInstalledExtensionManifests 验证 GetInstalledExtensionManifests
// 返回所有已安装扩展的 manifest，且 ParsedContributes 已填充。
func TestF3_GetInstalledExtensionManifests(t *testing.T) {
	svc, _ := newTestMarketplaceService(t)

	// 安装两个扩展。
	pkgA := `{"name":"ext-a","publisher":"alpha","version":"1.0.0","activationEvents":["onLanguage:go"],"contributes":{"commands":[{"command":"alpha.cmd","title":"A"}]}}`
	pkgB := `{"name":"ext-b","publisher":"beta","version":"1.0.0","contributes":{"grammars":[{"scopeName":"source.ts","path":"./ts.json"}]}}`
	for _, p := range []struct {
		publisher, name, pkg string
	}{
		{"alpha", "ext-a", pkgA},
		{"beta", "ext-b", pkgB},
	} {
		vsix, hash := buildVSIX(t, []zipEntry{
			{Name: "extension/package.json", Data: []byte(p.pkg)},
		})
		if err := svc.installFromVSIXData(vsix, hash, p.publisher, p.name, "1.0.0"); err != nil {
			t.Fatalf("install %s.%s: %v", p.publisher, p.name, err)
		}
	}

	manifests, err := svc.GetInstalledExtensionManifests()
	if err != nil {
		t.Fatalf("GetInstalledExtensionManifests: %v", err)
	}
	if len(manifests) != 2 {
		t.Fatalf("got %d manifests, want 2", len(manifests))
	}
	// 按 ExtensionID 排序便于断言。
	sort.Slice(manifests, func(i, j int) bool {
		return manifests[i].ExtensionID() < manifests[j].ExtensionID()
	})
	if manifests[0].ExtensionID() != "alpha.ext-a" {
		t.Errorf("manifests[0] = %s, want alpha.ext-a", manifests[0].ExtensionID())
	}
	if len(manifests[0].ParsedContributes.Commands) != 1 {
		t.Errorf("alpha.ext-a Commands len = %d, want 1", len(manifests[0].ParsedContributes.Commands))
	}
	if manifests[1].ExtensionID() != "beta.ext-b" {
		t.Errorf("manifests[1] = %s, want beta.ext-b", manifests[1].ExtensionID())
	}
	if len(manifests[1].ParsedContributes.Grammars) != 1 {
		t.Errorf("beta.ext-b Grammars len = %d, want 1", len(manifests[1].ParsedContributes.Grammars))
	}
}

// TestF3_ExtensionContributes_NewFields 验证 ExtensionContributes 新增字段
// （Views/ViewsWelcome/Menus/Keybindings/Themes/IconThemes）的 JSON 解析。
func TestF3_ExtensionContributes_NewFields(t *testing.T) {
	src := `{
		"name": "rich", "publisher": "acme", "version": "1.0.0",
		"contributes": {
			"views": {
				"explorer": [{ "id": "acme.explorer", "name": "Explorer View" }],
				"debug": [{ "id": "acme.debug", "name": "Debug View" }]
			},
			"viewsWelcome": [{ "view": "acme.explorer", "contents": "Welcome" }],
			"menus": { "editor/context": [{ "command": "acme.ctx", "group": "navigation" }] },
			"keybindings": [{ "command": "acme.kb", "key": "ctrl+shift+k", "mac": "cmd+shift+k" }],
			"themes": [{ "label": "Dark", "uiTheme": "vs-dark", "path": "./themes/dark.json" }],
			"iconThemes": [{ "id": "acme-icons", "label": "Acme Icons", "path": "./icons.json" }]
		}
	}`
	m, err := ParseExtensionManifest(src)
	if err != nil {
		t.Fatalf("ParseExtensionManifest: %v", err)
	}
	c := m.Contributes
	if len(c.Views["explorer"]) != 1 || c.Views["explorer"][0].ID != "acme.explorer" {
		t.Errorf("Views[explorer] = %+v", c.Views["explorer"])
	}
	if len(c.Views["debug"]) != 1 || c.Views["debug"][0].ID != "acme.debug" {
		t.Errorf("Views[debug] = %+v", c.Views["debug"])
	}
	if len(c.ViewsWelcome) != 1 || c.ViewsWelcome[0].View != "acme.explorer" {
		t.Errorf("ViewsWelcome = %+v", c.ViewsWelcome)
	}
	if len(c.Menus["editor/context"]) != 1 || c.Menus["editor/context"][0].Command != "acme.ctx" {
		t.Errorf("Menus[editor/context] = %+v", c.Menus["editor/context"])
	}
	if len(c.Keybindings) != 1 || c.Keybindings[0].Key != "ctrl+shift+k" || c.Keybindings[0].Mac != "cmd+shift+k" {
		t.Errorf("Keybindings = %+v", c.Keybindings)
	}
	if len(c.Themes) != 1 || c.Themes[0].Label != "Dark" || c.Themes[0].UI != "vs-dark" {
		t.Errorf("Themes = %+v", c.Themes)
	}
	if len(c.IconThemes) != 1 || c.IconThemes[0].ID != "acme-icons" {
		t.Errorf("IconThemes = %+v", c.IconThemes)
	}
}

// TestF3_ExtensionID_EmptyWhenMissingPublisher 验证 ExtensionID 在缺少
// publisher/name 时返回空字符串。
func TestF3_ExtensionID_EmptyWhenMissingPublisher(t *testing.T) {
	m := &VSCodeExtensionManifest{Name: "only-name"}
	if m.ExtensionID() != "" {
		t.Errorf("ExtensionID = %q, want empty", m.ExtensionID())
	}
	m = &VSCodeExtensionManifest{Publisher: "only-pub"}
	if m.ExtensionID() != "" {
		t.Errorf("ExtensionID = %q, want empty", m.ExtensionID())
	}
	m = nil
	if m.ExtensionID() != "" {
		t.Errorf("nil ExtensionID = %q, want empty", m.ExtensionID())
	}
}

// TestF3_ActivationService_TriggerOrder 验证多扩展同时匹配时全部激活，
// 且激活顺序按注册顺序（Go map 迭代无序，此处仅验证集合相等）。
func TestF3_ActivationService_TriggerOrder(t *testing.T) {
	as := NewActivationService()
	as.RegisterExtension("acme.a", []string{"onLanguage:go"})
	as.RegisterExtension("acme.b", []string{"onLanguage:go"})
	as.RegisterExtension("acme.c", []string{"onLanguage:go"})

	got := as.TriggerOnLanguage("go")
	if len(got) != 3 {
		t.Fatalf("TriggerOnLanguage = %v, want 3 items", got)
	}
	sort.Strings(got)
	want := []string{"acme.a", "acme.b", "acme.c"}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("got[%d] = %q, want %q", i, got[i], w)
		}
	}
}

// TestF3_ActivationService_WorkspaceContains_EmptyRoot 验证空 workspaceRoot
// 不触发激活。
func TestF3_ActivationService_WorkspaceContains_EmptyRoot(t *testing.T) {
	as := NewActivationService()
	as.RegisterExtension("acme.ext", []string{"workspaceContains:go.mod"})
	if got := as.TriggerWorkspaceContains(""); len(got) != 0 {
		t.Fatalf("TriggerWorkspaceContains(empty) = %v, want []", got)
	}
}

// TestF3_Marketplace_Install_NoActivationEvents 验证扩展未声明 activationEvents
// 时不注册到 ActivationService（避免无意义条目）。
func TestF3_Marketplace_Install_NoActivationEvents(t *testing.T) {
	svc, _ := newTestMarketplaceService(t)
	as := NewActivationService()
	svc.setActivationService(as)

	pkgJSON := `{"name":"noevents","publisher":"acme","version":"1.0.0"}`
	vsix, wantHash := buildVSIX(t, []zipEntry{
		{Name: "extension/package.json", Data: []byte(pkgJSON)},
	})
	if err := svc.installFromVSIXData(vsix, wantHash, "acme", "noevents", "1.0.0"); err != nil {
		t.Fatalf("install: %v", err)
	}
	// 无 activationEvents，不应被任何触发激活。
	if got := as.TriggerOnLanguage("go"); len(got) != 0 {
		t.Fatalf("TriggerOnLanguage = %v, want []", got)
	}
	if got := as.TriggerEager(); len(got) != 0 {
		t.Fatalf("TriggerEager = %v, want []", got)
	}
}

// TestF3_Marketplace_LoadInstalledManifestsForActivation 验证启动时扫描
// 已安装扩展的 manifest 能取到 activationEvents，用于补注册到
// ActivationService（main.go 启动路径）。
func TestF3_Marketplace_LoadInstalledManifestsForActivation(t *testing.T) {
	svc, _ := newTestMarketplaceService(t)

	pkgJSON := `{"name":"persist","publisher":"acme","version":"1.0.0","activationEvents":["onLanguage:go","onCommand:acme.persist"]}`
	vsix, wantHash := buildVSIX(t, []zipEntry{
		{Name: "extension/package.json", Data: []byte(pkgJSON)},
	})
	if err := svc.installFromVSIXData(vsix, wantHash, "acme", "persist", "1.0.0"); err != nil {
		t.Fatalf("install: %v", err)
	}

	// 模拟 main.go 启动路径：扫描已安装扩展，注册到新的 ActivationService。
	installed, err := svc.GetInstalledExtensionManifests()
	if err != nil {
		t.Fatalf("GetInstalledExtensionManifests: %v", err)
	}
	as := NewActivationService()
	for _, m := range installed {
		extID := m.ExtensionID()
		if extID == "" || len(m.ActivationEvents) == 0 {
			continue
		}
		as.RegisterExtension(extID, m.ActivationEvents)
	}
	// 验证 activationEvents 已注册。
	if got := as.TriggerOnLanguage("go"); len(got) != 1 || got[0] != "acme.persist" {
		t.Fatalf("TriggerOnLanguage = %v, want [acme.persist]", got)
	}
}

// TestF3_Marketplace_TriggerWorkspaceContainsForwards 验证 marketplace 转发
// TriggerActivationWorkspaceContains。
func TestF3_Marketplace_TriggerWorkspaceContainsForwards(t *testing.T) {
	svc, _ := newTestMarketplaceService(t)
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module test"), 0o644); err != nil {
		t.Fatalf("create go.mod: %v", err)
	}
	as := NewActivationService()
	svc.setActivationService(as)
	as.RegisterExtension("acme.go", []string{"workspaceContains:go.mod"})

	got := svc.TriggerActivationWorkspaceContains(tmp)
	if len(got) != 1 || got[0] != "acme.go" {
		t.Fatalf("TriggerActivationWorkspaceContains = %v, want [acme.go]", got)
	}
}

// TestF3_Marketplace_TriggerOnDebugResolveForwards 验证 marketplace 转发
// TriggerActivationOnDebugResolve。
func TestF3_Marketplace_TriggerOnDebugResolveForwards(t *testing.T) {
	svc, _ := newTestMarketplaceService(t)
	as := NewActivationService()
	svc.setActivationService(as)
	as.RegisterExtension("acme.go-dbg", []string{"onDebugResolve:go"})

	got := svc.TriggerActivationOnDebugResolve("go")
	if len(got) != 1 || got[0] != "acme.go-dbg" {
		t.Fatalf("TriggerActivationOnDebugResolve = %v, want [acme.go-dbg]", got)
	}
}

// 确保 strings 包被使用（避免未使用 import 在某些构建配置下报错）。
var _ = strings.Contains
