<script setup lang="ts">
// Koyori IDE 组件 · Terminal Split Pane。
// 喵，这是 Terminal Split Pane，负责 Koyori IDE 的界面呈现喵~
import { Close, DCaret, Switch } from "@element-plus/icons-vue";
import { FitAddon } from "@xterm/addon-fit";
import { Terminal, type ITheme } from "@xterm/xterm";
import {
  computed,
  nextTick,
  onBeforeUnmount,
  onMounted,
  ref,
  watch,
} from "vue";
import { useI18n } from "@/lib/i18n";
import {
  layoutNodeKey,
  type LayoutNode,
  type SplitDirection,
  type SplitNode,
} from "@/lib/terminalSplit";
import { appState } from "@/stores/app";
import {
  onTerminalOutput,
  resizeSession as resizeTerminalSession,
  writeToSession,
} from "@/stores/terminal";
import "@xterm/xterm/css/xterm.css";

interface TerminalSplitSession {
  id: string;
  output?: string;
  running?: boolean;
  cols?: number;
  rows?: number;
}

type TerminalSessions =
  | Readonly<Record<string, TerminalSplitSession>>
  | readonly TerminalSplitSession[];
type TerminalWriteHandler = (
  sessionId: string,
  data: string,
) => void | Promise<void>;
type TerminalResizeHandler = (
  sessionId: string,
  cols: number,
  rows: number,
) => void | Promise<void>;
type TerminalOutputSubscriber = (
  sessionId: string,
  listener: (data: string) => void,
) => (() => void) | void;

const props = defineProps<{
  node: LayoutNode;
  sessions: TerminalSessions;
  activeSessionId?: string | null;
  minPaneRatio?: number;
  writeSession?: TerminalWriteHandler;
  resizeTerminal?: TerminalResizeHandler;
  /** Pass null to use snapshot-only output supplied through sessions. */
  subscribeOutput?: TerminalOutputSubscriber | null;
}>();

const emit = defineEmits<{
  (e: "split", sessionId: string, direction: SplitDirection): void;
  (e: "close", sessionId: string): void;
  (e: "activate", sessionId: string): void;
  (e: "resize", node: SplitNode, ratio: number): void;
  (e: "resize-end", node: SplitNode, ratio: number): void;
}>();

const { t } = useI18n();
const splitContainer = ref<HTMLElement | null>(null);
const terminalHost = ref<HTMLDivElement | null>(null);
const contextMenuHost = ref<HTMLDivElement | null>(null);
const contextMenu = ref<{ x: number; y: number } | null>(null);
let contextMenuReturnFocus: HTMLElement | null = null;
const splitNode = computed<SplitNode | null>(() =>
  props.node.type === "split" ? props.node : null,
);
const leafSessionId = computed(() =>
  props.node.type === "leaf" ? props.node.sessionId : null,
);
const minimumRatio = computed(() =>
  Math.min(0.49, Math.max(0, props.minPaneRatio ?? 0.05)),
);

function clampPaneRatio(ratio: number): number {
  const min = minimumRatio.value;
  return Math.min(1 - min, Math.max(min, ratio));
}

const localRatio = ref(
  clampPaneRatio(splitNode.value?.ratio ?? 0.5),
);

watch(
  () => splitNode.value?.ratio,
  (ratio) => {
    if (ratio !== undefined && dragState === null) {
      localRatio.value = clampPaneRatio(ratio);
    }
  },
);

const firstPaneStyle = computed(() => ({
  flexBasis: "0",
  flexGrow: localRatio.value,
  flexShrink: 1,
}));
const secondPaneStyle = computed(() => ({
  flexBasis: "0",
  flexGrow: 1 - localRatio.value,
  flexShrink: 1,
}));

function forwardSplit(
  sessionId: string,
  direction: SplitDirection,
): void {
  emit("split", sessionId, direction);
}

function forwardClose(sessionId: string): void {
  emit("close", sessionId);
}

function forwardActivate(sessionId: string): void {
  emit("activate", sessionId);
}

