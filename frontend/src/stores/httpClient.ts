// Koyori IDE 模块 · Http Client。
// 喵，这是 Koyori IDE 的 Http Client 模块（前端实现）~
import { computed, reactive } from "vue";
import { errorMessage } from "@/lib/errors";
import * as HTTPClientServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/httpclientservice.js";
import type {
  HTTPEnvironment as BindingHTTPEnvironment,
  HTTPHistoryEntry as BindingHTTPHistoryEntry,
  HTTPRequest as BindingHTTPRequest,
  HTTPRequestOptions as BindingHTTPRequestOptions,
  HTTPResponse as BindingHTTPResponse,
} from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/models.js";

export interface HTTPEnvironment {
  values: Record<string, string>;
  secretRefs: Record<string, string>;
}

export interface HTTPRequest {
  name: string;
  method: string;
  url: string;
  headers: Record<string, string>;
  body: string;
  startLine: number;
  endLine: number;
  secretRefs: Record<string, string>;
}

export interface HTTPRequestOptions {
  requestId?: string;
  timeoutMs?: number;
  maxResponseBytes?: number;
  maxRedirects?: number;
  privateNetworkToken?: string;
}

export interface HTTPResponse {
  requestId: string;
  status: number;
  statusText: string;
  headers: Record<string, string>;
  body: string;
  durationMs: number;
}

export interface HTTPHistoryEntry {
  id: string;
  name?: string;
  method?: string;
  url?: string;
  requestHeaders?: Record<string, string>;
  status?: number;
  statusText?: string;
  responseHeaders?: Record<string, string>;
  responseBody?: string;
  durationMs?: number;
  createdAt?: string;
  error?: string;
}

export interface HTTPClientBackend {
  parseHTTPEnvironment(content: string, environmentName: string): Promise<HTTPEnvironment>;
  parseHTTPFile(content: string, environment: HTTPEnvironment): Promise<HTTPRequest[]>;
  requestPrivateNetworkAccess(targetOrigin: string, requestId: string): Promise<string>;
  sendRequest(request: HTTPRequest, options: HTTPRequestOptions): Promise<HTTPResponse>;
  cancelRequest(requestId: string): Promise<boolean>;
  getHistory(): Promise<HTTPHistoryEntry[]>;
  clearHistory(): Promise<void>;
}

interface HTTPClientState {
  requests: HTTPRequest[];
  selectedIndex: number;
  response: HTTPResponse | null;
  history: HTTPHistoryEntry[];
  loading: boolean;
  parsing: boolean;
  historyLoading: boolean;
  activeRequestId: string | null;
  error: string | null;
  authorizingPrivateNetwork: boolean;
  privateNetworkApproval: {
    origin: string;
    requestId: string;
    token: string;
  } | null;
}

export const httpClientState = reactive<HTTPClientState>({
  requests: [],
  selectedIndex: -1,
  response: null,
  history: [],
  loading: false,
  parsing: false,
  historyLoading: false,
  activeRequestId: null,
  error: null,
  authorizingPrivateNetwork: false,
  privateNetworkApproval: null,
});

export const selectedHTTPRequest = computed<HTTPRequest | null>(() => (
  httpClientState.selectedIndex >= 0
    ? httpClientState.requests[httpClientState.selectedIndex] ?? null
    : null
));

let backend: HTTPClientBackend | null = null;
let parseGeneration = 0;

function normalizeStringRecord(
  value: Record<string, string | undefined> | null | undefined,
): Record<string, string> {
  const normalized: Record<string, string> = {};
  for (const [name, entry] of Object.entries(value ?? {})) {
    if (typeof entry === "string") normalized[name] = entry;
  }
  return normalized;
}

function normalizeHTTPEnvironment(value: BindingHTTPEnvironment): HTTPEnvironment {
  return {
    values: normalizeStringRecord(value.values),
    secretRefs: normalizeStringRecord(value.secretRefs),
  };
}

function normalizeHTTPRequest(value: BindingHTTPRequest): HTTPRequest {
  return {
    ...value,
    headers: normalizeStringRecord(value.headers),
    secretRefs: normalizeStringRecord(value.secretRefs),
  };
}

function normalizeHTTPResponse(value: BindingHTTPResponse): HTTPResponse {
  return {
    ...value,
    headers: normalizeStringRecord(value.headers),
  };
}

