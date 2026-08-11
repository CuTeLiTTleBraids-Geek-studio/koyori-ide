/**
 * Extension API surface restriction (G-SEC-12 requirement 4).
 *
 * This module defines which VS Code-compatible APIs are exposed to which
 * security levels (Trusted / Reviewed / Restricted). It is the
 * compatibility layer that translates VS Code extension API calls into
 * the koyori-ide permission-gated koyoriIde.* surface.
 *
 * Rules (G-SEC-12 req. 4):
 *   - Trusted: read-only APIs — fs.read, languages.register*Provider,
 *     commands.registerCommand.
 *   - Reviewed: adds fs.write (with path validation),
 *     window.showInformationMessage.
 *   - Restricted: adds shell.execute (with confirmation) and network
 *     (with per-request approval).
 *
 * Dangerous commands always require confirmation regardless of level:
 *   - workbench.action.terminal.sendSequence / kill
 *   - workbench.action.files.save / saveAll
 *   - workbench.action.reloadWindow / closeWindow / closeFolder / newWindow / quit
 *   - workbench.action.openSettings / zoomIn / zoomOut / toggleSidebarVisibility
 *   - _workbench.*
 *
 * The API surface also enforces resource isolation (G-SEC-12 req. 5):
 * extensions never receive appState or window.go bindings directly.
 */
// Koyori IDE 模块 · Api Surface。
// 喵，这是 Koyori IDE 的 Api Surface 模块（前端实现）~

import type {
  ExtensionSecurityLevel,
  ExtensionPermission,
} from "@/stores/extensionSecurity";

// ---------------------------------------------------------------------------
// API → permission mapping
//
// Each VS Code-compatible API method maps to the permission it requires.
// Methods not in this map are unavailable to extensions (deny-by-default).
// ---------------------------------------------------------------------------

/**
 * The set of VS Code-compatible API methods exposed to extensions. Each
 * entry records the required permission and the minimum security level
 * that can call it.
 */
export interface ApiMethodSpec {
  /** The permission the extension must have declared. */
  permission: ExtensionPermission | null;
  /** The minimum security level required (trusted < reviewed < restricted). */
  minLevel: ExtensionSecurityLevel;
  /** Whether the method requires interactive confirmation before running. */
  requiresConfirmation?: boolean;
  /** Human-readable label for the confirmation dialog. */
  confirmLabel?: string;
}

const LEVEL_RANK: Record<ExtensionSecurityLevel, number> = {
  trusted: 0,
  reviewed: 1,
  restricted: 2,
};

/**
 * The canonical API surface map. An extension at level L may call method M
 * iff LEVEL_RANK[L] >= LEVEL_RANK[API[M].minLevel] AND the extension
 * declared API[M].permission (when non-null).
 *
 * Methods with requiresConfirmation=true always prompt the user, even for
 * Trusted extensions — this is the "dangerous commands require
 * confirmation" rule.
 */
