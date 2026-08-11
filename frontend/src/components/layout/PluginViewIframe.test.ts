import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { mount } from "@vue/test-utils";
import { nextTick } from "vue";
import PluginViewIframe from "./PluginViewIframe.vue";
import type { PluginManifest } from "@/types";
import type { RpcHandler } from "@/lib/pluginSandbox";

// Mock the global window.postMessage / addEventListener so tests can
// simulate iframe messages.
const messageListeners = new Set<(e: MessageEvent) => void>();
const originalAddEventListener = window.addEventListener;
const originalRemoveEventListener = window.removeEventListener;

beforeEach(() => {
  messageListeners.clear();
  vi.spyOn(window, "addEventListener").mockImplementation((type, listener) => {
    if (type === "message") {
      messageListeners.add(listener as (e: MessageEvent) => void);
    } else {
      originalAddEventListener.call(window, type, listener);
    }
  });
  vi.spyOn(window, "removeEventListener").mockImplementation((type, listener) => {
    if (type === "message") {
      messageListeners.delete(listener as (e: MessageEvent) => void);
    } else {
      originalRemoveEventListener.call(window, type, listener);
    }
  });
});

function makeManifest(overrides?: Partial<PluginManifest>): PluginManifest {
  return {
    name: "test-plugin",
    version: "1.0.0",
    main: "main.js",
    permissions: [],
    ...overrides,
  };
}

/** Simulate the iframe sending a message to the host. */
function sendMessageFromIframe(
  data: unknown,
  source: MessageEventSource | null = null,
  origin = "null",
) {
  const event = new MessageEvent("message", { data, origin, source });
  for (const listener of messageListeners) {
    listener(event);
  }
}

