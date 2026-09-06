/**
 * Plugin registry and koyoriIde.* API surface (Plan 49).
 *
 * This module is the frontend counterpart to the Go PluginService. It
 * loads plugin manifests discovered by the backend, dynamically imports
 * each plugin's main.js entry point, and invokes its `activate(context)`
 * export with a `koyoriIde` API object.
 *
 * Permission gating: privileged koyoriIde.* API calls (fs.read, fs.write,
 * shell.exec, etc.) check the calling plugin's declared permissions
 * before dispatching. A plugin that calls an API it did not declare in
 * its manifest gets a thrown error. "commands.register" and
 * "views.register" are always allowed.
 *
 * v1 runs plugins in the main thread via dynamic import(). A Web Worker
 * sandbox is a future enhancement (see prompt-2.md section 5.1). The
 * permission-gated API is the primary safety mechanism for v1.
 */
// Koyori IDE 模块 · Plugin Registry。
// 喵，这是 Koyori IDE 的 Plugin Registry 模块（前端实现）~

import { ref } from "vue";
import type {
  PluginInfo,
  PluginManifest,
  PluginPermission,
} from "@/types";
import { errorMessage } from "@/lib/errors";
import {
  assertExtensionCommandId,
  PluginSandboxHost,
  type SandboxHost,
  type RpcHandler,
  type RpcMethod,
} from "@/lib/pluginSandbox";

// ---------------------------------------------------------------------------
// Registry state
// ---------------------------------------------------------------------------

/**
 * RegisteredCommand is a command contributed by a plugin. The handler
 * is set when the plugin calls `koyoriIde.commands.register(id, handler)`.
 */
export interface RegisteredCommand {
  id: string;
  title: string;
  category?: string;
  keybinding?: string;
  pluginName: string;
  /**
   * Whether other plugins may invoke this command (Proposal E). When
   * false, only the owning plugin may execute it. Mirrors the `public`
   * flag from the manifest's command contribution.
   */
  public: boolean;
  handler: (...args: unknown[]) => unknown | Promise<unknown>;
}

/**
 * RegisteredView is a Vue component contributed by a plugin. The
 * component is set when the plugin calls `koyoriIde.views.register(id,
 * component)`. The host mounts the component in the declared location.
 */
export interface RegisteredView {
  id: string;
  title: string;
  location: "sidebar" | "panel" | "statusbar";
  pluginName: string;
  // The plugin supplies a component factory. We use a generic type to
  // avoid coupling the registry to Vue's component type at the type
  // level (plugins load via dynamic import and may not have full Vue
  // types available). The host component renders this via <component>.
  component: unknown;
}

/**
 * Activation state of a loaded plugin. Tracks whether activate() was
 * called, whether it threw, and the error message (if any). Used by
 * the PluginsView to show status.
 */
export interface PluginActivationState {
  name: string;
  status: "loaded" | "activating" | "activated" | "error" | "disabled";
  error?: string;
}

interface RegistryEntry {
  info: PluginInfo;
  activation: PluginActivationState;
}

/**
 * PluginModule is the shape of a plugin's main.js entry point. The
 * `activate` function is required; `deactivate` is optional and called
 * by deactivatePlugin().
 */
export interface PluginModule {
  activate: (ctx: NknkAPI) => unknown | Promise<unknown>;
  deactivate?: () => unknown | Promise<unknown>;
}

const registry = new Map<string, RegistryEntry>();
const commands = new Map<string, RegisteredCommand>();
const views = new Map<string, RegisteredView>();

// N-57 (Proposal Q): Reactive version counters bumped on every mutation
// of the commands/views Maps. listPluginCommands() / listPluginViews()
// read these refs so Vue computeds that call those functions re-evaluate
// when the registry changes. This replaces the 2-second polling that
// LayoutLeafView previously used to detect plugin view registration.
const commandsVersion = ref(0);
const viewsVersion = ref(0);

// Per-plugin activation event index, populated from the manifest. Used
// by isActivationEvent to decide whether to activate on a given trigger.
const activationEvents = new Map<string, Set<string>>();

// Module cache for test-injected plugin modules. When set, activatePlugin
// and deactivatePlugin use the cached module instead of dynamic import.
// activatePluginWithModule sets this; tests can also set it directly via
// __setPluginModule to pre-populate before calling activateOnStartup /
// activateOnCommand / deactivatePlugin.
const moduleCache = new Map<string, PluginModule>();
const inFlightActivations = new Set<Promise<void>>();
let pluginLifecycleGeneration = 0;
let pluginRegistryDisposing = false;
let pluginTeardownPromise: Promise<void> | null = null;
const PLUGIN_SANDBOX_ACTIVATION_TIMEOUT_MS = 15_000;
const PLUGIN_TEARDOWN_TIMEOUT_MS = 1_000;

/**
 * Resolve a plugin-supplied workspace path without ever weakening the
 * FileService sandbox. Plugin file APIs accept relative paths only; the
 * current workspace root is authoritative and an absent root fails closed.
 */
