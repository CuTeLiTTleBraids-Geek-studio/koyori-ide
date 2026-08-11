import { mount } from "@vue/test-utils";
import { describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  state: {
    snapshots: [
      {
        id: "history-1",
        createdAt: "2026-07-16T00:00:00Z",
        reason: "file-save",
        workspaceRoot: "C:\\work\\app",
        files: [],
        fileCount: 0,
      },
    ],
    selected: null,
    diff: null,
    selectedFilePaths: new Set<string>(),
  },
}));

vi.mock("@/stores/snapshot", () => ({
  snapshotState: mocks.state,
  listSnapshots: vi.fn(),
  selectSnapshot: vi.fn(),
  deleteSnapshot: vi.fn(),
  restoreSnapshot: vi.fn(),
  restorePartial: vi.fn(),
  toggleFileSelection: vi.fn(),
  toggleSelectAllFiles: vi.fn(),
  diffSnapshots: vi.fn(),
  createSnapshot: vi.fn(),
}));

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({ t: (key: string) => key }),
}));

import SnapshotTimeline from "./SnapshotTimeline.vue";

describe("SnapshotTimeline local history", () => {
  it("renders file-save snapshots with a dedicated history style", () => {
    const wrapper = mount(SnapshotTimeline);

    expect(wrapper.text()).toContain("snapshotTimeline.reason.file-save");
    expect(wrapper.find(".snapshot-timeline__dot.snap-reason--history").exists()).toBe(true);
  });
});
