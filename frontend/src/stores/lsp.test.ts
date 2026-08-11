import { describe, it, expect, beforeEach, vi } from "vitest";

// 使用 vi.hoisted 提前定义 mock 对象，确保 vi.mock 工厂能安全引用。
// vi.mock 的工厂在模块加载前执行，只有 vi.hoisted 创建的变量可在工厂内使用。
const mocks = vi.hoisted(() => {
  const resolveCodeAction = vi.fn();
  return {
    getCompletionList: vi.fn(),
    getInlayHintsRaw: vi.fn(),
    resolveCodeAction,
    lspService: {
      detectServers: vi.fn(),
      startServer: vi.fn(),
      stopServer: vi.fn(),
      getCompletions: vi.fn(),
      getHover: vi.fn(),
      getDiagnostics: vi.fn(),
      getInlayHints: vi.fn(),
      getSemanticTokens: vi.fn(),
      getWorkspaceSymbols: vi.fn(),
      prepareCallHierarchy: vi.fn(),
      callHierarchyIncomingCalls: vi.fn(),
      callHierarchyOutgoingCalls: vi.fn(),
      resolveCodeAction,
    },
  };
});

// 核心 mock：拦截 @/api/services，让 store 内部调用走我们的 vi.fn。
vi.mock("@/api/services", () => ({
  lspService: mocks.lspService,
}));
vi.mock("../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/lspservice.js", () => ({
  GetCompletionList: mocks.getCompletionList,
  GetInlayHintsRaw: mocks.getInlayHintsRaw,
}));

// 避免加载真实 app store（其依赖 @wailsio/runtime、monaco 主题等副作用模块）。
// lsp.ts 仅 re-export appState，测试本身不依赖其行为，提供最小桩即可。
vi.mock("@/stores/app", () => ({
  appState: {},
}));

// 拦截 pushOutput，便于在错误处理用例中断言告警被记录。
vi.mock("@/stores/output", () => ({
  pushOutput: vi.fn(),
}));

import {
  lspState,
  anyLSPAvailable,
  anyLSPRunning,
  detectLSPServers,
  startLSPServer,
  stopLSPServer,
  ensureLSPRunning,
  monacoLanguageToLSP,
  getLSPCompletions,
  getLSPHover,
  getLSPDefinition,
  getLSPInlayHints,
  getLSPSemanticTokens,
  getWorkspaceSymbols,
  prepareCallHierarchy,
  getCallHierarchyIncoming,
  getCallHierarchyOutgoing,
  resolveLSPCodeAction,
  stopAllLSPServers,
  initLSPStore,
  __resetLSPStoreForTesting,
} from "./lsp";
import { pushOutput } from "@/stores/output";

// 真实合理的 mock 数据：gopls 与 tsserver 已安装，javascript 未安装。
const goStatus = {
  language: "go",
  available: true,
  running: false,
  serverPath: "/usr/local/bin/gopls",
  version: "0.16.2",
};
const tsStatus = {
  language: "typescript",
  available: true,
  running: false,
  serverPath: "/usr/local/lib/node_modules/typescript/lib/tsserver.js",
  version: "5.4.5",
};
const jsStatus = {
  language: "javascript",
  available: false,
  running: false,
  serverPath: "",
  version: "",
};

const sampleCompletions = [
  {
    label: "fmt.Println",
    kind: 3,
    detail: "func(a ...any) (int, error)",
    insertText: "fmt.Println(${1:args})",
  },
  {
    label: "fmt.Printf",
    kind: 3,
    detail: "func(format string, a ...any) (int, error)",
    insertText: "fmt.Printf(${1:format}, ${2:args})",
  },
];

// Priority 1: 合理的 inlay hint mock 数据。kind 1=type, 2=parameter。
const sampleInlayHints = [
  { line: 5, column: 8, label: ": string", kind: 1 },
  { line: 6, column: 3, label: "name:", kind: 2 },
];
const sampleRawInlayHints = [
  {
    position: { line: 5, character: 8 },
    label: [{ value: ": string", tooltip: "inferred type" }],
    kind: 1,
    data: { id: 1 },
  },
];

