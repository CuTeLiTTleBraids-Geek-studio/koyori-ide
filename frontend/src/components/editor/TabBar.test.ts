import { describe, it, expect, beforeEach, vi } from "vitest";
import { mount } from "@vue/test-utils";
import type { App } from "vue";
import ElementPlus from "element-plus";
import * as ElementPlusIconsVue from "@element-plus/icons-vue";

// Mock monaco-themes so importing @/lib/i18n -> @/stores/app doesn't pull
// in the real Monaco editor (which calls document.queryCommandSupported,
// unavailable in jsdom). Same pattern as app.test.ts.
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
}));

vi.mock("@wailsio/runtime", () => ({
  Events: { On: vi.fn() },
}));

import TabBar from "./TabBar.vue";
import { editorState, openFile, updateContent } from "@/stores/editor";
import {
  activateEditorGroupFile,
  appState,
} from "@/stores/app";

const iconPlugin = {
  install(app: App) {
    for (const [key, component] of Object.entries(ElementPlusIconsVue)) {
      app.component(key, component);
    }
  },
};

function mountBar(groupId = "primary-editor-group") {
  return mount(TabBar, {
    props: { groupId },
    global: {
      plugins: [ElementPlus, iconPlugin],
    },
  });
}

describe("TabBar", () => {
  beforeEach(() => {
    editorState.openFiles = [];
    editorState.activeFilePath = null;
    appState.editorGroupFilePaths = {};
    appState.editorGroupActiveFiles = {};
  });

  it("keeps an empty drop zone when there are no open files", () => {
    const wrapper = mountBar();
    const tabBar = wrapper.get(".tab-bar");
    expect(tabBar.attributes("data-editor-group")).toBe("primary-editor-group");
    expect(wrapper.findAll(".tab-bar__tab")).toHaveLength(0);
  });

  it("renders a tab for each open file", () => {
    openFile("/src/a.ts", "a");
    openFile("/src/b.ts", "b");
    const wrapper = mountBar();
    expect(wrapper.findAll(".tab-bar__tab")).toHaveLength(2);
  });

  it("exposes tabs with keyboard navigation semantics", async () => {
    openFile("/src/a.ts", "a");
    openFile("/src/b.ts", "b");
    const wrapper = mountBar();
    expect(wrapper.get(".tab-bar").attributes("role")).toBe("tablist");
    const tabs = wrapper.findAll('[role="tab"]');
    expect(tabs[1].attributes("aria-selected")).toBe("true");
    expect(tabs[0].attributes("tabindex")).toBe("-1");
    expect(tabs[1].attributes("tabindex")).toBe("0");

    await tabs[0].trigger("keydown", { key: "Enter" });
    expect(wrapper.emitted("select")?.at(-1)).toEqual(["/src/a.ts"]);
  });

  it("displays the file name in each tab", () => {
    openFile("/src/app.ts", "x");
    const wrapper = mountBar();
    expect(wrapper.find(".tab-bar__name").text()).toBe("app.ts");
  });

  it("applies active class to the active tab", () => {
    openFile("/src/a.ts", "a");
    openFile("/src/b.ts", "b");
    const wrapper = mountBar();
    const tabs = wrapper.findAll(".tab-bar__tab");
    expect(tabs[0].classes()).not.toContain("tab-bar__tab--active");
    expect(tabs[1].classes()).toContain("tab-bar__tab--active");
  });

  it("shows dirty indicator when file is dirty", () => {
    openFile("/src/a.ts", "original");
    updateContent("/src/a.ts", "changed");
    const wrapper = mountBar();
    expect(wrapper.find(".tab-bar__dirty").exists()).toBe(true);
  });

  it("does not show dirty indicator when file is clean", () => {
    openFile("/src/a.ts", "original");
    const wrapper = mountBar();
    expect(wrapper.find(".tab-bar__dirty").exists()).toBe(false);
  });

  it("emits select with the path when a tab is clicked", async () => {
    openFile("/src/a.ts", "a");
    openFile("/src/b.ts", "b");
    const wrapper = mountBar();
    const tabs = wrapper.findAll('[role="tab"]');
    await tabs[0].trigger("click");
    const selectEvents = wrapper.emitted("select");
    expect(selectEvents).toBeTruthy();
    expect(selectEvents![0]).toEqual(["/src/a.ts"]);
  });

  it("emits close with the path when the close button is clicked", async () => {
    openFile("/src/a.ts", "a");
    const wrapper = mountBar();
    await wrapper.find(".tab-bar__close").trigger("click");
    const closeEvents = wrapper.emitted("close");
    expect(closeEvents).toBeTruthy();
    expect(closeEvents![0]).toEqual(["/src/a.ts"]);
  });

  it("does not emit select when the close button is clicked", async () => {
    openFile("/src/a.ts", "a");
    const wrapper = mountBar();
    await wrapper.find(".tab-bar__close").trigger("click");
    expect(wrapper.emitted("select")).toBeFalsy();
  });

  it("keeps the close button outside the tab control", () => {
    openFile("/src/a.ts", "a");
    const wrapper = mountBar();
    const tab = wrapper.get('[role="tab"]');
    const close = wrapper.get(".tab-bar__close");
    expect(tab.element.contains(close.element)).toBe(false);

    const space = new KeyboardEvent("keydown", {
      key: " ",
      bubbles: true,
      cancelable: true,
    });
    close.element.dispatchEvent(space);
    expect(space.defaultPrevented).toBe(false);
    expect(wrapper.emitted("select")).toBeFalsy();
  });

  it("keeps different active files for two editor groups", () => {
    openFile("/src/a.ts", "a");
    openFile("/src/b.ts", "b");
    appState.editorGroupFilePaths.left = ["/src/a.ts", "/src/b.ts"];
    appState.editorGroupFilePaths.right = ["/src/a.ts", "/src/b.ts"];
    activateEditorGroupFile("left", "/src/a.ts");
    activateEditorGroupFile("right", "/src/b.ts");

    const left = mountBar("left");
    const right = mountBar("right");

    expect(left.find(".tab-bar__tab--active .tab-bar__name").text()).toBe("a.ts");
    expect(right.find(".tab-bar__tab--active .tab-bar__name").text()).toBe("b.ts");
  });

  it("moves a dragged tab between editor groups", async () => {
    openFile("/src/a.ts", "a");
    openFile("/src/b.ts", "b");
    appState.editorGroupFilePaths.left = ["/src/a.ts"];
    appState.editorGroupActiveFiles.left = "/src/a.ts";
    appState.editorGroupFilePaths.right = ["/src/b.ts"];
    appState.editorGroupActiveFiles.right = "/src/b.ts";

    const left = mountBar("left");
    const right = mountBar("right");
    const data = new Map<string, string>();
    const dataTransfer = {
      effectAllowed: "none",
      setData: (type: string, value: string) => data.set(type, value),
      getData: (type: string) => data.get(type) ?? "",
    };

    await left.get(".tab-bar__tab").trigger("dragstart", { dataTransfer });
    await right.get(".tab-bar").trigger("drop", { dataTransfer });

    expect(appState.editorGroupFilePaths.left).toEqual([]);
    expect(appState.editorGroupFilePaths.right).toEqual(["/src/b.ts", "/src/a.ts"]);
    expect(appState.editorGroupActiveFiles.right).toBe("/src/a.ts");
    expect(right.emitted("select")?.[0]).toEqual(["/src/a.ts"]);
  });

  it("moves a dragged tab into an empty editor group", async () => {
    openFile("/src/a.ts", "a");
    appState.editorGroupFilePaths.left = ["/src/a.ts"];
    appState.editorGroupActiveFiles.left = "/src/a.ts";
    appState.editorGroupFilePaths.right = [];
    appState.editorGroupActiveFiles.right = null;

    const left = mountBar("left");
    const right = mountBar("right");
    const data = new Map<string, string>();
    const dataTransfer = {
      effectAllowed: "none",
      setData: (type: string, value: string) => data.set(type, value),
      getData: (type: string) => data.get(type) ?? "",
    };

    expect(right.get(".tab-bar").attributes("data-editor-group")).toBe("right");
    expect(right.findAll(".tab-bar__tab")).toHaveLength(0);

    await left.get(".tab-bar__tab").trigger("dragstart", { dataTransfer });
    await right.get(".tab-bar").trigger("drop", { dataTransfer });

    expect(appState.editorGroupFilePaths.left).toEqual([]);
    expect(appState.editorGroupFilePaths.right).toEqual(["/src/a.ts"]);
    expect(appState.editorGroupActiveFiles.right).toBe("/src/a.ts");
    expect(right.emitted("select")?.[0]).toEqual(["/src/a.ts"]);
  });
});
