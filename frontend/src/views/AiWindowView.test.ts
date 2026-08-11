import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const { projectServiceMock, settingsServiceMock } = vi.hoisted(() => ({
	projectServiceMock: {
		getRecentProjects: vi.fn(),
		addProject: vi.fn(),
		getWorkspaceSnapshot: vi.fn(),
	},
  settingsServiceMock: {
    loadSettings: vi.fn(),
    saveSettings: vi.fn(),
  },
}));

vi.mock("@wailsio/runtime", () => ({
  Events: { On: vi.fn(), Emit: vi.fn() },
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
  conversationService: { updateTitle: vi.fn() },
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
import { notifyError } from "@/lib/notifications";
import AiWindowView from "./AiWindowView.vue";

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
