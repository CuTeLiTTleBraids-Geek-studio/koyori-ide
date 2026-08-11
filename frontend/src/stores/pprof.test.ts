/**
 * Priority 7 (prompt-1.md 422-432): pprof store 前端测试。
 * 覆盖纯函数（formatDuration / formatBytes / defaultProfilePath）、
 * 状态管理（clearAnalysis / refreshProfilingStatus）与异步操作
 * （startCPUProfile / stopCPUProfile / captureHeapProfile /
 *   captureGoroutineProfile / analyzeProfile）。
 */
import { describe, it, expect, beforeEach, vi } from "vitest";

// vi.hoisted 确保 mock 函数在 vi.mock 工厂执行前就已定义，
// 避免 "Cannot access before initialization" 错误（与 editor.test.ts 同模式）。
const {
  startCPUProfileMock,
  stopCPUProfileMock,
  isProfilingMock,
  activeProfileMock,
  captureHeapProfileMock,
  captureGoroutineProfileMock,
  analyzeProfileMock,
  createDirectoryMock,
  notifyErrorMock,
  notifySuccessMock,
  pushOutputMock,
} = vi.hoisted(() => ({
  startCPUProfileMock: vi.fn().mockResolvedValue(undefined),
  stopCPUProfileMock: vi.fn().mockResolvedValue(undefined),
  isProfilingMock: vi.fn().mockResolvedValue(false),
  activeProfileMock: vi.fn().mockResolvedValue(""),
  captureHeapProfileMock: vi.fn().mockResolvedValue(undefined),
  captureGoroutineProfileMock: vi.fn().mockResolvedValue(undefined),
  analyzeProfileMock: vi.fn().mockResolvedValue(null),
  createDirectoryMock: vi.fn().mockResolvedValue(undefined),
  notifyErrorMock: vi.fn(),
  notifySuccessMock: vi.fn(),
  pushOutputMock: vi.fn(),
}));

vi.mock("@/api/services", () => ({
  pprofService: {
    startCPUProfile: startCPUProfileMock,
    stopCPUProfile: stopCPUProfileMock,
    isProfiling: isProfilingMock,
    activeProfile: activeProfileMock,
    captureHeapProfile: captureHeapProfileMock,
    captureGoroutineProfile: captureGoroutineProfileMock,
    analyzeProfile: analyzeProfileMock,
  },
  fileService: {
    createDirectory: createDirectoryMock,
  },
}));

vi.mock("@/stores/app", () => ({
  appState: {
    currentProject: "/proj",
  },
}));

vi.mock("@/stores/output", () => ({
  pushOutput: pushOutputMock,
}));

vi.mock("@/lib/notifications", () => ({
  notifyError: notifyErrorMock,
  notifySuccess: notifySuccessMock,
  notifyWarning: vi.fn(),
  notifyInfo: vi.fn(),
}));

import { appState } from "@/stores/app";
import {
  pprofState,
  defaultProfilePath,
  refreshProfilingStatus,
  startCPUProfile,
  stopCPUProfile,
  captureHeapProfile,
  captureGoroutineProfile,
  analyzeProfile,
  clearAnalysis,
  formatDuration,
  formatBytes,
} from "./pprof";

// 辅助：在 beforeEach 中重置 mock 与状态。
function resetState() {
  localStorage.clear();
  pprofState.cpuProfiling = false;
  pprofState.loading = false;
  pprofState.analysis = null;
  pprofState.lastProfilePath = "";
  pprofState.lastError = "";
  pprofState.lastKind = "";
  pprofState.cpuOutputPath = "";
}

