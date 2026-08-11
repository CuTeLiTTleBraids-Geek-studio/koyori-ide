<script setup lang="ts">
// Koyori IDE 组件 · Ai Section；交互服务：AI 对话（AIService）、设置（SettingsService）。
// 喵，这是 Ai Section，负责 Koyori IDE 的界面呈现喵~
import { ref, watch, onMounted, onBeforeUnmount, computed } from "vue";
import {
  appState,
  activateAIConfig,
  saveAIConfig,
  deleteAIConfig,
  createNewAIConfig,
  saveSettings,
} from "@/stores/app";
import type { AIProviderConfig } from "@/types";
import {
  PROVIDER_PRESETS,
  getProviderPreset,
  getSuggestedModels,
  normalizeAiBaseUrl,
} from "@/lib/aiProviders";
import { aiService, settingsService } from "@/api/services";
import { notifySuccess, notifyError } from "@/lib/notifications";
import { errorMessage } from "@/lib/errors";
import { Hide, View, Lock, Unlock, Plus, Edit, Delete, Check, Refresh } from "@element-plus/icons-vue";
import { useI18n } from "@/lib/i18n";

const { t } = useI18n();

interface DraftConfig {
  id: string;
  name: string;
  provider: string;
  protocol: string;
  apiKey: string;
  apiKeyConfigured: boolean;
  baseUrl: string;
  model: string;
  temperature: number;
  maxTokens: number;
  systemPrompt: string;
}

interface PortableAIConfigJson {
  name?: string;
  provider?: string;
  protocol?: string;
  baseUrl?: string;
  model?: string;
  temperature?: number;
  maxTokens?: number;
  systemPrompt?: string;
}

const showApiKey = ref(false);
const testingConnection = ref(false);
const testResult = ref<string | null>(null);
const loadingPrompt = ref<"default" | "agent" | null>(null);
const apiKeyStorageMethod = ref<string>("none");
let encryptionRefreshTimer: ReturnType<typeof setTimeout> | null = null;
const editingConfigId = ref<string | null>(null);
const editingDraft = ref<DraftConfig | null>(null);
const editingOriginalApiKey = ref<string>("");
const remoteModels = ref<string[]>([]);
const fetchingModels = ref(false);
const autoFetchModels = ref(true);
const configJsonText = ref("");
let modelFetchTimer: ReturnType<typeof setTimeout> | null = null;
let suppressProviderAutoFill = false;
let suppressModelAutoFetch = false;

const presetBaseUrls = new Set(
  PROVIDER_PRESETS.map((p) => p.baseUrl).filter((u) => u !== ""),
);

async function refreshApiKeyStorageMethod() {
  try {
    apiKeyStorageMethod.value = await settingsService.getAPIKeyStorageMethod();
  } catch {
    apiKeyStorageMethod.value = "none";
  }
}

onMounted(refreshApiKeyStorageMethod);

function saveSettingsAndRefreshEncryption() {
  saveSettings();
  if (encryptionRefreshTimer !== null) {
    clearTimeout(encryptionRefreshTimer);
  }
  encryptionRefreshTimer = setTimeout(() => {
    encryptionRefreshTimer = null;
    void refreshApiKeyStorageMethod();
  }, 800);
}

onBeforeUnmount(() => {
  if (encryptionRefreshTimer !== null) {
    clearTimeout(encryptionRefreshTimer);
    encryptionRefreshTimer = null;
  }
  if (modelFetchTimer !== null) {
    clearTimeout(modelFetchTimer);
    modelFetchTimer = null;
  }
});

const apiKeyEncryptionLabel = computed(() => {
  switch (apiKeyStorageMethod.value) {
    case "dpapi": return t("aiSection.encryptedDpapi");
    case "aes": return t("aiSection.encryptedAes");
    case "plain": return t("aiSection.notEncrypted");
    default: return "";
  }
});

const apiKeyEncrypted = computed(() =>
  apiKeyStorageMethod.value === "dpapi" || apiKeyStorageMethod.value === "aes",
);

const draft = computed(() => editingDraft.value as DraftConfig);

const baseUrlPlaceholder = computed(() =>
  draft.value?.protocol === "anthropic"
    ? t("aiSection.baseUrlAnthropicPlaceholder")
    : t("aiSection.baseUrlOpenaiPlaceholder"),
);

