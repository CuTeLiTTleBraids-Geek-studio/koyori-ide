package services

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// extension_service.go — Priority 8: VSCode 扩展 activationEvents + contributes 解析。
//
// 本文件解析 VS Code 扩展 package.json 的 activationEvents 与 contributes 段，
// 用于在 IDE 中展示扩展元数据并按贡献点（语言/命令/语法等）激活相应功能。
//
// 与 marketplace_service.go 中已有的 VSCodeExtensionManifest 不同，此处提供
// 更细粒度的 contributes 类型化解析（languages / grammars / snippets / commands /
// configuration / debuggers / jsonValidation），以及隐式 activationEvents 推导。
// 两者刻意分离：marketplace_service 关注安装/校验流程，本文件关注元数据语义。

// ExtensionManifest 是解析后的 VS Code 扩展 package.json 子集。
type ExtensionManifest struct {
	Name             string               `json:"name"`
	Publisher        string               `json:"publisher"`
	Version          string               `json:"version"`
	DisplayName      string               `json:"displayName"`
	Description      string               `json:"description"`
	Engines          map[string]string    `json:"engines"`
	ActivationEvents []string             `json:"activationEvents"`
	Contributes      ExtensionContributes `json:"contributes"`
	// Main 是 Node 扩展入口（require 路径）。
	Main string `json:"main"`
	// Browser 是 Web 扩展入口（浏览器环境加载的脚本路径）。
	Browser string `json:"browser"`
	// Koyori IDE 保留宿主专用元数据（当前包含 permissions）。
	KoyoriIde json.RawMessage `json:"koyoriIde,omitempty"`
}

// ExtensionContributes 描述 package.json 的 contributes 段。所有字段均为
// 可选——扩展按需贡献，未贡献的类型保持 nil。
//
// F-3 (prompt-2.md): 在原有 languages/grammars/snippets/commands/configuration/
// debuggers/jsonValidation 基础上补齐 views/viewsWelcome/menus/keybindings/
// themes/iconThemes，使前端扩展宿主能完整注入命令面板、侧边栏、Monaco 语法
// 与主题等贡献点。
type ExtensionContributes struct {
	Languages      []ExtensionLanguageContribution       `json:"languages,omitempty"`
	Grammars       []ExtensionGrammarContribution        `json:"grammars,omitempty"`
	Snippets       []ExtensionSnippetContribution        `json:"snippets,omitempty"`
	Commands       []ExtensionCommandContribution        `json:"commands,omitempty"`
	Configuration  []ExtensionConfigurationContribution  `json:"configuration,omitempty"`
	Debuggers      []ExtensionDebuggerContribution       `json:"debuggers,omitempty"`
	JSONValidation []ExtensionJSONValidationContribution `json:"jsonValidation,omitempty"`
	// F-3: views 按容器 ID 分组（如 "explorer"、"debug"），值为该容器下的视图列表。
	Views        map[string][]ExtensionViewContribution `json:"views,omitempty"`
	ViewsWelcome []ExtensionViewWelcomeContribution     `json:"viewsWelcome,omitempty"`
	Menus        map[string][]ExtensionMenuContribution `json:"menus,omitempty"`
	Keybindings  []ExtensionKeybindingContribution      `json:"keybindings,omitempty"`
	Themes       []ExtensionThemeContribution           `json:"themes,omitempty"`
	IconThemes   []ExtensionIconThemeContribution       `json:"iconThemes,omitempty"`
}

// ExtensionViewContribution 对应 contributes.views.<container>[] 的一项。
// F-3 (prompt-2.md)。
type ExtensionViewContribution struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	When            string `json:"when,omitempty"`
	Icon            string `json:"icon,omitempty"`
	ContextualTitle string `json:"contextualTitle,omitempty"`
	Visibility      string `json:"visibility,omitempty"`
}

// ExtensionViewWelcomeContribution 对应 contributes.viewsWelcome[]。
// F-3 (prompt-2.md)。
type ExtensionViewWelcomeContribution struct {
	View     string `json:"view"`
	Contents string `json:"contents"`
	When     string `json:"when,omitempty"`
}

