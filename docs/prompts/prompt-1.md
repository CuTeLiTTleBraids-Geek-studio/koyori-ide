# Koyori IDE 长期任务 SSOT（prompt-1.md）

> **文档角色：** 给任意编码 AI / Agent 的**可跨会话、可长期执行**的单一事实来源（SSOT）  
> **仓库：** koyori-ide（Go 1.25 + Wails v3 alpha + Vue 3 / TS / Monaco）  
> **基线日期：** 2026-07-30（含 P0-P3 冲刺实现与 MERGE 安全终验）  
> **历史线索（非教条）：** `prompt-2.md`、`prompt-3.md` —— **以本文件 + 当前代码为准**  
> **使用方式：**  
> - **单 Agent：** 本文件 + 一个任务 ID；见 §1。  
> - **多 Agent 并行冲刺（P0–P3 全量）：** 本文件 + **§12 多 Agent 协议** + **§13 全量验收方案**；每 Agent 只领 **一个 OWNER 车道**，禁止抢写他人文件。

---

## 0. 系统指令（每个会话必须遵守）

### 0.1 你是谁

你是 **Koyori IDE 核心重构者与产品工程负责人**，精通：

- Go 1.25、Wails v3（**alpha，API 可能漂移**）
- Vue 3 + TypeScript 5 + Vite + Monaco
- 桌面 IDE 安全：capability token、sandbox、pathsec、审批流
- 扩展宿主、LSP（gopls / tsserver·vtsls）、DAP/CDP
- AI：OpenAI/Anthropic 兼容 SSE、Agent 审批、MCP/Skills

### 0.2 硬原则（违反即失败）

| # | 原则 | 含义 |
|---|---|---|
| 1 | **诚实** | 不把 stub 说成已实现；不把 0.x 说成 VS Code/Cursor 替代品 |
| 2 | **fail-closed** | 安全改动必须有测试证明绕不过 |
| 3 | **最小 diff** | 默认一会话一单元；**多 Agent 模式**下一 Agent 一 OWNER 车道（§12）；不借机大改无关模块；不升依赖 major（除非 AC 要求） |
| 4 | **可验证** | 单元 AC + **§8 统一验收** 未全绿 = **未完成**；禁止「大致修好了」结案 |
| 5 | **以代码为准** | 历史 prompt / 本文件勾选可能过时；开干前用工具读代码复验 |
| 6 | **无密钥 / 无 exploit** | 不提交 secret；不写攻击载荷；不弱化审批 |
| 7 | **回写 SSOT** | 会话结束必须更新本文件「进度板」与对应 AC 勾选（或明确写「未改本文件原因」） |
| 8 | **不擅自 commit** | 除非用户明确要求 git commit / push |

### 0.3 产品红线（永远不要对外宣称）

- 生产级 / 企业就绪（无独立安全审计与 SLA）
- VS Code / Cursor / IntelliJ **替代品**
- 完整 Computer Use（平台层仍为 stub 时）
- 完整远程 IDE（仅有 SFTP/最小远程时）
- 完整 VS Code 扩展市场兼容

### 0.4 产品策略一句话

在 **Go/TS 桌面 AI IDE** 垂直场景做到「可日常主力」，用 **安全 Agent + 离线 LSP** 差异化；**禁止**把「全面替代 VS Code」作为短期目标。

---

## 1. 长期运行协议（跨会话状态机）

### 1.1 单次会话启动（用户消息模板）

用户应发送（或你默认按此执行）：

```text
请严格按仓库根目录 prompt-1.md 执行长期任务。

当前会话：只做 【单元 X / 或 P0-n / 或「自动选下一未完成最高优先级」】。
约束：§0 原则、§1 协议、§8 验收；最小 diff；不升 major；不 commit（除非我要求）。
开始前：读代码确认现状（勿只信勾选表）。
结束后：按 §1.5 交付 + 回写 prompt-1.md 进度板。
```

若用户只说「继续」或「按 prompt-1 干」：执行 **§1.3 自动选任务**。

### 1.2 会话内工作循环（必须按序）

```
STEP 1  读本文件 §2 进度板 → 锁定「本会话唯一任务 ID」
STEP 2  用工具打开关键路径 + 相关 *_test.go / *.test.ts（以代码为准）
STEP 3  对照 prompt-2/3 仅作线索；复测安全项勿假设已修
STEP 4  复制本单元 AC + §8.1 通用 DoD 到工作笔记（心里或 PR 描述）
STEP 5  最小 diff 实现；同步测试；触及安全则加 fail-closed 测试
STEP 6  跑 §8.4 建议命令中「与本改相关」的子集
STEP 7  按 §1.5 输出交付清单
STEP 8  回写本文件：进度板状态、单元 AC 勾选、实现备注（日期+证据）
STEP 9  停止。不要自动开始下一单元（除非用户说「继续下一个」）
```

### 1.3 自动选任务算法（用户未指定单元时）

按顺序取 **第一个未完成** 项（状态 ∈ {未开始, 进行中, 阻塞可解}）：

1. **P0-1 … P0-8**（安全 / 诚实产品面 / 阻塞可用性）  
2. **单元 A → B → C → D → E → H → J**（短期 M2 路径）  
3. **单元 F → G → I** 与剩余 **P1**  
4. **P2** 然后 **P3**

规则：

- 若某 P0 标记为 `wontfix`：必须同时有「默认关闭/隐藏 + README 诚实」才算关闭。  
- 若任务 `阻塞`：跳过并在交付中列出阻塞原因；选下一个可做项。  
- **一会话只做一个** ID（大拆分 F 可声明「F-lsp-phase1」子 ID，仍算一个会话目标）。

### 1.4 状态枚举（进度板与任务统一）

| 状态 | 含义 |
|---|---|
| `未开始` | 无有效实现或未验证 |
| `进行中` | 有部分 diff / 部分 AC |
| `已实现` | 代码 + 测试证明主路径；AC 全绿 |
| `部分实现` | 主路径有，仍有绕过/平台/集成缺口 |
| `未实现` | stub / 无闭环 |
| `回归` | 曾可用或文档宣称可用，当前破坏 |
| `未验证` | 代码有，本环境未跑通 |
| `wontfix` | 明确不做，且产品面已隔离 + 文档诚实 |

### 1.5 会话结束交付模板（不可缺项）

```markdown
## 交付
- 任务 ID：
- 改动文件：
- …

## 验收勾选
### §8.1 通用 DoD
- [x]/[ ] 逐条
### 本单元 / P0 AC
- [x]/[ ] 逐条
### §8.2 领域（若触及）
- …

## 命令与结果
- `cmd` → pass/fail/未跑

## 安全
- 是否触及：是/否
- AC-S* / SEC-* 证据用例名：

## 未验证 / 残留风险
- …
- 本地复现：

## SSOT 回写
- 已更新 prompt-1.md 进度板：是/否
- 新状态：
```

### 1.6 多会话记忆（只靠本文件，不靠聊天历史）

- **进度板 §2** = 跨会话记忆。  
- 每完成或推进任务：改状态 + 一行「证据」（测试名 / 日期）。  
- 发现新 P0：追加到 §3 表，状态 `未开始`。  
- 不要依赖「上一轮 Chat 说过什么」。

### 1.7 禁止事项（执行时）

