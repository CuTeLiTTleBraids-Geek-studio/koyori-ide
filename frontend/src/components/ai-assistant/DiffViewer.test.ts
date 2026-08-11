import { describe, it, expect, vi, beforeEach } from "vitest";
import { nextTick } from "vue";
import { mount } from "@vue/test-utils";
import type { DiffLineType, MultiFileDiff } from "@/types";

// vi.hoisted: mock 引用需在 vi.mock 工厂中使用，必须提升到顶部避免 TDZ。
const {
  // highlight.js 桩：highlight / highlightAuto 为 spy，getLanguage 恒真。
  hljsMock,
  // sanitizeHtml 透传，便于观察 hljs 输出是否被缓存。
  sanitizeHtmlMock,
  // diffState 原始对象，在 mock 工厂中用 reactive 包装。
  diffStateObj,
  applyFileMock,
  applyAllMock,
  notifySuccessMock,
  notifyWarningMock,
} = vi.hoisted(() => ({
  hljsMock: {
    highlight: vi.fn((code: string) => ({
      value: `<span class="hljs-keyword">${code}</span>`,
    })),
    highlightAuto: vi.fn((code: string) => ({
      value: `<span>${code}</span>`,
    })),
    getLanguage: vi.fn(() => true),
  },
  sanitizeHtmlMock: vi.fn((html: string) => html),
  diffStateObj: {
    diff: null as null | {
      files: unknown[];
      totalAdded: number;
      totalRemoved: number;
    },
    activeFileIdx: 0,
    collapsedHunks: new Set<string>(),
    aiReviewMode: false,
    artifactPreview: false,
  },
  applyFileMock: vi.fn(),
  applyAllMock: vi.fn(),
  notifySuccessMock: vi.fn(),
  notifyWarningMock: vi.fn(),
}));

vi.mock("highlight.js/lib/common", () => ({
  default: hljsMock,
}));

vi.mock("@/lib/markdown", () => ({
  sanitizeHtml: sanitizeHtmlMock,
  buildArtifactSrcDoc: vi.fn(() => ""),
  // 复用 markdown.ts 的 LRU 驱逐策略（容量 100）。
  putLru: (cache: Map<string, string>, key: string, value: string): void => {
    if (cache.size >= 100) {
      const oldest = cache.keys().next().value;
      if (oldest !== undefined) cache.delete(oldest);
    }
    cache.set(key, value);
  },
}));

vi.mock("@/lib/language", () => ({
  detectLanguage: vi.fn((filePath: string) => {
    if (filePath.endsWith(".ts")) return "typescript";
    return "plaintext";
  }),
}));

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({
    t: (key: string) => key,
    locale: { value: "en" },
  }),
}));

vi.mock("@/lib/notifications", () => ({
  notifySuccess: notifySuccessMock,
  notifyWarning: notifyWarningMock,
}));

vi.mock("@/stores/diff", async () => {
  const { reactive } = await import("vue");
  const diffState = reactive(diffStateObj);
  return {
    diffState,
    setActiveFile: vi.fn(),
    toggleHunk: vi.fn(),
    setAIReviewMode: vi.fn(),
    setArtifactPreview: vi.fn(),
    applyFile: applyFileMock,
    applyAll: applyAllMock,
    rejectFile: vi.fn().mockResolvedValue(undefined),
    rejectHunk: vi.fn().mockResolvedValue(undefined),
  };
});

// 在所有 mock 设置完成后再动态导入被测组件
const DiffViewerModule = await import("./DiffViewer.vue");
const DiffViewer = DiffViewerModule.default;
const { diffState: reactiveDiffState } = await import("@/stores/diff");

const flushPromises = () => new Promise((resolve) => setTimeout(resolve, 0));

