<script setup lang="ts">
// Koyori IDE 组件 · Diff Viewer。
// 喵，这是 Diff Viewer，负责 Koyori IDE 的界面呈现喵~
// Plan 11 Task 13 — DiffViewer.vue
// Step 5: 多文件 tab + 统计概览 + hunk 折叠 + 行号 + 语法高亮
// Step 8: AI 审查模式（自动生成 hunk 审查意见，severity 色标）
// Step 11: Artifact 预览模式（iframe sandbox="allow-scripts"）
import { computed, onBeforeUnmount, ref, shallowRef } from "vue";
import hljs from "highlight.js/lib/common";
import {
  diffState,
  setActiveFile,
  toggleHunk,
  setAIReviewMode,
  setArtifactPreview,
  applyFile,
  applyAll,
  rejectFile,
  rejectHunk,
  type DiffApplyOutcome,
} from "@/stores/diff";
import { notifySuccess, notifyWarning } from "@/lib/notifications";
import { useI18n } from "@/lib/i18n";
import { detectLanguage } from "@/lib/language";
import { sanitizeHtml, buildArtifactSrcDoc, putLru } from "@/lib/markdown";
import MarkdownContent from "@/components/common/MarkdownContent.vue";
import type { AICommentSeverity, DiffLine, FileDiff } from "@/types";

const { t } = useI18n();

const emit = defineEmits<{
  (e: "export", format: "markdown" | "unified" | "html"): void;
}>();

// ---- Step 5: 多文件 tab ----

const activeFile = computed<FileDiff | null>(() => {
  if (!diffState.diff || diffState.activeFileIdx >= diffState.diff.files.length) return null;
  return diffState.diff.files[diffState.activeFileIdx];
});

const stats = computed(() => {
  if (!diffState.diff) return { added: 0, removed: 0, files: 0 };
  return {
    added: diffState.diff.totalAdded,
    removed: diffState.diff.totalRemoved,
    files: diffState.diff.files.length,
  };
});

function selectFile(idx: number): void {
  setActiveFile(idx);
}

// ---- Step 5: hunk 折叠 ----

function isHunkCollapsed(fileIdx: number, hunkIdx: number): boolean {
  return diffState.collapsedHunks.has(`${fileIdx}-${hunkIdx}`);
}

// ---- Step 5: 行号 + 语法高亮 ----

function lineClass(line: DiffLine): string {
  switch (line.type) {
    case "added":
      return "diff-line--added";
    case "removed":
      return "diff-line--removed";
    case "conflict":
      return "diff-line--conflict";
    default:
      return "diff-line--context";
  }
}

function linePrefix(line: DiffLine): string {
  switch (line.type) {
    case "added":
      return "+";
    case "removed":
      return "-";
    case "conflict":
      return "!";
    default:
      return " ";
  }
}

// M-23: 行级高亮结果 LRU 缓存。diff 每次重渲染都会对每行调用 highlightLine，
// 超过 1000 行时重复执行 hljs.highlight + DOMPurify 会明显卡顿。缓存按
// `${filePath}\0${content}` 键命中已高亮结果，复用 markdown.ts 的 putLru
// LRU 驱逐策略（容量 MARKDOWN_CACHE_LIMIT=100）。
const highlightLineCache = shallowRef(new Map<string, string>());
const commentIdCache = new WeakMap<object, string>();
const lineIdCache = new WeakMap<object, string>();
let commentIdSequence = 0;
let lineIdSequence = 0;

type KeyedComment = { id?: string };

function commentKey(comment: object): string {
  const keyedComment = comment as KeyedComment;
  const existing = keyedComment.id ?? commentIdCache.get(comment);
  if (existing) return existing;

  const id = `diff-comment-${++commentIdSequence}`;
  commentIdCache.set(comment, id);
  return id;
}

function lineKey(line: DiffLine): string {
  const existing = lineIdCache.get(line);
  if (existing) return existing;

  const id = `diff-line-${++lineIdSequence}`;
  lineIdCache.set(line, id);
  return id;
}

