<script setup lang="ts">
// Koyori IDE 组件 · Terminal Panel。
// 喵，这是 Terminal Panel，负责 Koyori IDE 的界面呈现喵~
import { appState, toggleTerminal } from "@/stores/app";
import { computed, onMounted, onBeforeUnmount, ref, watch, nextTick } from "vue";
import { Close, Plus, Delete, Refresh } from "@element-plus/icons-vue";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import {
  terminalState,
  createSession,
  writeToSession,
  killSession,
  reconnectSession,
  resizeSession,
  clearSessionOutput,
} from "@/stores/terminal";
import {
  outputState,
  clearOutputs,
  clearProblems,
  problemCounts,
  type ProblemEntry,
} from "@/stores/output";
import {
  taskState,
  loadTasks,
  runTask,
  composeCommandLine,
  hasTasks,
} from "@/stores/tasks";
import {
  workflowState,
  loadWorkflows,
  runWorkflow,
  composeStepCommandLine,
  hasWorkflows,
} from "@/stores/workflows";
import { openFileFromPath } from "@/stores/editor";
import { useI18n } from "@/lib/i18n";
import DebugPanel from "@/components/layout/DebugPanel.vue";
// Priority 7: pprof 性能分析面板，与 DebugPanel 并列于底部面板。
import ProfilePanel from "@/components/layout/ProfilePanel.vue";
import "@xterm/xterm/css/xterm.css";

// BUG4b: embedded 模式用于 AI 窗口 dock。在 embedded 模式下：
//   - 可见性始终为 true（不依赖 appState.terminalVisible）
//   - 关闭按钮 emit('close') 而非调用全局 toggleTerminal
//   - 高度不依赖 appState.terminalHeight，由父容器控制
//   - 隐藏 output/problems/tasks/workflows/debug/profile 等主窗口专属视图
const props = defineProps<{
  embedded?: boolean;
}>();

const emit = defineEmits<{
  (e: "close"): void;
  (e: "fit"): void;
}>();

type PanelView = "terminal" | "output" | "problems" | "tasks" | "workflows" | "debug" | "profile";

const { t } = useI18n();

// BUG4b: embedded 模式下始终可见，不受 appState.terminalVisible 控制。
const isVisible = computed(() => props.embedded || appState.terminalVisible);
// N-20: bind height to appState so the drag handle can resize the panel.
// BUG4b: embedded 模式下高度由父容器控制（100%），不使用 appState.terminalHeight。
const panelHeightPx = computed(() =>
  props.embedded ? "100%" : `${appState.terminalHeight}px`,
);
const terminalContainer = ref<HTMLElement | null>(null);
const activeView = ref<PanelView>("terminal");

const problemCountSummary = computed(() => {
  const c = problemCounts();
  const parts: string[] = [];
  if (c.error > 0) parts.push(c.error === 1 ? t("terminal.errorSingular") : t("terminal.errorPlural", { count: c.error }));
  if (c.warning > 0) parts.push(c.warning === 1 ? t("terminal.warningSingular") : t("terminal.warningPlural", { count: c.warning }));
  return parts.join(" · ");
});

const hasProblems = computed(() => outputState.problems.length > 0);
const hasOutputs = computed(() => outputState.outputs.length > 0);

// Map of session ID -> { term, fitAddon, container }
// C-5: previously these were module-level (`const terminals: Record<...> = {}`
// and `let currentSessionId`), causing all TerminalPanel instances to share
// state. onBeforeUnmount would dispose every instance's terminals, not just
// the current one. Moved into setup() so each component instance owns its
// own map.
interface TerminalInstance {
  term: Terminal;
  fitAddon: FitAddon;
  container: HTMLDivElement;
}
const terminals = ref(new Map<string, TerminalInstance>());
const currentSessionId = ref<string | null>(null);

const sessionList = computed(() =>
  terminalState.sessionOrder.map((id, index) => ({
    id,
    label: t("terminal.terminalLabel", { n: index + 1 }),
    running: terminalState.sessions[id]?.running ?? false,
    reconnecting: terminalState.sessions[id]?.reconnecting ?? false,
  })),
);

// N-142: recent output for the active terminal session, exposed as an
// aria-live region so screen readers can announce new output. We tail
// the last 500 characters to avoid flooding the assistive-tech buffer.
const recentOutput = computed(() => {
  const id = terminalState.activeSessionId;
  if (!id) return "";
  const session = terminalState.sessions[id];
  if (!session) return "";
  const out = session.output;
  return out.length > 500 ? out.slice(-500) : out;
});

// Terminal color themes that match the app's light/dark modes.
// Light theme uses GitHub-style colors to match the highlight.js theme in
// main.css; dark theme uses the original dark surface.
function getTerminalTheme(): Record<string, string> {
  const mode = document.documentElement.getAttribute("data-mode");
  if (mode === "light") {
    return {
      background: "#f6f8fa",
      foreground: "#24292f",
      cursor: "#24292f",
      cursorAccent: "#f6f8fa",
      selectionBackground: "rgba(66, 133, 244, 0.25)",
      black: "#24292f",
      red: "#cf222e",
      green: "#116329",
      yellow: "#4d2d00",
      blue: "#0550ae",
      magenta: "#8250df",
      cyan: "#0a3069",
      white: "#6e7781",
      brightBlack: "#57606a",
      brightRed: "#a40e26",
      brightGreen: "#1a7f37",
      brightYellow: "#633c01",
      brightBlue: "#0969da",
      brightMagenta: "#8250df",
      brightCyan: "#1b7c83",
      brightWhite: "#8c959f",
    };
  }
  return {
    background: "#131316",
    foreground: "#e8e6e3",
    cursor: "#e8e6e3",
    cursorAccent: "#131316",
    selectionBackground: "rgba(255, 255, 255, 0.18)",
    black: "#131316",
    red: "#ff7b72",
    green: "#7ee787",
    yellow: "#f2cc60",
    blue: "#79c0ff",
    magenta: "#d2a8ff",
    cyan: "#56d4dd",
    white: "#e8e6e3",
    brightBlack: "#6e7681",
    brightRed: "#ffa198",
    brightGreen: "#7ee787",
    brightYellow: "#f2cc60",
    brightBlue: "#a5d6ff",
    brightMagenta: "#d2a8ff",
    brightCyan: "#79c0ff",
    brightWhite: "#ffffff",
  };
}

