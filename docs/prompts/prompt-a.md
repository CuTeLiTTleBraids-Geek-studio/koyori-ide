# Prompt A：P16 长期收口执行合同

> 用途：把本文件直接交给下一轮 AI coding agent，使其从当前工作区断点继续执行 P16。  
> 权威关系：`docs/prompts/prompt-16.md` 保留历史审查结论和已记录证据；本文件规定后续执行顺序、边界、验收和证据格式。两者冲突时，以当前源码、测试实际结果和最新用户指令为准；不得把本文件中的计划当作已经完成的事实。

---

## 0. 直接执行指令

你正在继续当前工作区的 P16 Goal：**Codex 级 AI / IDE 能力收口**。

不要重新规划项目，不要创建第二个 Goal，不要从空仓库开始，不要把“源码存在、组件存在、binding 已生成、局部 mock 通过、静态页面加载”当作功能完成。先读取：

1. 当前 Goal 状态和 todo 状态。
2. `docs/prompts/prompt-16.md` 全文。
3. 本文件全文。
4. 受影响源码、测试、API/binding 边界和已有证据。
5. 工作区状态；保留用户已有修改。

从 P1-03 的第一个未完成子任务继续。只有在 P1-03 形成真实 MCP 闭环后，才进入 P1-04；只有 P0/P1 的真实证据完整后，才进入 P2。每次只推进一个最小可验证断点，并立即执行：

```text
Inspect -> Implement -> Verify -> Evidence -> Update
```

每个断点结束时必须同时完成源码、测试/fixture、真实 smoke（若适用）、证据记录和 todo 更新；不得停在计划、代码审查、binding 生成或单个局部测试。

---

## 1. 总目标与不可变不变量

用户输入到最终结果必须稳定形成以下闭环：

```text
用户输入
-> 稳定流式输出
-> 原生 tool call
-> 后端权限决策
-> 工具执行
-> 原生 tool result
-> provider 下一轮请求
-> 验证结果
-> 必要时继续修复
-> 最终回答
```

必须持续保持这些不变量：

1. 一次用户输入只产生一个权威会话轮次；成功接纳后输入框不保留已发送草稿。
2. 一次 provider tool-call 轮次只执行一次；同一调用不会重复执行、重复展示或生成重复 tool result。
3. native tool call 是默认且唯一权威主路径；fenced protocol 只能是明确标记的兼容 fallback。
4. tool call 的 catalog、schema validation、approval、execute、result、dedupe 共用一条后端权威管线。
5. UI、前端状态和后端权限不是三套互相覆盖的策略；renderer 只能表达意图，后端是最终安全边界。
6. 所有可能影响工作区、外部网络或外部副作用的动作都可被看见、批准、拒绝、取消或恢复。
7. 批准不能绕过路径安全、workspace sandbox、危险命令阻断、能力 token、budget、CAS、审计、workspace generation 和 fail-closed。
8. workspace 切换、server 重连、配置改变和 session generation 改变后，旧连接、旧批准、旧 catalog、旧上下文不能复用。
9. 所有“已实现”声明必须有真实请求、真实服务调用或真实 IDE 操作证据。
10. 不展示隐藏思维推断为 reasoning；timeline 只能显示 provider 明确返回的 reasoning summary。

---

## 2. 当前工作区断点

### 2.1 已有历史证据，不要无故重做

`docs/prompts/prompt-16.md` 已记录以下断点的实现和验证。下一轮默认把它们视为已有历史证据；修改共享路径时必须做受影响回归，不能以历史记录替代当前变更后的验证：

