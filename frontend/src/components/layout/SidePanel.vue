<script setup lang="ts">
// Koyori IDE 组件 · Side Panel。
// 喵，这是 Side Panel，负责 Koyori IDE 的界面呈现喵~
import { computed, onMounted, watch } from "vue";
import { appState, setExtensionsSubview, toggleSidebar } from "@/stores/app";
import { Close } from "@element-plus/icons-vue";
import FileTree from "@/components/explorer/FileTree.vue";
import GitPanel from "@/components/layout/GitPanel.vue";
import PullRequestPanel from "@/components/layout/PullRequestPanel.vue";
import SearchPanel from "@/components/layout/SearchPanel.vue";
import AiChatPanel from "@/components/layout/AiChatPanel.vue";
import CallHierarchyPanel from "@/components/layout/CallHierarchyPanel.vue";
import OutlinePanel from "@/components/layout/OutlinePanel.vue";
import BuildToolWindow from "@/components/layout/BuildToolWindow.vue";
import DatabaseToolWindow from "@/components/database/DatabaseToolWindow.vue";
import InspectionToolWindow from "@/components/layout/InspectionToolWindow.vue";
import HTTPClientPanel from "@/components/http/HTTPClientPanel.vue";
// G-VSC-04: unified plugin/extension management panel renders in the
// "extensions" activity tab so native plugins and VS Code extensions
// coexist in one place with source labeling.
import PluginManagementPanel from "@/components/layout/PluginManagementPanel.vue";
// G-VSC-01: marketplace panel (Open VSX search/browse/install) shares the
// "extensions" tab via a sub-view toggle.
import MarketplacePanel from "@/components/marketplace/MarketplacePanel.vue";
import { activeFile, openFileFromPath } from "@/stores/editor";
import { gitState, refreshGit } from "@/stores/git";
import { pullRequestState, setSourceControlView } from "@/stores/pullRequests";
import { useI18n } from "@/lib/i18n";
// F-3 (prompt-2.md): VS Code 扩展动态视图渲染。当 activeExtensionView 非空时，
// SidePanel 渲染对应视图的 unsupported 状态（扩展主模块尚未接入）。
import { listAllVscodeExtensionViews } from "@/lib/vscodeExtensions";
import type { ExtensionViewContribution } from "@/types";

const { t } = useI18n();

const isCollapsed = computed(() => appState.sidebarCollapsed);
const currentTab = computed(() => appState.panelTab);
// G-VSC-01: sub-view of the extensions tab — "installed" (G-VSC-04 management)
// or "marketplace" (G-VSC-01 Open VSX browse/install).
const extensionsSubview = computed(() => appState.extensionsSubview);
const panelTitle = computed(() => {
  // F-3: 优先显示扩展视图名称。
  if (activeExtensionViewInfo.value) {
    return activeExtensionViewInfo.value.view.name || activeExtensionViewInfo.value.view.id;
  }
  switch (currentTab.value) {
    case "build":
      return t("activity.build");
    case "database":
      return t("activity.database");
    case "inspections":
      return t("activity.inspections");
    case "search":
      return t("activity.search");
    case "git":
      return t("activity.sourceControl");
    case "extensions":
      return t("activity.extensions");
    case "ai":
      return t("activity.ai");
    case "callHierarchy":
      return t("activity.callHierarchy");
    case "httpClient":
      return t("activity.httpClient");
    default:
      return t("activity.explorer");
  }
});

/**
 * F-3: 当前选中的扩展视图信息（视图对象 + 所属容器）。从所有容器中查找
 * id 匹配 activeExtensionView 的视图。null 表示未选中任何扩展视图。
 */
const activeExtensionViewInfo = computed<{ view: ExtensionViewContribution; container: string } | null>(() => {
  const viewId = appState.activeExtensionView;
  if (!viewId) return null;
  const allViews = listAllVscodeExtensionViews();
  for (const [container, viewList] of Object.entries(allViews)) {
    for (const view of viewList) {
      if (view.id === viewId) {
        return { view, container };
      }
    }
  }
  return null;
});

const projectPath = computed(() => appState.currentProject);
const projectName = computed(() => appState.projectName ?? t("sidePanel.defaultProjectName"));
const activeHTTPFile = computed(() => (
  activeFile.value?.path.toLowerCase().endsWith(".http") ? activeFile.value : null
));
// N-20: bind width to appState so the drag handle can resize the sidebar.
const panelWidthPx = computed(() =>
  isCollapsed.value ? "0px" : `${appState.sidebarWidth}px`,
);

function handleFileSelect(path: string) {
  openFileFromPath(path);
}

const emptyMessage = computed(() => {
  if (currentTab.value === "extensions") return t("sidePanel.noExtensions");
  if (currentTab.value === "ai") return t("sidePanel.aiReady");
  if (projectPath.value) return panelTitle.value;
  return t("sidePanel.openProjectToStart");
});

