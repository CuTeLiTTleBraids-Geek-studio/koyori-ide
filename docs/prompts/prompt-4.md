# Koyori IDE 全面代码与产品成熟度审查

审查日期：2026-07-30  
审查对象：当前工作区快照（不含 `.git` 元数据）  
结论口径：区分“本机实际通过”“代码与配置可确认”“当前环境未验证”，不把 README 声明等同于可复现能力。

## 1. 执行结论

Koyori IDE 不是空壳，也不只是 Monaco 外壳。当前代码已经形成一个规模可观的桌面 IDE 原型：Go/Wails 后端提供工作区文件、搜索、Git、PTY、LSP、调试、测试、插件、SSH/SFTP、更新、AI、工作流与安全边界；Vue/Monaco 前端提供编辑、面板、命令、问题列表、测试、调试、终端、扩展和 AI 交互。路径沙箱、命令 capability、扩展默认禁用、CI 跨平台矩阵、签名流程等设计体现了明显的生产化意识。

但当前版本不应被定义为“全语言、全覆盖、可替代 VS Code/JetBrains 的日常集成 IDE”，也不适合直接作企业级商业发布。核心原因不是功能数量不足，而是关键闭环尚未成立：

- 未保存编辑内容没有持久化 hot-exit/crash recovery，进程崩溃可能丢失全部脏缓冲区。
- RemoteService 有真实 SSH/SFTP 实现，但远程项目没有接入编辑器、终端、LSP、Git、调试和测试；当前不是 Remote-SSH 型远端 IDE。
- 默认 Goal 自动执行器是脚手架：固定规划、固定执行 `go env GOOS`、固定失败评估，不能完成真实编码目标。
- 自动快照相关服务以空工作区根构造，项目切换时也未注入根，正常运行中的自动保护会静默跳过。
- 扩展宿主是受限 Worker 兼容层，不是 VS Code Extension Host；通用 DAP、Test Controller、Node/Electron API 和大量 VS Code API 不兼容。
- 内建语言服务器候选仅覆盖 Go、TypeScript/JavaScript、Python、Rust；“Monaco 能高亮某语言”不等于该语言拥有工程模型、索引、重构、调试和测试闭环。
- 缺少打包桌面的真实 E2E；现有 smoke 使用 Node 文件 API 与 mocked Wails 服务，不能证明 WebView、原生对话框、PTY、签名包、升级或崩溃恢复。
- 版本治理存在明显漂移：构建版本仍为 `0.2.0`，CHANGELOG 却按 `0.2.0`、`0.5.0`、`0.4.0`、`0.3.0` 非时间/语义顺序排列，安全策略同时支持多个 0.x 线。

综合判断：**适合作为有真实技术积累的 0.x 开源 IDE 原型继续开发；不建议目前承诺生产、企业支持或 VS Code/JetBrains 替代。** 商业化应先收缩承诺、完成可靠性闭环，再扩语言和插件生态。

## 2. 审查方法与验证状态

### 2.1 本机实际通过

| 检查 | 结果 | 说明 |
|---|---|---|
| `node scripts/check-bindings.mjs` | 通过 | `ByName` 调用为 0，要求的 binding symbols 存在 |
| `node scripts/check-doc-links.mjs` | 通过 | 检查 16 个 Markdown 文件 |
| `node scripts/check-doc-numbers.mjs` | 通过 | `MAX_TOOL_CALLS=20` 与 README 对齐 |
| `node scripts/e2e-smoke.mjs --dry-run` | 通过 | 仅证明 smoke 计划可解析，不证明桌面流程 |
| smoke 文件系统阶段 | 通过（此前运行） | Node 临时目录、读树、打开、保存成功；不是应用服务 E2E |

### 2.2 本机未验证

| 检查 | 状态 | 原因与判定 |
|---|---|---|
| Go build/test/race/vet | 未验证 | 当前 PATH 无 `go`，`go version` 返回 command not found；不能据此判项目失败 |
| Vitest、coverage、ESLint、`vue-tsc`、Vite build | 未验证 | 既有 `node_modules` 损坏且缺少 `.bin`；WSL 与 Windows 原生 `npm ci` 均在挂载目录出现 `ENOTEMPTY`/`EPERM`，无法得到测试结果 |
| npm vulnerability audit | 未验证 | lockfile 指向 `registry.npmmirror.com`，该镜像 audit endpoint 返回 404 `NOT_IMPLEMENTED` |
| 原生桌面启动和打包 | 未验证 | 缺 Go/Wails/GUI 工具链，且没有可用的干净前端依赖树 |
| Git 历史、标签、分支保护、真实 CI 状态 | 未验证 | 工作区不含 `.git`，`git status` 明确失败 |

