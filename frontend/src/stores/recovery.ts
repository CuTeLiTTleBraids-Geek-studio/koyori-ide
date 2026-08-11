// GOAL-P0-03: hot-exit / dirty-buffer crash recovery (frontend half).
//
// Baseline defect: unsaved editor content lived only in the reactive
// `editorState.openFiles` array. A crash, WebView kill, or power loss discarded
// every dirty buffer with no journal to recover from, and CrashService only
// persists panic stack traces — it is not a content backup.
//
// This module journals dirty buffers to the backend RecoveryService and clears
// them once the content reaches disk. Writes are debounced so that typing does
// not produce one disk write per keystroke.

// Koyori IDE 模块 · Recovery；交互服务：恢复（RecoveryService）。
// 喵，这是 Koyori IDE 的 Recovery 模块（前端实现）~
import { reactive } from "vue";
import { recoveryService } from "@/api/services";
import type { RecoverableFile, RecoveryScan } from "@/types";
import type { OpenFile } from "@/stores/editor";
import { errorMessage } from "@/lib/errors";

/** Debounce window for journal writes. Long enough to coalesce typing bursts,
 * short enough that a crash loses at most this much work. */
export const JOURNAL_DEBOUNCE_MS = 900;

/**
 * Per-window identity. Journal records are keyed by window so two windows
 * editing the same workspace cannot overwrite each other's recovery state.
 *
 * Generated per page load rather than persisted: a reload is a new window from
 * the journal's perspective, and reusing an ID across reloads would let a fresh
 * window silently adopt (and then clear) a previous window's unsaved work.
 */
const windowID = `w-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`;

export function getWindowID(): string {
  return windowID;
}

interface BaselineEntry {
  mtime: number;
  hash: string;
}

/** Baselines captured at open/save time, keyed by path. Used to detect whether
 * the file changed on disk while the journal record was pending. */
const baselines = new Map<string, BaselineEntry>();
const pendingTimers = new Map<string, ReturnType<typeof setTimeout>>();

/** Paths whose journal write failed a quota check. Tracked so the UI warns
 * once per path instead of on every keystroke. */
const quotaWarned = new Set<string>();

export type RecoveryPhase = "scanning" | "pending" | "resolved" | "failed";
export type RecoveryDecisionKind = "restore" | "merge" | "keep-disk";

interface RecoveryDecisionEntry {
  file: RecoverableFile;
  decision: RecoveryDecisionKind;
  previousFile: OpenFile | null;
  previousActivePath: string | null;
}

const emptyRecoveryScan = (): RecoveryScan => ({
  workspaceRoot: "",
  files: [],
  corrupt: [],
  totalBytes: 0,
});

export const recoveryState = reactive<{
  phase: RecoveryPhase;
  visible: boolean;
  scanning: boolean;
  error: string | null;
  scan: RecoveryScan;
  decisions: RecoveryDecisionEntry[];
}>({
  phase: "scanning",
  visible: false,
  scanning: false,
  error: null,
  scan: emptyRecoveryScan(),
  decisions: [],
});

/**
 * True until a recovery decision has been made. Auto-save must stay disabled
 * while this holds, otherwise auto-save would overwrite the disk copy that the
 * conflict UI is still asking the user about.
 */
export function isRecoveryPending(): boolean {
  return recoveryState.phase !== "resolved";
}

/** Automatic disk writes are fail-closed until startup recovery is resolved. */
export function isAutomaticRecoveryWriteAllowed(): boolean {
  return recoveryState.phase === "resolved";
}

/** Captures the disk baseline for a path. Call on open and after each save. */
export async function captureBaseline(path: string): Promise<void> {
  try {
    const baseline = await recoveryService.computeBaseline(path);
    baselines.set(path, { mtime: baseline.mtime, hash: baseline.hash });
  } catch (err) {
    // A missing baseline only costs conflict precision: the backend still
    // records the buffer and reports `conflict` when hashes cannot be matched.
    console.warn("[recovery] computeBaseline failed:", errorMessage(err));
  }
}

export function forgetBaseline(path: string): void {
  baselines.delete(path);
  quotaWarned.delete(path);
  const timer = pendingTimers.get(path);
  if (timer) {
    clearTimeout(timer);
    pendingTimers.delete(path);
  }
}

