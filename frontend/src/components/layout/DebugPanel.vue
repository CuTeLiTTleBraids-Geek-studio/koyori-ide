<script setup lang="ts">
// Koyori IDE 组件 · Debug Panel。
// 喵，这是 Debug Panel，负责 Koyori IDE 的界面呈现喵~
/**
 * prompt-11/12: DAP panel — stack, locals, watches, restart, conditions.
 * prompt-5: 多会话切换 / 函数断点 / SetVariable / RestartFrame / InlineValues
 */
import { nextTick, onBeforeUnmount, onMounted, ref } from "vue";
import {
  debugState,
  refreshDebugStatus,
  launchDebugPackage,
  stopDebugSession,
  restartDebugSession,
  debugContinue,
  debugStepOver,
  debugStepIn,
  debugStepOut,
  selectDebugFrame,
  refreshStackAndLocals,
  addWatch,
  removeWatch,
  evaluateExpression,
  setBreakpointCondition,
  launchWithConfig,
  loadLaunchConfigs,
  probeAndAttachDelve,
  exportLaunchConfigsJSON,
  importLaunchConfigsJSON,
  // prompt-5: 调试器增强
  addFunctionBreakpoint,
  removeFunctionBreakpoint,
  applyFunctionBreakpoints,
  setVariable,
  restartFrame,
  refreshInlineValues,
  startEditVariable,
  cancelEditVariable,
  switchSession,
  startDebugSession,
  stopDebugSessionByID,
  selectBrowserTarget,
  // F-5 (task-1.md): Data breakpoints
  fetchDataBreakpointInfo,
  addDataBreakpoint,
  removeDataBreakpoint,
  clearDataBreakpoints,
  toggleVariableExpansion,
  // F-7 (task-1.md): Debug auxiliary
  fetchExceptionInfo,
  fetchCompletions,
  // GOAL-P1-03: stop-aware step-in target enumeration + targeted step.
  // The bare `fetchStepInTargets` returns a list that cannot be validated
  // against the current stop, which is what allowed the selected ID to be
  // silently dropped.
  fetchStepInTargetsForStop,
  debugStepInTarget,
} from "@/stores/debug";

const props = withDefaults(defineProps<{ showStatus?: boolean }>(), {
  showStatus: true,
});
import { openFileFromPath } from "@/stores/editor";
import { appState, requestEditorJump } from "@/stores/app";
import { layoutState } from "@/stores/layout";
import { notifySuccess } from "@/lib/notifications";
import { useI18n } from "@/lib/i18n";
import DebugCallStack from "./DebugCallStack.vue";
import type { DebugVariable } from "@/stores/debug";
import * as DebugServiceBindings from "../../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/debugservice.js";

const { t } = useI18n();
const renderedItemKeys = new WeakMap<object, string>();
let renderedItemKeySequence = 0;

function renderedItemKey(item: object): string {
  const existing = renderedItemKeys.get(item);
  if (existing) return existing;
  const key = `debug-item-${++renderedItemKeySequence}`;
  renderedItemKeys.set(item, key);
  return key;
}

const condFile = ref("");
const condLine = ref(1);
const condExpr = ref("");
const conditionInputRef = ref<HTMLInputElement | null>(null);
const importJSON = ref("");
const browserView = ref<"console" | "network">("console");
const editingWatchExpr = ref("");
const watchDraft = ref("");
const watchEditInputRef = ref<HTMLInputElement | null>(null);

interface ReplEntry {
  id: number;
  expression: string;
  result: string;
}

const replEntries = ref<ReplEntry[]>([]);
const replHistoryIndex = ref(-1);
let replSequence = 0;



onMounted(() => {
  void loadLaunchConfigs(appState.currentProject ?? undefined);
  void refreshDebugStatus();
  document.addEventListener("click", closeFloatingMenus);
  document.addEventListener("keydown", onDocumentKeydown);
});

onBeforeUnmount(() => {
  document.removeEventListener("click", closeFloatingMenus);
  document.removeEventListener("keydown", onDocumentKeydown);
});

async function jumpFrame(file: string, line: number, frameId: number) {
  if (frameId) {
    await selectDebugFrame(frameId);
    // prompt-5: 切换帧后拉取该帧的 inline values
    // G14: no hardcoded reference — frame-driven inline values.
    void refreshInlineValues(frameId, 0);
  }
  if (file) {
    try {
      await openFileFromPath(file);
      requestEditorJump(file, line || 1, 1, layoutState.tree.activeLeafId);
    } catch {
      /* notified */
    }
  }
}

async function onAddWatch() {
  const e = debugState.watchInput.trim();
  if (!e) return;
  await addWatch(e);
  await refreshWatchValue(e);
  debugState.watchInput = "";
}

async function onEvaluate() {
  const e = debugState.evaluateInput.trim();
  if (!e) return;
  try {
    debugState.evaluateResult = await DebugServiceBindings.EvaluateREPL(e);
  } catch {
    await evaluateExpression(e);
  }
  replEntries.value.push({
    id: ++replSequence,
    expression: e,
    result: debugState.evaluateResult || t("debug.replNoResult"),
  });
  if (replEntries.value.length > 100) replEntries.value.shift();
  debugState.evaluateInput = "";
  debugState.completionItems = [];
  replHistoryIndex.value = -1;
}

async function onSetCondition() {
  if (!condFile.value || !condLine.value) return;
  await applyConditionalBreakpoint(condFile.value, condLine.value, condExpr.value);
  condFile.value = "";
  condExpr.value = "";
}

async function editCondition(b: { file: string; line: number; condition?: string }) {
  closeBreakpointMenu();
  condFile.value = b.file;
  condLine.value = b.line;
  condExpr.value = b.condition || "";
  await nextTick();
  conditionInputRef.value?.focus();
}

function cancelConditionEdit(): void {
  condFile.value = "";
  condExpr.value = "";
}

function startEditWatch(expression: string): void {
  editingWatchExpr.value = expression;
  watchDraft.value = expression;
  void nextTick(() => watchEditInputRef.value?.focus());
}

function setWatchEditInput(element: unknown): void {
  watchEditInputRef.value = element instanceof HTMLInputElement ? element : null;
}

function cancelWatchEdit(): void {
  editingWatchExpr.value = "";
  watchDraft.value = "";
}

async function commitWatchEdit(): Promise<void> {
  const previous = editingWatchExpr.value;
  const next = watchDraft.value.trim();
  if (!previous || !next) return;
  if (previous !== next) {
    await removeWatch(previous);
    await addWatch(next);
    await refreshWatchValue(next);
  }
  cancelWatchEdit();
}

async function refreshWatchValue(expression: string): Promise<void> {
  try {
    const value = await DebugServiceBindings.EvaluateWatch(expression);
    const watch = debugState.watches.find((item) => item.name === expression);
    if (watch) watch.value = value;
  } catch {
    // addWatch already evaluated and surfaced any error through the store.
  }
}

