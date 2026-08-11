<script setup lang="ts">
// Koyori IDE 组件 · Workflow Section。
// 喵，这是 Workflow Section，负责 Koyori IDE 的界面呈现喵~
/**
 * Plan 11 Task 11 Step 6 — 工作流编排设置分区。
 *
 * 可视化展示已加载的工作流（节点画布 + 属性面板 + 触发器 + 运行历史）。
 *   - 节点画布：步骤列表 + 依赖关系（DependsOn）+ 类型图标
 *   - 属性面板：选中步骤的详细信息（命令/参数/条件/超时/失败策略）
 *   - 触发器：runOn 事件（startup/fileChange/file-saved/manual/schedule/shortcut）+ Condition
 *   - 运行历史：最近一次运行的步骤状态/耗时/输出/错误（Step 7）
 *
 * G-SEC-03（Step 8）：startup 触发器列入"待确认"，需用户显式点击运行；
 *   fileChange 需显式启用 + 防抖（后端 WorkflowEngine.ShouldTrigger）。
 * G-SEC-02（Step 9）：每步执行经后端 CheckCommand（由 runWorkflow 间接调用）。
 */
import { onMounted, ref, computed } from "vue";
import { ElMessageBox } from "element-plus";
import { useI18n } from "@/lib/i18n";
import {
  workflowState,
  hasWorkflows,
  pendingStartupWorkflows,
  loadWorkflows,
  runWorkflow,
  getWorkflowValidation,
  isWorkflowValid,
  createWorkflow,
  saveWorkflow,
  deleteWorkflow,
  renameWorkflow,
} from "@/stores/workflows";
import { appState } from "@/stores/app";
import type { WorkflowDef, WorkflowStep, WorkflowStepState, WorkflowStepStatus, WorkflowStepType, OnFailureAction } from "@/types";

const { t } = useI18n();

onMounted(async () => {
  if (appState.currentProject) {
    await loadWorkflows(appState.currentProject);
  }
});

// 选中的工作流名称（null 表示未选中）
const selectedWorkflowName = ref<string | null>(null);

// 选中的步骤名（用于属性面板）
const selectedStepName = ref<string | null>(null);

// ---- prompt-4 Task 12: 新建 / 编辑编辑器 ----
const editorOpen = ref(false);
const editorMode = ref<"create" | "edit">("create");
const editorName = ref("");
const editorDescription = ref("");
const editorTrigger = ref<string>("manual");
const editorTriggerGlob = ref("");
const workflowItemKeys = new WeakMap<object, string>();
let workflowItemKeySequence = 0;

function workflowItemKey(item: object): string {
  const existing = workflowItemKeys.get(item);
  if (existing) return existing;
  const key = `workflow-item-${++workflowItemKeySequence}`;
  workflowItemKeys.set(item, key);
  return key;
}

const editorSteps = ref<Array<{
  name: string;
  command: string;
  type: WorkflowStepType;
  onFailure: OnFailureAction;
  dependsOn: string;
  timeout: number;
}>>([]);

function blankStep() {
  return {
    name: "",
    command: "",
    type: "command" as WorkflowStepType,
    onFailure: "abort" as OnFailureAction,
    dependsOn: "",
    timeout: 0,
  };
}

function openCreateEditor(): void {
  editorMode.value = "create";
  editorName.value = "";
  editorDescription.value = "";
  editorTrigger.value = "manual";
  editorTriggerGlob.value = "";
  editorSteps.value = [blankStep()];
  editorOpen.value = true;
}

function openEditEditor(wf: WorkflowDef): void {
  editorMode.value = "edit";
  editorName.value = wf.name;
  editorDescription.value = wf.description ?? "";
  editorTrigger.value = wf.runOn?.event || "manual";
  editorTriggerGlob.value = wf.runOn?.glob || "";
  editorSteps.value = (wf.steps ?? []).map((s) => ({
    name: s.name,
    command: s.command,
    type: (s.type || "command") as WorkflowStepType,
    onFailure: (s.onFailure || "abort") as OnFailureAction,
    dependsOn: (s.dependsOn ?? []).join(", "),
    timeout: s.timeout ?? 0,
  }));
  if (editorSteps.value.length === 0) editorSteps.value = [blankStep()];
  editorOpen.value = true;
}

function addEditorStep(): void {
  editorSteps.value.push(blankStep());
}

function removeEditorStep(idx: number): void {
  editorSteps.value.splice(idx, 1);
  if (editorSteps.value.length === 0) editorSteps.value.push(blankStep());
}

