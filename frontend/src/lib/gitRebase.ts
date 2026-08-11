// Koyori IDE 模块 · Git Rebase。
// 喵，这是 Koyori IDE 的 Git Rebase 模块（前端实现）~
import { computed, reactive } from "vue";
import * as GitRebaseBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/gitrebaseservice.js";

export const REBASE_ACTIONS = [
  "pick",
  "reword",
  "edit",
  "squash",
  "fixup",
  "exec",
  "drop",
] as const;

export const REBASE_COMMIT_ACTIONS = [
  "pick",
  "reword",
  "edit",
  "squash",
  "fixup",
  "drop",
] as const;

export type RebaseAction = (typeof REBASE_ACTIONS)[number];
export type RebaseCommitAction = (typeof REBASE_COMMIT_ACTIONS)[number];
export type RebaseOperation = "start" | "apply" | "continue" | "abort" | "skip";
export type RebasePhase = "idle" | "foreign" | "awaitingApply" | "applying" | "ready" | "stopped";
export type RebaseStopReason = "" | "commandError" | "syntheticEdit" | "manual";

export interface RebaseTodoAction {
  action: RebaseAction;
  commitSha: string;
  shortMessage: string;
  longMessage?: string;
  authorName?: string;
  authorEmail?: string;
  date?: string;
}

export interface GitRebaseServiceBindings {
  GetRebaseTodoList(repoPath: string, upstreamBranch: string): Promise<RebaseTodoAction[]>;
  GetRebaseStatus?(repoPath: string): Promise<GitRebaseStatus>;
  StartInteractiveRebase(repoPath: string, upstreamBranch: string): Promise<void>;
  ApplyRebaseActions(repoPath: string, actions: RebaseTodoAction[]): Promise<void>;
  ContinueRebase(repoPath: string): Promise<void>;
  AbortRebase(repoPath: string): Promise<void>;
  SkipCommit(repoPath: string): Promise<void>;
  IsRebaseInProgress(repoPath: string): Promise<boolean>;
}

export interface GitRebaseStatus {
  inProgress: boolean;
  owned: boolean;
  phase?: string;
  stopReason?: string;
  upstreamRef?: string;
  upstream?: string;
  origHead?: string;
  actions?: RebaseTodoAction[];
}

export interface GitRebaseState {
  repoPath: string;
  upstreamBranch: string;
  actions: RebaseTodoAction[];
  loading: boolean;
  operation: RebaseOperation | null;
  inProgress: boolean;
  owned: boolean;
  phase: RebasePhase;
  stopReason: RebaseStopReason;
  dirty: boolean;
  startPrepared: boolean;
  actionsApplied: boolean;
  rebaseAdvanced: boolean;
  error: string | null;
}

export const gitRebaseState = reactive<GitRebaseState>({
  repoPath: "",
  upstreamBranch: "",
  actions: [],
  loading: false,
  operation: null,
  inProgress: false,
  owned: false,
  phase: "idle",
  stopReason: "",
  dirty: false,
  startPrepared: false,
  actionsApplied: false,
  rebaseAdvanced: false,
  error: null,
});

export const gitRebaseBusy = computed(
  () => gitRebaseState.loading || gitRebaseState.operation !== null,
);

const defaultBindings: GitRebaseServiceBindings = {
  GetRebaseTodoList: async (repoPath, upstreamBranch) =>
    (await GitRebaseBindings.GetRebaseTodoList(repoPath, upstreamBranch) ?? []) as RebaseTodoAction[],
  GetRebaseStatus: async (repoPath) =>
    await GitRebaseBindings.GetRebaseStatus(repoPath) as GitRebaseStatus,
  StartInteractiveRebase: GitRebaseBindings.StartInteractiveRebase,
  ApplyRebaseActions: GitRebaseBindings.ApplyRebaseActions,
  ContinueRebase: GitRebaseBindings.ContinueRebase,
  AbortRebase: GitRebaseBindings.AbortRebase,
  SkipCommit: GitRebaseBindings.SkipCommit,
  IsRebaseInProgress: GitRebaseBindings.IsRebaseInProgress,
};

