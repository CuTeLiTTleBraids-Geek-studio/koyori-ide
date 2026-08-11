/**
 * G-VSC-04: Tests for unified command aggregation.
 *
 * Verifies that native plugin commands and VS Code extension commands are
 * merged into a single list with native commands first (priority), correct
 * source labels, and conflict handling (same label → both retained, native
 * wins, warning logged).
 *
 * Native commands are registered through the real pluginRegistry via the
 * test-only activatePluginWithModule entry point (same pattern as
 * pluginRegistry.test.ts). VS Code commands go through the vscodeExtensions
 * registry. Both registries are reset between tests.
 */

import { describe, it, expect, beforeEach, vi } from "vitest";

// Mock @/stores/app to break the Monaco editor import chain in jsdom (same
// reason as pluginRegistry.test.ts: activatePlugin imports appState).
vi.mock("@/stores/app", () => ({
  appState: {
    currentProject: "",
    language: "en",
  },
  loadSettings: vi.fn(),
}));

// Mock @/stores/output so logConflictWarning's lazy import resolves without
// pulling in the Output store's full dependency graph. Capture calls so the
// conflict-warning test can assert a warning was emitted.
const pushOutputMock = vi.fn();
vi.mock("@/stores/output", () => ({
  pushOutput: pushOutputMock,
}));

import {
  syncPlugins,
  activatePluginWithModule,
  clearRegistry,
  type NknkAPI,
} from "@/lib/pluginRegistry";
import {
  registerVscodeExtensionCommand,
  clearVscodeExtensions,
} from "@/lib/vscodeExtensions";
import {
  getUnifiedCommands,
  toPaletteCommand,
  getUnifiedPaletteCommands,
} from "@/lib/unifiedCommands";
import { getExtensionCommandPrefix } from "@/lib/pluginSandbox";
import type { PluginInfo, PluginManifest } from "@/types";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function makeManifest(overrides: Partial<PluginManifest> = {}): PluginManifest {
  return {
    name: "test-plugin",
    version: "1.0.0",
    main: "main.js",
    activationEvents: ["onStartup"],
    ...overrides,
  };
}

function makePluginInfo(
  manifestOverrides: Partial<PluginManifest> = {},
  infoOverrides: Partial<PluginInfo> = {},
): PluginInfo {
  return {
    manifest: makeManifest(manifestOverrides),
    path: "/plugins/test-plugin",
    source: "user",
    enabled: true,
    mainExists: true,
    ...infoOverrides,
  };
}

/**
 * Register a native command with an explicit title + optional category by
 * crafting the manifest contributes directly. The command's title/category
 * come from the manifest's contributes.commands (matching pluginRegistry
 * behavior, where registerCommandImpl reads the contributed title).
 */
async function registerNativeCommandWithTitle(
  pluginName: string,
  cmdId: string,
  title: string,
  category?: string,
  handler: (...args: unknown[]) => unknown = () => undefined,
): Promise<void> {
  syncPlugins([
    makePluginInfo({
      name: pluginName,
      contributes: {
        commands: [{ id: cmdId, title, category }],
      },
    }),
  ]);
  await activatePluginWithModule(pluginName, {
    activate: (ctx: NknkAPI) => {
      ctx.commands.register(cmdId, handler);
    },
  });
}

function registerVscodeCmd(
  extensionId: string,
  cmdId: string,
  label: string,
  category?: string,
  handler: (...args: unknown[]) => unknown = () => undefined,
): void {
  registerVscodeExtensionCommand({
    id: cmdId,
    extensionId,
    label,
    category,
    handler,
  });
}

function nativeCommandId(pluginName: string, commandName: string): string {
  return `${getExtensionCommandPrefix(pluginName)}${commandName}`;
}

beforeEach(() => {
  clearRegistry();
  clearVscodeExtensions();
  pushOutputMock.mockClear();
  vi.restoreAllMocks();
});

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("getUnifiedCommands - ordering", () => {
  it("returns an empty list when no commands are registered", () => {
    expect(getUnifiedCommands()).toEqual([]);
  });

  it("places native commands before VS Code extension commands", async () => {
    // Register a VS Code command first to ensure ordering is by source
    // priority, not insertion order.
    registerVscodeCmd("ms-python.python", "python.run", "Run Python File");
    await registerNativeCommandWithTitle(
      "native-runner",
      nativeCommandId("native-runner", "run"),
      "Run Native",
    );

    const commands = getUnifiedCommands();
    expect(commands).toHaveLength(2);
    expect(commands[0].source).toBe("native");
    expect(commands[1].source).toBe("vscode");
  });

  it("keeps all native commands grouped before all VS Code commands", async () => {
    // syncPlugins replaces the registry, so both native plugins must be
    // synced in a single call before activating each.
    syncPlugins([
      makePluginInfo({
        name: "native.a",
        contributes: {
          commands: [{ id: nativeCommandId("native.a", "one"), title: "Native A1" }],
        },
      }),
      makePluginInfo({
        name: "native.b",
        contributes: {
          commands: [{ id: nativeCommandId("native.b", "one"), title: "Native B1" }],
        },
      }),
    ]);
    await activatePluginWithModule("native.a", {
      activate: (ctx: NknkAPI) => {
        ctx.commands.register(nativeCommandId("native.a", "one"), () => undefined);
      },
    });
    await activatePluginWithModule("native.b", {
      activate: (ctx: NknkAPI) => {
        ctx.commands.register(nativeCommandId("native.b", "one"), () => undefined);
      },
    });
    registerVscodeCmd("ext.a", "ext.a.1", "Ext A1");
    registerVscodeCmd("ext.b", "ext.b.1", "Ext B1");

    const commands = getUnifiedCommands();
    expect(commands).toHaveLength(4);
    // First two are native, last two are vscode.
    expect(commands[0].source).toBe("native");
    expect(commands[1].source).toBe("native");
    expect(commands[2].source).toBe("vscode");
    expect(commands[3].source).toBe("vscode");
  });
});

