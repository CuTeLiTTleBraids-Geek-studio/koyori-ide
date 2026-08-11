<script setup lang="ts">
// Koyori IDE 组件 · Layout Leaf View。
// 喵，这是 Layout Leaf View，负责 Koyori IDE 的界面呈现喵~
// N-30: Renders a single leaf in the layout tree. The viewId determines
// which view is displayed. When viewId is null, an empty state is shown.
//
// N-35 (prompt-5.md): The leaf now renders plugin-registered views in
// addition to the built-in editor. Plugin views are looked up via
// pluginRegistry.listPluginViews() — non-sandboxed plugins provide a
// component directly, sandboxed plugins provide null (handled by the
// iframe wrapper in N-36).
//
// N-57 (Proposal Q): listPluginViews() is now reactive — it reads a
// version counter that bumps on every register/unregister. This
// computed re-evaluates automatically when the registry changes,
// replacing the previous 2-second polling.
//
// Proposal H2 (prompt-5.md): When viewId is null, a welcome panel lists
// all registered views (built-in + plugin) and lets the user pick one
// to open in this leaf via replaceLeafView.
import { computed } from "vue";
import type { LayoutLeaf, PluginManifest } from "@/types";
import EditorView from "@/views/EditorView.vue";
import PluginViewIframe from "@/components/layout/PluginViewIframe.vue";
import { useI18n } from "@/lib/i18n";
import { listPluginViews, getPluginInfo, type RegisteredView } from "@/lib/pluginRegistry";
import { createSandboxRpcHandler } from "@/lib/pluginRegistry";
import { replaceLeafView } from "@/stores/layout";

const props = defineProps<{
  leaf: LayoutLeaf;
  active: boolean;
}>();

const emit = defineEmits<{
  activate: [leafId: string];
}>();

const { t } = useI18n();

const viewId = computed(() => props.leaf.viewId);

// Available views: built-in editor + plugin-registered views.
// N-57: This is a computed that depends on listPluginViews(), which
// reads the reactive viewsVersion ref. When a plugin registers or
// unregisters a view, this re-evaluates automatically — no polling.
interface ViewOption {
  id: string;
  title: string;
  pluginName: string | null; // null = built-in
  component: unknown; // null = sandboxed plugin (iframe, handled in N-36)
}

const availableViews = computed<ViewOption[]>(() => {
  const pluginViews = listPluginViews();
  const options: ViewOption[] = [
    { id: "editor", title: t("layout.editorView"), pluginName: null, component: null },
  ];
  for (const v of pluginViews) {
    options.push({
      id: v.id,
      title: v.title,
      pluginName: v.pluginName,
      component: v.component,
    });
  }
  return options;
});

function handleClick() {
  emit("activate", props.leaf.id);
}

// Proposal H2: User clicked a view option in the welcome panel.
function openView(view: ViewOption) {
  replaceLeafView(props.leaf.id, view.id);
}

// N-35: Resolve the plugin view component for the current viewId.
// Returns null if the view is sandboxed (no component) — the iframe
// wrapper (N-36) handles that case. N-57: reactive via listPluginViews.
const pluginView = computed<RegisteredView | null>(() => {
  if (viewId.value === "editor" || viewId.value === null) return null;
  const all = listPluginViews();
  return all.find((v) => v.id === viewId.value) ?? null;
});

// N-36: Resolve the manifest for the plugin that owns the current view.
// Needed by PluginViewIframe for permission checks.
const pluginManifest = computed<PluginManifest | null>(() => {
  if (!pluginView.value) return null;
  const info = getPluginInfo(pluginView.value.pluginName);
  return info?.manifest ?? null;
});

// Shared RPC handler instance for plugin view iframes. Created once
// and reused across all iframe views — same as the Worker sandbox.
let sharedRpcHandler: ReturnType<typeof createSandboxRpcHandler> | null = null;
function getRpcHandler() {
  if (!sharedRpcHandler) {
    sharedRpcHandler = createSandboxRpcHandler();
  }
  return sharedRpcHandler;
}
</script>

