import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  events: [] as string[],
  fileService: { readFile: vi.fn() },
  lspService: {
    stopServer: vi.fn(),
    startServer: vi.fn(),
  },
  refreshGoTarget: vi.fn(),
  notifySuccess: vi.fn(),
  notifyError: vi.fn(),
  pushOutput: vi.fn(),
}));

vi.mock("@/api/services", () => ({
  fileService: mocks.fileService,
  lspService: mocks.lspService,
}));
vi.mock("@/stores/app", () => ({ appState: { currentProject: "/workspace" } }));
vi.mock("@/stores/goTarget", () => ({ refreshGoTarget: mocks.refreshGoTarget }));
vi.mock("@/lib/notifications", () => ({
  notifySuccess: mocks.notifySuccess,
  notifyError: mocks.notifyError,
}));
vi.mock("@/stores/output", () => ({ pushOutput: mocks.pushOutput }));

import { setActiveWorkspaceRoot, workspaceModulesState } from "./workspaceModules";

function record<T extends unknown[]>(name: string) {
  return (...args: T) => {
    mocks.events.push(`${name}:${args.join(":")}`);
    return Promise.resolve();
  };
}

describe("workspaceModules multi-root switching", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.events.length = 0;
    workspaceModulesState.activeRoot = "";
    mocks.refreshGoTarget.mockResolvedValue(undefined);
    mocks.lspService.stopServer.mockImplementation(record("stop"));
    mocks.lspService.startServer.mockImplementation(record("start"));
  });

  it("sets the analysis root before restarting supported servers", async () => {
    await setActiveWorkspaceRoot("/workspace/packages/web/");

    expect(workspaceModulesState.activeRoot).toBe("/workspace/packages/web");
    expect(mocks.events).toEqual([
      "stop:go",
      "stop:typescript",
      "stop:javascript",
      "start:go",
      "start:typescript",
    ]);
  });

  it("keeps the selected root and completes the ordered restart when individual LSP operations fail", async () => {
    mocks.lspService.stopServer.mockImplementation(async (language: string) => {
      mocks.events.push(`stop:${language}`);
      if (language === "typescript") throw new Error("stop failed");
    });
    mocks.lspService.startServer.mockImplementation(async (language: string) => {
      mocks.events.push(`start:${language}`);
      if (language === "go") throw new Error("start failed");
    });

    await expect(setActiveWorkspaceRoot("/workspace/packages/api")).resolves.toBeUndefined();

    expect(workspaceModulesState.activeRoot).toBe("/workspace/packages/api");
    expect(mocks.events).toEqual([
      "stop:go",
      "stop:typescript",
      "stop:javascript",
      "start:go",
      "start:typescript",
    ]);
    expect(mocks.notifySuccess).toHaveBeenCalledWith("Workspace root: /workspace/packages/api");
  });
});
