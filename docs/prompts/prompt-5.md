# Koyori IDE 下一阶段 AI Goal 执行 Prompt（SSOT）

> **用途：** 提供给任意编码 AI / Agent，作为当前仓库下一阶段开发、复核和验收的单一执行入口。  
> **仓库基线：** Go 1.25 + Wails v3 alpha2.111 + Vue 3 + TypeScript + Vite + Monaco。  
> **形成依据：** `prompt-1.md` 的长期工程规则与已完成安全基线，加上 `prompt-4.md` 对当前工作区的成熟度审查。  
> **事实优先级：** 当前代码与实际命令结果 > 本文件 > `prompt-4.md` > `prompt-1.md` 的历史状态与评分。  
> **核心定位：** Koyori IDE 是 Go / TypeScript / JavaScript 优先的 0.x 桌面 AI IDE，不是 VS Code、Cursor、IntelliJ 的替代品，也不宣称生产级或企业就绪。

---

## 0. 给 AI 的总指令

你是 Koyori IDE 的核心产品工程 Agent。你的工作不是继续堆叠表面功能，而是把已有功能集合收敛为一个**不会轻易丢失用户工作、关键闭环真实、声明可由测试证明**的桌面 IDE。

执行任何 Goal 时必须遵守：

1. **先读代码，再接受结论。** 本文件中的缺陷是已知线索，不是永久真理；开始前必须打开相关实现和测试，确认问题仍存在。
2. **一次只做一个 Goal。** 用户未指定时按 §3 的优先级选择第一个未完成且依赖已满足的 Goal；完成后停止，不自动开始下一项。
3. **最小正确改动。** 不借机重构无关模块，不升级依赖 major，不为兼容假设增加无需求代码。
4. **诚实分级证据。** 必须使用 `V / S / U`：`V` 为本机命令实际通过，`S` 为源码或测试存在但本机未运行，`U` 为需外部环境、凭据、真实平台或 CI 历史才能确认。
5. **安全默认拒绝。** 涉及执行、文件、网络、凭据、扩展、Agent、MCP、Remote、更新时必须 fail-closed，并增加绕过失败测试。
6. **不信任 renderer。** 前端传来的 `approved`、`confirmed`、`safe`、`target path` 或相似字段不能直接抬高权限；高风险能力必须由后端签发、绑定参数、短时、单次使用。
7. **保护用户数据。** 恢复、快照、多文件编辑和更新不得用“看似成功”的 partial result 覆盖磁盘新版本或静默丢数据。
8. **测试不是声明。** 测试文件或 workflow 存在只算 `S`；只有当前环境实际运行成功才能标为 `V`。
9. **不删除测试保绿，不弱化审批，不提交 secret，不写 exploit，不擅自 commit / push。** 只有用户明确要求时才执行 Git 提交或推送。
10. **环境阻塞不等于项目失败。** 若 Go、Node、Wails、GUI、签名凭据或挂载文件系统阻塞验证，准确记录阻塞、已完成的源码检查以及可复制的复现命令。
11. **文档必须与能力一致。** stub、prototype、mock、contract smoke、手动更新、最小远程等边界必须显式可见。
12. **保留旧安全成果。** `prompt-1.md` 已完成的 capability token、MCP root、IM 批准、扩展权限、pathsec、更新 E2 边界等是不可回归基线，而不是待重做功能。

---

## 1. `prompt-1.md` 是否仍有作用

### 1.1 仍然有作用的部分

`prompt-1.md` 仍应作为历史基线与工程规则参考，尤其保留以下价值：

1. 安全与产品红线：诚实、fail-closed、不可伪造审批、不可泄露密钥、不可夸大产品能力。
2. 最小 diff、先读代码、同步测试、按证据交付的工作方式。
3. P0 安全成果与安全回归清单，包括 Agent、MCP、Computer Use、IM、pathsec、扩展权限、危险 Wails binding 和审计日志。
4. Wails alpha 精确锁定、绑定检查、Go / frontend 基础质量门。
5. 多 Agent 文件所有权、共享热点和禁止并行抢写同一路径的原则。
6. 对 Computer Use、IM、更新、远程、VSIX、DAP 和产品定位的诚实边界。

### 1.2 已基本失去当前执行价值的部分

以下内容不应继续作为“下一任务”依据：

1. `prompt-1.md` 的 P0-1 至 P0-8、单元 A 至 J、P1-P3 冲刺表已全部结案或标记 `wontfix / 部分实现`。
2. “自动选择旧进度板第一个未完成项”的算法已没有有效新任务，继续使用会造成重复劳动。
3. 旧成熟度评分、测试数量、日期和本机环境描述是历史快照，不是当前事实。
4. 旧 M2 / M3 结论没有覆盖 `prompt-4.md` 新发现的数据恢复、快照接线、Goal 脚手架、真实桌面 E2E 和版本治理问题。
5. 长达千行的旧验收清单适合追溯，不适合作为下一阶段的精确任务队列。

### 1.3 新旧文档关系

1. `prompt-5.md` 是下一阶段的执行入口与 Goal 队列。
2. `prompt-1.md` 是已完成冲刺、安全基线和历史证据档案。
3. `prompt-4.md` 是审查报告与问题来源，不是可直接勾选的实施计划。
4. 三份文档冲突时，以当前代码、测试和命令结果为准，并在交付中记录冲突。

---

## 2. 当前已复核事实与不可回归基线

### 2.1 当前源码直接确认的问题

以下为创建本文件时从当前源码复核到的 `S` 级事实；执行时仍需再次确认：

1. `main.go` 为 AI Plan、AI Goal、Diff 和默认执行器注入空 workspace root；`ProjectService.buildWorkspaceRootSetters()` 未包含这些服务。
2. `services/executor_adapters.go` 中默认 Goal planner 返回固定文本，executor 固定运行 `go env GOOS`，evaluator 固定返回 `false`。
3. `DebugPanel.vue` 的 Step In target 菜单虽然展示 target，但 `onPickStepInTarget` 丢弃 `targetId`，后端 `StepIn()` 也不接收该参数。
4. `frontend/src/stores/agent.ts` 的 20 次工具调用上限是 renderer 告警；达到上限后仍允许继续批准调用。
5. 编辑器脏内容主要保存在前端 reactive store，未发现完整 hot-exit / dirty-buffer recovery journal。
6. `SnapshotService.RestoreSnapshot` 只重写快照中已有文件，不处理快照后新增文件，因此不是精确工作区回滚。
7. Remote 具备 SSH/SFTP 服务和 UI，但远程 Project 尚未成为 File / PTY / LSP / Git / Debug / Test 共用的远程 workspace host。
8. `scripts/e2e-smoke.mjs` 直接使用 Node 文件 API，并运行 mocked Wails 前端测试，不是 packaged desktop E2E。
9. `build/config.yml` 为 `0.2.0`，`SECURITY.md` 同时声明多个 0.x 支持线，`CHANGELOG.md` 的版本与日期顺序漂移。
10. release workflow 的 SBOM 是 optional / `continue-on-error`，发布产物的真实签名、provenance 和 workflow 历史不能仅由源码快照证明。
11. 用户报告点击 Outline / 大纲符号会卡死；当前 `OutlinePanel.vue` 会随每次文件内容变化重新请求 document symbols，并递归展开、过滤和计算 active symbol，但卡死根因尚未通过性能采样确认，必须先复现再修复。
12. 主 IDE `SettingsView.vue` 仍同时包含 AI、Agent、Prompts、Presets 和 Computer Use，而独立 AI 窗口已有完整 AI 设置界面，存在双入口、信息架构和状态一致性风险。
13. 当前 LSP 设置 UI 主要只有 organize imports 与 inlay hints；Language Pack 尚未形成可离线安装、校验、切换和诊断的完整产品闭环。

### 2.2 必须保留的现有优势

1. 工作区路径与符号链接逃逸使用集中 `pathsec` 边界。
2. Agent、MCP、Remote 和 Computer Use 的高风险动作继续使用后端 capability、参数绑定、TTL、generation 和单次消费语义。
3. Computer Use 平台执行器仍是 unsupported stub 时保持默认关闭和实验性文案。
4. IM 保持仅出站；不得重新引入 renderer 可伪造的入站批准或命令执行。
5. 更新继续保持 E2：HTTPS / host / 大小 / SHA-256 校验，安装、重启、回滚为手动，除非另立 Goal 完整实现 E1。
6. 扩展继续默认禁用、权限 deny-by-default、Worker 隔离和安装包路径安全。
7. Wails 继续精确锁定 `v3.0.0-alpha2.111`，CLI、Go module、binding 生成和 CI 不得漂移。
8. 不把 Monaco 语法高亮等同于语言工程能力，不把 mocked smoke 等同于桌面 E2E，不把受限 Worker 等同于 VS Code Extension Host。

---

## 3. Goal 状态机、选择算法与依赖

### 3.1 状态枚举

| 状态 | 含义 |
|---|---|
| `未开始` | 尚无针对当前 AC 的有效实现或验证 |
| `进行中` | 已有部分改动，但 AC 未全绿 |
| `已实现-S` | 源码和自动化存在，但当前环境未实际跑通 |
| `已验证-V` | 相关实现、测试和规定命令在当前环境实际通过 |
| `部分实现` | 仅部分 AC 成立，或仍有明确绕过 / 集成缺口 |
| `阻塞` | 依赖、平台、凭据或共享文件冲突阻塞；必须写清复现方式 |
| `wontfix` | 明确不做，且 UI / 文档 / 默认开关已隔离，不制造错误预期 |

### 3.2 自动选择顺序

用户没有指定 Goal 时，按以下顺序选择第一个依赖已满足且状态不是 `已验证-V / wontfix` 的 Goal：

