# Koyori IDE / Gugacode 多轮功能、AI、IDE、BUG 与可开源性审查（prompt-13）

> **用途：** 对当前工作区做一次独立、可复核的全面审查，覆盖前后端功能完整性、AI 可用性、IDE 可用性、前端显示 BUG、后端 BUG、可开源性，以及 `docs/prompts/prompt-1.md` ~ `prompt-12.md` 的声明是否真实落地。
>
> **与既有文档的关系：** prompt-1~11 是 Goal/AC 续作台账；prompt-12 是 2026-08-12 的审查结论 + P12-G28~G33 规划。本文是 **2026-08-21 工作区快照的复审**，不替代 prompt-9 的执行纪律，但会明确标出 prompt-12 已过时的结论。
>
> **事实优先级：** 当前代码与本机实际命令 > 本文 > prompt-12 > prompt-11/9 进度板叙述。历史 packaged SHA / CI run 只作对照，不自动升级为当前代码态证据。
>
> **审查方式：** 静态源码取证（S）+ 定向命令（T/V）+ 既有测试/契约阅读（T）+ 既有 packaged/集成记录对照（P/I，不重新宣称）。未启动桌面 GUI、未调用真实 AI provider、未跑全量 Go/Vitest、未访问 GitHub Actions。
>
> **工作区：** `%USERPROFILE%\Downloads\Gugacode-main`
> **审查日期：** 2026-08-21

---

## 0. 证据分级（沿用 prompt-9，不可弱化）

| 级 | 含义 | 本文如何使用 |
|---|---|---|
| **S** | 静态源码/配置/文档存在 | 打开文件并引用 `file:line` |
| **T** | 单测/mock/contract/门禁脚本 | 测试文件或本机脚本 exit 0 |
| **I** | 真实子进程/真实服务 | 仅当代码或既有记录证明真实进程 |
| **P** | 真实 packaged 桌面工作流 | 仅引用已落盘 manifest；本会话未重跑 |
| **R** | 真实 CI/tag/release/签名/公证 | 仅当有可核验 run/tag/artifact |
| **U** | 未验证、环境阻塞或证据缺失 | 明确写出缺什么 |
| **V** | 本机命令实际通过（prompt-5 口径） | 本会话仅少量门禁 |

**禁止：** 把 mock、dry-run、YAML、设计文档、进度板勾选或历史 SHA 写成当前产品可用。

---

## 1. 总体结论（TL;DR）

**一句话：这不是空壳 Monaco 外壳，而是工程纪律很强的 0.x 桌面 AI IDE；本地编辑/Git/终端/Chat+Agent 主路径有实质实现，但按仓库自己的门禁仍未达到「开源发布合格」「生产级」「自治编码 Agent」「远程 IDE」。**

| 维度 | 结论 | 证据密度 |
|---|---|---|
| 后端功能完整性 | **本地主路径完整**；远程/更新安装/崩溃上报/Computer Use/Goal 自治为半成品或 stub | 高 |
| 前端功能完整性 | **几乎无占位屏**；巨型组件、双窗设置、部分入口未接全 | 高 |
| 前端显示 BUG | **确认 1 个版本号显示错误**；若干历史溢出 BUG 已在源码中修补；无 GUI 复现 | 中高 |
| 后端 BUG / 正确性 | **确认**：Goal 伪执行器、Plan 无生成工具、三路合并简化算法、自动更新拒绝安装、崩溃上报无端点、Computer Use 全平台 stub、服务计数文档漂移 | 高 |
| AI 可用性 | **Chat + 后端统一工具目录可用**；MCP 已接入 catalog（prompt-12 已过时）；Plan/Goal 仍未闭环；Computer Use 不可用 | 高 |
| IDE 可用性 | **本地 Go/TS 单机可日常使用（条件性）**；LSP/DAP 取决于本机工具；远程不是 IDE | 高 |
| 可开源性 | **许可证与治理文件齐全，适合实验性开源**；发布供应链、CI 历史、工作树卫生、跨平台 packaged **不合格** | 高 |
| Prompt 落地 | **安全/恢复/WorkspaceContext 等 P0 大多已落地**；P2 远程宿主、发布运营、i18n 矩阵、Goal 真执行仍未完成；prompt-12 若干安全/AI 结论需修正 | 高 |

**三个硬结论：**

1. **不可宣称生产级 / 企业就绪 / VS Code·Cursor 替代品。** README、SECURITY.md、ARCHITECTURE.md 自己也这样写；Wails v3 仍是 `alpha2.111`。
2. **不可宣称开源发布合格。** prompt-9 进度板：G07/G08/G09/G10/G19/G21 仍阻塞（真实 CI/macOS/发布 `U`）；G25/G26/G27/G33 AC 未勾选；当前工作树 **289 条 porcelain**（约 182 修改 / 107 未跟踪），`docs/prompts/prompt-12.md` 本身都还没入库。
3. **不可宣称「自治编码 Agent / 远程 IDE / 全语言」。** Goal 内置执行器自报 prototype 且默认拒绝；Remote 是 SSH/SFTP；开箱完整语言包仍是 Go/TS 优先。

---

## 2. 本会话实际跑过的命令（V）与明确没跑的（U）

### 2.1 本机通过（V）

| 检查 | 结果 |
|---|---|
| `node scripts/check-doc-numbers.mjs` | exit 0，`MAX_TOOL_CALLS=20` 与 README 对齐 |
| `git rev-parse HEAD` / `git log -1` / `git tag` / `git status -sb` | HEAD `18b43cf0825f1e280dc56b54563c8f73506bbd36`（`feat: establish G27 release operations contracts`，2026-08-12）；分支 `release/v0.2.0...origin/release/v0.2.0 [ahead 3]`；tag `beta0.2.0`、`g26-foundation-backup` |
| `git ls-files` 敏感文件扫描 | 未跟踪 `.pem/.key/.p12/.env/credentials.json/secrets.json/id_rsa` |
| `go version` | `go1.26.4 windows/amd64`（项目 `go.mod` 目标 `1.25.0`） |
| `node -v` | `v24.18.0` |
| `wails3 version` | `v3.0.0-alpha2.111`（与 `go.mod` 锁定一致） |

### 2.2 本会话未跑（U，不等于项目失败）

- `go test ./...` / `-race` / `node scripts/backend-gate.mjs`
- `task frontend:check` / Vitest / ESLint / `vue-tsc`
- `node scripts/check-bindings.mjs` / `npm-audit-gate.mjs` / `govulncheck`
- `wails3 build` / `node scripts/packaged-e2e.mjs`
- 真实 OpenAI/Anthropic/Ollama 调用、真实 SSH、真实 gopls 会话、桌面 GUI 点击

> PowerShell 执行策略阻止了 `npm -v`（`npm.ps1 cannot be loaded`）。Node 本身可用。这是本机策略问题，不是仓库缺陷。

---

## 3. 仓库快照（审查对象）

| 项 | 值 | 级 |
|---|---|---|
| 产品名 | Koyori IDE（こより IDE）；历史标识 `gugacode` / `koyori-ide` 并存 | S |
| 版本 SSOT | `VERSION` = `0.2.0`；`frontend/package.json` = `0.2.0` | S |
| 模块 | `github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide` | S |
| 栈 | Go 1.25 + Wails v3 `v3.0.0-alpha2.111` + Vue 3 + TS + Vite 8 + Monaco 0.52 + Element Plus | S |
| 后端服务注册 | `bootstrap_services.go:88-138` **47** 次 `application.NewService` | S |
| `services/*.go` | 367 个文件，其中 `*_test.go` 187 个 | S |
| 前端 | `frontend/src` 约 425 个 ts/vue/js；93 个 `.vue` 组件；16 个 views | S |
| Prompt 档案 | `docs/prompts/prompt-1,4,5,6,7,8,9,10,11,12.md`；**无 prompt-2/3**（prompt-1 仍引用为历史线索） | S |
| 许可 | MIT（`LICENSE`）；NOTICE 声明生产闭包无 GPL/AGPL | S |

根目录还存在被 gitignore 的本地二进制：`gugacode`（58.6 MB）、`koyori-ide`（58.0 MB）、`server-gateway.test.exe`（10.9 MB）。它们**不应**随源码发布。

---

## 4. 前后端功能完整性

### 4.1 后端服务面（S）

`bootstrap_services.go:85-139` 实际注册 47 个 Wails 服务：

File, Project, Settings, HTTPClient, Database, Window, Terminal, AI, Git, GitWorktree, GitRebase, PullRequest, Search, Conversation, Task, Workflow, Agent, Rules, LogLevel, Plugin, Profile, Layout, LSP, Toolchain, LanguagePacks, Marketplace, ExtensionSecurity, Activation, MCP, Skills, ComputerUse, IM, Persona, AIPlan, AIGoal, AIPermission, Diff, Snapshot, Debug, Coverage, Eslint, SymbolIndex, PProf, Update, Crash, Remote, Recovery。

**文档漂移（P13-G01 已修）：** README / ARCHITECTURE.md / `bootstrap_services.go` 头注释均为 47；`scripts/check-doc-numbers.mjs` 现锁定 `application.NewService` 次数 = 47 且 README/ARCHITECTURE 文案同步。

### 4.2 前端表面（S）——几乎无桩

