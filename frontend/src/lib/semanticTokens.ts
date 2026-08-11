// Koyori IDE 模块 · Semantic Tokens。
// 喵，这是 Koyori IDE 的 Semantic Tokens 模块（前端实现）~
import * as defaultMonaco from "monaco-editor";
import type * as monacoEditor from "monaco-editor";
import * as LSPServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/lspservice.js";
import * as lspStore from "@/stores/lsp";
import { editorState } from "@/stores/editor";
import type { LSPCompletionRequest, SemanticToken } from "@/types";
type MonacoApi = typeof import("monaco-editor");

interface SemanticTokenInput {
  line: number;
  column?: number;
  start?: number;
  length: number;
  type: number;
  modifiers?: number[] | number;
}

interface SemanticTokensEditResponse {
  start: number;
  deleteCount: number;
  data?: readonly number[] | null;
}

interface SemanticTokensDeltaResponse {
  resultId?: string;
  data?: readonly number[] | null;
  edits?: readonly SemanticTokensEditResponse[] | null;
}

interface SemanticTokenCacheEntry {
  model: monacoEditor.editor.ITextModel;
  filePath: string;
  resultId: string;
  backendResultId?: string;
  tokens: Uint32Array;
  modelVersionId: number;
}

export const SEMANTIC_TOKEN_TYPES = [
  "namespace",
  "type",
  "class",
  "enum",
  "interface",
  "struct",
  "typeParameter",
  "parameter",
  "variable",
  "property",
  "enumMember",
  "event",
  "function",
  "method",
  "macro",
  "keyword",
  "modifier",
  "comment",
  "string",
  "number",
  "regexp",
  "operator",
  "decorator",
] as const;

export const SEMANTIC_TOKEN_MODIFIERS = [
  "declaration",
  "definition",
  "readonly",
  "static",
  "deprecated",
  "abstract",
  "async",
  "modification",
  "documentation",
  "defaultLibrary",
] as const;

const VARIABLE_TOKEN_INDEX = SEMANTIC_TOKEN_TYPES.indexOf("variable");
const registrations = new Set<monacoEditor.IDisposable>();
let semanticRegistrationSequence = 0;

function toNonNegativeInteger(value: number): number {
  if (!Number.isFinite(value) || value <= 0) return 0;
  return Math.floor(value);
}

function normalizeTokenType(type: number): number {
  const index = toNonNegativeInteger(type);
  return index < SEMANTIC_TOKEN_TYPES.length ? index : VARIABLE_TOKEN_INDEX;
}

function encodeModifierMask(modifiers: number[] | number | undefined): number {
  if (typeof modifiers === "number") return toNonNegativeInteger(modifiers) >>> 0;
  let mask = 0;
  for (const modifier of modifiers ?? []) {
    if (Number.isInteger(modifier) && modifier >= 0 && modifier < 32) {
      mask = (mask | (1 << modifier)) >>> 0;
    }
  }
  return mask;
}

/**
 * Monaco 0.52 expects semantic tokens as LSP-style relative five-tuples:
 * [deltaLine, deltaStart, length, tokenType, tokenModifierBitset].
 * The Wails API currently returns decoded absolute tokens, while accepting a
 * numeric array here keeps the adapter compatible with raw LSP payloads.
 */
