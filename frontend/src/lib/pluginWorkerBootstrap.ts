/**
 * Plugin Worker bootstrap script (N-26).
 *
 * This file runs inside a Web Worker. It receives an 'init' message
 * from the host containing the plugin URL and manifest, dynamically
 * imports the plugin module, and calls its activate() function with
 * a proxied koyoriIde context.
 *
 * The proxied context forwards all koyoriIde.* API calls to the host via
 * postMessage (rpc-request). The host validates permissions and
 * dispatches to the real service, then sends the result back via
 * rpc-response.
 *
 * Before untrusted plugin code is imported, the Worker removes direct access
 * to browser networking, persistence, device, nested-worker, and raw messaging
 * capabilities. The bootstrap keeps only a private, bound postMessage bridge.
 *
 * The Worker has NO access to:
 *   - window, document, or browser persistence APIs
 *   - Monaco editor (runs on the main thread)
 *   - Vue app root
 *   - Wails IPC bindings (window.go is on the main thread)
 *
 * This isolation is the core security benefit of the sandbox: a
 * malicious plugin cannot steal API keys or bypass permissions because
 * it can only communicate through the permission-gated postMessage bridge.
 */
// Koyori IDE 模块 · Plugin Worker Bootstrap。
// 喵，这是 Koyori IDE 的 Plugin Worker Bootstrap 模块（前端实现）~

/// <reference lib="webworker" />

import type { PluginManifest, PluginPermission } from "@/types";
import { getExtensionCommandPrefix } from "./pluginSandbox";
import type { HostToWorkerMessage, WorkerToHostMessage, RpcMethod } from "./pluginSandbox";

/**
 * Globals that would let plugin code bypass the permission-gated host RPC.
 * Missing capabilities are allowed because browser Worker implementations vary.
 */
export const PLUGIN_WORKER_BLOCKED_GLOBALS = [
  "AbsoluteOrientationSensor",
  "Accelerometer",
  "AmbientLightSensor",
  "BroadcastChannel",
  "EventSource",
  "FontFace",
  "GeolocationSensor",
  "GravitySensor",
  "Gyroscope",
  "IdleDetector",
  "LinearAccelerationSensor",
  "Magnetometer",
  "MessageChannel",
  "NDEFReader",
  "Notification",
  "PaymentRequest",
  "PressureObserver",
  "RTCPeerConnection",
  "RTCDataChannel",
  "RTCDtlsTransport",
  "RTCIceGatherer",
  "RTCIceTransport",
  "RTCSctpTransport",
  "RelativeOrientationSensor",
  "SharedWorker",
  "TCPServerSocket",
  "TCPSocket",
  "UDPSocket",
  "WebSocket",
  "WebSocketStream",
  "WebTransport",
  "Worker",
  "XMLHttpRequest",
  "addEventListener",
  "caches",
  "close",
  "cookieStore",
  "dispatchEvent",
  "fetch",
  "fetchLater",
  "fonts",
  "importScripts",
  "indexedDB",
  "localStorage",
  "onmessage",
  "open",
  "postMessage",
  "removeEventListener",
  "requestFileSystem",
  "resolveLocalFileSystemURL",
  "sessionStorage",
  "showDirectoryPicker",
  "showOpenFilePicker",
  "showSaveFilePicker",
  "webkitRTCPeerConnection",
  "webkitRequestFileSystem",
  "webkitResolveLocalFileSystemURL",
] as const;

/** Browser-owned capabilities reachable through WorkerNavigator. */
export const PLUGIN_WORKER_BLOCKED_NAVIGATOR_CAPABILITIES = [
  "bluetooth",
  "clipboard",
  "contacts",
  "credentials",
  "geolocation",
  "getBattery",
  "getGamepads",
  "getInstalledRelatedApps",
  "getUserMedia",
  "getVRDisplays",
  "gpu",
  "hid",
  "keyboard",
  "locks",
  "mediaDevices",
  "nfc",
  "openTCPSocket",
  "openUDPSocket",
  "permissions",
  "presentation",
  "registerProtocolHandler",
  "requestMediaKeySystemAccess",
  "requestMIDIAccess",
  "sendBeacon",
  "serial",
  "serviceWorker",
  "share",
  "storage",
  "storageBuckets",
  "unregisterProtocolHandler",
  "usb",
  "virtualKeyboard",
  "wakeLock",
  "webkitPersistentStorage",
  "webkitGetGamepads",
  "webkitGetUserMedia",
  "webkitTemporaryStorage",
  "xr",
] as const;

