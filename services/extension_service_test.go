package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// extension_service_test.go — Priority 8 测试。
//
// 覆盖 ParseExtensionManifest / DiscoverExtensions /
// GetExtensionActivationEvents 的核心路径：基础解析、contributes 各类型、
// debuggers、目录发现、隐式 activation events 推导、错误与边界情况。

// --- ParseExtensionManifest: 基础 ---

// TestExtensionService_P8_ParseManifest_Basic 验证最小清单的解析：
// name/version/activationEvents 等顶层字段正确读取。
func TestExtensionService_P8_ParseManifest_Basic(t *testing.T) {
	src := `{
		"name": "hello",
		"publisher": "acme",
		"version": "1.2.3",
		"displayName": "Hello World",
		"description": "A basic extension",
		"engines": { "vscode": "^1.80.0" },
		"activationEvents": ["onStartupFinished", "onCommand:acme.hello"],
		"main": "./dist/main.js",
		"browser": "./dist/browser.js",
		"koyoriIde": { "permissions": ["fs.read"] }
	}`
	m, err := ParseExtensionManifest(src)
	if err != nil {
		t.Fatalf("ParseExtensionManifest failed: %v", err)
	}
	if m.Name != "hello" {
		t.Errorf("Name = %q, want %q", m.Name, "hello")
	}
	if m.Publisher != "acme" {
		t.Errorf("Publisher = %q, want %q", m.Publisher, "acme")
	}
	if m.Version != "1.2.3" {
		t.Errorf("Version = %q, want %q", m.Version, "1.2.3")
	}
	if m.DisplayName != "Hello World" {
		t.Errorf("DisplayName = %q, want %q", m.DisplayName, "Hello World")
	}
	if m.Description != "A basic extension" {
		t.Errorf("Description = %q, want %q", m.Description, "A basic extension")
	}
	if m.Engines["vscode"] != "^1.80.0" {
		t.Errorf("Engines.vscode = %q, want %q", m.Engines["vscode"], "^1.80.0")
	}
	if len(m.ActivationEvents) != 2 || m.ActivationEvents[0] != "onStartupFinished" || m.ActivationEvents[1] != "onCommand:acme.hello" {
		t.Errorf("ActivationEvents = %v, want [onStartupFinished onCommand:acme.hello]", m.ActivationEvents)
	}
	if m.Main != "./dist/main.js" {
		t.Errorf("Main = %q, want %q", m.Main, "./dist/main.js")
	}
	if m.Browser != "./dist/browser.js" {
		t.Errorf("Browser = %q, want %q", m.Browser, "./dist/browser.js")
	}
	if !strings.Contains(string(m.KoyoriIde), `"permissions"`) || !strings.Contains(string(m.KoyoriIde), `"fs.read"`) {
		t.Errorf("KoyoriIde = %s, want permissions metadata", m.KoyoriIde)
	}
}

// --- ParseExtensionManifest: contributes ---

