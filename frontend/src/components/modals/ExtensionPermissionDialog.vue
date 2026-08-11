<script setup lang="ts">
// Koyori IDE 组件 · Extension Permission Dialog。
// 喵，这是 Extension Permission Dialog，负责 Koyori IDE 的界面呈现喵~
// G-VSC-03 / G-SEC-12: Extension permission approval dialog.
//
// Shown when a user tries to enable a Reviewed or Restricted extension
// (G-VSC-03 requirement 2, G-SEC-12 requirement 2). The dialog lists all
// requested API permissions with human-readable descriptions so the user
// can make an informed decision before granting access.
//
// Behavior:
//   - Reviewed extensions: lists permissions + an "Enable" button.
//   - Restricted extensions: shows a prominent warning banner and
//     requires an explicit confirmation checkbox before the "Enable"
//     button becomes active (the backend hard-blocks Restricted enable
//     without explicit approval —see ExtensionSecurityService).
//   - Emits "approve" with the extension ID when the user confirms, or
//     "cancel" to dismiss without enabling.

import { ref, computed, watch } from "vue";
import type {
  ExtensionSecurityInfo,
  ExtensionPermission,
} from "@/stores/extensionSecurity";
import { permissionDescription, permissionRisk } from "@/stores/extensionSecurity";
import { useI18n } from "@/lib/i18n";
import FocusTrapDialog from "@/components/common/FocusTrapDialog.vue";

const { t } = useI18n();

const props = defineProps<{
  visible: boolean;
  info: ExtensionSecurityInfo | null;
}>();

const emit = defineEmits<{
  (e: "close"): void;
  (e: "approve", extensionId: string): void;
}>();

// Restricted extensions require the user to check an explicit confirmation
// checkbox before the Enable button activates. Reviewed extensions enable
// directly (the popup is informational).
const restrictedConfirmed = ref(false);

const isRestricted = computed(
  () => props.info?.level === "restricted",
);

const isReviewed = computed(
  () => props.info?.level === "reviewed",
);

const canEnable = computed(() => {
  if (!props.info) return false;
  if (isRestricted.value) return restrictedConfirmed.value;
  return true; // Trusted/Reviewed can enable directly.
});

// Sorted permissions: highest-risk first so the user sees the dangerous
// capabilities at the top of the list.
// NOTE: Go nil slice serializes to JSON `null`, so props.info.permissions may
// be null even though the TS interface declares it non-optional. Guard with
// `?? []` to avoid "props.info.permissions is not iterable" at runtime.
const sortedPermissions = computed<{ perm: ExtensionPermission; risk: string }[]>(() => {
  if (!props.info) return [];
  const perms = props.info.permissions ?? [];
  return [...perms]
    .map((perm) => ({ perm, risk: permissionRisk(perm) }))
    .sort((a, b) => {
      const order: Record<string, number> = { high: 0, medium: 1, low: 2 };
      return (order[a.risk] ?? 3) - (order[b.risk] ?? 3);
    });
});

const levelLabel = computed(() => {
  switch (props.info?.level) {
    case "trusted":
      return t("extPerm.levelTrusted");
    case "reviewed":
      return t("extPerm.levelReviewed");
    case "restricted":
      return t("extPerm.levelRestricted");
    default:
      return t("extPerm.levelUnknown");
  }
});

function handleEnable() {
  if (!props.info || !canEnable.value) return;
  emit("approve", props.info.extensionId);
}

watch(
  () => props.visible,
  (v) => {
    if (v) restrictedConfirmed.value = false;
  },
  { immediate: true },
);
</script>

<template>
  <transition name="epd-fade">
    <div
      v-if="visible && info"
      class="epd-overlay"
    >
      <button
        type="button"
        class="dialog-backdrop-button"
        tabindex="-1"
        :aria-label="t('a11y.closeDialog')"
        @click="emit('close')"
      />
      <FocusTrapDialog
        class="epd"
        :aria-label="t('extPerm.ariaLabel', { id: info.extensionId })"
        @close="emit('close')"
      >
        <header class="epd__header">
          <h2 class="epd__title">{{ t("extPerm.title") }}</h2>
          <span class="epd__level" :class="`epd__level--${info.level}`">
            {{ levelLabel }}
          </span>
        </header>

        <div class="epd__body">
          <p class="epd__ext-name">
            <strong>{{ info.extensionId }}</strong>
          </p>

          <!-- Restricted warning banner -->
          <div v-if="isRestricted" class="epd__warning" role="alert">
            <strong>{{ t("extPerm.warningTitle") }}</strong>
            {{ t("extPerm.warningBody") }}
          </div>

          <!-- Reviewed notice -->
          <p v-if="isReviewed" class="epd__notice">
            {{ t("extPerm.reviewedNotice") }}
          </p>

          <p v-if="!info.verified" class="epd__unverified" role="alert">
            {{ t("extPerm.unverified") }}
          </p>

          <h3 class="epd__subtitle">{{ t("extPerm.requestedPermissions") }}</h3>
          <ul class="epd__perm-list">
            <li
              v-for="item in sortedPermissions"
              :key="item.perm"
              class="epd__perm"
              :class="`epd__perm--${item.risk}`"
            >
              <code class="epd__perm-id">{{ item.perm }}</code>
              <span class="epd__perm-desc">{{ permissionDescription(item.perm) }}</span>
            </li>
            <li v-if="sortedPermissions.length === 0" class="epd__perm epd__perm--none">
              {{ t("extPerm.noPermissions") }}
            </li>
          </ul>

          <!-- Restricted explicit confirmation -->
          <label v-if="isRestricted" class="epd__confirm">
            <input
              v-model="restrictedConfirmed"
              type="checkbox"
              :disabled="!info.verified"
            />
            <span>
              {{ t("extPerm.confirmLabel") }}
            </span>
          </label>
        </div>

        <footer class="epd__footer">
          <button type="button" class="epd__btn" @click="emit('close')">
            {{ t("extPerm.cancel") }}
          </button>
          <button
            type="button"
            class="epd__btn epd__btn--primary"
            :class="{ 'epd__btn--danger': isRestricted }"
            :disabled="!canEnable || !info.verified"
            @click="handleEnable"
          >
            {{ isRestricted ? t("extPerm.enableRestricted") : t("extPerm.enable") }}
          </button>
        </footer>
      </FocusTrapDialog>
    </div>
  </transition>
