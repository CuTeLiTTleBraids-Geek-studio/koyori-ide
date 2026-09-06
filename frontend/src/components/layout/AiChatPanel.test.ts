import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { mount } from "@vue/test-utils";
import type { App } from "vue";
import ElementPlus from "element-plus";

// ── vi.hoisted：在 vi.mock 工厂中可引用的变量 ──
// vi.mock 调用会被提升到文件顶部（早于任何 const 声明），
// 因此必须用 vi.hoisted 来声明被 mock 工厂引用的变量，避免进入暂时性死区。
const {
  // 各 store 的初始状态（普通对象，在 mock 工厂内用 reactive 包裹）
  appStateInit,
  aiStateInit,
  agentStateInit,
  rulesStateInit,
  connectivityStateInit,
  // @/stores/app
  toggleAiChatMock,
  saveSettingsMock,
  activateAIConfigMock,
  // @/stores/ai
  sendMessageMock,
  clearMessagesMock,
  stopGenerationMock,
  clearContextMock,
  loadConversationMock,
  addMentionedFileMock,
  removeMentionedFileMock,
  renameConversationMock,
  setSystemPromptOverrideMock,
  // @/stores/agent
  toggleModeMock,
  approveAndFeedMock,
  rejectAndFeedMock,
  clearPendingToolCallsMock,
  switchAssistantModeMock,
  // @/stores/editor
  updateContentMock,
  // @/stores/rules
  loadRulesMock,
  saveRulesMock,
  saveRulesConfigMock,
  // @/api/services
  conversationListMock,
  conversationDeleteMock,
  fileServiceReadFileMock,
  aiServiceListModelsMock,
  // @/lib/notifications
  notifyErrorMock,
  notifySuccessMock,
  notifyWarningMock,
} = vi.hoisted(() => ({
  // appState：只提供组件实际读取的字段
  appStateInit: {
    aiChatVisible: false,
    aiChatWidth: 380,
    aiProvider: "openai",
    aiModel: "gpt-4o",
    aiApiKey: "",
    aiBaseUrl: "https://api.openai.com",
    aiProviderConfigs: [{ id: "cfg1", name: "OpenAI", model: "gpt-4o" }],
    activeAIConfigId: "cfg1",
    currentProject: "/proj",
    fontSize: 14,
    fontFamily: "JetBrains Mono",
    accentTheme: "blue",
  },
  // aiState：消息列表、流式状态、错误等
  aiStateInit: {
    messages: [] as Array<{
      role: string;
      content: string;
      toolCalls?: Array<{ id: string; name: string; arguments: string }>;
    }>,
    streaming: false,
    globalStreamBusy: false,
    error: null as string | null,
    context: null as unknown,
    currentConversationId: null as string | null,
    currentConversationTitle: null as string | null,
    mentionedFiles: [] as unknown[],
    currentSystemPromptOverride: null as string | null,
  },
  // agentState：模式与待批准工具调用
  agentStateInit: {
    mode: "chat",
    pendingToolCalls: [] as unknown[],
    toolCallCount: 0,
  },
  // rulesState：规则候选与已加载文件
  rulesStateInit: {
    candidates: [] as unknown[],
    config: null as unknown,
    rulesFiles: [] as unknown[],
  },
  // connectivityState：在线状态（控制发送按钮是否可用）
  connectivityStateInit: {
    online: true,
  },
  // ── mock 函数 ──
  toggleAiChatMock: vi.fn(),
  saveSettingsMock: vi.fn(),
  activateAIConfigMock: vi.fn(),
  sendMessageMock: vi.fn().mockResolvedValue(true),
  clearMessagesMock: vi.fn(),
  stopGenerationMock: vi.fn(),
  clearContextMock: vi.fn(),
  loadConversationMock: vi.fn().mockResolvedValue(undefined),
  addMentionedFileMock: vi.fn(),
  removeMentionedFileMock: vi.fn(),
  renameConversationMock: vi.fn().mockResolvedValue(true),
  setSystemPromptOverrideMock: vi.fn(),
  toggleModeMock: vi.fn(),
  approveAndFeedMock: vi.fn(),
  rejectAndFeedMock: vi.fn(),
  clearPendingToolCallsMock: vi.fn(),
  switchAssistantModeMock: vi.fn(),
  updateContentMock: vi.fn(),
  loadRulesMock: vi.fn().mockResolvedValue(undefined),
  saveRulesMock: vi.fn().mockResolvedValue(true),
  saveRulesConfigMock: vi.fn().mockResolvedValue(true),
  conversationListMock: vi.fn().mockResolvedValue([]),
  conversationDeleteMock: vi.fn().mockResolvedValue(undefined),
  fileServiceReadFileMock: vi.fn().mockResolvedValue("file content"),
  aiServiceListModelsMock: vi.fn().mockResolvedValue([]),
  notifyErrorMock: vi.fn(),
  notifySuccessMock: vi.fn(),
  notifyWarningMock: vi.fn(),
}));

