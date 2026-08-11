<script setup lang="ts">
// Koyori IDE 组件 · Diff Section。
// 喵，这是 Diff Section，负责 Koyori IDE 的界面呈现喵~
/**
 * Plan 11 Task 13 Step 5 — Diff 增强设置分区。
 *
 * 提供结构化 diff 预览（DiffViewer）+ 三方合并测试 + 导出选项。
 *   - 文件选择：输入旧/新内容计算 diff
 *   - 三方合并：base/ours/theirs 输入 + 冲突标记预览
 *   - 导出：Markdown / unified diff / HTML
 *
 * G-SEC-11：iframe sandbox="allow-scripts"（DiffViewer 内部）。
 * G-SEC-11：代码高亮经 sanitizeHtml 净化（DiffViewer 内部）。
 */
import { ref } from "vue";
import { useI18n } from "@/lib/i18n";
import DiffViewer from "@/components/ai-assistant/DiffViewer.vue";
import {
  diffState,
  computeFileDiff,
  threeWayMerge,
  exportDiff,
} from "@/stores/diff";
import type { DiffExportFormat } from "@/types";

const { t } = useI18n();

// 单文件 diff 输入
const filePath = ref("example.txt");
const oldContent = ref("line1\nline2\nline3");
const newContent = ref("line1\nline2-modified\nline3\nline4");

// 三方合并输入
const mergeBase = ref("shared\nbase\ncontent");
const mergeOurs = ref("shared\nours\ncontent");
const mergeTheirs = ref("shared\ntheirs\ncontent");

async function handleComputeFileDiff(): Promise<void> {
  await computeFileDiff(filePath.value, oldContent.value, newContent.value);
}

async function handleThreeWayMerge(): Promise<void> {
  await threeWayMerge(mergeBase.value, mergeOurs.value, mergeTheirs.value);
}

async function handleExport(format: DiffExportFormat): Promise<void> {
  await exportDiff(format, []);
}
</script>

<template>
  <div class="diff-section">
    <h3 class="diff-section__title">{{ t("diffSection.title") }}</h3>
    <p class="diff-section__desc">{{ t("diffSection.description") }}</p>

    <!-- 单文件 diff -->
    <div class="diff-section__group">
      <h4>{{ t("diffSection.singleFile") }}</h4>
      <div class="diff-section__row">
        <label class="diff-section__label">
          <span>{{ t("diffSection.filePath") }}</span>
          <input v-model="filePath" class="diff-section__input" />
        </label>
      </div>
      <div class="diff-section__cols">
        <label class="diff-section__label">
          <span>{{ t("diffSection.oldContent") }}</span>
          <textarea v-model="oldContent" class="diff-section__textarea" rows="6" />
        </label>
        <label class="diff-section__label">
          <span>{{ t("diffSection.newContent") }}</span>
          <textarea v-model="newContent" class="diff-section__textarea" rows="6" />
        </label>
      </div>
      <button class="diff-section__btn" @click="handleComputeFileDiff">
        {{ t("diffSection.computeDiff") }}
      </button>
    </div>

    <!-- 三方合并 -->
    <div class="diff-section__group">
      <h4>{{ t("diffSection.threeWayMerge") }}</h4>
      <div class="diff-section__cols diff-section__cols--merge">
        <label class="diff-section__label">
          <span>{{ t("diffSection.base") }}</span>
          <textarea v-model="mergeBase" class="diff-section__textarea" rows="4" />
        </label>
        <label class="diff-section__label">
          <span>{{ t("diffSection.ours") }}</span>
          <textarea v-model="mergeOurs" class="diff-section__textarea" rows="4" />
        </label>
        <label class="diff-section__label">
          <span>{{ t("diffSection.theirs") }}</span>
          <textarea v-model="mergeTheirs" class="diff-section__textarea" rows="4" />
        </label>
      </div>
      <button class="diff-section__btn" @click="handleThreeWayMerge">
        {{ t("diffSection.runMerge") }}
      </button>
      <div v-if="diffState.mergeResult" class="diff-section__merge-result">
        <span v-if="diffState.mergeResult.hasConflict" class="diff-merge__conflict">
          {{ t("diffSection.conflicts") }}: {{ diffState.mergeResult.conflicts }}
        </span>
        <span v-else class="diff-merge__clean">{{ t("diffSection.noConflicts") }}</span>
        <pre class="diff-merge__output">{{ diffState.mergeResult.merged }}</pre>
      </div>
    </div>

    <!-- 导出 -->
    <div class="diff-section__group">
      <h4>{{ t("diffSection.export") }}</h4>
      <div class="diff-section__export-btns">
        <button class="diff-section__btn" :disabled="!diffState.diff" @click="handleExport('markdown')">
          {{ t("diffSection.exportMarkdown") }}
        </button>
        <button class="diff-section__btn" :disabled="!diffState.diff" @click="handleExport('unified')">
          {{ t("diffSection.exportUnified") }}
        </button>
        <button class="diff-section__btn" :disabled="!diffState.diff" @click="handleExport('html')">
          {{ t("diffSection.exportHtml") }}
        </button>
      </div>
    </div>

    <!-- DiffViewer 预览 -->
    <div class="diff-section__group">
      <h4>{{ t("diffSection.preview") }}</h4>
      <div class="diff-section__viewer">
        <DiffViewer @export="handleExport" />
      </div>
    </div>
  </div>
