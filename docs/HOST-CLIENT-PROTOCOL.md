# Unified Host Client Protocol

> **Design draft, not implemented.** This document locks a proposed P2
> boundary. Koyori IDE currently has only a minimal SSH/SFTP `RemoteService` and
> local-only service paths. There is no unified host client, remote PTY, SCM,
> language, debug, or test broker. Nothing here is a current capability claim.

## Current boundary

- `WorkspaceContext` identifies a local canonical root plus a process-local
  generation. It does not carry a host identity or workspace URI.
- `RemoteService` provides named SSH connections, SFTP file operations, polling
  watch, and separately approved argv execution. Real SSH integration remains
  unverified in the current environment.
- File, terminal, Git, LSP, debug, test, recovery, and snapshot services do not
  share a transport-neutral host interface.
- Disconnect recovery, remote edit transactions, and remote recovery journals
  are not implemented. The editor must not imply otherwise.

## Goals and non-goals

The protocol should make local and remote workspaces use the same explicit
identity, revision, cancellation, and cleanup semantics. It must preserve the
existing fail-closed workspace generation and capability-token boundaries.

This draft does not select a wire encoding, ship an SSH daemon, promise VS Code
Remote-SSH compatibility, or permit renderer-supplied credentials or trust
decisions. Port forwarding, container orchestration, and collaborative editing
are outside the first implementation.

## Identity and negotiation

Every connection starts with a backend-to-host handshake. Unknown major
versions or required capabilities fail closed.

```text
HostHello {
  protocolVersion: "1.0"
  hostId: sha256(host public identity)
  instanceNonce: 256-bit random value
  os: windows | linux | darwin
  arch: amd64 | arm64
  pathStyle: windows | posix
  capabilities: [fs.v1, watch.v1, ...]
  limits: { maxFrameBytes, maxConcurrentRequests, maxWatchBacklog }
}
```

`hostId` is derived from a pinned cryptographic identity, never from a mutable
display name or network address. First trust and changed-host-key decisions are
native backend/UI confirmations. Credentials, tokens, and usernames never
appear in workspace URIs or renderer events.

Workspace identity is a tuple:

```text
WorkspaceRef {
  uri: file:///absolute/path
       | koyori-ide-remote://<hostId>/<workspaceId>/<canonical-path>
  hostId: local | <pinned identity digest>
  workspaceId: host-issued opaque ID
  generation: monotonically increasing lease generation
}
```

The host canonicalizes the root before issuing `workspaceId`. A client-supplied
path is only a request to open; it is never authoritative identity. A URI is
invalid if it contains credentials, dot-segment escape, an unknown host, or a
workspace ID from another connection.

## Common request envelope

All broker calls carry `requestId`, `workspaceId`, `generation`, deadline, and
an optional cancellation ID. Mutations additionally require a backend-issued,
single-use capability bound to the operation, canonical target, precondition,
host instance nonce, and generation. Responses return a typed error code and
never rely on parsing human text.

Required error codes include `UNAUTHENTICATED`, `PERMISSION_DENIED`,
`STALE_GENERATION`, `STALE_REVISION`, `NOT_FOUND`, `DISCONNECTED`,
`CANCELLED`, `DEADLINE_EXCEEDED`, `RESOURCE_EXHAUSTED`, and `UNSUPPORTED`.
Retries are permitted only for operations explicitly marked idempotent.

## Broker surfaces

### File system and watcher

`fs.v1` provides canonical stat/list/read operations plus transactional
mutations. Reads return a content digest and opaque revision. Writes require an
expected revision or an explicit create precondition; blind overwrite is not a
protocol operation.

`ApplyEditTransaction` accepts a bounded set of create/write/rename/delete
edits, validates every path and precondition before mutation, stages content,
and returns either a committed revision set or a structured rollback result.
Partial success must never be reported as success.

`watch.v1` streams monotonically sequenced events scoped to one workspace and
generation. Overflow emits `RESYNC_REQUIRED`; it does not silently drop events.
Reconnect always performs a snapshot/digest reconciliation before incremental
events resume.

### PTY

`pty.v1` defines `Create`, `Input`, `Resize`, `Signal`, `Wait`, and `Close`.
Process creation uses argv arrays and a server-validated workspace CWD. It
requires an approval capability bound to executable, argv, environment allow
list, workspace, and generation. Output frames carry sequence numbers and a
bounded replay cursor. Disconnect does not imply that an unknown process is
safe to reattach; the UI shows its state as unknown until the host proves the
session identity.

### SCM