已观察到真实实现、不是空页面的模块：

| 区域 | 入口 | 判定 |
|---|---|---|
| 欢迎 / 打开 / 新建项目 | `WelcomeView.vue`, `ProjectsView.vue`, `NewProjectWizard.vue` | 完整 |
| Monaco 编辑 / 多标签 / Diff | `CodeEditor.vue`, `TabBar.vue`, `DiffView.vue` | 完整 |
| 文件树 / 命令面板 / 快速打开 | `FileTree.vue`, `CommandPalette.vue`, `QuickOpen.vue` | 完整 |
| 终端 | `TerminalPanel.vue` + xterm + 后端 PTY | 完整（配置/macOS 信号仍 U） |
| Git / rebase / worktree / PR | `GitPanel.vue`, `RebaseEditor.vue`, `WorktreePanel.vue`, `PullRequestPanel.vue` | 超完整 |
| AI 聊天 / Agent / 双窗 | `AiChatPanel.vue`, `AiWindowView.vue`, `AiAssistantView.vue` | Chat/Agent 主路径完整；Plan/Goal 半成品 |
| LSP / Outline / 重构预览 | `stores/lsp.ts`, `OutlinePanel.vue`, `RefactorPreviewModal.vue` | 协议层完整；真实语言服务器 I/P 视本机 |
| Debug / Test / Coverage | `DebugPanel.vue`, `TestView.vue`, `coverage` store | 代码完整；真实 Delve/Node 依环境 |
| 市场 / 插件 / 扩展宿主 | `MarketplacePanel.vue`, `pluginRegistry.ts`, `vscodeExtensionActivation.ts` | 受限 VS Code 子集，不是完整 Extension Host |
| HTTP Client / Database | `HTTPClientPanel.vue`, `DatabaseToolWindow.vue` | 完整（私网需后端令牌） |
| Remote | `RemoteView.vue`, `RemoteProjectWizard.vue` | **最小 SSH/SFTP**，有 `RemoteOnly` 边界测试 |
| 设置 | `SettingsView.vue` + 大量 section | 完整但信息架构拥挤 |
| 恢复对话框 | `RecoveryDialog.vue` + `recovery_service.go` | 完整（packaged kill/restart 本会话 U） |

**没有发现**「Coming soon」占位主界面。Computer Use / Goal / 自动更新在 UI 上存在，但后端能力是 stub 或拒绝——这是**诚实降级**，不是空白页。

### 4.3 半成品清单（必须对外诚实）

| 能力 | 现状 | 证据 |
|---|---|---|
| 远程 IDE | 不存在。SSH 连接 + SFTP + 受限命令；无远端 PTY/Git/LSP/DAP/Agent | `remote_service.go:794-859`；`docs/HOST-CLIENT-PROTOCOL.md:3-18` 自报 design draft；`project_remote_boundary_test.go:62` |
| 自动更新安装 | E2：可检查/下载/SHA-256，`ApplyUpdate` 明确拒绝 | `update_service.go:398-403` |
| 崩溃上报 | 本地落盘；`UploadCrash` 只打日志 | `crash_service.go:250-271` |
| Computer Use | **三平台均为 stub**（Windows 也返回 `ErrPlatformUnsupported`） | `computer_use_windows.go:22-46`，`computer_use_unix.go`，`computer_use_service.go:20-22` 注释仍写「Windows 有 gdi32」——注释过时 |
| Goal 自治 | 默认拒绝 prototype executor | `executor_adapters.go:190-244`，`ai_goal_service.go:138-146` |
| Plan 生成 | UI 用空步骤数组创建；注释承认 generator 未接线 | `PlanPanel.vue:53-55`，`ai_plan_service.go:3-5` |
| 语言包 | Go/TS 优先；Python/Rust 等为 PATH 发现 + 部分 I 测试；G23 AC 1/4 | `lsp_service_server.go:135-213`；prompt-9 G23 |
| 符号索引 | 仅 `.go/.ts/.tsx/.js/.jsx` | `symbol_index_service.go:1003-1004` |
| i18n 产品矩阵 | 生产字典仅 en/zh/ja；G25 ICU T 级有，packaged 矩阵 U | `frontend/src/lib/locales/*` |
| 统一远程 Host | G26 AC 0/4 | prompt-9 §8 |
| 发布运营 / SLO | G27 AC 0/4；遥测默认关 | `operational_events_g27.go`（prompt-12 引用） |

---

## 5. AI 功能可用性

### 5.1 可用（✅）

**Provider / Chat（S，真实 provider U）**

- OpenAI Chat Completions 与 Anthropic Messages 双协议，SSE 事件驱动（README 与 `ai_service.go` 一致）。
- BaseURL 规范化，避免 `/v1/v1`（`ai_urlsec.go:57-71`）。
- 非 loopback 强制 HTTPS，禁止 userinfo（`ai_urlsec.go:29-54`）。
- API key：Windows DPAPI + 应用熵（`secrets_windows.go:22-73`）；设置保存加密失败 **fail-closed**（`settings_service.go:536-556`）。
- `LoadSettings` 对前端清空明文的设计仍在（prompt-12 描述，本会话未逐行复读 LoadSettings，标 S/既有）。
- 内联幽灵补全：debounce + 取消，避免过期 ghost text（`inlineCompletion.ts:62,127,139,159`）。

**Agent（S，相对 prompt-12 已升级）**

- 四个历史内置工具 `read/write/run/search` 仍在；前端 `MAX_TOOL_CALLS=20` 只是展示，硬限制在 `agent_budget.go`（`agent.ts:49-61`）。
- **Renderer 禁止自行注册工具**（`agent.ts:636-651`）。动态 MCP/workflow/skill 必须由后端 catalog 发布。
- 执行走 `agentService.executeAgentTool`，绑定 session + catalogRevision + schema（`agent.ts:753-787`）。
- **MCP 已接入 Agent catalog**（prompt-12 §5.2「MCP 断链」**已过时**）：
  - `agent_execution_mcp.go:23-75` `ReplaceSource(SourceMCP, ...)`，`ExecuteKey: "mcp.call"`
  - schema 强制关闭 `additionalProperties`
  - 真实 Windows stdio 测试文件存在：`agent_execution_mcp_process_test.go`（I 子证据，本会话未跑）
- 写文件：`RequestWriteApproval` 绑定路径/hash/size/generation/TTL，走统一事务（`agent_write_approval.go`）。

**代码动作 / Preset（S）**

后端 `builtinPresets` 共 **10** 个（`ai_prompts.go:184-318`）：

`explain`, `refactor`, `fix`, `implement`, `generate_docs`, `generate_tests`, `optimize`, `review`, `security`, `commit_message`。

编辑器右键只挂了 **8** 个（`CodeEditor.vue:248-286`）：explain / refactor / fix / generate_docs / generate_tests / optimize / review / security。

README 写「9 个右键代码动作……提交信息」。**两边都不精确：**

- 右键实际 8 个，缺 `commit_message` 与 `implement`
- `commit_message` 作为 preset 存在，`AIActionName` 含该值（`types/index.ts:414-423`），`runAIAction` 通用（`ai.ts:1079-1099`），但 `GitPanel.vue` **无** `commit_message` / `runAIAction` 命中

所以「提交信息」是 **preset 能力，不是 Git 提交框里一键生成**。

### 5.2 不可用 / 半成品（❌ / ⚠️）

| 功能 | 判定 | 证据 |
|---|---|---|
| Goal 自治循环 | ❌ 默认禁用；Plan 固定句子，Execute 无视计划跑 `go env GOOS`，Evaluate 恒 false | `executor_adapters.go:190-244` |
| Plan AI 生成步骤 | ❌ 无 `plan` 工具；UI `createPlan(id, goal, [])` | `ai_plan_service.go:5`，`PlanPanel.vue:53-55` |
| Computer Use | ❌ 所有平台 `ErrPlatformUnsupported`；默认关闭 | `computer_use_windows.go:22-42` |
| 流式重试 | ⚠️ 非流式 429/5xx 才退避（prompt-12；`ai_retry.go` 本会话未逐行复读，保持 S/既有） | prompt-12 §5.2 |
| AI SSRF 拨号二次校验 | ⚠️ Chat 用普通 `aiTransport`，**没有** `NewSSRFSafeTransport` | `ai_service.go:37-50,57,64` vs `ai_urlsec.go:187-192` |
| 真实 provider E2E | U | 本会话未调用 |

### 5.3 AI 判定

**Chat 模式 + Agent 统一 catalog（含 MCP 源）在架构上是可用的；Plan/Goal/Computer Use 不能当产品功能卖。**  
「自治 Agent 读/写/跑/搜」基本属实；「规划→执行→评估→调整」对默认 Goal **不属实**。README 已把 Goal 改成 prototype 口径（CHANGELOG Unreleased），这一点文档比部分 prompt 旧叙述更诚实。

---

## 6. IDE 功能可用性

### 6.1 本地单机主路径（高质量，S；真实进程 I 视环境）

