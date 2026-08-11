import { describe, it, expect, beforeEach, vi } from "vitest";

vi.mock("@wailsio/runtime", () => ({
  Events: { On: vi.fn() },
}));

vi.mock("@/api/services", () => ({
  aiService: {
    listPresets: vi.fn(),
    listPresetsWithSource: vi.fn(),
    getPresetPrompt: vi.fn(),
    saveProjectPreset: vi.fn(),
    saveUserPreset: vi.fn(),
    deleteProjectPreset: vi.fn(),
    deleteUserPreset: vi.fn(),
  },
}));

vi.mock("@/lib/notifications", () => ({
  notifyError: vi.fn(),
  notifySuccess: vi.fn(),
}));

import {
  presetsState,
  loadPresets,
  getPresetPrompt,
  saveProjectPreset,
  saveUserPreset,
  deleteProjectPreset,
  deleteUserPreset,
} from "./presets";
import { aiService } from "@/api/services";

describe("presets store", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    presetsState.presets = [];
    presetsState.presetsWithSource = [];
    presetsState.loading = false;
    presetsState.error = null;
  });

  describe("loadPresets", () => {
    it("loads merged presets and with-source list", async () => {
      (aiService.listPresets as any).mockResolvedValue([
        { name: "explain", label: "Explain", description: "x", icon: "i" },
      ]);
      (aiService.listPresetsWithSource as any).mockResolvedValue([
        { name: "explain", label: "Explain", description: "x", icon: "i", prompt: "p", source: "builtin" },
      ]);
      await loadPresets();
      expect(presetsState.presets).toHaveLength(1);
      expect(presetsState.presetsWithSource).toHaveLength(1);
      expect(presetsState.presetsWithSource[0].source).toBe("builtin");
      expect(presetsState.loading).toBe(false);
      expect(presetsState.error).toBeNull();
    });

    it("sets error and notifies on failure", async () => {
      (aiService.listPresets as any).mockRejectedValue(new Error("network"));
      await loadPresets();
      expect(presetsState.error).toBe("network");
      expect(presetsState.loading).toBe(false);
    });
  });

  describe("getPresetPrompt", () => {
    it("delegates to aiService.getPresetPrompt", async () => {
      (aiService.getPresetPrompt as any).mockResolvedValue("PROMPT TEXT");
      const result = await getPresetPrompt("explain");
      expect(aiService.getPresetPrompt).toHaveBeenCalledWith("explain");
      expect(result).toBe("PROMPT TEXT");
    });
  });

  describe("saveProjectPreset", () => {
    it("saves and reloads the preset list", async () => {
      (aiService.saveProjectPreset as any).mockResolvedValue(undefined);
      (aiService.listPresets as any).mockResolvedValue([]);
      (aiService.listPresetsWithSource as any).mockResolvedValue([]);
      await saveProjectPreset({
        name: "translate",
        label: "Translate",
        description: "d",
        prompt: "p",
      });
      expect(aiService.saveProjectPreset).toHaveBeenCalledWith({
        name: "translate",
        label: "Translate",
        description: "d",
        prompt: "p",
      });
      // loadPresets should have been called after save
      expect(aiService.listPresets).toHaveBeenCalled();
    });
  });

  describe("saveUserPreset", () => {
    it("saves to user config and reloads", async () => {
      (aiService.saveUserPreset as any).mockResolvedValue(undefined);
      (aiService.listPresets as any).mockResolvedValue([]);
      (aiService.listPresetsWithSource as any).mockResolvedValue([]);
      await saveUserPreset({
        name: "global",
        label: "Global",
        description: "",
        prompt: "p",
      });
      expect(aiService.saveUserPreset).toHaveBeenCalled();
    });
  });

  describe("deleteProjectPreset", () => {
    it("deletes and reloads", async () => {
      (aiService.deleteProjectPreset as any).mockResolvedValue(undefined);
      (aiService.listPresets as any).mockResolvedValue([]);
      (aiService.listPresetsWithSource as any).mockResolvedValue([]);
      await deleteProjectPreset("translate");
      expect(aiService.deleteProjectPreset).toHaveBeenCalledWith("translate");
      expect(aiService.listPresets).toHaveBeenCalled();
    });

    it("propagates error", async () => {
      (aiService.deleteProjectPreset as any).mockRejectedValue(new Error("not found"));
      await expect(deleteProjectPreset("nope")).rejects.toThrow("not found");
    });
  });

  describe("deleteUserPreset", () => {
    it("deletes and reloads", async () => {
      (aiService.deleteUserPreset as any).mockResolvedValue(undefined);
      (aiService.listPresets as any).mockResolvedValue([]);
      (aiService.listPresetsWithSource as any).mockResolvedValue([]);
      await deleteUserPreset("global");
      expect(aiService.deleteUserPreset).toHaveBeenCalledWith("global");
    });
  });
});
