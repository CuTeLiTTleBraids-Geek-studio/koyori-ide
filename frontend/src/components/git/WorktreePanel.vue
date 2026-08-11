<script setup lang="ts">
// Koyori IDE 组件 · Worktree Panel；交互服务：文件系统（FileService）。
// 喵，这是 Worktree Panel，负责 Koyori IDE 的界面呈现喵~
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { ElMessage, ElMessageBox } from "element-plus";
import {
  Brush,
  Close,
  Delete,
  FolderOpened,
  Loading,
  Lock,
  Plus,
  Rank,
  Refresh,
  Unlock,
} from "@element-plus/icons-vue";
import { fileService as defaultFileService } from "@/api/services";
import FocusTrapDialog from "@/components/common/FocusTrapDialog.vue";
import { errorMessage, isCancellationError } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";
import {
  wailsGitWorktreeService,
  type AddWorktreeOptions,
  type GitWorktreeServiceBindings,
  type WorktreeInfo,
} from "@/lib/gitWorktree";

interface RevealService {
  revealInOS(path: string): Promise<void>;
}

type BranchOption = string | { name: string; isHead?: boolean };
type SourceMode = "existing" | "new";
type RowOperation = "open" | "remove" | "lock" | "unlock" | "move";

const props = withDefaults(defineProps<{
  repoPath: string;
  branches?: readonly BranchOption[];
  service?: GitWorktreeServiceBindings;
  revealService?: RevealService | null;
}>(), {
  branches: () => [],
});

const emit = defineEmits<{
  (event: "open", path: string): void;
  (event: "changed", worktrees: WorktreeInfo[]): void;
}>();

const { t } = useI18n();
const service = computed(() => props.service ?? wailsGitWorktreeService);
const revealService = computed<RevealService | null>(() => {
  if (props.revealService === null) return null;
  return props.revealService ?? defaultFileService;
});

const worktrees = ref<WorktreeInfo[]>([]);
const loading = ref(false);
const loadError = ref("");
const addFormOpen = ref(false);
const adding = ref(false);
const pruning = ref(false);
const targetPath = ref("");
const sourceMode = ref<SourceMode>("existing");
const existingBranch = ref("");
const newBranch = ref("");
const startPoint = ref("");
const detach = ref(false);
const force = ref(false);
const rowOperations = ref<Record<string, RowOperation | undefined>>({});
const pendingRemoval = ref<WorktreeInfo | null>(null);
const removeForce = ref(false);
const primaryWorktreePath = computed(() => worktrees.value[0]?.path ?? "");

let loadGeneration = 0;
let contextGeneration = 0;
let disposed = false;

const branchNames = computed(() => {
  const names = props.branches
    .map((branch) => typeof branch === "string" ? branch : branch.name)
    .map((branch) => branch.trim())
    .filter(Boolean);
  return [...new Set(names)];
});

const canAdd = computed(() => {
  if (!props.repoPath || !targetPath.value.trim() || adding.value) return false;
  if (sourceMode.value === "new") return Boolean(newBranch.value.trim()) && !detach.value;
  return Boolean(existingBranch.value.trim());
});

function shortHead(head: string): string {
  return head ? head.slice(0, 8) : t("worktree.unknownHead");
}

function displayBranch(branch: string): string {
  return branch && branch !== "detached" ? branch : t("worktree.detached");
}

function resetAddForm(): void {
  targetPath.value = "";
  sourceMode.value = "existing";
  existingBranch.value = branchNames.value[0] ?? "";
  newBranch.value = "";
  startPoint.value = "";
  detach.value = false;
  force.value = false;
}

function toggleAddForm(): void {
  addFormOpen.value = !addFormOpen.value;
  if (!addFormOpen.value) resetAddForm();
}

function closeAddForm(): void {
  addFormOpen.value = false;
  resetAddForm();
}

function onDetachChanged(): void {
  if (detach.value) sourceMode.value = "existing";
}

function setRowOperation(path: string, operation?: RowOperation): void {
  const next = { ...rowOperations.value };
  if (operation) next[path] = operation;
  else delete next[path];
  rowOperations.value = next;
}

function rowBusy(path: string): boolean {
  return Boolean(rowOperations.value[path]);
}

function isCurrentOperationContext(
  repoPath: string,
  operationService: GitWorktreeServiceBindings,
  generation: number,
): boolean {
  return !disposed
    && contextGeneration === generation
    && props.repoPath === repoPath
    && service.value === operationService;
}

function shouldOfferForce(error: unknown): boolean {
  return /modified|untracked|dirty|not clean|local changes|use\s+--force|requires?\s+--force/i
    .test(errorMessage(error));
}

function normalizeComparablePath(path: string): string {
  const trimmed = path.trim();
  const windowsStyle = /^[a-z]:/i.test(trimmed)
    || /^\\/.test(trimmed)
    || /^\/\//.test(trimmed);
  let normalized = trimmed.replaceAll("\\", "/").replace(/\/{2,}/g, "/");
  if (windowsStyle) normalized = normalized.toLowerCase();
  return normalized.length > 1 ? normalized.replace(/\/$/, "") : normalized;
}

function isOutsideRepository(repoPath: string, targetPath: string): boolean {
  const target = targetPath.trim();
  if (!target) return false;
  const absolute = /^(?:[a-z]:[\\/]|[\\/]{1,2})/i.test(target);
  if (!absolute) {
    return target.split(/[\\/]+/).includes("..");
  }
  const repo = normalizeComparablePath(repoPath);
  const candidate = normalizeComparablePath(target);
  return candidate !== repo && !candidate.startsWith(`${repo}/`);
}

