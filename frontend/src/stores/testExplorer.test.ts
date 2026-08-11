// Priority-6: 测试树 + 连续测试 的单元测试。
import { describe, it, expect, beforeEach, vi } from "vitest";

vi.mock("@wailsio/runtime", () => ({
  Events: { On: vi.fn(() => () => {}), Emit: vi.fn() },
}));

vi.mock("@/api/services", () => ({
  fileService: { listAllFiles: vi.fn(), readFile: vi.fn() },
  toolchainService: { runGoTestsJSON: vi.fn() },
}));

vi.mock("@/stores/app", () => ({
  appState: {
    currentProject: "/proj",
    cursorLine: 0,
    cursorColumn: 0,
    editorJumpSeq: 0,
  },
}));

vi.mock("@/stores/toolchain", () => ({
  runTestAtCursor: vi.fn(),
}));

vi.mock("@/stores/editor", () => ({
  openFileFromPath: vi.fn(),
}));

vi.mock("@/stores/output", () => ({
  pushOutput: vi.fn(),
}));

vi.mock("@/lib/notifications", () => ({
  notifyError: vi.fn(),
  notifySuccess: vi.fn(),
}));

// runGoTestsJSON / discoverTests 内部动态 import 的 workspaceModules，mock 掉避免拉起重依赖。
vi.mock("@/stores/workspaceModules", () => ({
  workspaceModulesState: { activeRoot: null },
}));

import { Events } from "@wailsio/runtime";
import { fileService, toolchainService } from "@/api/services";
import { runTestAtCursor } from "@/stores/toolchain";
import {
  buildTestTree,
  runDiscoveredTest,
  testExplorerState,
  findRelatedEntries,
  setContinuousTesting,
  toggleContinuousTesting,
  initContinuousTesting,
  initTestExplorerRuntime,
  runContinuousTestsForFile,
  teardownContinuousTesting,
  selectTest,
  selectedTestOutput,
  toggleSuite,
  setSuiteExpanded,
} from "./testExplorer";
import type { TestEntry, TestNode } from "./testExplorer";

function makeEntry(id: string, file: string, name: string, lang: TestEntry["language"] = "go"): TestEntry {
  return { id, file, line: 0, name, language: lang, status: "idle" };
}

/** 递归统计树中叶子（test）节点数量。 */
function countLeaves(nodes: TestNode[]): number {
  let n = 0;
  for (const node of nodes) {
    if (node.type === "test") n += 1;
    else if (node.children) n += countLeaves(node.children);
  }
  return n;
}

/** 在树中按名称查找节点。 */
function findNode(nodes: TestNode[], name: string): TestNode | undefined {
  for (const node of nodes) {
    if (node.name === name) return node;
    if (node.children) {
      const found = findNode(node.children, name);
      if (found) return found;
    }
  }
  return undefined;
}

