// Koyori IDE 模块 · Index。
// 喵，这是 Koyori IDE 的 Index 模块（前端实现）~
export interface DirEntry {
  name: string;
  path: string;
  isDir: boolean;
  size: number;
  modified: number;
}

export interface Project {
  id: string;
  name: string;
  path: string;
  /**
   * Priority 4 (prompt-1.md): 多根工作区支持。当项目来源于 .code-workspace
   * 文件或具有多个根时，roots 保存全部根路径。单根项目时此字段可能为空
   * 或仅含 path；多根项目时含全部根。后端 omitempty。
   */
  roots?: string[];
  /**
   * Priority 4: 标记此项目是否来源于 .code-workspace 文件。
   * 后端 omitempty，缺失视为 false。
   */
  isWorkspace?: boolean;
  createdAt: number;
  lastOpened: number;
  /** Whether the project directory still exists on disk (computed by backend). */
  exists: boolean;
  /**
   * F-9 (prompt-2.md 第 537-586 行): 远程项目配置。
   * 缺失（undefined）表示本地项目；非空表示 SSH 远程项目，path 字段保存远程目录。
   * 与后端 services.RemoteConfig 结构一致；后端使用 *RemoteConfig + omitempty。
   */
  remote?: RemoteConfig;
}

/**
 * Priority 4 (prompt-1.md): .code-workspace 文件中的单个 folder 条目。
 * 与后端 services.codeWorkspaceFolder 结构一致。
 */
export interface CodeWorkspaceFolder {
  path?: string;
  name?: string;
  uri?: string;
}

/**
 * F-9 (prompt-2.md 第 537-586 行): SSH 连接配置。
 * 与后端 services.SSHConfig 结构一致。
 * keyPath 与 password 二选一：keyPath 优先。
 */
export interface SSHConfig {
  host: string;
  port: number;
  user: string;
  /** 私钥文件路径（与 password 二选一，优先使用）。 */
  keyPath?: string;
  /** 密码（与 keyPath 二选一）。绝不记录到日志。 */
  password?: string;
  /** known_hosts 文件路径。为空时拒绝连接。 */
  knownHostsPath?: string;
}

/**
 * F-9: 远程项目配置。嵌入 Project.remote 字段。
 * 与后端 services.RemoteConfig 结构一致。
 */
export interface RemoteConfig {
  config: SSHConfig;
  /** 远程项目名（用于在 UI 中显示）。 */
  name: string;
}

/**
 * F-9: 远程文件/目录元信息。与后端 services.FileInfo 结构一致。
 * 注意：与已有的 DirEntry 字段名相同但语义独立，DirEntry 用于本地文件树，
 * RemoteFileInfo 用于远程文件树。两者字段一致，可互换。
 */
export interface RemoteFileInfo {
  name: string;
  path: string;
  isDir: boolean;
  size: number;
  /** 修改时间戳（Unix 毫秒）。 */
  modified: number;
}

/**
 * F-9: 文件系统变更事件。type 取值：
 *   - "create"  新建文件/目录
 *   - "modify"  内容修改
 *   - "delete"  删除
 *   - "rename"  重命名（oldPath 保存旧路径）
 * 与后端 services.FileEvent 结构一致。
 */
export interface RemoteFileEvent {
  type: "create" | "modify" | "delete" | "rename";
  path: string;
  oldPath?: string;
}

/**
 * Priority 4: .code-workspace 文件的 JSON 结构（仅解析需要的字段）。
 * 与后端 services.codeWorkspaceFile 结构一致。
 */
export interface CodeWorkspaceFile {
  folders: CodeWorkspaceFolder[];
  settings?: Record<string, unknown>;
}

/**
 * G-FEAT-01: A scaffolding template the New Project wizard can generate.
 * Mirrors the Go ProjectTemplate struct.
 */
export interface ProjectTemplate {
  id: string;
  name: string;
  description: string;
  language: string;
}

/**
 * G-FEAT-01: Request payload for creating a new project from a template.
 * Mirrors the Go CreateProjectRequest struct.
 */
export interface CreateProjectRequest {
  templateId: string;
  projectName: string;
  targetDir: string;
  moduleName: string;
}

/**
 * A keyboard shortcut's key combination (N-8). Used as the persisted shape
 * for custom overrides and as the comparison key for conflict detection.
 */
export interface ShortcutKeys {
  key: string;
  ctrl: boolean;
  shift: boolean;
  alt: boolean;
}

export type AIWindowTheme =
  "apple-dark" | "apple-light" | "claude-dark" | "claude-light" | "system";

export interface Settings {
  /** Persisted settings JSON schema, independent from the CAS version. */
  schemaVersion: number;
  /** prompt-7 Task F: monotonic settings version for dual-window CAS. */
  version: number;
  /** Write-intent only; not persisted. */
  expectedVersion?: number;
  language: string;
  theme: string;
  fontSize: number;
  fontFamily: string;
  tabSize: number;
  wordWrap: boolean;
  lineNumbers: boolean;
  minimap: boolean;
  aiApiKey: string;
  // G-SEC-07: the backend no longer returns the decrypted key in aiApiKey
  // (it is cleared to ""). aiApiKeyConfigured signals whether a key is stored,
  // and aiApiKeyStorageMethod labels how ("dpapi"/"aes"/"keyring"/"plain"/
  // "none"). The plaintext key never crosses the Wails binding.
  aiApiKeyConfigured: boolean;
  aiApiKeyStorageMethod: string;
  aiBaseUrl: string;
  aiModel: string;
  aiSystemPrompt: string;
  // Plan 54: optional user overrides for the other three built-in prompts.
  // Empty string means "use the built-in".
  aiAgentSystemPrompt?: string;
  aiConversationTitlePrompt?: string;
  aiInlineCompletionPrompt?: string;
  cursorBlinking: string;
  cursorStyle: string;
  bracketColorization: boolean;
  autoSave: boolean;
  autoSaveDelay: string;
  aiProvider: string;
  temperature: number;
  maxTokens: number;
  defaultShell: string;
  terminalFontSize: number;
  terminalCursorStyle: string;
  scrollback: number;
  uiDensity: string;
  fontSizeScaling: number;
  inlineCompletionEnabled: boolean;
  /** prompt-9 9-A: format via LSP before writing on save (default true). */
  formatOnSave: boolean;
  /** G-EDIT-01: trim trailing whitespace on each line before saving. */
  trimTrailingWhitespace: boolean;
  /** G-EDIT-02: use spaces instead of tabs for indentation (default true). */
  insertSpaces: boolean;
  /** G-EDIT-03: insert a final newline at end of file on save (default true). */
  insertFinalNewline: boolean;
  /** G-BLAME-01: show inline git blame at end of each line (default false). */
  gitBlameEnabled: boolean;
  /** Enable Emmet abbreviation completions in supported Monaco languages. */
  emmetEnabled: boolean;
  /** Additional Monaco language ID to Emmet language mappings. */
  emmetIncludeLanguages?: Record<string, string>;
  // N-8: user-customized keyboard shortcuts, keyed by shortcut label.
  // Missing entries fall back to the default binding.
  customShortcuts?: Record<string, ShortcutKeys>;
  // N-20: layout state.
  aiChatPosition?: "left" | "right";
  activityBarVisible: boolean;
  // Agent approval policy per tool kind (Plan 47). Missing entries default
  // to "always-ask". Keys are tool kinds (read/write/run/search + custom).
  toolApprovalConfig?: ToolApprovalConfig;
  // Plan 48: accent theme persistence. Can be a built-in key
  // ("blue"/"teal"/.../"indigo") or "custom".
  accentTheme?: string;
  // Plan 48: custom accent theme definition. Set when accentTheme === "custom".
  customAccent?: CustomAccentTheme | null;
  // N-29: plugin sandbox mode. When true, plugins run in isolated Web
  // Workers. Defaults to true. NOT optional — false must round-trip.
  enablePluginSandbox: boolean;
  // Design language: "apple" (Apple Design Language, default) or
  // "claude" (Anthropic Claude warm-canvas editorial style).
  designLanguage?: "apple" | "claude";
  // Multi-provider AI configs (CC Switch-style). Each entry is a named
  // configuration with its own provider/apiKey/baseUrl/model/temperature/
  // maxTokens/systemPrompt. activeAIConfigId points at the currently
  // active config. The legacy single-config fields (aiApiKey/aiBaseUrl/
  // aiModel/aiProvider/temperature/maxTokens/aiSystemPrompt) mirror the
  // active config so existing AI call paths work unchanged.
  aiProviderConfigs?: AIProviderConfig[];
  activeAIConfigId?: string;
  /** G-FEAT-03: optional toolchain binary path overrides (e.g. { "golangci-lint": "/usr/local/bin/golangci-lint" }). */
  toolPaths?: Record<string, string>;
  /** Plan 11 Task 15: personalization config (background images, avatars, fonts, bubble styles). */
  personalization?: PersonalizationConfig;
  /** prompt-5 Task C: open AI companion OS window on startup (default false). */
  openAIWindowOnStartup: boolean;
  /** Independent theme used only by the AI companion window. */
  aiWindowTheme: AIWindowTheme;
  /** Persisted AI workspace sidebar width in pixels. */
  aiSidebarWidth: number;
  /** Persisted right-docked AI terminal width in pixels. */
  aiTerminalWidth: number;
  /** Per-section configuration returned to LSP workspace/configuration requests. */
  lspConfigs?: Record<string, unknown>;
}