const apiKeyPlaceholder = computed(() =>
  editingOriginalApiKey.value
    ? t("aiSection.apiKeyConfigured")
    : "sk-...",
);

const protocolDisabled = computed(() => draft.value?.provider === "anthropic");

const activeProviderPreset = computed(() =>
  getProviderPreset(editingDraft.value?.provider ?? ""),
);

const providerNotes = computed(() => {
  const key = activeProviderPreset.value?.notesKey;
  return key ? t(key) : "";
});

const protocolHint = computed(() =>
  editingDraft.value?.protocol === "anthropic"
    ? t("aiSection.protocolHintAnthropic")
    : t("aiSection.protocolHintOpenai"),
);

const endpointPreview = computed(() => {
  if (!editingDraft.value) return "";
  const base = normalizeAiBaseUrl(editingDraft.value.baseUrl || "");
  if (!base) return "";
  if (editingDraft.value.protocol === "anthropic") {
    return `${base}/v1/messages`;
  }
  return `${base}/v1/chat/completions`;
});

const modelOptions = computed(() => {
  const set = new Set<string>();
  for (const m of remoteModels.value) set.add(m);
  for (const m of getSuggestedModels(editingDraft.value?.provider ?? "")) set.add(m);
  if (editingDraft.value?.model) set.add(editingDraft.value.model);
  return [...set];
});

function providerLabel(providerId: string): string {
  return getProviderPreset(providerId)?.label ?? providerId;
}

function syncConfigJsonFromDraft(): void {
  if (!editingDraft.value) {
    configJsonText.value = "";
    return;
  }
  const portable: PortableAIConfigJson = {
    name: editingDraft.value.name,
    provider: editingDraft.value.provider,
    protocol: editingDraft.value.protocol,
    baseUrl: normalizeAiBaseUrl(editingDraft.value.baseUrl),
    model: editingDraft.value.model,
    temperature: editingDraft.value.temperature,
    maxTokens: editingDraft.value.maxTokens,
    systemPrompt: editingDraft.value.systemPrompt,
  };
  configJsonText.value = JSON.stringify(portable, null, 2);
}

watch(
  () => editingDraft.value?.provider,
  (newProvider, oldProvider) => {
    if (suppressProviderAutoFill) {
      suppressProviderAutoFill = false;
      return;
    }
    if (!editingDraft.value || newProvider === oldProvider) return;
    const preset = getProviderPreset(newProvider ?? "");
    if (!preset) return;
    const currentUrl = editingDraft.value.baseUrl.trim();
    if (currentUrl === "" || presetBaseUrls.has(currentUrl)) {
      editingDraft.value.baseUrl = preset.baseUrl;
    }
    if (preset.protocol) {
      editingDraft.value.protocol = preset.protocol;
    }
    remoteModels.value = [...(preset.models ?? [])];
    syncConfigJsonFromDraft();
    scheduleAutoFetchModels();
  },
);

watch(
  () => [
    editingDraft.value?.name,
    editingDraft.value?.protocol,
    editingDraft.value?.baseUrl,
    editingDraft.value?.model,
    editingDraft.value?.temperature,
    editingDraft.value?.maxTokens,
    editingDraft.value?.systemPrompt,
  ],
  () => {
    if (editingDraft.value) syncConfigJsonFromDraft();
  },
);

watch(
  () => editingDraft.value?.baseUrl,
  (next, prev) => {
    if (suppressModelAutoFetch) return;
    if (!editingDraft.value || next === prev) return;
    scheduleAutoFetchModels();
  },
);

function scheduleAutoFetchModels(): void {
  if (!autoFetchModels.value || !editingDraft.value) return;
  if (modelFetchTimer !== null) clearTimeout(modelFetchTimer);
  modelFetchTimer = setTimeout(() => {
    modelFetchTimer = null;
    void handleFetchModels(true);
  }, 600);
}