// Sync git branch name to appState for StatusBar
watch(
  () => gitState.branchName,
  (name) => {
    if (name) appState.branchName = name;
  },
);

onMounted(() => {
  if (projectPath.value && currentTab.value === "git") {
    refreshGit(projectPath.value);
  }
});

watch(
  [currentTab, projectPath],
  ([tab, path]) => {
    if (tab === "git" && path) {
      refreshGit(path as string);
    }
  },
);
</script>

<template>
  <aside
    class="side-panel"
    :class="{ 'side-panel--collapsed': isCollapsed }"
    :style="{ width: panelWidthPx }"
    role="complementary"
    :aria-label="t('sidePanel.panelAria', { title: panelTitle })"
  >
    <div class="side-panel__content">
      <!-- Panel header -->
      <div class="side-panel__header">
        <span class="side-panel__title">{{ panelTitle }}</span>
        <button
          type="button"
          class="side-panel__close"
          :aria-label="t('sidePanel.closePanelAria')"
          :title="t('sidePanel.closePanelTitle')"
          @click="toggleSidebar"
        >
          <el-icon :size="14">
            <Close />
          </el-icon>
        </button>
      </div>

      <!-- Panel body -->
      <div
        class="side-panel__body"
        :class="{ 'side-panel__body--chat': currentTab === 'ai' }"
      >
        <Transition name="side-panel-fade" mode="out-in">
          <!-- Explorer: file tree -->
          <div v-if="currentTab === 'explorer' && projectPath" key="explorer" class="side-panel__explorer">
            <div class="side-panel__files">
              <div class="side-panel__project-header">{{ projectName }}</div>
              <FileTree :path="projectPath" :name="projectName" :depth="0" @select="handleFileSelect" />
            </div>
            <OutlinePanel class="side-panel__outline" />
          </div>

          <BuildToolWindow
            v-else-if="currentTab === 'build' && projectPath"
            key="build"
            class="side-panel__build"
          />

          <DatabaseToolWindow
            v-else-if="currentTab === 'database'"
            key="database"
            class="side-panel__database"
          />

          <InspectionToolWindow
            v-else-if="currentTab === 'inspections'"
            key="inspections"
            :repo-path="projectPath ?? ''"
            class="side-panel__inspections"
          />

          <!-- Search panel -->
          <SearchPanel v-else-if="currentTab === 'search' && projectPath" key="search" />

          <!-- Source control: local changes and provider pull requests. -->
          <div v-else-if="currentTab === 'git' && projectPath" key="git" class="side-panel__source-control">
            <div class="side-panel__subtabs" role="tablist" :aria-label="t('pullRequests.sourceControlViews')">
              <button
                type="button"
                role="tab"
                data-test="source-control-changes"
                :aria-selected="pullRequestState.sourceControlView === 'changes'"
                class="side-panel__subtab"
                :class="{ 'side-panel__subtab--active': pullRequestState.sourceControlView === 'changes' }"
                @click="setSourceControlView('changes')"
              >
                {{ t("pullRequests.changesTab") }}
              </button>
              <button
                type="button"
                role="tab"
                data-test="source-control-pull-requests"
                :aria-selected="pullRequestState.sourceControlView === 'pullRequests'"
                class="side-panel__subtab"
                :class="{ 'side-panel__subtab--active': pullRequestState.sourceControlView === 'pullRequests' }"
                @click="setSourceControlView('pullRequests')"
              >
                {{ t("pullRequests.pullRequestsTab") }}
              </button>
            </div>
            <div class="side-panel__source-control-body">
              <GitPanel v-if="pullRequestState.sourceControlView === 'changes'" />
              <PullRequestPanel
                v-else
                :repo-path="projectPath"
                :config-id="appState.activeAIConfigId"
              />
            </div>
          </div>

          <!-- G-VSC-01 / G-VSC-04: extensions tab. A sub-tab toggle switches
               between installed extension/plugin management (G-VSC-04) and the
               Open VSX marketplace browse/install panel (G-VSC-01). Always
               available, even without an open project. -->
          <div v-else-if="currentTab === 'extensions'" key="extensions" class="side-panel__extensions">
            <div class="side-panel__subtabs" role="tablist">
              <button
                type="button"
                role="tab"
                :aria-selected="extensionsSubview === 'installed'"
                class="side-panel__subtab"
                :class="{ 'side-panel__subtab--active': extensionsSubview === 'installed' }"
                @click="setExtensionsSubview('installed')"
              >
                {{ t("marketplace.tabInstalled") }}
              </button>
              <button
                type="button"
                role="tab"
                :aria-selected="extensionsSubview === 'marketplace'"
                class="side-panel__subtab"
                :class="{ 'side-panel__subtab--active': extensionsSubview === 'marketplace' }"
                @click="setExtensionsSubview('marketplace')"
              >
                {{ t("marketplace.tabMarketplace") }}
              </button>
            </div>
            <div class="side-panel__extensions-body">
              <PluginManagementPanel v-if="extensionsSubview === 'installed'" />
              <MarketplacePanel v-else />
            </div>
          </div>

          <!-- AI chat panel (embedded，占用侧边栏空间，不挡住代码) -->
          <AiChatPanel v-else-if="currentTab === 'ai'" key="ai" embedded />

          <!-- F-1: Call/Type Hierarchy 面板 -->
          <CallHierarchyPanel v-else-if="currentTab === 'callHierarchy'" key="callHierarchy" />

          <HTTPClientPanel
            v-else-if="currentTab === 'httpClient' && activeHTTPFile"
            key="httpClient"
            :source="activeHTTPFile.content"
            :cursor-line="appState.cursorLine"
          />

          <div v-else-if="currentTab === 'httpClient'" key="httpClient-empty" class="side-panel__empty">
            <div class="side-panel__empty-line" aria-hidden="true" />
            <p class="side-panel__empty-text">{{ t("httpClient.openFile") }}</p>
          </div>

          <!-- F-3 (prompt-2.md): VS Code 扩展动态视图边界面板。
               当 activeExtensionView 非空（用户点击 ActivityBar 上的扩展视图按钮）
               且能找到对应视图信息时渲染。扩展宿主尚未接入，因此明确显示
               unsupported 状态，不伪造视图内容或交互。 -->
          <div
            v-else-if="activeExtensionViewInfo"
            :key="`ext-view-${activeExtensionViewInfo.view.id}`"
            class="side-panel__extension-view"
          >
            <div class="side-panel__extension-view-header">
              <span class="side-panel__extension-view-name">
                {{ activeExtensionViewInfo.view.name || activeExtensionViewInfo.view.id }}
              </span>
              <span class="side-panel__extension-view-badge" aria-hidden="true">
                {{ activeExtensionViewInfo.container }}
              </span>
            </div>
            <div class="side-panel__extension-view-body">
              <p class="side-panel__extension-view-placeholder">
                {{ t("sidePanel.extensionViewPlaceholder") }}
              </p>
              <p v-if="activeExtensionViewInfo.view.when" class="side-panel__extension-view-when">
                when: {{ activeExtensionViewInfo.view.when }}
              </p>
            </div>
          </div>

          <!-- Empty state for other tabs -->
          <div v-else key="empty" class="side-panel__empty">
            <div class="side-panel__empty-line" aria-hidden="true" />
            <p class="side-panel__empty-text">
              {{ emptyMessage }}
            </p>
          </div>
        </Transition>
      </div>
    </div>
  </aside>
