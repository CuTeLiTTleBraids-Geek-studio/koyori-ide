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
12. 2026-08-14 当前工作区可读取 `.git` 与 HEAD `18b43cf0825f1e280dc56b54563c8f73506bbd36`，但工作树不干净；本地 commit/HEAD 不等于真实 CI、tag、release 或发布历史。只有实际 run/tag/release 证据才能把对应项从 `U` 升为 `R`。

## 1. 当前状态快照（2026-08-14 复核）

### 1.1 全局

- 27 个 Goal / 103 条 AC：**74 条已勾选，29 条未勾选**。
- **完成（14）**：G02、G03、G04、G05、G06、G11、G12、G14、G15、G17、G18、G20、G22、G24。
- **阻塞（9）**：G01、G07、G08、G09、G10、G13、G19、G21、G23（均为外部状态或语料/发布证据 U，非代码缺失）。
- **进行中（4）**：G16（剩 macOS shell/signal 证据）、G25（T 级基础已完成，AC 未勾选）、G26（T 级 Host 基础，AC 0/4）、G27（T 级发布运营基础，AC 0/4）。
- **未开始（0）**：本文件 G01~G27 范围内无未开始 Goal；P12-G28~G33 属 prompt-12 §11 的后续范围。
- 未勾选 AC 分布：G01×1、G07×1、G08×1、G09×3、G10×1、G13×1、G16×2、G19×1、G21×3、G23×3、G25×4、G26×4、G27×4 = 29。
- **开源发布资格：未达成**。G07/G08/G09/G10/G19/G21 的真实 CI/macOS/发布证据仍为 `U`；G27 未开始。

### 1.2 本会话已验证基线（后续 AI 以此对照，避免重复排查）

- `node scripts/backend-gate.mjs`：2026-08-14 最终 Windows **9/9 全绿、exit 0**。本次第一次全量 Go 腿偶发失败；独立复跑与第二次完整 gate 均 exit 0，未隐藏首次红灯。2026-08-11 的 TypeScript LSP TempDir 首次失败/修复记录保留为历史。
- `task frontend:check`：**172 test files / 2739 tests**，Vitest、ESLint、`vue-tsc`、bindings、docs 全部 exit 0。独立 `node scripts/check-bindings.mjs` exit 0；`check-doc-links.mjs` exit 0（25 Markdown）；`check-doc-numbers.mjs` exit 0。
- G24 定向：runtime recovery + activation 51/51；`go test -tags e2e ./internal/e2e -count=1` exit 0；H3 extension integrity 前端 3 files / 41 tests exit 0。
- G24 corpus：`node --test scripts/g24-corpus-report.test.mjs` **11/11**；`build/e2e-evidence/p9-g24/corpus-report.json` = 10 包、10 blocked（缺 `koyoriIde.permissions`）、supported/unsupported/corrupt 均为 0、无重复 identity。
- 后端：`gofmt -l internal/e2e/extension_host_g24.go` 空；`go test -tags e2e ./internal/e2e -count=1` ok。
- **历史 packaged 基线（2026-08-14）**：`build/e2e-evidence/packaged-e2e/manifest.json` 当时为 `status=passed`、phase=`complete`、24/24 fixtures passed；artifact SHA-256 `9e382df322b7ea42f881a6cc656c029c5e32432cac84286b162f1a5c39caaa1a`，source fingerprint `09635b427d2fc8852049c5b44550ed8ed2a49afc7ef4aabd097401865fcf8485`，recordedAt `2026-08-14T10:26:36.958Z`，completedAt `2026-08-14T10:29:00.371Z`，HEAD `18b43cf0825f1e280dc56b54563c8f73506bbd36`。该记录仅作历史对照；当前权威 manifest 见 prompt-12 §13.30。工作树不干净，HEAD 不代表全部被测源码；P 级结论仍绑定 artifact digest/manifest/launch 日志。`screenshot=null`，旧 `window.png` 不作本次证据。
- **当前代码态 packaged 证据（2026-08-17 Windows 本机）**：§13.30 记录的 manifest 为 `status=passed`、`phase=complete`、24/24 passed、0 failed/0 not-run，`artifactReused=false`、`sourceFingerprintStableAfterBuild=true`；artifact SHA-256 `65ca828752f20590edfd88987c84a1548e99167d79f71e909b70192ca3098300`，`build-inputs-v2` source fingerprint `d6033de324af040178161221fb6890041a0cc064a284cceed45974cc1abf84d3`（1004 files），recordedAt `2026-08-17T10:25:06.435Z`、completedAt `2026-08-17T10:28:31.707Z`。这是 Windows 本机 `P`，不升级为 macOS/Linux、CI 或 release `R`。
- 本次第一次 packaged 构建成功后，G24 旧 25ms inactive 采样未观察到短暂 false 而 exit 1；改为验证随机 runtime identity 更换后第二次 exit 0。四类 fault 均记录 `previousRuntimeId != recoveredRuntimeId`，且故障后 edit/save、disable/uninstall、kill/restart recovery 通过。2026-08-11 WSL/skip-build 与 `0xc0000142` 记录仍是历史，不覆盖当前 manifest。
- 本会话已修复的回归：① `check-doc-links` 3 个失效链接；② `services/language_pack_rust_integration_test.go` 的 rust-analyzer 冷启动 flake（hover/completion 重试期限 20s→45s；原 20s 在负载下偶发 `raw=null` 20 秒不返回）；③ `docs/THIRD_PARTY_LICENSES.md` 重新生成（G17 门禁）。
- i18n/profile 实测数量：i18n 全量 **53/53**（`i18n.test.ts`+`i18n.g25.test.ts`+`localeMetadata.test.ts`）；`profile_service_test.go` 40 个顶层测试函数（其中 G25 新增导入导出 8 个）。

## 2. 推进顺序（一次只推进一个 Goal）

> prompt-12 §11 已新增 P12-G28~G33；2026-08-14 用户指定当前下一 Goal 为 **G33**。本节原有 G24→G25 顺序仅适用于 prompt-11 的历史续作范围，不得覆盖最新显式执行顺序。

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
- 真实 packaged：2026-08-11 历史收口 24/24 通过；2026-08-14 复验也为 24/24（均为历史基线）。探针以随机 Worker runtime identity 更换证明 crash/hang/rate/size 确实重启，不依赖瞬时 inactive 采样；故障后 edit/save、disable/uninstall 与 kill/restart recovery 均通过。当前代码态 packaged 证据以 prompt-12 §13.30 为准。
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

**T-G24-6（四个故障专项检查，2026-08-14 复核完成）**：
1. 真正未捕获异常是否产生 `runtime-error` 或外部 `onerror`；
2. `globalThis.close()`/`scope.close()` 后 heartbeat 是否在 8 秒内终止并进入恢复；
3. hang 的 busy loop 是否由 heartbeat watchdog 终止；
4. rate/size quota 是否 fail-closed，且恢复后命令与保存仍可用。
- 验收：四项各有真实 packaged 断言与原始日志；失败按 §0 规则 10 与 T-G24-9 记录。

**T-G24-7（禁止假成功，2026-08-14 复核完成）**：probe 未主动 `deactivateExtension`、未手工重建状态或直接设置 `isExtensionActivated=false`；每类 fault 读取故障前 runtime ID，并只在恢复版本匹配且 runtime ID 改变后接受恢复。旧 inactive 采样竞态的首次失败已保留，不冒充产品恢复失败。

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

> **P12-G33 AgentService.Close audit/renderer-surface overlay（2026-08-18，详见 prompt-12 §13.31）**：Close 已从 Wails runtime surface 移除，forbidden export、ignored runtime Close 与锁定 Wails 版本 bindings 重生成均通过；root-backed usage/receipt/lock 的 post-write identity/sync/poison 与失败 Release 幂等边界取得定向 `-race` 和 headless CLI 证据。权威 backend 9/9（Go test 219.4s）、frontend 173/173 files/2765 tests、bindings/docs 全绿；fresh Windows packaged 24/24、artifact `895a351f...`、source `8b0f4d12...`（1020 files），`artifactReused=false` 且 fingerprint 匹配。npm `nanoid` high 仍红，全局 diff-check 仍受既有 `build-msi.ps1` EOF 空行阻塞；H1 仍存在、H2 已关闭、G33/AC 仍 `0/6`/[ ]。

> **P12-G33 trusted headless CLI / opaque owner overlay（2026-08-18，详见 prompt-12 §13.32）**：`internal/agentcli` 的 external-receipt inventory 与精确 `manual-unknown` 处置复用 root-backed lifecycle，Close 等待 operation lease，真实子进程跨重启 replay、redaction 与 durable terminal receipt 通过 `-race -count=3`；Plan/Goal adapters 统一 logical→opaque runtime owner。固定 Wails 后 backend 9/9（Go test 357.2s）、frontend 173/173 files/2765 tests、bindings/docs 全绿；fresh Windows packaged 24/24，artifact `86f89d0b...`、source `91cabc5e...`（1023 files），仅本机 P。npm `nanoid` high 仍红，H1 原始范围仍存在、H2 已关闭、G33/AC 仍 `0/6`/[ ]；真实跨平台/CI operator、resume/compensation、cross-caller/domain rollback、跨进程 CAS、provider 与 macOS/Linux packaged 仍 U。

> **P12-G33 workspace reset owner-map rollback + current shared-tree gates（2026-08-18，详见 prompt-12 §13.33）**：修复 `clearAgentSkillSessions` 的 reset 顺序：lifecycle reset 成功后才清理 `sessionOwners/sessionSkills`；`PersistenceNotPublished` 保留旧 runtime/owner，published-unknown/poison 在 authority 撤销后清理 maps。首轮红测 `TestAgentWorkspaceResetPrePublicationPreservesRendererOwner` exit 1，修复后 workspace reset `-race -count=5`、owner/session 与 agentcore 矩阵、vet、scoped diff-check exit 0。当前共享树 full Go test 300.2s、gofmt/vet/build/contract smoke/Wails pin/docs 全部通过；自动 Wails 安装因 `proxy.golang.org` 网络失败，显式本机 `v3.0.0-alpha2.111` CLI 复核 bindings exit 0；`task frontend:check` 173/173 files、2765/2765 tests、ESLint 0 errors/1 warning、vue-tsc/bindings/docs exit 0。fresh packaged 24/24，artifact `83ee98f7...`、source `2ab1eb5c...`（1024 files）、`artifactReused=false`、completedAt `2026-08-18T14:16:37.283Z`，Windows 本机 P；WebView `EBUSY` 仅首次 cleanup，脚本重试后 exit 0。显式本机 Wails CLI 下随后完整 `node scripts/backend-gate.mjs` 9/9、exit 0（全量 Go test 353.1s）。G33/AC 仍 `0/6`/[ ]，npm `nanoid` high 仍红，H1 原始范围仍存在、H2 已关闭；跨平台/CI/CLI、真正 resume/compensation、cross-caller/domain rollback、跨进程 CAS 与 macOS/Linux packaged 仍 U。未 commit/push/tag/release。

> **P12-G33 MCP workspace admission / current static-tree overlay（2026-08-19，详见 prompt-12 §13.36）**：H1 原始全平台范围仍存在（Linux T、Windows junction I、macOS/Reveal/CAS/公共 Workflow pathname loader U）；H2 已关闭（真实 Windows `cmd.exe` 双路径 I）；G33/AC 仍 `0/6`/[ ]。nil-workspace MCP stdio rejection、canonical workspace admission、legacy deny-only renderer surface、partial teardown reconciliation 与 Wails ignored/forbidden contract 已取得 T；MCP store 6/6、Wails contract 16/16、固定 Wails backend 9/9（Go test 252.6s）、`task frontend:check` 174/174 files/2792 tests、docs/bindings 全绿。根 `npm run frontend:check` ENOENT（无根 package.json），Taskfile 为权威入口；npm gate 仍因 `nanoid <3.3.18` / `GHSA-2v37-7h3g-55p8` high exit 1，lock 未改。fresh Windows packaged 24/24，artifact `684190ff...`、source `a97bb30e...`（1034 files）、`artifactReused=false`，completed `2026-08-19T11:14:57.408Z`，仅 P；完整 MCP 协议/AI provider-stream、跨平台/CI/CLI、recovery/domain rollback、macOS/Linux packaged 与新增 AI 静态审查缺口仍 U。未修改 workflow/Docker/package-lock/治理元数据，无 commit/push/tag/release。

## 9. 会话交付模板（沿用 prompt-9 §9 / prompt-10 §6）

### 历史会话交付：P11-T-G24（2026-08-11）

- 复核结论：缺口已不存在；G24 状态为完成，AC 4/4。
- AC：AC1–AC4 全部 `[x]`，证据为 `T/I/P`；manifest `status=passed`，24/24 fixtures passed。
- 证据：artifact SHA-256 `7e8abff533098129f6cf858dd9278053c71786edbf5858a6003452589f07b181`；source fingerprint `690aa31cad880bf803037ab734207a9e1f7281d9e05140f65daaef15bd7b6180`；`recordedAt=2026-08-11T03:23:53.760Z`；corpus 11/11，10 包全 blocked。
- 首次失败：本轮首次结果为 `disabled=true` 且仍 `active=true`；修复后端 lifecycle stop handshake。低内存 full run 遇 Git `0xc0000142`，随后 true `KOYORI_IDE_E2E_SKIP_BUILD=1` 复用既有 artifact 通过。
- 最终门禁：Windows backend-gate 9/9、exit 0（gofmt 0.6s、vet 15.3s、build 14.5s、Go 全量测试 333.9s、contract 3.1s、bindings 12.6s、pin/docs 各检查 0.x 秒）。首次 gate 的 TypeScript LSP TempDir 占用由 `.cmd -> node` 后代未回收导致；`lspProcess.stop` 使用 `taskkill /PID /T /F` + `Wait` 修复，定向 TypeScript LSP 与 LSP 测试通过。
- 环境与 production：WSL `.wslconfig` 8GB -> 6GB、`autoMemoryReclaim=gradual` 已生效，`free` 为 5.8GiB；true skip-build packaged 24/24；普通 production frontend 已重建，五个 E2E marker 扫描为 0。
- 未验证/限制：manifest `gitMetadataAvailable=false`；本轮不声称 git、CI、release，也不推进或修改 G25 实现。
- SSOT 回写：已同步 prompt-9 G24 AC/§8 进度板、prompt-10 G24 章节与 prompt-11 全局计数/D1/D3/D4；没有 commit/push/tag/release。

