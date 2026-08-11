// Koyori IDE 模块 · Feature Registry。
// 喵，这是 Koyori IDE 的 Feature Registry 模块（前端实现）~
import {
  acquireDebugThreadsActivation,
} from "./debugThreads";

export interface FeatureRegistration {
  id: string;
  name: string;
  activate: () => void;
  deactivate?: () => void;
}

export interface FeatureComponentModule {
  default: unknown;
}

export type FeatureComponentLoader = () => Promise<FeatureComponentModule>;

const features = new Map<string, FeatureRegistration>();
const activeFeatures = new Set<string>();
const activationErrors = new Map<string, unknown>();
const componentLoaders = new Map<string, FeatureComponentLoader>();
let releaseDebugThreadsFeature: (() => void) | null = null;

function normalizedFeature(feature: FeatureRegistration): FeatureRegistration {
  const id = feature.id.trim();
  const name = feature.name.trim();
  if (!id || !/^[a-z][a-z0-9]*(?:[.-][a-z0-9]+)*$/i.test(id)) {
    throw new Error(`Invalid feature id: ${feature.id}`);
  }
  if (!name) throw new Error(`Feature ${id} must have a name`);
  if (typeof feature.activate !== "function") {
    throw new Error(`Feature ${id} must provide an activate function`);
  }
  if (feature.deactivate !== undefined && typeof feature.deactivate !== "function") {
    throw new Error(`Feature ${id} has an invalid deactivate function`);
  }
  return { ...feature, id, name };
}

export function registerFeature(feature: FeatureRegistration): void {
  const registration = normalizedFeature(feature);
  if (features.has(registration.id)) {
    console.warn(`[featureRegistry] feature "${registration.id}" already registered`);
    return;
  }
  features.set(registration.id, registration);
}

export function unregisterFeature(id: string): boolean {
  const normalizedId = id.trim();
  if (activeFeatures.has(normalizedId)) return false;
  activationErrors.delete(normalizedId);
  componentLoaders.delete(normalizedId);
  return features.delete(normalizedId);
}

export function listFeatures(): FeatureRegistration[] {
  return Array.from(features.values(), (feature) => ({ ...feature }));
}

export function isFeatureActive(id: string): boolean {
  return activeFeatures.has(id);
}

export function getFeatureActivationError(id: string): unknown {
  return activationErrors.get(id);
}

export function setFeatureComponentLoader(
  id: string,
  loader: FeatureComponentLoader,
): void {
  if (!features.has(id)) throw new Error(`Feature ${id} is not registered`);
  componentLoaders.set(id, loader);
}

export function getFeatureComponentLoader(
  id: string,
): FeatureComponentLoader | undefined {
  return componentLoaders.get(id);
}

export async function loadFeatureComponent(
  id: string,
): Promise<FeatureComponentModule> {
  const loader = componentLoaders.get(id);
  if (!loader) throw new Error(`Feature ${id} has no registered component`);
  return loader();
}

export function activateFeature(id: string): boolean {
  if (activeFeatures.has(id)) return true;
  const feature = features.get(id);
  if (!feature) return false;
  try {
    feature.activate();
    activeFeatures.add(id);
    activationErrors.delete(id);
    return true;
  } catch (error) {
    activationErrors.set(id, error);
    console.error(`Failed to activate feature ${id}:`, error);
    return false;
  }
}

export function deactivateFeature(id: string): boolean {
  if (!activeFeatures.has(id)) return features.has(id);
  const feature = features.get(id);
  if (!feature) {
    activeFeatures.delete(id);
    componentLoaders.delete(id);
    return false;
  }
  try {
    feature.deactivate?.();
    activeFeatures.delete(id);
    activationErrors.delete(id);
    return true;
  } catch (error) {
    activationErrors.set(id, error);
    console.error(`Failed to deactivate feature ${id}:`, error);
    return false;
  }
}

export function activateAllFeatures(): void {
  for (const id of features.keys()) activateFeature(id);
}

export function deactivateAllFeatures(): void {
  const ids = Array.from(features.keys()).reverse();
  for (const id of ids) deactivateFeature(id);
}

registerFeature({
  id: "debug.threads",
  name: "Multi-thread Debugging",
  activate: () => {
    releaseDebugThreadsFeature ??= acquireDebugThreadsActivation();
    setFeatureComponentLoader("debug.threads", () =>
      import("../components/debug/ThreadsPanel.vue"),
    );
  },
  deactivate: () => {
    componentLoaders.delete("debug.threads");
    releaseDebugThreadsFeature?.();
    releaseDebugThreadsFeature = null;
  },
});

registerFeature({
  id: "terminal.split",
  name: "Terminal Split Pane",
  activate: () => {
    setFeatureComponentLoader("terminal.split", () =>
      import("../components/layout/TerminalSplitPane.vue"),
    );
  },
  deactivate: () => {
    componentLoaders.delete("terminal.split");
  },
});

registerFeature({
  id: "git.worktree",
  name: "Git Worktree",
  activate: () => {
    setFeatureComponentLoader("git.worktree", () =>
      import("../components/git/WorktreePanel.vue"),
    );
  },
  deactivate: () => {
    componentLoaders.delete("git.worktree");
  },
});

registerFeature({
  id: "git.merge-editor",
  name: "3-Way Merge Editor",
  activate: () => {
    setFeatureComponentLoader("git.merge-editor", () =>
      import("../components/git/MergeEditor.vue"),
    );
  },
  deactivate: () => {
    componentLoaders.delete("git.merge-editor");
  },
});

registerFeature({
  id: "git.rebase-editor",
  name: "Interactive Rebase Editor",
  activate: () => {
    setFeatureComponentLoader("git.rebase-editor", () =>
      import("../components/git/RebaseEditor.vue"),
    );
  },
  deactivate: () => {
    componentLoaders.delete("git.rebase-editor");
  },
});

// Importing this module is the integration point: all built-in extensions are
// registered and activated exactly once through the module singleton.
activateAllFeatures();

if (import.meta.hot) {
  import.meta.hot.dispose(() => deactivateAllFeatures());
}
