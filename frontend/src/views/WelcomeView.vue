<script setup lang="ts">
// Koyori IDE 组件 · Welcome View；交互服务：文件系统（FileService）。
// 喵，这是 Welcome View，负责 Koyori IDE 的界面呈现喵~
import { useRouter } from "vue-router";
import { FolderOpened, DocumentAdd, Clock, Monitor, Setting, Key, Notebook } from "@element-plus/icons-vue";
import { fileService } from "@/api/services";
import { openProject } from "@/stores/app";
import { useI18n } from "@/lib/i18n";
import { isCancellationError } from "@/lib/errors";
import { ElMessageBox } from "element-plus";

const { t } = useI18n();
// P9-G08: VERSION is injected at build time (vite define). Binding it here
// keeps the template type-checked and pins Welcome/About to the packaged
// metadata version.
const appVersion = __APP_VERSION__;

const router = useRouter();

async function handleOpenProject() {
  try {
    const path = await fileService.pickDirectory();
    if (!path) return;
    await openProject(path, path);
    router.push("/editor");
  } catch (e: unknown) {
    if (isCancellationError(e)) return;
    throw e;
  }
}

function handleNewProject() {
  router.push("/projects");
}

function handleRecentProjects() {
  router.push("/projects");
}

function handleQuickAction(action: string) {
  switch (action) {
    case "terminal":
      router.push("/editor");
      break;
    case "settings":
      router.push("/settings");
      break;
    case "shortcuts":
      router.push("/settings");
      break;
    case "docs":
      // Desktop has no in-app docs site. Do not open the Wails marketing
      // site as if it were Koyori documentation (P13-G02 / UI-2).
      void ElMessageBox.alert(
        t("welcome.docsLocalPath"),
        t("welcome.docs"),
        { confirmButtonText: t("common.ok") },
      );
      break;
  }
}
</script>

<template>
  <div class="welcome-page">
    <div class="welcome-inner">
      <!-- Hero -->
      <div class="welcome-hero anim-1">
        <h1 class="welcome-title text-hero">{{ t('app.name') }}</h1>
        <p class="welcome-tagline text-fine-print">{{ t("welcome.tagline") }}</p>
        <p class="welcome-version text-fine-print" data-testid="welcome-version">v{{ appVersion }}</p>
      </div>

      <!-- Separator -->
      <div class="welcome-divider anim-2" aria-hidden="true"></div>

      <!-- Primary Actions -->
      <nav class="welcome-actions anim-3" :aria-label="t('welcome.primaryActionsAria')">
        <button type="button" class="action-btn" :aria-label="t('welcome.openProject')" @click="handleOpenProject">
          <el-icon :size="18"><FolderOpened /></el-icon>
          <span>{{ t("welcome.openProject") }}</span>
        </button>

        <span class="action-sep" aria-hidden="true"></span>

        <button type="button" class="action-btn" :aria-label="t('welcome.newProject')" @click="handleNewProject">
          <el-icon :size="18"><DocumentAdd /></el-icon>
          <span>{{ t("welcome.newProject") }}</span>
        </button>

        <span class="action-sep" aria-hidden="true"></span>

        <button type="button" class="action-btn" :aria-label="t('welcome.recentProjects')" @click="handleRecentProjects">
          <el-icon :size="18"><Clock /></el-icon>
          <span>{{ t("welcome.recentProjects") }}</span>
        </button>
      </nav>

      <!-- Quick Actions -->
      <div class="welcome-quick anim-4" :aria-label="t('welcome.quickActionsAria')">
        <button type="button" class="quick-link" :aria-label="t('welcome.openTerminalAria')" @click="handleQuickAction('terminal')">
          <el-icon :size="13"><Monitor /></el-icon>
          <span>{{ t("welcome.terminal") }}</span>
        </button>

        <span class="quick-sep" aria-hidden="true">&middot;</span>

        <button type="button" class="quick-link" :aria-label="t('welcome.settings')" @click="handleQuickAction('settings')">
          <el-icon :size="13"><Setting /></el-icon>
          <span>{{ t("welcome.settings") }}</span>
        </button>

        <span class="quick-sep" aria-hidden="true">&middot;</span>

        <button type="button" class="quick-link" :aria-label="t('welcome.keyboardShortcutsAria')" @click="handleQuickAction('shortcuts')">
          <el-icon :size="13"><Key /></el-icon>
          <span>{{ t("welcome.keys") }}</span>
        </button>

        <span class="quick-sep" aria-hidden="true">&middot;</span>

        <button type="button" class="quick-link" :aria-label="t('welcome.documentationAria')" @click="handleQuickAction('docs')">
          <el-icon :size="13"><Notebook /></el-icon>
          <span>{{ t("welcome.docs") }}</span>
        </button>
      </div>

      <!-- Footer: same VERSION SSOT as the hero (P13-G01 / UI-1). -->
      <div class="welcome-footer anim-5">
        <span data-testid="welcome-footer-version">v{{ appVersion }}</span>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* ── Layout ─────────────────────────────────────────────── */

