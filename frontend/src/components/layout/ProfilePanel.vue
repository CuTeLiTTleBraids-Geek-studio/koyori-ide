<script setup lang="ts">
// Koyori IDE 组件 · Profile Panel。
// 喵，这是 Profile Panel，负责 Koyori IDE 的界面呈现喵~
/**
 * Priority 7 (prompt-1.md 422-432): Go pprof 性能分析面板。
 * 提供以下能力：
 *   - Start / Stop CPU Profiling（带可自定义输出路径输入框，留空自动生成）
 *   - Capture Heap Profile
 *   - Capture Goroutine Profile
 *   - 分析结果展示（top functions 表格）
 *   - Profile 类型指示器（CPU / Heap / Goroutine）
 * 与 DebugPanel 并列于底部面板，视觉风格对齐 DebugPanel.vue。
 */
import { getCurrentInstance, onMounted, ref } from "vue";
import FlameGraph from "@/components/layout/FlameGraph.vue";
import { useI18n } from "@/lib/i18n";
import {
  pprofState,
  refreshProfilingStatus,
  startCPUProfile,
  stopCPUProfile,
  captureHeapProfile,
  captureGoroutineProfile,
  startBlockProfile,
  stopBlockProfile,
  startMutexProfile,
  stopMutexProfile,
  startTrace,
  stopTrace,
  analyzeTrace,
  analyzeProfile,
  clearAnalysis,
  formatDuration,
  formatBytes,
  type ProfileKind,
  type TraceProfileView,
} from "@/stores/pprof";

const { t } = useI18n();

// 用户可编辑的输出路径输入框；留空表示使用自动生成的默认路径。
const cpuPathInput = ref("");
const heapPathInput = ref("");
const goroutinePathInput = ref("");
const blockPathInput = ref("");
const mutexPathInput = ref("");
const tracePathInput = ref("");
const traceView = ref<TraceProfileView>("sched");
// 手动分析时输入的 profile 文件路径。
const analyzePathInput = ref("");
const panelUID = getCurrentInstance()?.uid ?? 0;
const controlID = (name: string): string => `profile-${panelUID}-${name}`;

onMounted(() => {
  // 挂载时刷新一次 CPU 采样状态，以应对其它窗口/进程已启动采样的情况。
  void refreshProfilingStatus();
});

async function onStartCPU() {
  const ok = await startCPUProfile(cpuPathInput.value.trim() || undefined);
  if (ok) {
    // 开始采样后清空输入框，避免下次误用同一文件名。
    cpuPathInput.value = "";
  }
}

async function onStopCPU() {
  await stopCPUProfile(true);
}

async function onCaptureHeap() {
  await captureHeapProfile(heapPathInput.value.trim() || undefined);
  heapPathInput.value = "";
}

async function onCaptureGoroutine() {
  await captureGoroutineProfile(goroutinePathInput.value.trim() || undefined);
  goroutinePathInput.value = "";
}

async function onToggleBlock() {
  if (pprofState.activeProfile === "block") await stopBlockProfile(true);
  else await startBlockProfile(blockPathInput.value.trim() || undefined);
}

async function onToggleMutex() {
  if (pprofState.activeProfile === "mutex") await stopMutexProfile(true);
  else await startMutexProfile(mutexPathInput.value.trim() || undefined);
}

async function onToggleTrace() {
  if (pprofState.activeProfile === "trace") await stopTrace();
  else await startTrace(tracePathInput.value.trim() || undefined);
}

async function onAnalyzeTrace() {
  const path = tracePathInput.value.trim() || pprofState.lastProfilePath;
  if (path) await analyzeTrace(path, traceView.value);
}

async function onAnalyze() {
  const p = analyzePathInput.value.trim();
  if (!p) return;
  await analyzeProfile(p);
}

function onClearAnalysis() {
  clearAnalysis();
}

// 把 ProfileKind 转为人类可读标签（用于类型指示器）。
function kindLabel(kind: ProfileKind | ""): string {
  switch (kind) {
    case "cpu":
      return "CPU";
    case "heap":
      return "Heap";
    case "goroutine":
      return "Goroutine";
    case "block":
      return "Block";
    case "mutex":
      return "Mutex";
    case "trace":
      return "Trace";
    default:
      return "—";
  }
}

