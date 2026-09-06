# Versioned Extension Contribution Protocol

> **Split status.** The G24 Worker ABI and lifecycle/fault-isolation path below
> are implemented and have historical Windows packaged evidence. The E0-E5
> contribution envelope remains a design draft: there is still no implemented
> versioned contribution schema or E0-E5 enforcement. The historical packaged
> result does not make existing VSIX packages generally compatible or
> production-ready.

## Current boundary

- `PluginManifest` supports a small unversioned `contributes` shape for commands
  and views. Activation ownership and rollback have implementation tests, but
  there is no negotiated protocol version.
- The Worker extension host exposes only the allow-listed methods documented in
  [Extension API Compatibility Matrix](EXTENSION-COMPATIBILITY.md). Missing APIs
  fail closed; most real VS Code extensions will not work.
- Existing `trusted`, `reviewed`, and `restricted` security levels are not the
  E0-E5 model below. Mapping them in documentation does not implement it.
- Generic extension-contributed DAP adapters, test controllers, filesystem
  providers, authentication providers, notebooks, and native modules are not
  supported.

## G24 worker ABI (implemented, 2026-08-10)

The dedicated Worker extension host (`frontend/src/lib/vscodeExtensionActivation.ts`)
negotiates a concrete ABI before any extension code runs:

- `protocol-ready` / `protocol-error` handshake on a `1.0` protocol version;
  the host refuses every message before negotiation and rejects unknown or
  renegotiated versions.
- A per-Worker random protocol token authenticates every RPC; forged tokens are
  ignored.
- A heartbeat watchdog (2s interval / 8s timeout) terminates unresponsive
  Workers and routes them into the crash-recovery path.
- Message quotas fail closed: 4 MiB per message and 1000 messages/second.
- The Worker bridges its global `error` event as an authenticated
  `runtime-error` message before closing, so an uncaught exception terminates
  the runtime even when the hosting WebView does not forward `Worker.onerror`.
- Crash, hang, rate, and size faults all enter the same recovery path; a
  successful restart clears the consecutive-failure counter so an unrelated
  later fault does not permanently disable the extension.

This is a runtime protocol, distinct from the E0-E5 contribution envelope
below. It does not implement the versioned contribution schema proposed in this
document.

### Historical G24 packaged evidence (2026-08-11)

The 2026-08-11 historical Windows x64 run recorded `status=passed` and 24/24
fixtures for a `desktop,production,e2e`-tagged artifact. The tested artifact
SHA-256 was
`7e8abff533098129f6cf858dd9278053c71786edbf5858a6003452589f07b181` and its
source fingerprint was
`690aa31cad880bf803037ab734207a9e1f7281d9e05140f65daaef15bd7b6180`.
The workspace had no Git metadata, so the fingerprint was not a commit or CI
attestation. The current authoritative
`build/e2e-evidence/packaged-e2e/manifest.json` has since overwritten that
record and is partial (11/24); it does not satisfy current-code qualification
or G40-AC6.

The `extension-host-g24-package` fixture retains the following concrete
evidence:

- v1 package hash
  `f9bfd0c7220088eae58d4770a69e308df58f7def8b1e8aff266419c76d3f4a12`
  activated as `1.0.0`; v2 package hash
  `b10304f4d8d609e1232a1b9ec8df69b7859f23cde70f54ed8d3796c240586cc9`
  activated as `2.0.0`.
- ABI fallback activated and an incompatible ABI was rejected. Permission
  denial succeeded, and a forged protocol message was ignored.
- Worker crash, heartbeat hang, message-rate overflow, and message-size
  overflow each entered recovery and restarted the host.
- Disable completed the renderer/backend lifecycle handshake: the extension
  was inactive and its command registration was gone. Uninstall then removed
  the installed manifest and renderer activation state; `remainingInstalled`
  was zero.
- `editSaveAfterFaults=true` proves that the packaged IDE could edit and save
  after all four injected host faults.

