import { shallowMount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";

const handle = vi.hoisted(() => ({
  state: undefined as unknown as Record<string, unknown>,
  fns: undefined as unknown as Record<string, ReturnType<typeof vi.fn>>,
}));

vi.mock("@/stores/mcp", async () => {
  const { reactive, ref } = await import("vue");
  handle.state = reactive({
    servers: [
      { name: "fs", transport: "stdio", command: "fixture.exe", args: [], enabled: true },
      { name: "off", transport: "http", url: "https://example.com", enabled: true },
    ],
    connected: { fs: true },
    agentTools: [],
    serverContexts: {
      fs: {
        status: "loaded",
        resourcesStatus: "loaded",
        resources: [{ uri: "fixture://notes", name: "notes", mimeType: "text/plain" }],
        resourcesError: null,
        promptsStatus: "loaded",
        prompts: [{ name: "greet", description: "greets" }],
        promptsError: null,
        capabilities: {
          protocolVersion: "2024-11-05",
          serverInfo: { name: "fs", version: "1.0" },
          capabilities: {
            tools: { state: "supported", declared: true },
            resources: { state: "supported", declared: true },
            prompts: { state: "supported", declared: true },
            sampling: { state: "unsupported", declared: false },
            elicitation: { state: "unsupported", declared: false },
            logging: { state: "unsupported", declared: false },
          },
          serverName: "fs",
          rootGeneration: 1,
          lifecycleGeneration: 3,
          run: 7,
          establishedAt: "2026-08-28T00:00:00Z",
        },
        error: null,
        lifecycleGeneration: 3,
      },
    },
    contextsWorkspaceRoot: null,
    loading: false,
    error: null,
  });
  handle.fns = {
    loadMcpServers: vi.fn().mockResolvedValue(undefined),
    refreshAgentMcpTools: vi.fn().mockResolvedValue(undefined),
    connectMcpServer: vi.fn().mockResolvedValue(true),
    disconnectMcpServer: vi.fn().mockResolvedValue(true),
    toggleMcpServerEnabled: vi.fn().mockResolvedValue(true),
    saveMcpServer: vi.fn().mockResolvedValue(true),
    deleteMcpServer: vi.fn().mockResolvedValue(true),
    refreshMcpServerContext: vi.fn().mockResolvedValue(undefined),
    injectMcpResourceContext: vi.fn().mockResolvedValue(true),
    injectMcpPromptContext: vi.fn().mockResolvedValue(true),
    clearStaleMcpContexts: vi.fn(),
    openServerEditor: vi.fn(),
    closeServerEditor: vi.fn(),
  };
  return {
    ...handle.fns,
    mcpState: handle.state,
    mcpServers: ref(handle.state.servers),
    agentMcpTools: ref([]),
    editingServer: ref(null),
  };
});

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({ t: (key: string) => key }),
}));

import McpSection from "./McpSection.vue";

function mountSection() {
  return shallowMount(McpSection, {
    global: {
      stubs: {
        "el-button": {
          emits: ["click"],
          template: '<button type="button" @click="$emit(\'click\')"><slot /></button>',
        },
        "el-switch": true,
        "el-select": true,
        "el-option": true,
      },
    },
  });
}

async function expandContext(wrapper: ReturnType<typeof mountSection>, name: string): Promise<void> {
  const buttons = wrapper.findAll("button");
  const contextButton = buttons.find((button) => button.text() === "mcpSection.contextButton");
  if (!contextButton) {
    throw new Error(`no context button for ${name}`);
  }
  await contextButton.trigger("click");
  await wrapper.vm.$nextTick();
}

describe("McpSection server context panel", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("hides the context button for servers that are not connected", () => {
    const wrapper = mountSection();
    const contextButtons = wrapper.findAll("button").filter((b) => b.text() === "mcpSection.contextButton");
    expect(contextButtons).toHaveLength(1);
  });

  it("projects capabilities including unsupported client-side families", async () => {
    const wrapper = mountSection();
    await expandContext(wrapper, "fs");
    const text = wrapper.text();
    expect(text).toContain("mcpSection.contextStatusloaded");
    expect(text).toContain("mcpSection.capTools");
    expect(text).toContain("mcpSection.capSupported");
    // sampling/elicitation/logging are client-unsupported and must be shown,
    // never silently hidden behind a generic label.
    expect(text).toContain("mcpSection.capSampling");
    expect(text).toContain("mcpSection.capUnsupported");
    expect(text).toContain("mcpSection.capElicitation");
    expect(text).toContain("mcpSection.capLogging");
    expect(text).toContain("2024-11-05");
  });

  it("lists resources and prompts with inject controls bound to the store actions", async () => {
    const wrapper = mountSection();
    await expandContext(wrapper, "fs");
    const text = wrapper.text();
    expect(text).toContain("fixture://notes");
    expect(text).toContain("mcpSection.promptsTitle");
    expect(text).toContain("greet");

    const injectButtons = wrapper.findAll("button").filter((button) => button.text() === "mcpSection.inject");
    expect(injectButtons.length).toBe(2);
    await injectButtons[0].trigger("click");
    await wrapper.vm.$nextTick();
    expect(handle.fns.injectMcpResourceContext).toHaveBeenCalledWith("fs", "fixture://notes");
    await injectButtons[1].trigger("click");
    await wrapper.vm.$nextTick();
    expect(handle.fns.injectMcpPromptContext).toHaveBeenCalledWith("fs", "greet", {});
    expect(wrapper.text()).toContain("mcpSection.injected");
  });

  it("projects stale and error states with diagnosable messages", async () => {
    const state = handle.state as unknown as {
      serverContexts: Record<string, { status: string; resourcesStatus: string; resourcesError: string | null }>;
    };
    state.serverContexts.fs.status = "stale";
    state.serverContexts.fs.resourcesStatus = "error";
    state.serverContexts.fs.resourcesError = "rpc error -32000: boom";
    const wrapper = mountSection();
    await expandContext(wrapper, "fs");
    const text = wrapper.text();
    expect(text).toContain("mcpSection.contextStatusstale");
    expect(text).toContain("rpc error -32000: boom");
    expect(text).not.toContain("mcpSection.contextStatusloaded");
  });

  it("refreshes an unloaded context when the panel is expanded", async () => {
    const state = handle.state as unknown as { serverContexts: Record<string, unknown> };
    delete state.serverContexts.fs;
    const wrapper = mountSection();
    await expandContext(wrapper, "fs");
    expect(handle.fns.refreshMcpServerContext).toHaveBeenCalledWith("fs");
    // The refresh button is available for manual reloads too.
    await wrapper.findAll("button").find((b) => b.text() === "mcpSection.refreshContext")!.trigger("click");
    expect(handle.fns.refreshMcpServerContext).toHaveBeenCalledTimes(2);
  });

  it("offers a stale-context cleanup action in the toolbar", async () => {
    const wrapper = mountSection();
    const clearButton = wrapper.findAll("button").find((b) => b.text() === "mcpSection.clearStale");
    expect(clearButton).toBeDefined();
    await clearButton!.trigger("click");
    expect(handle.fns.clearStaleMcpContexts).toHaveBeenCalledTimes(1);
  });
});
