import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  activeKey: undefined as string | undefined,
  themes: [] as Array<{
    key: string;
    extensionId: string;
    label: string;
    path: string;
    uiTheme?: string;
  }>,
  defineTheme: vi.fn(),
  setTheme: vi.fn(),
  readExtensionFile: vi.fn(),
  onSetActiveTheme: undefined as (() => void) | undefined,
  setActiveTheme: vi.fn((key: string | undefined) => {
    mocks.onSetActiveTheme?.();
    if (key !== undefined && !mocks.themes.some((theme) => theme.key === key)) {
      throw new Error(`VS Code extension theme "${key}" is not registered`);
    }
    mocks.activeKey = key;
  }),
}));

vi.mock("monaco-editor", () => ({
  editor: { defineTheme: mocks.defineTheme, setTheme: mocks.setTheme },
}));

vi.mock("@/api/services", () => ({
  marketplaceService: { readExtensionFile: mocks.readExtensionFile },
}));

vi.mock("@/lib/vscodeExtensions", () => ({
  getActiveVscodeExtensionTheme: () => mocks.themes.find((theme) => theme.key === mocks.activeKey),
  listVscodeExtensionThemes: () => mocks.themes,
  setActiveVscodeExtensionTheme: mocks.setActiveTheme,
}));

import {
  applyVscodeExtensionTheme,
  getMonacoThemeNameForMode,
  registerAllThemes,
} from "./monaco-themes";