async function applyConditionalBreakpoint(file: string, line: number, condition: string): Promise<void> {
  try {
    await DebugServiceBindings.SetConditionalBreakpoint(file, line, condition);
    await refreshDebugStatus();
  } catch {
    // Compatibility fallback for older backends without the prompt-3 alias.
    await setBreakpointCondition(file, line, condition);
  }
}

function navigateReplHistory(direction: -1 | 1): void {
  if (!replEntries.value.length) return;
  if (direction === 1 && replHistoryIndex.value >= replEntries.value.length - 1) {
    replHistoryIndex.value = -1;
    debugState.evaluateInput = "";
    return;
  }
  const nextIndex = replHistoryIndex.value < 0
    ? replEntries.value.length - 1
    : Math.min(replEntries.value.length - 1, Math.max(0, replHistoryIndex.value + direction));
  replHistoryIndex.value = nextIndex;
  debugState.evaluateInput = replEntries.value[nextIndex]?.expression ?? "";
}

function onReplKeydown(event: KeyboardEvent): void {
  if (event.key === "ArrowUp") {
    event.preventDefault();
    navigateReplHistory(-1);
  } else if (event.key === "ArrowDown") {
    event.preventDefault();
    navigateReplHistory(1);
  }
}

function copyLaunchConfigs() {
  const j = exportLaunchConfigsJSON();
  void globalThis.navigator?.clipboard?.writeText(j);
  notifySuccess("Launch configs copied");
}

function doImportLaunchConfigs() {
  const n = importLaunchConfigsJSON(importJSON.value);
  if (n > 0) notifySuccess(`Imported ${n} config(s)`);
}

// prompt-5: 添加函数断点 (从输入框)
async function onAddFuncBp() {
  const name = debugState.newFuncBpName.trim();
  if (!name) return;
  await addFunctionBreakpoint(name, debugState.newFuncBpCondition, debugState.newFuncBpHitCondition);
}

// prompt-5: 启动新会话 (使用当前选中的 launch config)
async function onStartSession() {
  const name = debugState.activeConfigName;
  const cfg = debugState.launchConfigs.find((c) => c.name === name);
  if (!cfg) {
    void launchDebugPackage();
    return;
  }
  await startDebugSession({ ...cfg, dir: cfg.dir || appState.currentProject || "" });
}

// prompt-5: 会话切换
async function onSwitchSession(id: string) {
  await switchSession(id);
}

// prompt-5: 停止指定会话
async function onStopSession(id: string) {
  await stopDebugSessionByID(id);
}

// prompt-5: 变量内联编辑 — 提交
async function onCommitVarEdit(name: string) {
  await setVariable(debugState.editingVarRef, name, debugState.editingVarValue);
}

// prompt-5: 重启栈帧
async function onRestartFrame(frameId: number) {
  await restartFrame(frameId);
}

// F-7: 刷新按钮 — 刷新栈/局部变量 + 辅助信息 (异常/源/模块)
async function onRefresh() {
  await refreshStackAndLocals();
  await refreshAuxiliary();
}

// ============================================================================
// F-5 (task-1.md): Data breakpoint right-click menu on Locals
// ============================================================================

// 右键菜单状态：visible + 坐标 + 候选 info 列表
const dataBpMenu = ref({
  visible: false,
  x: 0,
  y: 0,
  infos: [] as { dataId: string; description: string; accessTypes?: string[] }[],
});

interface BreakpointMenuTarget {
  file: string;
  line: number;
  condition?: string;
}

const breakpointMenu = ref({
  visible: false,
  x: 0,
  y: 0,
  target: null as BreakpointMenuTarget | null,
});

function onBreakpointContextmenu(e: MouseEvent, breakpoint: BreakpointMenuTarget): void {
  e.preventDefault();
  e.stopPropagation();
  dataBpMenu.value.visible = false;
  stepInMenu.value.visible = false;
  breakpointMenu.value = {
    visible: true,
    x: e.clientX,
    y: e.clientY,
    target: breakpoint,
  };
}

function closeBreakpointMenu(): void {
  breakpointMenu.value.visible = false;
  breakpointMenu.value.target = null;
}

async function editBreakpointConditionFromMenu(): Promise<void> {
  const target = breakpointMenu.value.target;
  if (!target) return;
  await editCondition(target);
}

async function clearBreakpointConditionFromMenu(): Promise<void> {
  const target = breakpointMenu.value.target;
  if (!target) return;
  closeBreakpointMenu();
  await applyConditionalBreakpoint(target.file, target.line, "");
}

function closeFloatingMenus(event?: MouseEvent): void {
  const target = event?.target;
  if (target instanceof Element && target.closest(".debug-panel__menu")) return;
  closeDataBpMenu();
  closeBreakpointMenu();
  closeStepInMenu();
}

function onDocumentKeydown(event: KeyboardEvent): void {
  if (event.key === "Escape") closeFloatingMenus();
}

/** G14: nested children already loaded for a variable's reference. */
function expandedChildren(v: DebugVariable): DebugVariable[] {
  const ref = v.variablesReference ?? 0;
  return ref > 0 ? (debugState.expandedVariables[ref] ?? []) : [];
}

/** F-5: 变量右键 — 查询可设数据断点信息并弹出菜单 */
async function onVariableContextmenu(e: MouseEvent, varName: string, variablesReference: number) {
  e.preventDefault();
  if (!debugState.running || !debugState.stopped) return;
  // G14: use the adapter-owned variablesReference, never a hardcoded id.
  const infos = await fetchDataBreakpointInfo(variablesReference, varName);
  if (!infos.length) return;
  dataBpMenu.value = {
    visible: true,
    x: e.clientX,
    y: e.clientY,
    infos,
  };
}

/** F-5: 关闭数据断点右键菜单 */
function closeDataBpMenu() {
  dataBpMenu.value.visible = false;
}

/** F-5: 选择 accessType 后添加数据断点 */
async function onPickDataBpAccess(
  info: { dataId: string; description: string; accessTypes?: string[] },
  accessType: string,
) {
  await addDataBreakpoint(info as never, accessType);
  closeDataBpMenu();
}

/** F-5: 移除数据断点 */
async function onRemoveDataBp(dataId: string, accessType: string) {
  await removeDataBreakpoint(dataId, accessType);
}

/** F-5: 清空所有数据断点 */
async function onClearDataBps() {
  await clearDataBreakpoints();
}

// ============================================================================
// F-7 (task-1.md): Exception panel + StepIn target picker + Console completion
// ============================================================================

// StepIn target 选择菜单
const stepInMenu = ref({ visible: false, targets: [] as { id: number; label: string }[] });

/**
 * F-7 / GOAL-P1-03: StepIn button — enumerate targets, show a menu only when
 * the adapter actually offers a choice.
 */