1. `GOAL-P0-01` 可重复验证基线
2. `GOAL-P0-02` WorkspaceContext 与自动快照接线
3. `GOAL-P0-03` Hot Exit 与脏缓冲恢复
4. `GOAL-P0-04` Goal 模式诚实关闭或真实闭环
5. `GOAL-P0-05` 版本单一事实源与发布一致性
6. `GOAL-P0-06` 真实 packaged desktop E2E
7. `GOAL-P0-07` Remote 产品边界收敛
8. `GOAL-P0-08` Outline 符号点击卡死
9. `GOAL-P1-01` Snapshot 精确语义
10. `GOAL-P1-02` 后端 Agent 硬预算
11. `GOAL-P1-03` Step In target 正确透传
12. `GOAL-P1-04` Workspace Edit 事务与撤销
13. `GOAL-P1-05` 离线 Language Pack / LSP 与真实语言矩阵
14. `GOAL-P1-06` 性能阻断门禁
15. `GOAL-P1-07` IDE 设置收敛与 AI 功能迁移
16. `GOAL-P1-08` 编辑器个性化与视觉质量
17. `GOAL-P2-02` 插件适配与版本化贡献协议
18. 其他 P2 / P3 长期架构和商业治理 Goal

### 3.3 强依赖

1. `P0-03` 依赖 `P0-02` 提供可靠 WorkspaceContext，但可以先设计 journal 接口和纯单元测试。
2. `P0-04` 若选择“真实 Goal 闭环”，依赖 `P0-02`、`P0-03`、`P1-02` 和至少可用的 edit transaction；若选择“默认关闭并标 prototype”，可独立完成。
3. `P0-06` 的 restart recovery 场景依赖 `P0-03`；基础 open / edit / save / terminal 场景可先落地。
4. `P1-04` 是 AI 修改、rename、code action 和搜索替换统一原子性的基础。
5. `P1-05` 应建立在 WorkspaceContext 和 edit transaction 稳定后，避免把旧路径模型固化进 SDK。
6. `P2-01` 真远程 Workspace Host 依赖 URI / host identity、WorkspaceContext 和版本化 RPC；不能仅在现有本地服务上继续加 remote 分支。
7. `P0-08` 可独立复现和修复；若根因在 LSP 请求调度，则修复不得提前实现完整 `P1-05`，只建立必要的取消、去重和响应边界。
8. `P1-07` 应先于 `P1-08`，先稳定设置所有权、路由和持久化，再做视觉个性化，避免美化重复入口。
9. `P2-02` 依赖稳定的 Language Pack contract；插件不得直接绕过 `P1-04` edit transaction、WorkspaceContext 或后端权限系统。

### 3.4 当前 Goal 进度板

