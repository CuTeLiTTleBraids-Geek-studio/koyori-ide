<script setup lang="ts">
// Koyori IDE 组件 · Snapshot Timeline。
// 喵，这是 Snapshot Timeline，负责 Koyori IDE 的界面呈现喵~
/**
 * Plan 11 Task 14 — SnapshotTimeline.vue
 *
 * Step 6: 时间线 + 原因标签 + 文件变更数 + diff 查看 + 回滚
 * Step 7: 选择性回滚（勾选文件回滚）
 * Step 8: Git 状态展示（branch / clean / changes）
 *
 * 安全：本组件不渲染外部 HTML，仅展示结构化数据。
 */
import { computed, onMounted } from "vue";
import { ElMessageBox } from "element-plus";
import { useI18n } from "@/lib/i18n";
import {
  snapshotState,
  listSnapshots,
  selectSnapshot,
  deleteSnapshot,
  restorePartial,
  calculateRestoreDiff,
  restoreSnapshotExact,
  toggleFileSelection,
  toggleSelectAllFiles,
  diffSnapshots,
  createSnapshot,
} from "@/stores/snapshot";
import type { Snapshot, SnapshotReason } from "@/types";

const { t } = useI18n();

onMounted(() => {
  void listSnapshots();
});

// ---- Step 6: 原因标签 ----

function reasonLabel(reason: SnapshotReason): string {
  return t(`snapshotTimeline.reason.${reason}`);
}

function reasonClass(reason: SnapshotReason): string {
  switch (reason) {
    case "manual":
      return "snap-reason--manual";
    case "plan-step":
      return "snap-reason--plan";
    case "goal-checkpoint":
      return "snap-reason--goal";
    case "pre-apply":
      return "snap-reason--apply";
    case "workflow-step":
      return "snap-reason--workflow";
    case "file-save":
      return "snap-reason--history";
    default:
      return "";
  }
}

function formatTime(iso: string): string {
  try {
    return new Date(iso).toLocaleString();
  } catch {
    return iso;
  }
}

// ---- Step 7: 选择性回滚 ----

const selectedSnap = computed<Snapshot | null>(() => snapshotState.selected);

const allFilesSelected = computed<boolean>(() => {
  if (!selectedSnap.value || selectedSnap.value.files.length === 0) return false;
  return selectedSnap.value.files.every((f) =>
    snapshotState.selectedFilePaths.has(f.path),
  );
});

function isFileSelected(path: string): boolean {
  return snapshotState.selectedFilePaths.has(path);
}

/**
 * GOAL-P1-01: restore the workspace to the exact snapshot state.
 *
 * The old handler called the partial-semantics `restoreSnapshot`, which wrote
 * snapshot files back but silently kept every file created after the snapshot.
 * A button labelled "Restore All" that leaves post-snapshot files in place is a
 * false capability claim, so this now uses the exact path.
 *
 * Deletion is irreversible, so the diff is shown first and the user must
 * confirm. Cancelling leaves the workspace untouched: `calculateRestoreDiff` is
 * preview-only and `restoreSnapshotExact` is never reached.
 */
async function handleRestoreAll(id: string): Promise<void> {
  const diff = await calculateRestoreDiff(id);
  if (!diff) return;

  const deletions = diff.addedAfterSnapshot ?? [];
  if (deletions.length > 0) {
    const preview = deletions.slice(0, 20).join("\n");
    const more = deletions.length > 20
      ? `\n${t("snapshotTimeline.restoreMoreFiles", { n: deletions.length - 20 })}`
      : "";
    try {
      await ElMessageBox.confirm(
        `${t("snapshotTimeline.restoreExactWarning", { n: deletions.length })}\n\n${preview}${more}`,
        t("snapshotTimeline.restoreExactTitle"),
        { type: "warning" },
      );
    } catch {
      return; // user cancelled — nothing was modified
    }
  }

  // confirmed=true is justified at this point: either the user accepted the
  // deletion dialog above, or the diff showed nothing to delete so the restore
  // is non-destructive. The backend still refuses confirmed=false outright, so
  // the flag cannot be defaulted on inside the service.
  if (await restoreSnapshotExact(id, true)) {
    await listSnapshots();
  }
}

async function handleRestorePartial(): Promise<void> {
  const ok = await restorePartial();
  if (ok) {
    await listSnapshots();
  }
}

async function handleDelete(id: string): Promise<void> {
  await deleteSnapshot(id);
}

async function handleCreateManual(): Promise<void> {
  await createSnapshot("manual");
}

// ---- Step 2: diff 查看 ----

const diffTargetID = computed<string | null>(
  () => snapshotState.snapshots[1]?.id ?? null,
);

async function handleDiff(fromID: string): Promise<void> {
  if (!diffTargetID.value || fromID === diffTargetID.value) return;
  await diffSnapshots(fromID, diffTargetID.value);
}
</script>

