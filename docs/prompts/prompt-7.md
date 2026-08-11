# Koyori IDE 一次性修复 Goal 任务（prompt-7，SSOT）

> **用途：** 汇总 2026-08-03 全量审计（功能完整性、实际可用性、代码质量、安全性、可维护性、开源准备度）发现的全部待修复/待推进事项，形成**单次长会话可执行完毕**的修复目标清单。
> **仓库基线：** Go 1.25 + Wails v3 alpha2.111 + Vue 3 + TS + Vite + Monaco。
> **事实优先级：** 当前代码与实际命令结果 > 本文件 > prompt-6.md > prompt-5.md > prompt-4.md > prompt-1.md。
> **定位：** 0.x Go / TS 优先桌面 AI IDE。审计结论：**修复本文件 P0+P1 全部 AC 前，当前二进制不可发布；完成仓库卫生与高危边界修复后，可开源实验性源码。不得宣称生产级 / 企业就绪 / 完整 Remote-SSH / VS Code 或 Cursor 替代品。**

---

## 0. 总指令（继承 prompt-6 §0，不可弱化）

1. **先读代码再接受结论。** 本文件每条"现状/证据"是线索，不是真理。开始每个 Goal 前必须打开实现与测试复核，并在交付里写明"仍存在 / 已变化 / 已不存在"。行号可能因修复漂移，以实际代码为准。
2. **一次只做一个 Goal。** 按 §2 顺序执行；完成一个（AC 全绿 + 证据落账）才允许开始下一个。完成后停止，不自动越界扩展。
3. **最小正确改动。** 不重构无关模块，不升 major 依赖，不为假设需求加代码，不新增"可能有用"的抽象。
4. **诚实分级 `V / S / U`。** `V` = 本机命令实际通过；`S` = 源码/测试存在但本机未运行；`U` = 需外部环境、凭据、真实平台或 CI 历史。**禁止把 S 或 U 写成 V，禁止伪造证据。**
5. **安全默认拒绝。** 执行、文件、网络、凭据、扩展、Agent、MCP、Remote、更新一律 fail-closed，并加绕过失败测试。
6. **不信任 renderer。** 前端传来的 `approved` / `confirmed` / `safe` / `targetPath` / `allowPrivateNetwork` 不得抬权。高风险能力由后端签发、绑定参数、短时、单次使用。
7. **保护用户数据。** 恢复、快照、多文件编辑、更新不得用 partial result 覆盖磁盘新版本或静默丢数据。
8. **不删测试保绿、不弱化审批、不提交 secret、不擅自 commit / push。**
9. **环境阻塞 ≠ 项目失败。** 阻塞时记录阻塞原因、已完成的源码检查、可复制的复现命令，然后继续下一个可执行的 Goal。
10. **文档必须与能力一致。** stub / prototype / mock / contract-smoke / 手动更新 / 最小远程等边界必须显式可见。
11. **不可回归基线：** prompt-1 的 capability token、MCP root、IM 批准、扩展权限、pathsec、更新边界；prompt-5 已完成的 `WorkspaceContext`、`RecoveryService`、Agent budget epoch、Snapshot 精确语义、Goal prototype 网关；prompt-6 已完成的 `workspace_edit_transaction.go`（P1-04R V 部分）。
12. **本文件是一次性清单：** G-01 ~ G-18 必须**全部**完成并验证后才允许宣称"本次修复闭环"；G-19 / G-20 为边界锁定与状态标注（不要求本会话完成实现）。缺任何 P0 项都不得写"闭环"。
13. **证据纪律：** 每个改动完成立即记录证据（命令输出 / 测试名 / diff 摘要）到 §25 进度板与交付模板；不许到最后才补记。

---

## 1. 已验证基线（2026-08-03 审计确认，不要重做）

### 1.1 命令通过（V，本机已实测）

| 命令 | 结果 |
|---|---|
| `go test ./services/ -count=1` | pass |
| `go test . -count=1` | pass |
| `go vet ./services/... .` | pass |
| 安全切片 `go test ./services/ -race -count=1 -run 'Agent|MCP|ComputerUse|IM|Remote|Path|Snapshot|Goal|Recovery|Workspace'` | pass |
| `go test ./services/ -coverprofile=... -count=1` | pass，覆盖率 ≈ 69.7% |
| `npx vue-tsc --noEmit` | pass |
| `npm run lint` | pass |
| `npm run build` | pass |
| `node scripts/check-bindings.mjs` | pass |
| `node scripts/check-doc-links.mjs` / `check-doc-numbers.mjs` / `check-wails-pin.mjs` | pass |
| `node scripts/contract-smoke.mjs` | pass（非 packaged E2E） |
| `node scripts/packaged-e2e.mjs --dry-run` | pass（无 driver 时实际运行 exit 1，fail-closed 已确认） |

### 1.2 已知失败与风险（V 级证据，修复目标）

| 项 | 证据 | 结论 |
|---|---|---|
| 前端测试 4 项失败 | 157 文件 / 2561 测试 / 4 fail，全部在 `frontend/src/views/SettingsView.test.ts` | 测试仍期望已迁移到 AI 窗口的 ai/agent/prompts/presets/computerUse 设置 section |
| `npm audit --audit-level=high` 2 个 High | `brace-expansion` DoS `GHSA-mh99-v99m-4gvg`；`postcss <=8.5.17` 路径穿越/源映射泄露 `GHSA-r28c-9q8g-f849` | 需升级或消除 |
| 发布元数据不一致 | `VERSION`=0.2.0；`build/windows/info.json`、`build/linux/nfpm/nfpm.yaml`、`build/darwin/Info.plist`、`build/scripts/*.sh` 回退值仍 0.1.0 | 需 SSOT 统一 |
| 仓库卫生 | 根目录存在 `koyori-ide.exe`（49MB）、`NUL`、`$profile`（1.2MB PowerShell 个人文件）、`.claude/`、`.agents/`、`.omo/` | 发布前需清理/确认未 tracked |

### 1.3 环境限制（U，不得谎报）

- 本机**无 `wails3` CLI** → packaged build / packaged E2E 一律 `U`。
- 本机**不是 Windows/macOS** → Windows/macOS 打包、签名、公证、启动一律 `U`。
- 仓库 `.git/` 为空目录 → 无法确认 tracked 文件、tag/release 历史与 CI 历史（release workflow 只读 YAML = `S`）。
- `govulncheck` 因外网 EOF 失败（`sum.golang.org`、`vuln.go.dev` 均不可达）→ Go 漏洞扫描本会话标 `U`，记录重试命令。
- 真实 SSH 集成、packaged kill/restart 恢复演练、Monaco long-task trace → `U`。

---

## 2. 目标总览与执行顺序

