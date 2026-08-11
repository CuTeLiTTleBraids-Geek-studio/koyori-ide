import { describe, it, expect, beforeEach, vi } from "vitest";

const {
  readFileMock,
      writeFileMock,
  writeFileIfUnchangedMock,
  organizeImportsMock,
  ensureLSPRunningMock,
  applyTextEditsMock,
  appStateMock,
  notifyErrorMock,
  notifySuccessMock,
  createSnapshotMock,
  pushOutputMock,
  closeLSPDocumentMock,
  automaticWriteAllowedMock,
  captureBaselineMock,
  clearJournalForSavedMock,
  forgetBaselineMock,
  scheduleJournalWriteMock,
} = vi.hoisted(() => ({
    readFileMock: vi.fn().mockResolvedValue("file content"),
    writeFileMock: vi.fn().mockResolvedValue(undefined),
    writeFileIfUnchangedMock: vi.fn().mockResolvedValue(undefined),
    organizeImportsMock: vi.fn().mockResolvedValue([]),
    ensureLSPRunningMock: vi.fn().mockResolvedValue(true),
    applyTextEditsMock: vi.fn((content: string) => content),
    appStateMock: {
      currentProject: "/proj",
      formatOnSave: false,
      organizeImportsOnSave: false,
      editorGroupFilePaths: {} as Record<string, string[]>,
      editorGroupActiveFiles: {} as Record<string, string | null>,
    },
    notifyErrorMock: vi.fn(),
    notifySuccessMock: vi.fn(),
    createSnapshotMock: vi.fn().mockResolvedValue({ id: "snap-1" }),
    pushOutputMock: vi.fn(),
    closeLSPDocumentMock: vi.fn().mockResolvedValue(undefined),
    automaticWriteAllowedMock: vi.fn().mockReturnValue(true),
    captureBaselineMock: vi.fn().mockResolvedValue(undefined),
    clearJournalForSavedMock: vi.fn().mockResolvedValue(undefined),
    forgetBaselineMock: vi.fn(),
    scheduleJournalWriteMock: vi.fn(),
  }));

vi.mock("@/api/services", () => ({
  fileService: {
    readFile: readFileMock,
    writeFile: writeFileMock,
    writeFileIfUnchanged: writeFileIfUnchangedMock,
  },
  // prompt-8: save/close may notify LSP (best-effort).
  lspService: {
    didSaveDocument: vi.fn().mockResolvedValue(undefined),
    closeDocument: vi.fn().mockResolvedValue(undefined),
    organizeImports: organizeImportsMock,
  },
}));

vi.mock("@/lib/lspCompletion", () => ({
  formatActiveDocument: vi.fn().mockResolvedValue(false),
  applyTextEditsToContent: applyTextEditsMock,
}));

vi.mock("@/stores/lsp", () => ({
  closeLSPDocument: closeLSPDocumentMock,
  ensureLSPRunning: ensureLSPRunningMock,
  // prompt-10 10-D: saveFilePath dynamic-imports this after write
  refreshDiagnosticsToProblems: vi.fn().mockResolvedValue(undefined),
}));

vi.mock("@/lib/notifications", () => ({
  notifyError: notifyErrorMock,
  notifyWarning: vi.fn(),
  notifySuccess: notifySuccessMock,
  notifyInfo: vi.fn(),
}));

vi.mock("@/lib/i18n", () => ({
  translate: (key: string, params?: Record<string, string>) =>
    params?.name ? `${key}:${params.name}` : key,
}));

vi.mock("@/stores/app", () => ({
  appState: appStateMock,
}));

vi.mock("@/stores/toolchain", () => ({
  runToolchainCommand: vi.fn().mockResolvedValue(undefined),
}));

vi.mock("@/stores/snapshot", () => ({
  createSnapshot: createSnapshotMock,
}));

vi.mock("@/stores/recovery", () => ({
  captureBaseline: captureBaselineMock,
  clearJournalForSaved: clearJournalForSavedMock,
  forgetBaseline: forgetBaselineMock,
  scheduleJournalWrite: scheduleJournalWriteMock,
  isAutomaticRecoveryWriteAllowed: automaticWriteAllowedMock,
}));