依赖安装失败是当前工作区/挂载文件系统问题，不是前端测试失败。后续应在本地 Linux 文件系统或干净 checkout 中使用 Node 20 + `npm ci` 重试，并将 npm registry 切到支持 audit 的官方或企业代理端点。

### 2.3 证据等级

- **V（Verified）**：本机命令实际通过。
- **S（Source-confirmed）**：实现、测试或 workflow 在源码中存在，但本机未执行。
- **U（Unverified）**：只有声明、需外部凭据/平台/CI 历史，或受当前环境阻塞。

## 3. 已确认的工程优势

### 3.1 后端不是简单 IPC 包装

- `services/pathsec.go` 集中处理工作区路径、绝对路径和符号链接逃逸，文件、搜索、Agent 等多处复用。（S）
- `services/file_service.go` 有工作区约束和 20 MiB 读取上限，不允许无根写入。（S）
- `services/search_service.go` 有取消、二进制跳过、资源预算、glob 校验、工作区约束和 hash 绑定替换。（S）
- `services/project_service.go` 对多个依赖执行工作区根传播，并在失败时回滚；这是良好的跨服务一致性设计。（S）
- `services/agent_service.go` 的命令授权 token 与 argv、cwd、工作区 generation 绑定，短时、单次使用。（S）
- `services/remote_service.go` 具备 SSH/SFTP、known_hosts、文件操作、轮询 watcher、审批 token、审计和超时，不是假接口。（S）
- `services/marketplace_service.go` 实现 Open VSX/VSIX 下载、hash、解压路径检查和默认禁用。（S）
- `services/update_service.go` 限制 HTTPS/host/大小并校验 SHA-256，同时诚实停在手动安装边界。（S）

### 3.2 IDE 核心能力有实质实现

- Monaco 编辑器、文件树、脏状态、多编辑组、预览、命令、问题列表、格式化、rename、definition、references、signature help、semantic tokens、inlay hints 等桥接存在。（S）
- LSP 是真实进程与 JSON-RPC 客户端，支持初始化、文档同步、诊断、workspace folders、server-to-client request 和配置。（S）
- 终端使用真实 PTY，有多 session、输出上限和生命周期管理。（S）
- 调试存在 Delve DAP 与较窄的 Node CDP 路径，并有 contract/mock 测试。（S）
- Git 覆盖状态、stage/unstage、commit、历史、worktree、rebase、PR 等多类操作，明显超过演示级按钮。（S）
- 测试层同时存在大量 Go 单元测试和大量前端 Vitest 文件；测试资产丰富，但本机未能执行。（S/U）

### 3.3 开源和发布基础较完整

- MIT、贡献指南、行为准则、安全策略、发布文档均存在。（S）
- CI 对 Go 使用三平台 build/vet/race test，并有 golangci-lint、gofmt、goimports、govulncheck。（S）
- 前端三平台执行 typecheck、lint、Vitest 和 blocking high-severity npm audit；coverage 四指标阈值为 50%。（S）
- release workflow 覆盖 Windows amd64、Linux amd64、macOS amd64/arm64，生成 checksum；Windows/macOS 默认要求签名/公证凭据。（S）
- Actions 使用 commit SHA 固定，Wails CLI 与 `go.mod` 版本对齐到 `v3.0.0-alpha2.111`。（S）
- 文档主动说明 Computer Use、IM、更新、远程、扩展、DAP 的能力边界，减少虚假宣传风险。（S）

## 4. 关键问题与风险

### P0：商业发布前必须解决

#### P0-1 未保存内容无法可靠恢复

编辑器的 `content`、`originalContent` 和 `isDirty` 仅存在于前端 reactive store；CrashService 保存崩溃报告和 stack，不保存编辑缓冲区或窗口 session。进程崩溃、WebView 崩溃或强制升级都可能丢失工作。