function isPrimaryOrBareWorktree(worktree: WorktreeInfo): boolean {
  return worktree.bare
    || normalizeComparablePath(worktree.path) === normalizeComparablePath(primaryWorktreePath.value);
}

function isCurrentWorktree(worktree: WorktreeInfo): boolean {
  return normalizeComparablePath(worktree.path) === normalizeComparablePath(props.repoPath);
}

function isDestructiveProtectedWorktree(worktree: WorktreeInfo): boolean {
  return isPrimaryOrBareWorktree(worktree) || isCurrentWorktree(worktree);
}

async function confirmOutsideRepository(
  repoPath: string,
  path: string,
  operation: "add" | "move",
): Promise<boolean> {
  if (!isOutsideRepository(repoPath, path)) return true;
  const actionKey = operation === "move" ? "worktree.move" : "worktree.add";
  const confirmationKey = operation === "move"
    ? "worktree.outsideMoveConfirm"
    : "worktree.outsideAddConfirm";
  try {
    await ElMessageBox.confirm(
      t(confirmationKey, { path, repo: repoPath }),
      t(actionKey),
      {
        confirmButtonText: t(actionKey),
        cancelButtonText: t("worktree.cancel"),
        type: "warning",
      },
    );
    return true;
  } catch (error: unknown) {
    if (isCancellationError(error)) return false;
    throw error;
  }
}

async function loadWorktrees(): Promise<void> {
  const generation = ++loadGeneration;
  const repoPath = props.repoPath;
  if (!repoPath) {
    worktrees.value = [];
    loadError.value = "";
    loading.value = false;
    emit("changed", []);
    return;
  }

  loading.value = true;
  loadError.value = "";
  try {
    const next = await service.value.ListWorktrees(repoPath);
    if (disposed || generation !== loadGeneration || repoPath !== props.repoPath) return;
    worktrees.value = next ?? [];
    emit("changed", worktrees.value);
  } catch (error: unknown) {
    if (disposed || generation !== loadGeneration) return;
    worktrees.value = [];
    loadError.value = t("worktree.loadFailed", { error: errorMessage(error) });
  } finally {
    if (!disposed && generation === loadGeneration) loading.value = false;
  }
}

async function submitAdd(): Promise<void> {
  if (!canAdd.value) return;

  const repoPath = props.repoPath;
  const operationService = service.value;
  const generation = contextGeneration;
  const path = targetPath.value.trim();
  const outsideRepository = isOutsideRepository(repoPath, path);
  const branchOrCommit = sourceMode.value === "new"
    ? startPoint.value.trim()
    : existingBranch.value.trim();
  const options: AddWorktreeOptions = {
    newBranch: sourceMode.value === "new" ? newBranch.value.trim() : "",
    detach: detach.value,
    force: force.value,
    allowOutsideRepository: outsideRepository,
  };

  adding.value = true;
  try {
    if (outsideRepository && !await confirmOutsideRepository(repoPath, path, "add")) return;
    if (!isCurrentOperationContext(repoPath, operationService, generation)) return;
    await operationService.AddWorktree(repoPath, path, branchOrCommit, options);
    if (!isCurrentOperationContext(repoPath, operationService, generation)) return;
    await loadWorktrees();
    if (!isCurrentOperationContext(repoPath, operationService, generation)) return;
    closeAddForm();
    ElMessage.success(t("worktree.addSuccess"));
  } catch (error: unknown) {
    if (!isCurrentOperationContext(repoPath, operationService, generation)) return;
    ElMessage.error(t("worktree.addFailed", { error: errorMessage(error) }));
  } finally {
    if (isCurrentOperationContext(repoPath, operationService, generation)) {
      adding.value = false;
    }
  }
}

async function openWorktree(worktree: WorktreeInfo): Promise<void> {
  if (rowBusy(worktree.path)) return;
  const repoPath = props.repoPath;
  if (!repoPath) return;
  const operationService = service.value;
  const generation = contextGeneration;
  setRowOperation(worktree.path, "open");
  try {
    const opener = revealService.value;
    if (opener) await opener.revealInOS(worktree.path);
    if (!isCurrentOperationContext(repoPath, operationService, generation)) return;
    emit("open", worktree.path);
  } catch (error: unknown) {
    if (!isCurrentOperationContext(repoPath, operationService, generation)) return;
    ElMessage.error(t("worktree.openFailed", { error: errorMessage(error) }));
  } finally {
    if (isCurrentOperationContext(repoPath, operationService, generation)) {
      setRowOperation(worktree.path);
    }
  }
}

