// Koyori IDE 模块 · Http Client Probe。
// 喵，这是 Koyori IDE 的 Http Client Probe 模块（前端实现）~
import { Events } from "@wailsio/runtime";
import * as HTTPClientServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/httpclientservice.js";
import type {
  HTTPRequest as BindingHTTPRequest,
  HTTPRequestOptions as BindingHTTPRequestOptions,
} from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/models.js";
import {
  authorizeSelectedPrivateNetwork,
  cancelActiveHTTPRequest,
  clearHTTPHistory,
  httpClientState,
  loadHTTPHistory,
  parseHTTPDocument,
  parseHTTPEnvironmentFile,
  resetHTTPClientStore,
  selectedHTTPRequest,
  sendHTTPRequest,
} from "@/stores/httpClient";

const resultEvent = "e2e:http-client-result";

interface HTTPClientProbeConfig {
  runId: string;
  primaryOrigin: string;
  secondaryOrigin: string;
  publicUrl: string;
  expiredToken: string;
  expiredRequestId: string;
}

type ProbeStep = Record<string, unknown> & { passed: true };

interface HTTPClientProbeResult {
  runId: string;
  ok: boolean;
  transport: string;
  generatedModule: string;
  steps?: Record<string, ProbeStep>;
  historyCount?: number;
  error?: string;
}

