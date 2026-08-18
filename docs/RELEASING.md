# Releasing Koyori IDE

## Version source of truth

The repository root `VERSION` file is authoritative. Nothing else may be edited
independently:

`VERSION` is a stable `X.Y.Z` value with no leading zeroes, prerelease suffix,
or build metadata. To remain representable by MSI, major and minor are at most
255 and patch is at most 65535. Prereleases require a separate workflow because
Windows and Apple package metadata need platform-specific numeric mappings.

| Artifact | Field | Enforced by |
|---|---|---|
| `VERSION` | file contents | — (source of truth) |
| `build/config.yml` | `info.version` | `TestReleaseVersionConsistency` |
| `frontend/package.json` | `version` | `TestReleaseVersionConsistency` |
| `build/windows/info.json` | file/product version | `TestPlatformReleaseMetadataMatchesVERSION` |
| `build/linux/nfpm/nfpm.yaml` | package version | `TestPlatformReleaseMetadataMatchesVERSION` |
| `build/darwin/Info.plist` | bundle versions | `TestPlatformReleaseMetadataMatchesVERSION` |
| `docs/CHANGELOG.md` | latest released `## [X.Y.Z]` | `TestReleaseVersionConsistency` |
| `vX.Y.Z` git tag | stable tag name (prerelease tags are excluded) | release workflow |

Run the gate locally before tagging:

```bash
node scripts/sync-release-metadata.mjs --check
go test ./internal/repo -run 'Version|Release|PlatformReleaseMetadata' -count=1
```

## Toolchain pins

The Wails CLI version must equal the `github.com/wailsapp/wails/v3` version in
`go.mod`. A CLI/library mismatch produces bindings that disagree with the Go
service signatures at runtime. Binding generation therefore uses the
version-addressed repository wrapper rather than whichever `wails3` is on PATH.

```bash
go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.8
node scripts/generate-bindings.mjs
node scripts/check-bindings.mjs
```

`scripts/check-wails-pin.mjs` fails the build when `go.mod`, the CI workflows,
the binding manifest or this document disagree, and when any operational
binding entry point bypasses the pinned generator.

Other pins:

- Go toolchain `1.26.6` (the module minimum remains Go `1.25`)
- Node `20.19+` (or `22.12+`)
- Syft release image `anchore/syft:v1.29.0@sha256:e86b0ba0b1d2fe8a2e9f96ed9b22033df9781f43b9a7eb27c57e6c89234946bc`

Release tags are intentionally stable `vX.Y.Z` tags. The workflow trigger
excludes `vX.Y.Z-*` and `vX.Y.Z+*`, and the first verification step rejects any
suffix or metadata drift. Do not use a prerelease tag for this workflow; create
a separate prerelease workflow with its own metadata and signing policy
instead.

## License review

`NOTICE` summarizes the third-party notice policy. The dependency-level record
is generated from the selected Go module graph and
`frontend/package-lock.json`:

```bash
node scripts/generate-license-inventory.mjs
node scripts/generate-license-inventory.mjs --check
node scripts/generate-license-inventory.mjs --full-check
```

`--check` is the network-independent CI gate: it verifies that the committed
inventory embeds the current `go.mod`, `go.sum`, and package-lock SHA-256
digests. `--full-check` rebuilds every table, rejects every `UNKNOWN` or
`UNRESOLVED` row, and compares the complete file; it requires the selected Go
module graph and license sources to be available from cache or the network.
The tag release job uses `--full-check`; record `U` rather than substituting
the digest check when external module metadata cannot be fetched.

The current inventory has zero unresolved licenses across the union of the
four supported `desktop,production` Go package closures and the frontend lock
file. The pinned Wails module graph also contains
`github.com/konoui/go-qsort@v0.1.0`, whose source archive has no detected
license, but the module is not imported by any supported production target;
the generator records the production-closure boundary and will fail if a
future target imports it. A zero GPL/AGPL count applies only to the classified
inventory and is not a legal conclusion about unrelated, unused modules.

Release assets have a separate fail-closed boundary check:

```bash
node scripts/check-release-assets.mjs
node scripts/check-release-assets.mjs --check --require-dist
```

