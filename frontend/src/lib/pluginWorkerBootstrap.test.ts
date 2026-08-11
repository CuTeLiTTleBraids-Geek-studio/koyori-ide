import { describe, expect, it, vi } from "vitest";
import type { PluginManifest } from "@/types";
import {
  capturePluginWorkerHostMessenger,
  initializePluginWorker,
  lockDownPluginWorkerCapabilities,
  PLUGIN_WORKER_BLOCKED_GLOBALS,
  PLUGIN_WORKER_BLOCKED_NAVIGATOR_CAPABILITIES,
} from "./pluginWorkerBootstrap";

function makeManifest(): PluginManifest {
  return {
    name: "security-test",
    version: "1.0.0",
    main: "main.js",
    permissions: [],
  };
}

function defineTestCapability(owner: object, name: string, index: number): void {
  if (index % 3 === 0) {
    Object.defineProperty(owner, name, {
      configurable: true,
      enumerable: true,
      value: vi.fn(),
      writable: true,
    });
    return;
  }

  if (index % 3 === 1) {
    Object.defineProperty(owner, name, {
      configurable: false,
      enumerable: false,
      value: vi.fn(),
      writable: true,
    });
    return;
  }

  Object.defineProperty(owner, name, {
    configurable: true,
    enumerable: false,
    get: () => vi.fn(),
  });
}

function expectPermanentlyNeutralized(owner: object, name: string): void {
  const descriptor = Object.getOwnPropertyDescriptor(owner, name);
  expect(descriptor).toMatchObject({
    configurable: false,
    value: undefined,
    writable: false,
  });
}

