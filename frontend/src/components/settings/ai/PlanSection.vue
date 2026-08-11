<script setup lang="ts">
// Koyori IDE 组件 · Plan Section。
// 喵，这是 Plan Section，负责 Koyori IDE 的界面呈现喵~
/**
 * Plan 11 Task 9 Step 5 - Plan 模式设置分区。
 * 展示当前活动 Plan（目标、步骤列表与状态）及审批操作。
 * （单步、全部批准、全部拒绝、编辑后批准）+ 回放（点击已完成步骤查看
 * 工具调用详情）+ 失败处理（跳过、重新规划、中止）。
 * Plan 与 Goal 互斥（Step 8）：创建新 Plan 会替换 active Plan。
 * G-SEC-02（Step 9）：每步执行经过后端 CheckCommand。
 */
import { onMounted, ref, computed } from "vue";
import { ElMessageBox } from "element-plus";
import { useI18n } from "@/lib/i18n";
import FocusTrapDialog from "@/components/common/FocusTrapDialog.vue";
import {
  aiPlanState,
  activePlan,
  createPlan,
  approveStep,
  editStep,
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

onMounted(async () => {
  await refreshActivePlan();
});

// ---------------------------------------------------------------------------
// 创建 Plan 表单
// ---------------------------------------------------------------------------

const showCreateForm = ref(false);
const newPlanId = ref("");
const newPlanGoal = ref("");
const newStepTitle = ref("");
const newStepDescription = ref("");
const newStepTool = ref("");
const newStepArgs = ref("");
const draftSteps: PlanStep[] = [];
const planStepKeys = new WeakMap<PlanStep, string>();
let planStepKeySequence = 0;

function planStepKey(step: PlanStep): string {
  const existing = planStepKeys.get(step);
  if (existing) return existing;
  const key = `plan-step-${++planStepKeySequence}`;
  planStepKeys.set(step, key);
  return key;
}

function addDraftStep(): void {
  if (!newStepTitle.value.trim()) return;
  draftSteps.push({
    title: newStepTitle.value.trim(),
    description: newStepDescription.value.trim(),
    status: "pending",
    tool: newStepTool.value.trim() || undefined,
    args: newStepArgs.value.trim() || undefined,
  });
  newStepTitle.value = "";
  newStepDescription.value = "";
  newStepTool.value = "";
  newStepArgs.value = "";
}

const draftStepList = computed(() => draftSteps);

function removeDraftStep(idx: number): void {
  draftSteps.splice(idx, 1);
}

async function handleCreatePlan(): Promise<void> {
  if (!newPlanId.value.trim() || !newPlanGoal.value.trim()) return;
  // 若还有未添加的草稿步骤，自动追加
  if (newStepTitle.value.trim()) addDraftStep();
  if (draftSteps.length === 0) {
    alert(t("planSection.noStepsToCreate"));
    return;
  }
  const ok = await createPlan(newPlanId.value.trim(), newPlanGoal.value.trim(), [...draftSteps]);
  if (ok) {
    showCreateForm.value = false;
    newPlanId.value = "";
    newPlanGoal.value = "";
    draftSteps.length = 0;
  } else if (aiPlanState.error) {
    alert(aiPlanState.error);
  }
}

// ---------------------------------------------------------------------------
// 编辑步骤（编辑后批准
// ---------------------------------------------------------------------------

const editingStepIdx = ref<number | null>(null);
const editBuffer = ref<PlanStep>({
  title: "",
  description: "",
  status: "approved",
  tool: "",
  args: "",
});

function startEditStep(planId: string, idx: number, step: PlanStep): void {
  editingStepIdx.value = idx;
  editBuffer.value = { ...step };
  void planId;
}

function cancelEditStep(): void {
  editingStepIdx.value = null;
}

async function saveEditStep(planId: string): Promise<void> {
  const stepIdx = editingStepIdx.value;
  if (stepIdx === null) return;

  const ok = await editStep(planId, stepIdx, { ...editBuffer.value });
  if (ok) {
    editingStepIdx.value = null;
  } else if (aiPlanState.error) {
    alert(aiPlanState.error);
  }
}

// ---------------------------------------------------------------------------
// 审批与执
// ---------------------------------------------------------------------------

async function handleApproveStep(planId: string, idx: number): Promise<void> {
  await approveStep(planId, idx);
}

async function handleApproveAll(planId: string): Promise<void> {
  await approveAll(planId);
}

async function handleRejectAll(planId: string): Promise<void> {
  await rejectAll(planId);
}

async function handleExecuteStep(planId: string, idx: number): Promise<void> {
  await executeStep(planId, idx);
}

async function handleSkipStep(planId: string, idx: number): Promise<void> {
  await skipStep(planId, idx);
}

async function handleReplan(planId: string): Promise<void> {
  const input = prompt(t("planSection.replanPrompt"));
  if (!input) return;
  // 简单解析：每行一个步"Title | tool | args"
  const steps: PlanStep[] = input
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line)
    .map((line) => {
      const [title, tool, args] = line.split("|").map((s) => s.trim());
      return { title, description: "", status: "pending" as PlanStepStatus, tool: tool || undefined, args: args || undefined };
    });
  await replan(planId, steps);
}