describe("PluginViewIframe (N-36 / Proposal G)", () => {
  it("renders an iframe with the correct src", () => {
    const manifest = makeManifest();
    const rpcHandler: RpcHandler = vi.fn();
    const wrapper = mount(PluginViewIframe, {
      props: {
        pluginName: "test-plugin",
        viewId: "my-view",
        title: "My View",
        manifest,
        rpcHandler,
      },
    });
    const iframe = wrapper.find("iframe");
    expect(iframe.exists()).toBe(true);
    expect(iframe.attributes("src")).toContain("/_plugins/test-plugin/view.html");
    expect(iframe.attributes("src")).toContain("viewId=my-view");
    expect(iframe.attributes("sandbox")).toBe("allow-scripts");
    expect(iframe.attributes("title")).toBe("My View");
  });

  it("sends koyoriIde:init when iframe reports ready", async () => {
    const manifest = makeManifest();
    const rpcHandler: RpcHandler = vi.fn();
    const wrapper = mount(PluginViewIframe, {
      props: {
        pluginName: "test-plugin",
        viewId: "my-view",
        title: "My View",
        manifest,
        rpcHandler,
      },
    });

    // Mock the iframe's contentWindow.postMessage
    const postMessageSpy = vi.fn();
    const iframe = wrapper.find("iframe").element as HTMLIFrameElement;
    const mockContentWindow = { postMessage: postMessageSpy };
    Object.defineProperty(iframe, "contentWindow", {
      value: mockContentWindow,
      configurable: true,
    });

    // Simulate iframe ready
    sendMessageFromIframe(
      { type: "koyoriIde:ready" },
      mockContentWindow as unknown as MessageEventSource,
    );

    await nextTick();

    expect(postMessageSpy).toHaveBeenCalledWith(
      expect.objectContaining({
        type: "koyoriIde:init",
        viewId: "my-view",
      }),
      "*",
    );
    const initMsg = postMessageSpy.mock.calls[0][0];
    expect(initMsg.manifest.name).toBe("test-plugin");
  });

  it("routes rpc-request to the rpcHandler and sends response", async () => {
    const manifest = makeManifest({ permissions: ["fs.read"] });
    const rpcHandler: RpcHandler = vi.fn().mockResolvedValue("file contents");
    const wrapper = mount(PluginViewIframe, {
      props: {
        pluginName: "test-plugin",
        viewId: "my-view",
        title: "My View",
        manifest,
        rpcHandler,
      },
    });

    const postMessageSpy = vi.fn();
    const iframe = wrapper.find("iframe").element as HTMLIFrameElement;
    const mockContentWindow = { postMessage: postMessageSpy };
    Object.defineProperty(iframe, "contentWindow", {
      value: mockContentWindow,
      configurable: true,
    });

    sendMessageFromIframe(
      {
        type: "koyoriIde:rpc-request",
        id: 42,
        method: "workspace.readFile",
        args: ["src/main.ts"],
      },
      mockContentWindow as unknown as MessageEventSource,
    );

    // Wait for async handler
    await new Promise((r) => setTimeout(r, 10));

    expect(rpcHandler).toHaveBeenCalledWith(
      "test-plugin",
      manifest,
      "workspace.readFile",
      ["src/main.ts"],
    );
    // Response sent back
    expect(postMessageSpy).toHaveBeenCalledWith(
      expect.objectContaining({
        type: "koyoriIde:rpc-response",
        id: 42,
        result: "file contents",
      }),
      "*",
    );
  });

  it("rejects rpc-request when permission not declared", async () => {
    const manifest = makeManifest({ permissions: [] }); // no fs.read
    const rpcHandler: RpcHandler = vi.fn();
    const wrapper = mount(PluginViewIframe, {
      props: {
        pluginName: "test-plugin",
        viewId: "my-view",
        title: "My View",
        manifest,
        rpcHandler,
      },
    });

    const postMessageSpy = vi.fn();
    const iframe = wrapper.find("iframe").element as HTMLIFrameElement;
    const mockContentWindow = { postMessage: postMessageSpy };
    Object.defineProperty(iframe, "contentWindow", {
      value: mockContentWindow,
      configurable: true,
    });

    sendMessageFromIframe(
      {
        type: "koyoriIde:rpc-request",
        id: 1,
        method: "workspace.readFile",
        args: ["file.ts"],
      },
      mockContentWindow as unknown as MessageEventSource,
    );

    await new Promise((r) => setTimeout(r, 10));

    // Handler should NOT have been called (permission denied)
    expect(rpcHandler).not.toHaveBeenCalled();
    // Error response sent
    expect(postMessageSpy).toHaveBeenCalledWith(
      expect.objectContaining({
        type: "koyoriIde:rpc-response",
        id: 1,
        error: expect.stringContaining("fs.read"),
      }),
      "*",
    );
  });

  it("sends error response when rpcHandler throws", async () => {
    const manifest = makeManifest({ permissions: ["fs.read"] });
    const rpcHandler: RpcHandler = vi.fn().mockRejectedValue(new Error("File not found"));
    const wrapper = mount(PluginViewIframe, {
      props: {
        pluginName: "test-plugin",
        viewId: "my-view",
        title: "My View",
        manifest,
        rpcHandler,
      },
    });

    const postMessageSpy = vi.fn();
    const iframe = wrapper.find("iframe").element as HTMLIFrameElement;
    const mockContentWindow = { postMessage: postMessageSpy };
    Object.defineProperty(iframe, "contentWindow", {
      value: mockContentWindow,
      configurable: true,
    });

    sendMessageFromIframe(
      {
        type: "koyoriIde:rpc-request",
        id: 7,
        method: "workspace.readFile",
        args: ["missing.ts"],
      },
      mockContentWindow as unknown as MessageEventSource,
    );

    await new Promise((r) => setTimeout(r, 10));

    expect(postMessageSpy).toHaveBeenCalledWith(
      expect.objectContaining({
        type: "koyoriIde:rpc-response",
        id: 7,
        error: "File not found",
      }),
      "*",
    );
  });

  it("ignores messages from disallowed sources", async () => {
    const manifest = makeManifest();
    const rpcHandler: RpcHandler = vi.fn();
    const wrapper = mount(PluginViewIframe, {
      props: {
        pluginName: "test-plugin",
        viewId: "my-view",
        title: "My View",
        manifest,
        rpcHandler,
      },
    });

    const postMessageSpy = vi.fn();
    const iframe = wrapper.find("iframe").element as HTMLIFrameElement;
    const mockContentWindow = { postMessage: postMessageSpy };
    Object.defineProperty(iframe, "contentWindow", {
      value: mockContentWindow,
      configurable: true,
    });

    // Send from a foreign source (not the iframe's contentWindow).
    // A spoofed "null" origin must NOT bypass the source check.
    const foreignSource = { postMessage: vi.fn() } as unknown as MessageEventSource;
    sendMessageFromIframe(
      { type: "koyoriIde:rpc-request", id: 1, method: "workspace.readFile", args: [] },
      foreignSource,
      "null",
    );

    // Also send with no source at all (e.g. a spoofed message).
    sendMessageFromIframe(
      { type: "koyoriIde:rpc-request", id: 2, method: "workspace.readFile", args: [] },
      null,
      "null",
    );

    await new Promise((r) => setTimeout(r, 10));

    expect(rpcHandler).not.toHaveBeenCalled();
    expect(postMessageSpy).not.toHaveBeenCalled();
  });

  it("ignores messages from the iframe source when its opaque origin is spoofed", async () => {
    const rpcHandler: RpcHandler = vi.fn();
    const wrapper = mount(PluginViewIframe, {
      props: {
        pluginName: "test-plugin",
        viewId: "my-view",
        title: "My View",
        manifest: makeManifest({ permissions: ["fs.read"] }),
        rpcHandler,
      },
    });
    const iframe = wrapper.find("iframe").element as HTMLIFrameElement;
    const mockContentWindow = { postMessage: vi.fn() };
    Object.defineProperty(iframe, "contentWindow", {
      value: mockContentWindow,
      configurable: true,
    });

    sendMessageFromIframe(
      { type: "koyoriIde:rpc-request", id: 9, method: "workspace.readFile", args: ["x"] },
      mockContentWindow as unknown as MessageEventSource,
      window.location.origin,
    );
    await Promise.resolve();

    expect(rpcHandler).not.toHaveBeenCalled();
  });

  it("logs koyoriIde:log messages to console", async () => {
    const manifest = makeManifest();
    const rpcHandler: RpcHandler = vi.fn();
    const wrapper = mount(PluginViewIframe, {
      props: {
        pluginName: "test-plugin",
        viewId: "my-view",
        title: "My View",
        manifest,
        rpcHandler,
      },
    });

    const iframe = wrapper.find("iframe").element as HTMLIFrameElement;
    const mockContentWindow = { postMessage: vi.fn() };
    Object.defineProperty(iframe, "contentWindow", {
      value: mockContentWindow,
      configurable: true,
    });

    const infoSpy = vi.spyOn(console, "info").mockImplementation(() => {});
    sendMessageFromIframe(
      { type: "koyoriIde:log", level: "info", message: "Hello from iframe" },
      mockContentWindow as unknown as MessageEventSource,
    );

    await nextTick();

    expect(infoSpy).toHaveBeenCalledWith(
      expect.stringContaining("plugin view: test-plugin/my-view"),
      "Hello from iframe",
    );
    infoSpy.mockRestore();
  });
});

