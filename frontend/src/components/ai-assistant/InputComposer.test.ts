/**
 * InputComposer tests (Plan 11 Task 3).
 *
 * Verifies (Step 11):
 *   - Slash command popup filtering + keyboard navigation + selection (Step 6).
 *   - @ mention popup + selection adds a context chip (Step 5).
 *   - Image paste → image chip via FileReader (Step 3).
 *   - Code block paste → codeblock chip with detected language (Step 4).
 *   - detectLanguage identifies Go/TypeScript/Vue/Python (Step 4).
 *   - selectSlash switches mode for /goal and /agent (Step 6).
 *   - Legacy /plan input is not exposed and never creates an empty plan.
 *   - selectSlash clears messages for /clear (Step 6).
 *   - contextChips preview row + remove button (Step 8).
 *   - tokenCount includes text + chips content (Step 9).
 *   - Enter sends, Shift+Enter inserts newline (Step 1).
 */
import { describe, it, expect, vi, beforeEach } from "vitest";
import { mount } from "@vue/test-utils";
import { nextTick } from "vue";
import type { ContextChip } from "@/types";

// Use vi.hoisted so mock factories can reference these without hoisting issues.
const {
  aiStateMock,
  aiAssistantStateMock,
  appStateMock,
  storeFnsMock,
  mcpMock,
  skillsMock,
	agentStateMock,
} = vi.hoisted(() => ({
  aiStateMock: {
    messages: [] as Array<{ role: string; content: string }>,
    streaming: false,
    globalStreamBusy: false,
    error: null as string | null,
    context: null,
    currentConversationId: null as string | null,
    currentConversationTitle: null as string | null,
    mentionedFiles: [] as unknown[],
    contextChips: [] as ContextChip[],
    currentSystemPromptOverride: null as string | null,
  },
  aiAssistantStateMock: {
    mode: "chat",
    sidebarWidth: 260,
    contextPanelCollapsed: false,
    activeConversationId: null as string | null,
  },
  appStateMock: {
    aiModel: "gpt-4",
    aiSystemPrompt: "",
  },
  storeFnsMock: {
    sendMessage: vi.fn(),
    stopGeneration: vi.fn(),
    addContextChip: vi.fn(),
    removeContextChip: vi.fn(),
    clearMessages: vi.fn(),
    createPlan: vi.fn(),
  },
  mcpMock: {
    // 用普通对象模拟 ref 的 `.value` 接口（vi.hoisted 中不能调用 ref）。
    // 组件里 `agentMcpTools.value` 读取此 value；测试设置此 value 即可。
    agentToolsRef: {
      value: [] as Array<{
        namespace: string;
        server: string;
        tool: string;
        description: string;
        riskLevel: string;
      }>,
    },
    refresh: vi.fn(),
  },
  skillsMock: {
    // 用普通对象模拟 ref 的 `.value` 接口（vi.hoisted 中不能调用 ref）。
    skillsListRef: {
      value: [] as Array<{
        id: string;
        name: string;
        description: string;
        priority: number;
        trigger: { keywords?: string[]; regex?: string; manual?: boolean };
        systemPrompt: string;
        allowedTools?: string[];
        allowedMcp?: string[];
        examples?: string[];
        scope: "project" | "user" | "global";
        filePath?: string;
      }>,
    },
    load: vi.fn(async () => {
      /* noop */
    }),
    activate: vi.fn(async () => true),
    skillsState: { error: null as string | null },
  },
	agentStateMock: { sessionId: "chat-test" },
}));

// Mock @/lib/i18n to cut the @/stores/app → @/lib/monaco-themes → monaco-editor
// import chain (monaco-editor cannot resolve in the test environment).
vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({
    t: (key: string) => key,
  }),
  translate: (key: string) => key,
}));

vi.mock("@/stores/ai", () => ({
  aiState: aiStateMock,
  sendMessage: storeFnsMock.sendMessage,
  stopGeneration: storeFnsMock.stopGeneration,
  addContextChip: storeFnsMock.addContextChip,
  removeContextChip: storeFnsMock.removeContextChip,
  clearMessages: storeFnsMock.clearMessages,
}));

vi.mock("@/stores/aiAssistant", () => ({
  aiAssistantState: aiAssistantStateMock,
  switchMode: vi.fn((mode: string) => {
    aiAssistantStateMock.mode = mode === "plan" ? "chat" : mode;
  }),
}));

vi.mock("@/stores/aiPlan", () => ({
  createPlan: storeFnsMock.createPlan,
}));

vi.mock("@/stores/app", () => ({
  appState: appStateMock,
  activeAIConfig: () => ({ protocol: "openai" }),
}));

// Plan 11 Task 4 Step 7 — mock MCP store so @MCP 二级菜单可被测试。
vi.mock("@/stores/mcp", () => ({
  agentMcpTools: mcpMock.agentToolsRef,
  refreshAgentMcpTools: mcpMock.refresh,
}));

// Plan 11 Task 5 Step 6 — mock Skills store so @Skill 二级菜单可被测试。
vi.mock("@/stores/skills", () => ({
  skillsList: skillsMock.skillsListRef,
  loadSkills: skillsMock.load,
  activateSkill: skillsMock.activate,
  skillsState: skillsMock.skillsState,
}));

