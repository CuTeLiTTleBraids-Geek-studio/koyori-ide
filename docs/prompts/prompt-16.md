# P16 Goal Prompt：Codex 级 AI / IDE 能力收口

> 本文件是独立 Goal，不继承 `prompt-14.md` 或 `prompt-15.md` 的任务状态、完成声明和验收结论。它只记录本轮对当前工作区的只读审查结果，以及下一阶段应执行的真实收口目标。

## 1. 唯一目标

把 Koyori IDE 的 AI 主链路收口到可持续使用的 Codex 级开发体验：用户输入可靠，模型输出不会丢失或重复，原生工具调用形成完整的请求-批准-执行-观察-下一轮闭环，权限只有三档可理解的会话策略，模型推理强度真正进入 provider 请求，MCP 和 Git 状态可被 AI 与用户完整发现，IDE 核心开发路径可运行。

Codex 级的含义不是复制所有产品功能，而是满足以下不变量：

1. 一次用户输入只产生一个权威会话轮次；成功接纳后输入框不保留已发送草稿。
2. 一次 provider tool-call 轮次只执行一次；工具结果以正确的原生消息结构返回给 provider，下一轮结果可继续流式输出。
3. UI 展示、前端状态和后端权限不是三套互相覆盖的策略；后端是最终权限边界。
4. 所有会影响工作区的动作都能被用户看见、批准、拒绝、取消或恢复；批准不能绕过路径校验、危险命令阻断、能力令牌、CAS 和审计。
5. “已实现”必须有真实请求或真实 IDE 操作证据；组件、binding、静态 mock 和注释不算闭环证据。

## 2. 当前审查结论

### 2.1 P0：已经影响基本可用性的缺陷

#### P0-01：成功发送返回失败值，导致输入框保留草稿

证据：`frontend/src/stores/ai.ts:790-996`。`sendMessageInternal` 在成功启动流、设置 `activeStreamId`、启动超时和重放已接纳事件后，函数末尾仍统一执行 `return false`；只有异常分支和若干提前退出显式返回 `true`。`frontend/src/components/ai-assistant/InputComposer.vue:432-461` 依据 `accepted` 决定是否清空输入，因此成功发送可能被当作拒绝，用户会看到已发送内容仍留在输入框中。

要求：修复发送接纳结果的单一语义，成功启动并归属流必须返回 `true`，真正未接纳才返回 `false`，异常必须保留可重试草稿。补充覆盖 chat、agent、原生 tool-result round、stream busy、重复发送、stale stream 和双窗口路径。

#### P0-02：native tool calling 与 fenced tool protocol 并存，协议权威性不清

证据：`frontend/src/stores/agent.ts`、`frontend/src/stores/ai.ts`、`services/ai_service.go` 和 `services/ai_prompts.go` 同时保留 native tool call 与 fenced 文本解析；UI 需要对 fence 做额外清理，native 与 fence 的去重和调用 ID 不能只由组件兜底。

要求：native 成为默认且唯一权威主路径；兼容 fence 只能是明确标记的 fallback；两者共享 catalog、schema、审批、执行、结果消息和去重管线。

#### P0-03：provider 多轮闭环需要真实 HTTP fixture 证据

证据：现有 provider、stream、tool round 测试覆盖了部分路径，但必须继续以本地 HTTP fixture 验证 OpenAI-compatible 与 Anthropic 实际请求形状、原生 tool result、下一轮 provider 响应和状态恢复。

要求：覆盖单工具、同批多工具、拒绝、执行错误、malformed response、超时、停止生成、stale event、双窗口隔离和空文本 tool call。

#### P0-04：Plan 模式不能保留空计划伪闭环

证据：若输入路径仍创建空 steps，用户会误以为系统已完成规划。

要求：接入真实 plan generation 闭环，或隐藏未完成入口。

### 2.2 P1：统一能力模型与核心 IDE 投影

#### P1-01：统一为三档会话级权限

用户可见权限只能有“始终询问”“帮我批准”“全部批准”，最终决策必须在后端完成。危险命令、路径、能力 token、budget、CAS、审计和 fail-closed 仍不可绕过。

#### P1-02：reasoning effort 必须进入真实 provider 请求

设置、会话、provider assignment、fallback 和 operation assignment 必须保持 `low`、`medium`、`high`；支持的 provider 请求发送真实 reasoning 字段，不支持或未知模型必须明确 unsupported/unknown 并 fail-closed。

#### P1-03：MCP 上下文能力

继续在现有 MCP client/service/catalog 上完成 initialize capabilities、server capabilities、tools/resources/prompts、list-changed、workspace roots、generation 绑定和 unsupported 能力显式展示。

#### P1-04：Git 前端完整投影

真实窄侧栏必须展示 branch、ahead/behind、staged、unstaged、untracked、renamed、conflict、rebase、operation error 和 truncation continuation；diff、hunk、apply/reject、stage/unstage、CAS 失效和外部刷新使用一致文件身份。

### 2.3 P2：默认曝光收口

P0/P1 稳定后再处理重复模型权限分配、低价值默认设置、高级功能默认曝光和无调用者旧协议。删除前必须确认 caller、binding、locales、测试和文档均已迁移。

## 3. 验收标准

### AC-01：AI 输入接纳

成功流只清空一次草稿；busy、后端拒绝和异常不清空可重试草稿，且不会遗留 optimistic message 或 streaming 状态。

### AC-02：native tool calling

native tool call 经过统一 catalog/schema/approval/execute/result 管线，调用 ID 稳定且不会重复执行或重复展示。

### AC-03：真实 provider 多轮

OpenAI-compatible 和 Anthropic fixture 均验证真实 HTTP 请求、原生 tool result、下一轮请求和最终回复。

### AC-04：三档权限

权限在后端生效，renderer 只能表达意图，所有工具类型共享三档会话策略与安全硬边界。

### AC-05：reasoning effort

`low`、`medium`、`high` 进入支持 provider 的真实 JSON 请求；不支持/未知明确展示并 fail-closed。

### AC-06：MCP

tools/resources/prompts 可发现、读取、注入上下文并展示错误；workspace roots、generation、批准和缓存失效正确绑定。

### AC-07：Git 投影

窄侧栏完整展示状态；长列表可继续查看；rename/conflict/rebase/error/truncation 可准确确认。

### AC-08：IDE 核心路径

editor、terminal、search、Git、LSP、AI agent 均通过真实运行操作验证。

### AC-09：安全

路径安全、审批、能力 token、budget、CAS、workspace generation、审计和 fail-closed 无回归。

### AC-10：回归与交付

受影响 Go 单测、前端定向测试、provider/MCP/Git fixture 和真实 smoke 均有命令与结果记录；未验证范围标记 `U`。

## 4. 执行策略

先做 P0 基本正确性，再做 P1 能力统一，最后处理 P2 默认曝光。每个断点执行 Inspect -> Implement -> Verify -> Evidence -> Update；局部 mock、静态 binding 和安装成功不能替代真实行为证据。

## 5. 未完成边界

P0/P1 的范围仍需按上述验收标准逐项验证。未执行的 packaged/release、跨平台验证和外部 provider 访问均不得改写为完成。

## 6. 交付证据格式

每个 AC 必须记录：

- 具体命令、测试名称或真实操作步骤。
- provider/server/repository fixture 身份和请求/响应摘要。
- 结果是 `T`（自动化测试）、`I`（真实集成）、`P`（打包产物）、`U`（外部阻塞）中的哪一种。
- 失败时保留原始错误和影响范围；不能用“源码存在”“组件存在”“binding 已生成”“安装成功”替代激活证据。
- 最终报告必须明确列出仍未完成的能力，不能把 P2 低价值功能或外部 packaged/Release 阻塞改写成完成。

## 7. 执行状态

本轮已对 P0-01 的真实发送路径完成实现和验证；后续断点仍以本文件的验收标准为准，未验证范围不得标记为完成。

## 8. 执行证据

### P0-01：发送接纳语义

- `T`：`npm exec vitest run src/stores/ai.test.ts src/components/ai-assistant/InputComposer.test.ts src/components/layout/AiChatPanel.test.ts`。覆盖成功 admission、busy、后端异常、重复发送、native tool-result round、stale stream、composer 草稿与未接纳 optimistic message rollback。
- 状态转换：只有后端返回非空 stream ID 且该 ID 被当前 renderer 接纳后返回 `true`；busy、未接纳和异常返回 `false`，异常清理 renderer stream ownership、按稳定 message ID 回滚本轮 user/tool + assistant draft，但保留 composer 中可重试的用户草稿。已经接纳后收到 `ai:error` 的权威 user turn 不回滚。
- `I`：真实 Chromium 打开 `http://127.0.0.1:9245/#/ai-window`，在无 provider 配置时输入 `p16 rollback draft` 并点击 Send。后端拒绝后 textarea 原样保留草稿、Send 恢复可用，消息区回到空状态提示，不再残留 optimistic user 消息。

### P0-02：native tool calling 收敛

