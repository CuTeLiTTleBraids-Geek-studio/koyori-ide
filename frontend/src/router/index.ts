// Koyori IDE 模块 · Index。
// 喵，这是 Koyori IDE 的 Index 模块（前端实现）~
import { createRouter, createWebHashHistory, type RouteRecordRaw } from "vue-router";
import { translate } from "@/lib/i18n";
import { appState } from "@/stores/app";

const routes: RouteRecordRaw[] = [
  {
    path: "/",
    redirect: "/welcome",
  },
  {
    path: "/welcome",
    name: "Welcome",
    component: () => import("@/views/WelcomeView.vue"),
    meta: { title: "Welcome", hideLayout: true },
  },
  {
    path: "/editor",
    name: "Editor",
    component: () => import("@/views/EditorView.vue"),
    meta: { title: "Editor" },
  },
  {
    path: "/settings",
    name: "Settings",
    component: () => import("@/views/SettingsView.vue"),
    meta: { title: "Settings" },
  },
  {
    path: "/projects",
    name: "Projects",
    component: () => import("@/views/ProjectsView.vue"),
    meta: { title: "Projects" },
  },
  {
    path: "/plugins",
    name: "Plugins",
    component: () => import("@/views/PluginsView.vue"),
    meta: { title: "Plugins" },
  },
  {
    // Plan 11 Task 1 — AI 助手独立全屏页面。复用主布局（hideLayout: false）
    // 以保留顶栏与 ActivityBar；三栏布局由 AiAssistantView 自行管理。
    path: "/ai",
    name: "AiAssistant",
    component: () => import("@/views/AiAssistantView.vue"),
    meta: { title: "AI Assistant" },
  },
  {
    // prompt-4 Task 2 — OS 级独立 AI 伴侣窗口根视图。
    // hideLayout: true，不复用主布局；自带活动栏 + 顶栏 + 消息流 + 输入区。
    path: "/ai-window",
    name: "AiWindow",
    component: () => import("@/views/AiWindowView.vue"),
    meta: { title: "AI Assistant", hideLayout: true },
  },
  {
    // F-10 (task-5.md): 独立调试视图。复用主布局以保留顶栏与 ActivityBar；
    // 全屏调试体验由 DebugView 内部管理（工具栏 + 调用栈 + 变量 + 断点 + 控制台）。
    path: "/debug",
    name: "Debug",
    component: () => import("@/views/DebugView.vue"),
    meta: { title: "Debug" },
  },
  {
    // F-10 (task-5.md): 性能分析视图。从 EditorView 拆出的独立 ProfilePanel，
    // 提供 CPU/Heap/Goroutine 采样与 Top functions 分析。
    path: "/profile",
    name: "Profile",
    component: () => import("@/views/ProfileView.vue"),
    meta: { title: "Profile" },
  },
  {
    // F-10 (task-5.md): 测试探索器视图。嵌套测试树 + 结果展示 + 输出面板，
    // 复用 testExplorer store 的全部能力。
    path: "/test",
    name: "Test",
    component: () => import("@/views/TestView.vue"),
    meta: { title: "Test" },
  },
  {
    // F-10 (task-5.md): 远程项目管理视图。与 F-9 配套；本任务仅创建视图框架，
    // 不实现 SSH 逻辑（复用 remoteState + RemoteProjectWizard）。
    path: "/remote",
    name: "Remote",
    component: () => import("@/views/RemoteView.vue"),
    meta: { title: "Remote" },
  },
];

const router = createRouter({
  history: createWebHashHistory(),
  routes,
});

router.beforeEach((to, _from, next) => {
  // Plan 11 Task 1 Step 8 — 未配置 AI Provider 时引导去设置页。
  // 判断标准：既无主 key（aiApiKeyConfigured），又无 multi-provider 配置。
  if (to.path === "/ai" || to.path === "/ai-window") {
    // /ai-window is an OS-level companion window: still allow mounting so the
    // user can configure from within, but do not hard-redirect away (no shared
    // navigation chrome). Only redirect the in-app /ai page.
    if (to.path === "/ai") {
      const configured = appState.aiApiKeyConfigured || appState.aiProviderConfigs.length > 0;
      if (!configured) {
        next({ path: "/settings", query: { section: "ai" } });
        return;
      }
    }
  }
  const title = to.meta.title as string | undefined;
  if (title) {
    document.title = `${title} — ${translate("app.name")}`;
  }
  next();
});

export default router;