async function initTerminalForSession(sessionId: string) {
  if (terminals.value.has(sessionId) || !terminalContainer.value) return;

  // xterm.js's fontFamily option is a literal CSS string — it does NOT resolve
  // CSS variables. Read the --font-mono token at runtime and build a concrete
  // font stack (#25 / B-9).
  const cssFont =
    getComputedStyle(document.documentElement)
      .getPropertyValue("--font-mono")
      .trim() || "JetBrains Mono";
  const fontFamily = `${cssFont}, JetBrains Mono, Consolas, 'Courier New', monospace`;

  // G-PERF-03: cap xterm scrollback at 5000 lines. Combined with the
  // backend outputBuffer 1 MiB cap (output_buffer.go), this bounds peak
  // memory per terminal session. xterm's default is 1000, which drops
  // useful context during long builds; 5000 keeps recent history without
  // unbounded growth.
  // G16: the user's scrollback setting is honored but clamped to [100, 5000]
  // so the memory gate is never silently raised; cursor style (block /
  // underline / bar) comes from the persisted terminal setting.
  const term = new Terminal({
    fontFamily,
    fontSize: appState.terminalFontSize || 13,
    theme: getTerminalTheme(),
    cursorBlink: true,
    scrollback: Math.min(Math.max(appState.scrollback || 5000, 100), 5000),
    cursorStyle:
      appState.terminalCursorStyle === "underline" || appState.terminalCursorStyle === "bar"
        ? appState.terminalCursorStyle
        : "block",
  });

  const fitAddon = new FitAddon();
  term.loadAddon(fitAddon);

  // Create a div for this terminal.
  // IMPORTANT: xterm.js cannot measure dimensions on a display:none container.
  // If we open() while hidden, the renderer gets 0x0 and keyboard input is
  // silently dropped. We keep the div visible during open()+fit(), then hide
  // it if it's not the active session. switchToSession() will toggle display
  // and re-fit as needed.
  const div = document.createElement("div");
  div.style.height = "100%";
  terminalContainer.value.appendChild(div);
  term.open(div);
  fitAddon.fit();

  // BUG-TERM2: 回放初始化前已到达的输出（如 PTY 启动时 shell 提示符）。
  // sessionsVersion 浅 watch 在 xterm 尚不存在时跳过该 session（无 terminal
  // 条目），因此缓存中的早期输出永远不会被渲染 —— 表现为终端打开即空白。
  // 这里快照后同步清空，写入期间到达的新输出会进入空缓冲，由 watcher 拾取。
  const existing = terminalState.sessions[sessionId];
  if (existing && existing.output) {
    const buffered = existing.output;
    clearSessionOutput(sessionId);
    term.write(buffered);
  }

  term.onData((data) => {
    writeToSession(sessionId, data);
  });

  term.onResize(({ cols, rows }) => {
    resizeSession(sessionId, cols, rows);
  });

  terminals.value.set(sessionId, { term, fitAddon, container: div });

  // Ensure xterm receives keyboard focus when the terminal area is clicked.
  // Without this, the hidden textarea inside xterm won't get focus and
  // onData won't fire.
  div.addEventListener("mousedown", () => {
    term.focus();
  });

  // If this is not the active session, hide it now that initialization is done.
  if (terminalState.activeSessionId !== sessionId) {
    div.style.display = "none";
  }

  // Auto-focus the active terminal after a tick to ensure it captures input.
  if (terminalState.activeSessionId === sessionId) {
    nextTick(() => term.focus());
  }
}

function switchToSession(sessionId: string) {
  if (currentSessionId.value === sessionId) return;

  // Hide current
  if (currentSessionId.value) {
    const cur = terminals.value.get(currentSessionId.value);
    if (cur) cur.container.style.display = "none";
  }

  // Show new
  const next = terminals.value.get(sessionId);
  if (next) {
    next.container.style.display = "block";
    next.fitAddon.fit();
    // Focus the new terminal so it captures keyboard input immediately.
    next.term.focus();
  }

  currentSessionId.value = sessionId;
}

async function initFirstSession() {
  if (terminalState.sessionOrder.length === 0) {
    const workingDir = appState.currentProject ?? "";
    await createSession(workingDir, appState.defaultShell?.trim() ?? "");
  }
  if (terminalState.activeSessionId) {
    await initTerminalForSession(terminalState.activeSessionId);
    switchToSession(terminalState.activeSessionId);
  }
}

async function handleNewTerminal() {
  const workingDir = appState.currentProject ?? "";
  const id = await createSession(workingDir, appState.defaultShell?.trim() ?? "");
  if (id) {
    await nextTick();
    await initTerminalForSession(id);
    switchToSession(id);
  }
}

async function handleReconnectTerminal(sessionId: string) {
  const reconnected = await reconnectSession(sessionId);
  if (!reconnected) return;
  await nextTick();
  const entry = terminals.value.get(sessionId);
  if (entry) {
    entry.fitAddon.fit();
    entry.term.focus();
  }
}

async function handleCloseTerminal(sessionId: string) {
  // Dispose xterm instance
  const entry = terminals.value.get(sessionId);
  if (entry) {
    entry.term.dispose();
    entry.container.remove();
    terminals.value.delete(sessionId);
  }

  await killSession(sessionId);

  // Switch to another session if any
  if (terminalState.activeSessionId) {
    await nextTick();
    if (!terminals.value.has(terminalState.activeSessionId)) {
      await initTerminalForSession(terminalState.activeSessionId);
    }
    switchToSession(terminalState.activeSessionId);
  } else {
    currentSessionId.value = null;
  }
}

function handleSelectTab(sessionId: string) {
  terminalState.activeSessionId = sessionId;
}

// N-141: roving tabindex for the panel view tablist. ArrowLeft/ArrowRight
// move focus and selection among the 5 view tabs (terminal/output/problems/
// tasks/workflows). Home/End jump to the first/last tab.
const viewTabs: PanelView[] = ["terminal", "output", "problems", "debug", "tasks", "workflows", "profile"];
function handleViewTabKeydown(e: KeyboardEvent) {
  const idx = viewTabs.indexOf(activeView.value);
  if (idx < 0) return;
  let next = idx;
  if (e.key === "ArrowRight") next = (idx + 1) % viewTabs.length;
  else if (e.key === "ArrowLeft") next = (idx - 1 + viewTabs.length) % viewTabs.length;
  else if (e.key === "Home") next = 0;
  else if (e.key === "End") next = viewTabs.length - 1;
  else return;
  e.preventDefault();
  activeView.value = viewTabs[next];
  // Focus the newly-selected tab after the DOM updates.
  nextTick(() => {
    const container = document.querySelector<HTMLDivElement>(".terminal-panel__view-tabs");
    if (!container) return;
    const btns = container.querySelectorAll<HTMLButtonElement>("button[role='tab']");
    btns[next]?.focus();
  });
}

