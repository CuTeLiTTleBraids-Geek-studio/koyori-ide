// Agent store: manages Agent mode state, tool-call parsing, and the
// approval→execute→feed-back loop. The AI emits tool calls as fenced code
// blocks whose first line is `read:`, `write:`, `run:`, or `search:`
// followed by the target. See AgentSystemPrompt in services/ai_prompts.go.
// Koyori IDE 模块 · Agent；交互服务：AI 对话（AIService）、自治 Agent（AgentService）、文件系统（FileService）。
// 喵，这是 Koyori IDE 的 Agent 模块（前端实现）~
import { reactive, computed, ref } from "vue";
import { Events } from "@wailsio/runtime";
import { fileService, searchService, agentService, aiService } from "@/api/services";
import { appState } from "@/stores/app";
import { pushOutput } from "@/stores/output";
import { notifyError, notifySuccess, notifyWarning, notifyInfo } from "@/lib/notifications";
import { errorMessage } from "@/lib/errors";
import { getWindowOriginId, unwrapEventData, parseSyncOrigin } from "@/lib/windowOrigin";
import { translate } from "@/lib/i18n";
import type { RiskLevel, ApprovalPolicy, ToolBudgetStatus } from "@/types";

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

export interface ToolCall {
  id: string;
  kind: ToolCallKind;
  // For read/write/run/search: the path or command or query on the first line.
  target: string;
  // For write: the full file content (rest of the code block).
  content?: string;
  status: ToolCallStatus;
  // Human-readable result summary, populated after execution.
  result?: string;
  // Error message, populated when status === "error".
  error?: string;
  // Risk level for `run` tool calls, populated asynchronously by
  // checkRunRisk() after the tool call is added to the pending queue
  // (N-1). Used by the approval UI to show a risk badge.
  riskLevel?: RiskLevel;
  // Block reason when the command matches the denylist (N-1).
  blockReason?: string;
  // Plan 47: in-flight risk check promise. Used by applyApprovalPolicy to
  // await the risk check without re-triggering it. Not persisted.
  _riskCheckPromise?: Promise<void>;
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
}

export const agentState = reactive<AgentStoreState>({
  mode: "chat",
  pendingToolCalls: [],
  toolCallCount: 0,
  budget: null,
});

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
  agentState.mode = mode;
}

export function toggleMode(): void {
  agentState.mode = agentState.mode === "chat" ? "agent" : "chat";
  // Clear pending approvals when switching modes.
  agentState.pendingToolCalls = [];
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
  const kinds = Array.from(toolRegistry.keys());
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

function invalidateToolCallRegex(): void {
  cachedToolCallRe = null;
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
    const kind = match[2] as ToolCallKind;
    const target = match[3].trim();
    // match[5] is undefined when there's no newline after the target,
    // or "" when the content block is empty. Strip a trailing newline
    // (always present when content is non-empty due to the closing fence).
    const rawContent = match[5];
    const content =
      rawContent && rawContent.length > 0
        ? rawContent.replace(/\n+$/, "")
        : undefined;
    calls.push({
      id: nextToolCallId(),
      kind,
      target,
      content,
      status: "pending",
    });
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
  // Remove the tool-call blocks from the rendered message.
  const re = getToolCallRegex();
  re.lastIndex = 0;
  const cleanedMessage = message.replace(re, "").trim();
  return { toolCalls, cleanedMessage };
}

/**
 * resolveProjectPath validates that `target` is a relative path within the
 * currently open project root, and returns the absolute path. Rejects absolute
 * paths and parent-traversal paths before they reach FileService, so the AI
 * gets a clear, structured error instead of a low-level validation failure
 * (#25 / N-3).
 */
function resolveProjectPath(
  target: string,
): { ok: true; absPath: string } | { ok: false; error: string } {
  const root = appState.currentProject;
  if (!root) {
    return { ok: false, error: "No project open" };
  }
  // Reject absolute paths (Windows drive letter or POSIX root).
  if (/^([a-zA-Z]:[\\/]|[\\/])/.test(target)) {
    return {
      ok: false,
      error: `Absolute paths are not allowed: ${target}. Use a path relative to the project root.`,
    };
  }
  // Normalize and reject parent traversal that escapes the root.
  const parts = target.replace(/\\/g, "/").split("/");
  const normalized: string[] = [];
  for (const p of parts) {
    if (p === "." || p === "") continue;
    if (p === "..") {
      if (normalized.length === 0) {
        return { ok: false, error: `Path escapes project root: ${target}` };
      }
      normalized.pop();
    } else {
      normalized.push(p);
    }
  }
  const relPath = normalized.join("/");
  if (!relPath) {
    return { ok: false, error: `Empty path after normalization: ${target}` };
  }
  const absPath = root.replace(/[\\/]+$/, "") + "/" + relPath;
  return { ok: true, absPath };
}

/**
 * ToolExecutor is the signature of a registered tool's execute function.
 * It receives the parsed ToolCall and returns a string observation to feed
 * back to the AI. Errors should be thrown; the caller (approveToolCall)
 * catches them and marks the call as failed.
 */
export type ToolExecutor = (tc: ToolCall) => Promise<string>;

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
}

