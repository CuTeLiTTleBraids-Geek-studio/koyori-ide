<script setup lang="ts">
// Koyori IDE 组件 · Code Editor；交互服务：Git 集成（GitService）、窗口（WindowService）。
// 喵，这是 Code Editor，负责 Koyori IDE 的界面呈现喵~
import { computed, nextTick, onBeforeUnmount, ref, toRaw, watch } from "vue";
import { VueMonacoEditor } from "@guolao/vue-monaco-editor";
import type * as monacoEditor from "monaco-editor";
import {
  appState,
  cloneEditorGroup,
  setPanelTab,
  setSidebarVisible,
} from "@/stores/app";
import { detectLanguage } from "@/lib/language";
import { runAIAction } from "@/stores/ai";
import { sendSelectionToAIDesktopWindow } from "@/stores/aiAssistant";
import {
  requestCompletion,
  cancelInlineCompletion,
} from "@/stores/inlineCompletion";
import { runToolchainCommand } from "@/stores/toolchain";
import { notifyWarning, notifySuccess } from "@/lib/notifications";
import type { AIActionName } from "@/types";
import { getMonacoThemeNameForMode } from "@/lib/monaco-themes";
import { useI18n } from "@/lib/i18n";
import { registerLSPProviders } from "@/lib/lspCompletion";
import { gitService } from "@/api/services";
import { setCallHierarchyQuery } from "@/stores/lsp";
import { coverageHitsForFile, coverageState } from "@/stores/coverage";
import { registerEmmetProviders } from "@/lib/emmet";
import { registerExtensionEditorSurface } from "@/lib/extensionDecorations";
import { Position, Selection } from "@/lib/extensionHost/vscodeApi";
import { notifyExtensionHostTextEditorSelectionChange } from "@/lib/vscodeExtensionActivation";
import { layoutState, splitLeaf } from "@/stores/layout";
import { registerEditorCommands } from "@/lib/editorCommands";
import * as GitServiceBindings from "../../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/gitservice.js";

/** Active Monaco instance for Problems/search jump (prompt-10 10-D). */
const editorInstance = ref<monacoEditor.editor.IStandaloneCodeEditor | null>(
  null,
);

const { t } = useI18n();

const props = defineProps<{
  path: string;
  content: string;
  language?: string;
  groupId?: string;
}>();
const inlineCompletionOwner = Symbol("CodeEditor.inlineCompletion");

const emit = defineEmits<{
  (e: "update:content", value: string): void;
  (e: "cursor-change", line: number, column: number): void;
}>();

// Resolve the Monaco theme based on both the accent and the resolved mode
// (dark/light). We watch appState.theme (the user's choice) and read the
// effective <html data-mode> attribute that applyMode() keeps in sync, so
// the editor flips when the user switches mode or (for "system") when the
// OS preference changes.
function resolvedMode(): "dark" | "light" {
  const m = document.documentElement.getAttribute("data-mode");
  return m === "light" ? "light" : "dark";
}

function prefersHighContrast(): boolean {
  if (typeof window === "undefined" || typeof window.matchMedia !== "function")
    return false;
  return (
    window.matchMedia("(forced-colors: active)").matches ||
    window.matchMedia("(prefers-contrast: more)").matches
  );
}

const highContrastEnabled = ref(prefersHighContrast());
const contrastMediaListeners: Array<{
  query: MediaQueryList;
  listener: () => void;
}> = [];

const monacoTheme = computed(() => {
  // Touch appState.theme so this recomputes when the user switches mode.
  void appState.theme;
  return getMonacoThemeNameForMode(
    appState.accentTheme,
    resolvedMode(),
    highContrastEnabled.value,
  );
});

const resolvedLanguage = computed(
  () => props.language ?? detectLanguage(props.path),
);
const editorModelInstanceId =
  globalThis.crypto?.randomUUID?.() ?? Math.random().toString(36).slice(2);

function modelUriForPath(path: string): string {
  const normalized = path.replace(/\\/g, "/");
  let uri: URL;
  if (/^[A-Za-z]:\//.test(normalized)) {
    uri = new URL("file:///");
    uri.pathname = `/${normalized}`;
  } else if (
    /^(?:inmemory|untitled):/i.test(normalized) ||
    /^[A-Za-z][A-Za-z\d+.-]*:\//.test(normalized)
  ) {
    uri = new URL(normalized);
  } else if (normalized.startsWith("//")) {
    const [host, ...segments] = normalized.slice(2).split("/");
    uri = new URL(`file://${host || "localhost"}/`);
    uri.pathname = `/${segments.join("/")}`;
  } else {
    uri = new URL("file:///");
    uri.pathname = normalized.startsWith("/") ? normalized : `/${normalized}`;
  }
  return uri.toString();
}

const monacoModelPath = computed(() =>
  props.path
    ? modelUriForPath(props.path)
    : `inmemory://koyori-ide/${editorModelInstanceId}`,
);