### 会话交付：P12-Preflight/G24-Revalidation（2026-08-14）

- 复核结论：G24 缺口仍为已不存在、AC 4/4；本次只强化 recovery 判据并重跑当前代码态，不重复改变 Goal 状态。H1 缺口已变化但仍存在（Linux T；Windows/macOS/RevealInOS/CAS U）；H2 缺口已不存在（真实 cmd.exe I）；H3 误导性签名文案缺口已不存在（T），但真正发布者签名仍不在本修复范围。
- 改动文件：`scripts/packaged-e2e.mjs`/driver 及测试补 fail-closed lifecycle/逐 fixture checkpoint；`extensionHostG24Recovery.ts`/测试与 G24 probe/VSIX fixture 改用随机 runtime identity；`build/Taskfile.yml` 消除 frontend install 与 go mod tidy 竞态。未覆盖用户既有 MSI/terminal/layout 改动。
- AC：G24 AC1~AC4 保持 `[x]`（T/I/P）；P12-G33 及 G28~G32 均未在本记录开始，不勾选其 AC。
- 验证：backend-gate 9/9 exit 0；frontend:check 172 files / 2739 tests + ESLint/vue-tsc/bindings/docs exit 0；独立 bindings/docs links/docs numbers exit 0；packaged 第二次 exit 0，24/24。
- 产物：artifact `9e382df322b7ea42f881a6cc656c029c5e32432cac84286b162f1a5c39caaa1a`；source fingerprint `09635b427d2fc8852049c5b44550ed8ed2a49afc7ef4aabd097401865fcf8485`；recordedAt `2026-08-14T10:26:36.958Z`；completedAt `2026-08-14T10:29:00.371Z`。`screenshot=null`，P 级原始日志为 `launch-1.log`/`launch-2.log`。
- 首次失败：第一次真实 packaged 运行因 25ms inactive 采样竞态在 G24 失败；随机 runtime identity 判据修复后复跑通过。第一次 backend gate 的全量 Go 腿偶发失败，独立复跑及第二次完整 gate 通过。H3 独立复跑第一次被 PowerShell `npm.ps1` 策略拦截，改用 `npm.cmd` 后 3 files / 41 tests 通过。
- 安全与数据：G24 recovery 必须证明 Worker identity 更换；manifest 在构建前写 `running/not-run`，异常写 `failed`，每个 fixture 增量落盘，证据写失败不覆盖产品原始错误。H1 残余 U 未被门禁通过掩盖。
- 未验证：macOS/Linux packaged、真实 CI/tag/release/签名、公证仍为 U；当前工作树不干净，manifest 中 HEAD 不能单独绑定全部被测源码；未执行 `check-encoding`、license full-check 或 release metadata check，不把它们写为通过。
- SSOT 回写：同步 prompt-12 §7.3/§9/§13、prompt-9 G24 正文/AC/§8 与本文件；没有 commit/push/tag/release。

### 会话交付：P12-G33-AC4 receipt 子切片（2026-08-15，Goal 未完成）

- 复核结论：G33 缺口已变化但仍存在；状态从未开始转为进行中，AC 0/6，G28~G32 与 P12-BUG-01 未开始。
- 改动文件：`internal/agentcore/runtime.go`/测试/外部 headless 测试新增两阶段 usage receipt 与 production-required meter contract；`services/agent_lifecycle.go`、`ai_permission_service.go`、`task_service.go` 让账本先于生命周期完成态，追加 ledger 以同 UnitID 逻辑 upsert；`agent_execution_core.go`/锁定生成 bindings 投影 pending 状态。AC2 的 `WorkspaceTransactionHandler` 子切片同时保留，外部副作用仍未事务化。
- AC：AC1 `[ ] T/I/U`（单 catalog 基础，workflow adapters/runner 未全）；AC2 `[ ] T/U`（workspace write 真实 transaction boundary，external mutation 未全）；AC3 `[ ] T/U`（共享 lifecycle 基础，ownership/I 待审）；AC4 `[ ] T/U`（tool durable receipt 与 AI/orchestration 单入口已有，完整生产矩阵未全）；AC5 `[ ] T/U`（god-file 拆分/headless external test，真实 CLI/CI 未有）；AC6 `[ ] T/P/U`（新改动后最终全量/packaged 未跑）。
- 验证：`go test ./internal/agentcore -count=1` exit 0；`go test -race ./internal/agentcore -count=1` exit 0；services AgentExecutionCore/Lifecycle/AI/Task 定向组 exit 0；锁定 Wails `v3.0.0-alpha2.111` 生成及 `check-bindings` exit 0（47 modules/55 files，ByName=0）。
- 首次失败：只更新 binding manifest 后，`check-bindings` 正确以 `services/models.ts` drift 退出 1；运行仓库锁定生成器后复验 exit 0。`git diff --check` 被用户既有 `build-msi.ps1` EOF 空行阻塞，未触碰该文件，不伪报通过。
- 安全与数据：pending receipt 必须 fsync 成功后 handler 才可执行；begin 失败 builtin write 零落盘；terminal 失败保留 pending 记录并将 result/audit 标失败；renderer `RecordUsage`/`ResetUsage` 保持 deny-only。诊断生成的 `.probe-bindings-*` 已仅在核验 workspace 路径后删除，正式 bindings 保留。
- 未验证/下一步：继续唯一 Goal G33，先闭环 AC1 workflow runner/adapter 与 AC2 external side-effect receipt/compensation，再完成 AC3 ownership；receipt 后 backend/frontend/packaged 门禁必须重跑。未 commit/push/tag/release。
- SSOT 回写：已同步 prompt-12 §11/§13、prompt-9 §8 与本文件。

### 会话交付：P12-G33-AC2 external mutation 子切片（2026-08-15，Goal 未完成）

- 复核结论：G33 缺口已变化但仍存在；当前唯一 Goal 仍为 G33，AC 0/6，G28~G32 与 P12-BUG-01 未开始。
- 改动文件：`internal/agentcore/runtime.go`/测试新增 `ExternalMutationTransactionHandler` 与 receipt/compensation 状态机；`services/agent_execution_core.go`、`agent_execution_workflow_skill.go`、`agent_execution_mcp.go` 接入 run/workflow command/MCP/skill activation；`ai_permission_service.go` durable JSONL 与 production audit/renderer projection 只保留 bounded receipt 字段；`skills_service.go` 支持恢复本次 trusted approval/session binding。
- AC：AC1 `[ ] T/I/U`；AC2 `[ ] T/U`（workspace/external transaction boundary 已有，跨重启 recovery/manual-disposition 未有）；AC3 `[ ] T/U`；AC4 `[ ] T/U`（pending/terminal ledger 基础已有，完整矩阵未全）；AC5 `[ ] T/U`；AC6 `[ ] T/P/U`（本子切片后 bindings/full/packaged 尚未重跑）。
- 验证：`go test ./internal/agentcore -count=1` exit 0；`go test -race ./internal/agentcore -count=1` exit 0；services external adapter/production ledger 定向组及同组 `-race` exit 0；ledger reload、audit receipt 与 metadata redaction 定向测试 exit 0。
- 首次失败：强 transaction contract 使未实现新接口的 builtin `run` 初始化失败，四个生产 adapter 接线后通过；privacy schema 收窄后测试仍引用已删除 metadata 字段导致编译失败，测试同步到 bounded schema 后通过；handler + terminal meter 双失败曾重复补偿，修为 exactly-once cleanup。
- 安全与数据：`BeginExternalMutation` 必须无副作用；receipt 无效或 pending fsync 失败时 handler 零调用；cleanup 使用 `context.WithTimeout(context.WithoutCancel(ctx), 30s)`，保留可信 context value 并脱离 caller cancel；receipt metadata 仅存 adapter 内存。skill 仅回滚本次状态，不覆盖先前 approval/binding；不可逆 adapter 返回 `ErrExternalMutationIrreversible`。
- 未验证/限制：terminal `CompleteUsage` 失败后先补偿并以同一 UnitID 最多幂等重试一次；若重试仍失败，durable JSONL 仍保留 pending，补偿结果只在返回 record/best-effort audit。receipt identity/status 不包含私有 rollback state，不能冒充 crash recovery journal；跨进程 dispatcher、重试失败后的后续 durable recovery 与真实恢复 I 仍为 `U`。未 commit/push/tag/release。
- SSOT 回写：已同步 prompt-12 §11/§13、prompt-9 §8 与本文件；所有 G33 AC 保持未勾选。

### 会话交付：P12-G33-AC3 workspace ownership / observation 子切片（2026-08-15，Goal 未完成）

- 复核结论：G33 缺口已变化但仍存在；AC 0/6，当前唯一 Goal 仍为 G33，G28~G32 与 P12-BUG-01 未开始。
- 改动文件：`internal/agentcore/runtime.go` 清空 workspace reset 后的 outstanding capabilities；`internal/agentcore/session.go` 增加 lifecycle `CloseAll` 与 UnitID 原子 observation；`services/agent_lifecycle.go` 增加 workspace reset 关闭 rows/清 owner map，并修复 opaque runtime ID 到 logical session 的 observation；`services/agent_execution_core.go` 拒绝 renderer 创建 plan/goal session；`frontend/src/stores/agent.ts` 绑定 workspace generation 旋转 chat authority；`frontend/src/api/automation.ts` 保留 bounded receipt projection。
- AC：AC1 `[ ] T/I/U`；AC2 `[ ] T/U`；AC3 `[ ] T/U`（workspace reset/domain-owner拒绝/opaque observation T 基础已变化，durable owner/restart/domain convergence/I 未有）；AC4 `[ ] T/U`；AC5 `[ ] T/U`；AC6 `[ ] T/P/U`（本子切片后 full gates/packaged 未重跑）。
- 验证：`go test ./internal/agentcore -count=1` exit 0；services lifecycle/ownership/receipt 定向组及 `-race` exit 0；`npm.cmd exec vitest run src/stores/agent.test.ts` 107/107 exit 0。
- 首次失败：workspace reset 首次只撤销 runtime，测试发现 lifecycle row/owner map 残留；opaque observation 首次写错 runtime ID；重复 UnitID 首次重复 stream/checkpoint；前端默认 mock 暴露空 createSession 使 4 个既有测试回归。均已收紧/修复并保留失败记录。
- 安全与数据：workspace reset 先撤销 runtime authority/capability，再终结内存 lifecycle rows并清 owner map；plan/goal authority 只能由 domain service 创建；observation 原子写入并按 UnitID 去重；frontend 不发布跨 generation 的迟到 session ID。SessionStore/owner metadata 仍未持久化，不能宣称 restart recovery 或跨窗口 owner 认证。
- 未验证/下一步：继续 G33，优先补 durable lifecycle owner/recovery contract，再推进 workflow adapters/runner；AC 全保持未勾选。未 commit/push/tag/release。
- SSOT 回写：已同步 prompt-12 §11/§13、prompt-9 §8 与本文件。

### 会话交付：P12-G33-AC3 durable owner / restart fail-closed 子切片（2026-08-15，Goal 未完成）

- 复核结论：G33 缺口已变化但仍存在；AC 0/6，当前唯一 Goal 仍为 G33，G28~G32 与 P12-BUG-01 未开始。
- 改动文件：`internal/agentcore/session.go`/`session_persistence.go` 增加原子 lifecycle snapshot、durable owner/process incarnation 与 restart `recovery-required`；`services/agent_lifecycle.go`、`agent_execution_core.go`、`agent_service.go`、`main.go` 接线 durable-before-authority 与 workspace rollback；`services/task_service.go` 复用已创建 workflow row，并固定 transactional usage identity；相关测试覆盖重启、持久化失败回滚和 workflow ownership。
- AC：AC1 `[ ] T/I/U`；AC2 `[ ] T/U`；AC3 `[ ] T/U`（durable owner/restart fail-closed T 基础已变化，recovery dispatcher/cross-caller proof/domain rollback/I 未有）；AC4 `[ ] T/U`；AC5 `[ ] T/U`；AC6 `[ ] T/P/U`（本子切片后 bindings/full gates/packaged 未复跑）。
- 验证：`go test ./internal/agentcore -count=1` exit 0；`go test -race ./internal/agentcore -count=1` exit 0；五个 workflow/lazy persistence 定向测试 exit 0；10 个 lifecycle/workflow 定向测试加 `-race` exit 0。
- 首次失败：workflow session 已由 `CreateAgentSession` 持久创建，TaskService 第二次 `Begin` 令四测试报 `ErrSessionExists`，改用 `BeginExisting`；随后 pending/terminal `Operation` 不同触发 receipt identity fail-closed，保持同一 `workflow.attempt` 身份并用 `Success`/`Error` 表示结果后通过，未放宽账本断言。
- 安全与数据：snapshot 失败时内存 mutation 回滚，runtime authority 不先发布；重启只保留诊断 row 并清旧 runtime ID，不自动恢复 authority。snapshot 仍含完整 stream，写入总量未 bounded；workflow attempt 仍内存态；无跨 window/caller owner proof，不能宣称 restart recovery 或 AC3 完成。
- 未验证/下一步：继续 G33，补 trusted recovery/manual-disposition、domain rollback 收敛与真实 I，再推进 workflow runner/adapters；最新代码需重跑 bindings/full gates/packaged。未 commit/push/tag/release。
- SSOT 回写：已同步 prompt-12 §11/§13、prompt-9 §8 与本文件；所有 G33 AC 保持未勾选。

### 会话交付：P12-G33-AC3 lifecycle snapshot schema / size 子切片（2026-08-15，Goal 未完成）

