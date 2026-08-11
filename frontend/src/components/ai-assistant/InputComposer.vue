<script setup lang="ts">
// Koyori IDE 组件 · Input Composer。
// 喵，这是 Input Composer，负责 Koyori IDE 的界面呈现喵~
// Plan 11 Task 3enhanced input composer.
// Features: multi-line auto-resize textarea (Shift+Enter newline / Enter send),
// paste handling (image/file/codeblockcontext chips), @mention popup,
// slash-command popup, bottom toolbar (mode/persona/attach/send/stop), and
// live token count. Context chips are stored in aiState.contextChips and
// serialized by buildUserMessage in @/stores/ai.
import { ref, computed, nextTick, watch } from "vue";
import { ElMessageBox } from "element-plus";
import { useI18n } from "@/lib/i18n";
import {
  aiState,
  sendMessage,
  stopGeneration,
  addContextChip,
  removeContextChip,
  clearMessages,
} from "@/stores/ai";
import { aiAssistantState, switchMode } from "@/stores/aiAssistant";
import { appState } from "@/stores/app";
import { agentMcpTools, refreshAgentMcpTools } from "@/stores/mcp";
import { skillsList, loadSkills, activateSkill } from "@/stores/skills";
import { createPlan as createPlanFromStore } from "@/stores/aiPlan";
import type { ContextChipKind } from "@/types";

const { t } = useI18n();
const text = ref("");
const textareaRef = ref<HTMLTextAreaElement | null>(null);

// --- Slash command definitions (Step 6) ---
interface SlashCommand {
  cmd: string;
  descKey: string;
  mode?: "chat" | "plan" | "goal" | "agent";
  action?: "clear";
}
const SLASH_COMMANDS: SlashCommand[] = [
  { cmd: "/explain", descKey: "aiAssistant.slashExplain" },
  { cmd: "/refactor", descKey: "aiAssistant.slashRefactor" },
  { cmd: "/fix", descKey: "aiAssistant.slashFix" },
  { cmd: "/test", descKey: "aiAssistant.slashTest" },
  { cmd: "/doc", descKey: "aiAssistant.slashDoc" },
  { cmd: "/review", descKey: "aiAssistant.slashReview" },
  { cmd: "/security", descKey: "aiAssistant.slashSecurity" },
  { cmd: "/commit", descKey: "aiAssistant.slashCommit" },
  { cmd: "/plan", descKey: "aiAssistant.slashPlan", mode: "plan" },
  { cmd: "/goal", descKey: "aiAssistant.slashGoal", mode: "goal" },
  { cmd: "/agent", descKey: "aiAssistant.slashAgent", mode: "agent" },
  { cmd: "/clear", descKey: "aiAssistant.slashClear", action: "clear" },
  { cmd: "/model", descKey: "aiAssistant.slashModel" },
  { cmd: "/persona", descKey: "aiAssistant.slashPersona" },
];

// --- @ mention types (Step 5) ---
interface MentionType {
  kind: ContextChipKind;
  labelKey: string;
}
const MENTION_TYPES: MentionType[] = [
  { kind: "file", labelKey: "aiAssistant.mentionFile" },
  { kind: "symbol", labelKey: "aiAssistant.mentionSymbol" },
  { kind: "codebase", labelKey: "aiAssistant.mentionCodebase" },
  { kind: "gitdiff", labelKey: "aiAssistant.mentionGitdiff" },
  { kind: "web", labelKey: "aiAssistant.mentionWeb" },
  { kind: "docs", labelKey: "aiAssistant.mentionDocs" },
  { kind: "mcp", labelKey: "aiAssistant.mentionMcp" },
  { kind: "skill", labelKey: "aiAssistant.mentionSkill" },
  { kind: "persona", labelKey: "aiAssistant.mentionPersona" },
  { kind: "url", labelKey: "aiAssistant.mentionUrl" },
];