type PluginModuleImporter = (pluginUrl: string) => Promise<unknown>;

interface PluginWorkerInitOptions {
  scope?: object;
  importModule?: PluginModuleImporter;
  sendToHost?: (message: WorkerToHostMessage) => void;
}

const workerScope = self as unknown as DedicatedWorkerGlobalScope;
const safeReflectApply = Reflect.apply;
const safeReflectGet = Reflect.get;
const safeFunctionBind = Function.prototype.bind;

function bindWorkerMethod<TArgs extends unknown[]>(
  scope: object,
  name: string,
): (...args: TArgs) => void {
  const method: unknown = safeReflectGet(scope, name);
  if (typeof method !== "function") {
    throw new Error(`Plugin worker requires ${name}()`);
  }
  const boundMethod = safeReflectApply(safeFunctionBind, method, [scope]) as (
    ...args: TArgs
  ) => unknown;
  return (...args: TArgs): void => {
    void boundMethod(...args);
  };
}

/** Capture the private bridge before raw Worker messaging is locked down. */
export function capturePluginWorkerHostMessenger(
  scope: object,
): (message: WorkerToHostMessage) => void {
  return bindWorkerMethod<[WorkerToHostMessage]>(scope, "postMessage");
}

// Capture the only host communication primitives before lockdown removes the
// raw globals from untrusted plugin code.
const postMessageToHost = capturePluginWorkerHostMessenger(workerScope);
const addWorkerEventListener = bindWorkerMethod<[
  string,
  EventListenerOrEventListenerObject,
]>(workerScope, "addEventListener");
const closeWorker = bindWorkerMethod<[]>(workerScope, "close");

// ---------------------------------------------------------------------------
// State
// ---------------------------------------------------------------------------

let nextRequestId = 0;
const pendingRequests = new Map<
  number,
  { resolve: (v: unknown) => void; reject: (e: Error) => void }
>();
let activePluginDeactivate: (() => unknown | Promise<unknown>) | null = null;
let workerTerminating = false;
let pluginInitialization: Promise<void> | null = null;

const RESERVED_COMMAND_NAMESPACES = new Set([
  "_workbench",
  "debug",
  "editor",
  "explorer",
  "file",
  "files",
  "git",
  "preferences",
  "scm",
  "search",
  "settings",
  "task",
  "tasks",
  "terminal",
  "view",
  "views",
  "vscode",
  "window",
  "workbench",
  "workspace",
]);

function lockdownError(capability: string, cause?: unknown): Error {
  const suffix = cause instanceof Error && cause.message ? `: ${cause.message}` : "";
  return new Error(
    `Plugin worker security lockdown failed for ${capability}${suffix}`,
  );
}

function isNeutralizedDescriptor(descriptor: PropertyDescriptor): boolean {
  if ("value" in descriptor) {
    return (
      descriptor.value === undefined &&
      descriptor.writable === false &&
      descriptor.configurable === false
    );
  }
  return (
    descriptor.get === undefined &&
    descriptor.set === undefined &&
    descriptor.configurable === false
  );
}

function getCapabilityOwners(root: object, name: string, label: string): object[] {
  const owners: object[] = [];
  const visited = new Set<object>();
  let current: object | null = root;

  try {
    while (current && !visited.has(current)) {
      visited.add(current);
      if (Object.getOwnPropertyDescriptor(current, name)) {
        owners.push(current);
      }
      current = Object.getPrototypeOf(current);
    }
  } catch (error) {
    throw lockdownError(label, error);
  }

  return owners;
}

function neutralizeOwnCapability(owner: object, name: string, label: string): void {
  const descriptor = Object.getOwnPropertyDescriptor(owner, name);
  if (!descriptor || isNeutralizedDescriptor(descriptor)) return;

  try {
    Object.defineProperty(owner, name, {
      configurable: false,
      enumerable: descriptor.enumerable ?? false,
      value: undefined,
      writable: false,
    });
  } catch (error) {
    throw lockdownError(label, error);
  }

  const lockedDescriptor = Object.getOwnPropertyDescriptor(owner, name);
  if (!lockedDescriptor || !isNeutralizedDescriptor(lockedDescriptor)) {
    throw lockdownError(label);
  }
}

/**
 * Remove a capability from both the instance and every reachable prototype.
 * Merely shadowing a WorkerGlobalScope property is insufficient because plugin
 * code can inspect its prototype and recover the original native function.
 */