- 复核结论：G33 缺口已变化但仍存在；AC 0/6，当前唯一 Goal 仍为 G33，G28~G32 与 P12-BUG-01 未开始。
- 改动文件：`internal/agentcore/session_persistence.go` 增加单句柄限长读取、严格 JSON schema/trailing 检查与 16 MiB 写入上限；`internal/agentcore/session.go` 拒绝缺失 status、无效 owner/recovery；`session_test.go` 覆盖不可信 shape 与超限保存不替换旧文件。
- AC：AC3 `[ ] T/U`（snapshot schema/size T 边界已变化，recovery/cross-owner/domain rollback/privacy/I 未有）；AC1、AC2、AC4~AC6 状态不变，G33 仍 0/6。
- 验证：snapshot 定向测试 exit 0；`go test ./internal/agentcore -count=1` exit 0；`go test -race ./internal/agentcore -count=1` exit 0；8 个 services durable owner/restart/workflow 定向测试 exit 0。
- 首次失败：测试首次 exit 1，未知字段、缺失 status、无效 owner 被接受且超限 snapshot 覆盖旧文件；严格 decoder/结构校验/计数 writer 修复后通过，尾随 JSON 原本已 fail-closed。
- 安全与数据：读入最多 16 MiB+1 用于判断超限；写入超过上限立即失败且不 replace，旧 snapshot 字节保持不变。该上限不等于 stream 隐私/retention，完整内容仍会持久化。
- 未验证/下一步：继续 G33 的 recovery/manual-disposition、cross-caller owner proof 与 domain rollback；最新代码仍需 bindings/full gates/packaged。未 commit/push/tag/release。
- SSOT 回写：已同步 prompt-12 §11/§13、prompt-9 §8 与本文件；所有 G33 AC 保持未勾选。

### 会话交付：P12-G33-AC1 typed workflow file-read 子切片（2026-08-15，Goal 未完成）

- 复核结论：G33 缺口已变化但仍存在；当前唯一 Goal 仍为 G33，AC 0/6，G28~G32 与 P12-BUG-01 未开始。
- 改动文件：`services/workflow_service.go`/测试增加 `tool/input` typed contract、root-relative path 与 whole-workflow fail-closed validation；`agent_execution_workflow_skill.go`/测试发布 `adapter=file.read` ToolDef 并复用 builtin read/FileService；`task_service.go`/agentcore 测试按 session-owned workflow/step 与同一 catalog revision 签发/执行；`frontend/src/types/index.ts`、`api/automation.ts`、`stores/workflows.ts` 及测试接入 DTO 和 catalog-only runner；bindings 由锁定生成器重建并同步 manifest。
- AC：AC1 `[ ] T/I/U`（file.read 单一 typed slice 已有 T，AI/Git/file mutation/MCP/Skill adapters 与真实 MCP observation I 未有）；AC2~AC5 状态不变；AC6 `[ ] T/P/U`（bindings/定向前后端已复跑，full gates/本代码态 packaged 未跑）。G33 保持 0/6。
- 验证：services workflow/file adapter 定向 exit 0；四个安全用例 `-race` exit 0；workflow/DTO Vitest 2 files / 160 tests、H3 Vitest 3 files / 41 tests、定向 ESLint、`vue-tsc --noEmit` 均 exit 0；bindings ownership、16/16 contract、最终 `check-bindings` 均 exit 0，锁定 Wails `v3.0.0-alpha2.111`；切片 `git diff --check` exit 0。
- 首次失败：首轮 Go 构建因缺 `workflowStepCapabilityArguments` exit 1；实现严格 adapter mapping 后通过。首次 bindings 检查以 `models.ts`/`taskservice.ts` generated drift exit 1；运行 manifest accept + 锁定完整生成后通过，未手调生成 ID。
- 安全与数据：file step 拒绝 command/args/cwd、额外 input/root、绝对/drive/UNC/parent traversal；renderer path redirect 与跨 step replay 均在 FileService 前拒绝并烧毁 token。成功读取走 `FileService` 的 H1 `os.Root` capability；catalog revision、arguments hash、epoch、workspace generation 与 session 仍由统一 runtime 绑定。usage/audit 不记录文件内容或原始 arguments。
- 未验证/下一步：继续 G33 AC1 的下一真实 adapter；跨进程 recovery/manual-disposition、cross-caller ownership、完整 usage matrix、真实 CLI/CI、全量门禁与新 packaged 仍为 `U`。当前 manifest 24/24 passed 早于本切片，不能冒充当前 AC6。未 commit/push/tag/release。
- SSOT 回写：已同步 prompt-12 §11/§13、prompt-9 §8 与本文件；所有 G33 AC 保持未勾选。

### 会话交付：P12-G33-AC1 typed workflow MCP / Git status 子切片（2026-08-16，Goal 未完成）

- 复核结论：G33 缺口已变化但仍存在；当前唯一 Goal 仍为 G33，AC 0/6，G28~G32 与 P12-BUG-01 未开始。H1 原始全平台范围仍未闭环；H2/H3/H4/M3 在各自修复范围内已不存在。
- 改动文件：`services/workflow_service.go`/validation tests 增加严格 MCP 与零参数 git/status contract；`services/agent_execution_mcp.go` 在 receipt 前二次核对 delegated tool/schema 并限制 observation；`services/agent_execution_workflow_skill.go`/tests 发布复用既有 MCP/GitService 的 workflow ToolDef/handler；`services/task_service.go`/agentcore tests 绑定 session/workflow/step/catalog 参数并桥接 typed observation；`services/agent_execution_core.go`/`main.go` 注入现有 GitService 与共享 observation budget；`frontend/src/stores/workflows.ts`/tests 增加 Git/MCP catalog-only runner。没有改 Wails DTO 或手调 binding ID。
- AC：AC1 `[ ] T/I/U`（file.read、只读 git.status、mcp.call 三个 typed slice 已有 T；AI、Git/file mutation、更多 Git/file、Skill 与真实 MCP process I 未有）；AC2~AC5 状态不变；AC6 `[ ] T/P/U`（bindings/docs/定向前后端已跑，full gates/本代码态 packaged 未跑）。G33 保持 0/6。
- 验证：MCP/TaskService/observation、Git repository/TaskService 与扩展 workflow services 定向组 exit 0；安全组 `-race` exit 0。`workflows.test.ts` 147/147，workflow+DTO Vitest 2 files/162 tests，定向 ESLint、`vue-tsc --noEmit` 均 exit 0。当前代码态 `task frontend:check` 复跑 173 files/2758 tests、ESLint（0 error/1 既有 warning）、vue-tsc、bindings/docs 全部 exit 0；首轮 extensionHost 同毫秒 fixture 曾 2757/2758，单文件 107/107 与全量复跑均绿，未改该测试。`node scripts/check-bindings.mjs` exit 0（pinned `v3.0.0-alpha2.111`、ByName=0），`check-doc-links` exit 0；本切片 `git diff --check` exit 0。
- 首次失败：TaskService fixture 缺 `encoding/json` 先编译失败；补齐后审批后 tool disappearance 测试真实出现第二次 `tools/call`，补 execution-time revalidation。首轮 guard 将 server raw schema 与闭合 catalog schema直接比较而误拒合法调用，改为两侧统一 `normalizeMCPAgentSchema`。Git 创建后空 input 被 `omitempty` 省略导致 ToolDef 缺失，明确 nil/空对象 canonical 为零参数并仍拒绝额外字段。全树 diff check 仍被既有 `build-msi.ps1` EOF 空行阻塞，未修改该用户文件。
- 安全与数据：renderer 不能提供 MCP command/cwd/root、Git repo/path 或预解析 workspace 作为授权；workflow input 重载、hash、catalog revision、epoch、workspace generation、session 与 one-shot token 均由 backend 验证。MCP tool/schema 在 external receipt 分配前复核，变化时零 `tools/call`；observation 最多 8,000 bytes；audit/usage 不落原始 input/content/secret/完整 repo 绝对路径。
- 未验证/下一步：继续唯一 Goal G33 AC1 的 AI/Skill/剩余 file/Git adapters，或回到 AC2/AC3 的 durable recovery 缺口；真实 MCP 子进程与跨平台仍为 U。frontend full gate 已绿；backend gate 当前 exit 1，`services` 全量自身通过但并行任务的 `build/docker/server-gateway/main.go` 未格式化且 unused `net` 令 gofmt/vet/build/全仓 test 红，已回传其 owner，本任务按约束不修改。修复后需复跑 backend，再跑当前代码态 packaged；旧 24/24 manifest 不能用于 AC6。未 commit/push/tag/release。
- SSOT 回写：已同步 prompt-12 §11/§13、prompt-9 §8 与本文件；所有 G33 AC 保持未勾选。

