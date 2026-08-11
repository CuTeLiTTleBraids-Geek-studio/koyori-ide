<script setup lang="ts">
// Koyori IDE 组件 · Plan Panel。
// 喵，这是 Plan Panel，负责 Koyori IDE 的界面呈现喵~
// BUG4: AI 界面 plan 模式面板。
// 在 AiAssistantView 右侧替换 ContextChips 显示，让用户能在 AI 页面
// 直接创建/审批/执行 Plan。复用 aiPlan store 与 planSection.* 文案。
import { onMounted, ref, computed } from "vue";
import { ElMessageBox } from "element-plus";
import { useI18n } from "@/lib/i18n";
import FocusTrapDialog from "@/components/common/FocusTrapDialog.vue";
import {
  aiPlanState,
  activePlan,
  createPlan,
  approveStep,
  approveAll,
  rejectAll,
  executeStep,
  skipStep,
  replan,
  abortPlan,
  getStepResult,
  refreshActivePlan,
} from "@/stores/aiPlan";
import type { PlanStep, PlanStepStatus, PlanStatus } from "@/stores/aiPlan";

const { t } = useI18n();
const planStepKeys = new WeakMap<PlanStep, string>();
let planStepKeySequence = 0;

function planStepKey(step: PlanStep): string {
  const existing = planStepKeys.get(step);
  if (existing) return existing;
  const key = `plan-step-${++planStepKeySequence}`;
  planStepKeys.set(step, key);
  return key;
}

onMounted(async () => {
  await refreshActivePlan();
});

// 创建表单
const newPlanGoal = ref("");
const creating = ref(false);

async function handleCreateFromGoal(): Promise<void> {
  const goal = newPlanGoal.value.trim();
  if (!goal) return;
  creating.value = true;
  // 用时间戳生成唯一 id，避免与已有 Plan 冲突。
  const id = `plan-${Date.now()}`;
  // Do not fabricate an executable step. The plan generator is not wired to
  // this UI yet, so an empty plan must be completed through explicit replan.
  const ok = await createPlan(id, goal, []);
  creating.value = false;
  if (ok) {
    newPlanGoal.value = "";
  } else if (aiPlanState.error) {
    ElMessageBox.alert(aiPlanState.error, t("common.error"), { type: "error" });
  }
}

// 审批与执行
async function handleApproveStep(idx: number): Promise<void> {
  if (!activePlan.value) return;
  await approveStep(activePlan.value.id, idx);
}

async function handleApproveAll(): Promise<void> {
  if (!activePlan.value) return;
  await approveAll(activePlan.value.id);
}

async function handleRejectAll(): Promise<void> {
  if (!activePlan.value) return;
  await rejectAll(activePlan.value.id);
}

async function handleExecuteStep(idx: number): Promise<void> {
  if (!activePlan.value) return;
  await executeStep(activePlan.value.id, idx);
}

async function handleSkipStep(idx: number): Promise<void> {
  if (!activePlan.value) return;
  await skipStep(activePlan.value.id, idx);
}

async function handleAbort(): Promise<void> {
  if (!activePlan.value) return;
  try {
    await ElMessageBox.confirm(
      t("planSection.abortConfirm"),
      t("common.confirm"),
      {
        type: "warning",
        confirmButtonText: t("common.confirm"),
        cancelButtonText: t("common.cancel"),
      },
    );
  } catch {
    return;
  }
  await abortPlan(activePlan.value.id);
}

async function handleReplan(): Promise<void> {
  if (!activePlan.value) return;
  const input = prompt(t("planSection.replanPrompt"));
  if (!input) return;
  const steps: PlanStep[] = input
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line)
    .map((line) => {
      const [title, tool, args] = line.split("|").map((s) => s.trim());
      return {
        title,
        description: "",
        status: "pending" as PlanStepStatus,
        tool: tool || undefined,
        args: args || undefined,
      };
    });
  await replan(activePlan.value.id, steps);
}

// 回放
const replayStep = ref<PlanStep | null>(null);
const replayLoading = ref(false);

async function handleReplay(idx: number): Promise<void> {
  if (!activePlan.value) return;
  replayLoading.value = true;
  replayStep.value = await getStepResult(activePlan.value.id, idx);
  replayLoading.value = false;
}