describe("plugin Worker capability lockdown", () => {
  it("locks native capabilities before import while preserving the private host bridge", async () => {
    const globalPrototype = Object.create(null) as Record<string, unknown>;
    const scope = Object.create(globalPrototype) as Record<string, unknown>;
    const navigatorPrototype = Object.create(null) as Record<string, unknown>;
    const workerNavigator = Object.create(navigatorPrototype) as Record<string, unknown>;
    const globalOwners = new Map<string, object>();
    const navigatorOwners = new Map<string, object>();

    PLUGIN_WORKER_BLOCKED_GLOBALS.forEach((name, index) => {
      if (name === "postMessage") return;
      const owner = index % 2 === 0 ? scope : globalPrototype;
      globalOwners.set(name, owner);
      defineTestCapability(owner, name, index);
    });
    PLUGIN_WORKER_BLOCKED_NAVIGATOR_CAPABILITIES.forEach((name, index) => {
      const owner = index % 2 === 0 ? workerNavigator : navigatorPrototype;
      navigatorOwners.set(name, owner);
      defineTestCapability(owner, name, index);
    });

    const hostMessages: unknown[] = [];
    const rawPostMessage = vi.fn((message: unknown) => hostMessages.push(message));
    Object.defineProperty(scope, "postMessage", {
      configurable: true,
      value: rawPostMessage,
      writable: true,
    });
    globalOwners.set("postMessage", scope);
    Object.defineProperty(scope, "navigator", {
      configurable: true,
      value: workerNavigator,
      writable: false,
    });
    const setTimeoutCapability = vi.fn();
    const cryptoCapability = { getRandomValues: vi.fn() };
    scope.setTimeout = setTimeoutCapability;
    scope.crypto = cryptoCapability;

    // Production captures this bound bridge before removing raw postMessage.
    const capturedPostMessage = capturePluginWorkerHostMessenger(scope);
    const activate = vi.fn();
    const importModule = vi.fn(async () => {
      for (const name of PLUGIN_WORKER_BLOCKED_GLOBALS) {
        expect(Reflect.get(scope, name), name).toBeUndefined();
        expect(Reflect.set(scope, name, vi.fn()), name).toBe(false);
      }
      for (const name of PLUGIN_WORKER_BLOCKED_NAVIGATOR_CAPABILITIES) {
        expect(Reflect.get(workerNavigator, name), name).toBeUndefined();
        expect(Reflect.set(workerNavigator, name, vi.fn()), name).toBe(false);
      }
      expect(scope.setTimeout).toBe(setTimeoutCapability);
      expect(scope.crypto).toBe(cryptoCapability);
      return { activate };
    });

    await initializePluginWorker("plugin://security-test/main.js", makeManifest(), {
      scope,
      importModule,
      sendToHost: capturedPostMessage,
    });

    expect(importModule).toHaveBeenCalledOnce();
    expect(activate).toHaveBeenCalledOnce();
    for (const [name, owner] of globalOwners) {
      expectPermanentlyNeutralized(owner, name);
    }
    for (const [name, owner] of navigatorOwners) {
      expectPermanentlyNeutralized(owner, name);
    }
    expect(rawPostMessage).toHaveBeenCalledWith({ type: "activated" });
    expect(hostMessages).toEqual([{ type: "activated" }]);
  });

  it("fails closed before import when a global cannot be neutralized", async () => {
    const scope = Object.create(null) as Record<string, unknown>;
    const nativeFetch = vi.fn();
    Object.defineProperty(scope, "fetch", {
      configurable: false,
      value: nativeFetch,
      writable: false,
    });
    const activate = vi.fn();
    const importModule = vi.fn(async () => ({ activate }));
    const sendToHost = vi.fn();

    await initializePluginWorker("plugin://security-test/main.js", makeManifest(), {
      scope,
      importModule,
      sendToHost,
    });

    expect(importModule).not.toHaveBeenCalled();
    expect(activate).not.toHaveBeenCalled();
    expect(nativeFetch).not.toHaveBeenCalled();
    expect(sendToHost).toHaveBeenCalledWith({
      type: "activation-error",
      error: expect.stringContaining("globalThis.fetch"),
    });
  });

  it("does not expose the raw host bridge through a replaced Reflect.apply", async () => {
    const scope = Object.create(null) as Record<string, unknown>;
    const rawPostMessage = vi.fn();
    Object.defineProperty(scope, "postMessage", {
      configurable: true,
      value: rawPostMessage,
      writable: true,
    });
    const capturedPostMessage = capturePluginWorkerHostMessenger(scope);
    const originalApply = Reflect.apply;
    const interceptedTargets: unknown[] = [];
    const compromisedApply = (
      ...args: Parameters<typeof Reflect.apply>
    ): ReturnType<typeof Reflect.apply> => {
      const [target, thisArgument, argumentsList] = args;
      interceptedTargets.push(target);
      return originalApply(target, thisArgument, argumentsList);
    };
    const activate = vi.fn(
      (context: {
        commands: {
          register(id: string, handler: () => void): void;
        };
      }) => {
        context.commands.register("extension.security-test.run", vi.fn());
      },
    );
    const importModule = vi.fn(async () => {
      Object.defineProperty(Reflect, "apply", { value: compromisedApply });
      return { activate };
    });

    try {
      await initializePluginWorker("plugin://security-test/main.js", makeManifest(), {
        scope,
        importModule,
        sendToHost: capturedPostMessage,
      });
    } finally {
      Object.defineProperty(Reflect, "apply", { value: originalApply });
    }

    expect(activate).toHaveBeenCalledOnce();
    expect(interceptedTargets).not.toContain(rawPostMessage);
    expect(rawPostMessage).toHaveBeenCalledWith(
      expect.objectContaining({
        type: "rpc-request",
        method: "commands.register",
        args: ["extension.security-test.run"],
      }),
    );
    expect(rawPostMessage).toHaveBeenCalledWith({ type: "activated" });
    expect(Reflect.get(scope, "postMessage")).toBeUndefined();
  });

  it("fails closed when a navigator prototype capability remains reachable", async () => {
    const navigatorPrototype = Object.create(null) as Record<string, unknown>;
    const workerNavigator = Object.create(navigatorPrototype) as Record<string, unknown>;
    const scope = Object.create(null) as Record<string, unknown>;
    const nativeStorage = { getDirectory: vi.fn() };
    Object.defineProperty(navigatorPrototype, "storage", {
      configurable: false,
      get: () => nativeStorage,
    });
    Object.defineProperty(scope, "navigator", {
      configurable: false,
      value: workerNavigator,
      writable: false,
    });
    const activate = vi.fn();
    const importModule = vi.fn(async () => ({ activate }));
    const sendToHost = vi.fn();

    await initializePluginWorker("plugin://security-test/main.js", makeManifest(), {
      scope,
      importModule,
      sendToHost,
    });

    expect(importModule).not.toHaveBeenCalled();
    expect(activate).not.toHaveBeenCalled();
    expect(nativeStorage.getDirectory).not.toHaveBeenCalled();
    expect(sendToHost).toHaveBeenCalledWith({
      type: "activation-error",
      error: expect.stringContaining("navigator.storage"),
    });
  });

  it("accepts already locked non-configurable undefined capabilities", () => {
    const scope = Object.create(null) as Record<string, unknown>;
    Object.defineProperty(scope, "fetch", {
      configurable: false,
      value: undefined,
      writable: false,
    });

    expect(() => lockDownPluginWorkerCapabilities(scope)).not.toThrow();
    expectPermanentlyNeutralized(scope, "fetch");
  });
});
