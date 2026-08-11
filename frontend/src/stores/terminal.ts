// Koyori IDE 模块 · Terminal；交互服务：内置终端（TerminalService）。
// 喵，这是 Koyori IDE 的 Terminal 模块（前端实现）~
import { reactive } from "vue";
import { terminalService } from "@/api/services";
import { Events } from "@wailsio/runtime";
import type { TerminalOutputPayload, TerminalExitedPayload } from "@/types";

export interface TerminalSessionState {
  id: string;
  output: string;
  running: boolean;
  cols: number;
  rows: number;
  /** Inputs required to recreate the PTY after a natural process exit. */
  workingDir?: string;
  shell?: string;
  reconnecting?: boolean;
  exitCode?: number;
  exitSignal?: string;
  lastError?: string;
}

export interface TerminalStoreState {
  sessions: Record<string, TerminalSessionState>;
  sessionOrder: string[];
  activeSessionId: string | null;
  /**
   * M-24: 会话结构变更版本号。每次会话增删（createSession/killSession）以及
   * 输出追加（terminal:output 事件）时自增，供 TerminalPanel 用浅 watch 监听，
   * 替代原先对 sessions 的深度 watch（深度 watch 在每次输出追加都会遍历整个
   * sessions 对象，输出量大时开销显著）。
   */
  sessionsVersion: number;
}

export const terminalState = reactive<TerminalStoreState>({
  sessions: {},
  sessionOrder: [],
  activeSessionId: null,
  sessionsVersion: 0,
});

let eventListenerRegistered = false;

// Per-session output listeners, used by runCommandInSession to detect
// exit-code sentinels without polling session.output.
type OutputListener = (data: string) => void;
const outputListeners = new Map<string, Set<OutputListener>>();

// N-47: Per-session exit listeners, used by runCommandInSession to detect
// when the backend PTY has exited (so it can return -1 immediately instead
// of waiting for the 5-minute timeout).
type ExitListener = () => void;
const exitListeners = new Map<string, Set<ExitListener>>();

// N-149: Wails Events.On returns a cancel function. Collected here so the
// global listeners can be torn down during HMR / tests to avoid duplicates.
const terminalEventCancellers: Array<() => void> = [];

/**
 * Register a callback that fires whenever terminal output arrives for the
 * given session. Returns an unsubscribe function. Used by
 * runCommandInSession to watch for the exit-code sentinel.
 */
export function onTerminalOutput(sessionId: string, cb: OutputListener): () => void {
  if (!outputListeners.has(sessionId)) {
    outputListeners.set(sessionId, new Set());
  }
  outputListeners.get(sessionId)!.add(cb);
  return () => {
    outputListeners.get(sessionId)?.delete(cb);
  };
}

/**
 * N-47: Register a callback that fires when the backend PTY for the given
 * session exits. Returns an unsubscribe function. Used by
 * runCommandInSession to return promptly when the PTY dies mid-step,
 * instead of waiting for the sentinel that will never arrive.
 */
export function onSessionExit(sessionId: string, cb: ExitListener): () => void {
  if (!exitListeners.has(sessionId)) {
    exitListeners.set(sessionId, new Set());
  }
  exitListeners.get(sessionId)!.add(cb);
  return () => {
    exitListeners.get(sessionId)?.delete(cb);
  };
}

