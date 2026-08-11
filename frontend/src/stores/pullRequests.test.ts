import { beforeEach, describe, expect, it, vi } from "vitest";
import type {
  PullRequest,
  PullRequestListResult,
  PullRequestRepository,
} from "@/types/pullRequests";
import {
  configurePullRequests,
  createPullRequest,
  filteredPullRequests,
  loadPullRequest,
  loadPullRequests,
  pullRequestState,
  resetPullRequestStore,
  setPullRequestBackend,
  submitPullRequestReview,
  type PullRequestBackend,
} from "./pullRequests";

vi.mock("@wailsio/runtime", () => ({
  Call: { ByID: vi.fn(), ByName: vi.fn() },
  Events: { On: vi.fn(() => vi.fn()) },
}));

function repository(provider: "github" | "gitlab" = "github"): PullRequestRepository {
  return {
    provider,
    host: provider === "github" ? "github.com" : "gitlab.example.com",
    owner: "acme",
    name: "widgets",
    projectPath: "acme/widgets",
    webUrl: `https://${provider}.example/acme/widgets`,
    apiBaseUrl: `https://api.${provider}.example`,
    capabilities: {
      canCreate: true,
      canComment: true,
      canApprove: true,
      canRequestChanges: provider === "github",
    },
  };
}

function pullRequest(number: number, title: string): PullRequest {
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
  return {
    detectRepository: vi.fn().mockResolvedValue(repository()),
    listPullRequests: vi.fn().mockResolvedValue({
      repository: repository(),
      items: [],
      truncated: false,
    } satisfies PullRequestListResult),
    getPullRequest: vi.fn(),
    createPullRequest: vi.fn(),
    addComment: vi.fn(),
    submitReview: vi.fn(),
    ...overrides,
  };
}

describe("pull request store", () => {
  beforeEach(() => {
    resetPullRequestStore();
    setPullRequestBackend(null);
    configurePullRequests({ repoPath: "C:/repo", configId: "github-config" });
  });

  it("loads and filters pull requests by title, branch, author, and state", async () => {
    const mock = backend({
      listPullRequests: vi.fn().mockResolvedValue({
        repository: repository(),
        items: [pullRequest(1, "Fix parser"), pullRequest(2, "Add panel")],
        truncated: true,
      }),
    });
    setPullRequestBackend(mock);

    await loadPullRequests();
    expect(pullRequestState.items).toHaveLength(2);
    expect(pullRequestState.truncated).toBe(true);
    expect(pullRequestState.access).toBe("ready");

    pullRequestState.filter = "feature-2";
    expect(filteredPullRequests.value.map((item) => item.number)).toEqual([2]);
    pullRequestState.filter = "octo";
    expect(filteredPullRequests.value).toHaveLength(2);
  });

  it("ignores a list result from a previous repository", async () => {
    let resolveFirst!: (value: PullRequestListResult) => void;
    const first = new Promise<PullRequestListResult>((resolve) => { resolveFirst = resolve; });
    const list = vi.fn()
      .mockReturnValueOnce(first)
      .mockResolvedValueOnce({ repository: repository(), items: [pullRequest(2, "New")], truncated: false });
    setPullRequestBackend(backend({ listPullRequests: list }));

    const pending = loadPullRequests();
    configurePullRequests({ repoPath: "D:/other", configId: "github-config" });
    await loadPullRequests();
    resolveFirst({ repository: repository(), items: [pullRequest(1, "Stale")], truncated: false });
    await pending;

    expect(pullRequestState.items.map((item) => item.title)).toEqual(["New"]);
  });

  it("loads detail, creates a request, and submits a supported review", async () => {
    const detail = { ...pullRequest(4, "Detail"), diff: "diff --git a/a b/a" };
    const created = pullRequest(9, "Created");
    const mock = backend({
      getPullRequest: vi.fn().mockResolvedValue(detail),
      createPullRequest: vi.fn().mockResolvedValue(created),
      submitReview: vi.fn().mockResolvedValue({ id: 5, state: "approved", author: { login: "me" } }),
    });
    setPullRequestBackend(mock);
    pullRequestState.repository = repository();

    await loadPullRequest(4);
    expect(pullRequestState.selected?.diff).toContain("diff --git");
    await createPullRequest({ title: "Created", sourceBranch: "feature", targetBranch: "main" });
    expect(pullRequestState.selected?.number).toBe(9);
    expect(pullRequestState.items[0]?.number).toBe(9);
    await submitPullRequestReview("approve", "Approved");
    expect(mock.submitReview).toHaveBeenCalledWith(expect.objectContaining({ number: 9, action: "approve" }));
    expect(pullRequestState.lastAction).toBe("approved");
  });

  it("classifies missing credentials and provider permissions", async () => {
    setPullRequestBackend(backend({
      listPullRequests: vi.fn().mockRejectedValue(new Error("authentication is not configured")),
    }));
    await loadPullRequests();
    expect(pullRequestState.access).toBe("authentication-required");
    expect(pullRequestState.error).toContain("authentication");

    pullRequestState.repository = repository("gitlab");
    pullRequestState.selected = pullRequest(3, "GitLab");
    const ok = await submitPullRequestReview("request_changes", "No");
    expect(ok).toBe(false);
    expect(pullRequestState.access).toBe("permission-denied");
  });
});
