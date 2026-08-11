import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  getCompletionList: vi.fn(),
  lspService: {
    detectServers: vi.fn(),
    startServer: vi.fn(),
    stopServer: vi.fn(),
    getReferences: vi.fn(),
    getCompletions: vi.fn(),
    getHover: vi.fn(),
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
vi.mock("../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/lspservice.js", () => ({
  GetCompletionList: mocks.getCompletionList,
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
  getLSPCompletions,
  getLSPHover,
  getLSPReferences,
  lspState,
  lspStatusDetail,
  monacoLanguageToLSP,
  refreshDiagnosticsToProblems,
  restartLSPServer,
} from "./lsp";

const statuses = [
  {
    language: "typescript",
    available: true,
    running: true,
    serverPath: "/react/node_modules/.bin/typescript-language-server",
    version: "",
    serverKind: "typescript-language-server",
    framework: "react",
    workspaceRoot: "/react",
  },
  {
    language: "vue",
    available: true,
    running: false,
    serverPath: "/vue/node_modules/.bin/vue-language-server",
    version: "",
    serverKind: "vue-language-server",
    framework: "vue",
    workspaceRoot: "/vue",
  },
  {
    language: "angular",
    available: true,
    running: false,
    serverPath: "/angular/node_modules/.bin/ngserver",
    version: "",
    serverKind: "angular-language-server",
    framework: "angular",
    workspaceRoot: "/angular",
  },
];

beforeEach(() => {
  __resetLSPStoreForTesting();
  vi.clearAllMocks();
  mocks.lspService.detectServers.mockResolvedValue(statuses.map((status) => ({ ...status })));
  mocks.lspService.startServer.mockResolvedValue(undefined);
  mocks.lspService.stopServer.mockResolvedValue(undefined);
  mocks.lspService.getReferences.mockResolvedValue([]);
  mocks.getCompletionList.mockResolvedValue({ items: [], isIncomplete: false });
  mocks.lspService.getCompletions.mockResolvedValue([]);
  mocks.lspService.getHover.mockResolvedValue("hover");
  mocks.lspService.getDiagnostics.mockImplementation(async (request: { language: string }) => [{
    line: request.language === "angular" ? 2 : 1,
    column: 0,
    severity: 1,
    message: `${request.language} diagnostic`,
    source: request.language,
  }]);
  mocks.outputState.problems = [];
});

describe("LSP 9J framework routing", () => {
  it("uses Volar for Vue files and Angular LS only when its project status exists", async () => {
    await detectLSPServers();

    expect(monacoLanguageToLSP("html", "/vue/src/App.vue")).toBe("vue");
    // .ts 文件始终走 typescript 服务器（对标 VSCode），不路由到 angular。
    // Angular 服务器作为辅助通过 diagnosticServerLanguages 协同工作。
    expect(monacoLanguageToLSP("typescript", "/angular/src/app.component.ts")).toBe("typescript");
    expect(monacoLanguageToLSP("html", "/angular/src/app.component.html")).toBe("angular");
    expect(diagnosticServerLanguages("typescript", "/angular/src/app.component.ts")).toEqual([
      "typescript",
      "angular",
    ]);

    await getLSPReferences("typescript", "/angular/src/app.component.ts", 0, 0, "class AppComponent {} ");
    // .ts 文件路由到 typescript 服务器（已 running），不调用 startServer。
    // getReferences 用 typescript 语言查询。
    expect(mocks.lspService.getReferences).toHaveBeenCalledWith(expect.objectContaining({ language: "typescript" }));
  });

  it("fans Angular diagnostics out once and keeps the primary server when Angular is missing", async () => {
    await detectLSPServers();

    await refreshDiagnosticsToProblems(
      "typescript",
      "/angular/src/app.component.ts",
      "class AppComponent {}",
    );
    expect(mocks.lspService.getDiagnostics.mock.calls.map(([request]) => request.language)).toEqual([
      "typescript",
      "angular",
    ]);
    expect(mocks.pushProblem).toHaveBeenCalledTimes(2);

    vi.clearAllMocks();
    lspState.statuses.angular.available = false;
    expect(diagnosticServerLanguages("typescript", "/angular/src/app.component.ts")).toEqual([
      "typescript",
    ]);
    await refreshDiagnosticsToProblems(
      "typescript",
      "/angular/src/app.component.ts",
      "class AppComponent {}",
    );
    expect(mocks.lspService.getDiagnostics).toHaveBeenCalledTimes(1);
    expect(mocks.lspService.getDiagnostics).toHaveBeenCalledWith(
      expect.objectContaining({ language: "typescript" }),
    );
  });

  it("starts Volar for Vue completion, hover and diagnostics requests", async () => {
    mocks.lspService.detectServers.mockResolvedValue([statuses[1]]);
    await detectLSPServers();

    await getLSPCompletions("vue", "/vue/src/App.vue", 0, 0, "<template />");
    await getLSPHover("vue", "/vue/src/App.vue", 0, 1, "<template />");
    await refreshDiagnosticsToProblems("vue", "/vue/src/App.vue", "<template />");

    expect(mocks.lspService.startServer).toHaveBeenCalledWith("vue");
    expect(mocks.getCompletionList).toHaveBeenCalledWith(
      expect.objectContaining({ language: "vue" }),
    );
    expect(mocks.lspService.getHover).toHaveBeenCalledWith(
      expect.objectContaining({ language: "vue" }),
    );
    expect(mocks.lspService.getDiagnostics).toHaveBeenCalledWith(
      expect.objectContaining({ language: "vue" }),
    );
  });

  it("keeps React on the TypeScript server and exposes framework status detail", async () => {
    mocks.lspService.detectServers.mockResolvedValue([statuses[0]]);
    await detectLSPServers();

    expect(monacoLanguageToLSP("typescriptreact", "/react/src/App.tsx")).toBe("typescript");
    expect(Object.keys(lspState.statuses)).not.toContain("react");
    expect(lspStatusDetail.value).toContain("react");
  });

  it("restarts Volar without disturbing the independent TypeScript server", async () => {
    await detectLSPServers();
    lspState.statuses.vue.running = true;

    await expect(restartLSPServer("vue")).resolves.toBe(true);

    expect(mocks.lspService.stopServer).toHaveBeenCalledWith("vue");
    expect(mocks.lspService.startServer).toHaveBeenCalledWith("vue");
    expect(lspState.statuses.typescript.running).toBe(true);
  });
});
