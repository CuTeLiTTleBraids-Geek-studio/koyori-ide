// Koyori IDE 模块 · Git；交互服务：Git 集成（GitService）、文件系统（FileService）。
// 喵，这是 Koyori IDE 的 Git 模块（前端实现）~
import { reactive } from "vue";
import { gitService, fileService } from "@/api/services";
import type { GitFileChange, BranchRef, MergeConflict, StashEntry, TagEntry, SubmoduleInfo } from "@/types";
import { pushOutput } from "@/stores/output";
import { errorMessage } from "@/lib/errors";

export interface GitState {
  changes: GitFileChange[];
  branchName: string;
  ahead: number;
  behind: number;
  loading: boolean;
  error: string | null;
  /** prompt-7 Task L: status list was capped for UI. */
  truncated: boolean;
  /** P1-04: total status rows; `changes` is the visible window of it. */
  totalChanges: number;
}

/** Cap Git status list in UI to avoid jank on huge dirty trees (prompt-7 Task L). */
export const MAX_GIT_UI_CHANGES = 1000;

/** _lastRepoPath 记录最近一次 refreshGit 使用的仓库路径，供 stash/tag 等
 * 无需 repoPath 参数的操作在完成后刷新 git 状态使用。 */
let _lastRepoPath = "";

/** P1-01 stale 守卫：并发刷新/加载时只有最新一次调用允许回写状态，
 * 快速切换仓库（或重复触发）后迟到的旧响应一律丢弃，不覆盖新数据。 */
let _refreshGitGeneration = 0;
let _conflictsGeneration = 0;
let _stashGeneration = 0;
let _tagGeneration = 0;
let _submoduleGeneration = 0;

/** currentRepoPath 返回最近一次 refreshGit 使用的仓库路径。 */
function currentRepoPath(): string {
  return _lastRepoPath;
}

/** P1-04 截断续读：完整状态保留在此，`gitState.changes` 只是可见窗口。 */
let _allChanges: GitFileChange[] = [];

function applyVisibleGitChanges(): void {
  gitState.changes = _allChanges.slice(0, MAX_GIT_UI_CHANGES);
  gitState.truncated = _allChanges.length > gitState.changes.length;
  gitState.totalChanges = _allChanges.length;
}

/** P1-04 续读：把可见窗口扩大一页，返回仍隐藏的行数（0 表示已全部可见）。 */
export function loadMoreGitChanges(): number {
  if (_allChanges.length <= gitState.changes.length) return 0;
  const pages = Math.floor(gitState.changes.length / MAX_GIT_UI_CHANGES) + 1;
  gitState.changes = _allChanges.slice(0, pages * MAX_GIT_UI_CHANGES);
  gitState.truncated = _allChanges.length > gitState.changes.length;
  return _allChanges.length - gitState.changes.length;
}

export const gitState = reactive<GitState>({
  changes: [],
  branchName: "",
  ahead: 0,
  behind: 0,
  loading: false,
  error: null,
  truncated: false,
  totalChanges: 0,
});

/**
 * M-29: 规范化路径分隔符并拼接 workspace 路径。
 * Windows 下 repoPath 可能使用反斜杠（如 "C:\repo"），直接用 "/" 拼接
 * 会产生混合分隔符（"C:\repo/src/file.txt"）。将两部分都统一为正斜杠
 * 后再拼接，确保跨平台一致性。
 */
export function joinWorkspacePath(repoPath: string, file: string): string {
  const normalizedRepo = repoPath.replace(/\\/g, "/");
  const normalizedFile = file.replace(/\\/g, "/");
  return `${normalizedRepo}/${normalizedFile}`;
}

export const branchState = reactive({
  branches: [] as BranchRef[],
  loadingBranches: false,
});

// G-FEAT-04: merge/rebase conflict state.
export interface ConflictState {
  conflicts: MergeConflict[];
  loading: boolean;
  error: string | null;
}

export const conflictState = reactive<ConflictState>({
  conflicts: [],
  loading: false,
  error: null,
});

