<script setup lang="ts">
// Koyori IDE 组件 · Setting Row。
// 喵，这是 Setting Row，负责 Koyori IDE 的界面呈现喵~
defineProps<{
  label: string;
  description?: string;
}>();
</script>

<template>
  <div class="setting-row">
    <div class="setting-row__info">
      <span class="setting-row__label">{{ label }}</span>
      <p v-if="description" class="setting-row__desc">{{ description }}</p>
    </div>
    <div class="setting-row__control">
      <slot />
    </div>
  </div>
</template>

<style scoped>
.setting-row {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  padding: 12px 0;
  border-bottom: 1px solid var(--color-divider-soft, #f0f0f0);
}
.setting-row__info {
  flex: 1;
  min-width: 0;
}
.setting-row__label {
  font-size: 14px;
  font-weight: 500;
  color: var(--color-text-primary, #1d1d1f);
  line-height: 1.4;
}
.setting-row__desc {
  font-size: 13px;
  color: var(--color-text-secondary, #666);
  margin: 4px 0 0;
  line-height: 1.4;
}
.setting-row__control {
  flex-shrink: 0;
  min-width: 180px;
  max-width: 320px;
  display: flex;
  align-items: center;
  justify-content: flex-end;
}
</style>
