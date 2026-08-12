# Koyori IDE 未完成点总清单与续作任务（prompt-11）

> 用途：把 `docs/prompts/prompt-9.md` 与 `docs/prompts/prompt-10.md` 中**全部未完成点**（未勾选 AC、U 证据、阻塞项、文档内部矛盾）聚合为可直接交给后续 AI 按点执行的 Goal 任务清单。
>
> 与既有文档的关系：prompt-9 是长期执行清单与上位规范（第 0、8、9、10、11 节始终有效）；prompt-10 是 G24 交接细节。本文不替代两者，只做“未完成点 → 任务点 → 验收标准”的总索引与执行顺序，并继承两者已确认的真实证据，不重复已完成工作。
>
> 当前定位：完成 §7 所列 P0/P1 剩余 U 项（真实 CI/macOS/发布证据）前，不得宣称“开源发布合格”；完成 G24/G25/G26/G27 前，不得宣称“全语言、国际化、插件化、远程 IDE”或“生产级”。

## 0. 继承规则（速查，完整条款以 prompt-9 第 0 节为准）

1. 先读代码、测试与运行结果，再接受本文现状。每个任务开始时写明对应缺口“仍存在 / 已变化 / 已不存在”。
2. **一次只推进一个 Goal**，按 §2 顺序选择；不得同时修改下一个 Goal。
3. 每次只做使当前任务闭环的最小正确改动；不得用新增面板、占位按钮、mock 或文档声明掩盖核心工作流断裂。
4. AC 未全部勾选时，Goal 不得写成“完成”；状态必须由 AC 与证据自动推导，叙述、代码量、测试数量不得覆盖失败门禁。
5. 证据分级固定为：`S` 静态检查、`T` 单测/mock/contract、`I` 真实服务/真实进程、`P` 真实 packaged 用户工作流、`R` 真实 CI/tag/release/签名/审计历史、`U` 未验证或环境阻塞。mock/contract 不得升级为 `I/P/R`；dry-run ≠ packaged E2E；YAML 存在 ≠ 真实 CI。
6. 安全能力默认 fail-closed；renderer 传入的 approved/safe/路径/root/私网许可都不是授权。
7. 用户数据优先：保存、恢复、更新、扩展故障必须验证冲突、崩溃、重试、回滚与失败后可恢复。
8. 禁止手工猜 Wails binding ID；使用仓库锁定的 `v3.0.0-alpha2.111` 生成并由检查脚本验证。
9. 不删除测试保绿、不放宽安全断言、不用 `any`/类型压制隐藏问题、不提交 secret、不擅自 commit/push/tag/release。
10. 环境失败不等于项目通过：记录阻塞、原始命令、退出码与仍能完成的静态检查；被阻塞 AC 保持未勾选并标 `U`。
11. 修复后立即回写 prompt-9 进度板与本文档，不得在会话末凭记忆补写。
12. 当前工作区没有可核验 `.git` 元数据；涉及 tracked/untracked、commit、CI、tag、release、历史的一律 `U`。

## 1. 当前状态快照（2026-08-11 复核）

### 1.1 全局

- 27 个 Goal / 103 条 AC：**74 条已勾选，29 条未勾选**。
- **完成（14）**：G02、G03、G04、G05、G06、G11、G12、G14、G15、G17、G18、G20、G22、G24。
- **阻塞（9）**：G01、G07、G08、G09、G10、G13、G19、G21、G23（均为外部状态或语料/发布证据 U，非代码缺失）。
- **进行中（2）**：G16（剩 macOS shell/signal 证据）、G25（T 级基础已完成，AC 未勾选）。
- **未开始（2）**：G26、G27（依赖 G23/G24）。
- 未勾选 AC 分布：G01×1、G07×1、G08×1、G09×3、G10×1、G13×1、G16×2、G19×1、G21×3、G23×3、G25×4、G26×4、G27×4 = 29。
- **开源发布资格：未达成**。G07/G08/G09/G10/G19/G21 的真实 CI/macOS/发布证据仍为 `U`；G27 未开始。

### 1.2 本会话已验证基线（后续 AI 以此对照，避免重复排查）

