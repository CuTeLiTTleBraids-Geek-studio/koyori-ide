<script setup lang="ts">
// Koyori IDE 组件 · Title Bar；交互服务：窗口（WindowService）。
// 喵，这是 Title Bar，负责 Koyori IDE 的界面呈现喵~
import { computed } from "vue";
import { useRouter } from "vue-router";
import { appState, toggleSidebar, toggleTerminal } from "@/stores/app";
import { windowService } from "@/api/services";
import { Minus, FullScreen, Close, Menu } from "@element-plus/icons-vue";
import { useI18n } from "@/lib/i18n";
import { errorMessage } from "@/lib/errors";
import { recoveryState } from "@/stores/recovery";

const router = useRouter();
const { t } = useI18n();

const menuItems = [
  { labelKey: "title.file", action: "file" },
  { labelKey: "title.edit", action: "edit" },
  { labelKey: "title.view", action: "view" },
  { labelKey: "title.terminal", action: "terminal" },
  { labelKey: "title.help", action: "help" },
] as const;

// N-152: 最大化 ↔ 还原 共用一个按钮。appState.isWindowMaximised 由
// main.go 的 window:maximised 事件驱动，无需轮询后端。
const isMax = computed(() => appState.isWindowMaximised);
const maximiseLabelKey = computed(() => (isMax.value ? "title.restore" : "title.maximize"));

function handleMenu(action: string) {
  switch (action) {
    case "file":
      router.push("/welcome");
      break;
    case "edit":
      router.push("/editor");
      break;
    case "view":
      toggleSidebar();
      break;
    case "terminal":
      router.push("/editor");
      toggleTerminal();
      break;
    case "help":
      window.open("https://v3.wails.io/", "_blank", "noopener,noreferrer");
      break;
  }
}

function handleMinimise() {
  windowService.minimise();
}
function handleMaximiseToggle() {
  // 切换最大化/还原状态；图标由事件回调更新，无需乐观更新。
  windowService.toggleMaximise();
}
async function handleClose() {
  try {
    await windowService.close();
  } catch (err) {
    recoveryState.visible = true;
    recoveryState.error ??= errorMessage(err);
  }
}
</script>

<template>
  <header
    class="titlebar"
    role="banner"
    :aria-label="t('titleBar.bannerAria')"
  >
    <!-- Left: App identity -->
    <div class="titlebar__left">
      <span class="titlebar__diamond" aria-hidden="true">&#9670;</span>
      <span class="titlebar__title">{{ t('app.name') }}</span>
      <span v-if="appState.projectName" class="titlebar__separator" aria-hidden="true">&mdash;</span>
      <span v-if="appState.projectName" class="titlebar__project">
        {{ appState.projectName }}
      </span>
    </div>

    <!-- Center: Menu items -->
    <nav class="titlebar__menu" role="menubar" :aria-label="t('titleBar.menuAria')">
      <button
        type="button"
        v-for="item in menuItems"
        :key="item.action"
        class="titlebar__menu-item"
        role="menuitem"
        :aria-label="t('a11y.menuItem', { label: t(item.labelKey) })"
        @click="handleMenu(item.action)"
      >
        {{ t(item.labelKey) }}
      </button>
    </nav>

    <!-- P2-14: <520px 折叠进汉堡菜单 -->
    <el-dropdown
      trigger="click"
      placement="bottom-start"
      class="titlebar__menu-hamburger"
      @command="handleMenu"
    >
      <button
        type="button"
        class="titlebar__hamburger-btn"
        :aria-label="t('titleBar.menuAria')"
      >
        <el-icon :size="16"><Menu /></el-icon>
      </button>
      <template #dropdown>
        <el-dropdown-menu>
          <el-dropdown-item
            v-for="item in menuItems"
            :key="item.action"
            :command="item.action"
          >
            {{ t(item.labelKey) }}
          </el-dropdown-item>
        </el-dropdown-menu>
      </template>
    </el-dropdown>

    <!-- Right: Window controls -->
    <div class="titlebar__controls" role="group" :aria-label="t('titleBar.windowControlsAria')">
      <button
        type="button"
        class="titlebar__control"
        :aria-label="t('title.minimize')"
        :title="t('title.minimize')"
        @click="handleMinimise"
      >
        <el-icon :size="12"><Minus /></el-icon>
      </button>
      <button
        type="button"
        class="titlebar__control"
        :aria-label="t(maximiseLabelKey)"
        :title="t(maximiseLabelKey)"
        @click="handleMaximiseToggle"
      >
        <!-- N-152: 最大化时显示还原图标（两块重叠方框），未最大化时显示 FullScreen。 -->
        <el-icon v-if="!isMax" :size="12"><FullScreen /></el-icon>
        <svg v-else class="titlebar__restore-icon" width="12" height="12" viewBox="0 0 12 12" fill="none" xmlns="http://www.w3.org/2000/svg" aria-hidden="true">
          <rect x="1.5" y="3.5" width="7" height="7" rx="0.5" stroke="currentColor" stroke-width="1" />
          <path d="M3.5 3.5V2.5C3.5 2.22386 3.72386 2 4 2H10C10.2761 2 10.5 2.22386 10.5 2.5V8.5C10.5 8.77614 10.2761 9 10 9H9" stroke="currentColor" stroke-width="1" fill="none" />
        </svg>
      </button>
      <button
        type="button"
        class="titlebar__control titlebar__control--close"
        :aria-label="t('title.close')"
        :title="t('title.close')"
        @click="handleClose"
      >
        <el-icon :size="12"><Close /></el-icon>
      </button>
    </div>
  </header>
