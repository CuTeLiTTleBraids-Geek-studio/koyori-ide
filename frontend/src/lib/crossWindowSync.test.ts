/**
 * 架构改进 B (prompt-1.md): crossWindowSync 单元测试。
 *
 * 验证「事件 → store 动作」编排层：
 *   - handleApplyToEditorEvent 正确解析 payload 并分发到 requestApplyToEditor
 *   - initCrossWindowSync 注册 ai:apply-to-editor 并编排 app 三个 init 函数
 *   - 幂等：重复 init 先 teardown 旧监听器再重注册（不泄漏）
 *   - teardownCrossWindowSync 注销监听器并调用 unregisterAppListeners
 *   - 事件回调触发时正确分发到 store 动作
 *   - AI 窗口（hash 含 ai-window）不注册 apply-to-editor
 */
import { describe, it, expect, beforeAll, beforeEach, afterEach, afterAll, vi } from "vitest";

// vi.hoisted 创建可追踪的 mock 句柄——必须在 vi.mock 工厂之前定义，因为
// vi.mock 会被提升到文件顶部执行。Events.On 每次注册返回一个 cancel spy，
// 并按事件名 + 回调收集，便于断言「注册/注销/回调分发」。
const mocks = vi.hoisted(() => {
  const applyToEditorCancels: Array<ReturnType<typeof vi.fn>> = [];
  const eventCancels: Array<ReturnType<typeof vi.fn>> = [];
  const registeredEvents: Array<{ name: string; cb: (e: unknown) => void }> = [];
  const emittedEvents: Array<{ name: string; payload: unknown }> = [];
  return {
    applyToEditorCancels,
    eventCancels,
    registeredEvents,
    emittedEvents,
    initWindowMaximiseListener: vi.fn(),
    handleWindowMaximisedEvent: vi.fn(),
    initSettingsSyncListener: vi.fn(),
    handleSettingsChangedEvent: vi.fn(),
    initProjectRemovedListener: vi.fn(),
    handleProjectRemovedEvent: vi.fn(),
    unregisterAppListeners: vi.fn(),
    ensureAIEventListeners: vi.fn(),
    cleanupAIEventListeners: vi.fn(),
    handleAIChunkEvent: vi.fn(),
    handleAIDoneEvent: vi.fn(),
    handleAIErrorEvent: vi.fn(),
    handleAIStreamBusyEvent: vi.fn(),
    handleAIToolCallsEvent: vi.fn(),
    handleConversationSavedEvent: vi.fn(),
    initAgentPendingSyncListener: vi.fn(),
    cleanupAgentPendingSyncListener: vi.fn(),
    handleAgentPendingUpdatedEvent: vi.fn(),
    requestApplyToEditor: vi.fn(),
    notifyError: vi.fn(),
    translate: vi.fn((k: string) => k),
    beginExtensionLifecycleHold: vi.fn(),
    deactivateExtension: vi.fn(),
    invalidateExtensionCaches: vi.fn(),
    isExtensionActivated: vi.fn(),
    refreshExtensionCaches: vi.fn(),
    reactivateExtension: vi.fn(),
    releaseExtensionLifecycleHold: vi.fn(),
    restoreExtensionContributions: vi.fn(),
    failOnEvent: "" as string,
  };
});

vi.doMock("@wailsio/runtime", () => ({
  Events: {
    On: vi.fn((name: string, cb: (event: unknown) => void) => {
      if (name === mocks.failOnEvent) throw new Error(`registration failed: ${name}`);
      mocks.registeredEvents.push({ name, cb });
      const cancel = vi.fn();
      mocks.eventCancels.push(cancel);
      if (name === "ai:apply-to-editor") mocks.applyToEditorCancels.push(cancel);
      return cancel;
    }),
    Emit: vi.fn((name: string, payload: unknown) => {
      mocks.emittedEvents.push({ name, payload });
      return Promise.resolve();
    }),
  },
}));

vi.doMock("@/stores/app", () => ({
  initWindowMaximiseListener: mocks.initWindowMaximiseListener,
  handleWindowMaximisedEvent: mocks.handleWindowMaximisedEvent,
  initSettingsSyncListener: mocks.initSettingsSyncListener,
  handleSettingsChangedEvent: mocks.handleSettingsChangedEvent,
  initProjectRemovedListener: mocks.initProjectRemovedListener,
  handleProjectRemovedEvent: mocks.handleProjectRemovedEvent,
  unregisterAppListeners: mocks.unregisterAppListeners,
}));

