export interface AgentToolRoundMessage {
  role: string;
  content: string;
  toolCalls?: Array<{ id: string; name: string; arguments: string }>;
  toolResults?: Array<{ toolCallId: string; content: string; isError?: boolean }>;
}

export type AgentToolRoundToolKind = "read" | "write" | "run" | "search";
export type AgentToolRoundApprovalMode = "auto-approve" | "ask";
export type AgentToolRoundExpectedDecision = "approve" | "reject";
export type AgentToolRoundExpectedOutcome = "executed" | "rejected";

export interface AgentToolRoundToolCall {
  id: string;
  kind: string;
  status: string;
  result?: string;
  error?: string;
}

export interface AgentToolRoundExecutionUsage {
  unitId: string;
  sessionId: string;
  operation: string;
  success: boolean;
  pending: boolean;
  sessionMatchesRequest: boolean;
  observationMatchesToolResult: boolean;
  externalReceiptId?: string;
  externalReceiptReversible?: boolean;
  externalCompensation?: string;
}

export interface AgentToolRoundTimelineEntry {
  toolCallId?: string;
  stage: string;
  status?: string;
}

export interface AgentToolRoundSnapshot {
  streaming: boolean;
  globalStreamBusy: boolean;
  error: string | null;
  executionUsage?: AgentToolRoundExecutionUsage;
  messages: AgentToolRoundMessage[];
  toolCalls: AgentToolRoundToolCall[];
  timeline: AgentToolRoundTimelineEntry[];
}

export interface AgentToolRoundCompletion {
  toolKind: AgentToolRoundToolKind;
  approvalMode: AgentToolRoundApprovalMode;
  expectedDecision: AgentToolRoundExpectedDecision;
  outcome: AgentToolRoundExpectedOutcome;
  nativeToolCallObserved: true;
  decisionObserved: true;
  approvalObserved: boolean;
  approvalPrecededExecution: boolean;
  backendExecutionObserved: boolean;
  executionUsageObserved: boolean;
  usageUnitId?: string;
  usageSessionId?: string;
  usageOperation?: AgentToolRoundToolKind;
  usageSuccess?: boolean;
  usagePending?: boolean;
  usageSessionMatchesRequest?: boolean;
  usageObservationMatchesResult?: boolean;
  externalReceiptId?: string;
  externalReceiptReversible?: boolean;
  externalCompensation?: string;
  observationSubmitted: boolean;
  rejectionSubmitted: boolean;
  nativeProtocolResultSubmitted: true;
  finalAssistantObserved: true;
  toolCallId: string;
  observation?: string;
  rejection?: string;
  assistantContent: string;
}

interface WaitForAgentToolRoundOptions {
  toolKind: AgentToolRoundToolKind;
  expectedUsageOperation: AgentToolRoundToolKind;
  approvalMode: AgentToolRoundApprovalMode;
  expectedDecision: AgentToolRoundExpectedDecision;
  expectedOutcome: AgentToolRoundExpectedOutcome;
  expectedToolCallId: string;
  expectedObservation: string;
  expectedFinalAssistant: string;
  readSnapshot: () => AgentToolRoundSnapshot;
  timeoutMs: number;
  pollIntervalMs: number;
  now?: () => number;
  sleep?: (milliseconds: number) => Promise<void>;
}

export interface AgentToolManualControlEvidence {
  manualControlRendered: true;
  manualControlClicked: true;
  manualControlClickEventObserved: true;
  manualControlWasEnabled: true;
  manualControlAction: AgentToolRoundExpectedDecision;
  manualControlCallId: string;
  manualControlKind: AgentToolRoundToolKind;
}

interface ClickManualAgentToolDecisionOptions {
  root: ParentNode;
  toolKind: AgentToolRoundToolKind;
  expectedToolCallId: string;
  expectedDecision: AgentToolRoundExpectedDecision;
  readSnapshot: () => AgentToolRoundSnapshot;
  timeoutMs: number;
  pollIntervalMs: number;
  now?: () => number;
  sleep?: (milliseconds: number) => Promise<void>;
}