// N-141: roving tabindex for the terminal session tablist. ArrowLeft/
// ArrowRight move focus and selection among the open terminal sessions.
function handleSessionTabKeydown(e: KeyboardEvent) {
  const sessions = sessionList.value;
  if (sessions.length === 0) return;
  const idx = sessions.findIndex((s) => s.id === terminalState.activeSessionId);
  let next = idx < 0 ? 0 : idx;
  if (e.key === "ArrowRight") next = (idx + 1) % sessions.length;
  else if (e.key === "ArrowLeft") next = (idx - 1 + sessions.length) % sessions.length;
  else if (e.key === "Home") next = 0;
  else if (e.key === "End") next = sessions.length - 1;
  else return;
  e.preventDefault();
  handleSelectTab(sessions[next].id);
  nextTick(() => {
    const container = document.querySelector<HTMLDivElement>(".terminal-panel__tabs");
    if (!container) return;
    const btns = container.querySelectorAll<HTMLButtonElement>("button[role='tab']");
    btns[next]?.focus();
  });
}

function fitTerminal() {
  // BUG4a: Fit ALL terminal sessions, not just the current one. In the AI
  // dock, the container transitions from width:0 to full width; we need to
  // refit every xterm instance after the transition completes.
  for (const entry of terminals.value.values()) {
    try {
      entry.fitAddon.fit();
    } catch {
      // xterm may not be ready yet (0x0 container during transition) —
      // the caller will retry via the @after-enter hook or ResizeObserver.
    }
  }
}

// BUG4b: In embedded mode, the close button emits 'close' instead of
// calling the global toggleTerminal (which toggles appState.terminalVisible
// and would hide the panel in the AI window).
function handleClose() {
  if (props.embedded) {
    emit("close");
  } else {
    toggleTerminal();
  }
}

// BUG4a: Expose fitTerminal so the parent (AiTerminalDock) can call it
// after the transition animation completes.
defineExpose({ fitTerminal });

// --- Output/Problems view helpers ---
function formatTimestamp(ts: number): string {
  const d = new Date(ts);
  const hh = String(d.getHours()).padStart(2, "0");
  const mm = String(d.getMinutes()).padStart(2, "0");
  const ss = String(d.getSeconds()).padStart(2, "0");
  return `${hh}:${mm}:${ss}`;
}

function severityIcon(sev: ProblemEntry["severity"]): string {
  switch (sev) {
    case "error":
      return "✕";
    case "warning":
      return "⚠";
    case "info":
      return "ℹ";
    case "hint":
      return "💡";
  }
}

async function handleProblemClick(p: ProblemEntry) {
  // prompt-10 10-D: resolve relative paths against project root; jump line via appState.
  let path = p.file;
  if (path && !/^[A-Za-z]:[\\/]/.test(path) && !path.startsWith("/")) {
    const root = appState.currentProject;
    if (root) {
      path = root.replace(/[\\/]$/, "") + "/" + path.replace(/^[\\/]/, "");
    }
  }
  try {
    await openFileFromPath(path);
    appState.cursorLine = p.line || 1;
    appState.cursorColumn = p.column || 1;
    // prompt-10 10-D: force Monaco reveal even if line/col unchanged
    appState.editorJumpSeq = (appState.editorJumpSeq || 0) + 1;
  } catch {
    // openFileFromPath already notifies
  }
}

function handleClearCurrentView() {
  if (activeView.value === "output") clearOutputs();
  else if (activeView.value === "problems") clearProblems();
}

// Watch activeSessionId changes
watch(
  () => terminalState.activeSessionId,
  async (newId) => {
    if (newId) {
      if (!terminals.value.has(newId)) {
        await initTerminalForSession(newId);
      }
      switchToSession(newId);
    }
  },
);

// Refit terminal when switching back to terminal view; reload tasks/workflows
// when switching to those views so freshly-edited files are reflected.
watch(activeView, (v) => {
  if (v === "terminal") {
    nextTick(() => fitTerminal());
  } else if (v === "tasks" && appState.currentProject) {
    loadTasks(appState.currentProject);
  } else if (v === "workflows" && appState.currentProject) {
    loadWorkflows(appState.currentProject);
  }
});

// G-FEAT-03: a toolchain run can request focusing a specific bottom-panel
// tab (output/problems) by setting appState.bottomPanelView. Watch it and
// switch activeView, then clear the override so manual tab clicks still work.
watch(
  () => appState.bottomPanelView,
  (v) => {
    if (
      v === "output" ||
      v === "problems" ||
      v === "terminal" ||
      v === "tasks" ||
      v === "workflows" ||
      v === "debug" ||
      v === "profile"
    ) {
      activeView.value = v;
      appState.bottomPanelView = "";
    }
  },
);

// Reload tasks and workflows when a project is opened.
watch(
  () => appState.currentProject,
  (root) => {
    if (root) {
      loadTasks(root);
      loadWorkflows(root);
    }
  },
);

// Re-apply terminal theme when the app mode (dark/light) changes so the
// terminal surface matches the rest of the UI. appState.theme holds the
// user's choice ("dark"/"light"/"system"); the resolved mode is reflected
// on <html data-mode>. We watch appState.theme and tick through nextTick
// to let applyMode() update the DOM before we read data-mode.
watch(
  () => appState.theme,
  () => {
    nextTick(() => {
      const theme = getTerminalTheme();
      for (const entry of terminals.value.values()) {
        entry.term.options.theme = theme as never;
      }
    });
  },
);

function handleRunTask(label: string) {
  const task = taskState.tasks.find((t) => t.label === label);
  if (!task || !appState.currentProject) return;
  runTask(task, appState.currentProject);
  // Switch to terminal view so the user sees the running command.
  activeView.value = "terminal";
}

