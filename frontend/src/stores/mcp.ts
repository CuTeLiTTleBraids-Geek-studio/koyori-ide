/**
 * Plan 11 Task 4 Step 5 — MCP（Model Context Protocol）前端 store。
 *
 * 后端 `services/mcp_service.go` 的前端对应物。职责：
 *   - 管理用户配置的 MCP server 列表（增删改查 + 持久化在后端）。
 *   - 触发连接/断开（stdio 子进程 / SSE / HTTP transport）。
 *   - 暴露 agent 可用的 MCP 工具（`mcp.<server>.<tool>` 命名空间）。
 *
 * 安全（G-SEC-02 / G-SEC-09 / G-SEC-12）：
 *   - 新增 server 默认 Enabled=false（Restricted），需用户显式启用。
 *   - MCP 工具默认 RiskElevated；write/exec/network 类 RiskDangerous。
 *   - 配置文件由后端 atomicWriteJSON 写入，0600 权限。
 *   - 前端不缓存任何密钥/凭据；Headers 中的 token 由后端处理。
 *
 * 采用与 `extensionSecurity.ts` 相同的「lazy bindings + 可注入 backend」
 * 模式，便于单元测试注入 mock。
 */
// Koyori IDE 模块 · Mcp。
// 喵，这是 Koyori IDE 的 Mcp 模块（前端实现）~

import { reactive, computed, ref } from "vue";
import { errorMessage } from "@/lib/errors";
import type { ContextChip } from "@/types";

// ---------------------------------------------------------------------------
// 类型 — 镜像 Go 结构体（services/mcp_service.go）
// ---------------------------------------------------------------------------

/** Transport 类型：stdio（子进程）/ sse（Server-Sent Events）/ http（流式 HTTP）。 */
export type MCPTransport = "stdio" | "sse" | "http";

/** 单个 MCP server 的配置。镜像 Go `MCPServerConfig`。 */
export interface MCPServerConfig {
  name: string;
  transport: MCPTransport;
  /** stdio transport 专用：可执行命令路径。 */
  command?: string;
  /** stdio transport 专用：命令参数。 */
  args?: string[];
  /** stdio transport 专用：子进程环境变量。 */
  env?: Record<string, string>;
  /** sse / http transport 专用：服务器 URL。 */
  url?: string;
  /** sse / http transport 专用：自定义请求头（含鉴权 token）。 */
  headers?: Record<string, string>;
  /**
   * 是否启用。G-SEC-12：新增 server 默认 false，需用户显式启用
   * （等同 Restricted 扩展的显式审批）。
   */
  enabled: boolean;
}

/** MCP server 暴露的工具。镜像 Go `MCPTool`。 */
export interface MCPTool {
  name: string;
  description?: string;
  inputSchema?: Record<string, unknown>;
}

/** Agent 可用的 MCP 工具（带命名空间与风险分级）。镜像 Go `AgentMCPTool`。 */
export type RiskLevel = "safe" | "elevated" | "dangerous";

export interface AgentMCPTool {
  /** 命名空间标识：`mcp.<server>.<tool>`。 */
  namespace: string;
  server: string;
  tool: string;
  description: string;
  inputSchema: Record<string, unknown>;
  riskLevel: RiskLevel;
}

/** MCP 工具调用结果。镜像 Go `MCPToolResult`。 */
export interface MCPToolResult {
  content: Array<{ type: string; text?: string }>;
  isError: boolean;
}

// ---------------------------------------------------------------------------
// P1-03-E 类型 — 镜像 Go services/mcp_capability.go / mcp_client.go
// ---------------------------------------------------------------------------

/** MCP server 暴露的资源。镜像 Go `MCPResource`。 */
export interface MCPResource {
  uri: string;
  name: string;
  description?: string;
  mimeType?: string;
}

/** MCP server 暴露的 prompt 模板。镜像 Go `MCPPrompt`。 */
export interface MCPPrompt {
  name: string;
  description?: string;
  arguments?: Array<Record<string, unknown>>;
}

