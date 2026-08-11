// Rules store: loads project-level AI rules files (#25, N-18) and exposes
// the rules content for prepending to the AI system prompt.
//
// N-18 adds configurable candidate paths and merge mode. When mode is
// "merge", all existing rules files are concatenated in priority order.
// When mode is "first" (default), only the first existing file is used.
// Koyori IDE 模块 · Rules。
// 喵，这是 Koyori IDE 的 Rules 模块（前端实现）~
import { reactive, computed } from "vue";
import { rulesService } from "@/api/services";
import { pushOutput } from "@/stores/output";
import { notifyError, notifySuccess } from "@/lib/notifications";
import { errorMessage } from "@/lib/errors";
import type { RulesFile, RulesFileCandidate, RulesConfig, RulesCandidateConfig } from "@/types";

export interface RulesState {
  // All loaded rules files in priority order (N-18). Empty when none exist.
  rulesFiles: RulesFile[];
  // The currently edited rules file path (for the editor UI).
  // True while loading rules from disk.
  loading: boolean;
  // Error message from the last load/save operation, or null.
  error: string | null;
  // All candidate rules file locations and their existence status.
  candidates: RulesFileCandidate[];
  // The merged rules configuration (N-18).
  config: RulesConfig | null;
}

export const rulesState = reactive<RulesState>({
  rulesFiles: [],
  loading: false,
  error: null,
  candidates: [],
  config: null,
});

// Backward-compat: the first loaded rules file, or null.
// Existing code that only cares about a single rules file can use this.
export const rules = computed<RulesFile | null>(
  () => rulesState.rulesFiles[0] ?? null,
);

// True when at least one non-empty rules file is loaded.
export const hasRules = computed(
  () => rulesState.rulesFiles.some((r) => r.content.trim().length > 0),
);

// The number of loaded rules files (N-18 merge mode).
export const rulesFileCount = computed(() => rulesState.rulesFiles.length);

/**
 * N-71 / Proposal AG: pattern matching XML-like tags that could be used for
 * prompt injection inside rules content. Stripped before the content is
 * wrapped in <project_rules> delimiters. Must match the Go-side regex in
 * ai_prompts.go (dangerousTagPattern).
 */
const DANGEROUS_TAG_RE = /<\/?(?:system|instructions?|prompt|role|assistant|developer|openai)\s*>/gi;

/**
 * N-71 / Proposal AG: structured delimiters that wrap project rules content
 * in the system prompt. Must match the Go-side constants in ai_prompts.go.
 * The system prompt declares that content inside these tags is project
 * context data, NOT system instructions.
 */
const RULES_OPEN_TAG = "<project_rules>";
const RULES_CLOSE_TAG = "</project_rules>";

/**
 * Sanitizes rules content by stripping prompt-injection tags (N-71).
 * Mirrors services.SanitizeRulesContent in Go.
 */
function sanitizeRulesContent(content: string): string {
  return content.replace(DANGEROUS_TAG_RE, "");
}

/**
 * The rules content formatted for inclusion in the AI system prompt.
 *
 * N-71 / Proposal AG: each file's content is sanitized (dangerous tags
 * stripped) and the whole block is wrapped in <project_rules>...</project_rules>
 * structured delimiters. The system prompt (see ai_prompts.go) declares that
 * content inside these tags is project context data, not system instructions.
 * Returns an empty string when no rules are loaded.
 */
export const rulesForPrompt = computed(() => {
  const files = rulesState.rulesFiles.filter((r) => r.content.trim().length > 0);
  if (files.length === 0) return "";
  const parts = files.map((r) => {
    const cleaned = sanitizeRulesContent(r.content).trim();
    return cleaned ? `# Source: ${r.path}\n${cleaned}` : "";
  }).filter((p) => p.length > 0);
  if (parts.length === 0) return "";
  return `\n\n${RULES_OPEN_TAG}\n${parts.join("\n\n")}\n${RULES_CLOSE_TAG}`;
});

