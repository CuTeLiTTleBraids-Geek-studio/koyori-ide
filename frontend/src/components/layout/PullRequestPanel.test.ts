import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { enableAutoUnmount, flushPromises, mount } from "@vue/test-utils";
import type {
  PullRequest,
  PullRequestListResult,
  PullRequestRepository,
} from "@/types/pullRequests";
import {
  resetPullRequestStore,
  setPullRequestBackend,
  type PullRequestBackend,
} from "@/stores/pullRequests";
import PullRequestPanel from "./PullRequestPanel.vue";

enableAutoUnmount(afterEach);

function repository(provider: "github" | "gitlab" = "github"): PullRequestRepository {
  return {
    provider,
    host: provider === "github" ? "github.com" : "gitlab.example.com",
    owner: "acme",
    name: "widgets",
    projectPath: "acme/widgets",
    webUrl: "https://example.com/acme/widgets",
    apiBaseUrl: "https://api.example.com",
    capabilities: {
      canCreate: true,
      canComment: true,
      canApprove: true,
      canRequestChanges: provider === "github",
    },
  };
}

function request(number: number, title: string): PullRequest {
  return {
    id: number,
    number,
    title,
    state: "open",
    author: { login: "octo" },
    sourceBranch: `feature-${number}`,
    targetBranch: "main",
    draft: false,
  };
}

function backend(overrides: Partial<PullRequestBackend> = {}): PullRequestBackend {
  const repo = repository();
  return {
    detectRepository: vi.fn().mockResolvedValue(repo),
    listPullRequests: vi.fn().mockResolvedValue({
      repository: repo,
      items: [request(1, "Fix parser"), request(2, "Add panel")],
      truncated: false,
    } satisfies PullRequestListResult),
    getPullRequest: vi.fn().mockImplementation(async ({ number }) => ({
      ...request(number, number === 1 ? "Fix parser" : "Add panel"),
      body: "Detailed description",
      diff: "diff --git a/main.go b/main.go\n+fixed",
    })),
    createPullRequest: vi.fn().mockResolvedValue(request(9, "New request")),
    addComment: vi.fn().mockResolvedValue({ id: 8, body: "LGTM", author: { login: "me" } }),
    submitReview: vi.fn().mockResolvedValue({ id: 7, state: "approved", author: { login: "me" } }),
    ...overrides,
  };
}

function mountPanel() {
  return mount(PullRequestPanel, {
    props: { repoPath: "C:/repo", configId: "github-config" },
    global: { stubs: { "el-icon": true } },
  });
}

describe("PullRequestPanel", () => {
  beforeEach(() => {
    resetPullRequestStore();
    setPullRequestBackend(null);
  });

  it("loads the provider list and filters visible requests", async () => {
    setPullRequestBackend(backend());
    const wrapper = mountPanel();
    await flushPromises();

    expect(wrapper.get('[data-test="provider-name"]').text()).toContain("GitHub");
    expect(wrapper.findAll('[data-test="pull-request-row"]')).toHaveLength(2);
    await wrapper.get('[data-test="pull-request-filter"]').setValue("feature-2");
    expect(wrapper.findAll('[data-test="pull-request-row"]')).toHaveLength(1);
    expect(wrapper.text()).toContain("Add panel");
  });

  it("opens detail, renders diff as text, comments, and approves", async () => {
    const mock = backend();
    setPullRequestBackend(mock);
    const wrapper = mountPanel();
    await flushPromises();

    await wrapper.findAll('[data-test="pull-request-row"]')[0].trigger("click");
    await flushPromises();
    expect(wrapper.get('[data-test="pull-request-diff"]').text()).toContain("diff --git");

    await wrapper.get('[data-test="review-body"]').setValue("LGTM");
    await wrapper.get('[data-test="comment-button"]').trigger("click");
    await flushPromises();
    expect(mock.addComment).toHaveBeenCalledWith(expect.objectContaining({ number: 1, body: "LGTM" }));

    await wrapper.get('[data-test="approve-button"]').trigger("click");
    await flushPromises();
    expect(mock.submitReview).toHaveBeenCalledWith(expect.objectContaining({ number: 1, action: "approve" }));
  });

  it("creates a pull request from the create view", async () => {
    const mock = backend();
    setPullRequestBackend(mock);
    const wrapper = mountPanel();
    await flushPromises();

    await wrapper.get('[data-test="new-pull-request"]').trigger("click");
    await wrapper.get('[data-test="create-title"]').setValue("New request");
    await wrapper.get('[data-test="create-source"]').setValue("feature");
    await wrapper.get('[data-test="create-target"]').setValue("main");
    await wrapper.get('[data-test="create-form"]').trigger("submit");
    await flushPromises();

    expect(mock.createPullRequest).toHaveBeenCalledWith(expect.objectContaining({
      title: "New request",
      sourceBranch: "feature",
      targetBranch: "main",
    }));
    expect(wrapper.text()).toContain("New request");
  });

  it("shows authentication guidance without exposing provider errors", async () => {
    setPullRequestBackend(backend({
      listPullRequests: vi.fn().mockRejectedValue(new Error("authentication is not configured")),
    }));
    const wrapper = mountPanel();
    await flushPromises();

    const state = wrapper.get('[data-test="pull-request-access-state"]');
    expect(state.text()).toContain("credential configuration");
    expect(wrapper.find('[data-test="pull-request-row"]').exists()).toBe(false);
  });

  it("hides request-changes when the provider does not support it", async () => {
    const gitlab = repository("gitlab");
    setPullRequestBackend(backend({
      listPullRequests: vi.fn().mockResolvedValue({ repository: gitlab, items: [request(3, "MR")], truncated: false }),
      getPullRequest: vi.fn().mockResolvedValue({ ...request(3, "MR"), diff: "patch" }),
    }));
    const wrapper = mountPanel();
    await flushPromises();
    await wrapper.get('[data-test="pull-request-row"]').trigger("click");
    await flushPromises();

    expect(wrapper.find('[data-test="approve-button"]').exists()).toBe(true);
    expect(wrapper.find('[data-test="request-changes-button"]').exists()).toBe(false);
  });
});