function highlightLine(content: string, filePath: string): string {
  const cacheKey = `${filePath}\u0000${content}`;
  const cached = highlightLineCache.value.get(cacheKey);
  if (cached !== undefined) {
    // LRU 刷新：删除后重新插入，使其成为最近使用项，避免被优先驱逐。
    highlightLineCache.value.delete(cacheKey);
    highlightLineCache.value.set(cacheKey, cached);
    return cached;
  }
  const lang = detectLanguage(filePath);
  let html: string;
  try {
    if (lang && hljs.getLanguage(lang)) {
      html = hljs.highlight(content, { language: lang }).value;
    } else {
      html = hljs.highlightAuto(content).value;
    }
    // G-SEC-11: 经 DOMPurify 净化后再渲染（highlight.js 输出仅含 <span>，净化是纵深防御）。
    html = sanitizeHtml(html);
  } catch {
    html = escapeHtml(content);
  }
  putLru(highlightLineCache.value, cacheKey, html);
  return html;
}

function escapeHtml(s: string): string {
  return s
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

// ---- Step 8: AI 审查模式（severity 色标）----

function severityClass(sev: AICommentSeverity): string {
  return `ai-comment--${sev}`;
}

function severityIcon(sev: AICommentSeverity): string {
  switch (sev) {
    case "critical":
      return "🔴";
    case "error":
      return "❌";
    case "warning":
      return "⚠️";
    default:
      return "ℹ️";
  }
}

function toggleAIReview(): void {
  setAIReviewMode(!diffState.aiReviewMode);
}

// ---- Step 11: Artifact 预览模式（iframe sandbox）----

function toggleArtifactPreview(): void {
  setArtifactPreview(!diffState.artifactPreview);
}

const artifactSrcDoc = computed(() => {
  if (!activeFile.value) return "";
  // Artifact 预览仅对 HTML 类文件有意义；其他文件显示源码。
  const path = activeFile.value.path;
  if (!/\.(html?|svg)$/i.test(path)) return "";
  // H-17: 用 CSP + SVG <script> 剥离构建沙箱化的 srcdoc，
  // 配合 iframe sandbox="allow-scripts" 阻止 AI 生成内容发起外部请求。
  const isSvg = /\.svg$/i.test(path);
  return buildArtifactSrcDoc(activeFile.value.newContent, isSvg);
});

const isArtifactable = computed(() => {
  if (!activeFile.value) return false;
  return /\.(html?|svg)$/i.test(activeFile.value.path);
});

// ---- Step 6-7: Apply / Reject ----

const applyResult = ref<DiffApplyOutcome | null>(null);
const applying = ref(false);
const lastApplyAction = ref<"file" | "all" | null>(null);

function consumeApplyResult(result: DiffApplyOutcome | null): void {
  if (!result) return;
  if (result.status === "applied") {
    applyResult.value = null;
    notifySuccess(t("diffViewer.applySuccess", { count: result.appliedFiles.length }));
    return;
  }
  applyResult.value = result;
  if (result.status === "committed-ui-sync-failed") {
    // G18: the disk transaction committed; only the in-memory editor sync
    // failed. Tell the user the change IS on disk and offer a safe reload —
    // never a re-apply.
    notifyWarning(t("diffViewer.applyCommittedSyncFailed"));
    return;
  }
  notifyWarning(t(result.status === "conflict"
    ? "diffViewer.applyConflict"
    : "diffViewer.applyFailed"));
}

async function handleApplyFile(): Promise<void> {
  if (applying.value) return;
  applying.value = true;
  lastApplyAction.value = "file";
  try {
    consumeApplyResult(await applyFile(diffState.activeFileIdx));
  } finally {
    applying.value = false;
  }
}

async function handleApplyAll(): Promise<void> {
  if (applying.value) return;
  applying.value = true;
  lastApplyAction.value = "all";
  try {
    consumeApplyResult(await applyAll());
  } finally {
    applying.value = false;
  }
}

async function retryApply(): Promise<void> {
  if (lastApplyAction.value === "all") await handleApplyAll();
  else await handleApplyFile();
}

function dismissApplyResult(): void {
  applyResult.value = null;
}

async function handleRejectFile(): Promise<void> {
  await rejectFile(diffState.activeFileIdx);
}

async function handleRejectHunk(hunkIdx: number): Promise<void> {
  await rejectHunk(diffState.activeFileIdx, hunkIdx);
}

function handleExport(format: "markdown" | "unified" | "html"): void {
  emit("export", format);
}

// 导出菜单可见性
const exportMenuVisible = ref(false);

onBeforeUnmount(() => {
  highlightLineCache.value.clear();
});
</script>

<template>
  <div class="diff-viewer">
    <!-- Step 5: 统计概览 -->
    <div class="diff-viewer__stats">
      <span class="diff-stat diff-stat--added">+{{ stats.added }}</span>
      <span class="diff-stat diff-stat--removed">−{{ stats.removed }}</span>
      <span class="diff-stat diff-stat--files">{{ stats.files }} {{ t("diffViewer.files") }}</span>
    </div>

    <!-- Step 5: 多文件 tab -->
    <div v-if="diffState.diff && diffState.diff.files.length > 1" class="diff-viewer__tabs">
      <button
        v-for="(file, idx) in diffState.diff.files"
        :key="file.path"
        :class="['diff-tab', { 'diff-tab--active': idx === diffState.activeFileIdx }]"
        @click="selectFile(idx)"
      >
        <span class="diff-tab__name">{{ file.path }}</span>
        <span class="diff-tab__count">
          <span class="diff-tab__added">+{{ file.addedLines }}</span>
          <span class="diff-tab__removed">−{{ file.removedLines }}</span>
        </span>
      </button>
    </div>

    <!-- 工具栏 -->
    <div class="diff-viewer__toolbar" :aria-busy="applying">
      <label class="diff-toolbar__toggle">
        <input type="checkbox" :checked="diffState.aiReviewMode" @change="toggleAIReview" />
        <span>{{ t("diffViewer.aiReviewMode") }}</span>
      </label>
      <label v-if="isArtifactable" class="diff-toolbar__toggle">
        <input type="checkbox" :checked="diffState.artifactPreview" @change="toggleArtifactPreview" />
        <span>{{ t("diffViewer.artifactPreview") }}</span>
      </label>
      <div class="diff-toolbar__spacer" />
      <button class="diff-toolbar__btn" :disabled="!activeFile || applying" @click="handleApplyFile">
        {{ t("diffViewer.applyFile") }}
      </button>
      <button class="diff-toolbar__btn" :disabled="!activeFile" @click="handleRejectFile">
        {{ t("diffViewer.rejectFile") }}
      </button>
      <button class="diff-toolbar__btn diff-toolbar__btn--primary" :disabled="applying" @click="handleApplyAll">
        {{ t("diffViewer.applyAll") }}
      </button>
      <div class="diff-toolbar__export">
        <button class="diff-toolbar__btn" @click="exportMenuVisible = !exportMenuVisible">
          {{ t("diffViewer.export") }} ▾
        </button>
        <div v-if="exportMenuVisible" class="diff-export__menu">
          <button @click="handleExport('markdown'); exportMenuVisible = false">Markdown</button>
          <button @click="handleExport('unified'); exportMenuVisible = false">Unified Diff</button>
          <button @click="handleExport('html'); exportMenuVisible = false">HTML</button>
        </div>
      </div>
    </div>

    <div v-if="applyResult" class="diff-viewer__apply-result" role="alert">
      <strong>{{
  t(applyResult.status === "committed-ui-sync-failed"
    ? "diffViewer.applyCommittedSyncFailed"
    : applyResult.status === "conflict"
      ? "diffViewer.applyConflict"
      : "diffViewer.applyFailed")
}}</strong>
      <p v-if="applyResult.failureReason">{{ applyResult.failureReason }}</p>
      <ul v-if="applyResult.conflicts.length">
        <li v-for="conflict in applyResult.conflicts" :key="conflict">{{ conflict }}</li>
      </ul>
      <p v-if="applyResult.status === 'committed-ui-sync-failed'">
    {{ t("diffViewer.applyReloadHint") }}
  </p>
  <p v-if="applyResult.rollbackAttempted">
        {{ t(applyResult.rolledBack ? "diffViewer.rollbackComplete" : "diffViewer.rollbackIncomplete") }}
      </p>
      <div class="diff-apply__actions">
        <button type="button" class="diff-toolbar__btn diff-apply__retry" :disabled="applying" @click="retryApply">
          {{ t("common.retry") }}
        </button>
        <button type="button" class="diff-toolbar__btn diff-apply__dismiss" :disabled="applying" @click="dismissApplyResult">
          {{ t("diffViewer.dismiss") }}
        </button>
      </div>
    </div>

    <!-- Step 11: Artifact 预览模式 -->
    <div v-if="diffState.artifactPreview && isArtifactable && artifactSrcDoc" class="diff-viewer__artifact">
      <!--
        H-17 / G-SEC-11: iframe 沙箱限制 ——
          sandbox="allow-scripts" 仅允许脚本执行，明确不包含：
            - allow-same-origin  → 脚本运行在 null origin，无法访问父窗口/同源存储
            - allow-popups       → 阻止 window.open() 弹窗
            - allow-forms        → 阻止表单提交
            - allow-top-navigation → 阻止iframe导航父窗口
          配合 srcdoc 中的 CSP（connect-src 'none'）阻止 fetch/XHR 数据外泄。
      -->
      <iframe
        :srcdoc="artifactSrcDoc"
        sandbox="allow-scripts"
        class="diff-artifact__iframe"
        :title="t('diffViewer.artifactTitle')"
      />
    </div>

    <!-- Step 5: diff 内容 -->
    <div v-else-if="activeFile" class="diff-viewer__content">
      <div
        v-for="(hunk, hunkIdx) in activeFile.hunks"
        :key="hunk.oldStart + '-' + hunk.newStart"
        class="diff-hunk"
      >
        <!-- hunk 头（可折叠） -->
        <div class="diff-hunk__header">
          <button
            type="button"
            class="diff-hunk__collapse"
            :aria-expanded="!isHunkCollapsed(diffState.activeFileIdx, hunkIdx)"
            @click="toggleHunk(diffState.activeFileIdx, hunkIdx)"
          >
            <span class="diff-hunk__toggle" aria-hidden="true">{{ isHunkCollapsed(diffState.activeFileIdx, hunkIdx) ? "▶" : "▼" }}</span>
            <span class="diff-hunk__range">@@ -{{ hunk.oldStart }},{{ hunk.oldCount }} +{{ hunk.newStart }},{{ hunk.newCount }} @@</span>
          </button>
          <span class="diff-hunk__actions">
            <button
              type="button"
              class="diff-hunk__reject"
              :title="t('diffViewer.rejectHunk')"
              :aria-label="t('diffViewer.rejectHunk')"
              @click="handleRejectHunk(hunkIdx)"
            >✕</button>
          </span>
        </div>

        <!-- hunk 行 -->
        <div v-show="!isHunkCollapsed(diffState.activeFileIdx, hunkIdx)" class="diff-hunk__body">
          <table class="diff-table">
            <tbody>
              <tr
                v-for="line in hunk.lines"
                :key="lineKey(line)"
                :class="lineClass(line)"
              >
                <td class="diff-table__num diff-table__num--old">{{ line.oldNum || "" }}</td>
                <td class="diff-table__num diff-table__num--new">{{ line.newNum || "" }}</td>
                <td class="diff-table__prefix">{{ linePrefix(line) }}</td>
                <td class="diff-table__content">
                  <MarkdownContent :html="highlightLine(line.content, activeFile.path)" class="diff-table__code" />
                </td>
              </tr>
            </tbody>
          </table>

          <!-- Step 8: AI 审查意见（severity 色标） -->
          <div v-if="diffState.aiReviewMode && hunk.aiComments && hunk.aiComments.length" class="diff-hunk__ai-comments">
            <div
              v-for="comment in hunk.aiComments"
              :key="commentKey(comment)"
              :data-comment-id="commentKey(comment)"
              :class="['ai-comment', severityClass(comment.severity)]"
            >
              <span class="ai-comment__icon">{{ severityIcon(comment.severity) }}</span>
              <span class="ai-comment__severity">{{ comment.severity }}</span>
              <span class="ai-comment__message">{{ comment.message }}</span>
              <span v-if="comment.suggestion" class="ai-comment__suggestion">
                {{ t("diffViewer.suggestion") }}: {{ comment.suggestion }}
              </span>
            </div>
          </div>

          <!-- Step 4: 行内评论 -->
          <template
            v-for="line in hunk.lines"
            :key="'comments-' + lineKey(line)"
          >
            <div v-if="line.comments && line.comments.length" class="diff-line__comments">
              <div
                v-for="c in line.comments"
                :key="commentKey(c)"
                :data-comment-id="commentKey(c)"
                class="inline-comment"
              >
                <span class="inline-comment__author">{{ c.author }}</span>
                <span class="inline-comment__body">{{ c.body }}</span>
              </div>
            </div>
          </template>
        </div>
      </div>
    </div>

    <!-- 空状态 -->
    <div v-else class="diff-viewer__empty">
      {{ t("diffViewer.empty") }}
    </div>
  </div>
</template>

<style scoped>
.diff-viewer {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-width: 0;
  overflow: hidden;
  font-family: var(--koyori-ide-font-mono, "Cascadia Code", "Fira Code", monospace);
  font-size: 13px;
}

/* Step 5: 统计概览 */
.diff-viewer__stats {
  display: flex;
  gap: 12px;
  padding: 6px 12px;
  background: var(--color-bg-surface-container);
  border-bottom: 1px solid var(--color-border-default);
}
.diff-stat--added { color: var(--color-success); font-weight: 600; }
.diff-stat--removed { color: var(--color-error); font-weight: 600; }
.diff-stat--files { color: var(--color-text-tertiary); }

/* Step 5: 多文件 tab */
.diff-viewer__tabs {
  display: flex;
  gap: 2px;
  padding: 0 8px;
  background: var(--color-bg-surface);
  border-bottom: 1px solid var(--color-border-default);
  overflow-x: auto;
}
.diff-tab {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 6px 12px;
  border: none;
  background: transparent;
  cursor: pointer;
  border-bottom: 2px solid transparent;
  white-space: nowrap;
  color: var(--color-text-secondary);
  transition: color var(--transition-fast);
}
.diff-tab:hover { color: var(--color-text-primary); }
.diff-tab--active {
  border-bottom-color: var(--color-primary);
  color: var(--color-primary);
}
.diff-tab__name { font-size: 12px; }
.diff-tab__count { display: flex; gap: 4px; font-size: 11px; }
.diff-tab__added { color: var(--color-success); }
.diff-tab__removed { color: var(--color-error); }

/* 工具栏 */
.diff-viewer__toolbar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  padding: 6px 12px;
  border-bottom: 1px solid var(--color-border-default);
}
.diff-toolbar__toggle {
  display: flex;
  align-items: center;
  gap: 4px;
  min-width: 0;
  font-size: 12px;
  cursor: pointer;
  color: var(--color-text-secondary);
}
.diff-toolbar__toggle span { overflow-wrap: anywhere; }
.diff-toolbar__spacer { flex: 1 1 24px; min-width: 0; }
.diff-toolbar__btn {
  max-width: 100%;
  padding: 4px 10px;
  font-size: 12px;
  border: 1px solid var(--color-border-default);
  background: var(--color-bg-surface);
  border-radius: 3px;
  cursor: pointer;
  color: var(--color-text-secondary);
  transition: border-color var(--transition-fast);
}
.diff-toolbar__btn:hover { border-color: var(--color-primary); }
.diff-toolbar__btn:disabled { opacity: 0.5; cursor: not-allowed; }
.diff-toolbar__btn--primary {
  background: var(--color-primary);
  color: var(--color-on-primary);
  border-color: var(--color-primary);
}
.diff-toolbar__export { position: relative; }
.diff-viewer__apply-result {
  box-sizing: border-box;
  width: 100%;
  min-width: 0;
  padding: 8px 12px;
  border-bottom: 1px solid var(--color-warning);
  background: var(--color-warning-container);
  color: var(--color-text-primary);
  overflow-wrap: anywhere;
}
.diff-viewer__apply-result p { margin: 4px 0; }
.diff-viewer__apply-result ul { margin: 4px 0; padding-left: 20px; }
.diff-apply__actions { display: flex; flex-wrap: wrap; gap: 8px; margin-top: 8px; }
.diff-export__menu {
  position: absolute;
  right: 0;
  top: 100%;
  display: flex;
  flex-direction: column;
  background: var(--color-bg-surface);
  border: 1px solid var(--color-border-default);
  border-radius: 3px;
  z-index: 10;
  min-width: 120px;
}
.diff-export__menu button {
  padding: 6px 12px;
  border: none;
  background: transparent;
  text-align: left;
  cursor: pointer;
  font-size: 12px;
  color: var(--color-text-primary);
  transition: background var(--transition-fast);
}
.diff-export__menu button:hover { background: var(--chrome-hover-bg); }

