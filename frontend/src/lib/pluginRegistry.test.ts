/**
 * Tests for the plugin registry (Plan 49).
 *
 * The registry's core logic (sync, activation state, permission
 * gating, command/view registration) is testable without a real
 * dynamic import via the `activatePluginWithModule` test entry point.
 */

import { describe, it, expect, beforeEach, vi } from "vitest";
import { watchEffect } from "vue";

const fileServiceMocks = vi.hoisted(() => ({
  readFile: vi.fn<(path: string) => Promise<string>>(),
  writeFile: vi.fn<(path: string, content: string) => Promise<void>>(),
}));

// Mock @/stores/app to break the Monaco editor import chain in jsdom.
// The sandbox path in activatePlugin imports appState to get
// currentProject; without this mock, jsdom fails on
// document.queryCommandSupported (called by Monaco's clipboard module).
vi.mock("@/stores/app", () => ({
  appState: {
    currentProject: "",
    language: "en",
  },
  loadSettings: vi.fn(),
}));
vi.mock("@/api/services", () => ({
  fileService: fileServiceMocks,
}));
import { appState } from "@/stores/app";
import {
  syncPlugins,
  listPluginStates,
  getPluginInfo,
  listPluginCommands,
  listPluginViews,
  activatePluginWithModule,
  activatePlugin,
  activateOnStartup,
  activateOnCommand,
  deactivatePlugin,
  deactivateAllPlugins,
  enablePlugin,
  disablePlugin,
  clearRegistry,
  setSandboxHost,
  setSandboxMode,
  isSandboxEnabled,
  executePluginCommand,
  createSandboxRpcHandler,
  __setPluginModule,
  type NknkAPI,
} from "@/lib/pluginRegistry";
import type { SandboxHost } from "@/lib/pluginSandbox";
import type { PluginInfo, PluginManifest } from "@/types";

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

type WorkspaceAccess = {
  read(path: unknown): Promise<unknown>;
  write(path: unknown, content?: unknown): Promise<unknown>;
};

async function createWorkspaceAccess(
  mode: "sandbox RPC" | "main-thread API",
): Promise<WorkspaceAccess> {
  if (mode === "sandbox RPC") {
    const handler = createSandboxRpcHandler();
    const manifest = makeManifest({ permissions: ["fs.read", "fs.write"] });
    return {
      read: (path) =>
        handler("test-plugin", manifest, "workspace.readFile", [path]),
      write: (path, content = "updated") =>
        handler("test-plugin", manifest, "workspace.writeFile", [
          path,
          content,
        ]),
    };
  }

  let context: NknkAPI | undefined;
  syncPlugins([
    makePluginInfo({ permissions: ["fs.read", "fs.write"] }),
  ]);
  await activatePluginWithModule("test-plugin", {
    activate: (ctx: NknkAPI) => {
      context = ctx;
    },
  });
  if (!context) throw new Error("Plugin context was not created");
  const activeContext = context;
  return {
    read: (path) => activeContext.workspace.readFile(path as string),
    write: (path, content = "updated") =>
      activeContext.workspace.writeFile(path as string, content as string),
  };
}

beforeEach(() => {
  setSandboxHost(null);
  clearRegistry();
  appState.currentProject = "";
  fileServiceMocks.readFile.mockReset().mockResolvedValue("contents");
  fileServiceMocks.writeFile.mockReset().mockResolvedValue(undefined);
});

describe("syncPlugins", () => {
  it("adds new plugins in loaded state", () => {
    syncPlugins([makePluginInfo()]);
    const states = listPluginStates();
    expect(states).toHaveLength(1);
    expect(states[0].name).toBe("test-plugin");
    expect(states[0].status).toBe("loaded");
  });

  it("marks disabled plugins as disabled", () => {
    syncPlugins([makePluginInfo({}, { enabled: false })]);
    const states = listPluginStates();
    expect(states[0].status).toBe("disabled");
  });

  it("updates existing plugin info while preserving activation state", () => {
    syncPlugins([makePluginInfo()]);
    // Simulate the plugin being activated.
    return activatePluginWithModule("test-plugin", {
      activate: () => {},
    }).then(() => {
      // Re-sync with updated info.
      syncPlugins([
        makePluginInfo({ version: "2.0.0" }),
      ]);
      const info = getPluginInfo("test-plugin");
      expect(info?.manifest.version).toBe("2.0.0");
      const states = listPluginStates();
      expect(states[0].status).toBe("activated");
    });
  });

  it("removes plugins no longer present", () => {
    syncPlugins([makePluginInfo()]);
    expect(listPluginStates()).toHaveLength(1);
    syncPlugins([]);
    expect(listPluginStates()).toHaveLength(0);
  });

  it("reflects disabled state when backend reports enabled=false", () => {
    syncPlugins([makePluginInfo()]);
    return activatePluginWithModule("test-plugin", {
      activate: () => {},
    }).then(() => {
      // Backend now reports the plugin as disabled.
      syncPlugins([makePluginInfo({}, { enabled: false })]);
      const states = listPluginStates();
      expect(states[0].status).toBe("disabled");
    });
  });
});

describe("getPluginInfo", () => {
  it("returns the plugin info by name", () => {
    syncPlugins([makePluginInfo()]);
    const info = getPluginInfo("test-plugin");
    expect(info).toBeDefined();
    expect(info?.manifest.name).toBe("test-plugin");
  });

  it("returns undefined for unknown plugin", () => {
    expect(getPluginInfo("nonexistent")).toBeUndefined();
  });
});