// G-PERF-01: Monaco virtualizes line rendering by default — only visible
// lines (plus a small overscan) are realized in the DOM, so opening large
// files does not scale the DOM with line count. No explicit option is
// required; this is inherent to Monaco's viewport-based renderer. We avoid
// disabling it (there is no opt-out flag) and rely on the default here.
const options = computed(
  (): monacoEditor.editor.IStandaloneEditorConstructionOptions => ({
    fontSize: appState.fontSize,
    fontFamily: appState.fontFamily,
    tabSize: appState.tabSize,
    // G-EDIT-02: insert spaces instead of tabs (configurable in Editor settings).
    insertSpaces: appState.insertSpaces,
    wordWrap: appState.wordWrap ? "on" : "off",
    lineNumbers: appState.lineNumbers ? "on" : "off",
    minimap: {
      enabled: appState.minimapEnabled,
      side: "right",
      showSlider: "mouseover",
      renderCharacters: false,
      maxColumn: 120,
    },
    stickyScroll: {
      enabled: appState.stickyScrollEnabled,
      maxLineCount: 5,
      defaultModel: "outlineModel",
    },
    multiCursorModifier: "ctrlCmd",
    columnSelection: true,
    automaticLayout: true,
    scrollBeyondLastLine: false,
    smoothScrolling: true,
    cursorSmoothCaretAnimation: "on",
    cursorBlinking: appState.cursorBlinking as
      "blink" | "smooth" | "phase" | "expand" | "solid",
    cursorStyle: appState.cursorStyle as
      | "line"
      | "block"
      | "underline"
      | "line-thin"
      | "block-outline"
      | "underline-thin",
    renderWhitespace: "selection",
    bracketPairColorization: {
      enabled: appState.bracketColorization,
      independentColorPoolPerBracketType: true,
    },
    guides: {
      bracketPairs: "active",
      bracketPairsHorizontal: "active",
      highlightActiveBracketPair: true,
    },
    inlayHints: { enabled: appState.inlayHintsEnabled ? "on" : "off" },
    scrollbar: {
      verticalScrollbarSize: 14,
      horizontalScrollbarSize: 14,
      useShadows: false,
    },
    // 建议一: Smart Tab completion — enable inline suggestions (ghost text)
    // so Tab-to-accept works. Also enable tabCompletion so Tab accepts
    // word-based suggestions from the popup, matching VSCode/IntelliJ behavior.
    inlineSuggest: { enabled: appState.inlineCompletionEnabled },
    tabCompletion: "on" as const,
    // Enable quick suggestions (autocomplete popup) as the user types.
    quickSuggestions: { other: true, comments: false, strings: true },
    suggest: {
      showWords: true,
      showStatusBar: true,
      insertMode: "insert" as const,
      selectionMode: "always" as const,
      filterGraceful: true,
      snippetsPreventQuickSuggestions: false,
    },
    // Enable parameter hints (function signature help) as a popup.
    parameterHints: { enabled: true },
    suggestOnTriggerCharacters: true,
    // Enable code lens (references count, etc.) if providers are registered.
    codeLens: true,
    // G-ACT-01: Enable the lightbulb for code actions (quick fixes, refactors).
    lightbulb: {
      enabled: "on" as unknown as monacoEditor.editor.ShowLightbulbIconMode,
    },
  }),
);

