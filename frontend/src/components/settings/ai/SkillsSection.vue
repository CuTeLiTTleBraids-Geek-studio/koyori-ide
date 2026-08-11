<script setup lang="ts">
// Koyori IDE 组件 · Skills Section。
// 喵，这是 Skills Section，负责 Koyori IDE 的界面呈现喵~
/**
 * Plan 11 Task 5 Step 5 - Skills 设置分区。
 * 列出后端 SkillsService 已加载的技能（项目级 `.koyori-ide/skills/*.yaml` +
 * 用户级 `<configDir>/koyori-ide/skills/*.yaml`），支持：
 *   - 查看技能详情（SystemPrompt / Trigger / AllowedTools / Examples）
 *   - 项目级技能首次激活需用户显式确认（G-SEC-03）：列表展示批准状态
 *     + 「批准」按钮，点击后调用 ActivateSkill
 *   - 重新扫描磁盘加载（Load）
 * 技能的编辑/新增通过手动创建 YAML 文件实现（Out of scope：完整 UI
 * 编辑器推迟到后续 Plan）。此分区聚焦于「发现 + 审批 + 浏览」。
 */
import { onMounted, computed, ref } from "vue";
import { ElMessageBox } from "element-plus";
import { useI18n } from "@/lib/i18n";
import {
  skillsState,
  skillsList,
  loadSkills,
  activateSkill,
  editingSkill,
  openSkillEditor,
  closeSkillEditor,
} from "@/stores/skills";
import type { Skill, SkillScope } from "@/stores/skills";
import FocusTrapDialog from "@/components/common/FocusTrapDialog.vue";

const { t } = useI18n();

onMounted(async () => {
  await loadSkills();
});

const form = computed(() => editingSkill.value);

function scopeLabel(scope: SkillScope): string {
  if (scope === "project") return t("skillsSection.scopeProject");
  if (scope === "user") return t("skillsSection.scopeUser");
  return t("skillsSection.scopeGlobal");
}

function scopeBadgeClass(scope: SkillScope): string {
  if (scope === "project") return "scope-badge--project";
  if (scope === "user") return "scope-badge--user";
  return "scope-badge--global";
}

function triggerSummary(sk: Skill): string {
  const parts: string[] = [];
  if (sk.trigger.keywords?.length) {
    parts.push(`${t("skillsSection.triggerKeywords")}: ${sk.trigger.keywords.join(", ")}`);
  }
  if (sk.trigger.regex) {
    parts.push(`${t("skillsSection.triggerRegex")}: /${sk.trigger.regex}/`);
  }
  if (sk.trigger.manual) {
    parts.push(t("skillsSection.triggerManual"));
  }
  return parts.length > 0 ? parts.join("  |  ") : t("skillsSection.triggerNone");
}

function needsApproval(sk: Skill): boolean {
  return sk.scope === "project";
}

/** G-SEC-03：批准项目级技能。 */
async function handleApprove(sk: Skill): Promise<void> {
  try {
    await ElMessageBox.confirm(
      t("skillsSection.approveConfirm", { name: sk.name }),
      t("common.confirm"),
      { type: "warning", confirmButtonText: t("common.confirm"), cancelButtonText: t("common.cancel") },
    );
  } catch {
    return;
  }
  await activateSkill(sk.id);
}

function isApproved(sk: Skill): boolean {
  // G-SEC-03: project-scope skills need explicit approval tracking in the UI.
  if (!needsApproval(sk)) return true;
  return approvedSet.value.has(sk.id);
}

// Frontend-only approved id set for UI feedback (backend remains authoritative).
const approvedSet = ref<Set<string>>(new Set());

async function handleApproveAndTrack(sk: Skill): Promise<void> {
  await handleApprove(sk);
  if (!skillsState.error) {
    approvedSet.value.add(sk.id);
  }
}

function openDetail(sk: Skill): void {
  openSkillEditor(sk);
}
</script>