- `node scripts/backend-gate.mjs`：最终 Windows **9/9 全绿、exit 0**（gofmt 0.6s、vet 15.3s、build 14.5s、`go test ./... -count=1` 333.9s、contract 3.1s、bindings 12.6s、pin/docs 各检查 0.x 秒）。首次 gate 红于 `TestLSPServiceRealTypeScriptWorkspaceLocalServer` TempDir 被占用；根因 `.cmd -> node` 后代未被 `Process.Kill` 回收，`lspProcess.stop` 使用 `taskkill /PID /T /F` + `Wait` 修复后，定向 TypeScript LSP 与 LSP 测试通过。
- `node scripts/check-doc-links.mjs`：OK（23 个 Markdown 文件）。此前 `.github/PULL_REQUEST_TEMPLATE.md` 3 个失效相对链接（`docs/CHANGELOG.md`、`docs/`、`.github/CODE_OF_CONDUCT.md`）已修为 `../docs/CHANGELOG.md`、`../docs/`、`CODE_OF_CONDUCT.md`。
- G24 前端：`vue-tsc --noEmit` exit 0；`vscodeExtensionActivation.test.ts` + `extensionHost.test.ts` **155/155**。
- G24 corpus：`node --test scripts/g24-corpus-report.test.mjs` **11/11**；`build/e2e-evidence/p9-g24/corpus-report.json` = 10 包、10 blocked（缺 `koyoriIde.permissions`）、supported/unsupported/corrupt 均为 0、无重复 identity。
- 后端：`gofmt -l internal/e2e/extension_host_g24.go` 空；`go test -tags e2e ./internal/e2e -count=1` ok。
- **最新 packaged 证据是 G24 通过运行**：`build/e2e-evidence/packaged-e2e/manifest.json` status=`passed`，24/24 fixtures passed，artifact SHA-256 `7e8abff533098129f6cf858dd9278053c71786edbf5858a6003452589f07b181`，source fingerprint `690aa31cad880bf803037ab734207a9e1f7281d9e05140f65daaef15bd7b6180`，recordedAt `2026-08-11T03:23:53.760Z`；manifest `gitMetadataAvailable=false`，不作 git/CI 声明。
- WSL `.wslconfig` 8GB -> 6GB 且 `autoMemoryReclaim=gradual` 已生效，`free` 显示 5.8GiB；true skip-build packaged 24/24。普通 production frontend 已重建，五个 E2E marker 扫描为 0。
- 本会话已修复的回归：① `check-doc-links` 3 个失效链接；② `services/language_pack_rust_integration_test.go` 的 rust-analyzer 冷启动 flake（hover/completion 重试期限 20s→45s；原 20s 在负载下偶发 `raw=null` 20 秒不返回）；③ `docs/THIRD_PARTY_LICENSES.md` 重新生成（G17 门禁）。
- i18n/profile 实测数量：i18n 全量 **53/53**（`i18n.test.ts`+`i18n.g25.test.ts`+`localeMetadata.test.ts`）；`profile_service_test.go` 40 个顶层测试函数（其中 G25 新增导入导出 8 个）。

## 2. 推进顺序（一次只推进一个 Goal）

1. **G24（已完成）**：AC1-4 已闭环并完成文档回写。
2. **G25（下一候选，暂不得开工）**：依赖 G23/G24；G24 已闭环，但 G23 AC2-4 仍为 `U`，不得宣称依赖全部满足或无条件开工；G25 AC 仍未完成，本轮不推进其实现。
3. **G26（Unified Remote Workspace Host）**：依赖 G23/G24；真实 SSH/Linux agent 证据，禁止仅 broker/mock/文档。
4. **G27（发布运营、SLO、更新回滚与外部审计）**：最后处理，全部 `R` 级证据。

补充规则：

- G23 的 AC2 已有部分 `I` 证据（真实 Python/Rust 本地 LSP/toolchain/DAP 通过），但 AC2/3/4 仍 U；G23 **不阻塞 G24**。
- §7 中 P0/P1 的 U 项被外部状态（macOS runner、真实 CI、可核验 `.git`、代表性语料、发布机）阻塞：在外部状态可得前**保持 U 并如实记录**，不得伪造；若当前 Goal 与外部状态无关，可在不影响当前 Goal 的前提下先准备脚本与验收清单，但不得并行推进多个 Goal。
## 3. 已完成 Goal：P9-G24（建立独立 Extension Host 和版本化贡献协议）

### 3.1 现状（承接 prompt-10 §3，已复核）

- 代码主体已实现：`frontend/src/lib/vscodeExtensionActivation.ts`（Dedicated Worker ABI 1.0 协商、随机 token、heartbeat 2s/8s、4 MiB/message + 1000 msg/s 配额、crash/hang/quota 恢复、`WorkerExtensionModule`、激活诊断、bundle 闭包修复、Worker 内部 `runtime-error` 事件桥）；`internal/e2e/extension_host_g24.go`（loopback registry、真实 VSIX v1/v2 下载/hash/安装/升级/禁用/卸载）；`frontend/src/e2e/extensionHostG24Probe.ts`；packaged-e2e 已接入第 24 个 fixture `extension-host-g24-package`。
- T 级证据齐备：前端 155/155；`vue-tsc` exit 0；`go test -tags e2e ./internal/e2e` ok；corpus 脚本 11/11、真实 corpus 报告 10 blocked。
- 真实 packaged：最终 24/24 通过；首次本轮失败记录 `disabled=true` 且仍 `active=true`，后端 lifecycle stop handshake 已修复；低内存 full run 的 Git `0xc0000142` 失败后，true `KOYORI_IDE_E2E_SKIP_BUILD=1` 复用既有 artifact 通过。
- AC 勾选状态：prompt-9 G24 AC 正文与进度板均为 4/4，G24 已完成。