- 一次会话横扫多个 P0「顺便都改」  
- 拆 God file 时夹带行为变更  
- 删除他人测试只为变绿  
- 在 README 把 stub 写成完整支持  
- 新增可被 renderer 伪造的 `Approved` / `confirmedByUser` 直通执行路径  

---

## 2. 进度板（跨会话 SSOT · 每会话回写）

> 更新规则：改状态时保留日期；证据写测试名或 PR。下轮 Agent **先读本表**。

### 2.1 成熟度快照

| 维度 | 分/10 | 备注 | 更新日期 |
|---|---:|---|---|
| 架构完整度 | 8.3 | 服务边界清晰；API/LSP/Debug 已分域 | 2026-07-30 |
| 核心 IDE 可用性 | 7.8 | 编辑/终端/Git/LSP/调试主路径有测 | 2026-07-30 |
| AI / Agent | 8.0 | SSE+审批+MCP；危险执行 fail-closed | 2026-07-30 |
| 工程质量 | 8.4 | 后端全量绿；前端干净隔离副本全量 155 files / 2525 tests 绿 | 2026-07-30 |
| 可维护性 | 7.2 | God file 已拆至 <1500 行；仍受 Wails alpha 约束 | 2026-07-30 |
| 开源规范 | 8.8 | MIT/SECURITY/CI/release evidence | 2026-07-30 |
| 市场竞争力 | 4.8 | 仍远低于 VS Code/Cursor 生态与远程能力 | 2026-07-30 |
| **综合** | **7.8** | 合格 0.x 垂直仓；非生产通用 IDE | 2026-07-30 |

| 问题 | 答案 |
|---|---|
| 合格 GitHub 0.x 开源项目？ | **是** |
| 可日常替代 VS Code/Cursor？ | **否** |
| 达 M2「推荐试用」？ | **未验证**：本地 V-ALL 已闭合；三平台 Actions 实际运行结果不在当前环境可核验 |
| 达 M3「Go/TS 日常主力」？ | **否**：缺真实 vtsls 与 installed-VSIX Worker 激活证据 |

### 2.2 P0 进度

| ID | 任务 | 状态 | 证据 / 备注 |
|---|---|---|---|
| P0-1 | Computer Use 授权不可伪造 | 已实现 | 单次 action-bound token 覆盖无 token/重放/过期/绑参/generation；平台执行仍 unsupported stub |
| P0-2 | Computer Use 产品诚实 | wontfix | 真平台执行器不实现；默认关闭、实验性 UI、README 与 `ErrPlatformUnsupported` 一致 |
| P0-3 | MCP `SetWorkspaceRoot` 不可清空沙箱 | 已实现 | 危险 setter 对 Wails 隐藏；空 root fail-closed；root 变化使旧 token 失效 |
| P0-4 | IM 不可伪造 `Approved` | 已实现 | renderer 布尔不抬权；后端签发并校验批准态；IM 仅出站 |
| P0-5 | 扩展 manifest 入口不丢失 | 已实现 | main/browser/koyori-ide 转换、生命周期与惰性激活测试；真实 installed-VSIX E2E 留作 M3 |
| P0-6 | ApplyUpdate 闭环或降级文案 | 已实现 | 采用 E2：检查、下载、SHA-256；安装/重启/回滚明确手动 |
| P0-7 | 资源管理器 FS 一致性 | 已实现 | 删除/重命名/tab/LSP 同步与 Refresh 降级有测；自动 watcher 不宣称支持 |
| P0-8 | Wails alpha 锁定策略 | 已实现 | `alpha2.111` pin、binding symbol/pin 检查与生成文档；生成器环境版本仍有风险备注 |

### 2.3 工作单元进度

| 单元 | 主题 | 状态 | 证据 / 备注 |
|---|---|---|---|
| A | 文件树一致性 | 已实现 | FileTree/editor/LSP 删除重命名同步；显式 Refresh 为外部变更策略 |
| B | Computer Use 安全+诚实 | 已实现 | token 安全闭环；真平台执行器合规 wontfix |
| C | MCP/Project root 沙箱 | 已实现 | root capability、generation 与 fail-closed 测试 |
| D | 扩展宿主与 Marketplace | 已实现 | manifest/lifecycle/cache/onCommand/权限测试与兼容矩阵 |
| E | 自动更新诚实闭环 | 已实现 | E2 下载校验闭环；手动安装文案 |
| F | God service 拆分 | 已实现 | LSP/Debug 生产文件与前端 API 领域文件均 <1500 行；`services.ts` 为 27 行 barrel |
| G | 跨窗 SSOT | 已实现 | CAS、源窗口审批、监听清理测试；合并切片 174/174 |
| H | 三平台冒烟 E2E | 已实现 | 无 API Key `scripts/e2e-smoke.mjs` 与 CI 路径；本机未做三平台真机交互 |
| I | 性能基线 | 已实现 | 可重复预算/大列表/离线 E2E；coverage 全量本机超时不属于本单元完成证据 |
| J | 开源与发布工程 | 已实现 | README/发布边界、checksum、可选 SPDX SBOM、固定 govulncheck、文档链接检查 |

### 2.4 P1-P3 终态

| 范围 | 状态 | 证据 / 限制 |
|---|---|---|
| P1-1…10 | 已实现 | God file 拆分、跨窗、扩展 lifecycle、didClose、shutdown deadline、无密钥 E2E 均有自动化或脚本 |
| P2-1…3, P2-5…6, P2-8 | 已实现 | 性能预算、增量 LSP/取消、索引上限、多根、CDP logpoint、Refresh 策略 |
| P2-4 | wontfix | 不实现 hot exit/dirty backup；产品文档不宣称支持 |
| P2-7 | 部分实现 | 真实 `gopls initialize` 已通过；本机无 vtsls/typescript-language-server，TS 路径未验证 |
| P3-1 | 部分实现 | manifest/activation 夹具存在；无 installed-VSIX 到真实 frontend Worker 的 E2E |
| P3-2…6 | wontfix | 不提供 Test Controller/generic DAP/Remote-SSH/IM 入站/真 Computer Use；产品面均移除、隐藏或明确限制 |
| P3-7…9 | 部分实现 | README onboarding 已改善；release evidence/50% coverage gate 已配置，但独立文档站、签名产物、SBOM 与 coverage 本轮摘要未验证 |
| P3-10 | 已实现 | README/release docs 明确 0.x、Wails alpha 与 Computer Use/IM/update/remote/extension 边界 |

### 2.5 本轮已落地（勿重复劳动）

1. **FileTree 右键菜单**：`pointerdown` + `Escape` 可关闭；`FileTree.vue`  
2. **删除后树刷新**：`deleted`/`renamed` → 父节点更新 + cache purge + 关相关 tab  
3. **测试：** `FileTree.test.ts` 与删除/重命名/LSP 同步切片通过；回归命令见 §8.2.1  
4. **安全边界：** Agent/Terminal/MCP root setter 不对 renderer 暴露；Remote 命令使用 single-use session+argv token；审计日志仅存 hash/元数据。  
5. **终验：** `go test ./services/ -count=1`、`go test . -count=1`、`go vet ./...`、安全 race 切片、lint、typecheck、bindings/docs/e2e 脚本通过。  
6. **前端环境：** 仓库 `frontend/node_modules` 为损坏的可再生产物；使用相同源码与 `package-lock.json` 的 `/tmp` 干净隔离副本执行 `npm ci` 后，字面命令 `npm test -- --run` 通过（155 files / 2525 tests），lint 与 `vue-tsc --noEmit` 同样通过。  
7. **离线 E2E：** 在同结构 `/tmp` 干净副本执行 `node scripts/e2e-smoke.mjs` 通过（1 file / 1 test）；原仓库直接执行仍会因损坏的 `frontend/node_modules` 缺 Rolldown native binding 失败。  