<template>
  <section class="settings-section">
    <h2 class="section-title">{{ t("settings.skills") }}</h2>
    <p class="section-hint">{{ t("skillsSection.hint") }}</p>
    <p class="section-warning">
      <strong>{{ t("skillsSection.warningLabel") }}</strong> {{ t("skillsSection.warning") }}
    </p>

    <div class="skills-toolbar">
      <el-button size="small" type="primary" :loading="skillsState.loading" @click="loadSkills">
        {{ t("skillsSection.reload") }}
      </el-button>
      <span v-if="skillsState.error" class="skills-error">{{ skillsState.error }}</span>
    </div>

    <div v-if="skillsList.length === 0 && !skillsState.loading" class="skills-empty">
      {{ t("skillsSection.empty") }}
    </div>

    <div class="skills-table">
      <div class="skills-row skills-row--header">
        <span class="skills-cell skills-cell--name">{{ t("skillsSection.nameHeader") }}</span>
        <span class="skills-cell skills-cell--scope">{{ t("skillsSection.scopeHeader") }}</span>
        <span class="skills-cell skills-cell--priority">{{ t("skillsSection.priorityHeader") }}</span>
        <span class="skills-cell skills-cell--trigger">{{ t("skillsSection.triggerHeader") }}</span>
        <span class="skills-cell skills-cell--approved">{{ t("skillsSection.approvedHeader") }}</span>
        <span class="skills-cell skills-cell--actions">{{ t("skillsSection.actionsHeader") }}</span>
      </div>
      <div
        v-for="sk in skillsList"
        :key="sk.id"
        class="skills-row"
      >
        <div class="skills-cell skills-cell--name">
          <code>{{ sk.id }}</code>
          <span class="skills-cell__sub">{{ sk.name }}</span>
        </div>
        <div class="skills-cell skills-cell--scope">
          <span class="scope-badge" :class="scopeBadgeClass(sk.scope)">
            {{ scopeLabel(sk.scope) }}
          </span>
        </div>
        <div class="skills-cell skills-cell--priority">
          <span class="priority-badge">{{ sk.priority }}</span>
        </div>
        <div class="skills-cell skills-cell--trigger">
          <span class="trigger-text">{{ triggerSummary(sk) }}</span>
        </div>
        <div class="skills-cell skills-cell--approved">
          <span v-if="isApproved(sk)" class="approved-badge approved-badge--yes">
            {{ t("skillsSection.approvedYes") }}
          </span>
          <span v-else class="approved-badge approved-badge--pending">
            {{ t("skillsSection.approvedPending") }}
          </span>
        </div>
        <div class="skills-cell skills-cell--actions">
          <el-button
            v-if="needsApproval(sk) && !isApproved(sk)"
            size="small"
            type="primary"
            @click="handleApproveAndTrack(sk)"
          >{{ t("skillsSection.approve") }}</el-button>
          <el-button size="small" @click="openDetail(sk)">{{ t("skillsSection.details") }}</el-button>
        </div>
      </div>
    </div>

    <!-- 详情对话框 -->
    <div
      v-if="form"
      class="skills-editor-overlay"
    >
      <button
        type="button"
        class="dialog-backdrop-button"
        tabindex="-1"
        :aria-label="t('a11y.closeDialog')"
        @click="closeSkillEditor"
      />
      <FocusTrapDialog
        class="skills-editor"
        :aria-label="form.name"
        @close="closeSkillEditor"
      >
        <h3 class="skills-editor__title">{{ form.name }}</h3>
        <p class="skills-editor__id"><code>{{ form.id }}</code></p>
        <p v-if="form.description" class="skills-editor__desc">{{ form.description }}</p>

        <div class="skills-editor__row">
          <label class="skills-editor__label">{{ t("skillsSection.fieldScope") }}</label>
          <span class="scope-badge" :class="scopeBadgeClass(form.scope)">
            {{ scopeLabel(form.scope) }}
          </span>
        </div>
        <div class="skills-editor__row">
          <label class="skills-editor__label">{{ t("skillsSection.fieldPriority") }}</label>
          <span>{{ form.priority }}</span>
        </div>
        <div class="skills-editor__row">
          <label class="skills-editor__label">{{ t("skillsSection.fieldTrigger") }}</label>
          <span class="trigger-text">{{ triggerSummary(form) }}</span>
        </div>
        <div v-if="form.allowedTools?.length" class="skills-editor__row">
          <label class="skills-editor__label">{{ t("skillsSection.fieldAllowedTools") }}</label>
          <div class="chip-list">
            <code v-for="tool in form.allowedTools" :key="tool" class="chip">{{ tool }}</code>
          </div>
        </div>
        <div v-if="form.allowedMcp?.length" class="skills-editor__row">
          <label class="skills-editor__label">{{ t("skillsSection.fieldAllowedMcp") }}</label>
          <div class="chip-list">
            <code v-for="m in form.allowedMcp" :key="m" class="chip chip--mcp">{{ m }}</code>
          </div>
        </div>
        <div v-if="form.examples?.length" class="skills-editor__row">
          <label class="skills-editor__label">{{ t("skillsSection.fieldExamples") }}</label>
          <ul class="example-list">
            <li v-for="ex in form.examples" :key="ex">{{ ex }}</li>
          </ul>
        </div>
        <div class="skills-editor__row skills-editor__row--prompt">
          <label class="skills-editor__label">{{ t("skillsSection.fieldSystemPrompt") }}</label>
          <pre class="prompt-text">{{ form.systemPrompt }}</pre>
        </div>
        <div v-if="form.filePath" class="skills-editor__row">
          <label class="skills-editor__label">{{ t("skillsSection.fieldFilePath") }}</label>
          <code class="filepath">{{ form.filePath }}</code>
        </div>

        <div class="skills-editor__actions">
          <el-button size="small" @click="closeSkillEditor">{{ t("common.close") }}</el-button>
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

.skills-toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 16px;
}

