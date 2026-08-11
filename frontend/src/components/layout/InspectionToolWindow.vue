<script setup lang="ts">
// Koyori IDE 组件 · Inspection Tool Window。
// 喵，这是 Inspection Tool Window，负责 Koyori IDE 的界面呈现喵~
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { Check, Close, Hide, MagicStick, RefreshRight, VideoPause } from "@element-plus/icons-vue";
import { appState } from "@/stores/app";
import { openFileFromPath } from "@/stores/editor";
import {
  applyInspectionQuickFix,
  cancelInspectionQuickFix,
  cancelInspectionRun,
  inspectionState,
  loadInspectionQuickFixes,
  previewInspectionQuickFix,
  runInspections,
  setInspectionSourceEnabled,
  setInspectionWorkspace,
  updateInspectionProfile,
  type InspectionFinding,
  type InspectionSeverity,
} from "@/stores/inspections";
import { useI18n } from "@/lib/i18n";

const props = defineProps<{ repoPath: string }>();
const { t } = useI18n();
const filterText = ref("");
const quickFixKeys = new WeakMap<object, string>();
let quickFixKeySequence = 0;

function quickFixKey(action: object): string {
  const existing = quickFixKeys.get(action);
  if (existing) return existing;
  const key = `inspection-fix-${++quickFixKeySequence}`;
  quickFixKeys.set(action, key);
  return key;
}

const includeGlob = computed({
  get: () => inspectionState.profile.includeGlobs.join(", "),
  set: (value: string) => updateInspectionProfile({ includeGlobs: parseGlobs(value) }),
});
const excludeGlob = computed({
  get: () => inspectionState.profile.excludeGlobs.join(", "),
  set: (value: string) => updateInspectionProfile({ excludeGlobs: parseGlobs(value) }),
});
const visibleFindings = computed(() => {
  const query = filterText.value.trim().toLowerCase();
  if (!query) return inspectionState.findings;
  return inspectionState.findings.filter((finding) => (
    finding.message.toLowerCase().includes(query)
    || finding.path.toLowerCase().includes(query)
    || finding.source.toLowerCase().includes(query)
  ));
});
const findingFiles = computed(() => new Set(inspectionState.findings.map((finding) => finding.path)).size);
const severityCounts = computed(() => inspectionState.findings.reduce(
  (counts, finding) => {
    counts[finding.severity] += 1;
    return counts;
  },
  { 1: 0, 2: 0, 3: 0, 4: 0 } as Record<InspectionSeverity, number>,
));
const mutedSources = computed(() => Object.entries(inspectionState.profile.sourceRules)
  .filter(([, rule]) => rule.enabled === false)
  .map(([source]) => source)
  .sort());

function parseGlobs(value: string): string[] {
  return value.split(",").map((glob) => glob.trim()).filter(Boolean);
}

function updateName(event: Event): void {
  updateInspectionProfile({ name: (event.target as HTMLInputElement).value });
}

function updateSeverity(event: Event): void {
  updateInspectionProfile({
    severityThreshold: Number((event.target as HTMLSelectElement).value) as InspectionSeverity,
  });
}

async function handleRun(): Promise<void> {
  await runInspections(props.repoPath);
}

async function handleFindingClick(finding: InspectionFinding): Promise<void> {
  await openFileFromPath(finding.filePath);
  appState.cursorLine = finding.line + 1;
  appState.cursorColumn = finding.column + 1;
  appState.editorJumpSeq = (appState.editorJumpSeq || 0) + 1;
}

function excerpt(content: string): string {
  const lines = content.split(/\r?\n/);
  const result = lines.slice(0, 24).join("\n");
  return lines.length > 24 ? `${result}\n...` : result;
}

async function handleApplyQuickFix(): Promise<void> {
  await applyInspectionQuickFix();
}

watch(
  () => props.repoPath,
  (root) => setInspectionWorkspace(root),
  { immediate: true },
);
onBeforeUnmount(cancelInspectionRun);
</script>