The first command verifies the public asset allowlist, pinned Wails Vue
template hashes, native icon inputs, and the generated attribution record. The
second command is run after the frontend is built; it verifies that `dist`
contains exactly the allowlisted public files and one generated Monaco Codicon
font. Unused public files are removed rather than silently distributed. The
asset record is not a legal opinion; it preserves the source, digest, and
license evidence needed for a release review.

An attempted independent scan with
`go run github.com/google/go-licenses@v1.6.0` could not complete because
`proxy.golang.org` timed out. That result is `U`, not a successful second-tool
audit. Retry it when network access is available and retain its output with the
release evidence.

## Release artifact matrix

The tag workflow passes each matrix architecture through `GOARCH`; it does not
infer the target from the hosted runner. Packaging accepts exactly one build
product per job and fails on a missing or ambiguous product.

| Runner target | Release artifact | Archive format |
|---|---|---|
| Windows amd64 | `koyori-ide-vX.Y.Z-windows-amd64.zip` | PowerShell ZIP |
| Linux amd64 | `koyori-ide-vX.Y.Z-linux-amd64.tar.gz` | gzip-compressed tar |
| macOS amd64 (`macos-15-intel`) | `koyori-ide-vX.Y.Z-darwin-amd64.zip` | ZIP created by `ditto`; single `x86_64` slice asserted |
| macOS arm64 (`macos-15`) | `koyori-ide-vX.Y.Z-darwin-arm64.zip` | ZIP created by `ditto`; single `arm64` slice asserted |

Before building, the workflow compares the tag and `VERSION` with
`build/config.yml`, `build/windows/info.json`,
`build/linux/nfpm/nfpm.yaml`, and `build/darwin/Info.plist`. Any missing,
duplicate, or different version field fails the release before an artifact is
created.

The tag-triggered workflow has not been run locally: YAML contract tests and
shell syntax checks are source-level evidence only. A real tag build, signing,
notarization, and the four hosted-runner artifacts remain unverified until a
workflow run URL is recorded.

Before any platform build, the `quality-gate` job verifies the immutable
annotated tag object, peels it to the exact target commit, and requires the tag
object or that direct commit to match the tag push event's `GITHUB_SHA`. This
handles both annotated-tag event representations without treating a tag object
as a commit. The gate publishes the independently peeled SHA as a job output
consumed by every portable build checkout, the reusable installer caller, and
the final publisher checkout/revalidation; consumers never use the event field
as their checkout ref. It then resolves the newest
`push` run of the repository's `.github/workflows/ci.yml` on `main` for that
exact commit and reads every required job from one immutable run ID and attempt.
Each job's check run must belong to the same check suite and the
`github-actions` App. A third-party same-name check, a job from another
workflow, or successful jobs spliced across multiple runs cannot satisfy the
gate. It requires successful CI jobs for contract
smoke, bindings, all three Go build/test legs, Go lint, all three frontend
check/test legs, frontend coverage, the production Wails build, govulncheck,
the benchmark gate, and npm audit. A missing or stale same-commit check fails
the release; it is not sufficient that the branch passed checks on a different
commit. The gate waits for the authoritative run and its in-flight jobs for up
to 10 minutes and fails closed on missing, ambiguous, foreign, or unsuccessful
jobs.

After the same-commit CI gate passes, a separate `release-packaged-e2e` job
checks out only the peeled commit and launches the real packaged Linux desktop
artifact through `scripts/packaged-e2e.mjs`. It has no signing environment or
secrets, uploads its evidence even on failure, and blocks every portable build
and the final publisher. The manual CI qualification job remains accurately
labelled as such; a tag release does not treat its source-level checks as
packaged execution evidence.

The reusable installer validator independently fetches and verifies the signed
annotated tag, emits its peeled 40-character commit as a job output, and every
build checkout uses only that output. The optional manual `expected_commit`
input is normalized and validated as a full SHA before the validator's bootstrap
checkout, then enforced again as an equality constraint; it is never a mutable
tag or branch fallback.
The final publisher repeats the signed-tag and peeled-commit check immediately
before creating the Release.

## Installer artifacts (`.github/workflows/package.yml`)

The `Package Desktop Apps` workflow produces native installers for all three
desktop platforms on push to `main`/`master` (path-filtered) or via
`workflow_dispatch`. Installers are uploaded as workflow artifacts; they are
not attached to GitHub Releases. A shared preflight first requires an exact,
single-line stable `VERSION` and runs `sync-release-metadata.mjs --check`; every
platform job depends on that preflight, so a new package name cannot be combined
with stale executable, installer, or bundle metadata. The platform build scripts
repeat the metadata check for direct local invocations.

