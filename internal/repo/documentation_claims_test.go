package repo

import (
	"os"
	"strings"
	"testing"
)

func TestG18ReadmeCapabilityMatrixAndBoundaries(t *testing.T) {
	raw, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	for _, required := range []string{
		"### 当前能力与验证边界（V/S/U）",
		"`V` = 本机实际命令通过",
		"本地编辑与保存", "Git", "LSP", "AI", "Agent", "Recovery",
		"最小 Remote", "Debug / Test", "插件 / VSIX", "发布供应链",
		"gopls`、`typescript-language-server`、`vtsls` 均未安装",
		"不是 VS Code、Cursor 或 IntelliJ 的替代品",
		"不宣称生产级或企业就绪",
	} {
		if !strings.Contains(doc, required) {
			t.Errorf("README.md is missing honest capability boundary %q", required)
		}
	}
	for _, forbidden := range []string{
		"ready out of the box",
		"真实 `gopls` 集成路径已验证",
		"A real gopls path has been verified",
		"支持任何 OpenAI 兼容 API",
		"安全漏洞扫描 |",
	} {
		if strings.Contains(doc, forbidden) {
			t.Errorf("README.md retains overclaim %q", forbidden)
		}
	}
}

func TestG18SecurityPolicyHasNoUnverifiedSLOOrAuditClaim(t *testing.T) {
	raw, err := os.ReadFile("../../.github/SECURITY.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	for _, required := range []string{
		"Wails v3 beta.8", "best-effort", "无响应或修复 SLO", "未接受独立外部安全审计",
		"No response or remediation SLO", "has not undergone an independent external security audit",
		"security/advisories/new", "dianasoylu423@gmail.com",
	} {
		if !strings.Contains(doc, required) {
			t.Errorf("SECURITY.md is missing policy boundary %q", required)
		}
	}
	for _, forbidden := range []string{
		"48 小时内", "within **48 hours**", "7 天内", "within **7 days**",
		"## 披露时间线 / Disclosure Timeline",
	} {
		if strings.Contains(doc, forbidden) {
			t.Errorf("SECURITY.md retains unverified service target %q", forbidden)
		}
	}
}

func TestG18ReleasingDocumentsPackagedEvidenceBoundary(t *testing.T) {
	raw, err := os.ReadFile("../../docs/RELEASING.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	for _, required := range []string{
		"## Packaged E2E qualification", "workflow_dispatch", "three consecutive",
		"wails3", "remains `U`", "sbom.spdx.json", "provenance.intoto.jsonl",
		"real tag build", "Signing status",
	} {
		if !strings.Contains(doc, required) {
			t.Errorf("docs/RELEASING.md is missing release evidence boundary %q", required)
		}
	}
}
