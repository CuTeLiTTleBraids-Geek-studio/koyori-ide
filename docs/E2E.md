# End-to-end testing

## Current state, stated precisely

| Layer | What it actually runs | Evidence level |
|---|---|---|
| `scripts/contract-smoke.mjs` | Node drives file-service semantics plus one mocked-Wails store test | contract smoke only |
| `scripts/packaged-e2e.mjs --dry-run` | Validates the 24-fixture source plan and Wails pin without launching an artifact | source validation only |
| `scripts/packaged-e2e.mjs` | Builds an `e2e`-tagged Koyori IDE artifact, launches it, and drives real backend services through a loopback-only test endpoint | packaged integration after a retained successful run |

The contract smoke and packaged harness are separate programs and must not be
conflated. `contract-smoke.mjs` does not launch a binary or WebView and cannot
prove packaging, binding, process-recovery, or native-runtime behavior.

## Retained Windows packaged run

The retained manifest at
`build/e2e-evidence/packaged-e2e/manifest.json` records a successful Windows
x64 run completed on 2026-08-11:

- status: `passed`; all 24 fixtures have status `passed`
- artifact: `bin/koyori-ide.exe`
- artifact SHA-256:
  `7e8abff533098129f6cf858dd9278053c71786edbf5858a6003452589f07b181`
- source fingerprint SHA-256:
  `690aa31cad880bf803037ab734207a9e1f7281d9e05140f65daaef15bd7b6180`
- Wails CLI: `v3.0.0-alpha2.111`
- build tags: `desktop`, `production`, `e2e`
- screenshot: not retained by this run (`null` in the manifest)

This workspace has no Git metadata, so the manifest correctly records
`commit: null` and `gitMetadataAvailable: false`. The source fingerprint binds
the run to the tested source set; it is not a commit or CI attestation. The
artifact is test-tagged and must not be described as a release artifact.

## Toolchain pin

Packaged E2E requires the same Wails CLI version as the library in `go.mod`:

```bash
go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.5
```

## Running the contract smoke

```bash
cd frontend && npm ci
node scripts/contract-smoke.mjs
```

Exit code 0 proves only the named contract-smoke paths.

## Language Pack integration (P9-G23)

These focused Go integration tests launch real local tools; they are not
packaged UI evidence:

```powershell
go test ./services -run 'TestLanguagePackReal(Python|Rust)LSPToolchainAndDebug' -count=1 -v
```

The pinned Windows matrix uses Pyright `1.1.411`, Rust and rust-analyzer
`1.97.1`, LLVM/lldb-dap `22.1.8`, and debugpy. Python covers 5,000 source files
and two workspace roots; Rust covers two Cargo roots. Both require non-empty
completion and second-root hover, execute a declared toolchain command, and
stop at a real DAP breakpoint with local variable `answer=42`.

Focused tests also cover missing tools/offline operation, engine/host/platform
incompatibility, strict SemVer downgrade rejection, rollback/uninstall,
traversal/symlink/decompression attacks, adapter stderr bounds, and toolchain
output bounds.

The packaged fixtures distinguish two claims:

- `language-pack-builtins-g23-package` proves the built-in Go/TypeScript Monaco
  language IDs, LSP, formatting, build, test, Delve/Node debug, native debug
  approval, and pack ID/version/source metadata through the packaged process.
- `language-pack-g23-package` proves signed manifest-only Python/Rust publisher
  trust, install, version pin, disable/enable, rollback, and uninstall. It does
  not execute complete Python/Rust LSP/DAP paths and therefore does not prove a
  complete third-party packaged language pack.

Server-binary payload installation, remote language hosting, Linux/macOS
packaged language runs, and a complete third-party packaged LSP/DAP matrix
remain `U`.

## Packaged E2E harness

The harness performs the following stages:

| Stage | Status |
|---|---|
| Verify the installed `wails3` CLI against the `go.mod` pin | implemented |
| Build the frontend with the E2E Monaco marker | implemented |
| Build with `wails3 build -tags desktop,production,e2e` | implemented |
| Record artifact hash, source commit when available, otherwise source fingerprint | implemented |
| Launch the native artifact and authenticate a loopback-only driver | implemented |
| Capture runner metadata, logs, screenshot metadata, fixture results, and goal-specific evidence | implemented |
| Kill and restart the artifact to verify recovery | implemented |

The 24 fixtures are:

```text
open-workspace
open-file
edit
save
terminal-command
terminal-exit-package
terminal-reconnect-package
lsp-hover-completion
search-replace
git-diff
git-worktree-package
git-rebase-package
ai-diff-receipt-package
ai-fail-cancel
ai-request-context-package
extension-api-g13-package
monaco-editor-ready
settings-concurrent-package
debug-g14-package
test-explorer-g15-package
language-pack-g23-package
language-pack-builtins-g23-package
extension-host-g24-package
kill-restart-recovery
```

