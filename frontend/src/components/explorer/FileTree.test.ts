import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { mount, flushPromises, type VueWrapper } from "@vue/test-utils";
import type { App } from "vue";
import ElementPlus from "element-plus";
import { ElMessageBox } from "element-plus";
import * as ElementPlusIconsVue from "@element-plus/icons-vue";
import FileTree from "./FileTree.vue";
import { fileService, projectService } from "@/api/services";
import type { DirEntry } from "@/types";
import { notifyFileTreeRefresh } from "@/stores/fileTreeRefresh";

vi.mock("@/api/services", () => ({
  fileService: {
    listDirectory: vi.fn(),
    createFile: vi.fn().mockResolvedValue(undefined),
    createDirectory: vi.fn().mockResolvedValue(undefined),
    renamePath: vi.fn().mockResolvedValue(undefined),
    deletePath: vi.fn().mockResolvedValue(undefined),
    revealInOS: vi.fn().mockResolvedValue(undefined),
  },
  projectService: {
    createProject: vi.fn().mockResolvedValue("/root/vue-project"),
  },
}));

vi.mock("@/stores/terminal", () => ({
  createSession: vi.fn().mockResolvedValue("session-1"),
}));

vi.mock("@/stores/app", () => ({
  appState: { terminalVisible: false, currentProject: null },
  removeFileFromAllEditorGroups: vi.fn(),
}));

vi.mock("@/stores/editor", () => ({
  editorState: { openFiles: [], activeFilePath: null },
  closeFile: vi.fn(),
  closeFilesUnder: vi.fn(),
  renameOpenFilesUnder: vi.fn(),
}));

import { closeFilesUnder, renameOpenFilesUnder } from "@/stores/editor";

const iconPlugin = {
  install(app: App) {
    for (const [key, component] of Object.entries(ElementPlusIconsVue)) {
      app.component(key, component);
    }
  },
};

const mountedWrappers: VueWrapper[] = [];

function makeEntry(name: string, path: string, isDir: boolean): DirEntry {
  return { name, path, isDir, size: 0, modified: 0 };
}

function mountTree(props: Partial<{ path: string; name: string; depth: number; isDir: boolean }> = {}) {
  const wrapper = mount(FileTree, {
    props: { path: "/root", name: "root", ...props },
    global: {
      plugins: [ElementPlus, iconPlugin],
    },
  });
  mountedWrappers.push(wrapper);
  return wrapper;
}

