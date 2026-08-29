# Koyori IDE (GugaCode) 代码质量、安全与可用性审查总结（prompt-12）

> 用途：对当前 checkout 进行一次独立、全面的代码审查总结，覆盖六个维度——代码质量、前后端功能完整性与可用性、代码安全性、AI 功能可用性、IDE 功能可用性与完整性、商业性与日常性。
>
> 与既有文档的关系：prompt-1~prompt-11 是 AI 驱动的**续作任务清单与证据台账**（Goal/AC/S-T-I-P-R-U 分级）；prompt-12 是一份**审查结论文档 + §11 长期发展 Goal 规划**（P12-G28~G33），对代码现状给出可复核的结论、证据定位与风险清单，并把发展方案写成可执行的长期 Goal。prompt-11 §1 的 Goal/AC 快照（27 Goals / 103 AC，74 勾选 / 29 未勾选）作为本文的成熟度基线沿用；§11 的 Goal 编号自 G28 起，不与 prompt-9/prompt-11 的 G01~G27 冲突。
>
> 审查方式：静态只读审查 + 定向证据核验。所有结论均附 `file:line` 证据，未执行真实构建/联网验证。审查日期：2026-08-12。

---

## 1. 总体结论（TL;DR）

**一句话：工程纪律远超同类 AI IDE 原型、核心本地编辑闭环质量上乘，但按自身验收标准仍处于 0.x 实验期——安全性存在 4 个 High 级缺陷、AI 的 Goal/MCP 链路未闭环、无真实发布历史、零商业化基础设施。**

| 维度 | 结论 | 证据密度 |
|---|---|---|
| 后端代码质量 | **良好**（架构生产级，测试纪律接近商业项目） | 高 |
| 前端代码质量 | **高度可用 + 可维护性中等偏上**（无桩、覆盖广、巨型组件） | 高 |
| 功能完整性 | 本地单机功能**全**（编辑/Git/终端/调试/市场/远程-最小）；远程 IDE、自动更新、崩溃上报、语言包为**半成品** | 高 |
| 代码安全性 | **Weak，部分 fail-closed**（0 Critical / 4 High / 7 Medium / 2 Low / 1 Info） | 高（对抗性审计） |
| AI 功能可用性 | **Chat + Agent 四工具可用**；Plan 半成品；Goal 默认禁用；MCP 与 agent 断链 | 高 |
| IDE 功能可用性 | **本地单机可日常使用**；生产级/全语言/远程不成立（G23/G26/G27 均未闭环） | 高 |
| 商业性 | **高质量开源实验项目**（engineering excellent, distribution unproven） | 中高 |

**跨维度的两个硬结论：**

1. **不可宣称"生产级"或"开源发布合格"**——prompt-11 §0/§1 自身即裁定：G07/G08/G09/G10/G19/G21 的真实 CI/macOS/发布证据仍为 `U`，G27 仅有 T 级基础；2026-08-17 当前本地 Windows packaged E2E 在一次 G16 失败后以同一 artifact/source 严格复用取得 24/24（见 §7.3 与 §13.27），但单平台 `P` 证据及一次复跑不能替代 macOS/Linux、真实 CI、签名与发布历史的 `R/U` 门禁，也不能抹去该可重复性风险。
2. **不可宣称"远程 IDE / 自治 AI / 全语言"**——remote_service 仅是 SSH+SFTP 文件系统与命令执行；Goal 自治模式内置执行器是自认的 prototype 且默认禁用；开箱完整语言包仅 Go/TS 两个。

---

## 2. 代码质量

### 2.1 后端架构与装配（生产级）

- `main.go` 仅 1006 行，职责限于进程生命周期：单实例锁 → 装配 → 窗口 → Run；业务对象图全部在 `bootstrap_services.go`，注册 **47 个 Wails 服务**（`bootstrap_services.go:85-139`）。
- 装配拆成 4 段 builder（`main.go:293-426`）：`buildFoundationServices` / `buildEditorServices` / `buildAgentServices` / `buildAnalysisServices`，依赖注入走 `services/trusted_wiring.go`，服务间无互相 `new`。
- `WorkspaceContext`（`workspace_context.go`）以 generation 计数实现共享身份与过期 capability 检测。
- 事件总线集中注册强类型事件（`main.go:113-152`）；e2e 通过 build tag + 环境变量门控，不污染生产路径。
- 关闭流程按依赖顺序并发 shutdown、10s 超时（`main.go:729-757`）。

### 2.2 后端代码质量指标

| 指标 | 数值 | 评价 |
|---|---|---|
| 非测试 TODO/FIXME/HACK | **1 / 0 / 0**（仅 `computer_use_windows.go:23` 截图待实现） | 极干净 |
| 错误体系 | `errors.go` 12 个 sentinel；`errors.Is/As` 390 处 | 好，但… |
| 字符串错误匹配 | 205 处 `err.Error()` 字符串判断 | 与 sentinel 并存，一致性风险 |
| 忽略错误 | 150 处 `_ =`（多为 best-effort cleanup） | 可接受 |
| >1000 行文件 | 12 个（`git_service.go` 3147、`mcp_service.go` 3017、`marketplace_service.go` 2334…） | 迷你 god-file |
| >200 行函数 | `AuthService.Login` 397 行（`ai_prompts.go:111`）等 | 需拆分 |

**正确性弱点（有实质影响）：**
- `diff_service.go:122-187` `ThreeWayMerge` 是**按行索引对齐的简化实现**（注释自认非 diff3），任一方插入/删除会让后续行错位，产生伪冲突或错误合并；测试只覆盖对齐场景。
- `workspace_edit_transaction.go`（466 行）相反是**高质量**：5 阶段执行 + LIFO 回滚 + hash/版本/dirty-buffer 前置校验 + commit receipt 重启校验，27 个事务测试护航。
- 重复逻辑：`contentHash` 两处、原子写辅助函数三份（`atomic_write.go` vs `workflow_service.go:541`）。

### 2.3 前端代码质量

- 规模：91 组件 / 11 视图 / 56 store / 46 lib / 14 api 模块；状态管理**不用 Pinia**，全为模块级 `reactive` 单例（`stores/ai.ts:79`）。
- TODO/FIXME 仅 **14 处**；TS `strict: true`（`tsconfig.json:7-9`）、ESLint 把 `no-explicit-any` 与 `vue/no-v-html` 设为 error（配合 DOMPurify）。
- ⚠️ 生成 bindings 被排除在类型检查外（`tsconfig.json:24`，11,238 行绑定无 vue-tsc 保障）；28 处非 api 文件直接 import 生成绑定，绕过 boundary 防御层。
- ⚠️ 巨型组件：`AiChatPanel.vue` 2937 行、`GitPanel.vue` 2573 行、`TerminalPanel.vue` 1683 行、`CodeEditor.vue` 1380 行——长期维护风险。
- ⚠️ 构建链整洁性：`vite.config.ts:12` 在 ESM 下用 `__dirname`；vite `^8.0.5`（Rolldown 新栈）；`pnpm-workspace.yaml` 与 `package-lock.json` 混用；`jest` 30 与 vitest 并存（`package.json:54`）。

### 2.4 测试健康与门禁（接近生产级）

- **Go：155 个测试文件、2,171 个 `func Test`、测试/源码 LOC 约 1:1**（68.4K / 73.1K）；含真实集成测试（gopls/tsls/vtsls 真实进程、真实 loopback HTTP、真实 Python/Rust 语言包 LSP+DAP、真实 pty），环境变量门控。
- 契约测试：`g13_wiring_test.go` 用 go/ast 断言服务已注册；`bindings_runtime_surface_test.go` 用反射比对 manifest 并**禁止 secrets/SetToolPaths 暴露**。
- **前端：170 个 *.test.ts、3,059 个 test/it/describe**；vitest 覆盖率阈值 50% 强制（`vitest.config.ts:47-52`）。⚠️ 测试基建慢（2 个文件实测 93.96s，大量 vi.mock @wailsio/runtime）。
- 门禁管线：`scripts/backend-gate.mjs`（gofmt→vet→build→`go test`（检测空测试二进制判红）→contract→bindings→pin→docs）与 `scripts/packaged-e2e.mjs`；CI 中 contract-smoke 仅 ubuntu 为强制门禁，Windows/macOS `continue-on-error`。
- 盲区：`activation_service.go`、`crash_service.go`、`update_service.go`、`errors.go` 无直接测试。

### 2.5 构建/依赖健康

- Go 1.25.0；依赖分组合理（Wails v3 `v3.0.0-alpha2.111`、go-git、sqlx+modernc sqlite、pgx、creack/pty、conpty、websocket、sftp）。**Wails v3 alpha 是最大底座风险**（README 自认），且 `main.go:959-962` 注释证实需绕过 Wails 公共 API 实现 plugin 协议。
- 绑定防漂移三重审计（manifest 比对 + 重生成 diff + 前端口径）；`check-wails-pin.mjs` 强制 lock 版本、禁 `@latest`。
- 前端 dist 29MB 产物存在且按路由/组件拆分，未入库。

---

## 3. 前后端功能完整性与可用性

### 3.1 前端表面（几乎无桩）

编辑器（Monaco，workers 本地打包）、文件树、终端（xterm 多分屏/重连）、调试（Delve/断点/watch）、Git 面板（**超完整**：stash/tag/submodule/bisect/rebase/worktree）、AI 聊天、设置（14+12 节）、插件/扩展宿主、Open VSX 市场（SHA-256 校验）、状态栏、i18n 切换（en/zh/ja）、Profiles、HTTP 客户端、数据库工具窗、远程开发、MCP、Skills、Workflows、Computer Use、覆盖率、FlameGraph、Test Explorer、Call Hierarchy、Structural Search——**均有真实实现**，无占位屏。

### 3.2 后端服务完整性

47 个服务覆盖：文件/编辑事务、LSP、DAP、终端、Git、PR（GitHub+GitLab）、搜索/符号索引、项目/多根、窗口、布局、恢复、更新、市场、扩展、MCP、技能、工作流、工具链、快照、PProf、远程（SSH/SFTP）、AI 全套。

### 3.3 关键缺口（半成品清单）

1. **远程"IDE"不存在**——仅 SSH+SFTP 文件系统与命令执行，无远端 Terminal/Git/LSP/DAP/agent/端口转发；G26 **0/4 AC**（`remote_service.go:794-1005`；README 自认"不是 Remote-SSH"）。
2. **自动更新是占位**——`ApplyUpdate` 明确拒绝自动安装（`update_service.go:400-403`）；Ed25519 签名清单（`update_manifest_g27.go`）**未接入生产更新链**（仅测试证明）。
3. **崩溃上报是占位**——`UploadCrash` 无端点（`crash_service.go:253-273`）。
4. **开箱语言包仅 2 个**（Go/TS 完整；Python/Rust/JSON/CSS/HTML/YAML/ESLint/Vue/Angular 为无打包 debugger/版本 pin 的 base 定义，`lsp_service_server.go:135-213`）；G23 AC2-4 `U`。
5. **符号索引仅 5 种语言**（.go/.ts/.tsx/.js/.jsx，`symbol_index_service.go:1004-1105`）；"结构性"搜索是行范围编辑而非 tree-sitter AST。
6. **macOS 全面未验证**（构建/打包/信号/CI 历史全 `U`）。

---

## 4. 代码安全性（对抗性审计）

**总体判定：Weak，部分 fail-closed。** AI 命令审批、私网 HTTP 令牌、SSH host key、扩展 Worker 协议与 VSIX 解压防护较强；但核心文件 API 是 check-then-use TOCTOU、扩展真实性与更新签名链不完整、Wails 权限边界过宽、存在明文 secret 降级。共 **14 项发现：0 Critical / 4 High / 7 Medium / 2 Low / 1 Info**。

### 4.1 High（4）

| # | 发现 | 证据 |
|---|---|---|
| H1 | **FileService symlink TOCTOU，可逃逸工作区**：`ValidatePathWithinRoot` 先 EvalSymlinks 但返回原始 `abs`，随后按路径名操作；校验与使用之间父目录可被替换为指向工作区外的 symlink/junction。安全的 openat2 实现仅用于未暴露给 Wails 的 `LocalWorkspaceHost`（`local_host.go:33`），实际前端调用的是 `FileService` | `pathsec.go:56`、`file_service.go:288`、`atomic_write.go:62` |
| H2 | **Windows `.cmd/.bat` 命令注入**：shim 改为 `cmd.exe /c <basename> <args...>`，cmd 重新解释 `& \| < > % ! ^`；仓库控制的文件名/测试名/配置值可触发额外命令 | `exec_cmd.go:44`、`toolchain_service.go:600` |
| H3 | **VSIX"签名验证"只是同源 SHA-256**：注释承认 detached signature 是 future implementation；VSIX 与期望 hash 均来自同一 registry，registry/CDN/发布账号被攻陷可同时替换二者并被标为 Verified | `extension_security_service.go:288`、`marketplace_service.go:695` |
| H4 | **插件 sandbox 可关闭**：关闭后插件在主线动态 `import()` 执行，直接访问 DOM/Wails bindings/设置/终端，不再受 manifest permission RPC 限制 | `frontend/src/lib/pluginRegistry.ts:242,714`、`main.ts:312` |

### 4.2 Medium（7）

| # | 发现 | 证据 |
|---|---|---|
| M1 | Wails 后端**无窗口角色授权**——runtime-role token 只控制前端启动，所有窗口共享 47 个服务；任何主 origin XSS/插件逃逸可直接调用文件/终端/更新/Git/调试绑定 | `bootstrap_services.go:84`、`runtime_role.go:112` |
| M2 | **Ed25519 更新验证未接入生产更新链**，当前只信任 GitHub API 返回的 asset digest | `update_manifest_g27.go:100` vs `update_service.go:248` |
| M3 | **密钥加密失败静默降级为明文**：`EncryptSecret` 失败仍保存 `plain:<key>` | `settings_service.go:525` |
| M4 | Reviewed 扩展的启用确认**仅由前端执行**，后端只对 Restricted 强制 capability；直调 `SetExtensionEnabled` 可绕过 | `extension_security_service.go:680`、`marketplace_service.go:1574` |
| M5 | 市场仅校验 registry base URL，registry 返回的 download/hash/readme URL **未做私网/重定向复验**，恶意 registry 可引导 SSRF | `marketplace_service.go:413,695,1774` |
| M6 | **已变化（P13-G03）：** AI Chat/stream 使用 `NewAISSRFSafeTransport` 拨号二次校验（允许 loopback 本地模型，拒绝 metadata/private）。MCP/HTTP client 仍用更严的 `NewSSRFSafeTransport`（拒绝 loopback） | `ai_urlsec.go`、`ai_service.go` |
| M7 | AI provider 错误正文**未脱敏即入日志并可能回显**（Authorization/凭证） | `ai_service.go:130,877` |

### 4.3 Low / Info

- Low：日志文件权限 0644（`logging.go:57`）。
- Low：AES fallback 的 key 与密文同属用户配置域，非 OS keyring 级保护（`secrets_aes.go:30`）。
- Info：未发现生产 TLS/随机数误用——无 InsecureSkipVerify/MD5/SHA-1；SSH 强制 known_hosts（`remote_service.go:1362`）；扩展 Worker token/ABI/配额/watchdog 均 fail-closed；VSIX 解压有 traversal/symlink/数量/大小/压缩比限制。

### 4.4 Top 5 风险

1. FileService symlink TOCTOU → 工作区逃逸（读/写/删外部文件）。
2. Windows `.cmd/.bat` 二次解释 → 命令注入。
3. VSIX 缺少真正发布者签名，registry hash 不抵御供应链攻击。
4. 插件 sandbox 可关闭 → 完整 Wails 本机能力。
5. Wails 无窗口级授权 → 任何 webview 代码执行放大为本机能力。

---

## 5. AI 功能可用性

### 5.1 可用（✅）

- **Provider**：OpenAI 与 Anthropic 双协议，7 个预设（OpenAI/Azure/Anthropic/Gemini/Ollama/LM Studio/Custom），Base URL 可配；API key 加密落盘（DPAPI/AES-256-GCM，`settings_service.go:525-551`），`LoadSettings` 对前端永远清空明文，明文 key **不跨 Wails 绑定**（UseStoredKey 机制，`ai_service.go:339-340`）。
- **Chat 完整链路**：流式 SSE（streamId 归属校验 + 5 分钟超时兜底 + 200 条 FIFO）、会话历史持久化（CAS 修订 + 跨窗口同步）、上下文截断（token_estimator 启发式）、双协议 native tool-call 解析。
- **Agent 四内置工具闭环**：`read` / `write` / `run` / `search`（`frontend/src/stores/agent.ts:552-583`）。write 走一次性审批令牌（绑定路径+内容 hash+baseline+工作区代+TTL）+ 事务化写入；run 走危险命令 denylist + 全部最低 RiskElevated + shell 元字符拒绝 + shlex 直连 exec（无 shell 注入面）+ cwd 沙箱；预算为**后端强制硬限制**（epoch 绑定，20 次/30 分钟，跨 epoch 令牌作废）；公网方法（`ExecCommand`/`CallMCPTool`/`CallTool`）一律 deny-only 桩。
- **SSRF 防护**：`ValidateNonPrivateURL` 全量阻断 private/loopback/link-local（含 169.254.169.254），域名 A/AAAA 解析校验 + 拨号时二次校验防 DNS rebinding（`ai_urlsec.go:159-240`），用于 MCP 与 HTTP 客户端。

### 5.2 断链/半成品（❌/⚠️）

| 功能 | 状态 | 证据 |
|---|---|---|
| MCP 工具被 agent 调用 | ❌ **断链**：MCP 工具仅以 `@`-mention chip 提示模型，工具注册表只有 4 个内置工具，`executeToolCall` 对未知 kind 直接报错 | `agent.ts:552-583,590-596` |
| Goal 自治模式 | ❌ **默认禁用**：内置 `defaultGoalExecutor` 自报 IsPrototype、固定跑 `go env GOOS`、从不评估成功，被 `AIGoalService` 拒绝驱动 | `executor_adapters.go:190-245`、`ai_goal_service.go:443-450` |
| AI 生成规划 | ⚠️ 无 `plan` 工具：头注释声称 Plan 模式只能用 `plan` 工具生成步骤，但工具集没有定义；前端以**空步骤数组**创建 Plan，步骤由用户手填 | `ai_plan_service.go:4-5`、`PlanPanel.vue:55` |
| Computer Use | ⚠️ 仅 Windows 原生实现，Unix 为 stub，默认禁用 | `computer_use_unix.go` |
| 流式重试 | ⚠️ 重试仅覆盖非流式路径（429/5xx 退避+抖动）；流式中断直接失败 | `ai_retry.go:80-82` |

### 5.3 判定

**Chat 模式与 Agent read/write/run/search 端到端可用（安全设计是亮点）；Plan 可执行用户手写步骤但无 AI 生成；Goal 与 MCP-agent 集成为"看起来有、实际不闭环"。** 因此 README/注释中关于"自治编码 Agent"的宣传与实际能力不符。

---

## 6. IDE 功能可用性与完整性

### 6.1 完整且高质量（✅）

- **LSP**：近乎 LSP 3.17 全量覆盖（导航/编辑/符号/诊断/语义 token/代码操作/重构/workspace symbols），server 崩溃自愈（`lsp_service_session.go:213`）、框架识别（vue/angular/react）、workspace folder 切换；真实集成测试（gopls/tsls/vtsls）存在（`KOYORI_IDE_LSP_INTEGRATION=1` 门控）。
- **DAP/CDP 调试**：Go/Delve（DAP）、Node/TS（CDP inspector 全量客户端含 async stack）、浏览器（Chrome/Edge CDP）、外部语言包 stdio DAP；条件/logpoint/函数断点/数据断点/嵌套变量/evaluate/watch/restartFrame/线程模型齐全；**有真实 Delve 与 Node 会话的 I 级测试**（`debug_g14_real_adapter_test.go`）。
- **Git**：覆盖最广——status/diff/blame(带 LRU 缓存)/commit/amend/push/pull/branch/tag/stash/**交互式 rebase**/merge 冲突解决/**worktree**/cherry-pick/revert/bisect/submodule/commit graph；PR 双 provider（GitHub+GitLab）list/get/create/comment/review。
- **文件保存完整性（四层防护）**：原子写（tmp+fsync+rename；Windows `MoveFileExW(WRITE_THROUGH)`）+ baseline hash 冲突检测 + 编辑事务冲突 + 脏缓冲 journal 恢复——同类 IDE 少见的安全写盘设计。
- **终端**：ConPTY/Unix pty、shell 白名单（拒绝非白名单）、Windows 参数转义防注入、退出协议、并发安全测试齐全。
- **恢复/快照**：hot-exit 脏缓冲 journal（原子写、0600、配额、baseline 三态冲突检测）+ 内容寻址快照/回滚，设计成熟（1138 行）。

### 6.2 缺口（❌/⚠️，见 §3.3）

远程非 IDE、自动更新拒绝安装、崩溃上报无端点、开箱语言包仅 2 个、符号索引 5 语言、macOS 全 U。

### 6.3 判定

**作为本地单机编辑器：编辑/保存/Git/LSP(Go/TS)/调试(Go/Node)/终端均已闭环且有真实进程级测试，可日常使用。** 但按 prompt-11 自身门禁，**"开源发布合格"与"生产级"均未达成**（G23 AC2-4、G26 0/4、G27 0/4 均为 `U`）。

---

## 7. 商业性与日常性

### 7.1 许可与合规（✅ 商业友好）

- MIT 许可（`LICENSE`），可闭源衍生、可嵌入；NOTICE 声明未发现 GPL/AGPL；42KB 三方许可清单零 UNKNOWN/UNRESOLVED，`--full-check` 为发布硬门禁；唯一例外 `go-qsort@v0.1.0` 不在生产构建闭包内。

### 7.2 变现机制（❌ 零商业化基础设施）

- `activation_service.go` 管理的是**扩展 activationEvents**，不是产品授权/激活服务器；市场默认 Open VSX（免费）；全仓扫描 license key/entitlement/billing/paywall/trial 为零命中。**无激活服务器、无付费功能、无订阅、无自营市场。** 若未来商业化需从零搭建授权/支付/市场基础设施。

### 7.3 发布管线（设计专业，本地 Windows P 已恢复；跨平台/R 仍 U）

- 设计：三平台 release 矩阵、签名默认强制、每产物 SBOM + in-toto provenance + SHA256SUMS、版本 SSOT（VERSION=0.2.0 五处一致性检查）、npm audit 各级清零。
- 真实证据（负面为主）：
  - **无 v0.2.0 tag**（仅有 `g26-foundation-backup` 备份 tag；git 现有 6 个 commit）。
  - **历史本地 Windows packaged 基线（2026-08-14）**：`build/e2e-evidence/packaged-e2e/manifest.json` 当时为 `status=passed`、`phase=complete`、24/24 fixtures passed；artifact `bin\\koyori-ide.exe` SHA-256 `9e382df322b7ea42f881a6cc656c029c5e32432cac84286b162f1a5c39caaa1a`，source fingerprint `09635b427d2fc8852049c5b44550ed8ed2a49afc7ef4aabd097401865fcf8485`，commit `18b43cf0825f1e280dc56b54563c8f73506bbd36`，Wails expected/actual 均为 `v3.0.0-alpha2.111`。该记录仅作历史对照；当前代码态以 §13.27 的 2026-08-17 manifest 为准。`screenshot` 字段为 `null`，旧 `detached-exit-code.txt`/`detached-run.log` 的 `0xc0000142` 失败仍保留为历史。
  - **当前代码态 Windows packaged 证据（P13-G05，2026-08-22）**：权威文件仍是 `build/e2e-evidence/packaged-e2e/manifest.json`，但是 **partial**：`status=running` / `phase=fixtures`，11 passed / 13 not-run，不得当 24/24 P。HEAD `18b43cf...`，`workingTreeDirty=true`，porcelain sha256 `af69540e...`，source `bc677b18...`（1054 files），artifact `ef0891eb...`，`artifactReused=false`。完整 fixtures 未完成；本会话因 `wails3 build` 打崩 DSH web 而停止重建。2026-08-17 的 24/24 SHA 仅作历史。
  - macOS 构建/打包/信号/CI 历史全部 `U`；CI 中 Windows/macOS 腿 `continue-on-error`。
  - `docs/CHANGELOG.md` / `docs/E2E.md`（P13-G01）：现写「有本地 git（commits + `beta0.2.0` tag）；无已验证正式 `v0.2.0` GitHub Release」。不要把 `beta0.2.0` 写成正式 `v0.2.0`。
  - `bin/` 下 Linux ELF 未 strip、含 debug_info（非发布形态）。

### 7.4 日常可用性信号

| 维度 | 状态 | 证据 |
|---|---|---|
| Onboarding | ✅ 欢迎页（打开/新建/最近项目） | `WelcomeView.vue` |
| 版本显示 | ✅ 英雄区与页脚同源 `__APP_VERSION__`（P13-G01） | `WelcomeView.vue` + `WelcomeView.test.ts` |
| 错误处理 UX | ✅ 全局错误边界 + toast + 统一 notifyError | `App.vue:65-70` |
| 崩溃恢复 | ✅ 成熟（hot-exit journal + 快照回滚） | `recovery_service.go` |
| i18n | ✅ en/zh/ja 三语 7,591 行字典 + ICU 复数 + 缺失 key 监控 | `lib/i18n.ts` |
| 可访问性 | ⚠️ 有 aria/prefers-reduced-motion，但 **WCAG 2.2 门禁 U** | `prompt-11.md:165,173` |
| 性能 | ✅ pprof 全套 + CI ±20% 回归门，但无端到端大仓库基准 | `pprof_service.go` |
| 离线 | ✅ 离线优先名副其实（LSP 本地 PATH、go-git 纯本地、离线测试套） | `offline_test.go:13-33` |
| 遥测/隐私 | ✅ 事件 buffer 默认关闭、仅内存、隐私正则 fail-closed；生产代码无 Enable 调用 | `operational_events_g27.go:296-323` |

### 7.5 成熟度分级

- **(a) Hobby/实验级：成立**（自身声明 + Wails alpha 底座 + 零真实发布 + packaged 仅 Windows 单平台 P 级 + 29 AC 未勾选）。
- **(b) 个人开发者日常可用：条件性成立**——功能广度与工程纪律惊人，但需 Windows 尝鲜用户 + 容忍实验定位；Go/TS 语言服务与打包稳定性未闭环前体验有不可预知风险。
- **(c) 商业产品/团队采用：不成立**——零商业化基础设施、无 SLO/审计/真实发布、macOS 未验证。

---

## 8. 综合评分矩阵

| 维度 | 评分 (1-5) | 一句话 |
|---|---|---|
| 后端代码质量 | 4.0 | 架构与测试纪律生产级；巨型文件/merge 算法/错误风格待治理 |
| 前端代码质量 | 3.5 | 无桩、测试密、安全内建；巨型组件/store、bindings 半集中 |
| 功能完整性 | 3.5 | 本地全、远程/更新/上报/语言包半成品 |
| 代码安全性 | 2.0 | 部分 fail-closed，4 High 需优先处置 |
| AI 功能可用性 | 3.0 | Chat+四工具可用；Goal/MCP 断链 |
| IDE 功能可用性 | 3.5 | 本地日常可用；生产级不成立 |
| 商业性 | 2.0 | 开源实验项目，distribution unproven |
| 日常性 | 3.0 | Windows 尝鲜可用；macOS/打包/LSP 真实验证缺失 |

---

## 9. 交叉核对与冲突修正（Reconciliation）

本文在汇总结论时对既有文档做了以下核验与修正：

1. **packaged E2E 状态**：prompt-11 §1.2 的 2026-08-11 G24 24/24 通过、2026-08-14 24/24 基线及后续所有通过/失败均为历史事实；**当前权威** `build/e2e-evidence/packaged-e2e/manifest.json` 以 §13.28 记录的 2026-08-17 Windows 本机 fresh build 结果为准（24/24）。§13.27 的首次 5 passed、`terminal-exit-package` failed、18 not-run 与同 artifact/source reuse 通过必须保留为历史。**结论：当前 Windows 本地 P 级通过**；这不构成 macOS/Linux、CI、签名或 release 的 `R` 级发布就绪证据。
2. **LSP 真实验证**：README V/U 表称"本机未装任何语言服务器"（旧状态）；代码库存在门控的真实集成测试且 prompt-11 §1.2 记录过真实 TS LSP 通过。**结论：I 级测试存在但需环境变量门控，README 诚实表已过时**。
3. **git 状态**：prompt-11 §0 规则 12 称"工作区没有可核验 .git"；当前 checkout 有 6 个 commit + 1 个备份 tag。**prompt-11 该条已过时**，但"无真实发布 tag/CI 历史"的结论不变。
4. **前端测试数**：170 个测试文件 / 3,059 用例（vitest），与 G25 的 i18n 53 项数字不冲突（后者为专项子集）。
5. **Wails 暴露面**：`FileService` 是实际前端调用路径（TOCTOU 风险面），`LocalWorkspaceHost` 的 openat2 安全实现未接入——两个审查员的发现相互印证，非矛盾。

