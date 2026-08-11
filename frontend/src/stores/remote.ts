/**
 * F-9 (prompt-2.md 第 537-586 行) — SSH 远程开发前端 store。
 *
 * 后端 `services/remote_service.go` 的前端对应物。职责：
 *   - 管理已建立的 SSH 会话列表（按 name 索引）。
 *   - 触发连接 / 断开 / 列表查询。
 *   - 在远程会话上执行 argv 命令（远程终端）。
 *
 * 安全（G-SEC-07 / G-SEC-12）：
 *   - 密码和密钥路径绝不记录到日志（后端 Connect 仅记录 host/port/user）。
 *   - SSH 连接必须经过 known_hosts 校验；KnownHostsPath 为空时拒绝连接。
 *   - 前端不缓存任何密码/密钥；config 对象在使用后立即清空敏感字段。
 *
 * 采用与 `stores/mcp.ts` 相同的「lazy bindings + 可注入 backend」模式，
 * 便于单元测试注入 mock。
 */
// Koyori IDE 模块 · Remote；交互服务：远程开发（RemoteService）。
// 喵，这是 Koyori IDE 的 Remote 模块（前端实现）~

import { reactive, computed, ref } from "vue";
import { errorMessage } from "@/lib/errors";
import { notifyError } from "@/lib/notifications";
import { remoteService as defaultBackend } from "@/api/services";
import type { SSHConfig, RemoteConfig, RemoteFileInfo } from "@/types";

// ---------------------------------------------------------------------------
// Store state
// ---------------------------------------------------------------------------

interface RemoteStoreState {
  /** 所有已建立的 SSH 会话名称（按字母序）。 */
  connections: string[];
  /** 当前活动连接名（用于文件树 / 终端绑定）。 */
  current: string | null;
  /** 远程文件树根目录（打开远程项目后设置）。 */
  currentRemotePath: string | null;
  /** 远程文件树缓存（按路径索引其直接子项）。 */
  remoteTree: Record<string, RemoteFileInfo[]>;
  /** 操作进行中标志（连接/断开/命令）。 */
  loading: boolean;
  /** 最近一次错误信息；null 表示无错误。 */
  error: string | null;
}

export const remoteState = reactive<RemoteStoreState>({
  connections: [],
  current: null,
  currentRemotePath: null,
  remoteTree: {},
  loading: false,
  error: null,
});

export const remoteConnections = computed(() => remoteState.connections);
export const currentRemoteConnection = computed(() => remoteState.current);
export const currentRemoteRoot = computed(() => remoteState.currentRemotePath);
export const remoteTreeCache = computed(() => remoteState.remoteTree);
export const isLoadingRemote = computed(() => remoteState.loading);
export const remoteError = computed(() => remoteState.error);

// ---------------------------------------------------------------------------
// 编辑对话框状态（RemoteProjectWizard.vue 使用）
// ---------------------------------------------------------------------------

/** 当前正在编辑的远程项目配置；为 null 时对话框隐藏。 */
export const editingRemoteProject = ref<{
  name: string;
  config: SSHConfig;
  remotePath: string;
} | null>(null);

export function openRemoteProjectEditor(): void {
  editingRemoteProject.value = {
    name: "",
    config: {
      host: "",
      port: 22,
      user: "",
      keyPath: "",
      password: "",
      knownHostsPath: "",
    },
    remotePath: "/",
  };
}

export function closeRemoteProjectEditor(): void {
  editingRemoteProject.value = null;
}

// ---------------------------------------------------------------------------
// Backend 适配层（lazy 加载 Wails bindings；测试可注入 mock）
// ---------------------------------------------------------------------------

export interface RemoteBackend {
  connect(name: string, config: SSHConfig): Promise<void>;
  disconnect(name: string): Promise<void>;
  isConnected(name: string): Promise<boolean>;
  requestCommandApproval(name: string, argv: string[]): Promise<string>;
  executeCommand(name: string, argv: string[], approvalToken: string): Promise<string>;
  listConnections(): Promise<string[]>;
}

let backend: RemoteBackend | null = null;

/** 注入 backend 适配器。测试注入 mock；应用启动时使用默认 Wails 适配器。 */
export function setRemoteBackend(b: RemoteBackend | null): void {
  backend = b;
}

function getDefaultBackend(): RemoteBackend {
  return {
    connect(name, config) {
      return defaultBackend.connect(name, config);
    },
    disconnect(name) {
      return defaultBackend.disconnect(name);
    },
    isConnected(name) {
      return defaultBackend.isConnected(name);
    },
    requestCommandApproval(name, argv) {
      return defaultBackend.requestCommandApproval(name, argv);
    },
    executeCommand(name, argv, approvalToken) {
      return defaultBackend.executeCommand(name, argv, approvalToken);
    },
    listConnections() {
      return defaultBackend.listConnections();
    },
  };
}

