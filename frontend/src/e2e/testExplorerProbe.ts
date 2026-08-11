// Koyori IDE 模块 · Test Explorer Probe。
// 喵，这是 Koyori IDE 的 Test Explorer Probe 模块（前端实现）~
import { Events } from "@wailsio/runtime";
import type { TestEntry, TestNode } from "@/stores/testExplorer";

const resultEvent = "e2e:g15-test-explorer-result";

interface TestExplorerProbeConfig {
  runId: string;
  workspace: string;
  filePath: string;
  content: string;
  passLine: number;
  failLine: number;
}

interface TestExplorerProbeResult {
  runId: string;
  passExitCode: number;
  failExitCode: number;
  passEntryStatus: string;
  failEntryStatus: string;
  passTreeStatus: string;
  failTreeStatus: string;
  passOutputVisible: boolean;
  failOutputVisible: boolean;
  runningCleared: boolean;
  ok: boolean;
  error?: string;
}

function message(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function findTestNode(nodes: TestNode[], id: string): string {
  for (const node of nodes) {
    if (node.id === id) return node.status ?? "missing";
    if (node.children) {
      const status = findTestNode(node.children, id);
      if (status !== "missing") return status;
    }
  }
  return "missing";
}

async function runProbe(config: TestExplorerProbeConfig): Promise<TestExplorerProbeResult> {
  const { appState } = await import("@/stores/app");
  const { toolchainState } = await import("@/stores/toolchain");
  const {
    buildTestTree,
    runDiscoveredTest,
    testExplorerState,
  } = await import("@/stores/testExplorer");

  appState.currentProject = config.workspace;
  const passEntry: TestEntry = {
    id: "g15-pass",
    file: config.filePath,
    line: config.passLine,
    name: "TestPass",
    language: "go",
    status: "idle",
  };
  const failEntry: TestEntry = {
    id: "g15-fail",
    file: config.filePath,
    line: config.failLine,
    name: "TestFail",
    language: "go",
    status: "idle",
  };
  testExplorerState.entries = [passEntry, failEntry];
  testExplorerState.tree = buildTestTree(testExplorerState.entries, config.workspace);

  const passResult = await runDiscoveredTest(passEntry);
  const passTreeStatus = findTestNode(testExplorerState.tree, passEntry.id);
  const failResult = await runDiscoveredTest(failEntry);
  const failTreeStatus = findTestNode(testExplorerState.tree, failEntry.id);
  const passOutput = testExplorerState.outputsByTest[passEntry.id] ?? "";
  const failOutput = testExplorerState.outputsByTest[failEntry.id] ?? "";

  const ok =
    passResult?.success === true &&
    passResult.exitCode === 0 &&
    passEntry.status === "pass" &&
    passTreeStatus === "passed" &&
    failResult?.success === false &&
    (failResult.exitCode ?? 0) !== 0 &&
    failEntry.status === "fail" &&
    failTreeStatus === "failed" &&
    passOutput.length > 0 &&
    failOutput.includes("G15_EXPECTED_FAILURE") &&
    toolchainState.runningId === null;

  return {
    runId: config.runId,
    passExitCode: passResult?.exitCode ?? -1,
    failExitCode: failResult?.exitCode ?? -1,
    passEntryStatus: passEntry.status,
    failEntryStatus: failEntry.status,
    passTreeStatus,
    failTreeStatus,
    passOutputVisible: passOutput.length > 0,
    failOutputVisible: failOutput.includes("G15_EXPECTED_FAILURE"),
    runningCleared: toolchainState.runningId === null,
    ok,
    error: ok ? undefined : `pass=${passEntry.status}/${passTreeStatus}/${passResult?.exitCode} fail=${failEntry.status}/${failTreeStatus}/${failResult?.exitCode}`,
  };
}

export function installTestExplorerProbe(): void {
  const target = globalThis as typeof globalThis & {
    __koyoriIdeRunG15TestExplorerProbe?: (config: TestExplorerProbeConfig) => Promise<void>;
  };
  target.__koyoriIdeRunG15TestExplorerProbe = async (config) => {
    let result: TestExplorerProbeResult;
    try {
      result = await runProbe(config);
    } catch (error: unknown) {
      result = {
        runId: config.runId,
        passExitCode: -1,
        failExitCode: -1,
        passEntryStatus: "error",
        failEntryStatus: "error",
        passTreeStatus: "error",
        failTreeStatus: "error",
        passOutputVisible: false,
        failOutputVisible: false,
        runningCleared: false,
        ok: false,
        error: message(error),
      };
    }
    await Events.Emit(resultEvent, result);
  };
}