async function handleAbortPlan(planId: string): Promise<void> {
  try {
    await ElMessageBox.confirm(
      t("planSection.abortConfirm"),
      t("common.confirm"),
      { type: "warning", confirmButtonText: t("common.confirm"), cancelButtonText: t("common.cancel") },
    );
  } catch {
    return;
  }
  await abortPlan(planId);
}

// ---------------------------------------------------------------------------
// 回放（Step 6
// ---------------------------------------------------------------------------

const replayStep = ref<PlanStep | null>(null);
const replayLoading = ref(false);

async function handleReplay(planId: string, idx: number): Promise<void> {
  replayLoading.value = true;
  const step = await getStepResult(planId, idx);
  replayLoading.value = false;
  replayStep.value = step;
}

// ---------------------------------------------------------------------------
// 状态映
// ---------------------------------------------------------------------------

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
  // 当前高亮：第一个非 completed/skipped 的步骤。
  const plan = activePlan.value;
  if (!plan) return false;
  for (let i = 0; i < idx; i++) {
    if (plan.steps[i].status !== "completed" && plan.steps[i].status !== "skipped") {
      return false;
    }
  }
  return step.status !== "completed" && step.status !== "skipped";
}

function isPlanPaused(status: PlanStatus): boolean {
  return status === "paused";
}

function isPlanActive(status: PlanStatus): boolean {
  return status !== "completed" && status !== "aborted";
}
</script>

