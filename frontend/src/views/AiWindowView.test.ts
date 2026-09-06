import { enableAutoUnmount, flushPromises, mount } from "@vue/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const {
  projectServiceMock,
  settingsServiceMock,
  conversationServiceMock,
  crossWindowHandlers,
} = vi.hoisted(() => ({
	projectServiceMock: {
		getRecentProjects: vi.fn(),
		addProject: vi.fn(),
		getWorkspaceSnapshot: vi.fn(),
	},
  settingsServiceMock: {
    loadSettings: vi.fn(),
    saveSettings: vi.fn(),
  },
  conversationServiceMock: {
    updateTitle: vi.fn(),
    load: vi.fn(),
  },
  crossWindowHandlers: {} as Record<string, ((event: unknown) => void) | undefined>,
}));

vi.mock("@wailsio/runtime", () => ({
  Events: { On: vi.fn(), Emit: vi.fn() },
}));

vi.mock("@/lib/crossWindowSync", () => ({
  subscribeCrossWindowEvent: vi.fn((name: string, handler: (event: unknown) => void) => {
    crossWindowHandlers[name] = handler;
    return () => {
      if (crossWindowHandlers[name] === handler) delete crossWindowHandlers[name];
    };
  }),
}));

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({ t: (key: string) => key }),
  translate: (key: string) => key,
}));

vi.mock("@/api/services", () => ({
  settingsService: {
    loadSettings: vi.fn((...args: unknown[]) => (
      globalThis as typeof globalThis & {
        __koyoriIdeSettingsServiceTestBinding?: typeof settingsServiceMock;
      }
    ).__koyoriIdeSettingsServiceTestBinding?.loadSettings(...args)),
    saveSettings: vi.fn((...args: unknown[]) => (
      globalThis as typeof globalThis & {
        __koyoriIdeSettingsServiceTestBinding?: typeof settingsServiceMock;
      }
    ).__koyoriIdeSettingsServiceTestBinding?.saveSettings(...args)),
  },
  conversationService: conversationServiceMock,
  windowService: {
    isAIAlwaysOnTop: vi.fn().mockResolvedValue(true),
    setAIAlwaysOnTop: vi.fn(),
    openPathInExplorer: vi.fn(),
    openPathInVSCode: vi.fn(),
    isAIWindowMaximised: vi.fn().mockResolvedValue(false),
    minimiseAIWindow: vi.fn(),
    toggleMaximiseAIWindow: vi.fn(),
    closeAIWindow: vi.fn(),
  },
  projectService: projectServiceMock,
}));

vi.mock("@/stores/mcp", () => ({ agentMcpTools: [], refreshAgentMcpTools: vi.fn() }));
vi.mock("@/stores/skills", () => ({ skillsList: [], loadSkills: vi.fn() }));
vi.mock("@/stores/snapshot", () => ({ setSnapshotWorkspaceRoot: vi.fn(), listSnapshots: vi.fn() }));
vi.mock("@/stores/workflows", () => ({ loadWorkflows: vi.fn() }));
vi.mock("@/lib/vscodeExtensionActivation", () => ({ activateOnWorkspaceContains: vi.fn() }));
vi.mock("@/lib/notifications", () => ({ notifyError: vi.fn(), notifySuccess: vi.fn(), notifyWarning: vi.fn() }));
vi.mock("@/components/layout/TerminalPanel.vue", () => ({ default: { template: "<div />" } }));

import { aiWindowState } from "@/stores/aiWindow";
import { appState } from "@/stores/app";
import { aiState } from "@/stores/ai";
import {
  AI_CONVERSATION_TARGET_ACK_KEY,
  AI_CONVERSATION_TARGET_KEY,
  aiAssistantState,
} from "@/stores/aiAssistant";
import { agentState } from "@/stores/agent";
import { notifyError } from "@/lib/notifications";
import AiWindowView from "./AiWindowView.vue";

enableAutoUnmount(afterEach);

const sidebarStub = {
  props: ["activeView", "width", "terminalVisible"],
  emits: ["select-view", "select-conversation", "toggle-terminal", "resize", "resize-commit"],
  template: `<aside data-test="workspace-sidebar">
    <button data-test="open-settings" @click="$emit('select-view', 'settings')" />
    <button data-test="toggle-terminal" @click="$emit('toggle-terminal')" />
  </aside>`,
};

const dockStub = {
  props: ["visible", "width", "maxWidth"],
  emits: ["close", "resize", "resize-commit"],
  template: '<aside v-if="visible" data-test="terminal-dock" />',
};