| 断点 | 当前记录的事实 | 历史证据边界 |
|---|---|---|
| P0-01 | `sendMessageInternal` 的 stream admission、busy、异常草稿保留、optimistic rollback、stale stream 和双窗口语义已修复 | 仅覆盖已记录的前端定向测试与真实 Chromium 无 provider 拒绝场景；不扩展为所有 provider/runtime 完成 |
| P0-02 | native/fence 来源、调用 ID、去重、统一 catalog/schema/approval/execute/result 管线和 UI 混用显示规则已收口 | fence 仍是显式兼容 fallback；不能删除旧符号或旧路径，除非重新确认 caller、binding、locale、测试和文档 |
| P0-03 | OpenAI-compatible 与 Anthropic 的本地 HTTP 多轮 fixture 已记录工具调用、原生 tool result、第二轮请求、最终文本和状态恢复 | 外部 provider、打包产物和跨平台行为仍未证明 |
| P0-04 | Plan 输入入口已隐藏，旧计划后端能力未删除；不再创建空 steps 伪闭环 | 不得重新暴露空 Plan 入口；若要恢复只能实现完整真实 plan generation/edit/approve/execute/pause/resume/persist |
| P1-01 | 仅保留 Always Ask、Assist、Allow All 三档会话级权限，后端保留硬安全边界 | 不得恢复 per-tool always-ask/auto-approve/never-approve 或 MCP `autoApprove[]` 用户策略 |
| P1-02 | `low`/`medium`/`high` 已进入 OpenAI `reasoning_effort`、Anthropic `thinking/budget_tokens`；不支持/未知 provider/model fail-closed；设置和 assignment 已传递 | 外部 provider、所有 fallback 运行时路由、打包和跨平台仍未证明 |

### 2.2 当前正在处理：P1-03 MCP 上下文能力

当前盘点已经确认以下事实，下一轮必须基于它们继续，不得另起第二套 MCP 客户端：

1. `services/mcp_transport.go` 已定义 MCP 配置、tools、resources、prompts、content 和 JSON-RPC transport；已有 stdio、SSE、streamable HTTP 基础。
2. `services/mcp_client.go` 已有 request multiplexing、`initialize`、`notifications/initialized`、`tools/list`、`tools/call`、`resources/list`、`resources/read`、`prompts/list`、`prompts/get`。
3. 当前 `initialize` 发送的 `capabilities` 是空对象，响应被 `_ = resp` 丢弃；这不满足真实 client capabilities 和 server capability 校验。
4. 当前 dispatcher 主要按 response ID 匹配 pending call；没有形成可观察的 server notification/request 分发边界，因此 list-changed 和 roots 等能力不能假定已经工作。
5. 当前 MCP client 只有 tools TTL cache；resources/prompts 没有同等级缓存/失效语义。
6. `services/mcp_service.go` 已有 workspace lease、root generation、lifecycle generation、连接/断开和后端安全边界；`ListResources`、`ReadResource`、`ListPrompts`、`GetPrompt` 当前带有 `//wails:ignore`，不能把它们当作 renderer 可用 API。
7. 当前 `frontend/src/stores/mcp.ts` 只投影 server、connected、agent tools；`McpBackend` 和 binding shape 也只包含 config、tools、agent tools，缺少 resources/prompts/capabilities/context actions。
8. 当前生成 binding `frontend/bindings/.../services/mcpservice.ts` 仅导出配置、连接、tools 和 `ListAgentMCPTools` 等方法；不能手工修改生成文件来伪造后端能力。
9. 当前 `frontend/src/components/settings/ai/McpSection.vue` 只展示 server 和 agent tools，没有 resources/prompts 发现、读取、上下文注入和明确错误状态。
10. 当前 MCP 测试已有 transport/安全/配置/部分 HTTP initialize 覆盖，但旧注释仍说明没有真正连接 MCP server 的集成测试；必须补真实本地 fixture，不能只扩展 mock handler 断言。

上述第 3 至第 10 项是 P1-03 的未完成边界；完成前不得在 P16 文档中写成完成。

---

## 3. 全局工程与安全规则

### 3.1 研究规则