/* Step 11: Artifact 预览 */
.diff-viewer__artifact { flex: 1; overflow: hidden; }
.diff-artifact__iframe { width: 100%; height: 100%; border: none; }

/* diff 内容 */
.diff-viewer__content { flex: 1; overflow: auto; }
.diff-hunk { border-bottom: 1px solid var(--color-border-subtle); }
.diff-hunk__header {
  display: flex;
  align-items: center;
  width: 100%;
  background: var(--color-bg-surface-container);
  font-family: inherit;
  font-size: 12px;
  color: var(--color-text-tertiary);
}
.diff-hunk__collapse {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: 1;
  min-width: 0;
  padding: 4px 12px;
  border: none;
  background: transparent;
  color: inherit;
  cursor: pointer;
  text-align: left;
  font: inherit;
}
.diff-hunk__toggle { width: 12px; }
.diff-hunk__range { color: var(--color-primary); }
.diff-hunk__actions { display: flex; padding-right: 8px; }
.diff-hunk__reject {
  border: none;
  background: transparent;
  color: var(--color-error);
  cursor: pointer;
  font-size: 12px;
  padding: 0 4px;
}
.diff-hunk__body { font-size: 13px; }

.diff-table { width: 100%; border-collapse: collapse; }
.diff-table__num {
  width: 50px;
  min-width: 50px;
  padding: 0 8px;
  text-align: right;
  color: var(--color-text-tertiary);
  user-select: none;
  font-size: 12px;
  vertical-align: top;
}
.diff-table__prefix {
  width: 20px;
  min-width: 20px;
  text-align: center;
  user-select: none;
  vertical-align: top;
}
.diff-table__content { vertical-align: top; }
.diff-table__code { font-family: inherit; white-space: pre-wrap; word-break: break-all; }
.diff-table__code :deep(code) { font-family: inherit; }

