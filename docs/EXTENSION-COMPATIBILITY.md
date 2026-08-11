# Extension API Compatibility Matrix

> **Scope statement.** Koyori IDE implements a *constrained, permission-gated
> subset* of the VS Code Extension API. It is **not** a VS Code compatibility
> layer and does not claim Marketplace compatibility. Most VS Code extensions
> will not work. This document lists exactly what is implemented, so the
> boundary is verifiable rather than asserted.

The authoritative source is `frontend/src/lib/extensionHost/apiSurface.ts`.
Methods absent from that file are unavailable: the host denies by default.

G13 capability statuses (`implemented` / `partial` / `unsupported`) are
tracked at runtime in `frontend/src/lib/extensionHost/apiCapability.ts` and
must match this document. Unsupported APIs fail closed with the versioned
error `KOYORI_IDE_EXT_API_UNSUPPORTED` — they never return fake success.

## Security levels

Every method declares a minimum security level. Extensions are assigned a level
by `ExtensionSecurityService`; extensions are **disabled by default** on install.

| Level | Intent |
|---|---|
| `trusted` | Read-only APIs and provider registration |
| `reviewed` | Adds file writes (path-validated) and user-visible messages |
| `restricted` | Adds shell execution (confirmation required) and network (per-request approval) |

Level ordering is `trusted < reviewed < restricted`; a method requiring
`reviewed` is unavailable to a `trusted` extension.

## Permissions

An extension must declare a permission in its manifest *and* satisfy the level
requirement. Available permissions:

`clipboard`, `debug.execute`, `fs.read`, `fs.write`, `network`, `scm.read`,
`scm.write`, `secrets.read`, `secrets.write`, `shell.execute`, `tasks.execute`,
`ui.notifications`, `ui.webview`

## Implemented methods

### `commands`

| Method | Notes |
|---|---|
| `commands.registerCommand` | Dangerous `workbench.action.*` commands always require confirmation regardless of level |

### `languages` — language feature providers

All provider registrations are implemented:

`registerCodeActionsProvider`, `registerCodeLensProvider`,
`registerColorProvider`, `registerCompletionItemProvider`,
`registerDeclarationProvider`, `registerDefinitionProvider`,
`registerDocumentFormattingEditProvider`, `registerDocumentHighlightProvider`,
`registerDocumentLinkProvider`, `registerDocumentRangeFormattingEditProvider`,
`registerDocumentSemanticTokensProvider`, `registerDocumentSymbolProvider`,
`registerFoldingRangeProvider`, `registerHoverProvider`,
`registerImplementationProvider`, `registerInlayHintsProvider`,
`registerOnTypeFormattingEditProvider`, `registerReferenceProvider`,
`registerRenameProvider`, `registerSignatureHelpProvider`,
`registerTypeDefinitionProvider`, `registerWorkspaceSymbolProvider`

### `workspace`

| Method | Permission |
|---|---|
| `workspace.applyEdit` | `fs.write` |
| `workspace.findFiles` | `fs.read` |
| `workspace.findTextInFiles` | `fs.read` |
| `workspace.openTextDocument` | `fs.read` |
| `workspace.saveAll` | `fs.write` | G13: real save — flushes every dirty buffer through the editor bridge and throws with the failed paths when any file fails; fails closed (versioned unsupported error) when no bridge is wired |
| `workspace.onDidChangeTextDocument` | `fs.read` |
| `workspace.onDidOpenTextDocument` | `fs.read` |
| `workspace.onDidSaveTextDocument` | `fs.read` |

### `fs`

| Method | Permission |
|---|---|
| `fs.readFile`, `fs.readdir`, `fs.readDirectory` | `fs.read` |
| `fs.writeFile`, `fs.createDirectory`, `fs.rename` | `fs.write` |
| `fs.delete`, `fs.deleteFile` | `fs.write` |

All paths are validated against the workspace root by the Go backend; an
extension cannot escape the workspace even with `fs.write`.

### `window`

| Method | Permission | G13 status |
|---|---|---|
| `window.showInformationMessage`, `showWarningMessage`, `showErrorMessage` | `ui.notifications` | implemented (routes to host notifications when the notify bridge is wired; console-only otherwise — partial) |
| `window.showInputBox`, `showQuickPick` | `ui.notifications` | **unsupported** — fail closed with `KOYORI_IDE_EXT_API_UNSUPPORTED`; returning a default/first value would be fake success |
| `window.createOutputChannel` | — |
| `window.createTerminal` | `shell.execute` |
| `window.createWebviewPanel`, `registerWebviewViewProvider` | `ui.webview` |
| `window.registerTreeDataProvider` | — |

### `scm`

| Method | Permission |
|---|---|
| `scm.getStatus`, `getDiff`, `getBranchInfo` | `scm.read` |
| `scm.stage`, `unstage`, `commit` | `scm.write` |
| `scm.createSourceControl` | `scm.read` |

### `tasks`

| Method | Permission |
|---|---|
| `tasks.fetchTasks`, `registerTaskProvider` | — |
| `tasks.executeTask` | `tasks.execute` |

### `debug`

| Method | Permission |
|---|---|
| `debug.registerDebugConfigurationProvider` | — |
| `debug.startDebugging` | `debug.execute` |

Only the built-in Go Delve DAP path and a narrower Node CDP path are supported.
**Generic extension-contributed DAP adapters are not supported.**

### `env`

| Method | Permission |
|---|---|
| `env.clipboard.readText`, `writeText` | `clipboard` |
| `env.openExternal` | `network` |
| `env.machineId`, `env.sessionId` | — |