/** capability 解析状态。镜像 Go `MCPCapabilityState`。 */
export type MCPCapabilityState = "supported" | "missing" | "unsupported" | "unknown";

/** 单个 capability 特性。镜像 Go `MCPCapabilityFeature`。 */
export interface MCPCapabilityFeature {
  state: MCPCapabilityState;
  declared: boolean;
  listChanged?: boolean;
  subscribe?: boolean;
}

/** capability 报告。镜像 Go `MCPCapabilityReport`。 */
export interface MCPCapabilityReport {
  tools: MCPCapabilityFeature;
  resources: MCPCapabilityFeature;
  prompts: MCPCapabilityFeature;
  sampling: MCPCapabilityFeature;
  elicitation: MCPCapabilityFeature;
  logging: MCPCapabilityFeature;
  unknown?: string[];
}

/** initialize 校验结果的服务端身份。镜像 Go `MCPServerIdentity`。 */
export interface MCPServerIdentity {
  name: string;
  version: string;
}

/** capability 快照。镜像 Go `MCPCapabilitySnapshot`（绑定 workspace/generation）。 */
export interface MCPCapabilitySnapshot {
  protocolVersion: string;
  serverInfo: MCPServerIdentity;
  instructions?: string;
  capabilities: MCPCapabilityReport;
  serverName: string;
  workspaceRoot?: string;
  rootGeneration: number;
  lifecycleGeneration: number;
  run: number;
  establishedAt: string;
}

/** 校验后的资源内容块。镜像 Go `MCPResourceContent`。 */
export interface MCPResourceContent {
  uri: string;
  mimeType?: string;
  text: string;
}

/** 带来源信息的资源读取结果。镜像 Go `MCPResourceRead`。 */
export interface MCPResourceRead {
  server: string;
  uri: string;
  contents: MCPResourceContent[];
  rootGeneration: number;
  lifecycleGeneration: number;
}

/** 保留 role/content 的 prompt 消息。镜像 Go `MCPPromptMessage`。 */
export interface MCPPromptMessage {
  role: string;
  content: { type: string; text?: string };
}

/** 带来源信息的 prompt 渲染结果。镜像 Go `MCPPromptRender`。 */
export interface MCPPromptRender {
  server: string;
  prompt: string;
  messages: MCPPromptMessage[];
  rootGeneration: number;
  lifecycleGeneration: number;
}

/**
 * 单个 server 上下文/发现状态。区分：未加载、加载中、已加载、generation
 * 过期（stale）、能力未声明（unsupported）、错误、空列表。
 */
export type McpListStatus = "unloaded" | "loading" | "loaded" | "stale" | "unsupported" | "error" | "empty";

/** 按 server 分组的发现状态。 */
export interface McpServerContextState {
  status: McpListStatus;
  resourcesStatus: McpListStatus;
  resources: MCPResource[];
  resourcesError: string | null;
  promptsStatus: McpListStatus;
  prompts: MCPPrompt[];
  promptsError: string | null;
  capabilities: MCPCapabilitySnapshot | null;
  error: string | null;
  lifecycleGeneration: number;
}

// ---------------------------------------------------------------------------
// Store state
// ---------------------------------------------------------------------------

interface McpStoreState {
  /** 所有已配置的 MCP server（按配置顺序）。 */
  servers: MCPServerConfig[];
  /** 当前已连接的 server 名称集合。 */
  connected: Record<string, boolean>;
  /** agent 可用工具缓存（mcp.<server>.<tool>）。 */
  agentTools: AgentMCPTool[];
  /** 按 server 分组的 resources/prompts/capabilities 发现状态（P1-03-E）。 */
  serverContexts: Record<string, McpServerContextState>;
  /** 上下文加载时的工作区根；变化即全部 stale。 */
  contextsWorkspaceRoot: string | null;
  loading: boolean;
  error: string | null;
}

