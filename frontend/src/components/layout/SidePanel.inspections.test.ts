import { shallowMount } from "@vue/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({ t: (key: string) => key }),
  translate: (key: string) => key,
}));
vi.mock("@/stores/git", () => ({
  gitState: { branchName: "main" },
  refreshGit: vi.fn(),
}));
vi.mock("@/lib/vscodeExtensions", () => ({ listAllVscodeExtensionViews: () => ({}) }));

import SidePanel from "./SidePanel.vue";
import { appState } from "@/stores/app";

describe("SidePanel inspections entry", () => {
  beforeEach(() => {
    appState.sidebarCollapsed = false;
    appState.currentProject = "C:/workspace";
    appState.panelTab = "inspections";
  });

  afterEach(() => {
    appState.panelTab = "explorer";
  });

  it("renders the project inspection tool without replacing existing tool branches", () => {
    const wrapper = shallowMount(SidePanel);
    const inspections = wrapper.findComponent({ name: "InspectionToolWindow" });
    expect(inspections.exists()).toBe(true);
    expect(inspections.props("repoPath")).toBe("C:/workspace");
    expect(wrapper.text()).toContain("activity.inspections");
    wrapper.unmount();
  });
});
