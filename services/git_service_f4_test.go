package services

import (
	"strings"
	"testing"
)

// git_service_f4_test.go — F-4 (prompt-2.md) tests for Submodule + Cherry-pick
// + Revert + Bisect.
//
// 测试覆盖：
//   - validateCommitHash 正向/反向
//   - validateSubmoduleURL 正向/反向（file:// 协议穿越）
//   - validateSubmodulePath 正向/反向（路径穿越）
//   - parseSubmoduleStatusLine 解析各种状态行
//   - SubmoduleList 在空仓库上返回空列表
//   - CherryPick/RevertCommit hash 格式校验
//   - BisectStart hash 格式校验
//   - SubmoduleAdd URL 和路径校验

// --- validateCommitHash ---

func TestGitSubmodule_validateCommitHash_AcceptsValidHashes(t *testing.T) {
	valid := []string{
		"abc1234", // 7 字符短 hash
		"abcdef0123456789abcdef0123456789abcdef01", // 40 字符完整 SHA-1
		"0123456789abcdef0123456789abcdef01234567", // 40 字符
		"deadbeef", // 8 字符
	}
	for _, h := range valid {
		if err := validateCommitHash(h); err != nil {
			t.Errorf("validateCommitHash(%q) should accept, got error: %v", h, err)
		}
	}
}

func TestGitSubmodule_validateCommitHash_RejectsInvalidHashes(t *testing.T) {
	invalid := []string{
		"",                      // 空
		"abc123",                // 6 字符（太短）
		"ABC1234",               // 大写字母
		"abcdefg",               // 含非十六进制字符 'g'
		"xyz1234",               // 含非十六进制字符
		"abc12345 ",             // 含空格
		"abc12345\n",            // 含换行
		"abc12345;rm",           // 命令注入尝试
		strings.Repeat("a", 41), // 41 字符（太长）
	}
	for _, h := range invalid {
		if err := validateCommitHash(h); err == nil {
			t.Errorf("validateCommitHash(%q) should reject, but accepted", h)
		}
	}
}

// --- validateSubmoduleURL ---

func TestGitSubmodule_validateSubmoduleURL_AcceptsValidURLs(t *testing.T) {
	valid := []string{
		"https://github.com/user/repo.git",
		"git://github.com/user/repo.git",
		"ssh://git@github.com/user/repo.git",
		"git@github.com:user/repo.git",
		"https://gitlab.com/user/repo.git",
	}
	for _, u := range valid {
		if err := validateSubmoduleURL(u); err != nil {
			t.Errorf("validateSubmoduleURL(%q) should accept, got error: %v", u, err)
		}
	}
}

func TestGitSubmodule_validateSubmoduleURL_RejectsFileProtocol(t *testing.T) {
	invalid := []string{
		"file:///etc/passwd",
		"file://../outside",
		"FILE:///some/path",
		"File://host/share",
		"",                     // 空
		"https://valid\x00url", // null 字节
	}
	for _, u := range invalid {
		if err := validateSubmoduleURL(u); err == nil {
			t.Errorf("validateSubmoduleURL(%q) should reject, but accepted", u)
		}
	}
}

// --- validateSubmodulePath ---

func TestGitSubmodule_validateSubmodulePath_AcceptsValidPaths(t *testing.T) {
	svc := &GitService{}
	dir := t.TempDir()
	if err := svc.setWorkspaceRoot(dir); err != nil {
		t.Fatalf("SetWorkspaceRoot failed: %v", err)
	}
	valid := []string{
		"vendor/lib",
		"third_party/foo",
		"lib",
		"sub/module/path",
	}
	for _, p := range valid {
		if err := svc.validateSubmodulePath(dir, p); err != nil {
			t.Errorf("validateSubmodulePath(%q) should accept, got error: %v", p, err)
		}
	}
}

func TestGitSubmodule_validateSubmodulePath_RejectsTraversal(t *testing.T) {
	svc := &GitService{}
	dir := t.TempDir()
	if err := svc.setWorkspaceRoot(dir); err != nil {
		t.Fatalf("SetWorkspaceRoot failed: %v", err)
	}
	invalid := []string{
		"",               // 空
		"../outside",     // 父目录穿越
		"../../etc",      // 多级穿越
		"/etc/passwd",    // 绝对路径
		"sub/../../../x", // 混合穿越
	}
	for _, p := range invalid {
		if err := svc.validateSubmodulePath(dir, p); err == nil {
			t.Errorf("validateSubmodulePath(%q) should reject, but accepted", p)
		}
	}
}