export const mcpState = reactive<McpStoreState>({
  servers: [],
  connected: {},
  agentTools: [],
  serverContexts: {},
  contextsWorkspaceRoot: null,
  loading: false,
  error: null,
});

export const mcpServers = computed(() => mcpState.servers);
export const connectedMcpServers = computed(() => mcpState.connected);
export const agentMcpTools = computed(() => mcpState.agentTools);
export const isLoadingMcp = computed(() => mcpState.loading);
export const mcpError = computed(() => mcpState.error);

// ---------------------------------------------------------------------------
// 编辑对话框状态（McpSection.vue 使用）
// ---------------------------------------------------------------------------

/** 当前正在编辑的 server 配置；为 null 时对话框隐藏。 */
export const editingServer = ref<MCPServerConfig | null>(null);

export function openServerEditor(cfg?: MCPServerConfig): void {
  editingServer.value = cfg
    ? { ...cfg, args: cfg.args ? [...cfg.args] : undefined }
    : {
        name: "",
        transport: "stdio",
        command: "",
        args: [],
        env: {},
        enabled: false,
      };
}

export function closeServerEditor(): void {
  editingServer.value = null;
}

// ---------------------------------------------------------------------------
// Backend 适配层（lazy 加载 Wails bindings；测试可注入 mock）
// ---------------------------------------------------------------------------

export interface McpBackend {
  listServers(): Promise<MCPServerConfig[]>;
  getServer(name: string): Promise<MCPServerConfig>;
  saveServer(cfg: MCPServerConfig): Promise<void>;
  setServerEnabled(name: string, enabled: boolean): Promise<void>;
  deleteServer(name: string): Promise<void>;
  connectServer(name: string): Promise<void>;
  disconnectServer(name: string): Promise<void>;
  listTools(name: string): Promise<MCPTool[]>;
  listAgentMCPTools(): Promise<AgentMCPTool[]>;
  /** P1-03-E: 资源/prompt 发现、读取与能力快照（全部经后端安全边界）。 */
  serverCapabilities(name: string): Promise<MCPCapabilitySnapshot>;
  listResources(name: string): Promise<MCPResource[]>;
  readResource(name: string, uri: string): Promise<MCPResourceRead>;
  listPrompts(name: string): Promise<MCPPrompt[]>;
  getPrompt(name: string, prompt: string, args: Record<string, string>): Promise<MCPPromptRender>;
}

let backend: McpBackend | null = null;

/** 注入 backend 适配器。测试注入 mock；应用启动时注入默认 Wails 适配器。 */
export function setMcpBackend(b: McpBackend | null): void {
  backend = b;
}

/**
 * Wails 生成 bindings 路径。bindings 由 Wails Vite 插件在 dev/build 时
 * 重新生成。
 */

interface McpBindingsShape {
  ListServers(): Promise<MCPServerConfig[]>;
  GetServer(name: string): Promise<MCPServerConfig>;
  SaveServer(cfg: MCPServerConfig): Promise<void>;
  SetServerEnabled(name: string, enabled: boolean): Promise<void>;
  DeleteServer(name: string): Promise<void>;
  ConnectServer(name: string): Promise<void>;
  DisconnectServer(name: string): Promise<void>;
  ListTools(name: string): Promise<MCPTool[]>;
  ListAgentMCPTools(): Promise<AgentMCPTool[]>;
  ServerCapabilities(name: string): Promise<MCPCapabilitySnapshot>;
  ListResources(name: string): Promise<MCPResource[] | null>;
  ReadResource(name: string, uri: string): Promise<MCPResourceRead | null>;
  ListPrompts(name: string): Promise<MCPPrompt[] | null>;
  GetPrompt(name: string, prompt: string, args: Record<string, string>): Promise<MCPPromptRender | null>;
}

let bindingsCache: McpBindingsShape | null = null;

