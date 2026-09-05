/**
 * Plan 11 Task 5 Step 5 — Skills（技能系统）前端 store。
 *
 * 后端 `services/skills_service.go` 的前端对应物。职责：
 *   - 加载已发现的技能列表（项目级 `.koyori-ide/skills/*.yaml` + 用户级
 *     `<configDir>/koyori-ide/skills/*.yaml`）。
 *   - 触发消息匹配（关键词/正则/手动），获取命中的技能集合。
 *   - 项目级技能首次激活需用户显式确认（G-SEC-03）。
 *
 * 安全（G-SEC-02 / G-SEC-03）：
 *   - 项目级技能（Scope=project）默认未批准；UI 必须弹窗确认后调用
 *     ActivateSkill 才会注入其 SystemPrompt。
 *   - AllowedTools 白名单由后端 AgentService.CheckCommand 强制执行，
 *     前端不绕过。
 *
 * 采用与 `mcp.ts` 相同的「lazy bindings + 可注入 backend」模式，便于
 * 单元测试注入 mock。
 */
// Koyori IDE 模块 · Skills。
// 喵，这是 Koyori IDE 的 Skills 模块（前端实现）~

import { reactive, computed, ref } from "vue";
import { errorMessage } from "@/lib/errors";
import {
  agentService,
  type AgentToolCatalog,
  type AgentToolExecutionRequest,
  type AgentToolExecutionResult,
} from "@/api/automation";

// ---------------------------------------------------------------------------
// 类型 — 镜像 Go 结构体（services/skills_service.go）
// ---------------------------------------------------------------------------

export type SkillScope = "project" | "user" | "global";

export interface SkillTrigger {
  keywords?: string[];
  regex?: string;
  /** Manual=true 时仅手动 @Skill 激活，不自动触发。 */
  manual?: boolean;
}

export interface Skill {
  id: string;
  name: string;
  description: string;
  priority: number;
  trigger: SkillTrigger;
  systemPrompt: string;
  allowedTools?: string[];
  allowedMcp?: string[];
  examples?: string[];
  /** 由加载位置决定：project / user / global。 */
  scope: SkillScope;
  filePath?: string;
}

// ---------------------------------------------------------------------------
// Store state
// ---------------------------------------------------------------------------

interface SkillsStoreState {
  /** 所有已加载的技能（合并项目级 + 用户级，项目级覆盖同 ID 用户级）。 */
  skills: Skill[];
  loading: boolean;
  error: string | null;
}

export const skillsState = reactive<SkillsStoreState>({
  skills: [],
  loading: false,
  error: null,
});

export const skillsList = computed(() => skillsState.skills);
export const isLoadingSkills = computed(() => skillsState.loading);
export const skillsError = computed(() => skillsState.error);

// ---------------------------------------------------------------------------
// 编辑/详情对话框状态（SkillsSection.vue 使用）
// ---------------------------------------------------------------------------

/** 当前正在查看/编辑的技能；为 null 时对话框隐藏。 */
export const editingSkill = ref<Skill | null>(null);

export function openSkillEditor(sk?: Skill): void {
  editingSkill.value = sk
    ? {
        ...sk,
        trigger: { ...sk.trigger, keywords: sk.trigger.keywords ? [...sk.trigger.keywords] : undefined },
        allowedTools: sk.allowedTools ? [...sk.allowedTools] : undefined,
        allowedMcp: sk.allowedMcp ? [...sk.allowedMcp] : undefined,
        examples: sk.examples ? [...sk.examples] : undefined,
      }
    : null;
}

export function closeSkillEditor(): void {
  editingSkill.value = null;
}

// ---------------------------------------------------------------------------
// Backend 适配层（lazy 加载 Wails bindings；测试可注入 mock）
// ---------------------------------------------------------------------------

export interface SkillsBackend {
  load(): Promise<void>;
  listSkills(): Promise<Skill[]>;
  getSkill(id: string): Promise<Skill>;
  matchTriggers(message: string): Promise<Skill[]>;
  isApproved(id: string): Promise<boolean>;
}

export interface SkillAgentBackend {
  getToolCatalog(): Promise<AgentToolCatalog>;
  executeAgentTool(request: AgentToolExecutionRequest): Promise<AgentToolExecutionResult>;
	createSession?: (kind: "chat" | "plan" | "goal" | "workflow") => Promise<string>;
	closeSession?: (sessionId: string) => Promise<void>;
}

let backend: SkillsBackend | null = null;
let skillAgentBackend: SkillAgentBackend = agentService;

/** 注入 backend 适配器。测试注入 mock；应用启动时注入默认 Wails 适配器。 */
export function setSkillsBackend(b: SkillsBackend | null): void {
  backend = b;
}

export function setSkillAgentBackend(b: SkillAgentBackend | null): void {
  skillAgentBackend = b ?? agentService;
}