export function encodeSemanticTokens(
  tokens: readonly SemanticTokenInput[] | readonly number[],
): Uint32Array {
  if (tokens.length === 0) return new Uint32Array();

  if (typeof tokens[0] === "number") {
    const raw = tokens as readonly number[];
    const completeLength = raw.length - (raw.length % 5);
    const encoded = new Uint32Array(completeLength);
    for (let offset = 0; offset < completeLength; offset += 5) {
      encoded[offset] = toNonNegativeInteger(raw[offset]);
      encoded[offset + 1] = toNonNegativeInteger(raw[offset + 1]);
      encoded[offset + 2] = toNonNegativeInteger(raw[offset + 2]);
      encoded[offset + 3] = normalizeTokenType(raw[offset + 3]);
      encoded[offset + 4] = encodeModifierMask(raw[offset + 4]);
    }
    return encoded;
  }

  const absoluteTokens = (tokens as readonly SemanticTokenInput[])
    .map((token) => ({ ...token, column: token.start ?? token.column }))
    .filter(
      (token): token is SemanticTokenInput & { column: number } =>
        Number.isFinite(token.line) &&
        token.line >= 0 &&
        token.column !== undefined &&
        Number.isFinite(token.column) &&
        token.column >= 0 &&
        Number.isFinite(token.length) &&
        token.length > 0,
    )
    .slice()
    .sort((left, right) => left.line - right.line || left.column - right.column);

  const encoded = new Uint32Array(absoluteTokens.length * 5);
  let previousLine = 0;
  let previousColumn = 0;

  absoluteTokens.forEach((token, index) => {
    const line = toNonNegativeInteger(token.line);
    const column = toNonNegativeInteger(token.column);
    const deltaLine = line - previousLine;
    const offset = index * 5;

    encoded[offset] = deltaLine;
    encoded[offset + 1] = deltaLine === 0 ? column - previousColumn : column;
    encoded[offset + 2] = toNonNegativeInteger(token.length);
    encoded[offset + 3] = normalizeTokenType(token.type);
    encoded[offset + 4] = encodeModifierMask(token.modifiers);

    previousLine = line;
    previousColumn = column;
  });

  return encoded;
}

function encodeSemanticTokenEditData(values: readonly number[]): Uint32Array {
  const data = new Uint32Array(values.length);
  values.forEach((value, index) => {
    data[index] = toNonNegativeInteger(value);
  });
  return data;
}

function normalizeSemanticTokenEdits(
  edits: readonly SemanticTokensEditResponse[],
): monacoEditor.languages.SemanticTokensEdit[] | null {
  const normalized: monacoEditor.languages.SemanticTokensEdit[] = [];
  for (const edit of edits) {
    if (
      !Number.isInteger(edit.start) ||
      edit.start < 0 ||
      !Number.isInteger(edit.deleteCount) ||
      edit.deleteCount < 0
    ) {
      return null;
    }
    normalized.push({
      start: edit.start,
      deleteCount: edit.deleteCount,
      ...(edit.data !== undefined && edit.data !== null
        ? { data: encodeSemanticTokenEditData(edit.data) }
        : {}),
    });
  }
  return normalized;
}

function applySemanticTokenEdits(
  previous: Uint32Array,
  edits: readonly monacoEditor.languages.SemanticTokensEdit[],
): Uint32Array | null {
  let previousEditEnd = 0;
  let deltaLength = 0;
  for (const edit of edits) {
    const editEnd = edit.start + edit.deleteCount;
    if (
      edit.start < previousEditEnd ||
      edit.start > previous.length ||
      editEnd > previous.length
    ) {
      return null;
    }
    previousEditEnd = editEnd;
    deltaLength += (edit.data?.length ?? 0) - edit.deleteCount;
  }

  const nextLength = previous.length + deltaLength;
  if (nextLength < 0 || nextLength % 5 !== 0) return null;

  const next = new Uint32Array(nextLength);
  let sourceEnd = previous.length;
  let targetEnd = next.length;
  for (let index = edits.length - 1; index >= 0; index -= 1) {
    const edit = edits[index];
    const editEnd = edit.start + edit.deleteCount;
    const copyLength = sourceEnd - editEnd;
    if (copyLength > 0) {
      targetEnd -= copyLength;
      next.set(previous.subarray(editEnd, sourceEnd), targetEnd);
    }
    if (edit.data) {
      targetEnd -= edit.data.length;
      next.set(edit.data, targetEnd);
    }
    sourceEnd = edit.start;
  }
  if (sourceEnd > 0) next.set(previous.subarray(0, sourceEnd), 0);
  return next;
}

