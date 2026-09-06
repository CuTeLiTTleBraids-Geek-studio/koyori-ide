# Koyori IDE Goal 续作任务（prompt-10）

> 用途：交给后续 AI 继续维护当前工作区。本文是 `docs/prompts/prompt-9.md` 的续作交接，不替代 prompt-9；所有验收标准、证据等级和安全纪律以 prompt-9 为上位规范。
>
> 产品正式名称：**Koyori IDE**。代码中的稳定机器标识 `koyori-ide` 可以保留。请使用中文思考、沟通和回写文档。

## 0. 继承规则

1. 先完整阅读 `docs/prompts/prompt-9.md` 的第 0、1、2、3、8、9 节，再阅读本文件；每次只推进一个 Goal。
2. 每个 Goal 开始前写明审计缺口是“仍存在 / 已变化 / 已不存在”；结束或暂停时立即回写本文件和 prompt-9 的进度板。
3. 严格区分证据：`S` 静态、`T` 单测/mock/contract、`I` 真实服务或真实进程、`P` 真实 packaged 用户工作流、`R` CI/release/签名/审计历史、`U` 未验证或环境阻塞。禁止把 mock、dry-run、fixture、设计文档或 YAML 当作 `I/P/R`。
4. 未满足全部 AC 不得写“完成”，只能写“进行中”或“阻塞”；环境不可用也不能伪造通过，保留 `U` 并继续完成其他可做任务。
5. 默认 fail-closed。不得放宽 workspace/path、权限、token、generation、进程参数、网络 SSRF、扩展 API 或更新校验。不得用 `any`、类型压制、删除测试或泛化成功结果隐藏问题。
6. 不手工猜 Wails binding ID，使用仓库锁定的 `v3.0.0-alpha2.111` 生成并检查 bindings。不要 commit、push、tag、release 或发布；当前工作区没有可核验 `.git` 元数据。
7. 用户数据优先：保存、恢复、更新、扩展故障必须验证冲突、崩溃、重试、回滚和失败后的可恢复性。
8. 不得出现 P0 或 P2 级灾难。修改安全边界前先补绕过失败测试；修改真实工作流前先保留原始红灯和日志。

## 1. 当前工作区和外部状态

- 工作区：`%USERPROFILE%\Downloads\Gugacode-main`
- 平台：Windows amd64，当前日期上下文为 2026-08-10；Go `1.26.4`，项目 `go.mod` 目标 `1.25.0`；Node `v24.18.0`。
- Wails CLI 与项目模块锁定：`v3.0.0-alpha2.111`。
- `.git` 目录为空/不可核验，不能证明 tracked/untracked、commit、CI、tag、release 或历史；涉及这些条件统一记为 `U`。
- 公开 GitHub API/Actions 与 macOS runner 当前不可达；不能用本地 Windows 结果替代 macOS 或真实 CI。
- 当前 production 版本元数据已经统一为 `0.2.0`，产品名应显示为 Koyori IDE。

## 2. 已完成的前置 Goal

以下状态继承 prompt-9 的真实记录，不要重复做无关重构：

- G01：阻塞，主要剩 `.git` ownership/生成物归属与 CI/镜像等 U。
- G02、G03、G04、G05、G06：已满足各自 AC；G05 已完成 WorkspaceContext 唯一权威和真实双窗口切换，G06 已完成 runtime role 隔离、单 owner 与 packaged 双窗验证。
- G07、G08、G09、G10、G13、G16、G19、G21、G23：仍有真实 CI/macOS/语料或发布证据 U，不能擅自升级状态。
- G11、G12、G14、G15、G17、G18、G20、G22：按 prompt-9 进度板已有真实证据，继续保留其限制条件。

不要因为某个 Goal 的代码已经存在，就把它的 AC 自动勾选；必须读取 prompt-9 的对应证据和进度板。

## 3. P9-G24 完成记录

### 3.1 审计缺口（截至 2026-08-10 复核）

状态：**完成，AC 4/4 已正式勾选**。

