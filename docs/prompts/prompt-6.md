# Koyori IDE 断点续做 Prompt（prompt-6，SSOT）

> **用途：** prompt-5.md 的执行断点。P0-01 ~ P0-08、P1-01 ~ P1-03 已关闭；本文件只覆盖**未完成或仅部分完成**的 Goal。
> **仓库基线：** Go 1.25 + Wails v3 alpha2.111 + Vue 3 + TS + Vite + Monaco。
> **事实优先级：** 当前代码与实际命令结果 > 本文件 > prompt-5.md > prompt-4.md > prompt-1.md。
> **定位：** 0.x Go / TS 优先桌面 AI IDE。不是 VS Code / Cursor 替代品，不宣称生产级或企业就绪。

---

## 0. 总指令（继承 prompt-5 §0，不可弱化）

1. **先读代码再接受结论。** 本文件的缺陷描述是线索，不是真理。开始前必须打开实现与测试，确认问题仍存在，并在交付里写明"仍存在 / 已变化 / 已不存在"。
2. **一次只做一个 Goal。** 未指定时按 §2 顺序取第一个依赖已满足的未完成项。完成后停止，不自动开始下一项。
3. **最小正确改动。** 不重构无关模块，不升 major 依赖，不为假设需求加代码。
4. **诚实分级 `V / S / U`。** `V` = 本机命令实际通过；`S` = 源码/测试存在但本机未运行；`U` = 需外部环境、凭据、真实平台或 CI 历史。**禁止把 S 或 U 写成 V。**
5. **安全默认拒绝。** 执行、文件、网络、凭据、扩展、Agent、MCP、Remote、更新一律 fail-closed，并加绕过失败测试。
6. **不信任 renderer。** 前端传来的 `approved` / `confirmed` / `safe` / `targetPath` 不得抬权。高风险能力由后端签发、绑定参数、短时、单次使用。
7. **保护用户数据。** 恢复、快照、多文件编辑、更新不得用 partial result 覆盖磁盘新版本或静默丢数据。
8. **不删测试保绿、不弱化审批、不提交 secret、不擅自 commit / push。**
9. **环境阻塞 ≠ 项目失败。** 阻塞时记录阻塞原因、已完成的源码检查、可复制的复现命令。
10. **文档必须与能力一致。** stub / prototype / mock / contract-smoke / 手动更新 / 最小远程等边界必须显式可见。
11. **不可回归基线：** prompt-1 的 capability token、MCP root、IM 批准、扩展权限、pathsec、更新边界；prompt-5 已完成的 `WorkspaceContext`、`RecoveryService`、Agent budget epoch、Snapshot 精确语义、Goal prototype 网关。

---

## 1. 已完成部分（不要重做）

| Goal | 状态 | 关键产物（改动前请先读） |
|---|---|---|
| P0-01 | V | 基线可跑；3 个平台守卫/竞争测试已修 |
| P0-02 | V | `services/workspace_context.go`：共享 root + generation，参与 `AddProject` 两阶段提交；快照触发 fail-closed |
| P0-03 | V | `services/recovery_service.go` + `frontend/src/stores/recovery.ts`：dirty-buffer journal、限额、冲突检测、损坏隔离 |
| P0-04 | V（仅 04A） | `PrototypeExecutor` + `ErrGoalPrototypeDisabled`；`RunGoal` / `ResumeGoal` 默认拒绝驱动 prototype |
| P0-05 | V | 根 `VERSION` 为单一事实源；`release_version_test.go` 13 例；release workflow tag 门禁 |
| P0-07 | V（仅 07A） | `rejectRemoteProjectPath`；`Project.RemoteOnly`；UI 固定标注 SSH/SFTP 边界 |
| P0-08 | V | `flattenSymbols` 带环路/深度/行数守卫；content 防抖 300ms；stale-response generation guard |
| P1-01 | V | `CalculateRestoreDiff` + `RestoreSnapshotExact(confirmed)` + LIFO rollback journal |
| P1-02 | V | `services/agent_budget.go`：签发点原子扣减 + `budgetEpoch` 绑定 + `ErrAgentBudgetExhausted` |
| P1-03 | V | `StepInWithTarget` / `StepInTargetsForStop` / `stopSequence` |

**已完成 Goal 的 `U` 残留（不属于本文件任务，但报告时不得声称已验证）：** packaged artifact build、真实 SSH 集成、packaged kill/restart 恢复演练、Monaco long-task trace。

