/**
 * ConversationSidebar tests (Plan 11 Task 2).
 *
 * Verifies:
 *   - Search filters by title and message content (Step 3/9).
 *   - Favorite filter (Step 3).
 *   - Tag filter (Step 3).
 *   - Trash view shows soft-deleted conversations (Step 8).
 *   - toggleFavorite persists via save (Step 4/5).
 *   - handleDelete calls delete (soft-delete, Step 5/8).
 *   - handleRestore clears deleted_at via save (Step 8).
 *   - handleExportMarkdown triggers a download (Step 5).
 *   - previewOf truncates long content (Step 4).
 */
import { describe, it, expect, vi, beforeEach } from "vitest";
import { mount, flushPromises } from "@vue/test-utils";
import { nextTick } from "vue";
import type { Conversation } from "@/types";

// Use vi.hoisted so mock factories can reference these without hoisting issues.
const { conversationServiceMock, promptValue, notificationsMock } = vi.hoisted(() => ({
  conversationServiceMock: {
    list: vi.fn(),
    save: vi.fn(),
    delete: vi.fn(),
  },
  promptValue: { value: "" },
  notificationsMock: {
    notifyError: vi.fn(),
    notifySuccess: vi.fn(),
    notifyWarning: vi.fn(),
  },
}));

// Mock @/lib/i18n to cut the @/stores/app → @/lib/monaco-themes → monaco-editor
// import chain (monaco-editor cannot resolve in the test environment).
vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({
    t: (key: string) => key,
  }),
  translate: (key: string) => key,
}));

vi.mock("@/api/services", () => ({
  conversationService: conversationServiceMock,
}));

vi.mock("@/lib/notifications", () => notificationsMock);

vi.mock("element-plus", () => ({
  ElMessageBox: {
    prompt: vi.fn(() => Promise.resolve({ value: promptValue.value })),
  },
}));

// Import the component AFTER mocks are set up.
import ConversationSidebar from "./ConversationSidebar.vue";

const { aiState } = await import("@/stores/ai");

function makeConv(overrides: Partial<Conversation> = {}): Conversation {
  return {
    id: "c1",
    title: "Test",
    created_at: 1000,
    messages: [{ role: "user", content: "hello" }],
    revision: 1,
    updated_at: 1000,
    ...overrides,
  };
}

function mountSidebar() {
  return mount(ConversationSidebar, {
    props: { width: 260 },
  });
}

