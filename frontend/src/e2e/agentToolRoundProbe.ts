import { Events } from "@wailsio/runtime";
import {
  clickManualAgentToolDecision,
  waitForAgentToolRoundCompletion,
  type AgentToolRoundApprovalMode,
  type AgentToolRoundExecutionUsage,
  type AgentToolRoundExpectedDecision,
  type AgentToolRoundExpectedOutcome,
  type AgentToolManualControlEvidence,
  type AgentToolRoundSnapshot,
  type AgentToolRoundToolKind,
} from "./agentToolRoundProbeCore";

const resultEvent = "e2e:agent-tool-round-result";

interface AgentToolRoundProbeConfigBase {
  runId: string;
  toolKind: AgentToolRoundToolKind;
  expectedUsageOperation: AgentToolRoundToolKind;
  providerId: string;
  providerBaseUrl: string;
  providerModel: string;
  prompt: string;
  expectedToolCallId: string;
  expectedObservation: string;
  expectedFinalAssistant: string;
}

export type AgentToolRoundProbeConfig = AgentToolRoundProbeConfigBase & (
  | {
      approvalMode: "auto-approve";
      expectedDecision?: "approve";
      expectedOutcome?: "executed";
    }
  | {
      approvalMode: "ask";
      expectedDecision: "approve";
      expectedOutcome: "executed";
    }
  | {
      approvalMode: "ask";
      expectedDecision: "reject";
      expectedOutcome: "rejected";
    }
);

export interface AgentToolRoundProbeResult {
  runId: string;
  ok: boolean;
  toolKind: AgentToolRoundToolKind;
  approvalMode: AgentToolRoundApprovalMode;
  expectedDecision?: AgentToolRoundExpectedDecision;
  expectedOutcome?: AgentToolRoundExpectedOutcome;
  outcome?: AgentToolRoundExpectedOutcome;
  rendererSubmitted: boolean;
  agentModeConfigured: boolean;
  agentPermissionModeConfigured: boolean;
  storedProviderLoaded: boolean;
  nativeToolCallObserved: boolean;
  decisionObserved: boolean;
  approvalObserved: boolean;
  approvalPrecededExecution: boolean;
  backendExecutionObserved: boolean;
  executionUsageObserved: boolean;
  usageUnitId?: string;
  usageSessionId?: string;
  usageOperation?: string;
  usageSuccess?: boolean;
  usagePending?: boolean;
  usageSessionMatchesRequest?: boolean;
  usageObservationMatchesResult?: boolean;
  externalReceiptId?: string;
  externalReceiptReversible?: boolean;
  externalCompensation?: string;
  observationSubmitted: boolean;
  rejectionSubmitted: boolean;
  nativeProtocolResultSubmitted: boolean;
  finalAssistantObserved: boolean;
  toolCallId?: string;
  observation?: string;
  rejection?: string;
  assistantContent?: string;
  manualControlRequired: boolean;
  manualControlRendered: boolean;
  manualControlClicked: boolean;
  manualControlClickEventObserved: boolean;
  manualControlWasEnabled: boolean;
  manualControlAction?: AgentToolRoundExpectedDecision;
  manualControlCallId?: string;
  manualControlKind?: AgentToolRoundToolKind;
  transport: string;
  error?: string;
}

const transport =
  "packaged renderer DOM/store -> AIService stream -> AgentService capability -> backend tool execution -> renderer native result -> AIService stream";