beforeEach(() => {
  // 每个用例前重置 store 状态与所有 mock 调用记录。
  __resetLSPStoreForTesting();
  vi.clearAllMocks();
  // 默认 mock 行为：检测到 go / typescript 已安装，javascript 未安装。
  // 注意：每次调用返回全新副本，避免被测代码对 statuses[x] 的写操作
  // （如 running=true）污染常量数据导致跨用例串扰。
  mocks.lspService.detectServers.mockImplementation(async () => [
    { ...goStatus },
    { ...tsStatus },
    { ...jsStatus },
  ]);
  mocks.lspService.startServer.mockResolvedValue(undefined);
  mocks.lspService.stopServer.mockResolvedValue(undefined);
  mocks.getCompletionList.mockResolvedValue({
    items: sampleCompletions,
    isIncomplete: true,
  });
  mocks.lspService.getCompletions.mockResolvedValue({
    items: sampleCompletions,
    isIncomplete: true,
  });
  mocks.lspService.getHover.mockResolvedValue("func fmt.Println(a ...any) (int, error)");
  mocks.lspService.getDiagnostics.mockResolvedValue([]);
  mocks.lspService.getSemanticTokens.mockResolvedValue([]);
  mocks.getInlayHintsRaw.mockResolvedValue(sampleRawInlayHints);
  mocks.lspService.getInlayHints.mockResolvedValue(sampleInlayHints);
  mocks.lspService.getWorkspaceSymbols.mockResolvedValue([]);
  mocks.lspService.prepareCallHierarchy.mockResolvedValue([]);
  mocks.lspService.callHierarchyIncomingCalls.mockResolvedValue([]);
  mocks.lspService.callHierarchyOutgoingCalls.mockResolvedValue([]);
});

describe("lsp store — 初始状态与 getters", () => {
  it("初始状态：statuses 为空、busy 为 false、enabled 为 true", () => {
    expect(lspState.statuses).toEqual({});
    expect(lspState.busy).toBe(false);
    expect(lspState.enabled).toBe(true);
  });

  it("无可用服务器时 anyLSPAvailable 与 anyLSPRunning 均为 false", () => {
    expect(anyLSPAvailable.value).toBe(false);
    expect(anyLSPRunning.value).toBe(false);
  });

  it("detectLSPServers 后 getters 能反映可用与运行状态", async () => {
    await detectLSPServers();
    // go / typescript 可用 -> anyLSPAvailable 为 true
    expect(anyLSPAvailable.value).toBe(true);
    // 均未运行 -> anyLSPRunning 为 false
    expect(anyLSPRunning.value).toBe(false);
  });
});

describe("lsp store — detectLSPServers", () => {
  it("调用 lspService.detectServers 并按 language 填充 statuses", async () => {
    await detectLSPServers();
    expect(mocks.lspService.detectServers).toHaveBeenCalledTimes(1);
    expect(Object.keys(lspState.statuses).sort()).toEqual(["go", "javascript", "typescript"]);
    expect(lspState.statuses["go"]).toEqual(goStatus);
    expect(lspState.statuses["typescript"].available).toBe(true);
    expect(lspState.statuses["javascript"].available).toBe(false);
  });

  it("执行期间 busy 为 true，完成后恢复为 false", async () => {
    // 在 mock 实现内部采样 busy，验证其被置位。
    let busyDuringCall: boolean | null = null;
    mocks.lspService.detectServers.mockImplementationOnce(async () => {
      busyDuringCall = lspState.busy;
      return [goStatus, tsStatus, jsStatus];
    });
    await detectLSPServers();
    expect(busyDuringCall).toBe(true);
    expect(lspState.busy).toBe(false);
  });

  it("detectServers 抛错时静默失败：记录告警、busy 复位、statuses 保持原样", async () => {
    // 源码在错误路径不清理 statuses（仅在成功路径先置空再填充），
    // 因此失败时应保留调用前的状态。
    const preExisting = { go: { ...goStatus, running: true } };
    lspState.statuses = { ...preExisting };
    mocks.lspService.detectServers.mockRejectedValueOnce(new Error("backend down"));
    await detectLSPServers();
    expect(pushOutput).toHaveBeenCalledWith(
      "ide",
      "warn",
      expect.stringContaining("LSP detect failed: backend down"),
    );
    expect(lspState.busy).toBe(false);
    expect(lspState.statuses).toEqual(preExisting);
  });
});