describe("testExplorer store (Priority-6)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    testExplorerState.entries = [];
    testExplorerState.tree = [];
    testExplorerState.outputsByTest = {};
    testExplorerState.selectedTestId = "";
    testExplorerState.continuousTesting = false;
    testExplorerState.continuousRunning = false;
    testExplorerState.expanded = {};
    teardownContinuousTesting();
  });

  describe("buildTestTree", () => {
    it("把 pkg/subpkg/TestFoo 扁平路径构建为嵌套 suite/test 树", () => {
      const entries = [
        makeEntry("e1", "/proj/pkg/subpkg/foo_test.go", "TestFoo"),
      ];
      const tree = buildTestTree(entries, "/proj");

      // 期望：suite "pkg" > suite "subpkg" > test "TestFoo"
      expect(tree).toHaveLength(1);
      expect(tree[0].type).toBe("suite");
      expect(tree[0].name).toBe("pkg");
      expect(tree[0].id).toBe("suite:pkg");
      expect(tree[0].children).toHaveLength(1);

      const subpkg = tree[0].children![0];
      expect(subpkg.type).toBe("suite");
      expect(subpkg.name).toBe("subpkg");
      expect(subpkg.id).toBe("suite:pkg/subpkg");
      expect(subpkg.children).toHaveLength(1);

      const testLeaf = subpkg.children![0];
      expect(testLeaf.type).toBe("test");
      expect(testLeaf.name).toBe("TestFoo");
      // 叶子 id 复用 entry id，便于回查运行/输出。
      expect(testLeaf.id).toBe("e1");
      expect(testLeaf.status).toBe("pending");
    });

    it("复用相同 suite 路径节点，不重复创建", () => {
      const entries = [
        makeEntry("e1", "/proj/pkg/a_test.go", "TestA"),
        makeEntry("e2", "/proj/pkg/b_test.go", "TestB"),
      ];
      const tree = buildTestTree(entries, "/proj");

      expect(tree).toHaveLength(1);
      expect(tree[0].name).toBe("pkg");
      expect(tree[0].children).toHaveLength(2);
      expect(tree[0].children!.map((c) => c.name).sort()).toEqual(["TestA", "TestB"]);
    });

    it(">500 测试全部进入树形，无截断", () => {
      const entries: TestEntry[] = [];
      for (let i = 0; i < 600; i++) {
        entries.push(
          makeEntry(`e${i}`, `/proj/pkg${i % 5}/t${i}_test.go`, `Test${i}`),
        );
      }
      const tree = buildTestTree(entries, "/proj");

      // 叶子节点数应等于全部 600 个测试（旧实现会截断到 500）。
      expect(countLeaves(tree)).toBe(600);
      // 5 个包 suite。
      expect(tree).toHaveLength(5);
    });

    it("映射 entry status 到 node status", () => {
      const entries = [
        { ...makeEntry("e1", "/proj/pkg/a_test.go", "TestA"), status: "pass" as const },
        { ...makeEntry("e2", "/proj/pkg/b_test.go", "TestB"), status: "fail" as const },
        { ...makeEntry("e3", "/proj/pkg/c_test.go", "TestC"), status: "run" as const },
        { ...makeEntry("e4", "/proj/pkg/d_test.go", "TestD"), status: "skip" as const },
      ];
      const tree = buildTestTree(entries, "/proj");
      const pkg = tree[0];
      expect(findNode(pkg.children!, "TestA")!.status).toBe("passed");
      expect(findNode(pkg.children!, "TestB")!.status).toBe("failed");
      expect(findNode(pkg.children!, "TestC")!.status).toBe("running");
      expect(findNode(pkg.children!, "TestD")!.status).toBe("skipped");
    });

    it("支持多层 / 嵌套与 Go 子测试名 TestFoo/sub", () => {
      // 多层目录 + 子测试名（含 /）应正确嵌套。
      const entries = [
        makeEntry("e1", "/proj/a/b/c/foo_test.go", "TestFoo/sub"),
      ];
      const tree = buildTestTree(entries, "/proj");
      // a > b > c > TestFoo > sub
      expect(tree[0].name).toBe("a");
      expect(tree[0].children![0].name).toBe("b");
      expect(tree[0].children![0].children![0].name).toBe("c");
      expect(tree[0].children![0].children![0].children![0].name).toBe("TestFoo");
      expect(tree[0].children![0].children![0].children![0].type).toBe("suite");
      expect(tree[0].children![0].children![0].children![0].children![0].name).toBe("sub");
      expect(tree[0].children![0].children![0].children![0].children![0].type).toBe("test");
    });

    it("不同包生成独立的根 suite", () => {
      const entries = [
        makeEntry("e1", "/proj/pkgA/a_test.go", "TestA"),
        makeEntry("e2", "/proj/pkgB/b_test.go", "TestB"),
      ];
      const tree = buildTestTree(entries, "/proj");
      expect(tree).toHaveLength(2);
      expect(tree.map((n) => n.name).sort()).toEqual(["pkgA", "pkgB"]);
    });
  });

  describe("runDiscoveredTest", () => {
    it("keeps the derived tree and output aligned with a real exit result", async () => {
      const entry = makeEntry("e1", "/proj/pkg/fail_test.go", "TestFail");
      testExplorerState.entries = [entry];
      testExplorerState.tree = buildTestTree([entry], "/proj");
      (fileService.readFile as any).mockResolvedValue("func TestFail(t *testing.T) {}\n");
      (runTestAtCursor as any).mockResolvedValue({
        success: false,
        output: "FAIL\nexit status 1",
        errors: [],
        durationMs: 10,
        notInstalled: false,
        canceled: false,
        exitCode: 1,
      });

      const result = await runDiscoveredTest(entry);

      expect(result?.exitCode).toBe(1);
      expect(entry.status).toBe("fail");
      expect(findNode(testExplorerState.tree, "TestFail")?.status).toBe("failed");
      expect(testExplorerState.outputsByTest.e1).toContain("exit status 1");
    });
  });

  describe("per-test 输出面板", () => {
    it("selectTest + selectedTestOutput 读取对应输出", () => {
      testExplorerState.outputsByTest["e1"] = "stdout line\n";
      selectTest("e1");
      expect(testExplorerState.selectedTestId).toBe("e1");
      expect(selectedTestOutput()).toBe("stdout line\n");
    });

    it("未选中或无输出时返回空串", () => {
      selectTest("nope");
      expect(selectedTestOutput()).toBe("");
    });

    it("toggleSuite / setSuiteExpanded 切换展开状态", () => {
      setSuiteExpanded("suite:pkg", true);
      expect(testExplorerState.expanded["suite:pkg"]).toBe(true);
      toggleSuite("suite:pkg");
      expect(testExplorerState.expanded["suite:pkg"]).toBe(false);
      toggleSuite("suite:pkg");
      expect(testExplorerState.expanded["suite:pkg"]).toBe(true);
    });
  });

  describe("findRelatedEntries", () => {
    it("返回同目录下的测试条目", () => {
      const entries = [
        makeEntry("e1", "/proj/pkg/foo_test.go", "TestFoo"),
        makeEntry("e2", "/proj/pkg/bar_test.go", "TestBar"),
        makeEntry("e3", "/proj/other/baz_test.go", "TestBaz"),
      ];
      const related = findRelatedEntries(entries, "/proj/pkg/foo.go");
      expect(related.map((e) => e.id).sort()).toEqual(["e1", "e2"]);
    });

    it("保存文件本身就是测试文件时也命中", () => {
      const entries = [makeEntry("e1", "/proj/pkg/foo_test.go", "TestFoo")];
      const related = findRelatedEntries(entries, "/proj/pkg/foo_test.go");
      expect(related).toHaveLength(1);
    });

    it("不同目录不命中", () => {
      const entries = [makeEntry("e1", "/proj/pkg/foo_test.go", "TestFoo")];
      const related = findRelatedEntries(entries, "/proj/other/source.go");
      expect(related).toHaveLength(0);
    });

    it("兼容 Windows 反斜杠路径", () => {
      const entries = [makeEntry("e1", "C:\\proj\\pkg\\foo_test.go", "TestFoo")];
      const related = findRelatedEntries(entries, "C:\\proj\\pkg\\foo.go");
      expect(related).toHaveLength(1);
    });
  });

  describe("连续测试开关", () => {
    it("toggleContinuousTesting 翻转开关", () => {
      expect(testExplorerState.continuousTesting).toBe(false);
      toggleContinuousTesting();
      expect(testExplorerState.continuousTesting).toBe(true);
      toggleContinuousTesting();
      expect(testExplorerState.continuousTesting).toBe(false);
    });

    it("setContinuousTesting(true) 注册 file:saved 监听器", () => {
      setContinuousTesting(true);
      expect(Events.On).toHaveBeenCalledWith("file:saved", expect.any(Function));
      expect(testExplorerState.continuousTesting).toBe(true);
    });

    it("initContinuousTesting 幂等（多次调用只注册一次）", () => {
      initContinuousTesting();
      initContinuousTesting();
      initContinuousTesting();
      const calls = (Events.On as any).mock.calls.filter(
        (c: any[]) => c[0] === "file:saved",
      );
      expect(calls).toHaveLength(1);
    });

    it("runtime 仅在连续测试已启用时重新注册监听器", () => {
      initTestExplorerRuntime();
      expect(Events.On).not.toHaveBeenCalled();

      testExplorerState.continuousTesting = true;
      initTestExplorerRuntime();
      initTestExplorerRuntime();
      expect(Events.On).toHaveBeenCalledTimes(1);

      teardownContinuousTesting();
      initTestExplorerRuntime();
      expect(Events.On).toHaveBeenCalledTimes(2);
    });
  });

  describe("连续测试触发", () => {
    it("开关关闭时 file:saved 不触发测试运行", async () => {
      testExplorerState.entries = [
        makeEntry("e1", "/proj/pkg/foo_test.go", "TestFoo"),
      ];
      setContinuousTesting(false);
      await runContinuousTestsForFile("/proj/pkg/foo.go");
      expect(toolchainService.runGoTestsJSON).not.toHaveBeenCalled();
    });

    it("开关开启且无相关测试时不运行", async () => {
      testExplorerState.entries = [
        makeEntry("e1", "/proj/pkg/foo_test.go", "TestFoo"),
      ];
      setContinuousTesting(true);
      await runContinuousTestsForFile("/proj/other/source.go");
      expect(toolchainService.runGoTestsJSON).not.toHaveBeenCalled();
    });

    it("file:saved 触发自动运行相关 Go 测试", async () => {
      // 捕获 Events.On 注册的 file:saved 回调。
      let savedCb: ((e: { data: string }) => void) | null = null;
      (Events.On as any).mockImplementationOnce(
        (name: string, cb: (e: { data: string }) => void) => {
          if (name === "file:saved") savedCb = cb;
          return () => {};
        },
      );

      testExplorerState.entries = [
        makeEntry("e1", "/proj/pkg/foo_test.go", "TestFoo"),
        makeEntry("e2", "/proj/pkg/bar_test.go", "TestBar"),
      ];
      (toolchainService.runGoTestsJSON as any).mockResolvedValue({
        success: true,
        output: "ok",
        events: [],
        statusByTest: { TestFoo: "pass", TestBar: "pass" },
        durationMs: 10,
      });

      setContinuousTesting(true);
      expect(savedCb).not.toBeNull();

      // 模拟后端发出 file:saved 事件。
      savedCb!({ data: "/proj/pkg/source.go" });
      // 等待异步运行完成（回调内为 fire-and-forget，需轮询）。
      await vi.waitFor(() => {
        expect(toolchainService.runGoTestsJSON).toHaveBeenCalledTimes(1);
      });
      // 应以相关测试所在包目录调用。
      const args = (toolchainService.runGoTestsJSON as any).mock.calls[0];
      expect(args[0]).toContain("pkg");
    });

    it("continuousRunning 指示器在运行期间为 true", async () => {
      testExplorerState.entries = [
        makeEntry("e1", "/proj/pkg/foo_test.go", "TestFoo"),
      ];
      let resolveRun: () => void = () => {};
      (toolchainService.runGoTestsJSON as any).mockImplementation(
        () =>
          new Promise((res) => {
            resolveRun = () =>
              res({ success: true, output: "", events: [], statusByTest: {}, durationMs: 0 });
          }),
      );
      setContinuousTesting(true);
      const p = runContinuousTestsForFile("/proj/pkg/foo.go");
      // continuousRunning 在同步段即被置为 true。
      expect(testExplorerState.continuousRunning).toBe(true);
      // 等待 toolchainService.runGoTestsJSON 被调用（此时 resolveRun 已指向真正的 resolver）。
      await vi.waitFor(() => expect(toolchainService.runGoTestsJSON).toHaveBeenCalled());
      resolveRun();
      await p;
      expect(testExplorerState.continuousRunning).toBe(false);
    });
  });
});