// --- Popup state ---
const showSlashMenu = ref(false);
const showMentionMenu = ref(false);
const slashIndex = ref(0);
const mentionIndex = ref(0);
// @MCP 二级菜单：选择 @mcp 后展示可MCP 工具列表（mcp.<server>.<tool>）
const showMcpToolMenu = ref(false);
const mcpToolIndex = ref(0);
// @Skill 二级菜单：选择 @skill 后展示所有已加载技能（Task 5 Step 6）
// 选择具体技能后，项目级未批准的会先弹确认（G-SEC-03），再添skill chip
const showSkillMenu = ref(false);
const skillIndex = ref(0);
// 已在前端确认批准的项目级技id 集合（避免重复弹窗；后端仍权威）
const approvedSkillIds = ref<Set<string>>(new Set());

const filteredSlashCommands = computed(() => {
  const match = text.value.match(/^\/(\w*)$/);
  if (!match) return [];
  const q = match[1].toLowerCase();
  return SLASH_COMMANDS.filter((c) => c.cmd.slice(1).includes(q));
});

// --- Auto-resize textarea (Step 1) ---
function autoResize(): void {
  const el = textareaRef.value;
  if (!el) return;
  el.style.height = "auto";
  // Clamp to 32 rows (~60px88px).
  const h = Math.min(Math.max(el.scrollHeight, 60), 288);
  el.style.height = `${h}px`;
}

watch(text, () => {
  void nextTick(autoResize);
});

// --- Token count (Step 9)frontend heuristic matching token_estimator.go ---
function estimateTokensLocal(s: string): number {
  if (!s) return 0;
  const chars = [...s];
  let cjk = 0;
  for (const ch of chars) {
    const code = ch.codePointAt(0) ?? 0;
    if (
      (code >= 0x4e00 && code <= 0x9fff) ||
      (code >= 0x3040 && code <= 0x30ff) ||
      (code >= 0xac00 && code <= 0xd7af) ||
      (code >= 0xff00 && code <= 0xffef)
    ) {
      cjk++;
    }
  }
  const total = chars.length;
  return Math.floor(cjk / 2) + Math.floor((total - cjk) / 4);
}

const tokenCount = computed(() => {
  let total = estimateTokensLocal(text.value);
  for (const chip of aiState.contextChips) {
    if (chip.content) total += estimateTokensLocal(chip.content);
  }
  return total;
});

// --- Keydown handling (Step 1 + popup nav) ---
function onKeydown(e: KeyboardEvent): void {
  // Enter = send, Shift+Enter = newline. 当任意弹出菜单（slash/mention/mcp/skill
  // 打开时，Enter/Tab 用于选择菜单项，不触发发送
  if (e.key === "Enter" && !e.shiftKey && !showSlashMenu.value && !showMentionMenu.value && !showMcpToolMenu.value && !showSkillMenu.value) {
    e.preventDefault();
    handleSend();
    return;
  }
  // Slash command popup: arrow up/down + Tab/Enter to select, Esc to close.
  if (showSlashMenu.value) {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      slashIndex.value = (slashIndex.value + 1) % filteredSlashCommands.value.length;
      return;
    }
    if (e.key === "ArrowUp") {
      e.preventDefault();
      slashIndex.value =
        (slashIndex.value - 1 + filteredSlashCommands.value.length) % filteredSlashCommands.value.length;
      return;
    }
    if (e.key === "Tab" || (e.key === "Enter" && filteredSlashCommands.value.length > 0)) {
      e.preventDefault();
      selectSlash(filteredSlashCommands.value[slashIndex.value]);
      return;
    }
    if (e.key === "Escape") {
      e.preventDefault();
      showSlashMenu.value = false;
      return;
    }
  }
  // @ mention popup nav.
  if (showMentionMenu.value) {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      mentionIndex.value = (mentionIndex.value + 1) % MENTION_TYPES.length;
      return;
    }
    if (e.key === "ArrowUp") {
      e.preventDefault();
      mentionIndex.value = (mentionIndex.value - 1 + MENTION_TYPES.length) % MENTION_TYPES.length;
      return;
    }
    if (e.key === "Tab" || e.key === "Enter") {
      e.preventDefault();
      selectMention(MENTION_TYPES[mentionIndex.value]);
      return;
    }
    if (e.key === "Escape") {
      e.preventDefault();
      showMentionMenu.value = false;
      return;
    }
  }
  // @MCP 工具二级菜单导航（Task 4 Step 7）
  if (showMcpToolMenu.value) {
    const count = filteredMcpTools.value.length;
    if (e.key === "ArrowDown" && count > 0) {
      e.preventDefault();
      mcpToolIndex.value = (mcpToolIndex.value + 1) % count;
      return;
    }
    if (e.key === "ArrowUp" && count > 0) {
      e.preventDefault();
      mcpToolIndex.value = (mcpToolIndex.value - 1 + count) % count;
      return;
    }
    if ((e.key === "Tab" || e.key === "Enter") && count > 0) {
      e.preventDefault();
      selectMcpTool(mcpToolIndex.value);
      return;
    }
    if (e.key === "Escape") {
      e.preventDefault();
      showMcpToolMenu.value = false;
      return;
    }
  }
  // @Skill 二级菜单导航（Task 5 Step 6）
  if (showSkillMenu.value) {
    const count = filteredSkills.value.length;
    if (e.key === "ArrowDown" && count > 0) {
      e.preventDefault();
      skillIndex.value = (skillIndex.value + 1) % count;
      return;
    }
    if (e.key === "ArrowUp" && count > 0) {
      e.preventDefault();
      skillIndex.value = (skillIndex.value - 1 + count) % count;
      return;
    }
    if ((e.key === "Tab" || e.key === "Enter") && count > 0) {
      e.preventDefault();
      void selectSkill(skillIndex.value);
      return;
    }
    if (e.key === "Escape") {
      e.preventDefault();
      showSkillMenu.value = false;
      return;
    }
  }
}