describe("lsp store — startLSPServer", () => {
  it("成功启动已安装但未运行的服务器：调用 service 并置 running=true，返回 true", async () => {
    await detectLSPServers();
    const ok = await startLSPServer("go");
    expect(ok).toBe(true);
    expect(mocks.lspService.startServer).toHaveBeenCalledWith("go");
    expect(lspState.statuses["go"].running).toBe(true);
    expect(lspState.busy).toBe(false);
  });

  it("服务器已运行时为 no-op：不调用 service 且直接返回 true", async () => {
    await detectLSPServers();
    lspState.statuses["go"].running = true;
    const ok = await startLSPServer("go");
    expect(ok).toBe(true);
    expect(mocks.lspService.startServer).not.toHaveBeenCalled();
  });

  it("服务器未安装时返回 false 且不调用 service", async () => {
    await detectLSPServers();
    const ok = await startLSPServer("javascript");
    expect(ok).toBe(false);
    expect(mocks.lspService.startServer).not.toHaveBeenCalled();
  });

  it("startServer 抛错时返回 false、记录告警、busy 复位", async () => {
    await detectLSPServers();
    mocks.lspService.startServer.mockRejectedValueOnce(new Error("port in use"));
    const ok = await startLSPServer("go");
    expect(ok).toBe(false);
    expect(pushOutput).toHaveBeenCalledWith(
      "ide",
      "warn",
      expect.stringContaining("LSP start go failed: port in use"),
    );
    expect(lspState.busy).toBe(false);
    expect(lspState.statuses["go"].running).toBe(false);
  });
});

describe("lsp store — stopLSPServer", () => {
  it("停止运行中的服务器：调用 service 并置 running=false", async () => {
    await detectLSPServers();
    lspState.statuses["go"].running = true;
    await stopLSPServer("go");
    expect(mocks.lspService.stopServer).toHaveBeenCalledWith("go");
    expect(lspState.statuses["go"].running).toBe(false);
  });

  it("未运行时为 no-op：不调用 service", async () => {
    await detectLSPServers();
    await stopLSPServer("go");
    expect(mocks.lspService.stopServer).not.toHaveBeenCalled();
  });

  it("stopServer 抛错时静默失败并记录告警", async () => {
    await detectLSPServers();
    lspState.statuses["go"].running = true;
    mocks.lspService.stopServer.mockRejectedValueOnce(new Error("timeout"));
    await stopLSPServer("go");
    expect(pushOutput).toHaveBeenCalledWith(
      "ide",
      "warn",
      expect.stringContaining("LSP stop go failed: timeout"),
    );
    // 出错路径不翻转 running，保持原状（仍为 true）。
    expect(lspState.statuses["go"].running).toBe(true);
  });
});

describe("lsp store — ensureLSPRunning", () => {
  it("已运行时直接返回 true 且不重复启动", async () => {
    await detectLSPServers();
    lspState.statuses["go"].running = true;
    const ok = await ensureLSPRunning("go");
    expect(ok).toBe(true);
    expect(mocks.lspService.startServer).not.toHaveBeenCalled();
  });

  it("可用但未运行时懒启动并返回 true", async () => {
    await detectLSPServers();
    const ok = await ensureLSPRunning("typescript");
    expect(ok).toBe(true);
    expect(mocks.lspService.startServer).toHaveBeenCalledWith("typescript");
    expect(lspState.statuses["typescript"].running).toBe(true);
  });

  it("并发请求同一语言时只启动一次", async () => {
    await detectLSPServers();
    let resolveStart!: () => void;
    mocks.lspService.startServer.mockReturnValueOnce(
      new Promise<void>((resolve) => {
        resolveStart = resolve;
      }),
    );

    const first = ensureLSPRunning("go");
    const second = ensureLSPRunning("go");
    expect(mocks.lspService.startServer).toHaveBeenCalledTimes(1);
    resolveStart();
    await expect(Promise.all([first, second])).resolves.toEqual([true, true]);
  });

  it("不可用时返回 false 且不尝试启动", async () => {
    await detectLSPServers();
    const ok = await ensureLSPRunning("javascript");
    expect(ok).toBe(false);
    expect(mocks.lspService.startServer).not.toHaveBeenCalled();
  });

  it("未检测到的语言（status 不存在）返回 false", async () => {
    await detectLSPServers();
    const ok = await ensureLSPRunning("rust");
    expect(ok).toBe(false);
    expect(mocks.lspService.startServer).not.toHaveBeenCalled();
  });
});

