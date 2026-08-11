// Koyori IDE 模块 · Connectivity。
// 喵，这是 Koyori IDE 的 Connectivity 模块（前端实现）~
import { reactive } from "vue";
import { appState } from "@/stores/app";

/**
 * G-FEAT-02: Offline detection.
 *
 * Tracks network connectivity so the UI can:
 *   - Show a "离线补全" (offline completion) badge in the status bar
 *   - Disable the AI send button when offline
 *   - Let LSP-based completion keep working offline
 *
 * Signals:
 *   1. navigator.onLine + window online/offline events (primary, instant)
 *   2. Periodic heartbeat to the AI BaseURL (best-effort reachability check)
 *
 * The heartbeat uses a no-cors fetch with a short timeout. If the fetch fails
 * (network error, CSP block, or timeout), the online state falls back to
 * navigator.onLine. This never throws — it only updates connectivityState.
 */

export interface ConnectivityState {
  /** Whether the network is online (navigator.onLine + heartbeat). */
  online: boolean;
  /** Whether the AI BaseURL responded to the last heartbeat. */
  aiReachable: boolean;
  /** True while a heartbeat probe is in flight. */
  checking: boolean;
}

export const connectivityState = reactive<ConnectivityState>({
  online: typeof navigator !== "undefined" ? navigator.onLine : true,
  aiReachable: true,
  checking: false,
});

/** Heartbeat interval (ms). 30s balances responsiveness with resource use. */
const HEARTBEAT_INTERVAL_MS = 30_000;
/** Heartbeat request timeout (ms). */
const HEARTBEAT_TIMEOUT_MS = 5_000;

let heartbeatTimer: ReturnType<typeof setInterval> | null = null;
let onlineListener: (() => void) | null = null;
let offlineListener: (() => void) | null = null;
let initialised = false;
const activeHeartbeatControllers = new Set<AbortController>();
/**
 * M-19: Monotonic sequence number for refreshOnlineState. Each call captures
 * its own seq; when the async probe resolves, if the counter has advanced
 * past the call's seq, the (stale) result is discarded. This prevents a
 * slow earlier heartbeat from overwriting the result of a faster later one.
 */
let refreshSeq = 0;

function abortActiveHeartbeats(): void {
  for (const controller of activeHeartbeatControllers) {
    controller.abort();
  }
  activeHeartbeatControllers.clear();
  connectivityState.checking = false;
}

/**
 * Probe the AI BaseURL for reachability. Uses no-cors mode so the request
 * succeeds (opaque response) if the server is reachable, regardless of auth.
 * Returns true if reachable, false otherwise. Never throws.
 */
export async function checkAIReachable(): Promise<boolean> {
  const baseUrl = appState.aiBaseUrl;
  if (!baseUrl) return false;
  connectivityState.checking = true;
  const controller = new AbortController();
  activeHeartbeatControllers.add(controller);
  const timer = setTimeout(() => controller.abort(), HEARTBEAT_TIMEOUT_MS);
  try {
    // no-cors: the response is opaque but the promise resolves if the server
    // is reachable. A network error or CSP block rejects the promise.
    await fetch(baseUrl, {
      method: "HEAD",
      mode: "no-cors",
      cache: "no-store",
      signal: controller.signal,
    });
    return true;
  } catch {
    // AbortError, network error, or CSP block — server not reachable from
    // the webview. Fall back to navigator.onLine for the online state.
    return false;
  } finally {
    clearTimeout(timer);
    activeHeartbeatControllers.delete(controller);
    connectivityState.checking = activeHeartbeatControllers.size > 0;
  }
}

/**
 * Update the online state from navigator.onLine and a heartbeat probe.
 * Called on init, on online/offline events, and on each heartbeat tick.
 */