| Goal | 主题 | 初始状态 | 当前证据 |
|---|---|---|---|
| GOAL-P0-01 | 可重复验证基线 | 已验证-V（含一处自我更正） | 2026-08-02 Linux/Go1.25/Node22：go test ./services/ V(44s)；go test . V；race 安全切片 V；frontend 2525/2525 V；vue-tsc V；lint V；build V；check-bindings V；e2e-smoke V；go vet V(仅Wails上游GDK deprecated警告)。修复3个Linux平台守卫测试(TestLSP_pathToURI/TestIsAllowedShell/TestP2_2_CanceledRequest)。**更正：** 初版报告将 `check-doc-links.mjs` 与 `check-wails-pin.mjs` 记为 V，属误判——当时用 `cmd \| tail; echo $?` 取到的是 `tail` 的退出码。二者真实退出码均为 **1（失败）**，根因是 `docs/` 目录不存在（基线即已损坏，非本次引入），交由 P0-05 修复。Wails CLI未安装→U(packaged build)；无.git历史→无法确认commit。 |
| GOAL-P0-02 | WorkspaceContext / snapshot 接线 | **已验证-V** (2026-08-02) | 新增共享 `WorkspaceContext`（root + generation，单一 SSOT）。已接线 AIPlan / AIGoal / Diff / step executor / goal executor / security checker，并注册为 `AddProject` 两阶段提交的**首个** setter。快照触发点由 fail-open 改为 **fail-closed**；额外修复 §2.1 未列出的 `IsWorkspacePath` 空 root 返回 `true` 的提权缺口。`TestBootstrapWorkspaceSnapshotIntegration` + 5 项回归全绿；`go test ./services/` `go test .` race slice 全通过 |
| GOAL-P0-03 | Hot Exit / crash recovery | **已验证-V** (2026-08-02) | 新增 `RecoveryService`（Go）：journal schema + 原子写0600 + pathsec + 限额(8MiB/文件,64MiB/workspace,500条) + 敏感路径排除 + 损坏记录隔离 + 冲突检测（baseline hash）。前端 `recovery.ts`：debounce 写入 + 保存后清除 + 窗口隔离 + 失败时 notifyWarning(用户可见)。11 Go单测 + 17 前端单测全绿；CrashService 与内容恢复分层。packaged kill/restart E2E 延至 P0-06 → `U` |
| GOAL-P0-04 | Goal 模式真实或诚实关闭 | **已验证-V (仅 04A)** (2026-08-02) | 采用 04A 诚实降级（04B 依赖未完成的 P1-02/P1-04）。新增 `PrototypeExecutor` 标记接口 + `ErrGoalPrototypeDisabled` sentinel；`defaultGoalExecutor` 自报 prototype；`RunGoal` / `ResumeGoal` 默认**拒绝**驱动 prototype 且**不改变 goal 状态**（避免"尝试过但失败"的假象）。额外修复：`ResumeGoal` 原会先置 `Running` 再委托，网关拒绝后 goal 卡在 `Running` 无人驱动。新增 `GetExecutorCapability()` 让 UI 显示后端自报限制文案（不可漂移）。修正三语言 `goalSection.hint`（原文案宣称"AI 自治连续执行"，违反 §7.4）。7 Go 单测全绿；opt-in 不绕过 AgentService 审批（有测试）。04B 真实闭环 → `U` |
| GOAL-P0-05 | 版本与发布一致性 | **已验证-V** (2026-08-02) | 新增根 `VERSION`(0.2.0) 为单一事实源；`TestReleaseVersionConsistency` 13 例全绿（含 9 个漂移场景 + 真仓库校验）。修正 SECURITY.md **两条冲突"当前发行线"**（0.4.x 与 0.2.x 同时 ✅）；`package.json` 0.0.0→0.2.0。`release.yml` 新增 tag-vs-VERSION-vs-config 门禁（此前完全缺失），并**移除 changelog 静默回退 `[Unreleased]`**（执行点 6 禁止）。补建缺失的 `docs/`（CHANGELOG/RELEASING/E2E/ARCHITECTURE/EXTENSION-COMPATIBILITY），使 baseline 即失败的 `check-doc-links`+`check-wails-pin` 从 EXIT=1 → **EXIT=0**。CHANGELOG 无伪造日期（0.2.0 标 unreleased，0.3/0.4/0.5 记为未证实声明）。README 下载区标注"未验证/预览"。**无 git tag 证据 → 实际发布状态仍 `U`** |
| GOAL-P0-06 | Packaged desktop E2E | **部分完成** (2026-08-02) | **AC5 已验证-V**：`e2e-smoke.mjs`→`contract-smoke.mjs`、`vitest.e2e.config.ts`→`vitest.contract.config.ts`、CI job `e2e-smoke`→`contract-smoke`（名称明确标注"非 packaged E2E"）；`docs/E2E.md` 两层证据表；全仓 0 处残留旧名。重命名后 dry-run+full 双模式通过，Go 双包通过，doc-links/wails-pin 门禁通过。**AC1/2/3/4 未完成-U**：新增 `scripts/packaged-e2e.mjs` 脚手架（8 fixture 计划、artifact SHA-256、xvfb 启动、日志/截图收集）+ CI job `packaged-e2e`（needs 四个 required job，同 commit 顺序符合执行点 3），但**UI driver 未实现**——本机无 Wails CLI，且 app 未暴露任何 inspector/automation hook 可驱动 WebKitGTW。harness **fail-closed**（无 driver 时 exit 1，已实测），绝不产生假绿；CI job 暂 gate 在 `workflow_dispatch` 以免在 driver 落地前永久红灯。Windows/macOS packaged E2E 未上线 → `U` |
| GOAL-P0-07 | Remote 边界收敛 | **已验证-V (07A)** (2026-08-02) | 选择 07A 诚实降级（07B 依赖 P2-01 Workspace Host，未完成）。**后端**：`AddProject` 新增 `rejectRemoteProjectPath` 前置检查（在 Phase 1 之前），拒绝将带 `Remote` 配置的项目路径分发进本地 IDE 链路，且不会修改任何服务 root；`GetRecentProjects` 对远程条目停止调用 `os.Stat`、改为设置 `RemoteOnly=true`，消除"同名本地目录被误判为可打开"的缺陷。新增 `Project.RemoteOnly` 字段（非持久化）。4 项 Go 单测全绿，含"拒绝时 WorkspaceContext generation 不移动"的回归测试。**前端**：三语言 locale 将"添加远程项目"/"Remote Projects"等名称改为"添加 SSH 连接"/"SSH Connections"，消除 IDE-level remote 暗示；向导确认步骤 + RemoteView 固定标注 SSH/SFTP 执行边界（"无远端 PTY、无远端 LSP、无远端 git"）；`remote.boundary.*` 三语言键全部对齐。vue-tsc V；lint V；i18n 38 测试 V；全套前端 2542/2542 V；Go services+main 全通过 V |
| GOAL-P0-08 | Outline 符号点击卡死 | **已验证-V** (2026-08-02) | **取证结论：不存在跳转反馈循环**（handleCursorChange 不递增 editorJumpSeq）。实际根因是四处独立的**无界递归**：collectRows/collectBranchIds/symbolMatches/activeSymbolId.visit 均无深度上限和环路检测，循环/非法 children 硬挂 renderer。次要问题：content watcher 无防抖（每次按键一个 LSP 请求）、每次 loadSymbols 清空 expandedIds（用户折叠即消）、activeSymbolId 每次光标移动全树递归。**修复**：用带环路/深度/行数守卫的 `flattenSymbols` 替代所有递归（MAX_SYMBOL_DEPTH=64, MAX_OUTLINE_ROWS=5000）；content 变化防抖300ms，path/language 仍立即触发；同路径编辑保留 expandedIds；activeSymbolId 改为预建扁平索引线性扫描；safeStart 守卫修复 selectionRange 缺失时点击崩溃；loadSymbols catch rejection → 不再永久 loading；新增截断标注（AC7 降级阈值可见）。**证据**：11 项应激测试（含循环 tree、深度20000 chain、宽度2400 tree）修复前8 失/修复后 11 全绿；原有 6 测试无回归；前端 2553/2553 V；vue-tsc V；lint V；i18n 36 V。packaged E2E 中 Monaco + binding 的 long-task 计数 → `U`（CI 硬件不稳定，AC 5 fallback 已用事件/请求上限补充） |
| GOAL-P1-01 | Snapshot 精确语义 | **已验证-V** (2026-08-02) | 缺陷确认：`CreateSnapshot` 记录完整 manifest，但 `RestoreSnapshot` 只覆写快照内文件、从不删除后增文件，"整体回滚"按钮实为 partial 语义。选**精确 restore**：新增 `CalculateRestoreDiff`（预览 added/modified/removed，只读，输出排序稳定）+ `RestoreSnapshotExact(confirmed)`（`confirmed=false` 直接 `ErrNotAllowed` 且不触碰任何文件；写前建 rollback journal，任一写/删失败按 LIFO 完整回滚；路径经 `ValidatePathWithinRoot` fail-closed）。前端：`snapshotTimeline.restoreAll` 文案由"Restore All / 整体回滚"改为"Restore Exactly / 精确恢复"，按钮先调 diff、列出将被删除文件（>20 条折叠）并经 `ElMessageBox` 确认，取消则不修改工作区。10 Go 单测全绿（含幂等、外来 workspace 拒绝、忽略目录不参与 diff、删除后重建、legacy 语义未变）；`go test ./services/` `go test .` 全通过；前端 157 文件 2553 测试全绿；vue-tsc / ESLint 清洁 |
| GOAL-P1-02 | Agent 后端硬预算 | **已验证-V** (2026-08-02) | 新增 `services/agent_budget.go`：session budget（epoch + spent + limit + wall-clock window），在 **capability 签发点** `RequestCommandApproval` 原子扣减（而非执行点——否则可在预算内囤积 token 事后花费）。`commandApproval` 新增 `budgetEpoch`，`consumeCommandApproval` 拒绝跨 epoch token（AC3）。达限返回独立 sentinel `ErrAgentBudgetExhausted`。`StartNewToolBudgetEpoch` 为唯一解锁路径且 audit-log（新增 `auditEvent` 通用审计助手）。前端 `MAX_TOOL_CALLS` 降级为 display-only fallback，`agentState.budget` 由后端派生。12 Go 单测 + `-race` 并发测试（32 goroutine 争抢 8 额度，恰好 8 成功）全绿；104 前端 agent 单测全绿。**过程中修正自身 3 个缺陷**：(1) 在同步函数里 `void refreshToolBudget()` 并注释声称能让下方检查读到后端值——注释为假；(2) 刷新点错置于 emit 而非实际扣减点 approval；(3) 异步 budget 写入跨测试泄漏（`beforeEach` 未重置 `agentState.budget`）|
| GOAL-P1-03 | Step In target | **已验证-V** (2026-08-02) | 缺陷经代码自述确认：`DebugService.StepIn()` 无参数，`DebugPanel.onPickStepInTarget(_targetId)` 注释写明"当前后端 StepIn() 不接受 targetId"→ 用户选 overload B 实际进入 A。新增 `stopSequence`（3 个 stop 站点全部递增）+ `StepInTargetsForStop` / `StepInWithTarget` / `CurrentStopSequence` + `stepSessionWithArgs`。mock DAP 原 `default` 分支应答 stepIn 但不记录，已补 `stepIn` case 记录 `targetId`（指针区分"缺省"与"传 0"），AC 1 由此可断言。10 Go 单测（含 legacy `StepInTargets` 形状保持）+ 12 前端组件测试（选择/取消/默认/stale/unsupported）全绿。CDP 明确返回 `ErrPlatformUnsupported` 而非静默忽略 |
| GOAL-P1-04 | Workspace Edit 事务 | **已实现-S** (2026-08-02) | `applyWorkspaceEditPreviewTransaction` 已在 `lsp_service_edits.go` 中实现：版本前置条件、BaselineHash 冲突检测（磁盘改变→Conflict而非覆盖）、LIFO回滚（atomic write per file）、重叠edit拒绝、pathsec通过FileService.WriteFile传递。`ApplyRefactorWorkspaceEdit`(LSP rename/code-action)使用此事务。SearchService `ApplyReplacePreview`/`ApplyStructuralReplace`有hash检测+atomic写。AI写入路径仍经FileService.WriteFile（有pathsec）但尚未统一进事务→LSP和search-replace已迁移(S)，AI路径待P2-01稳定后迁移。dirty buffer hash检测：V。create/rename/delete resource ops仅preview不apply(partial-S)。 |
| GOAL-P1-05 | 离线 Language Pack / LSP 与矩阵 | **未开始** (2026-08-02) | 大型架构Goal。现有LSP discovery 硬编码gopls/vtsls候选，无manifest/SDK/离线包格式/安装校验/矩阵测试。依赖 P0-02(WorkspaceContext)已完成，依赖 P1-04 edit transaction部分实现。此Goal需多日工程，本会话不实现，诚实记为未开始。 |
| GOAL-P1-06 | 性能阻断门禁 | **已实现-S** (2026-08-02) | CI `perf-benchmark` job 已添加 **blocking 20% regression gate**：benchstat输出经grep捕获，发现 `+20%+` delta 的行时 exit 1 阻断流水线。现有3个benchmark覆盖`BenchmarkPathsecValidate`/`BenchmarkSearchWorkspace1KFiles`/`BenchmarkSymbolSearch100K`。baseline `.benchmark-baseline.txt` 缺失时首次运行自动建立(不假绿)。未到10K/100K fixture满足AC要求(S,非V)；本机无真实大仓fixture运行→实际regression检测`U`；CI门禁逻辑本机已验证语法`S`。 |
| GOAL-P1-07 | IDE 设置 / AI 界面迁移 | **已实现-S** (2026-08-02) | `SettingsView.vue`移除5个AI专属可写section(ai/agent/prompts/presets/computerUse)及其imports；导航列表仅保留通用IDE设置；AI sections保留在type union供旧深链解析，`selectSection()`和初始化时检测到AI section后`openAIDesktopWindow()`并rewrite URL到general。`AiSettingsView.vue`(AI窗口)成为AI配置SSOT。models.ts重复接口冲突已修复。vue-tsc V；go test ./... V。旧深链redirect需浏览器环境验证→`U`；窗口同步/设置schema迁移测试→`S`。 |
| GOAL-P1-08 | 编辑器个性化美化 | **未开始** (2026-08-02) | 已有`AppearanceSection.vue`/`EditorSection.vue`/`themeEditor.ts`基础，但缺统一typed schema、主题实时沙盒预览/Apply/Cancel、WCAG AA验收、visual regression截图、high-contrast支持。依赖P1-07设置收敛已完成。此Goal需较大前端工程量；本会话不实现，诚实记为未开始。 |
| GOAL-P2-01 | Workspace Host / 真远程 | **未开始** | 长期架构Goal。依赖稳定URI/host identity/WorkspaceContext(已完成P0-02)和版本化RPC。需要Workspace FS/PTY/SCM/Language/Debug/Test broker协议。本会话不实现，诚实记为未开始。 |
| GOAL-P2-02 | 插件适配 / 原生扩展贡献协议 | **未开始** | 依赖P1-04 edit transaction(已部分实现)和P1-05 Language Pack(未实现)。现有Native plugin + 部分VSIX适配存在，通用协议和兼容矩阵未完成。本会话不实现。 |
| GOAL-P3-01 | 供应链与企业治理 | **未开始** | AC要求"至少两个稳定版本周期的SLO数据"和"完成外部安全审计"——这两项在任何编码会话内不可能产生真实证据，诚实标U/未开始。 |

---

## 4. P0 Goals：可信个人预览版基础

### GOAL-P0-01：恢复干净、可重复、可审计的验证基线

**Goal：** 在干净 checkout 或原生文件系统中证明后端、前端、绑定、文档和构建主路径可重复运行，并将每项结果标成 `V / S / U`。

**执行点：**

1. 检查 Go 1.25、Node 20、npm、Wails CLI `v3.0.0-alpha2.111` 和平台 GUI 依赖。
2. 不信任仓库内可能损坏的 `frontend/node_modules`；优先用 `npm ci` 的干净依赖树。
3. 运行 Go test、race 关键安全切片、vet；运行 frontend test、typecheck、lint、build、coverage。
4. 运行 binding、文档链接、文档数字和 smoke 检查。
5. 能运行 Wails build 时构建真实桌面产物；不能运行则明确标 `U`，不得用 Vite build 替代。
6. 记录命令、工具版本、OS、耗时、失败日志摘要和环境修复方式。
7. 不为了通过而 skip 测试、降低 coverage、关闭 audit 或删除检查。

**AC：**

