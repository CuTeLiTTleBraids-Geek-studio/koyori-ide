<script setup lang="ts">
// Koyori IDE 组件 · Model Permission Section。
// 喵，这是 Model Permission Section，负责 Koyori IDE 的界面呈现喵~
/**
 * Plan 11 Task 12 Step 7-8 — 模型权限分配设置分区。
 *
 * Step 7: 操作列表 + 模型 dropdown + fallback + 测试 + 成本统计
 * Step 8: 用量仪表盘（按天/周/月/操作/模型统计 + 预算告警 + 趋势图）
 *
 * G-SEC-07（Step 9）：所有调用走 UseStoredKey+ConfigID（后端 ResolveModelFor）。
 */
import { onMounted, ref, computed } from "vue";
import { ElMessageBox } from "element-plus";
import { useI18n } from "@/lib/i18n";
import {
  aiPermissionState,
  loadAssignments,
  saveAssignment,
  loadUsageSummary,
  loadCostSuggestions,
  checkBudget,
  resetUsage,
} from "@/stores/aiPermission";
import type { AIOperation, ModelAssignment, BudgetAlert } from "@/stores/aiPermission";

const { t } = useI18n();

// 选中的操作（用于编辑）
const selectedOperation = ref<AIOperation | null>(null);
const selectedPeriod = ref<string>("month");
const budgetInput = ref<BudgetAlert>({ monthlyBudget: 10, thresholdPct: 80 });

onMounted(async () => {
  await loadAssignments();
  await Promise.all([
    loadUsageSummary(selectedPeriod.value),
    loadCostSuggestions(),
  ]);
});

const selectedAssignment = computed<ModelAssignment | null>(() => {
  if (!selectedOperation.value) return null;
  return aiPermissionState.assignments.find((a) => a.operation === selectedOperation.value) ?? null;
});

const OPERATIONS: AIOperation[] = [
  "chat", "inline-completion", "agent", "review",
  "commit-message", "title-generation", "plan", "goal",
];

function operationLabel(op: AIOperation): string {
  return t(`modelPermission.op.${op}`);
}

// 编辑表单状态
const editForm = ref<ModelAssignment>({
  operation: "chat",
  providerId: "",
  model: "",
  reasoningEffort: "",
  fallbackReasoningEffort: "",
  temperature: 0,
  maxTokens: 0,
  fallbackProviderId: "",
  fallbackModel: "",
  disabled: false,
});

function selectOperation(op: AIOperation): void {
  selectedOperation.value = op;
  const a = aiPermissionState.assignments.find((x) => x.operation === op);
  if (a) {
    editForm.value = { ...a };
  } else {
    editForm.value = { operation: op, providerId: "", model: "", disabled: false };
  }
}

async function handleSave(): Promise<void> {
  await saveAssignment({ ...editForm.value });
}

async function handlePeriodChange(): Promise<void> {
  await loadUsageSummary(selectedPeriod.value);
}

async function handleBudgetCheck(): Promise<void> {
  await checkBudget(budgetInput.value);
}

async function handleResetUsage(): Promise<void> {
  // prompt-4 Task 11: 使用 Element Plus 原生弹窗，避免浏览器 confirm 样式。
  try {
    await ElMessageBox.confirm(
      t("modelPermission.resetConfirm"),
      t("modelPermission.resetUsage"),
      {
        type: "warning",
        confirmButtonText: t("common.confirm"),
        cancelButtonText: t("common.cancel"),
      },
    );
  } catch {
    return; // 用户取消
  }
  await resetUsage();
  await Promise.all([
    loadUsageSummary(selectedPeriod.value),
    loadCostSuggestions(),
  ]);
}

// 用量趋势数据（按天排序）
const usageTrend = computed(() => {
  if (!aiPermissionState.usageSummary?.byDay) return [];
  return Object.entries(aiPermissionState.usageSummary.byDay)
    .map(([day, u]) => ({ day, ...u }))
    .sort((a, b) => a.day.localeCompare(b.day))
    .slice(-30); // 最近 30 天
});

// 按模型统计列表
const modelUsageList = computed(() => {
  if (!aiPermissionState.usageSummary?.byModel) return [];
  return Object.entries(aiPermissionState.usageSummary.byModel)
    .map(([model, u]) => ({ model, ...u }))
    .sort((a, b) => b.cost - a.cost);
});