The final run was a true skip-build run: `KOYORI_IDE_E2E_SKIP_BUILD=1` reused
the existing artifact and first required all five renderer markers in
`frontend/dist`: `__koyoriIdeRunG10MonacoProbe`,
`__koyoriIdeRunG13ExtensionApiProbe`,
`__koyoriIdeRunG15TestExplorerProbe`,
`__koyoriIdeRunTerminalReconnectProbe`, and
`__koyoriIdeRunG24ExtensionHostProbe`. This validates that the reused binary's
embedded frontend had the required test probes; it is not a rebuild.

This evidence qualifies the named Windows packaged G24 workflow only. It does
not establish release-artifact safety, production reliability, cross-platform
behavior, the proposed E0-E5 contribution protocol, or general VS Code/VSIX
compatibility.

## Goals and non-goals

The protocol should give every contribution a version, owner, risk class,
permission set, host placement, lifecycle, and atomic rollback behavior. Core
code must be able to reject an unknown contribution before activating extension
code.

It is not a VS Code compatibility layer, does not accept proposed VS Code APIs,
and does not let a manifest register arbitrary JavaScript objects or backend
services. E5 remains unsupported until a separately reviewed native-host model
exists.

## Protocol envelope

```json
{
  "protocolVersion": "1.0",
  "extension": {
    "id": "publisher.name",
    "version": "1.2.3",
    "packageSha256": "<hex>"
  },
  "contributions": [{
    "id": "publisher.name.command.example",
    "kind": "command",
    "schemaVersion": "1.0",
    "riskClass": "E1",
    "permissions": ["ui.notifications"],
    "placement": "uiHost",
    "activation": ["onCommand:publisher.name.example"],
    "payload": {}
  }]
}
```

The schema is closed. IDs are globally namespaced under the exact package
identity and cannot use reserved `koyori-ide.*` or `workbench.*` namespaces.
Package hash, extension version, protocol version, contribution IDs, risk
class, permissions, and placement are immutable activation identity.

Unknown major protocol/schema versions, kinds, permissions, placements, fields,
or activation events are rejected. Minor support is explicit feature
negotiation, never “ignore what is unknown.”

## E0-E5 risk classes

| Class | Intended contribution | Permission ceiling | Initial policy |
|---|---|---|---|
| `E0` | Pure declarative metadata: themes, icons, snippets, language IDs | no workspace/process/network access | eligible for schema-only registration |
| `E1` | UI commands, menus, keybindings, messages, static views | UI-scoped APIs; no workspace content | maps roughly to part of current `trusted` |
| `E2` | Read-only workspace/language providers, symbols, diagnostics | `fs.read`, read-only SCM, bounded provider callbacks | explicit install grant |
| `E3` | Workspace edits, save, source control mutations, tasks | revision-bound `fs.write`/`scm.write`/named tasks | maps roughly to current `reviewed`; per-extension grant |
| `E4` | Process, terminal, network, secrets, webview, debug launch | restricted APIs plus per-operation native approval/capability | maps roughly to current `restricted`; default disabled |
| `E5` | Native code, raw host IPC, arbitrary adapter/server, kernel/device/global OS access | beyond extension sandbox | unsupported and quarantined in v1 |

Risk is monotonic: a contribution's effective class is the maximum of its kind,
declared permissions, activation behavior, payload, and placement. A manifest
cannot self-assign a lower class. Core policy owns classification; mismatch is
an install error. E0-E5 does not replace backend per-operation approval.

## Proposed contribution kinds

The first protocol may standardize only already bounded surfaces:

- `command`, `menu`, `keybinding`, `view`, `theme`, `iconTheme`, `snippet`
- `language`, `grammar`, and declarative language configuration
- read-only language/provider registrations backed by the current Worker RPC
- named task definitions that still require Task broker policy

The following remain reserved until their brokers and security contracts exist:

- language server packs (owned by the Language Pack protocol)
- debug adapters and test controllers (owned by Host Debug/Test brokers)
- filesystem/remote/SCM providers, authentication, notebooks, custom editors
- native modules, arbitrary child processes, raw sockets, and Wails bindings

A reserved kind in a package is rejected, not silently skipped.

## Placement and remote workspaces

`uiHost` contributions render bounded UI and never receive raw workspace paths.
`workspaceHost` contributions require the Unified Host protocol and execute
through a broker on the workspace host. `either` is not allowed in v1 because
it makes security and state placement ambiguous.

