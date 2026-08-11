// 优先级 10 (prompt-1.md): 自动更新 + 崩溃报告 store 测试。
//
// 覆盖 checkForUpdates / loadCrashReports / viewCrashReport /
// deleteCrashReport / clearAllCrashReports 等动作的成功与失败路径，
// 以及 hasUpdate 计算属性。所有后端调用通过 vi.mock 替换，无真实网络。
import { describe, it, expect, beforeEach, vi } from "vitest";

vi.mock("@/api/services", () => ({
  updateService: {
    getCurrentVersion: vi.fn(),
    checkForUpdates: vi.fn(),
    compareVersions: vi.fn(),
    downloadUpdate: vi.fn(),
  },
  crashService: {
    reportCrash: vi.fn(),
    getCrashReports: vi.fn(),
    getCrashReport: vi.fn(),
    deleteCrashReport: vi.fn(),
    clearAllCrashReports: vi.fn(),
  },
}));

vi.mock("@/lib/notifications", () => ({
  notifyError: vi.fn(),
  notifySuccess: vi.fn(),
  notifyInfo: vi.fn(),
  notifyWarning: vi.fn(),
}));

import {
  updateState,
  crashState,
  hasUpdate,
  canDownloadUpdate,
  fetchCurrentVersion,
  checkForUpdates,
  downloadVerifiedUpdate,
  loadCrashReports,
  viewCrashReport,
  deleteCrashReport,
  clearAllCrashReports,
} from "./updateCrash";
import { updateService, crashService } from "@/api/services";
import { notifyError, notifySuccess, notifyInfo } from "@/lib/notifications";

