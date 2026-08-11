<script setup lang="ts">
// Koyori IDE 组件 · Rebase Editor。
// 喵，这是 Rebase Editor，负责 Koyori IDE 的界面呈现喵~
import { computed, nextTick, ref, watch } from "vue";
import {
  ArrowDown,
  ArrowUp,
  Check,
  Close,
  DArrowRight,
  Rank,
  Refresh,
  VideoPlay,
} from "@element-plus/icons-vue";
import { useI18n } from "@/lib/i18n";
import FocusTrapDialog from "@/components/common/FocusTrapDialog.vue";
import {
  REBASE_COMMIT_ACTIONS,
  isRebaseCommitAction,
  hasValidRebaseActionOrder,
  useGitRebaseStore,
  type GitRebaseStore,
  type RebaseCommitAction,
  type RebaseTodoAction,
} from "@/lib/gitRebase";

const props = withDefaults(defineProps<{
  repoPath: string;
  upstreamBranch?: string;
  autoLoad?: boolean;
  store?: GitRebaseStore;
}>(), {
  upstreamBranch: "",
  autoLoad: true,
});

const emit = defineEmits<{
  (event: "update:upstreamBranch", value: string): void;
  (event: "update:actions", actions: RebaseTodoAction[]): void;
  (event: "loaded", actions: RebaseTodoAction[]): void;
  (event: "started"): void;
  (event: "applied", actions: RebaseTodoAction[]): void;
  (event: "continued"): void;
  (event: "aborted"): void;
  (event: "skipped"): void;
  (event: "error", error: unknown): void;
}>();

const { t } = useI18n();
const defaultStore = useGitRebaseStore();
const store = computed(() => props.store ?? defaultStore);
const upstream = ref(props.upstreamBranch);
const listElement = ref<HTMLElement | null>(null);
const focusedCommitSha = ref<string | null>(null);
const draggedCommitSha = ref<string | null>(null);
const applyConfirmationOpen = ref(false);
const applyConfirmationContext = ref<{
  repoPath: string;
  upstreamBranch: string;
  actions: string;
} | null>(null);

let loadGeneration = 0;

const contextMatches = computed(() =>
  store.value.state.repoPath === props.repoPath.trim()
  && store.value.state.upstreamBranch === upstream.value.trim()
);
const actions = computed(() => contextMatches.value ? store.value.actions : []);
const busy = computed(() => store.value.busy);
const canLoad = computed(() => Boolean(props.repoPath.trim() && upstream.value.trim()) && !busy.value);
const canStart = computed(() =>
  canLoad.value
  && contextMatches.value
  && actions.value.length > 0
  && !store.value.inProgress
);
const canApply = computed(() =>
  contextMatches.value
  && actions.value.length > 0
  && store.value.owned
  && store.value.inProgress
  && store.value.startPrepared
  && (store.value.phase === "awaitingApply" || store.value.phase === "applying")
  && !store.value.rebaseAdvanced
  && actions.value.every((action) =>
    action.action !== "reword" || Boolean(action.shortMessage.trim())
  )
  && hasValidRebaseActionOrder(actions.value)
  && !busy.value
);
const canEdit = computed(() =>
  !busy.value
  && (store.value.phase === "idle" || store.value.phase === "awaitingApply"),
);
const canContinue = computed(() =>
  contextMatches.value
  && store.value.owned
  && store.value.inProgress
  && store.value.actionsApplied
  && (store.value.phase === "ready" || store.value.phase === "stopped")
  && !busy.value
);
const canSkip = computed(() =>
  canContinue.value
  && store.value.phase === "stopped"
  && store.value.stopReason === "commandError",
);
const canAbort = computed(() =>
  contextMatches.value && store.value.owned && store.value.inProgress && !busy.value
);

function cloneActions(): RebaseTodoAction[] {
  return actions.value.map((action) => ({ ...action }));
}

function shortSha(sha: string): string {
  return sha.slice(0, 8);
}

function authorLabel(action: RebaseTodoAction): string {
  if (action.authorName && action.authorEmail) {
    return `${action.authorName} <${action.authorEmail}>`;
  }
  return action.authorName || action.authorEmail || t("rebaseEditor.unknownAuthor");
}

function actionLabel(action: RebaseCommitAction): string {
  return t(`rebaseEditor.action.${action}`);
}
function canUseAction(index: number, action: RebaseCommitAction): boolean {
  if (action !== "squash" && action !== "fixup") return true;
  return actions.value.slice(0, index).some(
    (candidate) => isRebaseCommitAction(candidate.action) && candidate.action !== "drop",
  );
}