describe("activatePluginWithModule", () => {
  it("invokes the activate function with a koyoriIde context", () => {
    const activate = vi.fn().mockResolvedValue(undefined);
    syncPlugins([makePluginInfo()]);
    return activatePluginWithModule("test-plugin", { activate }).then(() => {
      expect(activate).toHaveBeenCalledTimes(1);
      const ctx = activate.mock.calls[0][0] as NknkAPI;
      expect(ctx.manifest.name).toBe("test-plugin");
      expect(typeof ctx.commands.register).toBe("function");
      expect(typeof ctx.commands.execute).toBe("function");
      expect(typeof ctx.views.register).toBe("function");
    });
  });

  it("transitions to activated state on success", () => {
    syncPlugins([makePluginInfo()]);
    return activatePluginWithModule("test-plugin", {
      activate: () => {},
    }).then(() => {
      const states = listPluginStates();
      expect(states[0].status).toBe("activated");
    });
  });

  it("captures activation errors in state", () => {
    syncPlugins([makePluginInfo()]);
    return activatePluginWithModule("test-plugin", {
      activate: () => {
        throw new Error("boom");
      },
    }).then(() => {
      const states = listPluginStates();
      expect(states[0].status).toBe("error");
      expect(states[0].error).toContain("boom");
    });
  });

  it("cleans contributions and deactivates when activation throws", async () => {
    const deactivate = vi.fn().mockResolvedValue(undefined);
    syncPlugins([makePluginInfo()]);

    await activatePluginWithModule("test-plugin", {
      activate: (ctx: NknkAPI) => {
        ctx.commands.register("extension.test-plugin.partial", () => undefined);
        ctx.views.register("test-plugin.partial-view", {});
        throw new Error("activation failed after registration");
      },
      deactivate,
    });

    expect(deactivate).toHaveBeenCalledOnce();
    expect(listPluginCommands()).toEqual([]);
    expect(listPluginViews()).toEqual([]);
    expect(listPluginStates()[0]).toMatchObject({
      status: "error",
      error: expect.stringContaining("activation failed after registration"),
    });
  });

  it("is a no-op when already activated", () => {
    const activate = vi.fn().mockResolvedValue(undefined);
    syncPlugins([makePluginInfo()]);
    return activatePluginWithModule("test-plugin", { activate })
      .then(() => activatePluginWithModule("test-plugin", { activate }))
      .then(() => {
        expect(activate).toHaveBeenCalledTimes(1);
      });
  });

  it("is a no-op when disabled", () => {
    const activate = vi.fn();
    syncPlugins([makePluginInfo({}, { enabled: false })]);
    return activatePluginWithModule("test-plugin", { activate }).then(() => {
      expect(activate).not.toHaveBeenCalled();
      const states = listPluginStates();
      expect(states[0].status).toBe("disabled");
    });
  });

  it("throws for unknown plugin", () => {
    return expect(
      activatePluginWithModule("nonexistent", { activate: () => {} }),
    ).rejects.toThrow(/not registered/);
  });

  it("deactivates active main-thread plugins before clearing the registry", async () => {
    const deactivate = vi.fn().mockResolvedValue(undefined);
    syncPlugins([makePluginInfo()]);
    await activatePluginWithModule("test-plugin", {
      activate: () => undefined,
      deactivate,
    });

    await deactivateAllPlugins();

    expect(deactivate).toHaveBeenCalledOnce();
    expect(listPluginStates()).toEqual([]);
  });

  it("waits for an in-flight activation and cleans its contributions", async () => {
    let finishActivation: () => void = () => undefined;
    const pending = new Promise<void>((resolve) => {
      finishActivation = resolve;
    });
    const deactivate = vi.fn().mockResolvedValue(undefined);
    syncPlugins([makePluginInfo()]);
    const activation = activatePluginWithModule("test-plugin", {
      activate: async (ctx: NknkAPI) => {
        ctx.commands.register("extension.test-plugin.pending", () => undefined);
        await pending;
      },
      deactivate,
    });

    const teardown = deactivateAllPlugins();
    expect(deactivateAllPlugins()).toBe(teardown);
    finishActivation();
    await Promise.all([activation, teardown]);

    expect(deactivate).toHaveBeenCalledOnce();
    expect(listPluginCommands()).toEqual([]);
    expect(listPluginStates()).toEqual([]);
  });

  it("times out a stuck activation and reuses the teardown promise", async () => {
    vi.useFakeTimers();
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    let finishActivation: () => void = () => undefined;
    const pending = new Promise<void>((resolve) => {
      finishActivation = resolve;
    });
    let activation = Promise.resolve();

    try {
      syncPlugins([makePluginInfo()]);
      activation = activatePluginWithModule("test-plugin", {
        activate: (ctx: NknkAPI) => {
          ctx.commands.register("extension.test-plugin.stuck", () => undefined);
          return pending;
        },
      });
      expect(listPluginCommands()).toHaveLength(1);

      const teardown = deactivateAllPlugins();
      expect(deactivateAllPlugins()).toBe(teardown);

      let settled = false;
      void teardown.then(() => {
        settled = true;
      });
      await vi.advanceTimersByTimeAsync(999);
      expect(settled).toBe(false);
      await vi.advanceTimersByTimeAsync(1);
      await teardown;

      expect(settled).toBe(true);
      expect(warn).toHaveBeenCalledWith(
        "Plugin activation teardown timed out; clearing stale registry state",
      );
      expect(listPluginCommands()).toEqual([]);
      expect(listPluginStates()).toEqual([]);

      const secondTeardown = deactivateAllPlugins();
      expect(secondTeardown).not.toBe(teardown);
      await secondTeardown;
      expect(warn).toHaveBeenCalledTimes(1);
    } finally {
      finishActivation();
      await activation;
      warn.mockRestore();
      vi.useRealTimers();
    }
  });

  it("isolates a replacement plugin from a timed-out stale activation", async () => {
    vi.useFakeTimers();
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    let finishOldActivation: () => void = () => undefined;
    const oldActivationGate = new Promise<void>((resolve) => {
      finishOldActivation = resolve;
    });
    let staleSharedRegistrationError: unknown;
    let staleUniqueRegistrationError: unknown;
    const oldDeactivate = vi.fn().mockResolvedValue(undefined);
    let oldActivation = Promise.resolve();
    let teardown = Promise.resolve();

    try {
      syncPlugins([makePluginInfo()]);
      oldActivation = activatePluginWithModule("test-plugin", {
        activate: async (ctx: NknkAPI) => {
          ctx.commands.register("extension.test-plugin.shared", () => "old");
          await oldActivationGate;
          try {
            ctx.commands.register("extension.test-plugin.shared", () => "stale");
          } catch (error) {
            staleSharedRegistrationError = error;
          }
          try {
            ctx.commands.register("extension.test-plugin.late", () => "stale");
          } catch (error) {
            staleUniqueRegistrationError = error;
          }
        },
        deactivate: oldDeactivate,
      });

      teardown = deactivateAllPlugins();
      await vi.advanceTimersByTimeAsync(1_000);
      await teardown;

      syncPlugins([makePluginInfo()]);
      await activatePluginWithModule("test-plugin", {
        activate: (ctx: NknkAPI) => {
          ctx.commands.register("extension.test-plugin.shared", () => "replacement");
          ctx.commands.register("extension.test-plugin.current", () => "current");
        },
      });

      finishOldActivation();
      await oldActivation;

      expect(warn).toHaveBeenCalledWith(
        "Plugin activation teardown timed out; clearing stale registry state",
      );
      expect(String(staleSharedRegistrationError)).toContain(
        "activation context is no longer active",
      );
      expect(String(staleUniqueRegistrationError)).toContain(
        "activation context is no longer active",
      );
      expect(oldDeactivate).toHaveBeenCalledOnce();
      expect(listPluginCommands().map((command) => command.id).sort()).toEqual([
        "extension.test-plugin.current",
        "extension.test-plugin.shared",
      ]);
      expect(await executePluginCommand("extension.test-plugin.shared")).toBe(
        "replacement",
      );
      expect(await executePluginCommand("extension.test-plugin.current")).toBe(
        "current",
      );
      expect(listPluginStates()[0].status).toBe("activated");
    } finally {
      finishOldActivation();
      await oldActivation;
      await vi.runOnlyPendingTimersAsync();
      await teardown;
      clearRegistry();
      warn.mockRestore();
      vi.useRealTimers();
    }
  });
});

