<script setup lang="ts">
// Koyori IDE 组件 · Rollback Section。
// 喵，这是 Rollback Section，负责 Koyori IDE 的界面呈现喵~
/**
 * Plan 11 Task 14 — RollbackSection.vue
 *
 * 智能回滚设置分区：内嵌 SnapshotTimeline + 清理策略配置。
 *   - Step 5: 清理策略（保留最近 N 个 + 时间过期）
 *   - Step 6/7: 时间线 + 选择性回滚（SnapshotTimeline）
 *
 * 安全：本组件仅展示结构化数据，不渲染外部 HTML。
 */
import { ref } from "vue";
import { useI18n } from "@/lib/i18n";
import SnapshotTimeline from "@/components/ai-assistant/SnapshotTimeline.vue";
import { cleanupSnapshots } from "@/stores/snapshot";

const { t } = useI18n();

// Step 5: 清理策略配置
const keepN = ref(20);
const maxAgeDays = ref(7);

async function handleCleanup(): Promise<void> {
  const maxAgeMs = maxAgeDays.value * 24 * 60 * 60 * 1000;
  await cleanupSnapshots(keepN.value, maxAgeMs);
}
</script>

<template>
  <div class="rollback-section">
    <h3 class="rollback-section__title">{{ t("rollbackSection.title") }}</h3>
    <p class="rollback-section__desc">{{ t("rollbackSection.description") }}</p>

    <!-- Step 5: 清理策略 -->
    <div class="rollback-section__group">
      <h4>{{ t("rollbackSection.cleanup") }}</h4>
      <div class="rollback-section__row">
        <label class="rollback-section__label">
          <span>{{ t("rollbackSection.keepN") }}</span>
          <input v-model.number="keepN" type="number" min="0" class="rollback-section__input" />
        </label>
        <label class="rollback-section__label">
          <span>{{ t("rollbackSection.maxAgeDays") }}</span>
          <input v-model.number="maxAgeDays" type="number" min="0" class="rollback-section__input" />
        </label>
      </div>
      <button class="rollback-section__btn" @click="handleCleanup">
        {{ t("rollbackSection.runCleanup") }}
      </button>
    </div>

    <!-- Step 6/7: 时间线 + 选择性回滚 -->
    <div class="rollback-section__group">
      <h4>{{ t("rollbackSection.timeline") }}</h4>
      <div class="rollback-section__timeline">
        <SnapshotTimeline />
      </div>
    </div>
  </div>
</template>

<style scoped>
.rollback-section {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.rollback-section__title {
  margin: 0;
  font-size: 16px;
}
.rollback-section__desc {
  margin: 0;
  color: var(--el-text-color-secondary, #909399);
  font-size: 13px;
}
.rollback-section__group {
  border: 1px solid var(--el-border-color, #dcdfe6);
  border-radius: 4px;
  padding: 12px;
}
.rollback-section__group h4 {
  margin: 0 0 8px 0;
  font-size: 14px;
}
.rollback-section__row {
  display: flex;
  gap: 12px;
  margin-bottom: 8px;
}
.rollback-section__label {
  display: flex;
  flex-direction: column;
  gap: 4px;
  font-size: 12px;
  color: var(--el-text-color-regular, #606266);
}
.rollback-section__input {
  padding: 6px 8px;
  border: 1px solid var(--el-border-color, #dcdfe6);
  border-radius: 3px;
  font-size: 12px;
  background: var(--el-bg-color, #fff);
  color: var(--el-text-color-primary, #303030);
  width: 120px;
}
.rollback-section__btn {
  padding: 6px 16px;
  border: 1px solid var(--el-color-primary, #409eff);
  background: var(--el-color-primary, #409eff);
  color: #fff;
  border-radius: 3px;
  cursor: pointer;
  font-size: 12px;
}
.rollback-section__timeline {
  height: 500px;
  overflow: hidden;
  border: 1px solid var(--el-border-color, #dcdfe6);
  border-radius: 4px;
}
</style>