---

## 2. 待做 Goal 与优先级

按此顺序取任务；括号内为依赖。

1. `GOAL-P1-04R` 统一 Workspace Edit 事务的**剩余入口**（依赖已满足）
2. `GOAL-P1-06R` 性能预算**真实 fixture 与覆盖面**（依赖已满足）
3. `GOAL-P1-07R` 设置收敛的**测试与深链验证**（依赖已满足）
4. `GOAL-P0-06R` packaged desktop E2E **UI driver**（需 Wails CLI + GUI runner）
5. `GOAL-P1-08` 编辑器个性化与一致视觉系统（依赖 P1-07R）
6. `GOAL-P1-05` 离线 Language Pack / LSP 与真实矩阵（依赖 P1-04R）
7. `GOAL-P2-01` 统一 Local / Remote Workspace Host（依赖 P1-04R、P1-05）
8. `GOAL-P2-02` 插件贡献协议与兼容矩阵（依赖 P1-05、P2-01 的 broker contract）
9. `GOAL-P3-01` 发布供应链与企业治理（依赖真实发布历史，编码会话内不可完成）

---

## 3. GOAL-P1-04R：把剩余写入入口收进统一 edit transaction

**现状（已实现部分，先读再改）：** `services/lsp_service_edits.go` 的 `applyWorkspaceEditPreviewTransaction` 已有版本前置条件、`BaselineHash` 冲突检测、LIFO 回滚、重复路径拒绝。`ApplyRefactorWorkspaceEdit`（LSP rename / code action）走此事务。`SearchService.ApplyReplacePreview` / `ApplyStructuralReplace` 有 hash 检测与原子写，但**不共享同一事务实现**。

**未完成缺口：**

1. AI 写入路径（Plan step / Goal round / Diff apply）仍各自调用 `FileService.WriteFile`，多文件写入中途失败会留下 partial 状态。
2. `create` / `rename` / `delete` resource operations 只能 preview，`apply` 阶段直接拒绝，跨文件重命名无法完成。
3. search-replace 的多文件批量替换没有统一 rollback journal。
4. 没有"dirty buffer 与磁盘不一致时拒绝写"的跨入口统一断言。

**执行点：**

1. 把 `applyWorkspaceEditPreviewTransaction` 提取为不依赖 LSPService 的包级事务（建议新文件 `services/workspace_edit_transaction.go`），入参保持 `read` / `write` / `version` 注入以便测试。
2. 事务支持 resource operations：`create`（目标已存在则冲突）、`rename`（源缺失或目标存在则冲突）、`delete`（内容 hash 不匹配则冲突）。回滚必须能撤销创建（删除）、撤销重命名（改回）、撤销删除（写回原内容）。
3. 迁移三类入口至该事务：AI Diff apply、AI Plan/Goal 文件写入、SearchService 多文件替换。单文件 `Replace` 可保留但必须走同一 precondition 检查。
4. 路径一律经 `ValidatePathWithinRoot`（fail-closed），root 取自 `WorkspaceContext.RequireRoot()`；空 root 直接拒绝。
5. 事务开始前采集所有目标文件的 dirty buffer 版本，冲突时返回结构化 `Conflicts` 而非覆盖。

**AC：**

- [ ] 跨文件 rename 注入 hash 冲突时，工作区无不可恢复 partial write（有测试断言全部文件回到原内容）。
- [ ] `create` / `rename` / `delete` 与 text edit 可在同一事务中预览并回滚。
- [ ] dirty buffer 不被磁盘 edit 静默覆盖。
- [ ] pathsec 与 symlink 逃逸测试保持 fail-closed。
- [ ] AI、LSP、search-replace 三类入口各至少一个已迁移到统一事务，并有各自的失败回滚测试。

**重点路径：** `services/lsp_service_edits.go`、`services/search_service.go`、`services/diff_service.go`、`services/ai_plan_service.go`、`services/ai_goal_service.go`。

**非目标：** 不引入完整 undo/redo 历史栈；不改 Monaco 侧 undo。

---

## 4. GOAL-P1-06R：把性能门禁从"语法可用"提升到"真实可复现"