function normalizeHTTPHistoryEntry(value: BindingHTTPHistoryEntry): HTTPHistoryEntry {
  return {
    ...value,
    requestHeaders: normalizeStringRecord(value.requestHeaders),
    responseHeaders: normalizeStringRecord(value.responseHeaders),
    createdAt: value.createdAt == null ? undefined : String(value.createdAt),
  };
}

function bindingRequestOptions(options: HTTPRequestOptions): BindingHTTPRequestOptions {
  return {
    requestId: options.requestId ?? "",
    timeoutMs: options.timeoutMs ?? 0,
    maxResponseBytes: options.maxResponseBytes ?? 0,
    maxRedirects: options.maxRedirects ?? 0,
    privateNetworkToken: options.privateNetworkToken ?? "",
  };
}

function defaultBackend(): HTTPClientBackend {
  return {
    async parseHTTPEnvironment(content, environmentName) {
      return normalizeHTTPEnvironment(
        await HTTPClientServiceBindings.ParseHTTPEnvironment(content, environmentName),
      );
    },
    async parseHTTPFile(content, environment) {
      const requests = await HTTPClientServiceBindings.ParseHTTPFile(content, environment);
      return (requests ?? []).map(normalizeHTTPRequest);
    },
    async requestPrivateNetworkAccess(targetOrigin, requestId) {
      return HTTPClientServiceBindings.RequestPrivateNetworkAccess(targetOrigin, requestId);
    },
    async sendRequest(request, options) {
      return normalizeHTTPResponse(
        await HTTPClientServiceBindings.SendRequest(request, bindingRequestOptions(options)),
      );
    },
    async cancelRequest(requestId) {
      return HTTPClientServiceBindings.CancelRequest(requestId);
    },
    async getHistory() {
      const history = await HTTPClientServiceBindings.GetHistory();
      return (history ?? []).map(normalizeHTTPHistoryEntry);
    },
    async clearHistory() {
      return HTTPClientServiceBindings.ClearHistory();
    },
  };
}

function getBackend(): HTTPClientBackend {
  if (!backend) backend = defaultBackend();
  return backend;
}

export function setHTTPClientBackend(value: HTTPClientBackend | null): void {
  backend = value;
}

export function requestAtLine(line: number): HTTPRequest | null {
  return httpClientState.requests.find((request) => (
    line >= request.startLine && line <= request.endLine
  )) ?? null;
}

export function selectHTTPRequest(index: number): void {
  clearPrivateNetworkAuthorization();
  if (index < 0 || index >= httpClientState.requests.length) {
    httpClientState.selectedIndex = -1;
    return;
  }
  httpClientState.selectedIndex = index;
}

export function selectHTTPRequestAtLine(line: number): void {
  clearPrivateNetworkAuthorization();
  const index = httpClientState.requests.findIndex((request) => (
    line >= request.startLine && line <= request.endLine
  ));
  httpClientState.selectedIndex = index >= 0 ? index : (httpClientState.requests.length ? 0 : -1);
}

export async function parseHTTPEnvironmentFile(
  content: string,
  environmentName: string,
): Promise<HTTPEnvironment | null> {
  httpClientState.error = null;
  try {
    return await getBackend().parseHTTPEnvironment(content, environmentName);
  } catch (error: unknown) {
    httpClientState.error = errorMessage(error);
    return null;
  }
}

export async function parseHTTPDocument(
  source: string,
  environment: HTTPEnvironment = { values: {}, secretRefs: {} },
  cursorLine = 1,
): Promise<HTTPRequest[]> {
  const generation = ++parseGeneration;
  httpClientState.parsing = true;
  httpClientState.error = null;
  try {
    const requests = await getBackend().parseHTTPFile(source, environment);
    if (generation !== parseGeneration) return requests;
    httpClientState.requests = requests ?? [];
    selectHTTPRequestAtLine(cursorLine);
    return httpClientState.requests;
  } catch (error: unknown) {
    if (generation === parseGeneration) {
      httpClientState.requests = [];
      httpClientState.selectedIndex = -1;
      httpClientState.error = errorMessage(error);
    }
    return [];
  } finally {
    if (generation === parseGeneration) httpClientState.parsing = false;
  }
}

export function clearPrivateNetworkAuthorization(): void {
  httpClientState.privateNetworkApproval = null;
}

