// 架构改进 A (prompt-1.md): Store 命名约定。
// 本仓库导出的 store 对象统一使用 `xxxState` 形式（此处为 editorState /
// applyDiffState）。模块级 `let` 共享可变状态已消除——历史上 setupAutoSave
// 的 autoSaveTimer 已改为函数内局部变量（见 M-14 注释），由调用者各自管理。
// Koyori IDE 模块 · Editor；交互服务：文件系统（FileService）、离线 LSP（LSPService）、插件市场（MarketplaceService）。
// 喵，这是 Koyori IDE 的 Editor 模块（前端实现）~
import { reactive, computed, watch } from "vue";
import { detectLanguage } from "@/lib/language";
import { fileService, lspService } from "@/api/services";
import { notifyError, notifyWarning, notifySuccess } from "@/lib/notifications";
import { errorMessage } from "@/lib/errors";
import { translate } from "@/lib/i18n";
import { appState } from "@/stores/app";
import { pushOutput } from "@/stores/output";
// GOAL-P0-03: dirty buffers are journaled to disk so an abnormal exit does not
// discard unsaved work. Imported statically because every edit must be
// journaled — a lazy import could drop the first keystrokes of a session.
import {
  captureBaseline,
  clearJournalForSaved,
  forgetBaseline,
  isAutomaticRecoveryWriteAllowed,
  scheduleJournalWrite,
} from "@/stores/recovery";

export interface OpenFile {
  path: string;
  name: string;
  content: string;
  originalContent: string;
  language: string;
  isDirty: boolean;
}

interface EditorState {
  openFiles: OpenFile[];
  activeFilePath: string | null;
}

/**
 * prompt-5 Task A / BUG-H2: global Diff-preview state for apply-to-editor.
 * Used by the main window when the AI companion window (or side chat) asks
 * to write code into an editor buffer. Success only happens after the user
 * confirms in the Diff modal.
 */
export interface ApplyDiffState {
  visible: boolean;
  path: string;
  original: string;
  modified: string;
  language: string;
}

export interface SaveConflictState {
  visible: boolean;
  path: string;
  message: string;
  diskContent: string | null;
  diskHash: string;
  resolving: boolean;
}

export const editorState = reactive<EditorState>({
  openFiles: [],
  activeFilePath: null,
});

export const applyDiffState = reactive<ApplyDiffState>({
  visible: false,
  path: "",
  original: "",
  modified: "",
  language: "",
});

export const saveConflictState = reactive<SaveConflictState>({
  visible: false,
  path: "",
  message: "",
  diskContent: null,
  diskHash: "",
  resolving: false,
});

export const activeFile = computed<OpenFile | null>(() =>
  editorState.openFiles.find((f) => f.path === editorState.activeFilePath) ?? null
);

let lspStorePromise: Promise<typeof import("@/stores/lsp")> | null = null;

function pathIsUnder(path: string, parent: string): boolean {
  if (path === parent) return true;
  const prefix = parent.endsWith("/") || parent.endsWith("\\") ? parent : `${parent}/`;
  const alternatePrefix = parent.endsWith("/") || parent.endsWith("\\") ? parent : `${parent}\\`;
  return path.startsWith(prefix) || path.startsWith(alternatePrefix);
}

function notifyLSPDidClose(file: OpenFile): void {
  const filePath = file.path;
  const lspLang = lspLangForFile(filePath, file.language);
  if (!lspLang) return;
  lspStorePromise ??= import("@/stores/lsp");
  void lspStorePromise.then(({ closeLSPDocument }) => {
    void closeLSPDocument(lspLang, filePath);
  });
}

function replaceEditorGroupPath(oldPath: string, newPath: string | null): void {
  for (const groupId of Object.keys(appState.editorGroupFilePaths)) {
    const paths = appState.editorGroupFilePaths[groupId];
    const index = paths.indexOf(oldPath);
    if (index >= 0) {
      if (newPath) paths.splice(index, 1, newPath);
      else paths.splice(index, 1);
    }
    if (appState.editorGroupActiveFiles[groupId] === oldPath) {
      appState.editorGroupActiveFiles[groupId] = newPath ?? paths[Math.min(index, paths.length - 1)] ?? null;
    }
  }
}

