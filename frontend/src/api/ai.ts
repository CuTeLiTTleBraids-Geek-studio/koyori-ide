// Koyori IDE 模块 · AI；交互服务：AI 对话（AIService）。
// 喵，这是 Koyori IDE 的 AI 模块（前端实现）~
import * as AIServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/aiservice.js";
import type {
  ChatMessage, CompletionRequest, CompletionResponse, PresetFile,
  PresetWithSource, RawCompletionResponse,
} from "@/types";
import { requireNonNull, unwrapNullable } from "./boundary";

type BindingPresetWithSource = NonNullable<
  Awaited<ReturnType<typeof AIServiceBindings.ListPresetsWithSource>>
>[number];

function fromBindingPresetWithSource(preset: BindingPresetWithSource): PresetWithSource {
  let source: PresetWithSource["source"];
  switch (preset.source) {
    case "builtin":
    case "project":
    case "user":
      source = preset.source;
      break;
    default:
      throw new Error(`AIService.ListPresetsWithSource returned invalid source: ${preset.source}`);
  }
  return { ...preset, source };
}

export const aiService = {
  // G-SEC-07: apiKey is optional. When omitted, useStoredKey=true tells the
  // backend to fetch the key from SettingsService via configId. The plaintext
  // key never crosses the Wails binding into the JS heap.
  setConfig: (config: {
	apiKey?: string;
	useStoredKey?: boolean;
	configId?: string;
	provider?: string;
	baseUrl: string;
	model: string;
	systemPrompt?: string;
	agentSystemPrompt?: string;
	conversationTitlePrompt?: string;
	inlineCompletionPrompt?: string;
	maxTokens?: number;
	contextWindow?: number;
	temperature?: number;
	reasoningEffort?: "" | "low" | "medium" | "high";
	protocol?: string; // "openai" | "anthropic"
    /** prompt-5 Task H: OpenAI-compatible tool definitions for native function calling. */
    tools?: Array<{
      type: "function";
      function: {
        name: string;
        description: string;
        parameters: Record<string, unknown>;
      };
    }>;
  }) =>
    AIServiceBindings.SetConfig({
		APIKey: config.apiKey ?? "",
		Provider: config.provider ?? "",
		UseStoredKey: config.useStoredKey ?? false,
		ConfigID: config.configId ?? "",
		BaseURL: config.baseUrl,
		Model: config.model,
		SystemPrompt: config.systemPrompt ?? "",
		AgentSystemPrompt: config.agentSystemPrompt ?? "",
		ConversationTitlePrompt: config.conversationTitlePrompt ?? "",
		InlineCompletionPrompt: config.inlineCompletionPrompt ?? "",
		MaxTokens: config.maxTokens ?? 0,
		ContextWindow: config.contextWindow ?? 0,
		Temperature: config.temperature ?? 0,
		ReasoningEffort: config.reasoningEffort ?? "",
		Protocol: config.protocol ?? "",
		Tools: config.tools ?? [],
	}),
	getReasoningCapability: (provider: string, model: string, protocol: string) =>
		AIServiceBindings.GetReasoningCapability(provider, model, protocol) as Promise<{
			provider: string;
			model: string;
			protocol: string;
			status: "supported" | "unsupported" | "unknown";
			requestField?: string;
		}>,
  send: (messages: ChatMessage[]) =>
    requireNonNull(AIServiceBindings.Send(messages), "AIService.Send"),
  // prompt-6/7: StartStream returns streamId (bindings typed as string).
  startStream: (messages: ChatMessage[]) =>
    AIServiceBindings.StartStream(messages) as Promise<string>,
  startAgentStream: (sessionId: string, messages: ChatMessage[]) =>
		AIServiceBindings.StartAgentStream(sessionId, messages) as Promise<{ streamId: string; sessionId: string }>,
  isStreaming: () =>
    AIServiceBindings.IsStreaming() as Promise<boolean>,
  stopStream: () =>
    AIServiceBindings.StopStream() as Promise<void>,
  getDefaultSystemPrompt: () =>
    AIServiceBindings.GetDefaultSystemPrompt() as Promise<string>,
  getAgentSystemPrompt: () =>
    AIServiceBindings.GetAgentSystemPrompt() as Promise<string>,
  getSystemPrompt: (name: string) =>
    AIServiceBindings.GetSystemPrompt(name) as Promise<string>,
  getConversationTitlePrompt: () =>
    AIServiceBindings.GetConversationTitlePrompt() as Promise<string>,
  getInlineCompletionSystemPrompt: () =>
    AIServiceBindings.GetInlineCompletionSystemPrompt() as Promise<string>,
  getEffectiveAgentSystemPrompt: () =>
    AIServiceBindings.GetEffectiveAgentSystemPrompt() as Promise<string>,
  getEffectiveConversationTitlePrompt: () =>
    AIServiceBindings.GetEffectiveConversationTitlePrompt() as Promise<string>,
  getEffectiveInlineCompletionPrompt: () =>
    AIServiceBindings.GetEffectiveInlineCompletionPrompt() as Promise<string>,
  generateTitleWithAI: (firstMessage: string) =>
    AIServiceBindings.GenerateTitleWithAI(firstMessage) as Promise<string>,
  getPresetPrompt: (name: string) =>
    AIServiceBindings.GetPresetPrompt(name) as Promise<string>,
  listPresets: () =>
    unwrapNullable(AIServiceBindings.ListPresets(), []),
  // N-50/Proposal S: Fetch available models from the provider's /v1/models endpoint.
  listModels: (baseURL: string, apiKey: string) =>
    unwrapNullable(AIServiceBindings.ListModels(baseURL, apiKey), []),
  listPresetsWithSource: () =>
    unwrapNullable(AIServiceBindings.ListPresetsWithSource(), [])
      .then((presets) => presets.map(fromBindingPresetWithSource)),
  saveProjectPreset: (preset: PresetFile) =>
    AIServiceBindings.SaveProjectPreset(preset) as Promise<void>,
  saveUserPreset: (preset: PresetFile) =>
    AIServiceBindings.SaveUserPreset(preset) as Promise<void>,
  deleteProjectPreset: (name: string) =>
    AIServiceBindings.DeleteProjectPreset(name) as Promise<void>,
  deleteUserPreset: (name: string) =>
    AIServiceBindings.DeleteUserPreset(name) as Promise<void>,
  complete: (req: CompletionRequest, signal?: AbortSignal) => {
    // N-43: Preserve the CancellablePromise so the caller can abort the
    // in-flight request. Wails bindings return a CancellablePromise with
    // a .cancelOn(signal) method that binds cancellation to an AbortSignal.
    const cancellable = AIServiceBindings.Complete(req);
    if (signal) cancellable.cancelOn(signal);
    return requireNonNull(cancellable, "AIService.Complete").then((response) => {
      const raw: RawCompletionResponse = response;
      return { text: raw.Text ?? raw.text ?? "" } satisfies CompletionResponse;
    });
  },
};
