// Koyori IDE 模块 · Inlay Hints。
// 喵，这是 Koyori IDE 的 Inlay Hints 模块（前端实现）~
import * as defaultMonaco from "monaco-editor";
import type * as monacoEditor from "monaco-editor";
import * as LSPServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/lspservice.js";
import type { BackendInlayHint } from "@/types";
import { getLSPInlayHints, monacoLanguageToLSP } from "@/stores/lsp";
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

interface LSPTextEditLike {
  range?: LSPRangeLike;
  startLine?: number;
  startCol?: number;
  endLine?: number;
  endCol?: number;
  newText: string;
}

interface LSPInlayHintLabelPartLike {
  value: string;
  tooltip?: LSPTooltipLike;
  location?: { uri: string; range: LSPRangeLike };
}

interface LSPInlayHintLike {
  line?: number;
  column?: number;
  position?: LSPPositionLike;
  label: string | LSPInlayHintLabelPartLike[];
  kind?: number;
  paddingLeft?: boolean;
  paddingRight?: boolean;
  tooltip?: LSPTooltipLike;
  textEdits?: LSPTextEditLike[];
  data?: unknown;
  rawLabel?: unknown;
}

const registrations = new Set<monacoEditor.IDisposable>();

interface LSPMarkupContentLike {
  kind?: string;
  value: string;
}

type LSPTooltipLike = string | LSPMarkupContentLike;

function toMonacoRange(range: LSPRangeLike): monacoEditor.IRange {
  return {
    startLineNumber: range.start.line + 1,
    startColumn: range.start.character + 1,
    endLineNumber: range.end.line + 1,
    endColumn: range.end.character + 1,
  };
}

function toMonacoTextEdit(
  edit: LSPTextEditLike,
): monacoEditor.languages.TextEdit | null {
  if (edit.range) {
    return { range: toMonacoRange(edit.range), text: edit.newText };
  }
  if (
    edit.startLine === undefined ||
    edit.startCol === undefined ||
    edit.endLine === undefined ||
    edit.endCol === undefined
  ) {
    return null;
  }
  return {
    range: {
      startLineNumber: edit.startLine + 1,
      startColumn: edit.startCol + 1,
      endLineNumber: edit.endLine + 1,
      endColumn: edit.endCol + 1,
    },
    text: edit.newText,
  };
}

function toMarkdown(
  tooltip: LSPTooltipLike | undefined,
): string | monacoEditor.IMarkdownString | undefined {
  if (!tooltip) return undefined;
  const value = typeof tooltip === "string" ? tooltip : tooltip.value;
  const kind = typeof tooltip === "string" ? "" : tooltip.kind?.toLowerCase();
  if (kind === "plaintext") return value;
  if (
    kind === "markdown" ||
    value.includes("```")
  ) {
    // Monaco's markdown renderer tokenizes fenced code blocks when providers
    // return an IMarkdownString. Keep HTML and command execution disabled.
    return { value, isTrusted: false, supportHtml: false };
  }
  return typeof tooltip === "string" ? tooltip : { value };
}

function fromMarkdown(
  tooltip: string | monacoEditor.IMarkdownString | undefined,
): LSPTooltipLike | undefined {
  if (!tooltip) return undefined;
  return typeof tooltip === "string" ? tooltip : { value: tooltip.value };
}

function toMonacoUri(monaco: MonacoApi, value: string): monacoEditor.Uri {
  const isWindowsPath = /^[A-Za-z]:[\\/]/.test(value);
  const hasUriScheme = /^[A-Za-z][A-Za-z\d+.-]*:/.test(value);
  return hasUriScheme && !isWindowsPath
    ? monaco.Uri.parse(value)
    : monaco.Uri.file(value);
}