/**
 * Schedules a debounced journal write for a dirty buffer.
 *
 * `eol` and `encoding` are recorded so recovery restores the buffer with the
 * same line endings it had, instead of silently normalizing them.
 */
export function scheduleJournalWrite(
  path: string,
  content: string,
  opts?: { encoding?: string; eol?: string },
): void {
  const existing = pendingTimers.get(path);
  if (existing) clearTimeout(existing);

  const timer = setTimeout(() => {
    pendingTimers.delete(path);
    void flushJournalWrite(path, content, opts);
  }, JOURNAL_DEBOUNCE_MS);
  pendingTimers.set(path, timer);
}

async function writeJournal(
  path: string,
  content: string,
  opts?: { encoding?: string; eol?: string },
): Promise<void> {
  const baseline = baselines.get(path);
  const eol = opts?.eol ?? (content.includes("\r\n") ? "crlf" : "lf");
  await recoveryService.saveDirtyBuffer(
    windowID,
    path,
    content,
    opts?.encoding ?? "utf-8",
    eol,
    baseline?.mtime ?? 0,
    baseline?.hash ?? "",
  );
  quotaWarned.delete(path);
}

async function flushJournalWrite(
  path: string,
  content: string,
  opts?: { encoding?: string; eol?: string },
): Promise<void> {
  try {
    await writeJournal(path, content, opts);
  } catch (err) {
    const msg = errorMessage(err);
    // Quota rejections are user-visible exactly once per path: silently
    // dropping them would leave the user believing their work is protected.
    if (!quotaWarned.has(path)) {
      quotaWarned.add(path);
      void import("@/lib/notifications").then(({ notifyWarning }) => {
        notifyWarning(`Crash recovery is not protecting this file: ${msg}`);
      });
    }
  }
}

/**
 * Writes any pending journal entry for `path` immediately, bypassing the
 * debounce. Used before operations that must not lose the buffer.
 */
export async function flushPending(path: string, content: string): Promise<void> {
  const timer = pendingTimers.get(path);
  if (timer) {
    clearTimeout(timer);
    pendingTimers.delete(path);
  }
  await writeJournal(path, content);
}

/**
 * Clears the journal entry for a saved file and re-captures its baseline.
 *
 * Order matters: the record is removed only after the content is on disk, so a
 * crash between write and clear leaves a recoverable (and now redundant, hence
 * harmless) record rather than no record at all.
 */
export async function clearJournalForSaved(path: string): Promise<void> {
  const timer = pendingTimers.get(path);
  if (timer) {
    clearTimeout(timer);
    pendingTimers.delete(path);
  }
  try {
    await recoveryService.clearDirtyBuffer(windowID, path);
  } catch (err) {
    console.warn("[recovery] clearDirtyBuffer failed:", errorMessage(err));
  }
  await captureBaseline(path);
}

/**
 * Narrows the backend's plain-string `status` into the frontend union.
 *
 * The Wails binding types `status` as `string` because Go has no union type. An
 * unrecognized value is mapped to "conflict" rather than trusted or dropped:
 * treating an unknown state as "clean" would let a restore overwrite disk
 * content without asking, which is exactly the data loss this feature exists to
 * prevent.
 */
function narrowRecoverableFile(file: {
  path: string;
  windowId: string;
  status: string;
  content: string;
  diskContent: string;
  encoding: string;
  eol: string;
  updatedAt: number;
  baselineHash: string;
  currentHash: string;
}): RecoverableFile {
  let status: RecoverableFile["status"];
  switch (file.status) {
    case "clean":
    case "conflict":
    case "missing":
      status = file.status;
      break;
    default:
      console.warn(`[recovery] unknown status ${file.status}, treating as conflict`);
      status = "conflict";
  }
  return { ...file, status };
}

/**
 * Scans the journal for recoverable buffers left by an abnormal exit.
 *
 * Returns an empty scan (rather than throwing) when no workspace is open or the
 * journal is unreadable: a recovery failure must never block startup.
 */
