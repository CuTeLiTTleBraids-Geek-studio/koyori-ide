# Koyori IDE 长期修复与平台化 Goal 任务（prompt-9）

> 用途：供后续 AI 在多次会话中持续修复 Koyori IDE。本文是 `prompt-8.md` 完成声明后的审计纠偏与新 SSOT，不继承其中未经真实运行证明的“已完成”状态。
>
> 当前定位：Koyori IDE 是基于 Go、Wails v3 alpha、Vue 3、TypeScript、Vite 和 Monaco 的 0.x 桌面 AI IDE。完成本文 P0 前，不得宣称“日常可用”；完成 P1 前，不得宣称“开源发布合格”；完成 P2 且有真实矩阵证据前，不得宣称“全语言、国际化、插件化 IDE”或 VS Code/Cursor 的替代品。

---

## 0. AI 执行总指令（不可弱化）

1. 先读代码、测试和运行结果，再接受本文现状。每个 Goal 开始时必须写明相关缺口“仍存在 / 已变化 / 已不存在”，行号仅作线索。
2. 一次只推进一个 Goal。按依赖顺序选择第一个“未开始”或“进行中”的 Goal；长期 Goal 可跨会话保持“进行中”，但不得同时修改下一个 Goal。
3. 每次只做使当前 Goal 闭环的最小正确改动。不得通过新增面板、占位按钮、mock 或文档声明掩盖核心工作流断裂。
4. AC 未全部勾选时，Goal 不得写成“完成”。Goal 状态必须由 AC 与证据自动推导，叙述、代码量和测试数量不得覆盖失败门禁。
5. 证据分级固定为：
   - `S`：静态源码、配置或文档检查；只能证明内容存在。
   - `T`：单元测试、mock、fixture、contract smoke；不能证明真实进程或桌面产品可用。
   - `I`：真实服务、真实子进程、真实 LSP/DAP/Test/Git/PTY/网络系统的集成验证。
   - `P`：从真实 packaged 桌面产物完成用户工作流，并保存日志、截图或录像及产物摘要。
   - `R`：真实 CI、tag、release、签名、公证、SBOM、provenance 或一段时间的运行历史。
   - `U`：未验证、环境阻塞或证据缺失。
6. mock/contract 结果不得升级为 `I`、`P` 或 `R`。dry-run 不等于 packaged E2E；YAML 存在不等于真实 CI；设计文档不等于运行时实现。
7. 安全能力默认 fail-closed。renderer 传入的 `approved`、`safe`、路径、root、私网许可和进程参数都不是授权；授权必须由后端签发，绑定参数、workspace generation、TTL，并单次消费。
8. 用户数据优先。保存、恢复、批量编辑、AI Diff、设置同步和更新不得静默覆盖、部分成功伪装成功或在失败后丢失恢复路径。
9. 禁止手工猜测 Wails binding ID 或复制旧错误 FQN。必须使用仓库锁定的 Wails 版本生成 bindings，再由检查脚本验证生成结果、导出面和禁止项。
10. 不删除测试保绿，不放宽安全断言，不用 `any`/类型压制隐藏问题，不提交 secret，不擅自 commit、push、tag 或发布。
11. 工作树可能有他人改动。编辑前重读目标文件；不得覆盖或回滚无关改动。
12. 环境失败不等于项目通过。记录阻塞、原始命令、退出码和仍能完成的静态检查；被阻塞 AC 保持未勾选并标 `U`。
13. 修复后立即回写本文进度板和证据，不得在会话末凭记忆补写。
14. P0 顺序优先于 P1，P1 优先于 P2。只有明确的依赖修复可以前置，不得用平台重构绕过当前日用阻断项。

## 1. 审计快照（2026-08-04，仅为复核线索）

- 环境：Windows amd64；Go `1.26.4`，项目 `go.mod` 为 `1.25.0`；Node `24.18.0`；`gopls` 可用。
- 项目锁定 Wails `v3.0.0-alpha2.111`，本机 `wails3` 为 `alpha2.117`。版本不一致时生成物不能作为合格证据。
- 当前工作区没有可核验的 `.git` 元数据，无法核验 tracked 状态、历史、tag、release、CI 运行或代码来源；相关结论全部为 `U`。
- 最近一次前端门禁：161 files / 2612 tests、`vue-tsc`、ESLint、Vite build 通过，仅达到 `T`/构建级证据。
- `go vet` 与 `go build -buildvcs=false` 通过；Windows `go test ./...` 受权限位与 Bash 选择问题影响，未形成稳定全量证据。
- `node scripts/check-bindings.mjs` 失败。诊断性的 `wails3 generate bindings -dry` 曾重建被忽略的 bindings，说明生成、忽略和校验约定不一致。
- 官方 registry 的 npm audit 有 1 个 High：`jsdom -> undici@7.28.0`。
- `gofmt -l services` 至少报告 `services/debug_service_f5f7_test.go`、`services/recovery_service.go`、`services/terminal_service_test.go`。
- 下列文件发现替换字符 `�`，必须逐处恢复语义而非全局删除：`services/ai_service.go`、`services/terminal_service.go`、`frontend/src/views/EditorView.vue`、`frontend/src/components/marketplace/MarketplacePanel.vue`、`frontend/src/components/settings/ai/PlanSection.vue`、`frontend/src/components/ai-assistant/InputComposer.vue`、`frontend/src/components/settings/ai/ComputerUseSection.vue`、`GoalSection.vue`、`McpSection.vue`。
- `docs/THIRD_PARTY_LICENSES.md` 尚有约 14 个 `UNRESOLVED`；数量须以执行时扫描为准。
- 旧 `prompt-8` 的 G19/G20 只是协议蓝图与 future-only 策略。禁止把 `docs/HOST-CLIENT-PROTOCOL.md`、`docs/LANGUAGE-PACK-SDK.md`、`docs/EXTENSION-CONTRIBUTION-PROTOCOL.md` 当作运行时实现。

## 2. 目标总览与依赖顺序

| 顺序 | Goal | 优先级 | 主题 | 最低完成证据 | 初始状态 |
|---:|---|---|---|---|---|
| 1 | P9-G01 | P0 | Wails bindings SSOT | T/I | 未开始 |
| 2 | P9-G02 | P0 | HTTP Client 生产绑定 | I/P | 未开始 |
| 3 | P9-G03 | P0 | 空 workspace 全链路拒绝 | T/I | 未开始 |
| 4 | P9-G04 | P0 | Recovery 与保存互锁 | P | 未开始 |
| 5 | P9-G05 | P0 | AI 窗 WorkspaceContext 唯一权威 | I/P | 未开始 |
| 6 | P9-G06 | P0 | AI 窗 runtime role 隔离 | I/P | 未开始 |
| 7 | P9-G07 | P0 | Windows Go/contract 门禁 | T/I | 未开始 |
| 8 | P9-G08 | P0 | 版本与平台元数据 SSOT | T/P | 未开始 |
| 9 | P9-G09 | P0 | macOS release 可移植性 | T/R | 未开始 |
| 10 | P9-G10 | P0 | packaged 日用基础闭环 | P | 未开始 |
| 11 | P9-G11 | P1 | Settings 双窗口并发一致性 | I/P | 未开始 |
| 12 | P9-G12 | P1 | AI 请求上下文与 Plan 真接线 | I/P | 未开始 |
| 13 | P9-G13 | P1 | Extension API 去假成功 | I/P | 未开始 |
| 14 | P9-G14 | P1 | Debug 真实 DAP 映射 | I/P | 未开始 |
| 15 | P9-G15 | P1 | Test Explorer 多语言 runner | I/P | 未开始 |
| 16 | P9-G16 | P1 | Terminal 配置与退出协议 | I/P | 未开始 |
| 17 | P9-G17 | P1 | Git workspace/worktree roots | I/P | 未开始 |
| 18 | P9-G18 | P1 | AI Diff 提交后 UI 恢复 | T/P | 未开始 |
| 19 | P9-G19 | P1 | npm 供应链与可复现安装 | T/R | 未开始 |
| 20 | P9-G20 | P1 | VSIX 解压、签名与权限 | T/I | 未开始 |
| 21 | P9-G21 | P1 | 许可证、SBOM、签名与 provenance | R | 未开始 |
| 22 | P9-G22 | P1 | 文档、编码、格式与死功能清理 | T/P | 未开始 |
| 23 | P9-G23 | P2 | Language Pack Runtime/SDK | I/P | 阻塞 |
| 24 | P9-G24 | P2 | 独立 Extension Host | I/P | 完成 |
| 25 | P9-G25 | P2 | 动态国际化与个性化 | I/P | 进行中 |
| 26 | P9-G26 | P2 | Unified Remote Workspace Host | I/P | 未开始 |
| 27 | P9-G27 | P2 | 发布运营、SLO 与外部审计 | R | 未开始 |

核心依赖：`G01 -> G02/G05/G06/G10`；`G03 -> G04/G10`；`G08 -> G09/G10/G21`；`G10 -> 全部 P1`；`G13/G20 -> G24`；`G14/G15/G16 -> G23/G26`；`G19/G20/G21/G22 -> 开源发布`；`G23/G24 -> G25/G26`；`G10-G26 -> G27`。

---

## GOAL P9-G01（P0）：建立 Wails bindings 唯一事实源

**现状与证据（2026-08-05 终审）：** 缺口已变化。锁定版本生成、完整 manifest、drift gate 与真实 Windows Wails WebView 调用已实现；锁定的 `alpha2.111` runtime 不遵守 `//wails:ignore` 权限假设，因此识别出的非 renderer 内部方法已改为非导出。终审发现 `SettingsService.GetSecret/StoreSecret/DeleteSecret` 让 renderer 可直接指定 keyring account，现已全部改为包内方法，并将历史三项及其自然导出名 `GetExtensionSecret/StoreExtensionSecret/DeleteExtensionSecret` 一并加入永久禁止表；扩展 `secrets` API 在权限检查后明确 fail-closed，直到可信独立 host/后端调用者身份存在。终审还补上了不可解密主密钥时 provider key 脱敏，以及无关设置保存时不可解密旧密钥的拒绝与逐字节保留；已有磁盘加密数据不迁移、不删除。cross 镜像入口固定并校验 Node `22.14.0`、拒绝 Linux CGO 宿主/目标架构不一致，WSL 脚本也不再硬编码 checkout 路径。生产注册面测试和真实探针同时拒绝旧 root setter 与六个 raw-secret FQN。前后端代码、格式与构建门禁现已通过，但官方 npm audit 仍有 1 个 High；`.git` 仍为空目录，实际 tracked/untracked 归属不可核验，故本 Goal 不得标完成。

**范围：** 锁定 CLI、生成命令、FQN/ID、导出白名单、`.gitignore`、CI drift gate。**不做：** 手修生成文件来绕过检查。

### 执行点
1. 从 `go.mod`/Wails 配置推导唯一版本，提供跨平台可复现的生成入口并拒绝版本不符。
2. 选择并记录生成物策略：tracked 则 CI 检查无 diff；untracked 则 build/test 前生成且发布包验证存在。二者不可混用。
3. 检查所有 service 导出，纠正 slash/FQN/包名，保留敏感 setter 的禁止列表。
4. 校验脚本在缺失、陈旧、多余、手工篡改和错误版本时均失败。

### 必须覆盖的失败路径
- CLI 缺失/版本不符、空 bindings、错误 FQN、敏感方法意外导出、生成后 dirty。

### AC
- [x] 项目锁定版本可一键生成 bindings，版本不符明确失败。`T`：`node scripts/generate-bindings.mjs` exit 0；不存在的 `WAILS3_BIN` 与本机 `alpha2.117` 分别 exit 1；`node --test scripts/wails-bindings.test.mjs` 16/16。
- [x] `check-bindings.mjs` 在干净生成物上通过，在注入 drift 后失败。`T`：54 files / 46 service modules / production `ByName=0`；真实生成树 drift 注入 exit 1 后重新生成恢复 exit 0。
- [ ] `.gitignore`、构建、CI 与生成物归属一致。`S/T`：manifest 与 `.gitignore` 均声明 `untracked-generate-before-use`；新增 `check-bindings-ownership.mjs`，CI、package 和 release 均在生成前执行真实 `git ls-files -- frontend/bindings`；构建/CI 入口契约 16/16。`Dockerfile.cross` 现从每次挂载的源码重新生成 bindings、`npm ci`、重建并校验 `dist`，固定 Node `22.14.0` 并校验官方 x64/arm64 SHA-256，Linux CGO 目标与容器架构不一致时在构建前 exit 1；WSL cross 脚本从自身位置解析仓库根并允许 `KOYORI_IDE_SOURCE_DIR` 显式覆盖。[2026-08-06 事故恢复重建：本条原文中段约 432 字符（旧 cross 镜像与新 cross 镜像的检查细节）在误替换事故中丢失，无备份可恢复；以下事实依据同章节「门禁与阻塞证据」段落与 `build/e2e-evidence/p9-g01/runtime-bindings.json` 重建，未添加新结论] 2026-08-05 终审重跑证据 JSON SHA-256 `9A63CDC923DA76870CDEEE35955AD9F424280F5F1FEE43882A1EFEE9F9C77BA3`，manifest SHA-256 `9436632DD3499892F726CFB945342F949D5370E27A6CD21CF16E1446A7F870C2`，探针可执行文件 SHA-256 `8CA2CC2A34896A237502E978D0538D752130B8154498D6AB5F457BC8010C1FD5`；旧 `FileService.SetWorkspaceRoot`，历史 `SettingsService.GetSecret/StoreSecret/DeleteSecret` 及自然导出名 `GetExtensionSecret/StoreExtensionSecret/DeleteExtensionSecret` FQN 均为 unknown，root 与磁盘内容未被越权改变。
**最低证据：** `T` + `I`。**回滚要求：** 不扩大 renderer 权限，不恢复 root setter。

## GOAL P9-G02（P0）：修复 HTTP Client 生产动态绑定加载

**现状与证据（2026-08-05 终审）：** 缺口已不再存在。`frontend/src/stores/httpClient.ts` 已删除变量字符串和 `@vite-ignore` 路径，静态导入锁定版本生成的 `httpclientservice.js`，默认 backend 直接调用全部 7 个生成 binding，并正规化 Wails nullable map/array 与 Go 零值 options。新增生成面与 production chunk 永久门禁、真实 loopback Go 集成测试，以及仅在 `e2e`/`VITE_KOYORI_IDE_E2E_HTTP_CLIENT=1` 下启用的 renderer 探针；普通 production bundle 不含探针标识。真实 Wails dev 与 packaged Windows 产物均已通过公网、私网批准/拒绝、redirect、超时、取消、历史清理矩阵。G01 仍为阻塞 3/4；本 Goal 依用户明确指示完成，不把 G01 的 `.git` ownership 阻塞改写为完成。

**范围：** HTTP Client 从面板到真实 Wails service 的加载、请求、错误与授权。**不做：** 用 fetch 绕过后端私网策略。

### 执行点
1. 消除测试与生产 import 路径差异，静态验证生成 binding 导出形状。
2. packaged 环境执行公网、本机测试服务、拒绝私网、批准私网和 redirect 重新校验。
3. 展示可诊断错误，不把 binding load failure 显示为普通网络失败。

### AC
- [x] dev 与 packaged 均能调用真实 HTTPClientService。`I/P`：`build/e2e-evidence/p9-g02/dev-runtime.json` 与 `packaged-runtime.json` 均为 `status=passed`；传输链为 renderer -> 生成 Wails binding -> `HTTPClientService`。dev 产物 SHA-256 `9aaaeb514f80dfe4422b64666322383983ec6fdd630b2f055d5bf27ebdce98f5`，packaged 产物 SHA-256 `4cf07f2e32d552322483feb93ff120c0ebd37c9e856d25ada1bec41910c92849`。
- [x] 私网缺 token、拒绝、过期、重放、跨 origin redirect 全部 fail-closed。`T/I/P`：dev 与 packaged 的五条拒绝路径均 `passed=true`；缺 token、拒绝和过期均未触网，重放在单次消费后拒绝，跨 origin private redirect 在 secondary server 前拒绝，两个运行的 `secondaryRequestCount=0`。批准后的同 origin redirect 到达并返回 202。
- [x] 响应状态、headers、body、超时和取消在 UI 可用。`T/I/P`：store 默认 backend 通过 7 个生成 binding；真实批准 POST 返回 201、JSON body、headers、duration 和 request ID，`Set-Cookie` 记录为 `[REDACTED]`；真实取消与超时分别返回 `context canceled`/`context deadline exceeded` 且服务端连接提前关闭；history 为 6 项并可通过 binding 清空。
- [x] packaged 证据含真实本机 HTTP 服务日志。`P`：`build/e2e-evidence/p9-g02/packaged-http-server.jsonl` SHA-256 `AA0EB65C2BDAEBD3828B54D8A6A53D57C25968BC22E24198B81D749A06AB1422`，`packaged-application.log` SHA-256 `D4E1266101C7417FB9995075133BEEB6B92E9B2B3B3357D91CBADB215C51D2DE`，`packaged-build.log` SHA-256 `43881B1CED4D78B55B0EBB27108B047C4B58B390230D1D190F6A39BCE092F83E`；原始日志记录 primary 6 次真实请求、secondary 0 次请求。

**门禁、红灯与限制证据：** `node --test scripts/http-client-bindings.test.mjs` 2/2、`node --test scripts/wails-bindings.test.mjs` 16/16、`node scripts/check-bindings.mjs`、`go build -buildvcs=false ./...`、`gofmt -l .`、`go vet ./...`、`go test -tags e2e ./internal/e2e ./services -count=1`、前端 161 files / 2617 tests、ESLint、`vue-tsc --noEmit` 与 production build/postbuild 均 exit 0；postbuild 证明 7 个生成调用入包且 unresolved path=0。`go test ./... -count=1` 首次因 `TestDebugService_ConnectMockDAPMarksAttachRunning` 在 Windows 出现 `wsasend: An established connection was aborted by the software in your host machine` 而 exit 1；该测试随后单独连续 3 次通过，第二次全量重跑 exit 0，仍保留为一次性 Debug socket 红灯，不宣称其已形成长期稳定性证据。开发过程还真实经历并修复：binding test 首次 1/2（无静态 import）、store test 首次 1/7（nullable map 未正规化）、Wails CLI 临时构建缺间接 `go.sum`、dev 首次 120 秒握手超时、e2e typed event 生成漂移、临时 lock 清理失败、e2e HTTP 15 秒 `WriteTimeout`、公网 403 被误判为 binding 失败；最终 dev 与 packaged 矩阵均通过。本次终审直接 `npm test` 又因 PowerShell 禁止 `npm.ps1` 而未启动测试，改用 `npm.cmd test` 后 2617/2617 通过。`npm audit --registry=https://registry.npmjs.org --audit-level=high` 仍因 `undici` 1 个 High exit 1，归属 G19，当前不得发布。工作区 `.git` 为空，两个运行均明确记录 `gitMetadataAvailable=false`，未伪造 commit，改记共同 source fingerprint `70b18b3cc680e87c14237dd80d053a4eb39e22eb98b93b80ec66990f35706097`；dev/runtime JSON SHA-256 分别为 `7A95DA73A8C628AE95D4D88C74DF58F5B3EB5067888FD9EF34F4539AF520B46A` 与 `0C29A883FBB09780209648357720177BC03E0C075BA7D4B98892E8F9C923B95D`。

**最低证据：** `I` + `P`。**回滚要求：** 保留后端签发的私网 capability。

## GOAL P9-G03（P0）：Search/MCP 等能力在空 workspace 下全链路 fail-closed

**现状与证据（2026-08-05 终审）：** 缺口已不再存在于本 Goal 范围。production Search、MCP、LSP、Symbol Index、File 和 Window 外部打开入口现均绑定共享 `WorkspaceContext`/generation；空 root 在读取、索引、server 探测/连接和外部进程启动前拒绝。短时外部启动通过 `workspaceLease.withCurrent` 在共享读锁内完成“最终校验 + Start”，workspace 切换不能插入 TOCTOU 窗口；长时 Search/Symbol/MCP 请求在遍历、发布或返回前复核 generation。MCP tool approval 继续绑定 root/lifecycle generation，且原 approval lease 原子选取 client，切换后即使新 workspace 重连同名 server，旧 token 也不能向新 client 发请求。`WorkspaceContext` 使用 `os.SameFile` 识别 Windows junction 身份，大小写和普通/extended UNC 拼写按 Windows 语义规范化，跨 share/本地盘不合并。File renderer 实例改为共享 context constructor，Go 内部 legacy/headless constructor 的兼容路径不进入 `main.go` renderer 注册。AI 子窗口现在先调用 `ProjectService` 两阶段入口，成功后才发布 `appState`/snapshot；失败保留旧 workspace、显示错误并解除 switching/disabled 状态。

**范围：** Search、MCP、文件、Agent、命令、Git、LSP、DAP、Test、Terminal 的 root 获取与 generation。**不做：** 以进程 cwd 作为隐式 workspace。

### 执行点
1. 盘点所有工作区入口，只允许从共享 WorkspaceContext 读取 root/generation。
2. workspace 清空或切换时取消旧 generation 的异步任务，清除缓存与 watch。
3. 为路径逃逸、symlink/junction、TOCTOU 和旧 generation 增加失败测试。

