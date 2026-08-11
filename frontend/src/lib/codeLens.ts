// Koyori IDE 模块 · Code Lens。
// 喵，这是 Koyori IDE 的 Code Lens 模块（前端实现）~
import * as defaultMonaco from "monaco-editor";
import type * as monacoEditor from "monaco-editor";
import * as LSPServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/lspservice.js";
import type { BackendCodeLens } from "@/types";
import { getLSPCodeLenses, monacoLanguageToLSP } from "@/stores/lsp";
import { editorState } from "@/stores/editor";

type MonacoApi = typeof import("monaco-editor");

interface LSPPositionLike {
  line: number;
  character: number;
}

interface LSPRangeLike {
  start: LSPPositionLike;
  end: LSPPositionLike;
}

interface LSPCommandLike {
  title: string;
  command: string;
  arguments?: unknown[];
}

interface LSPCodeLensLike {
  range?: LSPRangeLike;
  line?: number;
  column?: number;
  endLine?: number;
  endColumn?: number;
  label?: string;
  command?: string | LSPCommandLike;
  arguments?: unknown[];
  data?: unknown;
}

export interface CodeLensExecutionPayload {
  language: string;
  command: string;
  clientCommand?: string;
  arguments: unknown[];
  filePath: string;
  line: number;
  column: number;
}

interface CodeLensMappingContext {
  language: string;
  filePath: string;
  index: number;
}

interface CommandBridgeState {
  monaco: MonacoApi;
  disposable: monacoEditor.IDisposable;
  references: number;
}

export const CODE_LENS_EXECUTE_COMMAND_ID = "koyori-ide.lsp.executeCodeLens";

const CLIENT_COMMAND_ALIASES: Readonly<Record<string, string>> = {
  reference: "editor.action.referenceSearch.trigger",
  references: "editor.action.referenceSearch.trigger",
  implementation: "editor.action.goToImplementation",
  implementations: "editor.action.goToImplementation",
};

const registrations = new Set<monacoEditor.IDisposable>();
const commandBridges = new Map<MonacoApi, CommandBridgeState>();

function toMonacoRange(range: LSPRangeLike): monacoEditor.IRange {
  return {
    startLineNumber: range.start.line + 1,
    startColumn: range.start.character + 1,
    endLineNumber: range.end.line + 1,
    endColumn: range.end.character + 1,
  };
}

function getLensRange(lens: LSPCodeLensLike): monacoEditor.IRange | null {
  if (lens.range) return toMonacoRange(lens.range);
  if (
    lens.line === undefined ||
    lens.column === undefined ||
    !Number.isFinite(lens.line) ||
    !Number.isFinite(lens.column) ||
    lens.line < 0 ||
    lens.column < 0
  ) {
    return null;
  }
  const lineNumber = Math.floor(lens.line) + 1;
  const column = Math.floor(lens.column) + 1;
  const endLineNumber = Math.floor(lens.endLine ?? lens.line) + 1;
  const endColumn = Math.floor(lens.endColumn ?? lens.column) + 1;
  return {
    startLineNumber: lineNumber,
    startColumn: column,
    endLineNumber,
    endColumn,
  };
}

function normalizeCommand(lens: LSPCodeLensLike): LSPCommandLike | null {
  if (!lens.command) return null;
  if (typeof lens.command === "string") {
    if (!lens.command) return null;
    return {
      title: lens.label || lens.command,
      command: lens.command,
      arguments: [...(lens.arguments ?? [])],
    };
  }
  if (!lens.command.command) return null;
  return {
    title: lens.command.title || lens.label || lens.command.command,
    command: lens.command.command,
    arguments: [...(lens.command.arguments ?? [])],
  };
}

function toBackendCodeLens(
  lens: LSPCodeLensLike,
): BackendCodeLens {
  const range = getLensRange(lens) ?? {
    startLineNumber: 1,
    startColumn: 1,
    endLineNumber: 1,
    endColumn: 1,
  };
  const command = normalizeCommand(lens);
  const backend: BackendCodeLens = {
    line: range.startLineNumber - 1,
    column: range.startColumn - 1,
    endLine: range.endLineNumber - 1,
    endColumn: range.endColumn - 1,
    label: command?.title ?? lens.label ?? "",
    command: command?.command ?? "",
  };
  const argumentsValue = command?.arguments ?? lens.arguments;
  if (argumentsValue?.length) backend.arguments = [...argumentsValue];
  if (lens.data !== undefined) backend.data = lens.data;
  return backend;
}

function clientCommandId(command: string): string | null {
  const alias = CLIENT_COMMAND_ALIASES[command.toLowerCase()];
  if (alias) return alias;
  return command.startsWith("editor.action.") ? command : null;
}

