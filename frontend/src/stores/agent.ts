// Agent store: native provider tool calls are authoritative. Fenced tool
// parsing is an explicitly marked compatibility fallback for providers that
// cannot use native function calling; both sources share one execution path.

// Koyori IDE 模块 · Agent；交互服务：AI 对话（AIService）、自治 Agent（AgentService）、文件系统（FileService）。
// 喵，这是 Koyori IDE 的 Agent 模块（前端实现）~
import { reactive, computed, ref } from "vue";
import { Events } from "@wailsio/runtime";
import { agentService, aiService, fileService } from "@/api/services";
import { computeFileDiffPreview } from "@/stores/diff";
import type {
	AgentToolCatalog,
	AgentToolDefinition,
	AgentToolExecutionResult,
} from "@/api/automation";
import { appState } from "@/stores/app";
import { pushOutput } from "@/stores/output";
import { notifyError, notifySuccess, notifyWarning, notifyInfo } from "@/lib/notifications";
import { errorMessage } from "@/lib/errors";
import { getWindowOriginId, unwrapEventData, parseSyncOrigin } from "@/lib/windowOrigin";
import { translate } from "@/lib/i18n";
import type { RiskLevel, AgentPermissionMode, NativeToolResultContext, ToolBudgetStatus } from "@/types";
import {
  recordToolObservation,
  recordToolRequested,
  recordToolStage,
  bindAgentState,
  resetAgentTimeline,
} from "@/stores/agentTimeline";

export type AgentMode = "chat" | "agent";
// ToolCallKind is now `string` (N-16) to allow custom tools registered via
// registerTool(). BuiltinToolKind is exported for code that only handles the
// four built-in tools.
export type BuiltinToolKind = "read" | "write" | "run" | "search";
export type ToolCallKind = string;
export type ToolCallStatus =
  | "pending"
  | "approved"
  | "rejected"
  | "executed"
  | "error";

export interface ToolCallExecution {
	requestSessionId: string;
	result: AgentToolExecutionResult;
}

// MAX_TOOL_CALLS is a DISPLAY-ONLY fallback (GOAL-P1-02).
//
// It used to be the only tool-call ceiling in the product, enforced here by a
// `notifyWarning` that the user could ignore indefinitely. That was not a limit:
// a renderer refresh reset `toolCallCount` to zero, and a forged count was never
// checked against anything, so nothing bounded an agent loop.
//
// Enforcement now lives in the backend (`services/agent_budget.go`), which
// refuses to mint a capability token past the budget. This constant is retained
// only so the UI has a number to render before the first backend status arrives.
// Changing it grants nothing.
/** Exported for README/docs alignment; display fallback only, not enforced. */
export const MAX_TOOL_CALLS = 20;

export type ToolCallSource = "native" | "fence";

export interface ToolCall {
  id: string;
  source?: ToolCallSource;
  kind: ToolCallKind;
  // For read/write/run/search: the path or command or query on the first line.
  target: string;
  // For write: the full file content (rest of the code block).
  content?: string;
	// Structured arguments from the backend catalog schema. This is the
	// authoritative invocation payload; target/content are compatibility display
	// fields for the four historical tools.
	arguments?: Record<string, unknown>;
	writeDiff?: import("@/types").FileDiff;
	selectedHunks?: number[];
	catalogRevision?: number;
	wireName?: string;
	sessionId?: string;
	// Retain the typed backend result that produced the observation. This is
	// renderer evidence only; it does not grant or restore backend authority.
	execution?: ToolCallExecution;
  status: ToolCallStatus;
  // Human-readable result summary, populated after execution.
  result?: string;
  // Error message, populated when status === "error".
  error?: string;
  // Risk level for `run` tool calls, populated asynchronously by
  // checkRunRisk() after the tool call is added to the pending queue
  // (N-1). Used by the approval UI to show a risk badge.
  // Runtime classification and backend denial evidence; never an authority grant.
  riskLevel?: RiskLevel;
  blockReason?: string;
  /** Permission intent captured with the backend session that proposed this call. */
  permissionMode?: AgentPermissionMode;
	// Multiple native calls emitted by one provider turn are completed as one
	// batch. These renderer-only fields carry no backend authority.
	_turnBatchId?: string;
	_turnObservation?: string;

	// Renderer-local authority generation captured when the call entered the
	// queue. A conversation or mode reset burns it before any later approval.
	_turnGeneration?: number;
}

interface AgentStoreState {
  mode: AgentMode;
  pendingToolCalls: ToolCall[];
  // Total tool calls emitted in the current conversation (N-10).
  // Incremented in onAssistantFinished, reset in clearPendingToolCalls.
  //
  // GOAL-P1-02: this is a local display counter, NOT a limit. The authoritative
  // count lives in `budget` below, fetched from the backend. Keeping the local
  // counter for the "conversation is getting long" hint is fine; treating it as
  // a ceiling is what made the old limit fictional.
  toolCallCount: number;
  // GOAL-P1-02: authoritative budget state from the backend. null until the
  // first refresh completes.
  budget: ToolBudgetStatus | null;
	catalogRevision: number;
	catalogLoaded: boolean;
	sessionId: string;
}

export const agentState = reactive<AgentStoreState>({
  mode: "chat",
  pendingToolCalls: [],
  toolCallCount: 0,
  budget: null,
	catalogRevision: 0,
	catalogLoaded: false,
	sessionId: `chat-${Date.now().toString(36)}`,
});

// Keep legacy direct state consumers (including the embedded panel) reflected
// in the shared execution timeline. Explicit transition hooks below provide
// richer details for the authoritative execution path.
bindAgentState(agentState);

let backendAgentSessionId: string | null = null;
let agentSessionPromise: Promise<string> | null = null;
let agentSessionGeneration = 0;
let agentTurnGeneration = 0;
let agentSessionPermissionMode: AgentPermissionMode = "always-ask";
let backendAgentWorkspaceGeneration: number | null = null;

// Tool execution is serialized behind the active AI turn. Native tool-call
// events arrive before `ai:done`; sending an observation from that window
// would be silently ignored by ai.sendMessage while the prior stream is still
// active. The queue also gives manual approvals the same ordering guarantee.
let toolExecutionTail: Promise<void> = Promise.resolve();
interface AgentToolTurnBatch {
	id: string;
	generation: number;
	calls: ToolCall[];
	submitting: boolean;
}
const agentToolTurnBatches = new Map<string, AgentToolTurnBatch>();
let agentToolTurnBatchCounter = 0;
// Provider call IDs are authoritative within one backend Agent session. Keep
// the complete invocation identity so an exact event replay is idempotent,
// while reuse of an ID for different arguments fails closed.
const nativeToolCallIdentities = new Map<string, string>();
const AGENT_STREAM_IDLE_POLL_MS = 16;
const AGENT_STREAM_RELEASE_TIMEOUT_MS = 5_000;