// BUG3b: projectRoot MUST be in the iframe URL so the backend can resolve
// project-scoped plugins. The sandboxed iframe (opaque origin) cannot read
// its own URL, so this does not leak the path to plugin code. projectRoot
// is ALSO sent via postMessage on koyoriIde:init for plugins that need it.
describe("BUG3b: projectRoot included in iframe URL + postMessage", () => {
  afterEach(() => {
    delete (window as unknown as { __NKNK_PROJECT_ROOT__?: string }).__NKNK_PROJECT_ROOT__;
  });

  it("includes projectRoot in the iframe src URL when set", () => {
    (window as unknown as { __NKNK_PROJECT_ROOT__?: string }).__NKNK_PROJECT_ROOT__ = "/secret/project/path";
    const manifest = makeManifest();
    const rpcHandler: RpcHandler = vi.fn();
    const wrapper = mount(PluginViewIframe, {
      props: {
        pluginName: "test-plugin",
        viewId: "my-view",
        title: "My View",
        manifest,
        rpcHandler,
      },
    });
    const src = wrapper.find("iframe").attributes("src");
    expect(src).toContain("/_plugins/test-plugin/view.html");
    expect(src).toContain("viewId=my-view");
    expect(src).toContain("projectRoot=");
    expect(src).toContain(encodeURIComponent("/secret/project/path"));
  });

  it("omits projectRoot query param when no project root is set", () => {
    delete (window as unknown as { __NKNK_PROJECT_ROOT__?: string }).__NKNK_PROJECT_ROOT__;
    const manifest = makeManifest();
    const rpcHandler: RpcHandler = vi.fn();
    const wrapper = mount(PluginViewIframe, {
      props: {
        pluginName: "test-plugin",
        viewId: "my-view",
        title: "My View",
        manifest,
        rpcHandler,
      },
    });
    const src = wrapper.find("iframe").attributes("src");
    expect(src).toContain("/_plugins/test-plugin/view.html");
    expect(src).toContain("viewId=my-view");
    expect(src).not.toContain("projectRoot");
  });

  it("sends projectRoot via postMessage in the koyoriIde:init message", async () => {
    (window as unknown as { __NKNK_PROJECT_ROOT__?: string }).__NKNK_PROJECT_ROOT__ = "/secret/project/path";
    const manifest = makeManifest();
    const rpcHandler: RpcHandler = vi.fn();
    const wrapper = mount(PluginViewIframe, {
      props: {
        pluginName: "test-plugin",
        viewId: "my-view",
        title: "My View",
        manifest,
        rpcHandler,
      },
    });

    const postMessageSpy = vi.fn();
    const iframe = wrapper.find("iframe").element as HTMLIFrameElement;
    const mockContentWindow = { postMessage: postMessageSpy };
    Object.defineProperty(iframe, "contentWindow", {
      value: mockContentWindow,
      configurable: true,
    });

    sendMessageFromIframe(
      { type: "koyoriIde:ready" },
      mockContentWindow as unknown as MessageEventSource,
    );

    await nextTick();

    const initCall = postMessageSpy.mock.calls.find(
      (c) => (c[0] as { type?: string }).type === "koyoriIde:init",
    );
    expect(initCall).toBeDefined();
    expect(initCall![0]).toMatchObject({
      type: "koyoriIde:init",
      projectRoot: "/secret/project/path",
    });
  });
});