async function onStepInClick() {
  if (!debugState.running || !debugState.stopped) return;
  const frame = debugState.stack[0];
  const frameId = frame?.id || 0;
  if (frameId <= 0) {
    // No frame context, so there is nothing to enumerate against.
    await debugStepIn();
    return;
  }
  const set = await fetchStepInTargetsForStop(frameId);
  const targets = set.targets ?? [];
  // Zero targets, or an adapter without stepInTargets support, means the only
  // available behaviour is the default step-in. One target is also the default
  // path: DAP treats an omitted targetId as "the natural target", and that is
  // the same place the single enumerated entry points to, so sending the ID
  // adds a staleness failure mode without changing where execution lands.
  if (!set.supported || targets.length <= 1) {
    await debugStepIn();
    return;
  }
  stepInMenu.value = { visible: true, targets };
}

/**
 * F-7 / GOAL-P1-03: step into the target the user picked.
 *
 * This previously took `_targetId` and discarded it, calling the plain step-in
 * instead — so choosing overload B stepped into A. The ID is now delivered to
 * `stepIn.arguments.targetId`. If the backend refuses (stale menu, unsupported
 * adapter), fall back to a default step so the click is never a no-op.
 */
async function onPickStepInTarget(targetId: number) {
  stepInMenu.value.visible = false;
  if (!(await debugStepInTarget(targetId))) {
    await debugStepIn();
  }
}

/** F-7: 关闭 StepIn 菜单 (不步进) */
function closeStepInMenu() {
  stepInMenu.value.visible = false;
}

/** F-7: 异常停止时拉取 ExceptionInfo (在状态变化后由 Watcher/手动调用) */
async function maybeFetchExceptionInfo() {
  if (!debugState.running || !debugState.stopped) return;
  // 仅在 stopReason 为 exception 时拉取
  const reason = debugState.stopReason || "";
  if (!/exception/i.test(reason)) return;
  const tid = debugState.stack[0]?.id || 0;
  if (tid <= 0) return;
  await fetchExceptionInfo(tid);
}

/** F-7: 调试控制台补全 — Evaluate 输入框 input 事件 */
async function onEvaluateInput() {
  if (!debugState.running || !debugState.stopped) return;
  const text = debugState.evaluateInput;
  if (!text) {
    debugState.completionItems = [];
    return;
  }
  const frame = debugState.stack[0];
  const frameId = frame?.id || 0;
  if (frameId <= 0) return;
  // column = 文本长度 + 1 (简化：在行尾补全)
  await fetchCompletions(frameId, text, text.length + 1);
}

/** F-7: 选择补全项 — 插入到 Evaluate 输入框 */
function onPickCompletion(label: string) {
  debugState.evaluateInput = label;
  debugState.completionItems = [];
}

/** F-7: 刷新辅助信息 (源/模块) — 由用户点击刷新按钮时附加调用 */
async function refreshAuxiliary() {
  // 异常信息仅在异常停止时拉取
  await maybeFetchExceptionInfo();
}

// F-5: 数据断点 accessType 图标 (read=👁 write=✏ readWrite=↔)
function accessIcon(accessType: string): string {
  switch (accessType) {
    case "read":
      return "👁";
    case "readWrite":
      return "↔";
    case "write":
    default:
      return "✏";
  }
}

// F-5: 数据断点 accessType 显示标签
function accessLabel(accessType: string): string {
  switch (accessType) {
    case "read":
      return t("debug.f5f7.breakOnRead");
    case "readWrite":
      return t("debug.f5f7.breakOnReadWrite");
    case "write":
    default:
      return t("debug.f5f7.breakOnWrite");
  }
}
</script>

