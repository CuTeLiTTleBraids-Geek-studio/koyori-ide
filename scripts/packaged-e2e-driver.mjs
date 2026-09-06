import assert from "node:assert/strict";
import path from "node:path";

export const CORE_FIXTURE_IDS = Object.freeze([
  "open-workspace",
  "open-file",
  "edit",
  "save",
  "terminal-command",
  "terminal-exit-package",
  "terminal-reconnect-package",
  "lsp-hover-completion",
  "search-replace",
  "git-diff",
  "git-worktree-package",
  "git-rebase-package",
  "ai-diff-receipt-package",
  "ai-fail-cancel",
  "ai-request-context-package",
  "extension-api-g13-package",
  "monaco-editor-ready",
  "settings-concurrent-package",
  "debug-g14-package",
  "test-explorer-g15-package",
  "language-pack-g23-package",
  "language-pack-builtins-g23-package",
  "extension-host-g24-package",
  "kill-restart-recovery",
]);

export class PackagedE2EClient {
  constructor({ url, token, fetchImpl = globalThis.fetch, commandTimeoutMs = 120_000 }) {
    assert.match(url, /^http:\/\/127\.0\.0\.1:\d+$/);
    assert(token, "an initial E2E token is required");
    assert.equal(typeof fetchImpl, "function", "fetch implementation is required");
    assert(Number.isSafeInteger(commandTimeoutMs) && commandTimeoutMs > 0, "a positive command timeout is required");
    this.url = url;
    this.token = token;
    this.fetchImpl = fetchImpl;
    this.commandTimeoutMs = commandTimeoutMs;
  }

  async command(action, payload = {}) {
    // P9-G10: retry transient connection failures (e.g. the restarted
    // packaged process settling its loopback listener).
    let lastError;
    for (let attempt = 0; attempt < 4; attempt++) {
      const controller = new AbortController();
      let timedOut = false;
      const timeout = setTimeout(() => {
        timedOut = true;
        controller.abort();
      }, this.commandTimeoutMs);
      try {
        const response = await this.fetchImpl(`${this.url}/v1/command`, {
          method: "POST",
          headers: {
            Authorization: `Bearer ${this.token}`,
            "Content-Type": "application/json",
          },
          body: JSON.stringify({ action, ...payload }),
          signal: controller.signal,
        });
        const nextToken = response.headers.get("X-Koyori-IDE-E2E-Token");
        if (nextToken) this.token = nextToken;
        const body = await response.json();
        if (!response.ok || !body.ok) {
          throw new Error(`${action} failed (${response.status}): ${body.error ?? "unknown error"}`);
        }
        if (!nextToken) {
          throw new Error(`${action} succeeded without rotating the one-time token`);
        }
        return body.result;
      } catch (error) {
        if (timedOut) {
          throw new Error(`command ${action} timed out after ${this.commandTimeoutMs}ms`);
        }
        const code = error?.cause?.code ?? error?.cause?.errno;
        lastError = error;
        if (code === "ECONNREFUSED" || code === "ECONNRESET" || code === "ETIMEDOUT" || code === "UND_ERR_CONNECT_TIMEOUT") {
          await new Promise((resolve) => setTimeout(resolve, 750 * (attempt + 1)));
          continue;
        }
        throw error;
      } finally {
        clearTimeout(timeout);
      }
    }
    throw new Error(`command ${action} failed after retries: ${lastError?.message ?? lastError}`);
  }
}

const AGENT_TOOL_ROUND_SPECS = Object.freeze({
  readAuto: Object.freeze({
    toolKind: "read",
    usageOperation: "read",
    approvalMode: "auto-approve",
    decision: "approve",
    outcome: "executed",
    toolCallId: "call_packaged_agent_read",
    finalAssistant: "PACKAGED_AGENT_READ_ROUND_COMPLETE",
    observation: (marker) => marker,
  }),
  searchAuto: Object.freeze({
    toolKind: "search",
    usageOperation: "search",
    approvalMode: "auto-approve",
    decision: "approve",
    outcome: "executed",
    toolCallId: "call_packaged_agent_search",
    finalAssistant: "PACKAGED_AGENT_SEARCH_ROUND_COMPLETE",
    observation: (marker) => marker,
  }),
  writeManualApprove: Object.freeze({
    toolKind: "write",
    usageOperation: "write",
    approvalMode: "ask",
    decision: "approve",
    outcome: "executed",
    toolCallId: "call_packaged_agent_write_approve",
    finalAssistant: "PACKAGED_AGENT_WRITE_APPROVE_ROUND_COMPLETE",
    observation: () => "Wrote agent-write-approve-",
  }),
  writeManualReject: Object.freeze({
    toolKind: "write",
    approvalMode: "ask",
    decision: "reject",
    outcome: "rejected",
    toolCallId: "call_packaged_agent_write_reject",
    finalAssistant: "PACKAGED_AGENT_WRITE_REJECT_ROUND_COMPLETE",
    observation: () => "User rejected the write action",
  }),
  runManualApprove: Object.freeze({
    toolKind: "run",
    usageOperation: "run",
    approvalMode: "ask",
    decision: "approve",
    outcome: "executed",
    toolCallId: "call_packaged_agent_run_approve",
    finalAssistant: "PACKAGED_AGENT_RUN_APPROVE_ROUND_COMPLETE",
    observation: (marker) => marker,
  }),
  runManualReject: Object.freeze({
    toolKind: "run",
    approvalMode: "ask",
    decision: "reject",
    outcome: "rejected",
    toolCallId: "call_packaged_agent_run_reject",
    finalAssistant: "PACKAGED_AGENT_RUN_REJECT_ROUND_COMPLETE",
    observation: () => "User rejected the run action",
  }),
});