function handleNewConfig() {
  const cfg = createNewAIConfig();
  saveAIConfig(cfg);
  activateAIConfig(cfg.id);
  suppressProviderAutoFill = true;
  suppressModelAutoFetch = true;
  editingDraft.value = normalizeDraft(cfg);
  editingConfigId.value = cfg.id;
  editingOriginalApiKey.value = "";
  testResult.value = null;
  showApiKey.value = false;
  remoteModels.value = [...getSuggestedModels(cfg.provider)];
  syncConfigJsonFromDraft();
  suppressModelAutoFetch = false;
  scheduleAutoFetchModels();
}

function handleEdit(id: string) {
  const cfg = appState.aiProviderConfigs.find((c) => c.id === id);
  if (!cfg) return;
  suppressProviderAutoFill = true;
  suppressModelAutoFetch = true;
  editingDraft.value = normalizeDraft(cfg);
  editingOriginalApiKey.value = cfg.apiKeyConfigured ? "___stored___" : "";
  editingDraft.value.apiKey = "";
  editingConfigId.value = id;
  testResult.value = null;
  showApiKey.value = false;
  remoteModels.value = [...getSuggestedModels(cfg.provider)];
  syncConfigJsonFromDraft();
  suppressModelAutoFetch = false;
  scheduleAutoFetchModels();
}

function handleSetActive(id: string) {
  activateAIConfig(id);
}

function handleDelete(id: string) {
  if (appState.aiProviderConfigs.length <= 1) {
    notifyError(t("aiSection.deleteFailed"));
    return;
  }
  if (editingConfigId.value === id) {
    editingConfigId.value = null;
    editingDraft.value = null;
    testResult.value = null;
    remoteModels.value = [];
    configJsonText.value = "";
  }
  const ok = deleteAIConfig(id);
  if (!ok) {
    notifyError(t("aiSection.deleteFailed"));
  }
}

function normalizeDraft(cfg: AIProviderConfig): DraftConfig {
  return {
    id: cfg.id,
    name: cfg.name,
    provider: cfg.provider,
    protocol: cfg.protocol ?? "openai",
    apiKey: cfg.apiKey,
    apiKeyConfigured: cfg.apiKeyConfigured ?? false,
    baseUrl: normalizeAiBaseUrl(cfg.baseUrl ?? ""),
    model: cfg.model,
    temperature: cfg.temperature ?? 0.7,
    maxTokens: cfg.maxTokens ?? 4096,
    systemPrompt: cfg.systemPrompt ?? "",
  };
}

function handleSave() {
  if (!editingDraft.value) return;
  editingDraft.value.baseUrl = normalizeAiBaseUrl(editingDraft.value.baseUrl);
  if (!editingDraft.value.apiKey && editingOriginalApiKey.value) {
    editingDraft.value.apiKeyConfigured = true;
  } else if (editingDraft.value.apiKey) {
    editingDraft.value.apiKeyConfigured = true;
  } else {
    editingDraft.value.apiKeyConfigured = false;
  }
  saveAIConfig(editingDraft.value);
  saveSettingsAndRefreshEncryption();
  editingConfigId.value = null;
  editingDraft.value = null;
  editingOriginalApiKey.value = "";
  testResult.value = null;
  remoteModels.value = [];
  configJsonText.value = "";
}

function handleCancel() {
  editingConfigId.value = null;
  editingDraft.value = null;
  editingOriginalApiKey.value = "";
  testResult.value = null;
  remoteModels.value = [];
  configJsonText.value = "";
}

async function loadPrompt(name: "default" | "agent") {
  if (!editingDraft.value) return;
  loadingPrompt.value = name;
  try {
    const text = await aiService.getSystemPrompt(name);
    editingDraft.value.systemPrompt = text;
    notifySuccess(
      name === "agent" ? t("aiSection.agentPromptLoaded") : t("aiSection.defaultPromptLoaded"),
    );
  } catch (e: unknown) {
    notifyError(
      t("aiSection.loadFailed", { name, error: errorMessage(e) || t("aiSection.unknownError") }),
    );
  } finally {
    loadingPrompt.value = null;
  }
}

function resetSystemPrompt() {
  if (!editingDraft.value) return;
  editingDraft.value.systemPrompt = "";
  notifySuccess(t("aiSection.systemPromptCleared"));
}