function rowElements(): HTMLElement[] {
  if (!listElement.value) return [];
  return Array.from(listElement.value.querySelectorAll<HTMLElement>("[data-rebase-row]"));
}

async function focusRow(index: number): Promise<void> {
  await nextTick();
  const rows = rowElements();
  const row = rows[Math.max(0, Math.min(index, rows.length - 1))];
  row?.focus();
}

function emitActionsChanged(): void {
  emit("update:actions", cloneActions());
}

async function loadTodo(): Promise<void> {
  if (!props.repoPath.trim() || !upstream.value.trim()) return;
  const generation = ++loadGeneration;
  try {
    const loaded = await store.value.loadTodoList(
      props.repoPath,
      upstream.value,
    );
    if (generation !== loadGeneration) return;
    focusedCommitSha.value = loaded[0]?.commitSha ?? null;
    emit("loaded", loaded.map((action) => ({ ...action })));
  } catch (error: unknown) {
    if (generation === loadGeneration) emit("error", error);
  }
}

function updateUpstream(event: Event): void {
  const target = event.target;
  if (!(target instanceof HTMLInputElement)) return;
  upstream.value = target.value;
  emit("update:upstreamBranch", target.value);
}

function updateAction(index: number, event: Event): void {
  const target = event.target;
  if (!(target instanceof HTMLSelectElement) || !isRebaseCommitAction(target.value)) return;
  if (store.value.updateAction(index, target.value)) emitActionsChanged();
}

function updateMessage(index: number, event: Event): void {
  const target = event.target;
  if (!(target instanceof HTMLInputElement)) return;
  if (store.value.updateMessage(index, target.value)) emitActionsChanged();
}

function moveAction(fromIndex: number, toIndex: number, focus = true): void {
  if (!store.value.moveAction(fromIndex, toIndex)) return;
  focusedCommitSha.value = actions.value[toIndex]?.commitSha ?? null;
  emitActionsChanged();
  if (focus) void focusRow(toIndex);
}

function beginDrag(event: DragEvent, commitSha: string): void {
  if (!canEdit.value) {
    event.preventDefault();
    return;
  }
  draggedCommitSha.value = commitSha;
  if (event.dataTransfer) {
    event.dataTransfer.effectAllowed = "move";
    event.dataTransfer.setData("text/plain", commitSha);
  }
}

function finishDrag(): void {
  draggedCommitSha.value = null;
}

function dropOn(event: DragEvent, targetCommitSha: string): void {
  event.preventDefault();
  const sourceSha = draggedCommitSha.value
    || event.dataTransfer?.getData("text/plain")
    || "";
  draggedCommitSha.value = null;
  if (!sourceSha || sourceSha === targetCommitSha) return;
  const fromIndex = actions.value.findIndex((action) => action.commitSha === sourceSha);
  const toIndex = actions.value.findIndex((action) => action.commitSha === targetCommitSha);
  moveAction(fromIndex, toIndex, false);
}

function cycleAction(index: number): void {
  const current = actions.value[index];
  if (!current) return;
  const availableActions = REBASE_COMMIT_ACTIONS.filter((action) =>
    canUseAction(index, action)
  );
  const currentIndex = availableActions.indexOf(
    isRebaseCommitAction(current.action) ? current.action : "pick",
  );
  const nextAction = availableActions[
    (currentIndex + 1) % availableActions.length
  ];
  if (store.value.updateAction(index, nextAction)) emitActionsChanged();
}

function eventCameFromControl(event: KeyboardEvent): boolean {
  const target = event.target;
  return target instanceof HTMLElement
    && target !== event.currentTarget
    && target.closest("button, select, input, textarea") !== null;
}

function handleRowKeydown(event: KeyboardEvent, index: number): void {
  if (eventCameFromControl(event)) return;
  const lastIndex = actions.value.length - 1;
  if (event.key === "ArrowUp") {
    event.preventDefault();
    if ((event.altKey || event.ctrlKey) && index > 0) moveAction(index, index - 1);
    else void focusRow(index - 1);
  } else if (event.key === "ArrowDown") {
    event.preventDefault();
    if ((event.altKey || event.ctrlKey) && index < lastIndex) moveAction(index, index + 1);
    else void focusRow(index + 1);
  } else if (event.key === "Home") {
    event.preventDefault();
    void focusRow(0);
  } else if (event.key === "End") {
    event.preventDefault();
    void focusRow(lastIndex);
  } else if (event.key === " " || event.code === "Space") {
    event.preventDefault();
    cycleAction(index);
  }
}

