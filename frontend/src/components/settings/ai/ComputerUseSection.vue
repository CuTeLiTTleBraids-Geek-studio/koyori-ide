<script setup lang="ts">
// Koyori IDE 组件 · Computer Use Section。
// 喵，这是 Computer Use Section，负责 Koyori IDE 的界面呈现喵~
/**
 * Plan 11 Task 6 Step 5 - Computer Use 设置分区。
 * 展示并编辑 ComputerUseConfig（Step 5）：
 *   - 开关（G-SEC-12：默认禁用，启用需 explicitApproval 弹窗确认）
 *   - 截图质量 / 缩放比例（Step 2）
 *   - ConfirmationRequired（Step 3：每次操作前必须截图、AI 规划与用户确认）
 *   - 禁止快捷键黑名单（Step 6）
 *   - 禁止区域（Step 5/6）
 *   - 应用白名单（Step 5）
 *   - 录制模式开关（Step 4）
 *   - 操作日志审计（Step 7，最多 N 条不可逆操作记录）
 *
 * 安全（G-SEC-02 / G-SEC-06 / G-SEC-12）：
 *   - 启用时必须显式确认（视同 Restricted 扩展能力）
 *   - 禁止快捷键黑名单始终包含 OS 级危险快捷键（后端强制并集）
 *   - 审计日志只读展示，前端不可篡改。
 */
import { onMounted, ref, computed, watch } from "vue";
import { ElMessageBox } from "element-plus";
import { useI18n } from "@/lib/i18n";
import {
  computerUseState,
  computerUseConfig,
  loadComputerUseConfig,
  saveComputerUseConfig,
  refreshAuditLog,
  startRecording,
  stopRecording,
} from "@/stores/computerUse";
import type { ForbiddenZone } from "@/stores/computerUse";

const { t } = useI18n();

onMounted(async () => {
  await loadComputerUseConfig();
  await refreshAuditLog(50);
});

// 本地编辑副本（避免直接改 store；保存后才同步）
const draft = ref({
  enabled: false,
  confirmationRequired: true,
  screenshotQuality: 80,
  screenshotScale: 1.0,
  appWhitelistText: "",
  forbiddenHotkeysText: "",
  recordingEnabled: false,
});
const forbiddenZones = ref<ForbiddenZone[]>([]);
const newZone = ref<ForbiddenZone>({ name: "", x: 0, y: 0, w: 0, h: 0 });
const computerUseItemKeys = new WeakMap<object, string>();
let computerUseItemKeySequence = 0;

function computerUseItemKey(item: object): string {
  const existing = computerUseItemKeys.get(item);
  if (existing) return existing;
  const key = `computer-use-item-${++computerUseItemKeySequence}`;
  computerUseItemKeys.set(item, key);
  return key;
}

// 同步后端配置draft
function syncDraft(): void {
  const cfg = computerUseConfig.value;
  draft.value.enabled = cfg.enabled;
  draft.value.confirmationRequired = cfg.confirmationRequired;
  draft.value.screenshotQuality = cfg.screenshotQuality ?? 80;
  draft.value.screenshotScale = cfg.screenshotScale ?? 1.0;
  draft.value.appWhitelistText = (cfg.appWhitelist ?? []).join(", ");
  draft.value.forbiddenHotkeysText = (cfg.forbiddenHotkeys ?? []).join(", ");
  draft.value.recordingEnabled = cfg.recordingEnabled ?? false;
  forbiddenZones.value = (cfg.forbiddenZones ?? []).map((z) => ({ ...z }));
}

// 监听 config 变化（loadComputerUseConfig 完成后同步）
const configVersion = computed(() => computerUseConfig.value);
watch(configVersion, syncDraft, { immediate: true });

const isDirty = computed(() => {
  const cfg = computerUseConfig.value;
  return (
    draft.value.enabled !== cfg.enabled ||
    draft.value.confirmationRequired !== cfg.confirmationRequired ||
    draft.value.screenshotQuality !== (cfg.screenshotQuality ?? 80) ||
    draft.value.screenshotScale !== (cfg.screenshotScale ?? 1.0) ||
    draft.value.appWhitelistText !== (cfg.appWhitelist ?? []).join(", ") ||
    draft.value.forbiddenHotkeysText !== (cfg.forbiddenHotkeys ?? []).join(", ") ||
    draft.value.recordingEnabled !== (cfg.recordingEnabled ?? false) ||
    JSON.stringify(forbiddenZones.value) !== JSON.stringify((cfg.forbiddenZones ?? []).map((z) => ({ ...z })))
  );
});

function parseList(text: string): string[] {
  return text
    .split(/[,\n]/)
    .map((s) => s.trim())
    .filter((s) => s.length > 0);
}