async function handleFetchModels(silent = false): Promise<void> {
  if (!editingDraft.value) return;
  const base = normalizeAiBaseUrl(editingDraft.value.baseUrl);
  if (!base) {
    if (!silent) notifyError(t("aiSection.fetchModelsFail", { error: "empty Base URL" }));
    return;
  }
  fetchingModels.value = true;
  try {
    const key = editingDraft.value.apiKey || "";
    const models = await aiService.listModels(base, key);
    if (models.length > 0) {
      remoteModels.value = models;
      if (!editingDraft.value.model || !models.includes(editingDraft.value.model)) {
        // keep current model id even if not in list (user may use alias)
      }
      if (!silent) {
        notifySuccess(t("aiSection.fetchModelsOk", { count: String(models.length) }));
      }
    } else if (!silent) {
      notifyError(t("aiSection.fetchModelsEmpty"));
    }
  } catch (e: unknown) {
    if (!silent) {
      notifyError(
        t("aiSection.fetchModelsFail", {
          error: errorMessage(e) || t("aiSection.unknownError"),
        }),
      );
    }
  } finally {
    fetchingModels.value = false;
  }
}

async function handleCopyJson(): Promise<void> {
  syncConfigJsonFromDraft();
  try {
    await navigator.clipboard.writeText(configJsonText.value);
    notifySuccess(t("aiSection.jsonCopied"));
  } catch (e: unknown) {
    notifyError(errorMessage(e) || t("aiSection.unknownError"));
  }
}

function handleApplyJson(): void {
  if (!editingDraft.value) return;
  try {
    const parsed = JSON.parse(configJsonText.value) as PortableAIConfigJson;
    if (!parsed || typeof parsed !== "object") {
      throw new Error("root must be an object");
    }
    suppressModelAutoFetch = true;
    if (typeof parsed.name === "string") editingDraft.value.name = parsed.name;
    if (typeof parsed.provider === "string") editingDraft.value.provider = parsed.provider;
    if (typeof parsed.protocol === "string") editingDraft.value.protocol = parsed.protocol;
    if (typeof parsed.baseUrl === "string") {
      editingDraft.value.baseUrl = normalizeAiBaseUrl(parsed.baseUrl);
    }
    if (typeof parsed.model === "string") editingDraft.value.model = parsed.model;
    if (typeof parsed.temperature === "number") editingDraft.value.temperature = parsed.temperature;
    if (typeof parsed.maxTokens === "number") editingDraft.value.maxTokens = parsed.maxTokens;
    if (typeof parsed.systemPrompt === "string") {
      editingDraft.value.systemPrompt = parsed.systemPrompt;
    }
    suppressModelAutoFetch = false;
    syncConfigJsonFromDraft();
    notifySuccess(t("aiSection.jsonApplyOk"));
    scheduleAutoFetchModels();
  } catch (e: unknown) {
    notifyError(
      t("aiSection.jsonApplyFail", { error: errorMessage(e) || t("aiSection.unknownError") }),
    );
  }
}

function onBaseUrlBlur(): void {
  if (!editingDraft.value) return;
  editingDraft.value.baseUrl = normalizeAiBaseUrl(editingDraft.value.baseUrl);
}

async function handleTestConnection() {
  if (!editingDraft.value) return;
  testingConnection.value = true;
  testResult.value = null;
  try {
    editingDraft.value.baseUrl = normalizeAiBaseUrl(editingDraft.value.baseUrl);
    const newKey = editingDraft.value.apiKey;
    aiService.setConfig({
      apiKey: newKey || undefined,
      useStoredKey: !newKey,
      configId: editingDraft.value.id,
      baseUrl: editingDraft.value.baseUrl,
      model: editingDraft.value.model,
      systemPrompt: editingDraft.value.systemPrompt ?? "",
      temperature: editingDraft.value.temperature ?? 0.7,
      maxTokens: editingDraft.value.maxTokens ?? 4096,
      protocol: editingDraft.value.protocol ?? "openai",
    });
    const response = await aiService.send([{ role: "user", content: "ping" }]);
    if (response) {
      testResult.value = t("aiSection.testSuccess");
    } else {
      testResult.value = t("aiSection.testEmpty");
    }
  } catch (e: unknown) {
    testResult.value = t("aiSection.testError", { error: errorMessage(e) || t("aiSection.connectionFailed") });
  } finally {
    testingConnection.value = false;
  }
}
</script>

