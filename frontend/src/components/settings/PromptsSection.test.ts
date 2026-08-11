import { describe, it, expect, beforeEach, vi } from "vitest";
import { mount } from "@vue/test-utils";
import type { App } from "vue";
import ElementPlus from "element-plus";

// Use vi.hoisted so the mock factories can reference these variables —
// vi.mock calls are hoisted to the top of the file, before any const
// declarations, so plain top-level consts would be in the temporal dead zone.
const {
  mockAppState,
  saveSettingsMock,
  getDefaultSystemPromptMock,
  getAgentSystemPromptMock,
  getConversationTitlePromptMock,
  getInlineCompletionSystemPromptMock,
} = vi.hoisted(() => ({
  mockAppState: {
    aiSystemPrompt: "",
    aiAgentSystemPrompt: "",
    aiConversationTitlePrompt: "",
    aiInlineCompletionPrompt: "",
  },
  saveSettingsMock: vi.fn(),
  getDefaultSystemPromptMock: vi.fn().mockResolvedValue("BUILTIN DEFAULT"),
  getAgentSystemPromptMock: vi.fn().mockResolvedValue("BUILTIN AGENT"),
  getConversationTitlePromptMock: vi.fn().mockResolvedValue("BUILTIN TITLE {{first_message}}"),
  getInlineCompletionSystemPromptMock: vi.fn().mockResolvedValue("BUILTIN INLINE {{language}}"),
}));

vi.mock("@/stores/app", () => ({
  appState: mockAppState,
  saveSettings: saveSettingsMock,
}));

vi.mock("@/api/services", () => ({
  aiService: {
    getDefaultSystemPrompt: getDefaultSystemPromptMock,
    getAgentSystemPrompt: getAgentSystemPromptMock,
    getConversationTitlePrompt: getConversationTitlePromptMock,
    getInlineCompletionSystemPrompt: getInlineCompletionSystemPromptMock,
  },
}));

vi.mock("@/lib/notifications", () => ({
  notifySuccess: vi.fn(),
  notifyError: vi.fn(),
}));

vi.mock("@/lib/errors", () => ({
  errorMessage: (e: unknown) => (e instanceof Error ? e.message : String(e)),
}));

const iconPlugin = {
  install(_app: App) {
    // ElementPlus icons not needed for this test; no-op.
  },
};

// Import the component AFTER the mocks are set up.
const PromptsSectionModule = await import("./PromptsSection.vue");
const PromptsSection = PromptsSectionModule.default;

function mountSection() {
  return mount(PromptsSection, {
    global: {
      plugins: [ElementPlus, iconPlugin],
    },
  });
}