export async function scanRecoverable(): Promise<RecoveryScan> {
  const empty = emptyRecoveryScan();
  recoveryState.phase = "scanning";
  recoveryState.scanning = true;
  recoveryState.error = null;
  recoveryState.visible = true;
  recoveryState.decisions = [];
  try {
    const scan = await recoveryService.scanRecoverable();
    const files = (scan.files ?? []).map(narrowRecoverableFile);
    const normalized = { ...scan, files, corrupt: scan.corrupt ?? [] };
    recoveryState.phase = files.length > 0 || normalized.corrupt.length > 0
      ? "pending"
      : "resolved";
    recoveryState.scan = normalized;
    recoveryState.visible = files.length > 0 || normalized.corrupt.length > 0;
    return normalized;
  } catch (err) {
    const message = errorMessage(err);
    console.warn("[recovery] scanRecoverable failed:", message);
    recoveryState.phase = "failed";
    recoveryState.scan = empty;
    recoveryState.error = message;
    recoveryState.visible = true;
    return empty;
  } finally {
    recoveryState.scanning = false;
  }
}

/**
 * Atomically commits every explicit decision, then re-enables automatic writes.
 * Until this succeeds, the backend keeps the original crash records intact.
 */
export async function finishRecovery(): Promise<boolean> {
  if (recoveryState.phase === "resolved") {
    recoveryState.visible = false;
    return true;
  }
  if (recoveryState.phase === "scanning") return false;
  if (recoveryState.scan.files.length > 0) {
    recoveryState.error = "Every recoverable file needs an explicit decision.";
    recoveryState.visible = true;
    return false;
  }
  try {
    if (recoveryState.phase === "failed") {
      await recoveryService.acknowledgeRecoveryFailure();
    } else {
      await recoveryService.completeRecovery(
        recoveryState.decisions.map(({ file }) => ({
          windowId: file.windowId,
          path: file.path,
        })),
        recoveryState.scan.corrupt.map((record) => record.file),
      );
    }
    recoveryState.phase = "resolved";
    recoveryState.visible = false;
    recoveryState.error = null;
    recoveryState.scan.corrupt = [];
    recoveryState.decisions = [];
    return true;
  } catch (err) {
    recoveryState.error = errorMessage(err);
    recoveryState.visible = true;
    return false;
  }
}

function removeResolvedFile(file: RecoverableFile): void {
  recoveryState.scan.files = recoveryState.scan.files.filter(
    (candidate) =>
      candidate.windowId !== file.windowId || candidate.path !== file.path,
  );
  recoveryState.phase = "pending";
  recoveryState.visible = true;
}

function addUnresolvedFile(file: RecoverableFile): void {
  if (!recoveryState.scan.files.some(
    (candidate) => candidate.windowId === file.windowId && candidate.path === file.path,
  )) {
    recoveryState.scan.files.push(file);
    recoveryState.scan.files.sort((left, right) =>
      left.path.localeCompare(right.path) || left.windowId.localeCompare(right.windowId));
  }
  recoveryState.phase = "pending";
  recoveryState.visible = true;
}

async function restoreEditorBeforeDecision(entry: RecoveryDecisionEntry): Promise<void> {
  if (entry.decision === "keep-disk") return;
  const { closeFile, editorState, openFile } = await import("@/stores/editor");
  if (entry.previousFile) {
    let current = editorState.openFiles.find((file) => file.path === entry.file.path);
    if (!current) {
      openFile(entry.previousFile.path, entry.previousFile.originalContent);
      current = editorState.openFiles.find((file) => file.path === entry.file.path);
    }
    if (!current) {
      throw new Error("Could not restore editor state: " + entry.file.path);
    }
    Object.assign(current, entry.previousFile);
    editorState.activeFilePath = entry.previousActivePath;
    if (entry.previousFile.isDirty) {
      await flushPending(entry.file.path, entry.previousFile.content);
    } else {
      await clearJournalForSaved(entry.file.path);
    }
    return;
  }
  closeFile(entry.file.path);
  await recoveryService.clearDirtyBuffer(windowID, entry.file.path);
  editorState.activeFilePath = entry.previousActivePath;
}

/**
 * Applies one explicit recovery decision without writing recovered content to
 * disk. Restore opens the current disk state as the editor baseline, then puts
 * the journal content into the buffer so it remains dirty until the user saves.
 */
