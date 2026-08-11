import { flushPromises, mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";

const runtime = vi.hoisted(() => ({
  on: vi.fn(),
  listeners: new Map<string, (event: unknown) => void>(),
  unsubscribers: [] as Array<ReturnType<typeof vi.fn>>,
}));

vi.mock("@wailsio/runtime", () => ({
  Events: { On: runtime.on },
}));

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, string | number>) => {
      if (!params) return key;
      return `${key}:${Object.values(params).join(":")}`;
    },
  }),
}));

import {
  activateDebugThreads,
  continueAllDebugThreads,
  continueDebugThread,
  debugThreadsState,
  deactivateDebugThreads,
  getDebugThreadStackTrace,
  isDebugThreadsActive,
  listDebugThreads,
  pauseAllDebugThreads,
  resetDebugThreadsStore,
  selectDebugThread,
  setDebugThreadsServiceBindings,
  stepDebugThread,
  toggleDebugThreadExpanded,
  type DebugThreadsServiceBindings,
  useDebugThreadsStore,
} from "./debugThreads";
import ThreadsPanel from "@/components/debug/ThreadsPanel.vue";

const threadFixtures = [
  {
    id: 1,
    name: "main",
    state: "stopped" as const,
    frames: [],
    selected: true,
  },
  {
    id: 2,
    name: "worker",
    state: "running" as const,
    frames: [],
    selected: false,
  },
];

const frameFixtures = [
  {
    id: 101,
    name: "serve",
    source: "C:\\repo\\server.go",
    file: "C:\\repo\\server.go",
    line: 42,
    column: 3,
  },
];