function moveEditorStep(idx: number, dir: -1 | 1): void {
  const j = idx + dir;
  if (j < 0 || j >= editorSteps.value.length) return;
  const tmp = editorSteps.value[idx];
  editorSteps.value[idx] = editorSteps.value[j];
  editorSteps.value[j] = tmp;
}

function buildDefFromEditor(): WorkflowDef {
  const steps: WorkflowStep[] = editorSteps.value
    .filter((s) => s.name.trim() && s.command.trim())
    .map((s) => ({
      name: s.name.trim(),
      command: s.command.trim(),
      type: s.type,
      onFailure: s.onFailure,
      dependsOn: s.dependsOn
        ? s.dependsOn.split(",").map((x) => x.trim()).filter(Boolean)
        : undefined,
      timeout: s.timeout > 0 ? s.timeout : undefined,
    }));
  const def: WorkflowDef = {
    name: editorName.value.trim(),
    description: editorDescription.value.trim() || undefined,
    steps,
    requiresConfirmation: true,
    source: `.koyori-ide/workflows/${editorName.value.trim()}.yml`,
  };
  if (editorTrigger.value && editorTrigger.value !== "manual") {
    def.runOn = {
      event: editorTrigger.value,
      glob: editorTriggerGlob.value.trim() || undefined,
    };
  }
  return def;
}

async function handleEditorSave(): Promise<void> {
  const name = editorName.value.trim();
  if (!name) return;
  const def = buildDefFromEditor();
  if (def.steps.length === 0) return;
  let ok = false;
  if (editorMode.value === "create") {
    ok = await createWorkflow(name, def);
  } else {
    ok = await saveWorkflow(name, def);
  }
  if (ok) {
    editorOpen.value = false;
    selectedWorkflowName.value = name;
  }
}

async function handleDelete(name: string): Promise<void> {
  try {
    await ElMessageBox.confirm(
      t("workflowSection.deleteConfirm", { name }),
      t("workflowSection.delete"),
      {
        type: "warning",
        confirmButtonText: t("common.confirm"),
        cancelButtonText: t("common.cancel"),
      },
    );
  } catch {
    return;
  }
  const ok = await deleteWorkflow(name);
  if (ok && selectedWorkflowName.value === name) {
    selectedWorkflowName.value = null;
    selectedStepName.value = null;
  }
}

async function handleRename(oldName: string): Promise<void> {
  let newName = "";
  try {
    const { value } = await ElMessageBox.prompt(
      t("workflowSection.renamePrompt"),
      t("workflowSection.rename"),
      {
        inputValue: oldName,
        confirmButtonText: t("common.confirm"),
        cancelButtonText: t("common.cancel"),
      },
    );
    newName = (value ?? "").trim();
  } catch {
    return;
  }
  if (!newName || newName === oldName) return;
  const ok = await renameWorkflow(oldName, newName);
  if (ok) selectedWorkflowName.value = newName;
}

async function handleDuplicate(wf: WorkflowDef): Promise<void> {
  const copyName = `${wf.name}-copy`;
  const def: WorkflowDef = {
    ...wf,
    name: copyName,
    source: `.koyori-ide/workflows/${copyName}.yml`,
    requiresConfirmation: true,
  };
  const ok = await createWorkflow(copyName, def);
  if (ok) selectedWorkflowName.value = copyName;
}

const selectedWorkflow = computed<WorkflowDef | null>(() => {
  if (!selectedWorkflowName.value) return null;
  return workflowState.workflows.find((w) => w.name === selectedWorkflowName.value) ?? null;
});

const selectedStep = computed<WorkflowStep | null>(() => {
  if (!selectedWorkflow.value || !selectedStepName.value) return null;
  return selectedWorkflow.value.steps.find((s) => s.name === selectedStepName.value) ?? null;
});

// 选中工作流的最近一次运行步骤状态（Step 7: 运行历史）
const selectedStepStates = computed<WorkflowStepState[]>(() => {
  if (!selectedWorkflowName.value) return [];
  return workflowState.stepStates[selectedWorkflowName.value] ?? [];
});

const stepStateByName = computed<Map<string, WorkflowStepState>>(() => {
  const m = new Map<string, WorkflowStepState>();
  for (const s of selectedStepStates.value) m.set(s.name, s);
  return m;
});

function selectWorkflow(name: string): void {
  selectedWorkflowName.value = name;
  selectedStepName.value = null;
}

function selectStep(name: string): void {
  selectedStepName.value = name;
}

const isRunning = computed<boolean>(() => {
  if (!selectedWorkflowName.value) return false;
  return workflowState.running[selectedWorkflowName.value] === true;
});

async function handleRun(): Promise<void> {
  if (!selectedWorkflow.value || !appState.currentProject) return;
  await runWorkflow(selectedWorkflow.value, appState.currentProject);
}