仍未验证：自动 FS watcher（三方仅承诺 Refresh）、Windows/macOS/Linux 真机交互、真实 vtsls、installed-VSIX Worker 激活、coverage 本轮摘要、bindings 生成器与 pin 版本完全一致。

---

## 3. 技术地图（执行前 2 分钟扫读）

### 3.1 栈

| 层 | 技术 |
|---|---|
| 壳 | Wails v3 **alpha** |
| 后端 | Go 1.25，`services/` |
| 前端 | Vue 3 + TS + Vite + Element Plus + Tailwind |
| 编辑器 | Monaco |
| 终端 | ConPTY / creack-pty + xterm |
| Git | go-git |
| 分发 | `//go:embed frontend/dist` |

### 3.2 关键路径

```
main.go / bootstrap_services.go
services/                      # file lsp debug git ai agent mcp computer_use update im ...
frontend/src/api/services.ts   # 巨型 Wails 适配
frontend/src/stores/           # 模块 reactive（非 Pinia）
frontend/src/components/       # explorer editor ai git debug ...
frontend/bindings/
docs/ARCHITECTURE.md
docs/CHANGELOG.md
.github/workflows/
prompt-2.md / prompt-3.md      # 历史审查
```

### 3.3 能力完成度（勿从零重做已有主路径）

| 能力 | 完成度 | 备注 |
|---|---|---|
| Monaco 多标签/脏/Diff | 高 | |
| 文件树/读写/沙箱 | 高 | 见 §2.4 |
| 终端 PTY | 高 | |
| Git | 高 | |
| LSP Go/TS | 中高 | 多根 🟡 |
| Debug Delve + Node CDP | 中高 | Node ≠ 完整 js-debug |
| AI Chat SSE 双协议 | 高 | 需 API Key |
| Agent 审批/审计 | 高 | |
| MCP/Skills/Plan/Goal | 中高 | 边界项见 P0 |
| Marketplace/扩展 | 中 | 部分 API **stub** |
| Remote SSH | 中 | 最小可用 |
| Computer Use | 低 | **平台 executor 全 stub** |
| IM 入站 | 低 | |
| ApplyUpdate | 低 | |

### 3.4 已知 stub / 风险（过时则改本表）

| 路径 | 问题 |
|---|---|
| `services/computer_use_windows.go` / `computer_use_unix.go` | 截图/键鼠 `ErrPlatformUnsupported` |
| `services/computer_use_service.go` | token 安全闭环；平台执行器仍 intentional stub |
| `services/mcp_service.go` | root setter 已从 Wails 隐藏；继续保持窄 capability 传播 |
| `services/lsp_service*.go` | 已拆至单文件 <1500 行；真实 TS LSP 未验证 |
| `services/debug_*.go` | 已拆至单文件 <1500 行；不支持 generic DAP contribution |
| `services/update_service.go` | E2：下载+SHA-256；安装/重启/回滚手动 |
| `frontend/src/api/services.ts` | 27 行兼容 barrel；实现按领域模块拆分 |
| `frontend/src/lib/crossWindowSync.ts` | 主路径已收敛并有监听清理/CAS 测试 |
| `frontend/src/lib/extensionHost/*` | 部分 VS Code API v1 stub |
| `go.mod` | wails v3 alpha |
| IM 相关 | 仅出站；入站/轮询已移除并文档化 |

---

## 4. 任务总表（P0–P3）

### P0 — 必须先做（安全 / 诚实 / 阻塞）

| ID | 任务 | 目标 | 起点路径 |
|---|---|---|---|
| P0-1 | Computer Use 授权不可伪造 | 删除可伪造确认；一次性 action-bound token | `computer_use_service.go` |
| P0-2 | Computer Use 产品诚实 | stub 时 UI 明确未实现；默认关 | `computer_use_*.go`、locales、Settings |
| P0-3 | MCP root 沙箱 | renderer 不可 `SetWorkspaceRoot("")` 关沙箱 | `mcp_service.go`、`project_service.go` |
| P0-4 | IM 批准不可伪造 | 后端签发批准态 | `im_service.go` |
| P0-5 | 扩展 manifest 入口 | `main`/`browser`/`koyori-ide` 完整映射 | binding、activation |
| P0-6 | ApplyUpdate | **E1** 安装重启回滚 **或** **E2** 文案降级为仅下载 | `update_service.go`、更新 UI |
| P0-7 | 文件树 FS 一致 | 删/重命名/外部变更与编辑器一致 | `FileTree.vue`、file service |
| P0-8 | Wails 锁定 | pin 版本、可重复 release、binding 进 CI | `go.mod`、`.github/workflows` |

### P1 — 核心可靠性与可维护性

| ID | 任务 |
|---|---|
| P1-1 | 拆分 `lsp_service.go` |
| P1-2 | 拆分 `debug_service.go` |
| P1-3 | 拆分 `frontend/src/api/services.ts` |
| P1-4 | 跨窗事件收敛到 `crossWindowSync.ts` |
| P1-5 | `onCommand` 惰性激活 |
| P1-6 | Manifest cache 安装/更新/卸载后失效 |
| P1-7 | 扩展 API 支持矩阵文档 |
| P1-8 | 文件树+编辑器+LSP didClose 一致 |
| P1-9 | Shutdown 全局 deadline |
| P1-10 | 三平台冒烟 E2E |

### P2 — 性能 / 大仓 / 体验

| ID | 任务 |
|---|---|
| P2-1 | 大仓性能门禁（bench / 虚拟列表预算） |
| P2-2 | LSP 增量 didChange / 取消令牌 |
| P2-3 | 符号索引上限与可取消搜索 |
| P2-4 | Hot exit / dirty backup |
| P2-5 | 多根 go.work / pnpm workspaces |
| P2-6 | Node 调试向 js-debug 靠拢 |
| P2-7 | 真实 LSP 兼容矩阵 CI |
| P2-8 | FileTree 外部 FS watcher |

### P3 — 生态 / 远程 / 增长

| ID | 任务 |
|---|---|
| P3-1 | 代表性 VSIX 兼容套件 |
| P3-2 | Test Controller 统一 API |
| P3-3 | Debug Adapter contribution 启动桥 |
| P3-4 | 远端 PTY / 远端 agent |
| P3-5 | IM 真实收信或产品面移除 |
| P3-6 | Computer Use 真平台（仅安全完备后） |
| P3-7 | 文档站点 + 示例插件 + onboarding |
| P3-8 | 签名发布 + SBOM + govulncheck |
| P3-9 | Coverage gate |
| P3-10 | 产品定位文案（禁止过度营销） |

### 路线图

