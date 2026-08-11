/**
 * Priority 8 — extension manifest store / API wrapper tests.
 *
 * 注入 mock 后端适配器，验证：
 *   - loadDiscoveredExtensions: 列表加载、排序保持、selected 失效清除
 *   - parseExtensionManifest: 解析成功/失败路径
 *   - resolveActivationEvents: 成功/失败路径
 *   - 纯前端辅助函数 manifestId / manifestContributionCount
 *   - reset 注入与状态清理
 */
import { describe, it, expect, beforeEach, afterEach } from "vitest";
import {
  extensionManifestStore,
  discoveredManifests,
  selectedManifest,
  isLoadingManifests,
  manifestLoadError,
  scannedExtensionDir,
  setExtensionManifestBackend,
  loadDiscoveredExtensions,
  parseExtensionManifest,
  resolveActivationEvents,
  selectManifest,
  resetExtensionManifestStore,
  manifestId,
  manifestContributionCount,
  type ExtensionManifestBackend,
} from "@/stores/extensionManifests";
import type { ExtensionManifest } from "@/types";

// --- fixtures ---

function makeManifest(
  publisher: string,
  name: string,
  extra: Partial<ExtensionManifest> = {},
): ExtensionManifest {
  return {
    name,
    publisher,
    version: "1.0.0",
    displayName: `${publisher}.${name}`,
    description: "",
    engines: { vscode: "^1.80.0" },
    activationEvents: [],
    contributes: {},
    ...extra,
  };
}

const alphaManifest = makeManifest("acme", "alpha", {
  activationEvents: ["onStartupFinished"],
  contributes: {
    languages: [{ id: "golang", extensions: [".go"] }],
    commands: [{ command: "acme.alpha.run", title: "Run" }],
  },
});

const betaManifest = makeManifest("contoso", "beta", {
  contributes: {
    commands: [{ command: "contoso.beta.run", title: "Run" }],
  },
});

// --- mock backend ---

function makeMockBackend(
  overrides: Partial<ExtensionManifestBackend> = {},
): ExtensionManifestBackend & { calls: Record<string, unknown[]> } {
  const calls: Record<string, unknown[]> = {
    parseManifest: [],
    discoverExtensions: [],
    getActivationEvents: [],
  };
  const defaults: ExtensionManifestBackend = {
    async parseManifest(packageJSON) {
      calls.parseManifest.push([packageJSON]);
      return makeManifest("acme", "parsed", {});
    },
    async discoverExtensions(extensionDir) {
      calls.discoverExtensions.push([extensionDir]);
      // 后端已排序：acme.alpha < contoso.beta
      return [alphaManifest, betaManifest];
    },
    async getActivationEvents(manifest) {
      calls.getActivationEvents.push([manifest]);
      return manifest.activationEvents ?? [];
    },
  };
  return Object.assign(defaults, overrides, { calls });
}

describe("extensionManifests store — loadDiscoveredExtensions", () => {
  let backend: ReturnType<typeof makeMockBackend>;

  beforeEach(() => {
    resetExtensionManifestStore();
    backend = makeMockBackend();
    setExtensionManifestBackend(backend);
  });

  afterEach(() => {
    resetExtensionManifestStore();
  });

  it("loads manifests into the store and records the scanned dir", async () => {
    await loadDiscoveredExtensions("/fake/extensions");
    expect(discoveredManifests.value).toHaveLength(2);
    expect(discoveredManifests.value[0].publisher).toBe("acme");
    expect(discoveredManifests.value[1].publisher).toBe("contoso");
    expect(scannedExtensionDir.value).toBe("/fake/extensions");
    expect(backend.calls.discoverExtensions).toHaveLength(1);
    expect(backend.calls.discoverExtensions[0]).toEqual(["/fake/extensions"]);
  });

  it("toggles loading flag during load", async () => {
    expect(isLoadingManifests.value).toBe(false);
    const p = loadDiscoveredExtensions("/fake/extensions");
    expect(isLoadingManifests.value).toBe(true);
    await p;
    expect(isLoadingManifests.value).toBe(false);
  });

  it("clears selected when the selected manifest is no longer present", async () => {
    // 预置一个不在新列表中的选中项。
    selectManifest(makeManifest("ghost", "missing"));
    expect(selectedManifest.value?.publisher).toBe("ghost");
    await loadDiscoveredExtensions("/fake/extensions");
    expect(selectedManifest.value).toBeNull();
  });

  it("keeps selected when it is still present after reload", async () => {
    await loadDiscoveredExtensions("/fake/extensions");
    selectManifest(alphaManifest);
    await loadDiscoveredExtensions("/fake/extensions");
    expect(selectedManifest.value?.publisher).toBe("acme");
    expect(selectedManifest.value?.name).toBe("alpha");
  });

  it("stores error and keeps manifests unchanged on backend failure", async () => {
    extensionManifestStore.manifests = [alphaManifest];
    setExtensionManifestBackend(
      makeMockBackend({
        async discoverExtensions() {
          throw new Error("disk on fire");
        },
      }),
    );
    await loadDiscoveredExtensions("/fake/extensions");
    expect(manifestLoadError.value).toBe("disk on fire");
    expect(discoveredManifests.value).toHaveLength(1);
    expect(discoveredManifests.value[0].publisher).toBe("acme");
  });
});

