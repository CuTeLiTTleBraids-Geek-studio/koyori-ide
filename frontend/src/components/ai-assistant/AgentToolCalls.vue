<script setup lang="ts">
import { computed } from "vue";
import { Check, Close, Document, EditPen, MagicStick, Search, VideoPlay } from "@element-plus/icons-vue";
import { aiState } from "@/stores/ai";
import {
  agentState,
  approveAndFeed,
  clearPendingToolCalls,
  getRegisteredTools,
  isAgentMode,
  rejectAndFeed,
  toggleWriteHunk,
  type ToolCall,
  type ToolCallKind,
} from "@/stores/agent";
import type { RiskLevel } from "@/types";
import { notifyWarning } from "@/lib/notifications";
import { useI18n } from "@/lib/i18n";

const { t } = useI18n();
const pendingToolCalls = computed(() => agentState.pendingToolCalls);
const hasPendingRunToolCalls = computed(() => pendingToolCalls.value.some((call) => call.kind === "run"));
const agentTurnBusy = computed(() => aiState.streaming || aiState.globalStreamBusy);

const toolDangerLevelMap = computed(() => {
  const levels = new Map<string, RiskLevel>();
  for (const definition of getRegisteredTools()) {
    if (definition.schema.dangerLevel) levels.set(definition.kind, definition.schema.dangerLevel);
  }
  return levels;
});

function toolCallIcon(kind: ToolCallKind) {
  switch (kind) {
    case "read": return Document;
    case "write": return EditPen;
    case "run": return VideoPlay;
    case "search": return Search;
    default: return MagicStick;
  }
}

function effectiveRiskLevel(call: ToolCall): RiskLevel | undefined {
  return call.riskLevel ?? toolDangerLevelMap.value.get(call.kind);
}

function riskBadgeLabel(level: RiskLevel | undefined): string {
  switch (level) {
    case "safe": return t("aiChat.riskSafe");
    case "elevated": return t("aiChat.riskElevated");
    case "dangerous": return t("aiChat.riskDangerous");
    default: return "";
  }
}

function statusLabel(call: ToolCall): string {
  switch (call.status) {
    case "pending": return t("aiChat.statusPending");
    case "approved": return t("aiChat.statusApproved");
    case "rejected": return t("aiChat.statusRejected");
    case "executed": return t("aiChat.statusExecuted");
    case "error": return t("aiChat.statusError");
    default: return call.status;
  }
}

function structuredArguments(call: ToolCall): string {
  try {
    return JSON.stringify(call.arguments ?? {}, null, 2);
  } catch {
    return "{}";
  }
}

function handleAcceptAll(call: ToolCall): void {
  if (call.writeDiff) {
    call.selectedHunks = call.writeDiff.hunks.map((_, index) => index);
  }
  handleApprove(call);
}

function handleApprove(call: ToolCall): void {
  if (agentTurnBusy.value) {
    notifyWarning(t("aiChat.waitForResponse"));
    return;
  }
  void approveAndFeed(call);
}

function handleReject(call: ToolCall): void {
  if (agentTurnBusy.value) {
    notifyWarning(t("aiChat.waitForResponse"));
    return;
  }
  void rejectAndFeed(call);
}

function handleClear(): void {
  if (agentTurnBusy.value) {
    notifyWarning(t("aiChat.waitForResponse"));
    return;
  }
  clearPendingToolCalls();
}
</script>