- 先读现有调用者、测试和 binding，再改 exported symbol。
- 修改 exported symbol 前必须使用 LSP 查 references；跨文件 rename/refactor 使用 LSP，不用文本替换掩盖遗漏。
- 复用现有 MCP client、MCPService、workspace lease、lifecycle generation、agent catalog、approval 和 Wails binding 生成流程。
- 不创建第二套 Agent、权限、tool catalog、MCP client、消息协议或上下文注入管线。
- 发现用户已有未提交修改时，读取并在其基础上工作；不执行 `git reset --hard`、`git checkout --`、删除无关文件、commit、push 或 tag。
- 不为了通过测试降低安全边界，不把错误转成空成功，不添加 fake fallback、no-op、占位实现或 `TODO: implement`。

### 3.2 MCP 安全规则

- server 仍默认禁用，启用仍是显式 native consent；不要恢复持久化 enabled 权威或 renderer 自行批准。
- 远程 URL 继续使用现有 SSRF 校验和实际拨号复核；stdio 命令继续使用 workspace root、canonical executable identity 和进程树终止约束。
- resources/read 和 prompts/get 也必须经过连接状态、workspace lease、root/lifecycle generation、大小/预算和错误边界；不得绕开 `MCPService` 直接在 renderer 发 HTTP。
- 资源 URI 不是任意文件路径。服务端返回的 URI 必须保持 MCP 语义；若产品要求 workspace 文件资源，必须验证 URI 与当前 workspace identity 的绑定，拒绝跨 workspace 或外部越界。
- MCP server 返回的内容是外部不可信输入：限制响应体/文本大小，避免把任意内容当作 system prompt 或安全指令；上下文注入必须是用户显式动作并带来源元数据。
- sampling、elicitation、logging 等尚未实现的能力必须显示为 unsupported，并在收到对应请求时明确拒绝/返回协议错误；不得静默接受后丢弃。
- capability 宣告必须与真实实现一致。没有实现 server-to-client request/notification 处理时，不能在 initialize 中宣告对应 capability。
- list-changed 只能使对应 catalog cache 失效；不得因为通知直接执行工具、自动注入上下文或绕过批准。

### 3.3 验证规则

- 最小相关测试通过后，运行受影响模块测试。
- 后端 MCP 改动必须用真实本地 HTTP fixture 或服务调用；不能只用 `MCPClient` 假 transport 作为最终证据。
- UI 改动必须用真实 Chromium 驱动实际页面；静态 Vite 页面缺少 Wails backend 时只能记录 renderer 证据，不得宣称 backend 闭环。
- 每个断点都记录命令、测试名称、fixture 身份、请求/响应摘要、状态转换、失败原因、证据类型 `T/I/P/U`。
- 用户已经报告且工作区证据已记录的结果不需要重复跑同一命令；但源码发生影响时必须跑受影响回归。
- 不使用一次全量测试掩盖未覆盖的边界；测试必须能在 plausible bug 下失败。

---

## 4. P1-03 实现路线：MCP 上下文能力闭环

按以下顺序逐个关闭子断点。每个子断点都必须有代码、测试和证据；没有真实证据就保持 pending。

### P1-03-A：建立真实 MCP capability 模型

目标：initialize 不再发送空能力对象并丢弃响应，而是建立明确、版本化、fail-closed 的能力快照。

执行要求：

1. 盘点 MCP 2024-11-05 现有协议类型和项目实际支持范围；定义后端可序列化的 client/server capability model。字段只包含真正实现的能力。
2. initialize 请求至少正确表达已实现的 roots、notification 或其他 client capability；若 roots/list 尚未实现，先不要宣告 roots。
3. 解析并校验 server 返回的 `protocolVersion`、`capabilities`、`serverInfo`；保留 capability snapshot，区分 `supported`、`unsupported`、`unknown` 或等价状态。
4. 对 resources、prompts、tools 的 server capability 与实际 list/call API 做一致性检查。服务器宣称不支持的操作必须产生明确错误或空能力状态，而不是静默成功。
5. 对 sampling、elicitation、logging 等未实现能力提供显式 unsupported 状态和可观察错误路径。
6. capability snapshot 必须绑定当前 client run、server config identity、workspace identity 和 lifecycle generation；停止、重连、配置更新或 workspace 切换后不可复用。
7. 不把 server capability 当作 renderer 可绕过的权限；capability 只是协议能力，工具审批仍走后端 session policy。

