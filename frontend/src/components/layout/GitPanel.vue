<script setup lang="ts">
// Koyori IDE 组件 · Git Panel。
// 喵，这是 Git Panel，负责 Koyori IDE 的界面呈现喵~
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { ElMessage, ElMessageBox } from "element-plus";
import { appState } from "@/stores/app";
import {
  gitState,
  branchState,
  loadBranches,
  createBranch,
  checkoutBranch,
  refreshGit,
  discoverRepositories,
  initRepo,
  stageFile,
  unstageFile,
  loadMoreGitChanges,
  commitChanges,
  pushChanges,
  pullChanges,
  conflictState,
  rebaseState,
  loadConflicts,
  resolveConflictAsOurs,
  resolveConflictAsTheirs,
  markConflictResolved,
  startRebase,
  abortRebase,
  continueRebase,
  checkRebaseStatus,
  generateGitignore,
  clearConflictState,
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
  // F-4 (prompt-2.md): Submodule + Cherry-pick + Revert + Bisect
  submoduleState,
  bisectState,
  loadSubmodules,
  submoduleAdd,
  submoduleUpdate,
  submoduleDeinit,
  cherryPick,
  revertCommit,
  bisectStart,
  bisectGood,
  bisectBad,
  bisectReset,
  joinWorkspacePath,
} from "@/stores/git";
import { openFileFromPath } from "@/stores/editor";
import { ArrowDown, Plus, Minus, Check, Top, Bottom, Aim, Close, Refresh } from "@element-plus/icons-vue";
import DiffView from "@/components/editor/DiffView.vue";
import CommitGraph from "@/components/git/CommitGraph.vue";
import WorktreePanel from "@/components/git/WorktreePanel.vue";
import RebaseEditor from "@/components/git/RebaseEditor.vue";
import MarkdownContent from "@/components/common/MarkdownContent.vue";
import FocusTrapDialog from "@/components/common/FocusTrapDialog.vue";
import {
  reviewState,
  hasReview,
  runReview,
  clearReview,
} from "@/stores/review";
import { renderMarkdownWithApplyButtons } from "@/lib/markdown";
import { errorMessage, isCancellationError } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";
import type { MergeConflict } from "@/types";

const { t } = useI18n();

// G17: repo choices come from backend-authorized workspace roots and discovery,
// never from the .code-workspace display path. The primary root remains the
// default, while nested and secondary-root repositories can be selected.
const primaryWorkspacePath = computed(() => appState.workspaceRoot || appState.currentProject || "");
const workspaceRoots = computed(() => {
  const roots = appState.workspaceFolders.filter(Boolean);
  return roots.length > 0 ? roots : (primaryWorkspacePath.value ? [primaryWorkspacePath.value] : []);
});
const repositoryOptions = ref<string[]>([]);
const selectedRepoPath = ref("");
const repositoryDiscoveryLoading = ref(false);
const repoPath = computed(() => selectedRepoPath.value || primaryWorkspacePath.value);
const commitMessage = ref("");
let disposed = false;

async function loadRepositoryOptions(): Promise<void> {
  const roots = [...workspaceRoots.value];
  if (roots.length === 0 || disposed) {
    repositoryOptions.value = [];
    selectedRepoPath.value = "";
    return;
  }
  repositoryDiscoveryLoading.value = true;
  try {
    const discovered = await Promise.all(roots.map((root) => discoverRepositories(root)));
    if (disposed) return;
    const seen = new Set<string>();
    repositoryOptions.value = [...roots, ...discovered.flat()]
      .filter((path) => {
        if (!path || seen.has(path)) return false;
        seen.add(path);
        return true;
      });
    if (selectedRepoPath.value && !seen.has(selectedRepoPath.value)) {
      selectedRepoPath.value = "";
    }
  } finally {
    repositoryDiscoveryLoading.value = false;
  }
}

const currentBranchName = computed(() => {
  const head = branchState.branches.find((b) => b.isHead);
  return head?.name ?? gitState.branchName ?? "—";
});
const worktreeBranches = computed(() => branchState.branches.map((branch) => ({
  name: branch.name,
  isHead: branch.isHead,
})));

const diffVisible = ref(false);
const diffFilePath = ref("");
const diffFileStaged = ref(true);

type OperationDomain = "refresh" | "review";

const operationControllers = new Map<OperationDomain, AbortController>();

function beginOperation(domain: OperationDomain): AbortController | null {
  if (disposed) return null;
  operationControllers.get(domain)?.abort();
  const controller = new AbortController();
  operationControllers.set(domain, controller);
  return controller;
}

function isCurrentOperation(domain: OperationDomain, controller: AbortController): boolean {
  return !disposed && !controller.signal.aborted && operationControllers.get(domain) === controller;
}

function finishOperation(domain: OperationDomain, controller: AbortController): void {
  if (operationControllers.get(domain) === controller) {
    operationControllers.delete(domain);
  }
}

function cancelOperation(domain: OperationDomain): void {
  operationControllers.get(domain)?.abort();
  operationControllers.delete(domain);
}

function isAbortError(error: unknown): boolean {
  return error instanceof DOMException
    ? error.name === "AbortError"
    : typeof error === "object" && error !== null && "name" in error && error.name === "AbortError";
}

function viewDiff(filePath: string, staged: boolean) {
  if (disposed) return;
  diffFilePath.value = filePath;
  diffFileStaged.value = staged;
  diffVisible.value = true;
}

const hasChanges = computed(() => gitState.changes.length > 0);
const stagedChanges = computed(() => gitState.changes.filter((change) => change.staged === true));
const unstagedChanges = computed(() => gitState.changes.filter((change) => change.staged !== true));
const hasConflicts = computed(() => conflictState.conflicts.length > 0);
const isRebaseInProgress = computed(() => rebaseState.inProgress);
/** P1-04: rows still hidden behind the truncation window. */
const hiddenChangeCount = computed(() => Math.max(0, gitState.totalChanges - gitState.changes.length));

/** P1-04: a staged rename row owns both names — unstaging it must reset the
 * index for the added path and the deleted path, or the rename reappears as a
 * dangling deletion after refresh. */
async function handleUnstageChange(path: string, oldPath?: string) {
  if (!repoPath.value) return;
  await unstageFile(repoPath.value, path);
  if (oldPath) {
    await unstageFile(repoPath.value, oldPath);
  }
}

/** BUG2: Detect when the project directory is not a git repository.
 * Backend `errNotARepo` sentinel text is "not a git repository" (git_service.go).
 * Older paths also emit "repository does not exist" / "repository not found". */
const noRepo = computed(() =>
  !gitState.loading &&
  !!gitState.error &&
  /not a git repository|repository does not exist|repository not found/i.test(gitState.error),
);

async function handleInitRepo() {
  if (!repoPath.value || disposed) return;
  try {
    await ElMessageBox.confirm(
      t("git.initRepoConfirm"),
      t("git.initRepoTitle"),
      { confirmButtonText: t("git.initRepo"), cancelButtonText: t("common.cancel"), type: "info" },
    );
    if (disposed) return;
    await initRepo(repoPath.value);
    if (disposed) return;
    ElMessage.success(t("git.repoInitialized"));
  } catch (e: unknown) {
    if (disposed || isAbortError(e) || isCancellationError(e)) return;
    ElMessage.error(t("git.initRepoFailed", { error: errorMessage(e) }));
  }
}

async function handleRefresh() {
  const path = repoPath.value;
  if (!path || disposed) return;
  const controller = beginOperation("refresh");
  if (!controller) return;
  try {
    await refreshGit(path);
    if (!isCurrentOperation("refresh", controller)) return;
    await checkRebaseStatus();
    if (!isCurrentOperation("refresh", controller)) return;
    if (isRebaseInProgress.value) {
      await loadConflicts();
    }
  } catch (e: unknown) {
    if (controller.signal.aborted || isAbortError(e)) return;
    throw e;
  } finally {
    finishOperation("refresh", controller);
  }
}

async function handleStage(path: string) {
  if (!repoPath.value) return;
  await stageFile(repoPath.value, path);
}

async function handleCommit() {
  if (!repoPath.value || !commitMessage.value.trim()) return;
  await commitChanges(repoPath.value, commitMessage.value);
  commitMessage.value = "";
}

async function handlePush() {
  if (!repoPath.value) return;
  try {
    await pushChanges(repoPath.value);
    ElMessage.success(t("git.pushed"));
  } catch (e: unknown) {
    ElMessage.error(t("git.pushFailed", { error: errorMessage(e) }));
  }
}

async function handlePull() {
  if (!repoPath.value) return;
  try {
    await pullChanges(repoPath.value);
    ElMessage.success(t("git.pulled"));
  } catch (e: unknown) {
    ElMessage.error(t("git.pullFailed", { error: errorMessage(e) }));
  }
}

async function handleBranchCommand(name: string) {
  if (!repoPath.value) return;
  if (name === "__new__") {
    try {
      const { value } = await ElMessageBox.prompt(t("git.branchNamePrompt"), t("git.createBranchTitle"), {
        confirmButtonText: t("git.create"),
        cancelButtonText: t("common.cancel"),
        inputPattern: /^[A-Za-z0-9._\-/]+$/,
        inputErrorMessage: t("git.invalidBranchName"),
      });
      if (value) {
        await createBranch(repoPath.value, value);
        await checkoutBranch(repoPath.value, value);
        ElMessage.success(t("git.createdAndSwitched", { name: value }));
      }
    } catch {
      // user cancelled
    }
  } else {
    try {
      await checkoutBranch(repoPath.value, name);
      ElMessage.success(t("git.switched", { name }));
    } catch (e: unknown) {
      ElMessage.error(t("git.switchFailed", { error: errorMessage(e) }));
    }
  }
}

