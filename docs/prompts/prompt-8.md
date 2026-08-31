# Koyori IDE 断点续作 Goal 任务（prompt-8，SSOT 续作）

> **用途：** prompt-7 会话中断后的**断点续作清单**。本文件是唯一入口：先读 §0~§3（断点与当前事实），然后按 §4 执行顺序从第一个"未开始/进行中"的 Goal 开始，一次只做一个。全部 Goal 完成后按 §30 交付模板输出。
> **事实优先级：** 当前代码与实际命令结果 > 本文件 > prompt-7.md > prompt-6.md > prompt-5.md。
> **仓库基线：** Go 1.25 + Wails v3 alpha2.111 + Vue 3 + TS + Vite + Monaco。定位 0.x Go/TS 垂直桌面 AI IDE，**不得宣称生产级 / 企业就绪 / 完整 Remote-SSH / VS Code 或 Cursor 替代品**。

---

## 0. 总指令（继承 prompt-7 §0，不可弱化）

1. **先读代码再接受结论。** 本文件每条"现状/证据"是线索，不是真理。开始每个 Goal 前必须打开实现与测试复核，并在交付里写明"仍存在 / 已变化 / 已不存在"。行号可能因修复漂移，以实际代码为准。
2. **一次只做一个 Goal。** 按 §4 顺序执行；完成一个（AC 全绿 + 证据落账到 §29 进度板）才允许开始下一个。完成后停止，不自动越界扩展。
3. **最小正确改动。** 不重构无关模块，不升 major 依赖，不为假设需求加代码，不新增"可能有用"的抽象。
4. **诚实分级 `V / S / U`。** `V` = 本机命令实际通过；`S` = 源码/测试存在但本机未运行；`U` = 需外部环境、凭据、真实平台或 CI 历史。**禁止把 S 或 U 写成 V，禁止伪造证据。**
5. **安全默认拒绝。** 执行、文件、网络、凭据、扩展、Agent、MCP、Remote、更新一律 fail-closed，并加绕过失败测试。
6. **不信任 renderer。** 前端传来的 `approved` / `confirmed` / `safe` / `targetPath` / `allowPrivateNetwork` 不得抬权。高风险能力由后端签发、绑定参数、短时、单次使用。
7. **保护用户数据。** 恢复、快照、多文件编辑、更新不得用 partial result 覆盖磁盘新版本或静默丢数据。
8. **不删测试保绿、不弱化审批、不提交 secret、不擅自 commit / push。**
9. **环境阻塞 ≠ 项目失败。** 阻塞时记录阻塞原因、已完成的源码检查、可复制的复现命令，然后继续下一个可执行的 Goal。
10. **文档必须与能力一致。** stub / prototype / mock / contract-smoke / 手动更新 / 最小远程等边界必须显式可见。
11. **不可回归基线：** prompt-1 的 capability token、MCP root、IM 批准、扩展权限、pathsec、更新边界；prompt-5 的 `WorkspaceContext`、`RecoveryService`、Agent budget epoch、Snapshot 精确语义、Goal prototype 网关；prompt-6 的 `workspace_edit_transaction.go`；prompt-7 的 G-01/G-02/G-03 后端（见 §2）。
12. **并发修改警告：** 本仓库可能存在另一条并行修复流在工作。每次编辑前必须重新读取目标文件确认当前上下文（`apply_patch` 失败 = 上下文已变化，重读后再改）；**不覆盖他人改动，不重复实现已存在的能力**。
13. **证据纪律：** 每个改动完成立即记录证据（命令输出 / 测试名 / diff 摘要）到 §29 进度板与交付模板；不许到最后才补记。

---

## 1. 环境与命令（实测，2026-08-03）

### 1.1 环境限制（U，不得谎报）
- 本机**无 `wails3` CLI** → packaged build / packaged E2E 一律 `U`；binding 由手工维护 + `check-bindings.mjs` 门禁。
- 本机**不是 Windows/macOS** → Windows/macOS 打包、签名、公证、启动一律 `U`。
- 仓库 `.git/` 为空目录 → 无法确认 tracked 文件、tag/release/CI 历史（release workflow 只读 YAML = `S`）。
- `govulncheck` 因外网 EOF 失败 → Go 漏洞扫描标 `U`，记录重试命令（§1.3）。
- 真实 SSH 集成、packaged kill/restart 恢复演练、Monaco long-task trace → `U`。
- **`gopls` 与 `typescript-language-server` 未安装**，用户已拒绝安装 → LSP 诊断不可用，Go/TS 验证靠编译+测试+类型检查命令。

### 1.2 验证纪律（继承 prompt-7 §25，实测有效）
- 仓库在 WSL 挂载的 NTFS（`/mnt/c/...`），Go/npm 验证**必须**在 `/tmp/koyori-ide-audit` 原生 fs 副本跑：
  ```bash
  rsync -a --delete --exclude node_modules --exclude .git "/mnt/c/Users/<用户名>/Downloads/Koyori IDE-main/" "/tmp/koyori-ide-audit/"
  ```
  改动写回 `/mnt/c` 原仓库后重新同步再验证。`node_modules` 在 Linux 下需重新 `npm ci`（副本已有则跳过）。
- 退出码：`cmd; echo "EXIT=$?"`；**禁止** `cmd | tail; echo $?`（取到的是 tail 的退出码）。管道场景用 `${PIPESTATUS[0]}`。
- Go 路径：`PATH="/usr/local/go/bin:$PATH"`（1.25.0）；空 `.git/` 下 `go build` 需 `-buildvcs=false`（仅打包时）。
- `npm test` 已是 `vitest run`，不要再传 `-- --run` 或 `--runInBand`。
- 时间盒：P0 ≤ 1.5h，P1 ≤ 1h；3 次连续修复失败 → 回滚 + 记录 + 暂停该 Goal 报告。
- 停止规则：用户未要求 commit 时绝不 commit；全部完成（或用户叫停）即停。

### 1.3 关键命令表（改动后复跑受影响项）
```bash
# 后端
PATH="/usr/local/go/bin:$PATH" go test ./services/ -count=1
PATH="/usr/local/go/bin:$PATH" go test . -count=1
PATH="/usr/local/go/bin:$PATH" go vet ./services/... .
PATH="/usr/local/go/bin:$PATH" go test ./services/ -race -count=1 -run 'Agent|MCP|ComputerUse|IM|Remote|Path|Snapshot|Goal|Recovery|Workspace|Root|Write'
PATH="/usr/local/go/bin:$PATH" go test ./services/ -coverprofile=/tmp/cover.out -count=1 && PATH="/usr/local/go/bin:$PATH" go tool cover -func=/tmp/cover.out | tail -1
# 前端
cd /tmp/koyori-ide-audit/frontend && npm test
cd /tmp/koyori-ide-audit/frontend && npx vue-tsc --noEmit
cd /tmp/koyori-ide-audit/frontend && npm run lint
cd /tmp/koyori-ide-audit/frontend && npm run build
# 工程检查
cd /tmp/koyori-ide-audit && node scripts/check-bindings.mjs
cd /tmp/koyori-ide-audit && node scripts/check-doc-links.mjs && node scripts/check-doc-numbers.mjs && node scripts/check-wails-pin.mjs
cd /tmp/koyori-ide-audit && node scripts/contract-smoke.mjs
cd /tmp/koyori-ide-audit && node scripts/packaged-e2e.mjs --dry-run
# 阻塞重试（外网恢复后）
cd /tmp/koyori-ide-audit && PATH="/usr/local/go/bin:$PATH" go run golang.org/x/vuln/cmd/govulncheck@latest ./...
```

---

## 2. 断点快照（2026-08-03 实测，SSOT）

### 2.1 已完成并验证（V）
- **G-01（root setter 收敛 / 空 root fail-closed）—— 完成。** 证据见 prompt-7.md §27 G-01 行；本会话实测全绿：`go test ./services/`、`go test .`、`go vet`、安全 race 切片、`check-bindings.mjs`、`vue-tsc`、`npm run lint`、`npm run build` 均 EXIT=0；覆盖率 71.2%（≥ 基线 69.7%）。产物：`services/root_boundary_test.go`、`services/g01_remaining_boundary_test.go`、`services/project_workspace_clear.go`、`pathsec.go` 的 `ValidateMutatingPathWithinRoot`、全部 root setter `//wails:ignore`、生成 bindings 移除 root setter 导出、前端无直接 setter 调用。
- **G-02（Agent 写文件后端 capability + 统一事务）—— 实现完成。** 产物：
  - `services/agent_write_approval.go`：`RequestWriteApproval(targetPath, contentHash, size)` + `ExecuteApprovedWrite(targetPath, content, token)`；token 绑定（解析后绝对路径 + content hash + workspace generation + TTL 2min，单次使用）；审批时捕获磁盘 baselineHash，执行时重读比对（磁盘被外部修改则拒绝不覆盖）；写盘走 `applyEditTransaction`（LIFO 回滚语义）。
  - `services/agent_service.go`：新增 `writeApprovalMu` / `writeApprovals` / `approveWrite` 字段。
  - 测试：`services/agent_write_approval_test.go`（11 例：空 root、越界、拒绝、空/伪造/重放/跨 generation/改路径/hash 不符/size 不符/过期/成功路径）、`services/agent_write_transaction_test.go`（磁盘变更拒绝等事务契约）。
  - 前端：`frontend/bindings/koyori-ide/services/agentservice.ts` 新增 `RequestWriteApproval`(ByID 1551814169) / `ExecuteApprovedWrite`(ByID 2198789113)；`frontend/src/api/automation.ts` facade；`frontend/src/stores/agent.ts` `executeWriteTool` 改为 请求审批→执行 流程（SHA-256 绑定内容）；`scripts/check-bindings.mjs` required 增加两项。
- **G-03 后端（原子保存 + baseline 冲突）—— 实现完成。** 产物：`services/file_save_integrity.go`（`ErrFileConflict`、`WriteFileIfUnchanged(path, content, baselineHash)`、`FileService.writeAtomic` 可注入原子写字段）、`services/file_save_integrity_test.go`（5 例：原子写失败保留原文件、保留原权限位、baseline 匹配写入、baseline 冲突拒绝、越界拒绝）。**Go 侧全绿。**

### 2.2 最终续作状态（2026-08-03）
- **G-03 前端接线已完成。** `writeFileIfUnchanged` binding/facade、打开时 baseline、冲突保持 dirty、覆盖/重新加载 UI 与 save-all 聚合均已接线；原 3 个红测及相关 EditorView 测试已通过。G-03 至 G-20 的逐项状态与证据见 §29，最终交付见 §30。

### 2.3 已知失败处理结果
- 原 `SettingsView.test.ts` 4 个失败已在 G-10 修复；原 editor 3 个失败已在 G-03 修复。最终前端全量没有失败测试，仍存在的 warning/noise 见 §30，不将 warning 写成零告警。

