// Plan 11 Task 14 — 智能回滚前端 store。
//
// 使用 lazy bindings + 可注入 backend 模式：
//   - 生产环境懒加载 Wails bindings
//   - 测试环境通过 setSnapshotBackend 注入 mock
//
// 职责（Step 1-10）：
//   - 创建/列出/删除快照（Step 2）
//   - 选择性回滚（Step 7）
//   - 快照差异比较（Step 2: DiffSnapshots）
//   - 清理策略（Step 5）
//   - Git 集成（Step 8，后端侧）
// Koyori IDE 模块 · Snapshot。
// 喵，这是 Koyori IDE 的 Snapshot 模块（前端实现）~
import { reactive } from "vue";
import { Events } from "@wailsio/runtime";
import { notifyError, notifySuccess } from "@/lib/notifications";
import type {
  FileSavedEvent, RestoreDiff, Snapshot, SnapshotDiff, SnapshotReason,
} from "@/types";
import { Duration } from "../../bindings/time/models.js";

// backend 接口镜像 services/snapshot_service.go 导出方法。
interface SnapshotBackend {
  createSnapshot(workspaceRoot: string, reason: string): Promise<Snapshot>;
  restoreSnapshot(snapshotID: string, workspaceRoot: string): Promise<void>;
  restorePartial(snapshotID: string, workspaceRoot: string, filePaths: string[]): Promise<void>;
  // GOAL-P1-01: exact restore. calculateRestoreDiff is preview-only; the caller
  // must show addedAfterSnapshot to the user and pass confirmed=true before
  // restoreSnapshotExact will delete anything.
  calculateRestoreDiff(snapshotID: string, workspaceRoot: string): Promise<RestoreDiff>;
  restoreSnapshotExact(
    snapshotID: string,
    workspaceRoot: string,
    confirmed: boolean,
  ): Promise<void>;
  listSnapshots(): Promise<Snapshot[]>;
  deleteSnapshot(snapshotID: string): Promise<void>;
  diffSnapshots(fromID: string, toID: string): Promise<SnapshotDiff>;
  getSnapshot(id: string): Promise<Snapshot>;
  cleanupSnapshots(keepN: number, maxAgeDuration: number): Promise<number>;
}

interface SnapshotState {
  /** 快照时间线（按创建时间降序）。 */
  snapshots: Snapshot[];
  /** 当前选中的快照（详情/回滚）。 */
  selected: Snapshot | null;
  /** 两个快照之间的差异（DiffSnapshots）。 */
  diff: SnapshotDiff | null;
  /** 选择性回滚时勾选的文件路径集合（Step 7）。 */
  selectedFilePaths: Set<string>;
  /** 当前工作区根（创建快照用）。 */
  workspaceRoot: string;
  loading: boolean;
  error: string | null;
}

export const snapshotState = reactive<SnapshotState>({
  snapshots: [],
  selected: null,
  diff: null,
  selectedFilePaths: new Set(),
  workspaceRoot: "",
  loading: false,
  error: null,
});

let backend: SnapshotBackend | null = null;
let stopLocalHistory: (() => void) | null = null;
let localHistoryQueue: Promise<void> = Promise.resolve();

// 懒加载 Wails bindings（services.SnapshotService）。
async function getBackend(): Promise<SnapshotBackend> {
  if (backend) return backend;
  // 使用字面量路径（无 @vite-ignore），让 vite 将 bindings 打包为 chunk。
  const mod = await import("../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/snapshotservice.js");
  backend = {
    createSnapshot: (root, reason) =>
      mod.CreateSnapshot(root, reason) as Promise<Snapshot>,
    restoreSnapshot: (id, root) =>
      mod.RestoreSnapshot(id, root) as Promise<void>,
    restorePartial: (id, root, paths) =>
      mod.RestorePartial(id, root, paths) as Promise<void>,
    calculateRestoreDiff: (id, root) =>
      mod.CalculateRestoreDiff(id, root) as Promise<RestoreDiff>,
    restoreSnapshotExact: (id, root, confirmed) =>
      mod.RestoreSnapshotExact(id, root, confirmed) as Promise<void>,
    listSnapshots: () => mod.ListSnapshots() as Promise<Snapshot[]>,
    deleteSnapshot: (id) => mod.DeleteSnapshot(id) as Promise<void>,
    diffSnapshots: (from, to) =>
      mod.DiffSnapshots(from, to) as Promise<SnapshotDiff>,
    getSnapshot: (id) => mod.GetSnapshot(id) as Promise<Snapshot>,
    cleanupSnapshots: (keepN, maxAgeDuration) =>
      mod.CleanupSnapshots(keepN, maxAgeDuration) as Promise<number>,
  };
  return backend;
}