---

## 10. 行动建议（按优先级）

1. **安全 P0**：修复 H1（FileService 迁移 dirfd/openat2 或逐级 CreateFile + reparse 拒绝）、H2（.cmd/.bat 直启 node 入口而非 cmd /c）、H4（生产构建强制 sandbox）、M3（secret 加密失败 fail-closed）。
2. **安全 P1**：H3 改为真实发布者签名或至少 UI 改标 `integrityChecked`；M2 将 Ed25519 manifest 接入生产更新链；M1 增加后端窗口级能力边界。
3. **AI 闭环**：实现 `plan` 工具与真实 LLM 驱动 executor（接入 Goal）；将 MCP 工具注入 agent 工具注册表打通生态；或收窄 README 宣传口径。
4. **发布前置**：保留当前 Windows 24/24 证据并补齐 Linux/macOS 同矩阵；创建 v0.2.0 tag 与真实 release 记录；修复 `WelcomeView.vue:123` 版本硬编码。
5. **治理**：拆分 `mcp_service.go`/`git_service.go`/`AiChatPanel.vue`/`GitPanel.vue` 等巨型文件；用 diff3 算法替换简化 ThreeWayMerge；统一错误处理（减少字符串匹配与 `%v` 漏 wrap）；消除 jest/vitest 与 pnpm/npm 双轨。

---

## 11. 长期发展 Goal 规划（P12-G28 ~ G33，AI 续作任务）

> 本节把 §1~§10 的审查结论转化为可交给后续 AI 按点执行的长期 Goal 任务清单，沿用 prompt-9/prompt-11 的纪律：**一次只推进一个 Goal**；证据分级固定为 `S/T/I/P/R/U`；AC 未全勾选不得宣称完成；安全修改先补绕过失败测试；用户数据修改先证明冲突/崩溃/回滚；被外部状态阻塞的项保持 `U` 如实记录，不得伪造。
>
> **建议推进顺序（依赖优先）：G33（执行核心，架构地基）→ G28（计费，依赖 G33 的计量）→ G29（工作流编排，依赖 G33 的工具注册表）→ G30（Git）→ G31（diff-first，可并行于 G30）→ G32（内置 skill，可并行）。** G30/G31/G32 相互独立，可在不冲突的前提下分派并行车道，但不得同时推进多个 Goal 的主体实现（遵守一次一个 Goal 纪律）。

### G28：AI 计费 Dashboard（跨维度成本可见性与硬预算）

**现状（已核验）**：后端已有 Goal 级成本计量（`ai_goal_service.go` 的 `Cost/MaxCost/TotalCost/GetCostReport`，:45-117, 655）与 Agent 工具预算（`agent_budget.go` epoch 硬限制）；前端仅有 `GoalSection.vue:356-369` 的单 Goal 成本卡。**缺口**：无跨 provider / 会话 / 项目聚合、无用量历史与趋势图、无用户级总预算、无成本导出；Chat/Workflow/MCP 消耗无统一计量入口。

**AC（最低 `I` + `P`）**：
1. **统一计量**：所有 LLM 消耗（chat/agent/goal/workflow/MCP）经统一计量点记账，记录 model、provider、输入/输出 token、估算与真实成本、会话与项目归属；本地 bounded 落盘，遵守 `operational_events_g27.go` 的隐私纪律（默认关闭遥测、敏感内容不落盘）。
2. **Dashboard UI**：总览（今日/本周/本月成本、budget 条）、按 provider/model/项目/会话维度的趋势图与明细表、成本明细可导出。
3. **硬预算 fail-closed**：用户级每日/每周/总额预算，超限后后端拒绝新流（复用 `agent_budget.go` epoch 机制与 `errors.go` sentinel），渲染器传入的预算不构成授权。
4. **估算准确**：`token_estimator.go` 启发式与真实计费对比的误差率预算 + 校准测试。
5. **安全**：成本记录不包含 prompt 内容；日志脱敏复用 HTTP client secret sanitizer 模式。

**禁止**：仅做前端图表而无后端统一计量；把估算成本冒充真实账单。

### G29：AI 工作流编排闭环（一句话 → 可审批的多步计划 → 执行）

**现状（已核验）**：`workflow_service.go` 已支持 command/ai/git/file/mcp/skill 六类步骤与事件触发（:44-53, 66-87）、项目级 workflow 强制 `RequiresConfirmation`（:120-127）；但 **AI 不会生成 workflow**——`plan` 工具不存在（`ai_plan_service.go:4-5` 声称存在但工具集未定义）、Goal executor 是自认 prototype 且默认禁用（`executor_adapters.go:190-245`、`ai_goal_service.go:443-450`）、MCP 工具无法被 agent 调用（`agent.ts:552-596` 注册表无 mcp.*）。

**AC（最低 `I` + `P`）**：
1. **`plan` 工具**：实现 AI 生成多步计划的工具，输出步骤（type/tool/输入/依赖/风险标注），接入 native tool-call 双轨；前端可展示、逐条审批、编辑后批准。
2. **MCP 注入**：MCP 工具进入 agent 工具注册表与 `buildNativeToolDefs`，执行器可调用；`@`-mention 与自动注入统一。
3. **真实 executor**：以真实 LLM 驱动的 executor 替换 prototype `defaultGoalExecutor`，Goal 模式默认可用（保留 MaxIterations/MaxCost/MaxDuration/连续 3 错终止，:477-511）。
4. **统一调度**：Plan / Goal / Workflow 共享同一执行管线（审批 → 预算 → 事务 → 审计），workflow 的 `WorkflowStepAI`（commit-msg/review/generate，`workflow_service.go:49`）真正生效。
5. **失败恢复**：中断后可 checkpoint/resume，不静默覆盖用户文件（复用 `snapshot_service.go` 快照与 `workspace_edit_transaction.go` 事务）。

**禁止**：用固定步骤/伪执行冒充 AI 规划；MCP 仅提示不调用即宣称闭环。

### G30：Git 完美集成（以 AI + diff 差异化超越 JetBrains）

**现状（已核验）**：Git 已超完整（rebase/worktree/stash/blame LRU/PR GitHub+GitLab/CommitGraph）；**短板**：ThreeWayMerge 是行对齐简化算法（`diff_service.go:122-187`，插入/删除错位会产生伪冲突）、无 AI 辅助能力、`DiffView.vue` 只读（§11 G31 前置）。

**AC（最低 `T` + `I`（真实仓库 E2E））**：
1. **diff3 算法**：用真实 diff3（或等价安全策略）替换简化 ThreeWayMerge，补齐插入/删除/双方同改的错位测试（现有测试 `diff_service_test.go:64-131` 只覆盖对齐场景）。
2. **AI 冲突解决**：冲突块 → AI 三路合并建议（含原理说明）→ 用户批准 → 走事务写入；拒绝时保持冲突标记。
3. **AI commit message / PR 描述**：接入 `WorkflowStepAI` 与提交表单；无敏感内容、可编辑、默认不自动提交。
4. **hunk 级暂存**：partial stage/commit 的 hunk 选择 UI（diff-first 交互的 Git 侧）。
5. **大仓库性能**：go-git vs CLI 的真实基准与性能预算（blame 已用 LRU，扩展到 diff/log/graph）；超预算时降级策略。
6. **安全**：diff 视角下所有写入仍走 `workspace_edit_transaction.go` 路径校验（含 `pathsec.go`），修复 H1 前不得宣称 Git 写路径安全。

**禁止**：堆功能不做 diff3 正确性；AI 建议未经审批直接应用。

### G31：代码默认 diff 视角（diff-first 编辑体验）

**现状（已核验）**：`DiffView.vue` 是**只读查看器**（getDiff → 渲染，无 accept/reject/apply/stage 动作）；AI 写入虽有事务但用户审查依赖聊天卡片（`AiChatPanel.vue:1006-1077`）；无 inline diff 编辑与冲突导航。

**AC（最低 `T` + `P`）**：
1. **DiffView 交互化**：hunk 级 accept/reject/apply/stage/unstage，复用 `workspace_edit_transaction.go` 事务与 `diff_service.go` 导出；变更不落盘前可预览回滚。
2. **默认 diff 视角**：打开文件/切换分支/拉取后默认展示变更概览（可关闭）；"代码默认给 diff 视角"作为产品默认体验而非附加视图。
3. **AI 写入进 diff 审查流**：Agent write 审批后先在 diff 面板呈现待应用变更（与聊天卡片审批合一或二选一），用户批准后事务落地；写事务先决条件（hash/dirty-buffer/版本）在 UI 可见化。
4. **inline diff 编辑 + 冲突导航**：Monaco inline diff、逐冲突块跳转/解决（与 G30 AC2 复用）。
5. **性能**：大 diff 懒加载与虚拟滚动（参照 `MessageList.vue:37-71` 的依赖-free 虚拟滚动）。

**禁止**：只做展示不做落盘闭环；默认视角变更破坏既有保存流程（保留 `WriteFileIfUnchanged` 冲突检测）。

### G32：内置 Skill 体系（新用户快速上手）

**现状（已核验）**：`skills_service.go` 已实现 Skill 发现/加载/匹配（`SkillScopeProject/User/Global`，:36-41）、触发条件、优先级合并 SystemPrompt、项目级确认（G-SEC-03，:71-72）；但 **`SkillScopeGlobal` 生产代码零使用、无任何内置 skill 目录**（`services/templates/` 是项目脚手架模板，非 AI skill）。

**AC（最低 `T` + `P`）**：
1. **内置 skill 库**：随产品打包一组全局 skill（如：AI code review、commit message、debug 引导、语言包安装、workflow 向导），`SkillScopeGlobal` 实际生效；默认低权限、无需确认（仅 project scope 需确认，保持 G-SEC-03）。
2. **快速上手路径**：首次启动用 skill 引导完成"新建项目 → 配置 AI provider → 跑通第一次 commit"，目标 < 5 分钟。
3. **Skill 浏览器**：设置页列出可用 skill（内置/用户/项目）、触发器、优先级、启用状态；可手动 `@skill` 激活。
4. **导入导出**：skill 文件导入导出复用 profile 的 schema 版本化 + redact + 原型污染拒绝纪律（`profile_service.go`）；项目级 skill 首启确认不可被绕过。
5. **文档**：内置 skill 各有 README/示例，缺失 key 不显示 raw key（复用 i18n fallback）。

**禁止**：把模板/示例当内置 skill；项目级 skill 免确认激活。

### G33：统一 Agent 执行核心（架构地基，用户指定的"核心"）

**现状（2026-08-18 最新复核）**：缺口已变化但仍存在。`internal/agentcore.Registry`、统一 capability/lifecycle/meter、headless core 与 MCP/Git 拆分已形成 T 级架构基础；typed workflow `file.read`、`file.write`、只读 `git.status`、`mcp.call`、`skill.activate` 和 `ai.generate/review/commit-message` 已从 backend-owned identity 经同一 catalog/capability 执行。session recovery 已有仅接受 `discard` 的 backend-only dispatcher，external receipt recovery 已有独立、仅接受精确 `manual-unknown` 的 backend-only dispatcher；两者均不注册 Wails，后者不调用 adapter、不伪造 resume/compensation。trusted headless CLI 现可跨进程 inventory/处置 external receipt，Plan/Goal adapter 统一解析 opaque runtime owner；workflow attempt 已改用 durable usage ledger 作为 SSOT，TaskService 重建、terminal 写首次失败后的同 UnitID 重试、ambiguous/forged pending fail-closed 与并发完成均有 T 级回归。workspace reset 现在在 durable lifecycle 发布成功后才清理 `sessionOwners/sessionSkills`，预发布失败保留旧 runtime/owner，发布不确定时随 runtime authority 一并撤销；该回滚边界取得 T 级回归。Windows 本机真实 MCP stdio 与 trusted CLI 子进程已取得 I 子证据，但跨平台/CI operator/CLI、真正 resume、adapter-specific compensation、跨 caller/domain rollback、跨进程 CAS、跨平台/远端/CI 与真实 AI provider 仍 U，故 G33 AC 仍为 0/6。

**AC（最低 `T` + `I`）**：
1. **统一工具注册表**：单一 ToolDef 源（内置 read/write/run/search + mcp.* + workflow 步骤 + skill 动作），native defs 与围栏解析同源生成（消除 `agent.ts:763-803` 与 MCP 的断链）。
2. **统一执行管线**：所有工具执行经过同一审批（一次性令牌）→ 预算（epoch 硬限制）→ 事务（`workspace_edit_transaction.go`）→ 审计链；公网方法保持 deny-only 桩。
3. **统一会话生命周期**：chat / plan / goal / workflow 共享 stream、checkpoint、resume、失败恢复语义；上下文管理（`token_estimator.go`）单一入口。
4. **统一成本计量**：每个执行单元产出成本记录（G28 依赖本 AC）。
5. **架构验收**：`mcp_service.go`/`git_service.go` 等巨型文件按核心边界拆分（§2.2 治理项）；核心包无 UI 依赖、可 headless 复用（CLI/CI 调用）。
6. **回归**：现有 2,171 个 Go 测试与 3,059 个前端测试全绿；`backend-gate.mjs`/`frontend:check` 通过。

**2026-08-16 进行中快照（AC 仍为 0/6，不得宣称完成）**：

- [ ] AC1 `T/I/U`：`internal/agentcore.Registry` 已成为 builtin/MCP/workflow command/skill/AI 的单一后端 `ToolDef` catalog，前端 native defs 与围栏解析消费同一 catalog 投影；workflow/skill 文件变化会先清空 source、推进 revision 再刷新。workflow/Skill mutation 与普通 dynamic refresh 现由同一 `catalogRefreshMu` 串行，`Registry.ReplaceSources` 会在一个写锁内校验并提交 MCP/workflow/Skill 三个 candidate；普通 refresh 的消费者只能看到完整旧快照或完整新快照，mutation 则先用一个 revision 整批撤销三个动态 source、再用一个 revision 整批发布新快照，以立即烧毁旧 capability。任一 builder/schema/跨 source 冲突失败时整批保持空，不发布成功子集；确定性交错、失败窗口与并发 reader 测试取得 `T`。锁内 MCP 枚举有 15 秒硬上限，超时清空动态 source，`ListAgentMCPTools` 会向调用方传播 context cancellation，不再把取消吞成空成功。handler 注册仍发生在 candidate build 阶段且不随 Registry 回滚，未发布 ToolDef 时不可调用，但不得宣称 handler wiring 事务化。typed workflow 已有 `file.read`、`file.write`、只读 `git.status`、`mcp.call`、`skill.activate` 与 `ai.generate/review/commit-message`：AI 只接受显式 `tool` + 唯一 bounded `input.prompt`，prompt/operation 从 backend-owned workflow source 重载，renderer capability 参数固定为空对象，provider/config 由后端解析并绑定 fingerprint，统一 external receipt 与 provider usage；file 固化 canonical root-relative path 并复用 builtin read/`FileService`；Git 不接受 renderer repo/path/command/cwd，只从 backend 当前 workspace 调用 `GitService.GetStatus`；MCP 从 session-owned workflow 重载 delegated ToolDef/input，绑定 input hash、catalog revision 与一次性 capability，并在 receipt 前重新核对 tool/schema；Skill 只接受精确 `tool: activate` + 唯一 canonical `input.id`，ToolDef/Prepare/approval/TaskService/receipt 绑定 workflow/step、scope/fingerprint，并复用既有 `skill.activate` handler 与项目确认。前端 runner 只接受严格 typed shape，catalog API 缺失时不回退 command adapter。AI 的 provider assignment 由 backend-only resolver 绑定 endpoint/protocol/key；导出的 `ResolveModelFor` 仅返回脱敏的 model/ConfigID/预算元数据，不能把全局 provider 细节带回 renderer。Windows 本机真实 MCP stdio 子进程现取得 `I` 子证据：独立 helper PID 经 `ConnectServer -> catalog ToolDef ID/revision -> workflow approval/capability -> TaskService -> renderer result`，并以同一 configDir 重载 terminal usage receipt、在 `Close` 后确认子进程已同步回收。该证据不覆盖其他 OS、真实远端 MCP 或真实 AI provider；Git 其余 mutation、其余 adapters、跨平台 MCP 与 CLI/CI consumer `I` 仍缺，故 AC1 不勾选。
- [ ] AC2 `T/I/U`：审批、epoch budget、一次性 capability、参数/catalog/workspace/session generation 绑定、审计已归一；workspace transaction 与 external preallocated receipt/补偿边界保持 fail-closed。新增仅供 trusted bootstrap/headless 持有的 `AgentExternalReceiptRecoveryDispatcher`：跨重载 inventory 只暴露稳定 opaque HMAC handle、状态和时间，只接受精确 `manual-unknown`，把 pending receipt 终态化为 unresolved/unknown；它不调用 adapter、不伪造 resume 或 compensation，也不注册 Wails。owner incarnation、旧 runtime authority、workspace generation/fingerprint 在发布前同锁复核；pre-publish 失败可重试，post-publish durability unknown poison 当前进程，fresh reload 后才能确认。Windows trusted CLI 子进程 I 子证据已取得；跨平台/CI operator、真正 resume、adapter-specific compensation、ambiguous commit 处置与更广真实恢复仍 `U`，故不勾选。
- [ ] AC3 `T/U`：chat/plan/goal/workflow 已接到同一 `AgentLifecycle`/`SessionStore`/context manager，持久化 row+owner 后才授予 runtime authority；重启后的非终态行清空旧 RuntimeID 并保持 `recovery-required`。session recovery 的 backend-only dispatcher 仅接受 `discard`；external receipt recovery 是另一独立 dispatcher，仅接受 `manual-unknown`。Plan/Goal opaque runtime ID 在 receipt 持久化前归一为 logical lifecycle ID；completed lifecycle 的 pending receipt 仅在旧 runtime authority 已撤销后可处置。workflow attempt 不再依赖 TaskService 内存 map：durable usage ledger 是唯一 attempt SSOT，完成时只接受同 session 恰好一条 canonical `workflow/workflow.attempt` pending row；TaskService 重建仍复用原 UnitID，错误 kind/operation/provider、额外 pending row、poison 与并发 terminal 均 fail-closed，且 ledger reload 不恢复旧 runtime authority。snapshot schema/16 MiB/poison 与 workspace generation guard 均 fail-closed。workspace reset 的 `PersistenceNotPublished` 回滚保留旧 owner/runtime，`PersistencePublishedDurabilityUnknown` 则撤销二者，现有 T 回归；跨 window/caller owner proof、真正 resume、完整 workspace/domain setter rollback、stream privacy/retention 与真实恢复 I 仍缺。
- [ ] AC4 `T/U`：生产 runtime 已强制 `UsageTransactionSink`，每个 tool handler 执行前先 fsync `pending` receipt，终态按同一 `UnitID` 单调 upsert；unknown receipt、terminal→pending 回退与 divergent terminal replay fail-closed。external receipt handle 使用独立 per-config identity key；key 受跨进程锁保护，严格接受 64 hex，Unix 要求 `0600`。legacy-only ledger 可首次创建 key；一旦已有 external receipt 历史，key 缺失、损坏或权限过宽会 poison，禁止重建/轮换，新 external mutation 在 handler 副作用前拒绝。公开 usage/recovery 错误投影为固定 sentinel，不泄漏 configDir、路径、receipt ID 或底层持久化详情；完整生产矩阵与最终 I/P 复验仍未完成。
- [ ] AC5 `T/U`：`mcp_service.go` 已从 3017 行拆到当前 1004 行（`mcp_client/config/transport.go`），`git_service.go` 已从 3147 行拆到 416 行（`git_command/repository/diff/history/advanced.go`）；`internal/agentcore/headless_external_test.go` 以外部包、无 Wails/UI import 验证 catalog→capability→transactional meter→handler。Windows trusted CLI 子进程 I 子证据已取得，但跨平台/CI consumer I 证据仍缺。
- [ ] AC6 `T/P/U`：当前共享源码已取得固定本机 Wails 后的 `backend-gate.mjs` 9/9（含全仓 `go test ./... -count=1` 357.2s）、`frontend:check` 173/173 files 与 2765/2765 tests、ESLint（0 errors/1 个既有 warning）、vue-tsc、bindings 与 docs 全部 exit 0。最新 fresh `node scripts/packaged-e2e.mjs` exit `0`，manifest 记录 24 passed / 0 failed / 0 not-run、`artifactReused=false`、`sourceFingerprintStableAfterBuild=true`；artifact SHA-256 `86f89d0bace05b6660f867b24713f251448ebebd10db52b33461712d695b1cb9`，`build-inputs-v2` source fingerprint `91cabc5ecd4a0d9f70901b53537074fdbba0d21afcf68172cc26b596a7b3f148`、1023 files，completedAt `2026-08-18T13:33:33.636Z`。旧 packaged 与网络/并发 `EBUSY` 首败均保留为历史；本机 Windows `P` 不升级为跨平台/CI `R`。G33 AC1~AC5 尚未闭环，macOS/Linux packaged、真实跨平台/CI CLI 与 provider 仍为 `U`；当前 G33 锁图 `npm-audit-gate` 仍因 `nanoid <3.3.18` / `GHSA-2v37-7h3g-55p8` 报 1 个 high（exit 1），故 AC6 继续不勾选。

> **当前共享树门禁覆盖（2026-08-18）**：上述 AC6 行的 `86f89d…/91cabc…` 属旧代码态。当前 owner-map rollback 源码复跑的 gofmt/vet/build/full `go test ./... -count=1`（300.2s）、contract smoke、Wails pin 与 docs 均 exit 0；自动 `check-bindings` 仅因安装锁定 Wails 时 `proxy.golang.org` 不可达而 exit 1，显式本机 `wails3 v3.0.0-alpha2.111` 复核 bindings exit 0。`task frontend:check` 为 173/173 files、2765/2765 tests、ESLint 0 errors/1 existing warning、vue-tsc/bindings/docs exit 0。fresh packaged 24/24、`artifactReused=false`、artifact `83ee98f7ca00b5be4e7fd57703b04df60e80e397a322a23218127134e75ff662`、source `2ab1eb5cce63372cc4200f0693a96b95734c61af68d84ab4418609d5832b44e8`（1024 files）、completedAt `2026-08-18T14:16:37.283Z`，仅 Windows 本机 `P`；npm gate 仍 exit 1（唯一 high 为 `nanoid <3.3.18` / `GHSA-2v37-7h3g-55p8`，lock SHA `F1C0AED7…` 前后稳定）。随后在显式本机 Wails CLI 下完整 `node scripts/backend-gate.mjs` 9/9、exit 0（全量 Go test 353.1s）。因此 AC6 仍 `[ ]`，G33 仍 `0/6`。

> **§11 最新子切片覆盖（2026-08-19）**：`GenerateTitleWithAI`、`StartStream`、`StartAgentStream` 已接入同一 backend-owned operation admission；title/stream 的 Disabled、assigned endpoint/auth/model 与 unassigned global compatibility 定向及 `-race` 通过。`StartAgentStream` 当前按 `AIOpChat` 计量/授权，`AIOpAgent` 映射、缺失 renderer target 的 fail-closed、caller cancellation/worker retention、fallback identity、provider output bounds、frontend config race 与真实 provider 仍 `U`。本子切片不勾选 AC1~AC6，详见 §13.38；后续只推进 G33 stream target/lifecycle 边界。

> **§11 Agent 可用性覆盖（2026-08-20，P12-BUG-02）**：用户报告的“流式内容需退出重进才刷新、工具调用/执行过程不可见、native tool observation 被静默丢弃、独立 Agent 页面无法批准工具”已纳入 G33 的强制验收面；不新建平行工具系统。当前 renderer reactive stream、provider 明示 reasoning summary、工具时间线、turn barrier、跨窗口 durable handoff 与受控 loopback provider 的 packaged `read` 两轮已取得 `T/P` 子证据。隐藏 chain-of-thought 明确不展示或推断；产品展示的是 provider 主动提供的摘要、批准、参数、执行、结果、usage 与 observation。真实外部 provider、manual/mutating approval、restart ledger、真实双 WebView `I/P`、完整 MCP、跨平台/CI/CLI 仍 `U`，因此 P12-BUG-02 与 G33 均未关闭，AC 仍 `0/6`。详见 §12.8/§13.40。

**禁止**：新建平行工具系统替代现有实现；为"统一"破坏既有 fail-closed 语义（§4 修复项 H1~M7 优先于本 Goal 交付）。

**2026-08-16 file.write 子切片补充（AC 仍 0/6）**：typed workflow `file.write` 已接入统一 Agent catalog/capability，但仅作为 T 级子切片，不足以勾选 AC1/AC2。workflow 校验现在只接受 canonical root-relative `input.path` 与不超过 1 MiB 的 backend-owned `input.content`，拒绝 command/args/cwd、绝对路径、遍历和额外字段；renderer capability 参数固定为 `{}`。Prepare/approval/execute 会重载并校验 workflow source、content hash、workspace generation 与 target baseline，最终经既有 `workspace_edit_transaction.go` 和 `FileService.WriteFileIfUnchanged` CAS 写入，不触达 command runner；缺失目标仅在空 baseline 下安全创建，发布竞态和 baseline 冲突均 fail-closed。审计、公共执行错误和 usage 错误对 workspace pathname 做脱敏，文件内容/API key 不落审计。

- 改动文件：`services/workflow_service.go`、`services/agent_execution_workflow_file.go`、`services/agent_execution_workflow_skill.go`、`services/task_service.go`、`services/agent_execution_core.go`、`services/workspace_edit_transaction.go`、`services/file_save_integrity.go` 及对应 Go 测试；`frontend/src/stores/workflows.ts` 与 `frontend/src/stores/workflows.test.ts` 仅保留 catalog-only typed file-write runner。未修改 `.github/workflows`、`build/docker`、`package-lock` 或 Issue/PR/Release 治理元数据。
- T 证据：定向 file.write/CAS/审计脱敏/TaskService bridge 测试 exit 0；受影响 services 测试 exit 0；`node scripts/backend-gate.mjs` 9/9 exit 0；`task frontend:check` 173 files、2765 tests exit 0；bindings/docs exit 0。该切片当时的 Windows packaged manifest `2026-08-16T01:45:05.557Z` 为 24/24 passed，artifact SHA-256 `facaf467b692ececbbde53d40482bfc3f7126d2281abe55b0670ff0d8141a7ed`，source fingerprint `b9922c3238eae371166efc5fa03dfe5141ad977244cdf879212a33405465d0d3`；这是本机 P，不升级为跨平台/CI R，当前权威 manifest 见 §13.30。
- 首次失败与修复：发布竞态断言最初只接受 `ErrNotAllowed`，实际 CAS 正确返回 `ErrFileConflict`；公共错误脱敏测试和 write hook 仍期待旧的 `WriteFile` 路径。测试已改为同时断言冲突 sentinel、脱敏路径和 `WriteFileIfUnchanged`，未放宽安全行为。
- 未验证/下一步：`node scripts/npm-audit-gate.mjs` 当前树仍 exit 1（`nanoid <3.3.18` / `GHSA-2v37-7h3g-55p8` / 1 high），未改锁文件；真实 AI provider/MCP process、Git mutation、recovery/manual-disposition、cross-caller ownership、domain rollback、macOS/Linux packaged 与 CI/CLI consumer 仍 U。下一步只继续 G33 的下一未完成 AC1/AC2 子切片，AC1~AC6 全部保持 `[ ]`，H1 原始全平台范围仍存在。

---

## 12. 前端 Bug 侦察：浅色主题配色混叠（P12-BUG-01）

> 状态：**只侦察，未修复**（按用户要求仅写入本文档）。证据全部为静态代码定位，未运行真实打包验证。

### 12.1 现象

用户报告：在**浅色主题**下（任何浅色模式、任何界面）显示异常，**配色会混在一起**——不同层级的面板/弹窗/输入框等视觉边界消失，层次感塌陷。

### 12.2 根因（核心证据）

**亮色（默认）token 中，四个表面层级背景色完全相同**，导致亮色模式下整个 UI 的表面层级塌陷为单一白色：

- `frontend/src/assets/styles/main.css:48-51`（`@theme` 亮色默认块）：
  - `--color-bg-base: #ffffff`
  - `--color-bg-surface: #ffffff`
  - `--color-bg-elevated: #ffffff`
  - `--color-bg-overlay: #ffffff`
- 对比暗色覆盖块 `main.css:181-189`（`[data-mode="dark"]`）：`#1d1d1f / #272729 / #2a2a2c / #2a2a2c`——**四个层级各有区分**。
- `main.css:276-278` 的 `[data-mode="light"]` 块**只设置 `color-scheme: light`，没有为 `--color-bg-*` 提供任何亮色覆盖**——亮色模式完全依赖 `@theme` 默认值，而默认值恰好是"四合一白"。

**结论：暗色模式有完整的层级色板，亮色模式没有。** 这是"配色混在一起"的直接根因。