/** Plan 11 Task 15 — 个性化配置（Step 1）。 */
export interface PersonalizationConfig {
  codeEditorBgImage?: string;
  codeEditorBgOpacity?: number;
  codeEditorBgBlur?: number;
  chatBgImage?: string;
  chatBgOpacity?: number;
  chatBgBlur?: number;
  userAvatar?: string;
  aiAvatar?: string;
  personaAvatars?: Record<string, string>;
  fontFamily?: string;
  fontSize?: number;
  bubbleStyle?: "rounded" | "sharp" | "bubble";
  bubbleOpacity?: number;
  messageSpacing?: number;
}

/**
 * A single named AI provider configuration (CC Switch-style multi-provider).
 * Users can save any number of these and switch between them from the chat
 * panel or settings page. The `protocol` field controls which HTTP API
 * shape the backend uses:
 *   - "openai" (default): /v1/chat/completions + Bearer auth
 *   - "anthropic": /v1/messages + x-api-key + anthropic-version
 */
export interface AIProviderConfig {
  id: string;
  name: string;
  provider: string;
  /** "openai" | "anthropic". Empty defaults to "openai". */
  protocol?: string;
  apiKey: string;
  // G-SEC-07: signals whether a key is stored on disk (backend strips the
  // plaintext apiKey from the response so it never lives in the JS heap).
  apiKeyConfigured?: boolean;
  baseUrl: string;
  model: string;
  temperature?: number;
  maxTokens?: number;
  systemPrompt?: string;
}

/**
 * A user-defined custom accent theme (Plan 48). The base `color` is used to
 * derive the 6 accent CSS tokens and register a Monaco theme. Any token
 * override takes precedence over the derived value.
 */
export interface CustomAccentTheme {
  /** Display name shown in the UI. */
  name: string;
  /** Base accent hex color (e.g. "#ff6b35"). */
  color: string;
  // Optional token overrides. If not set, derived from color at apply time.
  primary?: string;
  primaryHover?: string;
  primaryLight?: string;
  primaryContainer?: string;
  onPrimary?: string;
  onPrimaryContainer?: string;
}

// ApprovalPolicy controls whether a tool call requires user approval.
// - "always-ask": user must approve each call (default, safest).
// - "auto-approve": call executes immediately without user interaction.
// - "never-approve": call is automatically rejected.
export type ApprovalPolicy = "always-ask" | "auto-approve" | "never-approve";

// ToolApprovalConfig maps a tool kind to its approval policy.
export type ToolApprovalConfig = Record<string, ApprovalPolicy>;

export interface ChatMessage {
  role: "user" | "assistant" | "system";
  content: string;
  /** C-6: 稳定 id，替代数组索引作 v-for key，避免 FIFO drop 时 DOM 错位。 */
  id?: string;
  /**
   * G12: optional image attachments as data URLs (e.g.
   * "data:image/png;base64,..."). The backend converts them to OpenAI
   * image_url / Anthropic image blocks and enforces count/size/type budgets
   * (fail-closed), so the UI selection actually reaches the provider request.
   */
  images?: string[];
}

export interface GitFileChange {
  path: string;
  status: string;
}

export interface BranchInfo {
  name: string;
  ahead: number;
  behind: number;
}

export interface SearchMatch {
  line: number;
  column: number;
  preview: string;
}

export interface SearchResult {
  path: string;
  matches: SearchMatch[];
}

export interface ConversationMessage {
  role: string;
  content: string;
}

export interface Conversation {
  id: string;
  title: string;
  created_at: number;
  messages: ConversationMessage[];
  // N-60: Per-conversation system prompt override. When non-empty, this
  // conversation uses a custom system prompt instead of the global default.
  system_prompt_override?: string;
  // Plan 11 Task 2 — conversation organization metadata.
  tags?: string[];
  favorite?: boolean;
  group?: string;
  persona_id?: string;
  mode?: string;
  // DeletedAt: Unix timestamp when soft-deleted (0 = active, >0 = in trash).
  deleted_at?: number;
  // Plan 11 Task 2 Step 6: manual drag-and-drop sort order. 0 = fall back
  // to created_at-desc; non-zero values compared ascending by the sidebar.
  sort_order?: number;
  /** prompt-7 Task C: monotonic revision for dual-window CAS. */
  revision: number;
  /** Unix seconds of last successful save. */
  updated_at: number;
  /** Write-intent only; not persisted. */
  expected_revision?: number;
}

/** Input accepted when creating or updating a conversation. */
export type ConversationSaveInput = Omit<Conversation, "revision" | "updated_at"> & {
  revision?: number;
  updated_at?: number;
};

export type AIActionName =
  | "explain"
  | "refactor"
  | "fix"
  | "generate_docs"
  | "generate_tests"
  | "optimize"
  | "review"
  | "security"
  | "commit_message";

export interface PresetMeta {
  name: string;
  label: string;
  description: string;
  icon: string;
}

// PresetSource identifies where a preset was loaded from (N-17).
export type PresetSource = "builtin" | "project" | "user";

// PresetFile is the on-disk JSON format for a custom preset (N-17).
export interface PresetFile {
  name: string;
  label: string;
  description: string;
  icon?: string;
  prompt: string;
}

// PresetWithSource is a PresetFile annotated with its source layer.
export interface PresetWithSource extends PresetFile {
  source: PresetSource;
}

export interface Command {
  id: string;
  label: string;
  shortcut?: string;
  action: () => void;
  disabled?: boolean;
  disabledReason?: string;
  /**
   * G-VSC-04: origin of the command for unified palette source labeling.
   * "native" = koyori-ide native plugin; "vscode" = VS Code extension.
   * Undefined means a built-in IDE command (no badge shown).
   */
  source?: "native" | "vscode";
}

/**
 * 优先级 10 (prompt-1.md): 应用更新信息。镜像后端 services.UpdateInfo。
 * HasUpdate 由当前版本与最新版本比较得出。
 */
export interface UpdateInfo {
  hasUpdate: boolean;
  latestVersion: string;
  currentVersion: string;
  releaseNotes: string;
  downloadUrl: string;
  releaseDate: string;
  /** Legacy compatibility fields emitted by the backend when available. */
  version?: string;
  releaseUrl?: string;
  publishedAt?: string;
}

/**
 * 优先级 10: 崩溃报告完整载荷（读取单条报告时返回）。镜像后端 services.CrashReport。
 */
export interface CrashReport {
  filename?: string;
  timestamp: string;
  version: string;
  os: string;
  stack: string;
  message: string;
  errorType: string;
}

/**
 * 优先级 10: 崩溃报告列表条目（仅元数据）。镜像后端 services.CrashReportInfo。
 */
export interface CrashReportInfo {
  filename: string;
  timestamp: string;
  size: number;
}

export interface AIContextAttachment {
  kind: "file" | "selection";
  filePath: string;
  language: string;
  content: string;
  startLine?: number;
  endLine?: number;
}

export interface FileContextEntry {
  filePath: string;
  language: string;
  content: string;
}

// Plan 11 Task 3 — unified context chip for @mention + paste. A chip is any
// piece of context attached to the next message: a file, a symbol, a web
// search, a pasted image, a code block, etc. The InputComposer creates chips
// and the ContextChips panel lists/removes them. buildUserMessage (ai.ts)
// serializes them into the message prefix.
export type ContextChipKind =
  | "file"
  | "symbol"
  | "codebase"
  | "gitdiff"
  | "web"
  | "docs"
  | "mcp"
  | "skill"
  | "persona"
  | "url"
  | "image"
  | "codeblock";

export interface ContextChip {
  id: string;
  kind: ContextChipKind;
  label: string;
  // Optional payload depending on kind:
  filePath?: string; // for file/symbol
  language?: string; // for file/codeblock
  content?: string; // text content for file/codeblock/symbol/gitdiff
  imageUrl?: string; // for image (data URL)
  url?: string; // for web/url
  query?: string; // for web/docs search query
}

/** Request payload for AI inline code completion. */
export interface CompletionRequest {
  prefix: string;
  suffix: string;
  language: string;
  filePath: string;
}

/** Response from AI inline code completion. */
export interface CompletionResponse {
  text: string;
}

/**
 * Raw completion response shape from the Wails binding. The Wails binding
 * returns PascalCase fields (e.g. `Text`), but some runtime environments
 * return camelCase. This interface lets us handle both without resorting to
 * `any` (#25 / N-5).
 */
