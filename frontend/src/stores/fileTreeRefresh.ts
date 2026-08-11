// Koyori IDE 模块 · File Tree Refresh。
// 喵，这是 Koyori IDE 的 File Tree Refresh 模块（前端实现）~
import { reactive } from "vue";

interface FileTreeRefreshState {
  revision: number;
  paths: string[];
}

export const fileTreeRefreshState = reactive<FileTreeRefreshState>({
  revision: 0,
  paths: [],
});

export function notifyFileTreeRefresh(paths: string[]): void {
  fileTreeRefreshState.paths = [...new Set(paths)];
  fileTreeRefreshState.revision += 1;
}