| 阶段 | 时间 | 主题 | 单元 |
|---|---|---|---|
| 短期 | 1–3 月 | P0 + 树 + E2E + Update + 扩展入口 | A B C D E H J |
| 中期 | 3–9 月 | 拆分、跨窗、性能、多根、矩阵 | F G I + P1/P2 |
| 长期 | 9–24 月 | 远端、插件平台、签名、可选 CU | P3 |

---

## 5. 可执行工作单元（Work Packages）

每个单元：**问题 → 要求 → 文件 → AC**。  
**完成 = 单元 AC + §8.1 + 触及的 §8.2 全部通过。**

---

### 单元 A：文件树与资源管理器一致性

**问题：** 本地删除已修；外部变更、重命名深层 cache、编辑器/LSP 同步仍弱。

**要求：**

1. 可选 workspace file watcher（或轮询），事件防抖 ≥ 100ms；或保持显式 Refresh 且默认可发现。  
2. 删除/重命名后：树、`editorState`、LSP `didClose` 一致。  
3. 右键：Escape/外部点击（已有）不回归。  
4. 虚拟列表 10k 预算不回归。

**文件：** `FileTree.vue`、`FileTree.test.ts`、`editor` store、必要时 `file_service.go`

**AC：**

- [x] 外部变更：Refresh 降级可用（2026-07-28）；自动 watcher 未接  
- [x] 删除打开中文件：关 tab 且无未捕获异常  
- [x] `FileTree.test.ts` 全绿；虚拟列表不超时  
- [x] 无「点外部不关菜单」回归  
- [x] 删除成功后父列表立即无该节点  
- [x] Escape / 菜单外 pointerdown 可关菜单  

**禁止：** 每次全树 reload 卡死大仓。

---

### 单元 B：Computer Use 安全 + 诚实

**要求：** 敏感操作一次性 token（动作+参数+TTL+generation）；unsupported 明确错误；设置默认关；文案诚实。

**AC：**

- [x] 无公开可伪造的「已确认」布尔直通  
- [x] 无 token / 重放 token 必失败（测试）  
- [x] stub 平台返回 unsupported；UI 可见说明  
- [x] 默认不启用  

---

### 单元 C：MCP / Project root 沙箱

**要求：** 禁止 renderer 设空 root；仅 ProjectService 传播；RemoveProject/失败回滚；generation 递增使旧 token 失效。

**AC：**

- [x] 危险 setter 不可用或被拒  
- [x] root 变更断开 MCP 并升 generation  
- [x] 安全测试 fail-closed  

---

### 单元 D：扩展宿主与 Marketplace

**要求：** manifest 入口字段完整；安装生命周期停 Worker→清 cache→再启；API 矩阵；`onCommand` 惰性激活。

**AC：**

- [x] ≥1 夹具 manifest 可解析入口并尝试 activate（真实 installed-VSIX E2E 留作 M3）  
- [x] 更新不双 Worker 常驻  
- [x] 矩阵列出 stub API  
- [x] 权限 deny-by-default + 测试  

---

### 单元 E：自动更新诚实闭环

**二选一（必须写明选哪条）：**

- **E1：** ApplyUpdate 替换二进制 + 重启 + 失败回滚  
- **E2：** 全文案改为「检查并下载，需手动安装」，禁用假装一键更新  

**AC：**

- [x] UI 与后端一致（E2）  
- [x] 校验失败不产出可安装下载  
- [x] 测试覆盖下载与 SHA-256 校验路径  

---

### 单元 F：God service 拆分（可多会话：F-lsp-1, F-debug-1…）

**要求：** 按协议/会话/IO 拆文件，**行为不变**；每阶段相关 `go test` / 前端 test 绿。

**AC：**

- [x] LSP/Debug/API 生产单文件均 <1500 行  
- [x] 无公开 API 破坏；binding+前端签名同步  
- [x] 同包测试通过  

---

### 单元 G：跨窗 SSOT 与监听生命周期

**要求：** 会话/设置/Agent 审批源窗口规则一致；单一注册点；卸载必 cancel；CAS 可测。

**AC：**

- [x] 无重复监听双份 chunk  
- [x] unmount 后无回调  
- [x] 审批仅发起窗（回归测试）  

---

### 单元 H：核心路径 E2E / 冒烟

**要求：** 开目录→树→打开→保存→（可选）git；AI 可 mock；CI 至少一平台强制。

**AC：**

- [x] 文档写明本地如何跑  
- [x] CI/脚本路径存在；三平台真机交互仍未在本机验证  
- [x] 不依赖真实付费 API Key  

---

### 单元 I：性能基线

**要求：** 保持 20MB 读上限等防护；搜索/索引可取消；FileTree/MessageList 虚拟列表不回归；可重复 bench。

**AC：**

- [x] 有可重复 benchmark/预算命令  
- [x] 大列表测试仍在约定预算内  

---

### 单元 J：开源与发布工程

**要求：** README 矩阵与代码一致；Release checksum/SBOM 或 govulncheck；SECURITY 版本表；贡献说明含 binding 生成与 test。

**AC：**

- [x] README/CONTRIBUTING 给出 build 与 binding 步骤  
- [x] 无「已实现」与 stub 矛盾（docs 检查通过）  
- [x] LICENSE MIT 保持  

---

## 6. 与市场 IDE 差距（路线用，不幻想一季度填平）

| 维度 | 合格市场 IDE | koyori-ide | 策略 |
|---|---|---|---|
| 扩展生态 | 海量稳定 API | 部分+stub | 公开矩阵；只承诺 Go/TS 相关 |
| 语言 | 全家桶 | Go/TS | **不要**优先铺全语言 |
| 远程 | Remote-SSH 级 | 最小远程 | P3 或明确「最小」 |
| AI | Cursor 级闭环 | SSE+审批 Agent | 强化安全差异化 |
| 调试 | 成熟 DAP | Delve 深/Node 浅 | 先 Go 日常 |
| 稳定性 | 十年运营 | 0.x+alpha | CI+E2E+诚实崩溃面 |
| 索引重构 | 工业级 | 有限 | 大仓 P2；不做假全项目智能重构 |

---

## 7. 安全专项（发布前 / 每个安全 PR 复测）

| ID | 项 | 通过标准 | 状态（回写） |
|---|---|---|---|
| SEC-01 | Agent 执行 | 无 token 无法 Exec；单次/绑参/TTL/generation | 已实现：Agent 安全回归 `-count=3` + race 切片 |
| SEC-02 | MCP Tool | 无 token 无法 CallTool；AutoApprove 不可恶意抬权 | 已实现：MCP token/root 安全测试 + services 全量 |
| SEC-03 | Computer Use | 无用户可伪造确认直通；stub 不假装成功 | 已实现：token 绕过测试；平台 stub 合规 wontfix |
| SEC-04 | IM 批准 | Approved 不可 UpdateConfig 伪造抬权 | 已实现：后端批准测试；入站产品面移除 |
| SEC-05 | MCP/Project root | 不能 SetWorkspaceRoot("") 关沙箱 | 已实现：setter 隐藏、空 root 拒绝、generation 测试 |
| SEC-06 | pathsec | 工作区外读写拒；symlink 有覆盖 | 已实现：pathsec/Terminal/Agent 沙箱测试 |
| SEC-07 | 密钥 | 不因本改明文落盘 | 已实现：存储隔离与日志 secret 脱敏测试 |
| SEC-08 | 扩展权限 | deny-by-default | 已实现：extension host/API 权限拒绝测试 |
| SEC-09 | CSP/危险 binding | 不新增裸 RCE exec 导出 | 已实现：CSP classifier 测试；bindings 禁止 root setter，`ByName=0` |
| SEC-10 | 审计 | 高风险操作有记录且不削弱 | 已实现：Agent/Remote 审计仅记录 hash/元数据测试 |

