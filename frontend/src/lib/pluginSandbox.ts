/**
 * Plugin Web Worker sandbox (N-26).
 *
 * Provides isolation for plugin code by running it in a Web Worker
 * instead of the main thread. The Worker has no access to the DOM,
 * window, localStorage, or Monaco — it can only communicate with the
 * main thread via postMessage. The main thread validates permissions
 * before executing any privileged operation on behalf of the Worker.
 *
 * Message protocol:
 *   Main → Worker:  { type: 'init', pluginUrl, manifest }
 *   Main → Worker:  { type: 'rpc-call', id, method, args }
 *   Worker → Main:  { type: 'rpc-result', id, result?, error? }
 *   Worker → Main:  { type: 'rpc-request', id, method, args }
 *   Main → Worker:  { type: 'rpc-response', id, result?, error? }
 *   Worker → Main:  { type: 'activated' } | { type: 'activation-error', error }
 *   Worker → Main:  { type: 'terminated' } after async deactivation finishes
 *
 * The Worker initiates RPC requests when the plugin calls a koyoriIde.*
 * API method (e.g. koyoriIde.workspace.readFile). The host validates
 * permissions and dispatches to the real service, then sends the
 * result back as an RPC response.
 */
// Koyori IDE 模块 · Plugin Sandbox。
// 喵，这是 Koyori IDE 的 Plugin Sandbox 模块（前端实现）~
import type { PluginManifest, PluginPermission } from "@/types";

// ---------------------------------------------------------------------------
// Message protocol types
// ---------------------------------------------------------------------------

/** Messages sent from the host (main thread) to the Worker. */
export type HostToWorkerMessage =
  | { type: "init"; pluginUrl: string; manifest: PluginManifest }
  | { type: "rpc-response"; id: number; result?: unknown; error?: string }
  | { type: "rpc-call"; id: number; method: string; args: unknown[] }
  | { type: "terminate" };

/** Messages sent from the Worker to the host (main thread). */
export type WorkerToHostMessage =
  | { type: "activated" }
  | { type: "activation-error"; error: string }
  | { type: "deactivated" }
  | { type: "terminated" }
  | { type: "rpc-request"; id: number; method: string; args: unknown[] }
  | { type: "rpc-result"; id: number; result?: unknown; error?: string }
  | { type: "log"; level: "info" | "warn" | "error"; message: string };

// ---------------------------------------------------------------------------
// RPC method types
// ---------------------------------------------------------------------------

/**
 * RPC methods that the Worker (plugin) can request the host to execute.
 * Each method maps to a koyoriIde.* API call. The host validates permissions
 * before dispatching.
 *
 * The method names use dot notation matching the API surface:
 *   "workspace.readFile" → koyoriIde.workspace.readFile
 *   "workspace.writeFile" → koyoriIde.workspace.writeFile
 *   "commands.register" → koyoriIde.commands.register
 *   "commands.execute" → koyoriIde.commands.execute
 *   "views.register" → koyoriIde.views.register
 */
export type RpcMethod =
  | "workspace.readFile"
  | "workspace.writeFile"
  | "commands.register"
  | "commands.execute"
  | "views.register"
  | "getPermissions";

/** Permission required for each RPC method. Undefined = always allowed. */
const METHOD_PERMISSIONS: Partial<Record<RpcMethod, PluginPermission>> = {
  "workspace.readFile": "fs.read",
  "workspace.writeFile": "fs.write",
  // commands.register, commands.execute, views.register, getPermissions
  // are always allowed (no permission required).
};

/**
 * Command namespaces owned by the application. Sandboxed extensions never
 * register through the built-in command path, so these names must not cross
 * the Worker RPC boundary even when the extension declares no permissions.
 */
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

/**
 * Encode an extension ID as one dot-delimited command owner segment.
 *
 * `encodeURIComponent` deliberately leaves dots unchanged, so encode them
 * explicitly. This keeps `alpha` and `alpha.beta` in disjoint namespaces,
 * while percent escaping makes the representation reversible and prevents a
 * literal `%2E` in an ID from aliasing a real dot.
 */
