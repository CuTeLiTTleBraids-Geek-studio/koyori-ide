/**
 * Plan 11 Task 7 Step 6 — IM 集成前端 store。
 *
 * 后端 `services/im_service.go` 的前端对应物。职责：
 *   - 加载 IMConfig（4 provider：Slack/Discord/飞书/企业微信）。
 *   - 保存 provider 配置 + 通知规则 + Approve（G-SEC-12）。
 *   - 发送测试消息 + 查询审计。
 *
 * 安全（G-SEC-07 / G-SEC-12）：
 *   - 后端 LoadConfig 不回传明文 token/webhook，仅返回 configured 布尔。
 *   - 前端编辑 webhook/token 时输入明文，保存时走 UpdateConfig（后端加密）。
 *   - IM 发送视同 Restricted，首次需 Approve。
 */
// Koyori IDE 模块 · Im。
// 喵，这是 Koyori IDE 的 Im 模块（前端实现）~

import { reactive, computed } from "vue";
import { errorMessage } from "@/lib/errors";

// ---------------------------------------------------------------------------
// 类型 — 镜像 Go 结构体（services/im_service.go）
// ---------------------------------------------------------------------------

export type IMProviderType = "slack" | "discord" | "feishu" | "wechat_work";

export interface IMProvider {
  type: IMProviderType;
  name: string;
  webhookUrl?: string;
  botToken?: string;
  channelId?: string;
  enabled: boolean;
}

export interface NotificationRule {
  event: string;
  provider: string;
  channel: string;
  template: string;
  enabled: boolean;
}

export interface IMConfig {
  providers: IMProvider[];
  notificationRules?: NotificationRule[];
  approved: boolean;
}

/** 后端返回的视图（敏感字段替换为 configured 布尔）。 */
export interface IMProviderView {
  type: IMProviderType;
  name: string;
  channelId?: string;
  enabled: boolean;
  webhookConfigured: boolean;
  botTokenConfigured: boolean;
}

export interface IMConfigView {
  providers: IMProviderView[];
  notificationRules?: NotificationRule[];
  approved: boolean;
}

// ---------------------------------------------------------------------------
// Store state
// ---------------------------------------------------------------------------

interface IMStoreState {
  config: IMConfigView;
  loading: boolean;
  saving: boolean;
  error: string | null;
}

export const imState = reactive<IMStoreState>({
  config: { providers: [], approved: false },
  loading: false,
  saving: false,
  error: null,
});

export const imConfig = computed(() => imState.config);
export const imProviders = computed(() => imState.config.providers);
export const imApproved = computed(() => imState.config.approved);
export const isLoadingIM = computed(() => imState.loading);
export const imError = computed(() => imState.error);

// ---------------------------------------------------------------------------
// Backend 适配层
// ---------------------------------------------------------------------------

export interface IMBackend {
  loadConfig(): Promise<IMConfigView>;
  updateConfig(cfg: IMConfig): Promise<void>;
  isApproved(): Promise<boolean>;
  approve(): Promise<void>;
  sendMessage(providerName: string, channel: string, text: string, attachments: string[]): Promise<void>;
  notify(event: string, title: string, body: string): Promise<void>;
}

let backend: IMBackend | null = null;

export function setIMBackend(b: IMBackend | null): void {
  backend = b;
}

interface IMBindingsShape {
  LoadConfig(): Promise<IMConfigView>;
  UpdateConfig(cfg: IMConfig): Promise<void>;
  IsApproved(): Promise<boolean>;
  Approve(): Promise<void>;
  SendMessage(providerName: string, channel: string, text: string, attachments: string[]): Promise<void>;
  Notify(event: string, title: string, body: string): Promise<void>;
}

let bindingsCache: IMBindingsShape | null = null;

async function loadBindings(): Promise<IMBindingsShape> {
  if (bindingsCache) return bindingsCache;
  // 使用字面量路径（无 @vite-ignore），让 vite 将 bindings 打包为 chunk。
  const mod = await import("../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/imservice.js");
  // bindings 文件使用命名导出，直接将 mod 作为 IMBindingsShape 使用。
  bindingsCache = mod as unknown as IMBindingsShape;
  return bindingsCache;
}

// normalizeConfigView 确保 IMConfigView 的切片字段不为 null。
function normalizeConfigView(c: IMConfigView): IMConfigView {
  return {
    ...c,
    providers: c.providers ?? [],
    notificationRules: c.notificationRules ?? [],
  };
}

function getDefaultBackend(): IMBackend {
  return {
    async loadConfig() {
      const b = await loadBindings();
      return normalizeConfigView(await b.LoadConfig());
    },
    async updateConfig(cfg) {
      const b = await loadBindings();
      await b.UpdateConfig(cfg);
    },
    async isApproved() {
      const b = await loadBindings();
      return b.IsApproved();
    },
    async approve() {
      const b = await loadBindings();
      await b.Approve();
    },
    async sendMessage(providerName, channel, text, attachments) {
      const b = await loadBindings();
      await b.SendMessage(providerName, channel, text, attachments);
    },
    async notify(event, title, body) {
      const b = await loadBindings();
      await b.Notify(event, title, body);
    },
  };
}

function getBackend(): IMBackend {
  if (backend) return backend;
  backend = getDefaultBackend();
  return backend;
}

// ---------------------------------------------------------------------------
// Store actions
// ---------------------------------------------------------------------------

export async function loadIMConfig(): Promise<void> {
  imState.loading = true;
  imState.error = null;
  try {
    imState.config = await getBackend().loadConfig();
  } catch (e: unknown) {
    imState.error = errorMessage(e);
  } finally {
    imState.loading = false;
  }
}

export async function saveIMConfig(cfg: IMConfig): Promise<boolean> {
  imState.saving = true;
  imState.error = null;
  try {
    // Approval is issued only by the backend-owned Approve flow.
    await getBackend().updateConfig({ ...cfg, approved: false });
    await loadIMConfig();
    return true;
  } catch (e: unknown) {
    imState.error = errorMessage(e);
    return false;
  } finally {
    imState.saving = false;
  }
}

/** G-SEC-12：批准 IM 集成（首次发送前必须调用）。 */
export async function approveIM(): Promise<boolean> {
  imState.error = null;
  try {
    await getBackend().approve();
    await loadIMConfig();
    return true;
  } catch (e: unknown) {
    imState.error = errorMessage(e);
    return false;
  }
}

export async function sendTestMessage(providerName: string, text: string): Promise<boolean> {
  imState.error = null;
  try {
    await getBackend().sendMessage(providerName, "", text, []);
    return true;
  } catch (e: unknown) {
    imState.error = errorMessage(e);
    return false;
  }
}

export function resetIMStore(): void {
  imState.config = { providers: [], approved: false };
  imState.loading = false;
  imState.saving = false;
  imState.error = null;
  backend = null;
  bindingsCache = null;
}
