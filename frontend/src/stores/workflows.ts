// Workflow store: loads multi-step workflow definitions from
// .koyori-ide/workflows/*.yml (via the backend WorkflowService) and runs them
// in a terminal session, respecting dependsOn ordering and condition
// gates (N-19).
// Koyori IDE 模块 · Workflows；交互服务：工作流（WorkflowService）。
// 喵，这是 Koyori IDE 的 Workflows 模块（前端实现）~
import { reactive, computed } from "vue";
import { Events } from "@wailsio/runtime";
import { workflowService } from "@/api/services";
import { appState } from "@/stores/app";
import { createSession, runCommandInSession, runCommandInSessionCapturing, killSession } from "@/stores/terminal";
import { pushOutput } from "@/stores/output";
import { notifyError, notifySuccess } from "@/lib/notifications";
import type { WorkflowDef, WorkflowStep, WorkflowStepState, FileSavedEvent, WorkflowValidationResult, WorkflowCompletedEvent } from "@/types";

interface WorkflowStoreState {
  workflows: WorkflowDef[];
  loading: boolean;
  errorMessage: string | null;
  // Per-workflow execution state, keyed by workflow name.
  running: Record<string, boolean>;
  stepStates: Record<string, WorkflowStepState[]>;
  // N-55: Per-workflow validation results, keyed by workflow name.
  // Populated by loadWorkflows via the backend ValidateAllWorkflows.
  // Used by the UI to mark invalid workflows with a red badge and
  // prevent execution.
  validation: Record<string, WorkflowValidationResult>;
}

export const workflowState = reactive<WorkflowStoreState>({
  workflows: [],
  loading: false,
  errorMessage: null,
  running: {},
  stepStates: {},
  validation: {},
});

export const hasWorkflows = computed(() => workflowState.workflows.length > 0);

// G-SEC-03: Startup workflows loaded from the project's .koyori-ide/workflows are
// untrusted (a cloned repository could contain malicious startup workflows).
// Instead of auto-running them on project load, the UI lists them as
// "Pending Confirmation" and the user must explicitly click "Run". This
// computed exposes the pending list so the UI can render a confirmation prompt.
export const pendingStartupWorkflows = computed(() =>
  workflowState.workflows.filter((wf) => wf.runOn?.event === "startup"),
);

// Load workflows for the given project root. A no-op when root is empty.
// Errors are surfaced to the store and a notification, but do not throw.
//
// N-55: After loading, each workflow is validated via the backend
// ValidateAllWorkflows. The results are stored in workflowState.validation
// (keyed by workflow name) so the UI can mark invalid workflows with a
// red badge and prevent execution. Invalid workflows are NOT filtered out
// of workflowState.workflows — the user should see them in the UI so they
// can fix the underlying .yml file.
export async function loadWorkflows(
  projectRoot: string,
  shouldApply: () => boolean = () => true,
): Promise<void> {
  if (!projectRoot) {
    workflowState.workflows = [];
    workflowState.errorMessage = null;
    workflowState.validation = {};
    return;
  }
  workflowState.loading = true;
  workflowState.errorMessage = null;
  try {
    const workflows = await workflowService.loadWorkflows(projectRoot);
    if (!shouldApply()) return;
    workflowState.workflows = workflows;
    // N-55: Validate all workflows at load time so the user sees errors
    // immediately rather than at run time. Errors are stored per-workflow
    // and surfaced in the UI. Validation failures do not block loading.
    try {
      const results = await workflowService.validateAllWorkflows(workflowState.workflows);
      if (!shouldApply()) return;
      workflowState.validation = {};
      for (const r of results) {
        workflowState.validation[r.workflowName] = r;
      }
    } catch (e: unknown) {
      // Validation is best-effort — if it fails, workflows still load.
      const msg = e instanceof Error ? e.message : String(e);
      pushOutput("workflow", "warn", `Workflow validation failed: ${msg}`);
    }
    // Register the file:saved event listener (idempotent) so workflows
    // with runOn triggers auto-run when matching files are saved.
    if (shouldApply()) initWorkflowTriggers();
  } catch (e: unknown) {
    if (!shouldApply()) return;
    workflowState.workflows = [];
    const msg = e instanceof Error ? e.message : String(e);
    workflowState.errorMessage = msg;
    notifyError(`Failed to load workflows: ${msg}`);
  } finally {
    if (shouldApply()) workflowState.loading = false;
  }
}

// N-55: Returns the validation result for a workflow, or null if not yet
// validated. Used by the UI to show error badges and block execution.
export function getWorkflowValidation(name: string): WorkflowValidationResult | null {
  return workflowState.validation[name] ?? null;
}

// ---- prompt-4 Task 12: 软件内 CRUD ----

