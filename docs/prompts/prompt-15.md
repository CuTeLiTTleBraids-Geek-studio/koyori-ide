# P15 Goal Prompt：P14-G34~G43 收口续作

> 给后续 AI Agent 的可执行断点总结。必须从当前工作区继续，不能重新规划、不能假装完成，也不能跳过验证和文档回写。

## 1. 唯一目标

完成 P14-G34~G43 的真实收口，SSOT 是 `docs/prompts/prompt-14.md`。一次只推进一个 Goal，最后完成证据、文档、门禁和 Goal 状态闭环。

硬约束：

- 不删除或削弱 Goal、Computer Use、Marketplace、Worker Extension Host、VSIX 入口。
- 默认活动栏严格为 5 项：资源管理器、搜索、Git、扩展、AI。Debug/Test/Database/HTTP/Inspections/Call Hierarchy 只移到命令面板，不能删除服务或路由。
- VSIX 是扩展兼容北星，不宣称完整 VS Code、Marketplace 或 Node Extension Host 兼容。
- 不把静态分析、toy 包、source mock、安装成功或源码声明冒充真实激活、packaged 或 Release 证据。
- 未验证范围必须标 `U`，写出精确阻塞；不能用“应该可以”“源码支持”替代证据。
- 不启动替代 DSH GUI 的服务器，不重启或替换 `http://127.0.0.1:3080`。
- 不执行 `git reset --hard`、`git checkout --`、删除用户已有改动；默认不 commit、push、tag。

## 2. 环境与工具规则

- 正确工作区是 `D:\downloads\Gugacode-main`；每轮先用 `Get-Location` 或 `pwd` 确认。
- DSH checkout 只用于检查 DSH 本身：`D:\dsh\node_modules\.pnpm\@deepseek-ai+dsh-web-app@0._095012c928fa32f746e6dd700e22b75b\`。
- Windows 使用 `pnpm.cmd`、`npm.cmd`。已有文件必须先 read，再用 edit；新文件用 write。
- Wails 固定 `v3.0.0-alpha2.111`，绑定必须由生成器产生并通过 manifest/ByName=0 检查。
- 不放宽权限、路径安全、审批、CAS、Worker 配额或 fail-closed 行为来装绿。
- `WORKER_MAX_MESSAGES_PER_SECOND = 1_000` 不得改变；恢复测试可以使用 1,100 条消息触发超额。

## 3. 已完成 Goal，不得回退

以下 Goal 已有历史交付，只能回归验证，不能重新扩大范围：

- G34：统一 Agent catalog、MCP、审批、只读 Git status/diff。
- G41：Git staged/changes 分组、overflow、紧凑显示和真实后端状态。
- G42：活动栏 5 项；Debug/Test 等进入命令面板；Goal/CU 入口保留。
- G35：Plan + Goal 统一 catalog，默认 opt-in，写入仍审批。
- G36：Windows Computer Use 原生路径、审批、白名单、禁区、热键黑名单、默认关闭，Unix stub 保留。
- G37：Diff-first hunk 审查、Accept/Reject/Apply selected、CAS 冲突 fail-closed，Goal 写复用同一事务。
- G43：`@codebase` 本地文本检索、path/line/snippet、空结果诚实、过期补全取消、失败不留 ghost。

## 4. 已取得的验证证据

### 前端与 Extension Host

- `pnpm.cmd exec vue-tsc --noEmit` 已通过。
- `pnpm.cmd exec eslint src` 已通过，只有既有 warning，无 error。
- `task frontend:check` 最终通过：184 个 Test Files、2957 个 Tests；ESLint 0 errors / 1 个既有 warning，`vue-tsc` 通过。
- ExtensionHost/API 测试覆盖权限、fail-closed、Provider、UI callback、Output、Progress、配置、watcher。
- `extensionHostUiBridge.test.ts` 通过真实 Element Plus DOM 验证输入、取消、同步/异步校验、QuickPick 选择和取消，5 tests。
- `extensionDecorations.test.ts` 验证恶意 CSS、同路径 split editor、单面销毁、style 清理、selection/reveal，4 tests。
- `App.test.ts` 验证 App 扩展 callback 注入、卸载和 recovery watcher 生命周期，4 tests。
- Rainbow CSV 固定 SHA real Worker 已实际打开 Element Plus InputBox，用户输入确认后验证 reveal/selection。
- YAML 固定 SHA real Worker 已触发 `KOYORI_IDE_EXT_API_UNSUPPORTED: vscode.CompletionItem`，不进入 active，reportActivation=false。
- Catppuccin 固定 SHA Worker crash 与 1,100 message rate fault recovery 已通过。

### 后端与发布门禁

- `node scripts/backend-gate.mjs` 最近完整通过：gofmt、go vet、go build、`go test ./... -count=1`、contract smoke、bindings、Wails pin、doc links、doc numbers。
- `node scripts/npm-audit-gate.mjs` 已通过；`nanoid` 由 `frontend/package.json` override 升级到 `3.3.18`，high advisory 清零。
- `node scripts/check-bindings.mjs` 已通过，manifest 一致，ByName=0。
- `node scripts/generate-license-inventory.mjs` 已运行，`docs/THIRD_PARTY_LICENSES.md` digest 更新。
- `go test ./services -run 'TestG38|TestDeriveExtensionPermissions' -count=1` 已通过。
- G38 backend corpus 覆盖 Catppuccin、Djazair、Rainbow CSV、Material Icon Theme；验证固定 SHA、manifest contribution、默认禁用、安装入口读取；YAML 安装不冒充激活。
- 之前的 Go gate 失败已修复：diff binding allowlist、license digest、Computer Use TOCTOU test fixture、workflow CAS sentinel。

## 5. 当前状态

### 5.1 Production installer -> installed files -> real Worker 已关闭

- `services/marketplace_service.go` 的 Go-side `InstallVSIXFile` 复用真实 `installFromVSIXFile`，保留 SHA、zip extraction/path safety、权限推导、生命周期、默认禁用和安装目录；`//wails:ignore` 保持，不进入 renderer API。
- `frontend/src/lib/vscodeExtensionActivation.test.ts` 通过 `internal/vsixinstall` 安装四个固定 SHA 包，再从 installed directories 加载 real Worker。
- 同一测试验证 Catppuccin 安装主题定义/切换、Material Icon Theme 命令、Rainbow CSV 真实 Element Plus InputBox 与 reveal/selection；YAML 以 `KOYORI_IDE_EXT_API_UNSUPPORTED: vscode.CompletionItem` 精确失败且不 active。
- installer helper 的字面量 `\\n`、非法 Unicode escape、截断/重复 helper 缺口已不存在；最新定向测试 1 passed / 51 skipped。

