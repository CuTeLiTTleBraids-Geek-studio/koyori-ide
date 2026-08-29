/**
 * Tests for the G-VSC-02 Extension Host.
 *
 * Covers:
 *   - Extension activation / deactivation lifecycle
 *   - Disposable tracking and cleanup on deactivate
 *   - Permission classification (Trusted / Reviewed / Restricted)
 *   - Restricted extensions disabled by default
 *   - Dangerous command confirmation (G-SEC-12)
 *   - workspace.fs bridging to FileService with permission gating
 *   - Monaco language-provider bridging
 *   - Webview panel creation with sandbox="allow-scripts" (G-SEC-05)
 */

// @ts-expect-error -- frontend tsconfig intentionally omits Node types; Vitest runs on Node.
import { webcrypto } from "node:crypto";
import { describe, it, expect, beforeEach, vi } from "vitest";

const callByNameMock = vi.fn<(...args: unknown[]) => Promise<unknown>>();
const callByIdMock = vi.fn<(...args: unknown[]) => Promise<unknown>>();
const appStateMock = vi.hoisted(() => ({
  currentProject: "/test/project",
  language: "en",
  workspaceGeneration: 1,
}));

function callExtensionBinding(...args: unknown[]): Promise<unknown> {
  const handler = (
    globalThis as typeof globalThis & {
      __koyoriIdeExtensionBindingCall?: (...values: unknown[]) => Promise<unknown>;
    }
  ).__koyoriIdeExtensionBindingCall;
  return handler
    ? handler(...args)
    : Promise.reject(new Error("Extension binding test handler unavailable"));
}

vi.mock("@wailsio/runtime", () => ({
  Call: {
    ByName: (...args: unknown[]) => callByNameMock(...args),
    ByID: (...args: unknown[]) => callByIdMock(...args),
  },
  Create: {
    Any: (value: unknown) => value,
    Array: () => (value: unknown) => value,
    Map: () => (value: unknown) => value,
    Nullable: () => (value: unknown) => value,
    Struct: () => (value: unknown) => value,
  },
}));

vi.mock("../../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/taskservice.js", () => ({
  Execute: (...args: unknown[]) => callExtensionBinding(2_308_805_722, ...args),
  ExecuteApproved: (...args: unknown[]) => callExtensionBinding(93_571_653, ...args),
  LoadTasks: (...args: unknown[]) => callExtensionBinding(3_032_347_519, ...args),
  RequestExecutionApproval: (...args: unknown[]) =>
    callExtensionBinding(1_113_621_941, ...args),
  Shutdown: (...args: unknown[]) => callExtensionBinding(2_220_420_061, ...args),
  Stop: (...args: unknown[]) => callExtensionBinding(1_861_972_639, ...args),
}));

// Mock @/stores/app to break the Monaco editor import chain in jsdom
// (mirrors pluginRegistry.test.ts). appState.currentProject is the
// workspace root used to resolve relative URIs from extensions.
vi.mock("@/stores/app", () => ({ appState: appStateMock }));

// Mock @/api/services so workspace.fs bridges to a controllable fake
// instead of the real Wails FileService bindings (which are absent in
// jsdom).
const readFileMock = vi.fn<(path: string) => Promise<string>>();
const writeFileMock = vi.fn<(path: string, content: string) => Promise<void>>();
const listDirectoryMock = vi.fn<(path: string) => Promise<Array<Record<string, unknown>>>>();
const launchWithConfigMock = vi.fn<
  (config: Record<string, unknown>) => Promise<void>
>();
const startSessionMock = vi.fn<
  (id: string, cwd: string, shell: string) => Promise<void>
>();
const writeSessionMock = vi.fn<
  (id: string, input: string) => Promise<void>
>();
const killSessionMock = vi.fn<(id: string) => Promise<void>>();
const windowOpenMock = vi.fn<(...args: unknown[]) => Window | null>();
vi.mock("@/api/services", () => ({
  fileService: {
    readFile: (path: string) => readFileMock(path),
    writeFile: (path: string, content: string) => writeFileMock(path, content),
    listDirectory: (path: string) => listDirectoryMock(path),
  },
  debugService: {
    launchWithConfig: (config: Record<string, unknown>) =>
      launchWithConfigMock(config),
  },
  terminalService: {
    startSession: (id: string, cwd: string, shell: string) =>
      startSessionMock(id, cwd, shell),
    writeSession: (id: string, input: string) =>
      writeSessionMock(id, input),
    killSession: (id: string) => killSessionMock(id),
  },
}));

import {
  ExtensionHost,
  resetExtensionHostModuleState,
  type ExtensionDescriptor,
  type ExtensionModule,
} from "@/lib/extensionHost/extensionHost";
import {
  classifyExtension,
  hasPermission,
  clearPermissionRegistry,
  registerExtensionPermissions,
} from "@/lib/extensionHost/permissions";
import type { VscodeAPI } from "@/lib/extensionHost/vscodeApi";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function makeDescriptor(
  overrides: Partial<ExtensionDescriptor> = {},
): ExtensionDescriptor {
  return {
    id: "test.ext",
    mainPath: "/exts/test/main.js",
    permissions: [],
    ...overrides,
  };
}

