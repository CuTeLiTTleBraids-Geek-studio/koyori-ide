# Koyori IDE 下一阶段规划：合格开源 AI IDE（prompt-14）

> **用途：** 把 2026-08-22 功能审查与 prompt-9~13 未闭环项，收成一份可交给后续 AI **按点执行** 的产品规划 SSOT。目标不是再做一次审查，也不是把产品砍成聊天外壳，而是把 Koyori 做成 **合格的开源桌面 AI IDE**。
>
> **与既有文档的关系：**
>
> - prompt-9 第 0、8、9、10、11 节始终有效（一次一个 Goal、S/T/I/P/R/U、fail-closed、不擅自 commit/push/tag）。
> - prompt-11 是 P9-G01~~G27 未完成索引；prompt-12 是审查 + P12-G28~~G33；prompt-13 是 2026-08-21 复审 + P13-G01~G05 收口。
> - **本文不替代 prompt-9 纪律，不勾选 prompt-12/13 的旧 AC。** P12-G33、P13-G05、P9-G21/G25/G26/G27 仍按原文档记账；本文新增 **P14-G34~G43**。
>
> **用户对本阶段的硬约束（不可弱化）：**
>
> 1. **不砍 Goal 模式。** 要换成真实 LLM 驱动的 plan→execute→evaluate，而不是删除入口或永远停在 prototype 文案。默认仍可要求 opt-in，但设置/AI 窗入口必须保留。
> 2. **不砍 Computer Use。** 要做成至少 Windows 可用的原生截图/键鼠，而不是继续整条 stub。默认关闭，设置入口必须保留。
> 3. **不砍扩展。扩展以 VSIX / VS Code API 兼容为北星**，而不是继续把「缺 `koyoriIde.permissions` 就拒绝安装」当成产品终点。
> 4. 产品目标是 **合格开源 AI IDE**，不是 Cursor 克隆口号，也不是 JetBrains 功能堆砌赛。
> 5. **收口的是默认壳，不是能力。** 活动栏默认只留 资源管理器 / 搜索 / Git / 扩展 / AI；Debug / Test 进命令面板（及 View 菜单）。Build / Database / HTTP / Inspections / Call Hierarchy 同样降为命令面板。服务、路由、Goal、Computer Use **不得删除**。
>
> **事实优先级：** 当前代码与本机命令 > 本文 > prompt-13 > prompt-12。历史 packaged SHA / 进度板勾选不能升级为当前产品可用。
>
> **工作区：** `C:\\Users\\Cute_\\Downloads\\Gugacode-main`
> **起草日期：** 2026-08-22
> **修订：** 2026-08-22 招入 Git 显示 BUG、活动栏收口、Debug/Test 命令面板、Go/TS 开箱、AI 补齐（`@codebase` / 内联补全）。不砍 Goal / Computer Use。

---

## 0. 继承规则（速查）

完整条款以 `docs/prompts/prompt-9.md` 第 0 节为准。本文只强调执行时最容易走歪的几条：

1. 每个 Goal 开始时写明缺口 **仍存在 / 已变化 / 已不存在**；行号只是线索，先重读文件。
2. **一次只推进一个 P14 Goal 的主体实现。** 不得并行改下一个 Goal。为当前 Goal 修回归测试、门禁、文档回写除外。
3. AC 未全勾选不得写「完成」。叙述、diff 行数、测试数量不能盖过失败门禁。
4. 证据分级：`S` 静态、`T` 单测/contract、`I` 真实进程/真实扩展/真实 provider、`P` 真实 packaged 工作流、`R` 真实 CI/tag/release/签名、`U` 未验证或环境阻塞。
5. mock 不得升级为 `I/P/R`。安装成功 ≠ 激活成功。文案诚实 ≠ 功能可用。
6. 安全默认 fail-closed。renderer 的 `approved` / `safe` / 路径 / 权限勾选不是授权。
7. 禁止手工猜 Wails binding ID。锁定 `WAILS3_BIN` 指向 `v3.0.0-alpha2.111`，禁止 `@latest`。
8. 不删除测试保绿，不放宽安全断言，不擅自 commit / push / tag / release。
9. 环境失败保持 `U`。`wails3 build` / packaged 重建若会打崩本机 DSH web GUI，停跑并如实记录，不得复用旧 24/24 SHA。
10. 修复后立即回写本文进度板与相关旧文档的交叉引用，不得在会话末凭记忆补写。

**与旧 Goal 的并行禁令：**

| 旧 Goal       | 本文关系                                                                                                                                               |
| ------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------ |
| P12-G33       | **被 P14-G34 收口，不再另开平行工具系统。** G33 AC 未勾选的项并入 G34；勾选仍写回 prompt-12，同时在本文进度板记账                                      |
| P13-G01~G04   | 已完成收口，不要重做审查式文案修补                                                                                                                     |
| P13-G05       | 门禁/供应链/packaged 残留并入 P14-G40；frontend:check flake 与 nanoid 不得假装消失                                                                     |
| P12-G28       | 暂缓。计费 Dashboard 不是「合格开源 AI IDE」的门槛                                                                                                     |
| P12-G29       | 被 P14-G35 覆盖（真 plan 工具 + 真 Goal executor）；不要再开一条「编排」平行线                                                                         |
| P12-G30 / G31 | diff3 与 diff-first 的 **Agent 写入审查** 并入 P14-G37；完整 Git 高级能力仍属 G30，本阶段不扩 bisect/worktree **功能**，但 **G41 必须修 Git 面板显示** |
| P12-G32       | 内置 skill 库可在 G35 之后做，**本阶段不作为关闭条件**                                                                                                 |
| P9-G25 / G26  | i18n 矩阵与 Remote Host **不是** 本阶段关闭条件                                                                                                        |
| P9-G21 / G27  | 发布签名/公证的全集仍 `U`；P14-G40 只要求 **一次诚实的 GitHub Release 最小集**。自动更新安装、崩溃上报端点继续拒绝/本地保留                            |

---

## 1. 产品定义：什么叫「合格的开源 AI IDE」

本阶段结束时，**可以**对外说：

1. 陌生人能按 README 在至少一个桌面平台从源码构建，并用 **Go 或 TypeScript** 打开本地项目：编辑 / 保存 / 终端 / Git 基础 / 格式化 / 测试 / Debug / LSP（本机已装 `gopls` / `typescript-language-server` 或 `vtsls`）。缺语言服务器时 **明确降级**，绝不把 mock 当可用。
2. 其他语言只承诺：本机 PATH 上有对应 LSP/工具则尽力接；没有就失败说明，不宣称「全语言 IDE」。
3. Chat 与 Agent 能在真实项目里读/写/搜/跑，工具调用、流式输出、审批卡可见，observation 回灌下一轮。写入先出补丁（G37）。工作区检索（`@codebase` / search 工具）可用。内联补全过期请求会取消，离线有徽标、不留空幽灵。
4. **Goal 不再是伪执行器**：opt-in 后用真实 LLM 做 plan→execute→evaluate。入口保留。
5. **Computer Use 在 Windows 上是真的**：默认关闭，设置入口保留。
6. **扩展以 VSIX 为准**：真实扩展能激活并产生用户可见效果；未知 API fail-closed。
7. **默认活动栏短**：资源管理器 / 搜索 / Git / 扩展 / AI。Debug 与 Test 从命令面板（及 View 菜单）打开，功能还在。
8. 仓库是可参与的开源项目：至少一次真实 GitHub Release（checksum）。没有这条之前，只称实验仓库。

本阶段结束时，**仍不可以**说：

- VS Code / Cursor / IntelliJ 替代品，或 Marketplace 全兼容。
- 生产级 / 企业就绪 / 四平台签名公证 / 自动更新安装 / 崩溃上报已接通。
- Remote-SSH / 全语言 IDE / 默认开启桌面控制。
- 「所有 VS Code 扩展都能用」。合格 = **矩阵内真实可跑**。

轻量化 = **默认壳短、主路径硬**。不是删 Goal、删 Computer Use、删扩展、删 Debug/Test 服务。

---

## 1.1 收口清单（本阶段必须落地，藏入口 ≠ 删能力）

| 面           | 默认可见                        | 必须保留                                                                                             | 禁止                                         |
| ------------ | ------------------------------- | ---------------------------------------------------------------------------------------------------- | -------------------------------------------- |
| 活动栏       | 资源管理器、搜索、Git、扩展、AI | Debug / Test / Build / Database / HTTP / Inspections / Call Hierarchy 可通过命令面板与 View 菜单打开 | 从代码库删除对应服务或路由                   |
| Goal         | AI 设置 + AI 窗，可 opt-in      | G35 真 LLM 循环                                                                                      | 删除 Goal 页/服务，或永远停在 prototype 文案 |
| Computer Use | 设置入口，默认关                | G36 Windows 原生                                                                                     | 删除 CU 页/服务                              |
| Agent        | 四工具 + MCP + 补丁预览         | G34 catalog、G37 diff、G43 检索/补全                                                                 | 平行第二套工具系统                           |
| 扩展         | VSIX 市场 + Worker Host         | G38/G39                                                                                              | 把 VSIX 降成「实验」或拆市场当关闭条件       |
| 语言         | Go/TS 开箱路径写进 README       | 其他语言 PATH 发现 + 诚实失败                                                                        | 符号索引 5 后缀宣传成全语言                  |
| 发布         | G40 最小 Release                | 商业级签名更新/崩溃上报/跨平台公证 **不做**，文案诚实                                                | 用本地 `beta0.2.0` 冒充正式版                |