// 状态映射
const STEP_STATUS_KEY: Record<PlanStepStatus, string> = {
  pending: "planSection.statusPending",
  approved: "planSection.statusApproved",
  executing: "planSection.statusExecuting",
  completed: "planSection.statusCompleted",
  failed: "planSection.statusFailed",
  skipped: "planSection.statusSkipped",
};

const PLAN_STATUS_KEY: Record<PlanStatus, string> = {
  draft: "planSection.planDraft",
  pending: "planSection.planPending",
  executing: "planSection.planExecuting",
  paused: "planSection.planPaused",
  completed: "planSection.planCompleted",
  aborted: "planSection.planAborted",
};

function stepStatusClass(status: PlanStepStatus): string {
  return `plan-step--${status}`;
}

function isCurrentStep(step: PlanStep, idx: number): boolean {
  const plan = activePlan.value;
  if (!plan) return false;
  for (let i = 0; i < idx; i++) {
    if (plan.steps[i].status !== "completed" && plan.steps[i].status !== "skipped") {
      return false;
    }
  }
  return step.status !== "completed" && step.status !== "skipped";
}

function isPlanActive(status: PlanStatus): boolean {
  return status !== "completed" && status !== "aborted";
}

const completedCount = computed(() => {
  if (!activePlan.value) return 0;
  return activePlan.value.steps.filter((s) => s.status === "completed").length;
});
</script>

