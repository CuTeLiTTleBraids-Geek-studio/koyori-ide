# P19 Goal Prompt：P16 收口审查整改与工程化收尾

> 本文件是独立 Goal，不继承 `prompt-16.md` 或 `prompt-a.md` 的任务状态、完成声明和验收结论。它记录 2026-08-29 对当前工作区（分支 `release/v0.2.0`，HEAD `18b43cf`）的一轮只读审查结果与整改目标。本轮审查方法：代码级逐条核查 + 自动化测试重跑 + 静态扫描；**未使用真实 UI 自动化**，因此所有"真实 UI 运行"类证据仍以 `prompt-16.md` 已记录的 U 项为准。执行本 Goal 前必须读取 `docs/prompts/prompt-a.md` 并遵守其证据纪律。

## 1. 唯一目标

把本轮审查发现的缺陷收敛到可验证的完成态：仓库版本控制收口（当前全部 P16 工作未入库）、IM Webhook 安全与全库 C-1 姿态对齐、前端单一包管理器、三处前端 stale 竞态修复、后端沙箱豁免点与死代码清理、prompt 文档个人路径脱敏、依赖与脚本卫生。整改不得破坏 prompt-16 已验证的 P0/P1 行为（见第 2 节基线），不得把 prompt-16 遗留的 U 项改写为完成。

## 2. 审查基线（2026-08-29 已验证事实，整改不得使其回退）

### 2.1 本轮验证命令与结果（全部通过，`T`）

- `cd frontend && npx vitest run` → 339 tests passed。
- `cd frontend && npx vue-tsc --noEmit` → 通过。
- `go test ./services -run '^TestAIServiceNativeToolStreamingRoundTripHTTP$|^TestAIProviderStreamBoundary|TestMCP|TestG03MCP' -count=1 -p 1` → ok（23.5s）。
- `go test ./services -run 'TestGit' -count=1 -p 1` → ok（44.9s）。
- `go vet ./services/... . ./internal/...` → 0 错误。
- `go build ./...` → 0 错误。
- `node scripts/check-bindings.mjs` → OK（pinned v3.0.0-alpha2.111，manifest 与生成树一致，ByName=0）。
- `go test . -run TestRegisteredWailsRuntimeSurfaceMatchesManifest -count=1` → ok（6.6s）。

### 2.2 prompt-16.md 声称核查结论

对 prompt-16 第 8 节声称的实现逐条做了 file:line 级核查，**8/8 属实**（P0-01 发送接纳语义、P0-02 native 主协议、P0-04 Plan 隐藏、P1-01 三档权限、P1-02 reasoning effort、P1-03 MCP A~H、P1-04 Git 投影、bindings manifest 登记）。未发现任何"声称完成但代码不存在"的情况。整改时**不得改动**这些已验证行为：`ai.ts:1032` 接纳返回 true、`agent.ts` native/fence 区分与 native-event-seen 防 fence 降级、`InputComposer/AssistantHeader` 仅 chat/goal/agent、`AgentSection.vue` 恰好三档权限、`ai_service.go` reasoning fail-closed、`mcp_capability.go/mcp_client.go/mcp_service.go` 的 capability 快照与 roots 受控响应、`git_repository.go` detectStagedRenames、`git_diff.go` GetDiffForSide、`GitPanel.vue` renamed/续读投影。

### 2.3 prompt-16 仍未闭合的 U 项（本 Goal 不得冒充完成）

1. **P1-03 整体不得标 complete**：唯一剩余 U 是"已批准连接后的真实 UI 正路径 smoke"，2026-08-29 已由用户指示转交本人手动验证（fixture 日志保留了一次真实协议闭环记录）。
2. P1-04 的真实桌面 Git 面板投影 smoke（U）。
3. AC-08 中 editor/terminal/search 的真实运行操作验证从未系统性执行（prompt-16 自己承认）。
4. 外部真实 provider 访问、packaged/release、跨平台（prompt-16 第 5 节声明为范围外）。
5. P2（默认曝光收口）未开始——prompt-16 设计如此，不构成本轮缺陷。

