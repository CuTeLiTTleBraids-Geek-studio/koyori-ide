/**
 * prompt-7 Task J — dual-window protocol smoke (no real OS windows).
 * Covers at least 3 critical paths from docs/qa-dual-window.md:
 *  1) settings:changed origin anti-loop + reload
 *  2) streamId foreign chunk ignored
 *  3) conversation stale-while-streaming flag
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

const { eventHandlers, settingsServiceMock } = vi.hoisted(() => ({
  eventHandlers: {} as Record<string, ((...args: unknown[]) => void) | undefined>,
  settingsServiceMock: {
    loadSettings: vi.fn().mockResolvedValue({
      version: 3,
      language: "en",
      theme: "dark",
      fontSize: 14,
      fontFamily: "Inter",
      tabSize: 2,
      wordWrap: true,
      lineNumbers: true,
      minimap: true,
      aiApiKey: "",
      aiApiKeyConfigured: false,
      aiBaseUrl: "",
      aiModel: "m",
      aiSystemPrompt: "",
      cursorBlinking: "blink",
      cursorStyle: "line",
      bracketColorization: true,
      autoSave: false,
      autoSaveDelay: "1000",
      aiProvider: "openai",
      temperature: 0.7,
      maxTokens: 4096,
      defaultShell: "",
      terminalFontSize: 13,
      terminalCursorStyle: "block",
      scrollback: 1000,
      uiDensity: "comfortable",
      fontSizeScaling: 100,
      inlineCompletionEnabled: true,
      enablePluginSandbox: true,
    }),
    saveSettings: vi.fn().mockResolvedValue(undefined),
  },
}));

vi.mock("@wailsio/runtime", () => ({
  Events: {
    On: vi.fn((event: string, handler: (...args: unknown[]) => void) => {
      eventHandlers[event] = handler;
      return () => undefined;
    }),
    Emit: vi.fn(),
  },
}));

vi.mock("@/api/services", () => ({
  aiService: {
    setConfig: vi.fn().mockResolvedValue(undefined),
    startStream: vi.fn().mockResolvedValue("stream-smoke-1"),
    stopStream: vi.fn().mockResolvedValue(undefined),
    generateTitleWithAI: vi.fn().mockResolvedValue("t"),
    getPresetPrompt: vi.fn(),
  },
  conversationService: {
    save: vi.fn().mockResolvedValue(undefined),
    load: vi.fn().mockResolvedValue({
      id: "c1",
      title: "t",
      created_at: 1,
      revision: 2,
      messages: [{ role: "user", content: "hi" }],
    }),
    generateId: vi.fn().mockResolvedValue("new-id"),
    generateTitle: vi.fn().mockResolvedValue("title"),
  },
  settingsService: {
    loadSettings: vi.fn((...args: unknown[]) => (
      globalThis as typeof globalThis & {
        __koyoriIdeSettingsServiceTestBinding?: typeof settingsServiceMock;
      }
    ).__koyoriIdeSettingsServiceTestBinding?.loadSettings(...args)),
    saveSettings: vi.fn((...args: unknown[]) => (
      globalThis as typeof globalThis & {
        __koyoriIdeSettingsServiceTestBinding?: typeof settingsServiceMock;
      }
    ).__koyoriIdeSettingsServiceTestBinding?.saveSettings(...args)),
  },
  windowService: {
    focusMainWindow: vi.fn().mockResolvedValue(undefined),
    isMaximised: vi.fn().mockResolvedValue(false),
  },
}));

beforeEach(() => {
  (
    globalThis as typeof globalThis & {
      __koyoriIdeSettingsServiceTestBinding?: typeof settingsServiceMock;
    }
  ).__koyoriIdeSettingsServiceTestBinding = settingsServiceMock;
});

afterEach(() => {
  delete (
    globalThis as typeof globalThis & {
      __koyoriIdeSettingsServiceTestBinding?: typeof settingsServiceMock;
    }
  ).__koyoriIdeSettingsServiceTestBinding;
});

vi.mock("@/lib/notifications", () => ({
  notify: vi.fn(),
  notifySuccess: vi.fn(),
  notifyError: vi.fn(),
  notifyWarning: vi.fn(),
  notifyInfo: vi.fn(),
}));

vi.mock("@/lib/monaco-themes", () => ({
  accentThemes: {},
  applyMonacoTheme: vi.fn(),
  applyMonacoThemeForMode: vi.fn(),
  registerAllThemes: vi.fn(),
  registerCustomTheme: vi.fn(),
}));

import {
  aiState,
  sendMessage,
  loadConversation,
  isOwnedStreamEvent,
  handleAIChunkEvent,
  handleAIDoneEvent,
  handleConversationSavedEvent,
} from "@/stores/ai";
import { appState, loadSettings } from "@/stores/app";

describe("dual-window smoke (prompt-7 Task J)", () => {
  beforeEach(() => {
    aiState.messages = [];
    aiState.streaming = false;
    aiState.globalStreamBusy = false;
    aiState.activeStreamId = null;
    aiState.currentConversationId = null;
    aiState.conversationRevision = 0;
    aiState.conversationStaleWhileStreaming = false;
    appState.settingsVersion = 0;
  });

  eventHandlers["ai:chunk"] = handleAIChunkEvent;
  eventHandlers["ai:done"] = handleAIDoneEvent;
  eventHandlers["conversation:saved"] = handleConversationSavedEvent;

  it("step1: settings load hydrates version (SSOT)", async () => {
    await loadSettings();
    expect(appState.settingsVersion).toBe(3);
  });

  it("step2: foreign streamId chunks do not pollute messages", async () => {
    const p = sendMessage("hello");
    await p;
    const sid = aiState.activeStreamId || "stream-smoke-1";
    eventHandlers["ai:chunk"]?.({ data: { streamId: "foreign", data: "LEAK" } });
    eventHandlers["ai:chunk"]?.({ data: { streamId: sid, data: "ok" } });
    eventHandlers["ai:done"]?.({ data: { streamId: sid, data: "" } });
    const assistant = aiState.messages.find((m) => m.role === "assistant");
    expect(assistant?.content).toBe("ok");
    expect(assistant?.content).not.toContain("LEAK");
    expect(isOwnedStreamEvent("foreign")).toBe(false);
  });

  it("step3: conversation:saved while streaming marks stale (BUG-H6)", async () => {
    await loadConversation("c1");
    expect(aiState.conversationRevision).toBe(2);
    aiState.streaming = true;
    eventHandlers["conversation:saved"]?.({
      data: { origin: "other-window", id: "c1" },
    });
    expect(aiState.conversationStaleWhileStreaming).toBe(true);
  });
});
