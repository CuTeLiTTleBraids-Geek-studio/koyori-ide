/**
 * P9-G16 packaged probe: verify the real renderer reconnects a naturally
 * exited PTY through the existing tab without creating a duplicate session.
 */
import { Events } from "@wailsio/runtime";
import { terminalService } from "@/api/services";
import router from "@/router";
import { appState } from "@/stores/app";
import { createSession, killSession, terminalState } from "@/stores/terminal";

const resultEvent = "e2e:g16-terminal-reconnect-result";

interface TerminalReconnectProbeConfig {
  runId: string;
  workspace: string;
  shell: string;
  exitInput: string;
  marker: string;
}

interface TerminalReconnectProbeResult {
  runId: string;
  exitObserved: boolean;
  rawExitEventReceived: boolean;
  rawExitEventData: string;
  exitCode: number | undefined;
  reconnectButtonVisible: boolean;
  reconnectButtonLabel: string;
  sameSessionReused: boolean;
  outputAfterReconnect: boolean;
  rawOutputEventData: string;
  sessionCountBefore: number;
  sessionCountAfter: number;
  ok: boolean;
  error?: string;
}

const sleep = (ms: number): Promise<void> =>
  new Promise((resolve) => setTimeout(resolve, ms));

async function waitFor(
  predicate: () => boolean,
  timeoutMs: number,
  label: string,
): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (predicate()) return;
    await sleep(50);
  }
  throw new Error(`timed out waiting for ${label}`);
}

function reconnectButton(sessionId: string): HTMLButtonElement | null {
  const tab = Array.from(
    document.querySelectorAll<HTMLElement>(".terminal-panel__tab"),
  ).find((candidate) => candidate.dataset.sessionId === sessionId);
  return tab?.querySelector<HTMLButtonElement>(
    ".terminal-panel__tab-reconnect",
  ) ?? null;
}

function terminalSurfaceContains(marker: string): boolean {
  return Array.from(document.querySelectorAll<HTMLElement>(".xterm-rows"))
    .some((surface) => surface.textContent?.includes(marker) ?? false);
}