| Platform | Installer | Produced by |
|---|---|---|
| Windows amd64 | `koyori-ide-vX.Y.Z-windows-amd64.msi` (WiX v3) | `build/scripts/build-windows.ps1` + `build/scripts/build-msi.ps1` |
| Linux amd64 / arm64 | portable `.tar.gz` (binary + AppImage) + `.deb` + `.rpm` | `build/scripts/build-linux.sh` |
| macOS arm64 / amd64 | portable `.tar.gz` (`.app` + binary) + `.dmg` | `build/scripts/build-macos.sh` |

The workflow wraps executable files and macOS app bundles in tar archives before
calling `upload-artifact`, because the artifact service does not preserve Unix
mode bits or symlinks. The archive is part of the workflow artifact only; the
tag-triggered Release keeps its separate signed asset layout below.

### Building installers for a GitHub Release

The tag-triggered `Release` workflow calls the reusable
(`.github/workflows/release-installers.yml`) workflow **before** creating the
GitHub Release. Installer jobs upload Actions artifacts only; the final Release
job verifies all portable and installer artifacts, checksums, SBOMs, and
provenance, then publishes them once. Immediately before publishing it requires
the tag lookup to return HTTP 404; `gh release create` then uses the create-only
API and fails if another run wins the race. There is no `release: published`
listener, update action, or `--clobber` path, so a failed or rerun build cannot
merge new files with an existing Release's stale assets:

| Platform | Release asset added |
|---|---|
| Windows amd64 | `koyori-ide-vX.Y.Z-windows-amd64.msi` + `.sha256` |
| Linux amd64 / arm64 | `.AppImage` / `.deb` / `.rpm` + `.sha256` |
| macOS arm64 / amd64 | `.dmg` + `.sha256` |

Tag-triggered production releases always require Windows and macOS signing;
the caller passes the literal boolean `true`, and no repository variable can
lower it. A manual installer run may explicitly select `false`, while an
environment `REQUIRE_CODE_SIGN=true` can still raise that manual policy. The
only accepted environment values are the exact strings `true`, `false`, or
unset; invalid values fail closed, and environment `false` never overrides a
true input.

Portable and installer builds run without the `release` Environment or signing
secrets. They upload opaque unsigned intermediate payloads, and fresh protected
signing jobs download only those payloads. Windows signs the EXE before a fresh
unprivileged job embeds it into the MSI, then a second fresh protected job signs
the MSI. macOS signs the downloaded app before creating, notarizing, and
stapling the DMG. Signing secrets are stored in the `release` Environment and
exposed only to the relevant signing steps; protected signer jobs do not check
out or execute repository build scripts. The caller does not forward
repository-level signing secrets. Linux installers stay unsigned. These
installer assets are supplementary to the portable zip/tar.gz artifacts and
their SBOM/provenance set.

Unsigned/signed intermediate Actions artifacts use staging-only names. The
final publisher downloads only `release-*` artifacts, so raw EXE, app bundle,
and MSI payloads cannot be mistaken for finished release subjects.

Local build commands mirror the CI jobs:

```bash
# Windows (run in PowerShell on Windows; requires Go/Node/wails3/WiX v3)
powershell -ExecutionPolicy Bypass -File build/scripts/build-windows.ps1 -Arch amd64 -SkipNSIS
powershell -ExecutionPolicy Bypass -File build/scripts/build-msi.ps1 -Arch amd64
#   NSIS (legacy) instead of MSI:        drop -SkipNSIS; do not run build-msi.ps1

# Linux (run on Linux; requires nfpm, linuxdeploy fetched automatically)
./build/scripts/build-linux.sh amd64

# macOS (run on macOS; requires create-dmg)
./build/scripts/build-macos.sh arm64
```

The Linux release script prefers GTK4/WebKitGTK 6.0 and falls back to the
`gtk3` build tag with GTK3/WebKit2GTK 4.1. It generates
`build/linux/nfpm/koyori-ide.yaml` after detecting the selected stack, so deb,
rpm, and Arch dependency names describe the ABI used by the binary. The
checked-in `build/linux/nfpm/nfpm.yaml` is the GTK4 default for direct Wails
Taskfile packaging and must not be used for a binary built with the `gtk3` tag.

