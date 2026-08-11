<script setup lang="ts">
// Koyori IDE 组件 · Remote View。
// 喵，这是 Remote View，负责 Koyori IDE 的界面呈现喵~
/**
 * F-10 (task-5.md): 远程项目管理视图 (/remote).
 *
 * 与 F-9 配套，但本任务仅创建视图框架，不实现 SSH 逻辑。提供：
 *   - 远程项目列表（已建立的 SSH 会话）
 *   - 连接状态展示
 *   - 添加远程项目入口（打开 RemoteProjectWizard）
 *
 * 复用 stores/remote 的 remoteState（connections / current / loading / error）
 * 与 listConnections / disconnect / openRemoteProjectEditor 等方法。
 * RemoteProjectWizard 已实现完整 SSH 创建向导，本视图通过 v-model:visible
 * 控制其显示。
 *
 * i18n 键前缀：view.remote.*
 */
import { onMounted, ref, watch } from "vue";
import {
  remoteState,
  listConnections,
  disconnect,
  openRemoteProjectEditor,
  editingRemoteProject,
  closeRemoteProjectEditor,
} from "@/stores/remote";
import RemoteProjectWizard from "@/components/remote/RemoteProjectWizard.vue";
import { useI18n } from "@/lib/i18n";

const { t } = useI18n();

// Wizard 显示状态：与 editingRemoteProject 联动（openRemoteProjectEditor
// 设置 editingRemoteProject 非 null 时显示向导）。
const wizardVisible = ref(false);

onMounted(() => {
  void listConnections();
});

function onAddRemote() {
  openRemoteProjectEditor();
  wizardVisible.value = true;
}

function onWizardClose() {
  wizardVisible.value = false;
  closeRemoteProjectEditor();
}

function onWizardCreated() {
  wizardVisible.value = false;
  closeRemoteProjectEditor();
  void listConnections();
}

async function onDisconnect(name: string) {
  await disconnect(name);
}

// 重新加载连接列表。
function onRefresh() {
  void listConnections();
}

// 当 editingRemoteProject 变化时同步 wizardVisible（防止外部调用
// openRemoteProjectEditor 后向导未显示）。
watch(editingRemoteProject, (val) => {
  wizardVisible.value = val !== null;
});
</script>

<template>
  <div class="remote-view">
    <header class="remote-view__header">
      <h1 class="remote-view__title">{{ t("view.remote.title") }}</h1>
      <p class="remote-view__subtitle">{{ t("view.remote.subtitle") }}</p>
    </header>

    <!-- 工具栏 -->
    <div class="remote-view__toolbar">
      <button type="button" class="remote-view__btn remote-view__btn--primary" @click="onAddRemote">
        + {{ t("view.remote.add") }}
      </button>
      <button type="button" class="remote-view__btn" :disabled="remoteState.loading" @click="onRefresh">
        ⟳ {{ t("view.remote.refresh") }}
      </button>
      <span v-if="remoteState.loading" class="remote-view__hint">{{ t("view.remote.loading") }}</span>
    </div>

    <!--
      GOAL-P0-07A: state the execution boundary on the persistent surface, not
      only inside the wizard. A user who returns to this view later must still
      be able to see that nothing here runs on the remote host.
    -->
    <div class="remote-view__boundary" role="note">
      <p class="remote-view__boundary-title">{{ t("remote.boundary.title") }}</p>
      <p class="remote-view__boundary-body">{{ t("remote.boundary.body") }}</p>
      <p class="remote-view__boundary-not">{{ t("remote.boundary.notRemote") }}</p>
    </div>

    <!-- 错误条 -->
    <div v-if="remoteState.error" class="remote-view__error" role="alert">
      ⚠ {{ remoteState.error }}
    </div>

    <!-- 远程项目列表 -->
    <div class="remote-view__body">
      <div v-if="remoteState.connections.length === 0" class="remote-view__empty">
        <p>{{ t("view.remote.emptyHint") }}</p>
        <button type="button" class="remote-view__btn remote-view__btn--primary" @click="onAddRemote">
          + {{ t("view.remote.addFirst") }}
        </button>
      </div>

      <ul v-else class="remote-view__list">
        <li
          v-for="name in remoteState.connections"
          :key="name"
          class="remote-view__item"
          :class="{ 'remote-view__item--active': remoteState.current === name }"
        >
          <div class="remote-view__item-head">
            <span class="remote-view__item-name">{{ name }}</span>
            <span
              v-if="remoteState.current === name"
              class="remote-view__badge remote-view__badge--connected"
            >
              {{ t("view.remote.connected") }}
            </span>
            <span v-else class="remote-view__badge remote-view__badge--disconnected">
              {{ t("view.remote.disconnected") }}
            </span>
          </div>
          <div v-if="remoteState.current === name && remoteState.currentRemotePath" class="remote-view__item-path">
            📁 {{ remoteState.currentRemotePath }}
          </div>
          <div class="remote-view__item-actions">
            <button
              v-if="remoteState.current === name"
              type="button"
              class="remote-view__btn remote-view__btn--danger"
              @click="onDisconnect(name)"
            >
              ⏻ {{ t("view.remote.disconnect") }}
            </button>
          </div>
        </li>
      </ul>
    </div>

    <!--
      SSH 远程项目创建向导（F-9 已实现）。
      本视图仅控制显示/隐藏，向导内部完成 SSH 连接测试、远程目录选择、
      项目创建，并通过 created 事件通知本视图刷新列表。
    -->
    <RemoteProjectWizard
      :visible="wizardVisible"
      @close="onWizardClose"
      @created="onWizardCreated"
    />
  </div>