### `secrets`

| Method | Permission |
|---|---|
| `secrets.get` | `secrets.read` |
| `secrets.store`, `secrets.delete` | `secrets.write` |

### `shell` / `network`

| Method | Permission | Notes |
|---|---|---|
| `shell.execute` | `shell.execute` | Requires interactive confirmation; goes through the same backend capability-token path as the Agent |
| `network.request` | `network` | Per-request approval |

## Not implemented

The following widely-used VS Code API areas have **no implementation**. This
list is not exhaustive; treat any namespace absent from `apiSurface.ts` as
unavailable.

- `vscode.window.createStatusBarItem`, `withProgress`, `createInputBox`
- `vscode.workspace.createFileSystemWatcher`
- `vscode.notebook.*` (notebook API)
- `vscode.test.*` (testing API)
- `vscode.authentication.*`
- `vscode.l10n.*`
- `vscode.comments.*`
- `vscode.CustomEditorProvider`
- Proposed / unstable APIs
- Node.js built-in module access from extension code (the host is a Web Worker,
  not a Node process)

## G13 no-fake-success audit (2026-08-07)

Every API below was audited for fake success (returning success without a real
user-visible behavior). Statuses come from `apiCapability.ts`:

| API | Status | Behavior |
|---|---|---|
| `workspace.saveAll` | implemented | Real save through editor bridge; per-file failures propagate (throws with failed paths) |
| `workspace.getConfiguration` | implemented | Bridged to settings store |
| `workspace.onDidChangeConfiguration` | partial | Disposable returned; change events not forwarded yet |
| `window.show*Message` | implemented | Host notifications when the notify bridge is wired; console-only otherwise (partial) |
| `window.showInputBox` / `showQuickPick` | unsupported | Fail closed with `KOYORI_IDE_EXT_API_UNSUPPORTED` — no fake default/first-item result |
| `window.createOutputChannel` | partial | In-memory buffer with console mirroring; no UI panel yet |

## Verification status

The permission gate and the API surface list are covered by unit tests
(`apiSurface.test.ts`, `vscodeApi.security.test.ts`, `extensionHost.test.ts`).

**No successful real-extension compatibility suite has been run.** The "Not
implemented" list above is derived from reading `apiSurface.ts`. A 10-package
Open VSX corpus has been analyzed, but every package was blocked before
activation as described below. Do not read this document as evidence that any
specific extension works.

## G24 corpus report (2026-08-10)

`scripts/g24-corpus-report.mjs` analyzes the real Open VSX corpus retained by
G20 (`build/e2e-evidence/p9-g20/corpus/`) and writes
`build/e2e-evidence/p9-g24/corpus-report.json`. Each package records
identity/version/SHA-256, entrypoint, activation events, contributes summary,
detected `vscode.<ns>.<api>` references, and a disposition:

- `supported` — compatible entrypoint, declared `koyoriIde.permissions`, and
  only known vscode API namespaces (static analysis; activation success still
  requires the packaged run);
- `unsupported` — missing/incompatible entrypoint or references to unknown
  vscode namespaces (e.g. `vscode.notebooks.*`), which would throw on
  activation;
- `blocked` — no `koyoriIde.permissions` declaration; the install is rejected
  by the permission gate;
- `corrupt` — unreadable archive or invalid/missing `package.json`.

Installing a package is never reported as activation success. Corpus runs are
covered by `scripts/g24-corpus-report.test.mjs` (success, corrupt, missing
entrypoint, unknown API, missing permission, duplicate identity, empty corpus,
and unsafe zip entry names).

Current real-corpus result: **10/10 blocked** (all lack `koyoriIde.permissions`
declarations), consistent with the G20 install matrix. No real package has
been observed activating. **10/10 blocked is security-policy evidence, not
compatibility success, a 100% compatibility rate, or proof that any corpus
package can run.**

## G24 packaged qualification (2026-08-11)

The retained Windows x64 manifest
`build/e2e-evidence/packaged-e2e/manifest.json` has `status=passed` with 24/24
fixtures passed. It binds the run to test artifact SHA-256
`7e8abff533098129f6cf858dd9278053c71786edbf5858a6003452589f07b181` and
source fingerprint
`690aa31cad880bf803037ab734207a9e1f7281d9e05140f65daaef15bd7b6180`.

The synthetic G24 lifecycle packages were retained as v1 SHA-256
`f9bfd0c7220088eae58d4770a69e308df58f7def8b1e8aff266419c76d3f4a12` and
v2 SHA-256
`b10304f4d8d609e1232a1b9ec8df69b7859f23cde70f54ed8d3796c240586cc9`.
The fixture proves v1/v2 activation, ABI fallback and incompatible-version
rejection, permission denial, forged-message ignore, recovery after
crash/hang/rate/size faults, the disable lifecycle handshake, uninstall, and
`editSaveAfterFaults=true`.

The successful rerun used `KOYORI_IDE_E2E_SKIP_BUILD=1` and therefore reused
the existing artifact. Before launch it verified these five renderer markers in
the existing `frontend/dist`:

- `__koyoriIdeRunG10MonacoProbe`
- `__koyoriIdeRunG13ExtensionApiProbe`
- `__koyoriIdeRunG15TestExplorerProbe`
- `__koyoriIdeRunTerminalReconnectProbe`
- `__koyoriIdeRunG24ExtensionHostProbe`

This is packaged qualification for the named synthetic workflow on one Windows
x64 test artifact. It is not evidence of production-grade reliability,
cross-platform coverage, Marketplace support, or general extension
compatibility.