export const API_SURFACE: Record<string, ApiMethodSpec> = {
  // --- Trusted (read-only) ---
  "fs.readFile": {
    permission: "fs.read",
    minLevel: "trusted",
  },
  "fs.readdir": {
    permission: "fs.read",
    minLevel: "trusted",
  },
  "fs.readDirectory": {
    permission: "fs.read",
    minLevel: "trusted",
  },
  "languages.registerCompletionItemProvider": {
    permission: null, // registration is always allowed
    minLevel: "trusted",
  },
  "languages.registerHoverProvider": {
    permission: null,
    minLevel: "trusted",
  },
  "languages.registerDefinitionProvider": {
    permission: null,
    minLevel: "trusted",
  },
  // F-6 (task-3.md): 补齐 languages API 完整集合。所有 register*Provider
  // 都是注册行为，本身不读取敏感数据 → Trusted + null permission。
  "languages.registerReferenceProvider": {
    permission: null,
    minLevel: "trusted",
  },
  "languages.registerCodeActionsProvider": {
    permission: null,
    minLevel: "trusted",
  },
  "languages.registerCodeLensProvider": {
    permission: null,
    minLevel: "trusted",
  },
  "languages.registerDocumentFormattingEditProvider": {
    permission: null,
    minLevel: "trusted",
  },
  "languages.registerDocumentRangeFormattingEditProvider": {
    permission: null,
    minLevel: "trusted",
  },
  "languages.registerOnTypeFormattingEditProvider": {
    permission: null,
    minLevel: "trusted",
  },
  "languages.registerSignatureHelpProvider": {
    permission: null,
    minLevel: "trusted",
  },
  "languages.registerWorkspaceSymbolProvider": {
    permission: null,
    minLevel: "trusted",
  },
  "languages.registerDocumentLinkProvider": {
    permission: null,
    minLevel: "trusted",
  },
  "languages.registerColorProvider": {
    permission: null,
    minLevel: "trusted",
  },
  "languages.registerFoldingRangeProvider": {
    permission: null,
    minLevel: "trusted",
  },
  "languages.registerDeclarationProvider": {
    permission: null,
    minLevel: "trusted",
  },
  "languages.registerImplementationProvider": {
    permission: null,
    minLevel: "trusted",
  },
  "languages.registerTypeDefinitionProvider": {
    permission: null,
    minLevel: "trusted",
  },
  "languages.registerRenameProvider": {
    permission: null,
    minLevel: "trusted",
  },
  "languages.registerDocumentSymbolProvider": {
    permission: null,
    minLevel: "trusted",
  },
  "languages.registerDocumentSemanticTokensProvider": {
    permission: null,
    minLevel: "trusted",
  },
  "languages.registerDocumentHighlightProvider": {
    permission: null,
    minLevel: "trusted",
  },
  "languages.registerInlayHintsProvider": {
    permission: null,
    minLevel: "trusted",
  },
  "commands.registerCommand": {
    permission: null,
    minLevel: "trusted",
  },
  "window.showInformationMessage": {
    permission: "ui.notifications",
    minLevel: "trusted",
  },
  "window.showWarningMessage": {
    permission: "ui.notifications",
    minLevel: "trusted",
  },
  "window.showErrorMessage": {
    permission: "ui.notifications",
    minLevel: "trusted",
  },
  // F-6: showInputBox/showQuickPick 属于 UI 交互，归入 ui.notifications 权限
  "window.showInputBox": {
    permission: "ui.notifications",
    minLevel: "trusted",
  },
  "window.showQuickPick": {
    permission: "ui.notifications",
    minLevel: "trusted",
  },
  "window.createOutputChannel": {
    permission: "ui.notifications",
    minLevel: "trusted",
  },
  "window.registerTreeDataProvider": {
    permission: null,
    minLevel: "trusted",
  },
  "window.registerWebviewViewProvider": {
    permission: "ui.webview",
    minLevel: "reviewed",
  },
  // F-6: workspace 只读事件与查询 → Trusted
  "workspace.findFiles": {
    permission: "fs.read",
    minLevel: "trusted",
  },
  "workspace.findTextInFiles": {
    permission: "fs.read",
    minLevel: "trusted",
  },
  "workspace.openTextDocument": {
    permission: "fs.read",
    minLevel: "trusted",
  },
  "workspace.onDidSaveTextDocument": {
    permission: null,
    minLevel: "trusted",
  },
  "workspace.onDidChangeTextDocument": {
    permission: null,
    minLevel: "trusted",
  },
  "workspace.onDidOpenTextDocument": {
    permission: null,
    minLevel: "trusted",
  },
  // F-6: env.clipboard 读取/写入 → Trusted（已存在 clipboard 权限）
  "env.clipboard.readText": {
    permission: "clipboard",
    minLevel: "trusted",
  },
  "env.clipboard.writeText": {
    permission: "clipboard",
    minLevel: "trusted",
  },
  // F-6: secrets 读取 → Trusted
  "secrets.get": {
    permission: "secrets.read",
    minLevel: "trusted",
  },
  // F-6: scm 读取（git status/diff/branches）→ Trusted
  "scm.getStatus": {
    permission: "scm.read",
    minLevel: "trusted",
  },
  "scm.getBranchInfo": {
    permission: "scm.read",
    minLevel: "trusted",
  },
  "scm.getDiff": {
    permission: "scm.read",
    minLevel: "trusted",
  },
  "scm.createSourceControl": {
    permission: "scm.read",
    minLevel: "trusted",
  },
  // F-6: tasks.fetchTasks（只读取任务定义）→ Trusted
  "tasks.fetchTasks": {
    permission: null,
    minLevel: "trusted",
  },
  "tasks.registerTaskProvider": {
    permission: null,
    minLevel: "trusted",
  },
  // F-6: debug 注册 provider 不读取敏感数据 → Trusted
  "debug.registerDebugConfigurationProvider": {
    permission: null,
    minLevel: "trusted",
  },
  // F-6: machineId/sessionId 是只读标识符 → Trusted（无需权限）
  "env.machineId": {
    permission: null,
    minLevel: "trusted",
  },
  "env.sessionId": {
    permission: null,
    minLevel: "trusted",
  },

  // --- Reviewed (file write + restricted terminal) ---
  "fs.writeFile": {
    permission: "fs.write",
    minLevel: "reviewed",
  },
  "fs.deleteFile": {
    permission: "fs.write",
    minLevel: "reviewed",
  },
  "fs.rename": {
    permission: "fs.write",
    minLevel: "reviewed",
  },
  "fs.delete": {
    permission: "fs.write",
    minLevel: "reviewed",
  },
  "fs.createDirectory": {
    permission: "fs.write",
    minLevel: "reviewed",
  },
  "workspace.applyEdit": {
    permission: "fs.write",
    minLevel: "reviewed",
  },
  "workspace.saveAll": {
    permission: "fs.write",
    minLevel: "reviewed",
  },
  "window.createWebviewPanel": {
    permission: "ui.webview",
    minLevel: "reviewed",
  },
  "window.createTerminal": {
    permission: "shell.execute",
    minLevel: "reviewed",
    requiresConfirmation: true,
    confirmLabel: "Create terminal",
  },
  // F-6: tasks.executeTask（运行构建/测试任务）→ Reviewed
  "tasks.executeTask": {
    permission: "tasks.execute",
    minLevel: "reviewed",
    requiresConfirmation: true,
    confirmLabel: "Execute task",
  },
  // F-6: debug.startDebugging（启动 DAP 调试会话）→ Reviewed
  "debug.startDebugging": {
    permission: "debug.execute",
    minLevel: "reviewed",
    requiresConfirmation: true,
    confirmLabel: "Start debugging",
  },
  // F-6: scm 写入（stage/unstage/commit）→ Reviewed
  "scm.stage": {
    permission: "scm.write",
    minLevel: "reviewed",
  },
  "scm.unstage": {
    permission: "scm.write",
    minLevel: "reviewed",
  },
  "scm.commit": {
    permission: "scm.write",
    minLevel: "reviewed",
  },
  // F-6: secrets 写入/删除 → Reviewed
  "secrets.store": {
    permission: "secrets.write",
    minLevel: "reviewed",
  },
  "secrets.delete": {
    permission: "secrets.write",
    minLevel: "reviewed",
  },

  // --- Restricted (network + unrestricted shell) ---
  "shell.execute": {
    permission: "shell.execute",
    minLevel: "restricted",
    requiresConfirmation: true,
    confirmLabel: "Execute shell command",
  },
  "network.request": {
    permission: "network",
    minLevel: "restricted",
    requiresConfirmation: true,
    confirmLabel: "Make network request",
  },
  "child_process.exec": {
    permission: "shell.execute",
    minLevel: "restricted",
    requiresConfirmation: true,
    confirmLabel: "Execute child process",
  },
  // F-6: env.openExternal（打开外部链接）可能被滥用为数据外泄通道 → Restricted
  "env.openExternal": {
    permission: "env.openExternal",
    minLevel: "restricted",
    requiresConfirmation: true,
    confirmLabel: "Open external URL",
  },
};

