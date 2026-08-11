import { beforeEach, describe, expect, it, vi } from "vitest";

const {
  getBlameMock,
  getBlameAtRevisionMock,
  getCommitGraphMock,
  pushMock,
  pullMock,
} = vi.hoisted(() => ({
  getBlameMock: vi.fn(),
  getBlameAtRevisionMock: vi.fn(),
  getCommitGraphMock: vi.fn(),
  pushMock: vi.fn(),
  pullMock: vi.fn(),
}));

vi.mock("../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/gitservice.js", () => ({
  GetBlame: getBlameMock,
  GetBlameAtRevision: getBlameAtRevisionMock,
  GetCommitGraph: getCommitGraphMock,
  Push: pushMock,
  Pull: pullMock,
}));

import { gitService } from "./git";

describe("8B git service bindings", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    getBlameMock.mockResolvedValue([]);
    getBlameAtRevisionMock.mockResolvedValue([]);
    getCommitGraphMock.mockResolvedValue([]);
    pushMock.mockResolvedValue(undefined);
    pullMock.mockResolvedValue(undefined);
  });

  it("routes bounded blame requests through the revision-aware binding", async () => {
    await gitService.getBlame("/repo", "main.go", 12, 12, "abc1234");
    expect(getBlameAtRevisionMock).toHaveBeenCalledWith(
      "/repo",
      "main.go",
      12,
      12,
      "abc1234",
    );
  });

  it("keeps the legacy two-argument blame call compatible", async () => {
    await gitService.getBlame("/repo", "main.go");
    expect(getBlameAtRevisionMock).toHaveBeenCalledWith("/repo", "main.go", 0, 0, "");
  });

  it("forwards commit graph limits and branch scope", async () => {
    await gitService.getCommitGraph("/repo", 50, "main", false);
    expect(getCommitGraphMock).toHaveBeenCalledWith("/repo", 50, "main", false);
  });

  it("forwards an explicitly selected push remote", async () => {
    await gitService.push("/repo", "upstream");
    expect(pushMock).toHaveBeenCalledWith("/repo", "upstream");
  });

  it("passes an empty push remote so the backend defaults to origin", async () => {
    await gitService.push("/repo");
    expect(pushMock).toHaveBeenCalledWith("/repo", "");
  });

  it("passes an empty remote to Pull so the backend resolves tracking", async () => {
    await gitService.pull("/repo");
    expect(pullMock).toHaveBeenCalledWith("/repo", "");
  });

  it("forwards an explicitly selected pull remote", async () => {
    await gitService.pull("/repo", "upstream");
    expect(pullMock).toHaveBeenCalledWith("/repo", "upstream");
  });
});