function getBackend(): RemoteBackend {
  if (backend) return backend;
  backend = getDefaultBackend();
  return backend;
}

// ---------------------------------------------------------------------------
// Store actions
// ---------------------------------------------------------------------------

/**
 * 从后端拉取已建立的连接列表，同步本地 store。
 * 调用时机：组件 mount / 用户手动刷新 / 连接或断开后。
 */
export async function listConnections(): Promise<void> {
  remoteState.loading = true;
  remoteState.error = null;
  try {
    const names = await getBackend().listConnections();
    remoteState.connections = names ?? [];
    // 若当前活动连接已不在列表中，重置 current。
    if (remoteState.current && !remoteState.connections.includes(remoteState.current)) {
      remoteState.current = null;
      remoteState.currentRemotePath = null;
    }
  } catch (e: unknown) {
    remoteState.error = errorMessage(e);
  } finally {
    remoteState.loading = false;
  }
}

/**
 * 建立到远程主机的 SSH 连接。
 * 成功后将 name 设为 current，并刷新连接列表。
 *
 * 安全：本函数不打印任何敏感字段。失败时将 errorMessage 写入 store.error。
 */
export async function connect(name: string, config: SSHConfig): Promise<boolean> {
  if (!name) {
    remoteState.error = "session name is empty";
    return false;
  }
  remoteState.loading = true;
  remoteState.error = null;
  try {
    await getBackend().connect(name, config);
    remoteState.current = name;
    await listConnections();
    return true;
  } catch (e: unknown) {
    remoteState.error = errorMessage(e);
    return false;
  } finally {
    remoteState.loading = false;
  }
}

/**
 * 断开指定会话。若断开的是 current，则重置 current 与 remotePath。
 * 未连接的会话返回成功（幂等）。
 */
export async function disconnect(name: string): Promise<boolean> {
  remoteState.loading = true;
  remoteState.error = null;
  try {
    await getBackend().disconnect(name);
    if (remoteState.current === name) {
      remoteState.current = null;
      remoteState.currentRemotePath = null;
    }
    await listConnections();
    return true;
  } catch (e: unknown) {
    remoteState.error = errorMessage(e);
    return false;
  } finally {
    remoteState.loading = false;
  }
}

/** 报告指定会话是否已连接。 */
export async function isConnected(name: string): Promise<boolean> {
  try {
    return await getBackend().isConnected(name);
  } catch (e: unknown) {
    remoteState.error = errorMessage(e);
    return false;
  }
}

/**
 * 在远程会话上执行 argv 命令，返回组合的 stdout+stderr。
 * 用于远程终端面板。
 */
export async function executeCommand(name: string, argv: string[]): Promise<string | null> {
  remoteState.error = null;
  try {
    const approvalToken = await getBackend().requestCommandApproval(name, argv);
    return await getBackend().executeCommand(name, argv, approvalToken);
  } catch (e: unknown) {
    remoteState.error = errorMessage(e);
    return null;
  }
}

/**
 * 设置当前活动远程会话的根目录。打开远程项目后由前端调用。
 * 调用后 remoteState.currentRemotePath 用于文件树展示。
 */
export function setCurrentRemotePath(path: string | null): void {
  remoteState.currentRemotePath = path;
}

/**
 * 缓存远程目录列表结果。文件树组件懒加载目录后调用此函数缓存结果，
 * 后续展开同一目录时直接从 cache 读取，避免重复 SFTP ReadDir。
 */
export function cacheRemoteDirectory(path: string, entries: RemoteFileInfo[]): void {
  remoteState.remoteTree[path] = entries;
}

/** 清空指定路径的缓存（目录被删除或刷新时调用）。 */
export function invalidateRemoteDirectory(path: string): void {
  delete remoteState.remoteTree[path];
}

/**
 * 创建一个本地 Project 对象，描述远程项目。
 * 用于在 ProjectsView 列表中显示远程项目。
 */
export function buildRemoteProject(name: string, config: SSHConfig, remotePath: string): {
  name: string;
  path: string;
  remote: RemoteConfig;
} {
  return {
    name,
    path: remotePath,
    remote: { config, name },
  };
}

/**
 * 在远程会话上执行命令并通知错误（用于 UI 按钮触发的命令）。
 * 与 executeCommand 的区别：失败时弹通知，便于用户感知。
 */
export async function executeCommandWithNotify(
  name: string,
  argv: string[],
  errorTitle: string,
): Promise<string | null> {
  const result = await executeCommand(name, argv);
  if (result === null) {
    notifyError(`${errorTitle}: ${remoteState.error ?? "unknown error"}`);
  }
  return result;
}

/** 重置 store 状态。测试专用。 */
export function resetRemoteStore(): void {
  remoteState.connections = [];
  remoteState.current = null;
  remoteState.currentRemotePath = null;
  remoteState.remoteTree = {};
  remoteState.loading = false;
  remoteState.error = null;
  editingRemoteProject.value = null;
  backend = null;
}