vi.mock("@/stores/agent", () => ({
	agentState: agentStateMock,
	ensureAgentSession: vi.fn(() => agentStateMock.sessionId),
}));

// prompt-5 Task D / BUG-M2: implementation uses ElMessageBox.confirm, not window.confirm.
const { elMessageBoxConfirmMock } = vi.hoisted(() => ({
  elMessageBoxConfirmMock: vi.fn().mockResolvedValue("confirm"),
}));
vi.mock("element-plus", async (importOriginal) => {
  const actual = await importOriginal<typeof import("element-plus")>();
  return {
    ...actual,
    ElMessageBox: {
      ...actual.ElMessageBox,
      confirm: elMessageBoxConfirmMock,
    },
  };
});

// Stub out FileReader for the image-paste test.
class FakeFileReader {
  result: string | null = null;
  onload: (() => void) | null = null;
  readAsDataURL(_file: File): void {
    this.result = "data:image/png;base64,AAAA";
    // Fire onload asynchronously so the chip is added after onPaste returns.
    queueMicrotask(() => {
      this.onload?.();
    });
  }
}
// Make FakeFileReader resolvable as the global FileReader type.
(globalThis as unknown as { FileReader: typeof FileReader }).FileReader =
  FakeFileReader as unknown as typeof FileReader;

// Import the component AFTER mocks are set up.
import InputComposer from "./InputComposer.vue";

// Vue 3 <script setup> defineExpose'd refs are auto-unwrapped on the vm
// proxy, so we type them as their inner values and access them directly.
interface ExposedVM {
  text: string;
  showSlashMenu: boolean;
  showMentionMenu: boolean;
  showMcpToolMenu: boolean;
  showSkillMenu: boolean;
  slashIndex: number;
  mentionIndex: number;
  mcpToolIndex: number;
  skillIndex: number;
  filteredSlashCommands: Array<{ cmd: string; mode?: string; action?: string }>;
  filteredMcpTools: Array<{
    namespace: string;
    server: string;
    tool: string;
    description: string;
    riskLevel: string;
  }>;
  filteredSkills: Array<{
    id: string;
    name: string;
    description: string;
    priority: number;
    trigger: { keywords?: string[]; regex?: string; manual?: boolean };
    systemPrompt: string;
    allowedTools?: string[];
    allowedMcp?: string[];
    examples?: string[];
    scope: "project" | "user" | "global";
    filePath?: string;
  }>;
  tokenCount: number;
  handleSend: () => void;
  handleStop: () => void;
  handleAttach: () => void;
  handleRemoveChip: (id: string) => void;
  selectSlash: (cmd: { cmd: string; mode?: string; action?: string }) => void;
  selectMention: (m: { kind: string; labelKey: string }) => void;
  selectMcpTool: (idx: number) => void;
  selectSkill: (idx: number) => Promise<void>;
  onKeydown: (e: KeyboardEvent) => void;
  onInput: () => void;
  onPaste: (e: ClipboardEvent) => void;
  detectLanguage: (code: string) => string;
  estimateTokensLocal: (s: string) => number;
  autoResize: () => void;
}

function mountComposer() {
  return mount(InputComposer, {});
}

// jsdom lacks DataTransfer/ClipboardEvent constructors with clipboardData, so
// we build a minimal mock that satisfies onPaste's access pattern.
interface FakePasteItem {
  kind: string;
  type?: string;
  file?: File;
  getAsFile?: () => File | null;
}
function makePasteEvent(opts: {
  items?: FakePasteItem[];
  text?: string;
  preventDefault?: () => void;
}): ClipboardEvent {
  const items = (opts.items ?? []).map((it) => ({
    kind: it.kind,
    type: it.type ?? "",
    getAsFile: () => it.file ?? null,
  }));
  const clipboardData = {
    items,
    getData: (type: string) => (type === "text" ? opts.text ?? "" : ""),
  };
  const preventDefault = opts.preventDefault ?? (() => {});
  return {
    clipboardData,
    preventDefault,
  } as unknown as ClipboardEvent;
}

function vmOf(w: ReturnType<typeof mountComposer>): ExposedVM {
  return w.vm as unknown as ExposedVM;
}