export interface RawCompletionResponse {
  Text?: string;
  text?: string;
}

export interface BranchRef {
  name: string;
  isHead: boolean;
}

/** One record returned by `git worktree list --porcelain`. */
export interface WorktreeInfo {
  path: string;
  head: string;
  branch: string;
  bare: boolean;
  locked?: string;
  prunable?: boolean;
}

/** Optional flags accepted when creating a Git worktree. */
export interface AddWorktreeOptions {
  newBranch?: string;
  detach?: boolean;
  force?: boolean;
  noCheckout?: boolean;
}

/** Source breakpoint mirrored from the debugger service. */
export interface DebugBreakpoint {
  id: number;
  file: string;
  line: number;
  verified: boolean;
  condition?: string;
  logMessage?: string;
  message?: string;
}

/** One debugger stack frame. */
export interface DebugStackFrame {
  id: number;
  name: string;
  file: string;
  line: number;
  column: number;
  presentationHint?: string;
  asyncBoundary?: boolean;
}

/** Frontend-facing state for one DAP thread. */
export interface ThreadInfo {
  id: number;
  name: string;
  state: string;
  frames?: DebugStackFrame[];
  selected: boolean;
}

/** G-FEAT-04: A single file with unresolved merge/rebase conflicts.
 *  Mirrors the Go MergeConflict struct. */
export interface MergeConflict {
  file: string;
  ours: string;
  theirs: string;
  base: string;
}

/** 优先级 3: 一条 git stash 记录。对应 Go StashEntry 结构。 */
export interface StashEntry {
  ref: string;
  message: string;
  date: string;
  author: string;
  commitHash: string;
}

/** 优先级 3: 一个 git tag。对应 Go TagEntry 结构。 */
export interface TagEntry {
  name: string;
  commitHash: string;
  message: string;
}

/** F-4 (prompt-2.md): 一个 git 子模块的状态。对应 Go SubmoduleInfo 结构。 */
export interface SubmoduleInfo {
  sha: string;
  path: string;
  name: string;
  branch?: string;
  url?: string;
  initialized: boolean;
  modified?: boolean;
}

export interface ReplaceResult {
  replacements: number;
}

export interface ReplacePreview {
  path: string;
  originalHash: string;
  originalContent: string;
  modifiedContent: string;
  replacements: number;
}

export interface StructuralReplaceEdit {
  startLine: number;
  startCharacter: number;
  endLine: number;
  endCharacter: number;
  expectedText: string;
  replacement: string;
}

export interface TerminalSessionInfo {
  id: string;
  title: string;
  active: boolean;
}

export interface TaskDef {
  label: string;
  command: string;
  args?: string[];
  cwd?: string;
  shell?: boolean;
  type?: string;
  env?: Record<string, string>;
  dependsOn?: string[];
  group?: string;
  problemMatcher?: string[];
}

/** A single step in a multi-step workflow (N-19). */
export interface WorkflowStep {
  name: string;
  command: string;
  args?: string[];
  cwd?: string;
  dependsOn?: string[];
  condition?: string;
  /** When false, a non-zero exit code does not abort the workflow. Defaults to true. */
  expectSuccess?: boolean;
  /**
   * Plan 11 Task 11 Step 1: Type specifies the step kind.
   * Supported: "command" (default), "ai", "git", "file", "mcp", "skill".
   */
  type?: WorkflowStepType;
  /**
   * Plan 11 Task 11 Step 1: OnFailure controls behavior when the step fails.
   * Supported: "abort" (default), "continue", "skip", "retry".
   */
  onFailure?: OnFailureAction;
  /**
   * Plan 11 Task 11 Step 1: Timeout is the maximum execution time in seconds.
   * 0 means no timeout.
   */
  timeout?: number;
  /**
   * Proposal F (prompt-5.md): Output templates to extract from the
   * step's stdout. Each key becomes accessible as
   * `steps.<name>.outputs.<key>` in subsequent step conditions and
   * command templates.
   *
   * Supported template values:
   *   - "{{stdout}}" — the entire stdout (trimmed)
   *   - "{{regex:pattern}}" — first match of the regex pattern
   *     (capturing group 1 if present, else full match)
   *
   * Example:
   *   outputs:
   *     tag: "{{stdout}}"
   *     major: "{{regex:v(\d+)}}"
   */
  outputs?: Record<string, string>;
}

/** Plan 11 Task 11 Step 1: Workflow step type. */
export type WorkflowStepType =
  "command" | "ai" | "git" | "file" | "mcp" | "skill";

/** Plan 11 Task 11 Step 1: Step failure behavior. */
export type OnFailureAction = "abort" | "continue" | "skip" | "retry";

/** An event trigger that auto-runs a workflow (Proposal B). */
export interface WorkflowTrigger {
  /**
   * Event name. Supported:
   *   - "file-saved": runs when a file matching `glob` is saved (Proposal B)
   *   - "startup": runs once when the IDE finishes loading (Proposal J / prompt-4.md)
   *   - "workflow-completed": runs when another workflow finishes (Proposal R / N-58)
   */
  event: string;
  /** Glob pattern matched against the file path relative to project root. */
  glob?: string;
  /**
   * When event is "workflow-completed", restricts the trigger to fire only
   * when the completed workflow's name matches this field. Empty means any
   * workflow completion triggers this workflow. Proposal R / N-58.
   */
  workflowName?: string;
  /**
   * Plan 11 Task 11 Step 2: Condition restricts when the trigger fires.
   * Branch matches git branch name; Language matches file language.
   */
  condition?: WorkflowTriggerCondition;
}

/** Plan 11 Task 11 Step 2: Trigger condition. */
export interface WorkflowTriggerCondition {
  /** Glob pattern matched against the current git branch. Empty matches all. */
  branch?: string;
  /** Language ID (e.g. "go", "typescript") matched against the changed file. Empty matches all. */
  language?: string;
  /** Additional glob pattern for file matching. */
  fileGlob?: string;
}

/** A multi-step workflow loaded from .koyori-ide/workflows/*.yml (N-19). */
export interface WorkflowDef {
  name: string;
  description?: string;
  steps: WorkflowStep[];
  watch?: string[];
  /** Auto-trigger on IDE events like file-saved (Proposal B). */
  runOn?: WorkflowTrigger;
  /**
   * G-SEC-03: When true the workflow needs explicit user approval before
   * execution. Project-level workflows (.koyori-ide/) default to true so that
   * untrusted startup workflows in cloned repositories cannot auto-run
   * shell commands. The UI must list these as "Pending Confirmation" and
   * require the user to click "Run".
   */
  requiresConfirmation?: boolean;
  source: string;
}

// N-55: Workflow validation result types.
export interface WorkflowValidationError {
  field: string;
  message: string;
}

export interface WorkflowValidationResult {
  workflowName: string;
  valid: boolean;
  errors?: WorkflowValidationError[];
}

/** Status of a workflow step during execution. */
export type WorkflowStepStatus =
  "pending" | "running" | "success" | "failed" | "skipped";

/** Runtime state of a workflow step being executed. */
export interface WorkflowStepState {
  name: string;
  status: WorkflowStepStatus;
  output?: string;
  error?: string;
  startedAt?: number;
  finishedAt?: number;
  /**
   * Proposal F (prompt-5.md): Extracted output values keyed by the
   * template name from WorkflowStep.outputs. Populated after the step
   * completes successfully. Accessible as `steps.<name>.outputs.<key>`
   * in subsequent step conditions and command templates.
   */
  outputs?: Record<string, string>;
}

export type RiskLevel = "safe" | "elevated" | "dangerous";

export interface ExecResult {
  command: string;
  cwd: string;
  stdout: string;
  stderr: string;
  exitCode: number;
  durationMs: number;
  riskLevel: RiskLevel;
  blocked: boolean;
  blockReason?: string;
}

export interface CommandCheck {
  riskLevel: RiskLevel;
  blocked: boolean;
  blockReason?: string;
}

/** Project-level AI rules file loaded from disk (#25). */
export interface RulesFile {
  path: string;
  content: string;
  source: string;
}

/** A candidate location for a rules file, with existence flag. */
export interface RulesFileCandidate {
  path: string;
  source: string;
  exists: boolean;
}

/** A configurable rules file candidate (N-18). Paths may contain globs. */
export interface RulesCandidateConfig {
  path: string;
  source: string;
}

/**
 * Rules configuration (N-18). Controls which rule files are probed and how
 * multiple files are combined.
 *  - mode "first" (default): only the first existing file is used
 *  - mode "merge": all existing files are concatenated in priority order
 */
export interface RulesConfig {
  candidates?: RulesCandidateConfig[];
  mode?: string;
}

/** Source layer a RulesConfig was loaded from (N-18). */
export type RulesConfigSource = "builtin" | "user" | "project";

// ============================================================================
// Plan 49 — Plugin System
// ============================================================================