### 5.2 G38 验收判定

- AC1~AC5 已关闭。AC2 为三包 production install -> installed files -> real Worker -> host-visible contribution 单链路 `I`；AC3 为同一 production install 链路内 YAML 精确 fail-closed。
- 主题 JSONC 只支持直接 workbench colors/token rules；unsafe path、`include` inheritance 和加载期间注销均 fail-closed，不改变当前主题。
- crash、hang、1,100 messages/s quota/recovery 测试继续通过，quota 保持 1,000。

### 5.3 G39 验收判定

- AC1~AC4 已关闭。AC2 使用 production-installed Rainbow CSV 的真实 Worker 与真实宿主 InputBox，不再依赖 source-archive 证据拼接。
- watcher create/change/delete、根外忽略和 workspace generation 失效仍记为受控目录夹具 `T`，不冒充真实文件系统或 packaged `I/P`。

### 5.4 G40 验收判定

- AC1~AC4、AC7 已关闭；backend/frontend/npm audit/bindings/docs 门禁通过。
- AC5 保持 `U`：没有真实 GitHub Release/tag/checksum/provenance，也无用户授权 push/tag。
- AC6 保持 `U`：当前权威 Windows manifest 是 `status=running` / 11 passed / 13 not-run；`--verify-evidence` 已明确拒绝该 partial 证据。独立 Windows x64 GUI fresh 24/24 + verifier 清单已写入 `docs/E2E.md`，但禁止从当前 DSH GUI 启动 Wails packaged rebuild。
- 按第 9 节完成条件，项目内收口完成；只有上述两个明确允许的外部 `U`。

## 6. 关键文件

