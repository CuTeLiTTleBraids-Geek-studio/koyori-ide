<script setup lang="ts">
// Koyori IDE 组件 · Goal Section。
// 喵，这是 Goal Section，负责 Koyori IDE 的界面呈现喵~
/**
 * Plan 11 Task 10 Step 7 - Goal 模式设置分区。
 * 展示活动 Goal（目标、标准、限制、进度、Checkpoint 时间线）
 * + 成本仪表盘 + 运行控制（运行、暂停、恢复、中止）。
 * 安全（Step 8-10）：
 *   - G-SEC-02：每轮工具调用经 CheckCommand
 *   - G-SEC-03：创建需显式确认
 *   - Step 8：禁止删除工作区外文件、git push --force 和其他 RiskDangerous 操作
 */
import { onMounted, ref, computed } from "vue";
import { ElMessageBox } from "element-plus";
import { useI18n } from "@/lib/i18n";
import {
  aiGoalState,
  activeGoal,
  costReport,
  createGoal,
  runGoal,
  pauseGoal,
  resumeGoal,
  abortGoal,
  refreshActiveGoal,
  refreshCostReport,
  createCheckpoint,
  rollbackToCheckpoint,
  refreshExecutorCapability,
  setPrototypeExecutionEnabled,
  isExecutorPrototype,
  executorLimitation,
  prototypeAutoRunEnabled,
} from "@/stores/aiGoal";
import type { GoalStatus } from "@/stores/aiGoal";

const { t } = useI18n();

onMounted(async () => {
  await refreshActiveGoal();
  // GOAL-P0-04A: ask the backend what the installed executor can actually do.
  // The UI must not assume Goal mode works; it renders the backend's own
  // limitation text so the two can never drift apart.
  await refreshExecutorCapability();
  if (activeGoal.value) {
    await refreshCostReport(activeGoal.value.id);
  }
});

/**
 * GOAL-P0-04A: opt in to running the prototype loop.
 *
 * Kept explicit and off by default: the default executor plans with a fixed
 * string, runs a fixed command regardless of that plan, and never evaluates
 * success, so auto-running it would show a convincing-looking iteration
 * sequence that cannot reach the user's goal.
 */
async function handleTogglePrototypeAutoRun(enabled: boolean): Promise<void> {
  if (enabled) {
    try {
      await ElMessageBox.confirm(
        t("goalSection.prototypeOptInWarning"),
        t("goalSection.prototypeOptInTitle"),
        { type: "warning" },
      );
    } catch {
      return; // user cancelled
    }
  }
  await setPrototypeExecutionEnabled(enabled);
}

// ---------------------------------------------------------------------------
// 创建 Goal 表单（Step 10: G-SEC-03 显式确认
// ---------------------------------------------------------------------------

const showCreateForm = ref(false);
const confirmed = ref(false);
const newGoalId = ref("");
const newGoalDesc = ref("");
const newGoalCriteria = ref("");
const newMaxIterations = ref(10);
const newMaxCost = ref(1.0);
const newMaxDuration = ref(30); // 分钟

async function handleCreateGoal(): Promise<void> {
  if (!confirmed.value) {
    alert(t("goalSection.confirmRequired"));
    return;
  }
  if (!newGoalId.value.trim() || !newGoalDesc.value.trim() || !newGoalCriteria.value.trim()) return;
  const durationNs = newMaxDuration.value * 60 * 1e9; // minutes -> nanoseconds
  const ok = await createGoal(
    newGoalId.value.trim(),
    newGoalDesc.value.trim(),
    newGoalCriteria.value.trim(),
    newMaxIterations.value,
    newMaxCost.value,
    durationNs,
  );
  if (ok) {
    showCreateForm.value = false;
    confirmed.value = false;
    newGoalId.value = "";
    newGoalDesc.value = "";
    newGoalCriteria.value = "";
    newMaxIterations.value = 10;
    newMaxCost.value = 1.0;
    newMaxDuration.value = 30;
  } else if (aiGoalState.error) {
    alert(aiGoalState.error);
  }
}

// ---------------------------------------------------------------------------
// Run controls
// ---------------------------------------------------------------------------

async function handleRun(): Promise<void> {
  if (!activeGoal.value) return;
  if (await runGoal(activeGoal.value.id)) {
    await refreshCostReport(activeGoal.value.id);
  } else if (aiGoalState.error) {
    alert(aiGoalState.error);
  }
}