<template>
  <section class="settings-section">
    <h2 class="section-title">{{ t("settings.plan") }}</h2>
    <p class="section-hint">{{ t("planSection.hint") }}</p>

    <div v-if="aiPlanState.error" class="plan-error">{{ aiPlanState.error }}</div>

    <!-- 无活动 Plan -->
    <div v-if="!activePlan" class="plan-empty">
      <p>{{ t("planSection.noActivePlan") }}</p>
      <el-button type="primary" size="small" @click="showCreateForm = !showCreateForm">
        {{ t("planSection.createPlan") }}
      </el-button>
    </div>

    <!-- 创建 Plan 表单 -->
    <div v-if="showCreateForm" class="plan-create-form">
      <h3 class="plan-form-title">{{ t("planSection.createTitle") }}</h3>
      <div class="plan-form-field">
        <label class="plan-label">{{ t("planSection.fieldPlanId") }}</label>
        <input type="text" v-model="newPlanId" class="plan-input" :placeholder="t('planSection.fieldPlanIdPlaceholder')" />
      </div>
      <div class="plan-form-field">
        <label class="plan-label">{{ t("planSection.fieldGoal") }}</label>
        <input type="text" v-model="newPlanGoal" class="plan-input" :placeholder="t('planSection.fieldGoalPlaceholder')" />
      </div>

      <div class="plan-form-field">
        <label class="plan-label">{{ t("planSection.addStep") }}</label>
        <input type="text" v-model="newStepTitle" class="plan-input" :placeholder="t('planSection.fieldStepTitle')" />
        <textarea v-model="newStepDescription" class="plan-input plan-input--textarea" :placeholder="t('planSection.fieldStepDescription')" rows="2"></textarea>
        <input type="text" v-model="newStepTool" class="plan-input" :placeholder="t('planSection.fieldStepTool')" />
        <input type="text" v-model="newStepArgs" class="plan-input" :placeholder="t('planSection.fieldStepArgs')" />
        <el-button size="small" @click="addDraftStep">{{ t("planSection.addStep") }}</el-button>
      </div>

      <div v-if="draftStepList.length" class="plan-draft-steps">
        <div v-for="(s, i) in draftStepList" :key="planStepKey(s)" class="plan-draft-step">
          <span class="plan-draft-step-title">{{ i + 1 }}. {{ s.title }}</span>
          <span v-if="s.tool" class="plan-draft-step-tool">{{ s.tool }}</span>
          <el-button size="small" type="danger" @click="removeDraftStep(i)">{{ t("common.remove") }}</el-button>
        </div>
      </div>

      <div class="plan-form-actions">
        <el-button size="small" @click="showCreateForm = false">{{ t("common.cancel") }}</el-button>
        <el-button size="small" type="primary" @click="handleCreatePlan">{{ t("common.create") }}</el-button>
      </div>
    </div>

    <!-- 活动 Plan 详情 -->
    <div v-if="activePlan" class="plan-detail">
      <div class="plan-detail-header">
        <div class="plan-detail-meta">
          <span class="plan-id">{{ activePlan.id }}</span>
          <span class="plan-status-badge" :class="`plan-status-badge--${activePlan.status}`">
            {{ t(PLAN_STATUS_KEY[activePlan.status]) }}
          </span>
        </div>
        <div class="plan-detail-goal">{{ activePlan.goal }}</div>
        <div class="plan-detail-actions" v-if="isPlanActive(activePlan.status)">
          <el-button v-if="activePlan.steps.length > 0" size="small" type="success" @click="handleApproveAll(activePlan.id)">
            {{ t("planSection.approveAll") }}
          </el-button>
          <el-button v-if="activePlan.steps.length > 0" size="small" type="danger" @click="handleRejectAll(activePlan.id)">
            {{ t("planSection.rejectAll") }}
          </el-button>
          <el-button size="small" type="warning" @click="handleReplan(activePlan.id)" v-if="isPlanPaused(activePlan.status) || activePlan.steps.length === 0">
            {{ t("planSection.replan") }}
          </el-button>
          <el-button size="small" @click="handleAbortPlan(activePlan.id)">
            {{ t("planSection.abort") }}
          </el-button>
        </div>
      </div>

      <p v-if="activePlan.steps.length === 0" class="plan-empty-steps">
        {{ t("planPanel.noSteps") }}
      </p>

      <!-- 步骤列表 -->
      <ol class="plan-steps">
        <li
          v-for="(step, idx) in activePlan.steps"
          :key="planStepKey(step)"
          class="plan-step"
          :class="[stepStatusClass(step.status), { 'plan-step--current': isCurrentStep(step, idx) }]"
        >
          <div class="plan-step-header">
            <span class="plan-step-icon" :class="stepStatusClass(step.status)" aria-hidden="true">
              <span v-if="step.status === 'pending'">○</span>
              <span v-else-if="step.status === 'approved'">✓</span>
              <span v-else-if="step.status === 'executing'">◐</span>
              <span v-else-if="step.status === 'completed'">✓</span>
              <span v-else-if="step.status === 'failed'">✗</span>
              <span v-else-if="step.status === 'skipped'">→</span>
            </span>
            <span class="plan-step-title">{{ idx + 1 }}. {{ step.title }}</span>
            <span class="plan-step-status" :class="stepStatusClass(step.status)">
              {{ t(STEP_STATUS_KEY[step.status]) }}
            </span>
          </div>

          <p v-if="step.description" class="plan-step-description">{{ step.description }}</p>

          <div v-if="step.tool" class="plan-step-tool">
            <code>{{ step.tool }}</code>
            <code v-if="step.args" class="plan-step-args">{{ step.args }}</code>
          </div>

          <!-- 结果回显 -->
          <div v-if="step.result" class="plan-step-result">
            <span class="plan-step-result-label">{{ t("planSection.resultLabel") }}</span>
            <pre class="plan-step-result-content">{{ step.result }}</pre>
          </div>

          <!-- 失败原因 -->
          <div v-if="step.error" class="plan-step-error">
            <span class="plan-step-error-label">{{ t("planSection.errorLabel") }}</span>
            <pre class="plan-step-error-content">{{ step.error }}</pre>
          </div>

          <!-- 编辑表单 -->
          <div v-if="editingStepIdx === idx" class="plan-step-edit">
            <input type="text" v-model="editBuffer.title" class="plan-input" :placeholder="t('planSection.fieldStepTitle')" />
            <textarea v-model="editBuffer.description" class="plan-input plan-input--textarea" rows="2"></textarea>
            <input type="text" v-model="editBuffer.tool" class="plan-input" :placeholder="t('planSection.fieldStepTool')" />
            <input type="text" v-model="editBuffer.args" class="plan-input" :placeholder="t('planSection.fieldStepArgs')" />
            <div class="plan-step-edit-actions">
              <el-button size="small" @click="cancelEditStep">{{ t("common.cancel") }}</el-button>
              <el-button size="small" type="primary" @click="saveEditStep(activePlan.id)">{{ t("planSection.editAndApprove") }}</el-button>
            </div>
          </div>

          <!-- 操作按钮 -->
          <div v-else class="plan-step-actions">
            <el-button v-if="step.status === 'pending'" size="small" type="success" @click="handleApproveStep(activePlan.id, idx)">
              {{ t("planSection.approve") }}
            </el-button>
            <el-button v-if="step.status === 'pending'" size="small" @click="startEditStep(activePlan.id, idx, step)">
              {{ t("planSection.editStep") }}
            </el-button>
            <el-button v-if="step.status === 'approved'" size="small" type="primary" @click="handleExecuteStep(activePlan.id, idx)">
              {{ t("planSection.execute") }}
            </el-button>
            <!-- 失败处理（Step 7）-->
            <el-button v-if="step.status === 'failed'" size="small" @click="handleExecuteStep(activePlan.id, idx)">
              {{ t("planSection.retry") }}
            </el-button>
            <el-button v-if="step.status === 'failed'" size="small" type="warning" @click="handleSkipStep(activePlan.id, idx)">
              {{ t("planSection.skip") }}
            </el-button>
            <!-- 回放（Step 6）-->
            <el-button v-if="step.status === 'completed' || step.status === 'failed'" size="small" :loading="replayLoading" @click="handleReplay(activePlan.id, idx)">
              {{ t("planSection.replay") }}
            </el-button>
          </div>
        </li>
      </ol>
    </div>

    <!-- 回放详情对话框（Step 6）-->
    <div
      v-if="replayStep"
      class="plan-replay-overlay"
    >
      <button
        type="button"
        class="dialog-backdrop-button"
        tabindex="-1"
        :aria-label="t('a11y.closeDialog')"
        @click="replayStep = null"
      />
      <FocusTrapDialog
        class="plan-replay-dialog"
        :aria-label="t('planSection.replayTitle')"
        @close="replayStep = null"
      >
        <h3 class="plan-replay-title">{{ t("planSection.replayTitle") }}</h3>
        <div class="plan-replay-field">
          <label class="plan-label">{{ t("planSection.fieldStepTool") }}</label>
          <code>{{ replayStep.tool || "" }}</code>
        </div>
        <div class="plan-replay-field">
          <label class="plan-label">{{ t("planSection.fieldStepArgs") }}</label>
          <pre class="plan-replay-content">{{ replayStep.args || "" }}</pre>
        </div>
        <div class="plan-replay-field">
          <label class="plan-label">{{ t("planSection.resultLabel") }}</label>
          <pre class="plan-replay-content">{{ replayStep.result || "" }}</pre>
        </div>
        <div v-if="replayStep.error" class="plan-replay-field">
          <label class="plan-label">{{ t("planSection.errorLabel") }}</label>
          <pre class="plan-replay-content plan-replay-content--error">{{ replayStep.error }}</pre>
        </div>
        <div class="plan-replay-actions">
          <el-button size="small" type="primary" @click="replayStep = null">{{ t("common.close") }}</el-button>
        </div>
      </FocusTrapDialog>
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

