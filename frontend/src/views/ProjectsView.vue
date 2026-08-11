<script setup lang="ts">
// Koyori IDE 组件 · Projects View；交互服务：文件系统（FileService）、项目（ProjectService）。
// 喵，这是 Projects View，负责 Koyori IDE 的界面呈现喵~
import { ref, onMounted } from "vue";
import { useRouter } from "vue-router";
import { Plus, FolderOpened, Delete } from "@element-plus/icons-vue";
import { fileService, projectService } from "@/api/services";
import { openProject } from "@/stores/app";
import { notifyError } from "@/lib/notifications";
import { isCancellationError } from "@/lib/errors";
import type { Project } from "@/types";
import { useI18n } from "@/lib/i18n";

const { t } = useI18n();

const router = useRouter();
const projects = ref<Project[]>([]);
const loading = ref(false);

async function loadProjects() {
  loading.value = true;
  try {
    projects.value = await projectService.getRecentProjects();
  } finally {
    loading.value = false;
  }
}

async function handleOpenFolder() {
  try {
    const path = await fileService.pickDirectory();
    if (!path) return;
    await openProject(path, path);
    await loadProjects();
    router.push("/editor");
  } catch (err) {
    if (isCancellationError(err)) return;
    notifyError(t("projects.openFolderFailed", { error: err instanceof Error ? err.message : String(err) }));
  }
}

async function handleOpenProject(project: Project) {
  try {
    await openProject(project.name, project.path);
    router.push("/editor");
  } catch (err) {
    notifyError(t("projects.openProjectFailed", { error: err instanceof Error ? err.message : String(err) }));
  }
}

async function handleRemoveProject(id: string) {
  try {
    await projectService.removeProject(id);
    await loadProjects();
  } catch (err) {
    notifyError(t("projects.removeProjectFailed", { error: err instanceof Error ? err.message : String(err) }));
  }
}

onMounted(loadProjects);
</script>

<template>
  <div class="projects-view">
    <div class="projects-header">
      <h1 class="projects-title text-display-md">{{ t("projects.title") }}</h1>
      <el-button
        type="primary"
        :icon="Plus"
        size="default"
        class="btn-primary"
        :aria-label="t('projects.openFolderAria')"
        @click="handleOpenFolder"
      >
        {{ t("projects.openFolder") }}
      </el-button>
    </div>

    <div class="projects-body">
      <!-- Empty state -->
      <div v-if="projects.length === 0 && !loading" class="projects-empty empty-state">
        <el-icon :size="48" class="projects-empty-icon empty-state__icon">
          <FolderOpened />
        </el-icon>
        <h2 class="projects-empty-title text-body-strong empty-state__title">{{ t("projects.emptyTitle") }}</h2>
        <p class="projects-empty-desc text-caption empty-state__desc">{{ t("projects.emptyDesc") }}</p>
        <el-button
          size="default"
          :icon="FolderOpened"
          class="btn-outline"
          :aria-label="t('projects.openFolderAria')"
          @click="handleOpenFolder"
        >
          {{ t("projects.openFolder") }}
        </el-button>
      </div>

      <!-- Project list -->
      <div v-else class="projects-list">
        <div
          v-for="project in projects"
          :key="project.id"
          class="project-card"
          :class="{ 'project-card--missing': !project.exists }"
          role="button"
          tabindex="0"
          :aria-label="t('projects.openProjectAria', { name: project.name })"
          @click="project.exists && handleOpenProject(project)"
          @keydown.enter="project.exists && handleOpenProject(project)"
          @keydown.space.prevent="project.exists && handleOpenProject(project)"
        >
          <el-icon :size="24" class="project-card__icon">
            <FolderOpened />
          </el-icon>
          <div class="project-card__info">
            <span class="project-card__name">{{ project.name }}</span>
            <span class="project-card__path">{{ project.path }}</span>
            <span v-if="!project.exists" class="project-card__missing-label">{{ t("projects.missing") }}</span>
          </div>
          <button
            type="button"
            class="project-card__remove"
            :aria-label="t('projects.removeAria')"
            @click.stop="handleRemoveProject(project.id)"
          >
            <el-icon :size="14"><Delete /></el-icon>
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.projects-view {
  display: flex;
  flex-direction: column;
  width: 100%;
  height: 100%;
  background-color: var(--color-bg-base);
  color: var(--color-on-background);
  padding: 24px 32px;
  overflow-y: auto;
}

