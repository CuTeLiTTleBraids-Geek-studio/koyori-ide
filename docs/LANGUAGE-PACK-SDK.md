# Language Pack Manifest and SDK

> **Partial implementation, not a completed Goal.** Closed backend and renderer
> validators share the same manifest files. A native manifest-only installer
> verifies Ed25519 signatures, publisher trust, canonical SHA-256, platform
> compatibility, staged publication, enable/disable, uninstall, and approved
> rollback. Signed Python and Rust packs exercise real local LSP, toolchain, and
> stdio DAP paths. A Windows x64 packaged run proves the built-in Go/TypeScript
> editing, LSP, format, build, test, and debug chain. A remote language broker,
> server-binary payload installer, and complete cross-platform packaged matrix
> do not exist. Unit or fake-server tests are not reported as packaged evidence.

## Purpose and current boundary

A Language Pack should describe how a language is detected and how a compatible
server is selected, started, initialized, stopped, and placed on a local or
remote host. It should remove language-specific branching from the core broker
without allowing an untrusted manifest to become an arbitrary command runner.

The current runtime intentionally does not bundle or download servers, accept
workspace-provided packs, or claim compatibility with VS Code language
extensions. It accepts two compiled-in manifests plus native-user-installed,
signature-verified external manifests. The renderer receives immutable
selector snapshots and has no process API. The Go backend verifies canonical
SHA-256, the closed schema, engine/host protocol, current OS/architecture, and
cross-pack ownership before deriving local LSP, toolchain, and debugger
definitions. Child processes remain owned by the existing Go brokers.

## Manifest envelope

The canonical format is JSON with a closed schema. Unknown fields are rejected
for major version 1 so misspelled security fields cannot be ignored.

```json
{
  "schemaVersion": "1.0",
  "id": "org.koyori.ide.go",
  "version": "1.0.0",
  "displayName": "Go",
  "compatibility": {
    "engineApi": "1.0",
    "hostProtocol": "language.local.v1",
    "platforms": [{ "os": "windows", "arch": "amd64" }]
  },
  "languages": [{
    "id": "go",
    "extensions": [".go"],
    "filenames": []
  }],
  "rootMarkers": ["go.work", "go.mod", ".git"],
  "servers": [{
    "id": "gopls",
    "statusOrder": 10,
    "languages": ["go"],
    "aliases": [],
    "executables": [{ "commandName": "gopls", "kind": "gopls" }],
    "args": ["serve"],
    "installHint": "go install golang.org/x/tools/gopls@latest",
    "workspaceNode": false,
    "initializationProfile": "go",
    "configurationSections": ["gopls"],
    "configurationResponse": "full",
    "versionArgs": ["version"],
    "preferReactWorkspace": false,
    "reactAware": false
  }],
  "debuggers": [{
    "id": "delve",
    "protocol": "dap",
    "languages": ["go"],
    "executable": "dlv",
    "args": ["dap", "--log=false"],
    "installHint": "go install github.com/go-delve/delve/cmd/dlv@latest"
  }],
  "toolchain": {
    "commands": [{
      "id": "go-build",
      "label": "Go: Build",
      "language": "go",
      "executable": "go",
      "args": ["build", "./..."],
      "description": "Compile all Go packages",
      "fileScoped": false
    }],
    "tools": [{
      "name": "go",
      "installHint": "Install Go from https://go.dev/dl/"
    }]
  },
  "permissions": ["workspace.read", "process.launch"],
  "configurationSchema": {},
  "integrity": { "manifestSha256": "<hex>" }
}
```

IDs are lowercase reverse-DNS identifiers and versions use strict SemVer. The
runtime compares prerelease identifiers according to SemVer: numeric identifiers
are compared numerically, numeric identifiers sort below non-numeric identifiers,
and build metadata does not affect precedence. Invalid versions fail closed.
The SHA-256 covers recursively key-sorted canonical JSON with the `integrity` member
removed. Version 1 requires engine API `1.0`, local host protocol
`language.local.v1`, and an explicit platform set from Windows/macOS/Linux and
amd64/arm64. A manifest that omits the current backend platform is rejected.