function deferredVoid() {
  let resolve!: () => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<undefined>((resolvePromise, rejectPromise) => {
    resolve = () => resolvePromise(undefined);
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

function deferredValue<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

const service = {
  ListThreads: vi.fn(async () => threadFixtures),
  GetThreadStackTrace: vi.fn(async () => frameFixtures),
  SelectThread: vi.fn(async () => undefined),
  ContinueThread: vi.fn(async () => undefined),
  ContinueAllThreads: vi.fn(async () => undefined),
  PauseAllThreads: vi.fn(async () => undefined),
  StepThread: vi.fn(async () => undefined),
} satisfies DebugThreadsServiceBindings;

function installRuntimeListeners(): void {
  runtime.on.mockImplementation((name: string, listener: (event: unknown) => void) => {
    runtime.listeners.set(name, listener);
    const unsubscribe = vi.fn(() => runtime.listeners.delete(name));
    runtime.unsubscribers.push(unsubscribe);
    return unsubscribe;
  });
}

describe("debugThreads store", () => {
  beforeEach(() => {
    resetDebugThreadsStore();
    vi.clearAllMocks();
    runtime.listeners.clear();
    runtime.unsubscribers.length = 0;
    installRuntimeListeners();
    service.ListThreads.mockResolvedValue(threadFixtures);
    service.GetThreadStackTrace.mockResolvedValue(frameFixtures);
    service.SelectThread.mockResolvedValue(undefined);
    service.ContinueThread.mockResolvedValue(undefined);
    service.ContinueAllThreads.mockResolvedValue(undefined);
    service.PauseAllThreads.mockResolvedValue(undefined);
    service.StepThread.mockResolvedValue(undefined);
    setDebugThreadsServiceBindings(service);
  });

  it("fails closed when no generated debug-thread binding is registered", async () => {
    setDebugThreadsServiceBindings(null);

    await expect(listDebugThreads("session-unbound")).resolves.toEqual([]);
    expect(debugThreadsState.error).toContain("no generated Wails binding is registered");
  });

  it("lists threads and tracks the backend-selected thread", async () => {
    const result = await listDebugThreads("session-1");

    expect(service.ListThreads).toHaveBeenCalledWith("session-1");
    expect(result).toHaveLength(2);
    expect(debugThreadsState.sessionId).toBe("session-1");
    expect(debugThreadsState.selected).toBe(1);
    expect(debugThreadsState.threads[0]).toEqual(expect.objectContaining({
      id: 1,
      state: "stopped",
      selected: true,
    }));
    expect(debugThreadsState.loading).toBe(false);
    expect(debugThreadsState.error).toBeNull();
  });

  it("loads stacks and performs select, continue, and step actions", async () => {
    await listDebugThreads("session-1");
    await toggleDebugThreadExpanded("session-1", 1);

    expect(service.GetThreadStackTrace).toHaveBeenCalledWith("session-1", 1, 0, 0);
    expect(debugThreadsState.expanded.has(1)).toBe(true);
    expect(debugThreadsState.threads[0].frames).toEqual(frameFixtures);

    await getDebugThreadStackTrace("session-1", 1, 2, 4);
    expect(service.GetThreadStackTrace).toHaveBeenLastCalledWith(
      "session-1",
      1,
      2,
      4,
    );

    expect(await selectDebugThread("session-1", 2)).toBe(true);
    expect(service.SelectThread).toHaveBeenCalledWith("session-1", 2);
    expect(debugThreadsState.selected).toBe(2);

    expect(await continueDebugThread("session-1", 1)).toBe(true);
    expect(service.ContinueThread).toHaveBeenCalledWith("session-1", 1);
    expect(debugThreadsState.threads[0].state).toBe("running");
    expect(debugThreadsState.threads[0].frames).toEqual([]);

    expect(await stepDebugThread("session-1", 2, "in")).toBe(true);
    expect(service.StepThread).toHaveBeenCalledWith("session-1", 2, "in");
    expect(debugThreadsState.selected).toBe(2);
    expect(debugThreadsState.threads[1].state).toBe("stepping");
  });

  it("binds blank convenience arguments to the active session", async () => {
    await listDebugThreads("session-1");
    await getDebugThreadStackTrace("", 1);
    await continueDebugThread("", 1);

    expect(service.GetThreadStackTrace).toHaveBeenCalledWith("session-1", 1, 0, 0);
    expect(service.ContinueThread).toHaveBeenCalledWith("session-1", 1);
  });

  it("exposes a stable composable store and runs all-thread controls", async () => {
    const store = useDebugThreadsStore();
    expect(useDebugThreadsStore()).toBe(store);

    await store.loadThreads("session-1");
    expect(store.threads).toHaveLength(2);
    expect(store.selectedThreadId).toBe(1);

    await expect(continueAllDebugThreads("session-1")).resolves.toBe(true);
    expect(service.ContinueAllThreads).toHaveBeenCalledWith("session-1");
    expect(debugThreadsState.threads.every((thread) => thread.state === "running")).toBe(true);
    expect(debugThreadsState.bulkActionLoading).toBe(false);

    await expect(pauseAllDebugThreads("session-1")).resolves.toBe(true);
    expect(service.PauseAllThreads).toHaveBeenCalledWith("session-1");
    expect(debugThreadsState.bulkActionLoading).toBe(false);
  });

  it("does not request an uncached stack for a running thread", async () => {
    await listDebugThreads("session-1");
    await expect(getDebugThreadStackTrace("session-1", 2)).resolves.toEqual([]);
    await toggleDebugThreadExpanded("session-1", 2);

    expect(service.GetThreadStackTrace).not.toHaveBeenCalled();
    expect(debugThreadsState.expanded.has(2)).toBe(false);
  });

  it("merges paginated stack responses at their requested offsets", async () => {
    const firstPage = [
      { ...frameFixtures[0], id: 101 },
      { ...frameFixtures[0], id: 102, name: "dispatch" },
    ];
    const secondPage = [
      { ...frameFixtures[0], id: 103, name: "main" },
    ];
    service.GetThreadStackTrace.mockReset()
      .mockResolvedValueOnce(firstPage)
      .mockResolvedValueOnce(secondPage);
    await listDebugThreads("session-1");

    await getDebugThreadStackTrace("session-1", 1, 0, 2);
    await getDebugThreadStackTrace("session-1", 1, 2, 1);

    expect(debugThreadsState.threads[0].frames.map((frame) => frame.id)).toEqual([
      101,
      102,
      103,
    ]);
    expect(service.GetThreadStackTrace).toHaveBeenNthCalledWith(
      2,
      "session-1",
      1,
      2,
      1,
    );
  });

  it("queues out-of-order stack pages until their offset gap is filled", async () => {
    const laterPage = deferredValue<typeof frameFixtures>();
    const firstPage = deferredValue<typeof frameFixtures>();
    service.GetThreadStackTrace.mockReset()
      .mockReturnValueOnce(laterPage.promise)
      .mockReturnValueOnce(firstPage.promise);
    await listDebugThreads("session-1");

    const laterRequest = getDebugThreadStackTrace("session-1", 1, 2, 1);
    const firstRequest = getDebugThreadStackTrace("session-1", 1, 0, 2);
    laterPage.resolve([
      { ...frameFixtures[0], id: 103, name: "main" },
    ]);
    await laterRequest;
    expect(debugThreadsState.threads[0].frames).toEqual([]);

    firstPage.resolve([
      { ...frameFixtures[0], id: 101 },
      { ...frameFixtures[0], id: 102, name: "dispatch" },
    ]);
    await firstRequest;

    expect(debugThreadsState.threads[0].frames.map((frame) => frame.id)).toEqual([
      101,
      102,
      103,
    ]);
    expect(debugThreadsState.loadingStacks.has(1)).toBe(false);
  });

  it("truncates the old stack tail when a refreshed page is short", async () => {
    const initialFrames = [
      { ...frameFixtures[0], id: 101 },
      { ...frameFixtures[0], id: 102, name: "dispatch" },
      { ...frameFixtures[0], id: 103, name: "worker" },
      { ...frameFixtures[0], id: 104, name: "main" },
    ];
    service.GetThreadStackTrace.mockReset()
      .mockResolvedValueOnce(initialFrames)
      .mockResolvedValueOnce([
        { ...frameFixtures[0], id: 202, name: "refreshed" },
      ]);
    await listDebugThreads("session-1");

    await getDebugThreadStackTrace("session-1", 1, 0, 4);
    await getDebugThreadStackTrace("session-1", 1, 1, 2);

    expect(debugThreadsState.threads[0].frames.map((frame) => frame.id)).toEqual([
      101,
      202,
    ]);
  });

  it("does not revive an older queued page beyond a confirmed stack end", async () => {
    service.GetThreadStackTrace.mockReset()
      .mockResolvedValueOnce([
        { ...frameFixtures[0], id: 103, name: "stale-tail" },
      ])
      .mockResolvedValueOnce([
        { ...frameFixtures[0], id: 101, name: "short-root" },
      ])
      .mockResolvedValueOnce([
        { ...frameFixtures[0], id: 102, name: "fresh-tail" },
      ]);
    await listDebugThreads("session-1");

    await getDebugThreadStackTrace("session-1", 1, 2, 1);
    await getDebugThreadStackTrace("session-1", 1, 0, 2);
    await getDebugThreadStackTrace("session-1", 1, 1, 1);

    expect(debugThreadsState.threads[0].frames.map((frame) => frame.id)).toEqual([
      101,
      102,
    ]);
  });

  it("drops invalid and duplicate frame ids across stack pages", async () => {
    service.GetThreadStackTrace.mockReset()
      .mockResolvedValueOnce([
        { ...frameFixtures[0], id: 101 },
        { ...frameFixtures[0], id: 101, name: "duplicate-in-page" },
        { ...frameFixtures[0], id: 0, name: "invalid" },
        { ...frameFixtures[0], id: 102, name: "dispatch" },
      ])
      .mockResolvedValueOnce([
        { ...frameFixtures[0], id: 102, name: "duplicate-across-pages" },
        { ...frameFixtures[0], id: -1, name: "invalid" },
        { ...frameFixtures[0], id: 103, name: "main" },
      ]);
    await listDebugThreads("session-1");

    await getDebugThreadStackTrace("session-1", 1, 0, 4);
    await getDebugThreadStackTrace("session-1", 1, 2, 3);

    expect(debugThreadsState.threads[0].frames.map((frame) => frame.id)).toEqual([
      101,
      102,
      103,
    ]);
  });

  it("invalidates queued and in-flight stack pages when an event replaces state", async () => {
    const firstPage = deferredValue<typeof frameFixtures>();
    service.GetThreadStackTrace.mockReset()
      .mockResolvedValueOnce([
        { ...frameFixtures[0], id: 103, name: "queued" },
      ])
      .mockReturnValueOnce(firstPage.promise);
    await listDebugThreads("session-1");
    activateDebugThreads();

    await getDebugThreadStackTrace("session-1", 1, 2, 1);
    const inFlightRequest = getDebugThreadStackTrace("session-1", 1, 0, 2);
    runtime.listeners.get("debug:threads-updated")?.({
      data: {
        sessionId: "session-1",
        allThreadsStopped: true,
        threads: [
          {
            ...threadFixtures[0],
            frames: [{ ...frameFixtures[0], id: 900, name: "authoritative" }],
          },
        ],
      },
    });
    expect(debugThreadsState.loadingStacks.has(1)).toBe(false);
    firstPage.resolve([
      { ...frameFixtures[0], id: 101 },
      { ...frameFixtures[0], id: 102, name: "dispatch" },
    ]);
    await inFlightRequest;

    expect(debugThreadsState.threads[0].frames.map((frame) => frame.id)).toEqual([900]);
    expect(debugThreadsState.loadingStacks.has(1)).toBe(false);
  });

  it("invalidates prior frames and expansion when execution state changes", async () => {
    await listDebugThreads("session-1");
    await toggleDebugThreadExpanded("session-1", 1);
    expect(debugThreadsState.expanded.has(1)).toBe(true);
    expect(debugThreadsState.threads[0].frames).toEqual(frameFixtures);
    activateDebugThreads();

    runtime.listeners.get("debug:thread-stopped")?.({
      data: {
        sessionId: "session-1",
        threadId: 1,
        reason: "breakpoint",
        allThreadsStopped: false,
      },
    });
    expect(debugThreadsState.threads[0].frames).toEqual([]);
    expect(debugThreadsState.expanded.has(1)).toBe(false);

    await toggleDebugThreadExpanded("session-1", 1);
    expect(service.GetThreadStackTrace).toHaveBeenCalledTimes(2);
    expect(debugThreadsState.expanded.has(1)).toBe(true);

    runtime.listeners.get("debug:threads-updated")?.({
      data: {
        sessionId: "session-1",
        allThreadsStopped: false,
        threads: [{
          ...threadFixtures[0],
          state: "running",
          frames: frameFixtures,
        }],
      },
    });
    expect(debugThreadsState.threads[0].state).toBe("running");
    expect(debugThreadsState.threads[0].frames).toEqual([]);
    expect(debugThreadsState.expanded.has(1)).toBe(false);
  });

  it("does not reuse queued stack pages after a session change", async () => {
    service.GetThreadStackTrace.mockReset()
      .mockResolvedValueOnce([
        { ...frameFixtures[0], id: 103, name: "old-session-page" },
      ])
      .mockResolvedValueOnce([
        { ...frameFixtures[0], id: 101 },
        { ...frameFixtures[0], id: 102, name: "dispatch" },
      ]);
    await listDebugThreads("session-a");
    await getDebugThreadStackTrace("session-a", 1, 2, 1);

    await listDebugThreads("session-b");
    await listDebugThreads("session-a");
    await getDebugThreadStackTrace("session-a", 1, 0, 2);

    expect(debugThreadsState.threads[0].frames.map((frame) => frame.id)).toEqual([
      101,
      102,
    ]);
  });

  it("clears queued stack pages when the store is reset", async () => {
    service.GetThreadStackTrace.mockReset()
      .mockResolvedValueOnce([
        { ...frameFixtures[0], id: 103, name: "pre-reset-page" },
      ])
      .mockResolvedValueOnce([
        { ...frameFixtures[0], id: 101 },
        { ...frameFixtures[0], id: 102, name: "dispatch" },
      ]);
    await listDebugThreads("session-1");
    await getDebugThreadStackTrace("session-1", 1, 2, 1);

    resetDebugThreadsStore();
    setDebugThreadsServiceBindings(service);
    debugThreadsState.sessionId = "session-1";
    debugThreadsState.threads = threadFixtures.map((thread) => ({
      ...thread,
      frames: [],
    }));
    debugThreadsState.selected = 1;
    await getDebugThreadStackTrace("session-1", 1, 0, 2);

    expect(debugThreadsState.threads[0].frames.map((frame) => frame.id)).toEqual([
      101,
      102,
    ]);
  });

  it("does not let an old session action commit into a new session with the same thread id", async () => {
    const continueA = deferredVoid();
    const stepB = deferredVoid();
    const sessionAThreads = [
      { id: 1, name: "session-a-main", state: "stopped" as const, frames: [], selected: true },
    ];
    const sessionBThreads = [
      { id: 1, name: "session-b-worker", state: "stopped" as const, frames: [], selected: false },
      { id: 2, name: "session-b-main", state: "stopped" as const, frames: [], selected: true },
    ];
    service.ListThreads.mockReset()
      .mockResolvedValueOnce(sessionAThreads)
      .mockResolvedValueOnce(sessionBThreads);
    service.ContinueThread.mockReset().mockReturnValueOnce(continueA.promise);
    service.StepThread.mockReset().mockReturnValueOnce(stepB.promise);

    await listDebugThreads("session-a");
    const oldAction = continueDebugThread("session-a", 1);
    expect(debugThreadsState.actionLoading.has(1)).toBe(true);

    await listDebugThreads("session-b");
    expect(debugThreadsState.selected).toBe(2);
    await expect(selectDebugThread("session-a", 1)).resolves.toBe(false);
    expect(debugThreadsState.sessionId).toBe("session-b");
    expect(debugThreadsState.threads.map((thread) => thread.name)).toEqual([
      "session-b-worker",
      "session-b-main",
    ]);
    expect(service.SelectThread).not.toHaveBeenCalled();
    const currentAction = stepDebugThread("session-b", 1, "next");
    expect(debugThreadsState.actionLoading.has(1)).toBe(true);

    continueA.resolve();
    await expect(oldAction).resolves.toBe(false);
    expect(debugThreadsState.sessionId).toBe("session-b");
    expect(debugThreadsState.threads[0]).toEqual(expect.objectContaining({
      id: 1,
      name: "session-b-worker",
      state: "stopped",
      selected: false,
    }));
    expect(debugThreadsState.selected).toBe(2);
    expect(debugThreadsState.actionLoading.has(1)).toBe(true);
    expect(debugThreadsState.error).toBeNull();

    stepB.resolve();
    await expect(currentAction).resolves.toBe(true);
    expect(debugThreadsState.threads[0].state).toBe("stepping");
    expect(debugThreadsState.selected).toBe(1);
    expect(debugThreadsState.actionLoading.has(1)).toBe(false);
  });

  it("treats an authoritative event delivered before the RPC result as success", async () => {
    const selection = deferredVoid();
    service.SelectThread.mockReset().mockReturnValueOnce(selection.promise);
    await listDebugThreads("session-1");
    activateDebugThreads();

    const action = selectDebugThread("session-1", 2);
    runtime.listeners.get("debug:thread-selected")?.({
      data: { sessionId: "session-1", threadId: 2 },
    });
    selection.resolve();

    await expect(action).resolves.toBe(true);
    expect(debugThreadsState.selected).toBe(2);
    expect(debugThreadsState.error).toBeNull();
    expect(debugThreadsState.actionLoading.has(2)).toBe(false);
  });

  it("records backend errors without leaving loading state behind", async () => {
    service.ListThreads.mockRejectedValueOnce(new Error("adapter offline"));

    await expect(listDebugThreads("session-1")).resolves.toEqual([]);
    expect(debugThreadsState.error).toBe("adapter offline");
    expect(debugThreadsState.loading).toBe(false);

    service.GetThreadStackTrace.mockRejectedValueOnce(new Error("stack unavailable"));
    await expect(getDebugThreadStackTrace("session-1", 1)).resolves.toEqual([]);
    expect(debugThreadsState.error).toBe("stack unavailable");
    expect(debugThreadsState.loadingStacks.has(1)).toBe(false);
  });

  it("activates event subscriptions once, applies matching events, and tears down once", async () => {
    await listDebugThreads("session-1");
    activateDebugThreads();
    activateDebugThreads();

    expect(isDebugThreadsActive()).toBe(true);
    expect(runtime.on).toHaveBeenCalledTimes(3);

    runtime.listeners.get("debug:threads-updated")?.({
      data: {
        sessionId: "session-1",
        allThreadsStopped: true,
        threads: [
          { id: 2, name: "worker", state: "stopped", selected: true, frames: frameFixtures },
        ],
      },
    });
    expect(debugThreadsState.threads).toHaveLength(1);
    expect(debugThreadsState.selected).toBe(2);
    expect(debugThreadsState.allThreadsStopped).toBe(true);

    runtime.listeners.get("debug:thread-selected")?.({
      data: { sessionId: "session-1", threadId: 2 },
    });
    expect(debugThreadsState.selected).toBe(2);

    runtime.listeners.get("debug:thread-stopped")?.({
      data: {
        sessionId: "session-1",
        threadId: 2,
        reason: "breakpoint",
        allThreadsStopped: true,
      },
    });
    expect(debugThreadsState.threads[0].state).toBe("stopped");
    expect(debugThreadsState.allThreadsStopped).toBe(true);

    runtime.listeners.get("debug:threads-updated")?.({
      data: { sessionId: "other-session", threads: [], allThreadsStopped: false },
    });
    expect(debugThreadsState.threads).toHaveLength(1);

    deactivateDebugThreads();
    deactivateDebugThreads();
    expect(isDebugThreadsActive()).toBe(false);
    expect(runtime.unsubscribers).toHaveLength(3);
    expect(runtime.unsubscribers.every((unsubscribe) => unsubscribe.mock.calls.length === 1)).toBe(true);
  });

  it("cleans up partial subscriptions and propagates event registration failures", () => {
    const registrationError = new Error("event registration failed");
    const unsubscribe = vi.fn(() => {
      runtime.listeners.delete("debug:threads-updated");
    });
    runtime.on
      .mockImplementationOnce((name: string, listener: (event: unknown) => void) => {
        runtime.listeners.set(name, listener);
        return unsubscribe;
      })
      .mockImplementationOnce(() => {
        throw registrationError;
      });

    expect(() => activateDebugThreads()).toThrow(registrationError);
    expect(unsubscribe).toHaveBeenCalledTimes(1);
    expect(runtime.listeners.has("debug:threads-updated")).toBe(false);
    expect(isDebugThreadsActive()).toBe(false);
    expect(debugThreadsState.error).toBe("event registration failed");

    activateDebugThreads();
    expect(isDebugThreadsActive()).toBe(true);
  });
});

describe("ThreadsPanel", () => {
  beforeEach(() => {
    resetDebugThreadsStore();
    vi.clearAllMocks();
    runtime.listeners.clear();
    runtime.unsubscribers.length = 0;
    installRuntimeListeners();
    service.ListThreads.mockResolvedValue(threadFixtures);
    service.GetThreadStackTrace.mockResolvedValue(frameFixtures);
    service.SelectThread.mockResolvedValue(undefined);
    service.ContinueThread.mockResolvedValue(undefined);
    service.ContinueAllThreads.mockResolvedValue(undefined);
    service.PauseAllThreads.mockResolvedValue(undefined);
    service.StepThread.mockResolvedValue(undefined);
    setDebugThreadsServiceBindings(service);
  });

  function mountPanel(autoActivate = false) {
    return mount(ThreadsPanel, {
      attachTo: document.body,
      props: { sessionId: "session-1", autoActivate },
      global: {
        stubs: { "el-icon": { template: "<span><slot /></span>" } },
      },
    });
  }

  it("renders threads, expands a stack, switches threads, and supports roving keyboard focus", async () => {
    const wrapper = mountPanel();
    await flushPromises();

    expect(wrapper.findAll(".debug-threads__thread")).toHaveLength(2);
    expect(wrapper.get(".debug-threads__tree").element.tagName).toBe("UL");
    expect(wrapper.find('[role="tree"]').exists()).toBe(false);
    expect(wrapper.find('[role="treeitem"]').exists()).toBe(false);
    expect(wrapper.find('[role="group"]').exists()).toBe(false);
    expect(wrapper.findAll(".debug-threads__thread").every((item) => item.element.tagName === "LI"))
      .toBe(true);
    expect(wrapper.text()).toContain("main");
    expect(wrapper.text()).toContain("worker");
    expect(wrapper.findAll<HTMLButtonElement>(".debug-threads__expand")[1].element.disabled).toBe(true);
    expect(wrapper.findAll(".debug-threads__expand")[1].attributes("aria-label")).toContain(
      "debugThreads.expandThread",
    );

    const selectButtons = wrapper.findAll<HTMLButtonElement>("[data-thread-select]");
    expect(selectButtons[0].attributes("aria-current")).toBe("true");
    expect(selectButtons[1].attributes("aria-current")).toBeUndefined();
    selectButtons[0].element.focus();
    await selectButtons[0].trigger("keydown", { key: "ArrowDown" });
    expect(document.activeElement).toBe(selectButtons[1].element);
    await selectButtons[1].trigger("keydown", { key: "Home" });
    expect(document.activeElement).toBe(selectButtons[0].element);
    await selectButtons[0].trigger("keydown", { key: "End" });
    expect(document.activeElement).toBe(selectButtons[1].element);
    await selectButtons[1].trigger("keydown", { key: "ArrowUp" });
    expect(document.activeElement).toBe(selectButtons[0].element);

    const firstExpand = wrapper.findAll(".debug-threads__expand")[0];
    expect(firstExpand.attributes("aria-expanded")).toBe("false");
    await firstExpand.trigger("click");
    await flushPromises();
    expect(firstExpand.attributes("aria-expanded")).toBe("true");
    expect(service.GetThreadStackTrace).toHaveBeenCalledWith("session-1", 1, 0, 0);
    expect(wrapper.text()).toContain("serve");
    expect(wrapper.get(".debug-threads__frame-list").element.tagName).toBe("UL");
    expect(wrapper.get(".debug-threads__frame-list").attributes("aria-label")).toContain(
      "debugThreads.stackForThread",
    );
    expect(wrapper.find(".debug-threads__frame").attributes("aria-label")).toContain(
      "debugThreads.selectFrame",
    );
    await wrapper.find(".debug-threads__frame").trigger("dblclick");
    expect(wrapper.emitted("goto-frame")?.[0]?.[0]).toEqual(expect.objectContaining({ id: 101 }));

    await selectButtons[1].trigger("click");
    await flushPromises();
    expect(service.SelectThread).toHaveBeenCalledWith("session-1", 2);
    expect(wrapper.emitted("selectThread")?.[0]?.[0]).toEqual(expect.objectContaining({ id: 2 }));
    wrapper.unmount();
  });

  it("does not request or expose stale threads without an active session", async () => {
    await listDebugThreads("session-1");
    service.ListThreads.mockClear();

    const wrapper = mount(ThreadsPanel, {
      props: { sessionId: "", autoActivate: false },
      global: {
        stubs: { "el-icon": { template: "<span><slot /></span>" } },
      },
    });
    await flushPromises();

    expect(service.ListThreads).not.toHaveBeenCalled();
    expect(wrapper.findAll(".debug-threads__thread")).toHaveLength(0);
    expect(wrapper.text()).not.toContain("main");
    expect(wrapper.find('[role="alert"]').exists()).toBe(false);
    expect(
      wrapper.get<HTMLButtonElement>('[data-test="debug-threads-refresh"]').element.disabled,
    ).toBe(true);
    wrapper.unmount();
  });

  it("wires the selected stopped thread controls to continue and step actions", async () => {
    const wrapper = mountPanel();
    await flushPromises();

    await wrapper.get('[data-test="debug-thread-step-over"]').trigger("click");
    await flushPromises();
    expect(service.StepThread).toHaveBeenCalledWith("session-1", 1, "next");

    await listDebugThreads("session-1");
    await wrapper.get('[data-test="debug-thread-continue"]').trigger("click");
    await flushPromises();
    expect(service.ContinueThread).toHaveBeenCalledWith("session-1", 1);
    wrapper.unmount();
  });

  it("wires the toolbar to continue and pause all threads", async () => {
    const wrapper = mountPanel();
    await flushPromises();

    await wrapper.get('[data-test="debug-threads-continue-all"]').trigger("click");
    await flushPromises();
    expect(service.ContinueAllThreads).toHaveBeenCalledWith("session-1");

    await wrapper.get('[data-test="debug-threads-pause-all"]').trigger("click");
    await flushPromises();
    expect(service.PauseAllThreads).toHaveBeenCalledWith("session-1");
    wrapper.unmount();
  });

  it("keeps shared event activation alive until the final panel unmounts", async () => {
    const first = mountPanel(true);
    const second = mountPanel(true);
    await flushPromises();

    expect(runtime.on).toHaveBeenCalledTimes(3);
    first.unmount();
    expect(runtime.unsubscribers.every((unsubscribe) => unsubscribe.mock.calls.length === 0)).toBe(true);

    second.unmount();
    expect(runtime.unsubscribers.every((unsubscribe) => unsubscribe.mock.calls.length === 1)).toBe(true);
  });
});
