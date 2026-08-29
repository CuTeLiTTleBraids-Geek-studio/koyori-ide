import { describe, it, expect, beforeEach, vi } from "vitest";

// N-47: Capture Events.On callbacks so tests can simulate event emission.
const eventCallbacks = new Map<string, (event: any) => void>();
vi.mock("@wailsio/runtime", () => ({
  Events: {
    On: vi.fn((name: string, cb: (event: any) => void) => {
      eventCallbacks.set(name, cb);
    }),
  },
}));

vi.mock("@/api/services", () => ({
  terminalService: {
    start: vi.fn().mockResolvedValue(undefined),
    write: vi.fn().mockResolvedValue(undefined),
    kill: vi.fn().mockResolvedValue(undefined),
    resize: vi.fn().mockResolvedValue(undefined),
    isRunning: vi.fn().mockReturnValue(false),
    startSession: vi.fn().mockResolvedValue(undefined),
    killSession: vi.fn().mockResolvedValue(undefined),
    writeSession: vi.fn().mockResolvedValue(undefined),
    resizeSession: vi.fn().mockResolvedValue(undefined),
    isSessionRunning: vi.fn().mockReturnValue(false),
    listSessions: vi.fn().mockReturnValue([]),
  },
}));

import {
  terminalState,
  createSession,
  writeToSession,
  killSession,
  resizeSession,
  reconnectSession,
  setActiveSession,
  clearSessionOutput,
  onTerminalOutput,
  onSessionExit,
  runCommandInSession,
} from "./terminal";
import { terminalService } from "@/api/services";