describe("getUnifiedCommands - source labels", () => {
  it("labels native commands with source 'native' and a 'native.' id prefix", async () => {
    const commandId = nativeCommandId("my-plugin", "cmd");
    await registerNativeCommandWithTitle("my-plugin", commandId, "My Command");

    const [cmd] = getUnifiedCommands();
    expect(cmd.source).toBe("native");
    expect(cmd.id).toBe(`native.my-plugin.${commandId}`);
    expect(cmd.label).toBe("My Command");
  });

  it("labels VS Code commands with source 'vscode' and a 'vscode.' id prefix", () => {
    registerVscodeCmd("ms-python.python", "python.run", "Run Python");

    const [cmd] = getUnifiedCommands();
    expect(cmd.source).toBe("vscode");
    expect(cmd.id).toBe("vscode.ms-python.python.python.run");
    expect(cmd.label).toBe("Run Python");
  });

  it("prefixes the label with category when present (native and vscode)", async () => {
    await registerNativeCommandWithTitle("p", nativeCommandId("p", "c"), "Title", "Cat");
    registerVscodeCmd("e", "e.c", "Title", "Cat");

    const commands = getUnifiedCommands();
    expect(commands[0].label).toBe("Cat: Title");
    expect(commands[1].label).toBe("Cat: Title");
    expect(commands[0].category).toBe("Cat");
    expect(commands[1].category).toBe("Cat");
  });
});

describe("getUnifiedCommands - conflict priority", () => {
  it("retains both commands when native and vscode share a label, native first", async () => {
    const nativeHandler = vi.fn().mockReturnValue("native-result");
    const vscodeHandler = vi.fn().mockReturnValue("vscode-result");

    await registerNativeCommandWithTitle(
      "fmt",
      nativeCommandId("fmt", "format"),
      "Format Code",
      undefined,
      nativeHandler,
    );
    registerVscodeCmd("ext.fmt", "ext.fmt.format", "Format Code", undefined, vscodeHandler);

    const commands = getUnifiedCommands();
    expect(commands).toHaveLength(2);
    // Native wins ordering (priority).
    expect(commands[0].source).toBe("native");
    expect(commands[0].label).toBe("Format Code");
    expect(commands[1].source).toBe("vscode");
    expect(commands[1].label).toBe("Format Code");
  });

  it("logs a warning when a native and vscode command share a label", async () => {
    const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});
    await registerNativeCommandWithTitle(
      "fmt",
      nativeCommandId("fmt", "format"),
      "Format Code",
    );
    registerVscodeCmd("ext.fmt", "ext.fmt.format", "Format Code");

    getUnifiedCommands();

    // console.warn is called synchronously inside logConflictWarning before
    // the awaited output-store import, so it's observable immediately.
    expect(warnSpy).toHaveBeenCalledTimes(1);
    const msg = warnSpy.mock.calls[0][0] as string;
    expect(msg).toContain("conflict");
    expect(msg).toContain("Format Code");
    expect(msg).toContain("ext.fmt");
  });

  it("does not log a conflict warning when labels differ", async () => {
    const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});
    await registerNativeCommandWithTitle(
      "fmt",
      nativeCommandId("fmt", "format"),
      "Format Code",
    );
    registerVscodeCmd("ext.fmt", "ext.fmt.lint", "Lint Code");

    getUnifiedCommands();

    expect(warnSpy).not.toHaveBeenCalled();
  });

  it("native handler is the one exposed for the native entry on conflict", async () => {
    const nativeHandler = vi.fn().mockReturnValue("native-result");
    await registerNativeCommandWithTitle(
      "fmt",
      nativeCommandId("fmt", "format"),
      "Format Code",
      undefined,
      nativeHandler,
    );
    registerVscodeCmd("ext.fmt", "ext.fmt.format", "Format Code", undefined, () => "vscode-result");

    const commands = getUnifiedCommands();
    // Invoke the native command's handler (fire-and-forget wrapper). The
    // wrapper calls executePluginCommand which resolves to the native handler.
    await commands[0].handler();
    expect(nativeHandler).toHaveBeenCalled();
  });
});

describe("toPaletteCommand / getUnifiedPaletteCommands", () => {
  it("maps a UnifiedCommand to a Command with the source field set", async () => {
    await registerNativeCommandWithTitle("p", nativeCommandId("p", "c"), "Title");
    const [unified] = getUnifiedCommands();
    const palette = toPaletteCommand(unified);
    expect(palette.id).toBe(unified.id);
    expect(palette.label).toBe(unified.label);
    expect(palette.source).toBe("native");
    expect(typeof palette.action).toBe("function");
  });

  it("getUnifiedPaletteCommands returns Command[] with sources for both kinds", async () => {
    await registerNativeCommandWithTitle(
      "p",
      nativeCommandId("p", "c"),
      "Native Title",
    );
    registerVscodeCmd("e", "e.c", "Ext Title");

    const palette = getUnifiedPaletteCommands();
    expect(palette).toHaveLength(2);
    expect(palette[0].source).toBe("native");
    expect(palette[1].source).toBe("vscode");
  });

  it("built-in commands (no source) are not produced by the aggregator", async () => {
    await registerNativeCommandWithTitle("p", nativeCommandId("p", "c"), "Title");
    const palette = getUnifiedPaletteCommands();
    expect(palette.every((c) => c.source === "native" || c.source === "vscode")).toBe(true);
  });
});
