import { shallowMount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  appState: {
    aiProviderConfigs: [
      {
        id: "provider-1",
        name: "Provider",
        provider: "openai",
        protocol: "openai",
        apiKey: "",
        apiKeyConfigured: true,
        baseUrl: "https://example.com",
        model: "model",
        temperature: 0.7,
        maxTokens: 4096,
        systemPrompt: "",
      },
    ],
    activeAIConfigId: "provider-1",
  },
  saveAIConfig: vi.fn(),
  saveSettings: vi.fn(),
  getAPIKeyStorageMethod: vi.fn(),
}));

vi.mock("@/stores/app", async () => {
  const { reactive } = await import("vue");
  return {
    appState: reactive(mocks.appState),
    activateAIConfig: vi.fn(),
    saveAIConfig: mocks.saveAIConfig,
    deleteAIConfig: vi.fn(),
    createNewAIConfig: vi.fn(() => mocks.appState.aiProviderConfigs[0]),
    saveSettings: mocks.saveSettings,
  };
});

vi.mock("@/api/services", () => ({
  aiService: {
    getSystemPrompt: vi.fn(),
    setConfig: vi.fn(),
    send: vi.fn(),
    listModels: vi.fn().mockResolvedValue(["gpt-4o", "gpt-4o-mini"]),
  },
  settingsService: {
    getAPIKeyStorageMethod: mocks.getAPIKeyStorageMethod,
  },
}));

vi.mock("@/lib/notifications", () => ({
  notifySuccess: vi.fn(),
  notifyError: vi.fn(),
}));

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({ t: (key: string) => key }),
}));

import AiSection from "./AiSection.vue";

function mountSection() {
  return shallowMount(AiSection, {
    global: {
      stubs: {
        "el-button": {
          emits: ["click"],
          template: '<button type="button" @click="$emit(\'click\')"><slot /></button>',
        },
      },
    },
  });
}

async function clickButton(wrapper: ReturnType<typeof mountSection>, text: string) {
  const button = wrapper.findAll("button").find((candidate) => candidate.text() === text);
  expect(button).toBeDefined();
  await button!.trigger("click");
}

describe("AiSection encryption refresh", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    mocks.saveAIConfig.mockClear();
    mocks.saveSettings.mockClear();
    mocks.getAPIKeyStorageMethod.mockReset().mockResolvedValue("dpapi");
  });

  it("cancels the delayed storage-method refresh when unmounted", async () => {
    const wrapper = mountSection();
    await Promise.resolve();

    expect(mocks.getAPIKeyStorageMethod).toHaveBeenCalledTimes(1);

    await clickButton(wrapper, "aiSection.edit");
    await clickButton(wrapper, "aiSection.save");
    expect(mocks.saveSettings).toHaveBeenCalledTimes(1);

    wrapper.unmount();
    await vi.advanceTimersByTimeAsync(800);

    expect(mocks.getAPIKeyStorageMethod).toHaveBeenCalledTimes(1);
    vi.useRealTimers();
  });
});
