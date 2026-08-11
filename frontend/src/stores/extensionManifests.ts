/**
 * Extension manifests store (Priority 8) — VSCode 扩展 activationEvents +
 * contributes 解析的前端编排。
 *
 * 本 store 是后端 services/extension_service.go 中 ParseExtensionManifest /
 * DiscoverExtensions / GetExtensionActivationEvents 的前端对应物。它：
 *   - 通过后端适配器（backend adapter）调用 Wails 绑定，懒加载以避免在绑
 *     定尚未生成时造成类型错误（与 extensionSecurity.ts 同一模式）。
 *   - 维护一个响应式的已发现扩展清单列表，供 UI（如扩展浏览器侧栏）消费。
 *   - 暴露 parseManifest / discoverExtensions / getActivationEvents 三个
 *     API 包装方法，可在测试中注入 mock 后端。
 *
 * 由于 Go 端这三个函数是包级函数（非注册服务的 method），Wails 绑定按需
 * 生成；为避免 vue-tsc 在绑定缺失时报“找不到模块”，默认后端使用动态
 * import() 加载绑定（动态 import 不被静态类型检查）。
 */
// Koyori IDE 模块 · Extension Manifests。
// 喵，这是 Koyori IDE 的 Extension Manifests 模块（前端实现）~

import { reactive, computed } from "vue";
import { errorMessage } from "@/lib/errors";
import type { ExtensionManifest } from "@/types";

// ---------------------------------------------------------------------------
// 后端适配器接口 —— API 包装方法
// ---------------------------------------------------------------------------

/**
 * 后端适配器：调用 Go 端 extension_service 的三个函数。默认实现懒加载
 * Wails 绑定；测试通过 setExtensionManifestBackend 注入 mock。
 */
export interface ExtensionManifestBackend {
  /** 解析单个 package.json 内容。 */
  parseManifest(packageJSON: string): Promise<ExtensionManifest>;
  /** 扫描目录下的扩展子目录并解析每个 package.json。 */
  discoverExtensions(extensionDir: string): Promise<ExtensionManifest[]>;
  /** 返回解析后的 activation events（含隐式推导）。 */
  getActivationEvents(manifest: ExtensionManifest): Promise<string[]>;
}

// 绑定模块的最小形状（动态加载，避免静态类型依赖）。
interface ExtensionServiceBindingsShape {
  ParseExtensionManifest(packageJSON: string): Promise<ExtensionManifest>;
  DiscoverExtensions(extensionDir: string): Promise<ExtensionManifest[]>;
  GetExtensionActivationEvents(manifest: ExtensionManifest): Promise<string[]>;
}

// ---------------------------------------------------------------------------
// Store 状态
// ---------------------------------------------------------------------------

interface ExtensionManifestStoreState {
  /** 已发现的扩展清单，按 publisher.name 排序（后端保证）。 */
  manifests: ExtensionManifest[];
  /** 当前选中的清单（用于详情视图），null 表示未选中。 */
  selected: ExtensionManifest | null;
  loading: boolean;
  error: string | null;
  /** 最近一次扫描的目录，便于 UI 显示与刷新。 */
  scannedDir: string | null;
}

export const extensionManifestStore = reactive<ExtensionManifestStoreState>({
  manifests: [],
  selected: null,
  loading: false,
  error: null,
  scannedDir: null,
});

export const discoveredManifests = computed(() => extensionManifestStore.manifests);
export const isLoadingManifests = computed(() => extensionManifestStore.loading);
export const manifestLoadError = computed(() => extensionManifestStore.error);
export const selectedManifest = computed(() => extensionManifestStore.selected);
export const scannedExtensionDir = computed(() => extensionManifestStore.scannedDir);

/**
 * 选中一个清单用于详情展示（null 清除选择）。
 */
export function selectManifest(manifest: ExtensionManifest | null): void {
  extensionManifestStore.selected = manifest;
}

// ---------------------------------------------------------------------------
// 后端注入
// ---------------------------------------------------------------------------

let backend: ExtensionManifestBackend | null = null;

/**
 * 注入后端适配器。测试传入 mock；应用启动时传入默认 Wails 适配器。
 * 传 null 重置为默认（懒加载）后端。
 */
export function setExtensionManifestBackend(b: ExtensionManifestBackend | null): void {
  backend = b;
}

let bindingsCache: ExtensionServiceBindingsShape | null = null;