export function mapLSPCodeLensToMonaco(
  lens: LSPCodeLensLike,
  context: CodeLensMappingContext,
): monacoEditor.languages.CodeLens | null {
  const range = getLensRange(lens);
  if (!range) return null;

  const command = normalizeCommand(lens);
  let monacoCommand: monacoEditor.languages.Command | undefined;
  if (command) {
    const nativeCommand = clientCommandId(command.command);
    const payload: CodeLensExecutionPayload = {
      language: context.language,
      command: command.command,
      clientCommand: nativeCommand ?? undefined,
      arguments: [...(command.arguments ?? [])],
      filePath: context.filePath,
      line: range.startLineNumber - 1,
      column: range.startColumn - 1,
    };
    monacoCommand = {
      id: CODE_LENS_EXECUTE_COMMAND_ID,
      title: command.title,
      arguments: [payload],
    };
  }

  return {
    range,
    id: `koyori-ide-codelens-${context.language}-${range.startLineNumber}-${range.startColumn}-${context.index}`,
    ...(monacoCommand ? { command: monacoCommand } : {}),
  };
}

export function executeCodeLensCommand(
  payload: CodeLensExecutionPayload,
): ReturnType<typeof LSPServiceBindings.ExecuteRefactorCommand> {
  return LSPServiceBindings.ExecuteRefactorCommand(
    payload.language,
    payload.command,
    [...payload.arguments],
  );
}

function acquireCommandBridge(monaco: MonacoApi): monacoEditor.IDisposable {
  let state = commandBridges.get(monaco);
  if (!state) {
    const disposable = monaco.editor.registerCommand(
      CODE_LENS_EXECUTE_COMMAND_ID,
      (_accessor, payload: CodeLensExecutionPayload) => {
        if (!payload?.language || !payload.command) return;
        if (payload.clientCommand) {
          const normalizedPath = payload.filePath.replace(/\\/g, "/");
          const openFile = editorState.openFiles.find(
            (file) => file.path.replace(/\\/g, "/") === normalizedPath,
          );
          const editors = monaco.editor.getEditors();
          const target =
            editors.find((editor) => {
              const model = editor.getModel();
              if (!model) return false;
              const modelPath = (model.uri.fsPath || model.uri.path || "").replace(
                /\\/g,
                "/",
              );
              return (
                modelPath === normalizedPath ||
                (openFile !== undefined && model.getValue() === openFile.content)
              );
            }) ?? editors.find((editor) => editor.hasTextFocus());
          if (!target) return;
          target.setPosition({
            lineNumber: payload.line + 1,
            column: payload.column + 1,
          });
          target.focus();
          target.trigger(
            CODE_LENS_EXECUTE_COMMAND_ID,
            payload.clientCommand,
            undefined,
          );
          return;
        }
        void executeCodeLensCommand(payload).catch((error: unknown) => {
          console.debug("[LSP] code lens command failed", error);
        });
      },
    );
    state = { monaco, disposable, references: 0 };
    commandBridges.set(monaco, state);
  }
  state.references += 1;

  let released = false;
  return {
    dispose() {
      if (released || !state) return;
      released = true;
      state.references -= 1;
      if (state.references === 0) {
        state.disposable.dispose();
        commandBridges.delete(state.monaco);
      }
    },
  };
}