// TODO: move to crossWindowSync.ts — terminal:output / terminal:exited 的
// Events.On 注册应集中到跨窗口同步层，但回调依赖本模块私有
// outputListeners/exitListeners 与 terminalState，迁移需一并抽出，暂留原处。
function ensureEventListener() {
  if (eventListenerRegistered) return;
  eventListenerRegistered = true;
  // N-44: typed event payload (was `any`). Wails' built-in Events.On
  // typing uses a generic string-map for object payloads, so we cast
  // event.data to our specific TerminalOutputPayload shape inside the
  // callback. The payload contract is documented in services/events.go.
  terminalEventCancellers.push(
    Events.On("terminal:output", (event) => {
      const payload = (event?.data ?? {}) as Partial<TerminalOutputPayload>;
      const sessionId = payload.sessionId ?? "";
      const data = payload.data ?? "";
      if (sessionId && typeof data === "string") {
        const session = terminalState.sessions[sessionId];
        if (session) {
          session.output += data;
        }
        // M-24: 自增 sessionsVersion 通知 TerminalPanel 的浅 watch 刷新 xterm，
        // 替代原先对 sessions 的深度 watch（避免每次输出追加都深度遍历）。
        terminalState.sessionsVersion++;
        // Notify registered output listeners.
        const listeners = outputListeners.get(sessionId);
        if (listeners) {
          for (const cb of listeners) cb(data);
        }
      }
    }),
  );
  // N-47: Listen for terminal:exited events emitted by the backend when
  // the PTY process exits. Marks the session as not running and notifies
  // any registered exit listeners (e.g. runCommandInSession waiting for
  // a sentinel that will never arrive).
  // BUG4c: 向会话输出追加一条可见提示，让用户知道该终端已停止。原先仅静默
  //   设置 running=false，导致用户继续键入时 writeToSession 静默返回，输入
  //   像被吞掉一样，体验上表现为"命令都无法使用"。
  // N-44: typed event payload (was `any`).
  terminalEventCancellers.push(
    Events.On("terminal:exited", (event) => {
      const payload = (event?.data ?? {}) as Partial<TerminalExitedPayload>;
      const sessionId = payload.sessionId ?? "";
      if (!sessionId) return;
      const session = terminalState.sessions[sessionId];
      if (session) {
        session.running = false;
        session.reconnecting = false;
        session.exitCode = typeof payload.code === "number" ? payload.code : undefined;
        session.exitSignal = payload.signal || undefined;
        session.lastError = payload.err || undefined;
        // BUG4c: 追加可见退出提示。使用 \r\n 以兼容 Windows/Unix 换行，
        //   颜色转义（ANSI 红）让提示更显眼。提示包含错误信息（若后端提供）
        //   和新建终端的引导。原先仅静默设置 running=false，用户继续键入时
        //   writeToSession 静默返回，输入像被吞掉一样。
        const codePart = typeof payload.code === "number" && payload.code >= 0
          ? ` (exit code ${payload.code})`
          : payload.signal
            ? ` (signal ${payload.signal})`
            : "";
        const errPart = payload.err && payload.err !== "EOF" ? `: ${payload.err}` : "";
        const hint = `\r\n\x1b[31m[Terminal session ended${codePart}${errPart}. This terminal is no longer accepting input. Use the reconnect button or click + to open a new terminal.]\x1b[0m\r\n`;
        session.output += hint;
        // M-24: 自增 sessionsVersion 让 TerminalPanel 的浅 watch 把提示写入 xterm。
        terminalState.sessionsVersion++;
      }
      const listeners = exitListeners.get(sessionId);
      if (listeners) {
        for (const cb of listeners) cb();
      }
    }),
  );
}

/**
 * N-149: Cancels all terminal event listeners. Intended for HMR teardown
 * in dev and test cleanup. After calling this, ensureEventListener() can
 * be invoked again to re-register fresh listeners.
 */
export function cleanupTerminalEventListeners(): void {
  for (const cancel of terminalEventCancellers) {
    try {
      cancel();
    } catch {
      // ignore — listener already removed
    }
  }
  terminalEventCancellers.length = 0;
  eventListenerRegistered = false;
}

let sessionSequence = 0;

function generateSessionId(): string {
  const sequence = (sessionSequence++).toString(36);
  let entropy: string;

  if (typeof globalThis.crypto?.randomUUID === "function") {
    entropy = globalThis.crypto.randomUUID();
  } else {
    const values = new Uint32Array(2);
    if (typeof globalThis.crypto?.getRandomValues === "function") {
      globalThis.crypto.getRandomValues(values);
    } else {
      values[0] = Math.floor(Math.random() * 0x100000000);
      values[1] = Math.floor(Math.random() * 0x100000000);
    }
    entropy = Array.from(values, (value) => value.toString(36)).join("");
  }

  // The sequence is a deterministic in-process collision guard. It also
  // protects the fallback path when a test or an older webview repeats time
  // and random values.
  return `term-${Date.now().toString(36)}-${entropy}-${sequence}`;
}

export async function createSession(
  workingDir: string,
  shell: string = "",
): Promise<string> {
  ensureEventListener();
  const id = generateSessionId();
  // BUG-TERM1: 先注册 session 再调用后端 RPC。后端 PTY 在 startSession 返回
  // 前就可能开始输出（shell 提示符、banner 等），terminal:output 事件处理器
  // 只追加到已存在的 session —— 若注册发生在 await 之后，这些早期输出会被
  // 静默丢弃，终端打开后是空白。现在事件在注册后到达，输出会正确缓存，
  // 由 TerminalPanel 初始化 xterm 时回放。
  terminalState.sessions[id] = {
    id,
    output: "",
    running: true,
    cols: 80,
    rows: 24,
    workingDir,
    shell,
  };
  terminalState.sessionOrder.push(id);
  terminalState.activeSessionId = id;
  // M-24: 通知浅 watch 会话结构已变更。
  terminalState.sessionsVersion++;
  try {
    await terminalService.startSession(id, workingDir, shell);
    return id;
  } catch (e) {
    console.error("Failed to create terminal session:", e);
    // 回滚乐观注册的会话，避免残留半初始化状态。
    delete terminalState.sessions[id];
    terminalState.sessionOrder = terminalState.sessionOrder.filter(
      (x) => x !== id,
    );
    if (terminalState.activeSessionId === id) {
      terminalState.activeSessionId = terminalState.sessionOrder[0] ?? null;
    }
    // M-24: 通知浅 watch 会话结构已变更。
    terminalState.sessionsVersion++;
    return "";
  }
}

