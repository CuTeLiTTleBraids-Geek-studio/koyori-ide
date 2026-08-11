// Koyori IDE 模块 · Recovery Probe；交互服务：文件系统（FileService）、恢复（RecoveryService）。
// 喵，这是 Koyori IDE 的 Recovery Probe 模块（前端实现）~
import { Events } from "@wailsio/runtime";
import * as monaco from "monaco-editor";
import { nextTick } from "vue";
import { fileService, recoveryService } from "@/api/services";
import { appState, openProject } from "@/stores/app";
import router from "@/router";
import {
  activeFile,
  editorState,
  openFileFromPath,
  saveFilePath,
} from "@/stores/editor";
import {
  finishRecovery,
  flushPending,
  getWindowID,
  recoveryState,
  resolveRecoverableFile,
  scanRecoverable,
  undoLastRecoveryDecision,
} from "@/stores/recovery";

const resultEvent = "e2e:recovery-result";

interface RecoveryProbeConfig {
  runId: string;
  mode: "prepare-crash" | "pending-check" | "resolve-save";
  path: string;
  diskContent: string;
  crashContent: string;
  pendingContent: string;
}

interface ElementEvidence {
  selector: string;
  width: number;
  height: number;
  left: number;
  top: number;
  withinViewport: boolean;
  edgeTolerancePixels: number;
  maxEdgeOverflowPixels: number;
}

function message(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function requireCondition(condition: unknown, detail: string): asserts condition {
  if (!condition) throw new Error(detail);
}

async function waitFor(
  condition: () => boolean,
  detail: string,
  timeoutMs = 15_000,
): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (condition()) return;
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  throw new Error(detail);
}

function normalizedPath(value: string): string {
  return value.replaceAll("\\", "/").toLowerCase();
}

function elementEvidence(selector: string): ElementEvidence {
  const candidates = Array.from(document.querySelectorAll<HTMLElement>(selector));
  requireCondition(candidates.length > 0, `required UI element is missing: ${selector}`);
  const viewport = { width: window.innerWidth, height: window.innerHeight };
  const edgeTolerancePixels = 1;
  const candidateDetails = candidates.map((candidate) => {
    const rect = candidate.getBoundingClientRect();
    const style = getComputedStyle(candidate);
    const maxEdgeOverflowPixels = Math.max(
      0,
      -rect.left,
      -rect.top,
      rect.right - viewport.width,
      rect.bottom - viewport.height,
    );
    return {
      rect: {
        width: Math.round(rect.width),
        height: Math.round(rect.height),
        left: Math.round(rect.left),
        top: Math.round(rect.top),
        right: Math.round(rect.right),
        bottom: Math.round(rect.bottom),
      },
      display: style.display,
      visibility: style.visibility,
      opacity: style.opacity,
      maxEdgeOverflowPixels,
      withinViewport: maxEdgeOverflowPixels <= edgeTolerancePixels,
    };
  });
  const candidateIndex = candidateDetails.findIndex(({ rect, display, visibility, opacity, withinViewport }) =>
    display !== "none" && visibility !== "hidden" && opacity !== "0" &&
    rect.width > 0 && rect.height > 0 && withinViewport);
  requireCondition(
    candidateIndex >= 0,
    `${selector} has no visible in-viewport instance: ${JSON.stringify({ viewport, candidates: candidateDetails })}`,
  );
  const element = candidates[candidateIndex];
  const selectedDetails = candidateDetails[candidateIndex];
  requireCondition(element, `required UI element is missing: ${selector}`);
  requireCondition(selectedDetails, `required UI geometry is missing: ${selector}`);
  const rect = element.getBoundingClientRect();
  return {
    selector,
    width: Math.round(rect.width),
    height: Math.round(rect.height),
    left: Math.round(rect.left),
    top: Math.round(rect.top),
    withinViewport: true,
    edgeTolerancePixels,
    maxEdgeOverflowPixels: Math.round(selectedDetails.maxEdgeOverflowPixels * 100) / 100,
  };
}

async function activeMonacoModel(path: string): Promise<monaco.editor.ITextModel> {
  await waitFor(
    () => document.querySelector(".monaco-editor") !== null &&
      monaco.editor.getModels().some((model) =>
        normalizedPath(model.uri.fsPath || model.uri.path).endsWith(normalizedPath(path))),
    `Monaco did not mount a model for ${path}`,
  );
  const model = monaco.editor.getModels().find((candidate) =>
    normalizedPath(candidate.uri.fsPath || candidate.uri.path).endsWith(normalizedPath(path)));
  requireCondition(model, `Monaco model disappeared for ${path}`);
  return model;
}

