import { describe, it, expect, beforeEach, vi } from "vitest";
import { mount } from "@vue/test-utils";
import { nextTick, type App } from "vue";
import ElementPlus from "element-plus";

// 用 vi.hoisted 定义 mock 引用：vi.mock 调用会被提升到文件顶部，
// 早于任何 const 声明，因此普通顶层 const 会落入暂时性死区。
// 这里把所有需要在 mock 工厂中引用、又在测试中断言的变量集中定义。
const {
  // ---- 响应式状态的原始对象（在 mock 工厂中用 reactive 包装）----
  appStateObj,
  terminalStateObj,
  outputStateObj,
  taskStateObj,
  workflowStateObj,
  // ---- @/stores/terminal 的动作 mock ----
  createSessionMock,
  reconnectSessionMock,
  writeToSessionMock,
  killSessionMock,
  resizeSessionMock,
  clearSessionOutputMock,
  // ---- @/stores/app 的动作 mock ----
  toggleTerminalMock,
  // ---- @/stores/output 的动作 mock ----
  clearOutputsMock,
  clearProblemsMock,
  problemCountsMock,
  // ---- @/stores/tasks 的动作 mock ----
  loadTasksMock,
  runTaskMock,
  composeCommandLineMock,
  // ---- @/stores/workflows 的动作 mock ----
  loadWorkflowsMock,
  runWorkflowMock,
  composeStepCommandLineMock,
  // ---- @/stores/editor 的动作 mock ----
  openFileFromPathMock,
  // ---- xterm onData 回调捕获（用于测试输入转发）----
  onDataCallbacks,
  // ---- xterm write 回调捕获（用于测试 M-13 异步竞态）----
  writeCallbacks,
  // ---- xterm write 数据捕获（用于测试 BUG-TERM2 输出回放）----
  writtenData,
  // ---- 会话 id 计数器 ----
  sessionCounter,
  // ---- G16: 记录 xterm 构造选项 ----
  terminalOptionsList,
} = vi.hoisted(() => ({
  // appState：组件读取 terminalVisible / terminalHeight / currentProject /
  // terminalFontSize / theme / bottomPanelView 等字段
  appStateObj: {
    terminalVisible: true,
    terminalHeight: 220,
    currentProject: "/proj",
    defaultShell: "",
    terminalFontSize: 13,
    scrollback: 5000,
    terminalCursorStyle: "block",
    theme: "dark",
    bottomPanelView: "" as string,
  },
  // terminalState：会话字典、顺序列表、当前激活会话
  terminalStateObj: {
    sessions: {} as Record<
      string,
      { id: string; output: string; running: boolean; cols: number; rows: number }
    >,
    sessionOrder: [] as string[],
    activeSessionId: null as string | null,
    // M-24: 浅 watch 通知版本号（镜像真实 store 字段）
    sessionsVersion: 0,
  },
  // outputState：输出条目与问题条目
  outputStateObj: {
    outputs: [] as unknown[],
    problems: [] as unknown[],
  },
  // taskState：任务列表与加载状态
  taskStateObj: {
    tasks: [] as unknown[],
    loading: false,
    errorMessage: null as string | null,
  },
  // workflowState：工作流列表与执行状态
  workflowStateObj: {
    workflows: [] as unknown[],
    loading: false,
    errorMessage: null as string | null,
    running: {} as Record<string, boolean>,
    stepStates: {} as Record<string, unknown[]>,
  },

  // ---- 动作 mock：默认返回已解决的 Promise，避免 onMounted 链路报错 ----
  createSessionMock: vi.fn(),
  reconnectSessionMock: vi.fn(),
  writeToSessionMock: vi.fn().mockResolvedValue(undefined),
  killSessionMock: vi.fn(),
  resizeSessionMock: vi.fn().mockResolvedValue(undefined),
  clearSessionOutputMock: vi.fn(),
  toggleTerminalMock: vi.fn(),
  clearOutputsMock: vi.fn(),
  clearProblemsMock: vi.fn(),
  problemCountsMock: vi.fn().mockReturnValue({
    error: 0,
    warning: 0,
    info: 0,
    hint: 0,
  }),
  loadTasksMock: vi.fn().mockResolvedValue(undefined),
  runTaskMock: vi.fn().mockResolvedValue(undefined),
  composeCommandLineMock: vi.fn().mockImplementation((task: { command?: string }) =>
    task?.command ?? "echo hi",
  ),
  loadWorkflowsMock: vi.fn().mockResolvedValue(undefined),
  runWorkflowMock: vi.fn().mockResolvedValue(undefined),
  composeStepCommandLineMock: vi.fn().mockImplementation((step: { command?: string }) =>
    step?.command ?? "echo step",
  ),
  openFileFromPathMock: vi.fn().mockResolvedValue(undefined),

  // xterm onData 回调列表：每次 term.onData(cb) 被调用时收集 cb，
  // 测试可通过 onDataCallbacks[0]("ls\n") 模拟用户输入。
  onDataCallbacks: [] as Array<(data: string) => void>,
  // M-13: xterm write 回调列表：每次 term.write(data, cb) 被调用时收集 cb，
  // 测试可通过 writeCallbacks[i]() 手动触发异步写入完成。
  writeCallbacks: [] as Array<() => void>,
  // BUG-TERM2: xterm write 的原始数据列表，用于断言初始化时回放了缓存输出。
  writtenData: [] as string[],
  // 会话 id 自增计数器
  sessionCounter: { value: 0 },
  // G16: 每次 new Terminal(opts) 记录 opts 供断言。
  terminalOptionsList: [] as Record<string, unknown>[],
}));