验收：

- fixture 捕获 initialize JSON，断言 client capabilities 与真实实现一致。
- fixture 返回完整、缺失、未知和 malformed capabilities，断言解析状态和错误边界。
- unsupported sampling/elicitation/logging 被明确记录，不能被当成成功处理。
- reconnect/workspace switch 后旧 capability snapshot 不再被返回或使用。

### P1-03-B：补齐 server notification/request 分发

目标：MCP server 发来的 notification/request 不被 dispatcher 丢弃，并且只进入受控的后端回调边界。

执行要求：

1. 区分 JSON-RPC response、notification 和 server-to-client request；处理无 ID 的 notification 与有 ID 的 request，保留 JSON-RPC malformed/error 语义。
2. 支持并测试：
   - `notifications/tools/list_changed`
   - `notifications/resources/list_changed`
   - `notifications/prompts/list_changed`
3. 每种 list-changed 只失效对应 cache，并带 cache generation/invalidated-at 之类可审计状态；并发 list 请求不能出现重复刷新、旧结果覆盖新结果或永久等待。
4. 若实现 workspace roots，则支持受控 `roots/list` server request：只返回当前 committed workspace identity 的根，禁止返回旧 workspace、任意本地路径或 renderer 自行伪造的根。
5. 对 sampling、elicitation、logging 等未实现 server request 返回明确 unsupported protocol error；不得阻塞 transport reader。
6. 回调通知必须在客户端生命周期内安全注销；StopServer、workspace 切换和 config mutation 后迟到通知不能改变新 session 状态。
7. 保留 `sendMu`、pending waiter、transport close 和 context cancellation 的并发不变量；不得在 transport reader 中调用会阻塞或持有 client mutex 的 renderer 回调。

验收：

- 真实 fixture 在 list cache 命中后发送对应 list-changed，再次 list 必须产生新的真实 JSON-RPC 请求。
- 非对应通知不导致其他 cache 无故刷新。
- 迟到通知、重复通知、关闭期间通知、malformed notification 均有确定结果且无 goroutine 泄漏。
- roots 请求只返回当前 workspace，切换 workspace 后旧请求不能得到旧根。

### P1-03-C：完成 resources/prompts cache 与安全读取

目标：tools/resources/prompts 都可发现；资源可读取；prompt 可渲染；缓存只在允许的生命周期内有效。

执行要求：

1. 在 `MCPClient` 上按现有 tools cache 模式补 resources/prompts metadata cache，包含 TTL、并发 refresh 合并、显式 invalidation、fresh query 和停止清理。
2. 不默认缓存任意 resource content；如确需缓存，必须说明大小、TTL、workspace/session 绑定和失效理由，并提供测试证明不会泄漏旧内容。
3. `ListResources`、`ReadResource`、`ListPrompts`、`GetPrompt` 在 service 层统一经过 workspace lease 和当前 generation 校验；连接断开、配置改变、server disabled、workspace 切换都 fail-closed。
4. 校验返回结构：空 contents、缺失 URI、未知 content type、过大文本、malformed JSON、RPC error、context cancellation 都要返回稳定错误；不能把 malformed response 当空成功。
5. prompt arguments 使用结构化 schema/现有类型边界，不用字符串拼接构造协议 JSON；返回消息保留 role/content 来源，不要把任意 prompt 内容提升为系统权限。
6. 资源读取结果和 prompt 内容只作为不可信上下文，必须保留 server、URI/name、mime type、generation 等来源信息供前端和 AI 消息链使用。

验收：

- 真实 fixture 断言 `resources/list`、`resources/read`、`prompts/list`、`prompts/get` 的精确 method、params、返回结构和错误。
- list cache 命中、TTL 过期、list-changed 后刷新各有计数断言。
- workspace 切换发生在请求前后时，结果被拒绝且不进入 renderer/AI 历史。
- 内容大小、malformed response、RPC error 和取消均 fail-closed。