| ID | 主题 | 严重级 | 会话范围 |
|---|---|---|---|
| G-01 | 收敛 renderer 可调 workspace root setter，空 root fail-closed | P0 / Critical | 一次性完成 |
| G-02 | Agent 文件写入后端 capability + write_file 接入统一事务 | P0 / Critical | 一次性完成 |
| G-03 | 普通保存原子化 + baseline 冲突检测 | P0 | 一次性完成 |
| G-04 | 多文件替换/搜索前端接入统一事务，消除 partial 风险 | P0 | 一次性完成 |
| G-05 | Recovery 启动扫描闭环接通 | P0 | 一次性完成 |
| G-06 | HTTPClient `AllowPrivateNetwork` 改后端签发 | P0 | 一次性完成 |
| G-07 | Coverage/Toolchain/ESLint/Debug 等命令入口绑定 WorkspaceContext | P0 | 一次性完成 |
| G-08 | release.yml 产物格式与架构矩阵修复 | P0 | 一次性完成 |
| G-09 | 平台元数据统一从 VERSION SSOT 生成 | P0 | 一次性完成 |
| G-10 | Settings 测试修复 + 设置收敛深链/双窗口测试 | P1 | 一次性完成 |
| G-11 | npm audit High 依赖消除 | P1 | 一次性完成 |
| G-12 | `.code-workspace` 多根接线 `AddMultiRootProject` | P1 | 一次性完成 |
| G-13 | Git Worktree / 交互式 Rebase 服务注册与 UI 接线 | P1 | 一次性完成 |
| G-14 | Debug / Test Explorer 普通入口 | P1 | 一次性完成 |
| G-15 | AI Diff apply 结果可靠落盘与编辑器同步 | P1 | 一次性完成 |
| G-16 | packaged E2E automation hook + driver 推进 | P1 | 一次性完成（本机 U 边界如实标注） |
| G-17 | 仓库卫生 + NOTICE / 许可证清单 / SBOM 流水线 | P1 | 一次性完成 |
| G-18 | README / SECURITY / 发布文档诚实定位 | P1 | 一次性完成 |
| G-19 | Remote 统一 Host / Language Pack / 插件协议边界锁定 | P2 | 只做边界与设计，不实现 |
| G-20 | SLO / 外部审计标注与策略 | P3 | 只做标注与文档 |

**依赖关系：** G-01 先于 G-02/G-06/G-07（安全边界是写路径前置）；G-04 依赖 G-03 的事务/原子能力；G-09 依赖 G-08 的 workflow 修复；G-15 依赖 G-04 的事务；G-16 独立可并行；G-17/G-18 收尾。

---

## 3. GOAL G-01（P0）：收敛 renderer 可调 workspace root setter，空 root fail-closed

**现状（证据，需复核）：** 以下方法仍作为 Wails 导出 API 暴露给 renderer，可独立切换/清空 workspace root，绕过 `ProjectService.AddProject` 的两阶段协调与 generation 绑定：

- `services/file_service.go:76 SetWorkspaceRoot`、`:107 SetWorkspaceRoots`
- `services/git_service.go:346 SetWorkspaceRoot`（空 root 注释明确"disable sandboxing"）
- `services/lsp_service.go:880 SetWorkspaceRoot`、`:896 SetWorkspaceRoots`
- `services/search_service.go:46`、`services/toolchain_service.go:53`、`services/coverage_service.go:157`、`services/eslint_service.go:53`、`services/skills_service.go:115`、`services/symbol_index_service.go:177`、`services/terminal_service.go:96`、`services/agent_service.go:213`、`services/ai_service.go:469 SetProjectRoot`
- MCP root setter（`MCPServiceRootSetter` 接口）

共享语义 `services/pathsec.go:28-30,53-55`：**空 root 时任意路径放行**（写入场景虽有 `validateMutatingPath` 兜底，但非全部入口统一）。`ProjectService` 已有 `SetGitService` / `SetSearchService` / `SetLSPService` / `SetToolchainService` / `SetSymbolIndexService` / `SetMCPService` / `SetWorkspaceContext` 等 `//wails:ignore` 注入点（`services/project_service.go:109-164`），说明协调路径已存在，只是 setter 本身仍可从 renderer 直接调用。

**执行点：**

1. 逐一复核上述 setter：确认每个是否带 `//wails:ignore` 注释（README §12.2 要求"新增 trusted setter 必须带 `//wails:ignore` 并有 AST 测试"）。缺失的补上；存在 AST 测试的确认覆盖全部 setter 清单。
2. 所有 root 变更收敛到 `ProjectService` 协调入口（`AddProject` / `RemoveProject` / workspace 切换）；renderer 只允许调用协调入口，不允许直接 `SetWorkspaceRoot`。
3. **空 root 语义统一 fail-closed**：所有变更操作（写文件、删除、重命名、git 变更、search 替换、agent run、terminal exec、LSP 变更、coverage run、debug launch）在空 root 下返回明确错误，不允许"空 root = 任意路径放行"。读取类操作按现有设计评估后明确记录策略（可保留宽松或收紧，但必须写清并测试）。
4. 增加/更新 AST 测试：扫描 `services/` 中所有 `SetWorkspaceRoot*` / `SetProjectRoot` / root setter 方法，断言带 `//wails:ignore` 或不在 Wails 导出范围。
5. 更新 `frontend/bindings/`：被隐藏的方法从 binding 中移除（`node scripts/check-bindings.mjs` 必须仍通过；若方法不再导出，需检查前端是否有调用点并改走协调入口）。

**AC：**

- [ ] AST/静态测试断言所有 root setter 均不可从 renderer 调用（有测试名证据）。
- [ ] 空 root 下：`WriteFile` / `CreateFile` / `CreateDirectory` / `DeleteFile` / `Rename` / `SearchService` 替换 / `AgentService` run / `TerminalService` exec / `CoverageService` run / `DebugService` launch 全部 fail-closed，各有测试。
- [ ] `pathsec.go` 空 root 语义测试更新：写路径空 root 一律拒绝，不再"任意路径放行"。
- [ ] `check-bindings.mjs` pass；前端无任何残留直接调用被隐藏 setter 的代码（grep 证据）。
- [ ] 既有安全测试（symlink 逃逸、pathsec、generation 切换）全部仍 pass。

---

## 4. GOAL G-02（P0）：Agent 文件写入后端 capability + write_file 接入统一事务

**现状（证据，需复核）：** `frontend/src/stores/agent.ts:444-455` 的 `executeWriteTool` 直接 `await fileService.writeFile(resolved.absPath, tc.content)`；`:567-573` `executeToolCall` 无后端授权。对比 `:471-481` 的 `executeRunTool` 已走 `agentService.requestCommandApproval` + `executeApprovedCommand`（后端签发一次性 token，绑定 cwd）。**写文件没有等价 capability**：`approved` 状态仅是前端状态，违反"不信任 renderer"。路径沙箱依赖 workspace root（受 G-01 保护），但写入意图、批准者、目标、content hash、workspace generation 未绑定。

**执行点：**

1. 仿照命令审批流实现后端写审批：`RequestWriteApproval(targetPath, contentHash, size)` 签发短时、单次使用、绑定（workspace generation + 解析后绝对路径 + content hash + TTL）的 token；`ExecuteApprovedWrite(targetPath, content, token)` 后端复核 token、pathsec、dirty-buffer baseline（G-03 完成后接入）。
2. token 校验失败 / 过期 / 重放 / 跨参数 / 跨 generation 一律拒绝，各加绕过失败测试。
3. write_file 工具迁移到统一事务（`services/workspace_edit_transaction.go` 已有 `ApplyDiffTransaction` / `ApplyMultiFileReplaceTransaction` 模式，P1-04R 剩余缺口：write_file 路径未接入、前端仍可绕过调 `FileService.WriteFile`）。write_file 必须与 AI Diff / Plan / Goal 写入共用同一事务与 precondition。
4. 前端 `executeWriteTool` 改为：请求审批 → 用户批准 → 调用 `ExecuteApprovedWrite`；拒绝时向 AI 反馈拒绝理由，不执行。
5. Plan step / Goal round 的文件写入同样迁移（复核 `services/ai_plan_service.go`、`services/ai_goal_service.go` 当前实现）。

**AC：**

- [ ] 写文件与 shell run 一样有后端签发 token（测试名证据）。
- [ ] 绕过测试：伪造 approved、重放 token、改 targetPath、跨 generation 用旧 token 全部失败。
- [ ] write_file / Plan / Goal / Diff 四类写入入口均走统一事务，各有失败回滚测试。
- [ ] 中途失败不留 partial write（多文件事务测试断言全部文件回到原内容）。
- [ ] 前端 approve/reject 流程测试覆盖成功 / 拒绝 / token 过期。

---

## 5. GOAL G-03（P0）：普通保存原子化 + baseline 冲突检测