### 2.4 最终前端测试汇总（实测 2026-08-03）
`161 文件 / 2603 测试 / 0 fail`，`npm test` EXIT=0；`vue-tsc`、lint、build 与官方 registry npm audit 均 EXIT=0。

### 2.5 仓库卫生处理结果
根目录 `koyori-ide.exe`、`NUL`、`$profile` 已删除并由 `.gitignore` 覆盖；`NOTICE` 与 `docs/THIRD_PARTY_LICENSES.md` 已生成。`.claude/` 保留但已 ignore，`settings.local.json` 仅含顶层 `permissions` 且敏感词文件扫描无命中；实际 SBOM 因 Docker Hub 超时仍为 `U`。

---

## 3. 手工维护 Wails binding 的 ByID 计算法（无 wails3 CLI 时用）

新增导出的 Go 服务方法必须同步 `frontend/bindings/koyori-ide/services/<svc>.ts`。ByID = FNV-1a 32-bit（`hash/fnv`，offset 2166136261 × prime 16777619）作用于 FQN `koyori-ide.services.<TypeName>.<Method>`：

```bash
cat > /tmp/fnv.go << 'EOF'
package main
import ("fmt"; "hash/fnv")
func h(s string) uint32 { x := fnv.New32a(); x.Write([]byte(s)); return x.Sum32() }
func main() { fmt.Println(h("koyori-ide.services.AgentService.RequestWriteApproval")) }
EOF
PATH="/usr/local/go/bin:$PATH" go run /tmp/fnv.go
```

- 已用此法验证：`RequestWriteApproval=1551814169`、`ExecuteApprovedWrite=2198789113`（实测匹配）。
- 改完 binding 后必跑 `node scripts/check-bindings.mjs`（required 清单与 forbidden 清单都要同步）。

---

## 4. 目标总览与执行顺序

| ID | 主题 | 状态 | 会话范围 |
|---|---|---|---|
| G-03（续） | 前端 save 走 baseline 事务 + 冲突 UI | ✅ 已完成（V） | 完成剩余前端接线 |
| G-04 | 多文件替换/搜索前端接入统一事务 | ✅ 已完成（V） | 一次性完成 |
| G-05 | Recovery 启动扫描闭环接通 | ✅ 已完成（V/U） | 一次性完成 |
| G-06 | HTTPClient `AllowPrivateNetwork` 改后端签发 | ✅ 已完成（V） | 一次性完成 |
| G-07 | Coverage/Toolchain/ESLint/Debug 等命令入口绑定 WorkspaceContext | ✅ 已完成（V） | 一次性完成 |
| G-08 | release.yml 产物格式与架构矩阵修复 | ✅ 已完成（S/U） | 一次性完成 |
| G-09 | 平台元数据统一从 VERSION SSOT 生成 | ✅ 已完成（V/S/U） | 一次性完成 |
| G-10 | Settings 测试修复 + 设置收敛深链/双窗口测试 | ✅ 已完成（V） | 一次性完成 |
| G-11 | npm audit High 依赖消除 | ✅ 已完成（V） | 一次性完成 |
| G-12 | `.code-workspace` 多根接线 `AddMultiRootProject` | ✅ 已完成（V） | 一次性完成 |
| G-13 | Git Worktree / 交互式 Rebase 服务注册与 UI 接线 | ✅ 已完成（V） | 一次性完成 |
| G-14 | Debug / Test Explorer 普通入口 | ✅ 已完成（V） | 一次性完成 |
| G-15 | AI Diff apply 结果可靠落盘与编辑器同步 | ✅ 已完成（V） | 一次性完成 |
| G-16 | packaged E2E automation hook + driver 推进 | ✅ 源码完成（V/S/U） | 一次性完成（U 边界如实标注） |
| G-17 | 仓库卫生 + NOTICE / 许可证清单 / SBOM 流水线 | ✅ 源码完成（V/S/U） | 一次性完成 |
| G-18 | README / SECURITY / 发布文档诚实定位 | ✅ 已完成（V/S/U） | 一次性完成 |
| G-19 | Remote 统一 Host / Language Pack / 插件协议边界锁定 | ✅ 蓝图完成（S/U） | 只做边界与设计，不实现 |
| G-20 | SLO / 外部审计标注与策略 | ✅ 文档完成（S/U） | 只做标注与文档 |

**依赖关系：** G-03（续）→ G-04（事务复用）；G-04 → G-15；G-06 依赖 G-01 边界（已完成）；G-08 → G-09；G-16 独立；G-17/G-18 收尾。

---

## 5. GOAL G-03（续，P0）：前端保存走 baseline 事务 + 冲突 UI

**现状（需复核）：** 后端已就绪（§2.1 G-03）。`frontend/src/stores/editor.test.ts` 3 测试红：mock 了 `fileService.writeFileIfUnchanged` 但 store 未接线。`editor.ts` 的 `saveFilePath`（~L341）、`saveFile`（~L361）、`saveAllFiles`（~L375）需改；冲突常量/提示已部分存在（~L120, ~L205）。

**执行点：**
1. 后端确认 `WriteFileIfUnchanged` 已导出到 binding（`frontend/bindings/koyori-ide/services/fileservice.ts` 需含 `WriteFileIfUnchanged(path, content, baselineHash)`，ByID 用 §3 计算；缺失则手工补 + `check-bindings.mjs` required 增加）。
2. `frontend/src/api/workspace.ts` facade 增加 `writeFileIfUnchanged`。
3. `editor.ts`：保存时携带打开文件的 baseline hash（打开/加载时记录，或从打开时内容计算 SHA-256，与后端 `contentHash` 一致）；`saveFilePath` 改调 `writeFileIfUnchanged`；收到 `ErrFileConflict` 时**保持 dirty**（不丢用户 buffer）、标记冲突状态、呈现选择 UI（覆盖 / 重新加载），不静默覆盖。
4. `saveAllFiles`：逐文件走 baseline 写，失败保留错误并继续其余文件，汇总返回；不部分成功误导。
5. 3 个红测试转绿；既有 `file:saved` 事件测试仍 pass。

**AC：**
- [ ] `writeFileIfUnchanged` binding + facade 存在，`check-bindings.mjs` pass。
- [ ] editor.test.ts 3 测试绿：绑定打开时内容、冲突保持 dirty、saveAll 聚合失败。
- [ ] 冲突 UI 测试覆盖"覆盖 / 重新加载"两条路径（如已有测试则转绿）。
- [ ] `file:saved` 事件行为不回归。

---

## 6. GOAL G-04（P0）：多文件替换/搜索前端接入统一事务

**现状（需复核）：** `frontend/src/stores/search.ts` 的 `applySelectedPreviews`（~L183-212）逐文件循环调 `searchService.applyReplacePreview`，中途失败不整体回滚 → partial result。后端已有 `ApplyMultiFileReplaceTransaction`（`search_service.go:662`，走 `applyEditTransaction`，hash 前置检查 + LIFO 回滚 + `WorkspaceContext.RequireRoot`）。

**执行点：**
1. 复核后端事务完整支持（全部文件 hash 前置、任一冲突整体拒绝、LIFO 回滚、pathsec、空 root 拒绝）——应为已完成，只读确认。
2. 前端 `applySelectedPreviews` / `replaceAll` 改调批量事务 API，单次调用完成全部文件；失败时展示结构化错误（含哪些文件冲突），**不出现"部分成功"文案**。
3. 逐文件 `applyReplacePreview` 若保留（单文件场景）必须走同一 precondition。
4. 预览生成保持逐文件（只读），apply 必须事务化。

**AC：**
- [ ] 注入 hash 冲突时所有文件回到原内容（测试断言）。
- [ ] 任一文件失败时前端收到结构化冲突列表，无"部分成功"误导。
- [ ] 空 root / 路径逃逸测试 fail-closed。
- [ ] `replaceAll` 与 SearchPanel UI 路径接线测试通过。

---

## 7. GOAL G-05（P0）：Recovery 启动扫描闭环接通

**现状（需复核）：** `frontend/src/stores/recovery.ts` `scanRecoverable()`（~L208-226）已实现，但生产启动路径无调用点。后端 `RecoveryService` 已注册（`bootstrap_services.go:124`）。

**执行点：**
1. 生产启动流程（app 初始化 / 主窗口 ready / 首个 workspace 打开后）调用 `scanRecoverable()`。
2. `recoveryPending > 0` 时展示恢复 UI（clean 恢复、conflict 呈现选择不覆盖磁盘新版本、missing 提示）；`finishRecovery()` 后才恢复 auto-save。
3. 扫描失败静默降级不阻塞启动但 UI 可见提示。
4. 启动接线测试：mock binding 断言启动调用 scanRecoverable 且 pending>0 进入恢复流程。
5. kill -9 / restart 演练标 `U`，在 `scripts/packaged-e2e.mjs` 记录 fixture 计划。

**AC：**
- [ ] 启动代码存在 `scanRecoverable()` 调用（grep + 测试）。
- [ ] pending>0 时恢复 UI 出现；conflict 不自动覆盖磁盘。
- [ ] 扫描异常不阻塞启动（测试）。
- [ ] G-16 的 recovery fixture 计划已写入 packaged-e2e.mjs 文档/注释。

---

## 8. GOAL G-06（P0）：HTTPClient `AllowPrivateNetwork` 改后端签发

**现状（需复核）：** `services/http_client_service.go:107-126` `SendRequest` 用 renderer 传入的 `options.AllowPrivateNetwork`（`http_client_model.go:32`、`HTTPClientPanel.vue`）决定 SSRF 校验 → 前端布尔抬权。

**执行点：**
1. 移除 renderer 布尔对 SSRF 策略的直接影响；实现 `RequestPrivateNetworkAccess(targetOrigin)` → 一次性 token（绑定 origin + 请求 ID + TTL，单次使用）；`SendRequest` 携带 token 时后端复核后才允许非公网目标。token 缺失/过期/跨 origin 拒绝。
2. redirect 校验与初始 URL 同一策略（同一 token 生命周期内允许，跨 origin 重定向仍需校验）。
3. UI：私网开关改为"请求后端授权"；授权被拒显示明确错误。
4. 现有 SSRF 测试（公网放行、私网默认拒绝、redirect 逃逸）保持通过。

**AC：**
- [ ] 无 token 时私网目标一律拒绝（测试）。
- [ ] token 绑定 origin/请求/TTL；重放、跨 origin、过期全部失败（测试）。
- [ ] `HTTPClientPanel.vue` 不再直接传 `allowPrivateNetwork` 布尔抬权。
- [ ] SSRF 基线测试无回归。

---

## 9. GOAL G-07（P0）：命令/启动入口统一绑定 WorkspaceContext

**现状（需复核）：** `CoverageService.RunPackageCoverage`（G-01/G-02 已部分修复）、Toolchain 命令、ESLint 运行、`DebugService` launch（`debug_launch.go`，G-01 已注入 context）、Terminal exec、Agent run 的 root 来源需在**调用时刻**从 `WorkspaceContext.RequireRoot()` 取，而非缓存启动值。