<template>
  <div class="inspection-tool-window">
    <div class="inspection-tool-window__profile">
      <label class="inspection-tool-window__enabled">
        <input
          :checked="inspectionState.profile.enabled"
          type="checkbox"
          @change="updateInspectionProfile({ enabled: ($event.target as HTMLInputElement).checked })"
        />
        <span>{{ t("inspections.enabled") }}</span>
      </label>
      <input
        :value="inspectionState.profile.name"
        class="inspection-tool-window__profile-name"
        :aria-label="t('inspections.profileName')"
        @change="updateName"
      />
      <button
        v-if="inspectionState.loading"
        type="button"
        class="inspection-tool-window__icon-button"
        :aria-label="t('inspections.stop')"
        :title="t('inspections.stop')"
        @click="cancelInspectionRun"
      >
        <el-icon><VideoPause /></el-icon>
      </button>
      <button
        v-else
        type="button"
        class="inspection-tool-window__icon-button"
        :aria-label="t('inspections.run')"
        :title="t('inspections.run')"
        :disabled="!repoPath || !inspectionState.profile.enabled"
        @click="handleRun"
      >
        <el-icon><RefreshRight /></el-icon>
      </button>
    </div>

    <div class="inspection-tool-window__filters">
      <input
        v-model="includeGlob"
        :placeholder="t('search.includePlaceholder')"
        :aria-label="t('search.includeAria')"
      />
      <input
        v-model="excludeGlob"
        :placeholder="t('search.excludePlaceholder')"
        :aria-label="t('search.excludeAria')"
      />
      <select
        :value="inspectionState.profile.severityThreshold"
        :aria-label="t('inspections.severityThreshold')"
        @change="updateSeverity"
      >
        <option :value="1">{{ t("inspections.errorsOnly") }}</option>
        <option :value="2">{{ t("inspections.warnings") }}</option>
        <option :value="3">{{ t("inspections.information") }}</option>
        <option :value="4">{{ t("inspections.allSeverities") }}</option>
      </select>
    </div>

    <div v-if="mutedSources.length" class="inspection-tool-window__muted">
      <span>{{ t("inspections.mutedSources") }}</span>
      <button
        v-for="source in mutedSources"
        :key="source"
        type="button"
        :title="t('inspections.enableSource', { source })"
        @click="setInspectionSourceEnabled(source, true)"
      >
        {{ source }}
      </button>
    </div>

    <div class="inspection-tool-window__summary">
      <span>{{ t("inspections.summary", { findings: inspectionState.findings.length, files: findingFiles }) }}</span>
      <span class="inspection-tool-window__counts">
        <b class="severity-error">{{ severityCounts[1] }}</b>
        <b class="severity-warning">{{ severityCounts[2] }}</b>
        <b>{{ severityCounts[3] + severityCounts[4] }}</b>
      </span>
      <input v-model="filterText" :placeholder="t('inspections.filter')" />
    </div>

    <div v-if="inspectionState.error" class="inspection-tool-window__state inspection-tool-window__state--error">
      {{ inspectionState.error }}
    </div>
    <div v-if="inspectionState.loading" class="inspection-tool-window__state">
      {{ t("inspections.running") }}
    </div>
    <div
      v-else-if="!inspectionState.findings.length && !inspectionState.error"
      class="inspection-tool-window__state"
    >
      {{ t("inspections.empty") }}
    </div>
    <div v-else class="inspection-tool-window__results">
      <div
        v-for="finding in visibleFindings"
        :key="finding.id"
        class="inspection-tool-window__finding"
        :class="`severity-${finding.severity}`"
      >
        <span class="inspection-tool-window__severity" aria-hidden="true" />
        <button type="button" class="inspection-tool-window__target" @click="handleFindingClick(finding)">
          <span class="inspection-tool-window__message">{{ finding.message }}</span>
          <span class="inspection-tool-window__location">{{ finding.path }}:{{ finding.line + 1 }}:{{ finding.column + 1 }}</span>
        </button>
        <span class="inspection-tool-window__source" :title="finding.source">{{ finding.source }}</span>
        <button
          type="button"
          class="inspection-tool-window__icon-button"
          :aria-label="t('inspections.quickFix')"
          :title="t('inspections.quickFix')"
          @click="loadInspectionQuickFixes(finding.id)"
        >
          <el-icon><MagicStick /></el-icon>
        </button>
        <button
          type="button"
          class="inspection-tool-window__icon-button"
          :aria-label="t('inspections.muteSource', { source: finding.source })"
          :title="t('inspections.muteSource', { source: finding.source })"
          @click="setInspectionSourceEnabled(finding.source, false)"
        >
          <el-icon><Hide /></el-icon>
        </button>
      </div>
    </div>

    <div v-if="inspectionState.quickFixLoading" class="inspection-tool-window__state">
      {{ t("inspections.loadingFixes") }}
    </div>
    <div v-else-if="inspectionState.quickFixes.length" class="inspection-tool-window__actions">
      <span>{{ t("inspections.quickFixes") }}</span>
      <button
        v-for="(action, index) in inspectionState.quickFixes"
        :key="quickFixKey(action)"
        type="button"
        @click="previewInspectionQuickFix(action.findingId, index)"
      >
        {{ action.title }}
      </button>
    </div>

    <div v-if="inspectionState.quickFixPreview" class="inspection-tool-window__preview">
      <div class="inspection-tool-window__preview-heading">
        <span>{{ inspectionState.quickFixPreview.title }}</span>
        <button
          type="button"
          class="inspection-tool-window__icon-button"
          :disabled="inspectionState.applying"
          :aria-label="t('inspections.applyFix')"
          :title="t('inspections.applyFix')"
          @click="handleApplyQuickFix"
        >
          <el-icon><Check /></el-icon>
        </button>
        <button
          type="button"
          class="inspection-tool-window__icon-button"
          :disabled="inspectionState.applying"
          :aria-label="t('search.cancelPreview')"
          :title="t('search.cancelPreview')"
          @click="cancelInspectionQuickFix"
        >
          <el-icon><Close /></el-icon>
        </button>
      </div>
      <div class="inspection-tool-window__diff">
        <pre>{{ excerpt(inspectionState.quickFixPreview.originalContent) }}</pre>
        <pre>{{ excerpt(inspectionState.quickFixPreview.modifiedContent) }}</pre>
      </div>
    </div>
  </div>