**现状（证据，需复核）：** `services/file_service.go:258-272` `WriteFile` 直接 `os.WriteFile(abs, []byte(content), 0644)`：非原子（崩溃可留截断文件）、无 baseline/hash 冲突检测（磁盘已被外部修改时会覆盖新内容）、无 dirty-buffer 一致性断言。`RecoveryService` 已有 baseline（mtime + SHA-256）设计（`services/recovery_service.go:76-98`），`workspace_edit_transaction.go` 已有 `BaselineHash` 前置条件——普通保存入口未统一接入。

**执行点：**

1. `WriteFile`（及 CreateFile/CreateDirectory/Delete/Rename 的保存路径）改为**原子写**：同目录 temp 文件 + fsync + rename 发布；保留原文件权限位；失败清理 temp。
2. 保存入口支持可选 baseline 参数（path + mtime + hash，复用 Recovery 的 `RecoveryBaseline`）：磁盘状态与 baseline 不一致时返回结构化 `Conflict` 错误，**不覆盖**。
3. 前端保存流程：保存前携带当前打开文件的 baseline；收到 Conflict 时显示冲突 UI（用户选择覆盖 / 重新加载），不得静默覆盖。
4. 保持 `file:saved` 事件与工作流触发（Proposal B）行为不变。
5. 并发保存 / last-write 策略：同一文件两窗口并发保存时，后写者必须带最新 baseline，旧 baseline 写入被拒。

**AC：**

- [ ] 原子写测试：注入失败（如目标为目录、权限拒绝、磁盘满模拟）后无残留 temp、原文件不被截断。
- [ ] baseline 冲突测试：外部修改后保存返回 Conflict，原内容保留。
- [ ] 前端冲突 UI 测试：覆盖选择覆盖 / 重新加载两条路径。
- [ ] 既有 `file:saved` 事件测试仍 pass。

---

## 6. GOAL G-04（P0）：多文件替换/搜索前端接入统一事务

**现状（证据，需复核）：** `frontend/src/stores/search.ts:183-212` `applySelectedPreviews` 逐文件循环调用 `searchService.applyReplacePreview`（带 `originalHash`），中途任一文件失败只累计已成功数并返回，**已成功的文件不回滚 → partial result**。后端已有 `ApplyMultiFileReplaceTransaction`（prompt-6 §14 记录 V 部分），但前端未接入。

**执行点：**

1. 后端确认 `ApplyMultiFileReplaceTransaction` 完整支持：全部文件 hash 前置检查（任一冲突即整体拒绝）、LIFO 回滚、dirty-buffer 检查、pathsec 校验（root 取 `WorkspaceContext.RequireRoot()`，空 root 拒绝）。
2. 前端 `applySelectedPreviews` / `replaceAll` 改调批量事务 API，**单次调用完成全部文件**；失败时后端保证整体回滚，前端只展示结构化错误（含哪些文件冲突）。
3. 删除/保留逐文件 `applyReplacePreview` 的决定：若保留（单文件场景），必须走同一 precondition 检查。
4. 预览生成仍可保留逐文件（只读），但 apply 必须事务化。

**AC：**

- [ ] 多文件替换注入 hash 冲突时，所有文件回到原内容（测试断言）。
- [ ] 任一文件失败时前端收到结构化冲突列表，不出现"部分成功"误导文案。
- [ ] 空 root / 路径逃逸测试 fail-closed。
- [ ] `replaceAll` 与 UI 路径（SearchPanel 等）接线测试通过。

---

## 7. GOAL G-05（P0）：Recovery 启动扫描闭环接通

**现状（证据，需复核）：** `frontend/src/stores/recovery.ts:208-226` `scanRecoverable()` 已实现（扫描 journal、分类 clean/conflict/missing、损坏隔离），但**全仓库 grep 仅发现该函数与测试调用，生产启动路径没有调用点** → 崩溃后 journal 已写入，启动却不会自动扫描提示，"崩溃恢复闭环"不可达。后端 `RecoveryService` 已注册（`bootstrap_services.go:124`）。

**执行点：**

1. 生产启动流程（app 初始化 / 主窗口 ready / 首个 workspace 打开后）调用 `scanRecoverable()`。
2. `recoveryPending > 0` 时展示恢复 UI（现有 Recovery 面板/对话框）：clean 恢复、conflict 呈现选择（不覆盖磁盘新版本）、missing 提示；`finishRecovery()` 后才恢复 auto-save（现有 gating 逻辑复用）。
3. 扫描失败必须静默降级（不阻塞启动）但**可见提示**（现有实现 catch 后 console.warn，需补 UI 可见的降级提示，除非已有）。
4. 加启动接线测试：mock binding 断言启动时调用 scanRecoverable 且 pending>0 时进入恢复流程。
5. kill -9 / restart 演练在本机 UI 环境不可行则标 `U`，但必须给出 packaged E2E（G-16）中的 fixture 计划（kill -9 → restart → 恢复 dirty buffer → 磁盘更新时冲突而非覆盖）。

**AC：**

- [ ] 启动代码存在 `scanRecoverable()` 调用（grep 证据 + 测试）。
- [ ] pending>0 时恢复 UI 出现；conflict 不自动覆盖磁盘。
- [ ] 扫描异常不阻塞启动（测试覆盖）。
- [ ] G-16 的 recovery fixture 计划已写入 packaged-e2e.mjs 文档或脚本注释。

---

## 8. GOAL G-06（P0）：HTTPClient `AllowPrivateNetwork` 改后端签发

**现状（证据，需复核）：** `services/http_client_service.go:107-126` `SendRequest` 用 `options.AllowPrivateNetwork` 决定 SSRF 校验（`:124` 初始 URL、`:191-197` transport 选择、`:208-210` redirect 校验）；该字段来自 renderer（`services/http_client_model.go:32`、`frontend/src/components/http/HTTPClientPanel.vue:32,62,93`）。**前端布尔直接抬权（可访问内网/metadata 服务）**，无后端批准、无绑定。

**执行点：**

1. 移除 renderer 布尔对 SSRF 策略的直接影响：`AllowPrivateNetwork` 不再从 `HTTPRequestOptions` 读取，或改由后端按请求签发。
2. 设计并实现后端签发能力：如 `RequestPrivateNetworkAccess(targetOrigin)` → 一次性 token（绑定 origin + 请求 ID + TTL，单次使用）；`SendRequest` 携带 token 时后端复核后才允许非公网目标。token 缺失/过期/跨 origin 一律拒绝。
3. redirect 校验与初始 URL 使用同一策略（同一 token 生命周期内允许，跨 origin 重定向仍需校验）。
4. UI：私网开关改为"请求后端授权"交互；授权被拒时显示明确错误。
5. 现有 SSRF 测试（公网放行、私网默认拒绝、redirect 逃逸）全部保持并通过。

**AC：**

- [ ] 无 token 时私网目标一律拒绝（测试）。
- [ ] token 绑定 origin / 请求 / TTL；重放、跨 origin、过期全部失败（测试）。
- [ ] `HTTPClientPanel.vue` 不再直接传 `allowPrivateNetwork` 布尔抬权（代码证据）。
- [ ] SSRF 基线测试无回归。

---

## 9. GOAL G-07（P0）：命令/启动入口统一绑定 WorkspaceContext

**现状（证据，需复核）：** 以下入口的路径/命令参数来自 renderer，未统一绑定 `WorkspaceContext.RequireRoot()` 与 capability：`CoverageService.RunPackageCoverage`（renderer 传 packageDir）、Toolchain 命令、ESLint 运行、`DebugService` launch（Node/browser/DAP 启动路径与参数，`services/debug_launch.go`）、Terminal exec、Agent run（已有 token 流，复核 root 绑定）。G-01 完成 setter 收敛后，这些入口必须在**调用时刻**从共享 context 取 root，而不是缓存启动时值。

