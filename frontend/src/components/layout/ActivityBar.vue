<script setup lang="ts">
// Koyori IDE 组件 · Activity Bar；交互服务：窗口（WindowService）。
// 喵，这是 Activity Bar，负责 Koyori IDE 的界面呈现喵~
import { appState, setPanelTab, toggleSidebar, setActiveExtensionView } from "@/stores/app";
import type { PanelTab } from "@/stores/app";
import {
  FolderOpened,
  Search,
  SetUp,
  Connection,
  MagicStick,
  Setting,
  Share,
  Grid,
  Promotion,
  Tools,
  Coin,
  DocumentChecked,
  List,
  VideoPlay,
} from "@element-plus/icons-vue";
import { computed, onMounted, onBeforeUnmount, ref } from "vue";
import { useRoute, useRouter } from "vue-router";
import { useI18n } from "@/lib/i18n";
import { windowService } from "@/api/services";
import { notifyError } from "@/lib/notifications";
import { listAllVscodeExtensionViews } from "@/lib/vscodeExtensions";
import type { ExtensionViewContribution } from "@/types";

const { t } = useI18n();
const router = useRouter();
const route = useRoute();

interface ActivityItem {
  icon: typeof FolderOpened;
  labelKey: string;
  /** null = special handler (settings); "ai-window" = OS companion window */
  tab: PanelTab | "ai-window" | null;
  route?: "/debug" | "/test";
  isBottom?: boolean;
}

const items: ActivityItem[] = [
  { icon: FolderOpened, labelKey: "activity.explorer", tab: "explorer" },
  { icon: Tools, labelKey: "activity.build", tab: "build" },
  { icon: Coin, labelKey: "activity.database", tab: "database" },
  { icon: DocumentChecked, labelKey: "activity.inspections", tab: "inspections" },
  { icon: Search, labelKey: "activity.search", tab: "search" },
  { icon: Connection, labelKey: "activity.sourceControl", tab: "git" },
  { icon: SetUp, labelKey: "activity.extensions", tab: "extensions" },
  { icon: Promotion, labelKey: "activity.httpClient", tab: "httpClient" },
  { icon: VideoPlay, labelKey: "activity.debug", tab: null, route: "/debug" },
  { icon: List, labelKey: "activity.testExplorer", tab: null, route: "/test" },
  // F-1: Call/Type Hierarchy 侧边栏面板入口
  { icon: Share, labelKey: "activity.callHierarchy", tab: "callHierarchy" },
  // 双窗协议：活动栏 AI 打开独立 OS 窗口，不占用主窗侧边栏
  { icon: MagicStick, labelKey: "activity.ai", tab: "ai-window" },
];

const settingsItem: ActivityItem = {
  icon: Setting,
  labelKey: "activity.settings",
  tab: null,
  isBottom: true,
};

const activeTab = computed(() => appState.panelTab);
/** AI 伴侣窗口是否打开（活动栏高亮用） */
const aiWindowVisible = ref(false);
const aiWindowPoll = ref<ReturnType<typeof setInterval> | null>(null);

async function refreshAiWindowState(): Promise<void> {
  try {
    aiWindowVisible.value = await windowService.isAIWindowVisible();
  } catch {
    aiWindowVisible.value = false;
  }
}

const isAiActive = computed(() => aiWindowVisible.value);
const isSettingsActive = computed(() => route.path === "/settings");
const isFullViewActive = computed(() => route.path === "/debug" || route.path === "/test");

function isItemActive(item: ActivityItem): boolean {
  if (item.route) return route.path === item.route;
  if (item.tab === "ai-window") return isAiActive.value;
  if (isFullViewActive.value) return false;
  return item.tab !== null && activeTab.value === item.tab;
}

/**
 * F-3 (prompt-2.md): 聚合所有 VS Code 扩展贡献的视图，作为动态按钮追加到
 * ActivityBar。每个视图一个按钮（用通用 Grid 图标），点击后设置
 * activeExtensionView，SidePanel 渲染对应的 unsupported 状态面板。
 * 跨容器聚合：把所有容器（explorer/debug/...）下的视图都列出来。
 */
const extensionViewItems = computed<{ view: ExtensionViewContribution; container: string }[]>(() => {
  const allViews = listAllVscodeExtensionViews();
  const out: { view: ExtensionViewContribution; container: string }[] = [];
  for (const [container, viewList] of Object.entries(allViews)) {
    for (const view of viewList) {
      out.push({ view, container });
    }
  }
  return out;
});

