// Koyori IDE 模块 · Git Worktree。
// 喵，这是 Koyori IDE 的 Git Worktree 模块（前端实现）~
import { reactive } from "vue";
import { errorMessage } from "@/lib/errors";
import * as GitWorktreeBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/gitworktreeservice.js";

export interface WorktreeInfo {
  path: string;
  head: string;
  branch: string;
  bare: boolean;
  locked?: string;
  prunable?: boolean;
}

export interface AddWorktreeOptions {
  newBranch?: string;
  detach?: boolean;
  force?: boolean;
  noCheckout?: boolean;
  allowOutsideRepository?: boolean;
}

export interface GitWorktreeServiceBindings {
  ListWorktrees(repoPath: string): Promise<WorktreeInfo[]>;
  AddWorktree(
    repoPath: string,
    path: string,
    commitish: string,
    options: AddWorktreeOptions,
  ): Promise<void>;
  RemoveWorktree(repoPath: string, path: string, force: boolean): Promise<void>;
  PruneWorktrees(repoPath: string, dryRun: boolean): Promise<string[]>;
  LockWorktree(repoPath: string, path: string, reason: string): Promise<void>;
  UnlockWorktree(repoPath: string, path: string): Promise<void>;
  MoveWorktree?(
    repoPath: string,
    oldPath: string,
    newPath: string,
    force: boolean,
    allowOutsideRepository: boolean,
  ): Promise<void>;
}

export interface GitWorktreeStoreState {
  repoPath: string;
  worktrees: WorktreeInfo[];
  loading: boolean;
  mutating: boolean;
  error: string | null;
}

export const wailsGitWorktreeService = {
  ListWorktrees: async (repoPath) =>
    (await GitWorktreeBindings.ListWorktrees(repoPath) ?? []) as WorktreeInfo[],
  AddWorktree: (repoPath, path, commitish, options) => {
    const backendOptions = { ...options };
    delete backendOptions.allowOutsideRepository;
    return GitWorktreeBindings.AddWorktree(repoPath, path, commitish, backendOptions);
  },
  RemoveWorktree: (repoPath, path, force) =>
    GitWorktreeBindings.RemoveWorktree(repoPath, path, force),
  PruneWorktrees: async (repoPath, dryRun) =>
    await GitWorktreeBindings.PruneWorktrees(repoPath, dryRun) ?? [],
  LockWorktree: (repoPath, path, reason) =>
    GitWorktreeBindings.LockWorktree(repoPath, path, reason),
  UnlockWorktree: (repoPath, path) =>
    GitWorktreeBindings.UnlockWorktree(repoPath, path),
  MoveWorktree: (repoPath, oldPath, newPath, force, _allowOutsideRepository) =>
    GitWorktreeBindings.MoveWorktree(repoPath, oldPath, newPath, force),
} satisfies GitWorktreeServiceBindings;

export const gitWorktreeState = reactive<GitWorktreeStoreState>({
  repoPath: "",
  worktrees: [],
  loading: false,
  mutating: false,
  error: null,
});

