import { shallowMount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";

const { appState, saveSettings } = vi.hoisted(() => ({
  appState: {
    emmetEnabled: true,
    emmetIncludeLanguages: {} as Record<string, string>,
  },
  saveSettings: vi.fn(),
}));

vi.mock("@/stores/app", async () => {
  const { reactive } = await import("vue");
  return { appState: reactive(appState), saveSettings };
});

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({ t: (key: string) => key }),
}));

import EditorSection from "./EditorSection.vue";

function mountSection() {
  return shallowMount(EditorSection, {
    global: {
      stubs: {
        "el-input": {
          props: ["modelValue"],
          emits: ["update:modelValue", "change"],
          template: `<textarea
            :value="modelValue"
            :aria-label="$attrs['aria-label']"
            :aria-invalid="$attrs['aria-invalid']"
            @input="$emit('update:modelValue', $event.target.value)"
            @change="$emit('change')"
          />`,
        },
        "el-switch": {
          props: ["modelValue"],
          emits: ["update:modelValue", "change"],
          template: `<input
            type="checkbox"
            :checked="modelValue"
            :aria-label="$attrs['aria-label']"
            @change="($event) => {
              $emit('update:modelValue', $event.target.checked);
              $emit('change');
            }"
          />`,
        },
      },
    },
  });
}

describe("EditorSection Emmet settings", () => {
  beforeEach(() => {
    appState.emmetEnabled = true;
    appState.emmetIncludeLanguages = {};
    saveSettings.mockClear();
  });

  it("updates and persists the Emmet enable switch", async () => {
    const wrapper = mountSection();
    const toggle = wrapper.find('[aria-label="editorSection.emmetAria"]');

    await toggle.setValue(false);

    expect(appState.emmetEnabled).toBe(false);
    expect(saveSettings).toHaveBeenCalledTimes(1);
  });

  it("updates and persists a valid includeLanguages JSON mapping", async () => {
    const wrapper = mountSection();
    const input = wrapper.find('[aria-label="editorSection.emmetIncludeLanguagesAria"]');

    await input.setValue('{"templ":"html","postcss":"css"}');

    expect(appState.emmetIncludeLanguages).toEqual({ templ: "html", postcss: "css" });
    expect(saveSettings).toHaveBeenCalledTimes(1);
    expect(input.attributes("aria-invalid")).toBe("false");
  });

  it("keeps the last valid mapping when the JSON is invalid", async () => {
    appState.emmetIncludeLanguages = { templ: "html" };
    const wrapper = mountSection();
    const input = wrapper.find('[aria-label="editorSection.emmetIncludeLanguagesAria"]');

    await input.setValue("{");

    expect(appState.emmetIncludeLanguages).toEqual({ templ: "html" });
    expect(saveSettings).not.toHaveBeenCalled();
    expect(input.attributes("aria-invalid")).toBe("true");
  });
});