- 实现：`frontend/src/stores/agent.ts` 的 `ToolCall.source` 明确区分 `native` 与 `fence`；native provider call ID 在当前 Agent session 内保存完整 invocation identity，exact event replay 返回 `0` 且无副作用，ID 被不同调用复用返回 `-1` 并 fail-closed。native 与 fence 仍进入同一个 `enqueueToolCalls -> applyApprovalPolicy -> executeAgentTool -> feedToolOutcome` 管线。
- 实现：`frontend/src/stores/ai.ts` 将 native-event-seen 绑定到当前 assistant draft。该轮收到任何 native event 后不再解析 fence fallback；malformed native batch 也不能降级执行同轮 fence。assistant message 按 provider call ID 合并重放事件，不重复展示。
- 实现：`services/ai_prompts.go` 与 en/zh/ja locale 均声明 provider native function/tool-calling 为默认主协议；动态 catalog 摘要不再输出 ``tool:`` fence 语法。Fence 只保留为 provider 不支持 native 时的显式兼容 fallback。
- 实现：`MessageList.vue` 与 `AiChatPanel.vue` 只清理 schema-valid 的纯 fence fallback。已有 native `toolCalls` 的消息保留额外 fence 文本，协议混用不会被 renderer 静默掩盖。
- `T`：`npm exec vitest run src/stores/agent.test.ts src/stores/ai.test.ts src/components/ai-assistant/MessageList.test.ts src/lib/i18n.test.ts`，4 files / 240 tests passed。
- `T`：`npm exec vitest run src/components/layout/AiChatPanel.test.ts src/components/ai-assistant/MessageList.test.ts src/components/ai-assistant/AgentToolCalls.test.ts src/components/ai-assistant/AgentExecutionTimeline.test.ts`，4 files / 45 tests passed。
- `T`：补齐 `mcpSection.fieldEnabled` 与 `mcpSection.enabledHint` 的中文翻译后，`npm exec vitest run src/lib/i18n.test.ts` 与 P0-02 前端定向组通过；locale key parity 不再阻塞协议验收。
- `T`：`npm exec vue-tsc -- --noEmit` passed。
- `T`：`go test ./services -run 'TestAgentSystemPrompt|TestAIService_GetSystemPrompt|TestAIService_Native|TestNative|TestOpenAI|TestAnthropic'` passed。覆盖 native prompt、OpenAI/Anthropic message shape、call/result ID 与 malformed protocol 校验。
- `I`：真实 Chromium 加载 Vite renderer 并挂载实际 `MessageList.vue`。注入与 backend catalog 相同的最小 `read` schema 后，纯 fence fallback 隐藏工具块但保留前后叙述；native+fence 消息保留 `read: duplicate.ts` 原文并只显示一张 native call card；组件 error handler 为空。
- 限制：静态 Vite runtime 缺少 Wails backend 时，AI 路由 fail-closed 到设置页并显示 `Could not open AI settings.`。本条只证明真实 renderer/UI 行为；OpenAI-compatible 与 Anthropic 的真实 HTTP 多轮 provider fixture 归 P0-03，尚未据此声明完成。

### P0-03：真实 provider 多轮闭环

- `I`：`go test ./services -run '^TestAIServiceNativeToolStreamingRoundTripHTTP$|^TestAIProviderStreamBoundary' -count=1 -v`。`httptest.Server` 捕获真实 HTTP JSON 与 SSE：OpenAI-compatible 单 `read`、OpenAI 用户拒绝、Anthropic 同批 `read + search`、Anthropic 缺失文件执行错误均完成第一轮 native call、原生 result 第二轮请求和最终 assistant 文本。
- 请求断言：OpenAI 第二轮为 `assistant.tool_calls[]` 后按原 call ID 发送 `role=tool + tool_call_id`；Anthropic 第二轮为 `assistant.tool_use` 后按原 ID 发送 `user.tool_result + tool_use_id`，错误结果含 `is_error=true`。两协议都断言 system/user 消息顺序、tools schema、stream=true、两次不同 stream ID 与同一 backend session。
- 状态断言：真实 backend catalog/schema/capability/execute 管线执行工作区 `read/search`；用户拒绝不触发 executor/usage；缺失文件产生真实失败 usage。每轮 lifecycle/usage 终结，persistent session 保持 `running`，global stream slot 释放，非 owner 窗口收到 0 个私有事件。
- `I`：`TestAIProviderStreamBoundaryMalformedResponse`、`ProviderTimeout`、`StopReleasesProviderAndDropsLateChunk`、`EmptyTextNativeToolCall` 分别证明连续 malformed SSE 产生带 stream ID 的 `ai:error`、idle timeout 取消 provider context、StopStream 释放请求且迟到 chunk 不到 renderer、空文本 native call 仍发出权威 `ai:tool_calls`；失败 session/usage 均 terminal，成功空文本 call session 保持 `running`。
- `T`：`npm exec vitest run src/stores/ai.test.ts src/stores/agent.test.ts src/stores/agentTimeline.test.ts src/components/ai-assistant/MessageList.test.ts src/components/ai-assistant/InputComposer.test.ts src/components/layout/AiChatPanel.test.ts`，6 files / 309 tests passed。覆盖 native 单拒绝、执行错误、同批顺序结果、stale generation、foreign/stale stream、busy terminal barrier、消息历史和 timeline 恢复。
- `T`：`npm exec vue-tsc -- --noEmit` passed。

### P0-04：Plan 模式

- 决策：隐藏未完成的 Plan 输入入口，不保留“清空输入 + 创建空 steps”的伪闭环。后端 `aiPlan`/PlanPanel/持久化能力未删除，仍可承载已有真实计划；新输入不再创建空计划。
- 实现：`InputComposer.vue` 移除 Plan 下拉项、`/plan` slash 命令和 `createPlan(..., [])` 分支；`AssistantHeader.vue` 仅显示 Chat/Goal/Agent；`switchMode("plan")` 对旧会话状态降级为 Chat。
- `T`：`npm exec vitest run src/components/ai-assistant/InputComposer.test.ts src/stores/aiAssistant.test.ts`，2 files / 91 tests passed；完整 P0 前端回归 7 files / 333 tests passed。
- `I`：真实 Chromium `http://127.0.0.1:9245/#/ai-window` 的模式选择器实际选项为 `chat`、`goal`、`agent`，不含 `plan`。

### P1-01：统一会话级权限

- `T`：`npm exec vitest run src/components/settings/AgentSection.test.ts src/components/ai-assistant/AgentToolCalls.test.ts src/stores/agent.test.ts src/stores/app.test.ts` 已通过；`npm exec vue-tsc -- --noEmit` 已通过；Go 权限/MCP 定向回归 `go test ./services ./internal/agentcore -run 'Test.*(Permission|Approval|MCP|AgentCore|Session|ComputerUse)' -count=1` 已通过。
- `I`：真实 Chromium `http://127.0.0.1:9245/#/ai-window` -> AI Settings -> Models & behavior 实际显示且仅显示 `Always Ask`、`Assist`、`Allow All` 三个 radio；依次切换 `Assist`、`Allow All` 后 checked 状态和描述分别变为“Allow low-risk calls when the backend permits them.”、“Let the backend decide without an interactive prompt.”。页面同时显示“Security: The backend remains authoritative for every tool call.”。
- `I`：同一真实页面的 Agent tool-call surface 已加载，批准队列 DOM 使用 `data-agent-tool-calls`、逐调用 `approve`/`reject` 控件，并在流忙时禁用；后端服务仍保留危险命令、工作区边界、能力令牌、budget、CAS、审计和 fail-closed 校验。
- 结果：权限设置与批准队列 smoke 通过。证据类型为 `T`、`I`、`P`；未宣称未执行的 packaged/release 和跨平台验证。

### P1-02：reasoning effort

- 实现：`services/ai_service.go` 对 `low`、`medium`、`high` 做 provider/model capability 映射；OpenAI-compatible 使用 `reasoning_effort`，Anthropic 使用 `thinking.type=enabled` 与 `budget_tokens`。不支持或未知 provider/model 在 `SetConfig` 阶段以 `ErrNotAllowed` fail-closed；reasoning 请求不发送与其冲突的 Anthropic `temperature`。
- 实现：`frontend/src/components/settings/AiSection.vue` 查询后端 `GetReasoningCapability` 并对 unsupported/unknown 禁用选项、显示原因；测试连接与主发送路径均传递真实 `provider`、`model`、`protocol` 和 `reasoningEffort`。`frontend/src/stores/aiPermission.ts` 的 operation assignment 保留主/备用 reasoning effort 字段。
- `T`：`go test ./services -run 'TestAIService_(SendReturnsResponse|ReasoningCapability|ReasoningEffortFailsClosedWhenUnsupported|ReasoningEffortFailsClosedWhenUnknown)|TestAnthropicProtocol_Send|TestAIPermission_(SetAssignment_GetModelFor|Persistence)' -count=1 -p 1` 通过。fixture 断言 OpenAI JSON 的 `reasoning_effort=high`；Anthropic JSON 的 `thinking.enabled/budget_tokens=4096`、省略 `temperature`；unsupported/unknown 在 `SetConfig` 阶段拒绝；assignment 主/备用 reasoning round-trip 通过。
- `T`：`npm exec vitest run src/components/settings/AiSection.test.ts src/stores/aiPermission.test.ts src/stores/ai.test.ts`，3 files / 77 tests passed。覆盖设置页能力查询与测试连接参数、主发送 admission 参数、assignment 主/备用 reasoning 字段加载保存及后端拒绝时保持旧状态。
- `T`：`npm exec vue-tsc -- --noEmit` 通过。
- `T`：`go test ./services ./internal/agentcore -run 'Test.*(Reasoning|Permission|Approval|MCP|AgentCore|Session|ComputerUse)' -count=1 -p 1` 通过；`services` 与 `internal/agentcore` 回归无失败。
- `I`：真实 Chromium 打开 `http://127.0.0.1:9245/#/ai-window`，点击 AI Settings 后确认三档权限 radio、Model Permission 区域可见；点击 `New Configuration` 后编辑表单出现 `Reasoning effort`，无 provider 配置时显示 unknown/unsupported 提示并保持 reasoning 选项禁用。未执行外部 provider 请求。
- 边界：本断点证明 provider capability 映射、真实本地 HTTP 请求 fixture、前端状态传递、assignment 持久化字段和设置页渲染；未证明外部 provider 可用性、所有 operation fallback 运行时路由、打包产物和跨平台行为，均保留为未验证范围。

