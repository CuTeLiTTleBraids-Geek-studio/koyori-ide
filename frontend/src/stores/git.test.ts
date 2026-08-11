import { describe, it, expect, beforeEach, vi } from "vitest";

vi.mock("@/api/services", () => ({
  gitService: {
    getStatus: vi.fn().mockResolvedValue([
      { path: "a.txt", status: "Modified" },
      { path: "b.txt", status: "Untracked" },
    ]),
    getBranchInfo: vi.fn().mockResolvedValue({
      name: "main",
      ahead: 2,
      behind: 0,
    }),
    stage: vi.fn().mockResolvedValue(undefined),
    unstage: vi.fn().mockResolvedValue(undefined),
    commit: vi.fn().mockResolvedValue(undefined),
    push: vi.fn().mockResolvedValue(undefined),
    pull: vi.fn().mockResolvedValue(undefined),
    resolveConflict: vi.fn().mockResolvedValue(undefined),
    listMergeConflicts: vi.fn().mockResolvedValue([]),
    // 优先级 3: Stash / Tag / Amend
    stashList: vi.fn().mockResolvedValue([
      { ref: "stash@{0}", commitHash: "abc123", message: "WIP" },
    ]),
    stashPush: vi.fn().mockResolvedValue(undefined),
    stashPop: vi.fn().mockResolvedValue(undefined),
    stashApply: vi.fn().mockResolvedValue(undefined),
    stashDrop: vi.fn().mockResolvedValue(undefined),
    listTags: vi.fn().mockResolvedValue([
      { name: "v1.0.0", commitHash: "def456", message: "Release 1.0.0" },
    ]),
    createTag: vi.fn().mockResolvedValue(undefined),
    deleteTag: vi.fn().mockResolvedValue(undefined),
    pushTags: vi.fn().mockResolvedValue(undefined),
    amendCommit: vi.fn().mockResolvedValue(undefined),
  },
  fileService: {
    writeFile: vi.fn().mockResolvedValue(undefined),
  },
}));

import {
  gitState,
  refreshGit,
  stageFile,
  unstageFile,
  commitChanges,
  pushChanges,
  pullChanges,
  resolveConflictAsOurs,
  resolveConflictAsTheirs,
  stashState,
  tagState,
  loadStashes,
  stashPush,
  stashPop,
  stashApply,
  stashDrop,
  loadTags,
  createTag,
  deleteTag,
  pushTags,
  amendCommit,
  clearStashAndTagState,
} from "./git";
import type { MergeConflict } from "@/types";

describe("git store", () => {
  beforeEach(() => {
    gitState.changes = [];
    gitState.branchName = "";
    gitState.ahead = 0;
    gitState.behind = 0;
    gitState.loading = false;
    gitState.error = null;
  });

  it("starts with empty state", () => {
    expect(gitState.changes).toHaveLength(0);
    expect(gitState.branchName).toBe("");
    expect(gitState.loading).toBe(false);
  });

  it("refreshGit loads changes and branch info", async () => {
    await refreshGit("/some/repo");
    expect(gitState.changes).toHaveLength(2);
    expect(gitState.changes[0].path).toBe("a.txt");
    expect(gitState.branchName).toBe("main");
    expect(gitState.ahead).toBe(2);
    expect(gitState.loading).toBe(false);
  });

  it("stageFile calls gitService.stage", async () => {
    await stageFile("/repo", "a.txt");
    const { gitService } = await import("@/api/services");
    expect(gitService.stage).toHaveBeenCalledWith("/repo", "a.txt");
  });

  it("unstageFile calls gitService.unstage", async () => {
    await unstageFile("/repo", "a.txt");
    const { gitService } = await import("@/api/services");
    expect(gitService.unstage).toHaveBeenCalledWith("/repo", "a.txt");
  });

  it("commitChanges calls gitService.commit and refreshes", async () => {
    await commitChanges("/repo", "fix: something");
    const { gitService } = await import("@/api/services");
    expect(gitService.commit).toHaveBeenCalledWith("/repo", "fix: something");
    expect(gitService.getStatus).toHaveBeenCalled();
  });

  it("stores error on failure", async () => {
    const { gitService } = await import("@/api/services");
    (gitService.getStatus as any).mockRejectedValueOnce(new Error("fail"));
    await refreshGit("/repo");
    expect(gitState.error).toBe("fail");
    expect(gitState.loading).toBe(false);
  });

  it("pushChanges forwards an explicitly selected remote", async () => {
    const { gitService } = await import("@/api/services");
    await pushChanges("/repo", "upstream");
    expect(gitService.push).toHaveBeenCalledWith("/repo", "upstream");
  });

  it("pushChanges forwards an empty remote for the backend origin default", async () => {
    const { gitService } = await import("@/api/services");
    await pushChanges("/repo");
    expect(gitService.push).toHaveBeenCalledWith("/repo", "");
  });

  it("pullChanges forwards an empty remote for backend tracking resolution", async () => {
    const { gitService } = await import("@/api/services");
    await pullChanges("/repo");
    expect(gitService.pull).toHaveBeenCalledWith("/repo", "");
  });

  it("pullChanges forwards an explicitly selected remote", async () => {
    const { gitService } = await import("@/api/services");
    await pullChanges("/repo", "upstream");
    expect(gitService.pull).toHaveBeenCalledWith("/repo", "upstream");
  });
});

