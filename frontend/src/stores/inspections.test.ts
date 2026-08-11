import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Diagnostic, LSPTextEdit } from "@/types";

const api = vi.hoisted(() => ({
  listAllFiles: vi.fn(),
  readFile: vi.fn(),
  getDiagnostics: vi.fn(),
}));

const lsp = vi.hoisted(() => ({
  ensureRunning: vi.fn(),
  getCodeActions: vi.fn(),
}));

const editor = vi.hoisted(() => ({
  state: { openFiles: [] as Array<Record<string, unknown>>, activeFilePath: null as string | null },
  openFile: vi.fn(),
  updateContent: vi.fn(),
}));

const edits = vi.hoisted(() => ({ apply: vi.fn() }));

vi.mock("@/api/services", () => ({
  fileService: { listAllFiles: api.listAllFiles, readFile: api.readFile },
  lspService: { getDiagnostics: api.getDiagnostics },
}));

vi.mock("@/stores/lsp", () => ({
  ensureLSPRunning: lsp.ensureRunning,
  getLSPCodeActions: lsp.getCodeActions,
  diagnosticServerLanguages: (language: string) => language === "typescript"
    ? ["typescript", "eslint"]
    : [language],
  monacoLanguageToLSP: (language: string) => (
    ["typescript", "go"].includes(language) ? language : null
  ),
}));

vi.mock("@/lib/language", () => ({
  detectLanguage: (path: string) => path.endsWith(".ts")
    ? "typescript"
    : path.endsWith(".go") ? "go" : "plaintext",
}));

vi.mock("@/stores/editor", () => ({
  editorState: editor.state,
  openFileFromPath: editor.openFile,
  updateContent: editor.updateContent,
}));

vi.mock("@/lib/lspCompletion", () => ({
  applyTextEditsToContent: edits.apply,
}));

import {
  applyInspectionQuickFix,
  cancelInspectionRun,
  clearInspectionState,
  inspectionState,
  loadInspectionQuickFixes,
  previewInspectionQuickFix,
  runInspections,
  setInspectionSourceEnabled,
  setInspectionWorkspace,
  updateInspectionProfile,
} from "./inspections";

function diagnostic(overrides: Partial<Diagnostic> = {}): Diagnostic {
  return {
    line: 1,
    column: 2,
    endLine: 1,
    endColumn: 5,
    severity: 2,
    message: "unused value",
    source: "typescript",
    ...overrides,
  };
}

