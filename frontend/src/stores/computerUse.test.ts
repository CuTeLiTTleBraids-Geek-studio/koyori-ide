import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  computerUseState,
  executeComputerUseOperation,
  resetComputerUseStore,
  setComputerUseBackend,
  type ComputerUseBackend,
} from "@/stores/computerUse";

function createBackend(): ComputerUseBackend {
  return {
    getConfig: vi.fn().mockResolvedValue({ enabled: true, confirmationRequired: true }),
    updateConfig: vi.fn(),
    isEnabled: vi.fn().mockResolvedValue(true),
    getAuditLog: vi.fn().mockResolvedValue([]),
    requestOperationApproval: vi.fn().mockResolvedValue("computer-use-token"),
    executeApprovedOperation: vi.fn().mockResolvedValue({}),
    startRecording: vi.fn(),
    stopRecording: vi.fn().mockResolvedValue([]),
    isRecording: vi.fn().mockResolvedValue(false),
  };
}

describe("executeComputerUseOperation", () => {
  beforeEach(() => resetComputerUseStore());

  it("is disabled by default", () => {
    expect(computerUseState.config).toMatchObject({
      enabled: false,
      confirmationRequired: true,
    });
  });

  it("requests a capability before consuming it", async () => {
    const backend = createBackend();
    setComputerUseBackend(backend);

    const result = await executeComputerUseOperation(
      "mouse_move",
      '{"x":10,"y":20}',
    );

    expect(backend.requestOperationApproval).toHaveBeenCalledWith(
      "mouse_move",
      '{"x":10,"y":20}',
    );
    expect(backend.executeApprovedOperation).toHaveBeenCalledWith(
      "computer-use-token",
    );
    expect(
      vi.mocked(backend.requestOperationApproval).mock.invocationCallOrder[0],
    ).toBeLessThan(
      vi.mocked(backend.executeApprovedOperation).mock.invocationCallOrder[0],
    );
    expect(result).toEqual({});
  });

  it("does not consume a capability when approval is denied", async () => {
    const backend = createBackend();
    vi.mocked(backend.requestOperationApproval).mockRejectedValue(
      new Error("denied"),
    );
    setComputerUseBackend(backend);

    await expect(
      executeComputerUseOperation("mouse_click", '{"button":"left"}'),
    ).resolves.toBeNull();
    expect(computerUseState.error).toBe("denied");
    expect(backend.executeApprovedOperation).not.toHaveBeenCalled();
  });
});