/* 行类型样式 */
.diff-line--added { background: var(--color-success-container); }
.diff-line--added .diff-table__prefix { color: var(--color-success); }
.diff-line--removed { background: var(--color-error-container); }
.diff-line--removed .diff-table__prefix { color: var(--color-error); }
.diff-line--conflict { background: var(--color-warning-container); }
.diff-line--conflict .diff-table__prefix { color: var(--color-warning); }
.diff-line--context .diff-table__prefix { color: var(--color-text-tertiary); }

/* Step 8: AI 审查意见 severity 色标 */
.diff-hunk__ai-comments {
  padding: 4px 12px 8px 48px;
  border-top: 1px dashed var(--color-border-default);
}
.ai-comment {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  padding: 3px 6px;
  margin: 2px 0;
  border-radius: 3px;
  font-size: 12px;
}
.ai-comment__icon { flex-shrink: 0; }
.ai-comment__severity {
  font-weight: 600;
  text-transform: uppercase;
  font-size: 10px;
  flex-shrink: 0;
}
.ai-comment__message { color: var(--color-text-primary); }
.ai-comment__suggestion {
  margin-left: 8px;
  color: var(--color-text-tertiary);
  font-style: italic;
}
.ai-comment--critical { background: var(--color-error-container); border-left: 3px solid var(--color-error); }
.ai-comment--error { background: var(--color-error-container); border-left: 3px solid var(--color-error); }
.ai-comment--warning { background: var(--color-warning-container); border-left: 3px solid var(--color-warning); }
.ai-comment--info { background: var(--color-primary-focus); background: color-mix(in srgb, var(--color-primary) 8%, transparent); border-left: 3px solid var(--color-primary); }

/* Step 4: 行内评论 */
.diff-line__comments { padding: 0 12px 4px 48px; }
.inline-comment {
  display: flex;
  gap: 6px;
  padding: 2px 6px;
  font-size: 12px;
  color: var(--color-text-primary);
  background: var(--color-bg-surface-container);
  border-radius: 3px;
  margin: 2px 0;
}
.inline-comment__author { font-weight: 600; color: var(--color-primary); }

/* 空状态 */
.diff-viewer__empty {
  display: flex;
  align-items: center;
  justify-content: center;
  flex: 1;
  color: var(--color-text-tertiary);
}
</style>