describe("pprof store — pure functions (P7)", () => {
  it("formatDuration formats nanoseconds correctly", () => {
    expect(formatDuration(0)).toBe("0");
    expect(formatDuration(500)).toBe("500 ns");
    expect(formatDuration(1500)).toBe("1.50 µs");
    expect(formatDuration(1_500_000)).toBe("1.50 ms");
    expect(formatDuration(1_500_000_000)).toBe("1.50 s");
  });

  it("formatDuration handles negative values", () => {
    expect(formatDuration(-500)).toBe("-500 ns");
    expect(formatDuration(-1_500_000)).toBe("-1.50 ms");
  });

  it("formatBytes formats bytes correctly", () => {
    expect(formatBytes(0)).toBe("0 B");
    expect(formatBytes(512)).toBe("512.00 B");
    expect(formatBytes(1024)).toBe("1.00 KB");
    expect(formatBytes(1048576)).toBe("1.00 MB");
    expect(formatBytes(1073741824)).toBe("1.00 GB");
  });

  it("defaultProfilePath generates path with project root and kind", () => {
    const path = defaultProfilePath("cpu");
    expect(path.startsWith("/proj/.pprof/cpu-")).toBe(true);
    expect(path.endsWith(".prof")).toBe(true);
  });

  it("defaultProfilePath returns empty when no project", () => {
    // 临时修改 mock 的 currentProject 验证空项目场景。
    const saved = appState.currentProject;
    appState.currentProject = "";
    expect(defaultProfilePath("heap")).toBe("");
    appState.currentProject = saved;
  });
});

describe("pprof store — state management (P7)", () => {
  beforeEach(() => {
    resetState();
    for (const m of [
      startCPUProfileMock, stopCPUProfileMock, isProfilingMock,
      captureHeapProfileMock, captureGoroutineProfileMock, analyzeProfileMock,
      createDirectoryMock, notifyErrorMock, notifySuccessMock, pushOutputMock,
    ]) {
      m.mockReset();
      m.mockResolvedValue?.(undefined);
    }
    isProfilingMock.mockResolvedValue(false);
    analyzeProfileMock.mockResolvedValue(null);
  });

  it("clearAnalysis resets analysis state", () => {
    pprofState.analysis = {
      totalSamples: 10,
      totalDuration: 1000,
      topFunctions: [],
      sampleUnit: "nanoseconds",
      sampleType: "samples",
    };
    pprofState.lastError = "err";
    pprofState.lastKind = "cpu";
    clearAnalysis();
    expect(pprofState.analysis).toBeNull();
    expect(pprofState.lastError).toBe("");
    expect(pprofState.lastKind).toBe("");
  });

  it("refreshProfilingStatus updates cpuProfiling from backend", async () => {
    isProfilingMock.mockResolvedValue(true);
    await refreshProfilingStatus();
    expect(pprofState.cpuProfiling).toBe(true);
  });

  it("refreshProfilingStatus handles errors gracefully", async () => {
    isProfilingMock.mockRejectedValue(new Error("rpc down"));
    await refreshProfilingStatus();
    expect(pprofState.cpuProfiling).toBe(false);
  });
});