// ── mock @/stores/app ──
vi.mock("@/stores/app", async () => {
  const { reactive } = await import("vue");
  return {
    appState: reactive(appStateInit),
    toggleAiChat: toggleAiChatMock,
    saveSettings: saveSettingsMock,
    activateAIConfig: activateAIConfigMock,
  };
});

// ── mock @/stores/ai ──
vi.mock("@/stores/ai", async () => {
  const { reactive } = await import("vue");
  return {
    aiState: reactive(aiStateInit),
    sendMessage: sendMessageMock,
    clearMessages: clearMessagesMock,
    stopGeneration: stopGenerationMock,
    clearContext: clearContextMock,
    loadConversation: loadConversationMock,
    addMentionedFile: addMentionedFileMock,
    removeMentionedFile: removeMentionedFileMock,
    renameConversation: renameConversationMock,
    setSystemPromptOverride: setSystemPromptOverrideMock,
  };
});

// ── mock @/stores/agent ──
// isAgentMode 用 computed 包装 agentState.mode，便于在测试中切换模式后 UI 响应。
vi.mock("@/stores/agent", async () => {
  const { reactive, computed } = await import("vue");
  const agentState = reactive(agentStateInit);
  return {
    agentState,
    isAgentMode: computed(() => agentState.mode === "agent"),
    toggleMode: toggleModeMock,
    extractToolCallBlocks: (content: string) => ({
      cleanedMessage: content.replace(/```\nread: [^\n]+\n```/g, ""),
      toolCalls: [],
    }),
    approveAndFeed: approveAndFeedMock,
    rejectAndFeed: rejectAndFeedMock,
    clearPendingToolCalls: clearPendingToolCallsMock,
    getRegisteredTools: () => [],
  };
});

vi.mock("@/stores/aiAssistant", () => ({
  openStandalonePage: vi.fn(),
  openAIDesktopWindow: vi.fn(),
  switchMode: switchAssistantModeMock,
}));

// ── mock @/stores/editor ──
vi.mock("@/stores/editor", async () => {
  const { ref } = await import("vue");
  return {
    activeFile: ref(null),
    updateContent: updateContentMock,
  };
});

// ── mock @/stores/rules ──
// rules/hasRules/rulesFileCount 用 computed 包装，匹配真实 store 的 ref 语义。
vi.mock("@/stores/rules", async () => {
  const { reactive, computed } = await import("vue");
  const rulesState = reactive(rulesStateInit);
  return {
    rulesState,
    rules: computed(() => null),
    hasRules: computed(() => false),
    rulesFileCount: computed(() => 0),
    loadRules: loadRulesMock,
    saveRules: saveRulesMock,
    saveRulesConfig: saveRulesConfigMock,
    makeDefaultRulesConfig: () => ({ mode: "first", candidates: [] }),
  };
});

// ── mock @/api/services ──
vi.mock("@/api/services", () => ({
  conversationService: {
    list: conversationListMock,
    delete: conversationDeleteMock,
  },
  fileService: {
    readFile: fileServiceReadFileMock,
  },
  aiService: {
    listModels: aiServiceListModelsMock,
  },
}));

// ── mock @/lib/i18n：t 返回 key 本身 ──
vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({
    t: (key: string) => key,
    locale: { value: "en" },
  }),
}));

// ── mock @/lib/markdown：跳过 DOMPurify 副作用，直接返回原文 ──
vi.mock("@/lib/markdown", () => ({
  renderMarkdownWithApplyButtons: (content: string) => content,
}));

// ── mock @/lib/language ──
vi.mock("@/lib/language", () => ({
  detectLanguage: (_path: string) => "plaintext",
}));

// ── mock @/lib/monaco-themes：避免任何 DOM 副作用 ──
vi.mock("@/lib/monaco-themes", () => ({
  getMonacoThemeName: () => "vs",
}));