function assertExpectedContract(
  approvalMode: AgentToolRoundApprovalMode,
  expectedDecision: AgentToolRoundExpectedDecision,
  expectedOutcome: AgentToolRoundExpectedOutcome,
): void {
  if (expectedDecision === "approve" && expectedOutcome !== "executed") {
    throw new Error("approve decisions require an executed outcome");
  }
  if (expectedDecision === "reject" && expectedOutcome !== "rejected") {
    throw new Error("reject decisions require a rejected outcome");
  }
  if (approvalMode === "auto-approve" && expectedDecision !== "approve") {
    throw new Error("auto-approved rounds cannot request a reject decision");
  }
}

function approvalPrecededExecution(
  timeline: AgentToolRoundTimelineEntry[],
  toolCallId: string,
): boolean {
  const approved = timeline.findIndex(
    (entry) => entry.toolCallId === toolCallId &&
      entry.stage === "approval" && entry.status === "approved",
  );
  const executing = timeline.findIndex(
    (entry, index) => index > approved && entry.toolCallId === toolCallId &&
      entry.stage === "executing" && entry.status === "executing",
  );
  const executed = timeline.findIndex(
    (entry, index) => index > executing && entry.toolCallId === toolCallId &&
      entry.stage === "result" && entry.status === "executed",
  );
  const observation = timeline.findIndex(
    (entry, index) => index > executed && entry.toolCallId === toolCallId &&
      entry.stage === "observation" && entry.status === "observation",
  );
  return approved >= 0 && executing > approved && executed > executing && observation > executed;
}

function rejectionPrecededObservation(
  timeline: AgentToolRoundTimelineEntry[],
  toolCallId: string,
): boolean {
  const waiting = timeline.findIndex(
    (entry) => entry.toolCallId === toolCallId &&
      entry.stage === "approval" && entry.status === "waiting-approval",
  );
  const rejected = timeline.findIndex(
    (entry, index) => index > waiting && entry.toolCallId === toolCallId &&
      entry.stage === "result" && entry.status === "rejected",
  );
  const observation = timeline.findIndex(
    (entry, index) => index > rejected && entry.toolCallId === toolCallId &&
      entry.stage === "observation" && entry.status === "observation",
  );
  const executionStage = timeline.some(
    (entry) => entry.toolCallId === toolCallId && (
      (entry.stage === "approval" && entry.status === "approved") ||
      entry.stage === "executing" ||
      (entry.stage === "result" && entry.status === "executed")
    ),
  );
  return waiting >= 0 && rejected > waiting && observation > rejected && !executionStage;
}

function nativeAssistantCallObserved(
  snapshot: AgentToolRoundSnapshot,
  toolCallId: string,
  toolKind: AgentToolRoundToolKind,
): boolean {
  const matching = snapshot.messages.filter(
    (message) => message.role === "assistant" &&
      message.toolCalls?.length === 1 &&
      message.toolCalls[0]?.id === toolCallId &&
      message.toolCalls[0]?.name === toolKind,
  );
  return matching.length === 1;
}

function nativeToolResult(
  snapshot: AgentToolRoundSnapshot,
  toolCallId: string,
): { content: string; isError?: boolean } | undefined {
  const allResults = snapshot.messages.flatMap((message) => message.toolResults ?? []);
  const matching = allResults.filter((result) => result.toolCallId === toolCallId);
  if (allResults.length !== 1 || matching.length !== 1) return undefined;
  const resultMessage = snapshot.messages.find(
    (message) => message.toolResults?.some((result) => result.toolCallId === toolCallId),
  );
  if (resultMessage?.role !== "tool" || resultMessage.toolResults?.length !== 1) {
    return undefined;
  }
  return matching[0];
}