// ExtensionMenuContribution 对应 contributes.menus.<menuId>[] 的一项。
// F-3 (prompt-2.md)。
type ExtensionMenuContribution struct {
	Command string `json:"command"`
	Alt     string `json:"alt,omitempty"`
	When    string `json:"when,omitempty"`
	Group   string `json:"group,omitempty"`
}

// ExtensionKeybindingContribution 对应 contributes.keybindings[]。
// F-3 (prompt-2.md)。
type ExtensionKeybindingContribution struct {
	Command string      `json:"command"`
	Key     string      `json:"key"`
	Mac     string      `json:"mac,omitempty"`
	Linux   string      `json:"linux,omitempty"`
	Win     string      `json:"win,omitempty"`
	When    string      `json:"when,omitempty"`
	Args    interface{} `json:"args,omitempty"`
}

// ExtensionThemeContribution 对应 contributes.themes[]。F-3 (prompt-2.md)。
type ExtensionThemeContribution struct {
	Label string `json:"label"`
	UI    string `json:"uiTheme,omitempty"`
	Path  string `json:"path"`
}

// ExtensionIconThemeContribution 对应 contributes.iconThemes[]。
// F-3 (prompt-2.md)。
type ExtensionIconThemeContribution struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Path  string `json:"path"`
}

// ExtensionLanguageContribution 对应 contributes.languages 的一项。
type ExtensionLanguageContribution struct {
	ID            string   `json:"id"`
	Aliases       []string `json:"aliases,omitempty"`
	Extensions    []string `json:"extensions,omitempty"`
	Configuration string   `json:"configuration,omitempty"`
}

// ExtensionGrammarContribution 对应 contributes.grammars 的一项。
type ExtensionGrammarContribution struct {
	Language  string `json:"language,omitempty"`
	ScopeName string `json:"scopeName"`
	Path      string `json:"path"`
}

// ExtensionSnippetContribution 对应 contributes.snippets 的一项。
type ExtensionSnippetContribution struct {
	Language string `json:"language"`
	Path     string `json:"path"`
}

// ExtensionCommandContribution 对应 contributes.commands 的一项。
type ExtensionCommandContribution struct {
	Command  string `json:"command"`
	Title    string `json:"title"`
	Category string `json:"category,omitempty"`
}

// ExtensionConfigurationContribution 对应 contributes.configuration。
// VS Code 允许该字段为单个对象或数组，ParseExtensionManifest 会统一归一化为数组。
type ExtensionConfigurationContribution struct {
	Title      string          `json:"title,omitempty"`
	Properties json.RawMessage `json:"properties,omitempty"`
}

// ExtensionDebuggerContribution 对应 contributes.debuggers 的一项。
type ExtensionDebuggerContribution struct {
	Type                    string          `json:"type"`
	Label                   string          `json:"label,omitempty"`
	Languages               []string        `json:"languages,omitempty"`
	ConfigurationAttributes json.RawMessage `json:"configurationAttributes,omitempty"`
}

// ExtensionJSONValidationContribution 对应 contributes.jsonValidation 的一项。
type ExtensionJSONValidationContribution struct {
	FileMatch string `json:"fileMatch"`
	URL       string `json:"url"`
}

// rawExtensionManifest 是解析用的中间结构。contributes.configuration 可能是
// 单个对象或数组，因此先用 json.RawMessage 捕获后再归一化。
type rawExtensionManifest struct {
	Name             string                  `json:"name"`
	Publisher        string                  `json:"publisher"`
	Version          string                  `json:"version"`
	DisplayName      string                  `json:"displayName"`
	Description      string                  `json:"description"`
	Engines          map[string]string       `json:"engines"`
	ActivationEvents []string                `json:"activationEvents"`
	Main             string                  `json:"main"`
	Browser          string                  `json:"browser"`
	KoyoriIde        json.RawMessage         `json:"koyoriIde"`
	Contributes      rawExtensionContributes `json:"contributes"`
}