### 2.4 审查代理误报复核记录（防止下一任务误信）

- "仓库提交了约 140MB 构建产物（gugacode/koyori-ide 等 exe）"——**不属实**。已跟踪文件最大为 `icon.png`（2.3MB）；`*.exe`、`bin/` 等均被 `.gitignore` 正确覆盖，二进制仅存在于本地未跟踪工作目录。
- 前端审查澄清：项目**未使用 Pinia**（无依赖、无 import），所有 store 是模块级 `reactive()` 单例 + 导出函数模式；后续计划不得基于 Pinia 假设。
- 前端无 `window.go` 直调；`@wailsio/runtime` 仅用于 `Events` 且集中在 `lib/crossWindowSync.ts`（统一 canceller）。

## 3. 当前缺陷与整改要求

### 3.1 P0：立即执行

#### P0-01：仓库版本控制收口（最高优先）

证据：`git log` 仅 6 个提交；`git status` 显示 **391 个文件改动（+32663/−10406）未提交**；`docs/prompts/prompt-12~16.md`、`prompt-a.md`、`frontend/src/components/ai-assistant/AgentToolCalls.vue(.test.ts)`、`AgentExecutionTimeline.vue(.test.ts)`、`frontend/src/components/settings/ai/McpSection.test.ts`、`frontend/src/e2e/agentToolRoundProbe*`、`conversationHandoffProbe*`、`extensionHostG24Recovery*`、`frontend/src/lib/extensionDecorations*`、`extensionHostUiBridge*`、`extensionIntegrityCopy*`、`planHonesty.test.ts`、`stores/agentTimeline.test.ts`、`assets/styles/main.test.ts`、`settings/AppearanceSection.test.ts`、`build/docker/SERVER.md`、`build/docker/server-gateway/` 等大量**源文件与新测试处于 untracked**。

要求：按可审查的批次提交（建议分组：后端 services/internal、前端 stores+组件、e2e probes、docs/prompts（先做 P1-05 脱敏）、build/docker、lockfile 迁移单独一批），提交信息沿用仓库 conventional 风格；提交后推送，确认 CI 在新 HEAD 上绿。禁止把不相关改动揉进单个巨型提交；禁止提交本地二进制、`frontend/pnpm-lock.yaml`（先执行 P0-03）或未脱敏的个人路径。

#### P0-02：IM Webhook SSRF 防护缺失 + wechat_work token 静默丢弃

证据：`services/im_service.go:113-116` `NewIMService` 使用裸 `&http.Client{Timeout: 30s}`（无 SSRF 安全传输层、无 no-redirect 策略）；`sendToProvider`（:377 起）向 renderer 可配置的 `WebhookURL` POST 并携带 `Authorization: Bearer/Bot <BotToken>`（:382-393）；`UpdateConfig`（:244-257）允许在一次性 IM 审批后任意更换 WebhookURL 而无需重新审批。对比：MCP（`mcp_transport.go:406-414`、`ai_urlsec.go:192-240`）、AI（`ai_urlsec.go:249-317`）、HTTPClientService（`http_client_service.go:389-425` 私网需单独原生审批）均已有完整防护，IM 是全库唯一遗漏点。另 `:388-392` wechat_work 分支注释自认"企微通过 query 参数 token，此处略；生产环境需补充"，token 完全不生效。

要求：
1. 复用 `ai_urlsec.go` 的 SSRF 安全传输与校验（`ValidateBaseURL`/`ValidateNonPrivateURL` + 私网/环回/链路本地/元数据地址拨号时拒绝 + 防 DNS rebinding + no-redirect），WebhookURL 变更时重新走原生同意边界（或至少强校验），与全库 C-1 姿态对齐。
2. wechat_work token 按 provider 规范经 query 参数真实发送；若决定不支持，必须在保存配置阶段显式 `ErrNotAllowed` fail-closed，不得静默丢弃。
3. `T`：新增 httptest fixture 测试覆盖——公网允许、私网/环回拒绝、重定向拒绝、各 provider token 放置位置正确、URL 变更触发重新审批；并回归既有 IM 测试。

