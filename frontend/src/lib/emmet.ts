// Koyori IDE 模块 · Emmet。
// 喵，这是 Koyori IDE 的 Emmet 模块（前端实现）~
import { emmetCSS, emmetHTML, emmetJSX } from "emmet-monaco-es";
import type * as monacoEditor from "monaco-editor";

export const DEFAULT_EMMET_INCLUDE_LANGUAGES = {
  html: "html",
  vue: "html",
  css: "css",
  scss: "scss",
  less: "less",
  javascriptreact: "javascriptreact",
  typescriptreact: "typescriptreact",
} as const;

export interface EmmetProviderOptions {
  enabled: boolean;
  includeLanguages?: Record<string, string>;
}

type EmmetKind = "html" | "css" | "jsx";

interface ProviderEntry {
  owners: number;
  dispose: () => void;
}

interface AutoCloseLanguage {
  language: string;
}

interface StandardLineTokens {
  getCount(): number;
  getStartOffset(index: number): number;
  getStandardTokenType(index: number): number;
}

interface TokenizedModel {
  tokenization?: {
    getLineTokens(lineNumber: number): StandardLineTokens;
  };
}

interface EditorWithTypingEvent {
  onDidType?: (listener: (text: string) => void) => monacoEditor.IDisposable;
}

interface TagAutoCloseController {
  add(entries: AutoCloseLanguage[]): void;
  remove(entries: AutoCloseLanguage[]): void;
  isEmpty(): boolean;
  dispose(): void;
}

const MAX_AUTO_CLOSE_SCAN = 64 * 1024;
const AUTO_CLOSE_EDIT_SOURCE = "emmet.auto-close-tag";
const VOID_ELEMENTS = new Set([
  "area",
  "base",
  "basefont",
  "bgsound",
  "br",
  "col",
  "embed",
  "frame",
  "hr",
  "img",
  "input",
  "keygen",
  "link",
  "meta",
  "param",
  "source",
  "track",
  "wbr",
]);
const RAW_TEXT_ELEMENTS = new Set(["script", "style", "textarea", "title"]);

const providersByMonaco = new WeakMap<
  typeof import("monaco-editor"),
  Map<string, ProviderEntry>
>();
const tagAutoCloseByMonaco = new WeakMap<
  typeof import("monaco-editor"),
  TagAutoCloseController
>();

function emmetKindForLanguage(language: string): EmmetKind | null {
  switch (language.toLowerCase()) {
    case "html":
    case "vue":
      return "html";
    case "css":
    case "scss":
    case "less":
      return "css";
    case "javascript":
    case "typescript":
    case "javascriptreact":
    case "typescriptreact":
    case "jsx":
      return "jsx";
    default:
      return null;
  }
}

function registerProvider(
  monaco: typeof import("monaco-editor"),
  language: string,
  kind: EmmetKind,
): () => void {
  // BUG4b: Pass { tokenizer: 'standard' } so emmet-monaco-es uses Monaco's
  // public tokenization API (tokenization.getLineTokens + StandardTokenType)
  // instead of the private `_tokenization._tokenizationStateStore` path.
  // The Monarch path relies on private fields that frequently break across
  // Monaco versions (the upstream code already has 6+ version branches for
  // 0.31–0.53). On Monaco 0.52.2 the private fields may be missing or
  // renamed, causing isValidLocationForEmmetAbbreviation to throw silently
  // and Emmet completions to never appear — which is exactly the symptom
  // in BUG4 (typing "div" only yields "div", never expands to <div></div>).
  // The standard tokenizer is stable across versions and is the
  // recommended path when a non-Monarch grammar might be active.
  const options = { tokenizer: "standard" as const };
  if (kind === "html") return emmetHTML(monaco, [language], options);
  if (kind === "css") return emmetCSS(monaco, [language], options);
  return emmetJSX(monaco, [language], options);
}