async function resolveSemanticServerLanguage(
  language: string,
  filePath: string,
): Promise<string | null | undefined> {
  if (!Object.prototype.hasOwnProperty.call(lspStore, "ensureLSPRunning")) {
    return undefined;
  }
  try {
    const serverLanguage =
      Object.prototype.hasOwnProperty.call(lspStore, "monacoLanguageToLSP") &&
      typeof lspStore.monacoLanguageToLSP === "function"
        ? lspStore.monacoLanguageToLSP(language, filePath) ?? language
        : language;
    if (typeof lspStore.ensureLSPRunning !== "function") return undefined;
    return (await lspStore.ensureLSPRunning(serverLanguage))
      ? serverLanguage
      : null;
  } catch {
    return undefined;
  }
}

async function requestSemanticTokensDelta(
  language: string,
  filePath: string,
  content: string,
  previousResultId: string,
): Promise<SemanticTokensDeltaResponse | undefined> {
  const serverLanguage = await resolveSemanticServerLanguage(language, filePath);
  if (serverLanguage === undefined) return undefined;
  if (serverLanguage === null) return {};

  const request: LSPCompletionRequest = {
    language: serverLanguage,
    filePath,
    line: 0,
    column: 0,
    content,
  };
  return LSPServiceBindings.GetSemanticTokensDelta(
    request,
    previousResultId,
  );
}