---

## 2. 现状基线（2026-08-22，S；未重跑 GUI / 真实 provider）

沿用 prompt-13 与同日审查，只列本阶段会踩到的事实：

| 面           | 已有                                                                                                              | 缺口                                                                            |
| ------------ | ----------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------- |
| Chat / SSE   | `StartStream` / `ai:chunk                                                                                         | done                                                                            | error`，双窗 `ErrStreamBusy` | 真实外部 provider `U`；流式重试只覆盖非流式 |
| Agent        | 四内置工具 + 后端 catalog；MCP stdio/SSE/HTTP 已进 catalog                                                        | G33 AC 0/6；无 Git mutation 一等工具；独立 Agent 窗批准/真实 mutation `U`       |
| Plan         | 空步骤合法，UI 已诚实                                                                                             | **没有 `plan` 工具，AI 不能生成步骤**                                           |
| Goal         | UI 标明 Prototype，默认拒绝                                                                                       | `defaultGoalExecutor` 固定 `go env GOOS`，Evaluate 恒 false                     |
| Computer Use | 审批/审计/白名单骨架完整，默认关                                                                                  | **三平台 `ErrPlatformUnsupported`**                                             |
| 内联补全     | debounce + AbortSignal（`inlineCompletion.ts`）                                                                   | 无离线徽标强制、无「空幽灵」禁令 AC；真实模型 `U`                               |
| 代码库感知   | `search` 工具 + 用户 `@` 文件；符号索引仅 5 后缀                                                                  | **没有 `@codebase` / 强制工作区检索工具**                                       |
| 活动栏       | Explorer / Build / Database / Inspections / Search / Git / Extensions / HTTP / Debug / Test / Call Hierarchy / AI | 对轻量壳过载；Debug/Test 占一等图标                                             |
| Git 面板     | 功能超完整                                                                                                        | **显示 BUG（见 §2.1）**：260px 顶栏挤爆、未分暂存区、截断无提示、高级区默认展开 |
| 扩展         | Worker ABI 1.0、VSIX 解压配额、Open VSX hash、G24 故障恢复                                                        | 语料 **10/10 blocked**（缺 `koyoriIde.permissions`）                            |
| 开源发布     | MIT、NOTICE、门禁脚本、本地 tag `beta0.2.0`                                                                       | 无已验证 `v0.2.0` GitHub Release；nanoid high；P13-G05 packaged `U`             |

README 仍写 Agent「Git 操作」——内置 ToolDef 只有 `read/write/run/search`。本阶段在 G34/G37 补 **经审批的 Git 工具** 或改文档；禁止继续口头宣称。

### 2.1 Git 显示 BUG（P14-G41，源码已定位，本起草会话未开 GUI）

权威文件：`frontend/src/components/layout/GitPanel.vue`、`frontend/src/stores/git.ts`（`sidebarWidth` 默认 260，`app.ts`）。

| ID           | 现象                                                                                                                                                  | 证据                                                                                         |
| ------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------- |
| **GIT-UI-1** | 分支顶栏单行 flex、无 wrap：分支名 + ahead/behind + pull/push/refresh/rebase + **`.gitignore` 文字按钮** + **Review 文字按钮** 在 260px 侧栏溢出/重叠 | `GitPanel.vue` `.git-panel__branch-bar`；首个 `.git-panel__action-btn { margin-left: auto }` |
| **GIT-UI-2** | 变更列表未分 **Staged / Unstaged**；stage 与 unstage 按钮同时出现在每一行，状态语义含混                                                               | 模板 `git-panel__changes` 单列表；`GitFileChange` 只有 `path` + `status` 字符串              |
| **GIT-UI-3** | `gitState.truncated`（>1000 条）已计算，**UI 不提示**                                                                                                 | `stores/git.ts` `MAX_GIT_UI_CHANGES`；模板无 `truncated`                                     |
| **GIT-UI-4** | Commit graph 与 Worktree 两个 `<details open>` 默认展开，变更列表被挤成细缝                                                                           | `GitPanel.vue` 约 1063–1074 行                                                               |
| **GIT-UI-5** | 冲突行四个长文案按钮，窄侧栏必换行/溢出                                                                                                               | `git-panel__conflict-actions`                                                                |
| **GIT-UI-6** | 行内 stage/unstage/diff **仅 hover 显示**（`opacity: 0`），键盘与触控达不到                                                                           | `.git-panel__actions`                                                                        |
| **GIT-UI-7** | 长路径 ellipsis 只露末尾；可接受，但须保留 `title` 全路径，且 staged 分组后仍可读                                                                     | `.git-panel__path`                                                                           |

G41 关闭这些显示 BUG。**不**借机实现 P12-G30 的 diff3 / bisect 新功能。

---

## 3. 推进顺序（一次一个）

```
P14-G34  Agent 执行核心收口（继承 G33）
  → P14-G41  Git 面板显示修复
  → P14-G42  IDE 壳收口（活动栏 + Debug/Test 进命令面板）
  → P14-G35  Plan 工具 + Goal 真 LLM 循环
  → P14-G36  Computer Use Windows 原生
  → P14-G37  Diff-first 应用（Agent/Goal 写入审查）
  → P14-G43  AI 补齐（@codebase + 内联补全稳）
  → P14-G38  VSIX 为北星：安装/激活真实扩展
  → P14-G39  高流量 VS Code API（无假成功）
  → P14-G40  开源发布最小合格集（含 Go/TS 开箱口径）
```

依赖理由：先统一 Agent 工具管线，再修每天都要点的 Git 壳，再把活动栏收短，避免后续 Goal/CU/VSIX 继续往默认栏堆图标。Diff-first 必须在 Goal 真写入之前或紧随其后。`@codebase` 与补全依赖 G34 catalog。VSIX 先改权限再扩 API。发布放最后。

用户指定单个 Goal 时，用 §16.2，仍一次一个。编号 G41~G43 是历史 ID，**执行顺序以本节为准，不以数字大小为准**。

---

## 4. P14-G34：Agent 执行核心收口（继承 P12-G33）

**优先级：** P0（地基）
**最低证据：** `T` + 至少一条真实 Windows `I`（MCP stdio 或 loopback provider 工具轮次）
**现状：** 缺口 **已变化仍存在**。`internal/agentcore`、统一 catalog、MCP `mcp.call`、workflow typed adapters、headless CLI 已有 T/部分 I；G33 AC 仍 0/6。禁止新建第二套工具注册表。

**本 Goal 要关闭的产品缺口（不是再写一篇架构）：**

1. 前端 native defs / 围栏解析 / 审批卡 **只消费后端 catalog**；MCP / Skill / workflow 工具在 Chat Agent 模式下可被模型直接调用（不只 workflow runner）。
2. `StartAgentStream` 使用独立 `AIOpAgent` 计量/授权，缺 renderer target fail-closed。
3. 工具时间线、turn barrier、observation 回灌在主窗与 AI 伴侣窗都可见；批准只在发起窗，但发起窗必须真能批准。
4. 内置工具补上经审批的只读 `git.status` 与受限 `git.diff`（mutation 级 Git 留给 G37 的事务/diff 流，本 Goal 不做 `git commit` 自动执行）。
5. G33 已有的 capability / epoch / workspace generation / external receipt 语义不得回退。

**AC：**

- [x] **G34-AC1** `T`：单一 `ToolDef` catalog 仍是 builtin+MCP+workflow+skill 的唯一源；renderer `registerTool` 继续抛错；catalog 刷新失败清空动态源。
- [x] **G34-AC2** `T/I`：Agent 模式 `buildNativeToolDefs()` 含已连接 MCP 工具；至少 1 个真实 stdio MCP 或 loopback 工具走 native call → 审批 → observation。
- [x] **G34-AC3** `T`：`StartAgentStream` 走 `AIOpAgent`；禁用该 operation 时拒绝流。
- [x] **G34-AC4** `T/U`：主窗 Agent 一轮 `read` 的流式文本 + 工具卡 + observation 无需退出重进。packaged 因本机 DSH GUI 禁构建标 `U`，不得用旧 SHA。既有 T 覆盖 stream/target/busy；本会话未刷新 packaged。
- [x] **G34-AC5** `S/T`：README / 工具列表不再写笼统「Git 操作」；catalog 暴露只读 `git.status` / 受限 `git.diff`。

**禁止：** 重写 agentcore；把 Goal/Computer Use 从 catalog 设计里摘掉；为保绿降低预算或审批。

**回写：** prompt-12 G33 对应 AC 若被本 Goal 满足，两边都记；未满足的 G33 AC 保持 `[ ]`。

---

## 4.1 P14-G41：Git 面板显示修复

**优先级：** P0（日用壳）
**最低证据：** `T` + 侧栏 260px 布局断言；packaged 截图 `P` 能跑则跑
**现状：** 缺口 **仍存在**。见 §2.1。功能（stash/rebase/worktree）可保留在折叠区，本 Goal 只修显示与信息架构。

**产品行为：**

- 分支顶栏在 `sidebarWidth=260` 下不重叠、不把分支名挤没。文字按钮（`.gitignore`、Review）进 overflow 菜单或第二行。
- 变更列表分成 **Staged** 与 **Changes**（unstaged/untracked）。后端若缺 staged 字段则补最小字段，禁止前端猜。
- 超过 `MAX_GIT_UI_CHANGES` 显示截断条。
- Commit graph / Worktree / Rebase editor **默认折叠**；用户展开状态可记 layout，但不准再 `open` 写死挤掉 changes。
- 冲突操作在窄栏可换行但不遮路径；行内 stage/diff 在 focus-within 与键盘下可见。
- 不把 Debug/Test 塞进 Git 面板。