async function handlePause(): Promise<void> {
  if (!activeGoal.value) return;
  await pauseGoal(activeGoal.value.id);
}

async function handleResume(): Promise<void> {
  if (!activeGoal.value) return;
  if (await resumeGoal(activeGoal.value.id)) {
    await refreshCostReport(activeGoal.value.id);
  }
}

async function handleAbort(): Promise<void> {
  if (!activeGoal.value) return;
  try {
    await ElMessageBox.confirm(
      t("goalSection.abortConfirm"),
      t("common.confirm"),
      { type: "warning", confirmButtonText: t("common.confirm"), cancelButtonText: t("common.cancel") },
    );
  } catch {
    return;
  }
  await abortGoal(activeGoal.value.id);
}

// ---------------------------------------------------------------------------
// Checkpoints
// ---------------------------------------------------------------------------

const checkpointNote = ref("");
const checkpointKeys = new WeakMap<object, string>();
let checkpointKeySequence = 0;

function checkpointKey(checkpoint: object): string {
  const existing = checkpointKeys.get(checkpoint);
  if (existing) return existing;
  const key = `goal-checkpoint-${++checkpointKeySequence}`;
  checkpointKeys.set(checkpoint, key);
  return key;
}

async function handleCreateCheckpoint(): Promise<void> {
  if (!activeGoal.value) return;
  const ok = await createCheckpoint(activeGoal.value.id, "", checkpointNote.value);
  if (ok) {
    checkpointNote.value = "";
  }
}

async function handleRollback(idx: number): Promise<void> {
  if (!activeGoal.value) return;
  try {
    await ElMessageBox.confirm(
      t("goalSection.rollbackConfirm", { idx: idx + 1 }),
      t("common.confirm"),
      { type: "warning", confirmButtonText: t("common.confirm"), cancelButtonText: t("common.cancel") },
    );
  } catch {
    return;
  }
  if (await rollbackToCheckpoint(activeGoal.value.id, idx)) {
    await refreshCostReport(activeGoal.value.id);
  }
}

// ---------------------------------------------------------------------------
// 状态映
// ---------------------------------------------------------------------------

const GOAL_STATUS_KEY: Record<GoalStatus, string> = {
  created: "goalSection.statusCreated",
  running: "goalSection.statusRunning",
  paused: "goalSection.statusPaused",
  completed: "goalSection.statusCompleted",
  aborted: "goalSection.statusAborted",
  failed: "goalSection.statusFailed",
};

function statusClass(status: GoalStatus): string {
  return `goal-status--${status}`;
}

const progressPercent = computed(() => {
  if (!activeGoal.value || activeGoal.value.maxIterations <= 0) return 0;
  return Math.min(100, Math.round((activeGoal.value.iteration / activeGoal.value.maxIterations) * 100));
});

const costPercent = computed(() => {
  if (!costReport.value || costReport.value.maxCost <= 0) return 0;
  return Math.min(100, Math.round((costReport.value.totalCost / costReport.value.maxCost) * 100));
});

function isGoalActive(status: GoalStatus): boolean {
  return status === "created" || status === "running" || status === "paused";
}

function formatDuration(ns: number): string {
  const minutes = Math.round(ns / 1e9 / 60);
  if (minutes >= 60) {
    const h = Math.floor(minutes / 60);
    const m = minutes % 60;
    return `${h}h ${m}m`;
  }
  return `${minutes}m`;
}
</script>

