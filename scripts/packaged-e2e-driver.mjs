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
export async function runCoreFixtures({
  client,
  workspace,
  filePath,
  initialContent,
  savedContent,
  dirtyContent,
  restart,
  onEvidence,
}) {
  const completed = [];
  const windowId = "packaged-e2e";

  await client.command("open-workspace", { workspace });
  completed.push("open-workspace");

  const initialRecovery = await client.command("recovery-scan");
  assert.equal(initialRecovery.files.length, 0, "initial recovery scan must be empty");
  assert.equal(initialRecovery.corrupt.length, 0, "initial recovery scan must not be corrupt");

  const opened = await client.command("open-file", { path: filePath });
  assert.equal(opened.content, initialContent);
  completed.push("open-file");

  const edit = await client.command("edit", {
    path: filePath,
    content: savedContent,
    windowId,
  });
  assert(edit.baselineHash, "edit must return a disk baseline hash");
  completed.push("edit");

  await client.command("save", {
    path: filePath,
    content: savedContent,
    baselineHash: edit.baselineHash,
    windowId,
  });
  const saved = await client.command("open-file", { path: filePath });
  assert.equal(saved.content, savedContent);
  completed.push("save");

  const terminalMarker = "KOYORI_IDE_E2E_TERMINAL_OK";
  const terminal = await client.command("terminal-command", {
    workspace,
    // P9-G10: `echo` works on Windows PowerShell and POSIX shells; `printf` is missing on Windows PowerShell.
    command: `echo ${terminalMarker}`,
    expected: terminalMarker,
  });
  assert.match(terminal.output, new RegExp(terminalMarker));
  completed.push("terminal-command");

  // G16: exit-code protocol — illegal shell rejected, real PTY exit 7 via
  // structured terminal:exited event, resize accepted.
  const g16 = await client.command("terminal-exit-probe", { workspace });
  assert.equal(g16.illegalShellRejected, true, "illegal shell was not rejected");
  assert.equal(g16.resizeOk, true, "resize failed");
  assert.equal(g16.exitEventReceived, true, "terminal:exited event was not received");
  assert.equal(g16.exitCode, 7, "exit code did not reach the event");
  completed.push("terminal-exit-package");

  const reconnect = await client.command("terminal-reconnect-probe", { workspace });
  assert.equal(reconnect.exitObserved, true, reconnect.error ?? `renderer did not observe terminal exit: ${JSON.stringify(reconnect)}`);
  assert.equal(reconnect.exitCode, 7, "renderer lost terminal exit code");
  assert.equal(reconnect.reconnectButtonVisible, true, "reconnect button was not actionable");
  assert.equal(reconnect.reconnectButtonLabel.length > 0, true, "reconnect button has no accessible label");
  assert.equal(reconnect.sameSessionReused, true, "reconnect created a duplicate session");
  assert.equal(reconnect.outputAfterReconnect, true, "reconnected terminal did not accept input");
  assert.equal(reconnect.ok, true, reconnect.error ?? "terminal reconnect probe failed");
  completed.push("terminal-reconnect-package");

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
  completed.push("lsp-hover-completion");

  // P9-G10: search-replace on the packaged service graph.
  const search = await client.command("search-replace", {
    workspace,
    path: filePath,
    marker: "fmt.Println",
    replacement: "fmt.Print",
  });
  assert(search.matches >= 1, "search-replace found no marker");
  assert(search.replacements >= 1, "search-replace applied no replacements");
  completed.push("search-replace");

  // P9-G10: git diff on the packaged service graph (fresh untracked file).
  const git = await client.command("git-diff", {
    workspace,
    path: path.join(workspace, "git-fixture.txt"),
    content: "git diff fixture content\n",
  });
  assert(git.changed, "git status did not report the fixture file");
  assert(git.diff.length > 0, "git diff is empty");
  completed.push("git-diff");

  // G17: sibling worktree inside the workspace + out-of-workspace rejection.
  const g17 = await client.command("git-worktree-probe", { workspace });
  assert.equal(g17.repoInitialized, true, "git repo was not initialized");
  assert.equal(g17.siblingCreated, true, "sibling worktree was not created");
  assert.equal(g17.siblingListed, true, "sibling worktree was not listed");
  assert.equal(g17.outsideRejected, true, "out-of-workspace worktree path was accepted");
  completed.push("git-worktree-package");

  const rebase = await client.command("git-rebase-probe", { workspace });
  assert.equal(rebase.todoLoaded, true, "rebase todo was not loaded");
  assert.equal(rebase.rebaseStarted, true, "interactive rebase did not start");
  assert.equal(rebase.actionsApplied, true, "rebase actions were not applied");
  assert.equal(rebase.rebaseCompleted, true, "interactive rebase did not complete");
  assert.equal(rebase.noRebaseInProgress, true, "rebase remains in progress");
  assert.equal(rebase.commitCount, 2, "unexpected rebased commit count");
  completed.push("git-rebase-package");

  // G18: AI diff commits once with a receipt; a duplicate apply is rejected.
  const g18 = await client.command("ai-diff-receipt-probe", { workspace });
  assert.equal(g18.committedOnce, true, "first ApplyDiff did not commit");
  assert((g18.transactionId ?? "").length > 0, "commit receipt missing transactionId");
  assert.equal(g18.fileHashesRecorded, true, "commit receipt missing file hashes");
  assert.equal(g18.diskMatchesCommit, true, "disk does not match the committed content");
  assert.equal(g18.duplicateRejected, true, "duplicate apply was not rejected");
  assert.equal(g18.diskUnchangedOnReject, true, "disk changed after rejected duplicate apply");
  completed.push("ai-diff-receipt-package");

  // P9-G10: AI must fail closed without credentials; a started stream can be stopped.
  const ai = await client.command("ai-fail-cancel", {});
  assert(ai.sendFailed, "AI Send did not fail closed without credentials");
  assert(ai.streamStopped, "AI stream was neither absent nor stopped");
  completed.push("ai-fail-cancel");

  // G12: the packaged service graph must deliver plan/persona + image fields
  // to a checkable local protocol service (httptest provider).
  const aiCtx = await client.command("ai-request-context-probe", {});
  assert.equal(aiCtx.systemPromptReachedProvider, true, "system prompt did not reach provider");
  assert.equal(aiCtx.planInSystemPrompt, true, "plan fields were lost in the provider request");
  assert.equal(aiCtx.personaInSystemPrompt, true, "persona fields were lost in the provider request");
  assert.equal(aiCtx.imageBlockReachedProvider, true, "image attachment did not reach provider as image_url block");
  assert.equal(aiCtx.captured, true, "provider request was not captured");
  completed.push("ai-request-context-package");

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
  completed.push("extension-api-g13-package");

  const monaco = await client.command("g10-monaco-probe", {
    workspace,
    path: filePath,
  });
  assert(monaco.ok, monaco.error ?? "monaco probe failed");
  assert(monaco.editors > 0, "monaco reported no editor instances");
  assert(monaco.monacoEditorDom, "monaco editor DOM is missing");
  assert.equal(monaco.languageId, "go", "Go file was not registered by the built-in language pack");
  completed.push("monaco-editor-ready");

  // P9-G11: dual-window settings CAS on the packaged service graph.
  const settings = await client.command("settings-concurrent", {});
  assert.equal(settings.windowAApplied, true, "window A settings save did not apply");
  assert.equal(settings.staleBRejected, true, "stale window B save was not rejected");
  assert.equal(settings.bReloadSawA, true, "window B reload did not see window A change");
  assert.equal(settings.bRetryApplied, true, "window B retry save did not apply");
  assert.equal(settings.bothFieldsPresent, true, "both windows settings changes were not preserved");
  assert.equal(settings.finalTheme, "dark");
  assert.equal(settings.finalFontSize, 16);
  completed.push("settings-concurrent-package");

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
  completed.push("debug-g14-package");

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
  completed.push("test-explorer-g15-package");

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
  onEvidence?.({ g23LanguagePack: g23 });
  completed.push("language-pack-g23-package");

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
  onEvidence?.({
    g23BuiltInLanguages: {
      ...g23Builtins,
      goEditing: true,
      goEditorLanguageId: goPackEditor.languageId,
      typescriptEditing: true,
      typescriptEditorLanguageId: tsPackEditor.languageId,
    },
  });
  completed.push("language-pack-builtins-g23-package");

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
  onEvidence?.({ g24ExtensionHost: { ...g24, editSaveAfterFaults: true } });
  completed.push("extension-host-g24-package");

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
  onEvidence?.({
    g18ReceiptRecovery: recoveredReceipt,
  });
  const recovery = await restartedClient.command("recovery-scan");
  const recovered = recovery.files.find((file) => file.path === filePath);
  assert(recovered, "restart did not expose the journaled dirty buffer");
  assert.equal(recovered.content, dirtyContent);
  assert.equal(recovered.status, "clean");
  completed.push("kill-restart-recovery");




  assert.deepEqual(completed, CORE_FIXTURE_IDS);
  return completed;
}