/**
 * Recreate a naturally exited PTY while retaining the session tab and its
 * output history. Explicitly closed sessions are removed by killSession and
 * therefore cannot be restarted through this path.
 */
export async function reconnectSession(sessionId: string): Promise<boolean> {
  const session = terminalState.sessions[sessionId];
  if (!session || session.running || session.reconnecting) return false;

  session.reconnecting = true;
  session.lastError = undefined;
  terminalState.sessionsVersion++;
  try {
    await terminalService.startSession(
      sessionId,
      session.workingDir ?? "",
      session.shell ?? "",
    );
    session.running = true;
    session.reconnecting = false;
    session.exitCode = undefined;
    session.exitSignal = undefined;
    session.lastError = undefined;
    terminalState.sessionsVersion++;
    return true;
  } catch (error) {
    session.running = false;
    session.reconnecting = false;
    session.lastError = error instanceof Error ? error.message : String(error);
    session.output += `\r\n\x1b[31m[Terminal reconnect failed: ${session.lastError}]\x1b[0m\r\n`;
    terminalState.sessionsVersion++;
    return false;
  }
}

export async function writeToSession(
  sessionId: string,
  input: string,
): Promise<void> {
  const session = terminalState.sessions[sessionId];
  if (!session || !session.running) return;
  try {
    await terminalService.writeSession(sessionId, input);
  } catch (e) {
    console.error("Failed to write to terminal:", e);
  }
}

export async function killSession(sessionId: string): Promise<void> {
  try {
    await terminalService.killSession(sessionId);
  } catch (e) {
    console.error("Failed to kill terminal:", e);
  }
  delete terminalState.sessions[sessionId];
  terminalState.sessionOrder = terminalState.sessionOrder.filter(
    (id) => id !== sessionId,
  );
  if (terminalState.activeSessionId === sessionId) {
    terminalState.activeSessionId = terminalState.sessionOrder[0] ?? null;
  }
  // M-24: 通知浅 watch 会话结构已变更。
  terminalState.sessionsVersion++;
  // H-12: 清理已终止会话的 listener 引用，避免 outputListeners / exitListeners
  // 的 Map 中残留已终止会话的回调，造成内存泄漏与对已销毁会话的误通知。
  outputListeners.delete(sessionId);
  exitListeners.delete(sessionId);
}

export async function resizeSession(
  sessionId: string,
  cols: number,
  rows: number,
): Promise<void> {
  const session = terminalState.sessions[sessionId];
  if (!session) return;
  session.cols = cols;
  session.rows = rows;
  if (!session.running) return;
  try {
    await terminalService.resizeSession(sessionId, cols, rows);
  } catch (e) {
    console.error("Failed to resize terminal:", e);
  }
}

export function setActiveSession(sessionId: string): void {
  terminalState.activeSessionId = sessionId;
}

export function getActiveSession(): TerminalSessionState | null {
  if (!terminalState.activeSessionId) return null;
  return terminalState.sessions[terminalState.activeSessionId] ?? null;
}

export function clearSessionOutput(sessionId: string): void {
  const session = terminalState.sessions[sessionId];
  if (session) {
    session.output = "";
  }
}

// ---------------------------------------------------------------------------
// Exit-code detection (Plan 61 / N-24)
// ---------------------------------------------------------------------------

const EXIT_SENTINEL_PREFIX = "__NKNK_EXIT_";

/**
 * Returns true if the current platform is Windows. Used to select the
 * correct shell variable for exit-code capture ($LASTEXITCODE on
 * PowerShell, $? on bash).
 */
function isWindowsPlatform(): boolean {
  if (typeof navigator !== "undefined" && navigator.platform) {
    return navigator.platform.indexOf("Win") >= 0;
  }
  return false;
}

/**
 * Wraps a shell command with a sentinel echo that reports the exit code.
 * The sentinel format is: __NKNK_EXIT_<marker>_<code>__
 *
 * On Windows (PowerShell), $LASTEXITCODE captures the exit code of native
 * commands. For cmdlets where $LASTEXITCODE is null, we fall back to $?
 * (True→0, False→1).
 *
 * On Unix (bash/sh), $? captures the exit code directly.
 */
export function wrapCommandWithExitMarker(command: string, marker: string): string {
  const sentinel = `${EXIT_SENTINEL_PREFIX}${marker}_`;
  if (isWindowsPlatform()) {
    return `${command}; $c = $LASTEXITCODE; if ($c -eq $null) { $c = if ($?) { 0 } else { 1 } }; echo "${sentinel}$c__"`;
  }
  return `${command}; echo "${sentinel}$?__"`;
}

