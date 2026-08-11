import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("@/stores/app", () => ({
  appState: { aiApiKey: "sk-secret-key-12345" },
}));

import {
  assertNotExtensionContext,
  classifyExtensionContext,
  getAiApiKeyForContext,
  isExtensionContext,
  type ExtensionContextSignals,
} from "@/stores/extensionSecurity";

const trustedSignals: ExtensionContextSignals = {
  worker: false,
  childFrame: false,
  marker: false,
  hasWindow: true,
  origin: "wails://localhost",
  nonce: "a".repeat(32),
};

describe("extension context classification", () => {
  it("trusts the main Wails webview only with an allowed origin and nonce", () => {
    expect(classifyExtensionContext(trustedSignals)).toBe(false);
  });

  it.each([
    ["worker", { worker: true }],
    ["sandboxed child frame", { childFrame: true }],
    ["explicit marker", { marker: true }],
    ["untrusted origin", { origin: "https://evil.attacker.test" }],
    ["missing origin", { origin: null }],
    ["missing nonce", { nonce: null }],
    ["short nonce", { nonce: "short" }],
  ])("rejects %s", (_name, overrides) => {
    expect(classifyExtensionContext({ ...trustedSignals, ...overrides })).toBe(true);
  });

  it.each([
    "wails://localhost",
    "http://wails.localhost",
    "https://wails.localhost",
    "http://localhost:5173",
    "http://127.0.0.1:34115",
  ])("accepts supported main-webview origin %s", (origin) => {
    expect(classifyExtensionContext({ ...trustedSignals, origin })).toBe(false);
  });

  it("does not classify a non-browser non-worker context as an extension", () => {
    expect(classifyExtensionContext({
      ...trustedSignals,
      hasWindow: false,
      origin: null,
      nonce: null,
    })).toBe(false);
  });
});

describe("extension resource isolation", () => {
  afterEach(() => {
    delete (globalThis as { __KOYORI_IDE_EXTENSION_CONTEXT__?: boolean })
      .__KOYORI_IDE_EXTENSION_CONTEXT__;
  });

  it("honors the explicit extension marker in the runtime wrapper", () => {
    (globalThis as { __KOYORI_IDE_EXTENSION_CONTEXT__?: boolean })
      .__KOYORI_IDE_EXTENSION_CONTEXT__ = true;
    expect(isExtensionContext()).toBe(true);
  });

  it("does not expose the AI key in an extension context", () => {
    (globalThis as { __KOYORI_IDE_EXTENSION_CONTEXT__?: boolean })
      .__KOYORI_IDE_EXTENSION_CONTEXT__ = true;
    expect(getAiApiKeyForContext()).toBe("");
  });

  it("blocks protected operations in an extension context", () => {
    (globalThis as { __KOYORI_IDE_EXTENSION_CONTEXT__?: boolean })
      .__KOYORI_IDE_EXTENSION_CONTEXT__ = true;
    expect(() => assertNotExtensionContext("read-ai-key")).toThrow(
      /blocked in extension contexts/,
    );
  });
});
