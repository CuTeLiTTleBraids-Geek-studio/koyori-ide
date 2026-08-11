/**
 * Tests for the profile store (Plan 50).
 *
 * The store orchestrates the backend ProfileService and reloads settings
 * after a profile switch. These tests mock the service layer and the
 * settings loader to verify the orchestration logic without touching
 * the filesystem.
 */

import { describe, it, expect, beforeEach, vi } from "vitest";

vi.mock("@wailsio/runtime", () => ({
  Events: { On: vi.fn() },
}));

vi.mock("@/api/services", () => ({
  profileService: {
    listProfiles: vi.fn(),
    getActiveProfile: vi.fn(),
    setActiveProfile: vi.fn(),
    createProfile: vi.fn(),
    deleteProfile: vi.fn(),
    renameProfile: vi.fn(),
    setProfileDescription: vi.fn(),
    exportProfile: vi.fn(),
    importProfile: vi.fn(),
  },
  layoutService: {
    loadLayout: vi.fn(),
    saveLayout: vi.fn(),
  },
}));

const { loadSettingsMock, loadLayoutFromBackendMock } = vi.hoisted(() => ({
  loadSettingsMock: vi.fn(),
  loadLayoutFromBackendMock: vi.fn(),
}));
vi.mock("@/stores/app", () => ({
  loadSettings: loadSettingsMock,
}));
vi.mock("@/stores/layout", () => ({
  loadLayoutFromBackend: loadLayoutFromBackendMock,
}));

import {
  profileStore,
  loadProfiles,
  switchProfile,
  createProfile,
  deleteProfile,
  renameProfile,
  setProfileDescription,
  exportProfile,
  importProfile,
} from "./profiles";
import { profileService } from "@/api/services";
import type { ProfileInfo } from "@/types";

function makeProfile(overrides: Partial<ProfileInfo> = {}): ProfileInfo {
  return {
    name: "default",
    active: true,
    ...overrides,
  };
}