// --- G-FEAT-04: Rebase controls ---

async function handleRebaseCommand(cmd: string) {
  if (cmd === "__start__") {
    await handleStartRebase();
  }
}

async function handleStartRebase() {
  if (!repoPath.value) return;
  try {
    const { value } = await ElMessageBox.prompt(t("git.rebaseBranchPrompt"), t("git.rebaseTitle"), {
      confirmButtonText: t("git.startRebase"),
      cancelButtonText: t("common.cancel"),
      inputPattern: /^[A-Za-z0-9._\-/]+$/,
      inputErrorMessage: t("git.invalidBranchName"),
    });
    if (value) {
      await startRebase(value);
      if (isRebaseInProgress.value) {
        ElMessage.warning(t("git.rebaseInProgress"));
      } else {
        ElMessage.success(t("git.rebaseStarted", { branch: value }));
      }
      await refreshGit(repoPath.value);
    }
  } catch {
    // user cancelled or rebase failed
    if (rebaseState.error) {
      ElMessage.error(t("git.rebaseFailed", { error: rebaseState.error }));
    }
  }
}

async function handleAbortRebase() {
  try {
    await abortRebase();
    ElMessage.success(t("git.rebaseAborted"));
    if (repoPath.value) await refreshGit(repoPath.value);
  } catch (e: unknown) {
    ElMessage.error(t("git.rebaseFailed", { error: errorMessage(e) }));
  }
}

async function handleContinueRebase() {
  try {
    await continueRebase();
    ElMessage.success(t("git.rebaseContinued"));
    if (repoPath.value) await refreshGit(repoPath.value);
  } catch (e: unknown) {
    ElMessage.error(t("git.rebaseFailed", { error: errorMessage(e) }));
  }
}

// --- G-FEAT-04: Conflict resolution ---

const resolvingFile = ref<string | null>(null);

async function handleAcceptOurs(conflict: MergeConflict) {
  if (!repoPath.value) return;
  resolvingFile.value = conflict.file;
  try {
    await resolveConflictAsOurs(repoPath.value, conflict);
    ElMessage.success(t("git.conflictResolved", { file: conflict.file }));
  } catch (e: unknown) {
    ElMessage.error(t("git.conflictResolveFailed", { error: errorMessage(e) }));
  } finally {
    resolvingFile.value = null;
  }
}

async function handleAcceptTheirs(conflict: MergeConflict) {
  if (!repoPath.value) return;
  resolvingFile.value = conflict.file;
  try {
    await resolveConflictAsTheirs(repoPath.value, conflict);
    ElMessage.success(t("git.conflictResolved", { file: conflict.file }));
  } catch (e: unknown) {
    ElMessage.error(t("git.conflictResolveFailed", { error: errorMessage(e) }));
  } finally {
    resolvingFile.value = null;
  }
}

async function handleOpenEditor(conflict: MergeConflict) {
  if (!repoPath.value) return;
  // P19 P2: reuse the store's separator-normalizing join (M-29) instead of
  // bare "/" concatenation, which produces mixed separators on Windows roots.
  const fullPath = joinWorkspacePath(repoPath.value, conflict.file);
  await openFileFromPath(fullPath);
}

async function handleMarkResolved(file: string) {
  if (!repoPath.value) return;
  resolvingFile.value = file;
  try {
    await markConflictResolved(repoPath.value, file);
    ElMessage.success(t("git.conflictResolved", { file }));
  } catch (e: unknown) {
    ElMessage.error(t("git.conflictResolveFailed", { error: errorMessage(e) }));
  } finally {
    resolvingFile.value = null;
  }
}

// --- G-FEAT-04: .gitignore generation ---

function handleOverflowCommand(command: string) {
  if (command === "review") {
    openReviewModal();
    return;
  }
  if (command.startsWith("gitignore:")) {
    void handleGitignoreCommand(command.slice("gitignore:".length));
  }
}

async function handleGitignoreCommand(projectType: string) {
  try {
    await generateGitignore(projectType);
    ElMessage.success(t("git.gitignoreCreated"));
    if (repoPath.value) await refreshGit(repoPath.value);
  } catch (e: unknown) {
    const msg = errorMessage(e);
    if (msg.includes("already exists")) {
      ElMessage.warning(t("git.gitignoreExists"));
    } else {
      ElMessage.error(t("git.gitignoreFailed", { error: msg }));
    }
  }
}

// --- 优先级 3: Git Stash / Tag / Amend ---

// Amend 模式：开启后提交按钮变为「修订提交」。
const amendMode = ref(false);
// Stash 消息输入。
const stashMessage = ref("");
// Tag 名称与消息输入。
const tagName = ref("");
const tagMessage = ref("");
// 推送 tags 使用的远程名。
const pushTagsRemote = ref("origin");

async function handleStashPush() {
  if (!repoPath.value) return;
  try {
    await stashPush(stashMessage.value.trim());
    ElMessage.success(t("git.stashCreated"));
    stashMessage.value = "";
  } catch (e: unknown) {
    ElMessage.error(t("git.stashCreateFailed", { error: errorMessage(e) }));
  }
}

async function handleStashPop(stashRef: string) {
  try {
    await stashPop(stashRef);
    ElMessage.success(t("git.stashPopped"));
  } catch (e: unknown) {
    ElMessage.error(t("git.stashPopFailed", { error: errorMessage(e) }));
  }
}

async function handleStashApply(stashRef: string) {
  try {
    await stashApply(stashRef);
    ElMessage.success(t("git.stashApplied"));
  } catch (e: unknown) {
    ElMessage.error(t("git.stashApplyFailed", { error: errorMessage(e) }));
  }
}

async function handleStashDrop(stashRef: string) {
  try {
    await ElMessageBox.confirm(
      t("git.stashDropConfirm", { ref: stashRef }),
      t("git.stashDropTitle"),
      { confirmButtonText: t("common.confirm"), cancelButtonText: t("common.cancel"), type: "warning" },
    );
  } catch {
    return;
  }
  try {
    await stashDrop(stashRef);
    ElMessage.success(t("git.stashDropped"));
  } catch (e: unknown) {
    ElMessage.error(t("git.stashDropFailed", { error: errorMessage(e) }));
  }
}

async function handleCreateTag() {
  const name = tagName.value.trim();
  if (!name) return;
  try {
    await createTag(name, tagMessage.value.trim());
    ElMessage.success(t("git.tagCreated", { name }));
    tagName.value = "";
    tagMessage.value = "";
  } catch (e: unknown) {
    ElMessage.error(t("git.tagCreateFailed", { error: errorMessage(e) }));
  }
}

async function handleDeleteTag(name: string) {
  try {
    await ElMessageBox.confirm(
      t("git.tagDeleteConfirm", { name }),
      t("git.tagDeleteTitle"),
      { confirmButtonText: t("common.confirm"), cancelButtonText: t("common.cancel"), type: "warning" },
    );
  } catch {
    return;
  }
  try {
    await deleteTag(name);
    ElMessage.success(t("git.tagDeleted", { name }));
  } catch (e: unknown) {
    ElMessage.error(t("git.tagDeleteFailed", { error: errorMessage(e) }));
  }
}

async function handlePushTags() {
  const remote = pushTagsRemote.value.trim() || "origin";
  try {
    await pushTags(remote);
    ElMessage.success(t("git.tagsPushed", { remote }));
  } catch (e: unknown) {
    ElMessage.error(t("git.tagsPushFailed", { error: errorMessage(e) }));
  }
}

/** 提交或修订提交：根据 amendMode 切换调用 commitChanges 或 amendCommit。 */
async function handleCommitOrAmend() {
  if (!repoPath.value || !commitMessage.value.trim()) return;
  if (amendMode.value) {
    try {
      await amendCommit(repoPath.value, commitMessage.value);
      ElMessage.success(t("git.amended"));
      commitMessage.value = "";
      amendMode.value = false;
    } catch (e: unknown) {
      ElMessage.error(t("git.amendFailed", { error: errorMessage(e) }));
    }
  } else {
    await handleCommit();
  }
}

function statusLabel(status: string): string {
  switch (status) {
    case "Modified":
      return "M";
    case "Added":
      return "A";
    case "Deleted":
      return "D";
    case "Untracked":
      return "U";
    case "Renamed":
      return "R";
    default:
      return "?";
  }
}

// --- AI Code Review (#27) ---
const reviewModalVisible = ref(false);

function openReviewModal() {
  if (disposed) return;
  reviewModalVisible.value = true;
  // Auto-run review on first open if no result exists
  if (!hasReview.value && !reviewState.loading && !reviewState.error && repoPath.value) {
    void runLatestReview(false);
  }
}

function closeReviewModal() {
  if (disposed) return;
  reviewModalVisible.value = false;
}

async function runLatestReview(clearExisting: boolean) {
  const path = repoPath.value;
  if (!path || disposed) return;
  const controller = beginOperation("review");
  if (!controller) return;
  try {
    if (clearExisting) clearReview();
    await runReview(path);
    if (!isCurrentOperation("review", controller)) return;
  } catch (e: unknown) {
    if (controller.signal.aborted || isAbortError(e)) return;
    throw e;
  } finally {
    finishOperation("review", controller);
  }
}