export async function authorizeSelectedPrivateNetwork(): Promise<boolean> {
  const request = selectedHTTPRequest.value;
  if (!request) {
    httpClientState.error = "No HTTP request is selected";
    return false;
  }
  let origin: string;
  try {
    const parsed = new URL(request.url);
    if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
      throw new Error("Only HTTP and HTTPS origins can be authorized");
    }
    origin = parsed.origin;
  } catch (error: unknown) {
    httpClientState.error = errorMessage(error);
    return false;
  }

  const requestId = createRequestId();
  httpClientState.authorizingPrivateNetwork = true;
  httpClientState.privateNetworkApproval = null;
  httpClientState.error = null;
  try {
    const token = await getBackend().requestPrivateNetworkAccess(origin, requestId);
    httpClientState.privateNetworkApproval = { origin, requestId, token };
    return true;
  } catch (error: unknown) {
    httpClientState.error = errorMessage(error);
    return false;
  } finally {
    httpClientState.authorizingPrivateNetwork = false;
  }
}

export async function sendHTTPRequest(options: HTTPRequestOptions = {}): Promise<HTTPResponse | null> {
  const request = selectedHTTPRequest.value;
  if (!request) {
    httpClientState.error = "No HTTP request is selected";
    return null;
  }
  if (httpClientState.loading) return null;

  const approval = httpClientState.privateNetworkApproval;
  const requestId = options.requestId || approval?.requestId || createRequestId();
  const resolvedOptions = { ...options, requestId };
  if (approval) {
    resolvedOptions.privateNetworkToken = approval.token;
    httpClientState.privateNetworkApproval = null;
  }
  httpClientState.loading = true;
  httpClientState.activeRequestId = requestId;
  httpClientState.response = null;
  httpClientState.error = null;
  httpClientState.authorizingPrivateNetwork = false;
  httpClientState.privateNetworkApproval = null;
  try {
    const response = await getBackend().sendRequest(request, resolvedOptions);
    httpClientState.response = response;
    await loadHTTPHistory();
    return response;
  } catch (error: unknown) {
    httpClientState.error = errorMessage(error);
    return null;
  } finally {
    if (httpClientState.activeRequestId === requestId) {
      httpClientState.activeRequestId = null;
      httpClientState.loading = false;
    }
  }
}

export async function cancelActiveHTTPRequest(): Promise<boolean> {
  const requestId = httpClientState.activeRequestId;
  if (!requestId) return false;
  try {
    return await getBackend().cancelRequest(requestId);
  } catch (error: unknown) {
    httpClientState.error = errorMessage(error);
    return false;
  }
}

export async function loadHTTPHistory(): Promise<void> {
  httpClientState.historyLoading = true;
  try {
    httpClientState.history = await getBackend().getHistory();
  } catch (error: unknown) {
    httpClientState.error = errorMessage(error);
  } finally {
    httpClientState.historyLoading = false;
  }
}

export async function clearHTTPHistory(): Promise<void> {
  try {
    await getBackend().clearHistory();
    httpClientState.history = [];
  } catch (error: unknown) {
    httpClientState.error = errorMessage(error);
  }
}

export function formatHTTPResponseBody(response: HTTPResponse): string {
  const contentType = Object.entries(response.headers).find(
    ([name]) => name.toLowerCase() === "content-type",
  )?.[1]?.toLowerCase() ?? "";
  if (!contentType.includes("json") && !looksLikeJSON(response.body)) return response.body;
  try {
    return JSON.stringify(JSON.parse(response.body), null, 2);
  } catch {
    return response.body;
  }
}

function looksLikeJSON(body: string): boolean {
  const trimmed = body.trim();
  return (trimmed.startsWith("{") && trimmed.endsWith("}"))
    || (trimmed.startsWith("[") && trimmed.endsWith("]"));
}

function createRequestId(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return `http-${crypto.randomUUID()}`;
  }
  return `http-${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

export function resetHTTPClientStore(): void {
  parseGeneration++;
  httpClientState.requests = [];
  httpClientState.selectedIndex = -1;
  httpClientState.response = null;
  httpClientState.history = [];
  httpClientState.loading = false;
  httpClientState.parsing = false;
  httpClientState.historyLoading = false;
  httpClientState.activeRequestId = null;
  httpClientState.error = null;
  httpClientState.authorizingPrivateNetwork = false;
  httpClientState.privateNetworkApproval = null;
  backend = null;
}
