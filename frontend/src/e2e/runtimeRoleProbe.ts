// Koyori IDE 模块 · Runtime Role Probe。
// 喵，这是 Koyori IDE 的 Runtime Role Probe 模块（前端实现）~
import { Events } from "@wailsio/runtime";
import * as WindowServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/windowservice.js";

const resultEvent = "e2e:g06-runtime-role-result";

interface RuntimeRoleProbeConfig {
  runId: string;
  expectedRole: "main" | "ai";
  forgedToken: string;
}

interface RuntimeRoleProbeResult {
  runId: string;
  expectedRole: string;
  role: string;
  stages: string[];
  forgedRole: string;
  ownerPresent: boolean;
  ok: boolean;
  error?: string;
}

type RuntimeGlobal = typeof globalThis & {
  __koyoriIdeRuntimeRole?: string;
  __koyoriIdeFrontendRuntimeOwner?: symbol | null;
  __koyoriIdeFrontendBootstrap?: { role: string; stages: string[] };
};

function message(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function requireCondition(condition: unknown, detail: string): asserts condition {
  if (!condition) throw new Error(detail);
}

async function waitForBootstrap(config: RuntimeRoleProbeConfig): Promise<void> {
  const expectedStage = config.expectedRole === "ai" ? "minimal-role-complete" : "workflows";
  const deadline = Date.now() + 30_000;
  while (Date.now() < deadline) {
    const trace = (globalThis as RuntimeGlobal).__koyoriIdeFrontendBootstrap;
    if (trace?.role === config.expectedRole && trace.stages.includes(expectedStage)) return;
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  throw new Error(`bootstrap trace did not settle for ${config.expectedRole}`);
}

async function runProbe(config: RuntimeRoleProbeConfig): Promise<RuntimeRoleProbeResult> {
  await waitForBootstrap(config);
  const runtime = globalThis as RuntimeGlobal;
  const trace = runtime.__koyoriIdeFrontendBootstrap;
  requireCondition(trace, "frontend bootstrap trace is missing");
  const forgedRole = await WindowServiceBindings.ResolveRuntimeRole(config.forgedToken);
  const stages = [...trace.stages];
  const forbiddenStages = [
    "debug-runtime",
    "test-explorer-runtime",
    "connectivity",
    "lsp",
    "plugin-sandbox",
    "plugins",
    "layout",
    "workflows",
  ];
  if (config.expectedRole === "ai") {
    requireCondition(
      forbiddenStages.every((stage) => !stages.includes(stage)),
      `AI bootstrap ran a main-only stage: ${stages.join(",")}`,
    );
  }
  requireCondition(trace.role === config.expectedRole, `resolved role ${trace.role} != ${config.expectedRole}`);
  requireCondition(runtime.__koyoriIdeRuntimeRole === config.expectedRole, "runtime global role mismatch");
  requireCondition(forgedRole === "minimal", `forged token resolved to ${forgedRole}`);
  return {
    runId: config.runId,
    expectedRole: config.expectedRole,
    role: trace.role,
    stages,
    forgedRole,
    ownerPresent: runtime.__koyoriIdeFrontendRuntimeOwner != null,
    ok: true,
  };
}

export function installRuntimeRoleProbe(): void {
  const target = globalThis as typeof globalThis & {
    __koyoriIdeRunG06RuntimeRoleProbe?: (config: RuntimeRoleProbeConfig) => Promise<void>;
  };
  target.__koyoriIdeRunG06RuntimeRoleProbe = async (config) => {
    let result: RuntimeRoleProbeResult;
    try {
      result = await runProbe(config);
    } catch (error: unknown) {
      result = {
        runId: config.runId,
        expectedRole: config.expectedRole,
        role: (globalThis as RuntimeGlobal).__koyoriIdeRuntimeRole ?? "minimal",
        stages: [...((globalThis as RuntimeGlobal).__koyoriIdeFrontendBootstrap?.stages ?? [])],
        forgedRole: "unknown",
        ownerPresent: (globalThis as RuntimeGlobal).__koyoriIdeFrontendRuntimeOwner != null,
        ok: false,
        error: message(error),
      };
    }
    await Events.Emit(resultEvent, result);
  };
}