**执行点：**
1. 逐一复核入口：root 是否调用时刻取自 `WorkspaceContext.RequireRoot()`、空 root 拒绝、参数路径经 `ValidatePathWithinRoot`。
2. 未绑定的改为调用时刻绑定；删除启动时缓存 root 的死字段。
3. 每入口加测试：空 root 拒绝、跨 generation 拒绝、路径逃逸拒绝。
4. debug launch 对项目提供的 executable 默认不自动启动（与 prompt-6 P1-05 §6 一致）。

**AC：**
- [ ] 所有命令/启动入口调用时刻绑定共享 root（grep/测试）。
- [ ] 空 root、跨 generation、路径逃逸各入口失败测试。
- [ ] debug launch 对项目 executable 有默认拒绝/显式确认策略与测试。

---

## 10. GOAL G-08（P0）：release.yml 产物格式与架构矩阵修复

**现状（需复核）：** `.github/workflows/release.yml` macOS 两 job 均未用 `matrix.arch`；Package 步骤用 `tar -czf` 打包却命名 `.zip`；版本校验未覆盖平台元数据。

**执行点：**
1. macOS 构建使用 `matrix.arch`；Linux/Windows 显式声明。
2. macOS 打包改 `ditto -c -k --keepParent` 生成真 zip，或 artifact 改名 `.tar.gz`（二选一，名称与格式一致）。
3. 打包步骤对产物缺失/多产物歧义 fail（显式单文件断言，弃 `head -1`）。
4. 版本校验扩展覆盖 Windows info.json、nfpm.yaml、Info.plist（或校验 G-09 生成结果）。
5. 只读 YAML 本机不可运行：`bash -n` + dry-run 推演标 `S`；实跑标 `U`（tag push 触发）。

**AC：**
- [ ] `bash -n` 通过；matrix 每 job 显式传递 arch（S 证据）。
- [ ] macOS artifact 名与打包格式一致。
- [ ] 版本校验覆盖全部平台元数据来源。
- [ ] `docs/RELEASING.md` 与 workflow 行为一致；CI 实跑标 `U`。

---

## 11. GOAL G-09（P0）：平台元数据统一从 VERSION SSOT 生成

**现状（需复核）：** 根 `VERSION`=0.2.0（SSOT），但 `build/darwin/Info.plist`（executable=koyori-ide.exe、bundle id=com.koyori-ide.app、0.1.0）、`build/windows/info.json`（0.1.0）、`build/linux/nfpm/nfpm.yaml`（0.1.0、vendor=koyoriIde、homepage=wails.io）、`build/scripts/build-*.sh`（grep 顶层 `version:` 失败回退 0.1.0）。

**执行点：**
1. 两 build 脚本版本读取改从根 `VERSION` 或 `info.version`；读不到 **fail** 不静默回退。
2. `Info.plist`：executable=koyori-ide、bundle id 正式反向域名、版本构建时注入。
3. `nfpm.yaml` vendor/homepage/version 修正；Windows info.json 从 SSOT 注入。
4. 扩展 `release_version_test.go`：断言平台元数据文件与 VERSION 一致。
5. 构建脚本 `bash -n` + 手动跑版本读取段（`V`）；打包段 `U`。

**AC：**
- [ ] 所有平台元数据文件版本与 VERSION 一致（新用例 pass）。
- [ ] Info.plist 无模板残留。
- [ ] build 脚本版本读取失败时 fail。
- [ ] 三平台元数据 CI 一致（S，实跑 U）。

---

## 12. GOAL G-10（P1）：Settings 测试修复 + 设置收敛深链/双窗口测试

**现状（需复核）：** `SettingsView.test.ts` 4 失败（AI section 已迁移到 `AiSettingsView.vue` SSOT，测试仍期望旧 section）。旧深链跳转无路由测试；`openAIDesktopWindow()` 失败被吞；双窗口一致性/并发保存无测试；schema 无版本化迁移测试；搜索命中已迁移项未验证。

**执行点：**
1. **先修 4 个失败测试**：改为断言 AI section 不在主设置出现、主 IDE 无第二个可写 AI 实例。
2. 路由测试：`settings?section=ai|agent|prompts|presets|computerUse` 每个旧深链断言"调用 openAIWindow + URL 重写 + 未渲染 AI 可写表单"。
3. `openAIDesktopWindow()` 失败路径：可见错误 + 重试入口。
4. 设置 schema 版本化 + 迁移函数：未知字段不被无关保存抹除、非法值回退并提示、升级不丢 provider/model/prompts/presets/permissions。
5. 双窗口同步：修改经响应式事件/版本号广播；并发保存 last-write-wins + 版本校验，旧窗口不覆盖新值（测试）。
6. 设置搜索支持"迁移项"类型：命中后打开 AI 窗口并深链到准确位置（测试）。
7. Computer Use / MCP / IM / 模型权限 / 数据发送边界安全文案与默认关闭策略不回归。

**AC：**
- [ ] `npm test` 0 失败（157 文件全绿，含 G-03 前端修复后）。
- [ ] 5 个旧深链各有路由测试（跳转 + URL 重写）。
- [ ] 双窗口并发保存不丢新值（测试）。
- [ ] schema 迁移测试覆盖升级不丢配置；未知字段不抹除。
- [ ] 设置搜索命中迁移项并打开准确位置（测试）。

---

## 13. GOAL G-11（P1）：npm audit High 依赖消除

**现状（需复核）：** `npm audit --audit-level=high --registry=https://registry.npmjs.org` 报 2 High：`brace-expansion`（GHSA-mh99-v99m-4gvg）、`postcss <=8.5.17`（GHSA-r28c-9q8g-f849）。

**执行点：**
1. `npm ls brace-expansion postcss` 定位引入链，升级到修复版或替换。
2. 间接依赖无修复版：记录缓解说明与可达性分析，不静默忽略。
3. 目标 0 high / 0 critical；medium 可记录。
4. 更新后在 `/tmp/koyori-ide-audit/frontend` 重跑 `npm ci && npm test && npm run build`。

**AC：**
- [ ] `npm audit --audit-level=high` 0 High/Critical（或逐条记录 + 可达性分析）。
- [ ] `npm ci` + `npm test` + `npm run build` 全绿。

---

## 14. GOAL G-12（P1）：`.code-workspace` 多根接线 `AddMultiRootProject`

**现状（需复核）：** 后端 `ProjectService.AddMultiRootProject`（`project_service.go:980`）已存在且有两阶段回滚；前端 grep 未发现调用点。

**执行点：**
1. 前端 `.code-workspace` 打开流程调用多根协调入口；所有根经 pathsec 校验；任一根无效整体失败。
2. 多根下 LSP workspaceFolders 推送、Search/File/SymbolIndex root 列表同步一致。
3. 测试：多根打开、单根退化、无效根整体拒绝、generation 切换原子性。

**AC：**
- [ ] 打开 `.code-workspace` 走统一多根入口（代码+测试）。
- [ ] 任一根无效整体拒绝，不部分生效。
- [ ] 多根下 LSP/Search/File 行为一致（测试）。

---

## 15. GOAL G-13（P1）：Git Worktree / 交互式 Rebase 服务注册与 UI 接线

**现状（需复核）：** `git_worktree_service.go`、`git_rebase_service.go` 已实现且有测试，但 `bootstrap_services.go` 未注册、appBundle 无字段 → UI 不可达。

**执行点：**
1. 注册两服务到 appBundle + wailsServices；同步 bindings/models/index（§3 计算 ByID）；`check-bindings.mjs` pass。
2. 前端 Git 面板加 worktree 列表/添加/移除/锁定入口 + 交互式 rebase 视图（todo 列表、reorder/drop/abort/continue、conflict 提示）。
3. safeRoots 配置沿用 `NewGitWorktreeServiceWithSafeRoots` 注入，root 与 WorkspaceContext 一致。
4. 测试：binding 存在性、前端 store/组件基础交互；真实 git 流程已有测试标 V。

**AC：**
- [ ] 两服务注册且 bindings 生成（check-bindings pass）。
- [ ] Git UI 有 worktree 与 rebase 普通入口（组件/路由证据）。
- [ ] worktree/rebase 既有后端测试全部仍 pass。

---

## 16. GOAL G-14（P1）：Debug / Test Explorer 普通入口

**现状（需复核）：** Debug 与 Test 能力存在，Activity Bar 缺普通入口，只能深链访问。

**执行点：**
1. Activity Bar 增加 Debug 与 Test Explorer 入口（图标 + 视图容器），与现有 deep link 指向同一视图（不复制实现）。
2. 空状态/loading/error 状态齐全；窄窗口不横向溢出；键盘可达。
3. 前端测试覆盖入口渲染与切换。

**AC：**
- [ ] Activity Bar 两个普通入口且可达对应视图。
- [ ] 视图状态齐全 + 键盘可达（测试）。

---

## 17. GOAL G-15（P1）：AI Diff apply 结果可靠落盘与编辑器同步

**现状（需复核）：** `DiffViewer.vue`（~L197-203）忽略 `applyFile`/`applyAll` 返回值；`stores/diff.ts`（~L243-269）只计算返回内容，未可靠写 model 与磁盘。后端 `DiffService.ApplyDiffTransaction` 已存在。

**执行点：**
1. `applyFile`/`applyAll` 走事务 API（复用 G-04 统一事务），返回成功文件、冲突列表、回滚信息。
2. apply 成功后更新 Monaco model（如已打开）、更新 dirty-buffer baseline、通知文件树刷新；失败/冲突显示结构化错误与选择。
3. `DiffViewer.vue` 消费返回值：成功 toast、失败/冲突提示。
4. 测试：成功同步 model/baseline；冲突路径显示选择；回滚后 UI 状态一致。

**AC：**
- [ ] apply 返回值被 UI 消费（代码证据）。
- [ ] 成功路径 model/baseline/文件树同步（测试）。
- [ ] 冲突/失败路径不静默、不覆盖（测试）。

---

## 18. GOAL G-16（P1）：packaged E2E automation hook + driver 推进

**现状（需复核）：** `scripts/packaged-e2e.mjs` 脚手架存在（无 driver 时 exit 1 已实测）；7 个核心 fixture 无 driver；本机无 wails3 → artifact 生产 `U`。

**执行点：**
1. 为 packaged build 增加仅测试构建标签启用的自动化端点（loopback + 一次性 token + 仅 `KOYORI_IDE_E2E=1` 监听）；加测试断言正式构建不含该端点。
2. driver 覆盖 7 个核心 fixture（open workspace、open file、edit、save、terminal 一条命令、LSP hover/completion、kill -9 后 restart 恢复）；失败上传日志/截图。
3. CI 配 Linux GUI runner（xvfb）+ Wails CLI；job 从 `workflow_dispatch` 转 required 前须稳定通过 3 次（写入门禁注释）。
4. artifact 记录 commit、checksum、runner 环境。
5. 本会话：hook + driver + fixture 代码完成并单测（V）；真实 packaged 运行 `U`。

