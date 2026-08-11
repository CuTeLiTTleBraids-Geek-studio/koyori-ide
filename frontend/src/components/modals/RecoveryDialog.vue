<script setup lang="ts">
// Koyori IDE 组件 · Recovery Dialog。
// 喵，这是 Recovery Dialog，负责 Koyori IDE 的界面呈现喵~
import { computed, ref } from "vue";
import ModalOverlay from "@/components/common/ModalOverlay.vue";
import { useI18n } from "@/lib/i18n";
import {
  discardAllRecoverable,
  finishRecovery,
  recoveryState,
  resolveRecoverableFile,
  undoLastRecoveryDecision,
} from "@/stores/recovery";
import type { RecoverableFile } from "@/types";

const { t } = useI18n();
const busyFile = ref("");
const discarding = ref(false);
const finishing = ref(false);
const undoing = ref(false);

const hasFiles = computed(() => recoveryState.scan.files.length > 0);

function fileKey(file: RecoverableFile): string {
  return `${file.windowId}\u0000${file.path}`;
}

function fileName(path: string): string {
  return path.split(/[/\\]/).pop() || path;
}

async function resolve(
  file: RecoverableFile,
  decision: "restore" | "merge" | "keep-disk",
): Promise<void> {
  busyFile.value = fileKey(file);
  try {
    await resolveRecoverableFile(file, decision);
  } finally {
    busyFile.value = "";
  }
}

async function undoDecision(): Promise<void> {
  undoing.value = true;
  try {
    await undoLastRecoveryDecision();
  } finally {
    undoing.value = false;
  }
}

async function continueRecovery(): Promise<void> {
  finishing.value = true;
  try {
    await finishRecovery();
  } finally {
    finishing.value = false;
  }
}

async function discardAll(): Promise<void> {
  discarding.value = true;
  try {
    await discardAllRecoverable();
  } finally {
    discarding.value = false;
  }
}
</script>

<template>
  <ModalOverlay
    :visible="recoveryState.visible"
    :title="t('recovery.title')"
    max-width="720px"
  >
    <section class="recovery-dialog" aria-live="polite">
      <header class="recovery-dialog__header">
        <div>
          <h2>{{ t("recovery.title") }}</h2>
          <p>{{ t("recovery.subtitle") }}</p>
        </div>
      </header>

      <p v-if="recoveryState.error" class="recovery-dialog__error" role="alert">
        {{ t("recovery.error", { message: recoveryState.error }) }}
      </p>

      <p v-if="recoveryState.scanning" class="recovery-dialog__state">
        {{ t("recovery.scanning") }}
      </p>

      <ul v-else-if="hasFiles" class="recovery-dialog__files">
        <li
          v-for="file in recoveryState.scan.files"
          :key="fileKey(file)"
          class="recovery-dialog__file"
        >
          <div class="recovery-dialog__file-info">
            <div class="recovery-dialog__file-heading">
              <strong :title="file.path">{{ fileName(file.path) }}</strong>
              <span
                class="recovery-dialog__status"
                :class="`recovery-dialog__status--${file.status}`"
              >
                {{ t(`recovery.status.${file.status}`) }}
              </span>
            </div>
            <code :title="file.path">{{ file.path }}</code>
            <p>{{ t(`recovery.description.${file.status}`) }}</p>
          </div>

          <div class="recovery-dialog__actions">
            <button
              type="button"
              class="recovery-dialog__restore"
              :disabled="busyFile !== '' || discarding"
              @click="resolve(file, file.status === 'clean' ? 'restore' : 'merge')"
            >
              {{ file.status === "clean"
                ? t("recovery.restoreBuffer")
                : t("recovery.mergeBuffer") }}
            </button>
            <button
              v-if="file.status !== 'clean'"
              type="button"
              class="recovery-dialog__keep-disk"
              :disabled="busyFile !== '' || discarding"
              @click="resolve(file, 'keep-disk')"
            >
              {{ file.status === "missing"
                ? t("recovery.keepDeleted")
                : t("recovery.keepDisk") }}
            </button>
          </div>
        </li>
      </ul>

      <div v-if="recoveryState.scan.corrupt.length" class="recovery-dialog__corrupt" role="alert">
        <h3>{{ t("recovery.corruptTitle") }}</h3>
        <ul>
          <li v-for="record in recoveryState.scan.corrupt" :key="record.file">
            <code>{{ record.file }}</code>: {{ record.reason }}
          </li>
        </ul>
      </div>

      <p
        v-if="!recoveryState.scanning && !hasFiles && !recoveryState.scan.corrupt.length"
        class="recovery-dialog__state"
      >
        {{ recoveryState.error ? t("recovery.scanFailed") : t("recovery.empty") }}
      </p>

      <footer class="recovery-dialog__footer">
        <button
          v-if="recoveryState.decisions.length"
          type="button"
          class="recovery-dialog__undo"
          :disabled="busyFile !== '' || discarding || undoing || finishing"
          @click="undoDecision"
        >
          {{ undoing ? t("recovery.undoing") : t("recovery.undo") }}
        </button>
        <button
          v-if="hasFiles"
          type="button"
          class="recovery-dialog__discard-all"
          :disabled="busyFile !== '' || discarding"
          @click="discardAll"
        >
          {{ discarding ? t("recovery.discarding") : t("recovery.discardAll") }}
        </button>
        <button
          v-else
          type="button"
          class="recovery-dialog__continue"
          :disabled="busyFile !== '' || discarding || undoing || finishing"
          @click="continueRecovery"
        >
          {{ finishing ? t("recovery.finishing") : t("recovery.continue") }}
        </button>
      </footer>
    </section>
  </ModalOverlay>