// --- 双保险 mock：同时 mock store 与 service，确保任何代码路径都不会触达真实实现 ---

vi.mock("@/stores/app", async () => {
  const { reactive } = await import("vue");
  const appState = reactive(appStateObj);
  // toggleTerminal 翻转 terminalVisible，驱动 v-if="isVisible" 响应式更新
  toggleTerminalMock.mockImplementation(() => {
    appState.terminalVisible = !appState.terminalVisible;
  });
  return {
    appState,
    toggleTerminal: toggleTerminalMock,
  };
});

vi.mock("@/stores/terminal", async () => {
  const { reactive } = await import("vue");
  const terminalState = reactive(terminalStateObj);
  // createSession 的默认实现：生成新会话并更新响应式状态，
  // 模拟真实 store 中 createSession 对 terminalState 的副作用。
  createSessionMock.mockImplementation(async (_workingDir: string, _shell: string) => {
    const id = `term-${++sessionCounter.value}`;
    terminalState.sessions[id] = {
      id,
      output: "",
      running: true,
      cols: 80,
      rows: 24,
    };
    terminalState.sessionOrder.push(id);
    terminalState.activeSessionId = id;
    // M-24: 镜像真实 store，自增 sessionsVersion 通知浅 watch。
    terminalState.sessionsVersion++;
    return id;
  });
  // killSession 的默认实现：删除会话并修正激活会话
  killSessionMock.mockImplementation(async (sessionId: string) => {
    delete terminalState.sessions[sessionId];
    terminalState.sessionOrder = terminalState.sessionOrder.filter(
      (id) => id !== sessionId,
    );
    if (terminalState.activeSessionId === sessionId) {
      terminalState.activeSessionId = terminalState.sessionOrder[0] ?? null;
    }
    // M-24: 镜像真实 store，自增 sessionsVersion 通知浅 watch。
    terminalState.sessionsVersion++;
  });
  // M-24: clearSessionOutput 镜像真实 store 实现（清空 output），
  // 使 M-13 竞态测试中对 output="" 的断言成立。
  clearSessionOutputMock.mockImplementation((sessionId: string) => {
    const session = terminalState.sessions[sessionId];
    if (session) session.output = "";
  });
  return {
    terminalState,
    createSession: createSessionMock,
    reconnectSession: reconnectSessionMock,
    writeToSession: writeToSessionMock,
    killSession: killSessionMock,
    resizeSession: resizeSessionMock,
    clearSessionOutput: clearSessionOutputMock,
  };
});

vi.mock("@/stores/output", async () => {
  const { reactive } = await import("vue");
  const outputState = reactive(outputStateObj);
  return {
    outputState,
    clearOutputs: clearOutputsMock,
    clearProblems: clearProblemsMock,
    problemCounts: problemCountsMock,
  };
});

vi.mock("@/stores/tasks", async () => {
  const { reactive, computed } = await import("vue");
  const taskState = reactive(taskStateObj);
  // hasTasks 必须是真正的 computed ref，模板中 v-if="hasTasks" 才会自动解包
  const hasTasks = computed(() => taskState.tasks.length > 0);
  return {
    taskState,
    loadTasks: loadTasksMock,
    runTask: runTaskMock,
    composeCommandLine: composeCommandLineMock,
    hasTasks,
  };
});

vi.mock("@/stores/workflows", async () => {
  const { reactive, computed } = await import("vue");
  const workflowState = reactive(workflowStateObj);
  // hasWorkflows 必须是真正的 computed ref
  const hasWorkflows = computed(() => workflowState.workflows.length > 0);
  return {
    workflowState,
    loadWorkflows: loadWorkflowsMock,
    runWorkflow: runWorkflowMock,
    composeStepCommandLine: composeStepCommandLineMock,
    hasWorkflows,
  };
});

vi.mock("@/stores/editor", () => ({
  openFileFromPath: openFileFromPathMock,
}));