**执行点：**

1. 逐一复核上述入口：root 是否来自 `WorkspaceContext.RequireRoot()`（调用时刻）、空 root 是否拒绝、参数路径是否经 `ValidatePathWithinRoot`。
2. 未绑定的改为调用时刻绑定；删除启动时缓存 root 的字段（复核现有 `workspaceRoot` 字段是否为死值）。
3. 每个入口加测试：空 root 拒绝、跨 workspace generation 拒绝、路径逃逸拒绝。
4. debug launch 的 executable/args 来源（用户配置 vs workspace 文件）明确策略：不可信 workspace 默认不自动启动项目提供的 executable（与 prompt-6 P1-05 §6 一致）。

**AC：**

- [ ] 所有命令/启动入口在调用时刻绑定共享 root（grep/测试证据）。
- [ ] 空 root、跨 generation、路径逃逸各入口均有失败测试。
- [ ] debug launch 对项目提供的 executable 有默认拒绝/显式确认策略与测试。

---

## 10. GOAL G-08（P0）：release.yml 产物格式与架构矩阵修复

**现状（证据，需复核）：** `.github/workflows/release.yml`：

- `:47-62` matrix 中 macOS amd64 与 arm64 两个 job 的 `build_cmd` 均为 `wails3 build -tags desktop,production`，**未使用 `matrix.arch`** → 两 job 可能构建相同（主机默认）架构却命名为 darwin-amd64 / darwin-arm64，其中一个是错误标注。
- `:264-280` Package (Linux/macOS) 步骤把 `.app` 或二进制用 `tar -czf ${{ matrix.artifact_name }}` 打包，而 artifact_name 是 `koyori-ide-<ver>-darwin-<arch>.zip`（`:57,:61`）→ **zip 扩展名 + tar.gz 内容**。
- `:120-121` 版本校验只比对 `VERSION` 与 `build/config.yml` 顶层 `version:`，未覆盖平台元数据（G-09 修复后此校验需扩展）。

**执行点：**

1. macOS 构建命令使用 `matrix.arch`（Wails3/Go 交叉或 runner 上 `GOARCH` 环境变量），确认产物真实架构；Linux/Windows 同理显式声明。
2. macOS 打包改用 `ditto -c -k --keepParent`（与 `:244-245` notarization 一致）生成真 zip；或把 artifact 名改为 `.tar.gz` 并更新文档/校验逻辑。二选一，但**名称必须与格式一致**。
3. 打包步骤对产物缺失/多产物歧义 fail（现有 `head -1` 选取逻辑改为显式单文件断言）。
4. 版本校验步骤扩展：校验 Windows info.json、nfpm.yaml、Info.plist 与 VERSION 一致（或由 G-09 的生成步骤保证后，此处校验生成结果）。
5. 只读 YAML 无法本机运行：所有 AC 以"代码审查 + shell 语法检查（`bash -n`）+ dry-run 推演"标注 `S`，真实跑通标 `U` 并给出触发方式（tag push）。

**AC：**

- [ ] `bash -n` 通过；matrix 每个 job 显式传递 arch（S 级代码证据）。
- [ ] macOS artifact 名与打包格式一致（zip→ditto / tar.gz→tar，二选一）。
- [ ] 版本校验覆盖全部平台元数据来源。
- [ ] 文档（`docs/RELEASING.md`）与 workflow 行为一致；CI 实跑结果如实标 `U`。

---

## 11. GOAL G-09（P0）：平台元数据统一从 VERSION SSOT 生成

**现状（证据，需复核）：** 根 `VERSION`=0.2.0（SSOT，`release_version_test.go` 13 例通过），但：

- `build/darwin/Info.plist:8-17`：`CFBundleExecutable`=**koyori-ide.exe**（macOS 实际 executable 是 `koyori-ide`）、`CFBundleIdentifier`=com.koyori-ide.app（模板）、版本 0.1.0、Copyright"My Company"。
- `build/windows/info.json:3,7`：file_version / ProductVersion = 0.1.0。
- `build/linux/nfpm/nfpm.yaml:9`：version=0.1.0，`:14-15` vendor=koyoriIde、homepage=https://wails.io（模板残留）。
- `build/scripts/build-macos.sh:62-66`、`build/scripts/build-linux.sh:65-69`：`grep -E '^version:'` 匹配顶层 `version:` 键，但实际配置版本在 `info.version` → grep 失败回退 0.1.0。

**执行点：**

1. 修正两个 build 脚本的版本读取：从根 `VERSION` 文件读取（SSOT），或正确解析 `build/config.yml` 的 `info.version`；无法读取时 **fail** 而不是静默回退 0.1.0。
2. 修正 `Info.plist`：executable=`koyori-ide`、bundle id 换成正式反向域名（不是 com.koyori-ide.app 模板，除非确认是正式标识）、版本从构建时注入（脚本/CI 替换占位符）。
3. 修正 `nfpm.yaml` vendor/homepage/version；Windows info.json 版本从 SSOT 注入。
4. 扩展 `release_version_test.go`：新增断言 platform 元数据文件与 `VERSION` 一致（可解析 Info.plist / info.json / nfpm.yaml 的测试）。
5. 构建脚本补测试或至少 `bash -n` + 手动运行（`build/scripts/build-linux.sh` 若能在本机部分执行则跑版本读取段，标 `V`；打包段标 `U`）。

**AC：**

- [ ] 所有平台元数据文件版本与 `VERSION` 一致（`release_version_test.go` 新用例 pass）。
- [ ] Info.plist executable/bundle id/copyright 正确，无模板残留。
- [ ] build 脚本版本读取失败时 fail（测试/脚本证据）。
- [ ] Windows/Linux/macOS 元数据在 CI 产物中一致（S 级，实跑 U）。

---

## 12. GOAL G-10（P1）：Settings 测试修复 + 设置收敛深链/双窗口测试

**现状（证据，需复核）：** `frontend/src/views/SettingsView.vue` 已把 5 个 AI 可写 section（ai/agent/prompts/presets/computerUse）迁移到独立 AI 窗口（`AiSettingsView.vue` 为 SSOT），`selectSection()` 对旧深链调 `openAIDesktopWindow()` 并重写 URL 为 general；但：

- `frontend/src/views/SettingsView.test.ts` **4 个测试失败**（仍期望旧 section 存在）。
- 旧深链跳转无路由测试；`openAIDesktopWindow()` 失败被 catch 吞掉（按钮无响应）；双窗口一致性/并发保存无测试；设置 schema 无版本化与迁移测试；搜索命中已迁移项并打开 AI 窗口准确位置未验证。

**执行点：**

1. **先修 4 个失败测试**：更新为断言 AI section 不在主设置中出现；主 IDE 设置中不存在第二个可写 AI 实例（有测试断言）。
2. 加路由测试：`settings?section=ai|agent|prompts|presets|computerUse` 每个旧深链都断言"调用了 openAIWindow + URL 被重写 + 未渲染任何 AI 可写表单"。
3. `openAIDesktopWindow()` 失败路径：可见错误 + 重试入口；不恢复第二套设置实现。
4. 设置 schema 版本化 + 迁移函数：未知字段不被无关保存抹除，非法值回退并提示；升级现有用户配置后 provider/model/prompts/presets/permissions 不丢失（测试）。
5. 双窗口同步：修改经响应式事件或版本号广播；并发保存 last-write-wins + 版本校验，旧窗口不得覆盖新值（测试）。
6. 设置搜索支持"迁移项"类型：命中后打开 AI 窗口并深链到对应 group/item（测试）。
7. Computer Use / MCP / IM / 模型权限 / 数据发送边界的安全文案与默认关闭策略不回归（测试/断言）。

**AC：**