</template>

<style scoped>
/* Apple SidePanel：Parchment 背景、发丝级边框、无阴影 */
.side-panel {
  min-width: 0;
  height: 100%;
  background: var(--color-sidebar-bg);
  overflow: hidden;
  flex-shrink: 0;
  z-index: 5;
  transition: width var(--transition-normal);
  /* Apple hairline 分割：色块本身就是分割，仅极弱边框 */
  border-right: 0.5px solid var(--color-border-subtle);
}

.side-panel--collapsed {
  width: 0;
  border-right: none;
}

.side-panel__content {
  display: flex;
  flex-direction: column;
  width: 100%;
  min-width: 140px;
  height: 100%;
  opacity: 1;
  transition: opacity var(--transition-fast);
}

.side-panel--collapsed .side-panel__content {
  opacity: 0;
  pointer-events: none;
}

/* Apple sub-nav 风格 header：52px 高、tagline 字体 */
.side-panel__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 16px;
  height: 44px;
  min-height: 44px;
}

.side-panel__title {
  /* Apple tagline 21px / 600 / 0.231px tracking */
  font-size: 14px;
  font-weight: 600;
  letter-spacing: -0.224px;
  color: var(--color-text-primary);
}

.side-panel__close {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  border: none;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-text-tertiary);
  cursor: pointer;
  transition:
    color var(--transition-fast),
    background-color var(--transition-fast),
    transform var(--transition-fast);
}

.side-panel__close:hover {
  color: var(--color-text-secondary);
  background-color: var(--color-border-subtle);
}

.side-panel__close:active {
  transform: scale(0.95);
}

.side-panel__close:focus-visible {
  outline: 2px solid var(--color-primary-focus);
  outline-offset: -2px;
}

.side-panel__body {
  flex: 1;
  overflow-y: auto;
  overflow-x: hidden;
}

.side-panel__body--chat {
  overflow: hidden;
  display: flex;
  flex-direction: column;
}

.side-panel__database {
  min-height: 0;
  height: 100%;
}

.side-panel__explorer {
  display: flex;
  flex-direction: column;
  min-height: 0;
  height: 100%;
}