| 子系统 | 可用性 | 说明 |
|---|---|---|
| 打开项目 / 读树 / 打开文件 / 保存 | 高 | FileService 空 root fail-closed；`os.Root` 能力句柄（`file_service_secure_root.go:12-80`）；20 MiB 读上限 |
| 脏缓冲恢复 | 高（代码） | `RecoveryService` + 启动扫描（prompt-6 P0-03 已宣称 V）；本会话 packaged kill/restart U |
| Git | 很高 | status/stage/commit/rebase/worktree/PR/stash/bisect/submodule 均有服务与面板 |
| 终端 | 高 | ConPTY / Unix pty；CJK fallback 已作为 BUG1 修过 |
| LSP | 中高 | 真实 JSON-RPC 客户端；Go/TS 优先；缺服务器时降级而不是 mock |
| Debug | 中高 | Delve DAP + Node CDP；G14 宣称有真实 adapter 测试 |
| 搜索替换 | 高 | 含 hash 绑定；结构性搜索是行范围而非 AST（prompt-12，仍成立） |
| 扩展 | 中 | Worker ABI 1.0 + 配额 + crash recovery；Open VSX SHA-256；不是 VS Code API 全集 |
| 工具链 | 中高 | go/tsc/eslint/prettier/vitest；Windows `.cmd` 走 `cmd.exe /c` + 元字符转义 |

### 6.2 明确不是的东西

- **不是 Remote-SSH。** `docs/HOST-CLIENT-PROTOCOL.md` 第一句：design draft, not implemented。
- **不是完整 VS Code 市场。** VSIX 是权限门控子集；G13 AC4 需要可激活语料仍 U。
- **不是全语言 IDE。** 符号索引 5 扩展名；Monaco 高亮 ≠ 工程模型。
- **macOS 日用未在本会话验证**（G09/G16 AC 仍 U）。

### 6.3 IDE 判定

作为 **Windows 上 Go/TS 个人开发者的实验性日常编辑器**：主路径足够用，前提是用户接受 0.x、自己装 gopls/tsserver、自己配 AI key。  
作为 **可推荐给团队的生产 IDE**：不成立。

---

## 7. 前端显示 BUG

本会话**没有**启动 WebView，下列为源码级确认或历史已修记录。

### 7.1 确认仍存在

| ID | 严重度 | 现象 | 证据 |
|---|---|---|---|
| **UI-1** | Medium | **已修（P13-G01）：** 页脚与英雄区同源 `__APP_VERSION__`；`WelcomeView.test.ts` | T |
| **UI-2** | Low | **已修（P13-G02）：** 文档按钮改为说明仓库内 `README.md` / `docs/`，不再打开 Wails 官网 | T |
| **UI-3** | Low | **已修（P13-G02）：** 生产强制时开关 disabled + 三语「生产强制开启」；dev 仍可关 | T |
| **UI-4** | Low | **已修（P13-G01）：** README 改为 8 项（解释/重构/修 Bug/文档/测试/优化/审查/安全审计），与 `CodeEditor.vue` `aiActions` 一致；未补 commit/implement 入口 | S |
| **UI-5** | Info | 仅 en/zh/ja 三套字典；G25 承诺的 ru/pl/ar/RTL packaged 切换未完成 | locales 目录只有 3 个文件 |

### 7.2 源码显示已修补（本会话未 GUI 回归）

这些是注释里的历史 BUG，代码侧有对应修复，**不能**再当现行显示故障宣传，除非 GUI 复现：

- TitleBar / StatusBar 窄窗重叠：`TitleBar.vue` / `StatusBar.vue` `BUG3` overflow/min-width 处理
- Outline 卡死：`flattenSymbols` 环路/深度/行数守卫 + 300ms debounce + stale request（`OutlinePanel.vue:80-251`）——对应 prompt-5 线索 11 / prompt-6 P0-08
- FlameGraph `null` children：`FlameGraph.vue` BUG2
- 终端 CJK 字体：`TerminalPanel.vue` BUG1
- AI 窗 frameless 最大化：`AiWindowView.vue` BUG6
- Git 非仓库空态：`GitPanel.vue` BUG2

### 7.3 巨型组件（可维护性，不是像素 BUG）

prompt-12 报过的体量问题方向仍在：`AiChatPanel.vue`、`GitPanel.vue`、`TerminalPanel.vue`、`CodeEditor.vue` 均为千行级 SFC。这会放大后续显示回归成本。

### 7.4 XSS 显示面

`v-html` 在前端源码中被有意识限制；`MarkdownContent.vue` / `MessageList.vue` / `MarketplacePanel.vue` 注释要求 DOMPurify。本会话未做 DOM XSS 动态验证（U）。

---

## 8. 后端 BUG 与正确性缺口

### 8.1 产品行为 BUG / 半实现（用户能感知）

| ID | 严重度 | 问题 | 证据 |
|---|---|---|---|
| **BE-1** | High（诚实性） | **显示面已诚实（P13-G02）：** Goal UI 标明 Prototype、默认不可自治；opt-in 明确无法完成目标。executor 仍是脚手架（G04 再核默认拒绝） | S |
| **BE-2** | High（功能缺口） | **已修（P13-G04）：** 头注释与 Plan UI 改为空步骤 + 生成器未接线；未做假步骤生成 | S/T |
| **BE-3** | Medium | `ThreeWayMerge` 按行下标对齐，自认非 diff3；插入/删除会错位 | `diff_service.go:122-187` |
| **BE-4** | Medium | `ApplyUpdate` 恒失败，自动安装不存在 | `update_service.go:398-403` |
| **BE-5** | Medium | `UploadCrash` 无上报端点，调用成功只是本地保留 | `crash_service.go:250-271` |
| **BE-6** | Medium | **已修（P13-G02）：** 头注释改为三平台 stub；设置页明示 platform unsupported | S/T |
| **BE-7** | Low | **已修（P13-G01）：** README / ARCHITECTURE / bootstrap 头注释均为 47；`check-doc-numbers.mjs` 锁定 47 | S/T |
| **BE-8** | Low | **已修（P13-G01）：** CHANGELOG / E2E 改为「有本地 git（含 `beta0.2.0` tag）；无已验证正式 `v0.2.0` Release」 | S |

### 8.2 安全相关（对照 prompt-12，状态已变化）

| prompt-12 | 本会话复核 | 结论 |
|---|---|---|
| **H1** FileService symlink TOCTOU | FileService 已 `os.Root`。P13-G03 将部分写路径迁到 `ValidateMutatingPathWithinRoot`（settings assets 写/删、snapshot restore、workflow YAML、workspace edit txn、crash delete、recovery restore）。`ValidatePathWithinRoot` **仍返回原始 abs**；git/local_host/window/coverage/mcp/Reveal/CAS/macOS 仍 U | **部分修复，未关闭** |
| **H2** `.cmd/.bat` 注入 | `escapeCmdArg` + 双层 `escapeCmdMeta`，`/v:off`（`exec_cmd.go:54-104`）；prompt-9 写 H2 已用真实 cmd.exe 双路径 I 关闭 | **已变化：按台账已关闭，本会话未重放 payload** |
| **H3** VSIX 仅同源 SHA-256 | 市场仍用 registry SHA-256（`marketplace_service.go:701-722`）；安全模型字段已改名为 `integrityChecked` 而非 Verified（`extension_security_service.go:122,146`） | **风险仍在，宣传口径已收紧** |
| **H4** 插件沙箱可关 | 生产 `import.meta.env.PROD` 强制 sandbox（`pluginRegistry.ts:247-267`） | **生产路径已变化；dev 仍可关** |
| **M3** 加密失败变 `plain:` | `SaveSettings` 加密失败直接 return error（`settings_service.go:536-556`）。`EncryptSecret` 仍**解密** `plain:` 遗留值（`secrets.go:91,128-140`） | **写入路径已 fail-closed；遗留明文读取仍支持** |
| **M6** AI URL 无拨号 SSRF | Chat/stream 使用 `NewAISSRFSafeTransport`（拨号二次校验；允许 loopback，拒绝 metadata/private） | **已变化：Chat 路径已修（P13-G03）** |

未发现仓库内真实 API key / 私钥被跟踪（`git ls-files` 敏感扫描空）。`plain:sk-fallback-key` 仅出现在测试（`secrets_test.go:98`）。

### 8.3 测试与门禁健康（S/U）

- 后端测试资产很大（187 个 `*_test.go`），含 race、真实 LSP/DAP 门控测试。
- 前端 vitest 与源码同目录；prompt-11 曾报 172 files / 2739 tests（历史 T）。
- CI：`ubuntu` contract-smoke **强制**；Windows/macOS `continue-on-error: true`（`.github/workflows/ci.yml:28-41`）。YAML 存在 ≠ 真实 run（R=U）。
- 本会话未重跑全量测试。prompt-9 最新叙述（2026-08-20）称 Windows packaged 24/24 与 backend-gate 9/9，但工作树此后又有大量未提交改动，**那些 SHA 不能代表当前 dirty tree**。

---

## 9. 可开源性

### 9.1 已经具备（适合「实验性开源」）