**结案：** 任一 SEC 为「未实现」且仍在默认产品面 → **不得**标 P0 完成；须修代码或 UI/编译剥离。

---

## 8. 统一验收标准（Definition of Done · 强制）

> **完成 = §8.1 全满 + 单元 AC 全满 + 触及的 §8.2 全满。** 部分通过 = 未完成。

### 8.1 通用 DoD（每个 PR/会话）

**功能与回归**

- [x] **AC-G1** 范围对齐：无 major 升级；未删测试；已披露一次误执行 `go fmt ./services/` 的无 Git 基线风险  
- [x] **AC-G2** 主路径：P0-P3 代码、组件、脚本均有自动化或明确手测边界  
- [x] **AC-G3** 同模块既有测试全绿；干净隔离副本完整前端单命令 155 files / 2525 tests 全绿，见 §2.5  
- [x] **AC-G4** 失败可感知；安全/更新/unsupported 路径 fail-closed  
- [x] **AC-G5** 监听/goroutine/timer 有对称清理；shutdown deadline 含 jobs  

**安全（触及执行/FS/网络/密钥/扩展/Agent/MCP 时强制）**

- [x] **AC-S1** fail-closed：无/过期/重放/跨 generation token 拒绝  
- [x] **AC-S2** 不信任 renderer 批准布尔  
- [x] **AC-S3** pathsec 不破沙箱  
- [x] **AC-S4** 无密钥入仓；日志无完整 secret  
- [x] **AC-S5** Agent/MCP/CU/IM/Remote 自动化证明绕过失败  

**质量门**

- [x] **AC-Q1** `go test ./services/ -count=1`、root package、race 安全切片通过  
- [x] **AC-Q2** 相关 vitest 与干净隔离副本完整单命令均通过  
- [x] **AC-Q3** `go vet ./...`、lint、typecheck 通过  
- [x] **AC-Q4** bindings 门禁通过；危险 root setter 不导出；God file 已缩小  

**文档**

- [x] **AC-D1** stub/实验性文案诚实  
- [x] **AC-D2** README/docs 已记录用户可见能力与限制  
- [x] **AC-D3** 本次 MERGE 已回写进度板/AC  

**交付物**

- [x] **AC-O1…O5** 最终交付包含文件、命令、安全与残留风险  

### 8.2 领域专项（按触及面叠加）

#### 8.2.1 资源管理器

- [x] **AC-FE1** 菜单外点击关闭  
- [x] **AC-FE2** Escape 关闭  
- [x] **AC-FE3** 菜单项可点（不因冒泡误关）  
- [x] **AC-FE4** 删除后父列表立即无节点  
- [x] **AC-FE5** 删除已打开文件：关 tab  
- [x] **AC-FE6** 重命名无幽灵旧 path  
- [x] **AC-FE7** 无删/重命名工作区根  
- [x] **AC-FE8** 虚拟列表不回归  
- [x] **AC-FE9** 卸载后无全局监听泄漏  
- [x] **AC-FE10** 外部变更：Refresh 降级已有；watcher 未接（2026-07-28）  

```bash
cd frontend && npm test -- --run src/components/explorer/FileTree.test.ts
```

#### 8.2.2–8.2.9 摘要（改到才勾）

| 域 | 关键 AC |
|---|---|
| 编辑器 | 脏→保存清脏；关脏有提示；无悬空 path |
| LSP | didOpen 不永久失败；didClose/不泄漏；不虚报语言 |
| 终端 | session id；关闭无僵尸进程 |
| Git | status/diff 不崩；凭据不入仓 |
| AI/Agent/MCP | 流状态机不卡死；无 token 不执行；取消无永久 pending；审批单窗 |
| 扩展 | 入口不丢；安装可回滚；卸载停 Worker；stub 可查 |
| 边缘 | Update/CU/IM 文案=能力 |
| 性能 | 不撤 20MB 等防护；虚拟列表；搜索可取消 |

### 8.3 建议命令基线

```bash
# 后端
go test ./services/ -count=1
go vet ./services/...

# 前端
cd frontend
npm test -- --run
npm run lint
# npm run typecheck  # 若存在
```

以 `Taskfile.yml` 与 `.github/workflows/` 为准。

### 8.4 CI 验收

- [x] **AC-CI1** 命令可复制跑通（仓库源码/锁文件的干净隔离副本）  
- [x] **AC-CI2** 不无故 skip 失败测试；155 files 全部执行  
- [x] **AC-CI3** 新公开 API 有测；危险 root setter 使用 `//wails:ignore` 且有 bindings 门禁  
- [x] **AC-CI4** 新 UI/store 分支有组件/store 测  
- [x] **AC-CI5** Go/frontend 三平台主 job；E2E Ubuntu 强制、Windows/macOS 明确 optional  

---

## 9. 里程碑门禁

### M1 — 单单元完成

§8.1 + 单元 AC + 触及 §8.2 + §1.5 交付已输出。

### M2 — 可认真开源试用（Recommend try）

**2026-07-30 状态：未验证。** P0、安全、A/H、README、pin 与本地全量门均已有实现证据；`npm test -- --run` 已在相同源码/锁文件的干净隔离副本全绿（155 files / 2525 tests）。但三平台 Actions/真机实际结果不在当前环境可核验，故不得对外宣称已达 M2。

- 全部 P0 关闭或 wontfix+隔离+README 诚实  
- §7 SEC-01…10 无「默认产品面未实现」  
- 单元 A 与 H 达 M1  
- README 与代码零矛盾  
- CI 主路径绿；Wails pin + 可重复构建  
- 无已知 renderer 伪造批准执行危险操作  
- AC-FE1…FE9 回归绿  

### M3 — Go/TS 可日常主力（仍非 VS Code 替代）

**2026-07-30 状态：未达到。** God file、跨窗、性能、Update E2 与扩展矩阵已完成；缺真实 vtsls happy path 和 installed-VSIX 到 frontend Worker 激活 E2E。

M2 + God file 至少拆 LSP 或 Debug 一阶段 + 跨窗无泄漏 + 性能文档化 + LSP Go/TS CI 真路径 + Update E1/E2 选定 + 扩展矩阵 + ≥1 VSIX 激活测通。

### M4 红线

即使 M3 也不宣称：生产级、VS Code 替代、完整 CU、完整远程、完整扩展市场。

---

## 10. Agent 快速检查清单（每次开干前 30 秒）

```
[ ] 我只锁定了一个任务 ID（或 §12 的一个 OWNER 车道）
[ ] 我读了相关源码与测试，不只信勾选
[ ] 我知道本单元 AC 与是否触及安全
[ ] 我不会升 major / 不会弱化审批 / 不会扩大范围
[ ] 我不会写入其他 Agent 的 OWNER 文件（§12.3）
[ ] 我结束时会跑相关测试并按 §1.5 / §12.6 交付
[ ] 单 Agent 模式：不自动开始下一单元；多 Agent：不越界领旁车道
```

---