G24 的实现已通过大量 `T` 级测试；最终真实 packaged manifest `build/e2e-evidence/packaged-e2e/manifest.json` 为 `status=passed`，24/24 fixtures 通过。artifact SHA-256 为 `7e8abff533098129f6cf858dd9278053c71786edbf5858a6003452589f07b181`，source fingerprint 为 `690aa31cad880bf803037ab734207a9e1f7281d9e05140f65daaef15bd7b6180`，`recordedAt=2026-08-11T03:23:53.760Z`。首次本轮失败记录 `disabled=true` 且仍 `active=true`；后端 lifecycle stop handshake 已修复。低内存 full run 曾因 Git `0xc0000142` 失败，随后 true `KOYORI_IDE_E2E_SKIP_BUILD=1` 复用既有 artifact 通过。

本轮（2026-08-10）已修复与新增：

- 修复 `WorkerScope.addEventListener` 联合 listener 类型回归（TS7006，vue-tsc 从 1 error 到 exit 0）。
- 修复 post-G24 edit driver 缺陷：新增 e2e `create-file` 命令（`internal/e2e/server.go`），driver 在 edit 前先按真实产品顺序 `CreateFile` 创建磁盘空文件（`ComputeBaseline` 对缺失文件返回空 hash 导致原断言失败）。
- 新增 `scripts/g24-corpus-report.mjs` + `scripts/g24-vsix-zip.mjs` + `scripts/g24-corpus-report.test.mjs`（11/11 测试），真实 corpus 报告 10 包全 blocked（缺 `koyoriIde.permissions`），输出 `build/e2e-evidence/p9-g24/corpus-report.json`，满足 AC3。
- `scripts/packaged-e2e.mjs` 支持 `KOYORI_IDE_E2E_SKIP_BUILD=1` 复用既有 artifact 复跑；manifest goal 字段加入 G24。
- G25 T 级基础：ICU plural（`Intl.PluralRules` 选类，含 ru/pl/ar 真实 few/many/zero/two 验证）、locale 元数据、`formatNumber`、missing-key 监测；profile 版本化导入导出（schema v1、secret redact、1 MiB 限制、非法 JSON/未知版本/原型污染键 fail-closed 拒绝）。本轮不推进或修改 G25 实现。

### 3.2 已经完成的代码工作

主要文件：

- `frontend/src/lib/vscodeExtensionActivation.ts`
  - Dedicated Worker ABI `1.0` 协商、`protocol-ready/protocol-error`、协商前 RPC 拒绝。
  - 随机 token 校验，伪造 token 忽略。
  - heartbeat watchdog：2 秒 interval、8 秒 timeout。
  - 消息配额：4 MiB/message、1000 messages/second。
  - crash/hang/quota 进入既有恢复路径；terminate/reset 清除 timers。
  - `WorkerExtensionModule` export，供真实 packaged probe 使用。
  - 成功恢复后清零连续失败计数，避免非连续故障永久停用。
  - 最近新增激活诊断：记录最近一次真实激活错误，懒加载命令传播原始错误和 `cause`。
  - 最近修复 bundle 闭包问题：删除 bootstrap 中会被 Rolldown 改写成外部 helper 的 `import(blobURL)`；扩展 CommonJS 源码直接注入同一个 Worker Blob 的本地加载器。
  - 最近新增 Worker 内部 `error` 事件桥：发送认证的 `runtime-error` 后关闭 Worker；宿主把它送入 crash/recovery 路径。该最新修改尚未完成全量复跑验证。

- `frontend/src/lib/vscodeExtensionActivation.test.ts`
  - 真实 ABI handshake 适配及故障测试。
  - 上一次验证结果：该文件 48/48；另 `extensionHost.test.ts` 107/107；两者合计 155/155。
  - 新增原始激活异常传播测试，已在诊断修改后通过。

- `frontend/src/e2e/extensionHostG24Probe.ts`
  - 真实 packaged probe：v1/v2 activate、ABI fallback/reject、permission denial、forged token、crash/hang/rate/size recovery、disable/uninstall。
  - 故障后 edit/save/open-file 路径由 packaged driver 继续验证。

- `internal/e2e/extension_host_g24.go`
  - loopback `httptest` registry，真实 VSIX v1/v2 下载、hash、安装、升级、禁用、卸载。
  - crash fixture 先抛出未捕获异常，再安排 Worker 退出，以适应 WebView2 不一定向外转发 `Worker.onerror` 的行为；后续改为 Worker 内部 error bridge 后需重新审视是否保留该退出兜底。

