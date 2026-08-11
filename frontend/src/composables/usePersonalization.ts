// Plan 11 Task 15 Step 4-8 — personalization runtime applier.
//
// Watches appState.personalization and projects the config onto CSS
// custom properties on :root. Image fields hold relative asset paths
// (e.g. "assets/abc.png") stored by the backend; we fetch the bytes via
// settingsService.readPersonalizationAsset and materialize them as blob
// URLs so they can be used in CSS background-image.
//
// CSS variables emitted (consumed in main.css):
//   --personalization-editor-bg, --personalization-editor-bg-opacity,
//   --personalization-editor-bg-blur,
//   --personalization-chat-bg, --personalization-chat-bg-opacity,
//   --personalization-chat-bg-blur,
//   --personalization-user-avatar, --personalization-ai-avatar,
//   --personalization-font-family, --personalization-font-size,
//   --personalization-bubble-style, --personalization-bubble-opacity,
//   --personalization-message-spacing
// Koyori IDE 模块 · Use Personalization；交互服务：设置（SettingsService）。
// 喵，这是 Koyori IDE 的 Use Personalization 模块（前端实现）~
import { watch, type WatchStopHandle } from "vue";
import { appState } from "@/stores/app";
import { settingsService } from "@/api/services";
import type { PersonalizationConfig } from "@/types";

// Track active blob URLs so we can revoke them when replaced, avoiding
// memory leaks across repeated image uploads.
const blobUrls = new Map<string, string>();
const pendingAssetUrls = new Map<
  string,
  { generation: number; promise: Promise<string> }
>();
let personalizationWatcher: WatchStopHandle | null = null;
let personalizationGeneration = 0;

/**
 * Resolve a relative asset path to a blob URL. Returns "" when relPath is
 * empty or the read fails (best-effort — a missing image must never break
 * the UI). Cached so repeated reads of the same path are free.
 */
async function resolveAssetUrl(relPath: string, generation: number): Promise<string> {
  if (!relPath) return "";
  const cached = blobUrls.get(relPath);
  if (cached) return cached;
  const pending = pendingAssetUrls.get(relPath);
  if (pending?.generation === generation) return pending.promise;

  const promise = (async () => {
    try {
      const bytes = await settingsService.readPersonalizationAsset(relPath);
      if (generation !== personalizationGeneration) return "";
      // Copy into a fresh ArrayBuffer-backed view so BlobPart accepts it under
      // strict DOM lib typings (Uint8Array<ArrayBufferLike> is rejected).
      const copy = new Uint8Array(bytes.byteLength);
      copy.set(bytes);
      const blob = new Blob([copy], { type: "image/*" });
      const url = URL.createObjectURL(blob);
      blobUrls.set(relPath, url);
      return url;
    } catch {
      return "";
    }
  })();
  const entry = { generation, promise };
  pendingAssetUrls.set(relPath, entry);
  try {
    return await promise;
  } finally {
    if (pendingAssetUrls.get(relPath) === entry) {
      pendingAssetUrls.delete(relPath);
    }
  }
}

/** Revoke a previously cached blob URL for a path (if any). */
function revokeAsset(relPath: string): void {
  const url = blobUrls.get(relPath);
  if (url) {
    URL.revokeObjectURL(url);
    blobUrls.delete(relPath);
  }
}

/** Drop a cached asset before a backend overwrite reuses the same path. */
export function invalidatePersonalizationAsset(relPath: string): void {
  if (!relPath) return;
  pendingAssetUrls.delete(relPath);
  revokeAsset(relPath);
}

function setVar(name: string, value: string): void {
  if (value) {
    document.documentElement.style.setProperty(name, value);
  } else {
    document.documentElement.style.removeProperty(name);
  }
}

/**
 * Apply the non-image fields synchronously (opacity/blur/font/bubble).
 * Image fields are resolved asynchronously and applied when ready.
 */
export function applyPersonalization(): void {
  const generation = ++personalizationGeneration;
  const p: PersonalizationConfig = appState.personalization;
  const activeAssetPaths = new Set([
    p.userAvatar,
    p.aiAvatar,
    p.codeEditorBgImage,
    p.chatBgImage,
  ].filter((path): path is string => Boolean(path)));
  for (const relPath of blobUrls.keys()) {
    if (!activeAssetPaths.has(relPath)) revokeAsset(relPath);
  }
  setVar("--personalization-editor-bg-opacity", String(p.codeEditorBgOpacity ?? 0));
  setVar("--personalization-editor-bg-blur", `${p.codeEditorBgBlur ?? 0}px`);
  setVar("--personalization-chat-bg-opacity", String(p.chatBgOpacity ?? 0));
  setVar("--personalization-chat-bg-blur", `${p.chatBgBlur ?? 0}px`);
  setVar("--personalization-font-family", p.fontFamily ?? "");
  setVar("--personalization-font-size", p.fontSize ? `${p.fontSize}px` : "");
  setVar("--personalization-bubble-style", p.bubbleStyle ?? "rounded");
  setVar("--personalization-bubble-opacity", String(p.bubbleOpacity ?? 1));
  setVar("--personalization-message-spacing", `${p.messageSpacing ?? 12}px`);

  // Avatars are applied directly as URL strings (consumed by <img>:src
  // bindings in components, not just CSS), so resolve them too.
  void resolveAssetUrl(p.userAvatar ?? "", generation).then((url) => {
    if (generation === personalizationGeneration) {
      setVar("--personalization-user-avatar", url);
    }
  });
  void resolveAssetUrl(p.aiAvatar ?? "", generation).then((url) => {
    if (generation === personalizationGeneration) {
      setVar("--personalization-ai-avatar", url);
    }
  });

  // Background images: revoke previous, resolve new.
  if (p.codeEditorBgImage) {
    document.documentElement.setAttribute("data-editor-bg", "");
    void resolveAssetUrl(p.codeEditorBgImage, generation).then((url) => {
      if (generation === personalizationGeneration) {
        setVar("--personalization-editor-bg", url ? `url("${url}")` : "");
      }
    });
  } else {
    document.documentElement.removeAttribute("data-editor-bg");
    setVar("--personalization-editor-bg", "");
  }
  if (p.chatBgImage) {
    document.documentElement.setAttribute("data-chat-bg", "");
    void resolveAssetUrl(p.chatBgImage, generation).then((url) => {
      if (generation === personalizationGeneration) {
        setVar("--personalization-chat-bg", url ? `url("${url}")` : "");
      }
    });
  } else {
    document.documentElement.removeAttribute("data-chat-bg");
    setVar("--personalization-chat-bg", "");
  }
}

/**
 * Initialize personalization: apply once and watch for changes. Call once
 * at app startup (after loadSettings hydrates appState.personalization).
 */
export function initPersonalization(): void {
  personalizationWatcher?.();
  applyPersonalization();
  personalizationWatcher = watch(
    () => ({ ...appState.personalization }),
    () => applyPersonalization(),
    { deep: true },
  );
}

/** Revoke all cached blob URLs. Call on teardown / hot-reload. */
export function teardownPersonalization(): void {
  personalizationGeneration += 1;
  personalizationWatcher?.();
  personalizationWatcher = null;
  pendingAssetUrls.clear();
  for (const url of blobUrls.values()) {
    URL.revokeObjectURL(url);
  }
  blobUrls.clear();
}

export { revokeAsset };
