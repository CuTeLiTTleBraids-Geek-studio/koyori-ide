import type * as monacoEditor from "monaco-editor";
import { Position, Selection as SelectionValue } from "@/lib/extensionHost/vscodeApi";
import type {
  DecorationOptions,
  DecorationRenderOptions,
  Disposable,
  Range,
  Selection,
  TextDocument,
  TextEditor,
  TextEditorDecorationType,
} from "@/lib/extensionHost/vscodeApi";

type DecorationSurface = {
  id: string;
  path: string;
  editor: monacoEditor.editor.IStandaloneCodeEditor;
  monaco: typeof import("monaco-editor");
};

type DecorationRecord = {
  key: string;
  ownerExtensionId: string;
  className: string;
  options: DecorationRenderOptions;
  disposed: boolean;
  style?: HTMLStyleElement;
  ids: Map<string, string[]>;
};

const surfacesByPath = new Map<string, Map<string, DecorationSurface>>();
const decorationTypes = new Map<string, DecorationRecord>();
let nextDecorationId = 1;
let nextSurfaceId = 1;

function cssValue(
  value: unknown,
  kind: "color" | "borderStyle" | "borderWidth",
): string | undefined {
  if (value && typeof value === "object") {
    const id = (value as Record<string, unknown>).id;
    if (kind === "color" && typeof id === "string" && /^[A-Za-z0-9_.-]{1,120}$/.test(id)) {
      return "var(--vscode-" + id.replace(/\./g, "-") + ")";
    }
    return undefined;
  }
  if (typeof value === "number") {
    return kind === "borderWidth" && Number.isFinite(value) && value >= 0 && value <= 100
      ? String(value)
      : undefined;
  }
  if (typeof value !== "string" || value.length > 160 || /[;{}<>"'\r\n]/.test(value)) return undefined;
  if (kind === "borderStyle") {
    return /^(?:none|hidden|dotted|dashed|solid|double|groove|ridge|inset|outset)$/.test(value)
      ? value
      : undefined;
  }
  if (kind === "borderWidth") {
    return /^(?:0|(?:\d+(?:\.\d+)?)(?:px|em|rem|pt|%)?)$/.test(value)
      ? value
      : undefined;
  }
  if (/^#[0-9a-fA-F]{3,8}$/.test(value)) return value;
  if (/^(?:rgb|rgba|hsl|hsla)\([0-9.%\s,+-]+\)$/.test(value)) return value;
  if (/^(?:var\(--[A-Za-z0-9_-]{1,80}\)|[A-Za-z][A-Za-z0-9_-]{0,40})$/.test(value)) return value;
  return undefined;
}

function addStyle(record: DecorationRecord): void {
  if (typeof document === "undefined") return;
  const declarations: string[] = [];
  const map: Array<[keyof DecorationRenderOptions, string]> = [
    ["borderStyle", "border-style"],
    ["borderWidth", "border-width"],
    ["borderColor", "border-color"],
    ["backgroundColor", "background-color"],
    ["color", "color"],
  ];
  for (const [key, cssKey] of map) {
    const kind = key === "borderStyle"
      ? "borderStyle"
      : key === "borderWidth"
        ? "borderWidth"
        : "color";
    const value = cssValue(record.options[key], kind);
    if (value) declarations.push(cssKey + ":" + value + ";");
  }
  if (record.options.isWholeLine) declarations.push("display:block;");
  if (declarations.length === 0) return;
  const style = document.createElement("style");
  style.dataset.koyoriExtensionDecoration = record.key;
  style.textContent = ".monaco-editor ." + record.className + "{" + declarations.join("") + "}";
  document.head.appendChild(style);
  record.style = style;
}

function rangeParts(value: Range | DecorationOptions): Range {
  return "range" in value ? value.range : value;
}

function toMonacoRange(monaco: typeof import("monaco-editor"), value: Range | DecorationOptions): monacoEditor.Range {
  const range = rangeParts(value);
  return new monaco.Range(
    Math.max(1, range.start.line + 1),
    Math.max(1, range.start.character + 1),
    Math.max(1, range.end.line + 1),
    Math.max(1, range.end.character + 1),
  );
}

function clearOnSurface(record: DecorationRecord, surface: DecorationSurface): void {
  const ids = record.ids.get(surface.id);
  if (!ids) return;
  try { surface.editor.deltaDecorations(ids, []); } catch { /* editor may be disposing */ }
  record.ids.delete(surface.id);
}

export function registerExtensionEditorSurface(
  path: string,
  editor: monacoEditor.editor.IStandaloneCodeEditor,
  monaco: typeof import("monaco-editor"),
): Disposable {
  const normalized = path.replaceAll("\\", "/");
  const id = "extension-surface-" + nextSurfaceId++;
  const surface: DecorationSurface = { id, path: normalized, editor, monaco };
  const surfaces = surfacesByPath.get(normalized) ?? new Map<string, DecorationSurface>();
  surfaces.set(id, surface);
  surfacesByPath.set(normalized, surfaces);
  return {
    dispose(): void {
      const current = surfacesByPath.get(normalized);
      if (current?.get(id)?.editor === editor) {
        current.delete(id);
        if (current.size === 0) surfacesByPath.delete(normalized);
      }
      for (const record of decorationTypes.values()) clearOnSurface(record, surface);
    },
  };
}

export function createExtensionTextEditor(
  path: string,
  document: TextDocument,
): TextEditor {
  const normalized = path.replaceAll("\\", "/");
  const getSurface = (): DecorationSurface | undefined => {
    const surfaces = surfacesByPath.get(normalized);
    return surfaces ? Array.from(surfaces.values()).at(-1) : undefined;
  };
  return {
    document,
    get selection(): Selection | undefined {
      const selection = getSurface()?.editor.getSelection();
      if (!selection) return undefined;
      return new SelectionValue(
        new Position(selection.selectionStartLineNumber - 1, selection.selectionStartColumn - 1),
        new Position(selection.positionLineNumber - 1, selection.positionColumn - 1),
      );
    },
    set selection(value: Selection | undefined) {
      if (!value) return;
      const surface = getSurface();
      if (!surface) throw new Error("No active Monaco editor surface for " + path);
      surface.editor.setSelection(new surface.monaco.Selection(
        value.anchor.line + 1,
        value.anchor.character + 1,
        value.active.line + 1,
        value.active.character + 1,
      ));
    },
    revealRange(range: Range, revealType = 0): void {
      const surface = getSurface();
      if (!surface) throw new Error("No active Monaco editor surface for " + path);
      const monacoRange = toMonacoRange(surface.monaco, range);
      if (revealType === 1) surface.editor.revealRangeInCenter(monacoRange);
      else if (revealType === 2) surface.editor.revealRangeInCenterIfOutsideViewport(monacoRange);
      else if (revealType === 3) surface.editor.revealRangeAtTop(monacoRange);
      else surface.editor.revealRange(monacoRange);
    },
    setDecorations(type: TextEditorDecorationType, ranges: readonly (Range | DecorationOptions)[]): void {
      applyExtensionDecorations(normalized, type.key, ranges);
    },
  };
}

export function createExtensionDecorationType(
  extensionId: string,
  options: DecorationRenderOptions,
): TextEditorDecorationType {
  const id = nextDecorationId++;
  const key = "extension-decoration-" + id;
  const record: DecorationRecord = {
    key,
    ownerExtensionId: extensionId,
    className: "koyori-extension-decoration-" + id,
    options: { ...options },
    disposed: false,
    ids: new Map(),
  };
  decorationTypes.set(key, record);
  addStyle(record);
  return {
    key,
    dispose(): void {
      if (record.disposed) return;
      record.disposed = true;
      for (const surfaces of surfacesByPath.values()) {
        for (const surface of surfaces.values()) clearOnSurface(record, surface);
      }
      record.style?.remove();
      decorationTypes.delete(key);
    },
  };
}

export function applyExtensionDecorations(
  path: string,
  key: string,
  ranges: readonly (Range | DecorationOptions)[],
): void {
  const record = decorationTypes.get(key);
  if (!record || record.disposed) throw new Error("Decoration type " + key + " is disposed");
  const normalized = path.replaceAll("\\", "/");
  const surfaces = surfacesByPath.get(normalized);
  const surface = surfaces ? Array.from(surfaces.values()).at(-1) : undefined;
  if (!surface) throw new Error("No active Monaco editor surface for " + path);
  const options = ranges.map((value) => ({
    range: toMonacoRange(surface.monaco, value),
    options: {
      className: record.className,
      isWholeLine: record.options.isWholeLine,
    },
  }));
  const previous = record.ids.get(surface.id) ?? [];
  const next = surface.editor.deltaDecorations(previous, options);
  record.ids.set(surface.id, next);
}
