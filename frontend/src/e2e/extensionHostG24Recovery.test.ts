import { describe, expect, it, vi } from "vitest";
import { waitForWorkerReplacement } from "./extensionHostG24Recovery";

function fakeClock() {
  let current = 0;
  return {
    now: () => current,
    sleep: async (milliseconds: number) => {
      current += milliseconds;
    },
  };
}

describe("G24 Worker recovery evidence", () => {
  it("waits for a new runtime identity before accepting the recovered version", async () => {
    const clock = fakeClock();
    const runtimeIds = ["runtime-old", "runtime-old", "runtime-new"];
    const readVersion = vi.fn().mockResolvedValue("2.0.0");

    const result = await waitForWorkerReplacement({
      previousRuntimeId: "runtime-old",
      expectedVersion: "2.0.0",
      readRuntimeId: vi.fn(async () => runtimeIds.shift() ?? "runtime-new"),
      readVersion,
      timeoutMs: 1_000,
      pollIntervalMs: 25,
      ...clock,
    });

    expect(result.runtimeId).toBe("runtime-new");
    expect(result.latestError).toBe("Worker runtime identity did not change");
    expect(readVersion).toHaveBeenCalledTimes(1);
  });

  it("rejects a responsive Worker whose runtime identity never changes", async () => {
    const clock = fakeClock();

    await expect(
      waitForWorkerReplacement({
        previousRuntimeId: "runtime-old",
        expectedVersion: "2.0.0",
        readRuntimeId: vi.fn().mockResolvedValue("runtime-old"),
        readVersion: vi.fn().mockResolvedValue("2.0.0"),
        timeoutMs: 100,
        pollIntervalMs: 25,
        ...clock,
      }),
    ).rejects.toMatchObject({
      name: "WorkerReplacementTimeoutError",
      message:
        "Worker recovery timed out: Worker runtime identity did not change",
      latestError: "Worker runtime identity did not change",
    });
  });

  it("also requires the replacement Worker to expose the expected version", async () => {
    const clock = fakeClock();
    const versions = ["1.0.0", "2.0.0"];

    const result = await waitForWorkerReplacement({
      previousRuntimeId: "runtime-old",
      expectedVersion: "2.0.0",
      readRuntimeId: vi.fn().mockResolvedValue("runtime-new"),
      readVersion: vi.fn(async () => versions.shift() ?? "2.0.0"),
      timeoutMs: 100,
      pollIntervalMs: 25,
      ...clock,
    });

    expect(result.runtimeId).toBe("runtime-new");
    expect(result.latestError).toBe("reactivated version was 1.0.0");
  });
});
