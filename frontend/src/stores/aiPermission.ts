// Plan 11 Task 12 — AI 模型权限分配前端 store。
//
// 使用 lazy bindings + 可注入 backend 模式：
//   - 生产环境懒加载 Wails bindings
//   - 测试环境通过 setAIPermissionBackend 注入 mock
//
// 职责（Step 7-8）：
//   - 加载/保存 ModelAssignment 列表
//   - 获取用量汇总（按天/周/月/操作/模型）
//   - 获取成本优化建议
//   - 预算告警
// Koyori IDE 模块 · Ai Permission。
// 喵，这是 Koyori IDE 的 Ai Permission 模块（前端实现）~
import { reactive } from "vue";
import { notifyError, notifySuccess } from "@/lib/notifications";

// 操作类型（与后端 AIOperation 对应）
export type AIOperation =
  | "chat"
  | "inline-completion"
  | "agent"
  | "review"
  | "commit-message"
  | "title-generation"
  | "plan"
  | "goal";

export interface ModelAssignment {
  operation: AIOperation;
  providerId: string;
  model: string;
  temperature?: number;
  maxTokens?: number;
  fallbackProviderId?: string;
  fallbackModel?: string;
  disabled?: boolean;
}

export interface ModelResolution {
  primary: ModelAssignment;
  fallback?: ModelAssignment;
}

export interface UsageRecord {
  timestamp: string;
  operation: AIOperation;
  providerId: string;
  model: string;
  tokensIn: number;
  tokensOut: number;
  cost: number;
}

export interface OperationUsage {
  tokensIn: number;
  tokensOut: number;
  cost: number;
  count: number;
}

export interface UsageSummary {
  totalTokensIn: number;
  totalTokensOut: number;
  totalCost: number;
  byOperation: Record<string, OperationUsage>;
  byModel: Record<string, OperationUsage>;
  byDay: Record<string, OperationUsage>;
}

export interface CostSuggestion {
  operation: AIOperation;
  currentModel: string;
  suggestedModel: string;
  reason: string;
  estimatedSavings: number;
}

export interface BudgetAlert {
  monthlyBudget: number;
  thresholdPct: number;
}

interface AIPermissionBackend {
  listAssignments(): Promise<ModelAssignment[]>;
  setAssignment(a: ModelAssignment): Promise<void>;
  getUsageSummary(period: string): Promise<UsageSummary>;
  getCostSuggestions(): Promise<CostSuggestion[]>;
  checkBudget(b: BudgetAlert): Promise<string>;
  resetUsage(): Promise<void>;
}

// Bindings shape — 前端类型与 bindings 生成的枚举类型存在差异（AIOperation
// 为 string union vs enum），通过 shape 接口统一为前端类型，避免 TS2345。
interface PermissionBindingsShape {
  ListAssignments(): Promise<ModelAssignment[]>;
  SetAssignment(a: ModelAssignment): Promise<void>;
  GetUsageSummary(period: string): Promise<UsageSummary>;
  GetCostSuggestions(): Promise<CostSuggestion[]>;
  CheckBudget(b: BudgetAlert): Promise<string>;
  ResetUsage(): Promise<void>;
}

interface AIPermissionState {
  assignments: ModelAssignment[];
  usageSummary: UsageSummary | null;
  suggestions: CostSuggestion[];
  budgetAlert: string;
  loading: boolean;
  error: string | null;
}

export const aiPermissionState = reactive<AIPermissionState>({
  assignments: [],
  usageSummary: null,
  suggestions: [],
  budgetAlert: "",
  loading: false,
  error: null,
});

let backend: AIPermissionBackend | null = null;

// 懒加载 Wails bindings
async function getBackend(): Promise<AIPermissionBackend> {
  if (backend) return backend;
  // 使用字面量路径（无 @vite-ignore），让 vite 将 bindings 打包为 chunk。
  const mod = await import("../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/aipermissionservice.js");
  // bindings 文件使用命名导出，直接将 mod 作为 PermissionBindingsShape 使用。
  const b = mod as unknown as PermissionBindingsShape;
  backend = {
    listAssignments: async () => (await b.ListAssignments()) ?? [],
    setAssignment: (a: ModelAssignment) => b.SetAssignment(a) as Promise<void>,
    getUsageSummary: async (period: string) => {
      const s = await b.GetUsageSummary(period);
      // Go nil map 序列化为 null，组件访问 byDay/byModel/byOperation 会报错。
      return {
        ...s,
        byOperation: s.byOperation ?? {},
        byModel: s.byModel ?? {},
        byDay: s.byDay ?? {},
      };
    },
    getCostSuggestions: async () => (await b.GetCostSuggestions()) ?? [],
    checkBudget: (budget: BudgetAlert) => b.CheckBudget(budget) as Promise<string>,
    resetUsage: () => b.ResetUsage() as Promise<void>,
  };
  return backend;
}

// 测试注入
export function setAIPermissionBackend(b: AIPermissionBackend): void {
  backend = b;
}

export async function loadAssignments(): Promise<void> {
  aiPermissionState.loading = true;
  aiPermissionState.error = null;
  try {
    const b = await getBackend();
    aiPermissionState.assignments = await b.listAssignments();
  } catch (e: unknown) {
    aiPermissionState.error = e instanceof Error ? e.message : String(e);
    notifyError(aiPermissionState.error);
  } finally {
    aiPermissionState.loading = false;
  }
}

export async function saveAssignment(a: ModelAssignment): Promise<boolean> {
  try {
    const b = await getBackend();
    await b.setAssignment(a);
    // 更新本地状态
    const idx = aiPermissionState.assignments.findIndex((x) => x.operation === a.operation);
    if (idx >= 0) {
      aiPermissionState.assignments[idx] = a;
    } else {
      aiPermissionState.assignments.push(a);
    }
    notifySuccess(`Assignment for ${a.operation} saved`);
    return true;
  } catch (e: unknown) {
    aiPermissionState.error = e instanceof Error ? e.message : String(e);
    notifyError(aiPermissionState.error);
    return false;
  }
}

export async function loadUsageSummary(period: string): Promise<void> {
  try {
    const b = await getBackend();
    aiPermissionState.usageSummary = await b.getUsageSummary(period);
  } catch (e: unknown) {
    aiPermissionState.error = e instanceof Error ? e.message : String(e);
  }
}

export async function loadCostSuggestions(): Promise<void> {
  try {
    const b = await getBackend();
    aiPermissionState.suggestions = await b.getCostSuggestions();
  } catch (e: unknown) {
    aiPermissionState.error = e instanceof Error ? e.message : String(e);
  }
}

export async function checkBudget(budget: BudgetAlert): Promise<void> {
  try {
    const b = await getBackend();
    aiPermissionState.budgetAlert = await b.checkBudget(budget);
  } catch (e: unknown) {
    aiPermissionState.error = e instanceof Error ? e.message : String(e);
  }
}

export async function resetUsage(): Promise<boolean> {
  try {
    const b = await getBackend();
    await b.resetUsage();
    aiPermissionState.usageSummary = null;
    aiPermissionState.suggestions = [];
    notifySuccess("Usage history cleared");
    return true;
  } catch (e: unknown) {
    aiPermissionState.error = e instanceof Error ? e.message : String(e);
    notifyError(aiPermissionState.error);
    return false;
  }
}

export function resetAIPermissionStore(): void {
  aiPermissionState.assignments = [];
  aiPermissionState.usageSummary = null;
  aiPermissionState.suggestions = [];
  aiPermissionState.budgetAlert = "";
  aiPermissionState.loading = false;
  aiPermissionState.error = null;
}