</template>

<style scoped>
/* Apple global-nav 规范：44px 高、12px nav-link 字体。
   背景与文本色随深/浅模式翻转（chrome 语义变量）。 */
.titlebar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  height: 44px;
  min-height: 44px;
  padding: 0 16px;
  background-color: var(--color-titlebar-bg);
  /* P3-21: 改用 1px border 替代 0.5px box-shadow（1x DPR 下不可见），
     box-sizing:border-box 保持总高 44px */
  box-sizing: border-box;
  border-bottom: 1px solid var(--chrome-border);
  z-index: 20;
  user-select: none;
  /* Frameless 模式下，标题栏作为窗口拖拽区域。
     Wails v3 运行时检测 --wails-draggable: drag 属性，在鼠标按下并移动时
     自动调用 invoke("wails:drag") 触发原生窗口拖拽。 */
  --wails-draggable: drag;
}

.titlebar__left {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  /* BUG3: 窗口缩小时，左侧内容溢出会与中心菜单重叠。overflow:hidden
     让 titlebar__title / titlebar__project 能正确截断。 */
  overflow: hidden;
}

.titlebar__diamond {
  font-size: 10px;
  color: var(--chrome-text-active);
  line-height: 1;
  flex-shrink: 0;
}

.titlebar__title {
  /* Apple nav-link 12px / 400 / -0.12px */
  font-size: 12px;
  font-weight: 600;
  color: var(--chrome-text-primary);
  white-space: nowrap;
  letter-spacing: -0.12px;
  /* BUG3: 允许缩窄并截断，避免长应用名溢出覆盖菜单。 */
  overflow: hidden;
  text-overflow: ellipsis;
  flex-shrink: 1;
  min-width: 0;
}

.titlebar__separator {
  color: var(--chrome-text-muted);
  font-size: 12px;
  flex-shrink: 0;
}