export async function resolveRecoverableFile(
  file: RecoverableFile,
  decision: RecoveryDecisionKind,
): Promise<boolean> {
  if (!recoveryState.scan.files.some(
    (candidate) => candidate.windowId === file.windowId && candidate.path === file.path,
  )) return false;
  recoveryState.error = null;
  let decisionEntry: RecoveryDecisionEntry | null = null;
  try {
    if (decision === "restore" || decision === "merge") {
      const { editorState, openFile, updateContent } = await import("@/stores/editor");
      const existing = editorState.openFiles.find((candidate) => candidate.path === file.path);
      decisionEntry = {
        file,
        decision,
        previousFile: existing ? { ...existing } : null,
        previousActivePath: editorState.activeFilePath,
      };
      if (existing) {
        existing.content = file.diskContent;
        existing.originalContent = file.diskContent;
        existing.isDirty = false;
      } else {
        openFile(file.path, file.diskContent);
      }
      if (!updateContent(file.path, file.content)) {
        throw new Error(`Could not open recovered buffer: ${file.path}`);
      }
      await flushPending(file.path, file.content);
    } else {
      decisionEntry = {
        file,
        decision,
        previousFile: null,
        previousActivePath: null,
      };
    }

    recoveryState.decisions.push(decisionEntry);
    removeResolvedFile(file);
    return true;
  } catch (err) {
    if (decisionEntry) {
      try {
        await restoreEditorBeforeDecision(decisionEntry);
      } catch (restoreErr) {
        recoveryState.error = errorMessage(err) +
          "; rollback failed: " + errorMessage(restoreErr);
        recoveryState.visible = true;
        return false;
      }
    }
    recoveryState.error = errorMessage(err);
    recoveryState.visible = true;
    return false;
  }
}

/** Reverts the most recent explicit decision while the recovery dialog is open. */
export async function undoLastRecoveryDecision(): Promise<boolean> {
  const entry = recoveryState.decisions.at(-1);
  if (!entry) return false;
  recoveryState.error = null;
  try {
    await restoreEditorBeforeDecision(entry);
    recoveryState.decisions.pop();
    addUnresolvedFile(entry.file);
    return true;
  } catch (err) {
    recoveryState.error = errorMessage(err);
    recoveryState.visible = true;
    return false;
  }
}

/** Marks every listed file as explicitly keeping its current disk state. */
export async function discardAllRecoverable(): Promise<boolean> {
  recoveryState.error = null;
  for (const file of [...recoveryState.scan.files]) {
    if (!await resolveRecoverableFile(file, "keep-disk")) return false;
  }
  return true;
}

/**
 * Discards the recovered session for this window after the user declines
 * recovery, then re-enables auto-save.
 */
export async function discardRecoveredSession(targetWindowID: string): Promise<void> {
  for (const file of recoveryState.scan.files.filter(
    (candidate) => candidate.windowId === targetWindowID,
  )) {
    if (!await resolveRecoverableFile(file, "keep-disk")) return;
  }
  await finishRecovery();
}

/**
 * Partitions a scan into buffers that are safe to restore silently and buffers
 * that need a user decision.
 *
 * `clean` means the file on disk is byte-identical to the baseline the buffer
 * was edited against, so restoring cannot lose anything. Everything else —
 * `conflict` (disk changed) and `missing` (file deleted) — requires the user to
 * choose, because auto-applying either could destroy work.
 */
export function partitionRecoverable(files: RecoverableFile[]): {
  restorable: RecoverableFile[];
  needsDecision: RecoverableFile[];
} {
  const restorable: RecoverableFile[] = [];
  const needsDecision: RecoverableFile[] = [];
  for (const file of files) {
    if (file.status === "clean") restorable.push(file);
    else needsDecision.push(file);
  }
  return { restorable, needsDecision };
}

/** Test-only reset of module state. */
export function __resetRecoveryStateForTests(): void {
  for (const timer of pendingTimers.values()) clearTimeout(timer);
  pendingTimers.clear();
  baselines.clear();
  quotaWarned.clear();
  recoveryState.phase = "scanning";
  recoveryState.visible = false;
  recoveryState.scanning = false;
  recoveryState.error = null;
  recoveryState.scan = emptyRecoveryScan();
  recoveryState.decisions = [];
}