// ---------------------------------------------------------------------------
// M-29: resolveConflictAsOurs/Theirs 路径分隔符规范化
// ---------------------------------------------------------------------------

describe("M-29: 冲突解决路径规范化（Windows 兼容）", () => {
  it("resolveConflictAsOurs: Windows 反斜杠路径被规范化为正斜杠（无混合分隔符）", async () => {
    const { fileService } = await import("@/api/services");
    const conflict: MergeConflict = {
      file: "src\\components\\App.tsx",
      ours: "ours content",
      theirs: "theirs content",
      base: "base content",
    };

    await resolveConflictAsOurs("C:\\Users\\dev\\project", conflict);

    // 路径全部使用正斜杠，无混合分隔符
    expect(fileService.writeFile).toHaveBeenCalledWith(
      "C:/Users/dev/project/src/components/App.tsx",
      "ours content",
    );
  });

  it("resolveConflictAsTheirs: Windows 反斜杠路径被规范化为正斜杠", async () => {
    const { fileService } = await import("@/api/services");
    const conflict: MergeConflict = {
      file: "docs\\guide.md",
      ours: "ours",
      theirs: "theirs content",
      base: "base",
    };

    await resolveConflictAsTheirs("D:\\repos\\myapp", conflict);

    expect(fileService.writeFile).toHaveBeenCalledWith(
      "D:/repos/myapp/docs/guide.md",
      "theirs content",
    );
  });

  it("POSIX 路径（正斜杠）不受影响", async () => {
    const { fileService } = await import("@/api/services");
    const conflict: MergeConflict = {
      file: "src/main.ts",
      ours: "ours",
      theirs: "theirs",
      base: "base",
    };

    await resolveConflictAsOurs("/home/user/project", conflict);

    expect(fileService.writeFile).toHaveBeenCalledWith(
      "/home/user/project/src/main.ts",
      "ours",
    );
  });
});

// ---------------------------------------------------------------------------
// 优先级 3: Git Stash / Tag / Amend store 单元测试
// ---------------------------------------------------------------------------

