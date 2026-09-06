package services

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Stash, tag, submodule, cherry-pick, revert, and bisect operations.
// gitignoreGo is the .gitignore template for Go projects.
const gitignoreGo = `# Go
*.exe
*.exe~
*.dll
*.so
*.dylib
*.test
*.out
go.work
go.work.sum
vendor/
.air.toml
`

// gitignoreTypeScript is the .gitignore template for TypeScript projects.
const gitignoreTypeScript = `# TypeScript / Node
node_modules/
dist/
build/
*.js.map
*.tsbuildinfo
.env
.env.*
!.env.example
`

// gitignoreJavaScript is the .gitignore template for JavaScript projects.
const gitignoreJavaScript = `# JavaScript / Node
node_modules/
dist/
build/
.env
.env.*
!.env.example
`

// gitignoreGeneral is the OS/IDE .gitignore template applicable to any project.
const gitignoreGeneral = `# OS files
.DS_Store
Thumbs.db
desktop.ini

# IDE files
.idea/
.vscode/*
!.vscode/settings.json
!.vscode/tasks.json
!.vscode/launch.json
!.vscode/extensions.json
*.swp
*.swo
*~
`

// ---------------------------------------------------------------------------
// 优先级 3: Git Stash / Tag / Amend
// ---------------------------------------------------------------------------

// StashEntry 表示一条 git stash 记录，对应 `git stash list` 的一行输出。
type StashEntry struct {
	Ref        string `json:"ref"`        // stash 引用名，如 stash@{0}
	Message    string `json:"message"`    // stash 提交信息
	Date       string `json:"date"`       // RFC3339 作者时间
	Author     string `json:"author"`     // stash 作者名
	CommitHash string `json:"commitHash"` // 兼容现有调用方的完整 SHA
}

// TagEntry 表示一个 git tag，对应 `git tag -l` 的一行输出。
type TagEntry struct {
	Name       string `json:"name"`       // 标签名（短名，不含 refs/tags/ 前缀）
	CommitHash string `json:"commitHash"` // 标签指向的提交 SHA
	Message    string `json:"message"`    // 标签信息（annotated tag 的 message）
}

// stashRefRe 匹配合法的 stash 引用名（stash@{N}），拒绝 shell 元字符和
// 路径穿越，防止通过 stashRef 参数注入。
var stashRefRe = regexp.MustCompile(`^stash@\{\d+\}$`)

// tagNameRe 匹配合法的 git 标签名：以字母或数字开头，后续可含字母、数字、
// 下划线、连字符、点。拒绝空格、shell 元字符和路径穿越。
var tagNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// remoteNameRe 匹配合法的 git 远程仓库名，与 branchNameRe 保持一致。
var remoteNameRe = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

// validateStashRef 校验 stashRef 是否为合法的 stash 引用（stash@{N}）。
func validateStashRef(stashRef string) error {
	if !stashRefRe.MatchString(stashRef) {
		return fmt.Errorf("invalid stash reference %q: expected format stash@{N}", stashRef)
	}
	return nil
}

// validateTagName 校验 tagName 是否为合法的 git 标签名。
func validateTagName(name string) error {
	if !tagNameRe.MatchString(name) {
		return fmt.Errorf("invalid tag name %q: must start with alphanumeric and contain only alphanumeric, -, _, .", name)
	}
	return nil
}

// validateRemoteName 校验 remote 是否为合法的 git 远程仓库名。
func validateRemoteName(remote string) error {
	if !remoteNameRe.MatchString(remote) {
		return fmt.Errorf("invalid remote name %q: must contain only alphanumeric, -, _, ., /", remote)
	}
	return nil
}

// commitHashRe 匹配 git commit hash：7-40 位十六进制字符。
// 7 位是 git 短 hash 的最小长度，40 位是完整 SHA-1。
var commitHashRe = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// validateCommitHash 校验 commitHash 是否为合法的 git commit hash。
// F-4 (prompt-2.md): CherryPick / RevertCommit / BisectStart 使用。
func validateCommitHash(hash string) error {
	if !commitHashRe.MatchString(hash) {
		return fmt.Errorf("invalid commit hash %q: must be 7-40 hexadecimal characters", hash)
	}
	return nil
}

