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
  /**
   * 免审批工具名白名单。G-SEC-02：即使在此名单中，后端仍记录审计日志，
   * 不会归为 RiskSafe。默认空（全部需审批）。
   */
  autoApprove?: string[];
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
  autoApproved: boolean;
}

/** MCP 工具调用结果。镜像 Go `MCPToolResult`。 */
export interface MCPToolResult {
  content: Array<{ type: string; text?: string }>;
  isError: boolean;
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
  loading: boolean;
  error: string | null;
}

export const mcpState = reactive<McpStoreState>({
  servers: [],
  connected: {},
  agentTools: [],
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
    ? { ...cfg, args: cfg.args ? [...cfg.args] : undefined, autoApprove: cfg.autoApprove ? [...cfg.autoApprove] : undefined }
    : {
        name: "",
        transport: "stdio",
        command: "",
        args: [],
        env: {},
        enabled: false,
        autoApprove: [],
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
  requestToolApproval(server: string, tool: string, args: Record<string, unknown>): Promise<string>;
  executeApprovedTool(server: string, tool: string, args: Record<string, unknown>, approvalToken: string): Promise<MCPToolResult>;
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
  RequestToolApproval(server: string, tool: string, args: Record<string, unknown>): Promise<string>;
  ExecuteApprovedTool(server: string, tool: string, args: Record<string, unknown>, approvalToken: string): Promise<MCPToolResult>;
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
    async requestToolApproval(server, tool, args) {
	  const b = await loadBindings();
	  return b.RequestToolApproval(server, tool, args);
	},
	async executeApprovedTool(server, tool, args, approvalToken) {
	  const b = await loadBindings();
	  return b.ExecuteApprovedTool(server, tool, args, approvalToken);
    },
  };
}

function getBackend(): McpBackend {
  if (backend) return backend;
  backend = getDefaultBackend();
  return backend;
}

// ---------------------------------------------------------------------------
// Store actions
// ---------------------------------------------------------------------------

/** 从后端加载全部 MCP server 配置并同步本地 store。可重复调用。 */
export async function loadMcpServers(): Promise<void> {
  mcpState.loading = true;
  mcpState.error = null;
  try {
    mcpState.servers = await getBackend().listServers();
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
  try {
    await getBackend().saveServer(cfg);
    await loadMcpServers();
    return true;
  } catch (e: unknown) {
    mcpState.error = errorMessage(e);
    return false;
  }
}

/** 删除一个 server 配置；若已连接会先断开。 */
export async function deleteMcpServer(name: string): Promise<boolean> {
  mcpState.error = null;
  try {
    await getBackend().deleteServer(name);
    delete mcpState.connected[name];
    await loadMcpServers();
    await refreshAgentMcpTools();
    return true;
  } catch (e: unknown) {
    mcpState.error = errorMessage(e);
    return false;
  }
}

/**
 * 连接到一个已配置的 MCP server。G-SEC-12：后端要求该 server enabled=true，
 * 即用户已显式启用（等同 Restricted 扩展的显式审批）。
 */
export async function connectMcpServer(name: string): Promise<boolean> {
  mcpState.error = null;
  try {
    await getBackend().connectServer(name);
    mcpState.connected[name] = true;
    await refreshAgentMcpTools();
    return true;
  } catch (e: unknown) {
    mcpState.error = errorMessage(e);
    return false;
  }
}

/** 断开一个已连接的 server。 */
export async function disconnectMcpServer(name: string): Promise<boolean> {
  mcpState.error = null;
  try {
    await getBackend().disconnectServer(name);
    delete mcpState.connected[name];
    await refreshAgentMcpTools();
    return true;
  } catch (e: unknown) {
    mcpState.error = errorMessage(e);
    return false;
  }
}

/**
 * 切换 server 的启用状态。
 * G-SEC-12：从 false → true 是显式审批动作。后端 SaveServer 在更新既有
 * server 时保留 enabled，因此这里显式传入新值。
 */
export async function toggleMcpServerEnabled(name: string, enabled: boolean): Promise<boolean> {
  mcpState.error = null;
  try {
    await getBackend().setServerEnabled(name, enabled);
    if (!enabled) delete mcpState.connected[name];
    await loadMcpServers();
    return true;
  } catch (e: unknown) {
    mcpState.error = errorMessage(e);
    return false;
  }
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

/** 调用一个 MCP 工具（agent 执行路径，需经 CheckCommand 审批后调用）。 */
export async function callMcpTool(
  server: string,
  tool: string,
  args: Record<string, unknown>,
): Promise<MCPToolResult | null> {
  mcpState.error = null;
  try {
    const approvalToken = await getBackend().requestToolApproval(server, tool, args);
    return await getBackend().executeApprovedTool(server, tool, args, approvalToken);
  } catch (e: unknown) {
    mcpState.error = errorMessage(e);
    return null;
  }
}

/** 重置 store 状态。测试专用。 */
export function resetMcpStore(): void {
  mcpState.servers = [];
  mcpState.connected = {};
  mcpState.agentTools = [];
  mcpState.loading = false;
  mcpState.error = null;
  editingServer.value = null;
  backend = null;
  bindingsCache = null;
}
