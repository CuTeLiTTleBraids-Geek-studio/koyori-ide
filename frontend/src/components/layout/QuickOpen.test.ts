import { describe, it, expect, beforeEach, vi } from "vitest";
import { mount } from "@vue/test-utils";
import type { App } from "vue";
import ElementPlus from "element-plus";

// Use vi.hoisted so the mock factories can reference these variables —
// vi.mock calls are hoisted to the top of the file, before any const
// declarations, so plain top-level consts would be in the temporal dead zone.
const {
  mockAppState,
  listAllFilesMock,
  readFileMock,
  openFileMock,
  notifyErrorMock,
} = vi.hoisted(() => ({
  mockAppState: {
    currentProject: "/proj",
  },
  listAllFilesMock: vi.fn().mockResolvedValue([
    "src/main.ts",
    "src/util/helper.go",
    "README.md",
  ]),
  readFileMock: vi.fn().mockResolvedValue("file content"),
  openFileMock: vi.fn(),
  notifyErrorMock: vi.fn(),
}));

vi.mock("@/stores/app", () => ({
  appState: mockAppState,
}));

vi.mock("@/stores/editor", () => ({
  openFile: openFileMock,
}));

vi.mock("@/api/services", () => ({
  fileService: {
    listAllFiles: listAllFilesMock,
    readFile: readFileMock,
  },
}));

vi.mock("@/lib/errors", () => ({
  errorMessage: (e: unknown) => (e instanceof Error ? e.message : String(e)),
}));

vi.mock("@/lib/notifications", () => ({
  notifyError: notifyErrorMock,
}));

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({
    t: (key: string) => {
      const map: Record<string, string> = {
        "quickOpen.placeholder": "Search files by name...",
        "quickOpen.loading": "Loading files...",
        "quickOpen.noFiles": "No matching files",
        "quickOpen.errorLoad": "Failed to load file list",
        "quickOpen.errorOpen": "Failed to open file",
      };
      return map[key] ?? key;
    },
    locale: { value: "en" },
  }),
}));

const iconPlugin = {
  install(_app: App) {
    // ElementPlus icons not needed for this test; no-op.
  },
};

// Import the component AFTER the mocks are set up.
const QuickOpenModule = await import("./QuickOpen.vue");
const QuickOpen = QuickOpenModule.default;

function mountQuickOpen(visible = true) {
  return mount(QuickOpen, {
    props: { visible },
    global: {
      plugins: [ElementPlus, iconPlugin],
    },
  });
}