async function startRebase(): Promise<void> {
  try {
    await store.value.startRebase(props.repoPath, upstream.value);
    emit("started");
  } catch (error: unknown) {
    emit("error", error);
  }
}

function requestApply(): void {
  if (!canApply.value) return;
  applyConfirmationContext.value = {
    repoPath: props.repoPath.trim(),
    upstreamBranch: upstream.value.trim(),
    actions: JSON.stringify(cloneActions()),
  };
  applyConfirmationOpen.value = true;
}

function cancelApply(): void {
  applyConfirmationOpen.value = false;
  applyConfirmationContext.value = null;
}

async function confirmApply(): Promise<void> {
  const confirmation = applyConfirmationContext.value;
  if (
    !confirmation
    || confirmation.repoPath !== props.repoPath.trim()
    || confirmation.upstreamBranch !== upstream.value.trim()
    || confirmation.actions !== JSON.stringify(cloneActions())
  ) {
    cancelApply();
    return;
  }
  cancelApply();
  try {
    await store.value.applyActions(confirmation.repoPath);
    emit("applied", cloneActions());
  } catch (error: unknown) {
    emit("error", error);
  }
}

async function continueRebaseOperation(): Promise<void> {
  try {
    await store.value.continueRebase(props.repoPath);
    emit("continued");
  } catch (error: unknown) {
    emit("error", error);
  }
}

async function abortRebaseOperation(): Promise<void> {
  try {
    await store.value.abortRebase(props.repoPath);
    applyConfirmationOpen.value = false;
    emit("aborted");
  } catch (error: unknown) {
    emit("error", error);
  }
}

async function skipCommit(): Promise<void> {
  try {
    await store.value.skipCommit(props.repoPath);
    emit("skipped");
  } catch (error: unknown) {
    emit("error", error);
  }
}

watch(
  () => props.upstreamBranch,
  (value) => {
    upstream.value = value;
  },
);

watch(
  () => [props.repoPath, upstream.value, JSON.stringify(cloneActions())] as const,
  () => {
    if (applyConfirmationOpen.value) cancelApply();
  },
);

watch(
  () => [props.repoPath, props.upstreamBranch, props.autoLoad] as const,
  ([repoPath, branch, autoLoad]) => {
    loadGeneration += 1;
    focusedCommitSha.value = null;
    if (!autoLoad || !repoPath.trim() || !branch.trim()) return;
    upstream.value = branch;
    void loadTodo();
  },
  { immediate: true },
);

defineExpose({
  loadTodo,
  moveAction,
  requestApply,
  startRebase,
});
</script>

