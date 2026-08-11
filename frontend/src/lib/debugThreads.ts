// Koyori IDE 模块 · Debug Threads。
// 喵，这是 Koyori IDE 的 Debug Threads 模块（前端实现）~
import { computed, reactive } from "vue";
import { Events } from "@wailsio/runtime";

export type DebugThreadState = "running" | "stopped" | "stepping";
export type DebugStepType = "next" | "in" | "out";

export interface DebugStackFrame {
  id: number;
  name: string;
  source: string;
  file: string;
  line: number;
  column: number;
  endLine?: number;
  endColumn?: number;
  module?: string;
  presentationHint?: string;
  asyncBoundary?: boolean;
}

export interface DebugThreadInfo {
  id: number;
  name: string;
  state: DebugThreadState;
  frames: DebugStackFrame[];
  selected: boolean;
}

export interface DebugThreadsUpdatedEvent {
  sessionId: string;
  threads: DebugThreadInfo[];
  allThreadsStopped: boolean;
}

export interface DebugThreadSelectedEvent {
  sessionId: string;
  threadId: number;
}

export interface DebugThreadStoppedEvent {
  sessionId: string;
  threadId: number;
  reason?: string;
  allThreadsStopped: boolean;
}

export interface DebugThreadsServiceBindings {
  ListThreads(sessionId: string): Promise<DebugThreadInfo[]>;
  GetThreadStackTrace(
    sessionId: string,
    threadId: number,
    startFrame: number,
    levels: number,
  ): Promise<DebugStackFrame[]>;
  SelectThread(sessionId: string, threadId: number): Promise<void>;
  ContinueThread(sessionId: string, threadId: number): Promise<void>;
  ContinueAllThreads(sessionId: string): Promise<void>;
  PauseAllThreads(sessionId: string): Promise<void>;
  StepThread(sessionId: string, threadId: number, stepType: DebugStepType): Promise<void>;
}

export interface DebugThreadsStoreState {
  sessionId: string;
  threads: DebugThreadInfo[];
  selected: number | null;
  expanded: Set<number>;
  loading: boolean;
  loadingStacks: Set<number>;
  actionLoading: Set<number>;
  bulkActionLoading: boolean;
  error: string | null;
  allThreadsStopped: boolean;
}

export const debugThreadsState = reactive<DebugThreadsStoreState>({
  sessionId: "",
  threads: [],
  selected: null,
  expanded: new Set<number>(),
  loading: false,
  loadingStacks: new Set<number>(),
  actionLoading: new Set<number>(),
  bulkActionLoading: false,
  error: null,
  allThreadsStopped: false,
});

export const selectedDebugThread = computed<DebugThreadInfo | null>(() =>
  debugThreadsState.threads.find((thread) => thread.id === debugThreadsState.selected) ?? null,
);

const debugThreadsUnavailable = (): Promise<never> => Promise.reject(new Error(
  "Debug thread controls are unavailable because no generated Wails binding is registered",
));

const defaultBindings: DebugThreadsServiceBindings = {
  ListThreads: debugThreadsUnavailable,
  GetThreadStackTrace: debugThreadsUnavailable,
  SelectThread: debugThreadsUnavailable,
  ContinueThread: debugThreadsUnavailable,
  ContinueAllThreads: debugThreadsUnavailable,
  PauseAllThreads: debugThreadsUnavailable,
  StepThread: debugThreadsUnavailable,
};

let bindings: DebugThreadsServiceBindings = defaultBindings;
let listSequence = 0;
let stackSequence = 0;
let actionSequence = 0;
let bulkActionSequence = 0;
let stateRevision = 0;
let stackPaginationRevision = 0;
const stackRequests = new Map<string, number>();
const actionRequests = new Map<string, number>();
interface PendingStackPage {
  startFrame: number;
  levels: number;
  frames: DebugStackFrame[];
  sequence: number;
}
const pendingStackPages = new Map<string, Map<number, PendingStackPage>>();
let eventUnsubscribers: Array<() => void> = [];
let eventsActive = false;
let activationLeaseCount = 0;
let activationLeasesOwnEvents = false;

export function setDebugThreadsServiceBindings(
  value: DebugThreadsServiceBindings | null,
): void {
  bindings = value ?? defaultBindings;
}

