// Koyori IDE 模块 · Workspace Settings。
// 喵，这是 Koyori IDE 的 Workspace Settings 模块（前端实现）~
import type { GoTarget } from "@/types";

const PREFIX = "koyori-ide.workspace.";

function workspaceKey(workspaceRoot: string): string {
  const normalized = workspaceRoot.replace(/\\/g, "/").replace(/\/+$/, "");
  return `${PREFIX}${encodeURIComponent(normalized)}.goTarget`;
}

export function loadWorkspaceGoTarget(workspaceRoot: string): GoTarget | null {
  if (!workspaceRoot) return null;
  try {
    const parsed = JSON.parse(localStorage.getItem(workspaceKey(workspaceRoot)) ?? "null") as Partial<GoTarget> | null;
    if (!parsed || typeof parsed.goos !== "string" || typeof parsed.goarch !== "string") return null;
    if (!parsed.goos || !parsed.goarch) return null;
    return { goos: parsed.goos, goarch: parsed.goarch };
  } catch {
    return null;
  }
}

export function saveWorkspaceGoTarget(workspaceRoot: string, target: GoTarget): void {
  if (!workspaceRoot) return;
  localStorage.setItem(workspaceKey(workspaceRoot), JSON.stringify(target));
}

export function clearWorkspaceGoTarget(workspaceRoot: string): void {
  if (!workspaceRoot) return;
  localStorage.removeItem(workspaceKey(workspaceRoot));
}