describe("extensionManifests store — parseExtensionManifest", () => {
  beforeEach(() => {
    resetExtensionManifestStore();
  });
  afterEach(() => {
    resetExtensionManifestStore();
  });

  it("returns the parsed manifest from the backend", async () => {
    const parsed = makeManifest("acme", "fromjson");
    setExtensionManifestBackend(
      makeMockBackend({
        async parseManifest(packageJSON) {
          expect(packageJSON).toBe('{"name":"x"}');
          return parsed;
        },
      }),
    );
    const result = await parseExtensionManifest('{"name":"x"}');
    expect(result).not.toBeNull();
    expect(result?.publisher).toBe("acme");
    expect(result?.name).toBe("fromjson");
  });

  it("returns null and stores error on backend failure", async () => {
    setExtensionManifestBackend(
      makeMockBackend({
        async parseManifest() {
          throw new Error("bad json");
        },
      }),
    );
    const result = await parseExtensionManifest("{ broken");
    expect(result).toBeNull();
    expect(manifestLoadError.value).toBe("bad json");
  });
});

describe("extensionManifests store — resolveActivationEvents", () => {
  beforeEach(() => {
    resetExtensionManifestStore();
  });
  afterEach(() => {
    resetExtensionManifestStore();
  });

  it("returns resolved events from the backend", async () => {
    setExtensionManifestBackend(
      makeMockBackend({
        async getActivationEvents(manifest) {
          expect(manifest.name).toBe("alpha");
          return ["onStartupFinished", "onLanguage:golang", "onCommand:acme.alpha.run"];
        },
      }),
    );
    const events = await resolveActivationEvents(alphaManifest);
    expect(events).toEqual([
      "onStartupFinished",
      "onLanguage:golang",
      "onCommand:acme.alpha.run",
    ]);
  });

  it("returns empty array and stores error on backend failure", async () => {
    setExtensionManifestBackend(
      makeMockBackend({
        async getActivationEvents() {
          throw new Error("boom");
        },
      }),
    );
    const events = await resolveActivationEvents(alphaManifest);
    expect(events).toEqual([]);
    expect(manifestLoadError.value).toBe("boom");
  });
});

describe("extensionManifests store — selection + reset", () => {
  beforeEach(() => {
    resetExtensionManifestStore();
  });
  afterEach(() => {
    resetExtensionManifestStore();
  });

  it("selectManifest sets and clears the selected manifest", () => {
    selectManifest(betaManifest);
    // Vue reactive 将 selected 包装为 proxy，故用深度相等而非引用相等。
    expect(selectedManifest.value).toStrictEqual(betaManifest);
    selectManifest(null);
    expect(selectedManifest.value).toBeNull();
  });

  it("resetExtensionManifestStore clears all state and backend injection", async () => {
    setExtensionManifestBackend(makeMockBackend());
    extensionManifestStore.manifests = [alphaManifest];
    extensionManifestStore.selected = alphaManifest;
    extensionManifestStore.scannedDir = "/x";
    extensionManifestStore.error = "err";

    resetExtensionManifestStore();
    expect(discoveredManifests.value).toEqual([]);
    expect(selectedManifest.value).toBeNull();
    expect(scannedExtensionDir.value).toBeNull();
    expect(manifestLoadError.value).toBeNull();
    expect(isLoadingManifests.value).toBe(false);
  });
});

describe("extensionManifests store — pure helpers", () => {
  it("manifestId returns publisher.name", () => {
    expect(manifestId(alphaManifest)).toBe("acme.alpha");
    expect(manifestId(makeManifest("p", "n"))).toBe("p.n");
  });

  it("manifestContributionCount sums all contribution arrays", () => {
    expect(manifestContributionCount(alphaManifest)).toBe(2); // 1 language + 1 command
    expect(manifestContributionCount(betaManifest)).toBe(1); // 1 command
    expect(manifestContributionCount(makeManifest("a", "b"))).toBe(0);
  });

  it("manifestContributionCount handles missing contributes fields", () => {
    const m = makeManifest("a", "b", { contributes: undefined as unknown as ExtensionManifest["contributes"] });
    expect(manifestContributionCount(m)).toBe(0);
  });
});
