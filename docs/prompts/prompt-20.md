# P20 Goal Prompt：PR 收敛、CI 恢复绿与 P19 残留收口

> 本文件是独立 Goal，不继承 `prompt-19.md` 或 `prompt-a.md` 的任务状态、完成声明和验收结论。它记录 2026-08-31 对当前仓库（分支 `release/v0.2.0`，HEAD `bf002dc`，remote `origin = github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide`）的一轮只读审查结果与整改目标。本轮审查方法：代码级逐条核查 + 自动化测试本地重跑 + GitHub Checks/PR 状态经 `gh api` 核实；**未使用真实 UI 自动化**。执行本 Goal 前必须读取 `docs/prompts/prompt-a.md` 并遵守其证据纪律。

## 1. 唯一目标

把三块缺陷收敛到可验证的完成态：

1. **GitHub PR 面收敛**：16 个 open PR（15 个 dependabot 升级 + 1 个 hardening PR #38）全部获得明确处置（合并 / 关闭 / 转专项 issue），不允许长期红 check 挂着。
2. **CI 恢复绿**：默认分支 `main` 与收敛后的集成 HEAD 上全部 required job 绿；packaged-e2e 指纹门禁修复或显式豁免。
3. **P19 残留收口**：个人路径清零 + 守卫正则加固（P19 AC-06 唯一未达成项），以及 P19 审查发现的相邻安全残留（marketplace 相邻 SSRF、IM 未知 Type fail-closed、goroutine recover 策略）。

整改不得破坏 P19 已验证行为（见第 2 节红线），不得把 P19/P16 遗留 U 项改写为完成。

## 2. 审查基线（2026-08-31 已验证事实，整改不得使其回退）

### 2.1 本轮验证命令与结果（在 release/v0.2.0 工作树上全部通过，`T`）

- `go vet ./services/... ./internal/... .` → 0 错误（exit 0）。
- `go build ./...` → 0 错误。
- `go test ./services -run 'TestGit' -count=1 -p 1` → ok。
- `go test ./services -run '^TestAIServiceNativeToolStreamingRoundTripHTTP$|^TestAIProviderStreamBoundary|TestMCP|TestG03MCP' -count=1 -p 1` → ok。
- `go test . -run TestRegisteredWailsRuntimeSurfaceMatchesManifest -count=1` → ok。
- `cd frontend && npx vitest run` → 全绿；`npx vue-tsc --noEmit` → 通过。
- `node scripts/check-bindings.mjs` → OK（pinned v3.0.0-alpha2.111，manifest 与生成树一致，ByName=0）。
- `node scripts/check-bindings-imports.mjs` → OK（16 registered exceptions，无 violation）。
- `node scripts/check-package-manager.mjs` / `node scripts/check-personal-paths.mjs` → OK（但注意 2.3 节：守卫 OK 与真实残留并存）。
- `git status` → 干净（0 未提交文件），31 个提交已推送 `origin/release/v0.2.0`。

### 2.2 P19 整改逐项核查结论（file:line 级，均为属实；整改不得回退）

P0-01 版本控制收口、P0-02 IM Webhook SSRF（`im_service.go:127` SSRF 传输、`:124` no-redirect、`:288-292` URL 变更撤销审批、`:450-460` 企微 `key` query 真实发送）、P0-03 单一包管理器（`package.json:6` packageManager + `scripts/check-package-manager.mjs`）、P1-01 三处 stale 竞态（git.ts 五函数 / DiffView.vue / mcp.ts generation 守卫 + 11 个回归测试）、P1-02 pprof 沙箱（`pprof_service.go:75-84` 统一经 `pathsec.go:85` ValidateMutatingPathWithinRoot）、P1-03 Legacy 审批管线删除（agentcore 唯一权威、mcp deny-only 面无损）、P1-04 marketplace 下载复验（`marketplace_service.go:1104` 漏斗 + no-redirect）、P1-06 依赖卫生、P1-07 binding 分层（sanctioned registry + CI 强制）——**8 项完成属实，且各有真实 fixture 测试**。P1-05 部分完成（见 2.3）。不得删除或弱化上述任何守卫与测试。

### 2.3 残留缺陷清单（本 Goal 的输入）

**A. CI 状态（`gh api` 核实，2026-08-31）**

