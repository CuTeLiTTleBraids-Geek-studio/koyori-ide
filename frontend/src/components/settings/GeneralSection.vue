<script setup lang="ts">
// Koyori IDE 组件 · General Section；交互服务：文件系统（FileService）。
// 喵，这是 General Section，负责 Koyori IDE 的界面呈现喵~
import { ref } from "vue";
import { appState, saveSettings } from "@/stores/app";
import { fileService, logLevelService } from "@/api/services";
import { Folder, Document, Warning } from "@element-plus/icons-vue";
import { useI18n } from "@/lib/i18n";
import { isProductionSandboxRequired } from "@/lib/pluginRegistry";
import { ElMessageBox } from "element-plus";
// Priority 10: 崩溃报告查看器（list + view detail + delete）。
import {
  crashState,
  updateState,
  canDownloadUpdate,
  checkForUpdates,
  downloadVerifiedUpdate,
  loadCrashReports,
  viewCrashReport,
  deleteCrashReport,
  clearAllCrashReports,
} from "@/stores/updateCrash";

const { t } = useI18n();
const pluginSandboxForced = isProductionSandboxRequired();
const pluginSandboxHintKey = pluginSandboxForced
  ? "general.pluginSandboxForcedHint"
  : "general.pluginSandboxHint";

async function handleBrowseFolder() {
  try {
    const path = await fileService.pickDirectory();
    if (path) {
      appState.dataFolderPath = path;
      saveSettings();
    }
  } catch (e) {
    console.error("Failed to pick directory:", e);
  }
}

async function handleDownloadUpdate() {
  const directory = await fileService.pickDirectory();
  if (directory) {
    await downloadVerifiedUpdate(directory);
  }
}

// --- Application log viewer (N-11) ---
const logPath = ref<string>("");
const logContent = ref<string>("");
const logModalVisible = ref(false);
const logLoading = ref(false);
const logError = ref<string>("");

async function loadLogPath() {
  try {
    logPath.value = await logLevelService.getLogPath();
  } catch (e) {
    console.error("Failed to get log path:", e);
    logPath.value = "";
  }
}

async function handleViewLog() {
  logModalVisible.value = true;
  logLoading.value = true;
  logError.value = "";
  logContent.value = "";
  try {
    if (!logPath.value) {
      await loadLogPath();
    }
    // 64 KiB tail is plenty for in-app inspection.
    logContent.value = await logLevelService.readLog(64 * 1024);
  } catch (e: unknown) {
    const msg = e instanceof Error ? e.message : String(e);
    logError.value = msg;
  } finally {
    logLoading.value = false;
  }
}

// Load the path lazily when the section is first rendered so the UI can
// display it next to the View Log button.
loadLogPath();

// --- Crash report viewer (Priority 10) ---
const crashModalVisible = ref(false);
const crashDetailVisible = ref(false);

// 打开崩溃报告列表对话框并加载列表。
async function handleViewCrashReports() {
  crashModalVisible.value = true;
  await loadCrashReports();
}

// 查看单条崩溃报告详情：读取后弹出详情对话框。
async function handleViewCrashDetail(filename: string) {
  await viewCrashReport(filename);
  if (crashState.selected) {
    crashDetailVisible.value = true;
  }
}

// 删除单条崩溃报告（带确认）。
async function handleDeleteCrash(filename: string) {
  try {
    await ElMessageBox.confirm(t("crashViewer.confirmDelete"), t("crashViewer.delete"), {
      type: "warning",
    });
  } catch {
    return; // 用户取消
  }
  await deleteCrashReport(filename);
}

// 清空所有崩溃报告（带确认）。
async function handleClearAllCrash() {
  try {
    await ElMessageBox.confirm(t("crashViewer.confirmClearAll"), t("crashViewer.clearAll"), {
      type: "warning",
    });
  } catch {
    return; // 用户取消
  }
  await clearAllCrashReports();
}

// 格式化字节数为人类可读字符串。
function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}

// 格式化 ISO 时间戳为本地可读字符串。
function formatTime(ts: string): string {
  if (!ts) return "";
  const d = new Date(ts);
  if (isNaN(d.getTime())) return ts;
  return d.toLocaleString();
}
</script>

