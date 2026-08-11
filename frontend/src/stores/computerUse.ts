/**
 * Plan 11 Task 6 Step 5 — Computer Use 前端 store。
 *
 * 后端 `services/computer_use_service.go` 的前端对应物。职责：
 *   - 加载/保存 ComputerUseConfig（G-SEC-12：默认 Enabled=false）。
 *   - 查询审计日志（最近 N 条不可逆操作记录）。
 *   - 录制模式控制（StartRecording / StopRecording / IsRecording）。
 *
 * 安全（G-SEC-02 / G-SEC-06 / G-SEC-12）：
 *   - 启用 Computer Use 视同 Restricted 扩展能力，需 explicitApproval；
 *     UI 必须弹窗确认后调用 UpdateConfig(Enabled=true)。
 *   - 5 个工具只通过后端签发的一次性 capability token 执行；关闭
 *     ConfirmationRequired 也不会开放直接操作入口或跳过原生授权。
 *   - 禁止快捷键黑名单 + 禁止区域由后端强制，前端仅展示。
 *
 * 通过 `api/services.ts` 使用生成 bindings，并保留可注入 backend 便于测试。
 */
// Koyori IDE 模块 · Computer Use；交互服务：Computer Use（ComputerUseService）。
// 喵，这是 Koyori IDE 的 Computer Use 模块（前端实现）~

import { reactive, computed } from "vue";
import { computerUseService } from "@/api/services";
import { errorMessage } from "@/lib/errors";

// ---------------------------------------------------------------------------
// 类型 — 镜像 Go 结构体（services/computer_use_service.go）
// ---------------------------------------------------------------------------

export interface ForbiddenZone {
  name: string;
  x: number;
  y: number;
  w: number;
  h: number;
}

export interface ComputerUseConfig {
  /** G-SEC-12：默认 false，启用需 explicitApproval（视同 Restricted）。 */
  enabled: boolean;
  /** Step 3：控制额外的逐步确认策略；后端 capability 仍始终需原生授权。 */
  confirmationRequired: boolean;
  /** Step 2：0-100，PNG/JPEG 压缩质量。 */
  screenshotQuality?: number;
  /** Step 2：0.1-1.0，截图缩放比例（降低分辨率节省 token）。 */
  screenshotScale?: number;
  /** 应用白名单（进程名）；空表示不限制。 */
  appWhitelist?: string[];
  /** Step 5/6：屏幕上禁止操作的矩形区域（密码管理器等）。 */
  forbiddenZones?: ForbiddenZone[];
  /** Step 6：禁止的快捷键组合黑名单（含 OS 级危险快捷键）。 */
  forbiddenHotkeys?: string[];
  /** Step 4：录制模式开关。 */
  recordingEnabled?: boolean;
}

export interface AuditAction {
  timestamp: string;
  action: string;
  args: string;
  success: boolean;
  error?: string;
  confirmedByUser: boolean;
}

export interface RecordedAction {
  timestamp: string;
  action: string;
  args: string;
}

export interface ComputerUseOperationResult {
  screenshot?: string;
}

// ---------------------------------------------------------------------------
// Store state
// ---------------------------------------------------------------------------

interface ComputerUseStoreState {
  config: ComputerUseConfig;
  auditLog: AuditAction[];
  recording: boolean;
  loading: boolean;
  saving: boolean;
  error: string | null;
}

export const computerUseState = reactive<ComputerUseStoreState>({
  config: {
    enabled: false,
    confirmationRequired: true,
  },
  auditLog: [],
  recording: false,
  loading: false,
  saving: false,
  error: null,
});

export const computerUseConfig = computed(() => computerUseState.config);
export const computerUseEnabled = computed(() => computerUseState.config.enabled);
export const computerUseRecording = computed(() => computerUseState.recording);
export const computerUseAuditLog = computed(() => computerUseState.auditLog);
export const isLoadingComputerUse = computed(() => computerUseState.loading);
export const computerUseError = computed(() => computerUseState.error);

// ---------------------------------------------------------------------------
// Backend 适配层（lazy 加载 Wails bindings；测试可注入 mock）
// ---------------------------------------------------------------------------

export interface ComputerUseBackend {
  getConfig(): Promise<ComputerUseConfig>;
  updateConfig(cfg: ComputerUseConfig): Promise<void>;
  isEnabled(): Promise<boolean>;
  getAuditLog(limit: number): Promise<AuditAction[]>;
  requestOperationApproval(action: string, details: string): Promise<string>;
  executeApprovedOperation(token: string): Promise<ComputerUseOperationResult>;
  startRecording(): Promise<void>;
  stopRecording(): Promise<RecordedAction[]>;
  isRecording(): Promise<boolean>;
}

let backend: ComputerUseBackend | null = null;

/** 注入 backend 适配器。测试注入 mock；应用启动时注入默认 Wails 适配器。 */
export function setComputerUseBackend(b: ComputerUseBackend | null): void {
  backend = b;
}