</template>

<style scoped>
.epd-overlay {
  position: fixed;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  background-color: rgba(0, 0, 0, 0.4);
  z-index: 1000;
  display: flex;
  justify-content: center;
  align-items: center;
  padding: 24px;
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

.epd {
  position: relative;
  z-index: 1;
  width: 540px;
  max-width: 92vw;
  max-height: 85vh;
  display: flex;
  flex-direction: column;
  background-color: var(--color-bg-surface, #1e1e1e);
  border: 1px solid var(--color-border-default, #333);
  border-radius: var(--radius-md);
  overflow: hidden;
  box-shadow: 0 8px 32px rgba(0, 0, 0, 0.3);
  outline: none;
}

.epd__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 16px 20px;
  border-bottom: 1px solid var(--color-border-subtle, #2a2a2a);
}

.epd__title {
  margin: 0;
  font-size: 15px;
  font-weight: 600;
}

.epd__level {
  font-size: 11px;
  font-weight: 600;
  padding: 3px 8px;
  border-radius: 4px;
  text-transform: uppercase;
  letter-spacing: 0.5px;
}

.epd__level--trusted {
  background: rgba(76, 175, 80, 0.15);
  color: #66bb6a;
}

.epd__level--reviewed {
  background: rgba(255, 193, 7, 0.15);
  color: #ffc107;
}

.epd__level--restricted {
  background: rgba(244, 67, 54, 0.15);
  color: #ef5350;
}

.epd__body {
  padding: 16px 20px;
  overflow-y: auto;
  flex: 1;
}

.epd__ext-name {
  margin: 0 0 12px 0;
  font-size: 14px;
}

.epd__warning {
  padding: 10px 12px;
  background: rgba(244, 67, 54, 0.1);
  border: 1px solid rgba(244, 67, 54, 0.3);
  border-radius: 6px;
  font-size: 12px;
  line-height: 1.5;
  margin-bottom: 12px;
  color: #ff8a80;
}

.epd__notice {
  padding: 10px 12px;
  background: rgba(255, 193, 7, 0.08);
  border-radius: 6px;
  font-size: 12px;
  line-height: 1.5;
  margin-bottom: 12px;
  color: var(--color-text-secondary, #aaa);
}

.epd__unverified {
  padding: 10px 12px;
  background: rgba(244, 67, 54, 0.1);
  border-radius: 6px;
  font-size: 12px;
  margin-bottom: 12px;
  color: #ff8a80;
}

.epd__subtitle {
  margin: 12px 0 8px 0;
  font-size: 12px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.5px;
  color: var(--color-text-secondary, #888);
}

.epd__perm-list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.epd__perm {
  display: flex;
  align-items: baseline;
  gap: 10px;
  padding: 8px 10px;
  border-radius: 6px;
  background: var(--color-bg-elevated, #252525);
  font-size: 12px;
  line-height: 1.4;
}

.epd__perm--high {
  border-left: 3px solid #ef5350;
}

.epd__perm--medium {
  border-left: 3px solid #ffc107;
}

.epd__perm--low {
  border-left: 3px solid #66bb6a;
}

.epd__perm--none {
  border-left: 3px solid var(--color-border-default, #444);
  color: var(--color-text-secondary, #888);
}

.epd__perm-id {
  font-family: var(--font-mono, monospace);
  font-size: 11px;
  background: rgba(255, 255, 255, 0.06);
  padding: 1px 5px;
  border-radius: 3px;
  white-space: nowrap;
  flex-shrink: 0;
}

.epd__perm-desc {
  color: var(--color-text-secondary, #aaa);
}

.epd__confirm {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  margin-top: 14px;
  padding: 10px 12px;
  background: rgba(244, 67, 54, 0.06);
  border-radius: 6px;
  font-size: 12px;
  line-height: 1.5;
  cursor: pointer;
}

.epd__confirm input {
  margin-top: 2px;
  flex-shrink: 0;
}

.epd__footer {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  padding: 12px 20px;
  border-top: 1px solid var(--color-border-subtle, #2a2a2a);
}

.epd__btn {
  padding: 7px 16px;
  font-size: 13px;
  border: 1px solid var(--color-border-default, #3a3a3a);
  border-radius: 6px;
  background: transparent;
  color: var(--color-text-primary, #e0e0e0);
  cursor: pointer;
  transition: background 0.15s ease;
}

.epd__btn:hover:not(:disabled) {
  background: var(--color-bg-elevated, #2a2a2a);
}

.epd__btn:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}

.epd__btn--primary {
  background: var(--color-accent, #3b82f6);
  border-color: var(--color-accent, #3b82f6);
  color: #fff;
}

.epd__btn--primary:hover:not(:disabled) {
  background: var(--color-accent-hover, #2563eb);
}

.epd__btn--danger {
  background: #d32f2f;
  border-color: #d32f2f;
}

.epd__btn--danger:hover:not(:disabled) {
  background: #c62828;
}

.epd-fade-enter-active,
.epd-fade-leave-active {
  transition: opacity 0.15s ease;
}

.epd-fade-enter-from,
.epd-fade-leave-to {
  opacity: 0;
}
</style>