async function handleRunReview() {
  await runLatestReview(true);
}

function renderReviewContent(content: string): string {
  return renderMarkdownWithApplyButtons(content);
}

function formatReviewTime(ts: number | null): string {
  if (!ts) return "";
  return new Date(ts).toLocaleString();
}

function statusClass(status: string): string {
  switch (status) {
    case "Modified":
      return "git-panel__status--modified";
    case "Added":
      return "git-panel__status--added";
    case "Deleted":
      return "git-panel__status--deleted";
    case "Untracked":
      return "git-panel__status--untracked";
    case "Renamed":
      return "git-panel__status--renamed";
    default:
      return "git-panel__status--default";
  }
}

onMounted(() => {
  void loadRepositoryOptions();
  if (repoPath.value) {
    void handleRefresh();
    loadBranches(repoPath.value);
    void loadStashes();
    void loadTags();
    void loadSubmodules();
  }
});

const stopWorkspaceRootsWatch = watch(
  () => [appState.workspaceRoot, appState.currentProject, ...appState.workspaceFolders],
  () => { void loadRepositoryOptions(); },
);

const stopRepoWatch = watch(repoPath, (newPath) => {
  if (newPath) {
    void handleRefresh();
    loadBranches(newPath);
    clearConflictState();
    void loadStashes();
    void loadTags();
    void loadSubmodules();
  } else {
    cancelOperation("refresh");
    clearConflictState();
    clearStashAndTagState();
  }
});

onBeforeUnmount(() => {
  disposed = true;
  stopRepoWatch();
  stopWorkspaceRootsWatch();
  for (const controller of operationControllers.values()) {
    controller.abort();
  }
  operationControllers.clear();
});

// ---------------------------------------------------------------------------
// F-4 (prompt-2.md): Submodule + Cherry-pick + Revert + Bisect
// ---------------------------------------------------------------------------

const submoduleAddUrl = ref("");
const submoduleAddPath = ref("");
const submoduleAddVisible = ref(false);
const cherryPickHash = ref("");
const revertHash = ref("");
const bisectGoodHash = ref("");
const bisectBadHash = ref("");

async function handleSubmoduleAdd(): Promise<void> {
  if (!submoduleAddUrl.value || !submoduleAddPath.value) {
    ElMessage.warning(t("git.f4SubmoduleAddRequired"));
    return;
  }
  try {
    await submoduleAdd(submoduleAddUrl.value, submoduleAddPath.value);
    submoduleAddUrl.value = "";
    submoduleAddPath.value = "";
    submoduleAddVisible.value = false;
    ElMessage.success(t("git.f4SubmoduleAdded"));
  } catch (e: unknown) {
    ElMessage.error(errorMessage(e));
  }
}

async function handleSubmoduleUpdate(init: boolean): Promise<void> {
  try {
    await submoduleUpdate(init);
    ElMessage.success(t("git.f4SubmoduleUpdated"));
  } catch (e: unknown) {
    ElMessage.error(errorMessage(e));
  }
}

async function handleSubmoduleDeinit(path: string): Promise<void> {
  try {
    await ElMessageBox.confirm(
      t("git.f4SubmoduleDeinitConfirm", { path }),
      t("common.confirm"),
      { type: "warning" },
    );
    await submoduleDeinit(path);
    ElMessage.success(t("git.f4SubmoduleDeinited"));
  } catch (e: unknown) {
    if (isCancellationError(e)) return;
    ElMessage.error(errorMessage(e));
  }
}

async function handleCherryPick(): Promise<void> {
  if (!cherryPickHash.value.trim()) {
    ElMessage.warning(t("git.f4HashRequired"));
    return;
  }
  try {
    await cherryPick(cherryPickHash.value.trim());
    cherryPickHash.value = "";
    ElMessage.success(t("git.f4CherryPicked"));
  } catch (e: unknown) {
    ElMessage.error(errorMessage(e));
  }
}

async function handleRevert(): Promise<void> {
  if (!revertHash.value.trim()) {
    ElMessage.warning(t("git.f4HashRequired"));
    return;
  }
  try {
    await revertCommit(revertHash.value.trim());
    revertHash.value = "";
    ElMessage.success(t("git.f4Reverted"));
  } catch (e: unknown) {
    ElMessage.error(errorMessage(e));
  }
}

async function handleBisectStart(): Promise<void> {
  if (!bisectGoodHash.value.trim() || !bisectBadHash.value.trim()) {
    ElMessage.warning(t("git.f4BisectHashRequired"));
    return;
  }
  try {
    await bisectStart(bisectGoodHash.value.trim(), bisectBadHash.value.trim());
    ElMessage.success(t("git.f4BisectStarted"));
  } catch (e: unknown) {
    ElMessage.error(errorMessage(e));
  }
}

async function handleBisectGood(): Promise<void> {
  try {
    await bisectGood();
  } catch (e: unknown) {
    ElMessage.error(errorMessage(e));
  }
}

async function handleBisectBad(): Promise<void> {
  try {
    await bisectBad();
  } catch (e: unknown) {
    ElMessage.error(errorMessage(e));
  }
}

async function handleBisectReset(): Promise<void> {
  try {
    await bisectReset();
    ElMessage.success(t("git.f4BisectReset"));
  } catch (e: unknown) {
    ElMessage.error(errorMessage(e));
  }
}
</script>