### AC
- [x] 空 workspace 下所有工作区能力拒绝且无磁盘/进程/网络副作用。`T/I`：`node scripts/g03-workspace-evidence.mjs` exit 0；Search 外部 `secret.txt` 不可读，MCP 空 root marker 不生成，Symbol Index 返回 `ErrNotAllowed`，LSP server map 保持空，File Reveal 与 Window Explorer/VS Code launcher 调用数均为 0；同一矩阵中的真实 stdio MCP helper PID `3988` 只在有效 workspace 下完成 initialize/tools-list，并在切换时被 reaped。
- [x] 切换后旧 Search/MCP/Agent 回调不能写入新 workspace。`T/I`：`TestG03SearchRejectsResultsAfterWorkspaceSwitch`、`TestG03SymbolIndexRejectsPublishAfterWorkspaceSwitch`、`TestG03MCPRejectsResultsAfterWorkspaceSwitch`、`TestG03MCPApprovalLeaseCannotSelectSameNamedClientAfterSwitch` 与既有 Agent generation/token 回归通过；旧 Search/Symbol 结果不发布，旧 MCP response/approval 被拒绝，同名新 client 的 send count 保持 0，真实 MCP 旧连接在 generation 切换后回收。
- [x] Windows junction、大小写与 UNC 边界有测试。`T`：`TestG03WindowsWorkspaceCaseVariantKeepsGeneration`、`TestG03WindowsWorkspaceJunctionUsesTargetIdentity`、`TestG03WindowsUNCIdentityBoundaries` 全部通过；junction/target 去重为一个 root，大小写别名不增 generation，跨 UNC share 与本地盘身份不合并。
- [x] UI 给出可行动错误，不显示成功或永久 loading。`T`：`npm.cmd test -- --run src/views/AiWindowView.test.ts` 为 1 file / 5 tests；后端 promise 提交前 renderer 保留旧 root 且 workspace 控件 disabled，成功后才发布；拒绝时 `notifyError("projects.openProjectFailed")`、旧项目状态不变且 disabled 被解除。

**证据与门禁：** `build/e2e-evidence/p9-g03/g03-workspace-evidence.json` SHA-256 `b79836a5ccdf0caa53495031c3dfb586b36347e6c402a4b263ebe22ec9450e46`，原始日志 SHA-256 `d7b9d8e599d2b3c497ff25324e65d0e78f5bae2225cfd2fa5ad45a47475abef1`，source fingerprint `b44bb687ed8d56d686d0f1adca7d6c03fe35b55eaa64bc036c7e50c7dff5ea45`，missing tests 为 0。首次红灯保存在 `build/e2e-evidence/p9-g03/g03-initial-red.json`（SHA-256 `034ecb94c96b33a84f2624ee9e25a2b7e23d090c5fecce4b90fd030a3623a2c6`）：修复前 Search 读到外部 secret、MCP 启动子进程后才失败、Symbol Index 空成功；新增 junction 测试首次暴露 generation `1 -> 2`；一次 File launcher 测试 seam 签名错误导致编译失败；首次全量还暴露 legacy/headless constructor 兼容回归；一次前端定向命令因在 `frontend` cwd 重复传入 `frontend/src/...` 而报告 No test files，均已纠正并回归。最终 `go test ./... -count=1`、`go test -race ./services -run '^TestG03' -count=1`、`gofmt -l .`、`go vet ./...`、`go build -buildvcs=false ./...` 均 exit 0；前端 161 files / 2619 tests、`vue-tsc`、ESLint、production build 与 HTTP postbuild 均 exit 0；bindings generate/check、文档链接/数字、Wails pin、contract smoke 均 exit 0。`npm audit --audit-level=high` 仍因 `undici` 1 High 而 exit 1，归属 G19，当前仍不得发布；Vite 仍有第三方 annotation、chunk size 和 ineffective dynamic import 警告。`packaged-e2e --dry-run` 仅证明 source plan，真实 packaged 执行仍为 `U`，不冒充本 Goal 的 `P` 证据；本 Goal 最低证据为 `T/I`，已满足。

**最低证据：** `T` + 至少一个真实子进程 `I`。**回滚要求：** 不引入 renderer root setter。

## GOAL P9-G04（P0）：Recovery pending 阻止自动保存和失焦保存

**现状与证据（2026-08-06 终审）：** 缺口已不存在于本 Goal 范围。后端以 workspace generation 为边界维护 `scanning/pending/resolved/failed`，未 resolved 时拒绝自动保存、blur/close、workspace switch、journal 禁用及所有可删除/覆盖 pending 快照的入口；新窗口编辑仍写入独立 journal。前端按每次 workspace 切换重新扫描，要求逐记录恢复/保留磁盘决定，支持提交前撤销与扫描失败显式确认。真实 Windows packaged 产物已完成两次强制终止、三次启动、再次崩溃、双快照恢复、撤销、手动保存及干净重扫。

**范围：** 启动扫描、恢复选择、autosave、blur save、close、workspace switch。**不做：** 仅增加提示而不阻止写盘。

### 执行点
1. 建立 `scanning/pending/resolved/failed` 状态机；只有 resolved 才开放自动写盘。
2. pending 期间允许编辑但记录独立 buffer；恢复/放弃/合并必须显式且可撤销。
3. 用 packaged 进程 kill/restart 演练验证实际恢复文件。

### AC
- [x] pending 时 autosave、blur、close hook 均不覆盖磁盘或快照。`T/P`：最终 packaged 运行中 autosave 与 blur 后磁盘逐字节保持初始内容；titlebar close、workspace switch、clear record/window/workspace、disable journal、discard session 共 7 条旁路均返回拒绝；原始快照前后 SHA-256 同为 `7b0c96f8545c6fb38cc0ea006c412d7fd072e5eba2676e4d15b89775475f85b2`。原生 `WindowClosing` hook 确认被调用且进程仍存活。
- [x] 恢复、放弃、冲突、扫描失败、再次崩溃路径均有测试。`T/P`：`go test -race ./services -run '^TestG04' -count=1` exit 0，8 个 G04 状态机/失败保留/cleanup bypass/重复 journal 测试通过；前端全量 162 files / 2630 tests 覆盖 restore、keep-disk、clean/conflict/missing、undo、scan failure acknowledgement、commit failure 与 clean rescan。packaged 第二次崩溃使 journal `1 -> 2`，第三次启动恢复 2 条。
- [x] 真实 packaged SIGKILL/TerminateProcess 后内容可恢复。`P`：锁定 Wails `v3.0.0-alpha2.111` 的 Windows amd64 产物启动 3 次，`taskkill /T /F` 两次均 exit 0；artifact SHA-256 `455299c54001105f6aa7f00df5965f429052d3e825e9e0d4fab2919ec0e18b37`，source fingerprint `020833b1e3fd069c0ebb4f6d12967473c2cea3a0b15ddb8e01b0ac901dfe658c`，`build/e2e-evidence/p9-g04/recovery-packaged-runtime.json` SHA-256 `6d395a84c3a222c0dbcda27b8f428ff9323c379b73af6f48ac4d673840d0a70c`。
- [x] 恢复完成后正常保存且无重复弹窗。`T/P`：最终 renderer 结果为 `recoveredCount=2`、`undoVerified=true`、`manualSave=true`、Monaco 与磁盘一致、`rescanCount=0`、`dialogVisible=false`，最终 journal 数为 0；`recovery-pending.png` 显示待恢复对话框，`recovery-resolved.png` 无对话框。

**证据、门禁、红灯与限制：** 最终 P 证据于 `2026-08-06T02:52:54.290Z` 完成；独立复核 103 项、0 不一致，覆盖 36 个源码文件、复合指纹、Wails CLI、artifact、两份构建日志、三份启动日志、三张截图及关键状态断言。三张 PNG SHA-256 依次为 `7d1e66e2069a56c82835c26bb566ae9ad044487913bc165fb653ee632fbe9d1f`、`3df943ebe38dfbbeafb1429af6cacb5e1e11a901e65a9cc9738f511235ea6b19`、`826740ad6efcd36206f5e2f65c0b3242a04563acee378985fe770b56ef0a32f7`，均为 1000x618 非空原生窗口截图并已目视复核。`go test ./... -count=1` 首次终审因两个旧 RecoveryService 测试在 `scanning/pending` 时直接清理而失败；保留 fail-closed 产品行为，修正测试为先完成启动扫描/显式恢复提交，定向回归与第二次全量（services `143.841s`）均 exit 0。`go test -race ./services -run '^TestG04' -count=1`、`gofmt -l .`、`go vet ./...`、`go build -buildvcs=false ./...`、带/不带 `e2e` 的 internal E2E 均 exit 0。干净 `npm ci` 安装 376 packages 后，前端 2630/2630、`vue-tsc`、ESLint、production build/postbuild 均 exit 0；bindings 生成/检查、16/16 binding contract、文档链接/数字、Wails pin、contract smoke 1/1、packaged driver 3/3 与 packaged dry-run 均通过。一次独立复核脚本因误认为 evidence fixture 含原文而 exit 1，按实际 schema 修正后为 103/103；额外尝试的 `node scripts/check-contracts.mjs` 因仓库不存在该文件而 exit 1，仓库实际合同入口 `contract-smoke.mjs` 已通过。两份 harness failure 与八份 packaged failure 证据及日志均保留。官方 `npm audit --registry=https://registry.npmjs.org --audit-level=high` 仍因 `undici` 1 High exit 1，归属 G19，统一发布门禁仍失败；build 警告与 npm `minimum-release-age`/`allow-scripts` 警告未隐藏。`.git` 为空目录，证据明确 `gitMetadataAvailable=false`，没有 commit/tag/CI/release 声明。

**最低证据：** `P`。**回滚要求：** 恢复失败时保留原始快照，不自动清理。

## GOAL P9-G05（P0）：AI 窗工作区切换使用唯一 WorkspaceContext 权威

**现状与证据（2026-08-06 G05 P 复核）：** 后端 `WorkspaceContext` 已成为主窗、AI 窗和设置窗共享的唯一写入入口。`ProjectService` 以校验、停止旧任务、切换服务、持久化和 `workspace:changed` 快照广播组成事务；失败会回滚旧 root/roots/generation。AI 窗和项目删除路径已改为重新读取后端快照，不再直接写入 renderer workspace 真相源。T/I 定向证据仍由 `node scripts/g05-workspace-evidence.mjs` 生成；新增 pinned Wails packaged 驱动 `node scripts/g05-packaged-e2e.mjs` 实际启动 `bin/koyori-ide.exe`，完成 workspace-a → workspace-b 切换后主窗与 AI 窗的真实 Search、AI project preset、Terminal 和 `WorkspaceSnapshot` 调用。通过报告为 `build/e2e-evidence/p9-g05/g05-packaged-runtime.json`，artifact SHA-256 `cdcda443790bcfa2db002622958c269a7dd09ffabb224087b0ffec46af101561`，source fingerprint `0895196ca7c45f1ab8b8b01a3138eac4594c0c429f8727ffb952ee36adce336e`，主窗/AI 窗截图为 `g05-packaged-main.png` / `g05-packaged-ai.png`（采样颜色 35/47）。锁定 Wails CLI `v3.0.0-alpha2.111`，CLI SHA-256 `6418943dd870472e7a4ff5f095d15f0908bdef497301603477a2bc6b1fd3ede6`；`.git` 为空，证据只记录 fingerprint，不声称 commit。首次运行真实暴露并修复了 recovery scanning 门禁等待和截图脚本 `$PID` 冲突，失败日志仍保留于同一 evidence 目录。
**2026-08-06 复核刷新：** 本会话自审发现 G06 的 role token 曾绑定 workspace generation，导致带持久化工作区启动/G05 packaged 流程将主窗降级为 `minimal`（G05 首次重跑真实复现 `recovery scan did not settle`）。修复 role token 与 generation 解耦后（见 G06），G05 T/I 证据 `node scripts/g05-workspace-evidence.mjs` 与 P 证据 `node scripts/g05-packaged-e2e.mjs` 已在当前代码上重跑通过：新报告 `g05-workspace-evidence.json` SHA-256 `99693ac9dd6d4955a6d0aa4043da56a3a8ecab20ac30ced849b0ef32260acd88`、`g05-packaged-runtime.json` status=`passed`，source fingerprint `6e47b6dcc94344ee17c7c100fd1c815fec6520e5efb7e7a4cee60d83e3336cb4`，artifact SHA-256 `95c6503ef40a2668dfbdd285d3610ad86a3b7744311b515bb6937edc348cc078`，截图为 `g05-packaged-main.png`（SHA-256 `cb091f6986fc41c844c4dbcddb3fe045a388fbcf84c59d8c96ab0eddb5752cbe`）/`g05-packaged-ai.png`（SHA-256 `8d7f6aed4a50f7c0d6ae664f4f737b3d0244c2f81284d347d6d5e51e42ebe2f1`）。首次重跑失败日志仍保留于同一 evidence 目录（`g05-packaged-runtime.failure.json`）。

**范围：** 主窗、AI 窗、设置窗的打开/清空/切换/重连。**不做：** 每窗口维护独立真相源。

### 执行点
1. 后端 WorkspaceContext 成为唯一写入入口，窗口只订阅带 generation 的快照。
2. 定义切换事务：校验、停止旧任务、切换服务、持久化、广播；失败整体回滚。
3. 多窗口并发切换使用 CAS/序列号解决，过期事件被丢弃。

### AC
- [x] 三类窗口观察到同一 root、roots 和 generation。`T/I`：`TestG05WorkspaceSnapshotPublishesRootRootsAndGeneration`、`TestG05WindowReopenReadsSameSnapshotAndClearRollsBackOnFailure`，以及 `frontend/src/views/AiWindowView.test.ts`、`frontend/src/stores/workspaceStore.test.ts`、`frontend/src/stores/app.test.ts` 定向测试通过。
- [x] AI 窗触发切换不调用任何 service root setter。`T/I`：`TestWorkspaceContextSettersAreHiddenFromRenderer` 与源码导出面检查通过。
- [x] 两窗口竞争、后端失败、窗口重开和 workspace 清空有测试。`T/I`：`TestG05ConcurrentWorkspaceSwitchesSerializeAndLatestSnapshotWins`、`TestG05WorkspaceSwitchFailureRollsBackAndDoesNotBroadcast`、`TestG05WindowReopenReadsSameSnapshotAndClearRollsBackOnFailure` 通过。
- [x] packaged 多窗口切换后 Search/AI/Terminal 均作用于同一 workspace。`P`：`node scripts/g05-packaged-e2e.mjs` exit 0；`g05-packaged-runtime.json` status=`passed`，generation 1→2，主窗和 AI 窗 renderer 均报告 workspace-b，Search 各 2 个匹配、AI preset 命中，主窗 Terminal 输出命中；真实 artifact 日志与两张窗口截图已归档。

**证据与门禁：** T/I 报告为 `build/e2e-evidence/p9-g05/g05-workspace-evidence.json`（status=`service-and-renderer-verified`），日志为 `g05-service-test.log`、`g05-frontend-test.log`；P 报告为 `g05-packaged-runtime.json`，构建/应用日志为 `g05-packaged-build.log`、`g05-packaged-launch-1.log`，截图为 `g05-packaged-main.png`、`g05-packaged-ai.png`。`node scripts/g05-packaged-e2e.mjs` 同时验证 production bundle 不含 probe marker、锁定 Wails 版本、artifact SHA-256、真实 WebView bindings 和非空窗口截图。`.git` 为空目录，不能声称 commit/追踪归属；不得伪造截图、日志或 commit。**最低证据：** `I` + `P`。**回滚要求：** 切换失败恢复全部旧服务状态。

## GOAL P9-G06（P0）：按 runtime role 隔离 AI 窗，避免重复启动完整 IDE

**现状与证据（2026-08-06 终审）：** 缺口已不再存在于本 Goal 范围。可信 role 由后端签发：`WindowService.issueRuntimeRoleToken` 生成 256-bit 单次消费 token 并嵌入窗口 URL（`koyori-ide_runtime_role` 查询参数），renderer 只能通过 `ResolveRuntimeRole` 消费一次；伪造/重放/过期一律解析为 `minimal` 并计数拒绝。主窗与 AI 窗 URL 分别以 `RuntimeRoleMain`/`RuntimeRoleAI` 签发（`services/runtime_role.go`、`main.go createMainWindow`、`services/window_service.go createAIWindow`）。前端在挂载前解析 role，`bootstrapFrontendRuntime` 按 role 声明生命周期：`ai`/`minimal` 只跑 themes/cross-window-sync/settings/personalization，跳过 debug-runtime、test-explorer-runtime、connectivity、lsp、plugin-sandbox、plugins、layout、workflows；主窗单例（文件 watch、extension host、恢复扫描、更新检查、快捷键）因此只有一个 owner。`App.vue` 的恢复扫描与 eager 激活同样以 full-IDE role 门控。后端服务图为进程级单例（`bootstrap_services.go`），AI 窗是同一进程内的第二个 WebView，不重复实例化任何后端服务。窗口关闭泄漏由 `aiWindowsCreated/Closed` 计数与真实进程树验证。

自审中发现并修复的真实回归：role token 原先绑定 workspace generation，而主窗 token 在启动恢复持久化工作区之前签发，导致任何带持久化工作区的启动（以及 G05 packaged 流程）都会把主窗降级为 `minimal`——恢复扫描、plugins、layout、workflows 全部缺失。已将 role token 与 generation 解耦：role token 是窗口的一次性 bootstrap 凭证而非工作区能力，工作区授权仍由 `WorkspaceContext` 的 generation 检查独立强制（G03/G05）；伪造/重放/过期仍 fail-closed。对应单测改为 `TestRuntimeRoleTokenExpiryAndWorkspaceSwitchValidity`，并在 G05 打包流程上复验通过。

**范围：** window role 判定、bootstrap 服务图、事件订阅/释放。**不做：** 用 UI 隐藏替代后端隔离。

### 执行点
1. 在可信启动参数中定义 main/ai/settings/e2e role，renderer 不得自我提权。
2. 每个 role 显式声明需要的服务和生命周期；AI 窗不启动文件 watch、extension host、recovery、更新检查等主窗单例。
3. 增加实例计数和窗口关闭泄漏验证。

### AC
- [x] AI 窗启动日志证明未重复初始化完整 IDE。`T/P`：`g06-packaged-launch.log` 记录 `runtime role resolved role=ai` 两次；renderer bootstrap trace 只含 role-resolved/themes/cross-window-sync/settings/personalization/minimal-role-complete；`frontend/src/main.ts` role 分支与 `frontend/src/runtimeRole.test.ts`、`frontend/src/main.test.ts`、`services/runtime_role_test.go` 通过。
- [x] 主窗单例服务在多窗口下只有一个 owner。`T/P`：前端 `activeRuntimeOwner` symbol 生命周期（`acquireFrontendRuntime`/`disposeFrontendRuntime`）保证同窗口单 owner，主窗专属阶段只在 full-IDE role 执行；后端服务进程级单例；packaged 统计 resolvedMain=1/resolvedAI=2，进程树仅 1 个 `koyori-ide.exe`。
- [x] 伪造 role、窗口重开、主窗先关闭路径安全。`T/P`：`TestRuntimeRoleTokenSingleUseAndForgeryFailClosed`、`TestRuntimeRoleTokenExpiryAndWorkspaceSwitchValidity`、`TestWindowService_MainWindowCloseClosesAIWindowFirst` 通过；packaged 探针伪造 token 全部拒绝（rejected=3），AI 窗重开（aiWindowsCreated=2/aiWindowsClosed=1）后 role 仍为 ai；主窗先关闭由 `main.go registerMainWindowEvents` 先关 AI 窗并由单测覆盖。
- [x] packaged 多窗口无重复通知、快捷键和后台进程。`P`：`node scripts/g06-packaged-e2e.mjs` exit 0；AI 窗 stages 不含 main-runtime-effects/plugins/workflows/layout；进程树断言恰 1 个 `koyori-ide.exe`；主窗/AI 窗截图非空且采样颜色 >20。

**证据与门禁：** T 级单测/前端测试通过；P 级报告 `build/e2e-evidence/p9-g06/g06-packaged-runtime.json` status=`passed`、evidenceLevel=`P`，source fingerprint `d8ad595663b5b2419cf7ae9c572c99ba958dd808989cd522659393d203edb114`，artifact SHA-256 `606b69120fa94b21e1e7bad321e99145887258a063dfd92758798fbdb726bc32`，构建/应用日志 `g06-packaged-build.log`、`g06-packaged-launch.log`，截图为 `g06-packaged-main.png`/`g06-packaged-ai.png`。`node scripts/g06-packaged-e2e.mjs` 同时验证 production bundle 不含 probe marker、锁定 Wails 版本、artifact SHA-256、真实 WebView bindings、进程树单实例与非空窗口截图。全量门禁：`go test ./... -count=1`、`go vet ./...`、`gofmt -l .`、`go build -buildvcs=false ./...`、`go build -tags e2e ./...`、前端 164 files / 2638 tests、`vue-tsc --noEmit`、ESLint、production build/postbuild、`node scripts/check-bindings.mjs`、`node scripts/contract-smoke.mjs` 均 exit 0。npm audit 1 High（undici）仍归 G19；`.git` 为空目录，不能声称 commit/追踪归属。**最低证据：** `I` + `P`（P 级 packaged 运行经由真实 loopback HTTP automation、真实 WebView2 子进程与真实服务，涵盖 I 级真实集成）。**回滚要求：** role 未知时使用最小权限集合。

## GOAL P9-G07（P0）：稳定 Windows 全量 Go 测试与 contract smoke