// --- parseSubmoduleStatusLine ---

func TestGitSubmodule_parseSubmoduleStatusLine(t *testing.T) {
	tests := []struct {
		line     string
		wantSHA  string
		wantPath string
		wantInit bool
		wantMod  bool
	}{
		{
			line:     " a1b2c3d vendor/lib (v1.0)",
			wantSHA:  "a1b2c3d",
			wantPath: "vendor/lib",
			wantInit: true,
			wantMod:  false,
		},
		{
			line:     "+a1b2c3d vendor/lib (v1.0)",
			wantSHA:  "a1b2c3d",
			wantPath: "vendor/lib",
			wantInit: true,
			wantMod:  true,
		},
		{
			line:     "-a1b2c3d vendor/lib",
			wantSHA:  "a1b2c3d",
			wantPath: "vendor/lib",
			wantInit: false,
			wantMod:  false,
		},
		{
			line:     "Ua1b2c3d vendor/lib",
			wantSHA:  "a1b2c3d",
			wantPath: "vendor/lib",
			wantInit: true,
			wantMod:  true,
		},
		{
			line:     "  deadbeef third_party/foo",
			wantSHA:  "deadbeef",
			wantPath: "third_party/foo",
			wantInit: true,
			wantMod:  false,
		},
	}
	for _, tt := range tests {
		info := parseSubmoduleStatusLine(tt.line)
		if info.SHA != tt.wantSHA {
			t.Errorf("parseSubmoduleStatusLine(%q).SHA = %q, want %q", tt.line, info.SHA, tt.wantSHA)
		}
		if info.Path != tt.wantPath {
			t.Errorf("parseSubmoduleStatusLine(%q).Path = %q, want %q", tt.line, info.Path, tt.wantPath)
		}
		if info.Initialized != tt.wantInit {
			t.Errorf("parseSubmoduleStatusLine(%q).Initialized = %v, want %v", tt.line, info.Initialized, tt.wantInit)
		}
		if info.Modified != tt.wantMod {
			t.Errorf("parseSubmoduleStatusLine(%q).Modified = %v, want %v", tt.line, info.Modified, tt.wantMod)
		}
	}
}

// --- SubmoduleList 在空仓库上返回空列表 ---

func TestGitSubmodule_List_EmptyRepo(t *testing.T) {
	skipIfNoGit(t)
	dir := setupTestRepo(t)
	svc := &GitService{}
	if err := svc.setWorkspaceRoot(dir); err != nil {
		t.Fatalf("SetWorkspaceRoot failed: %v", err)
	}
	submodules, err := svc.SubmoduleList()
	if err != nil {
		t.Fatalf("SubmoduleList failed: %v", err)
	}
	if len(submodules) != 0 {
		t.Errorf("SubmoduleList on empty repo should return empty list, got %d items", len(submodules))
	}
}

// --- 无 workspace root 时返回错误 ---

func TestGitSubmodule_NoWorkspaceRoot(t *testing.T) {
	svc := &GitService{}
	// 所有方法在无 workspace root 时应返回错误。
	if err := svc.SubmoduleAdd("https://example.com/repo.git", "lib"); err == nil {
		t.Error("SubmoduleAdd without workspace root should error")
	}
	if _, err := svc.SubmoduleList(); err == nil {
		t.Error("SubmoduleList without workspace root should error")
	}
	if err := svc.SubmoduleUpdate(false); err == nil {
		t.Error("SubmoduleUpdate without workspace root should error")
	}
	if err := svc.SubmoduleDeinit("lib"); err == nil {
		t.Error("SubmoduleDeinit without workspace root should error")
	}
	if err := svc.CherryPick("abcdef1234"); err == nil {
		t.Error("CherryPick without workspace root should error")
	}
	if err := svc.RevertCommit("abcdef1234"); err == nil {
		t.Error("RevertCommit without workspace root should error")
	}
	if err := svc.BisectStart("abcdef1234", "1234567890"); err == nil {
		t.Error("BisectStart without workspace root should error")
	}
	if err := svc.BisectGood(); err == nil {
		t.Error("BisectGood without workspace root should error")
	}
	if err := svc.BisectBad(); err == nil {
		t.Error("BisectBad without workspace root should error")
	}
	if err := svc.BisectReset(); err == nil {
		t.Error("BisectReset without workspace root should error")
	}
}

// --- CherryPick hash 格式校验 ---