function resolvePluginWorkspacePath(
  pluginName: string,
  workspaceRoot: string | null | undefined,
  value: unknown,
): string {
  if (typeof workspaceRoot !== "string" || workspaceRoot.trim().length === 0) {
    throw new Error(
      `Plugin "${pluginName}" cannot access files: no workspace is open`,
    );
  }
  if (
    typeof value !== "string" ||
    value.trim().length === 0 ||
    value.includes("\0")
  ) {
    throw new Error(
      `Plugin "${pluginName}" workspace path must be a non-empty relative path`,
    );
  }

  const unified = value.replace(/\\/g, "/");
  if (unified.startsWith("/") || /^[A-Za-z]:/.test(unified)) {
    throw new Error(
      `Plugin "${pluginName}" workspace path must be relative: ${value}`,
    );
  }

  const segments: string[] = [];
  for (const segment of unified.split("/")) {
    if (segment === "" || segment === ".") continue;
    if (segment === "..") {
      if (segments.length === 0) {
        throw new Error(
          `Plugin "${pluginName}" workspace path escapes the workspace: ${value}`,
        );
      }
      segments.pop();
      continue;
    }
    segments.push(segment);
  }
  if (segments.length === 0) {
    throw new Error(
      `Plugin "${pluginName}" workspace path must identify a file or directory`,
    );
  }

  const unifiedRoot = workspaceRoot.replace(/\\/g, "/");
  const base = unifiedRoot === "/" ? "" : unifiedRoot.replace(/\/+$/, "");
  return `${base}/${segments.join("/")}`;
}

async function waitForSandboxActivation(
  pluginName: string,
  activation: Promise<void>,
): Promise<void> {
  let timer: ReturnType<typeof setTimeout> | null = null;
  const timedOut = new Promise<never>((_resolve, reject) => {
    timer = setTimeout(() => {
      reject(
        new Error(
          `Plugin "${pluginName}" sandbox activation timed out after ${PLUGIN_SANDBOX_ACTIVATION_TIMEOUT_MS}ms`,
        ),
      );
    }, PLUGIN_SANDBOX_ACTIVATION_TIMEOUT_MS);
  });
  try {
    await Promise.race([activation, timedOut]);
  } finally {
    if (timer) clearTimeout(timer);
  }
}

async function waitForPluginWork(
  promises: Iterable<Promise<unknown>>,
): Promise<boolean> {
  const pending = [...promises];
  if (pending.length === 0) return true;
  let timer: ReturnType<typeof setTimeout> | null = null;
  const completed = Promise.allSettled(pending).then(() => true);
  const timedOut = new Promise<boolean>((resolve) => {
    timer = setTimeout(() => resolve(false), PLUGIN_TEARDOWN_TIMEOUT_MS);
  });
  const result = await Promise.race([completed, timedOut]);
  if (timer) clearTimeout(timer);
  return result;
}

async function deactivateStalePluginModule(module: PluginModule): Promise<void> {
  try {
    const result = module.deactivate?.();
    if (result !== undefined) {
      await waitForPluginWork([Promise.resolve(result)]);
    }
  } catch {
    // A stale plugin must not block registry replacement during cleanup.
  }
}

// Sandbox host for Web Worker-based plugin isolation (N-26). Production
// renderers are fail-closed: they always create/retain this host and can
// never reach the direct dynamic-import activation path. Tests can model
// production explicitly or inject a mock host outside production.
let sandboxHost: SandboxHost | null = null;

export interface SandboxModeOptions {
  /** Test-only production-mode override; a real production build always wins. */
  production?: boolean;
}

export function isProductionSandboxRequired(options?: SandboxModeOptions): boolean {
  return import.meta.env.PROD || options?.production === true;
}

function ensureSandboxHost(): SandboxHost {
  if (!sandboxHost) {
    sandboxHost = new PluginSandboxHost(createSandboxRpcHandler());
  }
  return sandboxHost;
}

/**
 * Enable or disable sandbox mode (N-26). Production builds always force
 * sandbox mode regardless of renderer settings. Development and tests may
 * explicitly disable it to exercise the legacy main-thread path.
 */
export function setSandboxMode(
  enabled: boolean,
  options?: SandboxModeOptions,
): void {
  const effectiveEnabled = enabled || isProductionSandboxRequired(options);
  if (effectiveEnabled && !sandboxHost) {
    ensureSandboxHost();
  } else if (!effectiveEnabled && sandboxHost) {
    sandboxHost.terminateAll();
    sandboxHost = null;
  }
}

/**
 * Set or clear the sandbox host directly. Production builds reject the
 * disabling effect of null and retain a default host. Useful for tests that
 * need to inject a mock host outside production.
 */
export function setSandboxHost(
  host: SandboxHost | null,
  options?: SandboxModeOptions,
): void {
  if (!host && isProductionSandboxRequired(options)) {
    ensureSandboxHost();
    return;
  }
  if (sandboxHost && sandboxHost !== host) {
    sandboxHost.terminateAll();
  }
  sandboxHost = host;
}

/** Check if sandbox mode is currently enabled. */
export function isSandboxEnabled(): boolean {
  return sandboxHost !== null;
}

/**
 * Create the default RPC handler that dispatches sandbox RPC requests
 * to real services. This handler is used when setSandboxMode(true) is
 * called without a custom host.
 *
 * The handler implements permission validation (delegated to the
 * PluginSandboxHost) and service dispatch:
 *   - workspace.readFile/writeFile → fileService (with path resolution)
 *   - commands.register → registers metadata in the commands map
 *   - commands.execute → executes if handler is on main thread
 *   - views.register → registers metadata (component is null)
 *   - getPermissions → returns manifest.permissions
 */