### 3.2 任务点

**T-G24-1（AC3 复核与同步）**：已完成。corpus 证据复核通过并同步文档勾选。
- 现状：脚本测试 11/11；`corpus-report.json` 10 包全 blocked、无假成功（安装成功未当激活成功）。
- 任务：确认脚本覆盖成功/损坏包/缺 entrypoint/未知 API/权限缺失/重复 identity/空 corpus 用例；确认报告含 identity/version/hash、entrypoint、contributions、API 引用、supported/unsupported/blocked 原因。
- 验收：已满足；prompt-9 的 G24 AC3 已勾选，进度板已同步，prompt-10 §3.6 过时矛盾已删除。

**T-G24-2（runtime-error bridge 全量复跑）**：已完成；`WorkerScope.addEventListener` 类型、内部 `runtime-error` 发送、host switch 分支、token/ABI 顺序无类型或安全回归。
- 现状：该修改已在最终 packaged 24/24 运行中复核。
- 任务：保留最终 manifest 与相关日志作为当前代码态证据。
- 验收：`vue-tsc` exit 0；155/155；`npm run lint` exit 0；`gofmt -l internal/e2e/extension_host_g24.go` 空；`go test -tags e2e ./internal/e2e -count=1` ok。

**T-G24-3（AC4：真实 packaged 24/24 闭环）**：已完成；manifest `status=passed`，24/24 fixtures 通过。
- 现状：最终运行已覆盖历史 recovery 与 post-G24 edit/save 路径。
- 任务：保留首次失败、修复与最终通过的原始证据，禁止泛化成 `Extension activation failed: <id>`。
- 验收：manifest status=`passed`、24/24 fixtures passed、G24 相关证据齐全（见 prompt-10 §3.5 证据清单）、AC4 勾选。

**T-G24-4（AC1：host 崩溃/卡死/超配额时主 IDE 可继续编辑保存并重启 host）**：已完成；
- 现状：最终 packaged 证据包含故障后 edit/save/open-file、host 重启与无数据丢失。
- 任务：保留归档证据。
- 验收：已满足，AC1 已勾选。

**T-G24-5（AC2：ABI 协商、权限拒绝、消息伪造 fail-closed）**：已完成；
- 现状：ABI fallback/reject、协商前拒绝、forged token ignored、permission denied 在最终 packaged 运行中通过。
- 任务：保留最终 manifest/证据目录与当前代码态关联。
- 验收：已满足，AC2 已勾选。

**T-G24-6（四个故障专项检查）**：
1. 真正未捕获异常是否产生 `runtime-error` 或外部 `onerror`；
2. `globalThis.close()`/`scope.close()` 后 heartbeat 是否在 8 秒内终止并进入恢复；
3. hang 的 busy loop 是否由 heartbeat watchdog 终止；
4. rate/size quota 是否 fail-closed，且恢复后命令与保存仍可用。
- 验收：四项各有真实 packaged 断言与原始日志；失败按 §0 规则 10 与 T-G24-9 记录。

**T-G24-7（禁止假成功）**：不得用 probe 主动 `deactivateExtension`、手工重建状态或直接设置 `isExtensionActivated=false` 掩盖恢复缺陷；crash fixture 在 runtime-error bridge 落地后重新审视“先抛未捕获异常再安排 Worker 退出”的兜底是否仍需保留。

**T-G24-8（证据与文档回写）**：packaged 通过后检查 manifest/evidence 至少包含：v1/v2 VSIX hash/identity/entrypoint/版本、ABI fallback/reject、协商前拒绝、permission denial、forged token ignored、crash/hang/rate/size 四种 recovery、disabled/uninstalled、故障后 edit/save/open-file、原始日志与 artifact/source fingerprint；不得把安装成功写成 API 兼容或激活成功。然后按真实生成物回写：prompt-9 的 G24 章节/AC/第 8 节进度板、`docs/EXTENSION-CONTRIBUTION-PROTOCOL.md`、`docs/EXTENSION-COMPATIBILITY.md`、`docs/E2E.md`、`scripts/packaged-e2e.mjs` manifest goal 字段。

**T-G24-9（环境约束与失败处置）**：runner 内存 <2GB 会触发 WindowsTerminal OOM（`Windows.UI.Xaml.dll` 0xc0000005）导致运行中断。若复现，先释放宿主内存或拆分 faults 阶段复跑；每次失败保留 manifest/日志/SHA-256，`U` 不得升级为通过。
## 4. P9-G25（实现动态国际化与可移植个性化）按点任务

**后续候选前提：G25 依赖 G23/G24；G24 已闭环，但 G23 AC2-4 仍为 `U`，因此不得宣称依赖全部满足或无条件开工。以下 G25 T 级基础已完成（2026-08-10），不要重复实现；本轮不推进 G25。**