export function openFile(path: string, content: string): void {
  const existing = editorState.openFiles.find((f) => f.path === path);
  if (existing) {
    editorState.activeFilePath = path;
    return;
  }
  const name = path.split(/[/\\]/).pop() ?? path;
  const language = detectLanguage(path);
  editorState.openFiles.push({
    path,
    name,
    content,
    originalContent: content,
    language,
    isDirty: false,
  });
  editorState.activeFilePath = path;
  // GOAL-P0-03: record the on-disk baseline (mtime + hash) at open time. The
  // journal needs it to tell "disk unchanged, safe to restore" apart from
  // "disk changed while we were away, must show a conflict".
  void captureBaseline(path);
  // F-3 (prompt-2.md): 文件首次打开时触发 onLanguage:<language> 扩展激活。
  // 仅在文件真正新打开（非 existing 分支）时触发，避免重复激活。
  // 动态 import 避免与 vscodeExtensionActivation → marketplaceService 的
  // 静态依赖链形成循环。激活失败不阻断文件打开。
  if (language) {
    void import("@/lib/vscodeExtensionActivation").then(
      ({ activateOnLanguage }) => activateOnLanguage(language),
    ).catch((err) => console.warn("[F-3] activateOnLanguage failed:", err));
  }
}

export function closeFile(path: string): void {
  const idx = editorState.openFiles.findIndex((f) => f.path === path);
  if (idx === -1) return;
  const closing = editorState.openFiles[idx];
  if (closing) notifyLSPDidClose(closing);
  // GOAL-P0-03: drop the in-memory baseline. The journal record is left alone
  // on purpose — closing a tab with unsaved content must not silently destroy
  // the only recoverable copy of that work.
  forgetBaseline(path);
  editorState.openFiles.splice(idx, 1);
  replaceEditorGroupPath(path, null);
  if (editorState.activeFilePath === path) {
    const next = editorState.openFiles[idx] ?? editorState.openFiles[idx - 1] ?? null;
    editorState.activeFilePath = next?.path ?? null;
  }
}

export function closeFilesUnder(path: string): void {
  for (const openPath of editorState.openFiles
    .map((file) => file.path)
    .filter((filePath) => pathIsUnder(filePath, path))) {
    closeFile(openPath);
  }
}

export function renameOpenFilesUnder(oldPath: string, newPath: string): void {
  const renamed = editorState.openFiles.filter((file) => pathIsUnder(file.path, oldPath));
  for (const file of renamed) {
    const previousPath = file.path;
    const nextPath = newPath + previousPath.slice(oldPath.length);
    notifyLSPDidClose(file);
    file.path = nextPath;
    file.name = nextPath.split(/[/\\]/).pop() ?? nextPath;
    file.language = detectLanguage(nextPath);
    replaceEditorGroupPath(previousPath, nextPath);
  }
  if (editorState.activeFilePath && pathIsUnder(editorState.activeFilePath, oldPath)) {
    editorState.activeFilePath = newPath + editorState.activeFilePath.slice(oldPath.length);
  }
  if (applyDiffState.path && pathIsUnder(applyDiffState.path, oldPath)) {
    applyDiffState.path = newPath + applyDiffState.path.slice(oldPath.length);
  }
}

/**
 * Updates an already-open file's buffer. Returns false when the file is not
 * open (prompt-5 Task A / BUG-H2: callers must not report success on no-op).
 */
export function updateContent(path: string, content: string): boolean {
  const file = editorState.openFiles.find((f) => f.path === path);
  if (!file) return false;
  file.content = content;
  file.isDirty = content !== file.originalContent;
  // GOAL-P0-03: journal the dirty buffer so an abnormal exit does not discard
  // it. Debounced inside the store: typing must not cause one disk write per
  // keystroke. An edit that returns the buffer to its on-disk content clears the
  // record instead of journaling content that no longer needs recovering.
  if (file.isDirty) {
    scheduleJournalWrite(path, content);
  } else {
    void clearJournalForSaved(path);
  }
  return true;
}