// mock @/api/services：提供组件可能经 store 间接触达的全部 service 方法
vi.mock("@/api/services", () => ({
  terminalService: {
    start: vi.fn().mockResolvedValue(undefined),
    write: vi.fn().mockResolvedValue(undefined),
    kill: vi.fn().mockResolvedValue(undefined),
    isRunning: vi.fn().mockReturnValue(false),
    resize: vi.fn().mockResolvedValue(undefined),
    startSession: vi.fn().mockResolvedValue(undefined),
    killSession: vi.fn().mockResolvedValue(undefined),
    writeSession: vi.fn().mockResolvedValue(undefined),
    resizeSession: vi.fn().mockResolvedValue(undefined),
    isSessionRunning: vi.fn().mockReturnValue(false),
    listSessions: vi.fn().mockReturnValue([]),
  },
  taskService: {
    loadTasks: vi.fn().mockResolvedValue([]),
  },
  workflowService: {
    loadWorkflows: vi.fn().mockResolvedValue([]),
    validateAllWorkflows: vi.fn().mockResolvedValue([]),
  },
  fileService: {
    readFile: vi.fn().mockResolvedValue(""),
  },
  settingsService: {},
  windowService: {},
}));

// mock i18n：提供 t 函数，覆盖模板中用到的 key
vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => {
      const map: Record<string, string> = {
        "terminal.terminalTab": "Terminal",
        "terminal.outputTab": "Output",
        "terminal.problemsTab": "Problems",
        "terminal.tasksTab": "Tasks",
        "terminal.workflowsTab": "Workflows",
        "terminal.noOutput": "No output",
        "terminal.noProblems": "No problems",
        "terminal.loadingTasks": "Loading tasks...",
        "terminal.loadingWorkflows": "Loading workflows...",
      };
      if (key === "terminal.terminalLabel") {
        return `Terminal ${params?.n ?? 1}`;
      }
      return map[key] ?? key;
    },
    locale: { value: "en" },
  }),
}));

// mock @xterm/xterm：jsdom 中无法运行真实终端，提供 Terminal 类的桩实现。
// onData 回调被收集到 onDataCallbacks，测试可借此模拟用户输入。
vi.mock("@xterm/xterm", () => ({
  Terminal: class MockTerminal {
    options: Record<string, unknown> = {};
    constructor(opts?: unknown) {
      terminalOptionsList.push((opts ?? {}) as Record<string, unknown>);
    }
    loadAddon(_addon: unknown) {}
    open(_el: HTMLElement) {}
    onData(cb: (data: string) => void) {
      onDataCallbacks.push(cb);
    }
    onResize(_cb: unknown) {}
    focus() {}
    // M-13: 捕获 write 回调，测试可手动触发以模拟异步写入完成。
    // BUG-TERM2: 同时记录原始数据，用于断言初始化时回放缓存输出。
    write(_data: string, cb?: () => void) {
      writtenData.push(_data);
      if (cb) writeCallbacks.push(cb);
    }
    dispose() {}
  },
}));

// mock @xterm/addon-fit：提供 FitAddon 类的桩实现
vi.mock("@xterm/addon-fit", () => ({
  FitAddon: class MockFitAddon {
    fit() {}
  },
}));

// ElementPlus 图标在测试中无需渲染，安装一个空插件即可。
const iconPlugin = {
  install(_app: App) {},
};

// 在所有 mock 设置完成后再动态导入被测组件
const TerminalPanelModule = await import("./TerminalPanel.vue");
const TerminalPanel = TerminalPanelModule.default;

// 导入响应式状态（已是被 mock 的响应式代理），用于在测试中读写
const { appState } = await import("@/stores/app");
const { terminalState } = await import("@/stores/terminal");
const { outputState } = await import("@/stores/output");
const { taskState } = await import("@/stores/tasks");
const { workflowState } = await import("@/stores/workflows");

const flushPromises = () => new Promise((resolve) => setTimeout(resolve, 0));

function mountTerminalPanel() {
  return mount(TerminalPanel, {
    global: {
      stubs: {
        DebugPanel: { template: "<div class=\"debug-panel-stub\" />" },
      },
      plugins: [ElementPlus, iconPlugin],
    },
  });
}