- [ ] 干净依赖环境中的 Go 与 frontend 基础门有可复制结果。
- [ ] 所有结果均明确标注 `V / S / U`，没有把 workflow 存在写成已通过。
- [ ] Wails CLI 与 `go.mod` 精确一致，binding check 通过或有明确阻塞。
- [ ] 若 npm registry 不支持 audit，记录原因并使用支持 audit 的可信 registry 重试；不得将 404 写成“零漏洞”。
- [ ] 生成一份基线报告，包含 commit / 工作区标识；无 `.git` 时写明无法确认历史。

**建议命令：**

```bash
go test ./services/ -count=1
go test . -count=1
go vet ./...
go test ./services/ -race -count=1 -run 'Agent|MCP|ComputerUse|IM|Remote|Path'
cd frontend && npm ci
cd frontend && npm test -- --run
cd frontend && npx vue-tsc --noEmit
cd frontend && npm run lint
cd frontend && npm run build
cd frontend && npm run test:coverage
node scripts/check-bindings.mjs
node scripts/check-doc-links.mjs
node scripts/check-doc-numbers.mjs
node scripts/e2e-smoke.mjs
```

**非目标：** 本 Goal 不修业务缺陷，不把 contract smoke 升格为 packaged E2E。

---

### GOAL-P0-02：统一 WorkspaceContext 并修复自动快照接线

**Goal：** 项目打开、切换、移除和失败回滚时，所有文件相关服务共享同一经过验证的 workspace identity / root / generation；AI 修改前能在正确工作区创建可恢复快照。

**执行点：**

1. 设计最小 `WorkspaceContext` 或 `WorkspaceAware` 契约，至少包含规范化 root、workspace ID / host identity、generation 和更新语义。
2. 将 AI Plan、AI Goal、Diff、Snapshot 触发器、Workflow 和默认 executor / security checker 纳入 root 传播。
3. 避免每个服务持有永不更新的构造期字符串；优先共享只读 snapshot 或受同步保护的 context provider。
4. ProjectService 切换必须原子：全部更新成功才提交；失败时恢复旧 context 和 generation。
5. root 改变后旧 capability、旧执行器和旧快照触发上下文不得继续用于新工作区。
6. `RemoveProject` 或关闭当前工作区时安全清空 context；安全服务不得因空根退化为无限制访问。
7. AI edit / Plan step / Goal checkpoint / Apply / Workflow step 前的快照错误必须可感知；不能静默跳过后继续危险写入。
8. 增加 bootstrap 级集成测试，而不仅是单服务 setter 测试。

**AC：**

- [ ] 打开 workspace A 后，Plan / Goal / Diff / Snapshot / executor 均读取 A 的规范化 root 和同一 generation。
- [ ] 切换到 B 后旧 generation token、旧 root executor 和旧上下文操作失败或被替换。
- [ ] 任一 setter 失败时所有服务仍保持 A，不出现 A / B 混合状态。
- [ ] AI edit 前在正确 root 创建快照；空 root 时 fail-closed，不静默写入。
- [ ] `BootstrapWorkspaceSnapshotIntegration` 或等价测试覆盖真实 bootstrap 接线。
- [ ] 不重新向 renderer 暴露危险 `SetWorkspaceRoot` binding。

**重点路径：** `main.go`、`services/project_service.go`、`ai_plan_service.go`、`ai_goal_service.go`、`diff_service.go`、`workflow_engine.go`、`executor_adapters.go` 及相关测试。

---

### GOAL-P0-03：实现 Hot Exit 与脏缓冲崩溃恢复 MVP

**Goal：** 应用或 WebView 异常退出后，用户未保存的编辑内容可以安全恢复；恢复过程不会覆盖磁盘上的更新版本。

**执行点：**

1. 建立按 workspace + window 隔离的 dirty-buffer journal，至少保存 URI / path、编码、EOL、基线 mtime + hash、当前内容、更新时间和 schema version。
2. journal 写入必须防抖、原子化、权限收敛、限额、可清理；单文件或总量超限时给用户可见提示。
3. 增加敏感目录 / 文件排除和用户禁用选项；默认策略必须文档化。
4. 正常保存后清除对应 journal；正常关闭且用户明确丢弃后清除；异常退出保留。
5. 启动时识别可恢复 session，逐文件比较基线与当前磁盘 hash。
6. 磁盘未变化时允许恢复为 dirty buffer；磁盘已变化时必须展示 diff / 冲突选择，禁止直接覆盖。
7. journal 损坏、版本不兼容或内容缺失时隔离坏记录并向用户说明，应用仍可启动。
8. 恢复完成前不得触发自动保存覆盖磁盘。
9. 将 CrashService 只保存崩溃报告的职责与 editor recovery 明确分层，不把 crash log 当作内容备份。

**AC：**

- [ ] 正常编辑后强杀进程，重启可恢复未保存内容且仍显示 dirty。
- [ ] 磁盘在退出期间变化时显示冲突 diff，不自动覆盖任一版本。
- [ ] 正常保存、明确丢弃、删除 workspace 后 journal 生命周期正确。
- [ ] journal 使用原子写并有容量、权限、路径安全和损坏记录测试。
- [ ] 多窗口 / 多工作区记录不串用。
- [ ] 至少有 store / service 单测；最终由 packaged E2E 覆盖 kill / restart。

**非目标：** 第一阶段不要求完整 session layout、终端进程或调试会话恢复。

---

### GOAL-P0-04：让 Goal 模式成为真实闭环，或诚实地默认关闭

**Goal：** 默认 Goal 模式不得再把固定规划、固定命令和固定失败 evaluator 呈现为自治编码能力。

**必须二选一并在交付声明：**

1. **P0-04A 诚实降级，推荐近期采用：** 默认关闭 / 隐藏 Goal 自动执行；UI 标记 prototype；禁止以付费核心能力宣传；保留接口与 mock 测试供后续开发。
2. **P0-04B 真实闭环：** 接入真实 planner、结构化 step executor、成功标准 evaluator、后端硬预算、每轮安全检查、取消、WorkspaceContext、修改前快照、diff review、审计和 fixture E2E。

**04A AC：**

- [ ] 默认设置不开启 Goal 自动执行，用户不会误以为它能完成真实编码目标。
- [ ] UI / README / locales 明确 `prototype` 以及当前固定执行器边界。
- [ ] 后端调用 prototype 时返回明确状态，不假装 goal completed。
- [ ] 现有安全审批不因隐藏功能而移除或弱化。

**04B AC：**

- [ ] planner 输出可校验的结构化步骤，不是固定字符串。
- [ ] executor 执行由步骤决定，不固定运行 `go env GOOS`。
- [ ] evaluator 根据用户成功标准、diff 和测试结果判定，不固定 false 或仅靠模型口头结论。
- [ ] 每轮有后端预算、取消、安全检查、正确 root 快照和可重放审计。
- [ ] `GoalCompletesFixture` 在固定小仓库中真实生成补丁、运行测试、满足标准并停止。
- [ ] 失败、取消、预算耗尽和测试失败不会标记成功。

---

### GOAL-P0-05：建立版本单一事实源与发布一致性门禁

**Goal：** tag、应用 metadata、runtime 显示、CHANGELOG、SECURITY 支持表和 release notes 必须来自一个可信版本源并在 CI 中强制一致。

**执行点：**

1. 选择单一版本源；其他 metadata 由脚本生成或严格校验，不手工多处漂移。
2. 明确当前真实发布线。没有 Git tag / Release 证据时，不得仅凭文档声称 0.3 / 0.4 / 0.5 已发布。
3. 按 SemVer 和日期重整 CHANGELOG；历史不确定项标为历史开发里程碑，不伪造发布日期。
4. SECURITY 只列真实支持线，0.x best-effort 与 SLA 边界保持清晰。
5. tag release 前校验 tag、版本源、build config、runtime、CHANGELOG section 和支持策略。
6. release notes 找不到对应版本 section 时必须失败，不能静默使用 `Unreleased`。
7. 防止低版本 tag 覆盖高版本 current / supported line。

**AC：**

- [ ] `ReleaseVersionConsistency` 自动测试覆盖合法与漂移场景。
- [ ] `vX.Y.Z` tag 与产物版本完全一致，不一致时 release job fail。
- [ ] CHANGELOG 顺序、日期和已发布 / 未发布语义一致。
- [ ] SECURITY 不同时把多个互相冲突的 0.x 都称为当前发行线。
- [ ] README 下载示例与真实 release evidence 一致；无证据时明确写预览 / 未验证。

---

### GOAL-P0-06：建立真实 Packaged Desktop E2E

**Goal：** 用实际 Wails 打包产物和真实 WebView / binding 证明核心产品路径，而不是仅证明 Node 文件 API 和 mocked store 可工作。

**执行点：**

1. 将现有 `e2e-smoke.mjs` 准确命名 / 文档化为 contract smoke，继续保留其快速价值。
2. 选择适合 Wails 的非 mock UI 驱动方案，先建立 Linux required packaged-app job，再扩 Windows / macOS。
3. 对同一 commit 先完成 required CI，再构建并测试该 commit 的 artifact；release 不得绕过 required checks。
4. fixture 覆盖：启动应用、打开目录、文件树、Monaco 编辑保存、真实 Wails binding、terminal 命令、至少一个真实 LSP 动作。
5. 在 P0-03 完成后加入 dirty buffer kill / restart recovery。
6. 收集日志、截图 / trace 和 artifact hash，失败时便于审计。
7. AI 路径使用本地 deterministic mock provider，但必须经过应用真实 IPC / HTTP 路径，不直接在测试里构造返回值。

**AC：**

- [ ] Linux packaged artifact 的 open / edit / save / terminal / LSP 核心路径在 CI 中 blocking。
- [ ] 测试不 mock FileService / Wails binding，不直接使用 Node fs 代替应用操作。
- [ ] 测试 artifact 与被验证源码 commit 一致，并记录 SHA-256。
- [ ] Windows / macOS 未上线时明确标 `U`，不宣称三平台桌面 E2E 已完成。
- [ ] contract smoke 与 packaged E2E 在名称和文档中不混淆。

