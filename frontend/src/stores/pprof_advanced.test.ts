import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  startBlock: vi.fn(),
  stopBlock: vi.fn(),
  startMutex: vi.fn(),
  stopMutex: vi.fn(),
  startTrace: vi.fn(),
  stopTrace: vi.fn(),
  analyzeTrace: vi.fn(),
  activeProfile: vi.fn(),
}));

vi.mock("@/api/services", () => ({
  pprofService: {
    isProfiling: vi.fn().mockResolvedValue(false),
    activeProfile: mocks.activeProfile,
    startBlockProfile: mocks.startBlock,
    stopBlockProfile: mocks.stopBlock,
    startMutexProfile: mocks.startMutex,
    stopMutexProfile: mocks.stopMutex,
    startTrace: mocks.startTrace,
    stopTrace: mocks.stopTrace,
    analyzeTrace: mocks.analyzeTrace,
  },
  fileService: { createDirectory: vi.fn() },
}));

vi.mock("@/stores/app", () => ({ appState: { currentProject: "/proj" } }));
vi.mock("@/stores/output", () => ({ pushOutput: vi.fn() }));
vi.mock("@/lib/notifications", () => ({ notifyError: vi.fn(), notifySuccess: vi.fn() }));

import {
  pprofState,
  refreshProfilingStatus,
  startBlockProfile,
  stopBlockProfile,
  startMutexProfile,
  stopMutexProfile,
  startTrace,
  stopTrace,
  analyzeTrace,
} from "./pprof";

describe("pprof advanced captures", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    mocks.activeProfile.mockResolvedValue("");
    pprofState.activeProfile = "";
    pprofState.activeOutputPath = "";
    pprofState.analysis = null;
  });

  it("recovers a safe stop path after reloading an active sample session", async () => {
    mocks.activeProfile.mockResolvedValue("block");
    await refreshProfilingStatus();

    expect(pprofState.activeProfile).toBe("block");
    const recoveredPath = pprofState.activeOutputPath;
    expect(recoveredPath).toContain("/proj/.pprof/block-");
    await stopBlockProfile(false);
    expect(mocks.stopBlock).toHaveBeenCalledWith(recoveredPath);
  });

  it("keeps a sample session visible when stop fails and the backend is still active", async () => {
    pprofState.activeProfile = "mutex";
    pprofState.activeOutputPath = "/proj/.pprof/mutex.prof";
    mocks.stopMutex.mockRejectedValueOnce(new Error("write failed"));
    mocks.activeProfile.mockResolvedValue("mutex");

    await stopMutexProfile(false);

    expect(pprofState.activeProfile).toBe("mutex");
    expect(pprofState.activeOutputPath).toBe("/proj/.pprof/mutex.prof");
  });

  it("runs block and mutex sessions and analyzes their output", async () => {
    await startBlockProfile();
    expect(mocks.startBlock).toHaveBeenCalledOnce();
    expect(pprofState.activeProfile).toBe("block");
    const blockPath = pprofState.activeOutputPath;
    await stopBlockProfile();
    expect(mocks.stopBlock).toHaveBeenCalledWith(blockPath);

    await startMutexProfile();
    expect(pprofState.activeProfile).toBe("mutex");
    const mutexPath = pprofState.activeOutputPath;
    await stopMutexProfile();
    expect(mocks.stopMutex).toHaveBeenCalledWith(mutexPath);
  });

  it("captures trace and converts a selected trace view", async () => {
    const analysis = { totalSamples: 1, totalDuration: 1, topFunctions: [], sampleUnit: "nanoseconds", sampleType: "delay", flameGraph: null };
    mocks.analyzeTrace.mockResolvedValue(analysis);

    await startTrace();
    expect(mocks.startTrace).toHaveBeenCalledWith(pprofState.activeOutputPath);
    const tracePath = pprofState.activeOutputPath;
    await stopTrace();
    expect(mocks.stopTrace).toHaveBeenCalledOnce();
    await analyzeTrace(tracePath, "sched");
    expect(mocks.analyzeTrace).toHaveBeenCalledWith(tracePath, "sched");
    expect(pprofState.analysis).toEqual(analysis);
  });
});
