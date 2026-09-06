import { beforeEach, describe, expect, it, vi } from "vitest";

const { projectServiceMock, fileServiceMock } = vi.hoisted(() => ({
  projectServiceMock: {
    getWorkspaceSnapshot: vi.fn(),
  },
  fileServiceMock: {
    readFile: vi.fn(),
  },
}));

vi.mock("@/api/services", () => ({
  projectService: projectServiceMock,
  fileService: fileServiceMock,
}));
vi.mock("@/stores/snapshot", () => ({ setSnapshotWorkspaceRoot: vi.fn() }));
vi.mock("@/stores/workflows", () => ({
  cleanupWorkflowRuntime: vi.fn(),
  loadWorkflows: vi.fn(),
}));
vi.mock("@/stores/tasks", () => ({
  cleanupTaskStoreTimers: vi.fn(),
  loadTasks: vi.fn(),
}));
vi.mock("@/lib/vscodeExtensionActivation", () => ({
  activateOnWorkspaceContains: vi.fn(),
}));

import { appState } from "@/stores/app";
import {
  applyWorkspaceSnapshot,
  handleWorkspaceChangedEvent,
  loadWorkspaceFolders,
  parseCodeWorkspaceContent,
  resetWorkspaceAuthorityForTesting,
  syncWorkspaceSnapshot,
} from "./workspaceStore";

describe("code-workspace UNC paths", () => {
  it("preserves a Windows UNC path root", () => {
    expect(
      parseCodeWorkspaceContent(
        JSON.stringify({ folders: [{ path: "\\\\server\\share\\repo" }] }),
        "C:/workspace",
      ),
    ).toEqual(["//server/share/repo"]);
  });

  it("preserves the authority of a file://server/share URI", () => {
    expect(
      parseCodeWorkspaceContent(
        JSON.stringify({ folders: [{ uri: "file://server/share/repo" }] }),
        "C:/workspace",
      ),
    ).toEqual(["//server/share/repo"]);
  });

  it("resolves a relative folder against a UNC workspace directory", () => {
    expect(
      parseCodeWorkspaceContent(
        JSON.stringify({ folders: [{ path: "packages\\\\client" }] }),
        "\\\\server\\share\\workspace",
      ),
    ).toEqual(["//server/share/workspace/packages/client"]);
  });

  it("keeps a local drive URI local and decodes URI path segments", () => {
    expect(
      parseCodeWorkspaceContent(
        JSON.stringify({
          folders: [
            { uri: "file://localhost/C:/Users/dev/My%20Repo" },
            { uri: "file:///C:/Users/dev/My%20Repo" },
          ],
        }),
        "C:/workspace",
      ),
    ).toEqual(["C:/Users/dev/My Repo"]);
  });

  it("accepts case-insensitive file URI schemes and de-duplicates Windows roots", () => {
    expect(
      parseCodeWorkspaceContent(
        JSON.stringify({
          folders: [
            { path: "\\\\SERVER\\SHARE\\Repo" },
            { uri: "FILE://server/share/repo" },
          ],
        }),
        "C:/workspace",
      ),
    ).toEqual(["//SERVER/SHARE/Repo"]);
  });

  it("canonicalizes dot segments without allowing an absolute root escape", () => {
    expect(
      parseCodeWorkspaceContent(
        JSON.stringify({ folders: [{ path: "packages/../client" }] }),
        "/home/user/workspace",
      ),
    ).toEqual(["/home/user/workspace/client"]);
    expect(() =>
      parseCodeWorkspaceContent(
        JSON.stringify({ folders: [{ path: "../../outside" }] }),
        "/workspace",
      ),
    ).toThrow(/escapes workspace root/);
    expect(() =>
      parseCodeWorkspaceContent(
        JSON.stringify({
          folders: [{ path: "\\\\server\\share\\..\\outside" }],
        }),
        "/workspace",
      ),
    ).toThrow(/escapes workspace root/);
  });

  it("keeps POSIX roots canonical when input has extra leading separators", () => {
    expect(
      parseCodeWorkspaceContent(
        JSON.stringify({ folders: [{ path: "///tmp/project" }] }),
        "/workspace",
      ),
    ).toEqual(["/tmp/project"]);
  });

  it("rejects encoded separators, NULs, malformed URI escapes, and device paths", () => {
    for (const uri of [
      "file:///tmp/a%2Fb",
      "file:///tmp/a%5Cb",
      "file:///tmp/a%00b",
      "file:///tmp/a%ZZ",
      "file://server/share/repo?query=1",
    ]) {
      expect(
        () =>
          parseCodeWorkspaceContent(
            JSON.stringify({ folders: [{ uri }] }),
            "/workspace",
          ),
        uri,
      ).toThrow();
    }
    expect(() =>
      parseCodeWorkspaceContent(
        JSON.stringify({ folders: [{ path: "\\\\?\\C:\\repo" }] }),
        "/workspace",
      ),
    ).toThrow(/device path/);
    expect(() =>
      parseCodeWorkspaceContent(
        JSON.stringify({ folders: [{ path: "C:relative" }] }),
        "C:/workspace",
      ),
    ).toThrow(/drive-relative/);
  });

  it("rejects incomplete UNC authorities", () => {
    expect(() =>
      parseCodeWorkspaceContent(
        JSON.stringify({ folders: [{ path: "//server" }] }),
        "/workspace",
      ),
    ).toThrow(/UNC/);
  });

  it("uses the filesystem root as baseDir for a root-level workspace file", async () => {
    fileServiceMock.readFile.mockResolvedValue(
      JSON.stringify({ folders: [{ path: "child" }] }),
    );
    await expect(
      loadWorkspaceFolders("/workspace.code-workspace"),
    ).resolves.toEqual(["/child"]);
    expect(fileServiceMock.readFile).toHaveBeenCalledWith(
      "/workspace.code-workspace",
    );
  });
});