/** G-SEC-12：启用 Computer Use 需 explicitApproval。 */
async function handleSave(): Promise<void> {
  const wasEnabled = computerUseConfig.value.enabled;
  if (!wasEnabled && draft.value.enabled) {
    try {
      await ElMessageBox.confirm(
        t("computerUseSection.enableConfirm"),
        t("common.confirm"),
        { type: "warning", confirmButtonText: t("common.confirm"), cancelButtonText: t("common.cancel") },
      );
    } catch {
      draft.value.enabled = false;
      return;
    }
  }
  const cfg = {
    enabled: draft.value.enabled,
    confirmationRequired: draft.value.confirmationRequired,
    screenshotQuality: draft.value.screenshotQuality,
    screenshotScale: draft.value.screenshotScale,
    appWhitelist: parseList(draft.value.appWhitelistText),
    forbiddenHotkeys: parseList(draft.value.forbiddenHotkeysText),
    forbiddenZones: forbiddenZones.value.map((z) => ({ ...z })),
    recordingEnabled: draft.value.recordingEnabled,
  };
  const ok = await saveComputerUseConfig(cfg);
  if (ok) {
    await refreshAuditLog(50);
  }
}

function addZone(): void {
  if (!newZone.value.name) return;
  forbiddenZones.value.push({ ...newZone.value });
  newZone.value = { name: "", x: 0, y: 0, w: 0, h: 0 };
}

function removeZone(idx: number): void {
  forbiddenZones.value.splice(idx, 1);
}

async function handleStartRecording(): Promise<void> {
  const ok = await startRecording();
  if (!ok && computerUseState.error) {
    alert(computerUseState.error);
  }
}

async function handleStopRecording(): Promise<void> {
  const actions = await stopRecording();
  if (actions.length > 0) {
    alert(t("computerUseSection.recordingStopped", { count: actions.length }));
  }
}
</script>