/**
 * Permission scope declared by a plugin manifest and enforced by the
 * frontend koyoriIde.* API before privileged calls (Plan 49). The frontend
 * checks the plugin's declared permissions before each privileged API
 * call. "commands.register" and "views.register" are always allowed.
 */
export type PluginPermission =
  "fs.read" | "fs.write" | "shell.exec" | "net" | "ai.send";

/**
 * A command contributed by a plugin to the command palette (Plan 49).
 * The plugin's main.js registers a handler via `koyoriIde.commands.register`
 * for the same ID declared here.
 */
export interface PluginCommandContribution {
  id: string;
  title: string;
  category?: string;
  keybinding?: string;
  /**
   * When true, other plugins may invoke this command via
   * `koyoriIde.commands.execute` (Proposal E). When false/unset (the
   * default), only the owning plugin may execute it; cross-plugin
   * callers get a permission error.
   */
  public?: boolean;
}

/**
 * A view contributed by a plugin. The plugin's main.js registers a
 * Vue component via `koyoriIde.views.register` for the same ID declared
 * here. Location controls which dock the view appears in.
 */
export interface PluginViewContribution {
  id: string;
  title: string;
  location?: "sidebar" | "panel" | "statusbar";
}

/**
 * IDE contributions declared by a plugin manifest (Plan 49). Each
 * contribution kind maps to a `koyoriIde.*` registration API.
 */
export interface PluginContribution {
  commands?: PluginCommandContribution[];
  views?: PluginViewContribution[];
}

/**
 * The parsed plugin.json descriptor (Plan 49). Mirrors the Go
 * PluginManifest struct exactly so the Wails binding round-trips
 * without field-name mapping.
 */
export interface PluginManifest {
  /** Manifest format version. Currently 1. 0/unset = v1 for backward compat. */
  schemaVersion?: number;
  name: string;
  version: string;
  description?: string;
  author?: string;
  /** URL to the plugin's source repository (Proposal D). */
  repository?: string;
  /** URL to the plugin's homepage/documentation (Proposal D). */
  homepage?: string;
  /** SPDX license identifier, e.g. "MIT" (Proposal D). */
  license?: string;
  /** Entry point .js file relative to the plugin directory. */
  main: string;
  permissions?: PluginPermission[];
  /**
   * Events that trigger activation. v1 supports "onStartup" and
   * "onCommand:<id>". "onLanguage:<id>" is reserved for future use.
   */
  activationEvents?: string[];
  contributes?: PluginContribution;
}

/** Discovery layer for an installed plugin (Plan 49). */
export type PluginSource = "user" | "project";

/**
 * Runtime descriptor for an installed plugin (Plan 49). Pairs the
 * parsed manifest with discovery metadata. Mirrors the Go PluginInfo
 * struct.
 */
export interface PluginInfo {
  manifest: PluginManifest;
  /** Absolute path to the plugin directory on disk. */
  path: string;
  source: PluginSource;
  enabled: boolean;
  /** True if the manifest's main file exists on disk. */
  mainExists: boolean;
  /** Manifest discovery or validation failure. Empty/undefined when healthy. */
  loadError?: string;
}

// ============================================================================
// G-VSC-04 — VS Code Extension coexistence
// ============================================================================

/**
 * G-VSC-04: Security level badge for a VS Code extension. Aligns with the
 * existing G-VSC-03 / G-SEC-12 security model in stores/extensionSecurity.ts
 * (ExtensionSecurityLevel). Native plugins run in a stricter, permission-gated
 * sandbox (koyoriIde.* API), so they are labeled "Native Plugin"; VS Code extensions
 * run in the Extension Host and carry a trusted/reviewed/restricted level so
 * the user can distinguish risk in the management panel.
 */
export type VscodeExtensionSecurityLevel =
  "trusted" | "reviewed" | "restricted";

/**
 * G-VSC-04: A command contributed by a VS Code extension. The Extension
 * Host registers these via registerVscodeExtensionCommand(); they are
 * aggregated into the unified command palette as supplementary commands
 * (lower priority than native plugin commands).
 */
export interface VscodeExtensionCommand {
  id: string;
  /** Extension id that owns this command, e.g. "ms-python.python". */
  extensionId: string;
  label: string;
  category?: string;
  keybinding?: string;
  handler: (...args: unknown[]) => unknown | Promise<unknown>;
}

/**
 * G-VSC-04: Runtime descriptor for an installed VS Code extension. Populated
 * by the Extension Host bridge (a future module) via registerVscodeExtension().
 * Mirrors the relevant subset of VS Code's Extension<T> for management UI.
 */
export interface VscodeExtensionInfo {
  id: string;
  name: string;
  displayName?: string;
  description?: string;
  version: string;
  publisher?: string;
  enabled: boolean;
  /** Whether the extension is currently active in the Extension Host. */
  isActive: boolean;
  securityLevel: VscodeExtensionSecurityLevel;
}

// ============================================================================
// G-VSC-01 — VS Code Extension Marketplace (Open VSX)
// ============================================================================
// These types mirror the Go structs in services/marketplace_service.go. The
// Wails binding regenerates the JS/TS wrappers on the next dev/build; the
// shapes here let the frontend consume search/detail/install results with
// full typing.

/** A single hit from a registry search (G-VSC-01). */
export interface ExtensionSearchResult {
  id: string;
  name: string;
  displayName: string;
  publisher: string;
  description: string;
  version: string;
  rating: number;
  ratingCount: number;
  downloadCount: number;
  iconUrl: string;
}

/** A single published version of an extension. */
export interface ExtensionVersion {
  version: string;
  downloadUrl: string;
  date: string;
}

/** Full metadata for a single extension (detail view). */
export interface ExtensionDetail {
  id: string;
  name: string;
  displayName: string;
  publisher: string;
  description: string;
  version: string;
  rating: number;
  ratingCount: number;
  downloadCount: number;
  iconUrl: string;
  categories: string[];
  tags: string[];
  license: string;
  repository: string;
  readme: string;
  versions: ExtensionVersion[];
}

/** A locally installed VS Code extension (G-VSC-01). */
export interface InstalledExtension {
  publisher: string;
  name: string;
  version: string;
  path: string;
  enabled: boolean;
}

/** G-MKT-02: An available update for an installed extension. */
export interface ExtensionUpdate {
  publisher: string;
  name: string;
  currentVersion: string;
  latestVersion: string;
  downloadUrl: string;
}

/**
 * Subset of extension/package.json parsed after VSIX extraction (Step 3).
 * engines.vscode gates compatibility; activationEvents/contributes/capabilities
 * drive the security classification and the management UI.
 */
export interface VSCodeExtensionManifest {
  name: string;
  publisher: string;
  version: string;
  displayName: string;
  description: string;
  engines: Record<string, string>;
  activationEvents: string[];
  contributes: unknown;
  capabilities: unknown;
  main?: string;
  browser?: string;
  koyoriIde?: {
    permissions?: import("@/lib/extensionHost/permissions").ExtensionPermission[];
  };
  /**
   * F-3 (prompt-2.md): 结构化解析后的 contributes，由后端 parseVSIXManifest
   * 填充。前端扩展宿主直接使用此字段注入命令面板/侧边栏/Monaco，无需二次
   * 解析 contributes (unknown)。后端 JSON 序列化时该字段标签为 "-"（不输出），
   * 因此前端在 getInstalledExtensionManifests 返回的 JSON 中不会看到此字段——
   * 需在前端用 ParseExtensionManifest 等价逻辑二次解析，或后端调整序列化。
   * 当前实现：前端 vscodeExtensionActivation.ts 在加载 manifest 后调用
   * parseContributesForFrontend(contributes) 填充此字段。
   */
  parsedContributes?: ExtensionContributes;
}

// ============================================================================
// Priority 8 — VSCode 扩展 activationEvents + contributes 解析
// 类型镜像 services/extension_service.go 中的 Go 结构体。
// ============================================================================

/** contributes.languages 的一项。 */
export interface ExtensionLanguageContribution {
  id: string;
  aliases?: string[];
  extensions?: string[];
  configuration?: string;
}

/** contributes.grammars 的一项。 */
export interface ExtensionGrammarContribution {
  language?: string;
  scopeName: string;
  path: string;
}

/** contributes.snippets 的一项。 */
export interface ExtensionSnippetContribution {
  language: string;
  path: string;
}

/** contributes.commands 的一项。 */
export interface ExtensionCommandContribution {
  command: string;
  title: string;
  category?: string;
}

/** contributes.configuration 的一项（单对象已归一化为单元素数组）。 */
export interface ExtensionConfigurationContribution {
  title?: string;
  properties?: unknown;
}

/** contributes.debuggers 的一项。 */
export interface ExtensionDebuggerContribution {
  type: string;
  label?: string;
  languages?: string[];
  configurationAttributes?: unknown;
}

