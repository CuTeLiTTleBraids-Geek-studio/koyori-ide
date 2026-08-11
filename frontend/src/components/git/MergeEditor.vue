<script lang="ts">
// Koyori IDE 组件 · Merge Editor。
// 喵，这是 Merge Editor，负责 Koyori IDE 的界面呈现喵~
export type MergeResolution = "ours" | "theirs" | "both";

export interface MergeResolutionOptions {
  oursEndsWithNewline?: boolean;
  theirsEndsWithNewline?: boolean;
}

export interface MergeConflictBlock {
  id: string;
  startOffset: number;
  endOffset: number;
  startLine: number;
  separatorLine: number;
  endLine: number;
  oursLabel: string;
  baseLabel?: string;
  theirsLabel: string;
  ours: string;
  base?: string;
  theirs: string;
  markerSize?: number;
  raw: string;
}

export interface ConflictRegion {
  startLine: number;
  endLine: number;
  oursLines: string[];
  theirsLines: string[];
  baseLines: string[];
}

export interface MergeSavePayload {
  filePath: string;
  content: string;
  repoPath?: string;
  path?: string;
}

export interface MergeSaveAdapter {
  saveResult?: (payload: MergeSavePayload) => Promise<void>;
  writeFile?: (path: string, content: string) => Promise<void>;
  resolveConflict?: (filePath: string) => Promise<void>;
}

interface ContentLine {
  text: string;
  startOffset: number;
  endOffset: number;
  lineNumber: number;
}

interface ParsedConflictBlock extends Omit<MergeConflictBlock, "id"> {
  generatedId: string;
}

interface MergeLineEdit {
  start: number;
  end: number;
  replacement: string[];
}

interface MergeOperation {
  start: number;
  end: number;
  content: string;
}

type ConflictMarkerKind = "start" | "base" | "separator" | "end";

interface ConflictMarker {
  kind: ConflictMarkerKind;
  size: number;
  label: string;
}

const MIN_CONFLICT_MARKER_SIZE = 7;

function parseConflictMarker(line: string): ConflictMarker | null {
  const match = /^([<|=>]+)(?:[ \t](.*))?$/.exec(line);
  if (!match || match[1].length < MIN_CONFLICT_MARKER_SIZE) return null;
  const marker = match[1];
  const character = marker[0];
  if (![...marker].every((candidate) => candidate === character)) return null;
  const kind: ConflictMarkerKind | null = character === "<"
    ? "start"
    : character === "|"
      ? "base"
      : character === "="
        ? "separator"
          : character === ">"
          ? "end"
          : null;
  if (kind === "separator" && match[2] !== undefined) return null;
  return kind ? { kind, size: marker.length, label: match[2]?.trim() ?? "" } : null;
}

function splitContentLines(content: string): ContentLine[] {
  const lines: ContentLine[] = [];
  let startOffset = 0;
  let lineNumber = 1;

  while (startOffset < content.length) {
    const lineFeedOffset = content.indexOf("\n", startOffset);
    const endOffset = lineFeedOffset === -1 ? content.length : lineFeedOffset + 1;
    let textEndOffset = lineFeedOffset === -1 ? content.length : lineFeedOffset;
    if (textEndOffset > startOffset && content[textEndOffset - 1] === "\r") {
      textEndOffset -= 1;
    }

    lines.push({
      text: content.slice(startOffset, textEndOffset),
      startOffset,
      endOffset,
      lineNumber,
    });
    startOffset = endOffset;
    lineNumber += 1;
  }

  return lines;
}

function hashConflict(value: string): string {
  let hash = 0x811c9dc5;
  for (let index = 0; index < value.length; index += 1) {
    hash ^= value.charCodeAt(index);
    hash = Math.imul(hash, 0x01000193);
  }
  return (hash >>> 0).toString(36);
}

function cleanMarkerLabel(label: string, fallback: string): string {
  return label.replace(/[\r\n]+/g, " ").trim() || fallback;
}

function appendLineBreak(content: string, newline: string): string {
  if (!content || content.endsWith("\n")) return content;
  return `${content}${newline}`;
}

function generatedConflictId(raw: string, duplicateNumber: number): string {
  const base = `merge-conflict-${hashConflict(raw)}`;
  return duplicateNumber === 1 ? base : `${base}-${duplicateNumber}`;
}

function closestBlock<T extends { startOffset: number }>(
  blocks: readonly T[],
  targetOffset: number,
): T | undefined {
  return blocks.reduce<T | undefined>((closest, block) => {
    if (!closest) return block;
    const closestDistance = Math.abs(closest.startOffset - targetOffset);
    const blockDistance = Math.abs(block.startOffset - targetOffset);
    return blockDistance < closestDistance ? block : closest;
  }, undefined);
}

/** Build the initial editable result without inventing a merge when both sides differ. */
export function createInitialMergeResult(
  ours: string,
  theirs: string,
  oursLabel = "OURS",
  theirsLabel = "THEIRS",
  preferredNewline?: "\n" | "\r\n",
): string {
  if (ours === theirs) return ours;

  const newline = preferredNewline
    ?? (ours.includes("\r\n") || theirs.includes("\r\n") ? "\r\n" : "\n");
  const safeOursLabel = cleanMarkerLabel(oursLabel, "OURS");
  const safeTheirsLabel = cleanMarkerLabel(theirsLabel, "THEIRS");
  return [
    `<<<<<<< ${safeOursLabel}${newline}`,
    appendLineBreak(ours, newline),
    `=======${newline}`,
    appendLineBreak(theirs, newline),
    `>>>>>>> ${safeTheirsLabel}`,
  ].join("");
}

function mergeLines(content: string): string[] {
  return splitContentLines(content).map((line) =>
    content.slice(line.startOffset, line.endOffset)
  );
}

function conflictRegionContent(lines: readonly string[], newline: "\n" | "\r\n"): string {
  return lines
    .map((line) => line.replace(/[\r\n]+$/g, ""))
    .join(newline);
}