// TestExtensionService_P8_ParseManifest_Contributes 验证 languages/grammars/
// commands/snippets/jsonValidation 等贡献点的类型化解析。
func TestExtensionService_P8_ParseManifest_Contributes(t *testing.T) {
	src := `{
		"name": "langpack",
		"publisher": "contoso",
		"version": "0.0.1",
		"contributes": {
			"languages": [
				{
					"id": "golang",
					"aliases": ["Go", "golang"],
					"extensions": [".go"],
					"configuration": "./language-configuration.json"
				}
			],
			"grammars": [
				{
					"language": "golang",
					"scopeName": "source.go",
					"path": "./syntaxes/go.tmLanguage.json"
				}
			],
			"snippets": [
				{ "language": "golang", "path": "./snippets/go.json" }
			],
			"commands": [
				{ "command": "contoso.go.run", "title": "Run Go", "category": "Go" }
			],
			"configuration": {
				"title": "Go",
				"properties": { "go.gopath": { "type": "string" } }
			},
			"jsonValidation": [
				{ "fileMatch": "go.mod", "url": "./go-mod.schema.json" }
			]
		}
	}`
	m, err := ParseExtensionManifest(src)
	if err != nil {
		t.Fatalf("ParseExtensionManifest failed: %v", err)
	}
	// languages
	if len(m.Contributes.Languages) != 1 {
		t.Fatalf("Languages len = %d, want 1", len(m.Contributes.Languages))
	}
	lang := m.Contributes.Languages[0]
	if lang.ID != "golang" {
		t.Errorf("Languages[0].ID = %q, want %q", lang.ID, "golang")
	}
	if len(lang.Aliases) != 2 || lang.Aliases[0] != "Go" || lang.Aliases[1] != "golang" {
		t.Errorf("Languages[0].Aliases = %v, want [Go golang]", lang.Aliases)
	}
	if len(lang.Extensions) != 1 || lang.Extensions[0] != ".go" {
		t.Errorf("Languages[0].Extensions = %v, want [.go]", lang.Extensions)
	}
	if lang.Configuration != "./language-configuration.json" {
		t.Errorf("Languages[0].Configuration = %q, want ./language-configuration.json", lang.Configuration)
	}
	// grammars
	if len(m.Contributes.Grammars) != 1 {
		t.Fatalf("Grammars len = %d, want 1", len(m.Contributes.Grammars))
	}
	g := m.Contributes.Grammars[0]
	if g.Language != "golang" || g.ScopeName != "source.go" || g.Path != "./syntaxes/go.tmLanguage.json" {
		t.Errorf("Grammars[0] = %+v, want {golang source.go ./syntaxes/go.tmLanguage.json}", g)
	}
	// snippets
	if len(m.Contributes.Snippets) != 1 {
		t.Fatalf("Snippets len = %d, want 1", len(m.Contributes.Snippets))
	}
	snip := m.Contributes.Snippets[0]
	if snip.Language != "golang" || snip.Path != "./snippets/go.json" {
		t.Errorf("Snippets[0] = %+v, want {golang ./snippets/go.json}", snip)
	}
	// commands
	if len(m.Contributes.Commands) != 1 {
		t.Fatalf("Commands len = %d, want 1", len(m.Contributes.Commands))
	}
	cmd := m.Contributes.Commands[0]
	if cmd.Command != "contoso.go.run" || cmd.Title != "Run Go" || cmd.Category != "Go" {
		t.Errorf("Commands[0] = %+v, want {contoso.go.run Run Go Go}", cmd)
	}
	// configuration（单对象归一化为单元素数组）
	if len(m.Contributes.Configuration) != 1 {
		t.Fatalf("Configuration len = %d, want 1 (normalized from single object)", len(m.Contributes.Configuration))
	}
	if m.Contributes.Configuration[0].Title != "Go" {
		t.Errorf("Configuration[0].Title = %q, want %q", m.Contributes.Configuration[0].Title, "Go")
	}
	if len(m.Contributes.Configuration[0].Properties) == 0 {
		t.Errorf("Configuration[0].Properties should be non-empty raw JSON")
	}
	// jsonValidation
	if len(m.Contributes.JSONValidation) != 1 {
		t.Fatalf("JSONValidation len = %d, want 1", len(m.Contributes.JSONValidation))
	}
	jv := m.Contributes.JSONValidation[0]
	if jv.FileMatch != "go.mod" || jv.URL != "./go-mod.schema.json" {
		t.Errorf("JSONValidation[0] = %+v, want {go.mod ./go-mod.schema.json}", jv)
	}
}

// --- ParseExtensionManifest: debuggers ---

// TestExtensionService_P8_ParseManifest_Debuggers 验证 debuggers 贡献点解析，
// 包括 type/label/languages 与 configurationAttributes（透传为 RawMessage）。
func TestExtensionService_P8_ParseManifest_Debuggers(t *testing.T) {
	src := `{
		"name": "debugger",
		"publisher": "contoso",
		"version": "1.0.0",
		"contributes": {
			"debuggers": [
				{
					"type": "go",
					"label": "Go Debug",
					"languages": ["go"],
					"configurationAttributes": {
						"launch": { "required": ["program"], "properties": { "program": { "type": "string" } } }
					}
				}
			]
		}
	}`
	m, err := ParseExtensionManifest(src)
	if err != nil {
		t.Fatalf("ParseExtensionManifest failed: %v", err)
	}
	if len(m.Contributes.Debuggers) != 1 {
		t.Fatalf("Debuggers len = %d, want 1", len(m.Contributes.Debuggers))
	}
	dbg := m.Contributes.Debuggers[0]
	if dbg.Type != "go" {
		t.Errorf("Debuggers[0].Type = %q, want %q", dbg.Type, "go")
	}
	if dbg.Label != "Go Debug" {
		t.Errorf("Debuggers[0].Label = %q, want %q", dbg.Label, "Go Debug")
	}
	if len(dbg.Languages) != 1 || dbg.Languages[0] != "go" {
		t.Errorf("Debuggers[0].Languages = %v, want [go]", dbg.Languages)
	}
	if len(dbg.ConfigurationAttributes) == 0 {
		t.Errorf("Debuggers[0].ConfigurationAttributes should be non-empty raw JSON")
	}
	if !strings.Contains(string(dbg.ConfigurationAttributes), "program") {
		t.Errorf("Debuggers[0].ConfigurationAttributes should mention 'program': %s", string(dbg.ConfigurationAttributes))
	}
}

