<script setup lang="ts">
// Koyori IDE 组件 · Mcp Section。
// 喵，这是 Mcp Section，负责 Koyori IDE 的界面呈现喵~
/**
 * Plan 11 Task 4 Step 5 - MCP 设置分区。
 * 列出用户配置的 MCP server，支持新增、编辑、删除、启用、连接和断开。
 * 并展示 agent 可用的 MCP 工具（`mcp.<server>.<tool>` 命名空间）。
 * 安全提示（G-SEC-12）：新增 server 默认禁用，需用户显式启用后才能连接。
 */
import { onMounted, computed } from "vue";
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
  editingServer,
  openServerEditor,
  closeServerEditor,
} from "@/stores/mcp";
import type { MCPServerConfig, MCPTransport } from "@/stores/mcp";
import FocusTrapDialog from "@/components/common/FocusTrapDialog.vue";

const { t } = useI18n();

onMounted(async () => {
  await loadMcpServers();
  await refreshAgentMcpTools();
});

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

function riskBadgeClass(risk: string): string {
  if (risk === "dangerous") return "tool-risk-badge--dangerous";
  if (risk === "safe") return "tool-risk-badge--safe";
  return "tool-risk-badge--elevated";
}

function riskLabel(risk: string): string {
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
          <el-button size="small" @click="openServerEditor(srv)">{{ t("mcpSection.edit") }}</el-button>
          <el-button size="small" type="danger" @click="handleDelete(srv.name)">{{ t("common.remove") }}</el-button>
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
        <span v-if="tool.autoApproved" class="auto-badge">{{ t("mcpSection.autoApproved") }}</span>
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
          <label class="mcp-editor__label">{{ t("mcpSection.fieldAutoApprove") }}</label>
          <input
            :value="form.autoApprove?.join(', ') ?? ''"
            type="text"
            class="mcp-editor__input"
            :placeholder="t('mcpSection.fieldAutoApprovePlaceholder')"
            @input="(e) => { form!.autoApprove = (e.target as HTMLInputElement).value.split(',').map((s) => s.trim()).filter(Boolean); }"
          />
        </div>

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
