// 优先级 10 (prompt-1.md 458-466): 自动更新 + 崩溃报告 store。
//
// 封装 UpdateService / CrashService 的前端调用与状态：
//   - checkForUpdates: 查询最新版本并与当前版本比较，结果写入 updateState。
//   - loadCrashReports / viewCrashReport / deleteCrashReport / clearAllCrashReports:
//     管理本地崩溃报告列表与详情。
//
// 所有错误经 notifyError 上报且不抛出，调用方可用返回值判断成功与否。
// Koyori IDE 模块 · Update Crash；交互服务：更新（UpdateService）、崩溃报告（CrashService）。
// 喵，这是 Koyori IDE 的 Update Crash 模块（前端实现）~
import { reactive, computed } from "vue";
import { updateService, crashService } from "@/api/services";
import { notifyError, notifySuccess, notifyInfo } from "@/lib/notifications";
import { errorMessage } from "@/lib/errors";
import { translate } from "@/lib/i18n";
import type { UpdateInfo, CrashReportInfo, CrashReport } from "@/types";

interface UpdateState {
  checking: boolean;
  downloading: boolean;
  currentVersion: string;
  info: UpdateInfo | null;
  downloadedDirectory: string;
  errorMessage: string | null;
}

interface CrashState {
  reports: CrashReportInfo[];
  selected: CrashReport | null;
  loading: boolean;
  errorMessage: string | null;
}

export const updateState = reactive<UpdateState>({
  checking: false,
  downloading: false,
  currentVersion: "",
  info: null,
  downloadedDirectory: "",
  errorMessage: null,
});

export const crashState = reactive<CrashState>({
  reports: [],
  selected: null,
  loading: false,
  errorMessage: null,
});

export const hasUpdate = computed(
  () => !!updateState.info?.hasUpdate,
);

export const canDownloadUpdate = computed(
  () => !!updateState.info?.hasUpdate && /#sha256=[a-f\d]{64}$/i.test(updateState.info.downloadUrl),
);

// fetchCurrentVersion 拉取当前应用版本并写入 updateState.currentVersion。
// 失败时静默（版本为空字符串，checkForUpdates 仍可工作）。
export async function fetchCurrentVersion(): Promise<string> {
  try {
    const v = await updateService.getCurrentVersion();
    updateState.currentVersion = v || "";
    return v || "";
  } catch {
    updateState.currentVersion = "";
    return "";
  }
}

// downloadVerifiedUpdate downloads a checksum-bearing release asset into an
// existing directory. The backend publishes it only after SHA-256 verification;
// installation remains an explicit manual step (E2).
export async function downloadVerifiedUpdate(destDirectory: string): Promise<boolean> {
  updateState.errorMessage = null;
  updateState.downloadedDirectory = "";
  if (!destDirectory || !canDownloadUpdate.value || !updateState.info) {
    updateState.errorMessage = translate("update.downloadUnavailable");
    notifyError(updateState.errorMessage);
    return false;
  }
  updateState.downloading = true;
  try {
    await updateService.downloadUpdate(updateState.info.downloadUrl, destDirectory);
    updateState.downloadedDirectory = destDirectory;
    notifySuccess(translate("update.downloadCompleteManual", { path: destDirectory }));
    return true;
  } catch (e: unknown) {
    updateState.errorMessage = errorMessage(e);
    notifyError(translate("update.downloadFailed", { error: updateState.errorMessage }));
    return false;
  } finally {
    updateState.downloading = false;
  }
}

// checkForUpdates 查询最新版本。updateURL 为空时后端使用默认 GitHub 端点。
// 返回 UpdateInfo（无更新时 HasUpdate 为 false）。错误经 notifyError 上报。
export async function checkForUpdates(updateURL = ""): Promise<UpdateInfo | null> {
  updateState.checking = true;
  updateState.errorMessage = null;
  try {
    if (!updateState.currentVersion) {
      await fetchCurrentVersion();
    }
    const info = await updateService.checkForUpdates(updateState.currentVersion, updateURL);
    updateState.info = info;
    if (info?.hasUpdate) {
      notifyInfo(translate("update.newVersion", { version: info.latestVersion }));
    } else if (info) {
      notifySuccess(translate("update.upToDate", {
        version: info.latestVersion || updateState.currentVersion,
      }));
    }
    return info;
  } catch (e: unknown) {
    updateState.errorMessage = errorMessage(e);
    notifyError(translate("update.checkFailed", {
      error: updateState.errorMessage,
    }));
    return null;
  } finally {
    updateState.checking = false;
  }
}

// loadCrashReports 拉取崩溃报告列表并写入 crashState.reports。
export async function loadCrashReports(): Promise<CrashReportInfo[]> {
  crashState.loading = true;
  crashState.errorMessage = null;
  try {
    const list = await crashService.getCrashReports();
    crashState.reports = Array.isArray(list) ? list : [];
    return crashState.reports;
  } catch (e: unknown) {
    crashState.reports = [];
    crashState.errorMessage = errorMessage(e);
    notifyError(translate("crash.loadFailed", {
      error: crashState.errorMessage,
    }));
    return [];
  } finally {
    crashState.loading = false;
  }
}

// viewCrashReport 读取指定崩溃报告详情并写入 crashState.selected。
export async function viewCrashReport(filename: string): Promise<CrashReport | null> {
  crashState.errorMessage = null;
  try {
    const report = await crashService.getCrashReport(filename);
    crashState.selected = report;
    return report;
  } catch (e: unknown) {
    crashState.errorMessage = errorMessage(e);
    notifyError(translate("crash.readFailed", {
      error: crashState.errorMessage,
    }));
    return null;
  }
}

// deleteCrashReport 删除指定崩溃报告并刷新列表。返回是否成功。
export async function deleteCrashReport(filename: string): Promise<boolean> {
  try {
    await crashService.deleteCrashReport(filename);
    if (crashState.selected?.filename === filename) {
      crashState.selected = null;
    }
    await loadCrashReports();
    return true;
  } catch (e: unknown) {
    crashState.errorMessage = errorMessage(e);
    notifyError(translate("crash.deleteFailed", {
      error: crashState.errorMessage,
    }));
    return false;
  }
}

// clearAllCrashReports 删除所有崩溃报告并刷新列表。返回是否成功。
export async function clearAllCrashReports(): Promise<boolean> {
  try {
    await crashService.clearAllCrashReports();
    crashState.selected = null;
    crashState.reports = [];
    return true;
  } catch (e: unknown) {
    crashState.errorMessage = errorMessage(e);
    notifyError(translate("crash.clearFailed", {
      error: crashState.errorMessage,
    }));
    return false;
  }
}