- `ci.yml` 的 `on.push`/`on.pull_request` 仅覆盖 `main`/`master`；`release/v0.2.0` 的 push 不触发 CI，全部 12 个 run 均为 `workflow_dispatch` 手动触发且全部 conclusion=failure（645c8c0 起连续失败）。
- 最新 release run `33314002949`（bf002dc）：唯一失败 job = **Packaged desktop E2E (Linux qualification)**，失败原因 `[packaged-e2e] FAIL Error: source fingerprint changed during build`（`scripts/packaged-e2e.mjs:434` 源指纹守门检出构建改动源树；bf002dc 修复尝试未生效）。其余 required job 该 run 全部 success。
- main 最新 run `33379160392`（30bae15）：**NPM Audit + 3× Frontend Check & Test 失败**，根因 `npm audit` 报 nanoid <3.3.18 高危（GHSA-2v37-7h3g-55p8）；main 的 lockfile `node_modules/nanoid` 为 **3.3.17**。另 **LSP real-server matrix (optional)** 失败、Govulncheck 在 PR #24 上出现 FAILURE（需在当前 main 上复诊是否新 Go advisory）。
- release/v0.2.0 的 lockfile 已含 `overrides: {"nanoid": "^3.3.18"}` 且解析为 3.3.18——**修复已存在于 release 线，未到达 main**。

**B. 分支漂移**

- merge-base `45f37e7`；release 领先 28 个提交（全部 P16/P19 产品与安全工作），main 领先 24 个提交（依赖升级线：Wails go.mod → v3.0.0-beta.5（PR #11+#20）、@wailsio/runtime → 3.0.0-beta.5、vue 3.5.41、vitest 4.1.10、typescript-eslint bumps、lockfile regen #23）。
- **main 缺全部 P16/P19 产品代码**（无 `typecheck` script、无 packageManager、无 nanoid override、无个人路径守卫、无 P16 AI/agent/MCP/git/extension 工作）——README 描述的功能与 main 代码不匹配，这是开源观感的结构性问题。
- 依赖线冲突面：`go.mod`/`go.sum`（alpha2.111 vs beta.5）、`frontend/package.json` + `package-lock.json`、`.github/workflows/ci.yml`（release +150 行 vs main 未含 P19 job）、`scripts/lib/wails-bindings.mjs` + `scripts/wails-bindings.manifest.json` + `check-wails-pin.mjs`（CLI pin 与 go.mod 联动）。

**C. 16 个 open PR 与 check 状态（2026-08-31 快照）**

| 类 | PR | 内容 | 失败形态 | 初步预判（执行时必须复诊） |
|---|---|---|---|---|
| 实质 | #38 codex/github-hardening | 78 文件：治理/CI/release fail-closed 契约（CODEOWNERS、dependabot grouping/cooldown、SBOM/license/installer 契约） | SUCCESS×21，Go Lint FAILURE，CANCELLED×1 | 唯一实质 PR，近乎绿；注意本地同名分支与 origin 相差 ahead 3/behind 3，必须以 PR head 为准 |
| Go 常规 | #25 x/crypto 0.55.0；#40 modernc sqlite 1.57.0 | 例行升级 | 失败为 base 继承（NPM Audit 等） | rebase 后应可合 |
| 前端常规 | #35 element-plus 2.14.4；#33 highlight.js 11.12.0；#31 @vueuse/core 14.4.0 | 例行升级 | 同上 | rebase 后应可合 |
| 前端 major | #37 globals 15→17；#36 jsdom 29→30；#28 eslint-plugin-vue 9→10 | major，需配置兼容工作 | 8 failures（含自身 frontend job） | 逐个跑通 vue-tsc/vitest/eslint 再合；不行则关闭并转 issue |
| 前端异常 | #34 @vitest/coverage-v8 4.1.10 | **14 failures 含 Go Build/Go Lint——coverage 包不可能破坏 Go**，疑似 stale 分支基于旧 main | 14 failures | 先 `@dependabot rebase` 再复诊 |
| Actions major | #24 checkout 4→7；#27 setup-go 5→7 | workflow YAML | 5 failures（base 继承）+ #24 上 Govulncheck FAILURE 需复诊 | 评审 major 兼容性（node runtime）后合 |
| 高危升级 | #26 typescript 5.9.3→**7.0.2**（tsgo 线）；#39 Wails go.mod beta.5→**beta.12** | major 跨大版本 | 14 / 11 failures | #26 默认关闭转 issue（vue-tsc 兼容性未知）；#39 与 bindings CLI pin 联动，转专项，禁止在本 Goal 内顺手合 |
| 冲突项 | #30 @types/dompurify 3.0.5→3.2.0 | 与 P1-06 冲突（该包已判定废弃并从 release 删除） | 8 failures（base 继承） | **直接 close** + dependabot ignore；不得合入 |

