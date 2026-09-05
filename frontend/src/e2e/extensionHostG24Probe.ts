/**
 * P9-G24 packaged probe.
 *
 * This hook is installed only in the opt-in packaged E2E build. It drives the
 * real MarketplaceService, renderer lifecycle handshake, and Dedicated Worker
 * extension host. The extension payload used by the backend supplies the
 * lifecycle and fault-injection commands.
 */
import { Events } from "@wailsio/runtime";
import { marketplaceService } from "@/api/services";
import {
  isExtensionActivated,
  refreshExtensionCaches,
  WorkerExtensionModule,
} from "@/lib/vscodeExtensionActivation";
import {
  executeVscodeExtensionCommand,
  listVscodeExtensionCommands,
} from "@/lib/vscodeExtensions";
import { ExtensionHost } from "@/lib/extensionHost/extensionHost";
import type { ExtensionDescriptor } from "@/lib/extensionHost/extensionHost";
import type { ExtensionWorkerRuntimePolicy } from "@/lib/vscodeExtensionActivation";
import {
  waitForWorkerReplacement,
  WorkerReplacementTimeoutError,
} from "./extensionHostG24Recovery";

const resultEvent = "e2e:g24-extension-host-result";

interface ExtensionHostG24ProbeConfig {
  runId: string;
  phase: "activate-v1" | "activate-v2" | "faults" | "verify-uninstalled";
  publisher: string;
  name: string;
  expectedVersion?: string;
}

type ProbeResult = Record<string, unknown> & {
  runId: string;
  ok: boolean;
  error?: string;
};

interface FaultCommandResult {
  suffix: string;
  commandResult: unknown;
  commandError: string | null;
  active: boolean;
  latestActivationError: string | null;
}

interface FaultProbeResult extends FaultCommandResult {
  restarted: boolean;
  previousRuntimeId: string;
  recoveredRuntimeId: string;
}

class WorkerRecoveryError extends Error {
  constructor(
    message: string,
    readonly latestActivationError: string | null,
  ) {
    super(message);
    this.name = "WorkerRecoveryError";
  }
}