**AC：**
- [ ] automation hook 仅测试构建存在（测试证明正式构建无该端点）。
- [ ] 7 个 fixture 的 driver 代码存在，dry-run/单元层面通过（V），真实运行标 U。
- [ ] CI job 配置完整（S），3 次稳定门槛写入门禁说明。
- [ ] Windows/macOS 明确标 `U`。

---

## 19. GOAL G-17（P1）：仓库卫生 + NOTICE / 许可证清单 / SBOM 流水线

**现状（需复核）：** 根目录 `koyori-ide.exe`、`NUL`、`$profile`、`.claude/`、`.agents/`、`.omo/`、`.task/`；无 `NOTICE`、无第三方许可证清单、无 SBOM。

**执行点：**
1. 移除工作区内 `koyori-ide.exe`/`NUL`/`$profile`（先确认未 tracked；`.git/` 空无法确认时按"应移除 + gitignore"处理并记录）；`.claude/settings.local.json` 检查 secret，有则提示轮换。
2. `.gitignore` 补充：`koyori-ide.exe`、`*.exe`（根）、`NUL`、`$profile`、`.claude/`、`.omo/`、`.task/`、bin/。
3. 新增 `NOTICE`：第三方组件/许可证汇总（Go deps + npm deps 工具生成 + 人工复核）。
4. release.yml 增加 SBOM/provenance 步骤（或 RELEASING.md 标注"稳定版发布需 SBOM"）。
5. 文档列出依赖许可证审查结果与例外。

**AC：**
- [ ] 上述文件移除且 gitignore 覆盖（证据）。
- [ ] `.claude/settings.local.json` 无 secret 残留（或已提示轮换）。
- [ ] `NOTICE` 与许可证清单存在（V/S）。
- [ ] SBOM 步骤在 release.yml（S），RELEASING.md 有说明。

---

## 20. GOAL G-18（P1）：README / SECURITY / 发布文档诚实定位

**现状（需复核）：** README 功能宣传需与真实能力对照（AI、Remote、Debug、LSP、插件）；SECURITY.md 已有 0.2.x best-effort 边界；RELEASING.md 未覆盖平台元数据与打包格式（G-08/G-09 后需同步）。

**执行点：**
1. 全仓 grep 修正宣称：`production|enterprise|生产级|企业就绪|完整 Remote-SSH|VS Code 兼容|替代品` → 能力矩阵（V/S/U 标注）。
2. README 增加"当前能力边界"表（本地编辑 V 部分 / Git / LSP / AI / Agent / Recovery / Remote 最小 / Debug / 插件 / 发布供应链，每项标注验证等级）。
3. SECURITY.md 明确：Wails v3 alpha、0.x、best-effort、无 SLO/外部审计（G-20 联动）；漏洞报告路径有效。
4. RELEASING.md 同步 artifact 格式、版本 SSOT、签名/SBOM 要求、packaged E2E 门禁状态。
5. `check-doc-links` / `check-doc-numbers` 保持通过。

**AC：**
- [ ] 无越界宣称（grep 证据 + 人工复核）。
- [ ] README 能力矩阵按 V/S/U 标注，与代码一致。
- [ ] SECURITY/RELEASING 与修复后行为一致。

---

## 21. GOAL G-19（P2 推进项）：Remote 统一 Host / Language Pack / 插件协议边界锁定

**现状：** Remote 是最小 SSH/SFTP（无远端 PTY/LSP/Git/Debug/Test broker）；LSP server 发现硬编码（gopls/vtsls 等）；插件体系无版本化贡献协议。**单会话不可完成，本会话只做：**
1. 复核并标注现有 stub/prototype/mock 边界（文档与 UI 不得宣称完整能力）。
2. 产出三份架构蓝图放 `docs/`：统一 Host Client 协议草案（workspace URI + host identity、FS/watcher、PTY、SCM、Language broker、Debug/Test broker、edit transaction、journal/snapshot、断线语义）；Language Pack manifest/SDK 草案；插件贡献协议草案（E0-E5 分级）。
3. 蓝图不实现，仅记录依赖与验收标准。

**AC：**
- [ ] 三份蓝图文档存在且标注"设计草案，未实现"。
- [ ] 现有 stub 边界在 README/文档显式可见。
- [ ] 不新增实现代码（除文档外无 diff）。

---

## 22. GOAL G-20（P3 标注项）：SLO / 外部审计标注与策略

**本会话只做：**
1. SECURITY.md / README 声明：无 SLO 数据、无外部安全/供应链/可访问性审计，状态 `U`。
2. 记录启动 SLO 数据收集的埋点清单（如 crash-free 需真实发布历史）作为未来 release 输入，不实现。

**AC：**
- [ ] 文档声明存在且措辞诚实（无"企业就绪"）。
- [ ] SLO 数据收集条件写入 RELEASING.md（S）。

---

## 23. 统一 Definition of Done（继承 prompt-7 §23）

- [ ] 每个 Goal 开始前复核源码与测试，交付写明"仍存在 / 已变化 / 已不存在"。
- [ ] 一次只完成一个 Goal；无无关大改、无 major 升级、无删除测试。
- [ ] 主路径、失败路径、取消/清理路径均有实现与测试。
- [ ] UI、backend、binding、类型、文档保持一致。
- [ ] 无 renderer 布尔抬权，无公开危险 root setter（全部 `//wails:ignore` + AST 测试）。
- [ ] 无 token / 过期 / 重放 / 跨参数 / 跨 generation / 跨 epoch 请求被接受。
- [ ] pathsec、symlink、SSRF、secret、日志脱敏不回归。
- [ ] 写入 / 恢复有 precondition、原子写、失败恢复语义。
- [ ] goroutine / listener / timer / process / Worker 有对称清理。
- [ ] 新行为有单元或集成测试；安全修改有绕过失败测试。
- [ ] 相关 Go tests、Vitest、typecheck、lint 实际通过，否则标 `S / U`。
- [ ] 触及导出 Go API 时检查 Wails bindings（`check-bindings.mjs`）。
- [ ] 触及 docs / 常量时运行文档检查。
- [ ] 真实平台、签名、远程、LSP、packaged E2E 未跑时明确保留风险。
- [ ] 修复后 Go 覆盖率不低于修复前基线（≈69.7%，当前 71.2%）。

---

## 24. AI 人格验收标准（继承 prompt-7 §24，逐条自检）

1. **诚实：** V/S/U 严格分级；未运行命令写"未运行（原因）"；不把 mock/contract-smoke 写成 packaged E2E；不把"源码存在"写成"能力可用"；不删测试保绿、不弱化审批；阻塞如实记录后继续下一 Goal；AC 勾选必须有可复现证据（测试名/命令输出/diff 摘要）。
2. **严谨：** Goal 前复核现状写"仍存在/已变化/已不存在"；安全修改"测试先红后绿"；代码改动后立即跑相关测试切片；不使用 `as any` / `@ts-ignore` / `@ts-expect-error`；不写空 catch；不 shotgun debugging（3 次失败 → 回滚 + 记录 + 请求决策）。
3. **最小：** 只改 Goal 内文件；新增/删除文件写明理由；不引入"可能有用"的抽象；复用既有机制（WorkspaceContext / 统一事务 / Recovery baseline）。
4. **安全：** 安全默认拒绝；不信任 renderer（approved/confirmed/safe/targetPath/allowPrivateNetwork 不抬权）；空 root = 拒绝；symlink 逃逸拒绝；不提交 secret；日志脱敏不回归。
5. **纪律：** 一次一个 Goal；todo 实时更新（用 todo 工具）；不擅自 commit / push；遵守 §1.2 退出码规则；每个 Goal 结束立即回写 §29 进度板。
6. **透明：** 报告每个命令实际结果（pass / fail / 未运行+原因）；标注全部 U 项与复现步骤；与审计结论不符时明确"已变化"。
7. **效率：** 验证批处理与并行；在 `/tmp/koyori-ide-audit` 副本跑验证；已确认结论引用证据不重复读；新探索用 grep 精确定位。
8. **自检：** 交付前对照 §23 DoD 逐项自检；§23 最终门禁全绿才允许写"闭环"；最终输出明确结论（闭环 / 部分闭环列缺口 / 未闭环列原因）。

---

## 25. 会话结构与断点（继承 prompt-7 §25）

- **阶段 A（~15min）：** 复核 §2 断点（抽查 2-3 条命令确认环境未变）；rsync 刷新 `/tmp/koyori-ide-audit`。
- **阶段 B（P0）：** G-03（续）→ G-04 → G-05 → G-06 → G-07 → G-08 → G-09，按依赖顺序，每 Goal 完成 = AC 全绿 + 证据落账。
- **阶段 C（P1）：** G-10 → G-18；G-16 的 U 边界如实标注。
- **阶段 D（边界锁定）：** G-19 / G-20 只写文档。
- **阶段 E（最终验收）：** 跑 §23 门禁 → §30 交付 → 回写 §29 进度板。
- **断点/续作：** 每 Goal 完成立即更新 §29 进度板 + 记录证据；会话中断后新会话先读 §29 与上次交付，从第一个"未开始/进行中"的 Goal 继续，**不重做已完成 Goal**。证据明细建议追加到 `/tmp/koyori-ide-audit/PROGRESS.md`（仓库外）。

---

## 26. 时间盒与停止条件（继承 prompt-7 §25.3）

- P0 每项 ≤ 1.5h，P1 每项 ≤ 1h，超时评估继续或降级。
- 遇到真实平台/凭据/CI 历史事项 → 立即标 `U` 记录复现步骤，不硬闯。
- 3 次连续修复失败 → 回滚 + 记录 + 暂停该 Goal（向用户报告，不自行扩大范围）。
- **停止规则：** 用户未要求 commit 时绝不 commit；本文件任务全部完成（或用户叫停）即停。

---

## 27. 补充门禁命令（§26 之外）

```bash
# 依赖安全（G-11）
cd /tmp/koyori-ide-audit/frontend && npm audit --audit-level=high --registry=https://registry.npmjs.org
# 发布脚本语法（G-08/G-09，S 级）
bash -n build/scripts/build-macos.sh && bash -n build/scripts/build-linux.sh
# 版本一致性（G-09）
PATH="/usr/local/go/bin:$PATH" go test -run 'Version|Release' . -count=1
# 仓库卫生（G-17）
ls -la /mnt/c/Users/<用户名>/Downloads/Koyori IDE-main | grep -Ei 'koyori-ide\.exe|NUL|\$profile|\.claude' || echo "clean"
```

---

## 28. 阻塞与环境重试（U 项复现命令）