async function legacyRemoveWorktree(worktree: WorktreeInfo): Promise<void> {
  if (rowBusy(worktree.path) || isDestructiveProtectedWorktree(worktree)) return;
  const repoPath = props.repoPath;
  if (!repoPath) return;
  const operationService = service.value;
  const generation = contextGeneration;
  try {
    await ElMessageBox.confirm(
      t("worktree.removeConfirm", { path: worktree.path }),
      t("worktree.removeTitle"),
      {
        confirmButtonText: t("worktree.remove"),
        cancelButtonText: t("worktree.cancel"),
        type: "warning",
      },
    );
  } catch (error: unknown) {
    if (isCancellationError(error)) return;
    if (!isCurrentOperationContext(repoPath, operationService, generation)) return;
    ElMessage.error(t("worktree.removeFailed", { error: errorMessage(error) }));
    return;
  }
  if (!isCurrentOperationContext(repoPath, operationService, generation)) return;

  setRowOperation(worktree.path, "remove");
  try {
    try {
      await operationService.RemoveWorktree(repoPath, worktree.path, false);
    } catch (removeError: unknown) {
      if (!isCurrentOperationContext(repoPath, operationService, generation)) return;
      if (!shouldOfferForce(removeError)) throw removeError;
      await ElMessageBox.confirm(
        t("worktree.forceRemoveConfirm", {
          path: worktree.path,
          error: errorMessage(removeError),
        }),
        t("worktree.forceRemoveTitle"),
        {
          confirmButtonText: t("worktree.forceRemoveAction"),
          cancelButtonText: t("worktree.cancel"),
          type: "warning",
        },
      );
      if (!isCurrentOperationContext(repoPath, operationService, generation)) return;
      await operationService.RemoveWorktree(repoPath, worktree.path, true);
    }
    if (!isCurrentOperationContext(repoPath, operationService, generation)) return;
    await loadWorktrees();
    if (!isCurrentOperationContext(repoPath, operationService, generation)) return;
    ElMessage.success(t("worktree.removeSuccess"));
  } catch (error: unknown) {
    if (isCancellationError(error)) return;
    if (!isCurrentOperationContext(repoPath, operationService, generation)) return;
    ElMessage.error(t("worktree.removeFailed", { error: errorMessage(error) }));
  } finally {
    if (isCurrentOperationContext(repoPath, operationService, generation)) {
      setRowOperation(worktree.path);
    }
  }
}

function requestRemove(worktree: WorktreeInfo): void {
  if (rowBusy(worktree.path) || isDestructiveProtectedWorktree(worktree)) return;
  if (!service.value.MoveWorktree) {
    void legacyRemoveWorktree(worktree);
    return;
  }
  pendingRemoval.value = worktree;
  removeForce.value = false;
}

function cancelRemove(): void {
  pendingRemoval.value = null;
  removeForce.value = false;
}

async function confirmRemove(): Promise<void> {
  const worktree = pendingRemoval.value;
  const repoPath = props.repoPath;
  if (!worktree || !repoPath || rowBusy(worktree.path)) return;
  const operationService = service.value;
  const generation = contextGeneration;
  const forceRemoval = removeForce.value;
  setRowOperation(worktree.path, "remove");
  try {
    await operationService.RemoveWorktree(repoPath, worktree.path, forceRemoval);
    if (!isCurrentOperationContext(repoPath, operationService, generation)) return;
    cancelRemove();
    await loadWorktrees();
    if (!isCurrentOperationContext(repoPath, operationService, generation)) return;
    ElMessage.success(t("worktree.removeSuccess"));
  } catch (error: unknown) {
    if (!isCurrentOperationContext(repoPath, operationService, generation)) return;
    ElMessage.error(t("worktree.removeFailed", { error: errorMessage(error) }));
  } finally {
    if (isCurrentOperationContext(repoPath, operationService, generation)) {
      setRowOperation(worktree.path);
    }
  }
}

async function lockWorktree(worktree: WorktreeInfo): Promise<void> {
  if (rowBusy(worktree.path) || isPrimaryOrBareWorktree(worktree)) return;
  const repoPath = props.repoPath;
  if (!repoPath) return;
  const operationService = service.value;
  const generation = contextGeneration;

  let reason = "";
  try {
    const result = await ElMessageBox.prompt(
      t("worktree.lockReasonPrompt", { path: worktree.path }),
      t("worktree.lockTitle"),
      {
        confirmButtonText: t("worktree.lock"),
        cancelButtonText: t("worktree.cancel"),
        inputPlaceholder: t("worktree.lockReasonPlaceholder"),
      },
    );
    reason = result.value.trim();
  } catch (error: unknown) {
    if (isCancellationError(error)) return;
    if (!isCurrentOperationContext(repoPath, operationService, generation)) return;
    ElMessage.error(t("worktree.lockFailed", { error: errorMessage(error) }));
    return;
  }

  if (!isCurrentOperationContext(repoPath, operationService, generation)) return;
  setRowOperation(worktree.path, "lock");
  try {
    await operationService.LockWorktree(repoPath, worktree.path, reason);
    if (!isCurrentOperationContext(repoPath, operationService, generation)) return;
    await loadWorktrees();
    if (!isCurrentOperationContext(repoPath, operationService, generation)) return;
    ElMessage.success(t("worktree.lockSuccess"));
  } catch (error: unknown) {
    if (!isCurrentOperationContext(repoPath, operationService, generation)) return;
    ElMessage.error(t("worktree.lockFailed", { error: errorMessage(error) }));
  } finally {
    if (isCurrentOperationContext(repoPath, operationService, generation)) {
      setRowOperation(worktree.path);
    }
  }
}

async function unlockWorktree(worktree: WorktreeInfo): Promise<void> {
  if (rowBusy(worktree.path) || isPrimaryOrBareWorktree(worktree)) return;
  const repoPath = props.repoPath;
  if (!repoPath) return;
  const operationService = service.value;
  const generation = contextGeneration;
  setRowOperation(worktree.path, "unlock");
  try {
    await operationService.UnlockWorktree(repoPath, worktree.path);
    if (!isCurrentOperationContext(repoPath, operationService, generation)) return;
    await loadWorktrees();
    if (!isCurrentOperationContext(repoPath, operationService, generation)) return;
    ElMessage.success(t("worktree.unlockSuccess"));
  } catch (error: unknown) {
    if (!isCurrentOperationContext(repoPath, operationService, generation)) return;
    ElMessage.error(t("worktree.unlockFailed", { error: errorMessage(error) }));
  } finally {
    if (isCurrentOperationContext(repoPath, operationService, generation)) {
      setRowOperation(worktree.path);
    }
  }
}

