<script setup lang="ts">
// Koyori IDE 组件 · Plugin View Iframe。
// 喵，这是 Plugin View Iframe，负责 Koyori IDE 的界面呈现喵~
// N-36 (prompt-5.md): Plugin view iframe wrapper.
//
// Sandboxed plugins register views with `koyoriIde.views.register(id, null, options)`
// because Vue components can't cross the Worker boundary. This component
// renders the plugin's `view.html` in an iframe and establishes a
// bidirectional postMessage channel for the plugin to call host APIs.
//
// Proposal G (prompt-5.md): iframe communication protocol.
//
// The iframe receives an `koyoriIde` proxy via `window.parent.postMessage`.
// The host validates permissions using the same METHOD_PERMISSIONS map
// as the Worker sandbox (N-26), so the security model is consistent.
//
// Message protocol (host ↔ iframe):
//   Host → Iframe:
//     { type: "koyoriIde:init", manifest, viewId }
//     { type: "koyoriIde:rpc-response", id, result?, error? }
//     { type: "koyoriIde:event", event, data }
//   Iframe → Host:
//     { type: "koyoriIde:ready" }
//     { type: "koyoriIde:rpc-request", id, method, args }
//     { type: "koyoriIde:log", level, message }
//
// Security:
//   - iframe has `sandbox="allow-scripts"` (no allow-same-origin, no forms,
//     no top navigation, no popup, no pointer lock). Without allow-same-origin
//     the iframe gets an opaque origin and cannot remove its own sandbox or
//     reach parent.window.go bindings directly.
//   - Source + origin check on every message: the source must match the
//     iframe's contentWindow and the sandboxed iframe must have opaque origin.
//   - Permission check on every RPC: METHOD_PERMISSIONS gate.
//   - With only `allow-scripts`, the iframe cannot access the parent's DOM,
//     localStorage, cookies, or parent.window bindings — only via the
//     postMessage RPC bridge.
import { ref, onMounted, onUnmounted, computed } from "vue";
import type { PluginManifest } from "@/types";
import { errorMessage } from "@/lib/errors";
import { hasPermissionForMethod, type RpcHandler, type RpcMethod } from "@/lib/pluginSandbox";

const props = defineProps<{
  pluginName: string;
  viewId: string;
  title: string;
  manifest: PluginManifest;
  rpcHandler: RpcHandler;
}>();

// Build the iframe src. The plugin's view.html is served by the
// backend's /_plugins/<name>/ asset handler.
// BUG3b: projectRoot MUST be included in the URL so the backend's
// servePluginAsset can resolve project-scoped plugins (the query param is
// the only signal backend has about which project is active). This is safe
// because the iframe is sandboxed with `allow-scripts` only (no
// allow-same-origin), so it gets an opaque origin and cannot read its own
// URL via `window.location` — the URL is only visible to the host page,
// which already knows the project path. projectRoot is ALSO sent via
// postMessage on koyoriIde:init for plugins that need it at runtime.
const iframeSrc = computed(() => {
  const root = (window as unknown as { __NKNK_PROJECT_ROOT__?: string }).__NKNK_PROJECT_ROOT__ ?? "";
  const rootParam = root ? `&projectRoot=${encodeURIComponent(root)}` : "";
  return `/_plugins/${encodeURIComponent(props.pluginName)}/view.html?viewId=${encodeURIComponent(props.viewId)}${rootParam}`;
});

const iframeEl = ref<HTMLIFrameElement | null>(null);
const isReady = ref(false);
const lastError = ref<string | null>(null);

// L-7: Timeout for pending RPC calls to the iframe. If the iframe doesn't
// respond within 30 seconds, the promise is rejected with a timeout error
// and the call is removed from pendingCalls.
const PENDING_CALL_TIMEOUT_MS = 30_000;

// Pending RPC calls waiting for the iframe to respond.
interface PendingCall {
  resolve: (value: unknown) => void;
  reject: (error: Error) => void;
  timer: ReturnType<typeof setTimeout>;
}
const pendingCalls = new Map<number, PendingCall>();
let nextCallId = 1;

// A sandbox without allow-same-origin always emits the opaque "null" origin.
// Neither signal is sufficient alone, so require both the exact WindowProxy
// source and the expected opaque origin.
function isAllowedOrigin(event: MessageEvent): boolean {
  return event.source === iframeEl.value?.contentWindow && event.origin === "null";
}