```bash
# govulncheck（外网恢复后）
cd /tmp/koyori-ide-audit && PATH="/usr/local/go/bin:$PATH" go run golang.org/x/vuln/cmd/govulncheck@latest ./...
# packaged E2E（装好 wails3 + GUI runner 后）
cd /tmp/koyori-ide-audit && node scripts/packaged-e2e.mjs
```

---

## 29. 进度板（每次会话结束回写；当前为 2026-08-03 最终状态）

| Goal | 主题 | 状态 | 证据 |
|---|---|---|---|
| G-01 | root setter 收敛 / 空 root fail-closed | ✅ 已完成（V） | 见 prompt-7.md §27 G-01 行；services/root、正确正则 race、bindings、vue-tsc、lint、build 全绿，最终 services 覆盖率 71.4% |
| G-02 | Agent write capability + 事务 | ✅ 已完成（V） | agent_write_approval/transaction 安全测试及最终正确正则 race 全绿；bindings ByID 1551814169/2198789113；executeWriteTool 已迁移；services 全量/覆盖测试 EXIT=0 |
| G-03 | 原子保存 + baseline 冲突 | ✅ 已完成（V，2026-08-03） | 断点事实已变化：binding/facade/baseline 接线已由并行修复流补齐；本会话新增显式“覆盖/重新加载”冲突 UI，覆盖仍绑定冲突时磁盘 hash。`editor.test.ts` + `EditorView.test.ts` 44/44、`vue-tsc`、lint、`check-bindings.mjs`、Go FileService 保存切片均通过；ByID=1387081499。 |
| G-04 | 多文件替换事务接入 | ✅ 已完成（V，2026-08-03） | 复核确认后端核心事务已存在但 renderer 不可达；新增共享 WorkspaceContext 适配入口 `ApplyMultiFileReplace`（ByID=3838411692），前端 selected/replaceAll 单次事务调用并保留 conflicts[]。Go 空 root/逃逸/hash 回滚切片通过；search/SearchPanel 16/16、vue-tsc、lint、bindings 通过。 |
| G-05 | Recovery 启动闭环 | ✅ 已完成（V/U，2026-08-03） | 复核确认 scan/store 已存在但生产无调用且无 UI；现由 App 在首个 workspace ready 后扫描，RecoveryDialog 显示 clean/conflict/missing 与扫描错误。恢复只写 dirty editor buffer，keep-disk/keep-deleted 只清 journal，清理失败保留 pending。recovery/App/Dialog 24/24、vue-tsc、lint、build、Go Recovery 切片、packaged dry-run 均通过；G-16 后 driver 代码已覆盖 kill/restart/recovery，但真实 packaged SIGKILL/restart 演练仍 U。 |
| G-06 | AllowPrivateNetwork 后端签发 | ✅ 已完成（V，2026-08-03） | 复核确认 renderer 布尔仍在初始 URL/redirect/transport 三处抬权；现已移除该字段，新增原生确认后签发的 origin+requestID+2min TTL 单次 token，redirect 对私网继续绑定同 origin。缺失/伪造/拒绝/重放/跨 request/跨 origin/过期/redirect 绕过测试通过；HTTPClient Go+race、go vet、前端 9/9、vue-tsc、lint、build、bindings 全绿；ByID=199403180。 |
| G-07 | 命令入口 workspace 绑定 | ✅ 已完成（V，2026-08-03） | 复核确认 Debug 仅部分接入、Coverage/Toolchain/ESLint/Terminal/Agent 生产构造仍缓存 root；现统一由 main.go 注入同一 WorkspaceContext，命令调用取得 root+generation lease，路径经 pathsec，进程启动前 generation 变化即拒绝。Debug 的 Dir/Program/WebRoot/PathMappings 全部限于 workspace，项目提供的 Program/ExecutablePath 由后端原生确认且默认拒绝。新增 G07 空 root/逃逸/跨 generation 六入口矩阵及 Debug 确认测试；先红（缺少 5 个 context 构造和 Debug 确认），后绿。期间相关切片曾有 7 个旧构造兼容失败、全量曾有 1 个 denylist 结构化错误失败，均修复后复跑：G07、相关切片、services 全量、根包、go vet、race、bindings 均 EXIT=0；覆盖率 71.2%。 |
| G-08 | release.yml 产物/架构修复 | ✅ 已完成（S/U，2026-08-03） | 复核确认 matrix arch 未传入构建、macOS 以 tar 冒充 zip、Linux 静默取首项及平台元数据未前置校验；现四项矩阵均显式设置 `GOARCH`，Windows/Linux/macOS 分平台打包且严格断言唯一产物，macOS 用 `ditto` 生成真实 ZIP、Linux 保持 tar.gz，tag 前校验 VERSION/config/Windows/nfpm/Info.plist。新增 YAML 结构化契约与全部 bash step 语法测试；`go test . -run 'ReleaseWorkflow|ReleasingDocs'`、build scripts `bash -n`、doc-links/doc-numbers/wails-pin 均 EXIT=0。真实 tag CI、签名、公证及四平台 artifact 未运行，均 U。 |
| G-09 | 平台元数据 SSOT | ✅ 已完成（V/S/U，2026-08-03） | 复核确认 VERSION=0.2.0 而 Windows/nfpm/Info.plist 仍为 0.1.0，Darwin/nfpm 品牌仍为模板，且两主 build 脚本误读 Taskfile 顶层 version 并回退 0.1.0。新增 `sync-release-metadata.mjs` 从 VERSION 严格同步 config/package/三平台元数据并支持 `--check`；两主脚本直接校验 VERSION、支持无副作用 `--print-version`、缺失/非法即失败，macOS bundle 再用 PlistBuddy 注入版本。结构化 Go 测试覆盖 JSON/YAML/XML、品牌和缺失 VERSION 拒绝；先红 10 项及错误进入完整构建，后 `go test .`、脚本探针/`bash -n`、sync/doc 门禁均 EXIT=0。真实三平台 CI/打包 U；仓库旧实验/离线脚本中的历史硬编码不属于本 Goal 正式发布路径，未虚报为已清理。 |
| G-10 | Settings 测试 + 深链/双窗口 | ✅ 已完成（V，2026-08-03） | 复核确认主设置已移除 AI 写入口且既有双窗口 CAS/未知字段合并已存在，但 5 个旧深链未携带准确目标、开窗错误被吞、搜索无定位测试、schema 仅有 CAS version。先运行新增红测：前端 12 例中 9 fail，Go 2 fail；现 `openAIDesktopWindow(section)` 保留错误并通过 pending+`ai:open-settings` 定位唯一 AI 设置实例，主设置失败时显示 alert+重试，旧 URL 成功后重写 general。新增独立 `schemaVersion=1`，legacy 0 迁移且保留 provider/model/四类 prompt/preset/tool permissions，未来 schema 保存 fail-closed，CAS `version` 语义不变。定向前端 91/91、schema Go 切片、`go test ./services/`、bindings、前端全量 158 文件/2587 测试、vue-tsc、lint、build 均 EXIT=0。 |
| G-11 | npm audit High 消除 | ✅ 已完成（V，2026-08-03） | 复核确认初始 `npm audit --audit-level=high` 报 `brace-expansion` 与 `postcss` 共 2 High；仅执行 lockfile 范围的 `npm audit fix --package-lock-only`，未升级顶层 major。现依赖树为 brace-expansion 1.1.18/2.1.4/5.0.9、postcss 8.5.25，`npm audit --audit-level=high` 为 0 vulnerabilities，`npm ci`、`npm ls`、build 均 EXIT=0。第一次全量测试虽 2587 断言全过但出现一次异步 `document is not defined` 并 EXIT=1；定向 SidePanel 测试及随后完整复跑均未复现，最终 158 文件/2587 测试 EXIT=0，未用猜测性改动掩盖该失败。 |
| G-12 | .code-workspace 多根接线 | ✅ 已完成（V，2026-08-03） | 复核确认 binding 已有 `AddMultiRootProject`，但前端 `.code-workspace` 未调用；File/LSP 已支持多根而 Search/SymbolIndex 仅主根，共享 WorkspaceContext generation 也未纳入切换，解析失败还会把 workspace 文件冒充目录。现由后端解析 `.code-workspace` 作为 roots 权威来源，renderer roots 非空时必须与文件完全一致；全部根先整体 canonicalize/校验/去重，任一无效不发布状态。Project 两阶段切换原子更新 WorkspaceContext、File、LSP、Search、SymbolIndex，持久化失败精确恢复 root/generation/根列表；Search/SymbolIndex 支持第二根读写、搜索、增量/惰性索引。前端统一调用 `addMultiRootProject([], path)`，成功后一次提交 project/folders，失败保留旧 workspace，解析失败不再伪造单根；调用方全部 await。先红：前端新增 3 fail，Go 因 Search/SymbolIndex 缺 `WorkspaceRoots` 无法编译；后绿：G12 Go 切片、第二根 File/Search/Symbol、无效根整体拒绝、generation 单次推进与持久化回滚测试，前端 app 38/38、相关 50/50、race、services/根包/go vet、bindings、vue-tsc、lint、build、audit 均 EXIT=0。首次最终全量 2587 断言通过但 Wails drag interval 在 jsdom teardown 后访问 `window`，EXIT=1；确认 `lsp_9j.test.ts` 漏 mock runtime 后按同类测试补齐隔离，定向连续 5 次及最终全量 158 文件/2587 测试 EXIT=0。最终全量仍记录一条被产品代码捕获的 `EnvironmentTeardownError` 日志，但无未处理错误且进程为 0，未隐瞒该噪声。 |
| G-13 | Worktree/Rebase 注册接线 | ✅ 已完成（V，2026-08-03） | 复核确认两套后端服务与完整前端组件/store 已存在，但 bootstrap 未注册、无 bindings 且 GitPanel 无普通入口；现 appBundle/wailsServices 构造并注册 GitWorktree/GitRebase，共享 WorkspaceContext，repo 在调用时绑定当前 workspace。Worktree 外部目标仅由后端可信 safe roots 决定，renderer 的 `allowOutsideRepository` 不再抬权。补齐生成物等价 bindings/models/index（15 个 ByID）并直接复用 WorktreePanel/RebaseEditor。先红：根包缺字段、services 缺 context setter、bindings 缺文件、GitPanel 缺入口；后绿：G13 root/services 切片、真实 Git Worktree/Rebase 既有测试、前端 4 文件/69 测试、services/root 全量、go vet、Git race、bindings、vue-tsc、lint、build 及前端全量 159 文件/2590 测试均 EXIT=0。定向 Rebase 测试仍有既存 Element Plus 解析警告，build 仍有既存 chunk/dynamic-import 警告，未虚报为无警告。 |
| G-14 | Debug/Test 入口 | ✅ 已完成（V，2026-08-03） | 复核确认 Debug/Test 视图与 deep link 已存在，但 Activity Bar 无普通入口，Debug 也缺显式 loading/error/idle 状态；现直接复用 `/debug`、`/test` 增加两个键盘可达入口，全屏路由只保留对应入口 pressed。Debug/Test 补齐 `aria-busy` 与 loading/error/empty，窄窗工具栏换行、Test 双列降为单列且无横向溢出。先红：新增 Activity Bar 测试 4 fail、视图状态测试 2 fail，浏览器还发现 Explorer+Debug 双 pressed 并由重置状态后的 2 fail 稳定复现；后绿：定向 4 文件/76 测试、vue-tsc、lint、build 均 EXIT=0，640×720 浏览器实测两入口可达、各仅一个 pressed、页面与视图无横向溢出。首次全量虽 160 文件/2596 断言通过，但 Wails drag interval 在 jsdom teardown 后触发 `window is not defined`，进程 EXIT=1；为 `pullRequests.test.ts` 补 runtime 隔离后，最终全量 160 文件/2596 测试 EXIT=0，未隐瞒首次失败。 |
| G-15 | AI Diff apply 落盘 | ✅ 已完成（V，2026-08-03） | 复核确认 `DiffViewer` 丢弃 apply 返回值，store 仍走只计算不落盘路径；现新增 renderer 可达 `DiffService.ApplyDiff` 事务适配器（ByID=1258791612），root/path/hash/原子写与回滚由后端控制，成功后才发布 `file:saved`，结果返回 applied files、冲突及回滚状态，内部事务/setter 不暴露 binding。前端 `applyFile/applyAll` 统一消费结构化 `applied/conflict/failed`，dirty 打开 buffer 前置拒绝且不写后端，成功同步 Monaco、recovery baseline 与已加载文件树；等待事务期间继续输入不会丢失，冲突/失败显示详情、回滚状态及 Retry/Dismiss，成功 toast。先红：store 新增 3/3 fail、组件新增 2 fail、Go 因缺结果字段与 renderer adapter 编译失败；后绿：定向前端 4 文件/71 测试、最终全量 161 文件/2603 测试、`vue-tsc --noEmit`、lint、build、npm audit（0 vulnerabilities）、services/根包 Go、go vet、事务 race、bindings 均 EXIT=0。全量 Vitest 仍记录被产品 catch 的既有 `EnvironmentTeardownError`，无未处理错误且进程为 0；定向 editor 测试仍有既有 mock/预期错误日志，build 仍有既有 style.css、pure annotation、dynamic-import/chunk 警告。响应式复验首次 `npm exec vue-tsc` 因参数解析实际启动普通 tsc 并 EXIT=1，改用审计副本内 `vue-tsc` 后 EXIT=0。640×720 浏览器发现并修复三列输入横向溢出后，实测 document/DiffSection/merge group/DiffViewer/toolbar 均 `scrollWidth == clientWidth`，三个 merge textarea 均在组内，导出与工具栏正确换行、无文本遮挡；视口 override 已恢复。 |
| G-16 | packaged E2E hook + driver | ✅ 代码/配置已完成（V/S/U，2026-08-03） | 复核确认 harness 仅能构建/启动，7 个 fixture 全为 `implemented:false`，无自动化面且 CI 手动 gate 的理由仍是“driver missing”。现用互斥 Go build tag：普通构建只编译空 stub，`-tags e2e` 才编译控制端点；即使带 tag 仍须 `KOYORI_IDE_E2E=1`，仅监听 `127.0.0.1:0`，256-bit token 不写 handshake 且每次认证立即轮换，旧 token 重放 401。端点不注册 renderer binding，复用 packaged 进程内真实 Project/File/Recovery/Terminal/LSP 服务。driver 源码覆盖 open-workspace/open-file/edit/save/terminal/LSP hover+completion/SIGKILL-restart-recovery 七项；harness 使用隔离配置与 Xvfb，构建 `desktop,production,e2e`，失败保留 launch/Xvfb 日志并尝试截图，manifest 记录 commit、SHA-256、runner 元数据。CI required 源码 job 运行 driver 单测/dry-run及三平台 tagged hook；Linux artifact job补齐 Xvfb/ImageMagick/gopls、证据上传并保持 `workflow_dispatch`，注释锁定“3 个不同 commit 连续真实成功并保留 manifest 后才转 required”。先红：Node 因 driver 模块缺失失败，Go build-constraint 测试明确报 production disabled stub 缺失；首轮实现后 Node fake 未模拟 save 状态、Go 测试未走生产 `bindWorkspaceRoots` 而 fail-closed，修正测试夹具后通过，未放宽产品边界。后绿：driver 3/3、dry-run 七项、production/e2e build-constraint、loopback/token replay、真实 File/Recovery、workflow 结构测试、tagged hook race、默认/`e2e` vet、bindings、doc-links、wails-pin、Go 全量（services 约 65s）均 EXIT=0；日志有既有 GTK deprecation。过程中的 WSL Go PATH/模块调用、管道引号与不可用 Git VCS stamping 命令曾 EXIT=1，分别改用 `/usr/local/go -C`、拆分精确命令、`-buildvcs=false` 后有效重跑，不计为首次通过。真实 `node scripts/packaged-e2e.mjs` 因本机缺 `wails3 v3.0.0-alpha2.111` 在 toolchain 阶段 EXIT=1，未产生/运行 artifact；Linux 实跑与 3 次稳定证据、Windows/macOS 实跑均 U。端点验证 packaged 后端服务图，不宣称 DOM/Monaco/像素级 UI 覆盖。 |
| G-17 | 仓库卫生 + NOTICE/SBOM | ✅ 代码/文档已完成（V/S/U，2026-08-03） | 复核确认 `koyori-ide.exe`（49,017,344 bytes）、`NUL`、`$profile` 仍存在且 NOTICE/清单缺失、release SBOM 为 best-effort；现三文件已直接永久删除并由 `.gitignore` 覆盖，另补 `/.claude/`、`/.omo/`、`.task/`、`bin/`/`*.exe`。`.git/` 空目录导致历史 tracked 状态无法确认（U）；`.claude/settings.local.json` 仅顶层 `permissions`，模式扫描未发现 API key/token/secret/password 值。新增 `NOTICE` 与可重现清单生成器：198 Go 模块、407 个去重 npm 包版本；分类行未检出 GPL/AGPL，但 14 个取不到上游源码的 Go 模块诚实标为 `UNRESOLVED`，不把 0 copyleft 误写成无风险。`go-licenses@v1.6.0` 因 `proxy.golang.org` 超时失败仍为 U。release 现强制非空可解析 SPDX JSON SBOM、发布 NOTICE/清单、生成明确 `unsigned`/“not a signed attestation”的 in-toto/SLSA-shaped provenance，最后重建覆盖全部资产的 SHA256SUMS；无 skip/`continue-on-error`/partial output 路径。先红：G17 测试因 Windows `NUL` 设备语义及 NOTICE 换行 2 fail；改用目录枚举/关键短语后绿。审计副本根包全量、`go vet .`、release/G17 契约、provenance 2/2、许可证离线摘要、两份 shell `bash -n`、doc-links/doc-numbers/wails-pin 均 EXIT=0，保留既有 GTK deprecated 警告；本机缓存完整时 `--full-check` EXIT=0。实际 SBOM 尝试因 Docker 拉取 `anchore/syft:v1.29.0` 访问 registry-1.docker.io 超时 EXIT=1，脚本清除临时/空输出；真实 tag CI、SBOM 产物、签名/公证、来源签名均 U。 |
| G-18 | 文档诚实定位 | ✅ 已完成（V/S/U，2026-08-03） | 复核确认 README 已有 0.x/Wails alpha、Remote/VSIX/Debug 限制和未验证下载提示，但仍误写“真实 gopls 已验证”（实际 `gopls`/`typescript-language-server`/`vtsls` 均不在 PATH）、“任意 OpenAI 兼容”、AI“安全漏洞扫描”与 ready-out-of-box，且无统一 V/S/U 表；SECURITY 虽写 best-effort/no SLA，却同时承诺 48h ACK/7d 修复并把 Ubuntu-only Wails build 泛化为三平台；RELEASING 未写 packaged qualification。现 README 中英矩阵逐项覆盖本地编辑、Git、LSP、AI、Agent、Recovery、最小 Remote、Debug/Test、插件/VSIX、发布供应链，明确 mock/contract 不升级真实集成，修正 LSP/provider/Monaco 0.52.2/签名-SBOM-provenance 表述并移除越界宣称。SECURITY 明确 Wails v3 alpha、0.x best-effort、无响应/修复 SLO、无独立外部审计；报告入口改为私密 Security Advisory URL + 公开维护者邮箱回退，CI 表区分源码配置、三平台 matrix、Ubuntu-only job 与手动 packaged U。RELEASING 新增 packaged E2E qualification：本机缺 `wails3` 未构建/启动，三次不同 commit 真实成功前不升 required，tag workflow 当前可绕开该手动 job。先红：新增 G18 合同测试三组均 fail（缺矩阵/边界、旧 SLO/外审声明、缺 packaged 章节）；后绿：本机与 `/tmp/koyori-ide-audit` `go test . -run G18`、doc-links/doc-numbers/wails-pin、release/G18 合同均 EXIT=0，保留既有 GTK deprecated。全仓声明 grep 剩余 production 均为 build tag/模式或否定性边界，VS Code compatibility/replacement 命中均为“不兼容/非替代品”；旧越界短语负向 grep 无命中（`rg` EXIT=1）。一次正向 grep 因 PowerShell Markdown 反引号引号解析 EXIT=1，移除该字面模式后复跑 EXIT=0。真实 LSP/provider/SSH/Delve/Node/Open VSX、Security Advisory 可达性、CI/tag/artifact/signing/notarization/packaged E2E 仍 U。 |
| G-19 | P2 边界锁定（蓝图） | ✅ 蓝图已完成、实现未开始（S/U，2026-08-03） | 复核确认现状仍是最小 SSH/SFTP（无远端 PTY/LSP/Git/Debug/Test broker）、本地绝对路径 `WorkspaceContext`、硬编码 LSP 发现、未版本化 command/view contributes 与受限 Worker API；真实 SSH/LSP/扩展 corpus 均无证据。仅新增三份 Markdown 设计草案：`HOST-CLIENT-PROTOCOL.md` 定义 host cryptographic identity、local/remote workspace URI、generation/request envelope、FS/watch、PTY、SCM、Language、Debug/Test、edit transaction、journal/snapshot 与 CONNECTED/DEGRADED/DISCONNECTED/RECONNECTING/STALE/CLOSED 语义；`LANGUAGE-PACK-SDK.md` 定义 closed manifest、server variant、workspaceHost placement、受限 SDK、权限/生命周期/包完整性及真实 server 验收；`EXTENSION-CONTRIBUTION-PROTOCOL.md` 定义版本 envelope、closed schema、uiHost/workspaceHost、shadow registry 原子发布/回滚、token/epoch 与 E0-E5（E5 v1 拒绝）。每份首屏均标注 “Design draft, not implemented”，包含依赖、迁移顺序和 hostile/real/packaged 验收；README/ARCHITECTURE 链接三草案并重申不会升级当前能力。G-19 期间除 Markdown 外未改 Go/TS/YAML/运行时代码；本机与 `/tmp/koyori-ide-audit` doc-links（13 files）、doc-numbers、wails-pin 均 EXIT=0，定向内容 grep 覆盖全部必需术语。协议实现、真实 host/language server/extension corpus 与 packaged 证据仍 U。 |
| G-20 | SLO/审计标注 | ✅ 文档策略已完成、采集未实现（S/U，2026-08-03） | 复核确认 G-18 仅写无响应/修复 SLO和无外部安全审计，仍缺产品可靠性数据、供应链/可访问性外审状态；README 还绝对声称“所有可点击元素”可键盘访问。现 README/SECURITY 将产品可靠性 SLO、漏洞响应/修复 SLO、外部安全、供应链、可访问性审计逐项标为 U，明确本地测试/NOTICE/SBOM/provenance/部分 ARIA 不是 SLO 或第三方审计，当前无遥测实现/默认收集。RELEASING 新增 future-only policy：opt-in/关闭/保留/删除与 privacy/threat review 前提，禁止源码/prompt/path/命令/secret/host identity 数据，列 crash-free、startup、edit durability、Recovery、process/debug/test、LSP、Remote、update、packaged E2E/CI 事件与解释边界；任何数字目标前须定义 query、分母/排除、样本决策、观察窗、release/platform cohort、owner/error budget、数据质量；外审需 assessor、标准、版本/commit/平台范围、日期、报告、findings/exceptions/retest，release 必须写 URL 或 “not externally audited”。仅修改 Markdown，无埋点/telemetry/dashboard/阈值实现。全仓 enterprise/production-ready grep 只命中中英两处明确否定句；本机与 `/tmp/koyori-ide-audit` doc-links（13 files）、doc-numbers、wails-pin 均 EXIT=0。一次正向 grep 因 PowerShell Markdown 反引号解析 EXIT=1，移除反引号模式后复跑 EXIT=0。真实 SLO 数据、真实 release cohort/dashboard、三类独立外审均 U。 |