describe("updateCrash store", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    // 重置响应式状态，避免用例间泄漏。
    updateState.checking = false;
    updateState.downloading = false;
    updateState.currentVersion = "";
    updateState.info = null;
    updateState.downloadedDirectory = "";
    updateState.errorMessage = null;
    crashState.reports = [];
    crashState.selected = null;
    crashState.loading = false;
    crashState.errorMessage = null;
  });

  describe("fetchCurrentVersion", () => {
    it("stores the version returned by the backend", async () => {
      (updateService.getCurrentVersion as any).mockResolvedValue("1.2.3");
      const v = await fetchCurrentVersion();
      expect(v).toBe("1.2.3");
      expect(updateState.currentVersion).toBe("1.2.3");
    });

    it("returns empty string and clears state on failure", async () => {
      (updateService.getCurrentVersion as any).mockRejectedValue(new Error("boom"));
      const v = await fetchCurrentVersion();
      expect(v).toBe("");
      expect(updateState.currentVersion).toBe("");
    });
  });

  describe("checkForUpdates", () => {
    it("notifies when a newer version is available", async () => {
      updateState.currentVersion = "1.0.0";
      const info = {
        hasUpdate: true,
        latestVersion: "1.2.0",
        currentVersion: "1.0.0",
        releaseNotes: "fixes",
        downloadUrl: "https://github.com/a/b/releases/1.2.0",
        releaseDate: "2026-01-01",
      };
      (updateService.checkForUpdates as any).mockResolvedValue(info);
      const result = await checkForUpdates();
      expect(result).toEqual(info);
      expect(updateState.info).toEqual(info);
      expect(updateState.checking).toBe(false);
      expect(notifyInfo).toHaveBeenCalledWith(expect.stringContaining("1.2.0"));
      expect(notifySuccess).not.toHaveBeenCalled();
    });

    it("notifies when already on the latest version", async () => {
      updateState.currentVersion = "1.0.0";
      const info = {
        hasUpdate: false,
        latestVersion: "1.0.0",
        currentVersion: "1.0.0",
        releaseNotes: "",
        downloadUrl: "",
        releaseDate: "",
      };
      (updateService.checkForUpdates as any).mockResolvedValue(info);
      const result = await checkForUpdates();
      expect(result).toEqual(info);
      expect(notifySuccess).toHaveBeenCalledWith(expect.stringContaining("1.0.0"));
      expect(notifyInfo).not.toHaveBeenCalled();
    });

    it("fetches current version first when unknown", async () => {
      (updateService.getCurrentVersion as any).mockResolvedValue("0.9.0");
      (updateService.checkForUpdates as any).mockResolvedValue({
        hasUpdate: false,
        latestVersion: "0.9.0",
        currentVersion: "0.9.0",
        releaseNotes: "",
        downloadUrl: "",
        releaseDate: "",
      });
      await checkForUpdates();
      expect(updateService.getCurrentVersion).toHaveBeenCalled();
      expect(updateService.checkForUpdates).toHaveBeenCalledWith("0.9.0", "");
    });

    it("surfaces backend errors via notification without throwing", async () => {
      updateState.currentVersion = "1.0.0";
      (updateService.checkForUpdates as any).mockRejectedValue(new Error("network down"));
      const result = await checkForUpdates();
      expect(result).toBeNull();
      expect(updateState.info).toBeNull();
      expect(updateState.errorMessage).toBe("network down");
      expect(updateState.checking).toBe(false);
      expect(notifyError).toHaveBeenCalledWith(expect.stringContaining("network down"));
    });
  });

  describe("hasUpdate computed", () => {
    it("is false when no info is present", () => {
      expect(hasUpdate.value).toBe(false);
    });
    it("is false when info.hasUpdate is false", () => {
      updateState.info = {
        hasUpdate: false,
        latestVersion: "1.0.0",
        currentVersion: "1.0.0",
        releaseNotes: "",
        downloadUrl: "",
        releaseDate: "",
      };
      expect(hasUpdate.value).toBe(false);
    });
    it("is true when info.hasUpdate is true", () => {
      updateState.info = {
        hasUpdate: true,
        latestVersion: "1.1.0",
        currentVersion: "1.0.0",
        releaseNotes: "",
        downloadUrl: "",
        releaseDate: "",
      };
      expect(hasUpdate.value).toBe(true);
    });
  });

  describe("downloadVerifiedUpdate", () => {
    const digest = "a".repeat(64);

    it("downloads a checksum-bearing package for manual installation", async () => {
      updateState.info = {
        hasUpdate: true,
        latestVersion: "1.1.0",
        currentVersion: "1.0.0",
        releaseNotes: "",
        downloadUrl: `https://github.com/a/b/releases/download/v1.1.0/app.zip#sha256=${digest}`,
        releaseDate: "",
      };
      (updateService.downloadUpdate as any).mockResolvedValue(undefined);

      expect(canDownloadUpdate.value).toBe(true);
      await expect(downloadVerifiedUpdate("/downloads")).resolves.toBe(true);
      expect(updateService.downloadUpdate).toHaveBeenCalledWith(updateState.info.downloadUrl, "/downloads");
      expect(updateState.downloadedDirectory).toBe("/downloads");
      expect(updateState.downloading).toBe(false);
      expect(notifySuccess).toHaveBeenCalledWith(expect.stringContaining("/downloads"));
    });

    it("fails closed when release metadata has no valid checksum", async () => {
      updateState.info = {
        hasUpdate: true,
        latestVersion: "1.1.0",
        currentVersion: "1.0.0",
        releaseNotes: "",
        downloadUrl: "https://github.com/a/b/releases/download/v1.1.0/app.zip",
        releaseDate: "",
      };

      await expect(downloadVerifiedUpdate("/downloads")).resolves.toBe(false);
      expect(canDownloadUpdate.value).toBe(false);
      expect(updateService.downloadUpdate).not.toHaveBeenCalled();
      expect(notifyError).toHaveBeenCalled();
    });

    it("does not report success when backend verification fails", async () => {
      updateState.info = {
        hasUpdate: true,
        latestVersion: "1.1.0",
        currentVersion: "1.0.0",
        releaseNotes: "",
        downloadUrl: `https://github.com/a/b/releases/download/v1.1.0/app.zip#sha256=${digest}`,
        releaseDate: "",
      };
      (updateService.downloadUpdate as any).mockRejectedValue(new Error("SHA-256 verification failed"));

      await expect(downloadVerifiedUpdate("/downloads")).resolves.toBe(false);
      expect(updateState.downloadedDirectory).toBe("");
      expect(updateState.errorMessage).toContain("SHA-256");
      expect(notifySuccess).not.toHaveBeenCalled();
      expect(notifyError).toHaveBeenCalledWith(expect.stringContaining("SHA-256"));
    });
  });

  describe("loadCrashReports", () => {
    it("loads the report list from the backend", async () => {
      const list = [
        { filename: "crash-1.json", timestamp: "2026-01-01T00:00:00Z", size: 128 },
        { filename: "crash-2.json", timestamp: "2026-01-02T00:00:00Z", size: 256 },
      ];
      (crashService.getCrashReports as any).mockResolvedValue(list);
      const result = await loadCrashReports();
      expect(result).toEqual(list);
      expect(crashState.reports).toEqual(list);
      expect(crashState.loading).toBe(false);
      expect(crashState.errorMessage).toBeNull();
    });

    it("clears the list and surfaces errors on failure", async () => {
      (crashService.getCrashReports as any).mockRejectedValue(new Error("read failed"));
      const result = await loadCrashReports();
      expect(result).toEqual([]);
      expect(crashState.reports).toEqual([]);
      expect(crashState.errorMessage).toBe("read failed");
      expect(crashState.loading).toBe(false);
      expect(notifyError).toHaveBeenCalledWith(expect.stringContaining("read failed"));
    });

    it("coerces a non-array response into an empty list", async () => {
      (crashService.getCrashReports as any).mockResolvedValue(null);
      const result = await loadCrashReports();
      expect(result).toEqual([]);
      expect(crashState.reports).toEqual([]);
    });
  });

  describe("viewCrashReport", () => {
    it("stores the selected report", async () => {
      const report = {
        filename: "crash-1.json",
        timestamp: "2026-01-01T00:00:00Z",
        version: "1.0.0",
        os: "linux",
        stack: "goroutine 1 ...",
        message: "nil pointer",
        errorType: "panic",
      };
      (crashService.getCrashReport as any).mockResolvedValue(report);
      const result = await viewCrashReport("crash-1.json");
      expect(result).toEqual(report);
      expect(crashState.selected).toEqual(report);
      expect(crashService.getCrashReport).toHaveBeenCalledWith("crash-1.json");
    });

    it("surfaces errors without throwing", async () => {
      (crashService.getCrashReport as any).mockRejectedValue(new Error("not found"));
      const result = await viewCrashReport("missing.json");
      expect(result).toBeNull();
      expect(crashState.selected).toBeNull();
      expect(crashState.errorMessage).toBe("not found");
      expect(notifyError).toHaveBeenCalledWith(expect.stringContaining("not found"));
    });
  });

  describe("deleteCrashReport", () => {
    it("deletes, clears selection if it matches, and refreshes the list", async () => {
      crashState.selected = {
        filename: "crash-1.json",
        timestamp: "2026-01-01T00:00:00Z",
        version: "1.0.0",
        os: "linux",
        stack: "",
        message: "",
        errorType: "",
      };
      (crashService.deleteCrashReport as any).mockResolvedValue(undefined);
      (crashService.getCrashReports as any).mockResolvedValue([]);
      const ok = await deleteCrashReport("crash-1.json");
      expect(ok).toBe(true);
      expect(crashService.deleteCrashReport).toHaveBeenCalledWith("crash-1.json");
      expect(crashState.selected).toBeNull();
      expect(crashService.getCrashReports).toHaveBeenCalled();
    });

    it("keeps selection when deleting a different filename", async () => {
      const sel = {
        filename: "crash-2.json",
        timestamp: "2026-01-01T00:00:00Z",
        version: "1.0.0",
        os: "linux",
        stack: "",
        message: "",
        errorType: "",
      };
      crashState.selected = sel;
      (crashService.deleteCrashReport as any).mockResolvedValue(undefined);
      (crashService.getCrashReports as any).mockResolvedValue([]);
      await deleteCrashReport("crash-1.json");
      expect(crashState.selected).toEqual(sel);
    });

    it("returns false and surfaces errors on failure", async () => {
      (crashService.deleteCrashReport as any).mockRejectedValue(new Error("perm denied"));
      const ok = await deleteCrashReport("crash-1.json");
      expect(ok).toBe(false);
      expect(crashState.errorMessage).toBe("perm denied");
      expect(notifyError).toHaveBeenCalledWith(expect.stringContaining("perm denied"));
    });
  });

  describe("clearAllCrashReports", () => {
    it("clears reports and selection on success", async () => {
      crashState.reports = [
        { filename: "crash-1.json", timestamp: "2026-01-01T00:00:00Z", size: 10 },
      ];
      crashState.selected = {
        filename: "crash-1.json",
        timestamp: "2026-01-01T00:00:00Z",
        version: "1.0.0",
        os: "linux",
        stack: "",
        message: "",
        errorType: "",
      };
      (crashService.clearAllCrashReports as any).mockResolvedValue(undefined);
      const ok = await clearAllCrashReports();
      expect(ok).toBe(true);
      expect(crashState.reports).toEqual([]);
      expect(crashState.selected).toBeNull();
      expect(crashService.clearAllCrashReports).toHaveBeenCalled();
    });

    it("returns false and surfaces errors on failure", async () => {
      (crashService.clearAllCrashReports as any).mockRejectedValue(new Error("busy"));
      const ok = await clearAllCrashReports();
      expect(ok).toBe(false);
      expect(crashState.errorMessage).toBe("busy");
      expect(notifyError).toHaveBeenCalledWith(expect.stringContaining("busy"));
    });
  });
});