function resolveFilePath(
  model: monacoEditor.editor.ITextModel,
  _preferredPath?: string,
): string | null {
  const uriText = model.uri.toString();
  const uriPath = model.uri.path || uriText;
  const isVirtual =
    uriText.startsWith("inmemory:") ||
    uriText.startsWith("untitled:") ||
    uriPath.startsWith("inmemory:") ||
    uriPath.startsWith("untitled:");
  const fsPath = model.uri.fsPath;
  if (!isVirtual && fsPath && !fsPath.startsWith("inmemory:")) return fsPath;
  if (!isVirtual) {
    if (/^\/[A-Za-z]:\//.test(uriPath)) {
      return uriPath.slice(1).replace(/\//g, "\\");
    }
    return uriPath;
  }
  if (model.isDisposed?.()) return null;
  const content = model.getValue();
  const matches = editorState.openFiles.filter(
    (file) => file.path && file.content === content,
  );
  if (matches.length === 1) return matches[0].path;
  return null;
}

function trackRegistration(
  disposable: monacoEditor.IDisposable,
  invalidate: () => void,
): monacoEditor.IDisposable {
  let disposed = false;
  const tracked: monacoEditor.IDisposable = {
    dispose() {
      if (disposed) return;
      disposed = true;
      invalidate();
      disposable.dispose();
      registrations.delete(tracked);
    },
  };
  registrations.add(tracked);
  return tracked;
}

export function cleanupCodeLensProviders(): void {
  for (const disposable of [...registrations]) disposable.dispose();
}

export function registerCodeLensProvider(
  lang: string,
  preferredPath?: string,
): monacoEditor.IDisposable;
export function registerCodeLensProvider(
  monaco: MonacoApi,
  lang: string,
  preferredPath?: string,
): monacoEditor.IDisposable;
export function registerCodeLensProvider(
  monacoOrLang: MonacoApi | string,
  langOrPreferredPath?: string,
  maybePreferredPath?: string,
): monacoEditor.IDisposable {
  const monaco =
    typeof monacoOrLang === "string" ? defaultMonaco : monacoOrLang;
  const lang =
    typeof monacoOrLang === "string" ? monacoOrLang : langOrPreferredPath;
  const preferredPath =
    typeof monacoOrLang === "string"
      ? langOrPreferredPath
      : maybePreferredPath;

  if (!lang) throw new TypeError("Code lens provider requires a language id");

  let disposed = false;
  const requestGenerations = new WeakMap<object, number>();
  const resolveGenerations = new WeakMap<object, number>();
  const sourceLenses = new WeakMap<object, LSPCodeLensLike>();
  const sourceLanguages = new WeakMap<object, string>();
  const commandBridge = acquireCommandBridge(monaco);
  let providerDisposable: monacoEditor.IDisposable;

  try {
    providerDisposable = monaco.languages.registerCodeLensProvider(lang, {
      async provideCodeLenses(model, token) {
        const generation = (requestGenerations.get(model) ?? 0) + 1;
        requestGenerations.set(model, generation);
        if (
          disposed ||
          token.isCancellationRequested ||
          model.isDisposed?.()
        ) {
          return null;
        }

        try {
          const filePath = resolveFilePath(model, preferredPath);
          if (!filePath) return null;
          const serverLanguage = monacoLanguageToLSP(lang, filePath) ?? lang;
          const result = await getLSPCodeLenses(
            serverLanguage,
            filePath,
            model.getValue(),
          );
          if (
            disposed ||
            token.isCancellationRequested ||
            model.isDisposed?.() ||
            requestGenerations.get(model) !== generation
          ) {
            return null;
          }
          const lenses: monacoEditor.languages.CodeLens[] = [];
          (result as unknown as LSPCodeLensLike[]).forEach((sourceLens, index) => {
            const lens = mapLSPCodeLensToMonaco(sourceLens, {
              language: serverLanguage,
              filePath,
              index,
            });
            if (lens) {
              sourceLenses.set(lens, sourceLens);
              sourceLanguages.set(lens, serverLanguage);
              lenses.push(lens);
            }
          });
          return { lenses, dispose: () => undefined };
        } catch {
          return null;
        }
      },
      async resolveCodeLens(model, lens, token) {
        const generation = (resolveGenerations.get(lens) ?? 0) + 1;
        resolveGenerations.set(lens, generation);
        if (
          disposed ||
          token.isCancellationRequested ||
          model.isDisposed?.()
        ) {
          return null;
        }

        const filePath = resolveFilePath(model, preferredPath);
        if (!filePath) return null;

        const source = sourceLenses.get(lens) ?? {
          range: {
            start: {
              line: lens.range.startLineNumber - 1,
              character: lens.range.startColumn - 1,
            },
            end: {
              line: lens.range.endLineNumber - 1,
              character: lens.range.endColumn - 1,
            },
          },
        };
        const serverLanguage = sourceLanguages.get(lens) ?? lang;
        try {
          const resolved = (await LSPServiceBindings.ResolveCodeLens(
            serverLanguage,
            toBackendCodeLens(source),
          )) as LSPCodeLensLike;
          if (
            disposed ||
            token.isCancellationRequested ||
            model.isDisposed?.() ||
            resolveGenerations.get(lens) !== generation
          ) {
            return null;
          }
          const mapped = mapLSPCodeLensToMonaco(resolved, {
            language: serverLanguage,
            filePath,
            index: 0,
          });
          if (!mapped) return lens;
          sourceLenses.set(mapped, resolved);
          sourceLanguages.set(mapped, serverLanguage);
          return mapped;
        } catch {
          if (
            disposed ||
            token.isCancellationRequested ||
            model.isDisposed?.() ||
            resolveGenerations.get(lens) !== generation
          ) {
            return null;
          }
          return lens;
        }
      },
    });
  } catch (error) {
    commandBridge.dispose();
    throw error;
  }

  return trackRegistration(
    {
      dispose() {
        providerDisposable.dispose();
        commandBridge.dispose();
      },
    },
    () => {
      disposed = true;
    },
  );
}