### 12.3 影响面（token 被谁消费）

- `var(--color-bg-elevated, …)`：**47 处**（`ContextChips.vue:128`、`ConversationSidebar.vue:592-744`、`InputComposer.vue:696-807`、`MessageList.vue:285`、`PlanPanel.vue:412-507` 等）——弹窗/浮动面板/输入框/气泡在亮色下全部与画布同色。
- `var(--color-bg-surface-container, …)`：**159 处**（侧栏、树视图、面板基底）——亮色下仅 `#f5f5f7` 与 `#ffffff` 的微弱差别。
- `var(--color-bg-overlay, …)`：1 处。
- **26 处**组件回退值是暗色系（如 `var(--color-bg-elevated, #252525)`、`MarketplacePanel.vue:1094` 的 `var(--color-bg-surface-container, #0d0d0d)`）：若任一 token 解析失败，亮色主题下会直接渲染出**深色块混在浅色界面**，进一步加重"配色混叠"观感。
- `--color-surface-tile-1/2/3`（`main.css:42-44`）= `#272729/#2a2a2c/#252527` 在 `@theme`（亮色默认）与暗色块中**都是深色**：产品瓦片在两种模式同色，属设计意图（Apple product-tile 风格），但会放大"浅色界面里混着深色块"的感知。

### 12.4 关联因素（次要）

- `main.css:258`：`html { color-scheme: light dark; }` 默认同时声明两种；`[data-mode="light"]`/`[data-mode="dark"]` 均会覆盖，方向正确，但依赖 `applyMode()`（`appActions.ts:102-110`）在启动与切换时正确设置 `data-mode`；若该属性缺失或值为 `system` 未解析，会退回浏览器默认渲染。
- `aiWindow.ts:71` 在 AI 伴随窗口**自己的 root** 上单独设置 `data-mode`，与主窗口 `document.documentElement` 两套体系；AI 窗口有独立主题选择器（`AiWindowThemePicker.vue:20-24` 的 apple-light/claude-dark 等），需分别验证。
- 设计语言叠加：Claude 亮色块（`main.css:695-728`）为 `bg-base/surface/overlay` 定义 `#faf9f5`、`bg-elevated` 定义 `#efe9de`，**有**层级区分；Apple（默认，无 `data-design-language` 属性）则完全依赖塌陷的 `@theme` 默认值——**Apple 设计语言下症状最重，Claude 语言下部分缓解但 overlay 与 surface 仍同色**（`#faf9f5`）。这与"任何浅色、任何界面都异常"的观察一致。

### 12.5 影响评估

| 项 | 值 |
|---|---|
| 严重度 | **高**（默认亮色模式整体视觉层次失效，直接影响可用性与观感） |
| 触发条件 | 切换到任意亮色模式（`theme=light` 或 `system`+系统亮色） |
| 影响范围 | 全局（Apple 设计语言最重；Claude 部分缓解） |
| 是否新建/复现 | 静态定位，未打包复现；建议修复后按 §12.7 验证 |

### 12.6 建议修复方向（未实施）

1. 为 `[data-mode="light"]` 补充完整亮色层级色板（如 `bg-base #ffffff` / `bg-surface #fafafc` / `bg-elevated #f5f5f7` / `bg-overlay rgba(0,0,0,0.5)` 或等效 Apple 亮色层次），而不是依赖 `@theme` 的四合一默认值。
2. 审计 26 处深色回退值：token 回退应使用同语义的**亮色**值或省略（让 CSS 继承），避免 token 解析失败时深色混入亮色界面。
3. 在 Claude 亮色块中补齐 `--color-bg-overlay` 与 `--color-bg-surface` 的区分。
4. 回归测试建议：新增主题 token 一致性静态测试（断言亮色模式下 `bg-base ≠ bg-elevated ≠ bg-overlay`，可复用 `bindings_runtime_surface_test.go` 的 AST/静态检查风格）；前端快照测试覆盖亮色/暗色两模式的布局组件。

### 12.7 验证方式（修复后）

- 切换 apple/light、claude/light、system(亮) 三种亮色模式，逐一检查：弹窗（ModalOverlay）、设置面板浮层、AI 聊天气泡/输入框（bg-elevated 消费者）、资源管理器（surface-container 消费者）是否仍与画布同色。
- 对照暗色模式（`main.css:181-189` 四层级色板）验证层级差异在亮色下同样存在。
- 运行 `npm run lint`、`npx vitest run` 相关主题测试与 `frontend:check` 门禁。

*本节为侦察记录，未做任何代码改动；修复请作为独立任务按 prompt-11 §0 纪律补测试后执行。*

### 12.8 P12-BUG-02：AI/Agent 对话、流式与工具执行不可用（重大，进行中）

**用户现象**：AI 输出不能稳定流式刷新；对话要退出重进才出现；Agent 无法可靠调用工具、等待批准、显示执行过程或把 observation 送入下一模型轮次。验收目标是达到 Codex/Claude Code 类工具的日常可用闭环，同时保持当前 UI 风格；可研究 OpenCode、pi、Orca 的功能与交互，但只能做 clean-room 行为借鉴，任何代码/资产复用必须先核对许可证、来源与归属，禁止无来源复制。

**当前状态（2026-08-20）**：缺口已显著变化但仍未关闭。

- [x] `T/P`：首个/后续 chunk 更新已挂载的 Vue reactive message；无需退出重进。只展示 provider 明示 reasoning summary，不暴露或推断隐藏 chain-of-thought。
- [x] `T/P`：native/fence tool call 经批准 -> backend capability -> 执行 -> usage/result -> observation -> 下一 provider turn 的串行 barrier；独立页面与嵌入面板显示风险、参数、状态和结果。
- [x] `T`：跨窗口 target 使用 durable handoff、generation/ACK 与失败保留；已有挂载组件无需 remount 的回归。真实双 WebView 运输、进程/窗口重启仍需 `I/P`。
- [x] `P`：Windows packaged 受控 loopback provider 的真实 `read` 两轮，包含非空 UnitID、同 session terminal usage、approval 顺序、FileService 磁盘 observation 与恰好两次 provider 请求。
- [ ] `I/P/U`：真实 OpenAI/Anthropic/兼容 provider，manual approval、write/run/search/MCP/Skill/Git mutation、多轮取消/重试/恢复、provider usage、重启 ledger 与跨平台矩阵。
- [ ] `T/I/U`：冻结 workflow AI primary+fallback identity；限制 provider body/SSE/tool count/arguments/稀疏 index/总 deadline；inline completion 默认 opt-in 且只发送相对路径；MCP process/env/schema/session/pagination 与 renderer surface 安全边界闭环。
- [ ] `T/I/P`：在原设计语言内完善 motion/glow、长任务阶段、失败重试与恢复入口；必须支持 `prefers-reduced-motion`、键盘/屏幕阅读器、窄屏和无重叠布局，不以视觉效果替代状态语义。

**执行顺序**：继续作为唯一 Goal G33 的 usability overlay，按“真实 provider/输出预算 -> manual 与 mutation 工具轮次 -> durable restart/recovery -> 完整 MCP/Skill/Git -> 跨平台 packaged/CI”逐切片推进；每个切片先补失败测试并按 S/T/I/P/R/U 回写。P12-BUG-02 全部未勾选项闭合前不得宣称 Agent 已达到生产可用。

---

*本文件为审查结论文档，同时承载 §11 长期发展 Goal 定义（P12-G28~G33）与 §12 前端/Agent Bug 侦察（P12-BUG-01~02）。作为续作输入时请与 prompt-9/prompt-11 的 Goal/AC 台账配合；任何修复必须按 prompt-11 §0 规则补测试、回写证据，且不得把 §11 的 AC 勾选与 prompt-9/prompt-11 既有 Goal 混淆。*

---

# §13 会话交接断点（2026-08-16，AI 续作交接）

> 本节由 2026-08-14 会话写入，并持续复核/推进 G33 至 2026-08-17。所有证据均以实际执行结果为准；**没有勾选的项一律不得视为完成**。旧失败日志保留为历史首次失败，不得反向覆盖当前 manifest，也不得把单平台 P 级扩大成跨平台/R 级。当前权威 packaged-e2e 以本节 §13.30 记录的 2026-08-17 manifest 为准，并须同时保留此前失败记录。

## 13.1 本会话执行范围与总状态

- 按用户指令执行顺序：现状复核（六 Goal + BUG-01）→ 安全前置 H1~H4/M3 → G33。**当前唯一推进 Goal 为 G33，状态进行中、AC 0/6**；G28~G32 与 P12-BUG-01 未开始，未并行推进主体实现。
- 已做：现状复核（§13.2）+ 安全修复 H1~H4/M3 的代码与定向测试（§13.3）+ H1/H2/H3 遗留验证复核 + 全量 backend/frontend/bindings/docs 门禁 + 真实 Windows packaged 24/24 复跑；G33 已落地 headless core、统一 catalog/capability/lifecycle/meter 基础，并完成 AC2 workspace transaction、external preallocated receipt/compensation T 级子切片、AC4 durable usage receipt 子切片，以及 AC3 durable owner/restart fail-closed 与 durable workflow attempt T 级子切片。
- 未做：P12-BUG-01、G28~G32；G33 各 AC 尚未全闭环；H1 的 Windows 核心 junction 交换已有 `I`，但 macOS、RevealInOS 与 CAS 最终跨平台闭环仍为 `U`。
- 工作树：存在**本会话之前**的用户改动（build-msi.ps1、build-windows.ps1、FlameGraph.vue、TerminalPanel.vue、TerminalSplitPane.vue、go.mod、debug_launch.go、shell_unix.go、shell_windows.go、terminal_service.go），本会话未触碰；后续 AI 不得覆盖或回滚。

## 13.2 现状复核结论（2026-08-14，与 §11/§12 描述一致）

| 项 | 结论 | 关键证据 |
|---|---|---|
| G28 计费 | **缺口仍存在**（骨架+UI 已存在：`ai_permission_service.go` RecordUsage/GetUsageSummary 定义齐全，`ModelPermissionSection.vue` 已有仪表盘，但 **RecordUsage 生产代码零调用**，数据管道断裂；无 provider/session/project 维度、无导出、无强制预算） | `ai_permission_service.go:311-321,323-380`、`ai_service.go:645`（仅注释）、`ModelPermissionSection.vue:230-342` |
| G29 编排 | **缺口仍存在但已变化**（plan 工具不存在；`defaultGoalExecutor` 仍 prototype + 默认禁用；后端 `WorkflowEngine` 未成为生产 SSOT；`WorkflowStepAI` 无生产 executor。已变化：前端 runner 的 command/file.read/git.status/MCP/Skill activation 子集改走 G33 backend catalog/capability，不再把 typed step 当 shell command；这不等于 AI 规划闭环） | `workflow_engine.go:236-244`、`executor_adapters.go:190-245`、`ai_goal_service.go:443-450`、`services/agent_execution_workflow_skill.go`、`frontend/src/stores/workflows.ts`、`PlanPanel.vue:47-62` |
| G30 Git | **缺口仍存在**（ThreeWayMerge 仍是简化行对齐，`diff_service.go:127-128` 注释自认非 diff3；无 hunk 级暂存；AI 冲突解决无；diff/log/graph 无性能基准。已变化：AI commit message 预设骨架存在未接入 Git 面板；Git 写入经 pathsec 双层校验） | `diff_service.go:122-187`、`GitPanel.vue:1015-1056`、`git_service.go:409-424,702-719` |
| G31 diff-first | **缺口仍存在**（DiffView.vue 仍只读；AI 写入审查仍为聊天卡片截断文本；新增 `MergeEditor.vue` 1571 行已注册 feature `git.merge-editor` 但**未接线到任何生产 UI**；diff 无虚拟滚动） | `DiffView.vue:73-81`、`AiChatPanel.vue:1006-1077`、`featureRegistry.ts:180-191` |
| G32 Skill | **缺口仍存在**（`SkillScopeGlobal` 生产零使用；无内置 skill 目录；后端 MatchTriggers/MergeSystemPrompts/AllowedToolsForSkills 无生产调用点。已变化：前端 skill 浏览器 + @skill 激活已实现） | `skills_service.go:38-42,232-315`、`SkillsSection.vue`、`InputComposer.vue:69,352-360` |
| G33 执行核心 | **缺口已变化但仍存在（进行中，AC 0/6）**：已有无 UI 的 `internal/agentcore`、单一 builtin/MCP/workflow/skill catalog、统一 capability/epoch/audit、共享 lifecycle/context/meter；typed workflow file read/write、Git status、MCP、Skill 与 AI adapter 均走 backend-owned catalog/capability。SessionStore/owner 与 durable usage receipt 已 fail-closed 持久化；session `discard` 与 external receipt `manual-unknown` 是两个独立 backend-only dispatcher，均使用 opaque handle 且不注册 Wails。external receipt identity key 跨进程锁定并严格校验；一旦存在 external receipt 历史，key 缺失/损坏/宽权限会 poison，禁止重建或轮换。Plan/Goal runtime ID 在持久化前归一为 logical lifecycle ID，completed lifecycle 仅在旧 runtime authority 已撤销后允许处置 pending receipt。Windows 本机真实 MCP stdio 为 I 子证据；真实 operator/CLI、真正 resume、adapter-specific compensation、跨 caller/domain rollback、真实 provider/CI 与跨平台 packaged 仍 U。 | `internal/agentcore/registry.go`、`internal/agentcore/runtime.go`、`internal/agentcore/session_persistence.go`、`services/agent_external_receipt_recovery.go`、`services/agent_external_receipt_recovery_dispatcher.go`、`services/agent_lifecycle.go`、`services/agent_execution_mcp.go`、§11 G33 进行中快照 |
| P12-BUG-01 | **缺口仍存在**（未修复；§12 根因确认：`main.css:48-51` 亮色四层级全 `#ffffff`，`[data-mode="light"]` 块只设 color-scheme，26 处深色回退值） | §12 全文 |

## 13.3 安全修复逐项状态（H1~H4、M3）

### H1（FileService symlink TOCTOU）—— **代码已变化，Linux T 与 Windows junction I 级证据成立；macOS/Reveal/CAS 保持 U，不得宣称闭环**

- 实现方案：改用 **Go 1.25 标准库 `os.Root`**（`services/file_service_secure_root.go` 新增，`secureWorkspace` 引用计数 + 根句柄 + generation/lease；`setWorkspaceRoot(s)` 时绑定并原子发布，失败全回滚）。FileService 全部 renderer 文件方法已迁移：ReadFile（单句柄 `Root.Open` + File.Stat + LimitReader 上限）、WriteFile、WriteFileIfUnchanged（同 capability 下 baseline+CAS 发布）、CreateFile、CreateDirectory、DeletePath、RenamePath（跨根 fail-closed `ErrNotAllowed`）、ListDirectory、ListAllFiles（`fs.WalkDir(root.FS())`）；`atomic_write.go` 改为 Root 内 temp+Sync+Chmod+identity+Rename，并恢复深层父目录自动创建语义。
- 测试：`file_service_test.go` 新增 parent/root swap 全操作矩阵、leaf 替换大文件（单句柄绑定）、深层 WriteFile、workspace switch retire、multi-root rename、cross-root fail-closed；修复了旧测试 err 被覆盖的 bug。
- 验证：Linux 定向与 `-race`、Windows 交叉编译均 exit 0；2026-08-15 Windows 11/amd64 本机运行 `go test ./services -run "TestFileService.*Junction|TestSecureWorkspace|TestFileService.*RootIdentity|TestFileService.*WorkspaceSwitch" -race -count=1 -v` **exit 0**，无需 symlink privilege 的真实 `mklink /J` 父目录交换矩阵 9/9（ReadFile、WriteFile、CreateFile、CreateDirectory、DeletePath、RenamePath、ListDirectory、ListAllFiles、WriteFileIfUnchanged）；同机 renderer 文件方法定向 `-race` 复跑 exit 0。普通 Windows symlink 用例因当前进程无创建符号链接权限而 skip，未伪造通过。
- **复核 verdict / 未闭环**：Linux 核心读写删改保持 **T**；Windows NTFS junction 交换属于真实 OS 文件系统机制集成证据 **I**。macOS 仍因 Wails `pkg/mac` build constraints/缺真实宿主为 **U**。`RevealInOS` 只能把 pathname 交给 explorer/open/xdg-open，不能传递 `os.Root` capability；它不读写文件内容，但阻止宣称“FileService 全暴露面完全句柄化”。temp identity 后按名称 `Rename` 与 baseline 后提交的持续换名/CAS 平台证明仍为 **U**。综合结论仍是“代码已变化、H1 原始全平台范围未闭环”。

### H2（Windows .cmd/.bat 命令注入）—— **代码完成，真实 cmd.exe I 级证据成立**

- 实现：`services/exec_cmd.go` 新增 `cmdShimLine`（`/d /v:off /s /c` + 双阶段 cmd/batch token escaping）+ `setCmdLine`（Windows `SysProcAttr.CmdLine`，避免 os/exec 二次 quoting）；保留绝对 shim 路径、context cancel、hideConsoleWindow。
- 测试：`cmd_escape_test.go`（round-trip 覆盖 `& | < > % ! ^`、引号、空参、Unicode）、`exec_cmd_windows_test.go`（真实 cmd.exe probe：十六进制无损 argv + 注入 payload 不产生 marker）。
- 验证：Linux `go test ./services -run 'TestEscapeCmdArgRoundTrip|TestCommand'` exit 0；`GOOS=windows CGO=0 go test -c` exit 0；Windows 11/amd64 宿主以 Go 1.25.0 运行 `go test ./services -run '^(TestEscapeCmdArgRoundTrip|TestCommandCmdShimRoundTrip)$' -count=1 -v` **exit 0**，真实 `cmd.exe` + 临时 `.cmd` 覆盖 `Command`/`CommandContext`，argv 字节往返且 marker 未生成。归档：`build/e2e-evidence/p12-h2/windows-cmd-shim-evidence.json`（`evidenceLevel=I`）与 `windows-cmd-shim-test.log` SHA-256 `ce504d1d88b099f1c399a0eac83e90e5b35892ef2f664d1a909bddda01e4d96f`；这不是 packaged P 级证据。

### H3（VSIX 同源 SHA-256 被标为签名验证）—— **代码完成，前后端 T 级验证成立**

- 后端：`extension_security_service.go` 新增 `IntegrityChecked`（持久化 `integrityChecked` + `UnmarshalJSON` 迁移旧 `verified`）、`VerifyExtensionIntegrity`、`ErrIntegrityMismatch`/`ErrIntegrityNotChecked`（旧名保留为兼容别名）；启用门禁读 `IntegrityChecked`，未通过 SHA-256 仍 fail-closed；`marketplace_service.go` 文案全部改为 integrity check。
- 前端：`extensionSecurity.ts` 用 `integrityChecked`（旧 `verified` 回退）、三种 locale `extPerm.unverified` 改 SHA-256 完整性文案、`MarketplacePanel.vue`、`ExtensionPermissionDialog.vue`、`api/extensions.ts`、`vscodeExtensionActivation.ts`、新增 `extensionIntegrityCopy.test.ts`。
- 验证：`go test ./services -run 'TestExtensionSecurity|TestMarketplace'` **exit 0**；2026-08-15 再跑 `npm.cmd exec vitest -- run src/stores/extensionSecurity.test.ts src/components/modals/ExtensionPermissionDialog.test.ts src/lib/extensionIntegrityCopy.test.ts` **3 files / 41 tests、exit 0**，同代码态 `vue-tsc --noEmit` **exit 0**。首次直接调用 `npm` 被 PowerShell 禁止 `npm.ps1`（测试未启动，exit 1），改用 `npm.cmd` 后通过。前端启用路径只读取 `info.integrityChecked`；`verified=true` 且 `integrityChecked=false` 的 store/dialog/activation 用例均 fail-closed，兼容别名不能绕过启用门禁。

### H4（插件 sandbox 可关闭）—— **代码完成，T 级证据成立**

- 实现：`pluginRegistry.ts` 生产构建强制 sandbox（`import.meta.env.PROD` + 测试注入 `SandboxModeOptions.production`）；生产激活前 `ensureSandboxHost`、跳过模块缓存/主线程 dynamic import；生产 teardown fail-closed 不回落 renderer import；`main.ts` 注释更新说明 renderer 设置仅 dev/test 可关。
- 测试：`pluginRegistry.test.ts` 新增「生产模式设置禁用仍强制启用」「非生产可显式关闭」两用例。
- 验证（本会话实际执行）：`npm exec vitest run --pool=vmThreads src/lib/pluginRegistry.test.ts src/main.test.ts` **exit 0 / 114 项通过**；fixer 报告 `vue-tsc --noEmit` exit 0（超时重跑后）。

### M3（secret 加密失败静默降级明文）—— **代码完成，T 级证据成立**

- 实现：`settings_service.go` `saveSettingsLocked` 加密失败 **fail-closed**（legacy key 与 provider key 均返回错误，不再写 `plain:` 前缀）；新增 `encryptSecretForSettingsForTest` 注入钩子。
- 测试：`settings_service_test.go` 新增 `TestSettingsService_SaveSettingsRejectsEncryptionFailure`（legacy/provider 双用例：错误包装 + 文件不落盘）。
- 验证（本会话实际执行）：`go test ./services -run 'TestSettingsService_SaveSettingsRejectsEncryptionFailure'` **exit 0**。

> 注：`settings_service.go` 的 M3 修改与 `cmd_escape_test.go`、`extensionIntegrityCopy.test.ts` 在**本会话开始时已存在于工作树**（上一个会话遗留），本会话对其验证并纳入证据；H1/H2/H3/H4 实现为本会话车道完成。

## 13.4 下个 AI 的续作清单（按序）

1. **H1 原始范围保持未闭环**：Linux T、Windows junction I 成立；macOS/RevealInOS/temp identity 与 CAS 竞争仍按 §13.3 保持 `U`，不得因本地门禁通过而关闭。
2. **历史门禁基线（2026-08-15，G33 receipt 改动前）**：`node scripts/backend-gate.mjs` exit 0；`task frontend:check` 173 files / 2748 tests、ESLint、vue-tsc、bindings、docs 全部 exit 0；独立 `check-bindings`/`check-doc-links` exit 0；真实 `node scripts/packaged-e2e.mjs` 24/24、exit 0。manifest artifact SHA-256 `d429e84e9d52b6c3229d814e6b2ea6342c46a64f62254a39b9bbbb0aef343f30`，source fingerprint `a78faf13914b0835e34f27747d4d092422533135e1dcd22089bc8149b916f155`，completedAt `2026-08-15T07:02:21.831Z`。receipt 改动后 `agentcore` 全量/`-race`、services 定向与锁定 bindings 已通过；最终 AC6 必须重跑全量与 packaged，不能沿用本基线冒充当前结果。
3. **继续 G33（统一 Agent 执行核心）**：当前唯一 Goal；session `discard`、external receipt `manual-unknown` 与 durable workflow attempt 已取得 backend-only `T` 子证据，但真实 operator/CLI、真正 resume、adapter-specific compensation、ambiguous commit 处置、跨 caller owner proof、domain rollback 与 stream privacy/retention 仍 `U`。下一步从这些未闭环边界或其余真实 adapter/consumer `I` 中选择一个子切片；AC 全勾选并附 T/I 后才可宣称完成。
4. **P12-BUG-01**（浅色主题）：可作独立修复在任何 Goal 间隙做（补 `[data-mode="light"]` 亮色层级色板 + 清理 26 处深色回退 + token 一致性静态测试）。
5. **回写**：当前 durable workflow attempt 子切片已同步 prompt-12 §11/§13.30、prompt-9 §8 与 prompt-11 §9；后续每个 G33 子切片继续即时同步，不能等到会话末凭记忆补写。

## 13.5 本会话遗留纪律提醒

- 不得删除测试保绿、不得放宽安全断言、不得用 any/类型压制、不提交 secret、不擅自 commit/push/tag。
- AC 未全勾选不得宣称 Goal 完成；H1 在 macOS、RevealInOS 与残余 CAS 平台验证前保持「代码已变化、未闭环」。
- 打包级证据以最新 manifest 为准（当前 Windows 24/24 通过；代码变化后即需重跑）；mock/contract 不得升级为 I/P/R。

## 13.6 G33 当前子切片交付（2026-08-15，Goal 仍进行中）

- 改动：`internal/agentcore/runtime.go` 增加 `UsageTransactionSink`/`UsageReceipt` 与 production-required meter contract；tool execution 在 handler 前持久化 pending receipt、handler 后完成同一 `UnitID`。`services/agent_lifecycle.go`/`ai_permission_service.go` 实现追加式两阶段账本和逻辑 upsert，账本失败不先发布 stream/checkpoint；chat/workflow 的 terminal state 不再在 meter 失败时误报完成。AC2 同时以 `WorkspaceTransactionHandler` 锁住 `MutationWorkspaceTransaction` 的真实入口。
- 测试：`runtime_test.go` 覆盖 meter 缺失/错误 contract、begin 失败零副作用、begin→execute→complete 顺序、complete 失败保留 pending receipt；`agent_lifecycle_test.go` 以不可写账本证明 builtin write 不落盘，并验证磁盘 pending/terminal 两行重载为一个终态逻辑单元；`headless_external_test.go` 通过 exported API 验证无 Wails/UI 的 transactional meter consumer。
- 验证：`go test ./internal/agentcore -count=1` exit 0；`go test -race ./internal/agentcore -count=1` exit 0；lifecycle/Task/AI/AgentExecutionCore services 定向组 exit 0；`node scripts/generate-bindings.mjs` 与 `node scripts/check-bindings.mjs` exit 0（锁定 Wails `v3.0.0-alpha2.111`，47 modules/55 files，ByName=0）。
- 首次失败：先运行 `update-bindings-manifest --accept-export-surface` 只更新 whitelist，`check-bindings` 随后正确报告 `models.ts` drift；再运行锁定生成器同步真实 binding tree 后通过。`git diff --check` 仍被用户既有 `build-msi.ps1` EOF 空行阻塞，本轮未修改该用户文件，不能记录为通过。
- 未验证：AC1 的非 command workflow adapters/runner SSOT、AC2 外部副作用补偿、AC3 完整 ownership/I、AC4 全生产矩阵、AC5 真实 CLI/CI、AC6 receipt 后全量与 packaged 均未完成；所有 AC 继续 `[ ]`。未 commit/push/tag/release。

## 13.7 G33 AC2 external mutation 子切片交付（2026-08-15，Goal 仍进行中）

- 改动：`internal/agentcore/runtime.go` 增加 `ExternalMutationTransactionHandler`，强制 external handler 先以 side-effect-free `BeginExternalMutation` 分配 receipt，再把 receipt ID/可逆性/`pending` 状态写入 durable usage ledger，最后才调用 `ExecuteExternalTransactionWithReceipt`。handler/terminal meter 失败只补偿一次；cleanup 使用 `context.WithoutCancel` + 30 秒上限。`run`、workflow command、MCP 明确不可逆；skill activation 记录先前 approval/session binding 并仅回滚本次变化。`ai_permission_service.go` 重载时按 `UnitID` 恢复终态；usage/audit/binding 只投影 receipt ID、可逆性与补偿状态，私有 receipt metadata 不落盘、不进 renderer。
- 测试：`runtime_test.go` 覆盖缺事务接口/空 receipt 在副作用前拒绝、pending receipt 先于 handler、handler/terminal 双失败只补偿一次、不可逆状态、补偿失败审计、取消请求不取消 cleanup，以及 terminal 失败后同 UnitID 幂等重试；`agent_execution_core_test.go` 用真实 production run adapter + 注入 terminal meter 失败证明 pending 先于 command capture、不可逆错误及一次重试后的 terminal 投影；`agent_execution_workflow_skill_test.go` 证明 skill terminal meter 失败会撤销本次 project approval 与 session binding；`agent_lifecycle_test.go`/`ai_permission_service_test.go` 证明 AIPermission JSONL 持久化 receipt 字段、重载逻辑 upsert、幂等 terminal 行且无 `externalReceiptMetadata`。
- 验证：`go test ./internal/agentcore -count=1` exit 0；`go test -race ./internal/agentcore -count=1` exit 0；services external adapter/production ledger 定向组及同组 `-race` exit 0；`git diff --check`（本切片文件）exit 0。
- 首次失败与修复：引入强 contract 后 builtin `run` 因缺 external transaction methods 使 services 初始化失败；四个生产 handler 接线后通过。首次 missing-receipt 旧测试仍预期“副作用后拒绝”，现已收紧为 receipt 无效时 meter/handler 均零调用。并修复 handler error + terminal meter error 会重复补偿的真实缺陷。privacy schema 收窄后测试仍引用已删除 metadata 字段而编译失败，测试同步到 bounded schema 后通过。
- AC/未验证：AC2 保持 `[ ] T/U`；workspace/external transaction T 级边界已变化。terminal `CompleteUsage` 失败后先在本次 Execute 内补偿一次并以同一 UnitID 最多幂等重试一次；重试仍失败时 JSONL 保留 pending，补偿结果仅在返回 record/best-effort audit，尚无后续 durable terminal retry；receipt bounded 字段也不包含恢复所需的私有 rollback state。进程重启后 pending external receipt 的恢复/人工处置 dispatcher、补偿幂等跨重试与 ambiguous commit 处置仍 `U`。AC1、AC3~AC6 状态不变；最终 bindings/full gates/packaged 尚未重跑。未 commit/push/tag/release。