function handleRunWorkflow(name: string) {
  const wf = workflowState.workflows.find((w) => w.name === name);
  if (!wf || !appState.currentProject) return;
  runWorkflow(wf, appState.currentProject);
  // Switch to terminal view so the user sees the running steps.
  activeView.value = "terminal";
}

function workflowStepStatus(name: string, stepName: string): string {
  const states = workflowState.stepStates[name];
  if (!states) return "pending";
  const s = states.find((x) => x.name === stepName);
  return s ? s.status : "pending";
}

function workflowRunning(name: string): boolean {
  return !!workflowState.running[name];
}

// M-13: 每会话写入版本号。每次 clear+write 时版本号递增；term.write 回调
// 触发时检查版本号是否变化，若变化则说明写入期间发生了另一次 clear，
// 此写入已过期 —— 跳过任何过期数据的追加，避免异步竞态导致丢字或
// 新旧输出混合。
const writeVersion = ref(new Map<string, number>());

// Watch each session's output and write to xterm.
// BUG5: Fix race condition — previously, if a new terminal:output event
// arrived between term.write(session.output) and clearSessionOutput(id),
// that chunk was lost (cleared without being written). Fix: snapshot the
// output and clear IMMEDIATELY (synchronously) before writing to xterm,
// so any new event appended during the write goes to a fresh buffer.
// M-13: 引入版本号机制 —— term.write 回调触发时检查版本号，若写入期间
// 发生了 clear（版本号变化），跳过过期追加，确保 clear 状态不被覆盖。
// M-24: 改为浅 watch sessionsVersion（store 在 createSession/killSession
// 及 terminal:output 事件中自增），替代原先对 sessions 的深度 watch。
// 深度 watch 在每次输出追加都会深度遍历整个 sessions 对象，输出量大时
// 开销显著；浅 watch 仅比较一个数字，变更检测为 O(1)。
watch(
  () => terminalState.sessionsVersion,
  () => {
    for (const [id, session] of Object.entries(terminalState.sessions)) {
      const entry = terminals.value.get(id);
      if (entry && session.output) {
        // Snapshot then clear synchronously — new events arriving during
        // term.write() will append to the now-empty buffer and be picked
        // up on the next watcher tick.
        const chunk = session.output;
        clearSessionOutput(id);
        // Bump version so any pending write callback can detect this clear.
        const ver = (writeVersion.value.get(id) ?? 0) + 1;
        writeVersion.value.set(id, ver);
        entry.term.write(chunk, () => {
          // M-13: 如果写入期间版本号变化（发生了另一次 clear），说明此
          // 写入已过期。不追加任何数据到 store —— store 已被 clear 重置，
          // 下一次 watcher tick 会从 store 快照中拾取 clear 后的新输出。
          if (writeVersion.value.get(id) !== ver) return;
          // 版本号未变化：写入正常完成，无需额外操作。
        });
      }
    }
  },
);

onMounted(async () => {
  if (isVisible.value) {
    await initFirstSession();
  }
  if (appState.currentProject) {
    loadTasks(appState.currentProject);
    loadWorkflows(appState.currentProject);
  }
});

// N-150: track pending setTimeout so it can be cleared on unmount.
// C-5: moved into setup() (was module-level `let`) so each instance owns
// its own timer and only clears its own timer on unmount.
// N-NEW-2: 声明上移到 watch(isVisible) 之前，提升静态可读性（运行时无 TDZ 问题，
// 但原顺序让读者误以为 watch 回调引用了未声明的变量）。
const fitTimer = ref<ReturnType<typeof setTimeout> | null>(null);

watch(isVisible, async (visible) => {
  if (visible) {
    if (terminalState.sessionOrder.length === 0) {
      await initFirstSession();
    } else if (terminalState.activeSessionId) {
      if (!terminals.value.has(terminalState.activeSessionId)) {
        await initTerminalForSession(terminalState.activeSessionId);
      }
      switchToSession(terminalState.activeSessionId);
    }
    fitTimer.value = setTimeout(fitTerminal, 50);
  }
});

onBeforeUnmount(() => {
  if (fitTimer.value !== null) {
    clearTimeout(fitTimer.value);
    fitTimer.value = null;
  }
  // C-5: only dispose terminals owned by THIS instance. Because
  // terminals.value is now per-instance (not module-level), unmounting
  // one TerminalPanel no longer disposes another instance's xterm.
  for (const entry of terminals.value.values()) {
    entry.term.dispose();
  }
});
</script>