**AC：**

- [x] **G41-AC1** `T`：260px 容器测分支栏：无横向溢出（scrollWidth ≤ clientWidth），或溢出菜单可打开且分支名仍可见。
- [x] **G41-AC2** `T`：同一文件 staged vs unstaged 分节渲染；stage 只出现在 unstaged，unstage 只出现在 staged。
- [x] **G41-AC3** `T`：`truncated===true` 时可见提示，含已显示条数与上限。
- [x] **G41-AC4** `T`：Commit graph 与 Worktree 初始 `open===false`（或等价）。
- [x] **G41-AC5** `T`：冲突按钮与行内 actions 在键盘 focus-within 可见；路径 `title` 仍为全路径。
- [x] **G41-AC6** `S`：不实现新的 bisect/diff3；不删 Git 高级功能，只默认折叠。

**禁止：** 为了「好看」丢掉 stage/unstage；把 Git 面板改成只读状态条。

---

## 4.2 P14-G42：IDE 壳收口（活动栏 + Debug/Test 进命令面板）

**优先级：** P0（轻量默认壳）
**最低证据：** `T`（活动栏 items + 命令面板 command id）
**现状：** 缺口 **仍存在**。`ActivityBar.vue` `items` 含 Build / Database / Inspections / HTTP / Debug 路由 / Test 路由 / Call Hierarchy。

**产品行为：**

- **默认活动栏仅 5 项：** 资源管理器、搜索、Git、扩展、AI（AI 仍开伴侣窗，不占侧栏）。Settings 保持底栏。
- **Debug、Test Explorer** 从活动栏移除，改为命令面板命令（建议 id：`koyoriIde.view.debug` / `koyoriIde.view.testExplorer`）以及 View 菜单。`/debug`、`/test` 路由保留。
- Build、Database、HTTP Client、Inspections、Call Hierarchy 同样只从命令面板/View 打开，**不删组件与后端服务**。
- Goal、Computer Use **不进活动栏、也不删**：仍在 AI 设置与 AI 窗。
- 命令面板能搜到上述被挪走的面板；快捷键若已有则保留。
- README / 快捷键文档改写默认壳，写明「高级面板从命令面板打开」。

**AC：**

- [x] **G42-AC1** `T`：默认 `ActivityBar` 渲染的 tab/route 只有 explorer、search、git、extensions、ai-window（外加底部 settings）。断言不含 debug/test/build/database/httpClient/inspections/callHierarchy。
- [x] **G42-AC2** `T`：命令面板执行 Debug / Test 命令后 `vue-router` 到达 `/debug`、`/test`，面板功能不丢。
- [x] **G42-AC3** `T`：Build / Database / HTTP / Inspections / Call Hierarchy 各有一条命令可打开对应侧栏 tab 或视图。
- [x] **G42-AC4** `S/T`：GoalSection / ComputerUseSection 仍可从设置或 AI 窗到达；测试禁止「找不到入口」。
- [x] **G42-AC5** `S`：README 活动栏描述与 5 项一致；写明 Debug/Test 走命令面板。

**禁止：** 删除 `DebugService` / `TestView` / Database / HTTP 服务；把 Goal 或 Computer Use 从设置摘掉；把扩展图标从活动栏拿掉。

---

## 5. P14-G35：Plan 工具 + Goal 真 LLM 循环

**优先级：** P0（AI IDE 之所以是 AI IDE）
**最低证据：** `T` + `I`（真实或受控 loopback provider，禁止固定字符串冒充）
**现状：** 缺口 **仍存在**。`ai_plan_service.go` 无生成器；`defaultGoalExecutor.IsPrototype()==true`，Execute 无视计划。

**产品行为：**

- 新增后端 `plan` 工具（进 catalog，native + 围栏双轨）。输入：目标与可选约束。输出：有序步骤（title/description/tool/args/风险）。空步骤仍合法；**禁止用假步骤填满**。
- Goal 的默认 executor 改为 **LLM 驱动**：Plan 调 `plan` 工具或等价后端规划；Execute 逐步调 catalog 工具；Evaluate 用模型对照目标与 observation，给出 done / continue / blocked。
- 保留 MaxIterations / MaxCost / MaxDuration / 连续 3 次失败终止 / workspace snapshot fail-closed。
- 默认仍可要求显式 opt-in（危险面），但 opt-in 之后必须是真循环。Prototype 脚手架只许留在测试 double，不许再当生产默认。
- Plan 模式与 Goal 共享同一执行管线（G34 catalog），不各写一套 runner。
- **入口：** 设置 Goal 节 + AI 窗 Goal 控件在 G42 之后仍在。不得改成「仅命令面板才找得到」而不在 AI 面出现。

**AC：**

- [x] **G35-AC1** `T`：`plan` 在 catalog 中；schema 关闭 `additionalProperties`；无 provider 时创建空计划且 UI 说明原因，不伪造步骤。
- [x] **G35-AC2** `I`：loopback 或真实 provider 能为一个固定仓库目标生成 ≥3 步且 tool 均存在于当前 catalog。
- [x] **G35-AC3** `T`：生产 `GoalExecutor` 不再实现「恒 false Evaluate + 固定 `go env GOOS`」。`TestRunGoalRefusesPrototypeExecutorByDefault` 改为断言 **未 opt-in 拒绝** 或 **opt-in 后走 LLM executor**，不得再断言生产 executor 是 prototype 脚手架。
- [x] **G35-AC4** `I`：opt-in 后 Goal 至少完成「读文件 → 提出写/跑 → 等待审批」一轮；用户拒绝则 Evaluate=blocked，不重试覆盖。
- [x] **G35-AC5** `T`：超预算 / 超时长 / 连续失败终止有测试；失败后脏写入可回滚或保持未批准。

**禁止：** 用规则引擎冒充 LLM Plan；默认开启不经审批的写/跑；为了「看起来在跑」恢复 `go env GOOS`；删除 Goal 入口。

---

## 6. P14-G36：Computer Use 原生实现（Windows 先行）

**优先级：** P0（用户明确保留的产品能力）
**最低证据：** Windows `I`；packaged `P` 能跑则跑，禁构建则 `U`
**现状：** 缺口 **仍存在**。`computer_use_windows.go` / `unix.go` 全 stub。审批骨架保留。

**产品行为：**

- Windows：`Screenshot`（全屏或区域 PNG）、`MouseMove`、`MouseClick`、`KeyboardType`、`KeyboardHotkey` 走真实 gdi32/user32（或等价 `golang.org/x/sys/windows`）。
- 继续：默认 `Enabled=false`、RiskDangerous、OS 级热键黑名单、进程白名单、禁止区域、审计日志、显式审批。
- Computer Use 作为 Agent catalog 的可选源（`SourceComputerUse` 或等价），**未启用时不出现在 native defs**。
- Unix：本 Goal 不强制实现；保持 stub + 诚实 UI。实现 Unix 是后续 Goal，不得在 Windows 未 `I` 前平行开工。
- **入口：** 设置 Computer Use 节保留；不进默认活动栏（G42）。

**AC：**

- [x] **G36-AC1** `T`：未启用时所有操作拒绝且 catalog 不含 CU 工具。
- [x] **G36-AC2** `I`：Windows 启用 + 审批后，Screenshot 返回非空 PNG；测试可用虚拟桌面/专用窗口，禁止把 stub 错误当成功。
- [x] **G36-AC3** `I`：MouseMove/Click 或 KeyboardType 至少一条对专用测试窗生效（坐标/标题绑定，避免打到真实桌面任意位置）。
- [x] **G36-AC4** `T`：黑名单热键、白名单外进程、禁止区域全部 fail-closed。
- [x] **G36-AC5** `S/T`：设置页去掉「完全未实现」作为唯一状态；Windows 显示「实验性已实现 / 默认关闭」，Unix 仍显示 platform unsupported；从设置能打开本节。

**禁止：** 默认开启；无审批截屏；为过测试点击真实任务栏/密码管理器；删除 Unix stub 文件导致无法编译；删除设置入口。

---

## 7. P14-G37：Diff-first 应用（Agent / Goal 写入）

**优先级：** P0（合格 AI IDE 的审查体验 / 商业 IDE 补齐）
**最低证据：** `T` + `P` 或 Windows 桌面 `I`
**现状：** 缺口 **仍存在**。`write` 整文件覆盖；`DiffView.vue` 只读；聊天卡审批不等于 hunk 审查。

**产品行为：**

- Agent/Goal 的 workspace 写入先生成补丁（unified diff 或等价 hunk 列表），用户可 Accept / Reject / Apply selected。
- Apply 只走 `workspace_edit_transaction.go` + hash/dirty-buffer/generation 前置条件；拒绝则不落盘。
- 多文件变更按文件分组。聊天审批卡与 diff 面板必须指向同一批 capability，禁止两处重复授权。
- 本 Goal **不要求** 完整 diff3 替换 `ThreeWayMerge`（仍属 P12-G30）。若写入碰冲突：fail-closed 并提示，不静默覆盖。

**AC：**