### P1-03-A：真实 MCP capability 模型

- 状态：`complete`（仅 P1-03-A 范围；P1-03 整体仍 pending，见剩余边界）。
- 实现：`services/mcp_capability.go`（新增）定义版本化 capability 模型：`mcpProtocolVersion="2024-11-05"`、typed `mcpInitializeRequestParams`（roots 未实现故不宣告，client capabilities 精确为 `{}`）、`MCPCapabilityState`(supported/missing/unsupported/unknown)、`MCPCapabilityReport`、`MCPCapabilitySnapshot`（绑定 ServerName、WorkspaceRoot、RootGeneration、LifecycleGeneration、全局单调 Run、EstablishedAt）。`parseMCPInitializeResult` 对缺失/不支持的 `protocolVersion`、缺失 `serverInfo.name/version`、malformed capabilities 全部 fail-closed；`parseMCPCapabilityReport` 区分 missing（未声明）、unknown（未识别键原样记录）、unsupported（sampling/elicitation/logging 恒定 unsupported 并记录 Declared）；tools/resources/prompts 声明形状错误 fail-closed。
- 实现：`services/mcp_client.go` 的 `initialize` 改为发送 typed 能力声明并解析、校验响应（移除原 `_ = resp` 丢弃）；校验通过才发送 `notifications/initialized`；快照存于当前 client run。`requireDeclaredCapability` 对 `fetchTools`/`CallTool`（tools）、`ListResources`/`ReadResource`（resources）、`ListPrompts`/`GetPrompt`（prompts）做 server capability 一致性检查：未声明即返回显式 `ErrNotAllowed` 且不发出 JSON-RPC 请求。`StartServer`/`stopTransport` 重置/清除快照；`capabilitySnapshotCopy`、`bindCapabilitySnapshot` 提供只读拷贝与服务侧绑定。
- 实现：`services/mcp_service.go` 在 `ConnectServer` 安装 client 时以当前 `root/rootGeneration/lifecycleGeneration` 原子绑定快照；新增 `ServerCapabilities`（`//wails:ignore`，非 renderer API）校验 workspace path、rootGeneration、lifecycleGeneration，过期/配置更新后 fail-closed 返回 `ErrNotAllowed`，断连/切换后返回 `ErrNotFound`。
- 实现：`services/mcp_transport.go` 抽出 `newMCPHTTPClient` 包级工厂——生产路径仍为 SSRF 安全 client（C-1 不变），仅测试可指向本地 httptest fixture。`bindings_runtime_surface_test.go` 在 mcpservice.ts ignored surface 登记 `ServerCapabilities`（renderer-deny，P1-03-D 再决定 binding 设计）。
- 实现：用仓库官方命令 `node scripts/update-bindings-manifest.mjs --accept-export-surface` 重新生成 `scripts/wails-bindings.manifest.json`：补登记此前会话已存在于生成 binding 但未登记的导出（`GetReasoningCapability`、`CloseAgentSessionForCaller`、`CreateAgentSessionForCaller`、`ExecuteAgentTool`、`ExecuteApprovedAgentTool`、`GetAgentToolCatalog`、`RequestAgentToolCapability`、`ApplySelectedHunks`、`VerifyExtensionIntegrity`）及当前文件哈希；`frontend/bindings` 生成文件未被改动，`ServerCapabilities` 因 `//wails:ignore` 不进入 manifest。
- `T`：`go test ./services -run 'TestMCP|TestG03MCP' -count=1 -p 1` 通过，82 个测试。新增 `services/mcp_capability_test.go`；受影响既有 MCP/agent/workflow/task 测试 fixture 已改为完成真实 initialize 握手并声明其真实 tools 能力（`TestMCPClient_HTTPInitializeSendsNotification`、CallTool、CacheTTL、legacy token、g03 helper、real stdio helper 等）。
- `T`：`go test . -run TestRegisteredWailsRuntimeSurfaceMatchesManifest -count=1` 通过；`node scripts/check-bindings.mjs` OK（pinned v3.0.0-alpha2.111，manifest 与生成树一致）。
- `I`：真实本地 HTTP fixture（`httptest.Server`，streamable HTTP POST JSON-RPC）：捕获 initialize 请求 `id=1`、`method=initialize`、params `{protocolVersion:2024-11-05, capabilities:{}, clientInfo:koyori-ide 1.0}`；返回完整 capabilities 后快照记录 tools/resources/prompts=supported（含 listChanged/subscribe 标志）、sampling/elicitation/logging=unsupported、`serverInfo=cap-fixture 2.3.1`、instructions 保留；随后 `notifications/initialized`（无 ID，HTTP 202）。malformed 表例（缺 protocolVersion、版本 2025-06-18、缺 serverInfo、capabilities 非对象、tools 形状错误）均使 StartServer 失败且 fixture 仅收到 1 个请求（无 initialized notification）、无快照残留。未声明 resources 时 `ListResources`/`ReadResource` 显式失败且请求记录中无任何 resources JSON-RPC 调用。
- `I`：真实 stdio 子进程 fixture（`TestHelperFakeMCPServer` reexec）：`ConnectServer`→`ServerCapabilities` 返回绑定当前 workspace root/generation 的快照（Run=N）；`DisconnectServer` 后返回 `ErrNotFound`；重连产生更大 Run 的新快照；等价 `SaveServer`（连接不变、lifecycleGeneration 递增）后快照 fail-closed `ErrNotAllowed`；`applyWorkspaceRoot` 切换后 client 全部分离、旧快照不再返回。
- 安全边界：capability 快照绑定 client run、server config 身份、workspace identity 与 lifecycle generation，停止/重连/配置更新/workspace 切换后不可复用；client capabilities 只宣告真实实现（无 roots、无 sampling），不夸大协议能力；未声明能力的 API fail-closed；快照仅是协议能力投影，不提供任何审批绕过（工具审批仍走后端 session policy 与既有 token/CAS/generation 管线）；`ServerCapabilities` 保持 renderer-deny。
- 剩余边界：roots/list 未实现故未宣告（P1-03-B）；server→client notification/request 分发未实现，sampling/elicitation/logging 收到对应请求时的显式协议错误路径在 P1-03-B 落地；resources/prompts 缓存与 list-changed（P1-03-C）、binding 暴露与真实 Wails 调用（P1-03-D）、前端 store/UI/上下文注入（P1-03-E/F）、完整 fixture 矩阵与模块回归（P1-03-G）均未完成；外部真实 MCP server、打包产物、跨平台未验证。全量回归中 `TestLanguagePackRealRustLSPToolchainAndDebug` 失败一次，单独运行与 `TestLanguagePack` 分组运行均通过，判定为负载型 flake，与 MCP 改动无共享路径。

### P1-03-B：server notification/request 分发