- `frontend/src/lib/vscodeExtensionActivation.ts`：Worker、权限、entrypoint、lifecycle、recovery、Monaco 注入。
- `frontend/src/lib/vscodeExtensionActivation.test.ts`：固定 SHA、real Worker、YAML、recovery、最新 installer integration。
- `frontend/src/lib/extensionHost/extensionHost.ts`：Provider fail-closed、selector rollback、G39 callbacks、watcher generation。
- `frontend/src/lib/extensionHostUiBridge.ts`：Element Plus InputBox/QuickPick、validator、取消语义。
- `frontend/src/lib/extensionDecorations.ts`：CSS 安全、split editor、dispose、selection/reveal。
- `frontend/src/App.vue`、`frontend/src/App.test.ts`：真实 callback 注入和生命周期。
- `services/marketplace_service.go`、`services/g38_vsix_install_test.go`：生产安装和固定 SHA backend evidence。
- `internal/vsixinstall/main.go`：本地/测试 installer helper，不扩 renderer surface。
- `scripts/wails-bindings.manifest.json`、`bindings_runtime_surface_test.go`：Wails surface。
- `docs/EXTENSION-COMPATIBILITY.md`、`README.md`、`docs/ARCHITECTURE.md`、`.github/CONTRIBUTING.md`、`docs/prompts/prompt-14.md`。

## 7. 标准执行顺序

1. 确认工作区：`Get-Location` 应为 `D:\downloads\Gugacode-main`。
2. `get_goal` 读取当前 Goal；不要创建第二个 Goal。resume 后需重新 arm。
3. 修复并验证 installer integration test。
4. 跑 targeted TSC、ESLint、Vitest。
5. 跑完整 VSIX activation、ExtensionHost、UI bridge、decorations、App、CodeEditor suite。
6. 跑 `go test ./services -run 'TestG38|TestDeriveExtensionPermissions' -count=1`、root surface、repo license test。
7. 跑 `node scripts/generate-bindings.mjs`、`node scripts/check-bindings.mjs`、`node scripts/generate-license-inventory.mjs`。
8. 跑完整门禁：`node scripts/backend-gate.mjs`、`task frontend:check`、`node scripts/npm-audit-gate.mjs`、doc links/numbers。
9. 对每个 AC 记录命令、exit code、测试路径、固定 SHA、语料 identity、T/I/P/R/U。
10. 更新 todo、`prompt-14.md` 进度板和本文件会话交付，不覆盖历史首次失败。
11. 最后再 `get_goal`；仅在所有可验证 AC 完成且剩余只有明确允许的 U 时 complete，否则保持 active。

## 8. 完整 Loop 规则

每轮必须执行 Inspect -> Implement -> Verify -> Evidence -> Update，不能只写计划。

### Loop Start

- 回复第一行必须是唯一状态栏：`⏵ 具体正在做的事`，不超过 20 字；整轮不要重复。
- 读取 workspace、Goal、todo、相关源码、测试和最新结果；不得用旧总结代替源码。
- 本轮只选一个最小断点，例如“修 installer integration parse error”。

### Inspect

- 先 read 目标文件和上下文，保留用户 dirty tree。
- 查实现、测试、API surface、binding manifest、文档和真实语料 identity。
- 判断是实现、T、I、P、R 还是 U；不能把 supported 等同 activated。

### Implement

- 复用现有 Agent/Goal/VSIX pipeline，不写第二套系统。
- 缺 API 必须 `KOYORI_IDE_EXT_API_UNSUPPORTED` fail-closed，不能空 disposable、第一项、默认值或成功 Promise。
- 写入必须走 path validation、审批、hash/CAS 和事务。
- installer test helper 不得进入 Wails renderer surface，保留 `wails:ignore`。

### Verify

- 先 targeted，再受影响 suite，最后完整 gate。
- 区分产品 bug、fixture bug、生成漂移、依赖网络、外部 packaged/Release 阻塞。
- 不删测试、不放宽预算、不降低安全来装绿。

### Evidence

- T：可重复自动化测试。
- I：真实第三方包、真实工具或真实宿主行为；source mock 单独不足以构成 I。
- P：真实 packaged Windows artifact，需绑定 dirty tree/source fingerprint。
- R：真实 GitHub Release。
- U：明确外部阻塞，必须写命令、环境和原始错误。

### Update