The Windows build script produces the GUI executable (icon/version `syso`
via `wails3 generate syso`, then `go build -tags production -H=windowsgui`).
The MSI is compiled separately by `build/scripts/build-msi.ps1` with the WiX v3
toolset (`candle` + `light`); NSIS packaging is skipped with `-SkipNSIS`.
Installer signing follows the same code-signing secrets policy as the tag
workflow (`REQUIRE_CODE_SIGN` / `WINDOWS_CERT_*` / `MACOS_CERT_*`).

## Release steps

1. Decide the new version. Koyori IDE is pre-1.0, so a minor bump may contain
   breaking changes; say so in the changelog rather than implying SemVer
   compatibility that does not hold.

2. Update `VERSION`, then generate and verify every mirrored metadata file:

   ```bash
   # Edit VERSION first. The sync command fails closed on an unexpected file shape.
   node scripts/sync-release-metadata.mjs
   node scripts/sync-release-metadata.mjs --check
   go test ./internal/repo -run 'Version|Release|PlatformReleaseMetadata' -count=1
   ```

3. Promote the `## [Unreleased]` changelog section to `## [X.Y.Z] - YYYY-MM-DD`
   using the **actual** date the artifacts are published. Do not backfill a date
   for a release that has not happened.

4. Update `.github/SECURITY.md` so exactly one line is described as current.
   Multiple simultaneous "current" lines make the support policy unverifiable.

5. Run the full gate set:

   ```bash
   (cd frontend && npm ci --registry=https://registry.npmjs.org && npm test && npx vue-tsc --noEmit && npm run lint && npm run build)
   node scripts/backend-gate.mjs
   node scripts/npm-audit-gate.mjs
   node scripts/check-release-assets.mjs --check --require-dist
   node scripts/generate-license-inventory.mjs --check
   node scripts/generate-license-inventory.mjs --full-check
   node --test scripts/generate-release-provenance.test.mjs
   ```

6. Create a signed annotated tag and push it. The tag must equal `VERSION`
   exactly, including the absence of a leading `v` inside the file (the tag
   carries the `v`, the file does not). GitHub must report the tag signature as
   verified; a lightweight, unsigned, or unverified tag fails before building.

   ```bash
   git tag -s "v$(cat VERSION)" -m "Koyori IDE v$(cat VERSION)"
   git verify-tag "v$(cat VERSION)"
   git push origin "v$(cat VERSION)"
   ```

7. The release workflow builds per-platform artifacts, extracts the changelog
   section matching the tag, and requires all of the following before the
   GitHub Release step: a current license inventory, a non-empty parseable SPDX
   JSON SBOM, an unsigned in-toto/SLSA-shaped provenance statement, and final
   SHA-256 checksums covering every attached asset. Any missing item fails the
   release. The workflow also **fails** when no changelog section matches the
   tag; it must not fall back to `Unreleased`, because publishing "unreleased"
   notes as release notes misrepresents what shipped.

## SBOM and provenance boundary

Tag releases generate one `*.sbom.spdx.json` through
`scripts/generate-sbom.sh` for each of the 13 executable/installer artifacts:
the portable ZIP/tar archives and native MSI/AppImage/deb/rpm/DMG installers.
LICENSE, NOTICE, inventories, provenance, and checksum metadata are not
executable subjects and therefore do not receive an SBOM. The script scans
each artifact itself (`file:<artifact>`), not the checkout directory, so each
SPDX document is bound to the artifact it describes. The release workflow then runs `scripts/check-sbom-artifact.mjs` to
require a unique SPDX file root whose SHA-256 exactly matches that archive; a
parseable but mismatched SBOM fails.
Syft or Docker is mandatory; there is no skip or best-effort path. Local runs
may use an explicit Syft binary, while the tag workflow forces `SYFT_MODE=docker`
and the digest-pinned image above so hosted-runner tool drift cannot change the
generator. The script uses a temporary file and only publishes a non-empty
completed output. The workflow parses all 13 JSON documents before continuing
and includes their digests in the final `SHA256SUMS`.