- 状态：`complete`（roots/list 实现为受控响应仍未做，见剩余边界；P1-03 整体仍 pending）。
- 实现：`services/mcp_transport.go` 的 `jsonrpcResponse` 增加 `Method`/`Params`（入站分类所需）；`jsonrpcRequest` 重命名为 `jsonrpcOutboundMessage` 并增加 `Error` 字段，统一表示客户端写出的 request/notification/错误响应。`services/mcp_client.go` 的 dispatcher 现按 JSON-RPC 2.0 分类：无 ID → notification；有 ID 且有 Method → server-to-client request；其余按 response ID 路由（保留 buffered(1) pending 语义与 transport 身份失效检查，stale dispatcher 直接退出，迟到消息不触碰新 run 状态）。
- 实现：三种 list-changed notification（tools/resources/prompts）各自只失效对应 cache family，并记录可审计状态 `mcpListInvalidation{generation, invalidatedAt, notifications}`；tools list-changed 置 `toolsCacheValid=false`，`listTools` 的 fetch 捕获 generation，仅在 fetch 期间未发生失效时才落缓存——并发 list 继续经 `toolsRefreshDone` 合并刷新，失效期间旧结果不能覆盖新状态。未识别/未实现的 notification（含 logging 的 `notifications/message`）与无 method 的 malformed notification 记录日志后确定丢弃。
- 实现：未实现的 server-to-client request（sampling/createMessage、elicitation、roots/list、未知方法）一律以 JSON-RPC `-32601` 显式拒绝，绝不静默丢弃；拒绝发送走 `enqueueHandler` 的有界异步路径（每 run 8 个 slot，`run.calls` 在 `c.mu` 内预留，`StopServer` 的 `run.calls.Wait` 不会漏等），transport reader 永不阻塞，发送带 5s 超时且先校验 transport 仍为当前连接。`MCPService.ConnectServer` 注册的 list-changed 回调仅在 tools 失效时触发 `notifyToolsChanged`（agent catalog 刷新同样离开 reader 线程）。
- `T`：`go test ./services -run 'TestMCPClientToolsListChanged|TestMCPClientDispatchFamilies|TestMCPClientNotificationHandler' -count=1 -p 1 -timeout 120s` 通过（3 个新测试）。
- `T`：`go test ./services -race -run 'TestMCP|TestG03MCP' -count=1 -p 1 -timeout 420s` 通过，无 DATA RACE；`go test ./services -run 'TestMCP|TestG03MCP|TestAgentExecutionCoreMCP|TestAgentExecutionCoreWorkflowMCP|TestTaskServiceWorkflowMCP|TestAgentMCP' -count=1 -p 1` 通过。
- `I`：真实 stdio 子进程 fixture（`TestHelperMCPNotificationServer` reexec，换行分隔 JSON-RPC）：cache 命中两次（tool-v1，helper 计数证明无新请求）后写入触发文件，helper 发送 `notifications/tools/list_changed`（无 ID、正确 method），客户端 `toolsInvalidation.generation=1、notifications=1、invalidatedAt 非零`，resources/prompts family 保持 0（非对应通知不刷新）；随后 `ListTools` 发出新的真实 `tools/list` 请求得到 tool-v2，再下一次命中缓存仍为 tool-v2。
- `I`：同一 fixture 按序发送 resources/prompts/tools×2 list-changed、`notifications/message`、`sampling/createMessage`(id=5001)、`roots/list`(id=5002) 与一行 malformed `{"jsonrpc":"2.0"}`：resources/prompts generation 各 +1、tools generation 精确 +2；helper 从 stdout 收到两条对 5001/5002 的响应，均为 `error.code=-32601`（sampling/roots 显式拒绝）；malformed 与 logging notification 被记录丢弃；后续 `ListTools` 正常完成 tool-v2 刷新，reader 未被阻塞。
- `I`：有界与生命周期：占满 8 个 handler slot 后第 9 个 `enqueueHandler` 返回 false 且不执行；释放后 `StopServer` 正常完成（`run.calls.Wait` 不悬挂）；停止后 `enqueueHandler` 拒绝。race 检测覆盖 dispatcher 并发路径。
- 安全边界：list-changed 只失效对应 cache 并可审计（generation/invalidatedAt/计数），不触发任何自动执行、自动注入或批准绕过；server request 一律显式协议错误，sampling/elicitation/roots 不被静默接受；handler/拒绝 goroutine 有界、离开 transport reader、绑定当前 run 并被 StopServer 等待；stale dispatcher/迟到通知在 transport 身份检查处丢弃，不能改变新连接状态；缓存失效与 fetch 的 generation 防护防止旧 catalog 覆盖新失效状态。
- 剩余边界：`roots/list` 仍未实现，当前以 `-32601` 显式拒绝且 initialize 不宣告 roots——受控 roots 响应（只返回当前 committed workspace identity 根、切换后旧根失效）需在后续子断点实现后才能满足 P1-03-H；resources/prompts 的 metadata cache、内容校验与 service 层安全读取在 P1-03-C；拒绝通知风暴时 overflow 丢弃依赖 TTL 兜底刷新；外部真实 MCP server、打包产物、跨平台未验证。

### P1-03-C：resources/prompts 缓存与安全读取

- 状态：`complete`（P1-03-C 范围；P1-03 整体仍 pending，见剩余边界）。
- 实现：`services/mcp_client.go` 为 resources/prompts 增加 metadata cache，镜像 tools cache 语义（30s TTL、`resourcesRefreshDone`/`promptsRefreshDone` 合并并发刷新、list-changed 显式失效、fetch 期间 generation 防护、`StartServer`/`stopTransport` 重置与清理）；resource/prompt 内容绝不缓存。`invalidateListCache` 现同时置空对应 family 的 cacheValid（tools/resources/prompts 三族一致）。
- 实现：`services/mcp_capability.go` 新增 `MCPResourceContent`、`MCPPromptMessage`（保留 role/content 来源）、`mcpContentByteLimit=512KiB`（刻意低于 HTTP transport 的 1MiB 读取上限，使超大载荷可携带完整信封到达校验）；`validateMCPResourceContents` 对 malformed JSON、空 contents、缺失 URI、非 text 内容类型、无类型无文本、超限文本返回稳定错误（`ErrInvalidInput`/`ErrNotFound`/`ErrNotAllowed`）；`validateMCPPromptMessages` 对空 messages、未知 role、非 text content、超限文本 fail-closed。client 的 `ReadResource` 改为返回验证后的 `[]MCPResourceContent`，`GetPrompt` 改为返回 `[]MCPPromptMessage`（不再丢失 role）；`resources/list`/`prompts/list` 结果校验 URI/name 非空，malformed 不再被当作空成功。prompt 参数仍以结构化 map 经 `encoding/json` 构造，无字符串拼接协议 JSON。
- 实现：`services/mcp_service.go` 的 `ReadResource`/`GetPrompt` 返回带来源信息的新类型 `MCPResourceRead`/`MCPPromptRender`（server、URI/prompt、contents/messages、rootGeneration、lifecycleGeneration）；四个读取方法继续在 workspace lease 之后执行，并在请求后再次 `lease.validateCurrent` 与复核 `lifecycleGeneration`，任一变化即 fail-closed `ErrNotAllowed`。方法保持 `//wails:ignore`（renderer 暴露留给 P1-03-D）。
- `T`：`go test ./services -run 'TestMCPClientResourceAndPrompt|TestMCPClientDispatchFamilies|TestMCPServiceResourcePromptReads|TestMCPClientToolsListChanged' -count=1 -p 1 -timeout 180s` 通过。
- `T`：`go test ./services -run 'TestMCP|TestG03MCP|TestAgentExecutionCoreMCP|TestAgentExecutionCoreWorkflowMCP|TestTaskServiceWorkflowMCP|TestAgentMCP|TestSSE|TestParseSSE' -count=1 -p 1 -timeout 420s` 通过；`go test ./services -race -run 'TestMCPClient|TestMCPService' -count=1 -p 1 -timeout 420s` 通过，无 DATA RACE。
- `I`：真实 stdio 子进程 fixture：resources/prompts list cache 命中（resource-v1/prompt-v1 两次读取仅一次请求）、经真实 `test/trigger` JSON-RPC 请求触发 `notifications/resources/list_changed`+`notifications/prompts/list_changed` 后再次 list 发出新的真实请求得到 resource-v2/prompt-v2、操纵 TTL 过期后刷新为 resource-v3/prompt-v3、`StopServer` 后两族缓存字段全部清空。
- `I`：真实本地 HTTP fixture（content-fixture）：`resources/read` 对 malformed result、空 contents、缺 URI、blob 类型、>512KiB 文本分别返回 `ErrInvalidInput`/`ErrNotFound`/`ErrNotAllowed` 且错误消息可诊断；合法双 content 块完整保留 URI/mimeType/text。`prompts/get` 对空 messages、system role、image content、超限文本 fail-closed；合法 render 保留 user/assistant role 与 content。请求断言：method 为 `resources/read`/`prompts/get`，params 按请求携带精确 `uri`/`name`+`arguments`。
- `I`：service 层真实 stdio fixture：`ListResources`/`ReadResource`/`ListPrompts`/`GetPrompt` 全部经真实连接完成并返回完整来源（Server、URI、Contents、generations、role/content）；`applyWorkspaceRoot` 切换后四个读取全部 fail-closed `ErrNotFound`，旧结果不能进入后续链路。
- 安全边界：内容不缓存；一切 server 返回内容先经结构/类型/大小校验；读取前后双重 lease + lifecycle generation 校验，workspace 切换/断连/配置变更均 fail-closed；prompt 内容保留 role 来源、不提升为系统权限；方法仍为 renderer-deny。
- 剩余边界：`roots/list` 受控实现仍未做（当前 `-32601` 拒绝）；service 层列表方法（ListResources/ListPrompts）尚未返回带 generation 的来源结构（读取类已返回），renderer-facing API 设计与 `//wails:ignore` 移除在 P1-03-D；前端发现/读取/注入在 P1-03-E/F；外部真实 MCP server、打包产物、跨平台未验证。

### P1-03-D：Go service 与 Wails binding 边界