function forwardResize(node: SplitNode, ratio: number): void {
  emit("resize", node, ratio);
}

function forwardResizeEnd(node: SplitNode, ratio: number): void {
  emit("resize-end", node, ratio);
}

function requestSplit(direction: SplitDirection): void {
  contextMenu.value = null;
  if (leafSessionId.value) {
    emit("split", leafSessionId.value, direction);
  }
}

function requestClose(): void {
  contextMenu.value = null;
  if (leafSessionId.value) emit("close", leafSessionId.value);
}

function openContextMenu(event: MouseEvent): void {
  const target = event.currentTarget;
  if (!(target instanceof HTMLElement)) return;
  const rect = target.getBoundingClientRect();
  contextMenuReturnFocus = document.activeElement instanceof HTMLElement
    ? document.activeElement
    : null;
  contextMenu.value = {
    x: Math.max(4, Math.min(rect.width - 164, event.clientX - rect.left)),
    y: Math.max(4, Math.min(rect.height - 112, event.clientY - rect.top)),
  };
  activateLeaf();
  nextTick(() =>
    contextMenuHost.value?.querySelector<HTMLButtonElement>("button")?.focus(),
  );
}

function onLeafKeydown(event: KeyboardEvent): void {
  if (
    event.key !== "ContextMenu" &&
    !(event.shiftKey && event.key === "F10")
  ) {
    return;
  }
  event.preventDefault();
  event.stopPropagation();
  contextMenuReturnFocus = document.activeElement instanceof HTMLElement
    ? document.activeElement
    : null;
  contextMenu.value = { x: 8, y: 36 };
  nextTick(() =>
    contextMenuHost.value?.querySelector<HTMLButtonElement>("button")?.focus(),
  );
}

function closeContextMenu(restoreFocus = false): void {
  contextMenu.value = null;
  const returnFocus = contextMenuReturnFocus;
  contextMenuReturnFocus = null;
  if (restoreFocus) nextTick(() => returnFocus?.focus());
}