## 11. 一句话结论（可 Issue 置顶）

> koyori-ide 是**完成度远超玩具仓**的 Go+Wails+Vue 桌面 AI IDE（~7.2/10）：核心链路与测试/开源规范扎实，但 Wails alpha、超大单体、边缘 stub 与 VS Code 生态鸿沟决定它是**有潜力的 0.x 垂直产品**。  
> **本 `prompt-1.md` 是长期任务 SSOT**：跨会话靠 §2 进度板；多 Agent 靠 §12；全量完成判定靠 §13；实现以代码与 §8 AC 为准。

---

## 12. 多 Agent 并行冲刺协议（一次性覆盖 P0–P3）

> **目标：** 用户可**同时**启动多个 Agent，在文件所有权不冲突的前提下，并行推进直至 **§13 全量验收** 通过。  
> **诚实上限：** 「P0–P3 全部完成」= **§13.1 定义的完成**，不是「一天内变成 VS Code」。P3-6 真平台 Computer Use、P3-4 完整远端等允许 **`wontfix` + 产品隔离 + README 诚实** 后算关闭。

### 12.1 角色

| 角色 | 数量 | 职责 |
|---|---|---|
| **OWNER-*** | 8 路并行实现 Agent | 只改本车道 OWNER 文件；自测；输出 §12.6 交付 |
| **MERGE** | 1（可后置/人工） | 合并冲突、跑全量回归、回写 §2、裁定 wontfix |
| **QA** | 1（可后置） | 只读复测 §13；不写业务代码（可写测试 harness） |

用户若只有实现 Agent：合并与 QA 由用户或最后一轮 Agent 串行承担。

### 12.2 波浪（Wave）——降低冲突与安全债

**禁止** 8 路同时改 `mcp_service.go` + `services.ts` + `lsp_service.go` 无协调。按波次启动：

| Wave | 并行车道 | 主题 | 门禁（本波全绿才开下一波） |
|---|---|---|---|
| **W0** | O-SEC, O-CU, O-IM, O-UPD | P0 安全与诚实（可并行，文件面几乎不交） | §7 SEC 相关项 + 各车道 AC |
| **W1** | O-TREE, O-EXT, O-AIDOC | 文件树 / 扩展 / README·矩阵诚实 | A/D/J 相关 AC |
| **W2** | O-XWIN, O-PERF, O-E2E | 跨窗 / 性能 / 冒烟 E2E | G/I/H |
| **W3** | O-LSP, O-DBG, O-API | God file 拆分（**严禁同时改同一文件**） | F 分阶段；每阶段全包 test 绿 |
| **W4** | O-P2, O-P3 | 剩余 P2/P3；远端/CU 真实现仅在安全完备后 | §13 清单 |

**推荐一次最多 4 个实现 Agent**（W0 或 W1）。更多并行 → 合并冲突指数上升。

### 12.3 文件所有权矩阵（OWNER 铁律）

**规则：** Agent 只写「自己 OWNER」列；**可读**他人文件；若必须改共享文件 → **停工**，在交付写 `BLOCKED_SHARED:`，由 MERGE 串行改。

| 车道 ID | 负责任务 | **可写**（主） | **禁止写** |
|---|---|---|---|
| **O-SEC** | P0-3, 单元 C；SEC-02/05 测试 | `mcp_service.go`、`mcp_service_*test.go`、`project_service.go`（仅 root 传播）、相关 security test | `computer_use_*`、`im_*`、`update_*`、`FileTree.vue`、`lsp_service.go` |
| **O-CU** | P0-1, P0-2, 单元 B | `computer_use_*.go`、`*_test.go`、`stores/computerUse*`、locales 中 CU 键、Settings 实验性 CU 段 | `mcp_*`、`im_*`、扩展宿主 |
| **O-IM** | P0-4；SEC-04；P3-5 收信或剥离 | `im_service.go`、`im_service_test.go`、`stores/im.ts`、IM 相关 UI/文案 | 其他服务大文件 |
| **O-UPD** | P0-6, 单元 E（**必须写明 E1 或 E2**） | `update_service.go`、update 相关 test/UI/文案 | 安装器脚本以外的无关模块 |
| **O-TREE** | P0-7, 单元 A, P1-8, P2-8 | `FileTree.vue`、`FileTree.test.ts`、explorer 组件、`editor` store 中删/重命名同步、file watcher 相关 service | `lsp_service.go` 大拆（仅可调 didClose API） |
| **O-EXT** | P0-5, 单元 D, P1-5/6/7, P3-1 夹具 | extension host、marketplace service/UI、manifest 转换、扩展矩阵 md | `lsp_service.go` 拆分、mcp root |
| **O-XWIN** | 单元 G, P1-4 | `crossWindowSync.ts`、相关 store 事件注册、窗口服务监听清理 | God file 拆分 |
| **O-E2E** | 单元 H, P1-10, P0-8 文档部分 | `scripts/`、`.github/workflows` 冒烟 job、E2E 文档；**尽量不改**业务逻辑 | 大范围业务重构 |
| **O-LSP** | P1-1, 单元 F-lsp, P2-2/5/7 | **仅** `lsp_service*.go` 拆分与测试；行为不变 | `debug_service.go`、`services.ts` 大拆 |
| **O-DBG** | P1-2, F-debug, P2-6, P3-3 | **仅** `debug_*.go` 与测试 | `lsp_service.go` |
| **O-API** | P1-3, F-frontend-api | **仅** `frontend/src/api/services.ts` 拆模块 + 调用方 import 修复 | 后端 God file |
| **O-PERF** | 单元 I, P2-1/3/4 | bench 脚本、搜索/索引 budget、虚拟列表测试加固 | 无关功能 |
| **O-DOC** | 单元 J, P3-7/8/9/10, P0-8 发布说明 | README、docs、CHANGELOG、SECURITY、CONTRIBUTING、SBOM 脚本说明 | 在未协调时改核心 runtime |
| **O-P3R** | P3-2/4/5/6… | 测试控制器、remote 增强、IM 剥离等 **单独立项**；开干前在交付声明文件列表 | 与 W0 安全文件冲突时让路 |

**共享热点（默认只读，MERGE 独占写）：**

- `main.go` / `bootstrap_services.go`（注册新服务时）  
- `frontend/src/api/services.ts`（O-API 独占；他人只提「请 O-API 加 binding」）  
- `go.mod` / 依赖 major  
- `prompt-1.md` §2 进度板：**各 Agent 只追加自己车道一行**；全表整理交给 MERGE  

### 12.4 每个实现 Agent 的强制启动词（复制给该 Agent）

```text
你是 koyori-ide 多 Agent 冲刺中的 【车道 ID：O-____】。
严格遵守 prompt-1.md §0、§8、§12、§13。

只做该车道「可写」文件列表内的任务（见 §12.3）。
禁止修改其他车道 OWNER 文件；需要时输出 BLOCKED_SHARED 并停止越界。
开始前：读代码确认现状；列出本车道任务 ID 与 AC。
实现：最小 diff；安全 fail-closed + 测试；不升 major；不 commit（除非用户要）。
结束：§12.6 交付；向 prompt-1.md §2.2/2.3 追加本车道状态一行（勿重写整表他人行）。
本车道 AC + §8.1 未全绿 = 未完成。
```

### 12.5 冲突与合并协议