- 状态：`complete`（P1-03-D 范围；P1-03 整体仍 pending，见剩余边界）。
- 实现：先用搜索确认 `ListResources`/`ReadResource`/`ListPrompts`/`GetPrompt`/`ServerCapabilities` 无任何非测试 caller（此前仅 `//wails:ignore` 挂起）；在 C 断点已具备 workspace lease、双重 generation 校验、连接状态、内容大小与结构校验之后，于 `services/mcp_service.go` 移除五个方法的 `//wails:ignore`，使其成为 renderer 可达的只读 API。deny-only 的 `CallTool`/`RequestToolApproval`/`ExecuteApprovedTool`/`Close` 保持 `//wails:ignore`，`setWorkspaceRoot`/`setWorkspaceContext`/`setOnToolsChanged` 保持 unexported。未暴露任何 `MCPClient`、transport、approval token、明文 secret 或可写的 workspace root setter。
- 实现：`scripts/lib/wails-bindings.mjs` 的 binding 策略更新：mcpservice.ts 的 `requiredExports` 增加 `ListResources`、`ReadResource`、`ListPrompts`、`GetPrompt`、`ServerCapabilities`；`forbiddenExports` 中移除这四项读取方法（保留 CallTool/Close/ExecuteApprovedTool/RequestToolApproval/SetOnToolsChanged）。随后用仓库官方命令重新生成：`node scripts/generate-bindings.mjs` 与 `node scripts/update-bindings-manifest.mjs --accept-export-surface`；生成的 `frontend/bindings/.../mcpservice.ts` 新增五个导出（`ReadResource` 返回 `$models.MCPResourceRead`、`GetPrompt` 返回 `$models.MCPPromptRender`、`ServerCapabilities` 返回 `$models.MCPCapabilitySnapshot`），models.ts 新增对应类型。
- `T`：`go test . -run 'TestRegisteredWailsRuntimeSurfaceMatchesManifest' -count=1` 通过；`node scripts/check-bindings.mjs` OK（manifest 与生成树一致、ByName=0、forbidden/required 策略满足）。
- `T`：`go test ./services -run 'TestMCPServiceRendererBindingContract|TestMCPServiceRendererReadAPIsFailClosed|TestMCPService_SetWorkspaceRoot|TestMCPServiceRootSetter|TestMCPServiceSetWorkspaceRoot' -count=1 -p 1` 通过。新增 `TestMCPServiceRendererBindingContract`：断言生成的 mcpservice.ts 真实导出五个读取 API 且签名与 Go 形状一致（含 `$models.MCPResourceRead`/`MCPPromptRender`/`MCPCapabilitySnapshot`），并断言 `CallTool(`、`RequestToolApproval(`、`ExecuteApprovedTool(`、`SetWorkspaceRoot(`、`setWorkspaceContext(`、`Close(` 均不在导出面（deny-only 未复活、内部 setter 不可达）。
- `T`：新增 `TestMCPServiceRendererReadAPIsFailClosedWithoutWorkspace`：无 committed workspace identity 时四个读取 API 直接 `ErrNotAllowed`、`ServerCapabilities` 未连接时 `ErrNotFound`，renderer 无法借新 API 绕过 workspace 准入。
- `I`：真实 Wails/service 调用：`TestMCPServiceResourcePromptReadsFailClosedOnWorkspaceSwitch_StdioFixture` 经真实 stdio MCP 子进程连接后，通过 service 层（即 renderer binding 将调用的同一 Go 方法）完成真实 list/read/get（resources list、resource read、prompts list、prompt get），并在 workspace 切换后全部 fail-closed；binding surface 测试在真实 `appBundle` 注册的服务实例上用反射核对运行时方法面与生成 manifest 完全一致。
- `T`：`cd frontend && npm exec vue-tsc -- --noEmit` 通过（新 binding 类型进入前端类型面后无类型错误）；`go test ./services -run 'TestMCP|TestG03MCP|TestAgentExecutionCoreMCP|TestTaskServiceWorkflowMCP' -count=1 -p 1` 通过。
- 安全边界：renderer 只获得只读、lease/generation/连接/大小四重校验后的数据；执行仍只能走 AgentService 统一 capability 管线，deny-only shim 行为与 binding 不可达同时成立；workspace root、server enable、tool approval、session generation 的权威值全部来自后端（`ServerCapabilities` 的 generation 校验由后端在每次调用时复核）；生成文件全部由官方生成命令产出，无手工伪造导出。
- 剩余边界：`roots/list` 受控实现仍未做（`-32601` 显式拒绝）；前端 store/UI 消费新 binding 在 P1-03-E/F；真实 Wails 运行时（浏览器内）调用 smoke 归 P1-03-F；外部真实 MCP server、打包产物、跨平台未验证。

### P1-03-E：前端 MCP store 与上下文状态

- 状态：`complete`（P1-03-E 范围；UI 消费在 P1-03-F）。
- 实现：`frontend/src/stores/mcp.ts` 新增镜像 Go 的类型（`MCPResource`、`MCPPrompt`、`MCPCapabilityState/Feature/Report/Snapshot`、`MCPResourceContent`、`MCPResourceRead`、`MCPPromptMessage`、`MCPPromptRender`）；state 增加 `serverContexts`（按 server 分组，status/resources/prompts/capabilities/lifecycleGeneration）与 `contextsWorkspaceRoot`；`McpBackend`/`McpBindingsShape`/默认 Wails adapter 增加 `serverCapabilities`/`listResources`/`readResource`/`listPrompts`/`getPrompt`（全部经 adapter，未绕过 transport；空返回显式抛错，不伪造成功）。
- 实现：状态机 `McpListStatus`（unloaded/loading/loaded/stale/unsupported/error/empty）按家族区分：capability 声明缺失时 family=unsupported 且不发 list 调用；list 失败保留可诊断 error；空列表=empty；能力快照被后端拒绝（stale/workspace）时整体 error 并保留消息。`refreshMcpServerContext`/`readMcpResource`/`getMcpPrompt`/`injectMcpResourceContext`/`injectMcpPromptContext`/`clearMcpServerContext`/`clearStaleMcpContexts`/`markMcpWorkspaceChanged` 为显式 action。
- 实现：注入为显式用户动作：未连接拒绝（`not connected; context injection refused`）、stale 拒绝（`stale; refresh before injecting`）；同一来源按确定性 id（`mcp-res:<server>:<uri>` / `mcp-prompt:<server>:<prompt>`）去重替换，不产生重复 chip；内容超 `mcpContextInjectionBudget`(64Ki) 截断并带显式标记；chip 携带 `mcpServer/mcpUri/mcpPrompt/mcpGeneration` 来源字段。断开/禁用/删除/配置协调（disconnect、toggle、delete、loadMcpServers reconcile）将对应 server 状态置 stale 并清扫其注入 chip；`markMcpWorkspaceChanged` 置全部 stale 并清扫全部 MCP chip（同根 no-op）。
- 实现：`frontend/src/types/index.ts` 的 `ContextChip` 增加可选 `mcpServer/mcpUri/mcpPrompt/mcpGeneration`；`frontend/src/stores/ai.ts` 的 `buildUserMessage` mcp 分支对带内容 chip 输出 `MCP context from <server> (<label>):` 前缀（无内容 chip 保持 `MCP tool:` 旧语义），进入既有 chip→消息序列化管线；新增 `upsertContextChip`（按 id 替换）。`frontend/src/stores/workspaceStore.ts` 在 workspace 快照提交后以惰性 import 调用 `markMcpWorkspaceChanged(primaryRoot)`（空工作区同样失效；空工作区分支在其守卫之前执行）。
- `T`：`cd frontend && npm exec vitest run src/stores/mcp.test.ts`，18 tests passed。覆盖：成功加载、空结果、unsupported（未声明家族不发起 list 调用）、家族独立 RPC 错误、capability 快照被拒、重复注入去重替换、budget 截断标记、未连接/stale 注入拒绝、断开清扫+stale、workspace 切换全量 stale+清扫+同根 no-op、按需清理。
- `T`：`npm exec vitest run src/stores/ai.test.ts src/stores/app.test.ts src/stores/mcp.test.ts`，3 files / 130 tests passed（修复了 workspace 惰性清扫与 vitest teardown 竞争的 unhandled rejection：清扫惰性导入补 catch）；`npm exec vitest run src/stores/ai.test.ts src/stores/agent.test.ts src/stores/mcp.test.ts src/stores/aiAssistant.test.ts`，218 tests passed；`npm exec vue-tsc -- --noEmit` 通过。
- `I`（既有真实后端证据，覆盖 adapter 调用的同一组方法）：Go 侧真实 stdio 子进程 fixture 已证明 `ServerCapabilities`/`ListResources`/`ReadResource`/`ListPrompts`/`GetPrompt` 的真实 JSON-RPC 请求、响应、generation 校验与 workspace 切换 fail-closed（P1-03-C/D 段）；本断点 store mock 仅作为前端单元证据，不重复宣称后端闭环。
- 安全边界：注入必须是显式动作且带 server/URI/prompt/generation 来源；未连接/stale 拒绝注入；断开、禁用、删除、配置协调与 workspace 切换都会把对应内容移出可发送队列（chip 清扫可注入测试钩子，默认惰性实现带 catch）；注入内容有预算上限且截断显式可见；store 不保存任何 MCP secret（masked Headers/Env 仍由后端处理）；无第二套发送协议——注入内容经既有 context chip 序列化进入 `buildUserMessage`。
- 剩余边界：设置/AI surface 的 UI 控件与真实 Chromium smoke 在 P1-03-F；`roots/list` 受控实现仍未做；外部真实 MCP server、打包产物、跨平台未验证。

### P1-03-F：MCP 设置/AI 上下文 UI 与真实应用 smoke