async function loadBindings(): Promise<McpBindingsShape> {
  if (bindingsCache) return bindingsCache;
  // 使用字面量路径（无 @vite-ignore），让 vite 将 bindings 打包为 chunk。
  const mod = await import("../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/mcpservice.js");
  // bindings 文件使用命名导出，直接将 mod 作为 McpBindingsShape 使用。
  bindingsCache = mod as unknown as McpBindingsShape;
  return bindingsCache;
}

function getDefaultBackend(): McpBackend {
  return {
    async listServers() {
      const b = await loadBindings();
      return (await b.ListServers()) ?? [];
    },
    async getServer(name) {
      const b = await loadBindings();
      return b.GetServer(name);
    },
    async saveServer(cfg) {
      const b = await loadBindings();
      await b.SaveServer(cfg);
    },
    async setServerEnabled(name, enabled) {
      const b = await loadBindings();
      await b.SetServerEnabled(name, enabled);
    },
    async deleteServer(name) {
      const b = await loadBindings();
      await b.DeleteServer(name);
    },
    async connectServer(name) {
      const b = await loadBindings();
      await b.ConnectServer(name);
    },
    async disconnectServer(name) {
      const b = await loadBindings();
      await b.DisconnectServer(name);
    },
    async listTools(name) {
      const b = await loadBindings();
      return (await b.ListTools(name)) ?? [];
    },
    async listAgentMCPTools() {
      const b = await loadBindings();
      return (await b.ListAgentMCPTools()) ?? [];
    },
    async serverCapabilities(name) {
      const b = await loadBindings();
      return b.ServerCapabilities(name);
    },
    async listResources(name) {
      const b = await loadBindings();
      return (await b.ListResources(name)) ?? [];
    },
    async readResource(name, uri) {
      const b = await loadBindings();
      const read = await b.ReadResource(name, uri);
      if (!read) throw new Error(`backend returned no content for resource ${uri}`);
      return read;
    },
    async listPrompts(name) {
      const b = await loadBindings();
      return (await b.ListPrompts(name)) ?? [];
    },
    async getPrompt(name, prompt, args) {
      const b = await loadBindings();
      const render = await b.GetPrompt(name, prompt, args);
      if (!render) throw new Error(`backend returned no render for prompt ${prompt}`);
      return render;
    },
  };
}

function getBackend(): McpBackend {
  if (backend) return backend;
  backend = getDefaultBackend();
  return backend;
}

async function reconcileMcpStateAfterMutation(): Promise<void> {
  // A backend mutation may commit its config before transport teardown fails.
  // Always re-read both views so a partial-success error cannot leave the
  // renderer showing stale enabled/connected/catalog state.
  await loadMcpServers();
  await refreshAgentMcpTools();
}

// ---------------------------------------------------------------------------
// Store actions
// ---------------------------------------------------------------------------

/** 从后端加载全部 MCP server 配置并同步本地 store。可重复调用。 */
export async function loadMcpServers(): Promise<void> {
  mcpState.loading = true;
  mcpState.error = null;
  try {
    const servers = await getBackend().listServers();
    mcpState.servers = servers;
    // A committed disable/delete is authoritative even when its transport
    // teardown returned an error. Drop only entries that the config snapshot
    // proves cannot still be connected; an enabled entry remains unchanged
    // until a dedicated connection snapshot is available.
    for (const name of Object.keys(mcpState.connected)) {
      const server = servers.find((candidate) => candidate.name === name);
      if (!server || !server.enabled) {
        delete mcpState.connected[name];
        // P1-03-E: the disconnected server's discovery state and injected
        // context must not survive the authoritative disconnect.
        markServerContextStale(name);
      }
    }
    for (const name of Object.keys(mcpState.serverContexts)) {
      if (!servers.some((candidate) => candidate.name === name)) {
        delete mcpState.serverContexts[name];
      }
    }
  } catch (e: unknown) {
    mcpState.error = errorMessage(e);
  } finally {
    mcpState.loading = false;
  }
}