// submodulePathRe 匹配合法的子模块相对路径：允许字母数字、-、_、.、/，
// 但拒绝 ".."（路径穿越）和绝对路径。
var submodulePathRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

// validateSubmodulePath 校验子模块路径：非空、无路径穿越、在 workspace 内。
func (g *GitService) validateSubmodulePath(root, subPath string) error {
	if subPath == "" {
		return errors.New("submodule path cannot be empty")
	}
	if filepath.IsAbs(subPath) {
		return fmt.Errorf("submodule path must be relative: %q", subPath)
	}
	cleaned := filepath.Clean(subPath)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return fmt.Errorf("submodule path traversal blocked: %q", subPath)
	}
	if !submodulePathRe.MatchString(filepath.ToSlash(cleaned)) {
		return fmt.Errorf("invalid submodule path %q: must contain only alphanumeric, -, _, ., /", subPath)
	}
	// 深度防御：校验最终路径在 workspace 内。
	return g.validatePath(filepath.Join(root, cleaned))
}

// validateSubmoduleURL 校验子模块 URL：拒绝 file:// 协议穿越。
// 允许 https://、git://、ssh://、git@host: 等常见格式。
func validateSubmoduleURL(url string) error {
	if url == "" {
		return errors.New("submodule URL cannot be empty")
	}
	lower := strings.ToLower(url)
	if strings.HasPrefix(lower, "file://") {
		// file:// 协议可能被用于访问 workspace 外的本地仓库。
		return fmt.Errorf("file:// protocol is not allowed for submodule URL: %q", url)
	}
	if strings.ContainsRune(url, 0) {
		return errors.New("submodule URL contains null byte")
	}
	return nil
}

// validateMessage 校验提交/stash/标签信息：非空且不含 null 字节。
// 由于 runGit 使用 exec.Command（不经过 shell），shell 元字符本身不会
// 造成注入，但空信息或 null 字节会导致 git 行为异常。
func validateMessage(msg string) error {
	if strings.TrimSpace(msg) == "" {
		return errors.New("message cannot be empty")
	}
	if strings.ContainsRune(msg, 0) {
		return errors.New("message contains null byte")
	}
	return nil
}

// StashList 返回指定仓库中的所有 stash 记录。使用 unit separator 分隔
// 字段，避免普通提交信息中的竖线破坏解析。
func (g *GitService) StashList(repoPath string) ([]StashEntry, error) {
	if repoPath == "" {
		return nil, errors.New("repository path cannot be empty")
	}
	if err := g.validatePath(repoPath); err != nil {
		return nil, err
	}
	release, err := g.acquireRepoMutation(repoPath)
	if err != nil {
		return nil, err
	}
	defer release()
	const fieldSep = "\x1f"
	out, err := g.runGit(repoPath, "stash", "list", "--format=%gd%x1f%H%x1f%an%x1f%aI%x1f%s")
	if err != nil {
		return nil, err
	}
	entries := make([]StashEntry, 0)
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, fieldSep, 5)
		if len(parts) != 5 {
			return nil, errors.New("parse git stash list: malformed record")
		}
		entries = append(entries, StashEntry{
			Ref:        parts[0],
			CommitHash: parts[1],
			Author:     parts[2],
			Date:       parts[3],
			Message:    parts[4],
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("parse git stash list: %w", err)
	}
	return entries, nil
}

// StashCreate 保存指定仓库当前工作区的修改。
func (g *GitService) StashCreate(repoPath string, message string) error {
	if repoPath == "" {
		return errors.New("repository path cannot be empty")
	}
	if err := g.validatePath(repoPath); err != nil {
		return err
	}
	if err := validateMessage(message); err != nil {
		return err
	}
	release, err := g.acquireRepoMutation(repoPath)
	if err != nil {
		return err
	}
	defer release()
	_, err = g.runGit(repoPath, "stash", "push", "-m", message)
	return err
}