function neutralizeCapability(root: object, name: string, label: string): void {
  const owners = getCapabilityOwners(root, name, label);
  if (owners.length === 0) return;

  for (const owner of owners) {
    neutralizeOwnCapability(owner, name, label);
  }

  try {
    if (!Object.getOwnPropertyDescriptor(root, name) && Object.isExtensible(root)) {
      Object.defineProperty(root, name, {
        configurable: false,
        enumerable: false,
        value: undefined,
        writable: false,
      });
    }
  } catch (error) {
    throw lockdownError(label, error);
  }

  for (const owner of getCapabilityOwners(root, name, label)) {
    const descriptor = Object.getOwnPropertyDescriptor(owner, name);
    if (!descriptor || !isNeutralizedDescriptor(descriptor)) {
      throw lockdownError(label);
    }
  }

  try {
    if (Reflect.get(root, name) !== undefined) {
      throw lockdownError(label);
    }
  } catch (error) {
    if (error instanceof Error && error.message.startsWith("Plugin worker security")) {
      throw error;
    }
    throw lockdownError(label, error);
  }
}

function getWorkerNavigator(scope: object): object | undefined {
  try {
    if (!Reflect.has(scope, "navigator")) return undefined;
    const navigatorValue: unknown = Reflect.get(scope, "navigator");
    if (navigatorValue === undefined || navigatorValue === null) return undefined;
    if (typeof navigatorValue !== "object" && typeof navigatorValue !== "function") {
      throw lockdownError("navigator");
    }
    return navigatorValue;
  } catch (error) {
    if (error instanceof Error && error.message.startsWith("Plugin worker security")) {
      throw error;
    }
    throw lockdownError("navigator", error);
  }
}

/** Fail closed before any untrusted plugin module code is evaluated. */
export function lockDownPluginWorkerCapabilities(scope: object): void {
  for (const name of PLUGIN_WORKER_BLOCKED_GLOBALS) {
    neutralizeCapability(scope, name, `globalThis.${name}`);
  }

  const workerNavigator = getWorkerNavigator(scope);
  if (!workerNavigator) return;
  for (const name of PLUGIN_WORKER_BLOCKED_NAVIGATOR_CAPABILITIES) {
    neutralizeCapability(workerNavigator, name, `navigator.${name}`);
  }
}

async function importPluginModule(pluginUrl: string): Promise<unknown> {
  return import(/* @vite-ignore */ pluginUrl);
}

/**
 * Initialize a plugin after locking down its Worker realm. Injectable options
 * keep the security boundary directly testable without weakening production.
 */
export async function initializePluginWorker(
  pluginUrl: string,
  mfst: PluginManifest,
  options: PluginWorkerInitOptions = {},
): Promise<void> {
  const scope = options.scope ?? workerScope;
  const importer = options.importModule ?? importPluginModule;
  const notifyHost = options.sendToHost ?? postMessageToHost;

  try {
    lockDownPluginWorkerCapabilities(scope);
    const module: unknown = await importer(pluginUrl);
    if (workerTerminating) return;
    if (
      module === null ||
      (typeof module !== "object" && typeof module !== "function")
    ) {
      throw new Error(`Plugin "${mfst.name}" main module is invalid`);
    }
    const activate: unknown = safeReflectGet(module, "activate");
    if (typeof activate !== "function") {
      throw new Error(
        `Plugin "${mfst.name}" main module does not export an activate() function`,
      );
    }
    const deactivate: unknown = safeReflectGet(module, "deactivate");

    const context = createProxyContext(mfst, (method, args) =>
      sendRpcRequest(method, args, notifyHost),
    );
    await safeReflectApply(activate, module, [context]);
    activePluginDeactivate =
      typeof deactivate === "function"
        ? () => safeReflectApply(deactivate, module, [])
        : null;
    if (!workerTerminating) notifyHost({ type: "activated" });
  } catch (error: unknown) {
    activePluginDeactivate = null;
    const errorMessage = error instanceof Error ? error.message : String(error);
    if (!workerTerminating) {
      notifyHost({ type: "activation-error", error: errorMessage });
    }
  }
}

// ---------------------------------------------------------------------------
// Message handling (from host)
// ---------------------------------------------------------------------------