- [ ] `npm test` 0 失败（157 文件全绿）。
- [ ] 5 个旧深链各有路由测试，断言跳转与 URL 重写。
- [ ] 双窗口并发保存不丢新值（测试）。
- [ ] schema 迁移测试覆盖升级不丢配置；未知字段不抹除。
- [ ] 设置搜索命中迁移项并打开准确位置（测试）。

---

## 13. GOAL G-11（P1）：npm audit High 依赖消除

**现状（证据，需复核）：** `npm audit --audit-level=high --registry=https://registry.npmjs.org` 报告 2 个 High：`brace-expansion`（`GHSA-mh99-v99m-4gvg`，正则 DoS）、`postcss <=8.5.17`（`GHSA-r28c-9q8g-f849`，路径穿越/源映射泄露）。

**执行点：**

1. 定位引入链（`npm audit` 完整输出 / `npm ls brace-expansion postcss`），升级到修复版本或替换。
2. 若为间接依赖且无修复版：记录缓解说明（是否可达攻击面），不允许静默忽略；`package-lock.json` 更新后重跑 audit。
3. 全量 `npm audit` 目标：0 high / 0 critical；medium 可记录。
4. 更新后重跑 `npm ci && npm test && npm run build`（在 `/tmp` 副本，见 §24）。

**AC：**

- [ ] `npm audit --audit-level=high` 0 个 High/Critical（或每条有记录说明 + 可达性分析）。
- [ ] `npm ci` + `npm test` + `npm run build` 在依赖更新后全绿。

---

## 14. GOAL G-12（P1）：`.code-workspace` 多根接线 `AddMultiRootProject`

**现状（证据，需复核）：** 后端存在多根支持：`FileService.SetWorkspaceRoots`（`:107-149`）、`LSPService.SetWorkspaceRoots`（`:896-911`）、`ValidatePathWithinRoots`（`pathsec.go:85+`）；`Project.IsWorkspace/Roots` 字段（`project_service.go:41-64`）。但前端 grep **未发现** `AddMultiRootProject` / `addMultiRootProject` 调用 → `.code-workspace` 解析大概率只停留在前端，未接入统一 workspace 切换（generation 原子性）。

**执行点：**

1. 复核后端是否已有 `AddMultiRootProject`（或等价多根 AddProject 路径）及其两阶段提交；没有则补。
2. 前端 `.code-workspace` 打开流程调用多根协调入口；所有根经 pathsec 校验；任一根无效整体失败（不部分生效）。
3. 多根下 LSP workspaceFolders 推送、Search/File/SymbolIndex 的 root 列表同步一致。
4. 测试：多根项目打开、单根退化、无效根整体拒绝、generation 切换原子性。

**AC：**

- [ ] 打开 `.code-workspace` 走统一多根入口（代码+测试证据）。
- [ ] 任一根无效时整体拒绝，不出现部分生效。
- [ ] 多根下 LSP/Search/File 行为一致（测试）。

---

## 15. GOAL G-13（P1）：Git Worktree / 交互式 Rebase 服务注册与 UI 接线

**现状（证据，需复核）：** `services/git_worktree_service.go`、`services/git_rebase_service.go` 已实现并有大量测试（worktree：list/add/remove/lock/move/prune、外部路径授权、safeRoots；rebase：todo 解析、applyActions 中断恢复、symlink todo 拒绝、real git 生命周期）。但 `bootstrap_services.go:76-125` 的 `wailsServices()` **未注册** `GitWorktreeService` / `GitRebaseService`（appBundle 中也没有对应字段）→ 后端能力存在但生产 UI 不可达。

**执行点：**

1. 在 `appBundle` 与 `wailsServices()` 注册两个服务（按 §13.3 binding 规则同步 `frontend/bindings/`、`models.ts`、`index.ts`；`check-bindings.mjs` 通过）。
2. 前端接线：Git 面板加入 worktree 列表/添加/移除/锁定入口与交互式 rebase 视图（todo 列表、reorder/drop/abort/continue、conflict 提示）；无入口的深链保持但必须有普通入口。
3. safeRoots 配置沿用现有依赖注入（`NewGitWorktreeServiceWithSafeRoots`），root 与 WorkspaceContext 一致。
4. 测试：binding 存在性（check-bindings）、前端 store/组件基础交互；真实 git 流程本机可跑（已有 real git 测试）标 V。

**AC：**

- [ ] 两个服务已注册且 bindings 生成（`check-bindings.mjs` pass）。
- [ ] Git UI 有 worktree 与 rebase 普通入口（组件/路由证据）。
- [ ] worktree/rebase 的既有后端测试全部仍 pass。

---

## 16. GOAL G-14（P1）：Debug / Test Explorer 普通入口

**现状（证据，需复核）：** Debug（`DebugService`、`services/debug_launch.go`）与 Test 能力存在，但 Activity Bar 缺少普通入口，只能深链访问 → 普通用户不可达。

**执行点：**

1. Activity Bar 增加 Debug 与 Test Explorer 入口（图标 + 视图容器），与现有 deep link 指向同一视图（不复制实现）。
2. 空状态/loading/error 状态齐全；窄窗口不横向溢出；键盘可达。
3. 前端测试覆盖入口渲染与切换。

**AC：**

- [ ] Activity Bar 存在两个普通入口且可达对应视图（证据）。
- [ ] 视图状态齐全 + 键盘可达（测试）。

---

## 17. GOAL G-15（P1）：AI Diff apply 结果可靠落盘与编辑器同步

**现状（证据，需复核）：** `frontend/src/components/ai-assistant/DiffViewer.vue:197-203` `handleApplyFile/handleApplyAll` **忽略** `applyFile`/`applyAll` 的返回内容；`frontend/src/stores/diff.ts:243-269` 只计算并返回内容，未可靠写入编辑器 model 与磁盘。后端已有 `DiffService.ApplyDiffTransaction`（P1-04R 产物）——前端未接线到事务结果。

**执行点：**

1. `applyFile`/`applyAll` 改走事务 API（复用 G-04 的统一事务），返回结果含：成功文件、冲突列表、回滚信息。
2. apply 成功后：更新 Monaco model（如文件已打开）、更新 dirty-buffer baseline（G-03 接入后）、通知文件树刷新；失败/冲突显示结构化错误与选择（不静默）。
3. `DiffViewer.vue` 消费返回值：成功 toast 与失败/冲突提示；不再丢弃。
4. 测试：apply 成功同步 model/baseline；冲突路径显示选择；回滚后 UI 状态一致。

**AC：**

- [ ] apply 返回值被 UI 消费（代码证据）。
- [ ] 成功路径 model/baseline/文件树同步（测试）。
- [ ] 冲突/失败路径不静默、不覆盖（测试）。

---

## 18. GOAL G-16（P1）：packaged E2E automation hook + driver 推进

**现状（证据，需复核）：** `scripts/contract-smoke.mjs` 已正名（非 packaged E2E）；`scripts/packaged-e2e.mjs` 脚手架存在（artifact SHA-256、xvfb、日志/截图收集、**无 driver 时 exit 1 已实测**）；7 个核心 fixture（open workspace、open file、edit、save、terminal 一条命令、LSP hover/completion、kill -9 后 restart 恢复）无 driver；CI job `packaged-e2e` 暂 gate 在 `workflow_dispatch`。本机无 `wails3` CLI → artifact 生产 `U`。

**执行点：**

1. 为 packaged build 增加**仅测试构建标签启用**的自动化端点：建议本地 loopback + 一次性 token + 仅在 `KOYORI_IDE_E2E=1` 时监听；**绝不在正式 release 构建中编译进去**。加测试断言正式构建不含该端点（如 build tag 编译检查）。
2. driver 覆盖 7 个核心 fixture（对应 §7 的 recovery fixture 联动）；失败上传日志/截图。
3. CI 配 Linux GUI runner（xvfb）+ Wails CLI；job 从 `workflow_dispatch` 转 required 前必须先稳定通过 3 次（写入门禁注释/文档）。
4. artifact 记录 commit、checksum、runner 环境。
5. 本会话：hook 代码 + driver 代码 + fixture 脚本可在仓库内完成并单测（标 V）；真实 packaged 运行标 `U`，记录复现步骤。

