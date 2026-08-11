import { flushPromises, mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  refreshDebugStatus: vi.fn().mockResolvedValue(undefined),
  addWatch: vi.fn().mockResolvedValue(undefined),
  removeWatch: vi.fn().mockResolvedValue(undefined),
  evaluateExpression: vi.fn().mockResolvedValue(undefined),
  setBreakpointCondition: vi.fn().mockResolvedValue(undefined),
  evaluateWatch: vi.fn().mockResolvedValue(""),
  evaluateRepl: vi.fn().mockResolvedValue(""),
  setConditionalBreakpoint: vi.fn().mockResolvedValue(undefined),
  openFileFromPath: vi.fn().mockResolvedValue(undefined),
  requestEditorJump: vi.fn(),
  // GOAL-P1-03: the step-in target path. Declared here (rather than inline in
  // the store mock) so individual tests can control enumeration results and
  // assert that the selected target ID actually reaches the store.
  fetchStepInTargetsForStop: vi.fn().mockResolvedValue({
    targets: [],
    stopSequence: 0,
    supported: false,
  }),
  debugStepInTarget: vi.fn().mockResolvedValue(true),
  // Hoisted so the default/fallback step-in is assertable. The store factory
  // below used to create this inline, which produced a fresh mock per call and
  // left the fallback path unobservable.
  debugStepIn: vi.fn().mockResolvedValue(undefined),
  appState: { currentProject: "/repo" },
  layoutState: { tree: { activeLeafId: "editor-debug" } },
}));

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({ t: (key: string) => key }),
}));

vi.mock("@/stores/app", () => ({
  appState: mocks.appState,
  requestEditorJump: mocks.requestEditorJump,
}));

vi.mock("@/stores/editor", () => ({
  openFileFromPath: mocks.openFileFromPath,
}));

vi.mock("@/stores/layout", () => ({ layoutState: mocks.layoutState }));

vi.mock("@/lib/notifications", () => ({
  notifySuccess: vi.fn(),
}));

vi.mock("./DebugCallStack.vue", () => ({
  default: {
    emits: ["select-frame", "restart-frame"],
    template: '<button type="button" data-test="debug-call-stack" @click="$emit(\'select-frame\', \'/repo/main.go\', 12, 7)" />',
  },
}));

vi.mock("../../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/debugservice.js", () => ({
  EvaluateWatch: mocks.evaluateWatch,
  EvaluateREPL: mocks.evaluateRepl,
  SetConditionalBreakpoint: mocks.setConditionalBreakpoint,
}));

