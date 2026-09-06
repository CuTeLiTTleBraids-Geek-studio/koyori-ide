<script setup lang="ts">
// App.vue — Koyori IDE 根组件。
//
// 职责：
//  1. 根据路由 meta 决定是否显示主布局（主 IDE / AI 伴侣窗口两种形态）
//  2. 抢占/释放「前端运行时所有权」（双窗 SSOT，见 main.ts 与 runtimeRole.ts）
//  3. 挂载后的全局副作用（错误捕获、恢复弹窗、系统模式监听）
import { computed, onBeforeUnmount, onErrorCaptured, onMounted, watch } from "vue";
import { useRoute } from "vue-router";
import MainLayout from "@/components/layout/MainLayout.vue";
import RecoveryDialog from "@/components/modals/RecoveryDialog.vue";
import { notifyError } from "@/lib/notifications";
import { appState } from "@/stores/app";
import { createExtensionDecorationType, createExtensionTextEditor } from "@/lib/extensionDecorations";
import { showExtensionInputBox, showExtensionQuickPick } from "@/lib/extensionHostUiBridge";
import { pushOutput } from "@/stores/output";
import { scanRecoverable } from "@/stores/recovery";

const route = useRoute();
const hideLayout = computed(() => route.meta.hideLayout === true);

const frontendRuntimeGlobal = globalThis as typeof globalThis & {
  __koyoriIdeFrontendRuntimeOwner?: symbol | null;
  __koyoriIdeRuntimeRole?: "main" | "ai" | "settings" | "e2e" | "minimal";
  __koyoriIdeAcquireFrontendRuntime?: (owner: symbol) => () => void;
};
const runtimeRole = frontendRuntimeGlobal.__koyoriIdeRuntimeRole ?? "minimal";
const ownsFullIDERuntime = runtimeRole === "main" || runtimeRole === "e2e";
const frontendRuntimeOwner = frontendRuntimeGlobal.__koyoriIdeFrontendRuntimeOwner;
const releaseFrontendRuntime = frontendRuntimeOwner
  ? frontendRuntimeGlobal.__koyoriIdeAcquireFrontendRuntime?.(frontendRuntimeOwner)
  : undefined;
let appLifecycleGeneration = 0;
let appUnmounted = false;
let recoveryScanWorkspace: string | null = null;
let stopRecoveryWatch: (() => void) | undefined;
let stopExtensionConfigurationWatch: (() => void) | undefined;

onBeforeUnmount(() => {
  appUnmounted = true;
  appLifecycleGeneration += 1;
  stopRecoveryWatch?.();
  stopRecoveryWatch = undefined;
  stopExtensionConfigurationWatch?.();
  stopExtensionConfigurationWatch = undefined;
  releaseFrontendRuntime?.();
});

// N-117 / Proposal AD: Error boundary. Catches errors thrown by child
// components' render, setup, lifecycle, and event handlers. Without this,
// a render error in any router view crashes the entire app (Vue unmounts
// the component tree). Returning false stops the error from propagating
// to app.config.errorHandler, but we still log it there via pushOutput —
// so we return true to let the global handler also see it.
onErrorCaptured((err, _instance, info) => {
  const msg = err instanceof Error ? err.message : String(err);
  console.error("[App onErrorCaptured]", err, info);
  pushOutput("ide", "error", `View error (${info}): ${msg}`);
  try {
    notifyError(`${msg}`, "View error");
  } catch {
    // notification may fail during early startup
  }
  // Return false to prevent the error from propagating further up (which
  // would crash the app). The error is already logged + notified above.
  return false;
});

