import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";

const { readFileMock, writeFileIfUnchangedMock, notifySuccessMock } = vi.hoisted(() => ({
  readFileMock: vi.fn(),
  writeFileIfUnchangedMock: vi.fn(),
  notifySuccessMock: vi.fn(),
}));

vi.mock("@/lib/monaco-themes", () => ({
  accentThemes: {
    blue: { label: "Blue", color: "#4285f4", monacoTheme: "koyoriIde-blue", monacoLightTheme: "koyoriIde-light-blue" },
  },
  applyMonacoTheme: vi.fn(),
  applyMonacoThemeForMode: vi.fn(),
  registerAllThemes: vi.fn(),
  registerCustomTheme: vi.fn(),
}));

vi.mock("@/api/services", () => ({
  settingsService: {
    loadSettings: vi.fn().mockResolvedValue({}),
    saveSettings: vi.fn().mockResolvedValue(undefined),
  },
  fileService: {
    readFile: readFileMock,
    writeFile: vi.fn().mockResolvedValue(undefined),
    writeFileIfUnchanged: writeFileIfUnchangedMock,
  },
  lspService: {
    organizeImports: vi.fn().mockResolvedValue([]),
    didSaveDocument: vi.fn().mockResolvedValue(undefined),
  },
}));

vi.mock("@wailsio/runtime", () => ({
  Events: { On: vi.fn() },
}));

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({
    t: (key: string) => key,
  }),
  translate: (key: string) => key,
}));

vi.mock("@/lib/notifications", () => ({
  notifyError: vi.fn(),
  notifyWarning: vi.fn(),
  notifySuccess: notifySuccessMock,
}));

vi.mock("@/lib/markdown", () => ({
  renderMarkdown: (content: string) => content,
}));

vi.mock("@/lib/vscodeExtensionActivation", () => ({
  activateOnLanguage: vi.fn(),
}));

vi.mock("@/stores/lsp", () => ({
  closeLSPDocument: vi.fn(),
  refreshDiagnosticsToProblems: vi.fn(),
}));

vi.mock("@/components/editor/CodeEditor.vue", () => ({
  default: {
    name: "CodeEditor",
    props: ["path", "content", "language", "groupId"],
    template: '<div class="code-editor-stub" />',
  },
}));

vi.mock("@/components/layout/BreadcrumbBar.vue", () => ({
  default: { name: "BreadcrumbBar", template: '<div class="breadcrumb-stub" />' },
}));

vi.mock("@/components/common/MarkdownContent.vue", () => ({
  default: { name: "MarkdownContent", template: '<div class="markdown-stub" />' },
}));

import EditorView from "./EditorView.vue";
import {
  dismissSaveConflict,
  editorState,
  openFile,
  saveConflictState,
  updateContent,
} from "@/stores/editor";
import { appState } from "@/stores/app";

const TAB_DRAG_TYPE = "application/x-koyori-ide-editor-tab";
const wrappers: VueWrapper[] = [];

function mountView(groupId: string, active: boolean) {
  const wrapper = mount(EditorView, {
    props: { groupId, active },
    global: {
      stubs: {
        "el-icon": true,
      },
    },
  });
  wrappers.push(wrapper);
  return wrapper;
}