<template>
  <div class="git-panel">
    <div v-if="repositoryOptions.length > 1" class="git-panel__repository-picker">
      <label class="git-panel__repository-label" for="git-repository-select">
        {{ t('git.repository') }}
      </label>
      <select
        id="git-repository-select"
        v-model="selectedRepoPath"
        class="git-panel__repository-select"
        :disabled="repositoryDiscoveryLoading"
        :aria-label="t('git.repository')"
      >
        <option v-for="path in repositoryOptions" :key="path" :value="path">{{ path }}</option>
      </select>
    </div>
    <!-- Branch header -->
    <div class="git-panel__branch-bar">
      <el-dropdown trigger="click" @command="handleBranchCommand">
        <span class="git-panel__branch-current">
          <el-icon :size="12"><ArrowDown /></el-icon>
          {{ currentBranchName }}
        </span>
        <template #dropdown>
          <el-dropdown-menu>
            <el-dropdown-item
              v-for="b in branchState.branches"
              :key="b.name"
              :command="b.name"
              :disabled="b.isHead"
            >
              {{ b.name }}{{ b.isHead ? t('git.current') : "" }}
            </el-dropdown-item>
            <el-dropdown-item divided command="__new__">
              <el-icon><Plus /></el-icon> {{ t('git.newBranch') }}
            </el-dropdown-item>
          </el-dropdown-menu>
        </template>
      </el-dropdown>
      <span v-if="gitState.ahead > 0" class="git-panel__ahead" :title="t('git.ahead')">
        <el-icon :size="11"><Top /></el-icon>{{ gitState.ahead }}
      </span>
      <span v-if="gitState.behind > 0" class="git-panel__behind" :title="t('git.behind')">
        <el-icon :size="11"><Bottom /></el-icon>{{ gitState.behind }}
      </span>
      <button
        type="button"
        class="git-panel__action-btn"
        :aria-label="t('git.pullAria')"
        :title="t('git.pullTitle')"
        @click="handlePull"
      >
        <el-icon :size="13"><Bottom /></el-icon>
      </button>
      <button
        type="button"
        class="git-panel__action-btn"
        :aria-label="t('git.pushAria')"
        :title="t('git.pushTitle')"
        @click="handlePush"
      >
        <el-icon :size="13"><Top /></el-icon>
      </button>
      <button
        type="button"
        class="git-panel__refresh"
        :aria-label="t('git.refreshAria')"
        :title="t('git.refreshTitle')"
        @click="handleRefresh"
      >
        <el-icon :size="13"><Refresh /></el-icon>
      </button>
      <!-- G-FEAT-04: Rebase controls -->
      <el-dropdown trigger="click" @command="handleRebaseCommand" v-if="!isRebaseInProgress">
        <button
          type="button"
          class="git-panel__action-btn"
          :aria-label="t('git.rebaseTitle')"
          :title="t('git.rebaseTitle')"
          @click.stop
        >
          <el-icon :size="13"><ArrowDown /></el-icon>
        </button>
        <template #dropdown>
          <el-dropdown-menu>
            <el-dropdown-item command="__start__">{{ t('git.startRebase') }}</el-dropdown-item>
          </el-dropdown-menu>
        </template>
      </el-dropdown>
      <template v-else>
        <button
          type="button"
          class="git-panel__action-btn git-panel__action-btn--warning"
          :aria-label="t('git.abortRebase')"
          :title="t('git.abortRebase')"
          :disabled="rebaseState.loading"
          @click="handleAbortRebase"
        >
          <el-icon :size="13"><Close /></el-icon>
        </button>
        <button
          type="button"
          class="git-panel__action-btn git-panel__action-btn--success"
          :aria-label="t('git.continueRebase')"
          :title="t('git.continueRebase')"
          :disabled="rebaseState.loading"
          @click="handleContinueRebase"
        >
          <el-icon :size="13"><Check /></el-icon>
        </button>
      </template>
      <el-dropdown class="git-panel__overflow" trigger="click" @command="handleOverflowCommand">
        <button
          type="button"
          class="git-panel__action-btn git-panel__overflow-btn"
          :aria-label="t('git.moreActions')"
          :title="t('git.moreActions')"
          @click.stop
        >
          ···
        </button>
        <template #dropdown>
          <el-dropdown-menu>
            <el-dropdown-item command="review">{{ t('git.review') }}</el-dropdown-item>
            <el-dropdown-item divided disabled>{{ t('git.gitignoreTitle') }}</el-dropdown-item>
            <el-dropdown-item command="gitignore:go">{{ t('git.gitignoreTypeGo') }}</el-dropdown-item>
            <el-dropdown-item command="gitignore:typescript">{{ t('git.gitignoreTypeTypeScript') }}</el-dropdown-item>
            <el-dropdown-item command="gitignore:javascript">{{ t('git.gitignoreTypeJavaScript') }}</el-dropdown-item>
            <el-dropdown-item command="gitignore:general">{{ t('git.gitignoreTypeGeneral') }}</el-dropdown-item>
          </el-dropdown-menu>
        </template>
      </el-dropdown>
    </div>

    <!-- G-FEAT-04: Rebase in progress banner -->
    <div v-if="isRebaseInProgress" class="git-panel__rebase-banner">
      <span class="git-panel__rebase-indicator" />
      <span class="git-panel__rebase-text">{{ t('git.rebaseInProgress') }}</span>
    </div>

    <!-- G-FEAT-04: Merge conflict resolver -->
    <div v-if="hasConflicts" class="git-panel__conflicts">
      <div class="git-panel__section-header git-panel__conflicts-header">
        {{ t('git.conflicts', { count: conflictState.conflicts.length }) }}
      </div>
      <div
        v-for="conflict in conflictState.conflicts"
        :key="conflict.file"
        class="git-panel__conflict-row"
      >
        <span class="git-panel__conflict-path" :title="conflict.file">{{ conflict.file }}</span>
        <span class="git-panel__conflict-actions">
          <button
            type="button"
            class="git-panel__conflict-btn git-panel__conflict-btn--ours"
            :disabled="resolvingFile === conflict.file"
            @click="handleAcceptOurs(conflict)"
          >
            {{ t('git.acceptOurs') }}
          </button>
          <button
            type="button"
            class="git-panel__conflict-btn git-panel__conflict-btn--theirs"
            :disabled="resolvingFile === conflict.file"
            @click="handleAcceptTheirs(conflict)"
          >
            {{ t('git.acceptTheirs') }}
          </button>
          <button
            type="button"
            class="git-panel__conflict-btn"
            :disabled="resolvingFile === conflict.file"
            @click="handleOpenEditor(conflict)"
          >
            {{ t('git.openEditor') }}
          </button>
          <button
            type="button"
            class="git-panel__conflict-btn git-panel__conflict-btn--resolved"
            :disabled="resolvingFile === conflict.file"
            @click="handleMarkResolved(conflict.file)"
          >
            {{ t('git.markResolved') }}
          </button>
        </span>
      </div>
    </div>

    <!-- Commit message + button -->
    <div class="git-panel__commit-area">
      <textarea
        v-model="commitMessage"
        class="git-panel__commit-input"
        :placeholder="t('git.commitMessagePlaceholder')"
        rows="2"
        :aria-label="t('git.commitMessageAria')"
      />
      <div class="git-panel__commit-toolbar">
        <label class="git-panel__amend-toggle" :title="t('git.amendTitle')">
          <input
            v-model="amendMode"
            type="checkbox"
            class="git-panel__amend-checkbox"
          />
          <span>{{ t('git.amend') }}</span>
        </label>
        <button
          type="button"
          class="git-panel__commit-btn"
          :class="{ 'git-panel__commit-btn--amend': amendMode }"
          :disabled="!commitMessage.trim()"
          @click="handleCommitOrAmend"
        >
          <el-icon :size="12"><Check /></el-icon>
          {{ amendMode ? t('git.amendBtn') : t('git.commit') }}
        </button>
      </div>
    </div>

    <!-- Loading -->
    <div v-if="gitState.loading" class="git-panel__loading">
      {{ t('common.loading') }}
    </div>

    <!-- Error -->
    <div v-if="gitState.error && !noRepo" class="git-panel__error">
      {{ gitState.error }}
    </div>

    <!-- BUG2: No repository detected — offer to initialize one -->
    <div v-if="noRepo" class="git-panel__no-repo">
      <p class="git-panel__no-repo-text">{{ t("git.noRepo") }}</p>
      <button
        type="button"
        class="git-panel__init-btn"
        @click="handleInitRepo"
      >
        {{ t("git.initRepo") }}
      </button>
    </div>

    <div
      v-if="gitState.truncated"
      class="git-panel__truncated"
      role="status"
    >
      <span>{{ t('git.truncated', { shown: gitState.changes.length, max: 1000 }) }}</span>
      <button
        type="button"
        class="git-panel__load-more"
        data-testid="git-load-more"
        @click="loadMoreGitChanges()"
      >
        {{ t('git.loadMoreChanges', { count: hiddenChangeCount }) }}
      </button>
    </div>

    <!-- Changes list: staged vs unstaged -->
    <div v-if="!gitState.loading && hasChanges" class="git-panel__changes">
      <div v-if="stagedChanges.length > 0" class="git-panel__change-group" data-testid="git-staged">
        <div class="git-panel__section-header">{{ t('git.stagedCount', { count: stagedChanges.length }) }}</div>
        <div
          v-for="change in stagedChanges"
          :key="'staged:' + change.path"
          class="git-panel__row"
        >
          <span
            class="git-panel__path"
            :title="change.oldPath ? `${change.oldPath} → ${change.path}` : change.path"
          >{{ change.oldPath ? `${change.oldPath} → ${change.path}` : change.path }}</span>
          <span class="git-panel__actions">
            <button
              type="button"
              class="git-panel__action"
              :aria-label="t('git.unstage')"
              :title="t('git.unstage')"
              @click="handleUnstageChange(change.path, change.oldPath)"
            >
              <el-icon :size="12"><Minus /></el-icon>
            </button>
            <button
              type="button"
              class="git-panel__action"
              :aria-label="t('git.viewDiffAria')"
              :title="t('git.diff')"
              @click="viewDiff(change.path, true)"
            >
              {{ t('git.diff') }}
            </button>
          </span>
          <span class="git-panel__status" :class="statusClass(change.status)">
            {{ statusLabel(change.status) }}
          </span>
        </div>
      </div>
      <div v-if="unstagedChanges.length > 0" class="git-panel__change-group" data-testid="git-unstaged">
        <div class="git-panel__section-header">{{ t('git.changesCount', { count: unstagedChanges.length }) }}</div>
        <div
          v-for="change in unstagedChanges"
          :key="'unstaged:' + change.path"
          class="git-panel__row"
        >
          <span
            class="git-panel__path"
            :title="change.oldPath ? `${change.oldPath} → ${change.path}` : change.path"
          >{{ change.oldPath ? `${change.oldPath} → ${change.path}` : change.path }}</span>
          <span class="git-panel__actions">
            <button
              type="button"
              class="git-panel__action"
              :aria-label="t('git.stage')"
              :title="t('git.stage')"
              @click="handleStage(change.path)"
            >
              <el-icon :size="12"><Plus /></el-icon>
            </button>
            <button
              type="button"
              class="git-panel__action"
              :aria-label="t('git.viewDiffAria')"
              :title="t('git.diff')"
              @click="viewDiff(change.path, false)"
            >
              {{ t('git.diff') }}
            </button>
          </span>
          <span class="git-panel__status" :class="statusClass(change.status)">
            {{ statusLabel(change.status) }}
          </span>
        </div>
      </div>
    </div>

    <!-- Empty state -->
    <div v-if="!gitState.loading && !hasChanges && !gitState.error" class="git-panel__empty">
      {{ t('git.noChanges') }}
    </div>

    <details v-if="repoPath && !noRepo" class="git-panel__commit-graph">
      <summary>Commit graph</summary>
      <CommitGraph
        :repo-path="repoPath"
        :branch="currentBranchName === '—' ? '' : currentBranchName"
      />
    </details>

    <details v-if="repoPath && !noRepo" class="git-panel__advanced-git">
      <summary>{{ t('worktree.title') }}</summary>
      <WorktreePanel :repo-path="repoPath" :branches="worktreeBranches" />
    </details>

    <details v-if="repoPath && !noRepo" class="git-panel__advanced-git">
      <summary>{{ t('rebaseEditor.ariaLabel') }}</summary>
      <RebaseEditor :repo-path="repoPath" :auto-load="false" />
    </details>

    <!-- 优先级 3: Stash section -->
    <div class="git-panel__stash">
      <div class="git-panel__section-header">{{ t('git.stash', { count: stashState.stashes.length }) }}</div>
      <div class="git-panel__stash-input-row">
        <input
          v-model="stashMessage"
          type="text"
          class="git-panel__stash-input"
          :placeholder="t('git.stashMessagePlaceholder')"
          :aria-label="t('git.stashMessageAria')"
        />
        <button
          type="button"
          class="git-panel__stash-btn"
          :disabled="!repoPath"
          @click="handleStashPush"
        >
          {{ t('git.stashPush') }}
        </button>
      </div>
      <div
        v-for="entry in stashState.stashes"
        :key="entry.ref"
        class="git-panel__stash-row"
      >
        <span class="git-panel__stash-ref" :title="entry.ref">{{ entry.ref }}</span>
        <span class="git-panel__stash-msg" :title="entry.message">{{ entry.message }}</span>
        <span class="git-panel__stash-actions">
          <button
            type="button"
            class="git-panel__stash-action"
            :title="t('git.stashPopTitle')"
            @click="handleStashPop(entry.ref)"
          >{{ t('git.stashPopBtn') }}</button>
          <button
            type="button"
            class="git-panel__stash-action"
            :title="t('git.stashApplyTitle')"
            @click="handleStashApply(entry.ref)"
          >{{ t('git.stashApplyBtn') }}</button>
          <button
            type="button"
            class="git-panel__stash-action git-panel__stash-action--danger"
            :title="t('git.stashDropTitle')"
            @click="handleStashDrop(entry.ref)"
          >{{ t('git.stashDropBtn') }}</button>
        </span>
      </div>
      <div v-if="!stashState.loading && stashState.stashes.length === 0" class="git-panel__stash-empty">
        {{ t('git.stashEmpty') }}
      </div>
    </div>

    <!-- 优先级 3: Tags section -->
    <div class="git-panel__tags">
      <div class="git-panel__section-header">{{ t('git.tags', { count: tagState.tags.length }) }}</div>
      <div class="git-panel__tags-input-row">
        <input
          v-model="tagName"
          type="text"
          class="git-panel__tags-input"
          :placeholder="t('git.tagNamePlaceholder')"
          :aria-label="t('git.tagNameAria')"
        />
        <input
          v-model="tagMessage"
          type="text"
          class="git-panel__tags-input"
          :placeholder="t('git.tagMessagePlaceholder')"
          :aria-label="t('git.tagMessageAria')"
        />
        <button
          type="button"
          class="git-panel__tags-btn"
          :disabled="!tagName.trim()"
          @click="handleCreateTag"
        >
          {{ t('git.tagCreate') }}
        </button>
      </div>
      <div
        v-for="tag in tagState.tags"
        :key="tag.name"
        class="git-panel__tags-row"
      >
        <span class="git-panel__tags-name" :title="tag.name">{{ tag.name }}</span>
        <span class="git-panel__tags-msg" :title="tag.message">{{ tag.message }}</span>
        <span class="git-panel__tags-actions">
          <button
            type="button"
            class="git-panel__tags-action git-panel__tags-action--danger"
            :title="t('git.tagDeleteTitle')"
            @click="handleDeleteTag(tag.name)"
          >{{ t('git.tagDeleteBtn') }}</button>
        </span>
      </div>
      <div v-if="!tagState.loading && tagState.tags.length === 0" class="git-panel__tags-empty">
        {{ t('git.tagsEmpty') }}
      </div>
      <div class="git-panel__tags-push-row">
        <input
          v-model="pushTagsRemote"
          type="text"
          class="git-panel__tags-remote"
          :placeholder="t('git.tagsRemotePlaceholder')"
          :aria-label="t('git.tagsRemoteAria')"
        />
        <button
          type="button"
          class="git-panel__tags-push-btn"
          :disabled="tagState.tags.length === 0"
          @click="handlePushTags"
        >
          {{ t('git.tagsPush') }}
        </button>
      </div>
    </div>

    <!-- F-4 (prompt-2.md): Submodule + Cherry-pick + Revert + Bisect -->
    <div class="git-panel__advanced">
      <button
        type="button"
        class="git-panel__advanced-header"
        :aria-expanded="submoduleAddVisible"
        @click="submoduleAddVisible = !submoduleAddVisible"
      >
        <span class="git-panel__advanced-title">{{ t('git.f4AdvancedTitle') }}</span>
        <el-icon :size="14"><ArrowDown /></el-icon>
      </button>

      <!-- Submodule 区块 -->
      <div class="git-panel__submodule">
        <div class="git-panel__section-label">{{ t('git.f4SubmoduleTitle') }}</div>
        <div class="git-panel__submodule-actions">
          <button class="git-panel__btn git-panel__btn--small" @click="submoduleAddVisible = !submoduleAddVisible">
            {{ t('git.f4SubmoduleAdd') }}
          </button>
          <button class="git-panel__btn git-panel__btn--small" @click="handleSubmoduleUpdate(false)">
            {{ t('git.f4SubmoduleUpdate') }}
          </button>
          <button class="git-panel__btn git-panel__btn--small" @click="handleSubmoduleUpdate(true)">
            {{ t('git.f4SubmoduleInit') }}
          </button>
        </div>
        <div v-if="submoduleAddVisible" class="git-panel__submodule-add">
          <input
            v-model="submoduleAddUrl"
            class="git-panel__input"
            :placeholder="t('git.f4SubmoduleUrlPlaceholder')"
          />
          <input
            v-model="submoduleAddPath"
            class="git-panel__input"
            :placeholder="t('git.f4SubmodulePathPlaceholder')"
          />
          <button class="git-panel__btn git-panel__btn--primary git-panel__btn--small" @click="handleSubmoduleAdd">
            {{ t('common.add') }}
          </button>
        </div>
        <div v-if="submoduleState.loading" class="git-panel__submodule-loading">
          {{ t('common.loading') }}
        </div>
        <div v-else-if="submoduleState.submodules.length === 0" class="git-panel__submodule-empty">
          {{ t('git.f4SubmoduleEmpty') }}
        </div>
        <div v-else class="git-panel__submodule-list">
          <div
            v-for="sub in submoduleState.submodules"
            :key="sub.path"
            class="git-panel__submodule-item"
          >
            <span class="git-panel__submodule-sha">{{ sub.sha.substring(0, 8) }}</span>
            <span class="git-panel__submodule-path">{{ sub.path }}</span>
            <span v-if="!sub.initialized" class="git-panel__submodule-badge git-panel__submodule-badge--uninit">
              {{ t('git.f4SubmoduleUninit') }}
            </span>
            <span v-else-if="sub.modified" class="git-panel__submodule-badge git-panel__submodule-badge--modified">
              {{ t('git.f4SubmoduleModified') }}
            </span>
            <button
              class="git-panel__btn git-panel__btn--small git-panel__btn--danger"
              @click="handleSubmoduleDeinit(sub.path)"
            >
              {{ t('git.f4SubmoduleDeinit') }}
            </button>
          </div>
        </div>
        <div v-if="submoduleState.error" class="git-panel__submodule-error">
          {{ submoduleState.error }}
        </div>
      </div>

      <!-- Cherry-pick / Revert 区块 -->
      <div class="git-panel__cherry-revert">
        <div class="git-panel__section-label">{{ t('git.f4CherryRevertTitle') }}</div>
        <div class="git-panel__cherry-revert-row">
          <input
            v-model="cherryPickHash"
            class="git-panel__input"
            :placeholder="t('git.f4HashPlaceholder')"
          />
          <button class="git-panel__btn git-panel__btn--small" @click="handleCherryPick">
            {{ t('git.f4CherryPick') }}
          </button>
        </div>
        <div class="git-panel__cherry-revert-row">
          <input
            v-model="revertHash"
            class="git-panel__input"
            :placeholder="t('git.f4HashPlaceholder')"
          />
          <button class="git-panel__btn git-panel__btn--small" @click="handleRevert">
            {{ t('git.f4Revert') }}
          </button>
        </div>
      </div>

      <!-- Bisect 区块 -->
      <div class="git-panel__bisect">
        <div class="git-panel__section-label">{{ t('git.f4BisectTitle') }}</div>
        <template v-if="!bisectState.inProgress">
          <div class="git-panel__bisect-row">
            <input
              v-model="bisectGoodHash"
              class="git-panel__input"
              :placeholder="t('git.f4BisectGoodPlaceholder')"
            />
            <input
              v-model="bisectBadHash"
              class="git-panel__input"
              :placeholder="t('git.f4BisectBadPlaceholder')"
            />
            <button class="git-panel__btn git-panel__btn--small git-panel__btn--primary" @click="handleBisectStart">
              {{ t('git.f4BisectStart') }}
            </button>
          </div>
        </template>
        <template v-else>
          <div class="git-panel__bisect-controls">
            <button class="git-panel__btn git-panel__btn--small git-panel__btn--success" @click="handleBisectGood">
              {{ t('git.f4BisectGood') }}
            </button>
            <button class="git-panel__btn git-panel__btn--small git-panel__btn--danger" @click="handleBisectBad">
              {{ t('git.f4BisectBad') }}
            </button>
            <button class="git-panel__btn git-panel__btn--small" @click="handleBisectReset">
              {{ t('git.f4BisectReset') }}
            </button>
          </div>
        </template>
        <div v-if="bisectState.error" class="git-panel__bisect-error">
          {{ bisectState.error }}
        </div>
      </div>
    </div>

    <DiffView
      :repo-path="repoPath"
      :file-path="diffFilePath"
      :staged="diffFileStaged"
      :visible="diffVisible"
      @close="diffVisible = false"
    />

    <!-- AI Code Review modal (#27) -->
    <transition name="fade">
      <div
          v-if="reviewModalVisible"
          class="review-modal-overlay"
        >
        <button
          type="button"
          class="dialog-backdrop-button"
          tabindex="-1"
          :aria-label="t('a11y.closeDialog')"
          @click="closeReviewModal"
        />
        <FocusTrapDialog
          class="review-modal"
          :aria-label="t('git.reviewAria')"
          @close="closeReviewModal"
        >
          <div class="review-modal__header">
            <div class="review-modal__header-left">
              <el-icon :size="14"><Aim /></el-icon>
              <span class="review-modal__title">{{ t('git.reviewTitle') }}</span>
              <span v-if="reviewState.reviewedFiles.length > 0" class="review-modal__file-count">
                {{ t('git.reviewedFilesCount', { count: reviewState.reviewedFiles.length }) }}
              </span>
            </div>
            <div class="review-modal__header-right">
              <button
                type="button"
                class="review-modal__rerun"
                :disabled="reviewState.loading || !repoPath"
                :title="t('git.rerunTitle')"
                @click="handleRunReview"
              >
                {{ reviewState.loading ? t('git.reviewing') : t('git.rerun') }}
              </button>
              <button
                type="button"
                class="review-modal__close"
                :aria-label="t('git.closeReviewAria')"
                @click="closeReviewModal"
              >
                <el-icon :size="14"><Close /></el-icon>
              </button>
            </div>
          </div>
          <div class="review-modal__body">
            <!-- Loading -->
            <div v-if="reviewState.loading" class="review-modal__loading">
              <div class="review-modal__spinner" />
              <p>{{ t('git.analyzingChanges') }}</p>
            </div>

            <!-- Error -->
            <div v-else-if="reviewState.error" class="review-modal__error">
              <p>{{ reviewState.error }}</p>
              <button
                type="button"
                v-if="repoPath"
                class="review-modal__retry"
                @click="handleRunReview"
              >{{ t('common.retry') }}</button>
            </div>

            <!-- Result -->
            <div v-else-if="hasReview" class="review-modal__result">
              <div v-if="reviewState.reviewedFiles.length > 0" class="review-modal__files">
                <span class="review-modal__files-label">{{ t('git.reviewed') }}</span>
                <span
                  v-for="f in reviewState.reviewedFiles"
                  :key="f"
                  class="review-modal__file-chip"
                  :title="f"
                >{{ f.split('/').pop() }}</span>
              </div>
              <MarkdownContent
                class="review-modal__content markdown-body"
                :html="renderReviewContent(reviewState.result!)"
              />
              <div v-if="reviewState.reviewedAt" class="review-modal__timestamp">
                {{ t('git.reviewedAt', { time: formatReviewTime(reviewState.reviewedAt) }) }}
              </div>
            </div>

            <!-- Empty (no review run yet) -->
            <div v-else class="review-modal__empty">
              <p>{{ t('git.noReviewYet') }}</p>
            </div>
          </div>
        </FocusTrapDialog>
      </div>
    </transition>
  </div>
</template>

<style scoped>
.git-panel {
  display: flex;
  flex-direction: column;
  height: 100%;
  font-family: var(--font-sans);
}

.git-panel__branch-bar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
  padding: 8px 12px;
  border-bottom: 1px solid var(--color-border-subtle);
  min-width: 0;
}

