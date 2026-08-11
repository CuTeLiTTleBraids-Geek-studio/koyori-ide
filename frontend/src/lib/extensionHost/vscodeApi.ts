/**
 * G-VSC-02: The `vscode` API shim handed to VS Code extensions.
 *
 * This module exposes the `VscodeAPI` interface (a subset of the real
 * `vscode` namespace) and a factory that wires each method to the host's
 * bridging logic. Extensions receive the object returned by
 * `createVscodeAPI` as the argument to their `activate()` export.
 *
 * Bridging summary (see extensionHost.ts for the host side):
 *   - languages.register*Provider  → monaco.languages.register*Provider
 *   - workspace.fs.readFile/writeFile → FileService (permission-gated)
 *   - window.createWebviewPanel    → sandboxed iframe (G-SEC-05)
 *   - commands.registerCommand     → host command registry (disposable)
 *   - commands.executeCommand      → host registry + dangerous-cmd gate
 */
// Koyori IDE 模块 · Vscode Api。
// 喵，这是 Koyori IDE 的 Vscode Api 模块（前端实现）~

import type { ExtensionPermission } from "@/lib/extensionHost/permissions";

// ---------------------------------------------------------------------------
// Core types (subset of the vscode API surface)
// ---------------------------------------------------------------------------

/**
 * vscode-compatible Thenable (Promise-like). Defined locally so we do not
 * depend on the real `vscode` module or ambient Thenable globals.
 */
export type Thenable<T> = PromiseLike<T>;

/** A handle that releases a resource when disposed. */
export interface Disposable {
  dispose(): void;
}

/** A filesystem URI. Only the `file` scheme is bridged in v1. */
export interface Uri {
  fsPath: string;
  scheme: string;
  authority?: string;
  path?: string;
  query?: string;
  fragment?: string;
}

/**
 * Language filter that selects which documents a provider applies to.
 * Mirrors `vscode.DocumentSelector` (single-filter form). The bridge
 * extracts `language` and passes it to Monaco.
 */
export interface DocumentSelector {
  language: string;
  scheme?: string;
  pattern?: string;
}

/** A completion item returned by a CompletionItemProvider. */
export interface CompletionItem {
  label: string;
  kind?: number;
  detail?: string;
  documentation?: string;
  insertText?: string;
}

/** Result of a completion request. */
export interface CompletionList {
  items: CompletionItem[];
  isIncomplete?: boolean;
}

/** Provides completion items for a document position. */
export interface CompletionItemProvider {
  provideCompletionItems(
    document: TextDocument,
    position: Position,
    token?: unknown,
  ): CompletionList | Thenable<CompletionList>;
}

/** Provides hover info for a document position. */
export interface HoverProvider {
  provideHover(
    document: TextDocument,
    position: Position,
    token?: unknown,
  ): Hover | Thenable<Hover | null> | null;
}

/** Hover tooltip content. */
export interface Hover {
  contents: string[];
}

/** Provides go-to-definition. */
export interface DefinitionProvider {
  provideDefinition(
    document: TextDocument,
    position: Position,
    token?: unknown,
  ): Definition | Thenable<Definition> | null;
}

/** A location in a document. */
export interface Location {
  uri: Uri;
  range: Range;
}

/** A definition result (one or many locations). */
export type Definition = Location | Location[];

/** Provides code actions (quick fixes) for a range. */
export interface CodeActionProvider {
  provideCodeActions(
    document: TextDocument,
    range: Range,
    token?: unknown,
  ): CodeAction[] | Thenable<CodeAction[]> | null;
}

/** A code action (quick fix). */
export interface CodeAction {
  title: string;
  command?: string;
  arguments?: unknown[];
}

// ---------------------------------------------------------------------------
// F-6 (task-3.md): 补齐 languages API 完整 Provider 类型集合
// ---------------------------------------------------------------------------

/** Provides references for a document position. */
export interface ReferenceProvider {
  provideReferences(
    document: TextDocument,
    position: Position,
    token?: unknown,
  ): Location[] | Thenable<Location[]> | null;
}

/** A code lens (clickable annotation above a line). */
export interface CodeLens {
  range: Range;
  command?: string;
  arguments?: unknown[];
}

/** Provides code lenses for a document. */
export interface CodeLensProvider {
  provideCodeLenses(
    document: TextDocument,
    token?: unknown,
  ): CodeLens[] | Thenable<CodeLens[]> | null;
}

/** A text edit (range → replacement). */
export interface TextEdit {
  range: Range;
  newText: string;
}

/** Provides formatting edits for an entire document. */
export interface DocumentFormattingEditProvider {
  provideDocumentFormattingEdits(
    document: TextDocument,
    token?: unknown,
  ): TextEdit[] | Thenable<TextEdit[]> | null;
}

/** Provides formatting edits for a range within a document. */
export interface DocumentRangeFormattingEditProvider {
  provideDocumentRangeFormattingEdits(
    document: TextDocument,
    range: Range,
    token?: unknown,
  ): TextEdit[] | Thenable<TextEdit[]> | null;
}

/** Provides on-type formatting edits (e.g. auto-indent on `}`). */
export interface OnTypeFormattingEditProvider {
  provideOnTypeFormattingEdits(
    document: TextDocument,
    position: Position,
    ch: string,
    token?: unknown,
  ): TextEdit[] | Thenable<TextEdit[]> | null;
}

/** Signature help (parameter hints). */
export interface SignatureHelp {
  signatures: Array<{
    label: string;
    documentation?: string;
    parameters?: Array<{ label: string; documentation?: string }>;
  }>;
  activeSignature?: number;
  activeParameter?: number;
}

/** Provides signature help for a document position. */
export interface SignatureHelpProvider {
  provideSignatureHelp(
    document: TextDocument,
    position: Position,
    token?: unknown,
  ): SignatureHelp | Thenable<SignatureHelp | null> | null;
}

/** A workspace symbol (returned by workspace/symbol). */
export interface SymbolInformation {
  name: string;
  kind: number;
  location: Location;
  containerName?: string;
}