type rawExtensionContributes struct {
	Languages      []ExtensionLanguageContribution       `json:"languages,omitempty"`
	Grammars       []ExtensionGrammarContribution        `json:"grammars,omitempty"`
	Snippets       []ExtensionSnippetContribution        `json:"snippets,omitempty"`
	Commands       []ExtensionCommandContribution        `json:"commands,omitempty"`
	Configuration  json.RawMessage                       `json:"configuration,omitempty"`
	Debuggers      []ExtensionDebuggerContribution       `json:"debuggers,omitempty"`
	JSONValidation []ExtensionJSONValidationContribution `json:"jsonValidation,omitempty"`
	// F-3: 新增的 contributes 字段，透传到 ExtensionContributes。
	Views        map[string][]ExtensionViewContribution `json:"views,omitempty"`
	ViewsWelcome []ExtensionViewWelcomeContribution     `json:"viewsWelcome,omitempty"`
	Menus        map[string][]ExtensionMenuContribution `json:"menus,omitempty"`
	Keybindings  []ExtensionKeybindingContribution      `json:"keybindings,omitempty"`
	Themes       []ExtensionThemeContribution           `json:"themes,omitempty"`
	IconThemes   []ExtensionIconThemeContribution       `json:"iconThemes,omitempty"`
}

// ParseExtensionManifest 解析 VS Code 扩展 package.json 内容并提取
// activationEvents、contributes 等关键字段。
//
// 对于 contributes.configuration 既支持单对象也支持数组形式（VS Code 两种
// 写法都合法），结果统一以数组返回。未知字段被忽略，缺失字段为零值。
func ParseExtensionManifest(packageJSON string) (*ExtensionManifest, error) {
	if strings.TrimSpace(packageJSON) == "" {
		return nil, fmt.Errorf("extension manifest: empty package.json content")
	}
	var raw rawExtensionManifest
	if err := json.Unmarshal([]byte(packageJSON), &raw); err != nil {
		return nil, fmt.Errorf("extension manifest: parse package.json: %w", err)
	}

	manifest := &ExtensionManifest{
		Name:             raw.Name,
		Publisher:        raw.Publisher,
		Version:          raw.Version,
		DisplayName:      raw.DisplayName,
		Description:      raw.Description,
		Engines:          raw.Engines,
		ActivationEvents: raw.ActivationEvents,
		Main:             raw.Main,
		Browser:          raw.Browser,
		KoyoriIde:        raw.KoyoriIde,
	}
	manifest.Contributes = ExtensionContributes{
		Languages:      raw.Contributes.Languages,
		Grammars:       raw.Contributes.Grammars,
		Snippets:       raw.Contributes.Snippets,
		Commands:       raw.Contributes.Commands,
		Debuggers:      raw.Contributes.Debuggers,
		JSONValidation: raw.Contributes.JSONValidation,
		// F-3: 透传新增的 contributes 字段。
		Views:        raw.Contributes.Views,
		ViewsWelcome: raw.Contributes.ViewsWelcome,
		Menus:        raw.Contributes.Menus,
		Keybindings:  raw.Contributes.Keybindings,
		Themes:       raw.Contributes.Themes,
		IconThemes:   raw.Contributes.IconThemes,
	}
	// 归一化 configuration：单对象 → 单元素数组；数组 → 原样保留；
	// 空/非法 → 忽略（不报错，保持容错）。
	if len(raw.Contributes.Configuration) > 0 {
		cfgs, err := normalizeConfiguration(raw.Contributes.Configuration)
		if err == nil {
			manifest.Contributes.Configuration = cfgs
		}
	}
	return manifest, nil
}

// normalizeConfiguration 将 contributes.configuration 字段归一化为切片。
// 接受单个对象或数组两种形式。
func normalizeConfiguration(data json.RawMessage) ([]ExtensionConfigurationContribution, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	// 尝试作为数组解析。
	var arr []ExtensionConfigurationContribution
	if err := json.Unmarshal(data, &arr); err == nil {
		return arr, nil
	}
	// 否则尝试作为单个对象解析。
	var single ExtensionConfigurationContribution
	if err := json.Unmarshal(data, &single); err == nil {
		return []ExtensionConfigurationContribution{single}, nil
	}
	return nil, fmt.Errorf("extension manifest: invalid contributes.configuration shape")
}