function conversationTarget(
  conversationId: string | null,
  mode: "chat" | "plan" | "goal" | "agent",
  sequence: number,
) {
  return {
    protocol: 1 as const,
    target: "ai-window" as const,
    conversationId,
    mode,
    requestId: `request_${sequence}`,
    sourceOrigin: "source_main_window",
    sourceEpoch: "source_epoch_1",
    recipientEpoch: null,
    sequence,
    createdAt: Date.now(),
  };
}

describe("AiWindowView workspace shell", () => {
  beforeEach(() => {
    (
      globalThis as typeof globalThis & {
        __koyoriIdeSettingsServiceTestBinding?: typeof settingsServiceMock;
      }
    ).__koyoriIdeSettingsServiceTestBinding = settingsServiceMock;
    aiWindowState.activeView = "assistant";
    aiWindowState.terminalVisible = false;
    appState.currentProject = "";
    appState.projectName = "";
    projectServiceMock.getRecentProjects.mockReset().mockResolvedValue([]);
    projectServiceMock.addProject.mockReset();
    projectServiceMock.getWorkspaceSnapshot.mockReset();
    conversationServiceMock.updateTitle.mockReset();
    conversationServiceMock.load.mockReset();
    for (const name of Object.keys(crossWindowHandlers)) delete crossWindowHandlers[name];
    localStorage.clear();
    aiState.messages = [];
    aiState.streaming = false;
    aiState.globalStreamBusy = false;
    aiState.currentConversationId = null;
    aiState.currentConversationTitle = null;
    aiAssistantState.mode = "chat";
    aiAssistantState.activeConversationId = null;
    agentState.mode = "chat";
    vi.mocked(notifyError).mockReset();
  });

  afterEach(() => {
    delete (
      globalThis as typeof globalThis & {
        __koyoriIdeSettingsServiceTestBinding?: typeof settingsServiceMock;
      }
    ).__koyoriIdeSettingsServiceTestBinding;
  });

  it("keeps the sidebar mounted while views change and docks terminal on the right", async () => {
    const wrapper = mount(AiWindowView, {
      global: {
        stubs: {
          AiWorkspaceSidebar: sidebarStub,
          AiTerminalDock: dockStub,
          MessageList: true,
          InputComposer: true,
          AiSkillsView: true,
          AiAutomationView: true,
          AiSettingsView: true,
          SnapshotTimeline: true,
          "el-icon": { template: "<span><slot /></span>" },
        },
      },
    });
    await flushPromises();

    expect(wrapper.find('[data-test="workspace-sidebar"]').exists()).toBe(true);
    await wrapper.get('[data-test="open-settings"]').trigger("click");
    expect(aiWindowState.activeView).toBe("settings");
    expect(wrapper.find('[data-test="workspace-sidebar"]').exists()).toBe(true);

    await wrapper.get('[data-test="toggle-terminal"]').trigger("click");
    expect(wrapper.find('[data-test="terminal-dock"]').exists()).toBe(true);
    expect(aiWindowState.activeView).toBe("settings");
  });

  it("keeps the AI theme independent when editor theme state changes", async () => {
    appState.aiWindowTheme = "claude-dark";
    aiWindowState.theme = "claude-dark";
    const wrapper = mount(AiWindowView, {
      global: {
        stubs: {
          AiWorkspaceSidebar: sidebarStub,
          AiTerminalDock: dockStub,
          MessageList: true,
          InputComposer: true,
          AiSkillsView: true,
          AiAutomationView: true,
          AiSettingsView: true,
          SnapshotTimeline: true,
          "el-icon": { template: "<span><slot /></span>" },
        },
      },
    });
    await flushPromises();
    appState.theme = "light";
    await flushPromises();

    expect(document.documentElement.getAttribute("data-mode")).toBe("dark");
    expect(document.documentElement.getAttribute("data-design-language")).toBe("claude");
    wrapper.unmount();
  });

  it("labels icon and toolbar buttons for assistive technology", async () => {
    const wrapper = mount(AiWindowView, {
      global: {
        stubs: {
          AiWorkspaceSidebar: sidebarStub,
          AiTerminalDock: dockStub,
          MessageList: true,
          InputComposer: true,
          AiSkillsView: true,
          AiAutomationView: true,
          AiSettingsView: true,
          SnapshotTimeline: true,
          "el-icon": { template: "<span><slot /></span>" },
        },
      },
    });
    await flushPromises();

    expect(wrapper.get('button[aria-label="aiWindow.actExplorer"]').attributes("aria-label")).toBe("aiWindow.actExplorer");
    expect(wrapper.get('button[aria-label="aiWindow.actVSCode"]').attributes("aria-label")).toBe("aiWindow.actVSCode");
    expect(wrapper.get('button[aria-label="aiWindow.alwaysOnTop"]').attributes("aria-label")).toBe("aiWindow.alwaysOnTop");
    expect(wrapper.get('button[aria-label="aiAssistant.attach"]').text()).toContain("📎");
    wrapper.unmount();
  });

  it("loads the staged conversation and Agent mode on first desktop-window mount", async () => {
    conversationServiceMock.load.mockResolvedValue({
      id: "conv-staged",
      title: "staged conversation",
      created_at: 1,
      updated_at: 2,
      revision: 1,
      messages: [{ role: "assistant", content: "already visible" }],
    });
    localStorage.setItem(
      AI_CONVERSATION_TARGET_KEY,
      JSON.stringify(conversationTarget("conv-staged", "agent", 1)),
    );

    const wrapper = mount(AiWindowView, {
      global: {
        stubs: {
          AiWorkspaceSidebar: sidebarStub,
          AiTerminalDock: dockStub,
          MessageList: true,
          InputComposer: true,
          AiSkillsView: true,
          AiAutomationView: true,
          AiSettingsView: true,
          SnapshotTimeline: true,
          "el-icon": { template: "<span><slot /></span>" },
        },
      },
    });
    await flushPromises();

    expect(conversationServiceMock.load).toHaveBeenCalledWith("conv-staged");
    expect(aiState.currentConversationId).toBe("conv-staged");
    expect(aiState.messages.map((message) => message.content)).toEqual(["already visible"]);
    expect(aiAssistantState.activeConversationId).toBe("conv-staged");
    expect(aiAssistantState.mode).toBe("agent");
    expect(agentState.mode).toBe("agent");
    expect(localStorage.getItem(AI_CONVERSATION_TARGET_KEY)).toBeNull();
    wrapper.unmount();
  });

  it("keeps the newest desktop target when older conversation loads resolve late", async () => {
    const resolvers = new Map<string, (conversation: {
      id: string;
      title: string;
      created_at: number;
      updated_at: number;
      revision: number;
      messages: Array<{ role: string; content: string }>;
    }) => void>();
    conversationServiceMock.load.mockImplementation(
      (id: string) => new Promise((resolve) => resolvers.set(id, resolve)),
    );
    const wrapper = mount(AiWindowView, {
      global: {
        stubs: {
          AiWorkspaceSidebar: sidebarStub,
          AiTerminalDock: dockStub,
          MessageList: true,
          InputComposer: true,
          AiSkillsView: true,
          AiAutomationView: true,
          AiSettingsView: true,
          SnapshotTimeline: true,
          "el-icon": { template: "<span><slot /></span>" },
        },
      },
    });
    await flushPromises();
    const createdAt = Date.now();
    crossWindowHandlers["ai:open-conversation"]?.({
      data: { ...conversationTarget("conv-old", "chat", 10), createdAt },
    });
    crossWindowHandlers["ai:open-conversation"]?.({
      data: { ...conversationTarget("conv-new", "agent", 11), createdAt },
    });
    await flushPromises();

    resolvers.get("conv-new")?.({
      id: "conv-new",
      title: "new",
      created_at: 2,
      updated_at: 2,
      revision: 1,
      messages: [{ role: "assistant", content: "new" }],
    });
    await flushPromises();
    resolvers.get("conv-old")?.({
      id: "conv-old",
      title: "old",
      created_at: 1,
      updated_at: 1,
      revision: 1,
      messages: [{ role: "assistant", content: "old" }],
    });
    await flushPromises();

    expect(aiState.currentConversationId).toBe("conv-new");
    expect(aiState.messages.map((message) => message.content)).toEqual(["new"]);
    expect(aiAssistantState.mode).toBe("agent");
    crossWindowHandlers["ai:open-conversation"]?.({
      data: { ...conversationTarget("conv-old", "chat", 10), createdAt },
    });
    await flushPromises();
    expect(conversationServiceMock.load).toHaveBeenCalledTimes(2);
    wrapper.unmount();
  });

  it("defers a conversation handoff until the process-wide stream is idle", async () => {
    conversationServiceMock.load.mockResolvedValue({
      id: "conv-after-stream",
      title: "after stream",
      created_at: 1,
      updated_at: 2,
      revision: 1,
      messages: [{ role: "assistant", content: "complete" }],
    });
    aiState.globalStreamBusy = true;
    const wrapper = mount(AiWindowView, {
      global: {
        stubs: {
          AiWorkspaceSidebar: sidebarStub,
          AiTerminalDock: dockStub,
          MessageList: true,
          InputComposer: true,
          AiSkillsView: true,
          AiAutomationView: true,
          AiSettingsView: true,
          SnapshotTimeline: true,
          "el-icon": { template: "<span><slot /></span>" },
        },
      },
    });
    await flushPromises();
    crossWindowHandlers["ai:open-conversation"]?.({
      data: {
        ...conversationTarget("conv-after-stream", "agent", 20),
      },
    });
    await flushPromises();
    expect(conversationServiceMock.load).not.toHaveBeenCalled();

    aiState.globalStreamBusy = false;
    await flushPromises();
    expect(conversationServiceMock.load).toHaveBeenCalledWith("conv-after-stream");
    expect(aiState.currentConversationId).toBe("conv-after-stream");
    wrapper.unmount();
  });

  it("keeps a failed target durable and accepts the same request on retry", async () => {
    const target = conversationTarget("conv-retry", "agent", 30);
    localStorage.setItem(AI_CONVERSATION_TARGET_KEY, JSON.stringify(target));
    conversationServiceMock.load
      .mockRejectedValueOnce(new Error("temporary load failure"))
      .mockResolvedValueOnce({
        id: "conv-retry",
        title: "retry succeeded",
        created_at: 1,
        updated_at: 2,
        revision: 1,
        messages: [{ role: "assistant", content: "visible after retry" }],
      });
    const wrapper = mount(AiWindowView, {
      global: {
        stubs: {
          AiWorkspaceSidebar: sidebarStub,
          AiTerminalDock: dockStub,
          MessageList: true,
          InputComposer: true,
          AiSkillsView: true,
          AiAutomationView: true,
          AiSettingsView: true,
          SnapshotTimeline: true,
          "el-icon": { template: "<span><slot /></span>" },
        },
      },
    });
    await flushPromises();

    expect(aiState.currentConversationId).not.toBe("conv-retry");
    expect(localStorage.getItem(AI_CONVERSATION_TARGET_KEY)).not.toBeNull();
    crossWindowHandlers["ai:open-conversation"]?.({ data: target });
    await flushPromises();

    expect(conversationServiceMock.load).toHaveBeenCalledTimes(2);
    expect(aiState.currentConversationId).toBe("conv-retry");
    expect(localStorage.getItem(AI_CONVERSATION_TARGET_KEY)).toBeNull();
    wrapper.unmount();
  });

  it("re-acknowledges an already committed target when the sender retries it", async () => {
    const target = conversationTarget("conv-duplicate-ack", "agent", 32);
    conversationServiceMock.load.mockResolvedValue({
      id: "conv-duplicate-ack",
      title: "loaded",
      created_at: 1,
      updated_at: 2,
      revision: 1,
      messages: [{ role: "assistant", content: "loaded" }],
    });
    const wrapper = mount(AiWindowView, {
      global: {
        stubs: {
          AiWorkspaceSidebar: sidebarStub,
          AiTerminalDock: dockStub,
          MessageList: true,
          InputComposer: true,
          AiSkillsView: true,
          AiAutomationView: true,
          AiSettingsView: true,
          SnapshotTimeline: true,
          "el-icon": { template: "<span><slot /></span>" },
        },
      },
    });
    await flushPromises();

    crossWindowHandlers["ai:open-conversation"]?.({ data: target });
    await flushPromises();
    expect(localStorage.getItem(AI_CONVERSATION_TARGET_ACK_KEY)).toContain(target.requestId);

    localStorage.removeItem(AI_CONVERSATION_TARGET_ACK_KEY);
    localStorage.setItem(AI_CONVERSATION_TARGET_KEY, JSON.stringify(target));
    crossWindowHandlers["ai:open-conversation"]?.({ data: target });
    await flushPromises();

    expect(conversationServiceMock.load).toHaveBeenCalledTimes(1);
    expect(localStorage.getItem(AI_CONVERSATION_TARGET_ACK_KEY)).toContain(target.requestId);
    expect(localStorage.getItem(AI_CONVERSATION_TARGET_KEY)).toBeNull();
    wrapper.unmount();
  });

  it("does not acknowledge a same-id target when its backend reload fails", async () => {
    aiState.currentConversationId = "conv-same";
    aiState.currentConversationTitle = "stale title";
    aiState.conversationRevision = 3;
    aiState.messages = [{
      id: "stale-message",
      role: "assistant",
      content: "stale content",
    }];
    const target = conversationTarget("conv-same", "agent", 31);
    localStorage.setItem(AI_CONVERSATION_TARGET_KEY, JSON.stringify(target));
    conversationServiceMock.load
      .mockRejectedValueOnce(new Error("same-id reload failed"))
      .mockResolvedValueOnce({
        id: "conv-same",
        title: "fresh title",
        created_at: 1,
        updated_at: 2,
        revision: 4,
        messages: [{ role: "assistant", content: "fresh content" }],
      });
    const wrapper = mount(AiWindowView, {
      global: {
        stubs: {
          AiWorkspaceSidebar: sidebarStub,
          AiTerminalDock: dockStub,
          MessageList: true,
          InputComposer: true,
          AiSkillsView: true,
          AiAutomationView: true,
          AiSettingsView: true,
          SnapshotTimeline: true,
          "el-icon": { template: "<span><slot /></span>" },
        },
      },
    });
    await flushPromises();

    expect(aiState.messages.map((message) => message.content)).toEqual(["stale content"]);
    expect(localStorage.getItem(AI_CONVERSATION_TARGET_KEY)).not.toBeNull();
    expect(localStorage.getItem(AI_CONVERSATION_TARGET_ACK_KEY)).toBeNull();

    crossWindowHandlers["ai:open-conversation"]?.({ data: target });
    await flushPromises();

    expect(conversationServiceMock.load).toHaveBeenCalledTimes(2);
    expect(aiState.messages.map((message) => message.content)).toEqual(["fresh content"]);
    expect(aiState.conversationRevision).toBe(4);
    expect(localStorage.getItem(AI_CONVERSATION_TARGET_KEY)).toBeNull();
    expect(localStorage.getItem(AI_CONVERSATION_TARGET_ACK_KEY)).not.toBeNull();
    wrapper.unmount();
  });

  it("retires an old source epoch after a newer epoch is committed", async () => {
    conversationServiceMock.load.mockImplementation(async (id: string) => ({
      id,
      title: id,
      created_at: 1,
      updated_at: 2,
      revision: 1,
      messages: [{ role: "assistant", content: id }],
    }));
    const wrapper = mount(AiWindowView, {
      global: {
        stubs: {
          AiWorkspaceSidebar: sidebarStub,
          AiTerminalDock: dockStub,
          MessageList: true,
          InputComposer: true,
          AiSkillsView: true,
          AiAutomationView: true,
          AiSettingsView: true,
          SnapshotTimeline: true,
          "el-icon": { template: "<span><slot /></span>" },
        },
      },
    });
    await flushPromises();
    const oldEpoch = conversationTarget("conv-old-epoch", "chat", 1);
    crossWindowHandlers["ai:open-conversation"]?.({ data: oldEpoch });
    await flushPromises();
    const newEpoch = {
      ...conversationTarget("conv-new-epoch", "agent", 1),
      requestId: "request_new_epoch",
      sourceEpoch: "source_epoch_2",
      createdAt: oldEpoch.createdAt + 1,
    };
    crossWindowHandlers["ai:open-conversation"]?.({ data: newEpoch });
    await flushPromises();
    crossWindowHandlers["ai:open-conversation"]?.({
      data: { ...oldEpoch, requestId: "request_old_retry", sequence: 2 },
    });
    await flushPromises();

    expect(aiState.currentConversationId).toBe("conv-new-epoch");
    expect(conversationServiceMock.load).toHaveBeenCalledTimes(2);
    wrapper.unmount();
  });

  it("polls the durable handoff when live cross-window delivery is unavailable", async () => {
    vi.useFakeTimers();
    try {
      conversationServiceMock.load.mockResolvedValue({
        id: "conv-polled",
        title: "polled",
        created_at: 1,
        updated_at: 2,
        revision: 1,
        messages: [{ role: "assistant", content: "loaded without event" }],
      });
      const wrapper = mount(AiWindowView, {
        global: {
          stubs: {
            AiWorkspaceSidebar: sidebarStub,
            AiTerminalDock: dockStub,
            MessageList: true,
            InputComposer: true,
            AiSkillsView: true,
            AiAutomationView: true,
            AiSettingsView: true,
            SnapshotTimeline: true,
            "el-icon": { template: "<span><slot /></span>" },
          },
        },
      });
      await flushPromises();
      localStorage.setItem(
        AI_CONVERSATION_TARGET_KEY,
        JSON.stringify(conversationTarget("conv-polled", "agent", 40)),
      );

      await vi.advanceTimersByTimeAsync(251);
      await flushPromises();

      expect(aiState.currentConversationId).toBe("conv-polled");
      expect(aiState.messages.map((message) => message.content)).toEqual([
        "loaded without event",
      ]);
      expect(localStorage.getItem(AI_CONVERSATION_TARGET_KEY)).toBeNull();
      wrapper.unmount();
    } finally {
      vi.useRealTimers();
    }
  });

  it("commits AI-window workspace switches through ProjectService before publishing renderer state", async () => {
	const nextProject = {
	  id: "next",
	  name: "next-project",
	  path: "C:/work/next-project",
	  createdAt: 1,
	  lastOpened: 2,
	  exists: true,
	};
	let resolveCommit!: (project: typeof nextProject) => void;
	projectServiceMock.getRecentProjects.mockResolvedValue([nextProject]);
	projectServiceMock.addProject.mockReturnValue(new Promise((resolve) => {
	  resolveCommit = resolve;
	}));
	projectServiceMock.getWorkspaceSnapshot.mockResolvedValue({
	  root: nextProject.path,
	  roots: [nextProject.path],
	  generation: 1,
	  projectId: nextProject.id,
	  projectName: nextProject.name,
	  projectPath: nextProject.path,
	});
	appState.currentProject = "C:/work/current";
	appState.projectName = "current";

	const wrapper = mount(AiWindowView, {
	  global: {
	    stubs: {
	      AiWorkspaceSidebar: sidebarStub,
	      AiTerminalDock: dockStub,
	      MessageList: true,
	      InputComposer: true,
	      AiSkillsView: true,
	      AiAutomationView: true,
	      AiSettingsView: true,
	      SnapshotTimeline: true,
	      "el-icon": { template: "<span><slot /></span>" },
	    },
	  },
	});
	await flushPromises();

	const workspaceButton = wrapper.get('button[aria-label="aiWindow.workspaceMenuAria"]');
	await workspaceButton.trigger("click");
	await wrapper.get('[role="menuitemradio"]').trigger("click");
	await flushPromises();

	expect(projectServiceMock.addProject).toHaveBeenCalledWith(nextProject.path);
	expect(appState.currentProject).toBe("C:/work/current");
	expect(appState.projectName).toBe("current");
	expect(workspaceButton.attributes("disabled")).toBeDefined();

	resolveCommit(nextProject);
	await flushPromises();
	expect(appState.currentProject).toBe(nextProject.path);
	expect(appState.projectName).toBe(nextProject.name);
	expect(workspaceButton.attributes("disabled")).toBeUndefined();
	wrapper.unmount();
  });

  it("keeps the previous workspace and exits switching state when backend coordination fails", async () => {
    const nextProject = {
      id: "next",
      name: "next-project",
      path: "C:/work/next-project",
      createdAt: 1,
      lastOpened: 2,
      exists: true,
    };
    projectServiceMock.getRecentProjects.mockResolvedValue([nextProject]);
    projectServiceMock.addProject.mockRejectedValue(new Error("workspace switch rejected"));
    appState.currentProject = "C:/work/current";
    appState.projectName = "current";

    const wrapper = mount(AiWindowView, {
      global: {
        stubs: {
          AiWorkspaceSidebar: sidebarStub,
          AiTerminalDock: dockStub,
          MessageList: true,
          InputComposer: true,
          AiSkillsView: true,
          AiAutomationView: true,
          AiSettingsView: true,
          SnapshotTimeline: true,
          "el-icon": { template: "<span><slot /></span>" },
        },
      },
    });
    await flushPromises();

    const workspaceButton = wrapper.get('button[aria-label="aiWindow.workspaceMenuAria"]');
    await workspaceButton.trigger("click");
    await wrapper.get('[role="menuitemradio"]').trigger("click");
    await flushPromises();

    expect(appState.currentProject).toBe("C:/work/current");
    expect(appState.projectName).toBe("current");
    expect(vi.mocked(notifyError)).toHaveBeenCalledWith("projects.openProjectFailed");
    expect(workspaceButton.attributes("disabled")).toBeUndefined();
    wrapper.unmount();
  });
});