/**
 * Searches a buffer for the exit-code sentinel and returns the parsed
 * exit code, or null if the sentinel hasn't appeared yet.
 */
export function extractExitCode(buffer: string, marker: string): number | null {
  const regex = new RegExp(`${EXIT_SENTINEL_PREFIX}${marker}_(\\d+)__`);
  const match = buffer.match(regex);
  if (match) {
    return parseInt(match[1], 10);
  }
  return null;
}

/**
 * Runs a command in a terminal session and waits for the exit-code
 * sentinel. Returns the exit code (0 = success), or -1 if the session
 * is not running or the command times out.
 *
 * The command is wrapped with a unique sentinel echo so the exit code
 * can be detected from the terminal output stream. This replaces the
 * old "dispatch and mark success" pattern (N-24).
 */
export async function runCommandInSession(
  sessionId: string,
  command: string,
  timeoutMs: number = 300000,
): Promise<number> {
  const session = terminalState.sessions[sessionId];
  if (!session || !session.running) {
    return -1;
  }

  const marker = Math.random().toString(36).slice(2, 10);
  const wrapped = wrapCommandWithExitMarker(command, marker);

  return new Promise<number>((resolve) => {
    let resolved = false;
    let buffer = "";

    const off = onTerminalOutput(sessionId, (data) => {
      if (resolved) return;
      buffer += data;
      const code = extractExitCode(buffer, marker);
      if (code !== null) {
        resolved = true;
        cleanup();
        resolve(code);
      }
    });

    // N-47: If the PTY exits before the sentinel arrives, resolve
    // immediately with -1 instead of waiting for the 5-minute timeout.
    const offExit = onSessionExit(sessionId, () => {
      if (resolved) return;
      resolved = true;
      cleanup();
      resolve(-1);
    });

    const timer = setTimeout(() => {
      if (!resolved) {
        resolved = true;
        cleanup();
        resolve(-1);
      }
    }, timeoutMs);

    function cleanup() {
      off();
      offExit();
      clearTimeout(timer);
    }

    void writeToSession(sessionId, wrapped + "\n");
  });
}

/**
 * Proposal F (prompt-5.md): Run a command in a terminal session and
 * capture both the exit code AND the stdout/stderr output. The output
 * is the raw terminal stream with the sentinel marker line stripped.
 *
 * Used by workflow steps that declare `outputs` templates — the host
 * parses the captured stdout to extract values like version strings,
 * commit hashes, etc.
 *
 * Returns { exitCode, output }. On timeout, exitCode is -1 and output
 * contains whatever was captured before the timeout.
 */
export async function runCommandInSessionCapturing(
  sessionId: string,
  command: string,
  timeoutMs: number = 300000,
): Promise<{ exitCode: number; output: string }> {
  const session = terminalState.sessions[sessionId];
  if (!session || !session.running) {
    return { exitCode: -1, output: "" };
  }

  const marker = Math.random().toString(36).slice(2, 10);
  const wrapped = wrapCommandWithExitMarker(command, marker);

  return new Promise<{ exitCode: number; output: string }>((resolve) => {
    let resolved = false;
    let buffer = "";

    const off = onTerminalOutput(sessionId, (data) => {
      if (resolved) return;
      buffer += data;
      const code = extractExitCode(buffer, marker);
      if (code !== null) {
        resolved = true;
        cleanup();
        const cleaned = stripExitMarker(buffer, marker);
        resolve({ exitCode: code, output: cleaned });
      }
    });

    // N-47: If the PTY exits before the sentinel arrives, resolve
    // immediately with -1 instead of waiting for the 5-minute timeout.
    const offExit = onSessionExit(sessionId, () => {
      if (resolved) return;
      resolved = true;
      cleanup();
      resolve({ exitCode: -1, output: stripExitMarker(buffer, marker) });
    });

    const timer = setTimeout(() => {
      if (!resolved) {
        resolved = true;
        cleanup();
        resolve({ exitCode: -1, output: stripExitMarker(buffer, marker) });
      }
    }, timeoutMs);

    function cleanup() {
      off();
      offExit();
      clearTimeout(timer);
    }

    void writeToSession(sessionId, wrapped + "\n");
  });
}

/**
 * Proposal F: Strip the sentinel marker line (and anything after it)
 * from the captured terminal output. The marker is the random string
 * inserted by wrapCommandWithExitMarker to delimit the exit code.
 */
function stripExitMarker(buffer: string, marker: string): string {
  const markerIdx = buffer.indexOf(`__NKNK_EXIT_${marker}`);
  if (markerIdx === -1) return buffer;
  return buffer.slice(0, markerIdx);
}
