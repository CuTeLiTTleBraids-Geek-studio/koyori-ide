<script setup lang="ts">
// Koyori IDE 组件 · Plugins View。
// 喵，这是 Plugins View，负责 Koyori IDE 的界面呈现喵~
import { ref, onMounted, computed } from "vue";
import { Search } from "@element-plus/icons-vue";
import { ElMessage } from "element-plus";
import {
  installedPlugins,
  pluginActivations,
  isLoadingPlugins,
  pluginLoadError,
  loadPlugins,
  togglePluginEnabled,
  reloadPlugins,
  retryPluginActivation,
} from "@/stores/plugins";
import type { PluginInfo } from "@/types";
import { errorMessage } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";

const { t } = useI18n();

type PluginCategory = "all" | "enabled" | "disabled" | "user" | "project";

const searchQuery = ref("");
const activeCategory = ref<PluginCategory>("all");

const categories = computed<{ key: PluginCategory; label: string }[]>(() => [
  { key: "all", label: t("plugins.categoryAll") },
  { key: "enabled", label: t("plugins.categoryEnabled") },
  { key: "disabled", label: t("plugins.categoryDisabled") },
  { key: "user", label: t("plugins.categoryUser") },
  { key: "project", label: t("plugins.categoryProject") },
]);

function selectCategory(key: PluginCategory) {
  activeCategory.value = key;
}

const activationMap = computed(() => {
  const m = new Map<string, { status: string; error?: string }>();
  for (const a of pluginActivations.value) {
    m.set(a.name, { status: a.status, error: a.error });
  }
  return m;
});

const filteredPlugins = computed(() => {
  const q = searchQuery.value.trim().toLowerCase();
  return installedPlugins.value.filter((p) => {
    if (q) {
      const hay = `${p.manifest.name} ${p.manifest.description ?? ""} ${p.manifest.author ?? ""}`.toLowerCase();
      if (!hay.includes(q)) return false;
    }
    switch (activeCategory.value) {
      case "enabled":
        return p.enabled;
      case "disabled":
        return !p.enabled;
      case "user":
        return p.source === "user";
      case "project":
        return p.source === "project";
      default:
        return true;
    }
  });
});

function statusLabel(name: string): string {
  const s = activationMap.value.get(name);
  if (!s) return t("plugins.statusUnknown");
  return s.status;
}

function statusColor(name: string): string {
  const s = activationMap.value.get(name);
  if (!s) return "info";
  switch (s.status) {
    case "activated":
      return "success";
    case "activating":
      return "warning";
    case "error":
      return "danger";
    case "disabled":
      return "info";
    case "loaded":
      return "info";
    default:
      return "info";
  }
}

async function handleToggle(p: PluginInfo, enabled: boolean) {
  try {
    await togglePluginEnabled(p.manifest.name, enabled);
  } catch (e: unknown) {
    ElMessage.error(errorMessage(e));
  }
}

async function handleReload() {
  await reloadPlugins();
}

/**
 * Proposal G (prompt-4.md): Retry loading a plugin that previously
 * failed activation. Shows a success/error toast after the retry.
 */
const retryingPlugins = ref<Set<string>>(new Set());

async function handleRetry(name: string) {
  retryingPlugins.value.add(name);
  try {
    await retryPluginActivation(name);
    const s = activationMap.value.get(name);
    if (s?.status === "activated") {
      ElMessage.success(t("plugins.retrySuccess", { name }));
    } else if (s?.status === "error") {
      ElMessage.error(t("plugins.retryFailed", { error: s.error ?? "" }));
    }
  } catch (e: unknown) {
    ElMessage.error(errorMessage(e));
  } finally {
    retryingPlugins.value.delete(name);
  }
}

onMounted(() => {
  void loadPlugins();
});
</script>