**现状：** `.github/workflows/ci.yml` 的 `perf-benchmark` job 已加 20% blocking gate（benchstat 输出中出现 `+20%` 以上 delta 则 exit 1），baseline 缺失时首次运行写入 `.benchmark-baseline.txt`。现有 benchmark 仅 3 个：`BenchmarkPathsecValidate`、`BenchmarkSearchWorkspace1KFiles`、`BenchmarkSymbolSearch100K`。

**未完成缺口：**

1. 无 10k / 100k 文件可重复 fixture（当前 1K 为运行时生成，ignore 规则未固定）。
2. 未覆盖启动时间、workspace scan、LSP request latency、idle memory、terminal throughput、large file。
3. blocking gate 的 grep 逻辑本机未跑过真实 regression，只验证过语法。
4. baseline 来源未固定为版本库 artifact，存在"临时 runner 自建 baseline 后永远通过"的风险窗口。

**执行点：**

1. 建 fixture 生成脚本（固定随机种子、固定目录结构、固定 `.gitignore`），10k 与 100k 两档；文档记录 runner 硬件基线。
2. 补齐 benchmark 覆盖上述 6 项，抗噪用 `-count>=5` + benchstat。
3. 用一个人为退化的 commit 实测 gate 会 exit 1（证据放交付里），再回退该 commit。
4. baseline 提交进版本库，缺失时 job **失败**并提示 bootstrap 流程，不静默自建。

**AC：**

- [ ] 至少核心 3 项指标在 CI 中有 blocking threshold，且有一次人为退化实测拦截记录。
- [ ] baseline 缺失时 job 失败或明确进入 bootstrap，不假绿。
- [ ] fixture、工具版本与结果可复现（同一 runner 两次运行差异在噪声阈值内）。
- [ ] 性能门禁不以关闭安全检查换速度。

---

## 5. GOAL-P1-07R：补齐设置收敛的测试与深链验证

**现状：** `frontend/src/views/SettingsView.vue` 已移除 5 个 AI 专属可写 section（`ai` / `agent` / `prompts` / `presets` / `computerUse`）及其 import；type union 保留这些键用于旧深链解析；`selectSection()` 与初始化检测到 AI section 时调用 `openAIDesktopWindow()` 并把 URL 重写为 `general`。`AiSettingsView.vue` 为 AI 配置 SSOT。

**未完成缺口：**

1. 旧深链 `settings?section=ai|agent|prompts|presets|computerUse` 的跳转只有源码，无路由测试。
2. 缺 AI 窗口不可用 / 创建失败时的错误与重试路径（当前 `openAIDesktopWindow()` 失败被 catch 吞掉，按钮看起来无响应）。
3. 无双窗口同时打开时的一致性测试，也无并发保存的版本 / last-write 策略。
4. 设置 schema 未版本化，旧配置迁移无测试。
5. 设置搜索是否能命中已迁移项并打开 AI 窗口准确位置，未验证。
6. `SettingsView.test.ts` / `AiSettingsView.test.ts` 未覆盖上述路径。

**执行点：**

1. 加路由测试：每个旧 section 深链都断言"调用了 openAIWindow + URL 被重写 + 未渲染任何 AI 可写表单"。
2. `openAIDesktopWindow()` 失败时给出可见错误与重试入口；必要时提供同一 AI route 的单窗口 fallback，**不得**恢复第二套设置实现。
3. 设置 schema 加版本号与迁移函数；未知字段不被无关保存抹除，非法值回退并提示。
4. 双窗口同步：修改经响应式事件或版本号广播；并发保存用 last-write-wins + 版本校验，旧窗口不得覆盖新值。
5. 搜索条目支持"迁移项"类型，命中后打开 AI 窗口并深链到对应 group/item。

**AC：**

- [ ] 主 IDE 设置中不存在 AI 专属设置的第二个可写实例（有测试断言）。
- [ ] 旧深链确定性跳转到对应 AI group / item，有路由测试。
- [ ] 两窗口同时打开时修改立即一致；并发保存不因旧窗口覆盖新值。
- [ ] 升级现有用户配置后 provider / model / prompts / presets / permissions 不丢失。
- [ ] 设置搜索可命中迁移项并打开准确位置；全键盘可达，focus 不丢失，窄窗口不横向溢出。
- [ ] Computer Use / MCP / IM / 模型权限 / 数据发送边界的安全文案与默认关闭策略不回归。
- [ ] `SettingsView.test.ts`、`AiSettingsView.test.ts`、schema migration、窗口同步测试覆盖成功 / 失败 / 旧链接 / 窗口不可用。

