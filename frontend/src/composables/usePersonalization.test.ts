import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { PersonalizationConfig } from "@/types";

interface Deferred<T> {
  promise: Promise<T>;
  resolve(value: T): void;
}

function deferred<T>(): Deferred<T> {
  let release: (value: T) => void = () => undefined;
  const promise = new Promise<T>((resolve) => {
    release = resolve;
  });
  return { promise, resolve: release };
}

const mocks = vi.hoisted(() => ({
  appState: { personalization: {} as PersonalizationConfig },
  readAsset: vi.fn(),
  watch: vi.fn(),
  watchStops: [] as Array<ReturnType<typeof vi.fn>>,
  createObjectURL: vi.fn(() => "blob:test"),
  revokeObjectURL: vi.fn(),
}));

vi.mock("vue", () => ({
  watch: mocks.watch,
}));

vi.mock("@/stores/app", () => ({
  appState: mocks.appState,
}));

vi.mock("@/api/services", () => ({
  settingsService: {
    readPersonalizationAsset: mocks.readAsset,
  },
}));

import {
  applyPersonalization,
  initPersonalization,
  invalidatePersonalizationAsset,
  teardownPersonalization,
} from "./usePersonalization";

async function flushAsyncStyles(): Promise<void> {
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
}

describe("personalization runtime lifecycle", () => {
  beforeEach(() => {
    teardownPersonalization();
    vi.clearAllMocks();
    mocks.watchStops.length = 0;
    mocks.appState.personalization = {};
    mocks.watch.mockImplementation(() => {
      const stop = vi.fn();
      mocks.watchStops.push(stop);
      return stop;
    });
    mocks.createObjectURL.mockReturnValue("blob:test");
    vi.stubGlobal("URL", {
      createObjectURL: mocks.createObjectURL,
      revokeObjectURL: mocks.revokeObjectURL,
    });
    document.documentElement.removeAttribute("style");
    document.documentElement.removeAttribute("data-editor-bg");
    document.documentElement.removeAttribute("data-chat-bg");
  });

  afterEach(() => {
    teardownPersonalization();
    vi.unstubAllGlobals();
  });

  it("keeps the newest image when older asset reads resolve later", async () => {
    const first = deferred<Uint8Array>();
    const second = deferred<Uint8Array>();
    mocks.readAsset.mockImplementation((path: string) =>
      path === "first.png" ? first.promise : second.promise,
    );

    mocks.appState.personalization = { codeEditorBgImage: "first.png" };
    applyPersonalization();
    mocks.appState.personalization = { codeEditorBgImage: "second.png" };
    applyPersonalization();

    second.resolve(new Uint8Array([2]));
    await flushAsyncStyles();
    expect(document.documentElement.style.getPropertyValue("--personalization-editor-bg"))
      .toBe('url("blob:test")');

    first.resolve(new Uint8Array([1]));
    await flushAsyncStyles();

    expect(mocks.createObjectURL).toHaveBeenCalledTimes(1);
    expect(document.documentElement.style.getPropertyValue("--personalization-editor-bg"))
      .toBe('url("blob:test")');
  });

  it("deduplicates concurrent reads of the same asset path", async () => {
    const pending = deferred<Uint8Array>();
    mocks.readAsset.mockReturnValue(pending.promise);
    mocks.appState.personalization = {
      userAvatar: "shared.png",
      aiAvatar: "shared.png",
    };

    applyPersonalization();
    expect(mocks.readAsset).toHaveBeenCalledTimes(1);
    pending.resolve(new Uint8Array([1]));
    await flushAsyncStyles();

    expect(mocks.createObjectURL).toHaveBeenCalledTimes(1);
    expect(document.documentElement.style.getPropertyValue("--personalization-user-avatar"))
      .toBe("blob:test");
    expect(document.documentElement.style.getPropertyValue("--personalization-ai-avatar"))
      .toBe("blob:test");
  });

  it("stops the previous watcher on reinitialization and teardown", () => {
    initPersonalization();
    const firstStop = mocks.watchStops[0];
    initPersonalization();
    const secondStop = mocks.watchStops[1];

    expect(firstStop).toHaveBeenCalledOnce();
    expect(secondStop).not.toHaveBeenCalled();

    teardownPersonalization();
    expect(secondStop).toHaveBeenCalledOnce();
  });

  it("reloads an asset after the backend overwrites the same relative path", async () => {
    mocks.appState.personalization = { userAvatar: "same.png" };
    mocks.readAsset.mockResolvedValue(new Uint8Array([1]));
    mocks.createObjectURL
      .mockReturnValueOnce("blob:first")
      .mockReturnValueOnce("blob:second");

    applyPersonalization();
    await flushAsyncStyles();
    expect(document.documentElement.style.getPropertyValue("--personalization-user-avatar"))
      .toBe("blob:first");

    invalidatePersonalizationAsset("same.png");
    applyPersonalization();
    await flushAsyncStyles();

    expect(mocks.readAsset).toHaveBeenCalledTimes(2);
    expect(mocks.revokeObjectURL).toHaveBeenCalledWith("blob:first");
    expect(document.documentElement.style.getPropertyValue("--personalization-user-avatar"))
      .toBe("blob:second");
  });
});