// G-FEAT-04: rebase state.
export interface RebaseState {
  inProgress: boolean;
  loading: boolean;
  error: string | null;
  lastOutput: string;
}

export const rebaseState = reactive<RebaseState>({
  inProgress: false,
  loading: false,
  error: null,
  lastOutput: "",
});

export async function refreshGit(repoPath: string): Promise<void> {
  const generation = ++_refreshGitGeneration;
  gitState.loading = true;
  gitState.error = null;
  _lastRepoPath = repoPath;
  try {
    const [changes, info] = await Promise.all([
      gitService.getStatus(repoPath),
      gitService.getBranchInfo(repoPath),
    ]);
    if (generation !== _refreshGitGeneration) return;
    _allChanges = changes;
    applyVisibleGitChanges();
    gitState.branchName = info.name;
    gitState.ahead = info.ahead;
    gitState.behind = info.behind;
  } catch (e: unknown) {
    if (generation !== _refreshGitGeneration) return;
    gitState.error = errorMessage(e);
  } finally {
    if (generation === _refreshGitGeneration) {
      gitState.loading = false;
    }
  }
}

export async function discoverRepositories(root: string): Promise<string[]> {
  if (!root) return [];
  try {
    return await gitService.discoverRepositories(root);
  } catch (e: unknown) {
    gitState.error = errorMessage(e);
    return [];
  }
}

/**
 * BUG2: Initialize a new git repository at the given path. Used when the
 * project directory is not yet a git repo, so the user can start tracking
 * changes from the source control panel instead of seeing an error.
 */
export async function initRepo(repoPath: string): Promise<void> {
  gitState.loading = true;
  gitState.error = null;
  try {
    await gitService.initRepo(repoPath);
    await refreshGit(repoPath);
    await loadBranches(repoPath);
  } catch (e: unknown) {
    gitState.error = errorMessage(e);
  } finally {
    gitState.loading = false;
  }
}

export async function stageFile(repoPath: string, filePath: string): Promise<void> {
  try {
    await gitService.stage(repoPath, filePath);
    await refreshGit(repoPath);
  } catch (e: unknown) {
    gitState.error = errorMessage(e);
  }
}

export async function unstageFile(repoPath: string, filePath: string): Promise<void> {
  try {
    await gitService.unstage(repoPath, filePath);
    await refreshGit(repoPath);
  } catch (e: unknown) {
    gitState.error = errorMessage(e);
  }
}

export async function commitChanges(repoPath: string, message: string): Promise<void> {
  try {
    await gitService.commit(repoPath, message);
    pushOutput("git", "success", `Committed: ${message}`);
    await refreshGit(repoPath);
  } catch (e: unknown) {
    gitState.error = errorMessage(e);
    pushOutput("git", "error", `Commit failed: ${errorMessage(e)}`);
  }
}

export function clearGitState(): void {
  _allChanges = [];
  gitState.changes = [];
  gitState.branchName = "";
  gitState.ahead = 0;
  gitState.behind = 0;
  gitState.loading = false;
  gitState.error = null;
  gitState.truncated = false;
  gitState.totalChanges = 0;
}

export async function loadBranches(repoPath: string) {
  if (!repoPath) return;
  branchState.loadingBranches = true;
  try {
    branchState.branches = await gitService.listBranches(repoPath);
  } catch (e) {
    console.error("Failed to load branches:", e);
    branchState.branches = [];
  } finally {
    branchState.loadingBranches = false;
  }
}

export async function createBranch(repoPath: string, name: string) {
  await gitService.createBranch(repoPath, name);
  await loadBranches(repoPath);
  await refreshGit(repoPath);
}

export async function checkoutBranch(repoPath: string, name: string) {
  await gitService.checkoutBranch(repoPath, name);
  await loadBranches(repoPath);
  await refreshGit(repoPath);
}

export async function deleteBranch(repoPath: string, name: string) {
  await gitService.deleteBranch(repoPath, name);
  await loadBranches(repoPath);
}