/** 在当前项目创建新工作流并刷新列表。 */
export async function createWorkflow(name: string, def: WorkflowDef): Promise<boolean> {
  const root = appState.currentProject;
  if (!root) {
    notifyError("No project open");
    return false;
  }
  try {
    await workflowService.createWorkflow(root, name, def);
    await loadWorkflows(root);
    notifySuccess(`Workflow "${name}" created`);
    return true;
  } catch (e: unknown) {
    const msg = e instanceof Error ? e.message : String(e);
    notifyError(`Create workflow failed: ${msg}`);
    return false;
  }
}

/** 保存（覆盖）工作流定义。 */
export async function saveWorkflow(name: string, def: WorkflowDef): Promise<boolean> {
  const root = appState.currentProject;
  if (!root) {
    notifyError("No project open");
    return false;
  }
  try {
    await workflowService.saveWorkflow(root, name, def);
    await loadWorkflows(root);
    notifySuccess(`Workflow "${name}" saved`);
    return true;
  } catch (e: unknown) {
    const msg = e instanceof Error ? e.message : String(e);
    notifyError(`Save workflow failed: ${msg}`);
    return false;
  }
}

/** 删除工作流文件。 */
export async function deleteWorkflow(name: string): Promise<boolean> {
  const root = appState.currentProject;
  if (!root) {
    notifyError("No project open");
    return false;
  }
  try {
    await workflowService.deleteWorkflow(root, name);
    await loadWorkflows(root);
    notifySuccess(`Workflow "${name}" deleted`);
    return true;
  } catch (e: unknown) {
    const msg = e instanceof Error ? e.message : String(e);
    notifyError(`Delete workflow failed: ${msg}`);
    return false;
  }
}

/** 重命名工作流。 */
export async function renameWorkflow(oldName: string, newName: string): Promise<boolean> {
  const root = appState.currentProject;
  if (!root) {
    notifyError("No project open");
    return false;
  }
  try {
    await workflowService.renameWorkflow(root, oldName, newName);
    await loadWorkflows(root);
    notifySuccess(`Workflow renamed to "${newName}"`);
    return true;
  } catch (e: unknown) {
    const msg = e instanceof Error ? e.message : String(e);
    notifyError(`Rename workflow failed: ${msg}`);
    return false;
  }
}

// N-55: Returns true if the workflow is valid and can be run. A workflow
// is considered valid if it has no validation errors. Workflows that
// haven't been validated are assumed valid (backward compat).
export function isWorkflowValid(name: string): boolean {
  const result = workflowState.validation[name];
  return result ? result.valid : true;
}

// composeStepCommandLine mirrors the Go WorkflowStep.ComposeStepCommandLine
// so the frontend can build the shell command without a round-trip. Args
// are single-quoted with embedded-quote escaping (same scheme as tasks).
export function composeStepCommandLine(step: WorkflowStep): string {
  let out = step.command;
  for (const a of step.args ?? []) {
    out += " " + shellQuote(a);
  }
  return out;
}

