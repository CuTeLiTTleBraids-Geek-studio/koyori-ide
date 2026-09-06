<script setup lang="ts">
// Koyori IDE 组件 · Mcp Section。
// 喵，这是 Mcp Section，负责 Koyori IDE 的界面呈现喵~
/**
 * Plan 11 Task 4 Step 5 - MCP 设置分区。
 * 列出用户配置的 MCP server，支持新增、编辑、删除、启用、连接和断开。
 * 并展示 agent 可用的 MCP 工具（`mcp.<server>.<tool>` 命名空间）。
 * 安全提示（G-SEC-12）：新增 server 默认禁用，需用户显式启用后才能连接。
 */
import { onMounted, computed, ref } from "vue";
import { ElMessageBox } from "element-plus";
import { useI18n } from "@/lib/i18n";
import {
  mcpState,
  mcpServers,
  agentMcpTools,
  loadMcpServers,
  saveMcpServer,
  deleteMcpServer,
  connectMcpServer,
  disconnectMcpServer,
  toggleMcpServerEnabled,
  refreshAgentMcpTools,
  refreshMcpServerContext,
  injectMcpResourceContext,
  injectMcpPromptContext,
  clearStaleMcpContexts,
  editingServer,
  openServerEditor,
  closeServerEditor,
} from "@/stores/mcp";
import type { McpListStatus, MCPCapabilityState } from "@/stores/mcp";
import type { MCPServerConfig, MCPTransport } from "@/stores/mcp";
import FocusTrapDialog from "@/components/common/FocusTrapDialog.vue";

const { t } = useI18n();

onMounted(async () => {
  await loadMcpServers();
  await refreshAgentMcpTools();
});

// P1-03-F: per-server context (capabilities/resources/prompts) panel.
const expandedServers = ref<Record<string, boolean>>({});
const injectionFeedback = ref<Record<string, string>>({});

function serverContext(name: string) {
  return mcpState.serverContexts[name];
}

function contextStatusKey(status: McpListStatus | undefined): string {
  return `mcpSection.contextStatus${status ?? "unloaded"}`;
}

function contextStatusClass(status: McpListStatus | undefined): string {
  switch (status) {
    case "loaded":
      return "status-badge--connected";
    case "stale":
    case "unsupported":
    case "empty":
      return "status-badge--idle";
    case "error":
      return "status-badge--error";
    default:
      return "status-badge--idle";
  }
}

const capabilityFamilies: Array<{ key: "tools" | "resources" | "prompts" | "sampling" | "elicitation" | "logging"; labelKey: string }> = [
  { key: "tools", labelKey: "mcpSection.capTools" },
  { key: "resources", labelKey: "mcpSection.capResources" },
  { key: "prompts", labelKey: "mcpSection.capPrompts" },
  { key: "sampling", labelKey: "mcpSection.capSampling" },
  { key: "elicitation", labelKey: "mcpSection.capElicitation" },
  { key: "logging", labelKey: "mcpSection.capLogging" },
];

function capabilityStateKey(state: MCPCapabilityState | undefined): string {
  switch (state) {
    case "supported":
      return "mcpSection.capSupported";
    case "missing":
      return "mcpSection.capMissing";
    case "unknown":
      return "mcpSection.capUnknown";
    default:
      return "mcpSection.capUnsupported";
  }
}

async function handleRefreshContext(name: string): Promise<void> {
  await refreshMcpServerContext(name);
}

async function handleInjectResource(server: string, uri: string): Promise<void> {
  const ok = await injectMcpResourceContext(server, uri);
  injectionFeedback.value = {
    ...injectionFeedback.value,
    [server]: ok ? t("mcpSection.injected") : (mcpState.error ?? t("mcpSection.injectionFailed")),
  };
}

async function handleInjectPrompt(server: string, prompt: string): Promise<void> {
  const ok = await injectMcpPromptContext(server, prompt, {});
  injectionFeedback.value = {
    ...injectionFeedback.value,
    [server]: ok ? t("mcpSection.injected") : (mcpState.error ?? t("mcpSection.injectionFailed")),
  };
}

