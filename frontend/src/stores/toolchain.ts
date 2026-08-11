// Koyori IDE 模块 · Toolchain；交互服务：ESLint 工具链（EslintService）、工具链（ToolchainService）。
// 喵，这是 Koyori IDE 的 Toolchain 模块（前端实现）~
import { reactive, computed } from "vue";
import { toolchainService } from "@/api/services";
import { appState } from "@/stores/app";
import { pushOutput, pushProblem, clearProblems } from "@/stores/output";
import { notifyWarning, notifyError } from "@/lib/notifications";
import { translate } from "@/lib/i18n";
import type { ToolchainCommand, ToolchainResult } from "@/types";
import { parseToolOutputToProblems } from "@/lib/toolOutputProblems";

interface ToolchainState {
  /** Commands available in the open workspace (grouped by language). */
  commands: ToolchainCommand[];
  /** True while a command is executing. */
  running: boolean;
  /** ID of the currently-running command, or null. */
  runningId: string | null;
  /** Last detection result: tool name -> installed?. */
  detected: Record<string, boolean>;
}

export const toolchainState = reactive<ToolchainState>({
  commands: [],
  running: false,
  runningId: null,
  detected: {},
});

/** True when at least one toolchain command is available. */
export const hasToolchainCommands = computed(() => toolchainState.commands.length > 0);

/**
 * Refresh the list of toolchain commands available in the current workspace.
 * Called when a project is opened or the palette is about to open. Best-effort:
 * errors are logged and the command list is left unchanged.
 */
export async function loadToolchainCommands(): Promise<void> {
  try {
    toolchainState.commands = await toolchainService.listToolchainCommands();
  } catch (e) {
    console.error("Failed to list toolchain commands:", e);
  }
}

/**
 * Detect which toolchain binaries are installed. Updates toolchainState.detected
 * and returns the map. Best-effort; returns an empty map on error.
 */
export async function detectToolchains(): Promise<Record<string, boolean>> {
  try {
    toolchainState.detected = await toolchainService.detectToolchains();
  } catch (e) {
    console.error("Failed to detect toolchains:", e);
    toolchainState.detected = {};
  }
  return toolchainState.detected;
}

/**
 * Run a toolchain command by id and route its output to the Output panel and
 * parsed diagnostics to the Problems panel.
 *
 * - The bottom panel is shown and focused on the Problems tab when diagnostics
 *   are produced, otherwise the Output tab.
 * - When the tool is not installed, a warning notification with the install
 *   command is shown instead of a generic error.
 * - filePath, when provided, runs the command in the file's directory.
 */
export async function runToolchainCommand(cmdId: string, filePath?: string): Promise<ToolchainResult | null> {
  if (toolchainState.running) {
    notifyWarning(translate("toolchain.alreadyRunning"));
    return null;
  }
  toolchainState.running = true;
  toolchainState.runningId = cmdId;
  // Ensure the bottom panel is visible so the user sees the output.
  appState.terminalVisible = true;
  try {
    const result = await toolchainService.runToolchainCommand(cmdId, filePath ?? "");
    handleResult(cmdId, result);
    return result;
  } catch (e) {
    const msg = e instanceof Error ? e.message : String(e);
    notifyError(translate("toolchain.runFailed", { error: msg }));
    pushOutput(sourceLabel(cmdId), "error", msg);
    appState.bottomPanelView = "output";
    return null;
  } finally {
    toolchainState.running = false;
    toolchainState.runningId = null;
  }
}

/** prompt-12 12-C: single-flight + content-hash skip for live ESLint. */
const quietLintCache = new Map<string, string>(); // filePath -> content hash
let quietLintInflight: Promise<ToolchainResult | null> | null = null;
let quietLintQueued: { cmdId: string; filePath: string; contentHash: string } | null = null;

function simpleHash(s: string): string {
  let h = 0;
  for (let i = 0; i < s.length; i++) h = (Math.imul(31, h) + s.charCodeAt(i)) | 0;
  return String(h);
}