async function refreshOnlineState(): Promise<void> {
  // M-19: Stamp this call with the current sequence number so a slow,
  // out-of-order resolution can be detected and discarded.
  const mySeq = ++refreshSeq;
  const navOnline = typeof navigator !== "undefined" ? navigator.onLine : true;
  // If the browser reports offline, we're definitely offline.
  if (!navOnline) {
    // Synchronous path — no later call can race here, but still guard for
    // consistency. (refreshSeq may have advanced during any awaits above,
    // though there are none in this branch.)
    if (mySeq !== refreshSeq) return;
    connectivityState.online = false;
    connectivityState.aiReachable = false;
    return;
  }
  // Browser reports online — probe the AI BaseURL for a more precise signal.
  // If no BaseURL is configured, trust navigator.onLine.
  if (!appState.aiBaseUrl) {
    if (mySeq !== refreshSeq) return;
    connectivityState.online = true;
    connectivityState.aiReachable = true;
    return;
  }
  const reachable = await checkAIReachable();
  // M-19: If a newer refreshOnlineState() call has started while we were
  // awaiting the heartbeat, discard this stale result — the newer call's
  // outcome should win.
  if (mySeq !== refreshSeq) return;
  connectivityState.aiReachable = reachable;
  // Stay online if either navigator says online OR the heartbeat succeeded.
  // We use navigator.onLine as the authority for the `online` flag so that
  // a single failed heartbeat doesn't falsely show "offline" when the network
  // is actually up (the AI server might just be slow or behind a firewall).
  connectivityState.online = navOnline;
}

/**
 * Initialize the connectivity listener. Sets up online/offline event
 * listeners and starts the periodic heartbeat. Idempotent — safe to call
 * multiple times (subsequent calls are no-ops).
 *
 * Call once during app bootstrap (after loadSettings so aiBaseUrl is set).
 */
export function initConnectivityListener(): void {
  if (initialised) return;
  initialised = true;
  if (typeof window === "undefined") return;

  const handleOnline = () => {
    void refreshOnlineState();
  };
  const handleOffline = () => {
    refreshSeq += 1;
    abortActiveHeartbeats();
    connectivityState.online = false;
    connectivityState.aiReachable = false;
  };

  window.addEventListener("online", handleOnline);
  window.addEventListener("offline", handleOffline);
  onlineListener = handleOnline;
  offlineListener = handleOffline;

  // Start the periodic heartbeat.
  heartbeatTimer = setInterval(() => {
    void refreshOnlineState();
  }, HEARTBEAT_INTERVAL_MS);

  // Do an initial check.
  void refreshOnlineState();
}

/**
 * Unregister the connectivity listener — alias for stopConnectivityListener.
 * Removes the online/offline window event listeners and clears the heartbeat
 * timer so the listener no longer receives connectivity events.
 */
export function unregisterConnectivityListener(): void {
  stopConnectivityListener();
}

/**
 * Stop the connectivity listener and clean up event listeners + timer.
 * Intended for HMR teardown in dev and test cleanup.
 */
export function stopConnectivityListener(): void {
  // Invalidate any refresh already awaiting a heartbeat and abort the actual
  // fetch so an old runtime cannot update state after teardown.
  refreshSeq += 1;
  abortActiveHeartbeats();
  if (onlineListener && typeof window !== "undefined") {
    window.removeEventListener("online", onlineListener);
    onlineListener = null;
  }
  if (offlineListener && typeof window !== "undefined") {
    window.removeEventListener("offline", offlineListener);
    offlineListener = null;
  }
  if (heartbeatTimer) {
    clearInterval(heartbeatTimer);
    heartbeatTimer = null;
  }
  initialised = false;
}

import.meta.hot?.dispose(stopConnectivityListener);

/**
 * Test-only helper: reset the connectivity state and initialisation flag.
 */
export function __resetConnectivityForTesting(): void {
  stopConnectivityListener();
  connectivityState.online = typeof navigator !== "undefined" ? navigator.onLine : true;
  connectivityState.aiReachable = true;
  connectivityState.checking = false;
  // M-19: Reset the sequence counter so tests start from a known state.
  refreshSeq = 0;
}

/**
 * M-19: Test-only export of refreshOnlineState so the sequence-number race
 * guard can be exercised directly with controlled fetch timing (firing two
 * calls back-to-back and resolving them out of order).
 */
export function __refreshOnlineStateForTesting(): Promise<void> {
  return refreshOnlineState();
}
