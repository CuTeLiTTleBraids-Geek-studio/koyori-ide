/**
 * prompt-10/11/12: test explorer with go test -json + unified Run/Coverage/Debug (12-J).
 * Priority-6: 测试树（嵌套 suite/test，不再扁平 500 上限）+ 连续测试 + per-test 输出。
 */
// Koyori IDE 模块 · Test Explorer；交互服务：文件系统（FileService）、工具链（ToolchainService）。
// 喵，这是 Koyori IDE 的 Test Explorer 模块（前端实现）~
import { reactive } from "vue";
import { Events } from "@wailsio/runtime";
import { fileService, toolchainService } from "@/api/services";
import { appState } from "@/stores/app";
import { runTestAtCursor } from "@/stores/toolchain";
import { openFileFromPath } from "@/stores/editor";
import { pushOutput } from "@/stores/output";
import { notifyError, notifySuccess } from "@/lib/notifications";
import type { FileSavedEvent, ToolchainResult } from "@/types";

export type TestRunStatus = "idle" | "run" | "pass" | "fail" | "skip";

export interface TestEntry {
  id: string;
  file: string;
  line: number; // 0-based
  name: string;
  language: "go" | "typescript" | "javascript";
  status: TestRunStatus;
}

// Priority-6: 树形测试节点。suite 可嵌套 children，test 为叶子节点。
export type TestNodeStatus = "passed" | "failed" | "skipped" | "running" | "pending";

export interface TestNode {
  id: string;
  name: string;
  type: "suite" | "test";
  status?: TestNodeStatus;
  children?: TestNode[];
  duration?: number;
  error?: string;
}

export const testExplorerState = reactive({
  entries: [] as TestEntry[],
  // Priority-6: 树形根节点列表（由扁平 entries 派生，无 500 上限）。
  tree: [] as TestNode[],
  loading: false,
  running: false,
  error: "",
  lastJSONOutput: "",
  // Priority-6: per-test 输出，按测试节点 id（= entry id）索引。
  outputsByTest: {} as Record<string, string>,
  // Priority-6: 当前选中查看输出的测试 id。
  selectedTestId: "",
  // Priority-6: 连续测试开关与运行指示器。
  continuousTesting: false,
  continuousRunning: false,
  // Priority-6: suite 节点展开状态，按 suite id 索引。
  expanded: {} as Record<string, boolean>,
});

