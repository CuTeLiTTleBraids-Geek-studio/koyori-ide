import { flushPromises, mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";
import HTTPClientPanel from "./HTTPClientPanel.vue";
import {
  resetHTTPClientStore,
  setHTTPClientBackend,
  type HTTPClientBackend,
} from "@/stores/httpClient";

function backend(): HTTPClientBackend {
  return {
    parseHTTPEnvironment: vi.fn().mockResolvedValue({ values: {}, secretRefs: {} }),
    parseHTTPFile: vi.fn().mockImplementation(async (source: string) => {
      const match = source.match(/https?:\/\/\S+/);
      return [{
        name: "health",
        method: "GET",
        url: match?.[0] ?? "https://example.com/health",
        headers: {},
        body: "",
        startLine: 1,
        endLine: 1,
        secretRefs: {},
      }];
    }),
    requestPrivateNetworkAccess: vi.fn().mockResolvedValue("private-token"),
    sendRequest: vi.fn().mockResolvedValue({
      requestId: "response-1",
      status: 200,
      statusText: "OK",
      headers: { "Content-Type": "application/json" },
      body: `{"healthy":true}`,
      durationMs: 8,
    }),
    cancelRequest: vi.fn().mockResolvedValue(true),
    getHistory: vi.fn().mockResolvedValue([]),
    clearHistory: vi.fn().mockResolvedValue(undefined),
  };
}

describe("HTTPClientPanel", () => {
  beforeEach(() => {
    resetHTTPClientStore();
  });

  it("runs the request at the cursor and renders status, duration, headers, and JSON", async () => {
    const mock = backend();
    setHTTPClientBackend(mock);
    const wrapper = mount(HTTPClientPanel, {
      props: { source: "GET https://example.com/health", cursorLine: 1 },
    });
    await flushPromises();

    await wrapper.get('[data-testid="http-run"]').trigger("click");
    await flushPromises();

    expect(mock.sendRequest).toHaveBeenCalledOnce();
    expect(wrapper.get('[data-testid="http-status"]').text()).toContain("200 OK");
    expect(wrapper.get('[data-testid="http-duration"]').text()).toContain("8 ms");
    expect(wrapper.get('[data-testid="http-response-headers"]').text()).toContain("Content-Type");
    expect(wrapper.get('[data-testid="http-response-body"]').text()).toContain('"healthy": true');
  });

  it("requests backend private-network authorization before sending", async () => {
    const mock = backend();
    setHTTPClientBackend(mock);
    const wrapper = mount(HTTPClientPanel, {
      props: { source: "GET http://127.0.0.1:8080/health", cursorLine: 1 },
    });
    await flushPromises();

    await wrapper.get('[data-testid="http-private-network"]').setValue(true);
    await flushPromises();
    await wrapper.get('[data-testid="http-run"]').trigger("click");
    await flushPromises();

    expect(mock.requestPrivateNetworkAccess).toHaveBeenCalledWith(
      "http://127.0.0.1:8080",
      expect.any(String),
    );
    expect(mock.sendRequest).toHaveBeenCalledWith(expect.anything(), expect.objectContaining({
      privateNetworkToken: "private-token",
    }));
    expect(mock.sendRequest).not.toHaveBeenCalledWith(expect.anything(), expect.objectContaining({
      allowPrivateNetwork: true,
    }));
  });

  it("shows a backend authorization rejection and does not send", async () => {
    const mock = backend();
    vi.mocked(mock.requestPrivateNetworkAccess).mockRejectedValue(
      new Error("private-network access was not approved"),
    );
    setHTTPClientBackend(mock);
    const wrapper = mount(HTTPClientPanel, {
      props: { source: "GET http://127.0.0.1:8080/health", cursorLine: 1 },
    });
    await flushPromises();

    await wrapper.get('[data-testid="http-private-network"]').setValue(true);
    await flushPromises();

    expect(wrapper.get('[role="alert"]').text()).toContain("not approved");
    expect((wrapper.get('[data-testid="http-private-network"]').element as HTMLInputElement).checked)
      .toBe(false);
    expect(mock.sendRequest).not.toHaveBeenCalled();
  });
});