## 13.8 G33 AC3 workspace ownership / observation 子切片交付（2026-08-15，Goal 仍进行中）

- 改动：`internal/agentcore/runtime.go` 的 `UnregisterAllSessions` 同时清空 capability map；`internal/agentcore/session.go` 增加 workspace reset 的 `CloseAll` 与按 UnitID 原子去重的 usage observation；`services/agent_lifecycle.go` 在 workspace incarnation 变更时关闭 lifecycle rows、清空 opaque owner map/skills，并以 logical session 写 observation；`AgentService.CreateAgentSession` 拒绝 renderer 直接创建 plan/goal authority；`frontend/src/stores/agent.ts` 将 backend chat session 绑定 workspace generation，跨切换自动旋转/拒绝迟到创建结果；`frontend/src/api/automation.ts` 保留 bounded receipt 投影字段。
- 测试：`runtime_test.go` 证明 workspace reset 释放未消费 capability；`agent_lifecycle_test.go` 证明 opaque plan owner 的 usage event/checkpoint 写入 logical session、重复 UnitID 不重复观察、workspace A→B 终结 chat/plan rows 并清 owner；`agent_execution_core_test.go` 证明 plan/goal renderer session 创建 fail-closed；`frontend/src/stores/agent.test.ts` 107/107 覆盖 generation rotation、跨切换迟到 Promise 与显式 reset。
- 验证：`go test ./internal/agentcore -count=1` exit 0；services lifecycle/ownership/receipt 定向组及 `-race` exit 0；`npm.cmd exec vitest run src/stores/agent.test.ts` 107/107 exit 0。
- 首次失败与修复：workspace reset 首次只清 runtime，测试发现 lifecycle plan row 仍 running；加入 lifecycle `CloseAll` 与 owner map 清理。opaque usage 首次把 event/checkpoint 写到 runtime ID，测试失败后改为 logical ID；重复 receipt 首次产生重复 observation，改为 SessionStore 原子去重。前端首次默认 mock 暴露空 `createSession` 导致 4 个旧 fixture 进入未配置 Promise 分支，恢复“默认无 backend 方法、专项测试显式注入”后通过。
- AC/未验证：AC3 保持 `[ ] T/U`；workspace reset/domain-owned session/opaque observation T 级边界已变化。本子切片当时 SessionStore/owner metadata 仍仅内存，后续 durable owner/restart fail-closed 增量见 §13.9；进程重启恢复 dispatcher、跨 owner/window 校验、plan/goal/workflow domain 收敛与真实 workspace I 仍 `U`。AC2/AC4 仍 `[ ] T/U`（补偿跨重启、ambiguous commit 与完整生产矩阵未闭环）；AC1、AC5、AC6 不变。未 commit/push/tag/release。

## 13.9 G33 AC3 durable owner / restart fail-closed 子切片交付（2026-08-15，Goal 仍进行中）

- 改动：`internal/agentcore/session.go` 增加可选原子 persistence contract、durable `SessionOwner` 与 `recovery-required` 状态；重启加载会清除旧 runtime ID，所有 mutation 在 snapshot 保存失败时回滚内存。`internal/agentcore/session_persistence.go` 增加版本化原子 JSON snapshot、16 MiB 加载上限、结构校验与受支持平台 0600 保护。`services/agent_lifecycle.go`/`agent_execution_core.go`/`agent_service.go`/`main.go` 接线 process incarnation 与 config dir，chat/workflow 创建、domain begin、workspace reset 及惰性 usage 均执行 durable-before-authority；未知 bearer 不能直接注销 runtime session。`services/task_service.go` 改用 `BeginExisting` 复用已持久化 workflow row，并保持 pending/terminal receipt 的 `Operation` 身份不变。
- 测试：新增 session snapshot/restart/rollback、workspace persistence rollback 与惰性 usage persistence failure 用例；后者阻断 snapshot 后断言 runtime authority、内存 row 与 usage ledger 均未发布。既有四个 workflow lifecycle 测试覆盖 completed session 不可重新授权、unowned/non-running 拒绝、running approval 保留 owner row 与单 run 聚合。
- 验证：`go test ./internal/agentcore -count=1` exit 0；`go test -race ./internal/agentcore -count=1` exit 0；五个 workflow/lazy persistence 定向测试 exit 0；10 个 lifecycle/workflow 定向测试加 `-race` exit 0。
- 首次失败与修复：`CreateAgentSession("workflow")` 已创建 durable row，TaskService 再调用 `Begin` 导致四个测试以 `ErrSessionExists` 失败，改为 `BeginExisting`。复跑随后暴露 pending `Operation="workflow.attempt"` 与 terminal `workflow.completed/failed` 的 receipt identity 漂移；未放宽账本断言，改为同一 execution unit 始终使用 `workflow.attempt`，完成/失败只由 `Success`/`Error` 表达，最终五测试 exit 0。
- AC/未验证：AC3 保持 `[ ] T/U`，G33 仍 AC 0/6。当前只有 restart fail-closed，不存在可信 recovery/manual-disposition dispatcher；session ID 仍是 bearer，缺跨 window/caller owner proof；project 后续 setter/save 失败无法恢复已关闭 lifecycle rows；workflow usage attempt 仍仅内存。本子切片时 snapshot 含完整 stream 内容且写入未设置总上限；后续 schema/size 加固见 §13.10，stream privacy/retention 仍未闭环。无真实 restart/workspace-switch I 证据。AC1、AC2、AC4~AC6 状态不变，最新改动后 bindings/full gates/packaged 未复跑。未 commit/push/tag/release。

## 13.10 G33 AC3 lifecycle snapshot schema / size 子切片交付（2026-08-15，Goal 仍进行中）

- 改动：`internal/agentcore/session_persistence.go` 改为同一文件句柄检查权限并以 `LimitReader(max+1)` 读取；JSON decoder 启用 `DisallowUnknownFields` 并拒绝尾随值。写入经计数 writer 硬限 16 MiB，超限只留下随后删除的 temp，不执行 replace。`internal/agentcore/session.go` 不再把缺失 status 静默迁移为 failed，并拒绝无 domain/incarnation owner 与未知 recovery 状态。
- 测试：`session_test.go` 新增未知顶层/row 字段、缺失 status、无效 owner、尾随 JSON 的失败矩阵；先保存合法 snapshot，再尝试超限 stream，断言返回错误且目标文件字节保持不变。
- 首次失败与修复：定向测试首次 exit 1，四类不可信 shape 被接受、超限 snapshot 成功替换；尾随第二 JSON 值已由旧实现拒绝。实现严格 decoder、结构校验与写上限后，同一定向测试 exit 0，未放宽断言。
- 验证：snapshot 定向测试 exit 0；`go test ./internal/agentcore -count=1` exit 0；`go test -race ./internal/agentcore -count=1` exit 0；8 个 services durable owner/restart/workflow 定向测试 exit 0。
- AC/未验证：AC3 仍 `[ ] T/U`，G33 仍 AC 0/6。16 MiB 解决的是 snapshot 总量与加载内存边界，不等于 stream 内容隐私/retention；recovery dispatcher、cross-caller proof、domain rollback、workflow attempt persistence 与真实 I 仍缺。AC1、AC2、AC4~AC6 不变；bindings/full gates/packaged 尚未复跑。未 commit/push/tag/release。

## 13.11 G33 AC1 typed workflow file-read 子切片交付（2026-08-15，Goal 仍进行中）

- 复核结论：G33 缺口继续缩小但仍存在，状态保持进行中、AC 0/6；本轮只推进 AC1 的第一个 typed adapter，G28~G32 与 P12-BUG-01 未开始。
- 改动：`WorkflowStep` 增加 `tool/input` DTO；backend loader 对未知/畸形 step 整体 fail-closed，不再静默执行有效子集。`file.read` 只接受唯一 `input.path`、拒绝 command/args/cwd、绝对路径/drive/UNC/parent traversal/额外 root，并在 ToolDef 中保存 canonical root-relative path 与 `adapter=file.read`。TaskService 从 session-owned workflow/step 匹配唯一 ToolDef，使用刚读取的 catalog revision 和 catalog 参数签发 capability；执行复用 builtin read -> `FileService.ReadFile`，不触达 command runner。前端 DTO 双向转换 `tool/input`，runner 仅接受严格 file/read shape；catalog API 缺失时 file step 在 dev/production 均拒绝，旧 command approval 仅保留为 command 测试兼容 adapter。
- 测试：后端新增 valid/invalid file adapter 矩阵、loader 保留合法 typed step、catalog 发布/真实 FileService 内容 observation、renderer path redirect/额外 root 零触达、跨 step token 重放烧毁、TaskService observation bridge；前端新增 DTO round-trip 与 file runner catalog-only 用例。`verified=true` 且 `integrityChecked=false` 的 H3 启用门禁专项也再次复核。
- 验证：首次 `go test` 因 `workflowStepCapabilityArguments` 未实现而编译失败（exit 1），实现严格 adapter mapping 后定向 Go 组 exit 0；最终相关 services 定向组 exit 0，四个安全用例 `-race` exit 0。workflow/DTO Vitest **2 files / 160 tests** exit 0；H3 Vitest **3 files / 41 tests** exit 0；ESLint（5 个本轮 TS 文件）与 `vue-tsc --noEmit` exit 0。首次 `node scripts/check-bindings.mjs` 正确报告 `models.ts`/`taskservice.ts` drift（exit 1）；随后 `update-bindings-manifest --accept-export-surface`、锁定 Wails `v3.0.0-alpha2.111` 完整生成、ownership、16/16 binding contract、最终 `check-bindings` 均 exit 0，未手工猜 ID。切片文件 `git diff --check` exit 0。
- 安全与数据：renderer 不提供 root、绝对路径或预解析路径作为授权；catalog revision、arguments hash、budget epoch、workspace generation、session 与一次性 token 仍由统一 runtime 二次验证。错误 path/额外字段/跨 step replay 在 FileService 前拒绝；成功读取沿 H1 `os.Root` capability，observation 复用既有 8000 字符上限，usage/audit 不记录文件内容或原始 arguments。
- AC/未验证：AC1 仍 `[ ] T/I/U`，因为 AI/Git、file mutation/其余 file、MCP/Skill workflow adapters 与真实 MCP process -> renderer observation `I` 仍缺；AC2~AC5 状态不变。AC6 仍 `[ ] T/P/U`：当前权威 packaged manifest 是本切片之前的 Windows 24/24 passed，不能作为本代码态证据；backend/frontend 全量和新的 packaged 尚未复跑。未 commit/push/tag/release。

## 13.12 G33 AC1 typed workflow MCP / Git status 子切片交付（2026-08-16，Goal 仍进行中）

- 复核结论：G33 缺口继续缩小但仍存在，状态保持进行中、AC 0/6；本轮只推进 AC1 的 typed adapter，G28~G32 与 P12-BUG-01 未开始。H1 仍是 Linux T/Windows junction I、macOS/Reveal/CAS U；H2/H3/H4/M3 在各自修复范围内已不存在。
- 改动：`workflow_service.go` 对 `mcp` 与零参数 `git/status` shape 整体 fail-closed，拒绝 hidden command/args/cwd、空 MCP tool/key 与 renderer repo/root 字段。`agent_execution_workflow_skill.go` 把 workflow-owned MCP input hash/delegated ToolDef 和只读 Git status 发布到同一 registry；MCP 复用既有 `mcp.call` approval/external receipt，Git 只从当前 workspace 调用 `GitService.GetStatus`。`task_service.go` 只按 session-owned workflow/step 与刚读取的 catalog revision 签发 capability，typed result 不走 command runner。`frontend/src/stores/workflows.ts` 只接受严格 file/read、git/status、MCP shape，catalog API 缺失时无 command fallback；`main.go` 通过 trusted variadic wiring 注入现有 GitService，未建立平行系统。
- 安全修复：定向测试首次证明 MCP capability 审批后 delegated tool 消失仍会触达 `tools/call`。根因是 tool availability 只在 `Prepare`（capability issuance）检查；现于 `BeginExternalMutation` 分配 receipt 前重新列举工具并核对唯一 tool name 与 `normalizeMCPAgentSchema` 后的 schema hash，变化时零 `tools/call`。MCP 与 file observation 统一经 `boundAgentObservation` 硬限 8,000 bytes；usage/audit 不记录原始 workflow input、MCP 内容、secret 或完整 repo 绝对路径。
- 测试：新增 MCP valid/invalid adapter、catalog-owned input/metadata redaction、TaskService workflow/step replay与 catalog revision 失效、审批后 tool disappearance、不可逆 receipt、8,000-byte observation；新增真实 go-git repository 的 workflow/TaskService status、renderer repo redirect 与 command-runner 零触达；前端新增 Git/MCP catalog-only 正向路径并保留 command 混入负例。
- 首次失败与修复：TaskService MCP test 首次缺 `encoding/json` 编译失败；补 wiring 后通过。tool disappearance fixture 随后真实暴露执行前未重验并出现第二次 `tools/call`，补 production guard；guard 首轮直接比较 server raw schema 与闭合 catalog schema导致合法请求误拒绝，统一归一化后通过。Git 首轮创建 workflow 后 ToolDef 缺失，定位到空 `input:{}` 被 `omitempty` 序列化为 nil；零参数 adapter 明确把 nil/空对象 canonical 为 `{}`，额外字段仍拒绝。完整 `git diff --check` 仍被既有 `build/scripts/build-msi.ps1` EOF 空行阻塞，本切片 scoped diff check exit 0，未修改该用户文件。
- 验证：MCP/TaskService/observation 四项 Go 定向与 Git/workflow/TaskService 定向均 exit 0；workflow/MCP/Git 安全组 `-race` exit 0；扩展 workflow services 定向组 exit 0。前端 `workflows.test.ts` **147/147**，workflow+DTO **2 files / 162 tests**，定向 ESLint 与 `vue-tsc --noEmit` 均 exit 0。当前代码态 `task frontend:check` 复跑 **173 files / 2758 tests**、ESLint（0 errors/1 个既有 warning）、vue-tsc、bindings/docs 全部 exit 0；首轮曾有 `extensionHost.test.ts` 同毫秒 terminal session 时序失败（2757/2758），单文件 107/107 与全量复跑均通过，未改该测试。`check-bindings` exit 0（锁定 Wails `v3.0.0-alpha2.111`、manifest/generated tree match、ByName=0），`check-doc-links` exit 0；未手调 binding ID。
- AC/未验证：AC1 仍 `[ ] T/I/U`，因为 AI、Git mutation/更多 Git、file mutation/其余 file、Skill adapter与真实 MCP 子进程 -> renderer observation `I` 未闭环；AC2~AC5 状态不变。AC6 仍 `[ ] T/P/U`：frontend 全量已绿；`backend-gate` 当前 exit 1，但本轮 `services` 全量自身通过（342.932s），红灯仅来自并行任务拥有且本任务禁止修改的 `build/docker/server-gateway/main.go` 未格式化/第 13 行 unused `net`，导致 gofmt/vet/build/全仓 test 失败；contract/bindings/pin/docs 均通过。待该并行任务修复后必须复跑 backend，随后才跑本代码态 packaged；2026-08-15 的 24/24 manifest 早于本切片，不能引用为当前证据。未 commit/push/tag/release。

## 13.13 G33 AC1/AC3 workspace Skill reload fail-closed 子切片交付（2026-08-16，Goal 仍进行中）

- 复核结论：G33 缺口已变化但仍存在，AC 仍为 0/6；本切片只收紧 workspace Skill source 的撤销与失败加载边界，G28~G32 与 P12-BUG-01 未开始。H1 原始全平台范围仍存在（Linux T、Windows junction I、macOS/Reveal/CAS U），H2/H3/H4/M3 在各自修复范围内不存在。
- 改动：`services/skills_service.go` 的 `setWorkspaceRoot` 改为返回刷新错误，切换 root 时先清空已加载 Skill（包括 project approval），再通知统一 catalog；`Load` 改为 staged 读取并校验 source identity，用户/项目目录读取失败时清空旧 slice 并刷新 SourceSkill，避免旧 workspace Skill 被重新发布；慢的旧 workspace load 不得覆盖新 source。`services/agent_service.go` 记录 source reset/Load 失败但保持 workspace 切换的安全降级（Skill catalog 为空），rollback 清理错误不静默吞掉。`services/agent_execution_workflow_skill_test.go` 新增 A→B 失败加载矩阵，断言旧 ToolDef、批准状态、session binding 与 one-shot capability 均失效，新 workspace 不会签发旧 Skill capability。
- 首次失败与修复：新增测试首轮未把 `SkillsService` 注入 `AgentService.skillsService`，workspace switch 未进入 Skill reload 路径，测试在旧批准断言处失败；补齐 trusted wiring 后，修复前的静态链路确认旧 approval/ToolDef 会保留，实施 staged clear/fail-closed 后测试转绿。未删除或放宽任何安全断言。
- 验证：`go test ./services -run '^(TestAgentExecutionCoreSkill.*|TestAgentExecutionCoreWorkspaceSkillLoadFailureFailsClosed)$' -count=1 -v` exit 0；`go test ./services -run '^Test(Skill|SkillsService|MergeSystemPrompts|AllowedTools)' -count=1 -v` exit 0；`go test -race ./services -run '^TestAgentExecutionCoreWorkspaceSkillLoadFailureFailsClosed$' -count=1` exit 0；scoped `git diff --check` exit 0。
- 安全与数据：旧 project Skill 的 approval/session binding/capability 在 root 切换前后均不可复用；失败加载只保留空安全来源，不把旧配置冒充新 workspace。此为 Go/T 级边界，不构成真实多窗口/进程重启 I 证据。
- AC/未验证：AC1 `[ ] T/I/U`（catalog source 撤销边界已加强，AI、Git/file mutation、其余 adapters、Skill workflow adapter 与真实 MCP 子进程 I 仍缺）；AC2 `[ ] T/U`、AC3 `[ ] T/U`（workspace Skill ownership T 级边界已变化，跨重启 recovery/manual-disposition、cross-caller proof、domain rollback 与真实 I 仍缺）；AC4~AC6 不变。backend gate 当前并行 Docker blocker 与最新代码 packaged 仍未复跑；不要引用 2026-08-15 历史 24/24 manifest 作为当前 AC6。未 commit/push/tag/release。

## 13.14 G33 AC1/AC2 typed workflow Skill activation 子切片交付（2026-08-16，Goal 仍进行中）

- 复核结论：G33 缺口已变化但仍存在，AC 仍为 0/6；本切片只推进 typed workflow Skill activation，G28~G32 与 P12-BUG-01 未开始。H1 原始全平台范围仍存在（Linux T、Windows junction I、macOS/Reveal/CAS U）；H2/H3/H4/M3 在各自修复范围内不存在。
- 改动：`services/workflow_service.go` 增加唯一 `type: skill` / `tool: activate` / `input.id` shape 与 canonical Skill ID 校验，拒绝 command/args/cwd、缺失或额外 input；`services/agent_execution_workflow_skill.go` 发布 `SourceWorkflow` ToolDef，记录 workflow/step、skillId、scope、fingerprint，并复用既有 `skill.activate` handler、项目 Skill 审批和 external receipt；`services/task_service.go` 从 session-owned workflow/step 与当前 catalog 重新签发 capability，Skill observation 不走 command runner；`services/agent_execution_core.go` 允许 workflow Skill activation 建立 session policy；`frontend/src/stores/workflows.ts`/tests 增加 strict Skill shape 与 catalog-only runner。没有新建平行 Skill executor，也未修改 Wails DTO 或手调 binding ID。
- AC：AC1 `[ ] T/I/U`（typed Skill activation 已有 Go/Vitest T，AI、Git/file mutation、其余 file/Git、真实 MCP process I 仍缺）；AC2 `[ ] T/U`（Skill approval、one-shot capability、scope/fingerprint、workflow/step receipt compensation 已有 T，跨重启 pending dispatcher/ambiguous commit 仍 U）；AC3~AC5 状态不变；AC6 `[ ] T/P/U`。G33 保持 0/6。
- 验证：Skill/workflow validation、AgentCore、TaskService 定向 Go exit 0；Skill workflow/agent 相关 `-race` exit 0；`go test ./internal/agentcore -count=1` exit 0；`go test ./services -count=1` 全量 exit 0（263.128s）；`workflows.test.ts` 152/152 exit 0（`npm.cmd run test -- --run src/stores/workflows.test.ts`，锁定 bindings 生成器）；定向 ESLint、`vue-tsc --noEmit`、`check-bindings`（pinned `v3.0.0-alpha2.111`、manifest/generated tree match、ByName=0）、`check-doc-links` 与 scoped `git diff --check` 均 exit 0。
- 首次失败：Go 首轮因测试先引用尚不存在的 `workflowAdapterSkillActivate` 编译 exit 1；前端首轮 152 项中 typed Skill 正向用例无 catalog 调用而失败（151/152）。实现 shared handler/catalog adapter 与 strict runner 后复跑通过；未删除或放宽安全断言。
- 安全与数据：renderer 只提交 workflow/step identity，Skill id 从 backend workflow definition 重载；canonical ID、scope、fingerprint 在 ToolDef、Prepare、approval、TaskService 参数映射和 receipt/compensation 两侧复核。跨 step token replay、Skill/catalog 变化和 terminal ledger failure 均在 Skill state/handler 触达前 fail-closed；typed observation 以既有 8,000-byte Agent 上限桥接，audit/usage 不落 Skill 内容或 command/cwd。
- 未验证/下一步：当前 `task frontend:check` 已实际运行但 exit 1：Vitest 172/173 files、2762/2763 tests，唯一失败为既有 `extensionHost.test.ts` 的 `creates distinct backend sessions for same-millisecond terminals`（期望 `startSession` 2 次、实际 1 次）；单文件复跑仍为 106/107、exit 1，本切片未修改或放宽该测试。backend-gate 仍受并行 `build/docker/server-gateway/main.go` blocker 影响，当前代码态 packaged 未重跑；不要引用 2026-08-15 的 24/24 manifest 作为 AC6。继续 G33 AC1 的 AI/剩余 mutation adapters 或 AC2/AC3 durable recovery/cross-caller 缺口。未 commit/push/tag/release。
 - SSOT 回写：本切片同步 prompt-11 §9 交付记录与 prompt-9 §8 进度板；本切片未修改 GitHub workflow、Docker、package-lock 或治理元数据。

## 13.15 G33 Skill 子切片全量门禁复核（2026-08-16，Goal 仍进行中）

- 复核结论：G33 缺口已变化但仍存在，AC 仍为 0/6；H1 原始全平台范围仍存在（Linux T、Windows junction I、macOS/Reveal/CAS U），H2/H3/H4/M3 在各自修复范围内不存在。G28~G32 与 P12-BUG-01 未开始。
- 验证：`go test ./internal/agentcore -count=1` 与 `go test -race ./internal/agentcore -count=1` 均 exit 0；`node scripts/check-bindings.mjs`、`node scripts/check-doc-links.mjs` 均 exit 0（锁定 Wails `v3.0.0-alpha2.111`）。`node scripts/backend-gate.mjs` 的 gofmt、vet、build、contract、bindings、pin、docs 步骤均 exit 0，但全量 Go 测试首轮 exit 1，失败为 `TestAIPermission_RecordUsage_GetSummary` 与 `TestApplyEditTransaction_PathOutsideRoot_Rejected`；两项单独复跑均 exit 0。`task frontend:check` 首轮为 172/173 files、2762/2763 tests，唯一失败为既有 `extensionHost.test.ts` 同毫秒 terminal session 用例；该用例单独复跑 exit 0。前端 workflow Skill 定向仍为 152/152，相关 Go/`-race` 仍 exit 0。
- 首次失败与修复：本次全量红灯均为非 G33 Skill 定向代码的并发/时序复现，未修改或放宽相关断言；保留失败证据并以单测复跑确认非稳定红灯。此前 Skill 首轮缺少 adapter 常量、前端 typed positive case 未走 catalog 的失败与修复已记录于 §13.14。
- 第二轮全量复跑：`task frontend:check` 再次在同一 terminal session 用例失败（172/173 files、2762/2763），但随后 `extensionHost.test.ts` 整文件 107/107 exit 0；backend gate 的非测试步骤继续全绿，`go test ./...` 改为在 `TestLSPServiceRealTypeScriptWorkspaceLocalServer` 的 Windows `taskkill` 清理竞态失败，该测试单独复跑 exit 0。连续两轮完整 gate 均未全绿，因此不得宣称 AC6 门禁通过，也不得启动 packaged。
- AC/未验证：AC1 `[ ] T/I/U`、AC2 `[ ] T/U`，AC3~AC6 不变；当前代码态 packaged 尚未重跑，2026-08-15 的 24/24 manifest 仍是历史证据，不得用于 AC6。AI、Git/file mutation/更多 typed adapters、真实 MCP process I、跨重启 recovery/manual-disposition、cross-caller owner proof、domain rollback、stream privacy/retention、ambiguous commit 与真实 CLI/CI 仍未闭环。
- 约束：本次未修改 `.github/workflows`、`build/docker`、`package-lock.json` 或 Issue/PR/Release 治理元数据；未 commit/push/tag/release。

## 13.16 G33 Windows LSP process tree、extensionHost 时序与全量门禁闭环快照（2026-08-16，Goal 仍进行中）

- 复核结论：H1 原始全平台缺口**仍存在**（FileService 核心内容读写为 Linux `T`、Windows junction `I`；macOS、RevealInOS、CAS 与 Agent Workflow pathname loader 仍为 `U`）；H2 **已不存在**（真实 Windows `cmd.exe` 的 `Command`/`CommandContext` 注入矩阵为 `I`）；H3/H4/M3 在各自修复范围内**已不存在**。G33 **已变化但仍未完成**，AC 仍为 0/6；G28~G32 与 P12-BUG-01 未开始。
- 改动：`services/lsp_process_tree_windows.go`/`other.go`、`lsp_service_session.go`、`lsp_service_server.go` 及测试以 Windows Job Object `KILL_ON_JOB_CLOSE` 管理 LSP 子进程树，`TerminateJobObject` 后必须确认 `ActiveProcesses==0`；用 Toolhelp + `IsProcessInJob` 收编 `Start` 与 Job 分配之间已生成的后代，活跃后代查询/分配失败时 fail-closed，且不再解析本地化 `taskkill` 输出或因根进程 `done` 吞掉终止错误。`frontend/src/lib/extensionHost/extensionHost.ts`/test 将 terminal service 与 app store 合并为单一 lazy runtime promise，测试直接观察稳定 mock、幂等释放 terminal 并等待 `disposeAll`，没有放大既有 10s/20s 预算。
- 首次失败与修复：Windows 预存在 child 红灯首先证明子进程可在 Job 分配前逃逸，加入后代收编后转绿；backend JSON 诊断随后定位 `TestLanguagePackRealRustLSPToolchainAndDebug` 的 rust-analyzer/flycheck 占用 TempDir，清理边界修复后真实测试连续 3 次通过。extensionHost 同毫秒 terminal 用例在全量负载下反复只调用一次 `startSession`；移除对动态 import 局部对象的脆弱 spy、统一 lazy runtime 与确定性 teardown 后，目标用例连续 10 次及整文件 107/107 通过，未删除或放宽断言。
- 定向验证：Windows Job/LSP 矩阵 `-count=10` exit 0，同组 `-race` exit 0，`go vet ./services` exit 0；真实 Rust LSP/toolchain 集成 `-count=3` exit 0（102.096s）。extensionHost 目标用例连续 10 次、整文件 107/107、定向 ESLint 与 `vue-tsc --noEmit` 均 exit 0。Linux 交叉编译仍被 Wails alpha Linux `pointer` build constraint 阻塞，保持 `U`。
- 当前代码快照全量门禁：`node scripts/backend-gate.mjs` 9/9 exit 0（其中 `go test ./... -count=1` 350.6s）；`task frontend:check` exit 0（173/173 files、2763/2763 tests，ESLint 0 errors/1 个既有 warning、vue-tsc、bindings、docs 全绿）；独立 `node scripts/check-bindings.mjs` 与 `node scripts/check-doc-links.mjs` 均 exit 0。`scripts/wails-bindings.test.mjs` 现在验证 digest-pinned Go/Node images、bindings stage 与 `ARG GO_IMAGE` 同一 digest、`npm ci --ignore-scripts` 与敏感文件 `.dockerignore` 规则，定向 16/16 exit 0；本切片未修改 `build/docker`。
- packaged `P` 证据：`node scripts/packaged-e2e.mjs` exit 0，权威 `build/e2e-evidence/packaged-e2e/manifest.json` 为 `status=passed`、`phase=complete`、24/24 fixtures passed；artifact SHA-256 `cc7831f84ccd5bd3be66d16f57e768ca3cecd13deda2d8879ccb83f46afe4a8b`，source fingerprint `58b5e59ccda1419ed0853eb89f80f900a6be189498d86edf608343e9b3784ffe`，`recordedAt=2026-08-15T20:52:15.082Z`，`completedAt=2026-08-15T20:58:13.390Z`。首次 cleanup 遇 WebView lockfile `EBUSY`，脚本按既有重试策略成功清理并退出 0；无残留 Koyori 进程、根目录 exe 或 syso。`screenshot=null`，不得声称有本次截图或把单机 Windows `P` 扩大为跨平台/CI `R`。
- AC 与未验证：AC1~AC6 全部保持 `[ ]`，G33 仍为 0/6。AI、mutation adapters、真实 MCP process `I`、recovery/manual-disposition、cross-caller owner proof、domain rollback、stream privacy/retention、ambiguous commit 与真实 CLI/CI 均未闭环；Workflow catalog 仍经 `os.ReadDir`/pathname `os.Open` 读取 renderer 指定根，junction/symlink 交换可能把 workspace 外定义发布给 Agent，因此在 secure loader 前不得接入 typed AI。公开 Workflow CRUD 的 renderer `projectRoot` 仍是更大的独立 H1，不因只修 Agent loader 而关闭。
- 供应链边界：G33 树实际运行 `node scripts/npm-audit-gate.mjs` exit 1，明确命中 `nanoid <3.3.18` / GHSA-2v37-7h3g-55p8 / 1 high；其 675-entry、`@wailsio/runtime` alpha.95 锁图运行前后 SHA-256 前缀均为 `F1C0AED7`，门禁未改 lockfile。hardening 树的独立 431-entry、beta.8 锁图使用强化 gate 后 exit 0（424 个 registry packages 均有 official URL+SRI、official audit 无 high/critical、`npm ci --dry-run` 通过，锁 SHA 前后前缀 `B34117D4`），但两树证据不可互换。最终整合必须纳入 hardening 的 package manifest/lock 与强化 gate，或在整合树重新生成并审核；本切片没有修改 `frontend/package.json`/`package-lock.json`，当前 G33 供应链门禁保持红灯。
- 约束与下一步：本切片未修改 `.github/workflows`、`build/docker`、package lock 或 Issue/PR/Release 治理元数据；未 commit/push/tag/release。后续只推进 G33，先用 `FileService` 的 backend-owned `os.Root` capability 替换 Agent Workflow pathname loader，并在安全改动前补 junction/symlink 交换红灯；该代码一旦变化，以上全量/packaged 证据即转为历史，必须重新验证后方可用于 AC6。