/** Provides workspace symbol search (Ctrl+T). */
export interface WorkspaceSymbolProvider {
  provideWorkspaceSymbols(
    query: string,
    token?: unknown,
  ): SymbolInformation[] | Thenable<SymbolInformation[]> | null;
}

/** A document link (clickable URL/path in a document). */
export interface DocumentLink {
  range: Range;
  target?: Uri;
}

/** Provides document links. */
export interface DocumentLinkProvider {
  provideDocumentLinks(
    document: TextDocument,
    token?: unknown,
  ): DocumentLink[] | Thenable<DocumentLink[]> | null;
  resolveDocumentLink?(link: DocumentLink, token?: unknown): DocumentLink | Thenable<DocumentLink>;
}

/** A color presentation (how to render a color in code). */
export interface ColorPresentation {
  label: string;
  textEdit?: TextEdit;
}

/** A color range in a document. */
export interface ColorInformation {
  range: Range;
  color: { red: number; green: number; blue: number; alpha: number };
}

/** Provides document colors (for color pickers). */
export interface DocumentColorProvider {
  provideDocumentColors(
    document: TextDocument,
    token?: unknown,
  ): ColorInformation[] | Thenable<ColorInformation[]> | null;
  provideColorPresentations(
    color: { red: number; green: number; blue: number; alpha: number },
    context: { document: TextDocument; range: Range },
    token?: unknown,
  ): ColorPresentation[] | Thenable<ColorPresentation[]> | null;
}

/** A folding range (collapsible region). */
export interface FoldingRange {
  start: number;
  end: number;
  kind?: number;
}

/** Provides folding ranges. */
export interface FoldingRangeProvider {
  provideFoldingRanges(
    document: TextDocument,
    token?: unknown,
  ): FoldingRange[] | Thenable<FoldingRange[]> | null;
}

/** Provides go-to-declaration. */
export interface DeclarationProvider {
  provideDeclaration(
    document: TextDocument,
    position: Position,
    token?: unknown,
  ): Definition | Thenable<Definition> | null;
}

/** Provides go-to-implementation. */
export interface ImplementationProvider {
  provideImplementation(
    document: TextDocument,
    position: Position,
    token?: unknown,
  ): Definition | Thenable<Definition> | null;
}

/** Provides go-to-type-definition. */
export interface TypeDefinitionProvider {
  provideTypeDefinition(
    document: TextDocument,
    position: Position,
    token?: unknown,
  ): Definition | Thenable<Definition> | null;
}

/** A rename edit result (edits per file). */
export interface WorkspaceEdit {
  edits?: Array<{ uri: Uri; edits: TextEdit[] }>;
}

/** Provides rename (F2) for a symbol. */
export interface RenameProvider {
  provideRenameEdits(
    document: TextDocument,
    position: Position,
    newName: string,
    token?: unknown,
  ): WorkspaceEdit | Thenable<WorkspaceEdit | null> | null;
  prepareRename?(
    document: TextDocument,
    position: Position,
    token?: unknown,
  ): Range | Thenable<Range | null> | null;
}

/** A document symbol (outline entry). */
export interface DocumentSymbol {
  name: string;
  detail?: string;
  kind: number;
  range: Range;
  selectionRange: Range;
  children?: DocumentSymbol[];
}

/** Provides document symbols (outline). */
export interface DocumentSymbolProvider {
  provideDocumentSymbols(
    document: TextDocument,
    token?: unknown,
  ): DocumentSymbol[] | SymbolInformation[] | Thenable<DocumentSymbol[] | SymbolInformation[]> | null;
}

/** A semantic token (encoded as 5-tuple: line, char, length, type, modifier). */
export interface SemanticTokens {
  data: number[];
}

/** Provides semantic tokens (full document). */
export interface DocumentSemanticTokensProvider {
  provideDocumentSemanticTokens(
    document: TextDocument,
    token?: unknown,
  ): SemanticTokens | Thenable<SemanticTokens | null> | null;
  provideDocumentSemanticTokensEdits?(
    document: TextDocument,
    previousResultId: string,
    token?: unknown,
  ): SemanticTokens | { edits: Array<{ start: number; deleteCount: number; data?: number[] }> } | Thenable<SemanticTokens | { edits: Array<{ start: number; deleteCount: number; data?: number[] }> } | null> | null;
}

/** Provides document highlights (mark all occurrences of a symbol). */
export interface DocumentHighlightProvider {
  provideDocumentHighlights(
    document: TextDocument,
    position: Position,
    token?: unknown,
  ): Array<{ range: Range; kind?: number }> | Thenable<Array<{ range: Range; kind?: number }> | null> | null;
}

/** An inlay hint (inline annotation). */
export interface InlayHint {
  position: Position;
  label: string;
  kind?: number;
  paddingLeft?: boolean;
  paddingRight?: boolean;
}

/** Provides inlay hints for a range. */
export interface InlayHintsProvider {
  provideInlayHints(
    document: TextDocument,
    range: Range,
    token?: unknown,
  ): InlayHint[] | Thenable<InlayHint[]> | null;
}

// ---------------------------------------------------------------------------
// F-6 (task-3.md): tasks API — Task / TaskProvider / TaskExecution
// Bridges to services/task_service.go TaskDef.
// ---------------------------------------------------------------------------

/** A task definition (custom properties per task type). */
export interface TaskDefinition {
  type: string;
  [key: string]: unknown;
}

/** How a task is executed (shell/process). */
export interface ShellExecution {
  command: string;
  args?: string[];
  cwd?: string;
  shell?: boolean;
}

/** A task that can be executed by the host. */
export interface Task {
  name: string;
  source: string;
  detail?: string;
  definition: TaskDefinition;
  execution: ShellExecution;
}

/** An active task execution handle. */
export interface TaskExecution {
  task: Task;
  terminate(): void;
  /** Host-internal completion signal used to release remote handles. */
  completion?: Promise<void>;
}

/**
 * Provides tasks for a given task type. The host calls `provideTasks()`
 * to list tasks and `resolveTask(task)` to fill in details before
 * execution.
 */
export interface TaskProvider {
  provideTasks(): Task[] | Thenable<Task[]>;
  resolveTask(task: Task): Task | Thenable<Task | undefined> | undefined;
}