function finalAssistantMessage(
  snapshot: AgentToolRoundSnapshot,
  expectedFinalAssistant: string,
): AgentToolRoundMessage | undefined {
  const matching = snapshot.messages.filter(
    (message) => message.role === "assistant" &&
      message.content.includes(expectedFinalAssistant),
  );
  const final = matching.length === 1 ? matching[0] : undefined;
  return final && snapshot.messages.at(-1) === final ? final : undefined;
}

function nativeMessageChainObserved(
  snapshot: AgentToolRoundSnapshot,
  toolCallId: string,
  toolKind: AgentToolRoundToolKind,
  expectedFinalAssistant: string,
): boolean {
  const assistantIndex = snapshot.messages.findIndex(
    (message) => message.role === "assistant" &&
      message.toolCalls?.length === 1 &&
      message.toolCalls[0]?.id === toolCallId &&
      message.toolCalls[0]?.name === toolKind,
  );
  const resultIndex = snapshot.messages.findIndex(
    (message) => message.role === "tool" &&
      message.toolResults?.length === 1 &&
      message.toolResults[0]?.toolCallId === toolCallId,
  );
  const finalIndex = snapshot.messages.findIndex(
    (message) => message.role === "assistant" &&
      message.content.includes(expectedFinalAssistant),
  );
  return assistantIndex >= 0 && resultIndex === assistantIndex + 1 &&
    finalIndex === resultIndex + 1 && finalIndex === snapshot.messages.length - 1;
}

function approvedFailureDetail(
  snapshot: AgentToolRoundSnapshot,
  toolKind: AgentToolRoundToolKind,
  expectedUsageOperation: AgentToolRoundToolKind,
  expectedToolCallId: string,
  expectedObservation: string,
  expectedFinalAssistant: string,
): string {
  const callWithID = snapshot.toolCalls.find((call) => call.id === expectedToolCallId);
  if (callWithID && callWithID.kind !== toolKind) {
    return `native tool call kind was ${callWithID.kind}; expected ${toolKind}`;
  }
  if (!callWithID) return `native ${toolKind} tool call was not observed`;
  if (callWithID.status !== "executed") return `native tool call status was ${callWithID.status}`;
  if (!callWithID.result?.includes(expectedObservation)) {
    return "backend execution observation was not recorded";
  }
  if (!approvalPrecededExecution(snapshot.timeline, expectedToolCallId)) {
    return "renderer timeline did not record approved -> executing -> executed -> observation in order";
  }
  const usage = snapshot.executionUsage;
  if (usage && usage.operation !== expectedUsageOperation) {
    return `terminal backend usage operation was ${usage.operation}; expected ${expectedUsageOperation}`;
  }
  if (!usage || usage.unitId.trim() === "" || usage.sessionId.trim() === "" ||
    usage.operation !== expectedUsageOperation || !usage.success || usage.pending ||
    !usage.sessionMatchesRequest || !usage.observationMatchesToolResult) {
    return "terminal backend usage was not observed";
  }
  if (toolKind === "run" && (!usage.externalReceiptId?.trim() ||
    usage.externalReceiptReversible !== false ||
    usage.externalCompensation !== "not-needed")) {
    return "terminal run usage did not retain its irreversible external receipt";
  }
  if (!nativeAssistantCallObserved(snapshot, expectedToolCallId, toolKind)) {
    return "renderer did not retain exactly one provider-native assistant tool call";
  }
  const result = nativeToolResult(snapshot, expectedToolCallId);
  if (!result || result.isError === true || !result.content.includes(expectedObservation)) {
    return "renderer did not submit exactly one successful provider-native tool result";
  }
  if (!finalAssistantMessage(snapshot, expectedFinalAssistant)) {
    return "second provider completion was not rendered";
  }
  if (!nativeMessageChainObserved(
    snapshot,
    expectedToolCallId,
    toolKind,
    expectedFinalAssistant,
  )) {
    return "provider-native call, tool result, and final assistant turn were not consecutive";
  }
  if (snapshot.streaming || snapshot.globalStreamBusy) {
    return "second provider turn did not reach an idle terminal state";
  }
  return "Agent tool round did not complete";
}

