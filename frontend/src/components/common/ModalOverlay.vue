<script setup lang="ts">
// Koyori IDE 组件 · Modal Overlay。
// 喵，这是 Modal Overlay，负责 Koyori IDE 的界面呈现喵~
import { computed } from "vue";
import FocusTrapDialog from "./FocusTrapDialog.vue";

defineOptions({ inheritAttrs: false });

const props = withDefaults(
  defineProps<{
    visible?: boolean;
    title?: string;
    maxWidth?: string;
  }>(),
  {
    visible: false,
    title: "",
    maxWidth: "480px",
  }
);

const emit = defineEmits<{
  close: [];
}>();

const dialogStyle = computed(() => ({
  maxWidth: props.maxWidth,
}));
</script>

<template>
  <Teleport to="body">
    <Transition name="modal-fade">
      <div
        v-if="visible"
        class="dialog-backdrop-button"
        role="presentation"
        @click.self="emit('close')"
      >
        <FocusTrapDialog
          :style="dialogStyle"
          v-bind="$attrs"
          class="modal-overlay"
          role="dialog"
          aria-modal="true"
          :aria-label="title || undefined"
          @close="emit('close')"
        >
          <slot />
        </FocusTrapDialog>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.dialog-backdrop-button {
  position: fixed;
  inset: 0;
  z-index: 2000;
  display: flex;
  align-items: center;
  justify-content: center;
  overflow: auto;
  padding: 16px;
  background: rgba(0, 0, 0, 0.5);
  overscroll-behavior: contain;
}

.modal-fade-enter-active,
.modal-fade-leave-active {
  transition: opacity var(--transition-normal, 250ms var(--ease-standard, cubic-bezier(0.4, 0, 0.2, 1)));
}
.modal-fade-enter-from,
.modal-fade-leave-to {
  opacity: 0;
}
.modal-overlay {
  width: 100%;
  max-height: 85vh;
  overflow-y: auto;
  background: var(--color-bg-elevated, #f5f5f7);
  border-radius: var(--radius-md, 11px);
  border: 1px solid var(--color-border-default, rgba(0, 0, 0, 0.08));
  box-shadow: var(--shadow-floating, 0 1px 3px rgba(0, 0, 0, 0.04));
  padding: 24px;
  color: var(--color-text-primary, #1d1d1f);
}
</style>