export async function pushChanges(repoPath: string, remoteName = ""): Promise<void> {
  try {
    await gitService.push(repoPath, remoteName);
    pushOutput("git", "success", "Pushed successfully");
    await refreshGit(repoPath);
  } catch (e: unknown) {
    gitState.error = errorMessage(e);
    pushOutput("git", "error", `Push failed: ${errorMessage(e)}`);
    throw e;
  }
}

export async function pullChanges(repoPath: string, remoteName = ""): Promise<void> {
  try {
    await gitService.pull(repoPath, remoteName);
    pushOutput("git", "success", "Pulled successfully");
    await refreshGit(repoPath);
  } catch (e: unknown) {
    gitState.error = errorMessage(e);
    pushOutput("git", "error", `Pull failed: ${errorMessage(e)}`);
    throw e;
  }
}

// ---------------------------------------------------------------------------
// G-FEAT-04: merge conflict resolution
// ---------------------------------------------------------------------------

export async function loadConflicts(): Promise<void> {
  const generation = ++_conflictsGeneration;
  conflictState.loading = true;
  conflictState.error = null;
  try {
    const conflicts = await gitService.listMergeConflicts();
    if (generation !== _conflictsGeneration) return;
    conflictState.conflicts = conflicts;
  } catch (e: unknown) {
    if (generation !== _conflictsGeneration) return;
    conflictState.error = errorMessage(e);
  } finally {
    if (generation === _conflictsGeneration) {
      conflictState.loading = false;
    }
  }
}

export function clearConflictState(): void {
  conflictState.conflicts = [];
  conflictState.loading = false;
  conflictState.error = null;
}

/**
 * Resolve a conflict by accepting "ours": writes the ours-side blob content
 * to the working tree file, then stages it.
 */
export async function resolveConflictAsOurs(repoPath: string, conflict: MergeConflict): Promise<void> {
  try {
    const fullPath = joinWorkspacePath(repoPath, conflict.file);
    await fileService.writeFile(fullPath, conflict.ours);
    await gitService.resolveConflict(conflict.file);
    await loadConflicts();
    await refreshGit(repoPath);
  } catch (e: unknown) {
    conflictState.error = errorMessage(e);
    throw e;
  }
}

/**
 * Resolve a conflict by accepting "theirs": writes the theirs-side blob
 * content to the working tree file, then stages it.
 */
export async function resolveConflictAsTheirs(repoPath: string, conflict: MergeConflict): Promise<void> {
  try {
    const fullPath = joinWorkspacePath(repoPath, conflict.file);
    await fileService.writeFile(fullPath, conflict.theirs);
    await gitService.resolveConflict(conflict.file);
    await loadConflicts();
    await refreshGit(repoPath);
  } catch (e: unknown) {
    conflictState.error = errorMessage(e);
    throw e;
  }
}

/**
 * Mark a manually-resolved conflict as staged. The user is expected to have
 * edited and saved the file in the editor first.
 */
export async function markConflictResolved(repoPath: string, file: string): Promise<void> {
  try {
    await gitService.resolveConflict(file);
    await loadConflicts();
    await refreshGit(repoPath);
  } catch (e: unknown) {
    conflictState.error = errorMessage(e);
    throw e;
  }
}

// ---------------------------------------------------------------------------
// G-FEAT-04: rebase support
// ---------------------------------------------------------------------------

export async function checkRebaseStatus(): Promise<void> {
  try {
    rebaseState.inProgress = await gitService.isRebaseInProgress();
  } catch (e: unknown) {
    rebaseState.error = errorMessage(e);
  }
}

export async function startRebase(branch: string): Promise<string | null> {
  rebaseState.loading = true;
  rebaseState.error = null;
  try {
    const output = await gitService.rebase(branch);
    rebaseState.lastOutput = output;
    await checkRebaseStatus();
    if (rebaseState.inProgress) {
      await loadConflicts();
    }
    return output;
  } catch (e: unknown) {
    rebaseState.error = errorMessage(e);
    rebaseState.lastOutput = errorMessage(e);
    // A rebase conflict also produces a non-zero exit, so check if a rebase
    // is now in progress (conflicts waiting to be resolved).
    await checkRebaseStatus();
    if (rebaseState.inProgress) {
      await loadConflicts();
    }
    return null;
  } finally {
    rebaseState.loading = false;
  }
}