vi.doMock("@/stores/ai", () => ({
  ensureAIEventListeners: mocks.ensureAIEventListeners,
  cleanupAIEventListeners: mocks.cleanupAIEventListeners,
  handleAIChunkEvent: mocks.handleAIChunkEvent,
  handleAIDoneEvent: mocks.handleAIDoneEvent,
  handleAIErrorEvent: mocks.handleAIErrorEvent,
  handleAIStreamBusyEvent: mocks.handleAIStreamBusyEvent,
  handleAIToolCallsEvent: mocks.handleAIToolCallsEvent,
  handleConversationSavedEvent: mocks.handleConversationSavedEvent,
}));

vi.doMock("@/stores/agent", () => ({
  initAgentPendingSyncListener: mocks.initAgentPendingSyncListener,
  cleanupAgentPendingSyncListener: mocks.cleanupAgentPendingSyncListener,
  handleAgentPendingUpdatedEvent: mocks.handleAgentPendingUpdatedEvent,
}));

vi.doMock("@/stores/editor", () => ({
  requestApplyToEditor: mocks.requestApplyToEditor,
}));

vi.doMock("@/lib/notifications", () => ({
  notifyError: mocks.notifyError,
}));

vi.doMock("@/lib/i18n", () => ({
  translate: mocks.translate,
}));

vi.doMock("@/lib/vscodeExtensionActivation", () => ({
  beginExtensionLifecycleHold: mocks.beginExtensionLifecycleHold,
  deactivateExtension: mocks.deactivateExtension,
  invalidateExtensionCaches: mocks.invalidateExtensionCaches,
  isExtensionActivated: mocks.isExtensionActivated,
  refreshExtensionCaches: mocks.refreshExtensionCaches,
  reactivateExtension: mocks.reactivateExtension,
  releaseExtensionLifecycleHold: mocks.releaseExtensionLifecycleHold,
  restoreExtensionContributions: mocks.restoreExtensionContributions,
}));

type CrossWindowSyncModule = typeof import("./crossWindowSync");
let handleApplyToEditorEvent: CrossWindowSyncModule["handleApplyToEditorEvent"];
let initCrossWindowSync: CrossWindowSyncModule["initCrossWindowSync"];
let teardownCrossWindowSync: CrossWindowSyncModule["teardownCrossWindowSync"];
let isCrossWindowSyncInitialised: CrossWindowSyncModule["isCrossWindowSyncInitialised"];
let subscribeCrossWindowEvent: CrossWindowSyncModule["subscribeCrossWindowEvent"];

beforeAll(async () => {
  const module = await import("./crossWindowSync");
  handleApplyToEditorEvent = module.handleApplyToEditorEvent;
  initCrossWindowSync = module.initCrossWindowSync;
  teardownCrossWindowSync = module.teardownCrossWindowSync;
  isCrossWindowSyncInitialised = module.isCrossWindowSyncInitialised;
  subscribeCrossWindowEvent = module.subscribeCrossWindowEvent;
});

// 顶层 afterEach：无论哪个 describe 块，都确保模块状态（initialised /
// applyToEditorCancel）在测试间重置，避免相互污染。
afterEach(() => {
  teardownCrossWindowSync();
});

afterAll(() => {
  vi.doUnmock("@wailsio/runtime");
  vi.doUnmock("@/stores/app");
  vi.doUnmock("@/stores/ai");
  vi.doUnmock("@/stores/agent");
  vi.doUnmock("@/stores/editor");
  vi.doUnmock("@/lib/notifications");
  vi.doUnmock("@/lib/i18n");
  vi.doUnmock("@/lib/vscodeExtensionActivation");
  vi.resetModules();
});