async function runProbe(
  config: TerminalReconnectProbeConfig,
): Promise<TerminalReconnectProbeResult> {
  let sessionCountBefore = terminalState.sessionOrder.length;
  let sessionId = "";
  let rawExitEventReceived = false;
  let rawExitEventData = "";
  let rawOutputEventData = "";
  const removeRawOutputListener = Events.On("terminal:output", (event) => {
    const data = event?.data ?? {};
    if ((data as { sessionId?: string }).sessionId !== sessionId) return;
    const output = (data as { data?: string }).data ?? "";
    if (output.includes(config.marker)) rawOutputEventData = output;
  });
  const removeRawExitListener = Events.On("terminal:exited", (event) => {
    const data = event?.data ?? {};
    rawExitEventData = JSON.stringify(data) ?? "";
    if ((data as { sessionId?: string }).sessionId === sessionId) {
      rawExitEventReceived = true;
    }
  });
  try {
    // The packaged runner starts on /welcome, whose hideLayout intentionally
    // omits the terminal. Navigate through the real router to the editor
    // before exercising the real reconnect action.
    await router.push("/editor");
    // The packaged runner may reuse persisted window state from an earlier
    // launch. Re-open the real terminal surface, then wait for Vue to mount it
    // before creating the PTY.
    appState.terminalVisible = true;
    appState.bottomPanelView = "terminal";
    await waitFor(
      () => document.querySelector(".terminal-panel") !== null,
      5_000,
      "terminal panel",
    );
    // Allow TerminalPanel's onMounted initFirstSession() to settle so its
    // persisted default session is part of the measured baseline.
    await sleep(1_500);
    sessionCountBefore = terminalState.sessionOrder.length;
    sessionId = await createSession(config.workspace, config.shell);
    if (!sessionId) throw new Error("createSession returned no session id");
    await sleep(1200);
    await terminalService.writeSession(sessionId, config.exitInput);

    await waitFor(
      () => rawExitEventReceived,
      20_000,
      "raw terminal exit event",
    );
    await waitFor(
      () => {
        const session = terminalState.sessions[sessionId];
        return Boolean(session && !session.running && session.exitCode === 7);
      },
      20_000,
      "terminal exit event",
    );
    const ended = terminalState.sessions[sessionId];
    if (!ended) throw new Error("session disappeared after exit");
    const exitCode = ended.exitCode;

    await waitFor(
      () => reconnectButton(sessionId) !== null,
      10_000,
      "reconnect button",
    );
    const button = reconnectButton(sessionId);
    if (!button) throw new Error("reconnect button disappeared");
    const reconnectButtonLabel = button.getAttribute("aria-label") ?? "";
    const reconnectButtonVisible = !button.disabled;
    button.click();

    await waitFor(
      () => {
        const session = terminalState.sessions[sessionId];
        return Boolean(
          session &&
            session.running &&
            !session.reconnecting &&
            session.exitCode === undefined,
        );
      },
      20_000,
      "terminal reconnect",
    );
    await sleep(1200);
    await terminalService.writeSession(sessionId, `echo ${config.marker}\r\n`);
    await waitFor(
      () => rawOutputEventData.includes(config.marker),
      15_000,
      "post-reconnect terminal output event",
    );
    await waitFor(
      () => terminalSurfaceContains(config.marker),
      15_000,
      "post-reconnect terminal surface output",
    );

    const sessionCountAfter = terminalState.sessionOrder.length;
    const sameSessionReused =
      sessionCountAfter === sessionCountBefore + 1 &&
      terminalState.sessionOrder.filter((id) => id === sessionId).length === 1;
    const result: TerminalReconnectProbeResult = {
      runId: config.runId,
      exitObserved: exitCode === 7,
      rawExitEventReceived,
      rawExitEventData,
      exitCode,
      reconnectButtonVisible,
      reconnectButtonLabel,
      sameSessionReused,
      outputAfterReconnect: true,
      rawOutputEventData,
      sessionCountBefore,
      sessionCountAfter,
      ok: reconnectButtonVisible && reconnectButtonLabel.length > 0 && sameSessionReused,
    };
    if (!result.ok) {
      result.error =
        `button=${reconnectButtonVisible} label=${reconnectButtonLabel} ` +
        `sameSession=${sameSessionReused} before=${sessionCountBefore} ` +
        `after=${sessionCountAfter} order=${JSON.stringify(terminalState.sessionOrder)}`;
    }
    return result;
  } catch (error: unknown) {
    return {
      runId: config.runId,
      exitObserved: false,
      rawExitEventReceived,
      rawExitEventData,
      exitCode: undefined,
      reconnectButtonVisible: false,
      reconnectButtonLabel: "",
      sameSessionReused: false,
      outputAfterReconnect: false,
      rawOutputEventData,
      sessionCountBefore,
      sessionCountAfter: terminalState.sessionOrder.length,
      ok: false,
      error: error instanceof Error ? error.message : String(error),
    };
  } finally {
    removeRawExitListener();
    removeRawOutputListener();
    if (sessionId) await killSession(sessionId);
  }
}

export function installTerminalReconnectProbe(): void {
  const target = globalThis as typeof globalThis & {
    __koyoriIdeRunTerminalReconnectProbe?: (
      config: TerminalReconnectProbeConfig,
    ) => Promise<void>;
  };
  target.__koyoriIdeRunTerminalReconnectProbe = async (config) => {
    let result: TerminalReconnectProbeResult;
    try {
      result = await runProbe(config);
    } catch (error: unknown) {
      result = {
        runId: config.runId,
        exitObserved: false,
        rawExitEventReceived: false,
        rawExitEventData: "",
        exitCode: undefined,
        reconnectButtonVisible: false,
        reconnectButtonLabel: "",
        sameSessionReused: false,
        outputAfterReconnect: false,
        rawOutputEventData: "",
        sessionCountBefore: terminalState.sessionOrder.length,
        sessionCountAfter: terminalState.sessionOrder.length,
        ok: false,
        error: error instanceof Error ? error.message : String(error),
      };
    }
    await Events.Emit(resultEvent, result);
  };
}