// ---------------------------------------------------------------------------
// F-6 (task-3.md): debug API — DebugConfiguration / DebugConfigurationProvider
// Bridges to services/debug_service.go LaunchWithConfig.
// ---------------------------------------------------------------------------

/** A workspace folder (placeholder; koyori-ide is single-root in v1). */
export interface WorkspaceFolder {
  uri: Uri;
  name: string;
  index: number;
}

/** A debug configuration entry (from launch.json). */
export interface DebugConfiguration {
  type: string;
  name: string;
  request: "launch" | "attach";
  program?: string;
  args?: string[];
  cwd?: string;
  env?: Record<string, string>;
  stopOnEntry?: boolean;
  mode?: string;
  [key: string]: unknown;
}

/**
 * Provides debug configurations for a debug type. The host calls
 * `provideDebugConfigurations(folder)` to get the initial set (from
 * the extension's contributed configurations) and
 * `resolveDebugConfiguration(folder, config)` to let the provider
 * fill in/modify a config before launch.
 */
export interface DebugConfigurationProvider {
  provideDebugConfigurations(
    folder: WorkspaceFolder | undefined,
  ): DebugConfiguration[] | Thenable<DebugConfiguration[]>;
  resolveDebugConfiguration?(
    folder: WorkspaceFolder | undefined,
    config: DebugConfiguration,
  ): DebugConfiguration | Thenable<DebugConfiguration | undefined | null> | null;
}

// ---------------------------------------------------------------------------
// F-6 (task-3.md): scm API — SourceControl / SourceControlResourceGroup
// Bridges to services/git_service.go (status / stage / unstage).
// ---------------------------------------------------------------------------

/** An input box attached to a source control (the commit message box). */
export interface SourceControlInputBox {
  value: string;
  placeholder?: string;
}

/** A resource state (a single file change in a resource group). */
export interface SourceControlResourceState {
  resourceUri: Uri;
  decorations?: {
    iconPath?: string;
    tooltip?: string;
    strikeThrough?: boolean;
    faded?: boolean;
  };
}

/** A group of related resource states (e.g. "Changes", "Staged Changes"). */
export interface SourceControlResourceGroup {
  readonly id: string;
  readonly label: string;
  resourceStates: SourceControlResourceState[];
  dispose(): void;
}

/** A source control (e.g. "Git"). */
export interface SourceControl {
  readonly id: string;
  readonly label: string;
  readonly rootUri: Uri | undefined;
  inputBox: SourceControlInputBox;
  createResourceGroup(id: string, label: string): SourceControlResourceGroup;
  dispose(): void;
}

// ---------------------------------------------------------------------------
// F-6 (task-3.md): window API补齐类型 — InputBoxOptions / QuickPickItem /
// OutputChannel / Terminal / TreeDataProvider / WebviewView
// ---------------------------------------------------------------------------

/** Options for window.showInputBox. */
export interface InputBoxOptions {
  prompt?: string;
  value?: string;
  password?: boolean;
  placeHolder?: string;
  ignoreFocusOut?: boolean;
  validateInput?(value: string): string | Thenable<string | undefined | null> | undefined;
}

/** A quick pick item (string or object form). */
export interface QuickPickItem {
  label: string;
  description?: string;
  detail?: string;
  picked?: boolean;
}

/** Options for window.showQuickPick. */
export interface QuickPickOptions {
  placeHolder?: string;
  ignoreFocusOut?: boolean;
  canPickMany?: boolean;
}

/** An output channel (writable log stream). */
export interface OutputChannel {
  readonly name: string;
  append(value: string): void;
  appendLine(value: string): void;
  clear(): void;
  show(preserveFocus?: boolean): void;
  hide(): void;
  dispose(): void;
}

/** Options for window.createTerminal. */
export interface TerminalOptions {
  name?: string;
  cwd?: string;
  shellPath?: string;
  shellArgs?: string[];
}

/** A terminal handle. */
export interface Terminal {
  readonly name: string;
  sendText(text: string, addNewLine?: boolean): void;
  show(preserveFocus?: boolean): void;
  hide(): void;
  dispose(): void;
}

/** A tree item (node) returned by a TreeDataProvider. */
export interface TreeItem {
  label: string;
  id?: string;
  collapsibleState?: number;
  tooltip?: string;
  command?: string;
  arguments?: unknown[];
}

/** Provides tree data for a view (e.g. the explorer sidebar). */
export interface TreeDataProvider<T> {
  getTreeItem(element: T): TreeItem | Thenable<TreeItem>;
  getChildren(element?: T): T[] | Thenable<T[]>;
  getParent?(element: T): T | Thenable<T | undefined> | undefined;
  onDidChangeTreeData?: unknown;
}

/** A webview view (sidebar-embedded webview). */
export interface WebviewView {
  webview: Webview;
  visible: boolean;
  onDidChangeVisibility(listener: () => void): Disposable;
  show(preserveFocus?: boolean): void;
}

/** Provides a webview view for a view id. */
export interface WebviewViewProvider {
  resolveWebviewView(view: WebviewView): void | Thenable<void>;
}

/** A text range (line/column pair). */
export interface Range {
  start: Position;
  end: Position;
}

/** A 0-based line/character position. */
export interface Position {
  line: number;
  character: number;
}

/** A text document exposed to providers. */
export interface TextDocument {
  uri: Uri;
  languageId: string;
  getText(): string;
}

/** A text editor (placeholder; activeTextEditor returns undefined in v1). */
export interface TextEditor {
  document: TextDocument;
}

/** Workspace configuration snapshot (read-only in v1). */
export interface WorkspaceConfiguration {
  get<T>(section: string, defaultValue?: T): T;
  has(section: string): boolean;
}

/** Configuration change event (stubbed). */
export interface ConfigurationChangeEvent {
  affectsConfiguration(section: string): boolean;
}

// ---------------------------------------------------------------------------
// F-6 (task-3.md): workspace API 补齐类型 — GlobPattern / TextSearchQuery /
// FindTextInFilesOptions / TextSearchResult / TextDocumentChangeEvent /
// FileType / Event.
// ---------------------------------------------------------------------------