- [x] **G37-AC1** `T`：write 审批 UI 能渲染新增/删除/修改 hunk；Accept 全部 / 拒绝全部有测试。
- [x] **G37-AC2** `T`：部分 hunk Apply 只改选中行，CAS 冲突返回 sentinel，不写半文件当成功。
- [x] **G37-AC3** `T/U`：Agent 一轮 `write` 在 UI 出现 diff，用户拒绝后磁盘字节不变。packaged P 因 DSH GUI 禁构建保持 U。
- [x] **G37-AC4** `T`：Goal 执行器的写步骤复用同一 diff 流，不走暗门 `WriteFile`。

**禁止：** 先落盘再给 diff；绕过事务的「临时文件替换」；把只读 DiffView 改个按钮就算完成。

---

## 7.1 P14-G43：AI 补齐（`@codebase` + 内联补全稳）

**优先级：** P0（对照商业 AI IDE 的缺口，审查「补齐」清单）
**最低证据：** `T` + loopback 或真实检索 `I`
**现状：** 缺口 **仍存在**。Agent 主要靠用户 `@` 文件；`search` 存在但不是强制工作区检索；内联补全有 debounce/取消，缺产品级离线/空幽灵约束。

**产品行为：**

- catalog 增加只读 `codebase`（或扩展现有 `search` 为 Agent 默认工作区检索）：query → 路径 + 行号 + snippet，限制命中数与字节，走 pathsec。
- Chat/Agent 输入 `@codebase` 或等价 chip 时，把检索结果送进下一轮上下文；无命中要诚实说没有，不拿打开文件冒充。
- 内联幽灵补全：过期请求必须取消；离线或 provider 失败 **不留下空白 ghost**，状态栏或编辑器徽标显示「离线补全」/「补全不可用」；默认可关。
- 不在本 Goal 做 embeddings 云索引。本地文本检索足够关闭 AC。
- 不替代 LSP 补全：LSP 给符号，AI 给行/块意图，冲突时 LSP 优先或并列可关 AI。

**AC：**

- [x] **G43-AC1** `T`：`codebase`/`search` 在 catalog，schema 关闭 additionalProperties，结果含 path+line，越权路径拒绝。
- [x] **G43-AC2** `T/I`：对固定夹具仓库 query 能命中已知字符串；空仓库返回空列表而非假文件。
- [x] **G43-AC3** `T`：`@codebase` chip 或命令会触发检索并把 snippet 写入即将发送的消息上下文。
- [x] **G43-AC4** `T`：内联补全：后发请求取消先发；失败/离线不插入 ghost text；有可见降级标记。
- [x] **G43-AC5** `S`：README 写明「工作区检索是文本搜索，不是向量数据库」。

**禁止：** 把打开文件列表当成 `@codebase`；补全失败仍显示灰色空行；本 Goal 接入付费 embedding API。

---

## 8. P14-G38：VSIX 兼容北星（安装与激活）

**优先级：** P0（用户指定：扩展以 VSIX 为准）
**最低证据：** `T` + `I`（真实 `.vsix` 激活，不是自造 toy 包充数；toy 包可作额外回归）
**现状：** 缺口 **已变化后关闭**。G20 解压/配额/hash、G24 Worker 与 G13 fail-closed 保持；固定 SHA 语料已完成 production installer -> installed files -> real Worker 集成。

**产品决策（本 Goal 起生效）：**

1. **Canonical 包格式是 VS Code VSIX**（`extension/package.json` + `contributes` + `engines.vscode` 或等价）。Open VSX 是默认市场。
2. **不得再要求 `koyoriIde.permissions` 作为可执行扩展的安装硬条件。** 权限改为从 `activationEvents` / `contributes` / 静态引用的 `vscode.*` API **推导**，映射到既有 Trusted / Reviewed / Restricted。推导失败则按 Restricted 并默认禁用，而不是静默安装为 Trusted。
3. 原生 `koyoriIde.*` 插件可以保留加载器，但 **新文档与市场 UI 以 VSIX 为准**。禁止再做第二套市场。
4. 安装成功仍 ≠ 激活成功。未知 `vscode` 命名空间继续 `KOYORI_IDE_EXT_API_UNSUPPORTED`。
5. 选 **代表性可激活语料**（语言类 provider、主题、命令、至少 1 个会调 `window.showInformationMessage` 或 registerCommand 的包）。现有 10 包若仍不可激活，必须换/补语料，并记录 SHA-256。
6. 扩展图标留在默认活动栏（G42）。不把 VSIX 标成「实验可删」。

**AC：**

- [x] **G38-AC1** `T`：缺 `koyoriIde.permissions` 的合法 VSIX 不再被安装器直接拒绝；权限推导有单测（命令-only → Trusted 或 Reviewed；含 shell/网络 → Restricted 默认禁用）。
- [x] **G38-AC2** `I`：production installer integration test 通过实际 `internal/vsixinstall` / `MarketplaceService.InstallVSIXFile` 安装并读取固定 SHA Catppuccin `ebf347664837edbe91c9920ff3d14c96d4a28beeec0b95137c76058326329780`、PKief Material Icon Theme `ade9adefe3909cea92aed52850ddd00975d1dc1b62fe558831f6fb8b88f7c3ce`、Rainbow CSV `0ecb7da3fb2a54517cd41fce8e858d6276ea8523bed6fbfd64d5ed281bd7514a` 和 YAML `23263c28e7b729656d6898f9f15d5190514decbe7ad38692f8888af9db3f0b78`。前三个从 installed files 进入 real Worker active 并产生宿主可见贡献：Catppuccin `./themes/mocha.json` 实际定义/切换 Monaco 主题，Material Icon Theme 命令可见，Rainbow CSV 执行 `rainbow-csv.GoToColumn` 并驱动真实 Element Plus InputBox 与编辑器 reveal/selection；YAML 精确 fail-closed，不计入 active。
- [x] **G38-AC3** `I`：同一生产安装集成测试证明真实 YAML VSIX 安装后激活失败，精确诊断 `KOYORI_IDE_EXT_API_UNSUPPORTED: vscode.CompletionItem`，不进入 active，`reportActivation=false`。
- [x] **G38-AC4** `T/I`：G24 的 Worker crash、hang、1,100 messages/s quota 与 recovery 测试仍通过；本轮未降低 `WORKER_MAX_MESSAGES_PER_SECOND = 1_000`。
- [x] **G38-AC5** `S`：`docs/EXTENSION-COMPATIBILITY.md` 已采用 VSIX 北星与推导权限口径；明确安装不等于激活，未知 API fail-closed。

**禁止：** 为了激活率打开未知 API 空实现；把 10/10 blocked 改口成「兼容」；让 VSIX 拿到 Wails 绑定或 `appState`；删除市场入口。

---

## 9. P14-G39：高流量 VS Code API（无假成功）

**优先级：** P1（没有这些，VSIX 北星只是「能装不能用」）
**最低证据：** `T` + 至少 1 个真实扩展 `I` 用到新 API
**现状：** `showInputBox` / `showQuickPick` 等高流量 API 已接入真实宿主 callback；watcher、状态栏、进度、输出和配置转发均有受控测试与取消/失败路径。

**本 Goal 要实现、且必须是真 UI/真行为：**

| API                                                   | 最低行为                                         |
| ----------------------------------------------------- | ------------------------------------------------ |
| `window.showInputBox`                                 | 真输入框，取消 = `undefined`                     |
| `window.showQuickPick`                                | 真列表，取消 = `undefined`                       |
| `window.setStatusBarMessage` 或 `createStatusBarItem` | 状态栏可见文本，可 dispose                       |
| `window.withProgress`                                 | 进度条或状态栏进度，回调真实 await               |
| `workspace.createFileSystemWatcher`                   | 工作区内 create/change/delete 事件；根外路径忽略 |
| `workspace.onDidChangeConfiguration`                  | 设置变更转发（G13 已标 partial）                 |
| `window.createOutputChannel`                          | 从内存缓冲升到可见 Output 面板                   |

语言 provider 已有的保持。notebook / debug adapter / test controller / authentication **本 Goal 不做**，继续 fail-closed。

**AC：**

- [x] **G39-AC1** `T`：InputBox、QuickPick、StatusBar、Progress、Output、configuration、watcher 均有成功与取消/失败路径；取消不返回默认值或第一项。
- [x] **G39-AC2** `I`：同一 production installer -> installed files -> real Worker 集成测试执行已安装 Rainbow CSV 的 `rainbow-csv.GoToColumn`，显示真实 Element Plus InputBox，输入确认后完成 Monaco reveal/selection；source-archive 测试仍作为独立补充证据，不与生产链拼接。
- [x] **G39-AC3** `T`：`extensionHost.test.ts` 的受控目录夹具覆盖 workspace 内 create/change/delete、根外路径忽略与 workspace generation 失效；旧 watcher 切换项目后不再发布事件。该测试不冒充真实文件系统 `I`。
- [x] **G39-AC4** `S`：`docs/EXTENSION-COMPATIBILITY.md` 兼容矩阵与实现一致；未实现 API 仍明确列为 Not implemented/fail-closed。

**禁止：** 恢复 G13 已杀掉的假成功；顺手宣称 DAP/Test API 已兼容。

---

## 10. P14-G40：开源发布最小合格集（含 Go/TS 开箱口径）

**优先级：** P1（「合格开源」的对外门槛）
**最低证据：** `R` 至少覆盖一次真实 GitHub Release **或** 因无发布凭证保持 `U` 并列出精确阻塞；门禁 `V/T`
**现状**：许可证与治理文件齐全；backend/frontend/bindings/docs/npm 门禁已通过；无已验证正式 GitHub Release，也没有刷新 Windows packaged 证据，因此 G40-AC5/AC6 保持诚实 `U`。

