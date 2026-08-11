import { beforeEach, describe, expect, it, vi } from "vitest";

const { projectServiceMock } = vi.hoisted(() => ({
  projectServiceMock: {
    getWorkspaceSnapshot: vi.fn(),
  },
}));

vi.mock("@/api/services", () => ({ projectService: projectServiceMock }));
vi.mock("@/stores/snapshot", () => ({ setSnapshotWorkspaceRoot: vi.fn() }));
vi.mock("@/stores/workflows", () => ({ loadWorkflows: vi.fn() }));
vi.mock("@/lib/vscodeExtensionActivation", () => ({ activateOnWorkspaceContains: vi.fn() }));

import { appState } from "@/stores/app";
import {
  applyWorkspaceSnapshot,
  handleWorkspaceChangedEvent,
  resetWorkspaceAuthorityForTesting,
  syncWorkspaceSnapshot,
} from "./workspaceStore";

function snapshot(overrides: Partial<Parameters<typeof applyWorkspaceSnapshot>[0]> = {}) {
  return {
    root: "C:/work/one",
    roots: ["C:/work/one"],
    generation: 1,
    projectId: "one-id",
    projectName: "one",
    projectPath: "C:/work/one",
    ...overrides,
  };
}

describe("workspace authority snapshots", () => {
  beforeEach(() => {
    resetWorkspaceAuthorityForTesting();
    appState.currentProject = null;
    appState.projectName = null;
    appState.workspaceRoot = null;
    appState.workspaceFolders = [];
    appState.workspaceGeneration = 0;
    projectServiceMock.getWorkspaceSnapshot.mockReset();
  });

  it("applies a newer snapshot atomically", () => {
    expect(applyWorkspaceSnapshot(snapshot())).toBe(true);
    expect(applyWorkspaceSnapshot(snapshot({
      root: "C:/work/two",
      roots: ["C:/work/two", "C:/work/two/packages"],
      generation: 2,
      projectId: "two-id",
      projectName: "two",
      projectPath: "C:/work/two.code-workspace",
    }))).toBe(true);

    expect(appState.workspaceGeneration).toBe(2);
    expect(appState.workspaceRoot).toBe("C:/work/two");
    expect(appState.workspaceFolders).toEqual(["C:/work/two", "C:/work/two/packages"]);
    expect(appState.currentProject).toBe("C:/work/two.code-workspace");
    expect(appState.projectName).toBe("two");
  });

  it("drops stale snapshots without mutating the committed workspace", () => {
    applyWorkspaceSnapshot(snapshot({ generation: 4 }));
    expect(applyWorkspaceSnapshot(snapshot({
      root: "C:/work/stale",
      roots: ["C:/work/stale"],
      generation: 3,
      projectName: "stale",
      projectPath: "C:/work/stale",
    }))).toBe(false);

    expect(appState.workspaceGeneration).toBe(4);
    expect(appState.workspaceRoot).toBe("C:/work/one");
    expect(appState.currentProject).toBe("C:/work/one");
  });

  it("rejects conflicting snapshots from the same generation", () => {
    applyWorkspaceSnapshot(snapshot({ generation: 7 }));
    expect(applyWorkspaceSnapshot(snapshot({
      root: "C:/work/conflict",
      roots: ["C:/work/conflict"],
      generation: 7,
      projectName: "conflict",
      projectPath: "C:/work/conflict",
    }))).toBe(false);

    expect(appState.workspaceRoot).toBe("C:/work/one");
    expect(appState.workspaceGeneration).toBe(7);
    expect(appState.projectName).toBe("one");
  });

  it("synchronizes an initially empty workspace from the backend", async () => {
    projectServiceMock.getWorkspaceSnapshot.mockResolvedValue({
      root: "",
      roots: [],
      generation: 9,
      projectId: "",
      projectName: "",
      projectPath: "",
    });

    await expect(syncWorkspaceSnapshot()).resolves.toBe(true);
    expect(projectServiceMock.getWorkspaceSnapshot).toHaveBeenCalledOnce();
    expect(appState.workspaceGeneration).toBe(9);
    expect(appState.workspaceRoot).toBeNull();
    expect(appState.workspaceFolders).toEqual([]);
    expect(appState.currentProject).toBeNull();
    expect(appState.projectName).toBeNull();
  });

  it("accepts Wails event payloads with a newer generation", () => {
    handleWorkspaceChangedEvent({ data: snapshot({ generation: 6 }) });
    expect(appState.workspaceGeneration).toBe(6);
    expect(appState.workspaceRoot).toBe("C:/work/one");
  });
});
