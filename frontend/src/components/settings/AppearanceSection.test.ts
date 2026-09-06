import { flushPromises, shallowMount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  activeKey: undefined as string | undefined,
  themes: [] as Array<{
    key: string;
    extensionId: string;
    label: string;
    path: string;
  }>,
  applyExtensionTheme: vi.fn(),
  messageError: vi.fn(),
  saveSettings: vi.fn(),
}));

vi.mock("element-plus", () => ({
  ElMessage: { error: mocks.messageError },
  ElMessageBox: { alert: vi.fn() },
}));

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({ t: (key: string) => key }),
}));

vi.mock("@/lib/monaco-themes", () => ({
  accentThemes: {
    blue: {
      label: "Blue",
      color: "#4285f4",
      monacoTheme: "koyoriIde-blue",
      monacoLightTheme: "koyoriIde-light-blue",
    },
  },
  applyVscodeExtensionTheme: mocks.applyExtensionTheme,
}));

vi.mock("@/lib/vscodeExtensions", () => ({
  getActiveVscodeExtensionTheme: () => mocks.themes.find((theme) => theme.key === mocks.activeKey),
  listVscodeExtensionThemes: () => mocks.themes,
}));

vi.mock("@/stores/app", () => ({
  appState: {
    accentTheme: "blue",
    customAccent: null,
    designLanguage: "apple",
    theme: "dark",
    fontSizeScaling: 100,
    uiDensity: "comfortable",
  },
  saveSettings: mocks.saveSettings,
  applyAccentTheme: vi.fn(),
  applyMode: vi.fn(),
  applyDesignLanguage: vi.fn(),
  applyFontSizeScaling: vi.fn(),
  applyUiDensity: vi.fn(),
  setCustomAccent: vi.fn(),
  serializeCustomAccent: vi.fn(),
  deserializeCustomAccent: vi.fn(),
}));

import AppearanceSection from "./AppearanceSection.vue";

function mountSection() {
  return shallowMount(AppearanceSection, {
    global: {
      stubs: {
        "el-select": {
          props: ["modelValue", "disabled", "loading"],
          emits: ["change"],
          template: `<select
            :value="modelValue"
            :disabled="disabled"
            :aria-label="$attrs['aria-label']"
            @change="$emit('change', $event.target.value)"
          ><slot /></select>`,
        },
        "el-option": {
          props: ["label", "value"],
          template: `<option :value="value">{{ label }}</option>`,
        },
        "el-input": true,
        "el-color-picker": true,
        "el-button": true,
        "el-slider": true,
      },
    },
  });
}

describe("AppearanceSection installed editor themes", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.activeKey = undefined;
    mocks.themes = [];
    mocks.applyExtensionTheme.mockResolvedValue(undefined);
  });

  it("only renders registered themes and identifies their extension", () => {
    const emptyWrapper = mountSection();
    expect(emptyWrapper.find('[aria-label="appearance.installedEditorThemeAria"]').exists()).toBe(false);
    emptyWrapper.unmount();

    mocks.themes = [{
      key: "catppuccin.catppuccin-vsc:./themes/mocha.json",
      extensionId: "catppuccin.catppuccin-vsc",
      label: "Catppuccin Mocha",
      path: "./themes/mocha.json",
    }];
    const wrapper = mountSection();

    expect(wrapper.text()).toContain("Catppuccin Mocha — catppuccin.catppuccin-vsc");
    wrapper.unmount();
  });

  it("applies a selected registered theme asynchronously", async () => {
    const theme = {
      key: "catppuccin.catppuccin-vsc:./themes/mocha.json",
      extensionId: "catppuccin.catppuccin-vsc",
      label: "Catppuccin Mocha",
      path: "./themes/mocha.json",
    };
    mocks.themes = [theme];
    const wrapper = mountSection();

    await wrapper.get('[aria-label="appearance.installedEditorThemeAria"]').setValue(theme.key);
    await flushPromises();

    expect(mocks.applyExtensionTheme).toHaveBeenCalledWith(theme.key);
    expect(mocks.messageError).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  it("reports the exact load error without replacing the prior active key", async () => {
    const previous = {
      key: "publisher.theme:themes/previous.json",
      extensionId: "publisher.theme",
      label: "Previous",
      path: "themes/previous.json",
    };
    const next = {
      ...previous,
      key: "publisher.theme:themes/next.json",
      label: "Next",
      path: "themes/next.json",
    };
    mocks.themes = [previous, next];
    mocks.activeKey = previous.key;
    mocks.applyExtensionTheme.mockRejectedValue(new Error("Theme JSONC is invalid at line 4"));
    const wrapper = mountSection();

    await wrapper.get('[aria-label="appearance.installedEditorThemeAria"]').setValue(next.key);
    await flushPromises();

    expect(mocks.activeKey).toBe(previous.key);
    expect(mocks.messageError).toHaveBeenCalledWith("Theme JSONC is invalid at line 4");
    wrapper.unmount();
  });
});