| 项 | 证据 |
|---|---|
| OSI 许可 MIT | `LICENSE` |
| 第三方清单 | `docs/THIRD_PARTY_LICENSES.md`：Unknown=0，strong-copyleft=0（生成器口径） |
| NOTICE / 发布资产许可 | `NOTICE`，`docs/RELEASE_ASSET_LICENSES.md` |
| 安全政策 | `.github/SECURITY.md`：无假 SLO、私密漏洞通道、email |
| 贡献 / 行为准则 / Issue 模板 | `.github/CONTRIBUTING.md`, `CODE_OF_CONDUCT.md`, ISSUE_TEMPLATE |
| 诚实产品边界 | README V/S/U 表；`internal/repo/documentation_claims_test.go` 禁止若干夸大句 |
| 依赖锁定 | `go.mod` / `go.sum` / `frontend/package-lock.json` |
| 社区联系 | QQ 群、Telegram、邮箱（README） |

### 9.2 尚未具备（开源发布不合格的原因）

| 项 | 证据 |
|---|---|
| 工作树不干净 | 289 porcelain；`internal/agentcore/`、`internal/agentcli/`、大量 `agent_execution_*.go`、`prompt-12.md` 均 **untracked** |
| 无 `v0.2.0` tag | 仅 `beta0.2.0` 指到更旧的 `45f37e7`（MSI 提交），HEAD 是 `18b43cf` |
| 无本会话可核验的 GitHub Release / Actions run | R=U |
| macOS/Linux packaged 矩阵 | G10 AC2 U |
| SBOM/签名/公证全矩阵 | G21 1/4 |
| CHANGELOG 与 E2E 文档过时 | 仍写无 git history |
| npm 供应链 | prompt-9 仍记 G33 `nanoid` high；本会话未跑 audit（U）。lock 里可见 `nanoid@^3.3.16` 依赖边 |
| 根目录本地二进制 / `.agents` / `.claude` | gitignore 已覆盖；发布前仍需确认未误 track |
| Wails v3 **alpha** | 上游 API 可能漂移；`check-wails-pin` 存在是优点，不是稳定承诺 |

### 9.3 开源发布建议口径

**可以：** 以 MIT 实验项目公开源码，README 保持「0.x / 非生产 / 非 VS Code 替代」。  
**不可以：** 打 `v0.2.0` 正式发行、声称四平台已签名、声称独立安全审计、声称完整远程或完整 VSIX。

若要公开：先提交或有意识地丢弃 107 个 untracked 文件（尤其 `prompt-12.md` 与 `internal/agentcore`），跑 `backend-gate` + `frontend:check` + 官方 registry audit，再决定 tag。

---

## 10. 所有 Prompt 内容是否真实实现

> 判定列：**落地** = 代码与测试能证明主路径；**部分** = 有实现但 AC/平台/集成未齐；**未落地** = stub/文档/默认禁用；**过时** = 该 prompt 的现状描述已被后续代码推翻。

### 10.1 prompt-1（长期 SSOT / 安全红线）

| 声明 | 判定 | 说明 |
|---|---|---|
| 诚实、fail-closed、不把 stub 当完成 | **落地（纪律）** | README/SECURITY/多处 prototype 文案 |
| 不宣称 VS Code 替代 / 生产级 / 完整 Computer Use / 完整 Remote / 完整 VSIX | **落地（文档）** | README:35, 红线仍在 |
| Agent 命令强制人工审批、无 Safe 自动批准旁路 | **落地（主路径）** | capability token + 预算 epoch |
| pathsec 双侧 EvalSymlinks | **部分** | 函数存在，但返回原始 abs；FileService 另走 os.Root |
| Computer Use 平台 stub 时默认关 | **落地** | 且 Windows 也是 stub |
| 回写 SSOT / 一次一单元 | **流程要求** | 不是产品功能 |

### 10.2 prompt-4（成熟度审查，2026-07-30）

当时的 P0（无 hot-exit、快照空 root、Remote 伪装 IDE、Goal 脚手架、无 packaged E2E、版本漂移）——

| 当时问题 | 现在 |
|---|---|
| 无 dirty-buffer recovery | **已落地** `recovery_service.go` |
| Plan/Goal/Diff 空 workspace root | **已落地** `WorkspaceContext` |
| Goal 脚手架仍执行 | **部分** 仍是脚手架，但默认 `ErrGoalPrototypeDisabled` |
| Remote 像远端 IDE | **已落地边界** `RemoteOnly` + 拒绝当本地项目打开 |
| 版本 0.2.0 vs 0.3/0.4/0.5 表 | **已收敛** SECURITY 只保留 0.2.x |
| 无 packaged E2E | **部分** 有 Windows 历史 P；跨平台 U |

prompt-4 作为「当时审查」仍然正确；作为「当前缺陷清单」过时。

### 10.3 prompt-5 / 6（下一阶段 Goal）

| Goal | 判定 |
|---|---|
| P0-02 WorkspaceContext | 落地 |
| P0-03 Recovery journal | 落地 |
| P0-04 Goal prototype 网关 | 落地（禁用而非做真） |
| P0-05 VERSION SSOT | 落地；平台元数据 G08 仍缺 macOS AC |
| P0-07 Remote 降级 | 落地 |
| P0-08 Outline 卡死守卫 | 落地（源码） |
| P1-01 精确快照 | 落地（代码+测试名） |
| P1-02 Agent budget epoch | 落地 |
| P1-03 StepInWithTarget | 落地（服务/测试存在） |
| P1-04R 统一 edit transaction | 落地 `workspace_edit_transaction.go` |
| packaged E2E UI driver | 部分（Windows 历史 P） |
| Language Pack 完整矩阵 | 未完成（G23） |
| Unified Remote Host | 未完成（G26） |

### 10.4 prompt-7 / 8（一次性修复清单）

G-01 空 root fail-closed、G-02 Agent 写审批、G-03 原子保存、HTTP 私网令牌、release 元数据 SSOT 等，后续 prompt-9 把其中多项标完成。  
**不要**直接相信 prompt-8 的「已完成」——prompt-9 正是为纠偏而写。以 prompt-9 进度板 + 当前代码为准。

### 10.5 prompt-9 / 10 / 11（G01–G27）

按 prompt-9 §8 进度板（2026-08 台账，S）：

**完成（代码+宣称证据）：** G02, G03, G04, G05, G06, G11, G12, G14, G15, G17, G18, G20, G22, G24。

**阻塞（外部 U，不是没写代码）：** G01, G07, G08, G09, G10, G13, G19, G21, G23。

**进行中：** G16（macOS 信号）、G25（i18n packaged）、G26（远程 Host）、G27（发布运营）、**P12-G33**（Agent 核心，AC 0/6）。

本会话抽查与进度板**相符**的实现：G03 空 root、G24 Worker ABI 文件、G20 SHA-256 拒绝无 digest、G04 recovery 接线、G06 runtime role 前端全局。

本会话**不能**把历史 packaged SHA 升级为当前 dirty tree 的 P。

### 10.6 prompt-12（审查 + G28–G33）

| prompt-12 结论 | 复审 |
|---|---|
| 47 个服务 | 仍正确 |
| MCP 无法被 agent 调用 | **过时**（`agent_execution_mcp.go`） |
| H2 cmd 注入 | **过时（按台账已关）** |
| H4 沙箱可关 | **生产路径过时** |
| M3 加密降级 plain | **写入路径过时** |
| Computer Use 仅 Unix stub、Windows 原生 | **过时：Windows 也是 stub** |
| WelcomeView `v0.1.0` | **仍正确** |
| Goal prototype / 无 plan 工具 | **仍正确** |
| ThreeWayMerge 简化 | **仍正确** |
| 无商业化/授权服务器 | **仍正确** |
| G28–G32 | **未开始主体**（计费 Dashboard、真 plan 工具、diff3、diff-first、内置 skill 库） |
| G33 | **进行中，AC 0/6**；大量实现位于 **untracked** `internal/agentcore` 与 `agent_execution_*.go` |

### 10.7 文档自称「未实现」的协议

这些 **不是实现失败**，而是明确的设计稿：

- `docs/HOST-CLIENT-PROTOCOL.md`：「Design draft, not implemented.」
- `docs/LANGUAGE-PACK-SDK.md`：「Partial implementation, not a completed Goal.」
- `docs/EXTENSION-CONTRIBUTION-PROTOCOL.md`：版本化贡献协议（G24 有 runtime 子集，不是全文）

README 文档表把前两者标为「协议设计草案（未实现）」——与 HOST-CLIENT 一致；Language Pack 实际已有部分 runtime，README 这句话**略偏保守**，ARCHITECTURE.md 更准。

### 10.8 Prompt 覆盖率总表

| Prompt | 角色 | 主路径落地？ | 最大未完成 |
|---|---|---|---|
| 1 | 红线/纪律 | 是 | 不是功能清单 |
| 2 / 3 | 被 1 引用 | **文件不存在** | 仅历史线索 |
| 4 | 审查 | 当时正确 | 缺陷多已修 |
| 5 / 6 | P0/P1 | 大部分是 | 跨平台 P、语言包、远程 Host |
| 7 / 8 | 修复清单 | 大部分被 9 纠偏后落地 | 以 9 为准 |
| 9 | G01–G27 | 14 完成 / 9 阻塞 / 4 进行中 | CI/macOS/发布/G25-27 |
| 10 | G24 交接 | G24 代码在 | 证据绑定旧 SHA |
| 11 | 未完成索引 | 与 9 一致 | 29 AC 未勾选（历史计数） |
| 12 | 审查+G28-33 | 审查部分需修正；G33 进行中 | G28-32 未做；G33 AC 0/6 |
| 13 | 本文 | — | — |