function rejectedFailureDetail(
  snapshot: AgentToolRoundSnapshot,
  toolKind: AgentToolRoundToolKind,
  expectedToolCallId: string,
  expectedRejection: string,
  expectedFinalAssistant: string,
): string {
  const callWithID = snapshot.toolCalls.find((call) => call.id === expectedToolCallId);
  if (callWithID && callWithID.kind !== toolKind) {
    return `native tool call kind was ${callWithID.kind}; expected ${toolKind}`;
  }
  if (!callWithID) return `native ${toolKind} tool call was not observed`;
  if (callWithID.status !== "rejected") {
    return `native tool call status was ${callWithID.status}; expected rejected`;
  }
  if (snapshot.executionUsage) {
    return "rejected tool call unexpectedly retained backend execution usage";
  }
  if (!rejectionPrecededObservation(snapshot.timeline, expectedToolCallId)) {
    return "renderer timeline did not record waiting-approval -> rejected -> observation without execution";
  }
  if (!nativeAssistantCallObserved(snapshot, expectedToolCallId, toolKind)) {
    return "renderer did not retain exactly one provider-native assistant tool call";
  }
  const result = nativeToolResult(snapshot, expectedToolCallId);
  if (!result || result.isError !== true || !result.content.includes(expectedRejection)) {
    return "renderer did not submit exactly one rejected provider-native tool result";
  }
  if (!finalAssistantMessage(snapshot, expectedFinalAssistant)) {
    return "second provider completion was not rendered";
  }
  if (!nativeMessageChainObserved(
    snapshot,
    expectedToolCallId,
    toolKind,
    expectedFinalAssistant,
  )) {
    return "rejected native result did not lead directly to the final assistant turn";
  }
  if (snapshot.streaming || snapshot.globalStreamBusy) {
    return "rejection provider turn did not reach an idle terminal state";
  }
  return "Agent rejection round did not complete";
}