要求：

- 增加按窗口/工作区持久化的 hot-exit journal，保存 URI、编码、EOL、基线 mtime/hash、脏内容或增量。
- 写入必须原子化、限额、可恢复、可清理，并支持敏感目录排除和用户关闭。
- 启动时检测正常/异常退出，提供逐文件 diff 恢复；恢复完成前不得覆盖磁盘新版本。
- 增加 kill-process、WebView crash、磁盘变更、版本升级的桌面级恢复测试。

#### P0-2 自动快照保护在正常接线中失效

`main.go` 以空 workspace root 构造 Plan、Goal、Diff；这些服务在空根时直接跳过快照，而 ProjectService 的根传播依赖不包含它们。UI 即使显示“自动快照/检查点”，运行时也可能没有保护。

要求：统一实现 `WorkspaceAware`/`WorkspaceContext`，禁止服务各自缓存不更新的根；项目切换成功后所有文件相关服务必须共享同一 generation。增加应用 bootstrap 级测试，验证 AI edit 前确实创建可恢复 snapshot。

#### P0-3 远程项目不是远端 IDE

RemoteProjectWizard 生成 remote project 后，RemoteView 接收处理器没有消费 project 参数，只关闭向导并刷新连接。`Project.Remote` 目前只是元数据；正常 `OpenProject` 仍向 FileService、TerminalService、GitService、SearchService、ToolchainService、LSPService 分发本地路径。

实际边界：

- 编辑器继续调用本地 FileService。
- 终端继续验证本地目录并启动本地 PTY。
- LSP 在本地发现和启动 server。
- Git 使用本地 `git.PlainOpen`。
- debug adapter/目标进程在本地启动。
- 测试/toolchain 在本地执行。

商业发布必须二选一：要么将 Remote 明确降级为“SSH/SFTP 文件与命令工具”，不允许创建看似可编辑的 remote project；要么完成真正 remote workspace host 架构，不能继续在 UI 上形成错误预期。

#### P0-4 默认 Goal 模式不能完成真实任务

默认 planner 返回固定文本，executor 总是执行 `go env GOOS`，evaluator 总是 false；Goal loop 还未把生成 plan 正确转成可执行 step。这是生命周期脚手架，不是 autonomous coding。

要求：在完成真实 planner/executor/evaluator、预算硬限制、每轮安全检查、取消、快照、diff review 和可重放审计前，默认关闭或明确标为 prototype。不能把它作为付费核心能力宣传。

#### P0-5 缺少真实桌面 E2E 和可审计发布证据

`scripts/e2e-smoke.mjs` 的文件步骤直接调用 Node API，前端步骤使用 mocked Wails service。它不能证明打包应用能打开目录、编辑 Monaco、使用 PTY、调用 LSP、调试、重启恢复或升级。

要求：至少建立一个 Linux 必选 packaged-app smoke，并逐步增加 Windows/macOS：启动签名/打包产物，打开 fixture，修改并保存，运行 terminal/test，触发 LSP definition/rename，关闭并恢复脏文件。release job 应依赖相同 commit 的 required CI workflow，而不是 tag push 后仅重新 build。

### P1：日用 IDE 阻断项

#### P1-1 语言覆盖远低于“全语言”

当前内建 LSP 候选为：

| 语言 | Server 候选 | 当前判断 |
|---|---|---|
| Go | `gopls` | 最成熟路径 |
| TypeScript/JavaScript | `typescript-language-server` / `vtsls` | 有真实 server matrix，但非 PR gate |
| Python | `pyright-langserver` | 有候选与配置，缺本审查中的真实矩阵证据 |
| Rust | `rust-analyzer` | 有候选与配置，缺本审查中的真实矩阵证据 |
| 其他语言 | 无内建 adapter manifest | Monaco 高亮不能替代 IDE 支持 |

“全语言”不应通过在 Go switch 中继续硬编码 server 达成。需要数据驱动的 Language Pack SDK，统一描述扩展名、language id、root markers、server transport、install/resolve、capabilities、formatter、linter、test adapter、debug adapter、tasks 和配置 schema。

#### P1-2 扩展生态不能承接语言广度