#### P0-03：前端双 lockfile 不同步

证据：`frontend/package-lock.json`（npm，已跟踪）与 `frontend/pnpm-lock.yaml`（pnpm，untracked 残留）并存且解析出不同依赖树：pnpm-lock 解析 `vue-router@5.2.0`，npm lock 安装 `vue-router@5.1.0`；`pnpm-workspace.yaml` 已删除。同一 `package.json` 在两种包管理器下产生不同依赖树，构建不可复现。

要求：删除 `frontend/pnpm-lock.yaml`；确立 npm 为唯一包管理器（`package.json` 声明 `packageManager` 或 CI 守卫：存在 pnpm-lock.yaml 即失败）；`npm ci` + vitest + vue-tsc 全绿后把 `package-lock.json` 一并提交（并入 P0-01 批次）。

### 3.2 P1：本轮必须完成

#### P1-01：前端三处 stale 竞态修复

证据：
- `frontend/src/stores/git.ts:108-127` `refreshGit` 无 stale 响应保护：快速切换仓库时慢的旧请求返回后覆盖 `_allChanges`/`branchName`/ahead-behind，而 `_lastRepoPath` 已是新仓库；`loadConflicts`（:257）、`loadStashes`（:447）、`loadTags`（:511）、`loadSubmodules`（:614）同样依赖"后端最近仓库"语义，无 generation 守卫。
- `frontend/src/components/editor/DiffView.vue:25-35` `loadDiff` 无 stale 守卫，`filePath` 快速切换时慢响应覆盖 `diffContent`。
- `frontend/src/stores/mcp.ts:662-677` `refreshMcpServerContext` 无并发守卫，同一 server 连续两次刷新时旧 lifecycleGeneration 响应可覆盖新状态。

要求：参照 `ai.ts` 的 `streamGeneration` 模式补 generation/stale-token 守卫；`T`：为每处补"快速切换不回写旧状态"回归测试。

#### P1-02：pprof 输出路径沙箱豁免

证据：`services/pprof_service.go:52,61,116,138,214,279-283` `StartCPUProfile/StartTrace/writeRuntimeProfile` 接受 renderer 传入的任意绝对路径创建文件；现有缓解为 `os.OpenFile(..., O_CREATE|O_EXCL|O_WRONLY, 0600)`（:282，测试 `pprof_flame_test.go:224-236`），无覆盖/链接跟随，但可写任意位置，是全库唯一不走 `ValidatePathWithinRoot` 的文件写出点。

要求：输出路径约束到工作区根（或专用诊断目录）并复用 `ValidatePathWithinRoot`；保持 fail-closed 语义与既有测试通过，新增越界路径拒绝测试。

#### P1-03：Legacy 审批管线死代码删除

证据：`services/agent_service.go:753-755,757-868,1249-1281` 的导出包装一律返回错误，实际执行走 `requestInternalAgentToolCapability`/agentcore Runtime；但约 370 行 `*Legacy` 实现（含原生对话框、token 铸造/核销、CAS 基线，`services/agent_write_approval.go:103-280`）在生产代码零调用，仅测试引用。这是与安全主路径并行的第二套授权逻辑，存在误接线复活或漂移失修风险。

要求：先用搜索确认 caller 仅剩测试，然后删除 Legacy 实现与对应测试，agentcore Runtime 保持唯一权威；删除后 `go vet`/`go build`/surface 测试/受影响 services 测试全绿；`scripts/lib/wails-bindings.mjs` 的 forbidden/required 策略如受影响需同步并用官方命令重新生成 manifest。

#### P1-04：marketplace 下载 URL 复验

证据：`services/marketplace_service.go:785-789,802,856-867,1103-1106` `downloadUrl`/`sha256` 文件 URL 完全取自 registry 响应且不做 scheme/私网复验；`downloadVSIXToTempFile` 使用 `&http.Client{}`（默认跟随重定向、无 SSRF 传输层）。`SetRegistryURL` 有 `ValidateNonPrivateURL`（:414-430），但 registry 返回的下载地址绕过。威胁模型要求 registry 本身被攻破（注释已承认"does not authenticate the publisher"），故列 P1。

