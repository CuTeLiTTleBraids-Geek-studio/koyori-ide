import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Snapshot } from "@/types";

const mocks = vi.hoisted(() => ({
  on: vi.fn(),
  cancel: vi.fn(),
  notifyError: vi.fn(),
  notifySuccess: vi.fn(),
}));

vi.mock("@wailsio/runtime", () => ({
  Events: { On: mocks.on },
}));

vi.mock("@/lib/notifications", () => ({
  notifyError: mocks.notifyError,
  notifySuccess: mocks.notifySuccess,
}));

import {
  cleanupSnapshots,
  listSnapshots,
  millisecondsToGoDuration,
  resetSnapshotStore,
  setSnapshotBackend,
  setSnapshotWorkspaceRoot,
  snapshotState,
} from "./snapshot";

function makeSnapshot(id: string, workspaceRoot: string, reason: Snapshot["reason"] = "manual"): Snapshot {
  return {
    id,
    workspaceRoot,
    reason,
    createdAt: "2026-07-16T00:00:00Z",
    files: [],
    fileCount: 0,
  };
}

describe("snapshot local history", () => {
  const backend = {
    createSnapshot: vi.fn(async (root: string, reason: string) =>
      makeSnapshot(`snapshot-${backend.createSnapshot.mock.calls.length}`, root, reason as Snapshot["reason"]),
    ),
    restoreSnapshot: vi.fn(),
    restorePartial: vi.fn(),
    // GOAL-P1-01: exact-restore surface. Present so the mock still satisfies
    // SnapshotBackend; these tests cover local history, not restore semantics.
    calculateRestoreDiff: vi.fn(),
    restoreSnapshotExact: vi.fn(),
    listSnapshots: vi.fn<() => Promise<Snapshot[]>>(),
    deleteSnapshot: vi.fn(),
    diffSnapshots: vi.fn(),
    getSnapshot: vi.fn(),
    cleanupSnapshots: vi.fn(),
  };

  beforeEach(() => {
    window.location.hash = "";
    resetSnapshotStore();
    vi.clearAllMocks();
    mocks.on.mockImplementation(() => mocks.cancel);
    backend.listSnapshots.mockResolvedValue([]);
    backend.cleanupSnapshots.mockResolvedValue(0);
    setSnapshotBackend(backend);
  });

  function savedCallback(): (event: { data: string }) => void {
    const call = mocks.on.mock.calls.find(([name]) => name === "file:saved");
    if (!call) throw new Error("file:saved listener was not registered");
    return call[1] as (event: { data: string }) => void;
  }

  it("registers one listener and snapshots every save in the active workspace", async () => {
    setSnapshotWorkspaceRoot("C:\\work\\app");
    setSnapshotWorkspaceRoot("C:\\work\\app");
    expect(mocks.on.mock.calls.filter(([name]) => name === "file:saved")).toHaveLength(1);

    const onSaved = savedCallback();
    onSaved({ data: "C:\\work\\app\\src\\one.go" });
    onSaved({ data: "C:\\work\\app\\src\\two.go" });

    await vi.waitFor(() => expect(backend.createSnapshot).toHaveBeenCalledTimes(2));
    expect(backend.createSnapshot).toHaveBeenNthCalledWith(1, "C:\\work\\app", "file-save");
    expect(backend.createSnapshot).toHaveBeenNthCalledWith(2, "C:\\work\\app", "file-save");
    expect(snapshotState.snapshots).toHaveLength(2);
  });

  it("releases the local-history listener on reset and can register again", () => {
    setSnapshotWorkspaceRoot("C:\\work\\app");
    expect(mocks.on).toHaveBeenCalledTimes(1);

    resetSnapshotStore();
    resetSnapshotStore();
    expect(mocks.cancel).toHaveBeenCalledTimes(1);

    setSnapshotWorkspaceRoot("C:\\work\\app");
    expect(mocks.on).toHaveBeenCalledTimes(2);
  });

  it("ignores saved paths outside the active workspace", async () => {
    setSnapshotWorkspaceRoot("C:\\work\\app");
    savedCallback()({ data: "C:\\work\\other\\src\\one.go" });

    await Promise.resolve();
    expect(backend.createSnapshot).not.toHaveBeenCalled();
  });

  it("filters the timeline to the active workspace", async () => {
    setSnapshotWorkspaceRoot("C:\\work\\app");
    backend.listSnapshots.mockResolvedValue([
      makeSnapshot("app", "C:\\work\\app"),
      makeSnapshot("other", "C:\\work\\other"),
    ]);

    await listSnapshots();
    expect(snapshotState.snapshots.map(({ id }) => id)).toEqual(["app"]);
  });

  it("does not register a duplicate listener in the AI companion window", () => {
    window.location.hash = "/ai-window";
    setSnapshotWorkspaceRoot("C:\\work\\app");

    expect(mocks.on).not.toHaveBeenCalledWith("file:saved", expect.any(Function));
  });

  it("converts millisecond cleanup ages to Go duration nanoseconds", async () => {
    const sevenDaysMs = 7 * 24 * 60 * 60 * 1000;

    await cleanupSnapshots(20, sevenDaysMs);

    expect(millisecondsToGoDuration(sevenDaysMs)).toBe(604_800_000_000_000);
    expect(backend.cleanupSnapshots).toHaveBeenCalledWith(20, 604_800_000_000_000);
  });
});