<template>
  <div class="debug-panel">
    <div class="debug-panel__toolbar">
      <button type="button" class="debug-panel__btn" :disabled="debugState.busy" @click="launchDebugPackage">
        ▶ Start
      </button>
      <button
        type="button"
        class="debug-panel__btn"
        :disabled="debugState.busy || !debugState.activeConfigName"
        @click="onStartSession"
        :title="t('debug.startSessionHint')"
      >
        + Session
      </button>
      <button type="button" class="debug-panel__btn" :disabled="!debugState.running" @click="restartDebugSession">
        Restart
      </button>
      <button type="button" class="debug-panel__btn" :disabled="!debugState.running || !debugState.stopped" @click="debugContinue">
        Continue
      </button>
      <button type="button" class="debug-panel__btn" :disabled="!debugState.running || !debugState.stopped" @click="debugStepOver">
        Step Over
      </button>
      <button
        type="button"
        class="debug-panel__btn"
        data-test="step-in"
        :disabled="!debugState.running || !debugState.stopped"
        @click="onStepInClick"
        :title="t('debug.f5f7.stepInHint')"
      >
        Step In
      </button>
      <button type="button" class="debug-panel__btn" :disabled="!debugState.running || !debugState.stopped" @click="debugStepOut">
        Step Out
      </button>
      <button type="button" class="debug-panel__btn" :disabled="!debugState.running" @click="stopDebugSession">
        Stop
      </button>
      <button type="button" class="debug-panel__btn" :disabled="!debugState.running" @click="onRefresh">
        Refresh
      </button>
    </div>

    <!-- prompt-5: 多会话切换器 -->
    <div class="debug-panel__sessions" v-if="debugState.sessions.length">
      <label>Sessions:</label>
      <select
        class="debug-panel__select"
        :value="debugState.activeSessionID"
        @change="(e) => onSwitchSession((e.target as HTMLSelectElement).value)"
      >
        <option v-for="s in debugState.sessions" :key="s.id" :value="s.id">
          {{ s.id }}{{ s.active ? " *" : "" }} — {{ s.running ? (s.stopped ? "paused" : "running") : "idle" }}{{ s.mode ? ` (${s.mode})` : "" }}
        </option>
      </select>
      <button
        type="button"
        class="debug-panel__btn"
        :disabled="!debugState.activeSessionID"
        @click="onStopSession(debugState.activeSessionID)"
        :title="t('debug.stopSessionHint')"
      >
        Stop Session
      </button>
    </div>

    <div v-if="props.showStatus" class="debug-panel__status" aria-live="polite">
      {{ debugState.message || (debugState.available ? "Delve ready" : "Delve not found") }}
      <span v-if="debugState.stopped" class="debug-panel__paused">
        ·paused: <strong>{{ debugState.stopReason || "stopped" }}</strong>
      </span>
      <span v-if="debugState.mode" class="debug-panel__mode"> · {{ debugState.mode }}</span>
    </div>
    <div v-if="props.showStatus && debugState.lastError" class="debug-panel__error" role="alert">
      ⚠ {{ debugState.lastError }}
    </div>
    <!-- F-7: 异常停止面板 (ExceptionInfo) -->
    <div v-if="debugState.exceptionInfo" class="debug-panel__exception" role="alert">
      <div class="debug-panel__exception-head">
        <strong>⚠ {{ debugState.exceptionInfo.exceptionId }}</strong>
        <span class="debug-panel__exception-mode">[{{ debugState.exceptionInfo.breakMode }}]</span>
        <button type="button" class="debug-panel__x" :aria-label="t('common.close')" @click="debugState.exceptionInfo = null">×</button>
      </div>
      <div v-if="debugState.exceptionInfo.description" class="debug-panel__exception-desc">
        {{ debugState.exceptionInfo.description }}
      </div>
      <div v-if="debugState.exceptionInfo.details" class="debug-panel__exception-details">
        <div v-if="debugState.exceptionInfo.details.message">
          <span class="debug-panel__var">Message:</span> {{ debugState.exceptionInfo.details.message }}
        </div>
        <div v-if="debugState.exceptionInfo.details.typeName">
          <span class="debug-panel__var">Type:</span> {{ debugState.exceptionInfo.details.typeName }}
        </div>
        <pre v-if="debugState.exceptionInfo.details.stackTrace" class="debug-panel__exception-stack">{{ debugState.exceptionInfo.details.stackTrace }}</pre>
      </div>
    </div>
    <div class="debug-panel__attach">
      <input v-model="debugState.attachAddr" class="debug-panel__input" placeholder="127.0.0.1:2345 remote dlv" />
      <button type="button" class="debug-panel__btn" @click="probeAndAttachDelve()">Probe+Attach</button>
      <button type="button" class="debug-panel__btn" @click="copyLaunchConfigs">
        Export JSON
      </button>
    </div>
    <div class="debug-panel__import">
      <textarea v-model="importJSON" class="debug-panel__ta" rows="2" placeholder='Import launch JSON…' />
      <button type="button" class="debug-panel__btn" @click="doImportLaunchConfigs">
        Import
      </button>
    </div>

    <div class="debug-panel__configs" v-if="debugState.launchConfigs.length">
      <label>Launch:</label>
      <select
        class="debug-panel__select"
        :value="debugState.activeConfigName"
        @change="
          (e) => {
            const name = (e.target as HTMLSelectElement).value;
            const cfg = debugState.launchConfigs.find((c) => c.name === name);
            if (cfg) void launchWithConfig({ ...cfg, dir: cfg.dir || appState.currentProject || '' });
          }
        "
      >
        <option value="" disabled>Select config…</option>
        <option v-for="c in debugState.launchConfigs" :key="c.name" :value="c.name">{{ c.name }}</option>
      </select>
    </div>

    <div v-if="debugState.mode === 'browser'" class="debug-panel__browser">
      <div class="debug-panel__browser-bar">
        <label for="browser-debug-target">Target</label>
        <select
          id="browser-debug-target"
          class="debug-panel__select debug-panel__target-select"
          :value="debugState.browserTargetId"
          :disabled="debugState.busy"
          @change="(e) => selectBrowserTarget((e.target as HTMLSelectElement).value)"
        >
          <option v-for="target in debugState.browserTargets" :key="target.id" :value="target.id">
            {{ target.title || target.url || target.id }}
          </option>
        </select>
        <div class="debug-panel__browser-tabs" role="tablist" :aria-label="t('a11y.browserDebugData')">
          <button
            type="button"
            role="tab"
            class="debug-panel__browser-tab"
            :class="{ 'is-active': browserView === 'console' }"
            :aria-selected="browserView === 'console'"
            @click="browserView = 'console'"
          >
            Console
          </button>
          <button
            type="button"
            role="tab"
            class="debug-panel__browser-tab"
            :class="{ 'is-active': browserView === 'network' }"
            :aria-selected="browserView === 'network'"
            @click="browserView = 'network'"
          >
            Network
          </button>
        </div>
      </div>
      <div v-if="browserView === 'console'" class="debug-panel__browser-log" role="tabpanel">
        <div v-for="entry in debugState.browserConsole" :key="renderedItemKey(entry)" class="debug-panel__browser-row debug-panel__browser-row--console">
          <span class="debug-panel__event-kind" :class="`is-${entry.level}`">{{ entry.level || "log" }}</span>
          <span class="debug-panel__event-text">{{ entry.text }}</span>
          <span v-if="entry.url" class="debug-panel__event-meta">{{ entry.url }}{{ entry.line ? `:${entry.line}` : "" }}</span>
        </div>
        <p v-if="!debugState.browserConsole.length" class="debug-panel__empty">No console messages</p>
      </div>
      <div v-else class="debug-panel__browser-log" role="tabpanel">
        <div v-for="entry in debugState.browserNetwork" :key="renderedItemKey(entry)" class="debug-panel__browser-row">
          <span class="debug-panel__event-kind">{{ entry.status || entry.phase }}</span>
          <span class="debug-panel__event-method">{{ entry.method || entry.phase }}</span>
          <span class="debug-panel__event-text">{{ entry.url || entry.error || entry.requestId }}</span>
          <span v-if="entry.mimeType" class="debug-panel__event-meta">{{ entry.mimeType }}</span>
        </div>
        <p v-if="!debugState.browserNetwork.length" class="debug-panel__empty">No network activity</p>
      </div>
    </div>

    <div class="debug-panel__cols">
      <section class="debug-panel__section">
        <h4>Call stack</h4>
        <DebugCallStack
          @select-frame="jumpFrame"
          @restart-frame="onRestartFrame"
        />
        <!-- prompt-5: 内联值 (inlineValues) -->
        <div v-if="debugState.inlineValues.length" class="debug-panel__inline">
          <h4 class="debug-panel__sub">Inline values</h4>
          <ul class="debug-panel__list">
            <li v-for="iv in debugState.inlineValues" :key="renderedItemKey(iv)" class="debug-panel__item">
              <span v-if="iv.type === 'variable'">
                <span class="debug-panel__var">{{ iv.name }}</span> = {{ iv.value }}
              </span>
              <span v-else>{{ iv.text || iv.value }}</span>
            </li>
          </ul>
        </div>
      </section>

      <section class="debug-panel__section">
        <h4>Locals</h4>
        <ul v-if="debugState.locals.length" class="debug-panel__list">
          <li
            v-for="v in debugState.locals"
            :key="renderedItemKey(v)"
            class="debug-panel__item"
            @contextmenu="onVariableContextmenu($event, v.name, v.variablesReference ?? 0)"
            :title="t('debug.f5f7.breakOnChange')"
          >
            <button
              v-if="(v.variablesReference ?? 0) > 0"
              type="button"
              class="debug-panel__expand"
              :aria-label="expandedChildren(v) ? t('outline.collapse') : t('outline.expand')"
              @click.stop="toggleVariableExpansion(v)"
            >
              {{ expandedChildren(v) ? "▾" : "▸" }}
            </button>
            <span class="debug-panel__var">{{ v.name }}</span>
            <span class="debug-panel__type" v-if="v.type">: {{ v.type }}</span>
            <!-- prompt-5: 内联编辑 -->
            <span v-if="debugState.editingVarName === v.name" class="debug-panel__edit">
              <input
                v-model="debugState.editingVarValue"
                class="debug-panel__input debug-panel__input--edit"
                @keydown.enter="onCommitVarEdit(v.name)"
                @keydown.esc="cancelEditVariable"
              />
              <button type="button" class="debug-panel__link" :aria-label="t('common.save')" @click.stop="onCommitVarEdit(v.name)">✓</button>
              <button type="button" class="debug-panel__link" :aria-label="t('common.cancel')" @click.stop="cancelEditVariable">×</button>
            </span>
            <span v-else>
              <div class="debug-panel__val">{{ v.value }}</div>
              <button
                type="button"
                class="debug-panel__link"
                :disabled="!debugState.running || !debugState.stopped"
                @click.stop="startEditVariable(v.variablesReference ?? 0, v.name, v.value)"
                :title="t('debug.setVariableHint')"
              >
                ✎ set
              </button>
            </span>
            <!-- G14: nested variables expanded via adapter-owned reference -->
            <ul v-if="expandedChildren(v).length" class="debug-panel__nested">
              <li v-for="c in expandedChildren(v)" :key="renderedItemKey(c)" class="debug-panel__item debug-panel__item--nested">
                <button
                  v-if="(c.variablesReference ?? 0) > 0"
                  type="button"
                  class="debug-panel__expand"
                  :aria-label="expandedChildren(c) ? t('outline.collapse') : t('outline.expand')"
                  @click.stop="toggleVariableExpansion(c)"
                >
                  {{ expandedChildren(c) ? "▾" : "▸" }}
                </button>
                <span class="debug-panel__var">{{ c.name }}</span>
                <span class="debug-panel__type" v-if="c.type">: {{ c.type }}</span>
                <span class="debug-panel__val">{{ c.value }}</span>
                <ul v-if="expandedChildren(c).length" class="debug-panel__nested">
                  <li v-for="g in expandedChildren(c)" :key="renderedItemKey(g)" class="debug-panel__item debug-panel__item--nested">
                    <span class="debug-panel__var">{{ g.name }}</span>
                    <span class="debug-panel__type" v-if="g.type">: {{ g.type }}</span>
                    <span class="debug-panel__val">{{ g.value }}</span>
                  </li>
                </ul>
              </li>
            </ul>
          </li>
        </ul>
        <p v-else class="debug-panel__empty">No locals.</p>

        <h4 class="debug-panel__sub">{{ t("debug.watch") }}</h4>
        <div class="debug-panel__row">
          <input v-model="debugState.watchInput" class="debug-panel__input" :placeholder="t('debug.watchExpression')" :aria-label="t('debug.watchExpression')" @keydown.enter="onAddWatch" />
          <button type="button" class="debug-panel__btn" :aria-label="t('debug.addWatch')" @click="onAddWatch">+</button>
        </div>
        <ul v-if="debugState.watches.length" class="debug-panel__list" aria-live="polite">
          <li v-for="v in debugState.watches" :key="v.name" class="debug-panel__item debug-panel__watch-item">
            <template v-if="editingWatchExpr === v.name">
              <input
                :ref="setWatchEditInput"
                v-model="watchDraft"
                class="debug-panel__input"
                :aria-label="t('debug.watchExpression')"
                @keydown.enter.prevent="commitWatchEdit"
                @keydown.esc.prevent="cancelWatchEdit"
              />
              <button type="button" class="debug-panel__link" :aria-label="t('debug.saveWatch')" @click="commitWatchEdit">✓</button>
              <button type="button" class="debug-panel__x" :aria-label="t('debug.cancelWatchEdit')" @click="cancelWatchEdit">×</button>
            </template>
            <template v-else>
              <span class="debug-panel__watch-value"><span class="debug-panel__var">{{ v.name }}</span> = {{ v.value }}</span>
              <button type="button" class="debug-panel__link" :aria-label="t('debug.editWatch', { expression: v.name })" @click="startEditWatch(v.name)">✎</button>
              <button type="button" class="debug-panel__x" :aria-label="t('debug.removeWatch', { expression: v.name })" @click="removeWatch(v.name)">×</button>
            </template>
          </li>
        </ul>
        <h4 class="debug-panel__sub">{{ t("debug.repl") }}</h4>
        <div class="debug-panel__repl-log" role="log" aria-live="polite" :aria-label="t('debug.replOutput')">
          <div v-for="entry in replEntries" :key="entry.id" class="debug-panel__repl-entry">
            <div class="debug-panel__repl-expression">&gt; {{ entry.expression }}</div>
            <div class="debug-panel__repl-result">{{ entry.result }}</div>
          </div>
          <div v-if="!replEntries.length && debugState.evaluateResult" class="debug-panel__repl-result">
            {{ debugState.evaluateResult }}
          </div>
        </div>
        <div class="debug-panel__row debug-panel__eval">
          <input
            v-model="debugState.evaluateInput"
            class="debug-panel__input"
            :placeholder="t('debug.replInput')"
            :aria-label="t('debug.replInput')"
            @keydown.enter="onEvaluate"
            @keydown="onReplKeydown"
            @input="onEvaluateInput"
          />
          <button type="button" class="debug-panel__btn" :aria-label="t('debug.runRepl')" @click="onEvaluate">{{ t("debug.runRepl") }}</button>
          <!-- F-7: 调试控制台补全列表 -->
          <ul v-if="debugState.completionItems.length" class="debug-panel__completions" role="listbox" :aria-label="t('debug.f5f7.completions')">
            <li
              v-for="c in debugState.completionItems"
              :key="renderedItemKey(c)"
              class="debug-panel__completion-item"
              role="option"
              tabindex="0"
              @click="onPickCompletion(c.label)"
              @keydown.enter.prevent="onPickCompletion(c.label)"
              @keydown.space.prevent="onPickCompletion(c.label)"
            >
              <span class="debug-panel__var">{{ c.label }}</span>
              <span v-if="c.type" class="debug-panel__type">: {{ c.type }}</span>
            </li>
          </ul>
        </div>
      </section>

      <section class="debug-panel__section">
        <h4>Breakpoints ({{ debugState.breakpoints.length }})</h4>
        <ul v-if="debugState.breakpoints.length" class="debug-panel__list">
          <li
            v-for="b in debugState.breakpoints"
            :key="`${b.file}:${b.line}`"
            class="debug-panel__item debug-panel__item--breakpoint"
            :class="{ 'debug-panel__item--unverified': !b.verified && debugState.running }"
            @contextmenu="onBreakpointContextmenu($event, b)"
          >
            <button
              type="button"
              class="debug-panel__breakpoint-select"
              @click="jumpFrame(b.file, b.line, 0)"
            >
              <span class="debug-panel__bp-dot" :class="b.verified || !debugState.running ? 'is-ok' : 'is-warn'" />
              {{ b.file.split(/[\\/]/).pop() }}:{{ b.line }}
              <span v-if="b.condition" class="debug-panel__cond"> if {{ b.condition }}</span>
              <span v-if="!b.verified && debugState.running" class="debug-panel__unverified" :title="b.message || 'unverified'">
                ⚠ unverified
              </span>
            </button>
            <button type="button" class="debug-panel__link" @click.stop="editCondition(b)">{{ t("debug.editConditionalBreakpoint") }}</button>
          </li>
        </ul>
        <p v-else class="debug-panel__empty">F9 or glyph margin to set.</p>
        <div class="debug-panel__cond-form" v-if="condFile">
          <div class="debug-panel__loc">{{ condFile }}:{{ condLine }}</div>
          <input ref="conditionInputRef" v-model="condExpr" class="debug-panel__input" :placeholder="t('debug.conditionPlaceholder')" :aria-label="t('debug.conditionPlaceholder')" @keydown.enter.prevent="onSetCondition" @keydown.esc.prevent="cancelConditionEdit" />
          <div class="debug-panel__row">
            <button type="button" class="debug-panel__btn" @click="onSetCondition">{{ t("debug.setCondition") }}</button>
            <button type="button" class="debug-panel__btn" @click="cancelConditionEdit">{{ t("common.cancel") }}</button>
          </div>
        </div>

        <!-- prompt-5: 函数断点 -->
        <h4 class="debug-panel__sub">Function breakpoints ({{ debugState.functionBreakpoints.length }})</h4>
        <div class="debug-panel__funcbp">
          <input
            v-model="debugState.newFuncBpName"
            class="debug-panel__input"
            placeholder="function name e.g. main.main"
            @keydown.enter="onAddFuncBp"
          />
          <input
            v-model="debugState.newFuncBpCondition"
            class="debug-panel__input"
            placeholder="cond (opt)"
            title="condition (optional)"
          />
          <input
            v-model="debugState.newFuncBpHitCondition"
            class="debug-panel__input"
            placeholder="hit (opt)"
            title="hit condition (optional, e.g. >=2)"
          />
          <button
            type="button"
            class="debug-panel__btn"
            :aria-label="t('a11y.addFunctionBreakpoint')"
            @click="onAddFuncBp"
          >+</button>
        </div>
        <ul v-if="debugState.functionBreakpoints.length" class="debug-panel__list">
          <li v-for="b in debugState.functionBreakpoints" :key="renderedItemKey(b)" class="debug-panel__item">
            <span class="debug-panel__bp-dot is-ok" />
            <span class="debug-panel__var">{{ b.name }}</span>
            <span v-if="b.condition" class="debug-panel__cond"> if {{ b.condition }}</span>
            <span v-if="b.hitCondition" class="debug-panel__cond"> hit {{ b.hitCondition }}</span>
            <button type="button" class="debug-panel__x" :aria-label="t('debug.removeFunctionBreakpoint', { name: b.name })" @click="removeFunctionBreakpoint(b.name)">×</button>
          </li>
        </ul>
        <button
          type="button"
          class="debug-panel__btn"
          :disabled="!debugState.running || !debugState.functionBreakpoints.length"
          @click="applyFunctionBreakpoints"
          :title="t('debug.applyFunctionBreakpointsHint')"
        >
          Apply function bps
        </button>

        <!-- F-5: 数据断点列表 -->
        <h4 class="debug-panel__sub">
          Data breakpoints ({{ debugState.dataBreakpoints.length }})
        </h4>
        <ul v-if="debugState.dataBreakpoints.length" class="debug-panel__list">
          <li
            v-for="b in debugState.dataBreakpoints"
            :key="renderedItemKey(b)"
            class="debug-panel__item"
            :title="`F-5: dataId=${b.dataId} accessType=${b.accessType}`"
          >
            <span class="debug-panel__bp-dot is-data" :title="`access: ${b.accessType}`">{{ accessIcon(b.accessType) }}</span>
            <span class="debug-panel__var">{{ b.dataId }}</span>
            <span class="debug-panel__cond"> [{{ b.accessType }}]</span>
            <span v-if="b.condition" class="debug-panel__cond"> if {{ b.condition }}</span>
            <span v-if="b.hitCondition" class="debug-panel__cond"> hit {{ b.hitCondition }}</span>
            <button type="button" class="debug-panel__x" :aria-label="t('debug.removeDataBreakpoint', { id: b.dataId })" @click="onRemoveDataBp(b.dataId, b.accessType)">×</button>
          </li>
        </ul>
        <p v-else class="debug-panel__empty">F-5: Right-click a variable → "Break on Value Change".</p>
        <button
          v-if="debugState.dataBreakpoints.length"
          type="button"
          class="debug-panel__btn"
          @click="onClearDataBps"
          :title="t('debug.f5f7.clearDataBps')"
        >
          Clear data bps
        </button>
      </section>
    </div>

    <div
      v-if="breakpointMenu.visible"
      class="debug-panel__menu"
      role="menu"
      :aria-label="t('debug.breakpointMenu')"
      :style="{ left: breakpointMenu.x + 'px', top: breakpointMenu.y + 'px' }"
    >
      <div class="debug-panel__menu-head">{{ t("debug.conditionalBreakpoint") }}</div>
      <button type="button" class="debug-panel__menu-item" role="menuitem" data-test="edit-breakpoint-condition" @click="editBreakpointConditionFromMenu">
        {{ t("debug.editConditionalBreakpoint") }}
      </button>
      <button
        v-if="breakpointMenu.target?.condition"
        type="button"
        class="debug-panel__menu-item"
        role="menuitem"
        @click="clearBreakpointConditionFromMenu"
      >
        {{ t("debug.clearCondition") }}
      </button>
      <button type="button" class="debug-panel__menu-cancel" @click="closeBreakpointMenu">{{ t("debug.f5f7.cancel") }}</button>
    </div>

    <!-- F-5: 数据断点右键菜单 (浮动) -->
    <div
      v-if="dataBpMenu.visible"
      class="debug-panel__menu"
      role="menu"
      :aria-label="t('debug.f5f7.breakOnChange')"
      :style="{ left: dataBpMenu.x + 'px', top: dataBpMenu.y + 'px' }"
    >
      <div class="debug-panel__menu-head">{{ t("debug.f5f7.breakOnChange") }}</div>
      <template v-for="info in dataBpMenu.infos" :key="renderedItemKey(info)">
        <div class="debug-panel__menu-info">{{ info.description || info.dataId }}</div>
        <button
          v-for="at in (info.accessTypes && info.accessTypes.length ? info.accessTypes : ['write'])"
          :key="at"
          type="button"
          class="debug-panel__menu-item"
          @click="onPickDataBpAccess(info, at)"
        >
          {{ accessLabel(at) }}
        </button>
      </template>
      <button type="button" class="debug-panel__menu-cancel" @click="closeDataBpMenu">{{ t("debug.f5f7.cancel") }}</button>
    </div>

    <!-- F-7: StepIn target 选择菜单 (浮动) -->
    <div
      v-if="stepInMenu.visible"
      class="debug-panel__menu debug-panel__menu--stepin"
      role="menu"
      data-test="step-in-menu"
      :aria-label="t('debug.f5f7.stepInTarget')"
    >
      <div class="debug-panel__menu-head">{{ t("debug.f5f7.stepInTarget") }}</div>
      <button
        v-for="t in stepInMenu.targets"
        :key="t.id"
        type="button"
        class="debug-panel__menu-item"
        role="menuitem"
        :data-step-in-target="t.id"
        @click="onPickStepInTarget(t.id)"
      >
        {{ t.label }}
      </button>
      <button
        type="button"
        class="debug-panel__menu-cancel"
        data-test="step-in-cancel"
        @click="closeStepInMenu"
      >{{ t("debug.f5f7.cancel") }}</button>
    </div>
  </div>