.git-panel__branch-label {
  font-size: 12px;
  font-weight: 500;
  color: var(--color-text-secondary);
}

.git-panel__branch-current {
  display: flex;
  align-items: center;
  gap: 4px;
  min-width: 0;
  max-width: 140px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 12px;
  color: var(--color-text-secondary);
  cursor: pointer;
  padding: 2px 6px;
  border-radius: var(--radius-sm);
  transition: background-color var(--transition-fast);
}

.git-panel__branch-current:hover {
  background-color: var(--color-bg-surface-container-low);
}

.git-panel__ahead,
.git-panel__behind {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  font-size: 10px;
  color: var(--color-text-tertiary);
}

.git-panel__action-btn {
  margin-left: 0;
  flex-shrink: 0;
  border: none;
  background: transparent;
  color: var(--color-text-tertiary);
  cursor: pointer;
  line-height: 1;
  padding: 2px 4px;
  border-radius: var(--radius-sm);
  transition: background-color var(--transition-fast);
}

.git-panel__overflow {
  margin-left: auto;
}

.git-panel__action-btn + .git-panel__action-btn,
.git-panel__action-btn + .git-panel__refresh,
.git-panel__refresh + .git-panel__action-btn {
  margin-left: 0;
}

.git-panel__action-btn:hover {
  color: var(--color-text-primary);
  background-color: var(--color-bg-surface-container-low);
}

