import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { mount } from "@vue/test-utils";
import { nextTick, type App } from "vue";
import ElementPlus, { ElDropdown, ElMessage, ElMessageBox, type MessageBoxData } from "element-plus";

// 用 vi.hoisted 定义 mock 引用：vi.mock 调用会被提升到文件顶部，
// 早于任何 const 声明，因此普通顶层 const 会落入暂时性死区。
// 这里把所有需要在 mock 工厂中引用、又在测试中断言的变量集中定义。
const {
  mockAppState,
  gitState,
  branchState,
  conflictState,
  rebaseState,
  reviewState,
  stashState,
  tagState,
  submoduleState,
  bisectState,
  refreshGitMock,
  discoverRepositoriesMock,
  initRepoMock,
  loadBranchesMock,
  stageFileMock,
  unstageFileMock,
  loadMoreGitChangesMock,
  commitChangesMock,
  pushChangesMock,
  pullChangesMock,
  createBranchMock,
  checkoutBranchMock,
  loadConflictsMock,
  resolveConflictAsOursMock,
  resolveConflictAsTheirsMock,
  markConflictResolvedMock,
  startRebaseMock,
  abortRebaseMock,
  continueRebaseMock,
  checkRebaseStatusMock,
  generateGitignoreMock,
  clearConflictStateMock,
  loadStashesMock,
  stashPushMock,
  stashPopMock,
  stashApplyMock,
  stashDropMock,
  loadTagsMock,
  createTagMock,
  deleteTagMock,
  pushTagsMock,
  amendCommitMock,
  clearStashAndTagStateMock,
  loadSubmodulesMock,
  submoduleAddMock,
  submoduleUpdateMock,
  submoduleDeinitMock,
  cherryPickMock,
  revertCommitMock,
  bisectStartMock,
  bisectGoodMock,
  bisectBadMock,
  bisectResetMock,
  openFileFromPathMock,
  runReviewMock,
  clearReviewMock,
  renderMarkdownMock,
} = vi.hoisted(() => {
  // 所有异步 store 动作默认返回已解决的 Promise，
  // 避免 onMounted 中 `checkRebaseStatus().then(...)` 对 undefined 调用 .then 报错。
  const resolved = () => vi.fn().mockResolvedValue(undefined);
  return {
    // appState：组件通过 workspaceRoot（目录）优先、currentProject 兜底读取仓库路径。
    mockAppState: { currentProject: "/proj", workspaceRoot: "" as string, workspaceFolders: [] as string[] },

    // gitState：变更列表、分支名、ahead/behind、loading、error
    gitState: {
      changes: [] as Array<{ path: string; status: string; staged?: boolean; oldPath?: string }>,
      branchName: "main",
      ahead: 0,
      behind: 0,
      loading: false,
      error: null as string | null,
      truncated: false,
      totalChanges: 0,
    },

    // branchState：分支列表，第一个为 HEAD
    branchState: {
      branches: [
        { name: "main", isHead: true },
        { name: "dev", isHead: false },
      ],
      loadingBranches: false,
    },

    // conflictState / rebaseState：默认无冲突、无进行中的 rebase
    conflictState: {
      conflicts: [] as Array<{ file: string; ours: string; theirs: string; base: string }>,
      loading: false,
      error: null as string | null,
    },
    rebaseState: {
      inProgress: false,
      loading: false,
      error: null as string | null,
      lastOutput: "",
    },

    // reviewState：默认无审查结果
    reviewState: {
      result: null as string | null,
      loading: false,
      error: null as string | null,
      reviewedFiles: [] as string[],
      reviewedAt: null as number | null,
    },

    // 优先级 3: stash/tag 状态
    stashState: {
      stashes: [] as Array<{ ref: string; commitHash: string; message: string }>,
      loading: false,
      error: null as string | null,
    },
    tagState: {
      tags: [] as Array<{ name: string; commitHash: string; message: string }>,
      loading: false,
      error: null as string | null,
    },
    submoduleState: {
      submodules: [] as Array<{ path: string; url: string; branch: string; initialized: boolean }>,
      loading: false,
      error: null as string | null,
    },
    bisectState: {
      inProgress: false,
      goodHash: "",
      badHash: "",
      error: null as string | null,
    },

    // ---- @/stores/git 的动作 mock ----
    refreshGitMock: resolved(),
    discoverRepositoriesMock: vi.fn().mockResolvedValue([]),
    initRepoMock: resolved(),
    loadBranchesMock: resolved(),
    stageFileMock: resolved(),
    unstageFileMock: resolved(),
    loadMoreGitChangesMock: vi.fn().mockReturnValue(0),
    commitChangesMock: resolved(),
    pushChangesMock: resolved(),
    pullChangesMock: resolved(),
    createBranchMock: resolved(),
    checkoutBranchMock: resolved(),
    loadConflictsMock: resolved(),
    resolveConflictAsOursMock: resolved(),
    resolveConflictAsTheirsMock: resolved(),
    markConflictResolvedMock: resolved(),
    startRebaseMock: resolved(),
    abortRebaseMock: resolved(),
    continueRebaseMock: resolved(),
    checkRebaseStatusMock: resolved(),
    generateGitignoreMock: resolved(),
    clearConflictStateMock: vi.fn(),
    // 优先级 3: stash/tag/amend 动作
    loadStashesMock: resolved(),
    stashPushMock: resolved(),
    stashPopMock: resolved(),
    stashApplyMock: resolved(),
    stashDropMock: resolved(),
    loadTagsMock: resolved(),
    createTagMock: resolved(),
    deleteTagMock: resolved(),
    pushTagsMock: resolved(),
    amendCommitMock: resolved(),
    clearStashAndTagStateMock: vi.fn(),
    loadSubmodulesMock: resolved(),
    submoduleAddMock: resolved(),
    submoduleUpdateMock: resolved(),
    submoduleDeinitMock: resolved(),
    cherryPickMock: resolved(),
    revertCommitMock: resolved(),
    bisectStartMock: resolved(),
    bisectGoodMock: resolved(),
    bisectBadMock: resolved(),
    bisectResetMock: resolved(),

    // ---- @/stores/editor ----
    openFileFromPathMock: resolved(),

    // ---- @/stores/review ----
    runReviewMock: resolved(),
    clearReviewMock: vi.fn(),

    // ---- @/lib/markdown ----
    renderMarkdownMock: vi.fn((content: string) => `<div>${content ?? ""}</div>`),
  };
});