.welcome-page {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 100%;
  /* P2-16: 移除 min-height:100vh 避免在flex 父容器中溢出，     改为 min-height:0 + flex:1 让内容自适应可用高度 */
  min-height: 0;
  flex: 1;
  padding: 24px;
  background: var(--color-bg-base);
}

.welcome-inner {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 36px;
  max-width: 560px;
  width: 100%;
}

/* ── Hero ───────────────────────────────────────────────── */

.welcome-hero {
  text-align: center;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 12px;
}

.welcome-title {
  /* P2-11: 字号/字重由.text-hero 工具类提供     (Apple 56px/600, Claude 64px/400) */
  margin: 0;
  color: var(--color-primary);
}

.welcome-tagline {
  /* P2-12: 字号/字距/大小写由 .text-fine-print 工具类提供     (Apple 12px/-0.12px 不大写；Claude 12px/1.5px 大写) */
  color: var(--color-text-tertiary);
  margin: 0;
}

/* ── Divider ───────────────────────────────────────────── */

.welcome-divider {
  width: 48px;
  height: 1px;
  background: var(--color-outline-variant);
}

/* ── Primary Actions ────────────────────────────────────── */

.welcome-actions {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 4px;
  flex-wrap: wrap;
}

.action-btn {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  padding: 12px 20px;
  border: none;
  border-radius: var(--radius-md);
  background: transparent;
  color: var(--color-text-primary);
  font-family: var(--font-sans);
  font-size: 14px;
  font-weight: 500;
  cursor: pointer;
  transition: background var(--transition-fast);
  text-decoration: none;
  position: relative;
}

.action-btn:hover {
  background: var(--color-bg-surface-container);
}

.action-btn:active {
  transform: scale(0.98);
}

.action-btn:focus-visible {
  /* P3-20: 内容区域统一 outline-offset:2px + --color-primary-focus */
  outline: 2px solid var(--color-primary-focus);
  outline-offset: 2px;
  border-radius: var(--radius-md);
}

.action-sep {
  width: 3px;
  height: 3px;
  border-radius: 50%;
  background: var(--color-text-tertiary);
  opacity: 0.5;
  flex-shrink: 0;
}

/* ── Quick Actions ───────────────────────────────────────── */

.welcome-quick {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 2px;
  flex-wrap: wrap;
}

.quick-link {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 8px 12px;
  border: none;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-text-secondary);
  font-family: var(--font-sans);
  font-size: 12px;
  font-weight: 400;
  cursor: pointer;
  transition: color var(--transition-fast),
              background var(--transition-fast);
  text-decoration: none;
}

.quick-link:hover {
  color: var(--color-primary);
  background: var(--color-primary-container);
}

.quick-link:active {
  opacity: 0.8;
}

.quick-link:focus-visible {
  /* P3-20: 内容区域统一 --color-primary-focus */
  outline: 2px solid var(--color-primary-focus);
  outline-offset: 2px;
  border-radius: var(--radius-sm);
}

.quick-sep {
  color: var(--color-text-tertiary);
  font-size: 14px;
  opacity: 0.4;
  user-select: none;
  line-height: 1;
}

/* ── Footer ──────────────────────────────────────────────── */

.welcome-footer {
  font-size: 11px;
  color: var(--color-text-disabled);
  letter-spacing: 0.02em;
}

/* ── Staggered Entrance Animation ───────────────────────── */

@keyframes fadeUp {
  from {
    opacity: 0;
    transform: translateY(8px);
  }
  to {
    opacity: 1;
    transform: translateY(0);
  }
}

.anim-1 {
  animation: fadeUp var(--duration-slow) var(--ease-decelerate) 0ms both;
}

.anim-2 {
  animation: fadeUp var(--duration-slow) var(--ease-decelerate) 50ms both;
}

.anim-3 {
  animation: fadeUp var(--duration-slow) var(--ease-decelerate) 100ms both;
}

.anim-4 {
  animation: fadeUp var(--duration-slow) var(--ease-decelerate) 150ms both;
}

.anim-5 {
  animation: fadeUp var(--duration-slow) var(--ease-decelerate) 200ms both;
}

/* ── Reduced Motion ──────────────────────────────────────── */

@media (prefers-reduced-motion: reduce) {
  .anim-1,
  .anim-2,
  .anim-3,
  .anim-4,
  .anim-5 {
    animation: none;
    opacity: 1;
    transform: none;
  }
}

/* ── Responsive ─────────────────────────────────────────── */

@media (max-width: 480px) {
  .welcome-title {
    font-size: 32px;
  }

  .welcome-inner {
    gap: 28px;
  }

  .welcome-actions {
    flex-direction: column;
    gap: 2px;
  }

  .action-sep {
    width: auto;
    height: auto;
    width: 1px;
    height: 16px;
    border-radius: 0;
    background: var(--color-outline-variant);
    opacity: 0.3;
  }
}
</style>