function onContextMenuKeydown(event: KeyboardEvent): void {
  const items = Array.from(
    contextMenuHost.value?.querySelectorAll<HTMLButtonElement>('[role="menuitem"]') ?? [],
  );
  if (event.key === "Escape") {
    event.preventDefault();
    event.stopPropagation();
    closeContextMenu(true);
    return;
  }
  if (!items.length || !["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) {
    return;
  }
  event.preventDefault();
  const current = Math.max(0, items.indexOf(document.activeElement as HTMLButtonElement));
  const next = event.key === "Home"
    ? 0
    : event.key === "End"
      ? items.length - 1
      : event.key === "ArrowDown"
        ? (current + 1) % items.length
        : (current - 1 + items.length) % items.length;
  items[next].focus();
}

function onWindowPointerDown(event: PointerEvent): void {
  if (!contextMenu.value) return;
  const target = event.target;
  if (target instanceof Element && target.closest(".terminal-split-pane__menu")) {
    return;
  }
  closeContextMenu(false);
}

function onWindowKeydown(event: KeyboardEvent): void {
  if (event.key === "Escape") closeContextMenu(true);
}

let contextListenersRegistered = false;

function ensureContextListeners(): void {
  if (contextListenersRegistered) return;
  window.addEventListener("pointerdown", onWindowPointerDown);
  window.addEventListener("keydown", onWindowKeydown);
  contextListenersRegistered = true;
}

function removeContextListeners(): void {
  if (!contextListenersRegistered) return;
  window.removeEventListener("pointerdown", onWindowPointerDown);
  window.removeEventListener("keydown", onWindowKeydown);
  contextListenersRegistered = false;
}

function activateLeaf(): void {
  const sessionId = leafSessionId.value;
  if (!sessionId) return;
  emit("activate", sessionId);
  terminalInstance?.focus();
}

function activateLeafFromFocus(): void {
  const sessionId = leafSessionId.value;
  if (sessionId) emit("activate", sessionId);
}

interface SplitDragState {
  pointerId: number;
  startPointer: number;
  startRatio: number;
  availableSize: number;
}

let dragState: SplitDragState | null = null;
const HANDLE_SIZE = 4;
const KEYBOARD_RATIO_STEP = 0.05;

function setLocalRatio(ratio: number, finished = false): void {
  const node = splitNode.value;
  if (!node) return;
  const next = clampPaneRatio(ratio);
  localRatio.value = next;
  emit("resize", node, next);
  if (finished) emit("resize-end", node, next);
}

watch(minimumRatio, () => {
  if (!splitNode.value || dragState !== null) return;
  const next = clampPaneRatio(localRatio.value);
  if (!Object.is(next, localRatio.value)) setLocalRatio(next);
});

function onSeparatorPointerDown(event: PointerEvent): void {
  if (event.button !== 0 || !splitNode.value || !splitContainer.value) return;
  event.preventDefault();
  event.stopPropagation();

  const rect = splitContainer.value.getBoundingClientRect();
  const horizontal = splitNode.value.direction === "horizontal";
  const containerSize = horizontal ? rect.width : rect.height;
  const availableSize = containerSize - HANDLE_SIZE;
  if (availableSize <= 0) return;

  dragState = {
    pointerId: event.pointerId,
    startPointer: horizontal ? event.clientX : event.clientY,
    startRatio: localRatio.value,
    availableSize,
  };
  if (event.currentTarget instanceof HTMLElement) {
    event.currentTarget.setPointerCapture?.(event.pointerId);
  }
  window.addEventListener("pointermove", onSeparatorPointerMove);
  window.addEventListener("pointerup", onSeparatorPointerUp);
  window.addEventListener("pointercancel", onSeparatorPointerUp);
  window.addEventListener("blur", onSeparatorPointerUp);
}

function onSeparatorPointerMove(event: PointerEvent): void {
  const state = dragState;
  const node = splitNode.value;
  if (!state || !node || event.pointerId !== state.pointerId) return;
  event.preventDefault();
  const pointer =
    node.direction === "horizontal" ? event.clientX : event.clientY;
  setLocalRatio(
    state.startRatio + (pointer - state.startPointer) / state.availableSize,
  );
}

function onSeparatorPointerUp(event?: Event): void {
  const eventPointerId = event && "pointerId" in event
    ? event.pointerId
    : undefined;
  if (
    !dragState
    || (eventPointerId !== undefined && eventPointerId !== dragState.pointerId)
  ) {
    return;
  }
  const pointerId = dragState.pointerId;
  dragState = null;
  window.removeEventListener("pointermove", onSeparatorPointerMove);
  window.removeEventListener("pointerup", onSeparatorPointerUp);
  window.removeEventListener("pointercancel", onSeparatorPointerUp);
  window.removeEventListener("blur", onSeparatorPointerUp);
  const separator = splitContainer.value?.querySelector<HTMLButtonElement>(
    ".terminal-split-pane__separator",
  );
  separator?.releasePointerCapture?.(pointerId);
  const node = splitNode.value;
  if (node) emit("resize-end", node, localRatio.value);
}

function onSeparatorKeydown(event: KeyboardEvent): void {
  const node = splitNode.value;
  if (!node) return;
  let next: number | null = null;
  const step = event.shiftKey
    ? KEYBOARD_RATIO_STEP * 2
    : KEYBOARD_RATIO_STEP;

  if (event.key === "Home") next = minimumRatio.value;
  else if (event.key === "End") next = 1 - minimumRatio.value;
  else if (node.direction === "horizontal" && event.key === "ArrowLeft") {
    next = localRatio.value - step;
  } else if (
    node.direction === "horizontal" &&
    event.key === "ArrowRight"
  ) {
    next = localRatio.value + step;
  } else if (
    node.direction === "vertical" &&
    event.key === "ArrowUp"
  ) {
    next = localRatio.value - step;
  } else if (
    node.direction === "vertical" &&
    event.key === "ArrowDown"
  ) {
    next = localRatio.value + step;
  }

  if (next === null) return;
  event.preventDefault();
  setLocalRatio(next, true);
}

function getSession(sessionId: string): TerminalSplitSession | undefined {
  if (Array.isArray(props.sessions)) {
    return props.sessions.find((session) => session.id === sessionId);
  }
  return (props.sessions as Readonly<Record<string, TerminalSplitSession>>)[
    sessionId
  ];
}

const sessionOutput = computed(() => {
  const sessionId = leafSessionId.value;
  return sessionId ? getSession(sessionId)?.output ?? "" : "";
});

let terminalInstance: Terminal | null = null;
let fitAddon: FitAddon | null = null;
let terminalDisposables: Array<{ dispose(): void }> = [];
let outputUnsubscribe: (() => void) | null = null;
let outputSubscriptionVersion = 0;
let streamSnapshotOffset = 0;
let subscriptionReplaySnapshot = "";
let subscriptionReplayOffset = 0;
let resizeObserver: ResizeObserver | null = null;
let renderedOutput = "";
let mountedSessionId: string | null = null;
let lastReportedSize = "";

function terminalTheme(): ITheme {
  const light =
    document.documentElement.getAttribute("data-mode") === "light";
  if (light) {
    return {
      background: "#f6f8fa",
      foreground: "#24292f",
      cursor: "#24292f",
      cursorAccent: "#f6f8fa",
      selectionBackground: "rgba(66, 133, 244, 0.25)",
    };
  }
  return {
    background: "#131316",
    foreground: "#e8e6e3",
    cursor: "#e8e6e3",
    cursorAccent: "#131316",
    selectionBackground: "rgba(255, 255, 255, 0.18)",
  };
}

function terminalCursorStyle(): "block" | "underline" | "bar" {
  const value = appState.terminalCursorStyle;
  return value === "underline" || value === "bar" ? value : "block";
}

function terminalFontFamily(): string {
  const cssFont = getComputedStyle(document.documentElement)
    .getPropertyValue("--font-mono")
    .trim();
  // BUG1: append CJK-capable fallbacks so cmd.exe output on CJK Windows
  // (box-drawing + CJK glyphs) renders in a consistent font instead of
  // falling back to an unrelated system font mid-line.
  return `${cssFont || "JetBrains Mono"}, JetBrains Mono, Consolas, 'Courier New', 'Microsoft YaHei', 'PingFang SC', 'Noto Sans Mono CJK SC', 'SimHei', monospace`;
}

function fitTerminal(): void {
  if (!fitAddon || !terminalHost.value) return;
  const rect = terminalHost.value.getBoundingClientRect();
  if (rect.width <= 0 || rect.height <= 0) return;
  try {
    fitAddon.fit();
  } catch {
    // A following ResizeObserver notification retries once layout is visible.
  }
}

function runWrite(sessionId: string, data: string): void {
  const handler = props.writeSession ?? writeToSession;
  try {
    void Promise.resolve(handler(sessionId, data)).catch((error) => {
      console.error("Failed to write terminal split input:", error);
    });
  } catch (error) {
    console.error("Failed to write terminal split input:", error);
  }
}

function runResize(sessionId: string, cols: number, rows: number): void {
  const sizeKey = `${cols}x${rows}`;
  if (lastReportedSize === sizeKey) return;
  lastReportedSize = sizeKey;
  const handler = props.resizeTerminal ?? resizeTerminalSession;
  try {
    void Promise.resolve(handler(sessionId, cols, rows)).catch((error) => {
      console.error("Failed to resize terminal split session:", error);
    });
  } catch (error) {
    console.error("Failed to resize terminal split session:", error);
  }
}

function appendSnapshot(output: string): void {
  if (!terminalInstance || output === renderedOutput) return;
  if (!output.startsWith(renderedOutput)) {
    terminalInstance.reset();
    renderedOutput = output;
    streamSnapshotOffset = 0;
    subscriptionReplaySnapshot = output;
    subscriptionReplayOffset = 0;
    if (output) terminalInstance.write(output);
    return;
  }
  const chunk = output.slice(renderedOutput.length);
  renderedOutput = output;
  if (chunk) terminalInstance.write(chunk);
}

function appendStreamOutput(sessionId: string, data: string): void {
  if (!terminalInstance || mountedSessionId !== sessionId || !data) return;
  const snapshot = getSession(sessionId)?.output ?? "";
  const previousOutput = renderedOutput;
  if (snapshot.startsWith(previousOutput)) {
    const unseen = snapshot.slice(previousOutput.length);
    renderedOutput = snapshot;
    if (unseen) {
      terminalInstance.write(unseen);
    }
  } else if (!previousOutput.startsWith(snapshot)) {
    terminalInstance.reset();
    renderedOutput = snapshot;
    streamSnapshotOffset = 0;
    subscriptionReplaySnapshot = snapshot;
    subscriptionReplayOffset = 0;
    if (snapshot) {
      terminalInstance.write(snapshot);
    }
  }

  const snapshotMatch = snapshot.indexOf(
    data,
    Math.min(streamSnapshotOffset, snapshot.length),
  );
  if (snapshotMatch >= 0) {
    streamSnapshotOffset = snapshotMatch + data.length;
    return;
  }
  if (
    subscriptionReplaySnapshot.slice(
      subscriptionReplayOffset,
      subscriptionReplayOffset + data.length,
    ) === data
  ) {
    subscriptionReplayOffset += data.length;
    return;
  }
  subscriptionReplayOffset = subscriptionReplaySnapshot.length;
  terminalInstance.write(data);
  renderedOutput += data;
  streamSnapshotOffset += data.length;
}

function disposeOutputSubscription(): void {
  outputSubscriptionVersion += 1;
  const unsubscribe = outputUnsubscribe;
  outputUnsubscribe = null;
  streamSnapshotOffset = 0;
  subscriptionReplaySnapshot = "";
  subscriptionReplayOffset = 0;
  if (!unsubscribe) return;
  try {
    unsubscribe();
  } catch (error) {
    console.error("Failed to unsubscribe terminal split output:", error);
  }
}

function bindOutputSubscription(sessionId: string): void {
  disposeOutputSubscription();
  const subscriber =
    props.subscribeOutput === null
      ? null
      : props.subscribeOutput ?? onTerminalOutput;
  if (!subscriber || !terminalInstance || mountedSessionId !== sessionId) return;

  subscriptionReplaySnapshot = getSession(sessionId)?.output ?? "";
  subscriptionReplayOffset = 0;
  streamSnapshotOffset = subscriptionReplaySnapshot.length;
  const version = outputSubscriptionVersion;
  let unsubscribe: (() => void) | void;
  try {
    unsubscribe = subscriber(sessionId, (data) => {
      if (version !== outputSubscriptionVersion) return;
      appendStreamOutput(sessionId, data);
    });
  } catch (error) {
    // Fence a callback that a faulty subscriber may have retained before
    // throwing, while keeping snapshot rendering and xterm cleanup usable.
    outputSubscriptionVersion += 1;
    console.error("Failed to subscribe terminal split output:", error);
    return;
  }
  if (version !== outputSubscriptionVersion) {
    if (typeof unsubscribe === "function") {
      try {
        unsubscribe();
      } catch (error) {
        console.error("Failed to unsubscribe terminal split output:", error);
      }
    }
    return;
  }
  outputUnsubscribe = typeof unsubscribe === "function" ? unsubscribe : null;
}

function initializeTerminal(sessionId: string): void {
  const host = terminalHost.value;
  if (!host || (terminalInstance && mountedSessionId === sessionId)) return;
  disposeTerminal();

  const terminal = new Terminal({
    fontFamily: terminalFontFamily(),
    fontSize: appState.terminalFontSize || 13,
    cursorBlink: true,
    cursorStyle: terminalCursorStyle(),
    scrollback: appState.scrollback || 5000,
    theme: terminalTheme(),
  });
  const addon = new FitAddon();
  terminal.loadAddon(addon);
  terminal.open(host);

  terminalDisposables = [
    terminal.onData((data) => runWrite(sessionId, data)),
    terminal.onResize(({ cols, rows }) =>
      runResize(sessionId, cols, rows),
    ),
  ];
  terminalInstance = terminal;
  fitAddon = addon;
  mountedSessionId = sessionId;
  renderedOutput = "";
  lastReportedSize = "";

  appendSnapshot(getSession(sessionId)?.output ?? "");
  bindOutputSubscription(sessionId);

  if (typeof ResizeObserver !== "undefined") {
    resizeObserver = new ResizeObserver(fitTerminal);
    resizeObserver.observe(host);
  }

  nextTick(() => {
    fitTerminal();
    if (props.activeSessionId === sessionId) terminal.focus();
  });
}

function disposeTerminal(): void {
  resizeObserver?.disconnect();
  resizeObserver = null;
  disposeOutputSubscription();
  for (const disposable of terminalDisposables) disposable.dispose();
  terminalDisposables = [];
  terminalInstance?.dispose();
  terminalInstance = null;
  fitAddon = null;
  mountedSessionId = null;
  renderedOutput = "";
  lastReportedSize = "";
}

watch(
  leafSessionId,
  async (sessionId) => {
    disposeTerminal();
    if (!sessionId) {
      removeContextListeners();
      return;
    }
    ensureContextListeners();
    await nextTick();
    initializeTerminal(sessionId);
  },
  { flush: "post" },
);

watch(sessionOutput, (output) => appendSnapshot(output), { flush: "post" });

watch(
  () => props.subscribeOutput,
  () => {
    const sessionId = mountedSessionId;
    if (!terminalInstance || !sessionId) return;
    if (leafSessionId.value !== sessionId) {
      disposeOutputSubscription();
      return;
    }
    bindOutputSubscription(sessionId);
  },
  { flush: "sync" },
);

watch(
  () =>
    [
      appState.terminalFontSize,
      appState.terminalCursorStyle,
      appState.scrollback,
      appState.theme,
    ] as const,
  () => {
    nextTick(() => {
      if (!terminalInstance) return;
      terminalInstance.options.fontSize = appState.terminalFontSize || 13;
      terminalInstance.options.cursorStyle = terminalCursorStyle();
      terminalInstance.options.scrollback = appState.scrollback || 5000;
      terminalInstance.options.theme = terminalTheme();
      fitTerminal();
    });
  },
);

watch(
  () => props.activeSessionId,
  (sessionId) => {
    if (sessionId === mountedSessionId) nextTick(() => terminalInstance?.focus());
  },
);

onMounted(() => {
  const sessionId = leafSessionId.value;
  if (sessionId) {
    ensureContextListeners();
    initializeTerminal(sessionId);
  }
});

onBeforeUnmount(() => {
  removeContextListeners();
  disposeTerminal();
  if (dragState) {
    onSeparatorPointerUp();
  }
});

defineExpose({ fitTerminal, focus: () => terminalInstance?.focus() });
</script>

<template>
  <div
    v-if="splitNode"
    ref="splitContainer"
    class="terminal-split-pane terminal-split-pane--split"
    :class="`terminal-split-pane--${splitNode.direction}`"
  >
    <div class="terminal-split-pane__child" :style="firstPaneStyle">
      <TerminalSplitPane
        :key="layoutNodeKey(splitNode.children[0])"
        :node="splitNode.children[0]"
        :sessions="sessions"
        :active-session-id="activeSessionId"
        :min-pane-ratio="minPaneRatio"
        :write-session="writeSession"
        :resize-terminal="resizeTerminal"
        :subscribe-output="subscribeOutput"
        @split="forwardSplit"
        @close="forwardClose"
        @activate="forwardActivate"
        @resize="forwardResize"
        @resize-end="forwardResizeEnd"
      />
    </div>

    <button
      type="button"
      class="terminal-split-pane__separator"
      :class="`terminal-split-pane__separator--${splitNode.direction}`"
      role="separator"
      :aria-orientation="splitNode.direction === 'horizontal' ? 'vertical' : 'horizontal'"
      :aria-valuenow="Math.round(localRatio * 100)"
      :aria-valuemin="Math.round(minimumRatio * 100)"
      :aria-valuemax="Math.round((1 - minimumRatio) * 100)"
      :aria-label="t('terminal.resizeSplitAria')"
      @pointerdown="onSeparatorPointerDown"
      @keydown="onSeparatorKeydown"
    />

    <div class="terminal-split-pane__child" :style="secondPaneStyle">
      <TerminalSplitPane
        :key="layoutNodeKey(splitNode.children[1])"
        :node="splitNode.children[1]"
        :sessions="sessions"
        :active-session-id="activeSessionId"
        :min-pane-ratio="minPaneRatio"
        :write-session="writeSession"
        :resize-terminal="resizeTerminal"
        :subscribe-output="subscribeOutput"
        @split="forwardSplit"
        @close="forwardClose"
        @activate="forwardActivate"
        @resize="forwardResize"
        @resize-end="forwardResizeEnd"
      />
    </div>
  </div>

  <section
    v-else-if="leafSessionId"
    class="terminal-split-pane terminal-split-pane--leaf"
    :class="{
      'terminal-split-pane--active': activeSessionId === leafSessionId,
    }"
    :data-session-id="leafSessionId"
    @mousedown="activateLeaf"
    @focusin="activateLeafFromFocus"
    @contextmenu.prevent.stop="openContextMenu"
    @keydown="onLeafKeydown"
  >
    <div class="terminal-split-pane__toolbar">
      <button
        type="button"
        class="terminal-split-pane__action"
        data-action="split-horizontal"
        :aria-label="t('terminal.splitHorizontalAria', { sessionId: leafSessionId })"
        :title="t('terminal.splitHorizontalAria', { sessionId: leafSessionId })"
        @click.stop="requestSplit('horizontal')"
      >
        <Switch aria-hidden="true" />
      </button>
      <button
        type="button"
        class="terminal-split-pane__action"
        data-action="split-vertical"
        :aria-label="t('terminal.splitVerticalAria', { sessionId: leafSessionId })"
        :title="t('terminal.splitVerticalAria', { sessionId: leafSessionId })"
        @click.stop="requestSplit('vertical')"
      >
        <DCaret aria-hidden="true" />
      </button>
      <button
        type="button"
        class="terminal-split-pane__action"
        data-action="close"
        :aria-label="t('terminal.closeSplitAria', { sessionId: leafSessionId })"
        :title="t('terminal.closeSplitAria', { sessionId: leafSessionId })"
        @click.stop="requestClose"
      >
        <Close aria-hidden="true" />
      </button>
    </div>
    <div
      ref="terminalHost"
      class="terminal-split-pane__terminal"
      role="region"
      :aria-label="t('terminal.splitPaneAria', { sessionId: leafSessionId })"
    />
    <div
      v-if="contextMenu"
      ref="contextMenuHost"
      class="terminal-split-pane__menu"
      role="menu"
      :aria-label="t('terminal.splitMenuAria')"
      :style="{ left: `${contextMenu.x}px`, top: `${contextMenu.y}px` }"
      @mousedown.stop
      @keydown="onContextMenuKeydown"
    >
      <button
        type="button"
        role="menuitem"
        :aria-label="t('terminal.splitHorizontalAria', { sessionId: leafSessionId })"
        @click.stop="requestSplit('horizontal')"
      >
        <Switch aria-hidden="true" />
        <span>{{ t("terminal.splitHorizontal") }}</span>
      </button>
      <button
        type="button"
        role="menuitem"
        :aria-label="t('terminal.splitVerticalAria', { sessionId: leafSessionId })"
        @click.stop="requestSplit('vertical')"
      >
        <DCaret aria-hidden="true" />
        <span>{{ t("terminal.splitVertical") }}</span>
      </button>
      <button
        type="button"
        role="menuitem"
        :aria-label="t('terminal.closeSplitAria', { sessionId: leafSessionId })"
        @click.stop="requestClose"
      >
        <Close aria-hidden="true" />
        <span>{{ t("common.close") }}</span>
      </button>
    </div>
  </section>
