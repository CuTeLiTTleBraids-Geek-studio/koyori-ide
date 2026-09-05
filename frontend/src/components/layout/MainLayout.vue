<script setup lang="ts">
// Koyori IDE 组件 · Main Layout；交互服务：布局（LayoutService）。
// 喵，这是 Main Layout，负责 Koyori IDE 的界面呈现喵~
import { ref, computed, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import {
  appState,
  setPanelTab,
  setExtensionsSubview,
  toggleTerminal,
  toggleActivityBar,
  toggleStatusBar,
  saveSettings,
  openProject,
} from "@/stores/app";
import { activeFile, saveFile } from "@/stores/editor";
import { clearMessages } from "@/stores/ai";
import { toggleInlineCompletion } from "@/stores/inlineCompletion";
import { registerShortcut, useKeyboard } from "@/composables/useKeyboard";
import { useDragResize } from "@/composables/useDragResize";
import { useI18n } from "@/lib/i18n";
import type { Command } from "@/types";
import ActivityBar from "./ActivityBar.vue";
import TitleBar from "./TitleBar.vue";
import SidePanel from "./SidePanel.vue";
import TerminalPanel from "./TerminalPanel.vue";
import StatusBar from "./StatusBar.vue";
import CommandPalette from "./CommandPalette.vue";
import QuickOpen from "./QuickOpen.vue";
// G-SYM-01: Workspace symbol search (Ctrl+T).
import WorkspaceSymbolPicker from "./WorkspaceSymbolPicker.vue";
// G-FEAT-01: New Project scaffolding wizard.
import NewProjectWizard from "../modals/NewProjectWizard.vue";
import ApplyDiffModal from "../modals/ApplyDiffModal.vue";
import RefactorPreviewModal from "../modals/RefactorPreviewModal.vue";
import LayoutLeafView from "./LayoutLeafView.vue";
import LayoutSplitView from "./LayoutSplitView.vue";
import {
  layoutState,
  setActiveLeaf,
  saveLayoutToBackend,
} from "@/stores/layout";
import { layoutService } from "@/api/services";
import { getUnifiedPaletteCommands } from "@/lib/unifiedCommands";
import {
  toolchainState,
  loadToolchainCommands,
  runToolchainCommand,
} from "@/stores/toolchain";
import {
  REFACTOR_COMMANDS,
  cancelRefactorRequest,
  refactorState,
  refreshRefactorActions,
  selectRefactorCommand,
} from "@/stores/refactor";
import { monacoLanguageToLSP } from "@/stores/lsp";
import { commandRegistry, syncPaletteCommands } from "@/lib/commands";
import { notifyError } from "@/lib/notifications";

useKeyboard();

const { t } = useI18n();
const route = useRoute();
// Priority 10: 命令面板「查看崩溃报告」跳转到设置页。
const router = useRouter();

// App.vue 把 <router-view> 内容作为 default slot 传给 MainLayout。
// /editor 路由由 LayoutLeafView 直接渲染 EditorView（不走 slot），其他
// 路由（/settings、/projects、/plugins）通过 slot 显示在 center 区域。
const isEditorRoute = computed(() => route.path === "/editor");

const paletteVisible = ref(false);
// Plan 55: Quick Open (Ctrl+P) fuzzy file finder.
const quickOpenVisible = ref(false);
// G-SYM-01: Workspace symbol picker (Ctrl+T).
const workspaceSymbolVisible = ref(false);
// G-FEAT-01: New Project wizard visibility.
const newProjectVisible = ref(false);

const commands = computed<Command[]>(() => {
  // N-42: Reference paletteVisible so this computed re-evaluates when the
  // palette opens, picking up plugin commands registered since the last
  // open. (Full reactivity is N-57/Proposal Q; this is a pragmatic bridge.)
  void paletteVisible.value;
  const builtin: Command[] = [
    {
      id: "save",
      label: t("mainLayout.commandSaveFile"),
      shortcut: "Ctrl+S",
      action: () => saveFile(),
    },
    {
      id: "save-all",
      label: t("mainLayout.commandSaveAll"),
      shortcut: "Ctrl+K S",
      action: () => {
        void import("@/stores/editor").then(({ saveAllFiles }) => {
          void saveAllFiles().then((n) => {
            if (n > 0) {
              void import("@/lib/notifications").then(({ notifySuccess }) =>
                notifySuccess(`Saved ${n} file(s)`),
              );
            }
          });
        });
      },
    },
    {
      id: "organize-imports",
      label: t("mainLayout.commandOrganizeImports"),
      shortcut: "Shift+Alt+O",
      action: () => {
        void import("@/stores/editor").then(
          async ({ activeFile, updateContent }) => {
            const f = activeFile.value;
            if (!f) return;
            const lang = f.path.endsWith(".go")
              ? "go"
              : f.path.endsWith(".ts") || f.path.endsWith(".tsx")
                ? "typescript"
                : f.path.endsWith(".js")
                  ? "javascript"
                  : "";
            if (!lang) return;
            const { organizeLSPImports } = await import("@/stores/lsp");
            const { applyTextEditsToContent } =
              await import("@/lib/lspCompletion");
            const edits = await organizeLSPImports(lang, f.path, f.content);
            if (!edits.length) return;
            updateContent(f.path, applyTextEditsToContent(f.content, edits));
          },
        );
      },
    },
    {
      id: "debug-package",
      label: t("mainLayout.commandDebugPackage"),
      action: () => {
        void import("@/stores/debug").then(
          ({ launchDebugPackage, refreshDebugStatus }) => {
            void refreshDebugStatus().then(() => launchDebugPackage());
          },
        );
      },
    },
    {
      id: "coverage-package",
      label: t("mainLayout.commandCoverage"),
      action: () => {
        void import("@/stores/coverage").then(({ runPackageCoverage }) => {
          void runPackageCoverage();
        });
      },
    },
    {
      id: "coverage-vitest",
      label: "Coverage: Vitest (lcov)",
      action: () => {
        void import("@/stores/coverage").then(({ runVitestCoverage }) => {
          void runVitestCoverage();
        });
      },
    },
    {
      id: "debug-open-panel",
      label: "Debug: Open Panel",
      action: () => {
        appState.terminalVisible = true;
        appState.bottomPanelView = "debug";
        void import("@/stores/debug").then(({ refreshDebugStatus }) => {
          void refreshDebugStatus();
        });
      },
    },
    {
      id: "go-test-json",
      label: "Go: Test JSON (explorer status)",
      action: () => {
        void import("@/stores/testExplorer").then(
          ({ discoverTests, runGoTestsJSON }) => {
            void discoverTests().then(() => runGoTestsJSON());
          },
        );
      },
    },
    {
      id: "test-explorer-discover",
      label: "Tests: Discover (unified Run/Debug/Coverage)",
      action: () => {
        void import("@/stores/testExplorer").then(({ discoverTests }) => {
          void discoverTests().then(() => {
            void import("@/lib/notifications").then(
              ({ notifySuccess, notifyInfo }) => {
                void import("@/stores/testExplorer").then(
                  ({ testExplorerState }) => {
                    notifySuccess(
                      `Found ${testExplorerState.entries.length} tests`,
                    );
                    notifyInfo(
                      "Use palette: run/debug/coverage at cursor, or Debug Test at Cursor",
                    );
                  },
                );
              },
            );
          });
        });
      },
    },
    // Priority-6: 连续测试开关 —— 保存文件时自动跑相关测试。
    {
      id: "test-continuous-toggle",
      label: "Tests: Toggle Continuous Testing (on file save)",
      action: () => {
        void import("@/stores/testExplorer").then(
          ({ toggleContinuousTesting, testExplorerState }) => {
            toggleContinuousTesting();
            void import("@/lib/notifications").then(({ notifyInfo }) => {
              notifyInfo(
                `Continuous testing ${testExplorerState.continuousTesting ? "ON" : "OFF"}`,
              );
            });
          },
        );
      },
    },
    {
      id: "debug-restart",
      label: "Debug: Restart",
      action: () => {
        void import("@/stores/debug").then(({ restartDebugSession }) => {
          void restartDebugSession();
        });
      },
    },
    {
      id: "debug-current-file",
      label: "Debug: Current file (language pack)",
      action: () => {
        const path = activeFile.value?.path;
        if (!path) return;
        void import("@/stores/debug").then(({ launchCurrentFile }) => {
          void launchCurrentFile(path, []);
        });
      },
    },
    {
      id: "debug-node",
      label: "Debug: Node current file (inspect-brk)",
      action: () => {
        const path = activeFile.value?.path;
        if (!path) return;
        void import("@/stores/debug").then(({ launchNodeProgram }) => {
          void launchNodeProgram(path, []);
        });
      },
    },
    {
      id: "workspace-next-root",
      label: "Workspace: Next module root",
      action: () => {
        void import("@/stores/workspaceModules").then(
          ({ selectWorkspaceRootInteractive }) => {
            void selectWorkspaceRootInteractive();
          },
        );
      },
    },
    {
      id: "test-at-cursor",
      label: t("mainLayout.commandTestAtCursor"),
      shortcut: "Ctrl+Shift+T",
      action: () => {
        const f = activeFile.value;
        if (!f) return;
        const path = f.path;
        const lang =
          f.language === "go" || path.endsWith(".go")
            ? "go"
            : path.endsWith(".ts") || path.endsWith(".tsx")
              ? "typescript"
              : path.endsWith(".js") || path.endsWith(".jsx")
                ? "javascript"
                : "";
        if (!lang) return;
        const line = Math.max(0, (appState.cursorLine || 1) - 1);
        void import("@/stores/toolchain").then(({ runTestAtCursor }) => {
          void runTestAtCursor(lang, path, line, f.content);
        });
      },
    },
    {
      id: "debug-test-at-cursor",
      label: "Debug Test at Cursor",
      action: () => {
        const f = activeFile.value;
        if (!f || !f.path.endsWith(".go")) return;
        const line = Math.max(0, (appState.cursorLine || 1) - 1);
        void import("@/stores/debug").then(({ debugTestAtCursor }) => {
          void debugTestAtCursor("go", f.path, line, f.content);
        });
      },
    },
    {
      id: "toggle-ai",
      label: t("mainLayout.commandToggleAiChat"),
      action: () => {
        // Opening an already-visible window also performs the conversation handoff.
        void import("@/stores/aiAssistant")
          .then(({ openAIDesktopWindow }) => openAIDesktopWindow())
          .catch((error: unknown) => {
            notifyError(error instanceof Error ? error.message : t("aiWindow.toggleFailed"));
          });
      },
    },
    {
      id: "toggle-terminal",
      label: t("mainLayout.commandToggleTerminal"),
      action: () => toggleTerminal(),
    },
    {
      id: "clear-chat",
      label: t("mainLayout.commandClearChat"),
      action: () => clearMessages(),
    },
    {
      id: "toggle-sidebar",
      label: t("mainLayout.commandToggleSidebar"),
      action: () => {
        appState.sidebarCollapsed = !appState.sidebarCollapsed;
      },
    },
    {
      id: "toggle-minimap",
      label: t("mainLayout.commandToggleMinimap"),
      action: () => {
        appState.minimap = !appState.minimap;
      },
    },
    {
      id: "toggle-inline-completion",
      label: t("mainLayout.commandToggleInlineCompletion"),
      action: () => toggleInlineCompletion(),
    },
    // N-20: layout toggles
    {
      id: "toggle-activity-bar",
      label: t("mainLayout.commandToggleActivityBar"),
      action: () => {
        toggleActivityBar();
        saveSettings();
      },
    },
    {
      id: "toggle-status-bar",
      label: t("mainLayout.commandToggleStatusBar"),
      action: () => {
        toggleStatusBar();
        saveSettings();
      },
    },
    // G-FEAT-01: New Project scaffolding wizard.
    {
      id: "new-project",
      label: t("mainLayout.commandNewProject"),
      action: () => {
        newProjectVisible.value = true;
      },
    },
    // Priority 10: 检查应用更新（查询 GitHub Releases 并对比当前版本）。
    {
      id: "check-updates",
      label: t("mainLayout.commandCheckUpdates"),
      action: () => {
        void import("@/stores/updateCrash").then(({ checkForUpdates }) => {
          void checkForUpdates();
        });
      },
    },
    // Priority 10: 查看崩溃报告（跳转到设置页的崩溃报告查看器）。
    {
      id: "view-crash-reports",
      label: t("mainLayout.commandViewCrashReports"),
      action: () => {
        router.push("/settings");
      },
    },
    // G-VSC-01: open the VS Code extension marketplace (Open VSX) in the
    // extensions tab. Ensures the sidebar is visible so the panel shows.
    {
      id: "browse-marketplace",
      label: t("mainLayout.commandBrowseMarketplace"),
      action: () => {
        setPanelTab("extensions");
        setExtensionsSubview("marketplace");
        if (appState.sidebarCollapsed) appState.sidebarCollapsed = false;
      },
    },
    {
      id: "koyoriIde.view.debug",
      label: t("mainLayout.commandOpenDebugView"),
      action: () => {
        void router.push("/debug");
      },
    },
    {
      id: "koyoriIde.view.testExplorer",
      label: t("mainLayout.commandOpenTestExplorer"),
      action: () => {
        void router.push("/test");
      },
    },
    {
      id: "koyoriIde.view.build",
      label: t("mainLayout.commandOpenBuild"),
      action: () => {
        setPanelTab("build");
        if (appState.sidebarCollapsed) appState.sidebarCollapsed = false;
        if (route.path !== "/editor") void router.push("/editor");
      },
    },
    {
      id: "koyoriIde.view.database",
      label: t("mainLayout.commandOpenDatabase"),
      action: () => {
        setPanelTab("database");
        if (appState.sidebarCollapsed) appState.sidebarCollapsed = false;
        if (route.path !== "/editor") void router.push("/editor");
      },
    },
    {
      id: "koyoriIde.view.httpClient",
      label: t("mainLayout.commandOpenHttpClient"),
      action: () => {
        setPanelTab("httpClient");
        if (appState.sidebarCollapsed) appState.sidebarCollapsed = false;
        if (route.path !== "/editor") void router.push("/editor");
      },
    },
    {
      id: "koyoriIde.view.inspections",
      label: t("mainLayout.commandOpenInspections"),
      action: () => {
        setPanelTab("inspections");
        if (appState.sidebarCollapsed) appState.sidebarCollapsed = false;
        if (route.path !== "/editor") void router.push("/editor");
      },
    },
    {
      id: "koyoriIde.view.callHierarchy",
      label: t("mainLayout.commandOpenCallHierarchy"),
      action: () => {
        setPanelTab("callHierarchy");
        if (appState.sidebarCollapsed) appState.sidebarCollapsed = false;
        if (route.path !== "/editor") void router.push("/editor");
      },
    },
  ];
  // G-FEAT-03: surface toolchain commands (go build / eslint / ...) grouped
  // by language. toolchainState.commands is refreshed when a project opens.
  const toolchainCommands: Command[] = toolchainState.commands.map((tc) => ({
    id: `toolchain-${tc.id}`,
    label: tc.label,
    action: () => {
      void runToolchainCommand(tc.id, activeFile.value?.path);
    },
  }));
  const refactorCommands: Command[] = REFACTOR_COMMANDS.map((command) => ({
    id: `refactor-${command.id}`,
    label: `${t("refactor.category")}: ${t(command.labelKey)}`,
    disabled: refactorState.loading || !refactorState.available[command.id],
    disabledReason: refactorState.loading
      ? t("refactor.checkingActions")
      : t("refactor.noActionAvailable"),
    action: () => {
      selectRefactorCommand(command.id);
    },
  }));
  // G-VSC-04: Merge extension-contributed commands via the unified
  // aggregator. Native plugin commands come first (higher priority), then
  // VS Code extension commands (supplementary). Each carries a `source`
  // field so CommandPalette.vue can render a source badge.
  // Built-in IDs take absolute priority — if an extension registers a
  // command with the same ID as a built-in, the built-in wins and the
  // extension command is filtered out to avoid confusion.
  const builtinIds = new Set(builtin.map((c) => c.id));
  const extensionCommands: Command[] = getUnifiedPaletteCommands().filter(
    (c) => !builtinIds.has(c.id),
  );
  return [
    ...builtin,
    ...refactorCommands,
    ...toolchainCommands,
    ...extensionCommands,
  ];
});

watch(commands, (items) => syncPaletteCommands(items), { immediate: true });

watch(paletteVisible, (visible) => {
  if (!visible) {
    cancelRefactorRequest();
    return;
  }
  const file = activeFile.value;
  if (!file) {
    cancelRefactorRequest();
    return;
  }
  const language = monacoLanguageToLSP(file.language, file.path);
  if (!language) {
    cancelRefactorRequest();
    return;
  }
  const line = Math.max(0, (appState.cursorLine || 1) - 1);
  const column = Math.max(0, (appState.cursorColumn || 1) - 1);
  void refreshRefactorActions({
    language,
    filePath: file.path,
    line,
    column,
    endLine: line,
    endColumn: column,
    content: file.content,
  });
});

// G-FEAT-03: refresh the toolchain command list when a project is opened so
// the palette only offers commands relevant to the workspace (Go vs TS/JS).
watch(
  () => appState.currentProject,
  (root) => {
    if (root) void loadToolchainCommands();
  },
  { immediate: true },
);

registerShortcut({
  key: "p",
  ctrl: true,
  shift: true,
  label: t("mainLayout.commandPalette"),
  handler: () => {
    paletteVisible.value = true;
  },
});

// Plan 55: Ctrl+P opens Quick Open (fuzzy file finder).
registerShortcut({
  key: "p",
  ctrl: true,
  label: t("mainLayout.quickOpen"),
  handler: () => {
    quickOpenVisible.value = true;
  },
});

// G-SYM-01: Ctrl+T opens workspace symbol search.
registerShortcut({
  key: "t",
  ctrl: true,
  label: t("mainLayout.workspaceSymbols"),
  handler: () => {
    workspaceSymbolVisible.value = true;
  },
});

registerShortcut({
  key: "s",
  ctrl: true,
  label: t("mainLayout.commandSaveFile"),
  handler: () => {
    saveFile();
  },
});

// N-20: drag handles for resizable panels.
// Sidebar: dragging right increases width.
const sidebarDrag = useDragResize({
  direction: "horizontal",
  sign: "positive-increases",
  min: 140,
  max: 600,
  getStartSize: () => appState.sidebarWidth,
  onResize: (w) => {
    appState.sidebarWidth = w;
  },
  onCommit: () => saveSettings(),
});

// Terminal: dragging up increases height (positive-decreases).
const terminalDrag = useDragResize({
  direction: "vertical",
  sign: "positive-decreases",
  min: 80,
  max: 600,
  getStartSize: () => appState.terminalHeight,
  onResize: (h) => {
    appState.terminalHeight = h;
  },
  onCommit: () => saveSettings(),
});

// Show sidebar drag handle only when sidebar is expanded.
const sidebarHandleVisible = computed(() => !appState.sidebarCollapsed);
// Show terminal drag handle only when terminal is visible.
const terminalHandleVisible = computed(() => appState.terminalVisible);

function handleRunCommand(cmd: Command) {
  if (cmd.disabled) return;
  void commandRegistry.execute(cmd.id).then((executed) => {
    if (!executed) cmd.action();
  });
  paletteVisible.value = false;
}

// G-FEAT-01: When the wizard successfully creates a project, add it to the
// recent list and open the new workspace.
async function handleProjectCreated(path: string) {
  try {
    await openProject(path, path);
  } catch (e) {
    console.error("Failed to open created project:", e);
  }
}

// N-30: Layout tree activation handler. When a leaf is clicked, set it
// active and persist the layout (best-effort).
function handleLeafActivate(leafId: string) {
  setActiveLeaf(leafId);
  // Persist layout changes (debounced via saveSettings pattern is not
  // needed here — saveLayoutToBackend is already best-effort).
  void saveLayoutToBackend(layoutService.saveLayout);
}

// N-53/Proposal P: A drag handle resize ended — persist the updated sizes.
function handleResizeEnd() {
  void saveLayoutToBackend(layoutService.saveLayout);
}

// N-30 / Proposal H: Reset the layout to default — both in-memory and
// in the backend (removes layout.json, then persists the fresh default).
function handleResetLayout() {
  // Imported here to avoid circular dependency at module load.
  import("@/stores/layout").then(({ resetLayoutFromBackend }) => {
    void resetLayoutFromBackend(
      layoutService.resetLayout,
      layoutService.saveLayout,
    );
  });
}
// Used by command palette / future UI; keep referenced for tree-shake safety.
void handleResetLayout;
</script>

<template>
  <div class="main-layout">
    <TitleBar />

    <div class="main-layout__body">
      <ActivityBar v-if="appState.activityBarVisible" />

      <SidePanel />

      <!-- Sidebar resize handle (N-20, N-54 a11y) -->
      <div
        v-if="sidebarHandleVisible"
        class="drag-handle drag-handle--horizontal"
        role="separator"
        tabindex="0"
        aria-orientation="vertical"
        :aria-valuenow="sidebarDrag.getCurrentValue()"
        :aria-valuemin="sidebarDrag.ariaMin"
        :aria-valuemax="sidebarDrag.ariaMax"
        :aria-label="t('layout.sidebarResizeHandle')"
        @pointerdown="sidebarDrag.onPointerDown"
        @keydown="sidebarDrag.onKeyDown"
      />

      <div class="main-layout__center">
        <!-- N-30: Layout tree renders the editor area. The root can be a
             leaf (single view) or a split (multiple views). -->
        <template v-if="isEditorRoute">
          <LayoutSplitView
            v-if="layoutState.tree.root.type === 'split'"
            :node="layoutState.tree.root"
            :active-leaf-id="layoutState.tree.activeLeafId"
            @activate="handleLeafActivate"
            @resizeend="handleResizeEnd"
          />
          <LayoutLeafView
            v-else
            :leaf="layoutState.tree.root"
            :active="layoutState.tree.root.id === layoutState.tree.activeLeafId"
            @activate="handleLeafActivate"
          />
        </template>
        <!-- 非 /editor 路由（/settings、/projects、/plugins）通过 slot
             渲染 App.vue 传给 MainLayout 的 <router-view> 内容。 -->
        <div v-else class="main-layout__route-view">
          <slot />
        </div>

        <!-- Terminal resize handle (N-20, N-54 a11y) -->
        <div
          v-if="terminalHandleVisible"
          class="drag-handle drag-handle--vertical"
          role="separator"
          tabindex="0"
          aria-orientation="horizontal"
          :aria-valuenow="terminalDrag.getCurrentValue()"
          :aria-valuemin="terminalDrag.ariaMin"
          :aria-valuemax="terminalDrag.ariaMax"
          :aria-label="t('layout.terminalResizeHandle')"
          @pointerdown="terminalDrag.onPointerDown"
          @keydown="terminalDrag.onKeyDown"
        />

        <TerminalPanel />
      </div>
    </div>

    <StatusBar v-if="appState.statusBarVisible" />

    <CommandPalette
      :visible="paletteVisible"
      :commands="commands"
      @close="paletteVisible = false"
      @run="handleRunCommand"
    />

    <!-- prompt-5 Task A: Diff confirm for AI apply-to-editor -->
    <ApplyDiffModal />
    <RefactorPreviewModal />

    <!-- Plan 55: Quick Open (Ctrl+P) fuzzy file finder -->
    <QuickOpen :visible="quickOpenVisible" @close="quickOpenVisible = false" />

    <!-- G-SYM-01: Workspace Symbol Search (Ctrl+T) -->
    <WorkspaceSymbolPicker
      :visible="workspaceSymbolVisible"
      @close="workspaceSymbolVisible = false"
    />

    <!-- G-FEAT-01: New Project scaffolding wizard -->
    <NewProjectWizard
      :visible="newProjectVisible"
      @close="newProjectVisible = false"
      @created="handleProjectCreated"
    />
  </div>
</template>

<style scoped>
.main-layout {
  display: flex;
  flex-direction: column;
  height: 100vh;
  width: 100vw;
  overflow: hidden;
  background-color: var(--color-bg-base);
  font-family: var(--font-sans);
}

.main-layout__body {
  display: flex;
  flex: 1;
  min-height: 0;
  overflow: hidden;
}

.main-layout__center {
  display: flex;
  flex-direction: column;
  flex: 1;
  min-width: 0;
}

/* 非 /editor 路由视图（settings/projects/plugins）通过 slot 渲染，
   需要 flex: 1 + min-height: 0 才能正确填充 center 区域并支持内部滚动。 */
.main-layout__route-view {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

.main-layout__route-view > :deep(*) {
  flex: 1;
  min-height: 0;
}

.main-layout__editor {
  flex: 1;
  display: flex;
  align-items: center;
  justify-content: center;
  min-height: 0;
  overflow: auto;
}

.main-layout__empty-text {
  font-size: 13px;
  color: var(--color-text-tertiary);
  user-select: none;
}

/* N-20: drag handles for resizable panels */
.drag-handle {
  flex-shrink: 0;
  background-color: var(--color-border-subtle);
  transition: background-color var(--transition-fast);
  z-index: 10;
}

.drag-handle:hover {
  background-color: var(--color-primary, #4285f4);
}

.drag-handle--horizontal {
  width: 4px;
  cursor: col-resize;
  height: 100%;
}

.drag-handle--vertical {
  height: 4px;
  cursor: row-resize;
  width: 100%;
}
</style>
