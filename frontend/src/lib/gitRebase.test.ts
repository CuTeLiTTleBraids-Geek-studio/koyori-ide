import { flushPromises, mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";

const runtime = vi.hoisted(() => ({
  byName: vi.fn(),
}));

const monacoMock = vi.hoisted(() => {
  const models: Array<{ disposed: boolean }> = [];
  const editors: Array<{ disposed: boolean; kind: "code" | "diff" }> = [];
  let decorationSequence = 0;

  const createModel = vi.fn((initialValue: string) => {
    let value = initialValue;
    const listeners = new Set<() => void>();
    const model = {
      disposed: false,
      getValue: () => value,
      setValue: (nextValue: string) => {
        value = nextValue;
        for (const listener of listeners) listener();
      },
      onDidChangeContent: (listener: () => void) => {
        listeners.add(listener);
        return { dispose: () => listeners.delete(listener) };
      },
      deltaDecorations: (_oldIds: string[], decorations: unknown[]) =>
        decorations.map(() => `decoration-${++decorationSequence}`),
      dispose: () => {
        model.disposed = true;
        listeners.clear();
      },
    };
    models.push(model);
    return model;
  });

  const createDiffEditor = vi.fn(() => {
    const editor = {
      disposed: false,
      kind: "diff" as const,
      setModel: (_model: unknown) => undefined,
      layout: () => undefined,
      updateOptions: (_options: unknown) => undefined,
      dispose: () => {
        editor.disposed = true;
      },
    };
    editors.push(editor);
    return editor;
  });

  const createEditor = vi.fn(() => {
    const editor = {
      disposed: false,
      kind: "code" as const,
      setModel: (_model: unknown) => undefined,
      layout: () => undefined,
      updateOptions: (_options: unknown) => undefined,
      revealLineInCenter: (_line: number) => undefined,
      setPosition: (_position: unknown) => undefined,
      focus: () => undefined,
      dispose: () => {
        editor.disposed = true;
      },
    };
    editors.push(editor);
    return editor;
  });

  class Range {
    constructor(
      readonly startLineNumber: number,
      readonly startColumn: number,
      readonly endLineNumber: number,
      readonly endColumn: number,
    ) {}
  }

  return {
    models,
    editors,
    createModel,
    createDiffEditor,
    createEditor,
    setModelLanguage: vi.fn(),
    Range,
  };
});

vi.mock("@wailsio/runtime", () => ({
  Call: { ByID: runtime.byName },
  Create: {
    Any: (value: unknown) => value,
    Array: () => (value: unknown) => value,
    Map: () => (value: unknown) => value,
    Nullable: () => (value: unknown) => value,
    Struct: () => (value: unknown) => value,
  },
}));

vi.mock("monaco-editor", () => ({
  Range: monacoMock.Range,
  editor: {
    createModel: monacoMock.createModel,
    createDiffEditor: monacoMock.createDiffEditor,
    create: monacoMock.createEditor,
    setModelLanguage: monacoMock.setModelLanguage,
    OverviewRulerLane: { Full: 7 },
  },
}));

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({
    t: (key: string) => key,
  }),
}));

import RebaseEditor from "@/components/git/RebaseEditor.vue";
import MergeEditor, {
  createConflictRegionResult,
  type ConflictRegion,
} from "@/components/git/MergeEditor.vue";
import {
  abortRebase,
  applyRebaseActions,
  continueRebase,
  gitRebaseState,
  hasValidRebaseActionOrder,
  loadRebaseTodoList,
  moveRebaseAction,
  reorderRebaseActions,
  resetGitRebaseStore,
  setGitRebaseServiceBindings,
  skipRebaseCommit,
  startInteractiveRebase,
  updateRebaseAction,
  useGitRebaseStore,
  type GitRebaseServiceBindings,
  type GitRebaseStatus,
  type RebaseTodoAction,
} from "./gitRebase";

const fixtures: RebaseTodoAction[] = [
  {
    action: "pick",
    commitSha: "1111111111111111111111111111111111111111",
    shortMessage: "first commit",
    authorName: "Ada",
    authorEmail: "ada@example.com",
  },
  {
    action: "pick",
    commitSha: "2222222222222222222222222222222222222222",
    shortMessage: "second commit",
    authorName: "Lin",
  },
  {
    action: "pick",
    commitSha: "3333333333333333333333333333333333333333",
    shortMessage: "third commit",
  },
];