async function moveWorktree(worktree: WorktreeInfo): Promise<void> {
  if (rowBusy(worktree.path) || isDestructiveProtectedWorktree(worktree)) return;
  const repoPath = props.repoPath;
  if (!repoPath) return;
  const operationService = service.value;
  const generation = contextGeneration;
  const move = operationService.MoveWorktree;
  if (!move) {
    ElMessage.error(t("worktree.moveFailed", { error: t("worktree.moveUnavailable") }));
    return;
  }
  setRowOperation(worktree.path, "move");
  try {
    const result = await ElMessageBox.prompt(
      t("worktree.movePrompt", { path: worktree.path }),
      t("worktree.move"),
      {
        confirmButtonText: t("worktree.move"),
        cancelButtonText: t("worktree.cancel"),
        inputPlaceholder: t("worktree.movePlaceholder"),
        inputValue: worktree.path,
        inputValidator: (value: string) => Boolean(value.trim()),
      },
    );
    const newPath = result.value.trim();
    if (!newPath || newPath === worktree.path) return;
    if (!isCurrentOperationContext(repoPath, operationService, generation)) return;
    const outsideRepository = isOutsideRepository(repoPath, newPath);
    if (
      outsideRepository
      && !await confirmOutsideRepository(repoPath, newPath, "move")
    ) {
      return;
    }
    if (!isCurrentOperationContext(repoPath, operationService, generation)) return;

    try {
      await move(repoPath, worktree.path, newPath, false, outsideRepository);
    } catch (moveError: unknown) {
      if (!isCurrentOperationContext(repoPath, operationService, generation)) return;
      if (!shouldOfferForce(moveError)) throw moveError;
      await ElMessageBox.confirm(
        t("worktree.forceMoveConfirm", { error: errorMessage(moveError) }),
        t("worktree.move"),
        {
          confirmButtonText: t("worktree.forceMoveAction"),
          cancelButtonText: t("worktree.cancel"),
          type: "warning",
        },
      );
      if (!isCurrentOperationContext(repoPath, operationService, generation)) return;
      await move(repoPath, worktree.path, newPath, true, outsideRepository);
    }

    if (!isCurrentOperationContext(repoPath, operationService, generation)) return;
    await loadWorktrees();
    if (!isCurrentOperationContext(repoPath, operationService, generation)) return;
    ElMessage.success(t("worktree.moveSuccess"));
  } catch (error: unknown) {
    if (isCancellationError(error)) return;
    if (!isCurrentOperationContext(repoPath, operationService, generation)) return;
    ElMessage.error(t("worktree.moveFailed", { error: errorMessage(error) }));
  } finally {
    if (isCurrentOperationContext(repoPath, operationService, generation)) {
      setRowOperation(worktree.path);
    }
  }
}

async function legacyPruneWorktrees(
  repoPath: string,
  operationService: GitWorktreeServiceBindings,
  generation: number,
): Promise<void> {
  try {
    await ElMessageBox.confirm(
      t("worktree.pruneConfirm"),
      t("worktree.pruneTitle"),
      {
        confirmButtonText: t("worktree.prune"),
        cancelButtonText: t("worktree.cancel"),
        type: "warning",
      },
    );
  } catch (error: unknown) {
    if (isCancellationError(error)) return;
    if (!isCurrentOperationContext(repoPath, operationService, generation)) return;
    ElMessage.error(t("worktree.pruneFailed", { error: errorMessage(error) }));
    return;
  }
  if (!isCurrentOperationContext(repoPath, operationService, generation)) return;

  pruning.value = true;
  try {
    await operationService.PruneWorktrees(repoPath, false);
    if (!isCurrentOperationContext(repoPath, operationService, generation)) return;
    await loadWorktrees();
    if (!isCurrentOperationContext(repoPath, operationService, generation)) return;
    ElMessage.success(t("worktree.pruneSuccess"));
  } catch (error: unknown) {
    if (!isCurrentOperationContext(repoPath, operationService, generation)) return;
    ElMessage.error(t("worktree.pruneFailed", { error: errorMessage(error) }));
  } finally {
    if (isCurrentOperationContext(repoPath, operationService, generation)) {
      pruning.value = false;
    }
  }
}

async function pruneWorktrees(): Promise<void> {
  if (!props.repoPath || pruning.value) return;
  const repoPath = props.repoPath;
  const operationService = service.value;
  const generation = contextGeneration;
  if (!operationService.MoveWorktree) {
    await legacyPruneWorktrees(repoPath, operationService, generation);
    return;
  }
  pruning.value = true;
  try {
    const preview = await operationService.PruneWorktrees(repoPath, true);
    if (!isCurrentOperationContext(repoPath, operationService, generation)) return;
    if (preview.length === 0) {
      ElMessage.info(t("worktree.pruneEmpty"));
      return;
    }
    await ElMessageBox.confirm(
      t("worktree.prunePreview", { entries: preview.map((entry) => `- ${entry}`).join("\n") }),
      t("worktree.pruneTitle"),
      {
        confirmButtonText: t("worktree.prune"),
        cancelButtonText: t("worktree.cancel"),
        type: "warning",
      },
    );
    if (!isCurrentOperationContext(repoPath, operationService, generation)) return;
    await operationService.PruneWorktrees(repoPath, false);
    if (!isCurrentOperationContext(repoPath, operationService, generation)) return;
    await loadWorktrees();
    if (!isCurrentOperationContext(repoPath, operationService, generation)) return;
    ElMessage.success(t("worktree.pruneSuccess"));
  } catch (error: unknown) {
    if (isCancellationError(error)) return;
    if (!isCurrentOperationContext(repoPath, operationService, generation)) return;
    ElMessage.error(t("worktree.pruneFailed", { error: errorMessage(error) }));
  } finally {
    if (isCurrentOperationContext(repoPath, operationService, generation)) {
      pruning.value = false;
    }
  }
}

