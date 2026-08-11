/**
 * Plan 11 Task 1 Step 9 — aiAssistant store 测试。
 * 覆盖 openStandalonePage/switchMode/setSidebarWidth/toggleContextPanel/
 * setActiveConversation，以及与 aiState.currentConversationId 的同步。
 * mock @/stores/ai 以隔离 Wails bindings 依赖链。
 */
import { describe, it, expect, beforeEach, vi } from "vitest";

// 必须在 import store 之前 mock，避免触发 @/stores/ai → @/api/services
// → ../../bindings 的真实依赖链（Wails bindings 为构建生成物，测试期缺失）。
vi.mock("@/stores/ai", () => ({
  aiState: { currentConversationId: null as string | null },
}));

import {
  aiAssistantState,
  openStandalonePage,
  switchMode,
  setSidebarWidth,
  toggleContextPanel,
  setActiveConversation,
} from "@/stores/aiAssistant";
import { aiState } from "@/stores/ai";

describe("aiAssistant store (Plan 11 Task 1)", () => {
  beforeEach(() => {
    aiAssistantState.mode = "chat";
    aiAssistantState.sidebarWidth = 260;
    aiAssistantState.contextPanelCollapsed = false;
    aiAssistantState.activeConversationId = null;
    aiState.currentConversationId = null;
    // 重置 hash，避免上一个测试的导航残留影响断言。
    window.location.hash = "";
  });

  it("defaults to chat mode with sidebar 260 and expanded context panel", () => {
    expect(aiAssistantState.mode).toBe("chat");
    expect(aiAssistantState.sidebarWidth).toBe(260);
    expect(aiAssistantState.contextPanelCollapsed).toBe(false);
    expect(aiAssistantState.activeConversationId).toBeNull();
  });

  it("switchMode changes the active mode", () => {
    switchMode("plan");
    expect(aiAssistantState.mode).toBe("plan");
    switchMode("goal");
    expect(aiAssistantState.mode).toBe("goal");
    switchMode("agent");
    expect(aiAssistantState.mode).toBe("agent");
    switchMode("chat");
    expect(aiAssistantState.mode).toBe("chat");
  });

  it("setSidebarWidth clamps to the 200–480 range", () => {
    setSidebarWidth(100);
    expect(aiAssistantState.sidebarWidth).toBe(200);
    setSidebarWidth(999);
    expect(aiAssistantState.sidebarWidth).toBe(480);
    setSidebarWidth(320);
    expect(aiAssistantState.sidebarWidth).toBe(320);
  });

  it("toggleContextPanel flips the collapsed flag", () => {
    expect(aiAssistantState.contextPanelCollapsed).toBe(false);
    toggleContextPanel();
    expect(aiAssistantState.contextPanelCollapsed).toBe(true);
    toggleContextPanel();
    expect(aiAssistantState.contextPanelCollapsed).toBe(false);
  });

  it("setActiveConversation sets the active conversation id", () => {
    setActiveConversation("conv-abc");
    expect(aiAssistantState.activeConversationId).toBe("conv-abc");
    setActiveConversation(null);
    expect(aiAssistantState.activeConversationId).toBeNull();
  });

  it("openStandalonePage syncs currentConversationId and navigates to #/ai", () => {
    aiState.currentConversationId = "conv-from-embedded";
    openStandalonePage();
    expect(aiAssistantState.activeConversationId).toBe("conv-from-embedded");
    expect(window.location.hash).toBe("#/ai");
  });

  it("openStandalonePage does not reassign hash when already at #/ai", () => {
    aiState.currentConversationId = "conv-x";
    window.location.hash = "#/ai";
    openStandalonePage();
    expect(aiAssistantState.activeConversationId).toBe("conv-x");
    // hash 保持 #/ai，无重复触发。
    expect(window.location.hash).toBe("#/ai");
  });
});