/**
 * A glob pattern. v1 supports only string patterns (e.g. "**\/*.go"); the
 * relative-pattern form is accepted structurally but treated as a string
 * under the workspace root.
 */
export type GlobPattern = string | { base: string; pattern: string };

/** A text search query. */
export interface TextSearchQuery {
  pattern: string;
  isRegExp?: boolean;
  isCaseSensitive?: boolean;
  isWordMatch?: boolean;
}

/** Options for workspace.findTextInFiles. */
export interface FindTextInFilesOptions {
  include?: GlobPattern;
  exclude?: GlobPattern;
  maxResults?: number;
  ignoreCase?: boolean;
}

/** A single match in a text search result. */
export interface TextSearchMatch {
  uri: Uri;
  line: number;
  column: number;
  preview: string;
}

/** A text search result (one per file with matches). */
export interface TextSearchResult {
  uri: Uri;
  matches: TextSearchMatch[];
}

/** File type flags (mirrors vscode.FileType). */
export enum FileType {
  Unknown = 0,
  File = 1,
  Directory = 2,
  SymbolicLink = 64,
}

/**
 * A VS Code Event. Extensions register a listener and receive a Disposable
 * to remove it. The host's event sources are simple emitter maps.
 */
export interface Event<T> {
  (listener: (e: T) => void): Disposable;
}

/** A text document change event. */
export interface TextDocumentChangeEvent {
  document: TextDocument;
  contentChanges: Array<{ range: Range; text: string }>;
}

/** A webview backed by a sandboxed iframe. */
export interface Webview {
  /** HTML to render inside the sandboxed iframe. */
  html: string;
  /** The underlying iframe element (host-internal; tests inspect it). */
  readonly _iframe: HTMLIFrameElement;
}

/** A webview panel returned by createWebviewPanel. */
export interface WebviewPanel {
  viewType: string;
  title: string;
  webview: Webview;
  visible: boolean;
  active: boolean;
  dispose(): void;
  onDidDispose(listener: () => void): Disposable;
}

// ---------------------------------------------------------------------------
// API interface
// ---------------------------------------------------------------------------

/**
 * The `vscode` namespace subset exposed to extensions. Each method is
 * bridged by the ExtensionHost. Methods that touch privileged resources
 * check the extension's declared permissions before dispatching.
 */
