/**
 * prompt-10 10-L + prompt-11 11-I: go.work / package.json multi-root awareness + switch.
 */
// Koyori IDE 模块 · Workspace Modules；交互服务：文件系统（FileService）、离线 LSP（LSPService）。
// 喵，这是 Koyori IDE 的 Workspace Modules 模块（前端实现）~
import { reactive } from "vue";
import { fileService, lspService } from "@/api/services";
import { appState } from "@/stores/app";
import { notifySuccess, notifyError } from "@/lib/notifications";
import { pushOutput } from "@/stores/output";

export const workspaceModulesState = reactive({
  goWorkModules: [] as string[],
  packageWorkspaces: [] as string[],
  /** Absolute paths of selectable roots */
  roots: [] as string[],
  activeRoot: "" as string,
  loading: false,
});

function joinRoot(root: string, rel: string): string {
  const r = root.replace(/[\\/]$/, "");
  const p = rel.replace(/^[\\/]/, "").replace(/"/g, "");
  if (p.startsWith("/") || /^[A-Za-z]:/.test(p)) return p;
  return r + "/" + p;
}

export async function refreshWorkspaceModules(): Promise<void> {
  const root = appState.currentProject;
  if (!root) {
    workspaceModulesState.goWorkModules = [];
    workspaceModulesState.packageWorkspaces = [];
    workspaceModulesState.roots = [];
    return;
  }
  workspaceModulesState.loading = true;
  try {
    const roots = new Set<string>([root.replace(/[\\/]$/, "")]);
    // go.work
    try {
      const text = await fileService.readFile(root.replace(/[\\/]$/, "") + "/go.work");
      const mods: string[] = [];
      for (const line of text.split(/\r?\n/)) {
        const t = line.trim();
        if (!t || t.startsWith("//") || t.startsWith("go ") || t === "use (" || t === ")") continue;
        if (t.startsWith("use ")) {
          mods.push(t.slice(4).trim().replace(/^"|"$/g, ""));
        } else if (!t.includes(" ")) {
          mods.push(t.replace(/^"|"$/g, ""));
        }
      }
      workspaceModulesState.goWorkModules = mods;
      for (const m of mods) roots.add(joinRoot(root, m));
    } catch {
      workspaceModulesState.goWorkModules = [];
    }
    // package.json workspaces
    try {
      const pkg = await fileService.readFile(root.replace(/[\\/]$/, "") + "/package.json");
      const j = JSON.parse(pkg) as { workspaces?: string[] | { packages?: string[] } };
      let list: string[] = [];
      if (Array.isArray(j.workspaces)) list = j.workspaces;
      else if (j.workspaces && Array.isArray(j.workspaces.packages)) list = j.workspaces.packages;
      workspaceModulesState.packageWorkspaces = list;
      for (const m of list) {
        // skip globs for absolute root list (keep pattern for display)
        if (!m.includes("*")) roots.add(joinRoot(root, m));
      }
    } catch {
      workspaceModulesState.packageWorkspaces = [];
    }
    workspaceModulesState.roots = Array.from(roots);
    if (!workspaceModulesState.activeRoot) {
      workspaceModulesState.activeRoot = root.replace(/[\\/]$/, "");
    }
  } finally {
    workspaceModulesState.loading = false;
  }
}

/**
 * prompt-11/12 12-H: switch active root for toolchain + coverage + LSP restart.
 */
export async function setActiveWorkspaceRoot(absPath: string): Promise<void> {
  const path = absPath.replace(/[\\/]$/, "");
  if (!path) return;
  workspaceModulesState.activeRoot = path;
  // Soft-update project path used by test explorer / debug when sub-root selected
  try {
    const { appState: as } = await import("@/stores/app");
    // do not overwrite project open; keep dual pointer via activeRoot only
    void as;
  } catch {
    /* ignore */
  }
  try {
    const { refreshGoTarget } = await import("@/stores/goTarget");
    await refreshGoTarget(path);
  } catch {
    /* ignore */
  }
  for (const lang of ["go", "typescript", "javascript"] as const) {
    try {
      await lspService.stopServer(lang);
    } catch {
      /* ignore */
    }
  }
  for (const lang of ["go", "typescript"] as const) {
    try {
      await lspService.startServer(lang);
    } catch {
      /* ignore */
    }
  }
  pushOutput("Workspace", "info", `Active analysis root → ${path}`);
  notifySuccess(`Workspace root: ${path}`);
}

export async function selectWorkspaceRootInteractive(): Promise<void> {
  await refreshWorkspaceModules();
  const roots = workspaceModulesState.roots;
  if (roots.length <= 1) {
    notifyError("No additional go.work / package workspaces detected");
    return;
  }
  // Simple cycle for palette without full UI: pick next root
  const cur = workspaceModulesState.activeRoot || appState.currentProject || "";
  const idx = roots.findIndex((r) => r.replace(/\\/g, "/") === cur.replace(/\\/g, "/"));
  const next = roots[(idx + 1) % roots.length];
  await setActiveWorkspaceRoot(next);
}