/**
 * 保存（新增或更新）一个 server 配置。
 * G-SEC-12：新增 server 默认 enabled=false；更新时保留既有 enabled 状态。
 */
export async function saveMcpServer(cfg: MCPServerConfig): Promise<boolean> {
  mcpState.error = null;
  let operationError: unknown = null;
  try {
    await getBackend().saveServer(cfg);
  } catch (e: unknown) {
    operationError = e;
  } finally {
    await reconcileMcpStateAfterMutation();
  }
  if (operationError !== null) {
    mcpState.error = errorMessage(operationError);
    return false;
  }
  return true;
}

/** 删除一个 server 配置；若已连接会先断开。 */
export async function deleteMcpServer(name: string): Promise<boolean> {
  mcpState.error = null;
  let operationError: unknown = null;
  try {
    await getBackend().deleteServer(name);
    delete mcpState.connected[name];
    delete mcpState.serverContexts[name];
  } catch (e: unknown) {
    operationError = e;
  } finally {
    await reconcileMcpStateAfterMutation();
  }
  if (operationError !== null) {
    mcpState.error = errorMessage(operationError);
    return false;
  }
  return true;
}

/**
 * 连接到一个已配置的 MCP server。G-SEC-12：后端要求该 server enabled=true，
 * 即用户已显式启用（等同 Restricted 扩展的显式审批）。
 */
export async function connectMcpServer(name: string): Promise<boolean> {
  mcpState.error = null;
  let operationError: unknown = null;
  try {
    await getBackend().connectServer(name);
    mcpState.connected[name] = true;
    // A successful connect is a fresh backend run with new generations; any
    // previous discovery state belongs to the old run and must be reloaded.
    delete mcpState.serverContexts[name];
  } catch (e: unknown) {
    // ConnectServer either installs a client and returns nil, or tears down
    // the provisional transport before returning an error. A prior local
    // flag is therefore stale once this attempt fails; do not claim a live
    // connection without a backend connection snapshot.
    delete mcpState.connected[name];
    operationError = e;
  } finally {
    await reconcileMcpStateAfterMutation();
  }
  if (operationError !== null) {
    mcpState.error = errorMessage(operationError);
    return false;
  }
  return true;
}

/** 断开一个已连接的 server。 */
export async function disconnectMcpServer(name: string): Promise<boolean> {
  mcpState.error = null;
  let operationError: unknown = null;
  try {
    await getBackend().disconnectServer(name);
    delete mcpState.connected[name];
  } catch (e: unknown) {
    // DisconnectServer detaches the backend client before reporting a
    // transport teardown error. Keep the renderer from claiming a live
    // connection when the process is already no longer owned by the service.
    delete mcpState.connected[name];
    operationError = e;
  } finally {
    // P1-03-E: the server can no longer serve what it served before; its
    // discovery state goes stale and its injected context leaves the
    // sendable queue in every outcome.
    markServerContextStale(name);
    await reconcileMcpStateAfterMutation();
  }
  if (operationError !== null) {
    mcpState.error = errorMessage(operationError);
    return false;
  }
  return true;
}

/**
 * 切换 server 的启用状态。
 * G-SEC-12：从 false → true 是显式审批动作。后端 SaveServer 在更新既有
 * server 时保留 enabled，因此这里显式传入新值。
 */
export async function toggleMcpServerEnabled(name: string, enabled: boolean): Promise<boolean> {
  mcpState.error = null;
  let operationError: unknown = null;
  try {
    await getBackend().setServerEnabled(name, enabled);
    if (!enabled) {
      delete mcpState.connected[name];
      markServerContextStale(name);
    }
  } catch (e: unknown) {
    operationError = e;
  } finally {
    await reconcileMcpStateAfterMutation();
  }
  if (operationError !== null) {
    mcpState.error = errorMessage(operationError);
    return false;
  }
  return true;
}