describe("lsp store — monacoLanguageToLSP", () => {
  it("映射支持的语言到 LSP key", () => {
    expect(monacoLanguageToLSP("go")).toBe("go");
    expect(monacoLanguageToLSP("typescript")).toBe("typescript");
    expect(monacoLanguageToLSP("javascript")).toBe("javascript");
    expect(monacoLanguageToLSP("python")).toBe("python");
    expect(monacoLanguageToLSP("rust")).toBe("rust");
  });

  it("不支持的语言返回 null", () => {
    expect(monacoLanguageToLSP("kotlin")).toBeNull();
    expect(monacoLanguageToLSP("")).toBeNull();
  });
});

describe("lsp store — getLSPCompletions", () => {
  it("调用 GetCompletionList 并返回补全列表（含懒启动）", async () => {
    await detectLSPServers();
    const list = await getLSPCompletions(
      "go",
      "/repo/main.go",
      10,
      5,
      "package main\n",
      2,
      ".",
    );
    expect(list).toEqual({ items: sampleCompletions, isIncomplete: true });
    expect(mocks.getCompletionList).toHaveBeenCalledTimes(1);
    const req = mocks.getCompletionList.mock.calls[0][0];
    expect(req).toMatchObject({
      language: "go",
      filePath: "/repo/main.go",
      line: 10,
      column: 5,
      content: "package main\n",
      triggerKind: 2,
      triggerCharacter: ".",
    });
  });

  it("兼容旧后端直接返回 CompletionItem 数组", async () => {
    await detectLSPServers();
    mocks.getCompletionList.mockRejectedValueOnce(
      new Error("method unavailable"),
    );
    mocks.lspService.getCompletions.mockResolvedValueOnce(sampleCompletions);
    const list = await getLSPCompletions("go", "/repo/main.go", 1, 1, "");
    expect(list).toEqual({ items: sampleCompletions, isIncomplete: false });
  });

  it("enabled 为 false 时直接返回空列表且不调用 service", async () => {
    await detectLSPServers();
    lspState.enabled = false;
    const list = await getLSPCompletions("go", "/repo/main.go", 1, 1, "");
    expect(list).toEqual({ items: [], isIncomplete: false });
    expect(mocks.getCompletionList).not.toHaveBeenCalled();
    expect(mocks.lspService.getCompletions).not.toHaveBeenCalled();
  });

  it("没有可用服务器时返回空 CompletionList 且不调用 service", async () => {
    await detectLSPServers();
    const list = await getLSPCompletions("python", "/repo/main.py", 1, 1, "");
    expect(list).toEqual({ items: [], isIncomplete: false });
    expect(mocks.getCompletionList).not.toHaveBeenCalled();
    expect(mocks.lspService.getCompletions).not.toHaveBeenCalled();
  });

  it("getCompletions 抛错时优雅降级返回空列表（不抛出）", async () => {
    await detectLSPServers();
    mocks.getCompletionList.mockRejectedValueOnce(new Error("method unavailable"));
    mocks.lspService.getCompletions.mockRejectedValueOnce(new Error("conn reset"));
    const list = await getLSPCompletions("go", "/repo/main.go", 1, 1, "");
    expect(list).toEqual({ items: [], isIncomplete: false });
  });

  it("不同文件的并发 completion 不会互相判定为过期", async () => {
    await detectLSPServers();
    let resolveFirst!: (value: unknown) => void;
    let resolveSecond!: (value: unknown) => void;
    const firstResponse = new Promise((resolve) => {
      resolveFirst = resolve;
    });
    const secondResponse = new Promise((resolve) => {
      resolveSecond = resolve;
    });
    mocks.getCompletionList.mockImplementation(
      (req: { filePath: string }) =>
        req.filePath.endsWith("first.go") ? firstResponse : secondResponse,
    );

    const first = getLSPCompletions(
      "go",
      "/repo/first.go",
      0,
      0,
      "package first",
    );
    const second = getLSPCompletions(
      "go",
      "/repo/second.go",
      0,
      0,
      "package second",
    );
    await vi.waitFor(() =>
      expect(mocks.getCompletionList).toHaveBeenCalledTimes(2),
    );
    resolveSecond({
      items: [{ label: "second", kind: 6, detail: "", insertText: "second" }],
      isIncomplete: false,
    });
    resolveFirst({
      items: [{ label: "first", kind: 6, detail: "", insertText: "first" }],
      isIncomplete: false,
    });

    await expect(first).resolves.toEqual(
      expect.objectContaining({
        items: [expect.objectContaining({ label: "first" })],
      }),
    );
    await expect(second).resolves.toEqual(
      expect.objectContaining({
        items: [expect.objectContaining({ label: "second" })],
      }),
    );
  });
});

