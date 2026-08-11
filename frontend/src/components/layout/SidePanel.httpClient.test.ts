import { shallowMount } from "@vue/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { appState } from "@/stores/app";
import { editorState } from "@/stores/editor";
import SidePanel from "./SidePanel.vue";

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({ t: (key: string) => key }),
  translate: (key: string) => key,
}));

vi.mock("@/stores/git", () => ({
  gitState: { branchName: "main" },
  refreshGit: vi.fn(),
}));

vi.mock("@/lib/vscodeExtensions", () => ({
  listAllVscodeExtensionViews: () => ({}),
}));

vi.mock("@/stores/workspaceModules", () => ({
  setActiveWorkspaceRoot: vi.fn(),
}));

describe("SidePanel tool entries", () => {
  beforeEach(() => {
    appState.sidebarCollapsed = false;
    appState.currentProject = "C:/workspace";
    appState.cursorLine = 6;
    editorState.openFiles = [{
      path: "C:/workspace/requests.http",
      name: "requests.http",
      content: "### health\nGET https://example.com/health",
      originalContent: "### health\nGET https://example.com/health",
      language: "plaintext",
      isDirty: false,
    }];
    editorState.activeFilePath = "C:/workspace/requests.http";
  });

  afterEach(() => {
    (appState as unknown as { panelTab: string }).panelTab = "explorer";
    editorState.openFiles = [];
    editorState.activeFilePath = null;
  });

  it("preserves Outline, Build, and Database while passing the active .http buffer to HTTPClientPanel", () => {
    (appState as unknown as { panelTab: string }).panelTab = "explorer";
    const explorer = shallowMount(SidePanel);
    expect(explorer.findComponent({ name: "OutlinePanel" }).exists()).toBe(true);
    explorer.unmount();

    (appState as unknown as { panelTab: string }).panelTab = "build";
    const build = shallowMount(SidePanel);
    expect(build.findComponent({ name: "BuildToolWindow" }).exists()).toBe(true);
    build.unmount();

    (appState as unknown as { panelTab: string }).panelTab = "database";
    const database = shallowMount(SidePanel);
    expect(database.findComponent({ name: "DatabaseToolWindow" }).exists()).toBe(true);
    database.unmount();

    (appState as unknown as { panelTab: string }).panelTab = "httpClient";
    const http = shallowMount(SidePanel);
    const panel = http.findComponent({ name: "HTTPClientPanel" });
    expect(panel.exists()).toBe(true);
    expect(panel.props("source")).toBe("### health\nGET https://example.com/health");
    expect(panel.props("cursorLine")).toBe(6);
    http.unmount();
  });
});