interface SkillsBindingsShape {
  Load(): Promise<void>;
  ListSkills(): Promise<Skill[]>;
  GetSkill(id: string): Promise<Skill>;
  MatchTriggers(message: string): Promise<Skill[]>;
  IsApproved(id: string): Promise<boolean>;
}

let bindingsCache: SkillsBindingsShape | null = null;

async function loadBindings(): Promise<SkillsBindingsShape> {
  if (bindingsCache) return bindingsCache;
  // 使用字面量路径（无 @vite-ignore），让 vite 将 bindings 打包为 chunk。
  const mod = await import("../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/skillsservice.js");
  // bindings 文件使用命名导出，直接将 mod 作为 SkillsBindingsShape 使用。
  bindingsCache = mod as unknown as SkillsBindingsShape;
  return bindingsCache;
}

function getDefaultBackend(): SkillsBackend {
  return {
    async load() {
      const b = await loadBindings();
      await b.Load();
    },
    async listSkills() {
      const b = await loadBindings();
      return (await b.ListSkills()) ?? [];
    },
    async getSkill(id) {
      const b = await loadBindings();
      return b.GetSkill(id);
    },
    async matchTriggers(message) {
      const b = await loadBindings();
      return (await b.MatchTriggers(message)) ?? [];
    },
    async isApproved(id) {
      const b = await loadBindings();
      return b.IsApproved(id);
    },
  };
}

function getBackend(): SkillsBackend {
  if (backend) return backend;
  backend = getDefaultBackend();
  return backend;
}

// ---------------------------------------------------------------------------
// Store actions
// ---------------------------------------------------------------------------

/**
 * 从后端加载所有已发现的技能并同步本地 store。
 * 后端 Load() 会扫描项目级与用户级目录，合并后返回。
 */
export async function loadSkills(): Promise<void> {
  skillsState.loading = true;
  skillsState.error = null;
  try {
    await getBackend().load();
    skillsState.skills = await getBackend().listSkills();
  } catch (e: unknown) {
    skillsState.error = errorMessage(e);
  } finally {
    skillsState.loading = false;
  }
}

/** 仅刷新本地技能列表（不重新扫描磁盘）。用于 ActivateSkill 后同步状态。 */
export async function refreshSkillsList(): Promise<void> {
  skillsState.error = null;
  try {
    skillsState.skills = await getBackend().listSkills();
  } catch (e: unknown) {
    skillsState.error = errorMessage(e);
  }
}

/**
 * 批准一个项目级技能（G-SEC-03）。
 * 调用此方法后该技能的 SystemPrompt 才会在 MatchTriggers 命中时注入。
 */
export async function activateSkill(id: string, sessionId?: string): Promise<boolean> {
  skillsState.error = null;
	let ephemeralSession = "";
  try {
	let activationSession = sessionId ?? "";
	if (!activationSession.trim() && typeof skillAgentBackend.createSession === "function") {
		activationSession = await skillAgentBackend.createSession("chat");
		ephemeralSession = activationSession;
	}
	if (!activationSession.trim()) activationSession = `skill-activation:${id}`;
	if (activationSession.trim() === "") {
		throw new Error("Skill activation requires an Agent session");
	}
    const catalog = await skillAgentBackend.getToolCatalog();
    const tool = catalog.tools.find(
      (candidate) => candidate.source === "skill" && candidate.metadata?.skillId === id,
    );
    if (!tool) {
      throw new Error(`Skill "${id}" is not available in the Agent tool catalog`);
    }
    await skillAgentBackend.executeAgentTool({
	  sessionId: activationSession,
      catalogRevision: catalog.revision,
      toolId: tool.id,
      arguments: {},
    });
    await refreshSkillsList();
    return true;
  } catch (e: unknown) {
    skillsState.error = errorMessage(e);
    return false;
	} finally {
		if (ephemeralSession.trim() && typeof skillAgentBackend.closeSession === "function") {
			try {
				await skillAgentBackend.closeSession(ephemeralSession);
			} catch {
				// Activation result is already determined; cleanup is best effort.
			}
		}
  }
}

/** 查询技能是否已获用户批准。项目级默认 false；用户级/全局始终 true。 */
export async function isSkillApproved(id: string): Promise<boolean> {
  try {
    return await getBackend().isApproved(id);
  } catch (e: unknown) {
    skillsState.error = errorMessage(e);
    return false;
  }
}

/** 测试消息命中哪些技能（手动触发器除外）。 */
export async function matchSkillTriggers(message: string): Promise<Skill[]> {
  try {
    return await getBackend().matchTriggers(message);
  } catch (e: unknown) {
    skillsState.error = errorMessage(e);
    return [];
  }
}

/** 重置 store 状态。测试专用。 */
export function resetSkillsStore(): void {
  skillsState.skills = [];
  skillsState.loading = false;
  skillsState.error = null;
  editingSkill.value = null;
  backend = null;
  skillAgentBackend = agentService;
  bindingsCache = null;
}