/** 刷新 agent 可用的 MCP 工具列表（`mcp.<server>.<tool>` 命名空间）。 */
export async function refreshAgentMcpTools(): Promise<void> {
  mcpState.error = null;
  try {
    mcpState.agentTools = await getBackend().listAgentMCPTools();
  } catch (e: unknown) {
    // 工具列表刷新失败不阻塞 UI，仅记录错误。
    mcpState.error = errorMessage(e);
  }
}

// ---------------------------------------------------------------------------
// P1-03-E: 资源/prompt 发现、读取与上下文注入
// ---------------------------------------------------------------------------

/** 注入上下文的单条长度预算；超出部分截断并带显式标记（不伪造完整成功）。 */
export const mcpContextInjectionBudget = 64 * 1024;

function ensureServerContext(name: string): McpServerContextState {
  let ctx = mcpState.serverContexts[name];
  if (!ctx) {
    ctx = {
      status: "unloaded",
      resourcesStatus: "unloaded",
      resources: [],
      resourcesError: null,
      promptsStatus: "unloaded",
      prompts: [],
      promptsError: null,
      capabilities: null,
      error: null,
      lifecycleGeneration: 0,
    };
    mcpState.serverContexts[name] = ctx;
  }
  return ctx;
}

/**
 * P1-03-E: MCP 上下文 chip 清扫。server 为 null 时清扫全部 MCP 注入 chip。
 * 默认实现惰性加载 ai store，避免 store 图的静态环；测试可注入记录器。
 */
export type McpContextSweeper = (server: string | null) => void;

let contextSweeper: McpContextSweeper | null = null;

export function setMcpContextSweeper(sweeper: McpContextSweeper | null): void {
  contextSweeper = sweeper;
}

function sweepMcpContextChips(server: string | null): void {
  if (contextSweeper) {
    contextSweeper(server);
    return;
  }
  void import("@/stores/ai")
    .then(({ aiState, removeContextChip }) => {
      for (const chip of [...aiState.contextChips]) {
        if (chip.kind !== "mcp") continue;
        if (server !== null && chip.mcpServer !== server) continue;
        removeContextChip(chip.id);
      }
    })
    .catch((error: unknown) => {
      // A teardown racing the lazy import must not surface as an unhandled
      // rejection; the next sweep retries with the module cache warm.
      console.warn("[mcp] context chip sweep failed", error);
    });
}

async function upsertMcpChip(chip: ContextChip): Promise<void> {
  const ai = await import("@/stores/ai");
  ai.upsertContextChip(chip);
}

function truncateForInjection(text: string): string {
  if (text.length <= mcpContextInjectionBudget) return text;
  return `${text.slice(0, mcpContextInjectionBudget)}\n…[MCP context truncated at the ${mcpContextInjectionBudget}-character injection budget]`;
}

/** 将一个 server 的发现状态标记为 stale 并移除其可发送上下文。 */
function markServerContextStale(name: string): void {
  const ctx = mcpState.serverContexts[name];
  if (ctx) {
    ctx.status = "stale";
    ctx.resourcesStatus = "stale";
    ctx.promptsStatus = "stale";
    ctx.resources = [];
    ctx.prompts = [];
  }
  sweepMcpContextChips(name);
}

/**
 * 工作区切换后由 workspaceStore（惰性）调用：全部 server 上下文过期，
 * 所有 MCP 注入上下文从可发送队列移除。同根重复调用为 no-op。
 */
export function markMcpWorkspaceChanged(workspaceRoot: string): void {
  if (mcpState.contextsWorkspaceRoot === workspaceRoot) return;
  mcpState.contextsWorkspaceRoot = workspaceRoot;
  for (const name of Object.keys(mcpState.serverContexts)) {
    markServerContextStale(name);
  }
  sweepMcpContextChips(null);
}

/**
 * P1-01 stale 守卫：每个 server 的刷新序号。快速重复刷新（或工作区切换
 * 后的重连刷新）时，只有序号与当前一致的那次刷新允许回写上下文状态，
 * 迟到的旧响应一律丢弃。
 */