function isEscaped(text: string, index: number): boolean {
  let backslashes = 0;
  for (let current = index - 1; current >= 0 && text[current] === "\\"; current -= 1) {
    backslashes += 1;
  }
  return backslashes % 2 === 1;
}

function findOpeningTagStart(text: string): number {
  let quote: "\"" | "'" | "`" | null = null;
  let braceDepth = 0;
  for (let index = text.length - 2; index >= 0; index -= 1) {
    const char = text[index];
    if (quote) {
      if (char === quote && !isEscaped(text, index)) quote = null;
      continue;
    }
    if (char === "\"" || char === "'" || char === "`") {
      quote = char;
      continue;
    }
    if (char === "}") {
      braceDepth += 1;
      continue;
    }
    if (char === "{" && braceDepth > 0) {
      braceDepth -= 1;
      continue;
    }
    if (braceDepth > 0) continue;
    if (char === ">") return -1;
    if (char === "<") return index;
  }
  return -1;
}

function standardTokenTypeAt(
  model: monacoEditor.editor.ITextModel,
  position: monacoEditor.Position,
): number | null {
  const tokenized = model as unknown as TokenizedModel;
  if (typeof tokenized.tokenization?.getLineTokens !== "function") return null;
  try {
    const lineTokens = tokenized.tokenization.getLineTokens(position.lineNumber);
    const offset = Math.max(0, position.column - 2);
    for (let index = lineTokens.getCount() - 1; index >= 0; index -= 1) {
      if (offset >= lineTokens.getStartOffset(index)) {
        return lineTokens.getStandardTokenType(index);
      }
    }
  } catch {
    return null;
  }
  return null;
}

function isMarkupTextContext(text: string): boolean {
  let inTag = false;
  let inComment = false;
  let quote: "\"" | "'" | null = null;
  for (let index = 0; index < text.length; index += 1) {
    if (inComment) {
      if (text.startsWith("-->", index)) {
        inComment = false;
        index += 2;
      }
      continue;
    }
    if (quote) {
      if (text[index] === quote && !isEscaped(text, index)) quote = null;
      continue;
    }
    if (text.startsWith("<!--", index)) {
      inComment = true;
      index += 3;
      continue;
    }
    const char = text[index];
    if (inTag && (char === "\"" || char === "'")) {
      quote = char;
    } else if (char === "<") {
      inTag = true;
    } else if (char === ">") {
      inTag = false;
    }
  }
  return !inTag && !inComment && quote === null;
}

function isInsideRawTextElement(text: string): boolean {
  const open = new Set<string>();
  const pattern = /<\/?(script|style|textarea|title)\b[^>]*>/gi;
  let match: RegExpExecArray | null;
  while ((match = pattern.exec(text)) !== null) {
    const tag = match[1].toLowerCase();
    if (match[0].startsWith("</")) open.delete(tag);
    else if (!match[0].endsWith("/>")) open.add(tag);
  }
  return open.size > 0;
}