当前 Worker host 明确不提供完整 Node、Electron、VS Code API、generic DAP、Test Controller 和普通 webview 语义。它适合受控插件，不足以直接复用成熟 VS Code 生态。

建议不要追求逐 API 克隆 VS Code。优先建立 Koyori IDE 原生、版本化、可测试的 Language/Debug/Test Extension API；VSIX 兼容作为独立适配层，只声明经过自动兼容测试的 extension/version 组合。

#### P1-3 搜索与重构缺少大型工程索引层

文本搜索具备良好的安全与资源边界，但主要是有界文件扫描；symbol index/LSP 能提供部分语义能力，却不是持久、增量、跨语言的工程索引。Rename 和 code action 依赖 server，缺少 IDE 自身对 workspace edit 的统一事务、冲突、文件操作与 undo 模型。

要求：建立统一 `WorkspaceEditTransaction`，支持 text edits、create/rename/delete、版本/hash precondition、预览、partial failure rollback、undo journal。大型工作区需要增量文件索引、ignore 规则、watcher backpressure、缓存失效和可观测性。

#### P1-4 snapshot restore 不是精确回滚

全量恢复只重写 snapshot 中记录的文件，不删除 snapshot 后新增文件。因此“恢复到快照”实际是内容回填，不是工作区状态精确还原。UI 和文档必须改名或实现 manifest diff + 新增文件删除确认。

#### P1-5 Agent 工具调用上限是告警，不是硬预算

前端达到 `MAX_TOOL_CALLS` 后仍可继续排队和审批。虽然危险命令仍需要 backend capability，但成本和失控循环上限没有真正执行。预算必须由后端会话状态强制，不能仅依赖 renderer 常量。

#### P1-6 调试交互存在行为错误

Step In target UI 获取并展示 target，但调用时丢弃选中的 `targetId`，最终执行默认 Step In。应让 backend/store API 接收 targetId，并添加多 target contract test。

### P2：规模化与商业运营风险

#### P2-1 版本与支持策略漂移

- `build/config.yml` 产品版本是 `0.2.0`。
- CHANGELOG 在 `0.2.0` 后列 `0.5.0`、`0.4.0`、`0.3.0`，版本顺序与日期不可信。
- SECURITY 同时写 0.4.x 支持、0.3.x 高危、0.2.x 当前发行、0.5.x 开发。
- release workflow 可对任意 `v*.*.*` tag 构建，但没有在 build 前检查 tag、build config、CHANGELOG、应用 runtime 版本一致。

要求：单一版本源；CI 生成其他平台 metadata；tag release 强制校验 SemVer、版本一致性、CHANGELOG section、支持策略；不得回发较低版本覆盖高版本支持线。

#### P2-2 发布供应链仍有缺口

- SBOM 是 `continue-on-error`，且 runner 未明确安装 Syft，正常可能始终跳过。
- Linux 没有签名/仓库元数据/AppImage 签名路径。
- Windows/macOS 可通过 repository variable 明确关闭签名，允许 unsigned release。
- release matrix 不含 Windows arm64、Linux arm64。
- checksum 提供完整性，不提供发布者身份或 provenance。
- workflow 未生成 SLSA provenance，也未证明可复现构建。

0.x 可接受部分降级，但商业下载页必须逐 artifact 显示签名、notarization、SBOM、commit、builder 和 checksum 状态。1.0 前应将 SBOM 与 provenance 设为 mandatory。

#### P2-3 CI 配置强，但真实状态无法从快照证明

workflow 中多数质量门禁是 blocking，这是优势；但缺 `.git` 导致无法确认是否在 GitHub 实际绿灯、是否被 branch protection 要求、是否已有成功 tag release。发布决策必须引用 workflow run URL 和 artifact verification，不应仅引用 YAML。

#### P2-4 性能门禁不阻断回归

benchmark job 只报告 benchstat，不设置退化阈值；若 baseline 缺失，还在临时 runner 中复制一个不会提交的 baseline。应针对启动、工作区扫描、搜索、LSP latency、内存、terminal throughput、large file 建立稳定样本和预算，并在显著退化时失败或要求审批。

#### P2-5 Wails v3 alpha 是平台和长期维护风险