describe("ConversationSidebar (Plan 11 Task 2)", () => {
  beforeEach(() => {
    promptValue.value = "";
    conversationServiceMock.list.mockReset();
    conversationServiceMock.save.mockReset();
    conversationServiceMock.delete.mockReset();
    aiState.streaming = false;
    aiState.globalStreamBusy = false;
    aiState.activeStreamId = null;
  });

  it("loads the first bounded page and appends the next page", async () => {
    const firstPage = Array.from({ length: 50 }, (_, index) =>
      makeConv({ id: `page-1-${index}`, created_at: 100 - index }),
    );
    conversationServiceMock.list
      .mockResolvedValueOnce(firstPage)
      .mockResolvedValueOnce([makeConv({ id: "page-2-0", created_at: 1 })]);

    const w = mountSidebar();
    await flushPromises();

    const vm = w.vm as unknown as {
      conversations: Conversation[];
      hasMoreConversations: boolean;
      loadMoreConversations: () => Promise<void>;
    };
    expect(conversationServiceMock.list).toHaveBeenNthCalledWith(1, 0, 50);
    expect(vm.conversations).toHaveLength(50);
    expect(vm.hasMoreConversations).toBe(true);

    await vm.loadMoreConversations();

    expect(conversationServiceMock.list).toHaveBeenNthCalledWith(2, 50, 50);
    expect(vm.conversations).toHaveLength(51);
    expect(vm.conversations[50].id).toBe("page-2-0");
    expect(vm.hasMoreConversations).toBe(false);
  });

  it("filters by search query on title", async () => {
    conversationServiceMock.list.mockResolvedValue([
      makeConv({ id: "a", title: "Refactor auth" }),
      makeConv({ id: "b", title: "UI design" }),
    ]);
    const w = mountSidebar();
    await nextTick();
    const vm = w.vm as unknown as { filteredConversations: Conversation[]; searchQuery: string };
    expect(vm.filteredConversations).toHaveLength(2);
    vm.searchQuery = "auth";
    await nextTick();
    expect(vm.filteredConversations).toHaveLength(1);
    expect(vm.filteredConversations[0].id).toBe("a");
  });

  it("filters by search query on message content (full-text, Step 9)", async () => {
    conversationServiceMock.list.mockResolvedValue([
      makeConv({ id: "a", title: "A", messages: [{ role: "user", content: "token expired" }] }),
      makeConv({ id: "b", title: "B", messages: [{ role: "user", content: "hello world" }] }),
    ]);
    const w = mountSidebar();
    await nextTick();
    const vm = w.vm as unknown as { filteredConversations: Conversation[]; searchQuery: string };
    vm.searchQuery = "token";
    await nextTick();
    expect(vm.filteredConversations).toHaveLength(1);
    expect(vm.filteredConversations[0].id).toBe("a");
  });

  it("filters by favorite (Step 3)", async () => {
    conversationServiceMock.list.mockResolvedValue([
      makeConv({ id: "a", favorite: true }),
      makeConv({ id: "b", favorite: false }),
    ]);
    const w = mountSidebar();
    await nextTick();
    const vm = w.vm as unknown as { filteredConversations: Conversation[]; filterFavorite: boolean };
    vm.filterFavorite = true;
    await nextTick();
    expect(vm.filteredConversations).toHaveLength(1);
    expect(vm.filteredConversations[0].id).toBe("a");
  });

  it("filters by tag (Step 3)", async () => {
    conversationServiceMock.list.mockResolvedValue([
      makeConv({ id: "a", tags: ["go"] }),
      makeConv({ id: "b", tags: ["vue"] }),
    ]);
    const w = mountSidebar();
    await nextTick();
    const vm = w.vm as unknown as { filteredConversations: Conversation[]; filterTag: string };
    vm.filterTag = "vue";
    await nextTick();
    expect(vm.filteredConversations).toHaveLength(1);
    expect(vm.filteredConversations[0].id).toBe("b");
  });

  it("shows soft-deleted conversations in trash view (Step 8)", async () => {
    conversationServiceMock.list.mockResolvedValue([
      makeConv({ id: "a", deleted_at: 0 }),
      makeConv({ id: "b", deleted_at: 12345 }),
    ]);
    const w = mountSidebar();
    await nextTick();
    const vm = w.vm as unknown as {
      filteredConversations: Conversation[];
      trashConversations: Conversation[];
      showTrash: boolean;
    };
    // Active list excludes soft-deleted.
    expect(vm.filteredConversations).toHaveLength(1);
    expect(vm.filteredConversations[0].id).toBe("a");
    // Trash list contains soft-deleted.
    expect(vm.trashConversations).toHaveLength(1);
    expect(vm.trashConversations[0].id).toBe("b");
  });

  it("toggleFavorite persists via save (Step 4/5)", async () => {
    const conv = makeConv({ id: "a", favorite: false });
    conversationServiceMock.list.mockResolvedValue([conv]);
    const w = mountSidebar();
    await nextTick();
    const vm = w.vm as unknown as { toggleFavorite: (c: Conversation) => Promise<void> };
    await vm.toggleFavorite(conv);
    expect(conversationServiceMock.save).toHaveBeenCalledWith(expect.objectContaining({ id: "a", favorite: true }));
  });

  it("handleDelete calls delete (soft-delete, Step 5/8)", async () => {
    const conv = makeConv({ id: "a" });
    conversationServiceMock.list.mockResolvedValue([conv]);
    const w = mountSidebar();
    await nextTick();
    const vm = w.vm as unknown as { handleDelete: (c: Conversation) => Promise<void> };
    await vm.handleDelete(conv);
    expect(conversationServiceMock.delete).toHaveBeenCalledWith("a");
  });

  it("handleRestore clears deleted_at via save (Step 8)", async () => {
    const conv = makeConv({ id: "a", deleted_at: 12345 });
    conversationServiceMock.list.mockResolvedValue([conv]);
    const w = mountSidebar();
    await nextTick();
    const vm = w.vm as unknown as { handleRestore: (c: Conversation) => Promise<void> };
    await vm.handleRestore(conv);
    expect(conversationServiceMock.save).toHaveBeenCalledWith(expect.objectContaining({ id: "a", deleted_at: 0 }));
  });

  it("handleExportMarkdown triggers a download (Step 5)", async () => {
    const conv = makeConv({
      id: "a",
      title: "Export Test",
      messages: [
        { role: "user", content: "hi" },
        { role: "assistant", content: "hello" },
      ],
    });
    conversationServiceMock.list.mockResolvedValue([conv]);
    const w = mountSidebar();
    await nextTick();
    // Spy on DOM click to verify download was triggered.
    const clickSpy = vi.fn();
    const originalCreate = document.createElement.bind(document);
    vi.spyOn(document, "createElement").mockImplementation((tag: string) => {
      const el = originalCreate(tag);
      if (tag === "a") {
        el.click = clickSpy;
      }
      return el;
    });
    const vm = w.vm as unknown as { handleExportMarkdown: (c: Conversation) => void };
    vm.handleExportMarkdown(conv);
    expect(clickSpy).toHaveBeenCalledTimes(1);
    vi.restoreAllMocks();
  });

  it("previewOf truncates long content (Step 4)", async () => {
    conversationServiceMock.list.mockResolvedValue([
      makeConv({ id: "a", messages: [{ role: "user", content: "x".repeat(200) }] }),
    ]);
    const w = mountSidebar();
    await nextTick();
    const vm = w.vm as unknown as { previewOf: (c: Conversation) => string };
    const c: Conversation = makeConv({ messages: [{ role: "user", content: "y".repeat(200) }] });
    const preview = vm.previewOf(c);
    expect(preview.length).toBeLessThanOrEqual(83); // 80 + ellipsis
    expect(preview.endsWith("…")).toBe(true);
  });

  it("emits select with id when an item is clicked", async () => {
    conversationServiceMock.list.mockResolvedValue([makeConv({ id: "a", title: "Click me" })]);
    const w = mountSidebar();
    await flushPromises();
    const item = w.find(".ai-conv-sidebar__item");
    await item.trigger("click");
    expect(w.emitted("select")).toBeTruthy();
    expect(w.emitted("select")![0]).toEqual(["a"]);
  });

  it("emits select with empty string for new conversation", async () => {
    conversationServiceMock.list.mockResolvedValue([]);
    const w = mountSidebar();
    await nextTick();
    await w.find(".ai-conv-sidebar__new").trigger("click");
    expect(w.emitted("select")![0]).toEqual([""]);
  });

  it("disables new and selection while the backend stream slot is busy", async () => {
    aiState.globalStreamBusy = true;
    conversationServiceMock.list.mockResolvedValue([makeConv({ id: "busy" })]);
    const w = mountSidebar();
    await flushPromises();

    expect(w.get(".ai-conv-sidebar__new").attributes("disabled")).toBeDefined();
    const item = w.get(".ai-conv-sidebar__item");
    expect(item.attributes("aria-disabled")).toBe("true");
    await item.trigger("click");
    expect(w.emitted("select")).toBeUndefined();
  });

  // Plan 11 Task 2 Step 6: drag-and-drop reordering.
  it("handleDrop reorders conversations and persists sort_order via save (Step 6)", async () => {
    // Backend List returns created_at-desc, so b(2) comes before a(1).
    conversationServiceMock.list.mockResolvedValue([
      makeConv({ id: "b", title: "B", created_at: 2 }),
      makeConv({ id: "a", title: "A", created_at: 1 }),
    ]);
    conversationServiceMock.save.mockResolvedValue(undefined);
    const w = mountSidebar();
    await flushPromises();
    const vm = w.vm as unknown as {
      displayedConversations: Conversation[];
      handleDragStart: (e: DragEvent, index: number) => void;
      handleDrop: (e: DragEvent, index: number) => Promise<void>;
      dragIndex: number | null;
    };
    // Initial order: [b, a]. Drag a (index 1) to position 0.
    expect(vm.displayedConversations.map((c) => c.id)).toEqual(["b", "a"]);
    const evt = new Event("drop") as DragEvent;
    vm.handleDragStart(evt, 1);
    expect(vm.dragIndex).toBe(1);
    await vm.handleDrop(evt, 0);
    // save called twice (a -> sort_order 1, b -> sort_order 2).
    expect(conversationServiceMock.save).toHaveBeenCalledTimes(2);
    const firstSave = conversationServiceMock.save.mock.calls[0][0] as Conversation;
    const secondSave = conversationServiceMock.save.mock.calls[1][0] as Conversation;
    expect(firstSave.id).toBe("a");
    expect(firstSave.sort_order).toBe(1);
    expect(secondSave.id).toBe("b");
    expect(secondSave.sort_order).toBe(2);
  });

  it("displayedConversations applies manual sort_order when present (Step 6)", async () => {
    // a has sort_order 2, b has sort_order 1 (manual override of created_at-desc).
    conversationServiceMock.list.mockResolvedValue([
      makeConv({ id: "a", title: "A", created_at: 3, sort_order: 2 }),
      makeConv({ id: "b", title: "B", created_at: 2, sort_order: 1 }),
      makeConv({ id: "c", title: "C", created_at: 1 }), // no sort_order, falls back
    ]);
    const w = mountSidebar();
    await flushPromises();
    const vm = w.vm as unknown as { displayedConversations: Conversation[] };
    // Manual-order items first (b=1, a=2), then c by created_at-desc.
    expect(vm.displayedConversations.map((c) => c.id)).toEqual(["b", "a", "c"]);
  });

  it("handleDrop is a no-op in trash view (Step 6)", async () => {
    const oldConv = makeConv({ id: "old", title: "Old", created_at: 1, deleted_at: 999 });
    conversationServiceMock.list.mockResolvedValue([oldConv]);
    conversationServiceMock.save.mockResolvedValue(undefined);
    const w = mountSidebar();
    await flushPromises();
    const vm = w.vm as unknown as {
      showTrash: boolean;
      displayedConversations: Conversation[];
      handleDragStart: (e: DragEvent, index: number) => void;
      handleDrop: (e: DragEvent, index: number) => Promise<void>;
      dragIndex: number | null;
    };
    vm.showTrash = true;
    await nextTick();
    const evt = new Event("drop") as DragEvent;
    vm.handleDragStart(evt, 0);
    await vm.handleDrop(evt, 0);
    // No save calls because reordering is disabled in trash view.
    expect(conversationServiceMock.save).not.toHaveBeenCalled();
  });
});