export function createSandboxRpcHandler(): RpcHandler {
  return async (
    pluginName: string,
    manifest: PluginManifest,
    method: RpcMethod,
    args: unknown[],
  ): Promise<unknown> => {
    switch (method) {
      case "workspace.readFile": {
        const { appState } = await import("@/stores/app");
        const fullPath = resolvePluginWorkspacePath(
          pluginName,
          appState.currentProject,
          args[0],
        );
        const { fileService } = await import("@/api/services");
        return fileService.readFile(fullPath);
      }
      case "workspace.writeFile": {
        const { appState } = await import("@/stores/app");
        const fullPath = resolvePluginWorkspacePath(
          pluginName,
          appState.currentProject,
          args[0],
        );
        const content = args[1];
        if (typeof content !== "string") {
          throw new Error(
            `Plugin "${pluginName}" workspace.writeFile content must be a string`,
          );
        }
        const { fileService } = await import("@/api/services");
        await fileService.writeFile(fullPath, content);
        return undefined;
      }
      case "commands.register": {
        // The command handler lives in the Worker and can't be serialized.
        // Register metadata only; cross-sandbox execution is deferred.
        // The handler is a no-op that returns undefined.
        const id = args[0];
        // This handler is also used directly by sandboxed iframes, which do
        // not pass through PluginSandboxHost.handleRpcRequest. Keep the owner
        // namespace check at this authoritative registry boundary as well.
        assertExtensionCommandId(pluginName, id);
        const contributed = manifest.contributes?.commands?.find(
          (c) => c.id === id,
        );
        if (commands.has(id)) {
          const existing = commands.get(id);
          if (existing && existing.pluginName !== pluginName) {
            throw new Error(
              `Command "${id}" is already registered by plugin "${existing.pluginName}"`,
            );
          }
        }
        commands.set(id, {
          id,
          title: contributed?.title ?? id,
          category: contributed?.category,
          keybinding: contributed?.keybinding,
          pluginName,
          public: contributed?.public === true,
          handler: () => undefined,
        });
        commandsVersion.value++; // N-57: trigger reactive update
        return undefined;
      }
      case "commands.execute": {
        const id = args[0] as string;
        const cmdArgs = args.slice(1);
        const cmd = commands.get(id);
        if (!cmd) {
          throw new Error(`Command "${id}" is not registered`);
        }
        if (cmd.pluginName !== pluginName && !cmd.public) {
          throw new Error(
            `Plugin "${pluginName}" cannot execute command "${id}": command is not public`,
          );
        }
        // N-31/N-32: If the command's owning plugin is sandboxed, route
        // execution through the sandbox's callMethod. The real handler
        // lives in the Worker and can't be invoked directly.
        if (sandboxHost && sandboxHost.has(cmd.pluginName)) {
          return sandboxHost.callMethod(cmd.pluginName, "executeCommand", [id, ...cmdArgs]);
        }
        // Main-thread path: invoke the stored handler directly.
        return cmd.handler(...cmdArgs);
      }
      case "views.register": {
        const id = args[0] as string;
        const options = (args[1] as { title?: string; location?: string }) ?? {};
        const contributed = manifest.contributes?.views?.find(
          (v) => v.id === id,
        );
        if (views.has(id)) {
          const existing = views.get(id);
          if (existing && existing.pluginName !== pluginName) {
            throw new Error(
              `View "${id}" is already registered by plugin "${existing.pluginName}"`,
            );
          }
        }
        views.set(id, {
          id,
          title: options.title ?? contributed?.title ?? id,
          location: (options.location as "sidebar" | "panel" | "statusbar") ??
            contributed?.location ?? "panel",
          pluginName,
          // Component can't cross Worker boundary; null signals the host
          // to render a placeholder or load the component separately.
          component: null,
        });
        viewsVersion.value++; // N-57: trigger reactive update
        return undefined;
      }
      case "getPermissions": {
        return manifest.permissions ?? [];
      }
      default:
        throw new Error(`Unknown RPC method: ${method}`);
    }
  };
}

// ---------------------------------------------------------------------------
// Public registry queries
// ---------------------------------------------------------------------------

/**
 * List all plugins known to the registry with their activation state.
 * The PluginsView uses this to render the installed-plugin list.
 */
export function listPluginStates(): PluginActivationState[] {
  return Array.from(registry.values()).map((e) => ({ ...e.activation }));
}

/**
 * Get a plugin's full info by name. Returns undefined if the plugin is
 * not in the registry.
 */
export function getPluginInfo(name: string): PluginInfo | undefined {
  return registry.get(name)?.info;
}

/**
 * List all commands registered by plugins. Used by the command palette
 * to merge plugin commands with built-in commands.
 *
 * N-57: Reads commandsVersion to establish a reactive dependency so
 * Vue computeds that call this function re-evaluate when commands are
 * registered or unregistered.
 */
export function listPluginCommands(): RegisteredCommand[] {
  void commandsVersion.value; // track for reactivity
  return Array.from(commands.values());
}

/**
 * Execute a plugin command by ID. Intended for the command palette (N-42):
 * the palette is the user, not a plugin, so the cross-plugin `public` gate
 * does NOT apply — any registered command can be invoked. This helper:
 * 1. Triggers lazy activation via `activateOnCommand(id)` so commands owned
 *    by not-yet-active plugins get their handler registered.
 * 2. Routes execution through the sandbox RPC when the owning plugin is
 *    sandboxed (the stored handler is a no-op in that case).
 * 3. Calls the handler directly for main-thread plugins.
 */
export async function executePluginCommand(
  id: string,
  ...args: unknown[]
): Promise<unknown> {
  await activateOnCommand(id);
  const cmd = commands.get(id);
  if (!cmd) {
    throw new Error(`Plugin command "${id}" is not registered`);
  }
  // N-31/N-32: If the command's owning plugin is sandboxed, the stored
  // handler is a no-op stub. Route through the sandbox's callMethod so the
  // real handler (inside the Worker) is invoked.
  if (sandboxHost && sandboxHost.has(cmd.pluginName)) {
    return sandboxHost.callMethod(cmd.pluginName, "executeCommand", [id, ...args]);
  }
  return cmd.handler(...args);
}