已完成的 T 级基础：ICU plural 解析（`Intl.PluralRules` 选类，ru/pl/ar 真实 few/many/zero/two 验证）、locale 元数据（plural categories + RTL 检测 + Intl 缺失 fallback）、`formatNumber`、missing-key 监测（`frontend/src/lib/localeMetadata.ts` + `i18n.ts` + `i18n.g25.test.ts`；i18n 全量 53 测试）；profile 版本化导入导出（schema v1、顶层 `aiApiKey` 与嵌套 `aiProviderConfigs[].apiKey` redact、1 MiB 限制、非法 JSON/未知版本/原型污染键 fail-closed 拒绝；`services/profile_service.go` + 8 个新增导入导出测试，文件共 40 个顶层测试）。

**T-G25-1（AC1：packaged 矩阵）**：至少 `en-US`、`zh-CN`、一种复数复杂语言（如 ru/pl/ar）和一种 RTL 语言（如 ar/he）通过 packaged 切换矩阵。
- 现状：全部 `U`（packaged 未运行）。
- 任务：真实 locale 运行时切换（无需重启、不丢编辑状态）→ packaged 矩阵（每语言：启动、切换、关键界面字符串、RTL 布局）。
- 验收：packaged manifest/截图/日志覆盖 4 类语言；AC1 勾选。

**T-G25-2（AC2：无拼接错误、缺键可监测）**：
- 现状：`T` 已有 53 测试；文档数字不一致（G25 AC2 写 49、进度板写 53、prompt-10 §4 写 13）。
- 任务：i18n 全量 53/53 复核（已确认通过）；把 G25 AC2 的“49 个”修正为“53 个”；补充 `I` 级真实打包产物中缺键不显示 raw key 的验证。
- 验收：文案数字统一；缺失键在真实产物中 fallback 且不显示 raw key；AC2 勾选。

**T-G25-3（AC3：插件/语言包可提供翻译且不能覆盖系统安全提示）**：
- 现状：仅 `S`（安全提示覆盖保护未实现）。
- 任务：定义系统安全提示（权限、私网、危险操作等）的翻译来源优先级——语言包翻译**不得覆盖**安全提示文案；实现保护并补绕过测试。
- 验收：语言包注入的安全提示覆盖尝试被拒绝；T 级测试 + 必要时 I/P；AC3 勾选。

**T-G25-4（AC4：profile 跨平台导入安全）**：
- 现状：`T` 已覆盖 schema 版本化、redact、大小限制、非法 JSON/未知版本/原型污染拒绝、未知字段保留。
- 任务：恶意文件（原型污染、超限、畸形、路径穿越键）与跨平台 packaged 导入矩阵（Windows 导出 → Linux/macOS 导入及反向）真实运行。
- 验收：packaged 导入矩阵通过且敏感字段不落盘；AC4 勾选。

**T-G25-5（执行点补全）**：ICU message 全量迁移（所有用户可见字符串使用稳定 message ID，启动/后端错误也可本地化）；lazy locale pack；缺失键按确定链 fallback；pseudo locale 截断检测；RTL/bidi 覆盖 editor 外 UI、路径与代码片段隔离；profile 可预览、合并、回滚且 secret 不进入导出。

## 5. P9-G26（实现 Unified Remote Workspace Host）按点任务

**现状：Remote 协议文档不等于远程 IDE。G26 Phase 1–3 已完成 T 级统一 URI/Scope、本地 Host Adapter、Linux root-fd + `openat2` no-follow、非 Linux fail-closed、Windows `MoveFileExW` 替换代码，以及 SSH verified HostID/connection nonce/approval scope。真实 Windows NTFS、持续同目录恶意竞争、remote agent、host-issued workspace ID、远端 FS/PTY/Git/LSP/DAP/Test、重连和 packaged 证据仍为 `U`，全部 AC 未勾选。**

**T-G26-1（统一模型）**：workspace URI 明确 scheme/authority/path；host identity、认证、generation 统一；所有服务按 host 路由，本地/远端路径不混用。
**T-G26-2（远端 agent）**：版本/能力协商、最小权限安装、签名升级与回滚。
**T-G26-3（真实远端闭环）**：真实 SSH/Linux agent 完成编辑保存、watch、Terminal、Git、LSP、Test、DAP（AC1）。
**T-G26-4（断线重连）**：断网、host 重启、客户端重启后状态可收敛且无重复写；离线编辑冲突不静默覆盖（AC2）。
**T-G26-5（安全隔离）**：本地/远端路径不串用，跨 host token 不可重放；端口转发默认 loopback、显式授权；SSRF、凭据与日志脱敏纳入威胁模型（AC3）。
**T-G26-6（packaged 证据）**：高延迟、大仓库、端口转发和多根 workspace 的 packaged 证据（AC4）。
**禁止**：仅完成 broker/mock/协议文档即勾选。最低证据 `I` + `P`。

### 会话交付：P11-T-G26-Foundation（2026-08-11）