// --- Input handler: detect / and @ triggers ---
function onInput(): void {
  // Slash menu: only when text starts with / and has no spaces.
  const slashMatch = text.value.match(/^\/(\w*)$/);
  showSlashMenu.value = slashMatch !== null && filteredSlashCommands.value.length > 0;
  if (showSlashMenu.value) slashIndex.value = 0;

  // @ mention: detect @ at end of input (after space or at start).
  const atMatch = text.value.match(/(?:^|\s)@$/);
  showMentionMenu.value = atMatch !== null;
  if (showMentionMenu.value) {
    mentionIndex.value = 0;
    // Remove the trailing @ so it doesn't end up in the message.
    text.value = text.value.replace(/@$/, "");
  }
}

// --- Paste handling (Step 2/3/4) ---
function detectLanguage(code: string): string {
  if (/^\s*(func |package )/m.test(code)) return "go";
  if (/^\s*(import |export |const |let |function |class )/m.test(code)) return "typescript";
  if (/<template>|<\/template>/.test(code)) return "vue";
  if (/^\s*(def |class |import )/m.test(code)) return "python";
  return "text";
}

function onPaste(e: ClipboardEvent): void {
  const cl = e.clipboardData;
  if (!cl) return;
  // Image paste (Step 3).
  for (const item of Array.from(cl.items)) {
    if (item.kind === "file" && item.type.startsWith("image/")) {
      e.preventDefault();
      const file = item.getAsFile();
      if (!file) continue;
      const reader = new FileReader();
      reader.onload = () => {
        addContextChip({
          id: makeChipId(),
          kind: "image",
          label: file.name || "pasted-image",
          imageUrl: reader.result as string,
        });
      };
      reader.readAsDataURL(file);
      return;
    }
  }
  // Text pastedetect code block (Step 4).
  const pastedText = cl.getData("text");
  if (pastedText.includes("```") || pastedText.split("\n").length > 3) {
    e.preventDefault();
    addContextChip({
      id: makeChipId(),
      kind: "codeblock",
      label: t("aiAssistant.pastedCode"),
      content: pastedText,
      language: detectLanguage(pastedText),
    });
    return;
  }
  // Otherwise: let the default paste happen (plain text into textarea).
}

function makeChipId(): string {
  // crypto.randomUUID is available in modern browsers + Wails webview.
  // Fallback to timestamp+random for older environments.
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  return `chip-${Date.now()}-${Math.floor(Math.random() * 10000)}`;
}

// --- Slash command selection (Step 6) ---
function selectSlash(cmd: SlashCommand): void {
  showSlashMenu.value = false;
  if (cmd.action === "clear") {
    clearMessages();
    text.value = "";
    return;
  }
  if (cmd.mode) {
    switchMode(cmd.mode);
    text.value = "";
    return;
  }
  // For prompt-style commands, insert the command + space for the user to
  // add their target text. The actual instruction text is fetched by the
  // backend's getPresetPrompt when the message is sent via runAIAction.
  text.value = cmd.cmd + " ";
  void nextTick(() => {
    textareaRef.value?.focus();
    autoResize();
  });
}