---

## 11. 交叉核对：prompt-12 必须修正的条目

写给后续 Agent，避免重复错误结论：

1. **不要再说 MCP 与 Agent 断链。** 后端 `SourceMCP` + `mcp.call` handler 已存在；前端改为后端 catalog 投影。
2. **不要再说 Computer Use Windows 已实现。** `computer_use_windows.go` 全部 `ErrPlatformUnsupported`。
3. **不要把 H2/H4/M3 原样当现行 Critical/High。** 先读 `exec_cmd.go`、`pluginRegistry.ts`、`settings_service.go` 加密分支。
4. **H1 不要标已关闭。** `os.Root` 是实质进展；`ValidatePathWithinRoot` 返回值与 macOS/Reveal/CAS 仍是台账缺口。
5. **不要用 2026-08-14/17 的 packaged SHA 描述当前工作区。** 当前相对 `18b43cf` 有 289 条未提交变更，且 G33 核心文件大量 untracked。
6. **服务数写 47**，不要抄 README 的 46。
7. **Welcome 页脚 `v0.1.0` 仍是活 BUG。**

---

## 12. 综合评分（1–5，相对「可日常使用的开源桌面 IDE」而非相对空仓库）

| 维度 | 分 | 一句话 |
|---|---|---|
| 后端功能完整性 | 4.0 | 本地面极宽；远程/更新/CU/Goal 故意或实际半成品 |
| 前端功能完整性 | 3.7 | 无桩、双窗、面板齐全；入口与文档数字有毛边 |
| 前端显示质量 | 3.3 | 有主题/i18n/窄窗修复；版本号 BUG + 未 GUI 验证 |
| 后端正确性/安全 | 3.2 | fail-closed 意识强；H1 残留、AI SSRF、简化 merge |
| AI 可用性 | 3.4 | Chat+Agent+MCP catalog 强；Plan/Goal/CU 弱 |
| IDE 可用性 | 3.6 | Windows Go/TS 可日常；跨平台/LSP 真机 U |
| 可开源性 | 3.0 | MIT+治理够实验开源；发布与工作树卫生不够 |
| Prompt 兑现 | 3.5 | P0 安全/恢复兑现好；P2/G28+ 与发布类未兑现 |
| **总分（加权主观）** | **3.5** | 优秀 0.x 原型，不是 1.0 |

---

## 13. 优先行动（只排序，不在本审查会话实施）

1. **卫生：** 决定 `internal/agentcore` 与 prompt-12 等 107 个 untracked 是提交还是清理；删掉根目录本地 `gugacode`/`koyori-ide` 二进制后再谈 release。
2. **诚实显示：** 删掉 `WelcomeView.vue:123` 的 `v0.1.0`；文档按钮改指向项目文档；README 46→47；右键动作文案改为 8 或补上 commit/implement。
3. **安全收口：** H1 剩余调用方迁到 Root/openat；AI HTTP 复用 `NewSSRFSafeTransport`；生产设置里隐藏无效的「关闭沙箱」。
4. **AI 产品：** 要么做真 `plan` 工具 + LLM Goal executor，要么在 UI 隐藏 Goal 运行按钮；Computer Use 在三平台 stub 时不要在设置里看起来能开。
5. **发布：** 在干净树上重跑 backend-gate / frontend:check / npm-audit-gate / Windows packaged；没有 macOS/Linux/CI run 之前继续禁止「开源发布合格」。

---

## 14. 本审查未覆盖

- 未启动 `wails3 dev` / 未截图，故像素级布局、主题切换、拖拽分栏、IME、高 DPI 均为 U
- 未调用真实模型，故流式卡顿、tool-call 解析在真模型下的成功率 U
- 未在 macOS/Linux 构建
- 未验证 GitHub Actions 实际 run
- 未做依赖 CVE 全量审计（仅记录 lock 与历史 nanoid 线索）
- 未对 `AiChatPanel.vue` 近三千行做逐函数审查

---

## 15. 给后续会话的一句话入口

当前仓库是 **MIT 许可的 0.x 离线优先桌面 AI IDE**。本地编辑、Git、终端、Chat、带审批的 Agent（含后端 MCP catalog）是真的；Goal/Plan 生成、Computer Use、远程 IDE、自动更新安装、崩溃上报、跨平台发布不是。prompt-9/11/12 的未完成项以 **P12-G33 AC 0/6** 为现行 Goal，**不要**在脏工作树上把历史 24/24 packaged 当作当前证据。

---

## 16. 一键启动词（把下面整块复制给后续 AI）

> 角色：本文 §13 的优先行动是**审查后的修复队列**，不是再做一遍审查。  
> 与 G33 的关系：P12-G33 仍是 prompt-12 的现行长期 Goal（AC 0/6）。本文件的 P13-G01~G05 是审查收口项，**一次只做一个**；未完成 P13-G01/G02 前不要把脏树 packaged 24/24 当当前证据，也不要并行开工 G28~G32。

### 16.1 可直接粘贴的启动提示词

```text
严格按 docs/prompts/prompt-13.md 执行 Goal 任务，并继承 docs/prompts/prompt-9.md §0 与 prompt-11 §0 纪律。

你是 Koyori IDE 的产品工程 Agent。prompt-13 是 2026-08-21 的独立复审 SSOT：先读代码和命令结果，再接受其中的缺陷表。不要重做审查，不要再写一份 prompt-14 除非我要求。

事实优先级：当前代码与本机命令 > prompt-13 > prompt-12 > prompt-9/11 进度板叙述。

产品红线（违反即失败）：
- 不宣称生产级 / 企业就绪 / VS Code·Cursor·IntelliJ 替代品
- 不宣称开源发布合格、完整 Remote-SSH、完整 VSIX、全语言、自治 Goal、Computer Use 已实现
- 不把 stub / prototype / dry-run / YAML / 历史 packaged SHA 升级为当前 V/I/P/R
- 服务数写 47，不要抄 README 的 46
- 不要再说 MCP 与 Agent 断链；不要再说 Computer Use Windows 已实现；不要把 H2/H4/M3 原样当现行 Critical
- H1 不得标已关闭

工作方式：
1. 开始前：git status -sb、HEAD、脏文件规模；打开本会话目标相关源码与测试；写明缺口「仍存在 / 已变化 / 已不存在」。
2. 一次只做一个 Goal。用户未指定时按下面顺序取第一个未完成项。
3. 最小正确改动。不升 major，不重构无关模块，不弱化审批，不删测试保绿，不提交 secret。
4. 安全默认 fail-closed。renderer 传入的 approved/safe/路径/root 不是授权。
5. 绑定必须用仓库锁定的 Wails v3.0.0-alpha2.111 生成（本机 WAILS3_BIN 优先，禁止 @latest）。
6. 证据分级固定 S/T/I/P/R/U（及本机命令 V）。mock/contract 不得冒充 I/P/R。
7. 不要 commit / push / tag / release，除非我明确要求。
8. 完成后按 prompt-9 §9 模板交付，并立即回写 prompt-13 进度（若你改了代码，同步受影响的 prompt-9 §8 / prompt-12 §13 一句事实，不要凭记忆改 SHA）。

Goal 选择算法（用户未指定时）：
P13-G01 仓库卫生与诚实文档（untracked 分类、Welcome v0.1.0、README 46→47、右键动作文案、CHANGELOG/E2E「无 git history」过时句）
→ P13-G02 显示与设置诚实（文档按钮、生产构建隐藏无效「关闭插件沙箱」、Computer Use/Goal 在 stub 时不要看起来能开）
→ P13-G03 安全收口（H1 剩余调用方；AI HTTP 复用 NewSSRFSafeTransport；生产沙箱文案与开关一致）
→ P13-G04 AI 产品边界（无真 plan 工具则保持空计划+诚实 UI；Goal 默认禁用保留；不要做假自治循环）
→ P13-G05 干净树验证（仅在 G01 卫生完成后：backend-gate、frontend:check、npm-audit-gate、Windows packaged；跨平台/CI 保持 U）

禁止：并行推进 P12-G33 与 P13-Gx 的主体实现。若必须动 Agent 核心，先停下来问我选 G33 还是 P13。
P12-G28~G32（计费、真 plan 工具、diff3、diff-first、内置 skill）本轮不得开工。

当前会话只做：【自动选下一未完成最高优先级 / 或我指定的 P13-G0x】。
开始。
```

### 16.2 若用户指定单个 Goal，用这一句替换最后两行

```text
当前会话只做 P13-G0X：<一句话范围>。做完即停，回写 prompt-13。
```

### 16.3 长任务全量收口启动词（一次处理完所有 P13-G / P 级证据 / BUG / AC）

> 与 §16.1 的区别：§16.1 是「一会话一 Goal、做完即停」。本节是**跨会话长任务**：内部仍一次只改一个 Goal 的主体，但 **自动继续下一个未完成项**，直到下面进度板可关闭项全部勾选，或只剩必须保持 `U` 的外部证据。  
> **不要**把 P12-G28~G32、完整 diff3、真 LLM Goal executor、完整 Remote Host、四平台签名发布算进本长任务——那些不是 prompt-13 的收口范围。

正式 AC 与进度板见 **§17**。把下面整块复制给后续 AI 即可启动长任务。