/**
 * List all views registered by plugins. Used by the host to mount
 * contributed views in their declared dock locations.
 *
 * N-57: Reads viewsVersion to establish a reactive dependency so
 * Vue computeds that call this function re-evaluate when views are
 * registered or unregistered.
 */
export function listPluginViews(): RegisteredView[] {
  void viewsVersion.value; // track for reactivity
  return Array.from(views.values());
}

// ---------------------------------------------------------------------------
// Loader
// ---------------------------------------------------------------------------

/**
 * Sync the registry with the backend's plugin list. Plugins that are
 * no longer present are unloaded; new plugins are added in "loaded"
 * state. Already-activated plugins are preserved unless they were
 * removed or disabled.
 *
 * This does NOT activate plugins — call activatePending() to honor
 * activation events. Separating sync from activation lets the host
 * control activation order (e.g. wait for the editor to be ready).
 */
export function syncPlugins(plugins: PluginInfo[]): void {
  const seen = new Set<string>();
  for (const info of plugins) {
    seen.add(info.manifest.name);
    const existing = registry.get(info.manifest.name);
    if (existing) {
      existing.info = info;
      // If the plugin was disabled in the backend, reflect that.
      if (!info.enabled && existing.activation.status !== "disabled") {
        existing.activation.status = "disabled";
      } else if (info.enabled && existing.activation.status === "disabled") {
        existing.activation.status = "loaded";
      }
    } else {
      const status: PluginActivationState["status"] = info.enabled
        ? "loaded"
        : "disabled";
      registry.set(info.manifest.name, {
        info,
        activation: { name: info.manifest.name, status },
      });
      // Index activation events for later lookup.
      const events = new Set<string>();
      for (const ev of info.manifest.activationEvents ?? []) {
        events.add(ev);
      }
      activationEvents.set(info.manifest.name, events);
    }
  }
  // Remove plugins no longer present.
  for (const name of Array.from(registry.keys())) {
    if (!seen.has(name)) {
      unregisterPluginContributions(name);
      registry.delete(name);
      activationEvents.delete(name);
    }
  }
}

/**
 * Activate all plugins whose activation events include "onStartup" and
 * that have not yet been activated. Returns the list of plugin names
 * that were activated (or attempted). Errors are captured per-plugin
 * and surfaced via the activation state, not thrown.
 */
export async function activateOnStartup(
  shouldContinue: () => boolean = () => true,
): Promise<string[]> {
  const activated: string[] = [];
  for (const [name, entry] of registry.entries()) {
    if (!shouldContinue()) break;
    if (entry.activation.status === "disabled") continue;
    if (entry.activation.status === "activated") continue;
    // Skip plugins that previously errored — don't auto-retry without
    // an explicit user action (reload). Otherwise every activateOnStartup
    // call would re-attempt failed plugins.
    if (entry.activation.status === "error") continue;
    const events = activationEvents.get(name);
    if (!events || !events.has("onStartup")) continue;
    await activatePlugin(name, shouldContinue);
    if (!shouldContinue()) break;
    activated.push(name);
  }
  return activated;
}

/**
 * Activate a plugin by name. Idempotent: a no-op if the plugin is
 * already activated or disabled. Throws if the plugin is not in the
 * registry. The activation runs the plugin's main.js activate()
 * function with a permission-gated koyoriIde context.
 */
export function activatePlugin(
  name: string,
  shouldContinue: () => boolean = () => true,
): Promise<void> {
  const generation = pluginLifecycleGeneration;
  const activation = activatePluginImpl(
    name,
    () => !pluginRegistryDisposing
      && generation === pluginLifecycleGeneration
      && shouldContinue(),
    () => generation === pluginLifecycleGeneration,
  );
  inFlightActivations.add(activation);
  void activation.then(
    () => inFlightActivations.delete(activation),
    () => inFlightActivations.delete(activation),
  );
  return activation;
}