const getTodo = vi.fn(async (): Promise<RebaseTodoAction[]> => fixtures);
const start = vi.fn(async (): Promise<void> => undefined);
const apply = vi.fn(async (): Promise<void> => undefined);
const continueOperation = vi.fn(async (): Promise<void> => undefined);
const abort = vi.fn(async (): Promise<void> => undefined);
const skip = vi.fn(async (): Promise<void> => undefined);
const isInProgress = vi.fn(async (): Promise<boolean> => false);
const getStatus = vi.fn(async (): Promise<GitRebaseStatus> => ({
  inProgress: false,
  owned: false,
}));

const service: GitRebaseServiceBindings = {
  GetRebaseTodoList: getTodo,
  StartInteractiveRebase: start,
  ApplyRebaseActions: apply,
  ContinueRebase: continueOperation,
  AbortRebase: abort,
  SkipCommit: skip,
  IsRebaseInProgress: isInProgress,
};

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

beforeEach(() => {
  resetGitRebaseStore();
  vi.clearAllMocks();
  getTodo.mockResolvedValue(fixtures);
  isInProgress.mockResolvedValue(false);
  getStatus.mockResolvedValue({ inProgress: false, owned: false });
  setGitRebaseServiceBindings(service);
  monacoMock.models.length = 0;
  monacoMock.editors.length = 0;
});