async function toggleContextPanel(name: string): Promise<void> {
  const next = !expandedServers.value[name];
  expandedServers.value = { ...expandedServers.value, [name]: next };
  if (next) {
    const ctx = mcpState.serverContexts[name];
    if (!ctx || ctx.status === "unloaded") {
      await refreshMcpServerContext(name);
    }
  }
}

function handleClearStale(): void {
  clearStaleMcpContexts();
}

const transportOptions: { value: MCPTransport; label: string }[] = [
  { value: "stdio", label: "stdio" },
  { value: "sse", label: "sse" },
  { value: "http", label: "http" },
];

// 编辑表单本地副本，避免直接改 store ref。
const form = computed(() => editingServer.value);

function isStdio(cfg: MCPServerConfig | null): boolean {
  return cfg?.transport === "stdio";
}

/** 提交编辑表单。G-SEC-12：enabled 由表单开关控制（新 server 默认 false）。 */
async function submitForm(): Promise<void> {
  if (!form.value) return;
  if (!form.value.name.trim()) return;
  await saveMcpServer(form.value);
  if (!mcpState.error) {
    closeServerEditor();
  }
}

async function handleDelete(name: string): Promise<void> {
  try {
    await ElMessageBox.confirm(
      t("mcpSection.deleteConfirm", { name }),
      t("common.confirm"),
      { type: "warning", confirmButtonText: t("common.confirm"), cancelButtonText: t("common.cancel") },
    );
  } catch {
    return;
  }
  await deleteMcpServer(name);
}

function riskBadgeClass(risk: string | undefined): string {
  if (risk === "dangerous") return "tool-risk-badge--dangerous";
  if (risk === "safe") return "tool-risk-badge--safe";
  return "tool-risk-badge--elevated";
}

function riskLabel(risk: string | undefined): string {
  if (risk === "dangerous") return t("agentSection.riskDangerous");
  if (risk === "safe") return t("agentSection.riskSafe");
  return t("agentSection.riskElevated");
}

function argsText(cfg: MCPServerConfig): string {
  return cfg.args?.join(" ") ?? "";
}
</script>

