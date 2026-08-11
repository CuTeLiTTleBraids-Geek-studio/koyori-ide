// Presets store: manages AI preset actions from three layers (N-17).
// Loads presets with source metadata, and supports saving/deleting custom
// presets at the project or user level.
// Koyori IDE 模块 · Presets；交互服务：AI 对话（AIService）。
// 喵，这是 Koyori IDE 的 Presets 模块（前端实现）~
import { reactive, computed } from "vue";
import { aiService } from "@/api/services";
import type { PresetMeta, PresetFile, PresetWithSource } from "@/types";
import { notifyError, notifySuccess } from "@/lib/notifications";

interface PresetsState {
  // Merged preset list (builtin + user + project), ordered for display.
  presets: PresetMeta[];
  // Same as presets but with source layer info, for the manager UI.
  presetsWithSource: PresetWithSource[];
  loading: boolean;
  error: string | null;
}

export const presetsState = reactive<PresetsState>({
  presets: [],
  presetsWithSource: [],
  loading: false,
  error: null,
});

export const hasPresets = computed(() => presetsState.presets.length > 0);

/**
 * loadPresets fetches the merged preset list from the backend. Call on app
 * startup and after saving/deleting a preset to refresh the UI.
 */
export async function loadPresets(): Promise<void> {
  presetsState.loading = true;
  presetsState.error = null;
  try {
    const [merged, withSource] = await Promise.all([
      aiService.listPresets(),
      aiService.listPresetsWithSource(),
    ]);
    presetsState.presets = merged;
    presetsState.presetsWithSource = withSource;
  } catch (e: unknown) {
    presetsState.error = e instanceof Error ? e.message : String(e);
    notifyError(`Failed to load presets: ${presetsState.error}`);
  } finally {
    presetsState.loading = false;
  }
}

/**
 * getPresetPrompt fetches the instruction template for a named preset.
 */
export async function getPresetPrompt(name: string): Promise<string> {
  return aiService.getPresetPrompt(name);
}

/**
 * saveProjectPreset writes a preset to the project's .koyori-ide/presets/ directory.
 * After saving, the preset list is reloaded so the UI reflects the change.
 */
export async function saveProjectPreset(preset: PresetFile): Promise<void> {
  try {
    await aiService.saveProjectPreset(preset);
    await loadPresets();
    notifySuccess(`Saved preset "${preset.name}" to project`);
  } catch (e: unknown) {
    const msg = e instanceof Error ? e.message : String(e);
    notifyError(`Failed to save project preset: ${msg}`);
    throw e;
  }
}

/**
 * saveUserPreset writes a preset to the user-global config directory.
 * After saving, the preset list is reloaded.
 */
export async function saveUserPreset(preset: PresetFile): Promise<void> {
  try {
    await aiService.saveUserPreset(preset);
    await loadPresets();
    notifySuccess(`Saved preset "${preset.name}" to user config`);
  } catch (e: unknown) {
    const msg = e instanceof Error ? e.message : String(e);
    notifyError(`Failed to save user preset: ${msg}`);
    throw e;
  }
}

/**
 * deleteProjectPreset removes a project-level preset file and reloads the list.
 */
export async function deleteProjectPreset(name: string): Promise<void> {
  try {
    await aiService.deleteProjectPreset(name);
    await loadPresets();
    notifySuccess(`Deleted preset "${name}" from project`);
  } catch (e: unknown) {
    const msg = e instanceof Error ? e.message : String(e);
    notifyError(`Failed to delete project preset: ${msg}`);
    throw e;
  }
}

/**
 * deleteUserPreset removes a user-global preset file and reloads the list.
 */
export async function deleteUserPreset(name: string): Promise<void> {
  try {
    await aiService.deleteUserPreset(name);
    await loadPresets();
    notifySuccess(`Deleted preset "${name}" from user config`);
  } catch (e: unknown) {
    const msg = e instanceof Error ? e.message : String(e);
    notifyError(`Failed to delete user preset: ${msg}`);
    throw e;
  }
}