</template>

<style scoped>
.remote-view {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
  background: var(--color-bg-base, #1e1e1e);
  color: var(--color-text-primary, #eee);
}

.remote-view__header {
  flex-shrink: 0;
  padding: 12px 16px;
  border-bottom: 1px solid var(--color-border, #333);
  background: var(--color-bg-elevated, #252526);
}

.remote-view__title {
  margin: 0;
  font-size: 16px;
  font-weight: 600;
}

.remote-view__subtitle {
  margin: 4px 0 0;
  font-size: 12px;
  opacity: 0.7;
}

.remote-view__toolbar {
  flex-shrink: 0;
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 6px 16px;
  border-bottom: 1px solid var(--color-border, #333);
  background: var(--color-bg-elevated, #252526);
}

.remote-view__btn {
  font-size: 11px;
  padding: 3px 10px;
  border-radius: 4px;
  border: 1px solid var(--color-border, #444);
  background: var(--color-bg-surface-container-low, rgba(255, 255, 255, 0.04));
  color: inherit;
  cursor: pointer;
}

.remote-view__btn:hover:not(:disabled) {
  background: rgba(255, 255, 255, 0.08);
}

.remote-view__btn:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}

.remote-view__btn--primary {
  background: var(--color-primary, #4f8cf7);
  border-color: var(--color-primary, #4f8cf7);
  color: #fff;
}

.remote-view__btn--primary:hover:not(:disabled) {
  background: var(--color-primary-hover, #3b78e0);
}

.remote-view__btn--danger {
  border-color: rgba(248, 81, 73, 0.4);
  color: #f85149;
}

.remote-view__btn--danger:hover:not(:disabled) {
  background: rgba(248, 81, 73, 0.1);
}

.remote-view__hint {
  font-size: 11px;
  opacity: 0.7;
}

.remote-view__error {
  padding: 6px 16px;
  background: rgba(248, 81, 73, 0.15);
  color: #ff7b72;
  border-bottom: 1px solid #f85149;
  font-size: 12px;
}

/*
 * GOAL-P0-07A: the boundary notice is persistent, not dismissible. The view is
 * a connection manager, and every visit must restate that the editor, terminal,
 * language servers, git, and debugger keep running locally.
 */
.remote-view__boundary {
  padding: 8px 16px;
  background: rgba(210, 153, 34, 0.1);
  border-bottom: 1px solid rgba(210, 153, 34, 0.35);
  font-size: 12px;
  line-height: 1.5;
}

.remote-view__boundary-title {
  margin: 0;
  font-weight: 600;
  color: #d29922;
}

.remote-view__boundary-body {
  margin: 4px 0 0;
  color: var(--color-text-secondary, #aaa);
}

.remote-view__boundary-not {
  margin: 4px 0 0;
  color: var(--color-text-secondary, #aaa);
  font-family: var(--font-mono, monospace);
}

.remote-view__body {
  flex: 1;
  min-height: 0;
  overflow: auto;
  padding: 12px 16px;
}

.remote-view__empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 12px;
  padding: 48px 16px;
  opacity: 0.7;
  text-align: center;
}

.remote-view__list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.remote-view__item {
  padding: 10px 12px;
  border: 1px solid var(--color-border, #333);
  border-radius: 6px;
  background: var(--color-bg-elevated, #252526);
  transition: border-color 0.15s ease;
}

.remote-view__item:hover {
  border-color: var(--color-primary, #4f8cf7);
}

.remote-view__item--active {
  border-color: rgba(63, 185, 80, 0.4);
  background: rgba(63, 185, 80, 0.04);
}

.remote-view__item-head {
  display: flex;
  align-items: center;
  gap: 8px;
}

.remote-view__item-name {
  flex: 1;
  font-weight: 600;
  font-size: 13px;
  font-family: var(--font-mono, ui-monospace, monospace);
}

.remote-view__badge {
  display: inline-flex;
  align-items: center;
  padding: 2px 8px;
  border-radius: 10px;
  font-size: 10px;
  font-weight: 500;
  letter-spacing: 0.02em;
}

.remote-view__badge--connected {
  background: rgba(63, 185, 80, 0.15);
  color: #3fb950;
}

.remote-view__badge--disconnected {
  background: rgba(255, 255, 255, 0.06);
  color: var(--color-text-secondary, #8b949e);
}

.remote-view__item-path {
  margin-top: 6px;
  font-size: 11px;
  opacity: 0.75;
  font-family: var(--font-mono, ui-monospace, monospace);
  word-break: break-all;
}

.remote-view__item-actions {
  margin-top: 8px;
  display: flex;
  gap: 6px;
}
</style>