<template>
  <section class="settings-section">
    <h2 class="section-title">{{ t("settings.goal") }}</h2>
    <p class="section-hint">{{ t("goalSection.hint") }}</p>

    <!--
      GOAL-P0-04A: prototype boundary. Rendered before any Goal control so the
      user reads the capability limit before deciding to create or run a Goal.
      `executorLimitation` is the backend's own text, not UI copy, so the two
      cannot drift apart and overstate what the executor does.
    -->
    <div v-if="isExecutorPrototype" class="goal-prototype-notice">
      <div class="goal-prototype-head">
        <span class="goal-prototype-badge">{{ t("goalSection.prototypeBadge") }}</span>
        <span class="goal-prototype-title">{{ t("goalSection.prototypeTitle") }}</span>
      </div>
      <p class="goal-prototype-body">{{ t("goalSection.prototypeExplain") }}</p>
      <p v-if="executorLimitation" class="goal-prototype-limitation">{{ executorLimitation }}</p>
      <label class="goal-prototype-optin">
        <input
          type="checkbox"
          :checked="prototypeAutoRunEnabled"
          @change="handleTogglePrototypeAutoRun(($event.target as HTMLInputElement).checked)"
        />
        <span>{{ t("goalSection.prototypeOptIn") }}</span>
      </label>
    </div>

    <div v-if="aiGoalState.error" class="goal-error">{{ aiGoalState.error }}</div>

    <!-- 无活动 Goal -->
    <div v-if="!activeGoal" class="goal-empty">
      <p>{{ t("goalSection.noActiveGoal") }}</p>
      <el-button type="primary" size="small" @click="showCreateForm = !showCreateForm">
        {{ t("goalSection.createGoal") }}
      </el-button>
    </div>

    <!-- 创建 Goal 表单（Step 10: G-SEC-03 显式确认）-->
    <div v-if="showCreateForm" class="goal-create-form">
      <h3 class="goal-form-title">{{ t("goalSection.createTitle") }}</h3>
      <div class="goal-form-field">
        <label class="goal-label">{{ t("goalSection.fieldGoalId") }}</label>
        <input type="text" v-model="newGoalId" class="goal-input" :placeholder="t('goalSection.fieldGoalIdPlaceholder')" />
      </div>
      <div class="goal-form-field">
        <label class="goal-label">{{ t("goalSection.fieldDescription") }}</label>
        <textarea v-model="newGoalDesc" class="goal-input goal-input--textarea" :placeholder="t('goalSection.fieldDescriptionPlaceholder')" rows="2"></textarea>
      </div>
      <div class="goal-form-field">
        <label class="goal-label">{{ t("goalSection.fieldSuccessCriteria") }}</label>
        <textarea v-model="newGoalCriteria" class="goal-input goal-input--textarea" :placeholder="t('goalSection.fieldCriteriaPlaceholder')" rows="2"></textarea>
      </div>
      <div class="goal-form-row">
        <div class="goal-form-field">
          <label class="goal-label">{{ t("goalSection.fieldMaxIterations") }}</label>
          <input type="number" v-model.number="newMaxIterations" class="goal-input" min="1" />
        </div>
        <div class="goal-form-field">
          <label class="goal-label">{{ t("goalSection.fieldMaxCost") }}</label>
          <input type="number" v-model.number="newMaxCost" class="goal-input" min="0.01" step="0.01" />
        </div>
        <div class="goal-form-field">
          <label class="goal-label">{{ t("goalSection.fieldMaxDuration") }}</label>
          <input type="number" v-model.number="newMaxDuration" class="goal-input" min="1" />
        </div>
      </div>
      <!-- G-SEC-03 显式确认 -->
      <label class="goal-confirm">
        <input type="checkbox" v-model="confirmed" />
        <span>{{ t("goalSection.confirmLabel") }}</span>
      </label>
      <div class="goal-form-actions">
        <el-button size="small" @click="showCreateForm = false">{{ t("common.cancel") }}</el-button>
        <el-button size="small" type="primary" :disabled="!confirmed" @click="handleCreateGoal">{{ t("common.create") }}</el-button>
      </div>
    </div>

    <!-- 活动 Goal 详情 -->
    <div v-if="activeGoal" class="goal-detail">
      <div class="goal-detail-header">
        <div class="goal-detail-meta">
          <span class="goal-id">{{ activeGoal.id }}</span>
          <span class="goal-status-badge" :class="statusClass(activeGoal.status)">
            {{ t(GOAL_STATUS_KEY[activeGoal.status]) }}
          </span>
        </div>
        <div class="goal-detail-desc">{{ activeGoal.description }}</div>
        <div class="goal-detail-criteria">
          <span class="goal-criteria-label">{{ t("goalSection.fieldSuccessCriteria") }}:</span>
          <span class="goal-criteria-value">{{ activeGoal.successCriteria }}</span>
        </div>
        <div class="goal-detail-actions" v-if="isGoalActive(activeGoal.status)">
          <el-button v-if="activeGoal.status === 'created'" size="small" type="primary" @click="handleRun">
            {{ t("goalSection.run") }}
          </el-button>
          <el-button v-if="activeGoal.status === 'running'" size="small" type="warning" @click="handlePause">
            {{ t("goalSection.pause") }}
          </el-button>
          <el-button v-if="activeGoal.status === 'paused'" size="small" type="success" @click="handleResume">
            {{ t("goalSection.resume") }}
          </el-button>
          <el-button size="small" type="danger" @click="handleAbort">
            {{ t("goalSection.abort") }}
          </el-button>
        </div>
      </div>

      <!-- 进度（Step 7: 进度面板）-->
      <div class="goal-progress">
        <div class="goal-progress-header">
          <span class="goal-progress-label">{{ t("goalSection.progress") }}</span>
          <span class="goal-progress-value">{{ activeGoal.iteration }} / {{ activeGoal.maxIterations }}</span>
        </div>
        <div class="goal-progress-bar">
          <div class="goal-progress-fill" :style="{ width: progressPercent + '%' }"></div>
        </div>
      </div>

      <!-- 成本仪表盘（Step 6/7: 成本仪表盘） -->
      <div v-if="costReport" class="goal-cost-dashboard">
        <h4 class="goal-dashboard-title">{{ t("goalSection.costDashboard") }}</h4>
        <div class="goal-cost-grid">
          <div class="goal-cost-item">
            <span class="goal-cost-label">{{ t("goalSection.totalCost") }}</span>
            <span class="goal-cost-value">${{ (costReport.totalCost ?? 0).toFixed(4) }}</span>
          </div>
          <div class="goal-cost-item">
            <span class="goal-cost-label">{{ t("goalSection.maxCost") }}</span>
            <span class="goal-cost-value">${{ (costReport.maxCost ?? 0).toFixed(2) }}</span>
          </div>
          <div class="goal-cost-item">
            <span class="goal-cost-label">{{ t("goalSection.remaining") }}</span>
            <span class="goal-cost-value goal-cost-value--remaining">${{ (costReport.remainingCost ?? 0).toFixed(4) }}</span>
          </div>
          <div class="goal-cost-item">
            <span class="goal-cost-label">{{ t("goalSection.totalTokens") }}</span>
            <span class="goal-cost-value">{{ costReport.totalTokens }}</span>
          </div>
        </div>
        <div class="goal-cost-bar">
          <div class="goal-cost-fill" :class="{ 'goal-cost-fill--warning': costPercent > 80 }" :style="{ width: costPercent + '%' }"></div>
        </div>
      </div>

      <!-- 限制信息 -->
      <div class="goal-limits">
        <div class="goal-limit-item">
          <span class="goal-limit-label">{{ t("goalSection.fieldMaxIterations") }}:</span>
          <span class="goal-limit-value">{{ activeGoal.maxIterations }}</span>
        </div>
        <div class="goal-limit-item">
          <span class="goal-limit-label">{{ t("goalSection.fieldMaxCost") }}:</span>
          <span class="goal-limit-value">${{ (activeGoal.maxCost ?? 0).toFixed(2) }}</span>
        </div>
        <div class="goal-limit-item">
          <span class="goal-limit-label">{{ t("goalSection.fieldMaxDuration") }}:</span>
          <span class="goal-limit-value">{{ formatDuration(activeGoal.maxDuration) }}</span>
        </div>
      </div>

      <!-- 错误信息 -->
      <div v-if="activeGoal.lastError" class="goal-last-error">
        <span class="goal-error-label">{{ t("goalSection.lastError") }}:</span>
        <pre class="goal-error-content">{{ activeGoal.lastError }}</pre>
      </div>

      <!-- Checkpoint 时间线（Step 5/7: Checkpoint 时间线） -->
      <div class="goal-checkpoints">
        <h4 class="goal-dashboard-title">{{ t("goalSection.checkpoints") }}</h4>

        <!-- 创建检查点 -->
        <div v-if="isGoalActive(activeGoal.status)" class="goal-checkpoint-create">
          <input type="text" v-model="checkpointNote" class="goal-input goal-input--inline" :placeholder="t('goalSection.checkpointNote')" />
          <el-button size="small" @click="handleCreateCheckpoint">{{ t("goalSection.createCheckpoint") }}</el-button>
        </div>

        <div v-if="activeGoal.checkpoints.length === 0" class="goal-checkpoints-empty">
          {{ t("goalSection.noCheckpoints") }}
        </div>

        <ol v-else class="goal-checkpoint-timeline">
          <li v-for="(cp, idx) in activeGoal.checkpoints" :key="checkpointKey(cp)" class="goal-checkpoint-item">
            <div class="goal-checkpoint-dot" aria-hidden="true"></div>
            <div class="goal-checkpoint-content">
              <div class="goal-checkpoint-header">
                <span class="goal-checkpoint-iter">#{{ idx + 1 }} (iter {{ cp.iteration }})</span>
                <span class="goal-checkpoint-cost">${{ (cp.cost ?? 0).toFixed(4) }}</span>
              </div>
              <p v-if="cp.note" class="goal-checkpoint-note">{{ cp.note }}</p>
              <el-button v-if="isGoalActive(activeGoal.status)" size="small" type="warning" @click="handleRollback(idx)">
                {{ t("goalSection.rollback") }}
              </el-button>
            </div>
          </li>
        </ol>
      </div>
    </div>
  </section>