<template>
  <section
    v-if="isAgentMode && pendingToolCalls.length > 0"
    class="agent-tool-calls"
    data-agent-tool-calls
    aria-live="polite"
    :aria-label="t('aiChat.toolCalls', { count: pendingToolCalls.length })"
  >
    <div class="agent-tool-calls__header">
      <span>{{ t("aiChat.toolCalls", { count: pendingToolCalls.length }) }}</span>
      <button
        type="button"
        class="agent-tool-calls__clear"
        :disabled="agentTurnBusy"
        :aria-label="t('aiChat.clearToolCalls')"
        :title="t('aiChat.clearToolCalls')"
        @click="handleClear"
      >
        <Close :size="13" />
      </button>
    </div>
    <div v-if="hasPendingRunToolCalls" class="agent-tool-calls__warning">
      {{ t("aiChat.denylistWarning") }}
    </div>
    <article
      v-for="call in pendingToolCalls"
      :key="call.id"
      class="agent-tool-call"
      :class="`agent-tool-call--${call.status}`"
      :data-agent-tool-call-id="call.id"
      :data-agent-tool-kind="call.kind"
      :data-agent-tool-status="call.status"
    >
      <header class="agent-tool-call__header">
        <el-icon :size="14" class="agent-tool-call__icon"><component :is="toolCallIcon(call.kind)" /></el-icon>
        <span class="agent-tool-call__kind">{{ call.kind }}</span>
        <code class="agent-tool-call__target" :title="call.target">{{ call.target || call.wireName || call.kind }}</code>
        <span
          v-if="effectiveRiskLevel(call)"
          class="agent-tool-call__risk"
          :class="`agent-tool-call__risk--${effectiveRiskLevel(call)}`"
        >{{ riskBadgeLabel(effectiveRiskLevel(call)) }}</span>
        <span class="agent-tool-call__status">{{ statusLabel(call) }}</span>
      </header>
      <div v-if="call.blockReason" class="agent-tool-call__blocked">
        {{ t("aiChat.blocked", { reason: call.blockReason }) }}
      </div>
      <div v-if="call.kind === 'write' && call.writeDiff" class="agent-tool-call__diff" data-agent-write-diff>
        <div
          v-for="(hunk, hunkIdx) in call.writeDiff.hunks"
          :key="hunkIdx"
          class="agent-tool-call__hunk"
          :data-hunk-index="hunkIdx"
        >
          <label class="agent-tool-call__hunk-head">
            <input
              type="checkbox"
              :checked="(call.selectedHunks ?? []).includes(hunkIdx)"
              @change="toggleWriteHunk(call, hunkIdx)"
            />
            <span>@@ -{{ hunk.oldStart }},{{ hunk.oldCount }} +{{ hunk.newStart }},{{ hunk.newCount }} @@</span>
          </label>
          <pre class="agent-tool-call__hunk-body">{{ hunk.lines.map((line) => (line.type === 'added' ? '+' : line.type === 'removed' ? '-' : ' ') + line.content).join('\n') }}</pre>
        </div>
      </div>
      <pre v-else-if="call.kind === 'write' && call.content" class="agent-tool-call__preview">{{ call.content.length > 600 ? `${call.content.slice(0, 600)}\n…` : call.content }}</pre>
      <pre v-else-if="call.arguments" class="agent-tool-call__preview">{{ structuredArguments(call).length > 600 ? `${structuredArguments(call).slice(0, 600)}\n…` : structuredArguments(call) }}</pre>
      <pre v-if="call.result && (call.status === 'executed' || call.status === 'error')" class="agent-tool-call__result">{{ call.result.length > 800 ? `${call.result.slice(0, 800)}\n…` : call.result }}</pre>
      <div v-if="call.status === 'pending'" class="agent-tool-call__actions">
        <button
          type="button"
          class="agent-tool-call__button agent-tool-call__button--approve"
          data-agent-tool-action="approve"
          :data-agent-tool-call-id="call.id"
          :data-agent-tool-kind="call.kind"
          :disabled="agentTurnBusy || !!call.blockReason"
          @click="call.kind === 'write' ? handleAcceptAll(call) : handleApprove(call)"
        >
          <el-icon :size="12"><Check /></el-icon>
          {{ call.kind === 'write' ? t("aiChat.acceptAll") : t("aiChat.approveAndRun") }}
        </button>
        <button
          v-if="call.kind === 'write'"
          type="button"
          class="agent-tool-call__button agent-tool-call__button--approve"
          data-agent-tool-action="apply-selected"
          :data-agent-tool-call-id="call.id"
          :disabled="agentTurnBusy || !!call.blockReason || !(call.selectedHunks && call.selectedHunks.length)"
          @click="handleApprove(call)"
        >
          {{ t("aiChat.applySelected") }}
        </button>
        <button
          type="button"
          class="agent-tool-call__button agent-tool-call__button--reject"
          data-agent-tool-action="reject"
          :data-agent-tool-call-id="call.id"
          :data-agent-tool-kind="call.kind"
          :disabled="agentTurnBusy"
          @click="handleReject(call)"
        >
          <el-icon :size="12"><Close /></el-icon>
          {{ t("aiChat.reject") }}
        </button>
      </div>
    </article>
  </section>
</template>