const goTestRe = /^\s*func\s+(Test[A-Za-z0-9_]+)/;
const jsTestRe = /^\s*(?:it|test)(?:\.\w+)?\s*(?:\([^)]*\)\s*)?\(\s*['"`]([^'"`]+)['"`]/;

// Priority-6: TestRunStatus → TestNodeStatus 映射。
function entryStatusToNodeStatus(s: TestRunStatus): TestNodeStatus {
  switch (s) {
    case "run":
      return "running";
    case "pass":
      return "passed";
    case "fail":
      return "failed";
    case "skip":
      return "skipped";
    default:
      return "pending";
  }
}

/**
 * Priority-6: 将文件路径规整为相对于 root 的正斜杠目录路径。
 * 不在 root 下时退回到文件所在目录本身。
 */
function relDirOf(file: string, root: string): string {
  const normFile = file.replace(/\\/g, "/");
  const normRoot = root.replace(/[\\/]+$/, "").replace(/\\/g, "/");
  let rel = normFile;
  if (normRoot && normFile.startsWith(normRoot + "/")) {
    rel = normFile.slice(normRoot.length + 1);
  }
  const slash = rel.lastIndexOf("/");
  return slash >= 0 ? rel.slice(0, slash) : "";
}

/**
 * Priority-6: 由扁平 entries 构建嵌套 suite/test 树。
 *
 * 每个 entry 的路径由 `relDir/name` 拼出，按 `/` 切分得到层级；
 * 中间段为 suite 节点，末段为 test 叶子。同一 suite 路径复用同一节点。
 * 不做任何截断 —— 树形结构天然支持大型测试套件。
 *
 * 导出以便单元测试。
 */
export function buildTestTree(entries: TestEntry[], root = ""): TestNode[] {
  const roots: TestNode[] = [];
  // suitePath → node 引用（含 roots 层级）。
  const suiteIndex = new Map<string, TestNode>();

  for (const entry of entries) {
    const relDir = relDirOf(entry.file, root);
    const fullPath = relDir ? `${relDir}/${entry.name}` : entry.name;
    // 按 `/` 切分得到层级（目录分隔符；亦兼容 Go 子测试名 TestFoo/sub）。
    // 不按 `.` 切分，以免破坏含点号的测试描述名（如 JS "handles a.b"）。
    const segments = fullPath.split("/").map((s) => s.trim()).filter((s) => s.length > 0);
    if (segments.length === 0) continue;

    let children = roots;
    for (let i = 0; i < segments.length; i++) {
      const seg = segments[i];
      const isLeaf = i === segments.length - 1;
      if (isLeaf) {
        // 叶子 test 节点：id 复用 entry id，便于回查运行/输出。
        children.push({
          id: entry.id,
          name: seg,
          type: "test",
          status: entryStatusToNodeStatus(entry.status),
        });
      } else {
        const suitePath = segments.slice(0, i + 1).join("/");
        let node = suiteIndex.get(suitePath);
        if (!node) {
          node = {
            id: `suite:${suitePath}`,
            name: seg,
            type: "suite",
            children: [],
          };
          suiteIndex.set(suitePath, node);
          children.push(node);
        }
        children = node.children!;
      }
    }
  }
  return roots;
}

/** Priority-6: 依据当前 entries 重建树。 */
export function rebuildTree(): void {
  const root = appState.currentProject || "";
  testExplorerState.tree = buildTestTree(testExplorerState.entries, root);
}

export async function discoverTests(): Promise<void> {
  // prompt-12 12-H: prefer active workspace root
  let root = appState.currentProject;
  try {
    const { workspaceModulesState } = await import("@/stores/workspaceModules");
    if (workspaceModulesState.activeRoot) root = workspaceModulesState.activeRoot;
  } catch {
    /* ignore */
  }
  if (!root) {
    testExplorerState.entries = [];
    testExplorerState.tree = [];
    return;
  }
  testExplorerState.loading = true;
  testExplorerState.error = "";
  try {
    const files = await fileService.listAllFiles(root);
    const candidates = files.filter(
      (f) =>
        f.endsWith("_test.go") ||
        f.endsWith(".test.ts") ||
        f.endsWith(".test.tsx") ||
        f.endsWith(".spec.ts") ||
        f.endsWith(".test.js") ||
        f.endsWith(".spec.js"),
    );
    const entries: TestEntry[] = [];
    for (const rel of candidates) {
      const path =
        rel.includes(":") || rel.startsWith("/") || rel.startsWith(root)
          ? rel
          : root.replace(/[\\/]$/, "") + "/" + rel.replace(/^[\\/]/, "");
      try {
        const content = await fileService.readFile(path);
        const lines = content.split(/\r?\n/);
        const isGo = path.endsWith(".go");
        for (let i = 0; i < lines.length; i++) {
          if (isGo) {
            const m = lines[i].match(goTestRe);
            if (m) {
              entries.push({
                id: `${path}:${i}:${m[1]}`,
                file: path,
                line: i,
                name: m[1],
                language: "go",
                status: "idle",
              });
            }
          } else {
            const m = lines[i].match(jsTestRe);
            if (m) {
              entries.push({
                id: `${path}:${i}:${m[1]}`,
                file: path,
                line: i,
                name: m[1],
                language: path.endsWith(".js") ? "javascript" : "typescript",
                status: "idle",
              });
            }
          }
        }
      } catch {
        /* skip */
      }
    }
    // Priority-6: 移除扁平 500 上限 —— 树形结构自然承载大型套件。
    testExplorerState.entries = entries;
    testExplorerState.tree = buildTestTree(entries, root);
  } catch (e) {
    testExplorerState.error = e instanceof Error ? e.message : String(e);
  } finally {
    testExplorerState.loading = false;
  }
}

/** prompt-12 12-J: Run this test */
export async function runDiscoveredTest(entry: TestEntry): Promise<ToolchainResult | null> {
  try {
    entry.status = "run";
    const content = await fileService.readFile(entry.file);
    const result = await runTestAtCursor(entry.language, entry.file, entry.line, content);
    entry.status = result?.canceled ? "skip" : result?.success ? "pass" : "fail";
    // Priority-6: 记录 per-test 输出。
    testExplorerState.outputsByTest[entry.id] = result?.output ?? "";
    return result;
  } catch (e) {
    entry.status = "fail";
    testExplorerState.error = e instanceof Error ? e.message : String(e);
    testExplorerState.outputsByTest[entry.id] = e instanceof Error ? e.message : String(e);
    return null;
  } finally {
    rebuildTree();
  }
}

/** prompt-12 12-J: Debug this test */
export async function debugDiscoveredTest(entry: TestEntry): Promise<void> {
  try {
    const content = await fileService.readFile(entry.file);
    const { debugTestAtCursor } = await import("@/stores/debug");
    await debugTestAtCursor(entry.language, entry.file, entry.line, content);
  } catch (e) {
    notifyError(e instanceof Error ? e.message : String(e));
  }
}

/** prompt-12 12-J: Coverage for package containing this test */
export async function coverageDiscoveredTest(entry: TestEntry): Promise<void> {
  try {
    const dir = entry.file.replace(/[\\/][^\\/]+$/, "");
    if (entry.language === "go") {
      const { coverageService } = await import("@/api/services");
      const { coverageState, normalizeCoveragePath } = await import("@/stores/coverage");
      coverageState.loading = true;
      const result = await coverageService.runPackageCoverage(dir);
      coverageState.hits = (result.hits || []).map((h) => ({
        file: normalizeCoveragePath(h.file),
        line: h.line,
        covered: h.covered,
      }));
      coverageState.loading = false;
      notifySuccess(`Coverage for ${dir}`);
    } else {
      const { runVitestCoverage } = await import("@/stores/coverage");
      await runVitestCoverage();
    }
  } catch (e) {
    notifyError(e instanceof Error ? e.message : String(e));
  }
}

export async function runGoTestsJSON(packageDir?: string, runRegex?: string): Promise<void> {
  let dir = packageDir || appState.currentProject || "";
  try {
    const { workspaceModulesState } = await import("@/stores/workspaceModules");
    if (!packageDir && workspaceModulesState.activeRoot) dir = workspaceModulesState.activeRoot;
  } catch {
    /* ignore */
  }
  if (!dir) {
    notifyError("Open a Go project first");
    return;
  }
  testExplorerState.running = true;
  for (const e of testExplorerState.entries) {
    if (e.language === "go") e.status = "run";
  }
  try {
    const result = await toolchainService.runGoTestsJSON(dir, runRegex || "");
    testExplorerState.lastJSONOutput = result.output || "";
    const status = result.statusByTest || {};
    // Priority-6: 从 events 收集 per-test 输出（按测试名拼接）。
    const outputByName: Record<string, string> = {};
    for (const ev of result.events || []) {
      if (ev.test && ev.output) {
        outputByName[ev.test] = (outputByName[ev.test] || "") + ev.output;
      }
    }
    for (const e of testExplorerState.entries) {
      if (e.language !== "go") continue;
      const st = status[e.name] as TestRunStatus | undefined;
      if (st === "pass" || st === "fail" || st === "skip" || st === "run") {
        e.status = st;
      } else if (result.success) {
        e.status = e.status === "run" ? "pass" : e.status;
      } else {
        e.status = e.status === "run" ? "fail" : e.status;
      }
      if (outputByName[e.name]) {
        testExplorerState.outputsByTest[e.id] = outputByName[e.name];
      }
    }
    // Priority-6: 同步树形状态。
    rebuildTree();
    pushOutput("go test -json", result.success ? "info" : "error", result.output || "");
    if (result.success) notifySuccess("go test -json completed");
    else notifyError("Some tests failed (see Output / tree status)");
  } catch (e) {
    notifyError(e instanceof Error ? e.message : String(e));
  } finally {
    testExplorerState.running = false;
  }
}

export async function jumpToTest(entry: TestEntry): Promise<void> {
  try {
    await openFileFromPath(entry.file);
    appState.cursorLine = entry.line + 1;
    appState.cursorColumn = 1;
    appState.editorJumpSeq = (appState.editorJumpSeq || 0) + 1;
  } catch {
    /* notified */
  }
}

// ============================================================================
// Priority-6: per-test 输出面板 + suite 展开/折叠
// ============================================================================

/** Priority-6: 选中某个测试节点以在输出面板查看其输出。 */
export function selectTest(testId: string): void {
  testExplorerState.selectedTestId = testId;
}

/** Priority-6: 读取当前选中测试的输出。 */
export function selectedTestOutput(): string {
  return testExplorerState.outputsByTest[testExplorerState.selectedTestId] ?? "";
}

/** Priority-6: 切换 suite 节点展开/折叠。 */
export function toggleSuite(suiteId: string): void {
  testExplorerState.expanded[suiteId] = !testExplorerState.expanded[suiteId];
}

/** Priority-6: 设置 suite 展开状态（便于测试与批量操作）。 */
export function setSuiteExpanded(suiteId: string, expanded: boolean): void {
  testExplorerState.expanded[suiteId] = expanded;
}

// ============================================================================
// Priority-6: 连续测试 —— file:saved 触发自动跑相关测试
// ============================================================================

let continuousListenerRegistered = false;
const continuousCanceller: Array<() => void> = [];

/**
 * Priority-6: 找到与保存文件相关的测试条目。
 *
 * 相关性规则：测试所在文件目录 === 保存文件目录（Go 同包；
 * TS/JS 同目录测试文件）。保存文件本身就是测试文件时也命中。
 *
 * 纯函数，导出以便单元测试。
 */
export function findRelatedEntries(entries: TestEntry[], savedFile: string): TestEntry[] {
  const normSaved = savedFile.replace(/\\/g, "/");
  const savedDir = normSaved.slice(0, Math.max(0, normSaved.lastIndexOf("/")));
  return entries.filter((e) => {
    const normFile = e.file.replace(/\\/g, "/");
    const dir = normFile.slice(0, Math.max(0, normFile.lastIndexOf("/")));
    return dir === savedDir && dir !== "";
  });
}

/**
 * Priority-6: 为一次文件保存运行相关测试。
 *
 * - 若存在 Go 相关条目：对该目录跑一次 `runGoTestsJSON`（整包一次运行）。
 * - 对 TS/JS 相关条目：逐个 `runDiscoveredTest`。
 */
export async function runContinuousTestsForFile(absPath: string): Promise<void> {
  if (!testExplorerState.continuousTesting) return;
  const related = findRelatedEntries(testExplorerState.entries, absPath);
  if (related.length === 0) return;
  testExplorerState.continuousRunning = true;
  try {
    const goEntries = related.filter((e) => e.language === "go");
    const jsEntries = related.filter((e) => e.language !== "go");
    if (goEntries.length > 0) {
      // 取第一个 Go 条目所在目录作为包目录。
      const first = goEntries[0].file.replace(/\\/g, "/");
      const pkgDir = first.slice(0, Math.max(0, first.lastIndexOf("/"))) || goEntries[0].file;
      await runGoTestsJSON(pkgDir);
    }
    for (const e of jsEntries) {
      await runDiscoveredTest(e);
    }
  } finally {
    testExplorerState.continuousRunning = false;
  }
}

/**
 * Priority-6: 注册 `file:saved` 监听器（幂等）。
 * 监听器内部依据 `testExplorerState.continuousTesting` 决定是否真正运行。
 *
 * TODO: move to crossWindowSync.ts — file:saved 的 Events.On 注册应集中到跨
 * 窗口同步层，但本监听器与 workflows.initWorkflowTriggers 共享同一事件，
 * 迁移需先解决多消费者分发，暂留原处。
 */
export function initContinuousTesting(): void {
  if (continuousListenerRegistered) return;
  continuousListenerRegistered = true;
  const cancel = Events.On("file:saved", (event: FileSavedEvent) => {
    const absPath: string = event?.data ?? "";
    if (typeof absPath !== "string" || absPath === "") return;
    void runContinuousTestsForFile(absPath);
  });
  if (typeof cancel === "function") continuousCanceller.push(cancel);
}

export function initTestExplorerRuntime(): void {
  if (testExplorerState.continuousTesting) initContinuousTesting();
}

/** Priority-6: 开启/关闭连续测试。开启时确保监听器已注册。 */
export function setContinuousTesting(enabled: boolean): void {
  testExplorerState.continuousTesting = enabled;
  if (enabled) initContinuousTesting();
}

/** Priority-6: 切换连续测试开关。 */
export function toggleContinuousTesting(): void {
  setContinuousTesting(!testExplorerState.continuousTesting);
}

/** Priority-6: 注销监听器（供 HMR / 测试清理）。 */
export function teardownContinuousTesting(): void {
  for (const cancel of continuousCanceller) {
    try {
      cancel();
    } catch {
      /* ignore */
    }
  }
  continuousCanceller.length = 0;
  continuousListenerRegistered = false;
}

import.meta.hot?.dispose(teardownContinuousTesting);
