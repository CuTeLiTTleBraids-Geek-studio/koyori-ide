import { beforeEach, describe, expect, it, vi } from "vitest";
import type { LSPCompletionItem } from "@/types";

const bindings = vi.hoisted(() => ({
  DetectLSPServers: vi.fn(),
  GetCompletions: vi.fn(),
  GetDiagnostics: vi.fn(),
  GetDefinition: vi.fn(),
  GetReferences: vi.fn(),
  FormatDocument: vi.fn(),
  RenameSymbol: vi.fn(),
  RenameSymbolWorkspace: vi.fn(),
  GetCodeLenses: vi.fn(),
  GetCodeActions: vi.fn(),
  ResolveCodeAction: vi.fn(),
  GetImplementation: vi.fn(),
  OrganizeImports: vi.fn(),
  GetDocumentSymbols: vi.fn(),
  GetWorkspaceSymbols: vi.fn(),
  GetSemanticTokens: vi.fn(),
  GetInlayHints: vi.fn(),
  GetSignatureHelp: vi.fn(),
  GetDeclaration: vi.fn(),
  GetTypeDefinition: vi.fn(),
  GetDocumentLinks: vi.fn(),
  GetSelectionRanges: vi.fn(),
  GetFoldingRanges: vi.fn(),
  PrepareCallHierarchy: vi.fn(),
  CallHierarchyIncomingCalls: vi.fn(),
  CallHierarchyOutgoingCalls: vi.fn(),
  PrepareTypeHierarchy: vi.fn(),
  TypeHierarchySupertypes: vi.fn(),
  TypeHierarchySubtypes: vi.fn(),
  GetDocumentColors: vi.fn(),
  GetColorPresentations: vi.fn(),
  PrepareLinkedEdits: vi.fn(),
}));

vi.mock("@wailsio/runtime", () => ({
  Call: { ByID: vi.fn(), ByName: vi.fn() },
  Create: {
    Any: (value: unknown) => value,
    Array: () => (value: unknown) => value,
    Map: () => (value: unknown) => value,
    Nullable: () => (value: unknown) => value,
    Struct: () => (value: unknown) => value,
  },
}));

vi.mock("../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/lspservice.js", () => bindings);

import {
  fromBindingLSPCompletionItem,
  lspService,
  toBindingLSPCompletionItem,
} from "./lsp";

const request = {
  language: "typescript",
  filePath: "/workspace/src/main.ts",
  line: 0,
  column: 0,
  content: "",
};

const arrayBindings = [
  bindings.DetectLSPServers,
  bindings.GetCompletions,
  bindings.GetDiagnostics,
  bindings.GetDefinition,
  bindings.GetReferences,
  bindings.FormatDocument,
  bindings.RenameSymbol,
  bindings.RenameSymbolWorkspace,
  bindings.GetCodeLenses,
  bindings.GetCodeActions,
  bindings.GetImplementation,
  bindings.OrganizeImports,
  bindings.GetDocumentSymbols,
  bindings.GetWorkspaceSymbols,
  bindings.GetSemanticTokens,
  bindings.GetInlayHints,
  bindings.GetDeclaration,
  bindings.GetTypeDefinition,
  bindings.GetDocumentLinks,
  bindings.GetSelectionRanges,
  bindings.GetFoldingRanges,
  bindings.PrepareCallHierarchy,
  bindings.CallHierarchyIncomingCalls,
  bindings.CallHierarchyOutgoingCalls,
  bindings.PrepareTypeHierarchy,
  bindings.TypeHierarchySupertypes,
  bindings.TypeHierarchySubtypes,
  bindings.GetDocumentColors,
  bindings.GetColorPresentations,
  bindings.PrepareLinkedEdits,
];