Go module 与 CLI 固定版本是正确做法，但所有 0.x 仍依赖 alpha runtime。需要维护升级窗口、平台回归套件、WebView 版本矩阵和退出策略；商业 SLA 不应建立在未验证的 alpha 升级路径上。

### P3：产品与治理完善项

- 建立公开 roadmap、compatibility matrix、known issues 和 release health dashboard。
- 对遥测、崩溃上传、AI provider 数据流、日志脱敏、保留周期提供明确隐私策略和 opt-in。
- 增加许可证/NOTICE 自动生成、依赖许可证策略和第三方组件归属检查。
- 建立签名插件发布、撤销、恶意扩展响应和 marketplace moderation 流程。
- 增加企业配置：代理、私有 CA、离线安装、策略锁定、密钥托管、审计导出、禁用不可信工作区能力。
- 建立 accessibility 自动检查和键盘/屏幕阅读器人工验收矩阵。

## 5. 功能成熟度矩阵

评分：0=无；1=stub；2=原型；3=可用但有明显边界；4=稳定日用；5=成熟生态/企业级。

| 领域 | 分数 | 依据与主要差距 |
|---|---:|---|
| 编辑器基础 | 3 | Monaco、多 tab/组、格式化、诊断；缺 hot exit、强恢复和大型文件证据 |
| 工程/工作区模型 | 3 | 多根与 transactional root propagation；主要仍是本地路径模型 |
| 文本搜索替换 | 3 | 安全边界强、可取消；未形成大仓库持久索引 |
| 语义索引与重构 | 2 | 依赖少数 LSP，缺统一 workspace-edit transaction 与广泛重构 |
| LSP | 3 | 真实 JSON-RPC、多根与丰富 provider；真实 server gate 较窄且非 PR 必选 |
| 调试 | 2.5 | Delve DAP + Node CDP；非通用 adapter 平台，存在 Step In target bug |
| 测试/覆盖率 | 2.5 | Go/Vitest 路径和 UI 存在；adapter 生态窄，桌面闭环不足 |
| 终端 | 3 | 真实多 session PTY；不可恢复、非远端、缺 shell integration 深度 |
| Git/SCM | 3 | 功能面较广；尚无大型仓库、凭据、复杂冲突的运行证据 |
| 扩展平台 | 2 | 安全受限 Worker 有价值；远低于 VS Code Extension Host 兼容度 |
| 远程开发 | 1.5 | SSH/SFTP backend 可用；未接入 IDE 主链路 |
| AI chat/agent | 2.5 | streaming、审批和 capability 设计较强；Goal 与自动快照闭环失效 |
| 恢复与韧性 | 1.5 | crash report、手动 snapshot；无 dirty-buffer hot exit，restore 非精确 |
| 安全工程 | 3.5 | 沙箱、SSRF、密钥、CSP、审计、CI 扫描较完整；未构成外部审计结论 |
| 发布工程 | 3 | 多平台、checksum、签名流程；SBOM 可选、版本漂移、真实 artifact 未验证 |
| 企业治理 | 1.5 | 有安全政策；缺 SLA、策略管理、遥测/隐私、生态治理证据 |

## 6. 目标架构：从功能集合到全语言日用 IDE

### 6.1 核心原则

1. **Workspace URI 优先，不再以本地 path 为系统边界。** 所有文件、watch、process、LSP、Git、debug、test API 使用 `WorkspaceURI` 和 host identity。
2. **本地和远端共享同一协议。** UI 只连 Workspace Host；本机 host 可 in-process，SSH/container/cloud host 使用版本化 RPC。
3. **语言能力数据驱动。** Language Pack 声明 server、formatter、linter、test、debug 和配置，不在核心代码持续增加语言 switch。
4. **所有多文件修改事务化。** AI、rename、code action、format-all、search replace 都进入同一 edit transaction、preview、precondition、undo 与 recovery journal。
5. **扩展能力最小且版本化。** 先保证原生 API 稳定和安全，再做 VS Code compatibility adapter；不以 API 名称相似冒充兼容。
6. **能力声明来自测试矩阵。** 每个语言包、debug/test adapter、OS/arch、remote host 和 extension version 都由自动 contract/integration 结果驱动文档。

### 6.2 建议组件边界