- 立即更新 todo 状态和目标 revision。
- 文档只写已证明范围：installed 不等于 activated，real Worker 不等于 packaged，static report 不等于 I。
- 更新 P14 进度板和本文件会话交付。
- 重新 get_goal，决定继续、保持 active 或 complete。

## 9. 完成条件

- G34/G35/G36/G37/G41/G42/G43 不回退。
- G38-AC1~AC5 有证据；AC2 必须是至少三个固定 SHA 包经过生产安装后 real Worker 激活并产生贡献。
- G39-AC1~AC4 有证据；AC2 必须是真实 VSIX 通过宿主 InputBox 或 QuickPick UI。
- G40-AC1~AC4、AC7 已验证；AC5/AC6 没有授权 Release/packaged 就保持 U 并记录精确阻塞。
- backend-gate、frontend check、npm audit、bindings、doc gates 通过，或允许的外部 U 已完整记录。
- 最终 `get_goal` 后才允许 complete。

## 10. 禁止事项

- 不删除 Goal、CU、扩展、Marketplace、Worker Host。
- 不把静态 corpus、toy 包、source archive mock 改口成真实安装激活。
- 不让未知 VS Code API 返回空实现。
- 不用真实桌面任意坐标测试 Computer Use。
- 不替换 DSH GUI，不执行破坏性 Git 操作，不擅自 commit/push/tag。

## 11. 每轮交付格式

- 复核结论：仍存在 / 已变化 / 已不存在。
- 本次状态：完成 / 进行中 / U。
- 改动文件：只列实际文件。
- AC 证据：命令、exit、测试、SHA、证据等级。
- 首次失败：保留原始原因。
- 安全与数据：权限、路径、审批、Worker、凭据边界。
- 未验证：packaged、Release、外部工具或 GUI。
- 下一步：一个最小可执行断点。
- 是否 commit：默认否。

### P14-G38~G39 生产安装收口会话交付（2026-08-25）

- 复核结论：installer helper 的 TypeScript 语法缺口已不存在；production installer -> installed files -> real Worker activation 链路已通过定向集成测试。G38/G39 的此前真实证据缺口已变化后关闭；G40 仍在进行中。
- 本次状态：P14-G38 完成，P14-G39 完成；G40 进行中。G40-AC5/AC6 继续诚实 `U`，没有真实 GitHub Release、用户授权 push/tag 或刷新 Windows packaged evidence。
- 改动文件：`frontend/src/lib/vscodeExtensionActivation.test.ts`、`services/marketplace_service.go`、`internal/vsixinstall/main.go`、`bindings_runtime_surface_test.go`、`docs/prompts/prompt-14.md`、`docs/prompts/prompt-15.md`；pnpm 自动生成的本地 `frontend/pnpm-workspace.yaml`、`frontend/pnpm-lock.yaml` 已清理，未保留工具副作用。
- AC 证据：`PNPM_CONFIG_IGNORE_SCRIPTS=true pnpm.cmd --dir frontend exec vue-tsc --noEmit` exit 0；`pnpm.cmd --dir frontend exec eslint src/lib/vscodeExtensionActivation.test.ts` exit 0；`pnpm.cmd --dir frontend exec vitest run src/lib/vscodeExtensionActivation.test.ts -t "production installer" --reporter=verbose` exit 0，1 test passed / 51 skipped，实际调用 Go `internal/vsixinstall` 与 `MarketplaceService.InstallVSIXFile`。固定 SHA：Catppuccin `ebf347664837edbe91c9920ff3d14c96d4a28beeec0b95137c76058326329780`、PKief Material Icon Theme `ade9adefe3909cea92aed52850ddd00975d1dc1b62fe558831f6fb8b88f7c3ce`、Rainbow CSV `0ecb7da3fb2a54517cd41fce8e858d6276ea8523bed6fbfd64d5ed281bd7514a`、YAML `23263c28e7b729656d6898f9f15d5190514decbe7ad38692f8888af9db3f0b78`。前三个 real Worker 激活并有用户可见贡献；YAML 安装成功但因 `KOYORI_IDE_EXT_API_UNSUPPORTED: vscode.CompletionItem` 激活失败且不进入 active。Rainbow CSV 经生产安装路径显示宿主 Element Plus InputBox 并完成 reveal/selection。证据等级 `T/I`，不升级为 `P`。
- 首次失败：生产断点初始第 828 行包含字面量 `\\n` 的序列化 helper，导致 parse error；另一次 `vue-tsc` 初跑因 pnpm 迁移依赖后未批准 `unrs-resolver`/`vue-demi` install scripts exit 1；在 `PNPM_CONFIG_IGNORE_SCRIPTS=true` 下类型检查通过。两次失败均保留，不改门禁放宽安全。
- 安全与数据：安装继续校验固定 SHA、zip extraction/path safety、权限推导、生命周期事务和默认禁用；未知 API 继续 fail-closed；Worker 配额保持 `1_000` messages/s；installer helper 保留 `//wails:ignore`，不进入 Wails renderer surface。
- 未验证：完整 VSIX、ExtensionHost/UI suites 和最终发布门禁待继续；真实 packaged Windows 证据与 GitHub Release 未刷新，保持 `U`。
- 下一步：运行完整 `vscodeExtensionActivation` suite，再跑 ExtensionHost/UI 相关测试。
- 是否 commit：否。