describe("EditorView editor groups", () => {
  beforeEach(() => {
    editorState.openFiles = [];
    editorState.activeFilePath = null;
    appState.editorGroupFilePaths = {};
    appState.editorGroupActiveFiles = {};
    appState.currentFilePath = null;
    appState.autoSave = false;
    readFileMock.mockReset();
    writeFileIfUnchangedMock.mockReset();
    writeFileIfUnchangedMock.mockResolvedValue(undefined);
    notifySuccessMock.mockClear();
    dismissSaveConflict();
    vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
      callback(0);
      return 1;
    });
  });

  afterEach(() => {
    while (wrappers.length > 0) wrappers.pop()?.unmount();
    vi.unstubAllGlobals();
  });

  it("accepts an editor tab dropped on an empty group", async () => {
    openFile("/src/a.ts", "const a = 1");
    editorState.activeFilePath = null;
    appState.editorGroupFilePaths.left = ["/src/a.ts"];
    appState.editorGroupActiveFiles.left = "/src/a.ts";
    appState.editorGroupFilePaths.right = [];
    appState.editorGroupActiveFiles.right = null;

    const wrapper = mountView("right", true);
    const payload = JSON.stringify({ path: "/src/a.ts", sourceGroupId: "left" });
    const dataTransfer = {
      types: [TAB_DRAG_TYPE],
      files: [] as File[],
      dropEffect: "none",
      getData: (type: string) => type === TAB_DRAG_TYPE ? payload : "",
    };

    await wrapper.get(".editor-view").trigger("dragover", { dataTransfer });
    expect(dataTransfer.dropEffect).toBe("move");
    await wrapper.get(".editor-view").trigger("drop", { dataTransfer });

    expect(appState.editorGroupFilePaths.left).toEqual([]);
    expect(appState.editorGroupFilePaths.right).toEqual(["/src/a.ts"]);
    expect(appState.editorGroupActiveFiles.right).toBe("/src/a.ts");
    expect(editorState.activeFilePath).toBe("/src/a.ts");
    expect(appState.currentFilePath).toBe("/src/a.ts");
  });

  it("keeps native file drops working", async () => {
    appState.editorGroupFilePaths.right = [];
    appState.editorGroupActiveFiles.right = null;
    readFileMock.mockResolvedValue("from disk");
    const wrapper = mountView("right", true);
    const file = new File(["from disk"], "native.ts", { type: "text/plain" });
    Object.defineProperty(file, "path", { value: "/src/native.ts" });
    const dataTransfer = {
      types: ["Files"],
      files: [file],
      dropEffect: "none",
      getData: () => "",
    };

    await wrapper.get(".editor-view").trigger("dragover", { dataTransfer });
    expect(dataTransfer.dropEffect).toBe("copy");
    expect(wrapper.find(".editor-view__drop-overlay").exists()).toBe(true);
    await wrapper.get(".editor-view").trigger("drop", { dataTransfer });
    await flushPromises();

    expect(readFileMock).toHaveBeenCalledWith("/src/native.ts");
    expect(appState.editorGroupFilePaths.right).toEqual(["/src/native.ts"]);
    expect(editorState.activeFilePath).toBe("/src/native.ts");
    expect(appState.currentFilePath).toBe("/src/native.ts");
    expect(notifySuccessMock).toHaveBeenCalledTimes(1);
  });

  it("syncs both active path fields when selecting a tab", async () => {
    openFile("/src/a.ts", "a");
    openFile("/src/b.ts", "b");
    appState.editorGroupFilePaths.left = ["/src/a.ts", "/src/b.ts"];
    appState.editorGroupActiveFiles.left = "/src/a.ts";
    editorState.activeFilePath = "/src/a.ts";
    appState.currentFilePath = "/stale.ts";
    const wrapper = mountView("left", true);

    await wrapper.findAll(".tab-bar__tab")[1].trigger("click");

    expect(editorState.activeFilePath).toBe("/src/b.ts");
    expect(appState.currentFilePath).toBe("/src/b.ts");
  });

  it("syncs a file opened externally into the active group", async () => {
    appState.editorGroupFilePaths.left = [];
    appState.editorGroupActiveFiles.left = null;
    mountView("left", true);

    openFile("/src/external.ts", "external");
    await flushPromises();

    expect(appState.editorGroupFilePaths.left).toEqual(["/src/external.ts"]);
    expect(appState.editorGroupActiveFiles.left).toBe("/src/external.ts");
    expect(editorState.activeFilePath).toBe("/src/external.ts");
    expect(appState.currentFilePath).toBe("/src/external.ts");
  });

  it("clears both active path fields after closing the final tab", async () => {
    openFile("/src/a.ts", "a");
    appState.editorGroupFilePaths.left = ["/src/a.ts"];
    appState.editorGroupActiveFiles.left = "/src/a.ts";
    const wrapper = mountView("left", true);

    await wrapper.get(".tab-bar__close").trigger("click");
    await flushPromises();

    expect(appState.editorGroupFilePaths.left).toEqual([]);
    expect(editorState.activeFilePath).toBeNull();
    expect(appState.currentFilePath).toBeNull();
  });

  it("syncs the newly active group, including an empty group", async () => {
    openFile("/src/a.ts", "a");
    openFile("/src/b.ts", "b");
    appState.editorGroupFilePaths.left = ["/src/a.ts"];
    appState.editorGroupActiveFiles.left = "/src/a.ts";
    appState.editorGroupFilePaths.right = ["/src/b.ts"];
    appState.editorGroupActiveFiles.right = "/src/b.ts";
    editorState.activeFilePath = "/src/a.ts";
    appState.currentFilePath = "/src/a.ts";
    const wrapper = mountView("right", false);

    await wrapper.setProps({ active: true });
    expect(editorState.activeFilePath).toBe("/src/b.ts");
    expect(appState.currentFilePath).toBe("/src/b.ts");

    appState.editorGroupFilePaths.right = [];
    appState.editorGroupActiveFiles.right = null;
    await flushPromises();
    expect(editorState.activeFilePath).toBeNull();
    expect(appState.currentFilePath).toBeNull();
  });

  it("offers an explicit overwrite action for a baseline conflict", async () => {
    writeFileIfUnchangedMock
      .mockRejectedValueOnce(new Error("file changed on disk since it was opened"))
      .mockResolvedValueOnce(undefined);
    readFileMock.mockResolvedValueOnce("external version");
    openFile("/src/a.ts", "original");
    updateContent("/src/a.ts", "local version");
    appState.editorGroupFilePaths.left = ["/src/a.ts"];
    appState.editorGroupActiveFiles.left = "/src/a.ts";
    const wrapper = mountView("left", true);

    await wrapper.get(".editor-view").trigger("keydown", { ctrlKey: true, key: "s" });
    await vi.waitFor(() => expect(saveConflictState.visible).toBe(true));
    const overwrite = document.querySelector<HTMLButtonElement>(
      ".editor-save-conflict__overwrite",
    );
    expect(overwrite).not.toBeNull();
    overwrite?.click();
    await vi.waitFor(() => expect(saveConflictState.visible).toBe(false));

    expect(writeFileIfUnchangedMock).toHaveBeenCalledTimes(2);
    expect(editorState.openFiles[0].isDirty).toBe(false);
  });

  it("offers an explicit reload action that keeps the latest disk version", async () => {
    writeFileIfUnchangedMock.mockRejectedValueOnce(
      new Error("file changed on disk since it was opened"),
    );
    readFileMock
      .mockResolvedValueOnce("external version at conflict")
      .mockResolvedValueOnce("latest external version");
    openFile("/src/a.ts", "original");
    updateContent("/src/a.ts", "local version");
    appState.editorGroupFilePaths.left = ["/src/a.ts"];
    appState.editorGroupActiveFiles.left = "/src/a.ts";
    const wrapper = mountView("left", true);

    await wrapper.get(".editor-view").trigger("keydown", { ctrlKey: true, key: "s" });
    await vi.waitFor(() => expect(saveConflictState.visible).toBe(true));
    const reload = document.querySelector<HTMLButtonElement>(
      ".editor-save-conflict__reload",
    );
    expect(reload).not.toBeNull();
    reload?.click();
    await vi.waitFor(() => expect(saveConflictState.visible).toBe(false));

    expect(editorState.openFiles[0]).toMatchObject({
      content: "latest external version",
      originalContent: "latest external version",
      isDirty: false,
    });
  });
});