</template>

<style scoped>
.terminal-split-pane {
  width: 100%;
  height: 100%;
  min-width: 0;
  min-height: 0;
  overflow: hidden;
}

.terminal-split-pane--split {
  display: flex;
}

.terminal-split-pane--horizontal {
  flex-direction: row;
}

.terminal-split-pane--vertical {
  flex-direction: column;
}

.terminal-split-pane__child {
  min-width: 0;
  min-height: 0;
  overflow: hidden;
}

.terminal-split-pane__separator {
  display: block;
  flex: 0 0 auto;
  padding: 0;
  border: 0;
  border-radius: 0;
  background: var(--color-border-subtle);
  transition: background-color var(--transition-fast);
  z-index: 2;
}

.terminal-split-pane__separator:hover,
.terminal-split-pane__separator:focus-visible {
  background: var(--color-primary, #4285f4);
  outline: none;
}

.terminal-split-pane__separator--horizontal {
  width: 4px;
  height: 100%;
  cursor: col-resize;
}

.terminal-split-pane__separator--vertical {
  width: 100%;
  height: 4px;
  cursor: row-resize;
}

.terminal-split-pane--leaf {
  position: relative;
  display: flex;
  flex-direction: column;
  background: var(--color-terminal-bg);
  box-shadow: inset 0 0 0 1px transparent;
}

.terminal-split-pane--active {
  box-shadow: inset 0 0 0 1px var(--color-primary, #4285f4);
}

.terminal-split-pane__toolbar {
  position: absolute;
  top: 4px;
  right: 6px;
  display: flex;
  gap: 2px;
  z-index: 3;
  opacity: 0;
  transition: opacity var(--transition-fast);
}

.terminal-split-pane--leaf:hover .terminal-split-pane__toolbar,
.terminal-split-pane--leaf:focus-within .terminal-split-pane__toolbar {
  opacity: 1;
}

.terminal-split-pane__action {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  padding: 5px;
  border: 0;
  border-radius: var(--radius-sm, 4px);
  background: color-mix(in srgb, var(--color-terminal-bg) 88%, transparent);
  color: var(--color-text-tertiary);
  cursor: pointer;
}

.terminal-split-pane__action:hover,
.terminal-split-pane__action:focus-visible {
  color: var(--color-text-primary);
  background: var(--color-bg-surface-container-high);
  outline: 2px solid var(--color-primary-focus, #4285f4);
  outline-offset: -2px;
}

.terminal-split-pane__action svg {
  width: 14px;
  height: 14px;
}

.terminal-split-pane__terminal {
  flex: 1;
  min-width: 0;
  min-height: 0;
  padding: 6px 8px;
  overflow: hidden;
}

.terminal-split-pane__terminal :deep(.xterm) {
  height: 100%;
}

.terminal-split-pane__terminal :deep(.xterm-viewport) {
  overflow-y: auto;
}

.terminal-split-pane__menu {
  position: absolute;
  z-index: 8;
  display: grid;
  width: 160px;
  padding: 4px;
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-sm, 4px);
  background: var(--color-bg-surface-container-high);
  box-shadow: var(--shadow-lg);
}

.terminal-split-pane__menu button {
  display: grid;
  grid-template-columns: 16px minmax(0, 1fr);
  align-items: center;
  gap: 8px;
  width: 100%;
  min-height: 30px;
  padding: 4px 8px;
  border: 0;
  border-radius: var(--radius-xs, 3px);
  background: transparent;
  color: var(--color-text-primary);
  text-align: left;
  cursor: pointer;
}

.terminal-split-pane__menu button:hover,
.terminal-split-pane__menu button:focus-visible {
  background: var(--color-bg-surface-container-highest);
  outline: 2px solid var(--color-primary-focus, #4285f4);
  outline-offset: -2px;
}

.terminal-split-pane__menu svg {
  width: 14px;
  height: 14px;
}

@media (prefers-reduced-motion: reduce) {
  .terminal-split-pane__separator,
  .terminal-split-pane__toolbar {
    transition: none;
  }
}
</style>