<template>
  <section class="settings-section">
    <h2 class="section-title">{{ t("settings.general") }}</h2>

    <div class="setting-row">
      <label class="setting-label">{{ t("general.language") }}</label>
      <div class="setting-control">
        <el-select
          v-model="appState.language"
          size="default"
          style="width: var(--setting-control-width-sm)"
          :aria-label="t('general.language')"
          @change="saveSettings"
        >
          <el-option label="English" value="en" />
          <el-option label="中文" value="zh" />
          <el-option label="日本語" value="ja" />
        </el-select>
      </div>
    </div>

    <div class="setting-row">
      <label class="setting-label">{{ t("general.autoUpdate") }}</label>
      <div class="setting-control update-control">
        <el-button :loading="updateState.checking" @click="checkForUpdates()">
          {{ t("mainLayout.commandCheckUpdates") }}
        </el-button>
        <el-button
          v-if="updateState.info?.hasUpdate"
          type="primary"
          :disabled="!canDownloadUpdate"
          :loading="updateState.downloading"
          @click="handleDownloadUpdate"
        >
          {{ t("update.downloadVerified") }}
        </el-button>
        <span class="setting-hint">{{ t("general.autoUpdateUnavailable") }}</span>
      </div>
    </div>

    <div class="setting-row">
      <label class="setting-label">{{ t("general.pluginSandbox") }}</label>
      <div class="setting-control">
        <el-switch
          v-model="appState.enablePluginSandbox"
          :disabled="pluginSandboxForced"
          :aria-label="t('general.pluginSandbox')"
          @change="saveSettings"
        />
        <span class="setting-hint">{{ t(pluginSandboxHintKey) }}</span>
      </div>
    </div>

    <div class="setting-row">
      <label class="setting-label">{{ t("general.dataFolder") }}</label>
      <div class="setting-control">
        <el-input
          v-model="appState.dataFolderPath"
          size="default"
          style="width: var(--setting-control-width)"
          readonly
          :aria-label="t('general.dataFolder')"
        >
          <template #append>
            <el-button :icon="Folder" @click="handleBrowseFolder" :aria-label="t('common.browse')" />
          </template>
        </el-input>
      </div>
    </div>

    <div class="setting-row">
      <label class="setting-label">{{ t("general.applicationLog") }}</label>
      <div class="setting-control log-control">
        <span class="log-path" :title="logPath">{{ logPath || t("general.logUnavailable") }}</span>
        <el-button :icon="Document" size="default" @click="handleViewLog" :aria-label="t('general.viewLog')">
          {{ t("general.viewLog") }}
        </el-button>
      </div>
    </div>

    <!-- Priority 10: 崩溃报告查看器入口 -->
    <div class="setting-row">
      <label class="setting-label">{{ t("crashViewer.title") }}</label>
      <div class="setting-control log-control">
        <el-button :icon="Warning" size="default" @click="handleViewCrashReports" :aria-label="t('crashViewer.viewButton')">
          {{ t("crashViewer.viewButton") }}
        </el-button>
        <span class="setting-hint">{{ t("general.crashReportsLocalOnly") }}</span>
      </div>
    </div>

    <el-dialog
      v-model="logModalVisible"
      :title="t('general.logViewerTitle')"
      width="80%"
      top="6vh"
      :close-on-click-modal="false"
      :aria-label="t('general.logViewerTitle')"
    >
      <div v-loading="logLoading" class="log-modal-body">
        <p v-if="logError" class="log-error" role="alert">{{ t("general.logReadFailed", { error: logError }) }}</p>
        <pre v-else-if="logContent" class="log-pre">{{ logContent }}</pre>
        <p v-else class="log-empty">{{ t("general.logEmpty") }}</p>
      </div>
      <template #footer>
        <el-button @click="logModalVisible = false">{{ t("general.logClose") }}</el-button>
        <el-button type="primary" :loading="logLoading" @click="handleViewLog">{{ t("general.logRefresh") }}</el-button>
      </template>
    </el-dialog>

    <!-- Priority 10: 崩溃报告列表（list + delete） -->
    <el-dialog
      v-model="crashModalVisible"
      :title="t('crashViewer.title')"
      width="70%"
      top="8vh"
      :close-on-click-modal="false"
      :aria-label="t('crashViewer.title')"
    >
      <div v-loading="crashState.loading" class="crash-modal-body">
        <p v-if="crashState.errorMessage" class="log-error" role="alert">{{ crashState.errorMessage }}</p>
        <el-table
          v-if="crashState.reports.length > 0"
          :data="crashState.reports"
          size="small"
          border
          :empty-text="t('crashViewer.empty')"
        >
          <el-table-column :label="t('crashViewer.columnTime')" min-width="180">
            <template #default="{ row }">
              <span class="crash-time">{{ formatTime(row.timestamp) }}</span>
            </template>
          </el-table-column>
          <el-table-column prop="filename" :label="t('crashViewer.columnFile')" min-width="200" show-overflow-tooltip />
          <el-table-column :label="t('crashViewer.columnSize')" width="100">
            <template #default="{ row }">
              <span>{{ formatSize(row.size) }}</span>
            </template>
          </el-table-column>
          <el-table-column :label="t('common.actions')" width="160" align="right">
            <template #default="{ row }">
              <el-button size="small" @click="handleViewCrashDetail(row.filename)">{{ t("crashViewer.view") }}</el-button>
              <el-button size="small" type="danger" @click="handleDeleteCrash(row.filename)">{{ t("crashViewer.delete") }}</el-button>
            </template>
          </el-table-column>
        </el-table>
        <p v-else-if="!crashState.loading" class="log-empty">{{ t("crashViewer.empty") }}</p>
      </div>
      <template #footer>
        <el-button @click="crashModalVisible = false">{{ t("crashViewer.close") }}</el-button>
        <el-button :loading="crashState.loading" @click="loadCrashReports">{{ t("crashViewer.refresh") }}</el-button>
        <el-button
          type="danger"
          :disabled="crashState.reports.length === 0"
          @click="handleClearAllCrash"
        >
          {{ t("crashViewer.clearAll") }}
        </el-button>
      </template>
    </el-dialog>

    <!-- Priority 10: 崩溃报告详情（view detail） -->
    <el-dialog
      v-model="crashDetailVisible"
      :title="t('crashViewer.detailTitle')"
      width="70%"
      top="8vh"
      :close-on-click-modal="true"
      :aria-label="t('crashViewer.detailTitle')"
    >
      <div v-if="crashState.selected" class="crash-detail-body">
        <div class="crash-detail-meta">
          <div class="crash-meta-row"><span class="crash-meta-label">{{ t("crashViewer.columnFile") }}:</span> <code>{{ crashState.selected.filename }}</code></div>
          <div class="crash-meta-row"><span class="crash-meta-label">{{ t("crashViewer.columnTime") }}:</span> {{ formatTime(crashState.selected.timestamp) }}</div>
          <div class="crash-meta-row"><span class="crash-meta-label">{{ t("crashViewer.detailVersion") }}:</span> {{ crashState.selected.version }}</div>
          <div class="crash-meta-row"><span class="crash-meta-label">{{ t("crashViewer.detailOS") }}:</span> {{ crashState.selected.os }}</div>
          <div class="crash-meta-row"><span class="crash-meta-label">{{ t("crashViewer.detailErrorType") }}:</span> <code>{{ crashState.selected.errorType }}</code></div>
        </div>
        <div class="crash-section">
          <div class="crash-section-label">{{ t("crashViewer.detailMessage") }}</div>
          <pre class="crash-message">{{ crashState.selected.message }}</pre>
        </div>
        <div class="crash-section">
          <div class="crash-section-label">{{ t("crashViewer.detailStack") }}</div>
          <pre class="crash-stack">{{ crashState.selected.stack }}</pre>
        </div>
      </div>
      <template #footer>
        <el-button @click="crashDetailVisible = false">{{ t("crashViewer.close") }}</el-button>
      </template>
    </el-dialog>
  </section>