const contextRefreshSeqs = new Map<string, number>();

/**
 * 刷新一个 server 的发现状态：先取 capability 快照（后端校验 workspace/
 * lifecycle generation），resources/prompts 仅在后端声明 supported 时列出。
 * 每个家族独立区分 loaded/empty/unsupported/error，失败保留可诊断错误。
 */
export async function refreshMcpServerContext(name: string): Promise<void> {
  const seq = (contextRefreshSeqs.get(name) ?? 0) + 1;
  contextRefreshSeqs.set(name, seq);
  const ctx = ensureServerContext(name);
  ctx.status = "loading";
  ctx.error = null;
  try {
    const capabilities = await getBackend().serverCapabilities(name);
    if (contextRefreshSeqs.get(name) !== seq) return;
    ctx.capabilities = capabilities;
    ctx.lifecycleGeneration = capabilities.lifecycleGeneration;
    await refreshContextFamily(name, ctx, "resources", seq);
    await refreshContextFamily(name, ctx, "prompts", seq);
    if (contextRefreshSeqs.get(name) !== seq) return;
    ctx.status = "loaded";
  } catch (e: unknown) {
    if (contextRefreshSeqs.get(name) !== seq) return;
    ctx.status = "error";
    ctx.error = errorMessage(e);
  }
}

async function refreshContextFamily(
  name: string,
  ctx: McpServerContextState,
  family: "resources" | "prompts",
  seq: number,
): Promise<void> {
  const capability = ctx.capabilities?.capabilities[family];
  if (!capability || capability.state !== "supported") {
    if (family === "resources") {
      ctx.resources = [];
      ctx.resourcesStatus = "unsupported";
      ctx.resourcesError = capability
        ? `server did not declare the ${family} capability`
        : "capability snapshot unavailable";
    } else {
      ctx.prompts = [];
      ctx.promptsStatus = "unsupported";
      ctx.promptsError = capability
        ? `server did not declare the ${family} capability`
        : "capability snapshot unavailable";
    }
    return;
  }
  if (family === "resources") {
    ctx.resourcesStatus = "loading";
    ctx.resourcesError = null;
    try {
      const resources = await getBackend().listResources(name);
      if (contextRefreshSeqs.get(name) !== seq) return;
      ctx.resources = resources;
      ctx.resourcesStatus = ctx.resources.length === 0 ? "empty" : "loaded";
    } catch (e: unknown) {
      if (contextRefreshSeqs.get(name) !== seq) return;
      ctx.resourcesStatus = "error";
      ctx.resourcesError = errorMessage(e);
    }
  } else {
    ctx.promptsStatus = "loading";
    ctx.promptsError = null;
    try {
      const prompts = await getBackend().listPrompts(name);
      if (contextRefreshSeqs.get(name) !== seq) return;
      ctx.prompts = prompts;
      ctx.promptsStatus = ctx.prompts.length === 0 ? "empty" : "loaded";
    } catch (e: unknown) {
      if (contextRefreshSeqs.get(name) !== seq) return;
      ctx.promptsStatus = "error";
      ctx.promptsError = errorMessage(e);
    }
  }
}

/** 读取资源内容（预览用，不注入）。失败保留错误并返回 null。 */
export async function readMcpResource(name: string, uri: string): Promise<MCPResourceRead | null> {
  mcpState.error = null;
  try {
    return await getBackend().readResource(name, uri);
  } catch (e: unknown) {
    mcpState.error = errorMessage(e);
    return null;
  }
}

/** 渲染 prompt（预览用，不注入）。失败保留错误并返回 null。 */
export async function getMcpPrompt(
  name: string,
  prompt: string,
  args: Record<string, string>,
): Promise<MCPPromptRender | null> {
  mcpState.error = null;
  try {
    return await getBackend().getPrompt(name, prompt, args);
  } catch (e: unknown) {
    mcpState.error = errorMessage(e);
    return null;
  }
}