<style scoped>
.agent-tool-calls {
  flex: 0 0 auto;
  display: grid;
  gap: 8px;
  max-height: 320px;
  overflow: auto;
  padding: 10px 12px;
  border-top: 1px solid var(--color-border-default, #d9d9df);
  background: var(--color-bg-surface-container-low, rgba(127, 127, 127, 0.06));
}
.agent-tool-calls__header,
.agent-tool-call__header {
  display: flex;
  align-items: center;
  min-width: 0;
  gap: 7px;
}
.agent-tool-calls__header {
  justify-content: space-between;
  color: var(--color-text-primary, #202124);
  font-size: 12px;
  font-weight: 650;
}
.agent-tool-calls__clear {
  display: inline-grid;
  width: 24px;
  height: 24px;
  place-items: center;
  padding: 0;
  border: 0;
  border-radius: 4px;
  color: var(--color-text-secondary, #6b7280);
  background: transparent;
  cursor: pointer;
}
.agent-tool-calls__clear:hover { color: var(--color-text-primary, #202124); background: var(--color-bg-hover, rgba(127, 127, 127, 0.12)); }
.agent-tool-calls__clear:disabled { opacity: 0.45; cursor: not-allowed; }
.agent-tool-calls__warning,
.agent-tool-call__blocked {
  padding: 6px 8px;
  border: 1px solid color-mix(in srgb, var(--color-warning, #c58a18) 40%, transparent);
  border-radius: 4px;
  color: var(--color-warning, #8a5a00);
  font-size: 11px;
}
.agent-tool-call {
  display: grid;
  gap: 7px;
  padding: 9px;
  border: 1px solid var(--color-border-default, #d9d9df);
  border-radius: 6px;
  background: var(--color-bg-elevated, #ffffff);
}
.agent-tool-call--executed { border-color: color-mix(in srgb, var(--color-success, #2f9e68) 45%, var(--color-border-default, #d9d9df)); }
.agent-tool-call--error { border-color: color-mix(in srgb, var(--color-error, #b42318) 45%, var(--color-border-default, #d9d9df)); }
.agent-tool-call__icon { flex: 0 0 auto; color: var(--color-primary, #4f7cff); }
.agent-tool-call__kind { flex: 0 0 auto; color: var(--color-text-primary, #202124); font-size: 11px; font-weight: 650; }
.agent-tool-call__target { min-width: 0; overflow: hidden; color: var(--color-text-secondary, #6b7280); text-overflow: ellipsis; white-space: nowrap; }
.agent-tool-call__risk,
.agent-tool-call__status { flex: 0 0 auto; font-size: 10px; }
.agent-tool-call__risk { padding: 2px 5px; border-radius: 3px; }
.agent-tool-call__risk--safe { color: var(--color-success, #2f9e68); background: color-mix(in srgb, var(--color-success, #2f9e68) 12%, transparent); }
.agent-tool-call__risk--elevated { color: var(--color-warning, #8a5a00); background: color-mix(in srgb, var(--color-warning, #c58a18) 12%, transparent); }
.agent-tool-call__risk--dangerous { color: var(--color-error, #b42318); background: color-mix(in srgb, var(--color-error, #b42318) 12%, transparent); }
.agent-tool-call__status { margin-left: auto; color: var(--color-text-tertiary, #8a8f98); }
.agent-tool-call__preview,
.agent-tool-call__result { max-height: 120px; overflow: auto; margin: 0; padding: 7px; border-radius: 4px; font: 11px/1.4 var(--font-mono, ui-monospace); white-space: pre-wrap; word-break: break-word; }
.agent-tool-call__preview { color: var(--color-text-secondary, #6b7280); background: var(--color-bg-base, #f7f7f8); }
.agent-tool-call__result { color: var(--color-text-primary, #202124); background: color-mix(in srgb, var(--color-success, #2f9e68) 8%, var(--color-bg-base, #f7f7f8)); }
.agent-tool-call__actions { display: flex; flex-wrap: wrap; gap: 7px; }
.agent-tool-call__button { display: inline-flex; align-items: center; gap: 5px; min-height: 28px; padding: 5px 9px; border: 1px solid transparent; border-radius: 4px; font-size: 11px; cursor: pointer; }
.agent-tool-call__button:disabled { cursor: not-allowed; opacity: 0.55; }
.agent-tool-call__button--approve { color: #fff; border-color: var(--color-primary, #4f7cff); background: var(--color-primary, #4f7cff); }
.agent-tool-call__button--reject { color: var(--color-text-primary, #202124); border-color: var(--color-border-strong, #aeb4bf); background: transparent; }
@media (max-width: 640px) {
  .agent-tool-call__header { align-items: flex-start; flex-wrap: wrap; }
  .agent-tool-call__status { margin-left: 0; }
  .agent-tool-call__target { flex: 1 1 100%; order: 5; }
}
</style>