// --- DiscoverExtensions ---

// TestExtensionService_P8_DiscoverExtensions 在临时目录中构造多个扩展子目录
// （混合 VSIX 解包布局与根部 package.json 布局，并包含一个无效扩展与一个无清单目录），
// 验证发现结果按 publisher.name 排序、无效项被跳过。
func TestExtensionService_P8_DiscoverExtensions(t *testing.T) {
	root := t.TempDir()

	// 扩展 A：VSIX 解包布局（extension/package.json）。
	dirA := filepath.Join(root, "acme.alpha")
	if err := os.MkdirAll(filepath.Join(dirA, "extension"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirA, "extension", "package.json"), []byte(`{
		"name": "alpha", "publisher": "acme", "version": "1.0.0",
		"activationEvents": ["onStartupFinished"]
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// 扩展 B：根部 package.json 布局。
	dirB := filepath.Join(root, "contoso.beta")
	if err := os.MkdirAll(dirB, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirB, "package.json"), []byte(`{
		"name": "beta", "publisher": "contoso", "version": "2.0.0",
		"contributes": { "commands": [{"command":"contoso.beta.run","title":"Run"}] }
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// 无效扩展 C：损坏 JSON（应被跳过）。
	dirC := filepath.Join(root, "broken.delta")
	if err := os.MkdirAll(dirC, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirC, "package.json"), []byte(`{ not valid json`), 0o644); err != nil {
		t.Fatal(err)
	}

	// 无 package.json 目录 D（应被跳过）。
	if err := os.MkdirAll(filepath.Join(root, "empty.epsilon"), 0o755); err != nil {
		t.Fatal(err)
	}

	// 一个普通文件（应被跳过——非目录）。
	if err := os.WriteFile(filepath.Join(root, "stray.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	manifests, err := DiscoverExtensions(root)
	if err != nil {
		t.Fatalf("DiscoverExtensions failed: %v", err)
	}
	if len(manifests) != 2 {
		t.Fatalf("len(manifests) = %d, want 2 (alpha + beta)", len(manifests))
	}
	// 排序后：acme.alpha < contoso.beta。
	if manifests[0].Publisher != "acme" || manifests[0].Name != "alpha" {
		t.Errorf("manifests[0] = %s.%s, want acme.alpha", manifests[0].Publisher, manifests[0].Name)
	}
	if manifests[1].Publisher != "contoso" || manifests[1].Name != "beta" {
		t.Errorf("manifests[1] = %s.%s, want contoso.beta", manifests[1].Publisher, manifests[1].Name)
	}

	// 不存在的目录应返回空切片而非错误。
	empty, err := DiscoverExtensions(filepath.Join(root, "does-not-exist"))
	if err != nil {
		t.Fatalf("DiscoverExtensions on missing dir should not error: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("len(empty) = %d, want 0", len(empty))
	}
}

// --- Implicit activation events ---

// TestExtensionService_P8_ImplicitActivationEvents 验证当 manifest 无
// activationEvents 时，根据 contributes.languages / commands / debuggers
// 推导出隐式 onLanguage:/onCommand:/onDebugResolve: 事件。
// 同时验证显式事件与隐式事件合并去重。
func TestExtensionService_P8_ImplicitActivationEvents(t *testing.T) {
	src := `{
		"name": "implicit",
		"publisher": "acme",
		"version": "1.0.0",
		"activationEvents": ["onStartupFinished"],
		"contributes": {
			"languages": [{ "id": "golang", "extensions": [".go"] }],
			"commands": [{ "command": "acme.go.run", "title": "Run" }],
			"debuggers": [{ "type": "go", "label": "Go" }]
		}
	}`
	m, err := ParseExtensionManifest(src)
	if err != nil {
		t.Fatalf("ParseExtensionManifest failed: %v", err)
	}
	events := GetExtensionActivationEvents(m)
	// 期望包含：显式 onStartupFinished + 隐式 onLanguage:golang +
	// onCommand:acme.go.run + onDebugResolve:go。
	wantSet := map[string]bool{
		"onStartupFinished":     true,
		"onLanguage:golang":     true,
		"onCommand:acme.go.run": true,
		"onDebugResolve:go":     true,
	}
	if len(events) != len(wantSet) {
		t.Fatalf("len(events) = %d, want %d; events=%v", len(events), len(wantSet), events)
	}
	seen := make(map[string]bool)
	for _, e := range events {
		if seen[e] {
			t.Errorf("event %q duplicated in %v", e, events)
		}
		seen[e] = true
		if !wantSet[e] {
			t.Errorf("unexpected event %q in %v", e, events)
		}
	}

	// 显式事件应排在最前（保留 activationEvents 顺序）。
	if len(events) == 0 || events[0] != "onStartupFinished" {
		t.Errorf("first event = %q, want onStartupFinished (explicit first); events=%v", firstOrEmpty(events), events)
	}

	// --- 纯隐式场景：无 activationEvents，仅有 languages/commands ---
	m2, err := ParseExtensionManifest(`{
		"name": "pure", "publisher": "p", "version": "1.0.0",
		"contributes": { "languages": [{"id":"ts"}], "commands":[{"command":"p.x","title":"X"}] }
	}`)
	if err != nil {
		t.Fatalf("ParseExtensionManifest failed: %v", err)
	}
	ev2 := GetExtensionActivationEvents(m2)
	if len(ev2) != 2 {
		t.Fatalf("len(ev2) = %d, want 2; ev2=%v", len(ev2), ev2)
	}

	// --- 无 contributes 也无 activationEvents：返回空切片 ---
	m3, err := ParseExtensionManifest(`{ "name": "bare", "publisher": "p", "version": "1.0.0" }`)
	if err != nil {
		t.Fatalf("ParseExtensionManifest failed: %v", err)
	}
	ev3 := GetExtensionActivationEvents(m3)
	if len(ev3) != 0 {
		t.Errorf("len(ev3) = %d, want 0; ev3=%v", len(ev3), ev3)
	}

	// --- nil manifest 安全性：返回空切片 ---
	if got := GetExtensionActivationEvents(nil); len(got) != 0 {
		t.Errorf("GetExtensionActivationEvents(nil) = %v, want empty", got)
	}
}

// --- Invalid JSON ---

// TestExtensionService_P8_InvalidJSON 验证非法 JSON 返回错误。
func TestExtensionService_P8_InvalidJSON(t *testing.T) {
	cases := []string{
		`{ not valid json`,
		`{"name": "x",`, // 截断
		`[]`,            // 顶层不是对象
	}
	for _, c := range cases {
		if _, err := ParseExtensionManifest(c); err == nil {
			t.Errorf("ParseManifest(%q) expected error, got nil", c)
		}
	}
	// 空内容也视为错误。
	if _, err := ParseExtensionManifest(""); err == nil {
		t.Errorf("ParseManifest(\"\") expected error, got nil")
	}
	if _, err := ParseExtensionManifest("   \n  "); err == nil {
		t.Errorf("ParseManifest(whitespace) expected error, got nil")
	}
}

// --- Empty / minimal manifest ---

// TestExtensionService_P8_EmptyManifest 验证仅含 name/version 的最小清单可解析，
// 其余字段为零值，activation events 为空。
func TestExtensionService_P8_EmptyManifest(t *testing.T) {
	m, err := ParseExtensionManifest(`{ "name": "mini", "version": "0.1.0" }`)
	if err != nil {
		t.Fatalf("ParseExtensionManifest failed: %v", err)
	}
	if m.Name != "mini" {
		t.Errorf("Name = %q, want %q", m.Name, "mini")
	}
	if m.Version != "0.1.0" {
		t.Errorf("Version = %q, want %q", m.Version, "0.1.0")
	}
	if m.Publisher != "" {
		t.Errorf("Publisher = %q, want empty", m.Publisher)
	}
	if m.DisplayName != "" || m.Description != "" {
		t.Errorf("DisplayName/Description should be empty; got %q/%q", m.DisplayName, m.Description)
	}
	if m.Engines != nil {
		t.Errorf("Engines should be nil, got %v", m.Engines)
	}
	if len(m.ActivationEvents) != 0 {
		t.Errorf("ActivationEvents = %v, want empty", m.ActivationEvents)
	}
	if len(m.Contributes.Languages) != 0 || len(m.Contributes.Commands) != 0 {
		t.Errorf("Contributes sub-fields should be empty")
	}
	events := GetExtensionActivationEvents(m)
	if len(events) != 0 {
		t.Errorf("events = %v, want empty", events)
	}
}

// firstOrEmpty 返回切片首元素或空串（仅用于错误信息可读性）。
func firstOrEmpty(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}