// 根据当前样本单位选择合适的格式化函数（纳秒→时长，字节→大小）。
function formatValue(unit: string, value: number): string {
  if (unit === "nanoseconds" || unit === "ns") return formatDuration(value);
  if (unit === "bytes") return formatBytes(value);
  // count / 其他整数单位直接展示。
  return String(value);
}
</script>

<template>
  <div class="profile-panel">
    <!-- 工具栏：CPU 采样启停 + Heap / Goroutine 抓取 -->
    <div class="profile-panel__toolbar">
      <button
        type="button"
        class="profile-panel__btn"
        :disabled="pprofState.loading || !!pprofState.activeProfile"
        @click="onStartCPU"
        :title="t('pprof.startCpuHint')"
      >
        ▶ Start CPU
      </button>
      <button
        type="button"
        class="profile-panel__btn"
        :disabled="pprofState.loading || !pprofState.cpuProfiling"
        @click="onStopCPU"
        :title="t('pprof.stopCpuHint')"
      >
        ■ Stop CPU
      </button>
      <button
        type="button"
        class="profile-panel__btn"
        :disabled="pprofState.loading || !!pprofState.activeProfile"
        @click="onCaptureHeap"
        :title="t('pprof.captureHeapHint')"
      >
        ⬇ Heap
      </button>
      <button
        type="button"
        class="profile-panel__btn"
        :disabled="pprofState.loading || !!pprofState.activeProfile"
        @click="onCaptureGoroutine"
        :title="t('pprof.captureGoroutineHint')"
      >
        ⬇ Goroutine
      </button>
      <button data-test="profile-block" type="button" class="profile-panel__btn" :disabled="pprofState.loading || (!!pprofState.activeProfile && pprofState.activeProfile !== 'block')" @click="onToggleBlock">
        {{ pprofState.activeProfile === "block" ? "■ Stop Block" : "▶ Block" }}
      </button>
      <button data-test="profile-mutex" type="button" class="profile-panel__btn" :disabled="pprofState.loading || (!!pprofState.activeProfile && pprofState.activeProfile !== 'mutex')" @click="onToggleMutex">
        {{ pprofState.activeProfile === "mutex" ? "■ Stop Mutex" : "▶ Mutex" }}
      </button>
      <button data-test="profile-trace" type="button" class="profile-panel__btn" :disabled="pprofState.loading || (!!pprofState.activeProfile && pprofState.activeProfile !== 'trace')" @click="onToggleTrace">
        {{ pprofState.activeProfile === "trace" ? "■ Stop Trace" : "▶ Trace" }}
      </button>
    </div>

    <!-- 状态条：当前采样状态 + 最近一次 profile 类型指示器 -->
    <div class="profile-panel__status">
      <span v-if="pprofState.activeProfile" class="profile-panel__recording">
        {{ kindLabel(pprofState.activeProfile) }} profiling
      </span>
      <span v-else class="profile-panel__idle">idle</span>
      <span class="profile-panel__kind">
        · last: <strong>{{ kindLabel(pprofState.lastKind) }}</strong>
      </span>
      <span v-if="pprofState.loading" class="profile-panel__loading">· working…</span>
    </div>

    <div v-if="pprofState.lastError" class="profile-panel__error" role="alert">
      ⚠ {{ pprofState.lastError }}
    </div>

    <!-- 路径输入区：允许用户覆盖默认输出路径 -->
    <div class="profile-panel__paths">
      <div class="profile-panel__row">
        <label :for="controlID('cpu')">CPU</label>
        <input
          :id="controlID('cpu')"
          v-model="cpuPathInput"
          class="profile-panel__input"
          placeholder="(auto) <project>/.pprof/cpu-*.prof"
          :disabled="pprofState.cpuProfiling"
        />
      </div>
      <div class="profile-panel__row"><label :for="controlID('block')">Block</label><input :id="controlID('block')" v-model="blockPathInput" class="profile-panel__input" placeholder="(auto) <project>/.pprof/block-*.prof" /></div>
      <div class="profile-panel__row"><label :for="controlID('mutex')">Mutex</label><input :id="controlID('mutex')" v-model="mutexPathInput" class="profile-panel__input" placeholder="(auto) <project>/.pprof/mutex-*.prof" /></div>
      <div class="profile-panel__row"><label :for="controlID('trace')">Trace</label><input :id="controlID('trace')" v-model="tracePathInput" class="profile-panel__input" placeholder="(auto) <project>/.pprof/trace-*.prof" /></div>
      <div class="profile-panel__row">
        <label :for="controlID('heap')">Heap</label>
        <input
          :id="controlID('heap')"
          v-model="heapPathInput"
          class="profile-panel__input"
          placeholder="(auto) <project>/.pprof/heap-*.prof"
        />
      </div>
      <div class="profile-panel__row">
        <label :for="controlID('goroutine')">Goroutine</label>
        <input
          :id="controlID('goroutine')"
          v-model="goroutinePathInput"
          class="profile-panel__input"
          placeholder="(auto) <project>/.pprof/goroutine-*.prof"
        />
      </div>
    </div>

    <div class="profile-panel__analyze">
      <div class="profile-panel__row">
        <label :for="controlID('trace-view')">Trace view</label>
        <select :id="controlID('trace-view')" v-model="traceView" class="profile-panel__select">
          <option value="sched">Scheduler latency</option>
          <option value="sync">Synchronization blocking</option>
          <option value="net">Network blocking</option>
          <option value="syscall">System calls</option>
        </select>
        <button type="button" class="profile-panel__btn" :disabled="pprofState.loading || !(tracePathInput.trim() || pprofState.lastKind === 'trace')" @click="onAnalyzeTrace">Analyze trace</button>
      </div>
    </div>

    <!-- 手动分析区：分析任意已有的 profile 文件 -->
    <div class="profile-panel__analyze">
      <div class="profile-panel__row">
        <label :for="controlID('analyze')">Analyze</label>
        <input
          :id="controlID('analyze')"
          v-model="analyzePathInput"
          class="profile-panel__input"
          placeholder="path/to/*.prof"
          @keydown.enter="onAnalyze"
        />
        <button
          type="button"
          class="profile-panel__btn"
          :disabled="pprofState.loading || !analyzePathInput.trim()"
          @click="onAnalyze"
        >
          Analyze
        </button>
        <button
          type="button"
          class="profile-panel__btn"
          :disabled="!pprofState.analysis"
          @click="onClearAnalysis"
          :title="t('pprof.clearAnalysisHint')"
        >
          Clear
        </button>
      </div>
    </div>

    <!-- 分析结果：top functions 表格 -->
    <div class="profile-panel__section">
      <h4>Top functions</h4>
      <div v-if="pprofState.analysis" class="profile-panel__meta">
        <span>samples: <strong>{{ pprofState.analysis.totalSamples }}</strong></span>
        <span>total: <strong>{{ formatValue(pprofState.analysis.sampleUnit, pprofState.analysis.totalDuration) }}</strong></span>
        <span>type: <strong>{{ pprofState.analysis.sampleType }} ({{ pprofState.analysis.sampleUnit }})</strong></span>
      </div>
      <FlameGraph v-if="pprofState.analysis?.flameGraph" :root="pprofState.analysis.flameGraph" :unit="pprofState.analysis.sampleUnit" />
      <table v-if="pprofState.analysis && pprofState.analysis.topFunctions.length" class="profile-panel__table">
        <thead>
          <tr>
            <th class="profile-panel__th">Function</th>
            <th class="profile-panel__th profile-panel__th--num">Flat</th>
            <th class="profile-panel__th profile-panel__th--num">Flat %</th>
            <th class="profile-panel__th profile-panel__th--num">Cum</th>
            <th class="profile-panel__th profile-panel__th--num">Cum %</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="fn in pprofState.analysis.topFunctions" :key="fn.name" class="profile-panel__tr">
            <td class="profile-panel__td profile-panel__td--name" :title="fn.name">{{ fn.name }}</td>
            <td class="profile-panel__td profile-panel__td--num">{{ formatValue(pprofState.analysis!.sampleUnit, fn.flatTime) }}</td>
            <td class="profile-panel__td profile-panel__td--num">{{ fn.flatPercent.toFixed(1) }}%</td>
            <td class="profile-panel__td profile-panel__td--num">{{ formatValue(pprofState.analysis!.sampleUnit, fn.cumulativeTime) }}</td>
            <td class="profile-panel__td profile-panel__td--num">{{ fn.cumulativePercent.toFixed(1) }}%</td>
          </tr>
        </tbody>
      </table>
      <p v-else class="profile-panel__empty">
        No analysis yet — start CPU profiling or capture a heap/goroutine profile.
      </p>
    </div>
  </div>