// ---------------------------------------------------------------------------
// Dangerous commands — always require confirmation regardless of level.
// These are VS Code built-in commands that can cause side effects beyond
// the extension's declared permissions. G-SEC-12 req. 4.
// ---------------------------------------------------------------------------

/**
 * Commands that always require user confirmation before execution,
 * regardless of the calling extension's security level. Matching is
 * prefix-based for wildcard entries (e.g. "_workbench.*").
 *
 * H-15: 黑名单已扩充以覆盖更多会带来副作用的 VS Code 内置命令，
 * 例如批量保存、关闭窗口、退出应用、重载窗口、修改设置、缩放、
 * 切换侧边栏等。这些命令可能绕过扩展声明的权限范围，因此统一
 * 强制要求用户确认。
 */
export const DANGEROUS_COMMANDS: readonly string[] = [
  // 终端相关：可向终端注入按键序列或杀掉终端进程
  "workbench.action.terminal.sendSequence",
  "workbench.action.terminal.kill",
  // 文件保存：可能覆盖用户未保存的修改
  "workbench.action.files.save",
  "workbench.action.files.saveAll",
  // 窗口/工作区管理：影响用户工作状态
  "workbench.action.reloadWindow",
  "workbench.action.closeWindow",
  "workbench.action.closeFolder",
  "workbench.action.newWindow",
  "workbench.action.quit",
  // 设置入口：可能修改用户配置
  "workbench.action.openSettings",
  // UI 缩放：影响可用性
  "workbench.action.zoomIn",
  "workbench.action.zoomOut",
  // 侧边栏可见性：影响用户布局
  "workbench.action.toggleSidebarVisibility",
  // 通配符前缀：所有 VS Code 内部下划线开头命令均视为危险
  "_workbench.*", // prefix match — all internal workbench commands
];