// F-3 (prompt-2.md): 启动时触发 eager (*) 扩展激活，并预加载已安装扩展的
// manifest 到 vscodeExtensionActivation 缓存。声明 "*" 的扩展会在启动时
// 激活（可能影响性能，后端已记录；后续可加用户确认对话框）。
// 动态 import 避免循环依赖。失败不阻断应用启动。
//
// BUG-FIX-2d: 在扩展初始化前设置活跃编辑器回调和配置回调，
// 使扩展宿主能访问真实的编辑器状态和用户设置。
onMounted(() => {
  if (!ownsFullIDERuntime) return;
  appUnmounted = false;
  recoveryScanWorkspace = null;
  stopRecoveryWatch?.();
  stopRecoveryWatch = watch(
    () => appState.currentProject,
    (project) => {
      if (appUnmounted) return;
      if (!project) {
        recoveryScanWorkspace = null;
        return;
      }
      if (project === recoveryScanWorkspace) return;
      recoveryScanWorkspace = project;
      void scanRecoverable();
    },
    { immediate: true },
  );
  const generation = ++appLifecycleGeneration;
  const isCurrent = () =>
    !appUnmounted && generation === appLifecycleGeneration;

  void (async () => {
    try {
      const { loadInstalledExtensionManifests, activateEager } =
        await import("@/lib/vscodeExtensionActivation");
      if (!isCurrent()) return;

      // BUG-FIX-2d: 设置扩展宿主回调（必须在 loadInstalledExtensionManifests 之前，
      // 因为后者可能触发 activateExtensions 并创建 ExtensionHost）。
      const activationMod = await import("@/lib/vscodeExtensionActivation");
      if (!isCurrent()) return;

      // Editor state callback
      const { activeFile } = await import("@/stores/editor");
      activationMod.setExtensionHostWorkspaceFoldersCallback(() => {
        const project = appState.currentProject;
        if (!project) return undefined;
        const normalized = project.replaceAll("\\", "/");
        const name = normalized.split("/").filter(Boolean).pop() ?? "workspace";
        return [{
          uri: { fsPath: project, path: normalized, scheme: "file" },
          name,
          index: 0,
        }];
      });

      activationMod.setExtensionHostActiveEditorCallback(() => {
        const file = activeFile.value;
        if (!file) return undefined;
        const text = file.content ?? "";
        const lines = text.split(/\r\n|\r|\n/);
        const document = {
          uri: { fsPath: file.path, path: file.path, scheme: "file" },
          fileName: file.path,
          languageId: file.language,
          lineCount: lines.length,
          lineAt: (line: number) => ({ text: lines[line] ?? "" }),
          getText: () => text,
        };
        return createExtensionTextEditor(file.path, document);
      });
      activationMod.setExtensionHostDecorationCallback((extensionId, options) =>
        createExtensionDecorationType(extensionId, options),
      );

      // Configuration callback. Expose only editor-safe settings; AI keys,
      // provider credentials, approval policy, and workspace internals never
      // enter the Extension Host snapshot.
      const { settingsStore } = await import("@/stores/app");
      const extensionConfiguration = () => ({
        editor: {
          fontSize: settingsStore.fontSize,
          fontFamily: settingsStore.fontFamily,
          tabSize: settingsStore.tabSize,
          wordWrap: settingsStore.wordWrap,
          minimap: settingsStore.minimap,
          stickyScrollEnabled: settingsStore.stickyScrollEnabled,
          inlayHintsEnabled: settingsStore.inlayHintsEnabled,
          lineNumbers: settingsStore.lineNumbers,
          cursorBlinking: settingsStore.cursorBlinking,
          cursorStyle: settingsStore.cursorStyle,
          bracketColorization: settingsStore.bracketColorization,
          insertSpaces: settingsStore.insertSpaces,
          insertFinalNewline: settingsStore.insertFinalNewline,
          trimTrailingWhitespace: settingsStore.trimTrailingWhitespace,
        },
        files: {
          autoSave: settingsStore.autoSave,
          autoSaveDelay: settingsStore.autoSaveDelay,
          formatOnSave: settingsStore.formatOnSave,
        },
        terminal: {
          fontSize: settingsStore.terminalFontSize,
          cursorStyle: settingsStore.terminalCursorStyle,
          scrollback: settingsStore.scrollback,
        },
        git: { gitBlameEnabled: settingsStore.gitBlameEnabled },
      });
      activationMod.setExtensionHostConfigurationCallback((section) => {
        const snapshot = extensionConfiguration();
        if (!section) return snapshot;
        const value = snapshot[section as keyof typeof snapshot];
        return value && typeof value === "object"
          ? value as Record<string, unknown>
          : {};
      });
      stopExtensionConfigurationWatch?.();
      stopExtensionConfigurationWatch = watch(
        settingsStore,
        () => activationMod.notifyExtensionHostConfigurationChange(),
        { deep: true },
      );
      if (!isCurrent()) {
        stopExtensionConfigurationWatch?.();
        stopExtensionConfigurationWatch = undefined;
        return;
      }

      // G13: real saveAll (flush dirty buffers, propagate per-file failures)
      // and real notifications (lib/notifications), so extension APIs do not
      // report fake success.
      const { saveAllFilesDetailed } = await import("@/stores/editor");
      activationMod.setExtensionHostSaveAllCallback(async () => {
        return saveAllFilesDetailed();
      });
      const notifications = await import("@/lib/notifications");
      activationMod.setExtensionHostNotifyCallback((level, message) => {
        if (level === "error") notifications.notifyError(message, "Extension");
        else if (level === "warn") notifications.notifyWarning(message, "Extension");
        else notifications.notifyInfo(message, "Extension");
      });
      const extensionUi = await import("@/stores/extensionHostUi");
      activationMod.setExtensionHostInputCallback(showExtensionInputBox);
      activationMod.setExtensionHostQuickPickCallback(showExtensionQuickPick);
      activationMod.setExtensionHostStatusBarCallback(
        extensionUi.setExtensionStatusBarMessage,
        extensionUi.createExtensionStatusBarItem,
      );
      activationMod.setExtensionHostOutputCallback(extensionUi.writeExtensionOutput);
      activationMod.setExtensionHostProgressCallback(async (options, task) => {
        extensionUi.extensionProgress.value = { title: options.title };
        try {
          return await task({
            report: (value) => {
              extensionUi.extensionProgress.value = {
                title: options.title,
                message: value.message,
                increment: value.increment,
              };
            },
          });
        } finally {
          extensionUi.extensionProgress.value = null;
        }
      });

      if (!isCurrent()) return;
      await loadInstalledExtensionManifests();
      if (!isCurrent()) return;
      await activateEager();
    } catch (err) {
      if (isCurrent()) {
        console.warn("[F-3] eager activation failed:", err);
      }
    }
  })();
});
</script>

<template>
  <component :is="hideLayout ? 'div' : MainLayout">
    <router-view v-slot="{ Component }">
      <transition name="page-fade" mode="out-in">
        <component :is="Component" :key="route.path" />
      </transition>
    </router-view>
  </component>
  <RecoveryDialog v-if="ownsFullIDERuntime" />
</template>

<style scoped>
/* 建议二: Route-level transition — fade + subtle slide for a polished
 * page-switch feel. Uses --transition-normal (250ms) with the standard
 * easing curve. Respects prefers-reduced-motion via the global guard in
 * main.css. */
.page-fade-enter-active,
.page-fade-leave-active {
  transition: opacity var(--transition-normal), transform var(--transition-normal);
}
.page-fade-enter-from {
  opacity: 0;
  transform: translateY(8px);
}
.page-fade-leave-to {
  opacity: 0;
  transform: translateY(-8px);
}
</style>