**D. 个人路径残留（P19 AC-06 未达成项，5 处，全部实锤）**

`docs/prompts/prompt-14.md:21`（双反斜杠 `C:\\Users\\<name>` 形式）、`docs/prompts/prompt-7.md:690` 与 `docs/prompts/prompt-8.md:40`、`:521`（WSL `/mnt/c/Users/<name>` 形式）、`build/scripts/finalize-release-0.2.0.sh:4`（`/home/<name>` 形式）。守卫 `scripts/check-personal-paths.mjs:38` 正则仅覆盖单反斜杠/正斜杠 `C:Users` 两类，上述三种形式全部放行——守卫 OK 与真实残留并存的实证。修复守卫时必须让这三种形式可被检出。

### 2.4 误报防止记录（防止下一轮误信）

- P19 审查中"仓库提交了约 140MB 构建产物"的误报仍不成立：`git ls-files` 1132 个跟踪文件，`*.exe`/`bin/` 被 `.gitignore` 正确覆盖。
- 本项目**不使用 Pinia**，store 为模块级 `reactive()` 单例；不要基于 Pinia 假设。
- `agent_execution_core.go:1299` 的 `root, _ = workspace.Snapshot()` 仍在（P2 项，未修，不算缺陷回退）。
- MCP 侧 `executeApprovedToolLegacy`（`mcp_service.go:662-697`）为测试桩死代码（生产不可达），P2 可清退，非 P1-03 回退。
- PR check 计数是快照，dependabot rebase 后会变；处置前必须以 rebase 后的最新 run 为准。

## 3. 当前缺陷与整改要求

### 3.1 P0：立即执行

#### P0-01：main 基线修复（最小热修，先于一切收敛动作）

1. 在 main 上补 `frontend/package.json` 的 `overrides: {"nanoid": "^3.3.18"}`（与 release 线同值）并重新生成 lockfile；`npm ci` + vitest + vue-tsc 全绿后单独提交推送，确认 main 的 NPM Audit 与 3× Frontend Check job 转绿。
2. 复诊 Govulncheck 在 PR #24 上的 FAILURE：确定是新发布的 Go advisory 还是陈旧 run；若是新 advisory，按最小升级修掉并在报告记录 CVE/advisory 编号。
3. LSP real-server matrix (optional) 失败原因查明并记录（optional job 不阻塞，但需给出是环境缺失还是真实回归的结论）。

#### P0-02：分支收敛（本 Goal 最重的任务）

要求：把 `release/v0.2.0` 的 28 个 P16/P19 提交并入 `main`，使默认分支重新成为"产品代码 + CI 门禁"的唯一真线。

1. **依赖线决策（必须先做并写入报告）**：推荐收敛到 **alpha2.111 线**（release 线）——它是唯一有完整 P16~P19 测试证据的线；main 的 beta.5 从未跑过产品代码的测试。若选择相反方向（beta.5 线），必须先在 beta.5 上重跑第 2.1 节全部命令并重新生成 bindings/manifest，任何失败即回退到推荐方向。无论哪个方向，`check-wails-pin.mjs` 的 pin 联动与 `bindings_runtime_surface_test.go` 必须保持一致。
2. 冲突面清单：`go.mod`/`go.sum`、`frontend/package.json`/`package-lock.json`（以收敛线为准重新 resolve，保留 overrides + packageManager + typecheck script）、`.github/workflows/ci.yml`（必须同时保留 release 侧 P19 job：personal-path guard、bindings import layering、package-manager check，与 main 侧既有 job）。
3. main 上被 #11/#20 引入的 beta.5 若被回退为 alpha2.111，必须在合并提交信息中写明理由；后续 Wails 升级走 P3.1-C 的专项通道。
4. 收敛后 `git status` 干净、新 main HEAD 上 CI 全部 required job 绿（attach run URL）；第 2.1 节全部命令在新 HEAD 重跑记录。

#### P0-03：16 个 open PR 逐个处置（在 P0-01/P0-02 的新 base 上）

按 2.3-C 表执行，每条 PR 记录决定（merge / close / defer-to-issue）+ 依据 + 复诊后 check 状态：