function message(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function requireCondition(condition: unknown, detail: string): asserts condition {
  if (!condition) throw new Error(detail);
}

function request(method: string, url: string, headers: Record<string, string> = {}, body = ""): BindingHTTPRequest {
  return {
    name: "e2e-probe",
    method,
    url,
    headers,
    body,
    startLine: 1,
    endLine: 1,
    secretRefs: {},
  };
}

function options(
  requestId: string,
  privateNetworkToken = "",
  timeoutMs = 0,
): BindingHTTPRequestOptions {
  return {
    requestId,
    timeoutMs,
    maxResponseBytes: 0,
    maxRedirects: 0,
    privateNetworkToken,
  };
}

async function expectRejected(
  action: () => Promise<unknown>,
  pattern: RegExp,
  label: string,
): Promise<string> {
  try {
    await action();
  } catch (error: unknown) {
    const detail = message(error);
    requireCondition(pattern.test(detail), `${label} returned an unexpected error: ${detail}`);
    return detail;
  }
  throw new Error(`${label} unexpectedly succeeded`);
}

async function selectDocument(source: string): Promise<void> {
  resetHTTPClientStore();
  const parsed = await parseHTTPDocument(source, { values: {}, secretRefs: {} }, 1);
  requireCondition(parsed.length === 1, `expected one parsed HTTP request, got ${parsed.length}: ${httpClientState.error ?? "no error"}`);
  requireCondition(selectedHTTPRequest.value !== null, "parsed HTTP request was not selected");
}

function delay(milliseconds: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

async function runProbe(config: HTTPClientProbeConfig): Promise<HTTPClientProbeResult> {
  const steps: Record<string, ProbeStep> = {};
  await HTTPClientServiceBindings.ClearHistory();

  const environment = await parseHTTPEnvironmentFile(
    JSON.stringify({ probe: { base: config.primaryOrigin } }),
    "probe",
  );
  requireCondition(environment?.values.base === config.primaryOrigin, "generated ParseHTTPEnvironment binding returned the wrong value");
  steps.parseEnvironment = { passed: true, value: environment.values.base };

  await selectDocument(`GET ${config.publicUrl}`);
  const publicResponse = await sendHTTPRequest({ timeoutMs: 15_000 });
  requireCondition(publicResponse !== null, `public request failed: ${httpClientState.error ?? "unknown error"}`);
  requireCondition(publicResponse.status >= 100 && publicResponse.status <= 599, `public request returned invalid HTTP status ${publicResponse.status}`);
  requireCondition(publicResponse.durationMs >= 0, `public request duration was ${publicResponse.durationMs}`);
  steps.publicRequest = {
    passed: true,
    status: publicResponse.status,
    bodyBytes: new Blob([publicResponse.body]).size,
    durationMs: publicResponse.durationMs,
  };

  await selectDocument(`GET ${config.primaryOrigin}/guard/missing`);
  const missingResponse = await sendHTTPRequest({ requestId: "packaged-missing-token" });
  requireCondition(missingResponse === null, "private request without a token unexpectedly succeeded");
  requireCondition(/approval/i.test(httpClientState.error ?? ""), `missing-token error was not diagnostic: ${httpClientState.error ?? ""}`);
  steps.missingToken = { passed: true, error: httpClientState.error };

  await selectDocument(`GET ${config.primaryOrigin}/guard/rejected`);
  const approvedAfterDenial = await authorizeSelectedPrivateNetwork();
  requireCondition(!approvedAfterDenial, "backend approval rejection unexpectedly returned a token");
  requireCondition(/not approved|not allowed/i.test(httpClientState.error ?? ""), `approval rejection was not diagnostic: ${httpClientState.error ?? ""}`);
  steps.explicitRejection = { passed: true, error: httpClientState.error };

  const expiredError = await expectRejected(
    () => HTTPClientServiceBindings.SendRequest(
      request("GET", `${config.primaryOrigin}/guard/expired`, { "X-Probe-Case": "expired" }),
      options(config.expiredRequestId, config.expiredToken),
    ),
    /expired/i,
    "expired private-network token",
  );
  steps.expiredToken = { passed: true, error: expiredError };

  await selectDocument([
    "# @name packaged-response",
    `POST ${config.primaryOrigin}/response`,
    "X-Probe: real-packaged-webview",
    "Content-Type: text/plain",
    "",
    "payload",
  ].join("\n"));
  requireCondition(await authorizeSelectedPrivateNetwork(), `private-network approval failed: ${httpClientState.error ?? "unknown error"}`);
  const approval = httpClientState.privateNetworkApproval;
  const approvedRequest = selectedHTTPRequest.value;
  requireCondition(approval !== null && approvedRequest !== null, "approved request state was not retained before send");
  const response = await sendHTTPRequest();
  requireCondition(response !== null, `approved loopback request failed: ${httpClientState.error ?? "unknown error"}`);
  requireCondition(response.status === 201 && response.statusText === "Created", `approved response status was ${response.status} ${response.statusText}`);
  requireCondition(response.headers["Content-Type"] === "application/json", "response Content-Type was not available to the UI store");
  requireCondition(response.headers["Set-Cookie"] === "[REDACTED]", "Set-Cookie crossed the binding without redaction");
  requireCondition(response.body === '{"ok":true}', `response body was ${response.body}`);
  requireCondition(response.durationMs >= 0, `response duration was ${response.durationMs}`);
  requireCondition(httpClientState.response?.requestId === response.requestId, "UI store did not retain the real binding response");
  steps.approvedResponse = {
    passed: true,
    requestId: response.requestId,
    status: response.status,
    headers: response.headers,
    body: response.body,
    durationMs: response.durationMs,
  };

  const replayError = await expectRejected(
    () => HTTPClientServiceBindings.SendRequest(
      approvedRequest,
      options(approval.requestId, approval.token),
    ),
    /already used|missing|invalid/i,
    "replayed private-network token",
  );
  steps.tokenReplay = { passed: true, error: replayError };

  const sameRedirectID = "packaged-same-origin-redirect";
  const sameRedirectToken = await HTTPClientServiceBindings.RequestPrivateNetworkAccess(
    config.primaryOrigin,
    sameRedirectID,
  );
  const sameRedirectResponse = await HTTPClientServiceBindings.SendRequest(
    request("GET", `${config.primaryOrigin}/redirect-same`, { "X-Probe-Case": "same-redirect" }),
    options(sameRedirectID, sameRedirectToken),
  );
  requireCondition(sameRedirectResponse.status === 202, `same-origin redirect returned ${sameRedirectResponse.status}`);
  steps.sameOriginRedirect = { passed: true, status: sameRedirectResponse.status };

  const crossRedirectID = "packaged-cross-origin-redirect";
  const crossRedirectToken = await HTTPClientServiceBindings.RequestPrivateNetworkAccess(
    config.primaryOrigin,
    crossRedirectID,
  );
  const crossRedirectError = await expectRejected(
    () => HTTPClientServiceBindings.SendRequest(
      request("GET", `${config.primaryOrigin}/redirect-cross?target=${encodeURIComponent(config.secondaryOrigin)}`, { "X-Probe-Case": "cross-redirect" }),
      options(crossRedirectID, crossRedirectToken),
    ),
    /redirect/i,
    "cross-origin private redirect",
  );
  steps.crossOriginRedirect = { passed: true, error: crossRedirectError };

  await selectDocument(`GET ${config.primaryOrigin}/slow?case=cancel`);
  requireCondition(await authorizeSelectedPrivateNetwork(), `cancellation approval failed: ${httpClientState.error ?? "unknown error"}`);
  const cancellation = sendHTTPRequest({ timeoutMs: 10_000 });
  await delay(350);
  const cancelAccepted = await cancelActiveHTTPRequest();
  const cancelledResponse = await cancellation;
  requireCondition(cancelAccepted, "CancelRequest returned false for an active real request");
  requireCondition(cancelledResponse === null, "cancelled request unexpectedly returned a response");
  requireCondition(/cancel/i.test(httpClientState.error ?? ""), `cancellation error was not diagnostic: ${httpClientState.error ?? ""}`);
  steps.cancellation = { passed: true, error: httpClientState.error };

  await selectDocument(`GET ${config.primaryOrigin}/slow?case=timeout`);
  requireCondition(await authorizeSelectedPrivateNetwork(), `timeout approval failed: ${httpClientState.error ?? "unknown error"}`);
  const timedOutResponse = await sendHTTPRequest({ timeoutMs: 250 });
  requireCondition(timedOutResponse === null, "timed-out request unexpectedly returned a response");
  requireCondition(/deadline|timeout/i.test(httpClientState.error ?? ""), `timeout error was not diagnostic: ${httpClientState.error ?? ""}`);
  steps.timeout = { passed: true, error: httpClientState.error };

  await loadHTTPHistory();
  const historyCount = httpClientState.history.length;
  requireCondition(historyCount >= 6, `sanitized HTTP history contained only ${historyCount} entries`);
  await clearHTTPHistory();
  requireCondition(httpClientState.history.length === 0, "HTTP history did not clear through the real binding");
  steps.history = { passed: true, count: historyCount, cleared: true };

  return {
    runId: config.runId,
    ok: true,
    transport: "packaged renderer -> generated Wails binding -> HTTPClientService",
    generatedModule: "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/httpclientservice.ts",
    steps,
    historyCount,
  };
}

export function installHTTPClientProbe(): void {
  const target = globalThis as typeof globalThis & {
    __koyoriIdeRunHTTPClientProbe?: (config: HTTPClientProbeConfig) => Promise<void>;
  };
  target.__koyoriIdeRunHTTPClientProbe = async (config) => {
    let result: HTTPClientProbeResult;
    try {
      result = await runProbe(config);
    } catch (error: unknown) {
      result = {
        runId: config.runId,
        ok: false,
        transport: "packaged renderer -> generated Wails binding -> HTTPClientService",
        generatedModule: "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/httpclientservice.ts",
        error: message(error),
      };
    }
    await Events.Emit(resultEvent, result);
  };
}