---

## 6. GOAL-P0-06R：packaged desktop E2E 的 UI driver

**现状：** `scripts/contract-smoke.mjs`（原 `e2e-smoke.mjs`）已正名，明确标注"非 packaged E2E"。`scripts/packaged-e2e.mjs` 脚手架已存在：8 个 fixture 计划、artifact SHA-256、xvfb 启动、日志/截图收集，**无 driver 时 exit 1（fail-closed，已实测）**。CI job `packaged-e2e` 暂 gate 在 `workflow_dispatch`。

**未完成缺口：** app 未暴露任何 inspector / automation hook，无法驱动 WebKitGTK；本机无 Wails CLI，无法产出 artifact。

**执行点：**

1. 为 packaged build 增加**仅测试构建标签下启用**的自动化端点（建议：本地 loopback + 一次性 token + 仅在 `KOYORI_IDE_E2E=1` 时监听），绝不在正式 release 构建中编译进去。加一个测试断言正式构建不含该端点。
2. driver 覆盖 fixture：启动、open workspace、open file、edit、save、terminal 执行一条命令、LSP hover/completion、kill -9 后 restart 并验证 dirty buffer 恢复（联动 P0-03）。
3. CI 配 Linux GUI runner（xvfb）+ Wails CLI；job 从 `workflow_dispatch` 转为 required 前必须先稳定通过 3 次。
4. artifact 记录 commit、checksum、runner 环境；失败时上传日志与截图。

**AC：**

- [ ] packaged artifact 由真实 build 产出，checksum 记录在 job 输出。
- [ ] open / edit / save / terminal / LSP / restart 六项 fixture 在 packaged artifact 上通过。
- [ ] kill -9 后重启能恢复未保存缓冲，且磁盘更新时呈现冲突而非覆盖。
- [ ] 自动化端点在正式构建中不存在（有测试证明）。
- [ ] Windows / macOS 未跑时明确标 `U`，不得声称跨平台已验证。

---

## 7. GOAL-P1-08：编辑器个性化、美化与一致视觉系统

**现状：** 已有 `AppearanceSection.vue`、`EditorSection.vue`、`stores/themeEditor.ts` 基础选项与 theme editor。

**未完成缺口：** 无 typed settings schema 与语义 token registry；无沙盒实时预览 / Apply / Cancel；无 high-contrast 内置主题；无主题导入白名单校验；无 WCAG AA 验收；无 visual regression 截图；user / workspace / profile 优先级无来源标识与测试。

**执行点：**

1. 盘点 `appState`、workspace settings、theme editor、Monaco options 与散落 CSS variables，建立 typed schema 与语义 token registry。
2. 设置修改走沙盒实时预览；Apply / Cancel / Reset 可预测；非法字体 / 色值 / JSON / theme contribution 不得破坏整个 UI。
3. 提供 curated 内置 light / dark / high-contrast；用户主题导入导出有 schema、大小、字段白名单、版本校验，**禁止注入任意 CSS / URL / script**。
4. workspace override 只存与 user profile 的差异；切换原子应用，失败回滚上一主题，不出现 Monaco 与 chrome 半新半旧。
5. 主题适配 diff editor、merge editor、terminal、debug、Git、Outline、插件 iframe 边界、AI 窗口。
6. 保障 WCAG AA 关键文本/控件对比度、focus visible、reduced motion、200% zoom、色盲不只靠颜色传达状态。
7. 大文件与低配设备可关闭昂贵效果；主题切换不重建 model、不丢 selection / undo stack、不触发 LSP 重启。

**AC：**

- [ ] 所有声明的编辑器选项真实映射 Monaco 或 UI token，重启后恢复；无"有开关但无效果"。
- [ ] user / workspace / profile 优先级、来源标识、Reset 与切换行为有自动测试。
- [ ] 主题实时预览可 Apply / Cancel；非法主题 fail-soft 并恢复上一可用主题。
- [ ] light / dark / high-contrast 在 editor / diff / terminal / sidebar / dialogs / AI window 的 visual regression 截图中无不可读区域。
- [ ] 主题导入不能注入 CSS、远程资源或脚本；超大文件与未知字段被拒绝或安全忽略并提示。
- [ ] 主题 / profile 切换保留 open models、dirty buffers、cursor、selection、undo history，不重启 LSP。
- [ ] 关键流程全键盘可达，focus ring 清晰，reduced-motion 与 200% zoom 通过检查。
- [ ] 普通主题切换不产生大于 50 ms 的持续 renderer long task；不稳定平台标 `U` 并保留 trace。

