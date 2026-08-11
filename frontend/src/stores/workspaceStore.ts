/**
 * F-10 (task-5.md): Workspace folders store.
 *
 * 从 app.ts 拆出的多根工作区 (.code-workspace) 解析与管理工作区根列表。
 * 职责：
 *   - 持有 `workspaceFolders` 状态（通过 appState 委托到 projectStore，保持
 *     响应式语义与既有 `appState.workspaceFolders` 引用不变）。
 *   - 提供 addWorkspaceFolder / removeWorkspaceFolder / setWorkspaceFolders
 *     便捷访问器。
 *   - 提供 .code-workspace 文件解析工具（isCodeWorkspacePath /
 *     parseCodeWorkspaceContent / loadWorkspaceFolders）。
 *   - 提供 openProject 入口（包含 workspaceContains 扩展激活、snapshot /
 *     workflows 同步），与既有 app.ts 行为一致。
 *
 * 兼容性：app.ts re-export 本模块的所有公开符号，旧代码
 * `import { openProject, ... } from "@/stores/app"` 继续可用。
 */
// Koyori IDE 模块 · Workspace Store；交互服务：文件系统（FileService）、项目（ProjectService）。
// 喵，这是 Koyori IDE 的 Workspace Store 模块（前端实现）~
import { appState } from "@/stores/app";
import type { WorkspaceAuthoritySnapshot } from "@/api/workspace";
import type { CodeWorkspaceFile } from "@/types";

let authoritySnapshotApplied = false;

// ---------------------------------------------------------------------------
// 状态访问器（委托到 appState.workspaceFolders → projectStore.workspaceFolders）
// ---------------------------------------------------------------------------

/**
 * 当前工作区根列表。多根工作区 (.code-workspace) 时为所有根路径；单根项目为
 * [path]；空数组表示未加载（项目未打开）。
 *
 * 通过 Object.defineProperty 提供响应式 getter/setter，读写直接落到
 * appState.workspaceFolders，保持 Vue 响应式传播。
 */
export const workspaceStore = {
  get workspaceFolders(): string[] {
    return appState.workspaceFolders;
  },
  set workspaceFolders(v: string[]) {
    appState.workspaceFolders = v;
  },
  get root(): string | null {
    return appState.workspaceRoot;
  },
  get generation(): number {
    return appState.workspaceGeneration;
  },
};

/**
 * 设置工作区根列表（覆盖式）。
 */
export function setWorkspaceFolders(folders: string[]): void {
  appState.workspaceFolders = folders;
}

/**
 * 追加一个工作区根（去重，保留顺序）。
 */
export function addWorkspaceFolder(folder: string): void {
  if (!folder) return;
  const list = appState.workspaceFolders;
  if (list.includes(folder)) return;
  appState.workspaceFolders = [...list, folder];
}

/**
 * 移除一个工作区根。若移除后列表为空，保留空数组（不回退到单根语义）。
 */
export function removeWorkspaceFolder(folder: string): void {
  const list = appState.workspaceFolders;
  const idx = list.indexOf(folder);
  if (idx < 0) return;
  const next = list.slice();
  next.splice(idx, 1);
  appState.workspaceFolders = next;
}

// ---------------------------------------------------------------------------
// .code-workspace 解析工具（与后端 services.ParseCodeWorkspaceFile 行为一致）
// ---------------------------------------------------------------------------

/**
 * isCodeWorkspacePath 判断给定路径是否以 .code-workspace 扩展名结尾
 * （大小写不敏感，匹配 VS Code 行为）。与后端 IsCodeWorkspaceFile 一致。
 */
export function isCodeWorkspacePath(path: string): boolean {
  return path.toLowerCase().endsWith(".code-workspace");
}

/**
 * parseCodeWorkspaceContent 解析 .code-workspace 文件内容（JSON 字符串），
 * 返回其中的 folder 路径列表。每个 folder 的 path 字段若是相对路径，则以
 * baseDir 为基准解析为绝对路径；若是 file:// URI 则剥离协议前缀。
 *
 * 解析规则：
 *   - 仅取 folders 数组；忽略 settings 等其他字段。
 *   - 每个 folder 优先使用 path 字段；缺失则使用 uri 字段。
 *   - 相对路径以 baseDir 为基准 join；绝对路径保留。
 *   - 重复路径（解析后比较）去重，保留首次出现顺序。
 *   - 解析失败（JSON 格式错误、folder 缺失 path/uri）抛出 Error。
 *
 * @param content .code-workspace 文件的 JSON 字符串
 * @param baseDir .code-workspace 文件所在目录的绝对路径，用于解析相对路径
 * @returns 解析后的绝对路径列表（已去重，顺序保留）
 */