function assertManualAgentControl(evidence, round, spec) {
  if (spec.approvalMode !== "ask") {
    assert.equal(evidence.manualControlRequired, false, `${round} unexpectedly required manual control`);
    assert.notEqual(evidence.manualControlClicked, true, `${round} unexpectedly clicked a manual control`);
    return;
  }
  assert.equal(evidence.manualControlRequired, true, `${round} did not require manual control`);
  assert.equal(evidence.manualControlRendered, true, `${round} did not render the real approval component`);
  assert.equal(evidence.manualControlClicked, true, `${round} did not click the real approval component`);
  assert.equal(evidence.manualControlClickEventObserved, true, `${round} did not dispatch a DOM click event`);
  assert.equal(evidence.manualControlWasEnabled, true, `${round} clicked a disabled manual control`);
  assert.equal(evidence.manualControlAction, spec.decision, `${round} clicked the wrong manual action`);
  assert.equal(evidence.manualControlCallId, spec.toolCallId, `${round} manual control used the wrong call ID`);
  assert.equal(evidence.manualControlKind, spec.toolKind, `${round} manual control used the wrong tool kind`);
}

function assertBackendNativeApproval(evidence, round, spec) {
  if (spec.approvalMode !== "ask") return;
  assert.equal(evidence.backendApprovalSource, "e2e-exact-native-approver", `${round} did not use the exact backend approver`);
  if (spec.outcome === "rejected") {
    assert.equal(evidence.backendNativeApprovalObserved, false, `${round} unexpectedly reached backend approval`);
    assert.equal(evidence.backendNativeApprovalCallCount, 0, `${round} called backend approval after renderer rejection`);
    assert.equal(evidence.backendNativeApprovalExpectedCalls, 0, `${round} expected a backend approval call`);
    return;
  }
  assert.equal(evidence.backendNativeApprovalObserved, true, `${round} did not reach backend native approval`);
  assert.equal(evidence.backendNativeApprovalCallCount, 1, `${round} did not call backend approval exactly once`);
  assert.equal(evidence.backendNativeApprovalExpectedCalls, 1, `${round} approval expectation count changed`);
  assert.equal(evidence.backendNativeApprovalDecision, true, `${round} backend native approval was not affirmative`);
  assert.equal(evidence.backendNativeApprovalSequence, 1, `${round} backend native approval was not first and single-use`);
}

function assertExecutedAgentToolRound(evidence, round, spec, expectedObservation) {
  assert.equal(evidence.approvalObserved, true, `renderer ${round} approval was not observed`);
  assert.equal(evidence.approvalPrecededExecution, true, `renderer ${round} approval did not precede execution`);
  assert.equal(evidence.backendCapabilityExecutionObserved, true, `${round} did not traverse backend capability execution`);
  assert.equal(evidence.executionUsageObserved, true, `${round} backend execution usage was not observed`);
  assert.equal(typeof evidence.usageUnitId, "string", `${round} execution usage is missing its UnitID`);
  assert(evidence.usageUnitId.length > 0, `${round} execution usage has an empty UnitID`);
  assert.equal(typeof evidence.usageSessionId, "string", `${round} execution usage is missing its session ID`);
  assert(evidence.usageSessionId.length > 0, `${round} execution usage has an empty session ID`);
  assert.equal(evidence.usageOperation, spec.usageOperation, `${round} execution usage recorded the wrong operation`);
  assert.equal(evidence.usageSuccess, true, `${round} execution usage did not reach success`);
  assert.equal(evidence.usagePending, false, `${round} execution usage remained pending`);
  assert.equal(evidence.usageSessionMatchesRequest, true, `${round} usage belongs to a different Agent session`);
  assert.equal(evidence.usageObservationMatchesResult, true, `${round} usage observation diverged from the result`);
  assert.equal(evidence.observationSubmitted, true, `${round} did not submit its execution observation`);
  assert.equal(evidence.rejectionSubmitted, false, `${round} submitted a rejection after execution`);
  assert.match(evidence.backendObservation ?? "", new RegExp(expectedObservation), `${round} lost its backend observation`);
}

