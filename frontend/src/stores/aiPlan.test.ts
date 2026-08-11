import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  aiPlanState,
  editStep,
  resetPlanStore,
  setPlanBackend,
} from "./aiPlan";
import type { Plan, PlanBackend, PlanStep } from "./aiPlan";

function makePlan(step: PlanStep): Plan {
  return {
    id: "plan-1",
    goal: "test plan",
    steps: [step],
    status: "pending",
    createdAt: "2026-08-07T00:00:00Z",
  };
}

function makeBackend(overrides: Partial<PlanBackend> = {}): PlanBackend {
  const pending = makePlan({ title: "old", description: "old description", status: "pending" });
  return {
    createPlan: async () => pending,
    getPlan: async () => pending,
    getActivePlan: async () => pending,
    approveStep: async () => undefined,
    approveAll: async () => undefined,
    rejectAll: async () => undefined,
    editStep: async () => undefined,
    executeStep: async () => undefined,
    skipStep: async () => undefined,
    replan: async () => undefined,
    abortPlan: async () => undefined,
    getStepResult: async (_planId, _stepIdx) => pending.steps[0],
    listPlans: async () => [pending],
    ...overrides,
  };
}

describe("aiPlan editStep", () => {
  beforeEach(() => resetPlanStore());

  it("submits the edited step and refreshes the active plan", async () => {
    const edited: PlanStep = {
      title: "new title",
      description: "new description",
      status: "pending",
      tool: "read_file",
      args: "src/main.go",
    };
    const refreshed = makePlan({ ...edited, status: "approved" });
    const editStepMock = vi.fn().mockResolvedValue(undefined);
    const getPlanMock = vi.fn().mockResolvedValue(refreshed);
    setPlanBackend(makeBackend({ editStep: editStepMock, getPlan: getPlanMock }));

    await expect(editStep("plan-1", 0, edited)).resolves.toBe(true);

    expect(editStepMock).toHaveBeenCalledOnce();
    expect(editStepMock).toHaveBeenCalledWith("plan-1", 0, edited);
    expect(getPlanMock).toHaveBeenCalledWith("plan-1");
    expect(aiPlanState.activePlan).toEqual(refreshed);
    expect(aiPlanState.error).toBeNull();
  });

  it("keeps the editing operation failed and exposes the backend error", async () => {
    const getPlanMock = vi.fn();
    setPlanBackend(makeBackend({
      editStep: vi.fn().mockRejectedValue(new Error("step is no longer pending")),
      getPlan: getPlanMock,
    }));

    await expect(editStep("plan-1", 0, {
      title: "new title",
      description: "",
      status: "pending",
    })).resolves.toBe(false);

    expect(getPlanMock).not.toHaveBeenCalled();
    expect(aiPlanState.error).toBe("step is no longer pending");
  });
});