**非目标：** 不允许用户主题执行代码；不要求像素级复刻 VS Code。

---

## 8. GOAL-P1-05：离线 Language Pack / LSP 与真实能力矩阵

**现状：** LSP server discovery 主要硬编码（gopls / vtsls 等候选），无 manifest、无 SDK、无离线包格式、无安装校验、无矩阵测试。

**"全语言离线"定义：** "全语言"指统一协议与可扩展包模型，**不是**把所有 server 塞进默认安装包，也**不是**承诺每种语言都有 LSP。离线闭环不依赖运行时下载；许可证禁止再分发时支持用户从本地路径 / 企业镜像导入并显示 `user-provided`。没有可用 server 的语言只提供 `L0 Text`，不得伪装 LSP 可用。

**能力层级：** `L0 Text` / `L1 Tooling` / `L2 Intelligence` / `L3 Refactor` / `L4 Run-Test-Debug` / `L5 Production Matrix`（定义见 prompt-5 §5 GOAL-P1-05）。

**执行点：**

1. 定义最小 Language Pack manifest / SDK 与版本兼容规则。
2. manifest 至少声明 language ID / extensions、server 版本、OS / arch、entrypoint、args / env、root markers、sync mode、capabilities、许可证、来源、SHA-256 / 签名、包大小、最低 Host API。
3. 离线包格式 + CLI/UI 导入 + 校验 + 原子安装 + 版本共存 + 启停 + 卸载 + 失败回滚。安装包不得路径穿越、覆盖任意文件、在安装阶段执行未授权脚本。
4. 随安装介质分发离线目录或独立 bundle；运行时只选本地已安装包，不静默联网。
5. 先迁移 Go、TS/JS，再完成 Python、Rust 真实离线 reference packs；其余语言按许可与测试逐步贡献，**不允许只加名称和图标**。
6. server 进程按 workspace / language 隔离，具备启动超时、崩溃退避、取消、输出上限、资源预算、对称清理；不可信工作区默认不自动启动项目提供的 executable。
7. 设置界面显示 installed / missing / incompatible / disabled / crashed、实际 executable 与版本、来源、校验状态、日志、重启、workspace override。
8. 每个声明层级由 contract / integration test 产生矩阵，**不靠 README 手填"支持"**；首选真实 server fixture，mock 只验协议错误路径。
9. VSIX compatibility 是独立 adapter，可贡献符合 contract 的 pack，但不等同原生 Language Pack，也不能绕过包校验与权限。
10. 离线更新由本地 bundle 导入完成，升级失败保留上一可用版本；配置与缓存 schema 有迁移与回滚语义。

**AC：**

- [ ] Go / TS 不再依赖散落硬编码完成 server 发现与能力声明。
- [ ] 矩阵能区分 mock / 真实 server / 本机验证 / CI 验证 / unsupported。
- [ ] 至少 Go / TS / Python / Rust 离线 packs 在受支持 OS/arch 上完成无网络安装、启动、diagnostics、completion、definition、references、symbols、rename；未跑平台标 `U`。
- [ ] 加语言包无需修改多个核心 switch。
- [ ] 禁网测试证明安装后核心 LSP 路径不访问网络；缺包时给出本地导入指导，不静默下载。
- [ ] 篡改 checksum、路径穿越、不兼容 Host API、无权限 executable、崩溃循环均 fail-closed，且不破坏已安装可用版本。
- [ ] UI 与文档只按自动生成矩阵声明层级，不出现"所有语言完整支持"。

---

## 9. GOAL-P2-01：统一 Local / Remote Workspace Host

**Goal：** UI 只通过版本化 Host Client 使用 workspace；本地 host 可进程内，SSH / container / cloud host 使用同一协议。

**目标组件：** Workspace URI + host identity、FS / watcher、process / PTY、SCM、Language broker、Debug broker、Test broker、Task broker、edit transaction、journal / snapshot、取消 / streaming / tracing。

**AC：**

