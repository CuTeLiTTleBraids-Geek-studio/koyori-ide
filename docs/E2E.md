# End-to-end testing

## Current state, stated precisely

| Layer                                | What it actually runs                                                                                                           | Evidence level                                       |
| ------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------- |
| `scripts/contract-smoke.mjs`         | Node drives file-service semantics plus one mocked-Wails store test                                                             | contract smoke only                                  |
| `scripts/packaged-e2e.mjs --dry-run` | Validates the 24-fixture source plan and Wails pin without launching an artifact                                                | source validation only                               |
| `scripts/packaged-e2e.mjs`           | Builds an `e2e`-tagged Koyori IDE artifact, launches it, and drives real backend services through a loopback-only test endpoint | packaged integration after a retained successful run |

The contract smoke and packaged harness are separate programs and must not be
conflated. `contract-smoke.mjs` does not launch a binary or WebView and cannot
prove packaging, binding, process-recovery, or native-runtime behavior.

## Current Windows packaged evidence (P13-G05)

`build/e2e-evidence/packaged-e2e/manifest.json` is a **partial, non-authoritative**
run interrupted during fixtures. It is **not** a 24/24 P-level result and must
not be cited as one.

- status: `running`; phase: `fixtures`
- fixtures: 11 passed, 13 not-run (stopped before `git-rebase-package`)
- recordedAt: `2026-08-22T05:10:26.150Z`
- artifact: `bin/koyori-ide.exe`
- artifact SHA-256: `ef0891ebc0e6b4efc1e892b3a12b49fbe7639bebec4d073ccc9ca7550c6c80a4`
- `artifactReused`: false
- HEAD: `18b43cf0825f1e280dc56b54563c8f73506bbd36`
- `gitMetadataAvailable`: true; `workingTreeDirty`: true
- git porcelain SHA-256: `af69540ef46816e85fc0bc78fb3c5513415d9df43e3bc0480de2cd40695c01cd`
- source fingerprint (`build-inputs-v2`, 1054 files):
  `bc677b18d6d0584f03cae2474224eeb631fc592a8150c9fa77c46e119f5d11f8`
- Wails CLI: `v3.0.0-alpha2.111`
- build tags: `desktop`, `production`, `e2e`
- screenshot: not retained (`null`)

The harness now binds a dirty working tree (`workingTreeDirty` +
`gitStatusSha256` + source fingerprint). A HEAD SHA alone is not enough.
This session stopped further `wails3 build` / packaged rebuilds because they
crash the local DSH web GUI. Historical 24/24 SHA values from prompt-12 are
not current evidence.

`node scripts/packaged-e2e.mjs --verify-evidence` currently exits nonzero at
`status=running`, as required. The verifier is read-only: it does not build or
launch an artifact and cannot upgrade this partial run to `P`.

This workspace **has local git** (commits and a `beta0.2.0` tag) but **no
verified formal `v0.2.0` GitHub Release**. Packaged E2E artifacts are
test-tagged and must not be described as a formal release.

## Toolchain pin

Packaged E2E requires the same Wails CLI version as the library in `go.mod`:

```bash
go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-alpha2.111
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

| Stage                                                                                                    | Status      |
| -------------------------------------------------------------------------------------------------------- | ----------- |
| Verify the installed `wails3` CLI against the `go.mod` pin                                               | implemented |
| Build the frontend with the E2E Monaco marker                                                            | implemented |
| Build with `wails3 build -tags desktop,production,e2e`                                                   | implemented |
| Record artifact hash, source commit when available, otherwise source fingerprint                         | implemented |
| Launch the native artifact and authenticate a loopback-only driver                                       | implemented |
| Capture runner metadata, logs, screenshot metadata, fixture results, and goal-specific evidence          | implemented |
| Kill and restart the artifact to verify recovery                                                         | implemented |
| Verify retained fresh Windows evidence against the current artifact and source tree without launching it | implemented |

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

```powershell
node scripts/packaged-e2e.mjs --dry-run        # source plan only; no artifact launch
node scripts/packaged-e2e.mjs                  # fresh build, launch, and 24 fixtures
node scripts/packaged-e2e.mjs --verify-evidence # read-only qualification check
$env:KOYORI_IDE_E2E_SKIP_BUILD = "1"
node scripts/packaged-e2e.mjs                  # strict artifact reuse; not fresh-build qualification
```

### Independent Windows x64 qualification checklist

Run this only in an independent Windows x64 GUI environment with WebView2. Do
not run it from the current DSH Web GUI process. The runner needs Go 1.25 or
newer, Node 20, npm, Git, and the pinned tools below. NSIS is not required
because this harness qualifies the test-tagged desktop executable, not an
installer or release artifact.

```powershell
Set-Location <exact-source-checkout>
$env:Path = "$(go env GOPATH)\bin;$env:Path"
go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-alpha2.111
go install golang.org/x/tools/gopls@v0.21.1
go install github.com/go-delve/delve/cmd/dlv@v1.27.1
npm.cmd --prefix frontend ci --registry=https://registry.npmjs.org