<template>
  <transition name="slide-terminal">
    <div
      v-if="isVisible"
      class="terminal-panel"
      :style="{ height: panelHeightPx }"
      role="region"
      :aria-label="t('terminal.panelAria')"
    >
      <div class="terminal-panel__header">
        <div v-if="!embedded" class="terminal-panel__view-tabs" role="tablist" :aria-label="t('terminal.panelViewsAria')" @keydown="handleViewTabKeydown">
          <button
            type="button"
            class="terminal-panel__view-tab"
            :class="{ 'terminal-panel__view-tab--active': activeView === 'terminal' }"
            role="tab"
            :tabindex="activeView === 'terminal' ? 0 : -1"
            :aria-selected="activeView === 'terminal'"
            @click="activeView = 'terminal'"
          >
            {{ t('terminal.terminalTab') }}
          </button>
          <button
            type="button"
            class="terminal-panel__view-tab"
            :class="{ 'terminal-panel__view-tab--active': activeView === 'output' }"
            role="tab"
            :tabindex="activeView === 'output' ? 0 : -1"
            :aria-selected="activeView === 'output'"
            @click="activeView = 'output'"
          >
            {{ t('terminal.outputTab') }}
          </button>
          <button
            type="button"
            class="terminal-panel__view-tab"
            :class="{ 'terminal-panel__view-tab--active': activeView === 'problems' }"
            role="tab"
            :tabindex="activeView === 'problems' ? 0 : -1"
            :aria-selected="activeView === 'problems'"
            @click="activeView = 'problems'"
          >
            {{ t('terminal.problemsTab') }}
            <span v-if="hasProblems" class="terminal-panel__badge">{{ problemCountSummary }}</span>
          </button>
          <button
            type="button"
            class="terminal-panel__view-tab"
            :class="{ 'terminal-panel__view-tab--active': activeView === 'debug' }"
            role="tab"
            :tabindex="activeView === 'debug' ? 0 : -1"
            :aria-selected="activeView === 'debug'"
            @click="activeView = 'debug'"
          >
            Debug
          </button>
          <!-- Priority 7: pprof 性能分析 tab -->
          <button
            type="button"
            class="terminal-panel__view-tab"
            :class="{ 'terminal-panel__view-tab--active': activeView === 'profile' }"
            role="tab"
            :tabindex="activeView === 'profile' ? 0 : -1"
            :aria-selected="activeView === 'profile'"
            @click="activeView = 'profile'"
          >
            Profile
          </button>
          <button
            type="button"
            class="terminal-panel__view-tab"
            :class="{ 'terminal-panel__view-tab--active': activeView === 'tasks' }"
            role="tab"
            :tabindex="activeView === 'tasks' ? 0 : -1"
            :aria-selected="activeView === 'tasks'"
            @click="activeView = 'tasks'"
          >
            {{ t('terminal.tasksTab') }}
            <span v-if="hasTasks" class="terminal-panel__badge">{{ taskState.tasks.length }}</span>
          </button>
          <button
            type="button"
            class="terminal-panel__view-tab"
            :class="{ 'terminal-panel__view-tab--active': activeView === 'workflows' }"
            role="tab"
            :tabindex="activeView === 'workflows' ? 0 : -1"
            :aria-selected="activeView === 'workflows'"
            @click="activeView = 'workflows'"
          >
            {{ t('terminal.workflowsTab') }}
            <span v-if="hasWorkflows" class="terminal-panel__badge terminal-panel__badge--info">{{ workflowState.workflows.length }}</span>
          </button>
        </div>

        <div v-if="activeView === 'terminal'" class="terminal-panel__tabs" role="tablist" :aria-label="t('terminal.terminalTabsAria')" @keydown="handleSessionTabKeydown">
          <div
            v-for="s in sessionList"
            :key="s.id"
            class="terminal-panel__tab"
            :data-session-id="s.id"
            :class="{
              'terminal-panel__tab--active':
                terminalState.activeSessionId === s.id,
            }"
          >
            <button
              type="button"
              class="terminal-panel__tab-select"
              role="tab"
              :tabindex="terminalState.activeSessionId === s.id ? 0 : -1"
              :aria-selected="terminalState.activeSessionId === s.id"
              :aria-label="t('terminal.tabAria', { label: s.label })"
              @click="handleSelectTab(s.id)"
            >
              <span class="terminal-panel__tab-label">{{ s.label }}</span>
            </button>
            <button
              type="button"
              class="terminal-panel__tab-close"
              :aria-label="t('terminal.closeTerminalAria')"
              @click="handleCloseTerminal(s.id)"
            >
              <el-icon :size="11"><Close /></el-icon>
            </button>
            <button
              v-if="!s.running"
              type="button"
              class="terminal-panel__tab-reconnect"
              :disabled="s.reconnecting"
              :aria-label="t('terminal.reconnectTerminalAria')"
              :title="t('terminal.reconnectTerminalTitle')"
              @click="handleReconnectTerminal(s.id)"
            >
              <el-icon :size="11"><Refresh /></el-icon>
            </button>
          </div>
          <button
            type="button"
            class="terminal-panel__new"
            :aria-label="t('terminal.newTerminalAria')"
            :title="t('terminal.newTerminalTitle')"
            @click="handleNewTerminal"
          >
            <el-icon :size="14"><Plus /></el-icon>
          </button>
        </div>

        <div class="terminal-panel__header-actions">
          <button
            type="button"
            v-if="activeView === 'output' || activeView === 'problems'"
            class="terminal-panel__clear"
            :aria-label="t('terminal.clearViewAria', { view: activeView })"
            :title="t('terminal.clearViewTitle', { view: activeView })"
            @click="handleClearCurrentView"
          >
            <el-icon :size="13"><Delete /></el-icon>
          </button>
          <button
            type="button"
            class="terminal-panel__close"
            :aria-label="t('terminal.closePanelAria')"
            :title="t('terminal.closePanelTitle')"
            @click="handleClose"
          >
            <el-icon :size="14"><Close /></el-icon>
          </button>
        </div>
      </div>

      <!-- Terminal view (N-14: ARIA region for screen readers) -->
      <div
        v-show="activeView === 'terminal'"
        ref="terminalContainer"
        class="terminal-panel__body"
        role="region"
        :aria-label="t('terminal.outputRegionAria')"
      />
      <!-- N-142: visually-hidden aria-live region mirroring recent terminal
           output so screen readers can announce new output. xterm renders
           to canvas/divs which are not accessible by default. -->
      <div
        v-show="activeView === 'terminal'"
        class="terminal-panel__sr-live"
        aria-live="polite"
        aria-atomic="false"
        :aria-label="t('terminal.recentOutputAria')"
      >{{ recentOutput }}</div>

      <!-- Output view -->
      <div v-if="activeView === 'output'" class="terminal-panel__body terminal-panel__output">
        <div v-if="!hasOutputs" class="terminal-panel__empty">
          {{ t('terminal.noOutput') }}
        </div>
        <div v-else class="output-list">
          <div
            v-for="entry in outputState.outputs"
            :key="entry.id"
            class="output-row"
            :class="'output-row--' + entry.severity"
          >
            <span class="output-row__time">{{ formatTimestamp(entry.timestamp) }}</span>
            <span class="output-row__source">[{{ entry.source }}]</span>
            <span class="output-row__message">{{ entry.message }}</span>
          </div>
        </div>
      </div>

      <!-- Problems view -->
      <div v-if="activeView === 'problems'" class="terminal-panel__body terminal-panel__problems">
        <div v-if="!hasProblems" class="terminal-panel__empty">
          {{ t('terminal.noProblems') }}
        </div>
        <div v-else class="problem-list">
          <button
            type="button"
            v-for="p in outputState.problems"
            :key="p.id"
            class="problem-row"
            :class="'problem-row--' + p.severity"
            @click="handleProblemClick(p)"
          >
            <span class="problem-row__icon" aria-hidden="true">{{ severityIcon(p.severity) }}</span>
            <span class="problem-row__message">{{ p.message }}</span>
            <span class="problem-row__location">{{ p.file }}:{{ p.line }}:{{ p.column }}</span>
          </button>
        </div>
      </div>

      <!-- prompt-11 11-A: Debug DAP panel -->
      <div v-if="activeView === 'debug'" class="terminal-panel__body terminal-panel__debug">
        <DebugPanel />
      </div>

      <!-- Priority 7: pprof 性能分析面板 -->
      <div v-if="activeView === 'profile'" class="terminal-panel__body terminal-panel__profile">
        <ProfilePanel />
      </div>

      <!-- Tasks view -->
      <div v-if="activeView === 'tasks'" class="terminal-panel__body terminal-panel__tasks">
        <div v-if="taskState.loading" class="terminal-panel__empty">
          {{ t('terminal.loadingTasks') }}
        </div>
        <div v-else-if="taskState.errorMessage" class="terminal-panel__empty terminal-panel__empty--error">
          {{ taskState.errorMessage }}
        </div>
        <div v-else-if="!hasTasks" class="terminal-panel__empty">
          {{ t('terminal.noTasksDefinedPrefix') }} <code>.vscode/tasks.json</code> {{ t('terminal.noTasksDefinedSuffix') }}
        </div>
        <div v-else class="task-list">
          <button
            type="button"
            v-for="task in taskState.tasks"
            :key="task.label"
            class="task-row"
            :title="composeCommandLine(task)"
            @click="handleRunTask(task.label)"
          >
            <span class="task-row__icon" aria-hidden="true">&#9654;</span>
            <span class="task-row__label">{{ task.label }}</span>
            <span class="task-row__command">{{ composeCommandLine(task) }}</span>
          </button>
        </div>
      </div>

      <!-- Workflows view (N-19) -->
      <div v-if="activeView === 'workflows'" class="terminal-panel__body terminal-panel__workflows">
        <div v-if="workflowState.loading" class="terminal-panel__empty">
          {{ t('terminal.loadingWorkflows') }}
        </div>
        <div v-else-if="workflowState.errorMessage" class="terminal-panel__empty terminal-panel__empty--error">
          {{ workflowState.errorMessage }}
        </div>
        <div v-else-if="!hasWorkflows" class="terminal-panel__empty">
          {{ t('terminal.noWorkflowsDefinedPrefix') }} <code>.koyori-ide/workflows/*.yml</code> {{ t('terminal.noWorkflowsDefinedSuffix') }}
        </div>
        <div v-else class="workflow-list">
          <div
            v-for="wf in workflowState.workflows"
            :key="wf.name"
            class="workflow-card"
          >
            <div class="workflow-card__header">
              <span class="workflow-card__name" :title="wf.source">{{ wf.name }}</span>
              <span
                v-if="wf.runOn?.event === 'startup' && wf.requiresConfirmation"
                class="workflow-card__pending-badge"
                :title="t('terminal.pendingConfirmationTitle')"
              >{{ t('terminal.pendingConfirmation') }}</span>
              <span v-if="wf.description" class="workflow-card__desc">{{ wf.description }}</span>
              <button
                type="button"
                class="workflow-card__run"
                :disabled="workflowRunning(wf.name)"
                :title="workflowRunning(wf.name) ? t('terminal.runningTitle') : t('terminal.runWorkflowTitle')"
                @click="handleRunWorkflow(wf.name)"
              >
                <span v-if="workflowRunning(wf.name)" class="workflow-card__spinner" aria-hidden="true"></span>
                <span v-else aria-hidden="true">&#9654;</span>
                <span class="workflow-card__run-label">{{ workflowRunning(wf.name) ? t('terminal.running') : t('terminal.run') }}</span>
              </button>
            </div>
            <ol class="workflow-card__steps">
              <li
                v-for="step in wf.steps"
                :key="step.name"
                class="workflow-step"
                :class="'workflow-step--' + workflowStepStatus(wf.name, step.name)"
              >
                <span class="workflow-step__status-dot" aria-hidden="true"></span>
                <span class="workflow-step__name">{{ step.name }}</span>
                <span class="workflow-step__cmd">{{ composeStepCommandLine(step) }}</span>
              </li>
            </ol>
          </div>
        </div>
      </div>
    </div>
  </transition>
</template>

<style scoped>
.terminal-panel {
  display: flex;
  flex-direction: column;
  min-height: 0;
  background-color: var(--color-terminal-bg);
  overflow: hidden;
  border-top: 1px solid var(--color-border-subtle);
}

.terminal-panel__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  height: 34px;
  min-height: 34px;
  padding: 0 4px 0 8px;
  border-bottom: 1px solid var(--color-border-subtle);
  gap: 8px;
}

.terminal-panel__view-tabs {
  display: flex;
  align-items: center;
  gap: 2px;
  flex-shrink: 0;
}

.terminal-panel__view-tab {
  padding: 4px 10px;
  font-size: var(--font-size-xs, 11px);
  font-family: var(--font-sans);
  font-weight: 500;
  color: var(--color-text-tertiary);
  background: transparent;
  border: none;
  border-radius: var(--radius-sm);
  cursor: pointer;
  white-space: nowrap;
  display: inline-flex;
  align-items: center;
  gap: 4px;
  transition: color var(--transition-fast), background-color var(--transition-fast);
}

.terminal-panel__view-tab:hover {
  color: var(--color-text-secondary);
  background-color: var(--color-bg-surface-container-low);
}

.terminal-panel__view-tab--active {
  color: var(--color-text-primary);
  background-color: var(--color-bg-surface-container-low);
}

.terminal-panel__view-tab:focus-visible {
  outline: 2px solid var(--color-primary-focus);
  outline-offset: -2px;
}

.terminal-panel__badge {
  display: inline-flex;
  align-items: center;
  padding: 1px 6px;
  font-size: 10px;
  font-weight: 500;
  color: var(--color-text-primary);
  background-color: var(--color-error);
  border-radius: var(--radius-xs);
  line-height: 1.4;
}

.terminal-panel__header-actions {
  display: flex;
  align-items: center;
  gap: 2px;
  flex-shrink: 0;
  margin-left: auto;
}

.terminal-panel__clear {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  border: none;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-text-tertiary);
  cursor: pointer;
  transition: color var(--transition-fast), background-color var(--transition-fast);
}

.terminal-panel__clear:hover {
  color: var(--color-text-secondary);
  background-color: color-mix(in srgb, var(--color-text-tertiary) 6%, transparent);
}

.terminal-panel__clear:focus-visible {
  outline: 2px solid var(--color-primary-focus);
  outline-offset: -2px;
}

.terminal-panel__tabs {
  display: flex;
  align-items: center;
  gap: 0;
  overflow-x: auto;
}

.terminal-panel__tab {
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 0;
  font-size: var(--font-size-xs, 11px);
  font-family: var(--font-sans);
  color: var(--color-text-tertiary);
  background: transparent;
  border-radius: var(--radius-sm);
  white-space: nowrap;
  transition:
    color var(--transition-fast),
    background-color var(--transition-fast);
}

.terminal-panel__tab:hover {
  color: var(--color-text-secondary);
  background-color: var(--color-bg-surface-container-low);
}

.terminal-panel__tab--active {
  color: var(--color-text-primary);
  background-color: var(--color-bg-surface-container-low);
}

.terminal-panel__tab-select {
  display: flex;
  align-items: center;
  min-width: 0;
  padding: 4px 0 4px 10px;
  border: 0;
  color: inherit;
  background: transparent;
  font: inherit;
  cursor: pointer;
}

.terminal-panel__tab-select:focus-visible,
.terminal-panel__tab-close:focus-visible {
  outline: 2px solid var(--color-primary-focus);
  outline-offset: -2px;
}

.terminal-panel__tab-label {
  pointer-events: none;
}

.terminal-panel__tab-close {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
  margin-right: 6px;
  padding: 0;
  border: 0;
  border-radius: var(--radius-xs);
  color: inherit;
  background: transparent;
  cursor: pointer;
  opacity: 0.6;
  transition:
    opacity var(--transition-fast),
    background-color var(--transition-fast);
}

.terminal-panel__tab-close:hover {
  opacity: 1;
  background-color: var(--color-bg-surface-container-high);
}

.terminal-panel__tab-reconnect {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
  padding: 0;
  border: 0;
  border-radius: var(--radius-xs);
  color: var(--color-warning, #d97706);
  background: transparent;
  cursor: pointer;
  opacity: 0.9;
}

.terminal-panel__tab-reconnect:hover:not(:disabled) {
  background-color: var(--color-bg-surface-container-high);
}

.terminal-panel__tab-reconnect:disabled {
  cursor: wait;
  opacity: 0.45;
}

.terminal-panel__tab-reconnect:focus-visible {
  outline: 2px solid var(--color-primary-focus);
  outline-offset: -2px;
}

.terminal-panel__new {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  border: none;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-text-tertiary);
  cursor: pointer;
  flex-shrink: 0;
  transition:
    color var(--transition-fast),
    background-color var(--transition-fast);
}

.terminal-panel__new:hover {
  color: var(--color-text-primary);
  background-color: var(--color-bg-surface-container-low);
}

.terminal-panel__new:focus-visible {
  outline: 2px solid var(--color-primary-focus);
  outline-offset: -2px;
}

.terminal-panel__close {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  border: none;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-text-tertiary);
  cursor: pointer;
  flex-shrink: 0;
  transition:
    color var(--transition-fast),
    background-color var(--transition-fast);
}

.terminal-panel__close:hover {
  color: var(--color-text-secondary);
  background-color: color-mix(
    in srgb,
    var(--color-text-tertiary) 6%,
    transparent
  );
}

.terminal-panel__close:focus-visible {
  outline: 2px solid var(--color-primary-focus);
  outline-offset: -2px;
}

.terminal-panel__body {
  flex: 1;
  padding: 4px 8px;
  overflow: hidden;
  position: relative;
}

/* N-142: visually-hidden aria-live region for screen readers. */
.terminal-panel__sr-live {
  position: absolute;
  width: 1px;
  height: 1px;
  padding: 0;
  margin: -1px;
  overflow: hidden;
  clip: rect(0, 0, 0, 0);
  white-space: nowrap;
  border: 0;
}

