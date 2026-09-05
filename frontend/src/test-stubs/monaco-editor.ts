/**
 * prompt-5 Task D / BUG-M3 — minimal monaco-editor stub for vitest.
 * Real monaco-editor fails to resolve under jsdom; tests that only need
 * theme helpers or Monaco KeyMod constants use this stub via vitest alias.
 */

export const editor = {
  defineTheme: () => undefined,
  setTheme: () => undefined,
  create: () => ({
    dispose: () => undefined,
    getModel: () => null,
    onDidChangeModelContent: () => ({ dispose: () => undefined }),
  }),
  createDiffEditor: () => ({
    dispose: () => undefined,
  }),
};

const providerDisposable = () => ({ dispose: () => undefined });

export const languages = {
  register: () => undefined,
  setMonarchTokensProvider: () => undefined,
  registerCompletionItemProvider: providerDisposable,
  registerHoverProvider: providerDisposable,
  registerDefinitionProvider: providerDisposable,
  registerCodeActionProvider: providerDisposable,
  registerReferenceProvider: providerDisposable,
  registerCodeLensProvider: providerDisposable,
  registerDocumentFormattingEditProvider: providerDisposable,
  registerDocumentRangeFormattingEditProvider: providerDisposable,
  registerOnTypeFormattingEditProvider: providerDisposable,
  registerSignatureHelpProvider: providerDisposable,
  registerDocumentSymbolProvider: providerDisposable,
  registerLinkProvider: providerDisposable,
  registerColorProvider: providerDisposable,
  registerFoldingRangeProvider: providerDisposable,
  registerDeclarationProvider: providerDisposable,
  registerImplementationProvider: providerDisposable,
  registerTypeDefinitionProvider: providerDisposable,
  registerRenameProvider: providerDisposable,
  registerDocumentSemanticTokensProvider: providerDisposable,
  registerDocumentRangeSemanticTokensProvider: providerDisposable,
  registerDocumentHighlightProvider: providerDisposable,
  registerInlayHintsProvider: providerDisposable,
};

export const KeyMod = {
  CtrlCmd: 2048,
  Shift: 1024,
  Alt: 512,
  WinCtrl: 256,
};

export const KeyCode = {
  KeyA: 31,
  KeyS: 49,
  Enter: 3,
  Escape: 9,
  Backslash: 88,
};

const monaco = { editor, languages, KeyMod, KeyCode };
export default monaco;