/**
 * 显式用户动作：读取资源并注入为带来源标签的上下文 chip。同一来源重复
 * 注入会替换内容（按确定性 id 去重），不产生重复 chip。未连接或 stale
 * 状态拒绝注入。
 */
export async function injectMcpResourceContext(name: string, uri: string): Promise<boolean> {
  mcpState.error = null;
  if (!mcpState.connected[name]) {
    mcpState.error = `MCP server "${name}" is not connected; context injection refused`;
    return false;
  }
  const ctx = mcpState.serverContexts[name];
  if (ctx && ctx.status === "stale") {
    mcpState.error = `MCP server "${name}" context is stale; refresh before injecting`;
    return false;
  }
  let read: MCPResourceRead;
  try {
    read = await getBackend().readResource(name, uri);
  } catch (e: unknown) {
    mcpState.error = errorMessage(e);
    return false;
  }
  await upsertMcpChip({
    id: `mcp-res:${name}:${uri}`,
    kind: "mcp",
    label: uri,
    content: truncateForInjection(read.contents.map((c) => c.text).join("\n\n")),
    mcpServer: name,
    mcpUri: uri,
    mcpGeneration: read.lifecycleGeneration,
  });
  return true;
}

/**
 * 显式用户动作：渲染 prompt 并注入为带来源标签的上下文 chip，消息保留
 * role 标记。去重/budget/连接检查与资源注入一致。
 */
export async function injectMcpPromptContext(
  name: string,
  prompt: string,
  args: Record<string, string> = {},
): Promise<boolean> {
  mcpState.error = null;
  if (!mcpState.connected[name]) {
    mcpState.error = `MCP server "${name}" is not connected; context injection refused`;
    return false;
  }
  const ctx = mcpState.serverContexts[name];
  if (ctx && ctx.status === "stale") {
    mcpState.error = `MCP server "${name}" context is stale; refresh before injecting`;
    return false;
  }
  let render: MCPPromptRender;
  try {
    render = await getBackend().getPrompt(name, prompt, args);
  } catch (e: unknown) {
    mcpState.error = errorMessage(e);
    return false;
  }
  await upsertMcpChip({
    id: `mcp-prompt:${name}:${prompt}`,
    kind: "mcp",
    label: prompt,
    content: truncateForInjection(
      render.messages.map((m) => `[${m.role}]\n${m.content.text ?? ""}`).join("\n\n"),
    ),
    mcpServer: name,
    mcpPrompt: prompt,
    mcpGeneration: render.lifecycleGeneration,
  });
  return true;
}

/** 清理一个 server 的发现状态（unloaded）。 */
export function clearMcpServerContext(name: string): void {
  delete mcpState.serverContexts[name];
}

/** 清理所有已过期（stale）的发现状态。 */
export function clearStaleMcpContexts(): void {
  for (const name of Object.keys(mcpState.serverContexts)) {
    if (mcpState.serverContexts[name].status === "stale") {
      delete mcpState.serverContexts[name];
    }
  }
}

/** Legacy renderer MCP execution is deny-only. Agent calls are parsed from the
 * backend ToolDef catalog and executed by AgentService's unified capability
 * facade; keeping a second token pipeline here would reintroduce the bypass. */
export async function callMcpTool(
  _server: string,
  _tool: string,
  _args: Record<string, unknown>,
): Promise<MCPToolResult | null> {
	mcpState.error = "MCP Agent execution requires the unified Agent tool catalog";
	return null;
}

/** 重置 store 状态。测试专用。 */
export function resetMcpStore(): void {
  mcpState.servers = [];
  mcpState.connected = {};
  mcpState.agentTools = [];
  mcpState.serverContexts = {};
  mcpState.contextsWorkspaceRoot = null;
  mcpState.loading = false;
  mcpState.error = null;
  editingServer.value = null;
  backend = null;
  bindingsCache = null;
  contextSweeper = null;
  contextRefreshSeqs.clear();
}