<template>
  <section class="rebase-editor" :aria-label="t('rebaseEditor.ariaLabel')">
    <header class="rebase-editor__toolbar">
      <label class="rebase-editor__upstream">
        <span>{{ t("git.rebaseBranchPrompt") }}</span>
        <input
          :value="upstream"
          type="text"
          :disabled="busy || store.inProgress"
          :aria-label="t('git.rebaseBranchPrompt')"
          @input="updateUpstream"
          @keydown.enter="loadTodo"
        >
      </label>

      <div class="rebase-editor__commands">
        <button
          type="button"
          class="rebase-editor__icon-command"
          :disabled="!canLoad"
          :aria-label="t('rebaseEditor.refresh')"
          :title="t('rebaseEditor.refresh')"
          @click="loadTodo"
        >
          <el-icon aria-hidden="true" :class="{ 'is-spinning': store.loading }"><Refresh /></el-icon>
        </button>
        <button
          type="button"
          class="rebase-editor__command"
          :disabled="!canStart"
          :aria-label="t('rebaseEditor.start')"
          @click="startRebase"
        >
          <el-icon aria-hidden="true"><VideoPlay /></el-icon>
          <span>{{ t("rebaseEditor.start") }}</span>
        </button>
        <button
          type="button"
          class="rebase-editor__command rebase-editor__command--primary"
          :disabled="!canApply"
          :aria-label="t('rebaseEditor.apply')"
          @click="requestApply"
        >
          <el-icon aria-hidden="true"><Check /></el-icon>
          <span>{{ t("rebaseEditor.apply") }}</span>
        </button>
        <button
          type="button"
          class="rebase-editor__command"
          :disabled="!canContinue"
          :aria-label="t('git.continueRebase')"
          @click="continueRebaseOperation"
        >
          <el-icon aria-hidden="true"><DArrowRight /></el-icon>
          <span>{{ t("git.continueRebase") }}</span>
        </button>
        <button
          type="button"
          class="rebase-editor__command"
          :disabled="!canSkip"
          :aria-label="t('rebaseEditor.skip')"
          @click="skipCommit"
        >
          <el-icon aria-hidden="true"><ArrowDown /></el-icon>
          <span>{{ t("rebaseEditor.skip") }}</span>
        </button>
        <button
          type="button"
          class="rebase-editor__command rebase-editor__command--danger"
          :disabled="!canAbort"
          :aria-label="t('git.abortRebase')"
          @click="abortRebaseOperation"
        >
          <el-icon aria-hidden="true"><Close /></el-icon>
          <span>{{ t("git.abortRebase") }}</span>
        </button>
      </div>
    </header>

    <div v-if="store.error" class="rebase-editor__error" role="alert">
      {{ t("rebaseEditor.operationFailed", { error: store.error }) }}
    </div>

    <div
      v-if="store.loading && actions.length === 0"
      class="rebase-editor__empty"
      role="status"
      aria-live="polite"
    >
      {{ t("common.loading") }}
    </div>
    <div
      v-else-if="actions.length === 0"
      class="rebase-editor__empty"
      role="status"
      aria-live="polite"
    >
      {{ t("rebaseEditor.empty") }}
    </div>

    <div
      v-else
      ref="listElement"
      class="rebase-editor__list"
      role="listbox"
      :aria-label="t('rebaseEditor.commitList')"
      :aria-busy="busy"
    >
      <div
        v-for="(commit, index) in actions"
        :id="`rebase-commit-${commit.commitSha}`"
        :key="commit.commitSha"
        class="rebase-editor__row"
        :class="{
          'is-focused': focusedCommitSha === commit.commitSha,
          'is-dragging': draggedCommitSha === commit.commitSha,
          'is-locked': !canEdit,
        }"
        data-rebase-row
        role="option"
        :aria-selected="focusedCommitSha === commit.commitSha"
        :aria-label="t('rebaseEditor.commitAria', {
          sha: shortSha(commit.commitSha),
          message: commit.shortMessage,
          action: actionLabel(isRebaseCommitAction(commit.action) ? commit.action : 'pick'),
        })"
        :tabindex="focusedCommitSha === commit.commitSha || (focusedCommitSha === null && index === 0) ? 0 : -1"
        :draggable="canEdit"
        @focus="focusedCommitSha = commit.commitSha"
        @keydown="handleRowKeydown($event, index)"
        @dragstart="beginDrag($event, commit.commitSha)"
        @dragend="finishDrag"
        @dragover.prevent
        @drop="dropOn($event, commit.commitSha)"
      >
        <span class="rebase-editor__drag" aria-hidden="true">
          <el-icon aria-hidden="true"><Rank /></el-icon>
        </span>

        <label class="rebase-editor__action-select">
          <span class="sr-only">
            {{ t("rebaseEditor.actionForCommit", { sha: shortSha(commit.commitSha) }) }}
          </span>
          <select
            :value="commit.action"
            :disabled="!canEdit"
            :aria-label="t('rebaseEditor.actionForCommit', { sha: shortSha(commit.commitSha) })"
            @change="updateAction(index, $event)"
          >
            <option
              v-for="action in REBASE_COMMIT_ACTIONS"
              :key="action"
              :value="action"
              :disabled="!canUseAction(index, action)"
            >
              {{ actionLabel(action) }}
            </option>
          </select>
        </label>

        <code class="rebase-editor__sha">{{ shortSha(commit.commitSha) }}</code>
        <span class="rebase-editor__details">
          <input
            v-if="commit.action === 'reword'"
            class="rebase-editor__message-input"
            type="text"
            :value="commit.shortMessage"
            :disabled="!canEdit"
            :aria-label="t('rebaseEditor.rewordMessage', { sha: shortSha(commit.commitSha) })"
            @input="updateMessage(index, $event)"
          >
          <strong v-else class="rebase-editor__message">{{ commit.shortMessage }}</strong>
          <span class="rebase-editor__author">{{ authorLabel(commit) }}</span>
        </span>

        <span class="rebase-editor__move-actions">
          <button
            type="button"
            class="rebase-editor__icon-command"
            :disabled="!canEdit || index === 0"
            :aria-label="t('rebaseEditor.moveUp', { sha: shortSha(commit.commitSha) })"
            :title="t('rebaseEditor.moveUp', { sha: shortSha(commit.commitSha) })"
            @click="moveAction(index, index - 1)"
          >
            <el-icon aria-hidden="true"><ArrowUp /></el-icon>
          </button>
          <button
            type="button"
            class="rebase-editor__icon-command"
            :disabled="!canEdit || index === actions.length - 1"
            :aria-label="t('rebaseEditor.moveDown', { sha: shortSha(commit.commitSha) })"
            :title="t('rebaseEditor.moveDown', { sha: shortSha(commit.commitSha) })"
            @click="moveAction(index, index + 1)"
          >
            <el-icon aria-hidden="true"><ArrowDown /></el-icon>
          </button>
        </span>
      </div>
    </div>

    <div
      v-if="applyConfirmationOpen"
      class="rebase-editor__dialog-backdrop"
    >
      <button
        type="button"
        class="dialog-backdrop-button"
        tabindex="-1"
        :aria-label="t('a11y.closeDialog')"
        @click="cancelApply"
      />
      <FocusTrapDialog
        tag="section"
        dialog-role="alertdialog"
        class="rebase-editor__dialog"
        aria-labelledby="rebase-apply-title"
        aria-describedby="rebase-apply-description"
        @close="cancelApply"
      >
        <h2 id="rebase-apply-title">{{ t("rebaseEditor.confirmApplyTitle") }}</h2>
        <p id="rebase-apply-description">
          {{ t("rebaseEditor.confirmApplyDescription", { count: actions.length }) }}
        </p>
        <div class="rebase-editor__dialog-actions">
          <button
            type="button"
            class="rebase-editor__command"
            autofocus
            :aria-label="t('common.cancel')"
            @click="cancelApply"
          >
            {{ t("common.cancel") }}
          </button>
          <button
            type="button"
            class="rebase-editor__command rebase-editor__command--primary"
            :aria-label="t('common.confirm')"
            @click="confirmApply"
          >
            <el-icon aria-hidden="true"><Check /></el-icon>
            <span>{{ t("common.confirm") }}</span>
          </button>
        </div>
      </FocusTrapDialog>
    </div>
  </section>
