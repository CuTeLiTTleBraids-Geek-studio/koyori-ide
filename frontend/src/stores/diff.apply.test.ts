import { beforeEach, describe, expect, it, vi } from "vitest";
import type {
  AIComment,
  DiffFileInput,
  FileDiff,
  FileReview,
  InlineComment,
  MultiFileDiff,
} from "@/types";

const recoveryMocks = vi.hoisted(() => ({
  captureBaseline: vi.fn().mockResolvedValue(undefined),
  clearJournalForSaved: vi.fn().mockResolvedValue(undefined),
  forgetBaseline: vi.fn(),
  scheduleJournalWrite: vi.fn(),
}));
const fileTreeRefreshMock = vi.hoisted(() => vi.fn());

vi.mock("@/stores/recovery", () => recoveryMocks);
vi.mock("@/stores/fileTreeRefresh", () => ({
  notifyFileTreeRefresh: fileTreeRefreshMock,
}));
vi.mock("@/lib/notifications", () => ({
  notifyError: vi.fn(),
  notifySuccess: vi.fn(),
}));

const { applyAll, applyFile, diffState, setDiffBackend } = await import("@/stores/diff");
const { editorState } = await import("@/stores/editor");

function fileDiff(path: string, oldContent: string, newContent: string): FileDiff {
  return {
    path,
    oldPath: path,
    oldContent,
    newContent,
    hunks: [],
    addedLines: 1,
    removedLines: 1,
  };
}

function multiDiff(files: FileDiff[]): MultiFileDiff {
  return {
    files,
    totalAdded: files.reduce((sum, file) => sum + file.addedLines, 0),
    totalRemoved: files.reduce((sum, file) => sum + file.removedLines, 0),
  };
}

function backendWithTransaction(result: {
  applied: boolean;
  appliedFiles?: string[];
  failureReason?: string;
  conflicts?: string[];
  rollbackAttempted?: boolean;
  rolledBack?: boolean;
  transactionId?: string;
  fileHashes?: Record<string, string>;
}) {
  return {
    computeFileDiff: vi.fn(async (path: string, oldContent: string, newContent: string) =>
      fileDiff(path, oldContent, newContent)),
    computeMultiFileDiff: vi.fn(async (files: DiffFileInput[]) =>
      multiDiff(files.map((file) => fileDiff(file.path, file.oldContent, file.newContent)))),
    addAIComment: vi.fn(async (
      _diff: MultiFileDiff,
      _fileIdx: number,
      _hunkIdx: number,
      _comment: AIComment,
    ) => undefined),
    addInlineComment: vi.fn(async (
      _diff: MultiFileDiff,
      _fileIdx: number,
      _hunkIdx: number,
      _lineIdx: number,
      _comment: InlineComment,
    ) => undefined),
    applyDiffTransaction: vi.fn(async (_files: FileDiff[]) => result),
    getLatestCommitReceipt: vi.fn(async () => ({
      transactionId: "receipt-test",
      workspaceRoot: "/workspace",
      appliedFiles: [],
      fileHashes: {},
      committedAt: "2026-08-07T00:00:00Z",
    })),
    applyFile: vi.fn(async (_file: FileDiff) => "legacy path must not run"),
    applyAll: vi.fn(async (_diff: MultiFileDiff) => ({ legacy: "must not run" })),
    rejectHunk: vi.fn(async (file: FileDiff, _hunkIdx: number) => file.oldContent),
    rejectFile: vi.fn(async (file: FileDiff) => file.oldContent),
    rejectAll: vi.fn(async (diff: MultiFileDiff) =>
      Object.fromEntries(diff.files.map((file) => [file.path, file.oldContent]))),
    reviewPR: vi.fn(async (_diff: MultiFileDiff, _reviews: FileReview[]) => ({
      summary: "",
      fileReviews: [],
      stats: {
        filesReviewed: 0,
        totalComments: 0,
        critical: 0,
        errors: 0,
        warnings: 0,
      },
    })),
    exportMarkdown: vi.fn(async (_diff: MultiFileDiff, _reviews: FileReview[]) => ""),
    exportUnifiedDiff: vi.fn(async (_diff: MultiFileDiff) => ""),
    exportHTML: vi.fn(async (_diff: MultiFileDiff) => ""),
    threeWayMergeFile: vi.fn(async (_base: string, ours: string, _theirs: string) => ({
      merged: ours,
      conflicts: 0,
      hasConflict: false,
    })),
  };
}