// StashPush 将当前工作区的修改保存到一个新的 stash 中。
func (g *GitService) StashPush(message string) error {
	root := g.workspaceRootPath()
	if root == "" {
		return errors.New("no workspace root set")
	}
	return g.StashCreate(root, message)
}

// StashPop 应用并移除指定的 stash。stashRef 必须形如 stash@{N}。
func (g *GitService) StashPop(stashRef string) error {
	root := g.workspaceRootPath()
	if root == "" {
		return errors.New("no workspace root set")
	}
	if err := validateStashRef(stashRef); err != nil {
		return err
	}
	release, err := g.acquireRepoMutation(root)
	if err != nil {
		return err
	}
	defer release()
	_, err = g.runGit(root, "stash", "pop", stashRef)
	return err
}

// StashApply 应用指定的 stash 但不移除它。stashRef 必须形如 stash@{N}。
func (g *GitService) StashApply(repoPath string, stashRef string) error {
	if repoPath == "" {
		return errors.New("repository path cannot be empty")
	}
	if err := g.validatePath(repoPath); err != nil {
		return err
	}
	if err := validateStashRef(stashRef); err != nil {
		return err
	}
	release, err := g.acquireRepoMutation(repoPath)
	if err != nil {
		return err
	}
	defer release()
	_, err = g.runGit(repoPath, "stash", "apply", stashRef)
	return err
}

// StashDrop 移除指定的 stash。stashRef 必须形如 stash@{N}。
func (g *GitService) StashDrop(repoPath string, stashRef string) error {
	if repoPath == "" {
		return errors.New("repository path cannot be empty")
	}
	if err := g.validatePath(repoPath); err != nil {
		return err
	}
	if err := validateStashRef(stashRef); err != nil {
		return err
	}
	release, err := g.acquireRepoMutation(repoPath)
	if err != nil {
		return err
	}
	defer release()
	_, err = g.runGit(repoPath, "stash", "drop", stashRef)
	return err
}

// ListTags 返回工作区根仓库中的所有标签。
// 使用 `git tag -l --format=%(refname:short)|%(objectname)|%(subject)`。
func (g *GitService) ListTags() ([]TagEntry, error) {
	root := g.workspaceRootPath()
	if root == "" {
		return nil, errors.New("no workspace root set")
	}
	if err := g.validatePath(root); err != nil {
		return nil, err
	}
	release, err := g.acquireRepoMutation(root)
	if err != nil {
		return nil, err
	}
	defer release()
	out, err := g.runGit(root, "tag", "-l", "--format=%(refname:short)|%(objectname)|%(subject)")
	if err != nil {
		return nil, err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return []TagEntry{}, nil
	}
	lines := strings.Split(out, "\n")
	entries := make([]TagEntry, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 3)
		if len(parts) < 3 {
			continue
		}
		entries = append(entries, TagEntry{
			Name:       parts[0],
			CommitHash: parts[1],
			Message:    parts[2],
		})
	}
	return entries, nil
}

// CreateTag 在当前 HEAD 创建一个带注释的标签。
func (g *GitService) CreateTag(name, message string) error {
	root := g.workspaceRootPath()
	if root == "" {
		return errors.New("no workspace root set")
	}
	if err := validateTagName(name); err != nil {
		return err
	}
	if err := validateMessage(message); err != nil {
		return err
	}
	release, err := g.acquireRepoMutation(root)
	if err != nil {
		return err
	}
	defer release()
	_, err = g.runGit(root, "tag", "-a", name, "-m", message)
	return err
}

// DeleteTag 删除指定的标签。
func (g *GitService) DeleteTag(name string) error {
	root := g.workspaceRootPath()
	if root == "" {
		return errors.New("no workspace root set")
	}
	if err := validateTagName(name); err != nil {
		return err
	}
	release, err := g.acquireRepoMutation(root)
	if err != nil {
		return err
	}
	defer release()
	_, err = g.runGit(root, "tag", "-d", name)
	return err
}