**现状与证据（2026-08-07 复核）：** [2026-08-06 事故恢复重建：本段原文在误替换事故中丢失，无备份；以下依据第 8 节进度板 P9-G07 行与本章节 AC 重建，仅陈述既有事实，未添加新结论] 缺口已变化。Windows 原生 `go test ./... -count=1` 已连续通过（最终代码态 4 次 exit 0，无间歇红灯）；`go vet ./...` exit 0、`gofmt -l .` 空、`node scripts/contract-smoke.mjs` exit 0（1/1）、一键门禁 `node scripts/backend-gate.mjs` 9 步 PASS 且 exit 0；0 个 `[no tests to run]` 被计作安全 race 通过。CI `go-test` 矩阵现由 `TestGoTestWorkflowHasExplicitCrossPlatformRaceMatrix` 锁定为 ubuntu/windows/macos 三平台、同一 root/services/internal/repo race 包集合、显式 `-count=1`，并包含 `-tags e2e` hook。CI 同款包集合 + `-race` 已在 Windows 与 WSL2 真实 Linux（`scripts/wsl-linux-gate.sh` 可复现）通过，并修复 mock DAP 断管 flake；AC3 仍需真实 CI runner 与 macOS runner（`.git` 为空、无 CI 可执行），macOS 与真实 CI 平台差异矩阵保持 `U`，故本 Goal 保持「进行中」3/4。
**2026-08-08 当前 Windows 复核：** 首次重跑时，工作区带有 Linux 依赖树，缺少 `frontend/node_modules/.bin/{vitest,jest}.cmd` 与 `@rolldown/binding-win32-x64-msvc`，使 G15 真实 runner 和 contract smoke 失败；同时有 4 个 Go 文件未格式化。已用 `gofmt -w` 修复格式，并在 `frontend` 执行 `npm ci --include=optional --registry=https://registry.npmjs.org` 重建当前平台依赖（外部状态修复，未修改 lockfile）。随后 Windows 原生 `go test ./... -count=1`、`go vet ./...`、`gofmt -l .`、`node scripts/contract-smoke.mjs` 均 exit 0；一键门禁 `node scripts/backend-gate.mjs` 9 步 PASS（308.9s）。CI 同款 `go test -race ./services/... . ./internal/repo/... -count=1` 在 Windows 实际通过（services 242.064s、root 3.107s、internal/repo 5.762s），无 `[no tests to run]`；`TestGoTestWorkflowHasExplicitCrossPlatformRaceMatrix` 也通过。macOS 与真实 CI runner 仍未实际运行；当前无 `.git` 元数据，不能将远端 run 关联到本工作树，因此相关证据保持 `U`，本 Goal 保持「进行中」3/4。

**范围：** Windows 原生测试、shell fixture、权限语义、race 替代说明、contract smoke。**不做：** 跳过失败包保绿。

### 执行点
1. 消除测试对 Unix executable bit、隐式 bash、临时目录和 cwd 的偶然依赖。
2. shell 需求显式探测并给出 skip 原因；产品代码保持跨平台行为。
3. 统一一键门禁并输出各子命令退出码；修复 gofmt。

### AC
- [x] Windows `go test ./... -count=1` 连续三次通过。`I`：本会话最终代码态连续 4 次全量 exit 0（16:50、`[no tests to run]` 扫描 2 次、`node scripts/backend-gate.mjs` 内 1 次），无间歇红灯；单包耗时最长为 services 约 232s。
- [x] `go vet ./...`、gofmt gate、contract smoke 通过。`T/I`：`go vet ./...` exit 0；`gofmt -l .` 空；`node scripts/contract-smoke.mjs` exit 0（1/1）；`node scripts/backend-gate.mjs` 全部 9 步 PASS 且 exit 0。
- [ ] Linux 与 macOS CI 同一测试集通过或平台差异有精确矩阵。`I/U`：Windows 腿以 CI 完全相同的包集合 + `-race` 通过（services 235s、root 3.2s、internal/repo 26.6s）；Linux 腿已在 WSL2 真实 Linux 上以同一测试集 + `-race` 通过（含 `-tags e2e ./internal/e2e/...` 与 `go test ./... -count=1`，`scripts/wsl-linux-gate.sh` 可复现）；macOS 与真实 CI runner（`.git` 为空、无 CI 可执行）仍为 `U`；精确矩阵 = ci.yml `go-test` 作业三平台同命令、`contract-smoke` ubuntu 必过/win/mac 可选、N-129 包集合差异说明。G07 因此保持「进行中」。
- [x] 无 `[no tests to run]` 被计作安全 race 通过。`I`：Windows 与 Linux（WSL2）的 `go test ./... -count=1`、`go test -race ./services/... . ./internal/repo/... -count=1` 输出均 0 个 `[no tests to run]`；`scripts/backend-gate.mjs` 对 `[no tests to run]` 出现即失败；Windows 与 Linux 均已用 `-race` 跑通 CI 同款包集合，不冒充未执行的 race 覆盖。

**最低证据：** `T` + CI 上真实平台 `R`（P0 可先以 `I` 标进行中）。

## GOAL P9-G08（P0）：版本 SSOT 覆盖所有用户可见和平台元数据

**现状与证据（2026-08-06 复核；2026-08-16 发布口径校正）：** 缺口已变化。`VERSION`（0.2.0）为单一输入，`scripts/sync-release-metadata.mjs`（支持 `--check`，Taskfile `release:check`）现同步全部平台元数据：build/config.yml、frontend/package.json、build/windows/info.json（file_version/product_version + en-US 0409 字符串表）、build/windows/wails.exe.manifest（assemblyIdentity version）、build/windows/msix/app_manifest.xml（Identity Version 四段映射 `<major>.<minor>.<patch>.0`）、build/linux/nfpm/nfpm.yaml、build/darwin/Info.plist。真实修复了 Windows 文件属性空版本问题：原 info.json 用语言中性键 `0000`，Win32 `GetFileVersionInfo`/资源管理器无法匹配字符串表；改为 en-US `0409` 后，`node scripts/check-windows-versioninfo.mjs`（Win32 API 抽取验证工具）读出 FileVersion=0.2.0.0、ProductVersion=0.2.0、ProductName=Koyori IDE?CompanyName=Koyori IDE Contributors。构建时注入：main.go `//go:embed VERSION` → `services.SetAppVersion` → `UpdateService.GetCurrentVersion` 优先返回注入值（不再依赖 go install/vcs）。前端 Welcome/About 显示 `__APP_VERSION__`（vite define 读 VERSION；vitest 同步 define）。底层 SemVer 解析测试可以覆盖 prerelease/build metadata，但当前生产 release/package 合同限定为 stable-only `X.Y.Z`；prerelease/build metadata 只能进入未来独立 prerelease workflow，不属于当前稳定发布入口。MSIX 四段映射显式测试。Linux 腿：`scripts/wsl-linux-package.sh` 在 WSL2 真实 Linux 上构建 production 二进制并用 nfpm 打包真实 .deb，`dpkg-deb -f` 抽取 Version=0.2.0-1（0.2.0 + Debian revision 1），SHA-256 `12fdfc83822463b48bb5127f4ebfcab89dca62a9cf2a8701e03a2908e3557d38`；补建缺失的 `build/linux/koyori-ide.desktop`。

**范围：** `VERSION`、Go、前端、Welcome/About、Windows info/manifest/MSIX、macOS plist、Linux 包。**不做：** 运行时拼接不同版本。

**2026-08-09 当前 Windows 复核：** `wails3` CLI 与模块均为固定 `v3.0.0-alpha2.111`；`node scripts/sync-release-metadata.mjs --check`、`node scripts/check-bindings.mjs`、版本一致性 Go 测试、注入版本 Go 测试及 `frontend/src/appVersion.test.ts`（2/2）通过。使用 `scripts/build-windows-gui.ps1 -OutDir bin/production` 重建真实 production GUI artifact，前端 production build/postbuild 通过，PE Subsystem=2（GUI）；`node scripts/check-windows-versioninfo.mjs bin/production/koyori-ide.exe` 通过，抽取 `FileVersion=0.2.0.0`、`ProductVersion=0.2.0`、`ProductName=Koyori IDE`，artifact SHA-256 为 `36dc76ca58bcc0a66e9a4fd0a3fef3c3b7d184d9b94a01e261ecff28f5888774`，无 E2E probe marker。Windows 证据已重新绑定当前 Koyori IDE 工作树；macOS 环境/真实 CI 仍不可用，不能由该结果外推。

### 执行点
1. 生成或校验所有版本字段，处理 semver 与各平台格式映射。
2. build 在 drift 时失败；packaged 启动读取构建时注入版本。
3. 测试 stable-only `X.Y.Z`、非法版本及生产入口对 prerelease/build metadata 的 fail-closed 拒绝；若未来支持 prerelease，必须走独立 workflow。

### AC
- [x] 单一输入可同步全部元数据且 `--check` 无 drift。`T`：`node scripts/sync-release-metadata.mjs --check` exit 0；`TestReleaseVersionConsistency`（含 manifest/MSIX/package.json/config/CHANGELOG/SECURITY 全字段）与 `TestReleaseMetadataSyncCheck` 通过；drift 场景（manifest 0.1.0、MSIX 0.1.0.0、缺字段等）被拒。
- [x] Welcome/About、文件属性、包管理器元数据显示同一版本。`T/P`：前端 `__APP_VERSION__` = 0.2.0（`appVersion.test.ts` 2/2 + 当前 production dist）；Windows `bin/production/koyori-ide.exe` 经 Win32 API 抽取验证 FileVersion=0.2.0.0/ProductVersion=0.2.0/ProductName=Koyori IDE，SHA-256 `36dc76ca58bcc0a66e9a4fd0a3fef3c3b7d184d9b94a01e261ecff28f5888774`；Linux deb Version=0.2.0-1；`UpdateService.GetCurrentVersion` 返回嵌入 VERSION。
- [ ] Windows/macOS/Linux packaged 产物抽取验证通过。`P/I/U`：Windows 腿 P 级通过（真实 production exe + Win32 API 抽取）；Linux 腿 P 级通过（WSL2 真实 .deb 打包 + dpkg-deb 抽取）；**macOS 腿 `U`**（无 macOS 环境/CI，需外部状态），G08 保持「进行中」。
- [x] 非法或无法映射的版本 fail-closed。`T`：sync 脚本对非 SemVer VERSION 直接 throw；既有解析测试覆盖空版本、非 SemVer、无版本字段、prerelease 映射和 build metadata。生产 release/package 的权威目标合同为 stable-only `X.Y.Z`，prerelease/build metadata 仅可由未来独立 prerelease workflow 消费，不得据这些解析测试宣称稳定发布入口接受它们。

**最低证据：** `T` + `P`。

## GOAL P9-G09（P0）：修复 macOS release workflow 的 BSD 可移植性

**现状与证据（2026-08-06 复核）：** 缺口已变化。`.github/workflows/release.yml` 中全部 macOS/BSD 不兼容写法已替换：`mapfile`/`mapfile -d`（bash 4+/4.4+，macOS 默认 Bash 3.2 不支持）改为 `while IFS= read -r` + 进程替换；`find -maxdepth`/`find -printf`（GNU find）改为 glob 循环（`for candidate in bin/koyori-ide*`、`release-assets/*`、`./*`），路径含空格安全；版本提取改用无引号技巧的 `awk -F"` 写法；NUL 分隔改为换行分隔并对换行文件名 fail-closed。受影响步骤：Verify tag matches all release metadata（build job，macOS runner 上运行）、Package (Linux/macOS)、Consolidate、Generate unsigned provenance、Finalize release checksums。新增 `internal/repo/release_portability_test.go` 静态门禁（禁止 mapfile/readarray/declare -A/${var^^}/-maxdepth/-printf/sort -z；要求 while-read 与 glob 确定性选择逻辑存在）；`TestReleaseWorkflowBashStepsHaveValidSyntax`（bash -n 逐 step）与 `TestReleaseWorkflowRequiresSBOMProvenanceAndFinalChecksums` 同步更新并通过；WSL2 真实 Linux bash 上验证「单候选/含空格双候选/零候选/单 app bundle」均按预期 fail-closed 或通过。

**范围：** macOS runner 上构建、收集、签名/公证前置脚本。**不做：** 仅以 Linux `bash -n` 证明 macOS 可用。

**2026-08-09 当前复核：** `go test ./internal/repo -run 'TestReleaseWorkflow|TestReleasePortability|Test.*Bash|Test.*Checksum' -count=1 -v` 通过（6 个 release workflow 测试）；`bash -n scripts/release-evidence.sh` 与 `bash -n scripts/wsl-linux-gate.sh` 通过，且 release workflow 的每个 bash step 语法测试通过。当前 Windows 的 bash 为 GNU Bash 5.3，不能替代 macOS 默认 Bash 3.2；无 macOS runner、真实 macOS artifact 或 macOS CI run，相关 AC 继续为 `U`。

### 执行点
1. 替换非 POSIX/BSD 不兼容写法，路径含空格时仍安全。
2. 把产物选择写成确定性逻辑，多个或零个候选均失败。
3. 在真实 GitHub macOS runner 执行无签名 smoke；有凭据时走签名/公证。

### AC
- [ ] macOS 默认 shell 运行脚本通过。`U`：无 macOS runner；T 级证据 = bash -n 语法全过 + 静态门禁禁止非可移植构造 + WSL bash 行为验证。尝试在 WSL 编译同版本 Bash 3.2 做真实执行，共 3 次均失败并如实记录：gcc-14 下 K&R 旧式声明与 C23 冲突（`too many arguments to function 'xmalloc'; expected 0, have 1`，即使 `-std=gnu89 -fcommon` 对 CFLAGS 与 CFLAGS_FOR_BUILD 均生效仍失败）；安装 gcc-12 后 configure 生成的 siglist.h 与旧源码不兼容（`expected identifier or '(' before 'char'`）。真实 Bash 3.2 执行需 macOS runner 或预编译 3.2 二进制（外部状态）。
- [x] 空产物、多产物、空格路径和错误架构均 fail-closed。`T`：WSL2 真实 bash 行为验证单候选/双候选（含空格文件名）/零候选/零/单 app bundle 全部符合预期；workflow 中 Linux/macOS 打包步骤 `-ne 1` 检查与 provenance/checksum `-eq 0` 检查保留。
- [ ] artifact 的架构、版本、checksum 可核验。`T/U`：workflow 有 GOARCH env、Verify tag 版本一致步骤、sha256sum/shasum 生成与 `sha256sum -c` 校验；`TestReleaseWorkflowChecksumFallback` 锁定 macOS 无 `sha256sum` 时的 `shasum -a 256` fallback 分支；真实 artifact 抽取需 macOS runner，`U`。
- [ ] 至少一条真实 macOS CI 历史链接/运行 ID 记入证据。`U`：需外部状态（GitHub 认证 + 真实 runner）。

**最低证据：** `T` + `R`。

## GOAL P9-G10（P0）：建立真实 packaged 日用基础闭环门禁

**现状与证据（2026-08-09 G10 复核）：** 缺口已变化。`scripts/packaged-e2e.mjs` 真实执行（非 dry-run）：pinned Wails CLI `v3.0.0-alpha2.111` 构建并启动真实 Windows packaged 进程，经 loopback E2E 端点驱动真实服务图，**23/23 fixture 全部通过**，包含项目/编辑/保存、真实终端与退出码/重连 UI、真实 gopls hover/completion、搜索替换、Git diff/worktree/rebase、AI fail-closed/cancel、AI 请求上下文、G13 Extension API、Monaco、G11 Settings CAS、G14 Debug、G15 Test Explorer 与 kill/restart recovery。首次红灯为 fixture 的越界 LSP 列号（`fmt.Prin` 有效 UTF-16 列为 9，driver 曾请求 12），修正后真实 gopls 回归测试与 packaged E2E 均通过。当前 `desktop,production,e2e` 测试 artifact SHA-256 为 `38b7d545c69535b72770f0cf544e95f6ee962db10b390cd580196eebde5cc410`，source fingerprint 为 `0bcd15bea4628ab0cf4e65914deeb9a4d36ab822aee74dd0e6846088852a07f5`；manifest 为 `build/e2e-evidence/packaged-e2e/manifest.json`，截图 `window.png` 为 1000x618。随后用当前依赖重建无 `e2e` hook 的 Windows production artifact `bin/production/koyori-ide.exe`，SHA-256 为 `36dc76ca58bcc0a66e9a4fd0a3fef3c3b7d184d9b94a01e261ecff28f5888774`，生产 dist 不含三个 probe marker。macOS/Linux packaged 矩阵仍为 `U`，未用 WSL 探测替代真实平台证据。

**范围：** 三平台包的启动、项目、编辑、保存、搜索、终端、Git、AI 基础调用、恢复、设置持久化。**不做：** 用浏览器 dev server 替代 packaged。

### 执行点
1. driver 连接真实 package，采集进程日志、截图、关键状态和退出码。
2. 测试新建/打开项目、编辑保存重开、搜索替换、终端命令、Git diff、AI 失败与取消、崩溃恢复。
3. Windows/macOS/Linux 分开记录能力差异；测试产物与源码 commit/版本绑定。

### AC
- [x] Windows packaged 基础闭环全部通过。`P`：`node scripts/packaged-e2e.mjs` exit 0，23/23 fixtures passed，manifest `build/e2e-evidence/packaged-e2e/manifest.json` status=`passed`，当前测试 artifact SHA-256 `38b7d545c69535b72770f0cf544e95f6ee962db10b390cd580196eebde5cc410`（pinned Wails alpha2.111），source fingerprint 见 manifest；两次真实进程启动（含 SIGKILL 崩溃恢复）。无 `e2e` hook 的 current production artifact `bin/production/koyori-ide.exe` SHA-256 为 `36dc76ca58bcc0a66e9a4fd0a3fef3c3b7d184d9b94a01e261ecff28f5888774`，无 probe marker。
- [ ] macOS 与 Linux packaged 同一矩阵通过，例外有 issue 和用户可见限制。`U`：需真实 macOS/Linux runner（外部状态）；Linux 可在 WSL 尝试（未完成）。
- [x] Monaco 非空、可输入，IPC 是真实 Wails 调用，重启后数据仍在。`P`：Monaco 探针、真实输入、搜索替换、Git diff、AI 失败/取消、重启恢复均在本次真实 packaged 进程通过；IPC 真实 Wails 调用由 G05/G06 证据支撑；`build/e2e-evidence/packaged-e2e/window.png` 为 1000x618 非空窗口截图；正式 production artifact 已重建且不含 e2e probe marker。
- [x] 任一核心步骤失败即门禁失败，不输出“部分通过=日常可用”。`T/P`：脚本任何 fixture 断言失败即 exit 1（本次 23/23 全过）；不宣布“日常可用”，完成含义为“可进入受限日用试用”。

**最低证据：** `P`。**完成含义：** 达到“可进入受限日用试用”，不是生产级 IDE。

---

## GOAL P9-G11（P1）：Settings 双窗口 CAS 与 debounce 并发一致性

**现状与证据（2026-08-06 终审）：** 缺口已不再存在。核心机制（prompt-7 Task F）：后端 `SaveSettings` 以 `ExpectedVersion` 做 CAS（version 冲突返回 `ErrSettingsConflict`，磁盘 version 单调递增）；前端 `saveSettings` 500ms debounce 捕获 `appState.settingsVersion` 作为 expectedVersion，冲突时 notifyError + `loadSettings()` 重读（不静默覆盖）。后端测试 `TestSettingsService_TwoWindowsDifferentFieldsPreserved`（两窗口改不同字段：B 的陈旧 CAS 被拒 → 重读（含 A 修改）→ 重放自身字段 → 磁盘同时保留两者）、`TestSettingsService_TwoWindowsSameFieldConflictVisible`（同字段冲突返回 ErrSettingsConflict 且不覆盖）、`TestSettingsService_StaleResponseDoesNotOverwriteNewer`（乱序旧响应被 CAS 拒绝）全部通过。前端 `flushSettingsSave()`：窗口关闭/卸载（`unregisterAppListeners`）时立即保存 pending debounce（原为取消 → 修改丢失），3 个新测试通过，前端全量 2642/2642。数据安全设计决策：冲突时采用「提示 + 重读最新 + 用户确认重应用」（不做自动字段合并，避免 diff 错误导致用户数据丢失）；后端流程已证明「重读+重放」可保留两窗修改。本轮新增 packaged 闭环：`internal/e2e` 的 `settings-concurrent` action 在真实打包产物中以双窗口视图驱动真实 `SettingsService`（种子 → A 提交 → B 陈旧 CAS 被拒 → B 重读重放 → 磁盘双字段保留），driver 新增 `settings-concurrent-package` fixture 并在 `CORE_FIXTURE_IDS` 断言；`node scripts/packaged-e2e.mjs` 12/12 fixtures passed（artifact SHA-256 `ef57d4b4cf83c2e81ed4f6d52d39bf871bee7defbb43cfcf9008a9096ef9702c`），`go build -tags e2e -buildvcs=false .`、`go vet ./internal/e2e`、`go test ./... -count=1` 全绿。
**范围：** 设置 schema、revision/CAS、patch、debounce、迁移和冲突 UI。

### 执行点与失败路径
1. 后端提供 revision 与字段级 patch/CAS；写入原子化，冲突返回最新值。
2. debounce 保存捕获 revision，窗口关闭 flush；旧响应不能覆盖新状态。
3. 覆盖两窗口改不同字段、同字段、乱序响应、磁盘写失败、schema migration。

### AC
- [x] 不同字段并发修改均保留；同字段冲突行为确定且可见。`T/I`：后端测试证明「重读+重放」可保留两窗不同字段修改（T）；同字段冲突返回 ErrSettingsConflict 且不静默覆盖（T）；前端冲突「提示+重读」确定且可见（T）；严格「自动保留」未实现是有意的数据安全决策（冲突后需用户确认重应用，避免自动 diff 错误导致用户数据丢失），已如实记录而非隐藏。
- [x] debounce、关闭、重开和写失败不丢设置。`T`：debounce 500ms（T）；`flushSettingsSave` 窗口关闭 flush（新实现 + 3 测试）；写失败 fail-closed（notifyError + 后端保留旧值，既有测试）；迁移（既有 MigratesLegacySchema）；重开数据保留（settings:changed 同步 + 既有测试）。
- [x] packaged 双窗口压力测试通过。`P`：Windows packaged E2E 新增 `settings-concurrent-package` fixture（真实 SettingsService 双窗口视图：A 以当前版本改 Theme → B 以陈旧版本改 FontSize 被 CAS 拒绝 → B 重读（含 A 修改）→ 以新版本重放 → 磁盘同时保留两窗字段）；`node scripts/packaged-e2e.mjs` 12/12 passed，artifact SHA-256 `ef57d4b4cf83c2e81ed4f6d52d39bf871bee7defbb43cfcf9008a9096ef9702c`，证据见 `build/e2e-evidence/packaged-e2e/manifest.json`。
**最低证据：** `I` + `P`。
## GOAL P9-G12（P1）：让 AI Plan、上下文、图片、Persona、MCP/Skills 真实进入请求