</template>

<style scoped>
.section-hint {
  font-size: 13px;
  color: var(--color-text-secondary);
  margin-bottom: 12px;
  line-height: 1.5;
}

.goal-error {
  color: var(--color-error, #f44336);
  font-size: 12px;
  margin-bottom: 16px;
}

.goal-empty {
  padding: 32px 0;
  text-align: center;
  color: var(--color-text-tertiary);
  font-size: 13px;
  display: flex;
  flex-direction: column;
  gap: 12px;
  align-items: center;
}

.goal-create-form {
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-sm);
  padding: 16px;
  margin-bottom: 16px;
  background: var(--color-bg-surface-container-low);
}

.goal-form-title {
  font-size: 14px;
  font-weight: 600;
  margin-bottom: 12px;
  color: var(--color-text-primary);
}

.goal-form-field {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin-bottom: 12px;
}

.goal-form-row {
  display: flex;
  gap: 12px;
}

.goal-form-row .goal-form-field {
  flex: 1;
}

.goal-label {
  font-size: 12px;
  color: var(--color-text-secondary);
  font-weight: 500;
}

.goal-input {
  font-size: 13px;
  padding: 6px 8px;
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-xs);
  background: var(--color-bg-surface);
  color: var(--color-text-primary);
  width: 100%;
  box-sizing: border-box;
}