/** Insert standard conflict markers for each pre-parsed region. */
export function createConflictRegionResult(
  oursContent: string,
  regions: readonly ConflictRegion[],
  oursLabel = "OURS",
  theirsLabel = "THEIRS",
): string {
  if (regions.length === 0) return oursContent;
  const newline: "\n" | "\r\n" = oursContent.includes("\r\n") ? "\r\n" : "\n";
  const lines = mergeLines(oursContent);
  const prepared = regions.map((region, index) => {
    if (
      !Number.isInteger(region.startLine)
      || !Number.isInteger(region.endLine)
      || region.startLine < 1
      || region.endLine < region.startLine
    ) {
      throw new RangeError(`Invalid conflict region ${index + 1}: line range is invalid`);
    }
    if (
      !Array.isArray(region.oursLines)
      || !Array.isArray(region.theirsLines)
      || !Array.isArray(region.baseLines)
    ) {
      throw new TypeError(`Invalid conflict region ${index + 1}: content lines are invalid`);
    }

    const start = region.startLine - 1;
    const appendsAtEOF = start === lines.length && region.endLine === region.startLine;
    if (start > lines.length || (region.endLine > lines.length && !appendsAtEOF)) {
      throw new RangeError(`Invalid conflict region ${index + 1}: line range is outside the file`);
    }
    return {
      region,
      start,
      end: Math.min(region.endLine, lines.length),
      index,
    };
  }).sort((left, right) =>
    right.start - left.start || right.end - left.end
  );

  let nextRegionStart = Number.POSITIVE_INFINITY;
  for (const item of prepared) {
    if (
      item.end > nextRegionStart
      || (item.start === nextRegionStart && item.end === nextRegionStart)
    ) {
      throw new RangeError(`Invalid conflict region ${item.index + 1}: regions overlap`);
    }
    nextRegionStart = item.start;
  }

  for (const { region, start, end } of prepared) {
    const ours = conflictRegionContent(region.oursLines, newline);
    const theirs = conflictRegionContent(region.theirsLines, newline);
    let replacement = createInitialMergeResult(
      ours,
      theirs,
      oursLabel,
      theirsLabel,
      newline,
    );
    if (end < lines.length && replacement && !replacement.endsWith("\n")) {
      replacement += newline;
    }
    lines.splice(start, Math.max(0, end - start), replacement);
  }

  return lines.join("");
}

function lineEdits(base: readonly string[], variant: readonly string[]): MergeLineEdit[] {
  let prefix = 0;
  while (
    prefix < base.length
    && prefix < variant.length
    && base[prefix] === variant[prefix]
  ) {
    prefix += 1;
  }

  let suffix = 0;
  while (
    suffix < base.length - prefix
    && suffix < variant.length - prefix
    && base[base.length - suffix - 1] === variant[variant.length - suffix - 1]
  ) {
    suffix += 1;
  }

  const baseMiddle = base.slice(prefix, base.length - suffix);
  const variantMiddle = variant.slice(prefix, variant.length - suffix);
  if (baseMiddle.length === 0 && variantMiddle.length === 0) return [];

  // Keep memory bounded for unusually large generated files. Falling back to
  // one conservative edit can create a larger conflict, but never loses data.
  const cellCount = (baseMiddle.length + 1) * (variantMiddle.length + 1);
  if (cellCount > 4_000_000) {
    return [{
      start: prefix,
      end: base.length - suffix,
      replacement: [...variantMiddle],
    }];
  }

  const lcs = Array.from(
    { length: baseMiddle.length + 1 },
    () => new Uint32Array(variantMiddle.length + 1),
  );
  for (let baseIndex = baseMiddle.length - 1; baseIndex >= 0; baseIndex -= 1) {
    for (
      let variantIndex = variantMiddle.length - 1;
      variantIndex >= 0;
      variantIndex -= 1
    ) {
      lcs[baseIndex][variantIndex] = baseMiddle[baseIndex] === variantMiddle[variantIndex]
        ? lcs[baseIndex + 1][variantIndex + 1] + 1
        : Math.max(lcs[baseIndex + 1][variantIndex], lcs[baseIndex][variantIndex + 1]);
    }
  }

  const edits: MergeLineEdit[] = [];
  let baseIndex = 0;
  let variantIndex = 0;
  let editStart = -1;
  let replacement: string[] = [];
  const flush = () => {
    if (editStart < 0) return;
    edits.push({
      start: prefix + editStart,
      end: prefix + baseIndex,
      replacement,
    });
    editStart = -1;
    replacement = [];
  };

  while (baseIndex < baseMiddle.length || variantIndex < variantMiddle.length) {
    if (
      baseIndex < baseMiddle.length
      && variantIndex < variantMiddle.length
      && baseMiddle[baseIndex] === variantMiddle[variantIndex]
    ) {
      flush();
      baseIndex += 1;
      variantIndex += 1;
      continue;
    }

    if (editStart < 0) editStart = baseIndex;
    const insertVariantLine = variantIndex < variantMiddle.length
      && (
        baseIndex === baseMiddle.length
        || lcs[baseIndex][variantIndex + 1] >= lcs[baseIndex + 1][variantIndex]
      );
    if (insertVariantLine) {
      replacement.push(variantMiddle[variantIndex]);
      variantIndex += 1;
    } else {
      baseIndex += 1;
    }
  }
  flush();
  return edits;
}

function editsOverlap(left: MergeLineEdit, right: MergeLineEdit): boolean {
  const leftInsertion = left.start === left.end;
  const rightInsertion = right.start === right.end;
  if (leftInsertion && rightInsertion) return left.start === right.start;
  if (leftInsertion) return left.start > right.start && left.start < right.end;
  if (rightInsertion) return right.start > left.start && right.start < left.end;
  return left.start < right.end && right.start < left.end;
}

function applyLineEdits(
  base: readonly string[],
  start: number,
  end: number,
  edits: readonly MergeLineEdit[],
): string {
  const output: string[] = [];
  let cursor = start;
  for (const edit of edits) {
    output.push(...base.slice(cursor, edit.start), ...edit.replacement);
    cursor = edit.end;
  }
  output.push(...base.slice(cursor, end));
  return output.join("");
}