- 状态：`pending`（UI 实现与部分真实应用证据完成；已批准连接后的发现/读取/注入 UI 投影 smoke 标记 U，见下。2026-08-29 用户指示该 smoke 由用户本人手动验证，自动化收尾停止，见 U 段末）。
- 实现：`frontend/src/components/settings/ai/McpSection.vue` 复用现有布局新增按 server 的上下文面板：能力摘要（tools/resources/prompts/sampling/elicitation/logging 逐项显示 supported/未声明/unsupported/unknown，未知键原样展示，协议版本与 serverInfo 可见）、资源列表（URI 完整 title 不截断、名称/mime、逐条"注入"按钮）、prompt 列表（名称/描述、注入按钮）、家族级状态徽章（unloaded/loading/loaded/stale/unsupported/error/empty）与可诊断错误内联展示、`清理过期` 工具栏按钮、展开面板时自动刷新未加载上下文。补齐 en/zh/ja locale（`mcpSection.context*`/`cap*`/`family*`/`inject*` 等 33 键 × 3 语言）。
- `T`：`cd frontend && npm exec vitest run src/components/settings/ai/McpSection.test.ts`，6 tests passed（新增组件测试：连接才显示上下文按钮、能力/unsupported/错误状态投影、资源/prompt 注入控件绑定 store action 与成功反馈、stale/error 状态投影、展开自动刷新、清理过期 action）。`npm exec vitest run src/lib/i18n.test.ts src/stores/mcp.test.ts` 54 tests passed（locale 三语 parity）；`npm exec vue-tsc -- --noEmit` 通过；`npm exec vitest run src/stores/ai.test.ts src/stores/app.test.ts src/stores/mcp.test.ts` 130 tests passed；回归组（ai/agent/mcp/aiAssistant）218 tests passed。
- `I`：真实应用 smoke（实际 `wails3 dev` 启动的 koyori-ide.exe 桌面应用 = 真实 Wails backend + WebView2，工作区 `%USERPROFILE%\koyori-mcp-smoke` 经 UI 打开，独立构建的真实 stdio MCP fixture 进程 `mcp-fixture-server.exe` 作为目标服务器）：
  - 真实 UI 完成"新增服务器"对话框（名称 smoke-fixture / stdio / 命令指向 fixture exe）→ 保存 → 后端 `SaveServer` 真实持久化（磁盘 `mcp-servers.json` 出现该条目，密钥加密管线无涉）。
  - 真实 UI 点击"启用"→ 原生同意对话框（G-SEC-12）真实弹出，展示 Server/Transport/Target/Arguments/Environment entries 并要求确认。自动化点击落在"否(N)"→ 后端真实拒绝：UI 内投影出可诊断错误 `enable MCP server "smoke-fixture" was not approved by the native consent boundary: not allowed`（RuntimeError），开关保持关闭、连接按钮保持禁用——真实的 fail-closed 错误状态展示与同意边界证据。
  - 真实 fixture 进程的协议行为（initialize capability 声明、resources/list、resources/read 的 JSON-RPC 请求/响应）由 fixture 自身请求日志记录（`mcp-fixture.log`），此前已用于 P1-03-C 的真实 stdio 服务级闭环。
- `U`：**已批准启用 → 连接 → 上下文发现/读取/注入在真实 UI 中的正路径投影**。阻塞原因：G-SEC-12 原生同意对话框只能由真人在原生对话框上点击"是"完成；自动化（AXPress、真实 OS 鼠标事件、UIA Toggle/Invoke、键盘助记符）多次尝试均被同意边界拒绝或未命中（拒绝本身即边界生效的证据）。该正路径的协议与 store 逻辑已由真实 Go 服务级 stdio fixture（P1-03-C/D）与前端组件/store 测试分别证明；缺的仅是"真实 UI 中已批准连接后的投影"这一环。需要真人在场点击一次"是"后复跑本 smoke。
  - 2026-08-29 更新：用户明确指示"不需要进行 F smoke 验证，后续我自己验证"，自动化收尾停止，本 U 转交用户手动验证。补充证据：fixture 请求日志 `%USERPROFILE%\koyori-mcp-smoke\mcp-fixture.log` 保留了一次真实会话（2026-08-28T23:51:45+08:00）——IDE 客户端（clientInfo=`koyori-ide`）发出 `initialize` → 收到服务器 result（protocolVersion 2024-11-05，tools/resources/prompts 能力）→ 发出 `notifications/initialized` → `resources/list` → `resources/read`，fixture 全部成功应答。即真实应用中至少一次"已批准启用→连接→发现→读取"闭环在协议层真实发生（该 fixture 端口只接收来自 IDE 的 stdio 连接）。未捕获的仅是 UI 投影截图与注入 chip 的最终确认，由用户手动复跑。
  - 用户验证环境已备妥：独立 `projects.json` 已指向 `%USERPROFILE%\koyori-mcp-smoke`，`%LOCALAPPDATA%\koyori-ide\mcp-servers.json` 已持久化 smoke-fixture（stdio，enabled=false）。步骤：启动应用打开该工作区 → AI 设置→上下文与工具 → 点"启用" → 原生对话框点"是(Y)" → 点"连接" → 展开"上下文"面板核对能力/资源/prompt 投影 → 注入资源与 prompt → AI 窗口确认 `MCP context from smoke-fixture` chip。
- 安全边界：新增服务器默认禁用并需显式启用（原生同意对话框展示完整服务器身份）；同意拒绝时后端 fail-closed 且错误可诊断地投影到 UI；注入按钮只在前端表达意图（实际读取/注入仍走后端 lease/generation 校验）；store 不保存任何 MCP secret（masked Headers/Env 由后端处理）。环境说明：2026-08-29 起 smoke 环境按用户指示保留供本人验证——`projects.json` 当前为隔离 smoke 内容（用户原文件备份在同目录 `projects.json.smoke-backup`，验证完成后恢复），`mcp-servers.json` 保留 smoke-fixture 条目（默认禁用，加密密钥管线无涉）；此前一轮 smoke 已验证过恢复流程可行。
- 剩余边界：已批准连接后的上下文面板投影、资源/prompt 注入到 AI 上下文 chip 的真实 UI smoke（U，待真人在场批准）；`roots/list` 受控实现；P1-03-G fixture 矩阵收口与模块级回归、P1-03-H 完成判定；外部真实 MCP server、打包产物、跨平台未验证。

### P1-03-G：fixture 矩阵与模块级回归收口

- 状态：`complete`（本断点范围；P1-03 整体仍 pending——F 正路径 smoke 为 U、roots 受控实现未做，见 H 段清单）。
- fixture 矩阵 → 测试/记录映射（全部为真实传输：`httptest.Server` 真实 TCP HTTP 或真实 stdio 子进程；scripted transport 仅作为既有单元测试层补充）：
  1. initialize（检查 protocolVersion/clientInfo/client capabilities=精确 `{}`，返回 serverInfo/capabilities）：`TestMCPClientInitializeCapabilitySnapshot_HTTPFixture`（真实 HTTP）+ `TestHelperFakeMCPServer`/`TestHelperMCPNotificationServer`/`TestG03MCPProtocolHelperProcess`/`TestAgentMCPRealStdioHelper`（真实 stdio 子进程）。
  2. initialized notification（无 ID、正确 method）：`TestMCPClientInitializeCapabilitySnapshot_HTTPFixture`（第二个请求断言）+ stdio fixture 应答路径。
  3. tools/list、tools/call（稳定 schema 与结果）：`TestMCPClientCapabilityGating_HTTPFixture`、`TestMCPClientToolsListChangedForcesRefetch_StdioFixture`、`TestMCPClient_CallTool_Success/RPCFailure`。
  4. resources/list、resources/read（正常文本资源 + 错误资源）：`TestMCPClientResourceAndPromptListCaches_StdioFixture`、`TestMCPServiceResourcePromptReadsFailClosedOnWorkspaceSwitch_StdioFixture`、HTTP 内容校验表例（malformed/空/缺 URI/blob/超限，`TestMCPClientResourceAndPromptContentValidation_HTTPFixture`）。
  5. prompts/list、prompts/get（argument schema + 多条 message content）：stdio helper `greet`（含 arguments 声明、user+assistant 两条消息）+ HTTP 校验表例（空/未知 role/非文本/超限）。
  6. tools/resources/prompts list-changed（cache 命中后触发刷新）：`TestMCPClientToolsListChangedForcesRefetch_StdioFixture`（真实通知→新 JSON-RPC 请求 tool-v2）、`TestMCPClientResourceAndPromptListCaches_StdioFixture`（两族各自失效+TTL）、`TestMCPClientDispatchFamiliesRejectionsAndMalformed_StdioFixture`（非对应通知不串扰、重复通知精确计数、合并刷新）。
  7. roots/list（若实现）：**决策——本阶段不实现 roots**。当前以 JSON-RPC `-32601` 显式拒绝且 initialize 不宣告 roots（`TestMCPClientDispatchFamiliesRejectionsAndMalformed_StdioFixture` 断言 id=5002 收到 -32601）；受控 roots 响应保留为 P1-03-H 前的待办，未把"拒绝"冒充为完成。
  8. unsupported request / malformed / RPC error / 超时 / 断开与停止：
     - unsupported server request（sampling/createMessage id=5001、roots/list id=5002 均收 `-32601`）与 logging notification、malformed notification（`{"jsonrpc":"2.0"}`）：`TestMCPClientDispatchFamiliesRejectionsAndMalformed_StdioFixture`。
     - malformed initialize 结果（缺 protocolVersion、版本不支持、缺 serverInfo、capabilities 非对象、tools 形状错误）：`TestMCPClientInitializeRejectsInvalidResults_HTTPFixture`（6 子例，均无 initialized notification、无快照残留）。
     - RPC error：`TestMCPClient_CallTool_RPCFailure`、resources/prompts 家族错误表例。
     - 超时（真实 HTTP 传输挂起 + 客户端 deadline）：新增 `TestMCPClientRequestTimeout_HTTPFixture`——deadline 触发 `context.DeadlineExceeded`、pending 清零、后续请求正常完成（tools/list 计数=2）。
     - 断开/停止：`TestMCPClientSnapshotClearedOnStopAndReplacedOnReconnect`、`TestMCPClient_StopServerUnblocksPending_B1`、`TestMCPClient_BlockedSendDoesNotBlockDispatchOrStop_B1`、dispatcher stale-exit；并发与 race：`go test -race`（MCP 全集无 DATA RACE）。
  9. workspace switch、server disable、reconnect 后旧 response/notification/context/approval 不生效：`TestG03MCPApprovalLeaseCannotSelectSameNamedClientAfterSwitch`、`TestMCPService_WorkspaceRootChangeDisconnectsAndInvalidatesApproval`、`TestMCPServiceCapabilitySnapshotLifecycle_StdioFixture`、`TestMCPServiceResourcePromptReadsFailClosedOnWorkspaceSwitch_StdioFixture`、`TestMCPClientDispatchFamiliesRejectionsAndMalformed_StdioFixture`（等价 SaveServer 后快照 fail-closed）。