export async function abortRebase(): Promise<void> {
  rebaseState.loading = true;
  rebaseState.error = null;
  try {
    await gitService.abortRebase();
    rebaseState.inProgress = false;
    clearConflictState();
    pushOutput("git", "success", "Rebase aborted");
  } catch (e: unknown) {
    rebaseState.error = errorMessage(e);
    throw e;
  } finally {
    rebaseState.loading = false;
  }
}

export async function continueRebase(): Promise<void> {
  rebaseState.loading = true;
  rebaseState.error = null;
  try {
    await gitService.continueRebase();
    await checkRebaseStatus();
    if (rebaseState.inProgress) {
      await loadConflicts();
    } else {
      clearConflictState();
    }
    pushOutput("git", "success", "Rebase continued");
  } catch (e: unknown) {
    rebaseState.error = errorMessage(e);
    await checkRebaseStatus();
    if (rebaseState.inProgress) {
      await loadConflicts();
    }
    throw e;
  } finally {
    rebaseState.loading = false;
  }
}

// ---------------------------------------------------------------------------
// G-FEAT-04: .gitignore template generation
// ---------------------------------------------------------------------------

export async function generateGitignore(projectType: string): Promise<void> {
  try {
    await gitService.createGitignore(projectType);
    pushOutput("git", "success", `.gitignore created (${projectType})`);
  } catch (e: unknown) {
    gitState.error = errorMessage(e);
    throw e;
  }
}

// ---------------------------------------------------------------------------
// 优先级 3: Git Stash / Tag / Amend
// ---------------------------------------------------------------------------

/** Stash 列表状态。 */
export interface StashState {
  stashes: StashEntry[];
  loading: boolean;
  error: string | null;
}

export const stashState = reactive<StashState>({
  stashes: [],
  loading: false,
  error: null,
});

/** Tag 列表状态。 */
export interface TagState {
  tags: TagEntry[];
  loading: boolean;
  error: string | null;
}

export const tagState = reactive<TagState>({
  tags: [],
  loading: false,
  error: null,
});

/** 加载 stash 列表。 */
export async function loadStashes(): Promise<void> {
  const generation = ++_stashGeneration;
  stashState.loading = true;
  stashState.error = null;
  try {
    const stashes = await gitService.stashList(currentRepoPath());
    if (generation !== _stashGeneration) return;
    stashState.stashes = stashes;
  } catch (e: unknown) {
    if (generation !== _stashGeneration) return;
    stashState.error = errorMessage(e);
  } finally {
    if (generation === _stashGeneration) {
      stashState.loading = false;
    }
  }
}

/** 执行 git stash push，保存当前工作区修改到新 stash。 */
export async function stashPush(message: string): Promise<void> {
  try {
    await gitService.stashPush(message);
    pushOutput("git", "success", `Stashed: ${message}`);
    await loadStashes();
    await refreshGit(currentRepoPath());
  } catch (e: unknown) {
    stashState.error = errorMessage(e);
    pushOutput("git", "error", `Stash failed: ${errorMessage(e)}`);
    throw e;
  }
}

/** 应用并移除指定 stash。 */
export async function stashPop(stashRef: string): Promise<void> {
  try {
    await gitService.stashPop(stashRef);
    pushOutput("git", "success", `Stash popped: ${stashRef}`);
    await loadStashes();
    await refreshGit(currentRepoPath());
  } catch (e: unknown) {
    stashState.error = errorMessage(e);
    throw e;
  }
}

/** 应用指定 stash 但不移除。 */
export async function stashApply(stashRef: string): Promise<void> {
  try {
    await gitService.stashApply(currentRepoPath(), stashRef);
    pushOutput("git", "success", `Stash applied: ${stashRef}`);
    await refreshGit(currentRepoPath());
  } catch (e: unknown) {
    stashState.error = errorMessage(e);
    throw e;
  }
}