function registerContextMenuActions(
  editor: monacoEditor.editor.IStandaloneCodeEditor,
  monaco: typeof import("monaco-editor"),
  disposables: monacoEditor.IDisposable[],
) {
  // prompt-4 Task 5: 发送选中代码到 AI 独立窗口（置顶快捷入口）
  disposables.push(
    editor.addAction({
      id: "ai-send-to-window",
      label: t("codeEditor.sendToAIWindow"),
      contextMenuGroupId: "ai-navigation",
      contextMenuOrder: 0,
      keybindings: [
        // prompt-5 Task C / BUG-M1: Ctrl/Cmd+Shift+A — send selection to AI window
        // monaco.KeyMod.CtrlCmd | monaco.KeyMod.Shift | monaco.KeyCode.KeyA
        // Numeric form avoids depending on monaco namespace at action-register time.
        2048 /* CtrlCmd */ | 1024 /* Shift */ | 31 /* KeyA */,
      ],
      run: (ed: monacoEditor.editor.IStandaloneCodeEditor) => {
        const selection = ed.getSelection();
        const model = ed.getModel();
        if (!model) return;
        const selectedText =
          selection && !selection.isEmpty()
            ? model.getValueInRange(selection)
            : "";
        if (!selectedText) {
          notifyWarning(t("codeEditor.selectCodeFirst"));
          return;
        }
        const filePath = props.path || appState.currentFilePath || "untitled";
        const language = model.getLanguageId();
        void sendSelectionToAIDesktopWindow(selectedText, language, filePath)
          .then(() => notifySuccess(t("codeEditor.sentToAIWindow")))
          .catch((e: unknown) =>
            notifyWarning(e instanceof Error ? e.message : String(e)),
          );
      },
    }),
  );

  const aiActions: Array<{ id: string; label: string; action: AIActionName }> =
    [
      {
        id: "ai-explain-ctx",
        label: t("codeEditor.aiExplain"),
        action: "explain",
      },
      {
        id: "ai-refactor-ctx",
        label: t("codeEditor.aiRefactor"),
        action: "refactor",
      },
      { id: "ai-fix-ctx", label: t("codeEditor.aiFix"), action: "fix" },
      {
        id: "ai-docs-ctx",
        label: t("codeEditor.aiDocs"),
        action: "generate_docs",
      },
      {
        id: "ai-tests-ctx",
        label: t("codeEditor.aiTests"),
        action: "generate_tests",
      },
      {
        id: "ai-optimize-ctx",
        label: t("codeEditor.aiOptimize"),
        action: "optimize",
      },
      {
        id: "ai-review-ctx",
        label: t("codeEditor.aiReview"),
        action: "review",
      },
      {
        id: "ai-security-ctx",
        label: t("codeEditor.aiSecurity"),
        action: "security",
      },
    ];

  aiActions.forEach((act, index) => {
    disposables.push(
      editor.addAction({
        id: act.id,
        label: act.label,
        contextMenuGroupId: "ai-navigation",
        contextMenuOrder: index + 1,
        run: (ed: monacoEditor.editor.IStandaloneCodeEditor) => {
          const selection = ed.getSelection();
          const model = ed.getModel();
          if (!model) return;
          const selectedText =
            selection && !selection.isEmpty()
              ? model.getValueInRange(selection)
              : "";
          if (!selectedText) {
            notifyWarning(t("codeEditor.selectCodeFirst"));
            return;
          }
          const filePath = props.path || appState.currentFilePath || "untitled";
          const language = model.getLanguageId();
          void runAIAction(act.action, selectedText, language, filePath);
        },
      }),
    );
  });

  // G-FEAT-03: right-click "Run <tool>" entries for the current file. The
  // offered commands depend on the file's language so only relevant tools
  // clutter the menu.
  const lang = resolvedLanguage.value;
  const toolchainCtx: Array<{ id: string; label: string; cmd: string }> = [];
  if (lang === "go") {
    toolchainCtx.push(
      {
        id: "tc-golangci-lint-ctx",
        label: t("toolchain.ctxGolangciLint"),
        cmd: "golangci-lint",
      },
      {
        id: "tc-go-build-ctx",
        label: t("toolchain.ctxGoBuild"),
        cmd: "go-build",
      },
      { id: "tc-go-vet-ctx", label: t("toolchain.ctxGoVet"), cmd: "go-vet" },
      { id: "tc-gofmt-ctx", label: t("toolchain.ctxGofmt"), cmd: "gofmt-file" },
    );
  } else if (lang === "typescript" || lang === "javascript") {
    toolchainCtx.push(
      {
        id: "tc-eslint-ctx",
        label: t("toolchain.ctxEslint"),
        cmd: "eslint-file",
      },
      { id: "tc-tsc-ctx", label: t("toolchain.ctxTsc"), cmd: "tsc" },
      {
        id: "tc-prettier-ctx",
        label: t("toolchain.ctxPrettier"),
        cmd: "prettier-file",
      },
    );
  }
  toolchainCtx.forEach((act, index) => {
    disposables.push(
      editor.addAction({
        id: act.id,
        label: act.label,
        contextMenuGroupId: "toolchain",
        contextMenuOrder: index,
        run: () => {
          const filePath = props.path || appState.currentFilePath || "";
          void runToolchainCommand(act.cmd, filePath);
        },
      }),
    );
  });
  // prompt-9 9-C / 9-H: Test at Cursor
  disposables.push(
    editor.addAction({
      id: "tc-test-at-cursor",
      label: t("toolchain.ctxTestAtCursor"),
      contextMenuGroupId: "toolchain",
      contextMenuOrder: 20,
      // Numeric form: CtrlCmd|Shift|KeyT (same pattern as Ctrl+Shift+A above)
      keybindings: [2048 /* CtrlCmd */ | 1024 /* Shift */ | 46 /* KeyT */],
      run: (ed) => {
        const filePath = props.path || appState.currentFilePath || "";
        const model = ed.getModel();
        if (!model || !filePath) return;
        const pos = ed.getPosition();
        const line = pos ? pos.lineNumber - 1 : 0;
        const lspLang =
          lang === "go"
            ? "go"
            : lang === "typescript" || lang === "javascript"
              ? lang
              : "";
        if (!lspLang) return;
        void import("@/stores/toolchain").then(({ runTestAtCursor }) => {
          void runTestAtCursor(lspLang, filePath, line, model.getValue());
        });
      },
    }),
  );
  // prompt-11 11-G: Debug Test at Cursor
  if (lang === "go") {
    disposables.push(
      editor.addAction({
        id: "tc-debug-test-at-cursor",
        label: "Debug Test at Cursor",
        contextMenuGroupId: "toolchain",
        contextMenuOrder: 21,
        run: (ed) => {
          const filePath = props.path || appState.currentFilePath || "";
          const model = ed.getModel();
          if (!model || !filePath) return;
          const pos = ed.getPosition();
          const line = pos ? pos.lineNumber - 1 : 0;
          void import("@/stores/debug").then(({ debugTestAtCursor }) => {
            void debugTestAtCursor("go", filePath, line, model.getValue());
          });
        },
      }),
    );
  }
  // prompt-11 11-A: F5 start debug package; F9 toggle breakpoint
  disposables.push(
    editor.addAction({
      id: "debug-start",
      label: "Debug: Start (F5)",
      keybindings: [16 /* F5 */],
      run: () => {
        void import("@/stores/debug").then(
          ({ launchCurrentFile, debugContinue, debugState }) => {
            if (debugState.running && debugState.stopped) {
              void debugContinue();
            } else if (!debugState.running) {
              const filePath = props.path || appState.currentFilePath || "";
              if (filePath) void launchCurrentFile(filePath);
            }
          },
        );
      },
    }),
  );
  disposables.push(
    editor.addAction({
      id: "debug-toggle-bp",
      label: "Debug: Toggle Breakpoint (F9)",
      keybindings: [20 /* F9 */],
      run: (ed) => {
        const filePath = props.path || appState.currentFilePath || "";
        const pos = ed.getPosition();
        if (!filePath || !pos) return;
        void import("@/stores/debug").then(({ toggleBreakpoint }) => {
          void toggleBreakpoint(filePath, pos.lineNumber);
        });
      },
    }),
  );
  // 建议一: Smart Tab completion — Alt+\ manually triggers an inline
  // (ghost-text) AI completion request. Monaco's inlineSuggest widget
  // auto-shows suggestions while typing, but this lets the user force a
  // request on demand (e.g. after pausing or repositioning the cursor).
  // Once ghost text is visible, Tab accepts it (built-in Monaco behavior
  // enabled by the inlineSuggest option above).
  disposables.push(
    editor.addAction({
      id: "ai-trigger-inline-completion",
      label: t("codeEditor.triggerInlineCompletion"),
      contextMenuGroupId: "ai-navigation",
      contextMenuOrder: 30,
      keybindings: [monaco.KeyMod.Alt | monaco.KeyCode.Backslash],
      run: (ed) => {
        if (!appState.inlineCompletionEnabled) return;
        // Trigger Monaco's built-in inline-suggestion widget, which calls
        // our registered InlineCompletionsProvider → requestCompletion().
        ed.trigger("keyboard", "editor.action.inlineSuggest.trigger", {});
      },
    }),
  );

  // F-1 (prompt-2.md): Show Call Hierarchy / Show Type Hierarchy 右键菜单。
  // Monaco 不暴露 registerCallHierarchyProvider，因此用 editor action 触发
  // 自定义侧边栏面板（CallHierarchyPanel.vue）展示调用/类型层次树。
  const triggerHierarchy = (mode: "call" | "type") => {
    const model = editor.getModel();
    const selection = editor.getSelection();
    if (!model || !selection) return;
    const filePath = props.path || appState.currentFilePath || "";
    if (!filePath) {
      notifyWarning(t("codeEditor.noFileForHierarchy"));
      return;
    }
    const language = model.getLanguageId();
    setCallHierarchyQuery({
      mode,
      language,
      filePath,
      line: selection.startLineNumber - 1, // Monaco 1-based → LSP 0-based
      column: selection.startColumn - 1,
      content: model.getValue(),
    });
    setSidebarVisible(true);
    setPanelTab("callHierarchy");
  };

  disposables.push(
    editor.addAction({
      id: "show-call-hierarchy",
      label: t("codeEditor.showCallHierarchy"),
      contextMenuGroupId: "navigation",
      contextMenuOrder: 1.0,
      run: () => triggerHierarchy("call"),
    }),
  );
  disposables.push(
    editor.addAction({
      id: "show-type-hierarchy",
      label: t("codeEditor.showTypeHierarchy"),
      contextMenuGroupId: "navigation",
      contextMenuOrder: 1.1,
      run: () => triggerHierarchy("type"),
    }),
  );
}