export const __setDebugThreadsServiceBindingsForTesting =
  setDebugThreadsServiceBindings;

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function numberValue(value: unknown, fallback = 0): number {
  return typeof value === "number" && Number.isFinite(value) ? value : fallback;
}

function stringValue(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function normalizeThreadState(value: unknown): DebugThreadState {
  return value === "stopped" || value === "stepping" ? value : "running";
}

function normalizeFrame(value: unknown): DebugStackFrame | null {
  if (!isRecord(value)) return null;
  const id = numberValue(value.id);
  if (!Number.isInteger(id) || id <= 0) return null;
  const source = stringValue(value.source) || stringValue(value.file);
  return {
    id,
    name: stringValue(value.name),
    source,
    file: stringValue(value.file) || source,
    line: numberValue(value.line),
    column: numberValue(value.column),
    endLine: numberValue(value.endLine) || undefined,
    endColumn: numberValue(value.endColumn) || undefined,
    module: stringValue(value.module) || undefined,
    presentationHint: stringValue(value.presentationHint) || undefined,
    asyncBoundary: value.asyncBoundary === true || undefined,
  };
}

function normalizeFrames(value: unknown): DebugStackFrame[] {
  if (!Array.isArray(value)) return [];
  const seen = new Set<number>();
  const frames: DebugStackFrame[] = [];
  for (const candidate of value) {
    const frame = normalizeFrame(candidate);
    if (!frame || seen.has(frame.id)) continue;
    seen.add(frame.id);
    frames.push(frame);
  }
  return frames;
}

function normalizeThread(value: unknown): DebugThreadInfo | null {
  if (!isRecord(value)) return null;
  const id = numberValue(value.id);
  if (!Number.isInteger(id) || id <= 0) return null;
  const state = normalizeThreadState(value.state);
  return {
    id,
    name: stringValue(value.name),
    state,
    frames: state === "stopped" ? normalizeFrames(value.frames) : [],
    selected: value.selected === true,
  };
}

function normalizeThreads(value: unknown): DebugThreadInfo[] {
  if (!Array.isArray(value)) return [];
  const seen = new Set<number>();
  const result: DebugThreadInfo[] = [];
  for (const candidate of value) {
    const thread = normalizeThread(candidate);
    if (!thread || seen.has(thread.id)) continue;
    seen.add(thread.id);
    result.push(thread);
  }
  return result;
}

function deduplicateFrames(frames: DebugStackFrame[]): DebugStackFrame[] {
  const seen = new Set<number>();
  return frames.filter((frame) => {
    if (seen.has(frame.id)) return false;
    seen.add(frame.id);
    return true;
  });
}

function mergeDebugStackFramePage(
  existing: DebugStackFrame[],
  startFrame: number,
  levels: number,
  page: DebugStackFrame[],
): DebugStackFrame[] {
  if (startFrame > existing.length) return existing;
  const prefix = existing.slice(0, startFrame);
  const pageReachedStackEnd = levels > 0 && page.length < levels;
  const suffix = levels > 0 && !pageReachedStackEnd
    ? existing.slice(startFrame + levels)
    : [];
  return deduplicateFrames([...prefix, ...page, ...suffix]);
}

function stackThreadKey(sessionId: string, threadId: number): string {
  return `${sessionId}:${threadId}`;
}

function invalidateStackPagination(): void {
  stackPaginationRevision++;
  pendingStackPages.clear();
  stackRequests.clear();
  debugThreadsState.loadingStacks.clear();
}

function queuePendingStackPage(
  threadKey: string,
  page: PendingStackPage,
): void {
  let pages = pendingStackPages.get(threadKey);
  if (!pages) {
    pages = new Map<number, PendingStackPage>();
    pendingStackPages.set(threadKey, pages);
  }
  const existing = pages.get(page.startFrame);
  if (!existing || existing.sequence < page.sequence) {
    pages.set(page.startFrame, page);
  }
}

function removeSupersededPendingPages(
  threadKey: string,
  appliedPage: PendingStackPage,
): void {
  const pages = pendingStackPages.get(threadKey);
  if (!pages) return;
  const requestEnd = appliedPage.levels > 0
    ? appliedPage.startFrame + appliedPage.levels
    : Number.POSITIVE_INFINITY;
  const returnedEnd = appliedPage.startFrame + appliedPage.frames.length;
  const pageReachedStackEnd = appliedPage.levels > 0
    && appliedPage.frames.length < appliedPage.levels;
  for (const [startFrame, page] of pages) {
    if (
      page.sequence <= appliedPage.sequence
      && (
        (
          startFrame >= appliedPage.startFrame
          && startFrame < requestEnd
        )
        || (pageReachedStackEnd && startFrame >= returnedEnd)
      )
    ) {
      pages.delete(startFrame);
    }
  }
  if (pages.size === 0) pendingStackPages.delete(threadKey);
}

function applyStackFramePage(
  sessionId: string,
  thread: DebugThreadInfo,
  page: PendingStackPage,
): void {
  const threadKey = stackThreadKey(sessionId, thread.id);
  if (page.startFrame > thread.frames.length) {
    queuePendingStackPage(threadKey, page);
    return;
  }

  thread.frames = mergeDebugStackFramePage(
    thread.frames,
    page.startFrame,
    page.levels,
    page.frames,
  );
  removeSupersededPendingPages(threadKey, page);

  while (true) {
    const pages = pendingStackPages.get(threadKey);
    if (!pages) return;
    const nextPage = [...pages.values()]
      .filter((candidate) => candidate.startFrame <= thread.frames.length)
      .sort((left, right) =>
        left.startFrame - right.startFrame || left.sequence - right.sequence,
      )[0];
    if (!nextPage) return;
    pages.delete(nextPage.startFrame);
    if (pages.size === 0) pendingStackPages.delete(threadKey);
    thread.frames = mergeDebugStackFramePage(
      thread.frames,
      nextPage.startFrame,
      nextPage.levels,
      nextPage.frames,
    );
    removeSupersededPendingPages(threadKey, nextPage);
  }
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function clearSessionState(sessionId: string): void {
  invalidateStackPagination();
  debugThreadsState.sessionId = sessionId;
  debugThreadsState.threads = [];
  debugThreadsState.selected = null;
  debugThreadsState.expanded.clear();
  debugThreadsState.loadingStacks.clear();
  debugThreadsState.actionLoading.clear();
  debugThreadsState.bulkActionLoading = false;
  debugThreadsState.error = null;
  debugThreadsState.allThreadsStopped = false;
  stackRequests.clear();
  actionRequests.clear();
  actionSequence++;
  bulkActionSequence++;
  stateRevision++;
}

function ensureSession(sessionId: string): void {
  if (sessionId && debugThreadsState.sessionId !== sessionId) {
    clearSessionState(sessionId);
  }
}

function applySelectedThread(threadId: number | null): void {
  const selected = threadId !== null
    && debugThreadsState.threads.some((thread) => thread.id === threadId)
    ? threadId
    : null;
  debugThreadsState.selected = selected;
  for (const thread of debugThreadsState.threads) {
    thread.selected = thread.id === selected;
  }
  stateRevision++;
}

function applyThreadsSnapshot(
  sessionId: string,
  value: unknown,
  allThreadsStopped = debugThreadsState.allThreadsStopped,
): void {
  ensureSession(sessionId);
  if (sessionId && debugThreadsState.sessionId !== sessionId) return;

  const previousThreads = new Map(
    debugThreadsState.threads.map((thread) => [thread.id, thread]),
  );
  invalidateStackPagination();
  const threads = normalizeThreads(value);
  const selectedByBackend = threads.find((thread) => thread.selected)?.id ?? null;
  const retainedSelection = debugThreadsState.selected !== null
    && threads.some((thread) => thread.id === debugThreadsState.selected)
    ? debugThreadsState.selected
    : null;
  const selected = selectedByBackend ?? retainedSelection ?? threads[0]?.id ?? null;
  const ids = new Set(threads.map((thread) => thread.id));

  debugThreadsState.threads = threads;
  debugThreadsState.selected = selected;
  debugThreadsState.allThreadsStopped = allThreadsStopped;
  debugThreadsState.error = null;
  for (const thread of threads) {
    thread.selected = thread.id === selected;
    const previous = previousThreads.get(thread.id);
    if (
      thread.state !== "stopped"
      || (previous && previous.frames.length > 0 && thread.frames.length === 0)
    ) {
      debugThreadsState.expanded.delete(thread.id);
    }
  }
  for (const threadId of [...debugThreadsState.expanded]) {
    if (!ids.has(threadId)) debugThreadsState.expanded.delete(threadId);
  }
  for (const threadId of [...debugThreadsState.loadingStacks]) {
    if (!ids.has(threadId)) debugThreadsState.loadingStacks.delete(threadId);
  }
  for (const threadId of [...debugThreadsState.actionLoading]) {
    if (!ids.has(threadId)) debugThreadsState.actionLoading.delete(threadId);
  }
  stateRevision++;
}

function unwrapEventData(event: unknown): unknown {
  let value = isRecord(event) && "data" in event ? event.data : event;
  if (Array.isArray(value) && value.length === 1) value = value[0];
  return value;
}

function handleThreadsUpdated(event: unknown): void {
  const payload = unwrapEventData(event);
  if (!isRecord(payload)) return;
  const sessionId = stringValue(payload.sessionId);
  if (!sessionId) return;
  if (debugThreadsState.sessionId && debugThreadsState.sessionId !== sessionId) return;
  applyThreadsSnapshot(sessionId, payload.threads, payload.allThreadsStopped === true);
}

function handleThreadSelected(event: unknown): void {
  const payload = unwrapEventData(event);
  if (!isRecord(payload)) return;
  const sessionId = stringValue(payload.sessionId);
  const threadId = numberValue(payload.threadId);
  if (!sessionId || !Number.isInteger(threadId) || threadId <= 0) return;
  if (debugThreadsState.sessionId && debugThreadsState.sessionId !== sessionId) return;
  ensureSession(sessionId);
  invalidateStackPagination();
  applySelectedThread(threadId);
}

function handleThreadStopped(event: unknown): void {
  const payload = unwrapEventData(event);
  if (!isRecord(payload)) return;
  const sessionId = stringValue(payload.sessionId);
  const threadId = numberValue(payload.threadId);
  if (!sessionId || !Number.isInteger(threadId) || threadId < 0) return;
  if (debugThreadsState.sessionId && debugThreadsState.sessionId !== sessionId) return;
  ensureSession(sessionId);
  invalidateStackPagination();

  const allThreadsStopped = payload.allThreadsStopped === true;
  if (allThreadsStopped) {
    for (const thread of debugThreadsState.threads) {
      thread.state = "stopped";
      thread.frames = [];
    }
    debugThreadsState.expanded.clear();
  } else if (threadId === 0) {
    for (const thread of debugThreadsState.threads) {
      thread.frames = [];
    }
    debugThreadsState.expanded.clear();
  }
  if (threadId > 0) {
    const stoppedThread = debugThreadsState.threads.find((thread) => thread.id === threadId);
    if (stoppedThread) {
      stoppedThread.state = "stopped";
      stoppedThread.frames = [];
      debugThreadsState.expanded.delete(threadId);
      debugThreadsState.selected = threadId;
      for (const thread of debugThreadsState.threads) {
        thread.selected = thread.id === threadId;
      }
    }
  }
  debugThreadsState.allThreadsStopped = allThreadsStopped;
  stateRevision++;
}

export function isDebugThreadsActive(): boolean {
  return eventsActive;
}

export function activateDebugThreads(): void {
  if (eventsActive) return;
  const subscriptions: Array<() => void> = [];
  try {
    subscriptions.push(Events.On("debug:threads-updated", handleThreadsUpdated));
    subscriptions.push(Events.On("debug:thread-selected", handleThreadSelected));
    subscriptions.push(Events.On("debug:thread-stopped", handleThreadStopped));
    eventUnsubscribers = subscriptions;
    eventsActive = true;
  } catch (error) {
    for (const unsubscribe of subscriptions) {
      try {
        unsubscribe();
      } catch {
        // A failed event registration must not leave a partial subscription.
      }
    }
    eventUnsubscribers = [];
    eventsActive = false;
    debugThreadsState.error = errorMessage(error);
    throw error;
  }
}

export function deactivateDebugThreads(): void {
  if (!eventsActive && eventUnsubscribers.length === 0) return;
  const subscriptions = eventUnsubscribers;
  eventUnsubscribers = [];
  eventsActive = false;
  for (const unsubscribe of subscriptions) {
    try {
      unsubscribe();
    } catch {
      // Event teardown is best-effort during HMR and test cleanup.
    }
  }
}

export function acquireDebugThreadsActivation(): () => void {
  if (activationLeaseCount === 0 && !eventsActive) {
    activateDebugThreads();
    activationLeasesOwnEvents = true;
  }
  activationLeaseCount++;
  let released = false;
  return () => {
    if (released) return;
    released = true;
    activationLeaseCount = Math.max(0, activationLeaseCount - 1);
    if (activationLeaseCount === 0 && activationLeasesOwnEvents) {
      activationLeasesOwnEvents = false;
      deactivateDebugThreads();
    }
  };
}

export function clearDebugThreadsError(): void {
  debugThreadsState.error = null;
}

export async function listDebugThreads(sessionId = debugThreadsState.sessionId): Promise<DebugThreadInfo[]> {
  const boundSessionId = sessionId || debugThreadsState.sessionId;
  if (!boundSessionId) return [];
  if (boundSessionId !== debugThreadsState.sessionId) clearSessionState(boundSessionId);
  const sequence = ++listSequence;
  const revision = stateRevision;
  debugThreadsState.loading = true;
  debugThreadsState.error = null;
  try {
    const result = await bindings.ListThreads(boundSessionId);
    if (sequence !== listSequence) return debugThreadsState.threads;
    if (debugThreadsState.sessionId !== boundSessionId) return debugThreadsState.threads;
    if (revision === stateRevision) applyThreadsSnapshot(boundSessionId, result);
    return debugThreadsState.threads;
  } catch (error) {
    if (sequence === listSequence) debugThreadsState.error = errorMessage(error);
    return [];
  } finally {
    if (sequence === listSequence) debugThreadsState.loading = false;
  }
}

export async function getDebugThreadStackTrace(
  sessionId: string,
  threadId: number,
  startFrame = 0,
  levels = 0,
): Promise<DebugStackFrame[]> {
  const boundSessionId = sessionId || debugThreadsState.sessionId;
  if (
    !boundSessionId
    || !Number.isInteger(threadId) ||
    threadId <= 0 ||
    !Number.isInteger(startFrame) ||
    startFrame < 0 ||
    !Number.isInteger(levels) ||
    levels < 0
  ) {
    return [];
  }
  ensureSession(boundSessionId);
  const cachedThread = debugThreadsState.threads.find((thread) => thread.id === threadId);
  if (cachedThread && cachedThread.state !== "stopped") return cachedThread.frames;
  const requestPrefix = `${boundSessionId}:${threadId}:`;
  const requestKey = `${requestPrefix}${startFrame}:${levels}`;
  const sequence = ++stackSequence;
  const paginationRevision = stackPaginationRevision;
  stackRequests.set(requestKey, sequence);
  debugThreadsState.loadingStacks.add(threadId);
  debugThreadsState.error = null;
  try {
    const frames = normalizeFrames(
      await bindings.GetThreadStackTrace(boundSessionId, threadId, startFrame, levels),
    );
    if (stackRequests.get(requestKey) !== sequence) return frames;
    if (debugThreadsState.sessionId !== boundSessionId) return frames;
    if (paginationRevision !== stackPaginationRevision) return frames;
    const thread = debugThreadsState.threads.find((candidate) => candidate.id === threadId);
    if (thread) {
      applyStackFramePage(
        boundSessionId,
        thread,
        {
          sequence,
          startFrame,
          levels,
          frames,
        },
      );
    }
    stateRevision++;
    return frames;
  } catch (error) {
    if (
      stackRequests.get(requestKey) === sequence
      && debugThreadsState.sessionId === boundSessionId
      && paginationRevision === stackPaginationRevision
    ) {
      debugThreadsState.error = errorMessage(error);
    }
    return [];
  } finally {
    if (stackRequests.get(requestKey) === sequence) {
      stackRequests.delete(requestKey);
      const hasPendingPage = [...stackRequests.keys()].some((key) =>
        key.startsWith(requestPrefix),
      );
      if (
        !hasPendingPage &&
        debugThreadsState.sessionId === boundSessionId
      ) {
        debugThreadsState.loadingStacks.delete(threadId);
      }
    }
  }
}

export async function toggleDebugThreadExpanded(
  sessionId: string,
  threadId: number,
): Promise<void> {
  if (debugThreadsState.expanded.has(threadId)) {
    debugThreadsState.expanded.delete(threadId);
    return;
  }
  const thread = debugThreadsState.threads.find((candidate) => candidate.id === threadId);
  if (!thread) return;
  if (thread.state !== "stopped" && thread.frames.length === 0) return;
  debugThreadsState.expanded.add(threadId);
  if (thread.frames.length > 0) return;
  await getDebugThreadStackTrace(sessionId, threadId);
}

async function runThreadAction(
  sessionId: string,
  threadId: number,
  action: () => Promise<void>,
  onSuccess: () => void,
): Promise<boolean> {
  const boundSessionId = sessionId || debugThreadsState.sessionId;
  if (!boundSessionId || debugThreadsState.sessionId !== boundSessionId) {
    return false;
  }
  const requestKey = `${boundSessionId}:${threadId}`;
  if (
    debugThreadsState.bulkActionLoading
    || debugThreadsState.actionLoading.has(threadId)
    || actionRequests.has(requestKey)
  ) {
    return false;
  }
  const revision = stateRevision;
  const sequence = ++actionSequence;
  actionRequests.set(requestKey, sequence);
  debugThreadsState.actionLoading.add(threadId);
  debugThreadsState.error = null;
  try {
    await action();
    if (
      actionRequests.get(requestKey) !== sequence
      || debugThreadsState.sessionId !== boundSessionId
    ) {
      return false;
    }
    if (stateRevision === revision) {
      onSuccess();
      stateRevision++;
    }
    return true;
  } catch (error) {
    if (
      actionRequests.get(requestKey) === sequence
      && debugThreadsState.sessionId === boundSessionId
    ) {
      debugThreadsState.error = errorMessage(error);
    }
    return false;
  } finally {
    if (actionRequests.get(requestKey) === sequence) {
      actionRequests.delete(requestKey);
      if (debugThreadsState.sessionId === boundSessionId) {
        debugThreadsState.actionLoading.delete(threadId);
      }
    }
  }
}

export function selectDebugThread(sessionId: string, threadId: number): Promise<boolean> {
  const boundSessionId = sessionId || debugThreadsState.sessionId;
  if (
    debugThreadsState.sessionId === boundSessionId
    && debugThreadsState.selected === threadId
  ) {
    return Promise.resolve(true);
  }
  return runThreadAction(
    boundSessionId,
    threadId,
    () => bindings.SelectThread(boundSessionId, threadId),
    () => applySelectedThread(threadId),
  );
}

export function continueDebugThread(sessionId: string, threadId: number): Promise<boolean> {
  const boundSessionId = sessionId || debugThreadsState.sessionId;
  return runThreadAction(
    boundSessionId,
    threadId,
    () => bindings.ContinueThread(boundSessionId, threadId),
    () => {
      const thread = debugThreadsState.threads.find((candidate) => candidate.id === threadId);
      if (thread) {
        invalidateStackPagination();
        thread.state = "running";
        thread.frames = [];
        debugThreadsState.expanded.delete(threadId);
      }
      debugThreadsState.allThreadsStopped = false;
    },
  );
}

async function runBulkThreadAction(
  sessionId: string,
  action: () => Promise<void>,
  onSuccess: () => void,
): Promise<boolean> {
  const boundSessionId = sessionId || debugThreadsState.sessionId;
  if (
    !boundSessionId
    || debugThreadsState.sessionId !== boundSessionId
    || debugThreadsState.bulkActionLoading
    || debugThreadsState.actionLoading.size > 0
  ) {
    return false;
  }
  const revision = stateRevision;
  const sequence = ++bulkActionSequence;
  debugThreadsState.bulkActionLoading = true;
  debugThreadsState.error = null;
  try {
    await action();
    if (
      sequence !== bulkActionSequence
      || debugThreadsState.sessionId !== boundSessionId
    ) {
      return false;
    }
    if (stateRevision === revision) {
      onSuccess();
      stateRevision++;
    }
    return true;
  } catch (error) {
    if (
      sequence === bulkActionSequence
      && debugThreadsState.sessionId === boundSessionId
    ) {
      debugThreadsState.error = errorMessage(error);
    }
    return false;
  } finally {
    if (sequence === bulkActionSequence && debugThreadsState.sessionId === boundSessionId) {
      debugThreadsState.bulkActionLoading = false;
    }
  }
}

export function continueAllDebugThreads(sessionId: string): Promise<boolean> {
  const boundSessionId = sessionId || debugThreadsState.sessionId;
  return runBulkThreadAction(
    boundSessionId,
    () => bindings.ContinueAllThreads(boundSessionId),
    () => {
      invalidateStackPagination();
      for (const thread of debugThreadsState.threads) {
        thread.state = "running";
        thread.frames = [];
      }
      debugThreadsState.expanded.clear();
      debugThreadsState.allThreadsStopped = false;
    },
  );
}

export function pauseAllDebugThreads(sessionId: string): Promise<boolean> {
  const boundSessionId = sessionId || debugThreadsState.sessionId;
  return runBulkThreadAction(
    boundSessionId,
    () => bindings.PauseAllThreads(boundSessionId),
    () => undefined,
  );
}

export function stepDebugThread(
  sessionId: string,
  threadId: number,
  stepType: DebugStepType,
): Promise<boolean> {
  const boundSessionId = sessionId || debugThreadsState.sessionId;
  return runThreadAction(
    boundSessionId,
    threadId,
    () => bindings.StepThread(boundSessionId, threadId, stepType),
    () => {
      applySelectedThread(threadId);
      const thread = debugThreadsState.threads.find((candidate) => candidate.id === threadId);
      if (thread) {
        invalidateStackPagination();
        thread.state = "stepping";
        thread.frames = [];
        debugThreadsState.expanded.delete(threadId);
      }
      debugThreadsState.allThreadsStopped = false;
    },
  );
}

export function resetDebugThreadsStore(): void {
  deactivateDebugThreads();
  activationLeaseCount = 0;
  activationLeasesOwnEvents = false;
  listSequence++;
  stackSequence++;
  actionSequence++;
  bulkActionSequence++;
  stateRevision = 0;
  stackRequests.clear();
  invalidateStackPagination();
  actionRequests.clear();
  debugThreadsState.sessionId = "";
  debugThreadsState.threads = [];
  debugThreadsState.selected = null;
  debugThreadsState.expanded.clear();
  debugThreadsState.loading = false;
  debugThreadsState.loadingStacks.clear();
  debugThreadsState.actionLoading.clear();
  debugThreadsState.bulkActionLoading = false;
  debugThreadsState.error = null;
  debugThreadsState.allThreadsStopped = false;
  bindings = defaultBindings;
}

export interface DebugThreadsStore {
  readonly state: DebugThreadsStoreState;
  readonly threads: DebugThreadInfo[];
  readonly selectedThreadId: number | null;
  readonly isLoading: boolean;
  readonly isBulkActionLoading: boolean;
  loadThreads(sessionId: string): Promise<DebugThreadInfo[]>;
  selectThread(sessionId: string, threadId: number): Promise<boolean>;
  getStackTrace(
    sessionId: string,
    threadId: number,
    startFrame?: number,
    levels?: number,
  ): Promise<DebugStackFrame[]>;
  continueThread(sessionId: string, threadId: number): Promise<boolean>;
  continueAllThreads(sessionId: string): Promise<boolean>;
  pauseAllThreads(sessionId: string): Promise<boolean>;
  stepThread(sessionId: string, threadId: number, stepType: DebugStepType): Promise<boolean>;
  setupEventListeners(): void;
  disposeEventListeners(): void;
}

const debugThreadsStore: DebugThreadsStore = {
  get state() {
    return debugThreadsState;
  },
  get threads() {
    return debugThreadsState.threads;
  },
  get selectedThreadId() {
    return debugThreadsState.selected;
  },
  get isLoading() {
    return debugThreadsState.loading;
  },
  get isBulkActionLoading() {
    return debugThreadsState.bulkActionLoading;
  },
  loadThreads: listDebugThreads,
  selectThread: selectDebugThread,
  getStackTrace: getDebugThreadStackTrace,
  continueThread: continueDebugThread,
  continueAllThreads: continueAllDebugThreads,
  pauseAllThreads: pauseAllDebugThreads,
  stepThread: stepDebugThread,
  setupEventListeners: activateDebugThreads,
  disposeEventListeners: deactivateDebugThreads,
};

// Pinia is not a project dependency; this stable singleton exposes the same
// composable-shaped API without adding an unresolved runtime package.
export function useDebugThreadsStore(): DebugThreadsStore {
  return debugThreadsStore;
}
