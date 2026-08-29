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
    submoduleList: vi.fn().mockResolvedValue([]),
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
  loadMoreGitChanges,
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
  conflictState,
  loadConflicts,
  submoduleState,
  loadSubmodules,
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
// P1-04: 截断续读 — 完整状态保留，可见窗口可分页扩大
// ---------------------------------------------------------------------------

describe("P1-04: 截断续读", () => {
  it("refreshGit 保留全量并投影 1000 条可见窗口", async () => {
    const { gitService } = await import("@/api/services");
    const big = Array.from({ length: 2500 }, (_, i) => ({
      path: `f${i}.txt`,
      status: "Modified",
      staged: false,
    }));
    (gitService.getStatus as any).mockResolvedValueOnce(big);
    await refreshGit("/repo");
    expect(gitState.changes).toHaveLength(1000);
    expect(gitState.truncated).toBe(true);
    expect(gitState.totalChanges).toBe(2500);
    expect(gitState.changes[999].path).toBe("f999.txt");
  });

  it("loadMoreGitChanges 扩大一页窗口并返回剩余隐藏行数", async () => {
    const { gitService } = await import("@/api/services");
    const big = Array.from({ length: 2500 }, (_, i) => ({
      path: `f${i}.txt`,
      status: "Modified",
      staged: false,
    }));
    (gitService.getStatus as any).mockResolvedValueOnce(big);
    await refreshGit("/repo");
    const hidden = loadMoreGitChanges();
    expect(gitState.changes).toHaveLength(2000);
    expect(gitState.truncated).toBe(true);
    expect(hidden).toBe(500);
    expect(loadMoreGitChanges()).toBe(0);
    expect(gitState.changes).toHaveLength(2500);
    expect(gitState.truncated).toBe(false);
  });

  it("非截断列表续读为 no-op 且 truncated 复位", async () => {
    await refreshGit("/some/repo");
    expect(gitState.truncated).toBe(false);
    expect(gitState.totalChanges).toBe(2);
    expect(loadMoreGitChanges()).toBe(0);
    expect(gitState.changes).toHaveLength(2);
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

// ---------------------------------------------------------------------------
// P1-01: stale 竞态守卫 — 快速切换/重复触发时迟到的旧响应不回写状态
// ---------------------------------------------------------------------------

describe("P1-01: stale 竞态守卫", () => {
  it("refreshGit 快速切换仓库时，迟到的旧仓库响应不回写状态", async () => {
    const { gitService } = await import("@/api/services");
    let resolveOldStatus!: (v: unknown) => void;
    let resolveOldInfo!: (v: unknown) => void;
    (gitService.getStatus as any)
      .mockImplementationOnce(
        () => new Promise((resolve) => { resolveOldStatus = resolve; }),
      )
      .mockResolvedValueOnce([{ path: "new.txt", status: "Modified" }]);
    (gitService.getBranchInfo as any)
      .mockImplementationOnce(
        () => new Promise((resolve) => { resolveOldInfo = resolve; }),
      )
      .mockResolvedValueOnce({ name: "feature", ahead: 0, behind: 0 });

    const stale = refreshGit("/old/repo");
    await refreshGit("/new/repo");
    expect(gitState.branchName).toBe("feature");
    expect(gitState.ahead).toBe(0);

    // 旧仓库的响应此刻才迟到完成：不允许覆盖新仓库状态
    resolveOldStatus([{ path: "old.txt", status: "Modified" }]);
    resolveOldInfo({ name: "main", ahead: 5, behind: 3 });
    await stale;

    expect(gitState.changes).toHaveLength(1);
    expect(gitState.changes[0].path).toBe("new.txt");
    expect(gitState.branchName).toBe("feature");
    expect(gitState.ahead).toBe(0);
    expect(gitState.behind).toBe(0);
    expect(gitState.loading).toBe(false);
    expect(gitState.error).toBeNull();
  });

  it("refreshGit 迟到的旧响应失败也不回写 error", async () => {
    const { gitService } = await import("@/api/services");
    let rejectOld!: (e: Error) => void;
    (gitService.getStatus as any)
      .mockImplementationOnce(
        () => new Promise((_, reject) => { rejectOld = reject; }),
      )
      .mockResolvedValueOnce([{ path: "new.txt", status: "Modified" }]);
    (gitService.getBranchInfo as any).mockResolvedValue({ name: "feature", ahead: 0, behind: 0 });

    const stale = refreshGit("/old/repo");
    await refreshGit("/new/repo");

    rejectOld(new Error("old repo exploded"));
    await stale;

    expect(gitState.error).toBeNull();
    expect(gitState.branchName).toBe("feature");
    expect(gitState.loading).toBe(false);
  });

  it("loadConflicts 迟到的旧响应不回写冲突列表", async () => {
    const { gitService } = await import("@/api/services");
    let resolveOld!: (v: unknown) => void;
    (gitService.listMergeConflicts as any)
      .mockImplementationOnce(
        () => new Promise((resolve) => { resolveOld = resolve; }),
      )
      .mockResolvedValueOnce([{ file: "new-conflict.txt" }]);

    const stale = loadConflicts();
    await loadConflicts();
    expect(conflictState.conflicts).toHaveLength(1);

    resolveOld([{ file: "old-conflict.txt" }]);
    await stale;

    expect(conflictState.conflicts[0].file).toBe("new-conflict.txt");
    expect(conflictState.loading).toBe(false);
    conflictState.conflicts = [];
  });

  it("loadStashes 迟到的旧响应不回写 stash 列表", async () => {
    const { gitService } = await import("@/api/services");
    let resolveOld!: (v: unknown) => void;
    (gitService.stashList as any)
      .mockImplementationOnce(
        () => new Promise((resolve) => { resolveOld = resolve; }),
      )
      .mockResolvedValueOnce([{ ref: "stash@{0}", commitHash: "new", message: "new" }]);

    const stale = loadStashes();
    await loadStashes();
    expect(stashState.stashes).toHaveLength(1);

    resolveOld([{ ref: "stash@{0}", commitHash: "old", message: "old" }]);
    await stale;

    expect(stashState.stashes[0].commitHash).toBe("new");
    expect(stashState.loading).toBe(false);
    stashState.stashes = [];
  });

  it("loadSubmodules 迟到的旧响应不回写子模块列表", async () => {
    const { gitService } = await import("@/api/services");
    let resolveOld!: (v: unknown) => void;
    (gitService.submoduleList as any)
      .mockImplementationOnce(
        () => new Promise((resolve) => { resolveOld = resolve; }),
      )
      .mockResolvedValueOnce([{ path: "libs/new", url: "https://example.com/new.git" }]);

    const stale = loadSubmodules();
    await loadSubmodules();
    expect(submoduleState.submodules).toHaveLength(1);

    resolveOld([{ path: "libs/old", url: "https://example.com/old.git" }]);
    await stale;

    expect(submoduleState.submodules[0].path).toBe("libs/new");
    expect(submoduleState.loading).toBe(false);
    submoduleState.submodules = [];
  });
});