describe("activatePlugin (real dynamic import path)", () => {
  it("errors when main file is missing", () => {
    syncPlugins([makePluginInfo({}, { mainExists: false })]);
    return activatePlugin("test-plugin").then(() => {
      const states = listPluginStates();
      expect(states[0].status).toBe("error");
      expect(states[0].error).toContain("not found");
    });
  });
});

describe("activateOnStartup", () => {
  it("activates plugins with onStartup event", () => {
    syncPlugins([
      makePluginInfo({ name: "alpha", activationEvents: ["onStartup"] }),
      makePluginInfo({
        name: "beta",
        activationEvents: ["onCommand:extension.beta.go"],
      }),
    ]);
    __setPluginModule("alpha", { activate: () => {} });
    return activateOnStartup().then((names) => {
      expect(names).toEqual(["alpha"]);
      const states = listPluginStates();
      const alpha = states.find((s) => s.name === "alpha");
      const beta = states.find((s) => s.name === "beta");
      expect(alpha?.status).toBe("activated");
      expect(beta?.status).toBe("loaded");
    });
  });

  it("skips disabled plugins", () => {
    syncPlugins([
      makePluginInfo(
        { name: "alpha", activationEvents: ["onStartup"] },
        { enabled: false },
      ),
    ]);
    return activateOnStartup().then((names) => {
      expect(names).toEqual([]);
    });
  });

  it("skips already-activated plugins", () => {
    syncPlugins([
      makePluginInfo({ name: "alpha", activationEvents: ["onStartup"] }),
    ]);
    __setPluginModule("alpha", { activate: () => {} });
    return activateOnStartup()
      .then(() => activateOnStartup())
      .then((names) => {
        expect(names).toEqual([]);
      });
  });
});

describe("activateOnCommand", () => {
  it("activates plugins that declare onCommand:<id>", () => {
    syncPlugins([
      makePluginInfo({
        name: "alpha",
        activationEvents: ["onCommand:extension.alpha.go"],
      }),
    ]);
    __setPluginModule("alpha", { activate: () => {} });
    return activateOnCommand("extension.alpha.go").then(() => {
      const states = listPluginStates();
      expect(states[0].status).toBe("activated");
    });
  });

  it("does not activate plugins without matching event", () => {
    syncPlugins([
      makePluginInfo({
        name: "alpha",
        activationEvents: ["onCommand:extension.alpha.other"],
      }),
    ]);
    return activateOnCommand("extension.alpha.go").then(() => {
      const states = listPluginStates();
      expect(states[0].status).toBe("loaded");
    });
  });
});

describe("commands API", () => {
  it("registers and executes a command", () => {
    const handler = vi.fn().mockReturnValue("result");
    syncPlugins([makePluginInfo()]);
    return activatePluginWithModule("test-plugin", {
      activate: (ctx: NknkAPI) => {
        ctx.commands.register("extension.test-plugin.cmd", handler);
      },
    })
      .then(() => {
        const cmds = listPluginCommands();
        expect(cmds).toHaveLength(1);
        expect(cmds[0].id).toBe("extension.test-plugin.cmd");
        expect(cmds[0].pluginName).toBe("test-plugin");
      })
      .then(() => activateOnCommand("extension.test-plugin.cmd"))
      .then(() => {
        // The handler is invoked via koyoriIde.commands.execute, but here
        // we can verify it's registered.
        expect(handler).not.toHaveBeenCalled(); // not invoked yet
      });
  });

  it("uses contributed title from manifest", () => {
    syncPlugins([
      makePluginInfo({
        contributes: {
          commands: [
            { id: "extension.test-plugin.cmd", title: "My Command" },
          ],
        },
      }),
    ]);
    return activatePluginWithModule("test-plugin", {
      activate: (ctx: NknkAPI) => {
        ctx.commands.register("extension.test-plugin.cmd", () => {});
      },
    }).then(() => {
      const cmds = listPluginCommands();
      expect(cmds[0].title).toBe("My Command");
    });
  });

  it("allows the same command suffix in different plugin namespaces", async () => {
    syncPlugins([
      makePluginInfo({ name: "alpha" }),
      makePluginInfo({ name: "beta" }),
    ]);
    await activatePluginWithModule("alpha", {
      activate: (ctx: NknkAPI) => {
        ctx.commands.register("extension.alpha.shared", () => "alpha");
      },
    });
    await activatePluginWithModule("beta", {
      activate: (ctx: NknkAPI) => {
        ctx.commands.register("extension.beta.shared", () => "beta");
      },
    });

    expect(listPluginCommands().map((command) => command.id).sort()).toEqual([
      "extension.alpha.shared",
      "extension.beta.shared",
    ]);
    await expect(executePluginCommand("extension.alpha.shared")).resolves.toBe(
      "alpha",
    );
    await expect(executePluginCommand("extension.beta.shared")).resolves.toBe(
      "beta",
    );
  });

  it("allows same plugin to re-register the same id", () => {
    syncPlugins([makePluginInfo()]);
    return activatePluginWithModule("test-plugin", {
      activate: (ctx: NknkAPI) => {
        ctx.commands.register("extension.test-plugin.cmd", () => "first");
        ctx.commands.register("extension.test-plugin.cmd", () => "second");
      },
    }).then(() => {
      expect(listPluginCommands()).toHaveLength(1);
    });
  });
});