</template>

<style scoped>
.recovery-dialog {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.recovery-dialog__header h2,
.recovery-dialog__corrupt h3 {
  margin: 0;
  font-size: 16px;
  letter-spacing: 0;
}

.recovery-dialog__header p,
.recovery-dialog__file-info p,
.recovery-dialog__state {
  margin: 5px 0 0;
  color: var(--color-text-secondary, #8a8a8f);
  font-size: 13px;
  line-height: 1.45;
}

.recovery-dialog__error,
.recovery-dialog__corrupt {
  margin: 0;
  padding: 10px 12px;
  border-left: 3px solid var(--color-error, #d84a4a);
  background: color-mix(in srgb, var(--color-error, #d84a4a) 10%, transparent);
  color: var(--color-text-primary, #e7e7e7);
  font-size: 12px;
  line-height: 1.45;
}

.recovery-dialog__files,
.recovery-dialog__corrupt ul {
  list-style: none;
  margin: 0;
  padding: 0;
}

.recovery-dialog__files {
  display: flex;
  flex-direction: column;
  gap: 8px;
  max-height: min(52vh, 520px);
  overflow-y: auto;
}

.recovery-dialog__file {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: 12px;
  align-items: center;
  padding: 12px;
  border: 1px solid var(--color-border-default, #3a3a3a);
  border-radius: 6px;
  background: var(--color-bg-surface, #232323);
}

.recovery-dialog__file-info {
  min-width: 0;
}

.recovery-dialog__file-heading {
  display: flex;
  gap: 8px;
  align-items: center;
}

.recovery-dialog__file-heading strong,
.recovery-dialog__file-info code {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.recovery-dialog__file-info code {
  display: block;
  margin-top: 4px;
  color: var(--color-text-secondary, #999);
  font-size: 11px;
}

.recovery-dialog__status {
  flex: 0 0 auto;
  padding: 2px 6px;
  border-radius: 4px;
  font-size: 11px;
  font-weight: 600;
}

.recovery-dialog__status--clean {
  color: #72c48d;
  background: rgba(44, 148, 82, 0.15);
}

.recovery-dialog__status--conflict {
  color: #f0b85a;
  background: rgba(197, 126, 17, 0.16);
}

.recovery-dialog__status--missing {
  color: #e47575;
  background: rgba(199, 54, 54, 0.14);
}

.recovery-dialog__actions,
.recovery-dialog__footer {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}

.recovery-dialog button {
  min-height: 32px;
  padding: 6px 11px;
  border: 1px solid var(--color-border-default, #464646);
  border-radius: 5px;
  background: var(--color-bg-elevated, #303030);
  color: var(--color-text-primary, #ededed);
  font: inherit;
  font-size: 12px;
  cursor: pointer;
}

.recovery-dialog button:hover:not(:disabled) {
  background: var(--color-bg-hover, #3a3a3a);
}

.recovery-dialog button:disabled {
  opacity: 0.5;
  cursor: default;
}

.recovery-dialog__restore,
.recovery-dialog__continue {
  border-color: var(--color-accent, #3c8ddb) !important;
  background: var(--color-accent, #347fca) !important;
  color: #fff !important;
}

.recovery-dialog__corrupt ul {
  margin-top: 6px;
}

.recovery-dialog__footer {
  padding-top: 4px;
  border-top: 1px solid var(--color-border-subtle, #303030);
}

@media (max-width: 640px) {
  .recovery-dialog__file {
    grid-template-columns: 1fr;
  }

  .recovery-dialog__actions {
    justify-content: stretch;
  }

  .recovery-dialog__actions button {
    flex: 1 1 0;
  }
}
</style>