The workflow also generates `provenance.intoto.jsonl` with subjects for the
release artifacts, legal inventory, and SBOM. Its shape uses in-toto Statement
v1 and the SLSA provenance v1 predicate; this checked-in statement is an
unsigned, human-auditable record. Its resolved Git dependency is the
independently peeled release commit, and generation fails if that commit does
not equal the final publisher checkout's `HEAD`; the annotated tag object's
event SHA is never recorded as a commit. Separately, the final release job requests
`id-token: write` and `attestations: write` and runs
`actions/attest-build-provenance` against the final `SHA256SUMS`. Consumers can
verify that GitHub attestation independently; the unsigned JSON file must not
be described as the attestation itself.

`SHA256SUMS` is regenerated only after the SBOM and provenance exist. It covers
all files in `release-assets`; the workflow fails rather than publishing a
partial checksum set.

## Packaged E2E qualification

The Linux packaged E2E job is currently guarded by `workflow_dispatch`; it is
a qualification job, not a required tag-release gate. Its driver source covers
workspace open, file open/edit/save, terminal, LSP hover/completion, and
SIGKILL/restart recovery, but source coverage and `--dry-run` are not artifact
execution evidence.

An older 2026-08-03 harness record stopped at toolchain setup while the project
still used the pre-release `wails3` pin; that record predates the current
`v3.0.0-beta.8` toolchain and is not evidence for this release. No hosted tag
release artifact run is retained here, so packaged execution remains `U` for
release qualification. Promote the job to required only after three consecutive
successful manual runs on three distinct commits, each with its manifest and
launch evidence retained. Windows and macOS packaged execution remain `U`
independently of a Linux result.

The current tag workflow can publish without that qualification job because it
is a separate manual workflow path. A release record must state this residual
risk; it must not use green source contracts as a packaged E2E claim.

## Release evidence

A release is only claimed complete when all of the following are verifiable by
URL, not by assertion:

- [ ] Workflow run URL for the tag build
- [ ] Attached artifacts for every advertised platform
- [ ] `SHA256SUMS` attached and matching the artifacts
- [ ] `LICENSE`, `NOTICE`, and `THIRD_PARTY_LICENSES.md` attached; all unresolved license
      rows resolved or covered by an owned, dated exception
- [ ] `RELEASE_ASSET_LICENSES.md` attached and matching the checked public and
      native asset inputs
- [ ] Thirteen non-empty, parseable per-artifact `*.sbom.spdx.json` files attached
- [ ] `provenance.intoto.jsonl` attached and described accurately as unsigned
- [ ] GitHub build-provenance attestation verifies against the final
      `SHA256SUMS`
- [ ] Tag is annotated, signed, and shown by GitHub as verified
- [ ] `release` Environment approval/deployment is recorded for signing and
      publishing jobs
- [ ] Signing status recorded explicitly, including "unsigned" when that is the
      truth
- [ ] Packaged E2E status recorded; absent qualification evidence is explicitly
      marked `U`
- [ ] Changelog section matching the tag
- [ ] `SECURITY.md` support table naming this line
- [ ] SLO evidence status recorded (`U` when no qualifying real-release data)
- [ ] External security/supply-chain/accessibility audit status recorded with
      report URLs or the literal statement "not externally audited"

Absent any of these, describe the build as a preview and say which evidence is
missing. Do not describe an unverified build as a release.

## SLO and external audit release inputs

Current status is `U`: Koyori IDE has no production telemetry, real release
cohort, SLO dashboard, agreed reliability threshold, or independent security,
supply-chain, or accessibility audit. Unit/contract tests, CI configuration,
NOTICE, SBOM, and unsigned provenance are engineering evidence; none is SLO
history or an external audit report. This section is a future collection policy,
not an implementation and not consent to collect user data.

### Preconditions before collection

- Define an explicit opt-in or other reviewed lawful basis, a visible off
  control, retention period, deletion path, and privacy/security owner before
  any event leaves the device. Default collection is currently absent.
- Publish a versioned, closed event schema and threat/privacy review. Reject
  unknown fields so new sensitive data is not silently added.
- Never collect source/prompt/output contents, file/workspace/user names, raw
  paths/URIs, repository remotes, command arguments/output, environment values,
  credentials, extension secrets, capability tokens, or host fingerprints.
- Use a random, rotating installation/session identifier. Do not derive an ID
  from hardware, account, file content, repository, host identity, or API key.
- Record release version, commit, channel, OS/arch, event-schema version, and
  coarse failure category. Separate development/test/manual qualification data
  from real release cohorts.