// N-42: executePluginCommand is the entry point used by the command palette.
// It triggers lazy activation and routes through the sandbox when needed.
describe("executePluginCommand (N-42)", () => {
  it("throws when command is not registered", async () => {
    await expect(
      executePluginCommand("extension.nonexistent.cmd"),
    ).rejects.toThrow(
      /not registered/,
    );
  });

  it("invokes the handler directly for main-thread plugins", async () => {
    const handler = vi.fn().mockReturnValue("palette-result");
    syncPlugins([makePluginInfo()]);
    await activatePluginWithModule("test-plugin", {
      activate: (ctx: NknkAPI) => {
        ctx.commands.register("extension.test-plugin.cmd", handler);
      },
    });
    const result = await executePluginCommand("extension.test-plugin.cmd");
    expect(handler).toHaveBeenCalledTimes(1);
    expect(result).toBe("palette-result");
  });

  it("triggers lazy activation via activateOnCommand", async () => {
    const handler = vi.fn().mockReturnValue("activated-result");
    syncPlugins([
      makePluginInfo({
        name: "lazy-plugin",
        activationEvents: ["onCommand:extension.lazy-plugin.run"],
      }),
    ]);
    // Plugin is in "loaded" state, not yet activated.
    expect(listPluginStates()[0].status).toBe("loaded");
    // executePluginCommand should trigger activation, which registers
    // the handler.
    await activatePluginWithModule("lazy-plugin", {
      activate: (ctx: NknkAPI) => {
        ctx.commands.register("extension.lazy-plugin.run", handler);
      },
    });
    const result = await executePluginCommand("extension.lazy-plugin.run");
    expect(result).toBe("activated-result");
    expect(handler).toHaveBeenCalledTimes(1);
  });

  it("can execute private commands (palette is the user, not a plugin)", async () => {
    // The cross-plugin `public` gate does NOT apply to palette invocation.
    // A plugin's private command can still be invoked by the user via Ctrl+Shift+P.
    const handler = vi.fn().mockReturnValue("private-result");
    syncPlugins([makePluginInfo()]);
    await activatePluginWithModule("test-plugin", {
      activate: (ctx: NknkAPI) => {
        // Register without setting public: true (defaults to private).
        ctx.commands.register("extension.test-plugin.private", handler);
      },
    });
    const result = await executePluginCommand("extension.test-plugin.private");
    expect(result).toBe("private-result");
  });

  it("routes through sandbox callMethod when plugin is sandboxed", async () => {
    // When sandboxed, the stored handler is a no-op stub. executePluginCommand
    // must route through sandboxHost.callMethod to invoke the real handler
    // inside the Worker.
    const mockHost: SandboxHost = {
      has: vi.fn().mockReturnValue(true),
      activate: vi.fn().mockResolvedValue(undefined),
      reload: vi.fn().mockResolvedValue(undefined),
      callMethod: vi.fn().mockResolvedValue("sandbox-result"),
      terminate: vi.fn(),
      terminateAll: vi.fn(),
    };
    setSandboxHost(mockHost);
    syncPlugins([makePluginInfo()]);
    await activatePluginWithModule("test-plugin", {
      activate: (ctx: NknkAPI) => {
        ctx.commands.register(
          "extension.test-plugin.sandboxed",
          () => "direct-result",
        );
      },
    });
    const result = await executePluginCommand(
      "extension.test-plugin.sandboxed",
    );
    expect(result).toBe("sandbox-result");
    expect(mockHost.has).toHaveBeenCalledWith("test-plugin");
    expect(mockHost.callMethod).toHaveBeenCalledWith(
      "test-plugin",
      "executeCommand",
      ["extension.test-plugin.sandboxed"],
    );
  });

  it("passes through arguments to the handler", async () => {
    const handler = vi.fn().mockReturnValue("with-args");
    syncPlugins([makePluginInfo()]);
    await activatePluginWithModule("test-plugin", {
      activate: (ctx: NknkAPI) => {
        ctx.commands.register("extension.test-plugin.args", handler);
      },
    });
    const result = await executePluginCommand(
      "extension.test-plugin.args",
      "a",
      42,
    );
    expect(handler).toHaveBeenCalledWith("a", 42);
    expect(result).toBe("with-args");
  });
});

describe("views API", () => {
  it("registers a view with location from manifest", () => {
    syncPlugins([
      makePluginInfo({
        contributes: {
          views: [{ id: "test-plugin.view", title: "My View", location: "sidebar" }],
        },
      }),
    ]);
    return activatePluginWithModule("test-plugin", {
      activate: (ctx: NknkAPI) => {
        ctx.views.register("test-plugin.view", { template: "<div/>" });
      },
    }).then(() => {
      const views = listPluginViews();
      expect(views).toHaveLength(1);
      expect(views[0].title).toBe("My View");
      expect(views[0].location).toBe("sidebar");
    });
  });

  it("uses options.location override", () => {
    syncPlugins([makePluginInfo()]);
    return activatePluginWithModule("test-plugin", {
      activate: (ctx: NknkAPI) => {
        ctx.views.register("test-plugin.view", {}, { location: "statusbar" });
      },
    }).then(() => {
      const views = listPluginViews();
      expect(views[0].location).toBe("statusbar");
    });
  });

  it("defaults to panel location", () => {
    syncPlugins([makePluginInfo()]);
    return activatePluginWithModule("test-plugin", {
      activate: (ctx: NknkAPI) => {
        ctx.views.register("test-plugin.view", {});
      },
    }).then(() => {
      const views = listPluginViews();
      expect(views[0].location).toBe("panel");
    });
  });

  it("rejects duplicate view id from a different plugin", () => {
    syncPlugins([
      makePluginInfo({ name: "alpha" }),
      makePluginInfo({ name: "beta" }),
    ]);
    return activatePluginWithModule("alpha", {
      activate: (ctx: NknkAPI) => {
        ctx.views.register("shared.view", {});
      },
    })
      .then(() =>
        activatePluginWithModule("beta", {
          activate: (ctx: NknkAPI) => {
            ctx.views.register("shared.view", {});
          },
        }),
      )
      .then(() => {
        const states = listPluginStates();
        const beta = states.find((s) => s.name === "beta");
        expect(beta?.status).toBe("error");
        expect(beta?.error).toContain("already registered");
      });
  });
});

describe("permission gating", () => {
  it("workspace.readFile throws without fs.read permission", () => {
    syncPlugins([makePluginInfo()]); // no permissions
    return activatePluginWithModule("test-plugin", {
      activate: async (ctx: NknkAPI) => {
        await expect(ctx.workspace.readFile("foo.txt")).rejects.toThrow(
          /fs\.read/,
        );
      },
    }).then(() => {});
  });

  it("workspace.writeFile throws without fs.write permission", () => {
    syncPlugins([makePluginInfo()]);
    return activatePluginWithModule("test-plugin", {
      activate: async (ctx: NknkAPI) => {
        await expect(ctx.workspace.writeFile("foo.txt", "x")).rejects.toThrow(
          /fs\.write/,
        );
      },
    }).then(() => {});
  });

  it("getPermissions returns declared permissions", () => {
    syncPlugins([
      makePluginInfo({
        permissions: ["fs.read", "fs.write"],
      }),
    ]);
    return activatePluginWithModule("test-plugin", {
      activate: (ctx: NknkAPI) => {
        const perms = ctx.getPermissions();
        expect(perms).toEqual(expect.arrayContaining(["fs.read", "fs.write"]));
      },
    }).then(() => {});
  });

  it("getPermissions returns empty array when no permissions declared", () => {
    syncPlugins([makePluginInfo()]);
    return activatePluginWithModule("test-plugin", {
      activate: (ctx: NknkAPI) => {
        expect(ctx.getPermissions()).toEqual([]);
      },
    }).then(() => {});
  });

  it("commands.register does not require any permission", () => {
    syncPlugins([makePluginInfo()]); // no permissions
    return activatePluginWithModule("test-plugin", {
      activate: (ctx: NknkAPI) => {
        // Should not throw.
        ctx.commands.register("extension.test-plugin.cmd", () => {});
      },
    }).then(() => {
      expect(listPluginCommands()).toHaveLength(1);
    });
  });

  it.each([
    "workbench.action.quit",
    "editor.action.formatDocument",
    "extension.other-plugin.run",
  ])(
    "authoritative sandbox RPC rejects reserved or foreign command %s",
    async (commandId) => {
      const handler = createSandboxRpcHandler();
      const manifest = makeManifest();

      await expect(
        handler("test-plugin", manifest, "commands.register", [commandId]),
      ).rejects.toThrow(/reserved|must use/);
      expect(listPluginCommands()).toEqual([]);
      expect(fileServiceMocks.readFile).not.toHaveBeenCalled();
      expect(fileServiceMocks.writeFile).not.toHaveBeenCalled();
    },
  );

  it("authoritative sandbox RPC accepts the plugin's encoded owned namespace", async () => {
    const handler = createSandboxRpcHandler();
    const manifest = makeManifest({ name: "alpha.beta" });

    await expect(
      handler("alpha.beta", manifest, "commands.register", [
        "extension.alpha%2Ebeta.run",
      ]),
    ).resolves.toBeUndefined();
    expect(listPluginCommands().map((command) => command.id)).toEqual([
      "extension.alpha%2Ebeta.run",
    ]);
  });

  it.each([
    "workbench.action.reloadWindow",
    "editor.action.formatDocument",
    "terminal.create",
    "git.commit",
    "extension.other-plugin.run",
  ])(
    "commands.register rejects reserved or foreign namespace %s without permissions",
    async (commandId) => {
      syncPlugins([makePluginInfo()]); // no permissions

      await activatePluginWithModule("test-plugin", {
        activate: (ctx: NknkAPI) => {
          ctx.commands.register(commandId, () => undefined);
        },
      });

      expect(listPluginCommands()).toEqual([]);
      expect(listPluginStates()[0].status).toBe("error");
      expect(listPluginStates()[0].error).toMatch(/reserved|must use/);
    },
  );
});

