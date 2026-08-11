/**
 * F-10 (task-5.md): Connectivity store.
 *
 * Store 层聚合 `@/lib/connectivity.ts` 的网络/AI 服务连接状态，并提供
 * task-5.md 要求的便捷访问器：
 *   - checkConnectivity() — 主动触发一次连通性探测
 *   - setOnlineStatus() — 外部信号（如后端事件）覆盖 online 状态
 *
 * 兼容性：本模块 re-export `lib/connectivity.ts` 的所有公开符号，旧代码
 * 既可继续从 `@/lib/connectivity` 导入，也可改从 `@/stores/connectivityStore`
 * 导入。app.ts 同样 re-export 本模块以便旧引用 `from "@/stores/app"` 可用。
 *
 * 设计说明：connectivity.ts 已经是完整实现（含心跳、navigator 事件、
 * 序列号防竞态），本 store 不重复实现，仅做聚合与扩展。
 */
// Koyori IDE 模块 · Connectivity Store。
// 喵，这是 Koyori IDE 的 Connectivity Store 模块（前端实现）~
export {
  connectivityState,
  checkAIReachable,
  initConnectivityListener,
  unregisterConnectivityListener,
  stopConnectivityListener,
  __resetConnectivityForTesting,
  __refreshOnlineStateForTesting,
  type ConnectivityState,
} from "@/lib/connectivity";

import { connectivityState } from "@/lib/connectivity";

/**
 * 主动触发一次连通性探测（重置心跳节拍）。返回当前 online 状态。
 *
 * 与 lib/connectivity.ts 的 __refreshOnlineStateForTesting 不同，本函数
 * 是公开 API：调用后会刷新 connectivityState.online / aiReachable。
 */
export async function checkConnectivity(): Promise<boolean> {
  // __refreshOnlineStateForTesting 是 lib/connectivity.ts 内部 refreshOnlineState
  // 的公开别名（M-19 注释明确允许通过该名调用以触发探测）。
  const { __refreshOnlineStateForTesting } = await import("@/lib/connectivity");
  await __refreshOnlineStateForTesting();
  return connectivityState.online;
}

/**
 * 外部信号覆盖 online 状态（例如后端推送网络变化事件）。
 *
 * 注意：这会绕过心跳逻辑直接设置 connectivityState.online；下一次心跳 tick
 * 或 navigator 事件会重新校正。仅用于外部权威信号（如系统网络状态变化）。
 */
export function setOnlineStatus(online: boolean): void {
  connectivityState.online = online;
  if (!online) {
    connectivityState.aiReachable = false;
  }
}