## 13.17 G33 Agent Workflow secure loader 与当前代码态全量复核（2026-08-16，Goal 仍进行中）

- 复核结论：H1 原始全平台范围**仍存在**（Linux 核心路径 T、Windows junction 交换 I；macOS、RevealInOS、CAS 及公共 Workflow pathname loader 仍 U）；H2 **已不存在**（真实 Windows `cmd.exe` command/commandContext 注入矩阵为 I）。G33 **已变化但仍存在**，AC 仍为 0/6；G28~G32 与 P12-BUG-01 **仍未开始**。
- 改动：新增 `services/workflow_secure_loader.go` 及 Agent workflow loader 测试，使用 backend-owned `FileService`/`os.Root` capability，执行 `Lstat -> Open -> Stat -> SameFile -> bounded read -> post-Lstat`、SHA-256、workspace/file generation 校验并在批次任一失败时 fail-closed；catalog、approval、Prepare、receipt、Execute 均重新核验 workflow source。公共 Workflow CRUD 的 pathname loader 尚未纳入 H1 闭环。
- AC：AC1 `[ ] T/I/U`（typed `file.read`、只读 `git.status`、`mcp.call`、`skill.activate` 有 T；AI、mutation、剩余 adapters 与真实 MCP process I 仍缺）；AC2 `[ ] T/U`；AC3 `[ ] T/U`；AC4 `[ ] T/U`；AC5 `[ ] T/U`；AC6 `[ ] T/P/U`。G33 保持 0/6。
- 验证：`go test ./services -count=1` exit 0（319.693s）；workflow/loader `-race`、`go vet ./services`、gofmt exit 0；`node scripts/backend-gate.mjs` exit 0，9/9（全仓 Go test 375.3s）；`task frontend:check` exit 0，173 files / 2763 tests；`node scripts/check-bindings.mjs`、`node scripts/check-doc-links.mjs` exit 0；`node scripts/packaged-e2e.mjs` exit 0，24/24 passed，manifest `status=passed`/`phase=complete`，artifact SHA-256 `ec0847a981867f52969e8f2cb04719485a78bba2e67a4ba07cc925558f1e8353`，source fingerprint `58b5e59ccda1419ed0853eb89f80f900a6be189498d86edf608343e9b3784ffe`，`completedAt=2026-08-15T22:03:31.427Z`；该证据为 Windows 本机 P，不升级为跨平台/CI R。
- 首次失败与修复：backend gate 首轮出现既有临时文件时序 flake，隔离连续 5 次复跑后完整 9/9 通过；packaged cleanup 首次遇 WebView `EBUSY`，既有重试完成且无残留进程/根目录产物。`node scripts/npm-audit-gate.mjs` 仍 exit 1，命中 G33 锁图 `nanoid <3.3.18` / `GHSA-2v37-7h3g-55p8` / 1 high；未修改 package lock，hardening 树另一锁图绿灯不适用。
- 安全与数据：renderer 只提交 session-owned workflow identity，不能提供绝对 root 或预解析 pathname；读取结果受 bounded output/usage/audit 约束，不记录文件内容、secret 或完整本地绝对路径。公共 Workflow loader、RevealInOS 外部文件管理器与 CAS 竞争仍按 H1 保持 U。
- 未验证/下一步：真实 macOS/Linux packaged、CI/CLI consumer、真实 MCP 子进程 observation、AI/mutation adapters、recovery/manual-disposition、cross-caller ownership、domain rollback 与供应链高危修复仍未闭环；继续唯一 Goal G33，优先补 AC1 下一真实 adapter/安全负例，所有 AC 保持未勾选。无 commit/push/tag/release。
- SSOT 回写：同步更新 prompt-12 §11/§13、prompt-9 §8 与 prompt-11 §9；本切片未修改 GitHub workflow、Docker、package-lock 或治理元数据。

## 13.18 G33 AC1 typed workflow AI adapter 子切片（2026-08-16，Goal 仍进行中）

- 复核结论：H1 原始全平台范围**仍存在**（Linux 核心路径 T、Windows junction I；macOS、RevealInOS、CAS 及公共 Workflow pathname loader 仍 U）；H2 **已不存在**（真实 Windows `cmd.exe` command/commandContext 注入矩阵为 I）。G33 **已变化但仍存在**，AC 仍为 0/6；G28~G32 与 P12-BUG-01 **仍未开始**。
- 改动文件：`services/workflow_service.go` 增加严格 AI typed contract（`tool=generate|review|commit-message`、唯一 bounded `input.prompt`），旧 `type: ai` command 仅可作为迁移诊断源加载且永不进入 catalog；`services/agent_execution_workflow_skill.go`/`agent_execution_workflow_ai.go` 发布 workflow-owned AI ToolDef，renderer capability 参数固定 `{}`，Prepare/approval/Begin/Execute 重新加载 workflow source 并绑定 prompt hash、workspace generation、provider config fingerprint 与不可逆 external receipt；`services/ai_agent.go`/`settings_ai_provider.go` 由后端解析 assigned provider 的 endpoint/protocol/key/model，拒绝跨 provider 复用全局配置，支持有限 fallback；`internal/agentcore/runtime.go` 接收 handler provider usage 并写入统一 tool receipt；`services/agent_service.go` 增加独立 AI approval callback；`services/task_service.go` 将 typed AI observation 桥接为 workflow result；`main.go` trusted wiring 接入 AI；`frontend/src/stores/workflows.ts`/测试只走 catalog-only typed AI runner。未修改 `.github/workflows`、`build/docker`、`package-lock` 或 Issue/PR/Release 治理元数据。
- AC：AC1 `[ ] T/I/U`（AI typed adapter、provider boundary、prompt/operation source reload、one-shot capability、receipt/usage 与 renderer fail-closed 负例已有 Go/Vitest/`-race` `T`；真实 provider 进程/packaged I、真实 MCP process、Git/file mutation 与其余 adapters仍缺）；AC2 `[ ] T/U`（AI external receipt 明确不可逆，跨重启 pending dispatcher/人工处置仍 U）；AC3~AC6 状态不变，G33 保持 0/6。
- 验证：`go test ./services -run 'TestAgentWorkflowAIAdapter|TestResolveAgentOperationUsesAssignedProviderBoundary|TestAgentExecutionCoreWorkflow|TestTaskService.*Workflow' -count=1` exit 0；`go test -race ./services -run 'TestAgentWorkflowAIAdapter|TestAgentExecutionCoreWorkflowWorkflow|TestAgentWorkflow' -count=1` exit 0；`go test -race ./internal/agentcore -count=1` exit 0；`go test ./services -run '^$' -count=1` 编译 exit 0；`npm.cmd test -- --run src/stores/workflows.test.ts` 153/153 exit 0；scoped ESLint、`vue-tsc --noEmit` exit 0。`go test ./services -count=1` 本轮 300 秒超时，未取得全量通过证据，故不把此前历史全量记录当作当前证据。
- 首次失败与修复：AI 定向测试首轮因缺少 `approveAI`、trusted wiring、adapter 常量而编译失败；实现后 provider/renderer fail-closed 组转绿。workflow 全量定向首轮因 secure loader 将旧 AI command 视为 malformed source 失败，改为仅允许该旧形状进入迁移诊断且仍不发布 ToolDef；未把旧 prompt 重新解释为 shell，也未放宽 typed 负例。
- 安全与数据：renderer 不能提交 prompt、operation、provider、root 或 cwd；workflow source 变化、provider endpoint/protocol/key/model 变化、workspace generation 变化、receipt/参数不匹配均在 provider 触达前拒绝。AI observation 受 8,000-byte bound，usage/audit 仅保留 provider/model/token/cost basis/receipt identity，不写 prompt、API key 或完整绝对路径。provider assignment 不再沿用全局 BaseURL/Protocol，跨 provider key 泄漏路径由回归测试覆盖。
- 未验证/下一步：本轮全量 `go test ./services` 300 秒超时；backend/frontend 全量与 packaged 需在本代码态重新跑，当前 `npm-audit-gate` 的 G33 `nanoid` high 仍 exit 1；真实 AI provider/MCP 子进程 `I`、AI stream lifecycle、Git/file mutation、recovery/manual-disposition、cross-caller ownership、domain rollback、真实 CLI/CI 与跨平台 packaged 仍 U。下一步仍只推进 G33 AC1 的 mutation/remaining typed adapter 或 AC2 durable recovery，所有 AC 保持未勾选。
- SSOT 回写：本节同步 prompt-12 §11、prompt-9 §8 与 prompt-11 §9；未 commit/push/tag/release。

## 13.19 G33 AI provider resolver boundary 与遗留安全证据复核（2026-08-16，Goal 仍进行中）

- 复核结论：H1 原始全平台范围**仍存在**（Linux 核心读写 T、Windows junction 交换 I；macOS、RevealInOS、CAS 与公共 Workflow pathname loader 仍 U）；H2 **已不存在**（真实 Windows `cmd.exe` 的 `Command`/`CommandContext` 注入矩阵为 I）；H3 **已不存在**（前端 integrityChecked 门禁）；G33 **已变化但仍存在**，AC 仍为 0/6；G28~G32 与 P12-BUG-01 **仍未开始**。
- 改动文件：`services/ai_service.go` 将导出的 `ResolveModelFor` 收窄为脱敏 assignment metadata（清除 API key、endpoint、protocol、prompt 与 tool definitions）；backend-only `resolveModelFor` 保留完整 assignment，`services/ai_agent.go` 的 typed AI execution 只走该内部路径并在 `SettingsService` 中重载 provider endpoint/protocol/key。`services/ai_agent_test.go` 增加 assigned/global provider details 不得越过 Wails resolver 的回归断言。未修改 `.github/workflows`、`build/docker`、`package-lock` 或 Issue/PR/Release 治理元数据。
- AC：AC1 `[ ] T/I/U`（AI typed adapter、provider fingerprint/source reload、one-shot capability、receipt/usage 与 renderer fail-closed 负例为 T；本轮 provider resolver 脱敏回归为 T；真实 provider/MCP 子进程到 renderer observation 的 I、Git/file mutation 与其余 adapters仍缺）；AC2 `[ ] T/U`（AI external receipt 明确不可逆，跨重启 pending dispatcher/人工处置仍 U）；AC3~AC5 不变；AC6 `[ ] T/P/U`，本轮代码变化后必须重跑 full gate 与 packaged，不能沿用旧 manifest。G33 保持 0/6。
- 遗留验证：`go test ./services -run "TestFileService.*Junction|TestSecureWorkspace|TestFileService.*RootIdentity|TestFileService.*WorkspaceSwitch" -race -count=1 -v` exit 0（Windows junction 9/9）；`go test ./services -run "^(TestEscapeCmdArgRoundTrip|TestCommandCmdShimRoundTrip)$" -count=1 -v` exit 0（真实 cmd.exe 两路径）；H3 前端 integrity vitest 3 files/41 tests exit 0，`npm.cmd exec vue-tsc -- --noEmit` exit 0。AI/provider 定向 Go 组 exit 0，切片文件 gofmt 与 scoped diff check exit 0。
- 验证后状态：本轮 `node scripts/backend-gate.mjs` 9/9 exit 0（全仓 `go test ./... -count=1` 226.3s）；`task frontend:check` exit 0（173 files/2764 tests，ESLint 0 errors/1 个既有 warning，vue-tsc、bindings/docs 全绿）；独立 `check-bindings`、`check-doc-links`、`check-doc-numbers` exit 0；`node scripts/packaged-e2e.mjs` exit 0，最新 manifest 为 Windows 24/24 passed，artifact `8ce7efa35078eec8a371ea24e3a6583218fc043665882206f2657cb1b67e420b`、source `b8ee03e3d15482578a4e9c69131a39385fa5c6d17f5ecb1c28beb6429c2dd1d2`、completedAt `2026-08-15T23:29:22.127Z`；`node scripts/npm-audit-gate.mjs` 仍 exit 1，命中 G33 lock 图 `nanoid <3.3.18` / `GHSA-2v37-7h3g-55p8` / 1 high，未改 lockfile。真实 macOS/Linux packaged、CI/CLI consumer、真实 AI/MCP 子进程 I、AI stream lifecycle、Git/file mutation、recovery/manual-disposition、cross-caller ownership、domain rollback 仍 U；hardening 树另一 lock 图绿灯不可互换。下一步只推进 G33 的下一未完成 AC1/AC2 子切片；所有 AC 保持未勾选。未 commit/push/tag/release。
- SSOT 回写：同步更新 prompt-12 §11/§13、prompt-9 §8 与 prompt-11 §9。

## 13.20 G33 AC1 typed workflow file.write mutation 子切片与全量门禁复核（2026-08-16，Goal 仍进行中）

- 复核结论：H1 原始全平台范围**仍存在**（Linux 核心读写 T、Windows junction 交换 I；macOS、RevealInOS、CAS 与公共 Workflow pathname loader 仍 U）；H2 **已不存在**（真实 Windows `cmd.exe` `Command`/`CommandContext` 注入矩阵为 I）；H3 **已不存在**（integrityChecked 前端门禁已复核）。G33 **已变化但仍存在**，AC 仍为 0/6；G28~G32 与 P12-BUG-01 **仍未开始**。
- 改动文件：`services/workflow_service.go` 增加严格 `file.write` typed contract（canonical root-relative path、backend-owned content、1 MiB 上限，拒绝 command/args/cwd）；新增 `services/agent_execution_workflow_file.go`，并在 `services/agent_execution_workflow_skill.go`、`services/task_service.go` 接入统一 ToolDef/capability/approval/handler。`services/agent_execution_core.go` 与 `services/workspace_edit_transaction.go` 让 Agent 写入经 `FileService.WriteFileIfUnchanged` CAS；`services/file_save_integrity.go` 支持空 baseline 的安全创建并在提交前复核 identity。`frontend/src/stores/workflows.ts`/test 只接受 catalog-only typed runner。对应 Go 测试覆盖 source mutation、baseline conflict、publish race、approval missing、TaskService bridge、CAS 与 audit redaction。未修改 `.github/workflows`、`build/docker`、`package-lock` 或 Issue/PR/Release 治理元数据。
- AC：AC1 `[ ] T/I/U`（file.write adapter 的 backend-owned source、one-shot capability、CAS、renderer fail-closed 与审计脱敏已有 T；AI、Git/file 其余 mutation、真实 provider/MCP process I 仍缺）；AC2 `[ ] T/U`（workspace transaction/CAS 与 receipt 链路有 T，但跨重启 pending dispatcher、人工处置与完整 domain rollback 仍 U）；AC3~AC5 不变；AC6 `[ ] T/P/U`。G33 保持 0/6。
- 验证：`go test ./services -run 'TestAgentExecutionCoreWorkflowFileWrite|TestFileService_WriteFileIfUnchanged|TestAgentCoreAuditSinkRedactsWorkspacePaths' -count=1 -v` 与同组 `-race` 均 exit 0；受影响 services 测试组 exit 0；`node scripts/backend-gate.mjs` 9/9 exit 0（全量 `go test ./... -count=1` 355.4s）；`task frontend:check` 173 files、2765 tests exit 0（ESLint 0 errors/1 existing warning，vue-tsc/bindings/docs 全绿）；`node scripts/packaged-e2e.mjs` exit 0，最新 manifest `status=passed`/`phase=complete`，24/24 passed、`not-run=0`，recordedAt `2026-08-16T01:45:05.557Z`，artifact SHA-256 `facaf467b692ececbbde53d40482bfc3f7126d2281abe55b0670ff0d8141a7ed`，source fingerprint `b9922c3238eae371166efc5fa03dfe5141ad977244cdf879212a33405465d0d3`；仅 Windows 本机 P。独立 `node scripts/npm-audit-gate.mjs` exit 1，当前 G33 锁图命中 `nanoid <3.3.18` / `GHSA-2v37-7h3g-55p8` / 1 high，未修改 lockfile。
- 首次失败与修复：file.write publish-race 测试最初未接受 `ErrFileConflict`；审计/public error 测试仍暴露绝对 workspace pathname；事务 hook 仍只断言旧 `WriteFile`。修正断言和脱敏，并保留 CAS/冲突 fail-closed 行为。前端 root `npm run frontend:check` 不适用（根目录无 package.json），按仓库 `Taskfile.yml` 执行 `task frontend:check`。
- 安全与数据：renderer 只能提交 workflow/step identity 和空 capability args；content/path 从 backend-owned source 重载，provider/command/cwd 不可注入。写入冲突、workspace generation/source hash 变化和 approval 缺失在 handler 触达前拒绝；usage/audit 与公开错误不写文件内容、secret 或完整本地绝对路径。公共 Workflow pathname loader、RevealInOS、macOS 仍按 H1 保持 U。
- 未验证/下一步：真实 AI provider/MCP 子进程 observation、Git mutation、recovery/manual-disposition、跨 caller owner proof、domain rollback、真实 CI/CLI 与 macOS/Linux packaged 仍 U；npm high advisory 仍阻塞供应链门禁。只继续 G33 下一未完成 AC1/AC2 子切片，所有 AC 保持 `[ ]`，无 commit/push/tag/release。
- SSOT 回写：本节同步 prompt-12 §11、prompt-9 §8 与 prompt-11 §9。

## 13.21 G33 动态 catalog 发布串行化与 packaged source fingerprint 证据完整性修复（2026-08-16，Goal 仍进行中）

- 复核结论：H1 原始全平台范围**仍存在**（Linux 核心读写 T、Windows junction 交换 I；macOS、RevealInOS、CAS 与公共 Workflow pathname loader 仍 U）；H2/H3/H4/M3 在已验证修复范围内**已不存在**。G33 **已变化但仍存在**，AC 仍为 0/6；G28~G32 与 P12-BUG-01 **仍未开始**。
- 改动文件：`services/agent_service.go` 增加 catalog refresh 串行锁；`services/agent_execution_core.go` 拆出锁内完整重建并保留测试用 MCP timeout 注入；`services/agent_execution_workflow_skill.go` 让 workflow/Skill mutation 的 clear 与重建持有同一锁；`services/agent_execution_mcp.go` 对锁内 MCP catalog I/O 设置生产 15 秒上限；`services/agent_execution_workflow_skill_test.go` 增加 pre-publication 交错/second-start 同步，`services/agent_execution_core_test.go` 增加阻塞 MCP timeout 后清 source 回归。`scripts/packaged-e2e.mjs` 将 source fingerprint 改为递归 `build-inputs-v2`，增加生成物排除、symlink/junction 拒绝、构建后/fixtures 后复核和严格 skip-build reuse 绑定；`scripts/packaged-e2e-driver.test.mjs` 增加对应回归；`docs/E2E.md` 同步复用约束。未修改 `.github/workflows`、`build/docker`、package lock 或 Issue/PR/Release 治理元数据。
- AC：AC1 `[ ] T/I/U`（动态 catalog 的完整发布顺序取得 T，typed adapters 的真实 provider/MCP/CLI I 与其余 mutation 仍缺）；AC6 `[ ] T/P/U`（本机 Windows full build 与严格 reuse 为 P，但跨平台 packaged、真实 CI/CLI、供应链门禁仍 U/红）；AC2~AC5 状态不变。G33 保持 0/6，不得宣称 Goal 或 AC 完成。
- 首次失败与修复：`TestWorkflowCatalogRefreshesAreSerialized` 的 hook 首先稳定复现旧 `go version` snapshot 在较新的 `go env GOOS` snapshot 后发布并覆盖，串行锁后定向组及相关 `-race` 转绿。packaged artifact 已变为 `d2d15fa...` 时 source 仍错误保留旧 `b9922c...`，暴露手工静态清单漏项；Node 测试首轮因新 helper 尚未导出 exit 1。递归实现首轮又把 36 个 bindings 临时文件、55 个生成 bindings、50 MiB test exe、marker 与 overlay 纳入，且只在构建前取 snapshot；补排除、顶层 junction 拒绝、前后 snapshot 与 reuse contract 后 14/14 通过。旧 manifest 的无约束 skip-build 被新逻辑以 `source was not verified after its build` 拒绝，exit 1。
- packaged 失败/通过证据：第一次递归指纹实跑在构建成功后因 `git-rebase-probe` 的 `git rev-parse --absolute-git-dir` 异常退出 `0xc0000142` 而失败，manifest 为 11 passed / 1 failed / 12 not-run，未伪装通过。修复证据边界后曾以 source `57c458...` 完成一次 full 24/24 与一次严格 reuse 24/24，证明 reuse contract；随后 MCP timeout/cancellation 源码变化使其转为历史。最终完整 `node scripts/packaged-e2e.mjs` exit 0，24/24，`recordedAt=2026-08-16T03:48:56.614Z`、`completedAt=2026-08-16T03:52:56.056Z`、`artifactReused=false`、`sourceFingerprintStableAfterBuild=true`；artifact SHA-256 `e3fdd8608e750a3a2cf432f7939cbfd7af2e6c6f07d1655f2adad4402b91e257`，source scope `build-inputs-v2`、987 files、SHA-256 `aabfe61ace787475faea4cf7de07faf3ace0ffa0afb00555d90c1ebf308e49ca`；仅 Windows 本机 P，不升级为跨平台/CI R。
- 全量验证：`node --test scripts/packaged-e2e-driver.test.mjs` 14/14 exit 0；`go test ./services -run "TestMCPService_ListAgentMCPToolsPropagatesContextCancellation|TestAgentCatalogMCPRefreshIsBoundedAndFailClosed|TestWorkflowCatalogRefreshesAreSerialized" -race -count=20` exit 0（49.110s），覆盖 catalog serialization、MCP timeout 与 context cancellation 传播；`node scripts/backend-gate.mjs` 9/9 exit 0（最终全量 Go test 354.8s）；仓库无根 `package.json`，按权威 Taskfile 执行 `task frontend:check`，173/173 files、2765/2765 tests exit 0，ESLint 0 errors/1 existing warning，vue-tsc/bindings/docs 全绿；独立 `check-bindings` 与 `check-doc-links` exit 0。`node scripts/npm-audit-gate.mjs` 仍 exit 1：official registry 与 lock stability 通过，但 `nanoid <3.3.18` / `GHSA-2v37-7h3g-55p8` 保持 1 high，未改 lockfile；hardening 另一锁图绿灯不可互换。
- 最终 SSOT 对账：独立 `node scripts/check-doc-links.mjs`、`node scripts/check-doc-numbers.mjs`、`node scripts/check-bindings.mjs` 均 exit 0；实时递归 inventory 重算仍为 987 files / `aabfe61ace787475faea4cf7de07faf3ace0ffa0afb00555d90c1ebf308e49ca`，与最终 manifest 完全一致。G33 范围 `git diff --check -- ...` exit 0；未限定范围的 `git diff --check` exit 1，仅报告受保护的既有用户改动 `build/scripts/build-msi.ps1:124: new blank line at EOF`，本 Goal 未修改该文件。npm gate 前后 `frontend/package-lock.json` SHA-256 均为 `F1C0AED759A7A5DCCD0C58A2BB685B0C8CC94248D0ED6E36106DA00ADC6AC55F`。
- 安全与未验证：目录刷新锁只串行 refresh clear/rebuild/publish，不放宽 catalog revision、workspace generation、session owner 或 capability 校验；阻塞 MCP lister 在 deadline 后返回并清空旧 MCP source。但 Registry reader/capability 路径不持该锁，三个 dynamic source 仍逐项 `ReplaceSource`，所以整个 catalog 对消费者的单事务原子可见性仍 U。fingerprint 拒绝 symlink/junction 并排除任意深度 `node_modules/dist/coverage/.vite/e2e-evidence`、生成 bindings/临时探针及已知构建残留；skip-build 不再凭 marker 复用任意旧 artifact。真实 AI provider/MCP process observation、Git 其余 mutation、recovery/manual-disposition、cross-caller owner proof、domain rollback、macOS/Linux packaged 与真实 CI/CLI 仍 U；下一步仍只推进 G33 的下一未完成 AC1/AC2 子切片。无 commit/push/tag/release。
- SSOT 回写：本节同步 prompt-12 §11、prompt-9 §8 与 prompt-11 §9。

## 13.22 G33 多 source 原子 catalog 与 mutation 全量撤销子切片（2026-08-16，Goal 仍进行中）