function resolveFilePath(
  model: monacoEditor.editor.ITextModel,
  _preferredPath?: string,
): string | null {
  const uriText = model.uri.toString();
  const uriPath = model.uri.path || uriText;
  const isVirtual =
    uriText.startsWith("inmemory:") || uriText.startsWith("untitled:");
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
  const matches = editorState.openFiles.filter((file) => file.content === content);
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

export function cleanupSemanticTokensProviders(): void {
  for (const disposable of [...registrations]) disposable.dispose();
}

export function registerSemanticTokensProvider(
  lang: string,
  preferredPath?: string,
): monacoEditor.IDisposable;
export function registerSemanticTokensProvider(
  monaco: MonacoApi,
  lang: string,
  preferredPath?: string,
): monacoEditor.IDisposable;
export function registerSemanticTokensProvider(
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

  if (!lang) throw new TypeError("Semantic tokens provider requires a language id");

  let disposed = false;
  const registrationId = ++semanticRegistrationSequence;
  const requestGenerations = new WeakMap<object, number>();
  const cacheByModel = new WeakMap<
    monacoEditor.editor.ITextModel,
    SemanticTokenCacheEntry
  >();
  const cacheByResultId = new Map<string, SemanticTokenCacheEntry>();
  let clientResultSequence = 0;

  const clientResultId = (backendResultId?: string): string => {
    if (backendResultId && !cacheByResultId.has(backendResultId)) {
      return backendResultId;
    }
    clientResultSequence += 1;
    return `koyori-ide-semantic-${registrationId}-${clientResultSequence}`;
  };

  const forgetResult = (resultId: string): void => {
    const entry = cacheByResultId.get(resultId);
    if (!entry) return;
    cacheByResultId.delete(resultId);
    if (cacheByModel.get(entry.model) === entry) cacheByModel.delete(entry.model);
  };
  const rememberResult = (
    model: monacoEditor.editor.ITextModel,
    filePath: string,
    resultId: string,
    tokens: Uint32Array,
    modelVersionId: number,
    backendResultId?: string,
  ): void => {
    const previous = cacheByModel.get(model);
    if (previous) cacheByResultId.delete(previous.resultId);
    const entry = {
      model,
      filePath,
      resultId,
      backendResultId,
      tokens,
      modelVersionId,
    };
    cacheByModel.set(model, entry);
    cacheByResultId.set(resultId, entry);
  };
  const clearResults = (): void => {
    for (const entry of cacheByResultId.values()) cacheByModel.delete(entry.model);
    cacheByResultId.clear();
  };
  const disposable = monaco.languages.registerDocumentSemanticTokensProvider(
    lang,
    {
      getLegend() {
        return {
          tokenTypes: [...SEMANTIC_TOKEN_TYPES],
          tokenModifiers: [...SEMANTIC_TOKEN_MODIFIERS],
        };
      },
      async provideDocumentSemanticTokens(model, lastResultId, token) {
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
          if (model.isDisposed?.()) return null;
          const content = model.getValue();
          const modelVersionId = model.getVersionId?.() ?? 0;
          const isCurrentRequest = (): boolean =>
            !disposed &&
            !token.isCancellationRequested &&
            !model.isDisposed?.() &&
            requestGenerations.get(model) === generation &&
            (model.getVersionId?.() ?? 0) === modelVersionId;
          const cached = cacheByModel.get(model);
          const lastEntry = lastResultId
            ? cacheByResultId.get(lastResultId)
            : undefined;
          const previousEntry =
            lastEntry?.model === model &&
            lastEntry.filePath === filePath &&
            lastEntry.backendResultId
              ? lastEntry
              : !lastResultId &&
                  cached?.backendResultId &&
                  cached.filePath === filePath
                ? cached
                : undefined;
          const previousResultId = previousEntry?.backendResultId ?? "";

          const acceptFullResponse = (
            response: SemanticTokensDeltaResponse,
          ): monacoEditor.languages.SemanticTokens | null => {
            const fullData = response.data;
            if (
              fullData === undefined ||
              fullData === null ||
              fullData.length % 5 !== 0
            ) {
              return null;
            }
            const data = encodeSemanticTokens(fullData);
            const backendResultId = response.resultId || undefined;
            const resultId = clientResultId(backendResultId);
            rememberResult(
              model,
              filePath,
              resultId,
              data,
              modelVersionId,
              backendResultId,
            );
            return { resultId, data };
          };

          const acceptDeltaResponse = (
            response: SemanticTokensDeltaResponse,
          ): monacoEditor.languages.SemanticTokensEdits | null => {
            if (!previousEntry || response.edits === undefined || response.edits === null) {
              return null;
            }
            const edits = normalizeSemanticTokenEdits(response.edits);
            if (!edits) return null;
            const data = applySemanticTokenEdits(previousEntry.tokens, edits);
            if (!data) return null;
            const backendResultId =
              response.resultId || previousEntry.backendResultId;
            if (!backendResultId) return null;
            const resultId = clientResultId(backendResultId);
            rememberResult(
              model,
              filePath,
              resultId,
              data,
              modelVersionId,
              backendResultId,
            );
            return { resultId, edits };
          };

          let deltaResponse: SemanticTokensDeltaResponse | undefined;
          try {
            deltaResponse = await requestSemanticTokensDelta(
              lang,
              filePath,
              content,
              previousResultId,
            );
          } catch {
            deltaResponse = undefined;
          }

          if (deltaResponse !== undefined) {
            if (!isCurrentRequest()) return null;
            const fullResult = acceptFullResponse(deltaResponse);
            if (fullResult) return fullResult;
            const deltaResult = acceptDeltaResponse(deltaResponse);
            if (deltaResult) return deltaResult;

            if (previousResultId) {
              try {
                const fullResponse = await requestSemanticTokensDelta(
                  lang,
                  filePath,
                  content,
                  "",
                );
                if (!isCurrentRequest()) return null;
                if (fullResponse !== undefined) {
                  const retryResult = acceptFullResponse(fullResponse);
                  if (retryResult) return retryResult;
                }
              } catch {
                // Continue with the legacy full-token request below.
              }
            }
          }

          const result = await lspStore.getLSPSemanticTokens(
            lang,
            filePath,
            content,
          );
          if (!isCurrentRequest()) return null;
          const resultId = clientResultId();
          const data = encodeSemanticTokens(
            result satisfies readonly SemanticToken[],
          );
          rememberResult(
            model,
            filePath,
            resultId,
            data,
            modelVersionId,
          );
          return { resultId, data };
        } catch {
          return null;
        }
      },
      releaseDocumentSemanticTokens(resultId) {
        if (resultId) forgetResult(resultId);
      },
    },
  );

  return trackRegistration(disposable, () => {
    disposed = true;
    clearResults();
  });
}