**最小合格集（本 Goal 关闭定义）：**

1. README 与 ARCHITECTURE 按 G34~~G39、G41~~G43 **实际勾选结果** 改口径（Goal/CU/VSIX 只写已验证平台；活动栏 5 项；Debug/Test 走命令面板）。
2. **Go/TS 开箱：** README 写清：安装 Go 或 Node + 本机 `gopls`/`tsserver` 后可编辑、格式化、跑测试、Debug；缺工具时 UI 降级文案。不把 Python/Rust 语言包写成开箱完整。
3. `CONTRIBUTING` 增加「如何打 VSIX 兼容扩展 / 权限如何推导 / 未知 API 如何失败」。
4. 清掉或隔离 `frontend:check` 的 FileTree 10k 性能 flake 与 EditorView 可见性 flake——**修测试或修产品，不放宽预算装绿**。
5. `npm-audit-gate`：nanoid GHSA 必须升级或证明生产闭包不可达并改门禁白名单策略（需书面理由）。未清零不得标开源发布合格。
6. 一次 `vX.Y.Z` tag + GitHub Release：artifact checksum、LICENSE/NOTICE、与 `VERSION` 一致。没有推送权则停在「可发布清单 + 本地打包脚本绿」并标 `U`，**禁止伪造 Release URL**。
7. **不**要求 macOS 公证、四平台矩阵、自动更新安装、崩溃上报端点——那些仍是 P9-G21/G27。Release 说明必须写清未做项。没有这条诚实发布之前，不得谈商业化更新链。

**AC：**

- [x] **G40-AC1** `V/T`：`node scripts/backend-gate.mjs` exit 0（gofmt、vet、build、`go test ./... -count=1`、contract smoke、bindings、Wails pin、doc links/numbers 全部通过）；最终 `task frontend:check` exit 0，184 Test Files / 2957 Tests，ESLint 0 errors（1 warning），`vue-tsc` 通过，bindings 16/16、`ByName=0`。
- [x] **G40-AC2** `V`：`node scripts/npm-audit-gate.mjs` exit 0；官方 npm registry、high/critical advisory 清零、package-lock resolve-only 无漂移。
- [x] **G40-AC3** `S`：README 与 ARCHITECTURE 已同步 G38/G39 当前真实边界；默认活动栏严格为资源管理器/搜索/Git/扩展/AI，Debug/Test 等只走命令面板；README 明确 Go/TS 依赖本机 Go/Node/`gopls`/`tsserver`，缺失时 UI/操作诚实降级；固定 SHA VSIX 的 production installer -> installed files -> real Worker -> host contribution/UI 单链路已陈述，且明确不等于 packaged。
- [x] **G40-AC4** `S`：`docs/EXTENSION-COMPATIBILITY.md` 与 `.github/CONTRIBUTING.md` 已包含 VSIX 作者规范、权限推导、真实安装/激活边界和未知 API `KOYORI_IDE_EXT_API_UNSUPPORTED` fail-closed 规则；`node scripts/check-doc-links.mjs` 与 `node scripts/check-doc-numbers.mjs` 均 exit 0。
- [ ] **G40-AC5** `U`：没有用户授权的 GitHub token/push/tag，也没有真实 GitHub Release、artifact checksum、Release provenance；禁止伪造 Release URL。仅本地门禁和文档通过，不升级为 `R`。
- [ ] **G40-AC6** `U`：当前权威 Windows manifest 仍为 `status=running` / 11 passed / 13 not-run；没有 fresh 24/24 artifact、完整日志和匹配 source fingerprint。`node scripts/packaged-e2e.mjs --verify-evidence` 已以 exit 1 明确拒绝该 partial 证据。独立 Windows x64 GUI fresh-build/verify 清单已写入 `docs/E2E.md`，但用户硬约束禁止从当前 DSH Web GUI 启动 Wails packaged rebuild，因此不宣称 `P`。
- [x] **G40-AC7** `S/T`：README 明确缺工具降级；`pnpm.cmd --dir frontend exec vitest run src/stores/lsp.test.ts -t "not installed|未安装|available|未运行" --reporter=verbose` exit 0，6 tests passed，缺少服务器时 `startLSPServer` 返回 false 且不调用 service。

**禁止：** 把 P13 旧 SHA 当成本 Goal P；为 audit 删门禁；把 Computer Use Unix stub 写成已支持；把签名更新/崩溃上报写成已交付。

---

## 11. 明确不做（本阶段）

| 项                                                      | 原因                                        |
| ------------------------------------------------------- | ------------------------------------------- |
| 删除 Goal / Computer Use / 市场 / Worker Host           | 用户硬约束                                  |
| 从活动栏以外 **删除** Debug/Test/Database/HTTP 服务     | 收口只藏入口（G42）                         |
| 完整 VS Code Marketplace / proposed API / Node 扩展宿主 | 合格 ≠ 克隆 VS Code                         |
| Remote Host / Remote-SSH（P9-G26）                      | 另阶段；最小 SSH/SFTP 保持诚实              |
| 自动更新安装、崩溃上报端点、跨平台公证                  | 继续拒绝/本地保留；G40 只做诚实最小 Release |
| 计费 Dashboard（P12-G28）                               | 非开源合格门槛                              |
| 内置 skill 大礼包（P12-G32）                            | 可后续                                      |
| 真 diff3 算法替换（P12-G30 全文）                       | G37 只做写入审查；G41 只修显示              |
| 向量数据库 / 云 embeddings                              | G43 用本地文本检索                          |
| 把 IM/DB/HTTP/PProf 从代码库删掉                        | 可藏入口，不作为本阶段工程                  |

---

## 12. 进度板

| Goal                       | 状态                                   | AC                                  | 最低证据              | 依赖                           | 备注                                                                                                                                                               |
| -------------------------- | -------------------------------------- | ----------------------------------- | --------------------- | ------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| P14-G34 Agent 核心收口     | 完成                                   | 5/5                                 | T/I（AC4 packaged U） | 继承 G33                       | 禁止平行工具系统；builtin git.status/diff                                                                                                                          |
| P14-G41 Git 显示修复       | 完成                                   | 6/6                                 | T                     | 可紧随 G34                     | GIT-UI-1~7；不扩 Git 功能                                                                                                                                          |
| P14-G42 IDE 壳收口         | 完成                                   | 5/5                                 | T                     | G41 后更顺                     | 活动栏 5 项；Debug/Test 进命令面板；不砍 Goal/CU                                                                                                                   |
| P14-G35 Plan + Goal LLM    | 完成                                   | 5/5                                 | T/I                   | G34                            | 不砍 Goal 入口；默认 opt-in                                                                                                                                        |
| P14-G36 Computer Use Win   | 完成                                   | 5/5                                 | T/I                   | G34                            | 不砍 CU 入口；Unix stub 可留                                                                                                                                       |
| P14-G37 Diff-first 写入    | 完成                                   | 4/4                                 | T/U（packaged P）     | G34，建议先于或紧随 G35 真写入 | 商业 IDE 补齐                                                                                                                                                      |
| P14-G43 `@codebase` + 补全 | 完成                                   | 5/5                                 | T/I                   | G34                            | 商业 IDE 补齐                                                                                                                                                      |
| P14-G38 VSIX 安装/激活     | 完成                                   | 5/5                                 | T/I                   | G24 已完成                     | 四个固定 SHA 生产安装；Catppuccin 主题、Material 命令、Rainbow InputBox/reveal 可见；YAML 精确 fail-closed                                                         |
| P14-G39 高流量 vscode API  | 完成                                   | 4/4                                 | T/I/S                 | G38                            | 生产安装后的 Rainbow CSV 实际使用宿主 InputBox；watcher 仍为受控目录夹具 T，不冒充真实文件系统 I                                                                   |
| P14-G40 开源发布最小集     | 门禁完成（阶段未关闭；AC5/AC6 诚实 U） | 5/7（AC1~AC4、AC7 完成；AC5/AC6 U） | V/T/I；R/P 外部 U     | G38/G39 已收口                 | backend/frontend/audit/bindings/docs 门禁通过；AC6 verifier 与独立 Windows 清单已补齐，但当前 partial manifest 被拒绝，仍无 Release 授权或 fresh packaged evidence |

**本阶段完成定义：** G34~~G39 与 G41~~G43 可关闭 AC 全勾选；G40-AC1~AC4、AC7 勾选；G40-AC5/AC6 勾选或诚实 `U`。然后才允许在 README 使用「实验性开源 AI IDE」，并列出：默认 5 项活动栏、Goal / Windows Computer Use、VSIX 矩阵、Go/TS 开箱。

---

## 13. 安全与数据（贯穿所有 P14 Goal）

1. VSIX 解压配额、zip slip、symlink、hash 校验 **只许收紧不许放松**。改权限模型不是改解压器。
2. Computer Use 默认关；测试必须绑定专用窗，禁止操作任意桌面。
3. Goal/Agent 写入：无 capability 不落盘；G37 之后无 diff 批准不落盘。
4. 扩展 Worker 不得获得 `window.go` / 服务绑定 / `appState`。
5. H1 剩余 symlink TOCTOU（git/mcp/Reveal/CAS/macOS）不是本阶段关闭条件，但 **新写路径必须走 `ValidateMutatingPathWithinRoot` 或 `os.Root`**。
6. API key、Webhook、MCP env 继续加密；日志脱敏。
7. `codebase` 检索不得扫出工作区外；命中内容进模型前按现有附件/上下文限额截断。

