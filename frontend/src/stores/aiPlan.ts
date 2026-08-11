/**
 * Plan 11 Task 9 Step 5 — Plan 模式前端 store。
 *
 * 后端 `services/ai_plan_service.go` 的前端对应物。职责：
 *   - 创建/审批/执行/中止 Plan。
 *   - 步骤回放查看详情。
 *   - Plan 与 Goal 互斥（Step 8）。
 */
// Koyori IDE 模块 · Ai Plan。
// 喵，这是 Koyori IDE 的 Ai Plan 模块（前端实现）~

import { reactive, computed } from "vue";
import { errorMessage } from "@/lib/errors";

// ---------------------------------------------------------------------------
// 类型 — 镜像 Go 结构体
// ---------------------------------------------------------------------------

export type PlanStepStatus = "pending" | "approved" | "executing" | "completed" | "failed" | "skipped";
export type PlanStatus = "draft" | "pending" | "executing" | "paused" | "completed" | "aborted";

export interface PlanStep {
  title: string;
  description: string;
  status: PlanStepStatus;
  tool?: string;
  args?: string;
  result?: string;
  error?: string;
  startedAt?: string;
  finishedAt?: string;
}

export interface Plan {
  id: string;
  goal: string;
  steps: PlanStep[];
  status: PlanStatus;
  createdAt: string;
  approvedAt?: string;
  finishedAt?: string;
}

interface PlanStoreState {
  activePlan: Plan | null;
  plans: Plan[];
  loading: boolean;
  error: string | null;
}

export const aiPlanState = reactive<PlanStoreState>({
  activePlan: null,
  plans: [],
  loading: false,
  error: null,
});

export const activePlan = computed(() => aiPlanState.activePlan);
export const plansList = computed(() => aiPlanState.plans);
export const isLoadingPlans = computed(() => aiPlanState.loading);
export const planError = computed(() => aiPlanState.error);

// ---------------------------------------------------------------------------
// Backend 适配层
// ---------------------------------------------------------------------------

export interface PlanBackend {
  createPlan(id: string, goal: string, steps: PlanStep[]): Promise<Plan | null>;
  getPlan(id: string): Promise<Plan | null>;
  getActivePlan(): Promise<Plan | null>;
  approveStep(planId: string, stepIdx: number): Promise<void>;
  approveAll(planId: string): Promise<void>;
  rejectAll(planId: string): Promise<void>;
  editStep(planId: string, stepIdx: number, newStep: PlanStep): Promise<void>;
  executeStep(planId: string, stepIdx: number): Promise<void>;
  skipStep(planId: string, stepIdx: number): Promise<void>;
  replan(planId: string, newSteps: PlanStep[]): Promise<void>;
  abortPlan(planId: string): Promise<void>;
  getStepResult(planId: string, stepIdx: number): Promise<PlanStep>;
  listPlans(): Promise<(Plan | null)[]>;
}

let backend: PlanBackend | null = null;

export function setPlanBackend(b: PlanBackend | null): void {
  backend = b;
}

interface PlanBindingsShape {
  CreatePlan(id: string, goal: string, steps: PlanStep[]): Promise<Plan>;
  GetPlan(id: string): Promise<Plan>;
  GetActivePlan(): Promise<Plan | null>;
  ApproveStep(planId: string, stepIdx: number): Promise<void>;
  ApproveAll(planId: string): Promise<void>;
  RejectAll(planId: string): Promise<void>;
  EditStep(planId: string, stepIdx: number, newStep: PlanStep): Promise<void>;
  // executor 为 Go 接口，前端传 null，后端回退到内部注入的 executor。
  ExecuteStep(planId: string, stepIdx: number, executor: unknown): Promise<void>;
  SkipStep(planId: string, stepIdx: number): Promise<void>;
  Replan(planId: string, newSteps: PlanStep[]): Promise<void>;
  AbortPlan(planId: string): Promise<void>;
  GetStepResult(planId: string, stepIdx: number): Promise<PlanStep>;
  ListPlans(): Promise<Plan[]>;
}

let bindingsCache: PlanBindingsShape | null = null;

async function loadBindings(): Promise<PlanBindingsShape> {
  if (bindingsCache) return bindingsCache;
  // 使用字面量路径（无 @vite-ignore），让 vite 将 bindings 打包为 chunk。
  const mod = await import("../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/aiplanservice.js");
  // bindings 文件使用命名导出，直接将 mod 作为 PlanBindingsShape 使用。
  bindingsCache = mod as unknown as PlanBindingsShape;
  return bindingsCache;
}

// normalizePlan 确保 Plan 的 steps 切片不为 null（Go nil 切片序列化为 null）。
// 否则组件模板访问 plan.steps.length 会报 "Cannot read properties of null"。
function normalizePlan(p: Plan | null): Plan | null {
  if (!p) return null;
  return { ...p, steps: p.steps ?? [] };
}

