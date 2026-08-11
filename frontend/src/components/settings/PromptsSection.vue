<script setup lang="ts">
// Koyori IDE 组件 · Prompts Section；交互服务：AI 对话（AIService）。
// 喵，这是 Prompts Section，负责 Koyori IDE 的界面呈现喵~
import { ref, computed } from "vue";
import { appState, saveSettings } from "@/stores/app";
import { aiService } from "@/api/services";
import { notifySuccess, notifyError } from "@/lib/notifications";
import { errorMessage } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";

const { t } = useI18n();

// Tracks which prompt is currently being fetched from the backend.
const loadingPrompt = ref<string | null>(null);

interface PromptConfig {
  // Unique key for loading state tracking.
  key: string;
  // Display label for the prompt section.
  label: string;
  // Short description shown under the label.
  description: string;
  // The appState field to bind the textarea to (passed as a getter/setter pair).
  get: () => string;
  set: (v: string) => void;
  // The backend method that returns the built-in const for "Load Builtin".
  loadBuiltin: () => Promise<string>;
  // The builtin name shown in the "Load Builtin" button tooltip.
  builtinName: string;
}

// Build the list of configurable prompts. Each entry binds a textarea to an
// appState field and wires the "Load Builtin" / "Clear" buttons.
const prompts = computed<PromptConfig[]>(() => [
  {
    key: "default",
    label: t("prompts.defaultLabel"),
    description: t("prompts.defaultDesc"),
    get: () => appState.aiSystemPrompt,
    set: (v: string) => { appState.aiSystemPrompt = v; },
    loadBuiltin: () => aiService.getDefaultSystemPrompt(),
    builtinName: "DefaultSystemPrompt",
  },
  {
    key: "agent",
    label: t("prompts.agentLabel"),
    description: t("prompts.agentDesc"),
    get: () => appState.aiAgentSystemPrompt,
    set: (v: string) => { appState.aiAgentSystemPrompt = v; },
    loadBuiltin: () => aiService.getAgentSystemPrompt(),
    builtinName: "AgentSystemPrompt",
  },
  {
    key: "title",
    label: t("prompts.titleLabel"),
    description: t("prompts.titleDesc"),
    get: () => appState.aiConversationTitlePrompt,
    set: (v: string) => { appState.aiConversationTitlePrompt = v; },
    loadBuiltin: () => aiService.getConversationTitlePrompt(),
    builtinName: "ConversationTitlePrompt",
  },
  {
    key: "inline",
    label: t("prompts.inlineLabel"),
    description: t("prompts.inlineDesc"),
    get: () => appState.aiInlineCompletionPrompt,
    set: (v: string) => { appState.aiInlineCompletionPrompt = v; },
    loadBuiltin: () => aiService.getInlineCompletionSystemPrompt(),
    builtinName: "InlineCompletionSystemPrompt",
  },
]);

async function loadBuiltin(p: PromptConfig): Promise<void> {
  loadingPrompt.value = p.key;
  try {
    const text = await p.loadBuiltin();
    p.set(text);
    saveSettings();
    notifySuccess(t("prompts.loaded", { label: p.label }));
  } catch (e: unknown) {
    notifyError(
      t("prompts.loadFailed", { label: p.label, error: errorMessage(e) || t("prompts.unknownError") }),
    );
  } finally {
    loadingPrompt.value = null;
  }
}

function clearPrompt(p: PromptConfig): void {
  p.set("");
  saveSettings();
  notifySuccess(t("prompts.cleared", { label: p.label }));
}

// Open the backend docs (README) for prompt authoring guidance.
// Kept simple: no-op for now; the descriptions above already guide the user.
</script>

<template>
  <section class="settings-section prompts-section">
    <h2 class="section-title">{{ t("settings.prompts") }}</h2>
    <p class="section-hint">
      {{ t("prompts.hint") }}
    </p>

    <div v-for="p in prompts" :key="p.key" class="prompt-block">
      <div class="prompt-block__header">
        <div class="prompt-block__title">
          {{ p.label }}
          <span class="prompt-block__builtin">{{ p.builtinName }}</span>
        </div>
        <div class="prompt-block__actions">
          <el-button
            size="small"
            :loading="loadingPrompt === p.key"
            @click="loadBuiltin(p)"
          >
            {{ t("prompts.loadBuiltin") }}
          </el-button>
          <el-button
            size="small"
            @click="clearPrompt(p)"
            :disabled="p.get() === ''"
          >
            {{ t("prompts.clear") }}
          </el-button>
        </div>
      </div>
      <p class="prompt-block__desc">{{ p.description }}</p>
      <el-input
        :model-value="p.get()"
        type="textarea"
        :rows="8"
        :placeholder="t('prompts.emptyPlaceholder', { name: p.builtinName })"
        :aria-label="p.label"
        @update:model-value="(v: string) => { p.set(v); saveSettings(); }"
      />
      <div v-if="p.key === 'title' && p.get()" class="prompt-block__validate">
        <span v-if="p.get().includes('{{first_message}}')" class="prompt-block__ok">
          {{ t("prompts.containsFirstMessage") }}
        </span>
        <span v-else class="prompt-block__warn">
          {{ t("prompts.missingFirstMessage") }}
        </span>
      </div>
      <div v-if="p.key === 'inline' && p.get()" class="prompt-block__validate">
        <span v-if="p.get().includes('{{language}}')" class="prompt-block__ok">
          {{ t("prompts.containsLanguage") }}
        </span>
        <span v-else class="prompt-block__warn">
          {{ t("prompts.missingLanguage") }}
        </span>
      </div>
    </div>
  </section>
</template>

<style scoped>
.prompts-section {
  max-width: 720px;
}

.section-hint {
  font-size: 13px;
  color: var(--color-text-secondary);
  margin-bottom: 20px;
  line-height: 1.5;
}

.prompt-block {
  margin-bottom: 28px;
  padding: 16px;
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-sm);
  background: var(--color-bg-surface);
}

.prompt-block__header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 6px;
  gap: 12px;
}

.prompt-block__title {
  font-size: 14px;
  font-weight: 600;
  color: var(--color-text-primary);
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.prompt-block__builtin {
  font-family: var(--font-mono);
  font-size: 11px;
  font-weight: 400;
  color: var(--color-text-tertiary);
  background: var(--color-bg-surface-container);
  padding: 1px 6px;
  border-radius: var(--radius-xs);
}

.prompt-block__actions {
  display: flex;
  gap: 6px;
  flex-shrink: 0;
}

.prompt-block__desc {
  font-size: 12px;
  color: var(--color-text-secondary);
  margin: 0 0 10px 0;
  line-height: 1.5;
}

.prompt-block__validate {
  margin-top: 6px;
  font-size: 12px;
  font-family: var(--font-mono);
}

.prompt-block__ok {
  color: var(--color-success, #4caf50);
}

.prompt-block__warn {
  color: var(--color-warning, #ff9800);
}
</style>