// 重置响应式状态并重新建立默认 mock 实现，
// 确保每个用例互不影响（即使前一个用例覆盖了实现）。
function resetStateAndMocks() {
  // ---- 重置 appState ----
  appState.terminalVisible = true;
  appState.terminalHeight = 220;
  appState.currentProject = "/proj";
  appState.defaultShell = "";
  appState.terminalFontSize = 13;
  appState.scrollback = 5000;
  appState.terminalCursorStyle = "block";
  appState.theme = "dark";
  appState.bottomPanelView = "";

  // ---- 重置 terminalState ----
  Object.keys(terminalState.sessions).forEach((id) => delete terminalState.sessions[id]);
  terminalState.sessionOrder = [];
  terminalState.activeSessionId = null;
  // M-24: 重置浅 watch 版本号
  terminalState.sessionsVersion = 0;

  // ---- 重置 outputState ----
  outputState.outputs = [];
  outputState.problems = [];

  // ---- 重置 taskState / workflowState ----
  taskState.tasks = [];
  taskState.loading = false;
  taskState.errorMessage = null;
  workflowState.workflows = [];
  workflowState.loading = false;
  workflowState.errorMessage = null;
  workflowState.running = {};
  workflowState.stepStates = {};

  // ---- 重置 xterm 回调与计数器 ----
  onDataCallbacks.length = 0;
  writeCallbacks.length = 0;
  writtenData.length = 0;
  sessionCounter.value = 0;
  terminalOptionsList.length = 0;

  // ---- 清除调用记录并重新建立默认实现 ----
  vi.clearAllMocks();

  // createSession / killSession / toggleTerminal 需要操作响应式状态，
  // 在此重新绑定以防止被个别用例覆盖后影响后续用例。
  createSessionMock.mockImplementation(async (_workingDir: string, _shell: string) => {
    const id = `term-${++sessionCounter.value}`;
    terminalState.sessions[id] = {
      id,
      output: "",
      running: true,
      cols: 80,
      rows: 24,
    };
    terminalState.sessionOrder.push(id);
    terminalState.activeSessionId = id;
    // M-24: 镜像真实 store，自增 sessionsVersion 通知浅 watch。
    terminalState.sessionsVersion++;
    return id;
  });
  reconnectSessionMock.mockImplementation(async (sessionId: string) => {
    const session = terminalState.sessions[sessionId];
    if (!session || session.running) return false;
    session.running = true;
    return true;
  });
  killSessionMock.mockImplementation(async (sessionId: string) => {
    delete terminalState.sessions[sessionId];
    terminalState.sessionOrder = terminalState.sessionOrder.filter(
      (id) => id !== sessionId,
    );
    if (terminalState.activeSessionId === sessionId) {
      terminalState.activeSessionId = terminalState.sessionOrder[0] ?? null;
    }
    // M-24: 镜像真实 store，自增 sessionsVersion 通知浅 watch。
    terminalState.sessionsVersion++;
  });
  toggleTerminalMock.mockImplementation(() => {
    appState.terminalVisible = !appState.terminalVisible;
  });
  // M-13: clearSessionOutput 恢复默认实现（清空指定会话的 output）。
  clearSessionOutputMock.mockImplementation((sessionId: string) => {
    if (terminalState.sessions[sessionId]) {
      terminalState.sessions[sessionId].output = "";
    }
  });

  // 其余动作恢复默认返回值
  writeToSessionMock.mockResolvedValue(undefined);
  resizeSessionMock.mockResolvedValue(undefined);
  loadTasksMock.mockResolvedValue(undefined);
  loadWorkflowsMock.mockResolvedValue(undefined);
  runTaskMock.mockResolvedValue(undefined);
  runWorkflowMock.mockResolvedValue(undefined);
  openFileFromPathMock.mockResolvedValue(undefined);
  problemCountsMock.mockReturnValue({ error: 0, warning: 0, info: 0, hint: 0 });
  composeCommandLineMock.mockImplementation((task: { command?: string }) =>
    task?.command ?? "echo hi",
  );
  composeStepCommandLineMock.mockImplementation((step: { command?: string }) =>
    step?.command ?? "echo step",
  );
}

