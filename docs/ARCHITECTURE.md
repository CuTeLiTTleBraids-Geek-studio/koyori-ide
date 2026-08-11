# Architecture Overview

> This document describes what exists in the current codebase, verified by
> direct code inspection. "Planned" features are labelled; nothing is implied
> beyond what is stated. For the product roadmap and acceptance criteria see
> `docs/prompts/prompt-5.md`.

## Stack

| Layer | Technology |
|---|---|
| Backend | Go 1.25 |
| Desktop runtime | Wails v3 alpha (`v3.0.0-alpha2.111`, pinned in `go.mod`) |
| Frontend | Vue 3 + TypeScript + Vite |
| Editor | Monaco Editor |
| UI library | Element Plus |
| Terminal | xterm.js + ConPTY (Windows) / creack/pty (Unix) |
| Git | go-git v5 |
| Markdown | marked + DOMPurify |

## Backend service layer

46 Go services are registered with Wails via `application.NewService`. Binding
IDs and fully-qualified names are owned exclusively by the Wails generator;
application code must not calculate or copy them. If a service method changes,
regenerate with `node scripts/generate-bindings.mjs` and review the complete
export manifest before accepting the new surface.

Services communicate with each other through constructor injection; there is no
service locator. Platform-specific code is separated by build tags (e.g.
`pty_unix.go` / `pty_windows.go`).

Security-relevant services share a common design:

- **pathsec**: `ValidatePathWithinRoot` with both-side `EvalSymlinks` is the
  single path-validation entry point. Terminal, Agent, File, Git, Search, and
  Conversation services all call it rather than duplicating the logic.
- **Atomic writes**: `atomicWriteJSON` / `atomicWriteFile` (temp + rename +
  `0600`) is used for every persistent JSON file so a crash during a write
  cannot corrupt it.
- **Capability tokens**: AgentService, ComputerUseService, MCPService, and
  ExtensionSecurityService issue single-use, parameter-bound, short-lived
  backend tokens before any sensitive execution. The renderer supplies an opaque
  token, not a boolean.

## WorkspaceContext (GOAL-P0-02)

`WorkspaceContext` (`services/workspace_context.go`) is the single shared
workspace identity for the process. It holds:

- **root** — the canonicalised absolute path of the active workspace.
- **generation** — a monotonically increasing counter bumped on every workspace
  switch (and on clear). Capabilities and executors bound to an older generation
  stop being accepted.

`ProjectService.AddProject` registers the context as its **first** setter in
the two-phase commit sequence, so the root and generation are canonicalised and
committed before any other service is updated. A failure rolls the context back
to the previous state, keeping the invariant that every service observes the
same workspace identity.

Services that previously captured a constructor-time empty string (AIPlanService,
AIGoalService, DiffService, and the default executors) now hold a pointer to the
same `WorkspaceContext` instance instead.

## Recovery journal (GOAL-P0-03)

`RecoveryService` (`services/recovery_service.go`) persists unsaved editor
buffers to a per-workspace, per-window directory under the user config
directory. Each record is written atomically with `0600` permissions so dirty
source text is not world-readable.

On startup, `ScanRecoverable` compares each record's baseline hash (captured
when the file was opened or last saved) against the current on-disk hash. A
match returns `"clean"` (safe to restore); a mismatch returns `"conflict"` (the
user must choose); a missing file returns `"missing"`.

`CrashService` writes panic reports only. It is not a content backup and must
not be conflated with recovery.

## Frontend architecture

The frontend is a Vue 3 SPA running inside the Wails WebView. There is **no
Pinia**: the project uses module-level singleton reactive objects exported as
`xxxState` from `src/stores/*.ts`.

Communication with Go is through:
1. **Wails IPC** (`$Call.ByID`) — the primary path, used for all service calls.
2. **Wails events** (`app.Event.On/Emit`) — used for streaming (AI SSE chunks,
   terminal output, LSP diagnostics, extension messages).

The frontend stores are intentionally thin state containers; business logic
belongs in the Go backend.

## Extension host

The extension host (`src/lib/extensionHost/`) exposes a subset of the VS Code
extension API to Web Worker-sandboxed extensions. The exposed methods and their
permission requirements are enumerated in
[`docs/EXTENSION-COMPATIBILITY.md`](EXTENSION-COMPATIBILITY.md).

The host uses a permission-deny-by-default model: any method not in the allow
list returns an error immediately. Extensions cannot obtain a direct reference
to `appState`, the Wails runtime, or any Go service binding.

## Security architecture summary

| Area | Mechanism |
|---|---|
| Path traversal | `pathsec.ValidatePathWithinRoot` (double `EvalSymlinks`) |
| Command execution | Backend capability token (single-use, parameter-bound, TTL) |
| Extension isolation | Web Worker + permission gate + deny-by-default allow list |
| AI API key storage | AES-256-GCM (local build) / DPAPI / Keychain / Secret Service |
| XSS | DOMPurify on all `v-html`; CSP nonce from `crypto/rand` |
| Renderer trust | No renderer-supplied boolean can elevate capability |
| Path output | Atomic write + `0600` for all config JSON |

For vulnerability reporting and the full security policy see
[`.github/SECURITY.md`](../.github/SECURITY.md).

## Planned protocol boundaries (partially implemented)

The following documents describe capabilities that are not complete. The
Language Pack runtime has a local signed-package implementation, but its full
packaged, remote, and cross-platform acceptance criteria remain open:

- [Unified Host Client Protocol](HOST-CLIENT-PROTOCOL.md): proposed local/remote
  workspace identity and FS/watch/PTY/SCM/Language/Debug/Test broker boundary.
- [Language Pack Manifest and SDK](LANGUAGE-PACK-SDK.md): closed built-in and
  native-installed Ed25519-signed manifests drive language detection, local
  LSP declarations, structured toolchain commands, and debugger adapters.
  Engine/host protocol and OS/architecture compatibility are fail-closed;
  Python and Rust provide real local third-party integration evidence. Server
  payload download and the remote broker are not implemented.
- [Versioned Extension Contribution Protocol](EXTENSION-CONTRIBUTION-PROTOCOL.md):
  proposed contribution lifecycle and E0-E5 classification.

Today, Remote remains minimal SSH/SFTP and extension contributions remain
unversioned and constrained by the current API allow list. The Go LSP and
Debug brokers still own process lifecycle and protocol framing; only the
built-in Go/TS/JS LSP, toolchain, and local debugger declarations have moved
into verified manifests. These
documents do not expand the product surface beyond the explicitly verified
behavior. The product name is Koyori IDE; older module paths and retained test
evidence may preserve historical identifiers for compatibility and audit.