.plan-error {
  color: var(--color-error, #f44336);
  font-size: 12px;
  margin-bottom: 16px;
}

.plan-empty {
  padding: 32px 0;
  text-align: center;
  color: var(--color-text-tertiary);
  font-size: 13px;
  display: flex;
  flex-direction: column;
  gap: 12px;
  align-items: center;
}

.plan-create-form {
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-sm);
  padding: 16px;
  margin-bottom: 16px;
  background: var(--color-bg-surface-container-low);
}

.plan-form-title {
  font-size: 14px;
  font-weight: 600;
  margin-bottom: 12px;
  color: var(--color-text-primary);
}

.plan-form-field {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin-bottom: 12px;
}

.plan-label {
  font-size: 12px;
  color: var(--color-text-secondary);
  font-weight: 500;
}

.plan-input {
  font-size: 13px;
  padding: 6px 8px;
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-xs);
  background: var(--color-bg-surface);
  color: var(--color-text-primary);
  width: 100%;
  box-sizing: border-box;
}

.plan-input--textarea {
  font-family: var(--font-mono);
  resize: vertical;
  min-height: 60px;
}

.plan-draft-steps {
  margin: 8px 0 12px;
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.plan-draft-step {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 8px;
  background: var(--color-bg-surface);
  border-radius: var(--radius-xs);
  font-size: 12px;
}

.plan-draft-step-title {
  flex: 1;
  color: var(--color-text-primary);
}

.plan-draft-step-tool {
  font-family: var(--font-mono);
  font-size: 10px;
  color: var(--color-text-tertiary);
  background: var(--color-bg-surface-container);
  padding: 1px 4px;
  border-radius: var(--radius-xs);
}

.plan-form-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 8px;
}