- 复核结论：G26 缺口已变化但仍存在，状态为进行中，AC 0/4。
- T 级基础：新增 canonical `WorkspaceURI`、`WorkspaceRef`、`WorkspaceScope` 和 typed errors；本地 Host Adapter 使用 generation-bound root fd、Linux `openat2` no-follow 和 fd-relative 写入，非 Linux 文件操作 fail-closed；Windows 原子替换代码使用 `MoveFileExW(REPLACE_EXISTING|WRITE_THROUGH)`。
- SSH 安全边界：HostID 仅在 known_hosts 验证成功后由 SSH public key digest 生成；每次连接生成随机 instance nonce；审批绑定 HostID、nonce、session-only WorkspaceID、generation、argv digest、TTL、single-use 和 HMAC，断连/重连令旧 token 失效。
- 验证：G26 定向 Go 测试 exit 0；Linux root-handle/concurrency 测试与 `-race` exit 0；Windows build-tag compile-only exit 0；docs links/numbers/encoding 门禁 exit 0。三个阶段均经过 Oracle 门禁，Phase 2/3 的 T 级基础获准结束。
- 未验证：真实 Windows NTFS 替换行为、非 Linux no-follow 文件能力、持续同目录恶意竞争者命中 `fstatat`→`renameat/unlinkat` 窗口、完整断电持久性、host-issued workspace ID、remote agent、远端 FS/watch/PTY/Git/LSP/Test/DAP、断线收敛、高延迟/大仓库/端口转发/多根 packaged 证据均为 `U`。
- 结论限制：现有 SSH/SFTP 与本轮 T 级 Host 基础不等于 Unified Remote Workspace Host，不勾选任何 G26 AC，不进入 G27 的完成声明。
- 版本控制：本轮创建本地 commit，不 push/tag/release；用户现有 MSI/build 脚本改动不纳入提交。

## 6. P9-G27（建立发布运营、SLO、更新回滚与外部审计）按点任务

**现状：没有可核验的真实 crash/startup/edit durability 数据、稳定发布历史或独立审计；全部 AC 未勾选（U）。最低证据 `R`；完成本 Goal 才可评估“广泛日常可用/生产级”，不自动等于结论。**

**续作状态（2026-08-12）：** G27 Phase 1 T 级基础已实现但不构成 R 级证据：Ed25519 manifest/rollback authorization 绑定 channel、platform、arch、artifact、commit、transaction、nonce 并支持进程内一次性消费；更新事务状态机拒绝非法跳转；运营事件 schema v1 默认关闭、local-only、bounded、隐私 fail-closed。真实发布密钥/三平台 release/SLO cohort/外部审计仍为 `U`。

**T-G27-1（签名更新与回滚）**：更新 manifest、包、channel 全部签名；失败/损坏/降级/撤回可自动回滚且保留用户数据；至少三个不同 commit 的真实三平台 release 通过签名更新与回滚演练（AC1）。
**T-G27-2（SLO）**：定义并采集启动、崩溃、无响应、编辑持久性、LSP 延迟、扩展崩溃 SLI；默认最小化、可退出；连续稳定窗口达到已发布 SLO，原始查询与样本偏差可审计（AC2）。
**T-G27-3（性能与可访问性门禁）**：建立性能预算（大仓库场景）；WCAG 2.2 AA 核心流程门禁（AC3）。
**T-G27-4（外部审计）**：委托独立安全、供应链与可访问性审计；P0/P1 问题公开处置并复测（AC4）。

### 会话交付：P11-T-G27-Foundation（2026-08-12）

- 复核结论：G27 缺口已变化但仍存在；Phase 1 仅完成 T 级基础，AC 仍为 0/4，不能宣称 R 级或生产级。
- 改动文件：新增 `services/update_manifest_g27.go` / 测试，提供 Ed25519 manifest 验证、跨平台 artifact 名称校验、channel/版本/降级授权 scope、进程内 replay consume 和 update transaction journal；新增 `services/operational_events_g27.go` / 测试，提供默认关闭的本地 bounded v1 事件 buffer、严格枚举 schema、随机 session ID 和隐私拒绝。
- 验证：`go test ./services -run 'UpdateManifest|UpdateTransaction|RollbackAuthorization|OperationalEvent|OperationalBuffer|SessionID' -count=1` exit 0；同集合 `-race` exit 0。Wails/GTK 仅有既有 deprecated warnings。
- 门禁限制：本轮 Oracle 复核 session 异常退出，未把其当作通过证据；代码审查与定向测试仅证明 T 级基础。真实发布签名密钥托管、三个 commit 的三平台 release/update/rollback、持久化跨进程 replay 防护、生产 SLO、性能/WCAG、独立安全/供应链/可访问性审计均为 `U`。
- 下一步：补齐持久化更新 journal 与性能/WCAG 审计清单前，不得勾选 G27 AC；真实 R 级证据需发布机、release cohort 和独立 assessor。
## 7. P0/P1 未完成点（外部状态阻塞，保持 `U`）按点任务