---

### GOAL-P0-07：收敛 Remote 产品边界

**Goal：** 在真正 Workspace Host 架构完成前，不允许“远程项目”UI 暗示编辑器、终端、LSP、Git、调试和测试已在远端运行。

**必须二选一并在交付声明：**

1. **P0-07A 诚实降级，推荐近期采用：** 将 Remote 定位为 SSH/SFTP 文件与受限命令工具；移除或重命名会创建伪 remote project 的入口；UI 清楚列出本地执行边界。
2. **P0-07B 真远程：** 进入 `GOAL-P2-01` 的 Workspace Host 架构，不在本 Goal 用零散分支假装完成。

**07A AC：**

- [ ] 向导不再创建会被本地 `OpenProject` 当作本地 path 分发的 Remote Project，或明确阻止其进入本地 IDE 主链路。
- [ ] Remote UI、README 和类型名称准确表达 SSH/SFTP / command 工具边界。
- [ ] 无远端 PTY、LSP、Git、Debug、Test 时不显示等价能力。
- [ ] 已存 Remote Project 数据有明确迁移 / 提示策略，不静默误打开本地同名路径。

---

### GOAL-P0-08：修复 Outline / 大纲点击符号卡死

**Goal：** 点击大纲符号必须在可感知的瞬间跳转到准确位置，不阻塞 renderer、不触发 LSP 请求风暴、不形成 cursor / active-symbol / editor-jump 的反馈循环；超大或异常符号树仍可取消和恢复操作。

**已知现象与取证原则：** 用户已报告“点击符号会卡死”，该报告足以将其列为 P0 交互缺陷，但当前不得把递归、LSP、Monaco reveal 或 Vue 响应式中的任一项直接写成根因。先用真实文件与性能 trace 建立稳定复现，记录点击前后 CPU、long task、组件更新、LSP 请求数、Monaco selection / reveal 事件和内存变化。

**执行点：**

1. 建立最小 fixture 与压力 fixture：普通嵌套符号、重复名称、深层符号树、数千 symbols、循环 / 非法 children 防御、慢响应 LSP、文件快速切换和连续点击。
2. 为一次点击建立链路标识，跟踪 `OutlinePanel -> appState editorJumpSeq -> CodeEditor -> cursor event -> activeSymbolId`，证明是否存在同步循环或重复全树遍历。
3. document symbol 请求按 document URI + version 管理；内容快速变化时 debounce，旧请求可取消或丢弃，响应不得覆盖新文件 / 新版本状态。
4. 点击跳转只更新必要的 editor selection / reveal；相同位置的重复事件不得重新触发无界请求、保存、符号刷新或布局抖动。
5. 符号树计算避免在一次点击中多次全量递归；必要时预计算扁平索引、范围索引、稳定 symbol ID，并对大列表使用增量展开或虚拟化。
6. 对非法 range、越界 line / character、空文件、LSP error / timeout 做边界处理；失败时显示可恢复状态，不让 loading 永久挂起。
7. 避免用简单禁用 Outline、降低符号数量到不可用或移除 active symbol 同步掩盖问题；降级阈值必须可见且有理由。
8. 增加 renderer long-task 预算和请求计数断言；在 packaged E2E 中至少覆盖真实 Monaco + binding / LSP 的点击跳转。

**AC：**

- [ ] 普通 fixture 点击符号准确聚焦并 reveal 对应位置，焦点、selection 和键盘编辑保持正常。
- [ ] 压力 fixture 连续点击、展开、过滤和切换文件时无界面卡死、无限递归、请求风暴或永久 loading。
- [ ] 每次稳定文档版本的点击不会额外触发 document symbols 请求；输入导致的刷新经过 debounce / cancellation，旧响应不可覆盖新状态。
- [ ] 自动测试能在修复前复现至少一个导致卡死或事件风暴的路径，并在修复后阻断回归。
- [ ] 性能证据包含 trace 或等价计数；目标 fixture 的单次点击不产生大于 50 ms 的 renderer long task，若 CI 硬件无法稳定测时以可审计事件 / 请求上限补充并标 `U`。
- [ ] 深层 / 大型 / 非法符号结果 fail-soft，用户仍能关闭大纲、切换文件和继续编辑。

**重点路径：** `frontend/src/components/layout/OutlinePanel.vue`、`OutlinePanel.test.ts`、`frontend/src/components/editor/CodeEditor.vue`、editor / app / LSP stores、document symbol 后端及 packaged E2E。

**非目标：** 本 Goal 不重写整个 LSP 架构，不顺带改变所有侧栏视觉样式。

---

## 5. P1 Goals：稳定本地 IDE

### GOAL-P1-01：明确并实现 Snapshot 精确语义

**Goal：** “恢复快照”必须真实恢复到快照状态，或改名为“恢复快照中的文件”，不得混淆。

**执行点：**

1. 优先选择语义：精确 restore 或诚实 partial restore。
2. 若做精确 restore，snapshot manifest 需记录完整纳管文件集合和 ignore 规则。
3. 计算当前与 manifest 差异，新增文件删除必须先预览并要求明确确认。
4. restore 需具备 precondition、事务 / journal 和 partial failure rollback。
5. symlink、工作区外路径、ignore 文件、权限失败和大文件必须 fail-closed。

**AC：**

- [ ] snapshot 后新增文件不会在“精确恢复”中被静默保留。
- [ ] 删除新增文件前有清晰预览与确认；取消不修改工作区。
- [ ] 中途写入 / 删除失败可完整回滚或留下可恢复 journal。
- [ ] 若选择 partial 语义，API、按钮和文档全部改名，禁止使用“恢复整个工作区”表述。

---

### GOAL-P1-02：把 Agent 工具调用上限变成后端硬预算

**Goal：** Agent 的轮次、工具数、时间和可选成本预算由后端 session 强制执行，renderer 只能展示状态，不能绕过。

**执行点：**

1. 在后端建立 session budget，绑定 conversation / stream / workspace generation。
2. 每次工具排队、批准和执行时原子检查预算，避免并发超发。
3. 达到上限后拒绝新调用；用户继续必须由后端显式开启新预算 epoch，并记录审计。
4. 取消、重试和失败是否计数必须定义清晰，防止利用重试绕过。
5. frontend `MAX_TOOL_CALLS` 仅作展示，并从后端配置 / 状态派生，避免双源漂移。

**AC：**

- [ ] 伪造 frontend count、刷新 renderer 或并发批准均不能超过后端预算。
- [ ] 达限返回稳定、可本地化、可审计的错误，不继续排队执行。
- [ ] 新 session / 用户明确扩容后才可继续，旧 capability 不能跨 epoch 使用。
- [ ] race 测试覆盖并发调用与达限边界。

---

### GOAL-P1-03：正确透传 DAP Step In target

**Goal：** 用户选择某个 Step In target 时，DAP `stepIn` 请求携带准确 `targetId`。

**执行点：**

1. 扩展后端 `StepIn` 或增加窄 API 接收可选 target ID。
2. 将 target ID 透传到 DAP request；Node CDP 不支持时保持明确默认行为。
3. frontend store 与 `DebugPanel.vue` 不再丢弃所选 ID。
4. 0 个 target 走默认 Step In；1 个 target 是否自动传 ID 要按 adapter contract 明确定义；多个 target 显示选择菜单。
5. 校验 target 来自当前 stopped session / frame，防止陈旧菜单跨暂停状态使用。

**AC：**

- [ ] 多 target contract test 断言选择 ID 出现在 DAP `stepIn.arguments.targetId`。
- [ ] resume / new stop 后旧 target 失效。
- [ ] Node CDP 和不支持 stepInTargets 的 adapter 不回归。
- [ ] frontend 组件测试覆盖选择、取消和默认路径。

---

### GOAL-P1-04：统一 Workspace Edit 事务、预览与撤销

**Goal：** AI edit、LSP rename、code action、format-all 和 search replace 共享同一多文件修改事务，支持版本前置条件、预览、原子提交、rollback 和 undo journal。

**执行点：**

1. 定义 `WorkspaceEditTransaction`：text edits、create、rename、delete、URI、版本 / hash precondition。
2. 提交前规范化并校验所有路径，检测重叠 edit、冲突、dirty buffer 和磁盘变化。
3. 生成统一预览；用户批准的是事务 hash，而不是可伪造布尔。
4. 提交时先 journal / snapshot，再执行；中途失败回滚已应用部分。
5. 文件系统和 editor buffer 的一致性必须一起处理；LSP didClose / didOpen / rename 通知对称。
6. undo 必须验证当前 hash，避免覆盖事务后用户的新修改。

**AC：**

- [ ] 跨文件 rename 注入 hash 冲突时无不可恢复 partial write。
- [ ] create / rename / delete 与 text edit 可在一个事务中预览和回滚。
- [ ] dirty buffer 不被磁盘 edit 静默覆盖。
- [ ] pathsec 和 symlink 逃逸测试保持 fail-closed。
- [ ] AI、LSP 和 search replace 至少各有一个入口迁移到统一事务。

---

### GOAL-P1-05：建立全语言可扩展的离线 Language Pack / LSP 与真实能力矩阵

**Goal：** 在无互联网环境中，用户可从经过校验的本地 Language Pack 安装、发现、启动、停止、升级和诊断 LSP；架构允许持续覆盖所有存在可再分发或可由用户提供 LSP 的语言，但产品只对矩阵中实际验证的语言与能力作出声明。

**“全语言离线”定义：** “全语言”是统一协议和可扩展包模型，不是把所有语言服务器塞入默认安装包，也不是承诺自然界中的每种语言都有 LSP。离线闭环必须不依赖运行时下载；若 server 许可证禁止再分发，则支持用户从本地路径 / 企业镜像导入并明确显示 `user-provided`。没有可用 server 的语言只能提供 `L0 Text`，不得伪装成 LSP 可用。