function registerInlineCompletionProvider(
  editor: monacoEditor.editor.IStandaloneCodeEditor,
  monaco: typeof import("monaco-editor"),
): monacoEditor.IDisposable {
  // N-64: registerInlineCompletionsProvider returns an IDisposable that
  // must be disposed on unmount. Without disposal, every editor mount
  // (e.g. switching tabs in LayoutView) leaks a provider on the global
  // monaco.languages registry, causing duplicate completion requests
  // and memory growth over a long session.
  return monaco.languages.registerInlineCompletionsProvider(
    { pattern: "**" },
    {
      provideInlineCompletions: async (model, position) => {
        if (toRaw(editorInstance.value)?.getModel() !== model) {
          return { items: [] };
        }
        const language = resolvedLanguage.value;
        const filePath = props.path;

        // Get prefix (code before cursor, up to ~50 lines for context)
        const startLine = Math.max(1, position.lineNumber - 50);
        const prefix = model.getValueInRange({
          startLineNumber: startLine,
          startColumn: 1,
          endLineNumber: position.lineNumber,
          endColumn: position.column,
        });

        // Get suffix (code after cursor, up to ~30 lines)
        const lineCount = model.getLineCount();
        const endLine = Math.min(lineCount, position.lineNumber + 30);
        const suffix = model.getValueInRange({
          startLineNumber: position.lineNumber,
          startColumn: position.column,
          endLineNumber: endLine,
          endColumn: model.getLineMaxColumn(endLine),
        });

        const text = await requestCompletion(
          prefix,
          suffix,
          language,
          filePath,
          inlineCompletionOwner,
        );
        if (
          !text ||
          componentUnmounting.value ||
          model.isDisposed?.() ||
          toRaw(editorInstance.value)?.getModel() !== model
        ) {
          return { items: [] };
        }

        return {
          items: [
            {
              insertText: text,
              range: {
                startLineNumber: position.lineNumber,
                startColumn: position.column,
                endLineNumber: position.lineNumber,
                endColumn: position.column,
              },
            },
          ],
        };
      },
      freeInlineCompletions: () => {
        // Completions hold no external resources; required by monaco 0.52+ API.
      },
    },
  );
}

// Track disposables created on mount so we can clean them up on unmount.
// N-64: the inline completion provider is registered on the global
// monaco.languages registry, so it survives editor dispose and must be
// explicitly disposed here.
const inlineCompletionProvider = ref<monacoEditor.IDisposable | null>(null);
// G-FEAT-02: LSP completion-item + hover providers, also registered on the
// global monaco.languages registry, so they must be disposed on unmount.
const lspProvidersDisposable = ref<monacoEditor.IDisposable | null>(null);
const emmetProvidersDisposable = ref<monacoEditor.IDisposable | null>(null);
const monacoInstance = ref<typeof import("monaco-editor") | null>(null);
const componentUnmounting = ref(false);
const cursorListener = ref<monacoEditor.IDisposable | null>(null);
// M-12: 收集 editor.addAction / onDid* 返回的 IDisposable，在卸载时统一释放，
// 避免每次挂载（切换标签）都在 Monaco 全局注册表泄漏一份 action/listener。
const disposables = ref<monacoEditor.IDisposable[]>([]);

// H-11: 装饰 ID 与防抖定时器改为 per-instance ref，确保多个 CodeEditor 实例
// 各自独立持有状态，避免模块级共享导致的装饰污染（与上方
// inlineCompletionProvider / lspProvidersDisposable / cursorListener 一致）。
const coverageDecorations = ref<string[]>([]);
const coverageWatchStop = ref<(() => void) | null>(null);
const breakpointDecorations = ref<string[]>([]);
const debugLineDecorations = ref<string[]>([]);
const debugWatchStop = ref<(() => void) | null>(null);
const debugWatchGeneration = ref(0);
const eslintDebounceTimer = ref<ReturnType<typeof setTimeout> | null>(null);
const typingDiagTimer = ref<ReturnType<typeof setTimeout> | null>(null);
const blameTimer = ref<ReturnType<typeof setTimeout> | null>(null);
const blameWatchStop = ref<(() => void) | null>(null);
const blameGeneration = ref(0);

function disposeLSPProviders(): void {
  const disposable = lspProvidersDisposable.value;
  lspProvidersDisposable.value = null;
  try {
    disposable?.dispose();
  } catch {
    // Monaco providers may already have been released by a parent teardown.
  }
}

function replaceLSPProviders(path: string): void {
  disposeLSPProviders();
  const monaco = monacoInstance.value;
  if (!monaco || componentUnmounting.value) return;
  lspProvidersDisposable.value = registerLSPProviders(monaco, path);
}

