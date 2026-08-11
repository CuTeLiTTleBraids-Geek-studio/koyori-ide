/**
 * P9-G13 packaged probe: verifies the extension API no-fake-success contract
 * inside the real packaged renderer:
 *   - workspace.saveAll fails closed (KOYORI_IDE_EXT_API_UNSUPPORTED) when no
 *     save bridge is wired;
 *   - window.showInputBox / window.showQuickPick fail closed (no fake
 *     default/first-item result);
 *   - workspace.saveAll calls the injected bridge (real save) when wired.
 * Opt-in via VITE_KOYORI_IDE_E2E_MONACO=1 like the G10 Monaco probe.
 */
// Koyori IDE 模块 · Extension Api Probe。
// 喵，这是 Koyori IDE 的 Extension Api Probe 模块（前端实现）~
import { Events } from "@wailsio/runtime";
import type { ExtensionDescriptor } from "@/lib/extensionHost/extensionHost";

const resultEvent = "e2e:g13-extension-api-result";

interface ExtensionApiProbeConfig {
  runId: string;
}

interface ExtensionApiProbeResult {
  runId: string;
  saveAllNoBridgeFailsClosed: boolean;
  showInputBoxFailsClosed: boolean;
  showQuickPickFailsClosed: boolean;
  saveAllBridgeCallsRealSave: boolean;
  notificationRoutedToHost: boolean;
  outputChannelOperable: boolean;
  configurationBridged: boolean;
  treeViewRegistrationOperable: boolean;
  unsupportedErrorCode: string;
  ok: boolean;
  error?: string;
}