/* Header */
.projects-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-shrink: 0;
  margin-bottom: 24px;
}

.projects-title {
  /* 字号/字重由 .text-display-md 工具类提供 */
  margin: 0;
  color: var(--color-text-primary);
}

/* Buttons — P4-24: 改用 Element Plus CSS 变量覆盖，移除 !important */
.btn-primary {
  --el-button-bg-color: var(--color-primary);
  --el-button-border-color: var(--color-primary);
  --el-button-text-color: var(--color-bg-base);
  --el-button-hover-bg-color: var(--color-primary);
  --el-button-hover-border-color: var(--color-primary);
  --el-button-hover-text-color: var(--color-bg-base);
  --el-button-active-bg-color: var(--color-primary);
  --el-button-active-border-color: var(--color-primary);
  --el-button-active-text-color: var(--color-bg-base);
  border-radius: var(--radius-lg);
}

.btn-outline {
  --el-button-bg-color: transparent;
  --el-button-border-color: var(--color-outline);
  --el-button-text-color: var(--color-on-surface-variant);
  --el-button-hover-bg-color: transparent;
  --el-button-hover-border-color: var(--color-primary);
  --el-button-hover-text-color: var(--color-primary);
  --el-button-active-bg-color: transparent;
  --el-button-active-border-color: var(--color-primary);
  --el-button-active-text-color: var(--color-primary);
  border-radius: var(--radius-lg);
}

/* Body */
.projects-body {
  flex: 1;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 48px;
}

/* Empty State */
.projects-empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 12px;
  text-align: center;
  padding: 24px;
}

.projects-empty-icon {
  color: var(--color-text-tertiary);
  opacity: 0.5;
}

.projects-empty-title {
  /* 字号/字重由 .text-body-strong 工具类提供 */
  margin: 0;
  color: var(--color-text-secondary);
}

.projects-empty-desc {
  /* 字号由 .text-caption 工具类提供 */
  margin: 0;
  color: var(--color-text-tertiary);
}

.projects-list {
  display: flex;
  flex-direction: column;
  gap: 4px;
  width: 100%;
  max-width: 680px;
}

.project-card {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px 16px;
  border: 1px solid var(--color-border-subtle);
  border-radius: var(--radius-md);
  background-color: var(--color-bg-elevated);
  cursor: pointer;
  transition: border-color var(--transition-fast),
              background-color var(--transition-fast);
}

.project-card:hover {
  border-color: var(--color-primary);
  background-color: var(--color-bg-overlay);
}

.project-card__icon {
  color: var(--color-primary);
  flex-shrink: 0;
}

.project-card__info {
  display: flex;
  flex-direction: column;
  gap: 2px;
  flex: 1;
  min-width: 0;
}

.project-card__name {
  font-size: 14px;
  font-weight: 500;
  color: var(--color-text-primary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.project-card__path {
  font-size: 11px;
  color: var(--color-text-tertiary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.project-card__remove {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  border: none;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-text-tertiary);
  cursor: pointer;
  flex-shrink: 0;
  transition: color var(--transition-fast),
              background-color var(--transition-fast);
}

.project-card__remove:hover {
  color: var(--color-error);
  background-color: color-mix(in srgb, var(--color-error) 10%, transparent);
}

.project-card--missing {
  opacity: 0.5;
  cursor: not-allowed;
}

.project-card--missing:hover {
  border-color: var(--color-border-subtle);
  background-color: var(--color-bg-elevated);
}

.project-card__missing-label {
  font-size: 10px;
  color: var(--color-error);
  font-weight: 500;
}

@media (max-width: 600px) {
  .projects-view {
    padding: 16px;
  }
}
</style>