// PushTags 将所有本地标签推送到指定的远程仓库。
func (g *GitService) PushTags(remote string) error {
	root := g.workspaceRootPath()
	if root == "" {
		return errors.New("no workspace root set")
	}
	if err := validateRemoteName(remote); err != nil {
		return err
	}
	release, err := g.acquireRepoMutation(root)
	if err != nil {
		return err
	}
	defer release()
	_, err = g.runGit(root, "push", remote, "--tags")
	return err
}

// AmendCommit 使用给定的信息修订最近一次提交（git commit --amend -m）。
func (g *GitService) AmendCommit(message string) error {
	root := g.workspaceRootPath()
	if root == "" {
		return errors.New("no workspace root set")
	}
	if err := validateMessage(message); err != nil {
		return err
	}
	release, err := g.acquireRepoMutation(root)
	if err != nil {
		return err
	}
	defer release()
	_, err = g.runGit(root, "commit", "--amend", "-m", message)
	return err
}

// ---------------------------------------------------------------------------
// F-4 (prompt-2.md): Submodule + Cherry-pick + Revert + Bisect
// ---------------------------------------------------------------------------

// SubmoduleInfo 描述一个子模块的状态。镜像 `git submodule status` 输出。
type SubmoduleInfo struct {
	// SHA 是子模块当前检出的 commit hash（短格式）。
	SHA string `json:"sha"`
	// Path 是子模块在工作区中的相对路径。
	Path string `json:"path"`
	// Name 是 .gitmodules 中定义的子模块名称（通常等于 Path）。
	Name string `json:"name"`
	// Branch 是子模块跟踪的分支（如未设置则为空）。
	Branch string `json:"branch,omitempty"`
	// URL 是子模块的远程仓库 URL。
	URL string `json:"url,omitempty"`
	// Initialized 表示子模块是否已初始化（有 .git 目录）。
	Initialized bool `json:"initialized"`
	// Modified 表示子模块是否有未提交的变更。
	Modified bool `json:"modified,omitempty"`
}

// SubmoduleAdd 添加一个子模块到当前工作区。
// F-4 (prompt-2.md): `git submodule add <url> <path>`
// 安全校验：url 非 file:// 协议、path 在 workspace 内。
func (g *GitService) SubmoduleAdd(url, path string) error {
	root := g.workspaceRootPath()
	if root == "" {
		return errors.New("no workspace root set")
	}
	if err := validateSubmoduleURL(url); err != nil {
		return err
	}
	if err := g.validateSubmodulePath(root, path); err != nil {
		return err
	}
	release, err := g.acquireRepoMutation(root)
	if err != nil {
		return err
	}
	defer release()
	_, err = g.runGit(root, "submodule", "add", url, path)
	return err
}

// SubmoduleList 列出当前工作区中的所有子模块及其状态。
// F-4 (prompt-2.md): `git submodule status`
func (g *GitService) SubmoduleList() ([]SubmoduleInfo, error) {
	root := g.workspaceRootPath()
	if root == "" {
		return nil, errors.New("no workspace root set")
	}
	release, err := g.acquireRepoMutation(root)
	if err != nil {
		return nil, err
	}
	defer release()
	output, err := g.runGit(root, "submodule", "status")
	if err != nil {
		return nil, err
	}
	// 解析 .gitmodules 获取 name/branch/url 信息。
	modules, err := parseGitmodules(root)
	if err != nil {
		modules = map[string]SubmoduleInfo{}
	}
	var result []SubmoduleInfo
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		info := parseSubmoduleStatusLine(line)
		// 合并 .gitmodules 中的信息。
		if m, ok := modules[info.Path]; ok {
			info.Name = m.Name
			info.Branch = m.Branch
			info.URL = m.URL
		}
		result = append(result, info)
	}
	return result, nil
}