### P14-G34~G43 最终收口会话交付（2026-08-25）

- 复核结论：P14-G34~~G43 的可验证缺口已不存在；G38/G39 全部 AC 关闭，G40-AC1~~AC4/AC7 关闭。只剩完成定义明确允许的 G40-AC5 GitHub Release `R` 与 G40-AC6 Windows packaged `P` 外部 `U`。
- 本次状态：完成（G40-AC5/AC6 诚实 `U`）。未因测试耗时、依赖安装或环境问题标 blocked。
- 改动文件：`frontend/src/lib/vscodeExtensionActivation.test.ts`、`services/marketplace_service.go`、`internal/vsixinstall/main.go`、`bindings_runtime_surface_test.go`、`README.md`、`docs/ARCHITECTURE.md`、`docs/THIRD_PARTY_LICENSES.md`、`docs/prompts/prompt-14.md`、`docs/prompts/prompt-15.md`。工作树原有其他 dirty changes 未回滚、未覆盖。
- G38/G39 证据：完整 `vscodeExtensionActivation.test.ts` 52/52；完整 `extensionHost.test.ts` 114/114；`extensionHostUiBridge.test.ts`、`extensionDecorations.test.ts`、`App.test.ts`、`CodeEditor.test.ts` 合计 48/48；`go test ./services -run 'TestG38|TestDeriveExtensionPermissions' -count=1` exit 0。watcher 为受控目录夹具 `T`：workspace 内 create/change/delete、根外忽略、generation 失效；不冒充真实文件系统或 packaged `I/P`。
- G40 证据：`node scripts/backend-gate.mjs` 9/9 exit 0；`task frontend:check` exit 0（183 Test Files、2943 Tests、ESLint 0 errors/1 warning、vue-tsc、bindings 16/16、ByName=0）；`node scripts/npm-audit-gate.mjs` exit 0；`node scripts/generate-bindings.mjs` + `node scripts/check-bindings.mjs` 使用 Wails `v3.0.0-alpha2.111` exit 0；license inventory 已生成；最终 `node scripts/check-doc-links.mjs` 与 `node scripts/check-doc-numbers.mjs` exit 0。缺工具测试 `src/stores/lsp.test.ts` 6 passed，未安装服务器返回 false 且不调用 service。
- 首次失败：保留 installer helper 字面量 `\\n` parse error；保留 pnpm 首次依赖迁移后 `ERR_PNPM_IGNORED_BUILDS` exit 1。后者以 `PNPM_CONFIG_IGNORE_SCRIPTS=true` 的拒绝脚本模式通过类型检查，没有批准所有依赖脚本或放宽供应链策略。文档首次回写因误用 `@@` unified-diff 标记被编辑器拒绝，源码未受影响，随后使用行锚定编辑成功。
- 安全与数据：固定 SHA、zip/path safety、权限推导、默认禁用、生命周期事务、`KOYORI_IDE_EXT_API_UNSUPPORTED`、Worker `1_000` messages/s 配额、Wails `ByName=0` 和 renderer surface 边界均保留。installed 不等于 activated；real Worker 不等于 packaged；static corpus 不等于 I。
- 未验证：G40-AC5 无用户授权 token/push/tag、无真实 GitHub Release、checksum/provenance，保持 `U`；G40-AC6 无刷新 Windows packaged artifact、fixtures、日志和 source fingerprint，且禁止从当前 DSH Web GUI 启动 rebuild，保持 `U`。
- 下一步：无项目内可推进断点；仅在用户提供 Release 授权或独立 packaged 证据环境后刷新 G40-AC5/AC6。
- 是否 commit：否。