</template>

<style scoped>
.setting-hint {
  margin-left: 12px;
  font-size: 12px;
  color: var(--color-text-tertiary, #888);
}

.log-control {
  align-items: center;
  gap: 12px;
}

.update-control {
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
}

.log-path {
  display: inline-block;
  max-width: 260px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-family: var(--font-mono, monospace);
  font-size: 12px;
  color: var(--color-text-tertiary, #888);
}

.log-modal-body {
  min-height: 200px;
  max-height: 70vh;
  overflow-y: auto;
}

.log-pre {
  margin: 0;
  padding: 12px;
  background: var(--color-bg-surface-container, #f5f5f7);
  color: var(--color-text-primary, #d4d4d4);
  font-family: var(--font-mono, monospace);
  font-size: 12px;
  line-height: 1.5;
  white-space: pre-wrap;
  word-break: break-word;
  border-radius: 4px;
}

.log-error {
  color: var(--color-danger, #f56c6c);
  font-size: 13px;
}

.log-empty {
  color: var(--color-text-tertiary, #888);
  font-size: 13px;
  font-style: italic;
}

/* Priority 10: 崩溃报告查看器样式 */
.crash-modal-body {
  min-height: 200px;
  max-height: 65vh;
  overflow-y: auto;
}

.crash-time {
  font-family: var(--font-mono, monospace);
  font-size: 12px;
}

.crash-detail-body {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.crash-detail-meta {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 12px;
  background: var(--color-bg-surface-container, #f5f5f7);
  border-radius: 4px;
}

.crash-meta-row {
  font-size: 13px;
  color: var(--color-text-primary, #d4d4d4);
}

.crash-meta-row code {
  font-family: var(--font-mono, monospace);
  font-size: 12px;
  color: var(--color-text-secondary, #aaa);
}

.crash-meta-label {
  color: var(--color-text-tertiary, #888);
  margin-right: 4px;
}

.crash-section {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.crash-section-label {
  font-size: 12px;
  font-weight: 600;
  color: var(--color-text-tertiary, #888);
  text-transform: uppercase;
  letter-spacing: 0.5px;
}

.crash-message {
  margin: 0;
  padding: 12px;
  background: var(--color-bg-surface-container, #f5f5f7);
  color: var(--color-danger, #f56c6c);
  font-family: var(--font-mono, monospace);
  font-size: 12px;
  line-height: 1.5;
  white-space: pre-wrap;
  word-break: break-word;
  border-radius: 4px;
}

.crash-stack {
  margin: 0;
  padding: 12px;
  background: var(--color-bg-surface-container, #f5f5f7);
  color: var(--color-text-primary, #d4d4d4);
  font-family: var(--font-mono, monospace);
  font-size: 12px;
  line-height: 1.5;
  white-space: pre-wrap;
  word-break: break-word;
  border-radius: 4px;
  max-height: 40vh;
  overflow-y: auto;
}
</style>