/** 构造一个单文件 diff，hunk 包含给定的行（每行 {type, content}）。 */
function setDiff(lines: Array<{ type: DiffLineType; content: string }>): void {
  const nextDiff: MultiFileDiff = {
    files: [
      {
        path: "test.ts",
        oldPath: "test.ts",
        oldContent: "",
        newContent: "",
        hunks: [
          {
            oldStart: 1,
            oldCount: 1,
            newStart: 1,
            newCount: lines.length,
            lines: lines.map((l, i) => ({
              type: l.type,
              content: l.content,
              oldNum: l.type === "added" ? undefined : i + 1,
              newNum: l.type === "removed" ? undefined : i + 1,
            })),
          },
        ],
        addedLines: lines.filter((l) => l.type === "added").length,
        removedLines: lines.filter((l) => l.type === "removed").length,
      },
    ],
    totalAdded: lines.filter((l) => l.type === "added").length,
    totalRemoved: lines.filter((l) => l.type === "removed").length,
  };
  reactiveDiffState.diff = nextDiff;
  reactiveDiffState.activeFileIdx = 0;
  reactiveDiffState.aiReviewMode = false;
  reactiveDiffState.artifactPreview = false;
  reactiveDiffState.collapsedHunks = new Set<string>();
}

function mountDiffViewer() {
  return mount(DiffViewer, {
    global: {
      stubs: {
        // MarkdownContent 桩：渲染 html prop 以便断言缓存输出。
        MarkdownContent: {
          props: ["html"],
          template: '<div class="md-stub" v-html="html" />',
        },
      },
    },
  });
}