vi.mock("@/stores/debug", async () => {
  const { reactive } = await import("vue");
  const resolved = () => vi.fn().mockResolvedValue(undefined);
  const debugState = reactive({
    available: true,
    busy: false,
    running: true,
    stopped: true,
    message: "ready",
    stopReason: "breakpoint",
    mode: "go",
    sessions: [] as Array<Record<string, unknown>>,
    activeSessionID: "",
    lastError: "",
    exceptionInfo: null,
    attachAddr: "",
    launchConfigs: [] as Array<Record<string, unknown>>,
    activeConfigName: "",
    browserTargetId: "",
    browserTargets: [] as Array<Record<string, unknown>>,
    browserConsole: [] as Array<Record<string, unknown>>,
    browserNetwork: [] as Array<Record<string, unknown>>,
    stack: [] as Array<Record<string, unknown>>,
    inlineValues: [] as Array<Record<string, unknown>>,
    locals: [] as Array<Record<string, unknown>>,
    editingVarName: "",
    editingVarRef: 0,
    editingVarValue: "",
    watchInput: "",
    watches: [] as Array<{ name: string; value: string; type?: string }>,
    evaluateInput: "",
    evaluateResult: "",
    completionItems: [] as Array<{ label: string; type?: string }>,
    breakpoints: [] as Array<{ file: string; line: number; condition?: string; verified?: boolean; message?: string }>,
    functionBreakpoints: [] as Array<Record<string, unknown>>,
    newFuncBpName: "",
    newFuncBpCondition: "",
    newFuncBpHitCondition: "",
    dataBreakpoints: [] as Array<Record<string, unknown>>,
    supportsDelayedStackTraceLoading: false,
    stackHasMore: false,
    stackPageLoading: false,
    supportsAsyncStackTrace: false,
    asyncStackSegments: [] as Array<Record<string, unknown>>,
    asyncStackRootId: "",
    asyncStackLoading: false,
  });

  return {
    debugState,
    refreshDebugStatus: mocks.refreshDebugStatus,
    addWatch: mocks.addWatch,
    removeWatch: mocks.removeWatch,
    evaluateExpression: mocks.evaluateExpression,
    setBreakpointCondition: mocks.setBreakpointCondition,
    launchDebugPackage: resolved(),
    stopDebugSession: resolved(),
    restartDebugSession: resolved(),
    debugContinue: resolved(),
    debugStepOver: resolved(),
    debugStepIn: mocks.debugStepIn,
    debugStepOut: resolved(),
    selectDebugFrame: resolved(),
    refreshStackAndLocals: resolved(),
    launchWithConfig: resolved(),
    loadLaunchConfigs: resolved(),
    probeAndAttachDelve: resolved(),
    exportLaunchConfigsJSON: () => "{}",
    importLaunchConfigsJSON: () => 0,
    addFunctionBreakpoint: resolved(),
    removeFunctionBreakpoint: resolved(),
    applyFunctionBreakpoints: resolved(),
    setVariable: resolved(),
    restartFrame: resolved(),
    refreshInlineValues: resolved(),
    startEditVariable: vi.fn(),
    cancelEditVariable: vi.fn(),
    switchSession: resolved(),
    startDebugSession: resolved(),
    stopDebugSessionByID: resolved(),
    selectBrowserTarget: resolved(),
    fetchDataBreakpointInfo: vi.fn().mockResolvedValue([]),
    addDataBreakpoint: resolved(),
    removeDataBreakpoint: resolved(),
    clearDataBreakpoints: resolved(),
    fetchExceptionInfo: resolved(),
    fetchCompletions: resolved(),
    fetchStepInTargets: vi.fn().mockResolvedValue([]),
    // GOAL-P1-03: the stop-aware enumeration + targeted step. Defaults describe
    // an adapter with no step-in targets, which is the path most tests exercise.
    fetchStepInTargetsForStop: mocks.fetchStepInTargetsForStop,
    debugStepInTarget: mocks.debugStepInTarget,
  };
});

import DebugPanel from "./DebugPanel.vue";
import { debugState } from "@/stores/debug";

function mountPanel() {
  return mount(DebugPanel, {
    attachTo: document.body,
  });
}

describe("DebugPanel prompt-4 enhancements", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    debugState.watches = [];
    debugState.watchInput = "";
    debugState.evaluateInput = "";
    debugState.evaluateResult = "";
    debugState.breakpoints = [];
    mocks.layoutState.tree.activeLeafId = "editor-debug";
    mocks.addWatch.mockImplementation(async (expression: string) => {
      debugState.watches = [{ name: expression, value: "pending", type: "string" }];
    });
    mocks.removeWatch.mockImplementation(async (expression: string) => {
      debugState.watches = debugState.watches.filter((item) => item.name !== expression);
    });
  });

  it("uses EvaluateWatch when adding and editing watch expressions", async () => {
    mocks.evaluateWatch.mockResolvedValueOnce("41").mockResolvedValueOnce("42");
    const wrapper = mountPanel();
    await wrapper.get('input[aria-label="debug.watchExpression"]').setValue("count");
    await wrapper.get('button[aria-label="debug.addWatch"]').trigger("click");
    await flushPromises();

    expect(mocks.evaluateWatch).toHaveBeenCalledWith("count");
    expect(debugState.watches[0]?.value).toBe("41");

    await wrapper.get('button[aria-label="debug.editWatch"]').trigger("click");
    const editInput = wrapper.get('.debug-panel__watch-item input[aria-label="debug.watchExpression"]');
    await editInput.setValue("count + 1");
    await editInput.trigger("keydown", { key: "Enter" });
    await flushPromises();

    expect(mocks.removeWatch).toHaveBeenCalledWith("count");
    expect(mocks.addWatch).toHaveBeenCalledWith("count + 1");
    expect(mocks.evaluateWatch).toHaveBeenLastCalledWith("count + 1");
    wrapper.unmount();
  });

  it("uses EvaluateREPL and appends an aria-live console entry", async () => {
    mocks.evaluateRepl.mockResolvedValue("42");
    const wrapper = mountPanel();
    const input = wrapper.get('input[aria-label="debug.replInput"]');
    await input.setValue("6 * 7");
    await input.trigger("keydown", { key: "Enter" });
    await flushPromises();

    expect(mocks.evaluateRepl).toHaveBeenCalledWith("6 * 7");
    const log = wrapper.get('[role="log"]');
    expect(log.attributes("aria-live")).toBe("polite");
    expect(log.text()).toContain("> 6 * 7");
    expect(log.text()).toContain("42");
    expect((input.element as HTMLInputElement).value).toBe("");
    wrapper.unmount();
  });

  it("opens conditional breakpoint editing from the context menu and uses the dedicated binding", async () => {
    debugState.breakpoints = [{ id: 1, file: "/repo/main.go", line: 12, verified: true }];
    const wrapper = mountPanel();
    await wrapper.get(".debug-panel__item--breakpoint").trigger("contextmenu", {
      clientX: 20,
      clientY: 30,
    });
    await wrapper.get('[data-test="edit-breakpoint-condition"]').trigger("click");
    const input = wrapper.get('input[aria-label="debug.conditionPlaceholder"]');
    await input.setValue("count > 3");
    await wrapper.get(".debug-panel__cond-form .debug-panel__btn").trigger("click");
    await flushPromises();

    expect(mocks.setConditionalBreakpoint).toHaveBeenCalledWith("/repo/main.go", 12, "count > 3");
    expect(mocks.refreshDebugStatus).toHaveBeenCalled();
    expect(mocks.setBreakpointCondition).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  it("targets the active editor group when selecting a stack frame", async () => {
    const wrapper = mountPanel();
    await wrapper.get('[data-test="debug-call-stack"]').trigger("click");
    await flushPromises();

    expect(mocks.openFileFromPath).toHaveBeenCalledWith("/repo/main.go");
    expect(mocks.requestEditorJump).toHaveBeenCalledWith(
      "/repo/main.go",
      12,
      1,
      "editor-debug",
    );
    wrapper.unmount();
  });
});