export function markSaved(path: string): void {
  const file = editorState.openFiles.find((f) => f.path === path);
  if (file) {
    file.originalContent = file.content;
    file.isDirty = false;
    // GOAL-P0-03: content reached disk, so the recovery record is obsolete.
    // Re-capture the baseline first: the file we just wrote is the new
    // reference point for future conflict detection.
    void clearJournalForSaved(path);
  }
}

/**
 * Synchronizes an open editor after another trusted path committed content to
 * disk. If the user typed while that transaction was running, their buffer is
 * retained as dirty against the new disk baseline instead of being replaced.
 */
export async function syncTransactionalWrite(
  path: string,
  writtenContent: string,
  contentAtStart: string,
): Promise<boolean> {
  const file = editorState.openFiles.find((candidate) => candidate.path === path);
  if (!file) return false;

  const changedDuringWrite = file.isDirty || file.content !== contentAtStart;
  file.originalContent = writtenContent;
  if (!changedDuringWrite) file.content = writtenContent;
  file.isDirty = file.content !== writtenContent;

  if (file.isDirty) {
    await captureBaseline(path);
    scheduleJournalWrite(path, file.content);
  } else {
    await clearJournalForSaved(path);
  }
  return true;
}

export function getDirtyFiles(): OpenFile[] {
  return editorState.openFiles.filter((f) => f.isDirty);
}

function lspLangForFile(path: string, language: string): string {
  const lang = language || "";
  if (lang === "vue" || path.endsWith(".vue")) return "vue";
  if (lang === "go" || path.endsWith(".go")) return "go";
  if (lang.includes("typescript") || path.endsWith(".ts") || path.endsWith(".tsx")) return "typescript";
  if (lang.includes("javascript") || path.endsWith(".js") || path.endsWith(".jsx")) return "javascript";
  if (lang === "python" || path.endsWith(".py") || path.endsWith(".pyw")) return "python";
  if (lang === "rust" || path.endsWith(".rs")) return "rust";
  return "";
}

const saveQueues = new Map<string, Promise<boolean>>();

async function hashContent(content: string): Promise<string> {
  const digest = await crypto.subtle.digest(
    "SHA-256",
    new TextEncoder().encode(content),
  );
  return Array.from(new Uint8Array(digest))
    .map((byte) => byte.toString(16).padStart(2, "0"))
    .join("");
}

function isFileConflict(message: string): boolean {
  return message.toLowerCase().includes("file changed on disk");
}

function resetSaveConflict(): void {
  saveConflictState.visible = false;
  saveConflictState.path = "";
  saveConflictState.message = "";
  saveConflictState.diskContent = null;
  saveConflictState.diskHash = "";
  saveConflictState.resolving = false;
}

async function presentSaveConflict(path: string, message: string): Promise<void> {
  let diskContent: string | null = null;
  let diskHash = "";
  try {
    diskContent = await fileService.readFile(path);
    diskHash = await hashContent(diskContent);
  } catch (err) {
    message = `${message}; unable to read the current disk version: ${errorMessage(err)}`;
  }
  Object.assign(saveConflictState, {
    visible: true,
    path,
    message,
    diskContent,
    diskHash,
    resolving: false,
  });
}

export function dismissSaveConflict(): void {
  if (!saveConflictState.resolving) resetSaveConflict();
}

