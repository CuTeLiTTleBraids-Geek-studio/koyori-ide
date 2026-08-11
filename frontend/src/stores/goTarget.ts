// Koyori IDE 模块 · Go Target；交互服务：工具链（ToolchainService）。
// 喵，这是 Koyori IDE 的 Go Target 模块（前端实现）~
import { reactive } from "vue";
import { toolchainService } from "@/api/services";
import { notifyError } from "@/lib/notifications";
import type { GoTarget, GoTargetState } from "@/types";
import {
  clearWorkspaceGoTarget,
  loadWorkspaceGoTarget,
  saveWorkspaceGoTarget,
} from "./workspaceSettings";

export const goTargetState = reactive({
  targets: [] as GoTarget[],
  host: { goos: "", goarch: "" } as GoTarget,
  current: { goos: "", goarch: "" } as GoTarget,
  overridden: false,
  loading: false,
});

function applyState(state: GoTargetState): void {
  goTargetState.host = state.host;
  goTargetState.current = state.current;
  goTargetState.overridden = state.overridden;
}

export async function refreshGoTarget(workspaceRoot: string): Promise<void> {
  if (!workspaceRoot) return;
  goTargetState.loading = true;
  try {
    goTargetState.targets = await toolchainService.listGoTargets();
    const saved = loadWorkspaceGoTarget(workspaceRoot);
    try {
      applyState(saved
        ? await toolchainService.setGoTarget(saved.goos, saved.goarch)
        : await toolchainService.resetGoTarget());
    } catch (error) {
      clearWorkspaceGoTarget(workspaceRoot);
      applyState(await toolchainService.resetGoTarget());
      notifyError(error instanceof Error ? error.message : String(error));
    }
  } finally {
    goTargetState.loading = false;
  }
}

export async function selectGoTarget(workspaceRoot: string, target: GoTarget): Promise<void> {
  try {
    const state = await toolchainService.setGoTarget(target.goos, target.goarch);
    saveWorkspaceGoTarget(workspaceRoot, target);
    applyState(state);
  } catch (error) {
    notifyError(error instanceof Error ? error.message : String(error));
  }
}

export async function restoreHostGoTarget(workspaceRoot: string): Promise<void> {
  const state = await toolchainService.resetGoTarget();
  clearWorkspaceGoTarget(workspaceRoot);
  applyState(state);
}