</template>

<style scoped>
.diff-section {
  display: flex;
  flex-direction: column;
  gap: 16px;
  min-width: 0;
}
.diff-section__title { margin: 0; font-size: 16px; }
.diff-section__desc { margin: 0; color: var(--el-text-color-secondary, #909399); font-size: 13px; }
.diff-section__group {
  border: 1px solid var(--el-border-color, #dcdfe6);
  border-radius: 4px;
  padding: 12px;
  min-width: 0;
}
.diff-section__group h4 { margin: 0 0 8px 0; font-size: 14px; }
.diff-section__row { margin-bottom: 8px; }
.diff-section__cols {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 8px;
  margin-bottom: 8px;
}
.diff-section__cols--merge { grid-template-columns: repeat(3, minmax(0, 1fr)); }
.diff-section__label {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
  font-size: 12px;
  color: var(--el-text-color-regular, #606266);
}
.diff-section__input,
.diff-section__textarea {
  box-sizing: border-box;
  width: 100%;
  min-width: 0;
  padding: 6px 8px;
  border: 1px solid var(--el-border-color, #dcdfe6);
  border-radius: 3px;
  font-family: var(--koyori-ide-font-mono, "Cascadia Code", monospace);
  font-size: 12px;
  background: var(--el-bg-color, #fff);
  color: var(--el-text-color-primary, #303030);
  resize: vertical;
}
.diff-section__btn {
  padding: 6px 16px;
  border: 1px solid var(--el-color-primary, #409eff);
  background: var(--el-color-primary, #409eff);
  color: #fff;
  border-radius: 3px;
  cursor: pointer;
  font-size: 12px;
}
.diff-section__btn:disabled { opacity: 0.5; cursor: not-allowed; }
.diff-section__export-btns { display: flex; flex-wrap: wrap; gap: 8px; }
.diff-section__viewer {
  border: 1px solid var(--el-border-color, #dcdfe6);
  border-radius: 4px;
  height: 400px;
  overflow: hidden;
}
.diff-section__merge-result { margin-top: 8px; }
.diff-merge__conflict { color: #f5222d; font-weight: 600; }
.diff-merge__clean { color: #52c41a; font-weight: 600; }
.diff-merge__output {
  box-sizing: border-box;
  max-width: 100%;
  margin-top: 4px;
  padding: 8px;
  background: var(--el-fill-color-light, #f5f7fa);
  border-radius: 3px;
  font-family: var(--koyori-ide-font-mono, monospace);
  font-size: 12px;
  white-space: pre-wrap;
  max-height: 200px;
  overflow: auto;
}

@media (max-width: 760px) {
  .diff-section__cols,
  .diff-section__cols--merge {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