// L-7: pending calls must time out after 30s
describe("L-7: pendingCalls 30s timeout", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("rejects a pending call that never receives a response after 30s", async () => {
    const manifest = makeManifest();
    const rpcHandler: RpcHandler = vi.fn();
    const wrapper = mount(PluginViewIframe, {
      props: {
        pluginName: "test-plugin",
        viewId: "my-view",
        title: "My View",
        manifest,
        rpcHandler,
      },
    });

    const postMessageSpy = vi.fn();
    const iframe = wrapper.find("iframe").element as HTMLIFrameElement;
    const mockContentWindow = { postMessage: postMessageSpy };
    Object.defineProperty(iframe, "contentWindow", {
      value: mockContentWindow,
      configurable: true,
    });

    const vm = wrapper.vm as unknown as {
      callIframeMethod: (method: string, args: unknown[]) => Promise<unknown>;
    };
    const promise = vm.callIframeMethod("view:refresh", []);

    // The rpc-call should have been sent to the iframe.
    expect(postMessageSpy).toHaveBeenCalledWith(
      expect.objectContaining({ type: "koyoriIde:rpc-call", method: "view:refresh" }),
      "*",
    );

    // Advance time to just before the timeout — should still be pending.
    vi.advanceTimersByTime(29_999);
    await Promise.resolve();
    // Promise should still be pending (not settled).
    let settled = false;
    void promise.then(() => { settled = true; }, () => { settled = true; });
    await Promise.resolve();
    expect(settled).toBe(false);

    // Advance past 30s — the timeout should fire.
    vi.advanceTimersByTime(1);
    await expect(promise).rejects.toThrow(/timed out after 30s/);
  });

  it("clears the timeout when a response arrives before 30s", async () => {
    const manifest = makeManifest();
    const rpcHandler: RpcHandler = vi.fn();
    const wrapper = mount(PluginViewIframe, {
      props: {
        pluginName: "test-plugin",
        viewId: "my-view",
        title: "My View",
        manifest,
        rpcHandler,
      },
    });

    const postMessageSpy = vi.fn();
    const iframe = wrapper.find("iframe").element as HTMLIFrameElement;
    const mockContentWindow = { postMessage: postMessageSpy };
    Object.defineProperty(iframe, "contentWindow", {
      value: mockContentWindow,
      configurable: true,
    });

    const vm = wrapper.vm as unknown as {
      callIframeMethod: (method: string, args: unknown[]) => Promise<unknown>;
    };
    const promise = vm.callIframeMethod("view:getState", []);

    // Simulate the iframe responding before the timeout.
    sendMessageFromIframe(
      { type: "koyoriIde:rpc-response", id: 1, result: { ok: true } },
      mockContentWindow as unknown as MessageEventSource,
    );

    await expect(promise).resolves.toEqual({ ok: true });

    // Advance well past 30s — the timeout should NOT fire (already cleared).
    vi.advanceTimersByTime(60_000);
    // If the timer wasn't cleared, it would try to reject an already-settled
    // promise, which is a no-op, but we verify no unhandled rejection occurs.
  });
});