function message(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

async function runProbe(config: ExtensionApiProbeConfig): Promise<ExtensionApiProbeResult> {
  const { ExtensionHost } = await import("@/lib/extensionHost/extensionHost");
  const descriptor: ExtensionDescriptor = {
    id: "g13.ext",
    mainPath: "/exts/g13/main.js",
    permissions: ["fs.write"],
  };

  let saveAllNoBridgeFailsClosed = false;
  {
    const host = new ExtensionHost();
    let api: { workspace: { saveAll: () => Promise<unknown> } } | undefined;
    await host.activateWithModule(descriptor, {
      activate: (value: unknown) => {
        api = value as { workspace: { saveAll: () => Promise<unknown> } };
      },
    });
    try {
      await api!.workspace.saveAll();
    } catch (error: unknown) {
      saveAllNoBridgeFailsClosed = /KOYORI_IDE_EXT_API_UNSUPPORTED/.test(message(error));
    }
  }

  let showInputBoxFailsClosed = false;
  let showQuickPickFailsClosed = false;
  {
    const host = new ExtensionHost();
    let api: { window: { showInputBox: (options?: unknown) => Promise<unknown>; showQuickPick: (items?: unknown, options?: unknown) => Promise<unknown> } } | undefined;
    await host.activateWithModule({ ...descriptor, permissions: ["ui.notifications"] } as ExtensionDescriptor, {
      activate: (value: unknown) => {
        api = value as { window: { showInputBox: (options?: unknown) => Promise<unknown>; showQuickPick: (items?: unknown, options?: unknown) => Promise<unknown> } };
      },
    });
    try {
      await api!.window.showInputBox({ value: "fake" });
    } catch (error: unknown) {
      showInputBoxFailsClosed = /KOYORI_IDE_EXT_API_UNSUPPORTED/.test(message(error));
    }
    try {
      await api!.window.showQuickPick(["first"]);
    } catch (error: unknown) {
      showQuickPickFailsClosed = /KOYORI_IDE_EXT_API_UNSUPPORTED/.test(message(error));
    }
  }

  let saveAllBridgeCallsRealSave = false;
  {
    let bridgeCalled = false;
    const host = new ExtensionHost({
      onSaveAll: async () => {
        bridgeCalled = true;
        return { savedCount: 1, failedPaths: [] };
      },
    });
    let api: { workspace: { saveAll: () => Promise<unknown> } } | undefined;
    await host.activateWithModule(descriptor, {
      activate: (value: unknown) => {
        api = value as { workspace: { saveAll: () => Promise<unknown> } };
      },
    });
    try {
      const result = await api!.workspace.saveAll();
      saveAllBridgeCallsRealSave = result === true && bridgeCalled;
    } catch {
      saveAllBridgeCallsRealSave = false;
    }
  }

  // notification: the injected host surface must receive the message.
  let notificationRoutedToHost = false;
  {
    const onNotify = (level: string, messageText: string) => {
      notificationRoutedToHost = level === "error" && messageText.includes("g13-boom");
    };
    const host = new ExtensionHost({ onNotify });
    let api: { window: { showErrorMessage: (message: string) => Promise<unknown> } } | undefined;
    await host.activateWithModule({ ...descriptor, permissions: ["ui.notifications"] } as ExtensionDescriptor, {
      activate: (value: unknown) => {
        api = value as { window: { showErrorMessage: (message: string) => Promise<unknown> } };
      },
    });
    try {
      await api!.window.showErrorMessage("g13-boom");
    } catch {
      // resolution undefined is fine; the onNotify capture is the assertion
    }
  }

  // output: createOutputChannel must return an operable in-memory channel.
  let outputChannelOperable = false;
  {
    const host = new ExtensionHost();
    let api: { window: { createOutputChannel: (name: string) => { appendLine: (v: string) => void; dispose: () => void; show: () => void; clear: () => void } } } | undefined;
    await host.activateWithModule(descriptor, {
      activate: (value: unknown) => {
        api = value as { window: { createOutputChannel: (name: string) => { appendLine: (v: string) => void; dispose: () => void; show: () => void; clear: () => void } } };
      },
    });
    try {
      const channel = api!.window.createOutputChannel("g13-output");
      channel.appendLine("hello g13");
      channel.show();
      channel.clear();
      channel.dispose();
      outputChannelOperable = true;
    } catch {
      outputChannelOperable = false;
    }
  }

  // configuration: getConfiguration must see the bridged settings snapshot.
  let configurationBridged = false;
  {
    const host = new ExtensionHost({
      onGetConfiguration: (section?: string) => (section === "g13" ? { enabled: true } : {}),
    });
    let api: { workspace: { getConfiguration: (section?: string) => { get: (key: string) => unknown } } } | undefined;
    await host.activateWithModule(descriptor, {
      activate: (value: unknown) => {
        api = value as { workspace: { getConfiguration: (section?: string) => { get: (key: string) => unknown } } };
      },
    });
    try {
      configurationBridged = api!.workspace.getConfiguration("g13").get("enabled") === true;
    } catch {
      configurationBridged = false;
    }
  }

  // view: registerTreeDataProvider must register and dispose cleanly.
  let treeViewRegistrationOperable = false;
  {
    const host = new ExtensionHost();
    let api: { window: { registerTreeDataProvider: (viewId: string, provider: unknown) => { dispose: () => void } } } | undefined;
    await host.activateWithModule(descriptor, {
      activate: (value: unknown) => {
        api = value as { window: { registerTreeDataProvider: (viewId: string, provider: unknown) => { dispose: () => void } } };
      },
    });
    try {
      const disposable = api!.window.registerTreeDataProvider("g13-view", {
        getChildren: () => [],
        getTreeItem: () => ({}),
      });
      disposable.dispose();
      treeViewRegistrationOperable = true;
    } catch {
      treeViewRegistrationOperable = false;
    }
  }

  const ok =
    saveAllNoBridgeFailsClosed &&
    showInputBoxFailsClosed &&
    showQuickPickFailsClosed &&
    saveAllBridgeCallsRealSave &&
    notificationRoutedToHost &&
    outputChannelOperable &&
    configurationBridged &&
    treeViewRegistrationOperable;
  return {
    runId: config.runId,
    saveAllNoBridgeFailsClosed,
    showInputBoxFailsClosed,
    showQuickPickFailsClosed,
    saveAllBridgeCallsRealSave,
    notificationRoutedToHost,
    outputChannelOperable,
    configurationBridged,
    treeViewRegistrationOperable,
    unsupportedErrorCode: "KOYORI_IDE_EXT_API_UNSUPPORTED",
    ok,
    error: ok ? undefined : `saveAllNoBridge=${saveAllNoBridgeFailsClosed} inputBox=${showInputBoxFailsClosed} quickPick=${showQuickPickFailsClosed} bridge=${saveAllBridgeCallsRealSave} notify=${notificationRoutedToHost} output=${outputChannelOperable} config=${configurationBridged} view=${treeViewRegistrationOperable}`,
  };
}

export function installExtensionApiProbe(): void {
  const target = globalThis as typeof globalThis & {
    __koyoriIdeRunG13ExtensionApiProbe?: (config: ExtensionApiProbeConfig) => Promise<void>;
  };
  target.__koyoriIdeRunG13ExtensionApiProbe = async (config) => {
    let result: ExtensionApiProbeResult;
    try {
      result = await runProbe(config);
    } catch (error: unknown) {
      result = {
        runId: config.runId,
        saveAllNoBridgeFailsClosed: false,
        showInputBoxFailsClosed: false,
        showQuickPickFailsClosed: false,
        saveAllBridgeCallsRealSave: false,
        notificationRoutedToHost: false,
        outputChannelOperable: false,
        configurationBridged: false,
        treeViewRegistrationOperable: false,
        unsupportedErrorCode: "KOYORI_IDE_EXT_API_UNSUPPORTED",
        ok: false,
        error: message(error),
      };
    }
    await Events.Emit(resultEvent, result);
  };
}