These fixtures call the packaged process's real Project, File, Recovery,
Terminal, LSP, Git, AI, Extension, Debug, Test, Language Pack, and Toolchain
service graph. The harness creates only isolated fixture data. The run proves
backend service behavior and process recovery; it does not claim pixel-level or
full DOM/Monaco interaction coverage.

## Test-only automation boundary

The automation server lives in `internal/e2e/` and is compiled only with the Go
`e2e` build tag. Ordinary production/release builds compile
`internal/e2e/stub.go`; a build-constraint test verifies that
`internal/e2e/server.go` is absent from that file set. Even in an E2E build the
server remains disabled unless `KOYORI_IDE_E2E=1` is present.

When enabled, it listens on an ephemeral IPv4 loopback port. The harness
generates a 256-bit bearer token and every authenticated request rotates the
token; replaying the previous token returns HTTP 401. The handshake file
contains the loopback URL and PID, never the token.

`kill-restart-recovery` journals an unsaved buffer, kills the packaged process,
relaunches with the same isolated configuration directory, and requires a clean
recovery candidate.

### G24 packaged evidence

The `extension-host-g24-package` result records:

- v1 package SHA-256
  `f9bfd0c7220088eae58d4770a69e308df58f7def8b1e8aff266419c76d3f4a12`
  and v2 package SHA-256
  `b10304f4d8d609e1232a1b9ec8df69b7859f23cde70f54ed8d3796c240586cc9`,
  with both versions activated;
- ABI fallback, incompatible ABI rejection, permission denial, and forged
  protocol messages ignored;
- host recovery after crash, heartbeat hang, message-rate overflow, and
  message-size overflow;
- disable lifecycle handshake completion, uninstall verification, zero
  remaining installed G24 packages, and `editSaveAfterFaults=true`.

These are synthetic packaged lifecycle and fault-isolation checks. They do not
prove production reliability or general extension compatibility.

## Running the packaged harness

```bash
node scripts/packaged-e2e.mjs --dry-run  # source plan only; no artifact launch
node scripts/packaged-e2e.mjs            # build, launch, and run 24 fixtures
KOYORI_IDE_E2E_SKIP_BUILD=1 node scripts/packaged-e2e.mjs  # reuse bin artifact (no Go/Vite rebuild)
```

A true skip-build run does not rebuild either Go or Vite output. It must verify
the existing artifact and must find all five required renderer probe markers in
the existing `frontend/dist` before launch:

```text
__koyoriIdeRunG10MonacoProbe
__koyoriIdeRunG13ExtensionApiProbe
__koyoriIdeRunG15TestExplorerProbe
__koyoriIdeRunTerminalReconnectProbe
__koyoriIdeRunG24ExtensionHostProbe
```

The retained skip-build log records artifact reuse, `verified 5 E2E renderer
markers in dist`, the artifact SHA-256 above, and 24/24 passed. Marker presence
qualifies reuse of that test build; it is not source recompilation or a release
artifact check.

The harness temporarily builds `frontend/dist` with an E2E marker. After a
packaged run, rebuild the ordinary production frontend before release work:

```bash
cd frontend
npm run build
```

### G24 Extension Host corpus report (AC3)

The G24 corpus report is generated from the real Open VSX corpus retained by
G20. It records identity/version/hash, entrypoint, activation events,
contributes summary, detected vscode API references, and a disposition that
never equates installation with activation success:

```bash
node scripts/g24-corpus-report.mjs
node --test scripts/g24-corpus-report.test.mjs  # success/corrupt/entrypoint/API/permission/duplicate/empty cases
```

Output: `build/e2e-evidence/p9-g24/corpus-report.json`.

The current real corpus result is 10/10 `blocked` because every package lacks a
`koyoriIde.permissions` declaration. This demonstrates fail-closed policy. It
does **not** mean 10/10 compatible, activated, or successful; no corpus package
has produced activation evidence.

## CI qualification and platform status

The Linux job remains `workflow_dispatch`-only until three consecutive runs on
three distinct commits pass and retain manifests. Source tests and `--dry-run`
do not count. No qualifying CI run IDs exist in this workspace.

| Platform | Status |
|---|---|
| Windows x64 | local test-tagged packaged run passed 24/24; retained hash/fingerprint evidence above |
| Linux | source and CI configuration exist; real packaged run `U` |
| macOS | source and CI configuration exist; real packaged run `U` |

Linux CI starts a dedicated loopback-only virtual display before the artifact:

```bash
Xvfb :99 -screen 0 1280x1024x24 -nolisten tcp
DISPLAY=:99 <e2e-tagged-packaged-binary>
```

A WebView startup failure is a failure, not a skip. Linux CI also installs
ImageMagick so the harness can attempt a root-window screenshot on failure;
launch and Xvfb logs are retained even when screenshot capture fails.