export function mapLSPInlayHintToMonaco(
  hint: LSPInlayHintLike,
  monaco: MonacoApi = defaultMonaco,
): monacoEditor.languages.InlayHint | null {
  const line = hint.position?.line ?? hint.line;
  const column = hint.position?.character ?? hint.column;
  if (
    line === undefined ||
    column === undefined ||
    !Number.isFinite(line) ||
    !Number.isFinite(column) ||
    line < 0 ||
    column < 0
  ) {
    return null;
  }

  const rawLabel = parseRawInlayLabel(hint.rawLabel);
  const sourceLabel = rawLabel ?? hint.label;
  const label =
    typeof sourceLabel === "string"
      ? sourceLabel
      : sourceLabel.map((part) => ({
          label: part.value,
          tooltip: toMarkdown(part.tooltip),
          location: part.location
            ? {
                uri: toMonacoUri(monaco, part.location.uri),
                range: toMonacoRange(part.location.range),
              }
            : undefined,
        }));
  const kind =
    hint.kind === 1 || hint.kind === 2
      ? (hint.kind as monacoEditor.languages.InlayHintKind)
      : undefined;
  const textEdits = hint.textEdits
    ?.map(toMonacoTextEdit)
    .filter((edit): edit is monacoEditor.languages.TextEdit => edit !== null);

  return {
    label,
    position: { lineNumber: Math.floor(line) + 1, column: Math.floor(column) + 1 },
    kind,
    paddingLeft: hint.paddingLeft ?? hint.kind === 2,
    paddingRight: hint.paddingRight ?? hint.kind === 1,
    tooltip: toMarkdown(hint.tooltip),
    ...(textEdits?.length ? { textEdits } : {}),
  };
}

function parseRawInlayLabel(
  value: unknown,
): string | LSPInlayHintLabelPartLike[] | null {
  if (typeof value === "string") {
    try {
      return parseRawInlayLabel(JSON.parse(value));
    } catch {
      return null;
    }
  }
  if (value instanceof Uint8Array) {
    try {
      return parseRawInlayLabel(new TextDecoder().decode(value));
    } catch {
      return null;
    }
  }
  if (Array.isArray(value)) {
    const parts = value.filter(
      (part): part is LSPInlayHintLabelPartLike =>
        Boolean(
          part &&
            typeof part === "object" &&
            "value" in part &&
            typeof part.value === "string",
        ),
    );
    return parts.length === value.length ? parts : null;
  }
  return null;
}

function toBackendInlayHint(
  hint: LSPInlayHintLike,
): BackendInlayHint {
  const line = hint.position?.line ?? hint.line ?? 0;
  const column = hint.position?.character ?? hint.column ?? 0;
  const label =
    typeof hint.label === "string"
      ? hint.label
      : hint.label.map((part) => part.value).join("");
  const backend: BackendInlayHint = {
    line,
    column,
    label,
    kind: hint.kind ?? 0,
  };
  if (typeof hint.label !== "string") backend.rawLabel = hint.label;
  if (hint.rawLabel !== undefined) backend.rawLabel = hint.rawLabel;
  if (hint.tooltip !== undefined) backend.tooltip = hint.tooltip;
  if (hint.paddingLeft !== undefined) backend.paddingLeft = hint.paddingLeft;
  if (hint.paddingRight !== undefined) backend.paddingRight = hint.paddingRight;
  if (hint.data !== undefined) backend.data = hint.data;
  if (hint.textEdits?.length) {
    backend.textEdits = hint.textEdits.flatMap((edit) => {
      if (edit.range) {
        return [
          {
            startLine: edit.range.start.line,
            startCol: edit.range.start.character,
            endLine: edit.range.end.line,
            endCol: edit.range.end.character,
            newText: edit.newText,
          },
        ];
      }
      if (
        edit.startLine === undefined ||
        edit.startCol === undefined ||
        edit.endLine === undefined ||
        edit.endCol === undefined
      ) {
        return [];
      }
      return [
        {
          startLine: edit.startLine,
          startCol: edit.startCol,
          endLine: edit.endLine,
          endCol: edit.endCol,
          newText: edit.newText,
        },
      ];
    });
  }
  return backend;
}