/**
 * Loads all rules files for the given project root using the configured
 * merge mode (N-18). Also loads candidates and config.
 */
export async function loadRules(
  projectRoot: string,
  shouldApply: () => boolean = () => true,
): Promise<void> {
  if (!projectRoot) {
    rulesState.rulesFiles = [];
    rulesState.error = null;
    rulesState.candidates = [];
    rulesState.config = null;
    return;
  }
  rulesState.loading = true;
  rulesState.error = null;
  try {
    const [files, candidates, config] = await Promise.all([
      rulesService.loadRulesMerge(projectRoot),
      rulesService.listCandidates(projectRoot),
      rulesService.loadRulesConfig(projectRoot),
    ]);
    if (!shouldApply()) return;
    rulesState.rulesFiles = files ?? [];
    rulesState.candidates = candidates ?? [];
    rulesState.config = config ?? null;
    if (rulesState.rulesFiles.length > 0) {
      const paths = rulesState.rulesFiles.map((f) => f.path).join(", ");
      pushOutput("ai", "info", `Loaded project rules from: ${paths}`);
    }
  } catch (e: unknown) {
    if (!shouldApply()) return;
    rulesState.rulesFiles = [];
    rulesState.candidates = [];
    rulesState.config = null;
    rulesState.error = errorMessage(e);
    // Don't notify on load failures — rules are optional. Log to output only.
    pushOutput("ai", "warn", `Failed to load project rules: ${rulesState.error}`);
  } finally {
    if (shouldApply()) rulesState.loading = false;
  }
}

/**
 * Saves the rules content to the given relative path inside the project.
 * If relPath is empty, the default .koyori-ide/rules.md location is used.
 * After saving, reloads the rules state.
 */
export async function saveRules(
  projectRoot: string,
  content: string,
  relPath = "",
): Promise<boolean> {
  if (!projectRoot) {
    notifyError("Cannot save rules: no project open");
    return false;
  }
  try {
    await rulesService.saveRules(projectRoot, relPath, content);
    notifySuccess(relPath || ".koyori-ide/rules.md", "Rules saved");
    await loadRules(projectRoot);
    return true;
  } catch (e: unknown) {
    const msg = errorMessage(e);
    rulesState.error = msg;
    notifyError(`Failed to save rules: ${msg}`);
    return false;
  }
}

/**
 * Saves the project-level rules configuration (N-18).
 * After saving, reloads the rules state.
 */
export async function saveRulesConfig(
  projectRoot: string,
  config: RulesConfig,
): Promise<boolean> {
  if (!projectRoot) {
    notifyError("Cannot save rules config: no project open");
    return false;
  }
  try {
    await rulesService.saveRulesConfig(projectRoot, config);
    notifySuccess("Rules config saved");
    await loadRules(projectRoot);
    return true;
  } catch (e: unknown) {
    const msg = errorMessage(e);
    rulesState.error = msg;
    notifyError(`Failed to save rules config: ${msg}`);
    return false;
  }
}

/**
 * Saves the user-global rules configuration (N-18).
 */
export async function saveUserRulesConfig(
  config: RulesConfig,
): Promise<boolean> {
  try {
    await rulesService.saveUserRulesConfig(config);
    notifySuccess("User rules config saved");
    return true;
  } catch (e: unknown) {
    const msg = errorMessage(e);
    rulesState.error = msg;
    notifyError(`Failed to save user rules config: ${msg}`);
    return false;
  }
}

/**
 * Clears the rules state (e.g. when the project closes).
 */
export function clearRules(): void {
  rulesState.rulesFiles = [];
  rulesState.error = null;
  rulesState.candidates = [];
  rulesState.config = null;
}

/**
 * Helper to create a blank RulesConfig with default mode.
 */
export function makeDefaultRulesConfig(): RulesConfig {
  return {
    mode: "first",
    candidates: [] as RulesCandidateConfig[],
  };
}