- 复核结论：H1 原始全平台范围**仍存在**（Linux 核心读写 `T`、Windows junction 交换 `I`；macOS、RevealInOS、持续 CAS 与公共 Workflow pathname loader 仍 `U`）；H2/H3/H4/M3 在已验证修复范围内**已不存在**。G33 **已变化但仍存在**，AC 仍为 0/6；G28~G32 与 P12-BUG-01 **仍未开始**。
- 改动文件：`internal/agentcore/registry.go`/test 增加 `ReplaceSources`，对多个动态 source 的候选一次校验、一次提交、最多推进一个 revision，任一 schema/ID/wire 冲突时整批零变化；`ReplaceSource` 委托该 API。`services/agent_execution_core.go` 将 MCP/workflow/Skill builder 改为只构建候选，workflow MCP adapter 显式消费本轮 MCP candidate，workflow/Skill adapter 与 SourceSkill 消费同一次 Skill snapshot，最终一次批量发布；任一 builder 失败时整批清空并直接返回，不发布成功子集。`services/agent_execution_workflow_skill.go` 的 workflow/Skill mutation 先用一个 revision 整批清空三类动态 source，再用一个 revision 发布完整新快照，以立即撤销旧 capability；`services/agent_execution_workflow_skill_test.go` 增加 mutation 中间态与失败候选负例，并用 `sync.Once`/有界等待收紧并发测试。未修改 `.github/workflows`、`build/docker`、package lock 或 Issue/PR/Release 治理元数据。
- AC：AC1 `[ ] T/I/U`（普通 refresh 的消费者侧多 source 单事务可见性、mutation 的全量撤销态、失败整批清空与 Registry 并发 reader 已取得 `T`；真实 provider/MCP/CLI `I`、Git 其余 mutation 与其余 adapters 仍缺）；AC2~AC5 状态不变；AC6 `[ ] T/P/U`。G33 保持 0/6，不得宣称 Goal 或任一 AC 完成。
- 首次失败与修复：Registry 红测最初因 `ReplaceSources` 不存在而编译失败；服务交错红测真实观察到 MCP 已变化而旧 workflow/Skill 仍在的 mixed revision。mutation 新红测随后在 workflow 与 Skill 两个子项都观察到“只清一个 source”的中间 catalog；失败候选红测又证明无效 Skill 会让 refresh 返回错误却留下成功发布的 workflow ToolDef。实现批量候选/批量清源并在任何 builder error 时发布前返回后，上述红灯全部转绿；未删除测试或放宽安全断言。
- 定向验证：catalog/mutation/失败清源相关 services 组 exit 0；同组 `-race -count=20` exit 0（44.727s）；`internal/agentcore` `ReplaceSource|ReplaceSources` 组 `-race -count=20` exit 0（3.710s）。H1 Windows junction/renderer 文件矩阵 `-race` exit 0；H2 真实 `cmd.exe` `Command`/`CommandContext` 3.04s、exit 0；H3 前端 3 files/41 tests 与 `vue-tsc --noEmit` exit 0。
- 全量门禁：最终 `node scripts/backend-gate.mjs` 9/9 exit 0（全仓 Go test 348.8s）；`task frontend:check` 首轮在测试启动前因未继承本机 Wails 路径、访问 `proxy.golang.org` 超时而 exit 1，指定已验证的锁定 `WAILS3_BIN` 后 173/173 files、2765/2765 tests exit 0，ESLint 0 errors/1 existing warning，vue-tsc/bindings/docs 全绿。`node scripts/npm-audit-gate.mjs` 仍 exit 1：official registry/lock stability 通过，但 `nanoid <3.3.18` / `GHSA-2v37-7h3g-55p8` 为 1 high；lock SHA-256 前后均为 `F1C0AED759A7A5DCCD0C58A2BB685B0C8CC94248D0ED6E36106DA00ADC6AC55F`。
- packaged 证据：最终非复用 `node scripts/packaged-e2e.mjs` exit 0，manifest `status=passed`/`phase=complete`、24/24 passed、0 failed/0 not-run，`recordedAt=2026-08-16T05:58:43.631Z`、`completedAt=2026-08-16T06:02:46.357Z`、`artifactReused=false`、`sourceFingerprintStableAfterBuild=true`；artifact SHA-256 `7795a014badc90c7d10f8b23ba17035f85ebe77fa75aeceaf1b5dd1df8a48d01`，`build-inputs-v2` source SHA-256 `17f750a156b27ce0f5e3cd7000bce731d203a8862524b31a620151cd0bbd8b27`、987 files。实时 inventory 重算与 manifest 一致；这是 Windows 本机 `P`，不升级为跨平台/CI `R`。fixture cleanup 首次遇 WebView lockfile `EBUSY`，既有重试成功，最终 exit 0。
- 安全与未验证：普通 refresh 只发布完整旧或完整新 catalog；mutation 生命周期有意保留“整批空 -> 整批新”两个 revision，不能描述成只推进一次 revision。handler 注册发生在 candidate build 阶段且不随 Registry 回滚，但未发布 ToolDef 时不可调用，故不得宣称 handler wiring 事务化。真实 AI provider/MCP process observation、Git 其余 mutation、recovery/manual-disposition、cross-caller owner proof、domain rollback、真实 CLI/CI、macOS/Linux packaged 与 npm high 修复仍为 `U`/红；下一步仍只推进 G33 的下一未完成 AC1/AC2 子切片。无 commit/push/tag/release。
- SSOT 回写：本节同步 prompt-12 §11、prompt-9 §8 与 prompt-11 §9。

## 13.23 G33 MCP 生命周期/TaskService 终止竞态修复与最终门禁复核（2026-08-16，Goal 仍进行中）

- 复核结论：H1 原始全平台范围**仍存在**（Linux 核心内容读写/变更为 `T`、Windows junction 交换为 `I`；macOS、RevealInOS、持续 CAS/换名竞争与公共 Workflow pathname loader 仍 `U`）；H2 **已不存在**（真实 Windows `cmd.exe` `Command`/`CommandContext` 双路径注入矩阵为 `I`）；H3/H4/M3 在已验证范围内**已不存在**。G33 缺口已变化但仍存在，AC 仍为 `0/6`；G28~G32 与 P12-BUG-01 仍未开始。
- 改动文件：`services/mcp_service.go`、`services/mcp_config.go`、`services/mcp_transport.go` 及 `services/agent_execution_mcp_process_test.go`。新增服务级 `transportLifecycleMu`，串行化 `Close`、`DisconnectServer`、`DeleteServer`、disable 与 workspace switch；`Close` 幂等并缓存同一 `closeErr`。stdio transport 由唯一 owner 负责 `Process.Wait`/kill，避免 ConnectServer 与 transport 双重终止；`persistTail` 在 teardown 完成前保持，防止并发 `SaveServer` 观察中间态。该修复是测试暴露的生产竞态，不能只改测试：detach 后由另一个 teardown 调用提前 cancel shutdown context 会与 Windows `Process.Kill`/`Wait` 竞争并产生 Access denied。当前不变量是“一个 transport 一个终止 owner；所有 teardown 在同一 lifecycle mutex 下完成；真实退出错误可观测且不被吞掉”。
- MCP 真实链路证据：`ConnectServer -> catalog ToolDef ID/revision -> workflow approval/capability -> TaskService -> renderer result -> 同一 configDir 重载 durable usage receipt -> Close -> ProcessState.Exited`。receipt reload 保持同一 `UnitID`/`ExternalReceiptID`；exit-17 自行退出错误仍可观察。真实 stdio/exit、Close×Disconnect/Delete/disable/workspace-switch 与 TaskService Stop 的定向 Windows 矩阵以 `go test ./services -run "Test(AgentMCPRealStdioHelper|TaskServiceWorkflowMCPRealStdioProcessUsesUnifiedPipeline|StdioTransportClosePreservesUnexpectedProcessExit|MCPService(ConcurrentCloseUsesSingleTransportOwner|CloseWaitsForConcurrentTeardownOwner|CloseObservesConcurrentTeardownFailure)|MCPWorkspaceRollbackSerializesConcurrentConfigSave|ProjectServiceMCPTeardownFailureRollsBackRootWithoutRevivingApproval|TaskServiceStop_(ConcurrentCallsAreIdempotent|AllowsTreeKillFallbackBeforeOuterDeadline|TerminatesChildProcessTree))$" -race -count=10 -timeout=15m` 复跑 exit 0（61.5s，Windows 本机 `I` 子证据）；这不是完整 `go test ./services -race` 结果。受控 transport 并发回归为 `T`，不外推为跨平台 I。
- TaskService 改动：`services/task_service.go` 将 Windows `taskkill` 子预算与外层 Stop 调用预算分离（`taskTerminationCommandLimit=2s`、`taskTerminationCallLimit=3s`），保留 single-flight termination 与 direct-kill fallback，修复高负载下两个相同 2 秒 deadline 互相截断的问题。子进程树测试的 PID 文件改为临时文件写入后 rename，读取端重试空内容/Windows transient sharing violation，失败 setup 也登记 cleanup；`services/workspace_edit_transaction_test.go` 将固定 `%TEMP%\outside.txt` 改为每测试唯一、仍位于 workspace root 外的 sibling 路径，消除跨 worktree/process 污染。
- 首次失败与修复：backend-gate 首轮唯一红灯是 `TestTaskServiceStop_TerminatesChildProcessTree` 的 Windows PID 文件短暂为空/共享锁占用，随后 TempDir cleanup 被 child 占用；保留该失败证据并加入原子 PID 发布、读取重试和失败 cleanup 后复跑通过。此前 MCP 真实测试的 `Close` 并发窗口还暴露了生产 teardown 双 owner 竞态；以 lifecycle mutex/single-owner 修复后，MCP 定向真实进程矩阵 `-race -count=10` 与 TaskService child-tree 定向 `-race -count=20` 均 exit 0。未删除测试或放宽断言。
- 全量验证：`$env:WAILS3_BIN='%USERPROFILE%\go\bin\wails3.exe'; node scripts/backend-gate.mjs` exit `0`（9/9，含 `go test ./...` 443.9s）；`task frontend:check` exit `0`（173/173 files、2765/2765 tests，ESLint 0 errors/1 existing warning、vue-tsc/bindings/docs 全绿）；`node scripts/check-bindings.mjs`、`node scripts/check-doc-links.mjs`、`node scripts/check-doc-numbers.mjs` exit `0`。
- 当前 packaged 证据：`node scripts/packaged-e2e.mjs` exit `0`；manifest `status=passed`/`phase=complete`，24 passed、0 failed、0 not-run，`artifactReused=false`、`sourceFingerprintStableAfterBuild=true`；`recordedAt=2026-08-16T13:06:40.486Z`、`completedAt=2026-08-16T13:12:23.677Z`；artifact SHA-256 `ef42e7af188ab76fedfd9745231ac3b17c0bae7d6135865dcc92f8cc483af0da`；`build-inputs-v2` source fingerprint `395ac8db26a924b1e4852143807009836b3a791ba98cbb31564ba3aeb0bc624a`（990 files）。这是 Windows 本机 `P`，不是跨平台/CI `R`；`screenshot=null`。
- 供应链与未验证边界：`node scripts/npm-audit-gate.mjs` 仍 exit `1`，G33 锁图命中 `nanoid <3.3.18` / `GHSA-2v37-7h3g-55p8` / 1 high；`frontend/package-lock.json` 前后 SHA-256 均为 `F1C0AED759A7A5DCCD0C58A2BB685B0C8CC94248D0ED6E36106DA00ADC6AC55F`。hardening worktree 的不同锁图绿灯不可替代。真实 AI provider、跨平台/远端 MCP、recovery/manual-disposition、cross-caller owner proof、domain rollback、真实 CLI/CI consumer 与 macOS/Linux packaged 仍 `U`；G33 AC1~AC6 全部保持 `[ ]`。本轮未修改 `.github/workflows`、`build/docker`、`frontend/package.json`、`frontend/package-lock.json` 或 Issue/PR/Release 元数据；无 commit/push/tag/release。
- SSOT 回写：本节同步 prompt-9 §8 与 prompt-11 §9；下一步仍只推进 G33 的下一未完成 AC，不能把本轮 I/P/T 子证据写成 Goal 完成。

## 13.24 G33 AC3 durable owner/usage authority recovery recheck (2026-08-17, Goal incomplete)

- 缺口状态：H1 **仍存在**（Linux 核心 FileService 读写/变更为 `T`、Windows junction 交换为 `I`；macOS、RevealInOS、持续 CAS/换名竞争与公共 Workflow pathname loader 仍 `U`）；H2 **已不存在**（真实 Windows `cmd.exe` `Command`/`CommandContext` 注入矩阵为 `I`）；G33 **已变化但仍未闭环**，AC 仍为 `0/6`；G28~G32 与 P12-BUG-01 仍未开始。
- 改动文件：`services/agent_lifecycle.go` 在 trusted usage 入口先反向解析 backend-owned opaque plan/goal runtime ID，再核对 logical session；持久化 owner 存在但 runtime registration 已撤销时 fail-closed 为 `ErrUnknownSession`，不因 durable row 单独重新授予 usage authority。`services/agent_lifecycle_recovery_concurrency_test.go` 将恢复处置并发下 owner 已被撤销的 `ErrUnknownSession` 明确列为允许的拒绝结果，保留 recovery/transition 断言。
- AC：AC3 `[ ] T/U`。本轮补强的是 durable owner 与 runtime authority 的 T 级边界；recovery/manual-disposition dispatcher、跨 window/caller owner proof、workspace/domain 后续 setter 失败时的事务恢复、workflow attempt 持久化、stream privacy/retention 与真实恢复 `I` 仍 `U`。AC1、AC2、AC4、AC5、AC6 继续 `[ ]`，G33 不得宣称完成。
- 首次失败与修复：完整 lifecycle/recovery race 组首次发现三项问题：opaque plan usage 未反向映射、published-unknown owner row 在 runtime 未注册时可被 retry 接受、恢复处置竞态测试未接受正确的 `ErrUnknownSession`。修复后同组 `-race -count=1` exit `0`；恢复处置并发/opaque usage/indeterminate retry 矩阵 `-race -count=10` exit `0`；`go test ./services -run "Test.*(Lifecycle|Agent|Usage|TaskService)" -race -count=1 -timeout=20m` exit `0`；`go test -race ./internal/agentcore -count=10 -timeout=15m` exit `0`。未删除测试、未放宽安全断言。
- 安全与数据：usage 记录不得把 renderer 或 provider 提供的 runtime ID 变成新的 logical owner；只有当前注册且 incarnation/fingerprint 匹配的 backend owner 才能写入 usage/observation。持久化发布后 durability indeterminate 继续 poison store，后续 mutation/retry 不得恢复旧 capability；恢复处置只允许终态 discard/人工路径，不自动恢复 authority。
- 当前门禁边界：上述源码变化使既有 packaged manifest 与此前 full-gate 记录失效，必须以本快照重新运行 backend/frontend/bindings/docs/packaged；`node scripts/npm-audit-gate.mjs` 的 G33 `nanoid <3.3.18` / `GHSA-2v37-7h3g-55p8` high advisory 仍保持红灯，未修改 lockfile。未修改 `.github/workflows`、`build/docker`、`frontend/package.json`、`frontend/package-lock.json` 或 Issue/PR/Release 元数据；无 commit/push/tag/release。
- 下一步：仍只推进 G33，先取得本源码快照的全量门禁与 packaged 证据，再决定 AC3 的下一 recovery/manual-disposition 子切片；不得把本轮 T 证据升级为 I/P/R。

## 13.25 G33 usage ledger synthetic UnitID collision fix (2026-08-17, Goal incomplete)

- 新发现与修复：当前 backend gate 的全仓 Go 测试首次唯一红灯来自 `TestAIPermission_RecordUsage_GetSummary` 的偶发少计（150/300 tokens）。根因是没有显式 UnitID 的 legacy usage rows 在同一纳秒时间戳下生成相同 `legacy-<timestamp>` ID，第二/第三条被 receipt monotonicity 当作 divergent terminal 而拒绝。`services/ai_permission_service.go` 现在用进程内原子序号追加 synthetic UnitID；`services/ai_permission_service_test.go` 新增相同时间戳三条记录回归。
- 验证：`go test ./services -run '^TestAIPermission_(RecordUsage_GetSummary|LegacyUsageIDsRemainUniqueForSameTimestamp)$' -count=50 -timeout=15m` exit `0`；Rust LSP `TestLanguagePackRealRustLSPToolchainAndDebug -count=3` exit `0`（缺 lldb-dap 子项按既有 skip）；TaskService child-tree/concurrent Stop `-race -count=10` exit `0`。首次 backend gate 仍记录为 exit `1`（Go 全量 353.6s），修复后必须重跑完整 gate。
- 状态边界：G33 仍 AC `0/6`，AC3 durable owner/runtime authority T 证据与 AC4 ledger collision T 回归均不构成 AC 勾选；H1 仍未闭环、H2 已关闭，npm `nanoid` high 仍红。未修改受保护 workflow/Docker/package-lock/治理元数据，无 commit/push/tag/release。

## 13.26 G33 terminal failure-observation authority boundary (2026-08-17, Goal incomplete)

- 全量诊断新增边界：`TestAIGoalService_IterationCheckpointFailureRecordsFailedIteration` 要求一个已被可信 domain terminalize 的当前-incarnation owner 记录“已准入但 checkpoint 失败”的 usage。此前为防止 unregistered retry 的检查把该失败观察一并拒绝，账本为空。
- 修复：`services/agent_lifecycle.go` 对 completed row 仅使用 durable owner claim（opaque mapping 在 Complete 时已撤销）验证当前 incarnation；只允许 `Success=false` 的 trusted failure observation 进入 usage ledger，`Success=true` 仍 fail-closed 为 `ErrInvalidSessionTransition`，不恢复或注册 runtime。`services/agent_lifecycle_usage_owner_test.go` 新增成功/失败双向回归。
- 验证：Goal checkpoint failure、completed terminal usage、published-unknown retry 定向矩阵 `-race -count=20` exit `0`；Agent/Usage/TaskService 组 `-race -count=1` exit `0`；`internal/agentcore -race -count=10` exit `0`。第二次 backend gate 全量 Go 测试曾唯一失败该 Goal 用例（363.5s），修复后需重跑完整 gate。
- 状态边界：G33 仍 `0/6`，AC3/AC4 只有 T 子证据；recovery/manual-disposition、cross-caller/domain rollback、真实 provider/CLI/CI、跨平台 packaged 与 npm high advisory 仍 U/红。未修改受保护文件，无 commit/push/tag/release。

## 13.27 G33 latest full gates and packaged first-failure/reuse evidence (2026-08-17, Goal incomplete)

- 缺口状态：H1 **仍存在**（Linux 核心 FileService 为 `T`、Windows junction 为 `I`；macOS、RevealInOS、持续 CAS/换名竞争与公共 Workflow pathname loader 仍 `U`）；H2、H3、H4、M3 在已验证范围内**已不存在**。G33 **已变化但仍未闭环**，AC `0/6`；G28~G32 与 P12-BUG-01 仍未开始。
- 当前源码门禁：`node scripts/backend-gate.mjs` 9/9、exit `0`（全仓 Go test 约 490.4s）；`task frontend:check` exit `0`（173/173 files、2765/2765 tests，ESLint 0 errors/1 个既有 warning，vue-tsc、bindings、docs 全绿）；独立 `node scripts/check-bindings.mjs`、`node scripts/check-doc-links.mjs`、`node scripts/check-doc-numbers.mjs` 均 exit `0`。lifecycle/usage/Agent/TaskService 与 `internal/agentcore` race 矩阵、Rust LSP `-count=3`、TaskService Stop `-race -count=10` 均通过；缺失 `lldb-dap` 的子项按既有规则 skip，未伪造覆盖。
- packaged 首次失败：完整构建运行的 manifest 在 `recordedAt=2026-08-17T04:45:35.518Z`、`completedAt=2026-08-17T04:48:44.555Z` 记录前 5 个 fixture passed、`terminal-exit-package` failed、其余 18 not-run，exit `1`；错误为 `start cmd shell: path <temp>\\workspace is outside the workspace root`。失败时已构建 artifact SHA-256 为 `4fafb79db047c12d4ae49683fac7bd34898352e848928adb3c1873ee2b0b88d7`，source fingerprint 为 `c81e6a1c1db120c79c8821841b414be6c62a35887dce2796c440183c547cb84c`（998 files）。该红灯不得被后续通过删除或改写。
- 当前权威 packaged manifest：两次之间没有源码改动；严格 skip-build/reuse 校验 source scope/fingerprint/file count、artifact path/SHA-256、Wails version、build tags 与 renderer markers 后，复用同一 artifact 完成 24/24。当前 `build/e2e-evidence/packaged-e2e/manifest.json` 为 `status=passed`、`phase=complete`、24 passed / 0 failed / 0 not-run、`artifactReused=true`、`artifactReuseSourceRecordedAt=2026-08-17T04:45:35.518Z`、`sourceFingerprintStableAfterBuild=true`；recordedAt `2026-08-17T05:13:32.628Z`、completedAt/sourceFingerprintVerifiedAt `2026-08-17T05:17:13.566Z`。artifact/source digest 与首次运行相同。这是 Windows 本机 `P`，不是重新编译证明、跨平台证据或 CI `R`。
- 首次失败研判：同一运行中 `Terminal.Start("default")` 已成功使用该 workspace，约 288ms 后 G16 probe 才判 outside；`KillSession` 不改变 workspace root，项目持久化只发现一个项目，也未发现第二次成功 `AddProject`。同 artifact/source 的 reuse 无法稳定复现，因此当前只能记录 workspace-root/late-setter 可重复性风险，不能通过 probe 内重新 `AddProject`、空 cwd 或放宽 root 校验掩盖。若再次复现，应先补 deterministic late-setter/rollback 失败测试与 workspace commit barrier；E2E-only 诊断可附 generation/root identity，但不得泄露完整本地绝对路径。
- AC/供应链/未验证：AC1~AC6 全部保持 `[ ]`；本轮 full gates 与 Windows `P` 只更新 AC6 子证据。`node scripts/npm-audit-gate.mjs` 仍因当前树 `nanoid <3.3.18` / `GHSA-2v37-7h3g-55p8` / 1 high 而 exit `1`，lockfile 未改；hardening 独立 worktree 的不同锁图、release/governance 绿灯不可移植。recovery/manual-disposition、cross-caller ownership、domain rollback、真实 provider/CLI/CI、macOS/Linux packaged 与前述 H1 `U` 仍未闭环。
- SSOT 与边界：本节同步 prompt-12 §11、prompt-9 §8 与 prompt-11 §9。本子切片未修改 `.github/workflows`、`build/docker`、`frontend/package.json`、`frontend/package-lock.json` 或 Issue/PR/Release 元数据；无 commit/push/tag/release。下一步仍只推进 G33。

## 13.28 G33 recovery/manual-disposition dispatcher boundary and fresh packaged evidence (2026-08-17, Goal incomplete)

- 缺口状态：H1 **仍存在**（Linux 核心 FileService 读写/变更为 `T`、Windows junction 为 `I`；macOS、RevealInOS、持续 CAS/换名竞争与公共 Workflow pathname loader 仍 `U`）；H2、H3、H4、M3 在已验证范围内**已不存在**。G33 **已变化但仍未闭环**，AC `0/6`；G28~G32 与 P12-BUG-01 **仍未开始**。
- 本子切片行为：`services/agent_lifecycle.go` 的 trusted recovery dispatcher 只接受显式 `discard`，不提供 resume、不注册 Wails service。公开 list/result 只返回 keyed HMAC 的稳定 opaque handle、kind/status/time/count，不返回 logical session ID、owner、root、stream 或 checkpoint；request 只接受 handle + disposition，解析、workspace lease、owner fingerprint、incarnation、runtime registration 与 disposition 在同一 `transitionMu` 临界区复核。`internal/agentcore/session.go` 在任何幂等成功前先检查 persistence poison；pre-publish 失败保留 quarantine 并允许同 handle 重试，post-publish durability unknown 则 poison 当前 store、拒绝同进程 inventory/replay，fresh reload 后才确认 durable discard。
- 改动文件：`internal/agentcore/session.go`、`internal/agentcore/session_test.go`、`internal/agentcore/session_persistence_commit_test.go`、`internal/agentcore/session_recovery_concurrency_test.go`、`services/agent_lifecycle.go`、`services/agent_lifecycle_persistence_authority_test.go`、`services/agent_lifecycle_recovery_unscoped_test.go`、`services/agent_lifecycle_recovery_dispatcher_test.go`、`services/agent_lifecycle_recovery_dispatcher_fault_test.go`、`services/agent_lifecycle_recovery_concurrency_test.go`。未修改 `.github/workflows`、`build/docker`、`frontend/package.json`、`frontend/package-lock.json` 或 Issue/PR/Release 治理元数据。
- 首次失败与修复：安全红测先暴露 logical ID/path/secret-marker 泄露、post-rename unknown 后 caller 被误报成功、poison inventory 返回空且 nil，以及 workspace switch 与 discard 交错时可能跨 workspace。修复为 HMAC opaque handle、public error 脱敏、先 poison 后幂等、checked inventory、同锁 workspace/owner 复核；新增 workspace-switch race 只允许 durable discard 或原 quarantine + `ErrNotAllowed`，不得产生 hybrid authority。未删除测试、未放宽安全断言。
- 定向验证：`go test ./services -run '^TestAgentRecoveryDispatcherDiscardRacesWorkspaceSwitch$' -race -count=1 -v` exit `0`；services recovery/lifecycle/dispatcher 矩阵 `-race -count=20` exit `0`（25.873s）；`go test ./internal/agentcore -run 'Test.*(Recovery|Persistence|Disposition|Publication|Poison)' -race -count=20` exit `0`（31.781s）；`go vet ./services ./internal/agentcore`、gofmt 与 G33 scoped `git diff --check` exit `0`。本地 Wails `v3.0.0-alpha2.111` 复用后 `node scripts/check-bindings.mjs` exit `0`，manifest/generated tree 一致；`scripts/wails-bindings.test.mjs` 16/16 通过。
- 全量门禁：`node scripts/backend-gate.mjs` 9/9、exit `0`，其中 `go test ./... -count=1` 516.6s；`task frontend:check` exit `0`，Vitest 173/173 files、2765/2765 tests，ESLint 0 errors/1 existing warning，vue-tsc、bindings、docs 全绿；根目录 `npm run frontend:check` 的首次 ENOENT（根无 `package.json`）保留为入口失败，按仓库权威 Taskfile 重跑。独立 `node scripts/npm-audit-gate.mjs` 仍 exit `1`：official registry/lock stability 通过，但 `nanoid <3.3.18` / `GHSA-2v37-7h3g-55p8` 为 1 high，未修改 lockfile。
- 最新 packaged：`node scripts/packaged-e2e.mjs` 强制 fresh build，Wails `v3.0.0-alpha2.111` 匹配；manifest `status=passed`/`phase=complete`、24/24 passed、0 failed/0 not-run、`artifactReused=false`、`sourceFingerprintStableAfterBuild=true`，artifact SHA-256 `4dd46705c402d414170a65f9c1e4f1d9650fdfa22e38009e737b5a2e91a1508b`，`build-inputs-v2` source fingerprint `a51f0db27cbf708baf3c6d3c3138453384ea55a0e1c85d26baa0ff86c0f1f23b`（1000 files），recordedAt `2026-08-17T07:23:39.748Z`、completedAt `2026-08-17T07:29:36.987Z`，exit `0`。一次 Windows WebView 临时目录 `EBUSY` cleanup retry 仍以最终 exit `0` 收束；fresh artifact/source 与 manifest 完全一致。这是 Windows 本机 `P`，不升级为跨平台/CI `R`。
- AC 与未验证：AC1 `[ ] T/I/U`、AC2 `[ ] T/U`、AC3 `[ ] T/U`、AC4 `[ ] T/U`、AC5 `[ ] T/U`、AC6 `[ ] T/P/U`；本切片只取得 backend-only recovery/manual-disposition 的 `T` 子证据，真实 operator/CLI consumer、cross-window/caller owner proof、真正 resume、domain rollback、真实 provider/CI、macOS/Linux packaged 与 npm high 修复仍 `U`/红。G33 保持 `0/6`，无 commit/push/tag/release。
- SSOT 回写：本节同步 prompt-12 §11、prompt-9 §8 与 prompt-11 §9；下一步仍只推进 G33 的下一未完成 AC1/AC2 子切片。

## 13.29 G33 external receipt recovery identity/authority boundary and final shared-tree evidence (2026-08-17, Goal incomplete)