export function encodeExtensionCommandOwner(extensionId: string): string {
  return encodeURIComponent(extensionId).replace(/\./g, "%2E");
}

/** Reverse {@link encodeExtensionCommandOwner}. */
export function decodeExtensionCommandOwner(encodedOwner: string): string {
  return decodeURIComponent(encodedOwner);
}

/** Return the canonical command prefix owned by an extension. */
export function getExtensionCommandPrefix(extensionId: string): string {
  return `extension.${encodeExtensionCommandOwner(extensionId)}.`;
}

/**
 * Validate a command registered by an untrusted sandboxed extension.
 *
 * Built-in commands are registered directly by the application and do not
 * pass through this helper. Extension commands are deliberately confined to
 * an owner-specific namespace so an empty permissions array cannot be used to
 * shadow application commands or commands belonging to another extension.
 */
export function assertExtensionCommandId(
  extensionId: string,
  commandId: unknown,
): asserts commandId is string {
  if (
    typeof commandId !== "string" ||
    commandId.length === 0 ||
    commandId !== commandId.trim()
  ) {
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
// Worker factory (injectable for testing)
// ---------------------------------------------------------------------------

/**
 * A Worker-like object that can send and receive messages. The real
 * Worker class satisfies this interface; tests provide a mock.
 */
export interface WorkerLike {
  postMessage(message: unknown): void;
  terminate(): void;
  onmessage: ((e: { data: unknown }) => void) | null;
  onerror: ((e: unknown) => void) | null;
  onmessageerror: ((e: unknown) => void) | null;
}

/**
 * Factory function that creates the bootstrap Worker selected by the host.
 * Tests and embedders may inject a custom implementation.
 */
export type WorkerFactory = (workerScriptUrl: string) => WorkerLike;

/** Marker for the Vite-bundled bootstrap entry used by the default host. */
export const DEFAULT_WORKER_SCRIPT_URL = "./pluginWorkerBootstrap.ts";

function adaptBrowserWorker(worker: Worker): WorkerLike {
  const adapted: WorkerLike = {
    postMessage(message: unknown): void {
      worker.postMessage(message);
    },
    terminate(): void {
      worker.terminate();
    },
    onmessage: null,
    onerror: null,
    onmessageerror: null,
  };
  worker.onmessage = (event: MessageEvent<unknown>) => {
    adapted.onmessage?.({ data: event.data });
  };
  worker.onerror = (event: ErrorEvent) => {
    adapted.onerror?.(event);
  };
  worker.onmessageerror = (event: MessageEvent<unknown>) => {
    adapted.onmessageerror?.(event);
  };
  return adapted;
}

/**
 * Keep this constructor expression static so Vite emits and rewrites the
 * hashed Worker entry in both development and production builds.
 */
function defaultWorkerFactory(workerScriptUrl: string): WorkerLike {
  if (workerScriptUrl === DEFAULT_WORKER_SCRIPT_URL) {
    return adaptBrowserWorker(
      new Worker(new URL("./pluginWorkerBootstrap.ts", import.meta.url), {
        type: "module",
      }),
    );
  }
  return adaptBrowserWorker(new Worker(workerScriptUrl, { type: "module" }));
}

// ---------------------------------------------------------------------------
// RPC handler (host-side dispatcher for Worker requests)
// ---------------------------------------------------------------------------

/**
 * RpcHandler processes an RPC request from a sandboxed plugin. It
 * validates permissions and dispatches to the real service. The
 * handler is injectable so tests can mock the backend services.
 *
 * Returns a Promise that resolves to the method's result, or rejects
 * with an error message string.
 */
export type RpcHandler = (
  pluginName: string,
  manifest: PluginManifest,
  method: RpcMethod,
  args: unknown[],
) => Promise<unknown>;

// ---------------------------------------------------------------------------
// PluginSandboxHost
// ---------------------------------------------------------------------------

/**
 * Structural interface for the sandbox host's public API. Allows tests
 * to inject mock implementations without subclassing PluginSandboxHost.
 */
export interface SandboxHost {
  activate(pluginName: string, manifest: PluginManifest, pluginUrl: string): Promise<void>;
  /**
   * M-18: Force-rebuild an already-activated plugin's Worker. Terminates
   * the existing sandbox (losing in-memory state) and re-activates from
   * scratch. Use this when the plugin manifest/code changed and a fresh
   * activation is required; {@link activate} itself is idempotent.
   */
  reload(pluginName: string, manifest: PluginManifest, pluginUrl: string): Promise<void>;
  callMethod(pluginName: string, method: string, args: unknown[]): Promise<unknown>;
  terminate(pluginName: string): void;
  terminateAll(): void;
  has(pluginName: string): boolean;
  /** N-40 / Proposal J2: Get health snapshot for a sandboxed plugin. */
  getHealth?(pluginName: string): PluginHealth;
  /** N-40: Subscribe to health changes. Returns an unsubscribe function. */
  onHealthChange?(listener: HealthListener): () => void;
}

interface SandboxEntry {
  worker: WorkerLike;
  manifest: PluginManifest;
  pluginUrl: string;
  /** Pending RPC requests sent TO the worker, keyed by request ID. */
  pendingCalls: Map<number, { resolve: (v: unknown) => void; reject: (e: Error) => void }>;
  /** Pending RPC requests FROM the worker, keyed by request ID. */
  pendingRequests: Map<number, number>;
  /** Next request ID to use when calling into the worker. */
  nextCallId: number;
  /** Resolves when the worker reports 'activated' or 'activation-error'. */
  activationPromise: Promise<void>;
  activationResolve?: () => void;
  activationReject?: (e: Error) => void;
  /**
   * N-40: Runtime crash tracking. Set to true when the Worker errors
   * after activation. The entry stays in the map so callers get a
   * clear "crashed" error instead of a generic "not sandboxed".
   */
  crashed: boolean;
  /** N-40: Last error message (activation or runtime). */
  lastError: string | null;
  /** N-40: Timestamp of the last crash (ms since epoch). */
  lastCrashAt: number | null;
  activated: boolean;
  hasActivatedOnce: boolean;
  recovering: boolean;
  restartAttempts: number;
  restartScheduled: boolean;
}

interface WorkerShutdownState {
  finish: () => void;
  timeout: ReturnType<typeof setTimeout>;
}

const MAX_WORKER_RESTARTS = 3;
const WORKER_TERMINATION_GRACE_MS = 250;

/**
 * N-40 / Proposal J2: Health snapshot for a sandboxed plugin.
 * Returned by getHealth() and surfaced in the PluginsView health panel.
 */
export interface PluginHealth {
  status: "activating" | "running" | "crashed" | "terminated";
  lastError: string | null;
  lastCrashAt: number | null;
}

/**
 * N-40: Listener for sandbox health changes. Called when a plugin
 * crashes, recovers, or is terminated. Used by the PluginsView to
 * refresh the health panel without polling.
 */
export type HealthListener = (pluginName: string, health: PluginHealth) => void;

interface PluginSandboxState {
  sandboxes: Map<string, SandboxEntry>;
  healthListeners: Set<HealthListener>;
}

interface PluginSandboxModuleState {
  hosts: Set<WeakRef<PluginSandboxHost>>;
}

const pluginSandboxModuleState: PluginSandboxModuleState = {
  hosts: new Set<WeakRef<PluginSandboxHost>>(),
};

/**
 * PluginSandboxHost manages Web Worker sandboxes for plugins. Each
 * sandboxed plugin runs in its own Worker. The host:
 *   1. Creates Workers and sends init messages with the plugin URL
 *   2. Routes RPC requests from Workers through the permission-gated RpcHandler
 *   3. Routes RPC calls TO Workers (for command execution from other plugins)
 *   4. Tracks activation state and pending requests
 *
 * Usage:
 *   const host = new PluginSandboxHost(rpcHandler);
 *   await host.activate('my-plugin', manifest, pluginUrl);
 *   // Plugin is now running in a Worker.
 *   const result = await host.callMethod('my-plugin', 'myCommand', [args]);
 *   host.terminate('my-plugin');
 */
export class PluginSandboxHost implements SandboxHost {
  private readonly state: PluginSandboxState = {
    sandboxes: new Map<string, SandboxEntry>(),
    healthListeners: new Set<HealthListener>(),
  };
  private rpcHandler: RpcHandler;
  private workerFactory: WorkerFactory;
  private workerScriptUrl: string;
  private readonly workerShutdowns = new Map<WorkerLike, WorkerShutdownState>();

  private get sandboxes(): Map<string, SandboxEntry> {
    return this.state.sandboxes;
  }

  private get healthListeners(): Set<HealthListener> {
    return this.state.healthListeners;
  }

  constructor(
    rpcHandler: RpcHandler,
    options?: {
      workerFactory?: WorkerFactory;
      workerScriptUrl?: string;
    },
  ) {
    this.rpcHandler = rpcHandler;
    this.workerFactory = options?.workerFactory ?? defaultWorkerFactory;
    this.workerScriptUrl = options?.workerScriptUrl ?? DEFAULT_WORKER_SCRIPT_URL;
    pluginSandboxModuleState.hosts.add(new WeakRef(this));
  }

  /** N-40: Notify all health listeners of a change. */
  private notifyHealthChange(pluginName: string, entry: SandboxEntry): void {
    this.notifyHealth(pluginName, this.entryHealth(entry));
  }

  private notifyHealth(pluginName: string, health: PluginHealth): void {
    for (const listener of this.healthListeners) {
      try {
        listener(pluginName, health);
      } catch {
        // Listener errors are ignored — health notifications must not
        // crash the host.
      }
    }
  }

  /** N-40: Compute health snapshot from an entry. */
  private entryHealth(entry: SandboxEntry): PluginHealth {
    if (entry.crashed) {
      return {
        status: "crashed",
        lastError: entry.lastError,
        lastCrashAt: entry.lastCrashAt,
      };
    }
    if (!entry.activated) {
      return {
        status: "activating",
        lastError: entry.lastError,
        lastCrashAt: entry.lastCrashAt,
      };
    }
    return {
      status: "running",
      lastError: entry.lastError,
      lastCrashAt: entry.lastCrashAt,
    };
  }

  /**
   * Activate a plugin in a sandboxed Worker. Creates the Worker, sends
   * the init message, and waits for the 'activated' or 'activation-error'
   * response. Returns a promise that resolves on successful activation
   * or rejects on error.
   *
   * M-18: Idempotent — if the plugin is already activated (or still
   * activating) and has not crashed, this returns the existing
   * activation promise WITHOUT terminating/rebuilding the Worker, so
   * in-memory plugin state is preserved. To force a fresh Worker
   * (e.g. after a code/manifest change), use {@link reload}.
   */
  activate(pluginName: string, manifest: PluginManifest, pluginUrl: string): Promise<void> {
    // M-18: If already sandboxed, do NOT terminate+rebuild — that would
    // discard the plugin's in-memory state. Only a crashed Worker needs
    // tearing down before re-activation; a running/activating one is a
    // no-op so the second activate() is safe to call.
    const existing = this.sandboxes.get(pluginName);
    if (existing) {
      if (!existing.crashed || existing.restartScheduled) {
        return existing.activationPromise;
      }
      // Crashed Worker — clean up the dead entry before re-activating.
      this.terminate(pluginName);
    }

    const activation = this.createActivationDeferred();
    const worker = this.workerFactory(this.workerScriptUrl);

    const entry: SandboxEntry = {
      worker,
      manifest,
      pluginUrl,
      pendingCalls: new Map(),
      pendingRequests: new Map(),
      nextCallId: 0,
      activationPromise: activation.promise,
      activationResolve: activation.resolve,
      activationReject: activation.reject,
      crashed: false,
      lastError: null,
      lastCrashAt: null,
      activated: false,
      hasActivatedOnce: false,
      recovering: false,
      restartAttempts: 0,
      restartScheduled: false,
    };

    this.sandboxes.set(pluginName, entry);

    this.startWorker(pluginName, entry, worker);
    return entry.activationPromise;
  }

  /**
   * M-18: Force-rebuild an already-activated plugin's Worker. Terminates
   * the existing sandbox (losing in-memory state) and re-activates from
   * scratch. This is the explicit "reload" path; {@link activate} alone
   * is idempotent and will not rebuild a running Worker.
   */
  reload(pluginName: string, manifest: PluginManifest, pluginUrl: string): Promise<void> {
    // Tear down the existing Worker first so activate() doesn't short-circuit
    // on the idempotency guard.
    if (this.sandboxes.has(pluginName)) {
      this.terminate(pluginName);
    }
    return this.activate(pluginName, manifest, pluginUrl);
  }

  /**
   * Call a method on the sandboxed plugin (e.g. execute a command
   * handler). Returns a promise that resolves with the method's
   * return value. This is used when the main thread or another plugin
   * needs to invoke functionality inside the sandboxed plugin.
   *
   * N-31: Implemented. Sends an `rpc-call` message to the Worker. The
   * Worker's bootstrap script looks up the command handler in its
   * local `commandHandlers` map and returns the result via `rpc-result`.
   */
  callMethod(pluginName: string, method: string, args: unknown[]): Promise<unknown> {
    const entry = this.sandboxes.get(pluginName);
    if (!entry) {
      return Promise.reject(new Error(`Plugin "${pluginName}" is not sandboxed`));
    }
    // N-40: Fail fast if the worker has crashed. Without this, the
    // postMessage would silently no-op and the promise would hang.
    if (entry.crashed) {
      return Promise.reject(
        new Error(
          `Plugin "${pluginName}" Worker has crashed: ${entry.lastError ?? "unknown error"}`,
        ),
      );
    }
    if (!entry.activated) {
      return entry.activationPromise.then(() => this.callMethod(pluginName, method, args));
    }

    const id = entry.nextCallId++;
    const msg: HostToWorkerMessage = { type: "rpc-call", id, method, args };

    return new Promise((resolve, reject) => {
      entry.pendingCalls.set(id, { resolve, reject });
      try {
        entry.worker.postMessage(msg);
      } catch (error) {
        entry.pendingCalls.delete(id);
        reject(error instanceof Error ? error : new Error(String(error)));
      }
    });
  }

  /** Terminate a plugin's Worker and clean up. */
  terminate(pluginName: string): void {
    const entry = this.sandboxes.get(pluginName);
    if (!entry) return;

    // Reject any pending calls.
    for (const [, { reject }] of entry.pendingCalls) {
      reject(new Error("Worker terminated"));
    }
    entry.pendingCalls.clear();
    entry.pendingRequests.clear();
    entry.restartScheduled = false;

    const rejectActivation = entry.activationReject;
    entry.activationResolve = undefined;
    entry.activationReject = undefined;
    rejectActivation?.(new Error("Worker terminated during activation"));

    this.stopWorkerGracefully(entry.worker);

    // Mark the entry inert before removal so queued callbacks cannot revive it.
    entry.crashed = true;
    entry.activated = false;
    entry.recovering = false;
    entry.lastError = "Worker terminated";
    this.notifyHealth(pluginName, {
      status: "terminated",
      lastError: null,
      lastCrashAt: null,
    });

    this.sandboxes.delete(pluginName);
  }

  /** Terminate all sandboxed plugins. */
  terminateAll(): void {
    for (const name of Array.from(this.sandboxes.keys())) {
      this.terminate(name);
    }
  }

  /** Full state reset used by frontend HMR teardown. */
  reset(): void {
    this.terminateAll();
    for (const shutdown of Array.from(this.workerShutdowns.values())) {
      shutdown.finish();
    }
    this.healthListeners.clear();
  }

  /** Check if a plugin is currently sandboxed. */
  has(pluginName: string): boolean {
    return this.sandboxes.has(pluginName);
  }

  /** N-40 / Proposal J2: Get health snapshot for a sandboxed plugin. */
  getHealth(pluginName: string): PluginHealth {
    const entry = this.sandboxes.get(pluginName);
    if (!entry) {
      return { status: "terminated", lastError: null, lastCrashAt: null };
    }
    return this.entryHealth(entry);
  }

  /** N-40: Subscribe to health changes. Returns an unsubscribe function. */
  onHealthChange(listener: HealthListener): () => void {
    this.healthListeners.add(listener);
    return () => {
      this.healthListeners.delete(listener);
    };
  }

  // -------------------------------------------------------------------------
  // Internal: message dispatch
  // -------------------------------------------------------------------------

  private handleWorkerMessage(
    pluginName: string,
    sourceEntry: SandboxEntry,
    sourceWorker: WorkerLike,
    msg: WorkerToHostMessage,
  ): void {
    const entry = this.sandboxes.get(pluginName);
    if (!entry || entry !== sourceEntry || entry.worker !== sourceWorker) return;
    if (entry.crashed) return;

    switch (msg.type) {
      case "activated":
        // N-40: Clear any stale error from a previous failed attempt.
        entry.lastError = null;
        entry.crashed = false;
        entry.activated = true;
        entry.hasActivatedOnce = true;
        entry.recovering = false;
        entry.restartScheduled = false;
        {
          const resolveActivation = entry.activationResolve;
          entry.activationResolve = undefined;
          entry.activationReject = undefined;
          resolveActivation?.();
        }
        this.notifyHealthChange(pluginName, entry);
        break;

      case "activation-error":
        // N-40: Record the activation error on the entry.
        {
          const wasRecovering = entry.recovering;
          entry.lastError = msg.error;
          entry.lastCrashAt = Date.now();
          entry.crashed = true;
          entry.activated = false;
          entry.restartScheduled = false;
          this.stopWorkerGracefully(sourceWorker);
          this.notifyHealthChange(pluginName, entry);

          if (wasRecovering) {
            this.scheduleWorkerRestart(pluginName, entry, msg.error);
            break;
          }

          entry.recovering = false;
          const rejectActivation = entry.activationReject;
          entry.activationResolve = undefined;
          entry.activationReject = undefined;
          rejectActivation?.(new Error(msg.error));
        }
        break;

      case "deactivated":
      case "terminated":
        // Shutdown acknowledgements are consumed by stopWorkerGracefully().
        break;

      case "rpc-request":
        this.handleRpcRequest(
          pluginName,
          entry,
          sourceWorker,
          msg.id,
          msg.method as RpcMethod,
          msg.args,
        );
        break;

      case "rpc-result": {
        // N-31: Result of a call we made INTO the worker (e.g. command
        // execution). Resolve or reject the pending call.
        const pending = entry.pendingCalls.get(msg.id);
        if (pending) {
          entry.pendingCalls.delete(msg.id);
          if (msg.error) {
            pending.reject(new Error(msg.error));
          } else {
            pending.resolve(msg.result);
          }
        }
        break;
      }

      case "log":
        // Forward plugin console output to the host's console for debugging.
        console[msg.level](`[plugin:${pluginName}] ${msg.message}`);
        break;
    }
  }

  /**
   * Handle an RPC request from a sandboxed plugin. Validates permissions
   * and dispatches to the real service via the RpcHandler.
   */
  private async handleRpcRequest(
    pluginName: string,
    entry: SandboxEntry,
    sourceWorker: WorkerLike,
    requestId: number,
    method: RpcMethod,
    args: unknown[],
  ): Promise<void> {
    try {
      // Permission check: verify the plugin declared the required permission.
      const requiredPerm = METHOD_PERMISSIONS[method];
      if (requiredPerm) {
        const declared = new Set(entry.manifest.permissions ?? []);
        if (!declared.has(requiredPerm)) {
          throw new Error(
            `Plugin "${pluginName}" cannot call ${method}: requires permission "${requiredPerm}" not declared in manifest`,
          );
        }
      }

      // Command registration is always permission-independent, but it is not
      // namespace-independent. This check intentionally runs even when the
      // manifest has no permissions so a plugin cannot shadow built-in or
      // another extension's commands.
      if (method === "commands.register") {
        assertExtensionCommandId(pluginName, args[0]);
      }

      // Dispatch to the real service.
      const result = await this.rpcHandler(pluginName, entry.manifest, method, args);

      // Send success response.
      const response: HostToWorkerMessage = {
        type: "rpc-response",
        id: requestId,
        result,
      };
      if (
        this.sandboxes.get(pluginName) === entry
        && entry.worker === sourceWorker
        && !entry.crashed
      ) {
        sourceWorker.postMessage(response);
      }
    } catch (e: unknown) {
      const errorMsg = e instanceof Error ? e.message : String(e);
      const response: HostToWorkerMessage = {
        type: "rpc-response",
        id: requestId,
        error: errorMsg,
      };
      if (
        this.sandboxes.get(pluginName) === entry
        && entry.worker === sourceWorker
        && !entry.crashed
      ) {
        sourceWorker.postMessage(response);
      }
    }
  }

  private createActivationDeferred(): {
    promise: Promise<void>;
    resolve: () => void;
    reject: (error: Error) => void;
  } {
    let resolve!: () => void;
    let reject!: (error: Error) => void;
    const promise = new Promise<void>((resolvePromise, rejectPromise) => {
      resolve = resolvePromise;
      reject = rejectPromise;
    });
    return { promise, resolve, reject };
  }

  private startWorker(
    pluginName: string,
    entry: SandboxEntry,
    worker: WorkerLike,
  ): void {
    entry.worker = worker;
    entry.crashed = false;
    entry.activated = false;

    worker.onmessage = (event: { data: unknown }) => {
      this.handleWorkerMessage(
        pluginName,
        entry,
        worker,
        event.data as WorkerToHostMessage,
      );
    };
    worker.onerror = (error: unknown) => {
      this.handleWorkerFailure(pluginName, entry, worker, "error", error);
    };
    worker.onmessageerror = (error: unknown) => {
      this.handleWorkerFailure(pluginName, entry, worker, "message error", error);
    };

    try {
      worker.postMessage({
        type: "init",
        pluginUrl: entry.pluginUrl,
        manifest: entry.manifest,
      } satisfies HostToWorkerMessage);
    } catch (error) {
      this.handleWorkerFailure(
        pluginName,
        entry,
        worker,
        "initialization error",
        error,
      );
    }
  }

  private handleWorkerFailure(
    pluginName: string,
    entry: SandboxEntry,
    worker: WorkerLike,
    kind: string,
    error: unknown,
  ): void {
    if (this.sandboxes.get(pluginName) !== entry || entry.worker !== worker) return;

    const message = error instanceof Error ? error.message : String(error);
    const canRecover = entry.hasActivatedOnce;
    entry.lastError = message;
    entry.lastCrashAt = Date.now();
    entry.crashed = true;
    entry.activated = false;

    for (const { reject } of entry.pendingCalls.values()) {
      reject(new Error(`Worker crashed: ${message}`));
    }
    entry.pendingCalls.clear();
    entry.pendingRequests.clear();

    this.stopWorkerGracefully(worker);
    this.notifyHealthChange(pluginName, entry);

    if (!canRecover) {
      const rejectActivation = entry.activationReject;
      entry.activationResolve = undefined;
      entry.activationReject = undefined;
      rejectActivation?.(new Error(`Worker ${kind}: ${message}`));
      return;
    }

    this.ensureRecoveryPromise(entry);
    this.scheduleWorkerRestart(pluginName, entry, message);
  }

  private ensureRecoveryPromise(entry: SandboxEntry): void {
    if (entry.recovering) return;
    const activation = this.createActivationDeferred();
    entry.activationPromise = activation.promise;
    entry.activationResolve = activation.resolve;
    entry.activationReject = activation.reject;
    entry.recovering = true;
    void entry.activationPromise.catch(() => undefined);
  }

  private scheduleWorkerRestart(
    pluginName: string,
    entry: SandboxEntry,
    message: string,
  ): void {
    if (entry.restartAttempts >= MAX_WORKER_RESTARTS) {
      entry.restartScheduled = false;
      const rejectActivation = entry.activationReject;
      entry.activationResolve = undefined;
      entry.activationReject = undefined;
      entry.recovering = false;
      rejectActivation?.(
        new Error(
          `Worker crashed after ${MAX_WORKER_RESTARTS} restart attempts: ${message}`,
        ),
      );
      return;
    }

    entry.restartScheduled = true;
    queueMicrotask(() => {
      if (
        this.sandboxes.get(pluginName) !== entry ||
        !entry.restartScheduled ||
        !entry.crashed
      ) {
        return;
      }
      entry.restartScheduled = false;
      entry.restartAttempts += 1;
      let worker: WorkerLike;
      try {
        worker = this.workerFactory(this.workerScriptUrl);
      } catch (error) {
        const workerError = error instanceof Error ? error.message : String(error);
        entry.lastError = workerError;
        entry.lastCrashAt = Date.now();
        entry.crashed = true;
        this.notifyHealthChange(pluginName, entry);
        this.scheduleWorkerRestart(pluginName, entry, workerError);
        return;
      }
      this.startWorker(pluginName, entry, worker);
      this.notifyHealthChange(pluginName, entry);
    });
  }

  private stopWorkerGracefully(worker: WorkerLike): void {
    if (this.workerShutdowns.has(worker)) return;

    const finish = () => {
      const current = this.workerShutdowns.get(worker);
      if (!current || current.finish !== finish) return;
      clearTimeout(current.timeout);
      this.workerShutdowns.delete(worker);
      worker.onmessage = null;
      worker.onerror = null;
      worker.onmessageerror = null;
      try {
        worker.terminate();
      } catch {
        // WorkerLike implementations may throw after an earlier native crash.
      }
    };

    const shutdownState: WorkerShutdownState = {
      finish,
      timeout: setTimeout(finish, WORKER_TERMINATION_GRACE_MS),
    };
    this.workerShutdowns.set(worker, shutdownState);
    worker.onmessage = (event: { data: unknown }) => {
      const data = event.data;
      if (!data || typeof data !== "object" || !("type" in data)) return;
      const type = (data as { type?: unknown }).type;
      if (type === "terminated" || type === "deactivated") finish();
    };
    worker.onerror = null;
    worker.onmessageerror = null;

    try {
      worker.postMessage({ type: "terminate" } satisfies HostToWorkerMessage);
    } catch {
      finish();
    }
  }
}

/** Terminate every tracked plugin sandbox and clear HMR-sensitive listeners. */
export function resetPluginSandboxState(): void {
  for (const hostRef of pluginSandboxModuleState.hosts) {
    const host = hostRef.deref();
    if (host) {
      host.reset();
    } else {
      pluginSandboxModuleState.hosts.delete(hostRef);
    }
  }
}

import.meta.hot?.dispose(resetPluginSandboxState);

// ---------------------------------------------------------------------------
// Permission validation helper (exported for testing)
// ---------------------------------------------------------------------------

/**
 * Check if a plugin's manifest declares the permission required for a
 * given RPC method. Returns true if no permission is required or if the
 * permission is declared; false otherwise.
 */
export function hasPermissionForMethod(
  manifest: PluginManifest,
  method: RpcMethod,
): boolean {
  const required = METHOD_PERMISSIONS[method];
  if (!required) return true;
  const declared = manifest.permissions ?? [];
  return declared.includes(required);
}