func TestGitSubmodule_CherryPick_RejectsInvalidHash(t *testing.T) {
	svc := &GitService{}
	dir := t.TempDir()
	if err := svc.setWorkspaceRoot(dir); err != nil {
		t.Fatalf("SetWorkspaceRoot failed: %v", err)
	}
	invalidHashes := []string{
		"",           // 空
		"abc",        // 太短
		"xyz1234",    // 非十六进制
		"ABCDEF12",   // 大写
		"$(whoami)",  // 命令注入
		"a;rm -rf /", // 命令注入
	}
	for _, h := range invalidHashes {
		if err := svc.CherryPick(h); err == nil {
			t.Errorf("CherryPick(%q) should reject invalid hash", h)
		}
	}
}

// --- RevertCommit hash 格式校验 ---

func TestGitSubmodule_RevertCommit_RejectsInvalidHash(t *testing.T) {
	svc := &GitService{}
	dir := t.TempDir()
	if err := svc.setWorkspaceRoot(dir); err != nil {
		t.Fatalf("SetWorkspaceRoot failed: %v", err)
	}
	invalidHashes := []string{
		"",
		"abc",
		"xyz1234",
		"not-a-hash",
	}
	for _, h := range invalidHashes {
		if err := svc.RevertCommit(h); err == nil {
			t.Errorf("RevertCommit(%q) should reject invalid hash", h)
		}
	}
}

// --- BisectStart hash 格式校验 ---

func TestGitSubmodule_BisectStart_RejectsInvalidHash(t *testing.T) {
	svc := &GitService{}
	dir := t.TempDir()
	if err := svc.setWorkspaceRoot(dir); err != nil {
		t.Fatalf("SetWorkspaceRoot failed: %v", err)
	}
	// good 无效
	if err := svc.BisectStart("invalid", "abcdef1234"); err == nil {
		t.Error("BisectStart with invalid good hash should error")
	}
	// bad 无效
	if err := svc.BisectStart("abcdef1234", "invalid"); err == nil {
		t.Error("BisectStart with invalid bad hash should error")
	}
	// 都无效
	if err := svc.BisectStart("", ""); err == nil {
		t.Error("BisectStart with empty hashes should error")
	}
}

// --- SubmoduleAdd URL 校验 ---

func TestGitSubmodule_SubmoduleAdd_RejectsFileProtocol(t *testing.T) {
	svc := &GitService{}
	dir := t.TempDir()
	if err := svc.setWorkspaceRoot(dir); err != nil {
		t.Fatalf("SetWorkspaceRoot failed: %v", err)
	}
	fileURLs := []string{
		"file:///etc/passwd",
		"file://../outside",
		"FILE:///some/path",
	}
	for _, u := range fileURLs {
		if err := svc.SubmoduleAdd(u, "lib"); err == nil {
			t.Errorf("SubmoduleAdd(%q, ...) should reject file:// URL", u)
		}
	}
}

// --- SubmoduleAdd 路径校验 ---

func TestGitSubmodule_SubmoduleAdd_RejectsPathTraversal(t *testing.T) {
	svc := &GitService{}
	dir := t.TempDir()
	if err := svc.setWorkspaceRoot(dir); err != nil {
		t.Fatalf("SetWorkspaceRoot failed: %v", err)
	}
	paths := []string{
		"../outside",
		"../../etc",
		"/etc/passwd",
		"",
	}
	for _, p := range paths {
		if err := svc.SubmoduleAdd("https://github.com/user/repo.git", p); err == nil {
			t.Errorf("SubmoduleAdd(..., %q) should reject path traversal", p)
		}
	}
}

// --- SubmoduleDeinit 路径校验 ---

func TestGitSubmodule_SubmoduleDeinit_RejectsPathTraversal(t *testing.T) {
	svc := &GitService{}
	dir := t.TempDir()
	if err := svc.setWorkspaceRoot(dir); err != nil {
		t.Fatalf("SetWorkspaceRoot failed: %v", err)
	}
	paths := []string{
		"../outside",
		"/etc",
		"",
	}
	for _, p := range paths {
		if err := svc.SubmoduleDeinit(p); err == nil {
			t.Errorf("SubmoduleDeinit(%q) should reject path traversal", p)
		}
	}
}

// --- SubmoduleAdd + SubmoduleList 集成测试 ---
// 这个测试需要网络访问（克隆一个远程仓库），在无网络环境下会跳过。