.goal-input--textarea {
  font-family: var(--font-mono);
  resize: vertical;
  min-height: 50px;
}

.goal-input--inline {
  width: auto;
  flex: 1;
}

.goal-confirm {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 12px 0;
  font-size: 12px;
  color: var(--color-text-secondary);
  cursor: pointer;
}

.goal-form-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 8px;
}

.goal-detail {
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-sm);
  padding: 16px;
}

.goal-detail-header {
  margin-bottom: 16px;
}

.goal-detail-meta {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 6px;
}

.goal-id {
  font-family: var(--font-mono);
  font-size: 12px;
  color: var(--color-text-secondary);
}

.goal-status-badge {
  font-size: 10px;
  padding: 2px 6px;
  border-radius: var(--radius-xs);
  text-transform: uppercase;
  font-weight: 500;
}

.goal-status--created { color: var(--color-text-tertiary); background: var(--color-bg-surface-container); }
.goal-status--running { color: #2196f3; background: rgba(33, 150, 243, 0.1); }
.goal-status--paused { color: #ff9800; background: rgba(255, 152, 0, 0.1); }
.goal-status--completed { color: #4caf50; background: rgba(76, 175, 80, 0.1); }
.goal-status--aborted { color: var(--color-error, #f44336); background: rgba(244, 67, 54, 0.1); }
.goal-status--failed { color: #ff5722; background: rgba(255, 87, 34, 0.1); }

.goal-detail-desc {
  font-size: 14px;
  font-weight: 500;
  color: var(--color-text-primary);
  margin-bottom: 6px;
}

.goal-detail-criteria {
  font-size: 12px;
  color: var(--color-text-secondary);
  margin-bottom: 8px;
}

.goal-criteria-label {
  font-weight: 500;
  margin-right: 4px;
}

.goal-detail-actions {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}

.goal-progress {
  margin-bottom: 16px;
}

.goal-progress-header {
  display: flex;
  justify-content: space-between;
  margin-bottom: 4px;
}

.goal-progress-label,
.goal-progress-value {
  font-size: 12px;
  color: var(--color-text-secondary);
}

.goal-progress-bar {
  height: 6px;
  background: var(--color-bg-surface-container);
  border-radius: var(--radius-full);
  overflow: hidden;
}

.goal-progress-fill {
  height: 100%;
  background: var(--color-primary, #2196f3);
  border-radius: var(--radius-full);
  transition: width 0.3s ease;
}

.goal-cost-dashboard {
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-xs);
  padding: 12px;
  margin-bottom: 16px;
}

.goal-dashboard-title {
  font-size: 12px;
  font-weight: 600;
  color: var(--color-text-secondary);
  text-transform: uppercase;
  margin-bottom: 8px;
}

.goal-cost-grid {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 8px;
  margin-bottom: 8px;
}

.goal-cost-item {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.goal-cost-label {
  font-size: 10px;
  color: var(--color-text-tertiary);
}

.goal-cost-value {
  font-size: 13px;
  font-weight: 600;
  font-family: var(--font-mono);
  color: var(--color-text-primary);
}

.goal-cost-value--remaining {
  color: var(--color-success, #4caf50);
}

.goal-cost-bar {
  height: 4px;
  background: var(--color-bg-surface-container);
  border-radius: var(--radius-full);
  overflow: hidden;
}

.goal-cost-fill {
  height: 100%;
  background: var(--color-success, #4caf50);
  border-radius: var(--radius-full);
  transition: width 0.3s ease;
}

.goal-cost-fill--warning {
  background: var(--color-error, #f44336);
}

.goal-limits {
  display: flex;
  gap: 16px;
  flex-wrap: wrap;
  margin-bottom: 16px;
  padding: 8px 0;
  border-top: 1px solid var(--color-border-default);
  border-bottom: 1px solid var(--color-border-default);
}

.goal-limit-item {
  font-size: 12px;
}

.goal-limit-label {
  color: var(--color-text-tertiary);
  margin-right: 4px;
}

.goal-limit-value {
  color: var(--color-text-primary);
  font-weight: 500;
}

.goal-last-error {
  margin-bottom: 16px;
}

.goal-error-label {
  font-size: 10px;
  color: var(--color-error, #f44336);
  text-transform: uppercase;
  font-weight: 500;
}

.goal-error-content {
  font-family: var(--font-mono);
  font-size: 11px;
  background: rgba(244, 67, 54, 0.08);
  color: var(--color-error, #f44336);
  padding: 6px 8px;
  border-radius: var(--radius-xs);
  margin: 2px 0 0;
  white-space: pre-wrap;
  word-break: break-word;
}

.goal-checkpoints {
  margin-top: 16px;
}

.goal-checkpoint-create {
  display: flex;
  gap: 8px;
  margin-bottom: 12px;
  align-items: center;
}

.goal-checkpoints-empty {
  font-size: 12px;
  color: var(--color-text-tertiary);
  padding: 12px 0;
  text-align: center;
}

.goal-checkpoint-timeline {
  list-style: none;
  padding: 0;
  margin: 0;
  position: relative;
}

.goal-checkpoint-timeline::before {
  content: "";
  position: absolute;
  left: 5px;
  top: 0;
  bottom: 0;
  width: 2px;
  background: var(--color-border-default);
}

.goal-checkpoint-item {
  position: relative;
  padding-left: 24px;
  margin-bottom: 12px;
}

.goal-checkpoint-dot {
  position: absolute;
  left: 2px;
  top: 4px;
  width: 8px;
  height: 8px;
  border-radius: var(--radius-full);
  background: var(--color-primary, #2196f3);
  border: 2px solid var(--color-bg-surface);
}

.goal-checkpoint-content {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.goal-checkpoint-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
}

.goal-checkpoint-iter {
  font-size: 12px;
  font-weight: 500;
  color: var(--color-text-primary);
}

.goal-checkpoint-cost {
  font-family: var(--font-mono);
  font-size: 11px;
  color: var(--color-text-tertiary);
}

.goal-checkpoint-note {
  font-size: 12px;
  color: var(--color-text-secondary);
  margin: 0;
}
</style>