/** contributes.jsonValidation 的一项。 */
export interface ExtensionJSONValidationContribution {
  fileMatch: string;
  url: string;
}

/** contributes.views.<container>[] 的一项。F-3 (prompt-2.md)。 */
export interface ExtensionViewContribution {
  id: string;
  name: string;
  when?: string;
  icon?: string;
  contextualTitle?: string;
  visibility?: string;
}

/** contributes.viewsWelcome[] 的一项。F-3 (prompt-2.md)。 */
export interface ExtensionViewWelcomeContribution {
  view: string;
  contents: string;
  when?: string;
}

/** contributes.menus.<menuId>[] 的一项。F-3 (prompt-2.md)。 */
export interface ExtensionMenuContribution {
  command: string;
  alt?: string;
  when?: string;
  group?: string;
}

/** contributes.keybindings[] 的一项。F-3 (prompt-2.md)。 */
export interface ExtensionKeybindingContribution {
  command: string;
  key: string;
  mac?: string;
  linux?: string;
  win?: string;
  when?: string;
  args?: unknown;
}

/** contributes.themes[] 的一项。F-3 (prompt-2.md)。 */
export interface ExtensionThemeContribution {
  label: string;
  uiTheme?: string;
  path: string;
}

/** contributes.iconThemes[] 的一项。F-3 (prompt-2.md)。 */
export interface ExtensionIconThemeContribution {
  id: string;
  label: string;
  path: string;
}

/**
 * package.json 的 contributes 段，所有字段可选。
 * F-3 (prompt-2.md): 补齐 views/viewsWelcome/menus/keybindings/themes/iconThemes，
 * 镜像后端 services/extension_service.go 的 ExtensionContributes。
 */
export interface ExtensionContributes {
  languages?: ExtensionLanguageContribution[];
  grammars?: ExtensionGrammarContribution[];
  snippets?: ExtensionSnippetContribution[];
  commands?: ExtensionCommandContribution[];
  configuration?: ExtensionConfigurationContribution[];
  debuggers?: ExtensionDebuggerContribution[];
  jsonValidation?: ExtensionJSONValidationContribution[];
  /** F-3: 按容器 ID 分组的视图（如 explorer、debug）。 */
  views?: Record<string, ExtensionViewContribution[]>;
  viewsWelcome?: ExtensionViewWelcomeContribution[];
  menus?: Record<string, ExtensionMenuContribution[]>;
  keybindings?: ExtensionKeybindingContribution[];
  themes?: ExtensionThemeContribution[];
  iconThemes?: ExtensionIconThemeContribution[];
}

/**
 * Priority 8: 解析后的 VS Code 扩展 package.json 子集。相比
 * VSCodeExtensionManifest（marketplace 安装流程用的简版），此类型提供
 * 类型化的 contributes、入口字段（main/browser）与宿主元数据。
 */
export interface ExtensionManifest {
  name: string;
  publisher: string;
  version: string;
  displayName: string;
  description: string;
  engines: Record<string, string>;
  activationEvents: string[];
  contributes: ExtensionContributes;
  /** Node 扩展入口。 */
  main?: string;
  /** Web 扩展入口。 */
  browser?: string;
  /** Koyori IDE 宿主专用元数据；不是可执行入口。 */
  koyoriIde?: {
    permissions?: import("@/lib/extensionHost/permissions").ExtensionPermission[];
  };
}

// ============================================================================
// Plan 50 — Profile System
// ============================================================================

/**
 * A user profile (Plan 50). Each profile is a directory under
 * <configDir>/koyori-ide/profiles/<name>/ containing settings.json.
 * The active profile is the one currently loaded by SettingsService.
 */
export interface ProfileInfo {
  name: string;
  description?: string;
  createdAt?: number;
  modifiedAt?: number;
  active: boolean;
}

/**
 * Exported profile blob (Plan 50). The frontend serializes this as a
 * .json file for download. ImportProfile accepts the same shape.
 *
 * G25: schemaVersion is required; imports of unknown versions are rejected
 * fail-closed by the backend. Sensitive settings fields (secrets) are
 * redacted from the export and never round-trip through a profile blob.
 */
export interface ProfileExport {
  schemaVersion: number;
  name: string;
  description?: string;
  settings: unknown;
  exportedAt: number;
}

// ============================================================================
// N-49 — Secrets cross-platform migration
// ============================================================================

/**
 * Describes a secret entry discovered in the platform keyring (macOS
 * Keychain / Linux libsecret). Returned by settingsService.listSecrets()
 * so the settings UI can show users what's stored and let them clean up
 * orphan entries left behind when AIApiKey was cleared.
 */
export interface SecretInfo {
  account: string;
  method: string;
  stored: boolean;
}

// ============================================================================
// Plan 72 / N-25 — Layout Engine
// ============================================================================

/** Split orientation: horizontal = side-by-side, vertical = stacked. */
export type LayoutOrientation = "horizontal" | "vertical";

/**
 * A leaf node in the layout tree. Holds a single view (identified by
 * viewId). When viewId is null, the leaf is empty and can receive a
 * new view via drag-drop or the view picker.
 */
export interface LayoutLeaf {
  id: string;
  type: "leaf";
  viewId: string | null;
}

/**
 * A split node in the layout tree. Contains 2+ children arranged
 * horizontally or vertically. The `sizes` array holds relative
 * proportions (percentages) that should sum to 100; if absent, children
 * share equal space.
 */
export interface LayoutSplit {
  id: string;
  type: "split";
  orientation: LayoutOrientation;
  children: LayoutNode[];
  /** Relative sizes (percentages) per child. Optional; defaults to equal. */
  sizes?: number[];
}

/** A node in the layout tree: either a leaf or a split. */
export type LayoutNode = LayoutLeaf | LayoutSplit;

/**
 * The complete layout tree state. The root is the top-level node
 * (typically a split containing the sidebar + editor area). The
 * activeLeafId tracks which leaf currently has focus.
 */
export interface LayoutTree {
  root: LayoutNode;
  activeLeafId: string | null;
}

// --- N-44 / Proposal N: Wails event payload typing ---
//
// Wails delivers backend-emitted events to the frontend via
// `Events.On(name, cb)`. The callback receives an object whose shape
// depends on the event channel. Previously every handler used
// `any` and relied on runtime `typeof` checks — losing all compile-
// time type safety. These generic types restore it.
//
// The canonical payload mapping is mirrored in services/events.go
// (Go-side documentation). Update both files together when adding a
// new event channel.

/**
 * Generic shape of a Wails runtime event delivered to `Events.On`
 * callbacks. `data` carries the backend-emitted payload; `name` is
 * the event channel (always equal to the first arg of `Events.On`).
 */
export interface WailsEvent<T> {
  data: T;
  name?: string;
}

/**
 * Per-channel payload types. Each alias documents the exact shape
 * that the Go backend emits via `app.Event.Emit(name, payload)`.
 *
 * prompt-6 Task 2: AI stream events are structured with streamId.
 * Legacy string payloads are still accepted by the frontend parser.
 *
 * - `ai:chunk`     — { streamId, data } token from the streaming response
 * - `ai:done`      — { streamId, data } finish-reason (data may be empty)
 * - `ai:error`     — { streamId, data } error message
 * - `ai:stream-busy` — { streamId, busy }
 * - `ai:tool_calls`  — { streamId, data } JSON array string
 * - `settings:changed` / `conversation:saved` / `agent:pending-updated` — dual-window SSOT
 * - `file:saved`   — the absolute path of the saved file
 * - `terminal:output` — { sessionId, data } for a single PTY write
 * - `terminal:exited` — { sessionId, code, signal, err } when the PTY exits
 * - `workflow:completed` — { name } when a workflow finishes (Proposal R)
 */
export interface AIStreamPayload {
  streamId?: string;
  data?: string;
  busy?: boolean;
}
export type AIChunkEvent = WailsEvent<AIStreamPayload | string>;
export type AIDoneEvent = WailsEvent<AIStreamPayload | string>;
export type AIErrorEvent = WailsEvent<AIStreamPayload | string>;
export type FileSavedEvent = WailsEvent<string>;
export interface TerminalOutputPayload {
  sessionId: string;
  data: string;
}
export type TerminalOutputEvent = WailsEvent<TerminalOutputPayload>;
export interface TerminalExitedPayload {
  sessionId: string;
  code?: number;
  signal?: string;
  /** BUG4c: 后端在 PTY 退出时附带错误信息（如 "exit status 1"）。可选，用于向用户展示退出原因。 */
  err?: string;
}
export type TerminalExitedEvent = WailsEvent<TerminalExitedPayload>;

/** Backend/frontend handshake used while replacing or removing an extension. */
export interface ExtensionLifecycleRequest {
  requestId: string;
  extensionId: string;
  publisher: string;
  name: string;
  action: "stop" | "restore" | "invalidate" | "commit";
  wasActive: boolean;
}