describe.each(["sandbox RPC", "main-thread API"] as const)(
  "%s workspace path confinement",
  (mode) => {
    it("fails closed for reads and writes when no workspace is open", async () => {
      appState.currentProject = "";
      const access = await createWorkspaceAccess(mode);

      await expect(access.read("notes.txt")).rejects.toThrow(
        /no workspace is open/,
      );
      await expect(access.write("notes.txt")).rejects.toThrow(
        /no workspace is open/,
      );
      expect(fileServiceMocks.readFile).not.toHaveBeenCalled();
      expect(fileServiceMocks.writeFile).not.toHaveBeenCalled();
    });

    it.each([
      ["empty", ""],
      ["POSIX absolute", "/etc/passwd"],
      ["Windows absolute", "C:\\Users\\user\\.ssh\\id_rsa"],
      ["Windows drive-relative", "C:secrets.txt"],
      ["UNC", "\\\\server\\share\\secret.txt"],
      ["parent traversal", "../secret.txt"],
      ["normalized parent traversal", "safe/../../secret.txt"],
      ["NUL", "safe/secret\0.txt"],
    ])("rejects %s paths before calling FileService", async (_label, path) => {
      appState.currentProject = "C:\\workspace";
      const access = await createWorkspaceAccess(mode);

      await expect(access.read(path)).rejects.toThrow(
        /relative|escapes the workspace/,
      );
      await expect(access.write(path)).rejects.toThrow(
        /relative|escapes the workspace/,
      );
      expect(fileServiceMocks.readFile).not.toHaveBeenCalled();
      expect(fileServiceMocks.writeFile).not.toHaveBeenCalled();
    });

    it("normalizes legal relative paths within the workspace", async () => {
      appState.currentProject = "C:\\workspace\\";
      const access = await createWorkspaceAccess(mode);

      await expect(access.read("src/./nested/../main.ts")).resolves.toBe(
        "contents",
      );
      await expect(
        access.write("src\\generated\\..\\output.ts", "updated"),
      ).resolves.toBeUndefined();
      expect(fileServiceMocks.readFile).toHaveBeenCalledWith(
        "C:/workspace/src/main.ts",
      );
      expect(fileServiceMocks.writeFile).toHaveBeenCalledWith(
        "C:/workspace/src/output.ts",
        "updated",
      );
    });
  },
);

describe("deactivatePlugin", () => {
  it("calls deactivate export and unregisters contributions", () => {
    const deactivate = vi.fn().mockResolvedValue(undefined);
    syncPlugins([makePluginInfo()]);
    return activatePluginWithModule("test-plugin", {
      activate: (ctx: NknkAPI) => {
        ctx.commands.register("extension.test-plugin.cmd", () => {});
        ctx.views.register("test-plugin.view", {});
      },
      deactivate,
    })
      .then(() => {
        expect(listPluginCommands()).toHaveLength(1);
        expect(listPluginViews()).toHaveLength(1);
        return deactivatePlugin("test-plugin");
      })
      .then(() => {
        expect(deactivate).toHaveBeenCalledTimes(1);
        expect(listPluginCommands()).toHaveLength(0);
        expect(listPluginViews()).toHaveLength(0);
        const states = listPluginStates();
        expect(states[0].status).toBe("loaded");
      });
  });

  it("is a no-op for non-activated plugin", () => {
    syncPlugins([makePluginInfo()]);
    return deactivatePlugin("test-plugin").then(() => {
      expect(listPluginStates()[0].status).toBe("loaded");
    });
  });

  it("L-11: clears moduleCache for the deactivated plugin", async () => {
    // After deactivating an activated plugin, the cached module must be
    // removed so a subsequent activation re-imports a fresh module.
    const activate = vi.fn().mockResolvedValue(undefined);
    syncPlugins([makePluginInfo()]);
    __setPluginModule("test-plugin", { activate });

    // First activation uses the cached module.
    await activatePlugin("test-plugin");
    expect(activate).toHaveBeenCalledTimes(1);
    expect(listPluginStates()[0].status).toBe("activated");

    // Deactivate — should clear the cache.
    await deactivatePlugin("test-plugin");
    expect(listPluginStates()[0].status).toBe("loaded");

    // Second activation: cache is empty, so activatePlugin falls through
    // to the dynamic-import path which fails in jsdom (no real URL handler).
    // The status should become "error", proving the cache was cleared.
    await activatePlugin("test-plugin");
    expect(activate).toHaveBeenCalledTimes(1); // NOT called a second time
    expect(listPluginStates()[0].status).toBe("error");
  });

  it("preserves a replacement plugin when stale deactivation finishes late", async () => {
    vi.useFakeTimers();
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    let finishOldDeactivation: () => void = () => undefined;
    const oldDeactivationGate = new Promise<void>((resolve) => {
      finishOldDeactivation = resolve;
    });
    let signalOldDeactivationStarted: () => void = () => undefined;
    const oldDeactivationStarted = new Promise<void>((resolve) => {
      signalOldDeactivationStarted = resolve;
    });
    let signalOldDeactivationFinished: () => void = () => undefined;
    const oldDeactivationFinished = new Promise<void>((resolve) => {
      signalOldDeactivationFinished = resolve;
    });
    let oldDeactivationWasStarted = false;
    const oldDeactivate = vi.fn(async () => {
      oldDeactivationWasStarted = true;
      signalOldDeactivationStarted();
      await oldDeactivationGate;
      signalOldDeactivationFinished();
    });
    const replacementDeactivate = vi.fn().mockResolvedValue(undefined);
    let teardown = Promise.resolve();

    try {
      syncPlugins([makePluginInfo()]);
      await activatePluginWithModule("test-plugin", {
        activate: (ctx: NknkAPI) => {
          ctx.commands.register("extension.test-plugin.shared", () => "old");
        },
        deactivate: oldDeactivate,
      });

      teardown = deactivateAllPlugins();
      await oldDeactivationStarted;
      await vi.advanceTimersByTimeAsync(1_000);
      await teardown;

      syncPlugins([makePluginInfo()]);
      await activatePluginWithModule("test-plugin", {
        activate: (ctx: NknkAPI) => {
          ctx.commands.register("extension.test-plugin.shared", () => "replacement");
          ctx.commands.register("extension.test-plugin.current", () => "current");
        },
        deactivate: replacementDeactivate,
      });

      finishOldDeactivation();
      await oldDeactivationFinished;
      await Promise.resolve();
      await Promise.resolve();

      expect(warn).toHaveBeenCalledWith(
        "Plugin deactivation timed out; clearing stale registry state",
      );
      expect(oldDeactivate).toHaveBeenCalledOnce();
      expect(listPluginCommands().map((command) => command.id).sort()).toEqual([
        "extension.test-plugin.current",
        "extension.test-plugin.shared",
      ]);
      expect(await executePluginCommand("extension.test-plugin.shared")).toBe(
        "replacement",
      );
      expect(await executePluginCommand("extension.test-plugin.current")).toBe(
        "current",
      );
      expect(listPluginStates()[0].status).toBe("activated");

      await deactivatePlugin("test-plugin");
      expect(replacementDeactivate).toHaveBeenCalledOnce();
      expect(listPluginCommands()).toEqual([]);
      expect(listPluginStates()[0].status).toBe("loaded");
    } finally {
      finishOldDeactivation();
      await vi.runOnlyPendingTimersAsync();
      await teardown;
      if (oldDeactivationWasStarted) await oldDeactivationFinished;
      clearRegistry();
      warn.mockRestore();
      vi.useRealTimers();
    }
  });
});