function snapshot(
  overrides: Partial<Parameters<typeof applyWorkspaceSnapshot>[0]> = {},
) {
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
    expect(
      applyWorkspaceSnapshot(
        snapshot({
          root: "C:/work/two",
          roots: ["C:/work/two", "C:/work/two/packages"],
          generation: 2,
          projectId: "two-id",
          projectName: "two",
          projectPath: "C:/work/two.code-workspace",
        }),
      ),
    ).toBe(true);

    expect(appState.workspaceGeneration).toBe(2);
    expect(appState.workspaceRoot).toBe("C:/work/two");
    expect(appState.workspaceFolders).toEqual([
      "C:/work/two",
      "C:/work/two/packages",
    ]);
    expect(appState.currentProject).toBe("C:/work/two.code-workspace");
    expect(appState.projectName).toBe("two");
  });

  it("drops stale snapshots without mutating the committed workspace", () => {
    applyWorkspaceSnapshot(snapshot({ generation: 4 }));
    expect(
      applyWorkspaceSnapshot(
        snapshot({
          root: "C:/work/stale",
          roots: ["C:/work/stale"],
          generation: 3,
          projectName: "stale",
          projectPath: "C:/work/stale",
        }),
      ),
    ).toBe(false);

    expect(appState.workspaceGeneration).toBe(4);
    expect(appState.workspaceRoot).toBe("C:/work/one");
    expect(appState.currentProject).toBe("C:/work/one");
  });

  it("rejects conflicting snapshots from the same generation", () => {
    applyWorkspaceSnapshot(snapshot({ generation: 7 }));
    expect(
      applyWorkspaceSnapshot(
        snapshot({
          root: "C:/work/conflict",
          roots: ["C:/work/conflict"],
          generation: 7,
          projectName: "conflict",
          projectPath: "C:/work/conflict",
        }),
      ),
    ).toBe(false);

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