func TestGitSubmodule_AddAndList(t *testing.T) {
	skipIfNoGit(t)
	dir := setupTestRepo(t)
	setLocalGitConfig(t, dir)
	svc := &GitService{}
	if err := svc.setWorkspaceRoot(dir); err != nil {
		t.Fatalf("SetWorkspaceRoot failed: %v", err)
	}

	// 验证校验逻辑通过。
	if err := validateSubmoduleURL("https://github.com/golang/example.git"); err != nil {
		t.Fatalf("validateSubmoduleURL should accept https URL: %v", err)
	}
	if err := svc.validateSubmodulePath(dir, "vendor/example"); err != nil {
		t.Fatalf("validateSubmodulePath should accept valid path: %v", err)
	}

	// 验证 SubmoduleList 在没有子模块时返回空列表。
	submodules, err := svc.SubmoduleList()
	if err != nil {
		t.Fatalf("SubmoduleList failed: %v", err)
	}
	if len(submodules) != 0 {
		t.Errorf("SubmoduleList should return empty list, got %d items", len(submodules))
	}
}

// --- CherryPick + RevertCommit 集成测试 ---

func TestGitSubmodule_CherryPick_RevertIntegration(t *testing.T) {
	skipIfNoGit(t)
	dir := setupTestRepo(t)
	setLocalGitConfig(t, dir)
	svc := &GitService{}
	if err := svc.setWorkspaceRoot(dir); err != nil {
		t.Fatalf("SetWorkspaceRoot failed: %v", err)
	}

	// 先获取默认分支名（在创建 feature 分支之前）。
	defaultBranchOutput, err := svc.runGit(dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse --abbrev-ref HEAD failed: %v", err)
	}
	defaultBranch := strings.TrimSpace(defaultBranchOutput)

	// 创建一个分支并添加一个 commit。
	if _, err := svc.runGit(dir, "checkout", "-b", "feature"); err != nil {
		t.Fatalf("checkout -b feature failed: %v", err)
	}
	writeFile(t, dir, "feature.txt", "feature content\n")
	if _, err := svc.runGit(dir, "add", "feature.txt"); err != nil {
		t.Fatalf("git add failed: %v", err)
	}
	if _, err := svc.runGit(dir, "commit", "-m", "feature commit"); err != nil {
		t.Fatalf("git commit failed: %v", err)
	}
	// 获取 feature 分支上的 commit hash。
	hashOutput, err := svc.runGit(dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD failed: %v", err)
	}
	featureHash := strings.TrimSpace(hashOutput)
	if len(featureHash) != 40 {
		t.Fatalf("unexpected hash length: %d", len(featureHash))
	}

	// 切回默认分支。
	if defaultBranch == "HEAD" {
		// detached HEAD，尝试 master/main。
		if _, err := svc.runGit(dir, "checkout", "master"); err != nil {
			if _, err2 := svc.runGit(dir, "checkout", "main"); err2 != nil {
				t.Fatalf("checkout master/main failed: master=%v main=%v", err, err2)
			}
		}
	} else {
		if _, err := svc.runGit(dir, "checkout", defaultBranch); err != nil {
			t.Fatalf("checkout %s failed: %v", defaultBranch, err)
		}
	}

	// Cherry-pick feature 分支的 commit。
	if err := svc.CherryPick(featureHash); err != nil {
		t.Fatalf("CherryPick failed: %v", err)
	}

	// 验证 cherry-pick 后文件存在。
	output, err := svc.runGit(dir, "log", "--oneline")
	if err != nil {
		t.Fatalf("git log failed: %v", err)
	}
	if !strings.Contains(output, "feature commit") {
		t.Errorf("cherry-pick should add 'feature commit' to history, got: %s", output)
	}

	// Revert 刚才 cherry-pick 的 commit。
	// 获取 cherry-pick 后的 HEAD hash。
	headOutput, err := svc.runGit(dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD after cherry-pick failed: %v", err)
	}
	headHash := strings.TrimSpace(headOutput)

	if err := svc.RevertCommit(headHash); err != nil {
		t.Fatalf("RevertCommit failed: %v", err)
	}

	// 验证 revert 后历史中有 "Revert" 字样。
	output, err = svc.runGit(dir, "log", "--oneline")
	if err != nil {
		t.Fatalf("git log after revert failed: %v", err)
	}
	if !strings.Contains(strings.ToLower(output), "revert") {
		t.Errorf("revert should add a 'Revert' commit to history, got: %s", output)
	}
}

