import { beforeEach, describe, expect, it, vi } from "vitest";

const bindings = vi.hoisted(() => ({
  LoadAsyncStack: vi.fn(),
  LoadStackFrames: vi.fn(),
}));

vi.mock("../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/debugservice.js", () => bindings);

vi.mock("@wailsio/runtime", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@wailsio/runtime")>();
  return {
    ...actual,
    Events: { ...actual.Events, On: vi.fn(), Emit: vi.fn() },
  };
});

vi.mock("@/api/services", () => ({
  fileService: { readFile: vi.fn() },
  debugService: {
    isAvailable: vi.fn().mockResolvedValue(true),
    getState: vi.fn(),
    listSessions: vi.fn().mockResolvedValue([]),
    getActiveSession: vi.fn().mockResolvedValue("default"),
  },
}));

vi.mock("@/stores/app", () => ({
  appState: { currentProject: "/repo", terminalVisible: false },
}));

vi.mock("@/stores/output", () => ({ pushOutput: vi.fn() }));
vi.mock("@/lib/notifications", () => ({
  notifyError: vi.fn(),
  notifySuccess: vi.fn(),
  notifyInfo: vi.fn(),
  notifyWarning: vi.fn(),
}));

import {
  applyDebugSnapshot,
  cancelAsyncStackLoad,
  debugState,
  loadAsyncParentStack,
  loadMoreStackFrames,
} from "./debug";

function cancellable<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  }) as Promise<T> & { cancel: ReturnType<typeof vi.fn> };
  promise.cancel = vi.fn();
  return { promise, resolve };
}

describe("debug async stack store", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    debugState.stack = [];
    debugState.asyncStackSegments = [];
    debugState.runGeneration = 10;
    debugState.running = true;
    debugState.stopped = true;
    debugState.supportsDelayedStackTraceLoading = true;
    debugState.supportsAsyncStackTrace = true;
    debugState.stackHasMore = true;
    debugState.stackTotalFrames = 4;
    debugState.asyncStackRootId = "root";
    debugState.asyncStackLoading = false;
  });

  it("appends a DAP page with startFrame/levels for the current generation", async () => {
    debugState.stack = [{ id: 1, name: "top", file: "/repo/a.go", line: 1, column: 1 }];
    bindings.LoadStackFrames.mockResolvedValue({
      generation: 10,
      frames: [{ id: 2, name: "parent", file: "/repo/a.go", line: 2, column: 1 }],
      totalFrames: 2,
      hasMore: false,
    });

    await loadMoreStackFrames(16);

    expect(bindings.LoadStackFrames).toHaveBeenCalledWith(10, 1, 16);
    expect(debugState.stack.map((frame) => frame.id)).toEqual([1, 2]);
    expect(debugState.stackHasMore).toBe(false);
  });

  it("cancels an async parent load and ignores its late result after restart", async () => {
    const pending = cancellable<{
      generation: number;
      description: string;
      frames: Array<{ id: number; name: string; file: string; line: number; column: number }>;
      parentId: string;
    }>();
    bindings.LoadAsyncStack.mockReturnValue(pending.promise);

    const loading = loadAsyncParentStack("root");
    await vi.waitFor(() => expect(bindings.LoadAsyncStack).toHaveBeenCalledWith(10, "root"));
    applyDebugSnapshot({
      session: { running: true, address: "node", mode: "node", message: "new" },
      generation: 11,
      stack: [{ id: 99, name: "new", file: "/repo/new.js", line: 1, column: 1 }],
      supportsAsyncStackTrace: true,
      asyncStackRootId: "new-root",
    });
    cancelAsyncStackLoad();
    pending.resolve({
      generation: 10,
      description: "old promise",
      frames: [{ id: 7, name: "old", file: "/repo/old.js", line: 1, column: 1 }],
      parentId: "old-parent",
    });
    await loading;

    expect(pending.promise.cancel).toHaveBeenCalled();
    expect(debugState.asyncStackSegments).toEqual([]);
    expect(debugState.asyncStackRootId).toBe("new-root");
  });

  it("does not call unsupported adapter features", async () => {
    debugState.supportsDelayedStackTraceLoading = false;
    debugState.supportsAsyncStackTrace = false;

    await loadMoreStackFrames();
    await loadAsyncParentStack("root");

    expect(bindings.LoadStackFrames).not.toHaveBeenCalled();
    expect(bindings.LoadAsyncStack).not.toHaveBeenCalled();
  });
});
