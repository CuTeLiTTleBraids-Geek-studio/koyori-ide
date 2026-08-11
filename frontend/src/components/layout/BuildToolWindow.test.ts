import { mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  appState: { currentProject: "C:/repo" as string | null },
  state: {
    workspaceRoot: "C:/repo",
    tasks: [] as Array<Record<string, unknown>>,
    loading: false,
    errorMessage: null as string | null,
    favorites: [] as string[],
    recent: [] as string[],
    runs: {} as Record<string, Record<string, unknown>>,
    activeTaskId: null as string | null,
    selectedTaskId: null as string | null,
  },
  refresh: vi.fn(),
  run: vi.fn(),
  stop: vi.fn(),
  rerun: vi.fn(),
  toggleFavorite: vi.fn(),
  select: vi.fn(),
}));

vi.mock("@/stores/app", () => ({ appState: mocks.appState }));
vi.mock("@/stores/buildTool", () => ({
  buildToolState: mocks.state,
  refreshBuildTasks: mocks.refresh,
  runBuildTask: mocks.run,
  stopBuildTask: mocks.stop,
  rerunBuildTask: mocks.rerun,
  toggleBuildFavorite: mocks.toggleFavorite,
  selectBuildTask: mocks.select,
}));
vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({ t: (key: string) => key }),
}));

import BuildToolWindow from "./BuildToolWindow.vue";

function task(id: string, source: string, label: string) {
  return { id, source, label, description: `${label} description`, task: { label, command: "go" } };
}

describe("BuildToolWindow", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.appState.currentProject = "C:/repo";
    mocks.state.tasks = [
      task("task:build", "task", "VS Code Build"),
      task("launch:api", "launch", "Launch API"),
      task("toolchain:go-test-race", "toolchain", "Go Race"),
      task("npm:dev", "npm", "npm: dev"),
      task("make:release", "make", "make: release"),
      task("taskfile:verify", "taskfile", "task: verify"),
    ];
    mocks.state.favorites = ["task:build"];
    mocks.state.recent = ["npm:dev"];
    mocks.state.runs = {
      "task:build": {
        taskId: "task:build",
        status: "success",
        output: "build complete",
        durationMs: 1250,
        startedAt: 0,
        sessionId: null,
      },
    };
    mocks.state.activeTaskId = null;
    mocks.state.selectedTaskId = "task:build";
    mocks.state.loading = false;
    mocks.state.errorMessage = null;
  });

  it("refreshes the workspace and renders favorite, recent, and source groups", () => {
    const wrapper = mount(BuildToolWindow, {
      global: { stubs: { "el-icon": true } },
    });

    expect(mocks.refresh).toHaveBeenCalledWith("C:/repo");
    for (const group of ["favorites", "recent", "task", "launch", "toolchain", "npm", "make", "taskfile"]) {
      expect(wrapper.find(`[data-group="${group}"]`).exists()).toBe(true);
    }
    expect(wrapper.get('[data-test="build-output"]').text()).toContain("build complete");
    expect(wrapper.get('[data-task="task:build"]').text()).toContain("1.25s");
  });

  it("delegates run, rerun, select, and favorite actions", async () => {
    const wrapper = mount(BuildToolWindow, {
      global: { stubs: { "el-icon": true } },
    });

    await wrapper.get('[data-run="npm:dev"]').trigger("click");
    await wrapper.get('[data-favorite="npm:dev"]').trigger("click");
    await wrapper.get('[data-task="npm:dev"]').trigger("click");
    await wrapper.get('[data-test="build-rerun"]').trigger("click");

    expect(mocks.run).toHaveBeenCalledWith("npm:dev");
    expect(mocks.toggleFavorite).toHaveBeenCalledWith("npm:dev");
    expect(mocks.select).toHaveBeenCalledWith("npm:dev");
    expect(mocks.rerun).toHaveBeenCalled();
  });

  it("offers stop for the active run", async () => {
    mocks.state.activeTaskId = "toolchain:go-test-race";
    mocks.state.selectedTaskId = "toolchain:go-test-race";
    mocks.state.runs = {
      "toolchain:go-test-race": {
        taskId: "toolchain:go-test-race",
        status: "running",
        output: "testing...",
        durationMs: 0,
        startedAt: 0,
        sessionId: null,
      },
    };
    const wrapper = mount(BuildToolWindow, {
      global: { stubs: { "el-icon": true } },
    });

    await wrapper.get('[data-test="build-stop"]').trigger("click");
    expect(mocks.stop).toHaveBeenCalled();
  });

  it("keeps row actions separate from the task selection control", () => {
    const wrapper = mount(BuildToolWindow, {
      global: { stubs: { "el-icon": true } },
    });
    const taskButton = wrapper.get('[data-task="npm:dev"]');
    const runButton = wrapper.get('[data-run="npm:dev"]');
    expect(taskButton.element.contains(runButton.element)).toBe(false);

    const space = new KeyboardEvent("keydown", {
      key: " ",
      bubbles: true,
      cancelable: true,
    });
    runButton.element.dispatchEvent(space);
    expect(space.defaultPrevented).toBe(false);
    expect(mocks.select).not.toHaveBeenCalled();
  });
});