const activeExtensionView = computed(() => appState.activeExtensionView);

async function handleAiWindowClick(): Promise<void> {
  try {
    // 切换独立 AI 窗口；隐藏后应立即清除活动态。
    await windowService.toggleAIWindow();
    await refreshAiWindowState();
  } catch (e) {
    aiWindowVisible.value = false;
    notifyError(
      e instanceof Error ? e.message : t("aiWindow.toggleFailed"),
    );
  }
}

function handleClick(item: ActivityItem) {
  if (item.route) {
    // VS Code 风格：再次点击当前全屏视图按钮（/debug、/test）返回编辑器，
    // 点击另一个全屏视图按钮则切换过去。
    if (route.path === item.route) {
      void router.push("/editor");
    } else {
      void router.push(item.route);
    }
    return;
  }
  if (item.tab === "ai-window") {
    void handleAiWindowClick();
    return;
  }
  if (item.tab) {
    // 全屏视图（/debug、/test）中点击侧边栏 tab：返回编辑器并展开对应面板，
    // 否则视图仍停留在全屏页且按钮高亮无法清除。
    if (isFullViewActive.value) {
      setPanelTab(item.tab);
      // F-3: 切换到内置 tab 时清除扩展视图选中。
      setActiveExtensionView(null);
      if (appState.sidebarCollapsed) {
        toggleSidebar();
      }
      void router.push("/editor");
      return;
    }
    // VS Code 风格：点击当前 active tab 折叠/展开侧边栏；
    // 点击其他 tab 切换并确保侧边栏展开（解决关闭后无法呼出的问题）。
    if (activeTab.value === item.tab && !appState.sidebarCollapsed) {
      toggleSidebar();
    } else {
      setPanelTab(item.tab);
      // F-3: 切换到内置 tab 时清除扩展视图选中。
      setActiveExtensionView(null);
      if (appState.sidebarCollapsed) {
        toggleSidebar();
      }
    }
  } else if (item.isBottom) {
    // 已在 settings 页面时再次点击则返回 /editor；否则进入 settings。
    if (isSettingsActive.value) {
      router.push("/editor");
    } else {
      router.push("/settings");
    }
  }
}

/**
 * F-3: 点击扩展视图按钮。切换选中状态并展开侧边栏。
 */
function handleExtensionViewClick(viewId: string) {
  // 全屏视图（/debug、/test）中点击扩展视图按钮：返回编辑器并展开对应面板。
  if (isFullViewActive.value) {
    setActiveExtensionView(viewId);
    if (appState.sidebarCollapsed) {
      toggleSidebar();
    }
    void router.push("/editor");
    return;
  }
  if (activeExtensionView.value === viewId && !appState.sidebarCollapsed) {
    toggleSidebar();
  } else {
    setActiveExtensionView(viewId);
    if (appState.sidebarCollapsed) {
      toggleSidebar();
    }
  }
}

onMounted(() => {
  void refreshAiWindowState();
  // 轮询窗口状态，关闭 AI 窗后活动栏高亮可消失
  aiWindowPoll.value = setInterval(() => {
    void refreshAiWindowState();
  }, 1500);
});

onBeforeUnmount(() => {
  if (aiWindowPoll.value !== null) {
    clearInterval(aiWindowPoll.value);
    aiWindowPoll.value = null;
  }
});
</script>