</template>

<style scoped>
/* 视觉风格与 DebugPanel.vue 对齐（同属底部面板内的并列子面板）。 */
.profile-panel {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 140px;
  font-size: 12px;
  color: var(--color-text-secondary, #ccc);
  overflow: auto;
}

.profile-panel__toolbar {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  padding: 6px 8px;
  border-bottom: 1px solid var(--color-border, #333);
}

.profile-panel__btn {
  font-size: 11px;
  padding: 2px 8px;
  border-radius: 4px;
  border: 1px solid var(--color-border, #444);
  background: var(--color-bg-elevated, #f5f5f7);
  color: inherit;
  cursor: pointer;
}

.profile-panel__btn:hover:not(:disabled) {
  background: var(--color-bg-surface-container-low, rgba(255, 255, 255, 0.06));
}

.profile-panel__btn:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}

.profile-panel__btn:focus-visible {
  outline: 2px solid var(--color-primary-focus, var(--color-primary, #4f8cf7));
  outline-offset: -2px;
}

.profile-panel__status {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 8px;
  opacity: 0.9;
  border-bottom: 1px solid var(--color-border, #333);
}

/* CPU 采样进行中的指示器：红色圆点 + 脉冲动画。 */
.profile-panel__recording {
  color: var(--color-error, #f85149);
  font-weight: 600;
}

.profile-panel__recording::before {
  content: "●";
  margin-right: 4px;
  animation: profile-panel-pulse 1.2s ease-in-out infinite;
}

.profile-panel__idle {
  opacity: 0.7;
}

.profile-panel__kind {
  opacity: 0.8;
}

.profile-panel__loading {
  opacity: 0.7;
}

.profile-panel__error {
  padding: 4px 8px;
  background: rgba(248, 81, 73, 0.15);
  color: #ff7b72;
  border-bottom: 1px solid #f85149;
  font-size: 11px;
}

.profile-panel__paths,
.profile-panel__analyze {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 6px 8px;
  border-bottom: 1px solid var(--color-border, #333);
}

.profile-panel__row {
  display: flex;
  align-items: center;
  gap: 6px;
}

.profile-panel__row label {
  flex-shrink: 0;
  width: 70px;
  font-size: 10px;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  opacity: 0.7;
}

.profile-panel__input {
  flex: 1;
  min-width: 0;
  background: #1e1e1e;
  border: 1px solid #444;
  color: inherit;
  border-radius: 3px;
  padding: 2px 6px;
  font-size: 11px;
  font-family: var(--font-mono, ui-monospace, monospace);
}

.profile-panel__input:focus {
  outline: 1px solid var(--color-primary, #4f8cf7);
  border-color: var(--color-primary, #4f8cf7);
}

.profile-panel__input:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.profile-panel__select {
  min-width: 180px;
  padding: 3px 6px;
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-sm);
  color: var(--color-text-primary);
  background: var(--color-bg-surface);
  font-size: 11px;
}

.profile-panel__section {
  padding: 6px 8px;
  overflow: auto;
}

.profile-panel__section h4 {
  margin: 0 0 6px;
  font-size: 11px;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  opacity: 0.7;
}

.profile-panel__meta {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
  margin-bottom: 6px;
  font-size: 11px;
  opacity: 0.85;
}

.profile-panel__table {
  width: 100%;
  border-collapse: collapse;
  font-size: 11px;
}

.profile-panel__th {
  text-align: left;
  padding: 3px 6px;
  border-bottom: 1px solid var(--color-border, #333);
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.03em;
  opacity: 0.7;
  font-size: 10px;
}

.profile-panel__th--num {
  text-align: right;
}

.profile-panel__tr:hover {
  background: rgba(255, 255, 255, 0.04);
}

.profile-panel__td {
  padding: 3px 6px;
  border-bottom: 1px solid rgba(255, 255, 255, 0.04);
  vertical-align: top;
}

.profile-panel__td--name {
  font-family: var(--font-mono, ui-monospace, monospace);
  max-width: 320px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.profile-panel__td--num {
  text-align: right;
  font-family: var(--font-mono, ui-monospace, monospace);
  white-space: nowrap;
}

.profile-panel__empty {
  padding: 8px;
  opacity: 0.6;
  font-size: 11px;
}

@keyframes profile-panel-pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.4; }
}

@media (prefers-reduced-motion: reduce) {
  .profile-panel__recording::before {
    animation: none;
  }
}
</style>