watch(
  () => [props.repoPath, props.service] as const,
  () => {
    contextGeneration += 1;
    rowOperations.value = {};
    adding.value = false;
    pruning.value = false;
    cancelRemove();
    closeAddForm();
    void loadWorktrees();
  },
  { immediate: true, flush: "sync" },
);

watch(branchNames, (names) => {
  if (!names.includes(existingBranch.value)) existingBranch.value = names[0] ?? "";
  if (startPoint.value && !names.includes(startPoint.value)) startPoint.value = "";
}, { immediate: true });

onBeforeUnmount(() => {
  disposed = true;
  loadGeneration += 1;
});
</script>

<template>
  <section class="worktree-panel" :aria-label="t('worktree.panelAria')">
    <header class="worktree-panel__header">
      <h2 class="worktree-panel__title">{{ t("worktree.title") }}</h2>
      <div class="worktree-panel__toolbar">
        <button
          type="button"
          class="worktree-panel__command"
          :disabled="!repoPath || loading || pruning"
          :aria-label="t('worktree.pruneAria')"
          :aria-busy="pruning"
          @click="pruneWorktrees"
        >
          <Loading v-if="pruning" class="worktree-panel__spin" aria-hidden="true" />
          <Brush v-else aria-hidden="true" />
          <span>{{ t("worktree.prune") }}</span>
        </button>
        <button
          type="button"
          class="worktree-panel__command worktree-panel__command--primary"
          :disabled="!repoPath"
          :aria-label="t('worktree.addAria')"
          :aria-expanded="addFormOpen"
          @click="toggleAddForm"
        >
          <Close v-if="addFormOpen" aria-hidden="true" />
          <Plus v-else aria-hidden="true" />
          <span>{{ addFormOpen ? t("worktree.closeAddForm") : t("worktree.add") }}</span>
        </button>
        <button
          type="button"
          class="worktree-panel__icon-button"
          :disabled="!repoPath || loading"
          :aria-label="t('worktree.refreshAria')"
          :title="t('worktree.refresh')"
          @click="loadWorktrees"
        >
          <Refresh :class="{ 'worktree-panel__spin': loading }" aria-hidden="true" />
        </button>
      </div>
    </header>

    <form v-if="addFormOpen" class="worktree-panel__form" @submit.prevent="submitAdd">
      <div class="worktree-panel__form-grid">
        <label class="worktree-panel__field worktree-panel__field--wide">
          <span>{{ t("worktree.pathLabel") }}</span>
          <input
            v-model="targetPath"
            type="text"
            required
            autocomplete="off"
            :placeholder="t('worktree.pathPlaceholder')"
            :aria-label="t('worktree.pathAria')"
          />
        </label>

        <fieldset class="worktree-panel__source-mode worktree-panel__field--wide">
          <legend>{{ t("worktree.branchModeLabel") }}</legend>
          <label>
            <input v-model="sourceMode" type="radio" value="existing" />
            <span>{{ t("worktree.existingBranch") }}</span>
          </label>
          <label>
            <input v-model="sourceMode" type="radio" value="new" :disabled="detach" />
            <span>{{ t("worktree.newBranch") }}</span>
          </label>
        </fieldset>

        <label v-if="sourceMode === 'existing'" class="worktree-panel__field worktree-panel__field--wide">
          <span>{{ detach ? t("worktree.detachTargetLabel") : t("worktree.branchLabel") }}</span>
          <select
            v-model="existingBranch"
            required
            :aria-label="detach ? t('worktree.detachTargetAria') : t('worktree.branchAria')"
          >
            <option value="" disabled>{{ t("worktree.selectBranch") }}</option>
            <option v-for="branch in branchNames" :key="branch" :value="branch">
              {{ branch }}
            </option>
          </select>
        </label>

        <template v-else>
          <label class="worktree-panel__field">
            <span>{{ t("worktree.newBranchLabel") }}</span>
            <input
              v-model="newBranch"
              type="text"
              required
              autocomplete="off"
              :placeholder="t('worktree.newBranchPlaceholder')"
              :aria-label="t('worktree.newBranchAria')"
            />
          </label>
          <label class="worktree-panel__field">
            <span>{{ t("worktree.startPointLabel") }}</span>
            <select v-model="startPoint" :aria-label="t('worktree.startPointAria')">
              <option value="">{{ t("worktree.currentHead") }}</option>
              <option v-for="branch in branchNames" :key="branch" :value="branch">
                {{ branch }}
              </option>
            </select>
          </label>
        </template>

        <div class="worktree-panel__options worktree-panel__field--wide">
          <label>
            <input v-model="detach" type="checkbox" @change="onDetachChanged" />
            <span>{{ t("worktree.detachLabel") }}</span>
          </label>
          <label>
            <input v-model="force" type="checkbox" />
            <span>{{ t("worktree.forceLabel") }}</span>
          </label>
        </div>
      </div>

      <footer class="worktree-panel__form-actions">
        <button
          type="button"
          class="worktree-panel__command"
          :aria-label="t('worktree.cancelAddAria')"
          @click="closeAddForm"
        >
          {{ t("worktree.cancel") }}
        </button>
        <button
          type="submit"
          class="worktree-panel__command worktree-panel__command--primary"
          :disabled="!canAdd"
          :aria-label="t('worktree.submitAddAria')"
          :aria-busy="adding"
        >
          <Loading v-if="adding" class="worktree-panel__spin" aria-hidden="true" />
          <Plus v-else aria-hidden="true" />
          <span>{{ adding ? t("worktree.adding") : t("worktree.add") }}</span>
        </button>
      </footer>
    </form>

    <div v-if="!repoPath" class="worktree-panel__state" role="status">
      {{ t("worktree.noRepository") }}
    </div>
    <div v-else-if="loading && worktrees.length === 0" class="worktree-panel__state" role="status" aria-live="polite">
      <Loading class="worktree-panel__state-icon worktree-panel__spin" aria-hidden="true" />
      <span>{{ t("worktree.loading") }}</span>
    </div>
    <div v-else-if="loadError" class="worktree-panel__state worktree-panel__state--error" role="alert">
      <span>{{ loadError }}</span>
      <button
        type="button"
        class="worktree-panel__command"
        :aria-label="t('worktree.retryAria')"
        @click="loadWorktrees"
      >
        <Refresh aria-hidden="true" />
        <span>{{ t("worktree.retry") }}</span>
      </button>
    </div>
    <div v-else-if="worktrees.length === 0" class="worktree-panel__state" role="status">
      {{ t("worktree.empty") }}
    </div>

    <div
      v-else
      class="worktree-panel__table"
      role="table"
      :aria-label="t('worktree.listAria')"
      :aria-busy="loading"
    >
      <div class="worktree-panel__table-header" role="rowgroup">
        <div class="worktree-panel__header-row" role="row">
          <div role="columnheader">{{ t("worktree.pathLabel") }}</div>
          <div role="columnheader">{{ t("worktree.branchLabel") }}</div>
          <div role="columnheader">{{ t("worktree.head") }}</div>
          <div role="columnheader">{{ t("worktree.status") }}</div>
          <div class="worktree-panel__actions-heading" role="columnheader">
            {{ t("worktree.actions") }}
          </div>
        </div>
      </div>
      <div class="worktree-panel__table-body" role="rowgroup">
        <div
          v-for="worktree in worktrees"
          :key="worktree.path"
          class="worktree-panel__row"
          role="row"
        >
          <div class="worktree-panel__identity" role="cell">
            <FolderOpened class="worktree-panel__folder" aria-hidden="true" />
            <span class="worktree-panel__path" :title="worktree.path">
              {{ worktree.path }}
            </span>
          </div>
          <div class="worktree-panel__cell" role="cell">
            {{ displayBranch(worktree.branch) }}
          </div>
          <div class="worktree-panel__cell" role="cell">
            <code :title="worktree.head">{{ shortHead(worktree.head) }}</code>
          </div>
          <div class="worktree-panel__status" role="cell">
            <span v-if="worktree.bare" class="worktree-panel__badge">
              {{ t("worktree.bare") }}
            </span>
            <span
              v-if="worktree.locked"
              class="worktree-panel__badge worktree-panel__badge--locked"
              :title="worktree.locked"
            >
              {{ t("worktree.locked") }}
            </span>
            <span
              v-if="worktree.prunable"
              class="worktree-panel__badge worktree-panel__badge--warning"
            >
              {{ t("worktree.prunable") }}
            </span>
            <span v-if="!worktree.bare && !worktree.locked && !worktree.prunable">-</span>
          </div>
          <div class="worktree-panel__row-actions" role="cell">
            <Loading
              v-if="rowBusy(worktree.path)"
              class="worktree-panel__row-progress worktree-panel__spin"
              aria-hidden="true"
            />
            <button
              type="button"
              class="worktree-panel__icon-button"
              :disabled="rowBusy(worktree.path)"
              :aria-label="t('worktree.openAria', { path: worktree.path })"
              :title="t('worktree.open')"
              @click="openWorktree(worktree)"
            >
              <FolderOpened aria-hidden="true" />
            </button>
            <button
              v-if="worktree.locked"
              type="button"
              class="worktree-panel__icon-button"
              :disabled="rowBusy(worktree.path) || isPrimaryOrBareWorktree(worktree)"
              :aria-label="t('worktree.unlockAria', { path: worktree.path })"
              :title="t('worktree.unlock')"
              @click="unlockWorktree(worktree)"
            >
              <Unlock aria-hidden="true" />
            </button>
            <button
              v-else
              type="button"
              class="worktree-panel__icon-button"
              :disabled="rowBusy(worktree.path) || isPrimaryOrBareWorktree(worktree)"
              :aria-label="t('worktree.lockAria', { path: worktree.path })"
              :title="t('worktree.lock')"
              @click="lockWorktree(worktree)"
            >
              <Lock aria-hidden="true" />
            </button>
            <button
              type="button"
              class="worktree-panel__icon-button"
              :disabled="rowBusy(worktree.path)
                || isDestructiveProtectedWorktree(worktree)
                || !service.MoveWorktree"
              :aria-label="t('worktree.moveAria', { path: worktree.path })"
              :title="t('worktree.move')"
              @click="moveWorktree(worktree)"
            >
              <Rank aria-hidden="true" />
            </button>
            <button
              type="button"
              class="worktree-panel__icon-button worktree-panel__icon-button--danger"
              :disabled="rowBusy(worktree.path) || isDestructiveProtectedWorktree(worktree)"
              :aria-label="t('worktree.removeAria', { path: worktree.path })"
              :title="t('worktree.remove')"
              @click="requestRemove(worktree)"
            >
              <Delete aria-hidden="true" />
            </button>
          </div>
        </div>
      </div>
    </div>

    <div
      v-if="pendingRemoval"
      class="worktree-panel__dialog-backdrop"
    >
      <button
        type="button"
        class="dialog-backdrop-button"
        tabindex="-1"
        :aria-label="t('a11y.closeDialog')"
        @click="cancelRemove"
      />
      <FocusTrapDialog
        class="worktree-panel__dialog"
        :aria-label="t('worktree.removeTitle')"
        @close="cancelRemove"
      >
        <h3>{{ t("worktree.removeTitle") }}</h3>
        <p>{{ t("worktree.removeConfirm", { path: pendingRemoval.path }) }}</p>
        <code class="worktree-panel__dialog-path">{{ pendingRemoval.path }}</code>
        <label class="worktree-panel__dialog-option">
          <input v-model="removeForce" type="checkbox" />
          <span>{{ t("worktree.forceRemoveAction") }}</span>
        </label>
        <footer class="worktree-panel__form-actions">
          <button
            type="button"
            class="worktree-panel__command"
            :aria-label="t('worktree.cancel')"
            @click="cancelRemove"
          >
            {{ t("worktree.cancel") }}
          </button>
          <button
            type="button"
            class="worktree-panel__command worktree-panel__command--danger"
            :disabled="rowBusy(pendingRemoval.path)"
            :aria-busy="rowBusy(pendingRemoval.path)"
            :aria-label="t('worktree.removeAria', { path: pendingRemoval.path })"
            @click="confirmRemove"
          >
            <Loading
              v-if="rowBusy(pendingRemoval.path)"
              class="worktree-panel__spin"
              aria-hidden="true"
            />
            <Delete v-else aria-hidden="true" />
            <span>{{ removeForce ? t("worktree.forceRemoveAction") : t("worktree.remove") }}</span>
          </button>
        </footer>
      </FocusTrapDialog>
    </div>
  </section>