.skills-error {
  color: var(--color-error, #f44336);
  font-size: 12px;
  margin-left: 8px;
}

.skills-empty {
  font-size: 13px;
  color: var(--color-text-tertiary);
  padding: 24px 0;
  text-align: center;
}

.skills-table {
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-sm);
  overflow: hidden;
  margin-bottom: 24px;
}

.skills-row {
  display: grid;
  grid-template-columns: 1.4fr 90px 70px 1.6fr 100px 1fr;
  gap: 12px;
  padding: 10px 12px;
  align-items: center;
  border-top: 1px solid var(--color-border-subtle);
}

.skills-row--header {
  background: var(--color-bg-surface-container);
  font-size: 11px;
  font-weight: 500;
  text-transform: uppercase;
  letter-spacing: 0.5px;
  color: var(--color-text-tertiary);
  border-top: none;
}

.skills-cell {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.skills-cell--name code {
  font-family: var(--font-mono);
  font-size: 13px;
  color: var(--color-text-primary);
  background: var(--color-bg-surface-container);
  padding: 1px 6px;
  border-radius: var(--radius-xs);
  align-self: flex-start;
}

.skills-cell__sub {
  font-size: 11px;
  color: var(--color-text-tertiary);
  line-height: 1.4;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.skills-cell--actions {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
}

.scope-badge {
  font-size: 10px;
  padding: 2px 6px;
  border-radius: var(--radius-xs);
  text-transform: uppercase;
  letter-spacing: 0.5px;
  font-weight: 500;
  align-self: flex-start;
}

.scope-badge--project {
  color: var(--color-warning, #ff9800);
  background: var(--color-warning-container, rgba(255, 152, 0, 0.1));
}

.scope-badge--user {
  color: var(--color-primary, #2196f3);
  background: var(--color-primary-container, rgba(33, 150, 243, 0.1));
}

.scope-badge--global {
  color: var(--color-text-tertiary);
  background: var(--color-bg-surface-container);
}

.priority-badge {
  font-family: var(--font-mono);
  font-size: 12px;
  color: var(--color-text-secondary);
  background: var(--color-bg-surface-container);
  padding: 1px 6px;
  border-radius: var(--radius-xs);
  align-self: flex-start;
}

.trigger-text {
  font-size: 12px;
  color: var(--color-text-tertiary);
  line-height: 1.4;
  word-break: break-all;
}

.approved-badge {
  font-size: 10px;
  padding: 2px 6px;
  border-radius: var(--radius-xs);
  text-transform: uppercase;
  letter-spacing: 0.5px;
  font-weight: 500;
  align-self: flex-start;
}

.approved-badge--yes {
  color: var(--color-success, #4caf50);
  background: var(--color-success-container, rgba(76, 175, 80, 0.1));
}

.approved-badge--pending {
  color: var(--color-warning, #ff9800);
  background: var(--color-warning-container, rgba(255, 152, 0, 0.1));
}

/* 详情对话框 */
.skills-editor-overlay {
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

.skills-editor {
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

.skills-editor__title {
  font-size: 16px;
  font-weight: 600;
  margin-bottom: 4px;
  color: var(--color-text-primary);
}

.skills-editor__id {
  margin-bottom: 8px;
}

.skills-editor__id code {
  font-family: var(--font-mono);
  font-size: 12px;
  color: var(--color-text-tertiary);
  background: var(--color-bg-surface-container);
  padding: 1px 6px;
  border-radius: var(--radius-xs);
}

.skills-editor__desc {
  font-size: 13px;
  color: var(--color-text-secondary);
  margin-bottom: 16px;
  line-height: 1.5;
}

.skills-editor__row {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  margin-bottom: 14px;
}

.skills-editor__row--prompt {
  flex-direction: column;
  gap: 6px;
}

.skills-editor__label {
  width: 110px;
  flex-shrink: 0;
  font-size: 12px;
  color: var(--color-text-secondary);
  padding-top: 2px;
}

.chip-list {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}

.chip {
  font-family: var(--font-mono);
  font-size: 11px;
  color: var(--color-text-primary);
  background: var(--color-bg-surface-container);
  padding: 1px 6px;
  border-radius: var(--radius-xs);
}

.chip--mcp {
  color: var(--color-primary, #2196f3);
  background: var(--color-primary-container, rgba(33, 150, 243, 0.1));
}

.example-list {
  margin: 0;
  padding-left: 20px;
  font-size: 12px;
  color: var(--color-text-tertiary);
  line-height: 1.6;
}

.prompt-text {
  background: var(--color-bg-surface-container-low);
  border: 1px solid var(--color-border-subtle);
  border-radius: var(--radius-sm);
  padding: 10px 12px;
  font-family: var(--font-mono);
  font-size: 12px;
  color: var(--color-text-primary);
  white-space: pre-wrap;
  word-break: break-word;
  max-height: 240px;
  overflow-y: auto;
  margin: 0;
  width: 100%;
  box-sizing: border-box;
}

.filepath {
  font-family: var(--font-mono);
  font-size: 11px;
  color: var(--color-text-tertiary);
  word-break: break-all;
}

.skills-editor__actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 20px;
}
</style>