// --- 双保险 mock：同时 mock store 与 service，确保任何代码路径都不会触达真实实现 ---

vi.mock("@/stores/app", () => ({
  appState: mockAppState,
}));

vi.mock("@/stores/git", () => ({
  gitState,
  branchState,
  conflictState,
  rebaseState,
  stashState,
  tagState,
  submoduleState,
  bisectState,
  refreshGit: refreshGitMock,
  discoverRepositories: discoverRepositoriesMock,
  initRepo: initRepoMock,
  loadBranches: loadBranchesMock,
  stageFile: stageFileMock,
  unstageFile: unstageFileMock,
  loadMoreGitChanges: loadMoreGitChangesMock,
  commitChanges: commitChangesMock,
  pushChanges: pushChangesMock,
  pullChanges: pullChangesMock,
  createBranch: createBranchMock,
  checkoutBranch: checkoutBranchMock,
  loadConflicts: loadConflictsMock,
  resolveConflictAsOurs: resolveConflictAsOursMock,
  resolveConflictAsTheirs: resolveConflictAsTheirsMock,
  markConflictResolved: markConflictResolvedMock,
  startRebase: startRebaseMock,
  abortRebase: abortRebaseMock,
  continueRebase: continueRebaseMock,
  checkRebaseStatus: checkRebaseStatusMock,
  generateGitignore: generateGitignoreMock,
  clearConflictState: clearConflictStateMock,
  loadStashes: loadStashesMock,
  stashPush: stashPushMock,
  stashPop: stashPopMock,
  stashApply: stashApplyMock,
  stashDrop: stashDropMock,
  loadTags: loadTagsMock,
  createTag: createTagMock,
  deleteTag: deleteTagMock,
  pushTags: pushTagsMock,
  amendCommit: amendCommitMock,
  clearStashAndTagState: clearStashAndTagStateMock,
  loadSubmodules: loadSubmodulesMock,
  submoduleAdd: submoduleAddMock,
  submoduleUpdate: submoduleUpdateMock,
  submoduleDeinit: submoduleDeinitMock,
  cherryPick: cherryPickMock,
  revertCommit: revertCommitMock,
  bisectStart: bisectStartMock,
  bisectGood: bisectGoodMock,
  bisectBad: bisectBadMock,
  bisectReset: bisectResetMock,
}));

vi.mock("@/stores/editor", () => ({
  openFileFromPath: openFileFromPathMock,
}));

// hasReview 必须是真正的 ref，模板中 v-else-if="hasReview" 才会自动解包为 .value；
// 若用普通对象会被判定为 truthy。这里用动态导入 vue 创建 ref(false)。
vi.mock("@/stores/review", async () => {
  const { ref } = await import("vue");
  return {
    reviewState,
    hasReview: ref(false),
    runReview: runReviewMock,
    clearReview: clearReviewMock,
  };
});

vi.mock("@/api/services", () => ({
  gitService: {
    getStatus: vi.fn(),
    getBranchInfo: vi.fn(),
    stage: vi.fn(),
    unstage: vi.fn(),
    commit: vi.fn(),
    push: vi.fn(),
    pull: vi.fn(),
    listBranches: vi.fn(),
    createBranch: vi.fn(),
    checkoutBranch: vi.fn(),
    deleteBranch: vi.fn(),
    getDiff: vi.fn(),
    getFullDiff: vi.fn(),
    listMergeConflicts: vi.fn(),
    resolveConflict: vi.fn(),
    isRebaseInProgress: vi.fn(),
    rebase: vi.fn(),
    abortRebase: vi.fn(),
    continueRebase: vi.fn(),
    createGitignore: vi.fn(),
  },
  fileService: { readFile: vi.fn(), writeFile: vi.fn(), pickDirectory: vi.fn() },
  aiService: { getPresetPrompt: vi.fn(), send: vi.fn() },
  settingsService: {},
  windowService: {},
}));

vi.mock("@/lib/errors", () => ({
  errorMessage: (e: unknown) => (e instanceof Error ? e.message : String(e)),
  isCancellationError: (e: unknown) => e === "cancel" || e === "close",
}));