describe("crossWindowSync — handleApplyToEditorEvent（事件 → store 动作）", () => {
  beforeEach(() => {
    mocks.requestApplyToEditor.mockClear();
    mocks.notifyError.mockClear();
    mocks.translate.mockClear();
  });

  it("有效 payload（data 对象）→ 调用 requestApplyToEditor(path, code)", () => {
    handleApplyToEditorEvent({
      data: { code: "print(1)", filePath: "/a.go", language: "go" },
    });
    expect(mocks.requestApplyToEditor).toHaveBeenCalledWith("/a.go", "print(1)");
    expect(mocks.notifyError).not.toHaveBeenCalled();
  });

  it("无 code → 静默返回，不调用任何 store 动作", () => {
    handleApplyToEditorEvent({ data: { filePath: "/a.go" } });
    expect(mocks.requestApplyToEditor).not.toHaveBeenCalled();
    expect(mocks.notifyError).not.toHaveBeenCalled();
  });

  it("有 code 但无 filePath → 调用 notifyError 提示", () => {
    handleApplyToEditorEvent({ data: { code: "x = 1" } });
    expect(mocks.requestApplyToEditor).not.toHaveBeenCalled();
    expect(mocks.notifyError).toHaveBeenCalledTimes(1);
    expect(mocks.translate).toHaveBeenCalledWith("aiWindow.noActiveFile");
    expect(mocks.translate).toHaveBeenCalledWith("aiWindow.applyTitle");
  });

  it("filePath 仅空白 → 调用 notifyError", () => {
    handleApplyToEditorEvent({ data: { code: "x", filePath: "   " } });
    expect(mocks.notifyError).toHaveBeenCalledTimes(1);
    expect(mocks.requestApplyToEditor).not.toHaveBeenCalled();
  });

  it("data 为数组 → 取首项作为 payload", () => {
    handleApplyToEditorEvent({
      data: [{ code: "fmt.Println()", filePath: "/b.go" }],
    });
    expect(mocks.requestApplyToEditor).toHaveBeenCalledWith("/b.go", "fmt.Println()");
  });

  it("event 为 null → 静默返回", () => {
    handleApplyToEditorEvent(null);
    expect(mocks.requestApplyToEditor).not.toHaveBeenCalled();
    expect(mocks.notifyError).not.toHaveBeenCalled();
  });

  it("event 无 data 字段 → 静默返回", () => {
    handleApplyToEditorEvent({ foo: "bar" });
    expect(mocks.requestApplyToEditor).not.toHaveBeenCalled();
    expect(mocks.notifyError).not.toHaveBeenCalled();
  });
});

