import { beforeEach, describe, expect, it, vi } from "vitest";

const bindings = vi.hoisted(() => ({
  AddComment: vi.fn(),
  CreatePullRequest: vi.fn(),
  DetectRepository: vi.fn(),
  GetPullRequest: vi.fn(),
  ListPullRequests: vi.fn(),
  SubmitReview: vi.fn(),
}));

vi.mock("../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/pullrequestservice.js", () => bindings);

import { pullRequestService } from "./pullRequests";
import type { PullRequestConnection } from "@/types/pullRequests";

describe("pullRequestService bindings", () => {
  const connection: PullRequestConnection = {
    repoPath: "C:/repo",
    configId: "github-config",
  };

  beforeEach(() => {
    for (const binding of Object.values(bindings)) {
      binding.mockReset().mockResolvedValue(undefined);
    }
  });

  it("uses generated bindings without passing a token", async () => {
    await pullRequestService.detectRepository(connection);
    await pullRequestService.listPullRequests({ connection, state: "open" });
    await pullRequestService.getPullRequest({ connection, number: 7 });
    await pullRequestService.createPullRequest({
      connection,
      title: "Ship",
      sourceBranch: "feature",
      targetBranch: "main",
    });

    expect(bindings.DetectRepository).toHaveBeenCalledWith(connection);
    expect(bindings.ListPullRequests).toHaveBeenCalledWith({ connection, state: "open" });
    expect(bindings.GetPullRequest).toHaveBeenCalledWith({ connection, number: 7 });
    expect(bindings.CreatePullRequest).toHaveBeenCalledWith(
      expect.objectContaining({ connection, title: "Ship" }),
    );
    expect(JSON.stringify(Object.values(bindings).flatMap((binding) => binding.mock.calls))).not.toContain(
      "token",
    );
  });

  it("binds comments and provider reviews", async () => {
    await pullRequestService.addComment({ connection, number: 3, body: "LGTM" });
    await pullRequestService.submitReview({
      connection,
      number: 3,
      action: "approve",
      body: "Approved",
    });

    expect(bindings.AddComment).toHaveBeenCalledWith({ connection, number: 3, body: "LGTM" });
    expect(bindings.SubmitReview).toHaveBeenCalledWith({
      connection,
      number: 3,
      action: "approve",
      body: "Approved",
    });
  });
});