</template>

<style scoped>
.debug-panel {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 140px;
  font-size: 12px;
  color: var(--color-text-secondary);
}
.debug-panel__toolbar {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  padding: 6px 8px;
  border-bottom: 1px solid var(--color-border-default);
}
.debug-panel__btn {
  font-size: 11px;
  padding: 2px 8px;
  border-radius: 4px;
  border: 1px solid var(--color-border-default);
  background: var(--color-bg-elevated);
  color: inherit;
  cursor: pointer;
  transition: background var(--transition-fast);
}
.debug-panel__btn:hover:not(:disabled) {
  background: var(--chrome-hover-bg);
}
.debug-panel__btn:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}
.debug-panel__status {
  padding: 4px 8px;
  opacity: 0.9;
  border-bottom: 1px solid var(--color-border-default);
}
.debug-panel__paused {
  color: var(--color-warning);
}
.debug-panel__error {
  padding: 4px 8px;
  background: var(--color-error-container);
  color: var(--color-error);
  border-bottom: 1px solid var(--color-error);
  font-size: 11px;
}
.debug-panel__attach,
.debug-panel__import {
  display: flex;
  gap: 4px;
  padding: 4px 8px;
  border-bottom: 1px solid var(--color-border-default);
  align-items: flex-start;
}
.debug-panel__ta {
  flex: 1;
  min-width: 0;
  background: var(--color-bg-base);
  border: 1px solid var(--color-border-default);
  color: inherit;
  border-radius: 3px;
  font-size: 10px;
  font-family: var(--font-mono, ui-monospace, monospace);
}
.debug-panel__mode {
  opacity: 0.7;
}
.debug-panel__configs {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 8px;
  border-bottom: 1px solid var(--color-border-default);
}
/* prompt-5: 多会话切换器 */
.debug-panel__sessions {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 4px 8px;
  border-bottom: 1px solid var(--color-border-default);
  background: var(--color-bg-surface-container);
}
.debug-panel__sessions label {
  font-size: 10px;
  opacity: 0.7;
  text-transform: uppercase;
  letter-spacing: 0.04em;
}
.debug-panel__select {
  flex: 1;
  background: var(--color-bg-base);
  color: inherit;
  border: 1px solid var(--color-border-default);
  border-radius: 4px;
  padding: 2px 6px;
}
.debug-panel__cols {
  display: grid;
  grid-template-columns: 1fr 1fr 1fr;
  gap: 0;
  flex: 1;
  min-height: 0;
  overflow: hidden;
}
.debug-panel__section {
  overflow: auto;
  padding: 6px 8px;
  border-right: 1px solid var(--color-border-default);
}

