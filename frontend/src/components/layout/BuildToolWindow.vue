<script setup lang="ts">
// Koyori IDE 组件 · Build Tool Window。
// 喵，这是 Build Tool Window，负责 Koyori IDE 的界面呈现喵~
import { computed, onMounted, watch } from "vue";
import {
  Refresh,
  RefreshRight,
  Star,
  VideoPause,
  VideoPlay,
} from "@element-plus/icons-vue";
import { appState } from "@/stores/app";
import {
  buildToolState,
  refreshBuildTasks,
  rerunBuildTask,
  runBuildTask,
  selectBuildTask,
  stopBuildTask,
  toggleBuildFavorite,
  type BuildTaskItem,
  type BuildTaskSource,
} from "@/stores/buildTool";
import { useI18n } from "@/lib/i18n";

const { t } = useI18n();

const sourceOrder: BuildTaskSource[] = ["task", "launch", "toolchain", "npm", "make", "taskfile"];

const tasksById = computed(() => new Map(buildToolState.tasks.map((task) => [task.id, task])));
const groups = computed(() => {
  const result: Array<{ id: string; label: string; tasks: BuildTaskItem[] }> = [];
  const favorites = buildToolState.favorites
    .map((id) => tasksById.value.get(id))
    .filter((task): task is BuildTaskItem => task !== undefined);
  if (favorites.length > 0) result.push({ id: "favorites", label: t("build.group.favorites"), tasks: favorites });
  const recent = buildToolState.recent
    .map((id) => tasksById.value.get(id))
    .filter((task): task is BuildTaskItem => task !== undefined);
  if (recent.length > 0) result.push({ id: "recent", label: t("build.group.recent"), tasks: recent });
  for (const source of sourceOrder) {
    const tasks = buildToolState.tasks.filter((task) => task.source === source);
    if (tasks.length > 0) result.push({ id: source, label: t(`build.group.${source}`), tasks });
  }
  return result;
});

const selectedRun = computed(() => (
  buildToolState.selectedTaskId ? buildToolState.runs[buildToolState.selectedTaskId] : undefined
));
const isRunning = computed(() => {
  const id = buildToolState.activeTaskId;
  return !!id && buildToolState.runs[id]?.status === "running";
});

function formatDuration(durationMs: number): string {
  if (durationMs < 1000) return `${durationMs}ms`;
  return `${(durationMs / 1000).toFixed(2)}s`;
}

function refresh(): void {
  void refreshBuildTasks(appState.currentProject ?? "");
}

onMounted(refresh);
watch(
  () => appState.currentProject,
  (root, previous) => {
    if (root !== previous) void refreshBuildTasks(root ?? "");
  },
);
</script>

<template>
  <section class="build-tool" :aria-label="t('activity.build')">
    <div class="build-tool__toolbar">
      <button
        type="button"
        class="build-tool__icon-btn"
        data-test="build-refresh"
        :title="t('build.refresh')"
        :aria-label="t('build.refresh')"
        @click="refresh"
      >
        <el-icon :size="14"><Refresh /></el-icon>
      </button>
      <span class="build-tool__spacer" />
      <button
        v-if="isRunning"
        type="button"
        class="build-tool__icon-btn build-tool__icon-btn--stop"
        data-test="build-stop"
        :title="t('build.stop')"
        :aria-label="t('build.stop')"
        @click="stopBuildTask"
      >
        <el-icon :size="14"><VideoPause /></el-icon>
      </button>
      <button
        type="button"
        class="build-tool__icon-btn"
        data-test="build-rerun"
        :disabled="buildToolState.recent.length === 0 || isRunning"
        :title="t('build.rerun')"
        :aria-label="t('build.rerun')"
        @click="rerunBuildTask()"
      >
        <el-icon :size="14"><RefreshRight /></el-icon>
      </button>
    </div>

    <div class="build-tool__tree">
      <div v-if="buildToolState.loading" class="build-tool__state">{{ t("build.loading") }}</div>
      <div v-else-if="buildToolState.errorMessage" class="build-tool__state build-tool__state--error">
        {{ buildToolState.errorMessage }}
      </div>
      <template v-else>
        <section v-for="group in groups" :key="group.id" class="build-tool__group" :data-group="group.id">
          <h3 class="build-tool__group-title">{{ group.label }}</h3>
          <div
            v-for="task in group.tasks"
            :key="`${group.id}:${task.id}`"
            class="build-tool__task"
            :class="{
              'build-tool__task--selected': buildToolState.selectedTaskId === task.id,
              [`build-tool__task--${buildToolState.runs[task.id]?.status}`]: !!buildToolState.runs[task.id],
            }"
          >
            <button
              type="button"
              class="build-tool__task-select"
              :data-task="task.id"
              @click="selectBuildTask(task.id)"
            >
              <span class="build-tool__status" aria-hidden="true" />
              <span class="build-tool__task-main">
                <span class="build-tool__task-label">{{ task.label }}</span>
                <span v-if="buildToolState.runs[task.id]?.durationMs" class="build-tool__duration">
                  {{ formatDuration(buildToolState.runs[task.id].durationMs) }}
                </span>
              </span>
            </button>
            <button
              type="button"
              class="build-tool__row-action"
              :class="{ 'build-tool__row-action--active': buildToolState.favorites.includes(task.id) }"
              :data-favorite="task.id"
              :title="t('build.favorite')"
              :aria-label="t('build.favorite')"
              @click.stop="toggleBuildFavorite(task.id)"
            >
              <el-icon :size="12"><Star /></el-icon>
            </button>
            <button
              type="button"
              class="build-tool__row-action"
              :data-run="task.id"
              :disabled="isRunning"
              :title="t('build.run')"
              :aria-label="t('build.run')"
              @click.stop="runBuildTask(task.id)"
            >
              <el-icon :size="12"><VideoPlay /></el-icon>
            </button>
          </div>
        </section>
        <div v-if="groups.length === 0" class="build-tool__state">{{ t("build.empty") }}</div>
      </template>
    </div>

    <div class="build-tool__output-head">
      <span>{{ t("build.output") }}</span>
      <span v-if="selectedRun" class="build-tool__run-status">{{ selectedRun.status }}</span>
    </div>
    <pre class="build-tool__output" data-test="build-output">{{ selectedRun?.output ?? "" }}</pre>
  </section>