**现状与证据（2026-08-07 终审）：** 缺口已不再存在。本轮把 Plan、Persona、图片从「显示状态/测试岛」接入真实 provider 请求：前端 `sendMessage` 将 `activePlan`（goal+steps+状态）与 `activePersona`（systemPrompt，优先于全局 prompt）注入 `systemPrompt` 并随 `setConfig` 提交；persona chip 与 mcp/skill chip 一样序列化到消息前缀（原为「handled elsewhere」实际丢弃）；image chip 以结构化 `images` 字段挂在本次 user 消息上（不再丢弃）。后端 `ChatMessage` 新增 `Images []string`（data URL），`openAIMessages`/`anthropicMessages` 将其转换为 OpenAI `image_url` content block 与 Anthropic `image` base64 block，`validateImages` 在 provider 调用前强制预算（每消息 ≤4 张、解码 ≤5 MiB、仅 png/jpeg/webp/gif），超限/过大/类型不支持整体拒绝且绝不向 provider 发请求（fail-closed）；Send/SendStreamWithContext/streamWithEvents 三条路径全部转换并校验。真实打包闭环：packaged E2E 新增 `ai-request-context-package` fixture，在真实打包进程中启动本地协议服务（httptest）作为 provider，验证 system prompt（含 Plan 目标/步骤与 Persona）与图片 `image_url` block 原样到达 provider 请求体，`node scripts/packaged-e2e.mjs` 13/13 passed（artifact SHA-256 `a36ab78c20c9b32097eb7bdca1158e7458a1e18d66effb5a399dddfb2567a079`）。预算/取消由后端强制：MaxTokens（N-65）、ContextWindow（N-61）、StopStream（N-52）既有测试 + 新增图片预算 fail-closed 测试。bindings 因 ChatMessage 模型变化重新生成并更新 manifest（46 modules / 54 files，`node --test scripts/wails-bindings.test.mjs` 16/16，`check-bindings.mjs` exit 0，禁止导出表无变化）。
**范围：** composer 到后端/provider 的结构化 request、预算、权限、取消和审计。**不做：** 把 UI toggle 存在当作接线完成。

### 执行点与失败路径
1. 定义版本化请求模型，逐字段追踪 UI -> store -> backend -> provider adapter。
2. Plan 编辑可提交/撤销；附件按能力协商，MCP/Skill 只附带已授权清单。
3. 覆盖 provider 不支持、超预算、文件变化、图片过大、MCP 失联、取消和敏感数据过滤。

### AC
- [x] 真实 provider 或可检查的本地协议服务收到与 UI 一致的结构化字段。`P`：packaged E2E `ai-request-context-package` 在真实打包进程中以 httptest 本地协议服务为 provider，捕获请求体并断言 system prompt 含 Plan（目标+步骤）与 Persona、user 消息含 `image_url` block，13/13 fixtures passed（SHA-256 `a36ab78c20c9b32097eb7bdca1158e7458a1e18d66effb5a399dddfb2567a079`）；`I`：Go httptest 测试验证 OpenAI/Anthropic 请求体结构化转换。
- [x] Plan 修改影响下一次请求，不只是本地显示。`T`：前端 `sendMessage` 每次读取 `activePlan` 并注入 systemPrompt（goal+steps+状态），Plan 编辑后下一次请求反映新内容；正反测试（有/无 plan）在 `ai.test.ts` 中证明 `setConfig.systemPrompt` 含/不含 Plan 块；packaged probe 另证明 system prompt 中 Plan 字段到达 provider。
- [x] persona、图片、workspace context、MCP、Skills 各有正反集成测试。`T/I`：persona 正反（前端 2 测试 + probe personaInSystemPrompt）；图片正反（后端 7 测试：OpenAI/Anthropic/stream 转换、数量/大小/类型/非图片拒绝、无图保持 string、预算拒绝不触达 provider + probe imageBlock）；workspace context 正反（既有 attachContext/clearContext 测试）；MCP 正反（新 2 测试）；Skills 正反（新 2 测试）；前端全量 2653/2653。
- [x] token/费用预算与取消由后端强制。`T`：MaxTokens（N-65 既有）、ContextWindow（N-61 既有）、StopStream（N-52 既有）+ 新增图片预算 fail-closed（`TestAIService_G12_RejectsOversizedImage/RejectsTooManyImages/ImageBudgetFailsBeforeProviderCall`）；provider 调用前校验失败即拒绝，绝不发送部分/截断内容。
**最低证据：** `I` + `P`。

## GOAL P9-G13（P1）：Extension API 去除全部假成功

**现状与证据（2026-08-07 终审）：** 缺口已大幅收窄，假成功已消除。`workspace.saveAll` 原为直接 `return true`（假成功），现通过宿主注入的 `onSaveAll` 桥接 editor store 的 `saveAllFilesDetailed()`（新增）真实冲洗全部 dirty buffer 并传播逐文件失败（失败时 throw 含失败路径；无桥接时抛版本化 `KOYORI_IDE_EXT_API_UNSUPPORTED`，绝不假成功）。`window.showInputBox`/`showQuickPick` 原为返回默认值/首项（假 UI 成功），现 fail-closed 抛版本化错误。notification 原为 console-only，现经 `onNotify` 桥接真实 `lib/notifications`（未注入时降级 console 并标注 Partial，不假 UI 成功）。`createOutputChannel` 为内存缓冲+console（Partial，无 UI 面板）。运行时权威 capability matrix 新增 `frontend/src/lib/extensionHost/apiCapability.ts`（implemented/partial/unsupported + `KOYORI_IDE_EXT_API_UNSUPPORTED` v1 错误码），`docs/EXTENSION-COMPATIBILITY.md` 同步（含 G13 no-fake-success audit 表，移除过时的 getConfiguration 未实现条目）。packaged E2E 新增 `extension-api-g13-package` fixture，在真实打包 renderer 中验证 saveAll 无桥接 fail-closed、InputBox/QuickPick fail-closed、saveAll 桥接真实保存、notification 路由、output 可操作、configuration 桥接、tree view 注册/dispose，`node scripts/packaged-e2e.mjs` 14/14 passed（artifact SHA-256 `02538706c4f18f3b56a78aa4b0ed83e1c6378da16e35b63fd7032c5ef1af8784`）。前端全量 2666/2666，Go `go test ./...` exit 0。AC4（corpus 总数/可激活率/成功率统计）尚无代表性扩展 corpus 与统计运行，保持 `U`。
**范围：** 已声明兼容的 extension API 与贡献点。**不做：** 为未实现 API 返回空对象/成功 Promise。

### 执行点与失败路径
1. 建立 API capability matrix：Implemented/Partial/Unsupported，unsupported 明确抛版本化错误。
2. 上述 API 接到真实 UI、编辑器、配置与 dispose 生命周期；contributed view 有真实容器和权限。
3. 用代表性 VSIX corpus 验证激活、调用、关闭、异常、资源释放。

### AC
- [x] `saveAll` 真实保存并传播逐文件失败。`T/P`：`saveAllFilesDetailed()` 返回 `{savedCount, failedPaths}`，`saveFilePath` 失败入列；`workspace.saveAll` 经注入桥接真实冲洗 dirty buffer，失败 throw 含失败路径（editor.test.ts 新增 2 测试、extensionHost.test.ts 新增 3 测试）；packaged `extension-api-g13-package` 验证桥接真实调用（P）。
- [x] InputBox/QuickPick/notification/output/config/view 均有可操作 packaged 验证。`P`：packaged probe 验证 InputBox/QuickPick fail-closed（版本化错误）、notification 路由到宿主、output channel 可操作（append/show/clear/dispose）、configuration 桥接、tree view 注册/dispose 可操作；view 真实容器 UI 未验证（register/dispose 已可操作，UI 容器标 Partial）。
- [x] 未支持 API 不假成功，兼容矩阵与运行时一致。`T`：`ExtensionApiUnsupportedError`（`KOYORI_IDE_EXT_API_UNSUPPORTED` v1）+ `apiCapability.ts` 运行时矩阵（implemented/partial/unsupported）+ 5 测试断言矩阵与错误码一致；`docs/EXTENSION-COMPATIBILITY.md` 同步更新；InputBox/QuickPick 默认值/首项假成功已消除。
- [ ] 至少记录 corpus 总数、可激活率、核心 API 成功率与失败原因。`U`：尚无代表性扩展 corpus 与统计运行；本轮完成 no-fake-success 审计与矩阵，corpus 统计留待后续（需真实扩展样本集与激活/调用统计输出）。
**最低证据：** `I` + `P`。

## GOAL P9-G14（P1）：Debug 使用真实 DAP scope/variable reference

**现状与证据（2026-08-07 终审）：** 缺口已不再存在。变量引用不再硬编码/扁平：后端 `DebugVariable` 新增 `VariablesReference`（adapter 持有的 DAP `variablesReference` / CDP `objectId` 本地映射），`loadLocalsForFrameForRun` 从真实 scopes→variables 交换中保留 reference；新增 `GetVariables(ref, start, count)` 导出（DAP variables 命令 + 分页转发，拒绝非正引用）。Node CDP 路径新增连接级 `objectRefs` 映射（`getProperties` 为对象属性分配本地 ref，`getPropertiesByRef` 展开，`Close` 清空使旧引用天然失效），并修复产品缺陷：`launchNode` 缺少 `Runtime.runIfWaitingForDebugger` 导致 `--inspect-brk` 永远等待（stop-at-entry 是乐观标志、resume 被 adapter 拒绝），现已先设 `onPaused` 再释放运行时，真实 `Debugger.paused`（Break on start）到达并填充 stack/locals。前端删除 `LOCALS_VAR_REF = 10` 硬编码：`setVariable`/data breakpoint/`refreshInlineValues` 全部改用变量自身的 adapter reference；DebugPanel locals 支持嵌套展开/折叠（`expandedVariables` 状态 + `toggleVariableExpansion`），`applyDebugSnapshot` 保留 `variablesReference`。两种真实 adapter 集成测试通过（I）：真实 Delve DAP（`TestDebugService_G14_RealDelveNestedVariables`：断点→停→Outer→In→Z=42→单步→结束）与真实 Node CDP（`TestDebugService_G14_RealNodeCDPNestedVariables`：stop-at-entry→continue→debugger 停→outer→inner→z=42→单步→结束）。packaged P 级：`debug-g14-package` fixture 在真实打包进程中启动真实 dlv 完成同一工作流，`node scripts/packaged-e2e.mjs` 15/15 passed（artifact SHA-256 `e3a5ff3aa05a445a15149f890c13a4ace90c9452a2703df28d83f9a22e43301b`）。前端全量 2671/2671，`go test ./... -count=1` exit 0。
**范围：** DAP initialize/launch/attach、threads、stack、scopes、variables、evaluate、stop/terminate。

### 执行点与失败路径
1. 保留 adapter 返回的 `variablesReference`、frame/thread identity 和生命周期，停止后旧引用失效。
2. 支持多个 scope、嵌套、分页、evaluate 错误和 adapter crash。
3. Go/Delve 与 Node 调试真实样例至少各一套。

### AC
- [x] 两种真实 adapter 可断点、单步、展开嵌套变量并结束。`I/P`：真实 Delve DAP 与真实 Node CDP 集成测试均通过（断点→停→嵌套 Outer→In→Z=42 展开→单步→结束）；packaged `debug-g14-package` 在真实打包进程中跑真实 dlv 全流程（P）。
- [x] 无硬编码 reference；并发 session 不串数据。`T/I`：前端 `LOCALS_VAR_REF=10` 已删除，全部使用 adapter 提供的 reference（setVariable/dataBreakpoint/inlineValues/嵌套展开）；并发 session 隔离由既有 `DebugThreadsBackend` RunID/Generation/StateRevision 原子视图 + `ErrDebugThreadsStaleRun/ErrDebugThreadsStaleState` 保证（既有测试）。
- [x] adapter 缺失/崩溃、路径映射错误和 stale reference 可诊断。`T/I`：dlv 缺失 → `IsAvailable`/LaunchPackage 明确错误（既有）；adapter 崩溃 → natural disconnect 清理既有测试；stale reference → `GetVariables` 拒绝非正引用、CDP `Close` 清空 objectRefs 使旧引用天然失效（新测试 `TestDebugService_G14_GetVariablesRejectsInvalidReference` + 连接级映射）；路径映射 → browser `sourceURLToLocal`（既有）。
**最低证据：** `I` + `P`。

## GOAL P9-G15（P1）：Test Explorer 按语言选择真实 runner

**现状与证据（2026-08-07 复核）：** Goal 所需实现已闭环。后端 `RunTestAtCursor` 的 TS/JS 分支现由项目配置选择 runner（`resolveJSTestRunner`：jest.config.js/cjs/mjs/json 或 package.json `jest` 字段 → Jest；vitest.config*/vite.config* 或默认 → Vitest），不再依赖固定扩展名启发式；新增 Jest adapter（`npx jest <file> -t <name>`，参数经 `exec.CommandContext` 直接传参无 shell 插值）；参数构造提取为纯函数 `planJSTestRun` 并可单测。修复 Windows 产品缺陷：`command`/`commandContext` 对 `.cmd/.bat` shim（npx/npm/jest.cmd）通过 `cmd.exe /c` 启动（原 CreateProcess 直接执行 .cmd 报 `C:\Program` 错误，Vitest/Jest npx 路径此前在 Windows 不可用），context 取消保持传递。显式取消 API 已由后端 `CancelTestAtCursor`、Wails binding、前端 toolchain store 和 Test Explorer 取消按钮接通；取消真实活动子进程后返回 `Canceled`，并清除活动运行状态。真实集成测试（I）：`TestG15_RealGoTestAtCursor`、`TestG15_RealVitestRunThroughRunTestAtCursor`（真实 Vitest 样例通过）、`TestG15_RealJestRunThroughRunTestAtCursor`（真实 Jest CLI 链验证 argv = [file, -t, name]）与 `TestG15_CancelTestAtCursorStopsActiveRun`；单测覆盖 runner 解析 4 例、jest/vitest argv、shell 注入防护（`evil; rm -rf / && echo PWNED` 作为离散 argv 元素原样传递）和前端取消路由。真实 packaged renderer Test Explorer probe 已验证 pass/fail exit code、entry/tree 状态、output 保留和 running 清理。`go test ./... -count=1` exit 0。G15 专项后端 14/14、前端 toolchain/Test Explorer 41/41 通过。
**范围：** discovery、run/debug/cancel、状态、输出、单测/文件/项目级执行。

### 执行点与失败路径
1. runner 由 Language Pack/项目配置解析，不靠固定扩展名硬编码。
2. 实现 Go test、Vitest、Jest 的真实 adapter 与稳定 test identity。
3. 覆盖 watch 输出、重复名称、参数转义、取消、超时、runner 缺失与 monorepo。

### AC
- [x] Go/Vitest/Jest 样例仓库可发现、单独运行、失败定位和取消。`I/T`：真实 Go/Vitest/Jest 运行与非匹配测试失败定位通过（`TestG15_RealGoTestAtCursor`、`TestG15_RealVitestRunThroughRunTestAtCursor`、`TestG15_RealJestRunThroughRunTestAtCursor`）；`CancelTestAtCursor` 取消真实活动子进程并返回 `Canceled`（`TestG15_CancelTestAtCursorStopsActiveRun`），Wails binding、前端 store 和取消按钮由 41/41 定向测试覆盖。
- [x] TS/JS 不会调用 Go runner；命令参数无注入。`T/I`：`RunTestAtCursor` 按 language 分流（go→go test，typescript/javascript→vitest/jest），TS/JS 永不调用 Go runner；参数经 `exec.CommandContext` 直接传参（无 shell），注入测试证明 `evil; rm -rf / && echo PWNED` 作为单个 argv 元素原样传递（`TestG15_PlanJSTestRun_NoShellInjection`）。
- [x] packaged UI 状态与真实 exit code 一致。`P`：真实 packaged `test-explorer-g15-package` probe 验证 pass exit=0/fail exit!=0、entry/tree 状态、output 保留和 running 清理；`node scripts/packaged-e2e.mjs` 19/19 fixture 通过。
**最低证据：** `I` + `P`。

## GOAL P9-G16（P1）：接通 Terminal shell、scrollback、cursor 与 exit-code 协议

**现状与证据（2026-08-09 复核）：** 缺口已收窄，且补齐了 Unix signal 事件与重连 UI 的产品实现。后端 TerminalService 保留 shell 白名单（bash/sh/zsh/powershell/pwsh/cmd/wsl）、多会话、resize、1 MiB 有界 outputBuffer + 4KB/16ms 事件批处理（G-PERF-03）；`terminal:exited` 现在结构化发送 `sessionId/code/signal/err`，Unix PTY 从真实 `syscall.WaitStatus` 解析 signal，Windows 对 signal 留空。前置 Windows ConPTY 退出检测修复仍有效：`waitExit` 负责唯一 `Wait()`，子进程退出后关闭管道解除 `readLoop` 阻塞，真实 `cmd exit 7` 测试保持通过。前端 TerminalPanel 保存 cwd/shell，退出后保留退出码或 signal，按原参数调用 `reconnectSession`；重连失败保持 stopped 并保留错误，tab 中提供有 tooltip/ARIA 的重连图标按钮；会话 ID 使用 UUID/随机字节加进程内单调序号，避免同毫秒创建时覆盖已有会话。真实 Linux/WSL `TestTerminalService_UnixDefaultAndCustomShell` 覆盖默认 shell 与自定义 `sh` 启动/输入/exit 0，`TestUnixPty_RealSignalExit` 覆盖真实 `SIGTERM`；Windows G16 定向 Go 测试通过；store 24/24、TerminalPanel 29/29，生产 `vue-tsc + Vite build + HTTP binding postbuild` 通过。重新构建并运行 pinned Wails Windows packaged 矩阵 `node scripts/packaged-e2e.mjs` **23/23 passed**，其中 `terminal-reconnect-package` 通过真实 renderer 路由、TerminalPanel 重连按钮、同 session 重启以及 xterm surface 输出验证；当前 artifact SHA-256 `38b7d545c69535b72770f0cf544e95f6ee962db10b390cd580196eebde5cc410`，source fingerprint `0bcd15bea4628ab0cf4e65914deeb9a4d36ab822aee74dd0e6846088852a07f5`，manifest/screenshot 见 `build/e2e-evidence/packaged-e2e/manifest.json`。macOS runner、macOS 默认 shell 与 macOS signal 仍无环境证据，故不能勾选 AC1/AC3。
**范围：** profile/shell、cwd、env、PTY resize、scrollback、cursor、exit、restart、链接。

### 执行点与失败路径
1. 后端枚举可信 shell profile；参数结构化传递，cwd 来自 WorkspaceContext。
2. 设置动态作用于新/现有实例的语义明确；exit code/signal 作为结构化事件。
3. 大输出有有界 scrollback 与背压；关闭窗口释放 PTY。

### AC
- [ ] 三平台默认 shell 与自定义合法 shell 可真实启动。`I/U`：Windows 默认 powershell 与自定义 cmd 真实启动（既有测试 + packaged）；Linux/WSL 默认 shell 与自定义 `sh` 已有真实集成测试，但 macOS 仍无 runner/环境证据（U）。
- [x] scrollback/cursor 配置实际生效且持久化。`T`：TerminalPanel xterm 读取持久化设置（scrollback 钳制 [100,5000] 保持 G-PERF-03 内存门禁、cursorStyle block/underline/bar），4 个新测试；设置持久化走既有 settings 保存。
- [ ] exit 0/非 0、signal、启动失败、resize、重连 UI 正确。`I/P/U`：Windows 真实 exit 7、非法 shell fail-closed、resize 与 packaged `terminal-reconnect-package` 通过；Linux/WSL 真实 `SIGTERM` 已由 `TestUnixPty_RealSignalExit` 验证，前端 store/TerminalPanel 24/24、29/29 覆盖重连状态、按钮和同毫秒会话 ID 防碰撞；macOS signal 仍无证据，故本 AC 不勾选。
- [x] 大输出不导致无限内存或 renderer 卡死。`T`：后端 outputBuffer 1 MiB 上限 + 4KB/16ms 事件批处理（G-PERF-03 既有）+ xterm scrollback 5000 上限（前端，永不放开）。
**最低证据：** `I` + `P`。

## GOAL P9-G17（P1）：修复 Git `.code-workspace` repo root 与 sibling worktree safe roots