```markdown
### 会话交付：P12-G33-AC1/AC3 workspace Skill reload fail-closed 子切片（2026-08-16，Goal 未完成）

- 复核结论：G33 缺口已变化但仍存在；AC 0/6，当前唯一 Goal 仍为 G33。H1 原始全平台范围仍未闭环；H2/H3/H4/M3 在各自修复范围内已不存在。
- 改动文件：`services/skills_service.go` 在 workspace root 变化时先清空旧 Skill/项目批准并 staged 加载，读取失败清空旧 SourceSkill，source identity 变化时拒绝旧加载结果；`services/agent_service.go` 记录 reset/Load 错误并在 rollback 清理时传播错误；`services/agent_execution_workflow_skill_test.go` 新增 workspace A→B、坏 `skills` 文件的旧 ToolDef/approval/session binding/capability 失效矩阵。
- AC：AC1 `[ ] T/I/U`（Skill source/catalog 撤销边界 T 已加强，AI/Git/file mutation/其余 adapters 与真实 MCP process I 未有）；AC2 `[ ] T/U`；AC3 `[ ] T/U`（workspace Skill ownership T 已加强，recovery/manual-disposition/cross-caller/domain rollback/I 未有）；AC4~AC6 状态不变，G33 仍 0/6。
- 验证：Skill/Agent workflow 定向 Go 组 exit 0；`go test -race ./services -run '^TestAgentExecutionCoreWorkspaceSkillLoadFailureFailsClosed$' -count=1` exit 0；scoped `git diff --check` exit 0。首次测试因 fixture 未注入 `AgentService.skillsService` 而未进入切换路径，补齐 trusted wiring 后通过；未放宽断言。
- 安全与数据：root 切换先撤销旧 project Skill、approval、session binding 与 capability；坏目录只得到空安全 catalog，不能把旧 Skill 重新发布到新 workspace。该证据为 T，非真实进程重启/多窗口 I。
- 未验证/下一步：backend-gate 仍受并行 `build/docker/server-gateway/main.go` blocker 影响，最新代码 packaged 未重跑；继续当前 G33 AC1/AC2/AC3 缺口，所有 AC 保持未勾选。未 commit/push/tag/release。
- SSOT 回写：已同步 prompt-12 §11/§13、prompt-9 §8 与本文件；本切片未修改 GitHub workflow、Docker、package-lock 或治理元数据。

### Session delivery: P12-G33-AC1/AC2 typed workflow Skill activation slice (2026-08-16, Goal incomplete)

- Status: gap changed but still exists; G33 remains the only active Goal, AC 0/6. H1 remains open in its original all-platform scope; H2/H3/H4/M3 do not exist within their verified fix scopes.
- Files: `services/workflow_service.go` and validation tests enforce `skill/activate` with exactly one canonical `input.id` and reject command/args/cwd/extra input; `services/agent_execution_workflow_skill.go` publishes a workflow-owned ToolDef with skill scope/fingerprint and reuses the existing `skill.activate` handler, approval, and reversible receipt; `services/task_service.go` bridges the typed observation without command capture; `services/agent_execution_core.go` permits workflow Skill activation to establish session policy; `frontend/src/stores/workflows.ts` and tests use the catalog-only typed runner. No parallel Skill executor, Wails DTO edit, or hand-picked binding ID.
- AC: AC1 `[ ] T/I/U` (Skill typed adapter has T; AI, mutation adapters, remaining file/Git, and real MCP process I remain); AC2 `[ ] T/U` (approval, one-shot capability, scope/fingerprint and workflow/step receipt compensation T; restart dispatcher/ambiguous commit U); AC3-AC6 unchanged, G33 0/6.
- Verification: focused Go validation/AgentCore/TaskService tests exit 0; related services `-race` exit 0; `go test ./internal/agentcore -count=1` exit 0; full `go test ./services -count=1` exit 0 (263.128s); Skill workflow Vitest 152/152, scoped ESLint, vue-tsc, pinned bindings, docs, and scoped `git diff --check` all exit 0.
- First failure and repair: Go initially failed because tests referenced the not-yet-defined adapter constant (exit 1); frontend initially had 151/152 tests with no catalog call for typed Skill. Strict backend adapter/shared handler and frontend runner fixed both without weakening assertions.
- Security/data: renderer supplies only workflow/step identity; Skill id is reloaded by backend and scope/fingerprint are checked in ToolDef, Prepare, approval, TaskService mapping, and receipt compensation. Cross-step replay and catalog/Skill mutation fail closed before Skill activation; observation uses the existing bounded bridge and does not persist Skill content or command authority.
- Unverified/next: current `task frontend:check` exit 1 at the pre-existing extensionHost same-millisecond terminal test (172/173 files, 2762/2763 tests); isolated rerun also failed 106/107, so the full frontend gate remains red without modifying that test. Backend gate remains blocked by the parallel Docker gateway file; current-code packaged evidence is still U and the 2026-08-15 24/24 manifest is not current AC6 evidence. Continue only G33. No commit/push/tag/release.
- SSOT: prompt-12 §13.14 and prompt-9 §8 updated immediately; this slice did not modify GitHub workflows, Docker, package-lock, or governance metadata.

### Session delivery: P12-G33 full-gate refresh after Skill slice (2026-08-16, Goal incomplete)

- Status: G33 remains the only active Goal at AC 0/6. H1 remains open in its original all-platform scope; H2/H3/H4/M3 remain absent within their verified fix scopes.
- Verification: `go test ./internal/agentcore -count=1` and `go test -race ./internal/agentcore -count=1` exit 0; bindings/docs checks exit 0 with pinned Wails `v3.0.0-alpha2.111`. Backend gate gofmt/vet/build/contract/bindings/pin/docs steps exit 0, but `go test ./... -count=1` exits 1 on `TestAIPermission_RecordUsage_GetSummary` and `TestApplyEditTransaction_PathOutsideRoot_Rejected`; each isolated rerun exits 0. `task frontend:check` exits 1 at 172/173 files and 2762/2763 tests on the existing same-millisecond terminal-session test; isolated rerun exits 0. These are non-G33 full-suite flakes, not repaired here.
- Second full rerun: frontend fails the same terminal-session case again, while the complete `extensionHost.test.ts` file then passes 107/107 in isolation. Backend non-test steps remain green, while full Go now fails only `TestLSPServiceRealTypeScriptWorkspaceLocalServer` during Windows process-tree cleanup; its isolated rerun passes. Two full gate attempts are therefore red and packaged was not started.
- AC: AC1-AC6 remain unchecked. Current-code packaged evidence is still U; the 2026-08-15 24/24 manifest is historical and cannot satisfy AC6. No protected GitHub workflow, Docker, package-lock, or governance metadata file was modified; no commit/push/tag/release.

### 会话交付：P12-G33 Windows LSP/extensionHost 与全量门禁闭环快照（2026-08-16，Goal 未完成）

- 复核结论：H1 原始全平台缺口仍存在（Linux T、Windows junction I；macOS、Reveal/CAS 与 Workflow pathname loader U）；H2 已不存在（真实 Windows `cmd.exe` 双路径 I）；H3/H4/M3 在各自修复范围内已不存在。G33 已变化但仍未完成，AC 0/6；G28~G32 与 P12-BUG-01 未开始。
- 改动文件：`services/lsp_process_tree_windows.go`/`other.go`、`lsp_process_tree_windows_test.go`、`lsp_service_session.go`、`lsp_service_server.go`、`lsp_service_test.go` 实现 Windows Job Object 管理、终止后 active-process 核验、预存在后代收编与错误可观测；`frontend/src/lib/extensionHost/extensionHost.ts`/test 统一 lazy terminal runtime 并确定性 teardown；`scripts/wails-bindings.test.mjs` 同步 hardening 的 digest-pinned image/bindings stage 契约，但未改 `build/docker`。
- AC：AC1 `[ ] T/I/U`、AC2 `[ ] T/U`、AC3 `[ ] T/U`、AC4 `[ ] T/U`、AC5 `[ ] T/U`、AC6 `[ ] T/P/U`；当前全量/P 快照通过不补足 AC1~AC5，也不能替代跨平台、真实 CI/CLI 与后续代码变化后的复验，G33 保持 0/6。
- 验证：Windows Job/LSP 矩阵 `-count=10`、同组 `-race`、`go vet ./services` 均 exit 0；真实 Rust LSP/toolchain `-count=3` exit 0（102.096s）；extensionHost 目标用例连续 10 次与整文件 107/107、定向 ESLint/vue-tsc exit 0。`node scripts/backend-gate.mjs` 9/9 exit 0（`go test ./...` 350.6s）；`task frontend:check` 173/173 files、2763/2763 tests exit 0；独立 bindings/docs exit 0；Wails bindings 契约 16/16 exit 0。
- packaged：`node scripts/packaged-e2e.mjs` exit 0，24/24 passed；artifact SHA-256 `cc7831f84ccd5bd3be66d16f57e768ca3cecd13deda2d8879ccb83f46afe4a8b`，source fingerprint `58b5e59ccda1419ed0853eb89f80f900a6be189498d86edf608343e9b3784ffe`，completedAt `2026-08-15T20:58:13.390Z`，`screenshot=null`。仅为本机 Windows P，不升级为跨平台/CI R。
- 首次失败与修复：预存在 child 首轮逃逸 Job；Toolhelp + `IsProcessInJob` 收编后转绿。全量诊断定位 rust-analyzer/flycheck 占用 TempDir，清理修复后连续真实通过。extensionHost 同毫秒测试多次只产生 1/2 `startSession`；移除动态-import 局部 spy、统一 lazy runtime 与幂等 dispose 后稳定通过，未增大 timeout 或弱化断言。packaged cleanup 首次遇 WebView lockfile `EBUSY`，脚本按已有重试完成并无残留进程/根目录 exe/syso。
- 安全与数据：Windows 终止只作用于本服务 Job，测试明确无关进程存活；活跃后代无法查询/分配时拒绝启动完成，终止失败不会被根进程 `done` 吞掉。Workflow loader 仍通过 `os.ReadDir`/pathname `os.Open`，junction/symlink 交换可能读取 workspace 外定义，故 typed AI 暂不接入，H1/AC1 不关闭。
- 未验证/阻塞：Linux 交叉编译受 Wails alpha `pointer` build constraint 阻塞；macOS 未验证。G33 树实际 `node scripts/npm-audit-gate.mjs` exit 1，命中 `nanoid <3.3.18` / GHSA-2v37-7h3g-55p8 / 1 high，675-entry alpha.95 lockfile 的 SHA 前后前缀均 `F1C0AED7`。hardening 树 431-entry beta.8/424 registry packages 的强化 gate exit 0 属另一锁图，不得冒充 G33 通过；最终整合需带入其 package manifest/lock/gate 或重新生成审核。
- 下一步与 SSOT：只继续 G33，先为 Agent Workflow pathname loader 补 junction/symlink 红灯，再迁移到 backend-owned `FileService`/`os.Root` capability；代码变化后重跑全量与 packaged。已同步 prompt-12 §11/§13.16 与 prompt-9 §8；未修改 GitHub workflow、Docker、package-lock 或治理元数据，未 commit/push/tag/release。

### 会话交付：P12-G33 Agent Workflow secure loader 与当前代码态全量复核（2026-08-16，Goal 未完成）

- 复核结论：H1 原始全平台范围**仍存在**（Linux 核心路径 T、Windows junction 交换 I；macOS、RevealInOS、CAS 及公共 Workflow pathname loader 仍 U）；H2 **已不存在**（真实 Windows `cmd.exe` command/commandContext 注入矩阵为 I）。G33 **已变化但仍存在**，AC 仍为 0/6；G28~G32 与 P12-BUG-01 **仍未开始**。
- 改动文件：`services/workflow_secure_loader.go` 及 Agent workflow loader 测试使用 backend-owned `FileService`/`os.Root` capability，执行 `Lstat -> Open -> Stat -> SameFile -> bounded read -> post-Lstat`、SHA-256、workspace/file generation 校验并批量 fail-closed；catalog、approval、Prepare、receipt、Execute 均重新核验 workflow source。公共 Workflow CRUD pathname loader 尚未纳入 H1 闭环。未修改 `.github/workflows`、`build/docker`、`package-lock` 或 Issue/PR/Release 治理元数据。
- AC：AC1 `[ ] T/I/U`（typed `file.read`、只读 `git.status`、`mcp.call`、`skill.activate` 有 T；AI、mutation、剩余 adapters 与真实 MCP process I 仍缺）；AC2 `[ ] T/U`；AC3 `[ ] T/U`；AC4 `[ ] T/U`；AC5 `[ ] T/U`；AC6 `[ ] T/P/U`。G33 保持 0/6。
- 验证：`go test ./services -count=1` exit 0（319.693s）；workflow/loader `-race`、`go vet ./services`、gofmt exit 0；`node scripts/backend-gate.mjs` exit 0，9/9（全仓 Go test 375.3s）；`task frontend:check` exit 0，173 files / 2763 tests；`node scripts/check-bindings.mjs`、`node scripts/check-doc-links.mjs` exit 0；`node scripts/packaged-e2e.mjs` exit 0，24/24 passed，manifest `status=passed`/`phase=complete`，artifact SHA-256 `ec0847a981867f52969e8f2cb04719485a78bba2e67a4ba07cc925558f1e8353`，source fingerprint `58b5e59ccda1419ed0853eb89f80f900a6be189498d86edf608343e9b3784ffe`，`completedAt=2026-08-15T22:03:31.427Z`；为 Windows 本机 P，不升级为跨平台/CI R。
- 首次失败与修复：backend gate 首轮出现既有临时文件时序 flake，隔离连续 5 次复跑后完整 9/9 通过；packaged cleanup 首次遇 WebView `EBUSY`，既有重试完成且无残留进程/根目录产物。`node scripts/npm-audit-gate.mjs` 仍 exit 1，命中 G33 锁图 `nanoid <3.3.18` / `GHSA-2v37-7h3g-55p8` / 1 high；未修改 lockfile，hardening 树另一锁图绿灯不适用。
- 安全与数据：renderer 只提交 session-owned workflow identity，不能提供绝对 root 或预解析 pathname；读取结果受 bounded output/usage/audit 约束，不记录文件内容、secret 或完整本地绝对路径。公共 Workflow loader、RevealInOS 外部文件管理器与 CAS 竞争仍按 H1 保持 U。
- 未验证/下一步：真实 macOS/Linux packaged、CI/CLI consumer、真实 MCP 子进程 observation、AI/mutation adapters、recovery/manual-disposition、cross-caller ownership、domain rollback 与供应链高危修复仍未闭环；继续唯一 Goal G33，优先补 AC1 下一真实 adapter/安全负例，所有 AC 保持未勾选。无 commit/push/tag/release。
- SSOT 回写：已同步更新 prompt-12 §11/§13、prompt-9 §8 与本文件。

### 会话交付：P12-G33 AI provider resolver boundary 与遗留安全证据复核（2026-08-16，Goal 未完成）

- 复核结论：H1 原始全平台范围**仍存在**（Linux 核心读写 T、Windows junction 交换 I；macOS、RevealInOS、CAS 与公共 Workflow pathname loader 仍 U）；H2 **已不存在**（真实 Windows `cmd.exe` `Command`/`CommandContext` 注入矩阵为 I）；H3 前端完整性门禁已复核通过。G33 **已变化但仍存在**，AC 仍为 0/6；G28~G32 与 P12-BUG-01 未开始。
- 改动文件：`services/ai_service.go` 将导出的 `ResolveModelFor` 收窄为脱敏 model/ConfigID/预算 metadata，清除 API key、endpoint、protocol、prompt 和 tool definitions；新增 backend-only `resolveModelFor`，`services/ai_agent.go` 的 typed AI execution 只走内部 resolver 后再由 `SettingsService` 重载 assigned provider。`services/ai_agent_test.go` 增加 assigned/global provider details 不越过导出 resolver 的回归断言。未修改 `.github/workflows`、`build/docker`、`package-lock` 或 Issue/PR/Release 治理元数据。
- AC：AC1 `[ ] T/I/U`（AI typed adapter、source/provider fingerprint、one-shot capability、receipt/usage 与 renderer fail-closed 负例，以及 resolver 脱敏回归为 T；真实 provider/MCP 子进程 observation I、Git/file mutation 与其余 adapters仍缺）；AC2 `[ ] T/U`（AI external receipt 不可逆，跨重启 pending dispatcher/人工处置仍 U）；AC3~AC5 不变；AC6 `[ ] T/P/U`，本轮代码变化后必须重新取得 full gate/packaged 证据。G33 保持 0/6。
- 验证：H1 Windows junction 矩阵 `go test ./services -run "TestFileService.*Junction|TestSecureWorkspace|TestFileService.*RootIdentity|TestFileService.*WorkspaceSwitch" -race -count=1 -v` exit 0（9/9）；H2 真实 cmd.exe `go test ./services -run "^(TestEscapeCmdArgRoundTrip|TestCommandCmdShimRoundTrip)$" -count=1 -v` exit 0（command 与 commandContext）；H3 integrity vitest 3 files/41 tests exit 0，`npm.cmd exec vue-tsc -- --noEmit` exit 0；AI/provider 定向 Go 测试 exit 0，gofmt/scoped diff check exit 0。
- 首次失败与修复：本轮 resolver 改造前没有新的测试失败；既有 AI adapter 首轮缺 `approveAI`/trusted wiring/adapter 常量的编译红灯已按 §13.18 记录并修复。本轮未放宽安全断言。
- 安全与数据：导出 Wails resolver 不再暴露密钥、provider endpoint/protocol、prompt 或 tool definitions；内部执行在 provider 触达前重载 Settings provider 并比较 fingerprint，workspace/source/receipt 变化仍 fail-closed。H1/H2/H3 证据不升级或掩盖 macOS/Reveal/CAS U。
- 验证后状态：`node scripts/backend-gate.mjs` 9/9 exit 0（全仓 Go test 226.3s）；`task frontend:check` exit 0（173 files/2764 tests，ESLint 0 errors/1 个既有 warning，vue-tsc、bindings/docs 全绿）；独立 `check-bindings`、`check-doc-links`、`check-doc-numbers` exit 0；`node scripts/packaged-e2e.mjs` exit 0，Windows 24/24 passed，artifact SHA-256 `8ce7efa35078eec8a371ea24e3a6583218fc043665882206f2657cb1b67e420b`，source fingerprint `b8ee03e3d15482578a4e9c69131a39385fa5c6d17f5ecb1c28beb6429c2dd1d2`，manifest completedAt `2026-08-15T23:29:22.127Z`。`node scripts/npm-audit-gate.mjs` 仍 exit 1，命中 G33 lock 图 `nanoid <3.3.18` / `GHSA-2v37-7h3g-55p8` / 1 high，未改 lockfile。真实 macOS/Linux packaged、CI/CLI、AI/MCP 子进程 I、AI stream lifecycle、mutation adapters、recovery/manual-disposition、cross-caller owner proof、domain rollback 仍 U；hardening 另一 lock 图不可互换。下一步只继续 G33，选择 AC1 下一 mutation adapter 或 AC2 durable recovery。无 commit/push/tag/release。
- SSOT 回写：已同步 prompt-12 §11/§13、prompt-9 §8 与本文件。

### 会话交付：P12-G33 AC1 typed workflow file.write mutation 子切片（2026-08-16，Goal 未完成）

- 复核结论：H1 **仍存在**（Linux T、Windows junction I；macOS、RevealInOS、CAS 与公共 Workflow pathname loader U）；H2/H3 **已不存在**（真实 cmd.exe I；integrityChecked 前端门禁已通过）。G33 **已变化但仍存在**，AC 0/6；G28~G32 与 P12-BUG-01 仍未开始。
- 本次状态：G33 继续进行中，仅推进 AC1 的 typed `file.write` mutation 子切片。
- 改动文件：workflow typed validation、backend-owned file adapter、统一 ToolDef/TaskService wiring、Agent write CAS transaction、空 baseline 安全创建、workspace pathname 脱敏；frontend workflow runner 仅保留 catalog-only typed shape。未修改 `.github/workflows`、`build/docker`、`package-lock` 或 Issue/PR/Release 元数据。
- AC：AC1 `[ ] T/I/U`（file.write T；AI、其余 mutation、真实 provider/MCP I 缺）；AC2 `[ ] T/U`（CAS/transaction T；跨重启 recovery/manual-disposition 与 domain rollback U）；AC3 `[ ] T/U`；AC4 `[ ] T/U`；AC5 `[ ] T/U`；AC6 `[ ] T/P/U`。不得宣称 G33 或任何 AC 完成。
- 验证：定向 file.write/CAS/audit/TaskService Go 测试及同组 `-race` exit 0；受影响 services 测试 exit 0；`node scripts/backend-gate.mjs` 9/9 exit 0（全量 Go test 355.4s）；`task frontend:check` 173 files、2765 tests exit 0，bindings/docs exit 0；`node scripts/packaged-e2e.mjs` exit 0，manifest 24/24 passed、`not-run=0`，recordedAt `2026-08-16T01:45:05.557Z`，artifact `facaf467b692ececbbde53d40482bfc3f7126d2281abe55b0670ff0d8141a7ed`，source `b9922c3238eae371166efc5fa03dfe5141ad977244cdf879212a33405465d0d3`，Windows 本机 P。`node scripts/npm-audit-gate.mjs` exit 1（nanoid/GHSA-2v37-7h3g-55p8，1 high），lockfile 未改。
- 首次失败与修复：publish-race 断言改为接受 `ErrFileConflict`；public error/audit redaction 改为拒绝完整 workspace pathname；write hook 改为断言 `WriteFileIfUnchanged`，未放宽安全断言。
- 安全与数据：renderer 不能注入 content/path/root/command/cwd；source、baseline、workspace generation、approval 与 capability args 在写入前重核；CAS 冲突 fail-closed，审计不落文件内容、secret 或完整绝对路径。
- 未验证/下一步：真实 AI provider/MCP 子进程、Git mutation、recovery/manual-disposition、cross-caller ownership、domain rollback、macOS/Linux packaged、真实 CI/CLI 和 npm high advisory 仍未闭环。下一步只继续 G33 的下一未完成 AC1/AC2 子切片；无 commit/push/tag/release。
- SSOT 回写：已同步 prompt-12 §11/§13 与 prompt-9 §8。

### 会话交付：P12-G33 dynamic catalog publication / packaged source-fingerprint integrity（2026-08-16，Goal 未完成）

- 复核结论：H1 **仍存在**（Linux T、Windows junction I；macOS、RevealInOS、CAS 与公共 Workflow pathname loader U）；H2/H3/H4/M3 在已验证修复范围内**已不存在**。G33 **已变化但仍存在**，AC 0/6；G28~G32 与 P12-BUG-01 仍未开始。
- 本次状态：G33 继续进行中，仅收束 dynamic catalog 发布竞态和 packaged source/artifact 证据绑定；没有推进其他 Goal 主体。
- 改动文件：`services/agent_service.go`、`services/agent_execution_core.go`、`services/agent_execution_workflow_skill.go`、`services/agent_execution_workflow_skill_test.go` 串行 catalog clear/rebuild/publish 并增加交错/second-start 测试；`services/agent_execution_mcp.go`、`services/agent_execution_core_test.go` 为锁内 MCP 枚举增加生产 15 秒上限和 timeout 清 source 回归；`scripts/packaged-e2e.mjs`、`scripts/packaged-e2e-driver.test.mjs`、`docs/E2E.md` 实现/说明递归 fingerprint、生成物排除、symlink/junction fail-closed、构建前后复核及严格 artifact reuse。未修改 `.github/workflows`、`build/docker`、package lock 或 Issue/PR/Release 元数据。
- AC：AC1 `[ ] T/I/U`（catalog publish order 取得 T，真实 provider/MCP/CLI I 与其余 mutation 仍缺）；AC2 `[ ] T/U`；AC3 `[ ] T/U`；AC4 `[ ] T/U`；AC5 `[ ] T/U`；AC6 `[ ] T/P/U`（Windows full/reuse P；跨平台/CI 与 npm 门禁未闭环）。G33 保持 0/6，不得宣称完成。
- 验证：`go test ./services -run "TestMCPService_ListAgentMCPToolsPropagatesContextCancellation|TestAgentCatalogMCPRefreshIsBoundedAndFailClosed|TestWorkflowCatalogRefreshesAreSerialized" -race -count=20` exit 0（49.110s），覆盖 catalog serialization、MCP timeout 与 context cancellation 传播；`node --test scripts/packaged-e2e-driver.test.mjs` 14/14 exit 0；`node scripts/backend-gate.mjs` 9/9 exit 0（最终全量 Go test 354.8s）；`task frontend:check` 173/173 files、2765/2765 tests exit 0，ESLint 0 errors/1 existing warning，vue-tsc/bindings/docs 全绿；独立 bindings/doc-links exit 0。source `57c458...` 的完整 build + 严格 reuse 各 24/24 证明 reuse contract 后，MCP timeout/cancellation 源码变化使其转为历史；最终完整 `node scripts/packaged-e2e.mjs` 24/24、exit 0，manifest `recordedAt=2026-08-16T03:48:56.614Z`、`completedAt=2026-08-16T03:52:56.056Z`、`artifactReused=false`、`sourceFingerprintStableAfterBuild=true`，artifact `e3fdd8608e750a3a2cf432f7939cbfd7af2e6c6f07d1655f2adad4402b91e257`，`build-inputs-v2` source `aabfe61ace787475faea4cf7de07faf3ace0ffa0afb00555d90c1ebf308e49ca`、987 files。仅 Windows 本机 P。
- 最终对账：独立 doc-links/doc-numbers/bindings 均 exit 0；实时 source inventory 重算仍为 987 files / `aabfe61ace787475faea4cf7de07faf3ace0ffa0afb00555d90c1ebf308e49ca`。G33 范围 `git diff --check` exit 0；全工作树检查仅因受保护的既有 `build/scripts/build-msi.ps1:124` 尾部空行 exit 1，本 Goal 未修改该文件。npm gate 前后 lock SHA-256 均为 `F1C0AED759A7A5DCCD0C58A2BB685B0C8CC94248D0ED6E36106DA00ADC6AC55F`。
- 首次失败：确定性 hook 首先复现旧 `go version` snapshot 晚于新 `go env GOOS` snapshot 发布并覆盖；串行锁后转绿。artifact 已变为 `d2d15fa...` 而 source 仍为旧 `b9922c...`，证明原手工清单漏掉新 Agent/internal 文件；Node 新契约首轮缺 export exit 1。递归首版又纳入 94 个生成/忽略输入且只取构建前 snapshot；补排除、顶层 junction 拒绝、build/final 双复核与 reuse contract 后 14/14 通过。旧 manifest skip-build 被 `source was not verified after its build` fail-closed 拒绝，exit 1。第一次递归 packaged 实跑因 Git `0xc0000142` 失败，manifest 如实保留 11 passed / 1 failed / 12 not-run；完整重跑后 24/24。
- 安全与数据：catalog 锁不放宽 revision/workspace/session/capability 校验，阻塞 MCP lister 在 deadline 后返回并清空旧 MCP source。锁只串行刷新者，Registry reader/capability 仍可观察逐 source publication 的中间 revision，完整 catalog 单事务原子可见性保持 U。fingerprint 覆盖 untracked G33 build inputs，拒绝 symlink/junction，排除任意深度依赖/缓存/evidence、生成 bindings 与已知构建残留；构建中或 fixtures 中 source 漂移会失败。skip-build 不能凭 marker 把任意旧 artifact 绑定给当前源码。
- 未验证/下一步：`node scripts/npm-audit-gate.mjs` exit 1，official registry/lock stability 通过但 `nanoid <3.3.18` / `GHSA-2v37-7h3g-55p8` 仍为 1 high；hardening 另一锁图不可替代。真实 AI provider/MCP process、Git 其余 mutation、recovery/manual-disposition、cross-caller owner proof、domain rollback、macOS/Linux packaged 与 CI/CLI 仍 U。下一步只继续 G33 的下一未完成 AC1/AC2 子切片；无 commit/push/tag/release。
- SSOT 回写：已同步 prompt-12 §11/§13 与 prompt-9 §8。

### 会话交付：P12-G33 multi-source atomic catalog / mutation fail-closed（2026-08-16，Goal 未完成）

- 复核结论：H1 **仍存在**（Linux 核心读写 `T`、Windows junction `I`；macOS、RevealInOS、持续 CAS 与公共 Workflow pathname loader `U`）；H2/H3/H4/M3 在已验证范围内**已不存在**。G33 **已变化但仍存在**，AC 0/6；G28~G32 与 P12-BUG-01 仍未开始。
- 改动文件：`internal/agentcore/registry.go`、`internal/agentcore/registry_test.go`、`services/agent_execution_core.go`、`services/agent_execution_mcp.go`、`services/agent_execution_workflow_skill.go` 及其测试。新增 `ReplaceSources` 的多 source 候选事务校验/单 revision 发布；普通 refresh 只发布完整候选，builder 失败整批清空；workflow/Skill mutation 先整批撤销 MCP/workflow/Skill，再整批发布，测试覆盖 mixed snapshot、失败候选和有界并发收尾。未修改 `.github/workflows`、`build/docker`、package lock 或 Issue/PR/Release 治理元数据。
- AC：AC1 `[ ] T/I/U`（多 source 原子可见性、mutation 全量撤销与失败清源为 `T`；真实 provider/MCP/CLI `I`、Git/file 其余 mutation 与其余 adapters仍缺）；AC2~AC5 `[ ]`；AC6 `[ ] T/P/U`。G33 保持 0/6，不能宣称 Goal 完成。
- 验证：catalog/mutation/失败清源 services 定向及 `-race -count=20` exit 0；`internal/agentcore` ReplaceSources 组 `-race -count=20` exit 0；H1 junction matrix exit 0；H2 real `cmd.exe` command/commandContext exit 0；H3 integrity Vitest 3 files/41 tests、`vue-tsc --noEmit` exit 0；最终 `node scripts/backend-gate.mjs` 9/9 exit 0（Go test 348.8s）；`task frontend:check` 173/173 files、2765/2765 tests exit 0（ESLint 0 errors/1 existing warning）；packaged `node scripts/packaged-e2e.mjs` exit 0，24/24，artifact `7795a014badc90c7d10f8b23ba17035f85ebe77fa75aeceaf1b5dd1df8a48d01`，source `17f750a156b27ce0f5e3cd7000bce731d203a8862524b31a620151cd0bbd8b27`，987 files，completedAt `2026-08-16T06:02:46.357Z`，`artifactReused=false`、source stable，Windows 本机 `P`。
- 首次失败与修复：`ReplaceSources` 初次测试编译未定义；旧服务交错测试观察到 mixed revision；新增 mutation 负例观察到单 source 清空；失败候选测试观察到 refresh 返回错误仍发布 workflow。另 frontend 首轮因 Wails generator 走 `proxy.golang.org` 超时 exit 1，指定锁定 Wails binary 后通过。所有红灯均保留并在修复后复跑，未删测试、未放宽断言。
- 未验证/安全边界：npm gate 仍 exit 1（`nanoid <3.3.18` / GHSA-2v37-7h3g-55p8 / 1 high，lock SHA 未变）；真实 AI provider/MCP process observation、Git 其余 mutation、recovery/manual-disposition、cross-caller owner proof、domain rollback、macOS/Linux packaged 与 CI/CLI 仍 `U`。handler registration 不随 Registry 回滚，不能宣称 wiring 事务化；mutation 的整批空→整批新有意推进两个 revision。无 commit/push/tag/release。
- SSOT 回写：同步 prompt-12 §11/§13 与 prompt-9 §8。

### 会话交付：P12-G33 MCP lifecycle / TaskService final gate（2026-08-16，Goal 未完成）

- 复核结论：H1 原始全平台范围**仍存在**（Linux 核心读写/变更 `T`、Windows junction `I`；macOS、RevealInOS、持续 CAS 与公共 Workflow pathname loader `U`）；H2 **已不存在**（真实 Windows `cmd.exe` `Command`/`CommandContext` 双路径 `I`）；H3/H4/M3 在已验证范围内**已不存在**。G33 已变化但仍进行中，AC `0/6`；G28~G32 与 P12-BUG-01 未开始。
- 改动文件：`services/mcp_service.go`、`services/mcp_config.go`、`services/mcp_transport.go`、`services/agent_execution_mcp_process_test.go`、`services/task_service.go`、`services/task_service_test.go`、`services/workspace_edit_transaction_test.go`。MCP 生产 teardown 以 `transportLifecycleMu` 串行 Close/Disconnect/Delete/disable/workspace switch，Close 幂等缓存 `closeErr`，stdio transport 单一 owner 负责 Process.Wait/kill，`persistTail` 延迟到 teardown 完成；TaskService 分离 taskkill 子预算与 Stop 外层预算并保留 single-flight/fallback；测试 PID 文件改为原子发布并重试 Windows transient sharing violation；outside-root 测试使用每次唯一 sibling 路径。未修改 `.github/workflows`、`build/docker`、`frontend/package.json`、`frontend/package-lock.json` 或 Issue/PR/Release 元数据。
- 为什么改生产代码：真实并发测试暴露 detach 后第二个 teardown 提前 cancel shutdown context、与 Windows transport.Close/Process.Kill/Wait 竞争的双 owner 窗口；仅改测试会留下 Access denied 与子进程泄漏风险。生命周期不变量为“单 transport 单终止 owner、所有 teardown 同一 mutex 串行、退出错误可观测不吞掉”。
- AC：AC1 `[ ] T/I/U`（Windows 本机真实 MCP stdio workflow 为 I 子证据，跨平台/远端/CI/CLI、真实 AI provider、Git/file 其余 mutation 与其余 adapters仍 U）；AC2 `[ ] T/U`（receipt/compensation 与 teardown T/I 子证据，跨重启 recovery/manual-disposition、domain rollback 仍 U）；AC3 `[ ] T/U`；AC4 `[ ] T/U`；AC5 `[ ] T/U`；AC6 `[ ] T/P/U`。G33 与所有 AC 均不得宣称完成。
- 真实链路与回归：`ConnectServer -> catalog ToolDef ID/revision -> workflow approval/capability -> TaskService -> renderer result -> durable receipt reload -> Close -> ProcessState.Exited`；receipt reload 保持同一 UnitID/ExternalReceiptID，exit-17 自行退出错误可观测。真实 stdio/teardown/child-tree 矩阵 `-race -count=10/20` exit 0；MCP 真实链路仅记 Windows 本机 I 子证据，受控 transport 并发为 T。
- 首次失败与修复：backend-gate 首轮唯一失败为 child-tree 测试 PID 文件短暂为空/共享锁占用并导致 TempDir cleanup 被 child 占用；原子 PID 发布、读取重试与失败 cleanup 后通过。MCP 测试先暴露生产双 owner Close 竞态，加入 lifecycle mutex/single-owner 后通过；未删除测试或放宽断言。
- 门禁与产物：锁定 `WAILS3_BIN=%USERPROFILE%\go\bin\wails3.exe` 下 `node scripts/backend-gate.mjs` 9/9 exit 0（Go test 443.9s）；`task frontend:check` 173/173 files、2765/2765 tests exit 0；bindings/docs/numbers exit 0。`node scripts/packaged-e2e.mjs` exit 0，最新 manifest 24/24 passed、0 not-run，artifact `ef42e7af188ab76fedfd9745231ac3b17c0bae7d6135865dcc92f8cc483af0da`，source `395ac8db26a924b1e4852143807009836b3a791ba98cbb31564ba3aeb0bc624a`（990 files），completedAt `2026-08-16T13:12:23.677Z`；Windows 本机 P，不是跨平台/CI R。
- 供应链/未验证：`node scripts/npm-audit-gate.mjs` exit 1，`nanoid <3.3.18` / `GHSA-2v37-7h3g-55p8` / 1 high，lock SHA 前后均 `F1C0AED759A7A5DCCD0C58A2BB685B0C8CC94248D0ED6E36106DA00ADC6AC55F`；hardening 另一锁图绿灯不可替代。真实 AI provider、跨平台 MCP、recovery/manual-disposition、cross-caller owner proof、domain rollback、真实 CLI/CI 与 macOS/Linux packaged 仍 U。无 commit/push/tag/release。
- SSOT 回写：已同步 prompt-12 §11/§13 与 prompt-9 §8。

### 会话交付：P12-G33 AC3 durable owner/usage authority recovery recheck（2026-08-17，Goal 未完成）

- 复核结论：H1 原始全平台范围**仍存在**（Linux FileService 核心 `T`、Windows junction `I`；macOS、RevealInOS、持续 CAS/换名竞争与公共 Workflow pathname loader `U`）；H2 **已不存在**（真实 Windows `cmd.exe` 双路径 `I`）；G33 **已变化但仍未完成**，AC `0/6`；G28~G32 与 P12-BUG-01 未开始。
- 改动文件：`services/agent_lifecycle.go`、`services/agent_lifecycle_recovery_concurrency_test.go`。trusted usage 先 reverse-map backend-owned opaque plan/goal runtime ID；durable owner row 在 runtime registration 缺失时 fail-closed 为 `ErrUnknownSession`，不重新发放 authority；恢复处置竞态测试保留并明确该拒绝结果。
- AC：AC3 `[ ] T/U`；本轮只补 durable owner/runtime authority T 边界。recovery/manual-disposition dispatcher、跨 caller/window owner proof、domain rollback、workflow attempt 持久化、privacy/retention 与真实恢复 I 仍 U；AC1/2/4/5/6 全部 `[ ]`，G33 不得宣称完成。
- 验证：lifecycle/recovery 组 `-race -count=1` exit `0`；恢复处置/opaque usage/indeterminate retry 矩阵 `-race -count=10` exit `0`；`go test ./services -run "Test.*(Lifecycle|Agent|Usage|TaskService)" -race -count=1 -timeout=20m` exit `0`；`go test -race ./internal/agentcore -count=10 -timeout=15m` exit `0`。此前 full-gate/packaged 证据因本轮源码变化失效，待重跑。
- 首次失败与修复：首次完整组暴露 opaque usage 未反向映射、published-unknown row retry 越过 runtime registration、以及并发恢复断言遗漏 `ErrUnknownSession`；修复后复跑通过，未删除测试或放宽安全断言。
- 安全与未验证：只有当前注册且 incarnation/fingerprint 匹配的 backend owner 能写 usage/observation；poisoned persistence 后续 mutation/retry 不恢复 capability。recovery dispatcher、cross-caller proof、domain rollback、真实 AI/provider/跨平台 MCP、CLI/CI、macOS/Linux packaged 与 npm high advisory 仍 U/红。
- SSOT 与边界：已同步 prompt-12 §11/§13 与 prompt-9 §8；未修改 `.github/workflows`、`build/docker`、package-lock、Issue/PR/Release 元数据；无 commit/push/tag/release。

### 会话交付：P12-G33 AC4 usage ledger collision recheck（2026-08-17，Goal 未完成）

- 首次 backend gate 的全仓 Go 测试（353.6s）唯一失败为 `TestAIPermission_RecordUsage_GetSummary` 偶发少计；根因是 legacy usage 的 synthetic UnitID 仅使用纳秒时间戳，连续同时间戳会触发 divergent terminal 拒绝。
- 改动文件：`services/ai_permission_service.go` 使用 `sync/atomic` 原子序号生成唯一 legacy UnitID；`services/ai_permission_service_test.go` 新增相同时间戳三记录回归。未放宽 receipt 状态机或忽略写入错误。
- 验证：usage 回归 `-count=50` exit `0`；Rust LSP `-count=3` exit `0`（lldb-dap 缺失项按既有 skip）；TaskService Stop `-race -count=10` exit `0`。完整 backend/frontend/bindings/docs/packaged 尚待当前快照重跑。
- AC4 `[ ] T/U`、G33 AC `0/6`；H1 原始范围仍存在、H2 已关闭、npm `nanoid` high 仍红；未修改受保护文件，无 commit/push/tag/release。

### 会话交付：P12-G33 terminal failure-observation authority boundary（2026-08-17，Goal 未完成）

- 全量诊断第二次 backend gate（Go 363.5s）唯一失败为 Goal checkpoint-failure usage：完成态 owner 在 runtime mapping 撤销后被新检查误拒，失败迭代无法落账。
- 修复文件：`services/agent_lifecycle.go` 对 completed row 使用 durable owner claim 验证当前 incarnation，仅允许 `Success=false` 的 trusted failure observation；成功 usage 仍 `ErrInvalidSessionTransition`，不恢复 runtime。`services/agent_lifecycle_usage_owner_test.go` 覆盖成功拒绝、失败落账和 runtime 未重注册。
- 验证：Goal checkpoint failure/terminal usage/indeterminate retry `-race -count=20`、Agent/Usage/TaskService `-race`、agentcore `-race -count=10` 均 exit `0`；完整 gate 需针对最新源码重跑。
- AC3/AC4 仍 `[ ] T/U` 子证据，G33 `0/6`；recovery dispatcher、cross-caller/domain rollback、真实 provider/CI/CLI、跨平台 packaged 与 npm high 仍 U/红；未修改受保护文件，无 commit/push/tag/release。

### 会话交付：P12-G33 latest full gates / packaged reuse（2026-08-17，Goal 未完成）

- 复核结论：H1 原始全平台范围**仍存在**；H2/H3/H4/M3 在已验证范围内**已不存在**。G33 **已变化但仍未完成**，AC `0/6`；G28~G32 与 P12-BUG-01 未开始。
- 改动文件：本子切片仅回写 `docs/prompts/prompt-12.md`、`prompt-11.md`、`prompt-9.md`；此前当前源码切片改动为 `services/agent_lifecycle.go`、两项 lifecycle owner/recovery 测试、`services/ai_permission_service.go` 与其测试。没有手调 Wails bindings，也未修改受保护 workflow/Docker/package-lock/治理元数据。
- AC：AC1 `[ ] T/I/U`、AC2 `[ ] T/U`、AC3 `[ ] T/U`、AC4 `[ ] T/U`、AC5 `[ ] T/U`、AC6 `[ ] T/P/U`。当前门禁/packaged 只补 AC6 子证据；npm high、其余 AC、跨平台 packaged 与真实 CI/CLI 未闭环，任何 AC 均不勾选。
- 验证：`backend-gate.mjs` 9/9 exit `0`（Go test 约 490.4s）；`frontend:check` 173/173 files、2765/2765 tests exit `0`；ESLint/vue-tsc/bindings/docs 与独立 bindings/doc-links/doc-numbers 均 exit `0`。定向 lifecycle/usage/Agent/TaskService/agentcore race、Rust LSP `-count=3` 与 TaskService Stop `-race -count=10` 通过。
- 首次失败：完整 packaged 构建运行先得到 5 passed、`terminal-exit-package` failed、18 not-run、exit `1`；错误为 `start cmd shell: path <temp>\\workspace is outside the workspace root`，recordedAt `2026-08-17T04:45:35.518Z`、completedAt `2026-08-17T04:48:44.555Z`。失败不得隐藏。
- 当前 packaged：无源码变化、无 artifact 重建的严格 reuse 后，权威 manifest 为 24/24 passed、`artifactReused=true`，artifact `4fafb79db047c12d4ae49683fac7bd34898352e848928adb3c1873ee2b0b88d7`，source `c81e6a1c1db120c79c8821841b414be6c62a35887dce2796c440183c547cb84c`（998 files），completedAt `2026-08-17T05:17:13.566Z`。这是 Windows 本机 `P` 与未稳定复现 flake 并存，不是跨平台/CI `R`。
- 安全与未验证：未通过重新 `AddProject`、空 cwd 或放宽 workspace-root 校验掩盖失败；再次复现时先做 deterministic late-setter/rollback 与 workspace commit barrier 测试。npm gate 仍因 `nanoid` high exit `1`；recovery/manual-disposition、cross-caller/domain rollback、真实 provider/CI/CLI、macOS/Linux packaged 与 H1 残余边界仍 `U`。无 commit/push/tag/release。
- SSOT 回写：已同步 prompt-12 §11/§13.27 与 prompt-9 §8；下一步只继续 G33。

### 会话交付：P12-G33 recovery/manual-disposition dispatcher + fresh packaged（2026-08-17，Goal 未完成）

- 复核结论：H1 **仍存在**（Linux 核心 FileService `T`、Windows junction `I`；macOS/RevealInOS/持续 CAS/公共 Workflow pathname loader `U`）；H2/H3/H4/M3 在已验证范围内**已不存在**。G33 **已变化但仍未闭环**，AC `0/6`；G28~G32 与 P12-BUG-01 未开始。
- 改动文件：`internal/agentcore/session.go`、`session_test.go`、`session_persistence_commit_test.go`、`session_recovery_concurrency_test.go`；`services/agent_lifecycle.go` 与其 persistence/dispatcher/unscoped/concurrency 回归测试。dispatcher 仅接受 backend-only opaque-handle `discard`，不注册 Wails；公开 DTO/错误不含 logical ID、owner、root、stream、checkpoint 或存储细节。poison 检查先于幂等 replay；pre-publish 失败可用同 handle 重试，post-publish unknown poison 当前进程，fresh reload 才确认 durable discard；workspace-switch/discard 竞态只允许 durable discard 或原 quarantine + `ErrNotAllowed`。未修改 workflow、Docker、lockfile 或治理元数据。
- AC：AC1 `[ ] T/I/U`、AC2 `[ ] T/U`、AC3 `[ ] T/U`、AC4 `[ ] T/U`、AC5 `[ ] T/U`、AC6 `[ ] T/P/U`。本子切片只取得 AC3 backend-only recovery/manual-disposition `T` 子证据；真实 operator/CLI consumer、cross-window/caller owner proof、真正 resume、domain rollback、真实 provider/CI、跨平台 packaged 与 npm high 仍 `U`/红，G33 不得宣称完成。
- 首次失败与修复：红测发现 logical ID/path/`secret-marker` 泄露、durability unknown 后错误成功、poison inventory 空成功，以及 workspace-switch 交错窗口。修复为 keyed HMAC handle、错误脱敏、poison-first checked inventory、同一 transition 锁复核，并保留严格安全断言；未删除测试或放宽断言。
- 验证：workspace-switch recovery `-race` exit `0`；services recovery/lifecycle/dispatcher `-race -count=20` exit `0`（25.873s）；`internal/agentcore` recovery/persistence/disposition `-race -count=20` exit `0`（31.781s）；`go vet ./services ./internal/agentcore`、gofmt、G33 scoped diff check、Wails bindings contract 16/16、check-bindings 均 exit `0`。`node scripts/backend-gate.mjs` 9/9 exit `0`（全仓 Go test 516.6s）；`task frontend:check` 173/173 files、2765/2765 tests exit `0`，ESLint 0 errors/1 existing warning，vue-tsc/bindings/docs 全绿。根 `npm run frontend:check` 的 ENOENT 首败因根目录无 package.json，按 Taskfile 权威入口重跑。
- 供应链与 packaged：`node scripts/npm-audit-gate.mjs` exit `1`，official URL/lock stability 通过但 `nanoid <3.3.18` / `GHSA-2v37-7h3g-55p8` 仍为 1 high，未改 lockfile。fresh `node scripts/packaged-e2e.mjs` exit `0`，manifest 24/24 passed、0 not-run、`artifactReused=false`、`sourceFingerprintStableAfterBuild=true`，artifact `4dd46705c402d414170a65f9c1e4f1d9650fdfa22e38009e737b5a2e91a1508b`，source `a51f0db27cbf708baf3c6d3c3138453384ea55a0e1c85d26baa0ff86c0f1f23b`（1000 files），completedAt `2026-08-17T07:29:36.987Z`。一次 Windows WebView cleanup `EBUSY` retry 最终 exit `0`；该证据仅为 Windows 本机 `P`，不升级为跨平台/CI `R`。旧 §13.27 的 packaged 首次失败与 reuse 记录仍保留为历史。
- SSOT 回写：已更新 prompt-12 §11/§13.28、prompt-9 §8 与本节；无 commit/push/tag/release。

### 会话交付：P12-G33 external receipt recovery identity/authority + final shared-tree gates（2026-08-17，Goal 未完成）

- 复核结论：H1 **仍存在**（Linux 核心 FileService `T`、Windows junction `I`；macOS/RevealInOS/持续 CAS/公共 Workflow pathname loader `U`）；H2 已由真实 Windows `cmd.exe` command/commandContext 矩阵关闭。G33 **已变化但仍未闭环**，AC `0/6`。
- 行为与边界：新增未注册 Wails 的 `AgentExternalReceiptRecoveryDispatcher`，inventory/result 只暴露稳定 opaque HMAC handle、状态和时间，只接受精确 `manual-unknown`。它不调用 adapter、不声称 resume/rollback/compensation；session recovery 的独立 dispatcher 仍只接受 `discard`。per-config identity key 跨进程锁定、严格 64 hex，Unix 要求 `0600`；legacy-only ledger 可首次创建，已有 external receipt 历史时缺失/损坏/宽权限会 poison，禁止重建或轮换。Plan/Goal runtime ID 先归一为 logical lifecycle ID，completed owner 仅在旧 runtime authority 已撤销后允许处置。
- 持久化合同：pre-publish 失败保持 pending 并可用同 handle 重试；post-publish durability unknown poison 当前进程，fresh reload 后再确认。workspace generation/fingerprint、owner incarnation 与 runtime authority 在发布期间复核；公开 usage/recovery 错误只返回固定 sentinel，不泄漏 configDir、路径、receipt ID 或底层错误。
- 首次失败与修复：services 全量唯一失败是旧 `TestProductionRuntimeUsageReceiptPreventsWriteWhenLedgerUnavailable` 仍匹配历史错误文本；生产 fail-closed 正确。夹具改为先干净加载，再用目录占住 `usage_log.jsonl`，断言精确 `ErrUsagePersistence`、handler 未执行、内存 ledger 为空且公开结果不泄漏路径。frontend/backend/packaged 首次因 Wails 联网安装超时；显式使用本机精确 `v3.0.0-alpha2.111` 后通过。另一次 packaged 与共享树已有构建并发写 `frontend/dist`，Vite `vue.svg` 复制 `EBUSY`、0/24 fixture 启动；未终止对方进程，单 owner 联动运行随后通过。
- 验证：external receipt/AIPermission/public-redaction 定向与 recovery/lifecycle/usage-owner `-race`、`go vet ./services ./internal/agentcore`、`go test ./internal/agentcore -count=1` 均 exit `0`；`go test ./services -count=1` exit `0`（194.290s）。同一稳定共享树 backend gate 9/9（全仓 Go test 198.4s）；frontend 173/173 files、2765/2765 tests，ESLint 0 errors/1 existing warning，vue-tsc、bindings 16/16、docs 全绿。
- packaged/供应链：fresh Windows manifest 24/24 passed、0 failed/0 not-run、`artifactReused=false`、source build 后稳定；artifact `55d90e32f5d36c1412a6495e8aa7318a83e0dc3b96e9bd91a7944c6006c7f103`，source `78e8c29ac5370f2d9010e6dd06e7f8e6b681ce0e5c7f2b6298830c7423220d06`（1003 files），completedAt `2026-08-17T09:28:32.731Z`，仅本机 `P`。npm gate 仍因 `nanoid <3.3.18` / `GHSA-2v37-7h3g-55p8` / 1 high exit `1`，lockfile 未改。
- AC/未验证：AC1 `[ ] T/I/U`、AC2 `[ ] T/I/U`、AC3 `[ ] T/U`、AC4 `[ ] T/U`、AC5 `[ ] T/U`、AC6 `[ ] T/P/U`。真实 operator/CLI、真正 resume、adapter-specific compensation、ambiguous commit、跨进程 durable single-writer/CAS、cross-window/caller proof、domain rollback、真实 provider/CI 与 macOS/Linux packaged 仍 `U`。未修改 GitHub workflow、Docker、frontend manifest/lock 或治理元数据；无 commit/push/tag/release。
- SSOT 回写：同步 prompt-12 §11/§13.29、prompt-9 §8 与本节；下一步仍只继续 G33。

### 会话交付：P12-G33 durable workflow attempt authority + fresh packaged（2026-08-17，Goal 未完成）

- 复核结论：H1 原始全平台范围**仍存在**（Linux 核心 FileService `T`、Windows junction `I`；macOS/RevealInOS/持续 CAS/公共 Workflow pathname loader `U`）；H2 **已不存在**（真实 Windows `cmd.exe` command/commandContext 双路径 `I`）；H3/H4/M3 在已验证范围内**已不存在**。G33 **已变化但仍未完成**，AC `0/6`；G28~G32 与 P12-BUG-01 未开始。
- 改动文件：`services/task_service.go` 删除 workflow attempt 内存 map 权威并以 `workflowAttemptMu` 串行状态迁移；`services/ai_permission_service.go` 增加 poison-aware、canonical/唯一 pending attempt lookup；新增 `services/task_service_workflow_attempt_persistence_test.go`。未修改 GitHub workflow、Docker、frontend manifest/lock 或治理元数据。
- 行为与安全：durable usage ledger 是 attempt SSOT；TaskService 重建复用原 UnitID，pre-publication terminal 写失败不消费 pending receipt。多条或其他 pending、错误 kind/operation/provider/cost、poison、缺失均 fail-closed，不泄漏 UnitID/路径。lifecycle reload 只找回 attempt，不恢复旧 runtime authority；并发 Complete 只允许一个成功 terminal。跨进程 CAS/真正 resume/operator recovery 不在本切片证明范围。
- 首次失败与修复：红测复现 TaskService 重建后 attempt missing、terminal 写首次失败后内存 receipt 丢失，以及同 session 两条 pending 仍错误成功；ledger lookup 与严格身份/唯一性检查后转绿，未删除测试或放宽安全断言。
- 验证：workflow attempt `-race -count=10`、workflow/lifecycle/usage/external recovery 组合 `-race -count=10`、全部 `TestTaskService* -race`、`internal/agentcore -race -count=10`、go vet/gofmt/scoped diff check 均 exit `0`。backend gate 首轮全仓 Go 380.6s 通过但 Wails 联网安装超时，整体 exit `1`；固定本机 `v3.0.0-alpha2.111` 后 9/9 exit `0`（Go test 206.6s）。`task frontend:check` 173/173 files、2765/2765 tests，ESLint 0 errors/1 existing warning，vue-tsc、bindings 16/16、docs 全绿。
- packaged/供应链：fresh manifest 24/24 passed、0 failed/0 not-run、`artifactReused=false`、source stable；artifact `65ca828752f20590edfd88987c84a1548e99167d79f71e909b70192ca3098300`，source `d6033de324af040178161221fb6890041a0cc064a284cceed45974cc1abf84d3`（1004 files），completedAt `2026-08-17T10:28:31.707Z`，仅 Windows 本机 `P`。WebView cleanup 首次 `EBUSY` 经既有 retry 收束。npm gate 仍因 `nanoid <3.3.18` / `GHSA-2v37-7h3g-55p8` / 1 high exit `1`；lock SHA 前后均 `F1C0AED759A7A5DCCD0C58A2BB685B0C8CC94248D0ED6E36106DA00ADC6AC55F`，锁文件未改。
- AC/未验证：AC1 `[ ] T/I/U`、AC2 `[ ] T/I/U`、AC3 `[ ] T/U`、AC4 `[ ] T/U`、AC5 `[ ] T/U`、AC6 `[ ] T/P/U`。本轮只把 workflow attempt persistence 的本地 durable/reload 子边界升为 `T`；cross-window/caller owner proof、真正 resume、workspace/domain rollback、stream privacy/retention、跨进程 CAS、真实 CLI/CI/provider 与跨平台 packaged 仍 `U`。G33 保持 `0/6`；无 commit/push/tag/release。
- SSOT 回写：同步 prompt-12 §11/§13.30、prompt-9 §8 与本节；下一步仍只继续 G33。

### 会话交付：P12-G33 workspace authority/catalog admission closure + fresh packaged（2026-08-19，Goal 未完成）

- 复核结论：H1 原始全平台范围**仍存在**（Linux 核心 FileService `T`、Windows junction `I`；macOS/RevealInOS/持续 CAS/公共 Workflow pathname loader `U`）；H2 已由真实 Windows `cmd.exe` `Command`/`CommandContext` 注入矩阵 `I` 关闭；H3/H4/M3 在已验证范围内已不存在。G33 **已变化但仍未闭环**，AC `0/6`；G28~G32 与 P12-BUG-01 未开始。
- 改动文件：`services/agent_execution_core.go` 将普通 dynamic refresh 的 deferral 判定纳入 `catalogRefreshMu` admission；`services/agent_execution_workflow_skill.go` 让 Workflow/Skill mutation 复用同一 helper；`services/agent_service.go`、`services/project_service.go`、`services/project_workspace_clear.go` 保持 workspace authority 在 root setter、账本保存和 snapshot publication 期间有效，并在失败时逆序回滚/poison；新增 `services/project_service_agent_mcp_transaction_test.go` admission 红测与 `services/project_service_agent_rollback_test.go`、`services/agent_lifecycle_workspace_authority_test.go` 回归。未修改 `.github/workflows`、`build/docker`、`frontend/package-lock.json` 或 Issue/PR/Release 治理元数据。
- 首次失败与修复：`TestProjectServiceWorkspaceSwitchDrainsRefreshAdmissionBeforeSetters` 在旧实现 exit `1`，复现 refresh 通过 deferral 检查后尚未拿到 catalog 锁、workspace setter 越过 drain 的窗口；修复后该测试 `-race -count=10` exit `0`，更广 workspace/lifecycle/catalog 组合 `-race -count=10`、`go vet ./services ./internal/agentcore`、gofmt 与 scoped diff-check 均 exit `0`。没有删除测试或放宽 fail-closed 断言。
- AC：AC1 `[ ] T/I/U`、AC2 `[ ] T/I/U`、AC3 `[ ] T/U`、AC4 `[ ] T/U`、AC5 `[ ] T/U`、AC6 `[ ] T/P/U`；本轮只取得 catalog admission/workspace rollback 的 `T` 子证据与 Windows 本机 packaged `P`，不勾选任何 AC。跨平台/CI operator/CLI、真正 resume/compensation、cross-caller/domain rollback、跨进程 CAS、真实 provider、macOS/Linux packaged 与 npm high 仍 `U`/红。
- 验证：固定 `%USERPROFILE%\go\bin\wails3.exe` `v3.0.0-alpha2.111` 后 `node scripts/backend-gate.mjs` 9/9 exit `0`（全仓 Go test 368.0s）；`task frontend:check` 173/173 files、2765/2765 tests exit `0`，ESLint 0 errors/1 existing warning、vue-tsc/bindings/docs 全绿。final fresh `node scripts/packaged-e2e.mjs` exit `0`，24/24 passed、0 failed/0 not-run、`artifactReused=false`，artifact `f760f16d5514d7c9cbc24fd51c87793f32fbdeb9f36bd7bfd124eed6310635cf`，source `68b492caf9ebf4c780d76a2691d4c63818977d87537b9d127dba364fc3623630`（1027 files），completed `2026-08-18T20:51:05.790Z`，Windows 本机 `P`。cleanup 首次 `EBUSY` 经 bounded retry 收束，source fingerprint 独立重算匹配。`node scripts/npm-audit-gate.mjs` exit `1`，唯一 high 为 `nanoid <3.3.18` / `GHSA-2v37-7h3g-55p8`，lockfile 未改。
- 安全与未验证：catalog refresh 失败清空动态 source；项目保存/ setter/补偿失败不提交混合 workspace，必要时 poison Agent authority。真实跨平台/CI/CLI consumer、完整 domain rollback、跨进程 CAS、真实 provider 与 macOS/Linux packaged 仍 `U`。无 commit/push/tag/release。
- SSOT 回写：已同步 prompt-12 §13.34 与 prompt-9 §8；下一步仍只继续 G33 下一未完成 AC。

### 会话交付：P12-G33 legacy MCP renderer execution surface closure（2026-08-19，Goal 未完成）

- 复核结论：H1 原始全平台范围仍存在（Linux FileService 核心 `T`、Windows junction `I`；macOS/RevealInOS/持续 CAS/公共 Workflow pathname loader `U`）；H2 真实 Windows `cmd.exe` `Command`/`CommandContext` 双路径 `I` 已不存在；G33 已变化但仍未闭环，AC `0/6`。G28~G32 与 P12-BUG-01 未开始。
- 改动文件：`services/agent_service.go` 的 `CallMCPTool` deny-only shim 增加 `//wails:ignore`；`bindings_runtime_surface_test.go`、`scripts/lib/wails-bindings.mjs`、`scripts/wails-bindings.test.mjs` 增加 forbidden/ignored 合约；使用锁定 Wails `v3.0.0-alpha2.111` 重生成 manifest/generated bindings。MCP CRUD/discovery（含 `WorkspaceRoot`）仍导出；没有把整个 MCP surface 误写成关闭。未修改 `.github/workflows`、`build/docker`、`frontend/package-lock.json` 或治理元数据。
- AC：AC1 `[ ] T/I/U`、AC2 `[ ] T/I/U`、AC3 `[ ] T/U`、AC4 `[ ] T/U`、AC5 `[ ] T/U`、AC6 `[ ] T/P/U`；本切片只取得旧 renderer MCP 执行入口收口的 `T` 与当前 Windows packaged `P`，不勾选任何 AC。
- 首次失败与修复：编辑后首个 gate 发现 `bindings_runtime_surface_test.go` 未 gofmt，格式化后通过；无 `WAILS3_BIN` 的 packaged 首轮因 `proxy.golang.org` 不可达而 exit `1`，显式本机锁定 CLI 后重跑。根目录 `npm run frontend:check` 因无 `package.json` exit `1`，权威入口为 Taskfile。
- 验证：legacy deny-only `-race -count=10`、MCP/Agent/Task 组合 race、Go runtime surface、Wails binding contract 16/16、`check-bindings` 均 exit `0`；固定 Wails 后 `node scripts/backend-gate.mjs` 9/9（Go test 257.0s）exit `0`；`task frontend:check` 174/174 files、2791 tests、ESLint 0 errors/1 existing warning、vue-tsc/bindings/docs 全绿；packaged driver 14/14 exit `0`。
- packaged/供应链：fresh manifest 24/24 passed、0 failed/0 not-run、`artifactReused=false`、source stable；artifact `4d27d2b9b76b2fad07c547bc209f8198e4a0ac59bb5bab2f53320df7e7a5729c`，source `4063d6d1ee7a36cf488469edafbfbb492d1d9c4218dc6ce8556aea5abea21cf3`（1034 files），completed `2026-08-19T09:43:26.420Z`，Windows 本机 `P`。npm gate 仍因 `nanoid <3.3.18` / `GHSA-2v37-7h3g-55p8` 1 high exit `1`，lockfile 未改；全局 diff-check 仍有既有 `build-msi.ps1` EOF 空行，切片 scoped clean。
- 安全与未验证：旧 renderer MCP execution/approval shim 不触达 client/handler，统一 Agent capability 管线仍是生产路径；真实 provider、完整 MCP 协议/跨平台 process、Git mutation、recovery/manual-disposition、cross-caller/domain rollback、跨进程 CAS、CI/CLI 与 macOS/Linux packaged 仍 `U`。无 commit/push/tag/release。
- SSOT 回写：已同步 prompt-12 §13.35 与 prompt-9 §8；下一步仍只继续 G33 下一未完成边界。