describe("TerminalPanel", () => {
  beforeEach(() => {
    resetStateAndMocks();
  });

  it("可见时渲染终端面板及视图标签", async () => {
    const wrapper = mountTerminalPanel();
    await flushPromises();

    // 面板根节点存在
    expect(wrapper.find(".terminal-panel").exists()).toBe(true);
    // 7 个视图标签：terminal / output / problems / debug / profile / tasks / workflows
    const viewTabs = wrapper.findAll(".terminal-panel__view-tab");
    expect(viewTabs.length).toBe(7);
    // 默认激活 terminal 视图
    expect(wrapper.text()).toContain("Terminal");
  });

  it("不可见时不渲染面板", async () => {
    appState.terminalVisible = false;
    const wrapper = mountTerminalPanel();
    await flushPromises();

    // v-if="isVisible" 为 false 时面板不渲染
    expect(wrapper.find(".terminal-panel").exists()).toBe(false);
    // 且不会创建终端会话
    expect(createSessionMock).not.toHaveBeenCalled();
  });

  it("挂载时创建首个终端会话", async () => {
    const wrapper = mountTerminalPanel();
    await flushPromises();

    // onMounted 中 initFirstSession 调用 createSession(currentProject, "")
    expect(createSessionMock).toHaveBeenCalledWith("/proj", "");
    expect(terminalState.sessionOrder.length).toBe(1);
    // 终端会话标签栏显示 1 个会话标签
    const tabs = wrapper.findAll(".terminal-panel__tab");
    expect(tabs.length).toBe(1);
  });

  it("BUG-TERM2: 初始化 xterm 时回放已缓存的输出（提示符不丢失）", async () => {
    // 模拟真实场景：PTY 启动后、xterm 初始化前，shell 提示符已到达 store。
    const id = "term-buffered";
    terminalState.sessions[id] = {
      id,
      output: "PS C:\\> ",
      running: true,
      cols: 80,
      rows: 24,
    };
    terminalState.sessionOrder = [id];
    terminalState.activeSessionId = id;

    const wrapper = mountTerminalPanel();
    await flushPromises();
    await nextTick();

    // 缓存的输出必须被写入 xterm（修复前为空写，终端显示空白）。
    expect(writtenData).toContain("PS C:\\> ");
    // 写入后缓存被清空，避免后续 watcher 重复写入。
    expect(terminalState.sessions[id].output).toBe("");
    wrapper.unmount();
  });

  it("点击新建终端按钮创建新会话", async () => {
    const wrapper = mountTerminalPanel();
    await flushPromises();
    // 挂载后已有 1 个会话
    expect(terminalState.sessionOrder.length).toBe(1);
    vi.clearAllMocks();

    // 点击 "+" 新建按钮
    const newBtn = wrapper.find(".terminal-panel__new");
    await newBtn.trigger("click");
    await flushPromises();

    // createSession 被调用，会话数增至 2
    expect(createSessionMock).toHaveBeenCalledWith("/proj", "");
    expect(terminalState.sessionOrder.length).toBe(2);
    // 新标签出现
    expect(wrapper.findAll(".terminal-panel__tab").length).toBe(2);
  });

  it("uses the persisted default shell for new sessions", async () => {
    appState.defaultShell = "cmd";
    const wrapper = mountTerminalPanel();
    await flushPromises();

    expect(createSessionMock).toHaveBeenCalledWith("/proj", "cmd");
    wrapper.unmount();
  });

  it("点击会话标签切换 activeSessionId", async () => {
    const wrapper = mountTerminalPanel();
    await flushPromises();
    // 挂载后只有 1 个会话，手动追加第二个会话用于切换
    const id2 = "term-manual";
    terminalState.sessions[id2] = {
      id: id2,
      output: "",
      running: true,
      cols: 80,
      rows: 24,
    };
    terminalState.sessionOrder.push(id2);
    await flushPromises();

    const firstId = terminalState.sessionOrder[0];
    expect(terminalState.activeSessionId).toBe(firstId);

    // 点击第二个会话标签
    const tabs = wrapper.findAll(".terminal-panel__tab-select");
    expect(tabs.length).toBe(2);
    await tabs[1].trigger("click");
    await flushPromises();

    // activeSessionId 切换到第二个
    expect(terminalState.activeSessionId).toBe(id2);
  });

  it("点击会话关闭按钮调用 killSession 并移除会话", async () => {
    const wrapper = mountTerminalPanel();
    await flushPromises();
    expect(terminalState.sessionOrder.length).toBe(1);
    const sessionId = terminalState.sessionOrder[0];
    vi.clearAllMocks();

    // 点击会话标签上的关闭图标
    const closeIcon = wrapper.find(".terminal-panel__tab-close");
    await closeIcon.trigger("click");
    await flushPromises();

    // killSession 被调用，会话被移除
    expect(killSessionMock).toHaveBeenCalledWith(sessionId);
    expect(terminalState.sessionOrder.length).toBe(0);
    expect(terminalState.activeSessionId).toBeNull();
  });

  it("G16: exited session exposes a reconnect action", async () => {
    const wrapper = mountTerminalPanel();
    await flushPromises();
    const sessionId = terminalState.sessionOrder[0];
    terminalState.sessions[sessionId].running = false;
    await nextTick();

    const reconnect = wrapper.find(".terminal-panel__tab-reconnect");
    expect(reconnect.exists()).toBe(true);
    await reconnect.trigger("click");
    expect(reconnectSessionMock).toHaveBeenCalledWith(sessionId);
  });

  it("keeps the close action outside the terminal tab", async () => {
    const wrapper = mountTerminalPanel();
    await flushPromises();
    const tab = wrapper.get('[role="tab"]');
    const close = wrapper.get(".terminal-panel__tab-close");
    expect(tab.element.contains(close.element)).toBe(false);
  });

  it("终端 onData 回调将输入转发到 writeToSession", async () => {
    mountTerminalPanel();
    await flushPromises();

    // onMounted 初始化首个终端后应已注册 onData 回调
    expect(onDataCallbacks.length).toBeGreaterThan(0);
    const sessionId = terminalState.activeSessionId!;
    vi.clearAllMocks();

    // 模拟用户在终端输入 "ls\n"
    onDataCallbacks[0]("ls\n");
    await flushPromises();

    // 输入应转发到 writeToSession(sessionId, data)
    expect(writeToSessionMock).toHaveBeenCalledWith(sessionId, "ls\n");
  });

  it("点击关闭面板按钮触发 toggleTerminal 隐藏面板", async () => {
    const wrapper = mountTerminalPanel();
    await flushPromises();
    expect(appState.terminalVisible).toBe(true);

    const closeBtn = wrapper.find(".terminal-panel__close");
    await closeBtn.trigger("click");

    // toggleTerminal 被调用，terminalVisible 翻转为 false
    expect(toggleTerminalMock).toHaveBeenCalled();
    expect(appState.terminalVisible).toBe(false);
  });

  it("切换到 tasks 视图触发 loadTasks", async () => {
    const wrapper = mountTerminalPanel();
    await flushPromises();
    // onMounted 已调用过 loadTasks，清除后精确断言切换行为
    vi.clearAllMocks();
    loadTasksMock.mockResolvedValue(undefined);

    const viewTabs = wrapper.findAll(".terminal-panel__view-tab");
    const tasksTab = viewTabs.find((t) => t.text().includes("Tasks"));
    expect(tasksTab).toBeTruthy();
    await tasksTab!.trigger("click");
    await flushPromises();

    // 切换到 tasks 视图时 watch(activeView) 调用 loadTasks(currentProject)
    expect(loadTasksMock).toHaveBeenCalledWith("/proj");
  });

  it("切换到 workflows 视图触发 loadWorkflows", async () => {
    const wrapper = mountTerminalPanel();
    await flushPromises();
    vi.clearAllMocks();
    loadWorkflowsMock.mockResolvedValue(undefined);

    const viewTabs = wrapper.findAll(".terminal-panel__view-tab");
    const workflowsTab = viewTabs.find((t) => t.text().includes("Workflows"));
    expect(workflowsTab).toBeTruthy();
    await workflowsTab!.trigger("click");
    await flushPromises();

    expect(loadWorkflowsMock).toHaveBeenCalledWith("/proj");
  });

  it("output 视图无输出时显示空状态", async () => {
    const wrapper = mountTerminalPanel();
    await flushPromises();

    const viewTabs = wrapper.findAll(".terminal-panel__view-tab");
    const outputTab = viewTabs.find((t) => t.text().includes("Output"));
    await outputTab!.trigger("click");
    await nextTick();

    // 无输出时渲染空状态节点
    expect(wrapper.find(".terminal-panel__empty").exists()).toBe(true);
    expect(wrapper.text()).toContain("No output");
  });

  it("卸载组件时清理 xterm 实例（不抛错）", async () => {
    const wrapper = mountTerminalPanel();
    await flushPromises();
    // 卸载应触发 onBeforeUnmount 清理终端实例，不应抛出异常
    expect(() => wrapper.unmount()).not.toThrow();
  });
});