interface AIStreamActivity {
	streaming: boolean;
	globalStreamBusy: boolean;
}

async function readAIStreamActivity(): Promise<AIStreamActivity> {
	try {
		// Keep the dependency lazy: ai.ts imports this store for tool handling.
		const { aiState } = await import("@/stores/ai");
		return {
			streaming: Boolean(aiState?.streaming),
			globalStreamBusy: Boolean(aiState?.globalStreamBusy),
		};
	} catch {
		// Headless/unit consumers may not provide the renderer AI store. In that
		// case there is no renderer stream to fence and execution may proceed.
		return { streaming: false, globalStreamBusy: false };
	}
}

async function waitForAIStreamIdle(): Promise<void> {
	let releaseStartedAt: number | null = null;
	while (true) {
		const activity = await readAIStreamActivity();
		if (!activity.streaming && !activity.globalStreamBusy) return;
		if (!activity.streaming && activity.globalStreamBusy) {
			releaseStartedAt ??= Date.now();
			if (Date.now() - releaseStartedAt >= AGENT_STREAM_RELEASE_TIMEOUT_MS) {
				throw new Error("AI stream cleanup did not complete within 5 seconds");
			}
		} else {
			// A real provider stream may legitimately run for minutes. Bound only
			// the short done -> process-wide busy release window.
			releaseStartedAt = null;
		}
		await new Promise<void>((resolve) => {
			setTimeout(resolve, AGENT_STREAM_IDLE_POLL_MS);
		});
	}
}

function enqueueToolExecution(
	task: () => Promise<void>,
	expectedGeneration = agentTurnGeneration,
): Promise<void> {
	const guardedTask = async (): Promise<void> => {
		if (expectedGeneration !== agentTurnGeneration) return;
		await task();
	};
	const run = toolExecutionTail.then(guardedTask, guardedTask);
	// A failed execution must not poison later approvals, while the returned
	// promise still carries the current operation's error to its caller.
	toolExecutionTail = run.catch(() => undefined);
	return run;
}

function createAgentToolTurnBatch(calls: ToolCall[], generation: number): void {
	if (calls.length < 2) return;
	agentToolTurnBatchCounter += 1;
	const id = `tool-turn-${generation}-${agentToolTurnBatchCounter}`;
	for (const call of calls) call._turnBatchId = id;
	agentToolTurnBatches.set(id, { id, generation, calls, submitting: false });
}

/** Ensures all renderer tool calls reuse one backend-owned chat session. */
export function ensureAgentSession(): string | Promise<string> {
  const workspaceGeneration = Number.isSafeInteger(appState.workspaceGeneration)
    ? appState.workspaceGeneration
    : 0;
  if (backendAgentSessionId && backendAgentWorkspaceGeneration === workspaceGeneration) {
    return backendAgentSessionId;
  }
  if (backendAgentSessionId) resetAgentSession();
  if (typeof agentService.createSession !== "function") {
    agentSessionPermissionMode = normalizePermissionMode(appState.agentPermissionMode);
    return agentState.sessionId;
  }
  if (!agentSessionPromise) {
    const generation = agentSessionGeneration;
    const requestedWorkspaceGeneration = workspaceGeneration;
    const creating = agentService.createSession("chat").then((sessionId) => {
      const normalized = sessionId.trim();
      if (!normalized) throw new Error("backend returned an empty Agent session ID");
      if (generation !== agentSessionGeneration
        || backendWorkspaceGeneration() !== requestedWorkspaceGeneration) {
        if (typeof agentService.closeSession === "function") {
          void agentService.closeSession(normalized).catch(() => undefined);
        }
        throw new Error("Agent session changed while it was being created");
      }
      backendAgentSessionId = normalized;
      backendAgentWorkspaceGeneration = requestedWorkspaceGeneration;
      agentSessionPermissionMode = normalizePermissionMode(appState.agentPermissionMode);
      agentState.sessionId = normalized;
      return normalized;
    });
    const tracked = creating.finally(() => {
      if (agentSessionPromise === tracked) agentSessionPromise = null;
    });
    agentSessionPromise = tracked;
  }
  return agentSessionPromise;
}

/** Closes the current authority namespace and rotates the local draft ID. */
export function resetAgentSession(): void {
  const previous = backendAgentSessionId;
  agentSessionGeneration += 1;
  agentTurnGeneration += 1;
  backendAgentSessionId = null;
  backendAgentWorkspaceGeneration = null;
  agentSessionPermissionMode = "always-ask";
  agentSessionPromise = null;
  agentToolTurnBatches.clear();
  nativeToolCallIdentities.clear();
  agentState.sessionId = `chat-${Date.now().toString(36)}`;
  if (previous && typeof agentService.closeSession === "function") {
    void agentService.closeSession(previous).catch(() => undefined);
  }
}
function normalizePermissionMode(value: unknown): AgentPermissionMode {
  return value === "assist" || value === "allow-all" ? value : "always-ask";
}

function backendWorkspaceGeneration(): number {
	return Number.isSafeInteger(appState.workspaceGeneration) ? appState.workspaceGeneration : 0;
}

export const isAgentMode = computed(() => agentState.mode === "agent");
export const hasPendingToolCalls = computed(
  () => agentState.pendingToolCalls.some((tc) => tc.status === "pending"),
);

/**
 * Reports whether the tool budget is spent (GOAL-P1-02).
 *
 * Derived from the backend status when available. The local-counter comparison
 * is only a pre-backend fallback so the UI is not blank on first render; it has
 * no enforcement power either way, because the backend refuses to mint a
 * capability token regardless of what this returns.
 */
export const maxIterationsReached = computed(() => {
  if (agentState.budget) return agentState.budget.exhausted;
  return agentState.toolCallCount >= MAX_TOOL_CALLS;
});

/** Budget limit to display. Backend value wins over the local fallback. */
export const toolBudgetLimit = computed(
  () => agentState.budget?.limit ?? MAX_TOOL_CALLS,
);

/** Backend-reported calls spent in the current epoch. */
export const toolBudgetSpent = computed(
  () => agentState.budget?.spent ?? agentState.toolCallCount,
);