describe("git rebase store", () => {
  it("loads and normalizes the todo list and progress state", async () => {
    isInProgress.mockResolvedValueOnce(true);

    const loaded = await loadRebaseTodoList(" C:/repo ", " main ");

    expect(getTodo).toHaveBeenCalledWith("C:/repo", "main");
    expect(isInProgress).toHaveBeenCalledWith("C:/repo");
    expect(loaded).toEqual(fixtures);
    expect(loaded).not.toBe(fixtures);
    expect(gitRebaseState.actions).toEqual(fixtures);
    expect(gitRebaseState.inProgress).toBe(true);
    expect(gitRebaseState.loading).toBe(false);
    expect(gitRebaseState.dirty).toBe(false);
  });

  it("ignores a stale load after the repository context changes", async () => {
    const first = deferred<RebaseTodoAction[]>();
    getTodo
      .mockImplementationOnce(() => first.promise)
      .mockResolvedValueOnce([fixtures[2]]);

    const oldLoad = loadRebaseTodoList("C:/old", "main");
    const newLoad = loadRebaseTodoList("C:/new", "develop");
    await newLoad;
    first.resolve([fixtures[0]]);
    await oldLoad;

    expect(gitRebaseState.repoPath).toBe("C:/new");
    expect(gitRebaseState.upstreamBranch).toBe("develop");
    expect(gitRebaseState.actions.map((action) => action.commitSha)).toEqual([
      fixtures[2].commitSha,
    ]);
  });

  it("does not leak rebase progress into a different repository", async () => {
    isInProgress.mockResolvedValueOnce(true);
    await loadRebaseTodoList("C:/old", "main");
    expect(gitRebaseState.inProgress).toBe(true);

    getTodo.mockRejectedValueOnce(new Error("new repository unavailable"));
    await expect(loadRebaseTodoList("C:/new", "main")).rejects.toThrow(
      "new repository unavailable",
    );

    expect(gitRebaseState.repoPath).toBe("C:/new");
    expect(gitRebaseState.inProgress).toBe(false);
  });

  it("does not apply a todo loaded from an already active rebase", async () => {
    isInProgress.mockResolvedValueOnce(true);
    await loadRebaseTodoList("C:/repo", "main");

    expect(gitRebaseState.inProgress).toBe(true);
    expect(gitRebaseState.startPrepared).toBe(false);
    await expect(applyRebaseActions()).rejects.toThrow("Start this rebase");
    expect(apply).not.toHaveBeenCalled();
  });

  it("restores owned phases and action snapshots without adopting foreign rebases", async () => {
    const statusService = { ...service, GetRebaseStatus: getStatus };
    setGitRebaseServiceBindings(statusService);
    const readyActions = fixtures.map((action, index) => ({
      ...action,
      action: index === 1 ? "reword" as const : action.action,
      shortMessage: index === 1 ? "restored subject" : action.shortMessage,
    }));
    getStatus.mockResolvedValueOnce({
      inProgress: true,
      owned: true,
      phase: "ready",
      upstream: "a".repeat(40),
      upstreamRef: "main",
      actions: readyActions,
    });

    await loadRebaseTodoList("C:/repo", "main");
    expect(gitRebaseState.phase).toBe("ready");
    expect(gitRebaseState.startPrepared).toBe(true);
    expect(gitRebaseState.actionsApplied).toBe(true);
    expect(gitRebaseState.rebaseAdvanced).toBe(false);
    expect(gitRebaseState.actions[1].shortMessage).toBe("restored subject");
    expect(moveRebaseAction(0, 1)).toBe(false);
    expect(updateRebaseAction(0, "drop")).toBe(false);
    await expect(applyRebaseActions()).rejects.toThrow("Start this rebase");
    expect(apply).not.toHaveBeenCalled();

    getStatus.mockResolvedValueOnce({
      inProgress: true,
      owned: true,
      phase: "stopped",
      stopReason: "commandError",
      upstream: "a".repeat(40),
      upstreamRef: "main",
      actions: readyActions,
    });
    await loadRebaseTodoList("C:/repo", "main");
    expect(gitRebaseState.phase).toBe("stopped");
    expect(gitRebaseState.rebaseAdvanced).toBe(true);
    expect(gitRebaseState.stopReason).toBe("commandError");
    gitRebaseState.stopReason = "manual";
    await expect(skipRebaseCommit()).rejects.toThrow("stopped applied rebase");
    expect(skip).not.toHaveBeenCalled();

    getStatus.mockResolvedValueOnce({
      inProgress: true,
      owned: true,
      phase: "ready",
      upstream: "a".repeat(40),
      upstreamRef: "main",
      actions: readyActions,
    });
    await expect(loadRebaseTodoList("C:/repo", "develop")).rejects.toThrow(
      "different upstream",
    );
    expect(gitRebaseState.phase).toBe("foreign");
    expect(gitRebaseState.actions).toEqual([]);

    getStatus.mockResolvedValueOnce({ inProgress: true, owned: false });
    getTodo.mockClear();
    await loadRebaseTodoList("C:/repo", "main");
    expect(gitRebaseState.phase).toBe("foreign");
    expect(gitRebaseState.actions).toEqual([]);
    expect(getTodo).not.toHaveBeenCalled();
  });

  it("refreshes the owned stop phase when continue reports a conflict", async () => {
    setGitRebaseServiceBindings({ ...service, GetRebaseStatus: getStatus });
    await loadRebaseTodoList("C:/repo", "main");
    await startInteractiveRebase();
    await applyRebaseActions();
    continueOperation.mockRejectedValueOnce(new Error("merge conflict"));
    getStatus.mockResolvedValueOnce({
      inProgress: true,
      owned: true,
      phase: "stopped",
      stopReason: "commandError",
      upstream: "a".repeat(40),
      upstreamRef: "main",
      actions: fixtures,
    });

    await expect(continueRebase()).rejects.toThrow("merge conflict");
    expect(gitRebaseState.phase).toBe("stopped");
    expect(gitRebaseState.stopReason).toBe("commandError");
    expect(gitRebaseState.actionsApplied).toBe(true);
    expect(gitRebaseState.error).toBe("merge conflict");
  });

  it("reorders immutably and updates an action", async () => {
    await loadRebaseTodoList("C:/repo", "main");
    const original = gitRebaseState.actions;

    expect(moveRebaseAction(0, 2)).toBe(true);
    expect(gitRebaseState.actions).not.toBe(original);
    expect(gitRebaseState.actions.map((action) => action.commitSha)).toEqual([
      fixtures[1].commitSha,
      fixtures[2].commitSha,
      fixtures[0].commitSha,
    ]);
    expect(updateRebaseAction(1, "squash")).toBe(true);
    expect(gitRebaseState.actions[1].action).toBe("squash");
    expect(gitRebaseState.dirty).toBe(true);
    expect(fixtures.every((action) => action.action === "pick")).toBe(true);
  });

  it("returns a cloned list for invalid or no-op pure reorders", () => {
    const samePosition = reorderRebaseActions(fixtures, 1, 1);
    const invalidPosition = reorderRebaseActions(fixtures, -1, 2);

    expect(samePosition).toEqual(fixtures);
    expect(samePosition).not.toBe(fixtures);
    expect(invalidPosition).toEqual(fixtures);
    expect(invalidPosition[0]).not.toBe(fixtures[0]);
  });

  it("rejects fold actions without a previous replay target", async () => {
    const invalid: RebaseTodoAction[] = fixtures.map((action) => ({ ...action }));
    invalid[0] = { ...invalid[0], action: "drop" };
    invalid[1] = { ...invalid[1], action: "fixup" };
    expect(hasValidRebaseActionOrder(invalid)).toBe(false);
    expect(hasValidRebaseActionOrder([
      { ...fixtures[0] },
      { ...fixtures[1], action: "fixup" },
    ])).toBe(true);

    await loadRebaseTodoList("C:/repo", "main");
    await startInteractiveRebase();
    expect(updateRebaseAction(0, "drop")).toBe(true);
    expect(updateRebaseAction(1, "fixup")).toBe(true);
    await expect(applyRebaseActions()).rejects.toThrow(
      "Squash and fixup actions require a previous commit",
    );
    expect(apply).not.toHaveBeenCalled();
  });
  it("runs start, apply, continue, skip, and abort with coherent state", async () => {
    await loadRebaseTodoList("C:/repo", "main");
    updateRebaseAction(1, "reword");

    await startInteractiveRebase();
    expect(start).toHaveBeenCalledWith("C:/repo", "main");
    expect(gitRebaseState.inProgress).toBe(true);
    expect(gitRebaseState.actionsApplied).toBe(false);
    await expect(continueRebase()).rejects.toThrow("Apply rebase actions");
    await expect(skipRebaseCommit()).rejects.toThrow("stopped applied rebase");

    await applyRebaseActions();
    expect(apply).toHaveBeenCalledWith(
      "C:/repo",
      expect.arrayContaining([expect.objectContaining({ action: "reword" })]),
    );
    expect(gitRebaseState.dirty).toBe(false);
    expect(gitRebaseState.actionsApplied).toBe(true);

    isInProgress.mockResolvedValueOnce(true);
    await continueRebase();
    expect(continueOperation).toHaveBeenCalledWith("C:/repo");
    expect(gitRebaseState.inProgress).toBe(true);
    expect(gitRebaseState.rebaseAdvanced).toBe(true);

    isInProgress.mockResolvedValueOnce(true);
    await skipRebaseCommit();
    expect(skip).toHaveBeenCalledWith("C:/repo");

    isInProgress.mockResolvedValueOnce(false);
    await continueRebase();
    expect(gitRebaseState.inProgress).toBe(false);
    expect(gitRebaseState.actions).toEqual([]);

    await loadRebaseTodoList("C:/repo", "main");
    gitRebaseState.inProgress = true;
    gitRebaseState.owned = true;
    gitRebaseState.phase = "ready";
    await abortRebase();
    expect(abort).toHaveBeenCalledWith("C:/repo");
    expect(gitRebaseState.actions).toEqual([]);
  });

  it("records and rethrows binding failures", async () => {
    getTodo.mockRejectedValueOnce(new Error("log failed"));

    await expect(loadRebaseTodoList("C:/repo", "main")).rejects.toThrow("log failed");
    expect(gitRebaseState.error).toBe("log failed");
    expect(gitRebaseState.loading).toBe(false);
  });

  it("rejects malformed backend lists and cross-repository apply attempts", async () => {
    getTodo.mockResolvedValueOnce([fixtures[0], { ...fixtures[0] }]);
    await expect(loadRebaseTodoList("C:/repo", "main")).rejects.toThrow(
      "invalid or duplicate",
    );
    expect(gitRebaseState.error).toContain("invalid or duplicate");

    getTodo.mockResolvedValueOnce(fixtures);
    await loadRebaseTodoList("C:/repo", "main");
    await expect(applyRebaseActions("C:/other")).rejects.toThrow(
      "different repository",
    );
    expect(apply).not.toHaveBeenCalled();
  });
});