<template>
  <section class="settings-section">
    <h2 class="section-title">{{ t("aiSection.title") }}</h2>

    <div class="ai-configs">
      <div class="ai-configs__header">
        <span class="setting-label">{{ t("aiSection.configs") }}</span>
        <el-button size="small" type="primary" :icon="Plus" @click="handleNewConfig">
          {{ t("aiSection.newConfig") }}
        </el-button>
      </div>

      <div
        v-for="cfg in appState.aiProviderConfigs"
        :key="cfg.id"
        class="ai-config-row"
        :class="{ 'ai-config-row--active': cfg.id === appState.activeAIConfigId }"
      >
        <div class="ai-config-row__top">
          <div class="ai-config-row__info">
            <span class="ai-config-row__name">{{ cfg.name }}</span>
            <el-tag size="small" type="info">{{ providerLabel(cfg.provider) }}</el-tag>
            <el-tag size="small" type="info" effect="plain">{{ cfg.protocol || "openai" }}</el-tag>
            <span class="ai-config-row__model">{{ cfg.model }}</span>
            <el-tag
              v-if="cfg.id === appState.activeAIConfigId"
              size="small"
              type="success"
            >
              {{ t("aiSection.active") }}
            </el-tag>
          </div>
          <div class="ai-config-row__actions">
            <el-button
              v-if="cfg.id !== appState.activeAIConfigId"
              size="small"
              :icon="Check"
              @click="handleSetActive(cfg.id)"
            >
              {{ t("aiSection.setActive") }}
            </el-button>
            <el-button size="small" :icon="Edit" @click="handleEdit(cfg.id)">
              {{ t("aiSection.edit") }}
            </el-button>
            <el-button
              v-if="appState.aiProviderConfigs.length > 1"
              size="small"
              type="danger"
              :icon="Delete"
              @click="handleDelete(cfg.id)"
            >
              {{ t("aiSection.delete") }}
            </el-button>
          </div>
        </div>

        <div
          v-if="editingConfigId === cfg.id && editingDraft"
          class="ai-config-edit"
        >
          <div class="setting-row">
            <label class="setting-label">{{ t("aiSection.configName") }}</label>
            <div class="setting-control">
              <el-input
                v-model="draft.name"
                size="default"
                style="width: var(--setting-control-width)"
                :placeholder="t('aiSection.configNamePlaceholder')"
              />
            </div>
          </div>

          <div class="setting-row">
            <label class="setting-label">{{ t("aiSection.provider") }}</label>
            <div class="setting-control">
              <el-select
                v-model="draft.provider"
                size="default"
                style="width: var(--setting-control-width-md)"
              >
                <el-option
                  v-for="preset in PROVIDER_PRESETS"
                  :key="preset.id"
                  :label="preset.label"
                  :value="preset.id"
                />
              </el-select>
            </div>
          </div>

          <div v-if="providerNotes" class="ai-notes">
            <div class="ai-notes__title">{{ t("aiSection.providerNotes") }}</div>
            <p class="ai-notes__body">{{ providerNotes }}</p>
          </div>

          <div class="setting-row">
            <label class="setting-label">{{ t("aiSection.protocol") }}</label>
            <div class="setting-control setting-control--stack">
              <el-select
                v-model="draft.protocol"
                size="default"
                style="width: var(--setting-control-width-md)"
                :disabled="protocolDisabled"
              >
                <el-option
                  :label="t('aiSection.protocolOpenai')"
                  value="openai"
                />
                <el-option
                  :label="t('aiSection.protocolAnthropic')"
                  value="anthropic"
                />
              </el-select>
              <span class="field-hint">{{ protocolHint }}</span>
            </div>
          </div>

          <div class="setting-row">
            <label class="setting-label">
              {{ t("aiSection.apiKey") }}
              <span
                v-if="apiKeyEncryptionLabel"
                class="api-key-encryption-badge"
                :class="{ 'api-key-encryption-badge--encrypted': apiKeyEncrypted }"
              >
                <el-icon :size="11">
                  <Lock v-if="apiKeyEncrypted" />
                  <Unlock v-else />
                </el-icon>
                {{ apiKeyEncryptionLabel }}
              </span>
            </label>
            <div class="setting-control">
              <el-input
                v-model="draft.apiKey"
                size="default"
                style="width: var(--setting-control-width)"
                :type="showApiKey ? 'text' : 'password'"
                :placeholder="apiKeyPlaceholder"
              >
                <template #suffix>
                  <el-button
                    :icon="showApiKey ? View : Hide"
                    link
                    :aria-label="showApiKey ? t('a11y.hideApiKey') : t('a11y.showApiKey')"
                    @click="showApiKey = !showApiKey"
                  />
                </template>
              </el-input>
            </div>
          </div>

          <div class="setting-row">
            <label class="setting-label">{{ t("aiSection.baseUrl") }}</label>
            <div class="setting-control setting-control--stack">
              <el-input
                v-model="draft.baseUrl"
                size="default"
                style="width: var(--setting-control-width)"
                :placeholder="baseUrlPlaceholder"
                @blur="onBaseUrlBlur"
              />
              <span class="field-hint">{{ t("aiSection.baseUrlHint") }}</span>
              <code v-if="endpointPreview" class="endpoint-preview">
                {{ t("aiSection.endpointPreview") }}: {{ endpointPreview }}
              </code>
            </div>
          </div>

          <div class="setting-row">
            <label class="setting-label">{{ t("aiSection.model") }}</label>
            <div class="setting-control setting-control--stack">
              <div class="model-row">
                <el-select
                  v-model="draft.model"
                  size="default"
                  filterable
                  allow-create
                  default-first-option
                  style="width: var(--setting-control-width)"
                  :placeholder="t('aiSection.modelAria')"
                >
                  <el-option
                    v-for="m in modelOptions"
                    :key="m"
                    :label="m"
                    :value="m"
                  />
                </el-select>
                <el-button
                  size="default"
                  :icon="Refresh"
                  :loading="fetchingModels"
                  @click="handleFetchModels(false)"
                >
                  {{ t("aiSection.fetchModels") }}
                </el-button>
              </div>
              <label class="auto-fetch">
                <input v-model="autoFetchModels" type="checkbox" />
                {{ t("aiSection.fetchModelsAuto") }}
              </label>
              <span class="field-hint">{{ t("aiSection.modelListHint") }}</span>
            </div>
          </div>

          <div class="setting-row">
            <label class="setting-label">{{ t("aiSection.temperature") }}</label>
            <div class="setting-control">
              <el-slider
                v-model="draft.temperature"
                :min="0"
                :max="2"
                :step="0.1"
                style="width: var(--setting-control-width)"
              />
              <span class="slider-value">{{ draft.temperature.toFixed(1) }}</span>
            </div>
          </div>

          <div class="setting-row">
            <label class="setting-label">{{ t("aiSection.maxTokens") }}</label>
            <div class="setting-control">
              <el-input-number
                v-model="draft.maxTokens"
                :min="1"
                :max="128000"
                :step="256"
                size="default"
              />
            </div>
          </div>

          <div class="setting-row">
            <label class="setting-label">{{ t("aiSection.systemPrompt") }}</label>
            <div class="setting-control" style="flex-direction: column; align-items: stretch">
              <el-input
                v-model="draft.systemPrompt"
                type="textarea"
                :rows="6"
                :placeholder="t('aiSection.systemPromptPlaceholder')"
              />
              <div class="prompt-actions">
                <el-button
                  size="small"
                  :loading="loadingPrompt === 'default'"
                  @click="loadPrompt('default')"
                >
                  {{ t("aiSection.loadDefault") }}
                </el-button>
                <el-button
                  size="small"
                  :loading="loadingPrompt === 'agent'"
                  @click="loadPrompt('agent')"
                >
                  {{ t("aiSection.loadAgent") }}
                </el-button>
                <el-button size="small" @click="resetSystemPrompt">
                  {{ t("aiSection.clearUseDefault") }}
                </el-button>
              </div>
              <span class="prompt-hint">{{ t("aiSection.promptHint") }}</span>
            </div>
          </div>

          <div class="setting-row">
            <label class="setting-label">{{ t("aiSection.configJson") }}</label>
            <div class="setting-control setting-control--stack">
              <el-input
                v-model="configJsonText"
                type="textarea"
                :rows="8"
                :placeholder="t('aiSection.configJsonPlaceholder')"
                class="config-json"
              />
              <div class="prompt-actions">
                <el-button size="small" @click="handleCopyJson">
                  {{ t("aiSection.copyJson") }}
                </el-button>
                <el-button size="small" type="primary" plain @click="handleApplyJson">
                  {{ t("aiSection.applyJson") }}
                </el-button>
              </div>
              <span class="field-hint">{{ t("aiSection.configJsonHint") }}</span>
            </div>
          </div>

          <div class="setting-row">
            <label class="setting-label">{{ t("aiSection.connection") }}</label>
            <div class="setting-control">
              <el-button
                type="primary"
                size="default"
                :loading="testingConnection"
                @click="handleTestConnection"
              >
                {{ t("aiSection.testConnection") }}
              </el-button>
              <span v-if="testResult" class="ai-test-result">{{ testResult }}</span>
            </div>
          </div>

          <div class="ai-config-edit__footer">
            <el-button type="primary" @click="handleSave">
              {{ t("aiSection.save") }}
            </el-button>
            <el-button @click="handleCancel">
              {{ t("aiSection.cancel") }}
            </el-button>
          </div>
        </div>
      </div>
    </div>
  </section>
