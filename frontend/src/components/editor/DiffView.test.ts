// DiffView P1-01 stale 竞态守卫测试：
// 快速切换文件（或 staged 侧切换）时，迟到的旧 diff 响应不得回写内容，
// 也不得触发错误通知或让 loading 卡在旧请求上。
// mock 打在 @wailsio/runtime 的 Call 层：DiffView → api/services barrel →
// api/git → 生成的 gitservice 绑定 → runtime Call.ByID，这是组件调用链
// 上唯一在 .vue 组件消费侧也稳定生效的可拦截点。方法 ID 来自当前绑定的
// 生成产物（GetDiffForSide=291373738；测试场景均经由该绑定）。
import { describe, it, expect, vi, beforeEach } from "vitest";
import { mount, flushPromises } from "@vue/test-utils";
import ElementPlus from "element-plus";

const mocks = vi.hoisted(() => ({
  byID: vi.fn(),
  notifyError: vi.fn(),
}));

const GET_DIFF_FOR_SIDE_ID = 291373738;

vi.mock("@wailsio/runtime", () => ({
  Events: { On: vi.fn() },
  Call: { ByID: mocks.byID, ByName: vi.fn() },
  CancellablePromise: class {},
}));

vi.mock("@/lib/notifications", () => ({
  notifyError: mocks.notifyError,
}));

// Mock monaco-themes，避免真实 Monaco 编辑器在 jsdom 中初始化
//（同 TabBar.test.ts / app.test.ts 的既有模式）。
vi.mock("@/lib/monaco-themes", () => ({
  accentThemes: {
    blue: { label: "Blue", color: "#4285f4", monacoTheme: "koyoriIde-blue", monacoLightTheme: "koyoriIde-light-blue" },
  },
  getMonacoThemeName: vi.fn(() => "koyoriIde-blue"),
  applyMonacoTheme: vi.fn(),
  applyMonacoThemeForMode: vi.fn(),
  registerAllThemes: vi.fn(),
  registerCustomTheme: vi.fn(),
}));

vi.mock("@guolao/vue-monaco-editor", () => ({
  VueMonacoEditor: {
    name: "VueMonacoEditor",
    props: ["value", "language", "theme", "options"],
    template: "<div class='monaco-stub' />",
  },
}));

import DiffView from "./DiffView.vue";

function mountDiffView(props: { repoPath: string; filePath: string; visible: boolean; staged?: boolean }) {
  return mount(DiffView, {
    props,
    global: { plugins: [ElementPlus] },
  });
}

function editorValue(wrapper: ReturnType<typeof mountDiffView>): unknown {
  const editor = wrapper.findComponent({ name: "VueMonacoEditor" });
  return editor.exists() ? editor.props("value") : undefined;
}

describe("DiffView P1-01 stale 守卫", () => {
  beforeEach(() => {
    mocks.byID.mockReset();
    mocks.notifyError.mockReset();
  });

  it("快速切换文件时迟到的旧 diff 不回写内容", async () => {
    let resolveOld!: (v: string) => void;
    mocks.byID
      .mockImplementationOnce(() => new Promise<string>((resolve) => { resolveOld = resolve; }))
      .mockResolvedValueOnce("B-diff");

    // 注意：Vue 对缺省的 Boolean prop 做 absent-casting（staged → false），
    // 因此省略 staged 时组件走 GetDiffForSide(worktree 侧) 分支。
    const wrapper = mountDiffView({ repoPath: "/repo", filePath: "a.txt", visible: true });
    await flushPromises();
    expect(mocks.byID).toHaveBeenNthCalledWith(1, GET_DIFF_FOR_SIDE_ID, "/repo", "a.txt", false);
    expect(wrapper.find(".diff-view__loading").exists()).toBe(true);

    await wrapper.setProps({ filePath: "b.txt" });
    await flushPromises();
    expect(mocks.byID).toHaveBeenNthCalledWith(2, GET_DIFF_FOR_SIDE_ID, "/repo", "b.txt", false);
    expect(editorValue(wrapper)).toBe("B-diff");

    // 旧文件的 diff 此刻才迟到完成：不允许覆盖 b.txt 的内容
    resolveOld("A-diff");
    await flushPromises();

    expect(editorValue(wrapper)).toBe("B-diff");
    expect(wrapper.find(".diff-view__loading").exists()).toBe(false);
    expect(mocks.notifyError).not.toHaveBeenCalled();
  });

  it("staged 侧切换时迟到的旧侧响应不回写", async () => {
    let resolveOld!: (v: string) => void;
    mocks.byID
      .mockImplementationOnce(() => new Promise<string>((resolve) => { resolveOld = resolve; }))
      .mockResolvedValueOnce("staged-B-diff");

    const wrapper = mountDiffView({ repoPath: "/repo", filePath: "a.txt", visible: true, staged: true });
    await flushPromises();
    expect(mocks.byID).toHaveBeenNthCalledWith(1, GET_DIFF_FOR_SIDE_ID, "/repo", "a.txt", true);

    await wrapper.setProps({ staged: false });
    await flushPromises();
    expect(mocks.byID).toHaveBeenNthCalledWith(2, GET_DIFF_FOR_SIDE_ID, "/repo", "a.txt", false);
    expect(editorValue(wrapper)).toBe("staged-B-diff");

    resolveOld("staged-A-diff");
    await flushPromises();

    expect(editorValue(wrapper)).toBe("staged-B-diff");
    expect(wrapper.find(".diff-view__loading").exists()).toBe(false);
  });

  it("迟到的旧响应失败不触发错误通知且不影响新内容", async () => {
    let rejectOld!: (e: Error) => void;
    mocks.byID
      .mockImplementationOnce(() => new Promise<string>((_, reject) => { rejectOld = reject; }))
      .mockResolvedValueOnce("B-diff");

    const wrapper = mountDiffView({ repoPath: "/repo", filePath: "a.txt", visible: true });
    await flushPromises();

    await wrapper.setProps({ filePath: "b.txt" });
    await flushPromises();
    expect(editorValue(wrapper)).toBe("B-diff");

    rejectOld(new Error("old file vanished"));
    await flushPromises();

    expect(editorValue(wrapper)).toBe("B-diff");
    expect(mocks.notifyError).not.toHaveBeenCalled();
    expect(wrapper.find(".diff-view__loading").exists()).toBe(false);
  });

  it("最新一次加载失败时仍然正常通知错误", async () => {
    mocks.byID
      .mockResolvedValueOnce("A-diff")
      .mockRejectedValueOnce(new Error("diff exploded"));

    const wrapper = mountDiffView({ repoPath: "/repo", filePath: "a.txt", visible: true });
    await flushPromises();
    expect(editorValue(wrapper)).toBe("A-diff");

    await wrapper.setProps({ filePath: "b.txt" });
    await flushPromises();

    expect(mocks.notifyError).toHaveBeenCalledTimes(1);
    expect(wrapper.find(".diff-view__loading").exists()).toBe(false);
  });
});