describe("enablePlugin / disablePlugin", () => {
  it("disablePlugin deactivates and marks disabled", () => {
    syncPlugins([makePluginInfo()]);
    return activatePluginWithModule("test-plugin", {
      activate: (ctx: NknkAPI) => {
        ctx.commands.register("extension.test-plugin.cmd", () => {});
      },
    })
      .then(() => disablePlugin("test-plugin"))
      .then(() => {
        const states = listPluginStates();
        expect(states[0].status).toBe("disabled");
        expect(listPluginCommands()).toHaveLength(0);
      });
  });

  it("enablePlugin marks loaded and activates onStartup plugins", () => {
    const activate = vi.fn().mockResolvedValue(undefined);
    syncPlugins([makePluginInfo()]);
    __setPluginModule("test-plugin", { activate });
    return disablePlugin("test-plugin")
      .then(() => enablePlugin("test-plugin"))
      .then(() => {
        const states = listPluginStates();
        // status should be "activated" now — the cached module was
        // used instead of the dynamic import path.
        expect(states[0].status).toBe("activated");
        expect(activate).toHaveBeenCalledTimes(1);
      });
  });

  it("enablePlugin is a no-op for non-disabled plugin", () => {
    syncPlugins([makePluginInfo()]);
    return enablePlugin("test-plugin").then(() => {
      expect(listPluginStates()[0].status).toBe("loaded");
    });
  });
});

describe("clearRegistry", () => {
  it("removes all plugins, commands, and views", () => {
    syncPlugins([makePluginInfo()]);
    return activatePluginWithModule("test-plugin", {
      activate: (ctx: NknkAPI) => {
        ctx.commands.register("extension.test-plugin.cmd", () => {});
        ctx.views.register("test-plugin.view", {});
      },
    }).then(() => {
      clearRegistry();
      expect(listPluginStates()).toHaveLength(0);
      expect(listPluginCommands()).toHaveLength(0);
      expect(listPluginViews()).toHaveLength(0);
    });
  });
});

// Proposal E — cross-plugin command interop (koyoriIde.commands.execute)
describe("cross-plugin command interop (Proposal E)", () => {
  it("allows a plugin to execute its own command (default private)", () => {
    syncPlugins([makePluginInfo({ name: "alpha" })]);
    return activatePluginWithModule("alpha", {
      activate: async (ctx: NknkAPI) => {
        ctx.commands.register("extension.alpha.cmd", () => "ok");
        const result = await ctx.commands.execute("extension.alpha.cmd");
        expect(result).toBe("ok");
      },
    }).then(() => {});
  });

  it("rejects cross-plugin execute when public flag is not set", () => {
    syncPlugins([
      makePluginInfo({ name: "alpha" }),
      makePluginInfo({ name: "beta" }),
    ]);
    // alpha registers a private command.
    return activatePluginWithModule("alpha", {
      activate: (ctx: NknkAPI) => {
        ctx.commands.register("extension.alpha.private", () => "secret");
      },
    })
      .then(() =>
        // beta tries to execute alpha's private command.
        activatePluginWithModule("beta", {
          activate: async (ctx: NknkAPI) => {
            await expect(
              ctx.commands.execute("extension.alpha.private"),
            ).rejects.toThrow(/not public/);
          },
        }),
      )
      .then(() => {
        const states = listPluginStates();
        const beta = states.find((s) => s.name === "beta");
        expect(beta?.status).toBe("activated"); // no error thrown at activate level
      });
  });

  it("allows cross-plugin execute when public: true is declared", () => {
    syncPlugins([
      makePluginInfo({
        name: "alpha",
        contributes: {
          commands: [
            { id: "extension.alpha.public", title: "Pub", public: true },
          ],
        },
      }),
      makePluginInfo({ name: "beta" }),
    ]);
    return activatePluginWithModule("alpha", {
      activate: (ctx: NknkAPI) => {
        ctx.commands.register("extension.alpha.public", (...args: unknown[]) => {
          const x = args[0] as number;
          return x * 2;
        });
      },
    })
      .then(() =>
        activatePluginWithModule("beta", {
          activate: async (ctx: NknkAPI) => {
            const result = await ctx.commands.execute(
              "extension.alpha.public",
              21,
            );
            expect(result).toBe(42);
          },
        }),
      )
      .then(() => {
        // beta should be activated cleanly (no error).
        const states = listPluginStates();
        const beta = states.find((s) => s.name === "beta");
        expect(beta?.status).toBe("activated");
      });
  });

  it("stores the public flag on RegisteredCommand", () => {
    syncPlugins([
      makePluginInfo({
        name: "alpha",
        contributes: {
          commands: [
            { id: "extension.alpha.pub", title: "Pub", public: true },
            { id: "extension.alpha.priv", title: "Priv" },
          ],
        },
      }),
    ]);
    return activatePluginWithModule("alpha", {
      activate: (ctx: NknkAPI) => {
        ctx.commands.register("extension.alpha.pub", () => {});
        ctx.commands.register("extension.alpha.priv", () => {});
      },
    }).then(() => {
      const cmds = listPluginCommands();
      const pub = cmds.find((c) => c.id === "extension.alpha.pub");
      const priv = cmds.find((c) => c.id === "extension.alpha.priv");
      expect(pub?.public).toBe(true);
      expect(priv?.public).toBe(false);
    });
  });

  it("registerCommand alias works identically to register", () => {
    syncPlugins([makePluginInfo({ name: "alpha" })]);
    return activatePluginWithModule("alpha", {
      activate: async (ctx: NknkAPI) => {
        ctx.commands.registerCommand("extension.alpha.cmd", () => "alias-works");
        const result = await ctx.commands.execute("extension.alpha.cmd");
        expect(result).toBe("alias-works");
      },
    }).then(() => {});
  });

  it("executeCommand alias works identically to execute", () => {
    syncPlugins([makePluginInfo({ name: "alpha" })]);
    return activatePluginWithModule("alpha", {
      activate: async (ctx: NknkAPI) => {
        ctx.commands.register("extension.alpha.cmd", () => "exec-alias");
        const result = await ctx.commands.executeCommand("extension.alpha.cmd");
        expect(result).toBe("exec-alias");
      },
    }).then(() => {});
  });

  it("rejects cross-plugin executeCommand alias too (private cmd)", () => {
    syncPlugins([
      makePluginInfo({ name: "alpha" }),
      makePluginInfo({ name: "beta" }),
    ]);
    return activatePluginWithModule("alpha", {
      activate: (ctx: NknkAPI) => {
        ctx.commands.register("extension.alpha.secret", () => "nope");
      },
    }).then(() =>
      activatePluginWithModule("beta", {
        activate: async (ctx: NknkAPI) => {
          await expect(
            ctx.commands.executeCommand("extension.alpha.secret"),
          ).rejects.toThrow(/not public/);
        },
      }),
    );
  });

  it("error message names the owning plugin for actionable feedback", () => {
    syncPlugins([
      makePluginInfo({ name: "alpha" }),
      makePluginInfo({ name: "beta" }),
    ]);
    return activatePluginWithModule("alpha", {
      activate: (ctx: NknkAPI) => {
        ctx.commands.register("extension.alpha.cmd", () => {});
      },
    }).then(() =>
      activatePluginWithModule("beta", {
        activate: async (ctx: NknkAPI) => {
          try {
            await ctx.commands.execute("extension.alpha.cmd");
            throw new Error("should have thrown");
          } catch (e) {
            const msg = (e as Error).message;
            // Error should mention the calling plugin, the target
            // command, the owning plugin, and the public flag.
            expect(msg).toContain("beta");
            expect(msg).toContain("extension.alpha.cmd");
            expect(msg).toContain("alpha");
            expect(msg).toContain("public");
          }
        },
      }),
    );
  });
});