async function handleWorkerMessage(event: Event): Promise<void> {
  const msg = (event as MessageEvent).data as HostToWorkerMessage;
  if (!msg || typeof msg !== "object" || !("type" in msg)) return;
  if (workerTerminating && msg.type !== "terminate" && msg.type !== "rpc-response") return;

  switch (msg.type) {
    case "init":
      await handleInit(msg.pluginUrl, msg.manifest);
      break;

    case "rpc-response":
      handleRpcResponse(msg.id, msg.result, msg.error);
      break;

    case "rpc-call":
      // N-31: The host is calling INTO the worker (e.g. to execute a
      // command handler). Look up the handler and return the result.
      await handleRpcCall(msg.id, msg.method, msg.args);
      break;

    case "terminate":
      // The host is terminating us. Clean up and stop.
      await shutdownPluginWorker();
      break;
  }
}

addWorkerEventListener("message", {
  handleEvent(event: Event): void {
    void handleWorkerMessage(event);
  },
});

// ---------------------------------------------------------------------------
// Init: load and activate the plugin
// ---------------------------------------------------------------------------

async function handleInit(pluginUrl: string, mfst: PluginManifest): Promise<void> {
  if (workerTerminating) return;
  const initialization = initializePluginWorker(pluginUrl, mfst);
  pluginInitialization = initialization;
  try {
    await initialization;
  } finally {
    if (pluginInitialization === initialization) pluginInitialization = null;
  }
}

// ---------------------------------------------------------------------------
// Proxy koyoriIde context
// ---------------------------------------------------------------------------

interface ProxyNknkAPI {
  manifest: PluginManifest;
  commands: {
    register(id: string, handler: (...args: unknown[]) => unknown | Promise<unknown>): void;
    registerCommand(id: string, handler: (...args: unknown[]) => unknown | Promise<unknown>): void;
    execute(id: string, ...args: unknown[]): Promise<unknown>;
    executeCommand(id: string, ...args: unknown[]): Promise<unknown>;
  };
  views: {
    register(
      id: string,
      component: unknown,
      options?: { title?: string; location?: "sidebar" | "panel" | "statusbar" },
    ): void;
  };
  workspace: {
    readFile(relPath: string): Promise<string>;
    writeFile(relPath: string, content: string): Promise<void>;
  };
  getPermissions(): PluginPermission[];
}

/**
 * Create a proxy koyoriIde context that forwards all API calls to the host
 * via postMessage. The host validates permissions before executing.
 *
 * Note: commands.register and views.register send the handler/component
 * reference to the host. Since functions can't be serialized via
 * structured clone, the host stores a reference and calls back via
 * rpc-call when the command needs to be executed. (Cross-sandbox command
 * execution is a future enhancement; for now, commands.register just
 * notifies the host of the command's metadata.)
 */
function createProxyContext(
  mfst: PluginManifest,
  callHost: (method: RpcMethod, args: unknown[]) => Promise<unknown> = sendRpcRequest,
): ProxyNknkAPI {

  const registerCommand = (
    id: string,
    handler: (...args: unknown[]) => unknown | Promise<unknown>,
  ): void => {
    assertWorkerCommandId(mfst.name, id);
    commandHandlers.set(id, handler);
    void callHost("commands.register", [id]).catch(() => {
      // Host-side validation uses the authoritative registry extension ID.
      if (commandHandlers.get(id) === handler) {
        commandHandlers.delete(id);
      }
    });
  };

  return {
    manifest: mfst,
    commands: {
      register(id, handler) {
        assertWorkerCommandId(mfst.name, id);
        // Send the command registration to the host. The handler can't
        // be serialized, so we store it locally and the host calls
        // back via rpc-call when the command needs to execute.
        // For now, we just notify the host of the command metadata.
        // The handler is stored in a local map for later invocation.
        commandHandlers.set(id, handler);
        void callHost("commands.register", [id]).catch(() => {
          // Registration failed (e.g. duplicate ID). Ignore — the host
          // will have already thrown, and we can't undo the local
          // registration easily. The plugin will see the error when
          // it tries to execute the command.
          if (commandHandlers.get(id) === handler) {
            commandHandlers.delete(id);
          }
        });
      },
      registerCommand,
      async execute(id, ...args) {
        return callHost("commands.execute", [id, ...args]);
      },
      async executeCommand(id, ...args) {
        return callHost("commands.execute", [id, ...args]);
      },
    },
    views: {
      register(id, _component, options) {
        // The component can't be serialized across the Worker boundary.
        // The host will need to load the component separately (e.g. via
        // an iframe with the plugin's HTML/JS bundle). For now, we just
        // register the view metadata.
        void callHost("views.register", [id, options ?? {}]).catch(() => {});
      },
    },
    workspace: {
      async readFile(relPath) {
        return callHost("workspace.readFile", [relPath]) as Promise<string>;
      },
      async writeFile(relPath, content) {
        await callHost("workspace.writeFile", [relPath, content]);
      },
    },
    getPermissions() {
      return mfst.permissions ?? [];
    },
  };
}