export async function clickManualAgentToolDecision({
  root,
  toolKind,
  expectedToolCallId,
  expectedDecision,
  readSnapshot,
  timeoutMs,
  pollIntervalMs,
  now = Date.now,
  sleep = (milliseconds) =>
    new Promise<void>((resolve) => setTimeout(resolve, milliseconds)),
}: ClickManualAgentToolDecisionOptions): Promise<AgentToolManualControlEvidence> {
  const deadline = now() + timeoutMs;
  let latest = readSnapshot();
  let disabledControlObserved = false;

  while (true) {
    const callWithID = latest.toolCalls.find((call) => call.id === expectedToolCallId);
    if (callWithID && callWithID.kind !== toolKind) {
      throw new Error(`manual Agent control observed tool kind ${callWithID.kind}; expected ${toolKind}`);
    }
    if (callWithID && callWithID.status !== "pending") {
      throw new Error(`manual Agent control observed status ${callWithID.status} before the click`);
    }
    if (latest.error && !latest.streaming) {
      throw new Error(`packaged Agent provider turn failed: ${latest.error}`);
    }

    if (callWithID && !latest.streaming && !latest.globalStreamBusy) {
      const controls = Array.from(
        root.querySelectorAll<HTMLButtonElement>("button[data-agent-tool-action]"),
      );
      const sameCallControls = controls.filter(
        (button) => button.dataset.agentToolCallId === expectedToolCallId,
      );
      const wrongKind = sameCallControls.find(
        (button) => button.dataset.agentToolKind !== toolKind,
      );
      if (wrongKind) {
        throw new Error(
          `manual Agent DOM control kind was ${wrongKind.dataset.agentToolKind}; expected ${toolKind}`,
        );
      }
      const button = sameCallControls.find(
        (candidate) => candidate.dataset.agentToolAction === expectedDecision,
      );
      if (button) {
        const card = button.closest<HTMLElement>("[data-agent-tool-status]");
        if (!card || card.dataset.agentToolCallId !== expectedToolCallId ||
          card.dataset.agentToolKind !== toolKind || card.dataset.agentToolStatus !== "pending") {
          throw new Error("manual Agent DOM control was not rendered by the matching pending tool card");
        }
        if (button.disabled || button.getAttribute("aria-disabled") === "true") {
          // Vue may commit the backend busy-state release one microtask after
          // the store flips idle. Keep polling, but never click while disabled.
          disabledControlObserved = true;
        } else {
          let clickEventCount = 0;
          const observeClick = () => { clickEventCount += 1; };
          button.addEventListener("click", observeClick, { capture: true, once: true });
          button.click();
          await Promise.resolve();
          button.removeEventListener("click", observeClick, { capture: true });
          if (clickEventCount !== 1) {
            throw new Error("HTMLElement.click() did not dispatch exactly one manual Agent click event");
          }
          return {
            manualControlRendered: true,
            manualControlClicked: true,
            manualControlClickEventObserved: true,
            manualControlWasEnabled: true,
            manualControlAction: expectedDecision,
            manualControlCallId: expectedToolCallId,
            manualControlKind: toolKind,
          };
        }
      }
    }

    if (now() >= deadline) {
      const state = disabledControlObserved
        ? `matching ${toolKind} ${expectedDecision} button remained disabled after the stream became idle`
        : callWithID
          ? `matching ${toolKind} call was pending and idle but its ${expectedDecision} button was absent`
        : `matching pending ${toolKind} call was absent`;
      throw new Error(`timed out waiting for manual Agent DOM control: ${state}`);
    }
    await sleep(pollIntervalMs);
    latest = readSnapshot();
  }
}

