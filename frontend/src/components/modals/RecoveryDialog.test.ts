import { beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";

const actions = vi.hoisted(() => ({
  resolveRecoverableFile: vi.fn(),
  discardAllRecoverable: vi.fn(),
  finishRecovery: vi.fn(),
  undoLastRecoveryDecision: vi.fn(),
}));

vi.mock("@/stores/recovery", async () => {
  const { reactive } = await import("vue");
  return {
    recoveryState: reactive({
      visible: true,
      phase: "pending",
      scanning: false,
      error: null as string | null,
      scan: {
        workspaceRoot: "/ws",
        files: [],
        corrupt: [],
        totalBytes: 0,
      },
      decisions: [],
    }),
    ...actions,
  };
});

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({ t: (key: string) => key }),
}));

import RecoveryDialog from "./RecoveryDialog.vue";
import { recoveryState } from "@/stores/recovery";

const file = (status: "clean" | "conflict" | "missing", path: string) => ({
  path,
  windowId: "old-window",
  status,
  content: "recovered",
  diskContent: status === "missing" ? "" : "disk",
  encoding: "utf-8",
  eol: "lf",
  updatedAt: 1,
  baselineHash: "old",
  currentHash: "new",
});

function mountDialog() {
  return mount(RecoveryDialog, {
    global: { stubs: { Teleport: true } },
  });
}

describe("RecoveryDialog", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    recoveryState.visible = true;
    recoveryState.scanning = false;
    recoveryState.error = null;
    recoveryState.decisions = [];
    recoveryState.scan.files = [
      file("clean", "/ws/clean.ts"),
      file("conflict", "/ws/conflict.ts"),
      file("missing", "/ws/missing.ts"),
    ];
    recoveryState.scan.corrupt = [];
  });

  it("renders clean, conflict, and missing recovery decisions", () => {
    const wrapper = mountDialog();

    expect(wrapper.findAll(".recovery-dialog__file")).toHaveLength(3);
    expect(wrapper.text()).toContain("recovery.status.clean");
    expect(wrapper.text()).toContain("recovery.status.conflict");
    expect(wrapper.text()).toContain("recovery.status.missing");
    expect(wrapper.findAll(".recovery-dialog__restore")).toHaveLength(3);
    expect(wrapper.findAll(".recovery-dialog__keep-disk")).toHaveLength(2);
  });

  it("routes conflict choices to explicit merge and keep-disk actions", async () => {
    actions.resolveRecoverableFile.mockResolvedValue(true);
    const wrapper = mountDialog();
    const conflictRow = wrapper.findAll(".recovery-dialog__file")[1];

    await conflictRow.get(".recovery-dialog__restore").trigger("click");
    await flushPromises();
    await conflictRow.get(".recovery-dialog__keep-disk").trigger("click");

    expect(actions.resolveRecoverableFile).toHaveBeenNthCalledWith(
      1,
      expect.objectContaining({ path: "/ws/conflict.ts" }),
      "merge",
    );
    expect(actions.resolveRecoverableFile).toHaveBeenNthCalledWith(
      2,
      expect.objectContaining({ path: "/ws/conflict.ts" }),
      "keep-disk",
    );
  });

  it("offers undo before the final recovery commit", async () => {
    actions.undoLastRecoveryDecision.mockResolvedValue(true);
    recoveryState.scan.files = [];
    recoveryState.decisions = [{
      file: file("conflict", "/ws/conflict.ts"),
      decision: "keep-disk",
      previousFile: null,
      previousActivePath: null,
    }];
    const wrapper = mountDialog();

    await wrapper.get(".recovery-dialog__undo").trigger("click");
    await flushPromises();

    expect(actions.undoLastRecoveryDecision).toHaveBeenCalledOnce();
    expect(wrapper.find(".recovery-dialog__continue").exists()).toBe(true);
  });

  it("keeps scan failures visible until the user explicitly continues", () => {
    recoveryState.phase = "failed";
    recoveryState.error = "recovery directory is unreadable";
    recoveryState.scan.files = [];
    const wrapper = mountDialog();

    expect(wrapper.text()).toContain("recovery.scanFailed");
    expect(wrapper.find(".recovery-dialog__continue").exists()).toBe(true);
  });
});