<template>
  <section class="settings-section">
    <h2 class="section-title">{{ t("settings.mcp") }}</h2>
    <p class="section-hint">{{ t("mcpSection.hint") }}</p>
    <p class="section-warning">
      <strong>{{ t("mcpSection.warningLabel") }}</strong> {{ t("mcpSection.warning") }}
    </p>

    <div class="mcp-toolbar">
      <el-button size="small" type="primary" @click="openServerEditor()">
        {{ t("mcpSection.addServer") }}
      </el-button>
      <el-button size="small" :loading="mcpState.loading" @click="loadMcpServers">
        {{ t("mcpSection.refresh") }}
      </el-button>
      <el-button size="small" @click="handleClearStale">
        {{ t("mcpSection.clearStale") }}
      </el-button>
      <span v-if="mcpState.error" class="mcp-error">{{ mcpState.error }}</span>
    </div>

    <div v-if="mcpServers.length === 0 && !mcpState.loading" class="mcp-empty">
      {{ t("mcpSection.empty") }}
    </div>

    <div class="mcp-table">
      <div class="mcp-row mcp-row--header">
        <span class="mcp-cell mcp-cell--name">{{ t("mcpSection.nameHeader") }}</span>
        <span class="mcp-cell mcp-cell--transport">{{ t("mcpSection.transportHeader") }}</span>
        <span class="mcp-cell mcp-cell--enabled">{{ t("mcpSection.enabledHeader") }}</span>
        <span class="mcp-cell mcp-cell--status">{{ t("mcpSection.statusHeader") }}</span>
        <span class="mcp-cell mcp-cell--actions">{{ t("mcpSection.actionsHeader") }}</span>
      </div>
      <div
        v-for="srv in mcpServers"
        :key="srv.name"
        class="mcp-row"
      >
        <div class="mcp-cell mcp-cell--name">
          <code>{{ srv.name }}</code>
          <span v-if="isStdio(srv)" class="mcp-cell__sub">{{ srv.command }} {{ argsText(srv) }}</span>
          <span v-else class="mcp-cell__sub">{{ srv.url }}</span>
        </div>
        <div class="mcp-cell mcp-cell--transport">
          <span class="transport-badge">{{ srv.transport }}</span>
        </div>
        <div class="mcp-cell mcp-cell--enabled">
          <el-switch
            :model-value="srv.enabled"
            size="small"
            :aria-label="t('mcpSection.enabledAria', { name: srv.name })"
            @change="(val: boolean) => toggleMcpServerEnabled(srv.name, val)"
          />
        </div>
        <div class="mcp-cell mcp-cell--status">
          <span
            v-if="mcpState.connected[srv.name]"
            class="status-badge status-badge--connected"
          >{{ t("mcpSection.statusConnected") }}</span>
          <span v-else-if="srv.enabled" class="status-badge status-badge--idle">{{ t("mcpSection.statusIdle") }}</span>
          <span v-else class="status-badge status-badge--disabled">{{ t("mcpSection.statusDisabled") }}</span>
        </div>
        <div class="mcp-cell mcp-cell--actions">
          <el-button
            v-if="!mcpState.connected[srv.name]"
            size="small"
            :disabled="!srv.enabled"
            @click="connectMcpServer(srv.name)"
          >{{ t("mcpSection.connect") }}</el-button>
          <el-button
            v-else
            size="small"
            @click="disconnectMcpServer(srv.name)"
          >{{ t("mcpSection.disconnect") }}</el-button>
          <el-button
            v-if="mcpState.connected[srv.name]"
            size="small"
            :aria-expanded="expandedServers[srv.name] === true"
            @click="toggleContextPanel(srv.name)"
          >{{ t("mcpSection.contextButton") }}</el-button>
          <el-button size="small" @click="openServerEditor(srv)">{{ t("mcpSection.edit") }}</el-button>
          <el-button size="small" type="danger" @click="handleDelete(srv.name)">{{ t("common.remove") }}</el-button>
        </div>
        <!-- P1-03-F: capabilities/resources/prompts context panel -->
        <div v-if="expandedServers[srv.name]" class="mcp-context">
          <div class="mcp-context__bar">
            <span class="mcp-context__title">{{ t("mcpSection.contextTitle") }}</span>
            <el-button
              size="small"
              :loading="serverContext(srv.name)?.status === 'loading'"
              @click="handleRefreshContext(srv.name)"
            >{{ t("mcpSection.refreshContext") }}</el-button>
            <span
              class="status-badge"
              :class="contextStatusClass(serverContext(srv.name)?.status)"
            >{{ t(contextStatusKey(serverContext(srv.name)?.status)) }}</span>
            <span v-if="serverContext(srv.name)?.error" class="mcp-error">{{ serverContext(srv.name)?.error }}</span>
            <span v-if="injectionFeedback[srv.name]" class="mcp-context__feedback">{{ injectionFeedback[srv.name] }}</span>
          </div>

          <template v-if="serverContext(srv.name)?.capabilities">
            <div class="mcp-context__caps">
              <span class="mcp-context__meta">
                {{ serverContext(srv.name)!.capabilities!.serverInfo.name }}
                {{ serverContext(srv.name)!.capabilities!.serverInfo.version }}
                · {{ serverContext(srv.name)!.capabilities!.protocolVersion }}
              </span>
              <span
                v-for="family in capabilityFamilies"
                :key="family.key"
                class="mcp-context__cap"
              >
                <b>{{ t(family.labelKey) }}</b>
                {{ t(capabilityStateKey(serverContext(srv.name)!.capabilities!.capabilities[family.key].state)) }}
              </span>
              <span
                v-for="unknownKey in serverContext(srv.name)!.capabilities!.capabilities.unknown ?? []"
                :key="unknownKey"
                class="mcp-context__cap"
              >
                <b>{{ unknownKey }}</b> {{ t("mcpSection.capUnknown") }}
              </span>
            </div>
          </template>

          <h4 class="mcp-context__family">{{ t("mcpSection.resourcesTitle") }}</h4>
          <div v-if="serverContext(srv.name)?.resourcesStatus === 'unsupported'" class="mcp-context__hint">
            {{ t("mcpSection.familyUnsupported", { family: t("mcpSection.resourcesTitle") }) }}
          </div>
          <div v-else-if="serverContext(srv.name)?.resourcesStatus === 'error'" class="mcp-error">
            {{ serverContext(srv.name)?.resourcesError }}
          </div>
          <div v-else-if="serverContext(srv.name)?.resourcesStatus === 'empty'" class="mcp-context__hint">
            {{ t("mcpSection.familyEmpty") }}
          </div>
          <div
            v-for="res in serverContext(srv.name)?.resources ?? []"
            :key="res.uri"
            class="mcp-context__item"
          >
            <code class="mcp-context__uri" :title="res.uri">{{ res.uri }}</code>
            <span class="mcp-cell__sub" :title="res.description ?? res.name">
              {{ res.name }}<template v-if="res.mimeType"> · {{ res.mimeType }}</template>
            </span>
            <el-button
              size="small"
              :aria-label="t('mcpSection.injectAria', { source: res.uri })"
              @click="handleInjectResource(srv.name, res.uri)"
            >{{ t("mcpSection.inject") }}</el-button>
          </div>

          <h4 class="mcp-context__family">{{ t("mcpSection.promptsTitle") }}</h4>
          <div v-if="serverContext(srv.name)?.promptsStatus === 'unsupported'" class="mcp-context__hint">
            {{ t("mcpSection.familyUnsupported", { family: t("mcpSection.promptsTitle") }) }}
          </div>
          <div v-else-if="serverContext(srv.name)?.promptsStatus === 'error'" class="mcp-error">
            {{ serverContext(srv.name)?.promptsError }}
          </div>
          <div v-else-if="serverContext(srv.name)?.promptsStatus === 'empty'" class="mcp-context__hint">
            {{ t("mcpSection.familyEmpty") }}
          </div>
          <div
            v-for="prompt in serverContext(srv.name)?.prompts ?? []"
            :key="prompt.name"
            class="mcp-context__item"
          >
            <code class="mcp-context__uri" :title="prompt.name">{{ prompt.name }}</code>
            <span class="mcp-cell__sub" :title="prompt.description ?? ''">{{ prompt.description }}</span>
            <el-button
              size="small"
              :aria-label="t('mcpSection.injectAria', { source: prompt.name })"
              @click="handleInjectPrompt(srv.name, prompt.name)"
            >{{ t("mcpSection.inject") }}</el-button>
          </div>
        </div>
      </div>
    </div>

    <!-- Agent 可用工具 -->
    <h3 class="mcp-subtitle">{{ t("mcpSection.toolsSubtitle") }}</h3>
    <div v-if="agentMcpTools.length === 0" class="mcp-empty">
      {{ t("mcpSection.noTools") }}
    </div>
    <div v-else class="mcp-tools">
      <div v-for="tool in agentMcpTools" :key="tool.namespace" class="mcp-tool">
        <code class="mcp-tool__ns">{{ tool.namespace }}</code>
        <span v-if="tool.description" class="mcp-tool__desc">{{ tool.description }}</span>
        <span class="tool-risk-badge" :class="riskBadgeClass(tool.riskLevel)">
          {{ riskLabel(tool.riskLevel) }}
        </span>
      </div>
    </div>

    <!-- 编辑对话框 -->
    <div
      v-if="form"
      class="mcp-editor-overlay"
    >
      <button
        type="button"
        class="dialog-backdrop-button"
        tabindex="-1"
        :aria-label="t('a11y.closeDialog')"
        @click="closeServerEditor"
      />
      <FocusTrapDialog
        class="mcp-editor"
        :aria-label="t('mcpSection.editorTitle')"
        @close="closeServerEditor"
      >
        <h3 class="mcp-editor__title">{{ t("mcpSection.editorTitle") }}</h3>
        <div class="mcp-editor__row">
          <label class="mcp-editor__label">{{ t("mcpSection.fieldName") }}</label>
          <input
            v-model="form.name"
            type="text"
            class="mcp-editor__input"
            :placeholder="t('mcpSection.fieldNamePlaceholder')"
          />
        </div>
        <div class="mcp-editor__row">
          <label class="mcp-editor__label">{{ t("mcpSection.fieldTransport") }}</label>
          <el-select v-model="form.transport" size="small" style="width: 160px">
            <el-option
              v-for="opt in transportOptions"
              :key="opt.value"
              :label="opt.label"
              :value="opt.value"
            />
          </el-select>
        </div>

        <template v-if="isStdio(form)">
          <div class="mcp-editor__row">
            <label class="mcp-editor__label">{{ t("mcpSection.fieldCommand") }}</label>
            <input
              v-model="form.command"
              type="text"
              class="mcp-editor__input"
              placeholder="npx -y @modelcontextprotocol/server-filesystem"
            />
          </div>
          <div class="mcp-editor__row">
            <label class="mcp-editor__label">{{ t("mcpSection.fieldArgs") }}</label>
            <input
              :value="form.args?.join(' ') ?? ''"
              type="text"
              class="mcp-editor__input"
              :placeholder="t('mcpSection.fieldArgsPlaceholder')"
              @input="(e) => { form!.args = (e.target as HTMLInputElement).value.split(/\s+/).filter(Boolean); }"
            />
          </div>
        </template>
        <template v-else>
          <div class="mcp-editor__row">
            <label class="mcp-editor__label">{{ t("mcpSection.fieldUrl") }}</label>
            <input
              v-model="form.url"
              type="text"
              class="mcp-editor__input"
              placeholder="https://example.com/mcp"
            />
          </div>
        </template>


        <div class="mcp-editor__row">
          <label class="mcp-editor__label">{{ t("mcpSection.fieldEnabled") }}</label>
          <el-switch v-model="form.enabled" size="small" />
          <span class="mcp-editor__hint">{{ t("mcpSection.enabledHint") }}</span>
        </div>

        <div class="mcp-editor__actions">
          <el-button size="small" @click="closeServerEditor">{{ t("common.cancel") }}</el-button>
          <el-button size="small" type="primary" :disabled="!form.name.trim()" @click="submitForm">
            {{ t("common.save") }}
          </el-button>
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