URI-bearing payloads use `WorkspaceRef` URIs. A local UI extension cannot turn a
remote URI into a local path or proxy an unapproved process. Contributions that
need language servers, debug adapters, or tests call their typed brokers rather
than spawning executable paths.

## Registration transaction

Installation and activation are separate transactions:

1. Stage and hash package; reject traversal, links, bombs, invalid manifest,
   blacklist, downgrade, and incompatible engine/protocol.
2. Parse contributions with size/count/depth limits and compute risk classes.
3. Show native permission/risk review. Renderer booleans cannot approve.
4. Persist an immutable grant bound to package hash/version and permission set.
5. Build a shadow registry; validate ID ownership and collisions before running
   activation code.
6. Activate in a fresh Worker with bounded time/resources. Buffer registrations
   under the transaction owner.
7. Atomically publish all contributions or publish none. On failure, dispose
   buffered registrations and preserve/reactivate the previous version.

Update, disable, uninstall, crash recovery, and cross-window synchronization use
the same owner identity. A stale disposable/result cannot remove or settle a
replacement contribution. Failed cleanup is visible and blocks re-enable rather
than losing ownership state.

## Invocation and event semantics

Every invocation includes extension/contribution identity, registry epoch,
workspace host/generation when applicable, request ID, deadline, and
cancellation. Responses are size bounded and structured. Events are ordered per
contribution owner; overflow produces `RESYNC_REQUIRED`.

E3/E4 mutations obtain backend capabilities bound to canonical targets,
revisions, operation, host/workspace, generation, package hash, and invocation.
Missing, expired, replayed, cross-target, cross-host, cross-version, or stale
epoch tokens fail.

Secrets are handles in Worker code, not serialized values in manifests, events,
logs, or cross-window storage. Webviews keep unique CSP nonces and opaque
origins; `allow-same-origin` is not granted to untrusted content.

## Compatibility and deprecation

Core publishes a machine-readable supported protocol/kind/schema matrix. A pack
declares an engine range and exact protocol major. Deprecation requires at least
one minor line with a visible warning and migration text; security revocation
may be immediate and must be recorded.

There is no automatic fallback to the current unversioned `contributes` object
for a manifest that opts into v1. Legacy manifests remain on the legacy path,
with current limits, until explicitly migrated and tested.

## Dependencies and migration

1. Freeze and test the existing API allow list and legacy manifest parser.
2. Add a read-only parser/classifier for protocol v1 without activation.
3. Introduce shadow registry and atomic owner transaction for current
   command/view kinds.
4. Map existing `trusted/reviewed/restricted` decisions to policy inputs, not
   aliases, and implement E0-E5 classification tests.
5. Add kinds one at a time only after their backend broker, permissions,
   cleanup, and hostile fixtures pass.
6. Add workspace-host placement only after Unified Host identity and leases.

Language Pack, Debug/Test, SCM, and remote contributions depend on their typed
broker contracts; this protocol must not reimplement those transports.

## Acceptance criteria for implementation

- Closed-schema fixtures reject every unknown/duplicate/reserved identifier,
  incompatible version, forged owner, class downgrade, permission mismatch,
  invalid placement, oversized payload, and unsupported kind.
- E0-E5 table-driven tests prove the core-computed class and permission ceiling;
  E5 always fails before extraction/activation.
- Package fixtures cover hash mismatch, blacklist, zip slip, link escape,
  decompression limits, downgrade, interrupted stage, and rollback.
- Registration fault injection at every phase proves all-or-none publication,
  old-version preservation, deterministic disposal, and no stale-owner race.
- Invocation tests reject token replay/cross-binding and reclaim Worker,
  listener, timer, request, webview, process, and broker resources on every
  cancel/crash/disable/update/uninstall path.
- Multi-window and reconnect tests prove epoch ordering and resync without
  duplicating contributions or leaking grants.
- A curated real-extension corpus reports exact supported/unsupported reasons;
  no compatibility percentage or named-extension claim is made without saved
  results.
- README and the compatibility matrix remain constrained until protocol code,
  packaged runs, and retained evidence satisfy these gates.