## Implemented built-in server declaration

Each `servers` item defines:

```text
ServerVariant {
  id
  statusOrder
  languages[]
  aliases[]
  executables[{ commandName, kind }]
  args[]
  installHint
  workspaceNode
  initializationProfile
  configurationSections[]
  configurationResponse
  versionArgs[]
  preferReactWorkspace
  reactAware
}
```

There is no shell command string, manifest-controlled CWD, absolute executable
path, environment contribution, socket transport, or package artifact. A
`commandName` must be a basename and argv is passed directly to `exec.Cmd`.
Only embedded or active signature-verified manifests reach this broker.
Executables remain basenames resolved by the backend, and workspace-local
debug adapter shadowing is rejected. Environment filtering and OS sandbox
evidence remain open hardening work; compatibility metadata is not isolation.

## Implemented debugger declaration

The optional `debuggers` member declares a fixed adapter identity and protocol
for the languages it owns:

```text
DebuggerVariant {
  id
  protocol: dap | cdp
  languages[]
  executable
  args[]
  installHint
}
```

The Go pack currently declares `dlv dap --log=false`; the TypeScript pack
declares Node's `--inspect-brk` CDP entry. The backend resolves only the
declared executable basename through `PATH`, appends its own ephemeral listen
endpoint, and still owns workspace path validation, lifecycle, and protocol
framing. The renderer can inspect the declaration but cannot start an adapter.
External Python/debugpy and Rust/lldb-dap packs use the same backend path. The
adapter ID is selected only from an active signed manifest; renderer input
cannot provide an executable path. Adapter stderr is continuously drained,
captured to 64 KiB for startup diagnostics, and marked when truncated. This is
local evidence only and does not imply remote debugging.

## Implemented toolchain declaration

The optional `toolchain` member uses the same closed, hashed manifest. Each
command contains one executable basename, discrete argv elements, a language
ID, and a `fileScoped` flag. The backend derives the existing command-palette
catalog, resolves the executable through its configured tool path or PATH,
and executes it only under the current workspace lease. A file-scoped action
receives the backend-resolved file path as one additional argv element; it
cannot provide a shell string, working directory, environment, or approval.
`tools[].installHint` is display-only and never grants execution.

The built-in Go and TypeScript/JavaScript packs and active signed external
packs declare build, test, format, and lint actions used by
`ToolchainService`. Each stdout/stderr stream is capped at 2 MiB while excess
bytes are drained. The Windows x64 packaged matrix covers the built-in Go and
TypeScript chain; complete G23 acceptance still requires third-party packaged,
remote, and cross-platform matrices.

For a remote workspace, `workspacePlacement` is always `workspaceHost` in v1.
Running a local server over remote file copies is explicitly unsupported
because it breaks path identity, watch ordering, and tool execution semantics.

## SDK boundary

The core owns JSON-RPC/LSP framing. Pack code, if introduced later, receives a
restricted declarative SDK rather than raw process, filesystem, network, Wails,
or renderer access:

```text
LanguagePackSDKv1 {
  registerManifest(manifest)
  resolveRoot(workspaceRef, documentUri) -> RootRef
  selectServer(hostFacts, workspaceFacts) -> ServerVariantId
  buildInitialization(workspaceRef, settingsSnapshot) -> JSON value
  translateDiagnostic(diagnostic) -> diagnostic
  dispose()
}
```

`resolveRoot` may choose only broker-provided candidate ancestors inside the
workspace. `buildInitialization` returns data validated against a size/depth
limit. It cannot inject executable paths, environment values, or host URIs.

## Target lifecycle (not fully implemented)

```text
DISCOVERED -> COMPATIBLE -> APPROVAL_REQUIRED -> STARTING -> INITIALIZING
    -> READY -> STOPPING -> STOPPED
                    |          |
                    +-> FAILED +-> FAILED
```

1. Discovery parses and hashes the manifest without executing anything.
2. Compatibility checks schema, engine, host protocol, OS/arch, permissions,
   and package integrity.