function message(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function expectedContract(config: AgentToolRoundProbeConfig): {
  decision: AgentToolRoundExpectedDecision;
  outcome: AgentToolRoundExpectedOutcome;
} {
  const decision = config.expectedDecision ??
    (config.approvalMode === "auto-approve" ? "approve" : undefined);
  const outcome = config.expectedOutcome ??
    (config.approvalMode === "auto-approve" ? "executed" : undefined);
  if (!decision || !outcome) {
    throw new Error("ask Agent rounds require explicit expectedDecision and expectedOutcome");
  }
  if ((decision === "approve") !== (outcome === "executed")) {
    throw new Error(`Agent decision ${decision} does not match expected outcome ${outcome}`);
  }
  if (config.approvalMode === "auto-approve" && decision !== "approve") {
    throw new Error("auto-approved Agent rounds cannot request rejection");
  }
  return { decision, outcome };
}

interface MountedManualApprovalSurface {
  root: HTMLElement;
  unmount: () => void;
}

async function mountManualApprovalSurface(): Promise<MountedManualApprovalSurface> {
  if (!document.body) throw new Error("packaged renderer document has no body");
  const host = document.createElement("div");
  host.dataset.agentToolProbeHost = "manual-approval";
  host.setAttribute("role", "region");
  host.setAttribute("aria-label", "Agent tool approval probe");
  document.body.append(host);

  try {
    const [{ createApp, nextTick }, component] = await Promise.all([
      import("vue"),
      import("@/components/ai-assistant/AgentToolCalls.vue"),
    ]);
    const app = createApp(component.default);
    app.mount(host);
    await nextTick();
    return {
      root: host,
      unmount: () => {
        try {
          app.unmount();
        } finally {
          host.remove();
        }
      },
    };
  } catch (error) {
    host.remove();
    throw error;
  }
}

async function runProbe(
  config: AgentToolRoundProbeConfig,
): Promise<AgentToolRoundProbeResult> {
  const app = await import("@/stores/app");
  const agent = await import("@/stores/agent");
  const ai = await import("@/stores/ai");
  const timeline = await import("@/stores/agentTimeline");
  const previous = {
    mode: agent.agentState.mode,
    activeAIConfigId: app.appState.activeAIConfigId,
    aiBaseUrl: app.appState.aiBaseUrl,
    aiModel: app.appState.aiModel,
    aiProvider: app.appState.aiProvider,
    temperature: app.appState.temperature,
    maxTokens: app.appState.maxTokens,
    aiProviderConfigs: app.appState.aiProviderConfigs.map((provider) => ({
      ...provider,
    })),
    agentPermissionMode: app.appState.agentPermissionMode,
  };
  let rendererSubmitted = false;
  let agentModeConfigured = false;
  let agentPermissionModeConfigured = false;
  let storedProviderLoaded = false;
  let expectedDecision: AgentToolRoundExpectedDecision | undefined;
  let expectedOutcome: AgentToolRoundExpectedOutcome | undefined;
  let manualSurface: MountedManualApprovalSurface | undefined;
  let manualEvidence: AgentToolManualControlEvidence | undefined;

  const readSnapshot = (): AgentToolRoundSnapshot => {
    const expectedCall = agent.agentState.pendingToolCalls.find(
      (call) => call.id === config.expectedToolCallId,
    );
    const execution = expectedCall?.execution;
    const executionUsage: AgentToolRoundExecutionUsage | undefined = execution
      ? {
          unitId: execution.result.usage.unitId,
          sessionId: execution.result.usage.sessionId,
          operation: execution.result.usage.operation,
          success: execution.result.usage.success,
          pending: Boolean(execution.result.usage.pending),
          sessionMatchesRequest:
            execution.result.usage.sessionId === execution.requestSessionId,
          observationMatchesToolResult:
            execution.result.observation === expectedCall.result,
          externalReceiptId: execution.result.usage.externalReceiptId,
          externalReceiptReversible:
            execution.result.usage.externalReceiptReversible,
          externalCompensation: execution.result.usage.externalCompensation,
        }
      : undefined;
    return {
      streaming: ai.aiState.streaming,
      globalStreamBusy: ai.aiState.globalStreamBusy,
      error: ai.aiState.error,
      executionUsage,
      messages: ai.aiState.messages.map((entry) => ({
        role: entry.role,
        content: entry.content,
        toolCalls: entry.toolCalls?.map((call) => ({ ...call })),
        toolResults: entry.toolResults?.map((result) => ({ ...result })),
      })),
      toolCalls: agent.agentState.pendingToolCalls.map((call) => ({
        id: call.id,
        kind: call.kind,
        status: call.status,
        result: call.result,
        error: call.error,
      })),
      timeline: timeline.agentTimelineState.entries.map((entry) => ({
        toolCallId: entry.toolCallId,
        stage: entry.stage,
        status: entry.status,
      })),
    };
  };

  try {
    const contract = expectedContract(config);
    expectedDecision = contract.decision;
    expectedOutcome = contract.outcome;
    await app.loadSettings();
    const provider = app.appState.aiProviderConfigs.find(
      (candidate) => candidate.id === config.providerId,
    );
    storedProviderLoaded = Boolean(
      provider &&
        provider.baseUrl === config.providerBaseUrl &&
        provider.model === config.providerModel &&
        provider.apiKeyConfigured,
    );
    if (!storedProviderLoaded || app.appState.activeAIConfigId !== config.providerId) {
      throw new Error("packaged loopback provider was not loaded from backend settings");
    }
    const expectedPermissionMode = config.approvalMode === "auto-approve"
      ? "assist"
      : "always-ask";
    app.appState.agentPermissionMode = expectedPermissionMode;
    agentPermissionModeConfigured = app.appState.agentPermissionMode === expectedPermissionMode;
    if (!agentPermissionModeConfigured) {
      throw new Error(
        `Agent permission mode was ${app.appState.agentPermissionMode}; expected ${expectedPermissionMode}`,
      );
    }

    if (ai.aiState.streaming || ai.aiState.globalStreamBusy) {
      throw new Error("AI stream was busy before the packaged Agent probe");
    }
    ai.clearMessages();
    agent.clearPendingToolCalls();
    agent.setMode("agent");
    agentModeConfigured = agent.agentState.mode === "agent";
    if (config.approvalMode === "ask") {
      manualSurface = await mountManualApprovalSurface();
    }

    await ai.sendMessage(config.prompt);
    rendererSubmitted = ai.aiState.messages.some(
      (entry) => entry.role === "user" && entry.content.includes(config.prompt),
    );
    if (!rendererSubmitted) {
      throw new Error("renderer did not submit the initial Agent turn");
    }

    if (manualSurface) {
      manualEvidence = await clickManualAgentToolDecision({
        root: manualSurface.root,
        toolKind: config.toolKind,
        expectedToolCallId: config.expectedToolCallId,
        expectedDecision,
        readSnapshot,
        timeoutMs: 45_000,
        pollIntervalMs: 50,
      });
    }
    const completed = await waitForAgentToolRoundCompletion({
      toolKind: config.toolKind,
      expectedUsageOperation: config.expectedUsageOperation,
      approvalMode: config.approvalMode,
      expectedDecision,
      expectedOutcome,
      expectedToolCallId: config.expectedToolCallId,
      expectedObservation: config.expectedObservation,
      expectedFinalAssistant: config.expectedFinalAssistant,
      readSnapshot,
      timeoutMs: 45_000,
      pollIntervalMs: 50,
    });

    return {
      runId: config.runId,
      ok: true,
      rendererSubmitted,
      agentModeConfigured,
      agentPermissionModeConfigured,
      storedProviderLoaded,
      expectedOutcome,
      manualControlRequired: config.approvalMode === "ask",
      manualControlRendered: manualEvidence?.manualControlRendered ?? false,
      manualControlClicked: manualEvidence?.manualControlClicked ?? false,
      manualControlClickEventObserved:
        manualEvidence?.manualControlClickEventObserved ?? false,
      manualControlWasEnabled: manualEvidence?.manualControlWasEnabled ?? false,
      ...(manualEvidence ?? {}),
      ...completed,
      transport,
    };
  } catch (error: unknown) {
    return {
      runId: config.runId,
      ok: false,
      toolKind: config.toolKind,
      approvalMode: config.approvalMode,
      expectedDecision,
      expectedOutcome,
      rendererSubmitted,
      agentModeConfigured,
      agentPermissionModeConfigured,
      storedProviderLoaded,
      nativeToolCallObserved: readSnapshot().toolCalls.some(
        (call) => call.id === config.expectedToolCallId && call.kind === config.toolKind,
      ),
      decisionObserved: timeline.agentTimelineState.entries.some(
        (entry) => entry.toolCallId === config.expectedToolCallId &&
          ((entry.stage === "approval" && entry.status === "approved") ||
            (entry.stage === "result" && entry.status === "rejected")),
      ),
      approvalObserved: timeline.agentTimelineState.entries.some(
        (entry) =>
          entry.toolCallId === config.expectedToolCallId &&
          entry.stage === "approval" &&
          entry.status === "approved",
      ),
      approvalPrecededExecution: false,
      backendExecutionObserved: false,
      executionUsageObserved: false,
      observationSubmitted: false,
      rejectionSubmitted: false,
      nativeProtocolResultSubmitted: false,
      finalAssistantObserved: false,
      manualControlRequired: config.approvalMode === "ask",
      manualControlRendered: manualEvidence?.manualControlRendered ?? false,
      manualControlClicked: manualEvidence?.manualControlClicked ?? false,
      manualControlClickEventObserved:
        manualEvidence?.manualControlClickEventObserved ?? false,
      manualControlWasEnabled: manualEvidence?.manualControlWasEnabled ?? false,
      ...(manualEvidence ?? {}),
      transport,
      error: message(error),
    };
  } finally {
    if (ai.aiState.streaming) await ai.stopGeneration();
    manualSurface?.unmount();
    agent.clearPendingToolCalls();
    ai.clearMessages();
    agent.setMode(previous.mode);
    app.appState.activeAIConfigId = previous.activeAIConfigId;
    app.appState.aiBaseUrl = previous.aiBaseUrl;
    app.appState.aiModel = previous.aiModel;
    app.appState.aiProvider = previous.aiProvider;
    app.appState.temperature = previous.temperature;
    app.appState.maxTokens = previous.maxTokens;
    app.appState.aiProviderConfigs = previous.aiProviderConfigs;
    app.appState.agentPermissionMode = previous.agentPermissionMode;
  }
}

export function installAgentToolRoundProbe(): void {
  const target = globalThis as typeof globalThis & {
    __koyoriIdeRunAgentToolRoundProbe?: (
      config: AgentToolRoundProbeConfig,
    ) => Promise<void>;
  };
  target.__koyoriIdeRunAgentToolRoundProbe = async (config) => {
    const result = await runProbe(config);
    await Events.Emit(resultEvent, result);
  };
}