// SubmoduleUpdate 更新子模块。当 init 为 true 时，先初始化子模块再更新。
// F-4 (prompt-2.md): `git submodule update [--init]`
func (g *GitService) SubmoduleUpdate(init bool) error {
	root := g.workspaceRootPath()
	if root == "" {
		return errors.New("no workspace root set")
	}
	args := []string{"submodule", "update"}
	if init {
		args = append(args, "--init")
	}
	release, err := g.acquireRepoMutation(root)
	if err != nil {
		return err
	}
	defer release()
	_, err = g.runGit(root, args...)
	return err
}

// SubmoduleDeinit 取消初始化指定路径的子模块。
// F-4 (prompt-2.md): `git submodule deinit -f <path>`
func (g *GitService) SubmoduleDeinit(path string) error {
	root := g.workspaceRootPath()
	if root == "" {
		return errors.New("no workspace root set")
	}
	if err := g.validateSubmodulePath(root, path); err != nil {
		return err
	}
	release, err := g.acquireRepoMutation(root)
	if err != nil {
		return err
	}
	defer release()
	_, err = g.runGit(root, "submodule", "deinit", "-f", path)
	return err
}

// CherryPick 将指定 commit 的变更应用到当前分支。
// F-4 (prompt-2.md): `git cherry-pick <hash>`
// 安全校验：hash 格式为 7-40 位十六进制字符。
func (g *GitService) CherryPick(commitHash string) error {
	root := g.workspaceRootPath()
	if root == "" {
		return errors.New("no workspace root set")
	}
	if err := validateCommitHash(commitHash); err != nil {
		return err
	}
	release, err := g.acquireRepoMutation(root)
	if err != nil {
		return err
	}
	defer release()
	_, err = g.runGit(root, "cherry-pick", commitHash)
	return err
}

// RevertCommit 撤销指定 commit 的变更，创建一个新的反向 commit。
// F-4 (prompt-2.md): `git revert --no-edit <hash>`
// 安全校验：hash 格式为 7-40 位十六进制字符。
func (g *GitService) RevertCommit(commitHash string) error {
	root := g.workspaceRootPath()
	if root == "" {
		return errors.New("no workspace root set")
	}
	if err := validateCommitHash(commitHash); err != nil {
		return err
	}
	release, err := g.acquireRepoMutation(root)
	if err != nil {
		return err
	}
	defer release()
	_, err = g.runGit(root, "revert", "--no-edit", commitHash)
	return err
}

// BisectStart 启动二分查找，指定好的和坏的 commit。
// F-4 (prompt-2.md): `git bisect start <bad> <good>`
// 安全校验：good 和 bad hash 格式为 7-40 位十六进制字符。
func (g *GitService) BisectStart(good, bad string) error {
	root := g.workspaceRootPath()
	if root == "" {
		return errors.New("no workspace root set")
	}
	if err := validateCommitHash(good); err != nil {
		return fmt.Errorf("invalid good commit: %w", err)
	}
	if err := validateCommitHash(bad); err != nil {
		return fmt.Errorf("invalid bad commit: %w", err)
	}
	release, err := g.acquireRepoMutation(root)
	if err != nil {
		return err
	}
	defer release()
	_, err = g.runGit(root, "bisect", "start", bad, good)
	return err
}

// BisectGood 标记当前 commit 为好的（不含 bug）。
// F-4 (prompt-2.md): `git bisect good`
func (g *GitService) BisectGood() error {
	root := g.workspaceRootPath()
	if root == "" {
		return errors.New("no workspace root set")
	}
	release, err := g.acquireRepoMutation(root)
	if err != nil {
		return err
	}
	defer release()
	_, err = g.runGit(root, "bisect", "good")
	return err
}

// BisectBad 标记当前 commit 为坏的（含 bug）。
// F-4 (prompt-2.md): `git bisect bad`
func (g *GitService) BisectBad() error {
	root := g.workspaceRootPath()
	if root == "" {
		return errors.New("no workspace root set")
	}
	release, err := g.acquireRepoMutation(root)
	if err != nil {
		return err
	}
	defer release()
	_, err = g.runGit(root, "bisect", "bad")
	return err
}