describe("RebaseEditor", () => {
  it("switches actions and reorders through drag and keyboard controls", async () => {
    await loadRebaseTodoList("C:/repo", "main");
    const wrapper = mount(RebaseEditor, {
      props: {
        repoPath: "C:/repo",
        upstreamBranch: "main",
        autoLoad: false,
        store: useGitRebaseStore(),
      },
    });

    const selects = wrapper.findAll("select");
    await selects[0].setValue("reword");
    expect(gitRebaseState.actions[0].action).toBe("reword");
    await wrapper.get(".rebase-editor__message-input").setValue("Reworded subject");
    expect(gitRebaseState.actions[0].shortMessage).toBe("Reworded subject");

    const transferData = new Map<string, string>();
    const dataTransfer = {
      effectAllowed: "none",
      setData: (type: string, value: string) => transferData.set(type, value),
      getData: (type: string) => transferData.get(type) ?? "",
    };
    let rows = wrapper.findAll("[data-rebase-row]");
    await rows[0].trigger("dragstart", { dataTransfer });
    await rows[2].trigger("drop", { dataTransfer });
    expect(gitRebaseState.actions.map((action) => action.commitSha)).toEqual([
      fixtures[1].commitSha,
      fixtures[2].commitSha,
      fixtures[0].commitSha,
    ]);

    rows = wrapper.findAll("[data-rebase-row]");
    await rows[0].trigger("keydown", { key: "ArrowDown", altKey: true });
    expect(gitRebaseState.actions[1].commitSha).toBe(fixtures[1].commitSha);

    rows = wrapper.findAll("[data-rebase-row]");
    await rows[1].trigger("keydown", { key: " ", code: "Space" });
    expect(gitRebaseState.actions[1].action).toBe("reword");
    expect(wrapper.emitted("update:actions")?.length).toBeGreaterThanOrEqual(3);
  });

  it("starts and confirms applying the visible order", async () => {
    await loadRebaseTodoList("C:/repo", "main");
    const wrapper = mount(RebaseEditor, {
      props: {
        repoPath: "C:/repo",
        upstreamBranch: "main",
        autoLoad: false,
      },
    });

    await wrapper.get('[aria-label="rebaseEditor.start"]').trigger("click");
    await flushPromises();
    expect(start).toHaveBeenCalledOnce();
    expect(
      wrapper.get<HTMLButtonElement>('[aria-label="git.continueRebase"]').element.disabled,
    ).toBe(true);
    expect(
      wrapper.get<HTMLButtonElement>('[aria-label="rebaseEditor.skip"]').element.disabled,
    ).toBe(true);

    await wrapper.get('[aria-label="rebaseEditor.apply"]').trigger("click");
    const dialog = wrapper.get('[role="alertdialog"]');
    for (const button of dialog.findAll("button")) {
      expect(button.attributes("aria-label")).toBeTruthy();
    }
    await wrapper.get(".rebase-editor__dialog .rebase-editor__command--primary").trigger("click");
    await flushPromises();

    expect(apply).toHaveBeenCalledOnce();
    expect(wrapper.emitted("applied")).toHaveLength(1);
    expect(
      wrapper.get<HTMLButtonElement>('[aria-label="git.continueRebase"]').element.disabled,
    ).toBe(false);
  });

  it("closes an apply confirmation when its repository context changes", async () => {
    await loadRebaseTodoList("C:/repo", "main");
    const wrapper = mount(RebaseEditor, {
      props: {
        repoPath: "C:/repo",
        upstreamBranch: "main",
        autoLoad: false,
      },
    });

    await wrapper.get('[aria-label="rebaseEditor.start"]').trigger("click");
    await flushPromises();
    await wrapper.get('[aria-label="rebaseEditor.apply"]').trigger("click");
    expect(wrapper.find('[role="alertdialog"]').exists()).toBe(true);

    await wrapper.setProps({ repoPath: "C:/other" });
    await flushPromises();
    expect(wrapper.find('[role="alertdialog"]').exists()).toBe(false);
    expect(apply).not.toHaveBeenCalled();
  });
  it("disables fold actions without a previous replay target", async () => {
    await loadRebaseTodoList("C:/repo", "main");
    const wrapper = mount(RebaseEditor, {
      props: { repoPath: "C:/repo", upstreamBranch: "main", autoLoad: false },
    });
    await wrapper.get('[aria-label="rebaseEditor.start"]').trigger("click");
    await flushPromises();

    const firstSelect = wrapper.findAll("select")[0];
    expect(firstSelect.get('option[value="squash"]').attributes()).toHaveProperty("disabled");
    expect(firstSelect.get('option[value="fixup"]').attributes()).toHaveProperty("disabled");
    expect(updateRebaseAction(0, "squash")).toBe(true);
    await flushPromises();
    expect(
      wrapper.get<HTMLButtonElement>('[aria-label="rebaseEditor.apply"]').element.disabled,
    ).toBe(true);
    wrapper.unmount();
  });
});