export async function resolveSaveConflict(
  action: "overwrite" | "reload",
): Promise<boolean> {
  if (!saveConflictState.visible || !saveConflictState.path) return false;
  const path = saveConflictState.path;
  const file = editorState.openFiles.find((candidate) => candidate.path === path);
  if (!file) {
    notifyError(translate("editor.conflictFileClosed"));
    resetSaveConflict();
    return false;
  }

  saveConflictState.resolving = true;
  try {
    if (action === "reload") {
      const diskContent = await fileService.readFile(path);
      file.content = diskContent;
      file.originalContent = diskContent;
      file.isDirty = false;
      await clearJournalForSaved(path);
      resetSaveConflict();
      notifySuccess(translate("editor.conflictReloaded"));
      return true;
    }

    if (!saveConflictState.diskHash) {
      throw new Error(translate("editor.conflictDiskUnavailable"));
    }
    const writtenContent = file.content;
    await fileService.writeFileIfUnchanged(
      path,
      writtenContent,
      saveConflictState.diskHash,
    );
    const current = editorState.openFiles.find((candidate) => candidate.path === path);
    if (!current) {
      resetSaveConflict();
      return false;
    }
    current.originalContent = writtenContent;
    current.isDirty = current.content !== writtenContent;
    if (current.isDirty) {
      await captureBaseline(path);
      scheduleJournalWrite(path, current.content);
    } else {
      await clearJournalForSaved(path);
    }
    resetSaveConflict();
    notifySuccess(translate("editor.conflictOverwritten"));
    return !current.isDirty;
  } catch (err) {
    const message = errorMessage(err);
    if (action === "overwrite" && isFileConflict(message)) {
      await presentSaveConflict(path, message);
    } else {
      saveConflictState.message = message;
      saveConflictState.resolving = false;
    }
    notifyError(translate("editor.conflictResolveFailed", { error: message }));
    return false;
  }
}

/**
 * Saves a single open file by path (prompt-10 10-A Save All helper).
 */
async function performSaveFilePath(
  path: string,
  opts?: { skipFormat?: boolean; intent?: "manual" | "automatic" },
): Promise<boolean> {
  if (opts?.intent === "automatic" && !isAutomaticRecoveryWriteAllowed()) {
    return false;
  }
  const file = editorState.openFiles.find((f) => f.path === path);
  if (!file) return false;
  const { appState } = await import("@/stores/app");
  let content = file.content;
  const lspLang = lspLangForFile(file.path, file.language || "");
  if (!opts?.skipFormat && appState.formatOnSave && lspLang) {
    try {
      const { formatActiveDocument } = await import("@/lib/lspCompletion");
      const ok = await formatActiveDocument(lspLang, file.path, content);
      const currentContent = editorState.openFiles.find(
        (f) => f.path === file.path,
      )?.content;
      if (ok || (currentContent !== undefined && currentContent !== content)) {
        content = currentContent ?? content;
      }
    } catch (e) {
      // prompt-10 10-B: surface format failure (still continue to write).
      notifyWarning(
        `Format on Save failed: ${e instanceof Error ? e.message : String(e)}`,
      );
    }
  }
  const organizeImportsOnSave = Boolean(
    (appState as typeof appState & { organizeImportsOnSave?: boolean })
      .organizeImportsOnSave,
  );
  if (organizeImportsOnSave && lspLang) {
    try {
      const { ensureLSPRunning } = await import("@/stores/lsp");
      if (!(await ensureLSPRunning(lspLang))) {
        throw new Error(`LSP not running for ${lspLang}`);
      }
      const requestContent = content;
      const edits = await lspService.organizeImports({
        language: lspLang,
        filePath: file.path,
        line: 0,
        column: 0,
        content: requestContent,
      });
      const currentContent = editorState.openFiles.find(
        (f) => f.path === file.path,
      )?.content;
      if (currentContent !== undefined && currentContent !== requestContent) {
        // 用户在 RPC 期间继续输入：丢弃基于旧快照的 edits。
        content = currentContent;
      } else if (Array.isArray(edits) && edits.length > 0) {
        const { applyTextEditsToContent } = await import("@/lib/lspCompletion");
        content = applyTextEditsToContent(requestContent, edits);
        updateContent(file.path, content);
      }
    } catch {
      // Organize imports is best-effort and must never block the disk write.
    }
  }
  try {
    const latestContent = editorState.openFiles.find(
      (f) => f.path === file.path,
    )?.content;
    if (latestContent !== undefined && latestContent !== content) {
      content = latestContent;
    }
    // G-EDIT-01: trim trailing whitespace on each line before saving.
    if (appState.trimTrailingWhitespace) {
      content = content.replace(/[ \t]+$/gm, "");
      updateContent(file.path, content);
    }
    // G-EDIT-03: ensure final newline at end of file.
    if (appState.insertFinalNewline && content.length > 0 && !content.endsWith("\n")) {
      content += "\n";
      updateContent(file.path, content);
    }
    const baselineHash = await hashContent(file.originalContent);
    await fileService.writeFileIfUnchanged(file.path, content, baselineHash);
    const writtenFile = editorState.openFiles.find((f) => f.path === file.path);
    if (!writtenFile) return false;
    writtenFile.originalContent = content;
    writtenFile.isDirty = writtenFile.content !== content;
    if (writtenFile.isDirty) {
      await captureBaseline(file.path);
      const latestFile = editorState.openFiles.find((f) => f.path === file.path);
      if (latestFile) {
        latestFile.isDirty = latestFile.content !== content;
        if (latestFile.isDirty) scheduleJournalWrite(file.path, latestFile.content);
        else await clearJournalForSaved(file.path);
      }
    } else {
      await clearJournalForSaved(file.path);
    }
    const savedCurrentBuffer = !writtenFile.isDirty;
    if (lspLang) {
      void import("@/api/services").then(({ lspService }) => {
        void lspService.didSaveDocument({
          language: lspLang,
          filePath: file.path,
          line: 0,
          column: 0,
          content,
        });
      });
      // prompt-10 10-D / 10-J: refresh diagnostics after save (+ eslint for JS/TS)
      void import("@/stores/lsp").then(({ refreshDiagnosticsToProblems }) => {
        void refreshDiagnosticsToProblems(lspLang, file.path, content);
      });
      if (lspLang === "typescript" || lspLang === "javascript") {
        void import("@/stores/toolchain").then(({ runToolchainCommand }) => {
          void runToolchainCommand("eslint-file", file.path);
        });
      }
    }
    return savedCurrentBuffer;
  } catch (e: unknown) {
    const msg = errorMessage(e);
    console.error("Failed to save file:", msg);
    if (isFileConflict(msg)) await presentSaveConflict(file.path, msg);
    // 10-B: keep dirty, hard error for write failure
    notifyError(`Save failed: ${msg}`);
    return false;
  }
}

