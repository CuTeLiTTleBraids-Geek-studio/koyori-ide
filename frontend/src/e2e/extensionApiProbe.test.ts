import { describe, expect, it } from "vitest";
import { runExtensionApiProbe } from "./extensionApiProbe";

describe("G13 extension API probe", () => {
  it("requires a wired host Output panel and records the channel lifecycle", async () => {
    const result = await runExtensionApiProbe({ runId: "g13-output-wired" });
    expect(result.outputChannelOperable).toBe(true);
    expect(result.saveAllNoBridgeFailsClosed).toBe(true);
    expect(result.showInputBoxFailsClosed).toBe(true);
    expect(result.showQuickPickFailsClosed).toBe(true);
    expect(result.saveAllBridgeCallsRealSave).toBe(true);
    expect(result.notificationRoutedToHost).toBe(true);
    expect(result.configurationBridged).toBe(true);
    expect(result.treeViewRegistrationOperable).toBe(true);
    expect(result.ok).toBe(true);
    expect(result.error).toBeUndefined();
  });
});
