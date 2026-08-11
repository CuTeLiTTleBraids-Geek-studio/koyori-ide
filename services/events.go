// Package services event catalog (N-44 / Proposal N).
//
// This file documents the canonical event-name → payload-type mapping
// for all backend-emitted Wails events. The frontend mirrors these
// types in frontend/src/types/index.ts (WailsEvent<T> and per-channel
// aliases). When adding a new event channel, update BOTH this file
// and the TypeScript types together.
//
// Emitting an event:
//
//	s.app.Event.Emit("ai:chunk", "hello")
//	s.app.Event.Emit("terminal:output", map[string]string{
//	    "sessionId": id,
//	    "data":      output,
//	})
//
// All payloads MUST be JSON-serializable (Wails marshals them for the
// webview). The frontend handler receives a WailsEvent<T> whose `data`
// field carries the payload.
//
// Event channels:
//
//	ai:chunk            {streamId, data}    — a streamed token from the AI
//	ai:done             {streamId, data}    — finish reason (data may be "")
//	ai:error            {streamId, data}    — error message
//	ai:stream-busy      {streamId, busy}    — process-wide stream mutex (prompt-5/6)
//	ai:tool_calls       {streamId, data}    — JSON array of native tool calls
//	ai:selection        {code,language,...} — main → AI window selection
//	ai:apply-to-editor  {code,filePath,...} — AI window → main apply request
//	settings:changed    {origin, at}        — dual-window settings SSOT (prompt-6)
//	conversation:saved  {origin, id, ...}   — dual-window conversation SSOT
//	agent:pending-updated {origin, count}   — agent approval queue summary
//	file:saved          string              — absolute path of saved file
//	terminal:output     {sessionId, data}   — a single PTY stdout/stderr chunk
//	terminal:exited     {sessionId, code, signal, err} — PTY process exited;
//	                                      signal is set on Unix signal termination
//	workflow:completed  {name}              — workflow finished (Proposal R)
//	extension:lifecycle-request {requestId, extensionId, publisher, name, action, wasActive}
//	extension:lifecycle-result  {requestId, extensionId, publisher, name, action, ok, wasActive, error?}
//
// The frontend types are:
//
//	type WailsEvent<T> = { data: T; name?: string }
//	type AIStreamPayload      = { streamId: string; data?: string; busy?: boolean }
//	type AIChunkEvent         = WailsEvent<AIStreamPayload | string>
//	type AIDoneEvent          = WailsEvent<AIStreamPayload | string>
//	type AIErrorEvent         = WailsEvent<AIStreamPayload | string>
//	type FileSavedEvent       = WailsEvent<string>
//	type TerminalOutputEvent  = WailsEvent<{ sessionId: string; data: string }>
//	type TerminalExitedEvent  = WailsEvent<{
//	  sessionId: string; code?: number; signal?: string; err?: string
//	}>
//	type WorkflowCompletedEvent = WailsEvent<{ name: string }>
//
// This file is documentation-only — it declares no Go symbols. The
// actual Emit calls live in ai_service.go, file_service.go,
// terminal_service.go, etc.
package services

const (
	// Extension lifecycle requests are emitted by the backend and consumed by
	// the main renderer. Results travel back through the same Wails bus.
	extensionLifecycleRequestEvent = "extension:lifecycle-request"
	extensionLifecycleResultEvent  = "extension:lifecycle-result"
)