describe("inspections store", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    clearInspectionState();
    api.listAllFiles.mockResolvedValue([]);
    api.readFile.mockResolvedValue("const value = 1;\n");
    api.getDiagnostics.mockResolvedValue([]);
    lsp.ensureRunning.mockResolvedValue(true);
    lsp.getCodeActions.mockResolvedValue([]);
    edits.apply.mockReturnValue("const fixed = 1;\n");
    editor.state.openFiles = [];
    editor.state.activeFilePath = null;
    editor.updateContent.mockReturnValue(true);
  });

  it("persists a project inspection profile and source rules", () => {
    setInspectionWorkspace("/repo");
    updateInspectionProfile({
      name: "Strict project",
      severityThreshold: 2,
      includeGlobs: ["src/**/*.ts"],
      excludeGlobs: ["**/*.test.ts"],
    });
    setInspectionSourceEnabled("eslint", false);

    clearInspectionState();
    setInspectionWorkspace("/repo");

    expect(inspectionState.profile).toMatchObject({
      name: "Strict project",
      severityThreshold: 2,
      includeGlobs: ["src/**/*.ts"],
      excludeGlobs: ["**/*.test.ts"],
      sourceRules: { eslint: { enabled: false } },
    });
  });

  it("runs a filtered batch and merges enabled diagnostic servers", async () => {
    setInspectionWorkspace("/repo");
    updateInspectionProfile({
      severityThreshold: 2,
      includeGlobs: ["src/**/*.ts"],
      excludeGlobs: ["**/*.test.ts"],
    });
    api.listAllFiles.mockResolvedValue(["src/a.ts", "src/a.test.ts", "README.md"]);
    api.getDiagnostics.mockImplementation(async (request: { language: string }) => request.language === "typescript"
      ? [diagnostic({ severity: 1, message: "type error" })]
      : [
        diagnostic({ source: "eslint", severity: 2, message: "lint warning" }),
        diagnostic({ source: "eslint", severity: 3, message: "lint info" }),
      ]);

    await runInspections("/repo");

    expect(lsp.ensureRunning.mock.calls.map(([language]) => language).sort()).toEqual(["eslint", "typescript"]);
    expect(api.readFile).toHaveBeenCalledTimes(1);
    expect(api.readFile).toHaveBeenCalledWith("/repo/src/a.ts");
    expect(inspectionState.findings.map((finding) => finding.message)).toEqual([
      "type error",
      "lint warning",
    ]);
    expect(inspectionState.findings[0]).toMatchObject({
      path: "src/a.ts",
      filePath: "/repo/src/a.ts",
      documentContent: "const value = 1;\n",
    });
  });

  it("ignores a late batch after cancellation", async () => {
    setInspectionWorkspace("/repo");
    api.listAllFiles.mockResolvedValue(["src/a.ts"]);
    let resolveDiagnostics!: (value: Diagnostic[]) => void;
    api.getDiagnostics.mockImplementationOnce(
      () => new Promise<Diagnostic[]>((resolve) => { resolveDiagnostics = resolve; }),
    );

    const pending = runInspections("/repo");
    await vi.waitFor(() => expect(api.getDiagnostics).toHaveBeenCalled());
    cancelInspectionRun();
    resolveDiagnostics([diagnostic()]);
    await pending;

    expect(inspectionState.findings).toEqual([]);
    expect(inspectionState.loading).toBe(false);
  });

  it("inspects the current dirty buffer instead of stale disk content", async () => {
    setInspectionWorkspace("/repo");
    api.listAllFiles.mockResolvedValue(["src/a.ts"]);
    editor.state.openFiles = [{
      path: "/repo/src/a.ts",
      content: "const dirty = true;\n",
    }];
    api.getDiagnostics.mockResolvedValue([diagnostic()]);

    await runInspections("/repo");

    expect(api.readFile).not.toHaveBeenCalled();
    expect(api.getDiagnostics).toHaveBeenCalledWith(expect.objectContaining({
      content: "const dirty = true;\n",
    }));
    expect(inspectionState.findings[0].documentContent).toBe("const dirty = true;\n");
  });

  it("keeps primary findings when a secondary diagnostic server fails", async () => {
    setInspectionWorkspace("/repo");
    api.listAllFiles.mockResolvedValue(["src/a.ts"]);
    api.getDiagnostics.mockImplementation(async (request: { language: string }) => {
      if (request.language === "eslint") throw new Error("eslint offline");
      return [diagnostic({ severity: 1, message: "primary type error" })];
    });

    await runInspections("/repo");

    expect(inspectionState.findings.map((finding) => finding.message)).toEqual(["primary type error"]);
    expect(inspectionState.skippedFiles).toBe(0);
  });

  it("refuses to request quick fixes after the document changes", async () => {
    setInspectionWorkspace("/repo");
    inspectionState.findings = [{
      id: "finding-1",
      path: "src/a.ts",
      filePath: "/repo/src/a.ts",
      language: "typescript",
      server: "typescript",
      line: 1,
      column: 2,
      endLine: 1,
      endColumn: 5,
      severity: 2,
      message: "unused value",
      source: "typescript",
      ruleId: "typescript",
      documentContent: "const value = 1;\n",
    }];
    api.readFile.mockResolvedValueOnce("const changed = 2;\n");

    await loadInspectionQuickFixes("finding-1");

    expect(lsp.getCodeActions).not.toHaveBeenCalled();
    expect(inspectionState.error).toContain("changed since inspection");
  });

  it("previews and applies a single-file quick fix to the dirty buffer", async () => {
    setInspectionWorkspace("/repo");
    const baseline = "const value = 1;\n";
    inspectionState.findings = [{
      id: "finding-1",
      path: "src/a.ts",
      filePath: "/repo/src/a.ts",
      language: "typescript",
      server: "typescript",
      line: 1,
      column: 2,
      endLine: 1,
      endColumn: 5,
      severity: 2,
      message: "unused value",
      source: "typescript",
      ruleId: "typescript",
      documentContent: baseline,
    }];
    editor.state.openFiles = [{ path: "/repo/src/a.ts", content: baseline }];
    const textEdit: LSPTextEdit = {
      startLine: 0,
      startCol: 6,
      endLine: 0,
      endCol: 11,
      newText: "fixed",
    };
    lsp.getCodeActions.mockResolvedValue([{
      title: "Rename unused value",
      kind: "quickfix",
      edit: [{ filePath: "/repo/src/a.ts", edits: [textEdit] }],
    }]);

    await loadInspectionQuickFixes("finding-1");
    await previewInspectionQuickFix("finding-1", 0);

    expect(edits.apply).toHaveBeenCalledWith(baseline, [textEdit]);
    expect(inspectionState.quickFixPreview).toMatchObject({
      findingId: "finding-1",
      title: "Rename unused value",
      originalContent: baseline,
      modifiedContent: "const fixed = 1;\n",
    });

    const applied = await applyInspectionQuickFix();
    expect(applied).toBe(true);
    expect(editor.updateContent).toHaveBeenCalledWith("/repo/src/a.ts", "const fixed = 1;\n");
    expect(inspectionState.findings).toEqual([]);
  });

  it("rejects a stale quick-fix preview before touching the buffer", async () => {
    setInspectionWorkspace("/repo");
    inspectionState.quickFixPreview = {
      findingId: "finding-1",
      title: "Fix",
      filePath: "/repo/src/a.ts",
      originalContent: "const value = 1;\n",
      modifiedContent: "const fixed = 1;\n",
    };
    editor.state.openFiles = [{ path: "/repo/src/a.ts", content: "const changed = 2;\n" }];

    const applied = await applyInspectionQuickFix();

    expect(applied).toBe(false);
    expect(editor.updateContent).not.toHaveBeenCalled();
    expect(inspectionState.error).toContain("changed since quick-fix preview");
  });
});