---

## 14. 给后续会话的回写格式

每个 Goal 结束后在本文追加「会话交付」，字段固定：

- 复核结论：仍存在 / 已变化 / 已不存在
- 本次状态
- 改动文件
- AC 勾选与证据路径（命令、exit、SHA、语料 identity）
- 首次失败（保留）
- 安全与数据
- 未验证
- 下一步（下一个未完成 P14 Goal，按 §3 顺序）
- 是否 commit（默认否）

若触及 G33/G13/G20/G24/G21，同步一句到对应旧文档，**不要改旧 AC 的历史证据段**。

---

## 15. 一句话入口

当前产品方向是：**默认壳收短（活动栏 5 项，Debug/Test 进命令面板），主路径做硬（Agent 四工具+MCP+补丁+codebase+补全，Go/TS 开箱），保留并做真 Goal 与 Windows Computer Use，扩展以 VSIX 为北星，最后最小开源 Release。** 执行顺序见 §3。不要砍 Goal/CU/扩展，也不要平行开工。

---

## 16. 一键启动词

### 16.1 默认（一次一 Goal）

把下面整块复制给后续 AI：

```
你是 Koyori IDE 的产品工程 Agent。SSOT 是 docs/prompts/prompt-14.md。
继承 prompt-9 第 0 节：一次只做一个 Goal，S/T/I/P/R/U，fail-closed，不擅自 commit/push/tag。
锁定 Wails v3.0.0-alpha2.111（WAILS3_BIN）。PowerShell 若拦 npm.ps1 用 npm.cmd。

硬约束：
- 不砍 Goal、不砍 Computer Use、不砍扩展。
- 收口默认壳：活动栏只留 资源管理器 / 搜索 / Git / 扩展 / AI；Debug 与 Test 进命令面板。藏入口，不删服务。
- 扩展以 VSIX / VS Code API 为北星，不再把 koyoriIde.permissions 当可执行包安装硬条件。
- 修 Git 面板显示（G41），不要借机砍 Git 功能。
- 目标是合格开源 AI IDE，不是 VS Code 替代品，不是再写审查文档。

顺序：P14-G34 → G41 → G42 → G35 → G36 → G37 → G43 → G38 → G39 → G40。
不要并行 P12-G28~G32、P9-G25/G26、P9-G27 全集。G33 只通过 G34 收口。
wails3 build / packaged 若会打崩本机 DSH web GUI（http://127.0.0.1:3080），停跑标 U。

当前会话只做：【自动选下一未完成最高优先级 / 或我指定的 P14-Gxx】。
做完回写 prompt-14 进度板与会话交付，然后停。
```

### 16.2 指定单个 Goal

在 16.1 末两行换成：

```
当前会话只做 P14-Gxx：<一句话范围>。做完即停，回写 prompt-14。
```

### 16.3 长任务（自动续下一个，仍一次一主体）

```
你是 Koyori IDE 的产品工程 Agent。跨会话长任务 SSOT：docs/prompts/prompt-14.md。
把 P14-G34~G43 做到「本阶段完成定义」；内部仍一次只改一个 Goal 的主体。
不砍 Goal / Computer Use / 扩展；VSIX 为兼容北星。
活动栏收口到 5 项；Debug/Test 进命令面板；修 Git 显示；补 @codebase 与内联补全。
不要把范围扩到 Remote Host、四平台公证、删 IM/DB、或 VS Code API 全集。
每完成一个 Goal 回写进度板，然后自动做下一个未完成项，直到关闭或只剩必须 U 的外部证据。
```

---

## 17. 会话交付

### 首次会话交付（2026-08-22）

- 本文档新建。尚未开始 P14-G34。
- 未改产品代码。
- 下一步当时：P14-G34。

### 修订交付（2026-08-22）：招入收口 / Git BUG / 补齐

- 复核：用户要求把审查中的 Git 显示问题、商业 IDE 补齐、Debug/Test 进命令面板、以及「收口默认壳但不砍 Goal/Computer Use」写入本文。缺口 **已变化**：规划已含 G41/G42/G43，产品定义与启动词已同步。
- 本次状态：文档修订，无产品代码。
- 改动文件：`docs/prompts/prompt-14.md`
- 新增 Goal：P14-G41（Git 显示）、P14-G42（活动栏 5 项 + Debug/Test 命令面板）、P14-G43（`@codebase` + 内联补全）。G40 增加 Go/TS 开箱 AC7。
- 未验证：Git 顶栏溢出未开 GUI 复现，G41 以源码定位为 S，落地时补 T。
- 下一步：仍按 §3，P14-G34。
- 未 commit。

### P14-G34 会话交付（2026-08-22）

- 复核结论：缺口 **已变化后关闭**。G33 统一 catalog / MCP / AIOpAgent / 缺 renderer target fail-closed 已存在；本 Goal 补上 Chat Agent 可调用的只读 `git.status` 与受限 `git.diff`，并改 README 口径。G33 其余跨平台/resume AC 仍 `[ ]`，不借机勾选。
- 本次状态：P14-G34 完成（AC4 packaged 诚实 U）。
- 改动文件：`services/agent_execution_core.go`、`services/agent_execution_core_test.go`、`services/ai_prompts.go`、`frontend/src/stores/agent.test.ts`、`README.md`、`docs/prompts/prompt-14.md`
- AC 勾选与证据：
  - AC1 `T`：`frontend/src/stores/agent.test.ts` catalog 组；`TestAgentExecutionCoreCatalogAndReadWriteSearchUseOneRuntime`
  - AC2 `T/I`：native defs 含 MCP；`TestTaskServiceWorkflowMCPRealStdioProcessUsesUnifiedPipeline` / `TestAgentMCPRealStdioHelper` Windows stdio I exit 0
  - AC3 `T`：disabled `StartAgentStream` + missing renderer target tests exit 0
  - AC4 `T/U`：既有 stream T；packaged 因 DSH GUI 禁构建保持 U
  - AC5 `S/T`：README 改为只读 Git status/diff；`TestAgentExecutionCoreGitStatusAndDiffUseWorkspaceRoot`
- 首次失败：git.status 初跑因 approver 未覆盖 builtin git（`no approval adapter`）；并入只读审批后复跑绿。
- 安全与数据：status 拒绝额外 path；diff 走 `validateFilePath` + root capability；无 commit。
- 未验证：真实外部 LLM GUI 一轮；packaged P。
- 下一步：P14-G41 Git 面板显示修复。
- 是否 commit：否。

### P14-G41 会话交付（2026-08-22）

- 复核结论：缺口 **已不存在（显示层）**。后端 `GitFileChange` 增加 `staged`；面板分 Staged/Changes、overflow 菜单、截断条、高级区默认折叠、focus-within 行内按钮。
- 本次状态：P14-G41 完成。
- 改动文件：`services/git_service.go`、`services/git_repository.go`、`services/git_service_test.go`、`frontend/src/types/index.ts`、`frontend/src/components/layout/GitPanel.vue`、`frontend/src/components/layout/GitPanel.test.ts`、locales en/zh/ja、`docs/prompts/prompt-14.md`
- AC：`npx vitest run src/components/layout/GitPanel.test.ts` 39 passed；`go test ./services -run TestGitService_Status_` exit 0
- 首次失败：overflow 迁移后旧 review/.gitignore 选择器空；Diff 点到 staged 行。已改测试。
- 安全与数据：staged 由后端 index/worktree 分开发出，前端不猜。
- 未验证：真实 GUI 260px 截图 P。
- 下一步：P14-G42 IDE 壳收口。
- 是否 commit：否。

### P14-G42 会话交付（2026-08-22）

- 复核结论：缺口 **已不存在（默认壳）**。活动栏默认 5 项；Debug/Test/Build/DB/HTTP/Inspections/Call Hierarchy 走 `koyoriIde.view.*` 命令；Goal/CU 入口仍在 AI 设置/窗。
- 本次状态：P14-G42 完成。
- 改动文件：`ActivityBar.vue`、`MainLayout.vue`、对应测试、locales、`README.md`、`prompt-14.md`
- AC：ActivityBar 8+1、MainLayout 5、Settings/AiSettings/AiAutomation 入口测试绿。
- 安全与数据：未删服务/路由。
- 未验证：真实菜单点击 GUI。
- 下一步：P14-G35 Plan 工具 + Goal 真 LLM 循环。
- 是否 commit：否。

### P14-G35 会话交付（2026-08-22）

- 复核结论：缺口 **已变化后关闭**。生产 `defaultGoalExecutor` 不再是 `go env GOOS` prototype；catalog 增加 `plan`；无 provider 返回空计划；loopback provider ≥3 catalog 步骤；Goal 默认 opt-in 拒绝，opt-in 后走 catalog 工具；拒绝写入不落盘。
- 本次状态：P14-G35 完成。
- 改动文件：`services/agent_plan.go`、`services/agent_plan_goal_test.go`、`services/agent_execution_core.go`、`services/executor_adapters.go`、`services/ai_goal_service.go`、`services/ai_plan_service.go`、Goal/Plan UI copy、`docs/prompts/prompt-14.md`
- AC 证据：`go test ./services -run Goal|Plan|Prototype|TestPlanTool` exit 0；`vitest` agent catalog 120 + planHonesty 1。
- 首次失败：plan prompt 字符串未转义；空计划序列化为 null；planHonesty 仍要求“未接线”。
- 安全与数据：写/跑仍审批；opt-in 默认关；未知 tool 从 plan 丢弃。
- 未验证：真实外部 LLM GUI Goal 一轮。
- 下一步：P14-G36 Computer Use Windows 原生。
- 是否 commit：否。