- [ ] Remote fixture 完成 open / edit / save / terminal / LSP / Git status / test / debug / reconnect。
- [ ] 同一 Language Pack contract 可对 local 与 remote host 运行。
- [ ] 断线不会静默把远端路径交给本地 `FileService` 或本地 shell。
- [ ] host version negotiation、认证、升级、日志有安全设计与测试。

**禁止：** 在每个现有服务里散落 `if remote` 分支并宣称完成 Remote-SSH。

---

## 10. GOAL-P2-02：插件贡献协议与精确兼容矩阵

**分级：** `E0 Metadata` / `E1 Declarative UI` / `E2 Workspace` / `E3 Tooling` / `E4 Webview` / `E5 Compatibility Matrix`（定义见 prompt-5 §6 GOAL-P2-02）。

**关键约束：**

1. Native Plugin 与 VSIX 统一管理体验，但必须标识类型、信任级别、执行宿主、能力差异；不得用同一"兼容"徽章掩盖部分支持。
2. 写操作必须走 P1-04R 的统一 Workspace Edit Transaction，不得绕过。
3. 敏感 capability 由后端授权并绑定 extension identity、workspace generation、operation、target、TTL。
4. Webview 用 CSP + sandbox + 消息大小/频率限制 + 资源白名单；extension URI 不允许路径穿越。
5. compatibility lab 用固定扩展包 + deterministic workspace + 真实 activation/contribution 测试生成矩阵。

**AC：**

- [ ] manifest 有 API version、capability、host compatibility、签名状态。
- [ ] 扩展崩溃 / 恶意 Worker 不访问未授权路径、不阻塞 UI、不残留进程。
- [ ] Debug / Test adapter 有独立 contract kit 与示例扩展。
- [ ] VSIX 矩阵精确到 extension + version + platform + verified level。
- [ ] E1 各项有真实 fixture；不支持项显示 unsupported，不静默忽略后标兼容。
- [ ] 至少一个 Language Pack / Task / Test / Debug / SCM reference plugin 通过 broker contract，不直接导入 renderer 私有 store。
- [ ] enable / disable / reload / upgrade / uninstall / workspace switch 压力测试无重复 contribution、无内存持续增长、无残留 Worker / watcher / process。
- [ ] extension write / process / network / secret / webview escape 绕过测试保持 fail-closed，审计可关联 extension identity。
- [ ] 本地离线包安装全程无需联网；篡改、路径穿越、不兼容 API、签名拒绝不破坏已有插件。

**非目标：** 不承诺运行全部 VS Code 扩展；不复制 Node Extension Host 的无限权限模型；不以兼容数量替代精确验证。

---

## 11. GOAL-P3-01：发布供应链、隐私、可访问性与企业治理

**重要前提：** 本 Goal 的两项 AC —— "至少两个稳定版本周期的 crash-free / 升级成功率 / 性能 / 安全 SLO 数据" 与 "完成外部安全、供应链、恢复、可访问性审计" —— **在任何单次编码会话内都不可能产生真实证据**。AI 只能推进可在仓库内落地的子项，其余必须标 `U`，**禁止伪造**。

**可在仓库内推进的子项：**

1. 强制签名 / notarization、SBOM、provenance、许可证 / NOTICE、依赖策略的**流水线实现**。
2. release artifact 逐项披露 commit、builder、checksum、签名、SBOM、provenance 状态。
3. 代理、私有 CA、离线安装、策略锁定、密钥托管、审计导出、不可信工作区控制的**实现**。
4. 遥测 / 崩溃上传 / AI provider 数据流 / 日志脱敏 / 保留周期隐私政策文本，默认 opt-in。
5. accessibility 自动检查（axe 等）接入 CI。
6. 明确写出"Wails v3 alpha 上不承诺商业 SLA"与稳定 runtime 或退出计划。

**AC：**

- [ ] 所有 release artifact 有可验证 provenance / SBOM / 签名状态，缺失即阻断或明确不得发布稳定版。
- [ ] SLO 数据与外部审计在缺席时明确标 `U`，且 README / SECURITY 不出现企业就绪表述。

---

## 12. 统一 Definition of Done（继承 prompt-5 §7）

### 12.1 范围与实现
- [ ] 开始前复核源码与测试，记录问题是否仍存在。
- [ ] 只完成一个 Goal；无无关大改、无 major 升级、无删除测试。
- [ ] 主路径、失败路径、取消 / 清理路径均有实现。
- [ ] UI、backend、binding、类型、文档保持一致。
- [ ] UI Goal 有 loading / empty / error / disabled / narrow-window 状态；关键操作键盘可达且 focus 可见。

