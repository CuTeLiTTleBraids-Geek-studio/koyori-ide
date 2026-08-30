package services

import (
	"path/filepath"
	"testing"
)

// canonicalTestPath 把测试期望/输入中的路径解析到与生产层一致的规范形态
// （macOS /var → /private/var 符号链接前缀、Windows 8.3 短名展开）。
//
// 背景（P19 CI 修复）：GitHub 的 Windows runner TEMP 是 8.3 短名
// （C:\Users\RUNNER~1\...），macOS 的 TEMP 带 /var 符号链接前缀，而生产层
// （WorkspaceContext、FileService 安全根、agent 根等）统一以
// EvalSymlinks 解析后的形态保存与比较工作区路径。在开发机上两者恰好
// 相等，测试因此掩盖了拼写差异；在 CI 上，任何拿 t.TempDir() 原始拼写
// 与服务返回值做字符串相等比较的断言都会失败。测试侧的期望值统一用
// 本助手解析成规范形态；目标不存在时解析最深存在祖先再拼回。
func canonicalTestPath(t *testing.T, path string) string {
	t.Helper()
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}
	if resolved, err := evalSymlinksAllowMissing(path); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(path)
}