let bindings: GitWorktreeServiceBindings = wailsGitWorktreeService;
let repositoryRevision = 0;
let listSequence = 0;
let mutationSequence = 0;
const activeMutations = new Set<number>();

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function stringValue(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function normalizeWorktree(value: unknown): WorktreeInfo | null {
  if (!isRecord(value)) return null;
  const path = stringValue(value.path);
  if (!path) return null;
  const locked = stringValue(value.locked);
  return {
    path,
    head: stringValue(value.head),
    branch: stringValue(value.branch),
    bare: value.bare === true,
    locked: locked || undefined,
    prunable: value.prunable === true || undefined,
  };
}

function normalizeWorktrees(value: unknown): WorktreeInfo[] {
  if (!Array.isArray(value)) return [];
  const paths = new Set<string>();
  const result: WorktreeInfo[] = [];
  for (const candidate of value) {
    const worktree = normalizeWorktree(candidate);
    if (!worktree || paths.has(worktree.path)) continue;
    paths.add(worktree.path);
    result.push(worktree);
  }
  return result;
}

function normalizePruneEntries(value: unknown): string[] {
  if (!Array.isArray(value)) return [];
  return value
    .filter((entry): entry is string => typeof entry === "string")
    .map((entry) => entry.trim())
    .filter(Boolean);
}

function requireValue(name: string, value: string): void {
  if (!value.trim()) throw new Error(`${name} is required`);
}

function bindRepository(repoPath: string): number {
  requireValue("repository path", repoPath);
  if (gitWorktreeState.repoPath !== repoPath) {
    repositoryRevision += 1;
    listSequence += 1;
    mutationSequence += 1;
    activeMutations.clear();
    gitWorktreeState.repoPath = repoPath;
    gitWorktreeState.worktrees = [];
    gitWorktreeState.loading = false;
    gitWorktreeState.mutating = false;
    gitWorktreeState.error = null;
  }
  return repositoryRevision;
}

function isCurrentRepository(repoPath: string, revision: number): boolean {
  return gitWorktreeState.repoPath === repoPath && repositoryRevision === revision;
}

export function setGitWorktreeServiceBindings(
  value: GitWorktreeServiceBindings | null,
): void {
  bindings = value ?? wailsGitWorktreeService;
  repositoryRevision += 1;
  listSequence += 1;
  activeMutations.clear();
  gitWorktreeState.loading = false;
  gitWorktreeState.mutating = false;
}

export const __setGitWorktreeServiceBindingsForTesting =
  setGitWorktreeServiceBindings;

export function clearGitWorktreeError(): void {
  gitWorktreeState.error = null;
}

export async function loadWorktrees(repoPath: string): Promise<WorktreeInfo[]> {
  const revision = bindRepository(repoPath);
  const sequence = ++listSequence;
  gitWorktreeState.loading = true;
  gitWorktreeState.error = null;
  try {
    const worktrees = normalizeWorktrees(await bindings.ListWorktrees(repoPath));
    if (isCurrentRepository(repoPath, revision) && sequence === listSequence) {
      gitWorktreeState.worktrees = worktrees;
    }
    return worktrees;
  } catch (error: unknown) {
    if (isCurrentRepository(repoPath, revision) && sequence === listSequence) {
      gitWorktreeState.error = errorMessage(error);
    }
    throw error;
  } finally {
    if (isCurrentRepository(repoPath, revision) && sequence === listSequence) {
      gitWorktreeState.loading = false;
    }
  }
}

async function runMutation<T>(
  repoPath: string,
  action: () => Promise<T>,
  refresh: boolean,
): Promise<T> {
  const revision = bindRepository(repoPath);
  const mutation = ++mutationSequence;
  activeMutations.add(mutation);
  gitWorktreeState.mutating = true;
  gitWorktreeState.error = null;
  try {
    const result = await action();
    if (refresh && isCurrentRepository(repoPath, revision)) {
      await loadWorktrees(repoPath);
    }
    return result;
  } catch (error: unknown) {
    if (isCurrentRepository(repoPath, revision)) {
      gitWorktreeState.error = errorMessage(error);
    }
    throw error;
  } finally {
    activeMutations.delete(mutation);
    gitWorktreeState.mutating = activeMutations.size > 0;
  }
}

export async function addWorktree(
  repoPath: string,
  path: string,
  commitish: string,
  options: AddWorktreeOptions = {},
): Promise<void> {
  requireValue("worktree path", path);
  const normalizedOptions: AddWorktreeOptions = {
    newBranch: options.newBranch ?? "",
    detach: options.detach ?? false,
    force: options.force ?? false,
    noCheckout: options.noCheckout ?? false,
    allowOutsideRepository: options.allowOutsideRepository ?? false,
  };
  return await runMutation(
    repoPath,
    () => bindings.AddWorktree(repoPath, path, commitish, normalizedOptions),
    true,
  );
}

export async function removeWorktree(
  repoPath: string,
  path: string,
  force: boolean,
): Promise<void> {
  requireValue("worktree path", path);
  return await runMutation(
    repoPath,
    () => bindings.RemoveWorktree(repoPath, path, force),
    true,
  );
}

export async function pruneWorktrees(
  repoPath: string,
  dryRun: boolean,
): Promise<string[]> {
  return await runMutation(
    repoPath,
    async () => normalizePruneEntries(await bindings.PruneWorktrees(repoPath, dryRun)),
    !dryRun,
  );
}

export async function lockWorktree(
  repoPath: string,
  path: string,
  reason: string,
): Promise<void> {
  requireValue("worktree path", path);
  return await runMutation(
    repoPath,
    () => bindings.LockWorktree(repoPath, path, reason),
    true,
  );
}

export async function unlockWorktree(repoPath: string, path: string): Promise<void> {
  requireValue("worktree path", path);
  return await runMutation(
    repoPath,
    () => bindings.UnlockWorktree(repoPath, path),
    true,
  );
}

export async function moveWorktree(
  repoPath: string,
  oldPath: string,
  newPath: string,
  force = false,
  allowOutsideRepository = false,
): Promise<void> {
  requireValue("current worktree path", oldPath);
  requireValue("new worktree path", newPath);
  return await runMutation(
    repoPath,
    async () => {
      const move = bindings.MoveWorktree;
      if (!move) throw new Error("move worktree is unavailable");
      await move(repoPath, oldPath, newPath, force, allowOutsideRepository);
    },
    true,
  );
}

export interface GitWorktreeStore {
  readonly repoPath: string;
  readonly worktrees: WorktreeInfo[];
  readonly loading: boolean;
  readonly mutating: boolean;
  readonly error: string | null;
  loadWorktrees(repoPath: string): Promise<WorktreeInfo[]>;
  addWorktree(
    repoPath: string,
    path: string,
    commitish: string,
    options?: AddWorktreeOptions,
  ): Promise<void>;
  removeWorktree(repoPath: string, path: string, force: boolean): Promise<void>;
  pruneWorktrees(repoPath: string, dryRun: boolean): Promise<string[]>;
  lockWorktree(repoPath: string, path: string, reason: string): Promise<void>;
  unlockWorktree(repoPath: string, path: string): Promise<void>;
  moveWorktree(
    repoPath: string,
    oldPath: string,
    newPath: string,
    force?: boolean,
    allowOutsideRepository?: boolean,
  ): Promise<void>;
  clearError(): void;
}

const gitWorktreeStore: GitWorktreeStore = {
  get repoPath() {
    return gitWorktreeState.repoPath;
  },
  get worktrees() {
    return gitWorktreeState.worktrees;
  },
  get loading() {
    return gitWorktreeState.loading;
  },
  get mutating() {
    return gitWorktreeState.mutating;
  },
  get error() {
    return gitWorktreeState.error;
  },
  loadWorktrees,
  addWorktree,
  removeWorktree,
  pruneWorktrees,
  lockWorktree,
  unlockWorktree,
  moveWorktree,
  clearError: clearGitWorktreeError,
};

export function useGitWorktreeStore(): GitWorktreeStore {
  return gitWorktreeStore;
}

export function resetGitWorktreeStore(): void {
  repositoryRevision += 1;
  listSequence += 1;
  mutationSequence += 1;
  activeMutations.clear();
  gitWorktreeState.repoPath = "";
  gitWorktreeState.worktrees = [];
  gitWorktreeState.loading = false;
  gitWorktreeState.mutating = false;
  gitWorktreeState.error = null;
  bindings = wailsGitWorktreeService;
}
