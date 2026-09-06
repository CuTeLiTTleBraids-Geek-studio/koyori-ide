/**
 * G-VSC-02: Permission system for the VS Code Extension Host.
 *
 * VS Code extensions declare their capabilities in `package.json`. The
 * Extension Host maps those declarations to a finite set of permissions
 * and classifies each extension into a security level. The level governs
 * whether the extension can activate without explicit user approval and
 * which privileged operations are gated at runtime.
 *
 * Security levels (G-SEC-12):
 *   - Trusted:    only safe permissions (fs.read, clipboard, ui.*). May
 *                 activate without approval.
 *   - Reviewed:   declares fs.write or shell.execute. May activate, but
 *                 each privileged operation is still permission-gated at
 *                 runtime (e.g. writeFile requires fs.write).
 *   - Restricted: declares network (or any combination that includes a
 *                 Restricted-tier permission). Disabled by default — the
 *                 user must explicitly approve the extension before it
 *                 can activate.
 *
 * The permission registry is module-level so that the `vscode` API shim
 * (which lives in a separate module) can query permissions by extension
 * id without holding a back-reference to the ExtensionHost instance.
 */
// Koyori IDE 模块 · Permissions。
// 喵，这是 Koyori IDE 的 Permissions 模块（前端实现）~

/**
 * The finite set of capabilities an extension may declare. These mirror
 * the G-VSC-02 spec and map from the extension's `package.json`
 * `koyoriIde.permissions` (or VS Code's `contributes`/activation request).
 *
 * F-6 (task-3.md): 补齐 tasks / debug / scm / env.openExternal / secrets
 * 等扩展宿主 API 表面所需的权限。
 */
export type ExtensionPermission =
  | "fs.read"
  | "fs.write"
  | "shell.execute"
  | "network"
  | "clipboard"
  | "ui.notifications"
  | "ui.webview"
  // F-6: 任务系统（注册/执行任务）
  | "tasks.execute"
  // F-6: 调试系统（启动调试会话）
  | "debug.execute"
  // F-6: 源代码管理（读写 git 状态、stage/unstage）
  | "scm.read"
  | "scm.write"
  // F-6: 打开外部链接（浏览器/系统默认程序）
  | "env.openExternal"
  // F-6: 安全存储（读写删除 secrets）
  | "secrets.read"
  | "secrets.write";

const KNOWN_EXTENSION_PERMISSIONS = new Set<ExtensionPermission>([
  "fs.read",
  "fs.write",
  "shell.execute",
  "network",
  "clipboard",
  "ui.notifications",
  "ui.webview",
  "tasks.execute",
  "debug.execute",
  "scm.read",
  "scm.write",
  "env.openExternal",
  "secrets.read",
  "secrets.write",
]);

/**
 * Coarse security tier assigned from the declared permissions. Drives the
 * default-enabled behavior and the approval gate.
 */
export type SecurityLevel = "Trusted" | "Reviewed" | "Restricted";

/**
 * Risk rank per permission. Higher number = higher risk. The classifier
 * takes the max across all declared permissions. Permissions not listed
 * here default to the Trusted tier (rank 0) so unknown permissions fail
 * safe-ish: they don't elevate the level on their own, but they also
 * don't grant any privileged operation (the runtime gates check the
 * exact permission string, not the tier).
 */
const PERMISSION_RANK: Record<ExtensionPermission, number> = {
  // Trusted tier (rank 0–1)
  "fs.read": 0,
  clipboard: 0,
  "ui.notifications": 0,
  "ui.webview": 0,
  // F-6: SCM 读取（git status/diff）属于只读操作 → Trusted
  "scm.read": 0,
  // F-6: 安全存储读取属于只读操作 → Trusted
  "secrets.read": 0,
  // Reviewed tier (rank 2)
  "fs.write": 2,
  "shell.execute": 2,
  // F-6: 任务执行（运行构建/测试任务）→ Reviewed
  "tasks.execute": 2,
  // F-6: 调试执行（启动 DAP 调试会话）→ Reviewed
  "debug.execute": 2,
  // F-6: SCM 写入（stage/unstage/commit）→ Reviewed
  "scm.write": 2,
  // F-6: 安全存储写入 → Reviewed
  "secrets.write": 2,
  // Restricted tier (rank 3)
  network: 3,
  // F-6: 打开外部链接（浏览器/系统默认程序）可能被滥用 → Restricted
  "env.openExternal": 3,
};