---

## 30. 最终交付（2026-08-03）

### Goal 完成情况

- 本会话按顺序完成 G-03、G-04、G-05、G-06、G-07、G-08、G-09、G-10、G-11、G-12、G-13、G-14、G-15、G-16、G-17、G-18、G-19、G-20；G-01/G-02 基线保持通过。
- G-16 按规定完成源码 hook、driver、dry-run 与 CI 配置，真实 packaged 运行仍为 `U`；G-19/G-20 严格只做设计边界和状态标注，不把草案或策略写成实现。
- 部分完成：无（不把 Goal 明确允许保留的外部环境 `U` 项算作本机实现缺口）。
- 未完成：无本文件范围内 Goal；真实发布资格和外部集成仍未闭环，详见“U 项与风险”。

### 复核结论

- G-03：断点问题已变化；后端原子保存已存在，缺失的前端 baseline/冲突闭环已补齐，当前问题已不存在。
- G-04：逐文件 apply 导致 partial result 的问题原先仍存在；现已由单次多文件事务替代，问题已不存在。
- G-05：Recovery store 原先存在但生产启动未接线；现已接线扫描与决策 UI，源码范围问题已不存在，真实 kill/restart 演练仍 `U`。
- G-06：renderer `AllowPrivateNetwork` 原先仍可抬权；现已改为后端签发、绑定、短时、单次 token，问题已不存在。
- G-07：多个命令服务原先缓存 root 或未绑定 generation；现统一绑定共享 WorkspaceContext，问题已不存在。
- G-08：release matrix、架构传递和 artifact 格式问题原先仍存在；workflow 源码已修复，真实 tag CI 仍 `U`。
- G-09：VERSION 与平台元数据原先漂移；现正式发布路径由 VERSION 同步并 fail-closed，源码问题已不存在。
- G-10：旧 Settings 测试、深链和双窗口失败处理原先仍有缺口；现唯一写入口、迁移和错误重试已闭环。
- G-11：2 个 High npm 漏洞原先仍存在；lockfile 范围修复后官方 registry audit 为 0 vulnerabilities。
- G-12：`.code-workspace` 原先未接入多根服务；现 roots/generation/失败回滚已接线，问题已不存在。
- G-13：Worktree/Rebase 实现原先存在但未注册、无 binding/普通入口；现已完成接线，renderer 路径抬权被拒绝。
- G-14：Debug/Test 视图原先存在但缺普通入口和完整状态；现入口、loading/error/empty 与窄窗布局已补齐。
- G-15：AI Diff 原先只计算或丢弃 apply 结果；现经后端事务落盘并同步编辑器，冲突/回滚结果可见。
- G-16：原 harness 无 driver；现七项 driver 与受 build tag/env 双门控的 loopback hook 已存在，真实 artifact 运行仍 `U`。
- G-17：孤立二进制/伪文件、NOTICE/许可证/SBOM 流程缺口原先仍存在；本地卫生和发布源码已修复，实际 SBOM 生成仍 `U`。
- G-18：文档原先仍有未验证能力、SLO 和平台泛化声明；现 V/S/U 矩阵及边界已校正。
- G-19：运行时统一 Host/Language Pack/贡献协议仍未实现；按 Goal 限定只完成三份明确标为未实现的设计草案。
- G-20：可靠性数据、响应 SLO 和三类外审仍不存在；现已诚实标为 `U`，只写 future-only 采集/审计策略，未实现 telemetry。