### P1-03-D：完成 Go service 与 Wails binding 边界

目标：只有经过后端安全边界的方法才可被 renderer 调用，且 binding 真实由 backend 生成。

执行要求：

1. 先用 LSP/搜索确认 `ListResources`、`ReadResource`、`ListPrompts`、`GetPrompt`、capability/status 方法的所有 caller、测试和 binding 关系。
2. 设计稳定的 renderer-facing API：返回 resources/prompts/capability/status 和安全错误；不要把 `MCPClient`、transport、approval token、plaintext secret、workspace root setter 暴露给 renderer。
3. 仅在 service 方法已经具备 workspace lease、generation、连接、大小和错误校验后移除不应存在的 `//wails:ignore`；若某方法不能安全暴露，保持 deny-only 并实现明确的替代读取方法。
4. 运行项目既有 Wails binding 生成命令，确认生成的 TypeScript binding 和 models 与真实导出一致；禁止手改自动生成文件来制造导出。
5. 更新 binding contract test，证明危险内部 setter、旧 token pipeline、任意 client/transport 仍不可达；同时证明 resources/prompts/status 的新 API 真实可达。
6. workspace root、server enable、tool approval、session generation 的权威值必须来自后端；renderer 返回的 bool、generation 或 server capability 不能覆盖后端状态。

验收：

- Go binding contract test 通过。
- 生成 binding 中新方法的参数/返回形状与 Go 一致，旧 deny-only 方法未意外恢复。
- 至少一个真实 Wails/service 调用完成 list/read/get，不能只检查 binding 文件文本。

### P1-03-E：补齐前端 MCP store 与上下文状态

目标：前端能够发现、读取、选择和注入 MCP resources/prompts，并展示能力和错误；所有实际调用走 backend adapter。

执行要求：

1. 扩展 `frontend/src/stores/mcp.ts` 的类型、state、backend interface、default binding adapter、reset 和测试注入，不绕过 adapter 直接 import transport。
2. 设计按 server 分组的 resources/prompts/capabilities/context state，至少能区分：未加载、加载中、已加载、当前 generation 过期、unsupported、error、empty。
3. 新增显式 action：刷新 metadata、读取 resource、获取 prompt、清理 stale context；action 失败保留可诊断错误，不伪造空成功。
4. 资源/prompt 注入必须是用户显式选择，带来源标签（server/name/URI/generation），并进入现有 AI message/context 管线；不得另造第二套发送协议。
5. 资源内容和 prompt 内容必须经过长度/token budget 限制、重复去重和 generation 检查；server 断开或 workspace 切换后从可发送上下文中移除。
6. 不在 store 或组件保存 MCP secret；继续依赖后端 masking。
7. 写测试覆盖成功、空结果、unsupported、RPC 错误、stale generation、重复注入、server disconnect 和 workspace switch。

验收：

- store mock 只作为 unit evidence；另有真实 backend fixture/服务调用证明真实 JSON-RPC 请求走通。
- state 能从 loading 到 loaded/error/stale 正确转换，不残留旧 server 的 resources/prompts。
- AI 发送前能观察到用户选定的 MCP context 来源和内容；拒绝/断开/切换后不会发送过期上下文。

### P1-03-F：补齐 MCP 设置和 AI 上下文 UI

目标：用户可以在实际设置/AI surface 发现 MCP server 的 tools/resources/prompts 和能力状态，并明确看到错误与 unsupported。

执行要求：