> 本节每个点都被外部状态阻塞（macOS runner、真实 CI、可核验 `.git`、代表性语料、发布机）。任务 = 在外部状态可得时按验收闭环；不可得时保持 `U` 并记录阻塞，**不得伪造通过**。可在不影响当前 Goal 的前提下准备脚本与验收清单。

**T-P0-G01-AC3（G01 AC3：`.gitignore`/构建/CI 与生成物归属一致）**
- 现状：manifest 与 `.gitignore` 声明 `untracked-generate-before-use`；`check-bindings-ownership.mjs` 在 CI/package/release 前执行 `git ls-files -- frontend/bindings`；cross 镜像固定 Node 22.14.0 并校验 SHA-256。
- 缺口：工作区 `.git` 为空目录，tracked/untracked 归属不可核验；Docker 镜像构建仍 `U`。
- 验收（需外部状态）：恢复可核验 `.git` 后确认 untracked 归属、`check-bindings-ownership` 通过、cross 镜像在真实 CI 构建成功；AC3 勾选。

**T-P0-G07-AC3（G07 AC3：Linux/macOS CI 同一测试集）**
- 现状：Windows 腿（含 `-race` 同包集合）通过；Linux 腿已在 WSL2 真实 Linux 通过（`scripts/wsl-linux-gate.sh` 可复现）；`ci.yml` 三平台矩阵与 `-count=1` 由测试锁定。
- 缺口：真实 macOS 与 CI runner `U`（公开 GitHub API/Actions 不可达）。
- 验收（需外部状态）：ci.yml `go-test` 作业三平台同命令通过；`contract-smoke` ubuntu 必过/win/mac 可选；macOS 腿有真实 run ID；AC3 勾选。

**T-P0-G08-AC3（G08 AC3：三平台 packaged 产物抽取验证）**
- 现状：Windows（Win32 API 抽取 0.2.0.0）与 Linux（真实 .deb + dpkg-deb 抽取 0.2.0-1）通过。
- 缺口：macOS 腿 `U`。
- 验收（需外部状态）：真实 macOS 产物经 Info.plist/`codesign` 等抽取验证与 SSOT 一致；AC3 勾选。

**T-P0-G09（G09：macOS release workflow 的 BSD 可移植性）— 3 个未勾选 AC**
- 现状：AC2 已勾选（WSL2 真实 bash 行为验证空/多产物/空格路径/错误架构 fail-closed）；release.yml 已改 bash 3.2/BSD 兼容写法；Windows GNU Bash 5.3 不能替代 macOS Bash 3.2。
- 缺口：
  - AC1：macOS 默认 shell（Bash 3.2）运行脚本通过 —— `U`（WSL 编译 Bash 3.2 三次失败已记录）；
  - AC3：artifact 架构/版本/checksum 可核验 —— `T/U`（脚本与 fallback 测试已有，真实抽取需 macOS）；
  - AC4：至少一条真实 macOS CI 历史链接/运行 ID —— `U`。
- 验收（需外部状态）：真实 macOS runner 执行 release 脚本 exit 0、artifact 元数据核验、记录 CI run ID。

**T-P0-G10-AC2（G10 AC2：macOS 与 Linux packaged 同一矩阵）**
- 现状：Windows packaged 23/23 fixtures 通过。
- 缺口：macOS/Linux packaged 矩阵 `U`；Linux 可在 WSL 尝试（未完成）。
- 验收（需外部状态）：macOS/Linux 同一矩阵通过，例外有 issue 与用户可见限制；AC2 勾选。

**T-P1-G13-AC4（G13 AC4：corpus 统计）**
- 现状：no-fake-success 审计与兼容矩阵完成；真实 packaged `extension-api-g13-package` 通过。
- 缺口：无代表性可激活扩展 corpus 与统计运行；现有 Open VSX 10 包语料只能证明 G20 安全拒绝，不能产出 G13 激活/API 成功率。
- 验收：记录 corpus 总数、可激活率、核心 API 成功率与失败原因（最低 `I` + `P`）；AC4 勾选。

**T-P1-G16（G16：Terminal 配置与退出协议）— 2 个未勾选 AC**
- 现状：Windows 真实 exit 7/非法 shell fail-closed/resize/重连 packaged 通过；Linux/WSL 真实 `sh` 与 `SIGTERM`（`TestUnixPty_RealSignalExit`）通过；前端 24/24、29/29。
- 缺口：AC1 三平台默认与自定义 shell 真实启动（macOS `U`）；AC3 exit/signal/启动失败/resize/重连 UI（macOS signal `U`）。
- 验收（需外部状态）：macOS 默认/自定义 shell 启动与 signal/exit 协议真实证据；AC1/AC3 勾选。