describe("lsp store — Workspace Symbols", () => {
  it("聚合可用服务器并归一化为标准 WorkspaceSymbol", async () => {
    await detectLSPServers();
    mocks.lspService.getWorkspaceSymbols.mockImplementation(
      async (language: string) => language === "go"
        ? [{
            name: "main",
            kind: 12,
            containerName: "pkg",
            filePath: "/repo/main.go",
            line: 2,
            column: 5,
            endLine: 2,
            endColumn: 9,
          }]
        : [],
    );

    const symbols = await getWorkspaceSymbols("main");

    expect(mocks.lspService.getWorkspaceSymbols).toHaveBeenCalledWith("go", "main");
    expect(mocks.lspService.getWorkspaceSymbols).toHaveBeenCalledWith("typescript", "main");
    expect(symbols).toEqual([{
      name: "main",
      kind: 12,
      containerName: "pkg",
      location: {
        uri: "file:///repo/main.go",
        range: {
          start: { line: 2, character: 5 },
          end: { line: 2, character: 9 },
        },
      },
    }]);
  });

  it("空查询不启动服务器也不调用 service", async () => {
    await detectLSPServers();
    expect(await getWorkspaceSymbols("   ")).toEqual([]);
    expect(mocks.lspService.startServer).not.toHaveBeenCalled();
    expect(mocks.lspService.getWorkspaceSymbols).not.toHaveBeenCalled();
  });
});

describe("lsp store — Call Hierarchy compatibility names", () => {
  const item = {
    name: "main",
    kind: 12,
    filePath: "/repo/main.go",
    line: 2,
    column: 0,
    endLine: 4,
    endColumn: 1,
    selectionLine: 2,
    selectionColumn: 5,
    selectionEndLine: 2,
    selectionEndColumn: 9,
    data: { id: 1 },
  };

  it("prepareCallHierarchy 透传光标与文档内容", async () => {
    await detectLSPServers();
    mocks.lspService.prepareCallHierarchy.mockResolvedValueOnce([item]);
    const result = await prepareCallHierarchy(
      "go",
      "/repo/main.go",
      2,
      5,
      "package main",
    );
    expect(result).toEqual([item]);
    expect(mocks.lspService.prepareCallHierarchy).toHaveBeenCalledWith({
      language: "go",
      filePath: "/repo/main.go",
      line: 2,
      column: 5,
      content: "package main",
    });
  });

  it("getCallHierarchyIncoming 透传完整 item", async () => {
    await detectLSPServers();
    mocks.lspService.callHierarchyIncomingCalls.mockResolvedValueOnce([{ from: item, fromRanges: [] }]);
    const result = await getCallHierarchyIncoming(
      "go",
      "/repo/main.go",
      "package main",
      item,
    );
    expect(result).toEqual([{ from: item, fromRanges: [] }]);
    expect(mocks.lspService.callHierarchyIncomingCalls).toHaveBeenCalledWith(
      expect.objectContaining({ language: "go", filePath: "/repo/main.go" }),
      item,
    );
  });

  it("getCallHierarchyOutgoing 在 service 失败时降级为空数组", async () => {
    await detectLSPServers();
    mocks.lspService.callHierarchyOutgoingCalls.mockRejectedValueOnce(new Error("rpc failed"));
    await expect(getCallHierarchyOutgoing(
      "go",
      "/repo/main.go",
      "package main",
      item,
    )).resolves.toEqual([]);
  });
});

describe("lsp store — generated code action binding", () => {
  it("resolves a lazy code action through ResolveCodeAction", async () => {
    await detectLSPServers();
    const request = {
      language: "go",
      filePath: "/repo/main.go",
      line: 2,
      column: 5,
      content: "package main",
    };
    const action = { title: "Extract", data: { id: 7 } };
    mocks.resolveCodeAction.mockResolvedValueOnce({
      ...action,
      command: "gopls.extract_function",
    });

    await expect(resolveLSPCodeAction(request, action)).resolves.toMatchObject({
      command: "gopls.extract_function",
    });
    expect(mocks.resolveCodeAction).toHaveBeenCalledWith(request, action);
  });
});

