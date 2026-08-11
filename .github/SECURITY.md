# 安全策略 / Security Policy

## 支持版本 / Supported Versions

版本单一事实源是仓库根目录的 `VERSION` 文件。本表只描述该文件声明的开发线，
不声称任何已验证的历史发布。

The single source of truth for the version is the repository-root `VERSION`
file. This table describes only the line that file declares. It does not assert
any verified historical release.

| 版本 / Version | 支持 / Supported | 说明 / Notes |
|---|---|---|
| **0.2.x** | 🟡 尽力而为 / best-effort | `VERSION` 声明的当前开发线；预发布，无 SLA / current development line per `VERSION`; pre-release, no SLA |
| 更早 / earlier | ❌ | 请升级 / please upgrade |
| 1.0.x | 规划中 / planned | 正式 1.0 后按 semver 更新本表 / table updated per semver after 1.0 |

**关于此前列出的 0.3.x / 0.4.x / 0.5.x：** 本文件此前同时把 0.4.x 和 0.2.x 标为
"✅ 当前发行线"，并声称存在 `v0.4.0` 标签。仓库内没有可核对的 git tag 或 release
证据支持这些说法，因此它们已被移除，而不是保留为未经验证的支持承诺。恢复任何一行
都需要先提供真实的 tag 与 release artifact 证据。

**On the previously listed 0.3.x / 0.4.x / 0.5.x lines:** this file simultaneously
marked both 0.4.x and 0.2.x as the current supported release line and claimed a
`v0.4.0` tag exists. No verifiable git tag or release evidence in this repository
supports those claims, so they have been removed rather than kept as unverified
support commitments. Restoring any line requires real tag and artifact evidence
first.

`main` 和未发布版本仅接受尽力而为的修复，不等同于受支持的稳定发行版。所有 0.x 版本均基于 Wails v3 alpha；本表不构成 SLA 或企业支持承诺。项目当前无响应或修复 SLO、无产品可靠性 SLO 数据，且未接受独立外部安全审计、独立供应链审计或独立可访问性审计；这些状态均为 `U`。

`main` and unreleased versions receive best-effort fixes only and are not supported stable releases. All 0.x versions use Wails v3 alpha; this table is not an SLA or an enterprise-support commitment. There is no response or remediation SLO and no product-reliability SLO data. The project has not undergone an independent external security audit, supply-chain audit, or accessibility audit; all of these statuses are `U`.

### 发版周期 / Release cadence

- **Patch**（`0.x.Y`）：按需，含安全与回归修复 / as needed for security & regressions  
- **Minor**（`0.X.0`）：功能里程碑；附 CHANGELOG 与 Release 资产 / feature milestones + release assets  
- **安全响应 / Response：** 尽力而为；不承诺确认、修复或披露期限 / best-effort; no acknowledgment, remediation, or disclosure deadline  

## 保证证据状态 / Assurance Evidence Status

| 项目 / Area | 状态 / Status | 边界 / Boundary |
|---|---|---|
| 产品可靠性 SLO / Product reliability SLO | `U` | 无真实发布 cohort、生产遥测、crash-free 或延迟/错误率历史 / no release cohort, production telemetry, crash-free, latency, or error-rate history |
| 安全外审 / External security audit | `U` | 内部测试存在；无独立 assessor/report / internal tests only; no independent assessor/report |
| 供应链外审 / External supply-chain audit | `U` | NOTICE/SBOM/provenance 是内部控制，不是第三方审计 / internal controls, not a third-party audit |
| 可访问性外审 / External accessibility audit | `U` | 部分 ARIA/键盘测试；无 WCAG/屏幕阅读器/跨平台报告 / partial implementation tests; no WCAG/screen-reader/platform report |