describe("profile store", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    profileStore.profiles = [];
    profileStore.activeProfile = "";
    profileStore.isLoading = false;
    profileStore.error = null;
  });

  describe("loadProfiles", () => {
    it("loads profiles and active profile name", async () => {
      const list = [
        makeProfile({ name: "default", active: true }),
        makeProfile({ name: "work", active: false }),
      ];
      (profileService.listProfiles as any).mockResolvedValue(list);
      (profileService.getActiveProfile as any).mockResolvedValue("default");

      await loadProfiles();

      expect(profileStore.profiles).toEqual(list);
      expect(profileStore.activeProfile).toBe("default");
      expect(profileStore.isLoading).toBe(false);
      expect(profileStore.error).toBeNull();
    });

    it("sets error on failure", async () => {
      (profileService.listProfiles as any).mockRejectedValue(new Error("backend down"));
      (profileService.getActiveProfile as any).mockResolvedValue("default");

      await loadProfiles();

      expect(profileStore.error).toBe("backend down");
      expect(profileStore.isLoading).toBe(false);
    });
  });

  describe("switchProfile", () => {
    it("switches active profile and reloads settings", async () => {
      profileStore.profiles = [
        makeProfile({ name: "default", active: true }),
        makeProfile({ name: "work", active: false }),
      ];
      (profileService.setActiveProfile as any).mockResolvedValue(undefined);
      loadSettingsMock.mockResolvedValue(undefined);
      loadLayoutFromBackendMock.mockResolvedValue(undefined);

      await switchProfile("work");

      expect(profileService.setActiveProfile).toHaveBeenCalledWith("work");
      expect(profileStore.activeProfile).toBe("work");
      // The list should reflect the new active profile.
      expect(profileStore.profiles[0].active).toBe(false);
      expect(profileStore.profiles[1].active).toBe(true);
      // Settings should be reloaded from the new profile.
      expect(loadSettingsMock).toHaveBeenCalledTimes(1);
      // Proposal C: layout should also be reloaded from the new profile.
      expect(loadLayoutFromBackendMock).toHaveBeenCalledTimes(1);
      expect(profileStore.error).toBeNull();
    });

    it("sets error and rethrows on backend failure", async () => {
      (profileService.setActiveProfile as any).mockRejectedValue(new Error("not found"));

      await expect(switchProfile("missing")).rejects.toThrow("not found");
      expect(profileStore.error).toBe("not found");
      expect(loadSettingsMock).not.toHaveBeenCalled();
    });

    it("does not reload settings if backend switch fails", async () => {
      (profileService.setActiveProfile as any).mockRejectedValue(new Error("fail"));
      await expect(switchProfile("x")).rejects.toThrow();
      expect(loadSettingsMock).not.toHaveBeenCalled();
    });
  });

  describe("createProfile", () => {
    it("creates a profile and reloads the list", async () => {
      (profileService.createProfile as any).mockResolvedValue(undefined);
      (profileService.listProfiles as any).mockResolvedValue([
        makeProfile({ name: "default", active: true }),
        makeProfile({ name: "work", active: false }),
      ]);
      (profileService.getActiveProfile as any).mockResolvedValue("default");

      await createProfile("work", true);

      expect(profileService.createProfile).toHaveBeenCalledWith("work", true);
      expect(profileStore.profiles).toHaveLength(2);
    });

    it("propagates error", async () => {
      (profileService.createProfile as any).mockRejectedValue(new Error("exists"));
      await expect(createProfile("work", false)).rejects.toThrow("exists");
      expect(profileStore.error).toBe("exists");
    });
  });

  describe("deleteProfile", () => {
    it("deletes and reloads", async () => {
      (profileService.deleteProfile as any).mockResolvedValue(undefined);
      (profileService.listProfiles as any).mockResolvedValue([
        makeProfile({ name: "default", active: true }),
      ]);
      (profileService.getActiveProfile as any).mockResolvedValue("default");

      await deleteProfile("work");

      expect(profileService.deleteProfile).toHaveBeenCalledWith("work");
      expect(profileStore.profiles).toHaveLength(1);
    });

    it("propagates error", async () => {
      (profileService.deleteProfile as any).mockRejectedValue(new Error("protected"));
      await expect(deleteProfile("default")).rejects.toThrow("protected");
      expect(profileStore.error).toBe("protected");
    });
  });

  describe("renameProfile", () => {
    it("renames and reloads", async () => {
      (profileService.renameProfile as any).mockResolvedValue(undefined);
      (profileService.listProfiles as any).mockResolvedValue([
        makeProfile({ name: "default", active: true }),
        makeProfile({ name: "office", active: false }),
      ]);
      (profileService.getActiveProfile as any).mockResolvedValue("default");

      await renameProfile("work", "office");

      expect(profileService.renameProfile).toHaveBeenCalledWith("work", "office");
      expect(profileStore.profiles[1].name).toBe("office");
    });

    it("propagates error", async () => {
      (profileService.renameProfile as any).mockRejectedValue(new Error("target exists"));
      await expect(renameProfile("a", "b")).rejects.toThrow("target exists");
    });
  });

  describe("setProfileDescription", () => {
    it("updates description and reloads", async () => {
      (profileService.setProfileDescription as any).mockResolvedValue(undefined);
      (profileService.listProfiles as any).mockResolvedValue([
        makeProfile({ name: "default", active: true, description: "new desc" }),
      ]);
      (profileService.getActiveProfile as any).mockResolvedValue("default");

      await setProfileDescription("default", "new desc");

      expect(profileService.setProfileDescription).toHaveBeenCalledWith("default", "new desc");
      expect(profileStore.profiles[0].description).toBe("new desc");
    });
  });

  describe("exportProfile", () => {
    it("returns the export blob", async () => {
      const exportData = {
        name: "default",
        settings: { language: "zh" },
        exportedAt: 1234567890,
      };
      (profileService.exportProfile as any).mockResolvedValue(exportData);

      const result = await exportProfile("default");

      expect(profileService.exportProfile).toHaveBeenCalledWith("default");
      expect(result).toEqual(exportData);
    });

    it("sets error and rethrows on failure", async () => {
      (profileService.exportProfile as any).mockRejectedValue(new Error("nope"));
      await expect(exportProfile("missing")).rejects.toThrow("nope");
      expect(profileStore.error).toBe("nope");
    });
  });

  describe("importProfile", () => {
    it("imports and returns the actual name used", async () => {
      (profileService.importProfile as any).mockResolvedValue("imported-2");
      (profileService.listProfiles as any).mockResolvedValue([
        makeProfile({ name: "default", active: true }),
        makeProfile({ name: "imported-2", active: false }),
      ]);
      (profileService.getActiveProfile as any).mockResolvedValue("default");

      const data = { schemaVersion: 1, name: "imported", settings: {}, exportedAt: 0 };
      const name = await importProfile(data);

      expect(profileService.importProfile).toHaveBeenCalledWith(data);
      expect(name).toBe("imported-2");
      expect(profileStore.profiles).toHaveLength(2);
    });

    it("propagates error", async () => {
      (profileService.importProfile as any).mockRejectedValue(new Error("corrupt"));
      await expect(importProfile({ schemaVersion: 1, name: "x", settings: {}, exportedAt: 0 })).rejects.toThrow("corrupt");
    });
  });
});