/**
 * Check if a command ID is in the dangerous-commands list. Wildcard
 * entries (ending in ".*") match by prefix.
 */
export function isDangerousCommand(commandId: string): boolean {
  for (const pattern of DANGEROUS_COMMANDS) {
    if (pattern.endsWith(".*")) {
      const prefix = pattern.slice(0, -1); // keep the dot
      if (commandId.startsWith(prefix)) return true;
    } else if (commandId === pattern) {
      return true;
    }
  }
  return false;
}

// ---------------------------------------------------------------------------
// Access checks
// ---------------------------------------------------------------------------

/**
 * Result of an API access check. `allowed` is false when the extension
 * lacks the required permission or security level. `requiresConfirmation`
 * is true for dangerous/restricted operations that need a user prompt.
 */
export interface ApiAccessResult {
  allowed: boolean;
  reason?: string;
  requiresConfirmation: boolean;
  confirmLabel?: string;
}

/**
 * Check if an extension at the given level with the given declared
 * permissions may call an API method. Returns an ApiAccessResult.
 *
 * G-SEC-12 req. 4: the compatibility layer only exposes read-only +
 * restricted write APIs. Dangerous commands require confirmation.
 */
export function checkApiAccess(
  method: string,
  level: ExtensionSecurityLevel,
  declaredPermissions: ExtensionPermission[],
): ApiAccessResult {
  const spec = API_SURFACE[method];
  if (!spec) {
    // Deny-by-default: unknown methods are not exposed.
    return {
      allowed: false,
      reason: `API method "${method}" is not exposed to extensions.`,
      requiresConfirmation: false,
    };
  }

  // Level gate: the extension must be at or above the method's min level.
  if (LEVEL_RANK[level] < LEVEL_RANK[spec.minLevel]) {
    return {
      allowed: false,
      reason: `API method "${method}" requires security level "${spec.minLevel}" or higher (extension is "${level}").`,
      requiresConfirmation: false,
    };
  }

  // Permission gate: the extension must have declared the required permission.
  if (spec.permission !== null) {
    if (!declaredPermissions.includes(spec.permission)) {
      return {
        allowed: false,
        reason: `API method "${method}" requires permission "${spec.permission}" which the extension did not declare.`,
        requiresConfirmation: false,
      };
    }
  }

  return {
    allowed: true,
    requiresConfirmation: spec.requiresConfirmation === true,
    confirmLabel: spec.confirmLabel,
  };
}

/**
 * Check if a command execution should require confirmation. Returns true
 * for dangerous commands (G-SEC-12 req. 4) and for shell/network methods
 * in the API surface.
 */