1. 复用 `McpSection.vue` 的现有布局、i18n、风险提示和安全边界；不要创建第二个 MCP 设置页。
2. 在真实连接状态下展示 resources、prompts、server capabilities 和 unsupported 能力；未连接、加载中、空列表、读取失败、generation stale 都有明确状态。
3. 提供可访问的展开/详情/读取/选择控件；长 URI、mime type、描述和错误不能被截断到无法确认身份，必要时使用 tooltip/详情面板。
4. “注入上下文”必须表达显式用户意图，不得自动把全部资源/prompt 加进 system prompt；显示来源和取消/移除状态。
5. 连接、断开、启用、禁用、workspace 切换后刷新 UI；旧内容不应继续显示成当前可用。
6. 所有新文案补齐 en/zh/ja locale parity；不要用 UI 文案掩盖 backend unsupported。
7. 真实 Chromium smoke 必须在实际 Wails backend 环境执行；静态 Vite 缺 backend 时只记录受限 renderer 证据。

验收：

- 真实页面可发现 server、工具、资源、prompt、能力和错误。
- 真实读取一个 resource、真实获取一个 prompt，并确认来源/状态投影到 UI 和 AI context。
- 断开或切换 workspace 后，旧条目不可继续选择/发送。

### P1-03-G：P1-03 真实 fixture 与模块级回归

至少建立一个可重复的本地 MCP HTTP fixture，必要时补 stdio/SSE fixture；fixture 必须是实际 HTTP/transport 请求，不是把 client 内部 handler 当 server。

fixture 至少支持并记录：

1. initialize：检查 protocolVersion、clientInfo、client capabilities，返回 serverInfo/capabilities。
2. initialized notification：检查无 ID、method 和 params。
3. tools/list、tools/call：返回稳定 tool schema 和结果。
4. resources/list、resources/read：返回 text resource 和错误 resource。
5. prompts/list、prompts/get：返回 argument schema 和多条 message content。
6. tools/resources/prompts list-changed notification：在 cache 已命中后触发刷新。
7. roots/list（若实现）：检查返回的 workspace root 和 generation 绑定。
8. unsupported request、malformed response、RPC error、超时、断开和停止生成。
9. workspace switch、server disable、reconnect 后旧 response/notification/context/approval 不生效。

建议测试层次：

- `services/mcp_client_test.go` 或现有 MCP 测试文件：协议解析、dispatcher、cache 并发和 malformed 边界。
- `services/mcp_service_test.go`：workspace lease、generation、连接状态、资源/prompt 安全 service API。
- `services/mcp_service_binding_test.go`：Wails 暴露边界和 deny-only 内部能力。
- `frontend/src/stores/mcp.test.ts`：store state/action/error/generation 语义。
- `frontend/src/components/settings/ai/McpSection.test.ts` 或现有组件测试：UI 状态、可访问控件、错误/unsupported/来源投影。

最低命令示例，必须按仓库真实脚本调整：

```text
go test ./services -run 'TestMCP' -count=1 -p 1 -v
npm exec vitest run src/stores/mcp.test.ts src/components/settings/ai/McpSection.test.ts
npm exec vue-tsc -- --noEmit
```

定向测试通过后，再运行受影响 services/frontend 模块回归。不得只运行新测试文件而跳过已有 MCP、agent catalog、permission、workspace 和 i18n 测试。

### P1-03-H：P1-03 完成判定

只有以下全部成立，才可把 P1-03 标记完成：

- initialize 发送真实且不夸大的 client capabilities。
- server capabilities 被解析、校验、投影并绑定 lifecycle/workspace generation。
- tools/resources/prompts 都能真实发现；resources 能真实读取；prompts 能真实获取。
- tools/resources/prompts list-changed 能让对应 cache 失效并刷新。
- roots 与 committed workspace identity 绑定；workspace 切换后旧根、旧连接、旧批准和旧上下文不可复用。
- sampling、elicitation、logging 等未实现能力明确 unsupported，未静默丢弃。
- 前端可发现、读取、显式注入上下文并显示错误/unsupported/stale。
- binding 是真实生成且真实调用通过；不存在 renderer 直接 transport/任意 URI bypass。
- 真实 HTTP fixture 断言 method、params、请求顺序、IDs、结果和错误。
- Go、前端定向测试、类型检查和真实 Wails/browser/service smoke 均有结果。
- `prompt-16.md` 已记录准确证据和边界，未将局部 mock 或 binding 存在写成完成。