async function typeThroughMonaco(path: string, content: string) {
  const model = await activeMonacoModel(path);
  const before = model.getValue();
  requireCondition(before.length > 0, "Monaco model was blank before the input check");
  model.pushEditOperations(
    [],
    [{ range: model.getFullModelRange(), text: content }],
    () => null,
  );
  await waitFor(
    () => activeFile.value?.path === path && activeFile.value.content === content,
    "Monaco input did not reach the editor store",
  );
  const textarea = document.querySelector<HTMLTextAreaElement>(".monaco-editor textarea");
  requireCondition(textarea, "Monaco input textarea is missing");
  return {
    modelUri: model.uri.toString(),
    beforeBytes: new TextEncoder().encode(before).length,
    afterBytes: new TextEncoder().encode(model.getValue()).length,
    inputTextareaPresent: true,
    editor: elementEvidence(".monaco-editor"),
  };
}

async function openInRealEditor(path: string): Promise<void> {
  // The packaged driver creates the fixture through the backend automation
  // endpoint. Complete the same frontend project-open transaction that the
  // Welcome/Projects views perform before opening a file in Monaco.
  const workspace = path.replace(/[\\/]([^\\/]+)$/, "");
  const workspaceName = workspace.split(/[\\/]/).pop() ?? workspace;
  await openProject(workspaceName, workspace);
  await router.push("/editor");
  await openFileFromPath(path);
  await nextTick();
  await waitFor(
    () => editorState.activeFilePath === path && document.querySelector(".monaco-editor") !== null,
    `editor did not open ${path}`,
  );
}

async function prepareCrash(config: RecoveryProbeConfig) {
  const scan = await scanRecoverable();
  requireCondition(recoveryState.phase === "resolved", `initial recovery phase is ${recoveryState.phase}`);
  requireCondition(scan.files.length === 0 && scan.corrupt.length === 0, "initial recovery scan was not empty");
  await openInRealEditor(config.path);
  const monacoEvidence = await typeThroughMonaco(config.path, config.crashContent);
  await flushPending(config.path, config.crashContent);
  const disk = await fileService.readFile(config.path);
  requireCondition(disk === config.diskContent, "dirty edit unexpectedly reached disk before the crash");
  return {
    phase: recoveryState.phase,
    windowId: getWindowID(),
    diskContent: disk,
    dirtyContent: activeFile.value?.content,
    monaco: monacoEvidence,
    viewport: { width: window.innerWidth, height: window.innerHeight },
  };
}

async function pendingCheck(config: RecoveryProbeConfig) {
  const scan = await scanRecoverable();
  const original = scan.files.find((file) => file.content === config.crashContent);
  requireCondition(original, "the first crashed buffer was not recovered");
  requireCondition(recoveryState.phase === "pending", `recovery phase is ${recoveryState.phase}, want pending`);
  requireCondition(recoveryState.visible, "pending recovery dialog is not visible");

  await openInRealEditor(config.path);
  const previousAutoSave = appState.autoSave;
  const previousDelay = appState.autoSaveDelay;
  try {
    appState.autoSave = true;
    appState.autoSaveDelay = "50";
    const monacoEvidence = await typeThroughMonaco(config.path, config.pendingContent);
    await new Promise((resolve) => setTimeout(resolve, 1_200));
    const afterAutoSave = await fileService.readFile(config.path);
    requireCondition(afterAutoSave === config.diskContent, "autosave overwrote disk while recovery was pending");

    window.dispatchEvent(new Event("blur"));
    await new Promise((resolve) => setTimeout(resolve, 300));
    const afterBlur = await fileService.readFile(config.path);
    requireCondition(afterBlur === config.diskContent, "focus-loss save overwrote disk while recovery was pending");
    await flushPending(config.path, config.pendingContent);

    return {
      phase: recoveryState.phase,
      originalWindowId: original.windowId,
      currentWindowId: getWindowID(),
      pendingCount: scan.files.length,
      afterAutoSave,
      afterBlur,
      monaco: monacoEvidence,
      dialog: elementEvidence(".recovery-dialog"),
      restoreButton: elementEvidence(".recovery-dialog__restore"),
      continueButtonHiddenUntilDecisionsComplete: document.querySelector(".recovery-dialog__continue") === null,
      viewport: { width: window.innerWidth, height: window.innerHeight },
    };
  } finally {
    appState.autoSave = previousAutoSave;
    appState.autoSaveDelay = previousDelay;
  }
}