function conflictOperations(
  base: readonly string[],
  ours: readonly MergeLineEdit[],
  theirs: readonly MergeLineEdit[],
  oursLabel: string,
  theirsLabel: string,
  newline: string,
): { operations: MergeOperation[]; conflicted: Set<number> } {
  const total = ours.length + theirs.length;
  const parent = Array.from({ length: total }, (_, index) => index);
  const find = (index: number): number => {
    let root = index;
    while (parent[root] !== root) root = parent[root];
    while (parent[index] !== index) {
      const next = parent[index];
      parent[index] = root;
      index = next;
    }
    return root;
  };
  const union = (left: number, right: number) => {
    const leftRoot = find(left);
    const rightRoot = find(right);
    if (leftRoot !== rightRoot) parent[rightRoot] = leftRoot;
  };

  const conflicted = new Set<number>();
  for (let oursIndex = 0; oursIndex < ours.length; oursIndex += 1) {
    for (let theirsIndex = 0; theirsIndex < theirs.length; theirsIndex += 1) {
      if (!editsOverlap(ours[oursIndex], theirs[theirsIndex])) continue;
      const theirsNode = ours.length + theirsIndex;
      union(oursIndex, theirsNode);
      conflicted.add(oursIndex);
      conflicted.add(theirsNode);
    }
  }

  const groups = new Map<number, { ours: MergeLineEdit[]; theirs: MergeLineEdit[] }>();
  for (const node of conflicted) {
    const root = find(node);
    const group = groups.get(root) ?? { ours: [], theirs: [] };
    if (node < ours.length) group.ours.push(ours[node]);
    else group.theirs.push(theirs[node - ours.length]);
    groups.set(root, group);
  }

  const operations: MergeOperation[] = [];
  for (const group of groups.values()) {
    group.ours.sort((left, right) => left.start - right.start);
    group.theirs.sort((left, right) => left.start - right.start);
    const edits = [...group.ours, ...group.theirs];
    const start = Math.min(...edits.map((edit) => edit.start));
    const end = Math.max(...edits.map((edit) => edit.end));
    const baseContent = base.slice(start, end).join("");
    const oursContent = applyLineEdits(base, start, end, group.ours);
    const theirsContent = applyLineEdits(base, start, end, group.theirs);

    let content: string;
    if (oursContent === theirsContent) content = oursContent;
    else if (oursContent === baseContent) content = theirsContent;
    else if (theirsContent === baseContent) content = oursContent;
    else {
      content = createInitialMergeResult(
        oursContent,
        theirsContent,
        oursLabel,
        theirsLabel,
        newline as "\n" | "\r\n",
      );
    }
    if (content.includes("<<<<<<<") && end < base.length && !content.endsWith("\n")) {
      content += newline;
    }
    operations.push({ start, end, content });
  }

  return { operations, conflicted };
}

/** Perform a conservative line-based diff3 merge and mark only overlapping edits. */
export function createInitialThreeWayResult(
  base: string,
  ours: string,
  theirs: string,
  oursLabel = "OURS",
  theirsLabel = "THEIRS",
): string {
  if (ours === theirs) return ours;
  if (ours === base) return theirs;
  if (theirs === base) return ours;

  const baseLines = mergeLines(base);
  const oursEdits = lineEdits(baseLines, mergeLines(ours));
  const theirsEdits = lineEdits(baseLines, mergeLines(theirs));
  const newline = base.includes("\r\n") || ours.includes("\r\n") || theirs.includes("\r\n")
    ? "\r\n"
    : "\n";
  const { operations, conflicted } = conflictOperations(
    baseLines,
    oursEdits,
    theirsEdits,
    oursLabel,
    theirsLabel,
    newline,
  );

  for (let index = 0; index < oursEdits.length; index += 1) {
    if (conflicted.has(index)) continue;
    const edit = oursEdits[index];
    operations.push({ start: edit.start, end: edit.end, content: edit.replacement.join("") });
  }
  for (let index = 0; index < theirsEdits.length; index += 1) {
    if (conflicted.has(oursEdits.length + index)) continue;
    const edit = theirsEdits[index];
    operations.push({ start: edit.start, end: edit.end, content: edit.replacement.join("") });
  }

  operations.sort((left, right) =>
    left.start - right.start || (left.end - left.start) - (right.end - right.start)
  );
  const output: string[] = [];
  let cursor = 0;
  for (const operation of operations) {
    output.push(...baseLines.slice(cursor, operation.start), operation.content);
    cursor = operation.end;
  }
  output.push(...baseLines.slice(cursor));
  return output.join("");
}

/** Parse every complete standard Git conflict block while preserving exact content offsets. */
export function parseConflictBlocks(content: string): MergeConflictBlock[] {
  const lines = splitContentLines(content);
  const parsed: ParsedConflictBlock[] = [];
  const duplicateCounts = new Map<string, number>();

  for (let startIndex = 0; startIndex < lines.length; startIndex += 1) {
    const startLine = lines[startIndex];
    const startMarker = parseConflictMarker(startLine.text);
    if (startMarker?.kind !== "start") continue;

    let baseIndex = -1;
    let separatorIndex = -1;
    for (let index = startIndex + 1; index < lines.length; index += 1) {
      const marker = parseConflictMarker(lines[index].text);
      if (marker?.size === startMarker.size && marker.kind === "base" && baseIndex === -1) {
        baseIndex = index;
        continue;
      }
      if (marker?.size === startMarker.size && marker.kind === "separator") {
        separatorIndex = index;
        break;
      }
      if (marker?.kind === "start" || marker?.kind === "end") break;
    }
    if (separatorIndex === -1) continue;

    let endIndex = -1;
    let endMarker: ConflictMarker | null = null;
    for (let index = separatorIndex + 1; index < lines.length; index += 1) {
      const marker = parseConflictMarker(lines[index].text);
      if (marker?.size === startMarker.size && marker.kind === "end") {
        endIndex = index;
        endMarker = marker;
        break;
      }
      if (marker?.kind === "start") break;
    }
    if (endIndex === -1 || !endMarker) continue;

    const baseLine = baseIndex >= 0 ? lines[baseIndex] : null;
    const separatorLine = lines[separatorIndex];
    const endLine = lines[endIndex];
    const raw = content.slice(startLine.startOffset, endLine.endOffset);
    const duplicateNumber = (duplicateCounts.get(raw) ?? 0) + 1;
    duplicateCounts.set(raw, duplicateNumber);

    parsed.push({
      generatedId: generatedConflictId(raw, duplicateNumber),
      startOffset: startLine.startOffset,
      endOffset: endLine.endOffset,
      startLine: startLine.lineNumber,
      separatorLine: separatorLine.lineNumber,
      endLine: endLine.lineNumber,
      oursLabel: startMarker.label,
      baseLabel: baseLine ? parseConflictMarker(baseLine.text)?.label : undefined,
      theirsLabel: endMarker.label,
      ours: content.slice(
        startLine.endOffset,
        baseLine?.startOffset ?? separatorLine.startOffset,
      ),
      base: baseLine
        ? content.slice(baseLine.endOffset, separatorLine.startOffset)
        : undefined,
      theirs: content.slice(separatorLine.endOffset, endLine.startOffset),
      markerSize: startMarker.size,
      raw,
    });

    startIndex = endIndex;
  }

  return parsed.map(({ generatedId, ...block }) => ({ id: generatedId, ...block }));
}