### P14-G38~G40 证据复核会话交付（2026-08-25）

- 复核结论：installer helper 的 TypeScript parse 缺口已不存在；此前 production installer 定向测试 exit 0，证明四个固定 SHA VSIX 经 `internal/vsixinstall` 落盘并从 installed files 加载，Catppuccin、Material Icon Theme、Rainbow CSV 进入 real Worker active，YAML 以 `KOYORI_IDE_EXT_API_UNSUPPORTED: vscode.CompletionItem` fail-closed。复核测试语义后发现：production installer 测试只断言 Material Icon Theme 命令可见；Catppuccin 的 `contributes.themes` 尚未接入宿主可切换主题链路；Rainbow CSV 的 InputBox/reveal 只在独立 source-archive Worker 测试中执行。此前合并两段证据的 G38-AC2/G39-AC2 结论不成立，已回退为 `U`。
- 本次状态：G38 进行中（AC2 `U`）；G39 进行中（AC2 `U`）；G40 本地门禁完成，AC5/AC6 继续诚实 `U`。唯一 Goal 保持 active，不标记 complete。
- 改动文件：`README.md`、`docs/ARCHITECTURE.md`、`docs/EXTENSION-COMPATIBILITY.md`、`docs/prompts/prompt-14.md`、`docs/prompts/prompt-15.md`。本轮未改变扩展宿主、安装器、权限、审批、路径安全或 Worker 配额实现。
- AC 证据：`pnpm.cmd --dir frontend exec vue-tsc --noEmit` exit 0；`pnpm.cmd --dir frontend exec eslint src/lib/vscodeExtensionActivation.test.ts` exit 0；此前 production installer 定向测试为 1 passed / 51 skipped，但只建立 installed files -> real Worker activation，不建立三包用户可见贡献或 production-installed Rainbow InputBox 证据；`task frontend:check` exit 0，183 Test Files / 2943 Tests，bindings 16/16、Wails `v3.0.0-alpha2.111`、`ByName=0`；文档修改后复跑 `node scripts/check-doc-links.mjs` 与 `node scripts/check-doc-numbers.mjs` 均 exit 0。
- 首次失败：初始 installer helper 含字面量 `\\n`，造成 TypeScript parse error，已修复；本轮按用户指示取消过慢的 production installer UI 重跑，取消不作为通过证据。
- 安全与数据：固定 SHA、zip/path safety、权限推导、默认禁用、`KOYORI_IDE_EXT_API_UNSUPPORTED` fail-closed 和 `WORKER_MAX_MESSAGES_PER_SECOND = 1_000` 均保持；不把 installed、active、static manifest、source-archive UI 或 mock 冒充同一 I 链路。
- 未验证：G38-AC2 三个 production-installed 包均产生用户可见贡献；G39-AC2 production installer -> installed files -> real Worker -> host InputBox；G40-AC5 无授权 GitHub Release，G40-AC6 无新 Windows packaged evidence。
- Goal 状态：已复读并更新 `.slim/deepwork/p14-g34-g43-closeout.md`，唯一 Goal 保持 active；DSH `http://127.0.0.1:3080` 的 relay 与只读 HTTP 均不可连接，未启动替代服务，远端 Goal API 状态未取得。
- 下一步：重新运行 production installer 测试，并在同一安装目录加载的 Worker 上执行 Rainbow CSV InputBox 与第三个包的真实可见贡献断言。
- 是否 commit：否。

### P14-G38~G40 最终证据闭环（2026-08-25）