<template>
  <section class="settings-section">
    <h2 class="section-title">{{ t("settings.computerUse") }}</h2>
    <p class="section-hint">{{ t("computerUseSection.hint") }}</p>
    <!-- prompt-5 Task G / BUG-H3: platform ops are stubs — be honest in UI -->
    <p class="section-warning section-warning--experimental">
      <strong>{{ t("computerUseSection.experimentalLabel") }}</strong>
      {{ t("computerUseSection.experimentalNotice") }}
    </p>
    <p class="section-warning">
      <strong>{{ t("computerUseSection.warningLabel") }}</strong> {{ t("computerUseSection.warning") }}
    </p>

    <div v-if="computerUseState.error" class="cu-error">{{ computerUseState.error }}</div>

    <!-- 开关 + 截图参数 -->
    <div class="cu-field-group">
      <div class="cu-field">
        <label class="cu-label">
          <input type="checkbox" v-model="draft.enabled" />
          <span>{{ t("computerUseSection.enabled") }}</span>
        </label>
        <span class="cu-field-hint">{{ t("computerUseSection.enabledHint") }}</span>
      </div>

      <div class="cu-field">
        <label class="cu-label">
          <input type="checkbox" v-model="draft.confirmationRequired" />
          <span>{{ t("computerUseSection.confirmationRequired") }}</span>
        </label>
        <span class="cu-field-hint">{{ t("computerUseSection.confirmationRequiredHint") }}</span>
      </div>

      <div class="cu-field">
        <label class="cu-label">{{ t("computerUseSection.screenshotQuality") }}</label>
        <input
          type="number"
          min="1"
          max="100"
          v-model.number="draft.screenshotQuality"
          class="cu-input cu-input--narrow"
        />
      </div>

      <div class="cu-field">
        <label class="cu-label">{{ t("computerUseSection.screenshotScale") }}</label>
        <input
          type="number"
          min="0.1"
          max="1.0"
          step="0.1"
          v-model.number="draft.screenshotScale"
          class="cu-input cu-input--narrow"
        />
      </div>
    </div>

    <!-- 应用白名单 -->
    <div class="cu-field">
      <label class="cu-label">{{ t("computerUseSection.appWhitelist") }}</label>
      <textarea
        v-model="draft.appWhitelistText"
        :placeholder="t('computerUseSection.appWhitelistPlaceholder')"
        class="cu-input cu-input--textarea"
        rows="2"
      ></textarea>
      <span class="cu-field-hint">{{ t("computerUseSection.appWhitelistHint") }}</span>
    </div>

    <!-- 禁止快捷键黑名单 -->
    <div class="cu-field">
      <label class="cu-label">{{ t("computerUseSection.forbiddenHotkeys") }}</label>
      <textarea
        v-model="draft.forbiddenHotkeysText"
        :placeholder="t('computerUseSection.forbiddenHotkeysPlaceholder')"
        class="cu-input cu-input--textarea"
        rows="3"
      ></textarea>
      <span class="cu-field-hint">{{ t("computerUseSection.forbiddenHotkeysHint") }}</span>
    </div>

    <!-- 禁止区域 -->
    <div class="cu-field">
      <label class="cu-label">{{ t("computerUseSection.forbiddenZones") }}</label>
      <div v-if="forbiddenZones.length > 0" class="cu-zone-list">
        <div v-for="(z, i) in forbiddenZones" :key="computerUseItemKey(z)" class="cu-zone-item">
          <code>{{ z.name }} ({{ z.x }},{{ z.y }} {{ z.w }}x{{ z.h }})</code>
          <el-button size="small" text @click="removeZone(i)">{{ t("common.remove") }}</el-button>
        </div>
      </div>
      <div class="cu-zone-add">
        <input type="text" v-model="newZone.name" :placeholder="t('computerUseSection.zoneNamePlaceholder')" class="cu-input cu-input--zone-name" />
        <input type="number" v-model.number="newZone.x" placeholder="X" class="cu-input cu-input--zone-coord" />
        <input type="number" v-model.number="newZone.y" placeholder="Y" class="cu-input cu-input--zone-coord" />
        <input type="number" v-model.number="newZone.w" placeholder="W" class="cu-input cu-input--zone-coord" />
        <input type="number" v-model.number="newZone.h" placeholder="H" class="cu-input cu-input--zone-coord" />
        <el-button size="small" @click="addZone">{{ t("common.add") }}</el-button>
      </div>
    </div>

    <!-- 录制模式 -->
    <div class="cu-field">
      <label class="cu-label">
        <input type="checkbox" v-model="draft.recordingEnabled" />
        <span>{{ t("computerUseSection.recordingEnabled") }}</span>
      </label>
      <span class="cu-field-hint">{{ t("computerUseSection.recordingEnabledHint") }}</span>
    </div>

    <!-- 保存按钮 -->
    <div class="cu-actions">
      <el-button type="primary" :disabled="!isDirty" :loading="computerUseState.saving" @click="handleSave">
        {{ t("common.save") }}
      </el-button>
    </div>

    <!-- 录制控制 -->
    <div class="cu-recording">
      <h3 class="cu-subtitle">{{ t("computerUseSection.recordingControl") }}</h3>
      <div class="cu-recording-actions">
        <el-button
          size="small"
          type="success"
          :disabled="computerUseState.recording || !computerUseConfig.enabled"
          @click="handleStartRecording"
        >{{ t("computerUseSection.startRecording") }}</el-button>
        <el-button
          size="small"
          type="warning"
          :disabled="!computerUseState.recording"
          @click="handleStopRecording"
        >{{ t("computerUseSection.stopRecording") }}</el-button>
        <span v-if="computerUseState.recording" class="cu-recording-active">{{ t("computerUseSection.recordingActive") }}</span>
      </div>
    </div>

    <!-- 操作日志审计 -->
    <div class="cu-audit">
      <div class="cu-audit-header">
        <h3 class="cu-subtitle">{{ t("computerUseSection.auditLog") }}</h3>
        <el-button size="small" text @click="refreshAuditLog(50)">{{ t("common.refresh") }}</el-button>
      </div>
      <div v-if="computerUseState.auditLog.length === 0" class="cu-empty">
        {{ t("computerUseSection.auditEmpty") }}
      </div>
      <div v-else class="cu-audit-table">
        <div class="cu-audit-row cu-audit-row--header">
          <span class="cu-audit-cell cu-audit-cell--time">{{ t("computerUseSection.auditTime") }}</span>
          <span class="cu-audit-cell cu-audit-cell--action">{{ t("computerUseSection.auditAction") }}</span>
          <span class="cu-audit-cell cu-audit-cell--args">{{ t("computerUseSection.auditArgs") }}</span>
          <span class="cu-audit-cell cu-audit-cell--status">{{ t("computerUseSection.auditStatus") }}</span>
          <span class="cu-audit-cell cu-audit-cell--confirmed">{{ t("computerUseSection.auditConfirmed") }}</span>
        </div>
        <div v-for="e in computerUseState.auditLog" :key="computerUseItemKey(e)" class="cu-audit-row">
          <span class="cu-audit-cell cu-audit-cell--time">{{ new Date(e.timestamp).toLocaleTimeString() }}</span>
          <span class="cu-audit-cell cu-audit-cell--action"><code>{{ e.action }}</code></span>
          <span class="cu-audit-cell cu-audit-cell--args">{{ e.args }}</span>
          <span class="cu-audit-cell cu-audit-cell--status">
            <span :class="e.success ? 'cu-status cu-status--ok' : 'cu-status cu-status--fail'">
              {{ e.success ? t("computerUseSection.statusOk") : t("computerUseSection.statusFail") }}
            </span>
          </span>
          <span class="cu-audit-cell cu-audit-cell--confirmed">
            <span :class="e.confirmedByUser ? 'cu-confirmed cu-confirmed--yes' : 'cu-confirmed cu-confirmed--no'">
              {{ e.confirmedByUser ? t("computerUseSection.confirmedYes") : t("computerUseSection.confirmedNo") }}
            </span>
          </span>
        </div>
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