**现状与证据（2026-08-09 复核）：** 已完成。GitService 以可信 workspace roots 校验所有 repoPath（单根/多根 all-or-none），新增 `DiscoverRepositories` 递归发现嵌套 repo（不跟随目录 symlink、不遍历 `.git` 元数据），ProjectService 多根两阶段切换与回滚同步 Git roots；`.code-workspace`、单目录、多根与嵌套 repo 的 GitPanel 选择器不再从显示路径猜测，并明确当前 repo。GitWorktreeService 的 `trustedTargetRoots` 由 `repoPath + addSafeRoots + wsCtx.Root()` 派生，路径校验覆盖 NUL/换行/父级穿越/symlink 逃逸；真实 Git discovery 边界测试、GitPanel 36/36 定向测试通过。真实 packaged P 级：`node scripts/packaged-e2e.mjs` **23/23 passed**，包含 `git-worktree-package` 与 `git-rebase-package`（真实两提交历史 → GetRebaseTodoList → StartInteractiveRebase → ApplyRebaseActions → ContinueRebase → 无进行中状态）；当前 artifact SHA-256 `38b7d545c69535b72770f0cf544e95f6ee962db10b390cd580196eebde5cc410`，manifest `build/e2e-evidence/packaged-e2e/manifest.json`，source fingerprint `0bcd15bea4628ab0cf4e65914deeb9a4d36ab822aee74dd0e6846088852a07f5`。空 `.git` 仍按 fingerprint 记录，未伪造 commit。
**范围：** 多根 repo 选择、workspace 文件、嵌套 repo、sibling worktree 边界。

### 执行点与失败路径
1. Git repo 从后端 workspace roots/discovery 获取，不从显示路径猜测。
2. safeRoots 由可信 workspace 配置派生，新增 sibling 需显式预览/授权并防 symlink 逃逸。
3. 多 repo UI 明确当前 repo，切换不改变 workspace authority。

### AC
- [x] `.code-workspace`、单目录、多根、嵌套 repo 均选中正确根。`T/I`：GitPanel 36/36（workspaceRoot 优先、单目录回退、多根/嵌套选择），GitService discovery 多根边界测试通过；后端多根 roots 与 ProjectService 两阶段回滚同步。
- [x] 合法 sibling worktree 可创建，越界/碰撞/symlink 被拒绝。`I/P`：真实 git 集成测试（sibling 创建/锁定/移动/移除 + 越界拒绝）+ packaged `git-worktree-package`（workspace 内创建+列出+越界拒绝，P）；symlink 逃逸/穿越/NUL 拒绝有既有测试。
- [x] Git 状态、diff、rebase 和 worktree packaged 流程通过。`P`：真实 packaged `node scripts/packaged-e2e.mjs` 23/23，含 git-diff、git-worktree、git-rebase；artifact/manifest 见上。
**最低证据：** `I` + `P`。

## GOAL P9-G18（P1）：AI Diff 区分“已提交但 UI 同步失败”并可恢复

**现状与证据（2026-08-09 复核）：** 缺口已闭合。后端 `WorkspaceEditApplyResult` 返回 commit receipt：`TransactionID`（随机 hex）+ `FileHashes`（提交后磁盘内容 hash）；事务在首个磁盘写入前生成 ID，生产 `DiffService` 在所有编辑写入成功后以原子 JSON 持久化 receipt，receipt 写入失败会回滚编辑并返回失败，避免“已写盘但无可恢复凭据”。receipt 按 workspace 身份分文件保存于用户配置目录；`GetLatestCommitReceipt()` 每次从磁盘读取，并校验 workspace 身份、路径边界、transaction/hash 格式、时间戳及当前磁盘 SHA-256，损坏、跨 workspace 或磁盘漂移均 fail-closed。前端 `diff.ts` 的 `DiffApplyOutcome` 保留 `committed-ui-sync-failed` 并携带 receipt；`applyTransaction` 的 UI 同步阶段（`syncTransactionalWrite`）失败时明确区分“磁盘事务已提交、仅 UI 同步失败”，绝不抛错诱导重试；`DiffViewer.vue` 显示「已写入磁盘，请从磁盘重载——不要重复应用」+ i18n（en/zh/ja），并通过 generated binding 暴露 receipt recovery。注入测试覆盖 UI sync failure、receipt 携带、重启后 service recreation、磁盘漂移和 receipt 持久化失败回滚；dirty-buffer conflict 与 retains-typing 测试保留。packaged P 级：真实打包进程先 ApplyDiff 一次，再 `SIGKILL`/重启第二进程；`ai-diff-receipt-recovery-probe` 验证 receipt 可读、transaction ID 稳定、file hash 与磁盘一致、workspace 匹配、旧 diff 二次 apply 被拒且磁盘不变。`build/e2e-evidence/packaged-e2e/manifest.json` 状态为 `passed`，23/23 fixture 通过，`g18ReceiptRecovery` 六项均为 `true`；当前 packaged test artifact `bin/koyori-ide.exe` SHA-256 `38b7d545c69535b72770f0cf544e95f6ee962db10b390cd580196eebde5cc410`，source fingerprint `0bcd15bea4628ab0cf4e65914deeb9a4d36ab822aee74dd0e6846088852a07f5`。前端定向 diff/receipt 测试 2 文件/12 测试、Go receipt 定向测试通过、packaged driver 合约 3/3、artifact 与 manifest SHA-256 一致；无 probe 的正式 production artifact 另见 G08/G10，SHA-256 `36dc76ca58bcc0a66e9a4fd0a3fef3c3b7d184d9b94a01e261ecff28f5888774`。
**范围：** commit receipt、UI sync、dirty buffer、重试、重载和审计。

### 执行点与失败路径
1. 后端返回带 transaction ID、文件 hash 和 committed 状态的 receipt。
2. 前端状态至少区分 `not-committed`、`committed-synced`、`committed-ui-sync-failed`。
3. 后者只允许从磁盘/receipt 恢复 UI，不得重新提交磁盘事务。

### AC
- [x] 注入每个同步阶段失败，磁盘只提交一次。`T/P`：UI 同步失败注入 → `committed-ui-sync-failed` 且 `applyDiffTransaction` 恰好调用 1 次（测试）；packaged `ai-diff-receipt-package` 验证同 diff 二次 apply 被拒且磁盘不变（P）。
- [x] UI 明确告知已写盘并提供安全重载/合并。`T`：DiffViewer 对 `committed-ui-sync-failed` 显示「已写入磁盘，请从磁盘重载——不要重复应用」（en/zh/ja i18n）。
- [x] 重启后可用 receipt/磁盘状态收敛，无重复 diff。`T/P`：新 service 实例从用户配置目录读取 receipt，transaction ID 保持、workspace 匹配、receipt hash 与磁盘一致；重放原 diff 被 hash precondition 拒绝，拒绝前后磁盘内容不变。packaged manifest 的 `g18ReceiptRecovery` 六项断言全部为 `true`。
- [x] dirty editor 冲突不静默覆盖。`T`：dirty-buffer conflict 拒绝（不调事务）+ retains-typing（事务期间用户输入保留，changedDuringWrite 逻辑）既有测试保留。
**最低证据：** `T` + `P`。

## GOAL P9-G19（P1）：消除 npm High 并建立可复现依赖治理

**现状与证据（2026-08-09 复核）：** 缺口已收窄。官方 registry 审计现已清零：`undici@7.28.0 → 7.29.0`（jsdom 传递依赖）、`js-yaml@4.3.0 → 4.3.1`（eslint→@eslint/eslintrc 传递依赖）和 `dompurify@3.4.12 → 3.4.13`（运行时直接依赖）均为兼容升级，未忽略 advisory；`npm audit --registry=https://registry.npmjs.org --json` 报告 info/low/moderate/high/critical 全部为 0。`frontend/package-lock.json` 的 672 个 resolved URL 已全部归一到官方 `registry.npmjs.org`，避免 `npm ci --registry` 被旧 mirror URL 绕过；`scripts/npm-audit-gate.mjs` 现同时门禁官方 registry high+ 审计、resolved URL 白名单和 `npm install --package-lock-only --dry-run` 前后 package-lock.json SHA-256 不变，本机执行 exit 0；`ci.yml` 的 npm-audit job 与 `release.yml` 均改为该统一门禁并显式 `npm ci --registry=https://registry.npmjs.org`（release 前置 audit 补齐）。依赖重装后前端全量 167 files / 2690 tests、vue-tsc、ESLint、Vite production build/postbuild 全部通过。剩余：真实 CI runner 保存 dependency/audit 证据（AC4）标 `U`（工作树无可核验 `.git` 元数据、无 CI runner 可执行；本轮公开 GitHub API 也无法连接）。
**范围：** lockfile、dev/runtime 区分、官方 advisory、可复现安装、升级策略。

### 执行点与失败路径
1. 通过兼容升级/override 消除漏洞，验证测试环境行为；不得只忽略 advisory。
2. CI 使用锁文件与明确 registry，缓存不能改变解析结果。
3. runtime 与 dev 风险分开报告，例外需到期日、owner 和威胁分析。

### AC
- [x] 干净环境 `npm ci` 可复现且 lockfile 不漂移。`T`：672 个 resolved URL 全部为官方 `registry.npmjs.org`；`npm install --package-lock-only --dry-run` 前后 package-lock.json SHA-256 一致；`scripts/npm-audit-gate.mjs` 将 registry 白名单和 lockfile 稳定性设为门禁。
- [x] 官方 registry `npm audit --audit-level=high` 为 0 或有经审计的临时例外。`T`：undici 7.29.0、js-yaml 4.3.1、dompurify 3.4.13 兼容升级后，官方 registry JSON 报告各级漏洞均为 0，无例外；官方 registry 显式指定。
- [x] 全量前端测试/type/lint/build 通过。`T`：2690/2690 + vue-tsc + ESLint + Vite production build 全绿。
- [ ] 真实 CI 保存 dependency/audit 证据。`U`：ci.yml/release.yml 已配置统一门禁，但无真实 CI runner 可执行（当前工作区没有可核验 `.git` 元数据）。
**最低证据：** `T` + `R`。

## GOAL P9-G20（P1）：强化 VSIX 解压配额、发布者签名和权限

**现状与证据（2026-08-07 复核）：** 缺口已收窄。VSIX 解压已具备路径穿越防护（`filepath.Clean` + `evalSymlinksAllowMissing` 解析后校验 + 绝对路径/Windows 卷相对拒绝）、symlink 条目拒绝（既有）。本轮补齐解压配额（G20 fail-closed）：总展开大小 ≤200 MiB、单文件 ≤50 MiB、条目数 ≤5000、压缩比 ≤1000:1（zip bomb）、路径长度 ≤1024、嵌套深度 ≤32；预算按**实际流式字节**计数（`io.LimitReader` + `stats.totalBytes`），伪造 header 无法绕过；重复目标路径与大小写碰撞（`Foo.txt`/`foo.txt`）拒绝。新增 `TestG20_VSIX_*` 10 个测试（正常解压、zip bomb 比例、条目数、路径长度、嵌套深度、总大小、单文件、拒绝后无残留文件、重复路径、大小写碰撞）全部通过。既有签名/hash 治理完整：`VerifyExtensionSignature`（正确/错误/空 hash、大小写不敏感、流式 SHA-256、不存在文件）、黑名单（内置/用户/阻止安装/阻止启用）、未验证拒绝启用、approval token 单次消费。安装/升级原子性：`.installing` 临时目录失败清理 + `.updating`/`.backup` 升级回滚（既有）。新增 10 个固定 SHA-256 的 Open VSX 真实 VSIX 语料：2 个无运行入口的资产包真实安装、manifest/安全记录/默认禁用/卸载全部通过；8 个带 `main`/`browser` 但未声明 `koyoriIde.permissions` 的包按安全策略拒绝，且无安装目录和安全状态残留。`TestG20_VSIX_RealCorpusInstallMatrix` 真实矩阵 exit 0，日志 `build/e2e-evidence/p9-g20/real-corpus-security-matrix.log` SHA-256 `b4df0887d61f4a1c217cd384f647db6d3cf6e3d48347e50944055f92e096a5b4`。`go test ./... -count=1` exit 0。
**范围：** 下载、hash、签名、解压、manifest、权限、安装/升级/回滚。

### 执行点与失败路径
1. 流式限制总展开大小、文件数、单文件大小、压缩比、路径长度和嵌套深度；先校验再原子发布。
2. 定义可信发布者身份与签名链；无签名扩展默认明确警告/策略拒绝。
3. manifest 权限在激活前展示并绑定版本；升级新增权限需重授权。

### AC
- [x] zip slip、symlink、zip bomb、超配额、重复路径、大小写碰撞全部拒绝。`T`：既有 traversal/absolute/symlink 测试 + 新 10 个配额/重复/碰撞测试（`TestG20_VSIX_*`），全部 fail-closed 且拒绝后无残留文件。
- [x] 签名、hash、publisher 不匹配和撤销路径有测试。`T`：VerifyExtensionSignature 系列（正确/错误/空/大小写/流式/不存在）、黑名单增删与阻止安装/启用、未验证拒绝启用、manifest publisher/name 校验（既有）；撤销路径=黑名单+卸载（既有测试）。
- [x] 安装失败不留下半成品；升级可回滚。`T`：`.installing` 临时目录 + 失败清理（既有 PathTraversal/解析失败测试验证）；升级 `.updating`/`.backup` 回滚（既有）。
- [x] 真实 VSIX corpus 安装矩阵可复现。`T/I`：Open VSX 固定 SHA-256 语料 10 个；真实矩阵 `TestG20_VSIX_RealCorpusInstallMatrix` 结果为 installed=2、rejected-for-missing-permissions=8、total=10、exit 0。资产包完成真实安装/卸载与安全状态核验；可执行包缺失 `koyoriIde.permissions` 时 fail-closed 且无残留。运行日志为 `build/e2e-evidence/p9-g20/real-corpus-security-matrix.log`。
**最低证据：** `T` + `I`。

## GOAL P9-G21（P1）：闭合许可证、artifact SBOM、签名与 provenance

**现状与证据（2026-08-09 G21 复核）：** 缺口已变化但未闭合。许可证生成器现在扫描四个目标（Windows amd64、Linux amd64、macOS amd64/arm64）的 `desktop,production` 实际 Go package 闭包，并合并 frontend lockfile；当前为 53 个 Go module、639 个 npm package-version、`UNKNOWN=0`、`UNRESOLVED=0`。离线 `--check` 与发布用 `--full-check` 均通过；未进入任何生产闭包的 Wails 间接 module（包括无许可证的 qsort）不会被误报为已分发代码，若未来被目标导入则 `--full-check` 会 fail-closed。新增 `scripts/check-release-assets.mjs`，锁定 frontend public allowlist、Wails Vue 模板源哈希、原生图标哈希，并拒绝 dist 残留；未使用的背景图和 Inter 字体已移出 public 分发边界。当前生产前端构建后 `node scripts/check-release-assets.mjs --check --require-dist` 通过，Codicon 由 Monaco 0.52.2 生成并由其 MIT/ThirdPartyNotices 覆盖，`docs/RELEASE_ASSET_LICENSES.md` 已随 NOTICE 同步。`scripts/generate-sbom.sh` 已改为强制接收一个最终 artifact，并用 `file:<artifact>` 扫描，不再扫描 checkout 目录；新增 `scripts/check-sbom-artifact.mjs`，强制 SPDX-2.3 的唯一 file root SHA-256 与最终 artifact 一致，release workflow 已接入，3/3 fail-closed 测试通过。基于正式更名后的 `bin/production/koyori-ide.exe` 生成的本机 Windows artifact `koyori-ide-v0.2.0-windows-amd64.zip` 已由固定源 Syft 实际扫描：artifact SHA-256 `7ad09a65ca3c9e31d9e34504f679a54511c299f8419226383de516bf6fdf6303`，SPDX-2.3 SBOM SHA-256 `d34def80b46e6748b687dff918de2853b357e0255e2009c65258406c20ccc897`，含 2 个 package，SBOM 内 root digest 与 artifact 一致；这只是本机 artifact 证据，不是 release run。`bash -n`、repository release contract、macOS Bash 3.2 portability、文档链接/数字门禁均通过。独立 `go-licenses@v1.6.0 report ./...` 因许可证 URL 外联超时在 300 秒后退出，日志 SHA-256 `21c5e61dac262091b75c9116f88148fc1539dc2e44d892d114c6f54619d83e1e`，不能当作通过证据；其余三平台最终 artifact、真实 release run、签名/公证和外部 provenance 仍需发布机验证，保持 `U`。

**范围：** Go/npm/内嵌二进制/字体图标/扩展资产许可，逐 artifact SBOM，checksum、签名、公证、provenance。

### 执行点与失败路径
1. 消除所有 `UNRESOLVED`，保留来源、版本、许可证文本和生成器版本。
2. 对每个平台最终分发物解包/扫描，SBOM 与 artifact digest 绑定。
3. release provenance 绑定源码 commit、builder、依赖锁、测试与签名；验证器独立运行。

### AC
- [x] license inventory 为 0 unresolved，NOTICE 与实际分发内容一致。`S/T`：四个生产 Go 闭包与 frontend lockfile 的 inventory 为 `UNKNOWN=0`、`UNRESOLVED=0`，`--check`/`--full-check` 通过；`node scripts/check-release-assets.mjs --check --require-dist` 在真实 production frontend build 后通过，Wails 模板资产、原生图标和 Monaco Codicon 的来源、哈希及许可证记录在 `docs/RELEASE_ASSET_LICENSES.md`，NOTICE 和发布 workflow 已同步该边界。
- [ ] 每个平台 artifact 都有 SPDX/CycloneDX SBOM，不再仅扫描源码目录。`S/T/I`：脚本和 release workflow 已强制逐 artifact `file:<artifact>` 扫描、非空、JSON 解析和 `scripts/check-sbom-artifact.mjs` digest 绑定；本机 Koyori Windows artifact 的 SPDX-2.3 扫描与 root digest 校验已通过并有 artifact/SBOM SHA-256，但其余三平台和真实 release run 仍为 `U`。
- [ ] checksum、签名/公证、provenance 可由发布机外验证。
- [ ] 真实 release run 保存全部证据并在任一缺失时失败。

**最低证据：** `R`。

## GOAL P9-G22（P1）：诚实化文档并清理编码、格式、死 UI 与测试岛

**现状与证据（2026-08-07 G22 复核）：** 缺口已闭合。意外 U+FFFD 已按语义修复，`scripts/check-encoding.mjs` 与其测试通过；Go 格式问题已清零，前端 `vue-tsc`/ESLint、文档链接/数字门禁通过。README 能力声明已补证据链接并收敛 LSP 表述，日文缺失键已补齐。复核发现 Plan 设置区的“编辑后批准”原先只调用 `approveStep`、丢弃 `editBuffer`，现已改为调用真实 `editStep` 并在失败时保留编辑态；Plan store 与输入框回归测试覆盖真实透传、失败保留和无 `echo`。Plan 两个入口不再生成/兜底 `echo` 步骤，后端执行器拒绝空工具/参数；扩展动态视图明确显示 unsupported，不伪造内容。全量前端 168 files/2696 tests、生产 build/postbuild、Go 全量 test/vet/build、bindings、编码和文档门禁均通过；LICENSE、NOTICE、`.github/CONTRIBUTING.md`、`.github/SECURITY.md`、CODE_OF_CONDUCT、构建复现与发布证据文档均已复核。

**范围：** README、兼容矩阵、SECURITY、贡献/构建说明、字符编码、格式、死代码与孤立测试。

### 执行点与失败路径
1. 按 `S/T/I/P/R` 证据生成能力矩阵，明确 prototype、partial、unsupported。
2. 逐个恢复损坏字符语义并检查全仓 UTF-8；执行 Go/TS/Vue 格式与静态门禁。
3. 追踪 UI 控件到真实调用；删除无产品路径的死入口，或完成接线，不保留假功能。
4. 恢复真实 git metadata 后核验来源、tracked 产物、贡献历史与开源必要文件。

### AC
- [x] 全仓无意外 `�`，gofmt/lint/type/doc gate 通过。`T`：`node scripts/check-encoding.mjs`、`node --test scripts/check-encoding.test.mjs`、`gofmt -l .`、`npx vue-tsc --noEmit`、`npx eslint src`、`node scripts/check-doc-links.mjs`、`node scripts/check-doc-numbers.mjs` 均 exit 0；前端全量 168 files/2696 tests 通过。
- [x] README 能力逐项有证据链接，未实现项无完成式宣传。`T/P`：`go test ./... -count=1`（含 `internal/repo` 治理测试）exit 0；README 验证边界、RELEASING、E2E、兼容矩阵、资产/许可证文档链接已复核，Remote/VSIX/LSP/供应链等未验证项保留 `S/U`。
- [x] 所有可见核心控件有真实消费者或明确 disabled/unsupported 状态。`T`：全量 Vitest、`InputComposer.test.ts` 的无 `echo` 计划测试、`aiPlan.test.ts` 2/2、Go `DefaultStepExecutor`/`AIPlanService` 切片通过；Plan 编辑真实调用 `editStep`，空工具/参数后端拒绝，扩展动态视图显示 unsupported，既有扩展 API/Computer Use fail-closed 测试保持通过。
- [x] LICENSE、NOTICE、CONTRIBUTING、SECURITY、构建复现和治理信息齐全。`T/S`：`LICENSE`、`NOTICE`、`.github/CONTRIBUTING.md`、`.github/SECURITY.md`、`.github/CODE_OF_CONDUCT.md`、`docs/RELEASING.md`、`.github/workflows/ci.yml` 实体与链接复核；文档链接门禁 exit 0，CI/release 无历史 run 的限制仍按 G21/G07 保持 `U`。

**最低证据：** `T` + 关键声明 `P/R`。

---

## GOAL P9-G23（P2）：实现 Language Pack Runtime/SDK，并迁移 Go/TS