describe("QuickOpen (Plan 55)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    listAllFilesMock.mockResolvedValue([
      "src/main.ts",
      "src/util/helper.go",
      "README.md",
    ]);
    readFileMock.mockResolvedValue("file content");
  });

  it("does not render when visible is false", () => {
    const wrapper = mountQuickOpen(false);
    expect(wrapper.find(".quick-open").exists()).toBe(false);
  });

  it("renders the input and loads the file list when visible", async () => {
    const wrapper = mountQuickOpen(true);
    await new Promise((r) => setTimeout(r, 10));
    expect(listAllFilesMock).toHaveBeenCalledWith("/proj");
    expect(wrapper.find(".quick-open__input").exists()).toBe(true);
  });

  it("shows the loading message before files are loaded", async () => {
    // Make listAllFiles hang so loading state is visible.
    listAllFilesMock.mockReturnValue(new Promise(() => {}));
    const wrapper = mountQuickOpen(true);
    await new Promise((r) => setTimeout(r, 10));
    expect(wrapper.text()).toContain("Loading files...");
  });

  it("renders filtered results matching the query", async () => {
    const wrapper = mountQuickOpen(true);
    await new Promise((r) => setTimeout(r, 10));
    // Type a query that matches "main.ts".
    await wrapper.find(".quick-open__input").setValue("main");
    expect(wrapper.findAll(".quick-open__item")).toHaveLength(3);
    await new Promise((r) => setTimeout(r, 320));
    const items = wrapper.findAll(".quick-open__item");
    expect(items.length).toBe(1);
    expect(items[0].text()).toContain("main.ts");
  });

  it("shows the no-files message when no files match", async () => {
    const wrapper = mountQuickOpen(true);
    await new Promise((r) => setTimeout(r, 10));
    await wrapper.find(".quick-open__input").setValue("zzzzzzz");
    await new Promise((r) => setTimeout(r, 320));
    expect(wrapper.find(".quick-open__empty").exists()).toBe(true);
    expect(wrapper.text()).toContain("No matching files");
  });

  it("shows the basename prominently and the dirname as secondary", async () => {
    const wrapper = mountQuickOpen(true);
    await new Promise((r) => setTimeout(r, 10));
    const items = wrapper.findAll(".quick-open__item");
    // Find the item for "src/util/helper.go".
    const helperItem = items.find((i) => i.text().includes("helper.go"));
    expect(helperItem).toBeTruthy();
    expect(helperItem!.find(".quick-open__name").text()).toBe("helper.go");
    expect(helperItem!.find(".quick-open__dir").text()).toBe("src/util");
  });

  it("opens the selected file on Enter", async () => {
    const wrapper = mountQuickOpen(true);
    await new Promise((r) => setTimeout(r, 10));
    await wrapper.find(".quick-open__input").setValue("main");
    await wrapper.find(".quick-open__input").trigger("keydown", { key: "Enter" });
    await new Promise((r) => setTimeout(r, 10));
    expect(readFileMock).toHaveBeenCalled();
    expect(openFileMock).toHaveBeenCalledWith(
      expect.stringContaining("main.ts"),
      "file content",
    );
  });

  it("opens the file when an item is clicked", async () => {
    const wrapper = mountQuickOpen(true);
    await new Promise((r) => setTimeout(r, 10));
    const items = wrapper.findAll(".quick-open__item");
    await items[0].trigger("click");
    await new Promise((r) => setTimeout(r, 10));
    expect(openFileMock).toHaveBeenCalled();
  });

  it("emits close after opening a file", async () => {
    const wrapper = mountQuickOpen(true);
    await new Promise((r) => setTimeout(r, 10));
    await wrapper.find(".quick-open__input").setValue("main");
    await wrapper.find(".quick-open__input").trigger("keydown", { key: "Enter" });
    await new Promise((r) => setTimeout(r, 10));
    expect(wrapper.emitted("close")).toBeTruthy();
  });

  it("emits close on Escape", async () => {
    const wrapper = mountQuickOpen(true);
    await wrapper.find(".quick-open__input").trigger("keydown", { key: "Escape" });
    expect(wrapper.emitted("close")).toBeTruthy();
  });

  it("emits close when the overlay is clicked", async () => {
    const wrapper = mountQuickOpen(true);
    await wrapper.find(".dialog-backdrop-button").trigger("click");
    expect(wrapper.emitted("close")).toBeTruthy();
  });

  it("keeps file options out of the Tab sequence", async () => {
    const wrapper = mountQuickOpen(true);
    await new Promise((r) => setTimeout(r, 10));
    const items = wrapper.findAll('[role="option"]');
    expect(items.length).toBeGreaterThan(0);
    expect(items.every((item) => item.attributes("tabindex") === "-1")).toBe(true);
  });

  it("ArrowDown/ArrowUp moves the selection", async () => {
    const wrapper = mountQuickOpen(true);
    await new Promise((r) => setTimeout(r, 10));
    const input = wrapper.find(".quick-open__input");
    // Initially selectedIndex is 0.
    expect(wrapper.findAll(".quick-open__item--active")).toHaveLength(1);
    await input.trigger("keydown", { key: "ArrowDown" });
    // Now the second item should be active.
    const activeAfterDown = wrapper.findAll(".quick-open__item--active");
    expect(activeAfterDown).toHaveLength(1);
    // Move back up.
    await input.trigger("keydown", { key: "ArrowUp" });
    expect(wrapper.findAll(".quick-open__item--active")).toHaveLength(1);
  });

  it("does not fetch files when no project is set", async () => {
    mockAppState.currentProject = "";
    mountQuickOpen(true);
    await new Promise((r) => setTimeout(r, 10));
    expect(listAllFilesMock).not.toHaveBeenCalled();
    mockAppState.currentProject = "/proj";
  });

  it("caches the file list for the same project root", async () => {
    const wrapper = mountQuickOpen(true);
    await new Promise((r) => setTimeout(r, 10));
    expect(listAllFilesMock).toHaveBeenCalledTimes(1);
    // Re-trigger the watch by toggling visible off then on.
    await wrapper.setProps({ visible: false });
    await wrapper.setProps({ visible: true });
    await new Promise((r) => setTimeout(r, 10));
    // Should NOT have fetched again (cache hit).
    expect(listAllFilesMock).toHaveBeenCalledTimes(1);
  });

  it("shows an error notification when listAllFiles fails", async () => {
    listAllFilesMock.mockRejectedValue(new Error("disk error"));
    mountQuickOpen(true);
    await new Promise((r) => setTimeout(r, 10));
    expect(notifyErrorMock).toHaveBeenCalledWith(
      "Failed to load file list",
      "disk error",
    );
  });

  it("shows an error notification when readFile fails on open", async () => {
    readFileMock.mockRejectedValue(new Error("permission denied"));
    const wrapper = mountQuickOpen(true);
    await new Promise((r) => setTimeout(r, 10));
    await wrapper.find(".quick-open__input").setValue("main");
    await wrapper.find(".quick-open__input").trigger("keydown", { key: "Enter" });
    await new Promise((r) => setTimeout(r, 10));
    expect(notifyErrorMock).toHaveBeenCalledWith(
      "Failed to open file",
      "permission denied",
    );
  });
});