```text
严格按 docs/prompts/prompt-13.md §16.3 / §17 执行长任务收口，并继承 docs/prompts/prompt-9.md §0 与 prompt-11 §0 纪律。

你是 Koyori IDE 的产品工程 Agent。这是跨会话长任务：把 prompt-13 列出的全部 P13-G01~G05、全部仍存在的 UI/BE BUG、以及 §17 每条 AC 做到可关闭；不要重做审查，不要另写 prompt-14（除非我要求），不要把范围扩到 P12-G28~G32 / 真 LLM Goal executor / 完整 Remote Host / 四平台签名发布。

事实优先级：当前代码与本机命令 > prompt-13 §17 进度板 > prompt-13 正文 > prompt-12 > prompt-9/11 叙述。

════════════════════════════════
产品红线（违反即失败）
════════════════════════════════
- 不宣称生产级 / 企业就绪 / VS Code·Cursor·IntelliJ 替代品
- 不宣称开源发布合格、完整 Remote-SSH、完整 VSIX、全语言、自治 Goal、Computer Use 已实现
- 不把 stub / prototype / dry-run / YAML / 历史 packaged SHA 升级为当前 V/I/P/R
- 服务数写 47，不要抄旧 README 的 46
- 不要再说 MCP 与 Agent 断链；不要再说 Computer Use Windows 已实现
- 不要把 H2/H4/M3 原样当现行 Critical；H1 不得标已关闭（可推进剩余调用方，AC 未全绿就保持「部分修复」）
- UI-5（ru/pl/ar/RTL packaged i18n）属于 P9-G25，本长任务只要求文档诚实，不实现 G25
- BE-3 ThreeWayMerge/diff3 属于 P12-G30，本长任务只要求注释/UI/文档诚实，不换算法
- 不要 commit / push / tag / release，除非我明确要求

════════════════════════════════
工作循环（每个 Goal 内部，然后自动下一 Goal）
════════════════════════════════
1. 读 prompt-13 §17 进度板，锁定当前第一个未完成 Goal。
2. git status -sb、HEAD、脏文件规模；打开相关源码与测试。写明缺口「仍存在 / 已变化 / 已不存在」。
3. 先补失败测试（红灯），再最小正确改动。不升 major，不重构无关模块，不弱化审批，不删测试保绿，不提交 secret。
4. 安全默认 fail-closed。renderer 的 approved/safe/路径/root 不是授权。
5. 绑定用仓库锁定 Wails v3.0.0-alpha2.111（本机 WAILS3_BIN 优先，禁止 @latest）。PowerShell 禁用 npm.ps1 时用 npm.cmd。
6. 证据分级 S/T/I/P/R/U + 本机 V。mock/contract 不得冒充 I/P/R。
7. 当前 Goal 的 AC 全绿（或只剩必须 U 的外部项并已如实标注）后：按 prompt-9 §9 模板写交付，立即回写 §17 进度板与受影响的 prompt-9 §8 / prompt-12 一句事实（禁止凭记忆改 SHA）。
8. 自动开始下一个未完成 Goal。全部可关闭 AC 勾选完毕后停止，输出总交付。不要问「要不要继续」。

会话被打断时：从 §17 第一个未勾选 AC 接着干，不要重做已勾选项。

════════════════════════════════
必须清掉的 BUG（与 Goal 绑定，不得遗漏）
════════════════════════════════
P13-G01：
- UI-1 WelcomeView 页脚硬编码 v0.1.0 → 与 __APP_VERSION__ 同一来源
- UI-4 README「9 个右键含提交信息」与编辑器 8 项不一致 → 改文案或补入口（二选一，须与代码一致）
- BE-7 README / docs/ARCHITECTURE.md 服务数 46 → 47；检查文档门禁是否应锁定该数字
- BE-8 CHANGELOG.md 与 docs/E2E.md「无 git history / 无 Git metadata」→ 改为「有本地 git；无已验证 v0.2.0 正式 release」
- 卫生：列出 untracked（含 prompt-12.md、internal/agentcore、agent_execution_*）分类建议（保留实现 / 应入库 / 应 gitignore）；不要擅自 git add 全体；不要删除用户本地二进制 gugacode/koyori-ide，可确认已被 gitignore
- 补 Welcome 版本号回归测试（T）

P13-G02：
- UI-2 欢迎页「文档」不要打开 https://v3.wails.io/；改为仓库内文档说明或 README/docs 入口（桌面无浏览器文档站时：打开项目 README 路径说明 / 禁用外链并改文案，须诚实）
- UI-3 生产构建强制沙箱时，设置开关不可假装能关闭：生产禁用开关 + 三语文案说明「生产强制开启」；dev 可关
- BE-1 / BE-6 UI：Goal 保持 prototype 默认禁用；Computer Use 三平台 stub 时设置页必须明示「原生操作未实现 / 将返回 platform unsupported」，启用仍要警告；修正 computer_use_service.go 过时注释（Windows 也是 stub）
- 同步 en/zh/ja，禁止缺失 key 显示 raw key

P13-G03：
- M6：AI Chat/stream HTTP 复用 NewSSRFSafeTransport（或等价拨号二次校验），补绕过失败测试；loopback http 给 Ollama/LM Studio 仍须允许
- H4 生产路径：与 G02 开关一致；测试证明 PROD 下 setSandboxMode(false) 无效
- H1：盘点仍走 pathsec.ValidatePathWithinRoot 返回原始 abs 的可变调用方；能迁到 os.Root / ValidateMutatingPath 的迁；不能关闭的平台（macOS/Reveal/CAS/公共 workflow pathname）保持 U，进度板写「部分修复」不得写完成
- 不把 H3 VSIX 同源 SHA-256 扩成完整签名（超出本长任务）；若改 UI，只允许 integrityChecked 口径

P13-G04：
- BE-2：ai_plan_service.go 头注释不得声称存在 plan 工具；Plan UI 保持空步骤 + 明确「生成器未接线 / 需 replan 或手填」；不要做假步骤生成
- Goal：保留 ErrGoalPrototypeDisabled 默认拒绝；不要接假自治循环；UI 已有 prototype 告示则核对其与 executor 文案一致
- 不实现真 LLM executor、不实现 plan 工具（那是 G29）

P13-G05（验证，不扩功能）：
- 在 G01–G04 代码改完后跑：check-doc-numbers、check-doc-links、check-bindings（锁定 WAILS3_BIN）、受影响 Go 测试、受影响 Vitest、eslint/vue-tsc 切片
- 能跑则跑 node scripts/backend-gate.mjs 与 task frontend:check；失败保留红灯
- npm-audit-gate：记录 exit 与 advisory；不得为保绿改 lock 或删门禁；nanoid high 保持 U/红
- packaged-e2e：仅当工作树对该次运行是可指纹绑定的；Windows 本机 P 可追求，macOS/Linux/CI 保持 U
- 禁止用旧 24/24 SHA 冒充本长任务结果

════════════════════════════════
停止条件（满足其一即可结束长任务）
════════════════════════════════
A. §17 进度板上所有「本长任务可关闭」AC 已勾选，仅剩标注 U 的外部项；或
B. 同一 Goal 连续 3 次真实失败且已记录红灯/回滚，将该 Goal 标阻塞并继续下一个可做项；全部可做项结束后停止。

总交付必须包含：改动文件、每条 AC 的 S/T/I/P/R/U、命令与退出码、仍为 U 的清单、明确「未 commit」。

现在开始：按 §17 顺序取第一个未完成 Goal，执行工作循环，直到停止条件。
```

---

## 17. P13 长任务正式 AC 与进度板（本长任务的验收 SSOT）

> 勾选规则与 prompt-9 相同：AC 未全绿不得写「完成」。`U` 项保持未勾选。历史 packaged SHA 不能替当前树。  
> **本长任务范围 = P13-G01~G05 + 下表 BUG。** 表外的 G25 i18n 矩阵、G30 diff3、G26 Remote Host、G27 发布签名、G33 Agent 核心 **不是**本任务关闭条件。

状态枚举：`未开始` / `进行中` / `阻塞` / `完成`。

### P13-G01 仓库卫生与诚实文档

| # | AC | 最低证据 | 关闭时勾选 |
|---|---|---|---|
| G01-AC1 | Welcome 页脚与英雄区版本同源 `__APP_VERSION__`，无硬编码 `v0.1.0`；有回归测试 | T | [x] |
| G01-AC2 | README 架构/结构处服务数为 47，与 `bootstrap_services.go` 的 `application.NewService` 次数一致；ARCHITECTURE.md 同步 | S/T | [x] |
| G01-AC3 | README 右键 AI 动作条数/名单与 `CodeEditor.vue` 实际挂载一致（8 项则写 8；若补 commit/implement 入口则代码+文案一起变） | S/T | [x] |
| G01-AC4 | CHANGELOG.md 与 docs/E2E.md 不再声称「本 checkout 无 git history」；改为有本地提交/tag、无已验证正式 Release 的诚实句。不把 `beta0.2.0` 写成 `v0.2.0` 正式版 | S | [x] |
| G01-AC5 | 产出 untracked 分类表（路径 → 保留实现应入库 / 本地产物应 ignore / 审查文档应入库）。不擅自 `git add -A`，不删除用户本地二进制 | S | [x] |

