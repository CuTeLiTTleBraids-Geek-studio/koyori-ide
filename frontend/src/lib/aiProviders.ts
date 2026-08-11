// Provider presets for AI model switching (#13).
// Each preset contains the default Base URL and a list of suggested model names.
// The user can still type any model name in the chat panel (el-select allow-create).
//
// N-50/Proposal S: The hardcoded model lists below serve as OFFLINE FALLBACK.
// The frontend can call aiService.listModels(baseURL, apiKey) to refresh the
// list from the provider's /v1/models endpoint at runtime. If the online
// refresh fails, these presets are used instead.

// Koyori IDE 模块 · Ai Providers；交互服务：AI 对话（AIService）。
// 喵，这是 Koyori IDE 的 Ai Providers 模块（前端实现）~
export interface ProviderPreset {
  id: string;
  label: string;
  baseUrl: string;
  /** Suggested models shown in the chat panel dropdown (offline fallback). */
  models: string[];
  /** True if this provider runs locally (no API key required). */
  local?: boolean;
  /**
   * API protocol: "openai" (OpenAI-compatible /v1/chat/completions + Bearer)
   * or "anthropic" (native /v1/messages + x-api-key). Empty defaults to "openai".
   * Controls how the backend constructs requests and parses responses.
   */
  protocol?: "openai" | "anthropic";
  /** i18n key for provider notes in AI settings. */
  notesKey?: string;
}

export const PROVIDER_PRESETS: ProviderPreset[] = [
  {
    id: "openai",
    label: "OpenAI",
    baseUrl: "https://api.openai.com",
    protocol: "openai",
    notesKey: "aiSection.notesOpenai",
    models: [
      "gpt-5",
      "gpt-5-mini",
      "gpt-4o",
      "gpt-4o-mini",
      "gpt-4.1",
      "gpt-4.1-mini",
      "o3-mini",
      "o4-mini",
    ],
  },
  {
    id: "azure",
    label: "Azure OpenAI",
    baseUrl: "",
    protocol: "openai",
    notesKey: "aiSection.notesAzure",
    models: ["gpt-5", "gpt-4o", "gpt-4o-mini"],
  },
  {
    id: "anthropic",
    label: "Anthropic",
    baseUrl: "https://api.anthropic.com",
    protocol: "anthropic",
    notesKey: "aiSection.notesAnthropic",
    models: [
      "claude-sonnet-4-20250514",
      "claude-opus-4-20250514",
      "claude-3-7-sonnet-latest",
      "claude-3-5-sonnet-latest",
      "claude-3-5-haiku-latest",
    ],
  },
  {
    id: "gemini",
    label: "Google Gemini",
    baseUrl: "https://generativelanguage.googleapis.com/v1beta/openai",
    protocol: "openai",
    notesKey: "aiSection.notesGemini",
    models: [
      "gemini-2.5-pro",
      "gemini-2.5-flash",
      "gemini-2.0-flash",
      "gemini-1.5-pro",
    ],
  },
  {
    id: "ollama",
    label: "Ollama",
    baseUrl: "http://localhost:11434",
    protocol: "openai",
    notesKey: "aiSection.notesOllama",
    models: ["llama3.3", "qwen2.5-coder", "mistral", "phi3"],
    local: true,
  },
  {
    id: "lmstudio",
    label: "LM Studio",
    baseUrl: "http://localhost:1234",
    protocol: "openai",
    notesKey: "aiSection.notesLmstudio",
    models: [],
    local: true,
  },
  {
    id: "custom",
    label: "Custom",
    baseUrl: "",
    protocol: "openai",
    notesKey: "aiSection.notesCustom",
    models: [],
  },
];

const PRESET_BY_ID = new Map(PROVIDER_PRESETS.map((p) => [p.id, p]));

export function getProviderPreset(id: string): ProviderPreset | undefined {
  return PRESET_BY_ID.get(id);
}

/**
 * Returns the hardcoded fallback model list for a provider.
 * N-50: This is the offline fallback. For the live list, use
 * aiService.listModels(baseURL, apiKey) from the frontend.
 */
export function getSuggestedModels(providerId: string): string[] {
  return PRESET_BY_ID.get(providerId)?.models ?? [];
}

/** Same rules as Go NormalizeAIBaseURL: trim, drop trailing slash, strip /v1. */
export function normalizeAiBaseUrl(raw: string): string {
  let base = raw.trim().replace(/\/+$/, "");
  if (base.toLowerCase().endsWith("/v1")) {
    base = base.slice(0, -3).replace(/\/+$/, "");
  }
  return base;
}
