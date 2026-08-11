import { describe, expect, it, vi } from "vitest";
import {
  createVscodeAPI,
  type VscodeHostBridge,
} from "./vscodeApi";

function commandBridge(extensionId = "acme.tools") {
  const executeCommand = vi.fn().mockResolvedValue("ok");
  const bridge = {
    extensionId,
    permissions: [],
    executeCommand,
    isCommandAllowed: (command: string) => command.startsWith(`${extensionId}.`),
  } as unknown as VscodeHostBridge;
  return { bridge, executeCommand };
}

describe("extension command allowlist", () => {
  it("does not expose a cross-extension metadata namespace", () => {
    const { bridge } = commandBridge();
    const api = createVscodeAPI(bridge);

    expect((api as unknown as Record<string, unknown>).extensions).toBeUndefined();
  });

  it("blocks commands outside the extension namespace before reaching the host", async () => {
    const { bridge, executeCommand } = commandBridge();
    const api = createVscodeAPI(bridge);

    await expect(api.commands.executeCommand("other.extension.run")).rejects.toThrow(
      /not allowed/i,
    );
    expect(executeCommand).not.toHaveBeenCalled();
  });

  it("blocks reserved workbench commands even when the extension id mimics the prefix", async () => {
    const { bridge, executeCommand } = commandBridge("workbench.action.terminal");
    const api = createVscodeAPI(bridge);

    await expect(
      api.commands.executeCommand("workbench.action.terminal.sendSequence"),
    ).rejects.toThrow(/not allowed/i);
    expect(executeCommand).not.toHaveBeenCalled();
  });

  it("forwards commands owned by the calling extension", async () => {
    const { bridge, executeCommand } = commandBridge();
    const api = createVscodeAPI(bridge);

    await expect(api.commands.executeCommand("acme.tools.refresh")).resolves.toBe("ok");
    expect(executeCommand).toHaveBeenCalledWith("acme.tools.refresh");
  });
});

describe("extension notification actions", () => {
  it.each([
    "showInformationMessage",
    "showWarningMessage",
    "showErrorMessage",
  ] as const)("does not auto-select an action for %s", async (method) => {
    const notify = vi.fn();
    const bridge = {
      extensionId: "acme.tools",
      permissions: ["ui.notifications"],
      notify,
    } as unknown as VscodeHostBridge;
    const api = createVscodeAPI(bridge);

    await expect(api.window[method]("Continue?", "Yes", "No")).resolves.toBeUndefined();
    expect(notify).toHaveBeenCalledOnce();
  });
});