export interface VscodeAPI {
  languages: {
    registerCompletionItemProvider(
      selector: DocumentSelector,
      provider: CompletionItemProvider,
    ): Disposable;
    registerHoverProvider(
      selector: DocumentSelector,
      provider: HoverProvider,
    ): Disposable;
    registerDefinitionProvider(
      selector: DocumentSelector,
      provider: DefinitionProvider,
    ): Disposable;
    registerCodeActionProvider(
      selector: DocumentSelector,
      provider: CodeActionProvider,
    ): Disposable;
    // F-6 (task-3.md): 补齐 languages API 完整 21 个 Provider
    registerReferenceProvider(
      selector: DocumentSelector,
      provider: ReferenceProvider,
    ): Disposable;
    registerCodeLensProvider(
      selector: DocumentSelector,
      provider: CodeLensProvider,
    ): Disposable;
    registerDocumentFormattingEditProvider(
      selector: DocumentSelector,
      provider: DocumentFormattingEditProvider,
    ): Disposable;
    registerDocumentRangeFormattingEditProvider(
      selector: DocumentSelector,
      provider: DocumentRangeFormattingEditProvider,
    ): Disposable;
    registerOnTypeFormattingEditProvider(
      selector: DocumentSelector,
      provider: OnTypeFormattingEditProvider,
      firstTriggerCharacter: string,
      moreTriggerCharacter?: string[],
    ): Disposable;
    registerSignatureHelpProvider(
      selector: DocumentSelector,
      provider: SignatureHelpProvider,
    ): Disposable;
    registerWorkspaceSymbolProvider(
      provider: WorkspaceSymbolProvider,
    ): Disposable;
    registerDocumentLinkProvider(
      selector: DocumentSelector,
      provider: DocumentLinkProvider,
    ): Disposable;
    registerColorProvider(
      selector: DocumentSelector,
      provider: DocumentColorProvider,
    ): Disposable;
    registerFoldingRangeProvider(
      selector: DocumentSelector,
      provider: FoldingRangeProvider,
    ): Disposable;
    registerDeclarationProvider(
      selector: DocumentSelector,
      provider: DeclarationProvider,
    ): Disposable;
    registerImplementationProvider(
      selector: DocumentSelector,
      provider: ImplementationProvider,
    ): Disposable;
    registerTypeDefinitionProvider(
      selector: DocumentSelector,
      provider: TypeDefinitionProvider,
    ): Disposable;
    registerRenameProvider(
      selector: DocumentSelector,
      provider: RenameProvider,
    ): Disposable;
    registerDocumentSymbolProvider(
      selector: DocumentSelector,
      provider: DocumentSymbolProvider,
    ): Disposable;
    registerDocumentSemanticTokensProvider(
      selector: DocumentSelector,
      provider: DocumentSemanticTokensProvider,
    ): Disposable;
    registerDocumentHighlightProvider(
      selector: DocumentSelector,
      provider: DocumentHighlightProvider,
    ): Disposable;
    registerInlayHintsProvider(
      selector: DocumentSelector,
      provider: InlayHintsProvider,
    ): Disposable;
  };
  commands: {
    registerCommand(
      command: string,
      callback: (...args: unknown[]) => unknown,
    ): Disposable;
    executeCommand(command: string, ...args: unknown[]): Thenable<unknown>;
  };
  workspace: {
    fs: {
      readFile(uri: Uri): Thenable<Uint8Array>;
      writeFile(uri: Uri, content: Uint8Array): Thenable<void>;
      exists(uri: Uri): Thenable<boolean>;
      createDirectory(uri: Uri): Thenable<void>;
      // F-6 (task-3.md): workspace.fs 补齐
      rename(
        oldUri: Uri,
        newUri: Uri,
        options?: { overwrite?: boolean },
      ): Thenable<void>;
      delete(
        uri: Uri,
        options?: { recursive?: boolean; useTrash?: boolean },
      ): Thenable<void>;
      readDirectory(uri: Uri): Thenable<[string, FileType][]>;
    };
    getConfiguration(section?: string): WorkspaceConfiguration;
    onDidChangeConfiguration(
      listener: (e: ConfigurationChangeEvent) => void,
    ): Disposable;
    // F-6 (task-3.md): workspace API 补齐
    findFiles(
      include: GlobPattern,
      exclude?: GlobPattern,
      maxResults?: number,
    ): Thenable<Uri[]>;
    findTextInFiles(
      query: TextSearchQuery,
      options: FindTextInFilesOptions,
    ): Thenable<TextSearchResult[]>;
    openTextDocument(uri: Uri): Thenable<TextDocument>;
    saveAll(includeUntitled?: boolean): Thenable<boolean>;
    onDidSaveTextDocument: Event<TextDocument>;
    onDidChangeTextDocument: Event<TextDocumentChangeEvent>;
    onDidOpenTextDocument: Event<TextDocument>;
  };
  window: {
    createWebviewPanel(
      viewType: string,
      title: string,
      showOptions: unknown,
      options?: unknown,
    ): WebviewPanel;
    showInformationMessage(
      message: string,
      ...items: string[]
    ): Thenable<string | undefined>;
    showWarningMessage(
      message: string,
      ...items: string[]
    ): Thenable<string | undefined>;
    showErrorMessage(
      message: string,
      ...items: string[]
    ): Thenable<string | undefined>;
    // F-6 (task-3.md): window API 补齐
    showInputBox(options?: InputBoxOptions): Thenable<string | undefined>;
    showQuickPick(
      items: string[] | QuickPickItem[],
      options?: QuickPickOptions,
    ): Thenable<string | QuickPickItem | undefined>;
    createOutputChannel(name: string): OutputChannel;
    createTerminal(options?: TerminalOptions): Terminal;
    registerTreeDataProvider<T>(
      viewId: string,
      treeDataProvider: TreeDataProvider<T>,
    ): Disposable;
    registerWebviewViewProvider(
      viewId: string,
      provider: WebviewViewProvider,
    ): Disposable;
    activeTextEditor: TextEditor | undefined;
  };
  // F-6 (task-3.md): tasks API namespace
  tasks: {
    registerTaskProvider(type: string, provider: TaskProvider): Disposable;
    executeTask(task: Task): Thenable<TaskExecution>;
    fetchTasks(): Thenable<Task[]>;
  };
  // F-6 (task-3.md): debug API namespace
  debug: {
    registerDebugConfigurationProvider(
      type: string,
      provider: DebugConfigurationProvider,
    ): Disposable;
    startDebugging(
      folder: WorkspaceFolder | undefined,
      config: DebugConfiguration,
    ): Thenable<boolean>;
  };
  // F-6 (task-3.md): scm API namespace (minimal version)
  scm: {
    createSourceControl(
      id: string,
      label: string,
      rootUri?: Uri,
    ): SourceControl;
  };
  // F-6 (task-3.md): env API namespace
  env: {
    /** Clipboard read/write (bridged to navigator.clipboard with fallback). */
    clipboard: {
      readText(): Thenable<string>;
      writeText(value: string): Thenable<void>;
    };
    /** Open a URI in the OS-default external application (browser). */
    openExternal(uri: Uri): Thenable<boolean>;
    /** Stable per-machine ID (host-generated, persisted across sessions). */
    readonly machineId: string;
    /** Stable per-session ID (generated when the host starts). */
    readonly sessionId: string;
  };
  // F-6 (task-3.md): secrets API namespace
  secrets: {
    /** Retrieve a stored secret. Returns undefined if not found. */
    get(key: string): Thenable<string | undefined>;
    /** Store a secret (encrypted at rest by the backend keychain). */
    store(key: string, value: string): Thenable<void>;
    /** Delete a stored secret. No-op if the key does not exist. */
    delete(key: string): Thenable<void>;
  };
}

/**
 * The full set of language provider kinds the host can bridge. F-6
 * (task-3.md) extends the original four (completion/hover/definition/
 * codeAction) with 17 more to cover the VS Code languages API surface.
 */
export type LanguageProviderKind =
  | "completion"
  | "hover"
  | "definition"
  | "codeAction"
  // F-6: 17 additional kinds
  | "reference"
  | "codeLens"
  | "documentFormatting"
  | "documentRangeFormatting"
  | "onTypeFormatting"
  | "signatureHelp"
  | "workspaceSymbol"
  | "documentLink"
  | "color"
  | "foldingRange"
  | "declaration"
  | "implementation"
  | "typeDefinition"
  | "rename"
  | "documentSymbol"
  | "documentSemanticTokens"
  | "documentHighlight"
  | "inlayHints";

// ---------------------------------------------------------------------------
// Host interface (implemented by ExtensionHost; passed to the factory)
// ---------------------------------------------------------------------------

/**
 * The surface the vscode API shim uses to talk back to the ExtensionHost.
 * Keeping this as a structural interface lets the host and tests inject
 * different implementations without a circular import.
 */
export interface VscodeHostBridge {
  /** The extension id this API instance is bound to. */
  readonly extensionId: string;
  /** The extension's declared permissions. */
  readonly permissions: readonly ExtensionPermission[];

  /** Track a disposable so it is disposed when the extension deactivates. */
  trackDisposable(d: Disposable): void;

  /** Register a command handler in the host registry. */
  registerCommand(command: string, cb: (...args: unknown[]) => unknown): Disposable;

  /** Execute a command via the host (applies the dangerous-cmd gate). */
  executeCommand(command: string, ...args: unknown[]): Promise<unknown>;

  /** Whether this extension owns the registered command id. */
  isCommandAllowed(command: string): boolean;

  /** Bridge a language provider to Monaco and return the disposable. */
  bridgeLanguageProvider(
    kind: LanguageProviderKind,
    selector: DocumentSelector,
    provider: unknown,
    extra?: unknown,
  ): Disposable;