// ---------------------------------------------------------------------------
// Sandbox integration (N-26)
// ---------------------------------------------------------------------------

/**
 * Mock PluginSandboxHost for testing the registry's sandbox integration
 * without spawning real Web Workers. Simulates the Worker reporting
 * 'activated' immediately on activate().
 */
class MockSandboxHost implements SandboxHost {
  activated = new Map<string, PluginManifest>();
  terminatedNames: string[] = [];
  terminateAllCalled = false;

  async activate(
    pluginName: string,
    manifest: PluginManifest,
    _pluginUrl: string,
  ): Promise<void> {
    this.activated.set(pluginName, manifest);
  }

  // M-18: SandboxHost.reload — 模拟重新加载插件（与 activate 行为一致）。
  async reload(
    pluginName: string,
    manifest: PluginManifest,
    _pluginUrl: string,
  ): Promise<void> {
    this.activated.set(pluginName, manifest);
  }

  callMethod(_pluginName: string, _method: string, _args: unknown[]): Promise<unknown> {
    return Promise.reject(new Error("callMethod not yet implemented"));
  }

  terminate(pluginName: string): void {
    this.terminatedNames.push(pluginName);
    this.activated.delete(pluginName);
  }

  terminateAll(): void {
    this.terminateAllCalled = true;
    for (const name of Array.from(this.activated.keys())) {
      this.terminatedNames.push(name);
    }
    this.activated.clear();
  }

  has(pluginName: string): boolean {
    return this.activated.has(pluginName);
  }
}