/** 移除指定 stash。 */
export async function stashDrop(stashRef: string): Promise<void> {
  try {
    await gitService.stashDrop(currentRepoPath(), stashRef);
    pushOutput("git", "success", `Stash dropped: ${stashRef}`);
    await loadStashes();
  } catch (e: unknown) {
    stashState.error = errorMessage(e);
    throw e;
  }
}

/** 加载 tag 列表。 */
export async function loadTags(): Promise<void> {
  const generation = ++_tagGeneration;
  tagState.loading = true;
  tagState.error = null;
  try {
    const tags = await gitService.listTags();
    if (generation !== _tagGeneration) return;
    tagState.tags = tags;
  } catch (e: unknown) {
    if (generation !== _tagGeneration) return;
    tagState.error = errorMessage(e);
  } finally {
    if (generation === _tagGeneration) {
      tagState.loading = false;
    }
  }
}

/** 创建带注释的标签。 */
export async function createTag(name: string, message: string): Promise<void> {
  try {
    await gitService.createTag(name, message);
    pushOutput("git", "success", `Tag created: ${name}`);
    await loadTags();
  } catch (e: unknown) {
    tagState.error = errorMessage(e);
    throw e;
  }
}

/** 删除标签。 */
export async function deleteTag(name: string): Promise<void> {
  try {
    await gitService.deleteTag(name);
    pushOutput("git", "success", `Tag deleted: ${name}`);
    await loadTags();
  } catch (e: unknown) {
    tagState.error = errorMessage(e);
    throw e;
  }
}

/** 推送所有标签到远程仓库。 */
export async function pushTags(remote: string): Promise<void> {
  try {
    await gitService.pushTags(remote);
    pushOutput("git", "success", `Tags pushed to ${remote}`);
  } catch (e: unknown) {
    tagState.error = errorMessage(e);
    throw e;
  }
}

/** 修订最近一次提交（git commit --amend）。 */
export async function amendCommit(repoPath: string, message: string): Promise<void> {
  try {
    await gitService.amendCommit(message);
    pushOutput("git", "success", `Amended: ${message}`);
    await refreshGit(repoPath);
  } catch (e: unknown) {
    gitState.error = errorMessage(e);
    pushOutput("git", "error", `Amend failed: ${errorMessage(e)}`);
    throw e;
  }
}

/** 清空 stash/tag 状态。 */
export function clearStashAndTagState(): void {
  stashState.stashes = [];
  stashState.loading = false;
  stashState.error = null;
  tagState.tags = [];
  tagState.loading = false;
  tagState.error = null;
}

// ---------------------------------------------------------------------------
// F-4 (prompt-2.md): Submodule + Cherry-pick + Revert + Bisect
// ---------------------------------------------------------------------------

/** 子模块列表状态。 */
export interface SubmoduleState {
  submodules: SubmoduleInfo[];
  loading: boolean;
  error: string | null;
}

export const submoduleState = reactive<SubmoduleState>({
  submodules: [],
  loading: false,
  error: null,
});

/** Bisect 会话状态。 */
export interface BisectState {
  inProgress: boolean;
  goodHash: string;
  badHash: string;
  error: string | null;
}

export const bisectState = reactive<BisectState>({
  inProgress: false,
  goodHash: "",
  badHash: "",
  error: null,
});

/** 加载子模块列表。 */
export async function loadSubmodules(): Promise<void> {
  const generation = ++_submoduleGeneration;
  submoduleState.loading = true;
  submoduleState.error = null;
  try {
    const submodules = await gitService.submoduleList();
    if (generation !== _submoduleGeneration) return;
    submoduleState.submodules = submodules;
  } catch (e: unknown) {
    if (generation !== _submoduleGeneration) return;
    submoduleState.error = errorMessage(e);
  } finally {
    if (generation === _submoduleGeneration) {
      submoduleState.loading = false;
    }
  }
}

/** 添加子模块。 */
export async function submoduleAdd(url: string, path: string): Promise<void> {
  try {
    await gitService.submoduleAdd(url, path);
    pushOutput("git", "success", `Submodule added: ${path}`);
    await loadSubmodules();
  } catch (e: unknown) {
    submoduleState.error = errorMessage(e);
    pushOutput("git", "error", `Submodule add failed: ${errorMessage(e)}`);
    throw e;
  }
}