**现状与证据（2026-08-09 推进）：** Windows x64 的真实 packaged artifact 已完成 Go/TypeScript 内置语言包闭环。`bin/koyori-ide.exe` SHA-256 为 `38b7d545c69535b72770f0cf544e95f6ee962db10b390cd580196eebde5cc410`，源码指纹为 `0bcd15bea4628ab0cf4e65914deeb9a4d36ab822aee74dd0e6846088852a07f5`；`build/e2e-evidence/packaged-e2e/manifest.json` 为 `passed` 且 23/23 fixtures 通过，其中 `language-pack-builtins-g23-package` 真实验证 Monaco Go/TypeScript language ID、Go/TypeScript LSP、格式化、构建、测试、Go Delve/TypeScript Node 调试、pack ID/version/source metadata 与原生 debug approval 消耗。因此 AC1 勾选。两份内置 JSON manifest 共同驱动 Go/TS/JS 检测、LSP、结构化 toolchain 动作和本地 debugger；manifest 的闭合兼容协商覆盖 engine API `1.0`、host protocol `language.local.v1` 及 Windows/macOS/Linux 的 amd64/arm64 平台集合。原生 manifest-only installer 只接受 `manifest.json` 与 `signature.json`，验证 16 MiB/entry 限额、canonical SHA-256、Ed25519、显式 key-pinned publisher trust、staged rename、active-version 绑定卸载与审批 rollback；版本使用严格 SemVer，预发布数值标识符按数值比较、build metadata 不影响优先级，`1.0.0-2` 相对 `1.0.0-10` 的降级会在不改变目录或状态的前提下被拒绝。真实本地 Python pack 使用 Pyright `1.1.411`、debugpy 和 5,000 文件/双根 workspace，Rust pack 使用 rustc/cargo/rust-analyzer `1.97.1`、LLVM/lldb-dap `22.1.8` 和双 Cargo 根；两者均通过非空 completion、第二根 hover、声明式 toolchain、真实 DAP 断点与局部变量 `answer=42`。packaged `language-pack-g23-package` 另验证 Python/Rust 签名 manifest 的 publisher trust、安装、pin、disable/enable、rollback 与 uninstall，但没有运行其完整 LSP/DAP，不能据此勾 AC2。服务二进制 payload installer、跨平台 packaged matrix 和 remote language host 均未实现，保持 `U`；故 Goal 阻塞于 1/4。

**范围：** grammar/Monaco ID、LSP、formatter/linter、build/test、DAP、installer、平台变体、能力探测。

### 执行点与失败路径
1. 定义版本化 manifest、runtime API、权限、生命周期、诊断和兼容协商。
2. 把 Go 与 TypeScript 从硬编码迁为首批内置语言包，产品行为不回归。
3. 增加 Python/Rust/Java 中至少两种外部语言包验证 SDK 通用性。
4. 工具缺失、版本不兼容、下载失败、离线、崩溃和恶意输出均隔离。

### AC
- [x] Go/TS 全部编辑、LSP、格式、构建、测试、调试经语言包完成。
- [ ] 第三方语言包无需修改 IDE 核心即可安装并贡献完整能力。
- [ ] 平台 installer 有 checksum/signature、版本 pin 与卸载回滚。
- [ ] 大仓库、多根、远程和离线矩阵通过。

**最低证据：** `I` + `P`。**禁止：** 仅发布 manifest schema 即勾选完成。

## GOAL P9-G24（P2）：建立独立 Extension Host 和版本化贡献协议

**现状与证据（2026-08-14 复核，历史基线）：** 缺口已不存在，G24 仍为 4/4（本次是复验，不是重复完成）。已实现 Dedicated Worker Extension Host（`frontend/src/lib/vscodeExtensionActivation.ts`）：ABI `1.0` 协商（`protocol-ready/protocol-error`、协商前 RPC 拒绝、未知/重协商版本拒绝）、随机 token 校验（伪造 token 忽略）、heartbeat watchdog（2s/8s）、消息配额（4 MiB/消息、1000 msg/s fail-closed）、Worker 内部 `error` 事件桥、crash/hang/rate/size 统一恢复。2026-08-14 复验把旧的 25ms `isExtensionActivated()` 瞬时 inactive 采样改为每次 Worker 随机 runtime identity；四类 fault 均要求 `previousRuntimeId != recoveredRuntimeId` 且恢复版本匹配，避免把短暂状态采样竞态误判为产品恢复失败。2026-08-11 历史收口运行的 artifact `7e8abf...`/source `690aa3...` 与其后的 `0xc0000142`/24 not-run 失败均保留为历史。2026-08-14 该代码态的 packaged manifest 为 `status=passed`、`phase=complete`、24/24 fixtures 通过；artifact SHA-256 `9e382df322b7ea42f881a6cc656c029c5e32432cac84286b162f1a5c39caaa1a`，source fingerprint `09635b427d2fc8852049c5b44550ed8ed2a49afc7ef4aabd097401865fcf8485`，`recordedAt=2026-08-14T10:26:36.958Z`，`completedAt=2026-08-14T10:29:00.371Z`，manifest 记录 HEAD `18b43cf0825f1e280dc56b54563c8f73506bbd36`。当前代码态权威 manifest 见 P12-G33 行与 prompt-12 §13.30。工作树不干净，HEAD 本身不代表全部被测源码；历史 P 级结论以 artifact digest、manifest fixture 明细与 launch 日志为准。G24 corpus 报告仍为 11/11 测试、10 包全 blocked（缺 `koyoriIde.permissions` 声明），无假成功。

**历史门禁与环境记录（2026-08-14）：** 2026-08-11 的 9/9 backend-gate 与 WSL/skip-build 记录保留为历史。2026-08-14 该代码态复跑：`node scripts/backend-gate.mjs` 9/9、exit 0（首次全量 Go 腿偶发失败，独立复跑及第二次完整 gate 均通过）；`task frontend:check` 172 files / 2739 tests，ESLint、`vue-tsc`、bindings、docs 全部 exit 0；独立 `node scripts/check-bindings.mjs`、`check-doc-links.mjs`（25 Markdown）、`check-doc-numbers.mjs` 均 exit 0；真实 `node scripts/packaged-e2e.mjs` 第二次 exit 0。第一次 packaged 运行在构建成功后因旧 G24 探针错过短暂 inactive 窗口而 exit 1；runtime identity 判据修复后重跑 24/24。该次 `screenshot=null`，原始 P 级日志为 `launch-1.log`/`launch-2.log`，不得把 2026-08-09 的 `window.png` 冒充本次截图。以上仅证明当时本地 Windows T/I/P，不声称 CI、tag、release 或 macOS/Linux packaged；当前代码态门禁见 P12-G33 行与 prompt-12 §13.30。

**范围：** 独立进程/Worker、ABI negotiation、激活、RPC、权限、配额、崩溃隔离、更新。

### 执行点与失败路径
1. Extension Host 不持有任意后端 service；所有贡献经版本化、可撤销 capability RPC。
2. 限制 CPU、内存、进程、文件、网络和消息速率；扩展崩溃不带走编辑器。
3. 定义 activate/deactivate/reload/upgrade 和状态迁移；输出可诊断日志。
4. 维护真实 VSIX corpus、API coverage 和兼容率，不以单个 demo 宣称兼容。

### AC
- [x] host 崩溃/卡死/超配额时主 IDE 可继续编辑保存并重启 host。`P`：当前 packaged 24/24 manifest 为 `passed`；crash/hang/rate/size 四类 fault 均记录不同的 previous/recovered runtime ID，并覆盖故障后 edit/save、host kill/restart recovery；artifact SHA-256 `9e382df322b7ea42f881a6cc656c029c5e32432cac84286b162f1a5c39caaa1a`。
- [x] ABI 新旧版本协商、权限拒绝和消息伪造 fail-closed。`P`：最终 packaged 证据覆盖 ABI fallback/reject、协商前拒绝、forged token ignored 与 permission denial。
- [x] API/贡献点矩阵由 corpus 自动生成且无假成功。`T/I`：`node --test scripts/g24-corpus-report.test.mjs` 11/11；真实 corpus 报告 10 包全 blocked（缺权限声明），安装成功未当作激活成功。
- [x] packaged 安装、激活、升级、禁用、卸载闭环通过。`P`：当前 `build/e2e-evidence/packaged-e2e/manifest.json` status=`passed`，24/24 fixtures passed，disable/uninstall 及 kill/restart recovery 均通过；artifact SHA-256 `9e382df322b7ea42f881a6cc656c029c5e32432cac84286b162f1a5c39caaa1a`，source fingerprint `09635b427d2fc8852049c5b44550ed8ed2a49afc7ef4aabd097401865fcf8485`。

**最低证据：** `I` + `P`。

## GOAL P9-G25（P2）：实现动态国际化与可移植个性化

**现状与证据（2026-08-10 复核）：** 缺口已变化但仍存在。静态中文/英文字符串和零散设置不足以支持运行时 locale、RTL、插件语言包或跨设备 profile。本轮已完成以下 T 级基础（packaged 矩阵仍 `U`，G25 AC 仍未闭环）：

- `frontend/src/lib/localeMetadata.ts`：locale 规范化（`en-US`/`zh_CN` → `en-us`/`zh-cn`）、CLDR plural categories（静态表 + `Intl.PluralRules` fallback；en=one/other、zh/ja=other、ru/pl=one/few/many/other、ar=zero/one/two/few/many/other）、RTL 检测（ar/he/fa/ur/ps/yi/dv）。
- `frontend/src/lib/i18n.ts`：`translate()` 支持 ICU plural selector（`{count, plural, one{# file} other{# files}}`，经 `Intl.PluralRules(locale).select(count)` 选类，unknown category 回退 other）；`formatNumber()` ICU 数字格式化；缺失键监测计数 `__getMissingKeyCount()`/`__resetMissingKeyCount()`（缺键仍 fallback 到 raw key 不崩溃，但可监测）。
- `frontend/src/lib/i18n.g25.test.ts`（13 测试）与 `localeMetadata` 测试：plural one/other、zero 回退、unknown category、兄弟占位符、俄语段、missing-key 计数、locale 规范化、复数语言类别、RTL 检测全部通过。
- 修复 `i18n.test.ts` 中 `prompts.defaultSystem` 断言（产品名已为 "Koyori IDE" 大小写不敏感匹配）——pre-existing 漂移，非本轮引入。
- `services/profile_service.go`：`ProfileExport` 增加 `schemaVersion`（=1）；导出时 redact 顶层 `aiApiKey` 与嵌套 `aiProviderConfigs[].apiKey` 敏感字段、拒绝导出非法 JSON settings；导入时校验 schema 版本（未知版本 fail-closed）、settings 必须是合法 JSON 对象、大小 ≤1 MiB、拒绝 `__proto__`/`constructor`/`prototype` 原型污染键、规范化落盘、未知字段保留。
- `services/profile_service_test.go`：新增 8 个测试（导出 redact、非法 JSON 拒绝、导入未知/缺失 schema 拒绝、非法 JSON 拒绝、空 settings 拒绝、超大拒绝、未知字段保留），全部通过。
- bindings 重新生成并更新 manifest（47 服务模块 / 55 文件，`--accept-export-surface`）。

**范围：** ICU message、lazy locale pack、plural/date/number、fallback、pseudo locale、RTL/bidi、插件翻译、profile/theme/layout/keymap。

### 执行点与失败路径
1. 所有用户可见字符串使用稳定 message ID 与 ICU；启动错误和后端错误也可本地化。
2. locale 可运行时切换，无需重启且不丢编辑状态；缺失键按确定链 fallback。
3. pseudo locale 检测截断；RTL/bidi 覆盖 editor 外 UI、路径和代码片段隔离。
4. 个性化 profile 可版本化导入导出、预览、合并和回滚；secret 不进入导出。

### AC
- [ ] 至少 `en-US`、`zh-CN`、一种复数复杂语言和一种 RTL 语言通过 packaged 矩阵。`U`：locale 元数据与 ICU plural 解析已 T 级实现（en/zh/ja 静态、ru/pl/ar 类别表、RTL 检测），但 packaged 切换矩阵未运行。
- [ ] 日期/数字/复数无拼接错误，缺键可监测且不显示 raw key。`T`：`formatNumber` ICU 数字格式化、ICU plural 经 `Intl.PluralRules` 选类、missing-key 计数可监测且 raw key 不崩溃（49 个 i18n 测试通过）。
- [ ] 插件/语言包可提供翻译且不能覆盖系统安全提示。`S`：语言包运行时已有（G23），安全提示覆盖保护未实现。
- [ ] profile 跨平台导入，未知字段、冲突和恶意文件安全处理。`T`：schema 版本化导入导出、敏感字段 redact、未知字段保留、大小限制、非法 JSON 拒绝；恶意文件与跨平台 packaged 导入矩阵 `U`。

**最低证据：** `I` + `P`。

## GOAL P9-G26（P2）：实现 Unified Remote Workspace Host

**现状与证据：** Remote 协议文档不等于远程 IDE；统一 Workspace URI/Scope 契约、本地 Host Adapter、Linux root-fd + `openat2` no-follow 边界、非 Linux fail-closed 路径和 SSH host identity/connection nonce/approval scope 已有 T 级实现。Windows 原子替换使用 `MoveFileExW(REPLACE_EXISTING|WRITE_THROUGH)`，但真实 NTFS 证据仍为 `U`。远端 agent、host-issued workspace ID、PTY、Git、LSP、DAP、Test、断线收敛和 packaged 闭环仍缺失，全部 AC 保持未勾选。

**范围：** host identity、URI、认证、FS/watch、PTY、Git、LSP、DAP、Test、端口转发、断线重连、同步冲突。

### 执行点与失败路径
1. workspace URI 明确 scheme/authority/path，所有服务按 host 路由，不混用本地路径。
2. 远端 agent 版本/能力协商、最小权限安装、签名升级和回滚。
3. 断网重连使用 generation/sequence/resync；离线编辑冲突不静默覆盖。
4. 端口转发默认 loopback、显式授权，SSRF、凭据和日志脱敏纳入威胁模型。

### AC
- [ ] 至少 SSH/Linux 远端完成编辑保存、watch、Terminal、Git、LSP、Test、DAP。
- [ ] 断网、host 重启、客户端重启后状态可收敛且无重复写。
- [ ] 本地/远端路径不串用，跨 host token 不可重放。
- [ ] 高延迟、大仓库、端口转发和多根 workspace 有 packaged 证据。

**最低证据：** `I` + `P`。**禁止：** 仅完成 broker/mock/协议文档即勾选。

## GOAL P9-G27（P2）：建立发布运营、SLO、更新回滚与外部审计

**现状与证据：** G27 Phase 1 已建立 T 级签名更新候选/回滚授权契约和默认关闭的本地运营事件 schema；仍没有可核验的真实 crash/startup/edit durability 数据、稳定发布历史或独立安全/供应链/可访问性审计。Phase 1 的进程内 replay tracking 不是持久化发布防重放证据。

**范围：** signed update、rollback、channel、指标、隐私、性能、可访问性、安全、供应链和支持策略。

### 执行点与失败路径
1. 更新 manifest、包和 channel 全部签名；失败/损坏/降级/撤回可自动回滚且保留用户数据。
2. 定义并采集启动、崩溃、无响应、编辑持久性、LSP 延迟、扩展崩溃 SLI；默认最小化和可退出。
3. 建立性能预算、可访问性门禁、漏洞响应和 LTS/support 生命周期。
4. 委托独立安全、供应链和可访问性审计；问题有公开处置和复测。

### AC
- [ ] 至少三个不同 commit 的真实三平台 release 通过签名更新与回滚演练。
- [ ] 连续稳定窗口达到已发布 SLO，原始查询与样本偏差可审计。
- [ ] 大仓库性能和 WCAG 2.2 AA 核心流程门禁通过。
- [ ] 三类外部审计完成、P0/P1 问题关闭并复测。

**最低证据：** `R`。**完成含义：** 才可评估“广泛日常可用/生产级”，不自动等于结论。

---

## 3. 统一 Definition of Done

一个 Goal 只有同时满足以下条件才可标记完成：

- 范围内执行点和失败路径均有实现；全部 AC 为 `[x]`，每项附命令、退出码、测试名或 artifact/run ID。
- 最低证据等级达到要求；低等级证据不得替代。多平台要求不能用一个平台外推。
- 新增/修改行为有成功、拒绝、取消、超时、重试、并发、崩溃/重启和资源释放中适用的测试。
- 安全边界仍 fail-closed，用户数据失败时可恢复，不存在“返回成功但无动作”。
- 前端全量 test/type/lint/build 与后端全量 test/vet/format 通过；相关 packaged 矩阵通过。
- 文档、兼容矩阵、日志脱敏、迁移和回滚同步完成。
- 未删除或弱化既有测试；无无关重构；无 secret、生成垃圾、临时二进制。
- 本文进度板实时回写。若任一项未达成，只能标“进行中”或“阻塞”，并保留未勾选 AC。

## 4. 安全不回归矩阵

| 边界 | 必测攻击/失败 | 预期 |
|---|---|---|
| Workspace/path | 空 root、`..`、绝对逃逸、symlink/junction、UNC、大小写、TOCTOU | 后端拒绝，无副作用 |
| Capability | 伪造、重放、过期、跨 generation/窗口/host、参数变化 | 单次绑定并拒绝 |
| 文件/编辑 | baseline 冲突、部分写、磁盘满、权限失败、崩溃 | 原子或明确 committed receipt，可恢复 |
| 进程/Terminal | 参数注入、恶意 env/cwd、超时、大输出、子进程遗留 | 结构化启动、限额、完整清理 |
| 网络/Remote | SSRF、私网 redirect、跨 host token、MITM、断线乱序 | 默认拒绝、身份绑定、重同步 |
| 扩展/语言包 | zip bomb、路径逃逸、假签名、超配额、ABI 欺骗 | 安装前拒绝、隔离、可回滚 |
| 更新/供应链 | 降级、替换 artifact、SBOM 不匹配、签名撤销 | 验证失败即停止/回滚 |
| 隐私/AI | secret 泄漏、未授权上下文、日志原文、预算绕过 | 最小披露、可审计、后端强制 |

## 5. 真实工作流验收矩阵

每个格子记录 `平台 / commit / artifact SHA-256 / 操作步骤 / 结果 / 证据路径`，不得只写 pass。

| 工作流 | Windows | macOS | Linux | 最低级别 |
|---|---|---|---|---|
| 安装、首次启动、卸载 | 待验证 | 待验证 | 待验证 | P |
| 打开单目录/`.code-workspace`/多根 | 待验证 | 待验证 | 待验证 | P |
| 编辑、保存、冲突、重启恢复 | 待验证 | 待验证 | 待验证 | P |
| Search/replace 与 AI Diff 事务 | 待验证 | 待验证 | 待验证 | P |
| Terminal/Git/worktree | 待验证 | 待验证 | 待验证 | P |
| Go/TS LSP、格式、测试、调试 | 待验证 | 待验证 | 待验证 | P |
| AI 请求、Plan、附件、MCP/Skills | 待验证 | 待验证 | 待验证 | P/I |
| 扩展安装、激活、崩溃隔离 | 待验证 | 待验证 | 待验证 | P |
| locale/profile 切换 | 待验证 | 待验证 | 待验证 | P |
| 远程 workspace 断线重连 | 待验证 | 待验证 | 待验证 | P |
| 更新、回滚、数据保留 | 待验证 | 待验证 | 待验证 | R/P |

### Packaged E2E 硬要求

1. 必须启动分发形态产物，不是 `vite dev`、浏览器页面或 mock Wails runtime。
2. 每次运行验证 canvas/Monaco 非空且可输入，检查窗口无重叠和关键按钮可达。
3. driver 通过受限 e2e build tag/hook 控制；production artifact 必须证明该入口不存在。
4. 截图之外还需核验磁盘、进程、PTY、网络服务或 Git 状态的真实副作用。
5. 失败保留日志和工作目录；成功后清理临时凭据，不清除审计证据。

## 6. 性能、大仓库与可访问性基准

- 样例仓库至少包含：10k/100k 文件、1 GB 工作树、monorepo、多根、超长行、大二进制、深目录和大量 Git changes。
- 记录冷/热启动、首次可编辑、文件打开、搜索、Git status、LSP ready、测试 discovery、内存峰值、主线程 long task、扩展 host CPU。
- 阈值必须在 G27 通过真实样本确定；在此之前只报告分布，不伪造 SLO。基准退化超过批准预算即门禁失败。
- 640x720、1366x768、1920x1080、高 DPI、键盘-only、screen reader、高对比、200% zoom、RTL 均覆盖核心工作流。
- 性能优化不得牺牲保存完整性、安全校验、错误可见性或无障碍语义。

## 7. 建议验证命令（执行时按仓库脚本复核）

```powershell
# 后端基础门禁
gofmt -l .
go test ./... -count=1
go vet ./...
go build -buildvcs=false ./...

# 工程门禁
node scripts/generate-bindings.mjs
node scripts/check-bindings.mjs
node scripts/check-doc-links.mjs
node scripts/check-doc-numbers.mjs
node scripts/check-wails-pin.mjs
node scripts/contract-smoke.mjs
node scripts/packaged-e2e.mjs --dry-run

# 前端门禁
Set-Location frontend
npm ci
npm run bindings:generate
npm test
npx vue-tsc --noEmit
npm run lint
npm run build
npm audit --registry=https://registry.npmjs.org --audit-level=high
```

执行纪律：逐条记录退出码；不要用管道最后一个命令的退出码冒充被测命令。真实 packaged、CI、签名、SBOM 和外部审计需另记 artifact digest/run URL/audit report，以上命令不能替代。

## 8. 进度板（唯一状态源）

状态只能为：`未开始`、`进行中`、`阻塞`、`完成`。完成时必须写证据等级；阻塞不能勾选未验证 AC。