let bindings: GitRebaseServiceBindings = defaultBindings;
let contextRevision = 0;
let loadSequence = 0;
let operationSequence = 0;

export function setGitRebaseServiceBindings(
  value: GitRebaseServiceBindings | null,
): void {
  bindings = value ?? defaultBindings;
}

export const __setGitRebaseServiceBindingsForTesting =
  setGitRebaseServiceBindings;

export function isRebaseAction(value: unknown): value is RebaseAction {
  return typeof value === "string"
    && (REBASE_ACTIONS as readonly string[]).includes(value);
}

export function isRebaseCommitAction(value: unknown): value is RebaseCommitAction {
  return typeof value === "string"
    && (REBASE_COMMIT_ACTIONS as readonly string[]).includes(value);
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function optionalString(value: unknown): string | undefined {
  return typeof value === "string" && value !== "" ? value : undefined;
}

function normalizeTodoAction(value: unknown): RebaseTodoAction | null {
  if (!isRecord(value) || !isRebaseCommitAction(value.action)) return null;
  const commitSha = typeof value.commitSha === "string" ? value.commitSha.trim() : "";
  const shortMessage = typeof value.shortMessage === "string" ? value.shortMessage : "";
  if (!commitSha) return null;
  return {
    action: value.action,
    commitSha,
    shortMessage,
    longMessage: optionalString(value.longMessage),
    authorName: optionalString(value.authorName),
    authorEmail: optionalString(value.authorEmail),
    date: optionalString(value.date),
  };
}

function normalizeTodoList(value: unknown): RebaseTodoAction[] {
  if (!Array.isArray(value)) return [];
  const seen = new Set<string>();
  const result: RebaseTodoAction[] = [];
  for (const candidate of value) {
    const action = normalizeTodoAction(candidate);
    if (!action) continue;
    const key = action.commitSha.toLowerCase();
    if (seen.has(key)) continue;
    seen.add(key);
    result.push(action);
  }
  return result;
}

function normalizeRebasePhase(value: unknown, inProgress: boolean, owned: boolean): RebasePhase {
  if (!inProgress) return "idle";
  if (!owned) return "foreign";
  if (
    value === "awaitingApply"
    || value === "applying"
    || value === "ready"
    || value === "stopped"
  ) {
    return value;
  }
  throw new Error(`Git returned an invalid rebase phase: ${String(value)}`);
}

function normalizeRebaseStopReason(value: unknown, phase: RebasePhase): RebaseStopReason {
  if (phase !== "stopped") return "";
  if (value === "commandError" || value === "syntheticEdit" || value === "manual") {
    return value;
  }
  throw new Error(`Git returned an invalid rebase stop reason: ${String(value)}`);
}

function normalizeRebaseStatus(value: unknown): GitRebaseStatus {
  if (!isRecord(value)) return { inProgress: false, owned: false };
  const inProgress = value.inProgress === true;
  const owned = inProgress && value.owned === true;
  const phase = normalizeRebasePhase(value.phase, inProgress, owned);
  const rawActions = Array.isArray(value.actions) ? value.actions : undefined;
  const actions = rawActions ? normalizeTodoList(rawActions) : undefined;
  if (rawActions && actions && actions.length !== rawActions.length) {
    throw new Error("Git returned an invalid or duplicate rebase action snapshot");
  }
  return {
    inProgress,
    owned,
    phase,
    stopReason: normalizeRebaseStopReason(value.stopReason, phase),
    upstreamRef: typeof value.upstreamRef === "string" ? value.upstreamRef : undefined,
    upstream: typeof value.upstream === "string" ? value.upstream : undefined,
    origHead: typeof value.origHead === "string" ? value.origHead : undefined,
    actions,
  };
}

function cloneTodoAction(action: RebaseTodoAction): RebaseTodoAction {
  return { ...action };
}

function cloneTodoList(actions: readonly RebaseTodoAction[]): RebaseTodoAction[] {
  return actions.map(cloneTodoAction);
}

function canEditRebaseActions(): boolean {
  return gitRebaseState.phase === "idle" || gitRebaseState.phase === "awaitingApply";
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function requiredValue(value: string, name: string): string {
  const normalized = value.trim();
  if (!normalized) throw new Error(`${name} is required`);
  return normalized;
}

function updateContext(repoPath: string, upstreamBranch: string): number {
  const repositoryChanged = gitRebaseState.repoPath !== repoPath;
  if (
    repositoryChanged
    || gitRebaseState.upstreamBranch !== upstreamBranch
  ) {
    contextRevision += 1;
    gitRebaseState.repoPath = repoPath;
    gitRebaseState.upstreamBranch = upstreamBranch;
    gitRebaseState.actions = [];
    gitRebaseState.dirty = false;
    gitRebaseState.owned = false;
    gitRebaseState.phase = "idle";
    gitRebaseState.stopReason = "";
    gitRebaseState.startPrepared = false;
    gitRebaseState.actionsApplied = false;
    gitRebaseState.rebaseAdvanced = false;
    gitRebaseState.error = null;
    gitRebaseState.inProgress = false;
  }
  return contextRevision;
}

function currentContext(revision: number, repoPath: string): boolean {
  return revision === contextRevision && repoPath === gitRebaseState.repoPath;
}

export function reorderRebaseActions(
  actions: readonly RebaseTodoAction[],
  fromIndex: number,
  toIndex: number,
): RebaseTodoAction[] {
  const next = cloneTodoList(actions);
  if (
    !Number.isInteger(fromIndex)
    || !Number.isInteger(toIndex)
    || fromIndex < 0
    || toIndex < 0
    || fromIndex >= next.length
    || toIndex >= next.length
    || fromIndex === toIndex
  ) {
    return next;
  }
  const [moved] = next.splice(fromIndex, 1);
  next.splice(toIndex, 0, moved);
  return next;
}
export function hasValidRebaseActionOrder(
  actions: readonly RebaseTodoAction[],
): boolean {
  let hasReplayTarget = false;
  for (const action of actions) {
    if (
      (action.action === "squash" || action.action === "fixup")
      && !hasReplayTarget
    ) return false;
    if (isRebaseCommitAction(action.action) && action.action !== "drop") {
      hasReplayTarget = true;
    }
  }
  return true;
}

export function replaceRebaseActions(actions: readonly RebaseTodoAction[]): void {
  if (!canEditRebaseActions()) {
    throw new Error("Rebase actions cannot change after execution has advanced");
  }
  const normalized = normalizeTodoList(actions);
  if (normalized.length !== actions.length) {
    throw new Error("Rebase actions contain invalid or duplicate entries");
  }
  gitRebaseState.actions = normalized;
  gitRebaseState.dirty = true;
  gitRebaseState.actionsApplied = false;
}

export function moveRebaseAction(fromIndex: number, toIndex: number): boolean {
  if (!canEditRebaseActions()) return false;
  if (
    !Number.isInteger(fromIndex)
    || !Number.isInteger(toIndex)
    || fromIndex < 0
    || toIndex < 0
    || fromIndex >= gitRebaseState.actions.length
    || toIndex >= gitRebaseState.actions.length
    || fromIndex === toIndex
  ) {
    return false;
  }
  gitRebaseState.actions = reorderRebaseActions(
    gitRebaseState.actions,
    fromIndex,
    toIndex,
  );
  gitRebaseState.dirty = true;
  gitRebaseState.actionsApplied = false;
  return true;
}

export function updateRebaseAction(
  index: number,
  action: RebaseAction,
): boolean {
  if (!canEditRebaseActions()) return false;
  if (
    !Number.isInteger(index)
    || index < 0
    || index >= gitRebaseState.actions.length
    || !isRebaseCommitAction(action)
  ) {
    return false;
  }
  const current = gitRebaseState.actions[index];
  if (current.action === action) return true;
  const next = cloneTodoList(gitRebaseState.actions);
  next[index] = { ...next[index], action };
  gitRebaseState.actions = next;
  gitRebaseState.dirty = true;
  gitRebaseState.actionsApplied = false;
  return true;
}

export function updateRebaseMessage(index: number, shortMessage: string): boolean {
  if (
    !canEditRebaseActions()
    || !Number.isInteger(index)
    || index < 0
    || index >= gitRebaseState.actions.length
  ) {
    return false;
  }
  const current = gitRebaseState.actions[index];
  if (current.shortMessage === shortMessage) return true;
  const next = cloneTodoList(gitRebaseState.actions);
  next[index] = { ...next[index], shortMessage };
  gitRebaseState.actions = next;
  gitRebaseState.dirty = true;
  gitRebaseState.actionsApplied = false;
  return true;
}

async function loadRebaseStatus(repoPath: string): Promise<GitRebaseStatus> {
  if (bindings.GetRebaseStatus) {
    return normalizeRebaseStatus(await bindings.GetRebaseStatus(repoPath));
  }
  const inProgress = await bindings.IsRebaseInProgress(repoPath);
  return {
    inProgress: inProgress === true,
    owned: false,
    phase: inProgress === true ? "foreign" : "idle",
  };
}

function applyRebaseStatus(status: GitRebaseStatus): void {
  const phase = normalizeRebasePhase(status.phase, status.inProgress, status.owned);
  gitRebaseState.inProgress = status.inProgress;
  gitRebaseState.owned = status.owned;
  gitRebaseState.phase = phase;
  gitRebaseState.stopReason = normalizeRebaseStopReason(status.stopReason, phase);
  gitRebaseState.startPrepared = status.owned
    && (phase === "awaitingApply" || phase === "applying" || phase === "ready" || phase === "stopped");
  gitRebaseState.actionsApplied = status.owned
    && (phase === "ready" || phase === "stopped");
  gitRebaseState.rebaseAdvanced = phase === "stopped";
}

export async function loadRebaseTodoList(
  repoPath: string,
  upstreamBranch: string,
): Promise<RebaseTodoAction[]> {
  const repo = requiredValue(repoPath, "Repository path");
  const upstream = requiredValue(upstreamBranch, "Upstream branch");
  const revision = updateContext(repo, upstream);
  const sequence = ++loadSequence;
  gitRebaseState.loading = true;
  gitRebaseState.error = null;
  try {
    // Legacy bindings have no ownership query. Start their todo request before
    // the progress probe so overlapping loads retain invocation order.
    const legacyTodoPromise = bindings.GetRebaseStatus
      ? null
      : bindings.GetRebaseTodoList(repo, upstream);
    const status = await loadRebaseStatus(repo);
    if (sequence !== loadSequence || !currentContext(revision, repo)) {
      return cloneTodoList(gitRebaseState.actions);
    }
    if (
      status.inProgress
      && status.owned
      && status.upstreamRef
      && status.upstreamRef !== upstream
    ) {
      applyRebaseStatus({
        ...status,
        owned: false,
        phase: "foreign",
        actions: undefined,
      });
      gitRebaseState.actions = [];
      gitRebaseState.dirty = false;
      throw new Error("Active rebase belongs to a different upstream branch");
    }
    if (status.inProgress && status.owned && (!status.actions || status.actions.length === 0)) {
      throw new Error("Owned rebase status has no action snapshot");
    }
    let todo: RebaseTodoAction[] = [];
    if (status.inProgress && !status.owned && bindings.GetRebaseStatus) {
      applyRebaseStatus(status);
    } else {
      if (status.actions) {
        todo = cloneTodoList(status.actions);
      } else {
        const rawTodo = await (legacyTodoPromise ?? bindings.GetRebaseTodoList(repo, upstream));
        const normalized = normalizeTodoList(rawTodo);
        if (normalized.length !== rawTodo.length) {
          throw new Error("Git returned an invalid or duplicate rebase todo entry");
        }
        todo = normalized;
      }
      if (todo.length === 0 && !status.inProgress) {
        // An empty range is valid while idle, but still keeps the state clean.
        todo = [];
      }
      applyRebaseStatus(status);
    }
    gitRebaseState.actions = todo;
    gitRebaseState.dirty = false;
    if (!status.inProgress || !status.owned) {
      gitRebaseState.startPrepared = false;
      gitRebaseState.actionsApplied = false;
      gitRebaseState.rebaseAdvanced = false;
    }
    return cloneTodoList(gitRebaseState.actions);
  } catch (error: unknown) {
    if (sequence === loadSequence && currentContext(revision, repo)) {
      gitRebaseState.error = errorMessage(error);
    }
    throw error;
  } finally {
    if (sequence === loadSequence) gitRebaseState.loading = false;
  }
}

async function runRebaseOperation(
  operation: RebaseOperation,
  repoPath: string,
  callback: (repo: string, revision: number) => Promise<void>,
): Promise<void> {
  const repo = requiredValue(repoPath, "Repository path");
  if (gitRebaseState.operation !== null) {
    throw new Error(`Rebase operation ${gitRebaseState.operation} is already running`);
  }
  if (gitRebaseState.repoPath !== repo) updateContext(repo, "");
  const revision = contextRevision;
  const sequence = ++operationSequence;
  gitRebaseState.operation = operation;
  gitRebaseState.error = null;
  try {
    await callback(repo, revision);
  } catch (error: unknown) {
    if (sequence === operationSequence && currentContext(revision, repo)) {
      gitRebaseState.error = errorMessage(error);
    }
    throw error;
  } finally {
    if (sequence === operationSequence) gitRebaseState.operation = null;
  }
}

export function startInteractiveRebase(
  repoPath = gitRebaseState.repoPath,
  upstreamBranch = gitRebaseState.upstreamBranch,
): Promise<void> {
  const upstream = requiredValue(upstreamBranch, "Upstream branch");
  const repo = requiredValue(repoPath, "Repository path");
  updateContext(repo, upstream);
  return runRebaseOperation("start", repo, async (activeRepo, revision) => {
    await bindings.StartInteractiveRebase(activeRepo, upstream);
    if (currentContext(revision, activeRepo)) {
      gitRebaseState.inProgress = true;
      gitRebaseState.owned = true;
      gitRebaseState.phase = "awaitingApply";
      gitRebaseState.stopReason = "";
      gitRebaseState.startPrepared = true;
      gitRebaseState.actionsApplied = false;
      gitRebaseState.rebaseAdvanced = false;
    }
  });
}

export function applyRebaseActions(
  repoPath = gitRebaseState.repoPath,
): Promise<void> {
  const repo = requiredValue(repoPath, "Repository path");
  if (repo !== gitRebaseState.repoPath) {
    const error = new Error("Rebase todo belongs to a different repository");
    gitRebaseState.error = error.message;
    return Promise.reject(error);
  }
  const actions = cloneTodoList(gitRebaseState.actions);
  if (actions.length === 0) {
    const error = new Error("Rebase todo list is empty");
    gitRebaseState.error = error.message;
    return Promise.reject(error);
  }
  if (
    !gitRebaseState.startPrepared
    || !gitRebaseState.owned
    || !gitRebaseState.inProgress
    || (gitRebaseState.phase !== "awaitingApply" && gitRebaseState.phase !== "applying")
  ) {
    const error = new Error("Start this rebase before applying actions");
    gitRebaseState.error = error.message;
    return Promise.reject(error);
  }
  if (gitRebaseState.rebaseAdvanced) {
    const error = new Error("Rebase actions cannot be applied after execution has advanced");
    gitRebaseState.error = error.message;
    return Promise.reject(error);
  }
  if (actions.some((action) => action.action === "reword" && !action.shortMessage.trim())) {
    const error = new Error("Reword actions require a commit message");
    gitRebaseState.error = error.message;
    return Promise.reject(error);
  }
  if (!hasValidRebaseActionOrder(actions)) {
    const error = new Error("Squash and fixup actions require a previous commit");
    gitRebaseState.error = error.message;
    return Promise.reject(error);
  }
  return runRebaseOperation("apply", repo, async (activeRepo, revision) => {
    await bindings.ApplyRebaseActions(activeRepo, actions);
    if (currentContext(revision, activeRepo)) {
      gitRebaseState.dirty = false;
      gitRebaseState.phase = "ready";
      gitRebaseState.stopReason = "";
      gitRebaseState.owned = true;
      gitRebaseState.actionsApplied = true;
      gitRebaseState.rebaseAdvanced = false;
    }
  });
}

async function refreshProgressAfterControl(
  repo: string,
  revision: number,
): Promise<void> {
  let status: GitRebaseStatus;
  if (bindings.GetRebaseStatus) {
    status = await loadRebaseStatus(repo);
  } else {
    const inProgress = await bindings.IsRebaseInProgress(repo);
    status = {
      inProgress: inProgress === true,
      owned: inProgress === true,
      phase: inProgress === true ? "stopped" : "idle",
      stopReason: inProgress === true ? "commandError" : "",
      actions: gitRebaseState.actions,
    };
  }
  if (!currentContext(revision, repo)) return;
  applyRebaseStatus(status);
  if (status.owned && status.actions) {
    gitRebaseState.actions = cloneTodoList(status.actions);
  }
  if (!status.inProgress || !status.owned) {
    gitRebaseState.actions = [];
    gitRebaseState.dirty = false;
    gitRebaseState.actionsApplied = false;
    gitRebaseState.rebaseAdvanced = false;
    gitRebaseState.startPrepared = false;
  }
}

async function runControlAndRefresh(
  control: () => Promise<void>,
  repo: string,
  revision: number,
): Promise<void> {
  try {
    await control();
  } catch (error: unknown) {
    try {
      await refreshProgressAfterControl(repo, revision);
    } catch {
      // Preserve the control failure; it is the actionable Git error.
    }
    throw error;
  }
  await refreshProgressAfterControl(repo, revision);
}

export function continueRebase(
  repoPath = gitRebaseState.repoPath,
): Promise<void> {
  const repo = requiredValue(repoPath, "Repository path");
  if (
    repo !== gitRebaseState.repoPath
    || !gitRebaseState.owned
    || !gitRebaseState.inProgress
    || !gitRebaseState.actionsApplied
    || (gitRebaseState.phase !== "ready" && gitRebaseState.phase !== "stopped")
  ) {
    const error = new Error("Apply rebase actions before continuing");
    gitRebaseState.error = error.message;
    return Promise.reject(error);
  }
  return runRebaseOperation("continue", repo, async (activeRepo, revision) => {
    await runControlAndRefresh(
      () => bindings.ContinueRebase(activeRepo),
      activeRepo,
      revision,
    );
  });
}

export function abortRebase(
  repoPath = gitRebaseState.repoPath,
): Promise<void> {
  const repo = requiredValue(repoPath, "Repository path");
  if (repo !== gitRebaseState.repoPath || !gitRebaseState.owned || !gitRebaseState.inProgress) {
    const error = new Error("An owned rebase is required before aborting");
    gitRebaseState.error = error.message;
    return Promise.reject(error);
  }
  return runRebaseOperation("abort", repo, async (activeRepo, revision) => {
    await bindings.AbortRebase(activeRepo);
    if (!currentContext(revision, activeRepo)) return;
    gitRebaseState.inProgress = false;
    gitRebaseState.owned = false;
    gitRebaseState.phase = "idle";
    gitRebaseState.stopReason = "";
    gitRebaseState.actions = [];
    gitRebaseState.dirty = false;
    gitRebaseState.actionsApplied = false;
    gitRebaseState.rebaseAdvanced = false;
    gitRebaseState.startPrepared = false;
  });
}

export function skipRebaseCommit(
  repoPath = gitRebaseState.repoPath,
): Promise<void> {
  const repo = requiredValue(repoPath, "Repository path");
  if (
    repo !== gitRebaseState.repoPath
    || !gitRebaseState.owned
    || !gitRebaseState.inProgress
    || !gitRebaseState.actionsApplied
    || gitRebaseState.phase !== "stopped"
    || gitRebaseState.stopReason !== "commandError"
  ) {
    const error = new Error("A stopped applied rebase is required before skipping");
    gitRebaseState.error = error.message;
    return Promise.reject(error);
  }
  return runRebaseOperation("skip", repo, async (activeRepo, revision) => {
    await runControlAndRefresh(
      () => bindings.SkipCommit(activeRepo),
      activeRepo,
      revision,
    );
  });
}

export function clearGitRebaseError(): void {
  gitRebaseState.error = null;
}

export function resetGitRebaseStore(): void {
  contextRevision += 1;
  loadSequence += 1;
  operationSequence += 1;
  gitRebaseState.repoPath = "";
  gitRebaseState.upstreamBranch = "";
  gitRebaseState.actions = [];
  gitRebaseState.loading = false;
  gitRebaseState.operation = null;
  gitRebaseState.inProgress = false;
  gitRebaseState.owned = false;
  gitRebaseState.phase = "idle";
  gitRebaseState.stopReason = "";
  gitRebaseState.dirty = false;
  gitRebaseState.startPrepared = false;
  gitRebaseState.actionsApplied = false;
  gitRebaseState.rebaseAdvanced = false;
  gitRebaseState.error = null;
  bindings = defaultBindings;
}

export interface GitRebaseStore {
  readonly state: GitRebaseState;
  readonly actions: RebaseTodoAction[];
  readonly loading: boolean;
  readonly operation: RebaseOperation | null;
  readonly busy: boolean;
  readonly inProgress: boolean;
  readonly owned: boolean;
  readonly phase: RebasePhase;
  readonly stopReason: RebaseStopReason;
  readonly dirty: boolean;
  readonly startPrepared: boolean;
  readonly actionsApplied: boolean;
  readonly rebaseAdvanced: boolean;
  readonly error: string | null;
  loadTodoList(repoPath: string, upstreamBranch: string): Promise<RebaseTodoAction[]>;
  loadTodo(repoPath: string, upstreamBranch: string): Promise<RebaseTodoAction[]>;
  moveAction(fromIndex: number, toIndex: number): boolean;
  updateAction(index: number, action: RebaseAction): boolean;
  updateMessage(index: number, shortMessage: string): boolean;
  replaceActions(actions: readonly RebaseTodoAction[]): void;
  startRebase(repoPath?: string, upstreamBranch?: string): Promise<void>;
  applyActions(repoPath?: string): Promise<void>;
  continueRebase(repoPath?: string): Promise<void>;
  abortRebase(repoPath?: string): Promise<void>;
  skipCommit(repoPath?: string): Promise<void>;
  clearError(): void;
}

const gitRebaseStore: GitRebaseStore = {
  state: gitRebaseState,
  get actions() {
    return gitRebaseState.actions;
  },
  get loading() {
    return gitRebaseState.loading;
  },
  get operation() {
    return gitRebaseState.operation;
  },
  get busy() {
    return gitRebaseBusy.value;
  },
  get inProgress() {
    return gitRebaseState.inProgress;
  },
  get owned() {
    return gitRebaseState.owned;
  },
  get phase() {
    return gitRebaseState.phase;
  },
  get stopReason() {
    return gitRebaseState.stopReason;
  },
  get dirty() {
    return gitRebaseState.dirty;
  },
  get startPrepared() {
    return gitRebaseState.startPrepared;
  },
  get actionsApplied() {
    return gitRebaseState.actionsApplied;
  },
  get rebaseAdvanced() {
    return gitRebaseState.rebaseAdvanced;
  },
  get error() {
    return gitRebaseState.error;
  },
  loadTodoList: loadRebaseTodoList,
  loadTodo: loadRebaseTodoList,
  moveAction: moveRebaseAction,
  updateAction: updateRebaseAction,
  updateMessage: updateRebaseMessage,
  replaceActions: replaceRebaseActions,
  startRebase: startInteractiveRebase,
  applyActions: applyRebaseActions,
  continueRebase,
  abortRebase,
  skipCommit: skipRebaseCommit,
  clearError: clearGitRebaseError,
};

// Pinia is not installed in this project. This composable deliberately keeps
// the same singleton-store ergonomics while using the repository's reactive
// state convention.
export function useGitRebaseStore(): GitRebaseStore {
  return gitRebaseStore;
}
