<script setup lang="ts">
// Koyori IDE 组件 · Breadcrumb Bar。
// 喵，这是 Breadcrumb Bar，负责 Koyori IDE 的界面呈现喵~
import { computed, shallowRef, watch } from "vue";
import { ArrowRight } from "@element-plus/icons-vue";
import { appState } from "@/stores/app";
import { activeFile, editorState } from "@/stores/editor";
import { getLSPDocumentSymbols } from "@/stores/lsp";
import type { LSPDocumentSymbol, LSPPosition, LSPRange } from "@/types";
import { useI18n } from "@/lib/i18n";

const { t } = useI18n();

interface PathSegment {
  label: string;
  fullPath: string;
}

const documentSymbols = shallowRef<LSPDocumentSymbol[]>([]);
let symbolRequest = 0;

function normalizedPath(path: string): string {
  return path.replace(/\\/g, "/").replace(/\/$/, "");
}

const pathSegments = computed<PathSegment[]>(() => {
  const filePath = activeFile.value?.path;
  if (!filePath) return [];

  const normalizedFile = normalizedPath(filePath);
  const normalizedRoot = normalizedPath(appState.currentProject ?? "");
  const rootPrefix = normalizedRoot ? `${normalizedRoot}/` : "";
  const isInWorkspace =
    normalizedRoot.length > 0 &&
    normalizedFile.toLocaleLowerCase().startsWith(rootPrefix.toLocaleLowerCase());
  const relativePath = isInWorkspace
    ? normalizedFile.slice(rootPrefix.length)
    : normalizedFile.split("/").pop() ?? normalizedFile;
  const labels = relativePath.split("/").filter(Boolean);

  return labels.map((label, index) => ({
    label,
    fullPath: isInWorkspace
      ? `${normalizedRoot}/${labels.slice(0, index + 1).join("/")}`
      : normalizedFile,
  }));
});

function comparePosition(left: LSPPosition, right: LSPPosition): number {
  if (left.line !== right.line) return left.line - right.line;
  return left.character - right.character;
}

function rangeContains(range: LSPRange, position: LSPPosition): boolean {
  return comparePosition(range.start, position) <= 0 && comparePosition(position, range.end) <= 0;
}

function deepestSymbolChain(
  symbols: LSPDocumentSymbol[],
  position: LSPPosition,
): LSPDocumentSymbol[] {
  let best: LSPDocumentSymbol[] = [];
  for (const symbol of symbols) {
    if (!rangeContains(symbol.range, position)) continue;
    const chain = [symbol, ...deepestSymbolChain(symbol.children ?? [], position)];
    if (chain.length > best.length) best = chain;
  }
  return best;
}

const symbolSegments = computed(() => deepestSymbolChain(documentSymbols.value, {
  line: Math.max(0, appState.cursorLine - 1),
  character: Math.max(0, appState.cursorColumn - 1),
}));

watch(
  () => [
    activeFile.value?.path ?? "",
    activeFile.value?.language ?? "",
    activeFile.value?.content ?? "",
  ] as const,
  async ([path, language, content]) => {
    const request = ++symbolRequest;
    documentSymbols.value = [];
    if (!path || !language) return;
    const symbols = await getLSPDocumentSymbols(language, path, content);
    if (request !== symbolRequest || activeFile.value?.path !== path) return;
    documentSymbols.value = Array.isArray(symbols) ? symbols : [];
  },
  { immediate: true },
);

function focusCurrentFile(): void {
  const path = activeFile.value?.path;
  if (!path) return;
  editorState.activeFilePath = path;
}

function jumpToSymbol(symbol: LSPDocumentSymbol): void {
  appState.cursorLine = symbol.selectionRange.start.line + 1;
  appState.cursorColumn = symbol.selectionRange.start.character + 1;
  appState.editorJumpSeq += 1;
}
</script>

<template>
  <nav
    v-if="appState.breadcrumbVisible && activeFile"
    class="breadcrumb-bar"
    :aria-label="t('a11y.breadcrumb')"
  >
    <div class="breadcrumb-bar__scroll">
      <template v-for="(segment, index) in pathSegments" :key="segment.fullPath">
        <el-icon v-if="index > 0" class="breadcrumb-bar__separator" aria-hidden="true">
          <ArrowRight />
        </el-icon>
        <button
          type="button"
          class="breadcrumb-bar__segment"
          :class="{ 'breadcrumb-bar__segment--file': index === pathSegments.length - 1 }"
          :title="segment.fullPath"
          :data-path="segment.fullPath"
          @click="focusCurrentFile"
        >
          {{ segment.label }}
        </button>
      </template>

      <template v-for="symbol in symbolSegments" :key="`${symbol.name}:${symbol.range.start.line}:${symbol.range.start.character}`">
        <el-icon class="breadcrumb-bar__separator" aria-hidden="true">
          <ArrowRight />
        </el-icon>
        <button
          type="button"
          class="breadcrumb-bar__segment breadcrumb-bar__segment--symbol"
          :title="symbol.detail || symbol.name"
          :data-symbol="symbol.name"
          @click="jumpToSymbol(symbol)"
        >
          {{ symbol.name }}
        </button>
      </template>
    </div>
  </nav>
</template>

<style scoped>
.breadcrumb-bar {
  position: relative;
  z-index: 1;
  flex: 0 0 28px;
  min-width: 0;
  height: 28px;
  border-bottom: 1px solid var(--color-border-subtle);
  background: var(--color-bg-surface-dim);
}

.breadcrumb-bar__scroll {
  display: flex;
  align-items: center;
  min-width: 0;
  height: 100%;
  padding: 0 10px;
  overflow-x: auto;
  overflow-y: hidden;
  white-space: nowrap;
  scrollbar-width: thin;
}

.breadcrumb-bar__separator {
  flex: 0 0 auto;
  width: 13px;
  height: 13px;
  margin: 0 1px;
  color: var(--color-text-disabled);
}

.breadcrumb-bar__segment {
  flex: 0 0 auto;
  max-width: 240px;
  height: 24px;
  padding: 0 4px;
  overflow: hidden;
  border: 0;
  background: transparent;
  color: var(--color-text-tertiary);
  font-family: var(--font-sans);
  font-size: 12px;
  line-height: 24px;
  text-overflow: ellipsis;
  white-space: nowrap;
  cursor: pointer;
}

.breadcrumb-bar__segment:hover,
.breadcrumb-bar__segment:focus-visible {
  border-radius: var(--radius-xs);
  outline: none;
  background: var(--color-bg-surface-container-low);
  color: var(--color-text-primary);
}

.breadcrumb-bar__segment--file {
  color: var(--color-text-secondary);
}

.breadcrumb-bar__segment--symbol {
  color: var(--color-text-primary);
}
</style>