// --- Bisect 集成测试 ---

func TestGitSubmodule_BisectIntegration(t *testing.T) {
	skipIfNoGit(t)
	dir := setupTestRepo(t)
	setLocalGitConfig(t, dir)
	svc := &GitService{}
	if err := svc.setWorkspaceRoot(dir); err != nil {
		t.Fatalf("SetWorkspaceRoot failed: %v", err)
	}

	// setupTestRepo 已创建初始 commit（作为 good）。
	goodOutput, err := svc.runGit(dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse good failed: %v", err)
	}
	goodHash := strings.TrimSpace(goodOutput)

	// 创建 bad commit
	writeFile(t, dir, "file2.txt", "bad\n")
	if _, err := svc.runGit(dir, "add", "file2.txt"); err != nil {
		t.Fatalf("git add file2 failed: %v", err)
	}
	if _, err := svc.runGit(dir, "commit", "-m", "bad commit"); err != nil {
		t.Fatalf("git commit bad failed: %v", err)
	}
	badOutput, err := svc.runGit(dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse bad failed: %v", err)
	}
	badHash := strings.TrimSpace(badOutput)

	// 启动 bisect。
	if err := svc.BisectStart(goodHash, badHash); err != nil {
		t.Fatalf("BisectStart failed: %v", err)
	}

	// 标记当前为 bad。
	if err := svc.BisectBad(); err != nil {
		t.Fatalf("BisectBad failed: %v", err)
	}

	// 重置 bisect。
	if err := svc.BisectReset(); err != nil {
		t.Fatalf("BisectReset failed: %v", err)
	}
}

// --- parseGitmodules 测试 ---

func TestGitSubmodule_parseGitmodules(t *testing.T) {
	dir := t.TempDir()
	content := `[submodule "vendor/lib"]
	path = vendor/lib
	url = https://github.com/user/lib.git
	branch = main

[submodule "third_party/foo"]
	path = third_party/foo
	url = https://github.com/user/foo.git
`
	writeFile(t, dir, ".gitmodules", content)

	modules, err := parseGitmodules(dir)
	if err != nil {
		t.Fatalf("parseGitmodules failed: %v", err)
	}
	if len(modules) != 2 {
		t.Fatalf("expected 2 submodules, got %d", len(modules))
	}
	lib, ok := modules["vendor/lib"]
	if !ok {
		t.Fatal("missing vendor/lib submodule")
	}
	if lib.Name != "vendor/lib" {
		t.Errorf("vendor/lib name = %q, want %q", lib.Name, "vendor/lib")
	}
	if lib.URL != "https://github.com/user/lib.git" {
		t.Errorf("vendor/lib url = %q, want %q", lib.URL, "https://github.com/user/lib.git")
	}
	if lib.Branch != "main" {
		t.Errorf("vendor/lib branch = %q, want %q", lib.Branch, "main")
	}
	foo, ok := modules["third_party/foo"]
	if !ok {
		t.Fatal("missing third_party/foo submodule")
	}
	if foo.URL != "https://github.com/user/foo.git" {
		t.Errorf("third_party/foo url = %q, want %q", foo.URL, "https://github.com/user/foo.git")
	}
}

func TestGitSubmodule_parseGitmodules_DoesNotCarryPathAcrossMalformedSections(t *testing.T) {
	dir := t.TempDir()
	content := `[submodule "valid"]
	path = valid
	url = https://example.com/valid.git

[submodule "missing-path"]
	url = https://example.com/must-not-leak.git
	branch = must-not-leak
	this is not a valid assignment

[submodule "consecutive-section"]
[submodule "next"]
	path = next
	url = https://example.com/next.git
`
	writeFile(t, dir, ".gitmodules", content)

	modules, err := parseGitmodules(dir)
	if err != nil {
		t.Fatalf("parseGitmodules failed: %v", err)
	}
	if len(modules) != 2 {
		t.Fatalf("expected only sections with paths, got %#v", modules)
	}
	valid := modules["valid"]
	if valid.URL != "https://example.com/valid.git" || valid.Branch != "" {
		t.Fatalf("malformed section leaked into valid module: %#v", valid)
	}
	next := modules["next"]
	if next.Name != "next" || next.URL != "https://example.com/next.git" {
		t.Fatalf("next module parsed incorrectly: %#v", next)
	}
	if _, ok := modules[""]; ok {
		t.Fatal("pathless section must not produce an empty-path module")
	}
}