</template>

<style scoped>
.worktree-panel {
  position: relative;
  display: flex;
  flex-direction: column;
  min-width: 0;
  height: 100%;
  color: var(--color-text-primary);
  background: var(--color-bg-surface);
  font-family: var(--font-sans);
}

.worktree-panel__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  min-height: 44px;
  padding: 7px 10px;
  border-bottom: 1px solid var(--color-border-default);
}

.worktree-panel__title {
  min-width: 0;
  margin: 0;
  overflow: hidden;
  color: var(--color-text-primary);
  font-size: 13px;
  font-weight: 600;
  line-height: 20px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.worktree-panel__toolbar,
.worktree-panel__row-actions,
.worktree-panel__form-actions,
.worktree-panel__options {
  display: flex;
  align-items: center;
  gap: 6px;
}

.worktree-panel__command,
.worktree-panel__icon-button {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 5px;
  height: 28px;
  padding: 0 8px;
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-xs, 4px);
  cursor: pointer;
  color: var(--color-text-secondary);
  background: var(--color-bg-surface-container-low);
  font: inherit;
  font-size: 12px;
  letter-spacing: 0;
}

.worktree-panel__command > svg,
.worktree-panel__icon-button > svg {
  width: 14px;
  height: 14px;
  flex: 0 0 14px;
}