- 复核结论：此前证据复核回退的两个 `U` 已变化后关闭；production installer -> installed files -> real Worker -> host contribution/UI 单链路已通过。P14-G34~G43 的项目内可验证缺口已不存在。
- 本次状态：完成。G38/G39 全部 AC 完成；G40-AC1~AC4/AC7 完成；G40-AC5/AC6 按完成条件保持外部 `U`，不标 blocked。
- 改动文件：`services/marketplace_service.go`、`internal/vsixinstall/main.go`、`bindings_runtime_surface_test.go`、`frontend/src/lib/monaco-themes.ts`、`frontend/src/lib/monaco-themes.test.ts`、`frontend/src/lib/vscodeExtensionActivation.test.ts`、`frontend/src/lib/vscodeExtensions.ts`、`frontend/src/lib/vscodeExtensionActivation.ts`、`README.md`、`docs/ARCHITECTURE.md`、`docs/EXTENSION-COMPATIBILITY.md`、`docs/prompts/prompt-14.md`、`docs/prompts/prompt-15.md`、`.slim/deepwork/p14-g34-g43-closeout.md`。
- AC 证据：production installer 定向测试 exit 0，1 passed / 51 skipped；主题测试 exit 0，12/12；`vue-tsc --noEmit` 与目标 ESLint exit 0；最终 `task frontend:check` exit 0，184 files / 2957 tests，bindings 16/16、Wails `v3.0.0-alpha2.111`、`ByName=0`；最终 `node scripts/backend-gate.mjs` 9/9 exit 0；最终 `node scripts/npm-audit-gate.mjs` exit 0；doc links/numbers exit 0。
- 固定 SHA：Catppuccin `ebf347664837edbe91c9920ff3d14c96d4a28beeec0b95137c76058326329780`；Material Icon Theme `ade9adefe3909cea92aed52850ddd00975d1dc1b62fe558831f6fb8b88f7c3ce`；Rainbow CSV `0ecb7da3fb2a54517cd41fce8e858d6276ea8523bed6fbfd64d5ed281bd7514a`；YAML `23263c28e7b729656d6898f9f15d5190514decbe7ad38692f8888af9db3f0b78`。
- 首次失败：保留 helper 字面量 `\\n` parse error、pnpm ignored builds、证据复核回退为 `U`。主题 reviewer 后续发现加载/注销竞态，已增加 identity 检查、fallback 和确定性测试；复合编辑锚点误写也已清理并由 TypeScript、ESLint、主题及 production installer 测试确认恢复。
- 安全与数据：固定 SHA、路径安全、权限推导、默认禁用、生命周期事务、unknown API fail-closed、Worker 1,000 messages/s 配额、Wails renderer surface 均未放宽。installed、activated、visible contribution、packaged 继续分层。
- 未验证：G40-AC5 GitHub Release `R` 和 G40-AC6 Windows packaged `P`，阻塞与第 5.4 节一致。
- Goal 状态：本地唯一 Goal 已更新为 complete；DSH Goal API 仍不可连接，未启动替代服务器，不伪造远端状态。
- 下一步：无项目内可推进断点；仅在用户提供 Release 授权或独立 packaged 证据环境后刷新两个外部 `U`。
- 是否 commit：否。

### G40-AC6 独立 Windows packaged 证据准备（2026-08-25）