function mapMonacoInlayHintToLSP(
  hint: monacoEditor.languages.InlayHint,
): LSPInlayHintLike {
  return {
    position: {
      line: hint.position.lineNumber - 1,
      character: hint.position.column - 1,
    },
    label:
      typeof hint.label === "string"
        ? hint.label
        : hint.label.map((part) => ({
            value: part.label,
            tooltip: fromMarkdown(part.tooltip),
            location: part.location
              ? {
                  uri: part.location.uri.toString(),
                  range: {
                    start: {
                      line: part.location.range.startLineNumber - 1,
                      character: part.location.range.startColumn - 1,
                    },
                    end: {
                      line: part.location.range.endLineNumber - 1,
                      character: part.location.range.endColumn - 1,
                    },
                  },
                }
              : undefined,
          })),
    kind: hint.kind,
    paddingLeft: hint.paddingLeft,
    paddingRight: hint.paddingRight,
    tooltip: fromMarkdown(hint.tooltip),
    textEdits: hint.textEdits?.map((edit) => ({
      range: {
        start: {
          line: edit.range.startLineNumber - 1,
          character: edit.range.startColumn - 1,
        },
        end: {
          line: edit.range.endLineNumber - 1,
          character: edit.range.endColumn - 1,
        },
      },
      newText: edit.text,
    })),
  };
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

function isInsideRange(
  hint: monacoEditor.languages.InlayHint,
  range: monacoEditor.Range,
): boolean {
  const { lineNumber, column } = hint.position;
  if (lineNumber < range.startLineNumber || lineNumber > range.endLineNumber) {
    return false;
  }
  if (lineNumber === range.startLineNumber && column < range.startColumn) {
    return false;
  }
  if (lineNumber === range.endLineNumber && column > range.endColumn) {
    return false;
  }
  return true;
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

export function cleanupInlayHintsProviders(): void {
  for (const disposable of [...registrations]) disposable.dispose();
}

export function registerInlayHintsProvider(
  lang: string,
  preferredPath?: string,
): monacoEditor.IDisposable;
export function registerInlayHintsProvider(
  monaco: MonacoApi,
  lang: string,
  preferredPath?: string,
): monacoEditor.IDisposable;
export function registerInlayHintsProvider(
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

  if (!lang) throw new TypeError("Inlay hints provider requires a language id");

  let disposed = false;
  const requestGenerations = new WeakMap<object, Map<string, number>>();
  const resolveGenerations = new WeakMap<object, number>();
  const sourceHints = new WeakMap<object, LSPInlayHintLike>();
  const sourceLanguages = new WeakMap<object, string>();
  const disposable = monaco.languages.registerInlayHintsProvider(lang, {
    displayName: "Koyori IDE LSP",
    async provideInlayHints(model, range, token) {
      const rangeKey = [
        range.startLineNumber,
        range.startColumn,
        range.endLineNumber,
        range.endColumn,
      ].join(":");
      let generations = requestGenerations.get(model);
      if (!generations) {
        generations = new Map<string, number>();
        requestGenerations.set(model, generations);
      }
      const generation = (generations.get(rangeKey) ?? 0) + 1;
      generations.set(rangeKey, generation);
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
        const content = model.getValue();
        const modelVersionId = model.getVersionId?.() ?? 0;
        const result = await getLSPInlayHints(
          serverLanguage,
          filePath,
          content,
          Math.max(0, range.startLineNumber - 1),
          Math.max(0, range.endLineNumber),
        );
        if (
          disposed ||
          token.isCancellationRequested ||
          model.isDisposed?.() ||
          generations.get(rangeKey) !== generation ||
          (model.getVersionId?.() ?? 0) !== modelVersionId
        ) {
          return null;
        }
        const hints: monacoEditor.languages.InlayHint[] = [];
        for (const sourceHint of result) {
          const hint = mapLSPInlayHintToMonaco(sourceHint, monaco);
          if (hint && isInsideRange(hint, range)) {
            sourceHints.set(hint, sourceHint);
            sourceLanguages.set(hint, serverLanguage);
            hints.push(hint);
          }
        }
        return { hints, dispose: () => undefined };
      } catch {
        return null;
      }
    },
    async resolveInlayHint(hint, token) {
      const generation = (resolveGenerations.get(hint) ?? 0) + 1;
      resolveGenerations.set(hint, generation);
      if (disposed || token.isCancellationRequested) return null;

      const source = sourceHints.get(hint) ?? mapMonacoInlayHintToLSP(hint);
      const serverLanguage = sourceLanguages.get(hint) ?? lang;
      try {
        const resolved = (await LSPServiceBindings.ResolveInlayHint(
          serverLanguage,
          toBackendInlayHint(source),
        )) as LSPInlayHintLike;
        if (
          disposed ||
          token.isCancellationRequested ||
          resolveGenerations.get(hint) !== generation
        ) {
          return null;
        }
        const mapped = mapLSPInlayHintToMonaco(resolved, monaco);
        if (!mapped) return hint;
        sourceHints.set(mapped, resolved);
        sourceLanguages.set(mapped, serverLanguage);
        return mapped;
      } catch {
        if (
          disposed ||
          token.isCancellationRequested ||
          resolveGenerations.get(hint) !== generation
        ) {
          return null;
        }
        return hint;
      }
    },
  });

  return trackRegistration(disposable, () => {
    disposed = true;
  });
}