async function resolveAndSave(config: RecoveryProbeConfig) {
  const scan = await scanRecoverable();
  requireCondition(scan.files.length === 2, `second crash recovered ${scan.files.length} files, want 2`);
  const first = scan.files.find((file) => file.content === config.crashContent);
  const second = scan.files.find((file) => file.content === config.pendingContent);
  requireCondition(first, "first crash snapshot is missing after the second crash");
  requireCondition(second, "pending-session snapshot is missing after the second crash");

  await openInRealEditor(config.path);
  requireCondition(await resolveRecoverableFile(first, "restore"), "first restore decision failed");
  requireCondition(await resolveRecoverableFile(second, "restore"), "second restore decision failed");
  requireCondition(recoveryState.decisions.length === 2, "recovery decisions were not staged independently");
  requireCondition(await undoLastRecoveryDecision(), "undoing the latest recovery decision failed");
  requireCondition(recoveryState.scan.files.some((file) => file.content === config.pendingContent),
    "undo did not return the pending-session snapshot to the decision list");
  requireCondition(await resolveRecoverableFile(second, "restore"), "restoring after undo failed");
  requireCondition(await finishRecovery(), "committing recovery decisions failed");
  requireCondition(recoveryState.phase === "resolved", "recovery did not reach resolved");

  const previousFormatOnSave = appState.formatOnSave;
  appState.formatOnSave = false;
  try {
    requireCondition(await saveFilePath(config.path), "manual save after recovery failed");
  } finally {
    appState.formatOnSave = previousFormatOnSave;
  }
  const disk = await fileService.readFile(config.path);
  requireCondition(disk === config.pendingContent, "manual save did not persist the selected recovered buffer");
  const cleanScan = await scanRecoverable();
  requireCondition(cleanScan.files.length === 0 && cleanScan.corrupt.length === 0,
    "completed recovery appeared again on rescan");
  requireCondition(!recoveryState.visible, "recovery dialog remained visible after a clean rescan");
  const backendState = await recoveryService.getRecoveryState();
  requireCondition(backendState.phase === "resolved", `backend phase is ${backendState.phase}`);
  const model = await activeMonacoModel(config.path);
  return {
    recoveredCount: scan.files.length,
    undoVerified: true,
    committed: true,
    manualSave: true,
    diskContent: disk,
    rescanCount: cleanScan.files.length,
    dialogVisible: recoveryState.visible,
    backendState,
    monaco: {
      modelUri: model.uri.toString(),
      bytes: new TextEncoder().encode(model.getValue()).length,
      matchesDisk: model.getValue() === disk,
      inputTextareaPresent: document.querySelector(".monaco-editor textarea") !== null,
      editor: elementEvidence(".monaco-editor"),
    },
  };
}

async function runProbe(config: RecoveryProbeConfig) {
  switch (config.mode) {
    case "prepare-crash":
      return await prepareCrash(config);
    case "pending-check":
      return await pendingCheck(config);
    case "resolve-save":
      return await resolveAndSave(config);
    default:
      throw new Error(`unsupported recovery probe mode: ${String(config.mode)}`);
  }
}

export function installRecoveryProbe(): void {
  const target = globalThis as typeof globalThis & {
    __koyoriIdeRunRecoveryProbe?: (config: RecoveryProbeConfig) => Promise<void>;
  };
  target.__koyoriIdeRunRecoveryProbe = async (config) => {
    try {
      const result = await runProbe(config);
      await Events.Emit(resultEvent, {
        runId: config.runId,
        ok: true,
        transport: "packaged renderer -> Vue/Monaco stores -> generated Wails bindings -> RecoveryService",
        mode: config.mode,
        ...result,
      });
    } catch (error: unknown) {
      await Events.Emit(resultEvent, {
        runId: config.runId,
        ok: false,
        transport: "packaged renderer -> Vue/Monaco stores -> generated Wails bindings -> RecoveryService",
        mode: config.mode,
        error: message(error),
      });
    }
  };
}