.plan-detail {
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-sm);
  padding: 16px;
}

.plan-detail-header {
  margin-bottom: 16px;
}

.plan-detail-meta {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 6px;
}

.plan-id {
  font-family: var(--font-mono);
  font-size: 12px;
  color: var(--color-text-secondary);
}

.plan-status-badge {
  font-size: 10px;
  padding: 2px 6px;
  border-radius: var(--radius-xs);
  text-transform: uppercase;
  font-weight: 500;
}

.plan-status-badge--draft { color: var(--color-text-tertiary); background: var(--color-bg-surface-container); }
.plan-status-badge--pending { color: #ff9800; background: rgba(255, 152, 0, 0.1); }
.plan-status-badge--executing { color: #2196f3; background: rgba(33, 150, 243, 0.1); }
.plan-status-badge--paused { color: #ff5722; background: rgba(255, 87, 34, 0.1); }
.plan-status-badge--completed { color: #4caf50; background: rgba(76, 175, 80, 0.1); }
.plan-status-badge--aborted { color: var(--color-error, #f44336); background: rgba(244, 67, 54, 0.1); }

.plan-detail-goal {
  font-size: 14px;
  font-weight: 500;
  color: var(--color-text-primary);
  margin-bottom: 8px;
}

.plan-detail-actions {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}

.plan-steps {
  list-style: none;
  padding: 0;
  margin: 0;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.plan-step {
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-xs);
  padding: 10px 12px;
  background: var(--color-bg-surface);
  transition: border-color var(--transition-fast);
}

.plan-step--current {
  border-color: var(--color-primary, #2196f3);
  box-shadow: 0 0 0 2px rgba(33, 150, 243, 0.15);
}

.plan-step--completed {
  opacity: 0.8;
}

.plan-step--failed {
  border-color: var(--color-error, #f44336);
}

.plan-step-header {
  display: flex;
  align-items: center;
  gap: 8px;
}

.plan-step-icon {
  font-size: 14px;
  width: 18px;
  text-align: center;
  flex-shrink: 0;
}

.plan-step-icon--pending { color: var(--color-text-tertiary); }
.plan-step-icon--approved { color: #ff9800; }
.plan-step-icon--executing { color: #2196f3; }
.plan-step-icon--completed { color: #4caf50; }
.plan-step-icon--failed { color: var(--color-error, #f44336); }
.plan-step-icon--skipped { color: var(--color-text-tertiary); }

.plan-step-title {
  flex: 1;
  font-size: 13px;
  font-weight: 500;
  color: var(--color-text-primary);
}

.plan-step-status {
  font-size: 10px;
  text-transform: uppercase;
  font-weight: 500;
}

.plan-step-status--pending { color: var(--color-text-tertiary); }
.plan-step-status--approved { color: #ff9800; }
.plan-step-status--executing { color: #2196f3; }
.plan-step-status--completed { color: #4caf50; }
.plan-step-status--failed { color: var(--color-error, #f44336); }
.plan-step-status--skipped { color: var(--color-text-tertiary); }

.plan-step-description {
  font-size: 12px;
  color: var(--color-text-secondary);
  margin: 4px 0 0;
  line-height: 1.4;
}

.plan-step-tool {
  margin-top: 4px;
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
}

.plan-step-tool code {
  font-family: var(--font-mono);
  font-size: 11px;
  background: var(--color-bg-surface-container);
  padding: 1px 5px;
  border-radius: var(--radius-xs);
  color: var(--color-text-primary);
}

.plan-step-args {
  color: var(--color-text-tertiary);
}

.plan-step-result,
.plan-step-error {
  margin-top: 6px;
}

.plan-step-result-label,
.plan-step-error-label {
  font-size: 10px;
  color: var(--color-text-tertiary);
  text-transform: uppercase;
  font-weight: 500;
}

.plan-step-result-content,
.plan-step-error-content {
  font-family: var(--font-mono);
  font-size: 11px;
  background: var(--color-bg-surface-container);
  padding: 6px 8px;
  border-radius: var(--radius-xs);
  margin: 2px 0 0;
  max-height: 200px;
  overflow: auto;
  color: var(--color-text-primary);
  white-space: pre-wrap;
  word-break: break-word;
}

.plan-step-error-content {
  background: rgba(244, 67, 54, 0.08);
  color: var(--color-error, #f44336);
}

.plan-step-actions {
  margin-top: 6px;
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
}

.plan-step-edit {
  margin-top: 6px;
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.plan-step-edit-actions {
  display: flex;
  gap: 6px;
  justify-content: flex-end;
  margin-top: 4px;
}

.plan-replay-overlay {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.4);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
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

.plan-replay-dialog {
  position: relative;
  z-index: 1;
  background: var(--color-bg-surface);
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-md);
  padding: 24px;
  width: 560px;
  max-width: 90vw;
  max-height: 90vh;
  overflow-y: auto;
}

.plan-replay-title {
  font-size: 16px;
  font-weight: 600;
  margin-bottom: 16px;
  color: var(--color-text-primary);
}

.plan-replay-field {
  margin-bottom: 12px;
}

.plan-replay-content {
  font-family: var(--font-mono);
  font-size: 11px;
  background: var(--color-bg-surface-container);
  padding: 6px 8px;
  border-radius: var(--radius-xs);
  margin: 4px 0 0;
  max-height: 200px;
  overflow: auto;
  color: var(--color-text-primary);
  white-space: pre-wrap;
  word-break: break-word;
}

.plan-replay-content--error {
  background: rgba(244, 67, 54, 0.08);
  color: var(--color-error, #f44336);
}

.plan-replay-actions {
  display: flex;
  justify-content: flex-end;
  margin-top: 16px;
}
</style>