- Define event loss, offline buffering bounds, clock source, upload retry,
  sampling, duplicate suppression, and deletion behavior before interpreting
  any ratio. Missing events must not be treated as successful sessions.

### Proposed instrumentation checklist (not implemented)

| Candidate signal | Minimum events/fields | Required interpretation boundary |
|---|---|---|
| Crash-free sessions | `session.started`, `app.ready`, clean `session.ended`, crash marker; random session ID and release/platform cohort | Requires a defined session denominator and real release history; local test exits do not count |
| Startup reliability/latency | process start and first interactive-ready monotonic timestamps, outcome category | Define “ready,” timeout, cold/warm start, and p50/p95 observation window before setting a target |
| Workspace/edit durability | workspace-open outcome; transaction committed/conflict/rolled-back/rollback-failed counts | Never include paths/content; user conflicts are not automatically product failures |
| Recovery | journal detected; clean/conflict/missing choice; restore/keep/cleanup outcome | Requires packaged crash/restart evidence; never infer data safety from scan count alone |
| Terminal/task/debug/test | approved start, ready, cancel, exit/outcome category | Provider/tool absence and user cancellation are separate from application faults |
| LSP | server kind/version class, start/initialize outcome, request kind, bounded latency/error category | Only real server sessions count; mock/contract tests remain S |
| Remote host | connect/auth/host-key/reconnect/resync outcome and coarse latency | Only after Unified Host implementation and privacy review; no address/host fingerprint collection |
| Update/release | check/download/hash/signature/manual-install outcome | Current E2 flow cannot measure installation success automatically; do not invent it |
| Packaged E2E/CI | commit, runner/platform, artifact digest, fixture result, manifest URL | Qualification evidence, not end-user reliability data; preserve source-versus-artifact distinction |

No numeric objective is defined in this repository. Before adopting one, the
release record must name the metric query/version, denominator and exclusions,
minimum sample decision, observation window, supported release/platform cohort,
owner, alert/error budget policy, dashboard or immutable export, and data
quality limitations. Low/biased samples stay `U`; they are not rounded into an
SLO claim.

### External audit evidence checklist

An external-audit claim requires an independent assessor identity, audit type
and standard/version, exact product version/commit and platform scope, start/end
dates, public report or retained evidence URL, exclusions, findings/severity,
accepted exceptions with owner/expiry, and remediation/retest status.

- Security review must state whether architecture, source, dependencies,
  packaged binaries, dynamic testing, update/signing, and cloud/provider
  boundaries were in scope.
- Supply-chain review must cover build provenance, action/tool/image pins,
  dependency/license process, SBOM completeness, signing/key custody, artifact
  verification, and reproducibility limits. Generating an SBOM is not the audit.
- Accessibility review must name WCAG version/level, platform/WebView matrix,
  keyboard-only paths, screen readers, zoom/reflow, contrast, focus, live
  regions, reduced motion, localization, findings, and retest evidence.

Until those records exist, README and SECURITY must keep all three external
audit rows at `U`. A release checklist records either the evidence URLs or the
literal statement “not externally audited”; silence is not a passing result.

## LSP and debugger release claims

The README links here for the verification status of language and debug
services. Record only what was actually exercised on the verifying machine.

| Service | Local verification status |
|---|---|
| `gopls` (Go) | **Not installed on the verifying machine.** Availability detection and mock protocol paths are covered, but no real server session or transcript exists. |
| `typescript-language-server` / `vtsls` (TypeScript) | **Not installed on the verifying machine.** Initialization options are covered by `TestLSP_A4_TypeScriptInitializationOptions`, which asserts the wire shape against an in-memory peer. That is a contract test, not evidence of a real server session. |
| `basedpyright` / `pyright` (Python) | Detection only. No real session verified. |
| `rust-analyzer` (Rust) | Detection only. No real session verified. |
| Delve DAP (Go) | Built-in adapter present. Coverage is unit-level; a real debug session was not exercised in this verification pass. |
| Node CDP | Narrower built-in path. Generic extension-contributed DAP adapters are unsupported. |

A mock-backed unit test proves the protocol shape we send. It does not prove the
server accepts it. Do not upgrade a detection-only or contract-only row to
"verified" without a transcript from a real server process.

## Downgrade protection

A lower version tag must never overwrite a higher current/supported line.
Before tagging, confirm the new version is greater than the version currently
described as current in `SECURITY.md`.