export interface ExtensionLifecycleResult {
  requestId: string;
  extensionId: string;
  publisher: string;
  name: string;
  action: ExtensionLifecycleRequest["action"];
  ok: boolean;
  wasActive: boolean;
  warning?: string;
  error?: string;
}

export type ExtensionLifecycleRequestEvent = WailsEvent<ExtensionLifecycleRequest>;
export interface WorkflowCompletedPayload {
  name: string;
  /** Whether the workflow completed successfully (no failed steps). */
  success: boolean;
  /** Chain depth — how many workflow-completed triggers led here. 0 = direct. */
  chainDepth: number;
}
export type WorkflowCompletedEvent = WailsEvent<WorkflowCompletedPayload>;

// ============================================================================
// GOAL-P1-02 — backend-enforced Agent tool-call budget
// ============================================================================

/**
 * Authoritative Agent tool-call budget state, mirroring
 * services.ToolBudgetStatus.
 *
 * The renderer displays this; it cannot change it. Before GOAL-P1-02 the only
 * ceiling was a frontend constant that produced a warning, so a renderer
 * refresh reset the counter and the user simply kept approving. The backend now
 * enforces the limit at capability issuance and this struct is the single
 * source of truth for what the UI shows.
 */
export interface ToolBudgetStatus {
  /** Increments each time the user explicitly opens a new budget epoch. */
  epoch: number;
  /** Capabilities issued in this epoch. */
  spent: number;
  /** Maximum capabilities this epoch may issue. */
  limit: number;
  /** limit - spent, floored at zero. */
  remaining: number;
  /** No further capability will be issued until a new epoch is opened. */
  exhausted: boolean;
  /** RFC3339 timestamp for when this epoch opened. */
  startedAt: string;
  /** RFC3339 timestamp for when this epoch stops issuing capabilities. */
  expiresAt: string;
  /** The epoch's wall-clock window elapsed. */
  timedOut: boolean;
}

// ============================================================================
// G-FEAT-03 — Toolchain commands (Go/TS/JS build/test/lint/format)
// ============================================================================

/** A toolchain command exposed in the command palette / context menu. */
export interface ToolchainCommand {
  id: string;
  label: string;
  language: string;
  command: string;
  args?: string[];
  description?: string;
  sourcePackId?: string;
  sourcePackVersion?: string;
}

/** A single parsed compiler/linter issue. */
export interface GoTarget {
  goos: string;
  goarch: string;
}

export interface GoTargetState {
  host: GoTarget;
  current: GoTarget;
  overridden: boolean;
}

export interface ToolchainDiagnostic {
  file: string;
  line: number;
  column: number;
  message: string;
  severity: "error" | "warning" | "info";
  source: string;
}

/** The outcome of running a toolchain command. */
export interface ToolchainResult {
  success: boolean;
  output: string;
  errors: ToolchainDiagnostic[];
  durationMs: number;
  notInstalled: boolean;
  canceled?: boolean;
  exitCode?: number;
  installCmd?: string;
}

// G-FEAT-02: Offline LSP completion types.

/** Reports the availability and state of a language server (gopls/tsserver). */
export interface LSPServerStatus {
  language: string;
  available: boolean;
  running: boolean;
  serverPath: string;
  version: string;
  sourcePackId?: string;
  sourcePackVersion?: string;
  /** Explicit user-run command shown when this server is unavailable. */
  installHint?: string;
  /** prompt-8 Task 8-D */
  lastError?: string;
  serverKind?: string;
  /** Workspace framework using this server; React enhances the TS server. */
  framework?: "vue" | "angular" | "react";
  /** Workspace root that supplied the local framework server. */
  workspaceRoot?: string;
}

/** Request payload for LSP completion/hover/diagnostics queries. */
export interface LSPCompletionRequest {
  language: string;
  filePath: string;
  line: number;
  column: number;
  endLine?: number;
  endColumn?: number;
  only?: string[];
  content: string;
  /** LSP CompletionTriggerKind: 1=Invoked, 2=TriggerCharacter, 3=TriggerForIncompleteCompletions. */
  triggerKind?: 1 | 2 | 3;
  /** Character that caused a TriggerCharacter completion request. */
  triggerCharacter?: string;
}

/** LSP TextEdit wire shape used by CompletionItem.textEdit. */
export interface LSPProtocolTextEdit {
  range: LSPRange;
  newText: string;
}

/** LSP InsertReplaceEdit wire shape used by CompletionItem.textEdit. */
export interface LSPInsertReplaceEdit {
  newText: string;
  insert: LSPRange;
  replace: LSPRange;
}

/**
 * Current Go DTO keeps flattened coordinates for compatibility and may also
 * carry the protocol ranges while bindings transition to the full wire shape.
 */
export interface BackendLSPCompletionTextEdit extends LSPTextEdit {
  range?: LSPRange | null;
  insert?: LSPRange | null;
  replace?: LSPRange | null;
}

export type LSPCompletionTextEdit =
  | LSPProtocolTextEdit
  | LSPInsertReplaceEdit
  | BackendLSPCompletionTextEdit;

/** A single completion item returned by the LSP server. */
export interface LSPCompletionItem {
  label: string;
  kind: number;
  detail: string;
  insertText?: string | null;
  textEditText?: string | null;
  /**
   * Priority 2 (prompt-1.md): LSP insertTextFormat. 1 = plain text (default),
   * 2 = Snippet. When 2, insertText uses snippet syntax ($1, ${1:default}).
   */
  insertTextFormat?: number;
  /**
   * Priority 2 (prompt-1.md): LSP CompletionItemLabelDetails. `detail` is the
   * short signature shown beside the label; `description` is the fuller text.
   */
  labelDetails?: { detail?: string; description?: string } | null;
  /** prompt-10 10-I: auto-import / additionalTextEdits from LSP */
  additionalEdits?: LSPTextEdit[] | null;
  sortText?: string | null;
  filterText?: string | null;
  preselect?: boolean;
  deprecated?: boolean;
  tags?: number[] | null;
  documentation?: string | { kind: string; value: string } | null;
  /** Opaque server payload that must be returned unchanged during completionItem/resolve. */
  data?: unknown;
  commitCharacters?: string[] | null;
  /** LSP TextEdit/InsertReplaceEdit payload. */
  textEdit?: LSPCompletionTextEdit | null;
  insertTextMode?: number | null;
}

/** Current Go DTO accepted while generated bindings transition to wire edits. */
export type BackendLSPCompletionItem = Omit<LSPCompletionItem, "textEdit"> & {
  textEdit?: LSPCompletionTextEdit | null;
};

/** Completion response used by servers that report whether another request is needed. */
export interface LSPCompletionList {
  items: LSPCompletionItem[];
  isIncomplete: boolean;
}

/** A single LSP diagnostic (error/warning). */
export interface Diagnostic {
  line: number;
  column: number;
  endLine: number;
  endColumn: number;
  severity: number;
  message: string;
  source: string;
  code?: string | number | null;
  codeDescription?: DiagnosticCodeDescription | null;
  relatedInformation?: DiagnosticRelatedInformation[] | null;
  tags?: number[] | null;
  /** Opaque server payload retained for diagnostic refresh requests. */
  data?: unknown;
}

export interface DiagnosticCodeDescription {
  href: string;
}

export interface DiagnosticRelatedInformation {
  location: {
    uri: string;
    range: LSPRange;
  };
  message: string;
}

/** prompt-8 Task 8-F: definition / references location. */
export interface LSPLocation {
  filePath: string;
  line: number;
  column: number;
  endLine: number;
  endColumn: number;
}

/** prompt-8 Task 8-G/H: text edit for format/rename. */
export interface LSPTextEdit {
  startLine: number;
  startCol: number;
  endLine: number;
  endCol: number;
  newText: string;
}

// ============================================================================
// G-COMP-02: Enhanced LSP types — document symbols, workspace symbols,
// semantic tokens. Mirror services/lsp_service.go JSON tags.
// ============================================================================

/** Zero-based line/character pair (LSP Position). */
export interface LSPPosition {
  line: number;
  character: number;
}

/** A [start, end) range in a document (LSP Range). */
export interface LSPRange {
  start: LSPPosition;
  end: LSPPosition;
}

/** A single entry in a document outline (breadcrumb / outline tree). */
export interface LSPDocumentSymbol {
  name: string;
  detail?: string;
  /** LSP SymbolKind (1-26). */
  kind: number;
  range: LSPRange;
  selectionRange: LSPRange;
  children?: LSPDocumentSymbol[];
}

/** A workspace-wide symbol (workspace/symbol result). */
export interface LSPSymbolInformation {
  name: string;
  kind: number;
  containerName?: string;
  filePath: string;
  line: number;
  column: number;
  endLine: number;
  endColumn: number;
}

/** Wire-format workspace symbol used by workspace/symbol providers. */
export interface WorkspaceSymbol {
  name: string;
  kind: number;
  location: { uri: string; range: LSPRange };
  containerName?: string;
}