export function parseCodeWorkspaceContent(content: string, baseDir: string): string[] {
  if (!content.trim()) {
    throw new Error("code-workspace content is empty");
  }
  let ws: CodeWorkspaceFile;
  try {
    ws = JSON.parse(content) as CodeWorkspaceFile;
  } catch (e) {
    throw new Error(`parse code-workspace JSON: ${e instanceof Error ? e.message : String(e)}`);
  }
  if (!ws || !Array.isArray(ws.folders)) {
    throw new Error("code-workspace file missing 'folders' array");
  }
  const out: string[] = [];
  const seen = new Set<string>();
  for (let i = 0; i < ws.folders.length; i++) {
    const f = ws.folders[i] || {};
    let raw = f.path;
    if (!raw && f.uri) {
      raw = uriToLocalPath(f.uri);
    }
    if (!raw) {
      throw new Error(`code-workspace folder[${i}]: missing path/uri`);
    }
    const abs = resolveCodeWorkspaceFolder(raw, baseDir);
    if (!seen.has(abs)) {
      seen.add(abs);
      out.push(abs);
    }
  }
  return out;
}

/**
 * resolveCodeWorkspaceFolder 把单个 folder 路径解析为绝对路径。
 *   - file:// URI：剥离协议前缀后转本地路径。
 *   - 相对路径：与 baseDir join。
 *   - 绝对路径：保留。
 *
 * 仅做词法解析；不查询文件系统。
 */
function resolveCodeWorkspaceFolder(path: string, baseDir: string): string {
  let p = path;
  if (p.startsWith("file://")) {
    p = uriToLocalPath(p);
  }
  if (!isAbsolutePath(p)) {
    // 用 '/' 拼接再规范化（去除 './' 与多余分隔符）。
    const sep = baseDir.endsWith("/") || baseDir.endsWith("\\") ? "" : "/";
    p = `${baseDir}${sep}${p}`;
  }
  // 规范化：去除多余的 './' 与连续分隔符。Windows 下接受正反斜杠。
  return normalizePath(p);
}

/**
 * uriToLocalPath 将 file:// URI 转换为本地文件路径。
 * 跨平台兼容：file:///C:/... → C:/...（Windows）。
 */
function uriToLocalPath(uri: string): string {
  if (!uri.startsWith("file://")) {
    return uri;
  }
  let p = uri.slice("file://".length);
  // file:///C:/... → C:/...
  if (p.length >= 3 && p[0] === "/" && /^[a-zA-Z]$/.test(p[1]) && p[2] === ":") {
    p = p.slice(1);
  }
  // 统一把正斜杠转成当前平台的分隔符由调用方处理；这里返回正斜杠形式。
  return p;
}

/**
 * isAbsolutePath 判断路径是否为绝对路径。覆盖 POSIX（/开头）与
 * Windows（C:\ 或 C:/ 开头）。
 */
function isAbsolutePath(p: string): boolean {
  if (!p) return false;
  if (p.startsWith("/")) return true;
  // Windows drive: C:\ or C:/
  if (p.length >= 3 && /^[a-zA-Z]$/.test(p[0]) && p[1] === ":" && (p[2] === "\\" || p[2] === "/")) {
    return true;
  }
  return false;
}

/**
 * normalizePath 规范化路径：去除多余的 './' 与连续分隔符，统一分隔符为
 * 正斜杠。简单实现，不处理 '..'（跨平台 '..' 解析需要 fs/path，浏览器端
 * 不可用；.code-workspace 一般不会出现 .. 模式，由后端二次校验把关）。
 */
function normalizePath(p: string): string {
  // 统一分隔符为 '/'。
  let s = p.replace(/\\/g, "/");
  // 去除连续分隔符（但保留开头的双斜杠用于 UNC 路径——这里不处理 UNC）。
  s = s.replace(/\/+/g, "/");
  // 去除路径段中的 './'。
  const parts = s.split("/").filter((seg, i) => {
    if (seg === "." && i > 0) return false;
    if (seg === "" && i > 0 && s[i - 1] === "/") return false;
    return true;
  });
  // 重新拼接，保留开头的 '/'（POSIX 绝对路径）或 drive:（Windows 绝对路径）。
  let out = parts.join("/");
  if (s.startsWith("/") && !out.startsWith("/")) {
    out = "/" + out;
  }
  return out;
}

/**
 * loadWorkspaceFolders 读取 .code-workspace 文件并解析其 folders 数组。
 * 异步函数；读取或解析失败时拒绝。工作区文件不是目录，不能作为伪造的
 * 单根回退，否则前后端会发布不一致的根状态。
 *
 * 内部使用 fileService.readFile 读取文件内容，再调用
 * parseCodeWorkspaceContent 解析。
 *
 * @param workspacePath .code-workspace 文件的绝对路径
 * @returns 解析后的根路径列表（已去重，顺序保留）
 */
export async function loadWorkspaceFolders(workspacePath: string): Promise<string[]> {
  // 动态导入避免在测试环境中触发 Wails 绑定加载。
  const { fileService } = await import("@/api/services");
  const content = await fileService.readFile(workspacePath);
  // baseDir = .code-workspace 文件所在目录。
  const baseDir = workspacePath.substring(0, Math.max(workspacePath.lastIndexOf("/"), workspacePath.lastIndexOf("\\")));
  return parseCodeWorkspaceContent(content, baseDir);
}