### 12.2 安全与数据
- [ ] 无 renderer 布尔抬权，无公开危险 root setter（新增 trusted setter 必须带 `//wails:ignore` 并有 AST 测试）。
- [ ] 无 token / 过期 / 重放 / 跨参数 / 跨 generation / 跨 epoch 请求被接受。
- [ ] pathsec、symlink、SSRF、secret、日志脱敏不回归。
- [ ] 写入 / 恢复有 precondition、原子写、失败恢复语义。
- [ ] goroutine / listener / timer / process / Worker 有对称清理。

### 12.3 自动化与验证
- [ ] 新行为有单元或集成测试；安全修改有绕过失败测试。
- [ ] 相关 Go tests、Vitest、typecheck、lint **实际通过**，否则标 `S / U`。
- [ ] 触及导出 Go API 时检查 Wails bindings（`node scripts/check-bindings.mjs`）。
- [ ] 触及 docs / 常量时运行文档检查。
- [ ] 真实平台、签名、远程、LSP、packaged E2E 未跑时明确保留风险。
- [ ] 涉及卡死 / 主题 / 大型列表时保留 trace / 请求计数 / 截图，不只写"手测流畅"。

### 12.4 文档与产品诚实
- [ ] README / locales / UI 不把 prototype、stub、mock、partial restore、最小远程写成完整能力。
- [ ] 不宣称生产级、企业就绪、完整 Computer Use、完整 Remote-SSH、完整 VS Code 兼容。
- [ ] 回写本文件 §14 进度板。

---

## 13. 环境与验证命令

### 13.1 环境注意（实测）
- 仓库位于 WSL 挂载的 Windows NTFS（`/mnt/c/...`），**Go 编译与前端测试极慢**，全量 `go test` 会超时。
- **做法：** 用 `rsync -a --exclude node_modules --exclude .git` 把仓库同步到 `/tmp/koyori-ide-test`，在原生 fs 上运行测试；改动仍写回 `/mnt/c` 的原仓库。
- `node_modules` 若是在 Windows 下装的，缺 Linux 原生绑定（如 `@rolldown/binding-linux-x64-gnu`），需在 `/tmp` 副本里重新 `npm ci`。
- Go 在 `/usr/local/go/bin/go`（1.25.0），需要时手动加 `PATH`。
- 无 `.git` 历史 → 无法确认 commit；`go build` 需 `-buildvcs=false`。
- **Wails CLI 未安装** → packaged build / packaged E2E 一律 `U`。

### 13.2 命令（先跑相关切片，Goal 收尾前跑受影响层全量）

```bash
# 后端
go test ./services/ -count=1
go test . -count=1
go vet ./...

# 安全切片
go test ./services/ -race -count=1 -run 'Agent|MCP|ComputerUse|IM|Remote|Path|Snapshot|Goal|Recovery|Workspace'

# 前端
cd frontend && npm test
cd frontend && npx vue-tsc --noEmit
cd frontend && npm run lint
cd frontend && npm run build

# 工程检查
node scripts/check-bindings.mjs
node scripts/check-doc-links.mjs
node scripts/check-doc-numbers.mjs
node scripts/check-wails-pin.mjs
node scripts/contract-smoke.mjs
node scripts/packaged-e2e.mjs --dry-run
```

**规则：**
1. 取退出码用 `cmd; echo "EXIT=$?"`，**不要**用 `cmd | tail; echo $?`（会取到 `tail` 的退出码，prompt-5 曾因此误判两个门禁为 V）。
2. `npm test` 已是 `vitest run`，不要再传 `-- --run`。
3. packaged desktop E2E 必须跑真实 artifact；`contract-smoke.mjs` 只算 contract smoke。
4. workflow 状态必须引用真实 run URL / artifact；只读 YAML 是 `S`。

### 13.3 Wails binding 手写规则（CLI 缺失时）
binding ID = FNV-1a 32-bit of `koyori-ide/services.<Service>.<Method>`（已用 4 个已知 ID 验证）。Go 的 `(T, error)` 与 `T` 映射到**相同** TS 签名 `$CancellablePromise<T>`，因此只加 error 返回值无需改 binding。新增 service 需同时更新 `frontend/bindings/koyori-ide/services/<name>.ts`、`models.ts`、`index.ts`，并注意 `models.ts` 不要产生重复接口声明（曾因此触发 TS2717）。

