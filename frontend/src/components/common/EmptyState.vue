<script setup lang="ts">
// Koyori IDE 组件 · Empty State。
// 喵，这是 Empty State，负责 Koyori IDE 的界面呈现喵~
defineProps<{
  icon?: string;
  title: string;
  description?: string;
  actionLabel?: string;
}>();

defineEmits<{
  action: [];
}>();
</script>

<template>
  <div class="empty-state">
    <div v-if="icon" class="empty-state__icon">{{ icon }}</div>
    <p class="empty-state__title">{{ title }}</p>
    <p v-if="description" class="empty-state__desc">{{ description }}</p>
    <button
      v-if="actionLabel"
      type="button"
      class="empty-state__action"
      @click="$emit('action')"
    >
      {{ actionLabel }}
    </button>
    <slot />
  </div>
</template>

<style scoped>
.empty-state {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 8px;
  padding: 32px 16px;
  color: var(--color-text-secondary, #666);
  text-align: center;
}
.empty-state__icon {
  font-size: 48px;
  margin-bottom: 4px;
  opacity: 0.4;
  line-height: 1;
}
.empty-state__title {
  font-size: 14px;
  font-weight: 500;
  color: var(--color-text-primary, #333);
  margin: 0;
}
.empty-state__desc {
  font-size: 13px;
  color: var(--color-text-secondary, #888);
  margin: 0;
  max-width: 280px;
}
.empty-state__action {
  margin-top: 12px;
  padding: 8px 16px;
  border-radius: var(--radius-sm, 8px);
  background: var(--color-primary, #0066cc);
  color: var(--color-on-primary, #fff);
  border: none;
  font-size: 13px;
  font-weight: 500;
  cursor: pointer;
  transition: background var(--transition-fast, 150ms var(--ease-standard, cubic-bezier(0.4, 0, 0.2, 1)));
}
.empty-state__action:hover {
  background: var(--color-primary-focus, #0071e3);
}
</style>