1. 先逐个 `@dependabot rebase`（或手动 rebase），以新 run 判定，不沿用快照。
2. 常规 bump（#25/#40/#35/#33/#31）验证后合并；Actions major（#24/#27）评审 runner/node 兼容性后合并。
3. 前端 major（#37/#36/#28）逐个跑通 `typecheck`+`vitest`+`eslint` 再合；失败且修复成本高则 close 并开 issue 记录目标版本。
4. **#30 直接 close**（与 P1-06 冲突：@types/dompurify 属废弃包，dompurify 自带类型）；在 dependabot 配置中 ignore 防止重开。
5. **#26 关闭转 issue**（TypeScript 7/tsgo 与 vue-tsc 兼容性需专项验证，禁止顺手合）。
6. **#39 关闭转专项 issue**（Wails CLI pin + bindings regen + manifest + surface 测试联动的整体升级，见 P1-05）。
7. **#38 单独走 P1-04 流程**（不得在本任务里顺手合）。
8. 处置完成后 open PR 集合 = 有意保留项，且无红 check 的"死 PR"。

#### P0-04：packaged-e2e 源指纹门禁修复

现状：连续 dispatch 失败于 `source fingerprint changed during build`（bf002dc 修复尝试无效）。要求：本地复现（`node scripts/packaged-e2e.mjs --dry-run` + 全量跑），定位构建期间改动源树的具体步骤（疑似 bindings 生成或某 fixture 写回 tracked 路径），把该写入产物移到 `build/` 之外或纳入指纹豁免清单并说明理由；修复后手动 dispatch 一次并附绿 run URL。若判定该门禁本身设计过严（例如指纹范围误含生成物），允许修改 `scripts/packaged-e2e.mjs` 的指纹输入集，但必须同步更新 `packaged-e2e-driver.test.mjs` 并在报告记录理由。

### 3.2 P1：本轮必须完成

#### P1-01：个人路径清零 + 守卫加固（P19 AC-06 收口；必须在 P0-02 收敛合并前完成，避免把残留带入 main）

1. 修 2.3-D 列出的 5 处残留（占位符化，保留证据语义）。
2. `scripts/check-personal-paths.mjs:38` 正则加固：覆盖 `C:\\Users\\`（转义形式）、`/mnt/c/Users/`（WSL）、`/home/<segment>/`（Linux home）三种形态；allowlist 语义保持；为每种形态补自测用例（`check-personal-paths.test.mjs`）。
3. `T`：修复后守卫对修复前的残留样本（可用 git show 历史文本构造 fixture）全部报 FAIL；全仓跑守卫 exit 0。

#### P1-02：marketplace 相邻 SSRF 残留收口

P1-04 已收口 downloadUrl 主漏斗，但同源残留三处：① `resolveSha256` → `httpGetBytes`（`marketplace_service.go:1881-1883`）走默认 client 跟随重定向且无 dial-time 复验；② `GetExtensionReadme`（`:2320-2345`）的 readmeURL 完全无校验；③ VSIX 下载 client 未用 `NewSSRFSafeTransport`（对照 `ai_urlsec.go:187-239`）。要求：三处统一过 `ValidateNonPrivateURL` + no-redirect + SSRF-safe transport；`T`：registry 返回私网 readme URL / 302 跳私网时的拒绝测试。

#### P1-03：IM 未知 provider Type fail-closed

`im_service.go:442-461` 的 token switch 无 default 分支：未知 `Type` 的 `BotToken` 被加密存储但从不发送，也无 `ErrNotAllowed`——与 wechat_work 原缺陷同类。要求：`UpdateConfig` 增加 Type 白名单（或 sendToProvider switch default 显式 `ErrNotAllowed`）；`T`：未知类型保存或发送的 fail-closed 测试。

#### P1-04：PR #38 合并专项

1. 以 origin 的 PR head 为准（本地 `codex/github-hardening` 与 origin 相差 ahead 3/behind 3，先对齐丢弃本地漂移）。
2. 修 Go Lint 失败；在收敛后的新 main 上 rebase（ci.yml 三方冲突必须同时保留 P19 守卫 job 与 #38 的 fail-closed 契约，禁止静默放宽任一侧）。
3. 评审要点：CODEOWNERS/dependabot grouping 的取值是否合理；其"H1/H2 证据范围"声明是否与 `docs/E2E.md` 口径一致。
4. 合并后确认 dependabot grouping 生效方式与后续 PR 处置策略一致。

#### P1-05：goroutine recover 策略