// 绑定模块路径。使用变量（非字符串字面量）传给 import()，使 vue-tsc 不
// 会在静态类型检查阶段解析该模块——这样即使 wails 尚未重新生成
// extensionservice 绑定（package 级函数需要先生成），也不会引入新的类型
// 错误。运行时若绑定缺失，调用抛错；测试应通过 setExtensionManifestBackend
// 注入 mock 来规避。与 services.ts 中 “may need regen” 的注释同义。
const extensionServiceBindingPath = "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/extensionservice.js";

async function loadBindings(): Promise<ExtensionServiceBindingsShape> {
  if (bindingsCache) return bindingsCache;
  const mod = await import(/* @vite-ignore */ extensionServiceBindingPath) as unknown;
  bindingsCache = mod as ExtensionServiceBindingsShape;
  return bindingsCache;
}

function getDefaultBackend(): ExtensionManifestBackend {
  return {
    async parseManifest(packageJSON) {
      const b = await loadBindings();
      return (await b.ParseExtensionManifest(packageJSON)) as ExtensionManifest;
    },
    async discoverExtensions(extensionDir) {
      const b = await loadBindings();
      return (await b.DiscoverExtensions(extensionDir)) as ExtensionManifest[];
    },
    async getActivationEvents(manifest) {
      const b = await loadBindings();
      return (await b.GetExtensionActivationEvents(manifest)) as string[];
    },
  };
}

function getBackend(): ExtensionManifestBackend {
  if (backend) return backend;
  backend = getDefaultBackend();
  return backend;
}

// ---------------------------------------------------------------------------
// Store actions
// ---------------------------------------------------------------------------

/**
 * 扫描 extensionDir 下的扩展并加载到 store。成功后 manifests 被替换、
 * scannedDir 被记录。失败时 error 被设置且 manifests 保持不变。
 */
export async function loadDiscoveredExtensions(extensionDir: string): Promise<void> {
  extensionManifestStore.loading = true;
  extensionManifestStore.error = null;
  try {
    const list = await getBackend().discoverExtensions(extensionDir);
    extensionManifestStore.manifests = list ?? [];
    extensionManifestStore.scannedDir = extensionDir;
    // 选中项若已不在列表中则清除。
    if (extensionManifestStore.selected) {
      const stillPresent = extensionManifestStore.manifests.some(
        (m) => m.publisher === extensionManifestStore.selected!.publisher
          && m.name === extensionManifestStore.selected!.name,
      );
      if (!stillPresent) extensionManifestStore.selected = null;
    }
  } catch (e: unknown) {
    extensionManifestStore.error = errorMessage(e);
  } finally {
    extensionManifestStore.loading = false;
  }
}

/**
 * 解析单个 package.json 内容（不修改 store 列表）。返回解析后的清单或
 * null（失败时 error 被设置）。
 */
export async function parseExtensionManifest(
  packageJSON: string,
): Promise<ExtensionManifest | null> {
  extensionManifestStore.error = null;
  try {
    return await getBackend().parseManifest(packageJSON);
  } catch (e: unknown) {
    extensionManifestStore.error = errorMessage(e);
    return null;
  }
}

/**
 * 计算某个清单的解析后 activation events（含隐式推导）。失败时返回空数组。
 */
export async function resolveActivationEvents(
  manifest: ExtensionManifest,
): Promise<string[]> {
  extensionManifestStore.error = null;
  try {
    return await getBackend().getActivationEvents(manifest);
  } catch (e: unknown) {
    extensionManifestStore.error = errorMessage(e);
    return [];
  }
}

/**
 * 重置 store 状态。用于测试。
 */
export function resetExtensionManifestStore(): void {
  extensionManifestStore.manifests = [];
  extensionManifestStore.selected = null;
  extensionManifestStore.loading = false;
  extensionManifestStore.error = null;
  extensionManifestStore.scannedDir = null;
  backend = null;
  bindingsCache = null;
}

// ---------------------------------------------------------------------------
// 纯前端辅助函数（不依赖后端）：用于在不经过 Wails 的情况下快速摘要展示。
// ---------------------------------------------------------------------------

/**
 * 返回清单的可读标识："<publisher>.<name>"（缺失字段以空串占位）。
 */
export function manifestId(m: ExtensionManifest): string {
  return `${m.publisher}.${m.name}`;
}

/**
 * 统计清单的贡献点数量，用于 UI 概览徽标。
 */
export function manifestContributionCount(m: ExtensionManifest): number {
  const c = m.contributes ?? {};
  return (
    (c.languages?.length ?? 0) +
    (c.grammars?.length ?? 0) +
    (c.snippets?.length ?? 0) +
    (c.commands?.length ?? 0) +
    (c.configuration?.length ?? 0) +
    (c.debuggers?.length ?? 0) +
    (c.jsonValidation?.length ?? 0)
  );
}