// 按操作统计列表
const operationUsageList = computed(() => {
  if (!aiPermissionState.usageSummary?.byOperation) return [];
  return Object.entries(aiPermissionState.usageSummary.byOperation)
    .map(([op, u]) => ({ op: op as AIOperation, ...u }))
    .sort((a, b) => b.cost - a.cost);
});

const maxDayCost = computed(() => {
  if (usageTrend.value.length === 0) return 1;
  return Math.max(...usageTrend.value.map((d) => d.cost), 0.01);
});
</script>

<template>
  <section class="settings-section model-permission-section">
    <h2 class="section-title">{{ t("settings.modelPermission") }}</h2>
    <p class="section-hint">{{ t("modelPermission.hint") }}</p>

    <div v-if="aiPermissionState.error" class="mp-error">
      {{ aiPermissionState.error }}
    </div>

    <!-- Step 7: 操作列表 -->
    <div class="mp-operations">
      <h3 class="mp-subtitle">{{ t("modelPermission.operations") }}</h3>
      <div class="mp-operation-grid">
        <button
          v-for="op in OPERATIONS"
          :key="op"
          type="button"
          class="mp-operation-btn"
          :class="{ 'is-selected': selectedOperation === op }"
          @click="selectOperation(op)"
        >
          <span class="mp-operation-name">{{ operationLabel(op) }}</span>
          <span v-if="aiPermissionState.assignments.find((a) => a.operation === op)?.disabled" class="mp-disabled-badge">
            {{ t("modelPermission.disabled") }}
          </span>
        </button>
      </div>
    </div>

    <!-- Step 7: 属性面板 -->
    <div v-if="selectedAssignment || selectedOperation" class="mp-detail">
      <h3 class="mp-subtitle">{{ t("modelPermission.properties") }}</h3>
      <div class="mp-form">
        <div class="mp-form-row">
          <label class="mp-label">{{ t("modelPermission.propOperation") }}</label>
          <span class="mp-value">{{ operationLabel(editForm.operation) }}</span>
        </div>
        <div class="mp-form-row">
          <label class="mp-label">{{ t("modelPermission.propProviderId") }}</label>
          <input type="text" v-model="editForm.providerId" class="mp-input" :placeholder="t('modelPermission.placeholderProviderId')" />
        </div>
        <div class="mp-form-row">
          <label class="mp-label">{{ t("modelPermission.propModel") }}</label>
          <input type="text" v-model="editForm.model" class="mp-input" :placeholder="t('modelPermission.placeholderModel')" />
        </div>
        <div class="mp-form-row">
          <label class="mp-label">{{ t("modelPermission.propReasoningEffort") }}</label>
          <select v-model="editForm.reasoningEffort" class="mp-input">
            <option value="">{{ t("modelPermission.reasoningDefault") }}</option>
            <option value="low">{{ t("modelPermission.reasoningLow") }}</option>
            <option value="medium">{{ t("modelPermission.reasoningMedium") }}</option>
            <option value="high">{{ t("modelPermission.reasoningHigh") }}</option>
          </select>
        </div>
        <div class="mp-form-row">
          <label class="mp-label">{{ t("modelPermission.propTemperature") }}</label>
          <input type="number" v-model.number="editForm.temperature" step="0.1" min="0" max="2" class="mp-input" />
        </div>
        <div class="mp-form-row">
          <label class="mp-label">{{ t("modelPermission.propMaxTokens") }}</label>
          <input type="number" v-model.number="editForm.maxTokens" step="100" min="0" class="mp-input" />
        </div>
        <div class="mp-form-row">
          <label class="mp-label">{{ t("modelPermission.propFallbackProvider") }}</label>
          <input type="text" v-model="editForm.fallbackProviderId" class="mp-input" :placeholder="t('modelPermission.placeholderFallback')" />
        </div>
        <div class="mp-form-row">
          <label class="mp-label">{{ t("modelPermission.propFallbackModel") }}</label>
          <input type="text" v-model="editForm.fallbackModel" class="mp-input" :placeholder="t('modelPermission.placeholderFallbackModel')" />
        </div>
        <div class="mp-form-row">
          <label class="mp-label">{{ t("modelPermission.propFallbackReasoningEffort") }}</label>
          <select v-model="editForm.fallbackReasoningEffort" class="mp-input">
            <option value="">{{ t("modelPermission.reasoningDefault") }}</option>
            <option value="low">{{ t("modelPermission.reasoningLow") }}</option>
            <option value="medium">{{ t("modelPermission.reasoningMedium") }}</option>
            <option value="high">{{ t("modelPermission.reasoningHigh") }}</option>
          </select>
        </div>
        <div class="mp-form-row">
          <label class="mp-label">{{ t("modelPermission.propDisabled") }}</label>
          <input type="checkbox" v-model="editForm.disabled" />
          <span class="mp-hint">{{ t("modelPermission.disabledHint") }}</span>
        </div>
        <div class="mp-form-actions">
          <el-button size="small" type="primary" @click="handleSave">{{ t("common.save") }}</el-button>
        </div>
      </div>
    </div>

    <!-- Step 8: 用量仪表盘 -->
    <div class="mp-dashboard">
      <h3 class="mp-subtitle">{{ t("modelPermission.usageDashboard") }}</h3>
      <div class="mp-period-selector">
        <button
          v-for="p in ['day', 'week', 'month', 'all']"
          :key="p"
          type="button"
          class="mp-period-btn"
          :class="{ 'is-active': selectedPeriod === p }"
          @click="selectedPeriod = p; handlePeriodChange()"
        >
          {{ t(`modelPermission.period.${p}`) }}
        </button>
      </div>

      <div v-if="aiPermissionState.usageSummary" class="mp-summary-cards">
        <div class="mp-summary-card">
          <span class="mp-card-label">{{ t("modelPermission.totalTokensIn") }}</span>
          <span class="mp-card-value">{{ (aiPermissionState.usageSummary.totalTokensIn ?? 0).toLocaleString() }}</span>
        </div>
        <div class="mp-summary-card">
          <span class="mp-card-label">{{ t("modelPermission.totalTokensOut") }}</span>
          <span class="mp-card-value">{{ (aiPermissionState.usageSummary.totalTokensOut ?? 0).toLocaleString() }}</span>
        </div>
        <div class="mp-summary-card">
          <span class="mp-card-label">{{ t("modelPermission.totalCost") }}</span>
          <span class="mp-card-value">${{ (aiPermissionState.usageSummary.totalCost ?? 0).toFixed(4) }}</span>
        </div>
      </div>

      <!-- 趋势图（简单柱状图） -->
      <div v-if="usageTrend.length" class="mp-trend-chart">
        <h4 class="mp-chart-title">{{ t("modelPermission.trendChart") }}</h4>
        <div class="mp-trend-bars">
          <div
            v-for="d in usageTrend"
            :key="d.day"
            class="mp-trend-bar"
            :style="{ height: `${(d.cost / maxDayCost) * 100}%` }"
            :title="`${d.day}: $${(d.cost ?? 0).toFixed(4)}`"
          >
            <span class="mp-trend-label">{{ d.day.slice(5) }}</span>
          </div>
        </div>
      </div>

      <!-- 按操作统计 -->
      <div v-if="operationUsageList.length" class="mp-usage-table">
        <h4 class="mp-chart-title">{{ t("modelPermission.byOperation") }}</h4>
        <table>
          <thead>
            <tr>
              <th>{{ t("modelPermission.colOperation") }}</th>
              <th>{{ t("modelPermission.colTokensIn") }}</th>
              <th>{{ t("modelPermission.colTokensOut") }}</th>
              <th>{{ t("modelPermission.colCost") }}</th>
              <th>{{ t("modelPermission.colCount") }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="u in operationUsageList" :key="u.op">
              <td>{{ operationLabel(u.op) }}</td>
              <td>{{ u.tokensIn.toLocaleString() }}</td>
              <td>{{ u.tokensOut.toLocaleString() }}</td>
              <td>${{ (u.cost ?? 0).toFixed(4) }}</td>
              <td>{{ u.count }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <!-- 按模型统计 -->
      <div v-if="modelUsageList.length" class="mp-usage-table">
        <h4 class="mp-chart-title">{{ t("modelPermission.byModel") }}</h4>
        <table>
          <thead>
            <tr>
              <th>{{ t("modelPermission.colModel") }}</th>
              <th>{{ t("modelPermission.colTokensIn") }}</th>
              <th>{{ t("modelPermission.colTokensOut") }}</th>
              <th>{{ t("modelPermission.colCost") }}</th>
              <th>{{ t("modelPermission.colCount") }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="u in modelUsageList" :key="u.model">
              <td>{{ u.model }}</td>
              <td>{{ u.tokensIn.toLocaleString() }}</td>
              <td>{{ u.tokensOut.toLocaleString() }}</td>
              <td>${{ (u.cost ?? 0).toFixed(4) }}</td>
              <td>{{ u.count }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- Step 5: 成本优化建议 -->
    <div v-if="aiPermissionState.suggestions.length" class="mp-suggestions">
      <h3 class="mp-subtitle">{{ t("modelPermission.costSuggestions") }}</h3>
      <ul class="mp-suggestion-list">
        <li v-for="s in aiPermissionState.suggestions" :key="s.operation" class="mp-suggestion">
          <span class="mp-suggestion-op">{{ operationLabel(s.operation) }}</span>
          <span class="mp-suggestion-model">{{ s.currentModel }}</span>
          <span class="mp-suggestion-reason">{{ s.reason }}</span>
          <span class="mp-suggestion-savings">~${{ (s.estimatedSavings ?? 0).toFixed(4) }}</span>
        </li>
      </ul>
    </div>

    <!-- 预算告警 -->
    <div class="mp-budget">
      <h3 class="mp-subtitle">{{ t("modelPermission.budgetAlert") }}</h3>
      <div class="mp-budget-form">
        <label class="mp-label">{{ t("modelPermission.monthlyBudget") }}</label>
        <input type="number" v-model.number="budgetInput.monthlyBudget" step="1" min="0" class="mp-input" />
        <label class="mp-label">{{ t("modelPermission.thresholdPct") }}</label>
        <input type="number" v-model.number="budgetInput.thresholdPct" step="5" min="0" max="100" class="mp-input" />
        <el-button size="small" @click="handleBudgetCheck">{{ t("modelPermission.checkBudget") }}</el-button>
      </div>
      <div v-if="aiPermissionState.budgetAlert" class="mp-budget-alert">
        {{ aiPermissionState.budgetAlert }}
      </div>
    </div>

    <div class="mp-actions">
      <el-button size="small" type="danger" @click="handleResetUsage">{{ t("modelPermission.resetUsage") }}</el-button>
    </div>
  </section>
</template>

<style scoped>
.model-permission-section {
  display: flex;
  flex-direction: column;
  gap: 20px;
}

.section-hint {
  color: var(--color-text-tertiary);
  font-size: 12px;
  margin-bottom: 12px;
}

.mp-error {
  padding: 8px 12px;
  background: var(--color-danger-container, #fef2f2);
  color: var(--color-danger, #b91c1c);
  border-radius: var(--radius-sm);
  font-size: 12px;
}

.mp-subtitle {
  font-size: 14px;
  font-weight: 600;
  margin: 0 0 8px 0;
  color: var(--color-text-secondary);
}

.mp-operation-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(140px, 1fr));
  gap: 8px;
}

.mp-operation-btn {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 8px 12px;
  border: 1px solid var(--color-border-default);
  background: var(--color-bg-surface);
  color: var(--color-text-secondary);
  font-size: 12px;
  border-radius: var(--radius-xs);
  cursor: pointer;
  transition: all var(--transition-fast);
}

.mp-operation-btn:hover {
  border-color: var(--color-primary);
}

.mp-operation-btn.is-selected {
  border-color: var(--color-primary);
  background: var(--color-primary-container);
  color: var(--color-on-primary-container);
  font-weight: 500;
}

.mp-operation-name {
  flex: 1;
}

.mp-disabled-badge {
  font-size: 10px;
  padding: 1px 6px;
  background: var(--color-danger-container, #fef2f2);
  color: var(--color-danger, #b91c1c);
  border-radius: var(--radius-xs);
}

.mp-detail {
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-sm);
  padding: 16px;
  background: var(--color-bg-surface);
}

.mp-form {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.mp-form-row {
  display: grid;
  grid-template-columns: 140px 1fr;
  gap: 12px;
  align-items: center;
}

.mp-label {
  font-size: 12px;
  color: var(--color-text-tertiary);
  font-weight: 500;
}

.mp-value {
  font-size: 13px;
  color: var(--color-text-primary);
}

.mp-input {
  padding: 4px 8px;
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-xs);
  font-size: 12px;
  background: var(--color-bg-surface);
  color: var(--color-text-primary);
  width: 100%;
}

.mp-hint {
  font-size: 11px;
  color: var(--color-text-tertiary);
}

.mp-form-actions {
  display: flex;
  gap: 8px;
}

.mp-dashboard {
  border-top: 1px solid var(--color-border-default);
  padding-top: 16px;
}

.mp-period-selector {
  display: flex;
  gap: 4px;
  margin-bottom: 12px;
}

.mp-period-btn {
  padding: 4px 12px;
  border: 1px solid var(--color-border-default);
  background: var(--color-bg-surface);
  color: var(--color-text-secondary);
  font-size: 11px;
  border-radius: var(--radius-xs);
  cursor: pointer;
}

.mp-period-btn.is-active {
  border-color: var(--color-primary);
  background: var(--color-primary-container);
  color: var(--color-on-primary-container);
}

.mp-summary-cards {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 8px;
  margin-bottom: 16px;
}

.mp-summary-card {
  display: flex;
  flex-direction: column;
  padding: 12px;
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-sm);
  background: var(--color-bg-surface);
}

.mp-card-label {
  font-size: 11px;
  color: var(--color-text-tertiary);
}

.mp-card-value {
  font-size: 18px;
  font-weight: 600;
  color: var(--color-text-primary);
}

.mp-trend-chart {
  margin-bottom: 16px;
}

.mp-chart-title {
  font-size: 12px;
  font-weight: 500;
  margin: 0 0 8px 0;
  color: var(--color-text-secondary);
}

.mp-trend-bars {
  display: flex;
  align-items: flex-end;
  gap: 2px;
  height: 80px;
  border-bottom: 1px solid var(--color-border-default);
  padding-bottom: 2px;
  overflow-x: auto;
}

.mp-trend-bar {
  flex: 1;
  min-width: 8px;
  max-width: 24px;
  background: var(--color-primary);
  border-radius: 2px 2px 0 0;
  position: relative;
  min-height: 1px;
}

.mp-trend-label {
  position: absolute;
  bottom: -16px;
  left: 50%;
  transform: translateX(-50%);
  font-size: 9px;
  color: var(--color-text-tertiary);
  white-space: nowrap;
}

.mp-usage-table {
  margin-bottom: 16px;
}

.mp-usage-table table {
  width: 100%;
  border-collapse: collapse;
  font-size: 11px;
}

.mp-usage-table th,
.mp-usage-table td {
  padding: 4px 8px;
  border: 1px solid var(--color-border-default);
  text-align: left;
}

.mp-usage-table th {
  background: var(--color-bg-surface-container);
  font-weight: 500;
  color: var(--color-text-secondary);
}

.mp-suggestions {
  border-top: 1px solid var(--color-border-default);
  padding-top: 16px;
}

.mp-suggestion-list {
  list-style: none;
  padding: 0;
  margin: 0;
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.mp-suggestion {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 10px;
  background: var(--color-warning-container, #fffbeb);
  border-radius: var(--radius-xs);
  font-size: 11px;
}

.mp-suggestion-op {
  font-weight: 600;
  color: var(--color-text-primary);
}

.mp-suggestion-model {
  font-family: var(--font-mono);
  color: var(--color-text-secondary);
}

.mp-suggestion-reason {
  flex: 1;
  color: var(--color-text-secondary);
}

.mp-suggestion-savings {
  font-weight: 600;
  color: var(--color-success, #16a34a);
}

.mp-budget {
  border-top: 1px solid var(--color-border-default);
  padding-top: 16px;
}

.mp-budget-form {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.mp-budget-form .mp-label {
  width: auto;
}

.mp-budget-form .mp-input {
  width: 100px;
}

.mp-budget-alert {
  margin-top: 8px;
  padding: 8px 12px;
  background: var(--color-warning-container, #fffbeb);
  color: var(--color-warning, #f59e0b);
  border-radius: var(--radius-sm);
  font-size: 12px;
  font-weight: 500;
}

.mp-actions {
  display: flex;
  justify-content: flex-end;
}
</style>