function assertRejectedAgentToolRound(evidence, round, expectedObservation) {
  assert.equal(evidence.approvalObserved, false, `${round} reported renderer approval`);
  assert.equal(evidence.approvalPrecededExecution, false, `${round} reported approval/execution ordering`);
  assert.equal(evidence.backendCapabilityExecutionObserved, false, `${round} reached backend capability execution`);
  assert.equal(evidence.executionUsageObserved, false, `${round} created execution usage`);
  assert.equal(evidence.observationSubmitted, false, `${round} submitted a success observation`);
  assert.equal(evidence.rejectionSubmitted, true, `${round} did not submit a native rejection`);
  assert.match(evidence.backendRejection ?? "", new RegExp(expectedObservation), `${round} lost its rejection result`);
  assert(!evidence.usageUnitId, `${round} retained an unexpected usage UnitID`);
  assert(!evidence.usageSessionId, `${round} retained an unexpected usage session`);
  assert(!evidence.externalReceiptId, `${round} retained an unexpected external receipt`);
}

function assertAgentToolRoundEvidence(evidence, round, spec, marker, workspace) {
  assert(evidence && typeof evidence === "object", `packaged Agent ${round} evidence is missing`);
  assert.equal(evidence.ok, true, `packaged Agent ${round} did not complete`);
  assert.equal(evidence.round, round, `packaged Agent ${round} reported the wrong round identity`);
  assert.equal(evidence.toolKind, spec.toolKind, `packaged Agent ${round} reported the wrong tool kind`);
  assert.equal(evidence.approvalMode, spec.approvalMode, `packaged Agent ${round} reported the wrong approval mode`);
  assert.equal(evidence.expectedDecision, spec.decision, `packaged Agent ${round} reported the wrong decision`);
  assert.equal(evidence.outcome, spec.outcome, `packaged Agent ${round} reported the wrong outcome`);
  assert.equal(
    evidence.backendCatalogPolicyObserved,
    true,
    `packaged Agent ${round} did not verify its backend catalog policy`,
  );
  assert.equal(
    evidence.providerRequestCount,
    2,
    `Agent ${round} loopback provider did not receive exactly two turns`,
  );
  assert.equal(
    evidence.firstRequestOfferedTool,
    true,
    `first Agent ${round} provider turn did not offer the ${spec.toolKind} tool`,
  );
  assert.equal(
    evidence.firstRequestContainedUserTurn,
    true,
    `initial renderer Agent ${round} turn did not reach the provider`,
  );
  assert.equal(evidence.nativeToolCallObserved, true, `renderer did not receive the native ${spec.toolKind} tool call`);
  assert.equal(evidence.decisionObserved, true, `renderer did not observe the ${round} decision`);
  assertManualAgentControl(evidence, round, spec);
  assertBackendNativeApproval(evidence, round, spec);
  const expectedObservation = spec.observation(marker);
  if (spec.outcome === "executed") {
    assertExecutedAgentToolRound(evidence, round, spec, expectedObservation);
  } else {
    assertRejectedAgentToolRound(evidence, round, expectedObservation);
  }
  assert.equal(
    evidence.nativeProtocolResultSubmitted,
    true,
    `renderer did not submit a structured native ${spec.toolKind} tool result`,
  );
  assert.equal(
    evidence.secondRequestContainedObservation,
    true,
    `second Agent ${round} provider turn did not contain the observation`,
  );
  assert.equal(
    evidence.secondRequestUsedNativeToolProtocol,
    true,
    `second Agent ${round} provider turn did not preserve the native tool-call/result protocol`,
  );
  assert.equal(evidence.finalAssistantObserved, true, `second Agent ${round} provider completion was not rendered`);
  assert.equal(
    evidence.finalAssistant,
    spec.finalAssistant,
    `terminal Agent ${round} assistant completion was not retained exactly`,
  );
  assert.equal(evidence.toolCallId, spec.toolCallId, `unexpected native ${spec.toolKind} tool call identity`);

  if (round === "writeManualApprove") {
    assert.equal(evidence.beforeExists, false, "approved write target existed before execution");
    assert.equal(evidence.afterExists, true, "approved write target was not created");
    assert.equal(evidence.diskMatchesRequestedContent, true, "approved write disk content diverged");
    assert.equal(evidence.unrelatedWorkspaceUnchanged, true, "approved write changed an unrelated file");
    assert.match(evidence.afterSha256 ?? "", /^[0-9a-f]{64}$/, "approved write has no disk SHA-256");
    assert.equal(evidence.afterSha256, evidence.expectedContentSha256, "approved write content hash diverged");
    assert.equal(evidence.approvedBytes > 0, true, "approved write byte count was not recorded");
    assert.match(evidence.approvedPath ?? "", /agent-write-approve-[0-9a-f]+\.txt$/i, "approved write path changed");
  }
  if (round === "writeManualReject") {
    assert.equal(evidence.beforeExists, false, "rejected write target existed before the round");
    assert.equal(evidence.afterExists, false, "rejected write created its target");
    assert.equal(evidence.diskUnchanged, true, "rejected write changed its target");
    assert.equal(evidence.workspaceUnchanged, true, "rejected write changed the workspace");
  }
  if (round === "runManualApprove") {
    assert.match(evidence.externalReceiptId ?? "", /\S/, "approved run has no external receipt");
    assert.equal(evidence.externalReceiptReversible, false, "approved run receipt was reversible");
    assert.equal(evidence.externalCompensation, "not-needed", "approved run receipt compensation changed");
    assert.equal(evidence.processOutputObserved, true, "approved run produced no controlled process output");
    assert.equal(evidence.workspaceUnchanged, true, "approved run changed the workspace");
    assert.match(evidence.approvedCommand ?? "", /(?:findstr|\/usr\/bin\/grep)/i, "approved run command was not the controlled direct executable");
    assert.doesNotMatch(evidence.approvedCommand ?? "", /(?:^|[\\/\s])(cmd|powershell|pwsh|sh|bash)(?:\.exe)?\s/i, "approved run used a shell wrapper");
    assert.equal(path.resolve(evidence.approvedCwd), path.resolve(workspace), "approved run cwd left the workspace");
    assert.match(String(evidence.approvedRisk ?? ""), /^(?:safe|elevated|dangerous)$/, "approved run risk was not recorded");
  }
  if (round === "runManualReject") {
    assert.equal(evidence.processOutputObserved, false, "rejected run produced process output");
    assert.equal(evidence.workspaceUnchanged, true, "rejected run changed the workspace");
  }
}