describe("lsp store — getLSPHover", () => {
  it("调用 lspService.getHover 并返回文本", async () => {
    await detectLSPServers();
    const hover = await getLSPHover("typescript", "/repo/index.ts", 3, 7, "const x = 1;");
    expect(hover).toBe("func fmt.Println(a ...any) (int, error)");
    expect(mocks.lspService.getHover).toHaveBeenCalledTimes(1);
    const req = mocks.lspService.getHover.mock.calls[0][0];
    expect(req).toMatchObject({ language: "typescript", filePath: "/repo/index.ts", line: 3, column: 7 });
  });

  it("getHover 抛错时优雅降级返回空字符串", async () => {
    await detectLSPServers();
    mocks.lspService.getHover.mockRejectedValueOnce(new Error("boom"));
    const hover = await getLSPHover("go", "/repo/main.go", 1, 1, "");
    expect(hover).toBe("");
  });

  it("enabled 为 false 时返回空字符串", async () => {
    await detectLSPServers();
    lspState.enabled = false;
    const hover = await getLSPHover("go", "/repo/main.go", 1, 1, "");
    expect(hover).toBe("");
    expect(mocks.lspService.getHover).not.toHaveBeenCalled();
  });
});

describe("lsp store — stopAllLSPServers", () => {
  it("停止所有正在运行的服务器，未运行的不调用 stop", async () => {
    await detectLSPServers();
    // 仅 go 与 typescript 处于运行态。
    lspState.statuses["go"].running = true;
    lspState.statuses["typescript"].running = true;
    // javascript 未安装且未运行。
    await stopAllLSPServers();
    expect(mocks.lspService.stopServer).toHaveBeenCalledWith("go");
    expect(mocks.lspService.stopServer).toHaveBeenCalledWith("typescript");
    expect(mocks.lspService.stopServer).not.toHaveBeenCalledWith("javascript");
    expect(lspState.statuses["go"].running).toBe(false);
    expect(lspState.statuses["typescript"].running).toBe(false);
  });

  it("无运行中服务器时不调用 stop", async () => {
    await detectLSPServers();
    await stopAllLSPServers();
    expect(mocks.lspService.stopServer).not.toHaveBeenCalled();
  });
});