function escapeRegExp(text: string): string {
  return text.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

interface ParsedMarkupTag {
  name: string;
  closing: boolean;
  selfClosing: boolean;
  end: number;
}

function parseMarkupTag(text: string, start: number): ParsedMarkupTag | null {
  let cursor = start + 1;
  const closing = text[cursor] === "/";
  if (closing) cursor += 1;
  const nameMatch = /^[A-Za-z_$][A-Za-z0-9_$:.-]*/.exec(text.slice(cursor));
  if (!nameMatch) return null;

  const name = nameMatch[0];
  cursor += name.length;
  let quote: "\"" | "'" | "`" | null = null;
  for (; cursor < text.length; cursor += 1) {
    const char = text[cursor];
    if (quote) {
      if (char === quote && !isEscaped(text, cursor)) quote = null;
      continue;
    }
    if (char === "\"" || char === "'" || char === "`") {
      quote = char;
      continue;
    }
    if (char !== ">") continue;
    return {
      name,
      closing,
      selfClosing: !closing && text.slice(start + 1, cursor).trimEnd().endsWith("/"),
      end: cursor + 1,
    };
  }
  return null;
}

function visitMarkupTags(
  text: string,
  visitor: (tag: ParsedMarkupTag) => void,
): void {
  const lowerText = text.toLowerCase();
  let rawElement: string | null = null;
  for (let index = 0; index < text.length;) {
    if (rawElement) {
      const closeStart = lowerText.indexOf(`</${rawElement}`, index);
      if (closeStart < 0) return;
      index = closeStart;
    }
    if (text.startsWith("<!--", index)) {
      const commentEnd = text.indexOf("-->", index + 4);
      if (commentEnd < 0) return;
      index = commentEnd + 3;
      continue;
    }
    if (text.startsWith("{{", index)) {
      const interpolationEnd = text.indexOf("}}", index + 2);
      if (interpolationEnd < 0) return;
      index = interpolationEnd + 2;
      continue;
    }
    if (text[index] !== "<") {
      index += 1;
      continue;
    }

    const tag = parseMarkupTag(text, index);
    if (!tag) {
      index += 1;
      continue;
    }
    visitor(tag);
    const normalizedName = tag.name.toLowerCase();
    if (rawElement && tag.closing && normalizedName === rawElement) {
      rawElement = null;
    } else if (
      !tag.closing
      && !tag.selfClosing
      && RAW_TEXT_ELEMENTS.has(normalizedName)
    ) {
      rawElement = normalizedName;
    }
    index = tag.end;
  }
}

function unmatchedSameTagDepth(text: string, tagName: string): number {
  let depth = 0;
  visitMarkupTags(text, (tag) => {
    if (tag.name.toLowerCase() !== tagName.toLowerCase()) return;
    if (tag.closing) depth = Math.max(0, depth - 1);
    else if (!tag.selfClosing) depth += 1;
  });
  return depth;
}

function sameTagClosingSurplus(text: string, tagName: string): number {
  let surplus = 0;
  visitMarkupTags(text, (tag) => {
    if (tag.name.toLowerCase() !== tagName.toLowerCase()) return;
    if (tag.closing) surplus += 1;
    else if (!tag.selfClosing) surplus -= 1;
  });
  return surplus;
}

function isVoidElement(tag: string, language: string): boolean {
  const normalizedTag = tag.toLowerCase();
  if (!VOID_ELEMENTS.has(normalizedTag)) return false;
  return language !== "vue" || tag === normalizedTag;
}

function closingTagForPosition(
  monaco: typeof import("monaco-editor"),
  model: monacoEditor.editor.ITextModel,
  position: monacoEditor.Position,
  language: string,
): string | null {
  if (model.isDisposed?.()) return null;
  const cursorOffset = model.getOffsetAt(position);
  if (cursorOffset <= 0) return null;
  const scanStartOffset = Math.max(0, cursorOffset - MAX_AUTO_CLOSE_SCAN);
  const scanStart = model.getPositionAt(scanStartOffset);
  const prefix = model.getValueInRange(new monaco.Range(
    scanStart.lineNumber,
    scanStart.column,
    position.lineNumber,
    position.column,
  ));
  if (!prefix.endsWith(">")) return null;

  const openingIndex = findOpeningTagStart(prefix);
  if (openingIndex < 0) return null;
  const tokenType = standardTokenTypeAt(model, position);
  if (tokenType !== null && tokenType !== 0) return null;
  const beforeTag = prefix.slice(0, openingIndex);
  if (!isMarkupTextContext(beforeTag)) return null;
  if (tokenType === null) {
    if (scanStartOffset > 0) return null;
  }
  if (isInsideRawTextElement(beforeTag)) return null;
  if (language === "vue" && beforeTag.lastIndexOf("{{") > beforeTag.lastIndexOf("}}")) return null;

  const source = prefix.slice(openingIndex + 1, -1).trim();
  if (!source || source.startsWith("/") || source.startsWith("!") || source.startsWith("?")) return null;
  if (source.endsWith("/")) return null;
  const tagMatch = /^([A-Za-z_$][A-Za-z0-9_$:-]*(?:\.[A-Za-z_$][A-Za-z0-9_$:-]*)*)(?=$|\s)/.exec(source);
  if (!tagMatch) return null;
  const tag = tagMatch[1];
  if (isVoidElement(tag, language)) return null;

  const suffixEndOffset = Math.min(model.getValueLength(), cursorOffset + MAX_AUTO_CLOSE_SCAN);
  const suffixEnd = model.getPositionAt(suffixEndOffset);
  const suffix = model.getValueInRange(new monaco.Range(
    position.lineNumber,
    position.column,
    suffixEnd.lineNumber,
    suffixEnd.column,
  ));
  if (new RegExp(`^\\s*<\\/${escapeRegExp(tag)}\\s*>`, "i").test(suffix)) {
    const surroundingDepth = unmatchedSameTagDepth(beforeTag, tag);
    const requiredClosings = surroundingDepth + 1;
    const suffixWasTruncated = suffixEndOffset < model.getValueLength();
    if (
      surroundingDepth === 0
      || sameTagClosingSurplus(suffix, tag) >= requiredClosings
      || suffixWasTruncated
    ) return null;
  }
  return `</${tag}>`;
}

function createTagAutoCloseController(
  monaco: typeof import("monaco-editor"),
): TagAutoCloseController {
  const active = new Map<string, number>();
  const editorDisposables = new Map<monacoEditor.editor.ICodeEditor, monacoEditor.IDisposable>();
  const applying = new WeakSet<monacoEditor.editor.ICodeEditor>();

  const isLanguageActive = (language: string): boolean => {
    return (active.get(language.toLowerCase()) ?? 0) > 0;
  };

  const attachEditor = (editor: monacoEditor.editor.ICodeEditor) => {
    if (editorDisposables.has(editor)) return;
    let disposed = false;
    let editorDisposeListener: monacoEditor.IDisposable | null = null;
    let composite: monacoEditor.IDisposable | null = null;
    const scheduleClose = (
      model: monacoEditor.editor.ITextModel,
      position: monacoEditor.Position,
      version: number,
    ) => {
      Promise.resolve().then(() => {
        if (
          disposed
          || editorDisposables.get(editor) !== composite
          || editor.getModel() !== model
          || model.isDisposed?.()
          || model.getVersionId() !== version
          || !editor.hasTextFocus()
        ) return;
        const selections = editor.getSelections();
        if (
          !selections
          || selections.length !== 1
          || !selections[0].isEmpty()
          || selections[0].positionLineNumber !== position.lineNumber
          || selections[0].positionColumn !== position.column
        ) return;
        const language = model.getLanguageId().toLowerCase();
        if (!isLanguageActive(language)) return;
        const closingTag = closingTagForPosition(monaco, model, position, language);
        if (!closingTag) return;
        const insertion = new monaco.Range(
          position.lineNumber,
          position.column,
          position.lineNumber,
          position.column,
        );
        const cursor = new monaco.Selection(
          position.lineNumber,
          position.column,
          position.lineNumber,
          position.column,
        );
        applying.add(editor);
        try {
          editor.executeEdits(AUTO_CLOSE_EDIT_SOURCE, [{
            range: insertion,
            text: closingTag,
            forceMoveMarkers: true,
          }], [cursor]);
        } finally {
          applying.delete(editor);
        }
      });
    };
    const contentListener = editor.onDidChangeModelContent((event) => {
      if (
        applying.has(editor)
        || event.isFlush
        || event.isUndoing
        || event.isRedoing
        || event.changes.length !== 1
      ) return;
      const change = event.changes[0];
      if (change.text !== ">" || change.rangeLength !== 0) return;
      const model = editor.getModel();
      if (!model || model.isDisposed?.()) return;
      scheduleClose(model, model.getPositionAt(change.rangeOffset + 1), event.versionId);
    });
    // Monaco 0.52 emits this runtime event after an auto-closing-pair overtype,
    // but ICodeEditor does not expose it in the public declaration.
    const typingEditor = editor as unknown as EditorWithTypingEvent;
    const typingListener = typingEditor.onDidType?.((text) => {
      if (text !== ">" || applying.has(editor)) return;
      const model = editor.getModel();
      const position = editor.getPosition();
      if (!model || !position || model.isDisposed?.()) return;
      scheduleClose(model, position, model.getVersionId());
    }) ?? null;
    composite = {
      dispose() {
        if (disposed) return;
        disposed = true;
        contentListener.dispose();
        typingListener?.dispose();
        editorDisposeListener?.dispose();
        editorDisposables.delete(editor);
      },
    };
    editorDisposeListener = editor.onDidDispose(() => composite?.dispose());
    editorDisposables.set(editor, composite);
  };

  const createListener = monaco.editor.onDidCreateEditor(attachEditor);
  for (const editor of monaco.editor.getEditors()) attachEditor(editor);

  return {
    add(entries) {
      for (const entry of entries) {
        active.set(entry.language, (active.get(entry.language) ?? 0) + 1);
      }
    },
    remove(entries) {
      for (const entry of entries) {
        const current = active.get(entry.language) ?? 0;
        if (current <= 1) active.delete(entry.language);
        else active.set(entry.language, current - 1);
      }
    },
    isEmpty() {
      return active.size === 0;
    },
    dispose() {
      createListener.dispose();
      for (const disposable of [...editorDisposables.values()]) disposable.dispose();
      active.clear();
    },
  };
}

function leaseTagAutoClose(
  monaco: typeof import("monaco-editor"),
  entries: AutoCloseLanguage[],
): () => void {
  if (entries.length === 0) return () => undefined;
  let controller = tagAutoCloseByMonaco.get(monaco);
  if (!controller) {
    controller = createTagAutoCloseController(monaco);
    tagAutoCloseByMonaco.set(monaco, controller);
  }
  controller.add(entries);
  let disposed = false;
  return () => {
    if (disposed) return;
    disposed = true;
    controller?.remove(entries);
    if (controller?.isEmpty()) {
      controller.dispose();
      tagAutoCloseByMonaco.delete(monaco);
    }
  };
}

export function registerEmmetProviders(
  monaco: typeof import("monaco-editor"),
  options: EmmetProviderOptions,
): monacoEditor.IDisposable {
  if (!options.enabled) return { dispose: () => undefined };

  const includeLanguages = {
    ...DEFAULT_EMMET_INCLUDE_LANGUAGES,
    ...(options.includeLanguages ?? {}),
  };
  let registry = providersByMonaco.get(monaco);
  if (!registry) {
    registry = new Map();
    providersByMonaco.set(monaco, registry);
  }

  const leasedKeys: string[] = [];
  const autoCloseLanguages: AutoCloseLanguage[] = [];
  for (const [language, emmetLanguage] of Object.entries(includeLanguages)) {
    const normalizedLanguage = language.trim().toLowerCase();
    const kind = emmetKindForLanguage(emmetLanguage.trim());
    if (!normalizedLanguage || !kind) continue;

    const key = `${kind}:${normalizedLanguage}`;
    const current = registry.get(key);
    if (current) {
      current.owners += 1;
    } else {
      registry.set(key, {
        owners: 1,
        dispose: registerProvider(monaco, normalizedLanguage, kind),
      });
    }
    leasedKeys.push(key);
    if (kind === "html") autoCloseLanguages.push({ language: normalizedLanguage });
  }
  const disposeTagAutoClose = leaseTagAutoClose(monaco, autoCloseLanguages);

  let disposed = false;
  return {
    dispose() {
      if (disposed) return;
      disposed = true;
      disposeTagAutoClose();
      for (const key of leasedKeys) {
        const entry = registry.get(key);
        if (!entry) continue;
        entry.owners -= 1;
        if (entry.owners > 0) continue;
        registry.delete(key);
        entry.dispose();
      }
    },
  };
}
