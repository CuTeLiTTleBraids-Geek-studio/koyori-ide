import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  getCodeActions: vi.fn(),
  applyRefactorWorkspaceEdit: vi.fn(),
  executeRefactorCommand: vi.fn(),
  updateContent: vi.fn(),
  markSaved: vi.fn(),
  openFiles: [] as Array<{
    path: string;
    content: string;
    originalContent: string;
    isDirty: boolean;
  }>,
}));

vi.mock("@/stores/lsp", () => ({ getLSPCodeActions: mocks.getCodeActions }));
vi.mock("../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/lspservice.js", () => ({
  ApplyRefactorWorkspaceEdit: mocks.applyRefactorWorkspaceEdit,
  ExecuteRefactorCommand: mocks.executeRefactorCommand,
}));
vi.mock("@/stores/editor", () => ({
  editorState: { openFiles: mocks.openFiles },
  updateContent: mocks.updateContent,
  markSaved: mocks.markSaved,
}));

import {
  applySelectedRefactor,
  cancelRefactorPreview,
  cancelRefactorRequest,
  refactorState,
  refreshRefactorActions,
  resetRefactorState,
  selectRefactorCommand,
} from "./refactor";

const request = {
  language: "go",
  filePath: "/workspace/main.go",
  line: 2,
  column: 3,
  endLine: 5,
  endColumn: 7,
  content: "package main\n",
};