export function saveFilePath(
  path: string,
  opts?: { skipFormat?: boolean; intent?: "manual" | "automatic" },
): Promise<boolean> {
  const previous = saveQueues.get(path);
  const task = (previous ?? Promise.resolve(true))
    .catch(() => false)
    .then(() => performSaveFilePath(path, opts));
  saveQueues.set(path, task);
  const cleanup = () => {
    if (saveQueues.get(path) === task) saveQueues.delete(path);
  };
  void task.then(cleanup, cleanup);
  return task;
}

/**
 * Saves the active file to disk and clears its dirty flag.
 * prompt-9 9-A + prompt-10 10-B: FoS with visible failures.
 */
export async function saveFile(
  opts?: { intent?: "manual" | "automatic" },
): Promise<void> {
  const file = activeFile.value;
  if (!file) return;
  await saveFilePath(file.path, opts);
}

/**
 * prompt-10 10-A: save every dirty buffer to disk.
 * Returns number of successfully saved files.
 */
export async function saveAllFiles(): Promise<number> {
  const result = await saveAllFilesDetailed();
  return result.savedCount;
}

/**
 * G13: save every dirty buffer and propagate per-file failures. The extension
 * API (workspace.saveAll) must not report success when any file failed, so
 * this returns the exact failure set for fail-closed callers.
 */
export interface SaveAllResult {
  savedCount: number;
  failedPaths: string[];
}

export async function saveAllFilesDetailed(): Promise<SaveAllResult> {
  const dirty = getDirtyFiles();
  let savedCount = 0;
  const failedPaths: string[] = [];
  for (const f of dirty) {
    if (await saveFilePath(f.path)) {
      savedCount += 1;
    } else {
      failedPaths.push(f.path);
    }
  }
  return { savedCount, failedPaths };
}

/**
 * Opens a file by absolute/workspace path. On failure notifies the user and
 * rethrows so callers (apply-to-editor) can avoid false-success (prompt-5 Task A).
 */
export async function openFileFromPath(path: string): Promise<void> {
  try {
    const content = await fileService.readFile(path);
    openFile(path, content);
  } catch (err) {
    const msg = err instanceof Error ? err.message : String(err);
    notifyError(`Failed to open file: ${msg}`);
    throw err instanceof Error ? err : new Error(msg);
  }
}