/** Wire-format CallHierarchyItem retained for resolve/incoming/outgoing requests. */
export interface CallHierarchyItem {
  name: string;
  kind: number;
  uri: string;
  range: LSPRange;
  selectionRange: LSPRange;
  data?: unknown;
}

/** Wire-format code lens returned by textDocument/codeLens. */
export interface CodeLens {
  range: LSPRange;
  command?: { title: string; command: string; arguments?: unknown[] };
  data?: unknown;
}

// ============================================================================
// F-1 (prompt-2.md): Call Hierarchy / Type Hierarchy types.
// Mirror services/lsp_service.go JSON tags.
// ============================================================================

/** F-1: Call Hierarchy 单个符号项（扁平化，range 用独立字段方便前端使用）。 */
export interface LSPCallHierarchyItem {
  name: string;
  kind: number;
  detail?: string;
  filePath: string;
  line: number;
  column: number;
  endLine: number;
  endColumn: number;
  selectionLine: number;
  selectionColumn: number;
  selectionEndLine: number;
  selectionEndColumn: number;
  /** Server 透传的不透明数据，后续 incoming/outgoing 请求需原样回传。 */
  data?: unknown;
}

/** F-1: Call Hierarchy incoming call（谁调用了 item）。 */
export interface LSPCallHierarchyIncomingCall {
  from: LSPCallHierarchyItem;
  fromRanges: LSPLocation[];
}

/** F-1: Call Hierarchy outgoing call（item 调用了谁）。 */
export interface LSPCallHierarchyOutgoingCall {
  to: LSPCallHierarchyItem;
  fromRanges: LSPLocation[];
}

/** F-1: Type Hierarchy 单个类型项。 */
export interface LSPTypeHierarchyItem {
  name: string;
  kind: number;
  detail?: string;
  filePath: string;
  line: number;
  column: number;
  endLine: number;
  endColumn: number;
  selectionLine: number;
  selectionColumn: number;
  selectionEndLine: number;
  selectionEndColumn: number;
  data?: unknown;
}

/** A single semantic token for semantic highlighting. */
export interface SemanticToken {
  line: number;
  start: number;
  length: number;
  type: number;
  modifiers: number;
}

/** Current Go DTO: absolute column plus decoded modifier indices. */
export interface BackendSemanticToken {
  line: number;
  column: number;
  length: number;
  type: number;
  modifiers?: number[];
}

/**
 * prompt-12 12-L / Priority 1: A simplified inlay hint.
 * Mirrors services/lsp_service.go InlayHint. Positions are 0-based (LSP).
 * kind: 1=type 2=parameter (matches Monaco InlayHintKind).
 */
export interface InlayHint {
  position: LSPPosition;
  label: string | InlayHintLabelPart[];
  kind?: number;
  paddingLeft?: boolean;
  paddingRight?: boolean;
  tooltip?: string;
  textEdits?: LSPTextEdit[];
  data?: unknown;
}

/** Current Go DTO: position and labels are flattened for existing consumers. */
export interface BackendInlayHint {
  line: number;
  column: number;
  label: string;
  kind: number;
  tooltip?: unknown;
  textEdits?: LSPTextEdit[];
  paddingLeft?: boolean;
  paddingRight?: boolean;
  data?: unknown;
  rawLabel?: unknown;
}

export interface InlayHintLabelPart {
  value: string;
  tooltip?: string;
  location?: { uri: string; range: LSPRange };
}

/** Current Go DTO for resolved code lenses. */
export interface BackendCodeLens {
  line: number;
  column: number;
  endLine?: number;
  endColumn?: number;
  label: string;
  command: string;
  arguments?: unknown[];
  data?: unknown;
}

// ============================================================================
// Architecture C (prompt-1.md 491-500): LSP 客户端能力补全 — declaration /
// typeDefinition / documentLink / selectionRange / foldingRange 响应类型。
// 镜像 services/lsp_service.go 中对应结构体的 JSON 标签。位置均为 0-based (LSP)。
// ============================================================================

/**
 * Architecture C: A clickable document link (textDocument/documentLink).
 * Positions are 0-based (LSP). target is the URI the link points to.
 */
export interface LSPDocumentLink {
  startLine: number;
  startColumn: number;
  endLine: number;
  endColumn: number;
  target?: string;
  tooltip?: string;
}

/**
 * Architecture C: A nested selection range (textDocument/selectionRange).
 * Used for expand/shrink selection. parent is the next enclosing range.
 */
export interface LSPSelectionRange {
  startLine: number;
  startColumn: number;
  endLine: number;
  endColumn: number;
  parent?: LSPSelectionRange;
}

/**
 * Architecture C: A foldable code region (textDocument/foldingRange).
 * kind is e.g. "comment", "imports", "region" (LSP FoldingRangeKind).
 */
export interface LSPFoldingRange {
  startLine: number;
  endLine: number;
  kind?: string;
}

// ============================================================================
// F-8 (prompt-2.md 517-535): LSP colorProvider / linkedEditingRange 响应类型。
// 镜像 services/lsp_service.go 中 Color / ColorInformation / ColorPresentation /
// LinkedEditRange 的 JSON 标签。Range 复用 LSPRange；TextEdit 复用 LSPTextEdit。
// 位置均为 0-based (LSP)。
// ============================================================================

/** F-8: LSP RGBA 颜色（0.0~1.0 浮点分量）。 */
export interface Color {
  red: number;
  green: number;
  blue: number;
  alpha: number;
}

/** F-8: 文档中一处颜色及其所在范围（textDocument/documentColor 响应元素）。 */
export interface ColorInformation {
  range: LSPRange;
  color: Color;
}

/** F-8: 颜色的某种文本表示形式（如 #ff0000 / rgb(255,0,0)）。 */
export interface ColorPresentation {
  label: string;
  textEdit?: LSPTextEdit;
  additionalTextEdits?: LSPTextEdit[];
}

/** F-8: 可同步编辑的范围之一（如 HTML 起始/结束标签）。 */
export interface LinkedEditRange {
  range: LSPRange;
}

// ============================================================================
// G-COMP-01: Symbol Index — workspace exported symbol scanning for auto-import.
// 镜像 services/symbol_index_service.go 的 JSON 标签。
// ============================================================================

/** A single exported symbol discovered in the workspace by the symbol index. */
export interface IndexedSymbol {
  name: string;
  kind: number;
  filePath: string;
  line: number;
  column: number;
  /** Import path for this symbol (Go package path or JS/TS relative module path). */
  exportPath: string;
  /** True for `export default` in JS/TS. */
  isDefaultExport: boolean;
  /** Short description, e.g. the declaration line. */
  detail?: string;
}

/** Statistics about the symbol index for diagnostics. */
export interface IndexStats {
  symbolCount: number;
  fileCount: number;
  workspaceRoot: string;
  lastIndex: string;
  indexVersion: number;
}

// ============================================================================
// Plan 11 Task 13 — Diff Enhancement（结构化多文件 diff / 三方合并 / AI 审查）
// 镜像 services/diff_service.go 的 JSON 标签，供 DiffViewer.vue + stores/diff.ts 使用。
// ============================================================================

/** 单行 diff 的变更类型。 */
export type DiffLineType = "context" | "added" | "removed" | "conflict";

/** AI 审查意见的严重级别（Step 8: severity 色标）。 */
export type AICommentSeverity = "info" | "warning" | "error" | "critical";

/** 行内评论（Step 4: 用户或 AI 添加）。 */
export interface InlineComment {
  author: string;
  body: string;
  createdAt: string;
  aiComment?: boolean;
}

/** AI 对 hunk 的审查意见（Step 3）。 */
export interface AIComment {
  severity: AICommentSeverity;
  message: string;
  suggestion?: string;
  /** 关联行号。 */
  line?: number;
}

/** 单行 diff。 */
export interface DiffLine {
  type: DiffLineType;
  /** 旧行号（removed/context 有）。 */
  oldNum?: number;
  /** 新行号（added/context 有）。 */
  newNum?: number;
  /** 行内容（不含前缀 +/-/空格）。 */
  content: string;
  /** Step 4: 行内评论。 */
  comments?: InlineComment[];
}

/** 一组连续的 diff 行。 */
export interface Hunk {
  oldStart: number;
  oldCount: number;
  newStart: number;
  newCount: number;
  lines: DiffLine[];
  /** Step 3: AI 审查标注。 */
  aiComments?: AIComment[];
}

/** 单个文件的 diff（Step 1）。 */
export interface FileDiff {
  path: string;
  /** 重命名时旧路径。 */
  oldPath?: string;
  oldContent: string;
  newContent: string;
  hunks: Hunk[];
  /** 统计。 */
  addedLines: number;
  removedLines: number;
}

/** 多文件 diff（Step 1）。 */
export interface MultiFileDiff {
  files: FileDiff[];
  totalAdded: number;
  totalRemoved: number;
}