- `scripts/packaged-e2e-driver.mjs`、`scripts/packaged-e2e.mjs`、`internal/e2e/server.go`、`internal/e2e/types.go`、`main.go`、`frontend/src/main.ts`、`services/marketplace_e2e.go`
  - 已接入 G24 e2e build tag、renderer probe、loopback registry 和第 24 个 fixture `extension-host-g24-package`。

### 3.3 已取得的真实结果

第一次真实 packaged 运行：

- 安装和 enable 成功，但 v1 Worker 激活失败，原始错误后来通过诊断修复定位为 `i is not defined`。
- 根因：Vite/Rolldown 将 bootstrap 内 `import(/* @vite-ignore */ blobURL)` 改写为模块级 helper；代码再通过 `function.toString()` 单独放入 Worker Blob，helper 不在 Worker 作用域。

第二次真实 packaged 运行：

- v1/v2 安装、激活、更新、renderer 生命周期前置流程已通过。
- 首个 fault 阶段失败：`Worker never entered the terminated state during recovery`。
- 说明 WebView2 对异步 `throw` 没有按宿主预期触发 `Worker.onerror`，且 Worker 仍响应 heartbeat。

第三次真实 packaged 运行：

- 仍通过构建、v1/v2 生命周期并进入 G24 faults。
- 在最新 Worker 内部 `runtime-error` bridge 修改前失败于同一 recovery 检查；该运行不能证明新修改失败，因为新修改是在其后加入的。

最近一次失败 artifact（仅作失败证据，不得作为通过证据）：

- `bin/koyori-ide.exe`
- SHA-256：`3b70ada71ccf0d79db249e2eb6270310b957c5b282b6675b7cd7e6b51eaed756`
- manifest：`build/e2e-evidence/packaged-e2e/manifest.json`
- manifest status=`failed`，fixture 仅 `not-run`，G23 evidence 仍存在。
- 失败信息：`extension-host-g24-probe failed (422): G24 renderer phase faults failed: Worker never entered the terminated state during recovery`。

### 3.4 G24 已完成记录

1. 最终 packaged manifest 已确认 `status=passed`，24/24 fixtures 通过；保留以下历史定向验证命令作为证据索引：

```powershell
Set-Location frontend
npm.cmd exec vue-tsc -- --noEmit
npm.cmd exec vitest run src/lib/vscodeExtensionActivation.test.ts src/lib/extensionHost/extensionHost.test.ts
npm.cmd run lint
Set-Location ..
gofmt -l internal/e2e/extension_host_g24.go
go test -tags e2e ./internal/e2e -count=1
```

2. 最终真实 packaged 证据：

```powershell
node scripts/packaged-e2e.mjs
```

   - artifact SHA-256 `7e8abff533098129f6cf858dd9278053c71786edbf5858a6003452589f07b181`；source fingerprint `690aa31cad880bf803037ab734207a9e1f7281d9e05140f65daaef15bd7b6180`。
   - 首次本轮 `disabled=true` 且仍 `active=true`；后端 lifecycle stop handshake 已修复。
   - 低内存 full run 的 Git `0xc0000142` 失败后，true `KOYORI_IDE_E2E_SKIP_BUILD=1` 复用既有 artifact 通过。

### 3.5 G24 完成证据归档

packaged 通过后检查 manifest/evidence 至少包含：

- v1/v2 VSIX hash、identity、entrypoint 和版本；
- ABI fallback/reject、协商前拒绝；
- permission denial、forged token ignored；
- crash、hang、rate、size 四种 recovery；
- disabled、uninstalled、故障后 edit/save/open-file；
- 原始日志和 artifact/source fingerprint；
- 不能把安装成功写成 API 兼容或激活成功。

本轮最终门禁归档：Windows `node scripts/backend-gate.mjs` 9/9、exit 0（gofmt 0.6s、vet 15.3s、build 14.5s、Go 全量测试 333.9s、contract 3.1s、bindings 12.6s、pin/docs 各检查 0.x 秒）。首次 gate 因 `TestLSPServiceRealTypeScriptWorkspaceLocalServer` 的 TempDir 被占用而失败；`.cmd -> node` 后代未被 `Process.Kill` 回收是根因，`lspProcess.stop` 改用 `taskkill /PID /T /F` + `Wait` 后，定向 TypeScript LSP 与 LSP 测试通过。WSL `.wslconfig` 8GB -> 6GB 且 `autoMemoryReclaim=gradual` 已生效，`free` 显示 5.8GiB；true skip-build packaged 24/24。普通 production frontend 已重建，五个 E2E marker 扫描为 0。仅记录本地 Windows/WSL 门禁，不声称 git、CI 或 release。

