/**
 * Plugin store (Plan 49) — orchestrates plugin discovery, activation,
 * and the enabled/disabled state. Wraps the backend PluginService and
 * the frontend pluginRegistry.
 */
// Koyori IDE 模块 · Plugins；交互服务：插件（PluginService）。
// 喵，这是 Koyori IDE 的 Plugins 模块（前端实现）~

import { reactive, computed } from "vue";
import { pluginService } from "@/api/services";
import {
  syncPlugins,
  activateOnStartup,
  activatePlugin,
  deactivatePlugin,
  enablePlugin,
  disablePlugin,
  listPluginStates,
  unregisterPluginContributions,
  type PluginActivationState,
} from "@/lib/pluginRegistry";
import type { PluginInfo } from "@/types";
import { errorMessage } from "@/lib/errors";
import { appState } from "@/stores/app";

interface PluginStoreState {
  plugins: PluginInfo[];
  activations: PluginActivationState[];
  loading: boolean;
  error: string | null;
}

export const pluginStore = reactive<PluginStoreState>({
  plugins: [],
  activations: [],
  loading: false,
  error: null,
});

export const installedPlugins = computed(() => pluginStore.plugins);
export const pluginActivations = computed(() => pluginStore.activations);
export const isLoadingPlugins = computed(() => pluginStore.loading);
export const pluginLoadError = computed(() => pluginStore.error);

/**
 * Load the installed-plugin list from the backend and sync the
 * registry. Optionally activate plugins with onStartup events. Safe
 * to call repeatedly (e.g. on project switch).
 */
export async function loadPlugins(
  shouldApply: () => boolean = () => true,
): Promise<void> {
  pluginStore.loading = true;
  pluginStore.error = null;
  try {
    const projectRoot = appState.currentProject ?? "";
    const plugins = await pluginService.listPlugins(projectRoot);
    if (!shouldApply()) return;
    pluginStore.plugins = plugins;
    syncPlugins(plugins);
    pluginStore.activations = listPluginStates();
  } catch (e: unknown) {
    if (shouldApply()) pluginStore.error = errorMessage(e);
  } finally {
    if (shouldApply()) pluginStore.loading = false;
  }
}

/**
 * Activate all plugins whose activation events include "onStartup".
 * Called once after the IDE finishes booting (e.g. in App.vue
 * onMounted, after loadSettings + loadPlugins).
 */
export async function activateStartupPlugins(
  shouldApply: () => boolean = () => true,
): Promise<void> {
  await activateOnStartup(shouldApply);
  if (shouldApply()) pluginStore.activations = listPluginStates();
}

/**
 * Toggle a plugin's enabled state. Persists to the backend, then
 * activates or deactivates the plugin locally.
 */
export async function togglePluginEnabled(name: string, enabled: boolean): Promise<void> {
  try {
    await pluginService.setPluginEnabled(name, enabled);
    if (enabled) {
      await enablePlugin(name);
    } else {
      await disablePlugin(name);
    }
    // Refresh the local plugin info list so .enabled reflects the new state.
    const projectRoot = appState.currentProject ?? "";
    const plugins = await pluginService.listPlugins(projectRoot);
    pluginStore.plugins = plugins;
    pluginStore.activations = listPluginStates();
  } catch (e: unknown) {
    pluginStore.error = errorMessage(e);
  }
}

/**
 * Manually activate a plugin (e.g. user clicked "Activate" in the UI).
 */
export async function activatePluginByName(name: string): Promise<void> {
  await activatePlugin(name);
  pluginStore.activations = listPluginStates();
}

/**
 * Manually deactivate a plugin (e.g. user clicked "Deactivate").
 */
export async function deactivatePluginByName(name: string): Promise<void> {
  await deactivatePlugin(name);
  pluginStore.activations = listPluginStates();
}

/**
 * Proposal G (prompt-4.md): Retry loading a plugin that previously
 * failed activation. Deactivates (best-effort cleanup) then activates.
 * Used by the "Retry loading" button in PluginsView when status is "error".
 */
export async function retryPluginActivation(name: string): Promise<void> {
  // Best-effort deactivate: if the plugin is in error state, this clears
  // any partial contributions before re-activating.
  try {
    await deactivatePlugin(name);
  } catch {
    // M-30: deactivate 抛错时,强制清理旧插件的 contributions(命令/视图),
    // 避免残留的注册项在重新激活后产生重复注册或指向已失效的插件。
    unregisterPluginContributions(name);
  }
  await activatePlugin(name);
  pluginStore.activations = listPluginStates();
}

/**
 * Reload the plugin list and re-sync the registry. Useful after
 * installing or removing a plugin from disk.
 */
export async function reloadPlugins(): Promise<void> {
  await loadPlugins();
  await activateStartupPlugins();
}