export async function waitForAgentToolRoundCompletion({
  toolKind,
  expectedUsageOperation,
  approvalMode,
  expectedDecision,
  expectedOutcome,
  expectedToolCallId,
  expectedObservation,
  expectedFinalAssistant,
  readSnapshot,
  timeoutMs,
  pollIntervalMs,
  now = Date.now,
  sleep = (milliseconds) =>
    new Promise<void>((resolve) => setTimeout(resolve, milliseconds)),
}: WaitForAgentToolRoundOptions): Promise<AgentToolRoundCompletion> {
  assertExpectedContract(approvalMode, expectedDecision, expectedOutcome);
  const deadline = now() + timeoutMs;
  let latest = readSnapshot();

  while (true) {
    const callWithID = latest.toolCalls.find((call) => call.id === expectedToolCallId);
    if (callWithID && callWithID.kind !== toolKind) {
      throw new Error(`packaged Agent native tool kind was ${callWithID.kind}; expected ${toolKind}`);
    }
    if (expectedOutcome === "executed" && callWithID?.status === "rejected") {
      throw new Error(`packaged Agent tool execution failed: ${callWithID.error || "rejected"}`);
    }
    if (callWithID?.status === "error") {
      throw new Error(`packaged Agent tool execution failed: ${callWithID.error || callWithID.status}`);
    }
    if (expectedOutcome === "rejected" && callWithID?.status === "executed") {
      throw new Error("packaged Agent rejected round unexpectedly executed the tool");
    }
    if (latest.error && !latest.streaming) {
      throw new Error(`packaged Agent provider turn failed: ${latest.error}`);
    }

    const assistantCallObserved = nativeAssistantCallObserved(latest, expectedToolCallId, toolKind);
    const result = nativeToolResult(latest, expectedToolCallId);
    const finalAssistant = finalAssistantMessage(latest, expectedFinalAssistant);

    if (expectedOutcome === "executed") {
      const observation = callWithID?.status === "executed" &&
        callWithID.result?.includes(expectedObservation)
        ? callWithID.result
        : undefined;
      const usage = latest.executionUsage;
      const approvalSequenceObserved = approvalPrecededExecution(latest.timeline, expectedToolCallId);
      const terminalUsageObserved = Boolean(
        usage && usage.unitId.trim() !== "" && usage.sessionId.trim() !== "" &&
          usage.operation === expectedUsageOperation && usage.success && !usage.pending &&
          usage.sessionMatchesRequest && usage.observationMatchesToolResult &&
          (toolKind !== "run" || (
            Boolean(usage.externalReceiptId?.trim()) &&
            usage.externalReceiptReversible === false &&
            usage.externalCompensation === "not-needed"
          )),
      );
      if (callWithID && observation && assistantCallObserved && result &&
        result.isError !== true && result.content.includes(expectedObservation) &&
        finalAssistant && nativeMessageChainObserved(
          latest,
          expectedToolCallId,
          toolKind,
          expectedFinalAssistant,
        ) && approvalSequenceObserved && usage && terminalUsageObserved &&
        !latest.streaming && !latest.globalStreamBusy) {
        return {
          toolKind,
          approvalMode,
          expectedDecision,
          outcome: "executed",
          nativeToolCallObserved: true,
          decisionObserved: true,
          approvalObserved: true,
          approvalPrecededExecution: true,
          backendExecutionObserved: true,
          executionUsageObserved: true,
          usageUnitId: usage.unitId,
          usageSessionId: usage.sessionId,
          usageOperation: expectedUsageOperation,
          usageSuccess: true,
          usagePending: false,
          usageSessionMatchesRequest: true,
          usageObservationMatchesResult: true,
          externalReceiptId: usage.externalReceiptId,
          externalReceiptReversible: usage.externalReceiptReversible,
          externalCompensation: usage.externalCompensation,
          observationSubmitted: true,
          rejectionSubmitted: false,
          nativeProtocolResultSubmitted: true,
          finalAssistantObserved: true,
          toolCallId: callWithID.id,
          observation,
          assistantContent: finalAssistant.content,
        };
      }
    } else {
      const rejectionSequenceObserved = rejectionPrecededObservation(
        latest.timeline,
        expectedToolCallId,
      );
      if (callWithID?.status === "rejected" && assistantCallObserved &&
        result?.isError === true && result.content.includes(expectedObservation) &&
        finalAssistant && nativeMessageChainObserved(
          latest,
          expectedToolCallId,
          toolKind,
          expectedFinalAssistant,
        ) && rejectionSequenceObserved && !latest.executionUsage &&
        !latest.streaming && !latest.globalStreamBusy) {
        return {
          toolKind,
          approvalMode,
          expectedDecision,
          outcome: "rejected",
          nativeToolCallObserved: true,
          decisionObserved: true,
          approvalObserved: false,
          approvalPrecededExecution: false,
          backendExecutionObserved: false,
          executionUsageObserved: false,
          observationSubmitted: false,
          rejectionSubmitted: true,
          nativeProtocolResultSubmitted: true,
          finalAssistantObserved: true,
          toolCallId: callWithID.id,
          rejection: result.content,
          assistantContent: finalAssistant.content,
        };
      }
    }

    if (now() >= deadline) {
      const detail = expectedOutcome === "executed"
        ? approvedFailureDetail(
            latest,
            toolKind,
            expectedUsageOperation,
            expectedToolCallId,
            expectedObservation,
            expectedFinalAssistant,
          )
        : rejectedFailureDetail(
            latest,
            toolKind,
            expectedToolCallId,
            expectedObservation,
            expectedFinalAssistant,
          );
      throw new Error(`timed out waiting for packaged Agent tool round: ${detail}`);
    }
    await sleep(pollIntervalMs);
    latest = readSnapshot();
  }
}