.git-panel__refresh {
  border: none;
  background: transparent;
  color: var(--color-text-tertiary);
  cursor: pointer;
  font-size: 14px;
  line-height: 1;
  padding: 2px 4px;
  border-radius: var(--radius-sm);
  transition: background-color var(--transition-fast);
}

.git-panel__refresh:hover {
  color: var(--color-text-primary);
  background-color: var(--color-bg-surface-container-low);
}

.git-panel__commit-area {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 10px 16px;
  border-bottom: 1px solid var(--color-border-subtle);
}

.git-panel__commit-input {
  width: 100%;
  padding: 8px 10px;
  font-size: 12px;
  font-family: var(--font-sans);
  color: var(--color-text-primary);
  background-color: var(--color-bg-elevated);
  border: 1px solid var(--color-border-subtle);
  border-radius: var(--radius-md);
  outline: none;
  resize: vertical;
  transition: border-color var(--transition-fast);
}

.git-panel__commit-input:focus {
  border-color: var(--color-primary);
}

.git-panel__commit-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 4px;
  padding: 6px 16px;
  font-size: 12px;
  font-weight: 500;
  color: var(--color-on-primary);
  background-color: var(--color-primary);
  border: none;
  border-radius: var(--radius-md);
  cursor: pointer;
  transition: background-color var(--transition-fast);
}

.git-panel__commit-btn:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}