<template>
  <aside class="plan-panel">
    <div class="plan-panel__header">
      <span class="plan-panel__title">{{ t("aiAssistant.modePlan") }}</span>
      <span
        v-if="activePlan"
        class="plan-panel__status"
        :class="`plan-panel__status--${activePlan.status}`"
      >
        {{ t(PLAN_STATUS_KEY[activePlan.status]) }}
      </span>
    </div>

    <div v-if="aiPlanState.error" class="plan-panel__error">{{ aiPlanState.error }}</div>

    <div class="plan-panel__body">
      <!-- 无活动 Plan：创建表单 -->
      <div v-if="!activePlan" class="plan-panel__empty">
        <p class="plan-panel__hint">{{ t("planSection.hint") }}</p>
        <div class="plan-panel__create">
          <textarea
            v-model="newPlanGoal"
            class="plan-panel__goal-input"
            :placeholder="t('planSection.fieldGoalPlaceholder')"
            rows="3"
          />
          <button
            class="plan-panel__btn plan-panel__btn--primary"
            :disabled="!newPlanGoal.trim() || creating"
            @click="handleCreateFromGoal"
          >
            {{ creating ? t("common.creating") : t("planSection.createPlan") }}
          </button>
        </div>
      </div>

      <!-- 活动 Plan 详情 -->
      <div v-else class="plan-panel__detail">
        <div class="plan-panel__goal">
          <span class="plan-panel__goal-label">{{ t("planSection.fieldGoal") }}:</span>
          <span class="plan-panel__goal-text">{{ activePlan.goal }}</span>
        </div>

        <div class="plan-panel__progress">
          {{ completedCount }} / {{ activePlan.steps.length }}
        </div>

        <div v-if="isPlanActive(activePlan.status)" class="plan-panel__actions">
          <button v-if="activePlan.steps.length > 0" class="plan-panel__btn plan-panel__btn--success" @click="handleApproveAll">
            {{ t("planSection.approveAll") }}
          </button>
          <button v-if="activePlan.steps.length > 0" class="plan-panel__btn" @click="handleRejectAll">
            {{ t("planSection.rejectAll") }}
          </button>
          <button
            v-if="activePlan.status === 'paused' || activePlan.steps.length === 0"
            class="plan-panel__btn plan-panel__btn--warning"
            @click="handleReplan"
          >
            {{ t("planSection.replan") }}
          </button>
          <button class="plan-panel__btn plan-panel__btn--danger" @click="handleAbort">
            {{ t("planSection.abort") }}
          </button>
        </div>

        <p v-if="activePlan.steps.length === 0" class="plan-panel__empty-steps">
          {{ t("planPanel.noSteps") }}
        </p>

        <ol class="plan-panel__steps">
          <li
            v-for="(step, idx) in activePlan.steps"
            :key="planStepKey(step)"
            class="plan-step"
            :class="[
              stepStatusClass(step.status),
              { 'plan-step--current': isCurrentStep(step, idx) },
            ]"
          >
            <div class="plan-step__header">
              <span class="plan-step__icon" :class="stepStatusClass(step.status)" aria-hidden="true">
                <span v-if="step.status === 'pending'">○</span>
                <span v-else-if="step.status === 'approved'">✓</span>
                <span v-else-if="step.status === 'executing'">◐</span>
                <span v-else-if="step.status === 'completed'">✓</span>
                <span v-else-if="step.status === 'failed'">✗</span>
                <span v-else-if="step.status === 'skipped'">→</span>
              </span>
              <span class="plan-step__title">{{ idx + 1 }}. {{ step.title }}</span>
              <span class="plan-step__status" :class="stepStatusClass(step.status)">
                {{ t(STEP_STATUS_KEY[step.status]) }}
              </span>
            </div>

            <p v-if="step.description" class="plan-step__desc">{{ step.description }}</p>

            <div v-if="step.tool" class="plan-step__tool">
              <code>{{ step.tool }}</code>
              <code v-if="step.args" class="plan-step__args">{{ step.args }}</code>
            </div>

            <div v-if="step.result" class="plan-step__result">
              <span class="plan-step__result-label">{{ t("planSection.resultLabel") }}</span>
              <pre class="plan-step__result-content">{{ step.result }}</pre>
            </div>

            <div v-if="step.error" class="plan-step__error">
              <span class="plan-step__error-label">{{ t("planSection.errorLabel") }}</span>
              <pre class="plan-step__error-content">{{ step.error }}</pre>
            </div>

            <div class="plan-step__actions">
              <button
                v-if="step.status === 'pending'"
                class="plan-panel__btn plan-panel__btn--success plan-panel__btn--sm"
                @click="handleApproveStep(idx)"
              >
                {{ t("planSection.approve") }}
              </button>
              <button
                v-if="step.status === 'approved'"
                class="plan-panel__btn plan-panel__btn--primary plan-panel__btn--sm"
                @click="handleExecuteStep(idx)"
              >
                {{ t("planSection.execute") }}
              </button>
              <button
                v-if="step.status === 'failed'"
                class="plan-panel__btn plan-panel__btn--sm"
                @click="handleExecuteStep(idx)"
              >
                {{ t("planSection.retry") }}
              </button>
              <button
                v-if="step.status === 'failed'"
                class="plan-panel__btn plan-panel__btn--warning plan-panel__btn--sm"
                @click="handleSkipStep(idx)"
              >
                {{ t("planSection.skip") }}
              </button>
              <button
                v-if="step.status === 'completed' || step.status === 'failed'"
                class="plan-panel__btn plan-panel__btn--sm"
                :disabled="replayLoading"
                @click="handleReplay(idx)"
              >
                {{ t("planSection.replay") }}
              </button>
            </div>
          </li>
        </ol>
      </div>
    </div>

    <!-- 回放详情对话框 -->
    <div
      v-if="replayStep"
      class="plan-panel__replay-overlay"
    >
      <button
        type="button"
        class="dialog-backdrop-button"
        tabindex="-1"
        :aria-label="t('a11y.closeDialog')"
        @click="replayStep = null"
      />
      <FocusTrapDialog
        class="plan-panel__replay-dialog"
        :aria-label="t('planSection.replayTitle')"
        @close="replayStep = null"
      >
        <h3 class="plan-panel__replay-title">{{ t("planSection.replayTitle") }}</h3>
        <div class="plan-panel__replay-field">
          <label class="plan-panel__label">{{ t("planSection.fieldStepTool") }}</label>
          <code>{{ replayStep.tool || "—" }}</code>
        </div>
        <div class="plan-panel__replay-field">
          <label class="plan-panel__label">{{ t("planSection.fieldStepArgs") }}</label>
          <pre class="plan-panel__replay-content">{{ replayStep.args || "—" }}</pre>
        </div>
        <div class="plan-panel__replay-field">
          <label class="plan-panel__label">{{ t("planSection.resultLabel") }}</label>
          <pre class="plan-panel__replay-content">{{ replayStep.result || "—" }}</pre>
        </div>
        <div v-if="replayStep.error" class="plan-panel__replay-field">
          <label class="plan-panel__label">{{ t("planSection.errorLabel") }}</label>
          <pre class="plan-panel__replay-content plan-panel__replay-content--error">{{ replayStep.error }}</pre>
        </div>
        <div class="plan-panel__replay-actions">
          <button class="plan-panel__btn plan-panel__btn--primary" @click="replayStep = null">
            {{ t("common.close") }}
          </button>
        </div>
      </FocusTrapDialog>
    </div>
  </aside>