// Local store of command handlers (can't be sent to the host).
const commandHandlers = new Map<
  string,
  (...args: unknown[]) => unknown | Promise<unknown>
>();

/** Mirror the host's namespace rule so invalid registration fails activation. */
function assertWorkerCommandId(extensionId: string, commandId: string): void {
  if (commandId.length === 0 || commandId !== commandId.trim()) {
    throw new Error(
      `Plugin "${extensionId}" cannot register a command without a valid string ID`,
    );
  }

  const namespace = commandId.split(".", 1)[0].toLowerCase();
  if (RESERVED_COMMAND_NAMESPACES.has(namespace)) {
    throw new Error(
      `Plugin "${extensionId}" cannot register command "${commandId}": namespace "${namespace}" is reserved for built-in commands`,
    );
  }

  const requiredPrefix = getExtensionCommandPrefix(extensionId);
  if (
    !commandId.startsWith(requiredPrefix) ||
    commandId.length === requiredPrefix.length
  ) {
    throw new Error(
      `Plugin "${extensionId}" cannot register command "${commandId}": extension commands must use the "${requiredPrefix}" namespace`,
    );
  }
}

// ---------------------------------------------------------------------------
// RPC request/response (Worker → Host)
// ---------------------------------------------------------------------------

function sendRpcRequest(
  method: RpcMethod,
  args: unknown[],
  sendMessage: (message: WorkerToHostMessage) => void = sendToHost,
): Promise<unknown> {
  return new Promise((resolve, reject) => {
    const id = nextRequestId++;
    pendingRequests.set(id, { resolve, reject });
    sendMessage({ type: "rpc-request", id, method, args });
  });
}

function handleRpcResponse(id: number, result: unknown, error?: string): void {
  const pending = pendingRequests.get(id);
  if (!pending) return;
  pendingRequests.delete(id);
  if (error) {
    pending.reject(new Error(error));
  } else {
    pending.resolve(result);
  }
}

async function shutdownPluginWorker(): Promise<void> {
  if (workerTerminating) return;
  workerTerminating = true;
  try {
    await pluginInitialization;
    await activePluginDeactivate?.();
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    sendToHost({ type: "log", level: "warn", message: `Plugin deactivate failed: ${message}` });
  } finally {
    activePluginDeactivate = null;
    const terminationError = new Error("Plugin Worker terminated");
    for (const pending of pendingRequests.values()) {
      pending.reject(terminationError);
    }
    pendingRequests.clear();
    commandHandlers.clear();
    try {
      sendToHost({ type: "terminated" });
    } finally {
      closeWorker();
    }
  }
}

/**
 * N-31: Handle an rpc-call from the host. The host calls INTO the worker
 * to execute a command handler stored in `commandHandlers`. The result
 * is sent back via `rpc-result`.
 *
 * Supported methods:
 *   - "executeCommand": args = [commandId, ...callArgs]
 */
async function handleRpcCall(id: number, method: string, args: unknown[]): Promise<void> {
  try {
    if (method === "executeCommand") {
      const commandId = args[0] as string;
      const callArgs = args.slice(1);
      const handler = commandHandlers.get(commandId);
      if (!handler) {
        sendToHost({
          type: "rpc-result",
          id,
          error: `Command "${commandId}" not found in worker`,
        });
        return;
      }
      const result = await handler(...callArgs);
      sendToHost({ type: "rpc-result", id, result });
    } else {
      sendToHost({
        type: "rpc-result",
        id,
        error: `Unknown rpc-call method: ${method}`,
      });
    }
  } catch (e: unknown) {
    const errorMsg = e instanceof Error ? e.message : String(e);
    sendToHost({ type: "rpc-result", id, error: errorMsg });
  }
}

// ---------------------------------------------------------------------------
// Utilities
// ---------------------------------------------------------------------------

function sendToHost(msg: WorkerToHostMessage): void {
  postMessageToHost(msg);
}