**T-P1-G19-AC4（G19 AC4：真实 CI 保存 dependency/audit 证据）**
- 现状：npm audit 各级清零；lockfile 672 个 URL 统一官方 registry；`npm-audit-gate` 通过；ci.yml/release.yml 已接统一门禁。
- 缺口：真实 CI runner 不可执行（`.git` 不可核验）。
- 验收（需外部状态）：真实 CI 运行保存 `npm ci`/audit 证据与 lockfile 稳定性记录；AC4 勾选。

**T-P1-G21（G21：许可证、SBOM、签名与 provenance）— 3 个未勾选 AC**
- 现状：license inventory UNKNOWN/UNRESOLVED=0；本机 Windows artifact 的 SPDX-2.3 逐 artifact 扫描与 digest 绑定已通过。
- 缺口：
  - AC2：每个平台 artifact 的 SPDX/CycloneDX SBOM（三平台与真实 release run `U`）；
  - AC3：checksum、签名/公证、provenance 可由发布机外验证（未实现证据）；
  - AC4：真实 release run 保存全部证据并在任一缺失时失败（`U`）。
- 验收（需外部状态）：三平台 artifact 各有 SBOM 且 digest 绑定；发布机外可验证 checksum/签名/provenance；真实 release run 全证据归档；AC2/3/4 勾选。

**T-P2-G23（G23：Language Pack Runtime/SDK）— 3 个未勾选 AC**
- 现状：AC1 完成（Go/TS 经语言包完成，真实 Python/Rust 本地 LSP/toolchain/DAP 通过）；packaged 23/23 fixtures 通过。
- 缺口：
  - AC2：第三方语言包无需修改 IDE 核心即可安装并贡献完整能力（packaged probe 未运行完整 LSP/DAP；服务 payload installer、跨平台 packaged、remote language host `U`）；
  - AC3：平台 installer 有 checksum/signature、版本 pin 与卸载回滚（实现存在，真实 installer 证据 `U`）；
  - AC4：大仓库、多根、远程和离线矩阵（`U`）。
- 验收：packaged 语言包完整 LSP/DAP 闭环；installer 校验/回滚真实证据；矩阵通过；AC2/3/4 勾选。

## 8. 文档维护与门禁任务

**D1（G24 勾选不同步）**：已解决。prompt-9 G24 AC 正文、prompt-10 §3.1/§4 与 prompt-9 §8 进度板已统一为 AC 4/4、状态完成。
**D2（G01 分母不一致）**：进度板写 3/4，AC 正文只有 3 条、勾 2 条（2/3）。核对 AC 条目数后统一。
**D3（corpus 测试数不一致）**：已解决。prompt-9、prompt-10 与本文件统一记录 `node --test scripts/g24-corpus-report.test.mjs` 为 11/11，真实 corpus 为 10 包全 blocked。
**D4（prompt-10 §3.6 自相矛盾）**：已解决。已删除 prompt-10 §3.6 过时的“G24 packaged 通过后再做，不要提前宣称 AC3”整段。
**D5（i18n 测试数不一致）**：G25 AC2 写“49 个 i18n 测试”，进度板写 53，prompt-10 §4 写 13（仅 i18n.g25.test.ts）。实测 i18n 全量 53/53；统一为 53，并注明 13 = i18n.g25.test.ts 单独数量。
**D6（G01 进度板过时注记）**：G01 行仍写“npm audit 1 High”，G19 已清零；更新为“npm audit 各级 0（见 G19）”。
**D7（最终门禁）**：每次重大修复后按顺序执行并记录原始命令/退出码/证据路径：
```powershell
node scripts/packaged-e2e.mjs
Set-Location frontend; npm.cmd run build; Set-Location ..
node scripts/backend-gate.mjs
node scripts/check-bindings.mjs
node scripts/check-doc-links.mjs
node scripts/check-doc-numbers.mjs
node scripts/check-encoding.mjs
node scripts/generate-license-inventory.mjs --full-check
node scripts/sync-release-metadata.mjs --check
```
最终检查必须恢复普通 production frontend；重建 `bin/production/koyori-ide.exe` 时重新记录 SHA-256 与 ProductName=`Koyori IDE`。
**D8（回写纪律）**：任何 AC 勾选/状态变化必须当轮同步 prompt-9 第 8 节进度板、AC 正文与本文件；不得在会话末凭记忆补写。

## 9. 会话交付模板（沿用 prompt-9 §9 / prompt-10 §6）

### 会话交付：P11-T-G24（2026-08-11）