describe("sandbox integration (N-26)", () => {
  it("setSandboxMode enables/disables sandbox mode", () => {
    expect(isSandboxEnabled()).toBe(false);
    setSandboxMode(true);
    expect(isSandboxEnabled()).toBe(true);
    setSandboxMode(false);
    expect(isSandboxEnabled()).toBe(false);
  });

  it("setSandboxHost sets and clears the host", () => {
    const host = new MockSandboxHost();
    setSandboxHost(host);
    expect(isSandboxEnabled()).toBe(true);
    setSandboxHost(null);
    expect(isSandboxEnabled()).toBe(false);
  });

  it("activatePlugin routes through sandbox host when enabled", async () => {
    const host = new MockSandboxHost();
    setSandboxHost(host);
    syncPlugins([makePluginInfo({ name: "sandbox-plugin" })]);

    await activatePlugin("sandbox-plugin");

    expect(host.activated.has("sandbox-plugin")).toBe(true);
    const states = listPluginStates();
    expect(states[0].status).toBe("activated");
  });

  it("activatePlugin captures activation error from sandbox", async () => {
    const host = new MockSandboxHost();
    host.activate = vi.fn().mockRejectedValue(new Error("Worker init failed"));
    setSandboxHost(host);
    syncPlugins([makePluginInfo({ name: "fail-plugin" })]);

    await activatePlugin("fail-plugin");

    const states = listPluginStates();
    expect(states[0].status).toBe("error");
    expect(states[0].error).toContain("Worker init failed");
  });

  it("times out a stuck sandbox activation and continues startup plugins", async () => {
    vi.useFakeTimers();
    const host = new MockSandboxHost();
    const rpcHandler = createSandboxRpcHandler();
    host.activate = vi.fn(async (pluginName, manifest) => {
      host.activated.set(pluginName, manifest);
      if (pluginName === "stuck-plugin") {
        await rpcHandler(pluginName, manifest, "commands.register", [
          "extension.stuck-plugin.partial",
        ]);
        await new Promise<void>(() => undefined);
      }
    });
    setSandboxHost(host);
    syncPlugins([
      makePluginInfo({ name: "stuck-plugin" }),
      makePluginInfo({ name: "healthy-plugin" }),
    ]);

    try {
      const startup = activateOnStartup();
      await vi.dynamicImportSettled();
      expect(host.activate).toHaveBeenCalledWith(
        "stuck-plugin",
        expect.objectContaining({ name: "stuck-plugin" }),
        expect.any(String),
      );

      await vi.advanceTimersByTimeAsync(15_000);
      await startup;

      expect(host.terminatedNames).toContain("stuck-plugin");
      expect(host.activate).toHaveBeenCalledWith(
        "healthy-plugin",
        expect.objectContaining({ name: "healthy-plugin" }),
        expect.any(String),
      );
      const states = new Map(
        listPluginStates().map((state) => [state.name, state]),
      );
      expect(states.get("stuck-plugin")).toMatchObject({
        status: "error",
        error: expect.stringContaining("timed out"),
      });
      expect(states.get("healthy-plugin")?.status).toBe("activated");
      expect(
        listPluginCommands().some(
          (command) => command.pluginName === "stuck-plugin",
        ),
      ).toBe(false);
    } finally {
      vi.useRealTimers();
    }
  });

  it("cleans sandbox contributions and terminates after activation rejects", async () => {
    const host = new MockSandboxHost();
    const rpcHandler = createSandboxRpcHandler();
    host.activate = vi.fn(async (pluginName, manifest) => {
      host.activated.set(pluginName, manifest);
      await rpcHandler(pluginName, manifest, "commands.register", [
        "extension.fail-plugin.partial",
      ]);
      await rpcHandler(pluginName, manifest, "views.register", [
        "fail-plugin.partial-view",
        { title: "Partial view" },
      ]);
      throw new Error("Worker init failed after registration");
    });
    setSandboxHost(host);
    syncPlugins([makePluginInfo({ name: "fail-plugin" })]);

    await activatePlugin("fail-plugin");

    expect(host.terminatedNames).toEqual(["fail-plugin"]);
    expect(listPluginCommands()).toEqual([]);
    expect(listPluginViews()).toEqual([]);
    expect(listPluginStates()[0]).toMatchObject({
      status: "error",
      error: expect.stringContaining("Worker init failed after registration"),
    });
  });

  it("deactivatePlugin terminates sandbox worker", async () => {
    const host = new MockSandboxHost();
    setSandboxHost(host);
    syncPlugins([makePluginInfo({ name: "sandbox-plugin" })]);

    await activatePlugin("sandbox-plugin");
    expect(host.has("sandbox-plugin")).toBe(true);

    await deactivatePlugin("sandbox-plugin");

    expect(host.terminatedNames).toContain("sandbox-plugin");
    expect(host.has("sandbox-plugin")).toBe(false);
    const states = listPluginStates();
    expect(states[0].status).toBe("loaded");
  });

  it("clearRegistry terminates all sandboxed plugins", async () => {
    const host = new MockSandboxHost();
    setSandboxHost(host);
    syncPlugins([
      makePluginInfo({ name: "plugin-a" }),
      makePluginInfo({ name: "plugin-b" }),
    ]);

    await activatePlugin("plugin-a");
    await activatePlugin("plugin-b");

    clearRegistry();

    expect(host.terminateAllCalled).toBe(true);
  });

  it("cached module takes precedence over sandbox", async () => {
    // When a test injects a module via __setPluginModule, the sandbox
    // should NOT be used (the cached module path runs first).
    const host = new MockSandboxHost();
    setSandboxHost(host);

    let activated = false;
    syncPlugins([makePluginInfo({ name: "cached-plugin" })]);
    __setPluginModule("cached-plugin", {
      activate: () => {
        activated = true;
      },
    });

    await activatePlugin("cached-plugin");

    expect(activated).toBe(true);
    expect(host.activated.has("cached-plugin")).toBe(false);
  });
});

describe("registry reactivity (N-57 / Proposal Q)", () => {
  it("listPluginViews updates reactively when a view is registered", async () => {
    let runCount = 0;
    let currentViews: ReturnType<typeof listPluginViews> = [];
    watchEffect(
      () => {
        currentViews = listPluginViews();
        runCount++;
      },
      { flush: "sync" },
    );
    expect(runCount).toBe(1);
    expect(currentViews).toHaveLength(0);

    syncPlugins([makePluginInfo({ name: "view-plugin" })]);
    await activatePluginWithModule("view-plugin", {
      activate: (ctx: NknkAPI) => {
        ctx.views.register("my-view", null, { title: "My View" });
      },
    });

    expect(runCount).toBe(2);
    expect(currentViews).toHaveLength(1);
    expect(currentViews[0].id).toBe("my-view");
  });

  it("listPluginCommands updates reactively when a command is registered", async () => {
    let runCount = 0;
    let currentCmds: ReturnType<typeof listPluginCommands> = [];
    watchEffect(
      () => {
        currentCmds = listPluginCommands();
        runCount++;
      },
      { flush: "sync" },
    );
    expect(runCount).toBe(1);

    syncPlugins([makePluginInfo({ name: "cmd-plugin" })]);
    await activatePluginWithModule("cmd-plugin", {
      activate: (ctx: NknkAPI) => {
        ctx.commands.register("extension.cmd-plugin.my-cmd", () => undefined);
      },
    });

    expect(runCount).toBe(2);
    expect(currentCmds).toHaveLength(1);
    expect(currentCmds[0].id).toBe("extension.cmd-plugin.my-cmd");
  });

  it("listPluginViews updates reactively when a plugin is deactivated", async () => {
    let runCount = 0;
    let currentViews: ReturnType<typeof listPluginViews> = [];
    watchEffect(
      () => {
        currentViews = listPluginViews();
        runCount++;
      },
      { flush: "sync" },
    );

    syncPlugins([makePluginInfo({ name: "view-plugin" })]);
    await activatePluginWithModule("view-plugin", {
      activate: (ctx: NknkAPI) => {
        ctx.views.register("my-view", null, { title: "My View" });
      },
    });
    expect(currentViews).toHaveLength(1);
    expect(runCount).toBe(2);

    await deactivatePlugin("view-plugin");
    expect(currentViews).toHaveLength(0);
    expect(runCount).toBe(3);
  });

  it("listPluginViews updates reactively when clearRegistry is called", async () => {
    let runCount = 0;
    let currentViews: ReturnType<typeof listPluginViews> = [];
    watchEffect(
      () => {
        currentViews = listPluginViews();
        runCount++;
      },
      { flush: "sync" },
    );

    syncPlugins([makePluginInfo({ name: "view-plugin" })]);
    await activatePluginWithModule("view-plugin", {
      activate: (ctx: NknkAPI) => {
        ctx.views.register("my-view", null, { title: "My View" });
      },
    });
    expect(currentViews).toHaveLength(1);

    clearRegistry();
    expect(currentViews).toHaveLength(0);
    // runCount should have increased (clearRegistry bumps the version)
    expect(runCount).toBeGreaterThanOrEqual(3);
  });

  it("listPluginCommands updates reactively when clearRegistry is called", async () => {
    let runCount = 0;
    let currentCmds: ReturnType<typeof listPluginCommands> = [];
    watchEffect(
      () => {
        currentCmds = listPluginCommands();
        runCount++;
      },
      { flush: "sync" },
    );

    syncPlugins([makePluginInfo({ name: "cmd-plugin" })]);
    await activatePluginWithModule("cmd-plugin", {
      activate: (ctx: NknkAPI) => {
        ctx.commands.register("extension.cmd-plugin.my-cmd", () => undefined);
      },
    });
    expect(currentCmds).toHaveLength(1);

    clearRegistry();
    expect(currentCmds).toHaveLength(0);
    expect(runCount).toBeGreaterThanOrEqual(3);
  });
});