class FaultProbeError extends Error {
  constructor(
    message: string,
    readonly details: Record<string, unknown>,
  ) {
    super(message);
    this.name = "FaultProbeError";
  }
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function latestErrorMessage(error: unknown): string {
  let current = error;
  let latest = errorMessage(error);
  const visited = new Set<unknown>();
  while (current instanceof Error && current.cause !== undefined) {
    if (visited.has(current)) break;
    visited.add(current);
    current = current.cause;
    latest = errorMessage(current);
  }
  return latest;
}

function extensionId(config: ExtensionHostG24ProbeConfig): string {
  return `${config.publisher}.${config.name}`;
}

function commandId(
  config: ExtensionHostG24ProbeConfig,
  suffix: string,
): string {
  return `${extensionId(config)}.${suffix}`;
}

function delay(milliseconds: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

function hasCommand(
  config: ExtensionHostG24ProbeConfig,
  suffix: string,
): boolean {
  return listVscodeExtensionCommands().some(
    (command) => command.id === commandId(config, suffix),
  );
}

async function enableAndActivate(
  config: ExtensionHostG24ProbeConfig,
): Promise<string> {
  await marketplaceService.setExtensionEnabled(
    config.publisher,
    config.name,
    true,
  );
  await refreshExtensionCaches(config.publisher, config.name);
  const value = await executeVscodeExtensionCommand(
    commandId(config, "version"),
  );
  return String(value);
}

async function waitForWorkerRecovery(
  config: ExtensionHostG24ProbeConfig,
  previousRuntimeId: string,
): Promise<{ runtimeId: string; latestError: string | null }> {
  try {
    return await waitForWorkerReplacement({
      previousRuntimeId,
      expectedVersion: config.expectedVersion ?? "",
      readRuntimeId: async () =>
        String(
          await executeVscodeExtensionCommand(commandId(config, "runtime")),
        ),
      readVersion: async () =>
        String(
          await executeVscodeExtensionCommand(commandId(config, "version")),
        ),
    });
  } catch (error: unknown) {
    throw new WorkerRecoveryError(
      errorMessage(error),
      error instanceof WorkerReplacementTimeoutError
        ? error.latestError
        : latestErrorMessage(error),
    );
  }
}

async function runAbiProbe(): Promise<Record<string, boolean>> {
  const descriptor: ExtensionDescriptor = {
    id: "g24.abi-fallback",
    mainPath: "extension/dist/main.js",
    permissions: [],
  };
  const policy: ExtensionWorkerRuntimePolicy = {
    offeredProtocolVersions: ["2.0", "1.0"],
    healthCheckIntervalMs: 100,
    healthCheckTimeoutMs: 400,
  };
  const fallbackModule = new WorkerExtensionModule(
    descriptor.id,
    descriptor.mainPath,
    "module.exports = { activate() {} };",
    () => undefined,
    () => undefined,
    policy,
  );
  const fallbackHost = new ExtensionHost();
  await fallbackHost.activateWithModule(descriptor, fallbackModule);
  await fallbackHost.deactivate(descriptor.id);

  const incompatibleDescriptor: ExtensionDescriptor = {
    ...descriptor,
    id: "g24.abi-incompatible",
  };
  const incompatibleModule = new WorkerExtensionModule(
    incompatibleDescriptor.id,
    incompatibleDescriptor.mainPath,
    "module.exports = { activate() {} };",
    () => undefined,
    () => undefined,
    { offeredProtocolVersions: ["2.0"] },
  );
  const incompatibleHost = new ExtensionHost();
  let rejected = false;
  try {
    await incompatibleHost.activateWithModule(
      incompatibleDescriptor,
      incompatibleModule,
    );
  } catch (error: unknown) {
    rejected = /protocol/i.test(errorMessage(error));
  }
  return {
    abiFallbackActivated: true,
    abiIncompatibleRejected: rejected,
  };
}

async function runFaultProbe(
  config: ExtensionHostG24ProbeConfig,
  suffix: string,
): Promise<FaultProbeResult> {
  let previousRuntimeId: string;
  try {
    previousRuntimeId = String(
      await executeVscodeExtensionCommand(commandId(config, "runtime")),
    );
  } catch (error: unknown) {
    throw new FaultProbeError(
      "Could not read the active Worker runtime identity",
      {
        faultPhase: config.phase,
        faultSuffix: suffix,
        commandResult: null,
        commandError: errorMessage(error),
        active: isExtensionActivated(extensionId(config)),
        latestActivationError: latestErrorMessage(error),
      },
    );
  }
  let commandResult: unknown = null;
  let commandError: string | null = null;
  try {
    commandResult = await executeVscodeExtensionCommand(
      commandId(config, suffix),
    );
  } catch (error: unknown) {
    commandError = errorMessage(error);
  }
  let recovery: { runtimeId: string; latestError: string | null };
  try {
    recovery = await waitForWorkerRecovery(config, previousRuntimeId);
  } catch (error: unknown) {
    throw new FaultProbeError(errorMessage(error), {
      faultPhase: config.phase,
      faultSuffix: suffix,
      commandResult,
      commandError,
      active: isExtensionActivated(extensionId(config)),
      previousRuntimeId,
      latestActivationError:
        error instanceof WorkerRecoveryError
          ? error.latestActivationError
          : latestErrorMessage(error),
    });
  }
  const restarted = recovery.runtimeId !== previousRuntimeId;
  if (!restarted) {
    throw new FaultProbeError("Fault command did not fail closed", {
      faultPhase: config.phase,
      faultSuffix: suffix,
      commandResult,
      commandError,
      active: isExtensionActivated(extensionId(config)),
      previousRuntimeId,
      recoveredRuntimeId: recovery.runtimeId,
      latestActivationError: recovery.latestError,
    });
  }
  return {
    suffix,
    commandResult,
    commandError,
    active: isExtensionActivated(extensionId(config)),
    previousRuntimeId,
    recoveredRuntimeId: recovery.runtimeId,
    latestActivationError: recovery.latestError,
    restarted,
  };
}

async function runProbe(
  config: ExtensionHostG24ProbeConfig,
): Promise<ProbeResult> {
  const id = extensionId(config);
  if (config.phase === "activate-v1" || config.phase === "activate-v2") {
    const version = await enableAndActivate(config);
    const expectedVersion = config.expectedVersion ?? "";
    return {
      runId: config.runId,
      ok: version === expectedVersion && isExtensionActivated(id),
      phase: config.phase,
      version,
      active: isExtensionActivated(id),
      commandRegistered: hasCommand(config, "version"),
    };
  }

  if (config.phase === "faults") {
    let abi: Record<string, boolean>;
    try {
      abi = await runAbiProbe();
    } catch (error: unknown) {
      throw new FaultProbeError(errorMessage(error), {
        faultPhase: config.phase,
        faultSuffix: "abi",
        commandResult: null,
        commandError: errorMessage(error),
        active: isExtensionActivated(id),
        latestActivationError: latestErrorMessage(error),
      });
    }
    let permissionResult: unknown;
    try {
      permissionResult = await executeVscodeExtensionCommand(
        commandId(config, "permission"),
      );
    } catch (error: unknown) {
      throw new FaultProbeError(errorMessage(error), {
        faultPhase: config.phase,
        faultSuffix: "permission",
        commandResult: null,
        commandError: errorMessage(error),
        active: isExtensionActivated(id),
        latestActivationError: latestErrorMessage(error),
      });
    }
    const permissionDenied = /permission|denied|unsupported|not allowed/i.test(
      String(permissionResult),
    );
    if (!permissionDenied) {
      throw new FaultProbeError("Permission probe did not fail closed", {
        faultPhase: config.phase,
        faultSuffix: "permission",
        commandResult: permissionResult,
        commandError: null,
        active: isExtensionActivated(id),
        latestActivationError: null,
      });
    }
    let forgedResult: unknown;
    try {
      forgedResult = await executeVscodeExtensionCommand(
        commandId(config, "forge"),
      );
    } catch (error: unknown) {
      throw new FaultProbeError(errorMessage(error), {
        faultPhase: config.phase,
        faultSuffix: "forge",
        commandResult: null,
        commandError: errorMessage(error),
        active: isExtensionActivated(id),
        latestActivationError: latestErrorMessage(error),
      });
    }
    await delay(0);
    const forgedIgnored =
      forgedResult === "forged-sent" && !hasCommand(config, "forged");
    if (!forgedIgnored) {
      throw new FaultProbeError("Forged message probe did not fail closed", {
        faultPhase: config.phase,
        faultSuffix: "forge",
        commandResult: forgedResult,
        commandError: null,
        active: isExtensionActivated(id),
        latestActivationError: null,
      });
    }
    const crash = await runFaultProbe(config, "crash");
    const hang = await runFaultProbe(config, "hang");
    const messageRate = await runFaultProbe(config, "flood");
    const messageSize = await runFaultProbe(config, "oversize");
    const crashRestarted = crash.restarted;
    const hangRestarted = hang.restarted;
    const messageRateRestarted = messageRate.restarted;
    const messageSizeRestarted = messageSize.restarted;

    try {
      await marketplaceService.setExtensionEnabled(
        config.publisher,
        config.name,
        false,
      );
    } catch (error: unknown) {
      throw new FaultProbeError(errorMessage(error), {
        faultPhase: config.phase,
        faultSuffix: "disable",
        commandResult: null,
        commandError: errorMessage(error),
        active: isExtensionActivated(id),
        latestActivationError: latestErrorMessage(error),
      });
    }
    await delay(0);
    const disabled =
      !isExtensionActivated(id) && !hasCommand(config, "version");
    if (!disabled) {
      throw new FaultProbeError(
        "Disabled extension remained active or registered",
        {
          faultPhase: config.phase,
          faultSuffix: "disable",
          commandResult: null,
          commandError: null,
          active: isExtensionActivated(id),
          latestActivationError: null,
        },
      );
    }
    return {
      runId: config.runId,
      ok:
        abi.abiFallbackActivated &&
        abi.abiIncompatibleRejected &&
        permissionDenied &&
        forgedIgnored &&
        crashRestarted &&
        hangRestarted &&
        messageRateRestarted &&
        messageSizeRestarted &&
        disabled,
      phase: config.phase,
      ...abi,
      permissionDenied,
      forgedIgnored,
      crashRestarted,
      hangRestarted,
      messageRateRestarted,
      messageSizeRestarted,
      disabled,
      faultResults: [crash, hang, messageRate, messageSize],
    };
  }

  const uninstalled =
    !isExtensionActivated(id) && !hasCommand(config, "version");
  let installed = true;
  try {
    await marketplaceService.getExtensionManifest(
      config.publisher,
      config.name,
    );
  } catch {
    installed = false;
  }
  return {
    runId: config.runId,
    ok: !installed && uninstalled,
    phase: config.phase,
    installed,
    uninstalled,
  };
}

export function installExtensionHostG24Probe(): void {
  const target = globalThis as typeof globalThis & {
    __koyoriIdeRunG24ExtensionHostProbe?: (
      config: ExtensionHostG24ProbeConfig,
    ) => Promise<void>;
  };
  target.__koyoriIdeRunG24ExtensionHostProbe = async (config) => {
    let result: ProbeResult;
    try {
      result = await runProbe(config);
    } catch (error: unknown) {
      result = {
        runId: config.runId,
        ok: false,
        phase: config.phase,
        error: errorMessage(error),
        ...(error instanceof FaultProbeError ? error.details : {}),
      };
    }
    await Events.Emit(resultEvent, result);
  };
}