**BUG 关闭映射：** UI-1、UI-4、BE-7、BE-8。

### P13-G02 显示与设置诚实

| # | AC | 最低证据 | 关闭时勾选 |
|---|---|---|---|
| G02-AC1 | 欢迎页文档入口不再打开 Wails 官网冒充项目文档；文案与行为一致 | T | [x] |
| G02-AC2 | 生产构建下插件沙箱开关不能关闭沙箱；设置 UI 禁用或隐藏该开关并说明「生产强制」；en/zh/ja 同步；有测试证明 PROD 强制 | T | [x] |
| G02-AC3 | Computer Use 设置/注释明示三平台原生操作为 unsupported stub；Windows 源注释不再写已实现 gdi32 | S/T | [x] |
| G02-AC4 | Goal UI 与默认 executor 一致：prototype + 默认不可自治运行；opt-in 文案不含「已能完成真实编码目标」 | S/T | [x] |

**BUG 关闭映射：** UI-2、UI-3、BE-1（诚实性）、BE-6。

### P13-G03 安全收口

| # | AC | 最低证据 | 关闭时勾选 |
|---|---|---|---|
| G03-AC1 | AI provider HTTP（至少 Send/Stream/非流式补全）使用 `NewSSRFSafeTransport` 或等价拨号期 SSRF 防护；补失败绕过测试；loopback http 本地模型仍允许 | T | [x] |
| G03-AC2 | 生产 renderer 无法通过设置关闭插件沙箱（与 G02-AC2 交叉）；非生产测试路径可关 | T | [x] |
| G03-AC3 | 盘点并记录仍用 `ValidatePathWithinRoot` 返回原始 abs 的写路径；可迁调用方已迁到 Root/mutating helper；剩余平台缺口列为 U，Goal 状态最多「部分修复」 | S/T/U | [x] |
| G03-AC4 | 不回归 H2（cmd 转义测试仍绿）；不把 VSIX 标成已签名 | T | [x] |

**BUG 关闭映射：** M6、H4 生产面、H1 部分。

### P13-G04 AI 产品边界（不做假闭环）

| # | AC | 最低证据 | 关闭时勾选 |
|---|---|---|---|
| G04-AC1 | `ai_plan_service.go` 及 Plan UI 不再声称存在 `plan` 工具；空计划 + 未接线说明 | S/T | [x] |
| G04-AC2 | 默认 Goal executor 仍 `PrototypeExecutor` 且未 opt-in 时 `RunGoal` 拒绝；测试保持红灯绕过失败 | T | [x] |
| G04-AC3 | 不新增假步骤生成、不默认打开 Computer Use、不把 MCP 断链写回文档 | S | [x] |

**BUG 关闭映射：** BE-2；巩固 BE-1。

### P13-G05 验证门禁（P 级只承认本任务新跑的）

| # | AC | 最低证据 | 关闭时勾选 |
|---|---|---|---|
| G05-AC1 | `node scripts/check-doc-numbers.mjs` 与 `check-doc-links.mjs` exit 0（若服务数被纳入数字门禁则一并绿） | V | [x] |
| G05-AC2 | 锁定 WAILS3_BIN 后 bindings check exit 0 | V | [x] |
| G05-AC3 | 受影响包 `go test` 与相关 Vitest/eslint/vue-tsc 切片 exit 0；能跑则 backend-gate 9/9、frontend:check 全绿。首次红灯必须保留 | V/T | [ ] |
| G05-AC4 | `npm-audit-gate` 结果落账；high 未清零则 AC 保持未勾选并标 U，不得删门禁保绿 | V/U | [ ] |
| G05-AC5 | 若跑 packaged-e2e：记录本任务的 artifact/source fingerprint 与 exit；失败保留。macOS/Linux/CI 保持 U。禁止复用 prompt-12 旧 SHA 当本任务 P | P/U | [ ] |

**本 Goal 不是「开源发布合格」。** G05-AC4/AC5 允许因外部/供应链保持 U，长任务仍可在注明后结束。

### 明确不在本长任务关闭范围（保持 U / 他号 Goal）

| 项 | 归属 | 本任务做法 |
|---|---|---|
| UI-5 ru/pl/ar/RTL packaged i18n | P9-G25 | 文档承认未做 |
| BE-3 真 diff3 | P12-G30 | 仅诚实注释 |
| BE-4/BE-5 自动安装与崩溃上报端点 | 产品 E2 边界 | 保持拒绝/本地保留，文案已诚实则不动代码 |
| H1 macOS/Reveal/CAS 全关 | 安全长期 | G03-AC3 部分修复 |
| H3 发布者签名 | G20/G21 | 不扩范围 |
| G26 Remote Host / G27 发布运营 / G33 AC | prompt-12 | 禁止并行主体 |
| 四平台签名 Release | R | 禁止宣称 |

### 进度板

| Goal | 状态 | 已满足 AC | 证据 | 阻塞/下一步 |
|---|---|---|---|---|
| P13-G01 | 完成 | 5/5 | S/T | 见文末 G01 交付；未 commit |
| P13-G02 | 完成 | 4/4 | S/T | 见文末 G02 交付；未 commit |
| P13-G03 | 部分修复 | 4/4 | S/T/U | H1 未关闭（macOS/Reveal/CAS/U）；见 G03 交付 |
| P13-G04 | 完成 | 3/3 | S/T | 见文末 G04 交付；未做真 executor |
| P13-G05 | 阻塞 | 2/5 | V/T/U | AC3 frontend:check 连续 3 次红。AC4 nanoid U。AC5：脏树已绑进 harness，完整 packaged 因 `wails3 build` 会打崩本机 DSH web 而停跑，保持 U |

**长任务完成定义：** G01–G04 可关闭 AC 全勾选；G05-AC1~AC3 为 V；G05-AC4/AC5 勾选或诚实 U；§17 范围外项未被假装完成。

### 会话交付：P13-G01

- 复核结论：缺口**已不存在**（UI-1/UI-4/BE-7/BE-8）。页脚不再硬编码 `v0.1.0`；README 服务数 47、右键 8 项；CHANGELOG/E2E 不再声称无 git history。未 git add / 未删除本地二进制。
- 本次状态：未开始 -> **完成**（5/5）
- 改动文件：
  - `frontend/src/views/WelcomeView.vue` — 页脚改用 `appVersion`（`__APP_VERSION__`）
  - `frontend/src/views/WelcomeView.test.ts` — 英雄区/页脚同源回归（T）
  - `README.md` — 47 服务 + 8 个右键动作
  - `docs/ARCHITECTURE.md` — 47 Go services
  - `bootstrap_services.go` — 头注释 46→47
  - `scripts/check-doc-numbers.mjs` — 锁定 `application.NewService` 次数 = 47
  - `docs/CHANGELOG.md` / `docs/E2E.md` — 有本地 git；无已验证正式 `v0.2.0` Release
- AC：
  - G01-AC1 [x] T — `npm.cmd run test -- --run src/views/WelcomeView.test.ts src/appVersion.test.ts` exit 0（3 tests）
  - G01-AC2 [x] S/T — `node scripts/check-doc-numbers.mjs` exit 0（`services=47`）
  - G01-AC3 [x] S — README 8 项与 `CodeEditor.vue` `aiActions` 一致（explain/refactor/fix/generate_docs/generate_tests/optimize/review/security）
  - G01-AC4 [x] S — CHANGELOG/E2E 诚实句；本地 tag 仍是 `beta0.2.0`，未写成正式 `v0.2.0`
  - G01-AC5 [x] S — 分类表如下；未 `git add -A`；`/gugacode` 与 `/koyori-ide` 已在 `.gitignore`
- 验证：Welcome Vitest exit 0；check-doc-numbers exit 0。未跑 packaged-e2e（留给 G05）。
- 首次失败：无。
- 安全与数据：无授权面变化；文档门禁新增服务数锁定，fail-closed。
- 未验证：GUI 肉眼回归 U；正式 GitHub Release U。
- 下一步：P13-G02。
- 未 commit。

### 会话交付：P13-G02

- 复核结论：UI-2 **已不存在**；UI-3 **已变化/已修**（生产开关 disabled）；BE-6 头注释 **已不存在**；BE-1 显示面诚实（默认 prototype，opt-in 不声称能完成真实目标）。未实现真 Computer Use / 真 Goal executor。
- 本次状态：未开始 -> **完成**（4/4）
- 改动文件：
  - `frontend/src/views/WelcomeView.vue` — 文档按钮改为本地路径说明，不再 `window.open` Wails 官网
  - `frontend/src/views/WelcomeView.test.ts` — 断言不打开外链
  - `frontend/src/components/settings/GeneralSection.vue` — 生产禁用沙箱开关
  - `frontend/src/lib/pluginRegistry.ts` — 导出 `isProductionSandboxRequired`
  - `frontend/src/lib/locales/{en,zh,ja}.ts` — `pluginSandboxForcedHint` / `welcome.docsLocalPath`
  - `frontend/src/components/settings/GeneralSection.test.ts` — 生产强制文案 + disabled
  - `services/computer_use_service.go` — 头注释改为三平台 stub
  - `services/computer_use_service_test.go` — 头注释回归