describe("FileTree", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(fileService.listDirectory).mockResolvedValue([]);
  });

  afterEach(() => {
    for (const wrapper of mountedWrappers.splice(0)) {
      if (wrapper.exists()) wrapper.unmount();
    }
    vi.useRealTimers();
  });

  it("renders the node name", () => {
    const wrapper = mountTree({ name: "my-project" });
    expect(wrapper.find(".file-tree__name").text()).toBe("my-project");
  });

  it("protects the workspace root from rename and delete", async () => {
    const wrapper = mountTree();
    await wrapper.find(".file-tree__row").trigger("contextmenu");
    await flushPromises();

    const labels = Array.from(document.body.querySelectorAll(".ctx-item"), (item) => item.textContent);
    expect(labels).not.toContain("Rename");
    expect(labels).not.toContain("Delete");
    wrapper.unmount();
  });

  it("closes the context menu on outside pointerdown and Escape", async () => {
    const wrapper = mountTree();
    await wrapper.find(".file-tree__row").trigger("contextmenu");
    await flushPromises();
    expect(document.body.querySelector(".file-tree__context-menu")).toBeTruthy();

    window.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true }));
    await flushPromises();
    expect(document.body.querySelector(".file-tree__context-menu")).toBeNull();

    await wrapper.find(".file-tree__row").trigger("contextmenu");
    await flushPromises();
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    await flushPromises();
    expect(document.body.querySelector(".file-tree__context-menu")).toBeNull();
    wrapper.unmount();
  });

  it("removes a deleted child from the parent tree after confirm", async () => {
    vi.mocked(fileService.listDirectory).mockResolvedValue([
      makeEntry("file.ts", "/root/file.ts", false),
      makeEntry("keep.ts", "/root/keep.ts", false),
    ]);
    const wrapper = mountTree();
    await wrapper.find(".file-tree__row").trigger("click");
    await flushPromises();
    expect(wrapper.text()).toContain("file.ts");

    vi.spyOn(ElMessageBox, "confirm").mockResolvedValueOnce("confirm" as never);
    const child = wrapper.findAllComponents(FileTree)
      .find((component) => component.props("path") === "/root/file.ts");
    expect(child).toBeTruthy();
    await child!.find(".file-tree__row").trigger("contextmenu");
    await flushPromises();
    const deleteButton = Array.from(document.body.querySelectorAll<HTMLButtonElement>(".ctx-item"))
      .find((button) => button.textContent === "Delete");
    deleteButton?.click();
    await flushPromises();

    expect(fileService.deletePath).toHaveBeenCalledWith("/root/file.ts");
    expect(closeFilesUnder).toHaveBeenCalledWith("/root/file.ts");
    expect(wrapper.text()).not.toContain("file.ts");
    expect(wrapper.text()).toContain("keep.ts");
    wrapper.unmount();
  });

  it("renames open editor paths and removes the old tree path", async () => {
    vi.mocked(fileService.listDirectory).mockResolvedValue([
      makeEntry("old.ts", "/root/old.ts", false),
    ]);
    const wrapper = mountTree();
    await wrapper.find(".file-tree__row").trigger("click");
    await flushPromises();
    vi.spyOn(ElMessageBox, "prompt").mockResolvedValueOnce({ value: "new.ts" } as never);

    const child = wrapper.findAllComponents(FileTree)
      .find((component) => component.props("path") === "/root/old.ts");
    await child!.find(".file-tree__row").trigger("contextmenu");
    const renameButton = Array.from(document.body.querySelectorAll<HTMLButtonElement>(".ctx-item"))
      .find((button) => button.textContent === "Rename");
    renameButton?.click();
    await flushPromises();

    expect(fileService.renamePath).toHaveBeenCalledWith("/root/old.ts", "/root/new.ts");
    expect(renameOpenFilesUnder).toHaveBeenCalledWith("/root/old.ts", "/root/new.ts");
    expect(wrapper.text()).not.toContain("old.ts");
    expect(wrapper.text()).toContain("new.ts");
    wrapper.unmount();
  });

  it("polls loaded folders, debounces external changes by 150ms, and cleans up", async () => {
    vi.useFakeTimers();
    vi.mocked(fileService.listDirectory)
      .mockResolvedValueOnce([makeEntry("old.ts", "/root/old.ts", false)])
      .mockResolvedValueOnce([makeEntry("new.ts", "/root/new.ts", false)]);
    const clearIntervalSpy = vi.spyOn(globalThis, "clearInterval");
    const wrapper = mountTree();
    await wrapper.find(".file-tree__row").trigger("click");
    await flushPromises();

    await vi.advanceTimersByTimeAsync(1000);
    expect(wrapper.text()).toContain("old.ts");
    await vi.advanceTimersByTimeAsync(149);
    expect(wrapper.text()).toContain("old.ts");
    await vi.advanceTimersByTimeAsync(1);
    expect(wrapper.text()).not.toContain("old.ts");
    expect(wrapper.text()).toContain("new.ts");
    expect(closeFilesUnder).toHaveBeenCalledWith("/root/old.ts");

    wrapper.unmount();
    expect(clearIntervalSpy).toHaveBeenCalled();
    clearIntervalSpy.mockRestore();
  });

  it("keeps a loaded folder synchronized while it is collapsed", async () => {
    vi.useFakeTimers();
    vi.mocked(fileService.listDirectory)
      .mockResolvedValueOnce([makeEntry("old.ts", "/root/old.ts", false)])
      .mockResolvedValueOnce([makeEntry("new.ts", "/root/new.ts", false)]);
    const wrapper = mountTree();
    const rootRow = wrapper.find(".file-tree__row");
    await rootRow.trigger("click");
    await flushPromises();
    await rootRow.trigger("click");

    await vi.advanceTimersByTimeAsync(1150);
    await rootRow.trigger("click");

    expect(wrapper.text()).not.toContain("old.ts");
    expect(wrapper.text()).toContain("new.ts");
  });

  it("refreshes a folder to reflect external filesystem changes", async () => {
    vi.mocked(fileService.listDirectory)
      .mockResolvedValueOnce([makeEntry("removed.ts", "/root/removed.ts", false)])
      .mockResolvedValueOnce([makeEntry("added.ts", "/root/added.ts", false)]);
    const wrapper = mountTree();
    await wrapper.find(".file-tree__row").trigger("click");
    await flushPromises();
    expect(wrapper.text()).toContain("removed.ts");

    await wrapper.find(".file-tree__row").trigger("contextmenu");
    await flushPromises();
    const refreshButton = Array.from(document.body.querySelectorAll<HTMLButtonElement>(".ctx-item"))
      .find((button) => button.textContent === "Refresh");
    refreshButton?.click();
    await flushPromises();

    expect(fileService.listDirectory).toHaveBeenCalledTimes(2);
    expect(wrapper.text()).not.toContain("removed.ts");
    expect(wrapper.text()).toContain("added.ts");
    wrapper.unmount();
  });

  it("creates a project template in the selected folder", async () => {
    const wrapper = mountTree();
    vi.spyOn(ElMessageBox, "prompt").mockResolvedValueOnce({ value: "client" } as never);
    await wrapper.find(".file-tree__row").trigger("contextmenu");
    await flushPromises();
    const vueButton = Array.from(document.body.querySelectorAll<HTMLButtonElement>(".ctx-item"))
      .find((button) => button.textContent === "Vue app");
    vueButton?.click();
    await flushPromises();

    expect(projectService.createProject).toHaveBeenCalledWith({
      templateId: "vue",
      projectName: "client",
      targetDir: "/root",
      moduleName: "",
    });
    wrapper.unmount();
  });

  it("applies indentation based on depth", () => {
    const wrapper = mountTree({ depth: 2 });
    const row = wrapper.find(".file-tree__row");
    expect(row.attributes("style")).toContain("padding-left: 32px");
  });

  it("expands and fetches children when a folder row is clicked", async () => {
    vi.mocked(fileService.listDirectory).mockResolvedValue([
      makeEntry("file.ts", "/root/file.ts", false),
      makeEntry("subfolder", "/root/subfolder", true),
    ]);
    const wrapper = mountTree({ path: "/root", name: "root" });

    await wrapper.find(".file-tree__row").trigger("click");
    await flushPromises();

    expect(fileService.listDirectory).toHaveBeenCalledWith("/root");
    expect(wrapper.findAll(".file-tree__children .file-tree")).toHaveLength(2);
  });

  it("refreshes loaded directories when a transactional write notifies the tree", async () => {
    vi.useFakeTimers();
    vi.mocked(fileService.listDirectory)
      .mockResolvedValueOnce([makeEntry("old.ts", "/root/old.ts", false)])
      .mockResolvedValueOnce([makeEntry("new.ts", "/root/new.ts", false)]);
    const wrapper = mountTree({ path: "/root", name: "root" });

    await wrapper.find(".file-tree__row").trigger("click");
    await flushPromises();
    expect(wrapper.text()).toContain("old.ts");

    notifyFileTreeRefresh(["/root/new.ts"]);
    await flushPromises();
    await vi.advanceTimersByTimeAsync(151);
    await flushPromises();

    expect(fileService.listDirectory).toHaveBeenCalledTimes(2);
    expect(wrapper.text()).toContain("new.ts");
    expect(wrapper.text()).not.toContain("old.ts");
  });

  it("shows loading state while fetching children", async () => {
    let resolveList!: (entries: DirEntry[]) => void;
    vi.mocked(fileService.listDirectory).mockReturnValue(
      new Promise<DirEntry[]>((resolve) => {
        resolveList = resolve;
      })
    );
    const wrapper = mountTree({ path: "/root", name: "root" });

    await wrapper.find(".file-tree__row").trigger("click");
    await flushPromises();

    expect(wrapper.find(".file-tree__loading").exists()).toBe(true);

    resolveList([]);
    await flushPromises();

    expect(wrapper.find(".file-tree__loading").exists()).toBe(false);
  });

  it("renders Folder icon for folders and Document icon for files", async () => {
    vi.mocked(fileService.listDirectory).mockResolvedValue([
      makeEntry("file.ts", "/root/file.ts", false),
      makeEntry("subfolder", "/root/subfolder", true),
    ]);
    const wrapper = mountTree({ path: "/root", name: "root" });

    await wrapper.find(".file-tree__row").trigger("click");
    await flushPromises();

    const childWrappers = wrapper.findAllComponents(FileTree);
    const fileChild = childWrappers.find((w) => w.props("name") === "file.ts");
    const folderChild = childWrappers.find((w) => w.props("name") === "subfolder");

    expect(fileChild?.findComponent({ name: "Document" }).exists()).toBe(true);
    expect(folderChild?.findComponent({ name: "Folder" }).exists()).toBe(true);
  });

  it("emits select with the file path when a file row is clicked", async () => {
    vi.mocked(fileService.listDirectory).mockResolvedValue([
      makeEntry("file.ts", "/root/file.ts", false),
    ]);
    const wrapper = mountTree({ path: "/root", name: "root" });

    await wrapper.find(".file-tree__row").trigger("click");
    await flushPromises();

    const childRow = wrapper.findAll(".file-tree__row")[1];
    await childRow.trigger("click");

    const selectEvents = wrapper.emitted("select");
    expect(selectEvents).toBeTruthy();
    expect(selectEvents![0]).toEqual(["/root/file.ts"]);
  });

  it("expands a subfolder when its chevron is clicked", async () => {
    vi.mocked(fileService.listDirectory).mockResolvedValue([
      makeEntry("subfolder", "/root/subfolder", true),
    ]);
    const wrapper = mountTree({ path: "/root", name: "root" });

    await wrapper.find(".file-tree__row").trigger("click");
    await flushPromises();
    expect(fileService.listDirectory).toHaveBeenCalledTimes(1);

    const childChevron = wrapper.find(".file-tree__children .file-tree__chevron");
    await childChevron.trigger("click");
    await flushPromises();

    expect(fileService.listDirectory).toHaveBeenCalledWith("/root/subfolder");
    expect(fileService.listDirectory).toHaveBeenCalledTimes(2);
  });

  it("does not show a chevron for files", async () => {
    vi.mocked(fileService.listDirectory).mockResolvedValue([
      makeEntry("file.ts", "/root/file.ts", false),
    ]);
    const wrapper = mountTree({ path: "/root", name: "root" });

    await wrapper.find(".file-tree__row").trigger("click");
    await flushPromises();

    const childChevron = wrapper.find(".file-tree__children .file-tree__chevron");
    expect(childChevron.exists()).toBe(false);
  });

  it("handles fetch errors by showing an error message", async () => {
    vi.mocked(fileService.listDirectory).mockRejectedValue(new Error("permission denied"));
    const wrapper = mountTree({ path: "/root", name: "root" });

    await wrapper.find(".file-tree__row").trigger("click");
    await flushPromises();

    expect(wrapper.find(".file-tree__error").exists()).toBe(true);
    expect(wrapper.find(".file-tree__error").text()).toContain("permission denied");
    expect(wrapper.find(".file-tree__children").exists()).toBe(false);
  });

  it("collapses when an expanded folder is clicked again", async () => {
    vi.mocked(fileService.listDirectory).mockResolvedValue([
      makeEntry("file.ts", "/root/file.ts", false),
    ]);
    const wrapper = mountTree({ path: "/root", name: "root" });

    await wrapper.find(".file-tree__row").trigger("click");
    await flushPromises();
    expect(wrapper.find(".file-tree__children").exists()).toBe(true);

    await wrapper.find(".file-tree__row").trigger("click");
    expect(wrapper.find(".file-tree__children").exists()).toBe(false);
  });

  it("does not refetch children when collapsing and re-expanding", async () => {
    vi.mocked(fileService.listDirectory).mockResolvedValue([
      makeEntry("file.ts", "/root/file.ts", false),
    ]);
    const wrapper = mountTree({ path: "/root", name: "root" });

    await wrapper.find(".file-tree__row").trigger("click");
    await flushPromises();
    expect(fileService.listDirectory).toHaveBeenCalledTimes(1);

    await wrapper.find(".file-tree__row").trigger("click");
    await wrapper.find(".file-tree__row").trigger("click");
    await flushPromises();

    expect(fileService.listDirectory).toHaveBeenCalledTimes(1);
  });

  it("supports Enter and Space keyboard activation on tree rows", async () => {
    vi.mocked(fileService.listDirectory).mockResolvedValue([
      makeEntry("file.ts", "/root/file.ts", false),
    ]);
    const wrapper = mountTree();
    const rootRow = wrapper.find(".file-tree__row");

    expect(rootRow.element.tagName).toBe("BUTTON");
    expect(rootRow.find("button").exists()).toBe(false);

    await rootRow.trigger("keydown", { key: "Enter" });
    await flushPromises();
    expect(wrapper.find(".file-tree__children").exists()).toBe(true);

    await rootRow.trigger("keydown", { key: " " });
    expect(wrapper.find(".file-tree__children").exists()).toBe(false);
    wrapper.unmount();
  });

  it("debounces file-tree filtering for exactly 300ms", async () => {
    vi.mocked(fileService.listDirectory).mockResolvedValue([
      makeEntry("alpha.ts", "/root/alpha.ts", false),
      makeEntry("beta.ts", "/root/beta.ts", false),
    ]);
    const wrapper = mountTree();
    await wrapper.find(".file-tree__row").trigger("click");
    await flushPromises();

    vi.useFakeTimers();
    try {
      await wrapper.find(".file-tree__search-input").setValue("alpha");
      await wrapper.vm.$nextTick();

      vi.advanceTimersByTime(299);
      await wrapper.vm.$nextTick();
      expect(wrapper.text()).toContain("beta.ts");

      vi.advanceTimersByTime(1);
      await wrapper.vm.$nextTick();
      expect(wrapper.text()).toContain("alpha.ts");
      expect(wrapper.text()).not.toContain("beta.ts");
    } finally {
      wrapper.unmount();
      vi.useRealTimers();
    }
  });

  it("propagates the debounced query through recursively expanded folders", async () => {
    vi.mocked(fileService.listDirectory).mockImplementation(async (path: string) => {
      if (path === "/root") {
        return [
          makeEntry("subfolder", "/root/subfolder", true),
          makeEntry("decoy.ts", "/root/decoy.ts", false),
        ];
      }
      if (path === "/root/subfolder") {
        return [
          makeEntry("needle.ts", "/root/subfolder/needle.ts", false),
          makeEntry("other.ts", "/root/subfolder/other.ts", false),
        ];
      }
      return [];
    });
    const wrapper = mountTree();
    await wrapper.find(".file-tree__row").trigger("click");
    await flushPromises();

    vi.useFakeTimers();
    await wrapper.find(".file-tree__search-input").setValue("needle");
    vi.advanceTimersByTime(300);
    await wrapper.vm.$nextTick();
    vi.useRealTimers();

    expect(wrapper.text()).not.toContain("decoy.ts");
    const subfolder = wrapper.findAllComponents(FileTree)
      .find((component) => component.props("path") === "/root/subfolder");
    expect(subfolder).toBeTruthy();
    await subfolder!.find(".file-tree__row").trigger("click");
    await flushPromises();

    expect(wrapper.text()).toContain("needle.ts");
    expect(wrapper.text()).not.toContain("other.ts");
    wrapper.unmount();
  });

  it("remeasures an expanded folder inside a virtual window before scrolling", async () => {
    let resizeCallback: ResizeObserverCallback | null = null;
    const disconnect = vi.fn();
    vi.stubGlobal("ResizeObserver", class {
      constructor(callback: ResizeObserverCallback) {
        resizeCallback = callback;
      }
      observe = vi.fn();
      unobserve = vi.fn();
      disconnect = disconnect;
    });

    const entries = [
      makeEntry("subfolder", "/root/subfolder", true),
      ...Array.from({ length: 1_000 }, (_, index) =>
        makeEntry(`file-${index}.ts`, `/root/file-${index}.ts`, false),
      ),
    ];
    vi.mocked(fileService.listDirectory).mockImplementation(async (path: string) => {
      if (path === "/root") return entries;
      if (path === "/root/subfolder") {
        return [
          makeEntry("nested-a.ts", "/root/subfolder/nested-a.ts", false),
          makeEntry("nested-b.ts", "/root/subfolder/nested-b.ts", false),
        ];
      }
      return [];
    });

    const wrapper = mountTree();
    try {
      await wrapper.find(".file-tree__row").trigger("click");
      await flushPromises();

      const folderItem = wrapper.find('[data-virtual-path="/root/subfolder"]');
      const nextItem = wrapper.find('[data-virtual-path="/root/file-0.ts"]');
      expect(nextItem.attributes("style")).toContain("translateY(26px)");

      await folderItem.find(".file-tree__row").trigger("click");
      await flushPromises();
      expect(folderItem.text()).toContain("nested-b.ts");

      expect(resizeCallback).not.toBeNull();
      const notifyResize = resizeCallback as unknown as ResizeObserverCallback;
      notifyResize(
        [{ target: folderItem.element, contentRect: { height: 78 } }] as unknown as ResizeObserverEntry[],
        {} as ResizeObserver,
      );
      await wrapper.vm.$nextTick();

      expect(nextItem.attributes("style")).toContain("translateY(78px)");
      expect(wrapper.find(".file-tree__virt-spacer").attributes("style")).toContain("height: 26078px");

      const viewport = wrapper.find(".file-tree__children--virtual");
      (viewport.element as HTMLElement).scrollTop = 26_078 - 400;
      await viewport.trigger("scroll");
      await wrapper.vm.$nextTick();
      expect(wrapper.text()).toContain("file-999.ts");
      expect(wrapper.findAllComponents(FileTree).length).toBeLessThan(50);

      // Returning to the top remounts the virtual row. Its recursive expansion
      // and loaded children must survive that unmount/remount cycle.
      (viewport.element as HTMLElement).scrollTop = 0;
      await viewport.trigger("scroll");
      await wrapper.vm.$nextTick();
      expect(wrapper.text()).toContain("nested-b.ts");
      expect(fileService.listDirectory).toHaveBeenCalledWith("/root/subfolder");
      expect(
        vi.mocked(fileService.listDirectory).mock.calls
          .filter(([path]) => path === "/root/subfolder"),
      ).toHaveLength(1);
    } finally {
      wrapper.unmount();
      expect(disconnect).toHaveBeenCalledTimes(1);
      vi.unstubAllGlobals();
    }
  });

  it("renders 10,000 entries within the 500ms budget using a bounded window", async () => {
    const entries = Array.from({ length: 10_000 }, (_, index) =>
      makeEntry(`file-${index}.ts`, `/root/file-${index}.ts`, false),
    );
    vi.mocked(fileService.listDirectory).mockResolvedValue(entries);
    const wrapper = mountTree();

    const started = performance.now();
    await wrapper.find(".file-tree__row").trigger("click");
    await flushPromises();
    const elapsed = performance.now() - started;

    expect(elapsed).toBeLessThan(500);
    expect(wrapper.find(".file-tree__children--virtual").exists()).toBe(true);
    expect(wrapper.findAllComponents(FileTree).length).toBeLessThan(50);
    expect(wrapper.text()).toContain("file-0.ts");
    expect(wrapper.text()).not.toContain("file-9999.ts");

    const viewport = wrapper.find(".file-tree__children--virtual");
    (viewport.element as HTMLElement).scrollTop = entries.length * 26 - 400;
    await viewport.trigger("scroll");
    await wrapper.vm.$nextTick();
    expect(wrapper.text()).toContain("file-9999.ts");
    expect(wrapper.findAllComponents(FileTree).length).toBeLessThan(50);
    wrapper.unmount();
  });
});