<template>
  <aside
    class="activity-bar"
    role="toolbar"
    :aria-label="t('activityBar.toolbarAria')"
  >
    <div class="activity-bar__top">
      <button
        type="button"
        v-for="item in items"
        :key="item.labelKey"
        class="activity-bar__item"
        :class="{
          'activity-bar__item--active': isItemActive(item),
        }"
        :aria-label="t(item.labelKey)"
        :aria-pressed="isItemActive(item)"
        :title="
          item.tab === 'ai-window'
            ? t('aiChat.openAIWindow')
            : t(item.labelKey)
        "
        @click="handleClick(item)"
      >
        <el-icon :size="20">
          <component :is="item.icon" />
        </el-icon>
        <!-- AI companion window open indicator -->
        <span
          v-if="item.tab === 'ai-window' && isAiActive"
          class="activity-bar__dot"
          aria-hidden="true"
        />
      </button>

      <!-- F-3 (prompt-2.md): VS Code 扩展贡献的动态视图按钮。
           每个视图一个按钮（通用 Grid 图标 + 扩展源徽章），点击后设置
           activeExtensionView，SidePanel 渲染对应的 unsupported 状态面板。
           extensionViewItems 跨容器聚合所有 views.* 下的视图。 -->
      <button
        v-for="extView in extensionViewItems"
        :key="`ext-view-${extView.view.id}`"
        type="button"
        class="activity-bar__item activity-bar__item--extension"
        :class="{
          'activity-bar__item--active': activeExtensionView === extView.view.id,
        }"
        :aria-label="extView.view.name || extView.view.id"
        :aria-pressed="activeExtensionView === extView.view.id"
        :title="extView.view.name || extView.view.id"
        @click="handleExtensionViewClick(extView.view.id)"
      >
        <el-icon :size="20">
          <Grid />
        </el-icon>
        <!-- 扩展视图源徽章：右下角小圆点，区分内置与扩展来源 -->
        <span
          class="activity-bar__ext-badge"
          aria-hidden="true"
        />
      </button>
    </div>

    <div class="activity-bar__bottom">
      <button
        type="button"
        class="activity-bar__item"
        :class="{ 'activity-bar__item--active': isSettingsActive }"
        :aria-label="t(settingsItem.labelKey)"
        :aria-pressed="isSettingsActive"
        :title="t(settingsItem.labelKey)"
        @click="handleClick(settingsItem)"
      >
        <el-icon :size="20">
          <component :is="settingsItem.icon" />
        </el-icon>
      </button>
    </div>
  </aside>
</template>

<style scoped>
/* Apple 风格 ActivityBar：纯黑背景、与 titlebar 同色形成统一全局导航 */
.activity-bar {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: space-between;
  width: 52px;
  min-width: 52px;
  height: 100%;
  background-color: var(--color-activitybar-bg);
  padding: 8px 0;
  z-index: 10;
  /* P2-15: 显式声明 overflow:visible，确保选中指示器不被祖先裁剪 */
  overflow: visible;
}

.activity-bar__top,
.activity-bar__bottom {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 4px;
  padding: 0 6px;
}

.activity-bar__bottom {
  padding-top: 8px;
  margin-top: auto;
  /* Apple 风格：用发丝级透明线，而非明显边框 */
  border-top: 0.5px solid var(--chrome-border);
}

.activity-bar__item {
  position: relative;
  display: flex;
  align-items: center;
  justify-content: center;
  width: 40px;
  height: 40px;
  border: none;
  /* Apple pill 容器：8px 圆角 */
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--chrome-text-secondary);
  cursor: pointer;
  transition:
    background-color var(--transition-fast),
    color var(--transition-fast),
    transform var(--transition-fast);
}

.activity-bar__item:hover {
  background-color: var(--chrome-hover-bg);
  color: var(--chrome-text-primary);
}

.activity-bar__item:active {
  /* Apple 微交互 */
  transform: scale(0.95);
}

/* Active 状态：使用 chrome-text-active（深/浅模式自适应） */
.activity-bar__item--active {
  color: var(--chrome-text-active);
  background-color: var(--chrome-active-bg);
}

.activity-bar__item--active::before {
  content: "";
  position: absolute;
  /* P2-15: left:0 确保指示器在按钮内部左边缘，不被祖先 overflow 裁剪 */
  left: 0;
  top: 50%;
  transform: translateY(-50%);
  width: 2px;
  height: 18px;
  border-radius: 2px;
  background: var(--chrome-text-active);
}

.activity-bar__item--active:hover {
  background-color: var(--chrome-active-bg);
  color: var(--chrome-text-active);
}

.activity-bar__dot {
  position: absolute;
  top: 7px;
  right: 7px;
  width: 6px;
  height: 6px;
  border-radius: var(--radius-full);
  background-color: var(--color-success);
  border: 1.5px solid var(--color-activitybar-bg);
  pointer-events: none;
}

/* F-3: 扩展视图源徽章——右下角小圆点，区分内置与扩展来源 */
.activity-bar__ext-badge {
  position: absolute;
  bottom: 7px;
  right: 7px;
  width: 5px;
  height: 5px;
  border-radius: var(--radius-full);
  background-color: var(--color-primary, #007aff);
  border: 1.5px solid var(--color-activitybar-bg);
  pointer-events: none;
}

.activity-bar__item:focus-visible {
  outline: 2px solid var(--color-primary-focus);
  outline-offset: -2px;
}

@media (prefers-reduced-motion: reduce) {
  .activity-bar__item {
    transition: none;
  }
  .activity-bar__item:active {
    transform: none;
  }
}
</style>
