/**
 * F-10 (task-5.md): Layout store (聚合层).
 *
 * 整合两块布局相关状态：
 *   1. 编辑器区域 Split Tree 引擎（来自现有 `@/stores/layout.ts`，N-25 / Plan 72）：
 *      `layoutState` / `splitLeaf` / `closeLeaf` / `replaceLeafView` /
 *      `setActiveLeaf` / `resetLayout` / `serializeLayout` /
 *      `deserializeLayout` / `loadLayoutFromBackend` / `saveLayoutToBackend` /
 *      `resetLayoutFromBackend` 等。
 *   2. IDE 外围布局状态（来自 app.ts → windowStore / aiConfigStore）：
 *      `activityBarVisible` / `sidebarVisible`(= !sidebarCollapsed) /
 *      `panelVisible`(= terminalVisible) / `aiChatPosition` /
 *      `aiSidebarWidth` / `aiTerminalWidth` / `aiChatVisible` 等。
 *
 * 通过 Object.defineProperty 提供响应式 getter/setter，读写直接落到
 * appState（最终委托到 windowStore / aiConfigStore），保持 Vue 响应式传播
 * 与既有 `appState.xxx` 引用不变。
 *
 * 兼容性：
 *   - 本模块 re-export `@/stores/layout.ts` 的所有公开符号，旧代码
 *     `import { layoutState, splitLeaf, ... } from "@/stores/layout"` 继续可用。
 *   - app.ts re-export 本模块的 toggle* / set* 函数，旧代码
 *     `import { toggleSidebar, ... } from "@/stores/app"` 继续可用。
 */
// Koyori IDE 模块 · Layout Store。
// 喵，这是 Koyori IDE 的 Layout Store 模块（前端实现）~

// ---------------------------------------------------------------------------
// Re-export Split Tree 引擎（layout.ts）
// ---------------------------------------------------------------------------

export {
  layoutState,
  activeLeaf,
  findLeaf,
  findLeafByViewId,
  findParent,
  countLeaves,
  collectLeaves,
  splitLeaf,
  closeLeaf,
  replaceLeafView,
  setActiveLeaf,
  resetLayout,
  serializeLayout,
  deserializeLayout,
  loadLayoutFromBackend,
  saveLayoutToBackend,
  resetLayoutFromBackend,
} from "@/stores/layout";

// ---------------------------------------------------------------------------
// 外围布局状态访问器（委托到 appState → windowStore / aiConfigStore）
// ---------------------------------------------------------------------------

import { appState } from "@/stores/app";

/**
 * layoutStore 是一个响应式视图对象，把分散在 windowStore / aiConfigStore 中
 * 的布局相关字段聚合到一个命名空间下，便于新代码统一引用。
 *
 * 读写直接落到 appState，保持响应式语义与既有 `appState.xxx` 引用不变。
 */
export const layoutStore = {
  /** 活动栏可见性（来自 windowStore.activityBarVisible）。 */
  get activityBarVisible(): boolean {
    return appState.activityBarVisible;
  },
  set activityBarVisible(v: boolean) {
    appState.activityBarVisible = v;
  },

  /**
   * 侧边栏可见性。appState 用 sidebarCollapsed 表示折叠态，这里反转成
   * "visible" 语义以匹配 task-5.md 命名约定。
   */
  get sidebarVisible(): boolean {
    return !appState.sidebarCollapsed;
  },
  set sidebarVisible(v: boolean) {
    appState.sidebarCollapsed = !v;
  },

  /**
   * 底部面板可见性。appState 用 terminalVisible 表示，这里别名成
   * "panelVisible" 以匹配 task-5.md 命名约定。
   */
  get panelVisible(): boolean {
    return appState.terminalVisible;
  },
  set panelVisible(v: boolean) {
    appState.terminalVisible = v;
  },

  /** AI 聊天面板可见性（来自 windowStore.aiChatVisible）。 */
  get aiChatVisible(): boolean {
    return appState.aiChatVisible;
  },
  set aiChatVisible(v: boolean) {
    appState.aiChatVisible = v;
  },

  /** AI 聊天面板停靠位置（来自 aiConfigStore.aiChatPosition）。 */
  get aiChatPosition(): "left" | "right" {
    return appState.aiChatPosition;
  },
  set aiChatPosition(v: "left" | "right") {
    appState.aiChatPosition = v;
  },

  /** AI 伴侣窗口侧边栏宽度（来自 aiConfigStore.aiSidebarWidth）。 */
  get aiSidebarWidth(): number {
    return appState.aiSidebarWidth;
  },
  set aiSidebarWidth(v: number) {
    appState.aiSidebarWidth = v;
  },

  /** AI 伴侣窗口终端宽度（来自 aiConfigStore.aiTerminalWidth）。 */
  get aiTerminalWidth(): number {
    return appState.aiTerminalWidth;
  },
  set aiTerminalWidth(v: number) {
    appState.aiTerminalWidth = v;
  },

  /** 状态栏可见性（来自 windowStore.statusBarVisible）。 */
  get statusBarVisible(): boolean {
    return appState.statusBarVisible;
  },
  set statusBarVisible(v: boolean) {
    appState.statusBarVisible = v;
  },
};

// ---------------------------------------------------------------------------
// Set* 便捷方法
// ---------------------------------------------------------------------------

/** 设置活动栏可见性。 */
export function setActivityBarVisible(v: boolean): void {
  appState.activityBarVisible = v;
}

/** 设置侧边栏可见性（visible=true 展开，false 折叠）。 */
export function setSidebarVisible(v: boolean): void {
  appState.sidebarCollapsed = !v;
}

/** 设置底部面板可见性。 */
export function setPanelVisible(v: boolean): void {
  appState.terminalVisible = v;
}

/** 设置 AI 聊天面板可见性。 */
export function setAiChatVisible(v: boolean): void {
  appState.aiChatVisible = v;
}

/** 设置 AI 聊天面板停靠位置。 */
export function setAiChatPosition(v: "left" | "right"): void {
  appState.aiChatPosition = v;
}

/** 设置 AI 伴侣窗口侧边栏宽度。 */
export function setAiSidebarWidth(v: number): void {
  appState.aiSidebarWidth = v;
}

/** 设置 AI 伴侣窗口终端宽度。 */
export function setAiTerminalWidth(v: number): void {
  appState.aiTerminalWidth = v;
}

/** 设置状态栏可见性。 */
export function setStatusBarVisible(v: boolean): void {
  appState.statusBarVisible = v;
}

// ---------------------------------------------------------------------------
// Toggle* 函数（从 app.ts 迁移）
// ---------------------------------------------------------------------------

/** 切换侧边栏折叠/展开。 */
export function toggleSidebar(): void {
  appState.sidebarCollapsed = !appState.sidebarCollapsed;
}

/** 切换底部面板（终端）可见性。 */
export function toggleTerminal(): void {
  appState.terminalVisible = !appState.terminalVisible;
}

/** 切换 AI 聊天面板可见性。 */
export function toggleAiChat(): void {
  appState.aiChatVisible = !appState.aiChatVisible;
}

/** 切换 AI 聊天面板停靠位置（左 ↔ 右）。 */
export function toggleAiChatPosition(): void {
  appState.aiChatPosition = appState.aiChatPosition === "right" ? "left" : "right";
}

/** 切换活动栏可见性。 */
export function toggleActivityBar(): void {
  appState.activityBarVisible = !appState.activityBarVisible;
}

/** 切换状态栏可见性。 */
export function toggleStatusBar(): void {
  appState.statusBarVisible = !appState.statusBarVisible;
}