// M-15: mock pushOutput 以便断言 requestApplyToEditor 的 catch 块记录错误。
vi.mock("@/stores/output", () => ({
  pushOutput: pushOutputMock,
}));

import {
  editorState,
  applyDiffState,
  saveConflictState,
  openFile,
  closeFile,
  updateContent,
  markSaved,
  saveFile,
  saveFilePath,
  saveAllFiles,
  saveAllFilesDetailed,
  dismissSaveConflict,
  resolveSaveConflict,
  openFileFromPath,
  requestApplyToEditor,
  confirmApplyDiff,
  cancelApplyDiff,
  setupAutoSave,
  saveOnFocusChange,
  closeFilesUnder,
  renameOpenFilesUnder,
} from "./editor";

describe("editor store", () => {
  beforeEach(async () => {
    await Promise.resolve();
    editorState.openFiles = [];
    editorState.activeFilePath = null;
    cancelApplyDiff();
    dismissSaveConflict();
    readFileMock.mockReset();
    readFileMock.mockResolvedValue("file content");
    writeFileMock.mockReset();
    writeFileMock.mockResolvedValue(undefined);
    writeFileIfUnchangedMock.mockReset();
    writeFileIfUnchangedMock.mockResolvedValue(undefined);
    organizeImportsMock.mockReset();
    organizeImportsMock.mockResolvedValue([]);
    ensureLSPRunningMock.mockReset();
    ensureLSPRunningMock.mockResolvedValue(true);
    applyTextEditsMock.mockReset();
    applyTextEditsMock.mockImplementation((content: string) => content);
    appStateMock.formatOnSave = false;
    appStateMock.organizeImportsOnSave = false;
    appStateMock.editorGroupFilePaths = {};
    appStateMock.editorGroupActiveFiles = {};
    notifyErrorMock.mockReset();
    notifySuccessMock.mockReset();
    createSnapshotMock.mockReset();
    createSnapshotMock.mockResolvedValue({ id: "snap-1" });
    pushOutputMock.mockReset();
    closeLSPDocumentMock.mockReset();
    closeLSPDocumentMock.mockResolvedValue(undefined);
    automaticWriteAllowedMock.mockReset();
    automaticWriteAllowedMock.mockReturnValue(true);
    captureBaselineMock.mockClear();
    clearJournalForSavedMock.mockClear();
    forgetBaselineMock.mockClear();
    scheduleJournalWriteMock.mockClear();
  });

  it("openFile adds a file and sets it active", () => {
    openFile("/src/app.ts", "const x = 1;");
    expect(editorState.openFiles).toHaveLength(1);
    expect(editorState.openFiles[0].name).toBe("app.ts");
    expect(editorState.activeFilePath).toBe("/src/app.ts");
    expect(editorState.openFiles[0].isDirty).toBe(false);
  });

  it("openFile does not duplicate an already-open file", () => {
    openFile("/src/app.ts", "const x = 1;");
    openFile("/src/app.ts", "const x = 1;");
    expect(editorState.openFiles).toHaveLength(1);
  });

  it("openFile reactivates an existing tab without changing content", () => {
    openFile("/src/app.ts", "const x = 1;");
    updateContent("/src/app.ts", "const x = 2;");
    openFile("/src/app.ts", "ignored — already open");
    expect(editorState.openFiles[0].content).toBe("const x = 2;");
  });

  it("updateContent marks file dirty when content changes", () => {
    openFile("/src/app.ts", "original");
    updateContent("/src/app.ts", "changed");
    expect(editorState.openFiles[0].isDirty).toBe(true);
    expect(editorState.openFiles[0].content).toBe("changed");
  });

  it("updateContent does not mark dirty if content equals original", () => {
    openFile("/src/app.ts", "original");
    updateContent("/src/app.ts", "original");
    expect(editorState.openFiles[0].isDirty).toBe(false);
  });

  it("markSaved clears dirty flag and updates original content", () => {
    openFile("/src/app.ts", "original");
    updateContent("/src/app.ts", "new content");
    markSaved("/src/app.ts");
    expect(editorState.openFiles[0].isDirty).toBe(false);
    expect(editorState.openFiles[0].originalContent).toBe("new content");
  });

  it("closeFile removes the file from the list", () => {
    openFile("/src/a.ts", "a");
    openFile("/src/b.ts", "b");
    closeFile("/src/a.ts");
    expect(editorState.openFiles).toHaveLength(1);
    expect(editorState.openFiles[0].path).toBe("/src/b.ts");
  });

  it("closeFile of the active tab selects a neighbor", () => {
    openFile("/src/a.ts", "a");
    openFile("/src/b.ts", "b");
    closeFile("/src/b.ts");
    expect(editorState.activeFilePath).toBe("/src/a.ts");
  });

  it("closeFile of the only tab clears active path", () => {
    openFile("/src/a.ts", "a");
    closeFile("/src/a.ts");
    expect(editorState.openFiles).toHaveLength(0);
    expect(editorState.activeFilePath).toBeNull();
  });

  it("closes every open file under a deleted folder and sends LSP didClose", async () => {
    openFile("/src/folder/a.ts", "a");
    openFile("/src/folder/nested/b.go", "b");
    openFile("/src/keep.ts", "keep");

    closeFilesUnder("/src/folder");
    await vi.waitFor(() => expect(closeLSPDocumentMock).toHaveBeenCalledTimes(2));

    expect(editorState.openFiles.map((file) => file.path)).toEqual(["/src/keep.ts"]);
    expect(closeLSPDocumentMock).toHaveBeenCalledWith("typescript", "/src/folder/a.ts");
    expect(closeLSPDocumentMock).toHaveBeenCalledWith("go", "/src/folder/nested/b.go");
  });

  it("renames open paths without leaving an old path and closes old LSP URIs", async () => {
    appStateMock.editorGroupFilePaths = { main: ["/src/old/a.ts"] };
    appStateMock.editorGroupActiveFiles = { main: "/src/old/a.ts" };
    openFile("/src/old/a.ts", "a");

    renameOpenFilesUnder("/src/old", "/src/new");
    await vi.waitFor(() => expect(closeLSPDocumentMock).toHaveBeenCalledOnce());

    expect(editorState.openFiles[0].path).toBe("/src/new/a.ts");
    expect(editorState.activeFilePath).toBe("/src/new/a.ts");
    expect(appStateMock.editorGroupFilePaths.main).toEqual(["/src/new/a.ts"]);
    expect(appStateMock.editorGroupActiveFiles.main).toBe("/src/new/a.ts");
    expect(closeLSPDocumentMock).toHaveBeenCalledWith("typescript", "/src/old/a.ts");
  });

  it("openFile sets language from extension", () => {
    openFile("/src/app.ts", "");
    expect(editorState.openFiles[0].language).toBe("typescript");
    openFile("/src/main.go", "");
    expect(editorState.openFiles[1].language).toBe("go");
  });

  it("saveFile writes active file to disk and clears dirty", async () => {
    openFile("/src/app.ts", "original");
    updateContent("/src/app.ts", "modified");
    expect(editorState.openFiles[0].isDirty).toBe(true);
    await saveFile();
    expect(editorState.openFiles[0].isDirty).toBe(false);
    expect(editorState.openFiles[0].originalContent).toBe("modified");
  });

  it("saveFile binds the write to the content opened from disk", async () => {
    openFile("/src/app.ts", "original");
    updateContent("/src/app.ts", "modified");

    await saveFile();

    expect(writeFileIfUnchangedMock).toHaveBeenCalledWith(
      "/src/app.ts",
      "modified",
      "0682c5f2076f099c34cfdd15a9e063849ed437a49677e6fcc5b4198c76575be5",
    );
    expect(writeFileMock).not.toHaveBeenCalled();
  });

  it("saveFile keeps the buffer dirty when the disk baseline conflicts", async () => {
    writeFileIfUnchangedMock.mockRejectedValueOnce(
      new Error("file changed on disk since it was opened"),
    );
    openFile("/src/app.ts", "original");
    updateContent("/src/app.ts", "modified");

    await saveFile();

    expect(editorState.openFiles[0]).toMatchObject({
      content: "modified",
      originalContent: "original",
      isDirty: true,
    });
    expect(notifyErrorMock).toHaveBeenCalledWith(
      expect.stringContaining("changed on disk"),
    );
  });

  it("overwrite conflict resolution is bound to the disk version shown to the user", async () => {
    writeFileIfUnchangedMock
      .mockRejectedValueOnce(new Error("file changed on disk since it was opened"))
      .mockResolvedValueOnce(undefined);
    readFileMock.mockResolvedValueOnce("external version");
    openFile("/src/app.ts", "original");
    updateContent("/src/app.ts", "local version");

    await saveFile();

    expect(saveConflictState).toMatchObject({
      visible: true,
      path: "/src/app.ts",
      diskContent: "external version",
    });
    await expect(resolveSaveConflict("overwrite")).resolves.toBe(true);
    expect(writeFileIfUnchangedMock).toHaveBeenLastCalledWith(
      "/src/app.ts",
      "local version",
      "c7e2dd264aef064c2911adc13167fc4d0210c955273d5a1d94d87211c748eba8",
    );
    expect(editorState.openFiles[0]).toMatchObject({
      content: "local version",
      originalContent: "local version",
      isDirty: false,
    });
    expect(saveConflictState.visible).toBe(false);
  });

  it("reload conflict resolution discards only after an explicit choice", async () => {
    writeFileIfUnchangedMock.mockRejectedValueOnce(
      new Error("file changed on disk since it was opened"),
    );
    readFileMock
      .mockResolvedValueOnce("external version at conflict")
      .mockResolvedValueOnce("latest external version");
    openFile("/src/app.ts", "original");
    updateContent("/src/app.ts", "local version");

    await saveFile();
    expect(editorState.openFiles[0].content).toBe("local version");

    await expect(resolveSaveConflict("reload")).resolves.toBe(true);
    expect(editorState.openFiles[0]).toMatchObject({
      content: "latest external version",
      originalContent: "latest external version",
      isDirty: false,
    });
    expect(saveConflictState.visible).toBe(false);
  });

  it("saveAllFiles counts only successful baseline writes and preserves failures", async () => {
    writeFileIfUnchangedMock
      .mockResolvedValueOnce(undefined)
      .mockRejectedValueOnce(new Error("file changed on disk"));
    openFile("/src/a.ts", "a0");
    updateContent("/src/a.ts", "a1");
    openFile("/src/b.ts", "b0");
    updateContent("/src/b.ts", "b1");

    const saved = await saveAllFiles();

    expect(saved).toBe(1);
    expect(editorState.openFiles.find((file) => file.path === "/src/a.ts")?.isDirty).toBe(false);
    expect(editorState.openFiles.find((file) => file.path === "/src/b.ts")?.isDirty).toBe(true);
  });

  it("saveAllFilesDetailed propagates per-file failures (G13)", async () => {
    writeFileIfUnchangedMock
      .mockResolvedValueOnce(undefined)
      .mockRejectedValueOnce(new Error("write failed"));
    openFile("/src/a.ts", "a0");
    updateContent("/src/a.ts", "a1");
    openFile("/src/b.ts", "b0");
    updateContent("/src/b.ts", "b1");

    const result = await saveAllFilesDetailed();

    expect(result.savedCount).toBe(1);
    expect(result.failedPaths).toEqual(["/src/b.ts"]);
    expect(editorState.openFiles.find((file) => file.path === "/src/a.ts")?.isDirty).toBe(false);
    expect(editorState.openFiles.find((file) => file.path === "/src/b.ts")?.isDirty).toBe(true);
  });

  it("saveAllFilesDetailed reports empty failure list when everything saves (G13)", async () => {
    writeFileIfUnchangedMock.mockResolvedValue(undefined);
    openFile("/src/a.ts", "a0");
    updateContent("/src/a.ts", "a1");

    const result = await saveAllFilesDetailed();

    expect(result.savedCount).toBe(1);
    expect(result.failedPaths).toEqual([]);
  });

  it("saveFile applies organize-import edits before writing", async () => {
    appStateMock.organizeImportsOnSave = true;
    organizeImportsMock.mockResolvedValueOnce([{
      startLine: 0,
      startCol: 0,
      endLine: 0,
      endCol: 0,
      newText: "import { x } from './x';\n",
    }]);
    applyTextEditsMock.mockReturnValueOnce("organized content");
    openFile("/src/app.ts", "original");
    updateContent("/src/app.ts", "modified");

    await saveFile();

    expect(organizeImportsMock).toHaveBeenCalledWith({
      language: "typescript",
      filePath: "/src/app.ts",
      line: 0,
      column: 0,
      content: "modified",
    });
    expect(applyTextEditsMock).toHaveBeenCalledWith(
      "modified",
      expect.any(Array),
    );
    expect(writeFileIfUnchangedMock).toHaveBeenCalledWith(
      "/src/app.ts",
      "organized content",
      expect.any(String),
    );
    expect(editorState.openFiles[0].originalContent).toBe("organized content");
    expect(editorState.openFiles[0].isDirty).toBe(false);
  });

  it("saveFile silently continues when organize imports fails", async () => {
    appStateMock.organizeImportsOnSave = true;
    organizeImportsMock.mockRejectedValueOnce(new Error("LSP unavailable"));
    openFile("/src/app.ts", "original");
    updateContent("/src/app.ts", "modified");

    await expect(saveFile()).resolves.toBeUndefined();

    expect(writeFileIfUnchangedMock).toHaveBeenCalledWith(
      "/src/app.ts",
      "modified",
      expect.any(String),
    );
    expect(editorState.openFiles[0].isDirty).toBe(false);
    expect(notifyErrorMock).not.toHaveBeenCalled();
    expect(pushOutputMock).not.toHaveBeenCalled();
  });

  it("丢弃 Organize Imports 等待期间基于旧内容返回的 edits", async () => {
    appStateMock.organizeImportsOnSave = true;
    let resolveOrganize!: (value: Array<{
      startLine: number;
      startCol: number;
      endLine: number;
      endCol: number;
      newText: string;
    }>) => void;
    organizeImportsMock.mockReturnValueOnce(
      new Promise((resolve) => {
        resolveOrganize = resolve;
      }),
    );
    openFile("/src/app.ts", "const initial = 1;");
    updateContent("/src/app.ts", "const beforeRpc = 1;");

    const saving = saveFilePath("/src/app.ts");
    await vi.waitFor(() => expect(organizeImportsMock).toHaveBeenCalledOnce());
    updateContent("/src/app.ts", "const typedWhileWaiting = 2;");
    resolveOrganize([
      {
        startLine: 0,
        startCol: 0,
        endLine: 0,
        endCol: 5,
        newText: "stale",
      },
    ]);

    await expect(saving).resolves.toBe(true);
    expect(applyTextEditsMock).not.toHaveBeenCalled();
    expect(writeFileIfUnchangedMock).toHaveBeenCalledWith(
      "/src/app.ts",
      "const typedWhileWaiting = 2;",
      expect.any(String),
    );
    expect(editorState.openFiles[0].content).toBe(
      "const typedWhileWaiting = 2;",
    );
  });

  it("写盘期间继续输入时保持 dirty，不把未写入内容标记为已保存", async () => {
    let resolveWrite!: () => void;
    writeFileIfUnchangedMock.mockReturnValueOnce(
      new Promise<void>((resolve) => {
        resolveWrite = resolve;
      }),
    );
    openFile("/src/app.ts", "const initial = 1;");
    updateContent("/src/app.ts", "const diskSnapshot = 2;");

    const saving = saveFilePath("/src/app.ts");
    await vi.waitFor(() => expect(writeFileIfUnchangedMock).toHaveBeenCalledOnce());
    updateContent("/src/app.ts", "const newerBuffer = 3;");
    resolveWrite();

    await expect(saving).resolves.toBe(false);
    expect(editorState.openFiles[0]).toMatchObject({
      content: "const newerBuffer = 3;",
      originalContent: "const diskSnapshot = 2;",
      isDirty: true,
    });

    await saveFilePath("/src/app.ts");
    expect(writeFileIfUnchangedMock).toHaveBeenLastCalledWith(
      "/src/app.ts",
      "const newerBuffer = 3;",
      "e4a4632c31556fc894eb035916a04e1c06cd95ff5d44c3c028f62a3eefc48c7f",
    );
  });

  it("saveFile skips organize imports for unsupported languages", async () => {
    appStateMock.organizeImportsOnSave = true;
    openFile("/src/notes.txt", "original");
    updateContent("/src/notes.txt", "modified");
    await saveFile();
    expect(organizeImportsMock).not.toHaveBeenCalled();
    expect(writeFileIfUnchangedMock).toHaveBeenCalledWith(
      "/src/notes.txt",
      "modified",
      expect.any(String),
    );
  });

  it("Python 保存时先启动 LSP，再执行 Organize Imports", async () => {
    appStateMock.organizeImportsOnSave = true;
    openFile("/src/app.py", "import os\n");
    updateContent("/src/app.py", "import os\nimport sys\n");

    await saveFile();

    expect(ensureLSPRunningMock).toHaveBeenCalledWith("python");
    expect(organizeImportsMock).toHaveBeenCalledWith(
      expect.objectContaining({
        language: "python",
        filePath: "/src/app.py",
      }),
    );
  });

  it("saveFile does nothing when no active file", async () => {
    await expect(saveFile()).resolves.toBeUndefined();
  });

  // prompt-5 Task A / BUG-H2
  it("updateContent returns false when file is not open", () => {
    expect(updateContent("/missing.ts", "x")).toBe(false);
  });

  it("updateContent returns true when file is open", () => {
    openFile("/src/app.ts", "original");
    expect(updateContent("/src/app.ts", "changed")).toBe(true);
    expect(editorState.openFiles[0].content).toBe("changed");
  });

  it("openFileFromPath opens file on success", async () => {
    readFileMock.mockResolvedValueOnce("from disk");
    await openFileFromPath("/src/from-disk.ts");
    expect(editorState.openFiles).toHaveLength(1);
    expect(editorState.openFiles[0].content).toBe("from disk");
    expect(editorState.activeFilePath).toBe("/src/from-disk.ts");
  });

  it("openFileFromPath rethrows and notifies on failure", async () => {
    readFileMock.mockRejectedValueOnce(new Error("ENOENT"));
    await expect(openFileFromPath("/missing.ts")).rejects.toThrow("ENOENT");
    expect(notifyErrorMock).toHaveBeenCalled();
    expect(editorState.openFiles).toHaveLength(0);
  });

  it("requestApplyToEditor fails without path", async () => {
    expect(await requestApplyToEditor("", "code")).toBe(false);
    expect(applyDiffState.visible).toBe(false);
    expect(notifyErrorMock).toHaveBeenCalled();
  });

  it("requestApplyToEditor opens file and shows diff on success", async () => {
    readFileMock.mockResolvedValueOnce("original body");
    const ok = await requestApplyToEditor("/src/app.ts", "new body");
    expect(ok).toBe(true);
    expect(applyDiffState.visible).toBe(true);
    expect(applyDiffState.path).toBe("/src/app.ts");
    expect(applyDiffState.original).toBe("original body");
    expect(applyDiffState.modified).toBe("new body");
    // Content not written until confirm
    expect(editorState.openFiles[0].content).toBe("original body");
  });

  it("requestApplyToEditor fails when open fails", async () => {
    readFileMock.mockRejectedValueOnce(new Error("permission denied"));
    const ok = await requestApplyToEditor("/locked.ts", "x");
    expect(ok).toBe(false);
    expect(applyDiffState.visible).toBe(false);
  });

  it("confirmApplyDiff writes content and reports success", async () => {
    openFile("/src/app.ts", "old");
    applyDiffState.visible = true;
    applyDiffState.path = "/src/app.ts";
    applyDiffState.original = "old";
    applyDiffState.modified = "new";
    applyDiffState.language = "typescript";
    const ok = await confirmApplyDiff();
    expect(ok).toBe(true);
    expect(editorState.openFiles[0].content).toBe("new");
    expect(editorState.openFiles[0].isDirty).toBe(true);
    expect(applyDiffState.visible).toBe(false);
    expect(notifySuccessMock).toHaveBeenCalled();
    expect(createSnapshotMock).toHaveBeenCalledWith("pre-apply");
  });

  // M-14: setupAutoSave 返回 disposer，调用 disposer 清除定时器并停止 watch。
  // 使用 vi.useFakeTimers 控制 setTimeout，验证：
  // 1) 未调用 disposer 时，定时器到期会触发 saveFile（writeFile 被调用）。
  // 2) 调用 disposer 后，定时器被清除，saveFile 不再被触发。
  it("setupAutoSave_returns_disposer (M-14)", async () => {
    vi.useFakeTimers();
    try {
      openFile("/src/app.ts", "original");
      const stop = setupAutoSave(() => true, () => "1000");
      // 修改内容触发 watch -> 调度 setTimeout(saveFile, 1000)
      updateContent("/src/app.ts", "modified");
      // 刷新微任务让 watch 回调执行（调度 setTimeout）
      await vi.advanceTimersByTimeAsync(0);
      // 推进到到期前一刻 —— saveFile 尚未触发
      await vi.advanceTimersByTimeAsync(999);
      expect(writeFileIfUnchangedMock).not.toHaveBeenCalled();
      // 调用 disposer 清除定时器并停止 watch
      stop();
      // 即使推进到原定到期时间，定时器已被清除，saveFile 不会触发
      await vi.advanceTimersByTimeAsync(100);
      expect(writeFileIfUnchangedMock).not.toHaveBeenCalled();
    } finally {
      vi.useRealTimers();
    }
  });

  // M-14 扩展：未调用 disposer 时，定时器到期正常触发 saveFile。
  it("setupAutoSave fires saveFile when timer expires without disposer (M-14)", async () => {
    vi.useFakeTimers();
    try {
      openFile("/src/app.ts", "original");
      const stop = setupAutoSave(() => true, () => "1000");
      updateContent("/src/app.ts", "modified");
      // 刷新微任务让 watch 回调执行
      await vi.advanceTimersByTimeAsync(0);
       // 到期触发 saveFile -> saveFilePath -> baseline-aware write
       await vi.advanceTimersByTimeAsync(1000);
       await vi.waitFor(() => expect(writeFileIfUnchangedMock).toHaveBeenCalled());
      stop();
    } finally {
      vi.useRealTimers();
    }
  });

  it("does not write through autosave while startup recovery is unresolved", async () => {
    vi.useFakeTimers();
    try {
      automaticWriteAllowedMock.mockReturnValue(false);
      openFile("/src/app.ts", "original");
      const stop = setupAutoSave(() => true, () => "1000");
      updateContent("/src/app.ts", "modified while recovery is pending");

      await vi.advanceTimersByTimeAsync(1000);

      expect(writeFileIfUnchangedMock).not.toHaveBeenCalled();
      expect(editorState.openFiles[0]).toMatchObject({
        content: "modified while recovery is pending",
        originalContent: "original",
        isDirty: true,
      });
      stop();
    } finally {
      vi.useRealTimers();
    }
  });

  it("does not write on window blur while startup recovery is unresolved", async () => {
    automaticWriteAllowedMock.mockReturnValue(false);
    openFile("/src/app.ts", "original");
    updateContent("/src/app.ts", "modified while recovery is pending");

    saveOnFocusChange(() => true);
    await Promise.resolve();

    expect(writeFileIfUnchangedMock).not.toHaveBeenCalled();
    expect(editorState.openFiles[0]?.isDirty).toBe(true);
  });

  // M-15: requestApplyToEditor 的 catch 块不再静默吞错，
  // 而是通过 pushOutput("editor", "error", ...) 记录到 Output 面板。
  it("requestApplyToEditor logs error via pushOutput on failure (M-15)", async () => {
    readFileMock.mockRejectedValueOnce(new Error("permission denied"));
    const ok = await requestApplyToEditor("/locked.ts", "new body");
    expect(ok).toBe(false);
    // pushOutput 应以 source="editor"、severity="error" 被调用
    expect(pushOutputMock).toHaveBeenCalledWith(
      "editor",
      "error",
      expect.stringContaining("Apply to editor failed"),
    );
  });
});