---

## 5. P1-04：Git 前端完整投影

P1-03 完成后，继续检查真实窄侧栏及其后端 API、store、binding、diff/hunk 和测试。不得只加状态字段或文案。

### 5.1 必须实现并真实展示

- branch
- ahead/behind
- staged
- unstaged
- untracked
- renamed（old/new identity）
- conflict
- rebase
- operation error
- truncation state 和继续查看路径

### 5.2 实现要求

1. 先定位 Git service、store、窄侧栏组件、diff/AI diff/hunk/apply/reject/stage/unstage caller 和 binding。
2. 以真实 repository fixture 产生每一种状态；不要用静态 store mock 作为最终证据。
3. 长列表必须能分页、加载更多、路径筛选或详情查看；仅显示“已截断”不合格。
4. 长路径和 rename old/new 必须可通过 tooltip、展开或详情准确确认。
5. 文件身份必须统一：path、oldPath/newPath、status、hunk、CAS token、apply/reject、stage/unstage 和外部刷新使用同一 identity 规则。
6. CAS 失效、外部刷新、repository operation error 必须清除或标记 stale action，不能对错误文件继续写入。
7. branch/ahead/behind 与 file status 的刷新必须能区分 loading、empty、error、stale 和 truncated。
8. UI 不得新增第二个 Git 状态模型；复用已有 service/store/binding。

### 5.3 P1-04 验收

- Go Git fixture/测试覆盖所有状态、rename identity、conflict/rebase/error、truncation continuation、CAS 和外部刷新。
- 前端定向测试覆盖真实状态投影、加载更多/筛选/详情和 stale action。
- 真实 Chromium 在实际应用中可看到窄侧栏，并完成一次长列表继续查看、rename 详情、冲突/rebase/error 状态操作。
- evidence 明确是 `T`、`I` 或 `U`，不得把 repository mock 当作真实 IDE smoke。

---

## 6. P2：默认曝光和遗留路径收口

只有 P0/P1 的真实证据稳定后处理 P2。每项删除/隐藏前都必须查 caller、binding、locale、测试、文档和运行时入口；不为了减少代码提前删除仍被后端或恢复流程使用的服务。

按以下顺序处理：

1. 重复的模型权限分配和成本仪表盘：确认用户可见权限仍只有三档，会话级 assignment 不出现第二套策略。
2. legacy MCP auto-approve：确认没有 renderer caller、binding、locale、文档和测试依赖后移除或隐藏；保留必要的后端兼容解析时必须 deny-only 或明确迁移。
3. 默认 AI 设置中的低价值配置：先确认保存/读取/assignment/provider 请求 caller，再决定隐藏或删除。
4. Goal、Workflow、Computer Use、IM、PProf、Database、HTTP、扩展兼容等高级功能：默认入口只保留真实可用路径；空入口、假成功、手工补步骤和误导性文案必须删除或隐藏。
5. 没有调用者的旧协议、重复 UI、旧文案和空入口：用 references、binding、locale、测试和真实路由证明无 caller 后再清理。

P2 的每次变更仍须有项目级测试和真实 smoke；不能把“组件未显示”作为功能正确性或安全正确性的唯一证据。

---

## 7. 最终 AC-01 至 AC-10 关闭矩阵

最终报告必须逐项列出状态、证据命令/操作、关键结果、证据类型和剩余边界。