// C-5: TerminalPanel 模块级状态迁移到组件内 ref 后，多实例隔离验证。
// 修复前 `const terminals: Record<...> = {}` 和 `let currentSessionId`
// 是模块级，所有实例共享同一份 terminals。onBeforeUnmount 遍历
// Object.keys(terminals) dispose 所有 xterm，包括其他实例的。
// 修复后 terminals/currentSessionId/fitTimer 都是组件内 ref，实例独立。
describe("C-5: TerminalPanel 多实例状态隔离", () => {
  beforeEach(() => {
    resetStateAndMocks();
  });

  it("两个实例各自创建独立的 xterm（不再共享模块级 terminals）", async () => {
    const wrapperA = mountTerminalPanel();
    await flushPromises();
    // 实例 A 创建 1 个会话，注册 1 个 onData 回调
    expect(onDataCallbacks.length).toBe(1);

    const wrapperB = mountTerminalPanel();
    await flushPromises();
    // 修复前：模块级 terminals 共享 → 实例 B 的 initTerminalForSession
    //          发现 terminals[sessionId] 已存在 → 跳过 → 不创建新 Terminal
    // 修复后：组件内 terminals (ref<Map>) → 实例 B 的 terminals.value 为空
    //          → 创建自己的 Terminal → onDataCallbacks 增至 2
    expect(onDataCallbacks.length).toBe(2);

    wrapperA.unmount();
    wrapperB.unmount();
  });

  it("卸载实例 A 不 dispose 实例 B 的 xterm", async () => {
    const wrapperA = mountTerminalPanel();
    await flushPromises();
    const wrapperB = mountTerminalPanel();
    await flushPromises();
    expect(onDataCallbacks.length).toBe(2);

    // 卸载实例 A：onBeforeUnmount 只遍历实例 A 自己的 terminals.value
    wrapperA.unmount();
    await flushPromises();

    // 实例 B 的 onData 回调（索引 1）仍可正常转发输入到 writeToSession
    // 修复前：模块级 terminals 共享 → 实例 A 卸载时 dispose 了所有 term
    //          → 实例 B 的 term 已被 dispose
    // 修复后：实例 A 只 dispose 自己的 terminals.value → 实例 B 的 term 完好
    vi.clearAllMocks();
    const sessionB = terminalState.activeSessionId!;
    onDataCallbacks[1]("ls\n");
    await flushPromises();
    expect(writeToSessionMock).toHaveBeenCalledWith(sessionB, "ls\n");

    wrapperB.unmount();
  });

  it("挂载新实例不继承已卸载实例的 terminals", async () => {
    const wrapperA = mountTerminalPanel();
    await flushPromises();
    expect(onDataCallbacks.length).toBe(1);
    wrapperA.unmount();
    await flushPromises();

    // 挂载实例 B：terminalState 保留实例 A 创建的会话（store 是共享的），
    // 但实例 B 的 terminals.value 是空 Map，应创建新 Terminal。
    // 修复前：模块级 terminals 在卸载后仍保留旧 entry（term 已 dispose）
    //          → 实例 B 跳过 initTerminalForSession → 复用已 dispose 的 term
    // 修复后：实例 B 有自己的 terminals ref → 创建新 Terminal
    const wrapperB = mountTerminalPanel();
    await flushPromises();
    expect(onDataCallbacks.length).toBe(2);

    wrapperB.unmount();
  });

  it("两个实例各自管理自己的 fitTimer", async () => {
    const wrapperA = mountTerminalPanel();
    await flushPromises();
    const wrapperB = mountTerminalPanel();
    await flushPromises();

    // 卸载实例 A 不影响实例 B（fitTimer 清理不互相干扰）
    // 修复前：fitTimer 是模块级 let → 实例 A 卸载时 clearTimeout(fitTimer)
    //          可能清掉实例 B 的 timer
    // 修复后：fitTimer 是组件内 ref → 各自独立
    expect(() => wrapperA.unmount()).not.toThrow();

    // 实例 B 仍可正常工作
    vi.clearAllMocks();
    const sessionB = terminalState.activeSessionId!;
    onDataCallbacks[1]("echo\n");
    await flushPromises();
    expect(writeToSessionMock).toHaveBeenCalledWith(sessionB, "echo\n");

    wrapperB.unmount();
  });
});