function applyCoverageDecorations(
  editor: monacoEditor.editor.IStandaloneCodeEditor,
  monaco: typeof import("monaco-editor"),
) {
  const hits = coverageHitsForFile(props.path || "");
  const decs: monacoEditor.editor.IModelDeltaDecoration[] = hits.map((h) => {
    const status =
      h.status === "partial" ? "partial" : h.covered ? "covered" : "uncovered";
    const overviewColor =
      status === "covered"
        ? "rgba(46,160,67,0.6)"
        : status === "partial"
          ? "rgba(210,153,34,0.72)"
          : "rgba(248,81,73,0.6)";
    return {
      range: new monaco.Range(h.line, 1, h.line, 1),
      options: {
        isWholeLine: true,
        className: `coverage-line--${status}`,
        linesDecorationsClassName: `coverage-gutter--${status}`,
        overviewRuler: {
          color: overviewColor,
          position: monaco.editor.OverviewRulerLane.Left,
        },
      },
    };
  });
  coverageDecorations.value = editor.deltaDecorations(
    coverageDecorations.value,
    decs,
  );
}

function applyBreakpointDecorations(
  editor: monacoEditor.editor.IStandaloneCodeEditor,
  monaco: typeof import("monaco-editor"),
) {
  const generation = debugWatchGeneration.value;
  void import("@/stores/debug").then(({ breakpointsForFile }) => {
    if (
      componentUnmounting.value ||
      generation !== debugWatchGeneration.value ||
      toRaw(editorInstance.value) !== editor
    )
      return;
    const bps = breakpointsForFile(props.path || "");
    const decs: monacoEditor.editor.IModelDeltaDecoration[] = bps.map((b) => ({
      range: new monaco.Range(b.line, 1, b.line, 1),
      options: {
        isWholeLine: false,
        glyphMarginClassName: b.verified
          ? "debug-bp-glyph"
          : "debug-bp-glyph debug-bp-glyph--unverified",
        glyphMarginHoverMessage: {
          value: b.verified
            ? `Breakpoint L${b.line}${b.condition ? ` if ${b.condition}` : ""}`
            : `Unverified breakpoint L${b.line}${b.message ? `: ${b.message}` : ""} — not bound yet`,
        },
      },
    }));
    breakpointDecorations.value = editor.deltaDecorations(
      breakpointDecorations.value,
      decs,
    );
  });
}

function applyDebugLineDecoration(
  editor: monacoEditor.editor.IStandaloneCodeEditor,
  monaco: typeof import("monaco-editor"),
) {
  const generation = debugWatchGeneration.value;
  void import("@/stores/debug").then(({ debugState }) => {
    if (
      componentUnmounting.value ||
      generation !== debugWatchGeneration.value ||
      toRaw(editorInstance.value) !== editor
    )
      return;
    const frame = debugState.stack[0];
    const path = (props.path || "").replace(/\\/g, "/").toLowerCase();
    if (!debugState.stopped || !frame?.file || !frame.line) {
      debugLineDecorations.value = editor.deltaDecorations(
        debugLineDecorations.value,
        [],
      );
      return;
    }
    const f = frame.file.replace(/\\/g, "/").toLowerCase();
    if (!(path === f || path.endsWith(f) || f.endsWith(path))) {
      debugLineDecorations.value = editor.deltaDecorations(
        debugLineDecorations.value,
        [],
      );
      return;
    }
    debugLineDecorations.value = editor.deltaDecorations(
      debugLineDecorations.value,
      [
        {
          range: new monaco.Range(frame.line, 1, frame.line, 1),
          options: {
            isWholeLine: true,
            className: "debug-current-line",
            glyphMarginClassName: "debug-current-glyph",
          },
        },
      ],
    );
  });
}

// G-BLAME-01: inline git blame decoration for the current line.
// H-11: blameDecorations 改为 per-instance ref，避免多实例共享。
const blameDecorations = ref<string[]>([]);

function clearTimer(timer: typeof blameTimer): void {
  if (!timer.value) return;
  clearTimeout(timer.value);
  timer.value = null;
}

function clearEditorDecorations(
  editor: monacoEditor.editor.IStandaloneCodeEditor | null,
  decorations: typeof coverageDecorations,
): void {
  if (editor && decorations.value.length > 0) {
    try {
      editor.deltaDecorations(decorations.value, []);
    } catch {
      // A Monaco remount may have disposed the previous editor first.
    }
  }
  decorations.value = [];
}

function disposeEditorMountResources(): void {
  const editor = toRaw(editorInstance.value);
  cancelInlineCompletion(inlineCompletionOwner);
  debugWatchGeneration.value += 1;
  blameGeneration.value += 1;

  debugWatchStop.value?.();
  debugWatchStop.value = null;
  coverageWatchStop.value?.();
  coverageWatchStop.value = null;
  blameWatchStop.value?.();
  blameWatchStop.value = null;

  clearTimer(blameTimer);
  clearTimer(typingDiagTimer);
  clearTimer(eslintDebounceTimer);

  clearEditorDecorations(editor, blameDecorations);
  clearEditorDecorations(editor, coverageDecorations);
  clearEditorDecorations(editor, breakpointDecorations);
  clearEditorDecorations(editor, debugLineDecorations);

  try {
    inlineCompletionProvider.value?.dispose();
  } catch {
    /* already disposed */
  }
  inlineCompletionProvider.value = null;
  disposeLSPProviders();
  try {
    emmetProvidersDisposable.value?.dispose();
  } catch {
    /* already disposed */
  }
  emmetProvidersDisposable.value = null;
  try {
    cursorListener.value?.dispose();
  } catch {
    /* already disposed */
  }
  cursorListener.value = null;

  for (const disposable of disposables.value.splice(0).reverse()) {
    try {
      disposable.dispose();
    } catch {
      /* already disposed */
    }
  }

  editorInstance.value = null;
  monacoInstance.value = null;
}