```text
Desktop Shell (Wails)
  -> Workbench UI (Vue/Monaco/xterm)
  -> Host Client (versioned RPC, cancellation, streaming, tracing)

Workspace Host (local / SSH / container / cloud)
  -> Workspace FS + watcher + content hash
  -> Process/PTY + environment + port forwarding
  -> Project graph + file index + symbol index
  -> SCM provider
  -> Language broker (LSP lifecycle/routing)
  -> Debug broker (DAP/CDP adapters)
  -> Test broker (discovery/run/coverage protocol)
  -> Task broker
  -> Edit transaction + journal + snapshot/hot exit

Extension Runtime
  -> Signed package + manifest + API version
  -> Language/Debug/Test/SCM/UI contribution points
  -> Capability grants + process/network/filesystem isolation
  -> Compatibility lab for selected VSIX packages
```

### 6.3 Language Pack 最小契约

每个语言包必须声明并通过以下分层状态，不能只用“支持/不支持”二值：

- L0 Text：language id、扩展名、语法、注释、括号、indent。
- L1 Tooling：formatter/linter、root markers、安装与版本检测。
- L2 Intelligence：LSP initialize、sync、diagnostics、completion、hover、definition、references。
- L3 Refactor：rename、code actions、workspace edits、imports、symbols、semantic tokens。
- L4 Run/Test/Debug：task template、test discovery/run/coverage、debug adapter。
- L5 Production matrix：local/remote、三平台、large workspace、restart/recovery、版本兼容范围。

第一方首批建议：Go、TypeScript/JavaScript、Python、Rust。第二批按用户需求加入 Java/Kotlin、C/C++、C#、PHP、Ruby、Lua。每增加一个语言必须增加真实 server fixture 和版本矩阵，而不是只增加 executable name。

## 7. 分阶段路线图

### Phase 0：可信基线（0-6 周）

目标：停止新增表面功能，恢复可重复验证能力。

- 在原生 Linux 文件系统/干净 checkout 修复并跑绿 Go、frontend、coverage、audit、Wails build。
- 修复自动 snapshot 根接线、Goal 默认实现/默认开关、Step In target、snapshot 精确语义。
- 实现脏缓冲区 hot exit MVP 和异常退出恢复。
- 统一版本源并校验 tag/build/CHANGELOG/SECURITY。
- 让 required CI checks 真正加入 branch protection；保存 workflow run 和 release evidence。
- 将 smoke 名称改为 contract smoke，并增加首个 packaged desktop E2E。

退出条件：三平台 required CI 绿；Linux packaged E2E 绿；kill/restart 不丢未保存内容；版本一致性 gate 绿；P0 缺陷均有回归测试。

### Phase 1：稳定本地 IDE（6-14 周）

目标：四种第一方语言达到稳定日用，而不是快速增加语言数量。

- 实现 WorkspaceContext、统一 URI、edit transaction、undo/recovery journal。
- 建立 Language Pack manifest/SDK，把现有四语言迁移出硬编码路径。
- 对 Go/TS/Python/Rust 建真实 LSP、format、lint、test matrix。
- 完善外部文件变化、encoding/EOL、large files、watcher backpressure、索引取消和缓存。
- 建统一 Test Adapter 和 Debug Adapter contract；修复 debug 生命周期与 reconnect。
- 建启动时间、idle memory、搜索、索引、LSP latency 性能预算。

退出条件：四语言 L3，至少 Go/TS 达到 L4；10k/100k 文件 fixture 有预算；崩溃恢复与多文件重构可回滚；Windows/macOS packaged smoke 上线。

### Phase 2：真正远程与生态（14-28 周）

目标：远端和本地使用同一 Workspace Host 协议。

- 把 FS、PTY、process、LSP、Git、debug、test 移入可部署 host。
- 实现 SSH bootstrap、host version negotiation、断线重连、端口转发、远端日志和安全升级。
- 扩展 API 提供 Language/Debug/Test/SCM contribution；发布示例和 compatibility test kit。
- 建选定 VSIX 的版本白名单矩阵，不承诺全量兼容。
- 增加 Java/C++/C# 等第二批语言包，由真实用户场景排序。