  /** Bridge a workspace.fs read to the backend FileService. */
  bridgeReadFile(uri: Uri): Promise<Uint8Array>;
  /** Bridge a workspace.fs write to the backend FileService. */
  bridgeWriteFile(uri: Uri, content: Uint8Array): Promise<void>;
  /** Bridge a workspace.fs exists check. */
  bridgeExists(uri: Uri): Promise<boolean>;
  /** Bridge a workspace.fs createDirectory. */
  bridgeCreateDirectory(uri: Uri): Promise<void>;

  // F-6 (task-3.md): workspace.fs 补齐 bridge methods.
  /** Bridge a workspace.fs rename (permission-gated: requires fs.write). */
  bridgeRename(
    oldUri: Uri,
    newUri: Uri,
    options?: { overwrite?: boolean },
  ): Promise<void>;
  /** Bridge a workspace.fs delete (permission-gated: requires fs.write). */
  bridgeDelete(
    uri: Uri,
    options?: { recursive?: boolean; useTrash?: boolean },
  ): Promise<void>;
  /** Bridge a workspace.fs readDirectory (permission-gated: requires fs.read). */
  bridgeReadDirectory(uri: Uri): Promise<[string, FileType][]>;

  // F-6 (task-3.md): workspace API 补齐 bridge methods.
  /** Bridge workspace.findFiles (permission-gated: requires fs.read). */
  bridgeFindFiles(
    include: GlobPattern,
    exclude: GlobPattern | undefined,
    maxResults: number | undefined,
  ): Promise<Uri[]>;
  /** Bridge workspace.findTextInFiles (permission-gated: requires fs.read). */
  bridgeFindTextInFiles(
    query: TextSearchQuery,
    options: FindTextInFilesOptions,
  ): Promise<TextSearchResult[]>;
  /** Bridge workspace.openTextDocument (permission-gated: requires fs.read). */
  bridgeOpenTextDocument(uri: Uri): Promise<TextDocument>;
  /** Bridge workspace.saveAll (best-effort: no live editor wiring in v1). */
  bridgeSaveAll(includeUntitled?: boolean): Promise<boolean>;
  /** workspace.onDidSaveTextDocument event source. */
  bridgeOnDidSaveTextDocument: Event<TextDocument>;
  /** workspace.onDidChangeTextDocument event source. */
  bridgeOnDidChangeTextDocument: Event<TextDocumentChangeEvent>;
  /** workspace.onDidOpenTextDocument event source. */
  bridgeOnDidOpenTextDocument: Event<TextDocument>;

  /** Create a sandboxed webview panel and track it. */
  createWebviewPanel(
    viewType: string,
    title: string,
    showOptions: unknown,
    options?: unknown,
  ): WebviewPanel;

  /** Show a host notification (no-op when ui.notifications absent). */
  notify(level: "info" | "warn" | "error", message: string): void;

  // F-6 (task-3.md): window API 补齐 bridge methods.
  /** Show an input box (returns user input or undefined if cancelled). */
  bridgeShowInputBox(options?: InputBoxOptions): Promise<string | undefined>;
  /** Show a quick pick (returns selected item or undefined if cancelled). */
  bridgeShowQuickPick(
    items: string[] | QuickPickItem[],
    options?: QuickPickOptions,
  ): Promise<string | QuickPickItem | undefined>;
  /** Create an output channel. */
  bridgeCreateOutputChannel(name: string): OutputChannel;
  /** Create a terminal (permission-gated: requires shell.execute). */
  bridgeCreateTerminal(options?: TerminalOptions): Terminal;
  /** Register a tree data provider for a view id. */
  bridgeRegisterTreeDataProvider<T>(
    viewId: string,
    treeDataProvider: TreeDataProvider<T>,
  ): Disposable;
  /** Register a webview view provider for a view id. */
  bridgeRegisterWebviewViewProvider(
    viewId: string,
    provider: WebviewViewProvider,
  ): Disposable;

  // F-6 (task-3.md): tasks API bridge methods.
  /** Register a task provider for a task type. Returns a disposable. */
  bridgeRegisterTaskProvider(type: string, provider: TaskProvider): Disposable;
  /** Execute a task (permission-gated: requires tasks.execute). */
  bridgeExecuteTask(task: Task): Promise<TaskExecution>;
  /** Fetch all known tasks (from providers + backend TaskDef list). */
  bridgeFetchTasks(): Promise<Task[]>;

  // F-6 (task-3.md): debug API bridge methods.
  /** Register a debug configuration provider for a debug type. */
  bridgeRegisterDebugConfigurationProvider(
    type: string,
    provider: DebugConfigurationProvider,
  ): Disposable;
  /** Start a debug session (permission-gated: requires debug.execute). */
  bridgeStartDebugging(
    folder: WorkspaceFolder | undefined,
    config: DebugConfiguration,
  ): Promise<boolean>;

  // F-6 (task-3.md): scm API bridge method.
  /** Create a source control with the given id/label/rootUri. */
  bridgeCreateSourceControl(
    id: string,
    label: string,
    rootUri: Uri | undefined,
  ): SourceControl;

  // F-6 (task-3.md): env API bridge methods.
  /** Read text from the clipboard. */
  bridgeClipboardReadText(): Promise<string>;
  /** Write text to the clipboard. */
  bridgeClipboardWriteText(value: string): Promise<void>;
  /** Open a URI in the OS-default external application. */
  bridgeOpenExternal(uri: Uri): Promise<boolean>;
  /** Stable per-machine ID. */
  readonly machineId: string;
  /** Stable per-session ID. */
  readonly sessionId: string;

  // F-6 (task-3.md): secrets API bridge methods.
  /** Retrieve a stored secret (permission-gated: requires secrets.read). */
  bridgeSecretGet(key: string): Promise<string | undefined>;
  /** Store a secret (permission-gated: requires secrets.write). */
  bridgeSecretStore(key: string, value: string): Promise<void>;
  /** Delete a stored secret (permission-gated: requires secrets.write). */
  bridgeSecretDelete(key: string): Promise<void>;

  // BUG-FIX-2d: 桥接活跃编辑器状态，解决 activeTextEditor 始终为 undefined。
  /** Get the current active text editor (if any). */
  bridgeGetActiveTextEditor?(): TextEditor | undefined;