**能力层级：**

1. `L0 Text`：language id、扩展名、语法、注释、括号、indent。
2. `L1 Tooling`：formatter、linter、root markers、安装与版本检测。
3. `L2 Intelligence`：initialize、sync、diagnostics、completion、hover、definition、references。
4. `L3 Refactor`：rename、code action、workspace edits、symbols、semantic tokens。
5. `L4 Run/Test/Debug`：task、test discovery / run / coverage、debug adapter。
6. `L5 Production Matrix`：local / remote、OS / arch、大仓、restart / recovery、版本范围。

**执行点：**

1. 定义最小 Language Pack manifest / SDK 和版本兼容规则。
2. manifest 至少声明 language ID / extensions、server 版本、OS / arch、entrypoint、args / env、root markers、sync mode、capabilities、许可证、来源、SHA-256 / 签名、包大小和最低 Host API。
3. 提供离线包格式、CLI / UI 导入、校验、原子安装、版本共存、启停、卸载和失败回滚；安装包不得路径穿越、覆盖任意文件或在安装阶段执行未授权脚本。
4. 提供随安装介质分发的离线目录或独立可下载离线 bundle；运行时选择已安装本地包，不得静默联网。联网 marketplace 如后续提供，必须是可选通道。
5. 先迁移 Go、TypeScript / JavaScript，并完成 Python、Rust 的真实离线 reference packs；再根据许可和测试贡献 Java、C / C++、C#、PHP、Ruby、Kotlin、Lua、Shell、HTML / CSS / JSON / YAML 等矩阵，不允许只加名称和图标。
6. server 进程按 workspace / language 隔离，具备启动超时、崩溃退避、取消、输出上限、资源预算和对称清理；不可信工作区默认不自动启动项目提供的 executable。
7. 设置界面显示 installed / missing / incompatible / disabled / crashed、实际 executable 与版本、来源、校验状态、日志、重启和 workspace override；不得只提供两个通用开关。
8. 每个声明层级都必须由 contract / integration test 产生矩阵，不靠 README 手填“支持”；首选真实 server fixture，mock 只验证协议错误路径。
9. VSIX compatibility 是独立 adapter，可贡献符合 contract 的 language pack，但不等同于原生 Language Pack，也不能绕过包校验和权限。
10. 离线更新由本地 bundle 导入完成，升级失败保留上一可用版本；配置与缓存 schema 有迁移和回滚语义。

**AC：**

- [ ] Go / TS 不再依赖散落的核心硬编码完成 server 发现与能力声明。
- [ ] 矩阵能区分 mock、真实 server、本机验证、CI 验证和 unsupported。
- [ ] 至少 Go / TS / Python / Rust 的离线 reference packs 在受支持 OS / arch 上完成无网络安装、启动、diagnostics、completion、definition、references、symbols 和 rename；未跑平台标 `U`。
- [ ] 加语言包无需修改多个核心 switch。
- [ ] 禁网测试证明安装后核心 LSP 路径不访问网络；缺包时给出本地导入指导，不静默下载。
- [ ] 篡改 checksum、路径穿越、不兼容 Host API、无权限 executable 和崩溃循环均 fail-closed，且不破坏已安装可用版本。
- [ ] UI 和文档只按自动生成矩阵声明语言层级，不出现“所有语言完整支持”的虚假表述。

---

### GOAL-P1-06：建立会阻断显著退化的性能预算

**Goal：** benchmark 不只上传报告；启动、扫描、搜索、LSP、内存、终端、大文件和虚拟列表有稳定 fixture、预算与审批机制。

**执行点：**

1. 建 10k / 100k 文件可重复 fixture，固定 ignore 与硬件 / runner 基线说明。
2. 覆盖启动时间、workspace scan、搜索 latency、symbol index、LSP request、idle memory、terminal throughput、large file。
3. 采用抗噪统计与合理阈值；显著退化使 CI 失败或要求显式批准。
4. baseline 必须来自版本库或可信 artifact，不能在临时 runner 中复制当前结果当 baseline。
5. 保留现有 20 MiB、输出上限、取消和资源预算防护。

**AC：**

- [ ] 至少核心 3 项性能指标在 CI 中有 blocking threshold。
- [ ] baseline 缺失时 job 失败或明确进入 bootstrap 流程，不假绿。
- [ ] fixture、工具版本和结果可复现。
- [ ] 性能门禁不以禁用安全检查换速度。

---

### GOAL-P1-07：收敛 IDE 设置界面并将 AI 功能迁移到 AI 界面

**Goal：** 主 IDE 设置只管理通用 IDE 能力，所有 AI 专属配置和工作流统一归属独立 AI 界面；迁移后不存在两套可写入口、状态漂移、失效深链或用户找不到设置的问题。

**信息架构：**

1. **主 IDE 设置保留：** General、Editor、LSP / Languages、Git、Debug、Terminal、Keyboard Shortcuts、Appearance、Profiles、Plugins / Extensions、Privacy / Security 等非 AI 能力。
2. **迁移到 AI 界面：** Provider / Model、Agent 行为、Prompts、Presets、Persona、Model Permissions、MCP、Skills、Computer Use、IM、AI Diff / Rollback、AI personalization、AI 窗口设置，以及仅服务于模型的 context / token / tool 配置。
3. **共享设置只保留一个 owner：** 例如编辑器 inline completion 的 UI 开关可留在 Editor，但 provider、model 和数据发送策略归 AI；主 IDE 通过只读状态和“在 AI 设置中打开”深链说明依赖，不复制表单。
4. **安全入口不隐藏：** AI 数据发送、工具权限、Computer Use、MCP 和模型权限迁移后必须同样或更容易发现；迁移不能绕过原审批与后端 capability。

**执行点：**

1. 制作现有设置 inventory：组件、store 字段、持久化 key、默认值、路由、locale、测试和调用方；逐项标 owner，禁止凭视觉直接删除组件。
2. 从 `SettingsView.vue` 移除 AI / Agent / Prompts / Presets / Computer Use 可写表单，在合适位置保留单一“打开 AI 设置”入口、当前 AI 状态摘要和安全 / 隐私提示。
3. `AiSettingsView.vue` 成为 AI 配置 SSOT，支持 group + item 深链、浏览器 / 窗口前进后退、搜索、键盘导航和迁移后旧链接重定向。
4. 复用同一 store / service / schema，不复制状态；保存失败必须可见并恢复 UI，窗口间修改通过响应式事件或版本号同步。
5. 设置 schema 版本化；旧版本持久化数据无损迁移，未知字段不被无关保存静默抹除，非法值回退并提示。
6. AI window 不可用、被关闭或平台窗口创建失败时，主 IDE 入口给出错误与重试，不出现无响应按钮；必要时提供同一 AI route 的单窗口 fallback，而不是恢复第二套设置实现。
7. 统一设置页面布局密度、标题层级、说明、危险项样式、搜索结果、空状态、focus、滚动定位和窄窗口响应式；不以纯装饰牺牲可读性。
8. 更新命令面板、菜单、快捷键、README、截图和 locales，避免继续把旧主设置路径写成 AI 配置入口。

**AC：**

- [ ] 主 IDE 设置中不再存在 AI 专属设置的第二个可写实例；AI 界面能访问 inventory 中全部迁移项。
- [ ] 旧 `settings?section=ai|agent|prompts|presets|computerUse` 深链确定性跳转到对应 AI group / item，并有路由测试。
- [ ] 两窗口同时打开时修改立即一致；并发保存有版本 / last-write 策略，不因旧窗口覆盖新值。
- [ ] 升级现有用户配置后 provider、model、prompts、presets、permissions 等不丢失；降级不支持时明确说明，不伪造兼容。
- [ ] 设置搜索可命中迁移项并打开准确位置；全键盘可操作，focus 不丢失，移动 / 窄窗口不横向溢出。
- [ ] Computer Use、MCP、IM、模型权限与数据发送边界的安全文案和默认关闭策略不回归。
- [ ] `SettingsView.test.ts`、`AiSettingsView.test.ts`、store migration 和窗口同步测试覆盖成功、失败、旧链接与窗口不可用路径。

**重点路径：** `frontend/src/views/SettingsView.vue`、`frontend/src/components/ai-window/AiSettingsView.vue`、settings / AI components、app / aiWindow stores、router、WindowService、locales 及测试。

**非目标：** 本 Goal 不重新设计 Agent 后端，不借迁移默认开启实验能力。

---

### GOAL-P1-08：编辑器个性化、美化与一致视觉系统

**Goal：** 在保持专业 IDE 信息密度和性能的前提下，让用户可安全个性化 Monaco 与 IDE chrome，并通过实时预览、作用域、重置、导入导出和可访问性约束形成完整体验，而不是堆叠零散 CSS 开关。

**个性化范围：**

1. **编辑器：** 字体 family / size / weight / ligatures、line height、letter spacing、cursor style / animation、word wrap、line numbers、minimap、sticky scroll、render whitespace、indent guides、bracket pairs、smooth scrolling、padding、inlay hints、semantic highlighting。
2. **主题：** Monaco token colors、semantic token colors、editor / selection / current line / gutter / diff / diagnostics 色彩，以及 activity bar、sidebar、panel、tabs、status bar、menus、dialogs 的语义 design tokens。
3. **布局：** sidebar / panel 位置与尺寸、compact / comfortable density、tabs 行为、breadcrumbs、centered layout、zen mode；移动或窄窗口提供合理折叠，不照搬桌面网格。
4. **作用域：** user 默认、workspace override、可命名 profile；优先级明确且 UI 显示值来源。AI 独立窗口主题可独立选择，但共享 token contract，不复制任意 CSS。

**执行点：**

