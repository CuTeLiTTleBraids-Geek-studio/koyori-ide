// Koyori IDE 模块 · Inline Completion；交互服务：AI 对话（AIService）。
// 喵，这是 Koyori IDE 的 Inline Completion 模块（前端实现）~
import { computed, ref } from "vue";
import { appState, saveSettings } from "@/stores/app";
import { aiService } from "@/api/services";
import { aiState } from "@/stores/ai";
import { connectivityState } from "@/lib/connectivity";

/**
 * Minimum milliseconds between completion requests per file (N-43).
 * Per-file debounce prevents A tab's request from blocking B tab's request.
 *
 * G-PERF-02: this debounce (combined with the AbortController below) is the
 * performance gate for inline completions. Debounce caps request frequency
 * while typing, and aborting the previous in-flight request avoids wasted
 * work (and wasted AI tokens) when the user keeps typing. Both must remain
 * in place; removing either re-introduces a per-keystroke request storm.
 */
const DEBOUNCE_MS = 300;

/** Minimum prefix length before requesting a completion. */
const MIN_PREFIX_LENGTH = 10;

/**
 * Whether inline completion is enabled. Bound to appState so the setting is
 * persisted (N-7). Use this in templates/computed for reactivity.
 */
export const inlineCompletionEnabled = computed(() => appState.inlineCompletionEnabled);

/** Visible degradation: offline / unreachable provider must not leave ghost text. */
export const inlineCompletionUnavailable = computed(
  () => !connectivityState.online || !connectivityState.aiReachable || inlineCompletionFailed.value,
);

const inlineCompletionFailed = ref(false);

/**
 * N-43: Per-file last-request timestamps for debounce. Keys are filePaths,
 * values are epoch milliseconds. Using a Map (instead of a single global
 * timestamp) prevents A tab's request from blocking B tab's request when
 * the user is editing multiple files.
 */
const lastRequestByFile = new Map<string, number>();

/**
 * N-43: The currently in-flight completion request's promise + abort
 * controller. Concurrent callers share this promise so we don't fire
 * duplicate HTTP requests for the same (prefix, suffix, language, filePath).
 * When a new request arrives, the previous AbortController is aborted,
 * cancelling the old Wails binding call.
 */
interface InFlight {
  promise: Promise<string>;
  controller: AbortController | null;
  // Signature used to dedup: if a concurrent call has the same signature,
  // it reuses the in-flight promise instead of starting a new request.
  signature: string;
  owners: Map<InlineCompletionOwner, { active: boolean }>;
}

export type InlineCompletionOwner = object | symbol;

const legacyOwner = Symbol("inline-completion-legacy-owner");
const inFlightBySignature = new Map<string, InFlight>();
const signaturesByOwner = new Map<InlineCompletionOwner, Set<string>>();

/**
 * N-43: Abort the in-flight request (if any) and clear it. Called when a
 * new request starts so the old request's result is discarded (preventing
 * stale ghost text from flashing in after the user has typed more).
 */
function attachOwner(
  entry: InFlight,
  owner: InlineCompletionOwner,
): { active: boolean } {
  const lease = entry.owners.get(owner) ?? { active: true };
  entry.owners.set(owner, lease);
  const signatures = signaturesByOwner.get(owner) ?? new Set<string>();
  signatures.add(entry.signature);
  signaturesByOwner.set(owner, signatures);
  return lease;
}

function completionForOwner(
  entry: InFlight,
  owner: InlineCompletionOwner,
): Promise<string> {
  const lease = attachOwner(entry, owner);
  return entry.promise.then((result) => (lease.active ? result : ""));
}

function removeEntry(entry: InFlight, abort: boolean): void {
  if (inFlightBySignature.get(entry.signature) === entry) {
    inFlightBySignature.delete(entry.signature);
  }
  for (const [owner, lease] of entry.owners) {
    if (abort) lease.active = false;
    const signatures = signaturesByOwner.get(owner);
    signatures?.delete(entry.signature);
    if (signatures?.size === 0) signaturesByOwner.delete(owner);
  }
  entry.owners.clear();
  if (abort) entry.controller?.abort();
}

function releaseOwner(owner: InlineCompletionOwner): void {
  const signatures = signaturesByOwner.get(owner);
  if (!signatures) return;
  for (const signature of Array.from(signatures)) {
    const entry = inFlightBySignature.get(signature);
    if (!entry) continue;
    const lease = entry.owners.get(owner);
    if (lease) lease.active = false;
    entry.owners.delete(owner);
    if (entry.owners.size === 0) removeEntry(entry, true);
  }
  signaturesByOwner.delete(owner);
}

function abortAllInFlight(): void {
  for (const entry of Array.from(inFlightBySignature.values())) {
    removeEntry(entry, true);
  }
  signaturesByOwner.clear();
}