function formatDuration(ms: number | undefined): string {
  if (!ms) return "-";
  if (ms < 1000) return `${ms}ms`;
  return `${(ms / 1000).toFixed(2)}s`;
}

function stepDuration(state: WorkflowStepState | undefined): string {
  if (!state || !state.startedAt || !state.finishedAt) return "-";
  return formatDuration(state.finishedAt - state.startedAt);
}

// 启动工作流待确认列表（G-SEC-03）
const pendingStartup = computed(() => pendingStartupWorkflows.value);

// 运行历史详情：选中步骤的运行结果
const selectedStepRuntime = computed<WorkflowStepState | null>(() => {
  if (!selectedStepName.value) return null;
  return stepStateByName.value.get(selectedStepName.value) ?? null;
});

// 状态图标映射
const STATUS_ICON: Record<WorkflowStepStatus, string> = {
  pending: "○",
  running: "◐",
  success: "✓",
  failed: "✗",
  skipped: "→",
};

function stepStatus(name: string): WorkflowStepStatus | undefined {
  return stepStateByName.value.get(name)?.status;
}

// 触发器配置描述
function triggerDescription(wf: WorkflowDef): string {
  if (!wf.runOn) return t("workflowSection.noTrigger");
  const parts = [wf.runOn.event];
  if (wf.runOn.glob) parts.push(`glob=${wf.runOn.glob}`);
  if (wf.runOn.workflowName) parts.push(`on=${wf.runOn.workflowName}`);
  return parts.join(", ");
}

// 验证错误展示
const validationErrors = computed(() => {
  if (!selectedWorkflowName.value) return [];
  const v = getWorkflowValidation(selectedWorkflowName.value);
  return v?.errors ?? [];
});

const isValid = computed<boolean>(() => {
  if (!selectedWorkflowName.value) return true;
  return isWorkflowValid(selectedWorkflowName.value);
});
</script>