describe("crossWindowSync — init / teardown 生命周期", () => {
  beforeEach(() => {
    mocks.initWindowMaximiseListener.mockClear();
    mocks.initSettingsSyncListener.mockClear();
    mocks.initProjectRemovedListener.mockClear();
    mocks.unregisterAppListeners.mockClear();
    mocks.requestApplyToEditor.mockClear();
    mocks.handleAIChunkEvent.mockClear();
    mocks.applyToEditorCancels.length = 0;
    mocks.eventCancels.length = 0;
    mocks.registeredEvents.length = 0;
    mocks.emittedEvents.length = 0;
    mocks.beginExtensionLifecycleHold.mockClear();
    mocks.deactivateExtension.mockReset();
    mocks.deactivateExtension.mockResolvedValue(undefined);
    mocks.invalidateExtensionCaches.mockClear();
    mocks.isExtensionActivated.mockReset();
    mocks.isExtensionActivated.mockReturnValue(true);
    mocks.refreshExtensionCaches.mockReset();
    mocks.refreshExtensionCaches.mockResolvedValue(undefined);
    mocks.reactivateExtension.mockReset();
    mocks.reactivateExtension.mockResolvedValue(true);
    mocks.releaseExtensionLifecycleHold.mockClear();
    mocks.restoreExtensionContributions.mockClear();
    mocks.failOnEvent = "";
    // 确保为主窗口（hash 不含 ai-window）。
    try {
      window.location.hash = "";
    } catch {
      /* jsdom 某些版本限制 location 写入，忽略 */
    }
  });

  it("initCrossWindowSync 注册 ai:apply-to-editor 并编排 app 三个 init 函数", () => {
    expect(isCrossWindowSyncInitialised()).toBe(false);

    initCrossWindowSync();

    expect(isCrossWindowSyncInitialised()).toBe(true);
    expect(mocks.initWindowMaximiseListener).toHaveBeenCalledTimes(1);
    expect(mocks.initSettingsSyncListener).toHaveBeenCalledTimes(1);
    expect(mocks.initProjectRemovedListener).toHaveBeenCalledTimes(1);
    const applyRegistrations = mocks.registeredEvents.filter(
      (r) => r.name === "ai:apply-to-editor",
    );
    expect(applyRegistrations).toHaveLength(1);
  });

  it("幂等：重复 init 先 teardown 旧监听器再重注册（不泄漏）", () => {
    initCrossWindowSync();
    const firstCancel = mocks.applyToEditorCancels[0];
    expect(firstCancel).toBeDefined();
    expect(firstCancel).not.toHaveBeenCalled();

    initCrossWindowSync();

    // 第一次注册的 cancel 应被内部 teardown 调用一次。
    expect(firstCancel).toHaveBeenCalledTimes(1);
    // 应有第二次注册。
    expect(mocks.applyToEditorCancels).toHaveLength(2);
    // unregisterAppListeners 也应在内部 teardown 中被调用一次。
    expect(mocks.unregisterAppListeners).toHaveBeenCalledTimes(1);
    // 仍处于已初始化状态。
    expect(isCrossWindowSyncInitialised()).toBe(true);
  });

  it("teardownCrossWindowSync 注销 apply-to-editor 监听器并调用 unregisterAppListeners", () => {
    initCrossWindowSync();
    const cancel = mocks.applyToEditorCancels[0];
    expect(cancel).not.toHaveBeenCalled();

    teardownCrossWindowSync();

    expect(cancel).toHaveBeenCalledTimes(1);
    expect(mocks.unregisterAppListeners).toHaveBeenCalledTimes(1);
    expect(isCrossWindowSyncInitialised()).toBe(false);
  });

  it("teardown 未初始化时也安全（无异常）", () => {
    expect(() => teardownCrossWindowSync()).not.toThrow();
  });

  it("事件回调触发时分发到 store 动作（requestApplyToEditor）", () => {
    initCrossWindowSync();
    mocks.requestApplyToEditor.mockClear();
    const reg = mocks.registeredEvents.find((r) => r.name === "ai:apply-to-editor");
    expect(reg).toBeDefined();
    // 模拟 Wails 派发事件。
    reg!.cb({ data: { code: "hello()", filePath: "/c.go" } });
    expect(mocks.requestApplyToEditor).toHaveBeenCalledWith("/c.go", "hello()");
  });

  it("AI stream 事件只注册一次，旧回调在 teardown 后失效", () => {
    initCrossWindowSync();
    const firstChunk = mocks.registeredEvents.find((r) => r.name === "ai:chunk");
    expect(firstChunk).toBeDefined();
    expect(mocks.registeredEvents.filter((r) => r.name === "ai:chunk")).toHaveLength(1);

    firstChunk!.cb({ data: { streamId: "s1", data: "a" } });
    expect(mocks.handleAIChunkEvent).toHaveBeenCalledTimes(1);

    teardownCrossWindowSync();
    firstChunk!.cb({ data: { streamId: "s1", data: "late" } });
    expect(mocks.handleAIChunkEvent).toHaveBeenCalledTimes(1);
  });

  it("重复 init 后旧 chunk 回调失效，只有当前注册点分发", () => {
    initCrossWindowSync();
    const firstChunk = mocks.registeredEvents.find((r) => r.name === "ai:chunk")!;

    initCrossWindowSync();
    const chunkRegistrations = mocks.registeredEvents.filter((r) => r.name === "ai:chunk");
    expect(chunkRegistrations).toHaveLength(2);

    firstChunk.cb({ data: { streamId: "s1", data: "stale" } });
    chunkRegistrations[1].cb({ data: { streamId: "s1", data: "current" } });
    expect(mocks.handleAIChunkEvent).toHaveBeenCalledTimes(1);
  });

  it("teardown 对称取消集中层注册的全部 Wails 监听器", () => {
    initCrossWindowSync();
    const registrations = [...mocks.registeredEvents];

    teardownCrossWindowSync();

    expect(registrations.length).toBeGreaterThan(0);
    expect(mocks.eventCancels).toHaveLength(registrations.length);
    for (const cancel of mocks.eventCancels) expect(cancel).toHaveBeenCalledTimes(1);
    expect(mocks.cleanupAIEventListeners).toHaveBeenCalled();
    expect(mocks.cleanupAgentPendingSyncListener).toHaveBeenCalled();
  });

  it("本地订阅取消后不再收到跨窗事件", () => {
    const subscriber = vi.fn();
    const unsubscribe = subscribeCrossWindowEvent("ai:selection", subscriber);
    initCrossWindowSync();
    const selection = mocks.registeredEvents.find((r) => r.name === "ai:selection")!;

    selection.cb({ data: { code: "before unmount" } });
    unsubscribe();
    selection.cb({ data: { code: "after unmount" } });

    expect(subscriber).toHaveBeenCalledTimes(1);
  });

  it("注册中途失败时回滚已注册监听器和 store 处理器", () => {
    mocks.failOnEvent = "ai:done";

    expect(() => initCrossWindowSync()).not.toThrow();

    expect(isCrossWindowSyncInitialised()).toBe(false);
    expect(mocks.cleanupAIEventListeners).toHaveBeenCalled();
    expect(mocks.cleanupAgentPendingSyncListener).toHaveBeenCalled();
    expect(mocks.unregisterAppListeners).toHaveBeenCalled();
    expect(mocks.eventCancels.length).toBeGreaterThan(0);
    for (const cancel of mocks.eventCancels) expect(cancel).toHaveBeenCalledTimes(1);
  });

  it("AI 窗口（hash 含 ai-window）不注册 apply-to-editor", () => {
    const originalHash = window.location.hash;
    try {
      window.location.hash = "/ai-window";
    } catch {
      // jsdom 限制写入时跳过精确断言。
    }
    initCrossWindowSync();
    const applyRegistrations = mocks.registeredEvents.filter(
      (r) => r.name === "ai:apply-to-editor",
    );
    // 仅在 hash 确实被设置时才断言（兼容 jsdom 限制）。
    if (window.location.hash.includes("ai-window")) {
      expect(applyRegistrations).toHaveLength(0);
      // 但 app 三个 init 仍被编排调用。
      expect(mocks.initWindowMaximiseListener).toHaveBeenCalledTimes(1);
    }
    try {
      window.location.hash = originalHash;
    } catch {
      /* 恢复失败忽略 */
    }
  });
});