function getDefaultBackend(): PlanBackend {
  return {
    async createPlan(id, goal, steps) {
      const b = await loadBindings();
      return normalizePlan(await b.CreatePlan(id, goal, steps));
    },
    async getPlan(id) {
      const b = await loadBindings();
      return normalizePlan(await b.GetPlan(id));
    },
    async getActivePlan() {
      const b = await loadBindings();
      return normalizePlan(await b.GetActivePlan());
    },
    async approveStep(planId, stepIdx) {
      const b = await loadBindings();
      await b.ApproveStep(planId, stepIdx);
    },
    async approveAll(planId) {
      const b = await loadBindings();
      await b.ApproveAll(planId);
    },
    async rejectAll(planId) {
      const b = await loadBindings();
      await b.RejectAll(planId);
    },
    async editStep(planId, stepIdx, newStep) {
      const b = await loadBindings();
      await b.EditStep(planId, stepIdx, newStep);
    },
    async executeStep(planId, stepIdx) {
      const b = await loadBindings();
      // executor 为 Go 接口，前端传 null，后端回退到内部注入的 StepExecutor。
      await b.ExecuteStep(planId, stepIdx, null);
    },
    async skipStep(planId, stepIdx) {
      const b = await loadBindings();
      await b.SkipStep(planId, stepIdx);
    },
    async replan(planId, newSteps) {
      const b = await loadBindings();
      await b.Replan(planId, newSteps);
    },
    async abortPlan(planId) {
      const b = await loadBindings();
      await b.AbortPlan(planId);
    },
    async getStepResult(planId, stepIdx) {
      const b = await loadBindings();
      return b.GetStepResult(planId, stepIdx);
    },
    async listPlans() {
      const b = await loadBindings();
      return (await b.ListPlans())?.map(normalizePlan) ?? [];
    },
  };
}

function getBackend(): PlanBackend {
  if (backend) return backend;
  backend = getDefaultBackend();
  return backend;
}

// ---------------------------------------------------------------------------
// Store actions
// ---------------------------------------------------------------------------

export async function refreshActivePlan(): Promise<void> {
  aiPlanState.error = null;
  try {
    aiPlanState.activePlan = await getBackend().getActivePlan();
  } catch (e: unknown) {
    aiPlanState.error = errorMessage(e);
  }
}

export async function createPlan(id: string, goal: string, steps: PlanStep[]): Promise<boolean> {
  aiPlanState.error = null;
  try {
    aiPlanState.activePlan = await getBackend().createPlan(id, goal, steps);
    return true;
  } catch (e: unknown) {
    aiPlanState.error = errorMessage(e);
    return false;
  }
}

export async function approveStep(planId: string, stepIdx: number): Promise<boolean> {
  aiPlanState.error = null;
  try {
    await getBackend().approveStep(planId, stepIdx);
    aiPlanState.activePlan = await getBackend().getPlan(planId);
    return true;
  } catch (e: unknown) {
    aiPlanState.error = errorMessage(e);
    return false;
  }
}

export async function editStep(planId: string, stepIdx: number, newStep: PlanStep): Promise<boolean> {
  aiPlanState.error = null;
  try {
    await getBackend().editStep(planId, stepIdx, newStep);
    aiPlanState.activePlan = await getBackend().getPlan(planId);
    return true;
  } catch (e: unknown) {
    aiPlanState.error = errorMessage(e);
    return false;
  }
}

export async function approveAll(planId: string): Promise<boolean> {
  aiPlanState.error = null;
  try {
    await getBackend().approveAll(planId);
    aiPlanState.activePlan = await getBackend().getPlan(planId);
    return true;
  } catch (e: unknown) {
    aiPlanState.error = errorMessage(e);
    return false;
  }
}

export async function rejectAll(planId: string): Promise<boolean> {
  aiPlanState.error = null;
  try {
    await getBackend().rejectAll(planId);
    aiPlanState.activePlan = await getBackend().getPlan(planId);
    return true;
  } catch (e: unknown) {
    aiPlanState.error = errorMessage(e);
    return false;
  }
}

export async function executeStep(planId: string, stepIdx: number): Promise<boolean> {
  aiPlanState.error = null;
  try {
    await getBackend().executeStep(planId, stepIdx);
    aiPlanState.activePlan = await getBackend().getPlan(planId);
    return true;
  } catch (e: unknown) {
    aiPlanState.error = errorMessage(e);
    return false;
  }
}

export async function skipStep(planId: string, stepIdx: number): Promise<boolean> {
  aiPlanState.error = null;
  try {
    await getBackend().skipStep(planId, stepIdx);
    aiPlanState.activePlan = await getBackend().getPlan(planId);
    return true;
  } catch (e: unknown) {
    aiPlanState.error = errorMessage(e);
    return false;
  }
}

export async function replan(planId: string, newSteps: PlanStep[]): Promise<boolean> {
  aiPlanState.error = null;
  try {
    await getBackend().replan(planId, newSteps);
    aiPlanState.activePlan = await getBackend().getPlan(planId);
    return true;
  } catch (e: unknown) {
    aiPlanState.error = errorMessage(e);
    return false;
  }
}

export async function abortPlan(planId: string): Promise<boolean> {
  aiPlanState.error = null;
  try {
    await getBackend().abortPlan(planId);
    aiPlanState.activePlan = null;
    return true;
  } catch (e: unknown) {
    aiPlanState.error = errorMessage(e);
    return false;
  }
}

export async function getStepResult(planId: string, stepIdx: number): Promise<PlanStep | null> {
  aiPlanState.error = null;
  try {
    return await getBackend().getStepResult(planId, stepIdx);
  } catch (e: unknown) {
    aiPlanState.error = errorMessage(e);
    return null;
  }
}

export function resetPlanStore(): void {
  aiPlanState.activePlan = null;
  aiPlanState.plans = [];
  aiPlanState.loading = false;
  aiPlanState.error = null;
  backend = null;
  bindingsCache = null;
}