<template>
  <section class="settings-section workflow-section">
    <h2 class="section-title">{{ t("settings.workflow") }}</h2>
    <p class="section-hint">{{ t("workflowSection.hint") }}</p>

    <div v-if="workflowState.errorMessage" class="workflow-error">
      {{ workflowState.errorMessage }}
    </div>

    <!-- G-SEC-03: 启动工作流待确认 -->
    <div v-if="pendingStartup.length" class="workflow-pending">
      <h3 class="workflow-subtitle">{{ t("workflowSection.pendingStartup") }}</h3>
      <ul class="workflow-pending-list">
        <li v-for="wf in pendingStartup" :key="wf.name" class="workflow-pending-item">
          <span class="workflow-pending-name">{{ wf.name }}</span>
          <el-button size="small" type="warning" @click="selectWorkflow(wf.name)">
            {{ t("workflowSection.review") }}
          </el-button>
        </li>
      </ul>
    </div>

    <!-- 工具栏：新建工作流 -->
    <div class="workflow-toolbar">
      <el-button
        type="primary"
        size="small"
        :disabled="!appState.currentProject"
        @click="openCreateEditor"
      >
        {{ t("workflowSection.create") }}
      </el-button>
      <span v-if="!appState.currentProject" class="workflow-toolbar-hint">
        {{ t("workflowSection.needProject") }}
      </span>
    </div>

    <!-- 无工作流 -->
    <div v-if="!hasWorkflows && !workflowState.loading" class="workflow-empty">
      <p>{{ t("workflowSection.noWorkflows") }}</p>
    </div>

    <!-- 加载中 -->
    <div v-if="workflowState.loading" class="workflow-loading">
      <p>{{ t("workflowSection.loading") }}</p>
    </div>

    <div v-if="hasWorkflows" class="workflow-layout">
      <!-- 左侧：工作流列表 -->
      <aside class="workflow-list">
        <h3 class="workflow-subtitle">{{ t("workflowSection.workflows") }}</h3>
        <ul class="workflow-list-items">
          <li
            v-for="wf in workflowState.workflows"
            :key="wf.name"
            class="workflow-list-item"
            :class="{ 'is-selected': selectedWorkflowName === wf.name }"
          >
            <button
              type="button"
              class="workflow-list-btn"
              :class="{ 'is-invalid': !isWorkflowValid(wf.name) }"
              @click="selectWorkflow(wf.name)"
            >
              <span class="workflow-list-name">{{ wf.name }}</span>
              <span v-if="wf.runOn" class="workflow-list-trigger">{{ wf.runOn.event }}</span>
              <span v-if="!isWorkflowValid(wf.name)" class="workflow-list-invalid" :title="t('workflowSection.invalid')">
                ⚠
              </span>
            </button>
            <div class="workflow-list-actions">
              <button type="button" class="workflow-mini-btn" :title="t('workflowSection.edit')" :aria-label="t('a11y.editWorkflow', { name: wf.name })" @click="openEditEditor(wf)">✎</button>
              <button type="button" class="workflow-mini-btn" :title="t('workflowSection.duplicate')" :aria-label="t('a11y.duplicateWorkflow', { name: wf.name })" @click="handleDuplicate(wf)">⧉</button>
              <button type="button" class="workflow-mini-btn" :title="t('workflowSection.rename')" :aria-label="t('a11y.renameWorkflow', { name: wf.name })" @click="handleRename(wf.name)">Aa</button>
              <button type="button" class="workflow-mini-btn workflow-mini-btn--danger" :title="t('workflowSection.delete')" :aria-label="t('a11y.deleteWorkflow', { name: wf.name })" @click="handleDelete(wf.name)">×</button>
            </div>
          </li>
        </ul>
      </aside>

      <!-- 右侧：详情区 -->
      <div v-if="selectedWorkflow" class="workflow-detail">
        <!-- 头部：名称 + 描述 + 操作 -->
        <div class="workflow-detail-header">
          <div class="workflow-detail-meta">
            <span class="workflow-detail-name">{{ selectedWorkflow.name }}</span>
            <span
              class="workflow-status-badge"
              :class="isRunning ? 'workflow-status-badge--running' : 'workflow-status-badge--idle'"
            >
              {{ isRunning ? t("workflowSection.running") : t("workflowSection.idle") }}
            </span>
            <span v-if="!isValid" class="workflow-status-badge workflow-status-badge--invalid">
              {{ t("workflowSection.invalid") }}
            </span>
          </div>
          <p v-if="selectedWorkflow.description" class="workflow-detail-desc">
            {{ selectedWorkflow.description }}
          </p>
          <p class="workflow-detail-trigger">
            <span class="workflow-label">{{ t("workflowSection.trigger") }}:</span>
            {{ triggerDescription(selectedWorkflow) }}
          </p>
          <p v-if="selectedWorkflow.requiresConfirmation" class="workflow-detail-confirm">
            <span class="workflow-label">{{ t("workflowSection.requiresConfirmation") }}:</span>
            {{ t("workflowSection.yes") }}
          </p>
          <div class="workflow-detail-actions">
            <el-button
              size="small"
              type="primary"
              :disabled="isRunning || !isValid"
              @click="handleRun"
            >
              {{ t("workflowSection.run") }}
            </el-button>
            <el-button size="small" @click="openEditEditor(selectedWorkflow)">
              {{ t("workflowSection.edit") }}
            </el-button>
            <el-button size="small" @click="handleRename(selectedWorkflow.name)">
              {{ t("workflowSection.rename") }}
            </el-button>
            <el-button size="small" type="danger" plain @click="handleDelete(selectedWorkflow.name)">
              {{ t("workflowSection.delete") }}
            </el-button>
          </div>
        </div>

        <!-- 验证错误 -->
        <div v-if="validationErrors.length" class="workflow-validation-errors">
          <h4 class="workflow-error-title">{{ t("workflowSection.validationErrors") }}</h4>
          <ul>
            <li v-for="e in validationErrors" :key="workflowItemKey(e)" class="workflow-validation-error">
              <code>{{ e.field }}</code>: {{ e.message }}
            </li>
          </ul>
        </div>

        <!-- 节点画布：步骤列表 + 依赖关系 -->
        <div class="workflow-canvas">
          <h3 class="workflow-subtitle">{{ t("workflowSection.steps") }}</h3>
          <ol class="workflow-steps">
            <li
              v-for="step in selectedWorkflow.steps"
              :key="step.name"
              class="workflow-step"
              :class="{
                'is-selected': selectedStepName === step.name,
                [`workflow-step--${stepStatus(step.name)}`]: !!stepStatus(step.name),
              }"
            >
              <button type="button" class="workflow-step-btn" @click="selectStep(step.name)">
                <span class="workflow-step-icon">{{ STATUS_ICON[stepStatus(step.name) ?? 'pending'] }}</span>
                <span class="workflow-step-name">{{ step.name }}</span>
                <span v-if="step.type" class="workflow-step-type">{{ step.type }}</span>
                <span v-if="step.dependsOn?.length" class="workflow-step-deps">
                  ← {{ step.dependsOn.join(", ") }}
                </span>
              </button>
            </li>
          </ol>
        </div>

        <!-- 属性面板：选中步骤的详情 -->
        <div v-if="selectedStep" class="workflow-props">
          <h3 class="workflow-subtitle">{{ t("workflowSection.properties") }}</h3>
          <dl class="workflow-prop-list">
            <div class="workflow-prop">
              <dt>{{ t("workflowSection.propName") }}</dt>
              <dd>{{ selectedStep.name }}</dd>
            </div>
            <div class="workflow-prop">
              <dt>{{ t("workflowSection.propType") }}</dt>
              <dd>{{ selectedStep.type || "command" }}</dd>
            </div>
            <div class="workflow-prop">
              <dt>{{ t("workflowSection.propCommand") }}</dt>
              <dd><code>{{ selectedStep.command }}</code></dd>
            </div>
            <div v-if="selectedStep.args?.length" class="workflow-prop">
              <dt>{{ t("workflowSection.propArgs") }}</dt>
              <dd><code>{{ selectedStep.args.join(" ") }}</code></dd>
            </div>
            <div v-if="selectedStep.cwd" class="workflow-prop">
              <dt>{{ t("workflowSection.propCwd") }}</dt>
              <dd><code>{{ selectedStep.cwd }}</code></dd>
            </div>
            <div v-if="selectedStep.condition" class="workflow-prop">
              <dt>{{ t("workflowSection.propCondition") }}</dt>
              <dd><code>{{ selectedStep.condition }}</code></dd>
            </div>
            <div v-if="selectedStep.dependsOn?.length" class="workflow-prop">
              <dt>{{ t("workflowSection.propDependsOn") }}</dt>
              <dd>{{ selectedStep.dependsOn.join(", ") }}</dd>
            </div>
            <div class="workflow-prop">
              <dt>{{ t("workflowSection.propOnFailure") }}</dt>
              <dd>{{ selectedStep.onFailure || "abort" }}</dd>
            </div>
            <div v-if="selectedStep.timeout" class="workflow-prop">
              <dt>{{ t("workflowSection.propTimeout") }}</dt>
              <dd>{{ selectedStep.timeout }}s</dd>
            </div>
          </dl>
        </div>

        <!-- 运行历史：最近一次运行的步骤结果（Step 7） -->
        <div v-if="selectedStepStates.length" class="workflow-history">
          <h3 class="workflow-subtitle">{{ t("workflowSection.runHistory") }}</h3>
          <table class="workflow-history-table">
            <thead>
              <tr>
                <th>{{ t("workflowSection.colStep") }}</th>
                <th>{{ t("workflowSection.colStatus") }}</th>
                <th>{{ t("workflowSection.colDuration") }}</th>
                <th>{{ t("workflowSection.colOutput") }}</th>
                <th>{{ t("workflowSection.colError") }}</th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="s in selectedStepStates"
                :key="s.name"
                :class="`workflow-history-row--${s.status}`"
              >
                <td>{{ s.name }}</td>
                <td>
                  <span class="workflow-status-icon">{{ STATUS_ICON[s.status] }}</span>
                  {{ s.status }}
                </td>
                <td>{{ stepDuration(s) }}</td>
                <td class="workflow-history-output">
                  <pre v-if="s.output">{{ s.output }}</pre>
                </td>
                <td class="workflow-history-error">
                  <pre v-if="s.error">{{ s.error }}</pre>
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <!-- 选中步骤的运行详情 -->
        <div v-if="selectedStepRuntime" class="workflow-step-runtime">
          <h4 class="workflow-subtitle">{{ t("workflowSection.stepRuntime") }}</h4>
          <dl class="workflow-prop-list">
            <div class="workflow-prop">
              <dt>{{ t("workflowSection.colStatus") }}</dt>
              <dd>{{ selectedStepRuntime.status }}</dd>
            </div>
            <div class="workflow-prop">
              <dt>{{ t("workflowSection.colDuration") }}</dt>
              <dd>{{ stepDuration(selectedStepRuntime) }}</dd>
            </div>
            <div v-if="selectedStepRuntime.output" class="workflow-prop">
              <dt>{{ t("workflowSection.colOutput") }}</dt>
              <dd><pre>{{ selectedStepRuntime.output }}</pre></dd>
            </div>
            <div v-if="selectedStepRuntime.error" class="workflow-prop">
              <dt>{{ t("workflowSection.colError") }}</dt>
              <dd><pre>{{ selectedStepRuntime.error }}</pre></dd>
            </div>
          </dl>
        </div>
      </div>
    </div>

    <!-- prompt-4 Task 12: 新建/编辑抽屉 -->
    <el-drawer
      v-model="editorOpen"
      :title="editorMode === 'create' ? t('workflowSection.create') : t('workflowSection.edit')"
      size="480px"
      direction="rtl"
      destroy-on-close
    >
      <div class="workflow-editor">
        <label class="workflow-editor-field">
          <span>{{ t("workflowSection.propName") }}</span>
          <el-input
            v-model="editorName"
            :disabled="editorMode === 'edit'"
            :placeholder="t('workflowSection.namePlaceholder')"
          />
        </label>
        <label class="workflow-editor-field">
          <span>{{ t("workflowSection.description") }}</span>
          <el-input v-model="editorDescription" type="textarea" :rows="2" />
        </label>
        <label class="workflow-editor-field">
          <span>{{ t("workflowSection.trigger") }}</span>
          <el-select v-model="editorTrigger" style="width: 100%">
            <el-option value="manual" :label="t('workflowSection.triggerManual')" />
            <el-option value="startup" label="startup" />
            <el-option value="file-saved" label="file-saved" />
            <el-option value="fileChange" label="fileChange" />
            <el-option value="workflow-completed" label="workflow-completed" />
            <el-option value="schedule" label="schedule" />
            <el-option value="shortcut" label="shortcut" />
          </el-select>
        </label>
        <label v-if="editorTrigger === 'file-saved' || editorTrigger === 'fileChange'" class="workflow-editor-field">
          <span>Glob</span>
          <el-input v-model="editorTriggerGlob" placeholder="**/*.{go,ts}" />
        </label>

        <div class="workflow-editor-steps-head">
          <h4>{{ t("workflowSection.steps") }}</h4>
          <el-button size="small" @click="addEditorStep">{{ t("workflowSection.addStep") }}</el-button>
        </div>
        <div
          v-for="(step, idx) in editorSteps"
          :key="workflowItemKey(step)"
          class="workflow-editor-step"
        >
          <div class="workflow-editor-step-bar">
            <span>#{{ idx + 1 }}</span>
            <button type="button" class="workflow-mini-btn" :aria-label="t('a11y.moveStepUp', { name: step.name })" @click="moveEditorStep(idx, -1)">↑</button>
            <button type="button" class="workflow-mini-btn" :aria-label="t('a11y.moveStepDown', { name: step.name })" @click="moveEditorStep(idx, 1)">↓</button>
            <button type="button" class="workflow-mini-btn workflow-mini-btn--danger" :aria-label="t('a11y.removeStep', { name: step.name })" @click="removeEditorStep(idx)">×</button>
          </div>
          <el-input v-model="step.name" :placeholder="t('workflowSection.propName')" size="small" />
          <el-input v-model="step.command" :placeholder="t('workflowSection.propCommand')" size="small" />
          <div class="workflow-editor-step-row">
            <el-select v-model="step.type" size="small" style="width: 40%">
              <el-option value="command" label="command" />
              <el-option value="ai" label="ai" />
              <el-option value="git" label="git" />
              <el-option value="file" label="file" />
              <el-option value="mcp" label="mcp" />
              <el-option value="skill" label="skill" />
            </el-select>
            <el-select v-model="step.onFailure" size="small" style="width: 40%">
              <el-option value="abort" label="abort" />
              <el-option value="continue" label="continue" />
              <el-option value="skip" label="skip" />
              <el-option value="retry" label="retry" />
            </el-select>
          </div>
          <el-input
            v-model="step.dependsOn"
            :placeholder="t('workflowSection.propDependsOn')"
            size="small"
          />
        </div>

        <div class="workflow-editor-actions">
          <el-button @click="editorOpen = false">{{ t("common.cancel") }}</el-button>
          <el-button type="primary" :disabled="!editorName.trim()" @click="handleEditorSave">
            {{ t("common.save") }}
          </el-button>
        </div>
      </div>
    </el-drawer>
  </section>