/**
 * prompt-11 11-D / prompt-12 12-C: quiet ESLint — no focus steal, hash skip, single-flight.
 * Avoids spawning eslint on every keystroke in large monorepos.
 */
export async function runToolchainCommandQuiet(
  cmdId: string,
  filePath?: string,
  content?: string,
): Promise<ToolchainResult | null> {
  const path = filePath ?? "";
  const hash = content != null ? simpleHash(content) : "";
  if (path && hash && quietLintCache.get(path) === hash) {
    return null; // unchanged since last successful lint
  }
  if (quietLintInflight) {
    // Coalesce: keep only latest request
    if (path) quietLintQueued = { cmdId, filePath: path, contentHash: hash };
    return quietLintInflight;
  }
  if (toolchainState.running && toolchainState.runningId !== cmdId) {
    return null;
  }

  const runOnce = async (): Promise<ToolchainResult | null> => {
    toolchainState.running = true;
    toolchainState.runningId = cmdId;
    try {
      // prompt-13 13-B: prefer long-lived EslintService (eslint_d) for eslint-file
      if (cmdId === "eslint-file" && path) {
        try {
          const { eslintService } = await import("@/api/services");
          const r = await eslintService.lintFile(path, content ?? "", hash);
          if (r.skipped) {
            return {
              success: true,
              output: "",
              errors: [],
              durationMs: 0,
              notInstalled: false,
            } as ToolchainResult;
          }
          if (path) {
            const { outputState } = await import("@/stores/output");
            outputState.problems = outputState.problems.filter(
              (p) =>
                p.source !== "eslint" &&
                p.file !== path &&
                !path.endsWith(p.file) &&
                !p.file.endsWith(path),
            );
          }
          for (const d of r.diagnostics || []) {
            const sev = d.severity === "error" ? "error" : "warning";
            pushProblem(sev, d.file || path, d.line, d.column, d.message, "eslint");
          }
          if (path && hash) quietLintCache.set(path, hash);
          return {
            success: r.success,
            output: r.output || "",
            errors: (r.diagnostics || []).map((d) => ({
              severity: d.severity,
              file: d.file,
              line: d.line,
              column: d.column,
              message: d.message,
              source: "eslint",
            })),
            durationMs: r.durationMs,
            notInstalled: false,
          } as ToolchainResult;
        } catch {
          // fall through to CLI
        }
      }
      const result = await toolchainService.runToolchainCommand(cmdId, path);
      const fromBackend = result.errors ?? [];
      const fromOutput =
        fromBackend.length === 0 && result.output
          ? parseToolOutputToProblems(result.output, sourceLabel(cmdId))
          : [];
      const diags = fromBackend.length
        ? fromBackend
        : fromOutput.map((p) => ({
            severity: p.severity,
            file: p.file,
            line: p.line,
            column: p.column,
            message: p.message,
            source: p.source,
          }));
      if (path) {
        const { outputState } = await import("@/stores/output");
        outputState.problems = outputState.problems.filter(
          (p) =>
            p.source !== "eslint" &&
            p.file !== path &&
            !path.endsWith(p.file) &&
            !p.file.endsWith(path),
        );
      }
      for (const d of diags) {
        const sev =
          d.severity === "error" || d.severity === "warning" || d.severity === "info" || d.severity === "hint"
            ? d.severity
            : "warning";
        pushProblem(
          sev as "error" | "warning" | "info" | "hint",
          d.file || path || "",
          d.line,
          d.column,
          d.message,
          d.source || "eslint",
        );
      }
      if (path && hash) quietLintCache.set(path, hash);
      return result;
    } catch {
      return null;
    } finally {
      toolchainState.running = false;
      toolchainState.runningId = null;
    }
  };

  quietLintInflight = runOnce().finally(async () => {
    quietLintInflight = null;
    const q = quietLintQueued;
    quietLintQueued = null;
    if (q && q.contentHash && quietLintCache.get(q.filePath) !== q.contentHash) {
      await runToolchainCommandQuiet(q.cmdId, q.filePath);
    }
  });
  return quietLintInflight;
}