// BisectReset 结束二分查找，回到原始分支。
// F-4 (prompt-2.md): `git bisect reset`
func (g *GitService) BisectReset() error {
	root := g.workspaceRootPath()
	if root == "" {
		return errors.New("no workspace root set")
	}
	release, err := g.acquireRepoMutation(root)
	if err != nil {
		return err
	}
	defer release()
	_, err = g.runGit(root, "bisect", "reset")
	return err
}

// ---------------------------------------------------------------------------
// F-4 辅助函数
// ---------------------------------------------------------------------------

// parseSubmoduleStatusLine 解析 `git submodule status` 的单行输出。
// 行格式：<status_char><sha> <path> (<description>)
// status_char: ' '（未修改）、'+'（修改）、'-'（未初始化）、'U'（冲突）
func parseSubmoduleStatusLine(line string) SubmoduleInfo {
	info := SubmoduleInfo{}
	if len(line) < 1 {
		return info
	}
	// 首字符是状态标志。
	statusChar := line[0]
	switch statusChar {
	case '-':
		info.Initialized = false
	case '+':
		info.Initialized = true
		info.Modified = true
	case 'U':
		info.Initialized = true
		info.Modified = true
	default:
		info.Initialized = true
	}
	// 去掉首字符后按空格分割。
	rest := strings.TrimSpace(line[1:])
	parts := strings.SplitN(rest, " ", 2)
	if len(parts) >= 1 {
		info.SHA = parts[0]
	}
	if len(parts) >= 2 {
		// path 后面可能有 (description)，去掉它。
		pathDesc := strings.TrimSpace(parts[1])
		if idx := strings.Index(pathDesc, "("); idx >= 0 {
			pathDesc = strings.TrimSpace(pathDesc[:idx])
		}
		info.Path = pathDesc
		info.Name = pathDesc
	}
	return info
}

// parseGitmodules 解析 .gitmodules 文件，返回按 path 索引的子模块信息。
func parseGitmodules(root string) (map[string]SubmoduleInfo, error) {
	content, err := os.ReadFile(filepath.Join(root, ".gitmodules"))
	if err != nil {
		return nil, err
	}
	result := make(map[string]SubmoduleInfo)
	var currentPath string
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[submodule ") {
			// [submodule "name"]
			name := strings.TrimPrefix(line, "[submodule ")
			name = strings.TrimSuffix(name, "]")
			name = strings.Trim(name, "\"")
			currentPath = ""
			if _, ok := result[currentPath]; !ok {
				result[currentPath] = SubmoduleInfo{Name: name}
			} else {
				info := result[currentPath]
				info.Name = name
				result[currentPath] = info
			}
			continue
		}
		if strings.HasPrefix(line, "path") && currentPath == "" {
			// path = sub/path
			val := strings.TrimSpace(strings.SplitN(line, "=", 2)[1])
			currentPath = val
			info := result[""]
			info.Path = val
			delete(result, "")
			result[currentPath] = info
		} else if strings.HasPrefix(line, "path") {
			val := strings.TrimSpace(strings.SplitN(line, "=", 2)[1])
			currentPath = val
			if info, ok := result[currentPath]; ok {
				info.Path = val
				result[currentPath] = info
			} else {
				result[currentPath] = SubmoduleInfo{Path: val}
			}
		} else if strings.HasPrefix(line, "url") && currentPath != "" {
			val := strings.TrimSpace(strings.SplitN(line, "=", 2)[1])
			info := result[currentPath]
			info.URL = val
			result[currentPath] = info
		} else if strings.HasPrefix(line, "branch") && currentPath != "" {
			val := strings.TrimSpace(strings.SplitN(line, "=", 2)[1])
			info := result[currentPath]
			info.Branch = val
			result[currentPath] = info
		}
	}
	return result, nil
}