</template>

<style scoped>
.rebase-editor {
  display: flex;
  width: 100%;
  height: 100%;
  min-height: 360px;
  flex-direction: column;
  overflow: hidden;
  color: var(--color-text-primary, var(--el-text-color-primary));
  background: var(--color-bg-base, var(--el-bg-color));
  font-size: 12px;
}

.rebase-editor__toolbar {
  display: flex;
  min-height: 46px;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 6px 10px;
  border-bottom: 1px solid var(--color-border-subtle, var(--el-border-color));
  background: var(--color-bg-surface, var(--el-bg-color));
}

.rebase-editor__upstream {
  display: flex;
  min-width: 180px;
  max-width: 320px;
  flex: 1 1 240px;
  align-items: center;
  gap: 8px;
}

.rebase-editor__upstream > span {
  flex: 0 0 auto;
  color: var(--color-text-secondary, var(--el-text-color-secondary));
}

.rebase-editor__upstream input,
.rebase-editor__action-select select {
  min-width: 0;
  height: 28px;
  border: 1px solid var(--color-border, var(--el-border-color));
  border-radius: 3px;
  color: inherit;
  background: var(--color-bg-input, var(--el-fill-color-blank));
  font: inherit;
}

.rebase-editor__upstream input {
  width: 100%;
  padding: 0 7px;
}

.rebase-editor__commands,
.rebase-editor__move-actions,
.rebase-editor__dialog-actions {
  display: flex;
  align-items: center;
  gap: 4px;
}

.rebase-editor__command,
.rebase-editor__icon-command {
  display: inline-flex;
  height: 28px;
  align-items: center;
  justify-content: center;
  gap: 5px;
  padding: 0 8px;
  border: 1px solid var(--color-border, var(--el-border-color));
  border-radius: 3px;
  color: inherit;
  background: transparent;
  cursor: pointer;
  font: inherit;
}

.rebase-editor__icon-command {
  width: 28px;
  flex: 0 0 28px;
  padding: 0;
  border-color: transparent;
}

.rebase-editor__command:hover:not(:disabled),
.rebase-editor__icon-command:hover:not(:disabled),
.rebase-editor__command:focus-visible,
.rebase-editor__icon-command:focus-visible,
.rebase-editor__row:focus-visible {
  outline: none;
  box-shadow: inset 0 0 0 2px var(--color-primary-focus, var(--el-color-primary));
}