/** Detect complete or malformed conflict markers before writing and staging. */
export function hasConflictMarkers(content: string): boolean {
  return splitContentLines(content).some((line) => {
    const marker = parseConflictMarker(line.text);
    return marker?.kind === "start"
      || marker?.kind === "base"
      || marker?.kind === "end";
  });
}

/**
 * Keep UI keys stable as text before a conflict changes. Exact blocks are matched first,
 * then an edited block can retain the nearest compatible marker pair's identifier.
 */
export function stabilizeConflictBlockIds(
  nextBlocks: readonly MergeConflictBlock[],
  previousBlocks: readonly MergeConflictBlock[],
): MergeConflictBlock[] {
  const availablePrevious = new Set(previousBlocks);
  const assigned = new Map<MergeConflictBlock, string>();

  for (const block of nextBlocks) {
    const candidates = previousBlocks.filter(
      (previous) => availablePrevious.has(previous) && previous.raw === block.raw,
    );
    const match = closestBlock(candidates, block.startOffset);
    if (match) {
      assigned.set(block, match.id);
      availablePrevious.delete(match);
    }
  }

  for (const block of nextBlocks) {
    if (assigned.has(block)) continue;
    const candidates = previousBlocks.filter(
      (previous) =>
        availablePrevious.has(previous) &&
        previous.oursLabel === block.oursLabel &&
        previous.theirsLabel === block.theirsLabel,
    );
    const match = closestBlock(candidates, block.startOffset);
    if (match) {
      assigned.set(block, match.id);
      availablePrevious.delete(match);
    }
  }

  const usedIds = new Set<string>();
  return nextBlocks.map((block) => {
    const preferredId = assigned.get(block) ?? block.id;
    let id = preferredId;
    let suffix = 2;
    while (usedIds.has(id)) {
      id = `${preferredId}-${suffix}`;
      suffix += 1;
    }
    usedIds.add(id);
    return id === block.id ? { ...block } : { ...block, id };
  });
}

function currentConflictBlock(
  content: string,
  blockOrId: MergeConflictBlock | string,
): MergeConflictBlock {
  if (typeof blockOrId !== "string") {
    const currentRaw = content.slice(blockOrId.startOffset, blockOrId.endOffset);
    if (currentRaw === blockOrId.raw) return blockOrId;
  }

  const id = typeof blockOrId === "string" ? blockOrId : blockOrId.id;
  const block = parseConflictBlocks(content).find((candidate) => candidate.id === id);
  if (!block) throw new Error(`Conflict block not found: ${id}`);
  return block;
}

/** Replace exactly one parsed block; offsets for later blocks remain valid after reparsing. */
export function replaceConflictBlock(
  content: string,
  blockOrId: MergeConflictBlock | string,
  replacement: string,
): string {
  const block = currentConflictBlock(content, blockOrId);
  return `${content.slice(0, block.startOffset)}${replacement}${content.slice(block.endOffset)}`;
}

/** Resolve one block with either side or with ours followed by theirs. */
export function resolveConflictBlock(
  content: string,
  blockOrId: MergeConflictBlock | string,
  resolution: MergeResolution,
  options?: MergeResolutionOptions,
): string {
  const block = currentConflictBlock(content, blockOrId);
  const stripSyntheticFinalNewline = (value: string, sourceEndsWithNewline?: boolean): string => {
    if (sourceEndsWithNewline !== false || block.endOffset !== content.length) return value;
    if (value.endsWith("\r\n")) return value.slice(0, -2);
    return value.endsWith("\n") ? value.slice(0, -1) : value;
  };
  const ours = stripSyntheticFinalNewline(block.ours, options?.oursEndsWithNewline);
  const theirs = stripSyntheticFinalNewline(block.theirs, options?.theirsEndsWithNewline);
  const replacement = resolution === "ours"
    ? ours
    : resolution === "theirs"
      ? theirs
      : theirs.length > 0
        ? `${block.ours}${theirs}`
        : ours;
  return replaceConflictBlock(content, block, replacement);
}

/** Join a repository root and validated Git-relative path without losing Windows separators. */
export function joinRepoFilePath(repoPath: string, filePath: string): string {
  if (filePath.includes("\0")) {
    throw new Error("Merge file path contains an invalid null character");
  }
  if (/^[\\/]/.test(filePath) || /^[A-Za-z]:/.test(filePath)) {
    throw new Error("Merge file path must be repository-relative");
  }

  const separator = repoPath.includes("\\") && !repoPath.includes("/") ? "\\" : "/";
  const segments = filePath.split(/[\\/]+/).filter((segment) => segment && segment !== ".");
  if (segments.some((segment) => segment === "..")) {
    throw new Error("Merge file path must not traverse outside the repository");
  }
  const relative = segments.join(separator);
  if (!repoPath) return relative;
  if (!relative) return repoPath;

  const root = repoPath.replace(/[\\/]+$/, "");
  return `${root}${separator}${relative}`;
}
</script>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import * as monaco from "monaco-editor";
import {
  Check,
  Close,
  CopyDocument,
  DocumentChecked,
  EditPen,
  Right,
} from "@element-plus/icons-vue";
import { fileService, gitService } from "@/api/services";
import { useI18n } from "@/lib/i18n";
import { detectLanguage } from "@/lib/language";
import { getMonacoThemeName } from "@/lib/monaco-themes";
import { appState } from "@/stores/app";