.terminal-panel__body :deep(.xterm) {
  height: 100%;
}

.terminal-panel__body :deep(.xterm-viewport) {
  overflow-y: auto;
}

.terminal-panel__output,
.terminal-panel__problems {
  overflow-y: auto;
  padding: 4px 8px;
  font-family: var(--font-mono);
  background-color: var(--color-terminal-bg);
}

.terminal-panel__empty {
  display: flex;
  align-items: center;
  justify-content: center;
  height: 100%;
  font-size: var(--font-size-xs, 11px);
  font-family: var(--font-sans);
  color: var(--color-text-tertiary);
}

.output-list,
.problem-list {
  display: flex;
  flex-direction: column;
  gap: 1px;
}

.output-row {
  display: flex;
  align-items: baseline;
  gap: 8px;
  padding: 2px 6px;
  font-size: 11px;
  line-height: 1.5;
  color: var(--color-text-secondary);
  border-radius: var(--radius-xs);
}

.output-row__time {
  color: var(--color-text-tertiary);
  flex-shrink: 0;
}

.output-row__source {
  color: var(--color-text-tertiary);
  font-weight: 500;
  flex-shrink: 0;
}

.output-row__message {
  white-space: pre-wrap;
  word-break: break-word;
  min-width: 0;
}

.output-row--error .output-row__message { color: var(--color-error); }
.output-row--warn .output-row__message { color: var(--color-warning); }
.output-row--success .output-row__message { color: var(--color-success); }