// mock markdown 以规避 DOMPurify / highlight.js 的 DOM 副作用
vi.mock("@/lib/markdown", () => ({
  renderMarkdownWithApplyButtons: renderMarkdownMock,
}));

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => {
      const map: Record<string, string> = {
        "git.stage": "Stage",
        "git.unstage": "Unstage",
        "git.diff": "Diff",
        "git.viewDiffAria": "View Diff",
        "git.commit": "Commit",
        "git.noChanges": "No changes",
        "git.review": "Review",
        "git.rerun": "Rerun",
        "git.reviewing": "Reviewing...",
        "git.noReviewYet": "No review yet",
        "git.gitignoreCreated": "Created",
        "git.gitignoreExists": "Exists",
        "git.truncated": "Showing {shown} of more than {max} changes",
        "git.loadMoreChanges": "Load remaining {count} changes",
        "git.moreActions": "More Git actions",
        "git.stagedCount": "Staged ({count})",
        "git.changesCount": "Changes ({count})",
        // 优先级 3: Stash / Tag / Amend
        "git.amend": "Amend",
        "git.amendBtn": "Amend Commit",
        "git.amended": "Last commit amended",
        "git.stash": "Stash",
        "git.stashPush": "Stash",
        "git.stashCreated": "Changes stashed",
        "git.stashPopBtn": "Pop",
        "git.stashApplyBtn": "Apply",
        "git.stashDropBtn": "Drop",
        "git.stashDropped": "Stash dropped",
        "git.stashApplied": "Stash applied",
        "git.stashPopped": "Stash popped",
        "git.stashEmpty": "No stashes",
        "git.tags": "Tags",
        "git.tagCreate": "Create Tag",
        "git.tagCreated": "Tag created",
        "git.tagDeleteBtn": "Delete",
        "git.tagDeleted": "Tag deleted",
        "git.tagsEmpty": "No tags",
        "git.tagsPush": "Push Tags",
        "git.tagsPushed": "Tags pushed",
        "common.loading": "Loading...",
        "common.retry": "Retry",
        "common.cancel": "Cancel",
        "common.confirm": "Confirm",
      };
      let v = map[key] ?? key;
      if (params) {
        for (const [k, val] of Object.entries(params)) {
          v = v.replace(`{${k}}`, String(val));
        }
      }
      return v;
    },
    locale: { value: "en" },
  }),
}));

// mock DiffView 子组件，避免引入 Monaco 编辑器与真实 gitService
vi.mock("@/components/editor/DiffView.vue", () => ({
  default: {
    name: "DiffView",
    props: ["repoPath", "filePath", "visible", "staged"],
    emits: ["close"],
    template: '<div class="stub-diff-view" />',
  },
}));

vi.mock("@/components/git/CommitGraph.vue", () => ({
  default: {
    name: "CommitGraph",
    props: ["repoPath", "branch"],
    template: '<div class="stub-commit-graph" />',
  },
}));

vi.mock("@/components/git/WorktreePanel.vue", () => ({
  default: {
    name: "WorktreePanel",
    props: ["repoPath", "branches"],
    template: '<div class="stub-worktree-panel" />',
  },
}));

vi.mock("@/components/git/RebaseEditor.vue", () => ({
  default: {
    name: "RebaseEditor",
    props: ["repoPath", "upstreamBranch", "autoLoad"],
    template: '<div class="stub-rebase-editor" />',
  },
}));

// ElementPlus 图标在测试中无需渲染，安装一个空插件即可。
const iconPlugin = {
  install(_app: App) {},
};

// 在所有 mock 设置完成后再动态导入被测组件
const GitPanelModule = await import("./GitPanel.vue");
const GitPanel = GitPanelModule.default;

const flushPromises = () => new Promise((resolve) => setTimeout(resolve, 0));