</template>

<style scoped>
.workflow-section {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.section-hint {
  color: var(--color-text-tertiary);
  font-size: 12px;
  margin-bottom: 12px;
}

.workflow-toolbar {
  display: flex;
  align-items: center;
  gap: 12px;
}
.workflow-toolbar-hint {
  font-size: 12px;
  color: var(--color-text-tertiary);
}
.workflow-list-actions {
  display: flex;
  gap: 2px;
  padding: 0 4px 4px;
}
.workflow-mini-btn {
  border: none;
  background: transparent;
  color: var(--color-text-secondary);
  cursor: pointer;
  padding: 2px 6px;
  border-radius: 4px;
  font-size: 12px;
}
.workflow-mini-btn:hover {
  background: var(--color-sidebar-hover);
  color: var(--color-text-primary);
}
.workflow-mini-btn--danger:hover {
  color: var(--el-color-danger, #f56c6c);
}
.workflow-editor {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.workflow-editor-field {
  display: flex;
  flex-direction: column;
  gap: 4px;
  font-size: 12px;
  color: var(--color-text-secondary);
}
.workflow-editor-steps-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
}
.workflow-editor-step {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 10px;
  border: 1px solid var(--color-border-default);
  border-radius: 8px;
}
.workflow-editor-step-bar {
  display: flex;
  align-items: center;
  gap: 4px;
  font-size: 12px;
  color: var(--color-text-secondary);
}
.workflow-editor-step-row {
  display: flex;
  gap: 8px;
}
.workflow-editor-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 8px;
}

.workflow-error {
  padding: 8px 12px;
  background: var(--color-danger-container, #fef2f2);
  color: var(--color-danger, #b91c1c);
  border-radius: var(--radius-sm);
  font-size: 12px;
}

.workflow-pending {
  padding: 12px;
  background: var(--color-warning-container, #fffbeb);
  border: 1px solid var(--color-warning, #f59e0b);
  border-radius: var(--radius-sm);
}

.workflow-subtitle {
  font-size: 13px;
  font-weight: 600;
  margin: 0 0 8px 0;
  color: var(--color-text-secondary);
}

.workflow-pending-list {
  list-style: none;
  padding: 0;
  margin: 0;
}

.workflow-pending-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 4px 0;
}

.workflow-pending-name {
  font-size: 13px;
  color: var(--color-text-primary);
}

.workflow-empty,
.workflow-loading {
  padding: 24px;
  text-align: center;
  color: var(--color-text-tertiary);
  font-size: 13px;
}

.workflow-layout {
  display: grid;
  grid-template-columns: 240px 1fr;
  gap: 16px;
  align-items: start;
}

.workflow-list {
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-sm);
  padding: 8px;
  background: var(--color-bg-surface);
}

.workflow-list-items {
  list-style: none;
  padding: 0;
  margin: 0;
}

.workflow-list-item {
  margin: 2px 0;
}

.workflow-list-btn {
  display: flex;
  align-items: center;
  gap: 6px;
  width: 100%;
  padding: 6px 10px;
  border: none;
  background: transparent;
  color: var(--color-text-secondary);
  font-size: 12px;
  text-align: left;
  border-radius: var(--radius-xs);
  cursor: pointer;
  transition: background var(--transition-fast);
}

.workflow-list-btn:hover {
  background: var(--color-sidebar-hover);
}

.workflow-list-btn.is-selected {
  background: var(--color-primary-container);
  color: var(--color-on-primary-container);
  font-weight: 500;
}

.workflow-list-btn.is-invalid {
  color: var(--color-danger, #b91c1c);
}

.workflow-list-name {
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.workflow-list-trigger {
  font-size: 10px;
  padding: 1px 6px;
  background: var(--color-bg-surface-container);
  border-radius: var(--radius-xs);
  color: var(--color-text-tertiary);
}

.workflow-list-invalid {
  color: var(--color-danger, #b91c1c);
  font-weight: 700;
}

.workflow-detail {
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-sm);
  padding: 16px;
  background: var(--color-bg-surface);
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.workflow-detail-header {
  border-bottom: 1px solid var(--color-border-default);
  padding-bottom: 12px;
}

.workflow-detail-meta {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 6px;
}

.workflow-detail-name {
  font-size: 16px;
  font-weight: 600;
  color: var(--color-text-primary);
}

.workflow-status-badge {
  font-size: 11px;
  padding: 2px 8px;
  border-radius: var(--radius-xs);
  font-weight: 500;
}

.workflow-status-badge--running {
  background: var(--color-primary-container);
  color: var(--color-on-primary-container);
}

.workflow-status-badge--idle {
  background: var(--color-bg-surface-container);
  color: var(--color-text-tertiary);
}

.workflow-status-badge--invalid {
  background: var(--color-danger-container, #fef2f2);
  color: var(--color-danger, #b91c1c);
}

.workflow-detail-desc {
  margin: 4px 0;
  font-size: 12px;
  color: var(--color-text-secondary);
}

.workflow-detail-trigger,
.workflow-detail-confirm {
  margin: 2px 0;
  font-size: 12px;
  color: var(--color-text-tertiary);
}

.workflow-label {
  font-weight: 500;
  color: var(--color-text-secondary);
}

.workflow-detail-actions {
  margin-top: 8px;
  display: flex;
  gap: 8px;
}

.workflow-validation-errors {
  padding: 8px 12px;
  background: var(--color-danger-container, #fef2f2);
  border-radius: var(--radius-sm);
  font-size: 12px;
}

.workflow-error-title {
  margin: 0 0 4px 0;
  font-size: 12px;
  color: var(--color-danger, #b91c1c);
}

.workflow-validation-error {
  color: var(--color-text-secondary);
}

.workflow-validation-error code {
  font-family: var(--font-mono);
  font-size: 11px;
  color: var(--color-danger, #b91c1c);
}

.workflow-canvas {
  border-top: 1px solid var(--color-border-default);
  padding-top: 12px;
}

.workflow-steps {
  list-style: none;
  padding: 0;
  margin: 0;
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.workflow-step {
  margin: 0;
}

.workflow-step-btn {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 6px 10px;
  border: 1px solid var(--color-border-default);
  background: var(--color-bg-surface);
  color: var(--color-text-secondary);
  font-size: 12px;
  text-align: left;
  border-radius: var(--radius-xs);
  cursor: pointer;
  transition: all var(--transition-fast);
}

.workflow-step-btn:hover {
  border-color: var(--color-primary);
}

.workflow-step.is-selected .workflow-step-btn {
  border-color: var(--color-primary);
  background: var(--color-primary-container);
  color: var(--color-on-primary-container);
}

.workflow-step--success .workflow-step-btn {
  border-color: var(--color-success, #16a34a);
}

.workflow-step--failed .workflow-step-btn {
  border-color: var(--color-danger, #b91c1c);
}

.workflow-step--running .workflow-step-btn {
  border-color: var(--color-primary);
  animation: workflowPulse 1.4s ease-in-out infinite;
}

@keyframes workflowPulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.6; }
}

.workflow-step-icon {
  font-size: 14px;
  width: 16px;
  text-align: center;
}

.workflow-step--success .workflow-step-icon {
  color: var(--color-success, #16a34a);
}

.workflow-step--failed .workflow-step-icon {
  color: var(--color-danger, #b91c1c);
}

.workflow-step--running .workflow-step-icon {
  color: var(--color-primary);
}

.workflow-step-name {
  font-weight: 500;
  flex: 1;
}

.workflow-step-type {
  font-size: 10px;
  padding: 1px 6px;
  background: var(--color-bg-surface-container);
  border-radius: var(--radius-xs);
  color: var(--color-text-tertiary);
}

.workflow-step-deps {
  font-size: 10px;
  color: var(--color-text-tertiary);
  font-family: var(--font-mono);
}

.workflow-props {
  border-top: 1px solid var(--color-border-default);
  padding-top: 12px;
}

.workflow-prop-list {
  margin: 0;
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.workflow-prop {
  display: grid;
  grid-template-columns: 120px 1fr;
  gap: 8px;
  font-size: 12px;
}

.workflow-prop dt {
  color: var(--color-text-tertiary);
  font-weight: 500;
}

.workflow-prop dd {
  margin: 0;
  color: var(--color-text-primary);
  word-break: break-all;
}

.workflow-prop code,
.workflow-prop pre {
  font-family: var(--font-mono);
  font-size: 11px;
  background: var(--color-bg-surface-container);
  padding: 2px 6px;
  border-radius: var(--radius-xs);
  color: var(--color-text-primary);
}

.workflow-history {
  border-top: 1px solid var(--color-border-default);
  padding-top: 12px;
}

.workflow-history-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 11px;
}

.workflow-history-table th,
.workflow-history-table td {
  padding: 4px 8px;
  border: 1px solid var(--color-border-default);
  text-align: left;
  vertical-align: top;
}

.workflow-history-table th {
  background: var(--color-bg-surface-container);
  font-weight: 500;
  color: var(--color-text-secondary);
}

.workflow-history-row--success td {
  color: var(--color-success, #16a34a);
}

.workflow-history-row--failed td {
  color: var(--color-danger, #b91c1c);
}

.workflow-history-output pre,
.workflow-history-error pre {
  margin: 0;
  max-width: 280px;
  max-height: 80px;
  overflow: auto;
  font-family: var(--font-mono);
  font-size: 10px;
  white-space: pre-wrap;
  word-break: break-all;
}

.workflow-step-runtime {
  border-top: 1px solid var(--color-border-default);
  padding-top: 12px;
}

.workflow-step-runtime pre {
  margin: 0;
  max-height: 120px;
  overflow: auto;
  font-family: var(--font-mono);
  font-size: 11px;
  background: var(--color-bg-surface-container);
  padding: 6px 8px;
  border-radius: var(--radius-xs);
  white-space: pre-wrap;
  word-break: break-all;
}

@media (max-width: 800px) {
  .workflow-layout {
    grid-template-columns: 1fr;
  }
}
</style>