.problem-row {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 8px;
  font-size: 11px;
  line-height: 1.4;
  font-family: var(--font-sans);
  color: var(--color-text-secondary);
  background: transparent;
  border: none;
  border-radius: var(--radius-xs);
  cursor: pointer;
  text-align: left;
  width: 100%;
  transition: background-color var(--transition-fast);
}

.problem-row:hover {
  background-color: var(--color-bg-surface-container-low);
}

.problem-row__icon {
  flex-shrink: 0;
  width: 14px;
  text-align: center;
  font-size: 12px;
}

.problem-row--error .problem-row__icon { color: var(--color-error); }
.problem-row--warning .problem-row__icon { color: var(--color-warning); }
.problem-row--info .problem-row__icon { color: var(--color-info, var(--color-text-tertiary)); }
.problem-row--hint .problem-row__icon { color: var(--color-text-tertiary); }

.problem-row__message {
  flex: 1;
  min-width: 0;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.problem-row__location {
  flex-shrink: 0;
  font-family: var(--font-mono);
  font-size: 10px;
  color: var(--color-text-tertiary);
}

.terminal-panel__empty--error {
  color: var(--color-error);
}

.terminal-panel__empty code {
  font-family: var(--font-mono);
  font-size: 10px;
  padding: 1px 4px;
  background-color: var(--color-bg-surface-container-low);
  border-radius: var(--radius-xs);
}

.task-list {
  display: flex;
  flex-direction: column;
  gap: 1px;
}

.task-row {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 8px;
  font-size: 11px;
  line-height: 1.4;
  font-family: var(--font-sans);
  color: var(--color-text-secondary);
  background: transparent;
  border: none;
  border-radius: var(--radius-xs);
  cursor: pointer;
  text-align: left;
  width: 100%;
  transition: background-color var(--transition-fast);
}

.task-row:hover {
  background-color: var(--color-bg-surface-container-low);
  color: var(--color-text-primary);
}

.task-row__icon {
  flex-shrink: 0;
  width: 12px;
  font-size: 9px;
  color: var(--color-success);
}

.task-row__label {
  flex-shrink: 0;
  font-weight: 500;
  min-width: 80px;
}

.task-row__command {
  flex: 1;
  min-width: 0;
  font-family: var(--font-mono);
  font-size: 10px;
  color: var(--color-text-tertiary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

/* Workflows view (N-19) */
.terminal-panel__workflows {
  overflow-y: auto;
  padding: 6px 8px;
  background-color: var(--color-terminal-bg);
}

/* Priority 7: pprof Profile 面板，允许内部内容滚动。 */
.terminal-panel__profile {
  overflow-y: auto;
  padding: 0;
  background-color: var(--color-terminal-bg);
}

.terminal-panel__badge--info {
  background-color: var(--color-primary);
}

.workflow-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.workflow-card {
  display: flex;
  flex-direction: column;
  border: 1px solid var(--color-border-subtle);
  border-radius: var(--radius-sm);
  background-color: var(--color-bg-surface-container-low);
  overflow: hidden;
}

.workflow-card__header {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 10px;
  background-color: var(--color-bg-surface-container);
  border-bottom: 1px solid var(--color-border-subtle);
}

.workflow-card__name {
  font-family: var(--font-sans);
  font-size: 12px;
  font-weight: 600;
  color: var(--color-text-primary);
  flex-shrink: 0;
}

.workflow-card__desc {
  flex: 1;
  min-width: 0;
  font-size: 11px;
  color: var(--color-text-tertiary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

/* G-SEC-03: badge marking startup workflows that require explicit confirmation. */
.workflow-card__pending-badge {
  flex-shrink: 0;
  font-size: 10px;
  font-weight: 600;
  padding: 2px 6px;
  border-radius: var(--radius-xs);
  color: var(--color-warning, #d97706);
  background-color: var(--color-warning-bg, rgba(217, 119, 6, 0.12));
  text-transform: uppercase;
  letter-spacing: 0.3px;
}

.workflow-card__run {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 3px 10px;
  font-size: 11px;
  font-family: var(--font-sans);
  font-weight: 500;
  color: var(--color-text-on-primary, #fff);
  background-color: var(--color-success);
  border: none;
  border-radius: var(--radius-xs);
  cursor: pointer;
  flex-shrink: 0;
  transition: opacity var(--transition-fast);
}

.workflow-card__run:hover:not(:disabled) {
  opacity: 0.85;
}

.workflow-card__run:disabled {
  cursor: default;
  opacity: 0.6;
}

.workflow-card__run:focus-visible {
  outline: 2px solid var(--color-primary-focus);
  outline-offset: 2px;
}

.workflow-card__run-label {
  line-height: 1;
}

.workflow-card__spinner {
  width: 10px;
  height: 10px;
  border: 1.5px solid currentColor;
  border-top-color: transparent;
  border-radius: 50%;
  animation: workflow-spin 0.8s linear infinite;
}

@keyframes workflow-spin {
  to { transform: rotate(360deg); }
}

.workflow-card__steps {
  list-style: none;
  margin: 0;
  padding: 2px 0;
  counter-reset: none;
}

.workflow-step {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 3px 10px;
  font-size: 11px;
  line-height: 1.4;
  font-family: var(--font-sans);
  color: var(--color-text-secondary);
  border-bottom: 1px solid var(--color-border-subtle);
}

.workflow-step:last-child {
  border-bottom: none;
}

.workflow-step__status-dot {
  flex-shrink: 0;
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background-color: var(--color-text-tertiary);
  opacity: 0.4;
}

.workflow-step--running .workflow-step__status-dot {
  background-color: var(--color-primary);
  opacity: 1;
  animation: workflow-pulse 1s ease-in-out infinite;
}

.workflow-step--success .workflow-step__status-dot {
  background-color: var(--color-success);
  opacity: 1;
}

.workflow-step--failed .workflow-step__status-dot {
  background-color: var(--color-error);
  opacity: 1;
}

.workflow-step--skipped .workflow-step__status-dot {
  background-color: var(--color-text-tertiary);
  opacity: 0.3;
}

.workflow-step--skipped .workflow-step__name,
.workflow-step--skipped .workflow-step__cmd {
  text-decoration: line-through;
  opacity: 0.6;
}

@keyframes workflow-pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.4; }
}

.workflow-step__name {
  flex-shrink: 0;
  font-weight: 500;
  min-width: 80px;
  color: var(--color-text-primary);
}

.workflow-step__cmd {
  flex: 1;
  min-width: 0;
  font-family: var(--font-mono);
  font-size: 10px;
  color: var(--color-text-tertiary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.slide-terminal-enter-active,
.slide-terminal-leave-active {
  transition:
    height var(--transition-normal),
    opacity var(--transition-fast);
  overflow: hidden;
}

.slide-terminal-enter-from,
.slide-terminal-leave-to {
  height: 0;
  opacity: 0;
}

@media (prefers-reduced-motion: reduce) {
  .terminal-panel__tab,
  .terminal-panel__close,
  .terminal-panel__new,
  .terminal-panel__view-tab,
  .terminal-panel__clear {
    transition: none;
  }
  .slide-terminal-enter-active,
  .slide-terminal-leave-active {
    transition: none;
  }
  .workflow-card__spinner,
  .workflow-step--running .workflow-step__status-dot {
    animation: none;
  }
}
</style>