</template>

<style scoped>
.inspection-tool-window {
  display: flex;
  width: 100%;
  min-width: 0;
  min-height: 0;
  flex: 1;
  flex-direction: column;
  color: var(--color-text-primary);
  font-size: 11px;
}

.inspection-tool-window__profile,
.inspection-tool-window__filters,
.inspection-tool-window__summary,
.inspection-tool-window__preview-heading {
  display: flex;
  align-items: center;
  gap: 5px;
  padding: 6px 8px;
  border-bottom: 1px solid var(--color-border-subtle);
}

.inspection-tool-window__enabled {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  gap: 4px;
}

.inspection-tool-window__profile-name,
.inspection-tool-window__filters input,
.inspection-tool-window__filters select,
.inspection-tool-window__summary input {
  min-width: 0;
  height: 26px;
  border: 1px solid var(--color-border-subtle);
  border-radius: var(--radius-xs);
  outline: none;
  background: var(--color-bg-surface);
  color: var(--color-text-primary);
  font-size: 11px;
}

.inspection-tool-window__profile-name {
  flex: 1;
  padding: 0 7px;
}

.inspection-tool-window__filters {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr) auto;
}

.inspection-tool-window__filters input,
.inspection-tool-window__filters select,
.inspection-tool-window__summary input {
  padding: 0 6px;
}

.inspection-tool-window__summary > span:first-child {
  flex: 1;
  color: var(--color-text-secondary);
}

.inspection-tool-window__muted {
  display: flex;
  align-items: center;
  gap: 5px;
  min-width: 0;
  padding: 4px 8px;
  overflow-x: auto;
  border-bottom: 1px solid var(--color-border-subtle);
  color: var(--color-text-tertiary);
}

