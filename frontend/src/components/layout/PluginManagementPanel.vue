<script setup lang="ts">
// Koyori IDE 组件 · Plugin Management Panel。
// 喵，这是 Plugin Management Panel，负责 Koyori IDE 的界面呈现喵~
// G-VSC-04: Unified plugin/extension management panel.
//
// Renders two sections so native koyori-ide plugins and VS Code extensions
// coexist visibly:
//   1. "Native Plugins" —from the plugin store (pluginRegistry). Sandboxed,
//      permission-gated, higher priority. Shows a "Native Plugin" badge.
//   2. "VS Code Extensions" —from the vscodeExtensions registry (populated by
//      a future Extension Host bridge). Shows a "VS Code Extension" badge plus
//      a security-level tag.
//
// Both sections show name, description, version, status and an enable/disable
// switch. The panel is self-contained: it reads the native plugin store and
// the vscode extension registry directly so it can be mounted anywhere (e.g.
// the SidePanel "extensions" tab or a dedicated route).
import { computed } from "vue";
import { ElMessage } from "element-plus";
import {
  installedPlugins,
  pluginActivations,
  togglePluginEnabled,
} from "@/stores/plugins";
import {
  listVscodeExtensions,
  setVscodeExtensionEnabled,
} from "@/lib/vscodeExtensions";
import { errorMessage } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";
import type { PluginInfo, VscodeExtensionInfo, VscodeExtensionSecurityLevel } from "@/types";

const { t } = useI18n();

// Native plugin activation status lookup by name.
const nativeActivationMap = computed(() => {
  const m = new Map<string, { status: string; error?: string }>();
  for (const a of pluginActivations.value) {
    m.set(a.name, { status: a.status, error: a.error });
  }
  return m;
});

const nativePlugins = computed<PluginInfo[]>(() => installedPlugins.value);
const vscodeExtensions = computed<VscodeExtensionInfo[]>(() => listVscodeExtensions());

function nativeStatus(name: string): string {
  return nativeActivationMap.value.get(name)?.status ?? t("plugins.statusUnknown");
}

function nativeStatusType(name: string): "success" | "warning" | "danger" | "info" {
  const s = nativeActivationMap.value.get(name)?.status;
  switch (s) {
    case "activated":
      return "success";
    case "activating":
      return "warning";
    case "error":
      return "danger";
    default:
      return "info";
  }
}

async function handleNativeToggle(p: PluginInfo, enabled: boolean) {
  try {
    await togglePluginEnabled(p.manifest.name, enabled);
  } catch (e: unknown) {
    ElMessage.error(errorMessage(e));
  }
}

function handleVscodeToggle(ext: VscodeExtensionInfo, enabled: boolean) {
  setVscodeExtensionEnabled(ext.id, enabled);
}

// G-VSC-04: security-level tag type for VS Code extensions.
// Levels mirror the G-VSC-03 extensionSecurity store:
//   trusted →success (least risk), reviewed →warning, restricted →danger.
function securityTagType(
  level: VscodeExtensionSecurityLevel,
): "success" | "warning" | "danger" {
  switch (level) {
    case "trusted":
      return "success";
    case "reviewed":
      return "warning";
    case "restricted":
      return "danger";
  }
}

function securityLabel(level: VscodeExtensionSecurityLevel): string {
  switch (level) {
    case "trusted":
      return t("plugins.securityTrusted");
    case "reviewed":
      return t("plugins.securityReviewed");
    case "restricted":
      return t("plugins.securityRestricted");
  }
}
</script>