要求：下载前对 downloadUrl 复用 `ValidateBaseURL` 并改用 no-redirect client；完整性校验（SHA-256）保持不变；`T`：registry 返回私网 URL / 重定向到私网时的拒绝测试。

#### P1-05：prompt 文档个人路径脱敏

证据：`docs/prompts/` 下 8 个文件含开发者本机路径 `%USERPROFILE%\...`：prompt-7、8、10、11（已跟踪）与 12、13、14、16（untracked 待入库）。开源发布会暴露 Windows 用户名。已跟踪源码（非 docs/prompts）中个人路径为 0。

要求：把这 8 个文件中的绝对路径替换为 `%USERPROFILE%` 类占位符或通用描述（保留证据语义，不破坏文档可读性）；P0-01 提交 prompt-12~16/prompt-a 前必须完成；在 CI（或 `scripts/check-encoding.mjs` 同层）新增守卫：已跟踪文件中出现 `C:\Users\<具体用户名>` 模式即失败。

#### P1-06：前端依赖与脚本卫生

证据：`frontend/package.json:57` `jest@30.2.0` 为死依赖（全仓无 jest 配置与 import，测试全部 vitest）；`:49-50` `@types/dompurify@3.0.5`、`@types/marked@5.0.2` 已废弃（dompurify/marked 自带类型，可能引起类型冲突）；scripts 缺独立 `typecheck`（vue-tsc 只在 build 内执行）。

要求：删除三处死依赖；新增 `"typecheck": "vue-tsc --noEmit"` script；删除后 `npm ci` + vitest + vue-tsc 全绿。

#### P1-07：binding 分层收敛

证据：17 处直接 `import ../../bindings/...` 绕过 `src/api/*` 包装层：`components/editor/CodeEditor.vue:35`、`components/layout/DebugPanel.vue:70`、`src/main.ts:68`、`stores/lsp.ts:10`、`stores/debug.ts:14`、`stores/httpClient.ts:5`、`stores/aiGoal.ts:184`、`stores/aiPlan.ts:111`、`lib/gitWorktree.ts:5`、`lib/gitRebase.ts:4`、`lib/semanticTokens.ts:5`、`lib/inlayHints.ts:5`、`lib/codeLens.ts:5`、`lib/lspCompletion.ts:26`、`lib/extensionHost/extensionHost.ts:85`。部分是刻意的 lazy-load 防循环依赖（有注释）。

要求：先做决策并写明——统一收敛到 `api/` 包装层（`unwrapNullable` 空值语义一致），或把 lazy-load 场景登记为 sanctioned 例外并加 lint/CI 规则防止新增；不做纯机械大搬移，优先保证错误处理一致性；`T`：回归受影响组件/store 测试与 vue-tsc。

### 3.3 P2：可选（不阻塞本 Goal 完成）

- 后端 Low：`mcp_transport.go:479` 错误消息内嵌最多 1MB 响应体（SSE 侧已限 4096，应对齐）；`:590-595` SSE 客户端无 `ResponseHeaderTimeout`；`agent_execution_core.go:1332` `root, _ = workspace.Snapshot()` 忽略错误可使路径脱敏失效；`http_client_service.go:319` `_ = s.recordHistory(...)` 与同函数其它路径不一致；`agent_service.go:56` `\bformat\b` denylist 误伤 `clang-format`（仅 UX，注释已声明非安全边界）。
- 前端 Low：`stores/aiAssistant.ts:706-708` `isStandaloneReady` 恒 true 死逻辑；`InputComposer.vue:273-288` 粘贴多图只处理第一张、`FileReader` 无 onerror；`GitPanel.vue:388` 路径裸拼接（应复用 `stores/git.ts:69` 的 `joinWorkspacePath`）、`:236-237` `void handleRefresh()` 的 throw 分支脆弱写法；`ai.ts:1489`、`AiChatPanel.vue:175/191`、`git.ts:205` console.error 残留；`stores/lsp.ts` 约 35 处 console.debug 建议收敛到日志开关。
- 大函数拆分：`services/ai_service.go`（3207 行）、`internal/agentcore/runtime.go` Execute（约 215 行）、`services/mcp_config.go` SaveServer（约 120 行）。
- 测试覆盖缺口（按价值排序，不要求全补）：stores `aiGoal/appActions/connectivityStore/extensionHostUi/fileTreeRefresh/im/layoutStore/persona`；组件 `AssistantHeader/ContextChips/PlanPanel/DiffView/ThreadsPanel/MergeEditor/RebaseEditor/WorktreePanel/MarkdownContent`；views `AiAssistantView/DebugView/PluginsView/ProjectsView/RemoteView/TestView`；api `ai.ts/automation.ts/workspace.ts/platform.ts/extensions.ts`。