### 会话交付：P12-G33 ordinary AI operation admission for chat/inline（2026-08-19，Goal 未完成）

- 复核结论：H1 原始全平台范围**仍存在**（Linux FileService 核心读写/变更 `T`、Windows junction `I`；macOS、RevealInOS、持续 CAS/换名竞争与公共 Workflow pathname loader `U`）；H2 真实 Windows `cmd.exe` `Command`/`CommandContext` 双路径注入矩阵 `I` 已关闭；G33 **已变化但仍未闭环**，AC `0/6`，G28~G32 与 P12-BUG-01 仍未开始。
- 改动文件：`services/ai_service.go`、`services/ai_agent.go`、新增 `services/ai_permission_boundary_test.go`。`Send`（`AIOpChat`）与 `Complete`（`AIOpInlineCompletion`）在 lifecycle/usage 和 HTTP 前走 backend-owned operation resolver；Disabled fail-closed，assigned provider 由 SettingsService 按 `ConfigID` hydration endpoint/key/model，未分配仍兼容 global config。未修改 `.github/workflows`、`build/docker`、`frontend/package-lock.json` 或治理元数据。
- 首次失败与修复：红测先复现旧实现 disabled chat/inline provider hit 及 assigned global-provider 命中；修复后 disabled hit=0、assigned exact endpoint/auth/model、unassigned global compatibility 均通过。未删测试或放宽断言。
- AC：AC1 `[ ] T/I/U`、AC2 `[ ] T/I/U`、AC3 `[ ] T/U`、AC4 `[ ] T/U`、AC5 `[ ] T/U`、AC6 `[ ] T/P/U`；本轮只取得普通 chat/inline admission `T` 子证据，不勾选任何 AC。标题、StartStream/StartAgentStream target/lifecycle、fallback identity、provider output/body budgets、frontend config race、真实 provider/CI/CLI 与完整 MCP 协议仍 `U`。
- 验证：operation 定向 `go test` exit `0`；operation/resolver `-race -count=10` 与 AI/Resolve race exit `0`；`go vet ./services`、gofmt、scoped diff-check exit `0`。固定 `%USERPROFILE%\go\bin\wails3.exe` 后 backend gate `9/9` exit `0`（Go test 241.5s）；无固定 Wails 首轮仅因 proxy 安装连接失败 exit `1`，不算代码失败。`task frontend:check` exit `0`（174/174 files、2792/2792 tests、ESLint 0 errors/1 existing warning、vue-tsc、bindings `16/16`、docs 全绿）。fresh packaged exit `0`，24/24 passed、0 failed/0 not-run、`artifactReused=false`、artifact `7d1d9cf475e3a6101bda3a02c3748abf13d6258ebbd2ef0d21c0a1bb70a7ccf7`、source `c37d6e0280749bb4221dd2933e563854e69b67474537e2ca682e45b044f54359`（1035 files）、`recordedAt=2026-08-19T14:09:04.000Z`、`completed=2026-08-19T14:10:56.000Z`；独立 source 重算一致，Windows 本机 `P`。npm nanoid high 仍红且 lockfile 未改。
- SSOT 回写：已同步 prompt-12 §13.37 与 prompt-9 §8；G33/AC 仍 `0/6`，无 commit/push/tag/release；下一步仍只推进 G33 下一未完成边界。