describe("terminal store (multi-session)", () => {
  beforeEach(() => {
    // Reset state
    Object.keys(terminalState.sessions).forEach((id) =>
      delete terminalState.sessions[id],
    );
    terminalState.sessionOrder = [];
    terminalState.activeSessionId = null;
    terminalState.sessionsVersion = 0;
  });

  it("creates a session", async () => {
    const id = await createSession("/some/path");
    expect(id).toBeTruthy();
    expect(terminalState.sessions[id]).toBeDefined();
    expect(terminalState.sessions[id].running).toBe(true);
    expect(terminalState.activeSessionId).toBe(id);
  });

  it("G16: keeps session IDs unique when sessions start in the same millisecond", async () => {
    const now = vi.spyOn(Date, "now").mockReturnValue(1_700_000_000_000);
    try {
      const [first, second] = await Promise.all([
        createSession("/same-time-a"),
        createSession("/same-time-b"),
      ]);

      expect(first).not.toBe(second);
      expect(new Set([first, second]).size).toBe(2);
      expect(terminalState.sessionOrder).toEqual([first, second]);
      expect(terminalState.sessions[first]).toBeDefined();
      expect(terminalState.sessions[second]).toBeDefined();
    } finally {
      now.mockRestore();
    }
  });

  it("writes to a session", async () => {
    const id = await createSession("/path");
    await writeToSession(id, "ls\n");
    // No throw = pass
  });

  it("kills a session", async () => {
    const id = await createSession("/path");
    await killSession(id);
    expect(terminalState.sessions[id]).toBeUndefined();
    expect(terminalState.activeSessionId).toBeNull();
  });

  it("retains the session and backend handle when kill fails", async () => {
    const id = await createSession("/path");
    vi.mocked(terminalService.killSession).mockRejectedValueOnce(new Error("access denied"));

    await killSession(id);

    expect(terminalState.sessions[id]).toBeDefined();
    expect(terminalState.sessions[id].lastError).toBe("access denied");
    expect(terminalState.sessions[id].output).toContain("Terminal close failed");
    expect(terminalState.activeSessionId).toBe(id);
  });

  it("switches active session", async () => {
    const id1 = await createSession("/path1");
    const id2 = await createSession("/path2");
    expect(terminalState.activeSessionId).toBe(id2);
    setActiveSession(id1);
    expect(terminalState.activeSessionId).toBe(id1);
  });

  it("resizes a session", async () => {
    const id = await createSession("/path");
    await resizeSession(id, 120, 40);
    expect(terminalState.sessions[id].cols).toBe(120);
    expect(terminalState.sessions[id].rows).toBe(40);
  });

  it("clears session output", async () => {
    const id = await createSession("/path");
    terminalState.sessions[id].output = "hello";
    clearSessionOutput(id);
    expect(terminalState.sessions[id].output).toBe("");
  });

  it("maintains session order", async () => {
    const id1 = await createSession("/path1");
    const id2 = await createSession("/path2");
    const id3 = await createSession("/path3");
    expect(terminalState.sessionOrder).toEqual([id1, id2, id3]);
  });

  it("switches active session after kill", async () => {
    const id1 = await createSession("/path1");
    const id2 = await createSession("/path2");
    expect(terminalState.activeSessionId).toBe(id2);
    await killSession(id2);
    expect(terminalState.activeSessionId).toBe(id1);
  });

  // --- N-47: PTY exit detection ---

  it("N-47: onSessionExit returns an unsubscribe function", async () => {
    const id = await createSession("/path");
    const off = onSessionExit(id, () => {});
    expect(typeof off).toBe("function");
    off();
  });

  it("N-47: terminal:exited event sets session.running to false", async () => {
    const id = await createSession("/path");
    expect(terminalState.sessions[id].running).toBe(true);
    // Simulate the backend emitting terminal:exited for this session.
    const cb = eventCallbacks.get("terminal:exited");
    expect(cb).toBeDefined();
    cb!({ data: { sessionId: id, err: "EOF" } });
    expect(terminalState.sessions[id].running).toBe(false);
  });

  it("G16: exit payload preserves code and signal for the reconnect UI", async () => {
    const id = await createSession("/path", "cmd");
    const cb = eventCallbacks.get("terminal:exited");
    cb!({ data: { sessionId: id, code: -1, signal: "terminated", err: "signal" } });

    expect(terminalState.sessions[id].exitCode).toBe(-1);
    expect(terminalState.sessions[id].exitSignal).toBe("terminated");
    expect(terminalState.sessions[id].running).toBe(false);

    vi.mocked(terminalService.startSession).mockClear();
    const reconnected = await reconnectSession(id);
    expect(reconnected).toBe(true);
    const reconnectBackendId = vi.mocked(terminalService.startSession).mock.calls[0][0];
    expect(reconnectBackendId).not.toBe(id);
    expect(reconnectBackendId).toContain(`${id}:reconnect:`);
    expect(terminalService.startSession).toHaveBeenCalledWith(reconnectBackendId, "/path", "cmd");
    expect(terminalState.sessions[id].running).toBe(true);

    vi.mocked(terminalService.writeSession).mockClear();
    await writeToSession(id, "echo reconnected\n");
    expect(terminalService.writeSession).toHaveBeenCalledWith(
      reconnectBackendId,
      "echo reconnected\n",
    );

    // Delayed events from the old PTY incarnation cannot stop or write into
    // the newly reconnected session.
    eventCallbacks.get("terminal:output")!({ data: { sessionId: id, data: "stale" } });
    eventCallbacks.get("terminal:exited")!({ data: { sessionId: id, code: 9 } });
    expect(terminalState.sessions[id].running).toBe(true);
    expect(terminalState.sessions[id].output).not.toContain("stale");
  });

  it("G16: failed reconnect remains stopped and exposes the error", async () => {
    const id = await createSession("/path");
    eventCallbacks.get("terminal:exited")!({ data: { sessionId: id, code: 7 } });
    vi.mocked(terminalService.startSession).mockRejectedValueOnce(new Error("shell unavailable"));

    const reconnected = await reconnectSession(id);
    expect(reconnected).toBe(false);
    expect(terminalState.sessions[id].running).toBe(false);
    expect(terminalState.sessions[id].lastError).toBe("shell unavailable");
    expect(terminalState.sessions[id].output).toContain("Terminal reconnect failed");
  });

  it("N-47: terminal:exited event notifies onSessionExit listeners", async () => {
    const id = await createSession("/path");
    let exited = false;
    onSessionExit(id, () => {
      exited = true;
    });
    const cb = eventCallbacks.get("terminal:exited");
    cb!({ data: { sessionId: id } });
    expect(exited).toBe(true);
  });

  it("N-47: runCommandInSession returns -1 when PTY exits mid-command", async () => {
    const id = await createSession("/path");
    // Start the command (returns a promise that resolves on sentinel or exit)
    const promise = runCommandInSession(id, "long-running-cmd", 10000);
    // Simulate PTY exit before the sentinel arrives.
    const exitCb = eventCallbacks.get("terminal:exited");
    exitCb!({ data: { sessionId: id } });
    const exitCode = await promise;
    expect(exitCode).toBe(-1);
  });

  it("N-47: terminal:exited with unknown sessionId does not throw", async () => {
    const cb = eventCallbacks.get("terminal:exited");
    expect(() => cb!({ data: { sessionId: "nonexistent" } })).not.toThrow();
  });

  it("N-47: terminal:exited with missing sessionId does not throw", () => {
    const cb = eventCallbacks.get("terminal:exited");
    expect(() => cb!({ data: {} })).not.toThrow();
  });

  // --- H-12: killSession 清理 listeners，避免 Map 残留 ---

  it("H-12: killSession 后 output listener 不再被触发", async () => {
    const id = await createSession("/path");
    let called = false;
    onTerminalOutput(id, () => {
      called = true;
    });
    await killSession(id);
    // 模拟后端在会话被 kill 后仍推送输出：listener 应已被清理，不触发回调。
    const cb = eventCallbacks.get("terminal:output");
    expect(cb).toBeDefined();
    cb!({ data: { sessionId: id, data: "after-kill" } });
    expect(called).toBe(false);
  });

  it("H-12: killSession 后 exit listener 不再被触发", async () => {
    const id = await createSession("/path");
    let exited = false;
    onSessionExit(id, () => {
      exited = true;
    });
    await killSession(id);
    const cb = eventCallbacks.get("terminal:exited");
    expect(cb).toBeDefined();
    cb!({ data: { sessionId: id } });
    expect(exited).toBe(false);
  });

  // --- M-24: sessionsVersion 浅 watch 通知 ---

  it("M-24: createSession 自增 sessionsVersion", async () => {
    expect(terminalState.sessionsVersion).toBe(0);
    await createSession("/path1");
    expect(terminalState.sessionsVersion).toBe(1);
    await createSession("/path2");
    expect(terminalState.sessionsVersion).toBe(2);
  });

  it("M-24: killSession 自增 sessionsVersion", async () => {
    const id = await createSession("/path");
    expect(terminalState.sessionsVersion).toBe(1);
    await killSession(id);
    expect(terminalState.sessionsVersion).toBe(2);
  });

  it("M-24: terminal:output 事件自增 sessionsVersion（供浅 watch 刷新 xterm）", async () => {
    const id = await createSession("/path");
    expect(terminalState.sessionsVersion).toBe(1);
    const cb = eventCallbacks.get("terminal:output");
    expect(cb).toBeDefined();
    cb!({ data: { sessionId: id, data: "hello\n" } });
    expect(terminalState.sessionsVersion).toBe(2);
    expect(terminalState.sessions[id].output).toBe("hello\n");
  });

  // --- BUG-TERM1: 早期输出（shell 提示符）不丢失 ---

  it("BUG-TERM1: startSession 完成前的 terminal:output 事件被缓存而非丢弃", async () => {
    let resolveStart!: (value: undefined) => void;
    const startPromise = new Promise<undefined>((res) => {
      resolveStart = res;
    });
    vi.mocked(terminalService.startSession).mockReturnValueOnce(startPromise);

    const createPromise = createSession("/path");
    // 乐观注册：RPC 未完成时 session 已存在于 store。
    const id = terminalState.activeSessionId!;
    expect(id).toBeTruthy();
    expect(terminalState.sessions[id]).toBeDefined();

    // 模拟 PTY 启动后立即推送 shell 提示符 —— 事件处理器必须追加到已注册的
    // session（修复前会静默丢弃，终端打开即空白）。
    const cb = eventCallbacks.get("terminal:output");
    expect(cb).toBeDefined();
    cb!({ data: { sessionId: id, data: "PS C:\\> " } });
    expect(terminalState.sessions[id].output).toBe("PS C:\\> ");

    resolveStart(undefined);
    const result = await createPromise;
    expect(result).toBe(id);
    expect(terminalState.sessions[id].output).toBe("PS C:\\> ");
  });

  it("BUG-TERM1: startSession 失败时回滚乐观注册的 session", async () => {
    vi.mocked(terminalService.startSession).mockRejectedValueOnce(
      new Error("boom"),
    );
    const id = await createSession("/path");
    expect(id).toBe("");
    expect(terminalState.sessionOrder).toEqual([]);
    expect(terminalState.activeSessionId).toBeNull();
  });
});