退出条件：remote fixture 能完成 open/edit/save/terminal/LSP/test/debug/reconnect；同一语言包在 local/remote 通过相同 contract suite；扩展崩溃不影响 workbench。

### Phase 3：商业与企业就绪（28-44 周）

目标：可承诺支持，而不仅是可下载。

- 稳定 runtime 依赖或完成 Wails alpha 风险退出计划。
- 强制签名/notarization、SBOM、provenance、恶意依赖与许可证 gate。
- 建 delta update、失败回滚、channel、灰度、崩溃率监控和 release health。
- 增加代理、私有 CA、离线源、策略锁定、审计、数据驻留和 secret provider。
- 发布隐私政策、支持政策、生命周期、CVE 流程和 enterprise SLA。
- 执行外部渗透测试、供应链审计、可访问性审计和恢复演练。

退出条件：连续多个版本满足 crash-free、升级成功率、性能和安全 SLO；所有 release artifact 有 provenance/SBOM/签名状态；支持团队可按 runbook 复现和处理故障。

## 8. 商业化 Go/No-Go 门槛

### 可作为开源预览版发布

前提是继续明确标为 0.x preview，并保持以下限制可见：Wails alpha、四语言有限支持、remote 非完整 IDE、VSIX 部分兼容、Computer Use stub、IM outbound-only、更新手动安装、Goal prototype。

### 不应作为稳定个人版发布

在以下条件满足前为 No-Go：

- hot exit 与 crash recovery 未通过杀进程测试。
- packaged desktop E2E 未覆盖 open/edit/save/terminal/LSP/restart。
- 自动 snapshot 接线和 Goal 行为未修复或未关闭。
- 版本/更新/签名状态不能由 artifact 证据证明。
- 干净 checkout 的 required CI 不能稳定重现。

### 不应作为企业版发布

除个人稳定版条件外，还必须有：remote host 闭环、策略与审计、代理/私有 CA、离线部署、数据与 AI 隐私控制、SBOM/provenance、签名强制、漏洞响应演练、支持 SLA、兼容矩阵和至少两个稳定版本周期的数据。

## 9. 建议立即创建的验收测试

1. `BootstrapWorkspaceSnapshotIntegration`：打开项目后执行 AI edit，断言 snapshot 在正确 root 创建且可恢复。
2. `DirtyBufferKillAndRestore`：输入未保存内容，强杀进程，重启后恢复并显示磁盘冲突 diff。
3. `WorkspaceEditAtomicity`：跨文件 rename 中注入 hash 冲突，断言无 partial write 或可完整 rollback。
4. `RemoteWorkspaceCorePath`：SSH fixture 中完成打开、保存、terminal、LSP、Git status、test；在架构完成前该测试应明确 skipped/unsupported，而非假绿。
5. `GoalCompletesFixture`：在固定小仓库中生成补丁、运行测试、评估成功，并验证预算、安全审批和 snapshot。
6. `PackagedDesktopCorePath`：对实际 Wails artifact 驱动 UI，不 mock binding。
7. `ReleaseVersionConsistency`：tag、build metadata、runtime、CHANGELOG、支持表必须一致。
8. `LanguagePackContractMatrix`：每个声明的层级能力都需真实 server/adapter 证据。
9. `ExtensionCrashIsolation`：恶意/崩溃 Worker 不得访问未授权路径、阻塞 UI 或残留 process。
10. `UpdateFailureRollback`：损坏包、错误 hash、无签名、安装中断和新版本启动失败均有可预测结果。

## 10. 最终优先级

最小正确推进顺序：

1. 先恢复干净、可重复、可审计的构建测试环境。
2. 修复数据丢失、自动快照、Goal 假闭环和版本治理。
3. 建 packaged desktop E2E，证明真实产品路径而非 mock 路径。
4. 把本地 IDE 的四语言做到稳定、可恢复、可度量。
5. 抽象 Workspace Host 与 Language Pack，再做真正远程和更多语言。
6. 最后投入企业治理、供应链、更新和商业 SLA。

如果反过来继续增加面板、语言名称或 VSIX API stub，功能数量会增长，但与成熟 IDE 的差距不会缩小。当前最重要的产品指标应从“有多少功能”改为“用户工作是否不会丢、关键流程是否真实可复现、声明是否由兼容矩阵证明”。
