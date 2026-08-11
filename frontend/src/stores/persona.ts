/**
 * Plan 11 Task 8 Step 3-6 — Persona 前端 store。
 *
 * 后端 `services/persona_service.go` 的前端对应物。职责：
 *   - 加载内置 7 个 + 用户自定义 Persona。
 *   - CRUD + 市场 导出/导入。
 *   - 知识库注入（BuildSystemPromptWithKnowledge）。
 */
// Koyori IDE 模块 · Persona。
// 喵，这是 Koyori IDE 的 Persona 模块（前端实现）~

import { reactive, computed } from "vue";
import { errorMessage } from "@/lib/errors";

// ---------------------------------------------------------------------------
// 类型 — 镜像 Go 结构体
// ---------------------------------------------------------------------------

export interface Persona {
  id: string;
  name: string;
  avatar?: string;
  systemPrompt: string;
  tone?: string;
  expertise?: string[];
  knowledgeBase?: string[];
  defaultModel?: string;
  defaultMode?: string;
  builtIn: boolean;
  createdAt?: string;
  updatedAt?: string;
}

interface PersonaStoreState {
  personas: Persona[];
  activePersonaId: string | null;
  loading: boolean;
  saving: boolean;
  error: string | null;
}

export const personaState = reactive<PersonaStoreState>({
  personas: [],
  activePersonaId: null,
  loading: false,
  saving: false,
  error: null,
});

export const personasList = computed(() => personaState.personas);
export const activePersona = computed(() =>
  personaState.personas.find((p) => p.id === personaState.activePersonaId) ?? null
);
export const isLoadingPersonas = computed(() => personaState.loading);
export const personaError = computed(() => personaState.error);

// ---------------------------------------------------------------------------
// Backend 适配层
// ---------------------------------------------------------------------------

export interface PersonaBackend {
  listPersonas(): Promise<Persona[]>;
  getPersona(id: string): Promise<Persona>;
  createPersona(p: Persona): Promise<void>;
  updatePersona(p: Persona): Promise<void>;
  deletePersona(id: string): Promise<void>;
  buildSystemPromptWithKnowledge(id: string, maxTokens: number): Promise<string>;
  exportPersona(id: string): Promise<string>;
  importPersona(data: string): Promise<void>;
}

let backend: PersonaBackend | null = null;

export function setPersonaBackend(b: PersonaBackend | null): void {
  backend = b;
}

interface PersonaBindingsShape {
  ListPersonas(): Promise<Persona[]>;
  GetPersona(id: string): Promise<Persona>;
  CreatePersona(p: Persona): Promise<void>;
  UpdatePersona(p: Persona): Promise<void>;
  DeletePersona(id: string): Promise<void>;
  BuildSystemPromptWithKnowledge(id: string, maxTokens: number): Promise<string>;
  ExportPersona(id: string): Promise<string>;
  ImportPersona(data: string): Promise<void>;
}

let bindingsCache: PersonaBindingsShape | null = null;

async function loadBindings(): Promise<PersonaBindingsShape> {
  if (bindingsCache) return bindingsCache;
  // 使用字面量路径（无 @vite-ignore），让 vite 将 bindings 打包为 chunk。
  const mod = await import("../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/personaservice.js");
  // bindings 文件使用命名导出，直接将 mod 作为 PersonaBindingsShape 使用。
  bindingsCache = mod as unknown as PersonaBindingsShape;
  return bindingsCache;
}

// normalizePersona 确保 Persona 的切片字段不为 null。
function normalizePersona(p: Persona): Persona {
  return {
    ...p,
    expertise: p.expertise ?? [],
    knowledgeBase: p.knowledgeBase ?? [],
  };
}

function getDefaultBackend(): PersonaBackend {
  return {
    async listPersonas() {
      const b = await loadBindings();
      return (await b.ListPersonas())?.map(normalizePersona) ?? [];
    },
    async getPersona(id) {
      const b = await loadBindings();
      return normalizePersona(await b.GetPersona(id));
    },
    async createPersona(p) {
      const b = await loadBindings();
      await b.CreatePersona(p);
    },
    async updatePersona(p) {
      const b = await loadBindings();
      await b.UpdatePersona(p);
    },
    async deletePersona(id) {
      const b = await loadBindings();
      await b.DeletePersona(id);
    },
    async buildSystemPromptWithKnowledge(id, maxTokens) {
      const b = await loadBindings();
      return b.BuildSystemPromptWithKnowledge(id, maxTokens);
    },
    async exportPersona(id) {
      const b = await loadBindings();
      return b.ExportPersona(id);
    },
    async importPersona(data) {
      const b = await loadBindings();
      await b.ImportPersona(data);
    },
  };
}

function getBackend(): PersonaBackend {
  if (backend) return backend;
  backend = getDefaultBackend();
  return backend;
}

// ---------------------------------------------------------------------------
// Store actions
// ---------------------------------------------------------------------------

export async function loadPersonas(): Promise<void> {
  personaState.loading = true;
  personaState.error = null;
  try {
    personaState.personas = await getBackend().listPersonas();
  } catch (e: unknown) {
    personaState.error = errorMessage(e);
  } finally {
    personaState.loading = false;
  }
}

export async function createPersona(p: Persona): Promise<boolean> {
  personaState.saving = true;
  personaState.error = null;
  try {
    await getBackend().createPersona(p);
    await loadPersonas();
    return true;
  } catch (e: unknown) {
    personaState.error = errorMessage(e);
    return false;
  } finally {
    personaState.saving = false;
  }
}

export async function updatePersona(p: Persona): Promise<boolean> {
  personaState.saving = true;
  personaState.error = null;
  try {
    await getBackend().updatePersona(p);
    await loadPersonas();
    return true;
  } catch (e: unknown) {
    personaState.error = errorMessage(e);
    return false;
  } finally {
    personaState.saving = false;
  }
}

export async function deletePersona(id: string): Promise<boolean> {
  personaState.error = null;
  try {
    await getBackend().deletePersona(id);
    await loadPersonas();
    return true;
  } catch (e: unknown) {
    personaState.error = errorMessage(e);
    return false;
  }
}

export function setActivePersona(id: string | null): void {
  personaState.activePersonaId = id;
}

export async function exportPersona(id: string): Promise<string | null> {
  personaState.error = null;
  try {
    return await getBackend().exportPersona(id);
  } catch (e: unknown) {
    personaState.error = errorMessage(e);
    return null;
  }
}

export async function importPersona(data: string): Promise<boolean> {
  personaState.error = null;
  try {
    await getBackend().importPersona(data);
    await loadPersonas();
    return true;
  } catch (e: unknown) {
    personaState.error = errorMessage(e);
    return false;
  }
}

export function resetPersonaStore(): void {
  personaState.personas = [];
  personaState.activePersonaId = null;
  personaState.loading = false;
  personaState.saving = false;
  personaState.error = null;
  backend = null;
  bindingsCache = null;
}