### 改动

- `.git/` 是空目录，无法生成可信的 tracked diff 或证明下面是历史意义上的完整文件清单；以下是本会话通过源码、测试和 §29 证据复核的交付索引，未把这一限制伪装成已核对。
- G-03/G-04/G-05：`services/file_save_integrity.go`、`services/file_save_integrity_test.go`、`services/search_service.go`、`services/workspace_edit_transaction_test.go`、`services/recovery_service.go`、`frontend/bindings/koyori-ide/services/fileservice.ts`、`searchservice.ts`、`frontend/src/api/workspace.ts`、`frontend/src/api/search.ts`、`frontend/src/stores/editor.ts`、`editor.test.ts`、`search.ts`、`search.test.ts`、`recovery.ts`、`recovery.test.ts`、`frontend/src/components/layout/SearchPanel.vue`、`SearchPanel.test.ts`、`frontend/src/components/modals/RecoveryDialog.vue`、`RecoveryDialog.test.ts`、`frontend/src/App.vue`。
- G-06/G-07：`services/http_client_model.go`、`http_client_service.go`、`http_client_private_approval_test.go`、`workspace_context.go`、`g07_workspace_command_boundary_test.go` 及 Coverage/Toolchain/ESLint/Debug/Terminal/Agent 服务接线，`frontend/bindings/koyori-ide/services/httpclientservice.ts`、`frontend/src/components/http/HTTPClientPanel.vue`、`HTTPClientPanel.test.ts`。
- G-08/G-09：`.github/workflows/release.yml`、`release_workflow_test.go`、`release_version_test.go`、`release_platform_metadata_test.go`、`scripts/sync-release-metadata.mjs`、`build/scripts/build-linux.sh`、`build/scripts/build-macos.sh`、`build/config.yml`、`build/windows/info.json`、`build/linux/nfpm/nfpm.yaml`、`build/darwin/Info.plist`、`frontend/package.json`。
- G-10/G-11/G-12：`services/settings_service.go`、`settings_service_test.go`、`project_service.go`、`g12_multi_root_test.go`、`frontend/src/views/SettingsView.vue`、`SettingsView.test.ts`、`frontend/src/components/ai-window/AiSettingsView.vue`、`AiSettingsView.test.ts`、`frontend/src/stores/appActions.ts`、`workspaceStore.ts`、`frontend/package-lock.json`。
- G-13/G-14/G-15：`services/git_worktree_service.go`、`git_rebase_service.go`、`g13_git_workspace_test.go`、`diff_service.go`、`workspace_edit_transaction_test.go`、`g13_wiring_test.go`、`bootstrap_services.go`、`main.go`、对应 Wails bindings、`frontend/src/lib/gitWorktree.ts`、`gitRebase.ts`、`frontend/src/components/layout/GitPanel.vue`、`ActivityBar.vue`、`frontend/src/views/DebugView.vue`、`TestView.vue`、`frontend/src/stores/diff.ts`、`diff.apply.test.ts`、`frontend/src/components/ai-assistant/DiffViewer.vue`、`DiffViewer.test.ts`。
- G-16：`e2e_automation_disabled.go`、`e2e_automation_enabled.go`、`e2e_automation_build_test.go`、`e2e_automation_enabled_test.go`、`scripts/packaged-e2e.mjs`、`packaged-e2e-driver.mjs`、`packaged-e2e-driver.test.mjs`、`.github/workflows/ci.yml`。
- G-17：删除根目录 `koyori-ide.exe`、`NUL`、`$profile`；修改 `.gitignore`；新增 `NOTICE`、`docs/THIRD_PARTY_LICENSES.md`、`scripts/generate-license-inventory.mjs`、`generate-sbom.sh`、`generate-release-provenance.mjs`、`generate-release-provenance.test.mjs`；修改 `scripts/release-evidence.sh`、`.github/workflows/release.yml`、`repository_hygiene_test.go`。
- G-18/G-19/G-20：修改 `README.md`、`.github/SECURITY.md`、`docs/RELEASING.md`、`docs/ARCHITECTURE.md`、`documentation_claims_test.go`；新增 `docs/HOST-CLIENT-PROTOCOL.md`、`docs/LANGUAGE-PACK-SDK.md`、`docs/EXTENSION-CONTRIBUTION-PROTOCOL.md`。
- 最终门禁修复：修改 `scripts/core-path-smoke.test.ts`，把旧 `writeFile` smoke 迁移到 SHA-256 baseline 绑定的 `writeFileIfUnchanged`，并隔离 Web Crypto、Recovery 与 extension activation；未修改产品保存逻辑。
- 明确未做：未 commit/push；未实现 G-19 协议或 G-20 telemetry；未伪造 tag、CI、artifact、签名、公证、SBOM、真实 LSP/SSH/provider/Debug/Open VSX 或 packaged E2E 结果。

### AC 证据

