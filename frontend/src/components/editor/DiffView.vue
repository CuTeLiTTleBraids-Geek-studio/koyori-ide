<script setup lang="ts">
// Koyori IDE 组件 · Diff View；交互服务：Git 集成（GitService）。
// 喵，这是 Diff View，负责 Koyori IDE 的界面呈现喵~
import { ref, watch, computed } from "vue";
import { VueMonacoEditor } from "@guolao/vue-monaco-editor";
import { gitService } from "@/api/services";
import { appState } from "@/stores/app";
import { detectLanguage } from "@/lib/language";
import { getMonacoThemeName } from "@/lib/monaco-themes";
import { Close } from "@element-plus/icons-vue";
import { notifyError } from "@/lib/notifications";
import { useI18n } from "@/lib/i18n";

const props = defineProps<{
  repoPath: string;
  filePath: string;
  visible: boolean;
  /** P1-04 row identity: staged=true diffs HEAD vs index, false index vs
   * worktree. Undefined keeps the legacy staged-first GetDiff behavior. */
  staged?: boolean;
}>();

const emit = defineEmits<{
  (e: "close"): void;
}>();

const { t } = useI18n();

const diffContent = ref("");
const loading = ref(false);

const monacoTheme = computed(() => getMonacoThemeName(appState.accentTheme));
const language = computed(() => detectLanguage(props.filePath));

// P1-01 stale 守卫：快速切换文件时只有最新一次加载允许回写 diff 内容，
// 迟到的旧响应（慢速 getDiff）不得覆盖新文件的 diff。
let loadSeq = 0;

async function loadDiff() {
  if (!props.filePath || !props.repoPath) return;
  const seq = ++loadSeq;
  loading.value = true;
  try {
    const diffText = props.staged === undefined
      ? await gitService.getDiff(props.repoPath, props.filePath)
      : await gitService.getDiffForSide(props.repoPath, props.filePath, props.staged);
    if (seq !== loadSeq) return;
    diffContent.value = diffText;
  } catch (e) {
    if (seq !== loadSeq) return;
    notifyError(t("diff.loadFailed", { error: e instanceof Error ? e.message : String(e) }));
  } finally {
    if (seq === loadSeq) {
      loading.value = false;
    }
  }
}

watch(() => [props.visible, props.filePath, props.staged], ([vis]) => {
  if (vis) loadDiff();
}, { immediate: true });

function handleClose() {
  emit("close");
}
</script>

<template>
  <div v-if="visible" class="diff-view">
    <div class="diff-view__header">
      <span class="diff-view__title">{{ t('diff.title', { path: filePath }) }}</span>
      <el-button
        :icon="Close"
        size="small"
        :aria-label="t('diff.closeAria')"
        @click="handleClose"
      >
        {{ t('diff.close') }}
      </el-button>
    </div>
    <div v-if="loading" class="diff-view__loading">{{ t('diff.loading') }}</div>
    <div v-else class="diff-view__editor">
      <VueMonacoEditor
        :value="diffContent"
        :language="language"
        :theme="monacoTheme"
        :options="{
          readOnly: true,
          fontSize: appState.fontSize,
          fontFamily: appState.fontFamily,
          minimap: { enabled: false },
          lineNumbers: 'on',
          scrollBeyondLastLine: false,
          automaticLayout: true,
        }"
        height="100%"
      />
    </div>
  </div>
</template>

<style scoped>
.diff-view {
  position: absolute;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  background-color: var(--color-bg-base);
  z-index: 10;
  display: flex;
  flex-direction: column;
}

.diff-view__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 8px 16px;
  border-bottom: 1px solid var(--color-border-subtle);
  background-color: var(--color-bg-surface);
}

.diff-view__title {
  font-size: 12px;
  color: var(--color-text-primary);
  font-family: var(--font-mono);
}

.diff-view__loading {
  display: flex;
  align-items: center;
  justify-content: center;
  height: 100%;
  color: var(--color-text-tertiary);
  font-size: 12px;
}

.diff-view__editor {
  flex: 1;
  min-height: 0;
}
</style>