// ---------------------------------------------------------------------------
// openProject — 打开项目入口
// ---------------------------------------------------------------------------

/**
 * openProject 打开项目：设置 currentProject/projectName，并根据是否为
 * .code-workspace 解析 workspaceFolders。同时触发 workspaceContains 扩展
 * 激活、snapshot 工作区根同步、workflows 加载。
 *
 * 多根项目先等待后端 AddMultiRootProject 完成全量校验和两阶段切换，再一次
 * 发布前端状态。app.ts re-export 本函数保持旧引用不变。
 */
export async function openProject(_name: string, path: string): Promise<void> {
  const { projectService } = await import("@/api/services");
  if (isCodeWorkspacePath(path)) {
    await projectService.addMultiRootProject([], path);
  } else {
    await projectService.addProject(path);
  }
  const snapshot = await projectService.getWorkspaceSnapshot();
  applyWorkspaceSnapshot(snapshot);
}

export function applyWorkspaceSnapshot(snapshot: WorkspaceAuthoritySnapshot): boolean {
  if (!Number.isSafeInteger(snapshot.generation) || snapshot.generation < 0) {
    console.warn("[workspace] ignored invalid backend generation", snapshot.generation);
    return false;
  }
  const roots = [...snapshot.roots];
  if ((snapshot.root === "") !== (roots.length === 0) ||
      (roots.length > 0 && roots[0] !== snapshot.root)) {
    console.warn("[workspace] ignored inconsistent backend snapshot", snapshot);
    return false;
  }

  const currentMatches = appState.workspaceGeneration === snapshot.generation &&
    appState.workspaceRoot === (snapshot.root || null) &&
    appState.currentProject === (snapshot.projectPath || snapshot.root || null) &&
    appState.projectName === (snapshot.projectName || null) &&
    appState.workspaceFolders.length === roots.length &&
    appState.workspaceFolders.every((root, index) => root === roots[index]);
  if (authoritySnapshotApplied && snapshot.generation < appState.workspaceGeneration) {
    return false;
  }
  if (authoritySnapshotApplied && snapshot.generation === appState.workspaceGeneration) {
    if (!currentMatches) {
      console.warn("[workspace] ignored conflicting snapshot for generation", snapshot.generation);
    }
    return false;
  }

  authoritySnapshotApplied = true;
  appState.workspaceGeneration = snapshot.generation;
  appState.workspaceRoot = snapshot.root || null;
  appState.currentProject = snapshot.projectPath || snapshot.root || null;
  appState.projectName = snapshot.projectName || null;
  appState.workspaceFolders = roots;

  if (currentMatches) return false;
  // F-3: single and multi-root activation use the same committed root list.
  triggerWorkspaceContainsForFolders(roots);

  const primaryRoot = roots[0] ?? "";
  if (!primaryRoot) return true;
  // prompt-4 Task 10: 打开项目时同步快照工作区根，激活智能回滚。
  void import("@/stores/snapshot").then(({ setSnapshotWorkspaceRoot }) => {
    setSnapshotWorkspaceRoot(primaryRoot);
  }).catch((error) => {
    console.warn("[workspace] snapshot store sync failed", error);
  });
  // 同步加载工作流（若 store 已就绪）
  void import("@/stores/workflows").then(({ loadWorkflows }) => {
    void loadWorkflows(primaryRoot);
  }).catch((error) => {
    console.warn("[workspace] workflow store sync failed", error);
  });
  return true;
}

export async function syncWorkspaceSnapshot(): Promise<boolean> {
  const { projectService } = await import("@/api/services");
  return applyWorkspaceSnapshot(await projectService.getWorkspaceSnapshot());
}

export function handleWorkspaceChangedEvent(event: unknown): void {
  const payload = event && typeof event === "object" && "data" in event
    ? (event as { data: unknown }).data
    : event;
  if (!payload || typeof payload !== "object") return;
  const snapshot = payload as Partial<WorkspaceAuthoritySnapshot>;
  if (typeof snapshot.root !== "string" || !Array.isArray(snapshot.roots) ||
      typeof snapshot.generation !== "number") {
    return;
  }
  applyWorkspaceSnapshot(snapshot as WorkspaceAuthoritySnapshot);
}

export function resetWorkspaceAuthorityForTesting(): void {
  authoritySnapshotApplied = false;
}

/**
 * F-3 (prompt-2.md): 对一组工作区根触发 workspaceContains 扩展激活。
 * 动态 import vscodeExtensionActivation 避免循环依赖。失败仅记录警告。
 */
function triggerWorkspaceContainsForFolders(folders: string[]): void {
  if (!folders || folders.length === 0) return;
  void import("@/lib/vscodeExtensionActivation").then(
    ({ activateOnWorkspaceContains }) => {
      for (const folder of folders) {
        if (folder) {
          void activateOnWorkspaceContains(folder);
        }
      }
    },
  ).catch((err) => console.warn("[F-3] activateOnWorkspaceContains failed:", err));
}