function shellQuote(s: string): string {
  return "'" + s.replace(/'/g, `'\\''`) + "'";
}

// resolveStepCwd returns the directory the step should run in. Absolute
// step cwd values are used as-is; relative paths are joined to the
// project root (no escape).
export function resolveStepCwd(step: WorkflowStep, projectRoot: string): string {
  if (!step.cwd) return projectRoot;
  if (/^[A-Za-z]:[\\/]/.test(step.cwd) || step.cwd.startsWith("/")) {
    return step.cwd;
  }
  const root = projectRoot.replace(/[\\/]+$/, "");
  return root + "/" + step.cwd;
}

// ---------------------------------------------------------------------------
// Condition expression evaluator (N-28)
// ---------------------------------------------------------------------------

/**
 * A function that returns the status of a step by name, or undefined if
 * the step hasn't run or is unknown. Used by evaluateCondition to resolve
 * `steps.<name>.success` / `.failed` / `.skipped` references.
 *
 * Proposal F (prompt-5.md): Also supports `steps.<name>.outputs.<key>`
 * references — when the ref starts with "steps." and has 4+ parts, the
 * lookup returns the output value (string) instead of the status.
 */
export type StepStatusLookup = (
  stepName: string,
) => string | undefined;

/**
 * Proposal F: A function that returns the outputs of a step by name,
 * or undefined if the step has no outputs. Used by evaluateStepRef to
 * resolve `steps.<name>.outputs.<key>` references.
 */
export type StepOutputsLookup = (
  stepName: string,
) => Record<string, string> | undefined;

/**
 * evaluateCondition returns true if the step should run.
 *
 * Backward compatibility:
 *   - undefined / empty / whitespace → true (run)
 *   - "false" / "0" / "no" (case-insensitive) → false (skip)
 *   - "true" / "1" / "yes" (case-insensitive) → true (run)
 *
 * Expression language (N-28):
 *   - Step status: `steps.<name>.success`, `.failed`, `.skipped`
 *   - Boolean operators: `&&`, `||`, `!`
 *   - Literals: `true`, `false`
 *   - Parentheses: `( ... )`
 *
 * Examples:
 *   - `steps.build.success` — run only if build step succeeded
 *   - `steps.build.success && steps.test.success` — run if both succeeded
 *   - `!steps.lint.failed` — run unless lint failed
 *   - `steps.build.success || steps.build.failed` — always run after build
 *
 * Conditions that don't look like expressions (no `steps.` prefix, no
 * operators) fall through to the truthy default for backward compat.
 */
export function evaluateCondition(
  condition?: string,
  stepStatus?: StepStatusLookup,
  stepOutputs?: StepOutputsLookup,
): boolean {
  if (condition === undefined) return true;
  const trimmed = condition.trim();
  if (trimmed === "") return true;

  // Backward compatibility: simple falsy/truthy strings.
  const lower = trimmed.toLowerCase();
  if (lower === "false" || lower === "0" || lower === "no") return false;
  if (lower === "true" || lower === "1" || lower === "yes") return true;

  // If the condition looks like an expression, evaluate it.
  if (
    trimmed.startsWith("steps.") ||
    trimmed.includes("&&") ||
    trimmed.includes("||") ||
    trimmed.startsWith("!") ||
    trimmed.startsWith("(")
  ) {
    return evaluateExpression(trimmed, stepStatus, stepOutputs);
  }

  // Default: truthy (backward compat with free-form conditions).
  return true;
}

/**
 * Evaluates a condition expression. Supports step status references,
 * boolean operators, and parentheses via a simple recursive descent parser.
 */
function evaluateExpression(
  input: string,
  stepStatus?: StepStatusLookup,
  stepOutputs?: StepOutputsLookup,
): boolean {
  const tokens = tokenizeExpression(input);
  let pos = 0;

  const peek = (): Token | undefined => tokens[pos];
  const consume = (): Token => tokens[pos++];

  function parseOr(): boolean {
    let left = parseAnd();
    while (peek()?.type === "or") {
      consume();
      left = parseAnd() || left;
    }
    return left;
  }

  function parseAnd(): boolean {
    let left = parseNot();
    while (peek()?.type === "and") {
      consume();
      left = parseNot() && left;
    }
    return left;
  }

  function parseNot(): boolean {
    if (peek()?.type === "not") {
      consume();
      return !parseNot();
    }
    return parsePrimary();
  }

  function parsePrimary(): boolean {
    const tok = peek();
    if (!tok) return false;
    switch (tok.type) {
      case "true":
        consume();
        return true;
      case "false":
        consume();
        return false;
      case "lparen": {
        consume();
        const val = parseOr();
        if (peek()?.type === "rparen") consume();
        return val;
      }
      case "ref": {
        consume();
        return evaluateStepRef(tok.value, stepStatus, stepOutputs);
      }
      default:
        return false;
    }
  }

  return parseOr();
}

/** Token types for the expression parser. */
type Token =
  | { type: "and" }
  | { type: "or" }
  | { type: "not" }
  | { type: "lparen" }
  | { type: "rparen" }
  | { type: "true" }
  | { type: "false" }
  | { type: "ref"; value: string };

/** Tokenizes a condition expression into a stream of tokens. */
function tokenizeExpression(input: string): Token[] {
  const tokens: Token[] = [];
  let i = 0;
  while (i < input.length) {
    const c = input[i];
    if (c === " " || c === "\t") {
      i++;
      continue;
    }
    if (c === "&" && input[i + 1] === "&") {
      tokens.push({ type: "and" });
      i += 2;
      continue;
    }
    if (c === "|" && input[i + 1] === "|") {
      tokens.push({ type: "or" });
      i += 2;
      continue;
    }
    if (c === "!") {
      tokens.push({ type: "not" });
      i++;
      continue;
    }
    if (c === "(") {
      tokens.push({ type: "lparen" });
      i++;
      continue;
    }
    if (c === ")") {
      tokens.push({ type: "rparen" });
      i++;
      continue;
    }
    if (input.startsWith("true", i)) {
      tokens.push({ type: "true" });
      i += 4;
      continue;
    }
    if (input.startsWith("false", i)) {
      tokens.push({ type: "false" });
      i += 5;
      continue;
    }
    if (input.startsWith("steps.", i)) {
      let j = i + 6;
      while (j < input.length && /[a-zA-Z0-9_.]/.test(input[j])) j++;
      tokens.push({ type: "ref", value: input.slice(i, j) });
      i = j;
      continue;
    }
    // Unknown character — skip.
    i++;
  }
  return tokens;
}

/** Evaluates a step status reference like "steps.build.success". */
/**
 * evaluateStepRef resolves a `steps.<name>.<property>` reference to a
 * boolean. For status references (e.g. `steps.build.success`), it
 * compares the step's status to the property. For output references
 * (e.g. `steps.version.outputs.tag`), it returns true if the output
 * value is non-empty (truthy).
 *
 * Proposal F (prompt-5.md): added output reference support.
 *   - `steps.version.outputs.tag` → true if outputs.tag is non-empty
 *   - `steps.version.outputs.tag != ''` → handled by tokenizer as
 *     comparison (NOT YET SUPPORTED — only truthy check for now)
 *
 * For more complex output comparisons, use a dedicated condition
 * language feature in a future iteration.
 */
function evaluateStepRef(
  ref: string,
  stepStatus?: StepStatusLookup,
  stepOutputs?: StepOutputsLookup,
): boolean {
  const parts = ref.split(".");
  if (parts.length < 3 || parts[0] !== "steps") return false;
  const stepName = parts[1];

  // Proposal F: Handle `steps.<name>.outputs.<key>` references.
  if (parts.length >= 4 && parts[2] === "outputs") {
    if (!stepOutputs) return false;
    const outputs = stepOutputs(stepName);
    if (!outputs) return false;
    const key = parts.slice(3).join(".");
    const value = outputs[key];
    // Truthy check: non-empty string is true.
    return value !== undefined && value !== "";
  }

  // Status reference: `steps.<name>.<status>` where <status> is
  // success/failed/skipped/running/pending.
  const property = parts.slice(2).join(".");
  if (!stepStatus) return false;
  const status = stepStatus(stepName);
  if (status === undefined) return false;
  return status === property;
}

/**
 * Proposal F (prompt-5.md): Extract output values from a step's stdout
 * based on the `outputs` template map. Each template value is either:
 *   - "{{stdout}}" — returns the entire stdout (trimmed)
 *   - "{{regex:pattern}}" — returns the first match of the pattern
 *     (capturing group 1 if present, else the full match)
 *
 * Returns an object mapping output keys to extracted values. If a
 * template is invalid or doesn't match, the key is set to an empty
 * string (so conditions can check `!= ''`).
 *
 * This is a pure function — exported for testing.
 */
export function extractStepOutputs(
  stdout: string,
  templates: Record<string, string> | undefined,
): Record<string, string> {
  if (!templates) return {};
  const result: Record<string, string> = {};
  for (const [key, template] of Object.entries(templates)) {
    result[key] = extractOutputValue(stdout, template);
  }
  return result;
}

/**
 * Proposal F: Extract a single output value from stdout using a template.
 *   - "{{stdout}}" → trimmed stdout
 *   - "{{regex:pattern}}" → first match (group 1 if present, else full match)
 *   - anything else → returned as-is (literal string)
 */
function extractOutputValue(stdout: string, template: string): string {
  const trimmedTemplate = template.trim();
  if (trimmedTemplate === "{{stdout}}") {
    return stdout.trim();
  }
  // Match {{regex:PATTERN}} — capture the PATTERN (may contain parens,
  // so we use a non-greedy match up to the closing }}).
  const regexMatch = trimmedTemplate.match(/^\{\{regex:(.+)\}\}$/);
  if (regexMatch) {
    const pattern = regexMatch[1];
    try {
      const re = new RegExp(pattern);
      const match = re.exec(stdout);
      if (!match) return "";
      // If the pattern has a capturing group, use group 1; else full match.
      return match[1] ?? match[0];
    } catch {
      // Invalid regex — return empty.
      return "";
    }
  }
  // Literal string (no template syntax) — return as-is.
  return template;
}

/**
 * Proposal F (prompt-5.md): Substitute `{{steps.<name>.outputs.<key>}}`
 * placeholders in a command string with their actual values. Used when
 * composing step commands so that later steps can reference earlier
 * steps' outputs.
 *
 * Example: "docker build -t app:{{steps.version.outputs.tag}} ."
 *   → "docker build -t app:v1.2.3 ."
 *
 * If a referenced output doesn't exist, the placeholder is left as-is
 * (so the error is visible in the command rather than silently empty).
 */
export function substituteOutputRefs(
  command: string,
  stepOutputs: (stepName: string) => Record<string, string> | undefined,
): string {
  // Match {{steps.<name>.outputs.<key>}} — name and key are [a-zA-Z0-9_-]+
  return command.replace(
    /\{\{steps\.([a-zA-Z0-9_-]+)\.outputs\.([a-zA-Z0-9_-]+)\}\}/g,
    (match, stepName: string, key: string) => {
      const outputs = stepOutputs(stepName);
      if (outputs && outputs[key] !== undefined) {
        return outputs[key];
      }
      // Leave the placeholder if the output doesn't exist.
      return match;
    },
  );
}

// topologicalSort returns the steps in dependency order. Steps with no
// dependsOn come first; steps depending on others come after their
// dependencies. Throws if a cycle is detected (defensive — the backend
// also validates this, but we guard against hand-edited files).
export function topologicalSort(steps: WorkflowStep[]): WorkflowStep[] {
  const byName = new Map<string, WorkflowStep>();
  for (const s of steps) byName.set(s.name, s);

  const visited = new Map<string, number>(); // 0=unvisited, 1=in-progress, 2=done
  const result: WorkflowStep[] = [];

  function visit(name: string): void {
    const state = visited.get(name) ?? 0;
    if (state === 2) return;
    if (state === 1) {
      throw new Error(`Circular dependency detected at step "${name}"`);
    }
    visited.set(name, 1);
    const step = byName.get(name);
    if (step) {
      for (const dep of step.dependsOn ?? []) {
        if (!byName.has(dep)) {
          throw new Error(`Step "${name}" depends on unknown step "${dep}"`);
        }
        visit(dep);
      }
      result.push(step);
    }
    visited.set(name, 2);
  }

  for (const s of steps) visit(s.name);
  return result;
}

// initStepStates builds a fresh pending-state array for a workflow run.
function initStepStates(wf: WorkflowDef): WorkflowStepState[] {
  return wf.steps.map((s) => ({ name: s.name, status: "pending" }));
}

// N-58 (Proposal R): Maximum chain depth for workflow-completed triggers.
// Prevents infinite loops (A triggers B, B triggers A, ...). When this
// limit is reached, the chain trigger is suppressed with a warning.
const MAX_CHAIN_DEPTH = 5;
const pendingWorkflowDelays = new Map<
  ReturnType<typeof setTimeout>,
  (ready: boolean) => void
>();
let workflowRuntimeGeneration = 0;

function waitForWorkflowShellReady(generation: number): Promise<boolean> {
  if (generation !== workflowRuntimeGeneration) return Promise.resolve(false);
  return new Promise((resolve) => {
    const timer = setTimeout(() => {
      pendingWorkflowDelays.delete(timer);
      resolve(generation === workflowRuntimeGeneration);
    }, 80);
    pendingWorkflowDelays.set(timer, resolve);
  });
}

function settlePendingWorkflowDelays(): void {
  for (const [timer, resolve] of pendingWorkflowDelays) {
    clearTimeout(timer);
    resolve(false);
  }
  pendingWorkflowDelays.clear();
}

// runWorkflow executes a workflow's steps sequentially in a new terminal
// session, respecting dependsOn ordering and condition gates. Steps whose
// condition evaluates falsy are marked "skipped" and not executed. The
// first failed step aborts the run.
//
// N-58 (Proposal R): The chainDepth parameter tracks how many
// workflow-completed triggers led to this run. 0 = direct invocation
// (user action, file-saved trigger, or startup trigger). When another
// workflow's completion triggers this one, chainDepth is incremented.
// The MAX_CHAIN_DEPTH limit prevents infinite loops.
export async function runWorkflow(
  wf: WorkflowDef,
  projectRoot: string,
  chainDepth = 0,
): Promise<void> {
  if (workflowState.running[wf.name]) return;
  const runtimeGeneration = workflowRuntimeGeneration;

  // N-55: Block execution of invalid workflows. The user must fix the
  // validation errors in the .yml file before the workflow can run.
  const validation = getWorkflowValidation(wf.name);
  if (validation && !validation.valid) {
    const errMsgs = (validation.errors ?? [])
      .map((e) => `${e.field}: ${e.message}`)
      .join("; ");
    notifyError(`Workflow "${wf.name}" is invalid: ${errMsgs}`);
    pushOutput("workflow", "error", `Workflow "${wf.name}" blocked (invalid): ${errMsgs}`);
    return;
  }

  let ordered: WorkflowStep[];
  try {
    ordered = topologicalSort(wf.steps);
  } catch (e: unknown) {
    const msg = e instanceof Error ? e.message : String(e);
    notifyError(`Workflow "${wf.name}" invalid: ${msg}`);
    return;
  }

  workflowState.running[wf.name] = true;
  workflowState.stepStates[wf.name] = initStepStates(wf);
  const states = workflowState.stepStates[wf.name];
  // Index states by step name so we can look them up regardless of
  // execution order (which differs from declaration order after topo sort).
  const stateByName = new Map<string, WorkflowStepState>();
  for (const s of states) stateByName.set(s.name, s);

  appState.terminalVisible = true;
  pushOutput("workflow", "info", `Starting workflow "${wf.name}"`);

  let sessionId: string | null = null;
  try {
    // All steps share one terminal session so that side effects (env
    // vars, cd, files) persist across steps.
    sessionId = await createSession(projectRoot);
    if (!sessionId) {
      notifyError(`Failed to start terminal for workflow "${wf.name}"`);
      pushOutput("workflow", "error", `Workflow "${wf.name}": terminal session creation failed`);
      return;
    }
    // Small delay to let the shell prompt render before writing commands.
    // Matches the workaround used by runTask.
    if (!await waitForWorkflowShellReady(runtimeGeneration)) return;

    let failed = false;
    for (const step of ordered) {
      if (runtimeGeneration !== workflowRuntimeGeneration) return;
      const state = stateByName.get(step.name);
      if (!state) continue;
      // Proposal F: pass stepOutputs lookup so conditions can reference
      // `steps.<name>.outputs.<key>`.
      const stepOutputsLookup: StepOutputsLookup = (name: string) =>
        stateByName.get(name)?.outputs;
      if (!evaluateCondition(
        step.condition,
        (name) => stateByName.get(name)?.status,
        stepOutputsLookup,
      )) {
        state.status = "skipped";
        state.startedAt = Date.now();
        state.finishedAt = Date.now();
        pushOutput("workflow", "info", `Step "${step.name}" skipped (condition)`);
        continue;
      }
      if (failed) {
        state.status = "skipped";
        continue;
      }
      state.status = "running";
      state.startedAt = Date.now();
      // Proposal F: substitute {{steps.<name>.outputs.<key>}} placeholders
      // in the command with actual output values from previous steps.
      const rawCmd = composeStepCommandLine(step);
      const cmd = substituteOutputRefs(rawCmd, stepOutputsLookup);
      pushOutput("workflow", "info", `Step "${step.name}": ${cmd}`);
      // Proposal F: Use the capturing variant so we can extract outputs
      // from stdout. Falls back to the non-capturing variant if the step
      // has no outputs template (slightly more efficient).
      let exitCode: number;
      let output = "";
      if (step.outputs && Object.keys(step.outputs).length > 0) {
        const result = await runCommandInSessionCapturing(sessionId, cmd);
        exitCode = result.exitCode;
        output = result.output;
      } else {
        exitCode = await runCommandInSession(sessionId, cmd);
      }
      if (runtimeGeneration !== workflowRuntimeGeneration) return;
      state.finishedAt = Date.now();
      if (exitCode === 0) {
        state.status = "success";
        // Proposal F: extract outputs from stdout and store on the state.
        if (step.outputs && output) {
          state.outputs = extractStepOutputs(output, step.outputs);
        }
      } else {
        state.status = "failed";
        state.error =
          exitCode === -1
            ? "Timed out or session ended"
            : `Exit code: ${exitCode}`;
        pushOutput(
          "workflow",
          "error",
          `Step "${step.name}" failed (${state.error})`,
        );
        // expectSuccess defaults to true: a failed step aborts the run.
        // When explicitly set to false, the failure is non-fatal and
        // subsequent steps continue.
        const expectSuccess = step.expectSuccess !== false;
        if (expectSuccess) {
          failed = true;
        }
      }
    }

    if (runtimeGeneration !== workflowRuntimeGeneration) return;
    const anyFailed = states.some((s) => s.status === "failed");
    const success = !anyFailed;
    if (anyFailed) {
      pushOutput("workflow", "error", `Workflow "${wf.name}" failed`);
      notifyError(`Workflow "${wf.name}" failed`);
    } else {
      pushOutput("workflow", "success", `Workflow "${wf.name}" completed`);
      notifySuccess(`Workflow "${wf.name}" completed`);
    }
    // N-58 (Proposal R): Emit workflow:completed so downstream workflows
    // with runOn.event "workflow-completed" can chain. The payload includes
    // the chain depth so listeners can enforce MAX_CHAIN_DEPTH.
    Events.Emit("workflow:completed", {
      name: wf.name,
      success,
      chainDepth,
    });
  } catch (e: unknown) {
    if (runtimeGeneration !== workflowRuntimeGeneration) return;
    const msg = e instanceof Error ? e.message : String(e);
    notifyError(`Workflow "${wf.name}" error: ${msg}`);
    pushOutput("workflow", "error", `Workflow "${wf.name}" error: ${msg}`);
    // N-58: Emit workflow:completed with success=false even on error, so
    // downstream workflows that depend on the failed workflow can react.
    Events.Emit("workflow:completed", {
      name: wf.name,
      success: false,
      chainDepth,
    });
  } finally {
    // N-46: Kill the terminal session created for this workflow run.
    // Without this, every workflow run leaks a session in
    // terminalState.sessions and a backend PTY process. killSession is
    // called after pushOutput so the session's output buffer isn't
    // destroyed before the final log entry is written.
    if (sessionId) {
      await killSession(sessionId);
    }
    workflowState.running[wf.name] = false;
  }
}

// ---------------------------------------------------------------------------
// Glob matching and file:saved event triggers (Proposal B)
// ---------------------------------------------------------------------------

/**
 * matchGlob matches a file path against a glob pattern. Supports:
 *   - single-star matches any run of characters within a single path segment
 *   - double-star matches across path segments (including zero segments)
 *   - `?` matches a single character
 *   - literal characters match themselves
 *
 * The path and pattern use forward slashes. Matching is case-sensitive.
 * See matchGlob tests for concrete examples.
 */
export function matchGlob(path: string, pattern: string): boolean {
  // Normalize: collapse leading "./" and ensure forward slashes.
  const p = path.replace(/^\.\//, "");
  return matchGlobSegments(p.split("/"), pattern.split("/"));
}

/** Recursive segment matcher. `**` consumes zero or more path segments. */
function matchGlobSegments(pathSegs: string[], patternSegs: string[]): boolean {
  if (patternSegs.length === 0) {
    return pathSegs.length === 0;
  }
  const pat = patternSegs[0];
  if (pat === "**") {
    // `**` matches zero or more segments. Try consuming 0, 1, 2, ... segments.
    for (let i = 0; i <= pathSegs.length; i++) {
      if (matchGlobSegments(pathSegs.slice(i), patternSegs.slice(1))) {
        return true;
      }
    }
    return false;
  }
  if (pathSegs.length === 0) {
    return false;
  }
  if (!matchGlobSegment(pathSegs[0], pat)) {
    return false;
  }
  return matchGlobSegments(pathSegs.slice(1), patternSegs.slice(1));
}

/** Matches a single path segment against a pattern segment with `*` and `?`. */
function matchGlobSegment(seg: string, pattern: string): boolean {
  // Fast path: no wildcard.
  if (!pattern.includes("*") && !pattern.includes("?")) {
    return seg === pattern;
  }
  // Iterative match with `*` and `?` support within one segment.
  let si = 0;
  let pi = 0;
  let starIdx = -1;
  let matchIdx = 0;
  while (si < seg.length) {
    if (pi < pattern.length && (pattern[pi] === seg[si] || pattern[pi] === "?")) {
      si++;
      pi++;
    } else if (pi < pattern.length && pattern[pi] === "*") {
      starIdx = pi;
      matchIdx = si;
      pi++;
    } else if (starIdx !== -1) {
      pi = starIdx + 1;
      matchIdx++;
      si = matchIdx;
    } else {
      return false;
    }
  }
  while (pi < pattern.length && pattern[pi] === "*") {
    pi++;
  }
  return pi === pattern.length;
}

/**
 * relativizePath returns the path relative to projectRoot, using forward
 * slashes. If the path is not under projectRoot, returns the original path.
 * Used by the file:saved trigger to compute the relative path for glob
 * matching.
 */
export function relativizePath(absPath: string, projectRoot: string): string {
  const root = projectRoot.replace(/[\\/]+$/, "");
  // Normalize both to forward slashes for comparison.
  const normPath = absPath.replace(/\\/g, "/");
  const normRoot = root.replace(/\\/g, "/");
  if (normPath.startsWith(normRoot + "/")) {
    return normPath.slice(normRoot.length + 1);
  }
  if (normPath === normRoot) {
    return "";
  }
  return normPath;
}

/**
 * findTriggeredWorkflows returns the workflows that should auto-run when
 * the given relative file path is saved. A workflow matches when:
 *   - it has a `runOn` trigger with `event: "file-saved"`
 *   - it is not already running
 *   - the file path matches the trigger's glob (default: catch-all)
 *
 * This is a pure function extracted from initWorkflowTriggers for testing.
 * Proposal B / Plan 65.
 */
export function findTriggeredWorkflows(
  workflows: WorkflowDef[],
  relPath: string,
  running: Record<string, boolean>,
): WorkflowDef[] {
  if (relPath === "") return [];
  const result: WorkflowDef[] = [];
  for (const wf of workflows) {
    const trigger = wf.runOn;
    if (!trigger || trigger.event !== "file-saved") continue;
    if (running[wf.name]) continue;
    const glob = trigger.glob ?? "**/*";
    if (matchGlob(relPath, glob)) {
      result.push(wf);
    }
  }
  return result;
}

/**
 * Proposal J (prompt-4.md): Find workflows with a `runOn` trigger of
 * `event: "startup"`. A workflow matches when:
 *   - it has a `runOn` trigger with `event: "startup"`
 *   - it is not already running
 *
 * G-SEC-03: This is a pure lookup used to list startup workflows for user
 * confirmation. It does NOT auto-execute them — the UI must present the
 * returned workflows as "Pending Confirmation" and the user must explicitly
 * click "Run". This prevents malicious startup workflows in cloned
 * repositories from auto-running shell commands.
 */
export function findStartupWorkflows(
  workflows: WorkflowDef[],
  running: Record<string, boolean>,
): WorkflowDef[] {
  const result: WorkflowDef[] = [];
  for (const wf of workflows) {
    const trigger = wf.runOn;
    if (!trigger || trigger.event !== "startup") continue;
    if (running[wf.name]) continue;
    result.push(wf);
  }
  return result;
}

/**
 * N-58 (Proposal R): Find workflows that should auto-run when another
 * workflow completes. A workflow matches when:
 *   - it has a `runOn` trigger with `event: "workflow-completed"`
 *   - it is not already running
 *   - `runOn.workflowName` is empty (matches any) or equals `completedName`
 *
 * This is a pure function for testing. The listener in
 * initWorkflowTriggers uses this to determine which workflows to chain.
 */
export function findChainTriggeredWorkflows(
  workflows: WorkflowDef[],
  completedName: string,
  running: Record<string, boolean>,
): WorkflowDef[] {
  const result: WorkflowDef[] = [];
  for (const wf of workflows) {
    const trigger = wf.runOn;
    if (!trigger || trigger.event !== "workflow-completed") continue;
    if (running[wf.name]) continue;
    // Don't let a workflow trigger itself (simple cycle prevention).
    if (wf.name === completedName) continue;
    const target = trigger.workflowName ?? "";
    if (target === "" || target === completedName) {
      result.push(wf);
    }
  }
  return result;
}

let triggerListenerRegistered = false;

// N-149: Wails Events.On returns a cancel function. Collected here so the
// trigger listeners can be torn down during HMR / tests to avoid duplicates.
const workflowEventCancellers: Array<() => void> = [];

/**
 * initWorkflowTriggers registers listeners for workflow trigger events:
 *   - "file:saved" (Proposal B): runs workflows with runOn.event "file-saved"
 *     when a matching file is saved.
 *   - "workflow:completed" (Proposal R / N-58): runs workflows with
 *     runOn.event "workflow-completed" when another workflow finishes.
 *     The chain depth is incremented and capped at MAX_CHAIN_DEPTH to
 *     prevent infinite loops.
 *
 * This is idempotent: calling it multiple times registers listeners only
 * once. Should be called once at app startup after the first loadWorkflows.
 *
 * TODO: move to crossWindowSync.ts — file:saved / workflow:completed 的
 * Events.On 注册应集中到跨窗口同步层，但回调依赖本模块 runWorkflow 与
 * workflowState，且 file:saved 与 testExplorer 共享同一事件，迁移需先解决
 * 多消费者分发，暂留原处。
 *
 * Proposal B / Plan 65, Proposal R / N-58.
 */
export function initWorkflowTriggers(): void {
  if (triggerListenerRegistered) return;
  triggerListenerRegistered = true;
  // N-44: typed event payload (was `any`).
  workflowEventCancellers.push(
    Events.On("file:saved", (event: FileSavedEvent) => {
      const absPath: string = event?.data ?? "";
      if (typeof absPath !== "string" || absPath === "") return;
      const root = appState.currentProject ?? "";
      if (!root) return;
      const relPath = relativizePath(absPath, root);
      const triggered = findTriggeredWorkflows(
        workflowState.workflows,
        relPath,
        workflowState.running,
      );
      for (const wf of triggered) {
        void runWorkflow(wf, root);
      }
    }),
  );

  // N-58 (Proposal R): Listen for workflow:completed events to chain
  // downstream workflows. The chain depth prevents infinite loops.
  workflowEventCancellers.push(
    Events.On("workflow:completed", (event: WorkflowCompletedEvent) => {
      const payload = event?.data;
      if (!payload || typeof payload.name !== "string") return;
      const { name, chainDepth } = payload;
      if (chainDepth >= MAX_CHAIN_DEPTH) {
        pushOutput(
          "workflow",
          "warn",
          `Chain trigger suppressed: max depth (${MAX_CHAIN_DEPTH}) reached after "${name}"`,
        );
        return;
      }
      const root = appState.currentProject ?? "";
      if (!root) return;
      const triggered = findChainTriggeredWorkflows(
        workflowState.workflows,
        name,
        workflowState.running,
      );
      for (const wf of triggered) {
        pushOutput(
          "workflow",
          "info",
          `Chain trigger: "${wf.name}" triggered by completion of "${name}"`,
        );
        void runWorkflow(wf, root, chainDepth + 1);
      }
    }),
  );
}

/**
 * N-149: Cancels all workflow trigger event listeners. Intended for HMR
 * teardown in dev and test cleanup. After calling this, initWorkflowTriggers()
 * can be invoked again to re-register fresh listeners.
 */
export function cleanupWorkflowEventListeners(): void {
  for (const cancel of workflowEventCancellers) {
    try {
      cancel();
    } catch {
      // ignore — listener already removed
    }
  }
  workflowEventCancellers.length = 0;
  triggerListenerRegistered = false;
}

export function cleanupWorkflowRuntime(): void {
  workflowRuntimeGeneration += 1;
  settlePendingWorkflowDelays();
  cleanupWorkflowEventListeners();
}

import.meta.hot?.dispose(cleanupWorkflowRuntime);