| Goal | 状态 | 已满足 AC | 证据等级 | 最后更新 | 阻塞/下一步 |
|---|---|---:|---|---|---|
| P9-G01 | 阻塞 | 3/4 | T/I/U | 2026-08-05 | 恢复可核验 `.git` 元数据后确认 untracked 归属；npm audit 1 High，Docker 镜像构建仍为 U |
| P9-G02 | 完成 | 4/4 | T/I/P | 2026-08-05 | dev 与 packaged 真实 binding/HTTP 矩阵通过；`.git` 元数据不可用且 npm audit 1 High 已如实记录，仍不得发布 |
| P9-G03 | 完成 | 4/4 | T/I | 2026-08-05 | 空 root、generation、MCP approval、Windows junction/大小写/UNC 与 AI 子窗口两阶段切换矩阵通过；真实 stdio 子进程证据已归档，npm audit 1 High 仍归 G19，packaged dry-run 仍为 U；下一项为 G04 |
| P9-G04 | 完成 | 4/4 | T/P | 2026-08-06 | Windows packaged 3 次启动、2 次真实强杀、重复崩溃与恢复后保存闭环通过；audit 1 High 仍归 G19，空 `.git` 已明确记录 |
| P9-G05 | 完成 | 4/4 | T/I/P | 2026-08-06 | pinned Wails packaged artifact 完成 A→B 多窗口切换；主窗/AI 窗 Search、AI preset、Terminal 均命中同一 workspace，真实日志/截图与 SHA-256 已归档；G06 role token 回归修复后 T/I/P 证据已在当前代码重跑通过，空 `.git` 仅记录 fingerprint |
| P9-G06 | 完成 | 4/4 | T/I/P | 2026-08-06 | 可信 runtime role（单次消费/防伪造/防重放）、role 化 bootstrap 服务图与生命周期计数落地；packaged 双窗无重复 IDE 初始化与单 owner 验证通过；role token 与 workspace generation 解耦修复主窗降级回归；npm audit 1 High 仍归 G19；下一项为 G07 |
| P9-G07 | 阻塞 | 3/4 | T/I/U | 2026-08-09 | 当前 Windows `go test ./... -count=1`、CI 同款 `-race` 包集合、backend-gate 9/9、contract smoke 与格式均重跑通过；`ci.yml` 三平台 race 矩阵与显式 `-count=1` 由 `TestGoTestWorkflowHasExplicitCrossPlatformRaceMatrix` 锁定，WSL2 Linux 腿为既有 I 证据；AC3 仍需真实 macOS 与 CI runner。公开 GitHub API 与 Actions 页面本轮均无法连接，外部状态阻塞，不能将本地证据升级为 R |
| P9-G08 | 阻塞 | 3/4 | T/I/P/U | 2026-08-09 | VERSION 单一输入同步 config/package.json/info.json/manifest/MSIX/nfpm/Info.plist，`--check` 无 drift；当前 Koyori IDE Windows production exe 经 Win32 API 抽取验证 0.2.0.0，SHA-256 `36dc76ca58bcc0a66e9a4fd0a3fef3c3b7d184d9b94a01e261ecff28f5888774`，Linux 真实 deb 抽取 0.2.0-1；Go runtime 嵌入 VERSION；AC3 macOS 腿需 macOS 环境，外部 runner 当前不可达 |
| P9-G09 | 阻塞 | 1/4 | T/U | 2026-08-09 | release.yml 全部 mapfile/-maxdepth/-printf 改为 bash 3.2/BSD 兼容（while-read + glob + awk），当前 release workflow 合约、bash 语法与 shasum fallback 测试重跑通过；Windows GNU Bash 5.3 不能替代 macOS Bash 3.2，AC1/AC3/AC4 仍需真实 macOS runner；外部 runner 当前不可达 |
| P9-G10 | 阻塞 | 3/4 | T/P/U | 2026-08-14 | 当前 Windows packaged 24/24 fixtures 通过（含真实 gopls、Settings CAS、Extension API、Debug、Test Explorer、Git worktree/rebase、AI receipt、终端重连与恢复）；artifact SHA-256 `9e382df322b7ea42f881a6cc656c029c5e32432cac84286b162f1a5c39caaa1a`，source fingerprint `09635b427d2fc8852049c5b44550ed8ed2a49afc7ef4aabd097401865fcf8485`。本次 `screenshot=null`，以 manifest/launch 日志归档；AC2 macOS/Linux packaged 同矩阵仍为 U，状态不变 |
| P9-G11 | 完成 | 3/3 | P | 2026-08-06 | CAS/ExpectedVersion/debounce 机制 + 窗口关闭 flush + 3 个后端新测试 + 前端 3 个新测试全部通过；packaged 双窗口压力测试完成：`settings-concurrent-package` fixture 12/12 passed（真实 SettingsService 双窗口视图 CAS：陈旧 B 被拒、重读重放、磁盘双字段保留），artifact SHA-256 `ef57d4b4cf83c2e81ed4f6d52d39bf871bee7defbb43cfcf9008a9096ef9702c`；AC1 严格自动保留为有意的数据安全决策（提示+重读+用户确认） |
| P9-G12 | 完成 | 4/4 | P | 2026-08-07 | Plan 注入 systemPrompt（goal+steps）、Persona 优先接管 chat prompt、persona/mcp/skill chip 序列化、图片以结构化 images 字段真实发送（OpenAI image_url / Anthropic image block，后端 4 张/5 MiB/类型白名单 fail-closed）；packaged E2E `ai-request-context-package` 13/13（本地协议服务捕获 system prompt + image block），artifact SHA-256 `a36ab78c20c9b32097eb7bdca1158e7458a1e18d66effb5a399dddfb2567a079`；预算/取消后端强制（MaxTokens/ContextWindow/StopStream + 图片预算）；bindings 重新生成并更新 manifest（16/16 契约、check-bindings exit 0） |
| P9-G13 | 阻塞 | 3/4 | P/U | 2026-08-09 | saveAll 真实保存+逐文件失败传播、InputBox/QuickPick fail-closed（版本化 KOYORI_IDE_EXT_API_UNSUPPORTED）、notification 桥接真实 UI、capability matrix（apiCapability.ts + EXTENSION-COMPATIBILITY.md 同步）；真实 packaged `extension-api-g13-package` 通过，整体 packaged 证据已归档；现有 Open VSX 10 包语料只能证明 G20 安全拒绝（8 个执行包缺 Koyori 权限、2 个资产包），不能产出 G13 激活/API 成功率；AC4 需可激活的代表性语料，网络当前不可达 |
| P9-G14 | 完成 | 3/3 | P | 2026-08-07 | DebugVariable 携带 adapter 真实 variablesReference（DAP scopes/variables + Node CDP objectId 映射），GetVariables 分页/嵌套展开；前端删除 LOCALS_VAR_REF=10 硬编码并支持嵌套展开；修复 launchNode 缺 runIfWaitingForDebugger（stop-at-entry 假标志）；真实 Delve/Node adapter 集成测试（I）+ packaged `debug-g14-package` 真实 dlv（P）15/15，artifact SHA-256 `e3a5ff3aa05a445a15149f890c13a4ace90c9452a2703df28d83f9a22e43301b` |
| P9-G15 | 完成 | 3/3 | T/I/P | 2026-08-07 | TS/JS runner 由项目配置解析（jest.config/package.json jest → Jest，默认 Vitest）+ Jest adapter；真实 Go/Vitest/Jest 集成测试与 `CancelTestAtCursor` 真实进程取消通过；修复 Windows `.cmd` shim（`cmd.exe /c`）产品缺陷；前端取消路由定向测试 41/41；真实 packaged Test Explorer 已验证 pass/fail exit code 与 UI 状态，整体 19/19 fixtures 通过 |
| P9-G16 | 进行中 | 2/4 | T/I/P/U | 2026-08-14 | `terminal:exited`、cwd/shell、fail-closed 重连与防碰撞会话 ID 已实现；Linux/WSL 默认/自定义 `sh` 与 SIGTERM 通过；当前 Windows packaged `terminal-reconnect-package` 及完整 24/24 矩阵通过，artifact `9e382df322b7ea42f881a6cc656c029c5e32432cac84286b162f1a5c39caaa1a`；macOS runner/默认 shell/signal 仍为 U，AC/状态不变 |
| P9-G17 | 完成 | 3/3 | T/I/P | 2026-08-14 | GitService 多根/nested discovery、GitPanel repo 选择与 ProjectService 回滚接线完成；当前 packaged 24/24 含真实 rebase/worktree，artifact `9e382df322b7ea42f881a6cc656c029c5e32432cac84286b162f1a5c39caaa1a`、source `09635b427d2fc8852049c5b44550ed8ed2a49afc7ef4aabd097401865fcf8485`；G17 仍为 3/3 完成 |
| P9-G18 | 完成 | 4/4 | T/P | 2026-08-14 | commit receipt、`committed-ui-sync-failed`、磁盘漂移/重复提交拒绝与重启恢复均已验证；当前 Windows packaged manifest 24/24，`g18ReceiptRecovery` 六项全为 `true`，artifact `9e382df322b7ea42f881a6cc656c029c5e32432cac84286b162f1a5c39caaa1a`；G18 仍为 4/4 完成 |
| P9-G19 | 阻塞 | 3/4 | T/U | 2026-08-09 | npm audit 各级清零；672 个 lockfile resolved URL 已统一为官方 registry；npm-audit-gate 的官方 registry/URL 白名单/lockfile 稳定性通过；ci.yml/release.yml 接统一门禁；前端 168 files/2696 tests + type/lint/build/postbuild 全绿；AC4 真实 CI 证据仍为 U，公开 GitHub API/Actions 当前不可达 |
| P9-G20 | 完成 | 4/4 | T/I | 2026-08-07 | VSIX 解压配额 fail-closed（总 200MiB/单 50MiB/5000 条/压缩比 1000/路径 1024/深度 32，流式计数防伪造 header）+ 重复路径/大小写碰撞拒绝 + 10 个新测试；签名/hash/黑名单/未验证拒绝启用与安装原子性既有；Open VSX 真实 10 包矩阵通过（2 安装/卸载、8 缺少权限声明而拒绝），日志 SHA-256 `b4df0887d61f4a1c217cd384f647db6d3cf6e3d48347e50944055f92e096a5b4` |
| P9-G21 | 阻塞 | 1/4 | S/T/I/U | 2026-08-09 | 四个生产 Go 闭包 + frontend lockfile inventory 为 UNKNOWN/UNRESOLVED=0；新增 public/native asset allowlist、Wails 模板源哈希、dist 残留和 SBOM-to-artifact digest 绑定门禁，production frontend 资产检查通过，NOTICE/资产许可证记录已同步；本机 Koyori Windows artifact `7ad09a65ca3c9e31d9e34504f679a54511c299f8419226383de516bf6fdf6303` 与 SPDX SBOM `d34def80b46e6748b687dff918de2853b357e0255e2009c65258406c20ccc897` 已验证；其余三平台、签名、公证、真实 CI/tag/release 证据需外部发布机，当前 U |
| P9-G22 | 完成 | 4/4 | T/P/S | 2026-08-07 | 编码、格式、类型、lint、文档门禁、全量前端 168 files/2696 tests、生产 build/postbuild、Go 全量 test/vet/build、bindings、Plan editStep/无 echo/后端拒绝空步骤均已通过；外部 release/CI 历史不属于本 Goal，继续按 G21/G07 标为 U |
| P9-G23 | 阻塞 | 1/4 | T/I/P/U | 2026-08-14 | 当前 Windows x64 packaged artifact `9e382df322b7ea42f881a6cc656c029c5e32432cac84286b162f1a5c39caaa1a`、source `09635b427d2fc8852049c5b44550ed8ed2a49afc7ef4aabd097401865fcf8485`，24/24 fixtures 通过；Go/TS Monaco ID、LSP、format/build/test、Delve/Node debug 与 pack metadata 通过，AC1 保持完成。Python/Rust packaged 完整 LSP/DAP、服务 payload installer、跨平台 packaged 与 remote language host 仍为 U；状态/AC 不变 |
| P9-G24 | 完成 | 4/4 | T/I/P | 2026-08-14 | 当前 packaged manifest `passed`/24/24；artifact `9e382df322b7ea42f881a6cc656c029c5e32432cac84286b162f1a5c39caaa1a`，source `09635b427d2fc8852049c5b44550ed8ed2a49afc7ef4aabd097401865fcf8485`，recordedAt `2026-08-14T10:26:36.958Z`。四类 fault 的 runtime ID 均更换，edit/save、disable/uninstall、kill/restart 通过；首次旧 inactive 采样竞态失败与修复保留。仅本地 Windows P，不声称 CI/release/跨平台 |
| P9-G25 | 进行中 | 0/4 | T/U | 2026-08-10 | ICU plural 解析（Intl.PluralRules 选类，含 ru/pl/ar 真实 few/many/zero/two 验证）、locale 元数据（独立 localeMetadata.test.ts，en/zh/ja 静态 + ru/pl/ar 类别 + RTL 检测 + Intl 缺失 fallback）、formatNumber、missing-key 监测已 T 级实现（i18n 全量 53 测试）；profile 版本化导入导出（schema v1、顶层 aiApiKey + 嵌套 aiProviderConfigs[].apiKey redact、1MiB 限制、非法 JSON/未知版本/原型污染键 fail-closed 拒绝）已 T 级实现（12 个新 Go 测试）；packaged 矩阵与恶意文件跨平台导入 U；AC 0/4 正式勾选 |
| P9-G26 | 进行中 | 0/4 | T/U | 2026-08-11 | Workspace URI/Scope、Linux no-follow 本地 Host Adapter、非 Linux fail-closed、Windows MoveFileEx 原子替换代码、SSH verified HostID/随机 instance nonce/命令 approval 完整 scope 绑定已 T 级实现；真实 Windows NTFS、remote agent、host-issued workspace、FS/watch/PTY/Git/LSP/Test/DAP、重连和 packaged 证据仍为 U；AC 0/4 |
| P9-G27 | 进行中 | 0/4 | T/U | 2026-08-12 | T 级 Ed25519 manifest/channel/platform/artifact/digest/降级授权与事务状态契约已实现；默认关闭、无网络、隐私闭合的本地运营事件 v1 buffer 已实现；真实三平台签名 release/rollback、生产 SLO 窗口、性能/WCAG 门禁和三类独立外部审计仍为 U；AC 0/4 |
| P12-G33 | 进行中 | 0/6 | T/I/P/U | 2026-08-20 | AgentCore catalog/capability/lifecycle/meter、typed adapters、durable recovery/headless CLI 与真实 Windows MCP stdio 已有 T/I 子证据；P12-BUG-02 已正式纳入本 Goal。renderer reactive stream、明确 reasoning summary、工具 timeline/批准/turn barrier、durable conversation handoff 与受控 loopback provider 的 packaged `read` 两轮已取得 T/P。当前 backend gate 9/9（Go test 418.2s）；frontend 静止源码 178/178 files、2869/2869 tests；fresh packaged 24/24，artifact `5bb66931...`、source `864bcd97...`（1045 files）、`artifactReused=false`。首次 source-final-verification 发现并修复 ConversationService 默认根把会话写到 cwd 的生产 bug。H1 原始范围、真实 provider/manual mutation/restart ledger/双 WebView I、fallback/output budgets、完整 MCP、跨平台/CI/CLI 与 npm `nanoid` high 仍 U/红；AC 全未勾选，唯一 Goal 仍为 G33。旁路事实：P13-G01~G04 已收口；H1 仍部分修复。P13-G05 AC1/AC2 绿，AC3 frontend:check 红保留，AC4 nanoid U。packaged 脏树已绑 harness（HEAD 18b43cf + porcelain af69540e + source bc677b18），完整 fixtures 未完成且本会话禁 Wails 构建（会打崩 DSH web）。未 commit；不改变 G33 AC。 |

> 最新全量复核（2026-08-16，详见 prompt-12 §13.17）：Agent Workflow secure loader 代码变化后，`node scripts/backend-gate.mjs` 9/9 exit 0（全仓 Go test 375.3s），`task frontend:check` 173/173 files、2763/2763 tests exit 0，bindings/docs exit 0；`node scripts/packaged-e2e.mjs` 24/24 exit 0，manifest completedAt `2026-08-15T22:03:31.427Z`，artifact SHA-256 `ec0847a981867f52969e8f2cb04719485a78bba2e67a4ba07cc925558f1e8353`。backend 首轮临时文件时序 flake 与 packaged WebView `EBUSY` 均保留并按隔离复跑/既有重试处理。独立供应链门禁仍红：G33 锁图 `npm-audit-gate` 因 `nanoid` high advisory exit 1；hardening 的不同锁图绿灯不可替代。G33 AC 仍为 0/6，后续代码变化后上述全量与 packaged 必须重跑。

> 本轮交接复核（2026-08-16）：H1 原始全平台范围仍存在（Linux T、Windows junction I、macOS/Reveal/CAS/公共 Workflow pathname loader U）；H2 已不存在（真实 Windows `cmd.exe` 两路径 I）；H3 前端完整性门禁与 vue-tsc 已复核通过。AI provider resolver 脱敏改动已完成定向 Go 测试，但其后 full gate、packaged 与 G33 `npm-audit-gate` 尚未重跑；所有 G33 AC 继续 `[ ]`。

> **本轮最新覆盖（2026-08-16，file.write 子切片）**：上述旧快照已由当前共享树复核覆盖。G33 仍为进行中、AC 0/6；typed workflow `file.write` 已接入 backend-owned source、统一 catalog/capability、workspace transaction 与 `WriteFileIfUnchanged` CAS，renderer 只能传空 args，冲突/源变更/审批缺失 fail-closed。`node scripts/backend-gate.mjs` 9/9、`task frontend:check` 173 files/2765 tests、bindings/docs 均 exit 0；`node scripts/packaged-e2e.mjs` 最新 manifest 24/24 passed（recordedAt `2026-08-16T01:45:05.557Z`，artifact `facaf467b692ececbbde53d40482bfc3f7126d2281abe55b0670ff0d8141a7ed`，source `b9922c3238eae371166efc5fa03dfe5141ad977244cdf879212a33405465d0d3`，Windows 本机 P）。`node scripts/npm-audit-gate.mjs` 仍 exit 1，命中 `nanoid <3.3.18` / `GHSA-2v37-7h3g-55p8` / 1 high，锁文件未改；hardening 另一锁图绿灯不可互换。真实 provider/MCP、Git mutation、recovery/manual-disposition、跨 caller ownership、domain rollback、macOS/Linux packaged 与 CI/CLI 仍 U，AC 全部保持 `[ ]`。

> **本轮最新覆盖（2026-08-16，dynamic catalog / packaged evidence integrity）**：上述 file.write packaged source `b9922c...` 经复核发现来自手工静态清单，遗漏 `services/agent_execution_core.go`、`services/agent_execution_workflow_skill.go` 与 `internal/agentcore/**`，不能继续作为当前源码绑定证据。现已用递归 `build-inputs-v2` 覆盖 services/internal/frontend/scripts/build 与相关根文件，任意深度排除依赖/缓存/evidence，排除生成 bindings 与已知构建残留，拒绝 symlink/junction，并在构建后和 fixtures 后复核；skip-build 只有在 prior manifest 的 source/artifact/toolchain/tags 全匹配后才允许。dynamic catalog refresh 同时用单一锁串行 clear/rebuild/publish，交错红灯与相关 `-race -count=20` 已转绿；MCP 枚举被 15 秒 deadline 约束且超时清 source，context cancellation 会向调用方传播；多 source 对 Registry 消费者的单事务可见性仍 U。当前 `backend-gate` 9/9（全量 Go test 354.8s）、`task frontend:check` 173 files/2765 tests、bindings/docs 均 exit 0；最终完整 packaged build 24/24、exit 0，manifest artifact `e3fdd8608e750a3a2cf432f7939cbfd7af2e6c6f07d1655f2adad4402b91e257`、source `aabfe61ace787475faea4cf7de07faf3ace0ffa0afb00555d90c1ebf308e49ca`、987 files、completedAt `2026-08-16T03:52:56.056Z`、`artifactReused=false`、`sourceFingerprintStableAfterBuild=true`。首次递归实跑保留 `git-rebase-probe` / `0xc0000142` 的 11 passed、1 failed、12 not-run 失败记录；`57c458...` 的 full/reuse 双绿只作为 reuse contract 历史证据。`npm-audit-gate` 仍因 `nanoid` high exit 1；G33 继续 0/6，所有 AC `[ ]`，真实 provider/MCP、Git 其余 mutation、recovery、跨平台 packaged 与 CI/CLI 仍 U。

> **最终证据对账（2026-08-16）**：doc-links、doc-numbers、bindings 与 G33 范围 `diff --check` 均 exit 0；实时递归 inventory 仍为 987 files / `aabfe61...`，与 manifest 匹配。全工作树 `diff --check` 仅因受保护的既有 `build-msi.ps1` 尾部空行 exit 1，未擅自修改。G33 npm gate 仍 exit 1，lock SHA 前后均为 `F1C0AED759A7A5DCCD0C58A2BB685B0C8CC94248D0ED6E36106DA00ADC6AC55F`。

> **本轮最新覆盖（2026-08-16，多 source atomic publication）**：上一条 `aabfe61...` packaged 记录属于历史代码态；当前 `ReplaceSources` 使普通 dynamic refresh 对 Registry 消费者只暴露完整旧/完整新 catalog，workflow/Skill mutation 先整批清空再整批发布，任一 builder 失败时不发布成功子集。mutation 中间态、失败候选与 Registry reader `-race -count=20` 均 exit 0。最终当前代码态 `backend-gate` 9/9（Go test 348.8s）、`frontend:check` 173/173 files/2765 tests；最新 Windows packaged manifest 24/24、artifact `7795a014badc90c7d10f8b23ba17035f85ebe77fa75aeceaf1b5dd1df8a48d01`、source `17f750a156b27ce0f5e3cd7000bce731d203a8862524b31a620151cd0bbd8b27`、987 files、completedAt `2026-08-16T06:02:46.357Z`、`artifactReused=false`、source stable。G33 仍 0/6；AC 全 `[ ]`。G33 npm gate 仍 exit 1（nanoid high），hardening 另一锁图不可替代；H1 原始全平台范围仍存在，H2/H3/H4/M3 在已验证范围内已不存在，真实 provider/MCP/CLI I、恢复、跨平台 packaged/CI 仍 U。

