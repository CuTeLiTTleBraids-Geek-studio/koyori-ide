import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  activateSkill,
  resetSkillsStore,
  setSkillAgentBackend,
  setSkillsBackend,
  skillsState,
  type SkillsBackend,
} from "./skills";
import type {
  AgentToolCatalog,
  AgentToolExecutionRequest,
  AgentToolExecutionResult,
} from "@/api/automation";

function installSkillsBackend(): void {
  const backend: SkillsBackend = {
    load: vi.fn(async () => undefined),
    listSkills: vi.fn(async () => []),
    getSkill: vi.fn(async () => { throw new Error("unused"); }),
    matchTriggers: vi.fn(async () => []),
    isApproved: vi.fn(async () => false),
  };
  setSkillsBackend(backend);
}

function skillCatalog(): AgentToolCatalog {
  return {
    revision: 7,
    tools: [{
      id: "skill.guarded.activate",
      wireName: "skill_guarded_activate",
      description: "Activate guarded",
      inputSchema: { type: "object", properties: {}, additionalProperties: false },
      source: "skill",
      risk: "elevated",
      approval: "manual",
      mutation: "external",
      metadata: { skillId: "guarded", scope: "project" },
    }],
  };
}

function executionResult(): AgentToolExecutionResult {
  return {
    observation: "Activated skill guarded.",
    usage: {
      unitId: "unit-1",
      sessionId: "skill-activation:guarded",
      unitKind: "tool",
      operation: "skill.guarded.activate",
      cost: 0,
      costBasis: "not-applicable",
      estimated: false,
      success: true,
    },
  };
}

describe("skills store unified Agent activation", () => {
  beforeEach(() => {
    resetSkillsStore();
    installSkillsBackend();
  });

  it("activates a project skill only through its catalog ToolDef", async () => {
    const executeAgentTool = vi.fn(async (_request: AgentToolExecutionRequest) => executionResult());
    setSkillAgentBackend({
      getToolCatalog: vi.fn(async () => skillCatalog()),
      executeAgentTool,
    });

    await expect(activateSkill("guarded")).resolves.toBe(true);
    expect(executeAgentTool).toHaveBeenCalledWith({
      sessionId: "skill-activation:guarded",
      catalogRevision: 7,
      toolId: "skill.guarded.activate",
      arguments: {},
    });
  });

  it("binds activation to the caller's Agent session", async () => {
    const executeAgentTool = vi.fn(async (_request: AgentToolExecutionRequest) => executionResult());
    setSkillAgentBackend({
      getToolCatalog: vi.fn(async () => skillCatalog()),
      executeAgentTool,
    });

    await expect(activateSkill("guarded", "chat-current")).resolves.toBe(true);
    expect(executeAgentTool).toHaveBeenCalledWith(expect.objectContaining({
      sessionId: "chat-current",
      toolId: "skill.guarded.activate",
    }));
  });

  it("fails closed when the backend catalog has no matching skill action", async () => {
    const executeAgentTool = vi.fn(async (_request: AgentToolExecutionRequest) => executionResult());
    setSkillAgentBackend({
      getToolCatalog: vi.fn(async () => ({ revision: 9, tools: [] })),
      executeAgentTool,
    });

    await expect(activateSkill("missing")).resolves.toBe(false);
    expect(executeAgentTool).not.toHaveBeenCalled();
    expect(skillsState.error).toContain("not available in the Agent tool catalog");
  });

  it("closes an ephemeral Agent session after activation", async () => {
    const closeSession = vi.fn(async (_sessionId: string) => undefined);
    const createSession = vi.fn(async (_kind: "chat" | "plan" | "goal" | "workflow") => "chat:ephemeral");
    const executeAgentTool = vi.fn(async (_request: AgentToolExecutionRequest) => executionResult());
    setSkillAgentBackend({
      getToolCatalog: vi.fn(async () => skillCatalog()),
      executeAgentTool,
      createSession,
      closeSession,
    });

    await expect(activateSkill("guarded")).resolves.toBe(true);
    expect(createSession).toHaveBeenCalledWith("chat");
    expect(closeSession).toHaveBeenCalledWith("chat:ephemeral");
  });
});