// M-13: 验证 term.write + clearSessionOutput 异步竞态修复。
// 修复前：term.write 是异步的，如果在写入期间发生 clear，写入回调可能
// 将过期数据重新追加到 store，导致 clear 状态丢失或新旧输出混合。
// 修复后：引入版本号机制，写入回调检查版本号，若变化则跳过过期追加。
describe("M-13: term.write + clearSessionOutput 异步竞态修复", () => {
  beforeEach(() => {
    resetStateAndMocks();
  });

  it("写入期间的 clear 使旧写入回调过期，store 保持 clear 后状态", async () => {
    const wrapper = mountTerminalPanel();
    await flushPromises();

    const sessionId = terminalState.activeSessionId!;
    expect(sessionId).toBeTruthy();

    // 第一次输出："stale-data"
    terminalState.sessions[sessionId].output = "stale-data";
    // M-24: 浅 watch 由 sessionsVersion 驱动，需手动自增以触发 watcher。
    terminalState.sessionsVersion++;
    await flushPromises();
    // watcher 触发：快照 "stale-data"，clear store → output=""，bump version=1，
    // term.write("stale-data", cb1)。cb1 被捕获但未执行（模拟异步写入 pending）。
    expect(writeCallbacks.length).toBeGreaterThanOrEqual(1);
    expect(terminalState.sessions[sessionId].output).toBe("");

    const staleCallbackIndex = writeCallbacks.length - 1;

    // 第二次输出："post-clear-data"（模拟 clear 后新输出到达）
    terminalState.sessions[sessionId].output = "post-clear-data";
    // M-24: 浅 watch 由 sessionsVersion 驱动，需手动自增以触发 watcher。
    terminalState.sessionsVersion++;
    await flushPromises();
    // watcher 再次触发：快照 "post-clear-data"，clear store → output=""，
    // bump version=2，term.write("post-clear-data", cb2)。
    expect(terminalState.sessions[sessionId].output).toBe("");

    // 触发第一次（过期）写入回调 cb1。版本号已变化（1→2），回调应跳过，
    // 不将 "stale-data" 重新追加到 store。
    writeCallbacks[staleCallbackIndex]();
    await flushPromises();

    // store 仍为 clear 后的空状态，未被过期的 "stale-data" 污染。
    expect(terminalState.sessions[sessionId].output).toBe("");

    wrapper.unmount();
  });

  it("版本号未变化时写入回调正常完成（无过期）", async () => {
    const wrapper = mountTerminalPanel();
    await flushPromises();

    const sessionId = terminalState.activeSessionId!;
    terminalState.sessions[sessionId].output = "chunk-1";
    // M-24: 浅 watch 由 sessionsVersion 驱动，需手动自增以触发 watcher。
    terminalState.sessionsVersion++;
    await flushPromises();

    expect(writeCallbacks.length).toBeGreaterThanOrEqual(1);
    expect(terminalState.sessions[sessionId].output).toBe("");

    // 触发写入回调：版本号未变化（没有新的 clear），回调正常完成。
    // 不应抛出异常，store 保持空状态。
    expect(() => writeCallbacks[0]()).not.toThrow();
    expect(terminalState.sessions[sessionId].output).toBe("");

    wrapper.unmount();
  });
});