function assertAgentToolRoundsEvidence(evidence, marker, workspace) {
  assert(evidence && typeof evidence === "object", "packaged Agent tool-round evidence is missing");
  for (const [round, spec] of Object.entries(AGENT_TOOL_ROUND_SPECS)) {
    assertAgentToolRoundEvidence(evidence[round], round, spec, marker, workspace);
  }
  assert.equal(
    evidence.workspaceUnchanged,
    true,
    "packaged Agent search round modified its workspace fixture",
  );
}

function assertConversationHandoffEvidence(evidence, marker) {
  assert(evidence && typeof evidence === "object", "packaged conversation handoff evidence is missing");
  assert.equal(evidence.ok, true, "packaged conversation handoff did not complete");
  assert.equal(evidence.aiWindowOpen, true, "AI companion window was not open");
  assert.equal(evidence.aiWindowVisible, true, "AI companion window was not visible");
  assert.equal(evidence.sameRendererInstance, true, "AI renderer remounted between handoffs");
  assert.equal(evidence.sameNativeWindow, true, "native AI window changed between handoffs");
  assert.equal(evidence.sameReceiverEpoch, true, "AI conversation receiver remounted between handoffs");
  assert.match(evidence.rendererInstanceId ?? "", /^handoff-renderer_/, "AI renderer instance ID is missing");
  assert.match(evidence.mainRendererInstanceId ?? "", /^handoff-renderer_/, "main renderer instance ID is missing");
  assert.notEqual(evidence.mainRendererInstanceId, evidence.rendererInstanceId, "main and AI renderer identities collided");
  assert.match(evidence.receiverEpoch ?? "", /^receiver_/, "AI receiver epoch is missing");
  assert.match(evidence.firstConversationId ?? "", /\S/, "first handoff has no conversation ID");
  assert.match(evidence.secondConversationId ?? "", /\S/, "second handoff has no conversation ID");
  assert.notEqual(evidence.firstConversationId, evidence.secondConversationId, "second handoff reused the first conversation");
  assert(Number.isSafeInteger(evidence.firstRevision) && evidence.firstRevision > 0, "first handoff revision is invalid");
  assert(Number.isSafeInteger(evidence.secondRevision) && evidence.secondRevision > 0, "second handoff revision is invalid");
  assert.equal(evidence.firstMarkerObserved, true, `AI window did not load ${marker}_A`);
  assert.equal(evidence.firstDOMMarkerObserved, true, `AI window did not render ${marker}_A`);
  assert.equal(evidence.firstActiveConversationMatches, true, "first active conversation identity diverged");
  assert.equal(evidence.secondMarkerObserved, true, `AI window did not load ${marker}_B`);
  assert.equal(evidence.secondDOMMarkerObserved, true, `AI window did not render ${marker}_B`);
  assert.equal(evidence.secondActiveConversationMatches, true, "second active conversation identity diverged");
  assert.equal(evidence.firstMode, "chat", "first handoff mode diverged");
  assert.equal(evidence.secondMode, "agent", "second handoff mode diverged");
  assert.equal(evidence.firstAcknowledged, true, "first handoff lacked an exact ACK");
  assert.equal(evidence.secondAcknowledged, true, "second handoff lacked an exact ACK");
  assert.equal(
    evidence.windowStatsBefore?.aiWindowsCreated,
    evidence.windowStatsAfter?.aiWindowsCreated,
    "AI native window creation count changed during handoff",
  );
  assert.equal(
    evidence.windowStatsBefore?.aiWindowsClosed,
    evidence.windowStatsAfter?.aiWindowsClosed,
    "AI native window close count changed during handoff",
  );
}

class FixtureProgressCheckpointError extends Error {
  constructor(cause) {
    super(`fixture progress checkpoint failed: ${cause?.message ?? cause}`, {
      cause,
    });
    this.name = "FixtureProgressCheckpointError";
  }
}