.worktree-panel__icon-button {
  width: 28px;
  padding: 0;
}

.worktree-panel__command:hover:not(:disabled),
.worktree-panel__icon-button:hover:not(:disabled) {
  border-color: var(--color-border-strong);
  color: var(--color-text-primary);
  background: var(--color-bg-surface-container-high);
}

.worktree-panel__command:focus-visible,
.worktree-panel__icon-button:focus-visible,
.worktree-panel input:focus-visible,
.worktree-panel select:focus-visible {
  outline: 2px solid var(--color-primary-focus);
  outline-offset: 1px;
}

.worktree-panel__command:disabled,
.worktree-panel__icon-button:disabled {
  cursor: not-allowed;
  color: var(--color-text-disabled);
  opacity: 0.7;
}

.worktree-panel__command--primary {
  border-color: var(--color-primary);
  color: var(--color-on-primary);
  background: var(--color-primary);
}

.worktree-panel__command--primary:hover:not(:disabled) {
  border-color: var(--color-primary-focus);
  color: var(--color-on-primary);
  background: var(--color-primary-focus);
}

.worktree-panel__command--danger {
  border-color: var(--color-error);
  color: var(--color-on-primary);
  background: var(--color-error);
}

.worktree-panel__command--danger:hover:not(:disabled) {
  border-color: var(--color-error);
  color: var(--color-on-primary);
  filter: brightness(0.92);
}

.worktree-panel__icon-button--danger:hover:not(:disabled) {
  border-color: var(--color-error);
  color: var(--color-error);
  background: var(--color-error-container);
}

.worktree-panel__form {
  padding: 10px;
  border-bottom: 1px solid var(--color-border-default);
  background: var(--color-bg-surface-container-low);
}

.worktree-panel__form-grid {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
  gap: 10px;
}

.worktree-panel__field,
.worktree-panel__source-mode {
  display: flex;
  flex-direction: column;
  min-width: 0;
  gap: 5px;
  margin: 0;
  padding: 0;
  border: 0;
  color: var(--color-text-secondary);
  font-size: 11px;
}

.worktree-panel__field--wide {
  grid-column: 1 / -1;
}

.worktree-panel__field > input,
.worktree-panel__field > select {
  width: 100%;
  height: 30px;
  min-width: 0;
  padding: 0 8px;
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-xs, 4px);
  color: var(--color-text-primary);
  background: var(--color-bg-base);
  font: inherit;
  font-size: 12px;
  letter-spacing: 0;
}

.worktree-panel__source-mode {
  flex-direction: row;
  flex-wrap: wrap;
  align-items: center;
  gap: 12px;
}

.worktree-panel__source-mode legend {
  width: 100%;
  margin-bottom: 1px;
}