- 缺口状态：H1 原始全平台范围**仍存在**（Linux 核心 FileService `T`、Windows junction `I`；macOS、RevealInOS、持续 CAS/换名竞争与公共 Workflow pathname loader `U`）；H2 在真实 Windows `cmd.exe` command/commandContext 注入矩阵范围内**已不存在**。G33 **已变化但仍未闭环**，AC `0/6`；本轮只增加 AC2/AC3/AC4 的 backend-only `T` 子证据。
- external receipt 边界：新增独立 `AgentExternalReceiptRecoveryDispatcher`，只供 trusted bootstrap/headless 持有且未注册 Wails。inventory/result 只暴露稳定 opaque HMAC handle、状态和时间；请求只接受精确 `manual-unknown`。处置不调用 adapter、不声称 resume/rollback/compensation，而是以原 `UnitID`/`ExternalReceiptID` 发布诚实的 unresolved/unknown terminal row。session recovery 的 `AgentRecoveryDispatcher` 仍是另一能力且只接受 `discard`，两者不得混写。
- identity/authority：per-config receipt identity key 受跨进程锁保护，严格接受 64 hex；Unix 创建/读取要求 `0600`。只有 legacy-only usage ledger 可首次创建 key；一旦存在 external receipt 历史，key 缺失、损坏或宽权限会 poison，禁止重建/轮换，新 external mutation 在 handler 副作用前拒绝。Plan/Goal opaque runtime ID 在持久化前归一为 logical lifecycle ID；completed lifecycle 的 pending receipt 仅在旧 runtime authority 已撤销后可处置。owner incarnation、workspace fingerprint/generation 与 runtime authority 在发布期间受同一 workspace generation guard 复核。
- 持久化/错误合同：pre-publish 失败保持 pending 且同 handle 可重试；post-publish durability unknown poison 当前进程，必须 fresh reload 后确认。公开 usage/recovery 错误投影为固定 sentinel，不泄漏 configDir、路径、receipt ID 或底层存储详情。全量首败 `TestProductionRuntimeUsageReceiptPreventsWriteWhenLedgerUnavailable` 仍匹配旧错误文本；生产 fail-closed 正确。测试改为先完成干净加载，再用目录占住 `usage_log.jsonl`，精确覆盖 pre-publication `ErrUsagePersistence`，同时断言 handler 未写文件、内存 ledger 为空且公开结果不泄漏路径。
- 改动范围：`services/agent_external_receipt_recovery.go`、`services/agent_external_receipt_recovery_dispatcher.go`、`services/agent_external_receipt_recovery_test.go`、`services/agent_lifecycle.go`、`services/ai_permission_service.go`、`services/ai_permission_service_test.go`、`services/agent_execution_core.go`、`services/agent_execution_core_test.go`、`services/errors.go`、`services/agent_lifecycle_test.go`。未修改 `.github/workflows`、`build/docker`、`frontend/package.json`、`frontend/package-lock.json` 或 Issue/PR/Release 元数据。
- 定向与全量验证：external receipt/AIPermission/public-redaction 定向测试、services recovery/lifecycle/usage-owner `-race`、`go vet ./services ./internal/agentcore` 与 `go test ./internal/agentcore -count=1` 均 exit `0`；`go test ./services -count=1` exit `0`（194.290s）。同一稳定共享树的 `node scripts/backend-gate.mjs` 9/9 exit `0`，其中全仓 Go test 198.4s；`task frontend:check` exit `0`，Vitest 173/173 files、2765/2765 tests，ESLint 0 errors/1 existing warning，vue-tsc、bindings 16/16 与 docs 全绿。
- 首次环境失败：未显式传本地固定 Wails 时，frontend/backend/packaged 的 bindings 生成尝试访问 `proxy.golang.org` 并超时；改用已核验的 `%USERPROFILE%\go\bin\wails3.exe`（精确 `v3.0.0-alpha2.111`）后通过。另一次 packaged 构建与共享树中已运行的 packaged 进程并发写 `frontend/dist`，Vite 复制 `vue.svg` 时 `EBUSY`，0/24 fixture 启动；未终止对方进程、未把该红灯归因给代码，单 owner 联动运行随后通过。
- 当前 packaged/供应链：fresh manifest 24/24 passed、0 failed/0 not-run、`artifactReused=false`、source build 后稳定；artifact `55d90e32f5d36c1412a6495e8aa7318a83e0dc3b96e9bd91a7944c6006c7f103`，source `78e8c29ac5370f2d9010e6dd06e7f8e6b681ce0e5c7f2b6298830c7423220d06`（1003 files），recordedAt `2026-08-17T09:24:50.209Z`、completedAt `2026-08-17T09:28:32.731Z`。这是 Windows 本机 `P`，不是跨平台/CI `R`。`node scripts/npm-audit-gate.mjs` 仍 exit `1`：official registry/lock stability 通过，唯一失败为 `nanoid <3.3.18` / `GHSA-2v37-7h3g-55p8` / 1 high；lockfile 未改，hardening 另一锁图绿灯不可替代。
- AC 与下一步：AC1 `[ ] T/I/U`、AC2 `[ ] T/I/U`、AC3 `[ ] T/U`、AC4 `[ ] T/U`、AC5 `[ ] T/U`、AC6 `[ ] T/P/U`。真实 operator/CLI、真正 resume、adapter-specific compensation、ambiguous commit 处置、跨进程 durable single-writer/CAS、cross-window/caller owner proof、domain rollback、真实 provider/CI 与 macOS/Linux packaged 仍 `U`。G33 保持 `0/6`；无 commit/push/tag/release。

## 13.30 G33 durable workflow attempt authority and fresh packaged evidence (2026-08-17, Goal incomplete)

- 缺口状态：H1 原始全平台范围**仍存在**（Linux 核心 FileService `T`、Windows junction `I`；macOS、RevealInOS、持续 CAS/换名竞争与公共 Workflow pathname loader `U`）；H2 在真实 Windows `cmd.exe` command/commandContext 注入矩阵范围内**已不存在**；H3/H4/M3 在已验证范围内**已不存在**。G33 **已变化但仍未完成**，AC `0/6`；G28~G32 与 P12-BUG-01 仍未开始。
- durable attempt authority：`TaskService` 删除 workflow attempt 的进程内 map 权威，改由 durable usage ledger 提供唯一事实源；`workflowAttemptMu` 串行同一服务实例内的 Begin/Resume/Complete/Fail 短状态迁移。完成只接受同 session 恰好一条 canonical `workflow/workflow.attempt` pending row，并复用其原 UnitID/StartedAt；多条 pending、同时存在其他 pending unit、错误 kind/operation/provider/cost 字段、poison 或缺失均 fail-closed。公开错误不包含 UnitID、账本路径或底层持久化详情。
- terminal 顺序与恢复边界：成功完成在同一 attempt 临界区内按 durable usage terminal -> checkpoint -> lifecycle terminal 顺序发布；pre-publication usage terminal 写失败不会消费原 pending receipt，内部重试仍使用同 UnitID。TaskService 重建可从同一 ledger 完成 attempt；完整 lifecycle/permission 重载虽能找到该 attempt，但旧 runtime authority 保持撤销并拒绝完成，不会因 ledger 存在而伪造 resume。两个并发 Complete 只有一个成功 terminal。该证据不等于跨进程 CAS、operator recovery 或公共自动重试。
- 改动范围：`services/task_service.go`、`services/ai_permission_service.go`、新增 `services/task_service_workflow_attempt_persistence_test.go`。未修改 `.github/workflows`、`build/docker`、`frontend/package.json`、`frontend/package-lock.json` 或 Issue/PR/Release 元数据。
- 首次失败与修复：新增红测稳定复现 TaskService 重建后 `workflow usage attempt is missing`、terminal ledger 首次写失败后原内存 map 丢失 receipt，以及同 session 两条 pending 时旧实现仍错误成功。改为 ledger lookup 与严格 canonical/唯一性检查后转绿；未删除测试、未放宽状态机或安全断言。
- 定向验证：workflow attempt 矩阵 `go test ./services -run '^TestTaskServiceWorkflowAttempt' -race -count=10`、workflow/lifecycle/usage/external recovery 组合 `-race -count=10`、全部 `TestTaskService*` `-race`、`go test ./internal/agentcore -race -count=10`、`go vet ./services ./internal/agentcore`、gofmt 与 scoped diff check 均 exit `0`。
- 全量门禁：首次 `node scripts/backend-gate.mjs` 的全仓 Go 腿 380.6s 通过，但 Wails 联网安装超时，整体 exit `1`；显式使用已核验的 `%USERPROFILE%\go\bin\wails3.exe`（`v3.0.0-alpha2.111`）重跑后 9/9、exit `0`，全仓 Go test 206.6s。`task frontend:check` exit `0`：Vitest 173/173 files、2765/2765 tests，ESLint 0 errors/1 existing warning，vue-tsc、bindings 16/16 与 docs 全绿。
- 当前 packaged/供应链：fresh `node scripts/packaged-e2e.mjs` exit `0`，manifest 24/24 passed、0 failed/0 not-run、`artifactReused=false`、`sourceFingerprintStableAfterBuild=true`；artifact `65ca828752f20590edfd88987c84a1548e99167d79f71e909b70192ca3098300`，`build-inputs-v2` source `d6033de324af040178161221fb6890041a0cc064a284cceed45974cc1abf84d3`（1004 files），recordedAt `2026-08-17T10:25:06.435Z`、completedAt `2026-08-17T10:28:31.707Z`。WebView lockfile cleanup 首次 `EBUSY` 后由既有 bounded retry 收束，artifact 进程已退出。这是 Windows 本机 `P`，不是跨平台/CI `R`。`node scripts/npm-audit-gate.mjs` 仍 exit `1`，唯一 high 为 `nanoid <3.3.18` / `GHSA-2v37-7h3g-55p8`；lock SHA-256 前后均为 `F1C0AED759A7A5DCCD0C58A2BB685B0C8CC94248D0ED6E36106DA00ADC6AC55F`，未修改锁文件。
- AC 与下一步：AC1 `[ ] T/I/U`、AC2 `[ ] T/I/U`、AC3 `[ ] T/U`、AC4 `[ ] T/U`、AC5 `[ ] T/U`、AC6 `[ ] T/P/U`。workflow attempt persistence 的本地 durable/reload 子边界已从 `U` 升为 `T`，但 cross-window/caller owner proof、真正 resume、workspace/domain setter rollback、stream privacy/retention、跨进程 durable single-writer/CAS、真实 operator/CLI/provider/CI 与 macOS/Linux packaged 仍 `U`。G33 保持 `0/6`；无 commit/push/tag/release。

## 13.31 G33 AgentService.Close renderer-surface audit and fresh shared gates (2026-08-18, Goal incomplete)

- 缺口状态：H1 原始全平台范围**仍存在**（Linux 核心 FileService `T`、Windows junction `I`；macOS、RevealInOS、持续 CAS/换名竞争与公共 Workflow pathname loader `U`）；H2 在真实 Windows `cmd.exe` `command`/`commandContext` 注入矩阵范围内**已不存在**。H3/H4/M3 在已验证范围内**已不存在**。G33 **已变化但仍未闭环**，AC `0/6`；G28~G32 与 P12-BUG-01 仍未开始。
- renderer surface 收口：`services/agent_service.go` 为 `AgentService.Close` 增加 `//wails:ignore`；`scripts/lib/wails-bindings.mjs` 与 `scripts/wails-bindings.test.mjs` 增加 forbidden export 合约；锁定 `%USERPROFILE%\go\bin\wails3.exe` `v3.0.0-alpha2.111` 重新生成 manifest/bindings，`agentservice.ts` 不再导出 `Close`；`bindings_runtime_surface_test.go` 增加按模块 ignored runtime Close 断言，未放宽 secrets forbidden。生成文件来自锁定版本，不手调 binding。
- 持久化/审计边界：root-backed usage ledger 在写入、关闭、root-relative identity 复核、state-root sync 和最终 identity 复核任一不确定时返回固定 persistence sentinel，并 poison 当前进程；headless audit、external receipt manual disposition 与 root-bound `InstanceLock` 均保留 poisoned 分类，失败 `Release` 结果稳定缓存，后续调用不会吞错。该边界仍不等于跨进程 CAS 或 H1 全平台闭环。
- 改动范围：`services/agent_service.go`、`services/ai_permission_service.go` 及其测试、`services/agent_external_receipt_recovery.go`、`services/agent_headless.go`/测试、`services/instance_lock.go`、`services/agent_state_root_test.go`；`scripts/lib/wails-bindings.mjs`、`scripts/wails-bindings.test.mjs`、`bindings_runtime_surface_test.go` 与锁定版本生成 bindings/manifest。未修改 `.github/workflows`、`build/docker`、`frontend/package-lock.json` 或 Issue/PR/Release 元数据。
- 首次失败与修复：backend gate 的 services 时序首轮暴露 runtime surface 仍导出 `AgentService.Close`；加入 Wails ignored-method forbidden export 合约并重新生成锁定版本 bindings 后转绿。全局 `git diff --check` 仍只被既有 `build/scripts/build-msi.ps1` EOF blank line 阻塞；本切片 scoped diff-check clean，未越界修改受保护文件。
- 验证：定向 core/service audit `-race`、真实 headless CLI `-race -count=3`、`go vet`、gofmt 与 scoped diff-check 均 exit `0`；固定 Wails 后 `node scripts/backend-gate.mjs` 权威 9/9 exit `0`（全仓 Go test 219.4s）；`task frontend:check` 173/173 files、2765/2765 tests，ESLint 0 errors/1 existing warning，vue-tsc、bindings、docs 全绿。
- packaged/供应链：fresh `node scripts/packaged-e2e.mjs` exit `0`，manifest 24/24 passed、0 failed/0 not-run、`artifactReused=false`，artifact SHA-256 `895a351f0f9062c811261bd94605bf9f1ad4bb70b47a8498cdb97e4fd4e05cf9`，source fingerprint `8b0f4d1294247e8422ffcd8b041d91386895de6d2b55b01fd15bd15de756f536`（1020 files），实时重算与 manifest 完全匹配，completedAt `2026-08-18T05:03:44.005Z`。这是 Windows 本机 `P`，不升级为跨平台/CI `R`。`node scripts/npm-audit-gate.mjs` 仍 exit `1`，唯一 high 为 `nanoid <3.3.18` / `GHSA-2v37-7h3g-55p8`；lockfile 未改，hardening 另一锁图不可替代。
- AC 与未验证：AC1 `[ ] T/I/U`、AC2 `[ ] T/I/U`、AC3 `[ ] T/U`、AC4 `[ ] T/U`、AC5 `[ ] T/U`、AC6 `[ ] T/P/U`。真实 operator/CLI、真正 resume、adapter-specific compensation、cross-window/caller owner proof、domain rollback、跨进程 durable single-writer/CAS、真实 provider/CI 与 macOS/Linux packaged 仍 `U`；G33 保持 `0/6`，无 commit/push/tag/release。
- SSOT 回写：本条同步 prompt-9 §8 与 prompt-11 §9；下一步仍只推进 G33 的下一未完成 AC，不把本机 packaged 或 bindings contract 证据升级为 AC 完成。

## 13.32 G33 trusted headless CLI external-receipt recovery and opaque owner routing (2026-08-18, Goal incomplete)

- 缺口状态：H1 原始全平台范围**仍存在**（Linux 核心 FileService `T`、Windows junction `I`；macOS、RevealInOS、持续 CAS/换名竞争与公共 Workflow pathname loader `U`）；H2 在真实 Windows `cmd.exe` `command`/`commandContext` 注入矩阵范围内**已不存在**。G33 **已变化但仍未闭环**，AC `0/6`；G28~G32 与 P12-BUG-01 仍未开始。
- trusted CLI 边界：`internal/agentcli` 新增 `external-receipts` inventory 与 `external-receipt-dispose` 命令，复用生产 `HeadlessAgentHost`/lifecycle，不注册 Wails、不创建 renderer authority。处置只接受精确 `manual-unknown`；`resume`、空白变体、重复/跨命令 flags 在打开 state 前即 `invalid-input`。输出只含 opaque handle、状态、时间与固定结果类别，不含 UnitID、ExternalReceiptID、session、workspace/state 路径或 receipt metadata。
- lifecycle/owner：`services/agent_headless.go` 的 inventory/dispatch wrapper 通过 operation lease 与 `Close` 等待机制保护 root-backed lifecycle；`services/agent_execution_session.go` 与 adapters 统一把 Plan/Goal logical lifecycle ID 解析为 backend-owned opaque runtime session ID，避免 renderer-facing ID 直接作为 capability authority。
- 改动文件：`services/agent_headless.go`、`services/agent_headless_cli_process_test.go`、`internal/agentcli/main.go`、`internal/agentcli/main_test.go`、`services/agent_execution_session.go`、`services/agent_execution_session_test.go`、`services/executor_adapters.go`。未修改 `.github/workflows`、`build/docker`、`frontend/package.json`、`frontend/package-lock.json` 或 Issue/PR/Release 元数据。
- 首次失败与修复：首次权威 backend gate 的 bindings 步骤因尝试从 `proxy.golang.org` 安装锁定 Wails 而 exit `1`；核验本机 `%USERPROFILE%\go\bin\wails3.exe` 精确为 `v3.0.0-alpha2.111` 后，以 `WAILS3_BIN` 重跑 backend gate 9/9 转绿。未手调 bindings、未放宽安全断言。
- 定向验证：`go test -race ./internal/agentcli -count=1` exit `0`；CLI recovery 与 Plan/Goal owner 矩阵 `go test -race ./services -run '^(TestHeadlessAgentExternalReceiptRecoveryCLIProcess|TestHeadlessAgent(CLIProcess|Host)|TestExecutionSessionIDResolvesOpaquePlanOwner|TestDefaultStepExecutorUsesOpaquePlanOwner)$' -count=3 -timeout=20m` exit `0`（57.160s）；`go vet ./internal/agentcli ./services`、gofmt 与 G33 scoped `git diff --check` exit `0`。
- 全量门禁：固定 Wails 后 `node scripts/backend-gate.mjs` 9/9 exit `0`（Go test 357.2s）；`task frontend:check` exit `0`，Vitest 173/173 files、2765/2765 tests，ESLint 0 errors/1 existing warning，vue-tsc、bindings、docs 全绿；独立 `node scripts/check-bindings.mjs`、`check-doc-links.mjs`（25 Markdown）、`check-doc-numbers.mjs` 均 exit `0`。根目录没有 `package.json`，因此 `npm run frontend:check` 不是本仓库入口；Taskfile 命令是权威前端门禁。
- packaged/供应链：fresh `node scripts/packaged-e2e.mjs` exit `0`，manifest `status=passed`/`phase=complete`、24/24 passed、0 failed/0 not-run、`artifactReused=false`、`sourceFingerprintStableAfterBuild=true`；artifact SHA-256 `86f89d0bace05b6660f867b24713f251448ebebd10db52b33461712d695b1cb9`，`build-inputs-v2` source fingerprint `91cabc5ecd4a0d9f70901b53537074fdbba0d21afcf68172cc26b596a7b3f148`（1023 files），completedAt `2026-08-18T13:33:33.636Z`。这是 Windows 本机 `P`，不升级为跨平台/CI `R`。`node scripts/npm-audit-gate.mjs` 仍 exit `1`，唯一 high 为 `nanoid <3.3.18` / `GHSA-2v37-7h3g-55p8`；lock SHA-256 前后均 `F1C0AED759A7A5DCCD0C58A2BB685B0C8CC94248D0ED6E36106DA00ADC6AC55F`。
- AC 与未验证：AC1 `[ ] T/I/U`、AC2 `[ ] T/I/U`、AC3 `[ ] T/U`、AC4 `[ ] T/U`、AC5 `[ ] T/U`、AC6 `[ ] T/P/U`。真实 operator/CLI 的跨平台与 CI consumer、真正 resume、adapter-specific compensation、cross-window/caller owner proof、domain rollback、跨进程 durable single-writer/CAS、真实 provider/CI 与 macOS/Linux packaged 仍 `U`；G33 保持 `0/6`，无 commit/push/tag/release。
- SSOT 回写：本条同步 prompt-11 §9 与 prompt-9 §8；下一步仍只推进 G33 的下一未完成 AC，不把本机 CLI/P packaged 或 contract 证据升级为 Goal 完成。

## 13.33 G33 workspace reset owner-map rollback boundary (2026-08-18, Goal incomplete)

- 缺口状态：H1 原始全平台范围**仍存在**（Linux 核心 FileService `T`、Windows junction `I`；macOS、RevealInOS、持续 CAS/换名竞争与公共 Workflow pathname loader `U`）；H2 在真实 Windows `cmd.exe` `command`/`commandContext` 注入矩阵范围内**已不存在**。G33 **已变化但仍未闭环**，AC `0/6`；G28~G32 与 P12-BUG-01 仍未开始。
- 失败边界：旧实现先清空 `sessionOwners/sessionSkills`，再调用 `SessionStore.CloseAllDurable`。当保存返回 `PersistenceNotPublished` 时，SessionStore 会恢复旧 rows/runtime，但 owner map 已丢失，原 caller 随后无法继续申请 capability 或关闭其 session。新增受控 persistence 红测复现该窗口。
- 修复：`services/agent_execution_core.go` 的 `clearAgentSkillSessions` 现在先完成 lifecycle reset，再按结果清理绑定；成功后清理，`PersistenceNotPublished` 保留旧 maps 与 runtime，`PersistencePublishedDurabilityUnknown`/poison 在 lifecycle 已撤销 authority 后清空 maps。`setWorkspaceRoot`/`restoreWorkspaceRoot` 的 root/generation 回滚保持原行为。未修改 `.github/workflows`、`build/docker`、frontend manifest/lock 或 Issue/PR/Release 元数据。
- 测试：新增 `services/agent_workspace_reset_owner_test.go`，受控 `SessionStorePersistence` 覆盖 pre-publication 与 published-unknown 两分支；前者断言 root、runtime session 与原 caller owner 保留，后者断言 root 回滚、runtime 未注册且 owner fail-closed。测试不使用固定全局 temp pathname。
- 验证：首轮红测 `TestAgentWorkspaceResetPrePublicationPreservesRendererOwner` exit `1`（owner map 被提前清空）；修复后 `go test ./services -run 'TestAgentWorkspaceReset(PrePublicationPreservesRendererOwner|IndeterminateRevokesRendererOwner)$' -race -count=5` exit `0`（2 tests x5）；owner/session 与 `internal/agentcore` 相关矩阵、`go vet ./services`、scoped `git diff --check` 均 exit `0`。由于本切片源码刚变化，既有 backend/frontend/packaged manifest 属历史，需对当前静止树重跑权威门禁。
- AC 与未验证：AC1 `[ ] T/I/U`、AC2 `[ ] T/I/U`、AC3 `[ ] T/U`（本条仅取得 workspace reset owner rollback 的 T 子证据）、AC4 `[ ] T/U`、AC5 `[ ] T/U`、AC6 `[ ] T/P/U`。真正 cross-window/caller/domain rollback、resume/adapter compensation、跨进程 CAS、真实 provider/CI/CLI 与 macOS/Linux packaged 仍 `U`；G33 保持 `0/6`，无 commit/push/tag/release。
- SSOT 回写：本条同步 prompt-9 §8 与 prompt-11 §9；下一步仍只推进 G33 的下一未完成 AC，不把本机 packaged 或 bindings contract 证据升级为 Goal 完成。

- 当前复跑覆盖：上述“需对当前静止树重跑”是首轮交付时状态；本轮已完成 full Go/frontend/bindings/docs 与 fresh packaged 复核。自动 Wails 安装的网络失败、npm high 与全局 `build-msi.ps1` EOF `diff --check` 红灯均如实保留；受影响范围 scoped `diff --check`、文档链接/编号仍 exit `0`。未 commit/push/tag/release。

## 13.34 G33 workspace authority catalog-admission closure and final fresh packaged (2026-08-19, Goal incomplete)

- 缺口状态：H1 原始全平台范围**仍存在**（Linux 核心 FileService 读写/变更 `T`、Windows NTFS junction 交换 `I`；macOS、RevealInOS、持续 CAS/换名竞争与公共 Workflow pathname loader `U`）；H2 在真实 Windows `cmd.exe` `Command`/`CommandContext` 注入矩阵范围内**已不存在**（`I`）。H3/H4/M3 在已验证范围内**已不存在**。G33 **已变化但仍未闭环**，AC `0/6`；G28~G32 与 P12-BUG-01 未开始，P12-BUG-01 缺口仍存在。
- 首次失败与修复：新增 `TestProjectServiceWorkspaceSwitchDrainsRefreshAdmissionBeforeSetters` 后，旧实现首跑稳定 exit `1`：refresh 在 deferral 检查后、取得 `catalogRefreshMu` 前被暂停，workspace setter 越过空 drain 并先行。修复将普通 refresh、Workflow mutation refresh 与 Skill mutation refresh 统一改为在 `catalogRefreshMu` 临界区内完成 deferral 判定；Project authority 仍在首个 setter 前取得 workflow/workspace 写屏障，并在 setter/账本保存/快照发布期间保持。修复后该红测 `go test ... -race -count=10` exit `0`，workspace/lifecycle/catalog 组合 `-race -count=10` exit `0`，未删除测试或放宽安全断言。
- 改动文件（本子切片）：`services/agent_execution_core.go`（catalog admission helper、workspace authority drain）、`services/agent_execution_workflow_skill.go`（Workflow/Skill mutation 复用 admission helper）、`services/agent_service.go`、`services/project_service.go`、`services/project_workspace_clear.go`（root transition 的两阶段 commit/rollback 与 catalog flush）、`services/project_service_agent_mcp_transaction_test.go`、`services/project_service_agent_rollback_test.go`、`services/agent_lifecycle_workspace_authority_test.go`。本子切片未修改 `.github/workflows`、`build/docker`、`frontend/package-lock.json` 或 Issue/PR/Release 治理元数据；未 commit/push/tag/release。
- 行为与安全：`AddProject`、`AddMultiRootProject`、active workspace clear/remove 在所有 root setter 完成后才发布动态 Agent catalog；refresh 失败或项目账本保存失败会逆序回滚，补偿失败使 Agent authority poison。`PersistenceNotPublished` 保留旧 lifecycle/runtime/owner，published-unknown/poison 撤销并清空 owner maps；catalog 消费者只能看到完整旧快照或完整新快照，混合 root 与 stale capability 继续 fail-closed。
- 验证（当前静止树）：
  - `go test ./services -run '^TestProjectServiceWorkspaceSwitchDrainsRefreshAdmissionBeforeSetters$' -race -count=10` exit `0`；workspace/lifecycle/catalog 组合 `-race -count=10` exit `0`（56.350s）；`go vet ./services ./internal/agentcore`、gofmt、切片 `git diff --check` exit `0`。
  - 固定 `%USERPROFILE%\go\bin\wails3.exe` `v3.0.0-alpha2.111` 后，`node scripts/backend-gate.mjs` **9/9 exit `0`**（全仓 `go test ./... -count=1` 355.2s）；`task frontend:check` **173/173 files、2765/2765 tests exit `0`**，ESLint 0 errors/1 既有 warning、vue-tsc、bindings 16/16、docs 全绿。未固定 Wails 的首次 `task frontend:check` 仍保留 proxy.golang.org 网络 exit `1`；根目录 `npm run frontend:check` 因无 `package.json` exit `1`，Taskfile 是仓库权威入口。
  - `node scripts/packaged-e2e.mjs` final fresh exit `0`：manifest `status=passed`/`phase=complete`，24/24 passed、0 failed、0 not-run，`artifactReused=false`、`sourceFingerprintStableAfterBuild=true`；artifact SHA-256 `f760f16d5514d7c9cbc24fd51c87793f32fbdeb9f36bd7bfd124eed6310635cf`，`build-inputs-v2` source fingerprint `68b492caf9ebf4c780d76a2691d4c63818977d87537b9d127dba364fc3623630`（1027 files），Wails `v3.0.0-alpha2.111`，`completedAt=2026-08-18T20:51:05.790Z`。Windows 本机真实 packaged 为 `P`，不升级为跨平台/CI `R`；cleanup 首次 `EBUSY` 经 bounded retry 收束且无残留 artifact 进程，source fingerprint 独立重算匹配。
  - `node scripts/npm-audit-gate.mjs` exit `1`：official registry URL 与 lock stability 通过，唯一高危为 `nanoid <3.3.18` / `GHSA-2v37-7h3g-55p8`（1 high）；`frontend/package-lock.json` SHA-256 仍为 `F1C0AED759A7A5DCCD0C58A2BB685B0C8CC94248D0ED6E36106DA00ADC6AC55F`，未修改锁文件。全局 `git diff --check` 的既有 `build/scripts/build-msi.ps1` EOF 空行仍保留，切片范围 clean。
- AC：AC1 `[ ] T/I/U`、AC2 `[ ] T/I/U`、AC3 `[ ] T/U`、AC4 `[ ] T/U`、AC5 `[ ] T/U`、AC6 `[ ] T/P/U`；本条只补充 workspace authority/catalog admission 的 `T` 子证据与 Windows packaged `P`，不勾选任何 AC。真实跨平台/CI operator/CLI、真正 resume、adapter-specific compensation、cross-window/caller/domain rollback、跨进程 CAS、stream privacy/retention、真实 provider、macOS/Linux packaged 与 npm high 修复仍 `U`/红；G33 保持 `0/6`。
- SSOT 回写：本条同步 prompt-9 §8 进度覆盖与 prompt-11 §9 会话交付；下一步仍只推进 G33 的下一未完成 AC，不把本机 packaged、contract 或 focused race 升级为 Goal 完成。

## 13.35 G33 legacy MCP renderer execution surface closure and fresh evidence (2026-08-19, Goal incomplete)