<template>
  <div class="snapshot-timeline">
    <div class="snapshot-timeline__header">
      <h3 class="snapshot-timeline__title">{{ t("snapshotTimeline.title") }}</h3>
      <button class="snapshot-timeline__btn" @click="handleCreateManual">
        {{ t("snapshotTimeline.createManual") }}
      </button>
    </div>

    <!-- Step 6: 时间线 -->
    <ol v-if="snapshotState.snapshots.length > 0" class="snapshot-timeline__list">
      <li
        v-for="snap in snapshotState.snapshots"
        :key="snap.id"
        class="snapshot-timeline__item"
        :class="{ 'is-selected': selectedSnap?.id === snap.id }"
      >
        <div class="snapshot-timeline__item-head">
          <span class="snapshot-timeline__dot" :class="reasonClass(snap.reason)"></span>
          <span class="snapshot-timeline__reason" :class="reasonClass(snap.reason)">
            {{ reasonLabel(snap.reason) }}
          </span>
          <span class="snapshot-timeline__time">{{ formatTime(snap.createdAt) }}</span>
        </div>
        <div class="snapshot-timeline__meta">
          <span class="snapshot-timeline__count">
            {{ t("snapshotTimeline.fileCount", { n: snap.fileCount }) }}
          </span>
          <span v-if="snap.gitState" class="snapshot-timeline__git">
            <span :class="snap.gitState.isClean ? 'git-clean' : 'git-dirty'">
              {{ snap.gitState.isClean ? "clean" : "dirty" }}
            </span>
            <span v-if="snap.gitState.branch" class="snapshot-timeline__branch">
              {{ snap.gitState.branch }}
            </span>
          </span>
        </div>
        <div class="snapshot-timeline__actions">
          <button class="snapshot-timeline__btn" @click="selectSnapshot(snap.id)">
            {{ t("snapshotTimeline.viewDetails") }}
          </button>
          <button
            class="snapshot-timeline__btn"
            :disabled="!diffTargetID || snap.id === diffTargetID"
            @click="handleDiff(snap.id)"
          >
            {{ t("snapshotTimeline.diff") }}
          </button>
          <button
            class="snapshot-timeline__btn snapshot-timeline__btn--danger"
            @click="handleRestoreAll(snap.id)"
          >
            {{ t("snapshotTimeline.restoreAll") }}
          </button>
          <button
            class="snapshot-timeline__btn snapshot-timeline__btn--ghost"
            @click="handleDelete(snap.id)"
          >
            {{ t("snapshotTimeline.delete") }}
          </button>
        </div>
      </li>
    </ol>
    <p v-else class="snapshot-timeline__empty">{{ t("snapshotTimeline.empty") }}</p>

    <!-- Step 7: 选择性回滚面板 -->
    <div v-if="selectedSnap" class="snapshot-timeline__detail">
      <div class="snapshot-timeline__detail-head">
        <h4>{{ t("snapshotTimeline.detailTitle") }}</h4>
        <span class="snapshot-timeline__detail-id">{{ selectedSnap.id }}</span>
      </div>

      <!-- diff 结果 -->
      <div v-if="snapshotState.diff" class="snapshot-timeline__diff">
        <div class="snapshot-timeline__diff-group">
          <span class="diff-added">{{ t("snapshotTimeline.added") }}:</span>
          <span>{{ snapshotState.diff.added.length }}</span>
          <ul>
            <li v-for="p in snapshotState.diff.added" :key="p">{{ p }}</li>
          </ul>
        </div>
        <div class="snapshot-timeline__diff-group">
          <span class="diff-removed">{{ t("snapshotTimeline.removed") }}:</span>
          <span>{{ snapshotState.diff.removed.length }}</span>
          <ul>
            <li v-for="p in snapshotState.diff.removed" :key="p">{{ p }}</li>
          </ul>
        </div>
        <div class="snapshot-timeline__diff-group">
          <span class="diff-modified">{{ t("snapshotTimeline.modified") }}:</span>
          <span>{{ snapshotState.diff.modified.length }}</span>
          <ul>
            <li v-for="p in snapshotState.diff.modified" :key="p">{{ p }}</li>
          </ul>
        </div>
      </div>

      <!-- Step 7: 文件勾选列表 -->
      <div class="snapshot-timeline__files">
        <label class="snapshot-timeline__select-all">
          <input
            type="checkbox"
            :checked="allFilesSelected"
            @change="toggleSelectAllFiles(!allFilesSelected)"
          />
          <span>{{ t("snapshotTimeline.selectAll") }}</span>
        </label>
        <ul class="snapshot-timeline__file-list">
          <li
            v-for="f in selectedSnap.files"
            :key="f.path"
            class="snapshot-timeline__file"
          >
            <label>
              <input
                type="checkbox"
                :checked="isFileSelected(f.path)"
                @change="toggleFileSelection(f.path)"
              />
              <span class="snapshot-timeline__file-path">{{ f.path }}</span>
              <span class="snapshot-timeline__file-size">{{ f.size }}B</span>
            </label>
          </li>
        </ul>
        <button
          class="snapshot-timeline__btn snapshot-timeline__btn--danger"
          :disabled="snapshotState.selectedFilePaths.size === 0"
          @click="handleRestorePartial"
        >
          {{ t("snapshotTimeline.restoreSelected") }}
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.snapshot-timeline {
  display: flex;
  flex-direction: column;
  gap: 12px;
  height: 100%;
  overflow: auto;
}
.snapshot-timeline__header {
  display: flex;
  justify-content: space-between;
  align-items: center;
}
.snapshot-timeline__title {
  margin: 0;
  font-size: 14px;
}
.snapshot-timeline__list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.snapshot-timeline__item {
  border: 1px solid var(--el-border-color, #dcdfe6);
  border-radius: 4px;
  padding: 8px 10px;
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.snapshot-timeline__item.is-selected {
  border-color: var(--color-primary);
}
.snapshot-timeline__item-head {
  display: flex;
  align-items: center;
  gap: 8px;
}
.snapshot-timeline__dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  flex-shrink: 0;
}
.snap-reason--manual .snapshot-timeline__dot,
.snapshot-timeline__dot.snap-reason--manual { background: var(--color-text-tertiary); }
.snap-reason--plan .snapshot-timeline__dot,
.snapshot-timeline__dot.snap-reason--plan { background: var(--color-primary); }
.snap-reason--goal .snapshot-timeline__dot,
.snapshot-timeline__dot.snap-reason--goal { background: var(--color-success); }
.snap-reason--apply .snapshot-timeline__dot,
.snapshot-timeline__dot.snap-reason--apply { background: var(--color-warning); }
.snap-reason--workflow .snapshot-timeline__dot,
.snapshot-timeline__dot.snap-reason--workflow { background: var(--color-error); }
.snap-reason--history .snapshot-timeline__dot,
.snapshot-timeline__dot.snap-reason--history { background: var(--color-primary); }

.snapshot-timeline__reason {
  font-size: 12px;
  font-weight: 600;
}
.snapshot-timeline__time {
  font-size: 11px;
  color: var(--color-text-tertiary);
  margin-left: auto;
}
.snapshot-timeline__meta {
  display: flex;
  gap: 12px;
  font-size: 12px;
  color: var(--color-text-secondary);
}
.snapshot-timeline__git {
  display: flex;
  gap: 4px;
  align-items: center;
}
.git-clean { color: var(--color-success); }
.git-dirty { color: var(--color-error); }
.snapshot-timeline__branch {
  font-family: var(--koyori-ide-font-mono, "Cascadia Code", monospace);
  font-size: 11px;
}
.snapshot-timeline__actions {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
}
.snapshot-timeline__btn {
  padding: 4px 10px;
  border: 1px solid var(--color-primary);
  background: var(--color-primary);
  color: var(--color-on-primary);
  border-radius: 3px;
  cursor: pointer;
  font-size: 11px;
}
.snapshot-timeline__btn:disabled { opacity: 0.5; cursor: not-allowed; }
.snapshot-timeline__btn--danger {
  border-color: var(--color-error);
  background: var(--color-error);
}
.snapshot-timeline__btn--ghost {
  border-color: var(--color-border-default);
  background: transparent;
  color: var(--color-text-secondary);
}
.snapshot-timeline__empty {
  color: var(--color-text-tertiary);
  font-size: 12px;
  text-align: center;
  padding: 24px 0;
}
.snapshot-timeline__detail {
  border: 1px solid var(--color-border-default);
  border-radius: 4px;
  padding: 10px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.snapshot-timeline__detail-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
}
.snapshot-timeline__detail-id {
  font-family: var(--font-mono, monospace);
  font-size: 11px;
  color: var(--color-text-tertiary);
}
.snapshot-timeline__diff {
  display: flex;
  gap: 12px;
  font-size: 12px;
  border-top: 1px solid var(--color-border-subtle);
  padding-top: 8px;
}
.snapshot-timeline__diff-group {
  flex: 1;
}
.snapshot-timeline__diff-group ul {
  list-style: none;
  margin: 4px 0 0;
  padding: 0;
  font-family: var(--koyori-ide-font-mono, monospace);
  font-size: 11px;
  max-height: 100px;
  overflow: auto;
}
.diff-added { color: var(--color-success); font-weight: 600; }
.diff-removed { color: var(--color-error); font-weight: 600; }
.diff-modified { color: var(--color-warning); font-weight: 600; }
.snapshot-timeline__files {
  border-top: 1px solid var(--el-border-color-light, #e4e7ed);
  padding-top: 8px;
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.snapshot-timeline__select-all {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
}
.snapshot-timeline__file-list {
  list-style: none;
  margin: 0;
  padding: 0;
  max-height: 200px;
  overflow: auto;
}
.snapshot-timeline__file label {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
}
.snapshot-timeline__file-path {
  font-family: var(--koyori-ide-font-mono, monospace);
  flex: 1;
}
.snapshot-timeline__file-size {
  color: var(--el-text-color-secondary, #909399);
  font-size: 11px;
}
</style>