async function handleRpcRequest(id: number, method: RpcMethod, args: unknown[]): Promise<void> {
  try {
    // Permission check — same as Worker sandbox (N-26).
    if (!hasPermissionForMethod(props.manifest, method)) {
      const perm = method.startsWith("workspace.read") ? "fs.read" :
        method.startsWith("workspace.write") ? "fs.write" : "unknown";
      throw new Error(
        `Permission denied: plugin "${props.pluginName}" did not declare permission "${perm}" required for method "${method}"`,
      );
    }
    const result = await props.rpcHandler(props.pluginName, props.manifest, method, args);
    sendToIframe({ type: "koyoriIde:rpc-response", id, result });
  } catch (e: unknown) {
    sendToIframe({ type: "koyoriIde:rpc-response", id, error: errorMessage(e) });
  }
}

function sendToIframe(msg: unknown): void {
  const el = iframeEl.value;
  if (!el?.contentWindow) return;
  // Opaque sandbox origins cannot be named as targetOrigin. The receiving
  // iframe is isolated by sandbox and the host validates the WindowProxy on
  // every response, so "*" is required for this one-to-one channel.
  el.contentWindow.postMessage(msg, "*");
}

function onMessage(event: MessageEvent): void {
  if (!isAllowedOrigin(event)) return;
  const data = event.data;
  if (!data || typeof data !== "object") return;
  const msg = data as { type?: string };

  switch (msg.type) {
    case "koyoriIde:ready":
      // Iframe is ready — send init with manifest, viewId, and projectRoot.
      // L-8: projectRoot is sent via postMessage (not as a URL query param)
      // to avoid leaking the project path in the iframe src.
      isReady.value = true;
      sendToIframe({
        type: "koyoriIde:init",
        manifest: props.manifest,
        viewId: props.viewId,
        projectRoot: (window as unknown as { __NKNK_PROJECT_ROOT__?: string }).__NKNK_PROJECT_ROOT__ ?? "",
      });
      break;

    case "koyoriIde:rpc-request": {
      const req = data as { id: number; method: RpcMethod; args: unknown[] };
      void handleRpcRequest(req.id, req.method, req.args ?? []);
      break;
    }

    case "koyoriIde:rpc-response": {
      // This case is for responses to host→iframe calls (if we ever
      // need them). Currently the host only responds to iframe requests.
      const resp = data as { id: number; result?: unknown; error?: string };
      const pending = pendingCalls.get(resp.id);
      if (pending) {
        clearTimeout(pending.timer);
        pendingCalls.delete(resp.id);
        if (resp.error) {
          pending.reject(new Error(resp.error));
        } else {
          pending.resolve(resp.result);
        }
      }
      break;
    }

    case "koyoriIde:log": {
      const log = data as { level: "info" | "warn" | "error"; message: string };
      console[log.level](`[plugin view: ${props.pluginName}/${props.viewId}]`, log.message);
      break;
    }
  }
}

onMounted(() => {
  window.addEventListener("message", onMessage);
});

onUnmounted(() => {
  window.removeEventListener("message", onMessage);
  // Reject any pending calls so callers don't hang. Clear timers too.
  for (const [, pending] of pendingCalls) {
    clearTimeout(pending.timer);
    pending.reject(new Error("Plugin view iframe unmounted"));
  }
  pendingCalls.clear();
});

// Public API: call a method on the iframe (host → iframe direction).
// Currently unused but available for future host-initiated actions
// like "view:refresh" or "view:setState".
// L-7: each call has a 30-second timeout. If the iframe doesn't respond
// within 30s, the promise is rejected with a timeout error.
function callIframeMethod(method: string, args: unknown[]): Promise<unknown> {
  const id = nextCallId++;
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      const pending = pendingCalls.get(id);
      if (pending) {
        pendingCalls.delete(id);
        pending.reject(new Error(`Plugin view iframe call "${method}" timed out after 30s`));
      }
    }, PENDING_CALL_TIMEOUT_MS);
    pendingCalls.set(id, { resolve, reject, timer });
    sendToIframe({ type: "koyoriIde:rpc-call", id, method, args });
  });
}

defineExpose({ callIframeMethod, isReady, lastError });
</script>

<template>
  <div class="plugin-view-iframe">
    <div v-if="lastError" class="plugin-view-iframe__error">
      {{ lastError }}
    </div>
    <iframe
      ref="iframeEl"
      :src="iframeSrc"
      :title="title"
      sandbox="allow-scripts"
      class="plugin-view-iframe__el"
    />
  </div>
</template>

<style scoped>
.plugin-view-iframe {
  display: flex;
  flex-direction: column;
  flex: 1;
  min-height: 0;
  overflow: hidden;
}

.plugin-view-iframe__el {
  flex: 1;
  border: none;
  width: 100%;
  height: 100%;
  background: var(--color-bg, #fff);
}

.plugin-view-iframe__error {
  padding: 8px 12px;
  background: var(--color-error-bg, rgba(244, 67, 54, 0.1));
  color: var(--color-error, #ff6b6b);
  font-size: 12px;
  font-family: var(--font-mono, monospace);
}
</style>