// 测试注入。
export function setSnapshotBackend(b: SnapshotBackend): void {
  backend = b;
}

/** 设置工作区根（创建快照前调用）。 */
export function setSnapshotWorkspaceRoot(root: string): void {
  snapshotState.workspaceRoot = root;
  snapshotState.snapshots = snapshotState.snapshots.filter((snapshot) =>
    sameWorkspacePath(snapshot.workspaceRoot, root),
  );
  if (snapshotState.selected && !sameWorkspacePath(snapshotState.selected.workspaceRoot, root)) {
    snapshotState.selected = null;
    snapshotState.selectedFilePaths = new Set();
  }
  initLocalHistory();
}

function normalizeWorkspacePath(path: string): string {
  let normalized = path.trim().replace(/\\/g, "/").replace(/\/+$/, "");
  if (/^[A-Za-z]:\//.test(normalized) || normalized.startsWith("//")) {
    normalized = normalized.toLowerCase();
  }
  return normalized;
}

function sameWorkspacePath(left: string, right: string): boolean {
  return normalizeWorkspacePath(left) === normalizeWorkspacePath(right);
}

function isPathInWorkspace(path: string, workspaceRoot: string): boolean {
  const normalizedPath = normalizeWorkspacePath(path);
  const normalizedRoot = normalizeWorkspacePath(workspaceRoot);
  return normalizedPath === normalizedRoot || normalizedPath.startsWith(`${normalizedRoot}/`);
}

async function captureLocalHistory(workspaceRoot: string): Promise<void> {
  if (!sameWorkspacePath(snapshotState.workspaceRoot, workspaceRoot)) return;
  try {
    const b = await getBackend();
    const snapshot = await b.createSnapshot(workspaceRoot, "file-save");
    if (
      sameWorkspacePath(snapshotState.workspaceRoot, workspaceRoot) &&
      !snapshotState.snapshots.some(({ id }) => id === snapshot.id)
    ) {
      snapshotState.snapshots.unshift(snapshot);
    }
  } catch (e: unknown) {
    snapshotState.error = e instanceof Error ? e.message : String(e);
    console.error("Failed to capture local history:", e);
  }
}

export function initLocalHistory(): void {
  if (stopLocalHistory) return;
  if (typeof window !== "undefined" && window.location.hash.includes("ai-window")) return;
  stopLocalHistory = Events.On("file:saved", (event: FileSavedEvent) => {
    const workspaceRoot = snapshotState.workspaceRoot;
    const filePath = event.data;
    if (!workspaceRoot || typeof filePath !== "string" || !isPathInWorkspace(filePath, workspaceRoot)) return;
    localHistoryQueue = localHistoryQueue.then(() => captureLocalHistory(workspaceRoot));
  });
}

// ---- Step 2: 创建快照 ----

export async function createSnapshot(
  reason: SnapshotReason = "manual",
): Promise<Snapshot | null> {
  if (!snapshotState.workspaceRoot) {
    notifyError("workspace root not set");
    return null;
  }
  snapshotState.loading = true;
  snapshotState.error = null;
  try {
    const b = await getBackend();
    const snap = await b.createSnapshot(snapshotState.workspaceRoot, reason);
    snapshotState.snapshots.unshift(snap);
    return snap;
  } catch (e: unknown) {
    snapshotState.error = e instanceof Error ? e.message : String(e);
    notifyError(snapshotState.error);
    return null;
  } finally {
    snapshotState.loading = false;
  }
}

// ---- Step 2: 列出快照 ----

export async function listSnapshots(): Promise<void> {
  snapshotState.loading = true;
  snapshotState.error = null;
  try {
    const b = await getBackend();
    const snapshots = await b.listSnapshots();
    snapshotState.snapshots = snapshots.filter((snapshot) =>
      sameWorkspacePath(snapshot.workspaceRoot, snapshotState.workspaceRoot),
    );
  } catch (e: unknown) {
    snapshotState.error = e instanceof Error ? e.message : String(e);
    notifyError(snapshotState.error);
  } finally {
    snapshotState.loading = false;
  }
}