/**
 * Opens (if needed) the target file and shows a Diff preview modal.
 * Does NOT write content until the user confirms via confirmApplyDiff.
 * Returns false when path is missing, open fails, or the file still isn't open.
 */
export async function requestApplyToEditor(path: string, code: string): Promise<boolean> {
  if (!path?.trim()) {
    notifyError(translate("aiWindow.noActiveFile"), translate("aiWindow.applyTitle"));
    return false;
  }
  if (typeof code !== "string") {
    notifyError(translate("aiWindow.noActiveFile"), translate("aiWindow.applyTitle"));
    return false;
  }
  try {
    let file = editorState.openFiles.find((f) => f.path === path);
    if (!file) {
      await openFileFromPath(path);
      file = editorState.openFiles.find((f) => f.path === path);
    }
    if (!file) {
      notifyError(translate("aiWindow.noActiveFile"), translate("aiWindow.applyTitle"));
      return false;
    }
    applyDiffState.path = path;
    applyDiffState.original = file.content;
    applyDiffState.modified = code;
    applyDiffState.language = file.language || detectLanguage(path);
    applyDiffState.visible = true;
    return true;
  } catch (e: unknown) {
    // M-15: 不再静默吞错 —— 记录到 Output 面板（openFileFromPath 已通知用户）。
    const msg = e instanceof Error ? e.message : String(e);
    pushOutput("editor", "error", `Apply to editor failed: ${msg}`);
    return false;
  }
}

export function cancelApplyDiff(): void {
  applyDiffState.visible = false;
  applyDiffState.path = "";
  applyDiffState.original = "";
  applyDiffState.modified = "";
  applyDiffState.language = "";
}

/**
 * Confirms the pending Diff apply: optional snapshot, then updateContent.
 * Reports success only when the buffer was actually updated.
 */
export async function confirmApplyDiff(): Promise<boolean> {
  if (!applyDiffState.path || !applyDiffState.visible) return false;
  // Optional safety snapshot before overwrite (prompt-5 Task A).
  if (appState.currentProject) {
    try {
      const { createSnapshot } = await import("@/stores/snapshot");
      await createSnapshot("pre-apply");
    } catch {
      // Snapshot is best-effort; apply still proceeds.
    }
  }
  const ok = updateContent(applyDiffState.path, applyDiffState.modified);
  if (!ok) {
    notifyError(translate("aiWindow.noActiveFile"), translate("aiWindow.applyTitle"));
    return false;
  }
  const name =
    applyDiffState.path.split(/[/\\]/).pop() ?? applyDiffState.path;
  notifySuccess(translate("aiChat.appliedTo", { name }));
  cancelApplyDiff();
  return true;
}

// M-14: setupAutoSave 返回 stop handler，调用者应在卸载/re-setup 时调用
// 以清除定时器并停止 watch。autoSaveTimer 从模块级 let 改为函数内局部变量，
// 由闭包捕获，避免多实例共享定时器。
export function setupAutoSave(autoSave: () => boolean, autoSaveDelay: () => string): () => void {
  let autoSaveTimer: ReturnType<typeof setTimeout> | null = null;
  const stopWatch = watch(
    () => activeFile.value?.content,
    (newContent, oldContent) => {
      if (!autoSave() || !isAutomaticRecoveryWriteAllowed() ||
          !activeFile.value || newContent === oldContent) return;
      if (autoSaveTimer) clearTimeout(autoSaveTimer);
      const delay = parseInt(autoSaveDelay(), 10) || 1000;
      autoSaveTimer = setTimeout(() => {
        void saveFile({ intent: "automatic" });
      }, delay);
    }
  );
  return () => {
    stopWatch();
    if (autoSaveTimer) {
      clearTimeout(autoSaveTimer);
      autoSaveTimer = null;
    }
  };
}

export function saveOnFocusChange(autoSave: () => boolean) {
  if (autoSave() && activeFile.value?.isDirty) {
    void saveFile({ intent: "automatic" });
  }
}