.debug-panel__browser {
  border-top: 1px solid var(--color-border-default);
  border-bottom: 1px solid var(--color-border-default);
}

.debug-panel__browser-bar {
  min-height: 34px;
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 8px;
  background: var(--color-bg-elevated);
}

.debug-panel__target-select {
  min-width: 160px;
  max-width: 360px;
}

.debug-panel__browser-tabs {
  display: flex;
  align-self: stretch;
  margin-left: auto;
}

.debug-panel__browser-tab {
  min-width: 76px;
  border: 0;
  border-bottom: 2px solid transparent;
  background: transparent;
  color: var(--color-text-secondary);
  cursor: pointer;
  transition: color var(--transition-fast);
}

.debug-panel__browser-tab:hover {
  color: var(--color-text-primary);
}

.debug-panel__browser-tab.is-active {
  border-bottom-color: var(--color-primary);
  color: var(--color-text-primary);
}

.debug-panel__browser-log {
  height: 148px;
  overflow: auto;
  padding: 4px 8px;
  font-family: var(--font-mono, ui-monospace, monospace);
  font-size: 11px;
}

.debug-panel__browser-row {
  min-height: 22px;
  display: grid;
  grid-template-columns: minmax(52px, auto) minmax(48px, auto) minmax(0, 1fr) auto;
  align-items: baseline;
  gap: 8px;
  border-bottom: 1px solid var(--color-border-default);
}