/**
 * Fetches the authoritative budget from the backend.
 *
 * Failure is non-fatal and leaves the previous status in place: a status-read
 * outage must not make the UI claim the agent has budget it does not have, and
 * it cannot grant budget either, since the ceiling is enforced backend-side.
 */
export async function refreshToolBudget(): Promise<void> {
  try {
    agentState.budget = await agentService.getToolBudget();
  } catch (e: unknown) {
    pushOutput("agent", "warn", `getToolBudget failed: ${errorMessage(e)}`);
  }
}

/**
 * Asks the backend to open a new budget epoch after the user explicitly chooses
 * to continue (GOAL-P1-02 execution point 3).
 *
 * Passing 0 keeps the backend's configured limit. This is the only path past an
 * exhausted budget, and the backend audit-logs it.
 */
export async function startNewToolBudgetEpoch(limit = 0): Promise<boolean> {
  try {
    agentState.budget = await agentService.startNewToolBudgetEpoch(limit);
    // The local hint counter is per-epoch too; leaving it high would keep the
    // "conversation is long" warning on screen after a deliberate reset.
    agentState.toolCallCount = 0;
    return true;
  } catch (e: unknown) {
    const message = errorMessage(e);
    agentState.budget = agentState.budget ?? null;
    notifyError(message);
    pushOutput("agent", "error", `startNewToolBudgetEpoch failed: ${message}`);
    return false;
  }
}

export function setMode(mode: AgentMode): void {
  if (agentState.mode === mode) return;
  agentState.mode = mode;
  clearPendingToolCalls();
}

export function toggleMode(): void {
  setMode(agentState.mode === "chat" ? "agent" : "chat");
}

let toolCallCounter = 0;
function nextToolCallId(): string {
  toolCallCounter += 1;
  return `tc-${Date.now().toString(36)}-${toolCallCounter}`;
}

// Cache the agent system prompt so we don't round-trip to the backend on
// every send. Fetched lazily on first use. Declared early (before
// registerTool) so that registerTool can safely invalidate it without hitting
// the temporal dead zone (N-16).
let agentSystemPromptCache: string | null = null;

// Tool call block regex. Matches fenced code blocks where the first line is
// `kind: target`. Supports both ``` and ~~~ fences, optional language tag.
// The opening fence is captured in group 1 and referenced via \1 backreference
// so the closing fence matches the opening one. The regex captures:
//   group 1 = fence (``` or ~~~)
//   group 2 = kind (e.g. read|write|run|search, plus any custom tools)
//   group 3 = target (rest of first line)
//   group 4 = content (rest of block, may be undefined)
//   group 5 = same as group 4 (kept for legacy index compatibility)
//
// N-16: The regex is built dynamically from the toolRegistry so that custom
// tools (registered via registerTool) are automatically recognized by the
// parser without code changes. The regex is cached and rebuilt only when the
// set of registered tools changes.