// ── mock @/lib/aiProviders ──
vi.mock("@/lib/aiProviders", () => ({
  getSuggestedModels: () => ["gpt-4o"],
  getProviderPreset: () => null,
}));

// ── mock @/lib/notifications ──
vi.mock("@/lib/notifications", () => ({
  notifyError: notifyErrorMock,
  notifySuccess: notifySuccessMock,
  notifyWarning: notifyWarningMock,
}));

// ── mock @/lib/errors ──
vi.mock("@/lib/errors", () => ({
  errorMessage: (e: unknown) => (e instanceof Error ? e.message : String(e)),
}));

// ── mock @/lib/connectivity：reactive 包装以便测试切换 online ──
vi.mock("@/lib/connectivity", async () => {
  const { reactive } = await import("vue");
  return { connectivityState: reactive(connectivityStateInit) };
});

// ── mock @guolao/vue-monaco-editor：避免加载 monaco 编辑器 ──
vi.mock("@guolao/vue-monaco-editor", () => ({
  VueMonacoDiffEditor: {
    name: "VueMonacoDiffEditor",
    render: () => null,
  },
}));

// 空的 icon 插件（参考 NewProjectWizard.test.ts 的处理）
const iconPlugin = {
  install(_app: App) {
    // ElementPlus 图标在测试中无需真实渲染，no-op。
  },
};

// 在所有 mock 设置完成后再动态导入被测组件与 store（拿到响应式引用以便测试中改写状态）
const aiStore: any = await import("@/stores/ai");
const appStore: any = await import("@/stores/app");
const agentStore: any = await import("@/stores/agent");
const connectivityMod: any = await import("@/lib/connectivity");

const aiState = aiStore.aiState;
const appState = appStore.appState;
const agentState = agentStore.agentState;
const connectivityState = connectivityMod.connectivityState;
const { agentTimelineState } = await import("@/stores/agentTimeline");

const AiChatPanelModule = await import("./AiChatPanel.vue");
const AiChatPanel = AiChatPanelModule.default;
const AgentExecutionTimelineModule = await import("@/components/ai-assistant/AgentExecutionTimeline.vue");
const AgentExecutionTimeline = AgentExecutionTimelineModule.default;

const mountedWrappers: Array<ReturnType<typeof mount>> = [];

// 挂载辅助：默认 embedded=true，面板始终可见，无需依赖 aiChatVisible
function mountPanel(options: { embedded?: boolean } = {}) {
  const wrapper = mount(AiChatPanel, {
    props: { embedded: options.embedded ?? true },
    global: {
      plugins: [ElementPlus, iconPlugin],
    },
  });
  mountedWrappers.push(wrapper);
  return wrapper;
}

// 刷新微任务（异步 setup / watch / nextTick 回调）
async function flush() {
  await new Promise((r) => setTimeout(r, 10));
}

// 重置响应式状态，保证各用例相互独立
function resetState() {
  aiState.messages = [];
  aiState.streaming = false;
  aiState.globalStreamBusy = false;
  aiState.error = null;
  aiState.context = null;
  aiState.currentConversationId = null;
  aiState.currentConversationTitle = null;
  aiState.mentionedFiles = [];
  aiState.currentSystemPromptOverride = null;
  appState.aiChatVisible = false;
  agentState.mode = "chat";
  agentState.pendingToolCalls = [];
  agentTimelineState.entries.splice(0);
  connectivityState.online = true;
}