function deferred<T>(): {
  promise: Promise<T>;
  resolve(value: T): void;
} {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

beforeEach(() => {
  (
    globalThis as typeof globalThis & {
      __koyoriIdeExtensionBindingCall?: (...values: unknown[]) => Promise<unknown>;
    }
  ).__koyoriIdeExtensionBindingCall = (...args: unknown[]) => callByIdMock(...args);
  vi.stubGlobal("crypto", webcrypto as unknown as Crypto);
  appStateMock.currentProject = "/test/project";
  appStateMock.workspaceGeneration = 1;
  readFileMock.mockReset();
  writeFileMock.mockReset();
  listDirectoryMock.mockReset();
  listDirectoryMock.mockResolvedValue([]);
  launchWithConfigMock.mockReset();
  launchWithConfigMock.mockResolvedValue(undefined);
  startSessionMock.mockReset();
  startSessionMock.mockResolvedValue(undefined);
  writeSessionMock.mockReset();
  writeSessionMock.mockResolvedValue(undefined);
  killSessionMock.mockReset();
  killSessionMock.mockResolvedValue(undefined);
  callByIdMock.mockReset();
  callByIdMock.mockImplementation((methodId: unknown, ...args: unknown[]) => {
    switch (methodId) {
      case 1_383_910_210:
        return startSessionMock(args[0] as string, args[1] as string, args[2] as string);
      case 2_400_545_499:
        return writeSessionMock(args[0] as string, args[1] as string);
      case 4_001_821_542:
        return killSessionMock(args[0] as string);
      case 1_113_621_941:
        return callByNameMock(
          "services.TaskService.RequestExecutionApproval",
          ...args,
        ).then(() => "task-approval");
      case 93_571_653:
        return callByNameMock("services.TaskService.Execute", ...args.slice(0, 3));
      case 1_861_972_639:
        return callByNameMock("services.TaskService.Stop", ...args);
      default:
        return Promise.resolve(undefined);
    }
  });
  windowOpenMock.mockReset();
  windowOpenMock.mockReturnValue(window);
  vi.stubGlobal("open", windowOpenMock);
  callByNameMock.mockReset();
  callByNameMock.mockRejectedValue(new Error("Wails binding unavailable"));
  localStorage.clear();
  sessionStorage.clear();
  clearPermissionRegistry();
});

// ---------------------------------------------------------------------------
// Permission classification
// ---------------------------------------------------------------------------

describe("classifyExtension", () => {
  it("classifies an extension with no permissions as Trusted", () => {
    expect(classifyExtension([])).toBe("Trusted");
  });

  it("classifies an extension with only fs.read as Trusted", () => {
    expect(classifyExtension(["fs.read"])).toBe("Trusted");
  });

  it("classifies safe permissions (clipboard, ui.notifications, ui.webview) as Trusted", () => {
    expect(
      classifyExtension(["clipboard", "ui.notifications", "ui.webview"]),
    ).toBe("Trusted");
  });

  it("classifies fs.write as Reviewed", () => {
    expect(classifyExtension(["fs.write"])).toBe("Reviewed");
  });

  it("classifies shell.execute as Reviewed", () => {
    expect(classifyExtension(["shell.execute"])).toBe("Reviewed");
  });

  it("classifies network as Restricted", () => {
    expect(classifyExtension(["network"])).toBe("Restricted");
  });

  it("classifies a mix of safe + Reviewed as Reviewed", () => {
    expect(classifyExtension(["fs.read", "fs.write"])).toBe("Reviewed");
  });

  it("classifies a mix containing network as Restricted (highest risk wins)", () => {
    expect(classifyExtension(["fs.read", "fs.write", "network"])).toBe(
      "Restricted",
    );
  });

  it("classifies shell.execute + network as Restricted", () => {
    expect(classifyExtension(["shell.execute", "network"])).toBe("Restricted");
  });
});

describe("hasPermission", () => {
  it("returns true when the extension declared the permission", () => {
    registerExtensionPermissions("alpha", ["fs.read", "fs.write"]);
    expect(hasPermission("alpha", "fs.read")).toBe(true);
    expect(hasPermission("alpha", "fs.write")).toBe(true);
  });

  it("returns false when the extension did not declare the permission", () => {
    registerExtensionPermissions("alpha", ["fs.read"]);
    expect(hasPermission("alpha", "fs.write")).toBe(false);
  });

  it("returns false for an unknown extension", () => {
    expect(hasPermission("unknown", "fs.read")).toBe(false);
  });

  it("returns false after the extension permissions are unregistered", () => {
    registerExtensionPermissions("alpha", ["fs.read"]);
    expect(hasPermission("alpha", "fs.read")).toBe(true);
    clearPermissionRegistry();
    expect(hasPermission("alpha", "fs.read")).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// Extension activation / deactivation
// ---------------------------------------------------------------------------

describe("ExtensionHost activation", () => {
  it("rejects unknown runtime permission strings", async () => {
    const host = new ExtensionHost();
    await expect(
      host.activateWithModule(
        makeDescriptor({
          permissions: ["shell.exec" as unknown as "shell.execute"],
        }),
        { activate: vi.fn() },
      ),
    ).rejects.toThrow(/unknown extension permission/i);
  });

  it("activates an executable extension when permissions are omitted", async () => {
    const activate = vi.fn();
    const loadModule = vi.fn<() => Promise<ExtensionModule>>().mockResolvedValue({
      activate,
    });
    const host = new ExtensionHost({ loadModule });
    const descriptor = {
      id: "undeclared.ext",
      mainPath: "/exts/undeclared/main.js",
    } as ExtensionDescriptor;

    await expect(host.activate(descriptor)).resolves.toBeUndefined();
    expect(loadModule).toHaveBeenCalledWith(
      "undeclared.ext",
      "/exts/undeclared/main.js",
    );
    expect(activate).toHaveBeenCalledTimes(1);
    expect(host.getSecurityLevel("undeclared.ext")).toBe("Trusted");
  });

  it("activates an extension and invokes its activate() with the vscode API", async () => {
    const host = new ExtensionHost();
    const activate = vi.fn<(api: VscodeAPI) => Promise<void>>();
    const module: ExtensionModule = { activate };
    const desc = makeDescriptor({ id: "alpha" });

    await host.activateWithModule(desc, module);

    expect(activate).toHaveBeenCalledTimes(1);
    const api = activate.mock.calls[0][0];
    expect(api.languages).toBeDefined();
    expect(api.commands).toBeDefined();
    expect(api.workspace).toBeDefined();
    expect(api.window).toBeDefined();
    expect(host.isActive("alpha")).toBe(true);
  });

  it("preserves Restricted approval when disposeAll resets transient host state", async () => {
    const host = new ExtensionHost();
    const descriptor = makeDescriptor({
      id: "approved.ext",
      permissions: ["network"],
    });
    const module: ExtensionModule = { activate: vi.fn(), deactivate: vi.fn() };
    host.approveExtension(descriptor.id);

    await host.activateWithModule(descriptor, module);
    await host.disposeAll();
    await expect(host.activateWithModule(descriptor, module)).resolves.toBeUndefined();
  });

  it("resets every tracked host without revoking Restricted approval", async () => {
    const host = new ExtensionHost();
    const descriptor = makeDescriptor({
      id: "module-reset.ext",
      permissions: ["network"],
    });
    const module: ExtensionModule = { activate: vi.fn(), deactivate: vi.fn() };
    host.approveExtension(descriptor.id);
    await host.activateWithModule(descriptor, module);

    await resetExtensionHostModuleState();

    expect(host.isActive(descriptor.id)).toBe(false);
    await expect(host.activateWithModule(descriptor, module)).resolves.toBeUndefined();
  });

  it("records the security level on activation", async () => {
    const host = new ExtensionHost();
    await host.activateWithModule(
      makeDescriptor({ id: "alpha", permissions: ["fs.write"] }),
      { activate: () => {} },
    );
    expect(host.getSecurityLevel("alpha")).toBe("Reviewed");
  });

  it("throws when activating an unknown extension via activate()", async () => {
    const host = new ExtensionHost({
      loadModule: vi.fn(),
    });
    await expect(host.activate(makeDescriptor({ id: "ghost" }))).rejects.toThrow(
      /loadModule|failed|not found|main/i,
    );
  });

  it("is a no-op when activating an already-active extension", async () => {
    const host = new ExtensionHost();
    const activate = vi.fn(() => Promise.resolve());
    await host.activateWithModule(makeDescriptor({ id: "alpha" }), { activate });
    await host.activateWithModule(makeDescriptor({ id: "alpha" }), { activate });
    expect(activate).toHaveBeenCalledTimes(1);
  });

  it("captures activation errors and does not mark the extension active", async () => {
    const host = new ExtensionHost();
    await expect(
      host.activateWithModule(makeDescriptor({ id: "alpha" }), {
        activate: () => {
          throw new Error("boom");
        },
      }),
    ).rejects.toThrow(/boom/);
    expect(host.isActive("alpha")).toBe(false);
  });
});

describe("ExtensionHost deactivation", () => {
  it("calls deactivate() and disposes all tracked disposables", async () => {
    const host = new ExtensionHost({
      monaco: {
        languages: {
          registerCompletionItemProvider: () => ({ dispose: () => undefined }),
          registerHoverProvider: () => ({ dispose: () => undefined }),
        },
      },
    });
    const disposed: string[] = [];
    const deactivate = vi.fn(() => Promise.resolve());

    await host.activateWithModule(makeDescriptor({ id: "alpha" }), {
      activate: (api: VscodeAPI) => {
        api.commands.registerCommand("alpha.cmd", () => undefined);
        // Register a custom disposable to verify cleanup ordering.
        api.languages.registerCompletionItemProvider(
          { language: "go" },
          { provideCompletionItems: () => ({ items: [] }) },
        );
        // Track a manual disposable.
        const tracked = host.trackDisposable("alpha", {
          dispose: () => {
            disposed.push("manual");
          },
        });
        void tracked;
      },
      deactivate,
    });

    await host.deactivate("alpha");

    expect(deactivate).toHaveBeenCalledTimes(1);
    expect(host.isActive("alpha")).toBe(false);
    expect(disposed).toEqual(["manual"]);
  });

  it("is a no-op for an extension that is not active", async () => {
    const host = new ExtensionHost();
    await host.deactivate("alpha");
    expect(host.isActive("alpha")).toBe(false);
  });

  it("unregisters extension permissions on deactivate", async () => {
    const host = new ExtensionHost();
    await host.activateWithModule(
      makeDescriptor({ id: "alpha", permissions: ["fs.read"] }),
      { activate: () => {} },
    );
    expect(hasPermission("alpha", "fs.read")).toBe(true);
    await host.deactivate("alpha");
    expect(hasPermission("alpha", "fs.read")).toBe(false);
  });

  it("propagates deactivate failures after completing host cleanup", async () => {
    const host = new ExtensionHost();
    const dispose = vi.fn();
    await host.activateWithModule(
      makeDescriptor({ id: "alpha", permissions: ["fs.read"] }),
      {
        activate: () => {
          host.trackDisposable("alpha", { dispose });
        },
        deactivate: () => Promise.reject(new Error("shutdown failed")),
      },
    );

    await expect(host.deactivate("alpha")).rejects.toThrow("shutdown failed");

    expect(dispose).toHaveBeenCalledOnce();
    expect(host.isActive("alpha")).toBe(false);
    expect(hasPermission("alpha", "fs.read")).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// Restricted extensions disabled by default
// ---------------------------------------------------------------------------

describe("Restricted extensions are disabled by default", () => {
  it("refuses to activate a Restricted extension without explicit approval", async () => {
    const host = new ExtensionHost();
    const activate = vi.fn(() => Promise.resolve());
    await expect(
      host.activateWithModule(
        makeDescriptor({ id: "net.ext", permissions: ["network"] }),
        { activate },
      ),
    ).rejects.toThrow(/Restricted|disabled|approval/i);
    expect(activate).not.toHaveBeenCalled();
    expect(host.isActive("net.ext")).toBe(false);
  });

  it("activates a Restricted extension when explicitly approved", async () => {
    const host = new ExtensionHost();
    const activate = vi.fn(() => Promise.resolve());
    host.approveExtension("net.ext");
    await host.activateWithModule(
      makeDescriptor({ id: "net.ext", permissions: ["network"] }),
      { activate },
    );
    expect(activate).toHaveBeenCalledTimes(1);
    expect(host.isActive("net.ext")).toBe(true);
  });

  it("activates a Reviewed extension without prior approval (Reviewed needs runtime approval, not activation block)", async () => {
    // Reviewed extensions are allowed to activate; the runtime approval
    // gate applies to the privileged operations themselves (e.g. fs.write
    // requires the fs.write permission, which is already declared).
    const host = new ExtensionHost();
    const activate = vi.fn(() => Promise.resolve());
    await host.activateWithModule(
      makeDescriptor({ id: "write.ext", permissions: ["fs.write"] }),
      { activate },
    );
    expect(activate).toHaveBeenCalledTimes(1);
    expect(host.getSecurityLevel("write.ext")).toBe("Reviewed");
  });

  it("getSecurityLevel returns the classified level for an active extension", async () => {
    const host = new ExtensionHost();
    await host.activateWithModule(
      makeDescriptor({ id: "safe.ext", permissions: ["fs.read"] }),
      { activate: () => {} },
    );
    expect(host.getSecurityLevel("safe.ext")).toBe("Trusted");
  });

  it("getSecurityLevel returns undefined for an inactive extension", () => {
    const host = new ExtensionHost();
    expect(host.getSecurityLevel("never.activated")).toBeUndefined();
  });
});

// ---------------------------------------------------------------------------
// Dangerous command confirmation (G-SEC-12)
// ---------------------------------------------------------------------------

describe("dangerous commands require confirmation (G-SEC-12)", () => {
  it("rejects workbench.action.terminal.sendSequence without a confirm handler", async () => {
    const host = new ExtensionHost();
    await host.activateWithModule(makeDescriptor({ id: "alpha" }), {
      activate: (api: VscodeAPI) => {
        api.commands.registerCommand("alpha.safe", () => "safe-result");
      },
    });
    await expect(
      host.executeCommand("workbench.action.terminal.sendSequence", { text: "rm -rf /" }),
    ).rejects.toThrow(/confirm|denied|dangerous/i);
  });

  it("calls the confirm handler for workbench.action.terminal.sendSequence", async () => {
    const confirmHandler = vi.fn<(cmd: string, args: unknown[]) => Promise<boolean>>(
      async () => true,
    );
    const host = new ExtensionHost({ confirmHandler });
    await host.activateWithModule(makeDescriptor({ id: "alpha" }), {
      activate: (api: VscodeAPI) => {
        api.commands.registerCommand(
          "workbench.action.terminal.sendSequence",
          () => "ran",
        );
      },
    });
    await host.executeCommand("workbench.action.terminal.sendSequence", "ls");
    expect(confirmHandler).toHaveBeenCalledTimes(1);
    expect(confirmHandler.mock.calls[0][0]).toBe(
      "workbench.action.terminal.sendSequence",
    );
  });

  it("executes the dangerous command when the confirm handler approves", async () => {
    const host = new ExtensionHost({
      confirmHandler: async () => true,
    });
    let executed = false;
    await host.activateWithModule(makeDescriptor({ id: "alpha" }), {
      activate: (api: VscodeAPI) => {
        // The extension registers a command with a dangerous-looking id.
        api.commands.registerCommand(
          "workbench.action.terminal.sendSequence",
          () => {
            executed = true;
            return "sent";
          },
        );
      },
    });
    const result = await host.executeCommand(
      "workbench.action.terminal.sendSequence",
      "ls",
    );
    expect(executed).toBe(true);
    expect(result).toBe("sent");
  });

  it("rejects the dangerous command when the confirm handler denies", async () => {
    const host = new ExtensionHost({
      confirmHandler: async () => false,
    });
    let executed = false;
    await host.activateWithModule(makeDescriptor({ id: "alpha" }), {
      activate: (api: VscodeAPI) => {
        api.commands.registerCommand(
          "workbench.action.terminal.sendSequence",
          () => {
            executed = true;
          },
        );
      },
    });
    await expect(
      host.executeCommand("workbench.action.terminal.sendSequence", "ls"),
    ).rejects.toThrow(/denied|rejected|confirm/i);
    expect(executed).toBe(false);
  });

  it("requires confirmation for _workbench.* commands", async () => {
    const confirmHandler = vi.fn<(cmd: string, args: unknown[]) => Promise<boolean>>(
      async () => true,
    );
    const host = new ExtensionHost({ confirmHandler });
    await host.activateWithModule(makeDescriptor({ id: "alpha" }), {
      activate: (api: VscodeAPI) => {
        api.commands.registerCommand("_workbench.internal", () => "secret");
      },
    });
    await host.executeCommand("_workbench.internal");
    expect(confirmHandler).toHaveBeenCalledTimes(1);
    expect(confirmHandler.mock.calls[0][0]).toBe("_workbench.internal");
  });

  it("does not require confirmation for ordinary commands", async () => {
    const confirmHandler = vi.fn<(cmd: string, args: unknown[]) => Promise<boolean>>();
    const host = new ExtensionHost({ confirmHandler });
    await host.activateWithModule(makeDescriptor({ id: "alpha" }), {
      activate: (api: VscodeAPI) => {
        api.commands.registerCommand("alpha.safe", () => "safe");
      },
    });
    const result = await host.executeCommand("alpha.safe");
    expect(result).toBe("safe");
    expect(confirmHandler).not.toHaveBeenCalled();
  });

  it("vscode.commands.executeCommand rejects reserved commands before confirmation", async () => {
    const confirmHandler = vi.fn<(cmd: string, args: unknown[]) => Promise<boolean>>(
      async () => false,
    );
    const host = new ExtensionHost({ confirmHandler });
    let executed = false;
    await host.activateWithModule(makeDescriptor({ id: "alpha" }), {
      activate: async (api: VscodeAPI) => {
        api.commands.registerCommand(
          "workbench.action.terminal.sendSequence",
          () => {
            executed = true;
          },
        );
        // Extension-initiated execution must also go through the gate.
        await expect(
          api.commands.executeCommand(
            "workbench.action.terminal.sendSequence",
            "rm",
          ),
        ).rejects.toThrow(/not allowed/i);
      },
    });
    expect(executed).toBe(false);
    expect(confirmHandler).not.toHaveBeenCalled();
  });
});

// ---------------------------------------------------------------------------
// workspace.fs bridging (G-SEC-12 permission gating)
// ---------------------------------------------------------------------------

describe("workspace.fs bridges to FileService", () => {
  it("readFile delegates to fileService.readFile and returns a Uint8Array", async () => {
    readFileMock.mockResolvedValue("hello world");
    const host = new ExtensionHost();
    let result: Uint8Array | undefined;
    await host.activateWithModule(
      makeDescriptor({ id: "reader", permissions: ["fs.read"] }),
      {
        activate: async (api: VscodeAPI) => {
          result = await api.workspace.fs.readFile({
            fsPath: "src/foo.txt",
            scheme: "file",
          });
        },
      },
    );
    expect(readFileMock).toHaveBeenCalledTimes(1);
    // The bridge resolves relative paths against the workspace root.
    expect(readFileMock.mock.calls[0][0]).toBe("/test/project/src/foo.txt");
    expect(result).toBeInstanceOf(Uint8Array);
    expect(new TextDecoder().decode(result!)).toBe("hello world");
  });

  it("readFile throws without fs.read permission", async () => {
    const host = new ExtensionHost();
    await host.activateWithModule(makeDescriptor({ id: "reader" }), {
      activate: async (api: VscodeAPI) => {
        await expect(
          api.workspace.fs.readFile({ fsPath: "foo.txt", scheme: "file" }),
        ).rejects.toThrow(/fs\.read|permission/i);
      },
    });
    expect(readFileMock).not.toHaveBeenCalled();
  });

  it("writeFile delegates to fileService.writeFile with fs.write permission", async () => {
    writeFileMock.mockResolvedValue(undefined);
    const host = new ExtensionHost();
    await host.activateWithModule(
      makeDescriptor({ id: "writer", permissions: ["fs.write"] }),
      {
        activate: async (api: VscodeAPI) => {
          await api.workspace.fs.writeFile(
            { fsPath: "out.txt", scheme: "file" },
            new TextEncoder().encode("content"),
          );
        },
      },
    );
    expect(writeFileMock).toHaveBeenCalledTimes(1);
    expect(writeFileMock.mock.calls[0][0]).toBe("/test/project/out.txt");
    expect(writeFileMock.mock.calls[0][1]).toBe("content");
  });

  it("writeFile throws without fs.write permission", async () => {
    const host = new ExtensionHost();
    await host.activateWithModule(
      makeDescriptor({ id: "writer", permissions: ["fs.read"] }),
      {
        activate: async (api: VscodeAPI) => {
          await expect(
            api.workspace.fs.writeFile(
              { fsPath: "out.txt", scheme: "file" },
              new TextEncoder().encode("x"),
            ),
          ).rejects.toThrow(/fs\.write|permission/i);
        },
      },
    );
    expect(writeFileMock).not.toHaveBeenCalled();
  });
});

describe("additional privileged API permission gates", () => {
  it("requires fs.write for workspace.saveAll", async () => {
    const host = new ExtensionHost();
    let api: VscodeAPI | undefined;
    await host.activateWithModule(makeDescriptor({ id: "reader.ext" }), {
      activate: (value: VscodeAPI) => { api = value; },
    });

    await expect(api!.workspace.saveAll()).rejects.toThrow(/fs\.write/i);
  });

  it("requires ui.notifications for extension-originated messages", async () => {
    const host = new ExtensionHost();
    let deniedApi: VscodeAPI | undefined;
    let allowedApi: VscodeAPI | undefined;
    await host.activateWithModule(makeDescriptor({ id: "silent.ext" }), {
      activate: (value: VscodeAPI) => { deniedApi = value; },
    });
    await host.activateWithModule(
      makeDescriptor({ id: "notifier.ext", permissions: ["ui.notifications"] }),
      { activate: (value: VscodeAPI) => { allowedApi = value; } },
    );

    await expect(deniedApi!.window.showInformationMessage("hidden")).rejects.toThrow(
      /ui\.notifications/i,
    );
    await expect(
      allowedApi!.window.showInformationMessage("visible", "ok"),
    ).resolves.toBeUndefined();
  });
});

// ---------------------------------------------------------------------------
// M-27: resolveWorkspacePath 路径遍历校验
// ---------------------------------------------------------------------------

describe("M-27: resolveWorkspacePath 路径遍历校验", () => {
  it("(a) 拒绝包含 '..' 段的路径", async () => {
    const host = new ExtensionHost();
    await host.activateWithModule(
      makeDescriptor({ id: "reader", permissions: ["fs.read"] }),
      {
        activate: async (api: VscodeAPI) => {
          await expect(
            api.workspace.fs.readFile({ fsPath: "../etc/passwd", scheme: "file" }),
          ).rejects.toThrow(/path traversal|\.\./i);
        },
      },
    );
    expect(readFileMock).not.toHaveBeenCalled();
  });

  it("(b) 拒绝 workspace root 之外的绝对路径", async () => {
    const host = new ExtensionHost();
    await host.activateWithModule(
      makeDescriptor({ id: "reader", permissions: ["fs.read"] }),
      {
        activate: async (api: VscodeAPI) => {
          await expect(
            api.workspace.fs.readFile({ fsPath: "/etc/passwd", scheme: "file" }),
          ).rejects.toThrow(/path traversal|outside workspace/i);
        },
      },
    );
    expect(readFileMock).not.toHaveBeenCalled();
  });

  it("(c) 合法相对路径正确解析到 workspace root 下", async () => {
    readFileMock.mockResolvedValue("content");
    const host = new ExtensionHost();
    await host.activateWithModule(
      makeDescriptor({ id: "reader", permissions: ["fs.read"] }),
      {
        activate: async (api: VscodeAPI) => {
          const result = await api.workspace.fs.readFile({
            fsPath: "src/foo.txt",
            scheme: "file",
          });
          // 解析为 workspace root 下的绝对路径
          expect(readFileMock.mock.calls[0][0]).toBe("/test/project/src/foo.txt");
          expect(new TextDecoder().decode(result)).toBe("content");
        },
      },
    );
  });

  it.each([
    ["公共前缀", "/test/project-evil/secret.txt"],
    ["编码的绝对路径", "%2ftest%2fproject-evil%2fsecret.txt"],
    ["编码的混合分隔符和点段", "%2e%2e%5csecret.txt"],
  ])("拒绝%s绕过", async (_case, fsPath) => {
    const host = new ExtensionHost();

    await host.activateWithModule(
      makeDescriptor({ id: "reader", permissions: ["fs.read"] }),
      {
        activate: async (api: VscodeAPI) => {
          await expect(
            api.workspace.fs.readFile({ fsPath, scheme: "file" }),
          ).rejects.toThrow(/path traversal|outside workspace/i);
        },
      },
    );

    expect(readFileMock).not.toHaveBeenCalled();
  });

  it("规范化 workspace 内的混合分隔符和点段", async () => {
    readFileMock.mockResolvedValue("content");
    const host = new ExtensionHost();

    await host.activateWithModule(
      makeDescriptor({ id: "reader", permissions: ["fs.read"] }),
      {
        activate: async (api: VscodeAPI) => {
          await api.workspace.fs.readFile({
            fsPath: "src\\.\\nested\\..\\file.txt",
            scheme: "file",
          });
        },
      },
    );

    expect(readFileMock).toHaveBeenCalledWith("/test/project/src/file.txt");
  });
});

// ---------------------------------------------------------------------------
// Monaco language-provider bridging
// ---------------------------------------------------------------------------

describe("Monaco language-provider bridging", () => {
  it("registerCompletionItemProvider delegates to monaco.languages.registerCompletionItemProvider", async () => {
    const registerCompletion = vi.fn(() => ({ dispose: vi.fn() }));
    const monaco = {
      languages: {
        registerCompletionItemProvider: registerCompletion,
        registerHoverProvider: vi.fn(() => ({ dispose: vi.fn() })),
      },
    };
    const host = new ExtensionHost({ monaco });
    const provider = {
      provideCompletionItems: vi.fn(() => ({ items: [] as { label: string }[] })),
    };
    await host.activateWithModule(makeDescriptor({ id: "alpha" }), {
      activate: (api: VscodeAPI) => {
        api.languages.registerCompletionItemProvider(
          { language: "go" },
          provider,
        );
      },
    });
    expect(registerCompletion).toHaveBeenCalledTimes(1);
    // Monaco selector is derived from the vscode DocumentSelector.
    const call0 = registerCompletion.mock.calls[0] as unknown as [string, unknown];
    expect(call0[0]).toBe("go");
  });

  it("registerHoverProvider delegates to monaco.languages.registerHoverProvider", async () => {
    const registerHover = vi.fn(() => ({ dispose: vi.fn() }));
    const monaco = {
      languages: {
        registerCompletionItemProvider: vi.fn(() => ({ dispose: vi.fn() })),
        registerHoverProvider: registerHover,
      },
    };
    const host = new ExtensionHost({ monaco });
    await host.activateWithModule(makeDescriptor({ id: "alpha" }), {
      activate: (api: VscodeAPI) => {
        api.languages.registerHoverProvider(
          { language: "typescript" },
          { provideHover: () => null },
        );
      },
    });
    expect(registerHover).toHaveBeenCalledTimes(1);
    const call0 = registerHover.mock.calls[0] as unknown as [string, unknown];
    expect(call0[0]).toBe("typescript");
  });

  it("fails closed when Monaco lacks the requested provider method", async () => {
    const host = new ExtensionHost({
      monaco: {
        languages: {
          registerCompletionItemProvider: vi.fn(() => ({ dispose: vi.fn() })),
          registerHoverProvider: vi.fn(() => ({ dispose: vi.fn() })),
        },
      },
    });

    await expect(host.activateWithModule(makeDescriptor({ id: "missing-provider" }), {
      activate: (api: VscodeAPI) => {
        api.languages.registerReferenceProvider(
          { language: "typescript" },
          { provideReferences: () => [] },
        );
      },
    })).rejects.toThrow(/KOYORI_IDE_EXT_API_UNSUPPORTED.*languages\.registerProvider/);
    expect(host.isActive("missing-provider")).toBe(false);
  });

  it("rolls back selector-array registrations after a later filter fails", async () => {
    const firstDisposable = { dispose: vi.fn() };
    const registerDefinitionProvider = vi.fn()
      .mockReturnValueOnce(firstDisposable)
      .mockImplementationOnce(() => { throw new Error("second selector failed"); });
    const host = new ExtensionHost({
      monaco: {
        languages: {
          registerCompletionItemProvider: vi.fn(() => ({ dispose: vi.fn() })),
          registerHoverProvider: vi.fn(() => ({ dispose: vi.fn() })),
          registerDefinitionProvider,
        },
      },
    });

    await expect(host.activateWithModule(makeDescriptor({ id: "selector-rollback" }), {
      activate: (api: VscodeAPI) => {
        api.languages.registerDefinitionProvider(
          [{ language: "go" }, { language: "typescript" }],
          { provideDefinition: () => null },
        );
      },
    })).rejects.toThrow("second selector failed");
    expect(registerDefinitionProvider).toHaveBeenCalledTimes(2);
    expect(firstDisposable.dispose).toHaveBeenCalled();
    expect(host.isActive("selector-rollback")).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// Webview panel creation (G-SEC-05 sandbox)
// ---------------------------------------------------------------------------

describe("window.createWebviewPanel", () => {
  it("rejects panel creation without ui.webview before mutating the DOM", async () => {
    const host = new ExtensionHost();
    const iframeCount = document.body.querySelectorAll("iframe").length;

    await expect(
      host.activateWithModule(makeDescriptor({ id: "alpha" }), {
        activate: (api: VscodeAPI) => {
          api.window.createWebviewPanel("alpha.preview", "Preview", {}, {});
        },
      }),
    ).rejects.toThrow(/ui\.webview|permission|not declared/i);

    expect(document.body.querySelectorAll("iframe")).toHaveLength(iframeCount);
  });

  it("rejects webview view provider registration without ui.webview", async () => {
    const host = new ExtensionHost();

    await expect(
      host.activateWithModule(makeDescriptor({ id: "alpha" }), {
        activate: (api: VscodeAPI) => {
          api.window.registerWebviewViewProvider("alpha.view", {
            resolveWebviewView: () => undefined,
          });
        },
      }),
    ).rejects.toThrow(/ui\.webview|permission|not declared/i);

    const registries = host as unknown as {
      webviewViewProviders: Map<string, unknown>;
    };
    expect(registries.webviewViewProviders.size).toBe(0);
  });

  it("rejects executable webviews without the network permission", async () => {
    const host = new ExtensionHost();
    const iframeCount = document.body.querySelectorAll("iframe").length;

    await expect(
      host.activateWithModule(
        makeDescriptor({ id: "alpha", permissions: ["ui.webview"] }),
        {
          activate: (api: VscodeAPI) => {
            api.window.createWebviewPanel(
              "alpha.preview",
              "Preview",
              {},
              { enableScripts: true },
            );
          },
        },
      ),
    ).rejects.toThrow(/network|permission/i);

    expect(document.body.querySelectorAll("iframe")).toHaveLength(iframeCount);
  });

  it("creates a panel and tracks it for disposal", async () => {
    const host = new ExtensionHost();
    let panel: ReturnType<VscodeAPI["window"]["createWebviewPanel"]> | undefined;
    await host.activateWithModule(
      makeDescriptor({ id: "alpha", permissions: ["ui.webview"] }),
      {
        activate: (api: VscodeAPI) => {
          panel = api.window.createWebviewPanel(
            "alpha.preview",
            "Preview",
            {},
            {},
          );
        },
      },
    );
    expect(panel).toBeDefined();
    expect(panel!.webview).toBeDefined();
    // Setting HTML should not throw.
    panel!.webview.html = "<p>hello</p>";
    expect(panel!.webview.html).toBe("<p>hello</p>");
    // Static webviews do not receive script execution capability.
    const iframe = panel!.webview._iframe as HTMLIFrameElement | undefined;
    expect(iframe).toBeDefined();
    expect(iframe!.getAttribute("sandbox")).toBe("");
  });

  it("disposes the panel (removes the iframe) on deactivate", async () => {
    const host = new ExtensionHost();
    let panel: ReturnType<VscodeAPI["window"]["createWebviewPanel"]> | undefined;
    await host.activateWithModule(
      makeDescriptor({ id: "alpha", permissions: ["ui.webview"] }),
      {
        activate: (api: VscodeAPI) => {
          panel = api.window.createWebviewPanel(
            "alpha.preview",
            "Preview",
            {},
            {},
          );
        },
      },
    );
    const iframe = panel!.webview._iframe as HTMLIFrameElement;
    expect(document.body.contains(iframe)).toBe(true);
    await host.deactivate("alpha");
    expect(document.body.contains(iframe)).toBe(false);
  });

  it("sanitizes srcdoc and injects a restrictive CSP", async () => {
    const host = new ExtensionHost();
    let panel: ReturnType<VscodeAPI["window"]["createWebviewPanel"]> | undefined;
    await host.activateWithModule(
      makeDescriptor({ id: "alpha", permissions: ["ui.webview"] }),
      {
        activate: (api: VscodeAPI) => {
          panel = api.window.createWebviewPanel(
            "alpha.preview",
            "Preview",
            {},
            {},
          );
        },
      },
    );

    panel!.webview.html =
      '<script>parent.postMessage("stolen", "*")</script>' +
      '<script src="https://example.test/external.js"></script>' +
      '<meta http-equiv="refresh" content="0;url=https://example.test/leak">' +
      "<style>p { color: red; }</style>" +
      '<img src="x" onerror="parent.postMessage(\'xss\', \'*\')">' +
      '<a href="https://example.test/leak">leave</a>' +
      "<p>safe content</p>";

    const iframe = panel!.webview._iframe as HTMLIFrameElement;
    const srcdoc = iframe.srcdoc;
    const parsed = new DOMParser().parseFromString(srcdoc, "text/html");
    const csp = parsed.querySelector(
      'meta[http-equiv="Content-Security-Policy"]',
    );
    const inlineScript = parsed.querySelector("script:not([src])");
    const style = parsed.querySelector("style");
    const nonce = style?.getAttribute("nonce");
    expect(inlineScript).toBeNull();
    expect(parsed.querySelector("script[src]")).toBeNull();
    expect(parsed.querySelector('meta[http-equiv="refresh" i]')).toBeNull();
    expect(parsed.querySelector("a")?.hasAttribute("href")).toBe(false);
    expect(parsed.querySelector("img")?.hasAttribute("src")).toBe(false);
    expect(nonce).toMatch(/^[a-f0-9]{32}$/);
    expect(style?.getAttribute("nonce")).toBe(nonce);
    expect(parsed.querySelector("[onerror]")).toBeNull();
    expect(parsed.body.textContent).toContain("safe content");
    expect(csp?.getAttribute("content")).toContain("default-src 'none'");
    expect(csp?.getAttribute("content")).toContain("object-src 'none'");
    expect(csp?.getAttribute("content")).toContain("base-uri 'none'");
    expect(csp?.getAttribute("content")).toContain("form-action 'none'");
    expect(csp?.getAttribute("content")).toContain("script-src 'none'");
    expect(csp?.getAttribute("content")).toContain(`style-src 'nonce-${nonce}'`);
    expect(csp?.getAttribute("content")).toContain("script-src-attr 'none'");
    expect(csp?.getAttribute("content")).toContain("style-src-attr 'none'");
    expect(csp?.getAttribute("content")).toContain("img-src data:");
    expect(csp?.getAttribute("content")).toContain("media-src data:");
    expect(csp?.getAttribute("content")).not.toMatch(/img-src[^;]*https:/);
    expect(csp?.getAttribute("content")).not.toMatch(/media-src[^;]*https:/);
    expect(csp?.getAttribute("content")).toContain("connect-src 'none'");
    expect(csp?.getAttribute("content")).toContain("navigate-to 'none'");
    expect(iframe.getAttribute("sandbox")).toBe("");
  });

  it("generates a fresh nonce for every webview HTML document", async () => {
    const host = new ExtensionHost();
    let panel: ReturnType<VscodeAPI["window"]["createWebviewPanel"]> | undefined;
    await host.activateWithModule(
      makeDescriptor({ id: "alpha", permissions: ["ui.webview"] }),
      {
        activate: (api: VscodeAPI) => {
          panel = api.window.createWebviewPanel("alpha.preview", "Preview", {}, {});
        },
      },
    );

    panel!.webview.html = "<style>body { color: red; }</style>";
    const first = new DOMParser().parseFromString(
      (panel!.webview._iframe as HTMLIFrameElement).srcdoc,
      "text/html",
    ).querySelector("style")?.getAttribute("nonce");
    panel!.webview.html = "<style>body { color: blue; }</style>";
    const second = new DOMParser().parseFromString(
      (panel!.webview._iframe as HTMLIFrameElement).srcdoc,
      "text/html",
    ).querySelector("style")?.getAttribute("nonce");

    expect(first).toMatch(/^[a-f0-9]{32}$/);
    expect(second).toMatch(/^[a-f0-9]{32}$/);
    expect(second).not.toBe(first);
  });

  it("allows https webview resources only for an approved network extension", async () => {
    const host = new ExtensionHost();
    host.approveExtension("alpha");
    let panel: ReturnType<VscodeAPI["window"]["createWebviewPanel"]> | undefined;
    await host.activateWithModule(
      makeDescriptor({ id: "alpha", permissions: ["ui.webview", "network"] }),
      {
        activate: (api: VscodeAPI) => {
          panel = api.window.createWebviewPanel(
            "alpha.preview",
            "Preview",
            {},
            { enableScripts: true },
          );
        },
      },
    );

    panel!.webview.html =
      '<script>globalThis.webviewReady = true</script>' +
      '<img src="https://example.test/image.png">';
    const parsed = new DOMParser().parseFromString(
      (panel!.webview._iframe as HTMLIFrameElement).srcdoc,
      "text/html",
    );
    const csp = parsed
      .querySelector('meta[http-equiv="Content-Security-Policy"]')
      ?.getAttribute("content");
    const scriptNonce = parsed.querySelector("script")?.getAttribute("nonce");
    expect(scriptNonce).toMatch(/^[a-f0-9]{32}$/);
    expect(csp).toContain(`script-src 'nonce-${scriptNonce}'`);
    expect(csp).toMatch(/img-src[^;]*https:/);
    expect(csp).toMatch(/font-src[^;]*https:/);
    expect(csp).toMatch(/media-src[^;]*https:/);
    expect(csp).toMatch(/connect-src[^;]*https:/);
    expect((panel!.webview._iframe as HTMLIFrameElement).getAttribute("sandbox"))
      .toBe("allow-scripts");
  });
});

// ---------------------------------------------------------------------------
// Privileged runtime operation confirmation
// ---------------------------------------------------------------------------

describe("tasks.executeTask runtime confirmation", () => {
  const task = {
    name: "Sensitive task",
    source: "tests",
    definition: { type: "shell" },
    execution: {
      command: "deploy",
      args: ["--token", "task-secret"],
      cwd: "/private/task-workspace",
    },
  } satisfies Parameters<VscodeAPI["tasks"]["executeTask"]>[0];

  it("executes only after approval and redacts task details from confirmation", async () => {
    callByNameMock.mockResolvedValue(undefined);
    const confirmHandler = vi.fn<
      (operation: string, args: unknown[]) => Promise<boolean>
    >(async () => true);
    const host = new ExtensionHost({ confirmHandler });
    let api: VscodeAPI | undefined;
    await host.activateWithModule(
      makeDescriptor({ id: "tasks.ext", permissions: ["tasks.execute"] }),
      { activate: (value: VscodeAPI) => { api = value; } },
    );

    const execution = await api!.tasks.executeTask(task);

    expect(execution.task).toBe(task);
    expect(confirmHandler).toHaveBeenCalledWith("tasks.executeTask", []);
    expect(JSON.stringify(confirmHandler.mock.calls)).not.toContain("task-secret");
    expect(callByNameMock).toHaveBeenCalledWith(
      "services.TaskService.RequestExecutionApproval",
      expect.stringMatching(/^task:/),
      "deploy --token task-secret",
      "/private/task-workspace",
    );
    expect(callByNameMock).toHaveBeenCalledWith(
      "services.TaskService.Execute",
      expect.stringMatching(/^task:/),
      "deploy --token task-secret",
      "/private/task-workspace",
    );
    expect(callByIdMock).toHaveBeenCalledWith(
      93_571_653,
      expect.stringMatching(/^task:/),
      "deploy --token task-secret",
      "/private/task-workspace",
      "task-approval",
    );
    expect(confirmHandler.mock.invocationCallOrder[0]).toBeLessThan(
      callByNameMock.mock.invocationCallOrder[0],
    );
  });

  it("signals task completion so remote execution handles can be released", async () => {
    const executionFinished = deferred<void>();
    callByNameMock.mockImplementation((method: unknown) =>
      method === "services.TaskService.Execute"
        ? executionFinished.promise
        : Promise.resolve(undefined),
    );
    const host = new ExtensionHost({ confirmHandler: async () => true });
    let api: VscodeAPI | undefined;
    await host.activateWithModule(
      makeDescriptor({ id: "tasks.ext", permissions: ["tasks.execute"] }),
      { activate: (value: VscodeAPI) => { api = value; } },
    );
    const execution = await api!.tasks.executeTask(task);
    executionFinished.resolve();

    await expect(execution.completion).resolves.toBeUndefined();
  });

  it("does not execute after the extension deactivates during confirmation", async () => {
    const approval = deferred<boolean>();
    const host = new ExtensionHost({ confirmHandler: () => approval.promise });
    let api: VscodeAPI | undefined;
    await host.activateWithModule(
      makeDescriptor({ id: "tasks.ext", permissions: ["tasks.execute"] }),
      { activate: (value: VscodeAPI) => { api = value; } },
    );

    const execution = api!.tasks.executeTask(task);
    await Promise.resolve();
    await host.deactivate("tasks.ext");
    approval.resolve(true);

    await expect(execution).rejects.toThrow(/no longer active|revoked/i);
    expect(callByNameMock).not.toHaveBeenCalled();
  });

  it("preserves task arguments containing spaces and quotes", async () => {
    callByNameMock.mockResolvedValue(undefined);
    const host = new ExtensionHost({ confirmHandler: async () => true });
    let api: VscodeAPI | undefined;
    await host.activateWithModule(
      makeDescriptor({ id: "tasks.ext", permissions: ["tasks.execute"] }),
      { activate: (value: VscodeAPI) => { api = value; } },
    );

    await api!.tasks.executeTask({
      ...task,
      execution: {
        ...task.execution,
        args: ["--label", "hello world", "O'Brien", 'say "hi"'],
      },
    });

    expect(callByNameMock).toHaveBeenCalledWith(
      "services.TaskService.Execute",
      expect.stringMatching(/^task:/),
      "deploy --label 'hello world' 'O'\"'\"'Brien' 'say \"hi\"'",
      "/private/task-workspace",
    );
  });

  it("preserves backslashes in task arguments for the backend shlex parser", async () => {
    callByNameMock.mockResolvedValue(undefined);
    const host = new ExtensionHost({ confirmHandler: async () => true });
    let api: VscodeAPI | undefined;
    await host.activateWithModule(
      makeDescriptor({ id: "tasks.ext", permissions: ["tasks.execute"] }),
      { activate: (value: VscodeAPI) => { api = value; } },
    );

    await api!.tasks.executeTask({
      ...task,
      execution: {
        ...task.execution,
        args: ["--path", "C:\\temp\\file"],
      },
    });

    expect(callByNameMock).toHaveBeenCalledWith(
      "services.TaskService.Execute",
      expect.stringMatching(/^task:/),
      "deploy --path 'C:\\temp\\file'",
      "/private/task-workspace",
    );
  });

  it("requests backend termination once for an active task", async () => {
    let finishExecution: () => void = () => undefined;
    const pendingExecution = new Promise<void>((resolve) => {
      finishExecution = resolve;
    });
    callByNameMock.mockImplementation((method: unknown) =>
      method === "services.TaskService.Execute"
        ? pendingExecution
        : Promise.resolve(undefined),
    );
    const host = new ExtensionHost({ confirmHandler: async () => true });
    let api: VscodeAPI | undefined;
    await host.activateWithModule(
      makeDescriptor({ id: "tasks.ext", permissions: ["tasks.execute"] }),
      { activate: (value: VscodeAPI) => { api = value; } },
    );
    const execution = await api!.tasks.executeTask(task);
    const executeCall = callByNameMock.mock.calls.find(
      ([method]) => method === "services.TaskService.Execute",
    );
    const executionId = executeCall?.[1];
    expect(executionId).toEqual(expect.stringMatching(/^task:/));

    execution.terminate();
    execution.terminate();

    await vi.waitFor(() => {
      expect(callByNameMock).toHaveBeenCalledWith(
        "services.TaskService.Stop",
        executionId,
      );
    });
    expect(
      callByNameMock.mock.calls.filter(
        ([method]) => method === "services.TaskService.Stop",
      ),
    ).toHaveLength(1);
    finishExecution();
    await pendingExecution;
  });

  it("terminates an active task exactly once when its extension deactivates", async () => {
    let finishExecution: () => void = () => undefined;
    const pendingExecution = new Promise<void>((resolve) => {
      finishExecution = resolve;
    });
    callByNameMock.mockImplementation((method: unknown) =>
      method === "services.TaskService.Execute"
        ? pendingExecution
        : Promise.resolve(undefined),
    );
    const host = new ExtensionHost({ confirmHandler: async () => true });
    let api: VscodeAPI | undefined;
    await host.activateWithModule(
      makeDescriptor({ id: "tasks.ext", permissions: ["tasks.execute"] }),
      { activate: (value: VscodeAPI) => { api = value; } },
    );
    await api!.tasks.executeTask(task);

    await host.deactivate("tasks.ext");

    await vi.waitFor(() => {
      expect(
        callByNameMock.mock.calls.filter(
          ([method]) => method === "services.TaskService.Stop",
        ),
      ).toHaveLength(1);
    });
    finishExecution();
    await pendingExecution;
  });

  it("retries backend termination after a failed Stop request", async () => {
    let finishExecution: () => void = () => undefined;
    const pendingExecution = new Promise<void>((resolve) => {
      finishExecution = resolve;
    });
    let stopAttempts = 0;
    callByNameMock.mockImplementation((method: unknown) => {
      if (method === "services.TaskService.RequestExecutionApproval") {
        return Promise.resolve(undefined);
      }
      if (method === "services.TaskService.Execute") return pendingExecution;
      stopAttempts++;
      return stopAttempts === 1
        ? Promise.reject(new Error("temporary termination failure"))
        : Promise.resolve(undefined);
    });
    const host = new ExtensionHost({ confirmHandler: async () => true });
    let api: VscodeAPI | undefined;
    await host.activateWithModule(
      makeDescriptor({ id: "tasks.ext", permissions: ["tasks.execute"] }),
      { activate: (value: VscodeAPI) => { api = value; } },
    );
    const execution = await api!.tasks.executeTask(task);

    execution.terminate();
    execution.terminate();
    await vi.waitFor(() => expect(stopAttempts).toBe(1));
    await Promise.resolve();
    execution.terminate();
    await vi.waitFor(() => expect(stopAttempts).toBe(2));
    finishExecution();
    await pendingExecution;
  });

  it("requests cleanup when backend execution rejects", async () => {
    callByNameMock.mockImplementation((method: unknown) =>
      method === "services.TaskService.Execute"
        ? Promise.reject(new Error("execution failed"))
        : Promise.resolve(undefined),
    );
    const host = new ExtensionHost({ confirmHandler: async () => true });
    let api: VscodeAPI | undefined;
    await host.activateWithModule(
      makeDescriptor({ id: "tasks.ext", permissions: ["tasks.execute"] }),
      { activate: (value: VscodeAPI) => { api = value; } },
    );

    await api!.tasks.executeTask(task);

    await vi.waitFor(() => {
      const executeCall = callByNameMock.mock.calls.find(
        ([method]) => method === "services.TaskService.Execute",
      );
      expect(callByNameMock).toHaveBeenCalledWith(
        "services.TaskService.Stop",
        executeCall?.[1],
      );
    });
  });

  it("rejects denied execution before calling the backend", async () => {
    const host = new ExtensionHost({ confirmHandler: async () => false });
    let api: VscodeAPI | undefined;
    await host.activateWithModule(
      makeDescriptor({ id: "tasks.ext", permissions: ["tasks.execute"] }),
      { activate: (value: VscodeAPI) => { api = value; } },
    );

    await expect(api!.tasks.executeTask(task)).rejects.toThrow(
      /tasks\.executeTask.*denied/i,
    );
    expect(callByNameMock).not.toHaveBeenCalled();
  });

  it("defaults to denied when no confirmation handler exists", async () => {
    const host = new ExtensionHost();
    let api: VscodeAPI | undefined;
    await host.activateWithModule(
      makeDescriptor({ id: "tasks.ext", permissions: ["tasks.execute"] }),
      { activate: (value: VscodeAPI) => { api = value; } },
    );

    await expect(api!.tasks.executeTask(task)).rejects.toThrow(
      /tasks\.executeTask.*denied/i,
    );
    expect(callByNameMock).not.toHaveBeenCalled();
  });
});

describe("debug.startDebugging runtime confirmation", () => {
  const config = {
    type: "node",
    name: "Sensitive debug",
    request: "launch" as const,
    program: "/private/debug/program.js",
    env: { ACCESS_TOKEN: "debug-secret" },
  };

  it("launches only after approval and redacts debug configuration", async () => {
    const confirmHandler = vi.fn<
      (operation: string, args: unknown[]) => Promise<boolean>
    >(async () => true);
    const host = new ExtensionHost({ confirmHandler });
    let api: VscodeAPI | undefined;
    await host.activateWithModule(
      makeDescriptor({ id: "debug.ext", permissions: ["debug.execute"] }),
      { activate: (value: VscodeAPI) => { api = value; } },
    );

    await expect(api!.debug.startDebugging(undefined, config)).resolves.toBe(true);

    expect(confirmHandler).toHaveBeenCalledWith("debug.startDebugging", []);
    expect(JSON.stringify(confirmHandler.mock.calls)).not.toContain("debug-secret");
    expect(launchWithConfigMock).toHaveBeenCalledWith(
      expect.objectContaining({
        kind: "node",
        program: "/private/debug/program.js",
        env: { ACCESS_TOKEN: "debug-secret" },
      }),
    );
    expect(confirmHandler.mock.invocationCallOrder[0]).toBeLessThan(
      launchWithConfigMock.mock.invocationCallOrder[0],
    );
  });

  it("does not launch after the extension deactivates during confirmation", async () => {
    const approval = deferred<boolean>();
    const host = new ExtensionHost({ confirmHandler: () => approval.promise });
    let api: VscodeAPI | undefined;
    await host.activateWithModule(
      makeDescriptor({ id: "debug.ext", permissions: ["debug.execute"] }),
      { activate: (value: VscodeAPI) => { api = value; } },
    );

    const launch = api!.debug.startDebugging(undefined, config);
    await Promise.resolve();
    await host.deactivate("debug.ext");
    approval.resolve(true);

    await expect(launch).rejects.toThrow(/no longer active|revoked/i);
    expect(launchWithConfigMock).not.toHaveBeenCalled();
  });

  it("does not resolve providers or launch when confirmation is denied", async () => {
    const resolveDebugConfiguration = vi.fn(() => config);
    const host = new ExtensionHost({ confirmHandler: async () => false });
    let api: VscodeAPI | undefined;
    await host.activateWithModule(
      makeDescriptor({ id: "debug.ext", permissions: ["debug.execute"] }),
      {
        activate: (value: VscodeAPI) => {
          api = value;
          value.debug.registerDebugConfigurationProvider("node", {
            provideDebugConfigurations: () => [],
            resolveDebugConfiguration,
          });
        },
      },
    );

    await expect(api!.debug.startDebugging(undefined, config)).rejects.toThrow(
      /debug\.startDebugging.*denied/i,
    );
    expect(resolveDebugConfiguration).not.toHaveBeenCalled();
    expect(launchWithConfigMock).not.toHaveBeenCalled();
  });

  it("defaults to denied when no confirmation handler exists", async () => {
    const host = new ExtensionHost();
    let api: VscodeAPI | undefined;
    await host.activateWithModule(
      makeDescriptor({ id: "debug.ext", permissions: ["debug.execute"] }),
      { activate: (value: VscodeAPI) => { api = value; } },
    );

    await expect(api!.debug.startDebugging(undefined, config)).rejects.toThrow(
      /debug\.startDebugging.*denied/i,
    );
    expect(launchWithConfigMock).not.toHaveBeenCalled();
  });
});

describe("env.openExternal runtime confirmation", () => {
  const uri = {
    scheme: "https",
    authority: "example.test",
    path: "/launch?token=external-secret",
    fsPath: "https://example.test/launch?token=external-secret",
  };

  async function activateExternalApi(
    host: ExtensionHost,
  ): Promise<VscodeAPI> {
    host.approveExtension("external.ext");
    let api: VscodeAPI | undefined;
    await host.activateWithModule(
      makeDescriptor({
        id: "external.ext",
        permissions: ["env.openExternal"],
      }),
      { activate: (value: VscodeAPI) => { api = value; } },
    );
    return api!;
  }

  it("opens only after approval and redacts the URI", async () => {
    const confirmHandler = vi.fn<
      (operation: string, args: unknown[]) => Promise<boolean>
    >(async () => true);
    const api = await activateExternalApi(new ExtensionHost({ confirmHandler }));

    await expect(api.env.openExternal(uri)).resolves.toBe(true);

    expect(confirmHandler).toHaveBeenCalledWith("env.openExternal", []);
    expect(JSON.stringify(confirmHandler.mock.calls)).not.toContain(
      "external-secret",
    );
    expect(windowOpenMock).toHaveBeenCalledWith(
      "https://example.test/launch?token=external-secret",
      "_blank",
    );
    expect(confirmHandler.mock.invocationCallOrder[0]).toBeLessThan(
      windowOpenMock.mock.invocationCallOrder[0],
    );
  });

  it("does not open after the extension deactivates during confirmation", async () => {
    const approval = deferred<boolean>();
    const host = new ExtensionHost({ confirmHandler: () => approval.promise });
    const api = await activateExternalApi(host);

    const opening = api.env.openExternal(uri);
    await Promise.resolve();
    await host.deactivate("external.ext");
    approval.resolve(true);

    await expect(opening).rejects.toThrow(/no longer active|revoked/i);
    expect(windowOpenMock).not.toHaveBeenCalled();
  });

  it("rejects a forged https URI whose final URL uses a dangerous scheme", async () => {
    const api = await activateExternalApi(
      new ExtensionHost({ confirmHandler: async () => true }),
    );

    await expect(api.env.openExternal({
      scheme: "https",
      path: "",
      fsPath: "javascript:globalThis.compromised=true",
    })).resolves.toBe(false);

    expect(windowOpenMock).not.toHaveBeenCalled();
  });

  it("does not open when confirmation is denied", async () => {
    const api = await activateExternalApi(
      new ExtensionHost({ confirmHandler: async () => false }),
    );

    await expect(api.env.openExternal(uri)).rejects.toThrow(
      /env\.openExternal.*denied/i,
    );
    expect(windowOpenMock).not.toHaveBeenCalled();
  });

  it("defaults to denied when no confirmation handler exists", async () => {
    const api = await activateExternalApi(new ExtensionHost());

    await expect(api.env.openExternal(uri)).rejects.toThrow(
      /env\.openExternal.*denied/i,
    );
    expect(windowOpenMock).not.toHaveBeenCalled();
  });
});

describe("window.createTerminal runtime confirmation", () => {
  it("creates distinct backend sessions for consecutive terminals", async () => {
    await import("@/stores/app");
    const confirmHandler = vi.fn(async () => true);
    const host = new ExtensionHost({ confirmHandler });
    let first: ReturnType<VscodeAPI["window"]["createTerminal"]> | undefined;
    let second: ReturnType<VscodeAPI["window"]["createTerminal"]> | undefined;
    try {
      let api: VscodeAPI | undefined;
      await host.activateWithModule(
        makeDescriptor({ id: "terminal/ext", permissions: ["shell.execute"] }),
        { activate: (value: VscodeAPI) => { api = value; } },
      );

      first = api!.window.createTerminal();
      second = api!.window.createTerminal();
      expect(confirmHandler).toHaveBeenCalledTimes(2);
      await vi.waitFor(
        () => expect(startSessionMock).toHaveBeenCalledTimes(2),
        { timeout: 10_000 },
      );

      const firstId = startSessionMock.mock.calls[0][0];
      const secondId = startSessionMock.mock.calls[1][0];
      expect(firstId).not.toBe(secondId);
      expect(firstId).toMatch(/^ext-terminal%2Fext-/);
      expect(secondId).toMatch(/^ext-terminal%2Fext-/);

      first.dispose();
      second.dispose();
      first = undefined;
      second = undefined;
      await vi.waitFor(
        () => expect(killSessionMock).toHaveBeenCalledTimes(2),
        { timeout: 5_000 },
      );
      expect(killSessionMock).toHaveBeenCalledWith(firstId);
      expect(killSessionMock).toHaveBeenCalledWith(secondId);
    } finally {
      first?.dispose();
      second?.dispose();
      await host.disposeAll();
    }
  }, 20_000);

  it("buffers until one approval, then starts and writes without exposing input", async () => {
    let resolveConfirmation: ((approved: boolean) => void) | undefined;
    const confirmation = new Promise<boolean>((resolve) => {
      resolveConfirmation = resolve;
    });
    const confirmHandler = vi.fn(() => confirmation);
    const host = new ExtensionHost({ confirmHandler });
    let api: VscodeAPI | undefined;
    await host.activateWithModule(
      makeDescriptor({ id: "terminal.ext", permissions: ["shell.execute"] }),
      { activate: (value: VscodeAPI) => { api = value; } },
    );

    const terminal = api!.window.createTerminal({
      name: "Private terminal",
      cwd: "/private/terminal-workspace",
      shellPath: "/private/bin/shell",
    });
    terminal.sendText("terminal-secret");

    expect(confirmHandler).toHaveBeenCalledWith("window.createTerminal", []);
    expect(JSON.stringify(confirmHandler.mock.calls)).not.toContain(
      "terminal-secret",
    );
    expect(startSessionMock).not.toHaveBeenCalled();
    expect(writeSessionMock).not.toHaveBeenCalled();

    resolveConfirmation!(true);
    await vi.waitFor(() => expect(startSessionMock).toHaveBeenCalledTimes(1));
    await vi.waitFor(() =>
      expect(writeSessionMock).toHaveBeenCalledWith(
        expect.any(String),
        "terminal-secret\n",
      ),
    );
    expect(startSessionMock).toHaveBeenCalledWith(
      expect.any(String),
      "/private/terminal-workspace",
      "/private/bin/shell",
    );

    terminal.sendText("second-secret", false);
    await vi.waitFor(() => expect(writeSessionMock).toHaveBeenCalledTimes(2));
    expect(confirmHandler).toHaveBeenCalledTimes(1);
    expect(JSON.stringify(confirmHandler.mock.calls)).not.toContain(
      "second-secret",
    );

    terminal.dispose();
    await vi.waitFor(() => expect(killSessionMock).toHaveBeenCalledTimes(1));
  });

  it("clears queued writes and stays unavailable after denial", async () => {
    let resolveConfirmation: ((approved: boolean) => void) | undefined;
    const confirmation = new Promise<boolean>((resolve) => {
      resolveConfirmation = resolve;
    });
    const host = new ExtensionHost({ confirmHandler: () => confirmation });
    let terminal: ReturnType<VscodeAPI["window"]["createTerminal"]> | undefined;
    await host.activateWithModule(
      makeDescriptor({ id: "terminal.ext", permissions: ["shell.execute"] }),
      {
        activate: (api: VscodeAPI) => {
          terminal = api.window.createTerminal();
          terminal.sendText("queued-secret");
        },
      },
    );

    resolveConfirmation!(false);
    await new Promise<void>((resolve) => setTimeout(resolve, 0));
    terminal!.sendText("post-denial-secret");
    terminal!.dispose();
    await new Promise<void>((resolve) => setTimeout(resolve, 0));

    expect(startSessionMock).not.toHaveBeenCalled();
    expect(writeSessionMock).not.toHaveBeenCalled();
    expect(killSessionMock).not.toHaveBeenCalled();
  });

  it("defaults to unavailable without a confirmation handler", async () => {
    const host = new ExtensionHost();
    let terminal: ReturnType<VscodeAPI["window"]["createTerminal"]> | undefined;
    await host.activateWithModule(
      makeDescriptor({ id: "terminal.ext", permissions: ["shell.execute"] }),
      {
        activate: (api: VscodeAPI) => {
          terminal = api.window.createTerminal();
          terminal.sendText("queued-secret");
        },
      },
    );

    await new Promise<void>((resolve) => setTimeout(resolve, 0));
    terminal!.sendText("post-denial-secret");
    terminal!.dispose();
    await new Promise<void>((resolve) => setTimeout(resolve, 0));

    expect(startSessionMock).not.toHaveBeenCalled();
    expect(writeSessionMock).not.toHaveBeenCalled();
    expect(killSessionMock).not.toHaveBeenCalled();
  });
});

// ---------------------------------------------------------------------------
// ExtensionHost instance state isolation
// ---------------------------------------------------------------------------

describe("ExtensionHost instance state isolation", () => {
  it("isolates clipboard fallback values by extension and host instance", async () => {
    const firstHost = new ExtensionHost();
    const secondHost = new ExtensionHost();
    let alphaApi: VscodeAPI | undefined;
    let betaApi: VscodeAPI | undefined;
    let secondAlphaApi: VscodeAPI | undefined;

    await firstHost.activateWithModule(
      makeDescriptor({ id: "alpha", permissions: ["clipboard"] }),
      { activate: (api: VscodeAPI) => { alphaApi = api; } },
    );
    await firstHost.activateWithModule(
      makeDescriptor({ id: "beta", permissions: ["clipboard"] }),
      { activate: (api: VscodeAPI) => { betaApi = api; } },
    );
    await secondHost.activateWithModule(
      makeDescriptor({ id: "alpha", permissions: ["clipboard"] }),
      { activate: (api: VscodeAPI) => { secondAlphaApi = api; } },
    );

    await alphaApi!.env.clipboard.writeText("alpha private clipboard");

    expect(await alphaApi!.env.clipboard.readText()).toBe(
      "alpha private clipboard",
    );
    expect(await betaApi!.env.clipboard.readText()).toBe("");
    expect(await secondAlphaApi!.env.clipboard.readText()).toBe("");
  });

  it("owns tree and webview provider registries per host instance", async () => {
    const firstHost = new ExtensionHost();
    const secondHost = new ExtensionHost();

    for (const [host, id] of [
      [firstHost, "alpha"],
      [secondHost, "beta"],
    ] as const) {
      await host.activateWithModule(makeDescriptor({ id, permissions: ["ui.webview"] }), {
        activate: (api: VscodeAPI) => {
          api.window.registerTreeDataProvider("shared.tree", {
            getTreeItem: (item: string) => ({ label: item }),
            getChildren: () => [id],
          });
          api.window.registerWebviewViewProvider("shared.webview", {
            resolveWebviewView: () => undefined,
          });
        },
      });
    }

    type HostRegistries = {
      treeViewProviders: Map<string, unknown>;
      webviewViewProviders: Map<string, unknown>;
    };
    const first = firstHost as unknown as HostRegistries;
    const second = secondHost as unknown as HostRegistries;
    expect(first.treeViewProviders).toBeInstanceOf(Map);
    expect(first.webviewViewProviders).toBeInstanceOf(Map);
    expect(first.treeViewProviders).not.toBe(second.treeViewProviders);
    expect(first.webviewViewProviders).not.toBe(second.webviewViewProviders);
    expect(first.treeViewProviders.size).toBe(1);
    expect(second.treeViewProviders.size).toBe(1);
  });
});

describe("extension-owned listeners and provider registrations", () => {
  it("removes all workspace document listeners when the extension deactivates", async () => {
    const host = new ExtensionHost();
    const onSave = vi.fn();
    const onChange = vi.fn();
    const onOpen = vi.fn();
    await host.activateWithModule(makeDescriptor({ id: "listeners.ext" }), {
      activate: (api: VscodeAPI) => {
        api.workspace.onDidSaveTextDocument(onSave);
        api.workspace.onDidChangeTextDocument(onChange);
        api.workspace.onDidOpenTextDocument(onOpen);
      },
    });

    const document = {
      uri: { fsPath: "/test/project/file.ts", scheme: "file" },
      languageId: "typescript",
      getText: () => "const value = 1;",
    };
    const emitters = host as unknown as {
      onDidSaveTextDocumentEmitter: { emit(value: unknown): void };
      onDidChangeTextDocumentEmitter: { emit(value: unknown): void };
      onDidOpenTextDocumentEmitter: { emit(value: unknown): void };
    };
    emitters.onDidSaveTextDocumentEmitter.emit(document);
    emitters.onDidChangeTextDocumentEmitter.emit({
      document,
      contentChanges: [],
    });
    emitters.onDidOpenTextDocumentEmitter.emit(document);
    expect(onSave).toHaveBeenCalledTimes(1);
    expect(onChange).toHaveBeenCalledTimes(1);
    expect(onOpen).toHaveBeenCalledTimes(1);

    await host.deactivate("listeners.ext");
    emitters.onDidSaveTextDocumentEmitter.emit(document);
    emitters.onDidChangeTextDocumentEmitter.emit({
      document,
      contentChanges: [],
    });
    emitters.onDidOpenTextDocumentEmitter.emit(document);
    expect(onSave).toHaveBeenCalledTimes(1);
    expect(onChange).toHaveBeenCalledTimes(1);
    expect(onOpen).toHaveBeenCalledTimes(1);
  });

  it("prevents another extension from hijacking a tree view id", async () => {
    const host = new ExtensionHost();
    await host.activateWithModule(makeDescriptor({ id: "alpha" }), {
      activate: (api: VscodeAPI) => {
        api.window.registerTreeDataProvider("shared.tree", {
          getTreeItem: (item: string) => ({ label: item }),
          getChildren: () => ["alpha"],
        });
      },
    });

    await expect(
      host.activateWithModule(makeDescriptor({ id: "beta" }), {
        activate: (api: VscodeAPI) => {
          api.window.registerTreeDataProvider("shared.tree", {
            getTreeItem: (item: string) => ({ label: item }),
            getChildren: () => ["beta"],
          });
        },
      }),
    ).rejects.toThrow(/shared\.tree.*already registered.*alpha/i);

    const registries = host as unknown as {
      treeViewProviders: Map<string, { extensionId: string }>;
    };
    expect(registries.treeViewProviders.get("shared.tree")?.extensionId).toBe(
      "alpha",
    );
  });

  it("prevents another extension from hijacking a webview view id", async () => {
    const host = new ExtensionHost();
    await host.activateWithModule(
      makeDescriptor({ id: "alpha", permissions: ["ui.webview"] }),
      {
        activate: (api: VscodeAPI) => {
          api.window.registerWebviewViewProvider("shared.webview", {
            resolveWebviewView: () => undefined,
          });
        },
      },
    );

    await expect(
      host.activateWithModule(
        makeDescriptor({ id: "beta", permissions: ["ui.webview"] }),
        {
          activate: (api: VscodeAPI) => {
            api.window.registerWebviewViewProvider("shared.webview", {
              resolveWebviewView: () => undefined,
            });
          },
        },
      ),
    ).rejects.toThrow(/shared\.webview.*already registered.*alpha/i);

    const registries = host as unknown as {
      webviewViewProviders: Map<string, { extensionId: string }>;
    };
    expect(
      registries.webviewViewProviders.get("shared.webview")?.extensionId,
    ).toBe("alpha");
  });

  it("keeps same-owner replacements when an older disposable is disposed", async () => {
    const host = new ExtensionHost();
    let firstDisposable: { dispose(): void } | undefined;
    await host.activateWithModule(makeDescriptor({ id: "alpha" }), {
      activate: (api: VscodeAPI) => {
        firstDisposable = api.window.registerTreeDataProvider("alpha.tree", {
          getTreeItem: (item: string) => ({ label: item }),
          getChildren: () => ["first"],
        });
        api.window.registerTreeDataProvider("alpha.tree", {
          getTreeItem: (item: string) => ({ label: item }),
          getChildren: () => ["replacement"],
        });
      },
    });

    firstDisposable!.dispose();
    const registries = host as unknown as {
      treeViewProviders: Map<
        string,
        { provider: { getChildren(): string[] | PromiseLike<string[]> } }
      >;
    };
    const children = await registries.treeViewProviders
      .get("alpha.tree")!
      .provider.getChildren();
    expect(children).toEqual(["replacement"]);
  });

  it("prevents task and debug provider type conflicts across extensions", async () => {
    const host = new ExtensionHost();
    await host.activateWithModule(makeDescriptor({ id: "alpha" }), {
      activate: (api: VscodeAPI) => {
        api.tasks.registerTaskProvider("shared-task", {
          provideTasks: () => [],
          resolveTask: () => undefined,
        });
        api.debug.registerDebugConfigurationProvider("shared-debug", {
          provideDebugConfigurations: () => [],
        });
      },
    });

    await expect(
      host.activateWithModule(makeDescriptor({ id: "task-beta" }), {
        activate: (api: VscodeAPI) => {
          api.tasks.registerTaskProvider("shared-task", {
            provideTasks: () => [],
            resolveTask: () => undefined,
          });
        },
      }),
    ).rejects.toThrow(/shared-task.*already registered.*alpha/i);
    await expect(
      host.activateWithModule(makeDescriptor({ id: "debug-beta" }), {
        activate: (api: VscodeAPI) => {
          api.debug.registerDebugConfigurationProvider("shared-debug", {
            provideDebugConfigurations: () => [],
          });
        },
      }),
    ).rejects.toThrow(/shared-debug.*already registered.*alpha/i);
  });
});

// ---------------------------------------------------------------------------
// Secrets fail closed without a trusted backend identity
// ---------------------------------------------------------------------------

describe("secrets fail closed", () => {
  it("rejects permitted reads without calling a raw Wails secret binding", async () => {
    const host = new ExtensionHost();
    let api: VscodeAPI | undefined;
    await host.activateWithModule(
      makeDescriptor({
        id: "alpha.ext",
        permissions: ["secrets.read", "secrets.write"],
      }),
      { activate: (value: VscodeAPI) => { api = value; } },
    );

    await expect(api!.secrets.get("token")).rejects.toThrow(
      /ERR_EXTENSION_SECRET_STORAGE_UNAVAILABLE/,
    );
    expect(callByIdMock).not.toHaveBeenCalled();
    expect(Object.keys(localStorage).some((key) => key.includes("secret"))).toBe(false);
    expect(sessionStorage).toHaveLength(0);
  });

  it("rejects permitted stores without a sessionStorage fallback", async () => {
    const host = new ExtensionHost();
    let api: VscodeAPI | undefined;
    await host.activateWithModule(
      makeDescriptor({
        id: "alpha.ext",
        permissions: ["secrets.write"],
      }),
      { activate: (value: VscodeAPI) => { api = value; } },
    );

    await expect(api!.secrets.store("token", "new-secret")).rejects.toThrow(
      /ERR_EXTENSION_SECRET_STORAGE_UNAVAILABLE/,
    );
    expect(callByIdMock).not.toHaveBeenCalled();
    expect(Object.keys(localStorage).some((key) => key.includes("secret"))).toBe(false);
    expect(sessionStorage).toHaveLength(0);
  });

  it("rejects permitted deletes without touching backend or renderer storage", async () => {
    const host = new ExtensionHost();
    let api: VscodeAPI | undefined;
    await host.activateWithModule(
      makeDescriptor({
        id: "alpha.ext",
        permissions: ["secrets.read", "secrets.write"],
      }),
      { activate: (value: VscodeAPI) => { api = value; } },
    );

    await expect(api!.secrets.delete("token")).rejects.toThrow(
      /ERR_EXTENSION_SECRET_STORAGE_UNAVAILABLE/,
    );
    expect(callByIdMock).not.toHaveBeenCalled();
    expect(Object.keys(localStorage).some((key) => key.includes("secret"))).toBe(false);
    expect(sessionStorage).toHaveLength(0);
  });

  it("checks permissions before reporting secure storage unavailable", async () => {
    const host = new ExtensionHost();
    let api: VscodeAPI | undefined;
    await host.activateWithModule(
      makeDescriptor({ id: "alpha.ext", permissions: [] }),
      { activate: (value: VscodeAPI) => { api = value; } },
    );

    await expect(api!.secrets.get("token")).rejects.toThrow(/secrets\.read/);
    await expect(api!.secrets.store("token", "value")).rejects.toThrow(
      /secrets\.write/,
    );
    await expect(api!.secrets.delete("token")).rejects.toThrow(/secrets\.write/);
    expect(callByIdMock).not.toHaveBeenCalled();
  });

  it("does not mutate legacy renderer secret records on rejection", async () => {
    localStorage.setItem("koyori-ide.extHost.secrets.value.legacy", "local-record");
    sessionStorage.setItem("koyori-ide.extHost.secrets.value.legacy", "session-record");
    const host = new ExtensionHost();
    let api: VscodeAPI | undefined;
    await host.activateWithModule(
      makeDescriptor({
        id: "alpha.ext",
        permissions: ["secrets.read", "secrets.write"],
      }),
      { activate: (value: VscodeAPI) => { api = value; } },
    );

    await expect(api!.secrets.store("token", "value")).rejects.toThrow(
      /ERR_EXTENSION_SECRET_STORAGE_UNAVAILABLE/,
    );
    expect(localStorage.getItem("koyori-ide.extHost.secrets.value.legacy")).toBe(
      "local-record",
    );
    expect(sessionStorage.getItem("koyori-ide.extHost.secrets.value.legacy")).toBe(
      "session-record",
    );
  });

  it("returns the same fail-closed result for distinct extension namespaces", async () => {
    const host = new ExtensionHost();
    const apis: VscodeAPI[] = [];
    for (const id of ["a.b", "a.b.c"]) {
      await host.activateWithModule(
      makeDescriptor({
          id,
        permissions: ["secrets.read", "secrets.write"],
      }),
        { activate: (api: VscodeAPI) => { apis.push(api); } },
      );
    }

    for (const api of apis) {
      await expect(api.secrets.get("token")).rejects.toThrow(
        /ERR_EXTENSION_SECRET_STORAGE_UNAVAILABLE/,
      );
    }
    expect(callByIdMock).not.toHaveBeenCalled();
  });
});

// ---------------------------------------------------------------------------
// Command registration & host-level executeCommand
// ---------------------------------------------------------------------------

describe("commands registration and execution", () => {
  it("registerCommand registers a command that executeCommand can invoke", async () => {
    const host = new ExtensionHost();
    await host.activateWithModule(makeDescriptor({ id: "alpha" }), {
      activate: (api: VscodeAPI) => {
        api.commands.registerCommand("alpha.greet", (...args: unknown[]) => {
          const name = String(args[0] ?? "");
          return `hi ${name}`;
        });
      },
    });
    const result = await host.executeCommand("alpha.greet", "world");
    expect(result).toBe("hi world");
  });

  it("executeCommand throws for an unknown command", async () => {
    const host = new ExtensionHost();
    await expect(host.executeCommand("nope")).rejects.toThrow(/not registered/i);
  });

  it("registered commands are disposed on deactivate", async () => {
    const host = new ExtensionHost();
    await host.activateWithModule(makeDescriptor({ id: "alpha" }), {
      activate: (api: VscodeAPI) => {
        api.commands.registerCommand("alpha.cmd", () => undefined);
      },
    });
    await host.deactivate("alpha");
    await expect(host.executeCommand("alpha.cmd")).rejects.toThrow(/not registered/i);
  });
});

// ---------------------------------------------------------------------------
// G13: Extension API 去除全部假成功
// ---------------------------------------------------------------------------

describe("G13 extension API no-fake-success", () => {
  it("saves dirty buffers through the injected bridge (workspace.saveAll)", async () => {
    const onSaveAll = vi.fn<() => Promise<{ savedCount: number; failedPaths: string[] }>>()
      .mockResolvedValue({ savedCount: 2, failedPaths: [] });
    const host = new ExtensionHost({ onSaveAll });
    let api: VscodeAPI | undefined;
    await host.activateWithModule(
      makeDescriptor({ id: "saver.ext", permissions: ["fs.write"] }),
      { activate: (value: VscodeAPI) => { api = value; } },
    );

    await expect(api!.workspace.saveAll()).resolves.toBe(true);
    expect(onSaveAll).toHaveBeenCalledTimes(1);
  });

  it("propagates per-file failures instead of fake success (workspace.saveAll)", async () => {
    const onSaveAll = vi.fn<() => Promise<{ savedCount: number; failedPaths: string[] }>>()
      .mockResolvedValue({ savedCount: 1, failedPaths: ["C:/a.ts", "C:/b.ts"] });
    const host = new ExtensionHost({ onSaveAll });
    let api: VscodeAPI | undefined;
    await host.activateWithModule(
      makeDescriptor({ id: "saver.ext", permissions: ["fs.write"] }),
      { activate: (value: VscodeAPI) => { api = value; } },
    );

    await expect(api!.workspace.saveAll()).rejects.toThrow(/a\.ts/);
    await expect(api!.workspace.saveAll()).rejects.toThrow(/b\.ts/);
    await expect(api!.workspace.saveAll()).rejects.toThrow(/failed for 2 file\(s\)/);
  });

  it("fails closed with a versioned error when no save bridge is wired", async () => {
    const host = new ExtensionHost();
    let api: VscodeAPI | undefined;
    await host.activateWithModule(
      makeDescriptor({ id: "saver.ext", permissions: ["fs.write"] }),
      { activate: (value: VscodeAPI) => { api = value; } },
    );

    await expect(api!.workspace.saveAll()).rejects.toThrow(/KOYORI_IDE_EXT_API_UNSUPPORTED/);
    await expect(api!.workspace.saveAll()).rejects.toThrow(/workspace\.saveAll/);
  });

  it("window.showInputBox fails closed instead of returning the default value", async () => {
    const host = new ExtensionHost();
    let api: VscodeAPI | undefined;
    await host.activateWithModule(
      makeDescriptor({ id: "ui.ext", permissions: ["ui.notifications"] }),
      { activate: (value: VscodeAPI) => { api = value; } },
    );

    await expect(
      api!.window.showInputBox({ value: "not-a-user-input" }),
    ).rejects.toThrow(/KOYORI_IDE_EXT_API_UNSUPPORTED/);
    await expect(api!.window.showInputBox({ value: "x" })).rejects.toThrow(/window\.showInputBox/);
  });

  it("window.showQuickPick fails closed instead of returning the first item", async () => {
    const host = new ExtensionHost();
    let api: VscodeAPI | undefined;
    await host.activateWithModule(
      makeDescriptor({ id: "ui.ext", permissions: ["ui.notifications"] }),
      { activate: (value: VscodeAPI) => { api = value; } },
    );

    await expect(
      api!.window.showQuickPick(["a", "b"]),
    ).rejects.toThrow(/KOYORI_IDE_EXT_API_UNSUPPORTED/);
    await expect(api!.window.showQuickPick(["a"])).rejects.toThrow(/window\.showQuickPick/);
  });

  it("routes notifications through the injected surface", async () => {
    const onNotify = vi.fn<(level: "info" | "warn" | "error", message: string) => void>();
    const host = new ExtensionHost({ onNotify });
    let api: VscodeAPI | undefined;
    await host.activateWithModule(
      makeDescriptor({ id: "notifier.ext", permissions: ["ui.notifications"] }),
      { activate: (value: VscodeAPI) => { api = value; } },
    );

    await api!.window.showErrorMessage("boom");
    expect(onNotify).toHaveBeenCalledWith("error", expect.stringContaining("boom"));
  });

  it("returns input and picker callback results, including explicit cancellation", async () => {
    const onShowInputBox = vi.fn().mockResolvedValue("typed value");
    const onShowQuickPick = vi.fn().mockResolvedValue("second");
    const host = new ExtensionHost({ onShowInputBox, onShowQuickPick });
    let api: VscodeAPI | undefined;
    await host.activateWithModule(
      makeDescriptor({ id: "picker.ext", permissions: ["ui.notifications"] }),
      { activate: (value: VscodeAPI) => { api = value; } },
    );
    await expect(api!.window.showInputBox({ prompt: "Name" })).resolves.toBe("typed value");
    await expect(api!.window.showQuickPick(["first", "second"])).resolves.toBe("second");
    onShowInputBox.mockResolvedValueOnce(undefined);
    onShowQuickPick.mockResolvedValueOnce(undefined);
    await expect(api!.window.showInputBox()).resolves.toBeUndefined();
    await expect(api!.window.showQuickPick(["first"])).resolves.toBeUndefined();
  });

  it("fails closed when a G39 callback is absent", async () => {
    const host = new ExtensionHost();
    let api: VscodeAPI | undefined;
    await host.activateWithModule(
      makeDescriptor({ id: "missing-ui.ext", permissions: ["ui.notifications"] }),
      { activate: (value: VscodeAPI) => { api = value; } },
    );
    await expect(
      Promise.resolve().then(() => api!.window.withProgress({ title: "Work" }, async () => "done")),
    ).rejects.toThrow(/KOYORI_IDE_EXT_API_UNSUPPORTED/);
    expect(() => api!.window.setStatusBarMessage("Ready"))
      .toThrow(/KOYORI_IDE_EXT_API_UNSUPPORTED/);
    expect(() => api!.window.createStatusBarItem())
      .toThrow(/KOYORI_IDE_EXT_API_UNSUPPORTED/);
    expect(() => api!.window.createOutputChannel("Missing"))
      .toThrow(/KOYORI_IDE_EXT_API_UNSUPPORTED/);
    expect(() => api!.workspace.createFileSystemWatcher("**/*.ts"))
      .toThrow(/fs.read|permission|watch/i);
  });

  it("awaits progress work and exposes status/output lifecycle", async () => {
    const events: string[] = [];
    const statusItem = {
      text: "",
      tooltip: undefined as string | undefined,
      command: undefined as string | undefined,
      show: vi.fn(() => events.push("show")),
      hide: vi.fn(() => events.push("hide")),
      dispose: vi.fn(() => events.push("dispose")),
    };
    const statusMessage = vi.fn(() => ({ dispose: vi.fn(() => events.push("status-dispose")) }));
    const output = vi.fn<(channel: string, action: "append" | "appendLine" | "clear" | "show" | "hide" | "dispose", value?: string) => void>();
    const onWithProgress = vi.fn(async (_options, task) => task({ report: (value: { message?: string }) => events.push(value.message ?? "reported") }));
    const host = new ExtensionHost({
      onSetStatusBarMessage: statusMessage,
      onCreateStatusBarItem: () => statusItem,
      onOutput: output,
      onWithProgress,
    });
    let api: VscodeAPI | undefined;
    await host.activateWithModule(
      makeDescriptor({ id: "surface.ext", permissions: ["ui.notifications"] }),
      { activate: (value: VscodeAPI) => { api = value; } },
    );
    const message = api!.window.setStatusBarMessage("Ready", 1000);
    message.dispose();
    expect(statusMessage).toHaveBeenCalledWith("Ready", 1000);
    const item = api!.window.createStatusBarItem();
    item.text = "Ready";
    item.show();
    item.hide();
    item.dispose();
    const channel = api!.window.createOutputChannel("Surface");
    channel.appendLine("hello");
    channel.show();
    channel.clear();
    channel.dispose();
    await expect(api!.window.withProgress({ title: "Work" }, async (progress) => {
      progress.report({ message: "half" });
      await Promise.resolve();
      return "done";
    })).resolves.toBe("done");
    expect(onWithProgress).toHaveBeenCalledTimes(1);
    expect(events).toEqual(expect.arrayContaining(["show", "hide", "dispose", "half", "status-dispose"]));
    expect(output.mock.calls.map((call) => call[1])).toEqual(["appendLine", "show", "clear", "dispose"]);
  });

  it("forwards configuration changes and stops after listener disposal", async () => {
    const host = new ExtensionHost({ onGetConfiguration: () => ({ editor: { fontSize: 14 } }) });
    let api: VscodeAPI | undefined;
    await host.activateWithModule(
      makeDescriptor({ id: "config.ext" }),
      { activate: (value: VscodeAPI) => { api = value; } },
    );
    const listener = vi.fn();
    const disposable = api!.workspace.onDidChangeConfiguration(listener);
    host.notifyConfigurationChange("editor");
    expect(listener).toHaveBeenCalledTimes(1);
    const event = listener.mock.calls[0][0] as { affectsConfiguration(section: string): boolean };
    expect(event.affectsConfiguration("editor.fontSize")).toBe(true);
    disposable.dispose();
    host.notifyConfigurationChange("editor");
    expect(listener).toHaveBeenCalledTimes(1);
  });

  it("reports watcher create/change/delete, ignores outside paths, and invalidates on generation change", async () => {
    vi.useFakeTimers();
    try {
      let entries: Array<Record<string, unknown>> = [];
      listDirectoryMock.mockImplementation(async (directory) => {
        if (directory === "/test/project") return entries;
        return [];
      });
      const host = new ExtensionHost();
      let api: VscodeAPI | undefined;
      await host.activateWithModule(
        makeDescriptor({ id: "watcher.ext", permissions: ["fs.read"] }),
        { activate: (value: VscodeAPI) => { api = value; } },
      );
      const watcher = api!.workspace.createFileSystemWatcher("**/*.ts");
      const created: string[] = [];
      const changed: string[] = [];
      const deleted: string[] = [];
      watcher.onDidCreate((uri) => created.push(uri.fsPath));
      watcher.onDidChange((uri) => changed.push(uri.fsPath));
      watcher.onDidDelete((uri) => deleted.push(uri.fsPath));
      await vi.advanceTimersByTimeAsync(0);
      entries = [
        { path: "/test/project/src/a.ts", isDir: false, size: 1, modified: 1 },
        { path: "/outside/ignored.ts", isDir: false, size: 1, modified: 1 },
      ];
      await vi.advanceTimersByTimeAsync(500);
      expect(created).toEqual(["/test/project/src/a.ts"]);
      entries = [{ path: "/test/project/src/a.ts", isDir: false, size: 2, modified: 2 }];
      await vi.advanceTimersByTimeAsync(500);
      expect(changed).toEqual(["/test/project/src/a.ts"]);
      entries = [];
      await vi.advanceTimersByTimeAsync(500);
      expect(deleted).toEqual(["/test/project/src/a.ts"]);
      appStateMock.workspaceGeneration = 2;
      entries = [{ path: "/test/project/src/new.ts", isDir: false, size: 1, modified: 1 }];
      await vi.advanceTimersByTimeAsync(500);
      expect(created).toEqual(["/test/project/src/a.ts"]);
      watcher.dispose();
      expect(listDirectoryMock).toHaveBeenCalled();
    } finally {
      vi.useRealTimers();
    }
  });
});