---

## 14. 进度板（每次会话结束回写）

| Goal | 主题 | 状态 | 证据 |
|---|---|---|---|
| P1-04R | edit transaction 剩余入口 | **V（部分）** | `workspace_edit_transaction.go` 提取完成；DiffService.ApplyDiffTransaction、SearchService.ApplyMultiFileReplaceTransaction 已迁移；resource ops create/rename/delete + LIFO rollback + dirty-buffer check；24 个新测试全通过（2026-08-02）。剩余：write_file 工具路径未接入；前端仍可绕过调 FileService.WriteFile。 |
| P1-06R | 性能预算真实 fixture | 未开始 | gate 已存在但只验证语法；无 10k/100k fixture |
| P1-07R | 设置收敛测试与深链 | 未开始 | 迁移已完成；路由/同步/schema 迁移测试缺失 |
| P0-06R | packaged E2E UI driver | 未开始 | harness fail-closed 就绪；无 automation hook、无 Wails CLI |
| P1-08 | 编辑器个性化与视觉系统 | 未开始 | 有基础选项与 theme editor，缺 schema/预览/WCAG/visual regression |
| P1-05 | 离线 Language Pack / LSP | 未开始 | discovery 硬编码；无 manifest/SDK/离线包/矩阵 |
| P2-01 | Local / Remote Workspace Host | 未开始 | 长期架构 Goal |
| P2-02 | 插件贡献协议与矩阵 | 未开始 | 依赖 P1-05 与 broker contract |
| P3-01 | 供应链与企业治理 | 未开始 | SLO 与外部审计不可在会话内产生 → 永久 `U` 直到真实发布 |

---

## 15. 每次会话交付模板

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
- [x]/[ ] §12 通用 DoD 逐项

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
- prompt-6 §14 状态：
- 日期与证据：
- 是否发现已完成 Goal（prompt-5 P0-01~P1-03）的回归：
```

---

## 16. 一键启动词

### 16.1 自动选择下一 Goal
```text
请严格按仓库根目录 prompt-6.md 执行。
先读 §0、§1、§2、§12、§13；按 §2 顺序选择第一个依赖已满足且未完成的 Goal。
开始前复核代码与测试，不直接相信文档结论。
本会话只做一个 Goal，最小 diff，同步测试；安全与数据修改必须 fail-closed。
注意 §13.1 的 WSL 慢文件系统与 §13.2 的退出码陷阱。
结束时按 §15 交付并更新 §14；不要 commit，除非我明确要求。
```

### 16.2 执行指定 Goal
```text
请严格按 prompt-6.md 只执行【GOAL-____】。
先复核该问题在当前代码中是否仍存在，再逐项完成执行点与 AC。
遵守 §0、§12、§13；区分 V/S/U；不扩大范围，不弱化 prompt-1 与 prompt-5 已完成的安全基线，不 commit。
结束时按 §15 输出。
```

---

## 17. 最终产品目标（不变）

成功不以"新增多少面板、语言名称或 API stub"衡量，而以以下事实衡量：

1. 用户未保存的工作不会因常见崩溃静默丢失。
2. AI 修改前的快照确实属于当前 workspace，并能安全恢复。
3. Goal / Agent 不制造假自治闭环，预算与审批由后端强制。
4. 核心桌面路径由真实 packaged artifact 证明，而不是 Node fs + mock binding。
5. 每项语言、扩展、远程、平台能力都由精确测试矩阵声明。
6. 主 IDE 与 AI 界面职责清晰，AI 设置只有一个可信写入口且迁移不丢配置。
7. 离线语言能力通过可校验 Language Pack 扩展，个性化不牺牲性能、数据与可访问性。
8. 插件贡献经过版本化 contract、权限隔离、精确兼容矩阵验证。
9. 发布版本、artifact、签名、SBOM、provenance、支持策略可审计。
10. 在这些完成前，始终诚实地将项目定位为 0.x Go / TS 垂直桌面 AI IDE。

*文档结束。执行入口以本文件为准；已完成 Goal 的细节见 `prompt-5.md` §3.4，历史安全基线见 `prompt-1.md`，审查依据见 `prompt-4.md`。*