`scm.v1` exposes repository discovery, status, diff, branch, stage, commit,
worktree, and rebase operations as typed methods. It does not expose a renderer
controlled `git` command string. Repository roots are host-canonical paths
inside approved workspace/safe-root policy, and every mutation has revision or
operation-token preconditions.

### Language broker

`language.v1` launches language servers on the workspace host, not on the UI
machine for a remote workspace. It brokers initialize, document lifecycle,
request/response, progress, cancellation, diagnostics, and shutdown. Server
selection comes from the proposed Language Pack manifest, not a hardcoded
client switch. Raw host paths are translated to workspace URIs at the broker.

### Debug and test brokers

`debug.v1` owns adapter discovery, launch/attach approval, DAP transport, child
process lifetime, and source-path mapping on the workspace host. Project
provided executables remain default-denied.

`test.v1` owns discovery snapshots, stable test IDs, run/cancel, streaming
output, coverage artifacts, and result epochs. Debug and test requests are
bound to the same workspace generation and cannot reuse a launch capability.

### Journal and snapshot

`journal.v1` stores recovery entries keyed by `hostId`, `workspaceId`, window,
file URI, baseline digest, and generation. A journal never automatically
overwrites a changed remote file after reconnect. Clean/conflict/missing
classification remains explicit.

`snapshot.v1` creates an immutable manifest of URI, digest, metadata, and
repository state. Snapshot restore uses `ApplyEditTransaction`; it is not a
recursive copy and cannot escape the current host/workspace identity.

## Disconnect state machine

```text
CONNECTED -> DEGRADED -> DISCONNECTED -> RECONNECTING -> CONNECTED
     |            |            |               |
     +----------> STALE <-------+---------------+
                                  CLOSED (terminal)
```

- `DEGRADED`: reads already in flight may finish; new mutations are refused.
- `DISCONNECTED`: dirty editor buffers stay local and visibly unsynced. No
  mutation, approval, or process launch is queued for automatic replay.
- `RECONNECTING`: repeat identity handshake, compare instance nonce and
  generation, re-open the workspace, reconcile revisions, then resubscribe.
- `STALE`: host identity, workspace ID, generation, or file revision changed.
  User resolution is required; old capabilities and cursors are invalidated.
- `CLOSED`: all listeners, timers, streams, child-process handles, and secrets
  are released. Reuse requires a new connection object.

## Security requirements

- Transport authentication and host identity pinning happen in the backend.
- The renderer receives opaque connection/workspace IDs, never credentials.
- All target paths are canonicalized on the host with symlink/reparse-point
  checks; client normalization is not a security decision.
- Approval capabilities are short-lived, single-use, operation-bound, and
  invalidated on disconnect, generation change, or host nonce change.
- Logs redact credentials, file contents, environment secrets, and capability
  material. Correlation IDs are safe random IDs rather than content hashes.
- Resource limits cover frames, reads, watch backlog, PTYs, child processes,
  language/debug/test sessions, and reconnect attempts.

## Dependencies and migration

1. Introduce a local in-process host adapter whose behavior is identical to
   current local services; no remote claim is added.
2. Extend `WorkspaceContext` to carry `WorkspaceRef` while retaining generation
   invalidation and local-root compatibility during migration.
3. Move file/watch and edit transactions behind the host boundary first.
4. Move PTY and SCM, then Language, Debug, Test, journal, and snapshot brokers.
5. Adapt minimal SSH/SFTP only after real hostile/disconnect integration tests;
   do not wrap the current service and call it complete.

Language Pack depends on `language.v1`. Extension contributions that execute
workspace-side code depend on host placement and broker permissions. Recovery
depends on URI/revision semantics being stable.

## Acceptance criteria for implementation

- Version/feature negotiation rejects unknown required versions and downgrade.
- Local adapter parity tests cover paths, transactions, watches, PTY cleanup,
  SCM, language, debug, test, journal, and snapshot behavior.
- A real test host covers identity pinning, symlink escape, cancellation,
  bounded output, watch overflow, disconnect during every mutation phase, and
  reconnect with changed generation/revision.
- Token tests reject missing, expired, replayed, cross-host, cross-workspace,
  cross-operation, cross-generation, and cross-instance-nonce use.
- Transaction fault injection proves all-or-rollback semantics without data
  loss; recovery never silently replaces newer host content.
- Packaged E2E drives a built artifact through disconnect/reconnect and retains
  protocol transcript metadata without secrets.
- Documentation and UI continue to say “minimal SSH/SFTP” until these gates and
  real platform evidence exist.