export async function runCoreFixtures({
  client,
  workspace,
  filePath,
  initialContent,
  savedContent,
  dirtyContent,
  restart,
  onEvidence,
  onFixtureResult,
}) {
  const completed = [];
  const windowId = "packaged-e2e";

  const checkpoint = async (callback, value) => {
    if (!callback) return;
    try {
      await callback(value);
    } catch (error) {
      throw new FixtureProgressCheckpointError(error);
    }
  };
  const completeFixture = async (id) => {
    assert.equal(
      id,
      CORE_FIXTURE_IDS[completed.length],
      `fixture completed out of order: ${id}`,
    );
    completed.push(id);
    await checkpoint(onFixtureResult, { id, status: "passed" });
  };

  try {

  await client.command("open-workspace", { workspace });

  const initialRecovery = await client.command("recovery-scan");
  assert.equal(initialRecovery.files.length, 0, "initial recovery scan must be empty");
  assert.equal(initialRecovery.corrupt.length, 0, "initial recovery scan must not be corrupt");
  await completeFixture("open-workspace");

  const opened = await client.command("open-file", { path: filePath });
  assert.equal(opened.content, initialContent);
  await completeFixture("open-file");

  const edit = await client.command("edit", {
    path: filePath,
    content: savedContent,
    windowId,
  });
  assert(edit.baselineHash, "edit must return a disk baseline hash");
  await completeFixture("edit");

  await client.command("save", {
    path: filePath,
    content: savedContent,
    baselineHash: edit.baselineHash,
    windowId,
  });
  const saved = await client.command("open-file", { path: filePath });
  assert.equal(saved.content, savedContent);
  await completeFixture("save");

  const terminalMarker = "KOYORI_IDE_E2E_TERMINAL_OK";
  const terminal = await client.command("terminal-command", {
    workspace,
    // P9-G10: `echo` works on Windows PowerShell and POSIX shells; `printf` is missing on Windows PowerShell.
    command: `echo ${terminalMarker}`,
    expected: terminalMarker,
  });
  assert.match(terminal.output, new RegExp(terminalMarker));
  await completeFixture("terminal-command");

  // G16: exit-code protocol — illegal shell rejected, real PTY exit 7 via
  // structured terminal:exited event, resize accepted.
  const g16 = await client.command("terminal-exit-probe", { workspace });
  assert.equal(g16.illegalShellRejected, true, "illegal shell was not rejected");
  assert.equal(g16.resizeOk, true, "resize failed");
  assert.equal(g16.exitEventReceived, true, "terminal:exited event was not received");
  assert.equal(g16.exitCode, 7, "exit code did not reach the event");
  await completeFixture("terminal-exit-package");

  const reconnect = await client.command("terminal-reconnect-probe", { workspace });
  assert.equal(reconnect.exitObserved, true, reconnect.error ?? `renderer did not observe terminal exit: ${JSON.stringify(reconnect)}`);
  assert.equal(reconnect.exitCode, 7, "renderer lost terminal exit code");
  assert.equal(reconnect.reconnectButtonVisible, true, "reconnect button was not actionable");
  assert.equal(reconnect.reconnectButtonLabel.length > 0, true, "reconnect button has no accessible label");
  assert.equal(reconnect.sameSessionReused, true, "reconnect created a duplicate session");
  assert.equal(reconnect.outputAfterReconnect, true, "reconnected terminal did not accept input");
  assert.equal(reconnect.ok, true, reconnect.error ?? "terminal reconnect probe failed");
  await completeFixture("terminal-reconnect-package");

  const lsp = await client.command("lsp-hover-completion", {
    language: "go",
    path: filePath,
    content: savedContent,
    completionLine: 6,
    // `fmt.Prin` ends at UTF-16 column 9 (the leading tab is one code unit).
    // Keep the probe cursor inside the live line so gopls can answer it.
    completionColumn: 9,
    hoverLine: 5,
    hoverColumn: 8,
  });
  assert(
    lsp.completionCount > 0 || lsp.hover,
    "real LSP action returned neither completions nor hover content",
  );
  await completeFixture("lsp-hover-completion");

  // P9-G10: search-replace on the packaged service graph.
  const search = await client.command("search-replace", {
    workspace,
    path: filePath,
    marker: "fmt.Println",
    replacement: "fmt.Print",
  });
  assert(search.matches >= 1, "search-replace found no marker");
  assert(search.replacements >= 1, "search-replace applied no replacements");
  await completeFixture("search-replace");

  // P9-G10: git diff on the packaged service graph (fresh untracked file).
  const git = await client.command("git-diff", {
    workspace,
    path: path.join(workspace, "git-fixture.txt"),
    content: "git diff fixture content\n",
  });
  assert(git.changed, "git status did not report the fixture file");
  assert(git.diff.length > 0, "git diff is empty");
  await completeFixture("git-diff");

  // G17: sibling worktree inside the workspace + out-of-workspace rejection.
  const g17 = await client.command("git-worktree-probe", { workspace });
  assert.equal(g17.repoInitialized, true, "git repo was not initialized");
  assert.equal(g17.siblingCreated, true, "sibling worktree was not created");
  assert.equal(g17.siblingListed, true, "sibling worktree was not listed");
  assert.equal(g17.outsideRejected, true, "out-of-workspace worktree path was accepted");
  await completeFixture("git-worktree-package");

  const rebase = await client.command("git-rebase-probe", { workspace });
  assert.equal(rebase.todoLoaded, true, "rebase todo was not loaded");
  assert.equal(rebase.rebaseStarted, true, "interactive rebase did not start");
  assert.equal(rebase.actionsApplied, true, "rebase actions were not applied");
  assert.equal(rebase.rebaseCompleted, true, "interactive rebase did not complete");
  assert.equal(rebase.noRebaseInProgress, true, "rebase remains in progress");
  assert.equal(rebase.commitCount, 2, "unexpected rebased commit count");
  await completeFixture("git-rebase-package");

  // G18: AI diff commits once with a receipt; a duplicate apply is rejected.
  const g18 = await client.command("ai-diff-receipt-probe", { workspace });
  assert.equal(g18.committedOnce, true, "first ApplyDiff did not commit");
  assert((g18.transactionId ?? "").length > 0, "commit receipt missing transactionId");
  assert.equal(g18.fileHashesRecorded, true, "commit receipt missing file hashes");
  assert.equal(g18.diskMatchesCommit, true, "disk does not match the committed content");
  assert.equal(g18.duplicateRejected, true, "duplicate apply was not rejected");
  assert.equal(g18.diskUnchangedOnReject, true, "disk changed after rejected duplicate apply");
  await completeFixture("ai-diff-receipt-package");

  // P9-G10: AI must fail closed without credentials; a started stream can be stopped.
  const ai = await client.command("ai-fail-cancel", {});
  assert(ai.sendFailed, "AI Send did not fail closed without credentials");
  assert(ai.streamStopped, "AI stream was neither absent nor stopped");
  await completeFixture("ai-fail-cancel");

  // G12: the packaged service graph must deliver plan/persona + image fields
  // to a checkable local protocol service (httptest provider).
  const conversationHandoffMarker = "PACKAGED_CONVERSATION_HANDOFF";
  const conversationHandoff = await client.command("conversation-handoff-probe", {
    marker: conversationHandoffMarker,
  });
  assertConversationHandoffEvidence(conversationHandoff, conversationHandoffMarker);
  await checkpoint(onEvidence, { conversationHandoff });

  const agentToolObservation = "PACKAGED_AGENT_TOOL_OBSERVATION";
  const aiCtx = await client.command("ai-request-context-probe", {
    workspace,
    path: path.join(workspace, "agent-tool-round.txt"),
    marker: agentToolObservation,
  });
  assert.equal(aiCtx.systemPromptReachedProvider, true, "system prompt did not reach provider");
  assert.equal(aiCtx.planInSystemPrompt, true, "plan fields were lost in the provider request");
  assert.equal(aiCtx.personaInSystemPrompt, true, "persona fields were lost in the provider request");
  assert.equal(aiCtx.imageBlockReachedProvider, true, "image attachment did not reach provider as image_url block");
  assert.equal(aiCtx.captured, true, "provider request was not captured");
  assertAgentToolRoundsEvidence(aiCtx.agentToolRounds, agentToolObservation, workspace);
  await checkpoint(onEvidence, { agentToolRounds: aiCtx.agentToolRounds });
  await completeFixture("ai-request-context-package");

  // G13: extension API no-fake-success in the packaged renderer.
  const g13 = await client.command("extension-api-g13-probe", {});
  assert.equal(g13.ok, true, g13.error ?? "G13 extension API probe failed");
  assert.equal(g13.saveAllNoBridgeFailsClosed, true, "saveAll without bridge must fail closed");
  assert.equal(g13.showInputBoxFailsClosed, true, "showInputBox must fail closed (no fake default)");
  assert.equal(g13.showQuickPickFailsClosed, true, "showQuickPick must fail closed (no fake first item)");
  assert.equal(g13.saveAllBridgeCallsRealSave, true, "saveAll with bridge must call the real save path");
  assert.equal(g13.notificationRoutedToHost, true, "notification must route to the host surface");
  assert.equal(g13.outputChannelOperable, true, "output channel must be operable");
  assert.equal(g13.configurationBridged, true, "configuration must be bridged");
  assert.equal(g13.treeViewRegistrationOperable, true, "tree view registration must be operable");
  await completeFixture("extension-api-g13-package");

  const monaco = await client.command("g10-monaco-probe", {
    workspace,
    path: filePath,
  });
  assert(monaco.ok, monaco.error ?? "monaco probe failed");
  assert(monaco.editors > 0, "monaco reported no editor instances");
  assert(monaco.monacoEditorDom, "monaco editor DOM is missing");
  assert.equal(monaco.languageId, "go", "Go file was not registered by the built-in language pack");
  await completeFixture("monaco-editor-ready");

  // P9-G11: dual-window settings CAS on the packaged service graph.
  const settings = await client.command("settings-concurrent", {});
  assert.equal(settings.windowAApplied, true, "window A settings save did not apply");
  assert.equal(settings.staleBRejected, true, "stale window B save was not rejected");
  assert.equal(settings.bReloadSawA, true, "window B reload did not see window A change");
  assert.equal(settings.bRetryApplied, true, "window B retry save did not apply");
  assert.equal(settings.bothFieldsPresent, true, "both windows settings changes were not preserved");
  assert.equal(settings.finalTheme, "dark");
  assert.equal(settings.finalFontSize, 16);
  await completeFixture("settings-concurrent-package");

  // G14: real Delve DAP adapter inside the packaged process — breakpoint,
  // nested variables via adapter-owned references, single step, stop.
  const g14 = await client.command("debug-g14-probe", { workspace });
  assert.equal(g14.dlvLaunch, true, "real dlv did not launch");
  assert.equal(g14.breakpointStop, true, "breakpoint did not stop");
  assert.equal(g14.nestedExpanded, true, "nested variable (Z=42) was not expanded");
  assert.equal(g14.singleStep, true, "single step did not advance");
  assert((g14.adapterReference ?? 0) > 0, "adapter-owned reference missing");
  assert((g14.nestedReference ?? 0) > 0, "nested reference missing");
  assert.equal(g14.adapterId, "delve", "Go debug adapter id did not come from the language pack");
  assert.equal(g14.sourcePackId, "org.koyori.ide.go", "Go debug source pack diverged");
  assert.equal(g14.sourcePackVersion, "1.0.0", "Go debug source pack version diverged");
  await completeFixture("debug-g14-package");

  // G15: packaged renderer Test Explorer state must follow real Go exit codes.
  const g15 = await client.command("test-explorer-g15-probe", { workspace });
  assert.equal(g15.ok, true, g15.error ?? "G15 Test Explorer probe failed");
  assert.equal(g15.passExitCode, 0, "passing test did not expose exit code 0");
  assert.notEqual(g15.failExitCode, 0, "failing test exposed exit code 0");
  assert.equal(g15.passEntryStatus, "pass", "passing entry status diverged");
  assert.equal(g15.failEntryStatus, "fail", "failing entry status diverged");
  assert.equal(g15.passTreeStatus, "passed", "passing tree status stayed stale");
  assert.equal(g15.failTreeStatus, "failed", "failing tree status stayed stale");
  assert.equal(g15.passOutputVisible, true, "passing output was not retained");
  assert.equal(g15.failOutputVisible, true, "failing output was not retained");
  assert.equal(g15.runningCleared, true, "toolchain running state was not cleared");
  await completeFixture("test-explorer-g15-package");

  // G23: signed Python/Rust external packs inside the packaged service graph.
  const g23 = await client.command("language-pack-g23-probe", { workspace });
  assert.equal(g23.signedArchivesVerified, true, "signed archive metadata was not verified");
  assert.equal(g23.publisherTrustOnboarded, true, "unknown publisher trust onboarding was not exercised");
  assert.equal(g23.pythonRustInstalled, true, "Python/Rust packs were not installed");
  assert.equal(g23.versionPinVerified, true, "active language pack versions were not pinned");
  assert.equal(g23.lspSourcesVerified, true, "external LSP source metadata diverged");
  assert.equal(g23.toolchainSourcesVerified, true, "external toolchain source metadata diverged");
  assert.equal(g23.toolchainExecuted, true, "external toolchain command did not execute");
  assert.equal(g23.pythonToolchain?.success, true, "real Python toolchain command did not execute");
  assert.equal(g23.rustToolchain?.success, true, "real Rust toolchain command did not execute");
  assert.match(String(g23.pythonLsp), /^not-run:/, "Python LSP evidence must not be overstated");
  assert.match(String(g23.rustLsp), /^not-run:/, "Rust LSP evidence must not be overstated");
  assert.equal(g23.disableEnableVerified, true, "disable/enable lifecycle failed");
  assert.equal(g23.rollbackVerified, true, "language pack rollback failed");
  assert.equal(g23.uninstallRestoreVerified, true, "uninstall did not restore base brokers");
  await checkpoint(onEvidence, { g23LanguagePack: g23 });
  await completeFixture("language-pack-g23-package");

  const g23Builtins = await client.command("language-pack-builtins-g23-probe", { workspace });
  for (const field of [
    "goLspSource", "goFormat", "goBuild", "goTest",
    "typescriptFormat", "typescriptBuild", "typescriptTest", "typescriptDebug",
    "nativeDebugApprovalConsumed",
  ]) {
    assert.equal(g23Builtins[field], true, `built-in language pack workflow failed: ${field}`);
  }
  assert(
    (g23Builtins.typescriptLsp?.completionCount ?? 0) > 0 || g23Builtins.typescriptLsp?.hover,
    "real TypeScript LSP returned neither completion nor hover",
  );
  assert.equal(g23Builtins.nodeAdapterId, "node-inspector");
  assert.equal(g23Builtins.nodeSourcePackId, "org.koyori.ide.typescript");
  assert.equal(g23Builtins.nodeSourcePackVersion, "1.0.0");
  const goPackEditor = await client.command("g10-monaco-probe", {
    workspace,
    path: g23Builtins.goFilePath,
  });
  assert.equal(goPackEditor.ok, true, goPackEditor.error ?? "Go Monaco probe failed");
  assert.equal(goPackEditor.languageId, "go", "Go editor language did not come from the built-in pack");
  const tsPackEditor = await client.command("g10-monaco-probe", {
    workspace,
    path: g23Builtins.typescriptFilePath,
  });
  assert.equal(tsPackEditor.ok, true, tsPackEditor.error ?? "TypeScript Monaco probe failed");
  assert.equal(tsPackEditor.languageId, "typescript", "TypeScript editor language did not come from the built-in pack");
  await checkpoint(onEvidence, {
    g23BuiltInLanguages: {
      ...g23Builtins,
      goEditing: true,
      goEditorLanguageId: goPackEditor.languageId,
      typescriptEditing: true,
      typescriptEditorLanguageId: tsPackEditor.languageId,
    },
  });
  await completeFixture("language-pack-builtins-g23-package");

  // G24: real VSIX install/update lifecycle and Dedicated Worker isolation.
  const g24 = await client.command("extension-host-g24-probe", { workspace });
  assert.equal(g24.ok, true, g24.error ?? "G24 Extension Host probe failed");
  assert.equal(g24.initialDisabled, true, "G24 install did not start disabled");
  for (const field of [
    "abiFallbackActivated",
    "abiIncompatibleRejected",
    "permissionDenied",
    "forgedIgnored",
    "crashRestarted",
    "hangRestarted",
    "messageRateRestarted",
    "messageSizeRestarted",
    "disabled",
  ]) {
    assert.equal(g24.faultIsolation?.[field], true, `G24 isolation failed: ${field}`);
  }
  assert.equal(g24.v1Activation?.version, "1.0.0", "G24 v1 did not activate");
  assert.equal(g24.v2Activation?.version, "2.0.0", "G24 v2 did not activate");
  assert.equal(g24.uninstallVerification?.uninstalled, true, "G24 renderer retained the uninstalled extension");
  assert.equal(g24.remainingInstalled, 0, "G24 backend retained the uninstalled extension");

  const g24SurvivalPath = path.join(workspace, "g24-worker-survival.txt");
  const g24SurvivalContent = "Koyori IDE remained editable after G24 Worker faults.\n";
  // The survival file does not exist on disk yet. The real product creates
  // the file first (CreateFile) and only then opens an editor buffer whose
  // baseline is the empty file; mirror that ordering so the post-fault
  // edit/save asserts a real baseline instead of a missing-file no-op.
  await client.command("create-file", { path: g24SurvivalPath });
  const g24Edit = await client.command("edit", {
    path: g24SurvivalPath,
    content: g24SurvivalContent,
    windowId,
  });
  assert(g24Edit.baselineHash, "post-G24 edit must return a baseline hash");
  await client.command("save", {
    path: g24SurvivalPath,
    content: g24SurvivalContent,
    baselineHash: g24Edit.baselineHash,
    windowId,
  });
  const g24Saved = await client.command("open-file", { path: g24SurvivalPath });
  assert.equal(g24Saved.content, g24SurvivalContent, "post-G24 save did not reach disk");
  await checkpoint(onEvidence, {
    g24ExtensionHost: { ...g24, editSaveAfterFaults: true },
  });
  await completeFixture("extension-host-g24-package");

  await client.command("edit", {
    path: filePath,
    content: dirtyContent,
    windowId,
  });
  const restartedClient = await restart();
  await restartedClient.command("open-workspace", { workspace });
  const recoveredReceipt = await restartedClient.command("ai-diff-receipt-recovery-probe", {
    workspace,
    expected: g18.transactionId,
  });
  assert.equal(recoveredReceipt.receiptRecovered, true, "restart did not recover the commit receipt");
  assert.equal(recoveredReceipt.transactionIdStable, true, "recovered transaction id changed after restart");
  assert.equal(recoveredReceipt.fileHashesMatchDisk, true, "recovered receipt hash does not match disk");
  assert.equal(recoveredReceipt.receiptWorkspaceMatches, true, "recovered receipt belongs to another workspace");
  assert.equal(recoveredReceipt.duplicateRejected, true, "recovered diff was applied a second time");
  assert.equal(recoveredReceipt.diskUnchangedOnReject, true, "disk changed after recovered duplicate rejection");
  await checkpoint(onEvidence, {
    g18ReceiptRecovery: recoveredReceipt,
  });
  const recovery = await restartedClient.command("recovery-scan");
  const recovered = recovery.files.find((file) => file.path === filePath);
  assert(recovered, "restart did not expose the journaled dirty buffer");
  assert.equal(recovered.content, dirtyContent);
  assert.equal(recovered.status, "clean");
  await completeFixture("kill-restart-recovery");




  assert.deepEqual(completed, CORE_FIXTURE_IDS);
  return completed;
  } catch (error) {
    if (error instanceof FixtureProgressCheckpointError) {
      throw error.cause;
    }

    const failedID = CORE_FIXTURE_IDS[completed.length];
    if (failedID && onFixtureResult) {
      try {
        await onFixtureResult({
          id: failedID,
          status: "failed",
          failure: String(error?.message ?? error),
        });
      } catch (progressError) {
        if (
          error !== null &&
          (typeof error === "object" || typeof error === "function")
        ) {
          Object.defineProperty(error, "fixtureProgressError", {
            configurable: true,
            value: progressError,
          });
        }
      }
    }
    throw error;
  }
}
