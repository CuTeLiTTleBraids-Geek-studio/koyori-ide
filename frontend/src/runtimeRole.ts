// Koyori IDE 模块 · Runtime Role。
// 喵，这是 Koyori IDE 的 Runtime Role 模块（前端实现）~
export const runtimeRoleQueryKey = "koyori-ide_runtime_role";

export type FrontendRuntimeRole = "main" | "ai" | "settings" | "e2e" | "minimal";

const acceptedRoles = new Set<FrontendRuntimeRole>([
  "main",
  "ai",
  "settings",
  "e2e",
]);

export function normalizeRuntimeRole(value: unknown): FrontendRuntimeRole {
  return typeof value === "string" && acceptedRoles.has(value as FrontendRuntimeRole)
    ? (value as FrontendRuntimeRole)
    : "minimal";
}

export function readRuntimeRoleToken(href?: string): string {
  const source = href ?? (typeof window === "undefined" ? "" : window.location.href);
  if (!source) return "";
  try {
    return new URL(source, "http://koyori-ide.invalid").searchParams.get(runtimeRoleQueryKey) ?? "";
  } catch {
    return "";
  }
}

export function isFullIDERuntimeRole(role: FrontendRuntimeRole): boolean {
  return role === "main" || role === "e2e";
}

export async function resolveRuntimeRoleToken(
  resolve: (token: string) => Promise<unknown>,
  token = readRuntimeRoleToken(),
): Promise<FrontendRuntimeRole> {
  if (!token) return "minimal";
  try {
    return normalizeRuntimeRole(await resolve(token));
  } catch {
    return "minimal";
  }
}
