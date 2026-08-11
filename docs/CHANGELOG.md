# Changelog

All notable changes to Koyori IDE are documented here.

Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Versioning follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html),
with the caveat that Koyori IDE is pre-1.0: minor versions may contain breaking
changes.

## Version source of truth

The repository root `VERSION` file is the single authoritative version. Every
other version-bearing artifact is validated against it by
`TestReleaseVersionConsistency` (see `main_test.go`):

| Artifact | Field |
|---|---|
| `VERSION` | file contents (source of truth) |
| `build/config.yml` | `info.version` |
| `frontend/package.json` | `version` |
| `docs/CHANGELOG.md` | most recent released `## [X.Y.Z]` section |

A `vX.Y.Z` git tag must equal `VERSION`. The release workflow fails on
mismatch rather than publishing an artifact whose version disagrees with its
tag.

---

## [Unreleased]

### Added

- Shared `WorkspaceContext` as the single source of truth for the active
  workspace root and generation. Plan, Goal, Diff, and the default executors now
  resolve the live root instead of a constructor-time empty string, and the
  context participates in `AddProject`'s two-phase commit so no holder can
  observe workspace A while another observes B.
- `RecoveryService` — hot-exit journaling for unsaved editor buffers. Dirty
  buffers are written to a per-workspace, per-window journal with atomic `0600`
  writes, quota limits, sensitive-path exclusion, corrupt-record isolation, and
  baseline-hash conflict detection so a changed file on disk is never silently
  overwritten.
- `VERSION` file plus `TestReleaseVersionConsistency` enforcing a single version
  source across `build/config.yml`, `frontend/package.json`, and this changelog.
- `docs/RELEASING.md` and `docs/E2E.md`, which the `check-doc-links` and
  `check-wails-pin` gates require.

### Changed

- Snapshot creation before AI edits is now **fail-closed**. An injected
  `SnapshotService` with an unresolvable workspace root returns an error instead
  of silently skipping, so a Plan step or Goal checkpoint can no longer proceed
  without a recovery point.
- Goal mode autonomous execution is **disabled by default** and labelled a
  prototype in the UI. The bundled executor plans with a fixed string, runs a
  fixed command regardless of that plan, and never evaluates success; running it
  by default presented a convincing-looking iteration sequence that cannot reach
  the user's goal. Enabling it now requires an explicit, warned opt-in.
- `goalSection.hint` in all three locales no longer claims Goal mode "runs
  autonomously (plan→execute→evaluate→adjust)". It states the prototype boundary
  instead.
- `SECURITY.md` supported-version table lists exactly one current release line.
  It previously marked both `0.4.x` and `0.2.x` as current simultaneously, and
  referenced `0.3.x`/`0.5.x` lines with no verifiable tag.

### Fixed

- `defaultSecurityChecker.IsWorkspacePath` returned `true` for every path when
  the workspace root was empty, which is the state bootstrap left it in. Goal
  mode's path boundary was therefore inert. It now fails closed.
- `ResumeGoal` set a goal's status to `running` before delegating to `RunGoal`,
  so a rejection there left the goal stuck in `running` with nothing driving it.
- LSP `$/cancelRequest` was not sent when the initiating write returned a
  context error, because the pending entry had already been removed and
  `cancelRequest` was skipped. The notification is now sent on that path, and it
  no longer blocks the caller waiting for a pipe reader that cannot be scheduled
  until after the caller returns.
- `TestLSP_pathToURI` and `TestIsAllowedShell` asserted Windows-only path and
  shell-whitelist behaviour unconditionally, so they failed on Linux and macOS.
  Both assertions are now platform-guarded.

---

## [0.2.0]

**Status: not verified as released.** This is the version `VERSION` and
`build/config.yml` declare, and the version `README.md` points at for packaged
artifacts. No release date is given because none can be verified: this checkout
has no git history, so no `v0.2.0` tag, commit, or release artifact is available
to confirm. A date will be added when a tag and artifact are confirmed.

This section exists so that `TestReleaseVersionConsistency` and the release
workflow have a section to resolve for the declared version. Pushing a `v0.2.0`
tag will publish these notes; the workflow now fails rather than silently
substituting `[Unreleased]` if the section for a tag is missing.

### Added

- Desktop IDE shell on Wails v3 alpha: editor, terminal, Git, search, LSP
  integration, extension host, and AI panels. See `README.md` for the
  capability boundaries that apply to each.

### Security

- Baseline security posture recorded in `.github/SECURITY.md` (G-SEC-01 through
  G-SEC-12), including backend-enforced command approval, path sandboxing with
  symlink evaluation, encrypted API-key storage, and CSP nonces.

---

## Historical development milestones

The entries below are **development milestones, not verified releases**. This
repository checkout carries no git history, so no tag, commit, or release
artifact can be verified for them. Dates are deliberately omitted rather than
reconstructed: a fabricated release date is worse than an absent one.

Treat every version below as unverified until a tag and release artifact are
confirmed against the upstream repository.

### 0.2.0 — development milestone

Earliest version with packaged artifacts referenced by `README.md` and
`build/config.yml`. This is the version `VERSION` currently declares.

### Unverified version claims

`SECURITY.md` previously referenced `0.3.x`, `0.4.x` (with a `v0.4.0` tag), and
`0.5.x`. None of these are corroborated by a tag, changelog section, or release
artifact in this checkout. They are recorded here as unverified claims rather
than deleted, so that a future maintainer with access to the upstream tag list
can either confirm them or remove them.

[Unreleased]: https://github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/compare/v0.2.0...HEAD