**AC：**

- [ ] automation hook 仅在测试构建存在（有测试证明正式构建无该端点）。
- [ ] 7 个 fixture 的 driver 代码存在，dry-run/fixture 单元层面通过（V），真实 artifact 运行标 U。
- [ ] CI job 配置完整（S），3 次稳定门槛写入门禁说明。
- [ ] Windows / macOS 明确标 `U`。

---

## 19. GOAL G-17（P1）：仓库卫生 + NOTICE / 许可证清单 / SBOM 流水线

**现状（证据，需复核）：** 根目录存在：`koyori-ide.exe`（49MB，Windows 产物）、`NUL`（0 字节，Windows 保留名误创建）、`$profile`（1.2MB PowerShell 个人文件，疑似个人数据）、`.claude/`（个人配置，含 `settings.local.json` 可能带 secret）、`.agents/`、`.omo/`、`.task/`；空 `.git/` 无法确认 tracked 状态；无 `NOTICE`、无第三方许可证清单、无 SBOM 产物。

**执行点：**

1. 移除工作区内的 `koyori-ide.exe` / `NUL` / `$profile`（先确认未 tracked；`.git/` 为空无法确认时按"应移除 + gitignore"处理并记录）；`.claude/settings.local.json` 检查 secret（key/token），有则提示用户轮换。
2. `.gitignore` 补充：`koyori-ide.exe`、`*.exe`（根）、`NUL`、`$profile`、`.claude/`、`.omo/`、`.task/`、bin/（视需要）。
3. 新增 `NOTICE`：第三方组件/许可证汇总（Go deps + npm deps，可用工具生成 + 人工复核）。
4. release.yml 增加 SBOM/provenance 步骤（如 syft + attestation，或至少 SBOM 生成与 artifact 一起发布）；缺失时在 RELEASING.md 标注"稳定版发布需 SBOM"。
5. 文档列出依赖许可证审查结果与例外。

**AC：**

- [ ] 上述文件从工作区移除且 gitignore 覆盖（证据）。
- [ ] `.claude/settings.local.json` 无 secret 残留（或已提示轮换）。
- [ ] `NOTICE` 与许可证清单存在（V/S）。
- [ ] SBOM 步骤在 release.yml（S），RELEASING.md 有对应说明。

---

## 20. GOAL G-18（P1）：README / SECURITY / 发布文档诚实定位

**现状（证据，需复核）：** README 功能宣传需与真实能力对照（如 AI、Remote、Debug、LSP、插件）；SECURITY.md 已有 0.2.x best-effort 边界；`docs/RELEASING.md` 未覆盖平台元数据与打包格式问题（G-08/G-09 修复后需同步）。

**执行点：**

1. 全仓 grep 并修正宣称：`production|enterprise|生产级|企业就绪|完整 Remote-SSH|VS Code 兼容|替代品` 等表述，改为能力矩阵（按 V/S/U 标注）。
2. README 增加"当前能力边界"表：本地编辑（V 部分）/ Git / LSP / AI / Agent / Recovery / Remote（最小 SSH/SFTP）/ Debug / 插件 / 发布供应链，每项标注验证等级。
3. SECURITY.md 明确：Wails v3 alpha、0.x、best-effort、无 SLO/外部审计（G-20 联动）；漏洞报告路径有效。
4. RELEASING.md 同步：artifact 格式、版本 SSOT、签名/SBOM 要求、packaged E2E 门禁状态。
5. 文档检查脚本（check-doc-links / check-doc-numbers）保持通过。

**AC：**

- [ ] 无越界宣称（grep 证据 + 人工复核）。
- [ ] README 能力矩阵按 V/S/U 标注，与代码一致。
- [ ] SECURITY/RELEASING 与修复后行为一致。

---

## 21. GOAL G-19（P2 推进项）：Remote 统一 Host / Language Pack / 插件协议边界锁定

**现状（证据，需复核）：** Remote 当前是最小 SSH/SFTP 工具（无远端 PTY/LSP/Git/Debug/Test broker、无 host identity/版本协商）；LSP server 发现硬编码（gopls/vtsls 等，无 manifest/SDK/离线包）；插件体系无版本化贡献协议。这三项是 prompt-6 P2-01 / P1-05 / P2-02 的架构级目标，**单会话不可完成**。

**本会话只做：**

1. 复核并标注现有 Remote / LSP / Plugin 代码中的 stub/prototype/mock 边界（文档与 UI 不得宣称完整能力）。
2. 产出架构蓝图（放 `docs/`）：统一 Host Client 协议草案（workspace URI + host identity、FS/watcher、PTY、SCM、Language broker、Debug/Test broker、edit transaction、journal/snapshot、断线语义）；Language Pack manifest/SDK 草案（字段清单见 prompt-6 §8）；插件贡献协议草案（E0-E5 分级）。
3. 蓝图不实现，仅记录依赖（P1-04R 事务、G-01 安全边界）与验收标准（引用 prompt-6 §8/§9/§10 的 AC）。

**AC：**

- [ ] 三份蓝图文档存在且标注"设计草案，未实现"。
- [ ] 现有 stub 边界在 README/文档中显式可见（无越界宣称）。
- [ ] 不新增实现代码（除文档外无 diff）。

---

## 22. GOAL G-20（P3 标注项）：SLO / 外部审计标注与策略

**现状：** prompt-6 §11 已明确：crash-free/升级成功率/安全 SLO 数据与外部审计**在任何单次编码会话内都不可能产生真实证据**。

**本会话只做：**

1. SECURITY.md / README 增加明确声明：无 SLO 数据、无外部安全/供应链/可访问性审计，状态 `U`（不伪造）。
2. 记录启动 SLO 数据收集的埋点清单（如 crash-free 需要真实发布历史），作为未来 release 的输入，不实现。

**AC：**

- [ ] 文档声明存在且措辞诚实（无"企业就绪"）。
- [ ] SLO 数据收集条件写入 RELEASING.md（S）。

---

## 23. 统一 Definition of Done（继承 prompt-6 §12，强化）

### 23.1 范围与实现
- [ ] 每个 Goal 开始前复核源码与测试，交付中写明"仍存在 / 已变化 / 已不存在"。
- [ ] 一次只完成一个 Goal；无无关大改、无 major 升级、无删除测试。
- [ ] 主路径、失败路径、取消/清理路径均有实现与测试。
- [ ] UI、backend、binding、类型、文档保持一致。

### 23.2 安全与数据
- [ ] 无 renderer 布尔抬权，无公开危险 root setter（全部 `//wails:ignore` + AST 测试）。
- [ ] 无 token / 过期 / 重放 / 跨参数 / 跨 generation / 跨 epoch 请求被接受。
- [ ] pathsec、symlink、SSRF、secret、日志脱敏不回归。
- [ ] 写入 / 恢复有 precondition、原子写、失败恢复语义。
- [ ] goroutine / listener / timer / process / Worker 有对称清理。

### 23.3 自动化与验证
- [ ] 新行为有单元或集成测试；安全修改有绕过失败测试（测试名可引用）。
- [ ] 相关 Go tests、Vitest、typecheck、lint **实际通过**，否则标 `S / U`。
- [ ] 触及导出 Go API 时检查 Wails bindings（`node scripts/check-bindings.mjs`）。
- [ ] 触及 docs / 常量时运行文档检查。
- [ ] 真实平台、签名、远程、LSP、packaged E2E 未跑时明确保留风险。
- [ ] 修复后 Go 覆盖率不低于修复前基线（≈69.7%）。

