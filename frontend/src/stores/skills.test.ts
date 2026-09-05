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

// P19 CI 修复：@/api/automation 静态引入生成的 bindings（→ @wailsio/runtime
// → drag.js 的 window.setInterval 轮询，最多 100 次）。本文件只有 4 个快速
// 用例，轮询来不及自我清理就会在 jsdom 环境销毁后触发
// "window is not defined"（Linux CI 可稳定复现并使 vitest 以 Unhandled
// Error 退出 1）。mock 掉 automation 阻断这条模块加载链；被测行为由注入的
// SkillsBackend/SkillAgentBackend 桩覆盖，不依赖真实 agentService。
vi.mock("@/api/automation", () => ({
  agentService: {
    getToolCatalog: vi.fn(async () => ({ revision: 0, tools: [] })),
    executeAgentTool: vi.fn(async () => {
      throw new Error("not used: tests inject their own agent backend");
    }),
    createSession: vi.fn(async () => ""),
    closeSession: vi.fn(async () => undefined),
  },
}));

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
