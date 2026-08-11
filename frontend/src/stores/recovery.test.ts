import { beforeEach, afterEach, describe, expect, it, vi } from "vitest";
import type { RecoverableFile } from "@/types";

// GOAL-P0-03 store tests.
//
// Baseline defect: unsaved editor content lived only in the reactive
// `editorState.openFiles` array, so an abnormal exit discarded every dirty
// buffer. These tests lock the journal contract: debounced writes, clear-on-save,
// baseline capture for conflict detection, and fail-soft behaviour so a journal
// outage never blocks editing or startup.

const svc = vi.hoisted(() => ({
  saveDirtyBuffer: vi.fn(),
  clearDirtyBuffer: vi.fn(),
  clearWindowJournal: vi.fn(),
  scanRecoverable: vi.fn(),
  computeBaseline: vi.fn(),
  discardRecoveredSession: vi.fn(),
  completeRecovery: vi.fn(),
  acknowledgeRecoveryFailure: vi.fn(),
  setJournalEnabled: vi.fn(),
  isJournalEnabled: vi.fn(),
}));

vi.mock("@/api/services", () => ({ recoveryService: svc }));

// The store surfaces journal write failures through notifyWarning, not
// console.warn: a silent failure would leave the user believing their unsaved
// work is protected when it is not.
const notify = vi.hoisted(() => ({ notifyWarning: vi.fn() }));

const editor = vi.hoisted(() => ({
  editorState: {
    openFiles: [] as Array<{
      path: string;
      name: string;
      content: string;
      originalContent: string;
      language: string;
      isDirty: boolean;
    }>,
    activeFilePath: null as string | null,
  },
  openFile: vi.fn(),
  updateContent: vi.fn().mockReturnValue(true),
  closeFile: vi.fn(),
}));

vi.mock("@/lib/notifications", () => ({ notifyWarning: notify.notifyWarning }));
vi.mock("@/stores/editor", () => editor);

const recoverable = (over: Partial<RecoverableFile> = {}): RecoverableFile => ({
  path: "/ws/a.ts",
  windowId: "w-1",
  status: "clean",
  content: "dirty",
  diskContent: "disk",
  encoding: "utf-8",
  eol: "lf",
  updatedAt: 1,
  baselineHash: "h1",
  currentHash: "h1",
  ...over,
});

async function loadStore() {
  return await import("@/stores/recovery");
}