.debug-panel__browser-row--console .debug-panel__event-text {
  grid-column: 2 / 4;
}

.debug-panel__event-kind,
.debug-panel__event-method {
  color: var(--color-text-secondary);
  text-transform: uppercase;
}

.debug-panel__event-kind.is-error,
.debug-panel__event-kind.is-warning {
  color: var(--color-error);
}

.debug-panel__event-text {
  min-width: 0;
  overflow-wrap: anywhere;
  white-space: pre-wrap;
}

.debug-panel__event-meta {
  max-width: 300px;
  overflow: hidden;
  color: var(--color-text-secondary);
  text-overflow: ellipsis;
  white-space: nowrap;
}
.debug-panel__section h4,
.debug-panel__sub {
  margin: 0 0 6px;
  font-size: 11px;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  opacity: 0.7;
}
.debug-panel__sub {
  margin-top: 10px;
}
.debug-panel__list {
  list-style: none;
  margin: 0;
  padding: 0;
}
.debug-panel__item {
  padding: 4px 2px;
  cursor: pointer;
  border-radius: 3px;
  position: relative;
  transition: background var(--transition-fast);
}
.debug-panel__item:hover {
  background: var(--chrome-hover-bg);
}
.debug-panel__item--unverified {
  opacity: 0.85;
}
.debug-panel__item--breakpoint {
  display: flex;
  align-items: center;
  gap: 4px;
}
.debug-panel__breakpoint-select {
  flex: 1;
  min-width: 0;
  padding: 0;
  border: 0;
  color: inherit;
  background: transparent;
  text-align: left;
  cursor: pointer;
}
/* prompt-5: 栈帧行 — 让 restart 按钮靠右 */
.debug-panel__item--stack {
  display: flex;
  flex-direction: column;
}
.debug-panel__item--stack .debug-panel__link {
  align-self: flex-end;
  margin-top: 2px;
}
/* prompt-5: 内联值区 */
.debug-panel__inline {
  margin-top: 8px;
  padding-top: 6px;
  border-top: 1px dashed var(--color-border-default);
}
.debug-panel__name {
  font-weight: 500;
  color: var(--color-text-primary);
}
.debug-panel__loc,
.debug-panel__val {
  opacity: 0.75;
  word-break: break-all;
}
.debug-panel__var {
  color: var(--color-primary);
}
.debug-panel__type {
  opacity: 0.6;
}
.debug-panel__empty {
  opacity: 0.5;
  margin: 0;
}
.debug-panel__unverified {
  color: var(--color-warning);
  margin-left: 4px;
  font-size: 10px;
}
.debug-panel__bp-dot {
  display: inline-block;
  width: 8px;
  height: 8px;
  border-radius: 50%;
  margin-right: 4px;
  vertical-align: middle;
}
.debug-panel__bp-dot.is-ok {
  background: var(--color-error);
}
.debug-panel__bp-dot.is-warn {
  background: transparent;
  border: 1.5px solid var(--color-warning);
}
.debug-panel__cond {
  color: var(--color-primary);
  font-size: 10px;
  margin-left: 4px;
}
.debug-panel__row {
  display: flex;
  gap: 4px;
  margin-bottom: 4px;
}
.debug-panel__watch-item {
  display: flex;
  align-items: center;
  gap: 4px;
}
.debug-panel__watch-value {
  flex: 1;
  min-width: 0;
  overflow-wrap: anywhere;
}
.debug-panel__repl-log {
  min-height: 44px;
  max-height: 140px;
  margin-bottom: 4px;
  padding: 4px 6px;
  overflow: auto;
  border: 1px solid var(--color-border-default);
  border-radius: 3px;
  background: var(--color-bg-surface-container);
  font-family: var(--font-mono, ui-monospace, monospace);
  font-size: 11px;
}
.debug-panel__repl-entry + .debug-panel__repl-entry {
  margin-top: 5px;
}
.debug-panel__repl-expression {
  color: var(--color-primary);
}
.debug-panel__repl-result {
  overflow-wrap: anywhere;
  white-space: pre-wrap;
}
.debug-panel__input {
  flex: 1;
  min-width: 0;
  background: var(--color-bg-base);
  border: 1px solid var(--color-border-default);
  color: inherit;
  border-radius: 3px;
  padding: 2px 6px;
  font-size: 11px;
}
/* prompt-5: 变量内联编辑 */
.debug-panel__edit {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  margin-left: 4px;
  width: 100%;
}
.debug-panel__input--edit {
  flex: 1;
}
/* prompt-5: 函数断点输入区 */
.debug-panel__funcbp {
  display: grid;
  grid-template-columns: 2fr 1fr 1fr auto;
  gap: 3px;
  margin-bottom: 4px;
}
.debug-panel__x,
.debug-panel__link {
  background: none;
  border: none;
  color: var(--color-text-tertiary);
  cursor: pointer;
  font-size: 11px;
  margin-left: 4px;
}
.debug-panel__link:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}
.debug-panel__cond-form {
  margin-top: 8px;
  display: flex;
  flex-direction: column;
  gap: 4px;
}