## 4. 验收标准

- **AC-01 版本控制收口**：全部工作按批次提交并推送；新 HEAD 上 `git status` 干净（除有意保留的本地产物）；CI 绿；提交历史可逐批审查。
- **AC-02 IM 安全**：SSRF 安全传输 + no-redirect + 私网 fail-closed + URL 变更重新审批；wechat_work token 真实生效或显式 unsupported；fixture 测试覆盖允许/拒绝两侧。
- **AC-03 单一包管理器**：仅存 `package-lock.json`；pnpm 残留删除；有防回退守卫；`npm ci` + vitest + vue-tsc 绿。
- **AC-04 竞态修复**：git/DiffView/mcp 三处 generation 守卫 + 各自回归测试。
- **AC-05 沙箱与死代码**：pprof 路径入沙箱并有拒绝测试；Legacy 审批管线删除且 agentcore 单一权威不回退；marketplace 下载 URL 复验。
- **AC-06 隐私与文档**：8 个 prompt 文档脱敏后入库；CI 个人路径守卫生效；已跟踪文件中个人路径为 0。
- **AC-07 依赖卫生**：jest 与废弃 @types 删除；`typecheck` script 存在。
- **AC-08 回归**：第 2.1 节全部命令重跑并记录结果，全部通过；受影响模块定向组另有命令与结果。
- **AC-09 诚实边界**：第 2.3 节 U 项不被改写为完成；最终报告明确列出仍未完成能力；证据类型沿用 `T`/`I`/`P`/`U`。

## 5. 执行策略

先 P0（提交收口 → IM 安全 → lockfile），再 P1（P1-05 需在涉及 docs/prompts 的提交前完成），P2 按余力执行。每个断点执行 Inspect → Implement → Verify → Evidence → Update。第 2.2 节列出的 prompt-16 已验证行为是回归红线：任何整改导致 2.1 节任一命令失败即停止并修复，不得用"重构需要"豁免。局部 mock、静态 binding、安装成功不能替代真实行为证据。

## 6. 未完成边界与禁止事项

1. 不得使用"源码存在""组件存在""binding 已生成""安装成功"替代激活证据。
2. 不得把 2.3 节 U 项（MCP UI smoke 转用户手动验证、Git UI smoke、AC-08 真实运行验证、外部 provider、packaged/跨平台）改写为完成。
3. 不得引入 Pinia 等新状态框架来"顺手重构"（与现状不符，见 2.4）。
4. 不得删除或弱化 `mcp_service.go` deny-only 面（CallTool/RequestToolApproval/ExecuteApprovedTool/Close 的 `//wails:ignore`）与 `scripts/lib/wails-bindings.mjs` 的 forbidden 策略。
5. 本 Goal 范围不含 packaged/release、跨平台与外部网络验证；不得在本 Goal 内声称完成。

## 7. 交付证据格式

每个 AC 记录：具体命令、测试名称或操作步骤；改动文件清单（file:line）；结果类型 `T`/`I`/`P`/`U`；失败时保留原始错误与影响范围。最终报告必须列出仍未完成的能力清单。

## 8. 执行状态

本轮（2026-08-29）仅完成只读审查与 2.1 节自动化验证，未做任何整改；`git status` 的 391 文件未提交状态原样保留，作为 P0-01 的输入。下一轮实现从 P0-01 开始。