### 会话交付：P12-G33 ordinary AI title/stream admission（2026-08-19，Goal 未完成）

- 复核结论：H1 原始全平台范围仍存在（Linux FileService 核心 `T`、Windows junction `I`；macOS/RevealInOS/持续 CAS/公共 Workflow pathname loader `U`）；H2 真实 Windows `cmd.exe` 双路径 `I` 已关闭；G33 已变化但仍未闭环，AC `0/6`。
- 改动文件：`services/ai_service.go`、`services/ai_agent.go`、`services/ai_permission_boundary_test.go`。title 与两条 stream 入口在 provider/lifecycle/网络副作用前执行 backend-owned operation admission；Disabled fail-closed，assigned provider 由 SettingsService `ConfigID` hydration，unassigned 保留 global compatibility。`StartAgentStream` 当前使用 `AIOpChat` admission/usage，`AIOpAgent` 映射待独立决策；未修改受保护 workflow/Docker/package-lock/治理元数据。
- 首次失败与修复：title/stream 旁路红测及 Agent fixture 未设置 permission service；补齐 production admission 与 fixture wiring 后定向 disabled/assigned/unassigned 断言通过。无测试删除或断言放宽。
- AC：AC1 `[ ] T/I/U`、AC2 `[ ] T/I/U`、AC3 `[ ] T/U`、AC4 `[ ] T/U`、AC5 `[ ] T/U`、AC6 `[ ] T/P/U`；本次只补 title/chat/Agent stream admission 的 T 子证据与 packaged P，不勾选 G33 AC。
- 验证：operation focused、AI/Resolve race、`go vet ./services`、gofmt、scoped diff-check 均 exit `0`；固定 `WAILS3_BIN=%USERPROFILE%\go\bin\wails3.exe` 后 backend gate 9/9（Go test 354.6s）exit `0`；`task frontend:check` 174/174 files、2792 tests、ESLint/vue-tsc/bindings/docs 全绿；bindings/docs links/numbers exit `0`；driver contract 14/14 exit `0`。
- 首次失败与修复门禁：未固定 Wails 的 frontend/bindings 首次因 `proxy.golang.org` 连接失败 exit `1`，固定本机精确版本后通过；npm gate 保留 exit `1`（`nanoid <3.3.18` / `GHSA-2v37-7h3g-55p8` / 1 high，lockfile 未改）。
- packaged：fresh Windows manifest 24/24 passed、0 failed/0 not-run、`artifactReused=false`、source stable；artifact `fab0e53fc8dea7efa969e88047ebe74af7e3ff29498cefbebf98bef6a1c36e13`，source `90298aad599eb87b0fadd9479bfa729119d835717a1a2316a27695ff549ac83a`（1035 files），completed `2026-08-19T15:02:23.562Z`，独立重算一致；仅 Windows 本机 `P`，不升级为跨平台/CI `R`。
- 未验证/下一步：renderer target 缺失仍可能静默丢事件，caller cancellation/worker retention、fallback identity、provider output/body budget、frontend config race、真实 provider/CI/CLI 与完整 MCP 协议仍 `U`；下一步只继续 G33 stream target/lifecycle 边界。无 commit/push/tag/release。
- SSOT 回写：已同步 prompt-12 §13.38 与 prompt-9 §8。