<template>
  <div
    class="layout-leaf"
    :class="{ 'layout-leaf--active': active }"
    @mousedown="handleClick"
    @dragenter="handleClick"
  >
    <!-- Built-in editor view. null viewId also shows editor (default
         leaf state) so existing layouts keep working without surprise. -->
    <EditorView
      v-if="viewId === 'editor' || viewId === null"
      :group-id="leaf.id"
      :active="active"
    />

    <!-- N-35: Plugin-registered view with a component (non-sandboxed) -->
    <component
      v-else-if="pluginView && pluginView.component"
      :is="pluginView.component"
    />

    <!-- N-36: Sandboxed plugin view (no component) — iframe wrapper.
         PluginViewIframe handles the postMessage RPC protocol (Proposal G)
         and permission gating. -->
    <PluginViewIframe
      v-else-if="pluginView && !pluginView.component && pluginManifest"
      :plugin-name="pluginView.pluginName"
      :view-id="pluginView.id"
      :title="pluginView.title"
      :manifest="pluginManifest"
      :rpc-handler="getRpcHandler()"
    />

    <!-- Proposal H2: View selector shown when viewId is a non-null
         unknown value (e.g. plugin was unloaded but layout still
         references its viewId). Lets the user pick a replacement. -->
    <div v-else class="layout-leaf__welcome">
      <p class="layout-leaf__unknown">{{ t('layout.unknownView', { viewId }) }}</p>
      <h3>{{ t('layout.welcomeTitle') }}</h3>
      <p class="layout-leaf__welcome-hint">{{ t('layout.welcomeHint') }}</p>
      <ul class="layout-leaf__view-list">
        <li
          v-for="view in availableViews"
          :key="view.id"
          class="layout-leaf__view-item"
          role="button"
          tabindex="0"
          :aria-label="t('layout.openViewAria', { title: view.title })"
          @click="openView(view)"
          @keydown.enter="openView(view)"
          @keydown.space.prevent="openView(view)"
        >
          <span class="layout-leaf__view-title">{{ view.title }}</span>
          <span v-if="view.pluginName" class="layout-leaf__view-plugin">
            {{ t('layout.viewByPlugin', { name: view.pluginName }) }}
          </span>
        </li>
      </ul>
    </div>
  </div>
</template>

<style scoped>
.layout-leaf {
  display: flex;
  flex-direction: column;
  flex: 1;
  min-width: 0;
  min-height: 0;
  overflow: hidden;
  position: relative;
}

.layout-leaf--active {
  /* Subtle focus indicator — a thin accent border on the left. */
  box-shadow: inset 2px 0 0 var(--color-primary, #4285f4);
}

.layout-leaf__unknown {
  display: flex;
  align-items: center;
  justify-content: center;
  height: 100%;
  color: var(--color-text-tertiary, #888);
  font-size: 13px;
  user-select: none;
}

/* N-35: Plugin view iframe container */
.layout-leaf__iframe {
  flex: 1;
  display: flex;
  min-height: 0;
}

.layout-leaf__iframe-el {
  flex: 1;
  border: none;
  width: 100%;
  height: 100%;
}

/* Proposal H2: Welcome / view selector */
.layout-leaf__welcome {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  height: 100%;
  padding: 24px;
  color: var(--color-text-primary, #333);
  user-select: none;
  overflow: auto;
}

.layout-leaf__welcome h3 {
  margin: 0 0 8px 0;
  font-size: 16px;
  font-weight: 600;
}

.layout-leaf__welcome-hint {
  margin: 0 0 20px 0;
  color: var(--color-text-secondary, #666);
  font-size: 13px;
}

.layout-leaf__view-list {
  list-style: none;
  padding: 0;
  margin: 0;
  display: flex;
  flex-direction: column;
  gap: 4px;
  max-width: 360px;
  width: 100%;
}

.layout-leaf__view-item {
  display: flex;
  flex-direction: column;
  padding: 10px 14px;
  border: 1px solid var(--color-border, #e0e0e0);
  border-radius: 6px;
  cursor: pointer;
  transition: background 0.15s, border-color 0.15s;
}

.layout-leaf__view-item:hover {
  background: var(--color-hover-bg, rgba(66, 133, 244, 0.08));
  border-color: var(--color-primary, #4285f4);
}

.layout-leaf__view-title {
  font-size: 13px;
  font-weight: 500;
}

.layout-leaf__view-plugin {
  font-size: 11px;
  color: var(--color-text-tertiary, #888);
  margin-top: 2px;
}
</style>