/** 更新子模块。 */
export async function submoduleUpdate(init: boolean): Promise<void> {
  try {
    await gitService.submoduleUpdate(init);
    pushOutput("git", "success", `Submodule updated${init ? " (init)" : ""}`);
    await loadSubmodules();
  } catch (e: unknown) {
    submoduleState.error = errorMessage(e);
    pushOutput("git", "error", `Submodule update failed: ${errorMessage(e)}`);
    throw e;
  }
}

/** 取消初始化子模块。 */
export async function submoduleDeinit(path: string): Promise<void> {
  try {
    await gitService.submoduleDeinit(path);
    pushOutput("git", "success", `Submodule deinit: ${path}`);
    await loadSubmodules();
  } catch (e: unknown) {
    submoduleState.error = errorMessage(e);
    pushOutput("git", "error", `Submodule deinit failed: ${errorMessage(e)}`);
    throw e;
  }
}

/** Cherry-pick 指定 commit。 */
export async function cherryPick(commitHash: string): Promise<void> {
  try {
    await gitService.cherryPick(commitHash);
    pushOutput("git", "success", `Cherry-picked: ${commitHash}`);
    await refreshGit(currentRepoPath());
  } catch (e: unknown) {
    pushOutput("git", "error", `Cherry-pick failed: ${errorMessage(e)}`);
    throw e;
  }
}

/** Revert 指定 commit。 */
export async function revertCommit(commitHash: string): Promise<void> {
  try {
    await gitService.revertCommit(commitHash);
    pushOutput("git", "success", `Reverted: ${commitHash}`);
    await refreshGit(currentRepoPath());
  } catch (e: unknown) {
    pushOutput("git", "error", `Revert failed: ${errorMessage(e)}`);
    throw e;
  }
}

/** 启动 bisect 会话。 */
export async function bisectStart(good: string, bad: string): Promise<void> {
  bisectState.error = null;
  try {
    await gitService.bisectStart(good, bad);
    bisectState.inProgress = true;
    bisectState.goodHash = good;
    bisectState.badHash = bad;
    pushOutput("git", "success", `Bisect started: good=${good}, bad=${bad}`);
  } catch (e: unknown) {
    bisectState.error = errorMessage(e);
    pushOutput("git", "error", `Bisect start failed: ${errorMessage(e)}`);
    throw e;
  }
}

/** 标记当前 commit 为 good。 */
export async function bisectGood(): Promise<void> {
  try {
    await gitService.bisectGood();
    pushOutput("git", "success", "Bisect: marked good");
  } catch (e: unknown) {
    bisectState.error = errorMessage(e);
    pushOutput("git", "error", `Bisect good failed: ${errorMessage(e)}`);
    throw e;
  }
}

/** 标记当前 commit 为 bad。 */
export async function bisectBad(): Promise<void> {
  try {
    await gitService.bisectBad();
    pushOutput("git", "success", "Bisect: marked bad");
  } catch (e: unknown) {
    bisectState.error = errorMessage(e);
    pushOutput("git", "error", `Bisect bad failed: ${errorMessage(e)}`);
    throw e;
  }
}

/** 结束 bisect 会话。 */
export async function bisectReset(): Promise<void> {
  try {
    await gitService.bisectReset();
    bisectState.inProgress = false;
    bisectState.goodHash = "";
    bisectState.badHash = "";
    pushOutput("git", "success", "Bisect reset");
  } catch (e: unknown) {
    bisectState.error = errorMessage(e);
    pushOutput("git", "error", `Bisect reset failed: ${errorMessage(e)}`);
    throw e;
  }
}

/** 清理 F-4 状态。 */
export function clearF4State(): void {
  submoduleState.submodules = [];
  submoduleState.loading = false;
  submoduleState.error = null;
  bisectState.inProgress = false;
  bisectState.goodHash = "";
  bisectState.badHash = "";
  bisectState.error = null;
}