// --- @ mention selection (Step 5) ---
function selectMention(m: MentionType): void {
  showMentionMenu.value = false;
  // @MCP：打开二级菜单展示可用 MCP 工具（mcp.<server>.<tool>）
  // 工具列表来自 stores/mcp（由后端 ListAgentMCPTools 提供）。选择具体
  // 工具后才插入 chip，避免插入无意义的通用 @mcp chip
  if (m.kind === "mcp") {
    showMcpToolMenu.value = true;
    mcpToolIndex.value = 0;
    void refreshAgentMcpTools();
    return;
  }
  // @Skill：打开二级菜单展示所有已加载技能（Task 5 Step 6）
  // 技能列表来stores/skills（由后端 ListSkills 提供）。选择具体
  // 技能后，项目级未批准的会先弹确认（G-SEC-03），再添skill chip
  if (m.kind === "skill") {
    showSkillMenu.value = true;
    skillIndex.value = 0;
    void loadSkills();
    return;
  }
  addContextChip({
    id: makeChipId(),
    kind: m.kind,
    label: t(m.labelKey),
  });
}

// @MCP 工具选择：插入带命名空间chip（Task 4 Step 7）
function selectMcpTool(idx: number): void {
  const tool = filteredMcpTools.value[idx];
  if (!tool) return;
  showMcpToolMenu.value = false;
  addContextChip({
    id: makeChipId(),
    kind: "mcp",
    label: tool.namespace,
    content: tool.description,
  });
}

// MCP 工具列表（来store，二级菜单展示）。无工具时显示空提示
const filteredMcpTools = computed(() => agentMcpTools.value);

// Skill 列表（来store，二级菜单展示，Task 5 Step 6）
// 按优先级降序排列，与后端 MergeSystemPrompts 顺序一致
const filteredSkills = computed(() =>
  [...skillsList.value].sort((a, b) => b.priority - a.priority),
);

/**
 * @Skill 选择：添加 skill chip（Task 5 Step 6）。
 * G-SEC-03：项目级未批准的技能首次选择时弹确认对话框，确认后调用
 * ActivateSkill 标记批准，再添加 chip。用户级/全局技能直接添加 chip。
 * 拒绝批准则不添加 chip（避免注入未审计的 SystemPrompt）。
 */
async function selectSkill(idx: number): Promise<void> {
  const sk = filteredSkills.value[idx];
  if (!sk) return;
  showSkillMenu.value = false;
  // G-SEC-03：项目级技能需用户显式批准
  if (sk.scope === "project" && !approvedSkillIds.value.has(sk.id)) {
    try {
      await ElMessageBox.confirm(
        t("skillsSection.approveConfirm", { name: sk.name }),
        t("common.confirm"),
        { type: "warning", confirmButtonText: t("common.confirm"), cancelButtonText: t("common.cancel") },
      );
    } catch {
      return;
    }
    const success = await activateSkill(sk.id);
    if (!success) return;
    approvedSkillIds.value.add(sk.id);
  }
  addContextChip({
    id: makeChipId(),
    kind: "skill",
    label: sk.name,
    content: sk.systemPrompt,
  });
}

// --- Send ---
// BUG4: plan 模式下发送消息 = 用消息内容作为 goal 创建 Plan。
// 这样用户在主输入框输入目标即可创建 Plan，无需手动到右侧面板填写。
async function handleSend(): Promise<void> {
  const content = text.value.trim();
  if (!content || aiState.streaming || aiState.globalStreamBusy) return;
  text.value = "";
  showSlashMenu.value = false;
  showMentionMenu.value = false;
  showMcpToolMenu.value = false;
  showSkillMenu.value = false;
  void nextTick(autoResize);

  // plan 模式：把消息当作 goal 创建 Plan。
  if (aiAssistantState.mode === "plan") {
    const id = `plan-${Date.now()}`;
    // Do not turn the goal into an echo command. Until AI plan generation is
    // wired here, the user must add explicit steps through Replan.
    await createPlanFromStore(id, content, []);
    return;
  }

  void sendMessage(content);
}