.rebase-editor__command--primary {
  border-color: var(--el-color-primary);
  color: var(--el-color-white);
  background: var(--el-color-primary);
}

.rebase-editor__command--danger {
  color: var(--el-color-danger);
}

.rebase-editor__command:disabled,
.rebase-editor__icon-command:disabled,
.rebase-editor__upstream input:disabled,
.rebase-editor__action-select select:disabled {
  cursor: default;
  opacity: 0.45;
}

.rebase-editor__error {
  padding: 7px 10px;
  border-bottom: 1px solid var(--el-color-danger-light-5);
  color: var(--el-color-danger);
  background: var(--el-color-danger-light-9);
}

.rebase-editor__empty {
  display: grid;
  flex: 1;
  place-items: center;
  color: var(--color-text-secondary, var(--el-text-color-secondary));
}

.rebase-editor__list {
  min-height: 0;
  flex: 1;
  overflow: auto;
}

.rebase-editor__row {
  display: grid;
  min-height: 46px;
  grid-template-columns: 22px 112px 76px minmax(140px, 1fr) 60px;
  align-items: center;
  gap: 8px;
  padding: 4px 8px;
  border-bottom: 1px solid var(--color-border-subtle, var(--el-border-color-lighter));
  cursor: grab;
}

.rebase-editor__row:hover,
.rebase-editor__row.is-focused {
  background: var(--color-bg-selected, var(--el-fill-color-light));
}

.rebase-editor__row.is-dragging {
  opacity: 0.5;
}

.rebase-editor__row.is-locked {
  cursor: default;
}

.rebase-editor__drag {
  display: inline-flex;
  color: var(--color-text-tertiary, var(--el-text-color-secondary));
}

.rebase-editor__action-select select {
  width: 100%;
  padding: 0 5px;
}

.rebase-editor__sha {
  overflow: hidden;
  color: var(--el-color-primary);
  font-family: var(--font-mono, monospace);
  text-overflow: ellipsis;
}

.rebase-editor__details {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 2px;
}

.rebase-editor__message,
.rebase-editor__author {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.rebase-editor__message {
  font-weight: 600;
}

.rebase-editor__message-input {
  width: 100%;
  min-width: 0;
  height: 26px;
  padding: 0 6px;
  border: 1px solid var(--color-border, var(--el-border-color));
  border-radius: 3px;
  color: inherit;
  background: var(--color-bg-input, var(--el-fill-color-blank));
  font: inherit;
  font-weight: 600;
}

.rebase-editor__author {
  color: var(--color-text-secondary, var(--el-text-color-secondary));
  font-size: 11px;
}

.rebase-editor__dialog-backdrop {
  position: fixed;
  z-index: 3000;
  inset: 0;
  display: grid;
  place-items: center;
  padding: 16px;
  background: rgba(0, 0, 0, 0.48);
}

.dialog-backdrop-button {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  margin: 0;
  padding: 0;
  border: 0;
  background: transparent;
  cursor: default;
}

.rebase-editor__dialog {
  position: relative;
  z-index: 1;
  width: min(420px, 100%);
  padding: 16px;
  border: 1px solid var(--color-border, var(--el-border-color));
  border-radius: 6px;
  background: var(--color-bg-elevated, var(--el-bg-color-overlay));
  box-shadow: var(--el-box-shadow-dark);
}

.rebase-editor__dialog h2 {
  margin: 0 0 8px;
  font-size: 15px;
}

.rebase-editor__dialog p {
  margin: 0 0 16px;
  color: var(--color-text-secondary, var(--el-text-color-secondary));
}

.rebase-editor__dialog-actions {
  justify-content: flex-end;
}

.sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  padding: 0;
  margin: -1px;
  overflow: hidden;
  clip: rect(0, 0, 0, 0);
  white-space: nowrap;
  border: 0;
}

.is-spinning {
  animation: rebase-editor-spin 900ms linear infinite;
}

@keyframes rebase-editor-spin {
  to {
    transform: rotate(360deg);
  }
}

@media (max-width: 960px) {
  .rebase-editor__toolbar {
    align-items: stretch;
    flex-direction: column;
  }

  .rebase-editor__upstream {
    width: 100%;
    max-width: none;
  }

  .rebase-editor__commands {
    flex-wrap: wrap;
  }

  .rebase-editor__row {
    grid-template-columns: 22px 104px 68px minmax(120px, 1fr) 60px;
  }
}

@media (prefers-reduced-motion: reduce) {
  .is-spinning {
    animation: none;
  }
}
</style>