/**
 * ToolDef describes a registered agent tool. Custom tools (N-16) register
 * via the same shape, extending the toolRegistry Map. The `schema` field
 * provides metadata for the AI system prompt and approval UI.
 */
export interface ToolDef {
  kind: string;
  schema: ToolSchema;
  execute: ToolExecutor;
}

/**
 * toolRegistry maps tool kinds to their executors. Built-in tools are
 * registered here so that future custom tools (from plugins or config) can
 * be added via `registerTool` without modifying the dispatch logic (#25 / N-16).
 */
const toolRegistry = new Map<string, ToolDef>();

// N-151: toolRegistry itself is a plain Map (not reactive). To let UI
// consumers track registrations that happen after mount (e.g. plugin tools
// loaded asynchronously), we expose a reactive version counter. Reads of
// getRegisteredTools() / listRegisteredTools() touch it, so any computed
// that calls them re-evaluates when a tool is registered or unregistered.
const toolRegistryVersion = ref(0);

/**
 * registerTool adds (or replaces) a tool in the registry. Exposed so that
 * future plugin/config code can register custom agent tools. Invalidates the
 * regex and prompt caches so the new tool is immediately recognized by the
 * parser and included in the AI system prompt (N-16).
 */
export function registerTool(def: ToolDef): void {
  toolRegistry.set(def.kind, def);
  invalidateToolCallRegex();
  __resetAgentPromptCacheForTests();
  toolRegistryVersion.value++;
}

/**
 * unregisterTool removes a tool from the registry. Returns true if a tool
 * was removed, false if the kind was not registered. Invalidates caches.
 */