- 缺口状态：H1 原始全平台范围**仍存在**（Linux 核心 FileService 读写/变更 `T`、Windows NTFS junction 交换 `I`；macOS、RevealInOS、持续 CAS/换名竞争与公共 Workflow pathname loader `U`）；H2 在真实 Windows `cmd.exe` `Command`/`CommandContext` 注入矩阵范围内**已不存在**（`I`）。G33 **已变化但仍未闭环**，AC `0/6`；G28~G32 与 P12-BUG-01 仍未开始。
- 改动范围：`services/agent_service.go` 的 legacy `CallMCPTool` deny-only shim 增加 `//wails:ignore`；`bindings_runtime_surface_test.go`、`scripts/lib/wails-bindings.mjs`、`scripts/wails-bindings.test.mjs` 增加 Agent legacy MCP forbidden/ignored surface 合约；使用锁定 Wails `v3.0.0-alpha2.111` 重新生成 manifest/generated bindings。MCPService 的 `CallTool`/`RequestToolApproval`/`ExecuteApprovedTool`/`Close` 仍为 ignored，MCP CRUD/discovery（含 `WorkspaceRoot`）仍是明确 renderer API；本条不宣称整个 MCP surface 消失。未修改 `.github/workflows`、`build/docker`、`frontend/package-lock.json` 或 Issue/PR/Release 治理元数据。
- 行为与安全：renderer 不能调用旧 Agent MCP 执行入口或旧 MCP approval/execute shim；统一 Agent capability/catalog 管线仍是生产执行路径。deny-only 入口在 Go 包级兼容调用中返回固定 `ErrInvalidInput`，不触达 client/handler。
- 首次失败与修复：编辑后 backend gate 首轮发现 `bindings_runtime_surface_test.go` 未 gofmt；格式化后复跑。无 `WAILS3_BIN` 的 packaged 首轮因 prebuild 尝试联网安装锁定 Wails、`proxy.golang.org` 不可达而 exit `1`，失败 manifest 保留；显式本机 `%USERPROFILE%\go\bin\wails3.exe` 后重跑通过。根目录 `npm run frontend:check`/`npm.cmd run frontend:check` 因无根 `package.json` exit `1`，仓库权威入口为 `task frontend:check`。
- 验证：legacy deny-only `go test ./services -run '^TestMCPService_LegacyRendererApprovalShimsAreDenyOnly$' -race -count=10` exit `0`；MCP/Agent/Task workflow 组合 race 组 exit `0`；`go test . -run '^TestRegisteredWailsRuntimeSurfaceMatchesManifest$'` exit `0`；`node --test scripts/wails-bindings.test.mjs` 16/16、`node scripts/check-bindings.mjs` exit `0`；固定 Wails 后 `node scripts/backend-gate.mjs` 9/9 exit `0`（全仓 Go test 257.0s）；`task frontend:check` 174/174 files、2791/2791 tests exit `0`，ESLint 0 errors/1 existing warning、vue-tsc/bindings/docs 全绿；packaged driver `node --test scripts/packaged-e2e-driver.test.mjs` 14/14 exit `0`。
- 当前权威 packaged：fresh `node scripts/packaged-e2e.mjs` exit `0`，24/24 passed、0 failed、0 not-run，`artifactReused=false`、`sourceFingerprintStableAfterBuild=true`；artifact SHA-256 `4d27d2b9b76b2fad07c547bc209f8198e4a0ac59bb5bab2f53320df7e7a5729c`，`build-inputs-v2` source fingerprint `4063d6d1ee7a36cf488469edafbfbb492d1d9c4218dc6ce8556aea5abea21cf3`（1034 files），`completedAt=2026-08-19T09:43:26.420Z`；独立重算与 manifest 一致。Windows 本机 `P`，不升级为跨平台/CI `R`。
- 供应链/未验证：`node scripts/npm-audit-gate.mjs` 仍 exit `1`，唯一 high 为 `nanoid <3.3.18` / `GHSA-2v37-7h3g-55p8`；lock SHA 未改。scoped `git diff --check` clean；全局 `diff --check` 仍仅受既有 `build/scripts/build-msi.ps1` EOF 空行阻塞。真实 provider、完整 MCP 协议/跨平台 process、Git mutation、recovery/manual-disposition、cross-caller/domain rollback、跨进程 CAS、CI/CLI consumer 与 macOS/Linux packaged 仍 `U`。AC1~AC6 全部保持 `[ ]`，G33 仍 `0/6`，无 commit/push/tag/release。
- SSOT 回写：本条同步 prompt-9 §8 overlay 与 prompt-11 §9 会话交付；下一步仍只推进 G33 的下一未完成边界。

## 13.36 G33 MCP workspace admission, legacy surface and current static-tree gates (2026-08-19, Goal incomplete)

- 缺口状态：H1 原始全平台范围**仍存在**（Linux FileService 核心读写/变更 `T`、Windows NTFS junction 交换 `I`；macOS、RevealInOS、持续 CAS/换名竞争与公共 Workflow pathname loader `U`）；H2 真实 Windows `cmd.exe` `Command`/`CommandContext` 注入矩阵范围内**已不存在**（`I`）。G33 **已变化但仍未闭环**，AC `0/6`；G28~G32 与 P12-BUG-01 仍未开始/存在。最新 AI provider identity、operation permission、fallback approval、provider-output budget、frontend config race、stream lifecycle 与 usage 计量审查项均仅为新增 `U` 缺口，本轮未修改 AI 文件。
- 改动范围：`services/mcp_service.go`/`services/mcp_service_test.go`/`services/g03_workspace_fail_closed_test.go` 让 renderer-reachable MCP 在缺少 shared workspace context 且无 committed root 时于 stdio 启动前 fail-closed，canonical executable/workdir 与 workspace generation 在连接前后复核，legacy MCP approval/execute 与 Agent `CallMCPTool` 保持 deny-only；`frontend/src/stores/mcp.ts`/`mcp.test.ts` 在 connect/disconnect/delete/disable/保存 teardown 部分失败后重读配置与 Agent catalog，清除后端快照已证明失效的 stale connected 状态；`bindings_runtime_surface_test.go`、`scripts/lib/wails-bindings.mjs`、`scripts/wails-bindings.test.mjs` 与锁定 Wails 生成物/manifest 保持 ignored/forbidden renderer surface 合约。未修改 `.github/workflows`、`build/docker`、`frontend/package-lock.json` 或 Issue/PR/Release 治理元数据。
- 首次失败与修复：backend gate 首轮 `go test ./... -count=1` exit `1`，四个历史 MCP fixture 未提供 workspace root，分别在 unknown-tool、stdio lifecycle、connect/delete race 和 surviving-config connect 断言前收到 `ErrNotAllowed`；补充显式 root（真实 stdio fixture 使用测试二进制所在目录，仍受 root containment 约束）后定向 `-race -count=5` exit `0`，未删除测试或放宽安全断言。
- 定向验证：`go test -race ./services -run '^(TestMCPService_|TestG03MCP)' -count=3` exit `0`；修复 fixture 组 `-race -count=5` exit `0`；MCP store Vitest `6/6` exit `0`；`go test . -run '^TestRegisteredWailsRuntimeSurfaceMatchesManifest$'`、`node --test scripts/wails-bindings.test.mjs` `16/16`、固定 Wails 后 `node scripts/check-bindings.mjs` 与 scoped `git diff --check` 均 exit `0`。
- 全量门禁：第一次固定 Wails backend gate 的 gofmt/vet/build/contract/bindings/pin/docs 通过但全仓 Go 测试因上述四个 fixture exit `1`（368.592s）；修复后第二次 `node scripts/backend-gate.mjs` **9/9 exit `0`**（`go test ./... -count=1` 252.6s）。根目录 `npm.cmd run frontend:check` 因没有 `package.json` exit `1`（ENOENT）；仓库权威 `task frontend:check` exit `0`，Vitest `174/174` files、`2792/2792` tests，ESLint 0 errors/1 existing warning、vue-tsc、bindings `16/16`、docs 全绿；独立 `check-doc-links`/`check-doc-numbers` exit `0`。`node scripts/npm-audit-gate.mjs` exit `1`，official registry/lock stability 通过但唯一 high 为 `nanoid <3.3.18` / `GHSA-2v37-7h3g-55p8`；lock SHA-256 仍 `F1C0AED759A7A5DCCD0C58A2BB685B0C8CC94248D0ED6E36106DA00ADC6AC55F`，未修改锁文件。全局 `git diff --check` 仍仅受既有 `build/scripts/build-msi.ps1:124` EOF 空行阻塞，切片 scoped clean。
- packaged/证据：在上述修复后的静止源码上，固定 `%USERPROFILE%\go\bin\wails3.exe` `v3.0.0-alpha2.111` 运行 `node scripts/packaged-e2e.mjs` exit `0`；manifest `status=passed`/`phase=complete`，24/24 passed、0 failed、0 not-run，`artifactReused=false`、`sourceFingerprintStableAfterBuild=true`；artifact SHA-256 `684190ffed9c273f400f4e50cb9f066f6ac6b52bf6e94768d55075a6e28a5705`，`build-inputs-v2` source fingerprint `a97bb30e9a8da96b1660d6efd0a178a9e7862df97ec915b7d20c6725a21c61b4`（1034 files），`recordedAt=2026-08-19T11:11:01.706Z`、`completedAt=2026-08-19T11:14:57.408Z`。独立 `sourceFingerprint()` 重算与 manifest 一致；这是 Windows 本机 `P`，不升级为跨平台/CI `R`。首次 cleanup 记录一次 WebView `EBUSY`，脚本 bounded retry 后完成；更早启动的同路径 IDE 进程未被本轮终止，不能将其算作本次 packaged 泄漏。
- AC 与未验证：AC1 `[ ] T/I/U`、AC2 `[ ] T/I/U`、AC3 `[ ] T/U`、AC4 `[ ] T/U`、AC5 `[ ] T/U`、AC6 `[ ] T/P/U`；本条只取得 MCP workspace admission/legacy renderer denial/partial-failure reconciliation 的 `T` 子证据与 Windows packaged `P`，不勾选任何 AC。真实完整 MCP 协议（multi-content、pagination、notifications、HTTP session）、真实 provider/stream、Git mutation、operator/CLI cross-platform、真正 resume/compensation、cross-caller/domain rollback、跨进程 CAS、stream privacy/retention、macOS/Linux packaged 与 CI 仍 `U`。
- SSOT 回写：本条同步 prompt-9 §8 与 prompt-11 §9；下一步仍只推进 G33 的下一未完成边界。无 commit/push/tag/release。

## 13.37 G33 ordinary AI operation admission for chat and inline completion (2026-08-19, Goal incomplete)

- 缺口状态：H1 原始全平台范围**仍存在**（Linux FileService 核心读写/变更 `T`、Windows junction 交换 `I`；macOS、RevealInOS、持续 CAS/换名竞争与公共 Workflow pathname loader `U`）；H2 真实 Windows `cmd.exe` `Command`/`CommandContext` 注入矩阵范围内**已不存在**（`I`）。G33 **已变化但仍未闭环**，AC `0/6`；G28~G32 与 P12-BUG-01 仍未开始/存在。
- 改动文件：`services/ai_service.go`、`services/ai_agent.go`、新增 `services/ai_permission_boundary_test.go`。新增单一 `aiSnapshot` operation admission：`Send` 先按 `AIOpChat`、`Complete` 先按 `AIOpInlineCompletion` 检查 backend-owned assignment；禁用操作在 lifecycle/usage 单元创建和网络请求前返回 `ErrNotAllowed`；assigned provider 仅由 SettingsService 按 `ConfigID` hydration endpoint/protocol/key/model，renderer/global `BaseURL` 不再成为该操作的执行选择。未分配操作保留既有全局配置兼容路径。未修改 `.github/workflows`、`build/docker`、`frontend/package-lock.json` 或 Issue/PR/Release 治理元数据。
- 首次失败与修复：新增红测先证明旧旁路：disabled chat/inline 分别触达 provider，assigned 场景错误命中 global provider（含重试）；修复为 resolver/hydration 在 lifecycle 与 HTTP 前执行后，disabled provider hit 为 `0`，assigned chat/inline 均只命中持久化 provider，认证 key 与 model 精确匹配，未分配 global fallback 回归通过。未删除测试或放宽断言。
- 定向验证：`go test ./services -run '^TestAIServiceOperationPermission' -count=1 -v` exit `0`；`go test -race ./services -run '^(TestAIServiceOperationPermission|TestResolveAgentOperation|TestResolveModelFor)' -count=10 -timeout=20m` exit `0`；`go test -race ./services -run '^(TestAIService|TestResolve)' -count=1` exit `0`；`go vet ./services`、gofmt、切片 `git diff --check` 均 exit `0`。
- 全量验证：首次 `node scripts/backend-gate.mjs` 的 Go/test/contract/docs 均通过，绑定步骤仅因联网安装锁定 Wails `v3.0.0-alpha2.111` 时 `proxy.golang.org` 连接失败 exit `1`；使用本机锁定 `%USERPROFILE%\go\bin\wails3.exe` 重跑后 backend gate `9/9` exit `0`（`go test ./... -count=1` 241.5s，gofmt/vet/build/contract/bindings/Wails pin/doc links/doc numbers 全通过）。权威 `task frontend:check` exit `0`：Vitest `174/174` files、`2792/2792` tests，ESLint 0 errors/1 existing warning，vue-tsc、bindings `16/16`、docs 全绿。`node scripts/npm-audit-gate.mjs` exit `1`，official registry/lock stability 通过但 `nanoid <3.3.18` / `GHSA-2v37-7h3g-55p8` 唯一 1 high；lockfile 未改。
- AC/未验证：AC1 `[ ] T/I/U`、AC2 `[ ] T/I/U`、AC3 `[ ] T/U`、AC4 `[ ] T/U`、AC5 `[ ] T/U`、AC6 `[ ] T/P/U`；本轮只取得普通 chat/inline operation admission 的 `T` 子证据，不勾选任何 AC。`GenerateTitleWithAI`、`StartStream`/`StartAgentStream`、fallback approval identity、provider output/body budgets、stream target/cancellation、usage undercount、frontend `setConfig` race、真实 provider/CI/CLI 与完整 MCP 协议仍 `U`。
- packaged/供应链：固定 `%USERPROFILE%\go\bin\wails3.exe` `v3.0.0-alpha2.111` 运行 `node scripts/packaged-e2e.mjs` exit `0`；manifest 24/24 passed、0 failed、0 not-run，`artifactReused=false`、`sourceFingerprintStableAfterBuild=true`；artifact `7d1d9cf475e3a6101bda3a02c3748abf13d6258ebbd2ef0d21c0a1bb70a7ccf7`，`build-inputs-v2` source `c37d6e0280749bb4221dd2933e563854e69b67474537e2ca682e45b044f54359`（1035 files），`recordedAt=2026-08-19T14:09:04.000Z`、`completed=2026-08-19T14:10:56.000Z`。独立 `sourceFingerprint()` 重算一致；首次 WebView cleanup `EBUSY` 经 bounded retry 收束。这是 Windows 本机 `P`，不升级为跨平台/CI `R`。npm gate 的 `nanoid` high 保持红灯，未修改 lockfile。全局 `git diff --check` 的既有 `build/scripts/build-msi.ps1` EOF 空行仍不属于本轮；切片 scoped check clean。无 commit/push/tag/release。
- SSOT 回写：本条同步 prompt-9 §8 与 prompt-11 §9；下一步仍只推进 G33 的下一未完成边界。

## 13.38 G33 ordinary AI title/stream admission and current authority gates (2026-08-19, Goal incomplete)

- 缺口状态：H1 原始全平台范围**仍存在**（Linux FileService 核心读写/变更 `T`、Windows NTFS junction 交换 `I`；macOS、RevealInOS、持续 CAS/换名竞争与公共 Workflow pathname loader `U`）；H2 真实 Windows `cmd.exe` `Command`/`CommandContext` 注入矩阵范围内**已不存在**（`I`）。G33 **已变化但仍未闭环**，AC `0/6`；G28~G32 与 P12-BUG-01 仍未开始/存在。
- 改动文件：`services/ai_service.go`、`services/ai_agent.go`、`services/ai_permission_boundary_test.go`。在 `GenerateTitleWithAI`、`StartStream`、`StartAgentStream` 的 provider/lifecycle/网络副作用前复用 backend-owned `admitProviderOperation`；Disabled 返回 `ErrNotAllowed`，assigned provider 按 SettingsService `ConfigID` hydration endpoint/protocol/key/model，未分配操作保留 global compatibility。`StartAgentStream` 当前按 `AIOpChat` admission/usage，是否应改为独立 `AIOpAgent` assignment 仍需产品语义决策；未修改 `.github/workflows`、`build/docker`、`frontend/package-lock.json` 或 Issue/PR/Release 治理元数据。
- 首次失败与修复：title/stream 新增 fixture 首跑暴露 disabled/assigned provider 旁路及 Agent stream fixture 未注入 permission service；补齐 admission 与 fixture wiring 后，disabled provider hit=0、assigned endpoint/auth/model 精确匹配、unassigned global fallback 通过。未删除测试或放宽安全断言。
- 定向验证：`gofmt -w services/ai_service.go services/ai_agent.go services/ai_permission_boundary_test.go`；`go test ./services -run '^TestAIServiceOperationPermission.*(StartStream|StartAgentStream)' -count=1 -v` exit `0`；`go test -race ./services -run '^(TestAIServiceOperationPermission|TestResolveAgentOperation|TestResolveModelFor)' -count=10 -timeout=20m` exit `0`；`go test -race ./services -run '^(TestAIService|TestResolve)' -count=1 -timeout=20m` exit `0`；`go vet ./services`、gofmt 与 scoped `git diff --check` exit `0`。
- 全量门禁：未设置 `WAILS3_BIN` 的 `task frontend:check` 与独立 `check-bindings` 首次因尝试联网安装锁定 Wails `v3.0.0-alpha2.111`、`proxy.golang.org` 连接失败 exit `1`；固定 `WAILS3_BIN=%USERPROFILE%\go\bin\wails3.exe` 后 `node scripts/backend-gate.mjs` 9/9 exit `0`（`go test ./... -count=1` 354.6s），`task frontend:check` exit `0`（174/174 files、2792/2792 tests，ESLint 0 errors/1 existing warning、vue-tsc、bindings、docs 全绿），`node scripts/check-bindings.mjs`、`check-doc-links.mjs`、`check-doc-numbers.mjs` 均 exit `0`。`node scripts/npm-audit-gate.mjs` exit `1`：official URL/lock stability 通过，唯一 high 为 `nanoid <3.3.18` / `GHSA-2v37-7h3g-55p8`；lock SHA 未修改。
- packaged/证据：固定 Wails 后 fresh `node scripts/packaged-e2e.mjs` exit `0`，manifest `status=passed`/`phase=complete`，24/24 passed、0 failed、0 not-run，`artifactReused=false`、`sourceFingerprintStableAfterBuild=true`；artifact SHA-256 `fab0e53fc8dea7efa969e88047ebe74af7e3ff29498cefbebf98bef6a1c36e13`，`build-inputs-v2` source fingerprint `90298aad599eb87b0fadd9479bfa729119d835717a1a2316a27695ff549ac83a`（1035 files），`completedAt=2026-08-19T15:02:23.562Z`，独立 `sourceFingerprint()` 与 artifact hash 重算一致；driver contract 14/14 exit `0`。Windows 本机真实 packaged 为 `P`，不升级为跨平台/CI `R`；一次 cleanup `EBUSY` 记录保留，未终止共享树中无法归属本次的既有进程。
- AC/未验证：AC1 `[ ] T/I/U`、AC2 `[ ] T/I/U`、AC3 `[ ] T/U`、AC4 `[ ] T/U`、AC5 `[ ] T/U`、AC6 `[ ] T/P/U`；本轮只取得 title/chat/Agent stream operation admission 的 `T` 子证据与 Windows packaged `P`，不勾选任何 AC。`agentWindowForContext` 缺失时仍可能启动 provider/lifecycle 并丢失目标窗口事件；caller cancellation/worker retention、fallback approval identity、provider output/body budgets、frontend `setConfig` race、usage undercount、真实 provider/CI/CLI、完整 MCP 协议与跨平台 packaged 仍 `U`。无 commit/push/tag/release。
- SSOT 回写：同步 prompt-9 §8 与 prompt-11 §9；下一步只推进 G33 当前未完成的 stream target/lifecycle 边界。

## 13.39 G33 renderer stream visibility and Agent tool-turn usability (2026-08-20, Goal incomplete)

- 缺口状态：H1 原始全平台范围**仍存在**（Linux FileService 核心读写/变更 `T`、Windows NTFS junction 交换 `I`；macOS、RevealInOS、持续 CAS/换名竞争与公共 Workflow pathname loader `U`）；H2 在真实 Windows `cmd.exe` `Command`/`CommandContext` 注入矩阵范围内**已不存在**（`I`）。G33 **已变化但仍未闭环**，AC `0/6`；P12-BUG-01 仍存在。AI 对话“首个 chunk 需退出重进才刷新”已不存在；Agent 工具轮次静默丢 observation 与独立 Agent 页面无批准入口已变化并在本切片闭合为前端 `T` 子证据。
- 改动范围：`frontend/src/stores/ai.ts` 将插入 reactive message 数组后读回的 Vue proxy 作为流式 assistant target，加入 generation-scoped/有界的 pre-admission event buffer、stream owner 过滤、配置 `setConfig` await、超时后 backend cancel 与显式 provider reasoning summary 路由；`frontend/src/stores/agent.ts` 对 native/fence tool calls、auto/manual approval、执行与 observation 回传建立串行 turn barrier，并在会话/工作区/模式切换时使旧 generation 失效；新增 `agentTimeline.ts` 与 `AgentExecutionTimeline.vue`，只展示 provider 明确发送的摘要和工具阶段，绝不推断或展示隐藏 chain-of-thought。`AgentToolCalls.vue` 将批准/拒绝、风险、参数/结果与清理队列带到使用 `MessageList` 的独立页面/AI 窗口，流式期间禁用执行按钮。相关 store/UI 测试覆盖首 chunk 在已挂载 DOM 中原位更新、pre-return event admission、native tool `ai:done` barrier、stale generation 丢弃、时间线与 standalone approval UI。未修改 `.github/workflows`、`build/docker`、`frontend/package.json`/`package-lock.json` 或治理元数据。
- 首次失败与修复：首个定向 npm 命令错误使用不存在的 `vitest` script，exit `1`；改用仓库 `npm test` 后 pretest 又因未继承锁定 Wails CLI、访问 `proxy.golang.org` 失败，exit `1`；固定 `WAILS3_BIN=%USERPROFILE%\go\bin\wails3.exe` 后通过。新增 `AgentToolCalls` 测试首跑因 mock computed 读取 raw state 导致 chat-mode 隐藏断言失败，改为从同一 reactive state 计算后 10/10 通过；另一次命令误写本机路径为 `%USERPROFILE%` 的缺下划线变体，立即 fail-closed 为 executable not found，使用精确路径重跑。未删除测试或放宽安全断言。
- 验证：最终 focused AI/Agent/timeline/UI Vitest `5 files / 191 tests` exit `0`；新增 standalone approval 与 MessageList `2 files / 10 tests` exit `0`；定向 ESLint、`vue-tsc --noEmit`、bindings、doc-links/doc-numbers 与 scoped `git diff --check` 均 exit `0`。固定 `WAILS3_BIN=%USERPROFILE%\go\bin\wails3.exe` 后，当前静止树权威 `node scripts/backend-gate.mjs` **9/9 exit `0`**（gofmt/vet/build、`go test ./... -count=1` 411.7s、contract smoke、bindings、Wails pin、docs 全部通过）；`task frontend:check` exit `0`：`177/177` files、`2813/2813` tests，ESLint、vue-tsc、bindings contract `16/16`、bindings drift 与 docs 全绿。
- packaged/供应链：最终 fresh `node scripts/packaged-e2e.mjs` exit `0`，manifest `status=passed`/`phase=complete`、24/24 passed、0 failed/0 not-run、`artifactReused=false`、`sourceFingerprintStableAfterBuild=true`；artifact SHA-256 `83cab12cdb949aa731889dd3e80fa8aebaf36dc5c75ab2cec15fcb51fff63151`，`build-inputs-v2` source fingerprint `7084b637f68f6a3b9950f1e965f962128c5fba9991e59b5b53b1e62822fda3d2`（1042 files），Wails `v3.0.0-alpha2.111`，`completedAt=2026-08-19T18:05:19.742Z`。这是 Windows 本机 `P`，不升级为真实 provider、跨平台或 CI `R`；首次 WebView cleanup `EBUSY` 由既有 bounded retry 收束。`node scripts/npm-audit-gate.mjs` 仍 exit `1`：official URL/lock stability 通过，唯一 high 为 `nanoid <3.3.18` / `GHSA-2v37-7h3g-55p8`，lockfile 未改。
- AC/未验证：AC1 `[ ] T/I/U`、AC2 `[ ] T/I/U`、AC3 `[ ] T/U`、AC4 `[ ] T/U`、AC5 `[ ] T/U`、AC6 `[ ] T/P/U`；本条只取得 renderer stream/tool-turn/standalone approval 的 `T` 与 Windows packaged `P` 子证据，不勾选任何 AC。workflow AI fallback approval 仍只绑定 primary fingerprint；真实 provider 端到端、provider output/body/tool budget、stream usage、完整 MCP 协议、recovery/domain rollback、cross-caller/跨进程 CAS、macOS/Linux packaged 与 CI/CLI 仍 `U`。G33 保持 `0/6`；无 commit/push/tag/release。

## 13.40 G33 conversation handoff / packaged Agent round and default persistence root (2026-08-20, Goal incomplete)

- 缺口状态：H1 原始全平台范围**仍存在**（Linux FileService 核心 `T`、Windows junction `I`；macOS、RevealInOS、持续 CAS/换名竞争与公共 Workflow pathname loader `U`）；H2 在真实 Windows `cmd.exe` `Command`/`CommandContext` 注入矩阵范围内**已不存在**（`I`）。G33 与 P12-BUG-02 **已变化但仍未闭环**，AC `0/6`；P12-BUG-01、npm `nanoid` high 仍存在。
- 改动与行为：`aiAssistant.ts`、`AiWindowView.vue`、`crossWindowSync.ts` 及 ActivityBar/MainLayout 入口把 conversation/mode 作为 durable generation-scoped target 交接，加载成功才 ACK，失败目标保留重试；旧 persist/save 不能抢占新 target。packaged Agent probe 通过真实 renderer store、HTTP/SSE loopback provider、native tool call、自动批准、`AgentService` capability、`FileService.ReadFile`、terminal usage、observation 与第二 provider request，并在 driver/server 双侧 fail-closed 校验非空 UnitID、session/operation/success/pending、批准顺序与 observation 一致性。`ConversationService` 的默认根此前在首个 `Save/Load` 前为空，可能把会话写入/读自进程 cwd；新增 `resolvedStorageDir` 并在构造、path、list、purge 统一使用，零值也不再退化到 cwd。
- 失败测试与首次失败：`TestConversationService_DefaultStorageResolvedBeforeFirstPath` 在旧实现 constructor/zero-value 两项均得到 `first-conversation.json` 并 exit `1`；补充 cwd 伪造记录不可读、默认 Save 不在 cwd 写文件的回归后，相关 `-race` 通过。第一轮 fresh packaged 的 24 fixtures 虽均执行成功，但 probe 首次会话在仓库根生成 `20260820-174707-7561115c1ac2f5cf.json`，最终 source-final-verification 因 file set 改变而将 manifest 标为 failed；产物保留到 evidence 目录，未用 fingerprint exclusion 掩盖。修复后第一次重跑又因 bindings 脚本联网访问 `proxy.golang.org` 失败于 frontend-build；显式 `WAILS3_BIN` 指向锁定本机 CLI 后重跑通过。
- 定向与全量验证：conversation default/save/load/list/update 组 `go test -race` exit `0`；Agent probe Vitest `12/12`、packaged driver `25/25`、`go test -race -tags=e2e ./internal/e2e` 均 exit `0`。前端 Agent/stream/handoff 静止源码已有 `8 files/252 tests`、handoff `3 files/91 tests` 与权威 `frontend:check` `178/178 files`、`2869/2869 tests`、ESLint/vue-tsc/bindings/docs exit `0`。最终 conversation 修复后，固定 `WAILS3_BIN=%USERPROFILE%\go\bin\wails3.exe` 的 `node scripts/backend-gate.mjs` 9/9 exit `0`，全仓 `go test ./... -count=1` 用时 418.2s。
- packaged/证据：最终 fresh Windows `node scripts/packaged-e2e.mjs` exit `0`，manifest `status=passed`/`phase=complete`、24/24 passed、0 failed/0 not-run、`artifactReused=false`、`sourceFingerprintStableAfterBuild=true`；artifact SHA-256 `5bb669311cf5dfe8848bc25e49a3b49512ae99bb3d1c05cf6227a4f216086fbc`，`build-inputs-v2` source `864bcd97f016e1a7aaad7ea2e17a1a6e044512cf29dde3c747baf86d23c30143`（1045 files），`completedAt=2026-08-20T09:58:45.734Z`。Agent 字段记录两次 provider 请求、非空 `usageUnitId=2da008b12486ed3887b29d4d851c20c2`、同 session、`operation=read`、`success=true`、`pending=false`、approval 先于执行且 backend/renderer observation 一致；根目录无新会话 JSON，packaged 进程已回收。该证据仅为 Windows packaged + 受控 loopback provider `P`，不升级为真实外部 provider、重启 durable ledger、跨平台或 CI `R`。
- AC/阻塞：AC1 `[ ] T/I/U`、AC2 `[ ] T/I/U`、AC3 `[ ] T/U`、AC4 `[ ] T/U`、AC5 `[ ] T/U`、AC6 `[ ] T/P/U`。真实 provider、manual/mutating tools、实际双 WebView handoff、restart ledger、fallback freeze、provider output/usage budgets、完整 MCP、跨平台/CI/CLI 与 H1 剩余平台仍 `U`；npm gate 仍因 `nanoid <3.3.18` / `GHSA-2v37-7h3g-55p8` 1 high 为红，lock SHA `F1C0AED759A7A5DCCD0C58A2BB685B0C8CC94248D0ED6E36106DA00ADC6AC55F` 未变。G33/P12-BUG-02 均不得宣称完成；无 commit/push/tag/release。