interface MergeEditorProps {
  filePath: string;
  oursContent?: string;
  theirsContent?: string;
  baseContent?: string;
  conflictRegions?: ConflictRegion[];
  repoPath?: string;
  ours?: string;
  theirs?: string;
  base?: string;
  initialResult?: string;
  language?: string;
  saveAdapter?: MergeSaveAdapter;
}

interface DiffResources {
  editor: monaco.editor.IStandaloneDiffEditor;
  original: monaco.editor.ITextModel;
  modified: monaco.editor.ITextModel;
}

interface ResultResources {
  hostEditor: monaco.editor.IStandaloneDiffEditor | monaco.editor.IStandaloneCodeEditor;
  editor: monaco.editor.IStandaloneCodeEditor;
  model: monaco.editor.ITextModel;
  original?: monaco.editor.ITextModel;
}

const props = withDefaults(defineProps<MergeEditorProps>(), {
  conflictRegions: () => [],
});
const emit = defineEmits<{
  (event: "save", payload: MergeSavePayload): void;
  (event: "saved", payload: MergeSavePayload): void;
  (event: "abort"): void;
  (event: "error", error: unknown): void;
  (event: "update:result", content: string): void;
}>();

const { t } = useI18n();
const oursContainer = ref<HTMLElement | null>(null);
const resultContainer = ref<HTMLElement | null>(null);
const theirsContainer = ref<HTMLElement | null>(null);
const workspaceContainer = ref<HTMLElement | null>(null);
const resultContent = ref("");
const conflicts = ref<MergeConflictBlock[]>([]);
const saving = ref(false);
const saveError = ref("");
const resultBuildError = ref("");

let oursResources: DiffResources | null = null;
let resultResources: ResultResources | null = null;
let theirsResources: DiffResources | null = null;
let resultChangeListener: monaco.IDisposable | null = null;
let resizeObserver: ResizeObserver | null = null;
let resultDecorationIds: string[] = [];
let suppressResultEmit = false;
let pendingGeneratedEndConflictRaws: Set<string> | null = null;
let generatedEndConflictHints = new Map<string, string>();

const editorLanguage = computed(() => props.language?.trim() || detectLanguage(props.filePath));
const legacyMode = computed(() => props.oursContent === undefined && props.ours !== undefined);
const oursContent = computed(() => props.oursContent ?? props.ours ?? "");
const theirsContent = computed(() => props.theirsContent ?? props.theirs ?? "");
const baseContent = computed(() => props.baseContent ?? props.base ?? "");
const conflictRegionsKey = computed(() => JSON.stringify(props.conflictRegions));
const unresolvedCount = computed(() =>
  resultBuildError.value
    ? 1
    : conflicts.value.length || (hasConflictMarkers(resultContent.value) ? 1 : 0)
);
const canSave = computed(() => !saving.value && unresolvedCount.value === 0);

function recordGeneratedEndConflicts(content: string): string {
  pendingGeneratedEndConflictRaws = new Set(
    parseConflictBlocks(content)
      .filter((block) => block.endOffset === content.length)
      .map((block) => block.raw),
  );
  return content;
}

function buildResultContent(): string {
  pendingGeneratedEndConflictRaws = null;
  generatedEndConflictHints = new Map<string, string>();
  resultBuildError.value = "";
  saveError.value = "";
  try {
    if (props.initialResult !== undefined) return props.initialResult;
    if (props.conflictRegions.length > 0) {
      return recordGeneratedEndConflicts(createConflictRegionResult(
        oursContent.value,
        props.conflictRegions,
        "OURS",
        "THEIRS",
      ));
    }
    return recordGeneratedEndConflicts(createInitialThreeWayResult(
      baseContent.value,
      oursContent.value,
      theirsContent.value,
    ));
  } catch (error: unknown) {
    const message = error instanceof Error ? error.message : String(error);
    resultBuildError.value = message;
    saveError.value = message;
    emit("error", error);
    return oursContent.value;
  }
}

function editorOptions(
  editable: boolean,
  originalAriaLabel: string,
  modifiedAriaLabel: string,
): monaco.editor.IStandaloneDiffEditorConstructionOptions {
  return {
    automaticLayout: true,
    enableSplitViewResizing: true,
    fontFamily: appState.fontFamily,
    fontSize: appState.fontSize,
    glyphMargin: true,
    ignoreTrimWhitespace: false,
    lineNumbers: "on",
    minimap: { enabled: false },
    modifiedAriaLabel,
    originalAriaLabel,
    originalEditable: false,
    readOnly: !editable,
    renderOverviewRuler: true,
    renderSideBySide: false,
    scrollBeyondLastLine: false,
    theme: getMonacoThemeName(appState.accentTheme),
    wordWrap: appState.wordWrap ? "on" : "off",
  };
}

function createDiffResources(
  container: HTMLElement,
  modifiedContent: string,
  editable: boolean,
  originalAriaLabel: string,
  modifiedAriaLabel: string,
): DiffResources {
  const original = monaco.editor.createModel(baseContent.value, editorLanguage.value);
  const modified = monaco.editor.createModel(modifiedContent, editorLanguage.value);
  const editor = monaco.editor.createDiffEditor(
    container,
    editorOptions(editable, originalAriaLabel, modifiedAriaLabel),
  );
  editor.setModel({ original, modified });
  return { editor, original, modified };
}

