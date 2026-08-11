// Koyori IDE 模块 · Workspace Probe。
// 喵，这是 Koyori IDE 的 Workspace Probe 模块（前端实现）~
import { Events } from "@wailsio/runtime";
import * as AIServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/aiservice.js";
import * as ProjectServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/projectservice.js";
import * as SearchServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/searchservice.js";
import * as TerminalServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/terminalservice.js";
import { Duration } from "../../bindings/time/models.js";

const resultEvent = "e2e:g05-workspace-result";

interface WorkspaceProbeConfig {
  runId: string;
  role: "main" | "ai";
  workspace: string;
  marker: string;
  presetName: string;
}

interface WorkspaceProbeResult {
  runId: string;
  role: string;
  ok: boolean;
  transport: string;
  snapshot?: unknown;
  searchMatches?: number;
  aiPresetContainsMarker?: boolean;
  terminalOutput?: string;
  error?: string;
}

function message(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function requireCondition(condition: unknown, detail: string): asserts condition {
  if (!condition) throw new Error(detail);
}

function normalizedPath(value: string): string {
  return value.replaceAll("\\", "/").replace(/\/$/, "").toLowerCase();
}

async function runProbe(config: WorkspaceProbeConfig): Promise<WorkspaceProbeResult> {
  const snapshot = await ProjectServiceBindings.GetWorkspaceSnapshot();
  requireCondition(
    normalizedPath(snapshot.root) === normalizedPath(config.workspace),
    `workspace snapshot root ${snapshot.root} did not match ${config.workspace}`,
  );
  requireCondition(
    snapshot.roots?.some((root) => normalizedPath(root) === normalizedPath(config.workspace)),
    "workspace snapshot roots did not contain the active root",
  );

  const searchResults = await SearchServiceBindings.Search("", config.marker, false) ?? [];
  const searchMatches = searchResults.reduce(
    (count, result) => count + (result.matches?.length ?? 0),
    0,
  );
  requireCondition(searchMatches > 0, `SearchService found no ${config.marker} matches`);

  const preset = await AIServiceBindings.GetPresetPrompt(config.presetName);
  requireCondition(preset.includes(config.marker), "AI preset did not resolve from the switched workspace");

  let terminalOutput = "";
  if (config.role === "main") {
    await TerminalServiceBindings.Start("");
    try {
      await TerminalServiceBindings.Write(`echo ${config.marker}\r\n`);
      for (let attempt = 0; attempt < 20; attempt += 1) {
        terminalOutput += await TerminalServiceBindings.ReadOutput(Duration.Millisecond * 250);
        if (terminalOutput.includes(config.marker)) break;
      }
    } finally {
      await TerminalServiceBindings.Kill();
    }
    requireCondition(terminalOutput.includes(config.marker), "TerminalService did not run in the switched workspace");
  }

  return {
    runId: config.runId,
    role: config.role,
    ok: true,
    transport: "packaged WebView -> generated Wails bindings -> shared WorkspaceContext",
    snapshot,
    searchMatches,
    aiPresetContainsMarker: true,
    terminalOutput: config.role === "main" ? terminalOutput : undefined,
  };
}

export function installWorkspaceProbe(): void {
  const target = globalThis as typeof globalThis & {
    __koyoriIdeRunG05WorkspaceProbe?: (config: WorkspaceProbeConfig) => Promise<void>;
  };
  target.__koyoriIdeRunG05WorkspaceProbe = async (config) => {
    let result: WorkspaceProbeResult;
    try {
      result = await runProbe(config);
    } catch (error: unknown) {
      result = {
        runId: config.runId,
        role: config.role,
        ok: false,
        transport: "packaged WebView -> generated Wails bindings -> shared WorkspaceContext",
        error: message(error),
      };
    }
    await Events.Emit(resultEvent, result);
  };
}