describe("M-23: DiffViewer highlightLine LRU 缓存", () => {
  beforeEach(() => {
    hljsMock.highlight.mockClear();
    hljsMock.highlightAuto.mockClear();
    hljsMock.getLanguage.mockClear();
    sanitizeHtmlMock.mockClear();
    applyFileMock.mockReset();
    applyAllMock.mockReset();
    notifySuccessMock.mockReset();
    notifyWarningMock.mockReset();
  });

  it("相同行的高亮结果从缓存读取（hljs.highlight 只调用一次）", async () => {
    // 三行：前两行内容相同，第三行不同。
    setDiff([
      { type: "added", content: "const x = 1;" },
      { type: "added", content: "const x = 1;" },
      { type: "context", content: "const y = 2;" },
    ]);

    const wrapper = mountDiffViewer();
    await flushPromises();

    // 三个不同 (content, path) 中有两个唯一内容：
    //   "const x = 1;" 出现两次 → hljs.highlight 调用 1 次（第二次命中缓存）
    //   "const y = 2;" 出现一次 → hljs.highlight 调用 1 次
    // 共 2 次。若缓存未生效，则为 3 次。
    expect(hljsMock.highlight).toHaveBeenCalledTimes(2);

    // 两个相同行的渲染输出应一致（缓存返回相同字符串）。
    const stubs = wrapper.findAll(".md-stub");
    expect(stubs.length).toBe(3);
    expect(stubs[0].element.innerHTML).toBe(stubs[1].element.innerHTML);
    // 第三行（不同内容）输出不同。
    expect(stubs[2].element.innerHTML).not.toBe(stubs[0].element.innerHTML);

    wrapper.unmount();
  });

  it("keeps hunk collapse and reject controls as labelled sibling buttons", async () => {
    setDiff([{ type: "context", content: "const value = 1;" }]);
    const wrapper = mountDiffViewer();
    await nextTick();

    const header = wrapper.find(".diff-hunk__header");
    const controls = header.findAll("button");
    expect(controls).toHaveLength(2);
    expect(controls[0].find("button").exists()).toBe(false);
    expect(controls[1].attributes("aria-label")).toBe("diffViewer.rejectHunk");

    wrapper.unmount();
  });

  it("多个实例对同一路径的不同版本保持实例级缓存隔离", async () => {
    setDiff([{ type: "added", content: "const version = 1;" }]);
    const first = mountDiffViewer();
    await flushPromises();
    expect(hljsMock.highlight).toHaveBeenCalledTimes(1);

    // 第一实例切换到同一路径的新版本并缓存它。
    setDiff([{ type: "added", content: "const version = 2;" }]);
    await nextTick();
    expect(hljsMock.highlight).toHaveBeenCalledTimes(2);

    // 第二实例必须运行自己的高亮，而不能命中第一实例的新版本缓存。
    const second = mountDiffViewer();
    await flushPromises();
    expect(hljsMock.highlight).toHaveBeenCalledTimes(3);
    expect(first.find(".md-stub").text()).toContain("const version = 2;");
    expect(second.find(".md-stub").text()).toContain("const version = 2;");

    first.unmount();
    // Unmounting one instance must not clear the still-mounted instance's
    // private cache. This catches a module-level cache cleared by either owner.
    second.vm.$forceUpdate();
    await nextTick();
    expect(hljsMock.highlight).toHaveBeenCalledTimes(3);
    second.unmount();

    // A fresh instance starts with a fresh cache after previous owners unmount.
    const third = mountDiffViewer();
    await flushPromises();
    expect(hljsMock.highlight).toHaveBeenCalledTimes(4);
    third.unmount();
  });

  it("AI 与行内评论生成稳定且唯一的实例 key，且不修改输入 DTO", async () => {
    setDiff([{ type: "context", content: "const value = 1;" }]);
    const file = diffStateObj.diff!.files[0] as {
      hunks: Array<{
        aiComments: Array<Record<string, unknown>>;
        lines: Array<{ comments: Array<Record<string, unknown>> }>;
      }>;
    };
    const aiComments: Array<Record<string, unknown>> = [
      { severity: "warning", message: "first" },
      { severity: "info", message: "second" },
    ];
    const inlineComments: Array<Record<string, unknown>> = [
      { author: "a", body: "one", createdAt: "2026-01-01" },
      { author: "b", body: "two", createdAt: "2026-01-02" },
    ];
    file.hunks[0].aiComments = aiComments;
    file.hunks[0].lines[0].comments = inlineComments;
    diffStateObj.aiReviewMode = true;

    const wrapper = mountDiffViewer();
    await nextTick();

    const initialIds = wrapper
      .findAll("[data-comment-id]")
      .map((node) => node.attributes("data-comment-id"));
    expect(initialIds).toHaveLength(4);
    expect(new Set(initialIds).size).toBe(4);
    expect(aiComments.every((comment) => comment["id"] === undefined)).toBe(true);
    expect(inlineComments.every((comment) => comment["id"] === undefined)).toBe(true);

    file.hunks[0].aiComments.reverse();
    file.hunks[0].lines[0].comments.reverse();
    await nextTick();
    const reorderedIds = wrapper
      .findAll("[data-comment-id]")
      .map((node) => node.attributes("data-comment-id"));
    expect(new Set(reorderedIds)).toEqual(new Set(initialIds));

    wrapper.unmount();
  });

  it("consumes a successful apply result and reports the committed file count", async () => {
    setDiff([{ type: "added", content: "const value = 2;" }]);
    applyFileMock.mockResolvedValue({
      status: "applied",
      appliedFiles: ["test.ts"],
      conflicts: [],
      failureReason: "",
      rollbackAttempted: false,
      rolledBack: false,
    });
    const wrapper = mountDiffViewer();

    const applyButton = wrapper.findAll("button")
      .find((button) => button.text() === "diffViewer.applyFile");
    await applyButton?.trigger("click");
    await flushPromises();

    expect(applyFileMock).toHaveBeenCalledWith(0);
    expect(notifySuccessMock).toHaveBeenCalledWith("diffViewer.applySuccess");
    expect(wrapper.find(".diff-viewer__apply-result").exists()).toBe(false);
    wrapper.unmount();
  });

  it("renders conflict details with retry and dismiss choices instead of silently failing", async () => {
    setDiff([{ type: "added", content: "const value = 2;" }]);
    applyFileMock.mockResolvedValue({
      status: "conflict",
      appliedFiles: [],
      conflicts: ["test.ts: dirty buffer conflicts with disk edit"],
      failureReason: "workspace edit conflict",
      rollbackAttempted: false,
      rolledBack: false,
    });
    const wrapper = mountDiffViewer();

    const applyButton = wrapper.findAll("button")
      .find((button) => button.text() === "diffViewer.applyFile");
    await applyButton?.trigger("click");
    await flushPromises();

    const result = wrapper.find(".diff-viewer__apply-result");
    expect(result.attributes("role")).toBe("alert");
    expect(result.text()).toContain("test.ts: dirty buffer conflicts with disk edit");
    expect(result.find(".diff-apply__retry").exists()).toBe(true);
    expect(result.find(".diff-apply__dismiss").exists()).toBe(true);
    expect(notifyWarningMock).toHaveBeenCalledWith("diffViewer.applyConflict");
    wrapper.unmount();
  });
});
