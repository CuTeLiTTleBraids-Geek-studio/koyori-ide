import { Events } from "@wailsio/runtime";
import {
  aiState,
  clearMessages,
  type AIStoreMessage,
} from "@/stores/ai";
import {
  aiAssistantState,
  openAIDesktopWindow,
  switchMode,
  type AiMode,
} from "@/stores/aiAssistant";

const resultEvent = "e2e:conversation-handoff-result";
const rendererInstanceId = createRendererInstanceId();

type ConversationHandoffAction = "handoff" | "inspect" | "ready";

interface ConversationHandoffProbeConfig {
  runId: string;
  action: ConversationHandoffAction;
  marker?: string;
  mode?: AiMode;
  expectedConversationId?: string;
  expectedRevision?: number;
  expectedRendererInstanceId?: string;
  forbiddenMarker?: string;
}

interface ConversationHandoffProbeResult {
  runId: string;
  action: ConversationHandoffAction;
  ok: boolean;
  role: string;
  rendererInstanceId: string;
  conversationId: string | null;
  revision: number;
  mode: AiMode;
  markerObserved: boolean;
  domMarkerObserved: boolean;
  activeConversationMatches: boolean;
  windowMounted: boolean;
  acknowledged?: boolean;
  requestId?: string;
  sourceOrigin?: string;
  sourceEpoch?: string;
  sequence?: number;
  recipientEpoch?: string | null;
  receiverEpoch?: string | null;
  error?: string;
}

type RuntimeGlobal = typeof globalThis & {
  __koyoriIdeRuntimeRole?: string;
  __koyoriIdeRunConversationHandoffProbe?: (
    config: ConversationHandoffProbeConfig,
  ) => Promise<void>;
};

function createRendererInstanceId(): string {
  const id = typeof crypto !== "undefined" && typeof crypto.randomUUID === "function"
    ? crypto.randomUUID()
    : `${Date.now().toString(36)}_${Math.random().toString(36).slice(2, 12)}`;
  return `handoff-renderer_${id}`;
}

function message(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function requireCondition(condition: unknown, detail: string): asserts condition {
  if (!condition) throw new Error(detail);
}

function currentRole(): string {
  return (globalThis as RuntimeGlobal).__koyoriIdeRuntimeRole ?? "unknown";
}

function windowMounted(): boolean {
  return document.querySelector(".ai-window") !== null;
}

function markerObserved(marker: string): boolean {
  return aiState.messages.some((entry) => entry.content === marker);
}

function domContainsMarker(marker: string): boolean {
  return document.querySelector(".ai-window__messages")?.textContent?.includes(marker) === true;
}

function snapshot(
  config: ConversationHandoffProbeConfig,
  extra: Partial<ConversationHandoffProbeResult> = {},
): ConversationHandoffProbeResult {
  return {
    runId: config.runId,
    action: config.action,
    ok: true,
    role: currentRole(),
    rendererInstanceId,
    conversationId: aiState.currentConversationId,
    revision: aiState.conversationRevision,
    mode: aiAssistantState.mode,
    markerObserved: config.marker ? markerObserved(config.marker) : false,
    domMarkerObserved: config.marker ? domContainsMarker(config.marker) : false,
    activeConversationMatches:
      aiAssistantState.activeConversationId === aiState.currentConversationId,
    windowMounted: windowMounted(),
    ...extra,
  };
}

async function waitForInspection(config: ConversationHandoffProbeConfig): Promise<void> {
  const marker = config.marker?.trim();
  requireCondition(marker, "conversation handoff inspection requires a marker");
  requireCondition(
    config.expectedConversationId?.trim(),
    "conversation handoff inspection requires a conversation ID",
  );
  requireCondition(
    Number.isSafeInteger(config.expectedRevision) && (config.expectedRevision ?? 0) > 0,
    "conversation handoff inspection requires a positive revision",
  );
  requireCondition(config.mode, "conversation handoff inspection requires a mode");
  requireCondition(
    config.expectedRendererInstanceId?.trim(),
    "conversation handoff inspection requires a renderer instance ID",
  );

  const deadline = Date.now() + 30_000;
  while (Date.now() < deadline) {
    if (
      currentRole() === "ai" &&
      windowMounted() &&
      rendererInstanceId === config.expectedRendererInstanceId &&
      aiState.currentConversationId === config.expectedConversationId &&
      aiAssistantState.activeConversationId === config.expectedConversationId &&
      aiState.conversationRevision === config.expectedRevision &&
      aiAssistantState.mode === config.mode &&
      aiState.messages.length === 1 &&
      aiState.messages[0]?.role === "user" &&
      aiState.messages[0]?.content === marker &&
      domContainsMarker(marker) &&
      (!config.forbiddenMarker || (
        !markerObserved(config.forbiddenMarker) &&
        !domContainsMarker(config.forbiddenMarker)
      ))
    ) return;
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  throw new Error(
    `AI window did not apply conversation ${config.expectedConversationId} ` +
      `revision ${config.expectedRevision} without remounting`,
  );
}

async function runProbe(
  config: ConversationHandoffProbeConfig,
): Promise<ConversationHandoffProbeResult> {
  requireCondition(config.runId?.trim(), "conversation handoff probe requires a run ID");
  if (config.action === "ready") {
    requireCondition(currentRole() === "ai", "ready probe must run in the AI WebView");
    const deadline = Date.now() + 30_000;
    while (!windowMounted() && Date.now() < deadline) {
      await new Promise((resolve) => setTimeout(resolve, 50));
    }
    requireCondition(windowMounted(), "AI window view did not mount");
    return snapshot(config);
  }

  if (config.action === "inspect") {
    await waitForInspection(config);
    return snapshot(config);
  }

  requireCondition(config.action === "handoff", "unsupported conversation handoff action");
  requireCondition(currentRole() === "main", "handoff probe must run in the main WebView");
  const marker = config.marker?.trim();
  requireCondition(marker, "conversation handoff requires a marker");
  requireCondition(config.mode, "conversation handoff requires a mode");
  requireCondition(
    !aiState.streaming && !aiState.globalStreamBusy,
    "AI stream was busy before conversation handoff",
  );

  clearMessages();
  const messageEntry: AIStoreMessage = {
    id: crypto.randomUUID(),
    role: "user",
    content: marker,
  };
  aiState.messages.push(messageEntry);
  switchMode(config.mode);
  const receipt = await openAIDesktopWindow(undefined, { requireConversationAck: true });
  requireCondition(receipt?.acknowledged, "handoff did not receive an exact acknowledgement");
  requireCondition(receipt.receiverEpoch, "handoff acknowledgement has no receiver epoch");
  requireCondition(aiState.currentConversationId, "handoff did not persist a conversation ID");
  requireCondition(aiState.conversationRevision > 0, "handoff did not publish a conversation revision");
  return snapshot(config, {
    acknowledged: true,
    requestId: receipt.requestId,
    sourceOrigin: receipt.sourceOrigin,
    sourceEpoch: receipt.sourceEpoch,
    sequence: receipt.sequence,
    recipientEpoch: receipt.recipientEpoch,
    receiverEpoch: receipt.receiverEpoch,
  });
}

export function installConversationHandoffProbe(): void {
  const target = globalThis as RuntimeGlobal;
  target.__koyoriIdeRunConversationHandoffProbe = async (config) => {
    let result: ConversationHandoffProbeResult;
    try {
      result = await runProbe(config);
    } catch (error: unknown) {
      result = {
        ...snapshot(config),
        ok: false,
        error: message(error),
      };
    }
    await Events.Emit(resultEvent, result);
  };
}