<template>
  <div class="plugins-view">
    <!-- Sidebar -->
    <aside class="plugins-sidebar">
      <ul class="plugins-category-list">
        <li
          v-for="cat in categories"
          :key="cat.key"
          class="plugins-category-item"
        >
          <button
            type="button"
            class="plugins-category-btn"
            :class="{ 'is-active': activeCategory === cat.key }"
            :aria-label="cat.label"
            :aria-current="activeCategory === cat.key ? 'page' : undefined"
            @click="selectCategory(cat.key)"
          >
            {{ cat.label }}
          </button>
        </li>
      </ul>
    </aside>

    <!-- Main Content -->
    <div class="plugins-main">
      <!-- Header -->
      <div class="plugins-header">
        <h1 class="plugins-title text-display-md">{{ t("plugins.title") }}</h1>
        <div class="plugins-header-actions">
          <el-input
            v-model="searchQuery"
            :placeholder="t('plugins.searchPlaceholder')"
            :prefix-icon="Search"
            size="default"
            class="plugins-search"
            clearable
            :aria-label="t('plugins.searchAria')"
          />
          <el-button size="default" @click="handleReload" :loading="isLoadingPlugins">
            {{ t("plugins.reload") }}
          </el-button>
        </div>
      </div>

      <!-- Error banner -->
      <div v-if="pluginLoadError" class="plugins-error">
        {{ t("plugins.loadFailed", { error: pluginLoadError }) }}
      </div>

      <!-- Body -->
      <div class="plugins-body">
        <!-- Loading state -->
        <div v-if="isLoadingPlugins && installedPlugins.length === 0" class="plugins-empty">
          <p class="plugins-empty-desc text-caption">{{ t("plugins.loading") }}</p>
        </div>

        <!-- Empty state -->
        <div v-else-if="filteredPlugins.length === 0" class="plugins-empty empty-state">
          <el-icon :size="48" class="plugins-empty-icon empty-state__icon">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5">
              <path d="M14.7 6.3a1 1 0 0 0 0 1.4l1.6 1.6a1 1 0 0 0 1.4 0l3.77-3.77a6 6 0 0 1-7.94 7.94l-6.91 6.91a2.12 2.12 0 0 1-3-3l6.91-6.91a6 6 0 0 1 7.94-7.94l-3.76 3.76z" />
            </svg>
          </el-icon>
          <h2 class="plugins-empty-title text-body-strong empty-state__title">{{ t("plugins.noPlugins") }}</h2>
          <p class="plugins-empty-desc text-caption empty-state__desc">
            {{ t("plugins.emptyDescPrefix") }} <code>&lt;config&gt;/koyori-ide/plugins/&lt;name&gt;/</code>
            {{ t("plugins.emptyDescMiddle") }} <code>.koyori-ide/plugins/&lt;name&gt;/</code> {{ t("plugins.emptyDescSuffix") }} <code>plugin.json</code> {{ t("plugins.emptyDescEnd") }}
          </p>
        </div>

        <!-- Plugin list -->
        <div v-else class="plugins-list">
          <article
            v-for="p in filteredPlugins"
            :key="p.manifest.name"
            class="plugin-card"
            :class="{ 'is-disabled': !p.enabled }"
          >
            <header class="plugin-card__header">
              <div class="plugin-card__title">
                <h3 class="plugin-card__name">{{ p.manifest.name }}</h3>
                <span class="plugin-card__version">v{{ p.manifest.version }}</span>
                <!-- G-VSC-04: source labeling —native plugins take priority
                     and run in a stricter sandbox than VS Code extensions. -->
                <el-tag size="small" type="success">
                  {{ t("plugins.nativeBadge") }}
                </el-tag>
                <el-tag size="small" :type="p.source === 'project' ? 'warning' : 'info'">
                  {{ p.source }}
                </el-tag>
                <el-tag
                  v-if="!p.mainExists"
                  size="small"
                  type="danger"
                >
                  {{ t("plugins.entryMissing") }}
                </el-tag>
              </div>
              <el-switch
                :model-value="p.enabled"
                :aria-label="t('plugins.enableDisableAria', { name: p.manifest.name })"
                @change="(val: boolean) => handleToggle(p, val)"
              />
            </header>
            <p v-if="p.manifest.description" class="plugin-card__desc">
              {{ p.manifest.description }}
            </p>
            <p v-else class="plugin-card__desc plugin-card__desc--muted">
              <em>{{ t("plugins.noDescription") }}</em>
            </p>
            <footer class="plugin-card__footer">
              <span v-if="p.manifest.author" class="plugin-card__meta">
                {{ t("plugins.by", { author: p.manifest.author }) }}
              </span>
              <span class="plugin-card__meta">
                {{ t("plugins.statusLabel") }}
                <el-tag size="small" :type="statusColor(p.manifest.name)">
                  {{ statusLabel(p.manifest.name) }}
                </el-tag>
              </span>
              <span v-if="p.manifest.permissions && p.manifest.permissions.length > 0" class="plugin-card__meta">
                {{ t("plugins.permissions", { permissions: p.manifest.permissions.join(", ") }) }}
              </span>
            </footer>
            <p
              v-if="activationMap.get(p.manifest.name)?.error"
              class="plugin-card__error"
            >
              {{ t("plugins.activationError", { error: activationMap.get(p.manifest.name)?.error ?? "" }) }}
            </p>
            <div
              v-if="activationMap.get(p.manifest.name)?.status === 'error'"
              class="plugin-card__retry"
            >
              <el-button
                size="small"
                type="primary"
                plain
                :loading="retryingPlugins.has(p.manifest.name)"
                :aria-label="t('common.retry')"
                @click="handleRetry(p.manifest.name)"
              >
                {{ t("common.retry") }}
              </el-button>
            </div>
          </article>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.plugins-view {
  display: flex;
  width: 100%;
  height: 100%;
  background-color: var(--color-bg-base);
  color: var(--color-on-background, #f0f0f0);
  overflow: hidden;
}

/* Sidebar */
.plugins-sidebar {
  flex-shrink: 0;
  width: 200px;
  padding: 12px;
  border-right: 1px solid var(--color-border-default, #2a2a2c);
  background-color: var(--color-bg-surface-container-low, #fafafc);
  overflow-y: auto;
}

.plugins-category-list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.plugins-category-item {
  margin: 0;
  padding: 0;
}

.plugins-category-btn {
  display: block;
  width: 100%;
  padding: 8px 12px;
  border: none;
  border-radius: var(--radius-lg);
  background: transparent;
  color: var(--color-on-surface-variant, #b0b0b0);
  font-size: 0.875rem;
  font-family: inherit;
  text-align: left;
  cursor: pointer;
  transition: background-color var(--transition-fast, 150ms) ease,
              color var(--transition-fast, 150ms) ease;
}

.plugins-category-btn:hover {
  background-color: var(--color-bg-surface-container, #f5f5f7);
  color: var(--color-on-background, #f0f0f0);
}

.plugins-category-btn.is-active {
  background-color: var(--color-primary-container, #1a2a40);
  color: var(--color-primary, #a0c4ff);
  font-weight: 500;
}

.plugins-category-btn:focus-visible {
  /* P3-20: 内容区域统一 outline-offset:2px + --color-primary-focus */
  outline: 2px solid var(--color-primary-focus);
  outline-offset: 2px;
}

/* Main Content */
.plugins-main {
  flex: 1;
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

/* Header */
.plugins-header {
  flex-shrink: 0;
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 20px 24px 16px;
  gap: 16px;
  border-bottom: 1px solid var(--color-border-default, #2a2a2c);
}

.plugins-title {
  /* P2-11: 字号/字重由.text-display-md 工具类提供*/
  margin: 0;
  color: var(--color-text-primary);
  flex-shrink: 0;
}

.plugins-header-actions {
  display: flex;
  align-items: center;
  gap: 12px;
}

.plugins-search {
  width: 240px;
}

/* Body */
.plugins-body {
  flex: 1;
  display: flex;
  flex-direction: column;
  overflow-y: auto;
  padding: 16px 24px;
}

.plugins-error {
  padding: 12px 16px;
  margin-bottom: 12px;
  border-radius: var(--radius-md);
  background-color: var(--color-error-bg, rgba(244, 67, 54, 0.1));
  border: 1px solid var(--color-error-border, #b00020);
  color: var(--color-error, #ff6b6b);
  font-size: 0.875rem;
}

/* Empty State */
.plugins-empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 12px;
  text-align: center;
  padding: 48px 24px;
  margin: auto;
}

.plugins-empty-icon {
  color: var(--color-text-tertiary, #707070);
  opacity: 0.5;
}

.plugins-empty-title {
  /* P2-11: 字号/字重由.text-body-strong 工具类提供*/
  margin: 0;
  color: var(--color-text-secondary);
}

.plugins-empty-desc {
  /* P2-11: 字号由.text-caption 工具类提供*/
  margin: 0;
  color: var(--color-text-tertiary);
  max-width: 480px;
  line-height: 1.5;
}

.plugins-empty-desc code {
  font-family: var(--font-mono);
  background: var(--color-bg-surface-container, #f5f5f7);
  padding: 2px 6px;
  border-radius: 4px;
  font-size: 0.8125rem;
}

/* Plugin list */
.plugins-list {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.plugin-card {
  padding: 16px;
  border: 1px solid var(--color-border-default, #2a2a2c);
  border-radius: var(--radius-lg);
  background: var(--color-bg-surface-container-low, #fafafc);
  transition: border-color var(--transition-fast, 150ms) ease;
}

.plugin-card:hover {
  border-color: var(--color-primary, #a0c4ff);
}

.plugin-card.is-disabled {
  opacity: 0.65;
}

.plugin-card__header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 8px;
}

.plugin-card__title {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.plugin-card__name {
  font-size: 1rem;
  font-weight: 600;
  margin: 0;
  color: var(--color-text-primary, #f0f0f0);
}

.plugin-card__version {
  font-family: var(--font-mono);
  font-size: 0.8125rem;
  color: var(--color-text-secondary, #a0a0a0);
}

.plugin-card__desc {
  font-size: 0.875rem;
  color: var(--color-text-secondary, #a0a0a0);
  margin: 0 0 8px 0;
  line-height: 1.5;
}

.plugin-card__desc--muted {
  font-style: italic;
}

.plugin-card__footer {
  display: flex;
  align-items: center;
  gap: 16px;
  flex-wrap: wrap;
  font-size: 0.8125rem;
  color: var(--color-text-tertiary, #707070);
}

.plugin-card__meta {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}

.plugin-card__error {
  margin: 8px 0 0 0;
  padding: 8px 12px;
  border-radius: var(--radius-sm);
  background: var(--color-error-bg, rgba(244, 67, 54, 0.1));
  color: var(--color-error, #ff6b6b);
  font-size: 0.8125rem;
  font-family: var(--font-mono);
  word-break: break-word;
}

.plugin-card__retry {
  margin-top: 8px;
}

@media (max-width: 768px) {
  .plugins-sidebar {
    width: 160px;
  }

  .plugins-header {
    flex-direction: column;
    align-items: flex-start;
    padding: 16px;
  }

  .plugins-search {
    width: 100%;
  }
}
</style>