.worktree-panel__source-mode label,
.worktree-panel__options label {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  min-height: 24px;
  cursor: pointer;
  color: var(--color-text-secondary);
  font-size: 12px;
}

.worktree-panel__source-mode input,
.worktree-panel__options input {
  margin: 0;
  accent-color: var(--color-primary);
}

.worktree-panel__form-actions {
  justify-content: flex-end;
  margin-top: 10px;
}

.worktree-panel__state {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 10px;
  min-height: 112px;
  padding: 16px;
  color: var(--color-text-tertiary);
  font-size: 12px;
  line-height: 18px;
  text-align: center;
  overflow-wrap: anywhere;
}

.worktree-panel__state--error {
  color: var(--color-error);
}

.worktree-panel__state-icon {
  width: 20px;
  height: 20px;
}

.worktree-panel__table {
  min-height: 0;
  overflow: auto;
  color: var(--color-text-secondary);
  font-size: 12px;
}

.worktree-panel__table-header {
  position: sticky;
  z-index: 1;
  top: 0;
  background: var(--color-bg-surface-container-low);
}

.worktree-panel__row,
.worktree-panel__header-row {
  display: grid;
  grid-template-columns:
    minmax(220px, 2fr)
    minmax(110px, 1fr)
    90px
    minmax(120px, 1fr)
    154px;
  align-items: center;
  gap: 10px;
  min-width: 760px;
  min-height: 46px;
  padding: 7px 10px;
  border-bottom: 1px solid var(--color-border-subtle);
}

.worktree-panel__table-body .worktree-panel__row:hover {
  background: var(--color-bg-surface-container-low);
}

.worktree-panel__header-row {
  min-height: 34px;
  padding-top: 5px;
  padding-bottom: 5px;
  color: var(--color-text-tertiary);
  font-size: 11px;
  font-weight: 600;
}

.worktree-panel__actions-heading {
  text-align: right;
}

.worktree-panel__identity {
  display: flex;
  align-items: center;
  min-width: 0;
  gap: 8px;
}

.worktree-panel__folder {
  width: 17px;
  height: 17px;
  flex: 0 0 17px;
  color: var(--color-text-tertiary);
}

.worktree-panel__path {
  display: block;
  overflow: hidden;
  color: var(--color-text-primary);
  font-size: 12px;
  line-height: 18px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.worktree-panel__cell {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.worktree-panel__cell code {
  color: var(--color-text-tertiary);
  font-family: var(--font-mono);
  font-size: 11px;
}

.worktree-panel__status {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 5px;
  min-width: 0;
  color: var(--color-text-tertiary);
  font-size: 11px;
}

.worktree-panel__badge {
  display: inline-flex;
  align-items: center;
  min-height: 18px;
  padding: 0 5px;
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-xs, 4px);
  color: var(--color-text-secondary);
  background: var(--color-bg-surface-container);
  line-height: 16px;
}

.worktree-panel__badge--locked {
  border-color: var(--color-primary);
  color: var(--color-primary);
}

.worktree-panel__badge--warning {
  border-color: var(--color-warning);
  color: var(--color-warning);
  background: var(--color-warning-container);
}

.worktree-panel__row-actions {
  min-width: 154px;
  justify-content: flex-end;
}

.worktree-panel__row-progress {
  width: 14px;
  height: 14px;
  color: var(--color-text-tertiary);
}

.worktree-panel__dialog-backdrop {
  position: absolute;
  z-index: 10;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 16px;
  background: rgb(0 0 0 / 46%);
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

.worktree-panel__dialog {
  position: relative;
  z-index: 1;
  width: min(420px, 100%);
  max-height: 100%;
  padding: 16px;
  overflow: auto;
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-sm, 6px);
  color: var(--color-text-primary);
  background: var(--color-bg-surface);
  box-shadow: var(--shadow-lg);
}

.worktree-panel__dialog:focus-visible {
  outline: 2px solid var(--color-primary-focus);
  outline-offset: 2px;
}

.worktree-panel__dialog h3 {
  margin: 0 0 10px;
  font-size: 14px;
  line-height: 20px;
}

.worktree-panel__dialog p {
  margin: 0 0 8px;
  color: var(--color-text-secondary);
  font-size: 12px;
  line-height: 18px;
  overflow-wrap: anywhere;
}

.worktree-panel__dialog-path {
  display: block;
  max-height: 76px;
  padding: 7px 8px;
  overflow: auto;
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-xs, 4px);
  color: var(--color-text-primary);
  background: var(--color-bg-base);
  font-size: 11px;
  line-height: 16px;
  overflow-wrap: anywhere;
}

.worktree-panel__dialog-option {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  min-height: 28px;
  margin-top: 10px;
  color: var(--color-text-secondary);
  font-size: 12px;
}

.worktree-panel__dialog-option input {
  margin: 0;
  accent-color: var(--color-error);
}

.worktree-panel__spin {
  animation: worktree-panel-spin 0.8s linear infinite;
}

@keyframes worktree-panel-spin {
  to { transform: rotate(360deg); }
}

@media (max-width: 520px) {
  .worktree-panel__header {
    align-items: flex-start;
    flex-direction: column;
  }

  .worktree-panel__toolbar {
    width: 100%;
    flex-wrap: wrap;
  }

  .worktree-panel__form-grid {
    grid-template-columns: minmax(0, 1fr);
  }

  .worktree-panel__field--wide {
    grid-column: auto;
  }

  .worktree-panel__dialog-backdrop {
    align-items: flex-end;
    padding: 8px;
  }
}

@media (prefers-reduced-motion: reduce) {
  .worktree-panel__spin {
    animation-duration: 1.8s;
  }
}
</style>