describe("recovery store (GOAL-P0-03)", () => {
  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();
    vi.useFakeTimers();
    svc.saveDirtyBuffer.mockResolvedValue(undefined);
    svc.clearDirtyBuffer.mockResolvedValue(undefined);
    svc.computeBaseline.mockResolvedValue({
      path: "/ws/a.ts", mtime: 111, hash: "h1", exists: true,
    });
    svc.scanRecoverable.mockResolvedValue({
      workspaceRoot: "/ws", files: [], corrupt: [], totalBytes: 0,
    });
    svc.discardRecoveredSession.mockResolvedValue(undefined);
    svc.completeRecovery.mockResolvedValue(undefined);
    svc.acknowledgeRecoveryFailure.mockResolvedValue(undefined);
    editor.editorState.openFiles = [];
    editor.editorState.activeFilePath = null;
    editor.openFile.mockReset();
    editor.openFile.mockImplementation((path: string, content: string) => {
      if (!editor.editorState.openFiles.some((file) => file.path === path)) {
        editor.editorState.openFiles.push({
          path,
          name: path.split("/").pop() ?? path,
          content,
          originalContent: content,
          language: "typescript",
          isDirty: false,
        });
      }
      editor.editorState.activeFilePath = path;
    });
    editor.updateContent.mockReset();
    editor.updateContent.mockImplementation((path: string, content: string) => {
      const file = editor.editorState.openFiles.find((candidate) => candidate.path === path);
      if (!file) return false;
      file.content = content;
      file.isDirty = content !== file.originalContent;
      return true;
    });
    editor.closeFile.mockReset();
    editor.closeFile.mockImplementation((path: string) => {
      editor.editorState.openFiles = editor.editorState.openFiles.filter(
        (candidate) => candidate.path !== path,
      );
      if (editor.editorState.activeFilePath === path) {
        editor.editorState.activeFilePath = null;
      }
    });
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("debounces journal writes so typing does not write once per keystroke", async () => {
    const store = await loadStore();
    await store.captureBaseline("/ws/a.ts");

    // Simulate a typing burst: each keystroke reschedules the pending write.
    store.scheduleJournalWrite("/ws/a.ts", "a");
    store.scheduleJournalWrite("/ws/a.ts", "ab");
    store.scheduleJournalWrite("/ws/a.ts", "abc");

    expect(svc.saveDirtyBuffer).not.toHaveBeenCalled();

    await vi.advanceTimersByTimeAsync(store.JOURNAL_DEBOUNCE_MS + 10);

    // One write, carrying only the final content.
    expect(svc.saveDirtyBuffer).toHaveBeenCalledTimes(1);
    const [, path, content] = svc.saveDirtyBuffer.mock.calls[0]!;
    expect(path).toBe("/ws/a.ts");
    expect(content).toBe("abc");
  });

  it("sends the captured baseline so the backend can detect disk drift", async () => {
    const store = await loadStore();
    svc.computeBaseline.mockResolvedValue({
      path: "/ws/a.ts", mtime: 4242, hash: "baseline-hash", exists: true,
    });
    await store.captureBaseline("/ws/a.ts");

    store.scheduleJournalWrite("/ws/a.ts", "edited");
    await vi.advanceTimersByTimeAsync(store.JOURNAL_DEBOUNCE_MS + 10);

    const call = svc.saveDirtyBuffer.mock.calls[0]!;
    // (windowID, path, content, encoding, eol, baselineMtime, baselineHash)
    expect(call[5]).toBe(4242);
    expect(call[6]).toBe("baseline-hash");
  });

  it("cancels a pending write and clears the record when the file is saved", async () => {
    const store = await loadStore();
    await store.captureBaseline("/ws/a.ts");

    store.scheduleJournalWrite("/ws/a.ts", "unsaved");
    await store.clearJournalForSaved("/ws/a.ts");

    // The pending debounce must not fire after the content already reached disk.
    await vi.advanceTimersByTimeAsync(store.JOURNAL_DEBOUNCE_MS + 50);

    expect(svc.saveDirtyBuffer).not.toHaveBeenCalled();
    expect(svc.clearDirtyBuffer).toHaveBeenCalledWith(
      store.getWindowID(), "/ws/a.ts",
    );
  });

  it("re-captures the baseline after save so later edits compare against the new disk state", async () => {
    const store = await loadStore();
    svc.computeBaseline.mockResolvedValueOnce({
      path: "/ws/a.ts", mtime: 1, hash: "old", exists: true,
    });
    await store.captureBaseline("/ws/a.ts");

    svc.computeBaseline.mockResolvedValueOnce({
      path: "/ws/a.ts", mtime: 2, hash: "new", exists: true,
    });
    await store.clearJournalForSaved("/ws/a.ts");

    store.scheduleJournalWrite("/ws/a.ts", "next edit");
    await vi.advanceTimersByTimeAsync(store.JOURNAL_DEBOUNCE_MS + 10);

    const call = svc.saveDirtyBuffer.mock.calls[0]!;
    expect(call[6]).toBe("new");
  });

  it("keeps editing alive when the journal write fails, and tells the user", async () => {
    const store = await loadStore();
    svc.saveDirtyBuffer.mockRejectedValue(new Error("disk full"));
    await store.captureBaseline("/ws/a.ts");

    store.scheduleJournalWrite("/ws/a.ts", "x");
    // Must not reject: a journal outage cannot break typing. An unhandled
    // rejection here would surface as a failed test.
    await vi.advanceTimersByTimeAsync(store.JOURNAL_DEBOUNCE_MS + 10);

    // The failure must be visible. Swallowing it would leave the user believing
    // their unsaved work is being protected when it is not.
    expect(notify.notifyWarning).toHaveBeenCalledTimes(1);
    expect(String(notify.notifyWarning.mock.calls[0]?.[0])).toContain("disk full");

    // A second failure on the same path must not re-nag: one warning per path
    // until a write succeeds again.
    store.scheduleJournalWrite("/ws/a.ts", "y");
    await vi.advanceTimersByTimeAsync(store.JOURNAL_DEBOUNCE_MS + 10);
    expect(notify.notifyWarning).toHaveBeenCalledTimes(1);
  });

  it("returns an empty scan instead of throwing when the journal is unreadable", async () => {
    const store = await loadStore();
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    svc.scanRecoverable.mockRejectedValue(new Error("corrupt dir"));

    const scan = await store.scanRecoverable();

    // Startup can continue, but automatic disk writes remain fail-closed until
    // the user explicitly acknowledges that recovery could not be inspected.
    expect(scan.files).toEqual([]);
    expect(store.recoveryState.phase).toBe("failed");
    expect(store.isRecoveryPending()).toBe(true);
    expect(store.isAutomaticRecoveryWriteAllowed()).toBe(false);
    expect(store.recoveryState.visible).toBe(true);
    expect(store.recoveryState.error).toBe("corrupt dir");
    warn.mockRestore();
  });

  it("requires an explicit acknowledgement after a scan failure", async () => {
    const store = await loadStore();
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    svc.scanRecoverable.mockRejectedValue(new Error("corrupt dir"));
    await store.scanRecoverable();

    await expect(store.finishRecovery()).resolves.toBe(true);

    expect(svc.acknowledgeRecoveryFailure).toHaveBeenCalledOnce();
    expect(store.recoveryState.phase).toBe("resolved");
    expect(store.recoveryState.visible).toBe(false);
    warn.mockRestore();
  });

  it("holds auto-save while a recovery decision is pending and releases it after", async () => {
    const store = await loadStore();
    svc.scanRecoverable.mockResolvedValue({
      workspaceRoot: "/ws",
      files: [recoverable()],
      corrupt: [],
      totalBytes: 5,
    });

    expect(store.recoveryState.phase).toBe("scanning");
    expect(store.isAutomaticRecoveryWriteAllowed()).toBe(false);
    const scanning = store.scanRecoverable();
    expect(store.recoveryState.phase).toBe("scanning");
    expect(store.isRecoveryPending()).toBe(true);
    await scanning;
    // Auto-save must not overwrite disk before the user decides.
    expect(store.recoveryState.phase).toBe("pending");
    expect(store.isRecoveryPending()).toBe(true);

    await store.resolveRecoverableFile(recoverable(), "keep-disk");
    await store.finishRecovery();
    expect(svc.completeRecovery).toHaveBeenCalledWith(
      [{ windowId: "w-1", path: "/ws/a.ts" }],
      [],
    );
    expect(store.recoveryState.phase).toBe("resolved");
    expect(store.isRecoveryPending()).toBe(false);
    expect(store.isAutomaticRecoveryWriteAllowed()).toBe(true);
  });

  it("does not hold auto-save when there is nothing to recover", async () => {
    const store = await loadStore();
    await store.scanRecoverable();
    expect(store.recoveryState.phase).toBe("resolved");
    expect(store.isRecoveryPending()).toBe(false);
    expect(store.isAutomaticRecoveryWriteAllowed()).toBe(true);
  });

  it("partitions clean buffers from conflicts so conflicts are never auto-applied", async () => {
    const store = await loadStore();
    const { restorable, needsDecision } = store.partitionRecoverable([
      recoverable({ path: "/ws/clean.ts", status: "clean" }),
      recoverable({ path: "/ws/conflict.ts", status: "conflict" }),
      recoverable({ path: "/ws/gone.ts", status: "missing" }),
    ]);

    expect(restorable.map((f) => f.path)).toEqual(["/ws/clean.ts"]);
    // "missing" must not be auto-restored either: the file was deleted on
    // purpose as far as the journal can tell.
    expect(needsDecision.map((f) => f.path)).toEqual(["/ws/conflict.ts", "/ws/gone.ts"]);
  });

  it("restores a conflict into a dirty editor buffer without writing disk", async () => {
    const store = await loadStore();
    const file = recoverable({
      path: "/ws/conflict.ts",
      status: "conflict",
      content: "recovered work",
      diskContent: "newer disk work",
    });
    svc.scanRecoverable.mockResolvedValue({
      workspaceRoot: "/ws", files: [file], corrupt: [], totalBytes: 14,
    });
    await store.scanRecoverable();

    await expect(store.resolveRecoverableFile(file, "merge")).resolves.toBe(true);

    expect(editor.openFile).toHaveBeenCalledWith(file.path, "newer disk work");
    expect(editor.updateContent).toHaveBeenCalledWith(file.path, "recovered work");
    expect(svc.saveDirtyBuffer).toHaveBeenCalledWith(
      store.getWindowID(),
      file.path,
      "recovered work",
      "utf-8",
      "lf",
      0,
      "",
    );
    expect(svc.clearDirtyBuffer).not.toHaveBeenCalledWith(file.windowId, file.path);
    expect(store.isRecoveryPending()).toBe(true);
    expect(store.recoveryState.decisions).toHaveLength(1);

    await expect(store.finishRecovery()).resolves.toBe(true);
    expect(svc.completeRecovery).toHaveBeenCalledWith(
      [{ windowId: file.windowId, path: file.path }],
      [],
    );
    expect(store.isRecoveryPending()).toBe(false);
  });

  it("keeps the disk version without opening the recovered conflict", async () => {
    const store = await loadStore();
    const file = recoverable({ status: "conflict" });
    svc.scanRecoverable.mockResolvedValue({
      workspaceRoot: "/ws", files: [file], corrupt: [], totalBytes: 5,
    });
    await store.scanRecoverable();

    await expect(store.resolveRecoverableFile(file, "keep-disk")).resolves.toBe(true);

    expect(editor.openFile).not.toHaveBeenCalled();
    expect(editor.updateContent).not.toHaveBeenCalled();
    expect(svc.clearDirtyBuffer).not.toHaveBeenCalledWith(file.windowId, file.path);
    expect(store.recoveryState.decisions).toHaveLength(1);
  });

  it("can undo a keep-disk decision before final commit", async () => {
    const store = await loadStore();
    const file = recoverable({ status: "conflict" });
    svc.scanRecoverable.mockResolvedValue({
      workspaceRoot: "/ws", files: [file], corrupt: [], totalBytes: 5,
    });
    await store.scanRecoverable();
    await store.resolveRecoverableFile(file, "keep-disk");

    await expect(store.undoLastRecoveryDecision()).resolves.toBe(true);

    expect(store.recoveryState.scan.files).toEqual([file]);
    expect(store.recoveryState.decisions).toEqual([]);
    expect(store.recoveryState.phase).toBe("pending");
    expect(svc.completeRecovery).not.toHaveBeenCalled();
  });

  it("keeps the original crash snapshot when the independent journal flush fails", async () => {
    const store = await loadStore();
    const file = recoverable({ status: "conflict" });
    svc.scanRecoverable.mockResolvedValue({
      workspaceRoot: "/ws", files: [file], corrupt: [], totalBytes: 5,
    });
    await store.scanRecoverable();
    svc.saveDirtyBuffer.mockRejectedValueOnce(new Error("disk full"));

    await expect(store.resolveRecoverableFile(file, "merge")).resolves.toBe(false);

    expect(store.recoveryState.scan.files).toEqual([file]);
    expect(store.recoveryState.decisions).toEqual([]);
    expect(store.recoveryState.phase).toBe("pending");
    expect(svc.clearDirtyBuffer).not.toHaveBeenCalledWith(file.windowId, file.path);
  });

  it("does not show the same recovery prompt after decisions commit and a clean rescan", async () => {
    const store = await loadStore();
    const file = recoverable();
    svc.scanRecoverable.mockResolvedValueOnce({
      workspaceRoot: "/ws", files: [file], corrupt: [], totalBytes: 5,
    });
    await store.scanRecoverable();
    await store.resolveRecoverableFile(file, "keep-disk");
    await store.finishRecovery();

    svc.scanRecoverable.mockResolvedValueOnce({
      workspaceRoot: "/ws", files: [], corrupt: [], totalBytes: 0,
    });
    await store.scanRecoverable();

    expect(store.recoveryState.phase).toBe("resolved");
    expect(store.recoveryState.visible).toBe(false);
    expect(store.recoveryState.scan.files).toEqual([]);
  });

  it("normalizes an unexpected backend status to conflict rather than trusting it", async () => {
    const store = await loadStore();
    svc.scanRecoverable.mockResolvedValue({
      workspaceRoot: "/ws",
      // A status this frontend does not know must not be treated as safe.
      files: [{ ...recoverable(), status: "something-new" }],
      corrupt: [],
      totalBytes: 1,
    });

    const scan = await store.scanRecoverable();
    expect(scan.files[0]!.status).toBe("conflict");
  });

  it("tolerates a null files array from the backend", async () => {
    const store = await loadStore();
    svc.scanRecoverable.mockResolvedValue({
      workspaceRoot: "/ws", files: null, corrupt: null, totalBytes: 0,
    });

    const scan = await store.scanRecoverable();
    expect(scan.files).toEqual([]);
    expect(scan.corrupt).toEqual([]);
  });

  it("releases the auto-save hold after the user discards the session", async () => {
    const store = await loadStore();
    svc.scanRecoverable.mockResolvedValue({
      workspaceRoot: "/ws", files: [recoverable()], corrupt: [], totalBytes: 5,
    });
    await store.scanRecoverable();
    expect(store.isRecoveryPending()).toBe(true);

    await store.discardRecoveredSession("w-1");

    expect(svc.completeRecovery).toHaveBeenCalledWith(
      [{ windowId: "w-1", path: "/ws/a.ts" }],
      [],
    );
    expect(store.isRecoveryPending()).toBe(false);
  });

  it("keeps the recovery gate closed when decision commit fails", async () => {
    const store = await loadStore();
    svc.scanRecoverable.mockResolvedValue({
      workspaceRoot: "/ws", files: [recoverable()], corrupt: [], totalBytes: 5,
    });
    await store.scanRecoverable();
    svc.completeRecovery.mockRejectedValue(new Error("permission denied"));

    await store.discardRecoveredSession("w-1");

    expect(store.isRecoveryPending()).toBe(true);
    expect(store.recoveryState.phase).toBe("pending");
    expect(store.recoveryState.error).toBe("permission denied");
    expect(svc.clearDirtyBuffer).not.toHaveBeenCalledWith("w-1", "/ws/a.ts");
  });

  it("scopes records to this window so two windows cannot clobber each other", async () => {
    const store = await loadStore();
    await store.captureBaseline("/ws/a.ts");
    store.scheduleJournalWrite("/ws/a.ts", "from this window");
    await vi.advanceTimersByTimeAsync(store.JOURNAL_DEBOUNCE_MS + 10);

    expect(svc.saveDirtyBuffer.mock.calls[0]![0]).toBe(store.getWindowID());
    expect(store.getWindowID()).toMatch(/^w-/);
  });

  it("writes without a baseline rather than losing the buffer when baseline capture failed", async () => {
    const store = await loadStore();
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    svc.computeBaseline.mockRejectedValue(new Error("stat failed"));
    await store.captureBaseline("/ws/a.ts");

    store.scheduleJournalWrite("/ws/a.ts", "content");
    await vi.advanceTimersByTimeAsync(store.JOURNAL_DEBOUNCE_MS + 10);

    // The record must still be written: losing the baseline degrades conflict
    // detection, but dropping the journal entry loses the user's work.
    expect(svc.saveDirtyBuffer).toHaveBeenCalledTimes(1);
    const call = svc.saveDirtyBuffer.mock.calls[0]!;
    expect(call[2]).toBe("content");
    warn.mockRestore();
  });

  it("stops journaling a closed file", async () => {
    const store = await loadStore();
    await store.captureBaseline("/ws/a.ts");
    store.scheduleJournalWrite("/ws/a.ts", "typed");

    store.forgetBaseline("/ws/a.ts");
    await vi.advanceTimersByTimeAsync(store.JOURNAL_DEBOUNCE_MS + 50);

    expect(svc.saveDirtyBuffer).not.toHaveBeenCalled();
  });

  it("flushes a pending write immediately when asked", async () => {
    const store = await loadStore();
    await store.captureBaseline("/ws/a.ts");
    store.scheduleJournalWrite("/ws/a.ts", "pending");

    await store.flushPending("/ws/a.ts", "final");

    expect(svc.saveDirtyBuffer).toHaveBeenCalledTimes(1);
    expect(svc.saveDirtyBuffer.mock.calls[0]![2]).toBe("final");

    // The superseded debounce must not fire a second write.
    await vi.advanceTimersByTimeAsync(store.JOURNAL_DEBOUNCE_MS + 50);
    expect(svc.saveDirtyBuffer).toHaveBeenCalledTimes(1);
  });
});