describe("MergeEditor", () => {
  const regions: ConflictRegion[] = [
    {
      startLine: 2,
      endLine: 2,
      oursLines: ["ours one"],
      theirsLines: ["theirs one"],
      baseLines: ["base one"],
    },
    {
      startLine: 4,
      endLine: 4,
      oursLines: ["ours two"],
      theirsLines: ["theirs two"],
      baseLines: ["base two"],
    },
  ];
  const ours = "header\nours one\nmiddle\nours two\ntail";

  it("creates one standard marker block for every supplied conflict region", () => {
    const result = createConflictRegionResult(ours, regions);

    expect(result.match(/^<<<<<<< OURS$/gm)).toHaveLength(2);
    expect(result).toContain("theirs one");
    expect(result).toContain("theirs two");
  });

  it("resolves blocks independently, emits save and abort, and disposes Monaco resources", async () => {
    const wrapper = mount(MergeEditor, {
      props: {
        filePath: "src/file.ts",
        oursContent: ours,
        theirsContent: "header\ntheirs one\nmiddle\ntheirs two\ntail",
        baseContent: "header\nbase one\nmiddle\nbase two\ntail",
        conflictRegions: regions,
      },
    });
    await flushPromises();

    expect(monacoMock.createDiffEditor).toHaveBeenCalledTimes(2);
    expect(monacoMock.createEditor).toHaveBeenCalledTimes(1);
    expect(wrapper.findAll(".merge-editor__conflict")).toHaveLength(2);

    await wrapper.findAll('[aria-label="mergeEditor.acceptOursAria"]')[0].trigger("click");
    expect(wrapper.findAll(".merge-editor__conflict")).toHaveLength(1);
    await wrapper.findAll('[aria-label="mergeEditor.acceptIncomingAria"]')[0].trigger("click");
    expect(wrapper.findAll(".merge-editor__conflict")).toHaveLength(0);

    await wrapper.get('[aria-label="mergeEditor.saveResultAria"]').trigger("click");
    expect(wrapper.emitted("save")).toEqual([[
      {
        filePath: "src/file.ts",
        content: "header\nours one\nmiddle\ntheirs two\ntail",
      },
    ]]);

    await wrapper.get('[aria-label="mergeEditor.abortAria"]').trigger("click");
    expect(wrapper.emitted("abort")).toHaveLength(1);

    wrapper.unmount();
    expect(monacoMock.models).toHaveLength(5);
    expect(monacoMock.models.every((model) => model.disposed)).toBe(true);
    expect(monacoMock.editors).toHaveLength(3);
    expect(monacoMock.editors.every((editor) => editor.disposed)).toBe(true);
  });
  it("rebuilds a manual result when the repository identity changes", async () => {
    const wrapper = mount(MergeEditor, {
      props: {
        repoPath: "C:/repo-a",
        filePath: "src/file.ts",
        oursContent: ours,
        theirsContent: "header\ntheirs one\nmiddle\ntheirs two\ntail",
        baseContent: "header\nbase one\nmiddle\nbase two\ntail",
        conflictRegions: regions,
      },
    });
    await flushPromises();
    const resultModel = monacoMock.models[2] as unknown as {
      setValue(value: string): void;
    };
    const exposed = wrapper.vm as unknown as { getResult(): string };
    resultModel.setValue("manual result from repo a");
    expect(exposed.getResult()).toBe("manual result from repo a");

    await wrapper.setProps({ repoPath: "C:/repo-b" });
    await flushPromises();
    expect(exposed.getResult()).toBe(createConflictRegionResult(ours, regions));
    wrapper.unmount();
  });
});