describe("pprof store — async operations (P7)", () => {
  beforeEach(() => {
    resetState();
    for (const m of [
      startCPUProfileMock, stopCPUProfileMock, isProfilingMock,
      captureHeapProfileMock, captureGoroutineProfileMock, analyzeProfileMock,
      createDirectoryMock, notifyErrorMock, notifySuccessMock, pushOutputMock,
    ]) {
      m.mockReset();
      m.mockResolvedValue?.(undefined);
    }
    analyzeProfileMock.mockResolvedValue(null);
  });

  it("startCPUProfile calls service and updates state", async () => {
    const ok = await startCPUProfile();
    expect(ok).toBe(true);
    expect(startCPUProfileMock).toHaveBeenCalledTimes(1);
    expect(pprofState.cpuProfiling).toBe(true);
    expect(pprofState.lastKind).toBe("cpu");
    expect(pprofState.cpuOutputPath).toBeTruthy();
    expect(notifySuccessMock).toHaveBeenCalled();
  });

  it("startCPUProfile notifies error when no project open", async () => {
    const saved = appState.currentProject;
    appState.currentProject = "";
    const ok = await startCPUProfile();
    expect(ok).toBe(false);
    expect(startCPUProfileMock).not.toHaveBeenCalled();
    expect(notifyErrorMock).toHaveBeenCalled();
    appState.currentProject = saved;
  });

  it("startCPUProfile records error on service failure", async () => {
    startCPUProfileMock.mockRejectedValue(new Error("already profiling"));
    const ok = await startCPUProfile();
    expect(ok).toBe(false);
    expect(pprofState.cpuProfiling).toBe(false);
    expect(pprofState.lastError).toContain("already profiling");
    expect(notifyErrorMock).toHaveBeenCalled();
  });

  it("stopCPUProfile calls service and clears state", async () => {
    pprofState.cpuProfiling = true;
    pprofState.cpuOutputPath = "/proj/.pprof/cpu-test.prof";
    await stopCPUProfile(false);
    expect(stopCPUProfileMock).toHaveBeenCalledTimes(1);
    expect(pprofState.cpuProfiling).toBe(false);
    expect(pprofState.cpuOutputPath).toBe("");
    expect(pushOutputMock).toHaveBeenCalled();
  });

  it("stopCPUProfile auto-analyzes by default", async () => {
    pprofState.cpuProfiling = true;
    pprofState.cpuOutputPath = "/proj/.pprof/cpu-test.prof";
    const analysisResult = {
      totalSamples: 5,
      totalDuration: 1000000,
      topFunctions: [],
      sampleUnit: "nanoseconds",
      sampleType: "samples",
    };
    analyzeProfileMock.mockResolvedValue(analysisResult);
    await stopCPUProfile(true);
    expect(analyzeProfileMock).toHaveBeenCalledWith("/proj/.pprof/cpu-test.prof");
    expect(pprofState.analysis).toEqual(analysisResult);
  });

  it("captureHeapProfile calls service and updates state", async () => {
    const analysisResult = {
      totalSamples: 3,
      totalDuration: 2048,
      topFunctions: [],
      sampleUnit: "bytes",
      sampleType: "alloc_objects",
    };
    analyzeProfileMock.mockResolvedValue(analysisResult);
    await captureHeapProfile();
    expect(captureHeapProfileMock).toHaveBeenCalledTimes(1);
    expect(pprofState.lastKind).toBe("heap");
    expect(pprofState.lastProfilePath).toContain("heap-");
    expect(pprofState.analysis).toEqual(analysisResult);
    expect(pushOutputMock).toHaveBeenCalled();
  });

  it("captureGoroutineProfile calls service with debug=0", async () => {
    await captureGoroutineProfile();
    expect(captureGoroutineProfileMock).toHaveBeenCalledWith(expect.any(String), 0);
    expect(pprofState.lastKind).toBe("goroutine");
  });

  it("analyzeProfile updates analysis state on success", async () => {
    const analysisResult = {
      totalSamples: 10,
      totalDuration: 5000000,
      topFunctions: [
        { name: "main.work", cumulativeTime: 3000000, flatTime: 1000000, cumulativePercent: 60, flatPercent: 20 },
      ],
      sampleUnit: "nanoseconds",
      sampleType: "cpu",
    };
    analyzeProfileMock.mockResolvedValue(analysisResult);
    const result = await analyzeProfile("/proj/.pprof/cpu-test.prof");
    expect(result).toEqual(analysisResult);
    expect(pprofState.analysis).toEqual(analysisResult);
    expect(pprofState.lastProfilePath).toBe("/proj/.pprof/cpu-test.prof");
  });

  it("analyzeProfile returns null on empty path", async () => {
    const result = await analyzeProfile("");
    expect(result).toBeNull();
    expect(analyzeProfileMock).not.toHaveBeenCalled();
  });

  it("analyzeProfile handles errors and clears analysis", async () => {
    pprofState.analysis = {
      totalSamples: 1,
      totalDuration: 1,
      topFunctions: [],
      sampleUnit: "ns",
      sampleType: "x",
    };
    analyzeProfileMock.mockRejectedValue(new Error("invalid profile"));
    const result = await analyzeProfile("/bad.prof");
    expect(result).toBeNull();
    expect(pprofState.analysis).toBeNull();
    expect(pprofState.lastError).toContain("invalid profile");
    expect(notifyErrorMock).toHaveBeenCalled();
  });
});