export function shouldConfirmCommand(
  commandId: string,
  level: ExtensionSecurityLevel,
): boolean {
  // Dangerous commands always require confirmation.
  if (isDangerousCommand(commandId)) return true;
  // Shell/network API methods require confirmation (already encoded in
  // API_SURFACE, but command IDs may differ from API method names).
  if (level === "restricted") {
    // Restricted extensions touching shell/network always confirm.
    if (commandId.startsWith("shell.") || commandId.startsWith("network.")) {
      return true;
    }
  }
  return false;
}

// ---------------------------------------------------------------------------
// Exposed API namespaces per level
//
// Used by the extension host to build the `vscode`-compatible API object
// passed to an extension's activate() function. Only the namespaces listed
// here are present on the object; absent namespaces are `undefined` so
// accessing them throws naturally.
// ---------------------------------------------------------------------------

/**
 * The list of API namespace keys exposed at each security level. Lower
 * levels are subsets of higher levels.
 *
 * F-6 (task-3.md): 新增 tasks / debug / scm / env / secrets 命名空间。
 * - tasks / debug / scm.read / env.clipboard / env.machineId / env.sessionId /
 *   secrets.get / languages.* 注册 → Trusted
 * - tasks.executeTask / debug.startDebugging / scm.write / secrets.write /
 *   window.createWebviewPanel / workspace.saveAll / fs.rename / fs.delete → Reviewed
 * - env.openExternal / shell.execute / network.request → Restricted
 */
export const EXPOSED_NAMESPACES: Record<ExtensionSecurityLevel, readonly string[]> = {
  trusted: [
    "commands",
    "languages",
    "window", // only show*Message + register*Provider + showInputBox/showQuickPick/createOutputChannel/registerTreeDataProvider
    "workspace", // only readFile / readdir / findFiles / findTextInFiles / openTextDocument / onDid* events
    // F-6: 新增只读命名空间
    "tasks", // fetchTasks + registerTaskProvider
    "debug", // registerDebugConfigurationProvider
    "scm", // createSourceControl + read-only ops
    "env", // clipboard + machineId + sessionId
    "secrets", // get (read-only)
  ],
  reviewed: [
    "commands",
    "languages",
    "window",
    "workspace", // adds writeFile / applyEdit / saveAll
    "tasks", // adds executeTask
    "debug", // adds startDebugging
    "scm", // adds stage/unstage/commit
    "env",
    "secrets", // adds store/delete
  ],
  restricted: [
    "commands",
    "languages",
    "window",
    "workspace",
    "tasks",
    "debug",
    "scm",
    "env", // adds openExternal
    "secrets",
    "shell", // adds execute (with confirmation)
    "network", // adds request (with per-request approval)
  ],
};

/**
 * Returns the list of API method names an extension at the given level
 * (with the given declared permissions) may call. Used by the host to
 * build the gated proxy and by the permission dialog to list exactly
 * which APIs will be available.
 */
export function allowedMethodsFor(
  level: ExtensionSecurityLevel,
  declaredPermissions: ExtensionPermission[],
): string[] {
  const out: string[] = [];
  for (const method of Object.keys(API_SURFACE)) {
    const result = checkApiAccess(method, level, declaredPermissions);
    if (result.allowed) out.push(method);
  }
  return out;
}

/**
 * Build a human-readable summary of the API surface for an extension at
 * the given level. Used by the permission dialog's "Requested permissions"
 * list when the raw permission strings are too terse.
 */
export function apiSurfaceSummary(level: ExtensionSecurityLevel): string {
  switch (level) {
    case "trusted":
      return "Read-only: read files, register language providers and commands, show notifications, query tasks/scm/debug providers, read clipboard and secrets.";
    case "reviewed":
      return "Read + write: create/modify files, apply edits, create webview panels, execute tasks and debug sessions, stage/commit SCM changes, store secrets.";
    case "restricted":
      return "Read + write + network/shell: execute commands, make network requests, and open external URLs (with confirmation).";
    default:
      return "Unknown access level.";
  }
}