describe("9E refactor store", () => {
  beforeEach(() => {
    mocks.getCodeActions.mockReset();
    mocks.applyRefactorWorkspaceEdit.mockReset();
    mocks.executeRefactorCommand.mockReset();
    mocks.updateContent.mockReset();
    mocks.markSaved.mockReset();
    mocks.openFiles.splice(0);
    resetRefactorState();
  });

  it("requests only the three LSP refactor families and enables only returned actions", async () => {
    mocks.getCodeActions.mockResolvedValue([
      {
        title: "Extract function",
        kind: "refactor.extract",
        command: "gopls.extract_function",
      },
      {
        title: "Inline call",
        kind: "refactor.inline",
        command: "gopls.inline",
      },
      { title: "Move declaration", kind: "refactor.rewrite", disabled: true },
      {
        title: "Change signature",
        kind: "refactor.rewrite",
        command: "server.changeSignature",
      },
    ]);

    await refreshRefactorActions(request);

    expect(mocks.getCodeActions).toHaveBeenCalledWith(
      "go",
      "/workspace/main.go",
      2,
      3,
      "package main\n",
      {
        endLine: 5,
        endColumn: 7,
        only: ["refactor.extract", "refactor.inline", "refactor.rewrite"],
      },
    );
    expect(refactorState.available["extract-method"]).toBe(true);
    expect(refactorState.available.inline).toBe(true);
    expect(refactorState.available.move).toBe(false);
    expect(refactorState.available["change-signature"]).toBe(true);
    expect(refactorState.available["extract-variable"]).toBe(false);
    expect(refactorState.available["extract-interface"]).toBe(false);
    expect(refactorState.available["extract-superclass"]).toBe(false);
  });

  it("ignores a late response after a newer request", async () => {
    let resolveFirst!: (value: unknown[]) => void;
    const first = new Promise<unknown[]>((resolve) => {
      resolveFirst = resolve;
    });
    mocks.getCodeActions
      .mockReturnValueOnce(first)
      .mockResolvedValueOnce([
        { title: "Inline", kind: "refactor.inline", command: "server.inline" },
      ]);

    const pending = refreshRefactorActions(request);
    const latest = refreshRefactorActions({
      ...request,
      filePath: "/workspace/new.go",
    });
    await latest;
    resolveFirst([{ title: "Extract variable", kind: "refactor.extract" }]);
    await pending;

    expect(refactorState.request?.filePath).toBe("/workspace/new.go");
    expect(refactorState.available.inline).toBe(true);
    expect(refactorState.available["extract-variable"]).toBe(false);
  });

  it("cancels an in-flight request without accepting its response", async () => {
    let resolveRequest!: (value: unknown[]) => void;
    mocks.getCodeActions.mockReturnValue(
      new Promise<unknown[]>((resolve) => {
        resolveRequest = resolve;
      }),
    );
    const pending = refreshRefactorActions(request);
    cancelRefactorRequest();
    resolveRequest([
      { title: "Inline", kind: "refactor.inline", command: "server.inline" },
    ]);
    await pending;

    expect(refactorState.loading).toBe(false);
    expect(refactorState.available.inline).toBe(false);
  });

  it("keeps buffers unchanged when the backend rejects a multi-file preview conflict", async () => {
    const preview = {
      files: [
        {
          filePath: "/workspace/main.go",
          version: 4,
          baselineHash: "abc",
          originalContent: "old",
          modifiedContent: "new",
        },
      ],
    };
    mocks.getCodeActions.mockResolvedValue([
      { title: "Extract function", kind: "refactor.extract", preview },
    ]);
    mocks.applyRefactorWorkspaceEdit.mockResolvedValue({
      applied: false,
      failureReason: "workspace edit conflict",
      conflicts: ["/workspace/main.go: version conflict"],
    });
    await refreshRefactorActions(request);
    expect(selectRefactorCommand("extract-method")).toBe(true);

    const applied = await applySelectedRefactor();

    expect(applied).toBe(false);
    expect(mocks.applyRefactorWorkspaceEdit).toHaveBeenCalledWith("go", preview);
    expect(mocks.updateContent).not.toHaveBeenCalled();
    expect(refactorState.error).toContain("version conflict");
  });

  it("binds backend apply cancellation to closing the preview", async () => {
    const preview = {
      files: [
        {
          filePath: "/workspace/main.go",
          baselineHash: "abc",
          originalContent: "old",
          modifiedContent: "new",
        },
      ],
    };
    mocks.getCodeActions.mockResolvedValue([
      { title: "Extract function", kind: "refactor.extract", preview },
    ]);
    let resolveApply!: (value: { applied: boolean }) => void;
    const call = new Promise<{ applied: boolean }>((resolve) => {
      resolveApply = resolve;
    }) as Promise<{ applied: boolean }> & {
      cancelOn: (signal: AbortSignal) => Promise<{ applied: boolean }>;
    };
    call.cancelOn = vi.fn((signal: AbortSignal) => {
      signal.addEventListener("abort", () => resolveApply({ applied: false }), {
        once: true,
      });
      return call;
    });
    mocks.applyRefactorWorkspaceEdit.mockReturnValue(call);
    await refreshRefactorActions(request);
    selectRefactorCommand("extract-method");

    const pending = applySelectedRefactor();
    cancelRefactorPreview();

    expect(call.cancelOn).toHaveBeenCalledTimes(1);
    expect(vi.mocked(call.cancelOn).mock.calls[0][0].aborted).toBe(true);
    await expect(pending).resolves.toBe(false);
    expect(refactorState.previewVisible).toBe(false);
    expect(refactorState.applying).toBe(false);
  });

  it("executes a refactor command through the generated binding", async () => {
    mocks.getCodeActions.mockResolvedValue([
      {
        title: "Extract function",
        kind: "refactor.extract",
        command: "gopls.extract_function",
        commandArguments: ["main.go", 2],
      },
    ]);
    mocks.executeRefactorCommand.mockResolvedValue(undefined);
    await refreshRefactorActions(request);
    expect(selectRefactorCommand("extract-method")).toBe(true);

    await expect(applySelectedRefactor()).resolves.toBe(true);

    expect(mocks.executeRefactorCommand).toHaveBeenCalledWith(
      "go",
      "gopls.extract_function",
      ["main.go", 2],
    );
  });
});