describe("crossWindowSync — extension lifecycle coordinator", () => {
  const request = {
    requestId: "lifecycle-base",
    extensionId: "acme.demo",
    publisher: "acme",
    name: "demo",
    action: "stop" as const,
    wasActive: false,
  };

  function lifecycleCallback(): (event: unknown) => void {
    initCrossWindowSync();
    const registration = mocks.registeredEvents.find(
      (entry) => entry.name === "extension:lifecycle-request",
    );
    expect(registration).toBeDefined();
    return registration!.cb;
  }

  async function flushLifecycle(): Promise<void> {
    await new Promise((resolve) => setTimeout(resolve, 0));
    await new Promise((resolve) => setTimeout(resolve, 0));
  }

  beforeEach(() => {
    mocks.registeredEvents.length = 0;
    mocks.emittedEvents.length = 0;
    mocks.beginExtensionLifecycleHold.mockClear();
    mocks.deactivateExtension.mockReset();
    mocks.deactivateExtension.mockResolvedValue(undefined);
    mocks.invalidateExtensionCaches.mockClear();
    mocks.isExtensionActivated.mockReset();
    mocks.isExtensionActivated.mockReturnValue(true);
    mocks.refreshExtensionCaches.mockReset();
    mocks.refreshExtensionCaches.mockResolvedValue(undefined);
    mocks.reactivateExtension.mockReset();
    mocks.reactivateExtension.mockResolvedValue(true);
    mocks.releaseExtensionLifecycleHold.mockClear();
    mocks.restoreExtensionContributions.mockClear();
  });

  it("stops a Worker, reports the actual active state, and holds activation", async () => {
    mocks.isExtensionActivated.mockReturnValue(true);
    lifecycleCallback()({ data: { ...request, requestId: "lifecycle-stop" } });
    await flushLifecycle();

    expect(mocks.beginExtensionLifecycleHold).toHaveBeenCalledWith("acme.demo");
    expect(mocks.deactivateExtension).toHaveBeenCalledWith("acme.demo");
    expect(mocks.emittedEvents).toContainEqual({
      name: "extension:lifecycle-result",
      payload: expect.objectContaining({
        ...request,
        requestId: "lifecycle-stop",
        ok: true,
        wasActive: true,
      }),
    });
  });

  it("restores the old active extension when stopping its Worker fails", async () => {
    mocks.isExtensionActivated.mockReturnValue(true);
    mocks.deactivateExtension.mockRejectedValueOnce(new Error("stop failed"));
    lifecycleCallback()({
      data: { ...request, requestId: "lifecycle-stop-failure" },
    });
    await flushLifecycle();

    expect(mocks.releaseExtensionLifecycleHold).toHaveBeenCalledWith("acme.demo");
    expect(mocks.reactivateExtension).toHaveBeenCalledWith("acme.demo");
    expect(mocks.emittedEvents).toContainEqual({
      name: "extension:lifecycle-result",
      payload: expect.objectContaining({
        requestId: "lifecycle-stop-failure",
        action: "stop",
        ok: false,
        error: "stop failed",
      }),
    });
  });

  it("restores lazy contributions when stopping an inactive extension fails", async () => {
    mocks.isExtensionActivated.mockReturnValue(false);
    mocks.deactivateExtension.mockRejectedValueOnce(new Error("stop failed"));
    lifecycleCallback()({
      data: { ...request, requestId: "lifecycle-inactive-stop-failure" },
    });
    await flushLifecycle();

    expect(mocks.releaseExtensionLifecycleHold).toHaveBeenCalledWith("acme.demo");
    expect(mocks.reactivateExtension).not.toHaveBeenCalled();
    expect(mocks.restoreExtensionContributions).toHaveBeenCalledWith("acme.demo");
    expect(mocks.emittedEvents).toContainEqual({
      name: "extension:lifecycle-result",
      payload: expect.objectContaining({
        requestId: "lifecycle-inactive-stop-failure",
        action: "stop",
        ok: false,
      }),
    });
  });

  it("serializes duplicate request IDs without running the Worker operation twice", async () => {
    let resolveDeactivation: (() => void) | undefined;
    mocks.deactivateExtension.mockReturnValueOnce(
      new Promise<void>((resolve) => {
        resolveDeactivation = resolve;
      }),
    );
    const callback = lifecycleCallback();
    const duplicateRequest = { ...request, requestId: "lifecycle-duplicate" };
    callback({ data: duplicateRequest });
    callback({ data: duplicateRequest });

    await Promise.resolve();
    await Promise.resolve();
    expect(mocks.deactivateExtension).toHaveBeenCalledTimes(1);
    expect(resolveDeactivation).toBeDefined();
    resolveDeactivation?.();
    await flushLifecycle();
    expect(mocks.emittedEvents).toHaveLength(1);
  });

  it("keeps inflight lifecycle deduplication across listener replacement", async () => {
    let resolveDeactivation: (() => void) | undefined;
    mocks.deactivateExtension.mockReturnValueOnce(
      new Promise<void>((resolve) => {
        resolveDeactivation = resolve;
      }),
    );
    const lifecycleRequest = { ...request, requestId: "lifecycle-hmr-inflight" };
    lifecycleCallback()({ data: lifecycleRequest });
    await Promise.resolve();

    initCrossWindowSync();
    const registrations = mocks.registeredEvents.filter(
      (entry) => entry.name === "extension:lifecycle-request",
    );
    registrations.at(-1)!.cb({ data: lifecycleRequest });
    await Promise.resolve();

    expect(mocks.deactivateExtension).toHaveBeenCalledTimes(1);
    resolveDeactivation?.();
    await flushLifecycle();
    expect(mocks.emittedEvents).toHaveLength(1);
  });

  it("rejects forged identity payloads before invoking lifecycle actions", async () => {
    lifecycleCallback()({
      data: { ...request, requestId: "lifecycle-forged", extensionId: "other.demo" },
    });
    await flushLifecycle();

    expect(mocks.deactivateExtension).not.toHaveBeenCalled();
    expect(mocks.emittedEvents).toHaveLength(0);
  });

  it("keeps a lifecycle hold when restore cache refresh fails", async () => {
    mocks.refreshExtensionCaches.mockRejectedValueOnce(new Error("refresh failed"));
    lifecycleCallback()({
      data: { ...request, requestId: "lifecycle-refresh-failure", action: "restore", wasActive: true },
    });
    await flushLifecycle();

    expect(mocks.releaseExtensionLifecycleHold).not.toHaveBeenCalled();
    expect(mocks.reactivateExtension).not.toHaveBeenCalled();
    expect(mocks.emittedEvents).toContainEqual({
      name: "extension:lifecycle-result",
      payload: expect.objectContaining({ ok: false, action: "restore" }),
    });
  });

  it("re-establishes the hold when restored Worker activation fails", async () => {
    mocks.reactivateExtension.mockResolvedValueOnce(false);
    lifecycleCallback()({
      data: { ...request, requestId: "lifecycle-reactivation-failure", action: "restore", wasActive: true },
    });
    await flushLifecycle();

    expect(mocks.releaseExtensionLifecycleHold).toHaveBeenCalledWith("acme.demo");
    expect(mocks.beginExtensionLifecycleHold).toHaveBeenCalledWith("acme.demo");
    expect(mocks.emittedEvents).toContainEqual({
      name: "extension:lifecycle-result",
      payload: expect.objectContaining({ ok: false, action: "restore" }),
    });
  });
});