// normalizeConfig 确保 ComputerUseConfig 的切片字段不为 null。
type BindingComputerUseConfig = Awaited<ReturnType<typeof computerUseService.getConfig>>;

function normalizeConfig(c: BindingComputerUseConfig): ComputerUseConfig {
  return {
    ...c,
    appWhitelist: c.appWhitelist ?? [],
    forbiddenZones: c.forbiddenZones ?? [],
    forbiddenHotkeys: c.forbiddenHotkeys ?? [],
  };
}

function getDefaultBackend(): ComputerUseBackend {
  return {
    async getConfig() {
      return normalizeConfig(await computerUseService.getConfig());
    },
    async updateConfig(cfg) {
      await computerUseService.updateConfig(cfg);
    },
    async isEnabled() {
      return computerUseService.isEnabled();
    },
    async getAuditLog(limit) {
      return computerUseService.getAuditLog(limit);
    },
    async requestOperationApproval(action, details) {
      return computerUseService.requestOperationApproval(action, details);
    },
    async executeApprovedOperation(token) {
      return computerUseService.executeApprovedOperation(token);
    },
    async startRecording() {
      await computerUseService.startRecording();
    },
    async stopRecording() {
      return computerUseService.stopRecording();
    },
    async isRecording() {
      return computerUseService.isRecording();
    },
  };
}

function getBackend(): ComputerUseBackend {
  if (backend) return backend;
  backend = getDefaultBackend();
  return backend;
}

// ---------------------------------------------------------------------------
// Store actions
// ---------------------------------------------------------------------------

/** 从后端加载配置 + 录制状态。 */
export async function loadComputerUseConfig(): Promise<void> {
  computerUseState.loading = true;
  computerUseState.error = null;
  try {
    computerUseState.config = await getBackend().getConfig();
    computerUseState.recording = await getBackend().isRecording();
  } catch (e: unknown) {
    computerUseState.error = errorMessage(e);
  } finally {
    computerUseState.loading = false;
  }
}

/**
 * 保存配置到后端（持久化 0600 + atomicWriteFile）。
 * G-SEC-12：从 enabled=false → true 是显式审批动作，调用方需确保用户已确认。
 */
export async function saveComputerUseConfig(cfg: ComputerUseConfig): Promise<boolean> {
  computerUseState.saving = true;
  computerUseState.error = null;
  try {
    await getBackend().updateConfig(cfg);
    computerUseState.config = await getBackend().getConfig();
    return true;
  } catch (e: unknown) {
    computerUseState.error = errorMessage(e);
    return false;
  } finally {
    computerUseState.saving = false;
  }
}

/** 拉取审计日志（最近 N 条）。 */
export async function refreshAuditLog(limit = 100): Promise<void> {
  computerUseState.error = null;
  try {
    computerUseState.auditLog = await getBackend().getAuditLog(limit);
  } catch (e: unknown) {
    computerUseState.error = errorMessage(e);
  }
}

/** 请求后端签发一次性 Computer Use capability token。 */
export async function requestComputerUseApproval(
  action: string,
  details: string,
): Promise<string | null> {
  computerUseState.error = null;
  try {
    return await getBackend().requestOperationApproval(action, details);
  } catch (e: unknown) {
    computerUseState.error = errorMessage(e);
    return null;
  }
}

/** 消费后端签发的 Computer Use capability token。 */
export async function executeApprovedComputerUseOperation(
  token: string,
): Promise<ComputerUseOperationResult | null> {
  computerUseState.error = null;
  try {
    return await getBackend().executeApprovedOperation(token);
  } catch (e: unknown) {
    computerUseState.error = errorMessage(e);
    return null;
  }
}

/** 统一走 request -> execute，避免 renderer 直接调用兼容性入口。 */
export async function executeComputerUseOperation(
  action: string,
  details: string,
): Promise<ComputerUseOperationResult | null> {
  const token = await requestComputerUseApproval(action, details);
  if (!token) return null;
  return executeApprovedComputerUseOperation(token);
}

/** 开始录制模式（Step 4）。需 Computer Use 已启用。 */
export async function startRecording(): Promise<boolean> {
  computerUseState.error = null;
  try {
    await getBackend().startRecording();
    computerUseState.recording = true;
    return true;
  } catch (e: unknown) {
    computerUseState.error = errorMessage(e);
    return false;
  }
}

/** 停止录制并返回捕获的操作序列（Step 4）。 */
export async function stopRecording(): Promise<RecordedAction[]> {
  computerUseState.error = null;
  try {
    const actions = await getBackend().stopRecording();
    computerUseState.recording = false;
    return actions;
  } catch (e: unknown) {
    computerUseState.error = errorMessage(e);
    return [];
  }
}

/** 重置 store 状态。测试专用。 */
export function resetComputerUseStore(): void {
  computerUseState.config = { enabled: false, confirmationRequired: true };
  computerUseState.auditLog = [];
  computerUseState.recording = false;
  computerUseState.loading = false;
  computerUseState.saving = false;
  computerUseState.error = null;
  backend = null;
}