<template>
  <div class="plugin-mgmt">
    <!-- Native Plugins section -->
    <section class="plugin-mgmt__section">
      <header class="plugin-mgmt__section-header">
        <h3 class="plugin-mgmt__section-title">{{ t("plugins.nativeSectionTitle") }}</h3>
        <el-tag size="small" type="success" class="plugin-mgmt__source-badge">
          {{ t("plugins.nativeBadge") }}
        </el-tag>
      </header>
      <p class="plugin-mgmt__section-desc">{{ t("plugins.nativeSectionDesc") }}</p>

      <div v-if="nativePlugins.length === 0" class="plugin-mgmt__empty">
        {{ t("plugins.noPlugins") }}
      </div>
      <ul v-else class="plugin-mgmt__list">
        <li
          v-for="p in nativePlugins"
          :key="p.manifest.name"
          class="plugin-mgmt__item"
          :class="{ 'is-disabled': !p.enabled }"
        >
          <div class="plugin-mgmt__item-main">
            <div class="plugin-mgmt__item-title">
              <span class="plugin-mgmt__name">{{ p.manifest.name }}</span>
              <span class="plugin-mgmt__version">v{{ p.manifest.version }}</span>
              <el-tag size="small" :type="nativeStatusType(p.manifest.name)">
                {{ nativeStatus(p.manifest.name) }}
              </el-tag>
            </div>
            <p v-if="p.manifest.description" class="plugin-mgmt__desc">
              {{ p.manifest.description }}
            </p>
            <p v-else class="plugin-mgmt__desc plugin-mgmt__desc--muted">
              <em>{{ t("plugins.noDescription") }}</em>
            </p>
          </div>
          <el-switch
            :model-value="p.enabled"
            :aria-label="t('plugins.enableDisableAria', { name: p.manifest.name })"
            @change="(val: boolean) => handleNativeToggle(p, val)"
          />
        </li>
      </ul>
    </section>

    <!-- VS Code Extensions section -->
    <section class="plugin-mgmt__section">
      <header class="plugin-mgmt__section-header">
        <h3 class="plugin-mgmt__section-title">{{ t("plugins.vscodeSectionTitle") }}</h3>
        <el-tag size="small" type="primary" class="plugin-mgmt__source-badge">
          {{ t("plugins.vscodeBadge") }}
        </el-tag>
      </header>
      <p class="plugin-mgmt__section-desc">{{ t("plugins.vscodeSectionDesc") }}</p>

      <div v-if="vscodeExtensions.length === 0" class="plugin-mgmt__empty">
        {{ t("plugins.vscodeEmpty") }}
      </div>
      <ul v-else class="plugin-mgmt__list">
        <li
          v-for="ext in vscodeExtensions"
          :key="ext.id"
          class="plugin-mgmt__item"
          :class="{ 'is-disabled': !ext.enabled }"
        >
          <div class="plugin-mgmt__item-main">
            <div class="plugin-mgmt__item-title">
              <span class="plugin-mgmt__name">{{ ext.displayName ?? ext.name }}</span>
              <span class="plugin-mgmt__version">v{{ ext.version }}</span>
              <el-tag size="small" :type="ext.isActive ? 'success' : 'info'">
                {{ ext.isActive ? "active" : "inactive" }}
              </el-tag>
              <!-- G-VSC-04: security level badge -->
              <el-tag size="small" :type="securityTagType(ext.securityLevel)">
                {{ t("plugins.securityLevel") }}: {{ securityLabel(ext.securityLevel) }}
              </el-tag>
            </div>
            <p v-if="ext.description" class="plugin-mgmt__desc">{{ ext.description }}</p>
            <p v-else class="plugin-mgmt__desc plugin-mgmt__desc--muted">
              <em>{{ t("plugins.noDescription") }}</em>
            </p>
            <p v-if="ext.publisher" class="plugin-mgmt__publisher">
              {{ t("plugins.by", { author: ext.publisher }) }}
            </p>
          </div>
          <el-switch
            :model-value="ext.enabled"
            :aria-label="t('plugins.enableDisableAria', { name: ext.name })"
            @change="(val: boolean) => handleVscodeToggle(ext, val)"
          />
        </li>
      </ul>
    </section>
  </div>
</template>

<style scoped>
.plugin-mgmt {
  display: flex;
  flex-direction: column;
  gap: 20px;
  height: 100%;
  overflow-y: auto;
  padding: 12px 8px;
}

.plugin-mgmt__section {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.plugin-mgmt__section-header {
  display: flex;
  align-items: center;
  gap: 8px;
}

.plugin-mgmt__section-title {
  margin: 0;
  font-size: 0.9375rem;
  font-weight: 600;
  color: var(--color-text-primary, #f0f0f0);
}

.plugin-mgmt__source-badge {
  flex-shrink: 0;
}

.plugin-mgmt__section-desc {
  margin: 0 0 4px 0;
  font-size: 0.75rem;
  line-height: 1.45;
  color: var(--color-text-tertiary, #707070);
}

.plugin-mgmt__empty {
  padding: 12px;
  font-size: 0.8125rem;
  color: var(--color-text-tertiary, #707070);
  text-align: center;
  border: 1px dashed var(--color-border-subtle, rgba(255, 255, 255, 0.1));
  border-radius: var(--radius-md);
}

.plugin-mgmt__list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.plugin-mgmt__item {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
  padding: 10px 12px;
  border: 1px solid var(--color-border-default, #2a2a2c);
  border-radius: var(--radius-md);
  background: var(--color-bg-surface-container-low, #161616);
  transition: border-color var(--transition-fast, 150ms) ease;
}

.plugin-mgmt__item:hover {
  border-color: var(--color-primary, #a0c4ff);
}

.plugin-mgmt__item.is-disabled {
  opacity: 0.65;
}

.plugin-mgmt__item-main {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.plugin-mgmt__item-title {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}

.plugin-mgmt__name {
  font-size: 0.875rem;
  font-weight: 600;
  color: var(--color-text-primary, #f0f0f0);
}

.plugin-mgmt__version {
  font-family: var(--font-mono);
  font-size: 0.75rem;
  color: var(--color-text-secondary, #a0a0a0);
}

.plugin-mgmt__desc {
  margin: 0;
  font-size: 0.8125rem;
  color: var(--color-text-secondary, #a0a0a0);
  line-height: 1.45;
}

.plugin-mgmt__desc--muted {
  font-style: italic;
}

.plugin-mgmt__publisher {
  margin: 0;
  font-size: 0.75rem;
  color: var(--color-text-tertiary, #707070);
}
</style>