3. Selection is deterministic by explicit priority and stable ID.
4. Approval binds pack/version/server/executable/args/workspace/host/generation.
5. Broker launches with bounded environment and resources, then performs LSP
   initialize/initialized with deadlines.
6. Document and request traffic uses workspace URIs, request cancellation, and
   bounded payloads through the Host language broker.
7. Shutdown sends LSP shutdown/exit, kills on deadline, closes pipes/listeners,
   and releases every timer and capability.

Crash restart is bounded and visible. The broker never loops indefinitely and
never reuses an approval after executable, version, host, or generation change.

## Capability and settings negotiation

The manifest declares desired features, not guaranteed features. The broker
intersects pack, server initialize result, editor support, and policy. UI only
advertises the resulting intersection.

Settings are namespaced by pack ID, validated against a closed JSON Schema, and
provided as an immutable snapshot. Secret settings use backend secret storage
and are exposed only to an explicitly approved server environment key; they do
not appear in initialization JSON or logs by default.

Workspace files cannot install or elevate packs. A project may recommend a pack
ID/version range, but installation and permissions require native user action.

## Security classes

| Permission | Meaning | Default |
|---|---|---|
| `workspace.read` | Brokered document/root reads | required, scoped |
| `workspace.write` | Apply brokered edits with revision preconditions | denied until approved |
| `process.launch` | Launch the selected server executable | native approval |
| `network.client` | Server may access network | denied unless declared/approved |
| `tool.execute` | Server requests a named tool through policy | denied in v1 |

Language servers are untrusted child processes. Host sandboxing is a platform
goal, not assumed present. Until real sandbox evidence exists, documentation
must disclose that an approved server executes with the user's OS identity.

## Packaging, integrity, and updates

The implemented `.koyori-language-pack` artifact is manifest-only and contains
exactly `manifest.json` and `signature.json`. The native installer caps the
archive at 16 MiB, rejects traversal, backslashes, symlinks, duplicates, extra
payloads and oversized decompression, verifies canonical manifest SHA-256 and
Ed25519 publisher signatures, then publishes by staged rename. Publisher trust
is key-pinned and explicitly approved. A lower-precedence SemVer cannot be
installed over the active version; this also applies to prerelease identifiers
such as `1.0.0-2` versus `1.0.0-10`, while build metadata is
precedence-neutral. Rejection leaves active state and published directories
unchanged. Downgrade is available only through the separately approved rollback
operation. Uninstall approval binds the active version. Server-binary
download/install remains unimplemented.

## Dependencies and migration

1. Implement Unified Host `language.v1` and workspace URI mapping.
2. Extract current Go and TypeScript detection rules into built-in manifests
   without adding new claimed server support.
3. Add closed-schema parsing and deterministic selection contract tests.
4. Add restricted SDK hooks only for behavior that cannot be declarative.
5. Consider external packages only after integrity, rollback, and permission
   gates pass hostile fixtures.

Extension-contributed language features remain separate from server packs until
the versioned contribution protocol defines ownership and collision rules.

## Acceptance criteria for implementation

- Invalid schema, unknown fields, incompatible versions, duplicate IDs,
  ambiguous variants, unsupported OS/arch, and unapproved permissions fail.
- Executable/argv/environment approval rejects replay and cross-pack, server,
  host, workspace, generation, version, or path reuse.
- Root resolution cannot escape workspace or cross host; URI translation is
  round-trip tested for Windows/POSIX and multi-root workspaces.
- Fake-server tests cover initialize failure, malformed/oversized frames,
  cancellation, crash loops, shutdown timeout, stderr bounds, and cleanup.
- Real pinned `gopls` and TypeScript server matrices retain transcripts and
  prove completion, hover, definition, formatting, rename, diagnostics, and
  cancellation before any row is marked verified.
- Remote evidence proves the server runs on the workspace host and survives
  watch resync/reconnect without silently replaying edits.
- Package hostile fixtures cover zip slip, symlink escape, hash mismatch,
  downgrade, rollback, and interrupted update.
- README and release tables derive claims from retained evidence, not manifest
  presence or mock tests.