describe("PromptsSection (Plan 54)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockAppState.aiSystemPrompt = "";
    mockAppState.aiAgentSystemPrompt = "";
    mockAppState.aiConversationTitlePrompt = "";
    mockAppState.aiInlineCompletionPrompt = "";
  });

  it("renders a block for each of the 4 prompts", () => {
    const wrapper = mountSection();
    const blocks = wrapper.findAll(".prompt-block");
    expect(blocks).toHaveLength(4);
    expect(wrapper.text()).toContain("Default System Prompt");
    expect(wrapper.text()).toContain("Agent System Prompt");
    expect(wrapper.text()).toContain("Conversation Title Prompt");
    expect(wrapper.text()).toContain("Inline Completion Prompt");
  });

  it("shows the built-in const name for each prompt", () => {
    const wrapper = mountSection();
    const text = wrapper.text();
    expect(text).toContain("DefaultSystemPrompt");
    expect(text).toContain("AgentSystemPrompt");
    expect(text).toContain("ConversationTitlePrompt");
    expect(text).toContain("InlineCompletionSystemPrompt");
  });

  it("Load Builtin for default prompt fetches and stores the built-in", async () => {
    const wrapper = mountSection();
    const buttons = wrapper.findAll("button");
    const loadDefaultBtn = buttons.find((b) => b.text().includes("Load Builtin"));
    expect(loadDefaultBtn).toBeTruthy();
    await loadDefaultBtn!.trigger("click");
    // Wait for the async loadBuiltin to complete.
    await new Promise((r) => setTimeout(r, 10));
    expect(getDefaultSystemPromptMock).toHaveBeenCalledTimes(1);
    expect(mockAppState.aiSystemPrompt).toBe("BUILTIN DEFAULT");
    expect(saveSettingsMock).toHaveBeenCalled();
  });

  it("Load Builtin for agent prompt fetches and stores the built-in", async () => {
    const wrapper = mountSection();
    const agentBlock = wrapper.findAll(".prompt-block")[1];
    const loadBtn = agentBlock.find("button");
    expect(loadBtn.text()).toContain("Load Builtin");
    await loadBtn.trigger("click");
    await new Promise((r) => setTimeout(r, 10));
    expect(getAgentSystemPromptMock).toHaveBeenCalledTimes(1);
    expect(mockAppState.aiAgentSystemPrompt).toBe("BUILTIN AGENT");
  });

  it("Load Builtin for title prompt fetches and stores the built-in", async () => {
    const wrapper = mountSection();
    const titleBlock = wrapper.findAll(".prompt-block")[2];
    const loadBtn = titleBlock.find("button");
    await loadBtn.trigger("click");
    await new Promise((r) => setTimeout(r, 10));
    expect(getConversationTitlePromptMock).toHaveBeenCalledTimes(1);
    expect(mockAppState.aiConversationTitlePrompt).toBe("BUILTIN TITLE {{first_message}}");
  });

  it("Load Builtin for inline prompt fetches and stores the built-in", async () => {
    const wrapper = mountSection();
    const inlineBlock = wrapper.findAll(".prompt-block")[3];
    const loadBtn = inlineBlock.find("button");
    await loadBtn.trigger("click");
    await new Promise((r) => setTimeout(r, 10));
    expect(getInlineCompletionSystemPromptMock).toHaveBeenCalledTimes(1);
    expect(mockAppState.aiInlineCompletionPrompt).toBe("BUILTIN INLINE {{language}}");
  });

  it("Clear button empties the field and saves", async () => {
    mockAppState.aiSystemPrompt = "some custom text";
    const wrapper = mountSection();
    const defaultBlock = wrapper.findAll(".prompt-block")[0];
    // The 2nd button in the block is "Clear".
    const clearBtn = defaultBlock.findAll("button")[1];
    expect(clearBtn.text()).toContain("Clear");
    await clearBtn.trigger("click");
    expect(mockAppState.aiSystemPrompt).toBe("");
    expect(saveSettingsMock).toHaveBeenCalled();
  });

  it("Clear button is disabled when the field is already empty", () => {
    const wrapper = mountSection();
    const defaultBlock = wrapper.findAll(".prompt-block")[0];
    const clearBtn = defaultBlock.findAll("button")[1];
    expect(clearBtn.attributes("disabled")).toBeDefined();
  });

  it("shows placeholder validation when title prompt is missing {{first_message}}", () => {
    mockAppState.aiConversationTitlePrompt = "custom title prompt without placeholder";
    const wrapper = mountSection();
    const titleBlock = wrapper.findAll(".prompt-block")[2];
    const warn = titleBlock.find(".prompt-block__warn");
    expect(warn.exists()).toBe(true);
    expect(warn.text()).toContain("missing");
  });

  it("shows success validation when title prompt has {{first_message}}", () => {
    mockAppState.aiConversationTitlePrompt = "Title for: {{first_message}}";
    const wrapper = mountSection();
    const titleBlock = wrapper.findAll(".prompt-block")[2];
    const ok = titleBlock.find(".prompt-block__ok");
    expect(ok.exists()).toBe(true);
    expect(ok.text()).toContain("contains");
  });

  it("shows warning validation when inline prompt is missing {{language}}", () => {
    mockAppState.aiInlineCompletionPrompt = "complete the code";
    const wrapper = mountSection();
    const inlineBlock = wrapper.findAll(".prompt-block")[3];
    const warn = inlineBlock.find(".prompt-block__warn");
    expect(warn.exists()).toBe(true);
  });

  it("shows success validation when inline prompt has {{language}}", () => {
    mockAppState.aiInlineCompletionPrompt = "Complete this {{language}} code";
    const wrapper = mountSection();
    const inlineBlock = wrapper.findAll(".prompt-block")[3];
    const ok = inlineBlock.find(".prompt-block__ok");
    expect(ok.exists()).toBe(true);
  });

  it("does not show validation for default/agent prompts (no placeholders)", () => {
    mockAppState.aiSystemPrompt = "some default text";
    mockAppState.aiAgentSystemPrompt = "some agent text";
    const wrapper = mountSection();
    const defaultBlock = wrapper.findAll(".prompt-block")[0];
    const agentBlock = wrapper.findAll(".prompt-block")[1];
    expect(defaultBlock.find(".prompt-block__validate").exists()).toBe(false);
    expect(agentBlock.find(".prompt-block__validate").exists()).toBe(false);
  });

  it("does not show validation when fields are empty", () => {
    const wrapper = mountSection();
    const titleBlock = wrapper.findAll(".prompt-block")[2];
    const inlineBlock = wrapper.findAll(".prompt-block")[3];
    expect(titleBlock.find(".prompt-block__validate").exists()).toBe(false);
    expect(inlineBlock.find(".prompt-block__validate").exists()).toBe(false);
  });
});