describe("AI diff transactional apply", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    diffState.diff = null;
    diffState.error = null;
    diffState.loading = false;
    editorState.openFiles = [];
    editorState.activeFilePath = null;
  });

  it("persists through the transaction and synchronizes an open model and recovery baseline", async () => {
    const path = "/workspace/a.ts";
    const file = fileDiff(path, "const value = 1;\n", "const value = 2;\n");
    const backend = backendWithTransaction({
      applied: true,
      appliedFiles: [path],
      conflicts: [],
      rollbackAttempted: false,
      rolledBack: false,
    });
    setDiffBackend(backend);
    diffState.diff = multiDiff([file]);
    editorState.openFiles = [{
      path,
      name: "a.ts",
      content: file.oldContent,
      originalContent: file.oldContent,
      language: "typescript",
      isDirty: false,
    }];

    const outcome = await applyFile(0);

    expect(backend.applyDiffTransaction).toHaveBeenCalledWith([file]);
    expect(backend.applyFile).not.toHaveBeenCalled();
    expect(outcome).toMatchObject({ status: "applied", appliedFiles: [path] });
    expect(editorState.openFiles[0]).toMatchObject({
      content: file.newContent,
      originalContent: file.newContent,
      isDirty: false,
    });
    expect(recoveryMocks.clearJournalForSaved).toHaveBeenCalledWith(path);
    expect(fileTreeRefreshMock).toHaveBeenCalledWith([path]);
  });

  it("reports a dirty-buffer conflict without calling the transaction or overwriting Monaco state", async () => {
    const path = "/workspace/a.ts";
    const file = fileDiff(path, "const value = 1;\n", "const value = 2;\n");
    const backend = backendWithTransaction({ applied: true, appliedFiles: [path] });
    setDiffBackend(backend);
    diffState.diff = multiDiff([file]);
    editorState.openFiles = [{
      path,
      name: "a.ts",
      content: file.oldContent,
      originalContent: file.oldContent,
      language: "typescript",
      isDirty: true,
    }];

    const outcome = await applyFile(0);

    expect(backend.applyDiffTransaction).not.toHaveBeenCalled();
    expect(outcome).toMatchObject({ status: "conflict", appliedFiles: [] });
    expect(outcome?.conflicts[0]).toContain(path);
    expect(editorState.openFiles[0].content).toBe(file.oldContent);
  });

  it("retains typing that occurs while the disk transaction is in flight", async () => {
    const path = "/workspace/a.ts";
    const file = fileDiff(path, "const value = 1;\n", "const value = 2;\n");
    let resolveTransaction!: (result: {
      applied: boolean;
      appliedFiles: string[];
      conflicts: string[];
    }) => void;
    const backend = backendWithTransaction({ applied: true, appliedFiles: [path] });
    backend.applyDiffTransaction.mockImplementation(
      () => new Promise((resolve) => {
        resolveTransaction = resolve;
      }),
    );
    setDiffBackend(backend);
    diffState.diff = multiDiff([file]);
    editorState.openFiles = [{
      path,
      name: "a.ts",
      content: file.oldContent,
      originalContent: file.oldContent,
      language: "typescript",
      isDirty: false,
    }];

    const applying = applyFile(0);
    // Wait until the transaction call is in flight (getBackend is async).
    await vi.waitFor(() => expect(typeof resolveTransaction).toBe("function"));
    // User types while the disk transaction is in flight.
    editorState.openFiles[0].content = "const value = 3; // user typed during apply\n";
    editorState.openFiles[0].isDirty = true;
    resolveTransaction({ applied: true, appliedFiles: [path], conflicts: [] });
    const outcome = await applying;

    expect(outcome?.status).toBe("applied");
    expect(editorState.openFiles[0].content).toBe("const value = 3; // user typed during apply\n");
    expect(editorState.openFiles[0].isDirty).toBe(true);
  });

  it("keeps every UI buffer unchanged when a multi-file write is rolled back", async () => {
    const first = fileDiff("/workspace/a.ts", "a-old", "a-new");
    const second = fileDiff("/workspace/b.ts", "b-old", "b-new");
    const backend = backendWithTransaction({
      applied: false,
      appliedFiles: [],
      failureReason: "write b.ts: disk full",
      conflicts: [],
      rollbackAttempted: true,
      rolledBack: true,
    });
    setDiffBackend(backend);
    diffState.diff = multiDiff([first, second]);
    editorState.openFiles = [first, second].map((file) => ({
      path: file.path,
      name: file.path.split("/").pop() ?? file.path,
      content: file.oldContent,
      originalContent: file.oldContent,
      language: "typescript",
      isDirty: false,
    }));

    const outcome = await applyAll();

    expect(outcome).toMatchObject({
      status: "failed",
      failureReason: "write b.ts: disk full",
      rollbackAttempted: true,
      rolledBack: true,
    });
    expect(editorState.openFiles.map((file) => file.content)).toEqual(["a-old", "b-old"]);
    expect(recoveryMocks.clearJournalForSaved).not.toHaveBeenCalled();
    expect(fileTreeRefreshMock).not.toHaveBeenCalled();
  });

  it("reports committed-ui-sync-failed when disk committed but UI sync fails (G18)", async () => {
    const path = "/workspace/a.ts";
    const file = fileDiff(path, "const value = 1;\n", "const value = 2;\n");
    const backend = backendWithTransaction({
      applied: true,
      appliedFiles: [path],
      conflicts: [],
      transactionId: "receipt-123",
      fileHashes: { [path]: "hash-new" },
    });
    setDiffBackend(backend);
    diffState.diff = multiDiff([file]);
    editorState.openFiles = [{
      path,
      name: "a.ts",
      content: file.oldContent,
      originalContent: file.oldContent,
      language: "typescript",
      isDirty: false,
    }];

    // Simulate the editor UI sync failing after the disk transaction
    // committed (captureBaseline rejects -> syncTransactionalWrite throws).
    recoveryMocks.clearJournalForSaved.mockRejectedValueOnce(new Error("journal write failed"));

    const outcome = await applyFile(0);

    expect(outcome?.status).toBe("committed-ui-sync-failed");
    expect(outcome?.transactionId).toBe("receipt-123");
    expect(outcome?.fileHashes).toEqual({ [path]: "hash-new" });
    expect(outcome?.failureReason).toContain("disk transaction committed");
    // The disk transaction ran exactly once — never a second apply.
    expect(backend.applyDiffTransaction).toHaveBeenCalledTimes(1);
  });

  it("returns a commit receipt with hashes on a clean apply (G18)", async () => {
    const path = "/workspace/a.ts";
    const file = fileDiff(path, "a-old", "a-new");
    const backend = backendWithTransaction({
      applied: true,
      appliedFiles: [path],
      conflicts: [],
      transactionId: "receipt-abc",
      fileHashes: { [path]: "hash-a-new" },
    });
    setDiffBackend(backend);
    diffState.diff = multiDiff([file]);
    editorState.openFiles = [{
      path,
      name: "a.ts",
      content: file.oldContent,
      originalContent: file.oldContent,
      language: "typescript",
      isDirty: false,
    }];

    const outcome = await applyFile(0);

    expect(outcome).toMatchObject({
      status: "applied",
      transactionId: "receipt-abc",
      fileHashes: { [path]: "hash-a-new" },
    });
  });
});
