import { beforeEach, describe, expect, it, vi } from "vitest";

const bindingMocks = vi.hoisted(() => ({
  CancelRequest: vi.fn(),
  ClearHistory: vi.fn(),
  GetHistory: vi.fn(),
  ParseHTTPEnvironment: vi.fn(),
  ParseHTTPFile: vi.fn(),
  RequestPrivateNetworkAccess: vi.fn(),
  SendRequest: vi.fn(),
}));

vi.mock("../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/httpclientservice.js", () => bindingMocks);

import {
  authorizeSelectedPrivateNetwork,
  cancelActiveHTTPRequest,
  clearHTTPHistory,
  formatHTTPResponseBody,
  httpClientState,
  loadHTTPHistory,
  parseHTTPEnvironmentFile,
  parseHTTPDocument,
  requestAtLine,
  resetHTTPClientStore,
  sendHTTPRequest,
  setHTTPClientBackend,
  type HTTPClientBackend,
  type HTTPRequest,
} from "./httpClient";

const request: HTTPRequest = {
  name: "getUser",
  method: "GET",
  url: "https://example.com/users/1",
  headers: { Accept: "application/json" },
  body: "",
  startLine: 3,
  endLine: 7,
  secretRefs: {},
};

function backend(): HTTPClientBackend {
  return {
    parseHTTPEnvironment: vi.fn().mockResolvedValue({
      values: { baseUrl: "https://dev.example.com" },
      secretRefs: { token: "http-client/token" },
    }),
    parseHTTPFile: vi.fn().mockResolvedValue([request]),
    requestPrivateNetworkAccess: vi.fn().mockResolvedValue("private-token"),
    sendRequest: vi.fn().mockResolvedValue({
      requestId: "run-1",
      status: 200,
      statusText: "OK",
      headers: { "Content-Type": "application/json" },
      body: `{"ok":true}`,
      durationMs: 12,
    }),
    cancelRequest: vi.fn().mockResolvedValue(true),
    getHistory: vi.fn().mockResolvedValue([{ id: "history-1", name: "getUser", status: 200 }]),
    clearHistory: vi.fn().mockResolvedValue(undefined),
  };
}

