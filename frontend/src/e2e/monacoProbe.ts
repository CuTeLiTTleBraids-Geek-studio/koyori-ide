// Koyori IDE 模块 · Monaco Probe。
// 喵，这是 Koyori IDE 的 Monaco Probe 模块（前端实现）~
import { Events } from "@wailsio/runtime";

const resultEvent = "e2e:g10-monaco-result";

interface MonacoProbeConfig {
  runId: string;
  workspace: string;
  filePath: string;
}

interface MonacoProbeResult {
  runId: string;
  editors: number;
  monacoEditorDom: boolean;
  editorFocused: boolean;
  languageId: string;
  ok: boolean;
  error?: string;
}

function message(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

async function waitFor(predicate: () => boolean, timeoutMs: number): Promise<boolean> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (predicate()) return true;
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  return predicate();
}

async function runProbe(config: MonacoProbeConfig): Promise<MonacoProbeResult> {
  // Open the fixture file in the renderer's editor store so the editor view
  // mounts a real Monaco instance, then navigate to the editor route.
  const { activeFile, openFile } = await import("@/stores/editor");
  const { appState } = await import("@/stores/app");
  appState.currentProject = config.workspace;
  openFile(config.filePath, "");
  if (!window.location.hash.startsWith("#/editor")) {
    window.location.hash = "#/editor";
  }

  const domReady = await waitFor(
    () => document.querySelector(".monaco-editor") !== null,
    20_000,
  );
  const inputArea = document.querySelector<HTMLTextAreaElement>(".monaco-editor textarea.inputarea");
  let editorFocused = false;
  if (inputArea) {
    inputArea.focus();
    await new Promise((resolve) => setTimeout(resolve, 250));
    editorFocused = document.activeElement === inputArea;
  }
  const ok = domReady && inputArea !== null;
  return {
    runId: config.runId,
    editors: domReady ? 1 : 0,
    monacoEditorDom: domReady,
    editorFocused,
    languageId: activeFile.value?.language ?? "",
    ok,
    error: ok ? undefined : `monaco not editable: domReady=${domReady} inputarea=${inputArea !== null}`,
  };
}

export function installMonacoProbe(): void {
  const target = globalThis as typeof globalThis & {
    __koyoriIdeRunG10MonacoProbe?: (config: MonacoProbeConfig) => Promise<void>;
  };
  target.__koyoriIdeRunG10MonacoProbe = async (config) => {
    let result: MonacoProbeResult;
    try {
      result = await runProbe(config);
    } catch (error: unknown) {
      result = {
        runId: config.runId,
        editors: 0,
        monacoEditorDom: false,
        editorFocused: false,
        languageId: "",
        ok: false,
        error: message(error),
      };
    }
    await Events.Emit(resultEvent, result);
  };
}