</template>

<style scoped>
.api-key-encryption-badge {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  margin-left: 8px;
  padding: 1px 6px;
  font-size: 10px;
  font-weight: 500;
  border-radius: 8px;
  vertical-align: middle;
  color: var(--color-text-tertiary);
  background: var(--color-bg-surface-container-low);
  border: 1px solid var(--color-border-subtle);
}

.api-key-encryption-badge--encrypted {
  color: var(--color-success);
  background: var(--color-success-container);
  border-color: var(--color-success);
}

.ai-configs__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 12px;
}

.ai-config-row {
  border: 1px solid var(--color-border-subtle);
  border-left: 3px solid transparent;
  border-radius: 8px;
  padding: 12px 14px;
  margin-bottom: 10px;
  background: var(--color-bg-surface-container-low);
  transition: border-color 0.15s ease, background 0.15s ease;
}

.ai-config-row--active {
  border-left-color: var(--color-primary);
  background: var(--color-primary-container);
}

.ai-config-row__top {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
}

.ai-config-row__info {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.ai-config-row__name {
  font-weight: 600;
  color: var(--color-text-primary);
}

.ai-config-row__model {
  font-size: 12px;
  color: var(--color-text-tertiary);
}

.ai-config-row__actions {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}

.ai-config-edit {
  margin-top: 14px;
  padding-top: 14px;
  border-top: 1px dashed var(--color-border-subtle);
}

.ai-config-edit__footer {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 8px;
}

.ai-test-result {
  margin-left: 12px;
  font-size: 12px;
}

.ai-notes {
  margin: 0 0 12px;
  padding: 10px 12px;
  border-radius: 8px;
  border: 1px solid var(--color-border-subtle);
  background: var(--color-bg-surface);
}

.ai-notes__title {
  font-size: 11px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  color: var(--color-text-tertiary);
  margin-bottom: 4px;
}

.ai-notes__body {
  margin: 0;
  font-size: 12px;
  line-height: 1.45;
  color: var(--color-text-secondary);
}

.setting-control--stack {
  flex-direction: column;
  align-items: stretch !important;
  gap: 6px;
}

.field-hint {
  font-size: 11px;
  color: var(--color-text-tertiary);
  line-height: 1.4;
}

.endpoint-preview {
  display: block;
  font-size: 11px;
  padding: 6px 8px;
  border-radius: 6px;
  background: var(--color-bg-surface);
  border: 1px solid var(--color-border-subtle);
  color: var(--color-text-secondary);
  word-break: break-all;
}

.model-row {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: center;
}

.auto-fetch {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--color-text-secondary);
  cursor: pointer;
  user-select: none;
}

.config-json :deep(textarea) {
  font-family: var(--font-mono, ui-monospace, SFMono-Regular, Menlo, Consolas, monospace);
  font-size: 12px;
}
</style>