async function activatePluginImpl(
  name: string,
  shouldContinue: () => boolean,
  ownsRegistry: () => boolean,
): Promise<void> {
  const entry = registry.get(name);
  if (!entry) {
    throw new Error(`Plugin "${name}" is not registered`);
  }
  if (entry.activation.status === "activated") return;
  if (entry.activation.status === "disabled") return;
  if (entry.activation.status === "activating") return;
  const ownsEntry = (): boolean => registry.get(name) === entry;
  const canContinue = (): boolean => ownsRegistry() && ownsEntry() && shouldContinue();
  if (!canContinue()) return;

  entry.activation.status = "activating";
  entry.activation.error = undefined;
  void logPluginEvent("info", `Activating plugin "${name}"…`);

  // Fail closed even if activation is invoked before the normal bootstrap
  // stage. This compile-time production branch prevents the renderer from
  // ever reaching direct dynamic import in a production bundle.
  if (import.meta.env.PROD) ensureSandboxHost();

  // If a test or pre-loader injected a module, use it instead of the
  // dynamic import. This lets unit tests exercise activateOnStartup /
  // activateOnCommand / deactivatePlugin without a real URL handler.
  const cached = moduleCache.get(name);
  if (cached && !import.meta.env.PROD) {
    let activationStarted = false;
    try {
      if (!entry.info.mainExists) {
        throw new Error(
          `Plugin entry point "${entry.info.manifest.main}" not found at ${entry.info.path}`,
        );
      }
      const context = createPluginContext(entry.info, canContinue);
      activationStarted = true;
      await cached.activate(context);
      if (!canContinue()) {
        await deactivateStalePluginModule(cached);
        if (ownsEntry()) {
          unregisterPluginContributions(name);
          entry.activation.status = "loaded";
        }
        return;
      }
      entry.activation.status = "activated";
      void logPluginEvent("success", `Plugin "${name}" activated`);
    } catch (e: unknown) {
      const activationError = errorMessage(e);
      if (activationStarted) await deactivateStalePluginModule(cached);
      if (!ownsEntry()) return;
      unregisterPluginContributions(name);
      if (!canContinue()) {
        entry.activation.status = "loaded";
        return;
      }
      entry.activation.status = "error";
      entry.activation.error = activationError;
      void logPluginEvent("error", `Plugin "${name}" activation failed: ${entry.activation.error}`);
    }
    return;
  }

  // Sandbox path (N-26): when sandbox mode is enabled, activate the
  // plugin in a Web Worker instead of the main thread. The Worker has
  // no access to DOM/window/Wails bindings — all privileged calls go
  // through the permission-gated postMessage bridge.
  const activationSandboxHost = sandboxHost;
  if (activationSandboxHost) {
    let activationStarted = false;
    try {
      if (!entry.info.mainExists) {
        throw new Error(
          `Plugin entry point "${entry.info.manifest.main}" not found at ${entry.info.path}`,
        );
      }
      const { appState } = await import("@/stores/app");
      if (!canContinue()) {
        if (ownsEntry()) entry.activation.status = "loaded";
        return;
      }
      const url = pluginEntryUrl(entry.info, appState.currentProject ?? "");
      activationStarted = true;
      await waitForSandboxActivation(
        name,
        activationSandboxHost.activate(name, entry.info.manifest, url),
      );
      if (!canContinue()) {
        if (ownsEntry()) {
          activationSandboxHost.terminate(name);
          unregisterPluginContributions(name);
          entry.activation.status = "loaded";
        }
        return;
      }
      entry.activation.status = "activated";
      void logPluginEvent("success", `Plugin "${name}" activated (sandboxed)`);
    } catch (e: unknown) {
      const activationError = errorMessage(e);
      if (!ownsEntry()) return;
      if (activationStarted) activationSandboxHost.terminate(name);
      unregisterPluginContributions(name);
      if (!canContinue()) {
        entry.activation.status = "loaded";
        return;
      }
      entry.activation.status = "error";
      entry.activation.error = activationError;
      void logPluginEvent("error", `Plugin "${name}" sandbox activation failed: ${entry.activation.error}`);
    }
    return;
  }

  let loadedModule: PluginModule | undefined;
  let activationStarted = false;
  try {
    if (!entry.info.mainExists) {
      throw new Error(
        `Plugin entry point "${entry.info.manifest.main}" not found at ${entry.info.path}`,
      );
    }
    // Dynamic import. The plugin entry point is served by the backend's
    // asset middleware at /_plugins/<name>/<main>?projectRoot=<root>.
    // Plan 58 / N-21: Wails v3 has no custom scheme API, so we route
    // plugin assets under the existing asset handler via a path prefix.
    const { appState } = await import("@/stores/app");
    if (!canContinue()) {
      if (ownsEntry()) entry.activation.status = "loaded";
      return;
    }
    const url = pluginEntryUrl(entry.info, appState.currentProject ?? "");
    const module = await import(/* @vite-ignore */ url);
    if (typeof module.activate !== "function") {
      throw new Error(
        `Plugin "${name}" main module does not export an activate() function`,
      );
    }
    loadedModule = module as PluginModule;
    if (!canContinue()) {
      await deactivateStalePluginModule(loadedModule);
      if (ownsEntry()) entry.activation.status = "loaded";
      return;
    }
    const context = createPluginContext(entry.info, canContinue);
    activationStarted = true;
    await module.activate(context);
    if (!canContinue()) {
      await deactivateStalePluginModule(loadedModule);
      if (ownsEntry()) {
        unregisterPluginContributions(name);
        entry.activation.status = "loaded";
      }
      return;
    }
    entry.activation.status = "activated";
    void logPluginEvent("success", `Plugin "${name}" activated`);
  } catch (e: unknown) {
    const activationError = errorMessage(e);
    if (activationStarted && loadedModule) {
      await deactivateStalePluginModule(loadedModule);
    }
    if (!ownsEntry()) return;
    unregisterPluginContributions(name);
    if (!canContinue()) {
      entry.activation.status = "loaded";
      return;
    }
    entry.activation.status = "error";
    entry.activation.error = activationError;
    void logPluginEvent("error", `Plugin "${name}" activation failed: ${entry.activation.error}`);
  }
}

/**
 * Deactivate a plugin: call its `deactivate()` export if present, then
 * unregister all its contributions. Idempotent.
 */