> **本轮最新覆盖（2026-08-16，MCP teardown/TaskService 与最终门禁）**：G33 仍为唯一进行中 Goal、AC 0/6。`services/mcp_service.go`/`mcp_transport.go` 以 `transportLifecycleMu` 串行所有 teardown，stdio transport 由单一 owner 负责 Wait/kill，`Close` 幂等并缓存错误；真实 Windows stdio workflow 链路取得 I 子证据，durable receipt 重载保持同一 UnitID/ExternalReceiptID，Close 后 helper 已退出。`services/task_service.go` 分离 taskkill 子预算与外层 Stop 预算，保留 single-flight/fallback；child PID 原子发布与 workspace outside sibling 测试修复已通过。最终 `node scripts/backend-gate.mjs` 9/9 exit 0（Go test 443.9s），`task frontend:check` 173/173 files、2765/2765 tests exit 0，独立 bindings/docs/numbers exit 0；最新 Windows packaged manifest 24/24 passed、0 not-run、artifact `ef42e7af188ab76fedfd9745231ac3b17c0bae7d6135865dcc92f8cc483af0da`、source `395ac8db26a924b1e4852143807009836b3a791ba98cbb31564ba3aeb0bc624a`（990 files）、completedAt `2026-08-16T13:12:23.677Z`，仅本机 P。`node scripts/npm-audit-gate.mjs` 仍 exit 1（`nanoid <3.3.18` / `GHSA-2v37-7h3g-55p8` / 1 high；lock SHA 前后均 `F1C0AED759A7A5DCCD0C58A2BB685B0C8CC94248D0ED6E36106DA00ADC6AC55F`）；hardening 另一锁图绿灯不可互换。H1 原始全平台范围仍存在（macOS/Reveal/CAS/公共 Workflow pathname loader U），H2 真实 cmd.exe 双路径 I 已关闭；AI provider、跨平台/远端 MCP、recovery/manual-disposition、cross-caller ownership、domain rollback、真实 CI/CLI 与 macOS/Linux packaged 仍 U。

> **P12-G33 AC3 最新复核（2026-08-17）**：进度仍为 `进行中`、AC `0/6`。`services/agent_lifecycle.go` 现在先反向解析 opaque plan/goal runtime owner，并拒绝“durable owner 存在但 runtime 未注册”的 usage retry；恢复处置竞态的 `ErrUnknownSession` fail-closed 断言已补齐。lifecycle/recovery 组 `-race -count=1`、恢复/opaque/indeterminate 矩阵 `-race -count=10`、Agent/Usage/TaskService 组 `-race` 与 `internal/agentcore -race -count=10` 均 exit `0`。既有 full-gate/packaged 记录因源码变化失效，必须重跑；npm G33 `nanoid` high 仍红，H1 原始全平台仍存在、H2 已关闭。详见 prompt-12 §13.24；未修改受保护 GitHub/Docker/lockfile/治理元数据文件。

> **P12-G33 AC4 latest recheck (2026-08-17)**：full backend gate first failed only at `TestAIPermission_RecordUsage_GetSummary` because legacy synthetic UnitIDs collided for same-nanosecond timestamps; atomic sequence suffix plus `TestAIPermission_LegacyUsageIDsRemainUniqueForSameTimestamp` fixed the deterministic boundary. Targeted usage regression `-count=50`, TaskService stop `-race -count=10`, and Rust LSP `-count=3` passed. Full gate must be rerun against this snapshot; G33 remains `0/6`, npm high remains red.

> **P12-G33 terminal failure observation (2026-08-17)**：full-suite diagnosis then found Goal checkpoint-failure usage rejected after a trusted terminal transition. Completed rows now allow only current-incarnation `Success=false` failure observations using durable owner metadata; successful usage remains `ErrInvalidSessionTransition` and runtime authority stays revoked. Goal/terminal/indeterminate retry race matrix `-count=20`, Agent/Usage/TaskService race group, and agentcore race group pass. Full gate remains pending against this latest source.

> **P12-G33 previous full gate / packaged reuse（2026-08-17，历史）**：上述“full gate pending”曾由当前源码复跑覆盖：backend 9/9、frontend 173/173 files 与 2765/2765 tests、bindings/docs 均 exit 0；packaged 首次为 5 passed、1 failed、18 not-run，随后严格 reuse 24/24，artifact/source 为 `4fafb79...` / `c81e6a1...`（998 files）。该历史首次失败与 reuse 记录保留，不覆盖当前 fresh build 证据。
> **P12-G33 current full gate / packaged fresh（2026-08-17）**：当前源码 backend 9/9、`task frontend:check` 173/173 files 与 2765/2765 tests、bindings/docs 均 exit 0；`node scripts/packaged-e2e.mjs` fresh build/fixtures exit 0，manifest 24/24 passed、0 not-run，artifact `4dd46705...`、source `a51f0db2...`（1000 files）、`artifactReused=false`。这是 Windows 本机 P，不升级为跨平台/CI R；G33 仍 0/6，recovery/manual-disposition 只有 backend-only T 子证据，npm `nanoid` high 仍红，H1 原始范围仍未闭环，H2 已关闭。

> **P12-G33 external receipt recovery final shared-tree overlay（2026-08-17，详见 prompt-12 §13.29）**：上条 `4dd.../a51...` 已是历史代码态。当前新增独立 backend-only `AgentExternalReceiptRecoveryDispatcher`，只接受精确 `manual-unknown`；与 session-only `discard` dispatcher 分离，均不注册 Wails。identity key 缺失/损坏、durability unknown、workspace/incarnation/runtime authority 漂移均 fail-closed；真正 resume/adapter compensation 与跨进程 single-writer/CAS 仍 U。当前 backend 9/9（Go test 198.4s）、frontend 173/173 files/2765 tests；fresh packaged 24/24，artifact `55d90e32...`、source `78e8c29a...`（1003 files），Windows 本机 P。npm `nanoid` high 仍红，H1 原始范围仍未闭环，H2 已关闭，G33 仍 0/6。

> **P12-G33 durable workflow attempt overlay（2026-08-17，详见 prompt-12 §13.30）**：§13.29 的 `55d90e32.../78e8c29a...` 现为历史代码态。workflow attempt 不再由 TaskService 内存 map 授权，durable ledger 只接受同 session 唯一 canonical pending row；TaskService 重建、同 UnitID terminal retry、forged/ambiguous pending 拒绝、reload 不恢复旧 runtime authority与并发 Complete 单终态均取得 T。固定 Wails 后 backend 9/9（Go test 206.6s）、frontend 173/173 files/2765 tests；fresh packaged 24/24，artifact `65ca8287...`、source `d6033de3...`（1004 files），Windows 本机 P。cross-caller、真正 resume、domain rollback、stream privacy/retention、跨进程 CAS、真实 CLI/CI/provider 与跨平台 packaged 仍 U，npm `nanoid` high 仍红；H1 原始范围未闭环、H2 已关闭、G33 仍 0/6。

> **P12-G33 AgentService.Close audit/renderer-surface overlay（2026-08-18，详见 prompt-12 §13.31）**：`AgentService.Close` 已标记 `//wails:ignore`，forbidden export/runtime-surface 合约与锁定 Wails `v3.0.0-alpha2.111` 重生成后，`agentservice.ts` 不再导出 Close；root-backed usage/receipt/lock 的写后 identity、sync、poison 与失败 Release 幂等边界取得定向 `-race`/headless CLI 证据。权威 backend 9/9（Go test 219.4s）、frontend 173/173 files/2765 tests、bindings/docs 全绿；fresh Windows packaged 24/24、`artifactReused=false`，artifact `895a351f...`、source `8b0f4d12...`（1020 files），fingerprint 完全匹配。npm `nanoid <3.3.18` / `GHSA-2v37-7h3g-55p8` high 仍红，全局 diff-check 仍受既有 `build-msi.ps1` EOF 空行阻塞；H1 原始范围仍存在、H2 已关闭、G33 保持 0/6，AC 全部 `[ ]`。

> **P12-G33 trusted headless CLI / opaque owner overlay（2026-08-18，详见 prompt-12 §13.32）**：新增未注册 Wails 的 `external-receipts` inventory 与精确 `manual-unknown` 处置命令，输出只含 opaque handle/status/time/result；真实子进程跨重启 recovery、无泄漏、fresh replay 与单 UnitID terminal receipt 通过 `-race -count=3`。Plan/Goal adapters 统一解析 backend-owned opaque runtime session ID。当前固定 Wails 后 backend 9/9（Go test 357.2s）、`task frontend:check` 173/173 files/2765 tests、bindings/docs 全绿；fresh Windows packaged 24/24，artifact `86f89d0b...`、source `91cabc5e...`（1023 files），仅本机 P。`npm-audit-gate` 仍因 `nanoid` high exit 1、lock SHA 未变；H1 原始范围仍存在、H2 已关闭、G33/AC 仍 `0/6`，真实跨平台/CI CLI、resume/compensation、cross-caller/domain rollback、跨进程 CAS、provider 与 macOS/Linux packaged 仍 U。

> **P12-G33 workspace reset owner-map rollback + current shared-tree gates（2026-08-18，详见 prompt-12 §13.33）**：workspace reset 现在在 lifecycle reset 成功后才清理 `sessionOwners/sessionSkills`；`PersistenceNotPublished` 保留旧 runtime 与 caller owner，published-unknown/poison 在 authority 撤销后清理 owner maps。首轮受控红测 `TestAgentWorkspaceResetPrePublicationPreservesRendererOwner` exit 1，修复后该矩阵 `-race -count=5` exit 0；owner/session、agentcore、vet 与 scoped diff-check 均通过。当前后端权威门禁的 gofmt/vet/build/full Go test 300.2s/contract smoke/Wails pin/docs 全通过；自动安装锁定 Wails 因 `proxy.golang.org` 网络失败，使用本机版本匹配 `v3.0.0-alpha2.111` 的 `WAILS3_BIN` 复核 bindings exit 0，`task frontend:check` 173/173 files、2765/2765 tests、ESLint 0 errors/1 existing warning、vue-tsc/bindings/docs exit 0。fresh Windows packaged 24/24、`artifactReused=false`，artifact `83ee98f7ca00b5be4e7fd57703b04df60e80e397a322a23218127134e75ff662`，source `2ab1eb5cce63372cc4200f0693a96b95734c61af68d84ab4418609d5832b44e8`（1024 files），completedAt `2026-08-18T14:16:37.283Z`，仅本机 P；首次 WebView cleanup `EBUSY` 经脚本重试后 exit 0。显式本机 Wails CLI 下随后完整 `node scripts/backend-gate.mjs` 9/9、exit 0（全量 Go test 353.1s）。G33 仍 `0/6`，npm gate 仍 exit 1（`nanoid <3.3.18` / `GHSA-2v37-7h3g-55p8` / 1 high，lockfile stable）；H1 原始全平台范围仍存在、H2 真实 Windows `cmd.exe` 双路径 I 已关闭，跨平台/CI/CLI、recovery compensation、cross-caller/domain rollback、跨进程 CAS 与 macOS/Linux packaged 仍 U。未 commit/push/tag/release。

> **P12-G33 workspace authority/catalog-admission overlay（2026-08-19，详见 prompt-12 §13.34）**：缺口状态保持 H1 原始全平台范围未闭环（Linux T、Windows junction I、macOS/Reveal/CAS/公共 Workflow pathname loader U），H2 真实 Windows cmd.exe 双路径 I 已关闭；G33 仍 0/6，G28~G32 与 P12-BUG-01 未开始。旧实现 admission-window 红测 exit 1，修复后 catalog/Project/lifecycle race 矩阵与固定 Wails 全量 backend 9/9、frontend 173/173 files/2765 tests、bindings/docs 全绿。final fresh Windows packaged 24/24、artifact `f760f16d...`、source `68b492ca...`（1027 files）、`artifactReused=false`，仅 P；npm gate 仍因 nanoid high exit 1，lock SHA 未变。未修改受保护 workflow/Docker/package-lock/治理元数据；无 commit/push/tag/release。

> **P12-G33 legacy MCP renderer execution surface overlay（2026-08-19，详见 prompt-12 §13.35）**：`AgentService.CallMCPTool` 与既有 MCP legacy approval/execute shims 均为 deny-only 且通过 `//wails:ignore`、runtime surface 与 forbidden-export 合约收口；锁定 Wails `v3.0.0-alpha2.111` 重生成后旧入口不再出现在 generated renderer bindings。MCP CRUD/discovery（含 `WorkspaceRoot`）仍明确导出，不能把本条写成整个 MCP surface 移除。legacy deny-only race、runtime surface、binding contract 16/16 与固定 Wails backend gate 9/9（Go test 257.0s）通过；`task frontend:check` 174/174 files、2791 tests、bindings/docs 全绿。fresh Windows packaged 24/24、artifact `4d27d2b9...`、source `4063d6d1...`（1034 files）、`artifactReused=false`、completed `2026-08-19T09:43:26.420Z`，仅 P。npm gate 仍因 `nanoid <3.3.18` / `GHSA-2v37-7h3g-55p8` high exit 1；H1 原始范围仍存在、H2 已关闭、G33/AC 仍 `0/6`，真实 provider/完整 MCP、跨平台/CI/CLI、recovery/domain rollback 与 macOS/Linux packaged 仍 U；未修改受保护 workflow/Docker/package-lock/治理元数据，无 commit/push/tag/release。

> **P12-G33 MCP workspace admission / current static-tree overlay（2026-08-19，详见 prompt-12 §13.36）**：缺口状态保持 H1 原始全平台范围未闭环（Linux T、Windows junction I、macOS/Reveal/CAS/公共 Workflow pathname loader U），H2 真实 Windows `cmd.exe` 双路径 I 已关闭；G33 仍 `0/6`，G28~G32 与 P12-BUG-01 未开始/存在。MCP renderer-reachable stdio 在无 shared workspace identity 时于进程启动前拒绝，canonical executable/workdir、workspace generation、legacy deny-only surface 与 frontend partial-failure reconciliation 已补 T 子证据。固定 Wails backend gate 第二次 9/9（Go test 252.6s）、`task frontend:check` 174/174 files/2792 tests、bindings/docs 全绿；根 `npm run frontend:check` 因无 package.json ENOENT exit 1；npm gate 仍因 `nanoid <3.3.18` / `GHSA-2v37-7h3g-55p8` high exit 1，lock SHA 未变。fresh Windows packaged 24/24、artifact `684190ff...`、source `a97bb30e...`（1034 files）、`artifactReused=false`、completed `2026-08-19T11:14:57.408Z`，仅 P；全局 diff-check 仍受既有 `build-msi.ps1` EOF 空行阻塞。AI provider/stream/output 审查项与完整 MCP 协议、真实跨平台/CI/CLI、recovery/domain rollback、macOS/Linux packaged 仍 U；未修改受保护 workflow/Docker/package-lock/治理元数据，无 commit/push/tag/release。

> **P12-G33 ordinary AI operation admission overlay（2026-08-19，详见 prompt-12 §13.37）**：H1 原始全平台范围仍存在，H2 真实 Windows `cmd.exe` 双路径 I 已关闭；G33/AC 仍 `0/6`。`Send/AIOpChat` 与 `Complete/AIOpInlineCompletion` 现在在 lifecycle/网络前执行 backend-owned Disabled admission，并按 SettingsService `ConfigID` hydration assigned endpoint/key/model；disabled provider hit=0、assigned provider exact-auth/model 与 unassigned global compatibility 的定向与 `-race` 通过。固定 Wails backend gate 9/9 exit 0（Go test 241.5s）；`task frontend:check` exit 0（174/174 files、2792/2792 tests、ESLint/vue-tsc/bindings/docs 全绿）。fresh Windows packaged 24/24、artifact `7d1d9cf4...`、source `c37d6e02...`（1035 files）、`artifactReused=false`、`recordedAt=2026-08-19T14:09:04.000Z`、`completed=2026-08-19T14:10:56.000Z`，仅 P；独立 source fingerprint 重算一致，cleanup `EBUSY` bounded retry 收束。标题、stream target/lifecycle、fallback identity、provider output bounds、frontend config race、真实 provider/CI/CLI 与完整 MCP 仍 U；npm `nanoid` high 仍红，lockfile 未改。

> **P12-G33 ordinary AI title/stream admission overlay（2026-08-19，详见 prompt-12 §13.38）**：`GenerateTitleWithAI`、`StartStream`、`StartAgentStream` 现均在 provider/lifecycle/网络副作用前执行 backend-owned admission；title/stream disabled、assigned endpoint/auth/model、unassigned compatibility 定向与 `-race` 通过。当前 `StartAgentStream` 映射为 `AIOpChat`，`AIOpAgent` 语义仍待明确；renderer target 缺失、caller cancellation/worker retention、fallback identity、provider output bounds、frontend config race、真实 provider/CI/CLI 与完整 MCP 仍 U。固定 Wails backend 9/9（Go test 354.6s）、frontend 174/174 files/2792 tests、bindings/docs 全绿；fresh Windows packaged 24/24，artifact `fab0e53f...`、source `90298aad...`（1035 files）、`artifactReused=false`、completed `2026-08-19T15:02:23.562Z`，仅 P；独立 source/artifact 重算一致。npm `nanoid` high 仍红、lockfile 未改；H1 原始范围仍存在、H2 已关闭、G33/AC 仍 `0/6`，无 commit/push/tag/release。

> **P12-G33 Agent usability / packaged round overlay（2026-08-20，详见 prompt-12 §12.8/§13.40）**：P12-BUG-02 作为 G33 内部强制验收面登记，不并行新建 Goal/工具系统。AI 首 chunk/后续 chunk、provider 明示摘要、Agent 工具批准/执行/结果/observation、跨窗口 durable target 与下一 provider turn 已有 T；Windows packaged 受控 provider `read` 两轮为 P。首次 fresh run 在 24 fixtures 后因 ConversationService 首次 Save 把会话写到 cwd 而 source file set 变化，manifest 正确失败；constructor/zero-value/cwd 伪造红测后修复默认 state root，最终 backend 9/9（Go test 418.2s）与 fresh packaged 24/24 通过。artifact `5bb66931...`、source `864bcd97...`（1045 files）、`artifactReused=false`、completed `2026-08-20T09:58:45.734Z`；Agent UnitID/session/terminal usage、approval 顺序与 observation 均非空且匹配。真实 provider、manual/mutating tools、实际双 WebView I/P、restart ledger、fallback freeze/output budgets、完整 MCP、跨平台/CI/CLI 仍 U；H1 仍存在、H2 已关闭、P12-BUG-01 与 npm high 仍红，G33 AC 继续 `0/6`。无 commit/push/tag/release。

## 9. 每次会话交付模板

```markdown
### 会话交付：P9-Gxx

- 复核结论：缺口仍存在 / 已变化 / 已不存在（附源码与测试证据）
- 本次状态：未开始 -> 进行中 / 阻塞 / 完成
- 改动文件：逐项说明行为变化，不把生成文件与手写源码混淆
- AC：逐条 `[x]/[ ]`，每条附 S/T/I/P/R/U
- 验证：命令、退出码、测试数、真实进程/产物 SHA-256、截图或 CI run
- 首次失败：保留失败原因与修复，不隐藏红灯
- 安全与数据：说明 fail-closed、回滚、迁移和兼容性
- 未验证：明确环境、凭据、平台或历史阻塞，以及可复现步骤
- 下一步：只写当前 Goal 的下一条未完成 AC；完成后才指向下一个 Goal
- SSOT 回写：更新本文 AC 与进度板
```

## 10. 全部 Goal 完成后的最终交付模板

```markdown
## prompt-9 最终交付

### 结论
- 可用性定位：实验性 / 受限日用 / 广泛日用 / 生产级（只能按证据选择）
- 开源发布资格：合格 / 有条件 / 不合格
- 全语言、国际化、插件化、远程能力：分别报告，不合并宣传

### Goal 与证据
- P9-G01 ... P9-G27：AC、最高证据等级、artifact/CI/audit 引用

### 三平台真实矩阵
- packaged 工作流、版本、SHA-256、结果与已知例外

### 质量与风险
- 测试、性能、可访问性、安全、供应链、许可证、SLO、外部审计

### 未完成与禁止声明
- 所有 U、豁免、到期日、owner；任何未完成 AC 均在此列出

### 变更与验证
- 文件索引、迁移/回滚、完整命令与退出码；说明未 commit/push 或明确的发布记录
```

## 11. 一键续作词（2026-08-14：按 prompt-12 转入 G33）

```text
严格按 docs/prompts/prompt-12.md §11/§13 执行，并继承本文件 §0 与 prompt-11 §0 纪律。先复核当前代码与最新 packaged manifest；当前下一 Goal 为 P12-G33，G28~G32 与 P12-BUG-01 不得并行推进主体实现。开始时写明缺口“仍存在 / 已变化 / 已不存在”；一次只推进 G33，先补失败测试，按 AC1~AC6 逐条取得 T/I 证据。AC 未全勾选不得宣称完成；mock/contract 不得冒充 I/P/R；H1 的 Windows/macOS/Reveal/CAS U 与其他安全缺口不得被门禁通过掩盖。绑定必须用仓库锁定版本生成。完成或暂停时按 §9 模板立即回写 prompt-12 §11/§13、本文件 §8 与 prompt-11。不要 commit、push、tag 或发布，除非用户明确要求。
```

---

本文是长期执行清单，不是完成证明。代码、真实运行结果和可审计产物高于本文；但任何较低等级证据都不得被叙述升级。