全库生产代码 38 处 `go func()` 无任何 `recover()`，单点 panic 会击穿整个桌面进程。要求：不做无差别撒网——为事件泵/流 worker/服务后台循环三类长生命周期 goroutine 增加顶层 recover 钩子（可挂接既有 crash_service），panic 转结构化错误事件并 fail-closed；短生命周期、有 defer 包裹语义的不动。`T`：至少一条"goroutine panic 不击穿进程且上报错误"的测试。

#### P1-06：Wails 升级专项通道（不执行升级，只立规矩）

为未来 beta.12（原 PR #39）立验收门：升级必须同步 bump CLI pin（`check-wails-pin.mjs`）、重新生成 bindings + manifest、更新 `wails-bindings.mjs` forbidden/required 策略（如受影响）、重跑第 2.1 节全部命令 + surface 测试。在 `docs/` 下记录该清单（一份即可，避免重复）。

### 3.3 P2：可选（不阻塞本 Goal 完成）

- 前端：`git.ts` 的 `loadBranches`（:212）/`checkRebaseStatus`（:347）补同类 generation 守卫；`mcp.ts:698-713` unsupported 分支同步写入补 seq 检查；`main.ts`/`workspaceStore.ts` 调试 console 残留清理。
- 后端：`mcp_service.go:662-697` `executeApprovedToolLegacy` 测试桩死代码清退；`agent_execution_core.go:1299` `Snapshot()` 错误改为日志 + fail-closed 评估。
- 仓库外观：README Linux 依赖改为 CI 实装的 `libgtk-4-dev libwebkitgtk-6.0-dev`；`frontend/package.json` 补 `engines.node >=20`；GitHub 仓库补 description/topics；打 `v0.2.0` tag（VERSION=0.2.0 但仓库只有 `beta0.2.0` tag）；`build/scripts/` 一次性发布脚本加"一次性记录"头注。
- `docs/prompts/prompt-19.md` §8 追加执行轮结果摘要（不改写 U 项）。

## 4. 验收标准

- **AC-01 main CI 绿**：收敛后新 main HEAD 上 ci.yml 全部 required job 绿（attach run URL）；NPM Audit 无 high/critical。
- **AC-02 PR 清零**：16 个 open PR 每条有 merge/close/defer 决定 + 理由 + 复诊后 check 证据；关闭项均有对应 issue（#26/#39）或 dependabot ignore（#30）。
- **AC-03 分支唯一真线**：收敛后 main 含全部 P16/P19 工作；release/v0.2.0 与 main 无实质漂移（或 release 已删除/归档并记录决定）；第 2.1 节命令在新 HEAD 全绿。
- **AC-04 隐私清零**：跟踪文件中个人路径 0；守卫正则覆盖三种形态且有自测；守卫对历史残留样本 FAIL。
- **AC-05 packaged-e2e**：dispatch run 绿（run URL），或指纹输入集修改 + 测试同步 + 理由记录。
- **AC-06 SSRF 收口**：marketplace 三处残留 + IM Type fail-closed 全部落地并各有拒绝测试。
- **AC-07 PR #38 合并**：lint 修复、rebase 到新 main、P19 守卫与 #38 契约共存、合并后 CI 绿。
- **AC-08 recover 策略**：三类长生命周期 goroutine 有顶层 recover + 测试。
- **AC-09 诚实边界**：P19 §2.3 的 U 项（MCP UI smoke、Git UI smoke、AC-08 真实运行、外部 provider、packaged/跨平台）不被改写为完成；证据类型沿用 `T`/`I`/`P`/`U`。

## 5. 执行策略

顺序严格为：**P1-01（个人路径，必须在任何 docs 提交与收敛合并前）→ P0-01（main 热修）→ P0-02（分支收敛）→ P0-03（PR 处置）→ P0-04（指纹门禁）→ P1-02~06 → P2 按余力**。每个断点执行 Inspect → Implement → Verify → Evidence → Update。PR 处置前必须 rebase 复诊，不沿用本文件的快照结论。第 2.2 节列出的 P19 已验证行为是回归红线：收敛或 rebase 导致 2.1 节任一命令失败即停止并修复。禁止把多个 PR 的合并揉进同一个提交；合并依赖升级 PR 后必须跑对应受影响测试组。

## 6. 未完成边界与禁止事项

