import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  aiPermissionState,
  loadAssignments,
  resetAIPermissionStore,
  saveAssignment,
  setAIPermissionBackend,
  type ModelAssignment,
} from "./aiPermission";

vi.mock("@/lib/notifications", () => ({
  notifySuccess: vi.fn(),
  notifyError: vi.fn(),
}));

function assignment(overrides: Partial<ModelAssignment> = {}): ModelAssignment {
  return {
    operation: "chat",
    providerId: "primary-provider",
    model: "gpt-5",
    reasoningEffort: "high",
    fallbackProviderId: "fallback-provider",
    fallbackModel: "gpt-5-mini",
    fallbackReasoningEffort: "low",
    ...overrides,
  };
}

describe("aiPermission assignment reasoning", () => {
  beforeEach(() => {
    resetAIPermissionStore();
    setAIPermissionBackend({
      listAssignments: vi.fn(async () => []),
      setAssignment: vi.fn(async () => undefined),
      getUsageSummary: vi.fn(async () => ({
        totalTokensIn: 0,
        totalTokensOut: 0,
        totalCost: 0,
        byOperation: {},
        byModel: {},
        byDay: {},
      })),
      getCostSuggestions: vi.fn(async () => []),
      checkBudget: vi.fn(async () => ""),
      resetUsage: vi.fn(async () => undefined),
    });
  });

  it("loads primary and fallback reasoning effort without dropping fields", async () => {
    const configured = assignment();
    const listAssignments = vi.fn(async () => [configured]);
    setAIPermissionBackend({
      listAssignments,
      setAssignment: vi.fn(async () => undefined),
      getUsageSummary: vi.fn(async () => ({
        totalTokensIn: 0,
        totalTokensOut: 0,
        totalCost: 0,
        byOperation: {},
        byModel: {},
        byDay: {},
      })),
      getCostSuggestions: vi.fn(async () => []),
      checkBudget: vi.fn(async () => ""),
      resetUsage: vi.fn(async () => undefined),
    });

    await loadAssignments();

    expect(listAssignments).toHaveBeenCalledOnce();
    expect(aiPermissionState.assignments).toEqual([configured]);
    expect(aiPermissionState.assignments[0]).toMatchObject({
      reasoningEffort: "high",
      fallbackReasoningEffort: "low",
    });
    expect(aiPermissionState.loading).toBe(false);
    expect(aiPermissionState.error).toBeNull();
  });

  it("saves the complete assignment and updates the matching operation", async () => {
    const existing = assignment({ reasoningEffort: "low" });
    const setAssignment = vi.fn(async (_value: ModelAssignment) => undefined);
    setAIPermissionBackend({
      listAssignments: vi.fn(async () => [existing]),
      setAssignment,
      getUsageSummary: vi.fn(async () => ({
        totalTokensIn: 0,
        totalTokensOut: 0,
        totalCost: 0,
        byOperation: {},
        byModel: {},
        byDay: {},
      })),
      getCostSuggestions: vi.fn(async () => []),
      checkBudget: vi.fn(async () => ""),
      resetUsage: vi.fn(async () => undefined),
    });
    await loadAssignments();

    const updated = assignment({ reasoningEffort: "medium", fallbackReasoningEffort: "high" });
    await expect(saveAssignment(updated)).resolves.toBe(true);

    expect(setAssignment).toHaveBeenCalledWith(updated);
    expect(aiPermissionState.assignments).toEqual([updated]);
  });

  it("keeps the previous assignment when backend save rejects", async () => {
    const existing = assignment();
    setAIPermissionBackend({
      listAssignments: vi.fn(async () => [existing]),
      setAssignment: vi.fn(async () => { throw new Error("permission backend rejected"); }),
      getUsageSummary: vi.fn(async () => ({
        totalTokensIn: 0,
        totalTokensOut: 0,
        totalCost: 0,
        byOperation: {},
        byModel: {},
        byDay: {},
      })),
      getCostSuggestions: vi.fn(async () => []),
      checkBudget: vi.fn(async () => ""),
      resetUsage: vi.fn(async () => undefined),
    });
    await loadAssignments();

    const replacement = assignment({ reasoningEffort: "medium" });
    await expect(saveAssignment(replacement)).resolves.toBe(false);

    expect(aiPermissionState.assignments).toEqual([existing]);
    expect(aiPermissionState.error).toBe("permission backend rejected");
  });
});