.section-warning {
  font-size: 12px;
  color: var(--color-text-tertiary);
  margin-bottom: 20px;
  padding: 8px 12px;
  background: var(--color-bg-surface-container-low);
  border-radius: var(--radius-sm);
  border-left: 3px solid var(--color-warning, #ff9800);
  line-height: 1.5;
}

.cu-error {
  color: var(--color-error, #f44336);
  font-size: 12px;
  margin-bottom: 16px;
}

.cu-field-group {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 16px;
  margin-bottom: 20px;
}

.cu-field {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin-bottom: 16px;
}

.cu-label {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  color: var(--color-text-primary);
  font-weight: 500;
}

.cu-field-hint {
  font-size: 11px;
  color: var(--color-text-tertiary);
  line-height: 1.4;
}

.cu-input {
  font-family: var(--font-mono);
  font-size: 12px;
  padding: 6px 8px;
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-xs);
  background: var(--color-bg-surface);
  color: var(--color-text-primary);
  width: 100%;
  box-sizing: border-box;
}

.cu-input--narrow {
  width: 120px;
}

.cu-input--textarea {
  font-family: var(--font-mono);
  resize: vertical;
  min-height: 40px;
}

.cu-zone-list {
  margin-bottom: 8px;
}

.cu-zone-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 0;
}

.cu-zone-item code {
  font-family: var(--font-mono);
  font-size: 12px;
  color: var(--color-text-primary);
  background: var(--color-bg-surface-container);
  padding: 1px 6px;
  border-radius: var(--radius-xs);
}

.cu-zone-add {
  display: flex;
  gap: 6px;
  align-items: center;
  flex-wrap: wrap;
}

.cu-input--zone-name {
  width: 140px;
}

.cu-input--zone-coord {
  width: 60px;
}

.cu-actions {
  margin-bottom: 24px;
}

.cu-subtitle {
  font-size: 14px;
  font-weight: 600;
  margin-bottom: 10px;
  color: var(--color-text-primary);
}

.cu-recording {
  margin-bottom: 24px;
  padding: 12px;
  background: var(--color-bg-surface-container-low);
  border-radius: var(--radius-sm);
}

.cu-recording-actions {
  display: flex;
  align-items: center;
  gap: 8px;
}

.cu-recording-active {
  font-size: 12px;
  color: var(--color-error, #f44336);
  font-weight: 500;
}

.cu-audit-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 10px;
}

.cu-empty {
  font-size: 13px;
  color: var(--color-text-tertiary);
  padding: 16px 0;
  text-align: center;
}

.cu-audit-table {
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-sm);
  overflow: hidden;
  max-height: 320px;
  overflow-y: auto;
}

.cu-audit-row {
  display: grid;
  grid-template-columns: 90px 130px 1fr 80px 90px;
  gap: 8px;
  padding: 6px 10px;
  align-items: center;
  border-top: 1px solid var(--color-border-subtle);
  font-size: 12px;
}

.cu-audit-row--header {
  background: var(--color-bg-surface-container);
  font-size: 10px;
  font-weight: 500;
  text-transform: uppercase;
  letter-spacing: 0.5px;
  color: var(--color-text-tertiary);
  border-top: none;
  position: sticky;
  top: 0;
  z-index: 1;
}

.cu-audit-cell {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.cu-audit-cell--args {
  color: var(--color-text-tertiary);
  font-family: var(--font-mono);
}

.cu-audit-cell--action code {
  font-family: var(--font-mono);
  font-size: 11px;
  color: var(--color-text-primary);
  background: var(--color-bg-surface-container);
  padding: 1px 4px;
  border-radius: var(--radius-xs);
}

.cu-status {
  font-size: 10px;
  padding: 1px 5px;
  border-radius: var(--radius-xs);
  text-transform: uppercase;
  font-weight: 500;
}

.cu-status--ok {
  color: var(--color-success, #4caf50);
  background: var(--color-success-container, rgba(76, 175, 80, 0.1));
}

.cu-status--fail {
  color: var(--color-error, #f44336);
  background: var(--color-error-container, rgba(244, 67, 54, 0.1));
}

.cu-confirmed {
  font-size: 10px;
  padding: 1px 5px;
  border-radius: var(--radius-xs);
  text-transform: uppercase;
  font-weight: 500;
}

.cu-confirmed--yes {
  color: var(--color-success, #4caf50);
  background: var(--color-success-container, rgba(76, 175, 80, 0.1));
}

.cu-confirmed--no {
  color: var(--color-text-tertiary);
  background: var(--color-bg-surface-container);
}
</style>