describe("LSP service binding normalization", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    for (const binding of arrayBindings) binding.mockResolvedValue(null);
    bindings.GetSignatureHelp.mockResolvedValue(null);
    bindings.ResolveCodeAction.mockResolvedValue({ title: "Resolved" });
  });

  it("normalizes nullable generated arrays to empty arrays", async () => {
    const values = await Promise.all([
      lspService.detectServers(),
      lspService.getCompletions(request),
      lspService.getDiagnostics(request),
      lspService.getDefinition(request),
      lspService.getReferences(request),
      lspService.formatDocument(request),
      lspService.renameSymbol(request, "nextName"),
      lspService.renameSymbolWorkspace(request, "nextName"),
      lspService.getCodeLenses(request),
      lspService.getCodeActions(request),
      lspService.getImplementation(request),
      lspService.organizeImports(request),
      lspService.getDocumentSymbols(request),
      lspService.getWorkspaceSymbols("typescript", "main"),
      lspService.getSemanticTokens(request),
      lspService.getInlayHints(request),
      lspService.getDeclaration(request),
      lspService.getTypeDefinition(request),
      lspService.getDocumentLinks(request),
      lspService.getSelectionRanges(request),
      lspService.getFoldingRanges(request),
      lspService.prepareCallHierarchy(request),
      lspService.callHierarchyIncomingCalls(request, {
        name: "App",
        kind: 5,
        filePath: request.filePath,
        line: 0,
        column: 0,
        endLine: 0,
        endColumn: 3,
        selectionLine: 0,
        selectionColumn: 0,
        selectionEndLine: 0,
        selectionEndColumn: 3,
      }),
      lspService.callHierarchyOutgoingCalls(request, {
        name: "App",
        kind: 5,
        filePath: request.filePath,
        line: 0,
        column: 0,
        endLine: 0,
        endColumn: 3,
        selectionLine: 0,
        selectionColumn: 0,
        selectionEndLine: 0,
        selectionEndColumn: 3,
      }),
      lspService.prepareTypeHierarchy(request),
      lspService.typeHierarchySupertypes(request, {
        name: "App",
        kind: 5,
        filePath: request.filePath,
        line: 0,
        column: 0,
        endLine: 0,
        endColumn: 3,
        selectionLine: 0,
        selectionColumn: 0,
        selectionEndLine: 0,
        selectionEndColumn: 3,
      }),
      lspService.typeHierarchySubtypes(request, {
        name: "App",
        kind: 5,
        filePath: request.filePath,
        line: 0,
        column: 0,
        endLine: 0,
        endColumn: 3,
        selectionLine: 0,
        selectionColumn: 0,
        selectionEndLine: 0,
        selectionEndColumn: 3,
      }),
      lspService.getDocumentColors("file:///workspace/src/main.ts"),
      lspService.getColorPresentations(
        "file:///workspace/src/main.ts",
        { red: 1, green: 0, blue: 0, alpha: 1 },
        {
          start: { line: 0, character: 0 },
          end: { line: 0, character: 3 },
        },
      ),
      lspService.prepareLinkedEdits("file:///workspace/src/main.ts", {
        line: 0,
        character: 1,
      }),
    ]);

    expect(values).toEqual(values.map(() => []));
  });

  it("normalizes nested nullable binding fields", async () => {
    bindings.DetectLSPServers.mockResolvedValue([
      {
        language: "typescript",
        available: true,
        running: true,
        serverPath: "typescript-language-server",
        version: "1.0.0",
        framework: "future-framework",
      },
      {
        language: "angular",
        available: true,
        running: false,
        serverPath: "angular-language-server",
        version: "1.0.0",
        framework: "angular",
      },
    ]);
    bindings.RenameSymbolWorkspace.mockResolvedValue([
      { filePath: "/workspace/src/main.ts", edits: null },
    ]);
    bindings.GetDocumentSymbols.mockResolvedValue([
      {
        name: "App",
        kind: 5,
        range: {
          start: { line: 0, character: 0 },
          end: { line: 1, character: 0 },
        },
        selectionRange: {
          start: { line: 0, character: 6 },
          end: { line: 0, character: 9 },
        },
        children: [
          {
            name: "run",
            kind: 6,
            range: {
              start: { line: 0, character: 0 },
              end: { line: 0, character: 10 },
            },
            selectionRange: {
              start: { line: 0, character: 0 },
              end: { line: 0, character: 3 },
            },
            children: null,
          },
        ],
      },
    ]);
    bindings.GetCodeActions.mockResolvedValue([
      {
        title: "Fix import",
        commandArguments: null,
        edit: [{ filePath: "/workspace/src/main.ts", edits: null }],
        preview: { files: null },
      },
    ]);
    bindings.GetSignatureHelp.mockResolvedValue({
      label: "run(value: string)",
      documentation: "Runs the value",
      parameters: null,
      activeParameter: 0,
      activeSignature: 0,
    });
    bindings.GetSelectionRanges.mockResolvedValue([
      {
        startLine: 0,
        startColumn: 0,
        endLine: 0,
        endColumn: 3,
        parent: {
          startLine: 0,
          startColumn: 0,
          endLine: 1,
          endColumn: 0,
          parent: null,
        },
      },
    ]);
    bindings.CallHierarchyIncomingCalls.mockResolvedValue([
      {
        from: {
          name: "caller",
          kind: 12,
          filePath: request.filePath,
          line: 0,
          column: 0,
          endLine: 0,
          endColumn: 6,
          selectionLine: 0,
          selectionColumn: 0,
          selectionEndLine: 0,
          selectionEndColumn: 6,
        },
        fromRanges: null,
      },
    ]);
    bindings.CallHierarchyOutgoingCalls.mockResolvedValue([
      {
        to: {
          name: "callee",
          kind: 12,
          filePath: request.filePath,
          line: 1,
          column: 0,
          endLine: 1,
          endColumn: 6,
          selectionLine: 1,
          selectionColumn: 0,
          selectionEndLine: 1,
          selectionEndColumn: 6,
        },
        fromRanges: null,
      },
    ]);
    bindings.GetColorPresentations.mockResolvedValue([
      {
        label: "#ff0000",
        textEdit: null,
        additionalTextEdits: null,
      },
    ]);

    const statuses = await lspService.detectServers();
    expect(statuses[0].framework).toBeUndefined();
    expect(statuses[1].framework).toBe("angular");
    await expect(
      lspService.renameSymbolWorkspace(request, "nextName"),
    ).resolves.toEqual([
      { filePath: "/workspace/src/main.ts", edits: [] },
    ]);
    await expect(lspService.getDocumentSymbols(request)).resolves.toMatchObject([
      { name: "App", children: [{ name: "run", children: undefined }] },
    ]);
    await expect(lspService.getCodeActions(request)).resolves.toMatchObject([
      {
        title: "Fix import",
        commandArguments: undefined,
        edit: [{ filePath: "/workspace/src/main.ts", edits: [] }],
        preview: { files: [] },
      },
    ]);
    await expect(lspService.getSignatureHelp(request)).resolves.toMatchObject({
      label: "run(value: string)",
      parameters: [],
    });
    await expect(lspService.getSelectionRanges(request)).resolves.toMatchObject([
      { parent: { parent: undefined } },
    ]);
    await expect(
      lspService.callHierarchyIncomingCalls(request, {
        name: "App",
        kind: 5,
        filePath: request.filePath,
        line: 0,
        column: 0,
        endLine: 0,
        endColumn: 3,
        selectionLine: 0,
        selectionColumn: 0,
        selectionEndLine: 0,
        selectionEndColumn: 3,
      }),
    ).resolves.toMatchObject([{ fromRanges: [] }]);
    await expect(
      lspService.callHierarchyOutgoingCalls(request, {
        name: "App",
        kind: 5,
        filePath: request.filePath,
        line: 0,
        column: 0,
        endLine: 0,
        endColumn: 3,
        selectionLine: 0,
        selectionColumn: 0,
        selectionEndLine: 0,
        selectionEndColumn: 3,
      }),
    ).resolves.toMatchObject([{ fromRanges: [] }]);
    await expect(
      lspService.getColorPresentations(
        "file:///workspace/src/main.ts",
        { red: 1, green: 0, blue: 0, alpha: 1 },
        {
          start: { line: 0, character: 0 },
          end: { line: 0, character: 3 },
        },
      ),
    ).resolves.toMatchObject([
      {
        label: "#ff0000",
        textEdit: undefined,
        additionalTextEdits: undefined,
      },
    ]);
  });

  it("converts semantic token columns and modifier indices at the service boundary", async () => {
    bindings.GetSemanticTokens.mockResolvedValue([
      { line: 2, column: 4, length: 5, type: 7, modifiers: [0, 2, 31] },
    ]);

    await expect(lspService.getSemanticTokens(request)).resolves.toEqual([
      {
        line: 2,
        start: 4,
        length: 5,
        type: 7,
        modifiers: 2_147_483_653,
      },
    ]);
  });

  it("converts flattened inlay hints into protocol positions", async () => {
    bindings.GetInlayHints.mockResolvedValue([
      {
        line: 3,
        column: 8,
        label: ": string",
        kind: 1,
        tooltip: { value: "inferred type" },
        paddingLeft: true,
        paddingRight: false,
        textEdits: [
          {
            startLine: 3,
            startCol: 8,
            endLine: 3,
            endCol: 8,
            newText: " : string",
          },
        ],
      },
    ]);

    await expect(lspService.getInlayHints(request)).resolves.toEqual([
      {
        position: { line: 3, character: 8 },
        label: ": string",
        kind: 1,
        tooltip: "inferred type",
        paddingLeft: true,
        paddingRight: false,
        textEdits: [
          {
            startLine: 3,
            startCol: 8,
            endLine: 3,
            endCol: 8,
            newText: " : string",
          },
        ],
      },
    ]);
  });

  it("preserves opaque completion data and converts InsertReplaceEdit coordinates", () => {
    const data = { resolveId: 42, nested: ["value"] };
    const item: LSPCompletionItem = {
      label: "render",
      kind: 3,
      detail: "function",
      documentation: { kind: "markdown", value: "**render**" },
      data,
      textEdit: {
        newText: "render($1)",
        insert: {
          start: { line: 4, character: 2 },
          end: { line: 4, character: 5 },
        },
        replace: {
          start: { line: 4, character: 2 },
          end: { line: 4, character: 8 },
        },
      },
      additionalEdits: [
        {
          startLine: 0,
          startCol: 0,
          endLine: 0,
          endCol: 0,
          newText: "import { render } from './render';\n",
        },
      ],
    };

    const binding = toBindingLSPCompletionItem(item);
    expect(binding.data).toBe(data);
    expect(binding.textEdit).toMatchObject({
      startLine: 4,
      startCol: 2,
      endLine: 4,
      endCol: 8,
      insert: item.textEdit && "insert" in item.textEdit ? item.textEdit.insert : undefined,
      replace: item.textEdit && "replace" in item.textEdit ? item.textEdit.replace : undefined,
    });

    const normalized = fromBindingLSPCompletionItem(binding);
    expect(normalized.data).toBe(data);
    expect(normalized.documentation).toEqual(item.documentation);
    expect(normalized.additionalEdits).toEqual(item.additionalEdits);
    expect(normalized.textEdit).toEqual({
      startLine: 4,
      startCol: 2,
      endLine: 4,
      endCol: 8,
      range: undefined,
      insert: item.textEdit && "insert" in item.textEdit ? item.textEdit.insert : undefined,
      replace: item.textEdit && "replace" in item.textEdit ? item.textEdit.replace : undefined,
      newText: "render($1)",
    });
  });

  it("resolves code actions through the generated binding and normalizes nulls", async () => {
    const action = {
      title: "Extract",
      data: { resolveId: 7 },
    };
    bindings.ResolveCodeAction.mockResolvedValueOnce({
      ...action,
      command: "gopls.extract_function",
      commandArguments: null,
      edit: [{ filePath: request.filePath, edits: null }],
      preview: { files: null },
    });

    await expect(lspService.resolveCodeAction(request, action)).resolves.toMatchObject({
      command: "gopls.extract_function",
      commandArguments: undefined,
      edit: [{ filePath: request.filePath, edits: [] }],
      preview: { files: [] },
    });
    expect(bindings.ResolveCodeAction).toHaveBeenCalledWith(request, action);
  });
});