describe("lsp store — initLSPStore 与 reset 工具", () => {
  it("initLSPStore 触发一次 detectLSPServers（即 detectServers）", async () => {
    await initLSPStore();
    expect(mocks.lspService.detectServers).toHaveBeenCalledTimes(1);
    expect(Object.keys(lspState.statuses).length).toBeGreaterThan(0);
  });

  it("__resetLSPStoreForTesting 将状态恢复到初始值", async () => {
    await detectLSPServers();
    lspState.statuses["go"].running = true;
    lspState.busy = true;
    lspState.enabled = false;
    __resetLSPStoreForTesting();
    expect(lspState.statuses).toEqual({});
    expect(lspState.busy).toBe(false);
    expect(lspState.enabled).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// M-28: catch 块可观测性 — LSP 方法失败时 pushOutput 以 "warn" 级别记录
// ---------------------------------------------------------------------------

describe("lsp store — M-28 catch 块可观测性", () => {
  it("getLSPHover 失败时调用 pushOutput(source='LSP', severity='warn')", async () => {
    await detectLSPServers();
    mocks.lspService.getHover.mockRejectedValueOnce(new Error("hover rpc boom"));
    const result = await getLSPHover("go", "/repo/main.go", 1, 1, "");
    // 返回值不变（优雅降级）
    expect(result).toBe("");
    // pushOutput 被调用，source 为 "LSP"，severity 为 "warn"
    expect(pushOutput).toHaveBeenCalledWith(
      "LSP",
      "warn",
      expect.stringContaining("getLSPHover failed"),
    );
    expect(pushOutput).toHaveBeenCalledWith(
      "LSP",
      "warn",
      expect.stringContaining("hover rpc boom"),
    );
  });

  it("getLSPDefinition 失败时调用 pushOutput(source='LSP', severity='warn')", async () => {
    await detectLSPServers();
    // getLSPDefinition 不经过 lspService mock 的 getHover/getCompletions，
    // 而是调用 lspService.getDefinition — 需要在 mock 中补充。
    (mocks.lspService as any).getDefinition = vi.fn().mockRejectedValueOnce(new Error("def boom"));
    const result = await getLSPDefinition("go", "/repo/main.go", 1, 1, "");
    expect(result).toEqual([]);
    expect(pushOutput).toHaveBeenCalledWith(
      "LSP",
      "warn",
      expect.stringContaining("getLSPDefinition failed"),
    );
  });
});

describe("lsp store — semantic token normalization", () => {
  it("把后端 column/modifier indices 归一化为 canonical start/bitmask", async () => {
    await detectLSPServers();
    mocks.lspService.getSemanticTokens.mockResolvedValueOnce([
      {
        line: 2,
        column: 7,
        length: 4,
        type: 12,
        modifiers: [0, 2, 6],
      },
    ]);

    await expect(
      getLSPSemanticTokens("go", "/repo/main.go", "package main\n"),
    ).resolves.toEqual([
      { line: 2, start: 7, length: 4, type: 12, modifiers: 69 },
    ]);
  });
});

// ---------------------------------------------------------------------------
// Priority 1: getLSPInlayHints — 调用后端 GetInlayHints 绑定并优雅降级
// ---------------------------------------------------------------------------

describe("lsp store — getLSPInlayHints", () => {
  it("优先调用 GetInlayHintsRaw 并保留结构化 label parts（含懒启动）", async () => {
    await detectLSPServers();
    const hints = await getLSPInlayHints("go", "/repo/main.go", "package main\n");
    expect(hints).toEqual(sampleRawInlayHints);
    expect(mocks.getInlayHintsRaw).toHaveBeenCalledTimes(1);
    expect(mocks.lspService.getInlayHints).not.toHaveBeenCalled();
    // 验证传给后端的请求参数：line/column 为 0（后端自行构造 range）。
    const req = mocks.getInlayHintsRaw.mock.calls[0][0];
    expect(req).toMatchObject({
      language: "go",
      filePath: "/repo/main.go",
      line: 0,
      column: 0,
      content: "package main\n",
    });
    // 懒启动：首次调用会触发 startServer。
    expect(mocks.lspService.startServer).toHaveBeenCalledWith("go");
  });

  it("enabled 为 false 时直接返回空列表且不调用 service", async () => {
    await detectLSPServers();
    lspState.enabled = false;
    const hints = await getLSPInlayHints("go", "/repo/main.go", "");
    expect(hints).toEqual([]);
    expect(mocks.getInlayHintsRaw).not.toHaveBeenCalled();
    expect(mocks.lspService.getInlayHints).not.toHaveBeenCalled();
  });

  it("服务器未安装时返回空列表且不调用 getInlayHints", async () => {
    await detectLSPServers();
    // javascript 在 mock 中标记为不可用。
    const hints = await getLSPInlayHints("javascript", "/repo/app.js", "");
    expect(hints).toEqual([]);
    expect(mocks.getInlayHintsRaw).not.toHaveBeenCalled();
    expect(mocks.lspService.getInlayHints).not.toHaveBeenCalled();
  });

  it("getInlayHints 抛错时优雅降级返回空列表（不抛出）", async () => {
    await detectLSPServers();
    mocks.getInlayHintsRaw.mockRejectedValueOnce(new Error("raw unavailable"));
    mocks.lspService.getInlayHints.mockRejectedValueOnce(new Error("inlay rpc boom"));
    const hints = await getLSPInlayHints("go", "/repo/main.go", "");
    expect(hints).toEqual([]);
  });

  it("getInlayHints 失败时调用 pushOutput(source='LSP', severity='warn') — M-28", async () => {
    await detectLSPServers();
    mocks.getInlayHintsRaw.mockRejectedValueOnce(new Error("raw unavailable"));
    mocks.lspService.getInlayHints.mockRejectedValueOnce(new Error("inlay rpc boom"));
    await getLSPInlayHints("go", "/repo/main.go", "");
    expect(pushOutput).toHaveBeenCalledWith(
      "LSP",
      "warn",
      expect.stringContaining("getLSPInlayHints failed"),
    );
    expect(pushOutput).toHaveBeenCalledWith(
      "LSP",
      "warn",
      expect.stringContaining("inlay rpc boom"),
    );
  });
});