function handleStop(): void {
  void stopGeneration();
}

function handleAttach(): void {
  // Trigger @ mention menu as the attach entry point.
  showMentionMenu.value = true;
  mentionIndex.value = 0;
}

function handleRemoveChip(id: string): void {
  removeContextChip(id);
}

// Exposed for unit tests.
defineExpose({
  text,
  showSlashMenu,
  showMentionMenu,
  showMcpToolMenu,
  showSkillMenu,
  slashIndex,
  mentionIndex,
  mcpToolIndex,
  skillIndex,
  filteredSlashCommands,
  filteredMcpTools,
  filteredSkills,
  tokenCount,
  handleSend,
  handleStop,
  handleAttach,
  handleRemoveChip,
  selectSlash,
  selectMention,
  selectMcpTool,
  selectSkill,
  onKeydown,
  onInput,
  onPaste,
  detectLanguage,
  estimateTokensLocal,
  autoResize,
});
</script>

<template>
  <div class="ai-input">
    <!-- Context chips preview row (Step 8) -->
    <div v-if="aiState.contextChips.length > 0" class="ai-input__chips">
      <span
        v-for="chip in aiState.contextChips"
        :key="chip.id"
        class="ai-input__chip"
        :class="`ai-input__chip--${chip.kind}`"
      >
        <img
          v-if="chip.kind === 'image' && chip.imageUrl"
          :src="chip.imageUrl"
          :alt="chip.label"
          class="ai-input__chip-img"
        />
        <span class="ai-input__chip-label">{{ chip.label }}</span>
        <button
          class="ai-input__chip-remove"
          type="button"
          :aria-label="t('common.remove')"
          :title="t('common.remove')"
          @click="handleRemoveChip(chip.id)"
        >×</button>
      </span>
    </div>

    <textarea
      ref="textareaRef"
      v-model="text"
      class="ai-input__textarea"
      :placeholder="t('aiAssistant.inputPlaceholder')"
      rows="3"
      @keydown="onKeydown"
      @input="onInput"
      @paste="onPaste"
    />

    <!-- Slash command popup (Step 6) -->
    <ul v-if="showSlashMenu && filteredSlashCommands.length > 0" class="ai-input__popup ai-input__popup--slash" role="listbox" :aria-label="t('aiAssistant.slashMenuAria')">
      <li
        v-for="(cmd, i) in filteredSlashCommands"
        :key="cmd.cmd"
        :class="{ 'ai-input__popup-item--active': i === slashIndex }"
        role="option"
        tabindex="0"
        :aria-selected="i === slashIndex"
        @click="selectSlash(cmd)"
        @keydown.enter.prevent="selectSlash(cmd)"
        @keydown.space.prevent="selectSlash(cmd)"
        @mouseenter="slashIndex = i"
      >
        <code>{{ cmd.cmd }}</code>
        <span class="ai-input__popup-desc">{{ t(cmd.descKey) }}</span>
      </li>
    </ul>

    <!-- @ mention popup (Step 5) -->
    <ul v-if="showMentionMenu" class="ai-input__popup ai-input__popup--mention" role="listbox" :aria-label="t('aiAssistant.mentionMenuAria')">
      <li
        v-for="(m, i) in MENTION_TYPES"
        :key="m.kind"
        :class="{ 'ai-input__popup-item--active': i === mentionIndex }"
        role="option"
        tabindex="0"
        :aria-selected="i === mentionIndex"
        @click="selectMention(m)"
        @keydown.enter.prevent="selectMention(m)"
        @keydown.space.prevent="selectMention(m)"
        @mouseenter="mentionIndex = i"
      >
        <span class="ai-input__mention-kind">{{ m.kind }}</span>
        <span class="ai-input__popup-desc">{{ t(m.labelKey) }}</span>
      </li>
    </ul>

    <!-- @MCP 工具二级菜单（Task 4 Step 7）-->
    <ul
      v-if="showMcpToolMenu"
      class="ai-input__popup ai-input__popup--mcp"
      role="listbox"
      :aria-label="t('aiAssistant.mcpMenuAria')"
    >
      <li v-if="filteredMcpTools.length === 0" class="ai-input__popup-empty">
        {{ t("aiAssistant.noMcpTools") }}
      </li>
      <li
        v-for="(tool, i) in filteredMcpTools"
        :key="tool.namespace"
        :class="{ 'ai-input__popup-item--active': i === mcpToolIndex }"
        role="option"
        tabindex="0"
        :aria-selected="i === mcpToolIndex"
        @click="selectMcpTool(i)"
        @keydown.enter.prevent="selectMcpTool(i)"
        @keydown.space.prevent="selectMcpTool(i)"
        @mouseenter="mcpToolIndex = i"
      >
        <span class="ai-input__mention-kind">{{ tool.namespace }}</span>
        <span v-if="tool.description" class="ai-input__popup-desc">{{ tool.description }}</span>
      </li>
    </ul>

    <!-- @Skill 二级菜单（Task 5 Step 6）-->
    <ul
      v-if="showSkillMenu"
      class="ai-input__popup ai-input__popup--skill"
      role="listbox"
      :aria-label="t('aiAssistant.skillMenuAria')"
    >
      <li v-if="filteredSkills.length === 0" class="ai-input__popup-empty">
        {{ t("aiAssistant.noSkillsAvailable") }}
      </li>
      <li
        v-for="(sk, i) in filteredSkills"
        :key="sk.id"
        :class="{ 'ai-input__popup-item--active': i === skillIndex }"
        role="option"
        tabindex="0"
        :aria-selected="i === skillIndex"
        @click="selectSkill(i)"
        @keydown.enter.prevent="selectSkill(i)"
        @keydown.space.prevent="selectSkill(i)"
        @mouseenter="skillIndex = i"
      >
        <span class="ai-input__mention-kind">{{ sk.name }}</span>
        <span class="ai-input__skill-scope">{{ sk.scope }}</span>
        <span v-if="sk.description" class="ai-input__popup-desc">{{ sk.description }}</span>
      </li>
    </ul>

    <!-- Bottom toolbar (Step 7) -->
    <div class="ai-input__bar">
      <div class="ai-input__toolbar">
        <select
          class="ai-input__select"
          :value="aiAssistantState.mode"
          :title="t('aiAssistant.mode')"
          @change="switchMode(($event.target as HTMLSelectElement).value as 'chat' | 'plan' | 'goal' | 'agent')"
        >
          <option value="chat">{{ t("aiAssistant.modeChat") }}</option>
          <option value="plan">{{ t("aiAssistant.modePlan") }}</option>
          <option value="goal">{{ t("aiAssistant.modeGoal") }}</option>
          <option value="agent">{{ t("aiAssistant.modeAgent") }}</option>
        </select>
        <span class="ai-input__model" :title="appState.aiModel || ''">
          {{ appState.aiModel || t("aiAssistant.noModel") }}
        </span>
        <button
          type="button"
          class="ai-input__tool-btn"
          :title="t('aiAssistant.attach')"
          :aria-label="t('a11y.attachContext')"
          @click="handleAttach"
        >@</button>
      </div>
      <div class="ai-input__right">
        <span class="ai-input__token-count" :title="t('aiAssistant.tokenCount')">
          {{ tokenCount }} {{ t("aiAssistant.tokens") }}
        </span>
        <span class="ai-input__hint">{{ t("aiAssistant.enterToSend") }}</span>
        <button
          v-if="!aiState.streaming"
          class="ai-input__send"
          :disabled="!text.trim() || aiState.globalStreamBusy"
          :title="aiState.globalStreamBusy ? t('aiChat.streamBusy') : t('aiChat.sendMessage')"
          @click="handleSend"
        >
          {{ t("aiAssistant.send") }}
        </button>
        <button
          v-else
          class="ai-input__stop"
          @click="handleStop"
        >
          {{ t("aiAssistant.stop") }}
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.ai-input {
  border-top: 1px solid var(--color-border-subtle, #2a2a2a);
  padding: 8px 12px;
  background: var(--color-bg-surface, #1e1e1e);
  flex-shrink: 0;
  position: relative;
}
.ai-input__chips {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  margin-bottom: 6px;
}
.ai-input__chip {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 2px 6px;
  font-size: 11px;
  background: var(--color-bg-elevated, #252525);
  border: 1px solid var(--color-border-default, #3a3a3a);
  border-radius: 4px;
  color: var(--color-text-primary, #e0e0e0);
}
.ai-input__chip-img {
  max-width: 28px;
  max-height: 20px;
  border-radius: 2px;
}
.ai-input__chip-label {
  max-width: 120px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ai-input__chip-remove {
  border: none;
  background: transparent;
  color: var(--color-text-secondary, #888);
  cursor: pointer;
  font-size: 14px;
  line-height: 1;
  padding: 0 2px;
}
.ai-input__chip-remove:hover {
  color: var(--color-danger, #ef4444);
}
.ai-input__textarea {
  width: 100%;
  resize: none;
  min-height: 60px;
  max-height: 288px;
  padding: 8px;
  font-size: 13px;
  font-family: inherit;
  background: var(--color-bg-elevated, #252525);
  color: var(--color-text-primary, #e0e0e0);
  border: 1px solid var(--color-border-default, #3a3a3a);
  border-radius: 6px;
  box-sizing: border-box;
  overflow-y: auto;
}
.ai-input__popup {
  position: absolute;
  bottom: 100%;
  left: 12px;
  right: 12px;
  max-height: 200px;
  overflow-y: auto;
  list-style: none;
  margin: 0;
  padding: 4px 0;
  background: var(--color-bg-elevated, #2a2a2a);
  border: 1px solid var(--color-border-default, #3a3a3a);
  border-radius: 6px;
  box-shadow: 0 -4px 12px rgba(0, 0, 0, 0.3);
  z-index: 100;
}
.ai-input__popup li {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 12px;
  font-size: 12px;
  color: var(--color-text-primary, #e0e0e0);
  cursor: pointer;
}
.ai-input__popup li:hover,
.ai-input__popup-item--active {
  background: var(--color-accent, #3b82f6);
  color: #fff;
}
.ai-input__popup li:focus-visible,
.ai-input__chip-remove:focus-visible {
  outline: 2px solid var(--color-primary-focus);
  outline-offset: -2px;
}
.ai-input__popup code {
  font-size: 12px;
  font-weight: 500;
}
.ai-input__popup-desc {
  color: var(--color-text-secondary, #aaa);
  font-size: 11px;
}
.ai-input__popup li:hover .ai-input__popup-desc,
.ai-input__popup-item--active .ai-input__popup-desc {
  color: rgba(255, 255, 255, 0.85);
}
.ai-input__mention-kind {
  font-size: 11px;
  font-weight: 500;
  text-transform: uppercase;
  min-width: 60px;
}
.ai-input__bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-top: 6px;
  gap: 8px;
}
.ai-input__toolbar {
  display: flex;
  align-items: center;
  gap: 6px;
}
.ai-input__select {
  padding: 2px 4px;
  font-size: 11px;
  background: var(--color-bg-elevated, #252525);
  color: var(--color-text-primary, #e0e0e0);
  border: 1px solid var(--color-border-default, #3a3a3a);
  border-radius: 4px;
}
.ai-input__model {
  font-size: 11px;
  color: var(--color-text-secondary, #888);
  max-width: 120px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ai-input__tool-btn {
  padding: 2px 8px;
  font-size: 13px;
  border: 1px solid var(--color-border-default, #3a3a3a);
  border-radius: 4px;
  background: transparent;
  color: var(--color-text-secondary, #aaa);
  cursor: pointer;
}
.ai-input__tool-btn:hover {
  border-color: var(--color-accent, #3b82f6);
  color: var(--color-text-primary, #e0e0e0);
}
.ai-input__right {
  display: flex;
  align-items: center;
  gap: 8px;
}
.ai-input__token-count {
  font-size: 10px;
  color: var(--color-text-secondary, #666);
}
.ai-input__hint {
  font-size: 11px;
  color: var(--color-text-secondary, #888);
}
.ai-input__send {
  padding: 4px 16px;
  font-size: 12px;
  border: none;
  border-radius: 6px;
  background: var(--color-accent, #3b82f6);
  color: #fff;
  cursor: pointer;
}
.ai-input__send:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}
.ai-input__stop {
  padding: 4px 16px;
  font-size: 12px;
  border: none;
  border-radius: 6px;
  background: var(--color-danger, #ef4444);
  color: #fff;
  cursor: pointer;
}
</style>