| AC | 必须证明 |
|---|---|
| AC-01 AI 输入接纳 | 成功流只清空一次；busy/拒绝/异常保留草稿；无 optimistic 残留或 streaming 泄漏 |
| AC-02 native tool calling | native 调用进入统一 catalog/schema/approval/execute/result；ID 稳定；不重复执行/展示 |
| AC-03 真实 provider 多轮 | OpenAI-compatible 与 Anthropic 真实 HTTP 请求、原生 result、下一轮和最终回复 |
| AC-04 三档权限 | 后端最终决策；所有工具类型共用会话策略；硬边界不被 renderer 绕过 |
| AC-05 reasoning effort | low/medium/high 进入支持 provider 真实 JSON；unsupported/unknown 明确显示并 fail-closed |
| AC-06 MCP | tools/resources/prompts 发现、读取、注入和错误展示；capability、通知、roots、generation、批准和 cache 正确 |
| AC-07 Git 投影 | 窄侧栏完整状态、长列表继续查看、rename/conflict/rebase/error/truncation 和统一 identity |
| AC-08 IDE 核心路径 | editor、terminal、search、Git、LSP、AI agent 真实运行操作通过 |
| AC-09 安全 | path、approval、capability token、budget、CAS、workspace generation、audit、fail-closed 无回归 |
| AC-10 回归交付 | 受影响 Go/前端/fixture/smoke 全有可复现命令和结果；未验证范围标 U |

“完成”必须是全部相关条件成立；任何一个未验证项都写 `pending` 或 `U`，不能用源码存在、局部测试、静态 binding、安装成功或 UI 文案替代。

---

## 8. 证据记录合同

每个断点在 `docs/prompts/prompt-16.md` 的执行证据区追加一段，使用以下结构：

```markdown
### P1-XX：<断点名称>

- 状态：`complete` / `pending` / `U`。
- 实现：<真实修改的文件、符号、数据流和安全边界>。
- `T`：`<准确命令>`；<测试文件/测试名、通过数量、覆盖边界>。
- `I`：<fixture/server/repository/browser 身份>；<准确操作、请求/响应摘要、状态转换和最终结果>。
- `P`：<仅当真实打包产物被运行并验证时填写；构建成功本身不算激活>。
- `U`：<外部 provider、平台、打包、权限或其他未完成阻塞；保留原始错误和影响范围>。
- 安全边界：<path/approval/token/budget/CAS/generation/audit/fail-closed 证明>。
- 剩余边界：<没有被本断点证明的能力；不能省略>。
```

请求/响应摘要至少包含：

- fixture 身份和 transport。
- JSON-RPC method、request ID、关键 params。
- response ID、result/error、tool/resource/prompt identity。
- 多轮消息顺序、generation/session identity（如适用）。
- UI 或 service 的最终状态：loading、connected、stale、error、terminal、stream released 等。

失败记录必须保留原始命令和错误，不得改测试期望、吞异常或改成空结果来“通过”。

---

## 9. Todo、Goal 和交付纪律

- 使用当前 Goal，不创建第二个 Goal。
- todo 必须按本文件的真实阶段更新：开始时 `in_progress`，证据完成后立即 `done`；外部阻塞才标 `blocked` 并写原因。
- 不要把“盘点完成”标成“能力完成”；“binding 生成”不等于“真实调用”；“mock handler 被调用”不等于“真实 fixture 闭环”。
- 每次编辑前在 commentary 说明修改文件和原因；编辑后重新读取变更区段，避免 stale anchor 和误覆盖用户改动。
- 若工具输出与工具说明不一致，按 harness 规则报告工具问题，不用猜测内容继续编辑。
- 不运行 destructive git 命令，不 commit/push/tag。
- 最终响应使用简体中文，先列未完成/风险，再列已完成变更和证据；不要宣称 P16 完成，除非 AC-01 至 AC-10 全部有证据。

---

## 10. 下一步唯一动作

恢复后立即执行：

1. 读取当前 Goal/todo、`docs/prompts/prompt-16.md` 和本文件。
2. 将 P1-03 的“盘点 MCP 能力与现有边界”标记为完成前，先把盘点结果与实际代码逐项核对；若无新事实，进入 P1-03-A。
3. 实现 P1-03-A 的最小 capability model 和 initialize fixture。
4. 运行该断点的最小 Go 测试，记录真实请求摘要。
5. 再进入 P1-03-B；不要同时跨到前端或 Git，除非 A/B 的接口已经由测试固定。

停止条件不是“代码写完”，而是每个验收条件都有可复现证据，所有剩余能力都有明确 `pending` 或 `U` 记录。
