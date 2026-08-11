import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { enableAutoUnmount, mount } from "@vue/test-utils";

const { refreshGit } = vi.hoisted(() => ({ refreshGit: vi.fn() }));

vi.mock("@/stores/git", () => ({
  gitState: { branchName: "" },
  refreshGit,
}));

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({ t: (key: string) => key }),
}));

import SidePanel from "./SidePanel.vue";
import { appState } from "@/stores/app";
import {
  pullRequestState,
  resetPullRequestStore,
  setSourceControlView,
} from "@/stores/pullRequests";

enableAutoUnmount(afterEach);

function mountPanel() {
  return mount(SidePanel, {
    global: {
      stubs: {
        FileTree: true,
        GitPanel: { template: '<div data-test="git-panel-stub" />' },
        PullRequestPanel: {
          props: ["repoPath", "configId"],
          template: '<div data-test="pull-request-panel-stub" :data-repo="repoPath" :data-config="configId" />',
        },
        SearchPanel: true,
        AiChatPanel: true,
        CallHierarchyPanel: true,
        OutlinePanel: true,
        HTTPClientPanel: true,
        PluginManagementPanel: true,
        MarketplacePanel: true,
        "el-icon": true,
      },
    },
  });
}

describe("SidePanel pull request integration", () => {
  beforeEach(() => {
    resetPullRequestStore();
    setSourceControlView("changes");
    appState.sidebarCollapsed = false;
    appState.panelTab = "git";
    appState.currentProject = "C:/repo";
    appState.activeAIConfigId = "github-config";
    refreshGit.mockReset();
  });

  it("switches the source control body to the pull request panel", async () => {
    const wrapper = mountPanel();
    expect(wrapper.find('[data-test="git-panel-stub"]').exists()).toBe(true);

    await wrapper.get('[data-test="source-control-pull-requests"]').trigger("click");

    const panel = wrapper.get('[data-test="pull-request-panel-stub"]');
    expect(panel.attributes("data-repo")).toBe("C:/repo");
    expect(panel.attributes("data-config")).toBe("github-config");
    expect(pullRequestState.sourceControlView).toBe("pullRequests");
  });
});