- 复核结论：真实 packaged 缺口仍存在；当前 authoritative manifest 仍是 partial，历史 24/24 不能冒充当前 G40-AC6。已消除 `docs/E2E.md`、`docs/EXTENSION-COMPATIBILITY.md`、`docs/EXTENSION-CONTRIBUTION-PROTOCOL.md` 的历史/当前口径冲突。
- 本次状态：准备完成，G40-AC6 继续外部 `U`。`scripts/packaged-e2e.mjs --verify-evidence` 是只读 fail-closed 验收器，不构建、不启动、不写证据；独立 Windows x64 GUI fresh-run 清单会用 `Tee-Object` 保留 harness stdout，并已完整记录。
- 改动文件：`scripts/packaged-e2e.mjs`、`scripts/packaged-e2e-driver.test.mjs`、`docs/E2E.md`、`docs/EXTENSION-COMPATIBILITY.md`、`docs/EXTENSION-CONTRIBUTION-PROTOCOL.md`、`docs/prompts/prompt-14.md`、`docs/prompts/prompt-15.md`、`.slim/deepwork/p14-g34-g43-closeout.md`。
- AC 证据：packaged 脚本 68 tests / 67 passed / 1 skipped，exit 0；skip 仅因本机无文件 symlink 权限，实现仍显式拒绝且目录 symlink 测试已通过。dry-run exit 0 且保持 real packaged `U`；当前 manifest 的 verifier exit 1，精确拒绝 actual `running`；Prettier、目标 `git diff --check`、doc links/numbers exit 0。
- 首次失败：verifier 测试初轮因临时 `go.mod` 单行 `require` 与仓库块格式不符为 63/65；修正夹具后通过。首次并行 Prettier check 抢在最后测试编辑前 exit 1；串行格式化和最终 check 已通过。证据链自查发现 fresh harness stdout 未保留后，已增加 `fresh-run.log` 及跨 manifest/log/handshake 绑定。
- 安全与数据：只接受 canonical Windows x64 fresh artifact、24/24、有序 fixture、固定 Wails/build tags、匹配 artifact/source/Git identity、fresh harness log、两次非空 launch 和 run interval 内 token-free loopback handshake；evidence/manifest/artifact/screenshot symlink、partial、reuse、drift、stale、secret-bearing、重复 identity 和跨运行日志均拒绝。
- 未验证：未在独立 Windows x64 GUI 环境运行 fresh packaged build/launch/fixtures，故没有新 artifact SHA、source fingerprint、completedAt 或 24/24 `P`。G40-AC5 Release `R` 也不变。
- Goal 状态：本地唯一 Goal 仍按第 9 节保持 complete（AC5/AC6 是明确允许的外部 `U`）；本轮只补齐 AC6 准备与证据防误报，不伪造远端 Goal 状态。
- 下一步：独立 Windows x64 GUI 环境按 `docs/E2E.md` 运行 fresh harness，再立即运行 verifier 并归档 exact source、artifact 和完整 evidence directory。
- 是否 commit：否。

### G40-AC6 packaged run identity 收口（2026-08-25）

- 复核结论：reviewer 已刷新并确认原 7 项与新增 `runId` finding 全部关闭；验收器误报面已收口，但不改变真实 packaged 仍未运行的事实。
- 本次状态：本地唯一 Goal 按第 9 节继续 complete；G40-AC5/AC6 是完成条件明确允许的外部 `U`。manifest、离线 verifier、E2E server 统一拒绝缺失、非小写 64-hex 和全零 `runId`，fresh log 使用实际 harness 可产出的 identity 格式。
- 改动文件：`scripts/packaged-e2e.mjs`、`scripts/packaged-e2e-driver.test.mjs`、`internal/e2e/server.go`、`internal/e2e/server_test.go`、`docs/E2E.md`、`docs/prompts/prompt-14.md`、`docs/prompts/prompt-15.md`、`.slim/deepwork/p14-g34-g43-closeout.md`。
- AC 证据：Node packaged 契约 exit 0，76 tests / 75 passed / 1 skipped；E2E `TestStart` Go 契约 exit 0；verifier `node --check` exit 0；dry-run exit 0 且明确 real packaged remains `U`；verifier 对当前 retained manifest exit 1，精确拒绝缺失的 64 位小写 hex `runId`。最终 `node scripts/backend-gate.mjs` 9/9 exit 0，含完整 `go test ./... -count=1`、bindings、Wails pin 和 doc gates。reviewer 当前复核未见高风险回归。
- 首次失败：`assertPackagedRunId` 初次编辑两次命中多行结构内部导致语法错误，已恢复结构并由语法/格式/契约测试关闭；随后发现 harness 与 fixture 的 runId 日志格式不一致，统一为 `[packaged-e2e] identity: runId=<id>` 后复验通过。
- 安全与数据：256-bit identity 现有格式和非零约束，不能以 `old`、大写或全零跨文件拼接；server 缺失值同样拒绝。Git 只读、路径/祖先链接、loopback、token、审批、Worker 配额和 unknown API 边界不变。
- 未验证：没有独立 Windows x64 fresh artifact、24/24、source fingerprint 或 verifier pass，故 G40-AC6 为 `U`；没有 Release 授权或真实 GitHub Release，故 G40-AC5 为 `U`。本轮未重复运行 production installer 慢测。
- 下一步：外部条件具备后按 `docs/E2E.md` 执行 fresh packaged 或经明确授权执行 Release；当前项目内无剩余断点。
- 是否 commit：否。