- AC：
  - G02-AC1 [x] T — Welcome docs 按钮走 `welcome.docsLocalPath`，不打开 `v3.wails.io`
  - G02-AC2 [x] T — `setSandboxMode(false, {production:true})` 仍开启；设置开关 disabled + 三语强制文案
  - G02-AC3 [x] S/T — 设置页已有 experimentalNotice；头注释不再声称 Windows gdi32 已实现
  - G02-AC4 [x] S — Goal prototype 告示 / opt-in 警告不含「已能完成真实编码目标」
- 验证：Welcome+GeneralSection Vitest exit 0；pluginRegistry 104 tests 绿；`go test -run TestComputerUseServiceHeaderDoesNotClaimWindowsNativeImpl` exit 0；i18n.test.ts 36 tests 绿（en/zh/ja key 同步）。
- 首次失败：GeneralSection 找 `ElSwitch` 失败，改为断言 `el-switch[disabled]`。
- 安全：生产沙箱 fail-closed 未弱化；Computer Use 仍默认关。
- 未验证：GUI 肉眼 U。
- 下一步：P13-G03。
- 未 commit。

#### G01-AC5 untracked 分类（建议，未执行 git add）

| 路径 | 建议 |
|---|---|
| `docs/prompts/prompt-12.md`, `docs/prompts/prompt-13.md` | **审查文档应入库** |
| `internal/agentcore/**`, `internal/agentcli/**` | **保留实现，应入库**（G33 主体，本长任务不并行实现） |
| `services/agent_execution_*`, `services/agent_lifecycle*`, `services/agent_headless*`, `services/agent_state_root*`, `services/ai_agent.go`, 相关 `*_test.go` | **保留实现，应入库** |
| `services/file_service_secure_root*.go`, `services/git_{advanced,command,diff,history,repository}.go`, `services/mcp_{client,command,config,transport}*.go`, `services/workflow_secure_loader.go`, `services/settings_ai_provider.go`, 相关测试 | **保留实现，应入库**（拆分/加固源，已有 tracked 调用方） |
| `server_bind_guard*.go`, `server_transport_guard*.go` | **保留实现，应入库** |
| `frontend/src/views/WelcomeView.test.ts` | **应入库**（本 Goal 回归） |
| `frontend/src/components/ai-assistant/AgentExecutionTimeline*`, `AgentToolCalls*`, `stores/agentTimeline*`, `e2e/agentToolRoundProbe*`, `e2e/conversationHandoffProbe*`, `e2e/extensionHostG24Recovery*`, `stores/skills.test.ts`, `lib/extensionIntegrityCopy.test.ts`, `assets/styles/main.test.ts` | **保留实现 / 测试，应入库** |
| `build/docker/SERVER.md`, `build/docker/server-gateway/**` | **保留实现，应入库**（实验性 server gateway；非完整 Remote Host） |
| `internal/e2e/extension_host_g24_test.go` | **应入库** |
| 根目录 `gugacode` / `koyori-ide` 二进制 | **本地产物，已 gitignore**；不要删除用户副本 |

### 会话交付：P13-G03

- 复核结论：M6 **已变化/已修**（Chat/stream 拨号二次校验）；H4 生产面与 G02 一致；H1 **部分修复，未关闭**。H3 未扩成签名。
- 本次状态：未开始 -> **部分修复**（AC 4/4 可关闭，H1 本身不得标完成）
- 改动文件：
  - `services/ai_urlsec.go` — `NewAISSRFSafeTransport`（允许 loopback，拒绝 metadata/private）
  - `services/ai_service.go` — `aiTransport = NewAISSRFSafeTransport()`
  - `services/ai_urlsec_test.go` — 绕过失败 + loopback 允许
  - `services/settings_service.go` / `snapshot_service.go` / `workflow_service.go` / `workspace_edit_transaction.go` / `crash_service.go` / `recovery_service.go` — 写路径改 `ValidateMutatingPathWithinRoot`
  - `services/pathsec_test.go` — 仍返回原始 abs + 盘点
- AC：
  - G03-AC1 [x] T — metadata dial 失败；`127.0.0.1` / `localhost` 允许
  - G03-AC2 [x] T — `setSandboxMode(false,{production:true})` 仍开启（G02）
  - G03-AC3 [x] S/T/U — 可迁写路径已迁；剩余 git/local_host/window/coverage/mcp/FileService abs + macOS/Reveal/CAS/公共 workflow pathname **U**
  - G03-AC4 [x] T — `TestEscapeCmdArgRoundTrip` 绿；未改 VSIX 签名口径
- 验证：上述 Go -run 集合 exit 0；pluginRegistry 生产强制测试绿。
- 未验证：macOS/Reveal/CAS I；真实 DNS rebinding I。
- 下一步：P13-G04。
- 未 commit。

### 会话交付：P13-G04

- 复核结论：BE-2 **已不存在**（头注释/UI 不再声称 plan 工具）。Goal 默认仍 `ErrGoalPrototypeDisabled`。未做假自治循环、未接真 LLM executor。
- 本次状态：未开始 -> **完成**（3/3）
- 改动文件：
  - `services/ai_plan_service.go` — 头注释改为生成器未接线
  - `services/ai_plan_service_test.go` — 头注释 + 空步骤
  - `frontend/src/lib/locales/{en,zh,ja}.ts` — hint / noSteps
  - `frontend/src/lib/planHonesty.test.ts` — 三语文案
- AC：
  - G04-AC1 [x] S/T — 空计划合法；文案含「未接线」
  - G04-AC2 [x] T — `TestRunGoalRefusesPrototypeExecutorByDefault` 绿
  - G04-AC3 [x] S — 未新增假步骤；Computer Use 仍默认关；未写回 MCP 断链
- 下一步：P13-G05。
- 未 commit。

### 会话交付：P13-G05

- 复核结论：文档/bindings/backend-gate 本任务新跑为 V。`task frontend:check` **首次红灯保留**（`FileTree.test.ts` 10k 渲染 537ms > 500ms 预算，与 P13 改动无关）。`npm-audit-gate` nanoid high 仍红（U）。packaged-e2e **未跑**（脏树不可冒充历史 SHA）。
- 本次状态：未开始 -> **进行中**（2/5 勾选；AC4/AC5 诚实 U 未勾选）
- AC：
  - G05-AC1 [x] V — `node scripts/check-doc-numbers.mjs` exit 0（services=47）；`check-doc-links.mjs` exit 0（26 md）
  - G05-AC2 [x] V — `WAILS3_BIN=%USERPROFILE%\go\bin\wails3.exe`（v3.0.0-alpha2.111）`node scripts/check-bindings.mjs` exit 0
  - G05-AC3 [ ] V/T — 受影响 Go -run 集合 exit 0；受影响 Vitest 6 files / 148 tests exit 0；eslint src --max-warnings=0 exit 0；vue-tsc --noEmit exit 0；`backend-gate.mjs` **9/9 exit 0**（gofmt 0.9s、vet 163.2s、build 14.0s、go test 558.2s、contract 13.5s、bindings 14.1s、wails-pin 0.2s、doc-links 0.1s、doc-numbers 0.1s）。`task frontend:check` **两次 exit 1**（红灯保留）：`FileTree.test.ts` 10k 预算 537ms/525ms > 500ms；第二次另有 `EditorView.test.ts` save-conflict 可见性 flake。未为保绿删测试或放宽预算。
  - G05-AC4 [ ] U — `node scripts/npm-audit-gate.mjs` 门禁 FAIL：`nanoid <3.3.18` / `GHSA-2v37-7h3g-55p8` / 1 high。未改 lock、未删门禁。
  - G05-AC5 [ ] U — 脏树指纹已接入 harness（`workingTreeDirty` + `gitStatusSha256` + source fingerprint），**完整 fixtures 未完成，不得标 P**。半成品 manifest `status=running` / `phase=fixtures`，11 passed / 13 not-run，停在 `git-rebase-package`。HEAD `18b43cf0825f1e280dc56b54563c8f73506bbd36`，porcelain sha256 `af69540ef46816e85fc0bc78fb3c5513415d9df43e3bc0480de2cd40695c01cd`，source `bc677b18d6d0584f03cae2474224eeb631fc592a8150c9fa77c46e119f5d11f8`（1054 files），artifact `ef0891ebc0e6b4efc1e892b3a12b49fbe7639bebec4d073ccc9ca7550c6c80a4`（`artifactReused=false`）。用户明确：`wails3 build` / packaged 重建会打崩本机 DSH web GUI，本会话停止构建。macOS/Linux/CI U。禁止用 prompt-12 旧 24/24 SHA。
- 首次失败：backend-gate 第一轮 gofmt 红（`pathsec_test.go`），gofmt -w 后重跑 9/9。frontend:check 性能/竞态红灯保留。packaged 两次构建均在 fixtures 中途因 GUI/会话中断停下。
- 未验证：完整 packaged P；CI R；macOS/Linux frontend:check。
- 下一步：无（停止条件 B 仍适用：G05-AC3 连续 3 次 frontend:check 失败；G05-AC5 因 GUI 崩溃禁构建保持 U）。隔离 `vitest run FileTree+EditorView` exit 0（10k=306ms）。未为保绿放宽预算。未再启动 Wails 构建。
- 未 commit。