### 23.4 最终验收门禁（全部 P0/P1 完成后的整体自检）
- [ ] 8 项基线命令全绿（§24 命令表，逐条附退出码）。
- [ ] `npm test` 0 失败。
- [ ] `npm audit --audit-level=high` 无 High/Critical（或逐条记录）。
- [ ] AST setter 扫描通过（G-01 产出脚本）。
- [ ] 版本一致性测试扩展后 pass（G-09）。
- [ ] `bash -n` 通过 build 脚本与 workflow 片段（G-08）。
- [ ] 仓库卫生清单完成（G-17）。

---

## 24. AI 人格验收标准（详细）

执行 AI 的**行为**本身按以下标准验收；每项都要在交付模板 §26.3 自检表中逐条勾选并附证据。任意一条不达标视为本会话不合格。

### 24.1 诚实人格（Honesty）
1. `V / S / U` 严格分级：未运行的命令必须写"未运行（原因）"，禁止把 S/U 写成 V。
2. 禁止伪造证据：不把 mock / contract-smoke 写成 packaged E2E；不把"源码存在"写成"能力可用"；不把"测试通过"写成"真实平台验证"。
3. 不删测试保绿、不弱化审批；失败测试只能通过修实现修复（G-10 的 4 个失败测试是修实现，不是删测试）。
4. 阻塞时如实记录阻塞原因与复现步骤，然后继续下一个可执行 Goal；**不硬编造完成**。
5. 交付中每个 AC 勾选必须有可复现证据：测试名 / 命令输出 / 文件 diff 摘要，不允许裸勾。

### 24.2 严谨人格（Rigor）
1. 开始任何 Goal 前先复核现状（读代码 + grep），交付写明"仍存在 / 已变化 / 已不存在"。
2. 安全修改必须"测试先红后绿"：先写失败测试证明漏洞/缺口存在，再修复，再证明测试转绿。
3. 每个代码改动后立即跑相关测试切片，Goal 收尾前跑受影响的层全量；全量放最后是欺诈，不算严谨。
4. 不使用 `as any` / `@ts-ignore` / `@ts-expect-error`；不写空 catch。
5. 不 shotgun debugging；同一问题 3 次修复失败 → 回滚到最后可用状态、记录、请求决策。

### 24.3 最小人格（Minimalism）
1. 只改 Goal 内文件；新增/删除文件必须写明理由。
2. 不引入"可能有用"的抽象；不顺手重构无关模块；不升 major 依赖。
3. 修复优先在既有机制上做（复用 `WorkspaceContext` / 统一事务 / Recovery baseline），不另起炉灶。

### 24.4 安全人格（Security）
1. 安全默认拒绝：执行、文件、网络、凭据、扩展、Agent、MCP、Remote、更新一律 fail-closed。
2. 不信任 renderer：`approved` / `confirmed` / `safe` / `targetPath` / `allowPrivateNetwork` 不得抬权。
3. 空 root = 拒绝，不放过任意路径；symlink 逃逸保持拒绝。
4. 不提交 secret；日志脱敏（路径/内容/凭据）不回归。

### 24.5 纪律人格（Discipline）
1. 一次只做一个 Goal；todo 实时更新；完成一个标记一个（用 todo 工具，不用口头宣布）。
2. 不擅自 commit / push；不修改 git config；不跳过 hooks。
3. 遵守 §25 命令退出码规则（`cmd; echo "EXIT=$?"`，不用管道尾）。
4. 每个 Goal 结束立即回写 §26 进度板，不攒到最后。

### 24.6 透明人格（Transparency）
1. 报告每个命令实际结果（pass / fail / 未运行+原因）。
2. 标注全部 U 项与本地/CI 复现步骤。
3. 发现与审计结论不符（例如某问题已不存在）时，在交付中明确"已变化"，而不是照抄本文件。

### 24.7 效率人格（Efficiency）
1. 验证批处理与并行（独立命令并行发、相关命令合并），减少往返。
2. WSL 慢文件系统：Go/npm 验证在 `/tmp/koyori-ide-audit` 副本跑（§25.4），改动同步回 `/mnt/c` 后复核 diff。
3. 已确认结论直接引用证据，不重复读已读文件；新探索用 grep 精确定位。

### 24.8 自检人格（Self-verification）
1. 交付前对照 §23 DoD 逐项自检；§23.4 最终验收门禁全绿才允许写"本次修复闭环"。
2. 每个 Goal 的 AC 勾选必须附证据（测试名 / 命令输出 / diff 摘要）。
3. 最终输出明确结论：闭环 / 部分闭环（列出缺口）/ 未闭环（列出原因），禁止模糊表述。

---

## 25. 长任务指引（会话管理）

### 25.1 推荐会话结构
1. **阶段 A（环境准备，~15 分钟）：** 复核 §1 基线（抽查 2-3 条命令确认环境未变）；准备 `/tmp/koyori-ide-audit` 副本（见 §25.4）。
2. **阶段 B（P0，G-01 → G-09）：** 按依赖顺序执行；每个 Goal 完成 = AC 全绿 + 证据落账。
3. **阶段 C（P1，G-10 → G-18）：** 同序执行；G-16 的 U 边界如实标注。
4. **阶段 D（边界锁定，G-19 / G-20）：** 只写文档，不实现。
5. **阶段 E（最终验收）：** 跑 §23.4 全部门禁 → 按 §26 交付 → 回写进度板。

### 25.2 断点与续作
- 每个 Goal 完成立即：更新 §26 进度板 + 记录证据（命令 + 退出码 + 测试名）。这就是本会话的检查点。
- 会话中断/超时：新会话先读 §26 进度板与上次交付模板，从第一个"未开始 / 进行中"的 Goal 继续；**不重做已完成 Goal**。
- 证据明细建议追加到仓库外（如 `/tmp/koyori-ide-audit/PROGRESS.md`）或会话内摘要，避免污染仓库；进度板只放结论。

### 25.3 时间盒与停止条件
- 每个 Goal 设时间盒（P0 每项 ≤ 1.5 小时，P1 每项 ≤ 1 小时，超时评估继续或降级）。
- 遇到需要真实平台 / 凭据 / CI 历史的事项 → 立即标 `U` 并记录复现步骤，不硬闯。
- 3 次连续修复失败 → 回滚 + 记录 + 暂停该 Goal（向用户报告，不自行扩大范围）。
- **停止规则：** 用户未要求 commit 时绝不 commit；本文件任务全部完成（或用户叫停）即停，不自动扩展后续 Goal。

### 25.4 环境纪律（实测）
- 仓库位于 WSL 挂载的 Windows NTFS（`/mnt/c/...`），Go 编译与前端测试极慢，全量会超时。
- **做法：** `rsync -a --exclude node_modules --exclude .git` 同步到 `/tmp/koyori-ide-audit`，在原生 fs 跑测试；改动写回 `/mnt/c` 原仓库后 `diff -rq`（排除 node_modules/.git）复核一致性。
- `node_modules` 若在 Windows 下装过，缺 Linux 原生 binding，需在副本重新 `npm ci`。
- Go 在 `/usr/local/go/bin/go`（1.25.0），需要时加 `PATH`。
- 空 `.git/` → `go build` 需 `-buildvcs=false`。
- `wails3` CLI 未安装 → packaged build / E2E 一律 `U`（G-16 只做代码与单测）。

### 25.5 验证批处理
- 先跑相关切片，再跑受影响层全量；最后跑 §24 门禁全量。
- 退出码：`cmd; echo "EXIT=$?"`；**禁止** `cmd | tail; echo $?`（会取到 tail 的退出码）。
- `npm test` 已是 `vitest run`，不要再传 `-- --run`。
- 长命令分块或后台，避免工具超时；并行命令互不依赖时同时发起。