1. 不得使用"源码存在""组件存在""binding 已生成""CI 单 job 绿"替代整体激活证据；contract-smoke 绿不代表 packaged E2E 绿。
2. 不得把 P19 §2.3 的 U 项改写为完成；packaged-e2e 绿也不等于"packaged/release 已验证"。
3. 不得引入 Pinia 等新状态框架；不得基于 Pinia 假设重构。
4. 不得删除或弱化：`mcp_service.go` deny-only 面与 `//wails:ignore`、`scripts/lib/wails-bindings.mjs` forbidden/required 策略、`check-bindings-imports.mjs` sanctioned registry、`check-package-manager.mjs`、`check-personal-paths.mjs`（只能加固不能放宽）、P19 新增测试。
5. Wails 升级（alpha→beta 或 beta 小步）不得绕过 P1-06 的验收门。
6. 不得用 force-push 抹除 PR/dependabot 分支历史来"修复"检查；dependabot 分支用评论命令 rebase。
7. 不得在未跑第 2.1 节回归的情况下声明收敛完成；不得把 #38 的 fail-closed 收紧项静默放宽以换取绿。
8. 本 Goal 范围不含真实 UI smoke、外部 provider 与跨平台验证；不得声称完成。

## 7. 交付证据格式

每个 AC 记录：具体命令、测试名称、run URL 或操作步骤；改动文件清单（file:line）；结果类型 `T`/`I`/`P`/`U`；失败时保留原始错误与影响范围。PR 处置表逐行附决定与证据。最终报告必须列出仍未完成能力清单与本 Goal 新引入的已知限制。

## 8. 执行状态

本轮（2026-08-31）仅完成只读审查与 2.1 节自动化验证，未做任何整改；16 个 open PR 与两条分支的漂移状态原样保留，作为 P0 的输入。下一轮实现从 P1-01 开始（顺序见第 5 节）。

## 9. 执行日志（P20 整改轮，2026-08-31）

### P1-01：个人路径清零 + 守卫加固（对应 AC-04）— 状态：`complete`

- 实现：
  - `scripts/check-personal-paths.mjs:47-53`：单正则改为三形态正则组——`c:[\\/]{1,2}users[\\/]{1,2}<segment>`（Windows 原始 + 转义双反斜杠）、`/mnt/c/users/<segment>`（WSL 挂载）、`/home/<segment>`（Linux home）；allowlist 机制保持，新增 `user`/`alice` 两个既有 fixture 名（frontend locales 占位符 `frontend/src/lib/locales/en.ts:2427`、`services/operational_events_g27_test.go:97`），真实用户名不进 allowlist。
  - `scripts/check-personal-paths.test.mjs:25-45`：新增"三形态残留样本全部 FAIL"与"占位符/allowlist fixture 在新形态下放行"两组自测（样本用部件拼装，避免测试文件自命中）。
  - 5 处残留占位符化（保留证据语义）：`docs/prompts/prompt-14.md:21`（转义 Windows 形态 → `<用户名>` 段）、`docs/prompts/prompt-7.md:690`、`docs/prompts/prompt-8.md:40`、`docs/prompts/prompt-8.md:521`（WSL 形态 → `<用户名>` 段）、`build/scripts/finalize-release-0.2.0.sh:4`（Linux home 绝对路径 → `L="$HOME"/...`，语义等价且脚本可执行）。
  - `services/agent_service_test.go:305`：注释散文 "root, home, all"（斜杠连写形式）触发新形态误报，改写为顿号分隔（仅注释，无行为变更）。
- `T`：
  - 加固后、修复前：`node scripts/check-personal-paths.mjs` → exit 1，精确报告 5 处真实残留（`finalize-release-0.2.0.sh:4` Linux home 形态、`prompt-14.md:21` 转义 Windows 形态、`prompt-7.md:690`/`prompt-8.md:40`/`prompt-8.md:521` WSL 形态，匹配 segment 均为真实用户名）——守卫对修复前残留全部 FAIL 的直接证据。
  - 修复后：`node scripts/check-personal-paths.mjs` → OK，exit 0（全仓跟踪+未忽略文件清零）。
  - `node --test scripts/check-personal-paths.test.mjs` → 5 pass / 0 fail。
  - `go test ./services -run 'TestCheckCommand' -count=1` → ok（6.2s，注释改动无回归）。
  - `bash -n build/scripts/finalize-release-0.2.0.sh` → 语法通过。
- 证据类型：`T`（本地自动化验证；CI run 见收敛轮统一 dispatch）。
- 剩余边界：守卫覆盖文本文件（`textExtensions`/`textBasenames` 白名单扩展名）；二进制与忽略路径不在扫描面（沿 P19 既定 scope）。