/* F-5: 数据断点 accessType 图标 */
.debug-panel__bp-dot.is-data {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: auto;
  height: auto;
  min-width: 16px;
  padding: 0 2px;
  background: transparent;
  border: none;
  font-size: 11px;
  color: var(--color-primary);
}

/* F-5 + F-7: 浮动右键/选择菜单 */
.debug-panel__menu {
  position: fixed;
  z-index: 10000;
  min-width: 180px;
  background: var(--color-bg-surface);
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-sm);
  box-shadow: var(--shadow-floating);
  padding: 4px 0;
  font-size: 12px;
}
.debug-panel__menu-head {
  padding: 4px 10px;
  font-weight: 600;
  color: var(--color-text-primary);
  border-bottom: 1px solid var(--color-border-default);
  margin-bottom: 2px;
}
.debug-panel__menu-info {
  padding: 2px 10px;
  color: var(--color-text-tertiary);
  font-size: 11px;
}
.debug-panel__menu-item {
  display: block;
  width: 100%;
  padding: 4px 10px;
  background: transparent;
  border: none;
  color: var(--color-text-primary);
  text-align: left;
  cursor: pointer;
  font-size: 12px;
  transition: background var(--transition-fast);
}
.debug-panel__menu-item:hover {
  background: var(--chrome-hover-bg);
  color: var(--color-text-primary);
}
.debug-panel__menu-cancel {
  display: block;
  width: 100%;
  padding: 4px 10px;
  background: transparent;
  border: none;
  border-top: 1px solid var(--color-border-default);
  color: var(--color-text-tertiary);
  text-align: left;
  cursor: pointer;
  font-size: 12px;
  margin-top: 2px;
  transition: background var(--transition-fast);
}
.debug-panel__menu-cancel:hover {
  background: var(--chrome-hover-bg);
  color: var(--color-text-secondary);
}

/* F-7: 异常面板 */
.debug-panel__exception {
  margin: 4px 0;
  padding: 6px 8px;
  background: var(--color-error-container);
  border: 1px solid var(--color-error);
  border-radius: var(--radius-sm);
  color: var(--color-error);
  font-size: 12px;
}
.debug-panel__exception-head {
  display: flex;
  align-items: center;
  gap: 6px;
}
.debug-panel__exception-mode {
  font-size: 10px;
  color: var(--color-error);
  background: var(--color-error-container);
  padding: 1px 4px;
  border-radius: 2px;
}
.debug-panel__exception-desc {
  margin-top: 4px;
  color: var(--color-warning);
}
.debug-panel__exception-details {
  margin-top: 4px;
  font-size: 11px;
  color: var(--color-text-secondary);
}
.debug-panel__exception-details .debug-panel__var {
  color: var(--color-primary);
}
.debug-panel__exception-stack {
  margin-top: 4px;
  padding: 4px;
  background: var(--color-bg-surface-container);
  border-radius: 2px;
  font-size: 10px;
  color: var(--color-text-tertiary);
  white-space: pre-wrap;
  max-height: 120px;
  overflow: auto;
}

/* F-7: 调试控制台补全列表 */
.debug-panel__eval {
  position: relative;
  flex-wrap: wrap;
}
.debug-panel__completions {
  position: absolute;
  bottom: 100%;
  left: 0;
  right: 0;
  margin: 0 0 2px;
  padding: 2px 0;
  list-style: none;
  background: var(--color-bg-surface);
  border: 1px solid var(--color-border-default);
  border-radius: 3px;
  max-height: 160px;
  overflow: auto;
  z-index: 50;
}
.debug-panel__completion-item {
  padding: 3px 8px;
  cursor: pointer;
  font-size: 12px;
  color: var(--color-text-primary);
  transition: background var(--transition-fast);
}
.debug-panel__completion-item:hover {
  background: var(--chrome-hover-bg);
  color: var(--color-text-primary);
}
.debug-panel button:focus-visible,
.debug-panel input:focus-visible,
.debug-panel textarea:focus-visible,
.debug-panel select:focus-visible,
.debug-panel__breakpoint-select:focus-visible,
.debug-panel__completion-item:focus-visible {
  outline: 2px solid var(--color-primary-focus);
  outline-offset: -2px;
}
.debug-panel__completion-item .debug-panel__type {
  color: var(--color-text-tertiary);
  font-size: 10px;
}

</style>