- 测试层次（按 prompt-a 建议落位）：协议解析/dispatcher/cache 并发/malformed 边界在 `services/mcp_capability_test.go` + `services/mcp_service_test.go`；workspace lease/generation/连接状态/资源与 prompt 安全读取在 `services/mcp_service_test.go` + `services/mcp_capability_test.go`；Wails 暴露边界与 deny-only 在 `services/mcp_service_binding_test.go` + `bindings_runtime_surface_test.go`；前端 store 语义在 `frontend/src/stores/mcp.test.ts`；UI 状态投影在 `frontend/src/components/settings/ai/McpSection.test.ts`。
- `T`：`go test ./services -run 'TestMCP|TestG03MCP|TestAgentExecutionCoreMCP|TestAgentExecutionCoreWorkflowMCP|TestTaskServiceWorkflowMCP|TestAgentMCP|TestSSE|TestParseSSE|TestProjectServiceWorkspaceSwitch' -count=1 -p 1 -timeout 600s` → ok（33.1s，含新增超时用例）。
- `T`：`go test . -run 'TestRegisteredWailsRuntimeSurfaceMatchesManifest' -count=1` → ok；`node scripts/check-bindings.mjs` → OK（pinned v3.0.0-alpha2.111，manifest 与生成树一致，ByName=0）。
- `T`：`cd frontend && npm exec vitest run src/stores/mcp.test.ts src/components/settings/ai/McpSection.test.ts src/stores/ai.test.ts src/components/settings/AiSection.test.ts` → 4 files / 99 tests passed；`npm exec vue-tsc -- --noEmit` → 通过。
- `T`：`go test ./services -race -run 'TestMCP|TestG03MCP' -count=1 -p 1 -timeout 420s` → ok，无 DATA RACE（P1-03-B 段记录，本轮复核无 MCP 并发改动）。
- 边界（诚实记录，不冒充完成）：SSE transport 的真实 MCP-over-SSE e2e fixture 未建立（SSE 传输层有 `TestSSETransport_*`/`TestParseSSE*` 覆盖，MCP 会话层 e2e 走 stdio/HTTP）；scripted-transport 用例（CacheTTL/CallTool 等）保持单元层定位，最终证据以真实 HTTP/stdio fixture 为准；`roots/list` 受控实现未做。

### P1-03-H：完成判定审计

- 状态：`pending`（**P1-03 未完成**——按本断点逐项审计，仅剩一项 U；未满足前不得宣称 P1-03 完成）。
- 实现（roots 受控实现，补齐第 5 条）：`services/mcp_capability.go` 的客户端能力声明现在宣告 `roots:{}`（listChanged=false：本客户端连接不跨 workspace switch 存活，后端在切换时分离全部连接，变更通知不可能触发——如实声明）；`services/mcp_client.go` 实现 `roots/list` 受控响应：仅返回连接建立时由 `MCPService` 盖戳的 committed workspace root（`setRootsWorkspaceRoot` 在 `ConnectServer` 启动传输前写入，根来自后端 lease，绝不经 renderer 或任意路径）；根未绑定（无工作区）时诚实返回空 `roots:[]`；workspace switch 后旧连接被分离、无法再应答（旧根不可得）。`jsonrpcOutboundMessage` 增加 `Result` 字段以承载成功响应。sampling/elicitation/未知方法仍显式 `-32601` 拒绝。
- `T`：`go test ./services -run 'TestMCP|TestG03MCP' -count=1 -p 1 -timeout 600s` → ok（20.3s）。其中 `TestMCPClientDispatchFamiliesRejectionsAndMalformed_StdioFixture` 现断言：真实 stdio fixture 发送 `roots/list`(id=5002) 后客户端返回**成功**结果且 payload 恰为绑定根 `file:///C:/fixture-root`（name=fixture-root），`sampling/createMessage`(id=5001) 仍为 `-32601`；`TestMCPClientInitializeCapabilitySnapshot_HTTPFixture` 与 `TestMCPClient_HTTPInitializeSendsNotification` 断言 initialize 能力声明恰为 `{"roots":{}}`。
- 逐项判定（对照本断点条件）：
  1. initialize 真实且不夸大的 client capabilities — **满足**（恰为 `{"roots":{}}`，roots 有真实 handler）。
  2. server capabilities 解析、校验、投影并绑定 lifecycle/workspace generation — **满足**（A：快照校验 + 绑定 + staleness fail-closed）。
  3. tools/resources/prompts 真实发现；resources 真实读取；prompts 真实获取 — **满足**（C：真实 stdio/HTTP 服务级闭环）。
  4. list-changed 使对应 cache 失效并刷新 — **满足**（B/C：真实通知→新请求）。
  5. roots 与 committed workspace identity 绑定；切换后旧根、旧连接、旧批准和旧上下文不可复用 — **满足**（本轮实现 + 既有 workspace switch 失效测试；旧连接被分离故旧根不可答）。
  6. sampling/elicitation/logging 明确 unsupported 未静默丢弃 — **满足**（快照显式记录 + 请求 `-32601` + 通知可观测日志）。
  7. 前端可发现、读取、显式注入上下文并显示错误/unsupported/stale — **部分满足**：store/组件逻辑与状态机全有测试；真实 UI 已证明错误投影（同意拒绝错误）与 MCP 分区渲染；但"已批准连接后的发现/读取/注入正路径投影"为 **U**（F 段）。
  8. binding 真实生成且真实调用通过；无 renderer 直接 transport/任意 URI bypass — **满足**（D：真实生成 + 反射 surface + 真实服务级调用；浏览器层调用并入第 7 条 U）。
  9. 真实 HTTP fixture 断言 method、params、请求顺序、IDs、结果和错误 — **满足**（G 矩阵）。
  10. Go、前端定向测试、类型检查和真实 Wails/browser/service smoke 均有结果 — **部分满足**：Go/前端/类型检查/服务级 smoke 均有通过结果；browser 层 smoke 为 U（同第 7 条）。
- `U`（唯一剩余阻塞）：**真实 UI 的已批准连接正路径 smoke**。操作步骤已备妥（fixture 二进制、工作区、UI 流程），唯一缺口是原生同意对话框上的"是(Y)"必须由真人点击（自动化点击被同意边界按设计拒绝——该拒绝已作为错误投影证据记录）。真人点击后按 F 段步骤复跑：连接 → 展开"上下文"面板（能力/资源/prompt 投影）→ 注入资源与 prompt → AI 窗口确认 chip → 记录证据即可关闭本 U。（2026-08-29 用户指示转由用户本人手动验证；fixture 日志中保留的一次真实"连接→发现→读取"会话记录见 F 段更新。）
- 结论：除上述 U 外，P1-03 的代码实现与可自动化验证全部完成且有真实证据；P1-03 不得标记为 complete，直至该 U 由用户手动验证关闭（2026-08-29 用户指示自行验证）。P1-04 已完成（见下段），AC-01~AC-10 最终矩阵已编制（第 9 节）。

### P1-04：Git 前端完整投影