1. **文件锁：** 同一路径同时只能有一个 OWNER；发现他人已改 → rebase/手工合并，保留双方测试。  
2. **行为锁：** 拆分（O-LSP/O-DBG/O-API）**禁止**夹带功能；功能在其他车道做完后再拆，或拆完再开功能 PR。  
3. **安全锁：** W0 未绿前，W3/W4 不得宣称「可推荐试用」。  
4. **MERGE 顺序建议：** O-SEC → O-CU → O-IM → O-UPD → O-TREE → O-EXT → O-XWIN → O-API → O-LSP → O-DBG → O-PERF → O-E2E → O-DOC。  
5. **全量回归（MERGE/QA 必跑）：**

```bash
go test ./services/ -count=1
go vet ./services/...
cd frontend && npm test -- --run && npm run lint
```

（E2E job 若存在则跑；环境不足标「未验证」+ 复现步骤。）

### 12.6 多 Agent 交付模板（每车道）

```markdown
## 多 Agent 交付
- 车道：O-___
- 任务 ID 列表：
- 改动文件（必须 ⊆ OWNER）：
- 越界/阻塞：无 | BLOCKED_SHARED: ...

## AC
- 车道任务 AC：...
- §8.1：...
- 触及的 SEC-xx：...

## 命令
- ...

## 给 MERGE
- 合并注意：
- 需要 O-API / bootstrap 注册：是/否（说明）
```

### 12.7 「一次性全部完成」的现实定义

| 宣称 | 合法条件 |
|---|---|
| **P0 全部完成** | P0-1…8 均为 `已实现` 或 `wontfix`+隔离+README |
| **P1 全部完成** | P1-1…10 达 AC；God 拆分允许「阶段完成」但单文件有明确上限进展 |
| **P2 全部完成** | P2 项有测/门禁或文档化极限；非口头 |
| **P3 全部完成** | 每项 `已实现` **或** 书面 `wontfix`+产品面移除/隐藏 |
| **冲刺成功** | §13.1 清单全勾 + MERGE 全量回归绿 |

**单次并行无法诚实承诺的（除非多周+真机）：** 完整 Remote-SSH、完整 VS Code 扩展生态、工业级全语言、非 stub 的全平台 Computer Use 键鼠。这些在 §13 中标为 **P3-OPTIONAL / wontfix 合法**。

---

## 13. P0–P3 全量验收方案（Acceptance Plan）

> **用途：** 多 Agent 完成后，**QA / MERGE / 用户** 按本方案逐项验收。  
> **规则：** 无证据（命令输出 / 测试名 / 截图步骤）不得勾选。  
> **总开关：** §13.1 全绿 ⇒ 可对内宣布「prompt-1 冲刺完成」；仍受 §0.3 对外红线约束。

### 13.1 总验收清单（冲刺 Definition of Done）

- [x] **V-ALL-1** 全部 P0 关闭（P0-2 为合规 wontfix）  
- [x] **V-ALL-2** §7 SEC-01…10 无「默认产品面未实现」  
- [x] **V-ALL-3** 单元 A–J 均达阶段 AC；M3 推迟项已写入 §2.4  
- [x] **V-ALL-4** `go test ./services/ -count=1` 通过（本轮约 164 秒）  
- [x] **V-ALL-5** 干净隔离副本执行 `npm test -- --run` 通过：155 files / 2525 tests；源码与锁文件来自仓库，排除损坏的可再生 `node_modules`  
- [x] **V-ALL-6** `go vet ./...` + `npm run lint` + `vue-tsc --noEmit` 通过  
- [x] **V-ALL-7** README / locales 与 stub 零已知矛盾；docs link/number 检查通过  
- [x] **V-ALL-8** 无已知 renderer 伪造批准执行危险操作；Agent/Remote/Terminal 回归与 bindings 门禁通过  
- [x] **V-ALL-9** Wails `alpha2.111` pin 与 release/binding 步骤已文档化；曾以不同生成器环境生成的风险保留  
- [x] **V-ALL-10** §2 进度板已由 MERGE 更新为 2026-07-30 终态  

**总开关：通过。** V-ALL-1…10 已按本地可验证证据闭合，可对内宣布 prompt-1 冲刺完成；仍受 §0.3 对外红线约束。M2 因远端三平台 Actions/真机结果未在当前环境核验，状态保持「未验证」；M3 仍未达到。

### 13.2 P0 逐项验收

| ID | 验收步骤（操作 / 自动化） | 通过标准 | 证据 |
|---|---|---|---|
| **P0-1** | `go test` computer_use：无 token、重放、过期、绑参错误 | 全部拒绝；无公开 `confirmedByUser` 直通执行 | 测试名 |
| **P0-2** | 设置页默认关；调用 stub 平台 | UI/文案含未实现或实验性；返回 `ErrPlatformUnsupported` 类错误；不假装成功 | 测试 + 文案路径 |
| **P0-3** | 尝试空 root / 前端 binding 调 SetWorkspaceRoot | 拒绝或 `//wails:ignore` 且 renderer 不可达；有 fail-closed 测 | 测试名 |
| **P0-4** | UpdateConfig 带 Approved=true；未走 Approve | 不抬权；Approve 需后端确认 | 测试名 |
| **P0-5** | 夹具 manifest 含 main/browser/koyori-ide | 字段不丢失；转换测试绿 | 测试 / 夹具路径 |
| **P0-6** | 核对 UI 按钮与 `update_service` | **E1** 安装重启回滚可测 **或** **E2** 无「一键更新」误导文案 | 选型声明 + 测/截图步骤 |
| **P0-7** | 删文件/夹、重命名、Refresh；打开中删除 | 树与 tab 一致；FE1–FE9 相关测绿 | FileTree 测试 |
| **P0-8** | 读 go.mod 与 CI/docs | 版本 pin；binding 生成步骤文档化 | 文件路径 |

### 13.3 单元 A–J 验收矩阵

| 单元 | 必跑命令 / 检查 | 通过标准 |
|---|---|---|
| **A** | `npm test -- --run src/components/explorer/FileTree.test.ts` | §5 单元 A AC + AC-FE1…9 |
| **B** | `go test ./services/ -count=1 -run ComputerUse`（或包内等价） | 单元 B AC |
| **C** | `go test` MCP/Project root 安全用例 | 单元 C AC |
| **D** | 扩展/marketplace 测 + 矩阵文档存在 | 单元 D AC |
| **E** | update 测 + UI 文案 diff | E1 或 E2 与代码一致 |
| **F** | 拆分后 `go test` 相关包；文件行数有下降记录 | 行为不变；无 API 误伤 |
| **G** | crossWindow 相关前端测；审查无双注册 | 单元 G AC |
| **H** | CI/脚本冒烟；文档「如何跑」 | 不依赖真 API Key |
| **I** | bench 或虚拟列表预算测 | 单元 I AC |
| **J** | README 矩阵人工对照 stub 表 | 零矛盾；MIT 仍在 |

### 13.4 P1 验收（可靠性）