### 会话交付：P12-G33 renderer stream visibility / Agent tool-turn usability（2026-08-20，Goal 未完成）

- 复核结论：H1 原始全平台范围仍存在（Linux `T`、Windows junction `I`，macOS/Reveal/CAS/公共 Workflow pathname loader `U`）；H2 真实 Windows `cmd.exe` 双路径 `I` 已关闭。G33 已变化但 AC 仍 `0/6`；AI 首 chunk 需重进刷新已不存在，Agent tool observation turn barrier 与 standalone approval UI 已取得 `T` 子证据。
- 改动文件：`frontend/src/stores/ai.ts`/`ai.test.ts`、`frontend/src/stores/agent.ts`/`agent.test.ts`、新增 `agentTimeline.ts`/测试与 `AgentExecutionTimeline.vue`/测试；`MessageList.vue`、`AiChatPanel.vue` 及回归测试；新增 `AgentToolCalls.vue`/测试。流式 target 使用 Vue proxy；pre-admission event 有界并按 stream owner 过滤；`setConfig` 被 await；工具批准/执行/observation 串行等待前一流终态，reset/workspace generation 使旧任务失效。时间线只显示明确 provider summary 与工具阶段，不显示或推断隐藏思考链。独立 Agent 页面现在可批准/拒绝工具并检查风险、参数与结果。
- 首次失败：错误 npm script、未固定 Wails 的 pretest 网络安装、测试 mock raw/reactive state 不一致以及一次本机 CLI 路径拼写错误均保留；改用权威命令、锁定 CLI 与同一 reactive mock 后转绿。未删测试、未放宽断言。
- 验证：focused `5 files/191 tests` 与新增 standalone UI `2 files/10 tests` exit `0`；固定 `WAILS3_BIN=%USERPROFILE%\go\bin\wails3.exe` 后当前静止树 `node scripts/backend-gate.mjs` **9/9 exit `0`**（`go test ./... -count=1` 411.7s、contract/bindings/Wails/docs 全绿）；最终 `task frontend:check` `177/177 files`、`2813/2813 tests`，ESLint、vue-tsc、bindings `16/16`、drift/docs 均 exit `0`；scoped diff-check clean。fresh Windows packaged `24/24`、0 failed/0 not-run、`artifactReused=false`，artifact `83cab12cdb949aa731889dd3e80fa8aebaf36dc5c75ab2cec15fcb51fff63151`，source `7084b637f68f6a3b9950f1e965f962128c5fba9991e59b5b53b1e62822fda3d2`（1042 files），completed `2026-08-19T18:05:19.742Z`，仅 Windows `P`。
- AC/阻塞：AC1 `[ ] T/I/U`、AC2 `[ ] T/I/U`、AC3 `[ ] T/U`、AC4 `[ ] T/U`、AC5 `[ ] T/U`、AC6 `[ ] T/P/U`。workflow AI fallback approval 仍未冻结 fallback；真实 provider/output/usage、完整 MCP、跨平台/CI/CLI、domain recovery/CAS 仍 `U`。npm gate 仍因 `nanoid <3.3.18` high exit `1`，lockfile 未改。G33 不宣称完成；无 commit/push/tag/release。
- SSOT 回写：已同步 prompt-12 §13.39 与 prompt-9 §8；下一步仍只推进 G33 下一未完成安全/真实 provider 边界。