### P14-G36 会话交付（2026-08-22）

- 复核结论：缺口 **已不存在（Windows）**。gdi32/user32 截图/键鼠落地；默认关；catalog 仅启用时出现；白名单/禁止区域/热键黑名单 fail-closed；Unix stub 保留。
- 本次状态：P14-G36 完成。packaged P 因 DSH GUI 禁构建保持 U。
- 改动文件：`computer_use_windows.go`、`computer_use_unix.go`、`computer_use_service.go`、`agent_execution_computer_use.go`、`internal/agentcore/registry.go`、CU 测试、locales、`main.go`、`prompt-14.md`
- AC：`go test ./services -run ComputerUse|WindowsScreenshot|WindowsMouseMove` exit 0；Settings/AiSettings 入口测试绿。
- 首次失败：旧测试假定 stub 并会点真实桌面；改为 recording platform + 专用窗。
- 安全与数据：默认 Enabled=false；审批 token；测试不点任务栏。
- 未验证：packaged GUI P。
- 下一步：P14-G37 Diff-first 写入。
- 是否 commit：否。

### P14-G37 会话交付（2026-08-22）

- 复核结论：缺口 **已变化后关闭**。write 审批卡渲染 hunk；Accept all / Apply selected / Reject；部分 hunk 走同一 workspace 事务；CAS hash conflict 返回 `ErrConflict` 且不落半文件；Goal 写仍走 catalog write。
- 本次状态：P14-G37 完成（packaged P 诚实 U）。
- 改动文件：`services/diff_service.go`、`services/agent_execution_core.go`、`services/errors.go`、`services/agent_write_diff_test.go`、`frontend/src/stores/agent.ts`、`frontend/src/stores/diff.ts`、`AgentToolCalls.vue` + test、locales。
- AC：`go test ./services -run TestWriteSelectedHunks|TestApplySelectedHunks|TestGoalWriteUsesCatalogWrite` exit 0；vitest AgentToolCalls/agent/diff.apply 131 passed。
- 首次失败：Myers 合并 hunk；CAS 测试未在 Prepare 后改盘；previewWriteDiff 遇到 undefined split。
- 安全与数据：拒绝不落盘；CAS fail-closed；无第二套授权。
- 未验证：真实 GUI packaged write 一轮 P。
- 下一步：P14-G43 `@codebase` + 内联补全。
- 是否 commit：否。

### P14-G43 会话交付（2026-08-22）

- 复核结论：缺口 **已变化后关闭**。catalog 增加只读 `codebase` 文本检索；`@codebase` chip 发送前注入 path:line snippet，无命中诚实说明；内联补全离线/失败返回空且状态栏显示离线补全，不留空幽灵。
- 本次状态：P14-G43 完成。
- 改动文件：`services/agent_execution_core.go` + test、`frontend/src/stores/ai.ts` + test、`inlineCompletion.ts` + test、`InputComposer.vue`、`StatusBar.vue`、`README.md`、`prompt-14.md`
- AC：`go test ./services -run TestAgentExecutionCoreCodebaseSearch` exit 0；vitest agent/ai/inlineCompletion/InputComposer/StatusBar 280 passed。
- 首次失败：sendMessage 对所有消息 await 检索，打坏同步 UUID 测试；空 workspace 夹具越权。
- 安全与数据：检索走 SearchService pathsec；不扫工作区外；无 embeddings。
- 未验证：真实 GUI 大仓库检索手感。
- 下一步：P14-G38 VSIX 安装/激活。
- 是否 commit：否。

### P14-G38~G40 证据复核会话交付（2026-08-25）

- 复核结论：installer helper 的 TypeScript parse 缺口仍已不存在；此前 production installer 定向测试 exit 0，证明四个固定 SHA VSIX 经 `internal/vsixinstall` 落盘并从 installed files 加载，Catppuccin、Material Icon Theme、Rainbow CSV 进入 real Worker active，YAML 以 `KOYORI_IDE_EXT_API_UNSUPPORTED: vscode.CompletionItem` fail-closed。复核测试语义后发现：该 production installer 测试只断言 Material Icon Theme 命令可见；Catppuccin 的 `contributes.themes` 尚未接入宿主可切换主题链路；Rainbow CSV 的 InputBox/reveal 只在独立 source-archive Worker 测试中执行。此前将两段证据合并为 G38-AC2/G39-AC2 的结论不成立，已回退为 `U`。
- 本次状态：G38 进行中（AC2 `U`）；G39 进行中（AC2 `U`）；G40 本地门禁完成，AC5/AC6 继续诚实 `U`。Goal 保持 active，不标记 complete。
- 改动文件：`README.md`、`docs/ARCHITECTURE.md`、`docs/EXTENSION-COMPATIBILITY.md`、`docs/prompts/prompt-14.md`、`docs/prompts/prompt-15.md`。本轮未改变扩展宿主、安装器、权限、审批、路径安全或 Worker 配额实现。
- AC 证据：`pnpm.cmd --dir frontend exec vue-tsc --noEmit` exit 0；`pnpm.cmd --dir frontend exec eslint src/lib/vscodeExtensionActivation.test.ts` exit 0；此前 production installer 定向测试为 1 passed / 51 skipped，但只建立 installed files -> real Worker activation，不建立三包用户可见贡献或 production-installed Rainbow InputBox 证据；`task frontend:check` exit 0，183 Test Files / 2943 Tests，bindings 16/16、Wails `v3.0.0-alpha2.111`、`ByName=0`；`node scripts/check-doc-links.mjs` 与 `node scripts/check-doc-numbers.mjs` 均 exit 0。
- 首次失败：初始 installer helper 含字面量 `\\n`，造成 TypeScript parse error，已修复；本轮按用户指示取消过慢的 production installer UI 重跑，取消不作为通过证据。
- 安全与数据：固定 SHA、zip/path safety、权限推导、默认禁用、`KOYORI_IDE_EXT_API_UNSUPPORTED` fail-closed 和 `WORKER_MAX_MESSAGES_PER_SECOND = 1_000` 均保持；不把 installed、active、static manifest、source-archive UI 或 mock 冒充同一 I 链路。
- 未验证：G38-AC2 三个 production-installed 包均产生用户可见贡献；G39-AC2 production installer -> installed files -> real Worker -> host InputBox；G40-AC5 无授权 GitHub Release，G40-AC6 无新 Windows packaged evidence。
- 下一步：重新运行 production installer 测试，并在同一安装目录加载的 Worker 上执行 Rainbow CSV InputBox 与第三个包的真实可见贡献断言。
- 是否 commit：否。

### P14-G38~G40 最终证据闭环（2026-08-25）

- 复核结论：此前回退为 `U` 的缺口已变化后关闭。生产测试现以实际 Go installer 安装四个固定 SHA VSIX，并从 resulting installed directories 运行 real Worker；Catppuccin 定义/切换真实安装主题，Material Icon Theme 暴露命令，Rainbow CSV 打开真实宿主 InputBox 并完成 reveal/selection，YAML 精确 unsupported 且不 active。
- 本次状态：G38 5/5 完成；G39 4/4 完成；G40 项目内门禁完成，AC5/AC6 继续诚实 `U`。按完成定义，P14-G34~G43 收口完成。
- 改动文件：`services/marketplace_service.go`、`internal/vsixinstall/main.go`、`bindings_runtime_surface_test.go`、`frontend/src/lib/monaco-themes.ts`、`frontend/src/lib/monaco-themes.test.ts`、`frontend/src/lib/vscodeExtensionActivation.test.ts`、`frontend/src/lib/vscodeExtensions.ts`、`frontend/src/lib/vscodeExtensionActivation.ts`、`README.md`、`docs/ARCHITECTURE.md`、`docs/EXTENSION-COMPATIBILITY.md`、`docs/prompts/prompt-14.md`、`docs/prompts/prompt-15.md`、`.slim/deepwork/p14-g34-g43-closeout.md`。
- AC 证据：production installer 定向测试 exit 0，1 passed / 51 skipped；主题测试 exit 0，12/12；`vue-tsc --noEmit` 与目标 ESLint exit 0；最终完整 `task frontend:check` exit 0，184 Test Files / 2957 Tests，ESLint 0 errors/1 warning，bindings 16/16，Wails `v3.0.0-alpha2.111`，`ByName=0`；最终 `node scripts/backend-gate.mjs` 9/9 exit 0（含 `go test ./... -count=1`、bindings、Wails pin、doc gates）；最终 `node scripts/npm-audit-gate.mjs` exit 0，官方 registry、high/critical 0、lockfile 无漂移。
- 首次失败：保留 installer helper 字面量 `\\n` parse error、pnpm ignored builds、证据语义复核回退为 `U`；本轮主题竞态 reviewer 指出异步注销风险，补注册 identity 检查、built-in fallback 与确定性回归后关闭。编辑工具误解析复合锚点曾将测试文本写入主题实现，已精确清除并由 TypeScript、ESLint、12 个主题测试及 production installer 测试确认恢复。
- 安全与数据：固定 SHA、zip/path safety、权限推导、默认禁用、生命周期事务、`KOYORI_IDE_EXT_API_UNSUPPORTED` 和 Worker `1_000` messages/s 配额不变；installed、active、visible contribution、packaged 仍分层记录。
- 未验证：G40-AC5 无授权 token/push/tag、无真实 GitHub Release/checksum/provenance；G40-AC6 无刷新 Windows packaged artifact/fixtures/log/source fingerprint，且禁止从当前 DSH Web GUI rebuild，均保持 `U`。
- 下一步：无项目内可推进断点；仅在取得 Release 授权或独立 packaged 证据环境后刷新 G40-AC5/AC6。
- 是否 commit：否。