function formatRelativeTime(value: string): string {
  const timestamp = Date.parse(value);
  if (!Number.isFinite(timestamp)) return "unknown time";

  const seconds = Math.max(0, Math.floor((Date.now() - timestamp) / 1000));
  const units = [
    { seconds: 365 * 24 * 60 * 60, suffix: "y" },
    { seconds: 30 * 24 * 60 * 60, suffix: "mo" },
    { seconds: 24 * 60 * 60, suffix: "d" },
    { seconds: 60 * 60, suffix: "h" },
    { seconds: 60, suffix: "m" },
  ];
  for (const unit of units) {
    if (seconds >= unit.seconds) {
      return `${Math.floor(seconds / unit.seconds)}${unit.suffix} ago`;
    }
  }
  return "just now";
}

function applyBlameDecorations(
  editor: monacoEditor.editor.IStandaloneCodeEditor,
  _monaco: typeof import("monaco-editor"),
  blame: Array<{
    line: number;
    commit: string;
    author: string;
    email?: string;
    summary: string;
    time: string;
  }>,
): void {
  if (blame.length === 0) {
    blameDecorations.value = editor.deltaDecorations(
      blameDecorations.value,
      [],
    );
    return;
  }
  const decos = blame.slice(0, 1).map((b) => ({
    range: new _monaco.Range(b.line, 1, b.line, 1),
    options: {
      isWholeLine: true,
      hoverMessage: {
        value: [
          `**${b.summary}**`,
          `\`${b.commit.slice(0, 8)}\` | ${b.author}${b.email ? ` <${b.email}>` : ""} | ${formatRelativeTime(b.time)}`,
        ].join("\n\n"),
        isTrusted: false,
      },
      afterContentClassName: "koyori-ide-blame-deco",
      after: {
        content:
          `  ${b.author} · ${formatRelativeTime(b.time)} · ${b.summary}`.slice(
            0,
            140,
          ),
        inlineClassName: "koyori-ide-blame-text",
        cursorStops:
          0 as unknown as monacoEditor.editor.InjectedTextCursorStops,
      },
    },
  }));
  blameDecorations.value = editor.deltaDecorations(
    blameDecorations.value,
    decos,
  );
}

type AbsolutePathParts = {
  kind: "posix" | "drive" | "unc";
  anchor: string;
  segments: string[];
};

function hasInvalidPathSyntax(value: string): boolean {
  return (
    value.includes("\0") ||
    /^file:/i.test(value) ||
    /^[A-Za-z][A-Za-z0-9+.-]*:\/\//.test(value)
  );
}

function normalizeSegments(segments: string[]): string[] | null {
  const result: string[] = [];
  for (const segment of segments) {
    if (!segment || segment === ".") continue;
    if (segment === "..") {
      if (result.length === 0) return null;
      result.pop();
      continue;
    }
    result.push(segment);
  }
  return result;
}

function parseAbsolutePath(value: string): AbsolutePathParts | null {
  const normalized = value.trim().replace(/\\/g, "/");
  if (!normalized || hasInvalidPathSyntax(normalized)) return null;

  const driveMatch = /^([A-Za-z]):\/(.*)$/.exec(normalized);
  if (driveMatch) {
    const segments = normalizeSegments(driveMatch[2].split("/"));
    return segments
      ? { kind: "drive", anchor: `${driveMatch[1].toLowerCase()}:`, segments }
      : null;
  }
  if (/^[A-Za-z]:/.test(normalized)) return null;

  if (normalized.startsWith("//")) {
    const parts = normalized.slice(2).split("/");
    const server = parts.shift();
    const share = parts.shift();
    if (
      !server ||
      !share ||
      server === "." ||
      server === ".." ||
      share === "." ||
      share === ".."
    ) {
      return null;
    }
    const segments = normalizeSegments(parts);
    return segments
      ? {
          kind: "unc",
          anchor: `//${server.toLowerCase()}/${share.toLowerCase()}`,
          segments,
        }
      : null;
  }

  if (!normalized.startsWith("/")) return null;
  const segments = normalizeSegments(normalized.slice(1).split("/"));
  return segments ? { kind: "posix", anchor: "/", segments } : null;
}

function normalizeRelativePath(value: string): string[] | null {
  const normalized = value.trim().replace(/\\/g, "/");
  if (
    !normalized ||
    hasInvalidPathSyntax(normalized) ||
    normalized.startsWith("/") ||
    /^[A-Za-z]:/.test(normalized)
  ) {
    return null;
  }
  const segments = normalizeSegments(normalized.split("/"));
  return segments && segments.length > 0 ? segments : null;
}

function toRepoRelativePath(repoPath: string, filePath: string): string | null {
  const repo = parseAbsolutePath(repoPath);
  if (!repo) return null;

  const normalizedFile = filePath.trim().replace(/\\/g, "/");
  const fileIsAbsolute =
    normalizedFile.startsWith("/") || /^[A-Za-z]:\//.test(normalizedFile);
  if (!fileIsAbsolute) {
    return normalizeRelativePath(filePath)?.join("/") ?? null;
  }

  const file = parseAbsolutePath(filePath);
  if (!file || file.kind !== repo.kind || file.anchor !== repo.anchor)
    return null;
  if (file.segments.length <= repo.segments.length) return null;

  const caseInsensitive = repo.kind !== "posix";
  const isSameSegment = (left: string, right: string) =>
    caseInsensitive
      ? left.toLowerCase() === right.toLowerCase()
      : left === right;
  if (
    !repo.segments.every((segment, index) =>
      isSameSegment(segment, file.segments[index]),
    )
  ) {
    return null;
  }
  return file.segments.slice(repo.segments.length).join("/");
}

function currentGitRepoRoot(): string | null {
  const project = appState.currentProject?.trim();
  if (!project) return null;
  if (!project.toLowerCase().endsWith(".code-workspace")) return project;
  return appState.workspaceFolders[0]?.trim() || null;
}