export async function deactivatePlugin(name: string): Promise<void> {
  const generation = pluginLifecycleGeneration;
  const entry = registry.get(name);
  if (!entry) return;
  if (entry.activation.status !== "activated") return;
  const ownsEntry = (): boolean =>
    generation === pluginLifecycleGeneration && registry.get(name) === entry;

  // Sandbox path: terminate the Worker. The Worker's deactivate() (if
  // any) is called by the bootstrap script on receiving 'terminate'.
  const deactivationSandboxHost = sandboxHost;
  if (deactivationSandboxHost && deactivationSandboxHost.has(name)) {
    deactivationSandboxHost.terminate(name);
    if (!ownsEntry()) return;
    unregisterPluginContributions(name);
    // L-11: clear the cached module so a subsequent activation re-imports
    // a fresh module instead of reusing the stale cached one.
    moduleCache.delete(name);
    entry.activation.status = "loaded";
    void logPluginEvent("info", `Plugin "${name}" unloaded (sandboxed)`);
    return;
  }

  // A crashed or externally reset Worker may no longer be tracked by the
  // host. Production teardown must still stay fail-closed and must never
  // import the plugin entry module into the renderer just to deactivate it.
  if (import.meta.env.PROD) {
    if (!ownsEntry()) return;
    unregisterPluginContributions(name);
    moduleCache.delete(name);
    entry.activation.status = "loaded";
    void logPluginEvent("info", `Plugin "${name}" unloaded (sandbox unavailable)`);
    return;
  }

  // Prefer the cached module (test path) over dynamic import.
  const cached = moduleCache.get(name);
  if (cached) {
    try {
      if (typeof cached.deactivate === "function") {
        await cached.deactivate();
      }
    } catch {
      // Plugin's deactivate threw; continue with cleanup.
    }
  } else {
    try {
      const { appState } = await import("@/stores/app");
      const url = pluginEntryUrl(entry.info, appState.currentProject ?? "");
      const module = await import(/* @vite-ignore */ url);
      if (typeof module.deactivate === "function") {
        await module.deactivate();
      }
    } catch {
      // Module may have failed to load originally; ignore.
    }
  }
  if (!ownsEntry()) return;
  unregisterPluginContributions(name);
  // L-11: clear the cached module so a subsequent activation re-imports
  // a fresh module instead of reusing the stale cached one.
  if (moduleCache.get(name) === cached) moduleCache.delete(name);
  entry.activation.status = "loaded";
  void logPluginEvent("info", `Plugin "${name}" unloaded`);
}

/**
 * Proposal G (prompt-4.md): Log a plugin lifecycle event to the Output
 * panel's "Plugins" channel. Best-effort — if the output store is
 * unavailable (e.g. in unit tests), the log is silently dropped.
 *
 * Lazy import avoids pulling the output store into the pluginRegistry
 * test graph, which would re-introduce the Monaco/jsdom chain.
 */
async function logPluginEvent(
  severity: "info" | "warn" | "error" | "success",
  message: string,
): Promise<void> {
  try {
    const { pushOutput } = await import("@/stores/output");
    pushOutput("Plugins", severity, message);
  } catch {
    // Output store unavailable (e.g. test environment) — drop silently.
  }
}

/**
 * Trigger activation for plugins that declare "onCommand:<id>" as an
 * activation event. Called by the command palette before invoking a
 * plugin command that may not yet be active.
 */
export async function activateOnCommand(commandId: string): Promise<void> {
  const event = `onCommand:${commandId}`;
  for (const [name, entry] of registry.entries()) {
    if (entry.activation.status === "activated") continue;
    if (entry.activation.status === "disabled") continue;
    const events = activationEvents.get(name);
    if (!events || !events.has(event)) continue;
    await activatePlugin(name);
  }
}

/**
 * Disable a plugin: deactivate it (if active) and mark it disabled in
 * the local registry. Does NOT persist to the backend — the host
 * calls pluginService.setPluginEnabled() for persistence.
 */
export async function disablePlugin(name: string): Promise<void> {
  const generation = pluginLifecycleGeneration;
  const entry = registry.get(name);
  await deactivatePlugin(name);
  if (entry && generation === pluginLifecycleGeneration && registry.get(name) === entry) {
    entry.activation.status = "disabled";
  }
}

/** Deactivate every plugin and wait for in-flight activations before reset. */
export function deactivateAllPlugins(): Promise<void> {
  if (pluginTeardownPromise) return pluginTeardownPromise;
  const teardown = (async () => {
    pluginRegistryDisposing = true;
    pluginLifecycleGeneration += 1;
    try {
      const sandboxedNames = sandboxHost
        ? [...registry.keys()].filter((name) => sandboxHost?.has(name))
        : [];
      sandboxHost?.terminateAll();
      for (const name of sandboxedNames) {
        unregisterPluginContributions(name);
        moduleCache.delete(name);
        const entry = registry.get(name);
        if (entry) entry.activation.status = "loaded";
      }
      if (!await waitForPluginWork(inFlightActivations)) {
        console.warn("Plugin activation teardown timed out; clearing stale registry state");
      }
      inFlightActivations.clear();
      const activeNames = [...registry.entries()]
        .filter(([, entry]) => entry.activation.status === "activated")
        .map(([name]) => name);
      if (!await waitForPluginWork(activeNames.map((name) => deactivatePlugin(name)))) {
        console.warn("Plugin deactivation timed out; clearing stale registry state");
      }
      clearRegistry();
    } finally {
      pluginRegistryDisposing = false;
    }
  })();
  pluginTeardownPromise = teardown;
  void teardown.then(
    () => {
      if (pluginTeardownPromise === teardown) pluginTeardownPromise = null;
    },
    () => {
      if (pluginTeardownPromise === teardown) pluginTeardownPromise = null;
    },
  );
  return teardown;
}

/**
 * Enable a previously-disabled plugin: mark it loaded and attempt
 * activation if it has an onStartup event.
 */
export async function enablePlugin(name: string): Promise<void> {
  const entry = registry.get(name);
  if (!entry) return;
  if (entry.activation.status !== "disabled") return;
  entry.activation.status = "loaded";
  const events = activationEvents.get(name);
  if (events && events.has("onStartup")) {
    await activatePlugin(name);
  }
}