/** 三方合并结果（Step 2）。 */
export interface ThreeWayMergeResult {
  merged: string;
  conflicts: number;
  hasConflict: boolean;
}

// ============================================================================
// F-5 + F-7 (task-1.md): Debug service — data breakpoints & auxiliary types
// ============================================================================

/** F-5: 数据断点候选信息（DAP dataBreakpointInfo 响应）。 */
export interface DataBreakpointInfo {
  dataId: string;
  description: string;
  /** "read" | "write" | "readWrite" */
  accessTypes?: string[];
  canPersist?: boolean;
}

/** F-5: 数据断点（DAP setDataBreakpoints 请求项）。 */
export interface DataBreakpoint {
  dataId: string;
  accessType: string;
  condition?: string;
  hitCondition?: string;
}

/** F-7: 异常扩展信息（DAP exceptionInfo.details）。 */
export interface ExceptionDetails {
  message?: string;
  typeName?: string;
  fullTypeName?: string;
  stackTrace?: string;
  innerException?: ExceptionDetails;
}

/** F-7: 异常停止信息（DAP exceptionInfo 响应）。 */
export interface ExceptionInfoResp {
  exceptionId: string;
  description: string;
  /** "never" | "always" | "unhandled" | "userUnhandled" */
  breakMode: string;
  details?: ExceptionDetails;
}

/** F-7: 已加载源文件（DAP loadedSources 响应项）。 */
export interface DebugSource {
  name: string;
  path: string;
  sourceReference?: number;
}

/** F-7: 已加载模块（DAP modules 响应项）。 */
export interface DebugModule {
  id: number;
  name: string;
  path?: string;
  version?: string;
  symbolStatus?: string;
}

/** F-7: 调试控制台补全项（DAP completions 响应项）。 */
export interface DebugCompletionItem {
  label: string;
  text?: string;
  type?: string;
  start?: number;
  length?: number;
}

/** F-7: StepIn 目标（DAP stepInTargets 响应项）。 */
export interface StepInTarget {
  id: number;
  label: string;
}

/**
 * GOAL-P1-03: a step-in target list bound to the stop that produced it.
 *
 * `stopSequence` must be passed back to `stepInWithTarget`. A target ID is only
 * meaningful for the stop it was enumerated during — after a resume the
 * adapter's IDs may refer to nothing, or to something else entirely — so the
 * backend refuses a selection whose sequence no longer matches.
 *
 * `supported: false` means the adapter has no stepInTargets request (Node/browser
 * CDP, or a DAP adapter that rejects it). The UI must show no menu in that case
 * rather than an empty one.
 */
export interface StepInTargetSet {
  targets: StepInTarget[];
  stopSequence: number;
  supported: boolean;
}

/** F-7: 可设断点位置（DAP breakpointLocations 响应项）。 */
export interface BreakpointLocation {
  line: number;
  endLine?: number;
  column?: number;
  endColumn?: number;
}

/** 单个文件输入。 */
export interface DiffFileInput {
  path: string;
  oldContent: string;
  newContent: string;
}

/** 单文件审查结果（Step 9）。 */
export interface FileReview {
  path: string;
  comments: AIComment[];
}

/** 审查统计（Step 9）。 */
export interface ReviewStats {
  filesReviewed: number;
  totalComments: number;
  critical: number;
  errors: number;
  warnings: number;
}

/** PR 审查结果（Step 9）。 */
export interface ReviewPRResult {
  summary: string;
  fileReviews: FileReview[];
  stats: ReviewStats;
}

/** 导出格式（Step 10）。 */
export type DiffExportFormat = "markdown" | "unified" | "html";

// ============================================================================
// Plan 11 Task 14 — 智能回滚（快照）类型
// ============================================================================

/** 快照创建原因（Step 1/3）。 */
export type SnapshotReason =
  | "manual"
  | "plan-step"
  | "goal-checkpoint"
  | "pre-apply"
  | "workflow-step"
  | "file-save";

/** 单个文件的快照元数据（Step 1/4）。 */
export interface FileSnapshot {
  path: string;
  hash: string; // SHA-256 内容哈希
  size: number;
}

/** 快照创建时的 Git 状态（Step 1/8）。 */
export interface GitState {
  branch: string;
  isClean: boolean;
  changes?: string[];
}

/** 完整快照（Step 1）。 */
export interface Snapshot {
  id: string;
  createdAt: string; // ISO 时间
  reason: SnapshotReason;
  workspaceRoot: string;
  files: FileSnapshot[];
  gitState?: GitState;
  fileCount: number;
}

/** 两个快照之间的差异（Step 2: DiffSnapshots）。 */
export interface SnapshotDiff {
  fromSnapshotId: string;
  toSnapshotId: string;
  added: string[];
  removed: string[];
  modified: string[];
}

/**
 * GOAL-P1-01: what an exact restore would change, computed before touching the
 * workspace. Mirrors services.RestoreDiff.
 *
 * `addedAfterSnapshot` is the load-bearing field: those files will be
 * **permanently deleted** by the exact restore, so the UI must show them and
 * obtain explicit confirmation before calling `restoreSnapshotExact`.
 */
export interface RestoreDiff {
  addedAfterSnapshot: string[];
  modifiedSinceSnapshot: string[];
  removedFromWorkspace: string[];
}

/** 清理策略配置（Step 5）。 */
export interface CleanupConfig {
  keepN: number; // 保留最近 N 个（0 = 不限）
  maxAgeMs: number; // 最大保留时长毫秒（0 = 不过期）
}

/**
 * Priority 7 (prompt-1.md 422-432): Go pprof 性能分析集成。
 * 与后端 services.ProfileFunction / ProfileAnalysis 结构一致。
 */
export interface ProfileFunction {
  name: string;
  /** 累计时间（纳秒），对应 Go time.Duration 的 JSON 数值。 */
  cumulativeTime: number;
  /** 自身时间（纳秒）。 */
  flatTime: number;
  cumulativePercent: number;
  flatPercent: number;
}

export interface FlameGraphNode {
  id: string;
  name: string;
  value: number;
  children: FlameGraphNode[];
}

export interface ProfileAnalysis {
  totalSamples: number;
  /** 总时长（纳秒）。 */
  totalDuration: number;
  topFunctions: ProfileFunction[];
  flameGraph?: FlameGraphNode | null;
  /** 所选值列的单位（nanoseconds/count/bytes 等）。 */
  sampleUnit: string;
  /** 所选值列的类型名（samples/cpu/alloc_objects 等）。 */
  sampleType: string;
}

// ============================================================================
// IDEA-4: read-only database tools
// ============================================================================

export interface DatabaseConnectionConfig {
  id: string;
  name: string;
  provider: "sqlite" | string;
  databasePath?: string;
  credentialConfigId?: string;
  defaultSchema?: string;
}

/** Safe to retain in frontend state: paths, DSNs, and credentials are omitted. */
export interface DatabaseConnectionInfo {
  id: string;
  name: string;
  provider: string;
  defaultSchema?: string;
}

export interface DatabaseSchema {
  name: string;
}

export interface DatabaseTable {
  schema?: string;
  name: string;
  type: "table" | "view" | string;
}

export interface DatabaseColumn {
  name: string;
  dataType: string;
  nullable: boolean;
  defaultValue?: string;
  primaryKey: boolean;
  ordinal: number;
}

export interface DatabaseQueryRequest {
  requestId: string;
  connectionId: string;
  sql: string;
  parameters?: unknown[];
  page: number;
  pageSize: number;
}

export interface DatabaseQueryColumn {
  name: string;
  databaseType: string;
  nullable: boolean;
}

export interface DatabaseQueryResult {
  requestId: string;
  columns: DatabaseQueryColumn[];
  rows: unknown[][];
  page: number;
  pageSize: number;
  hasMore: boolean;
  durationMs: number;
}

/**
 * GOAL-P0-03: hot-exit / dirty-buffer recovery. Mirrors backend
 * services.RecoveryBaseline / RecoverableFile / CorruptRecoveryRecord /
 * RecoveryScan.
 */
export interface RecoveryBaseline {
  path: string;
  mtime: number;
  hash: string;
  exists: boolean;
}

/**
 * One recoverable buffer offered at startup. `status` is authoritative:
 * "clean" means the disk is unchanged since the baseline, so the buffer can be
 * restored directly; "conflict" means the disk changed and the user must choose;
 * "missing" means the file no longer exists on disk.
 */
export interface RecoverableFile {
  path: string;
  windowId: string;
  status: "clean" | "conflict" | "missing";
  content: string;
  diskContent: string;
  encoding: string;
  eol: string;
  updatedAt: number;
  baselineHash: string;
  currentHash: string;
}

export interface CorruptRecoveryRecord {
  file: string;
  reason: string;
}

export interface RecoveryScan {
  workspaceRoot: string;
  files: RecoverableFile[];
  corrupt: CorruptRecoveryRecord[];
  totalBytes: number;
}