describe("优先级 3: Stash / Tag / Amend store", () => {
  beforeEach(() => {
    // 重置 stash/tag 状态
    stashState.stashes = [];
    stashState.loading = false;
    stashState.error = null;
    tagState.tags = [];
    tagState.loading = false;
    tagState.error = null;
    vi.clearAllMocks();
  });

  it("loadStashes 调用 gitService.stashList 并填充 stashState", async () => {
    const { gitService } = await import("@/api/services");
    (gitService.stashList as any).mockResolvedValueOnce([
      { ref: "stash@{0}", commitHash: "abc123", message: "WIP: feature" },
      { ref: "stash@{1}", commitHash: "def456", message: "WIP: bugfix" },
    ]);

    await loadStashes();

    expect(gitService.stashList).toHaveBeenCalled();
    expect(stashState.stashes).toHaveLength(2);
    expect(stashState.stashes[0].ref).toBe("stash@{0}");
    expect(stashState.stashes[0].message).toBe("WIP: feature");
    expect(stashState.loading).toBe(false);
    expect(stashState.error).toBeNull();
  });

  it("loadStashes 失败时设置 error 并清空列表", async () => {
    const { gitService } = await import("@/api/services");
    (gitService.stashList as any).mockRejectedValueOnce(new Error("network"));

    await loadStashes();

    expect(stashState.error).toBe("network");
    expect(stashState.loading).toBe(false);
  });

  it("stashPush 调用 gitService.stashPush 并刷新 stash 列表", async () => {
    const { gitService } = await import("@/api/services");
    // 先调用 refreshGit 设置 _lastRepoPath，供 stashPush 内部 refreshGit 使用
    await refreshGit("/repo");

    await stashPush("WIP: save work");

    expect(gitService.stashPush).toHaveBeenCalledWith("WIP: save work");
    // stashPush 内部会调用 loadStashes 刷新列表
    expect(gitService.stashList).toHaveBeenCalled();
  });

  it("stashPop 调用 gitService.stashPop 并传入 stashRef", async () => {
    const { gitService } = await import("@/api/services");
    await refreshGit("/repo");

    await stashPop("stash@{0}");

    expect(gitService.stashPop).toHaveBeenCalledWith("stash@{0}");
  });

  it("stashApply 调用 gitService.stashApply 并传入 stashRef", async () => {
    const { gitService } = await import("@/api/services");
    await refreshGit("/repo");

    await stashApply("stash@{1}");

    expect(gitService.stashApply).toHaveBeenCalledWith("/repo", "stash@{1}");
  });

  it("stashDrop 调用 gitService.stashDrop 并刷新列表", async () => {
    const { gitService } = await import("@/api/services");

    await stashDrop("stash@{0}");

    expect(gitService.stashDrop).toHaveBeenCalledWith("/repo", "stash@{0}");
    expect(gitService.stashList).toHaveBeenCalled();
  });

  it("loadTags 调用 gitService.listTags 并填充 tagState", async () => {
    const { gitService } = await import("@/api/services");
    (gitService.listTags as any).mockResolvedValueOnce([
      { name: "v1.0.0", commitHash: "abc123", message: "Release 1.0.0" },
      { name: "v2.0.0", commitHash: "def456", message: "Release 2.0.0" },
    ]);

    await loadTags();

    expect(gitService.listTags).toHaveBeenCalled();
    expect(tagState.tags).toHaveLength(2);
    expect(tagState.tags[0].name).toBe("v1.0.0");
    expect(tagState.tags[1].message).toBe("Release 2.0.0");
    expect(tagState.loading).toBe(false);
    expect(tagState.error).toBeNull();
  });

  it("createTag 调用 gitService.createTag 并刷新 tag 列表", async () => {
    const { gitService } = await import("@/api/services");

    await createTag("v1.2.3", "Release v1.2.3");

    expect(gitService.createTag).toHaveBeenCalledWith("v1.2.3", "Release v1.2.3");
    expect(gitService.listTags).toHaveBeenCalled();
  });

  it("deleteTag 调用 gitService.deleteTag 并刷新 tag 列表", async () => {
    const { gitService } = await import("@/api/services");

    await deleteTag("v1.0.0");

    expect(gitService.deleteTag).toHaveBeenCalledWith("v1.0.0");
    expect(gitService.listTags).toHaveBeenCalled();
  });

  it("pushTags 调用 gitService.pushTags 并传入 remote", async () => {
    const { gitService } = await import("@/api/services");

    await pushTags("origin");

    expect(gitService.pushTags).toHaveBeenCalledWith("origin");
  });

  it("amendCommit 调用 gitService.amendCommit 并刷新 git 状态", async () => {
    const { gitService } = await import("@/api/services");

    await amendCommit("/repo", "amended message");

    expect(gitService.amendCommit).toHaveBeenCalledWith("amended message");
    expect(gitService.getStatus).toHaveBeenCalled();
  });

  it("clearStashAndTagState 清空所有 stash/tag 状态", () => {
    stashState.stashes = [{
      ref: "stash@{0}",
      commitHash: "x",
      message: "y",
      date: "2026-07-18T00:00:00Z",
      author: "Tester",
    }];
    stashState.loading = true;
    stashState.error = "err";
    tagState.tags = [{ name: "v1", commitHash: "x", message: "y" }];
    tagState.loading = true;
    tagState.error = "err";

    clearStashAndTagState();

    expect(stashState.stashes).toHaveLength(0);
    expect(stashState.loading).toBe(false);
    expect(stashState.error).toBeNull();
    expect(tagState.tags).toHaveLength(0);
    expect(tagState.loading).toBe(false);
    expect(tagState.error).toBeNull();
  });
});