.mcp-toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 16px;
}

.mcp-error {
  color: var(--color-error, #f44336);
  font-size: 12px;
  margin-left: 8px;
}

.mcp-empty {
  font-size: 13px;
  color: var(--color-text-tertiary);
  padding: 24px 0;
  text-align: center;
}

.mcp-table {
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-sm);
  overflow: hidden;
  margin-bottom: 24px;
}

.mcp-row {
  display: grid;
  grid-template-columns: 1.5fr 90px 80px 110px 1fr;
  gap: 12px;
  padding: 10px 12px;
  align-items: center;
  border-top: 1px solid var(--color-border-subtle);
}

.mcp-row--header {
  background: var(--color-bg-surface-container);
  font-size: 11px;
  font-weight: 500;
  text-transform: uppercase;
  letter-spacing: 0.5px;
  color: var(--color-text-tertiary);
  border-top: none;
}

.mcp-cell {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.mcp-cell--name code {
  font-family: var(--font-mono);
  font-size: 13px;
  color: var(--color-text-primary);
  background: var(--color-bg-surface-container);
  padding: 1px 6px;
  border-radius: var(--radius-xs);
  align-self: flex-start;
}

.mcp-cell__sub {
  font-size: 11px;
  color: var(--color-text-tertiary);
  line-height: 1.4;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.mcp-cell--actions {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
}

.transport-badge {
  font-size: 11px;
  font-family: var(--font-mono);
  color: var(--color-text-secondary);
  background: var(--color-bg-surface-container);
  padding: 1px 6px;
  border-radius: var(--radius-xs);
}

.status-badge {
  font-size: 10px;
  padding: 2px 6px;
  border-radius: var(--radius-xs);
  text-transform: uppercase;
  letter-spacing: 0.5px;
  font-weight: 500;
  align-self: flex-start;
}

.status-badge--connected {
  color: var(--color-success, #4caf50);
  background: var(--color-success-container, rgba(76, 175, 80, 0.1));
}

.status-badge--idle {
  color: var(--color-text-tertiary);
  background: var(--color-bg-surface-container);
}

.status-badge--disabled {
  color: var(--color-text-tertiary);
  background: var(--color-bg-surface-container-low);
}

.status-badge--error {
  color: var(--color-error, #f44336);
  background: var(--color-error-container, rgba(244, 67, 54, 0.1));
}

/* P1-03-F: per-server context panel (capabilities/resources/prompts). */
.mcp-context {
  grid-column: 1 / -1;
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 10px 12px;
  background: var(--color-bg-surface-container-low);
  border-radius: var(--radius-sm);
}

.mcp-context__bar {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}

.mcp-context__title {
  font-size: 12px;
  font-weight: 600;
  color: var(--color-text-primary);
}

.mcp-context__feedback {
  font-size: 11px;
  color: var(--color-success, #4caf50);
}

.mcp-context__caps {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  font-size: 11px;
  color: var(--color-text-secondary);
}

.mcp-context__cap {
  padding: 1px 6px;
  background: var(--color-bg-surface-container);
  border-radius: var(--radius-xs);
}

.mcp-context__meta {
  font-family: var(--font-mono);
  font-size: 11px;
  color: var(--color-text-tertiary);
}

.mcp-context__family {
  font-size: 12px;
  font-weight: 600;
  margin: 4px 0 0;
  color: var(--color-text-primary);
}

.mcp-context__hint {
  font-size: 11px;
  color: var(--color-text-tertiary);
}

.mcp-context__item {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
}

.mcp-context__item .mcp-cell__sub {
  flex: 1;
  min-width: 0;
}

.mcp-context__uri {
  font-family: var(--font-mono);
  font-size: 12px;
  color: var(--color-text-primary);
  background: var(--color-bg-surface-container);
  padding: 1px 6px;
  border-radius: var(--radius-xs);
  max-width: 40%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.mcp-subtitle {
  font-size: 14px;
  font-weight: 600;
  margin-bottom: 12px;
  color: var(--color-text-primary);
}

.mcp-tools {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.mcp-tool {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 12px;
  background: var(--color-bg-surface-container-low);
  border-radius: var(--radius-sm);
  font-size: 12px;
}

.mcp-tool__ns {
  font-family: var(--font-mono);
  color: var(--color-text-primary);
  background: var(--color-bg-surface-container);
  padding: 1px 6px;
  border-radius: var(--radius-xs);
}

.mcp-tool__desc {
  color: var(--color-text-tertiary);
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
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

.auto-badge {
  font-size: 10px;
  padding: 2px 6px;
  border-radius: var(--radius-xs);
  color: var(--color-primary, #2196f3);
  background: var(--color-primary-container, rgba(33, 150, 243, 0.1));
}

/* 编辑对话框 */
.mcp-editor-overlay {
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

.mcp-editor {
  position: relative;
  z-index: 1;
  background: var(--color-bg-surface);
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-md);
  padding: 24px;
  width: 480px;
  max-width: 90vw;
  max-height: 90vh;
  overflow-y: auto;
}

.mcp-editor__title {
  font-size: 16px;
  font-weight: 600;
  margin-bottom: 16px;
  color: var(--color-text-primary);
}

.mcp-editor__row {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 14px;
}

.mcp-editor__label {
  width: 110px;
  flex-shrink: 0;
  font-size: 12px;
  color: var(--color-text-secondary);
}

.mcp-editor__input {
  flex: 1;
  padding: 6px 10px;
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-sm);
  background: var(--color-bg-input, var(--color-bg-surface-container));
  color: var(--color-text-primary);
  font-family: var(--font-sans);
  font-size: 13px;
  outline: none;
}

.mcp-editor__input:focus {
  border-color: var(--color-primary, #2196f3);
}

.mcp-editor__hint {
  font-size: 11px;
  color: var(--color-text-tertiary);
}

.mcp-editor__actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 20px;
}
</style>