function createResultResources(
  container: HTMLElement,
  content: string,
): ResultResources {
  if (legacyMode.value) {
    const original = monaco.editor.createModel(baseContent.value, editorLanguage.value);
    const model = monaco.editor.createModel(content, editorLanguage.value);
    const hostEditor = monaco.editor.createDiffEditor(
      container,
      editorOptions(
        true,
        t("mergeEditor.resultBaseAria"),
        t("mergeEditor.result"),
      ),
    );
    hostEditor.setModel({ original, modified: model });
    return {
      hostEditor,
      editor: hostEditor.getModifiedEditor(),
      model,
      original,
    };
  }

  const model = monaco.editor.createModel(content, editorLanguage.value);
  const editor = monaco.editor.create(container, {
    automaticLayout: true,
    ariaLabel: t("mergeEditor.result"),
    fontFamily: appState.fontFamily,
    fontSize: appState.fontSize,
    glyphMargin: true,
    lineNumbers: "on",
    minimap: { enabled: false },
    model,
    readOnly: false,
    scrollBeyondLastLine: false,
    theme: getMonacoThemeName(appState.accentTheme),
    wordWrap: appState.wordWrap ? "on" : "off",
  });
  return { hostEditor: editor, editor, model };
}

function allDiffResources(): DiffResources[] {
  return [oursResources, theirsResources].filter(
    (resources): resources is DiffResources => resources !== null,
  );
}

function allEditors(): Array<
  monaco.editor.IStandaloneDiffEditor | monaco.editor.IStandaloneCodeEditor
> {
  const editors: Array<
    monaco.editor.IStandaloneDiffEditor | monaco.editor.IStandaloneCodeEditor
  > = allDiffResources().map((resources) => resources.editor);
  if (resultResources) editors.push(resultResources.hostEditor);
  return editors;
}

function updateModel(model: monaco.editor.ITextModel, content: string): void {
  if (model.getValue() !== content) model.setValue(content);
}

function updateConflictDecorations(): void {
  if (!resultResources) return;
  const decorations: monaco.editor.IModelDeltaDecoration[] = conflicts.value.flatMap((block) => [
    {
      range: new monaco.Range(block.startLine, 1, block.endLine, 1),
      options: {
        className: "merge-editor-conflict-block",
        isWholeLine: true,
        linesDecorationsClassName: "merge-editor-conflict-gutter",
        overviewRuler: {
          color: "rgba(230, 162, 60, 0.9)",
          position: monaco.editor.OverviewRulerLane.Full,
        },
      },
    },
    ...[block.startLine, block.separatorLine, block.endLine].map((line) => ({
      range: new monaco.Range(line, 1, line, 1),
      options: {
        className: "merge-editor-conflict-marker",
        isWholeLine: true,
      },
    })),
  ]);
  resultDecorationIds = resultResources.model.deltaDecorations(
    resultDecorationIds,
    decorations,
  );
}

function refreshConflicts(content: string): void {
  const nextConflicts = stabilizeConflictBlockIds(
    parseConflictBlocks(content),
    conflicts.value,
  );
  if (pendingGeneratedEndConflictRaws !== null) {
    generatedEndConflictHints = new Map(
      nextConflicts
        .filter(
          (block) => block.endOffset === content.length
            && pendingGeneratedEndConflictRaws?.has(block.raw),
        )
        .map((block) => [block.id, block.raw]),
    );
    pendingGeneratedEndConflictRaws = null;
  } else {
    generatedEndConflictHints = new Map(
      nextConflicts
        .filter(
          (block) => block.endOffset === content.length
            && generatedEndConflictHints.get(block.id) === block.raw,
        )
        .map((block) => [block.id, block.raw]),
    );
  }
  conflicts.value = nextConflicts;
  updateConflictDecorations();
}

function handleResultContentChange(): void {
  if (!resultResources) return;
  const content = resultResources.model.getValue();
  resultContent.value = content;
  refreshConflicts(content);
  if (!suppressResultEmit) emit("update:result", content);
}

function setResultContent(content: string, notify = true): void {
  resultContent.value = content;
  if (!resultResources) {
    refreshConflicts(content);
    if (notify) emit("update:result", content);
    return;
  }

  if (resultResources.model.getValue() === content) {
    refreshConflicts(content);
    return;
  }

  suppressResultEmit = !notify;
  try {
    resultResources.model.setValue(content);
  } finally {
    suppressResultEmit = false;
  }
}

function acceptConflict(block: MergeConflictBlock, resolution: MergeResolution): void {
  const preserveSourceEOF = block.endOffset === resultContent.value.length
    && generatedEndConflictHints.get(block.id) === block.raw;
  const nextContent = resolveConflictBlock(
    resultContent.value,
    block,
    resolution,
    preserveSourceEOF
      ? {
          oursEndsWithNewline: oursContent.value.endsWith("\n"),
          theirsEndsWithNewline: theirsContent.value.endsWith("\n"),
        }
      : undefined,
  );
  setResultContent(nextContent);
}

async function editConflictManually(block: MergeConflictBlock): Promise<void> {
  if (!resultResources) return;
  resultResources.editor.revealLineInCenter(block.startLine);
  resultResources.editor.setPosition({ lineNumber: block.startLine, column: 1 });
  await nextTick();
  resultResources.editor.focus();
}

function layoutEditors(): void {
  for (const editor of allEditors()) editor.layout();
}

async function saveResult(): Promise<void> {
  if (!canSave.value) {
    const firstConflict = conflicts.value[0];
    if (firstConflict) await editConflictManually(firstConflict);
    return;
  }

  const content = resultResources?.model.getValue() ?? resultContent.value;
  const payload: MergeSavePayload = {
    filePath: props.filePath,
    content,
  };
  if (!legacyMode.value) {
    emit("save", payload);
    return;
  }

  const repoPath = props.repoPath ?? "";
  saving.value = true;
  saveError.value = "";
  try {
    const path = joinRepoFilePath(repoPath, props.filePath);
    const legacyPayload: MergeSavePayload = {
      repoPath,
      filePath: props.filePath,
      path,
      content,
    };
    if (props.saveAdapter?.saveResult) {
      await props.saveAdapter.saveResult(legacyPayload);
    } else {
      if (props.saveAdapter?.writeFile) {
        await props.saveAdapter.writeFile(path, content);
      } else {
        await fileService.writeFile(path, content);
      }
      if (props.saveAdapter?.resolveConflict) {
        await props.saveAdapter.resolveConflict(props.filePath);
      } else {
        await gitService.stage(repoPath, props.filePath);
      }
    }
    emit("save", legacyPayload);
    emit("saved", legacyPayload);
  } catch (error: unknown) {
    saveError.value = error instanceof Error ? error.message : String(error);
    emit("error", error);
  } finally {
    saving.value = false;
  }
}