/**
 * Clear the entire registry. Used in tests and on full project switch.
 */
export function clearRegistry(): void {
  pluginLifecycleGeneration += 1;
  inFlightActivations.clear();
  // Terminate any sandboxed plugins before clearing the registry.
  if (sandboxHost) {
    sandboxHost.terminateAll();
  }
  registry.clear();
  commands.clear();
  views.clear();
  activationEvents.clear();
  moduleCache.clear();
  // N-57: Bump version counters so reactive consumers update.
  commandsVersion.value++;
  viewsVersion.value++;
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

export function unregisterPluginContributions(pluginName: string): void {
  let cmdsChanged = false;
  let viewsChanged = false;
  for (const [id, cmd] of Array.from(commands.entries())) {
    if (cmd.pluginName === pluginName) {
      commands.delete(id);
      cmdsChanged = true;
    }
  }
  for (const [id, view] of Array.from(views.entries())) {
    if (view.pluginName === pluginName) {
      views.delete(id);
      viewsChanged = true;
    }
  }
  // N-57: Bump version counters only if something actually changed.
  if (cmdsChanged) commandsVersion.value++;
  if (viewsChanged) viewsVersion.value++;
}

/**
 * Build the URL for a plugin's main entry point. The backend serves
 * plugin files via an asset middleware that intercepts the
 * /_plugins/<name>/<path> path prefix on the existing Wails asset
 * handler's scheme (http://wails.localhost on Windows,
 * wails://localhost on macOS/Linux).
 *
 * Plan 58 / N-21: Wails v3 alpha2.111 has no public API for registering
 * custom URL schemes like koyoriIde-plugin://, so we route plugin assets
 * under the existing asset handler via a path prefix instead.
 *
 * Format: `/_plugins/<plugin-name>/<main-relative-path>?projectRoot=<root>`
 *
 * The projectRoot query parameter lets the backend resolve project-scoped
 * plugins. For non-Wails (test) environments, the import will fail —
 * tests should mock the import or use activatePluginWithModule().
 */
function pluginEntryUrl(info: PluginInfo, projectRoot: string): string {
  const encoded = encodeURIComponent(projectRoot ?? "");
  return `/_plugins/${info.manifest.name}/${info.manifest.main}?projectRoot=${encoded}`;
}

/**
 * Test-only: inject a plugin module into the cache so subsequent calls
 * to activatePlugin / deactivatePlugin / activateOnStartup /
 * activateOnCommand use the cached module instead of dynamic import.
 * Pass undefined to clear the entry.
 */
export function __setPluginModule(name: string, module: PluginModule | undefined): void {
  if (module === undefined) {
    moduleCache.delete(name);
  } else {
    moduleCache.set(name, module);
  }
}

/**
 * Test-only entry point: activate a plugin with a pre-loaded module,
 * bypassing the dynamic import. Used by unit tests to inject a fake
 * plugin module without setting up a URL handler. Also caches the
 * module so subsequent deactivatePlugin() calls can find it.
 */
export async function activatePluginWithModule(
  name: string,
  module: PluginModule,
): Promise<void> {
  const entry = registry.get(name);
  if (!entry) {
    throw new Error(`Plugin "${name}" is not registered`);
  }
  if (entry.activation.status === "activated") return;
  if (entry.activation.status === "disabled") return;
  moduleCache.set(name, module);
  await activatePlugin(name);
}

// ---------------------------------------------------------------------------
// koyoriIde.* API surface
// ---------------------------------------------------------------------------

/**
 * The koyoriIde API object passed to a plugin's activate() function. Each
 * method that touches privileged resources checks the plugin's
 * declared permissions before dispatching. Methods that are always
 * allowed (commands.register, views.register) have no permission
 * requirement.
 */
export interface NknkAPI {
  /** Plugin manifest, as parsed from plugin.json. */
  manifest: PluginManifest;
  /** Commands namespace — register command palette handlers. */
  commands: {
    /**
     * Register a command handler. The command ID should match a
     * contribution in the manifest's contributes.commands. Always
     * allowed (no permission required).
     *
     * The command's `public` flag is read from the manifest
     * contribution: when true, other plugins may invoke it via
     * `execute` / `executeCommand` (Proposal E); when false/unset,
     * only this plugin may invoke it.
     */
    register(
      id: string,
      handler: (...args: unknown[]) => unknown | Promise<unknown>,
    ): void;
    /**
     * Alias for `register` (VS Code API name). Identical behavior.
     */
    registerCommand(
      id: string,
      handler: (...args: unknown[]) => unknown | Promise<unknown>,
    ): void;
    /**
     * Execute a registered command by ID. Returns the handler's
     * result. Triggers activation for plugins that declare
     * onCommand:<id> as an activation event.
     *
     * Cross-plugin invocation requires the target command to be
     * declared `public: true` in its manifest (Proposal E). A plugin
     * may always execute its own commands regardless of the public
     * flag.
     */
    execute(id: string, ...args: unknown[]): Promise<unknown>;
    /**
     * Alias for `execute` (VS Code API name). Identical behavior.
     */
    executeCommand(id: string, ...args: unknown[]): Promise<unknown>;
  };
  /** Views namespace — register Vue components for dock locations. */
  views: {
    /**
     * Register a view component. The view ID should match a
     * contribution in the manifest's contributes.views. Always
     * allowed (no permission required).
     */
    register(
      id: string,
      component: unknown,
      options?: { title?: string; location?: "sidebar" | "panel" | "statusbar" },
    ): void;
  };
  /** Workspace namespace — file system access (requires fs.* perms). */
  workspace: {
    /** Read a file from the workspace. Requires fs.read. */
    readFile(relPath: string): Promise<string>;
    /** Write a file in the workspace. Requires fs.write. */
    writeFile(relPath: string, content: string): Promise<void>;
  };
  /**
   * Returns the list of permissions the plugin declared in its
   * manifest. Plugins can use this to gracefully degrade when a
   * permission is missing.
   */
  getPermissions(): PluginPermission[];
}

/**
 * Create a permission-gated koyoriIde API context for a plugin. The
 * manifest's declared permissions are captured in the closure and
 * checked before each privileged call.
 */
function createPluginContext(
  info: PluginInfo,
  shouldContinue: () => boolean,
): NknkAPI {
  const declared = new Set(info.manifest.permissions ?? []);
  const pluginName = info.manifest.name;

  const requireActiveContext = (): void => {
    if (!shouldContinue()) {
      throw new Error(`Plugin "${pluginName}" activation context is no longer active`);
    }
  };

  const requirePermission = (perm: PluginPermission, action: string): void => {
    if (!declared.has(perm)) {
      throw new Error(
        `Plugin "${pluginName}" cannot ${action}: requires permission "${perm}" not declared in manifest`,
      );
    }
  };

  // Register a command handler under the calling plugin's name. The
  // `public` flag is read from the manifest's command contribution so
  // the registry enforces cross-plugin invocation gating (Proposal E).
  const registerCommandImpl = (
    id: string,
    handler: (...args: unknown[]) => unknown | Promise<unknown>,
  ): void => {
    requireActiveContext();
    assertExtensionCommandId(pluginName, id);
    if (commands.has(id)) {
      // Allow re-registration by the same plugin (idempotent).
      const existing = commands.get(id);
      if (existing && existing.pluginName !== pluginName) {
        throw new Error(
          `Command "${id}" is already registered by plugin "${existing.pluginName}"`,
        );
      }
    }
    // Look up the contributed title/category/public from the manifest.
    const contributed = info.manifest.contributes?.commands?.find(
      (c) => c.id === id,
    );
    commands.set(id, {
      id,
      title: contributed?.title ?? id,
      category: contributed?.category,
      keybinding: contributed?.keybinding,
      pluginName,
      public: contributed?.public === true,
      handler,
    });
    commandsVersion.value++; // N-57: trigger reactive update
  };

  // Execute a command by ID. Cross-plugin invocation requires the
  // target command to be `public: true` (Proposal E); a plugin may
  // always execute its own commands.
  const executeCommandImpl = async (
    id: string,
    args: unknown[],
  ): Promise<unknown> => {
    requireActiveContext();
    await activateOnCommand(id);
    requireActiveContext();
    const cmd = commands.get(id);
    if (!cmd) {
      throw new Error(`Command "${id}" is not registered`);
    }
    if (cmd.pluginName !== pluginName && !cmd.public) {
      throw new Error(
        `Plugin "${pluginName}" cannot execute command "${id}": command is not public (declare "public: true" in ${cmd.pluginName}'s manifest to allow cross-plugin invocation)`,
      );
    }
    // N-31/N-32: If the command's owning plugin is sandboxed, route
    // execution through the sandbox's callMethod. The real handler
    // lives in the Worker and can't be invoked directly.
    if (sandboxHost && sandboxHost.has(cmd.pluginName)) {
      return sandboxHost.callMethod(cmd.pluginName, "executeCommand", [id, ...args]);
    }
    return cmd.handler(...args);
  };

  return {
    manifest: info.manifest,
    commands: {
      register(id, handler) {
        registerCommandImpl(id, handler);
      },
      registerCommand(id, handler) {
        registerCommandImpl(id, handler);
      },
      async execute(id, ...args) {
        return executeCommandImpl(id, args);
      },
      async executeCommand(id, ...args) {
        return executeCommandImpl(id, args);
      },
    },
    views: {
      register(id, component, options) {
        requireActiveContext();
        if (views.has(id)) {
          const existing = views.get(id);
          if (existing && existing.pluginName !== pluginName) {
            throw new Error(
              `View "${id}" is already registered by plugin "${existing.pluginName}"`,
            );
          }
        }
        const contributed = info.manifest.contributes?.views?.find(
          (v) => v.id === id,
        );
        views.set(id, {
          id,
          title: options?.title ?? contributed?.title ?? id,
          location: options?.location ?? contributed?.location ?? "panel",
          pluginName,
          component,
        });
        viewsVersion.value++; // N-57: trigger reactive update
      },
    },
    workspace: {
      async readFile(relPath) {
        requireActiveContext();
        requirePermission("fs.read", "read files");
        // Lazy import to avoid a hard dependency cycle in tests.
        const { appState } = await import("@/stores/app");
        const fullPath = resolvePluginWorkspacePath(
          pluginName,
          appState.currentProject,
          relPath,
        );
        const { fileService } = await import("@/api/services");
        const content = await fileService.readFile(fullPath);
        requireActiveContext();
        return content;
      },
      async writeFile(relPath, content) {
        requireActiveContext();
        requirePermission("fs.write", "write files");
        const { appState } = await import("@/stores/app");
        const fullPath = resolvePluginWorkspacePath(
          pluginName,
          appState.currentProject,
          relPath,
        );
        const { fileService } = await import("@/api/services");
        await fileService.writeFile(fullPath, content);
        requireActiveContext();
      },
    },
    getPermissions() {
      requireActiveContext();
      return Array.from(declared);
    },
  };
}