export function unregisterTool(kind: string): boolean {
  const removed = toolRegistry.delete(kind);
  if (removed) {
    invalidateToolCallRegex();
    __resetAgentPromptCacheForTests();
    toolRegistryVersion.value++;
  }
  return removed;
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
 * getToolSchemaList builds a markdown-formatted list of all registered tools
 * for inclusion in the AI system prompt (N-16). The AI uses this list to know
 * which tools it can emit and what each tool does.
 */
export function getToolSchemaList(): string {
  const tools = getRegisteredTools();
  if (tools.length === 0) return "";
  const lines = tools.map((t) => {
    let line = `- \`${t.kind}:\` ${t.schema.description}`;
    if (t.schema.dangerLevel) {
      line += ` (risk: ${t.schema.dangerLevel})`;
    }
    return line;
  });
  return "Available tools:\n" + lines.join("\n");
}

// --- Built-in tool executors ---

async function executeReadTool(tc: ToolCall): Promise<string> {
  // prompt-7 Task G / BUG-M18: Agent reads require an open project root.
  if (!appState.currentProject) {
    throw new Error("no workspace root: open a project before Agent read tools");
  }
  const resolved = resolveProjectPath(tc.target);
  if (!resolved.ok) {
    throw new Error(resolved.error);
  }
  const content = await fileService.readFile(resolved.absPath);
  // Truncate very large files so we don't blow the context window.
  const max = 8000;
  const truncated =
    content.length > max
      ? content.slice(0, max) + `\n... [truncated, ${content.length} total chars]`
      : content;
  return `Read ${tc.target}:\n\`\`\`\n${truncated}\n\`\`\``;
}

async function executeWriteTool(tc: ToolCall): Promise<string> {
  // G-02: write-file capability — mirrors executeRunTool.
  // The renderer must request backend approval; only the backend can issue
  // a write token bound to (absPath, contentHash, workspace generation, TTL).
  if (!tc.content) {
    throw new Error("write tool call missing file content");
  }
  const resolved = resolveProjectPath(tc.target);
  if (!resolved.ok) {
    throw new Error(resolved.error);
  }

  // Compute SHA-256 of the new content to bind the approval to the exact bytes.
  const encoder = new TextEncoder();
  const data = encoder.encode(tc.content);
  const hashBuffer = await crypto.subtle.digest("SHA-256", data);
  const contentHash = Array.from(new Uint8Array(hashBuffer))
    .map((b) => b.toString(16).padStart(2, "0"))
    .join("");

  // Request backend approval — shows a native dialog to the user.
  const approvalToken = await agentService.requestWriteApproval(
    resolved.absPath,
    contentHash,
    data.byteLength,
  );

  // Execute with the backend-issued token — path, hash, and generation are
  // all re-verified on the backend before any byte reaches disk.
  await agentService.executeApprovedWrite(resolved.absPath, tc.content, approvalToken);
  notifySuccess(`Wrote ${tc.target}`);
  return `Wrote ${tc.target} (${tc.content.length} chars).`;
}

async function executeRunTool(tc: ToolCall): Promise<string> {
  // prompt-5 Task E: refuse run when no workspace is open (empty root would
  // widen the process cwd sandbox surface).
  const cwd = appState.currentProject;
  if (!cwd) {
    throw new Error("No project open: run tool requires an open workspace");
  }
  if (!tc.target || !tc.target.trim()) {
    throw new Error("run tool requires a command — the AI returned an empty target");
  }
  // GOAL-P1-02: this call is where backend budget is actually spent. The
  // backend charges one unit when it mints the capability token, so the budget
  // is refreshed here — on both success and refusal — rather than at emission
  // time, where nothing has been charged yet.
  let approvalToken: string;
  try {
    approvalToken = await agentService.requestCommandApproval(tc.target, cwd);
  } catch (e: unknown) {
    // A refusal is authoritative state: reconcile before rethrowing so the UI
    // shows the exhausted budget rather than a bare error.
    await refreshToolBudget();
    throw e;
  }
  await refreshToolBudget();
  const result = await agentService.executeApprovedCommand(tc.target, cwd, approvalToken);
  const summary =
    `Ran: ${result.command}\n` +
    `Exit code: ${result.exitCode} (${result.durationMs}ms)\n` +
    (result.stdout ? `stdout:\n\`\`\`\n${result.stdout}\n\`\`\`\n` : "") +
    (result.stderr ? `stderr:\n\`\`\`\n${result.stderr}\n\`\`\`\n` : "");
  pushOutput(
    "agent",
    result.exitCode === 0 ? "info" : "warn",
    `Agent ran "${result.command}" → exit ${result.exitCode}`,
  );
  return summary;
}

async function executeSearchTool(tc: ToolCall): Promise<string> {
  // prompt-7 Task G: Agent search also requires project root.
  const root = appState.currentProject;
  if (!root) {
    throw new Error("no workspace root: open a project before Agent search tools");
  }
  const results = await searchService.search(root, tc.target, true);
  // Flatten results: each SearchResult has a path + matches[].
  const allMatches: { path: string; line: number; column: number; preview: string }[] = [];
  for (const r of results) {
    for (const m of r.matches) {
      allMatches.push({ path: r.path, line: m.line, column: m.column, preview: m.preview });
    }
  }
  if (allMatches.length === 0) {
    return `No matches found for "${tc.target}".`;
  }
  const maxResults = 10;
  const top = allMatches.slice(0, maxResults);
  const summary =
    `Found ${allMatches.length} match(es) for "${tc.target}" (showing top ${top.length}):\n` +
    top
      .map(
        (m) =>
          `- ${m.path}:${m.line}:${m.column}: ${m.preview.trim()}`,
      )
      .join("\n");
  return summary;
}

// Register built-in tools. Done at module load so they're available
// immediately. Custom tools can be registered later via registerTool().
// Each tool includes a schema (N-16) with a description and default danger
// level used in the AI system prompt and approval UI.
registerTool({
  kind: "read",
  schema: {
    description: "Read a file from the project. Target is a path relative to the project root.",
    dangerLevel: "safe",
  },
  execute: executeReadTool,
});
registerTool({
  kind: "write",
  schema: {
    description: "Write or overwrite a file in the project. Target is a relative path, content is the file body.",
    dangerLevel: "elevated",
  },
  execute: executeWriteTool,
});
registerTool({
  kind: "run",
  schema: {
    description: "Execute a single command with arguments in the project root. The command is parsed into an argv and executed directly (no shell wrapper: no `sh -c`, no `cmd /c`). Pipes (|), redirects (> <), variable expansion ($VAR), command substitution (backtick or $()), chaining (&& ;), background (&), glob (* ?), brace expansion ({a,b}), and home expansion (~) are rejected. To pipe or chain, emit separate run calls and inspect the observation between them. Subject to sandbox validation and risk classification.",
    dangerLevel: "elevated",
  },
  execute: executeRunTool,
});
registerTool({
  kind: "search",
  schema: {
    description: "Search for a text pattern across the project. Target is the search query.",
    dangerLevel: "safe",
  },
  execute: executeSearchTool,
});

/**
 * executeToolCall runs the given tool call and returns a string summary that
 * should be fed back to the AI as the "observation" in the agent loop.
 * Dispatches via the toolRegistry Map (#25 / N-16).
 */
export async function executeToolCall(tc: ToolCall): Promise<string> {
  const def = toolRegistry.get(tc.kind);
  if (!def) {
    throw new Error(`unknown tool call kind: ${tc.kind}`);
  }
  return def.execute(tc);
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
  try {
    const observation = await executeToolCall(tc);
    tc.status = "executed";
    tc.result = observation;
    return observation;
  } catch (e: unknown) {
    tc.status = "error";
    tc.error = errorMessage(e);
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
  return `User rejected the ${tc.kind} action on "${tc.target}". Choose a different approach or ask the user for guidance.`;
}

/**
 * clearPendingToolCalls removes all tool calls (e.g. when starting a new
 * conversation or switching modes).
 */
export function clearPendingToolCalls(): void {
  agentState.pendingToolCalls = [];
  agentState.toolCallCount = 0;
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

/**
 * getApprovalPolicy returns the configured approval policy for a tool kind
 * (Plan 47). Reads from appState.toolApprovalConfig; missing entries default
 * to "always-ask". Exposed for tests and the settings UI.
 */
export function getApprovalPolicy(kind: ToolCallKind): ApprovalPolicy {
  const cfg = appState.toolApprovalConfig[kind];
  if (cfg === "auto-approve" || cfg === "never-approve") return cfg;
  return "always-ask";
}

/**
 * shouldAutoApprove determines whether a tool call should be auto-approved
 * based on the configured policy.
 *
 * G-SEC-02: `run` tools are NEVER auto-approved — every shell command
 * requires explicit manual approval, regardless of the configured policy
 * or risk level. The denylist is not a security boundary (only auxiliary
 * filtering), so even "Safe"-classified or unblocked commands must be
 * approved by the user.
 *
 * prompt-5 Task E / BUG-M4: `write` tools are also NEVER auto-approved so
 * a hallucinating agent cannot silently modify files. Only `read`/`search`
 * (and other safe tools) may use auto-approve.
 */
export function shouldAutoApprove(tc: ToolCall): boolean {
  // G-SEC-02: run commands always require manual approval.
  if (tc.kind === "run") return false;
  // prompt-5 Task E: write always requires manual approval.
  if (tc.kind === "write") return false;
  if (getApprovalPolicy(tc.kind) !== "auto-approve") return false;
  return true;
}

/**
 * applyApprovalPolicy applies the configured approval policy to a pending
 * tool call (Plan 47). Called fire-and-forget from onAssistantFinished.
 *
 * - "auto-approve": executes the call and feeds the observation back to
 *   the AI without waiting for user interaction.
 * - "never-approve": rejects the call and feeds the rejection back.
 * - "always-ask": no-op (the call stays in the pending queue).
 *
 * G-SEC-02: `run` tools are NEVER auto-approved. Even when the policy is
 * "auto-approve", run commands stay in the pending queue for mandatory
 * manual approval. The denylist is not a security boundary, so no command
 * bypasses approval — blocked or not. For `run` tools, this still waits
 * for the risk check to complete so the risk badge is populated in the UI.
 */
export async function applyApprovalPolicy(tc: ToolCall): Promise<void> {
  // For `run` tools, wait for the risk check so the UI badge is populated.
  if (tc.kind === "run" && tc._riskCheckPromise) {
    await tc._riskCheckPromise;
  }
  if (tc.status !== "pending") return;
  // G-SEC-02: run commands always require manual approval — never
  // auto-approve, regardless of the configured policy.
  if (tc.kind === "run") return;
  const policy = getApprovalPolicy(tc.kind);
  if (policy === "auto-approve") {
    pushOutput(
      "agent",
      "info",
      `Auto-approving ${tc.kind} tool call: ${tc.target}`,
    );
    await approveAndFeed(tc);
  } else if (policy === "never-approve") {
    pushOutput(
      "agent",
      "info",
      `Auto-rejecting ${tc.kind} tool call per policy: ${tc.target}`,
    );
    await rejectAndFeed(tc);
  }
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
    const kind = td.kind;
    const properties: Record<string, unknown> = {};
    const required: string[] = [];
    if (kind === "write") {
      properties.path = { type: "string", description: "Relative path within the project" };
      properties.content = { type: "string", description: "Full file content to write" };
      required.push("path", "content");
    } else if (kind === "run") {
      properties.command = { type: "string", description: "Command and arguments (no shell)" };
      required.push("command");
    } else if (kind === "search") {
      properties.query = { type: "string", description: "Search query" };
      required.push("query");
    } else {
      // read + custom tools default to path/target
      properties.path = { type: "string", description: "Relative path or tool target" };
      required.push("path");
    }
    return {
      type: "function" as const,
      function: {
        name: kind,
        description: td.schema.description || `Agent tool: ${kind}`,
        parameters: {
          type: "object",
          properties,
          required,
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
  for (const p of payloads) {
    if (!p?.name) continue;
    let args: Record<string, unknown> = {};
    try {
      args = p.arguments ? (JSON.parse(p.arguments) as Record<string, unknown>) : {};
    } catch {
      args = {};
    }
    const kind = p.name;
    let target = "";
    let content: string | undefined;
    if (kind === "write") {
      target = String(args.path ?? args.target ?? "");
      content = args.content != null ? String(args.content) : undefined;
    } else if (kind === "run") {
      target = String(args.command ?? args.target ?? "");
    } else if (kind === "search") {
      target = String(args.query ?? args.target ?? "");
    } else {
      target = String(args.path ?? args.target ?? args.query ?? "");
    }
    if (!target && !content) continue;
    out.push({
      id: p.id || nextToolCallId(),
      kind,
      target,
      content,
      status: "pending",
    });
  }
  return out;
}

/** prompt-6 Task 11: stable dedup key including content hash for write. */
export function toolCallDedupKey(tc: { kind: string; target: string; content?: string }): string {
  const content = tc.content ?? "";
  // Simple non-crypto hash for dual-track dedup (same content → same key).
  let h = 0;
  for (let i = 0; i < content.length; i++) {
    h = (Math.imul(31, h) + content.charCodeAt(i)) | 0;
  }
  return `${tc.kind}\0${tc.target}\0${h.toString(36)}`;
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
  agentState.pendingToolCalls.push(...calls);
  agentState.toolCallCount += calls.length;
  for (const tc of calls) {
    if (tc.kind === "run" && tc.status === "pending") {
      tc._riskCheckPromise = checkRunRisk(tc);
    }
  }
  pushOutput(
    "agent",
    "info",
    `Agent emitted ${calls.length} tool call(s) awaiting approval`,
  );
  if (maxIterationsReached.value) {
    // The wording states a refusal rather than offering advice: the backend
    // will not mint another capability token until a new epoch is opened, so
    // describing this as "consider starting a new conversation" would
    // understate it and leave the user waiting for calls that never run.
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
  void (async () => {
    for (const tc of calls) {
      if (tc.status === "pending") {
        await applyApprovalPolicy(tc);
      }
    }
    emitPendingUpdated();
  })();
  return calls.length;
}

/**
 * onNativeToolCalls handles ai:tool_calls events from the backend.
 */
export function onNativeToolCalls(payloads: NativeToolCallPayload[]): number {
  return enqueueToolCalls(parseNativeToolCalls(payloads));
}

/**
 * onAssistantFinished should be called by the ai store when an assistant
 * message finishes streaming in agent mode. Parses tool calls from the
 * message (fence dual-track) and appends them to the pending queue.
 * Native tool_calls may already have been enqueued via onNativeToolCalls;
 * fence parse still runs for models that only speak the text protocol.
 *
 * Returns the number of tool calls added (0 if none).
 */
export function onAssistantFinished(assistantContent: string): number {
  if (!assistantContent) return 0;
  const calls = parseToolCalls(assistantContent);
  if (calls.length === 0) return 0;
  // Dual-track: if native tool_calls already populated the queue, skip fence
  // duplicates by kind+target+content hash (prompt-6 Task 11 / BUG-L12).
  const existing = new Set(
    agentState.pendingToolCalls.map((tc) => toolCallDedupKey(tc)),
  );
  const fresh = calls.filter((tc) => !existing.has(toolCallDedupKey(tc)));
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
export async function feedObservation(observation: string): Promise<void> {
  // Inline dynamic import breaks the circular dep (ai.ts imports this module).
  const { sendMessage } = await import("@/stores/ai");
  await sendMessage(`[Observation]\n${observation}`);
}

/**
 * feedRejection sends a rejection message back to the AI so it knows the
 * action was not performed and can choose a different approach.
 */
export async function feedRejection(rejection: string): Promise<void> {
  const { sendMessage } = await import("@/stores/ai");
  await sendMessage(`[Rejection]\n${rejection}`);
}

/**
 * approveAndFeed approves a tool call, executes it, and feeds the observation
 * back to the AI. Designed to be called directly from UI handlers.
 */
export async function approveAndFeed(tc: ToolCall): Promise<void> {
  const observation = await approveToolCall(tc);
  if (observation !== null) {
    await feedObservation(observation);
  }
}

/**
 * rejectAndFeed rejects a tool call and feeds the rejection back to the AI.
 */
export async function rejectAndFeed(tc: ToolCall): Promise<void> {
  const rejection = rejectToolCall(tc);
  await feedRejection(rejection);
}