const REVIEWED_THRESHOLD = 2;
const RESTRICTED_THRESHOLD = 3;

/**
 * Determine the security level from requested permissions. The highest
 * risk permission wins: a single `network` declaration classifies the
 * whole extension as Restricted, even if it also declares `fs.read`.
 *
 * Empty permission list → Trusted (the extension only gets the always-
 * allowed surface like command registration).
 */
export function classifyExtension(
  permissions: ExtensionPermission[],
): SecurityLevel {
  let maxRank = 0;
  for (const perm of permissions) {
    const rank = PERMISSION_RANK[perm] ?? RESTRICTED_THRESHOLD;
    if (rank > maxRank) maxRank = rank;
  }
  if (maxRank >= RESTRICTED_THRESHOLD) return "Restricted";
  if (maxRank >= REVIEWED_THRESHOLD) return "Reviewed";
  return "Trusted";
}

/**
 * Normalize a descriptor's permissions without requiring the legacy
 * `koyoriIde.permissions` declaration. VSIX installation derives this list
 * from the manifest and bundled entrypoint before the descriptor is created;
 * a missing list here therefore means the descriptor has no granted
 * privileged capabilities, rather than an install-time rejection.
 */
export function requireDeclaredPermissions(
  permissions: ExtensionPermission[] | undefined,
  _hasExecutableMain: boolean,
): ExtensionPermission[] {
  if (!Array.isArray(permissions)) {
    return [];
  }
  const validated: ExtensionPermission[] = [];
  const seen = new Set<ExtensionPermission>();
  for (const permission of permissions as unknown[]) {
    if (
      typeof permission !== "string" ||
      !KNOWN_EXTENSION_PERMISSIONS.has(permission as ExtensionPermission)
    ) {
      throw new Error(`Unknown extension permission: ${String(permission)}`);
    }
    const known = permission as ExtensionPermission;
    if (!seen.has(known)) {
      seen.add(known);
      validated.push(known);
    }
  }
  return validated;
}

// ---------------------------------------------------------------------------
// Permission registry
// ---------------------------------------------------------------------------

/**
 * Module-level registry mapping extension id → declared permissions. The
 * ExtensionHost populates this on activation and clears it on
 * deactivation. The `vscode` API shim queries it via `hasPermission`
 * before dispatching privileged operations.
 */
interface PermissionRegistryState {
  extensionPermissions: Map<string, Set<ExtensionPermission>>;
}

const permissionRegistryState: PermissionRegistryState = {
  extensionPermissions: new Map<string, Set<ExtensionPermission>>(),
};

/**
 * Register the permissions an extension declared. Called by the
 * ExtensionHost during activation. Re-registration overwrites the
 * previous set (idempotent for re-activation).
 */
export function registerExtensionPermissions(
  extensionId: string,
  permissions: ExtensionPermission[],
): void {
  permissionRegistryState.extensionPermissions.set(extensionId, new Set(permissions));
}

/**
 * Remove an extension's permissions from the registry. Called by the
 * ExtensionHost during deactivation so that subsequent `hasPermission`
 * lookups for the extension return false.
 */
export function unregisterExtensionPermissions(extensionId: string): void {
  permissionRegistryState.extensionPermissions.delete(extensionId);
}

/**
 * Check if an extension has a specific permission. Returns false for
 * unknown extensions (fail-closed: no permission record → no access).
 */
export function hasPermission(
  extensionId: string,
  permission: ExtensionPermission,
): boolean {
  const set = permissionRegistryState.extensionPermissions.get(extensionId);
  return set ? set.has(permission) : false;
}

/**
 * Clear the entire permission registry. Used in tests and on a full
 * extension reset.
 */
export function clearPermissionRegistry(): void {
  permissionRegistryState.extensionPermissions.clear();
}