// M-24: 验证深度 watch sessions 改为浅 watch sessionsVersion。
// 修复前：watch(() => terminalState.sessions, ..., { deep: true }) 在每次
//   session.output 追加时都深度遍历整个 sessions 对象，输出量大时开销显著。
// 修复后：watch(() => terminalState.sessionsVersion, ...)（浅 watch），
//   仅在 store 自增 sessionsVersion 时触发（createSession/killSession/
//   terminal:output 事件）。直接修改 session.output 不再触发 watcher。
describe("M-24: 浅 watch sessionsVersion 替代深度 watch sessions", () => {
  beforeEach(() => {
    resetStateAndMocks();
  });

  it("直接修改 session.output 不触发 watch（无 sessionsVersion 自增）", async () => {
    const wrapper = mountTerminalPanel();
    await flushPromises();

    const sessionId = terminalState.activeSessionId!;
    vi.clearAllMocks();
    writeCallbacks.length = 0;

    // 直接修改 output，不 bump sessionsVersion → 浅 watch 不应触发
    terminalState.sessions[sessionId].output = "direct-chunk";
    await flushPromises();

    // clearSessionOutput 未被调用，term.write 未被调用
    expect(clearSessionOutputMock).not.toHaveBeenCalled();
    expect(writeCallbacks.length).toBe(0);

    wrapper.unmount();
  });

  it("bump sessionsVersion 触发 watch 并刷新 xterm", async () => {
    const wrapper = mountTerminalPanel();
    await flushPromises();

    const sessionId = terminalState.activeSessionId!;
    // 先放入待刷新的 output
    terminalState.sessions[sessionId].output = "flush-chunk";
    vi.clearAllMocks();
    writeCallbacks.length = 0;

    // bump sessionsVersion（模拟 store 内 createSession/killSession/output 通知）
    terminalState.sessionsVersion++;
    await flushPromises();

    // watcher 触发：快照 output，clear，term.write
    expect(clearSessionOutputMock).toHaveBeenCalledWith(sessionId);
    expect(writeCallbacks.length).toBe(1);

    wrapper.unmount();
  });

  it("增删会话（createSession/killSession）自增 sessionsVersion 并触发 watch", async () => {
    const wrapper = mountTerminalPanel();
    await flushPromises();
    // 挂载后 createSession 已自增 sessionsVersion 至 1
    expect(terminalState.sessionsVersion).toBe(1);

    const firstId = terminalState.activeSessionId!;
    // 给首个会话放入待刷新 output，清除记录
    terminalState.sessions[firstId].output = "pending";
    vi.clearAllMocks();
    writeCallbacks.length = 0;

    // 新建会话：createSession mock 自增 sessionsVersion → watch 触发，
    // 应刷新首个会话的 pending output
    await createSessionMock("/proj", "");
    await flushPromises();

    expect(clearSessionOutputMock).toHaveBeenCalledWith(firstId);
    expect(writeCallbacks.length).toBeGreaterThanOrEqual(1);

    wrapper.unmount();
  });
});

// ---------------------------------------------------------------------------
// G16: Terminal scrollback / cursor 配置实际生效（设置 UI 与 xterm 实例一致）
// ---------------------------------------------------------------------------

describe("G16 terminal settings reach xterm", () => {
  it("applies the user scrollback setting within the G-PERF-03 cap", async () => {
    appState.scrollback = 2000;
    mountTerminalPanel();
    const opts = terminalOptionsList[terminalOptionsList.length - 1];
    expect(opts.scrollback).toBe(2000);
  });

  it("clamps scrollback to the 5000-line G-PERF-03 cap", async () => {
    appState.scrollback = 999999;
    mountTerminalPanel();
    const opts = terminalOptionsList[terminalOptionsList.length - 1];
    expect(opts.scrollback).toBe(5000);
  });

  it("applies the persisted cursor style", async () => {
    appState.terminalCursorStyle = "bar";
    mountTerminalPanel();
    const opts = terminalOptionsList[terminalOptionsList.length - 1];
    expect(opts.cursorStyle).toBe("bar");
  });

  it("falls back to block for an unknown cursor style", async () => {
    appState.terminalCursorStyle = "not-a-style";
    mountTerminalPanel();
    const opts = terminalOptionsList[terminalOptionsList.length - 1];
    expect(opts.cursorStyle).toBe("block");
  });
});