function escapeRegex(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

let cachedToolCallRe: RegExp | null = null;

function getToolCallRegex(): RegExp {
  if (cachedToolCallRe !== null) return cachedToolCallRe;
	const kinds = Array.from(toolRegistry.values()).flatMap((tool) =>
		!tool.wireName || tool.wireName === tool.kind ? [tool.kind] : [tool.kind, tool.wireName],
	);
  if (kinds.length === 0) {
    // No tools registered — a regex that never matches.
    cachedToolCallRe = /(?!)/g;
    return cachedToolCallRe;
  }
  const kindsPattern = kinds.map(escapeRegex).join("|");
  cachedToolCallRe = new RegExp(
    "(?:^|\\n)(```|~~~)[a-zA-Z]*\\n(" + kindsPattern + "):\\s*(.+?)(\\n([\\s\\S]*?))?\\1",
    "g",
  );
  return cachedToolCallRe;
}

function findToolByCallName(name: string): ToolDef | undefined {
	const byId = toolRegistry.get(name);
	if (byId) return byId;
	return Array.from(toolRegistry.values()).find((tool) => tool.wireName === name);
}

function isRecordValue(value: unknown): value is Record<string, unknown> {
	return typeof value === "object" && value !== null && !Array.isArray(value);
}

function validateToolArguments(
	schema: Record<string, unknown>,
	value: unknown,
): value is Record<string, unknown> {
	const type = schema.type;
	if (type === "object") {
		if (!isRecordValue(value)) return false;
		const properties = isRecordValue(schema.properties) ? schema.properties : {};
		const required = Array.isArray(schema.required)
			? schema.required.filter((item): item is string => typeof item === "string")
			: [];
		if (required.some((name) => !(name in value))) return false;
		if (schema.additionalProperties !== false) return false;
		for (const [name, child] of Object.entries(value)) {
			const childSchema = properties[name];
			if (!isRecordValue(childSchema) || !validateToolArguments(childSchema, child)) return false;
		}
		return true;
	}
	if (type === "array") {
		if (!Array.isArray(value) || !isRecordValue(schema.items)) return false;
		if (typeof schema.minItems === "number" && value.length < schema.minItems) return false;
		if (typeof schema.maxItems === "number" && value.length > schema.maxItems) return false;
		return value.every((item) => validateToolArguments(schema.items as Record<string, unknown>, item));
	}
	if (type === "string") {
		return typeof value === "string"
			&& (typeof schema.minLength !== "number" || value.length >= schema.minLength);
	}
	if (type === "boolean") return typeof value === "boolean";
	if (type === "number") return typeof value === "number" && Number.isFinite(value);
	if (type === "integer") return typeof value === "number" && Number.isInteger(value);
	if (type === "null") return value === null;
	return false;
}

function legacyFenceArguments(
	tool: ToolDef,
	target: string,
	content: string | undefined,
): Record<string, unknown> | null {
	const schema = tool.schema.inputSchema;
	if (!schema) return null;
	const rawJSON = content === undefined ? target : `${target}\n${content}`;
	if (target.trimStart().startsWith("{")) {
		try {
			const parsed: unknown = JSON.parse(rawJSON);
			return validateToolArguments(schema, parsed) ? parsed : null;
		} catch {
			return null;
		}
	}
	let args: Record<string, unknown>;
	switch (tool.kind) {
		case "read":
			args = { path: target };
			break;
		case "write":
			args = { path: target, content: content ?? "" };
			break;
		case "run":
			args = { command: target };
			break;
		case "search":
			args = { query: target, ignoreCase: true };
			break;
		default:
			return null;
	}
	return validateToolArguments(schema, args) ? args : null;
}

function displayFields(
	tool: ToolDef,
	args: Record<string, unknown>,
): { target: string; content?: string } {
	if (tool.kind === "write") {
		return {
			target: typeof args.path === "string" ? args.path : "",
			content: typeof args.content === "string" ? args.content : undefined,
		};
	}
	const candidate = tool.kind === "run"
		? args.command
		: tool.kind === "search"
			? args.query
			: tool.kind === "read"
				? args.path
				: undefined;
	return {
		target: typeof candidate === "string" ? candidate : JSON.stringify(args),
	};
}

function invalidateToolCallRegex(): void {
  cachedToolCallRe = null;
}

function toolCallFromFenceMatch(match: RegExpExecArray): ToolCall | null {
	const tool = findToolByCallName(match[2]);
	if (!tool) return null;
  const target = match[3].trim();
  // match[5] is undefined when there is no body, or the body without the
  // closing fence when present. Strip only the newline introduced by the
  // fence grammar; preserve user content exactly for write previews.
  const rawContent = match[5];
  const content = rawContent && rawContent.length > 0
    ? rawContent.replace(/\n+$/, "")
    : undefined;
	const args = legacyFenceArguments(tool, target, content);
	if (!args) return null;
	const display = displayFields(tool, args);
  return {
    id: nextToolCallId(),
    source: "fence",
		kind: tool.kind,
		wireName: tool.wireName ?? tool.kind,
		target: display.target,
		content: display.content,
		arguments: args,
		catalogRevision: agentState.catalogRevision,
		sessionId: agentState.sessionId,
    status: "pending",
  };
}

/**
 * parseToolCalls scans an assistant message for tool-call fenced blocks and
 * returns them as ToolCall objects. Non-matching code blocks are ignored
 * (they are normal code suggestions the user can apply manually).
 */
export function parseToolCalls(message: string): ToolCall[] {
  if (!message) return [];
  const calls: ToolCall[] = [];
  let match: RegExpExecArray | null;
  const re = getToolCallRegex();
  // Reset regex state (it's a global regex with /g flag).
  re.lastIndex = 0;
  while ((match = re.exec(message)) !== null) {
		const call = toolCallFromFenceMatch(match);
		if (call) calls.push(call);
  }
  return calls;
}

/**
 * extractToolCallBlocks returns the tool-call code blocks found in a message,
 * used by the UI to render approval cards. Also returns the message with
 * tool-call blocks removed (so they don't render as normal code blocks).
 */
export function extractToolCallBlocks(
  message: string,
): { toolCalls: ToolCall[]; cleanedMessage: string } {
  const toolCalls = parseToolCalls(message);
  // Remove only schema-valid/parseable tool blocks. Keeping malformed blocks
  // visible is important: silently deleting a rejected invocation makes the
  // model appear to have answered normally and gives the user no diagnosis.
  const re = getToolCallRegex();
  re.lastIndex = 0;
  let cursor = 0;
  let cleanedMessage = "";
  let match: RegExpExecArray | null;
  while ((match = re.exec(message)) !== null) {
    cleanedMessage += message.slice(cursor, match.index);
    if (!toolCallFromFenceMatch(match)) cleanedMessage += match[0];
    cursor = match.index + match[0].length;
  }
  cleanedMessage += message.slice(cursor);
  cleanedMessage = cleanedMessage.trim();
  return { toolCalls, cleanedMessage };
}

/**
 * ToolSchema describes a tool's metadata for the AI prompt and UI (N-16).
 * The AI system prompt includes the list of registered tools with their
 * descriptions and danger levels so the model knows what tools are available.
 * The approval UI uses the danger level as a default risk badge for tools
 * that don't have a runtime risk classification (e.g. non-`run` tools).
 */
export interface ToolSchema {
  // Human-readable description of what the tool does, shown to the AI and UI.
  description: string;
  // Default danger level for this tool kind. `run` tools get a runtime risk
  // level from CheckCommand; other tools use this default for the UI badge.
  dangerLevel?: RiskLevel;
	inputSchema?: Record<string, unknown>;
	source?: AgentToolDefinition["source"];
	approval?: AgentToolDefinition["approval"];
	mutation?: AgentToolDefinition["mutation"];
}

/**
 * ToolDef describes a registered agent tool. Custom tools (N-16) register
 * via the same shape, extending the toolRegistry Map. The `schema` field
 * provides metadata for the AI system prompt and approval UI.
 */
export interface ToolDef {
  kind: string;
	wireName?: string;
  schema: ToolSchema;
}

/**
 * toolRegistry is a renderer projection of the backend catalog. It contains no
 * executable closures and therefore grants no authority by itself.
 */
const toolRegistry = new Map<string, ToolDef>();

// N-151: toolRegistry itself is a plain Map (not reactive). To let UI
// consumers track registrations that happen after mount (e.g. plugin tools
// loaded asynchronously), we expose a reactive version counter. Reads of
// getRegisteredTools() / listRegisteredTools() touch it, so any computed
// that calls them re-evaluates when a tool is registered or unregistered.
const toolRegistryVersion = ref(0);

/**
 * Renderer registration is deliberately forbidden. Dynamic MCP/workflow/skill
 * tools must be registered by trusted backend wiring and published as one
 * catalog revision.
 */
export function registerTool(def: ToolDef): void {
	throw new Error(`renderer tool registration is forbidden: ${def.kind}`);
}

/**
 * unregisterTool removes a tool from the registry. Returns true if a tool
 * was removed, false if the kind was not registered. Invalidates caches.
 */
export function unregisterTool(kind: string): boolean {
	throw new Error(`renderer tool unregistration is forbidden: ${kind}`);
}

function dangerLevelForTool(tool: AgentToolDefinition): RiskLevel {
	if (tool.risk === "read-only") return "safe";
	return tool.risk;
}
function canAssistAutoApprove(tc: ToolCall): boolean {
  const definition = toolRegistry.get(tc.kind);
  return definition?.schema.approval === "backend-policy"
    && definition.schema.dangerLevel === "safe"
    && definition.schema.mutation === "none";
}

function shouldAutoApprove(tc: ToolCall, mode: AgentPermissionMode): boolean {
  if (mode === "allow-all") return true;
  return mode === "assist" && canAssistAutoApprove(tc);
}


function projectToolDef(tool: AgentToolDefinition): ToolDef {
	return {
		kind: tool.id,
		wireName: tool.wireName,
		schema: {
			description: tool.description,
			dangerLevel: dangerLevelForTool(tool),
			inputSchema: tool.inputSchema,
			source: tool.source,
			approval: tool.approval,
			mutation: tool.mutation,
		},
	};
}

function publishToolCatalog(catalog: AgentToolCatalog): void {
	const next = new Map<string, ToolDef>();
	const wireNames = new Set<string>();
	for (const tool of catalog.tools) {
		if (!tool.id || !tool.wireName || next.has(tool.id) || wireNames.has(tool.wireName)) {
			throw new Error("backend returned an invalid or duplicate Agent ToolDef");
		}
		next.set(tool.id, projectToolDef(tool));
		wireNames.add(tool.wireName);
	}
	toolRegistry.clear();
	for (const [id, definition] of next) toolRegistry.set(id, definition);
	agentState.catalogRevision = catalog.revision;
	agentState.catalogLoaded = true;
	invalidateToolCallRegex();
	__resetAgentPromptCacheForTests();
	toolRegistryVersion.value++;
}

/** Refreshes the complete backend catalog atomically. Failure clears the local
 * projection so stale dynamic tools cannot remain model-callable. */
export async function refreshAgentToolCatalog(): Promise<AgentToolCatalog> {
	try {
		const catalog = await agentService.getToolCatalog();
		publishToolCatalog(catalog);
		return catalog;
	} catch (error: unknown) {
		toolRegistry.clear();
		agentState.catalogRevision = 0;
		agentState.catalogLoaded = false;
		invalidateToolCallRegex();
		__resetAgentPromptCacheForTests();
		toolRegistryVersion.value++;
		throw error;
	}
}

/** @internal Test-only catalog injection; production code must refresh the backend. */
export function __setAgentToolCatalogForTests(catalog: AgentToolCatalog): void {
	publishToolCatalog(catalog);
}

/**
 * listRegisteredTools returns the kinds of all currently registered tools.
 * Used by the UI to show available tools and by tests to verify registration.
 */
export function listRegisteredTools(): string[] {
  void toolRegistryVersion.value; // N-151: track reactive dependency
  return Array.from(toolRegistry.keys());
}

/**
 * getRegisteredTools returns the full ToolDef objects for all registered
 * tools. Used by the UI to display tool metadata (description, danger level)
 * and by getToolSchemaList to build the AI system prompt (N-16).
 */
export function getRegisteredTools(): ToolDef[] {
  void toolRegistryVersion.value; // N-151: track reactive dependency
  return Array.from(toolRegistry.values());
}

/**
 * getToolSchemaList builds a concise native-tool catalog summary for the
 * system prompt. The request's native tool schema remains authoritative;
 * this text must not teach the legacy fenced syntax as the primary protocol.
 */
export function getToolSchemaList(): string {
  const tools = getRegisteredTools();
  if (tools.length === 0) return "";
  const lines = tools.map((t) => {
    let line = `- \`${t.wireName ?? t.kind}\` — ${t.schema.description}`;
    if (t.schema.dangerLevel) {
      line += ` (risk: ${t.schema.dangerLevel})`;
    }
    return line;
  });
  return "Available tools: native function/tool-calling declarations (use the declared request schema):\n" + lines.join("\n");
}

/**
 * executeToolCall runs the given tool call and returns a string summary that
 * should be fed back to the AI as the "observation" in the agent loop.
 * The renderer has no executable ToolDef. Every call goes through the unified
 * backend capability facade using the catalog revision captured at parse time.
 */
async function previewWriteDiff(tc: ToolCall): Promise<void> {
	const path = typeof tc.arguments?.path === "string" ? tc.arguments.path : tc.target;
	const content = typeof tc.arguments?.content === "string" ? tc.arguments.content : String(tc.content ?? "");
	if (!path) return;
	let baseline = "";
	try {
		const raw = await fileService.readFile(path);
		baseline = typeof raw === "string" ? raw : "";
	} catch {
		baseline = "";
	}
	try {
		const diff = await computeFileDiffPreview(path, baseline, content);
		tc.writeDiff = diff;
		if (!tc.selectedHunks) tc.selectedHunks = diff.hunks.map((_, index) => index);
	} catch {
		tc.writeDiff = {
			path,
			oldContent: baseline,
			newContent: content,
			hunks: [{
				oldStart: 1,
				oldCount: baseline.split("\n").length,
				newStart: 1,
				newCount: content.split("\n").length,
				lines: [
					...baseline.split("\n").filter(Boolean).map((line) => ({ type: "removed" as const, content: line })),
					...content.split("\n").filter(Boolean).map((line) => ({ type: "added" as const, content: line })),
				],
			}],
			addedLines: content.split("\n").length,
			removedLines: baseline.split("\n").length,
		};
		tc.selectedHunks = [0];
	}
}

export function toggleWriteHunk(tc: ToolCall, hunkIdx: number): void {
	const selected = new Set(tc.selectedHunks ?? []);
	if (selected.has(hunkIdx)) selected.delete(hunkIdx);
	else selected.add(hunkIdx);
	tc.selectedHunks = [...selected].sort((a, b) => a - b);
}

export async function executeToolCall(tc: ToolCall): Promise<string> {
  const def = toolRegistry.get(tc.kind);
  if (!def) {
    throw new Error(`unknown tool call kind: ${tc.kind}`);
  }
	const catalogRevision = tc.catalogRevision ?? agentState.catalogRevision;
	const args = tc.arguments ?? legacyFenceArguments(def, tc.target, tc.content);
	await ensureAgentSession();
	const sessionId = backendAgentSessionId ?? tc.sessionId ?? agentState.sessionId;
  if (catalogRevision !== agentState.catalogRevision) {
    throw new Error("Agent tool catalog changed after this call was proposed; ask the model to retry");
  }
	if (!args || !sessionId || !def.schema.inputSchema || !validateToolArguments(def.schema.inputSchema, args)) {
    throw new Error("Agent tool call is missing its backend catalog binding");
  }
	const payload = { ...args };
	if (tc.kind === "write" && Array.isArray(tc.selectedHunks)) {
		payload.selectedHunks = tc.selectedHunks;
	}
	if (tc.kind === "write" && payload.selectedHunks !== undefined && !validateToolArguments(def.schema.inputSchema, payload)) {
		throw new Error("Agent write selectedHunks failed schema validation");
	}
	try {
		const result = await agentService.executeAgentTool({
			sessionId,
			catalogRevision,
			toolId: tc.kind,
			arguments: payload,
		});
		tc.execution = { requestSessionId: sessionId, result };
		await refreshToolBudget();
		if (tc.kind === "write") notifySuccess(`Wrote ${tc.target}`);
		return result.observation;
	} catch (error: unknown) {
		await refreshToolBudget();
		throw error;
	}
}

/**
 * approveToolCall executes the tool call and returns the observation string.
 * The caller (AiChatPanel) is responsible for feeding it back to the AI.
 * Updates the tool call's status and result fields in place.
 */
export async function approveToolCall(
  tc: ToolCall,
): Promise<string | null> {
  tc.status = "approved";
  recordToolStage(tc.id, tc.kind, "approved");
  recordToolStage(tc.id, tc.kind, "executing");
  try {
    const observation = await executeToolCall(tc);
    tc.status = "executed";
    tc.result = observation;
    recordToolStage(tc.id, tc.kind, "executed", observation);
    return observation;
  } catch (e: unknown) {
    tc.status = "error";
    tc.error = errorMessage(e);
    recordToolStage(tc.id, tc.kind, "error", tc.error);
    notifyError(`Tool call failed: ${tc.error}`);
    return `Error executing ${tc.kind} on "${tc.target}": ${tc.error}`;
  }
}

/**
 * rejectToolCall marks the tool call as rejected and returns a message the
 * caller can feed back to the AI so it knows the action was not performed.
 */
export function rejectToolCall(tc: ToolCall): string {
  tc.status = "rejected";
  recordToolStage(tc.id, tc.kind, "rejected");
  return `User rejected the ${tc.kind} action on "${tc.target}". Choose a different approach or ask the user for guidance.`;
}

/**
 * clearPendingToolCalls removes all tool calls (e.g. when starting a new
 * conversation or switching modes).
 */
export function clearPendingToolCalls(): void {
  agentState.pendingToolCalls = [];
  agentState.toolCallCount = 0;
	resetAgentSession();
  resetAgentTimeline();
}

// --- Agent loop wiring ---

/**
 * getAgentSystemPrompt returns the agent system prompt, fetching it from the
 * backend on first call and caching the result. Appends the list of
 * registered tools (N-16) so the AI knows what tools are available. The cache
 * is invalidated when tools are registered/unregistered.
 *
 * Plan 54: when the user has configured an agent prompt override
 * (appState.aiAgentSystemPrompt), that string is used as the base instead of
 * the built-in const. The override is read fresh on every call so settings
 * changes take effect on the next message without needing cache invalidation.
 */
export async function getAgentSystemPrompt(): Promise<string> {
  // Plan 54: user override takes precedence and is NOT cached (so settings
  // changes apply immediately). The built-in fetch is cached as before.
  const override = appState.aiAgentSystemPrompt;
  if (override && override.trim() !== "") {
    const toolList = getToolSchemaList();
    return override + (toolList ? "\n\n" + toolList : "");
  }
  if (agentSystemPromptCache !== null) return agentSystemPromptCache;
  try {
    const base = await aiService.getAgentSystemPrompt();
    const toolList = getToolSchemaList();
    agentSystemPromptCache = base + (toolList ? "\n\n" + toolList : "");
  } catch {
    // N-59: Fall back to the localized agent prompt from i18n so zh/ja
    // users get a prompt in their language when the backend is unavailable.
    const { translate } = await import("@/lib/i18n");
    const toolList = getToolSchemaList();
    agentSystemPromptCache = translate("prompts.agentSystem") + (toolList ? "\n\n" + toolList : "");
  }
  return agentSystemPromptCache;
}

/**
 * Resets the agent system prompt cache. Exposed for test isolation only.
 * @internal
 */
export function __resetAgentPromptCacheForTests(): void {
  agentSystemPromptCache = null;
}

/** Returns the permission mode captured when the active backend session began. */
export function getAgentPermissionMode(): AgentPermissionMode {
  return agentSessionPermissionMode;
}

/**
 * buildNativeToolDefs returns OpenAI-compatible tool definitions for every
 * registered agent tool (prompt-5 Task H). Passed to AIService.SetConfig so
 * the model can use native function calling; fence parsing remains as fallback.
 */

export function buildNativeToolDefs(): Array<{
  type: "function";
  function: {
    name: string;
    description: string;
    parameters: Record<string, unknown>;
  };
}> {
  return getRegisteredTools().map((td) => {
    return {
      type: "function" as const,
      function: {
				name: td.wireName ?? td.kind,
				description: td.schema.description || `Agent tool: ${td.kind}`,
				parameters: td.schema.inputSchema ?? {
					type: "object", properties: {}, additionalProperties: false,
				},
      },
    };
  });
}

export interface NativeToolCallPayload {
  id?: string;
  name: string;
  arguments: string;
}

/**
 * parseNativeToolCalls converts OpenAI/Anthropic native tool_calls into the
 * same ToolCall shape used by the fence parser (prompt-5 Task H dual-track).
 */
export function parseNativeToolCalls(payloads: NativeToolCallPayload[]): ToolCall[] {
  const out: ToolCall[] = [];
  const seen = new Set<string>();
  for (const p of payloads) {
    if (!p?.id || !p.name || seen.has(p.id)) return [];
		seen.add(p.id);
		const tool = findToolByCallName(p.name);
		if (!tool) return [];
		let parsed: unknown;
    try {
			parsed = JSON.parse(p.arguments || "{}");
    } catch {
			return [];
    }
		if (!tool.schema.inputSchema || !validateToolArguments(tool.schema.inputSchema, parsed)) return [];
		const args = parsed;
		const display = displayFields(tool, args);
    out.push({
      id: p.id,
      source: "native",
			kind: tool.kind,
			wireName: tool.wireName ?? tool.kind,
			target: display.target,
			content: display.content,
			arguments: args,
			catalogRevision: agentState.catalogRevision,
			sessionId: agentState.sessionId,
      status: "pending",
    });
  }
  return out;
}

function stableJSON(value: unknown): string {
	if (Array.isArray(value)) return `[${value.map(stableJSON).join(",")}]`;
	if (isRecordValue(value)) {
		return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${stableJSON(value[key])}`).join(",")}}`;
	}
	return JSON.stringify(value);
}

/** Stable dedup key for native/fence calls from the same catalog revision. */
export function toolCallDedupKey(tc: {
	kind: string;
	arguments?: Record<string, unknown>;
	catalogRevision?: number;
	target?: string;
	content?: string;
}): string {
	const content = stableJSON(tc.arguments ?? { target: tc.target ?? "", content: tc.content ?? "" });
  let h = 0;
  for (let i = 0; i < content.length; i++) {
    h = (Math.imul(31, h) + content.charCodeAt(i)) | 0;
  }
	return `${tc.catalogRevision ?? 0}\0${tc.kind}\0${h.toString(36)}`;
}

/**
 * prompt-7 Task D / BUG-H7 (D1): pending approvals stay on the originating
 * window. Peers get a toast + product copy; origin may focus its own window.
 */
function emitPendingUpdated(): void {
  try {
    const pending = agentState.pendingToolCalls.filter((tc) => tc.status === "pending");
    void Events.Emit("agent:pending-updated", {
      origin: getWindowOriginId(),
      count: pending.length,
      kinds: pending.map((tc) => tc.kind),
      // Signal peers that full approval UI is only on the origin window.
      approveOnlyOnOrigin: true,
      at: Date.now(),
    });
    if (pending.length > 0) {
      // Local strong guidance so the user does not leave without approving.
      notifyInfo(
        translate("agent.pendingApproveHere"),
        translate("agent.pendingApproveTitle"),
      );
    }
  } catch {
    // Events unavailable in tests.
  }
}

// Peer listener: show guidance when another window has pending tool calls.
let agentPendingListenerRegistered = false;
export function initAgentPendingSyncListener(): void {
  if (agentPendingListenerRegistered) return;
  agentPendingListenerRegistered = true;
}

export function handleAgentPendingUpdatedEvent(event: unknown): void {
  if (!agentPendingListenerRegistered) return;
  const payload = unwrapEventData(event) as { origin?: string; count?: number } | null;
  const origin = parseSyncOrigin(payload);
  if (origin && origin === getWindowOriginId()) return;
  const count = typeof payload?.count === "number" ? payload.count : 0;
  if (count <= 0) return;
  notifyWarning(
    translate("agent.pendingOnOtherWindow", { count: String(count) }),
    translate("agent.pendingApproveTitle"),
  );
}

export function cleanupAgentPendingSyncListener(): void {
  agentPendingListenerRegistered = false;
}

import.meta.hot?.dispose(cleanupAgentPendingSyncListener);

/**
 * enqueueToolCalls is the shared path for fence-parsed and native tool calls.
 */
export function enqueueToolCalls(calls: ToolCall[]): number {
  if (calls.length === 0) return 0;
  const existingIds = new Set(agentState.pendingToolCalls.map((call) => call.id));
  const acceptedIds = new Set<string>();
  const acceptedCalls = calls.filter((call) => {
    if (!call.id || existingIds.has(call.id) || acceptedIds.has(call.id)) return false;
    acceptedIds.add(call.id);
    return true;
  });
  if (acceptedCalls.length === 0) return 0;

  const firstIndex = agentState.pendingToolCalls.length;
  agentState.pendingToolCalls.push(...acceptedCalls);
  agentState.toolCallCount += acceptedCalls.length;
  // Read the just-inserted slice back from the reactive array. Vue wraps the
  // raw objects on access; mutating the caller's raw array would leave the UI
  // observing a different object and miss approved/executed/result changes.
  const reactiveCalls = agentState.pendingToolCalls.slice(
    firstIndex,
    firstIndex + acceptedCalls.length,
  );
  const expectedGeneration = agentTurnGeneration;
  const permissionMode = backendAgentSessionId
    ? agentSessionPermissionMode
    : normalizePermissionMode(appState.agentPermissionMode);
  for (const tc of reactiveCalls) {
    tc._turnGeneration = expectedGeneration;
    tc.permissionMode = permissionMode;
  }
  createAgentToolTurnBatch(reactiveCalls, expectedGeneration);
  for (const tc of reactiveCalls) {
		recordToolRequested(tc.id, tc.kind, tc.target);
    if (tc.kind === "run" && tc.status === "pending") {
      void checkRunRisk(tc);
    }
		if (tc.kind === "write" && tc.status === "pending") {
			void previewWriteDiff(tc);
		}
  }
  pushOutput(
    "agent",
    "info",
    `Agent emitted ${acceptedCalls.length} tool call(s) awaiting approval`,
  );
  if (maxIterationsReached.value) {
    notifyWarning(
      `Tool budget spent (${toolBudgetSpent.value}/${toolBudgetLimit.value}). `
      + "Further tool calls are refused by the backend until you start a new budget.",
    );
    pushOutput(
      "agent",
      "warn",
      `Tool budget exhausted (${toolBudgetSpent.value}/${toolBudgetLimit.value}); `
      + "backend will refuse new tool calls until a new budget epoch is opened.",
    );
  }
  emitPendingUpdated();
  if (permissionMode !== "always-ask") {
    void (async () => {
      try {
        for (const tc of reactiveCalls) {
          if (tc.status === "pending" && shouldAutoApprove(tc, permissionMode)) {
            await approveAndFeed(tc, expectedGeneration);
          }
        }
      } catch (error: unknown) {
        const message = errorMessage(error) || "Agent tool turn failed";
        notifyError(message, "Agent Error");
        pushOutput("agent", "error", message);
      } finally {
        emitPendingUpdated();
      }
    })();
  }
  return acceptedCalls.length;
}

/**
 * onNativeToolCalls handles ai:tool_calls events from the backend. Exact event
 * replay is idempotent; a provider reusing an ID for another invocation is an
 * invalid batch and returns -1 without mutating the queue.
 */
export function onNativeToolCalls(payloads: NativeToolCallPayload[]): number {
  const calls = parseNativeToolCalls(payloads);
  if (calls.length !== payloads.length) return -1;

  const fresh: ToolCall[] = [];
  for (const call of calls) {
    const identity = `${call.wireName ?? call.kind}\0${stableJSON(call.arguments ?? {})}`;
    const existingIdentity = nativeToolCallIdentities.get(call.id);
    if (existingIdentity !== undefined) {
      if (existingIdentity !== identity) return -1;
      continue;
    }
    if (agentState.pendingToolCalls.some((candidate) => candidate.id === call.id)) return -1;
    fresh.push(call);
  }
  for (const call of fresh) {
    nativeToolCallIdentities.set(
      call.id,
      `${call.wireName ?? call.kind}\0${stableJSON(call.arguments ?? {})}`,
    );
  }
  return enqueueToolCalls(fresh);
}

/**
 * Runs the explicitly marked fenced compatibility fallback after a completed
 * assistant turn. Any native tool-call event in that turn is authoritative,
 * so text fences from the same response are never parsed or executed.
 */
export function onAssistantFinished(
  assistantContent: string,
  nativeToolEventSeen = false,
): number {
  if (!assistantContent || nativeToolEventSeen) return 0;
  const calls = parseToolCalls(assistantContent);
  if (calls.length === 0) return 0;
  const existing = new Set(
    agentState.pendingToolCalls
      .filter((tc) => tc.status === "pending" || tc.status === "approved")
      .map((tc) => toolCallDedupKey(tc)),
  );
  const freshKeys = new Set<string>();
  const fresh = calls.filter((tc) => {
    const key = toolCallDedupKey(tc);
    if (existing.has(key) || freshKeys.has(key)) return false;
    freshKeys.add(key);
    return true;
  });
  return enqueueToolCalls(fresh);
}

/**
 * checkRunRisk calls the backend CheckCommand method to classify the
 * risk level of a `run` tool call and updates the tool call in place
 * (N-1). This populates the riskLevel and blockReason fields used by
 * the approval UI.
 */
export async function checkRunRisk(tc: ToolCall): Promise<void> {
  try {
    const check = await agentService.checkCommand(tc.target);
    tc.riskLevel = check.riskLevel;
    if (check.blocked) {
      tc.blockReason = check.blockReason;
    }
  } catch {
    // Best-effort — leave riskLevel undefined on error.
  }
}

/**
 * feedObservation sends an observation (tool-call result) back to the AI as a
 * new user message, continuing the agent loop. Imported lazily to avoid a
 * circular dependency with the ai store.
 */
export async function feedObservation(
	observation: string,
	expectedGeneration = agentTurnGeneration,
): Promise<void> {
	if (expectedGeneration !== agentTurnGeneration) return;
	await waitForAIStreamIdle();
	if (expectedGeneration !== agentTurnGeneration) return;
  // Inline dynamic import breaks the circular dep (ai.ts imports this module).
  const { sendMessage } = await import("@/stores/ai");
  await sendMessage(`[Observation]\n${observation}`);
}

/**
 * feedRejection sends a rejection message back to the AI so it knows the
 * action was not performed and can choose a different approach.
 */
export async function feedRejection(
	rejection: string,
	expectedGeneration = agentTurnGeneration,
): Promise<void> {
	if (expectedGeneration !== agentTurnGeneration) return;
	await waitForAIStreamIdle();
	if (expectedGeneration !== agentTurnGeneration) return;
  const { sendMessage } = await import("@/stores/ai");
  await sendMessage(`[Rejection]\n${rejection}`);
}

async function feedNativeToolResults(
	results: NativeToolResultContext[],
	expectedGeneration: number,
): Promise<void> {
	if (expectedGeneration !== agentTurnGeneration) return;
	await waitForAIStreamIdle();
	if (expectedGeneration !== agentTurnGeneration) return;
	const { sendNativeToolResults } = await import("@/stores/ai");
	const submitted = await sendNativeToolResults(results);
	if (!submitted) {
		throw new Error("Agent tool result was not submitted to the provider");
	}
}

function isTerminalToolCall(tc: ToolCall): boolean {
	return tc.status === "executed" || tc.status === "error" || tc.status === "rejected";
}

async function feedToolOutcome(
	tc: ToolCall,
	outcome: string,
	standaloneKind: "observation" | "rejection",
	expectedGeneration: number,
): Promise<void> {
	tc._turnObservation = outcome;
	if (!tc._turnBatchId) {
		if (tc.source === "native") {
			await feedNativeToolResults([{
				toolCallId: tc.id,
				content: outcome,
				isError: tc.status === "error" || tc.status === "rejected",
			}], expectedGeneration);
			return;
		}
		if (standaloneKind === "rejection") {
			await feedRejection(outcome, expectedGeneration);
		} else {
			await feedObservation(outcome, expectedGeneration);
		}
		return;
	}

	const batch = agentToolTurnBatches.get(tc._turnBatchId);
	if (!batch || batch.generation !== expectedGeneration || expectedGeneration !== agentTurnGeneration) return;
	if (batch.submitting) return;
	if (!batch.calls.every((call) => isTerminalToolCall(call) && typeof call._turnObservation === "string")) return;

	batch.submitting = true;
	const results = batch.calls.map((call) => ({
		callId: call.id,
		tool: call.wireName ?? call.kind,
		status: call.status,
		output: call._turnObservation,
	}));
	try {
		if (batch.calls.every((call) => call.source === "native")) {
			await feedNativeToolResults(batch.calls.map((call) => ({
				toolCallId: call.id,
				content: call._turnObservation as string,
				isError: call.status === "error" || call.status === "rejected",
			})), expectedGeneration);
		} else {
			await feedObservation(`[Tool Results]\n${JSON.stringify(results)}`, expectedGeneration);
		}
		agentToolTurnBatches.delete(batch.id);
	} catch (error) {
		batch.submitting = false;
		throw error;
	}
}

/**
 * approveAndFeed approves a tool call, executes it, and feeds the observation
 * back to the AI. Designed to be called directly from UI handlers.
 */
export async function approveAndFeed(
	tc: ToolCall,
	expectedGeneration = tc._turnGeneration ?? agentTurnGeneration,
): Promise<void> {
	try {
		await enqueueToolExecution(async () => {
			// Native tool calls are emitted while the assistant stream is still
			// active. Wait for its terminal event before executing and feeding the
			// observation, otherwise ai.sendMessage silently returns early.
			await waitForAIStreamIdle();
			if (expectedGeneration !== agentTurnGeneration || tc.status !== "pending") return;
			const observation = await approveToolCall(tc);
			if (observation !== null) {
				recordToolObservation(tc.id, tc.kind, observation);
				await feedToolOutcome(tc, observation, "observation", expectedGeneration);
			}
		}, expectedGeneration);
	} catch (error: unknown) {
		const message = errorMessage(error) || "Agent observation failed";
		notifyError(message, "Agent Error");
		pushOutput("agent", "error", message);
	}
}

/**
 * rejectAndFeed rejects a tool call and feeds the rejection back to the AI.
 */
export async function rejectAndFeed(
	tc: ToolCall,
	expectedGeneration = tc._turnGeneration ?? agentTurnGeneration,
): Promise<void> {
	try {
		await enqueueToolExecution(async () => {
			await waitForAIStreamIdle();
			if (expectedGeneration !== agentTurnGeneration || tc.status !== "pending") return;
			const rejection = rejectToolCall(tc);
			recordToolObservation(tc.id, tc.kind, rejection);
			await feedToolOutcome(tc, rejection, "rejection", expectedGeneration);
		}, expectedGeneration);
	} catch (error: unknown) {
		const message = errorMessage(error) || "Agent rejection turn failed";
		notifyError(message, "Agent Error");
		pushOutput("agent", "error", message);
	}
}
