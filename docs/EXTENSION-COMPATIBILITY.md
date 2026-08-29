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

Canonical packages are VS Code VSIX files. `koyoriIde.permissions` is **optional**.
If it is absent, Koyori infers Trusted / Reviewed / Restricted from
`activationEvents`, `contributes`, and a static scan of the entrypoint's
`vscode.*` references. Inference failure (missing entrypoint source, shell, or
network) is Restricted and stays disabled by default — it is not silently
Trusted, and it is **not** an install hard-block. Declared permissions still
must be known names. Available permissions:

`clipboard`, `debug.execute`, `fs.read`, `fs.write`, `network`, `scm.read`,
`scm.write`, `secrets.read`, `secrets.write`, `shell.execute`, `tasks.execute`,
`ui.notifications`, `ui.webview`

## Implemented methods

### `commands`

| Method | Notes |
|---|---|
| `commands.registerCommand` | Dangerous `workbench.action.*` commands always require confirmation regardless of level |

### `languages` — language feature providers

The host exposes the listed provider registrations through a Monaco bridge. A
registration is real only when the corresponding Monaco method exists; missing
methods fail closed with `KOYORI_IDE_EXT_API_UNSUPPORTED`. Monaco 0.52 uses
`registerLinkProvider` for VS Code document-link registrations.

The supported registration surface includes:

`registerCodeActionProvider`, `registerCodeLensProvider`,
`registerColorProvider`, `registerCompletionItemProvider`,
`registerDeclarationProvider`, `registerDefinitionProvider`,
`registerDocumentFormattingEditProvider`, `registerDocumentHighlightProvider`,
`registerDocumentLinkProvider`, `registerDocumentRangeFormattingEditProvider`,
`registerDocumentSemanticTokensProvider`, `registerDocumentSymbolProvider`,
`registerFoldingRangeProvider`, `registerHoverProvider`,
`registerImplementationProvider`, `registerInlayHintsProvider`,
`registerOnTypeFormattingEditProvider`, `registerReferenceProvider`,
`registerRenameProvider`, `registerSignatureHelpProvider`, and
`registerTypeDefinitionProvider`. Workspace-symbol registration remains
subject to the runtime bridge and is unsupported when Monaco has no matching
registration method; it never returns a no-op success.

### `workspace`

| Method | Permission |
|---|---|
| `workspace.applyEdit` | `fs.write` |
| `workspace.findFiles` | `fs.read` |
| `workspace.findTextInFiles` | `fs.read` |
| `workspace.openTextDocument` | `fs.read` |
| `workspace.saveAll` | `fs.write` | G13: real save — flushes every dirty buffer through the editor bridge and throws with the failed paths when any file fails; fails closed (versioned unsupported error) when no bridge is wired |
| `workspace.onDidChangeConfiguration` | — | Settings-store changes are forwarded and the listener is disposable |
| `workspace.createFileSystemWatcher` | `fs.read` | Reports create/change/delete within the current workspace generation; root-external paths are ignored |
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
| `window.showInformationMessage`, `showWarningMessage`, `showErrorMessage` | `ui.notifications` | partial (routes to host notifications when wired; console fallback without a UI bridge) |
| `window.showInputBox`, `showQuickPick` | `ui.notifications` | Real host dialogs; explicit cancellation returns `undefined` |
| `window.setStatusBarMessage`, `createStatusBarItem` | `ui.notifications` | Visible extension-owned text/items with dispose and timeout support |
| `window.withProgress` | `ui.notifications` | Awaits the extension task and forwards progress reports to the status surface |
| `window.createOutputChannel` | `ui.notifications` | Visible Output panel entries with append/clear/show/hide/dispose |
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

- `vscode.window.createInputBox`
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
| `workspace.onDidChangeConfiguration` | implemented | Settings-store changes are forwarded with `affectsConfiguration`; Worker snapshots refresh |
| `workspace.createFileSystemWatcher` | implemented | Polls the active workspace and invalidates when workspace generation changes |
| `window.show*Message` | partial | Host notifications when the notify bridge is wired; console fallback otherwise |
| `window.showInputBox` / `showQuickPick` | implemented | Real host UI; explicit cancellation returns `undefined` |
| `window.setStatusBarMessage` / `createStatusBarItem` | implemented | Visible extension-owned status items with disposal |
| `window.withProgress` | implemented | Awaited task and visible status progress reports |
| `window.createOutputChannel` | implemented | Visible Output panel entries with clear/show/hide/dispose |

## Verification status