- 复核结论：缺口已不存在；G24 状态为完成，AC 4/4。
- AC：AC1–AC4 全部 `[x]`，证据为 `T/I/P`；manifest `status=passed`，24/24 fixtures passed。
- 证据：artifact SHA-256 `7e8abff533098129f6cf858dd9278053c71786edbf5858a6003452589f07b181`；source fingerprint `690aa31cad880bf803037ab734207a9e1f7281d9e05140f65daaef15bd7b6180`；`recordedAt=2026-08-11T03:23:53.760Z`；corpus 11/11，10 包全 blocked。
- 首次失败：本轮首次结果为 `disabled=true` 且仍 `active=true`；修复后端 lifecycle stop handshake。低内存 full run 遇 Git `0xc0000142`，随后 true `KOYORI_IDE_E2E_SKIP_BUILD=1` 复用既有 artifact 通过。
- 最终门禁：Windows backend-gate 9/9、exit 0（gofmt 0.6s、vet 15.3s、build 14.5s、Go 全量测试 333.9s、contract 3.1s、bindings 12.6s、pin/docs 各检查 0.x 秒）。首次 gate 的 TypeScript LSP TempDir 占用由 `.cmd -> node` 后代未回收导致；`lspProcess.stop` 使用 `taskkill /PID /T /F` + `Wait` 修复，定向 TypeScript LSP 与 LSP 测试通过。
- 环境与 production：WSL `.wslconfig` 8GB -> 6GB、`autoMemoryReclaim=gradual` 已生效，`free` 为 5.8GiB；true skip-build packaged 24/24；普通 production frontend 已重建，五个 E2E marker 扫描为 0。
- 未验证/限制：manifest `gitMetadataAvailable=false`；本轮不声称 git、CI、release，也不推进或修改 G25 实现。
- SSOT 回写：已同步 prompt-9 G24 AC/§8 进度板、prompt-10 G24 章节与 prompt-11 全局计数/D1/D3/D4；没有 commit/push/tag/release。

```markdown
### 会话交付：P11-T<编号>（对应 P9-Gxx ACn）

- 复核结论：缺口仍存在 / 已变化 / 已不存在（附源码与测试证据）
- 本次状态：未开始 -> 进行中 / 阻塞 / 完成
- 改动文件：逐项说明行为变化，不把生成文件与手写源码混淆
- AC：逐条 `[x]/[ ]`，每条附 S/T/I/P/R/U
- 验证：命令、退出码、测试数、真实进程/产物 SHA-256、截图或 CI run
- 首次失败：保留失败原因与修复，不隐藏红灯
- 安全与数据：说明 fail-closed、回滚、迁移和兼容性
- 未验证：明确环境、凭据、平台或历史阻塞，及可复现步骤
- 下一步：只写当前 Goal 的下一条未完成 AC；完成后才指向下一个 Goal
- SSOT 回写：更新 prompt-9 AC 与第 8 节进度板、本文件
- 明确说明没有 commit/push/tag/release（工作区无 `.git`）
```

## 10. 一键续作词（G24 已完成）

```text
严格按 docs/prompts/prompt-11.md 执行；prompt-9 为上位规范，prompt-10 §3 为 G24 细节。先读第 0、1、2 节，复核当前代码与最新 packaged/manifest 证据；G24 已完成，按 §2 顺序下一候选为 G25，但 G25 依赖 G23/G24，G23 AC2-4 仍为 `U`，不得宣称依赖全部满足或无条件开工；本轮不推进 G25 实现。开始前写明审计缺口“仍存在 / 已变化 / 已不存在”。所有 AC 以 S/T/I/P/R/U 分级，mock/contract 不得冒充真实集成、packaged 或 CI 证据；未勾选 AC 不得宣称完成；被外部状态阻塞的 U 项（§7）保持 U 并记录，不得伪造。安全修改先补绕过失败测试，用户数据修改先证明冲突、崩溃和回滚。不要手工猜 Wails binding ID，必须用项目锁定版本生成。完成或暂停时按第 9 节交付，并立即回写 prompt-9 的 AC 与第 8 节进度板、prompt-10 与本文件。不要 commit、push、tag 或发布，除非用户明确要求。
```

## 附录 A：29 条未勾选 AC → 本文任务映射

| Goal | 未勾选 AC | 本文任务 |
|---|---|---|
| G01 | AC3（.gitignore/构建/CI 归属） | §7 T-P0-G01-AC3 |
| G07 | AC3（Linux/macOS CI 矩阵） | §7 T-P0-G07-AC3 |
| G08 | AC3（macOS packaged 抽取） | §7 T-P0-G08-AC3 |
| G09 | AC1、AC3、AC4（macOS 三项） | §7 T-P0-G09 |
| G10 | AC2（macOS/Linux packaged 矩阵） | §7 T-P0-G10-AC2 |
| G13 | AC4（corpus 统计） | §7 T-P1-G13-AC4 |
| G16 | AC1、AC3（macOS shell/signal） | §7 T-P1-G16 |
| G19 | AC4（真实 CI audit 证据） | §7 T-P1-G19-AC4 |
| G21 | AC2、AC3、AC4（SBOM/签名/provenance/release） | §7 T-P1-G21 |
| G23 | AC2、AC3、AC4（语言包/installer/矩阵） | §7 T-P2-G23 |
| G25 | AC1–AC4（i18n/profile） | §4 T-G25-1..5 |
| G26 | AC1–AC4（Remote） | §5 T-G26-1..6 |
| G27 | AC1–AC4（发布运营） | §6 T-G27-1..4 |