### 25.6 上下文预算
- 已确认结论引用证据即可，不重复读；grep 精确定位，避免全文件扫描。
- 每个 Goal 聚焦相关文件清单（§2 表已给），不浏览无关模块。
- 交付精简：只列证据、差异、风险；不复制大段代码。

---

## 26. 环境与验证命令

### 26.1 基线命令（§1.1 已通过，改动后需复跑受影响项）

```bash
# 后端
go test ./services/ -count=1
go test . -count=1
go vet ./services/... .

# 安全切片（每个安全 Goal 完成后必跑）
go test ./services/ -race -count=1 -run 'Agent|MCP|ComputerUse|IM|Remote|Path|Snapshot|Goal|Recovery|Workspace'

# 覆盖率基线（修复后不得低于修复前）
go test ./services/ -coverprofile=/tmp/cover.out -count=1 && go tool cover -func=/tmp/cover.out | tail -1

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

### 26.2 新增门禁命令（本文件引入）

```bash
# 依赖安全（G-11）
npm audit --audit-level=high --registry=https://registry.npmjs.org

# 发布脚本语法（G-08/G-09，S 级）
bash -n build/scripts/build-macos.sh && bash -n build/scripts/build-linux.sh
bash -n .github/workflows/release.yml 2>/dev/null || echo "workflow YAML 需用 actionlint 或 CI 校验（S）"

# 版本一致性（G-09，release_version_test.go 扩展后）
go test -run 'Version|Release' . -count=1

# AST root setter 扫描（G-01 产出脚本，如 scripts/check-root-setters.mjs 或 Go AST 测试）
# 断言 services/ 中 SetWorkspaceRoot*/SetProjectRoot 均带 //wails:ignore

# 仓库卫生（G-17）
ls -la /mnt/c/Users/Cute_/Downloads/Koyori IDE-main | grep -Ei 'koyori-ide\.exe|NUL|\$profile|\.claude' || echo "clean"
```

### 26.3 阻塞与环境重试（U 项复现命令）

```bash
# govulncheck（外网恢复后重试）
cd /tmp/koyori-ide-audit && go run golang.org/x/vuln/cmd/govulncheck@latest ./...

# packaged E2E（装好 wails3 + GUI runner 后）
cd /tmp/koyori-ide-audit && node scripts/packaged-e2e.mjs
```

---

## 27. 进度板（每次会话结束回写）

| Goal | 主题 | 状态 | 证据 |
|---|---|---|---|
| G-01 | root setter 收敛 / 空 root fail-closed | 已完成 | 复核：已变化但仍存在；新增 root_setter_boundary_test.go / root_boundary_test.go / g01_remaining_boundary_test.go；AST setter 扫描、空 root File/Search/Git/Agent/Terminal/Coverage/Debug 绕过测试先红后绿；ProjectService 协调清理含 Coverage/Eslint；Go services/root/security race/root 包/vet 全部 EXIT=0，覆盖率 71.1%，前端受影响 3 文件 15 测试 EXIT=0，vue-tsc/lint/build/bindings EXIT=0；源码无直接 setter 调用；/tmp 副本一致（排除 .omo/node_modules/.git） |
| G-02 | Agent write capability + 事务 | 已完成 | 复核：已变化但仍存在；并发基线已有后端 write token/测试与 bindings，补齐批准时 baseline、目标存在性、UTF-8 size 绑定及前端 capability 接线；伪造/重放/过期/改路径/改 hash/改 size/跨 generation/越界/批准后磁盘变化均拒绝，Agent 不再直调 FileService.WriteFile；Diff 已走统一事务，Plan/Goal 当前仅命令 executor、无文件写入口（已不存在，不虚构功能）；Go services/root/race/vet、前端 Agent 107 测试/typecheck/lint/build、bindings 全部 EXIT=0，覆盖率 71.2%，/tmp 一致 |
| G-03 | 原子保存 + baseline 冲突 | 未开始 | |
| G-04 | 多文件替换事务接入 | 未开始 | |
| G-05 | Recovery 启动闭环 | 未开始 | |
| G-06 | AllowPrivateNetwork 后端签发 | 未开始 | |
| G-07 | 命令入口 workspace 绑定 | 未开始 | |
| G-08 | release.yml 产物/架构修复 | 未开始 | |
| G-09 | 平台元数据 SSOT | 未开始 | |
| G-10 | Settings 测试 + 深链/双窗口 | 未开始 | |
| G-11 | npm audit High 消除 | 未开始 | |
| G-12 | .code-workspace 多根接线 | 未开始 | |
| G-13 | Worktree/Rebase 注册接线 | 未开始 | |
| G-14 | Debug/Test 入口 | 未开始 | |
| G-15 | AI Diff apply 落盘 | 未开始 | |
| G-16 | packaged E2E hook + driver | 未开始 | |
| G-17 | 仓库卫生 + NOTICE/SBOM | 未开始 | |
| G-18 | 文档诚实定位 | 未开始 | |
| G-19 | P2 边界锁定（蓝图） | 未开始 | |
| G-20 | SLO/审计标注 | 未开始 | |

---

## 28. 交付模板（每次会话结束必填）

```markdown
## Goal 完成情况
- 本会话完成：G-__（逐个）
- 部分完成：G-__（列出缺口）
- 未完成：G-__（原因：时间盒 / 阻塞 / 依赖）

## 复核结论（每个完成的 Goal 必须写）
- G-__：问题仍存在 / 已变化（变化说明） / 已不存在

## 改动
- 文件：逐个列出（含新增/删除及理由）
- 行为：每个 Goal 的最终行为摘要
- 明确未做：显式列出

## AC 证据
- 每个 Goal 的 AC 勾选，逐条附：测试名 / 命令输出摘要 / diff 摘要
- 安全 Goal 的绕过失败测试名必须列出

## 命令与结果
- `command` -> pass / fail / 未运行（原因）
- 含 §26.2 新增门禁逐条

## U 项与风险
- 未验证清单（含复现步骤）
- 回归风险说明

## AI 人格自检表（§24，逐条勾选）
- 诚实（V/S/U 分级、无伪造、未删测试）：□
- 严谨（先复核、先红后绿、切片先行、无类型压制）：□
- 最小（范围内、无多余抽象）：□
- 安全（fail-closed、无抬权、无 secret、空 root 拒绝）：□
- 纪律（一次一个、todo 实时、无 commit）：□
- 透明（命令结果逐条、U 项标注、变化说明）：□
- 效率（批处理、副本验证、引用证据）：□
- 自检（§23 DoD 全过、门禁全绿才写闭环）：□

## 整体结论
- 闭环 / 部分闭环（缺口清单）/ 未闭环（原因）
- 一句话定位更新：开源准备度 / 发布 readiness 现状

## SSOT 回写
- §27 进度板状态：
- 日期与证据：
- 是否发现已完成基线（§1）回归：
```

---

## 29. 一键启动词

```text
请严格按仓库根目录 prompt-7.md 执行。
先读 §0、§1、§2、§23、§24、§25；按 §2 顺序从第一个"未开始"的 Goal 开始，一次只做一个。
开始前复核代码与测试（写明"仍存在/已变化/已不存在"），安全修改先写失败测试再修复。
遵守 §24 AI 人格验收标准与 §25 长任务指引（副本验证、退出码规则、时间盒）。
每个 Goal 完成立即回写 §27 进度板并记录证据；结束时按 §28 交付，更新 §27。
G-19 / G-20 只做边界锁定与标注，不实现。
不要 commit，除非我明确要求。
```

---

## 30. 最终产品目标（不变，继承 prompt-6 §17）

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

*文档结束。执行入口以本文件为准；已完成基线与历史安全要求见 prompt-6.md、prompt-5.md、prompt-4.md、prompt-1.md。*