.inspection-tool-window__muted button {
  flex: 0 0 auto;
  padding: 2px 6px;
  border: 1px solid var(--color-border-subtle);
  border-radius: var(--radius-xs);
  background: transparent;
  color: var(--color-text-secondary);
  cursor: pointer;
  font-size: 10px;
}

.inspection-tool-window__summary input {
  width: min(180px, 32%);
}

.inspection-tool-window__counts {
  display: inline-flex;
  gap: 5px;
  color: var(--color-text-tertiary);
}

.inspection-tool-window__counts b {
  min-width: 18px;
  text-align: right;
}

.inspection-tool-window__icon-button {
  display: inline-flex;
  width: 26px;
  height: 26px;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  padding: 0;
  border: 0;
  border-radius: var(--radius-xs);
  background: transparent;
  color: var(--color-text-secondary);
  cursor: pointer;
}

.inspection-tool-window__icon-button:hover {
  background: var(--color-bg-surface-container-low);
  color: var(--color-text-primary);
}

.inspection-tool-window__icon-button:disabled {
  cursor: not-allowed;
  opacity: 0.5;
}

.inspection-tool-window__state {
  padding: 10px;
  color: var(--color-text-tertiary);
}

.inspection-tool-window__state--error {
  color: var(--color-error);
}

.inspection-tool-window__results {
  min-height: 0;
  flex: 1;
  overflow: auto;
}

.inspection-tool-window__finding {
  display: grid;
  grid-template-columns: 8px minmax(0, 1fr) minmax(50px, auto) 26px 26px;
  align-items: center;
  gap: 4px;
  min-height: 38px;
  padding: 3px 6px;
  border-bottom: 1px solid var(--color-border-subtle);
}

.inspection-tool-window__finding:hover {
  background: var(--color-bg-surface-container-low);
}

.inspection-tool-window__severity {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--color-text-tertiary);
}

.severity-1 .inspection-tool-window__severity {
  background: var(--color-error);
}

.severity-2 .inspection-tool-window__severity {
  background: var(--color-warning);
}

.severity-error {
  color: var(--color-error);
}

.severity-warning {
  color: var(--color-warning);
}

.inspection-tool-window__target {
  display: flex;
  min-width: 0;
  flex-direction: column;
  padding: 0;
  border: 0;
  background: transparent;
  color: inherit;
  cursor: pointer;
  text-align: left;
}

.inspection-tool-window__message,
.inspection-tool-window__location,
.inspection-tool-window__source {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.inspection-tool-window__location,
.inspection-tool-window__source {
  color: var(--color-text-tertiary);
  font-size: 10px;
}

.inspection-tool-window__source {
  max-width: 110px;
}

.inspection-tool-window__actions {
  display: flex;
  flex-wrap: wrap;
  gap: 5px;
  padding: 6px 8px;
  border-top: 1px solid var(--color-border-subtle);
}

.inspection-tool-window__actions span {
  color: var(--color-text-secondary);
}

.inspection-tool-window__actions button {
  min-width: 0;
  padding: 2px 7px;
  overflow: hidden;
  border: 1px solid var(--color-border-subtle);
  border-radius: var(--radius-xs);
  background: transparent;
  color: var(--color-text-primary);
  cursor: pointer;
  font-size: 11px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.inspection-tool-window__preview {
  max-height: min(44vh, 420px);
  overflow: auto;
  border-top: 1px solid var(--color-border-subtle);
}

.inspection-tool-window__preview-heading span {
  min-width: 0;
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.inspection-tool-window__diff {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
  gap: 5px;
  padding: 6px 8px;
}

.inspection-tool-window__diff pre {
  min-width: 0;
  max-height: 180px;
  margin: 0;
  padding: 6px;
  overflow: auto;
  border: 1px solid var(--color-border-subtle);
  border-radius: var(--radius-xs);
  background: var(--color-bg-base);
  color: var(--color-text-primary);
  font-family: var(--font-mono);
  font-size: 10px;
  line-height: 1.35;
  white-space: pre;
}
</style>