/**
 * Request an inline completion from the AI service.
 * Returns the completion text or empty string if no completion is available.
 *
 * N-43 (Proposal M):
 * - Per-file debounce: lastRequestByFile tracks the last request time per
 *   filePath, so editing file A doesn't block requests for file B.
 * - AbortController: each new request aborts the previous one, preventing
 *   stale ghost text from flashing in after the user types more.
 * - Dedup: concurrent callers with the same (prefix, suffix, language,
 *   filePath) signature reuse the in-flight promise, avoiding duplicate
 *   HTTP requests.
 */
export async function requestCompletion(
  prefix: string,
  suffix: string,
  language: string,
  filePath: string,
  owner?: InlineCompletionOwner,
): Promise<string> {
  if (!appState.inlineCompletionEnabled) return "";
  if (prefix.length < MIN_PREFIX_LENGTH) return "";
  // prompt-6 Task 8 / BUG-M9: do not compete with main chat stream for quota.
  if (aiState.streaming || aiState.globalStreamBusy) return "";
  if (!connectivityState.online || !connectivityState.aiReachable) {
    inlineCompletionFailed.value = false;
    return "";
  }

  // N-43: Dedup — check BEFORE debounce. If a concurrent caller already
  // started the same request, reuse its promise regardless of the debounce
  // window. This is safe because reusing an in-flight promise costs nothing
  // (no new HTTP request). This must come before the debounce check so
  // that rapid re-render passes (which fire requestCompletion for the same
  // position) get the in-flight result instead of "".
  const signature = `${filePath}\0${prefix}\0${suffix}\0${language}`;
  const ownerKey = owner ?? legacyOwner;
  const existing = inFlightBySignature.get(signature);
  if (existing) {
    return completionForOwner(existing, ownerKey);
  }

  // A changed signature supersedes this editor's previous request even when
  // the replacement is suppressed by the per-file debounce below. Releasing
  // first prevents the old response from surfacing stale ghost text.
  if (owner) releaseOwner(ownerKey);

  // N-43: Per-file debounce. Check AFTER dedup so that an in-flight request
  // with the same signature is reused even within the debounce window.
  const now = Date.now();
  const last = lastRequestByFile.get(filePath) ?? 0;
  if (now - last < DEBOUNCE_MS) return "";
  lastRequestByFile.set(filePath, now);

  // A new request only supersedes work owned by the same editor instance.
  // Legacy callers without an owner retain the original global-cancel behavior.
  if (!owner) {
    abortAllInFlight();
  }

  const controller = new AbortController();
  let completionRequest: ReturnType<typeof aiService.complete>;
  try {
    completionRequest = aiService.complete(
      { prefix, suffix, language, filePath },
      controller.signal,
    );
  } catch {
    return "";
  }
  const promise = (async () => {
    try {
      const response = await completionRequest;
      const text = response?.text ?? "";
      if (!text.trim()) {
        inlineCompletionFailed.value = true;
        return "";
      }
      inlineCompletionFailed.value = false;
      return text;
    } catch (error: unknown) {
      const aborted = error instanceof DOMException && error.name === "AbortError";
      if (!aborted) inlineCompletionFailed.value = true;
      return "";
    } finally {
      const current = inFlightBySignature.get(signature);
      if (current?.controller === controller) removeEntry(current, false);
    }
})();

  const entry: InFlight = {
    promise,
    controller,
    signature,
    owners: new Map<InlineCompletionOwner, { active: boolean }>(),
  };
  inFlightBySignature.set(signature, entry);
  return completionForOwner(entry, ownerKey);
}

/**
 * Toggle inline completion on/off and persist the setting (N-7).
 */
export function toggleInlineCompletion(): void {
  appState.inlineCompletionEnabled = !appState.inlineCompletionEnabled;
  saveSettings();
}

/**
 * N-43: Cancel any in-flight completion request. Called when the user
 * explicitly dismisses ghost text or switches files, so the pending
 * HTTP request is aborted rather than completing silently.
 */
export function cancelInlineCompletion(owner?: InlineCompletionOwner): void {
  if (owner) {
    releaseOwner(owner);
  } else {
    abortAllInFlight();
  }
}

/**
 * Release all module-level inline completion state. This is used by the
 * frontend runtime teardown so HMR/window shutdown cannot leave an AI request
 * running or carry per-file debounce timestamps into the successor runtime.
 */
export function cleanupInlineCompletion(): void {
  lastRequestByFile.clear();
  abortAllInFlight();
  inlineCompletionFailed.value = false;
}

import.meta.hot?.dispose(cleanupInlineCompletion);

/** Backward-compatible test helper. */
export function __resetInlineCompletionForTesting(): void {
  cleanupInlineCompletion();
}