- 状态：`complete`（代码与可自动化验证范围；真实 UI smoke 标 U，见剩余边界）。
- Inspect 结论（本轮实测差距）：窄侧栏已有 branch、ahead/behind、staged/unstaged 分组、Untracked/Renamed 标签、冲突区块（ours/theirs/editor/markResolved）、rebase banner + abort/continue、operation error 内联与 noRepo 引导；四个真实缺口——①截断仅静态提示且 >1000 的行被 store 丢弃（AC-07"长列表可继续查看"不满足）；②后端 `go-git` Status 不做 rename 检测，`git mv` 投影为两个无关的 Deleted+Added 行（renamed 不可准确确认）；③同一文件既有 staged 又有 unstaged 变更时，两行各自的 Diff 按钮都调用 `GetDiff`，而其语义固定 staged 优先——unstaged 行显示的是错误一侧的 diff（文件身份不一致）；④`statusClass` 缺 Renamed 样式。
- 实现（后端）：`services/git_service.go` 的 `GitFileChange` 增加 `OldPath`（`omitempty`，仅用于被证明的 staged rename）。`services/git_repository.go` 的 `getStatus` 在投影前调用 `detectStagedRenames`：staged Deleted 与 staged Added 按 blob hash 配对（HEAD 树中旧路径 blob == index 中新路径 blob）才合并为单行 `Renamed`（Path=新名、OldPath=旧名）；内容不同的 delete+add 保持两行不猜测；HEAD/index 读取失败降级为普通行不失败；配对遍历按字典序保证确定性。`services/git_diff.go` 新增 `GetDiffForSide(repoPath, filePath, staged)`：`staged=true` diff HEAD vs index，`staged=false` diff index vs worktree，完全 untracked 回退 all-additions；`GetDiff` 保持既有 staged-first 语义不变。
- 实现（binding）：官方管线重生成（`frontend` 的 `npm run bindings:generate` → `node scripts/generate-bindings.mjs`）产出 `gitservice.ts` 的 `GetDiffForSide` 与 models.ts 的 `oldPath` 字段；`node scripts/update-bindings-manifest.mjs --accept-export-surface` 接受新导出面（47 service modules、55 files）；`scripts/lib/wails-bindings.mjs` 的 gitservice `requiredExports` 增加 `GetDiffForSide` 哨兵防回退。
- 实现（前端）：`types/index.ts` 的 `GitFileChange` 增加 `oldPath?`；`api/git.ts` 增加 `getDiffForSide`；`DiffView.vue` 增加可选 `staged` prop（undefined 保持旧 `getDiff` 语义），watch 依赖含 `staged`（同一文件另一行重新打开时取正确一侧）。`GitPanel.vue`：renamed 行显示 `old → new`（title 同）并新增 `git-panel__status--renamed` 样式；renamed 行的 Unstage 组合调用 `unstageFile(new)` + `unstageFile(old)`（否则 rename 会以悬空删除重现）；staged 行 diff 传 `staged=true`、unstaged 行传 `false`（diff 与行身份一致）；截断区从死胡同提示改为 status + `git-load-more` 按钮（显示剩余行数）。`stores/git.ts`：完整状态保留在模块级 `_allChanges`，`gitState.changes` 只是可见窗口，新增 `totalChanges` 与 `loadMoreGitChanges()`（每次扩一页，返回剩余隐藏行数）；`clearGitState` 同步复位。locale en/zh/ja 各增 `git.loadMoreChanges`。
- `T`：`go test ./services -run 'TestGit' -count=1 -p 1 -timeout 600s` → ok（52.3s，全 Git 组回归）。4 个新测试（真实 go-git 临时仓库 fixture，非 mock）单独 `-v` 确认 PASS：`TestGitService_Status_projectsProvenStagedRename`（真实 staged rename → 恰一行 Renamed new.txt←old.txt）、`TestGitService_Status_keepsUnprovenDeleteAddPair`（不同内容 → 恰两行且无 Renamed/OldPath）、`TestGitService_GetDiffForSide_returnsRowIdentitySide`（同文件 staged"v2"+unstaged"v3"：staged 侧含 `+v2` 不含 `+v3`，unstaged 侧含 `+v3` 不含 `+v2`）、`TestGitService_GetDiffForSide_untrackedFallsBackToAllAdditions`。
- `T`：`go test . -run 'TestRegisteredWailsRuntimeSurfaceMatchesManifest' -count=1` → ok；`node scripts/check-bindings.mjs` → OK（pinned v3.0.0-alpha2.111、manifest 与生成树一致、ByName=0）。
- `T`：`cd frontend && npx vitest run src/components/layout/GitPanel.test.ts` → 43 passed（新增 4：renamed 行 `old → new` 渲染与 R 徽章、renamed 行 Unstage 两次调用精确顺序 new→old、DiffView 接收所点击行的 staged 维度、截断按钮显示剩余 500 并触发续读 action）；`npx vitest run src/stores/git.test.ts` → 28 passed（新增 3：2500 条保留全量投影 1000 窗口、续读 2000→2500 且返回剩余数、非截断续读 no-op）；`npx vitest run src/lib/i18n.test.ts src/api/gitService.test.ts src/api/gitAdvancedBindings.test.ts` → 45 passed（三语 locale parity 与 API 面回归）。
- `T`：`npm exec vue-tsc -- --noEmit` → 通过。
- AC-07 对照：branch/ahead/behind/staged/unstaged/untracked/conflict/rebase/error 为既有投影（既有 GitPanel/store 测试覆盖）；renamed 与 truncation continuation 为本轮补齐；diff/stage/unstage 的文件身份统一为 `(repoPath, 仓库相对路径, staged 维度)`，外部刷新经全量 `refreshGit` 使用同一身份，无第二套身份通道。
- 诚实边界（不冒充完成）：①hunk 级 apply/reject 操作在产品中不存在，本轮不发明新功能；apply/reject 语义由冲突解决的 ours/theirs/markResolved（MergeEditor + 后端 ResolveConflict）承担，均以同一相对路径身份操作。②Git 面板范围无独立 git 内容 CAS 机制（CAS 语义在 ai.ts 消息接纳层与 diffservice 事务，不在 Git 投影路径）。③renamed 行的 diff 视图对新路径显示 all-additions（go-git 对 rename diff 的固有限制），行身份与操作仍一致。④ahead/behind 依赖 origin upstream 引用，无上游时诚实显示 0。⑤**`U`：真实桌面应用的 Git 面板投影 smoke 未跑**（当前 dev 实例的二进制早于本轮 Go 改动）；GitPanel/store 行为由组件与 store 测试覆盖，真实 UI 投影与 F smoke 一并留待真人验证或后续断点。

## 9. AC-01~AC-10 证据矩阵（2026-08-29 编制）

逐项映射本文件既有证据段；`T`=自动化测试、`I`=真实集成、`P`=打包产物、`U`=外部阻塞/未验证。

| AC | 结论 | 证据（本文件段落） | 未验证/U |
| --- | --- | --- | --- |
| AC-01 AI 输入接纳 | 满足 | P0-01：vitest 240+309 组；真实 Chromium 草稿保留/回滚 smoke | 打包产物、跨平台 |
| AC-02 native tool calling | 满足 | P0-02：T（agent/ai/MessageList/i18n、45 测试组、vue-tsc、Go native 组）+ I（Chromium 真实 renderer） | 外部 provider、打包产物 |
| AC-03 真实 provider 多轮 | 满足（本地 fixture） | P0-03：真实 HTTP/SSE 双协议 round-trip、stream 边界/超时/停释放 | **U：外部真实 provider 访问**；打包产物 |
| AC-04 三档权限 | 满足 | P1-01：T（权限/审批/AgentCore 回归）+ I（真实页面仅三档 radio、批准队列 DOM、后端权威声明） | 打包产物、跨平台 |
| AC-05 reasoning effort | 满足（映射+fixture） | P1-02：T（OpenAI `reasoning_effort`/Anthropic thinking 断言、fail-closed、77 前端组）+ I（设置页渲染） | **U：外部 provider 真实请求**；operation fallback 运行时路由 |
| AC-06 MCP | 大体满足，一项 U | P1-03-A~H：真实 HTTP/stdio fixture 全矩阵、roots 受控响应、binding 面、store/组件 99 组 | **U：已批准连接的真实 UI 正路径 smoke（转交用户手动验证，2026-08-29）**；外部真实 MCP server；打包产物 |
| AC-07 Git 投影 | 满足（自动化），UI smoke U | P1-04：go git 全组 52.3s ok、4 新 fixture 测试、GitPanel 43/git store 28/i18n 45、vue-tsc、binding surface | **U：真实桌面 UI 投影 smoke**；打包产物 |
| AC-08 IDE 核心路径 | 部分满足 | AI agent：P0-02/03 真实 Chromium+HTTP；Git：P1-04 真实仓库 fixture；LSP 工具链：TestLanguagePack（一次负载型 flake 已记录，分组通过） | **U：editor/terminal/search 的真实运行操作验证未在本 Goal 系统性执行** |
| AC-09 安全 | 满足（无回归证据） | 各段安全边界：G-SEC-12 同意拒绝错误投影（P1-03-F）、workspace lease/generation fail-closed（P1-03-C/D）、validateFilePath 路径安全（既有 Git 测试）、deny-only binding 面（P1-03-D、surface 测试） | 打包产物、跨平台 |
| AC-10 回归与交付 | 满足（记录义务） | 本文件各段均记录命令/fixture 身份/结果与失败原始信息；本节即未验证范围清单 | **U：packaged/release、跨平台、外部网络验证未执行（不得改写为完成）** |

## 10. 后续执行入口

下一轮实现必须读取并遵守 `docs/prompts/prompt-a.md`。该文件是从当前工作区断点继续执行的长期、可追溯、可审查执行合同；它不替代本文件的历史证据，也不把未验证能力改写为完成。