.side-panel__files {
  flex: 1 1 62%;
  min-height: 120px;
  padding: 0 4px;
  overflow: auto;
}

.side-panel__outline {
  flex: 0 1 38%;
  min-height: 152px;
  border-top: 1px solid var(--color-border-subtle);
}

.side-panel__project-header {
  padding: 6px 12px 6px;
  font-size: 12px;
  font-weight: 600;
  letter-spacing: -0.12px;
  color: var(--color-text-secondary);
}

.side-panel__empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  height: 100%;
  padding: 32px 16px;
  text-align: center;
}

.side-panel__empty-line {
  width: 32px;
  height: 1px;
  background-color: var(--color-hairline);
  margin-bottom: 12px;
}

.side-panel__empty-text {
  font-size: 14px;
  color: var(--color-text-tertiary);
  line-height: 1.43;
  letter-spacing: -0.224px;
}

/* G-VSC-01: extensions tab wrapper — a sub-tab bar (Installed | Marketplace)
   above a body region that fills the remaining height so the marketplace
   panel's internal scroll works correctly. */
.side-panel__extensions {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
}

.side-panel__source-control {
  display: flex;
  flex-direction: column;
  min-height: 0;
  height: 100%;
}

.side-panel__source-control-body {
  display: flex;
  flex: 1;
  flex-direction: column;
  min-height: 0;
  overflow: hidden;
}

.side-panel__subtabs {
  display: flex;
  gap: 4px;
  padding: 0 8px 6px;
  border-bottom: 1px solid var(--color-border-subtle);
  flex-shrink: 0;
}

.side-panel__subtab {
  padding: 5px 10px;
  border: none;
  background: transparent;
  color: var(--color-text-tertiary);
  font-size: 0.8125rem;
  font-weight: 500;
  cursor: pointer;
  border-bottom: 2px solid transparent;
  transition: color var(--transition-fast) ease, border-color var(--transition-fast) ease;
}

.side-panel__subtab:hover {
  color: var(--color-text-secondary);
}

.side-panel__subtab--active {
  color: var(--chrome-text-active);
  border-bottom-color: var(--chrome-text-active);
}

.side-panel__extensions-body {
  flex: 1;
  min-height: 0;
  overflow: hidden;
  display: flex;
  flex-direction: column;
}

@media (prefers-reduced-motion: reduce) {
  .side-panel,
  .side-panel__content,
  .side-panel__close {
    transition: none;
  }
  .side-panel__close:active {
    transform: none;
  }
}

/* 侧边栏 tab 内容切换的丝滑过渡动画。
   out-in 模式：旧内容先淡出，新内容再淡入上移。 */
.side-panel-fade-enter-active {
  transition: opacity 0.2s cubic-bezier(0.4, 0, 0.2, 1),
              transform 0.2s cubic-bezier(0.4, 0, 0.2, 1);
}

.side-panel-fade-leave-active {
  transition: opacity 0.14s ease-out;
}

.side-panel-fade-enter-from {
  opacity: 0;
  transform: translateY(6px);
}

.side-panel-fade-leave-to {
  opacity: 0;
}

@media (prefers-reduced-motion: reduce) {
  .side-panel-fade-enter-active,
  .side-panel-fade-leave-active {
    transition: none;
  }
  .side-panel-fade-enter-from,
  .side-panel-fade-leave-to {
    opacity: 1;
    transform: none;
  }
}

/* F-3 (prompt-2.md): VS Code 扩展动态视图占位面板样式 */
.side-panel__extension-view {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
}

.side-panel__extension-view-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 8px 12px 10px;
  border-bottom: 1px solid var(--color-border-subtle);
}

.side-panel__extension-view-name {
  font-size: 13px;
  font-weight: 600;
  letter-spacing: -0.16px;
  color: var(--color-text-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.side-panel__extension-view-badge {
  flex-shrink: 0;
  padding: 2px 6px;
  font-size: 10px;
  font-weight: 500;
  letter-spacing: 0.08px;
  text-transform: uppercase;
  color: var(--color-text-tertiary);
  background-color: var(--color-border-subtle);
  border-radius: var(--radius-sm);
}

.side-panel__extension-view-body {
  flex: 1;
  overflow-y: auto;
  padding: 16px 12px;
}

.side-panel__extension-view-placeholder {
  margin: 0 0 8px;
  font-size: 13px;
  line-height: 1.5;
  color: var(--color-text-tertiary);
  text-align: center;
}

.side-panel__extension-view-when {
  margin: 0;
  padding: 6px 8px;
  font-size: 11px;
  font-family: var(--font-mono, monospace);
  color: var(--color-text-tertiary);
  background-color: var(--color-border-subtle);
  border-radius: var(--radius-sm);
  word-break: break-all;
}
</style>