// ---- Step 2: 获取详情 ----

export async function selectSnapshot(id: string): Promise<void> {
  snapshotState.loading = true;
  snapshotState.error = null;
  try {
    const b = await getBackend();
    snapshotState.selected = await b.getSnapshot(id);
    snapshotState.selectedFilePaths = new Set();
  } catch (e: unknown) {
    snapshotState.error = e instanceof Error ? e.message : String(e);
    notifyError(snapshotState.error);
  } finally {
    snapshotState.loading = false;
  }
}

// ---- Step 2: 删除快照 ----

export async function deleteSnapshot(id: string): Promise<void> {
  snapshotState.loading = true;
  snapshotState.error = null;
  try {
    const b = await getBackend();
    await b.deleteSnapshot(id);
    snapshotState.snapshots = snapshotState.snapshots.filter((s) => s.id !== id);
    if (snapshotState.selected?.id === id) {
      snapshotState.selected = null;
    }
  } catch (e: unknown) {
    snapshotState.error = e instanceof Error ? e.message : String(e);
    notifyError(snapshotState.error);
  } finally {
    snapshotState.loading = false;
  }
}

// ---- Step 2: 差异比较 ----

export async function diffSnapshots(fromID: string, toID: string): Promise<void> {
  snapshotState.loading = true;
  snapshotState.error = null;
  try {
    const b = await getBackend();
    snapshotState.diff = await b.diffSnapshots(fromID, toID);
  } catch (e: unknown) {
    snapshotState.error = e instanceof Error ? e.message : String(e);
    notifyError(snapshotState.error);
  } finally {
    snapshotState.loading = false;
  }
}

// ---- Step 7: 选择性回滚 ----

/** 切换文件勾选（Step 7）。 */
export function toggleFileSelection(path: string): void {
  if (snapshotState.selectedFilePaths.has(path)) {
    snapshotState.selectedFilePaths.delete(path);
  } else {
    snapshotState.selectedFilePaths.add(path);
  }
}

/** 全选/取消全选当前快照文件（Step 7）。 */
export function toggleSelectAllFiles(selectAll: boolean): void {
  if (!snapshotState.selected) return;
  if (selectAll) {
    snapshotState.selectedFilePaths = new Set(
      snapshotState.selected.files.map((f) => f.path),
    );
  } else {
    snapshotState.selectedFilePaths = new Set();
  }
}

/** 选择性回滚勾选的文件（Step 7）。 */
export async function restorePartial(filePaths?: string[]): Promise<boolean> {
  if (!snapshotState.selected || !snapshotState.workspaceRoot) {
    notifyError("no snapshot or workspace root");
    return false;
  }
  const paths = filePaths ?? Array.from(snapshotState.selectedFilePaths);
  if (paths.length === 0) {
    notifyError("no files selected");
    return false;
  }
  snapshotState.loading = true;
  snapshotState.error = null;
  try {
    const b = await getBackend();
    await b.restorePartial(
      snapshotState.selected.id,
      snapshotState.workspaceRoot,
      paths,
    );
    return true;
  } catch (e: unknown) {
    snapshotState.error = e instanceof Error ? e.message : String(e);
    notifyError(snapshotState.error);
    return false;
  } finally {
    snapshotState.loading = false;
  }
}

// ---- Step 2: 整体回滚 ----

/**
 * Restores only the files recorded in the snapshot.
 *
 * GOAL-P1-01: this is **partial** semantics. Files created after the snapshot
 * are left in place, so the workspace does not end up in the snapshot's state.
 * The name reflects that. Callers wanting the workspace to actually match the
 * snapshot must use `calculateRestoreDiff` + `restoreSnapshotExact`.
 */
export async function restoreSnapshotFiles(snapshotID: string): Promise<boolean> {
  if (!snapshotState.workspaceRoot) {
    notifyError("workspace root not set");
    return false;
  }
  snapshotState.loading = true;
  snapshotState.error = null;
  try {
    const b = await getBackend();
    await b.restoreSnapshot(snapshotID, snapshotState.workspaceRoot);
    return true;
  } catch (e: unknown) {
    snapshotState.error = e instanceof Error ? e.message : String(e);
    notifyError(snapshotState.error);
    return false;
  } finally {
    snapshotState.loading = false;
  }
}