// DiscoverExtensions 扫描 extensionDir 的子目录，解析每个子目录（或
// extension/ 子目录，兼容 VSIX 解包布局）下的 package.json，返回所有
// 成功解析的扩展清单。
//
// 缺失 extensionDir、非目录、无 package.json 的子目录、解析失败的清单
// 均被静默跳过——本函数不因个别扩展损坏而整体失败。结果按
// publisher.name 排序以保证确定性。
func DiscoverExtensions(extensionDir string) ([]*ExtensionManifest, error) {
	entries, err := os.ReadDir(extensionDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []*ExtensionManifest{}, nil
		}
		return nil, fmt.Errorf("discover extensions: read dir %q: %w", extensionDir, err)
	}
	var manifests []*ExtensionManifest
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		manifest, ok := readManifestFromExtensionDir(filepath.Join(extensionDir, entry.Name()))
		if !ok {
			continue
		}
		manifests = append(manifests, manifest)
	}
	sort.Slice(manifests, func(i, j int) bool {
		ki := manifestKey(manifests[i])
		kj := manifestKey(manifests[j])
		return ki < kj
	})
	return manifests, nil
}

// readManifestFromExtensionDir 在一个扩展目录内查找 package.json。
// 优先 VSIX 解包布局（extension/package.json），其次直接位于目录根部。
// 返回 (manifest, true) 表示成功；(nil, false) 表示跳过。
func readManifestFromExtensionDir(dir string) (*ExtensionManifest, bool) {
	candidates := []string{
		filepath.Join(dir, vsixExtensionPrefix, "package.json"),
		filepath.Join(dir, "package.json"),
	}
	for _, p := range candidates {
		data, err := readFileLimited(p, maxReadableFileBytes)
		if err != nil {
			continue
		}
		manifest, err := ParseExtensionManifest(string(data))
		if err != nil {
			continue
		}
		return manifest, true
	}
	return nil, false
}

// manifestKey 是排序用的稳定键：publisher.name（缺失字段以空串占位）。
func manifestKey(m *ExtensionManifest) string {
	return m.Publisher + "." + m.Name
}

// GetExtensionActivationEvents 返回扩展的解析后 activation events。
//
// 规则（对齐 VS Code 行为）：
//   - 若 manifest.ActivationEvents 非空，原样返回（去重保序）。
//   - 否则根据 contributes 推导隐式事件：
//     · contributes.languages → onLanguage:<id>
//     · contributes.commands  → onCommand:<command>
//     · contributes.debuggers → onDebugResolve:<type>（debugger 类型存在时）
//   - 若上述均为空，返回空切片。
//
// 即使显式 activationEvents 存在，也会追加未覆盖的隐式事件（避免遗漏
// 命令/语言激活点），最终结果去重并保持稳定顺序。
func GetExtensionActivationEvents(manifest *ExtensionManifest) []string {
	if manifest == nil {
		return []string{}
	}
	seen := make(map[string]struct{})
	var out []string
	add := func(evt string) {
		if evt == "" {
			return
		}
		if _, ok := seen[evt]; ok {
			return
		}
		seen[evt] = struct{}{}
		out = append(out, evt)
	}

	// 显式事件优先。
	for _, evt := range manifest.ActivationEvents {
		add(evt)
	}

	// 隐式事件：languages。
	for _, lang := range manifest.Contributes.Languages {
		if lang.ID != "" {
			add("onLanguage:" + lang.ID)
		}
	}
	// 隐式事件：commands。
	for _, cmd := range manifest.Contributes.Commands {
		if cmd.Command != "" {
			add("onCommand:" + cmd.Command)
		}
	}
	// 隐式事件：debuggers。
	for _, dbg := range manifest.Contributes.Debuggers {
		if dbg.Type != "" {
			add("onDebugResolve:" + dbg.Type)
		}
	}
	return out
}