.titlebar__project {
  font-size: 12px;
  color: var(--chrome-text-secondary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  max-width: 200px;
  letter-spacing: -0.12px;
  flex-shrink: 1;
  min-width: 0;
}

.titlebar__menu {
  position: absolute;
  left: 50%;
  transform: translateX(-50%);
  display: flex;
  align-items: center;
  gap: 0;
  /* 菜单区域禁用拖拽，否则按钮点击会被吞 */
  --wails-draggable: none;
}

.titlebar__menu-item {
  /* Apple nav-link typography */
  padding: 4px 12px;
  font-size: 12px;
  font-family: var(--font-sans);
  font-weight: 400;
  color: var(--chrome-text-secondary);
  background: transparent;
  border: none;
  border-radius: var(--radius-sm);
  cursor: pointer;
  transition:
    color var(--transition-fast),
    background-color var(--transition-fast),
    transform var(--transition-fast);
  white-space: nowrap;
  line-height: 1;
  letter-spacing: -0.12px;
}

.titlebar__menu-item:hover {
  color: var(--chrome-text-primary);
  background-color: var(--chrome-hover-bg);
}

.titlebar__menu-item:active {
  /* Apple 微交互：scale(0.95) */
  transform: scale(0.95);
}

.titlebar__menu-item:focus-visible {
  outline: 2px solid var(--color-primary-focus);
  outline-offset: -2px;
}

.titlebar__controls {
  display: flex;
  align-items: center;
  gap: 4px;
  /* 窗口控制按钮区域禁用拖拽 */
  --wails-draggable: none;
}

.titlebar__control {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  padding: 6px;
  border: none;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--chrome-text-secondary);
  cursor: pointer;
  transition:
    color var(--transition-fast),
    background-color var(--transition-fast),
    transform var(--transition-fast);
}

.titlebar__control:hover {
  background-color: var(--chrome-hover-bg);
  color: var(--chrome-text-primary);
}

/* N-152: 还原图标内联 SVG，继承按钮 currentColor 并居中显示。 */
.titlebar__restore-icon {
  display: block;
  flex-shrink: 0;
}

.titlebar__control:active {
  transform: scale(0.95);
}

.titlebar__control--close:hover {
  /* P1-8: 改用语义变量 --color-error，随设计语言与明暗模式翻转
     (Apple 暗色 #ff6b6b / Claude 亮色 #c64545 / Claude 暗色 #ff6b6b) */
  background-color: var(--color-error);
  color: #fff;
}

.titlebar__control:focus-visible {
  outline: 2px solid var(--color-primary-focus);
  outline-offset: -2px;
}

@media (prefers-reduced-motion: reduce) {
  .titlebar__menu-item,
  .titlebar__control {
    transition: none;
  }
  .titlebar__menu-item:active,
  .titlebar__control:active {
    transform: none;
  }
}

/* P2-14: 中心菜单响应式 —— <720px 左对齐，<520px 折叠进汉堡 */
.titlebar__menu-hamburger {
  display: none;
  /* 菜单区域禁用拖拽，否则按钮点击会被吞 */
  --wails-draggable: none;
}

.titlebar__hamburger-btn {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  padding: 0;
  border: none;
  background: transparent;
  color: var(--chrome-text-secondary);
  cursor: pointer;
  border-radius: var(--radius-sm);
  transition: color var(--transition-fast), background-color var(--transition-fast);
}

.titlebar__hamburger-btn:hover {
  color: var(--chrome-text-primary);
  background-color: var(--chrome-hover-bg);
}

.titlebar__hamburger-btn:focus-visible {
  outline: 2px solid var(--color-primary-focus);
  outline-offset: -2px;
}

/* <720px：菜单从绝对定位居中改为左对齐 */
@media (max-width: 719px) {
  .titlebar__menu {
    position: static;
    transform: none;
    margin-left: 8px;
  }
  /* BUG3: 窗口缩小时减小项目名最大宽度，防止与菜单重叠。 */
  .titlebar__project {
    max-width: 120px;
  }
}

/* <520px：菜单折叠进汉堡 */
@media (max-width: 519px) {
  .titlebar__menu {
    display: none;
  }
  .titlebar__menu-hamburger {
    display: inline-flex;
  }
  /* BUG3: 极窄窗口下隐藏项目名和分隔符，仅保留应用名。 */
  .titlebar__project,
  .titlebar__separator {
    display: none;
  }
}
</style>