.git-panel__commit-btn:not(:disabled):hover {
  background-color: color-mix(in srgb, var(--color-primary) 85%, #000);
}

/* 优先级 3: Commit toolbar with Amend toggle */
.git-panel__commit-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.git-panel__amend-toggle {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-size: 11px;
  color: var(--color-text-tertiary);
  cursor: pointer;
  user-select: none;
}

.git-panel__amend-checkbox {
  margin: 0;
  cursor: pointer;
}

.git-panel__commit-btn--amend {
  background-color: var(--color-warning, #f59e0b);
}

.git-panel__commit-btn--amend:not(:disabled):hover {
  background-color: color-mix(in srgb, var(--color-warning, #f59e0b) 85%, #000);
}

/* 优先级 3: Stash section */
.git-panel__stash {
  border-top: 1px solid var(--color-border-subtle);
  padding-bottom: 6px;
}

.git-panel__stash-input-row,
.git-panel__tags-input-row {
  display: flex;
  gap: 4px;
  padding: 4px 16px;
}

.git-panel__stash-input,
.git-panel__tags-input,
.git-panel__tags-remote {
  flex: 1;
  min-width: 0;
  padding: 4px 8px;
  font-size: 11px;
  font-family: var(--font-sans);
  color: var(--color-text-primary);
  background-color: var(--color-bg-elevated);
  border: 1px solid var(--color-border-subtle);
  border-radius: var(--radius-sm);
  outline: none;
  transition: border-color var(--transition-fast);
}

.git-panel__stash-input:focus,
.git-panel__tags-input:focus,
.git-panel__tags-remote:focus {
  border-color: var(--color-primary);
}

.git-panel__stash-btn,
.git-panel__tags-btn,
.git-panel__tags-push-btn {
  padding: 4px 10px;
  font-size: 11px;
  font-weight: 500;
  color: var(--color-on-primary);
  background-color: var(--color-primary);
  border: none;
  border-radius: var(--radius-sm);
  cursor: pointer;
  white-space: nowrap;
  transition: background-color var(--transition-fast);
}

.git-panel__stash-btn:disabled,
.git-panel__tags-btn:disabled,
.git-panel__tags-push-btn:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}

.git-panel__stash-btn:not(:disabled):hover,
.git-panel__tags-btn:not(:disabled):hover,
.git-panel__tags-push-btn:not(:disabled):hover {
  background-color: color-mix(in srgb, var(--color-primary) 85%, #000);
}

.git-panel__stash-row,
.git-panel__tags-row {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 3px 16px;
  font-size: 11px;
  border-radius: var(--radius-sm);
  transition: background-color var(--transition-fast);
}

.git-panel__stash-row:hover,
.git-panel__tags-row:hover {
  background: var(--color-bg-surface-container-low);
}

.git-panel__stash-ref,
.git-panel__tags-name {
  flex-shrink: 0;
  font-family: var(--font-mono);
  color: var(--color-text-primary);
  max-width: 90px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.git-panel__stash-msg,
.git-panel__tags-msg {
  flex: 1;
  color: var(--color-text-tertiary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.git-panel__stash-actions,
.git-panel__tags-actions {
  display: flex;
  gap: 3px;
  flex-shrink: 0;
  opacity: 0;
  transition: opacity var(--transition-fast);
}

.git-panel__stash-row:hover .git-panel__stash-actions,
.git-panel__tags-row:hover .git-panel__tags-actions,
.git-panel__stash-row:focus-within .git-panel__stash-actions,
.git-panel__tags-row:focus-within .git-panel__tags-actions {
  opacity: 1;
}

.git-panel__stash-action,
.git-panel__tags-action {
  padding: 2px 6px;
  font-size: 10px;
  font-family: var(--font-sans);
  color: var(--color-text-tertiary);
  background: var(--color-bg-surface-container-low);
  border: 1px solid var(--color-border-subtle);
  border-radius: var(--radius-xs);
  cursor: pointer;
  white-space: nowrap;
  transition: all var(--transition-fast);
}

.git-panel__stash-action:hover:not(:disabled),
.git-panel__tags-action:hover:not(:disabled) {
  color: var(--color-text-primary);
  background: var(--color-bg-surface-container);
}

.git-panel__stash-action--danger:hover:not(:disabled),
.git-panel__tags-action--danger:hover:not(:disabled) {
  color: var(--color-error, #ef4444);
  border-color: var(--color-error, #ef4444);
}

.git-panel__stash-empty,
.git-panel__tags-empty {
  padding: 4px 16px 6px;
  font-size: 11px;
  color: var(--color-text-disabled);
}

/* 优先级 3: Tags section */
.git-panel__tags {
  border-top: 1px solid var(--color-border-subtle);
  padding-bottom: 6px;
}

.git-panel__tags-push-row {
  display: flex;
  gap: 4px;
  padding: 4px 16px 6px;
}

.git-panel__tags-remote {
  max-width: 100px;
}

.git-panel__loading,
.git-panel__empty,
.git-panel__error {
  padding: 12px;
  font-size: 11px;
  color: var(--color-text-tertiary);
}

.git-panel__error {
  color: var(--color-error);
}

/* BUG2: No repository detected */
.git-panel__no-repo {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 12px;
  padding: 24px 16px;
  text-align: center;
}

.git-panel__no-repo-text {
  font-size: 12px;
  color: var(--color-text-tertiary);
  margin: 0;
  line-height: 1.5;
}

.git-panel__init-btn {
  padding: 6px 20px;
  font-size: 12px;
  font-weight: 500;
  color: var(--color-on-primary);
  background-color: var(--color-primary);
  border: none;
  border-radius: var(--radius-md);
  cursor: pointer;
  transition: background-color var(--transition-fast);
}

.git-panel__init-btn:hover {
  background-color: color-mix(in srgb, var(--color-primary) 85%, #000);
}

.git-panel__section-header {
  padding: 6px 16px 4px;
  font-size: 10px;
  font-weight: 500;
  text-transform: uppercase;
  letter-spacing: 0.5px;
  color: var(--color-text-tertiary);
}

.git-panel__commit-graph {
  border-top: 1px solid var(--color-border-default);
  border-bottom: 1px solid var(--color-border-default);
}

.git-panel__commit-graph > summary {
  min-height: 30px;
  padding: 7px 10px;
  color: var(--color-text-secondary);
  font-size: 11px;
  font-weight: 600;
  cursor: pointer;
  box-sizing: border-box;
}

.git-panel__commit-graph > summary:hover {
  color: var(--color-text-primary);
  background: var(--color-bg-surface-container);
}

.git-panel__commit-graph :deep(.commit-graph__title) {
  display: none;
}

.git-panel__commit-graph :deep(.commit-graph__header) {
  justify-content: flex-end;
}

.git-panel__advanced-git {
  border-bottom: 1px solid var(--color-border-default);
}

.git-panel__advanced-git > summary {
  cursor: pointer;
  padding: 7px 10px;
  color: var(--color-text-secondary);
  font-size: 11px;
  font-weight: 600;
  text-transform: uppercase;
  user-select: none;
}

.git-panel__advanced-git > summary:hover {
  background: var(--color-bg-hover);
  color: var(--color-text-primary);
}

.git-panel__changes {
  flex: 1;
  overflow-y: auto;
}

.git-panel__row {
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 3px 16px;
  height: 26px;
  font-size: 12px;
  cursor: default;
  border-radius: var(--radius-sm);
  transition: background-color var(--transition-fast);
}

.git-panel__row:hover {
  background: var(--color-bg-surface-container-low);
}

.git-panel__path {
  flex: 1;
  color: var(--color-text-primary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.git-panel__actions {
  display: flex;
  gap: 2px;
  opacity: 0;
  transition: opacity var(--transition-fast);
}

.git-panel__row:hover .git-panel__actions,
.git-panel__row:focus-within .git-panel__actions {
  opacity: 1;
}

.git-panel__truncated {
  padding: 6px 16px;
  font-size: 11px;
  color: var(--color-text-secondary);
  background: var(--color-bg-surface-container-low);
}

/* P1-04: proven staged renames get their own accent so "R" rows are
 * distinguishable from add/delete pairs at a glance. */
.git-panel__status--renamed {
  color: var(--color-accent, #7aa2f7);
}

.git-panel__load-more {
  margin-left: 8px;
  padding: 2px 8px;
  font-size: 11px;
  color: var(--color-text-primary);
  background: transparent;
  border: 1px solid var(--color-border-subtle);
  border-radius: 4px;
  cursor: pointer;
}

.git-panel__load-more:hover {
  background: var(--color-bg-surface-container);
}

.git-panel__action {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 22px;
  height: 22px;
  border: none;
  background: transparent;
  color: var(--color-text-tertiary);
  cursor: pointer;
  border-radius: var(--radius-xs);
  transition: color var(--transition-fast), background-color var(--transition-fast);
}

.git-panel__action:hover {
  color: var(--color-text-primary);
  background-color: var(--color-bg-surface-container-low);
}

.git-panel__status {
  width: 16px;
  text-align: center;
  font-weight: 500;
  font-size: 11px;
}

.git-panel__status--modified { color: var(--color-warning); }
.git-panel__status--added { color: var(--color-success); }
.git-panel__status--deleted { color: var(--color-error); }
.git-panel__status--untracked { color: var(--color-text-disabled); }
.git-panel__status--default { color: var(--color-text-tertiary); }

/* G-FEAT-04: Rebase button variants */
.git-panel__action-btn--warning {
  color: var(--color-error, #ef4444);
}
.git-panel__action-btn--warning:hover {
  background-color: color-mix(in srgb, var(--color-error, #ef4444) 12%, transparent);
}
.git-panel__action-btn--success {
  color: var(--color-success, #22c55e);
}
.git-panel__action-btn--success:hover {
  background-color: color-mix(in srgb, var(--color-success, #22c55e) 12%, transparent);
}

/* G-FEAT-04: Rebase in progress banner */
.git-panel__rebase-banner {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 4px 16px;
  background-color: color-mix(in srgb, var(--color-warning, #f59e0b) 10%, transparent);
  border-bottom: 1px solid var(--color-border-subtle);
}

.git-panel__rebase-indicator {
  display: inline-block;
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background-color: var(--color-warning, #f59e0b);
  animation: git-pulse 1.4s ease-in-out infinite;
}

@keyframes git-pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.4; }
}

@media (prefers-reduced-motion: reduce) {
  .git-panel__rebase-indicator { animation: none; }
}

.git-panel__rebase-text {
  font-size: 11px;
  color: var(--color-warning, #f59e0b);
  font-weight: 500;
}

/* G-FEAT-04: Merge conflict resolver */
.git-panel__conflicts {
  border-bottom: 1px solid var(--color-border-subtle);
}

.git-panel__conflicts-header {
  color: var(--color-error, #ef4444);
}

.git-panel__conflict-row {
  flex-wrap: wrap;
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 4px 16px;
  font-size: 12px;
  border-bottom: 1px solid var(--color-border-subtle, transparent);
}

.git-panel__conflict-row:last-child {
  border-bottom: none;
}

.git-panel__conflict-path {
  flex: 1;
  color: var(--color-text-primary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  font-family: var(--font-mono);
  font-size: 11px;
}

.git-panel__conflict-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 3px;
  flex-shrink: 1;
}

.git-panel__conflict-btn {
  padding: 2px 6px;
  font-size: 10px;
  font-family: var(--font-sans);
  color: var(--color-text-tertiary);
  background: var(--color-bg-surface-container-low);
  border: 1px solid var(--color-border-subtle);
  border-radius: var(--radius-xs);
  cursor: pointer;
  transition: all var(--transition-fast);
  white-space: nowrap;
}

.git-panel__conflict-btn:hover:not(:disabled) {
  color: var(--color-text-primary);
  background: var(--color-bg-surface-container);
}

.git-panel__conflict-btn:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}

.git-panel__conflict-btn--ours:hover:not(:disabled) {
  color: var(--color-primary);
  border-color: var(--color-primary);
}

.git-panel__conflict-btn--theirs:hover:not(:disabled) {
  color: var(--color-primary);
  border-color: var(--color-primary);
}

.git-panel__conflict-btn--resolved:hover:not(:disabled) {
  color: var(--color-success, #22c55e);
  border-color: var(--color-success, #22c55e);
}

/* AI Code Review button */
.git-panel__review-btn {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 2px 8px;
  font-size: 11px;
  font-weight: 500;
  color: var(--color-primary);
  background: color-mix(in srgb, var(--color-primary) 8%, transparent);
  border: 1px solid color-mix(in srgb, var(--color-primary) 30%, transparent);
  border-radius: var(--radius-sm);
  cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast);
}

.git-panel__review-btn:hover {
  background: color-mix(in srgb, var(--color-primary) 16%, transparent);
}

/* AI Code Review modal */
.review-modal-overlay {
  position: fixed;
  inset: 0;
  background-color: color-mix(in srgb, var(--color-bg-base) 75%, transparent);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 2000;
  padding: 24px;
}

.dialog-backdrop-button {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  margin: 0;
  padding: 0;
  border: 0;
  background: transparent;
  cursor: default;
}

.review-modal {
  position: relative;
  z-index: 1;
  display: flex;
  flex-direction: column;
  width: min(720px, 95vw);
  height: min(640px, 88vh);
  background-color: var(--color-bg-surface);
  border: 1px solid var(--color-border-subtle);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-3, 0 12px 32px rgba(0, 0, 0, 0.4));
  overflow: hidden;
}

.review-modal__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 10px 16px;
  border-bottom: 1px solid var(--color-border-subtle);
  background-color: var(--color-bg-elevated);
}

.review-modal__header-left {
  display: flex;
  align-items: center;
  gap: 8px;
  color: var(--color-primary);
}

.review-modal__title {
  font-size: 12px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--color-text-primary);
}

.review-modal__file-count {
  font-size: 10px;
  color: var(--color-text-tertiary);
  padding: 1px 6px;
  background: var(--color-bg-surface);
  border-radius: 8px;
}

.review-modal__header-right {
  display: flex;
  align-items: center;
  gap: 8px;
}

.review-modal__rerun {
  padding: 4px 12px;
  font-size: 11px;
  font-family: var(--font-sans);
  color: var(--color-primary);
  background: color-mix(in srgb, var(--color-primary) 8%, transparent);
  border: 1px solid color-mix(in srgb, var(--color-primary) 30%, transparent);
  border-radius: var(--radius-sm);
  cursor: pointer;
  transition: background-color var(--transition-fast);
}

.review-modal__rerun:hover:not(:disabled) {
  background: color-mix(in srgb, var(--color-primary) 16%, transparent);
}

.review-modal__rerun:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.review-modal__close {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 22px;
  height: 22px;
  border: none;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-text-tertiary);
  cursor: pointer;
  transition: color var(--transition-fast), background-color var(--transition-fast);
}

.review-modal__close:hover {
  color: var(--color-text-primary);
  background-color: color-mix(in srgb, var(--color-text-tertiary) 12%, transparent);
}

.review-modal__body {
  flex: 1;
  overflow-y: auto;
  padding: 16px 20px;
}

.review-modal__loading {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  height: 100%;
  gap: 16px;
  color: var(--color-text-tertiary);
  font-size: 12px;
}

.review-modal__spinner {
  width: 32px;
  height: 32px;
  border: 2px solid var(--color-border-subtle);
  border-top-color: var(--color-primary);
  border-radius: 50%;
  animation: review-spin 0.8s linear infinite;
}

@keyframes review-spin {
  to { transform: rotate(360deg); }
}

@media (prefers-reduced-motion: reduce) {
  .review-modal__spinner { animation: none; }
}

.review-modal__error {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  height: 100%;
  gap: 12px;
  color: var(--color-error);
  font-size: 12px;
  text-align: center;
}

.review-modal__retry {
  padding: 6px 14px;
  font-size: 12px;
  color: var(--color-primary);
  background: color-mix(in srgb, var(--color-primary) 8%, transparent);
  border: 1px solid var(--color-primary);
  border-radius: var(--radius-sm);
  cursor: pointer;
}

.review-modal__retry:hover {
  background: var(--color-primary);
  color: var(--color-on-primary);
}

.review-modal__result {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.review-modal__files {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 4px;
  padding-bottom: 8px;
  border-bottom: 1px solid var(--color-border-subtle);
}

.review-modal__files-label {
  font-size: 10px;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--color-text-tertiary);
}

.review-modal__file-chip {
  display: inline-flex;
  align-items: center;
  padding: 2px 8px;
  font-size: 10px;
  font-family: var(--font-mono);
  color: var(--color-text-secondary);
  background: var(--color-bg-elevated);
  border: 1px solid var(--color-border-subtle);
  border-radius: var(--radius-xs);
  max-width: 120px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.review-modal__content {
  font-size: 13px;
  line-height: 1.6;
  color: var(--color-text-primary);
}

.review-modal__content :deep(pre) {
  margin: 8px 0;
  padding: 12px 16px;
  background-color: var(--hljs-bg, var(--color-bg-base));
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-sm);
  overflow-x: auto;
  font-size: 13px;
  line-height: 1.5;
}

.review-modal__content :deep(code) {
  font-family: var(--font-mono);
  font-size: 13px;
}

.review-modal__content :deep(code.hljs) {
  background: transparent;
  padding: 0;
  font-weight: 500;
}

.review-modal__content :deep(p) {
  margin: 6px 0;
}

.review-modal__content :deep(ul),
.review-modal__content :deep(ol) {
  margin: 6px 0;
  padding-left: 20px;
}

.review-modal__content :deep(h1),
.review-modal__content :deep(h2),
.review-modal__content :deep(h3) {
  margin: 12px 0 6px;
  font-size: 14px;
  font-weight: 600;
}

.review-modal__timestamp {
  padding-top: 8px;
  border-top: 1px solid var(--color-border-subtle);
  font-size: 10px;
  color: var(--color-text-tertiary);
}

.review-modal__empty {
  display: flex;
  align-items: center;
  justify-content: center;
  height: 100%;
  color: var(--color-text-tertiary);
  font-size: 12px;
  text-align: center;
}

.fade-enter-active,
.fade-leave-active {
  transition: opacity var(--transition-fast);
}

.fade-enter-from,
.fade-leave-to {
  opacity: 0;
}

@media (prefers-reduced-motion: reduce) {
  .fade-enter-active,
  .fade-leave-active {
    transition: none;
  }
}

/* F-4 (prompt-2.md): Submodule + Cherry-pick + Revert + Bisect 区块样式 */
.git-panel__advanced {
  border-top: 1px solid var(--color-border);
  padding: 12px 0;
}

.git-panel__advanced-header {
  display: flex;
  width: 100%;
  align-items: center;
  justify-content: space-between;
  border: 0;
  background: transparent;
  color: inherit;
  text-align: left;
  cursor: pointer;
  padding: 4px 0;
  margin-bottom: 8px;
}

.git-panel__advanced-header:focus-visible {
  outline: 2px solid var(--color-primary-focus);
  outline-offset: 2px;
}

.git-panel__advanced-title {
  font-size: 11px;
  font-weight: 600;
  color: var(--color-text-secondary);
  text-transform: uppercase;
  letter-spacing: 0.5px;
}

.git-panel__section-label {
  font-size: 11px;
  font-weight: 500;
  color: var(--color-text-tertiary);
  margin-bottom: 6px;
}

.git-panel__submodule,
.git-panel__cherry-revert,
.git-panel__bisect {
  padding: 8px 0;
}

.git-panel__submodule-actions,
.git-panel__bisect-controls {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
  margin-bottom: 8px;
}

.git-panel__submodule-add {
  display: flex;
  gap: 6px;
  margin-bottom: 8px;
}

.git-panel__submodule-add .git-panel__input {
  flex: 1;
  min-width: 0;
}

.git-panel__submodule-list {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.git-panel__submodule-item {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 4px 6px;
  border-radius: var(--radius-sm, 4px);
  background-color: var(--color-bg-secondary, rgba(0, 0, 0, 0.03));
}

.git-panel__submodule-sha {
  font-family: var(--font-mono, monospace);
  font-size: 10px;
  color: var(--color-text-tertiary);
}

.git-panel__submodule-path {
  flex: 1;
  font-size: 12px;
  color: var(--color-text-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.git-panel__submodule-badge {
  font-size: 9px;
  padding: 1px 4px;
  border-radius: var(--radius-full, 9999px);
  font-weight: 500;
}

.git-panel__submodule-badge--uninit {
  background-color: var(--color-warning-bg, rgba(255, 165, 0, 0.15));
  color: var(--color-warning, #c47c00);
}

.git-panel__submodule-badge--modified {
  background-color: var(--color-danger-bg, rgba(255, 59, 48, 0.15));
  color: var(--color-danger, #d32f2f);
}

.git-panel__submodule-empty,
.git-panel__submodule-loading {
  font-size: 11px;
  color: var(--color-text-tertiary);
  padding: 8px 0;
}

.git-panel__submodule-error,
.git-panel__bisect-error {
  font-size: 11px;
  color: var(--color-danger, #d32f2f);
  padding: 4px 0;
}

.git-panel__cherry-revert-row,
.git-panel__bisect-row {
  display: flex;
  gap: 6px;
  margin-bottom: 6px;
}

.git-panel__cherry-revert-row .git-panel__input,
.git-panel__bisect-row .git-panel__input {
  flex: 1;
  min-width: 0;
}

.git-panel__btn--small {
  font-size: 11px;
  padding: 4px 8px;
  border-radius: var(--radius-sm, 4px);
  border: 1px solid var(--color-border);
  background-color: var(--color-bg-primary, transparent);
  color: var(--color-text-primary);
  cursor: pointer;
  white-space: nowrap;
}

.git-panel__btn--small:hover {
  background-color: var(--color-bg-hover, rgba(0, 0, 0, 0.05));
}

.git-panel__btn--primary {
  background-color: var(--color-primary);
  color: var(--color-on-primary);
  border-color: var(--color-primary);
}

.git-panel__btn--primary:hover {
  background-color: var(--color-primary-focus);
}

.git-panel__btn--success {
  background-color: var(--color-success);
  color: var(--color-on-primary);
  border-color: var(--color-success);
}

.git-panel__btn--danger {
  background-color: var(--color-error);
  color: var(--color-on-primary);
  border-color: var(--color-error);
}

.git-panel__input {
  font-size: 12px;
  padding: 4px 8px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm, 4px);
  background-color: var(--color-bg-primary, transparent);
  color: var(--color-text-primary);
  outline: none;
}

.git-panel__input:focus {
  border-color: var(--color-primary, #007aff);
}
</style>