</template>

<style scoped>
.plan-panel {
  display: flex;
  flex-direction: column;
  width: 320px;
  flex-shrink: 0;
  border-left: 1px solid var(--color-border-subtle, #2a2a2a);
  background: var(--color-bg-surface, #1e1e1e);
  overflow: hidden;
}
.plan-panel__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 10px 14px;
  border-bottom: 1px solid var(--color-border-subtle, #2a2a2a);
  flex-shrink: 0;
}
.plan-panel__title {
  font-size: 13px;
  font-weight: 600;
  color: var(--color-text-primary, #e0e0e0);
}
.plan-panel__status {
  font-size: 11px;
  padding: 2px 8px;
  border-radius: 4px;
  text-transform: uppercase;
  letter-spacing: 0.3px;
  background: var(--color-bg-elevated, #252525);
  color: var(--color-text-secondary, #aaa);
}
.plan-panel__status--executing {
  background: rgba(59, 130, 246, 0.18);
  color: var(--color-primary);
}
.plan-panel__status--completed {
  background: var(--color-success-container);
  color: var(--color-success);
}
.plan-panel__status--aborted {
  background: var(--color-error-container);
  color: var(--color-error);
}
.plan-panel__status--paused {
  background: var(--color-warning-container);
  color: var(--color-warning);
}
.plan-panel__error {
  padding: 8px 14px;
  font-size: 12px;
  color: var(--color-error);
  background: var(--color-error-container);
  border-bottom: 1px solid rgba(239, 68, 68, 0.2);
}
.plan-panel__body {
  flex: 1;
  overflow-y: auto;
  padding: 12px 14px;
}
.plan-panel__empty {
  display: flex;
  flex-direction: column;
  gap: 14px;
  align-items: stretch;
}
.plan-panel__hint {
  font-size: 12px;
  line-height: 1.5;
  color: var(--color-text-secondary, #aaa);
  margin: 0;
}
.plan-panel__create {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.plan-panel__goal-input {
  width: 100%;
  font-size: 13px;
  font-family: inherit;
  padding: 8px;
  background: var(--color-bg-elevated, #252525);
  color: var(--color-text-primary, #e0e0e0);
  border: 1px solid var(--color-border-default, #3a3a3a);
  border-radius: 6px;
  box-sizing: border-box;
  resize: vertical;
}
.plan-panel__detail {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.plan-panel__goal {
  font-size: 13px;
  line-height: 1.5;
  color: var(--color-text-primary, #e0e0e0);
}
.plan-panel__goal-label {
  font-size: 11px;
  color: var(--color-text-secondary, #888);
  margin-right: 4px;
}
.plan-panel__progress {
  font-size: 11px;
  color: var(--color-text-secondary, #888);
}
.plan-panel__actions {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.plan-panel__steps {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.plan-step {
  padding: 8px 10px;
  border-radius: 6px;
  background: var(--color-bg-elevated, #252525);
  font-size: 12px;
  line-height: 1.4;
  border-left: 3px solid var(--color-border-default, #444);
}
.plan-step--current {
  border-left-color: var(--color-accent, #3b82f6);
}
.plan-step--high,
.plan-step--pending {
  border-left-color: var(--color-warning);
}
.plan-step--approved {
  border-left-color: var(--color-success);
}
.plan-step--executing {
  border-left-color: var(--color-primary);
}
.plan-step--completed {
  border-left-color: var(--color-success);
}
.plan-step--failed {
  border-left-color: var(--color-error);
}
.plan-step--skipped {
  border-left-color: var(--color-text-tertiary);
}
.plan-step__header {
  display: flex;
  align-items: baseline;
  gap: 6px;
}
.plan-step__icon {
  font-size: 12px;
}
.plan-step__icon.plan-step--completed {
  color: var(--color-success);
}
.plan-step__icon.plan-step--failed {
  color: var(--color-error);
}
.plan-step__title {
  flex: 1;
  font-weight: 500;
  color: var(--color-text-primary, #e0e0e0);
}
.plan-step__status {
  font-size: 10px;
  color: var(--color-text-secondary, #888);
  text-transform: uppercase;
}
.plan-step__desc {
  margin: 4px 0 0 0;
  color: var(--color-text-secondary, #aaa);
  font-size: 11px;
}
.plan-step__tool {
  margin-top: 4px;
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
}
.plan-step__tool code {
  font-size: 10px;
  background: rgba(255, 255, 255, 0.06);
  padding: 1px 5px;
  border-radius: 3px;
}
.plan-step__args {
  color: var(--color-text-secondary, #aaa);
}
.plan-step__result,
.plan-step__error {
  margin-top: 6px;
  font-size: 11px;
}
.plan-step__result-label,
.plan-step__error-label {
  display: block;
  color: var(--color-text-secondary, #888);
  margin-bottom: 2px;
}
.plan-step__result-content,
.plan-step__error-content {
  margin: 0;
  padding: 6px;
  background: var(--color-bg-base, #131316);
  border-radius: 4px;
  font-family: var(--font-mono, monospace);
  font-size: 10px;
  color: var(--color-text-primary, #e0e0e0);
  white-space: pre-wrap;
  word-break: break-word;
  max-height: 120px;
  overflow-y: auto;
}
.plan-step__error-content {
  color: var(--color-error);
}
.plan-step__actions {
  margin-top: 6px;
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}
.plan-panel__btn {
  padding: 4px 10px;
  font-size: 11px;
  border: 1px solid var(--color-border-default, #3a3a3a);
  border-radius: 4px;
  background: transparent;
  color: var(--color-text-primary, #e0e0e0);
  cursor: pointer;
}
.plan-panel__btn:hover:not(:disabled) {
  background: var(--color-bg-elevated, #2a2a2a);
}
.plan-panel__btn:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}
.plan-panel__btn--sm {
  padding: 2px 8px;
  font-size: 10px;
}
.plan-panel__btn--primary {
  background: var(--color-accent, #3b82f6);
  border-color: var(--color-accent, #3b82f6);
  color: var(--color-on-primary);
}
.plan-panel__btn--primary:hover:not(:disabled) {
  background: var(--color-accent-hover, #2563eb);
}
.plan-panel__btn--success {
  background: var(--color-success);
  border-color: var(--color-success);
  color: var(--color-on-primary);
}
.plan-panel__btn--success:hover:not(:disabled) {
  background: color-mix(in srgb, var(--color-success) 80%, black);
}
.plan-panel__btn--warning {
  background: var(--color-warning);
  border-color: var(--color-warning);
  color: var(--color-on-primary);
}
.plan-panel__btn--warning:hover:not(:disabled) {
  background: color-mix(in srgb, var(--color-warning) 80%, black);
}
.plan-panel__btn--danger {
  background: var(--color-error);
  border-color: var(--color-error);
  color: var(--color-on-primary);
}
.plan-panel__btn--danger:hover:not(:disabled) {
  background: color-mix(in srgb, var(--color-error) 80%, black);
}

/* 回放对话框 */
.plan-panel__replay-overlay {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.5);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
  padding: 16px;
}
.dialog-backdrop-button {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  margin: 0;
  padding: 0;
  border: 0;
  background: transparent;
  cursor: default;
}
.plan-panel__replay-dialog {
  position: relative;
  z-index: 1;
  width: 540px;
  max-width: 92vw;
  max-height: 80vh;
  overflow-y: auto;
  padding: 16px;
  background: var(--color-bg-surface, #1e1e1e);
  border: 1px solid var(--color-border-default, #333);
  border-radius: 8px;
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.plan-panel__replay-title {
  margin: 0 0 4px 0;
  font-size: 14px;
  font-weight: 600;
}
.plan-panel__replay-field {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.plan-panel__label {
  font-size: 11px;
  color: var(--color-text-secondary, #888);
}
.plan-panel__replay-content {
  margin: 0;
  padding: 8px;
  background: var(--color-bg-base, #131316);
  border-radius: 4px;
  font-family: var(--font-mono, monospace);
  font-size: 11px;
  white-space: pre-wrap;
  word-break: break-word;
  max-height: 200px;
  overflow-y: auto;
}
.plan-panel__replay-content--error {
  color: var(--color-error);
}
.plan-panel__replay-actions {
  display: flex;
  justify-content: flex-end;
}
</style>