/**
 * prompt-9 9-C / 9-H: run the test at the given 0-based line.
 */
export async function runTestAtCursor(
  language: string,
  filePath: string,
  line: number,
  content: string,
): Promise<ToolchainResult | null> {
  if (toolchainState.running) {
    notifyWarning(translate("toolchain.alreadyRunning"));
    return null;
  }
  toolchainState.running = true;
  toolchainState.runningId = "test-cursor";
  appState.terminalVisible = true;
  try {
    const result = await toolchainService.runTestAtCursor(language, filePath, line, content);
    handleResult("test-cursor", result);
    return result;
  } catch (e) {
    const msg = e instanceof Error ? e.message : String(e);
    notifyError(translate("toolchain.runFailed", { error: msg }));
    pushOutput("test", "error", msg);
    appState.bottomPanelView = "output";
    return null;
  } finally {
    toolchainState.running = false;
    toolchainState.runningId = null;
  }
}

/** Cancel the active Test Explorer test-at-cursor process. */
export async function cancelTestAtCursor(): Promise<boolean> {
  if (toolchainState.runningId !== "test-cursor") return false;
  try {
    return await toolchainService.cancelTestAtCursor();
  } catch (e) {
    notifyError(e instanceof Error ? e.message : String(e));
    return false;
  }
}

/** Runtime versions for StatusBar (prompt-9 9-I). */
export const runtimeVersions = reactive({
  goVersion: "",
  nodeVersion: "",
  goplsVersion: "",
  hasGoWork: false,
});

export async function refreshRuntimeVersions(): Promise<void> {
  try {
    const v = await toolchainService.detectRuntimeVersions();
    runtimeVersions.goVersion = v.goVersion || "";
    runtimeVersions.nodeVersion = v.nodeVersion || "";
    runtimeVersions.goplsVersion = v.goplsVersion || "";
    runtimeVersions.hasGoWork = !!v.hasGoWork;
  } catch {
    /* best-effort */
  }
}

function handleResult(cmdId: string, result: ToolchainResult): void {
  const source = sourceLabel(cmdId);
  // Push the raw output to the Output panel.
  const severity = result.success ? "info" : "error";
  if (result.output) {
    pushOutput(source, severity, result.output);
  } else if (result.success) {
    pushOutput(source, "success", translate("toolchain.completedNoOutput"));
  }

  if (result.notInstalled) {
    // Tool missing: show an install hint instead of treating as a hard error.
    const installCmd = result.installCmd ?? "";
    notifyWarning(
      translate("toolchain.notInstalled", {
        install: installCmd || translate("toolchain.installManually"),
      }),
    );
    appState.bottomPanelView = "output";
    return;
  }

  // Replace stale diagnostics with freshly-parsed ones (prompt-9 9-J).
  // Prefer structured Errors from backend; if empty, parse raw output.
  const fromBackend = result.errors ?? [];
  const fromOutput =
    fromBackend.length === 0 && !result.success && result.output
      ? parseToolOutputToProblems(result.output, source)
      : [];
  if (fromBackend.length > 0 || fromOutput.length > 0) {
    clearProblems();
    for (const d of fromBackend) {
      pushProblem(
        d.severity === "error" ? "error" : d.severity === "warning" ? "warning" : "info",
        d.file,
        d.line,
        d.column,
        d.message,
        d.source,
      );
    }
    for (const p of fromOutput) {
      pushProblem(p.severity, p.file, p.line, p.column, p.message, p.source);
    }
    appState.bottomPanelView = "problems";
  } else if (!result.success) {
    // Non-zero exit with no parseable diagnostics: show the Output tab.
    appState.bottomPanelView = "output";
  } else {
    // Clean run: focus Output so the user sees the success message.
    appState.bottomPanelView = "output";
  }
}

/** Resolve a human-readable source label for output rows. */
function sourceLabel(cmdId: string): string {
  const cmd = toolchainState.commands.find((c) => c.id === cmdId);
  return cmd ? cmd.label : cmdId;
}
