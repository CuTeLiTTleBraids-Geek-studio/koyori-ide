import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  lspService: {
    detectServers: vi.fn(),
    startServer: vi.fn(),
    stopServer: vi.fn(),
    getDiagnostics: vi.fn(),
  },
  outputState: { problems: [] as Array<{ file: string }> },
  pushProblem: vi.fn(),
}));

vi.mock("@/api/services", () => ({ lspService: mocks.lspService }));
vi.mock("@wailsio/runtime", () => ({
  Call: { ByName: vi.fn() },
  Events: { On: vi.fn(() => vi.fn()) },
}));
vi.mock("@/stores/app", () => ({ appState: {} }));
vi.mock("@/stores/output", () => ({
  pushOutput: vi.fn(),
  clearProblems: vi.fn(),
  pushProblem: mocks.pushProblem,
  outputState: mocks.outputState,
}));

import {
  __resetLSPStoreForTesting,
  detectLSPServers,
  diagnosticServerLanguages,
  ensureLSPRunning,
  lspState,
  lspStatusLabel,
  monacoLanguageToLSP,
  refreshDiagnosticsToProblems,
  restartLSPServer,
  setLSPEnabled,
} from "./lsp";

const builtInStatuses = [
  "go",
  "typescript",
  "javascript",
  "json",
  "css",
  "html",
  "yaml",
  "eslint",
].map((language) => ({
  language,
  available: true,
  running: false,
  serverPath: `/bin/${language}-server`,
  version: "1.0.0",
  serverKind: `${language}-server`,
}));

beforeEach(() => {
  __resetLSPStoreForTesting();
  vi.clearAllMocks();
  mocks.lspService.detectServers.mockResolvedValue(builtInStatuses.map((status) => ({ ...status })));
  mocks.lspService.startServer.mockResolvedValue(undefined);
  mocks.lspService.stopServer.mockResolvedValue(undefined);
  mocks.lspService.getDiagnostics.mockResolvedValue([]);
  mocks.outputState.problems = [];
});

describe("LSP 10F ESLint diagnostics routing", () => {
  it("routes JS and TS diagnostics to both their primary server and ESLint", async () => {
    await detectLSPServers();
    expect(diagnosticServerLanguages("javascript")).toEqual(["javascript", "eslint"]);
    expect(diagnosticServerLanguages("typescriptreact")).toEqual(["typescript", "eslint"]);
  });

  it("merges primary and ESLint diagnostics before replacing file problems", async () => {
    await detectLSPServers();
    mocks.outputState.problems = [{ file: "/workspace/other.ts" }, { file: "/workspace/app.ts" }];
    mocks.lspService.getDiagnostics.mockImplementation(async (request: { language: string }) => [
      {
        line: request.language === "eslint" ? 2 : 1,
        column: 0,
        endLine: request.language === "eslint" ? 2 : 1,
        endColumn: 1,
        severity: request.language === "eslint" ? 2 : 1,
        message: `${request.language} diagnostic`,
        source: request.language,
      },
    ]);

    await refreshDiagnosticsToProblems("typescript", "/workspace/app.ts", "const value = 1;");

    expect(mocks.lspService.getDiagnostics.mock.calls.map(([request]) => request.language)).toEqual([
      "typescript",
      "eslint",
    ]);
    expect(mocks.outputState.problems).toEqual([{ file: "/workspace/other.ts" }]);
    expect(mocks.pushProblem).toHaveBeenCalledTimes(2);
    expect(mocks.pushProblem.mock.calls.map((call) => call[5])).toEqual(["typescript", "eslint"]);
  });

  it("keeps primary diagnostics when ESLint is unavailable", async () => {
    await detectLSPServers();
    lspState.statuses.eslint.available = false;

    await refreshDiagnosticsToProblems("javascript", "/workspace/app.js", "const value = 1;");

    expect(mocks.lspService.getDiagnostics).toHaveBeenCalledTimes(1);
    expect(mocks.lspService.getDiagnostics.mock.calls[0][0].language).toBe("javascript");
  });

  it("keeps primary diagnostics when the ESLint request fails", async () => {
    await detectLSPServers();
    mocks.lspService.getDiagnostics.mockImplementation(async (request: { language: string }) => {
      if (request.language === "eslint") throw new Error("eslint crashed");
      return [{ line: 0, column: 0, severity: 1, message: "ts error", source: "typescript" }];
    });

    await refreshDiagnosticsToProblems("typescript", "/workspace/app.ts", "const value = 1;");

    expect(mocks.pushProblem).toHaveBeenCalledTimes(1);
    expect(mocks.pushProblem).toHaveBeenCalledWith(
      "error",
      "/workspace/app.ts",
      1,
      1,
      "ts error",
      "typescript",
    );
  });
});

describe("LSP 10F built-in language routing", () => {
  it.each([
    ["json", "json"],
    ["jsonc", "json"],
    ["css", "css"],
    ["scss", "css"],
    ["less", "css"],
    ["html", "html"],
    ["yaml", "yaml"],
  ])("routes Monaco %s to server %s", (language, server) => {
    expect(monacoLanguageToLSP(language)).toBe(server);
  });

  it("retains independent status for every built-in server", async () => {
    await detectLSPServers();
    expect(Object.keys(lspState.statuses)).toEqual([
      "go",
      "typescript",
      "javascript",
      "json",
      "css",
      "html",
      "yaml",
      "eslint",
    ]);
  });
});

describe("LSP 10F lifecycle controls", () => {
  it("restarts only the selected server", async () => {
    await detectLSPServers();
    lspState.statuses.json.running = true;
    lspState.statuses.css.running = true;

    await expect(restartLSPServer("json")).resolves.toBe(true);

    expect(mocks.lspService.stopServer).toHaveBeenCalledTimes(1);
    expect(mocks.lspService.stopServer).toHaveBeenCalledWith("json");
    expect(mocks.lspService.startServer).toHaveBeenCalledTimes(1);
    expect(mocks.lspService.startServer).toHaveBeenCalledWith("json");
    expect(lspState.statuses.json.running).toBe(true);
    expect(lspState.statuses.css.running).toBe(true);
  });

  it("disabling LSP stops all running servers and prevents lazy restart", async () => {
    await detectLSPServers();
    lspState.statuses.json.running = true;
    lspState.statuses.css.running = true;

    await setLSPEnabled(false);

    expect(lspState.enabled).toBe(false);
    expect(lspStatusLabel.value).toBe("LSP: off");
    expect(mocks.lspService.stopServer).toHaveBeenCalledWith("json");
    expect(mocks.lspService.stopServer).toHaveBeenCalledWith("css");
    await expect(ensureLSPRunning("json")).resolves.toBe(false);
    expect(mocks.lspService.startServer).not.toHaveBeenCalled();
  });

  it("re-enabling LSP does not eagerly start servers", async () => {
    await detectLSPServers();
    await setLSPEnabled(false);
    await setLSPEnabled(true);

    expect(lspState.enabled).toBe(true);
    expect(mocks.lspService.startServer).not.toHaveBeenCalled();
  });
});