function scheduleInlineBlame(
  editor: monacoEditor.editor.IStandaloneCodeEditor,
  monaco: typeof import("monaco-editor"),
  line = editor.getPosition()?.lineNumber ?? 1,
): void {
  const generation = ++blameGeneration.value;
  if (blameTimer.value) {
    clearTimeout(blameTimer.value);
    blameTimer.value = null;
  }

  const repo = currentGitRepoRoot();
  const filePath = props.path;
  const relativeFilePath =
    repo && filePath ? toRepoRelativePath(repo, filePath) : null;
  if (!appState.gitBlameEnabled || !repo || !filePath || !relativeFilePath) {
    applyBlameDecorations(editor, monaco, []);
    return;
  }

  blameTimer.value = setTimeout(async () => {
    blameTimer.value = null;
    try {
      let blame: Parameters<typeof applyBlameDecorations>[2];
      try {
        blame =
          (await GitServiceBindings.GetBlameForRange(
            repo,
            relativeFilePath,
            line,
            line,
          )) ?? [];
      } catch {
        // Older backends do not expose the cached range binding yet.
        blame = await gitService.getBlame(
          repo,
          relativeFilePath,
          line,
          line,
          "",
        );
      }
      if (
        generation !== blameGeneration.value ||
        toRaw(editorInstance.value) !== editor ||
        !appState.gitBlameEnabled ||
        currentGitRepoRoot() !== repo ||
        props.path !== filePath
      ) {
        return;
      }
      applyBlameDecorations(editor, monaco, blame);
    } catch {
      // Blame is best-effort and must not interrupt editing.
    }
  }, 250);
}

/** prompt-11 11-D / 11-J: debounced ESLint + LSP diagnostics while typing. */
function scheduleLiveDiagnostics(content: string) {
  const filePath = props.path || "";
  const lang = resolvedLanguage.value;
  if (!filePath) return;
  if (typingDiagTimer.value) clearTimeout(typingDiagTimer.value);
  typingDiagTimer.value = setTimeout(() => {
    const lspLang =
      lang === "go" || lang === "typescript" || lang === "javascript"
        ? lang
        : lang === "typescriptreact"
          ? "typescript"
          : lang === "javascriptreact"
            ? "javascript"
            : "";
    if (lspLang) {
      void import("@/stores/lsp").then(({ refreshDiagnosticsToProblems }) => {
        void refreshDiagnosticsToProblems(lspLang, filePath, content);
      });
    }
  }, 900);
  if (
    lang === "typescript" ||
    lang === "javascript" ||
    lang === "typescriptreact" ||
    lang === "javascriptreact"
  ) {
    if (eslintDebounceTimer.value) clearTimeout(eslintDebounceTimer.value);
    // prompt-12 12-C: longer debounce + content hash single-flight (no per-key process storm)
    eslintDebounceTimer.value = setTimeout(() => {
      void import("@/stores/toolchain").then(({ runToolchainCommandQuiet }) => {
        void runToolchainCommandQuiet("eslint-file", filePath, content);
      });
    }, 2000);
  }
}

function handleMount(
  editor: monacoEditor.editor.IStandaloneCodeEditor,
  monaco: typeof import("monaco-editor"),
) {
  if (editorInstance.value) disposeEditorMountResources();
  editorInstance.value = editor;
  monacoInstance.value = monaco;
  disposables.value.push(registerExtensionEditorSurface(props.path, editor, monaco));
  const debugGeneration = ++debugWatchGeneration.value;
  if (
    contrastMediaListeners.length === 0 &&
    typeof window.matchMedia === "function"
  ) {
    for (const mediaQuery of [
      "(forced-colors: active)",
      "(prefers-contrast: more)",
    ]) {
      const query = window.matchMedia(mediaQuery);
      const listener = () => {
        highContrastEnabled.value = prefersHighContrast();
      };
      query.addEventListener?.("change", listener);
      contrastMediaListeners.push({ query, listener });
    }
  }
  // Enable glyph margin for breakpoints
  editor.updateOptions({ glyphMargin: true });
  cursorListener.value = editor.onDidChangeCursorPosition(
    (e: monacoEditor.editor.ICursorPositionChangedEvent) => {
      emit("cursor-change", e.position.lineNumber, e.position.column);
      scheduleInlineBlame(editor, monaco, e.position.lineNumber);
    },
  );
  disposables.value.push(editor.onDidChangeCursorSelection(() => {
    const selections = editor.getSelections()?.map((value) => new Selection(
      new Position(value.selectionStartLineNumber - 1, value.selectionStartColumn - 1),
      new Position(value.positionLineNumber - 1, value.positionColumn - 1),
    )) ?? [];
    notifyExtensionHostTextEditorSelectionChange(props.path, selections);
  }));
  // Click glyph margin to toggle breakpoint (prompt-11 11-A)
  // M-12: 跟踪 onMouseDown 返回的 IDisposable，卸载时统一释放。
  disposables.value.push(
    editor.onMouseDown((e) => {
      if (e.target.type === monaco.editor.MouseTargetType.GUTTER_GLYPH_MARGIN) {
        const line = e.target.position?.lineNumber;
        const filePath = props.path || appState.currentFilePath || "";
        if (line && filePath) {
          void import("@/stores/debug").then(({ toggleBreakpoint }) => {
            void toggleBreakpoint(filePath, line).then(() =>
              applyBreakpointDecorations(editor, monaco),
            );
          });
        }
      }
    }),
  );
  registerContextMenuActions(editor, monaco, disposables.value);
  disposables.value.push(
    ...registerEditorCommands(editor, monaco, {
      labels: {
        addSelectionToNextMatch: t("editorCommands.addSelectionToNextMatch"),
        selectAllMatches: t("editorCommands.selectAllMatches"),
        insertCursorAbove: t("editorCommands.insertCursorAbove"),
        insertCursorBelow: t("editorCommands.insertCursorBelow"),
        insertCursorAtLineEnds: t("editorCommands.insertCursorAtLineEnds"),
        cursorUndo: t("editorCommands.cursorUndo"),
        splitHorizontal: t("editorCommands.splitHorizontal"),
        splitVertical: t("editorCommands.splitVertical"),
      },
      splitEditor: (orientation) => {
        const sourceGroupId = props.groupId ?? layoutState.tree.activeLeafId;
        if (!sourceGroupId || !splitLeaf(sourceGroupId, orientation, "editor"))
          return;
        const targetGroupId = layoutState.tree.activeLeafId;
        if (targetGroupId) cloneEditorGroup(sourceGroupId, targetGroupId, true);
      },
    }),
  );
  inlineCompletionProvider.value = registerInlineCompletionProvider(
    editor,
    monaco,
  );
  // G-FEAT-02: register LSP-backed popup completion + hover providers. These
  // coexist with the AI inline completion above because they use a different
  // Monaco API (registerCompletionItemProvider vs registerInlineCompletionsProvider).
  // prompt-8 Task 8-C: pass the current absolute open-file path. Replacing the
  // lease also handles a Monaco remount without retaining the previous path.
  replaceLSPProviders(props.path);
  emmetProvidersDisposable.value?.dispose();
  emmetProvidersDisposable.value = registerEmmetProviders(monaco, {
    enabled: appState.emmetEnabled,
    includeLanguages: appState.emmetIncludeLanguages,
  });
  // prompt-10 10-H: coverage gutter
  applyCoverageDecorations(editor, monaco);
  applyBreakpointDecorations(editor, monaco);
  applyDebugLineDecoration(editor, monaco);
  coverageWatchStop.value?.();
  coverageWatchStop.value = watch(
    () => [
      props.path,
      ...coverageState.hits.map(
        (hit) =>
          `${hit.file}:${hit.line}:${hit.status ?? ""}:${hit.coveredCount ?? ""}:${hit.totalCount ?? ""}:${hit.covered}`,
      ),
    ],
    () => applyCoverageDecorations(editor, monaco),
  );
  void import("@/stores/debug").then(({ debugState }) => {
    if (
      debugGeneration !== debugWatchGeneration.value ||
      toRaw(editorInstance.value) !== editor
    )
      return;
    debugWatchStop.value?.();
    debugWatchStop.value = watch(
      () =>
        [
          debugState.breakpoints.length,
          debugState.stopped,
          debugState.stack.length,
        ] as const,
      () => {
        applyBreakpointDecorations(editor, monaco);
        applyDebugLineDecoration(editor, monaco);
      },
    );
  });
  blameWatchStop.value?.();
  blameWatchStop.value = watch(
    () => [appState.gitBlameEnabled, currentGitRepoRoot(), props.path] as const,
    () => scheduleInlineBlame(editor, monaco),
    { immediate: true },
  );
}