/**
 * Deprecated alias kept so existing callers and tests keep compiling.
 *
 * GOAL-P1-01: the old name claimed whole-workspace restore while the
 * implementation only wrote snapshot files back. Prefer
 * `restoreSnapshotFiles` (explicitly partial) or `restoreSnapshotExact`.
 */
export const restoreSnapshot = restoreSnapshotFiles;

/**
 * Previews what an exact restore would change. Read-only: it never modifies the
 * workspace, so it is safe to call before asking the user to confirm.
 *
 * GOAL-P1-01 AC 2: `addedAfterSnapshot` must be shown to the user, because
 * `restoreSnapshotExact` deletes those files permanently.
 */
export async function calculateRestoreDiff(
  snapshotID: string,
): Promise<RestoreDiff | null> {
  if (!snapshotState.workspaceRoot) {
    notifyError("workspace root not set");
    return null;
  }
  snapshotState.loading = true;
  snapshotState.error = null;
  try {
    const b = await getBackend();
    const diff = await b.calculateRestoreDiff(snapshotID, snapshotState.workspaceRoot);
    return {
      addedAfterSnapshot: diff.addedAfterSnapshot ?? [],
      modifiedSinceSnapshot: diff.modifiedSinceSnapshot ?? [],
      removedFromWorkspace: diff.removedFromWorkspace ?? [],
    };
  } catch (e: unknown) {
    snapshotState.error = e instanceof Error ? e.message : String(e);
    notifyError(snapshotState.error);
    return null;
  } finally {
    snapshotState.loading = false;
  }
}

/**
 * Restores the workspace to the exact snapshot state, deleting files created
 * after the snapshot.
 *
 * GOAL-P1-01: `confirmed` is passed through to the backend, which refuses the
 * call when it is false. The flag is deliberately not defaulted to true — the
 * caller has to have actually shown the diff.
 */
export async function restoreSnapshotExact(
  snapshotID: string,
  confirmed: boolean,
): Promise<boolean> {
  if (!snapshotState.workspaceRoot) {
    notifyError("workspace root not set");
    return false;
  }
  snapshotState.loading = true;
  snapshotState.error = null;
  try {
    const b = await getBackend();
    await b.restoreSnapshotExact(snapshotID, snapshotState.workspaceRoot, confirmed);
    return true;
  } catch (e: unknown) {
    snapshotState.error = e instanceof Error ? e.message : String(e);
    notifyError(snapshotState.error);
    return false;
  } finally {
    snapshotState.loading = false;
  }
}

// ---- Step 5: 清理策略 ----

export function millisecondsToGoDuration(maxAgeMs: number): number {
  if (!Number.isFinite(maxAgeMs) || maxAgeMs < 0) {
    throw new RangeError("snapshot max age must be a non-negative finite millisecond value");
  }
  return Math.trunc(maxAgeMs) * Duration.Millisecond;
}

export async function cleanupSnapshots(
  keepN: number,
  maxAgeMs: number,
): Promise<number> {
  snapshotState.loading = true;
  snapshotState.error = null;
  try {
    const b = await getBackend();
    const deleted = await b.cleanupSnapshots(
      keepN,
      millisecondsToGoDuration(maxAgeMs),
    );
    if (deleted > 0) {
      notifySuccess(`cleaned up ${deleted} snapshots`);
      await listSnapshots();
    }
    return deleted;
  } catch (e: unknown) {
    snapshotState.error = e instanceof Error ? e.message : String(e);
    notifyError(snapshotState.error);
    return 0;
  } finally {
    snapshotState.loading = false;
  }
}

/** 重置 store。 */
export function resetSnapshotStore(): void {
  stopLocalHistory?.();
  stopLocalHistory = null;
  localHistoryQueue = Promise.resolve();
  snapshotState.snapshots = [];
  snapshotState.selected = null;
  snapshotState.diff = null;
  snapshotState.selectedFilePaths = new Set();
  snapshotState.workspaceRoot = "";
  snapshotState.loading = false;
  snapshotState.error = null;
}

import.meta.hot?.dispose(resetSnapshotStore);