go version
node --version
wails3 version
gopls version
dlv version
git --version

node --test scripts/packaged-e2e-driver.test.mjs
node scripts/packaged-e2e.mjs --dry-run
Remove-Item Env:KOYORI_IDE_E2E_SKIP_BUILD -ErrorAction SilentlyContinue
$evidenceDir = "build/e2e-evidence/packaged-e2e"
New-Item -ItemType Directory -Force -Path $evidenceDir | Out-Null
node scripts/packaged-e2e.mjs 2>&1 |
  Tee-Object -FilePath "$evidenceDir/fresh-run.log"
$freshRunExit = $LASTEXITCODE
if ($freshRunExit -ne 0) { exit $freshRunExit }
node scripts/packaged-e2e.mjs --verify-evidence
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
```

The final verifier must itself run on Windows x64 and requires
`status=passed`, `phase=complete`, a fresh build (`artifactReused=false`), all
24 ordered fixtures passed, the exact Wails pin and build tags, stable source
fingerprint, and matching Git availability/state. The canonical artifact must
be a PE32+ AMD64 executable with the retained SHA-256. Evidence and artifact
path components may not be symlinks or junctions that resolve outside the
checkout.

One 256-bit `runId` binds the manifest, retained fresh harness log, both launch
logs, and both loopback-only token-free handshakes. The fresh log may be UTF-8
or BOM-tagged UTF-16 as emitted by Windows PowerShell `Tee-Object`; it must bind
the Wails build, artifact SHA, both launch PID/URLs, and 24/24 completion. Each
launch log must bind its handshake endpoint and the same `runId`.

The verifier rejects partial, reused, stale, drifted, symlinked, cross-run, or
secret-bearing evidence. Retain the exact source checkout,
`bin/koyori-ide.exe`, and the whole
`build/e2e-evidence/packaged-e2e/` directory before another run overwrites the
manifest. A passing verifier establishes only the named test-tagged Windows
workflow; it is not an NSIS test, release artifact, signature, or GitHub
Release.

A true skip-build run does not rebuild either Go or Vite output. It is allowed
only when the previous packaged manifest records a source fingerprint verified
after its build, and the current source scope, fingerprint, file count,
artifact path and SHA-256, Wails version, and build tags all match that
manifest. It must also find all seven required renderer probe markers in the
existing `frontend/dist` before launch:

```text
__koyoriIdeRunG10MonacoProbe
__koyoriIdeRunG13ExtensionApiProbe
__koyoriIdeRunG15TestExplorerProbe
__koyoriIdeRunTerminalReconnectProbe
__koyoriIdeRunG24ExtensionHostProbe
__koyoriIdeRunAgentToolRoundProbe
__koyoriIdeRunConversationHandoffProbe
```

The retained skip-build manifest records artifact reuse and the source
manifest timestamp. The harness checks the source fingerprint again after the
fixtures. Renderer marker presence alone never qualifies an artifact for
reuse; a skip-build run is still not source recompilation or a release artifact
check.

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

| Platform    | Status                                                                                |
| ----------- | ------------------------------------------------------------------------------------- |
| Windows x64 | current authoritative manifest is partial (11/24); refreshed packaged evidence is `U` |
| Linux       | source and CI configuration exist; real packaged run `U`                              |
| macOS       | source and CI configuration exist; real packaged run `U`                              |

Linux CI starts a dedicated loopback-only virtual display before the artifact:

```bash
Xvfb :99 -screen 0 1280x1024x24 -nolisten tcp
DISPLAY=:99 <e2e-tagged-packaged-binary>
```

A WebView startup failure is a failure, not a skip. Linux CI also installs
ImageMagick so the harness can attempt a root-window screenshot on failure;
launch and Xvfb logs are retained even when screenshot capture fails.