describe("Monaco theme registry", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.activeKey = undefined;
    mocks.themes = [];
    mocks.onSetActiveTheme = undefined;
  });

  it("registers six bracket colors and semantic token rules in both modes", () => {
    registerAllThemes();
    const darkTheme = mocks.defineTheme.mock.calls.find(([name]) => name === "koyoriIde-blue")?.[1];
    const lightTheme = mocks.defineTheme.mock.calls.find(([name]) => name === "koyoriIde-light-blue")?.[1];

    for (const theme of [darkTheme, lightTheme]) {
      expect(theme).toBeDefined();
      for (let index = 1; index <= 6; index += 1) {
        expect(theme.colors[`editorBracketHighlight.foreground${index}`]).toBeTruthy();
      }
      expect(theme.rules).toEqual(expect.arrayContaining([
        expect.objectContaining({ token: "function.declaration" }),
        expect.objectContaining({ token: "variable.readonly" }),
        expect.objectContaining({ token: "interface" }),
      ]));
    }
  });

  it("uses Monaco high-contrast themes for system contrast preferences", () => {
    expect(getMonacoThemeNameForMode("blue", "dark", true)).toBe("hc-black");
    expect(getMonacoThemeNameForMode("blue", "light", true)).toBe("hc-light");
  });

  it("preserves an active extension theme across light and dark mode changes", () => {
    const theme = {
      key: "publisher.theme:themes/active.json",
      extensionId: "publisher.theme",
      label: "Active",
      path: "themes/active.json",
    };
    mocks.themes = [theme];
    mocks.activeKey = theme.key;

    expect(getMonacoThemeNameForMode("blue", "light")).toBe(`koyoriIde-extension:${theme.key}`);
    expect(getMonacoThemeNameForMode("blue", "dark")).toBe(`koyoriIde-extension:${theme.key}`);
  });

  it("loads a real JSONC contribution from its normalized installed path", async () => {
    const theme = {
      key: "catppuccin.catppuccin-vsc:./themes/mocha.json",
      extensionId: "catppuccin.catppuccin-vsc",
      label: "Catppuccin Mocha",
      path: "./themes/mocha.json",
      uiTheme: "vs-dark",
    };
    mocks.themes = [theme];
    mocks.readExtensionFile.mockResolvedValue(new TextEncoder().encode(`{
      // Catppuccin themes are JSONC files.
      "type": "dark",
      "colors": { "editor.background": "#1e1e2e", },
      "tokenColors": [
        { "scope": ["keyword", "storage.type"], "settings": { "foreground": "#cba6f7", "fontStyle": "bold" } },
      ],
    }`));

    await expect(applyVscodeExtensionTheme(theme.key)).resolves.toEqual(theme);

    expect(mocks.readExtensionFile).toHaveBeenCalledWith(
      "catppuccin",
      "catppuccin-vsc",
      "extension/themes/mocha.json",
    );
    expect(mocks.defineTheme).toHaveBeenCalledWith(
      `koyoriIde-extension:${theme.key}`,
      expect.objectContaining({
        base: "vs-dark",
        colors: { "editor.background": "#1e1e2e" },
        rules: expect.arrayContaining([
          { token: "keyword", foreground: "cba6f7", fontStyle: "bold" },
          { token: "storage.type", foreground: "cba6f7", fontStyle: "bold" },
        ]),
      }),
    );
    expect(mocks.setTheme).toHaveBeenCalledWith(`koyoriIde-extension:${theme.key}`);
    expect(mocks.activeKey).toBe(theme.key);
  });

  it.each(["", "/themes/mocha.json", "C:\\themes\\mocha.json", "themes\\..\\mocha.json"])(
    "rejects unsafe contribution path %j before reading the extension",
    async (path) => {
      mocks.themes = [{
        key: `publisher.theme:${path}`,
        extensionId: "publisher.theme",
        label: "Unsafe",
        path,
      }];

      await expect(applyVscodeExtensionTheme(mocks.themes[0].key)).rejects.toThrow();
      expect(mocks.readExtensionFile).not.toHaveBeenCalled();
      expect(mocks.setActiveTheme).not.toHaveBeenCalled();
    },
  );

  it("preserves the prior active theme when Monaco rejects the new definition", async () => {
    const previous = {
      key: "publisher.theme:themes/previous.json",
      extensionId: "publisher.theme",
      label: "Previous",
      path: "themes/previous.json",
    };
    const next = { ...previous, key: "publisher.theme:themes/next.json", label: "Next", path: "themes/next.json" };
    mocks.themes = [previous, next];
    mocks.activeKey = previous.key;
    mocks.readExtensionFile.mockResolvedValue(new TextEncoder().encode(
      `{ "colors": { "editor.background": "#181825" } }`,
    ));
    mocks.defineTheme.mockImplementationOnce(() => {
      throw new Error("Monaco rejected theme");
    });

    await expect(applyVscodeExtensionTheme(next.key)).rejects.toThrow("Monaco rejected theme");

    expect(mocks.activeKey).toBe(previous.key);
    expect(mocks.setActiveTheme).not.toHaveBeenCalled();
    expect(mocks.setTheme).not.toHaveBeenCalled();
  });

  it("does not apply a theme that is unregistered while its file is loading", async () => {
    const theme = {
      key: "publisher.theme:themes/pending.json",
      extensionId: "publisher.theme",
      label: "Pending",
      path: "themes/pending.json",
    };
    mocks.themes = [theme];
    let resolveRead!: (value: Uint8Array) => void;
    mocks.readExtensionFile.mockReturnValue(new Promise<Uint8Array>((resolve) => {
      resolveRead = resolve;
    }));

    const applying = applyVscodeExtensionTheme(theme.key);
    mocks.themes = [];
    resolveRead(new TextEncoder().encode(
      `{ "colors": { "editor.background": "#181825" } }`,
    ));

    await expect(applying).rejects.toThrow("changed while loading");
    expect(mocks.defineTheme).not.toHaveBeenCalled();
    expect(mocks.setTheme).not.toHaveBeenCalled();
    expect(mocks.setActiveTheme).not.toHaveBeenCalled();
  });

  it("rejects unsupported include inheritance without changing the active theme", async () => {
    const theme = {
      key: "publisher.theme:themes/inherited.json",
      extensionId: "publisher.theme",
      label: "Inherited",
      path: "themes/inherited.json",
    };
    mocks.themes = [theme];
    mocks.readExtensionFile.mockResolvedValue(new TextEncoder().encode(
      `{ "include": "./base.json", "colors": { "editor.background": "#181825" } }`,
    ));

    await expect(applyVscodeExtensionTheme(theme.key)).rejects.toThrow(
      "Extension theme include inheritance is not supported",
    );
    expect(mocks.defineTheme).not.toHaveBeenCalled();
    expect(mocks.setActiveTheme).not.toHaveBeenCalled();
  });
  it("restores the built-in theme when unregistration wins the active commit", async () => {
    const theme = {
      key: "publisher.theme:themes/commit.json",
      extensionId: "publisher.theme",
      label: "Commit",
      path: "themes/commit.json",
    };
    mocks.themes = [theme];
    mocks.readExtensionFile.mockResolvedValue(new TextEncoder().encode(
      `{ "colors": { "editor.background": "#181825" } }`,
    ));
    mocks.onSetActiveTheme = () => {
      mocks.themes = [];
    };

    await expect(applyVscodeExtensionTheme(theme.key)).rejects.toThrow("not registered");
    expect(mocks.activeKey).toBeUndefined();
    expect(mocks.setTheme).toHaveBeenLastCalledWith("koyoriIde-blue");
  });
});