| Goal | AC | 证据摘要 |
|---|---|---|
| G-03 | ✅ `V` | `TestFileService_WriteFileIfUnchanged_*`、editor/EditorView 冲突覆盖与重新加载测试、binding gate；原子失败保留文件、权限与越界拒绝。 |
| G-04 | ✅ `V` | `TestSearchService_ApplyMultiFileReplaceTransaction_*`、`FailsClosedWithoutRoot`、`RejectsPathEscape`；Search store/Panel 单次事务路径通过。 |
| G-05 | ✅ `V/U` | Recovery/App/Dialog 24/24 与 Go Recovery 切片通过；真实 SIGKILL/restart 为 `U`。 |
| G-06 | ✅ `V` | `TestHTTPClientOptionsCannotCarryRendererPrivateNetworkBoolean`、缺 token/拒绝/重放/跨 request/origin/过期/redirect 绕过测试与 race 通过。 |
| G-07 | ✅ `V` | `TestG07CommandEntrypointsRejectEmptySharedWorkspace`、`RejectWorkspaceEscape`、`RejectWorkspaceGenerationChangeBeforeStart`、`DebugProjectExecutableRequiresBackendConfirmation` 等通过。 |
| G-08 | ✅ `S/U` | `TestReleaseWorkflowContract`、`TestReleaseWorkflowBashStepsHaveValidSyntax` 与 shell `bash -n` 通过；真实 tag CI 为 `U`。 |
| G-09 | ✅ `V/S/U` | `TestReleaseVersionConsistency*`、`TestPlatformReleaseMetadataMatchesVERSION`、metadata `--check` 通过；真实平台包为 `U`。 |
| G-10 | ✅ `V` | Settings/AiSettings 定向与全量前端测试、settings schema Go 测试、bindings/type/lint/build 通过。 |
| G-11 | ✅ `V` | 官方 `registry.npmjs.org` 的 `npm audit --audit-level=high`：0 vulnerabilities。 |
| G-12 | ✅ `V` | `TestG12MultiRootCoordinatesFileLSPSearchSymbolAndGeneration`、无 partial switch 与 persistence rollback 测试通过。 |
| G-13 | ✅ `V` | `TestG13GitWorktreeUsesWorkspaceContextAndRejectsRendererPathElevation`、跨 workspace rebase 拒绝、Git UI/bindings 测试通过。 |
| G-14 | ✅ `V` | ActivityBar/Debug/Test 状态测试与 640×720 浏览器检查通过；每个全屏路由仅一个 pressed 入口。 |
| G-15 | ✅ `V` | `TestDiffService_ApplyDiffTransaction_*`、renderer adapter、dirty buffer/conflict/rollback 与 DiffViewer/store 测试通过。 |
| G-16 | ✅ `V/S/U` | production build-tag 排除、loopback/token replay、real-service adapter 测试通过；driver 3/3、dry-run 七项通过；真实 packaged 为 `U`。 |
| G-17 | ✅ `V/S/U` | `TestG17RepositoryHygieneAndIgnoreRules`、Claude settings、NOTICE digest、SBOM fail-closed、license `--check`、provenance 2/2 通过；实际 SBOM 为 `U`。 |
| G-18 | ✅ `V/S/U` | `TestG18ReadmeCapabilityMatrixAndBoundaries`、`SecurityPolicyHasNoUnverifiedSLOOrAuditClaim`、packaged evidence boundary 与文档门禁通过。 |
| G-19 | ✅ `S/U` | 三份文档首屏均写明 “Design draft, not implemented”；doc links/numbers 通过；实现与真实 corpus 为 `U`。 |
| G-20 | ✅ `S/U` | README/SECURITY/RELEASING 的 SLO、telemetry、外审状态与 future-only policy 已复核；无数据、dashboard 或外审，均为 `U`。 |

- 关键绕过失败测试：`TestAgentService_ExecuteApprovedWrite_RejectsForgedToken`、`RejectsReplayToken`、`RejectsCrossGenerationToken`、`RejectsChangedPath`、`RejectsHashMismatch`、`RejectsSizeMismatch`、`RejectsExpiredToken`、`RejectsDiskChangeAfterApproval`；`TestHTTPClientPrivateNetworkRequiresBackendToken`、`ApprovalCanBeDenied`、`PrivateRedirectsStayBoundToApprovedOrigin`；G-07 empty-root/escape/generation/confirmation；G-13 renderer path elevation；E2E token replay；事务 empty-root/hash/path/rollback 测试均通过。

### 命令与结果

- `go test ./services/ -count=1` / 覆盖重跑 -> pass；最终覆盖测试 `TEST_EXIT=0`，71.4% statements（不低于 69.7% 基线），保留既有 GTK deprecated 警告。
- `go test . -count=1`、`go test -tags e2e . -count=1`、默认及 `-tags e2e` 的 `go vet` -> 全部 `EXIT=0`。
- security race 使用真实正则 `Agent|MCP|ComputerUse|IM|Remote|Path|Snapshot|Goal|Recovery|Workspace|Root|Write` -> `EXIT=0`，services 实际运行约 30s；此前下划线拼接导致 `[no tests to run]` 的结果作废，未计通过。
- `npm test` -> `161` files / `2603` tests passed，`EXIT=0`；日志仍有既有 browser/mock/Vue warning，但无失败或未处理错误。
- `npx vue-tsc --noEmit`、`npm run lint`、`npm run build` -> 全部 `EXIT=0`；build 仍有 `/style.css`、pure annotation、chunk size、dynamic-import 警告。
- `npm audit --audit-level=high` 首次因 `registry.npmmirror.com` 不实现 audit API 而 `EXIT=1`；显式改用官方 registry 后 0 vulnerabilities、`EXIT=0`。
- bindings、doc-links（13 files）、doc-numbers、Wails pin、metadata `--check`、四份 shell `bash -n`、version/release tests、packaged driver 3/3、packaged dry-run、license offline `--check`、provenance 2/2 -> 全部 `EXIT=0`。
- `contract-smoke.mjs` 首次因旧 fixture 缺 Web Crypto/Recovery 且仍断言 `writeFile` 而 `EXIT=1`；仅修 fixture 后 1/1 passed、最终 `EXIT=0`。
- 最终门禁首轮 root 与 `-tags e2e` 测试曾因 G-20 改写拆散 G-18 所需审计短语而失败；恢复连续中英文短语且保留三类外审 `U` 含义后，两项最终均 `EXIT=0`。
- 覆盖测试本身成功并输出 71.4%；其后非必需的 `go tool cover -func` 第一次被 PowerShell 参数转发破坏（`EXIT=2`），stop-parsing 重试又因绝对 profile 未落盘而 `EXIT=1`，未把这两次函数明细命令写成通过。
- 一次 WSL 同步验证的嵌套引号/Windows `rg` 调用失败、一次只读 PowerShell 搜索引号失败、一次批处理 JavaScript 编排语法错误在启动子命令前终止；均已用明确命令重跑，不作为项目通过证据。

### U 项与风险

- `.git/` 为空：tracked 状态、历史 tag/release/CI 和完整 diff 均无法确认。复现：恢复真实 git metadata 后运行 `git status --short`、`git tag --verify` 和对应 Actions 历史检查。
- `wails3` 不在 PATH（`which wails3` 为 `EXIT=1`）：未构建/启动真实 packaged artifact，Linux/Windows/macOS packaged E2E、三次不同 commit 稳定证据、真实 SIGKILL/restart Recovery 均 `U`。复现：安装精确 `v3.0.0-alpha2.111` 后运行 `node scripts/packaged-e2e.mjs`。
- 实际 SBOM：Docker 拉取 `anchore/syft:v1.29.0` 访问 Docker Hub 超时，生成命令 `EXIT=1` 且脚本清除空/临时输出；真实 SBOM 仍 `U`。外网恢复后运行 `bash scripts/generate-sbom.sh <artifact> <output>`。
- Go 漏洞与许可证外部核验：`govulncheck`、`go-licenses@v1.6.0` 因外网失败，均 `U`；复现命令见 §28 与 `docs/RELEASING.md`。
- 真实 tag CI、四平台 artifact、签名、公证、来源签名、Security Advisory 可达性未验证；workflow/YAML 只能标 `S`。
- 真实 gopls/vtsls、SSH、AI provider、Delve/Node、Open VSX/VSIX corpus 与 Remote broker 未运行；mock/合同测试不升级为真实能力。
- 无真实产品 reliability cohort、SLO query/dashboard、telemetry 数据或独立安全/供应链/可访问性审计；这些状态均为 `U`。
- 回归风险：Wails v3 alpha 和 GTK deprecated API、前端大 chunk/dynamic-import 警告仍存在；contract smoke 是 mocked renderer 合同而非 packaged UI/Monaco/像素级验证。

### AI 人格自检表（§24）

- 诚实（V/S/U 分级、无伪造、未删测试）：✅
- 严谨（先复核、先红后绿、切片先行、无类型压制）：✅
- 最小（范围内、无多余抽象）：✅
- 安全（fail-closed、无抬权、无 secret、空 root 拒绝）：✅
- 纪律（一次一个、进度板实时回写、无 commit/push）：✅
- 透明（首次失败、警告、U 项与复现命令均记录）：✅
- 效率（批处理、`/tmp/koyori-ide-audit` 副本、引用证据）：✅
- 自检（§23 本机可执行门禁最终全绿后才写本交付）：✅

### 整体结论

- `prompt-8.md` 规定的源码、测试、文档和 V/S/U 边界已闭环；真实发布资格仍是部分闭环，因为 packaged artifact、CI/tag、SBOM、签名、公证、真实集成和外部审计均为 `U`。
- 定位不变：项目是 0.x Go/TS 垂直桌面 AI IDE，基于 Wails v3 alpha，不是生产级、企业就绪、完整 Remote-SSH，也不是 VS Code/Cursor 替代品。

### SSOT 回写

- §29：G-03 至 G-20 均已逐 Goal 回写；G-19 标为“蓝图完成、实现未开始”，G-20 标为“文档策略完成、采集未实现”。
- 日期与最终证据：2026-08-03；Go root/services/vet/race/e2e、前端 161/2603、type/lint/build/audit、bindings/docs/release/contract/dry-run/license/provenance 最终结果见上。
- 基线回归：首轮发现 SECURITY 合同短语与 contract smoke fixture 两处回归，均修复并重跑通过；未发现 G-01/G-02 安全基线回归。

---

## 31. 一键启动词

```text
请严格按仓库根目录 prompt-8.md 执行。
先读 §0、§1、§2（断点快照）；按 §4 顺序从第一个"未开始/进行中"的 Goal 开始，一次只做一个。
开始前复核代码与测试（写明"仍存在/已变化/已不存在"），安全修改先写失败测试再修复。
遵守 §24 AI 人格验收标准与 §1.2 验证纪律（/tmp 副本、退出码规则、时间盒）。
每个 Goal 完成立即回写 §29 进度板并记录证据；结束时按 §30 交付，更新 §29。
G-19 / G-20 只做边界锁定与标注，不实现。
不要 commit，除非我明确要求。
```

---

## 32. 最终产品目标（不变，继承 prompt-7 §30）

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

*文档结束。执行入口以本文件为准；完整审计证据与历史安全要求见 prompt-7.md、prompt-6.md、prompt-5.md、prompt-4.md、prompt-1.md。*