describe("InputComposer (Plan 11 Task 3)", () => {
  beforeEach(() => {
    storeFnsMock.sendMessage.mockReset();
    storeFnsMock.sendMessage.mockResolvedValue(true);
    storeFnsMock.stopGeneration.mockReset();
    storeFnsMock.addContextChip.mockReset();
    storeFnsMock.removeContextChip.mockReset();
    storeFnsMock.clearMessages.mockReset();
    storeFnsMock.createPlan.mockReset();
    storeFnsMock.createPlan.mockResolvedValue(true);
    elMessageBoxConfirmMock.mockReset();
    elMessageBoxConfirmMock.mockResolvedValue("confirm");
    aiStateMock.streaming = false;
    aiStateMock.globalStreamBusy = false;
    aiStateMock.contextChips = [];
    aiAssistantStateMock.mode = "chat";
    appStateMock.aiModel = "gpt-4";
    mcpMock.agentToolsRef.value = [];
    mcpMock.refresh.mockReset();
    skillsMock.skillsListRef.value = [];
    skillsMock.load.mockReset();
    skillsMock.activate.mockReset();
    skillsMock.activate.mockResolvedValue(true);
    skillsMock.skillsState.error = null;
  });

  // --- Step 9: token estimator (matches token_estimator.go) ---
  describe("estimateTokensLocal (Step 9)", () => {
    it("returns 0 for empty string", () => {
      const w = mountComposer();
      expect(vmOf(w).estimateTokensLocal("")).toBe(0);
    });

    it("uses ~4 chars/token for ASCII", () => {
      const w = mountComposer();
      // 8 ASCII chars → 2 tokens
      expect(vmOf(w).estimateTokensLocal("abcdefgh")).toBe(2);
    });

    it("uses ~2 chars/token for CJK", () => {
      const w = mountComposer();
      // 4 CJK chars → 2 tokens
      expect(vmOf(w).estimateTokensLocal("你好世界")).toBe(2);
    });

    it("blends CJK + ASCII", () => {
      const w = mountComposer();
      // 2 CJK (1 token) + 4 ASCII (1 token) = 2 tokens
      expect(vmOf(w).estimateTokensLocal("你好abcd")).toBe(2);
    });
  });

  // --- Step 4: language detection ---
  describe("detectLanguage (Step 4)", () => {
    it("detects Go", () => {
      const w = mountComposer();
      expect(vmOf(w).detectLanguage("package main\nfunc foo() {}")).toBe("go");
    });

    it("detects TypeScript", () => {
      const w = mountComposer();
      expect(vmOf(w).detectLanguage("export const x = 1;")).toBe("typescript");
    });

    it("detects Vue", () => {
      const w = mountComposer();
      expect(vmOf(w).detectLanguage("<template>hi</template>")).toBe("vue");
    });

    it("detects Python", () => {
      const w = mountComposer();
      expect(vmOf(w).detectLanguage("def foo():\n    pass")).toBe("python");
    });

    it("defaults to text", () => {
      const w = mountComposer();
      expect(vmOf(w).detectLanguage("hello world")).toBe("text");
    });
  });

  // --- Step 6: slash command popup ---
  describe("slash command popup (Step 6)", () => {
    it("shows popup when text starts with /", async () => {
      const w = mountComposer();
      const vm = vmOf(w);
      vm.text = "/";
      vm.onInput();
      await nextTick();
      expect(vm.showSlashMenu).toBe(true);
      expect(vm.filteredSlashCommands.length).toBeGreaterThan(0);
    });

    it("does not expose the unfinished /plan command", async () => {
      const w = mountComposer();
      const vm = vmOf(w);
      vm.text = "/pl";
      vm.onInput();
      await nextTick();
      expect(vm.filteredSlashCommands.map((cmd) => cmd.cmd)).not.toContain("/plan");
    });

    it("hides popup when text has a space", async () => {
      const w = mountComposer();
      const vm = vmOf(w);
      vm.text = "/plan hello";
      vm.onInput();
      await nextTick();
      // /^\/(\w*)$/ does not match when there's a space → empty filter list
      expect(vm.filteredSlashCommands).toHaveLength(0);
    });

    it("ArrowDown moves selection down with wrap-around", async () => {
      const w = mountComposer();
      const vm = vmOf(w);
      vm.text = "/";
      vm.onInput();
      await nextTick();
      const len = vm.filteredSlashCommands.length;
      expect(len).toBeGreaterThan(0);
      vm.slashIndex = 0;
      vm.onKeydown(new KeyboardEvent("keydown", { key: "ArrowDown" }));
      expect(vm.slashIndex).toBe(1);
      // Wrap around to 0 from last index
      vm.slashIndex = len - 1;
      vm.onKeydown(new KeyboardEvent("keydown", { key: "ArrowDown" }));
      expect(vm.slashIndex).toBe(0);
    });

    it("ArrowUp moves selection up with wrap-around", async () => {
      const w = mountComposer();
      const vm = vmOf(w);
      vm.text = "/";
      vm.onInput();
      await nextTick();
      vm.slashIndex = 0;
      vm.onKeydown(new KeyboardEvent("keydown", { key: "ArrowUp" }));
      const len = vm.filteredSlashCommands.length;
      expect(vm.slashIndex).toBe(len - 1);
    });

    it("Escape closes the popup", async () => {
      const w = mountComposer();
      const vm = vmOf(w);
      vm.text = "/";
      vm.onInput();
      await nextTick();
      expect(vm.showSlashMenu).toBe(true);
      vm.onKeydown(new KeyboardEvent("keydown", { key: "Escape" }));
      expect(vm.showSlashMenu).toBe(false);
    });
  });

  // --- Step 6: slash command selection semantics ---
  describe("selectSlash (Step 6)", () => {
    it("normalizes legacy /plan selection to chat without creating a plan", () => {
      const w = mountComposer();
      const vm = vmOf(w);
      vm.text = "/plan";
      vm.selectSlash({ cmd: "/plan", mode: "plan" });
      expect(aiAssistantStateMock.mode).toBe("chat");
      expect(storeFnsMock.createPlan).not.toHaveBeenCalled();
      expect(vm.text).toBe("");
    });

    it("/goal switches to goal mode", () => {
      const w = mountComposer();
      const vm = vmOf(w);
      vm.selectSlash({ cmd: "/goal", mode: "goal" });
      expect(aiAssistantStateMock.mode).toBe("goal");
    });

    it("/agent switches to agent mode", () => {
      const w = mountComposer();
      const vm = vmOf(w);
      vm.selectSlash({ cmd: "/agent", mode: "agent" });
      expect(aiAssistantStateMock.mode).toBe("agent");
    });

    it("/clear calls clearMessages and clears text", () => {
      const w = mountComposer();
      const vm = vmOf(w);
      vm.text = "/clear";
      vm.selectSlash({ cmd: "/clear", action: "clear" });
      expect(storeFnsMock.clearMessages).toHaveBeenCalledTimes(1);
      expect(vm.text).toBe("");
    });

    it("prompt-style command inserts /cmd + space", () => {
      const w = mountComposer();
      const vm = vmOf(w);
      vm.selectSlash({ cmd: "/explain" });
      expect(vm.text).toBe("/explain ");
    });
  });

  it("does not create an empty plan when a legacy plan mode is restored", async () => {
    const w = mountComposer();
    const vm = vmOf(w);
    aiAssistantStateMock.mode = "plan";
    vm.text = "review the workspace";

    await vm.handleSend();

    expect(storeFnsMock.createPlan).not.toHaveBeenCalled();
    expect(storeFnsMock.sendMessage).toHaveBeenCalledWith("review the workspace");
  });

  // --- Step 5: @ mention popup ---
  describe("@ mention popup (Step 5)", () => {
    it("shows popup when trailing @ is typed", async () => {
      const w = mountComposer();
      const vm = vmOf(w);
      vm.text = "hello @";
      vm.onInput();
      await nextTick();
      expect(vm.showMentionMenu).toBe(true);
      // The trailing @ should be stripped so it doesn't end up in the message.
      expect(vm.text).toBe("hello ");
    });

    it("selectMention adds a context chip", () => {
      const w = mountComposer();
      const vm = vmOf(w);
      vm.selectMention({ kind: "file", labelKey: "aiAssistant.mentionFile" });
      expect(storeFnsMock.addContextChip).toHaveBeenCalledTimes(1);
      const chip = storeFnsMock.addContextChip.mock.calls[0][0] as ContextChip;
      expect(chip.kind).toBe("file");
      expect(chip.label).toBe("aiAssistant.mentionFile");
      expect(vm.showMentionMenu).toBe(false);
    });

    it("ArrowDown moves mention selection down with wrap-around", async () => {
      const w = mountComposer();
      const vm = vmOf(w);
      vm.text = "@";
      vm.onInput();
      await nextTick();
      vm.mentionIndex = 0;
      vm.onKeydown(new KeyboardEvent("keydown", { key: "ArrowDown" }));
      expect(vm.mentionIndex).toBe(1);
    });

    it("Escape closes the mention popup", async () => {
      const w = mountComposer();
      const vm = vmOf(w);
      vm.text = "@";
      vm.onInput();
      await nextTick();
      expect(vm.showMentionMenu).toBe(true);
      vm.onKeydown(new KeyboardEvent("keydown", { key: "Escape" }));
      expect(vm.showMentionMenu).toBe(false);
    });

    it("handleAttach opens the mention popup", () => {
      const w = mountComposer();
      const vm = vmOf(w);
      vm.handleAttach();
      expect(vm.showMentionMenu).toBe(true);
    });
  });

  // --- Step 3: image paste ---
  describe("image paste (Step 3)", () => {
    it("adds an image chip via FileReader.readAsDataURL", async () => {
      const w = mountComposer();
      const vm = vmOf(w);
      const file = { name: "pic.png", type: "image/png" } as unknown as File;
      const evt = makePasteEvent({ items: [{ kind: "file", type: "image/png", file }], text: "" });
      vm.onPaste(evt);
      // FileReader.readAsDataURL is async via queueMicrotask.
      await new Promise<void>((r) => queueMicrotask(r));
      expect(storeFnsMock.addContextChip).toHaveBeenCalledTimes(1);
      const chip = storeFnsMock.addContextChip.mock.calls[0][0] as ContextChip;
      expect(chip.kind).toBe("image");
      expect(chip.label).toBe("pic.png");
      expect(chip.imageUrl).toContain("data:image/png;base64,");
    });
  });

  // --- Step 4: code block paste ---
  describe("code block paste (Step 4)", () => {
    it("adds a codeblock chip for fenced content", () => {
      const w = mountComposer();
      const vm = vmOf(w);
      const evt = makePasteEvent({ text: "```go\nfunc main() {}\n```" });
      vm.onPaste(evt);
      expect(storeFnsMock.addContextChip).toHaveBeenCalledTimes(1);
      const chip = storeFnsMock.addContextChip.mock.calls[0][0] as ContextChip;
      expect(chip.kind).toBe("codeblock");
      expect(chip.content).toContain("func main()");
    });

    it("adds a codeblock chip for multi-line plain text", () => {
      const w = mountComposer();
      const vm = vmOf(w);
      const evt = makePasteEvent({ text: "line1\nline2\nline3\nline4" });
      vm.onPaste(evt);
      expect(storeFnsMock.addContextChip).toHaveBeenCalledTimes(1);
      const chip = storeFnsMock.addContextChip.mock.calls[0][0] as ContextChip;
      expect(chip.kind).toBe("codeblock");
    });

    it("lets default paste happen for short plain text", () => {
      const w = mountComposer();
      const vm = vmOf(w);
      const preventDefault = vi.fn();
      const evt = makePasteEvent({ text: "hi", preventDefault });
      vm.onPaste(evt);
      expect(storeFnsMock.addContextChip).not.toHaveBeenCalled();
      expect(preventDefault).not.toHaveBeenCalled();
    });
  });

  // --- Step 8: chips preview row + remove ---
  describe("chips preview + remove (Step 8)", () => {
    it("handleRemoveChip calls removeContextChip", () => {
      const w = mountComposer();
      const vm = vmOf(w);
      vm.handleRemoveChip("chip-1");
      expect(storeFnsMock.removeContextChip).toHaveBeenCalledWith("chip-1");
    });

    it("renders chips preview row when contextChips non-empty", async () => {
      aiStateMock.contextChips = [
        { id: "c1", kind: "file", label: "main.go" },
      ];
      const w = mountComposer();
      await nextTick();
      const chipsRow = w.find(".ai-input__chips");
      expect(chipsRow.exists()).toBe(true);
      const chipEls = w.findAll(".ai-input__chip");
      expect(chipEls).toHaveLength(1);
    });

    it("hides chips preview row when contextChips empty", async () => {
      aiStateMock.contextChips = [];
      const w = mountComposer();
      await nextTick();
      expect(w.find(".ai-input__chips").exists()).toBe(false);
    });

    it("clicking a chip's remove button calls removeContextChip", async () => {
      aiStateMock.contextChips = [
        { id: "chip-x", kind: "file", label: "main.go" },
      ];
      const w = mountComposer();
      await nextTick();
      const removeBtn = w.find(".ai-input__chip-remove");
      expect(removeBtn.exists()).toBe(true);
      await removeBtn.trigger("click");
      expect(storeFnsMock.removeContextChip).toHaveBeenCalledWith("chip-x");
    });
  });

  // --- Step 9: token count includes text + chips ---
  describe("tokenCount (Step 9)", () => {
    it("counts text tokens", async () => {
      const w = mountComposer();
      const vm = vmOf(w);
      vm.text = "abcdefgh"; // 2 tokens
      await nextTick();
      expect(vm.tokenCount).toBe(2);
    });

    it("adds chip content tokens", async () => {
      aiStateMock.contextChips = [
        { id: "c1", kind: "codeblock", label: "code", content: "abcd" }, // 1 token
      ];
      const w = mountComposer();
      const vm = vmOf(w);
      vm.text = "efgh"; // 1 token
      await nextTick();
      expect(vm.tokenCount).toBe(2);
    });

    it("ignores chips without content", async () => {
      aiStateMock.contextChips = [
        { id: "c1", kind: "file", label: "main.go" }, // no content → 0 tokens
      ];
      const w = mountComposer();
      const vm = vmOf(w);
      vm.text = "abcd"; // 1 token
      await nextTick();
      expect(vm.tokenCount).toBe(1);
    });
  });

  // --- Step 1: Enter sends, Shift+Enter newline ---
  describe("keydown handling (Step 1)", () => {
    it("Enter sends the message", () => {
      const w = mountComposer();
      const vm = vmOf(w);
      vm.text = "hello";
      vm.onKeydown(new KeyboardEvent("keydown", { key: "Enter" }));
      expect(storeFnsMock.sendMessage).toHaveBeenCalledWith("hello");
    });

    it("Shift+Enter does NOT send", () => {
      const w = mountComposer();
      const vm = vmOf(w);
      vm.text = "hello";
      vm.onKeydown(new KeyboardEvent("keydown", { key: "Enter", shiftKey: true }));
      expect(storeFnsMock.sendMessage).not.toHaveBeenCalled();
    });

    it("does not send empty text", () => {
      const w = mountComposer();
      const vm = vmOf(w);
      vm.text = "   ";
      vm.onKeydown(new KeyboardEvent("keydown", { key: "Enter" }));
      expect(storeFnsMock.sendMessage).not.toHaveBeenCalled();
    });

    it("does not send while streaming", () => {
      const w = mountComposer();
      const vm = vmOf(w);
      aiStateMock.streaming = true;
      vm.text = "hello";
      vm.onKeydown(new KeyboardEvent("keydown", { key: "Enter" }));
      expect(storeFnsMock.sendMessage).not.toHaveBeenCalled();
    });
  });

  // --- Step 7: send / stop buttons ---
  describe("send + stop (Step 7)", () => {
    it("handleSend calls sendMessage with trimmed text and clears input", async () => {
      const w = mountComposer();
      const vm = vmOf(w);
      vm.text = "  hi  ";
      await vm.handleSend();
      expect(storeFnsMock.sendMessage).toHaveBeenCalledWith("hi");
      expect(vm.text).toBe("");
    });

    it("keeps the draft when the store rejects stream admission", async () => {
      storeFnsMock.sendMessage.mockResolvedValueOnce(false);
      const w = mountComposer();
      const vm = vmOf(w);
      vm.text = "keep this draft";

      await vm.handleSend();

      expect(storeFnsMock.sendMessage).toHaveBeenCalledWith("keep this draft");
      expect(vm.text).toBe("keep this draft");
    });

    it("does not send while the backend stream slot is still busy", async () => {
      aiStateMock.globalStreamBusy = true;
      const w = mountComposer();
      const vm = vmOf(w);
      vm.text = "wait for cleanup";
      await nextTick();

      await vm.handleSend();

      expect(storeFnsMock.sendMessage).not.toHaveBeenCalled();
      expect(vm.text).toBe("wait for cleanup");
      expect(w.get(".ai-input__send").attributes("disabled")).toBeDefined();
    });

    it("handleStop calls stopGeneration", () => {
      const w = mountComposer();
      const vm = vmOf(w);
      vm.handleStop();
      expect(storeFnsMock.stopGeneration).toHaveBeenCalledTimes(1);
    });

    it("shows send button when not streaming", async () => {
      aiStateMock.streaming = false;
      const w = mountComposer();
      await nextTick();
      expect(w.find(".ai-input__send").exists()).toBe(true);
      expect(w.find(".ai-input__stop").exists()).toBe(false);
    });

    it("shows stop button when streaming", async () => {
      aiStateMock.streaming = true;
      const w = mountComposer();
      await nextTick();
      expect(w.find(".ai-input__stop").exists()).toBe(true);
      expect(w.find(".ai-input__send").exists()).toBe(false);
    });
  });

  // --- Step 7: bottom toolbar mode select ---
  describe("bottom toolbar (Step 7)", () => {
    it("renders only complete input modes", () => {
      const w = mountComposer();
      const select = w.find(".ai-input__select");
      expect(select.exists()).toBe(true);
      const values = select.findAll("option").map((o) => o.attributes("value"));
      expect(values).toEqual(["chat", "goal", "agent"]);
    });

    it("renders model display", () => {
      const w = mountComposer();
      const model = w.find(".ai-input__model");
      expect(model.exists()).toBe(true);
      expect(model.text()).toContain("gpt-4");
    });

    it("renders attach button", () => {
      const w = mountComposer();
      const button = w.get(".ai-input__tool-btn");
      expect(button.attributes("aria-label")).toBe("a11y.attachContext");
    });
  });

  // --- Task 4 Step 7: @MCP 二级菜单 ---
  describe("@MCP tool picker (Task 4 Step 7)", () => {
    it("selectMention(mcp) opens secondary menu and refreshes tools", () => {
      const w = mountComposer();
      const vm = vmOf(w);
      vm.selectMention({ kind: "mcp", labelKey: "aiAssistant.mentionMcp" });
      expect(vm.showMentionMenu).toBe(false);
      expect(vm.showMcpToolMenu).toBe(true);
      expect(vm.mcpToolIndex).toBe(0);
      expect(mcpMock.refresh).toHaveBeenCalled();
    });

    it("selectMcpTool inserts an mcp chip with namespace", () => {
      mcpMock.agentToolsRef.value = [
        {
          namespace: "mcp.fs.read_file",
          server: "fs",
          tool: "read_file",
          description: "Read a file",
          riskLevel: "elevated",
        },
      ];
      const w = mountComposer();
      const vm = vmOf(w);
      vm.selectMention({ kind: "mcp", labelKey: "aiAssistant.mentionMcp" });
      vm.selectMcpTool(0);
      expect(vm.showMcpToolMenu).toBe(false);
      expect(storeFnsMock.addContextChip).toHaveBeenCalledWith(
        expect.objectContaining({
          kind: "mcp",
          label: "mcp.fs.read_file",
          content: "Read a file",
        }),
      );
    });

    it("ArrowDown/Up navigate the tool list", () => {
      mcpMock.agentToolsRef.value = [
        {
          namespace: "mcp.a.t1",
          server: "a",
          tool: "t1",
          description: "",
          riskLevel: "elevated",
        },
        {
          namespace: "mcp.a.t2",
          server: "a",
          tool: "t2",
          description: "",
          riskLevel: "dangerous",
        },
      ];
      const w = mountComposer();
      const vm = vmOf(w);
      vm.selectMention({ kind: "mcp", labelKey: "aiAssistant.mentionMcp" });
      expect(vm.mcpToolIndex).toBe(0);
      vm.onKeydown(new KeyboardEvent("keydown", { key: "ArrowDown" }));
      expect(vm.mcpToolIndex).toBe(1);
      vm.onKeydown(new KeyboardEvent("keydown", { key: "ArrowDown" }));
      expect(vm.mcpToolIndex).toBe(0); // wraps
      vm.onKeydown(new KeyboardEvent("keydown", { key: "ArrowUp" }));
      expect(vm.mcpToolIndex).toBe(1); // wraps back
    });

    it("Enter selects the highlighted tool", () => {
      mcpMock.agentToolsRef.value = [
        {
          namespace: "mcp.s.do",
          server: "s",
          tool: "do",
          description: "do something",
          riskLevel: "dangerous",
        },
      ];
      const w = mountComposer();
      const vm = vmOf(w);
      vm.selectMention({ kind: "mcp", labelKey: "aiAssistant.mentionMcp" });
      vm.onKeydown(new KeyboardEvent("keydown", { key: "Enter" }));
      expect(vm.showMcpToolMenu).toBe(false);
      expect(storeFnsMock.addContextChip).toHaveBeenCalledWith(
        expect.objectContaining({ kind: "mcp", label: "mcp.s.do" }),
      );
    });

    it("Escape closes the MCP menu without inserting a chip", () => {
      mcpMock.agentToolsRef.value = [
        {
          namespace: "mcp.s.do",
          server: "s",
          tool: "do",
          description: "",
          riskLevel: "elevated",
        },
      ];
      const w = mountComposer();
      const vm = vmOf(w);
      vm.selectMention({ kind: "mcp", labelKey: "aiAssistant.mentionMcp" });
      vm.onKeydown(new KeyboardEvent("keydown", { key: "Escape" }));
      expect(vm.showMcpToolMenu).toBe(false);
      expect(storeFnsMock.addContextChip).not.toHaveBeenCalled();
    });

    it("renders empty hint when no tools available", async () => {
      const w = mountComposer();
      const vm = vmOf(w);
      vm.selectMention({ kind: "mcp", labelKey: "aiAssistant.mentionMcp" });
      await nextTick();
      const popup = w.find(".ai-input__popup--mcp");
      expect(popup.exists()).toBe(true);
      expect(popup.text()).toContain("aiAssistant.noMcpTools");
    });
  });

  // --- Task 5 Step 6: @Skill 手动激活 ---
  describe("@Skill picker (Task 5 Step 6)", () => {
    it("selectMention(skill) opens secondary menu and loads skills", () => {
      const w = mountComposer();
      const vm = vmOf(w);
      vm.selectMention({ kind: "skill", labelKey: "aiAssistant.mentionSkill" });
      expect(vm.showMentionMenu).toBe(false);
      expect(vm.showSkillMenu).toBe(true);
      expect(vm.skillIndex).toBe(0);
      expect(skillsMock.load).toHaveBeenCalled();
    });

    it("filteredSkills sorts by priority descending", () => {
      skillsMock.skillsListRef.value = [
        { id: "low", name: "Low", description: "", priority: 1, trigger: {}, systemPrompt: "low", scope: "user" },
        { id: "high", name: "High", description: "", priority: 10, trigger: {}, systemPrompt: "high", scope: "user" },
        { id: "mid", name: "Mid", description: "", priority: 5, trigger: {}, systemPrompt: "mid", scope: "user" },
      ];
      const w = mountComposer();
      const vm = vmOf(w);
      expect(vm.filteredSkills.map((s) => s.id)).toEqual(["high", "mid", "low"]);
    });

    it("selectSkill on user-scoped skill binds the current session without a dialog", async () => {
      skillsMock.skillsListRef.value = [
        { id: "user-skill", name: "User Skill", description: "A user skill", priority: 1, trigger: {}, systemPrompt: "You are a user skill.", scope: "user" },
      ];
      const w = mountComposer();
      const vm = vmOf(w);
      vm.selectMention({ kind: "skill", labelKey: "aiAssistant.mentionSkill" });
      await vm.selectSkill(0);
      expect(vm.showSkillMenu).toBe(false);
	  expect(skillsMock.activate).toHaveBeenCalledWith("user-skill", "chat-test");
      expect(storeFnsMock.addContextChip).toHaveBeenCalledWith(
        expect.objectContaining({
          kind: "skill",
          label: "User Skill",
          content: "You are a user skill.",
        }),
      );
    });

    it("selectSkill on project-scoped skill prompts for approval (G-SEC-03)", async () => {
      elMessageBoxConfirmMock.mockResolvedValueOnce("confirm");
      skillsMock.skillsListRef.value = [
        { id: "proj-skill", name: "Project Skill", description: "", priority: 1, trigger: {}, systemPrompt: "project prompt", scope: "project" },
      ];
      const w = mountComposer();
      const vm = vmOf(w);
      vm.selectMention({ kind: "skill", labelKey: "aiAssistant.mentionSkill" });
      await vm.selectSkill(0);
      expect(elMessageBoxConfirmMock).toHaveBeenCalled();
	  expect(skillsMock.activate).toHaveBeenCalledWith("proj-skill", "chat-test");
      expect(storeFnsMock.addContextChip).toHaveBeenCalledWith(
        expect.objectContaining({ kind: "skill", label: "Project Skill", content: "project prompt" }),
      );
    });

    it("selectSkill on project-scoped skill aborts if user rejects approval", async () => {
      elMessageBoxConfirmMock.mockRejectedValueOnce("cancel");
      skillsMock.skillsListRef.value = [
        { id: "proj-skill", name: "Project Skill", description: "", priority: 1, trigger: {}, systemPrompt: "project prompt", scope: "project" },
      ];
      const w = mountComposer();
      const vm = vmOf(w);
      vm.selectMention({ kind: "skill", labelKey: "aiAssistant.mentionSkill" });
      await vm.selectSkill(0);
      expect(elMessageBoxConfirmMock).toHaveBeenCalled();
      expect(skillsMock.activate).not.toHaveBeenCalled();
      expect(storeFnsMock.addContextChip).not.toHaveBeenCalled();
    });

    it("project-scoped skill approval is cached (no second confirm)", async () => {
      elMessageBoxConfirmMock.mockResolvedValueOnce("confirm");
      skillsMock.skillsListRef.value = [
        { id: "proj-skill", name: "Project Skill", description: "", priority: 1, trigger: {}, systemPrompt: "project prompt", scope: "project" },
      ];
      const w = mountComposer();
      const vm = vmOf(w);
      // First activation: prompts confirm + calls activate.
      vm.selectMention({ kind: "skill", labelKey: "aiAssistant.mentionSkill" });
      await vm.selectSkill(0);
      expect(elMessageBoxConfirmMock).toHaveBeenCalledTimes(1);
      expect(skillsMock.activate).toHaveBeenCalledTimes(1);
      storeFnsMock.addContextChip.mockClear();
      skillsMock.activate.mockClear();
      elMessageBoxConfirmMock.mockClear();
	  // Second activation: cached approval, no confirm, but the backend still
	  // refreshes the binding for the current Agent session.
      vm.selectMention({ kind: "skill", labelKey: "aiAssistant.mentionSkill" });
      await vm.selectSkill(0);
      expect(elMessageBoxConfirmMock).not.toHaveBeenCalled();
	  expect(skillsMock.activate).toHaveBeenCalledWith("proj-skill", "chat-test");
      expect(storeFnsMock.addContextChip).toHaveBeenCalled();
    });

    it("ArrowDown/Up navigate the skill list", () => {
      skillsMock.skillsListRef.value = [
        { id: "a", name: "A", description: "", priority: 5, trigger: {}, systemPrompt: "a", scope: "user" },
        { id: "b", name: "B", description: "", priority: 3, trigger: {}, systemPrompt: "b", scope: "user" },
      ];
      const w = mountComposer();
      const vm = vmOf(w);
      vm.selectMention({ kind: "skill", labelKey: "aiAssistant.mentionSkill" });
      expect(vm.skillIndex).toBe(0);
      vm.onKeydown(new KeyboardEvent("keydown", { key: "ArrowDown" }));
      expect(vm.skillIndex).toBe(1);
      vm.onKeydown(new KeyboardEvent("keydown", { key: "ArrowDown" }));
      expect(vm.skillIndex).toBe(0); // wraps
      vm.onKeydown(new KeyboardEvent("keydown", { key: "ArrowUp" }));
      expect(vm.skillIndex).toBe(1); // wraps back
    });

    it("Enter selects the highlighted skill", async () => {
      skillsMock.skillsListRef.value = [
        { id: "a", name: "Skill A", description: "", priority: 1, trigger: {}, systemPrompt: "prompt-a", scope: "user" },
      ];
      const w = mountComposer();
      const vm = vmOf(w);
      vm.selectMention({ kind: "skill", labelKey: "aiAssistant.mentionSkill" });
      vm.onKeydown(new KeyboardEvent("keydown", { key: "Enter" }));
      await nextTick();
      expect(vm.showSkillMenu).toBe(false);
      expect(storeFnsMock.addContextChip).toHaveBeenCalledWith(
        expect.objectContaining({ kind: "skill", label: "Skill A" }),
      );
    });

    it("Escape closes the skill menu without inserting a chip", () => {
      skillsMock.skillsListRef.value = [
        { id: "a", name: "A", description: "", priority: 1, trigger: {}, systemPrompt: "a", scope: "user" },
      ];
      const w = mountComposer();
      const vm = vmOf(w);
      vm.selectMention({ kind: "skill", labelKey: "aiAssistant.mentionSkill" });
      vm.onKeydown(new KeyboardEvent("keydown", { key: "Escape" }));
      expect(vm.showSkillMenu).toBe(false);
      expect(storeFnsMock.addContextChip).not.toHaveBeenCalled();
    });

    it("Enter does not trigger send when skill menu is open", () => {
      skillsMock.skillsListRef.value = [
        { id: "a", name: "A", description: "", priority: 1, trigger: {}, systemPrompt: "a", scope: "user" },
      ];
      const w = mountComposer();
      const vm = vmOf(w);
      vm.text = "hello";
      vm.selectMention({ kind: "skill", labelKey: "aiAssistant.mentionSkill" });
      vm.onKeydown(new KeyboardEvent("keydown", { key: "Enter" }));
      expect(storeFnsMock.sendMessage).not.toHaveBeenCalled();
    });

    it("renders empty hint when no skills available", async () => {
      const w = mountComposer();
      const vm = vmOf(w);
      vm.selectMention({ kind: "skill", labelKey: "aiAssistant.mentionSkill" });
      await nextTick();
      const popup = w.find(".ai-input__popup--skill");
      expect(popup.exists()).toBe(true);
      expect(popup.text()).toContain("aiAssistant.noSkillsAvailable");
    });
  });
});