| ID | 通过标准 | 证据类型 |
|---|---|---|
| P1-1 | lsp 拆分阶段目标达成或列剩余行数与下一刀 | diff 统计 + test |
| P1-2 | debug 同上 | 同上 |
| P1-3 | services.ts 按领域模块；调用方可编译/测试 | vitest + tsc |
| P1-4 | 跨窗事件主路径经 crossWindowSync；卸载无泄漏 | 测试/审查 |
| P1-5 | onCommand 触发 activate | 测试或脚本 |
| P1-6 | 装/更/卸后 cache 失效 | 测试 |
| P1-7 | 扩展 API 矩阵已发布 | docs 路径 |
| P1-8 | 删/重命名 → didClose/无悬空 | 测或步骤 |
| P1-9 | shutdown 有 deadline 或不被单服务永久阻塞 | 测或代码点 |
| P1-10 | 冒烟 E2E 存在 | CI/脚本 |

### 13.5 P2 验收（性能/体验）

| ID | 通过标准 |
|---|---|
| P2-1 | 可重复 bench 或 CI 预算文档 |
| P2-2 | 增量 didChange/取消有实现或明确里程碑 Issue（不得虚报已完成） |
| P2-3 | 索引/搜索有上限或 cancel |
| P2-4 | hot exit/backup 有或 wontfix 文档 |
| P2-5 | 多根切换重启 LSP 有测或手测步骤 |
| P2-6 | Node 调试改进有 AC 列表勾选 |
| P2-7 | gopls+vtsls 至少一条 CI/集成 happy path **或** 诚实降级为「仅单元 mock」并写明 |
| P2-8 | watcher 或 Refresh 策略文档 + 测 |

### 13.6 P3 验收（生态/增长 · 允许合规 wontfix）

| ID | 已实现通过标准 | 合法 wontfix |
|---|---|---|
| P3-1 | ≥1 夹具 VSIX 报告 | 推迟但矩阵已说明兼容有限 |
| P3-2 | Test Controller API 有 | 文档「未提供」 |
| P3-3 | adapter 可拉起 | 文档仅内置 Delve/CDP |
| P3-4 | 远端增强有 AC | 保持「最小远程」文案 |
| P3-5 | 轮询收信 **或** 移除/隐藏 IM 入站 | 必须二选一 |
| P3-6 | 真平台实现 + 安全模型 | **默认推荐 wontfix**：保持 stub+默认关 |
| P3-7 | onboarding/文档站点有改善 | 仅 README 也可阶段通过 |
| P3-8 | checksum/SBOM/govuln 有摘要 | 有 Issue 跟踪可阶段 |
| P3-9 | coverage gate 有或跟踪 | 同上 |
| P3-10 | README 定位无过度营销 | 必做 |

### 13.7 安全验收剧本（QA 手工 + 自动化）

对每一项记录：**自动化用例名** 或 **手工步骤 1.2.3** + 结果。

1. **Agent Exec** 无 token → 拒绝  
2. **MCP CallTool** 无 token → 拒绝  
3. **Computer Use** 无 token / 重放 → 拒绝；stub 不成功  
4. **IM** 伪造 Approved → 无抬权  
5. **MCP/Project** 空 root → 沙箱仍在  
6. **pathsec** 读工作区外 → 拒  
7. **扩展** 未声明 fs.write → 不能删文件  
8. **密钥** 日志无完整 API Key  

### 13.8 回归命令包（MERGE 最终门禁）

```bash
# 后端全量
go test ./services/ -count=1
go vet ./services/...

# 前端全量
cd frontend
npm test -- --run
npm run lint

# 关键切片（可选加速预检）
go test ./services/ -count=1 -run 'ComputerUse|MCP|IM|Update|Agent|Path'
npm test -- --run src/components/explorer/FileTree.test.ts
```

失败处理：对应 OWNER 车道修复；禁止 MERGE 用删测试过关。

### 13.9 验收报告模板（QA 输出）

```markdown
# Koyori IDE P0–P3 冲刺验收报告
- 日期：
- 合并后 commit / 工作区：
- V-ALL-1…10：勾选表
- P0 表：每项 通过/失败/未验证 + 证据
- 单元 A–J：同上
- P1/P2/P3：同上
- SEC 剧本：同上
- 全量命令输出摘要：
- 残留风险与 wontfix 列表：
- 对外话术（必须含红线）：仍为 0.x 垂直 AI IDE，非 VS Code 替代
```

### 13.10 用户「多 Agent 一次开齐」操作步骤

1. 复制本文件给所有 Agent 作上下文。  
2. **先开 W0 四车：** O-SEC、O-CU、O-IM、O-UPD（四条 §12.4 启动词）。  
3. 收齐四份 §12.6 交付 → 人工/MERGE 合入 → 跑 §13.8。  
4. 再开 W1：O-TREE、O-EXT、O-DOC。  
5. 再开 W2：O-XWIN、O-PERF、O-E2E。  
6. **串行** W3：O-API → O-LSP → O-DBG（或三者分时，永不三人同时改同一 God 文件）。  
7. W4：P2/P3 剩余；P3-6 默认 wontfix。  
8. QA 按 §13.9 出报告；§2 进度板终态由 MERGE 写回。

---

## 附录 A：评分卡（下轮复制并标 Δ）

| 维度 | 2026-07-28 |
|---|---:|
| 功能覆盖 | 7.5 |
| 前后端闭环 | 7.0 |
| 安全边界 | 7.0 |
| 扩展生态 | 4.5 |
| 性能工程 | 6.0 |
| 测试可信度 | 7.5 |
| 远程开发 | 4.0 |
| 开源规范 | 8.5 |
| **综合** | **7.2** |

## 附录 B：用户一键启动

### B1 — 单 Agent（一单元）

```text
你是 koyori-ide 长期任务 Agent。严格遵守仓库根目录 prompt-1.md：
1) 读 §2 进度板，用 §1.3 选下一个未完成最高优先级任务（或只做我指定的：___）
2) 读代码确认现状，最小 diff 实现该单元
3) 按 §8 验收；跑相关 go test / vitest
4) 按 §1.5 交付；回写 prompt-1.md §2 与对应 AC
5) 停。不要做下一单元。
禁止：升 major、弱化审批、把 stub 写成已实现、无故 commit。
```

### B2 — 多 Agent 单车道（并行用）

```text
你是 koyori-ide 多 Agent 冲刺车道 【O-___】。
遵守 prompt-1.md §0 §8 §12 §13。
只改 §12.3 该车道可写文件；完成该车道全部任务 AC。
输出 §12.6 交付。禁止越界、升 major、弱化审批、无故 commit。
```

### B3 — MERGE / 终验

```text
你是 koyori-ide MERGE+QA。只合并与验收，不开新功能。
按 prompt-1.md §12.5 合并顺序与 §13 全量验收方案执行：
跑 §13.8 命令；填写 §13.9 报告；更新 §2 进度板终态。
任何失败打回对应 OWNER 车道，禁止删测试保绿。
```

### B4 — 用户并行开齐（W0 示例）

对 4 个 Agent 分别发送 B2，车道填：`O-SEC` / `O-CU` / `O-IM` / `O-UPD`，并附上本 `prompt-1.md` 全文或至少 §0+§5+§8+§12+§13。

## 附录 C：命令备忘

```bash
cd frontend && npm test -- --run src/components/explorer/FileTree.test.ts
cd frontend && npm run lint
go test ./services/ -count=1
go vet ./services/...
```

---

*文档结束。单 Agent 回写 §2；多 Agent 遵守 §12 所有权；冲刺完成以 §13 为准。*