### G40-AC6 独立 Windows packaged 证据准备（2026-08-25）

- 复核结论：当前权威 `build/e2e-evidence/packaged-e2e/manifest.json` 仍是 `status=running` / `phase=fixtures`、11 passed / 13 not-run；`docs/E2E.md` 与两个扩展协议文档中将历史 2026-08-11 24/24 写成当前 retained evidence 的冲突已消除。G40-AC6 仍为 `U`。
- 本次状态：准备完成，真实 packaged 执行未开始。新增只读 `--verify-evidence`：只接受 Windows x64 fresh build、24/24 有序 fixtures、当前 Wails pin/build tags、artifact SHA、source fingerprint、Git 状态、fresh harness 日志、两次 launch 日志和 run interval 内 token-free loopback handshake；partial/reused/drift/symlink/stale/secret-bearing/跨运行拼接证据均 fail-closed。
- 改动文件：`scripts/packaged-e2e.mjs`、`scripts/packaged-e2e-driver.test.mjs`、`docs/E2E.md`、`docs/EXTENSION-COMPATIBILITY.md`、`docs/EXTENSION-CONTRIBUTION-PROTOCOL.md`、`docs/prompts/prompt-14.md`、`docs/prompts/prompt-15.md`、`.slim/deepwork/p14-g34-g43-closeout.md`。
- AC 证据：`node --test scripts/packaged-e2e-driver.test.mjs` exit 0，68 tests / 67 passed / 1 skipped（本机无文件 symlink 权限；实现仍显式拒绝，目录 symlink 测试已通过）；`node scripts/packaged-e2e.mjs --dry-run` exit 0 且明确 real packaged remains `U`；`node scripts/packaged-e2e.mjs --verify-evidence` 对当前 manifest exit 1，精确报 `packaged manifest status is not passed`（actual `running`）；Prettier、目标 `git diff --check`、doc links/numbers 均 exit 0。
- 首次失败：新增 verifier 测试首轮 63/65，临时夹具把 Wails `require` 写成单行而未匹配仓库真实 `go.mod` 块格式；改为真实格式后通过。首次并行 Prettier check 在最后一处测试编辑落盘前运行而 exit 1；串行格式化后最终 check 通过。证据链自查还发现未保留 fresh harness stdout，已要求 `fresh-run.log` 并绑定 build/SHA/launch/24-of-24。
- 安全与数据：verifier 不启动 artifact、不构建、不安装、不写 manifest；要求 canonical `bin/koyori-ide.exe`，拒绝 evidence dir/manifest/artifact/screenshot symlink 与 screenshot 路径逃逸，handshake 只允许 loopback、不得保留 bearer token、两次 identity 不得复用，并与 fresh/launch 日志相互绑定。real Worker、packaged、installer 和 Release 证据继续分层。
- 未验证：独立 Windows x64 GUI 环境尚未执行 fresh `node scripts/packaged-e2e.mjs` 和随后 verifier，因此 G40-AC6 保持 `U`；G40-AC5 Release `R` 也不变。
- 下一步：在独立 Windows x64 GUI checkout 按 `docs/E2E.md` 清单运行 fresh 24/24，再立即运行 `--verify-evidence` 并保留 exact source、artifact 和完整 evidence directory。
- 是否 commit：否。

### G40-AC6 packaged verifier reviewer 加固（2026-08-25）

- 复核结论：reviewer 指出的 7 个项目内误报风险已变化后关闭；`docs/E2E.md` 原已包含 pinned Delve `v1.27.1` 安装，其余 6 项由代码和定向契约关闭。production installer 慢测按用户指示本轮跳过，未重复启动，也不改变既有 G38/G39 证据。
- 本次状态：G40-AC6 验收器准备完成，真实 packaged 执行仍为外部 `U`。verifier 现在要求实际 verifier host 为 Windows x64、artifact 为 PE32+ AMD64、当前 Git availability 与 manifest 一致，并拒绝 evidence/bin 任一祖先 symlink/junction 逃逸。
- 改动文件：`scripts/packaged-e2e.mjs`、`scripts/packaged-e2e-driver.test.mjs`、`scripts/g05-packaged-e2e.mjs`、`scripts/g06-packaged-e2e.mjs`、`scripts/http-client-runtime-e2e.mjs`、`scripts/recovery-packaged-e2e.mjs`、`internal/e2e/server.go`、`internal/e2e/server_test.go`、`docs/E2E.md`、`docs/prompts/prompt-14.md`、`docs/prompts/prompt-15.md`、`.slim/deepwork/p14-g34-g43-closeout.md`。
- AC 证据：`node --test scripts/packaged-e2e-driver.test.mjs` exit 0，75 tests / 74 passed / 1 skipped；skip 仅因本机不能创建文件 symlink，目录 junction 负例通过。`go test -tags e2e ./internal/e2e -run TestStart -count=1` exit 0。6 个相关 JS 脚本 `node --check` exit 0。完整 `go test -tags e2e ./internal/e2e -count=1` 首次失败于既有 G23 Rust fixture：`cargo is not installed or not on PATH`，与 runId handshake 改动无关。
- 首次失败：Node 契约初次因误删 `markPackagedE2EManifestFailed` export 在 import 阶段失败；恢复后 75 项中先后暴露 runId 前置断言顺序和当前 Git 不支持 `status --no-refresh`。最终使用 `GIT_OPTIONAL_LOCKS=0` + `git --no-optional-locks`，并由 index bytes/mtime 前后不变测试证明 verifier 只读。
- 安全与数据：每次 fresh run 生成 256-bit `runId`，严格绑定 manifest、fresh log、两次 launch log 和两次 handshake；4 个既有 packaged harness 已同步必填 runId。PowerShell UTF-16LE/BOM fresh log 可读；非 PE、x86、ARM64、Git availability 降级、跨 run 拼接、祖先 junction 均 fail-closed。权限、路径、审批、Worker 配额和 unknown API 行为不变。
- 未验证：未在独立 Windows x64 GUI 环境运行 fresh build/launch/24 fixtures，G40-AC6 继续 `U`；G40-AC5 仍无授权 Release。未运行 production installer 慢测。
- 下一步：仅在独立 Windows x64 GUI checkout 按 `docs/E2E.md` 运行 fresh harness 和只读 verifier；Release 仍需明确授权。
- 是否 commit：否。

### G40-AC6 packaged run identity 收口（2026-08-25）

- 复核结论：reviewer 刷新当前文件后确认原 7 项误报风险及新增离线 `runId` 残余均已关闭；`docs/E2E.md` 已含 pinned Delve `v1.27.1` 安装。真实 packaged 证据仍不存在，不能由验收器契约替代。
- 本次状态：G40-AC6 验收器准备完成，真实 Windows packaged 执行继续外部 `U`。manifest 构造、离线 verifier 与 E2E server 现在统一要求非全零的 64 位小写 hex `runId`；fresh harness 实际输出与 verifier/fixture 统一为 `[packaged-e2e] identity: runId=<id>`。
- 改动文件：`scripts/packaged-e2e.mjs`、`scripts/packaged-e2e-driver.test.mjs`、`internal/e2e/server.go`、`internal/e2e/server_test.go`、`docs/E2E.md`、`docs/prompts/prompt-14.md`、`docs/prompts/prompt-15.md`、`.slim/deepwork/p14-g34-g43-closeout.md`。
- AC 证据：`node --test scripts/packaged-e2e-driver.test.mjs` exit 0，76 tests / 75 passed / 1 skipped；skip 仅因本机不能创建文件 symlink。`go test -tags e2e ./internal/e2e -run TestStart -count=1` exit 0。`node --check scripts/packaged-e2e.mjs` exit 0；`node scripts/packaged-e2e.mjs --dry-run` exit 0 并明确 real packaged remains `U`；`--verify-evidence` 对当前 retained manifest exit 1，精确拒绝缺失的 64 位小写 hex `runId`。最终 `node scripts/backend-gate.mjs` 9/9 exit 0，含 `go test ./... -count=1`、bindings、Wails pin 和 doc gates。
- 首次失败：插入 `assertPackagedRunId` 时两次落入既有多行断言/函数签名内部，产生局部语法错误；均在继续执行前按原始边界恢复，并由 Prettier、`node --check` 和完整 Node 契约确认。随后发现真实 `log("runId", id)` 输出与 fixture 期待的等号格式不一致，已改为单一 identity 格式并复验。
- 安全与数据：离线伪造 bundle 不能再用 `runId=old`、大写 hex 或全零 identity 跨文件自洽冒充 fresh run；server 对缺失/大写/全零值 fail-closed。路径、Git 只读、token-free loopback、审批、权限、Worker `1_000` messages/s 和 unknown API fail-closed 均未放宽。
- 未验证：未在独立 Windows x64 GUI 环境运行 fresh build/launch/24 fixtures，G40-AC6 继续 `U`；G40-AC5 仍无授权 Release。本轮按用户指示未重复运行 production installer 慢测。
- 下一步：仅在独立 Windows x64 GUI checkout 按 `docs/E2E.md` 运行 fresh harness 和只读 verifier；Release 仍需明确授权。
- 是否 commit：否。