// ---------------------------------------------------------------------------
// GOAL-P1-03 — Step In target selection
//
// Baseline defect: `onPickStepInTarget(_targetId)` accepted the user's chosen
// target and threw it away, calling the plain `debugStepIn()` instead. The menu
// was cosmetic: picking overload B stepped into A. These tests cover AC 4
// (selection / cancel / default paths) and AC 3 (unsupported adapters).
// ---------------------------------------------------------------------------

describe("DebugPanel step-in target selection (GOAL-P1-03)", () => {
  /** Locates the Step In toolbar button by its label. */
  function stepInButton(wrapper: ReturnType<typeof mountPanel>) {
    const button = wrapper
      .findAll("button")
      .find((candidate) => candidate.text().trim() === "Step In");
    if (!button) throw new Error("Step In button not found");
    return button;
  }

  beforeEach(() => {
    vi.clearAllMocks();
    debugState.running = true;
    debugState.stopped = true;
    // A frame is required for enumeration; without one the component takes the
    // no-context default path, which several tests below assert explicitly.
    debugState.stack = [{ id: 7, name: "main.main", file: "/repo/main.go", line: 3, column: 1 }];
    mocks.debugStepInTarget.mockResolvedValue(true);
  });

  it("opens a menu when the adapter offers multiple targets", async () => {
    mocks.fetchStepInTargetsForStop.mockResolvedValue({
      targets: [{ id: 1, label: "main.foo" }, { id: 2, label: "main.bar" }],
      stopSequence: 4,
      supported: true,
    });
    const wrapper = mountPanel();

    await stepInButton(wrapper).trigger("click");
    await flushPromises();

    expect(mocks.fetchStepInTargetsForStop).toHaveBeenCalledWith(7);
    expect(wrapper.find(".debug-panel__menu--stepin").exists()).toBe(true);
    // Showing a choice must not also step: the user has not chosen yet.
    expect(mocks.debugStepIn).not.toHaveBeenCalled();
    expect(mocks.debugStepInTarget).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  it("passes the selected target id through instead of discarding it", async () => {
    mocks.fetchStepInTargetsForStop.mockResolvedValue({
      targets: [{ id: 1, label: "main.foo" }, { id: 2, label: "main.bar" }],
      stopSequence: 4,
      supported: true,
    });
    const wrapper = mountPanel();
    await stepInButton(wrapper).trigger("click");
    await flushPromises();

    // Pick the SECOND entry. Picking the first would pass even under the old
    // broken behaviour, because a default step-in also lands on target 1.
    const items = wrapper.findAll(".debug-panel__menu--stepin .debug-panel__menu-item");
    expect(items).toHaveLength(2);
    await items[1]!.trigger("click");
    await flushPromises();

    expect(mocks.debugStepInTarget).toHaveBeenCalledWith(2);
    // The default step-in must NOT also fire — that was the original defect.
    expect(mocks.debugStepIn).not.toHaveBeenCalled();
    expect(wrapper.find(".debug-panel__menu--stepin").exists()).toBe(false);
    wrapper.unmount();
  });

  it("cancelling the menu steps nowhere", async () => {
    mocks.fetchStepInTargetsForStop.mockResolvedValue({
      targets: [{ id: 1, label: "main.foo" }, { id: 2, label: "main.bar" }],
      stopSequence: 4,
      supported: true,
    });
    const wrapper = mountPanel();
    await stepInButton(wrapper).trigger("click");
    await flushPromises();

    await wrapper.get(".debug-panel__menu--stepin .debug-panel__menu-cancel").trigger("click");
    await flushPromises();

    expect(wrapper.find(".debug-panel__menu--stepin").exists()).toBe(false);
    expect(mocks.debugStepInTarget).not.toHaveBeenCalled();
    expect(mocks.debugStepIn).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  it("uses the default step-in when the adapter reports no targets", async () => {
    mocks.fetchStepInTargetsForStop.mockResolvedValue({
      targets: [],
      stopSequence: 4,
      supported: true,
    });
    const wrapper = mountPanel();

    await stepInButton(wrapper).trigger("click");
    await flushPromises();

    expect(mocks.debugStepIn).toHaveBeenCalledTimes(1);
    expect(mocks.debugStepInTarget).not.toHaveBeenCalled();
    expect(wrapper.find(".debug-panel__menu--stepin").exists()).toBe(false);
    wrapper.unmount();
  });

  it("uses the default step-in for a single target rather than sending its id", async () => {
    mocks.fetchStepInTargetsForStop.mockResolvedValue({
      targets: [{ id: 9, label: "main.only" }],
      stopSequence: 4,
      supported: true,
    });
    const wrapper = mountPanel();

    await stepInButton(wrapper).trigger("click");
    await flushPromises();

    // DAP treats an omitted targetId as "the natural target", which is where the
    // single enumerated entry points. Sending the ID would add a staleness
    // failure mode without changing where execution lands.
    expect(mocks.debugStepIn).toHaveBeenCalledTimes(1);
    expect(mocks.debugStepInTarget).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  it("does not regress adapters without stepInTargets support (AC 3)", async () => {
    mocks.fetchStepInTargetsForStop.mockResolvedValue({
      targets: [],
      stopSequence: 4,
      supported: false,
    });
    const wrapper = mountPanel();

    await stepInButton(wrapper).trigger("click");
    await flushPromises();

    expect(mocks.debugStepIn).toHaveBeenCalledTimes(1);
    expect(wrapper.find(".debug-panel__menu--stepin").exists()).toBe(false);
    wrapper.unmount();
  });

  it("falls back to a default step when the targeted step is refused", async () => {
    mocks.fetchStepInTargetsForStop.mockResolvedValue({
      targets: [{ id: 1, label: "main.foo" }, { id: 2, label: "main.bar" }],
      stopSequence: 4,
      supported: true,
    });
    // A stale menu or unsupported adapter makes the backend refuse.
    mocks.debugStepInTarget.mockResolvedValue(false);
    const wrapper = mountPanel();
    await stepInButton(wrapper).trigger("click");
    await flushPromises();

    const items = wrapper.findAll(".debug-panel__menu--stepin .debug-panel__menu-item");
    await items[1]!.trigger("click");
    await flushPromises();

    // A menu click must never be a silent no-op.
    expect(mocks.debugStepInTarget).toHaveBeenCalledWith(2);
    expect(mocks.debugStepIn).toHaveBeenCalledTimes(1);
    wrapper.unmount();
  });

  it("steps directly when there is no frame to enumerate against", async () => {
    debugState.stack = [];
    const wrapper = mountPanel();

    await stepInButton(wrapper).trigger("click");
    await flushPromises();

    expect(mocks.fetchStepInTargetsForStop).not.toHaveBeenCalled();
    expect(mocks.debugStepIn).toHaveBeenCalledTimes(1);
    wrapper.unmount();
  });
});