function abortMerge(): void {
  emit("abort");
}

function disposeResources(resources: DiffResources | null): void {
  if (!resources) return;
  resources.editor.setModel(null);
  resources.editor.dispose();
  resources.original.dispose();
  resources.modified.dispose();
}

function disposeResultResources(resources: ResultResources | null): void {
  if (!resources) return;
  resources.hostEditor.setModel(null);
  resources.hostEditor.dispose();
  resources.original?.dispose();
  resources.model.dispose();
}

function disposeEditors(): void {
  resultChangeListener?.dispose();
  resultChangeListener = null;
  resizeObserver?.disconnect();
  resizeObserver = null;

  if (resultResources && resultDecorationIds.length > 0) {
    resultResources.model.deltaDecorations(resultDecorationIds, []);
  }
  resultDecorationIds = [];

  disposeResources(oursResources);
  disposeResultResources(resultResources);
  disposeResources(theirsResources);
  oursResources = null;
  resultResources = null;
  theirsResources = null;
}

onMounted(() => {
  if (!oursContainer.value || !resultContainer.value || !theirsContainer.value) return;

  const startingResult = buildResultContent();
  resultContent.value = startingResult;
  oursResources = createDiffResources(
    oursContainer.value,
    oursContent.value,
    false,
    t("mergeEditor.oursBaseAria"),
    t("mergeEditor.ours"),
  );
  resultResources = createResultResources(
    resultContainer.value,
    startingResult,
  );
  theirsResources = createDiffResources(
    theirsContainer.value,
    theirsContent.value,
    false,
    t("mergeEditor.theirsBaseAria"),
    t("mergeEditor.theirs"),
  );
  resultChangeListener = resultResources.model.onDidChangeContent(handleResultContentChange);
  refreshConflicts(startingResult);

  if (workspaceContainer.value && typeof ResizeObserver !== "undefined") {
    resizeObserver = new ResizeObserver(layoutEditors);
    resizeObserver.observe(workspaceContainer.value);
  }
  layoutEditors();
});

watch(
  () => [
    baseContent.value,
    oursContent.value,
    theirsContent.value,
    props.initialResult,
    conflictRegionsKey.value,
    props.filePath,
    props.repoPath,
  ] as const,
  () => {
    if (!oursResources || !resultResources || !theirsResources) return;
    updateModel(oursResources.original, baseContent.value);
    updateModel(theirsResources.original, baseContent.value);
    if (resultResources.original) updateModel(resultResources.original, baseContent.value);
    updateModel(oursResources.modified, oursContent.value);
    updateModel(theirsResources.modified, theirsContent.value);
    setResultContent(buildResultContent(), false);
  },
);

watch(editorLanguage, (language) => {
  for (const resources of allDiffResources()) {
    monaco.editor.setModelLanguage(resources.original, language);
    monaco.editor.setModelLanguage(resources.modified, language);
  }
  if (resultResources) {
    if (resultResources.original) {
      monaco.editor.setModelLanguage(resultResources.original, language);
    }
    monaco.editor.setModelLanguage(resultResources.model, language);
  }
});

watch(
  () => [appState.fontFamily, appState.fontSize, appState.wordWrap] as const,
  ([fontFamily, fontSize, wordWrap]) => {
    for (const editor of allEditors()) {
      editor.updateOptions({
        fontFamily,
        fontSize,
        wordWrap: wordWrap ? "on" : "off",
      });
    }
  },
);

onBeforeUnmount(disposeEditors);

defineExpose({
  acceptConflict,
  editConflictManually,
  getResult: () => resultContent.value,
  saveResult,
  abortMerge,
});
</script>

<template>
  <section class="merge-editor" :aria-label="t('mergeEditor.ariaLabel')">
    <header class="merge-editor__header">
      <div class="merge-editor__identity">
        <strong class="merge-editor__title">{{ t("mergeEditor.title") }}</strong>
        <span class="merge-editor__path">{{ filePath }}</span>
      </div>
      <div class="merge-editor__summary" aria-live="polite">
        <span v-if="unresolvedCount > 0" class="merge-editor__unresolved">
          {{ t("mergeEditor.unresolvedCount", { count: unresolvedCount }) }}
        </span>
        <span v-else class="merge-editor__resolved">
          <el-icon aria-hidden="true"><Check /></el-icon>
          {{ t("mergeEditor.allResolved") }}
        </span>
        <el-button
          type="primary"
          :icon="DocumentChecked"
          :disabled="!canSave"
          :aria-label="t('mergeEditor.saveResultAria')"
          @click="saveResult"
        >
          {{ t("mergeEditor.saveResult") }}
        </el-button>
        <el-button
          :icon="Close"
          :aria-label="t('mergeEditor.abortAria')"
          @click="abortMerge"
        >
          {{ t("mergeEditor.abort") }}
        </el-button>
      </div>
    </header>

    <div v-if="conflicts.length > 0" class="merge-editor__conflicts" role="list">
      <div
        v-for="block in conflicts"
        :key="block.id"
        class="merge-editor__conflict"
        role="listitem"
      >
        <span class="merge-editor__conflict-label">
          {{ t("mergeEditor.conflictAtLine", { line: block.startLine }) }}
        </span>
        <div class="merge-editor__conflict-actions">
          <el-button
            size="small"
            :icon="Check"
            :aria-label="t('mergeEditor.acceptOursAria', { line: block.startLine })"
            @click="acceptConflict(block, 'ours')"
          >
            {{ t("mergeEditor.acceptOurs") }}
          </el-button>
          <el-button
            size="small"
            :icon="Right"
            :aria-label="t('mergeEditor.acceptTheirsAria', { line: block.startLine })"
            @click="acceptConflict(block, 'theirs')"
          >
            {{ t("mergeEditor.acceptTheirs") }}
          </el-button>
          <el-button
            size="small"
            :icon="Right"
            :aria-label="t('mergeEditor.acceptIncomingAria', { line: block.startLine })"
            @click="acceptConflict(block, 'theirs')"
          >
            {{ t("mergeEditor.acceptIncoming") }}
          </el-button>
          <el-button
            size="small"
            :icon="CopyDocument"
            :aria-label="t('mergeEditor.acceptBothAria', { line: block.startLine })"
            @click="acceptConflict(block, 'both')"
          >
            {{ t("mergeEditor.acceptBoth") }}
          </el-button>
          <el-button
            size="small"
            :icon="EditPen"
            :aria-label="t('mergeEditor.manualAria', { line: block.startLine })"
            @click="editConflictManually(block)"
          >
            {{ t("mergeEditor.manual") }}
          </el-button>
        </div>
      </div>
    </div>

    <div ref="workspaceContainer" class="merge-editor__workspace">
      <article class="merge-editor__column">
        <div class="merge-editor__column-header">
          <strong>{{ t("mergeEditor.ours") }}</strong>
          <span>{{ t("mergeEditor.comparedWithBase") }}</span>
        </div>
        <div ref="oursContainer" class="merge-editor__monaco" />
      </article>

      <article class="merge-editor__column merge-editor__column--result">
        <div class="merge-editor__column-header">
          <strong>{{ t("mergeEditor.result") }}</strong>
        </div>
        <div ref="resultContainer" class="merge-editor__monaco" />
      </article>

      <article class="merge-editor__column">
        <div class="merge-editor__column-header">
          <strong>{{ t("mergeEditor.theirs") }}</strong>
          <span>{{ t("mergeEditor.comparedWithBase") }}</span>
        </div>
        <div ref="theirsContainer" class="merge-editor__monaco" />
      </article>
    </div>

    <footer v-if="saveError" class="merge-editor__error" role="alert">
      {{ t("mergeEditor.saveFailed", { error: saveError }) }}
    </footer>
  </section>