然后按真实生成物回写：

- `docs/prompts/prompt-9.md` 的 G24 章节、AC 和第 8 节进度板；
- `docs/EXTENSION-CONTRIBUTION-PROTOCOL.md`；
- `docs/EXTENSION-COMPATIBILITY.md`；
- `docs/E2E.md`；
- `scripts/packaged-e2e.mjs` manifest goal 字段加入 G24。

## 4. G24 完成后的 Goal 顺序

严格遵循 prompt-9 依赖，不并行修改下一个 Goal：

1. G24 已完成全部 AC 和文档回写；最终 packaged manifest 为 `passed`、24/24，corpus 11/11 且 10 包全 blocked。
2. 按 Goal 顺序，下一候选为 G25；但 G25 依赖 G23/G24，G23 AC2-4 仍为 `U`，不得宣称依赖全部满足或无条件开工。本轮不推进 G25 实现。
3. G25 动态国际化/个性化：先做真实 locale/profile 数据模型和 fail-closed 导入导出，再做 en-US、zh-CN、复杂复数语言、RTL 的 packaged 矩阵；不能只改静态文案或 mock locale。已完成的 T 级基础（2026-08-10）：ICU plural 解析（`Intl.PluralRules` 选类）、locale 元数据（plural categories + RTL 检测）、`formatNumber`、missing-key 监测（`frontend/src/lib/localeMetadata.ts` + `i18n.ts` + `i18n.g25.test.ts` 13 测试）；profile 版本化导入导出（schema v1、secret redact、1 MiB 限制、非法 JSON/未知版本 fail-closed 拒绝，`services/profile_service.go` + 8 个新测试）。下一步：真实 locale 切换 packaged 矩阵与 RTL/bidi 覆盖、语言包翻译安全边界。本轮不推进或修改 G25 实现。
4. G26 Remote Workspace：先做统一 host identity/URI/认证/generation，再做真实 SSH/Linux agent 的 FS/watch/PTY/Git/LSP/Test/DAP、断线重连、冲突和端口转发 packaged 证据；broker/mock/协议文档不算完成。
5. G27 最后处理发布运营、SLO、签名更新回滚、性能/可访问性和外部审计。没有真实 CI/release/三平台历史/外部审计时保持 `U`，不得声称生产级。

## 5. 最终门禁

每次重大修复后，按当前环境可执行项记录原始命令、退出码和证据路径：

```powershell
node scripts/packaged-e2e.mjs
Set-Location frontend
npm.cmd run build
Set-Location ..
node scripts/backend-gate.mjs
node scripts/check-bindings.mjs
node scripts/check-doc-links.mjs
node scripts/check-doc-numbers.mjs
node scripts/check-encoding.mjs
node scripts/generate-license-inventory.mjs --full-check
node scripts/sync-release-metadata.mjs --check
```

最终检查必须恢复普通 production frontend，不能把带 e2e marker 的 `frontend/dist` 或 `bin/koyori-ide.exe` 当普通发布物。本轮普通 production frontend 已重建，五个 E2E marker 扫描为 0。若重建 `bin/production/koyori-ide.exe`，重新记录 SHA-256 和 ProductName=`Koyori IDE`；没有 macOS/CI/签名/外部审计证据的 Goal 保持 `U`。

## 6. 交接输出格式

后续 AI 每次暂停或结束时至少报告：

- 当前 Goal、审计缺口和状态；
- 本轮改动的绝对路径；
- 真实执行的命令、退出码、测试数量；
- packaged artifact、manifest、日志和 SHA-256；
- 每个 AC 的证据等级，未验证项明确写 `U`；
- 阻塞原因及仍可继续的下一个任务；
- 是否已同步 prompt-9/prompt-10；
- 明确说明没有 commit/push/tag/release。

禁止用“代码已实现”“测试通过”代替证据等级和真实运行结果。