  // BUG-FIX-2d: 桥接扩展配置，解决 getConfiguration 始终返回空快照。
  /** Get configuration for the given section. */
  bridgeGetConfiguration?(section?: string): Record<string, unknown>;
}

const SAFE_BUILTIN_COMMANDS = new Set<string>([
  "vscode.open",
  "vscode.diff",
  "vscode.executeDefinitionProvider",
  "vscode.executeReferenceProvider",
  "vscode.executeDocumentSymbolProvider",
  "vscode.executeWorkspaceSymbolProvider",
  "editor.action.formatDocument",
  "editor.action.organizeImports",
  "workbench.action.navigateBack",
  "workbench.action.navigateForward",
]);

const RESERVED_COMMAND_PREFIXES = [
  "_",
  "workbench.",
  "vscode.",
  "editor.",
  "terminal.",
  "tasks.",
  "debug.",
  "scm.",
] as const;

/**
 * Extension-initiated execution is allowlist based: exact safe host commands
 * plus command ids owned by the calling extension. Reserved host namespaces
 * are never extension-owned, even if a manifest mimics their prefix.
 */
export function isAllowedExtensionCommand(
  host: Pick<VscodeHostBridge, "isCommandAllowed">,
  command: string,
): boolean {
  if (!/^[A-Za-z0-9][A-Za-z0-9._-]{0,255}$/.test(command)) return false;
  const safeBuiltin = SAFE_BUILTIN_COMMANDS.has(command);
  if (!safeBuiltin && RESERVED_COMMAND_PREFIXES.some((prefix) => command.startsWith(prefix))) {
    return false;
  }
  // The bridge owns the authoritative per-extension command registry. Even
  // exact safe built-ins must be host-owned or registered by this extension;
  // an unrelated extension cannot squat on an allowlisted id.
  return host.isCommandAllowed(command);
}

/**
 * Create the `vscode` API object handed to an extension's `activate()`.
 * Each method delegates to the host bridge, which enforces permission
 * checks and disposable tracking. The factory is pure: it does not touch
 * module-level state, so each extension gets an isolated API object.
 */
