// Koyori IDE 模块 · Git；交互服务：Git 集成（GitService）。
// 喵，这是 Koyori IDE 的 Git 模块（前端实现）~
import * as GitServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/gitservice.js";
import type { BlameLine, CommitGraphEntry } from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/models.js";
import type { BranchInfo } from "@/types";
import { unwrapNullable } from "./boundary";

export type GitBlameLine = BlameLine;
export type GitCommitGraphEntry = CommitGraphEntry;

export const gitService = {
  getStatus: (path: string) =>
    unwrapNullable(GitServiceBindings.GetStatus(path), []),
  getBranchInfo: (path: string) =>
    GitServiceBindings.GetBranchInfo(path) as Promise<BranchInfo>,
  initRepo: (path: string) =>
    GitServiceBindings.InitRepo(path) as Promise<void>,
  discoverRepositories: (path: string) =>
    unwrapNullable(GitServiceBindings.DiscoverRepositories(path), []),
  stage: (path: string, filePath: string) =>
    GitServiceBindings.Stage(path, filePath) as Promise<void>,
  unstage: (path: string, filePath: string) =>
    GitServiceBindings.Unstage(path, filePath) as Promise<void>,
  commit: (path: string, message: string) =>
    GitServiceBindings.Commit(path, message) as Promise<void>,
  getDiff: (path: string, filePath: string) =>
    GitServiceBindings.GetDiff(path, filePath) as Promise<string>,
  // G-BLAME-01: inline git blame for editor decoration.
  getBlame: (path: string, filePath: string, startLine = 0, endLine = 0, revision = "") =>
    unwrapNullable(GitServiceBindings.GetBlameAtRevision(
      path,
      filePath,
      startLine,
      endLine,
      revision,
    ), []),
  getCommitGraph: (path: string, limit = 50, branch = "", all = false) =>
    unwrapNullable(GitServiceBindings.GetCommitGraph(path, limit, branch, all), []),
  getFullDiff: (path: string) =>
    GitServiceBindings.GetFullDiff(path) as Promise<string>,
  listBranches: (path: string) =>
    unwrapNullable(GitServiceBindings.ListBranches(path), []),
  createBranch: (path: string, name: string) =>
    GitServiceBindings.CreateBranch(path, name) as Promise<void>,
  checkoutBranch: (path: string, name: string) =>
    GitServiceBindings.CheckoutBranch(path, name) as Promise<void>,
  deleteBranch: (path: string, name: string) =>
    GitServiceBindings.DeleteBranch(path, name) as Promise<void>,
  push: (repoPath: string, remoteName = "") =>
    GitServiceBindings.Push(repoPath, remoteName) as Promise<void>,
  pull: (repoPath: string, remoteName = "") =>
    GitServiceBindings.Pull(repoPath, remoteName) as Promise<void>,
  // G-FEAT-04: .gitignore template generation.
  gitignoreTemplate: (projectType: string) =>
    GitServiceBindings.GitignoreTemplate(projectType) as Promise<string>,
  createGitignore: (projectType: string) =>
    GitServiceBindings.CreateGitignore(projectType) as Promise<void>,
  // G-FEAT-04: rebase / merge conflict support.
  rebase: (branch: string) =>
    GitServiceBindings.Rebase(branch) as Promise<string>,
  abortRebase: () =>
    GitServiceBindings.AbortRebase() as Promise<void>,
  continueRebase: () =>
    GitServiceBindings.ContinueRebase() as Promise<void>,
  isRebaseInProgress: () =>
    GitServiceBindings.IsRebaseInProgress() as Promise<boolean>,
  listMergeConflicts: () =>
    unwrapNullable(GitServiceBindings.ListMergeConflicts(), []),
  resolveConflict: (filePath: string) =>
    GitServiceBindings.ResolveConflict(filePath) as Promise<void>,
  // 优先级 3: Git Stash / Tag / Amend
  stashList: (repoPath: string) =>
    unwrapNullable(GitServiceBindings.StashList(repoPath), []),
  stashPush: (message: string) =>
    GitServiceBindings.StashPush(message) as Promise<void>,
  stashPop: (stashRef: string) =>
    GitServiceBindings.StashPop(stashRef) as Promise<void>,
  stashApply: (repoPath: string, stashRef: string) =>
    GitServiceBindings.StashApply(repoPath, stashRef) as Promise<void>,
  stashDrop: (repoPath: string, stashRef: string) =>
    GitServiceBindings.StashDrop(repoPath, stashRef) as Promise<void>,
  listTags: () =>
    unwrapNullable(GitServiceBindings.ListTags(), []),
  createTag: (name: string, message: string) =>
    GitServiceBindings.CreateTag(name, message) as Promise<void>,
  deleteTag: (name: string) =>
    GitServiceBindings.DeleteTag(name) as Promise<void>,
  pushTags: (remote: string) =>
    GitServiceBindings.PushTags(remote) as Promise<void>,
  amendCommit: (message: string) =>
    GitServiceBindings.AmendCommit(message) as Promise<void>,
  // F-4 (prompt-2.md): Submodule + Cherry-pick + Revert + Bisect
  submoduleAdd: (url: string, path: string) =>
    GitServiceBindings.SubmoduleAdd(url, path),
  submoduleList: () =>
    unwrapNullable(GitServiceBindings.SubmoduleList(), []),
  submoduleUpdate: (init: boolean) =>
    GitServiceBindings.SubmoduleUpdate(init),
  submoduleDeinit: (path: string) =>
    GitServiceBindings.SubmoduleDeinit(path),
  cherryPick: (commitHash: string) =>
    GitServiceBindings.CherryPick(commitHash),
  revertCommit: (commitHash: string) =>
    GitServiceBindings.RevertCommit(commitHash),
  bisectStart: (good: string, bad: string) =>
    GitServiceBindings.BisectStart(good, bad),
  bisectGood: () =>
    GitServiceBindings.BisectGood(),
  bisectBad: () =>
    GitServiceBindings.BisectBad(),
  bisectReset: () =>
    GitServiceBindings.BisectReset(),
};
