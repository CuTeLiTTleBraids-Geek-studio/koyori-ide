import { beforeEach, describe, expect, it } from "vitest";
import {
  clearWorkspaceGoTarget,
  loadWorkspaceGoTarget,
  saveWorkspaceGoTarget,
} from "./workspaceSettings";

describe("workspace Go target settings", () => {
  beforeEach(() => localStorage.clear());

  it("persists targets independently for each workspace", () => {
    saveWorkspaceGoTarget("C:\\work\\alpha\\", { goos: "linux", goarch: "arm64" });
    saveWorkspaceGoTarget("C:/work/beta", { goos: "darwin", goarch: "amd64" });

    expect(loadWorkspaceGoTarget("C:/work/alpha")).toEqual({ goos: "linux", goarch: "arm64" });
    expect(loadWorkspaceGoTarget("C:\\work\\beta\\")).toEqual({ goos: "darwin", goarch: "amd64" });
  });

  it("clears only the selected workspace when restoring host", () => {
    saveWorkspaceGoTarget("/work/alpha", { goos: "linux", goarch: "arm64" });
    saveWorkspaceGoTarget("/work/beta", { goos: "windows", goarch: "amd64" });

    clearWorkspaceGoTarget("/work/alpha/");

    expect(loadWorkspaceGoTarget("/work/alpha")).toBeNull();
    expect(loadWorkspaceGoTarget("/work/beta")).toEqual({ goos: "windows", goarch: "amd64" });
  });

  it("ignores malformed persisted values", () => {
    localStorage.setItem("koyori-ide.workspace.%2Fwork%2Falpha.goTarget", "not-json");
    expect(loadWorkspaceGoTarget("/work/alpha")).toBeNull();
  });
});