describe("AiChatPanel", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    resetState();
    // 重新设定默认的异步返回值（clearAllMocks 不清除实现，这里仅做保险）
    sendMessageMock.mockResolvedValue(true);
    loadConversationMock.mockResolvedValue(undefined);
    renameConversationMock.mockResolvedValue(true);
    saveRulesMock.mockResolvedValue(true);
    saveRulesConfigMock.mockResolvedValue(true);
    loadRulesMock.mockResolvedValue(undefined);
    conversationListMock.mockResolvedValue([]);
    conversationDeleteMock.mockResolvedValue(undefined);
    fileServiceReadFileMock.mockResolvedValue("file content");
    aiServiceListModelsMock.mockResolvedValue([]);
  });

  afterEach(() => {
    for (const wrapper of mountedWrappers.splice(0)) {
      wrapper.unmount();
    }
    connectivityState.online = true;
  });

  it("embedded 模式下渲染面板标题与输入框", () => {
    const wrapper = mountPanel();
    expect(wrapper.find(".ai-chat-panel").exists()).toBe(true);
    expect(wrapper.find(".ai-chat-panel__title").text()).toBe("aiChat.title");
    expect(wrapper.find(".ai-chat-panel__input").exists()).toBe(true);
  });

  it("每个嵌入式窗口只挂载一个 agent execution timeline", () => {
    aiState.messages = [{ role: "assistant", content: "" }];
    const wrapper = mountPanel();
    expect(wrapper.findAllComponents(AgentExecutionTimeline)).toHaveLength(1);
  });

  it("即使尚无对话消息，嵌入面板也显示 reasoning/tool timeline", async () => {
    agentTimelineState.entries.push({
      id: "reasoning-1",
      kind: "reasoning",
      stage: "reasoning",
      explicit: true,
      detail: "provider summary",
      createdAt: 1,
      updatedAt: 1,
    });
    const wrapper = mountPanel();
    await wrapper.vm.$nextTick();
    expect(wrapper.find(".ai-chat-panel__empty").exists()).toBe(true);
    expect(wrapper.find(".agent-timeline").exists()).toBe(true);
    expect(wrapper.find(".agent-timeline").text()).toContain("provider summary");
  });

  it("follows newly appended agent activity when the message body is at the bottom", async () => {
    aiState.messages = [{ role: "assistant", content: "working" }];
    const wrapper = mountPanel();
    const body = wrapper.find(".ai-chat-panel__body").element as HTMLElement;
    Object.defineProperties(body, {
      clientHeight: { configurable: true, value: 100 },
      scrollHeight: { configurable: true, writable: true, value: 200 },
      scrollTop: { configurable: true, writable: true, value: 100 },
    });
    agentTimelineState.entries.push({
      id: "tool-1",
      kind: "tool",
      stage: "executing",
      status: "executing",
      tool: "read",
      createdAt: 1,
      updatedAt: 1,
    });
    Object.defineProperty(body, "scrollHeight", { configurable: true, writable: true, value: 280 });
    await wrapper.vm.$nextTick();
    await wrapper.vm.$nextTick();
    expect(body.scrollTop).toBe(280);
  });

  it("首个 ai:chunk 到达后当前嵌入窗口立即更新 assistant 内容", async () => {
    aiState.messages = [{ role: "assistant", content: "" }];
    const wrapper = mountPanel();
    await wrapper.vm.$nextTick();
    expect(wrapper.find(".ai-chat-panel__message-content").text()).toBe("");

    aiState.messages[0].content = "首个 chunk 已显示";
    await wrapper.vm.$nextTick();

    expect(wrapper.find(".ai-chat-panel__message-content").text()).toContain("首个 chunk 已显示");
  });

  it("等长流式内容替换也在当前嵌入窗口立即更新", async () => {
    aiState.messages = [{ role: "assistant", content: "旧答案" }];
    const wrapper = mountPanel();
    await wrapper.vm.$nextTick();
    expect(wrapper.find(".ai-chat-panel__message-content").text()).toContain("旧答案");

    // Equal-length replacements are the case a length-only v-memo key misses.
    aiState.messages[0].content = "新答案";
    await wrapper.vm.$nextTick();

    expect(wrapper.find(".ai-chat-panel__message-content").text()).toContain("新答案");
    expect(wrapper.find(".ai-chat-panel__message-content").text()).not.toContain("旧答案");
    wrapper.unmount();
  });

  it("非 embedded 且 aiChatVisible 为 false 时不渲染面板", () => {
    appState.aiChatVisible = false;
    const wrapper = mountPanel({ embedded: false });
    expect(wrapper.find(".ai-chat-panel").exists()).toBe(false);
  });

  it("无消息时显示空状态与建议项", () => {
    const wrapper = mountPanel();
    expect(wrapper.find(".ai-chat-panel__empty").exists()).toBe(true);
    // 5 个建议按钮
    expect(wrapper.findAll(".ai-chat-panel__suggestion")).toHaveLength(5);
    // 清除对话按钮在无消息时不应出现
    expect(wrapper.find(".ai-chat-panel__clear").exists()).toBe(false);
  });

  it("渲染 user 与 assistant 消息并显示角色标签", () => {
    aiState.messages = [
      { role: "user", content: "你好，帮我重构这段代码" },
      { role: "assistant", content: "好的，我来看一下。" },
    ];
    const wrapper = mountPanel();
    const userMsg = wrapper.find(".ai-chat-panel__message--user");
    const assistantMsg = wrapper.find(".ai-chat-panel__message--assistant");
    expect(userMsg.exists()).toBe(true);
    expect(assistantMsg.exists()).toBe(true);
    // 角色标签
    expect(userMsg.find(".ai-chat-panel__message-role").text()).toBe("user");
    expect(assistantMsg.find(".ai-chat-panel__message-role").text()).toBe("assistant");
    // 消息内容（renderMarkdown mock 透传原文）
    expect(wrapper.text()).toContain("帮我重构这段代码");
    expect(wrapper.text()).toContain("好的，我来看一下。");
  });

  it("纯 fence fallback 隐藏工具块，native 混用则保留原文", () => {
    agentState.mode = "agent";
    aiState.messages = [
      { role: "assistant", content: "fallback\n```\nread: fallback.ts\n```" },
      {
        role: "assistant",
        content: "native plus fence\n```\nread: duplicate.ts\n```",
        toolCalls: [{ id: "native-read", name: "read", arguments: '{"path":"a.ts"}' }],
      },
    ];

    const wrapper = mountPanel();
    const messages = wrapper.findAll(".ai-chat-panel__message-content");
    expect(messages[0].text()).not.toContain("read: fallback.ts");
    expect(messages[1].text()).toContain("read: duplicate.ts");
    expect(wrapper.get('[data-testid="chat-message-tool-calls"]').text()).toContain("read");
  });

  it("输入文本后点击发送按钮调用 sendMessage 并清空输入", async () => {
    const wrapper = mountPanel();
    const input = wrapper.find(".ai-chat-panel__input");
    await input.setValue("解释这个函数");
    await wrapper.find(".ai-chat-panel__send").trigger("click");
    await flush();
    expect(sendMessageMock).toHaveBeenCalledTimes(1);
    expect(sendMessageMock).toHaveBeenCalledWith("解释这个函数");
    // 输入框应被清空
    expect((input.element as HTMLInputElement).value).toBe("");
  });

  it("按下 Enter 发送消息，Shift+Enter 不发送", async () => {
    const wrapper = mountPanel();
    const input = wrapper.find(".ai-chat-panel__input");
    await input.setValue("生成测试");
    await input.trigger("keydown", { key: "Enter" });
    await flush();
    expect(sendMessageMock).toHaveBeenCalledWith("生成测试");

    // Shift+Enter 不应触发发送
    sendMessageMock.mockClear();
    await input.setValue("再补一条");
    await input.trigger("keydown", { key: "Enter", shiftKey: true });
    await flush();
    expect(sendMessageMock).not.toHaveBeenCalled();
  });

  it("输入为空时不发送消息", async () => {
    const wrapper = mountPanel();
    await wrapper.find(".ai-chat-panel__input").trigger("keydown", { key: "Enter" });
    await flush();
    expect(sendMessageMock).not.toHaveBeenCalled();
  });

  it("后端未接纳发送时保留用户输入", async () => {
    sendMessageMock.mockResolvedValueOnce(false);
    const wrapper = mountPanel();
    const input = wrapper.get(".ai-chat-panel__input");
    await input.setValue("keep my draft");
    await input.trigger("keydown", { key: "Enter" });
    await flush();

    expect(sendMessageMock).toHaveBeenCalledWith("keep my draft");
    expect((input.element as HTMLInputElement).value).toBe("keep my draft");
  });

  it("点击清除对话按钮调用 clearMessages", async () => {
    // clear 按钮仅在 hasMessages 时渲染
    aiState.messages = [{ role: "user", content: "hi" }];
    const wrapper = mountPanel();
    const clearBtn = wrapper.find(".ai-chat-panel__clear");
    expect(clearBtn.exists()).toBe(true);
    await clearBtn.trigger("click");
    expect(clearMessagesMock).toHaveBeenCalledTimes(1);
  });

  it("嵌入模式切换通过 aiAssistant 同步 visible/backend Agent mode", async () => {
    const wrapper = mountPanel();
    await wrapper.find(".ai-chat-panel__mode-toggle").trigger("click");
    expect(switchAssistantModeMock).toHaveBeenLastCalledWith("agent");
    expect(toggleModeMock).not.toHaveBeenCalled();

    agentState.mode = "agent";
    await wrapper.vm.$nextTick();
    await wrapper.find(".ai-chat-panel__mode-toggle").trigger("click");
    expect(switchAssistantModeMock).toHaveBeenLastCalledWith("chat");
  });

  it("backend stream busy 尾窗内禁用并拒绝 approve/reject/clear", async () => {
    aiState.messages = [{ role: "assistant", content: "tool requested" }];
    aiState.globalStreamBusy = true;
    agentState.mode = "agent";
    agentState.pendingToolCalls = [{
      id: "read-busy",
      kind: "read",
      target: "src/main.ts",
      status: "pending",
    }];
    const wrapper = mountPanel();

    const approve = wrapper.get(".tool-call-card__btn--approve");
    const reject = wrapper.get(".tool-call-card__btn--reject");
    const clear = wrapper.get(".agent-approvals__clear");
    expect(approve.attributes("disabled")).toBeDefined();
    expect(reject.attributes("disabled")).toBeDefined();
    expect(clear.attributes("disabled")).toBeDefined();
    (approve.element as HTMLButtonElement).disabled = false;
    (reject.element as HTMLButtonElement).disabled = false;
    (clear.element as HTMLButtonElement).disabled = false;
    await approve.trigger("click");
    await reject.trigger("click");
    await clear.trigger("click");
    expect(approveAndFeedMock).not.toHaveBeenCalled();
    expect(rejectAndFeedMock).not.toHaveBeenCalled();
    expect(clearPendingToolCallsMock).not.toHaveBeenCalled();
  });

  it("点击建议项调用 sendMessage 并传入对应建议文本", async () => {
    const wrapper = mountPanel();
    const suggestions = wrapper.findAll(".ai-chat-panel__suggestion");
    await suggestions[0].trigger("click");
    await flush();
    // i18n mock 的 t 返回 key 本身，sendSuggestion 透传该 key 给 sendMessage
    expect(sendMessageMock).toHaveBeenCalledWith("aiChat.suggestionExplain");
  });

  it("流式响应中显示停止按钮，点击调用 stopGeneration", async () => {
    aiState.streaming = true;
    const wrapper = mountPanel();
    // 流式时发送按钮被停止按钮替代
    const stopBtn = wrapper.find(".ai-chat-panel__stop");
    expect(stopBtn.exists()).toBe(true);
    expect(wrapper.find(".ai-chat-panel__send").exists()).toBe(false);
    // 输入框在流式时应被禁用
    expect(wrapper.find(".ai-chat-panel__input").attributes("disabled")).toBeDefined();
    await stopBtn.trigger("click");
    expect(stopGenerationMock).toHaveBeenCalledTimes(1);
  });

  it("点击关闭按钮调用 toggleAiChat", async () => {
    const wrapper = mountPanel();
    await wrapper.find(".ai-chat-panel__close").trigger("click");
    expect(toggleAiChatMock).toHaveBeenCalledTimes(1);
  });

  it("点击历史按钮加载对话列表并展示", async () => {
    conversationListMock.mockResolvedValue([
      { id: "c1", title: "第一次对话", created_at: 1700000000, messages: [] },
      { id: "c2", title: "第二次对话", created_at: 1700000100, messages: [] },
    ]);
    const wrapper = mountPanel();
    // P2-13: history 已折叠进 ⋯ more 菜单，通过 el-dropdown command 触发
    await wrapper.findComponent({ name: "ElDropdown" }).vm.$emit("command", "history");
    await flush();
    expect(conversationListMock).toHaveBeenCalledTimes(1);
    // 历史面板展开
    expect(wrapper.find(".ai-chat-panel__history-panel").exists()).toBe(true);
    // 历史项渲染
    const items = wrapper.findAll(".ai-chat-panel__history-item");
    expect(items).toHaveLength(2);
    expect(wrapper.text()).toContain("第一次对话");
  });

  it("离线时发送按钮被禁用", async () => {
    connectivityState.online = false;
    const wrapper = mountPanel();
    const input = wrapper.find(".ai-chat-panel__input");
    await input.setValue("离线测试");
    const sendBtn = wrapper.find(".ai-chat-panel__send");
    expect(sendBtn.attributes("disabled")).toBeDefined();
    await sendBtn.trigger("click");
    await flush();
    expect(sendMessageMock).not.toHaveBeenCalled();
  });
});