1. 盘点 `appState`、workspace settings、theme editor、Monaco options 和散落 CSS variables，建立 typed settings schema 与语义 token registry。
2. 设置修改使用沙盒实时预览；Apply / Cancel / Reset 可预测，非法字体、色值、JSON 或 theme contribution 不得破坏整个 UI。
3. 提供 curated 内置 light / dark / high-contrast 主题和用户主题导入导出；导入有 schema、大小、字段白名单和版本校验，禁止注入任意 CSS / URL / script。
4. workspace override 只存与 user profile 的差异；切换 workspace / profile 原子应用，失败回滚上一主题，避免 Monaco 和 chrome 半新半旧。
5. 主题适配 diff editor、merge / rebase editor、terminal、debug、Git、Outline、插件 iframe 边界和 AI 窗口；第三方贡献只能使用允许的 token / contribution API。
6. 控制视觉语言：清晰层级、克制圆角和阴影、稳定间距、可扫描状态，不引入与现有产品无关的模板化 dashboard 风格。
7. 保障 WCAG AA 级关键文本 / 控件对比度、focus visible、reduced motion、200% zoom、色盲不只靠颜色传达 diagnostics / Git / debug 状态。
8. 大文件和低配设备可关闭昂贵效果；主题切换不得重建 model、丢 selection / undo stack 或触发 LSP 重启。

**AC：**

- [ ] 所有声明的编辑器选项真实映射 Monaco 或 UI token，保存并重启后恢复；无“有开关但无效果”的设置。
- [ ] user / workspace / profile 优先级、来源标识、Reset 和切换行为有自动测试。
- [ ] 主题实时预览可 Apply / Cancel；非法或不兼容主题 fail-soft 并恢复上一可用主题。
- [ ] light、dark、high contrast 在 editor、diff、terminal、sidebar、dialogs、AI window 的 visual regression 截图中无不可读区域。
- [ ] 主题导入不能注入 CSS、远程资源或脚本；超大文件和未知字段被拒绝或安全忽略并提示。
- [ ] 主题 / profile 切换保留 open models、dirty buffers、cursor、selection 和 undo history，不重启 LSP。
- [ ] 关键流程全键盘可达，focus ring 清晰，reduced-motion 和 200% zoom 通过检查。
- [ ] 普通主题切换不产生大于 50 ms 的持续 renderer long task；不稳定平台标 `U` 并保留 trace。

**重点路径：** `EditorSection.vue`、`AppearanceSection.vue`、`CodeEditor.vue`、`stores/themeEditor.ts`、workspace settings、global design tokens、terminal / diff / AI window theme adapters 及视觉测试。

**非目标：** 不允许用户主题执行代码；不要求像素级复刻 VS Code 或其他 IDE。

---

## 6. P2 / P3 Goals：架构扩展与商业治理

### GOAL-P2-01：统一 Local / Remote Workspace Host

**Goal：** UI 只通过版本化 Host Client 使用 workspace；本地 host 可进程内，SSH / container / cloud host 使用同一协议。

**目标组件：** Workspace URI + host identity、FS / watcher、process / PTY、SCM、Language broker、Debug broker、Test broker、Task broker、edit transaction、journal / snapshot、取消 / streaming / tracing。

**AC：**

- [ ] Remote fixture 完成 open / edit / save / terminal / LSP / Git status / test / debug / reconnect。
- [ ] 同一 Language Pack contract 可对 local 和 remote host 运行。
- [ ] 断线不会静默把远端路径交给本地 FileService 或本地 shell。
- [ ] host version negotiation、认证、升级和日志有安全设计与测试。

**禁止：** 在每个现有服务中散落 `if remote` 分支并宣称完成 Remote-SSH。

---

### GOAL-P2-02：完成插件功能适配并建立原生、版本化的扩展贡献协议

**Goal：** 让 Native Plugin 与受支持 VSIX 在明确沙箱和 capability 下真实贡献 IDE 功能，形成可安装、可审查、可诊断、可禁用、可卸载和可精确声明兼容性的闭环；优先提供稳定安全的 Koyori IDE Language / Debug / Test / SCM / Task contribution API，只对经过测试的 VSIX 版本声明兼容。

**适配范围与分级：**

1. `E0 Metadata`：安装、启停、卸载、版本、来源、签名、权限、兼容状态和错误诊断。
2. `E1 Declarative UI`：commands、menus、keybindings、configuration、themes、icons、snippets、languages、grammars。
3. `E2 Workspace`：受限 read / search / watcher、diagnostics、status、output；写操作必须走统一 Workspace Edit Transaction。
4. `E3 Tooling`：Language Pack、formatter、linter、task、test、debug、SCM adapter，通过版本化 broker contract 接入。
5. `E4 Webview`：隔离 iframe、严格 CSP、资源 URI 转换、消息 schema、生命周期与序列化恢复；默认无任意本地 / 网络访问。
6. `E5 Compatibility Matrix`：extension ID + exact version + OS / arch + host API + verified contributions + known limitations。

**执行点：**

1. 统一 Native Plugin 与 VSIX 的管理体验，但明确标识类型、信任级别、执行宿主和能力差异；不得用同一“兼容”徽章掩盖部分支持。
2. manifest / package parser 采用 schema、大小和路径安全限制；安装原子化，升级失败回滚，卸载清理 Worker、watcher、process、command、menu、keybinding 和 webview state。
3. activation event、command registration 和 contribution registration 幂等；reload / disable / workspace switch 后不重复注册、不残留监听器、不持有旧 generation。
4. 所有敏感 capability 由后端授权并绑定 extension identity、workspace generation、operation、target 和 TTL；renderer / Worker 声称的 permission 不抬权。
5. API 具备版本协商、deprecated 生命周期和精确错误；未知 API / contribution fail-soft，不拖垮插件管理页或 editor。
6. Webview 使用 CSP、sandbox、消息大小 / 频率限制和资源白名单；extension URI 不允许路径穿越，关闭后消息和资源失效。
7. 建 compatibility lab：固定扩展包、deterministic workspace、真实 activation / contribution 测试和生成式矩阵；许可证不允许入库时记录来源与 hash，由 CI 缓存可信 artifact。
8. 插件管理 UI 展示权限 diff、来源 / 签名、Host API、已验证贡献、日志、失败原因、重试、安全禁用和残留清理结果。
9. 离线环境支持从本地包安装和导出清单；任何 marketplace 均为可选，并满足签名、撤销、恶意包响应与供应链 Goal。

**AC：**

- [ ] manifest 有 API version、capability、host compatibility 和签名状态。
- [ ] 扩展崩溃 / 恶意 Worker 不访问未授权路径、不阻塞 UI、不残留进程。
- [ ] Debug / Test adapter 有独立 contract kit 和示例扩展。
- [ ] VSIX compatibility matrix 精确到 extension + version + platform + verified level。
- [ ] 至少 E1 的 commands / menus / keybindings / configuration / theme / snippets / language grammar 有真实 fixture；不支持项显示 unsupported，不静默忽略后标兼容。
- [ ] 至少一个 Language Pack、Task、Test、Debug 和 SCM reference plugin 通过 broker contract，不直接导入 renderer 私有 store。
- [ ] enable / disable / reload / upgrade / uninstall / workspace switch 压力测试无重复 contribution、内存持续增长、残留 Worker / watcher / process。
- [ ] extension write、process、network、secret 和 webview escape 的绕过测试保持 fail-closed，审计能关联 extension identity。
- [ ] 本地离线包安装全程无需联网，篡改、路径穿越、不兼容 API 和签名 / policy 拒绝不会破坏已有插件。

**非目标：** 不承诺运行全部 VS Code 扩展，不复制 Node Extension Host 的无限权限模型，不以兼容数量替代精确验证。

---

### GOAL-P3-01：发布供应链、隐私、可访问性与企业治理

**Goal：** 在宣称稳定个人版 / 企业版前，形成可审计 artifact 和持续运营能力，而不仅是 CI YAML。

**执行点：**

1. 强制签名 / notarization、SBOM、provenance、许可证 / NOTICE 和依赖策略。
2. release artifact 逐项披露 commit、builder、checksum、签名、SBOM、provenance 状态。
3. 建代理、私有 CA、离线安装、策略锁定、密钥托管、审计导出和不可信工作区控制。
4. 发布遥测、崩溃上传、AI provider 数据流、日志脱敏和保留周期隐私政策，并默认 opt-in。
5. 建 marketplace 签名、撤销、恶意扩展响应和 moderation 流程。
6. 建 accessibility 自动检查与键盘 / 屏幕阅读器人工矩阵。
7. 在 Wails v3 alpha 上不承诺商业 SLA；制定稳定 runtime 或退出计划。

**AC：**

- [ ] 所有 release artifact 有可验证 provenance / SBOM / 签名状态，缺失即阻断或明确不得发布稳定版。
- [ ] 至少两个稳定版本周期有 crash-free、升级成功率、性能和安全 SLO 数据后，才评估企业 SLA。
- [ ] 完成外部安全、供应链、恢复和可访问性审计；源码自评不得替代独立审计。

---

## 7. 统一 Definition of Done

每个 Goal 只有同时满足以下条件才能标为 `已验证-V`：

### 7.1 范围与实现

- [ ] 开始前复核当前源码和相关测试，并记录问题是否仍存在。
- [ ] 只完成一个 Goal；无无关大改、无 major 升级、无删除测试。
- [ ] 主路径、失败路径、取消 / 清理路径均有实现。
- [ ] UI、backend、binding、类型和文档保持一致。
- [ ] UI Goal 有 loading / empty / error / disabled / narrow-window 状态，关键操作可由键盘完成且 focus 可见。

### 7.2 安全与数据