</template>

<style scoped>
.merge-editor {
  display: flex;
  flex-direction: column;
  width: 100%;
  height: 100%;
  min-height: 420px;
  overflow: hidden;
  color: var(--color-text-primary, var(--el-text-color-primary));
  background: var(--color-bg-base, var(--el-bg-color));
}

.merge-editor__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  min-height: 48px;
  padding: 6px 12px;
  border-bottom: 1px solid var(--color-border-subtle, var(--el-border-color));
  background: var(--color-bg-surface, var(--el-bg-color));
}

.merge-editor__identity,
.merge-editor__summary,
.merge-editor__resolved,
.merge-editor__conflict,
.merge-editor__conflict-actions,
.merge-editor__column-header {
  display: flex;
  align-items: center;
}

.merge-editor__identity {
  min-width: 0;
  gap: 10px;
}

.merge-editor__title {
  flex: 0 0 auto;
  font-size: 13px;
}

.merge-editor__path {
  overflow: hidden;
  color: var(--color-text-secondary, var(--el-text-color-secondary));
  font-family: var(--font-mono, monospace);
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.merge-editor__summary {
  flex: 0 0 auto;
  gap: 10px;
}

.merge-editor__resolved {
  gap: 4px;
  color: var(--el-color-success);
  font-size: 12px;
}

.merge-editor__unresolved {
  color: var(--el-color-warning);
  font-size: 12px;
}

.merge-editor__conflicts {
  flex: 0 0 auto;
  max-height: 150px;
  overflow: auto;
  border-bottom: 1px solid var(--color-border-subtle, var(--el-border-color));
  background: var(--el-color-warning-light-9);
}

.merge-editor__conflict {
  justify-content: space-between;
  gap: 12px;
  min-height: 38px;
  padding: 4px 12px;
  border-bottom: 1px solid var(--el-color-warning-light-7);
}

.merge-editor__conflict:last-child {
  border-bottom: 0;
}

.merge-editor__conflict-label {
  flex: 0 0 auto;
  color: var(--el-color-warning-dark-2);
  font-family: var(--font-mono, monospace);
  font-size: 12px;
}

.merge-editor__conflict-actions {
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 4px;
}

.merge-editor__conflict-actions :deep(.el-button + .el-button) {
  margin-left: 0;
}

.merge-editor__workspace {
  display: grid;
  flex: 1 1 auto;
  grid-template-columns: repeat(3, minmax(280px, 1fr));
  min-height: 0;
  overflow-x: auto;
}

.merge-editor__column {
  display: flex;
  flex-direction: column;
  min-width: 280px;
  min-height: 0;
  border-right: 1px solid var(--color-border-subtle, var(--el-border-color));
}

.merge-editor__column:last-child {
  border-right: 0;
}

.merge-editor__column--result {
  background: var(--el-color-primary-light-9);
}

.merge-editor__column-header {
  flex: 0 0 34px;
  justify-content: space-between;
  gap: 8px;
  min-width: 0;
  padding: 0 10px;
  border-bottom: 1px solid var(--color-border-subtle, var(--el-border-color));
  background: var(--color-bg-surface, var(--el-bg-color));
}

.merge-editor__column-header strong {
  font-size: 12px;
}

.merge-editor__column-header span {
  overflow: hidden;
  color: var(--color-text-tertiary, var(--el-text-color-secondary));
  font-size: 11px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.merge-editor__monaco {
  flex: 1 1 auto;
  min-height: 0;
}

.merge-editor__error {
  flex: 0 0 auto;
  padding: 6px 12px;
  border-top: 1px solid var(--el-color-danger-light-5);
  color: var(--el-color-danger);
  background: var(--el-color-danger-light-9);
  font-size: 12px;
}

:deep(.merge-editor-conflict-block) {
  background: rgba(230, 162, 60, 0.08);
}

:deep(.merge-editor-conflict-marker) {
  background: rgba(230, 162, 60, 0.24);
  font-weight: 600;
}

:deep(.merge-editor-conflict-gutter) {
  border-left: 3px solid var(--el-color-warning);
  margin-left: 3px;
}

@media (max-width: 900px) {
  .merge-editor__header,
  .merge-editor__conflict {
    align-items: flex-start;
    flex-direction: column;
  }

  .merge-editor__summary,
  .merge-editor__conflict-actions {
    width: 100%;
    justify-content: space-between;
  }
}
</style>