The permission gate and API surface are covered by unit tests
(`apiSurface.test.ts`, `vscodeApi.security.test.ts`, `extensionHost.test.ts`).
The G39 host-surface tests cover successful and failure/cancellation paths for
input and picker dialogs, status items, progress, Output, configuration
changes, and workspace-generation watcher invalidation.

The "Not implemented" list above is derived from reading `apiSurface.ts`; a
static declaration is not activation evidence. P14-G38 removes the install
hard-block on missing `koyoriIde.permissions`. Real VSIX packages can install
with inferred permissions, while activation success remains a separate evidence
claim and unknown vscode namespaces fail closed with
`KOYORI_IDE_EXT_API_UNSUPPORTED`.

### P14 fixed-SHA evidence

The frontend real-Worker corpus tests
(`frontend/src/lib/vscodeExtensionActivation.test.ts`) retain these third-party
VSIX bundles by fixed SHA-256. The production-installer test uses the actual Go
`internal/vsixinstall` helper and `MarketplaceService.InstallVSIXFile`, then
loads package files from each resulting installation directory into real
Workers:

| Package | SHA-256 (prefix) | Observed evidence |
|---|---|---|
| Catppuccin.catppuccin-vsc 3.19.0 | `ebf347664837...` | production install, real Worker active, installed `./themes/mocha.json` converted/defined/selected in Monaco through the host theme picker; deactivation restores the built-in theme |
| PKief.material-icon-theme 5.37.0 | `ade9adefe390...` | production install, real Worker active, visible command contribution |
| mechatroner.rainbow-csv 3.24.1 | `0ecb7da3fb2...` | production install, real Worker active, installed Worker executes `rainbow-csv.GoToColumn`, opens the real Element Plus InputBox, then applies Monaco reveal/selection |
| redhat.vscode-yaml 1.25.2026080708 | `23263c28e7b7...` | production install reaches unsupported `vscode.CompletionItem`; activation is rejected and never enters active |

This is a single production-installer -> installed-files -> real-Worker -> host
contribution chain for three packages, plus exact fail-closed evidence for YAML.
It remains frontend `T/I`, not packaged Windows evidence. Theme loading supports
direct JSONC workbench colors and token rules; unsupported `include` inheritance,
unsafe paths, and an extension being removed while its theme is loading fail
closed without changing the active theme. The backend installer corpus test
additionally verifies fixed-SHA extraction, manifest contributions,
disabled-by-default state, and installed entry reads for Catppuccin, Material
Icon Theme, Rainbow CSV, and Djazair. Installation, activation, runtime UI, and
packaged execution remain distinct evidence classes.

## G24 / G38 corpus report

`scripts/g24-corpus-report.mjs` analyzes the real Open VSX corpus retained by
G20 (`build/e2e-evidence/p9-g20/corpus/`) and writes
`build/e2e-evidence/p9-g24/corpus-report.json`. Each package records
identity/version/SHA-256, entrypoint, activation events, contributes summary,
detected `vscode.<ns>.<api>` references, declared or inferred permissions, and
a disposition:

- `supported` — compatible entrypoint and only known vscode API namespaces
  (static analysis). Permissions may be declared **or inferred**. Activation
  success still requires a packaged/host run.
- `unsupported` — missing/incompatible entrypoint or references to unknown
  vscode namespaces (e.g. `vscode.notebooks.*`), which would throw on
  activation. Install may still succeed; activation must not fake-succeed.
- `blocked` — reserved for policy denials other than missing
  `koyoriIde.permissions` (for example blacklist). Missing permission
  declarations are **not** blocked.
- `corrupt` — unreadable archive or invalid/missing `package.json`.

Installing a package is never reported as activation success. Corpus runs are
covered by `scripts/g24-corpus-report.test.mjs` (success, corrupt, missing
entrypoint, unknown API, inferred permissions, duplicate identity, empty
corpus, and unsafe zip entry names).

Do **not** treat a historical 10/10 blocked report as the current product
rule. That result was the old permission hard-block, not VSIX north-star
compatibility.

## Historical G24 packaged qualification (2026-08-11)

The 2026-08-11 historical Windows x64 manifest recorded `status=passed` with
24/24 fixtures, test artifact SHA-256
`7e8abff533098129f6cf858dd9278053c71786edbf5858a6003452589f07b181`, and
source fingerprint
`690aa31cad880bf803037ab734207a9e1f7281d9e05140f65daaef15bd7b6180`.
The current authoritative `build/e2e-evidence/packaged-e2e/manifest.json`
overwrote that record and is partial (11/24), so the historical result cannot
satisfy G40-AC6 or current-code packaged qualification.

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
