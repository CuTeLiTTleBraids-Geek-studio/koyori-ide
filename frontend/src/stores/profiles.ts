/**
 * Profile store (Plan 50). Orchestrates the backend ProfileService and
 * exposes reactive state for the settings UI. Switching a profile also
 * reloads settings so the UI reflects the new profile's values.
 */
// Koyori IDE 模块 · Profiles；交互服务：Profile（ProfileService）、布局（LayoutService）。
// 喵，这是 Koyori IDE 的 Profiles 模块（前端实现）~

import { reactive, computed } from "vue";
import { profileService, layoutService } from "@/api/services";
import { loadSettings } from "@/stores/app";
import { loadLayoutFromBackend } from "@/stores/layout";
import { errorMessage } from "@/lib/errors";
import type { ProfileInfo, ProfileExport } from "@/types";

interface ProfileStoreState {
  profiles: ProfileInfo[];
  activeProfile: string;
  isLoading: boolean;
  error: string | null;
}

export const profileStore = reactive<ProfileStoreState>({
  profiles: [],
  activeProfile: "",
  isLoading: false,
  error: null,
});

export const profiles = computed(() => profileStore.profiles);
export const activeProfileName = computed(() => profileStore.activeProfile);
export const isLoadingProfiles = computed(() => profileStore.isLoading);
export const profileLoadError = computed(() => profileStore.error);

export const hasMultipleProfiles = computed(
  () => profileStore.profiles.length > 1,
);

/**
 * Load the profile list and active profile from the backend. Called
 * on app startup and whenever the user opens the Profiles settings
 * section. Safe to call repeatedly.
 */
export async function loadProfiles(): Promise<void> {
  profileStore.isLoading = true;
  profileStore.error = null;
  try {
    const [list, active] = await Promise.all([
      profileService.listProfiles(),
      profileService.getActiveProfile(),
    ]);
    profileStore.profiles = list;
    profileStore.activeProfile = active;
  } catch (e: unknown) {
    profileStore.error = errorMessage(e);
  } finally {
    profileStore.isLoading = false;
  }
}

/**
 * Switch to a different profile. After the backend switches, reload
 * settings so the UI reflects the new profile's values. The settings
 * reload is critical — without it, the UI would show stale values
 * from the previous profile.
 *
 * Proposal C (prompt-4.md): Profile switch now reloads:
 *   1. Settings (including custom shortcuts via loadCustomShortcuts)
 *   2. Layout tree (each profile has its own layout.json)
 */
export async function switchProfile(name: string): Promise<void> {
  profileStore.error = null;
  try {
    await profileService.setActiveProfile(name);
    profileStore.activeProfile = name;
    // Mark the new active profile in the list.
    for (const p of profileStore.profiles) {
      p.active = p.name === name;
    }
    // Reload settings from the new profile's settings.json. This also
    // reloads custom shortcuts via loadCustomShortcuts() inside loadSettings.
    await loadSettings();
    // Proposal C: Reload the layout tree from the new profile's layout.json.
    await loadLayoutFromBackend(layoutService.loadLayout);
  } catch (e: unknown) {
    profileStore.error = errorMessage(e);
    throw e;
  }
}

/**
 * Create a new profile. If fromCurrent is true, the new profile
 * inherits the current profile's settings; otherwise it starts with
 * defaults. Does NOT switch to the new profile.
 */
export async function createProfile(
  name: string,
  fromCurrent: boolean,
): Promise<void> {
  profileStore.error = null;
  try {
    await profileService.createProfile(name, fromCurrent);
    await loadProfiles();
  } catch (e: unknown) {
    profileStore.error = errorMessage(e);
    throw e;
  }
}

/**
 * Delete a profile. The backend refuses to delete the default or
 * active profile.
 */
export async function deleteProfile(name: string): Promise<void> {
  profileStore.error = null;
  try {
    await profileService.deleteProfile(name);
    await loadProfiles();
  } catch (e: unknown) {
    profileStore.error = errorMessage(e);
    throw e;
  }
}

/**
 * Rename a profile. The backend refuses to rename the default profile.
 */
export async function renameProfile(
  oldName: string,
  newName: string,
): Promise<void> {
  profileStore.error = null;
  try {
    await profileService.renameProfile(oldName, newName);
    await loadProfiles();
  } catch (e: unknown) {
    profileStore.error = errorMessage(e);
    throw e;
  }
}

/**
 * Update a profile's description.
 */
export async function setProfileDescription(
  name: string,
  description: string,
): Promise<void> {
  profileStore.error = null;
  try {
    await profileService.setProfileDescription(name, description);
    await loadProfiles();
  } catch (e: unknown) {
    profileStore.error = errorMessage(e);
    throw e;
  }
}

/**
 * Export a profile as a JSON blob. Returns the ProfileExport object
 * which the caller can serialize and download as a file.
 */
export async function exportProfile(name: string): Promise<ProfileExport> {
  profileStore.error = null;
  try {
    return await profileService.exportProfile(name);
  } catch (e: unknown) {
    profileStore.error = errorMessage(e);
    throw e;
  }
}

/**
 * Import a profile from a previously exported JSON blob. Returns the
 * actual name used (may differ from the export's name if there was a
 * collision).
 */
export async function importProfile(data: ProfileExport): Promise<string> {
  profileStore.error = null;
  try {
    const name = await profileService.importProfile(data);
    await loadProfiles();
    return name;
  } catch (e: unknown) {
    profileStore.error = errorMessage(e);
    throw e;
  }
}
