<script setup lang="ts">
// Koyori IDE 组件 · Agent Section。
// 喵，这是 Agent Section，负责 Koyori IDE 的界面呈现喵~
import { computed } from "vue";
import { appState, saveSettings } from "@/stores/app";
import { getRegisteredTools } from "@/stores/agent";
import type { ApprovalPolicy, ToolApprovalConfig } from "@/types";
import { useI18n } from "@/lib/i18n";

const { t } = useI18n();

// Build the list of tool kinds to show in the UI. We always show the four
// built-in tools (read/write/run/search) even if somehow unregistered, plus
// any additional custom tools from the registry.
const BUILTIN_TOOLS = ["read", "write", "run", "search"];

const toolKinds = computed<string[]>(() => {
  const registered = getRegisteredTools().map((td) => td.kind);
  const seen = new Set<string>();
  const result: string[] = [];
  for (const k of [...BUILTIN_TOOLS, ...registered]) {
    if (!seen.has(k)) {
      seen.add(k);
      result.push(k);
    }
  }
  return result;
});

function policyFor(kind: string): ApprovalPolicy {
  const cfg = appState.toolApprovalConfig[kind];
  if (cfg === "auto-approve" || cfg === "never-approve") return cfg;
  return "always-ask";
}

function setPolicy(kind: string, policy: ApprovalPolicy): void {
  const next: ToolApprovalConfig = { ...appState.toolApprovalConfig };
  if (policy === "always-ask") {
    // "always-ask" is the default — remove the entry rather than storing it,
    // so the config stays compact.
    delete next[kind];
  } else {
    next[kind] = policy;
  }
  appState.toolApprovalConfig = next;
  saveSettings();
}

function toolDescription(kind: string): string {
  const td = getRegisteredTools().find((td) => td.kind === kind);
  return td?.schema.description ?? "";
}

function toolRiskLabel(kind: string): string {
  const td = getRegisteredTools().find((td) => td.kind === kind);
  if (!td?.schema.dangerLevel) return "";
  switch (td.schema.dangerLevel) {
    case "safe": return t("agentSection.riskSafe");
    case "elevated": return t("agentSection.riskElevated");
    case "dangerous": return t("agentSection.riskDangerous");
    default: return "";
  }
}

function toolDangerLevel(kind: string): string {
  const td = getRegisteredTools().find((td) => td.kind === kind);
  return td?.schema.dangerLevel ?? "";
}

const policyOptions = computed<{ value: ApprovalPolicy; label: string }[]>(() => [
  { value: "always-ask", label: t("agentSection.policyAlwaysAsk") },
  { value: "auto-approve", label: t("agentSection.policyAutoApprove") },
  { value: "never-approve", label: t("agentSection.policyNeverApprove") },
]);

/** prompt-5 Task E: run/write cannot use auto-approve (forced always-ask or never). */
function policyOptionsFor(kind: string): { value: ApprovalPolicy; label: string }[] {
  if (kind === "run" || kind === "write") {
    return policyOptions.value.filter((o) => o.value !== "auto-approve");
  }
  return policyOptions.value;
}

function setPolicySafe(kind: string, policy: ApprovalPolicy): void {
  if ((kind === "run" || kind === "write") && policy === "auto-approve") {
    policy = "always-ask";
  }
  setPolicy(kind, policy);
}
</script>

<template>
  <section class="settings-section">
    <h2 class="section-title">{{ t("settings.agent") }}</h2>
    <p class="section-hint">
      {{ t("agentSection.hintPrefix") }}
      <code>read</code> {{ t("agentSection.hintOr") }} <code>search</code>) {{ t("agentSection.hintSuffix") }}
    </p>
    <p class="section-warning">
      <strong>{{ t("agentSection.warningLabel") }}</strong> {{ t("agentSection.warningPrefix") }} <code>run</code> {{ t("agentSection.warningMiddle") }} <code>write</code> {{ t("agentSection.warningSuffix") }}
    </p>

    <div class="tool-policy-table">
      <div class="tool-policy-header">
        <span class="tool-policy-header__name">{{ t("agentSection.toolHeader") }}</span>
        <span class="tool-policy-header__policy">{{ t("agentSection.approvalPolicyHeader") }}</span>
        <span class="tool-policy-header__risk">{{ t("agentSection.riskHeader") }}</span>
      </div>
      <div
        v-for="kind in toolKinds"
        :key="kind"
        class="tool-policy-row"
      >
        <div class="tool-policy-row__name">
          <code>{{ kind }}</code>
          <span v-if="toolDescription(kind)" class="tool-policy-row__desc">
            — {{ toolDescription(kind) }}
          </span>
        </div>
        <div class="tool-policy-row__policy">
          <el-select
            :model-value="policyFor(kind)"
            size="small"
            style="width: var(--setting-control-width-sm, 160px)"
            :aria-label="t('agentSection.approvalPolicyAria', { kind })"
            @change="(val: ApprovalPolicy) => setPolicySafe(kind, val)"
          >
            <el-option
              v-for="opt in policyOptionsFor(kind)"
              :key="opt.value"
              :label="opt.label"
              :value="opt.value"
            />
          </el-select>
        </div>
        <div class="tool-policy-row__risk">
          <span v-if="toolRiskLabel(kind)" class="tool-risk-badge" :class="`tool-risk-badge--${toolDangerLevel(kind)}`">
            {{ toolRiskLabel(kind) }}
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

.section-warning code {
  font-family: var(--font-mono);
  font-size: 11px;
  background: var(--color-bg-surface-container);
  padding: 1px 4px;
  border-radius: var(--radius-xs);
}

.tool-policy-table {
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-sm);
  overflow: hidden;
}

.tool-policy-header {
  display: grid;
  grid-template-columns: 1fr 180px 80px;
  gap: 12px;
  padding: 8px 12px;
  background: var(--color-bg-surface-container);
  font-size: 11px;
  font-weight: 500;
  text-transform: uppercase;
  letter-spacing: 0.5px;
  color: var(--color-text-tertiary);
}

.tool-policy-row {
  display: grid;
  grid-template-columns: 1fr 180px 80px;
  gap: 12px;
  padding: 10px 12px;
  border-top: 1px solid var(--color-border-subtle);
  align-items: center;
}

.tool-policy-row__name {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.tool-policy-row__name code {
  font-family: var(--font-mono);
  font-size: 13px;
  color: var(--color-text-primary);
  background: var(--color-bg-surface-container);
  padding: 1px 6px;
  border-radius: var(--radius-xs);
  align-self: flex-start;
}

.tool-policy-row__desc {
  font-size: 11px;
  color: var(--color-text-tertiary);
  line-height: 1.4;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.tool-policy-row__risk {
  display: flex;
  justify-content: flex-start;
}

.tool-risk-badge {
  font-size: 10px;
  padding: 2px 6px;
  border-radius: var(--radius-xs);
  text-transform: uppercase;
  letter-spacing: 0.5px;
  font-weight: 500;
}

.tool-risk-badge--safe {
  color: var(--color-success, #4caf50);
  background: var(--color-success-container, rgba(76, 175, 80, 0.1));
}

.tool-risk-badge--elevated {
  color: var(--color-warning, #ff9800);
  background: var(--color-warning-container, rgba(255, 152, 0, 0.1));
}

.tool-risk-badge--dangerous {
  color: var(--color-error, #f44336);
  background: var(--color-error-container, rgba(244, 67, 54, 0.1));
}
</style>