watch(
  () => props.path,
  async (path) => {
    const previousModel = editorInstance.value?.getModel() ?? null;
    replaceLSPProviders(path);
    await nextTick();
    if (
      previousModel &&
      editorInstance.value?.getModel() !== previousModel &&
      !previousModel.isDisposed?.() &&
      !(previousModel.isAttachedToEditor?.() ?? false)
    ) {
      previousModel.dispose?.();
    }
  },
  { flush: "sync" },
);

watch(
  () =>
    [
      appState.emmetEnabled,
      JSON.stringify(appState.emmetIncludeLanguages),
    ] as const,
  () => {
    const monaco = monacoInstance.value;
    if (!monaco) return;
    emmetProvidersDisposable.value?.dispose();
    emmetProvidersDisposable.value = registerEmmetProviders(monaco, {
      enabled: appState.emmetEnabled,
      includeLanguages: appState.emmetIncludeLanguages,
    });
  },
);

watch(
  options,
  (value) => {
    editorInstance.value?.updateOptions(value);
  },
  { deep: true },
);

// prompt-10 10-D: jump caret when Problems / external nav requests it
function normalizedEditorPath(path: string): string {
  return path.replace(/\\/g, "/").toLowerCase();
}

watch(
  () => appState.editorJumpSeq,
  (seq) => {
    const ed = editorInstance.value;
    if (!ed || !seq) return;
    if (appState.editorJumpTargetSeq === seq) {
      if (
        appState.editorJumpTargetPath &&
        normalizedEditorPath(appState.editorJumpTargetPath) !==
          normalizedEditorPath(props.path)
      )
        return;
      if (
        appState.editorJumpTargetGroupId &&
        appState.editorJumpTargetGroupId !== props.groupId
      )
        return;
    } else if (
      props.groupId &&
      props.groupId !== layoutState.tree.activeLeafId
    ) {
      // Legacy callers only update cursorLine/cursorColumn/editorJumpSeq.
      return;
    }
    const line = Math.max(1, appState.cursorLine || 1);
    const col = Math.max(1, appState.cursorColumn || 1);
    ed.focus();
    ed.setPosition({ lineNumber: line, column: col });
    ed.revealLineInCenter(line);
  },
);

onBeforeUnmount(() => {
  componentUnmounting.value = true;
  const activeEditor = toRaw(editorInstance.value);
  const activeModel = activeEditor?.getModel() ?? null;
  for (const { query, listener } of contrastMediaListeners.splice(0)) {
    query.removeEventListener?.("change", listener);
  }
  disposeEditorMountResources();
  try {
    activeEditor?.setModel?.(null);
  } catch {
    // The editor may already have been disposed by the wrapper.
  }
  if (
    activeModel &&
    !activeModel.isDisposed?.() &&
    !(activeModel.isAttachedToEditor?.() ?? false)
  ) {
    activeModel.dispose?.();
  }
});

function handleChange(value: string | undefined) {
  const v = value ?? "";
  emit("update:content", v);
  // prompt-11 11-D / 11-J: live diagnostics (debounced)
  scheduleLiveDiagnostics(v);
}
</script>

<template>
  <div class="code-editor">
    <VueMonacoEditor
      :value="content"
      :path="monacoModelPath"
      :language="resolvedLanguage"
      :theme="monacoTheme"
      :options="options"
      @mount="handleMount"
      @change="handleChange"
    />
  </div>
</template>

<style scoped>
.code-editor {
  width: 100%;
  height: 100%;
}

.code-editor :deep(.monaco-editor) {
  background-color: var(--color-bg-base);
}

.code-editor :deep(.monaco-editor .margin) {
  background-color: var(--color-bg-base);
}
</style>