export function createVscodeAPI(host: VscodeHostBridge): VscodeAPI {
  const api: VscodeAPI = {
    languages: {
      registerCompletionItemProvider(selector, provider) {
        return host.bridgeLanguageProvider("completion", selector, provider);
      },
      registerHoverProvider(selector, provider) {
        return host.bridgeLanguageProvider("hover", selector, provider);
      },
      registerDefinitionProvider(selector, provider) {
        return host.bridgeLanguageProvider("definition", selector, provider);
      },
      registerCodeActionProvider(selector, provider) {
        return host.bridgeLanguageProvider("codeAction", selector, provider);
      },
      // F-6 (task-3.md): 17 additional language provider registrations.
      // Each delegates to bridgeLanguageProvider with its kind; the host
      // routes the provider to Monaco (or a no-op disposable when Monaco
      // lacks the registration method).
      registerReferenceProvider(selector, provider) {
        return host.bridgeLanguageProvider("reference", selector, provider);
      },
      registerCodeLensProvider(selector, provider) {
        return host.bridgeLanguageProvider("codeLens", selector, provider);
      },
      registerDocumentFormattingEditProvider(selector, provider) {
        return host.bridgeLanguageProvider("documentFormatting", selector, provider);
      },
      registerDocumentRangeFormattingEditProvider(selector, provider) {
        return host.bridgeLanguageProvider("documentRangeFormatting", selector, provider);
      },
      registerOnTypeFormattingEditProvider(
        selector,
        provider,
        firstTriggerCharacter,
        moreTriggerCharacter,
      ) {
        return host.bridgeLanguageProvider("onTypeFormatting", selector, provider, {
          firstTriggerCharacter,
          moreTriggerCharacter,
        });
      },
      registerSignatureHelpProvider(selector, provider) {
        return host.bridgeLanguageProvider("signatureHelp", selector, provider);
      },
      registerWorkspaceSymbolProvider(provider) {
        // Workspace symbol providers are not document-scoped; pass a dummy
        // selector so the host treats it uniformly.
        return host.bridgeLanguageProvider("workspaceSymbol", { language: "*" }, provider);
      },
      registerDocumentLinkProvider(selector, provider) {
        return host.bridgeLanguageProvider("documentLink", selector, provider);
      },
      registerColorProvider(selector, provider) {
        return host.bridgeLanguageProvider("color", selector, provider);
      },
      registerFoldingRangeProvider(selector, provider) {
        return host.bridgeLanguageProvider("foldingRange", selector, provider);
      },
      registerDeclarationProvider(selector, provider) {
        return host.bridgeLanguageProvider("declaration", selector, provider);
      },
      registerImplementationProvider(selector, provider) {
        return host.bridgeLanguageProvider("implementation", selector, provider);
      },
      registerTypeDefinitionProvider(selector, provider) {
        return host.bridgeLanguageProvider("typeDefinition", selector, provider);
      },
      registerRenameProvider(selector, provider) {
        return host.bridgeLanguageProvider("rename", selector, provider);
      },
      registerDocumentSymbolProvider(selector, provider) {
        return host.bridgeLanguageProvider("documentSymbol", selector, provider);
      },
      registerDocumentSemanticTokensProvider(selector, provider) {
        return host.bridgeLanguageProvider("documentSemanticTokens", selector, provider);
      },
      registerDocumentHighlightProvider(selector, provider) {
        return host.bridgeLanguageProvider("documentHighlight", selector, provider);
      },
      registerInlayHintsProvider(selector, provider) {
        return host.bridgeLanguageProvider("inlayHints", selector, provider);
      },
    },
    commands: {
      registerCommand(command, callback) {
        return host.registerCommand(command, callback);
      },
      executeCommand(command, ...args) {
        if (!isAllowedExtensionCommand(host, command)) {
          return Promise.reject(
            new Error(`Command "${command}" is not allowed for extension "${host.extensionId}"`),
          );
        }
        return host.executeCommand(command, ...args);
      },
    },
    workspace: {
      fs: {
        readFile(uri) {
          return host.bridgeReadFile(uri);
        },
        writeFile(uri, content) {
          return host.bridgeWriteFile(uri, content);
        },
        exists(uri) {
          return host.bridgeExists(uri);
        },
        createDirectory(uri) {
          return host.bridgeCreateDirectory(uri);
        },
        // F-6 (task-3.md): workspace.fs 补齐
        rename(oldUri, newUri, options) {
          return host.bridgeRename(oldUri, newUri, options);
        },
        delete(uri, options) {
          return host.bridgeDelete(uri, options);
        },
        readDirectory(uri) {
          return host.bridgeReadDirectory(uri);
        },
      },
      getConfiguration(section): WorkspaceConfiguration {
        // BUG-FIX-2d: 通过 host 桥接获取配置值，不再返回空快照。
        // 当 host 未提供回调时，回退为空配置。
        const configData: Record<string, unknown> =
          host.bridgeGetConfiguration?.(section) ?? {};
        return {
          get<T>(key: string, defaultValue?: T): T {
            const parts = key.split(".");
            // eslint-disable-next-line @typescript-eslint/no-explicit-any
            let current: any = configData;
            for (const part of parts) {
              if (current == null || typeof current !== "object") {
                return defaultValue as T;
              }
              current = current[part];
            }
            return (current !== undefined ? current : defaultValue) as T;
          },
          has(key: string): boolean {
            const parts = key.split(".");
            // eslint-disable-next-line @typescript-eslint/no-explicit-any
            let current: any = configData;
            for (const part of parts) {
              if (current == null || typeof current !== "object") return false;
              if (!(part in current)) return false;
              current = current[part];
            }
            return true;
          },
        };
      },
      onDidChangeConfiguration(_listener) {
        // v1 stub: configuration changes are not forwarded yet.
        return { dispose: () => undefined };
      },
      // F-6 (task-3.md): workspace API 补齐
      findFiles(include, exclude, maxResults) {
        return host.bridgeFindFiles(include, exclude, maxResults);
      },
      findTextInFiles(query, options) {
        return host.bridgeFindTextInFiles(query, options);
      },
      openTextDocument(uri) {
        return host.bridgeOpenTextDocument(uri);
      },
      saveAll(includeUntitled) {
        return host.bridgeSaveAll(includeUntitled);
      },
      onDidSaveTextDocument(listener) {
        return host.bridgeOnDidSaveTextDocument(listener);
      },
      onDidChangeTextDocument(listener) {
        return host.bridgeOnDidChangeTextDocument(listener);
      },
      onDidOpenTextDocument(listener) {
        return host.bridgeOnDidOpenTextDocument(listener);
      },
    },
    window: {
      createWebviewPanel(viewType, title, showOptions, options) {
        return host.createWebviewPanel(viewType, title, showOptions, options);
      },
      showInformationMessage(message, ..._items) {
        try {
          host.notify("info", message);
          return Promise.resolve(undefined);
        } catch (error) {
          return Promise.reject(error);
        }
      },
      showWarningMessage(message, ..._items) {
        try {
          host.notify("warn", message);
          return Promise.resolve(undefined);
        } catch (error) {
          return Promise.reject(error);
        }
      },
      showErrorMessage(message, ..._items) {
        try {
          host.notify("error", message);
          return Promise.resolve(undefined);
        } catch (error) {
          return Promise.reject(error);
        }
      },
      // F-6 (task-3.md): window API 补齐实现
      showInputBox(options) {
        return host.bridgeShowInputBox(options);
      },
      showQuickPick(items, options) {
        return host.bridgeShowQuickPick(items, options);
      },
      createOutputChannel(name) {
        return host.bridgeCreateOutputChannel(name);
      },
      createTerminal(options) {
        return host.bridgeCreateTerminal(options);
      },
      registerTreeDataProvider(viewId, treeDataProvider) {
        return host.bridgeRegisterTreeDataProvider(viewId, treeDataProvider);
      },
      registerWebviewViewProvider(viewId, provider) {
        return host.bridgeRegisterWebviewViewProvider(viewId, provider);
      },
      // BUG-FIX-2d: activeTextEditor 改为 getter 以每次访问时动态获取当前编辑器状态。
      // 不再使用静态赋值（扩展 activate 时读取一次后就不再更新）。
      get activeTextEditor(): TextEditor | undefined {
        return (host.bridgeGetActiveTextEditor
          ? host.bridgeGetActiveTextEditor()
          : undefined) as TextEditor | undefined;
      },
    },
    // F-6 (task-3.md): tasks API namespace implementation.
    tasks: {
      registerTaskProvider(type, provider) {
        return host.bridgeRegisterTaskProvider(type, provider);
      },
      executeTask(task) {
        return host.bridgeExecuteTask(task);
      },
      fetchTasks() {
        return host.bridgeFetchTasks();
      },
    },
    // F-6 (task-3.md): debug API namespace implementation.
    debug: {
      registerDebugConfigurationProvider(type, provider) {
        return host.bridgeRegisterDebugConfigurationProvider(type, provider);
      },
      startDebugging(folder, config) {
        return host.bridgeStartDebugging(folder, config);
      },
    },
    // F-6 (task-3.md): scm API namespace implementation.
    scm: {
      createSourceControl(id, label, rootUri) {
        return host.bridgeCreateSourceControl(id, label, rootUri);
      },
    },
    // F-6 (task-3.md): env API namespace implementation.
    env: {
      clipboard: {
        readText() {
          return host.bridgeClipboardReadText();
        },
        writeText(value) {
          return host.bridgeClipboardWriteText(value);
        },
      },
      openExternal(uri) {
        return host.bridgeOpenExternal(uri);
      },
      machineId: host.machineId,
      sessionId: host.sessionId,
    },
    // F-6 (task-3.md): secrets API namespace implementation.
    secrets: {
      get(key) {
        return host.bridgeSecretGet(key);
      },
      store(key, value) {
        return host.bridgeSecretStore(key, value);
      },
      delete(key) {
        return host.bridgeSecretDelete(key);
      },
    },
  };
  return api;
}
