import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  service: {
    listGoTargets: vi.fn(),
    getGoTarget: vi.fn(),
    setGoTarget: vi.fn(),
    resetGoTarget: vi.fn(),
  },
  notifyError: vi.fn(),
}));

vi.mock("@/api/services", () => ({ toolchainService: mocks.service }));
vi.mock("@/lib/notifications", () => ({ notifyError: mocks.notifyError }));

import { goTargetState, refreshGoTarget, restoreHostGoTarget, selectGoTarget } from "./goTarget";

describe("Go target store", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    goTargetState.targets = [];
    goTargetState.host = { goos: "", goarch: "" };
    goTargetState.current = { goos: "", goarch: "" };
    goTargetState.overridden = false;
    mocks.service.listGoTargets.mockResolvedValue([
      { goos: "windows", goarch: "amd64" },
      { goos: "linux", goarch: "arm64" },
    ]);
    mocks.service.resetGoTarget.mockResolvedValue({
      host: { goos: "windows", goarch: "amd64" },
      current: { goos: "windows", goarch: "amd64" },
      overridden: false,
    });
  });

  it("restores the persisted override only for the active workspace", async () => {
    localStorage.setItem(
      "koyori-ide.workspace.%2Fwork%2Falpha.goTarget",
      JSON.stringify({ goos: "linux", goarch: "arm64" }),
    );
    mocks.service.setGoTarget.mockResolvedValue({
      host: { goos: "windows", goarch: "amd64" },
      current: { goos: "linux", goarch: "arm64" },
      overridden: true,
    });

    await refreshGoTarget("/work/alpha");

    expect(mocks.service.setGoTarget).toHaveBeenCalledWith("linux", "arm64");
    expect(goTargetState.current).toEqual({ goos: "linux", goarch: "arm64" });
    expect(goTargetState.overridden).toBe(true);
  });

  it("persists selection and restoring host clears only that workspace", async () => {
    mocks.service.setGoTarget.mockResolvedValue({
      host: { goos: "windows", goarch: "amd64" },
      current: { goos: "linux", goarch: "arm64" },
      overridden: true,
    });
    await selectGoTarget("/work/alpha", { goos: "linux", goarch: "arm64" });
    expect(localStorage.getItem("koyori-ide.workspace.%2Fwork%2Falpha.goTarget")).toContain("linux");

    await restoreHostGoTarget("/work/alpha");
    expect(mocks.service.resetGoTarget).toHaveBeenCalledTimes(1);
    expect(localStorage.getItem("koyori-ide.workspace.%2Fwork%2Falpha.goTarget")).toBeNull();
    expect(goTargetState.overridden).toBe(false);
  });

  it("surfaces backend validation errors without persisting the invalid target", async () => {
    mocks.service.setGoTarget.mockRejectedValue(new Error('unsupported Go target "linux/not-real"'));

    await expect(
      selectGoTarget("/work/alpha", { goos: "linux", goarch: "not-real" }),
    ).resolves.toBeUndefined();

    expect(mocks.notifyError).toHaveBeenCalledWith('unsupported Go target "linux/not-real"');
    expect(localStorage.getItem("koyori-ide.workspace.%2Fwork%2Falpha.goTarget")).toBeNull();
  });
});