function deferred<T = void>() {
  let resolve!: (value: T | PromiseLike<T>) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

// 真实合理的 GitFileChange 样本数据
const sampleChanges: Array<{ path: string; status: string; staged?: boolean }> = [
  { path: "src/main.ts", status: "Modified", staged: false },
  { path: "README.md", status: "Added", staged: true },
  { path: "old/deleted.go", status: "Deleted", staged: false },
];

function mountGitPanel(options: { attachTo?: string | Element } = {}) {
  return mount(GitPanel, {
    ...options,
    global: {
      plugins: [ElementPlus, iconPlugin],
    },
  });
}

function resetState() {
  mockAppState.currentProject = "/proj";
  mockAppState.workspaceRoot = "";
  mockAppState.workspaceFolders = [];
  gitState.changes = [];
  gitState.branchName = "main";
  gitState.ahead = 0;
  gitState.behind = 0;
  gitState.loading = false;
  gitState.error = null;
  gitState.truncated = false;
  gitState.totalChanges = 0;
  branchState.branches = [
    { name: "main", isHead: true },
    { name: "dev", isHead: false },
  ];
  branchState.loadingBranches = false;
  conflictState.conflicts = [];
  conflictState.loading = false;
  conflictState.error = null;
  rebaseState.inProgress = false;
  rebaseState.loading = false;
  rebaseState.error = null;
  rebaseState.lastOutput = "";
  reviewState.result = null;
  reviewState.loading = false;
  reviewState.error = null;
  reviewState.reviewedFiles = [];
  reviewState.reviewedAt = null;
  // 优先级 3: 重置 stash/tag 状态
  stashState.stashes = [];
  stashState.loading = false;
  stashState.error = null;
  tagState.tags = [];
  tagState.loading = false;
  tagState.error = null;
  submoduleState.submodules = [];
  submoduleState.loading = false;
  submoduleState.error = null;
  bisectState.inProgress = false;
  bisectState.goodHash = "";
  bisectState.badHash = "";
  bisectState.error = null;
}

describe("GitPanel", () => {
  beforeEach(() => {
    resetState();
    vi.clearAllMocks();
    // 清除调用记录会重置 mockImplementation 吗？不会，clearAllMocks 只清调用记录。
    // 但需要保证默认 resolved 行为仍在：
    refreshGitMock.mockResolvedValue(undefined);
    discoverRepositoriesMock.mockResolvedValue([]);
    initRepoMock.mockResolvedValue(undefined);
    loadBranchesMock.mockResolvedValue(undefined);
    stageFileMock.mockResolvedValue(undefined);
    unstageFileMock.mockResolvedValue(undefined);
    commitChangesMock.mockResolvedValue(undefined);
    pushChangesMock.mockResolvedValue(undefined);
    pullChangesMock.mockResolvedValue(undefined);
    checkRebaseStatusMock.mockResolvedValue(undefined);
    generateGitignoreMock.mockResolvedValue(undefined);
    runReviewMock.mockResolvedValue(undefined);
    // 优先级 3: 重置 stash/tag/amend 默认 resolved 行为
    loadStashesMock.mockResolvedValue(undefined);
    stashPushMock.mockResolvedValue(undefined);
    stashPopMock.mockResolvedValue(undefined);
    stashApplyMock.mockResolvedValue(undefined);
    stashDropMock.mockResolvedValue(undefined);
    loadTagsMock.mockResolvedValue(undefined);
    createTagMock.mockResolvedValue(undefined);
    deleteTagMock.mockResolvedValue(undefined);
    pushTagsMock.mockResolvedValue(undefined);
    amendCommitMock.mockResolvedValue(undefined);
    loadSubmodulesMock.mockResolvedValue(undefined);
    submoduleAddMock.mockResolvedValue(undefined);
    submoduleUpdateMock.mockResolvedValue(undefined);
    submoduleDeinitMock.mockResolvedValue(undefined);
    cherryPickMock.mockResolvedValue(undefined);
    revertCommitMock.mockResolvedValue(undefined);
    bisectStartMock.mockResolvedValue(undefined);
    bisectGoodMock.mockResolvedValue(undefined);
    bisectBadMock.mockResolvedValue(undefined);
    bisectResetMock.mockResolvedValue(undefined);
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("渲染初始状态，显示当前分支名", () => {
    const wrapper = mountGitPanel();
    // 分支栏应展示 HEAD 分支名 main
    expect(wrapper.find(".git-panel__branch-current").text()).toContain("main");
  });

  it("renders commit history as a collapsed collapsible section", () => {
    const wrapper = mountGitPanel();

    const section = wrapper.find(".git-panel__commit-graph");
    const graph = wrapper.findComponent({ name: "CommitGraph" });
    expect(section.element.tagName).toBe("DETAILS");
    expect(section.attributes("open")).toBeUndefined();
    expect(graph.props()).toMatchObject({ repoPath: "/proj", branch: "main" });
  });

  it("renders ordinary worktree and interactive rebase entries", () => {
    const wrapper = mountGitPanel();

    const worktree = wrapper.findComponent({ name: "WorktreePanel" });
    const rebase = wrapper.findComponent({ name: "RebaseEditor" });
    expect(worktree.exists()).toBe(true);
    expect(worktree.props()).toMatchObject({ repoPath: "/proj" });
    expect(rebase.exists()).toBe(true);
    expect(rebase.props()).toMatchObject({ repoPath: "/proj", autoLoad: false });
  });

  it("无变更时显示空状态", () => {
    gitState.changes = [];
    const wrapper = mountGitPanel();
    expect(wrapper.find(".git-panel__empty").exists()).toBe(true);
    expect(wrapper.text()).toContain("No changes");
  });

  it("渲染变更列表并显示状态标签", () => {
    gitState.changes = sampleChanges;
    const wrapper = mountGitPanel();
    const rows = wrapper.findAll(".git-panel__row");
    expect(rows).toHaveLength(3);
    expect(wrapper.text()).toContain("src/main.ts");
    expect(wrapper.text()).toContain("old/deleted.go");
  });

  it("splits staged and unstaged rows and keeps matching actions", () => {
    gitState.changes = sampleChanges;
    const wrapper = mountGitPanel();
    const staged = wrapper.find('[data-testid="git-staged"]');
    const unstaged = wrapper.find('[data-testid="git-unstaged"]');
    expect(staged.text()).toContain("README.md");
    expect(staged.find('button[aria-label="Stage"]').exists()).toBe(false);
    expect(staged.find('button[aria-label="Unstage"]').exists()).toBe(true);
    expect(unstaged.text()).toContain("src/main.ts");
    expect(unstaged.find('button[aria-label="Stage"]').exists()).toBe(true);
    expect(unstaged.find('button[aria-label="Unstage"]').exists()).toBe(false);
    expect(wrapper.find(".git-panel__path").attributes("title")).toBeTruthy();
  });

  it("shows a truncation banner when the backend capped the list", () => {
    gitState.changes = sampleChanges;
    gitState.truncated = true;
    const wrapper = mountGitPanel();
    expect(wrapper.find(".git-panel__truncated").exists()).toBe(true);
    expect(wrapper.find(".git-panel__truncated").text()).toMatch(/3/);
  });

  it("renders a proven staged rename as one old → new row (P1-04)", () => {
    gitState.changes = [{ path: "new.txt", status: "Renamed", staged: true, oldPath: "old.txt" }];
    const wrapper = mountGitPanel();
    const staged = wrapper.find('[data-testid="git-staged"]');
    expect(staged.exists()).toBe(true);
    expect(staged.text()).toContain("old.txt → new.txt");
    expect(staged.find(".git-panel__path").attributes("title")).toBe("old.txt → new.txt");
    expect(staged.find(".git-panel__status--renamed").text()).toBe("R");
  });

  it("unstaging a rename row resets both the added and deleted paths (P1-04)", async () => {
    gitState.changes = [{ path: "new.txt", status: "Renamed", staged: true, oldPath: "old.txt" }];
    const wrapper = mountGitPanel();
    vi.clearAllMocks();
    unstageFileMock.mockResolvedValue(undefined);

    await wrapper.find('[data-testid="git-staged"] button[aria-label="Unstage"]').trigger("click");
    await flushPromises();

    expect(unstageFileMock).toHaveBeenCalledTimes(2);
    expect(unstageFileMock).toHaveBeenNthCalledWith(1, "/proj", "new.txt");
    expect(unstageFileMock).toHaveBeenNthCalledWith(2, "/proj", "old.txt");
  });

  it("passes the clicked row's side to DiffView so both rows get their own diff (P1-04)", async () => {
    gitState.changes = sampleChanges;
    const wrapper = mountGitPanel();
    const diffStub = wrapper.findComponent({ name: "DiffView" });

    await wrapper.find('[data-testid="git-staged"] button[aria-label="View Diff"]').trigger("click");
    await nextTick();
    expect(diffStub.props("filePath")).toBe("README.md");
    expect(diffStub.props("staged")).toBe(true);

    await wrapper.find('[data-testid="git-unstaged"] button[aria-label="View Diff"]').trigger("click");
    await nextTick();
    expect(diffStub.props("filePath")).toBe("src/main.ts");
    expect(diffStub.props("staged")).toBe(false);
  });

  // jsdom 渲染 1000 行变更接近 vitest 默认 5s 超时，在负载高的机器上会
  // 间歇性超时（断言本身不变，仅放宽该用例的时间预算）。
  it("truncation offers a continuation action that extends the visible window (P1-04)", { timeout: 20000 }, async () => {
    gitState.changes = Array.from({ length: 1000 }, (_, i) => ({
      path: `f${i}.txt`,
      status: "Modified",
      staged: false,
    }));
    gitState.truncated = true;
    gitState.totalChanges = 2500;
    const wrapper = mountGitPanel();
    const btn = wrapper.find('[data-testid="git-load-more"]');
    expect(btn.exists()).toBe(true);
    expect(btn.text()).toContain("500");

    loadMoreGitChangesMock.mockReturnValue(1500);
    await btn.trigger("click");
    expect(loadMoreGitChangesMock).toHaveBeenCalledTimes(1);
  });

  it("keeps the branch name visible in a 260px sidebar", () => {
    const host = document.createElement("div");
    host.style.width = "260px";
    document.body.appendChild(host);
    const wrapper = mountGitPanel({ attachTo: host });
    const bar = wrapper.find(".git-panel__branch-bar").element as HTMLElement;
    expect(wrapper.find(".git-panel__branch-current").text()).toContain("main");
    expect(bar.scrollWidth).toBeLessThanOrEqual(bar.clientWidth + 1);
    expect(wrapper.find(".git-panel__overflow-btn").exists()).toBe(true);
    wrapper.unmount();
    host.remove();
  });

  it("点击暂存按钮调用 stageFile", async () => {
    gitState.changes = sampleChanges;
    const wrapper = mountGitPanel();
    // onMounted 已调用过若干 mock，清除后再触发交互以精确断言
    vi.clearAllMocks();
    stageFileMock.mockResolvedValue(undefined);

    const stageBtn = wrapper.find('button[aria-label="Stage"]');
    await stageBtn.trigger("click");
    await flushPromises();

    expect(stageFileMock).toHaveBeenCalledWith("/proj", "src/main.ts");
  });

  it("点击取消暂存按钮调用 unstageFile", async () => {
    gitState.changes = sampleChanges;
    const wrapper = mountGitPanel();
    vi.clearAllMocks();
    unstageFileMock.mockResolvedValue(undefined);

    const unstageBtn = wrapper.find('button[aria-label="Unstage"]');
    await unstageBtn.trigger("click");
    await flushPromises();

    expect(unstageFileMock).toHaveBeenCalledWith("/proj", "README.md");
  });

  it("无提交信息时提交按钮被禁用", () => {
    const wrapper = mountGitPanel();
    const commitBtn = wrapper.find(".git-panel__commit-btn");
    expect(commitBtn.attributes("disabled")).toBeDefined();
  });

  it("输入提交信息并提交，调用 commitChanges 并清空输入框", async () => {
    gitState.changes = sampleChanges;
    const wrapper = mountGitPanel();
    vi.clearAllMocks();
    commitChangesMock.mockResolvedValue(undefined);

    const input = wrapper.find(".git-panel__commit-input");
    await input.setValue("feat: add new feature");
    const commitBtn = wrapper.find(".git-panel__commit-btn");
    expect(commitBtn.attributes("disabled")).toBeUndefined();

    await commitBtn.trigger("click");
    await flushPromises();

    expect(commitChangesMock).toHaveBeenCalledWith("/proj", "feat: add new feature");
    // 提交后输入框应被清空
    expect((input.element as HTMLTextAreaElement).value).toBe("");
  });

  it("点击刷新按钮调用 refreshGit 与 checkRebaseStatus", async () => {
    const wrapper = mountGitPanel();
    vi.clearAllMocks();
    refreshGitMock.mockResolvedValue(undefined);
    checkRebaseStatusMock.mockResolvedValue(undefined);

    await wrapper.find(".git-panel__refresh").trigger("click");
    await flushPromises();

    expect(refreshGitMock).toHaveBeenCalledWith("/proj");
    expect(checkRebaseStatusMock).toHaveBeenCalled();
    // 未处于 rebase 中，不应加载冲突
    expect(loadConflictsMock).not.toHaveBeenCalled();
  });

  it("同域新刷新使先发后到的旧刷新失效", async () => {
    const wrapper = mountGitPanel();
    vi.clearAllMocks();
    const firstRefresh = deferred();
    const secondRefresh = deferred();
    refreshGitMock
      .mockImplementationOnce(() => firstRefresh.promise)
      .mockImplementationOnce(() => secondRefresh.promise);

    await wrapper.find(".git-panel__refresh").trigger("click");
    await wrapper.find(".git-panel__refresh").trigger("click");

    secondRefresh.resolve();
    await flushPromises();
    expect(checkRebaseStatusMock).toHaveBeenCalledTimes(1);

    firstRefresh.resolve();
    await flushPromises();
    expect(checkRebaseStatusMock).toHaveBeenCalledTimes(1);
  });

  it("卸载后在途刷新不得继续提交 rebase 状态", async () => {
    const wrapper = mountGitPanel();
    vi.clearAllMocks();
    const refresh = deferred();
    refreshGitMock.mockReturnValueOnce(refresh.promise);

    await wrapper.find(".git-panel__refresh").trigger("click");
    wrapper.unmount();
    refresh.resolve();
    await flushPromises();

    expect(checkRebaseStatusMock).not.toHaveBeenCalled();
    expect(loadConflictsMock).not.toHaveBeenCalled();
  });

  it("卸载后延迟确认不得初始化仓库或打开成功提示", async () => {
    gitState.error = "not a git repository";
    const wrapper = mountGitPanel();
    vi.clearAllMocks();
    const confirmation = deferred<MessageBoxData>();
    vi.spyOn(ElMessageBox, "confirm").mockReturnValue(confirmation.promise);
    const successSpy = vi.spyOn(ElMessage, "success");

    await wrapper.find(".git-panel__init-btn").trigger("click");
    wrapper.unmount();
    confirmation.resolve({ value: "", action: "confirm" } as MessageBoxData);
    await flushPromises();

    expect(initRepoMock).not.toHaveBeenCalled();
    expect(successSpy).not.toHaveBeenCalled();
  });

  it("点击 Diff 按钮打开 DiffView 并传入文件路径", async () => {
    gitState.changes = sampleChanges;
    const wrapper = mountGitPanel();
    const diffStub = wrapper.findComponent({ name: "DiffView" });
    expect(diffStub.props("visible")).toBe(false);

    await wrapper.find('[data-testid="git-unstaged"] button[aria-label="View Diff"]').trigger("click");
    await nextTick();

    expect(diffStub.props("visible")).toBe(true);
    expect(diffStub.props("filePath")).toBe("src/main.ts");
  });

  it(".gitignore 下拉菜单命令触发 generateGitignore 并刷新", async () => {
    const wrapper = mountGitPanel();
    vi.clearAllMocks();
    generateGitignoreMock.mockResolvedValue(undefined);
    refreshGitMock.mockResolvedValue(undefined);

    const overflow = wrapper.findAllComponents(ElDropdown).find((candidate) => candidate.classes().includes("git-panel__overflow"))!;
    expect(overflow.exists()).toBe(true);
    overflow.vm.$emit("command", "gitignore:typescript");
    await flushPromises();

    expect(generateGitignoreMock).toHaveBeenCalledWith("typescript");
    expect(refreshGitMock).toHaveBeenCalledWith("/proj");
  });

  it(".gitignore 已存在时走警告分支且不再调用 refreshGit", async () => {
    const wrapper = mountGitPanel();
    vi.clearAllMocks();
    generateGitignoreMock.mockRejectedValue(new Error(".gitignore already exists"));
    refreshGitMock.mockResolvedValue(undefined);

    const overflow = wrapper.findAllComponents(ElDropdown).find((candidate) => candidate.classes().includes("git-panel__overflow"))!;
    overflow.vm.$emit("command", "gitignore:go");
    await flushPromises();

    expect(generateGitignoreMock).toHaveBeenCalledWith("go");
    // 错误分支不应继续调用 refreshGit
    expect(refreshGitMock).not.toHaveBeenCalled();
  });

  it("点击审查按钮打开模态框并触发 runReview", async () => {
    const wrapper = mountGitPanel();
    vi.clearAllMocks();
    runReviewMock.mockResolvedValue(undefined);

    wrapper.findAllComponents(ElDropdown).find((candidate) => candidate.classes().includes("git-panel__overflow"))!.vm.$emit("command", "review");
    await nextTick();

    // 模态框应可见
    expect(wrapper.find(".review-modal").exists()).toBe(true);
    // 首次打开且无结果时应自动触发审查
    expect(runReviewMock).toHaveBeenCalledWith("/proj");
  });

  it("重新审查按钮调用 clearReview 与 runReview", async () => {
    const wrapper = mountGitPanel();
    // 先打开模态框
    wrapper.findAllComponents(ElDropdown).find((candidate) => candidate.classes().includes("git-panel__overflow"))!.vm.$emit("command", "review");
    await nextTick();
    vi.clearAllMocks();
    runReviewMock.mockResolvedValue(undefined);

    const rerunBtn = wrapper.find(".review-modal__rerun");
    expect(rerunBtn.attributes("disabled")).toBeUndefined();
    await rerunBtn.trigger("click");
    await flushPromises();

    expect(clearReviewMock).toHaveBeenCalled();
    expect(runReviewMock).toHaveBeenCalledWith("/proj");
  });

  it("同域新审查取消仍在进行的旧审查", async () => {
    const wrapper = mountGitPanel();
    vi.clearAllMocks();
    const firstReview = deferred();
    runReviewMock
      .mockImplementationOnce(() => firstReview.promise)
      .mockResolvedValueOnce(undefined);
    const abortSpy = vi.spyOn(AbortController.prototype, "abort");

    wrapper.findAllComponents(ElDropdown).find((candidate) => candidate.classes().includes("git-panel__overflow"))!.vm.$emit("command", "review");
    await nextTick();
    await wrapper.find(".review-modal__rerun").trigger("click");

    expect(runReviewMock).toHaveBeenCalledTimes(2);
    expect(abortSpy).toHaveBeenCalledTimes(1);
    firstReview.resolve();
    await flushPromises();
  });

  it("点击推送按钮调用 pushChanges", async () => {
    const wrapper = mountGitPanel();
    vi.clearAllMocks();
    pushChangesMock.mockResolvedValue(undefined);

    // 通过 title 定位推送按钮（推送按钮使用 Top 图标）
    const pushBtn = wrapper.find('button[title="git.pushTitle"]');
    await pushBtn.trigger("click");
    await flushPromises();

    expect(pushChangesMock).toHaveBeenCalledWith("/proj");
  });

  it("点击拉取按钮调用 pullChanges", async () => {
    const wrapper = mountGitPanel();
    vi.clearAllMocks();
    pullChangesMock.mockResolvedValue(undefined);

    const pullBtn = wrapper.find('button[title="git.pullTitle"]');
    await pullBtn.trigger("click");
    await flushPromises();

    expect(pullChangesMock).toHaveBeenCalledWith("/proj");
  });

  it("未设置项目时不触发刷新等动作", async () => {
    mockAppState.currentProject = "";
    const wrapper = mountGitPanel();
    vi.clearAllMocks();

    await wrapper.find(".git-panel__refresh").trigger("click");
    await flushPromises();

    // repoPath 为空时 handleRefresh 直接返回
    expect(refreshGitMock).not.toHaveBeenCalled();
    mockAppState.currentProject = "/proj";
  });

  // --- 优先级 3: Git Stash / Tag / Amend 测试 ---

  it("渲染 stash 列表，显示 stash ref 与消息", () => {
    stashState.stashes = [
      { ref: "stash@{0}", commitHash: "abc1234", message: "WIP: feature" },
      { ref: "stash@{1}", commitHash: "def5678", message: "WIP: bugfix" },
    ];
    const wrapper = mountGitPanel();
    const rows = wrapper.findAll(".git-panel__stash-row");
    expect(rows).toHaveLength(2);
    expect(wrapper.text()).toContain("stash@{0}");
    expect(wrapper.text()).toContain("WIP: feature");
    expect(wrapper.text()).toContain("stash@{1}");
  });

  it("stash 为空时显示空状态文案", () => {
    stashState.stashes = [];
    const wrapper = mountGitPanel();
    expect(wrapper.find(".git-panel__stash-empty").exists()).toBe(true);
  });

  it("shows stash actions when keyboard focus enters the row", async () => {
    stashState.stashes = [
      { ref: "stash@{0}", commitHash: "abc1234", message: "WIP" },
    ];
    const wrapper = mountGitPanel({ attachTo: document.body });
    const action = wrapper.find<HTMLButtonElement>(".git-panel__stash-action");

    action.element.focus();
    await nextTick();

    expect(document.activeElement).toBe(action.element);
    expect(window.getComputedStyle(action.element.parentElement!).opacity).toBe("1");
    wrapper.unmount();
  });

  it("点击 Stash 按钮调用 stashPush store 动作", async () => {
    const wrapper = mountGitPanel();
    vi.clearAllMocks();
    stashPushMock.mockResolvedValue(undefined);
    loadStashesMock.mockResolvedValue(undefined);
    refreshGitMock.mockResolvedValue(undefined);

    const stashInput = wrapper.find(".git-panel__stash-input");
    await stashInput.setValue("WIP: save current work");
    await wrapper.find(".git-panel__stash-btn").trigger("click");
    await flushPromises();

    expect(stashPushMock).toHaveBeenCalledWith("WIP: save current work");
  });

  it("点击 stash Pop 按钮调用 stashPop store 动作", async () => {
    stashState.stashes = [
      { ref: "stash@{0}", commitHash: "abc1234", message: "WIP" },
    ];
    const wrapper = mountGitPanel();
    vi.clearAllMocks();
    stashPopMock.mockResolvedValue(undefined);

    const popBtn = wrapper.find(".git-panel__stash-row .git-panel__stash-action");
    await popBtn.trigger("click");
    await flushPromises();

    expect(stashPopMock).toHaveBeenCalledWith("stash@{0}");
  });

  it("渲染 tag 列表，显示 tag 名称与消息", () => {
    tagState.tags = [
      { name: "v1.0.0", commitHash: "abc1234", message: "Release 1.0.0" },
      { name: "v2.0.0", commitHash: "def5678", message: "Release 2.0.0" },
    ];
    const wrapper = mountGitPanel();
    const rows = wrapper.findAll(".git-panel__tags-row");
    expect(rows).toHaveLength(2);
    expect(wrapper.text()).toContain("v1.0.0");
    expect(wrapper.text()).toContain("Release 2.0.0");
  });

  it("tag 为空时显示空状态文案", () => {
    tagState.tags = [];
    const wrapper = mountGitPanel();
    expect(wrapper.find(".git-panel__tags-empty").exists()).toBe(true);
  });

  it("输入标签名并点击创建标签按钮，调用 createTag store 动作", async () => {
    const wrapper = mountGitPanel();
    vi.clearAllMocks();
    createTagMock.mockResolvedValue(undefined);
    loadTagsMock.mockResolvedValue(undefined);

    // tags-input 是 name 与 message 两个输入框
    const inputs = wrapper.findAll(".git-panel__tags-input");
    expect(inputs.length).toBeGreaterThanOrEqual(2);
    await inputs[0].setValue("v1.2.3");
    await inputs[1].setValue("Release v1.2.3");
    await wrapper.find(".git-panel__tags-btn").trigger("click");
    await flushPromises();

    expect(createTagMock).toHaveBeenCalledWith("v1.2.3", "Release v1.2.3");
  });

  it("未输入标签名时创建按钮被禁用", () => {
    const wrapper = mountGitPanel();
    const btn = wrapper.find(".git-panel__tags-btn");
    expect(btn.attributes("disabled")).toBeDefined();
  });

  it("勾选 Amend 后提交按钮调用 amendCommit store 动作", async () => {
    const wrapper = mountGitPanel();
    vi.clearAllMocks();
    amendCommitMock.mockResolvedValue(undefined);

    // 输入提交信息
    const input = wrapper.find(".git-panel__commit-input");
    await input.setValue("fix: typo in docs");

    // 勾选 Amend 复选框
    const amendCheckbox = wrapper.find(".git-panel__amend-checkbox");
    await amendCheckbox.setValue(true);

    // 提交按钮文案应切换为修订提交
    const commitBtn = wrapper.find(".git-panel__commit-btn");
    expect(commitBtn.text()).toContain("Amend Commit");

    await commitBtn.trigger("click");
    await flushPromises();

    expect(amendCommitMock).toHaveBeenCalledWith("/proj", "fix: typo in docs");
    // 提交后 amendMode 应被重置为 false
    expect((amendCheckbox.element as HTMLInputElement).checked).toBe(false);
  });

  it("未勾选 Amend 时提交按钮调用 commitChanges store 动作", async () => {
    const wrapper = mountGitPanel();
    vi.clearAllMocks();
    commitChangesMock.mockResolvedValue(undefined);

    const input = wrapper.find(".git-panel__commit-input");
    await input.setValue("feat: new feature");
    await wrapper.find(".git-panel__commit-btn").trigger("click");
    await flushPromises();

    expect(commitChangesMock).toHaveBeenCalledWith("/proj", "feat: new feature");
    expect(amendCommitMock).not.toHaveBeenCalled();
  });
});

// ---------------------------------------------------------------------------
// G17: .code-workspace 不把 workspace 文件当 repo 根
// ---------------------------------------------------------------------------

describe("G17 git repo root selection", () => {
  it("uses workspaceRoot (directory) when currentProject is a .code-workspace file", async () => {
    mockAppState.currentProject = "C:/ws/my.code-workspace";
    mockAppState.workspaceRoot = "C:/ws";
    const wrapper = mountGitPanel();
    vi.clearAllMocks();

    await wrapper.find(".git-panel__refresh").trigger("click");
    await flushPromises();

    // refreshGit 必须收到目录根，而不是 .code-workspace 文件路径。
    expect(refreshGitMock).toHaveBeenCalledWith("C:/ws");
  });

  it("falls back to currentProject when workspaceRoot is empty (single directory)", async () => {
    mockAppState.currentProject = "/proj";
    mockAppState.workspaceRoot = "";
    const wrapper = mountGitPanel();
    vi.clearAllMocks();

    await wrapper.find(".git-panel__refresh").trigger("click");
    await flushPromises();

    expect(refreshGitMock).toHaveBeenCalledWith("/proj");
  });

  it("lists multi-root and nested repositories and refreshes the selected root", async () => {
    mockAppState.currentProject = "C:/ws/root-a.code-workspace";
    mockAppState.workspaceRoot = "C:/ws/root-a";
    mockAppState.workspaceFolders = ["C:/ws/root-a", "C:/ws/root-b"];
    discoverRepositoriesMock.mockImplementation(async (root: string) => (
      root === "C:/ws/root-a" ? ["C:/ws/root-a/packages/nested"] : []
    ));
    const wrapper = mountGitPanel();
    await flushPromises();

    const picker = wrapper.find("#git-repository-select");
    expect(picker.exists()).toBe(true);
    expect(picker.findAll("option").map((option) => option.element.value)).toEqual([
      "C:/ws/root-a",
      "C:/ws/root-b",
      "C:/ws/root-a/packages/nested",
    ]);

    await picker.setValue("C:/ws/root-a/packages/nested");
    await flushPromises();
    expect(refreshGitMock).toHaveBeenCalledWith("C:/ws/root-a/packages/nested");
  });
});