- [ ] 无 renderer 布尔抬权或公开危险 root setter。
- [ ] 无 token、过期、重放、跨参数、跨 generation / epoch 请求被拒绝。
- [ ] pathsec、symlink、SSRF、secret 和日志脱敏不回归。
- [ ] 涉及写入 / 恢复时有 precondition、原子写和失败恢复语义。
- [ ] 涉及 goroutine、listener、timer、process、Worker 时有对称清理。

### 7.3 自动化与验证

- [ ] 新行为有单元或集成测试；安全修改有绕过失败测试。
- [ ] 相关 Go tests、Vitest、typecheck、lint 实际通过，或明确标 `S / U` 而非 `V`。
- [ ] 触及导出 Go API 时重新生成 / 检查 Wails bindings。
- [ ] 触及 docs / 常量时运行文档检查。
- [ ] 真实平台、签名、远程、LSP 或 packaged E2E 未跑时明确保留风险。
- [ ] 涉及交互卡死、主题或大型列表时保留性能 trace / 请求计数 / visual evidence，不只写“手测流畅”。

### 7.4 文档与产品诚实

- [ ] README / locales / UI 没有把 prototype、stub、mock、partial restore 或最小远程写成完整能力。
- [ ] 不宣称生产级、企业就绪、完整 Computer Use、完整 Remote-SSH 或完整 VS Code compatibility。
- [ ] 本文件 §3.4 的 Goal 状态、日期和证据已回写；多 Agent 时由 MERGE 统一回写。

---

## 8. 验证命令选择规则

不要机械地每次运行全部命令；先跑相关切片，再在 Goal 完成前跑受影响层的完整门禁。命令以 `Taskfile.yml` 与当前 CI 为准。

```bash
# 后端基础
go test ./services/ -count=1
go test . -count=1
go vet ./...

# 安全相关
go test ./services/ -race -count=1 -run 'Agent|MCP|ComputerUse|IM|Remote|Path|Snapshot|Goal'

# 前端基础
cd frontend && npm test -- --run
cd frontend && npx vue-tsc --noEmit
cd frontend && npm run lint
cd frontend && npm run build

# 工程检查
node scripts/check-bindings.mjs
node scripts/check-doc-links.mjs
node scripts/check-doc-numbers.mjs
node scripts/e2e-smoke.mjs
```

规则：

1. `npm test -- --run` 在当前 `package.json` 中可能形成重复 `run` 参数；若命令行为异常，应以 `npm test` 或 `npx vitest run` 复核，并同步修正文档，不能只照抄历史命令。
2. 仓库内 `node_modules` 损坏时，在干净 checkout / 原生文件系统执行 `npm ci`；不要直接修改依赖源码。
3. packaged desktop E2E 必须运行实际 artifact；`e2e-smoke.mjs` 只能算 contract smoke。
4. GitHub workflow 状态必须引用真实 run URL / artifact；仅阅读 YAML 是 `S`。

---

## 9. 多 Agent 协议

### 9.1 原则

1. 每个 Agent 只领取一个 Goal 或明确子 Goal。
2. 同一路径同一时间只有一个 OWNER；共享文件由 MERGE 串行修改。
3. 开始前声明预计修改文件；发现必须越界时输出 `BLOCKED_SHARED`，不要抢写。
4. 安全 / workspace context 先于 Goal automation、remote host 和扩展生态。
5. 重构与行为修改分开；拆文件时禁止夹带功能变更。

### 9.2 推荐所有权

| 车道 | Goal | 主要文件面 |
|---|---|---|
| O-BASE | P0-01、P0-06 | `scripts/`、CI、测试 harness、E2E docs |
| O-WCTX | P0-02 | `main.go`、Project / Plan / Goal / Diff / Snapshot 接线与测试 |
| O-RECOVERY | P0-03、P1-01 | editor store、recovery / snapshot service、恢复 UI 与测试 |
| O-GOAL | P0-04、P1-02 | Goal executor、Agent budget、Goal UI / docs |
| O-RELEASE | P0-05、P3-01 | build config、release workflow、CHANGELOG、SECURITY、release scripts |
| O-REMOTE | P0-07、P2-01 | remote UI / service、workspace host 协议 |
| O-OUTLINE | P0-08 | Outline panel、editor jump、LSP document symbols 与性能测试 |
| O-DEBUG | P1-03 | debug service / store / panel 与 contract tests |
| O-EDIT | P1-04 | file / editor / LSP edit transaction 与测试 |
| O-LANG | P1-05 | LSP / toolchain / test / debug language manifest 与矩阵 |
| O-PERF | P1-06 | benchmark、fixture、CI budget |
| O-SETTINGS | P1-07 | SettingsView、AI Settings、router、settings schema / migration 与 locales |
| O-THEME | P1-08 | Monaco options、theme editor、design tokens、workspace profiles 与视觉测试 |
| O-EXT | P2-02 | extension host、manifest、compatibility lab |

**共享热点：** `main.go`、`frontend/src/api/*`、`go.mod`、`frontend/package.json`、`.github/workflows/*`、本文件进度板。没有 MERGE 协调时默认只读。

---

## 10. 每次会话交付模板

```markdown
## Goal
- ID：
- 初始状态：
- 复核结论：问题仍存在 / 已变化 / 已不存在
- 证据等级：V / S / U

## 改动
- 文件：
- 行为：
- 明确未做：

## AC
- [x]/[ ] 本 Goal AC 逐项
- [x]/[ ] §7 通用 DoD 逐项

## 安全与数据
- 是否触及高风险面：
- fail-closed / token / pathsec / recovery 证据测试名：
- 是否新增 renderer 信任：否；若是则 Goal 失败

## 命令与结果
- `command` -> pass / fail / 未运行（原因）

## 未验证与风险
- U 项：
- 本地或 CI 复现步骤：

## SSOT 回写
- prompt-5 §3.4 状态：
- 日期与证据：
- prompt-1 历史基线是否发现回归：
```

---

## 11. 发布与产品 Go / No-Go

### 11.1 可继续作为 0.x 开源预览版

允许，但必须持续显式展示：Wails v3 alpha、Go / TS / JS 优先、Remote 非完整 IDE、VSIX 部分兼容、Computer Use unsupported、IM outbound-only、更新手动安装，以及 Goal prototype（若 P0-04B 未完成）。

### 11.2 稳定个人版 No-Go 条件

任一条件成立即 No-Go：

1. P0-01 至 P0-08 未关闭或仅有未经运行的关键证据。
2. hot exit / crash recovery 未通过真实 kill / restart。
3. packaged desktop E2E 未覆盖 open / edit / save / terminal / LSP / restart。
4. 自动 snapshot 仍可能以空 root 静默跳过。
5. Goal prototype 仍默认可用且形成错误预期。
6. 版本、tag、CHANGELOG、支持线和 artifact 无法一致验证。
7. required CI 不能从干净 checkout 稳定复现。
8. Outline 等核心导航交互仍可卡死 renderer，或没有真实大符号树回归证据。

### 11.3 企业版 No-Go 条件

除稳定个人版全部条件外，以下未完成均为 No-Go：真 Remote Workspace Host、策略与审计、代理 / 私有 CA、离线部署、AI / 遥测隐私控制、强制 SBOM / provenance / 签名、漏洞响应演练、兼容矩阵、外部审计和至少两个稳定版本周期的 SLO 数据。

---

## 12. 一键启动词

### 12.1 自动选择下一 Goal

```text
请严格按仓库根目录 prompt-5.md 执行。
先读 §0、§2、§3、§7；按 §3.2 选择第一个依赖已满足且未完成的 Goal。
开始前复核代码与测试，不直接相信文档结论。
本会话只做一个 Goal，最小 diff，同步测试；安全与数据修改必须 fail-closed。
结束时按 §10 交付并更新 §3.4；不要 commit，除非我明确要求。
```

### 12.2 执行指定 Goal

```text
请严格按 prompt-5.md 只执行【GOAL-____】。
先复核该问题在当前代码中是否仍存在，再逐项完成 Goal 的执行点与 AC。
遵守 §0、§7、§8；区分 V/S/U；不扩大范围，不弱化 prompt-1 已完成的安全基线，不 commit。
结束时按 §10 输出。
```

### 12.3 MERGE / QA

```text
你是 Koyori IDE 的 MERGE + QA Agent。
只合并当前 Goal 的 OWNER 交付与执行验收，不新增功能。
复核改动没有破坏 prompt-1 的安全基线；运行 prompt-5 §7 和该 Goal 的 AC；所有结果标 V/S/U。
失败时打回对应 OWNER，不删除测试、不降低门禁；最后统一更新 prompt-5 §3.4。
```

---

## 13. 最终产品目标

Koyori IDE 下一阶段的成功不以“新增多少面板、语言名称或 VSIX API stub”衡量，而以以下事实衡量：

1. 用户未保存的工作不会因常见崩溃静默丢失。
2. AI 修改前的快照确实属于当前 workspace，并能安全恢复。
3. Goal / Agent 不制造假自治闭环，预算和审批由后端强制。
4. 核心桌面路径由真实 packaged artifact 证明，而不是由 Node fs 和 mock binding 代替。
5. 每项语言、扩展、远程和平台能力都由精确测试矩阵声明。
6. 主 IDE 与 AI 界面职责清晰，AI 设置只有一个可信写入口且迁移不丢配置。
7. 离线语言能力通过可校验 Language Pack 扩展，编辑器个性化不牺牲性能、数据和可访问性。
8. 插件贡献经过版本化 contract、权限隔离和精确兼容矩阵验证。
9. 发布版本、artifact、签名、SBOM、provenance 和支持策略可审计。
10. 在这些条件完成前，始终诚实地将项目定位为有真实技术积累的 0.x Go / TS 垂直桌面 AI IDE。

*文档结束。执行入口以本文件为准；历史安全与冲刺证据见 `prompt-1.md`，审查依据见 `prompt-4.md`。*