</template>

<style scoped>
.build-tool {
  display: grid;
  grid-template-rows: 34px minmax(140px, 1fr) 28px minmax(96px, 0.45fr);
  width: 100%;
  min-width: 0;
  height: 100%;
  color: var(--color-text-primary);
}

.build-tool__toolbar,
.build-tool__output-head {
  display: flex;
  align-items: center;
  padding: 0 8px;
  border-bottom: 0.5px solid var(--color-border-subtle);
}

.build-tool__spacer { flex: 1; }

.build-tool__icon-btn,
.build-tool__row-action {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  padding: 0;
  border: 0;
  border-radius: 4px;
  color: var(--color-text-secondary);
  background: transparent;
  cursor: pointer;
}

.build-tool__icon-btn:hover,
.build-tool__row-action:hover { background: var(--color-hover-bg); color: var(--color-text-primary); }
.build-tool__icon-btn:disabled,
.build-tool__row-action:disabled { opacity: 0.4; cursor: default; }
.build-tool__icon-btn--stop { color: var(--color-error); }

.build-tool__tree { min-height: 0; overflow: auto; padding: 4px 0; }
.build-tool__group { margin: 0; padding: 0; }
.build-tool__group-title {
  margin: 0;
  padding: 5px 10px 3px;
  color: var(--color-text-tertiary);
  font-size: 10px;
  font-weight: 600;
  letter-spacing: 0;
  text-transform: uppercase;
}

.build-tool__task {
  display: grid;
  grid-template-columns: 8px minmax(0, 1fr) 24px 24px;
  align-items: center;
  width: 100%;
  min-height: 30px;
  padding: 0 6px;
  color: inherit;
  background: transparent;
}

.build-tool__task:hover,
.build-tool__task--selected { background: var(--color-hover-bg); }
.build-tool__task-select {
  display: grid;
  grid-column: 1 / 3;
  grid-template-columns: 8px minmax(0, 1fr);
  align-items: center;
  align-self: stretch;
  min-width: 0;
  padding: 0;
  border: 0;
  color: inherit;
  background: transparent;
  text-align: left;
  cursor: pointer;
}
.build-tool__task-select:focus-visible {
  outline: 2px solid var(--color-primary-focus);
  outline-offset: -2px;
}
.build-tool__status { width: 5px; height: 5px; border-radius: 50%; background: var(--color-text-tertiary); }
.build-tool__task--running .build-tool__status { background: var(--color-warning); }
.build-tool__task--success .build-tool__status { background: var(--color-success); }
.build-tool__task--failed .build-tool__status { background: var(--color-error); }
.build-tool__task--cancelled .build-tool__status { background: var(--color-text-tertiary); }

.build-tool__task-main { display: flex; min-width: 0; align-items: baseline; gap: 6px; }
.build-tool__task-label { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12px; }
.build-tool__duration { color: var(--color-text-tertiary); font-size: 10px; white-space: nowrap; }
.build-tool__row-action { width: 22px; height: 22px; opacity: 0.35; }
.build-tool__task:hover .build-tool__row-action,
.build-tool__row-action--active { opacity: 1; }
.build-tool__row-action--active { color: var(--color-warning); }

.build-tool__state { padding: 18px 12px; color: var(--color-text-tertiary); font-size: 12px; text-align: center; }
.build-tool__state--error { color: var(--color-error); }
.build-tool__output-head { justify-content: space-between; color: var(--color-text-tertiary); font-size: 10px; text-transform: uppercase; }
.build-tool__run-status { text-transform: none; }
.build-tool__output {
  min-width: 0;
  min-height: 0;
  margin: 0;
  padding: 8px 10px;
  overflow: auto;
  color: var(--color-text-secondary);
  background: var(--color-editor-bg);
  font-family: var(--font-mono);
  font-size: 11px;
  line-height: 1.45;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
</style>