未来采集条件只记录在 [RELEASING.md](../docs/RELEASING.md#slo-and-external-audit-release-inputs)；当前没有遥测实现或默认数据收集。

Future collection conditions are documented only in [RELEASING.md](../docs/RELEASING.md#slo-and-external-audit-release-inputs). No telemetry implementation or default data collection exists today.

## 漏洞报告 / Reporting a Vulnerability

### 中文
1. **不要**为安全漏洞公开开 GitHub Issue。  
2. 首选仓库的 [私密漏洞报告表单](https://github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/security/advisories/new)。  
3. 如果该表单不可用，发送至 README 中公开的维护者邮箱 **dianasoylu423@gmail.com**，主题注明 `[Koyori IDE Security]`。不要发送真实 API key、私钥或用户数据。  
4. 维护者会尽力确认和处理，但当前没有响应或修复 SLO；请在收到协调回复前不要公开细节。  

请尽量包含：漏洞描述、复现步骤、受影响组件、潜在影响、建议修复（如有）。

### English
1. **Do NOT** open a public GitHub issue for security vulnerabilities.  
2. Prefer the repository's [private vulnerability report form](https://github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/security/advisories/new).  
3. If the form is unavailable, email the public maintainer contact **dianasoylu423@gmail.com** with subject `[Koyori IDE Security]`. Do not send real API keys, private keys, or user data.  
4. Maintainers will respond on a best-effort basis. No response or remediation SLO is offered; please keep details private until coordinated.  

Include: description, steps to reproduce, affected components, potential impact, suggested fix (optional).

## CI 安全门禁 / Continuous Integration Security Gates

`.github/workflows/ci.yml` 为推送/PR 配置下列门禁。仓库没有可核验的 CI 历史，因此下表是源码级 `S` 证据，不是某次 run 已通过的声明。

| 门禁 / Gate | 要求 / Requirement |
|---|---|
| Race detector (G-SEC-04) | 三平台 Go matrix 配置 `go test -race ./services/... .` |
| govulncheck (G-SEC-04) | Ubuntu job 配置固定 v1.6.0 扫描；本机因网络阻塞未完成 |
| Frontend type/lint/test | 三平台 matrix 配置 `vue-tsc`、ESLint、Vitest 与 npm audit high 阻断 |
| go vet / golangci-lint | 三平台 vet；Ubuntu golangci-lint |
| Frontend coverage | Ubuntu Vitest 四项 50% 门禁；报告作为 artifact |
| Wails build | 仅 Ubuntu 配置 `wails3 build -tags desktop,production`；不是三平台 artifact 验证 |
| Packaged E2E | 仅 `workflow_dispatch` Linux qualification；尚非 required，真实运行 U |
| Release supply chain | tag workflow 源码强制 NOTICE/许可证、SPDX SBOM、未签名 provenance 与最终校验和；真实 tag run U |

Go 与前端 matrix 覆盖 Ubuntu / Windows / macOS；Wails build、coverage、govulncheck 与 packaged qualification 的平台范围如上。没有 workflow run URL 时，不得把配置写成 CI 已通过。

## 安全措施摘要 / Security Measures Summary

| ID | 中文 | English |
|---|---|---|
| G-SEC-01 | AI BaseURL 校验，防 SSRF / 禁 userinfo；非回环强制 HTTPS | BaseURL validation; SSRF / credential-leak prevention |
| G-SEC-02 | Agent 命令强制人工审批，无 run 自动批准 | All agent shell commands require manual approval |
| G-SEC-03 | 项目级工作流不可信，启动类不自动执行 | Untrusted workflows never auto-run on load |
| G-SEC-04 | CI 源码配置 race + govulncheck；真实 run 待证据 | Race detector + govulncheck configured; run evidence pending |
| G-SEC-05 | iframe `sandbox="allow-scripts"`，无 allow-same-origin | Extension iframes without same-origin |
| G-SEC-06 | 路径双侧 EvalSymlinks，防符号链接逃逸 | Symlink-aware path sandbox |
| G-SEC-07 | API Key 加密存储且不回传前端明文 | Encrypted API keys; never returned to frontend |
| G-SEC-08 | 错误响应体限制 64KB | Error body limited to 64 KB |
| G-SEC-09 | 关键 JSON 原子写 + 0600 | Atomic JSON writes, 0600 perms |
| G-SEC-10 | CSP nonce 使用 crypto/rand，无弱回退 | CSP nonces from crypto/rand only |
| G-SEC-11 | Markdown 外链强制 noopener | Links forced `rel=noopener` |
| G-SEC-12 | 扩展 SHA-256、默认禁用、权限分级与黑名单 | VSIX hash, default-disabled, classification, blacklist |

### 实验性能力 / Experimental capabilities

- Computer Use 默认关闭；Windows/Linux/macOS 平台操作均返回 unsupported。它不应被视为完整或经安全审计的平台自动化能力。
- IM 仅允许经批准的出站消息；没有入站收信、轮询或命令执行入口。
- 更新采用 E2：只下载经 SHA-256 校验的包；安装、重启与回滚由用户手动完成。
- 最小 SSH/SFTP 与受限扩展宿主扩大了网络/代码执行攻击面，但不构成完整 Remote-SSH 或 VS Code 扩展兼容性。只启用可信配置与扩展。

Computer Use is off by default and Windows/Linux/macOS actions remain unsupported. IM is outbound-only. Updates stop after a verified download and require manual installation. Minimal SSH/SFTP and the constrained extension host are security-sensitive surfaces, not claims of full Remote-SSH or VS Code compatibility.

### 路径沙箱 / Path Sandboxing
所有文件操作限制在工作区根内；`pathsec` 防止目录遍历与符号链接逃逸。终端与 Agent CWD 同样校验。

All file operations are sandboxed to the workspace root with symlink evaluation. Terminal and agent CWDs are validated similarly.

### XSS 防护 / XSS Prevention
Markdown 经 DOMPurify 清洗；Vue 模板默认转义用户输入。  
Markdown is sanitized with DOMPurify; Vue escapes other UI input by default.

### API Key
静态加密（Windows DPAPI / macOS Keychain / Linux AES 或 Secret Service），仅发往用户配置的 AI 端点；不记入日志。  
Keys are encrypted at rest and only sent to the user-configured AI provider; never logged.

### 依赖安全 / Dependency Security
CI 源码配置 `govulncheck` 与阻断型 `npm audit --audit-level=high`。tag workflow 还要求当前 NOTICE/许可证清单与 SPDX SBOM。2026-08-03 本机 `govulncheck`/`go-licenses` 外部下载失败，不能作为扫描已通过的证据。  
CI source config includes `govulncheck` and blocking `npm audit --audit-level=high`; tag releases also require current notices, the license inventory, and an SPDX SBOM. External downloads for local `govulncheck`/`go-licenses` failed on 2026-08-03, so no successful local scan is claimed.

## 安全响应头 / Security Headers

Wails 资源中间件注入：
- `Content-Security-Policy`（`script-src 'nonce-...'`，无 `unsafe-inline`）
- `X-Content-Type-Options: nosniff`
- `X-Frame-Options: DENY`
- `Referrer-Policy: no-referrer`

桌面 WebView 场景下不额外做浏览器 CSRF/CORS 方案；外链在系统浏览器打开。

## 协调披露 / Coordinated Disclosure

报告、分级、修复与披露按个案尽力协调。没有固定天数承诺。修复可用且受影响用户有合理迁移时间后，再与报告者协商公开；如果维护者无法继续处理，会通过私密渠道说明当前状态，不伪造已修复结论。

Reports, triage, remediation, and disclosure are coordinated case by case on a best-effort basis; no fixed-day commitment is offered. Public disclosure should be discussed after a fix is available and affected users have reasonable migration time. If maintainers cannot continue, they will state the current status privately rather than claim a resolution.

## 联系 / Contact

- 私密漏洞报告 / Private report: [GitHub Security Advisory](https://github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/security/advisories/new)  
- 邮件回退 / Email fallback: dianasoylu423@gmail.com  
- 一般问题 / General: [GitHub Issues](https://github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/issues)  