### 会话交付：P12-G33 Agent usability / conversation handoff / packaged tool round（2026-08-20，Goal 未完成）

- 复核结论：H1 原始全平台范围仍存在（Linux核心 `T`、Windows junction `I`，macOS/Reveal/CAS/公共 Workflow pathname loader `U`）；H2 真实 Windows `cmd.exe` 双路径 `I` 已关闭。P12-BUG-02 已加入 G33 执行计划；流式需重进、工具 observation 静默丢失与默认对话文件写 cwd 已不存在，真实 provider/manual mutation/restart/跨平台等总体 Agent 缺口仍存在。G33 AC 保持 `0/6`。
- 改动文件：conversation handoff/target 的 `frontend/src/stores/aiAssistant.ts`、`AiWindowView.vue`、`crossWindowSync.ts`、ActivityBar/MainLayout 及测试；packaged Agent probe 的 `agentToolRoundProbe*.ts`、`internal/e2e/server.go`、`packaged-e2e*.mjs` 及测试；`services/conversation_service.go`/测试修复默认 storage root。UI 沿原有风格展示 provider 明示摘要与批准/执行/结果，不显示隐藏 chain-of-thought。未修改 `.github/workflows`、`build/docker`、`frontend/package.json`/`package-lock.json` 或治理元数据。
- 首次失败与修复：旧 ConversationService 在 `pathFor` 后才解析默认目录，红测 constructor/zero-value 均返回 cwd-relative path；fresh packaged 首轮因此在 24 fixtures 后生成根目录会话 JSON并以 `source-final-verification` failed 收束。统一 eager/defensive root 后，Load 不接受 cwd 伪造、Save 不写 cwd；失败 JSON移入 evidence 保留。随后一次 packaged frontend-build 因锁定 Wails 联网安装失败；设置精确 `WAILS3_BIN` 后重跑，未放宽 bindings 或 fingerprint。
- AC：AC1 `[ ] T/I/U`、AC2 `[ ] T/I/U`、AC3 `[ ] T/U`、AC4 `[ ] T/U`、AC5 `[ ] T/U`、AC6 `[ ] T/P/U`；本轮只增加 handoff/tool-round/default-root 的 `T/P` 子证据，不勾选任何 AC。
- 验证：conversation 相关 `go test -race`、Agent probe `12/12`、driver `25/25`、`go test -race -tags=e2e ./internal/e2e` exit `0`；前端 Agent/handoff 静止源码定向 `8 files/252 tests` + `3 files/91 tests`，权威 `frontend:check` `178/178 files`、`2869/2869 tests` exit `0`；最终 `node scripts/backend-gate.mjs` 9/9 exit `0`（全仓 Go 418.2s）。fresh Windows packaged 24/24、artifact `5bb669311cf5dfe8848bc25e49a3b49512ae99bb3d1c05cf6227a4f216086fbc`、source `864bcd97f016e1a7aaad7ea2e17a1a6e044512cf29dde3c747baf86d23c30143`（1045 files）、`artifactReused=false`、completed `2026-08-20T09:58:45.734Z`；非空 UnitID、同 session terminal read usage、approval 顺序、FileService observation 与恰好两次 provider request 均匹配。仅 Windows 受控 provider `P`。
- 未验证/下一步：真实 provider、manual/mutating approval、实际双 WebView handoff、restart ledger、primary+fallback freeze、provider body/SSE/tool/usage budget、完整 MCP、macOS/Linux packaged 与 CI/CLI 仍 `U`。npm gate仍因 `nanoid <3.3.18` high exit `1`，lock SHA `F1C0AED7...` 未变；P12-BUG-01 仍存在。下一步只继续 G33/P12-BUG-02 的真实 provider/输出预算切片。无 commit/push/tag/release。
- SSOT 回写：已同步 prompt-12 §11/§12.8/§13.40 与 prompt-9 §8。

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

## 10. 一键续作词（2026-08-14：按 prompt-12 转入 G33）

```text
严格按 docs/prompts/prompt-12.md §11/§13 执行，并继承 prompt-9 §0 与本文 §0。当前下一 Goal 为 P12-G33；先复核最新 manifest 和 G33 现状，开始时写明缺口“仍存在 / 已变化 / 已不存在”，一次只推进 G33。所有 AC 以 S/T/I/P/R/U 分级，先补失败测试；mock/contract 不得冒充 I/P/R，未勾选 AC 不得宣称完成，被外部状态阻塞的 U 项保持 U。安全修改 fail-closed，用户数据写入证明冲突/崩溃/回滚，bindings 用锁定版本生成。完成或暂停时按 §9 交付，并立即同步 prompt-12 §11/§13、prompt-9 §8 与本文。不要 commit、push、tag 或发布，除非用户明确要求。
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