describe("http client store", () => {
  beforeEach(() => {
    vi.resetAllMocks();
    resetHTTPClientStore();
  });

  it("uses the generated binding as the default production backend", async () => {
    bindingMocks.ParseHTTPEnvironment.mockResolvedValue({ values: null, secretRefs: null });
    bindingMocks.ParseHTTPFile.mockResolvedValue([{ ...request, headers: null, secretRefs: null }]);
    bindingMocks.SendRequest.mockResolvedValue({
      requestId: "generated-1",
      status: 204,
      statusText: "No Content",
      headers: null,
      body: "",
      durationMs: 3,
    });
    bindingMocks.GetHistory.mockResolvedValue(null);

    await expect(parseHTTPEnvironmentFile("{}", "dev")).resolves.toEqual({
      values: {},
      secretRefs: {},
    });
    await parseHTTPDocument("GET https://example.com", { values: {}, secretRefs: {} }, 5);
    const response = await sendHTTPRequest({ timeoutMs: 2_000 });

    expect(bindingMocks.ParseHTTPEnvironment).toHaveBeenCalledWith("{}", "dev");
    expect(bindingMocks.ParseHTTPFile).toHaveBeenCalledOnce();
    expect(bindingMocks.SendRequest).toHaveBeenCalledWith(
      expect.objectContaining({ headers: {}, secretRefs: {} }),
      {
        requestId: expect.any(String),
        timeoutMs: 2_000,
        maxResponseBytes: 0,
        maxRedirects: 0,
        privateNetworkToken: "",
      },
    );
    expect(response).toEqual(expect.objectContaining({ headers: {}, status: 204 }));
    expect(httpClientState.history).toEqual([]);
  });

  it("parses a document and selects the request containing the cursor", async () => {
    const mock = backend();
    setHTTPClientBackend(mock);

    await parseHTTPDocument("GET https://example.com", { values: {}, secretRefs: {} }, 5);

    expect(mock.parseHTTPFile).toHaveBeenCalledOnce();
    expect(httpClientState.requests).toEqual([request]);
    expect(httpClientState.selectedIndex).toBe(0);
    expect(requestAtLine(5)).toEqual(request);
    expect(requestAtLine(20)).toBeNull();
  });

  it("parses a named environment without receiving secret plaintext", async () => {
    const mock = backend();
    setHTTPClientBackend(mock);

    const environment = await parseHTTPEnvironmentFile("{}", "dev");

    expect(mock.parseHTTPEnvironment).toHaveBeenCalledWith("{}", "dev");
    expect(environment).toEqual({
      values: { baseUrl: "https://dev.example.com" },
      secretRefs: { token: "http-client/token" },
    });
    expect(Object.values(environment!.values)).not.toContain("top-secret");
  });

  it("sends the selected request and formats JSON without changing raw body", async () => {
    const mock = backend();
    setHTTPClientBackend(mock);
    httpClientState.requests = [request];
    httpClientState.selectedIndex = 0;

    const response = await sendHTTPRequest({ timeoutMs: 2_000, maxResponseBytes: 1_024 });

    expect(mock.sendRequest).toHaveBeenCalledWith(request, expect.objectContaining({
      requestId: expect.any(String),
      timeoutMs: 2_000,
      maxResponseBytes: 1_024,
    }));
    expect(response?.status).toBe(200);
    expect(formatHTTPResponseBody(response!)).toBe(`{\n  "ok": true\n}`);
    expect(response?.body).toBe(`{"ok":true}`);
    expect(httpClientState.loading).toBe(false);
  });

  it("binds private-network authorization to the request id and token", async () => {
    const mock = backend();
    setHTTPClientBackend(mock);
    const privateRequest = { ...request, url: "http://127.0.0.1:8080/health" };
    httpClientState.requests = [privateRequest];
    httpClientState.selectedIndex = 0;

    await expect(authorizeSelectedPrivateNetwork()).resolves.toBe(true);
    const [, approvedRequestID] = vi.mocked(mock.requestPrivateNetworkAccess).mock.calls[0]!;
    await sendHTTPRequest();

    expect(mock.requestPrivateNetworkAccess).toHaveBeenCalledWith(
      "http://127.0.0.1:8080",
      expect.any(String),
    );
    expect(mock.sendRequest).toHaveBeenCalledWith(privateRequest, expect.objectContaining({
      requestId: approvedRequestID,
      privateNetworkToken: "private-token",
    }));
    expect(httpClientState.privateNetworkApproval).toBeNull();
  });

  it("cancels the active backend request", async () => {
    let finishSend!: (value: Awaited<ReturnType<HTTPClientBackend["sendRequest"]>>) => void;
    const mock = backend();
    mock.sendRequest = vi.fn(() => new Promise<Awaited<ReturnType<HTTPClientBackend["sendRequest"]>>>((resolve) => {
      finishSend = resolve;
    }));
    setHTTPClientBackend(mock);
    httpClientState.requests = [request];
    httpClientState.selectedIndex = 0;

    void sendHTTPRequest();
    await Promise.resolve();
    const activeId = httpClientState.activeRequestId;
    expect(activeId).toBeTruthy();
    expect(await cancelActiveHTTPRequest()).toBe(true);
    expect(mock.cancelRequest).toHaveBeenCalledWith(activeId);
    finishSend({
      requestId: activeId!,
      status: 499,
      statusText: "Cancelled",
      headers: {},
      body: "",
      durationMs: 1,
    });
  });

  it("loads and clears sanitized history through the backend", async () => {
    const mock = backend();
    setHTTPClientBackend(mock);

    await loadHTTPHistory();
    expect(httpClientState.history).toHaveLength(1);
    await clearHTTPHistory();
    expect(mock.clearHistory).toHaveBeenCalledOnce();
    expect(httpClientState.history).toEqual([]);
  });
});
