import assert from "node:assert/strict";
import test from "node:test";

import {
  CORE_FIXTURE_IDS,
  PackagedE2EClient,
  runCoreFixtures,
} from "./packaged-e2e-driver.mjs";

test("declares the complete packaged fixture contract", () => {
  assert.deepEqual(CORE_FIXTURE_IDS, [
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
});

test("rotates the one-time bearer token after every authenticated command", async () => {
  const authorizations = [];
  const tokens = ["token-2", "token-3"];
  const client = new PackagedE2EClient({
    url: "http://127.0.0.1:32123",
    token: "token-1",
    fetchImpl: async (_url, options) => {
      authorizations.push(options.headers.Authorization);
      return new Response(JSON.stringify({ ok: true, result: {} }), {
        status: 200,
        headers: { "X-Koyori-IDE-E2E-Token": tokens.shift() },
      });
    },
  });

  await client.command("first");
  await client.command("second");
  assert.deepEqual(authorizations, ["Bearer token-1", "Bearer token-2"]);
});

test("fails a timed-out command without retrying a possibly mutating request", async () => {
  let attempts = 0;
  const client = new PackagedE2EClient({
    url: "http://127.0.0.1:32123",
    token: "token-1",
    commandTimeoutMs: 20,
    fetchImpl: async (_url, options) => {
      attempts++;
      await new Promise((_resolve, reject) => {
        options.signal.addEventListener("abort", () => reject(options.signal.reason), { once: true });
      });
    },
  });

  await assert.rejects(client.command("mutating-command"), /timed out after 20ms/);
  assert.equal(attempts, 1);
});

test("runs every fixture and restarts before checking recovery", async () => {
  const actions = [];
  const evidence = [];
  let currentContent = "package fixture\n";
  const firstClient = {
    command: async (action, payload) => {
      actions.push(`first:${action}`);
      if (action === "recovery-scan") return { files: [], corrupt: [] };
      if (action === "open-file") return { content: currentContent };
      if (action === "edit") return { baselineHash: "baseline-hash" };
      if (action === "save") {
        currentContent = payload.content;
        return { saved: true };
      }
      if (action === "terminal-command") return { output: payload.expected };
      if (action === "terminal-exit-probe") {
        return { illegalShellRejected: true, resizeOk: true, exitEventReceived: true, exitCode: 7 };
      }
      if (action === "terminal-reconnect-probe") {
        return {
          exitObserved: true,
          exitCode: 7,
          reconnectButtonVisible: true,
          reconnectButtonLabel: "Reconnect terminal",
          sameSessionReused: true,
          outputAfterReconnect: true,
          ok: true,
        };
      }
      if (action === "lsp-hover-completion") return { completionCount: 1, hover: "fixture" };
      if (action === "search-replace") return { matches: 1, replacements: 1 };
      if (action === "git-diff") return { changed: true, diff: "fixture diff" };
      if (action === "git-worktree-probe") {
        return { repoInitialized: true, siblingCreated: true, siblingListed: true, outsideRejected: true };
      }
      if (action === "git-rebase-probe") {
        return {
          todoLoaded: true,
          rebaseStarted: true,
          actionsApplied: true,
          rebaseCompleted: true,
          noRebaseInProgress: true,
          commitCount: 2,
        };
      }
      if (action === "ai-diff-receipt-probe") {
        return {
          committedOnce: true,
          transactionId: "tx-1",
          fileHashesRecorded: true,
          diskMatchesCommit: true,
          duplicateRejected: true,
          diskUnchangedOnReject: true,
        };
      }
      if (action === "ai-fail-cancel") return { sendFailed: true, streamStopped: true };
      if (action === "ai-request-context-probe") {
        return {
          systemPromptReachedProvider: true,
          planInSystemPrompt: true,
          personaInSystemPrompt: true,
          imageBlockReachedProvider: true,
          captured: true,
        };
      }
      if (action === "extension-api-g13-probe") {
        return {
          ok: true,
          saveAllNoBridgeFailsClosed: true,
          showInputBoxFailsClosed: true,
          showQuickPickFailsClosed: true,
          saveAllBridgeCallsRealSave: true,
          notificationRoutedToHost: true,
          outputChannelOperable: true,
          configurationBridged: true,
          treeViewRegistrationOperable: true,
        };
      }
      if (action === "g10-monaco-probe") {
        return {
          ok: true,
          editors: 1,
          monacoEditorDom: true,
          languageId: payload.path?.endsWith("index.ts") ? "typescript" : "go",
        };
      }
      if (action === "settings-concurrent") {
        return {
          windowAApplied: true,
          staleBRejected: true,
          bReloadSawA: true,
          bRetryApplied: true,
          bothFieldsPresent: true,
          finalTheme: "dark",
          finalFontSize: 16,
        };
      }
      if (action === "debug-g14-probe") {
        return {
          dlvLaunch: true,
          breakpointStop: true,
          nestedExpanded: true,
          singleStep: true,
          adapterReference: 1,
          nestedReference: 2,
          adapterId: "delve",
          sourcePackId: "org.koyori.ide.go",
          sourcePackVersion: "1.0.0",
        };
      }
      if (action === "test-explorer-g15-probe") {
        return {
          ok: true,
          passExitCode: 0,
          failExitCode: 1,
          passEntryStatus: "pass",
          failEntryStatus: "fail",
          passTreeStatus: "passed",
          failTreeStatus: "failed",
          passOutputVisible: true,
          failOutputVisible: true,
          runningCleared: true,
        };
      }
      if (action === "language-pack-g23-probe") {
        return {
          signedArchivesVerified: true,
          publisherTrustOnboarded: true,
          pythonRustInstalled: true,
          versionPinVerified: true,
          lspSourcesVerified: true,
          toolchainSourcesVerified: true,
          toolchainExecuted: true,
          pythonToolchain: { success: true, output: "Python compiled" },
          rustToolchain: { success: true, output: "Finished" },
          pythonLsp: "not-run: contract mock",
          rustLsp: "not-run: contract mock",
          disableEnableVerified: true,
          rollbackVerified: true,
          uninstallRestoreVerified: true,
        };
      }
      if (action === "language-pack-builtins-g23-probe") {
        return {
          goLspSource: true,
          goFormat: true,
          goBuild: true,
          goTest: true,
          typescriptLsp: { completionCount: 1, hover: "number" },
          typescriptFormat: true,
          typescriptBuild: true,
          typescriptTest: true,
          typescriptDebug: true,
          nativeDebugApprovalConsumed: true,
          nodeAdapterId: "node-inspector",
          nodeSourcePackId: "org.koyori.ide.typescript",
          nodeSourcePackVersion: "1.0.0",
          goFilePath: "/workspace/main.go",
          typescriptFilePath: "/workspace/g23-typescript/index.ts",
        };
      }
      if (action === "extension-host-g24-probe") {
        return {
          ok: true,
          initialDisabled: true,
          v1Activation: { version: "1.0.0" },
          v2Activation: { version: "2.0.0" },
          faultIsolation: {
            abiFallbackActivated: true,
            abiIncompatibleRejected: true,
            permissionDenied: true,
            forgedIgnored: true,
            crashRestarted: true,
            hangRestarted: true,
            messageRateRestarted: true,
            messageSizeRestarted: true,
            disabled: true,
          },
          uninstallVerification: { uninstalled: true },
          remainingInstalled: 0,
        };
      }
      return {};
    },
  };
    const restartedClient = {
      command: async (action) => {
        actions.push(`restarted:${action}`);
        if (action === "recovery-scan") {
          return { files: [{ path: "/workspace/main.go", content: "dirty", status: "clean" }] };
        }
        if (action === "ai-diff-receipt-recovery-probe") {
          return {
            receiptRecovered: true,
            transactionIdStable: true,
            fileHashesMatchDisk: true,
            receiptWorkspaceMatches: true,
            duplicateRejected: true,
            diskUnchangedOnReject: true,
          };
        }
        return {};
      },
  };

  const completed = await runCoreFixtures({
    client: firstClient,
    workspace: "/workspace",
    filePath: "/workspace/main.go",
    initialContent: "package fixture\n",
    savedContent: "package fixture\n\nfunc Saved() {}\n",
    dirtyContent: "dirty",
    restart: async () => restartedClient,
    onEvidence: (entry) => evidence.push(entry),
  });

  assert.deepEqual(completed, CORE_FIXTURE_IDS);
  assert.deepEqual(actions, [
    "first:open-workspace",
    "first:recovery-scan",
    "first:open-file",
    "first:edit",
    "first:save",
    "first:open-file",
    "first:terminal-command",
    "first:terminal-exit-probe",
    "first:terminal-reconnect-probe",
    "first:lsp-hover-completion",
    "first:search-replace",
    "first:git-diff",
    "first:git-worktree-probe",
    "first:git-rebase-probe",
    "first:ai-diff-receipt-probe",
    "first:ai-fail-cancel",
    "first:ai-request-context-probe",
    "first:extension-api-g13-probe",
    "first:g10-monaco-probe",
    "first:settings-concurrent",
    "first:debug-g14-probe",
    "first:test-explorer-g15-probe",
    "first:language-pack-g23-probe",
    "first:language-pack-builtins-g23-probe",
    "first:g10-monaco-probe",
    "first:g10-monaco-probe",
    "first:extension-host-g24-probe",
    "first:create-file",
    "first:edit",
    "first:save",
    "first:open-file",
    "first:edit",
    "restarted:open-workspace",
    "restarted:ai-diff-receipt-recovery-probe",
    "restarted:recovery-scan",
  ]);
  assert.equal(evidence.length, 4);
  assert.equal(
    evidence.find((entry) => entry.g23LanguagePack)?.g23LanguagePack.rollbackVerified,
    true,
  );
  assert.equal(
    evidence.find((entry) => entry.g24ExtensionHost)?.g24ExtensionHost.editSaveAfterFaults,
    true,
  );
});
