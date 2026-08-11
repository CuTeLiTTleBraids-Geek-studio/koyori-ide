import { describe, it, expect, vi, beforeEach } from "vitest";
import {
  assertExtensionCommandId,
  DEFAULT_WORKER_SCRIPT_URL,
  decodeExtensionCommandOwner,
  encodeExtensionCommandOwner,
  getExtensionCommandPrefix,
  PluginSandboxHost,
  resetPluginSandboxState,
  hasPermissionForMethod,
  type WorkerLike,
  type RpcHandler,
  type WorkerToHostMessage,
  type HostToWorkerMessage,
} from "./pluginSandbox";
import type { PluginManifest } from "@/types";

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

/** A mock Worker that simulates message passing. */
class MockWorker implements WorkerLike {
  onmessage: ((e: { data: unknown }) => void) | null = null;
  onerror: ((e: unknown) => void) | null = null;
  onmessageerror: ((e: unknown) => void) | null = null;
  terminated = false;
  receivedMessages: HostToWorkerMessage[] = [];
  lifecycle: string[] = [];
  autoAcknowledgeTermination = true;
  terminationAck: "deactivated" | "terminated" = "terminated";

  postMessage(message: unknown): void {
    const typed = message as HostToWorkerMessage;
    this.receivedMessages.push(typed);
    if (typed.type === "terminate") {
      this.lifecycle.push("post:terminate");
      if (this.autoAcknowledgeTermination) {
        this.sendToHost(
          this.terminationAck === "terminated"
            ? { type: "terminated" }
            : { type: "deactivated" },
        );
      }
    }
  }

  terminate(): void {
    this.lifecycle.push("terminate");
    this.terminated = true;
  }

  /** Simulate the worker sending a message to the host. */
  sendToHost(msg: WorkerToHostMessage): void {
    if (this.onmessage) {
      this.onmessage({ data: msg });
    }
  }
}

function makeManifest(overrides?: Partial<PluginManifest>): PluginManifest {
  return {
    name: "test-plugin",
    version: "1.0.0",
    main: "main.js",
    permissions: [],
    ...overrides,
  };
}

function makeMockWorkerFactory(): {
  factory: (url: string) => WorkerLike;
  workers: MockWorker[];
  scriptUrls: string[];
} {
  const workers: MockWorker[] = [];
  const scriptUrls: string[] = [];
  const factory = (url: string) => {
    scriptUrls.push(url);
    const w = new MockWorker();
    workers.push(w);
    return w;
  };
  return { factory, workers, scriptUrls };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("Plugin Sandbox (N-26)", () => {
  describe("extension command owner encoding", () => {
    it.each([
      "alpha",
      "alpha.beta",
      "alpha%2Ebeta",
      "publisher/extension",
      "\u7ec4\u7ec7.\u5de5\u5177",
    ])("round-trips %s without owner aliases", (extensionId) => {
      const encodedOwner = encodeExtensionCommandOwner(extensionId);

      expect(decodeExtensionCommandOwner(encodedOwner)).toBe(extensionId);
    });

    it("keeps dotted IDs outside their prefix owner's namespace", () => {
      expect(getExtensionCommandPrefix("alpha")).toBe("extension.alpha.");
      expect(getExtensionCommandPrefix("alpha.beta")).toBe(
        "extension.alpha%2Ebeta.",
      );
      expect(getExtensionCommandPrefix("alpha%2Ebeta")).toBe(
        "extension.alpha%252Ebeta.",
      );
      expect(
        getExtensionCommandPrefix("alpha.beta").startsWith(
          getExtensionCommandPrefix("alpha"),
        ),
      ).toBe(false);

      expect(() =>
        assertExtensionCommandId(
          "alpha",
          `${getExtensionCommandPrefix("alpha.beta")}run`,
        ),
      ).toThrow(getExtensionCommandPrefix("alpha"));
      expect(() =>
        assertExtensionCommandId(
          "alpha.beta",
          `${getExtensionCommandPrefix("alpha")}run`,
        ),
      ).toThrow(getExtensionCommandPrefix("alpha.beta"));
    });
  });

  describe("hasPermissionForMethod", () => {
    it("returns true for methods that require no permission", () => {
      const manifest = makeManifest();
      expect(hasPermissionForMethod(manifest, "commands.register")).toBe(true);
      expect(hasPermissionForMethod(manifest, "commands.execute")).toBe(true);
      expect(hasPermissionForMethod(manifest, "views.register")).toBe(true);
      expect(hasPermissionForMethod(manifest, "getPermissions")).toBe(true);
    });

    it("returns false for fs.read when permission is not declared", () => {
      const manifest = makeManifest({ permissions: [] });
      expect(hasPermissionForMethod(manifest, "workspace.readFile")).toBe(false);
    });

    it("returns true for fs.read when permission is declared", () => {
      const manifest = makeManifest({ permissions: ["fs.read"] });
      expect(hasPermissionForMethod(manifest, "workspace.readFile")).toBe(true);
    });

    it("returns false for fs.write when only fs.read is declared", () => {
      const manifest = makeManifest({ permissions: ["fs.read"] });
      expect(hasPermissionForMethod(manifest, "workspace.writeFile")).toBe(false);
    });

    it("returns true for fs.write when permission is declared", () => {
      const manifest = makeManifest({ permissions: ["fs.write"] });
      expect(hasPermissionForMethod(manifest, "workspace.writeFile")).toBe(true);
    });

    it("returns true when both fs.read and fs.write are declared", () => {
      const manifest = makeManifest({ permissions: ["fs.read", "fs.write"] });
      expect(hasPermissionForMethod(manifest, "workspace.readFile")).toBe(true);
      expect(hasPermissionForMethod(manifest, "workspace.writeFile")).toBe(true);
    });
  });

  describe("PluginSandboxHost", () => {
    let rpcHandler: RpcHandler;
    let mockFactory: ReturnType<typeof makeMockWorkerFactory>;

    beforeEach(() => {
      rpcHandler = vi.fn().mockResolvedValue(undefined);
      mockFactory = makeMockWorkerFactory();
    });

    describe("activate", () => {
      it("constructs the default Worker from Vite's module URL", async () => {
        const constructorCalls: Array<{
          url: string | URL;
          options?: WorkerOptions;
        }> = [];
        const workers: MockWorker[] = [];
        class DefaultWorkerMock extends MockWorker {
          constructor(url: string | URL, options?: WorkerOptions) {
            super();
            constructorCalls.push({ url, options });
            workers.push(this);
          }
        }
        vi.stubGlobal("Worker", DefaultWorkerMock);

        try {
          const host = new PluginSandboxHost(rpcHandler);
          const activation = host.activate(
            "test-plugin",
            makeManifest(),
            "/_plugins/test-plugin/main.js",
          );
          expect(workers).toHaveLength(1);
          workers[0].sendToHost({ type: "activated" });
          await activation;

          expect(constructorCalls).toHaveLength(1);
          expect(String(constructorCalls[0].url)).toContain("pluginWorkerBootstrap");
          expect(String(constructorCalls[0].url)).not.toBe(
            "/pluginWorkerBootstrap.js",
          );
          expect(constructorCalls[0].options).toEqual({ type: "module" });
        } finally {
          vi.unstubAllGlobals();
        }
      });

      it("preserves an explicit custom module Worker URL", async () => {
        const constructorCalls: Array<{
          url: string | URL;
          options?: WorkerOptions;
        }> = [];
        const workers: MockWorker[] = [];
        class CustomUrlWorkerMock extends MockWorker {
          constructor(url: string | URL, options?: WorkerOptions) {
            super();
            constructorCalls.push({ url, options });
            workers.push(this);
          }
        }
        vi.stubGlobal("Worker", CustomUrlWorkerMock);

        try {
          const customUrl = "https://plugins.example.test/custom-bootstrap.js";
          const host = new PluginSandboxHost(rpcHandler, {
            workerScriptUrl: customUrl,
          });
          const activation = host.activate(
            "test-plugin",
            makeManifest(),
            "/_plugins/test-plugin/main.js",
          );
          workers[0].sendToHost({ type: "activated" });
          await activation;

          expect(constructorCalls).toEqual([
            { url: customUrl, options: { type: "module" } },
          ]);
        } finally {
          vi.unstubAllGlobals();
        }
      });

      it("creates a worker and sends init message", async () => {
        const host = new PluginSandboxHost(rpcHandler, {
          workerFactory: mockFactory.factory,
        });
        const manifest = makeManifest();
        const pluginUrl = "/_plugins/test-plugin/main.js";

        // Start activation (don't await yet — the worker hasn't responded).
        const promise = host.activate("test-plugin", manifest, pluginUrl);

        // The factory should have created one worker.
        expect(mockFactory.workers).toHaveLength(1);
        expect(mockFactory.scriptUrls).toEqual([DEFAULT_WORKER_SCRIPT_URL]);
        const worker = mockFactory.workers[0];

        // The host should have sent an init message.
        expect(worker.receivedMessages).toHaveLength(1);
        const initMsg = worker.receivedMessages[0];
        expect(initMsg.type).toBe("init");
        if (initMsg.type === "init") {
          expect(initMsg.pluginUrl).toBe(pluginUrl);
          expect(initMsg.manifest.name).toBe("test-plugin");
        }

        // Simulate the worker reporting successful activation.
        worker.sendToHost({ type: "activated" });

        await expect(promise).resolves.toBeUndefined();
      });

      it("rejects when the worker reports activation-error", async () => {
        const host = new PluginSandboxHost(rpcHandler, {
          workerFactory: mockFactory.factory,
        });
        const manifest = makeManifest();

        const promise = host.activate("test-plugin", manifest, "/url");

        const worker = mockFactory.workers[0];
        worker.sendToHost({ type: "activation-error", error: "Plugin failed to load" });

        await expect(promise).rejects.toThrow("Plugin failed to load");
        await Promise.resolve();
        expect(mockFactory.workers).toHaveLength(1);
      });

      it("rejects when the worker errors", async () => {
        const host = new PluginSandboxHost(rpcHandler, {
          workerFactory: mockFactory.factory,
        });
        const manifest = makeManifest();

        const promise = host.activate("test-plugin", manifest, "/url");

        const worker = mockFactory.workers[0];
        worker.onerror?.(new Error("Worker crashed"));

        await expect(promise).rejects.toThrow("Worker error: Worker crashed");
      });

      it("M-18: activate is idempotent — second call is a no-op (worker NOT terminated+rebuilt)", async () => {
        const host = new PluginSandboxHost(rpcHandler, {
          workerFactory: mockFactory.factory,
        });
        const manifest = makeManifest();

        // First activation succeeds.
        const promise1 = host.activate("test-plugin", manifest, "/url1");
        const firstWorker = mockFactory.workers[0];
        firstWorker.sendToHost({ type: "activated" });
        await promise1;

        // Second activate() must NOT terminate/rebuild the worker.
        const result = host.activate("test-plugin", manifest, "/url2");

        // Same activation promise is returned (no new worker created).
        expect(result).toBe(promise1);
        // The factory was called only once — no rebuild.
        expect(mockFactory.workers).toHaveLength(1);
        // The original worker was NOT terminated.
        expect(firstWorker.terminated).toBe(false);
        // No extra init message was sent to the worker.
        expect(firstWorker.receivedMessages).toHaveLength(1);
      });

      it("M-18: reload force-rebuilds the worker (terminates old, creates new)", async () => {
        const host = new PluginSandboxHost(rpcHandler, {
          workerFactory: mockFactory.factory,
        });
        const manifest = makeManifest();

        // First activation.
        const promise1 = host.activate("test-plugin", manifest, "/url1");
        mockFactory.workers[0].sendToHost({ type: "activated" });
        await promise1;

        // reload() — should terminate the first worker and build a fresh one.
        const promise2 = host.reload("test-plugin", manifest, "/url2");
        expect(mockFactory.workers[0].terminated).toBe(true);
        mockFactory.workers[1].sendToHost({ type: "activated" });
        await promise2;

        expect(mockFactory.workers).toHaveLength(2);
      });

      it("ignores queued messages from a worker replaced during reload", async () => {
        const host = new PluginSandboxHost(rpcHandler, {
          workerFactory: mockFactory.factory,
        });
        const manifest = makeManifest();

        const firstActivation = host.activate("test-plugin", manifest, "/url1");
        const firstWorker = mockFactory.workers[0];
        const firstRejected = expect(firstActivation).rejects.toThrow(
          "Worker terminated during activation",
        );

        const secondActivation = host.reload("test-plugin", manifest, "/url2");
        const secondWorker = mockFactory.workers[1];
        let secondResolved = false;
        void secondActivation.then(() => {
          secondResolved = true;
        });

        firstWorker.sendToHost({ type: "activated" });
        await Promise.resolve();
        expect(secondResolved).toBe(false);

        secondWorker.sendToHost({ type: "activated" });
        await expect(secondActivation).resolves.toBeUndefined();
        await firstRejected;
      });
    });

    describe("RPC request handling", () => {
      it("routes rpc-request to the rpcHandler and sends the result back", async () => {
        const handler = vi.fn().mockResolvedValue("file contents");
        const host = new PluginSandboxHost(handler, {
          workerFactory: mockFactory.factory,
        });
        const manifest = makeManifest({ permissions: ["fs.read"] });

        const activatePromise = host.activate("test-plugin", manifest, "/url");
        const worker = mockFactory.workers[0];
        worker.sendToHost({ type: "activated" });
        await activatePromise;

        // Simulate the worker requesting a file read.
        worker.sendToHost({
          type: "rpc-request",
          id: 42,
          method: "workspace.readFile",
          args: ["src/main.ts"],
        });

        // Wait for the async handler to complete.
        await new Promise((r) => setTimeout(r, 10));

        // The handler should have been called with the right args.
        expect(handler).toHaveBeenCalledWith(
          "test-plugin",
          manifest,
          "workspace.readFile",
          ["src/main.ts"],
        );

        // The host should have sent an rpc-response with the result.
        const response = worker.receivedMessages.find(
          (m) => m.type === "rpc-response",
        );
        expect(response).toBeDefined();
        if (response && response.type === "rpc-response") {
          expect(response.id).toBe(42);
          expect(response.result).toBe("file contents");
          expect(response.error).toBeUndefined();
        }
      });

      it("sends an error response when the handler throws", async () => {
        const handler = vi.fn().mockRejectedValue(new Error("File not found"));
        const host = new PluginSandboxHost(handler, {
          workerFactory: mockFactory.factory,
        });
        const manifest = makeManifest({ permissions: ["fs.read"] });

        const activatePromise = host.activate("test-plugin", manifest, "/url");
        const worker = mockFactory.workers[0];
        worker.sendToHost({ type: "activated" });
        await activatePromise;

        worker.sendToHost({
          type: "rpc-request",
          id: 1,
          method: "workspace.readFile",
          args: ["missing.ts"],
        });

        await new Promise((r) => setTimeout(r, 10));

        const response = worker.receivedMessages.find(
          (m) => m.type === "rpc-response",
        );
        expect(response).toBeDefined();
        if (response && response.type === "rpc-response") {
          expect(response.id).toBe(1);
          expect(response.error).toBe("File not found");
        }
      });

      it("rejects fs.read when permission is not declared", async () => {
        const handler = vi.fn();
        const host = new PluginSandboxHost(handler, {
          workerFactory: mockFactory.factory,
        });
        const manifest = makeManifest({ permissions: [] }); // no fs.read

        const activatePromise = host.activate("test-plugin", manifest, "/url");
        const worker = mockFactory.workers[0];
        worker.sendToHost({ type: "activated" });
        await activatePromise;

        worker.sendToHost({
          type: "rpc-request",
          id: 1,
          method: "workspace.readFile",
          args: ["file.ts"],
        });

        await new Promise((r) => setTimeout(r, 10));

        // The handler should NOT have been called (permission denied).
        expect(handler).not.toHaveBeenCalled();

        // The response should contain the permission error.
        const response = worker.receivedMessages.find(
          (m) => m.type === "rpc-response",
        );
        expect(response).toBeDefined();
        if (response && response.type === "rpc-response") {
          expect(response.error).toContain("fs.read");
          expect(response.error).toContain("not declared");
        }
      });

      it("allows an extension-owned command without any permission", async () => {
        const handler = vi.fn().mockResolvedValue(undefined);
        const host = new PluginSandboxHost(handler, {
          workerFactory: mockFactory.factory,
        });
        const manifest = makeManifest({ permissions: [] });

        const activatePromise = host.activate("test-plugin", manifest, "/url");
        const worker = mockFactory.workers[0];
        worker.sendToHost({ type: "activated" });
        await activatePromise;

        worker.sendToHost({
          type: "rpc-request",
          id: 1,
          method: "commands.register",
          args: [`${getExtensionCommandPrefix("test-plugin")}cmd`],
        });

        await new Promise((r) => setTimeout(r, 10));

        expect(handler).toHaveBeenCalled();
        const response = worker.receivedMessages.find(
          (m) => m.type === "rpc-response",
        );
        expect(response).toBeDefined();
        if (response && response.type === "rpc-response") {
          expect(response.error).toBeUndefined();
        }
      });

      it.each([
        ["short owner first", ["alpha", "alpha.beta"]],
        ["dotted owner first", ["alpha.beta", "alpha"]],
      ] as const)(
        "isolates dotted extension owners with %s",
        async (_case, activationOrder) => {
          const handler = vi.fn().mockResolvedValue(undefined);
          const host = new PluginSandboxHost(handler, {
            workerFactory: mockFactory.factory,
          });
          const workerByExtension = new Map<string, MockWorker>();

          for (const extensionId of activationOrder) {
            const activatePromise = host.activate(
              extensionId,
              makeManifest({ name: extensionId }),
              `/_plugins/${extensionId}/main.js`,
            );
            const worker = mockFactory.workers[mockFactory.workers.length - 1];
            workerByExtension.set(extensionId, worker);
            worker.sendToHost({ type: "activated" });
            await activatePromise;
          }

          const commandByExtension = new Map([
            ["alpha", `${getExtensionCommandPrefix("alpha")}shared`],
            ["alpha.beta", `${getExtensionCommandPrefix("alpha.beta")}shared`],
          ]);

          let requestId = 1;
          for (const extensionId of activationOrder) {
            workerByExtension.get(extensionId)!.sendToHost({
              type: "rpc-request",
              id: requestId++,
              method: "commands.register",
              args: [commandByExtension.get(extensionId)!],
            });
          }

          await new Promise((resolve) => setTimeout(resolve, 10));

          expect(handler).toHaveBeenCalledTimes(2);
          for (const extensionId of activationOrder) {
            expect(handler).toHaveBeenCalledWith(
              extensionId,
              expect.objectContaining({ name: extensionId }),
              "commands.register",
              [commandByExtension.get(extensionId)],
            );
          }

          const foreignAttempts = [
            {
              extensionId: "alpha",
              commandId: commandByExtension.get("alpha.beta")!,
              requiredPrefix: getExtensionCommandPrefix("alpha"),
            },
            {
              extensionId: "alpha.beta",
              commandId: commandByExtension.get("alpha")!,
              requiredPrefix: getExtensionCommandPrefix("alpha.beta"),
            },
          ];

          for (const attempt of foreignAttempts) {
            const foreignRequestId = requestId++;
            const worker = workerByExtension.get(attempt.extensionId)!;
            worker.sendToHost({
              type: "rpc-request",
              id: foreignRequestId,
              method: "commands.register",
              args: [attempt.commandId],
            });

            await new Promise((resolve) => setTimeout(resolve, 10));

            const response = worker.receivedMessages.find(
              (message) =>
                message.type === "rpc-response" && message.id === foreignRequestId,
            );
            expect(response).toMatchObject({
              type: "rpc-response",
              id: foreignRequestId,
              error: expect.stringContaining(attempt.requiredPrefix),
            });
          }

          expect(handler).toHaveBeenCalledTimes(2);
        },
      );

      it.each([
        ["legacy plugin prefix", "my-plugin.cmd", "extension.test-plugin."],
        ["workbench namespace", "workbench.action.quit", "reserved"],
        ["editor namespace", "editor.action.formatDocument", "reserved"],
        ["terminal namespace", "terminal.clear", "reserved"],
        ["git namespace", "git.commit", "reserved"],
      ])("rejects commands.register using the %s", async (_case, commandId, errorText) => {
        const handler = vi.fn().mockResolvedValue(undefined);
        const host = new PluginSandboxHost(handler, {
          workerFactory: mockFactory.factory,
        });
        const manifest = makeManifest({ permissions: [] });

        const activatePromise = host.activate("test-plugin", manifest, "/url");
        const worker = mockFactory.workers[0];
        worker.sendToHost({ type: "activated" });
        await activatePromise;

        worker.sendToHost({
          type: "rpc-request",
          id: 1,
          method: "commands.register",
          args: [commandId],
        });

        await new Promise((r) => setTimeout(r, 10));

        expect(handler).not.toHaveBeenCalled();
        const response = worker.receivedMessages.find(
          (m) => m.type === "rpc-response",
        );
        expect(response).toBeDefined();
        if (response && response.type === "rpc-response") {
          expect(response.error).toContain(commandId);
          expect(response.error).toContain(errorText);
        }
      });

      it("allows views.register without any permission", async () => {
        const handler = vi.fn().mockResolvedValue(undefined);
        const host = new PluginSandboxHost(handler, {
          workerFactory: mockFactory.factory,
        });
        const manifest = makeManifest({ permissions: [] });

        const activatePromise = host.activate("test-plugin", manifest, "/url");
        const worker = mockFactory.workers[0];
        worker.sendToHost({ type: "activated" });
        await activatePromise;

        worker.sendToHost({
          type: "rpc-request",
          id: 1,
          method: "views.register",
          args: ["my-view", { title: "My View" }],
        });

        await new Promise((r) => setTimeout(r, 10));

        expect(handler).toHaveBeenCalled();
      });

      it("allows getPermissions without any permission", async () => {
        const handler = vi.fn().mockResolvedValue(["fs.read"]);
        const host = new PluginSandboxHost(handler, {
          workerFactory: mockFactory.factory,
        });
        const manifest = makeManifest({ permissions: ["fs.read"] });

        const activatePromise = host.activate("test-plugin", manifest, "/url");
        const worker = mockFactory.workers[0];
        worker.sendToHost({ type: "activated" });
        await activatePromise;

        worker.sendToHost({
          type: "rpc-request",
          id: 1,
          method: "getPermissions",
          args: [],
        });

        await new Promise((r) => setTimeout(r, 10));

        expect(handler).toHaveBeenCalled();
        const response = worker.receivedMessages.find(
          (m) => m.type === "rpc-response",
        );
        if (response && response.type === "rpc-response") {
          expect(response.result).toEqual(["fs.read"]);
        }
      });
    });

    describe("log forwarding", () => {
      it("forwards log messages to console", async () => {
        const consoleSpy = vi.spyOn(console, "info").mockImplementation(() => {});
        const host = new PluginSandboxHost(rpcHandler, {
          workerFactory: mockFactory.factory,
        });
        const manifest = makeManifest();

        const promise = host.activate("test-plugin", manifest, "/url");
        const worker = mockFactory.workers[0];
        worker.sendToHost({ type: "activated" });
        await promise;

        worker.sendToHost({ type: "log", level: "info", message: "Hello from plugin" });

        expect(consoleSpy).toHaveBeenCalledWith("[plugin:test-plugin] Hello from plugin");
        consoleSpy.mockRestore();
      });

      it("forwards warn messages to console.warn", async () => {
        const consoleSpy = vi.spyOn(console, "warn").mockImplementation(() => {});
        const host = new PluginSandboxHost(rpcHandler, {
          workerFactory: mockFactory.factory,
        });
        const manifest = makeManifest();

        const promise = host.activate("test-plugin", manifest, "/url");
        const worker = mockFactory.workers[0];
        worker.sendToHost({ type: "activated" });
        await promise;

        worker.sendToHost({ type: "log", level: "warn", message: "Warning!" });

        expect(consoleSpy).toHaveBeenCalledWith("[plugin:test-plugin] Warning!");
        consoleSpy.mockRestore();
      });
    });

    describe("terminate", () => {
      it("rejects an activation that is still pending", async () => {
        const host = new PluginSandboxHost(rpcHandler, {
          workerFactory: mockFactory.factory,
        });
        const promise = host.activate("test-plugin", makeManifest(), "/url");

        host.terminate("test-plugin");

        await expect(promise).rejects.toThrow("Worker terminated during activation");
        expect(host.has("test-plugin")).toBe(false);
      });

      it("terminates the worker and removes the sandbox", async () => {
        const host = new PluginSandboxHost(rpcHandler, {
          workerFactory: mockFactory.factory,
        });
        const manifest = makeManifest();

        const promise = host.activate("test-plugin", manifest, "/url");
        const worker = mockFactory.workers[0];
        worker.sendToHost({ type: "activated" });
        await promise;

        expect(host.has("test-plugin")).toBe(true);

        host.terminate("test-plugin");

        expect(worker.terminated).toBe(true);
        expect(host.has("test-plugin")).toBe(false);
      });

      it("sends a terminate message before terminating", async () => {
        const host = new PluginSandboxHost(rpcHandler, {
          workerFactory: mockFactory.factory,
        });
        const manifest = makeManifest();

        const promise = host.activate("test-plugin", manifest, "/url");
        const worker = mockFactory.workers[0];
        worker.sendToHost({ type: "activated" });
        await promise;

        host.terminate("test-plugin");

        // The last message should be 'terminate'.
        const lastMsg = worker.receivedMessages[worker.receivedMessages.length - 1];
        expect(lastMsg.type).toBe("terminate");
      });

      it("waits for a worker shutdown acknowledgement before native terminate", async () => {
        const host = new PluginSandboxHost(rpcHandler, {
          workerFactory: mockFactory.factory,
        });
        const activation = host.activate("test-plugin", makeManifest(), "/url");
        const worker = mockFactory.workers[0];
        worker.sendToHost({ type: "activated" });
        await activation;
        worker.autoAcknowledgeTermination = false;

        host.terminate("test-plugin");

        expect(host.has("test-plugin")).toBe(false);
        expect(worker.lifecycle).toEqual(["post:terminate"]);
        expect(worker.terminated).toBe(false);

        worker.sendToHost({ type: "deactivated" });

        expect(worker.lifecycle).toEqual(["post:terminate", "terminate"]);
        expect(worker.terminated).toBe(true);
      });

      it("force-terminates an unresponsive worker after the grace timeout", async () => {
        vi.useFakeTimers();
        try {
          const host = new PluginSandboxHost(rpcHandler, {
            workerFactory: mockFactory.factory,
          });
          const activation = host.activate("test-plugin", makeManifest(), "/url");
          const worker = mockFactory.workers[0];
          worker.sendToHost({ type: "activated" });
          await activation;
          worker.autoAcknowledgeTermination = false;

          host.terminate("test-plugin");
          expect(worker.terminated).toBe(false);

          await vi.runAllTimersAsync();

          expect(worker.lifecycle).toEqual(["post:terminate", "terminate"]);
          expect(worker.terminated).toBe(true);
        } finally {
          vi.useRealTimers();
        }
      });

      it("force-terminates pending shutdowns immediately during a full reset", async () => {
        const host = new PluginSandboxHost(rpcHandler, {
          workerFactory: mockFactory.factory,
        });
        const activation = host.activate("test-plugin", makeManifest(), "/url");
        const worker = mockFactory.workers[0];
        worker.sendToHost({ type: "activated" });
        await activation;
        worker.autoAcknowledgeTermination = false;

        host.reset();

        expect(worker.lifecycle).toEqual(["post:terminate", "terminate"]);
        expect(worker.terminated).toBe(true);
      });

      it("is a no-op for non-existent plugin", () => {
        const host = new PluginSandboxHost(rpcHandler, {
          workerFactory: mockFactory.factory,
        });
        // Should not throw.
        host.terminate("nonexistent");
      });
    });

    describe("terminateAll", () => {
      it("terminates all sandboxed plugins", async () => {
        const host = new PluginSandboxHost(rpcHandler, {
          workerFactory: mockFactory.factory,
        });

        // Activate two plugins.
        const p1 = host.activate("plugin1", makeManifest({ name: "plugin1" }), "/url1");
        mockFactory.workers[0].sendToHost({ type: "activated" });
        await p1;

        const p2 = host.activate("plugin2", makeManifest({ name: "plugin2" }), "/url2");
        mockFactory.workers[1].sendToHost({ type: "activated" });
        await p2;

        expect(host.has("plugin1")).toBe(true);
        expect(host.has("plugin2")).toBe(true);

        host.terminateAll();

        expect(mockFactory.workers[0].terminated).toBe(true);
        expect(mockFactory.workers[1].terminated).toBe(true);
        expect(host.has("plugin1")).toBe(false);
        expect(host.has("plugin2")).toBe(false);
      });

      it("resets every tracked sandbox host for HMR", async () => {
        const host = new PluginSandboxHost(rpcHandler, {
          workerFactory: mockFactory.factory,
        });
        const activation = host.activate("test-plugin", makeManifest(), "/url");
        mockFactory.workers[0].sendToHost({ type: "activated" });
        await activation;

        resetPluginSandboxState();

        expect(mockFactory.workers[0].terminated).toBe(true);
        expect(host.has("test-plugin")).toBe(false);
      });
    });

    describe("has", () => {
      it("returns false for non-sandboxed plugins", () => {
        const host = new PluginSandboxHost(rpcHandler, {
          workerFactory: mockFactory.factory,
        });
        expect(host.has("nonexistent")).toBe(false);
      });

      it("returns true for sandboxed plugins", async () => {
        const host = new PluginSandboxHost(rpcHandler, {
          workerFactory: mockFactory.factory,
        });
        const promise = host.activate("test-plugin", makeManifest(), "/url");
        mockFactory.workers[0].sendToHost({ type: "activated" });
        await promise;

        expect(host.has("test-plugin")).toBe(true);
      });
    });

    describe("callMethod (N-31)", () => {
      it("rejects for non-sandboxed plugin", async () => {
        const host = new PluginSandboxHost(rpcHandler, {
          workerFactory: mockFactory.factory,
        });
        await expect(host.callMethod("nonexistent", "executeCommand", [])).rejects.toThrow(
          'Plugin "nonexistent" is not sandboxed',
        );
      });

      it("sends rpc-call to worker and resolves on rpc-result", async () => {
        const host = new PluginSandboxHost(rpcHandler, {
          workerFactory: mockFactory.factory,
        });
        const manifest = makeManifest();
        const promise = host.activate("test-plugin", manifest, "/url");
        const worker = mockFactory.workers[0];
        worker.sendToHost({ type: "activated" });
        await promise;

        // Initiate callMethod — this sends an rpc-call to the worker.
        const callPromise = host.callMethod("test-plugin", "executeCommand", ["my-cmd"]);
        // Verify the worker received the rpc-call message.
        const callMsg = worker.receivedMessages.find(
          (m) => m.type === "rpc-call",
        ) as { type: "rpc-call"; id: number; method: string; args: unknown[] } | undefined;
        expect(callMsg).toBeDefined();
        expect(callMsg!.method).toBe("executeCommand");
        expect(callMsg!.args).toEqual(["my-cmd"]);

        // Simulate the worker responding with a result.
        worker.sendToHost({ type: "rpc-result", id: callMsg!.id, result: "ok" });

        const result = await callPromise;
        expect(result).toBe("ok");
      });

      it("does not let a stale rpc-result settle a replacement worker call", async () => {
        const host = new PluginSandboxHost(rpcHandler, {
          workerFactory: mockFactory.factory,
        });
        const manifest = makeManifest();
        const firstActivation = host.activate("test-plugin", manifest, "/url1");
        const firstWorker = mockFactory.workers[0];
        firstWorker.sendToHost({ type: "activated" });
        await firstActivation;

        const staleMessageHandler = firstWorker.onmessage;
        const firstCall = host.callMethod("test-plugin", "executeCommand", ["old"]);
        const firstCallRejection = expect(firstCall).rejects.toThrow("Worker terminated");
        const firstCallMessage = firstWorker.receivedMessages.find(
          (message) => message.type === "rpc-call",
        );
        expect(firstCallMessage).toMatchObject({ type: "rpc-call", id: 0 });

        const replacementActivation = host.reload("test-plugin", manifest, "/url2");
        const replacementWorker = mockFactory.workers[1];
        replacementWorker.sendToHost({ type: "activated" });
        await replacementActivation;
        await firstCallRejection;

        const replacementCall = host.callMethod(
          "test-plugin",
          "executeCommand",
          ["replacement"],
        );
        const replacementCallMessage = replacementWorker.receivedMessages.find(
          (message) => message.type === "rpc-call",
        );
        expect(replacementCallMessage).toMatchObject({ type: "rpc-call", id: 0 });

        let replacementSettled = false;
        void replacementCall.then(
          () => { replacementSettled = true; },
          () => { replacementSettled = true; },
        );
        staleMessageHandler?.({
          data: { type: "rpc-result", id: 0, result: "stale-result" },
        });
        await Promise.resolve();

        expect(replacementSettled).toBe(false);

        replacementWorker.sendToHost({
          type: "rpc-result",
          id: 0,
          result: "replacement-result",
        });
        await expect(replacementCall).resolves.toBe("replacement-result");
      });

      it("rejects on rpc-result with error", async () => {
        const host = new PluginSandboxHost(rpcHandler, {
          workerFactory: mockFactory.factory,
        });
        const manifest = makeManifest();
        const promise = host.activate("test-plugin", manifest, "/url");
        const worker = mockFactory.workers[0];
        worker.sendToHost({ type: "activated" });
        await promise;

        const callPromise = host.callMethod("test-plugin", "executeCommand", ["bad-cmd"]);
        const callMsg = worker.receivedMessages.find(
          (m) => m.type === "rpc-call",
        ) as { type: "rpc-call"; id: number } | undefined;
        expect(callMsg).toBeDefined();

        worker.sendToHost({ type: "rpc-result", id: callMsg!.id, error: "Command not found" });

        await expect(callPromise).rejects.toThrow("Command not found");
      });

      it("rejects cleanly when posting an rpc-call fails", async () => {
        const host = new PluginSandboxHost(rpcHandler, {
          workerFactory: mockFactory.factory,
        });
        const activation = host.activate("test-plugin", makeManifest(), "/url");
        const worker = mockFactory.workers[0];
        worker.sendToHost({ type: "activated" });
        await activation;
        const postError = new Error("DataCloneError: value could not be cloned");
        worker.postMessage = vi.fn(() => {
          throw postError;
        });

        await expect(
          host.callMethod("test-plugin", "executeCommand", [() => undefined]),
        ).rejects.toBe(postError);

        host.terminate("test-plugin");
        expect(host.has("test-plugin")).toBe(false);
      });

      it("rejects pending calls on terminate", async () => {
        const host = new PluginSandboxHost(rpcHandler, {
          workerFactory: mockFactory.factory,
        });
        const manifest = makeManifest();
        const promise = host.activate("test-plugin", manifest, "/url");
        const worker = mockFactory.workers[0];
        worker.sendToHost({ type: "activated" });
        await promise;

        const callPromise = host.callMethod("test-plugin", "executeCommand", ["slow-cmd"]);
        // Terminate before the worker responds.
        host.terminate("test-plugin");

        await expect(callPromise).rejects.toThrow("Worker terminated");
      });
    });

    // -------------------------------------------------------------------------
    // N-40 (prompt-5.md): Worker crash handling
    // -------------------------------------------------------------------------

    describe("N-40: Worker crash handling", () => {
      it("getHealth returns 'running' after successful activation", async () => {
        const host = new PluginSandboxHost(rpcHandler, {
          workerFactory: mockFactory.factory,
        });
        const manifest = makeManifest();
        const promise = host.activate("test-plugin", manifest, "/url");
        mockFactory.workers[0].sendToHost({ type: "activated" });
        await promise;

        const health = host.getHealth("test-plugin");
        expect(health.status).toBe("running");
        expect(health.lastError).toBeNull();
        expect(health.lastCrashAt).toBeNull();
      });

      it("getHealth returns 'terminated' for unknown plugin", () => {
        const host = new PluginSandboxHost(rpcHandler, {
          workerFactory: mockFactory.factory,
        });
        const health = host.getHealth("nonexistent");
        expect(health.status).toBe("terminated");
      });

      it("getHealth returns 'crashed' after runtime worker.onerror", async () => {
        const host = new PluginSandboxHost(rpcHandler, {
          workerFactory: mockFactory.factory,
        });
        const manifest = makeManifest();
        const promise = host.activate("test-plugin", manifest, "/url");
        const worker = mockFactory.workers[0];
        worker.sendToHost({ type: "activated" });
        await promise;

        // Simulate a runtime crash (after activation).
        worker.onerror?.(new Error("Uncaught TypeError: undefined is not a function"));

        const health = host.getHealth("test-plugin");
        expect(health.status).toBe("crashed");
        expect(health.lastError).toContain("Uncaught TypeError");
        expect(health.lastCrashAt).not.toBeNull();
      });

      it("rejects pending calls when worker crashes at runtime", async () => {
        const host = new PluginSandboxHost(rpcHandler, {
          workerFactory: mockFactory.factory,
        });
        const manifest = makeManifest();
        const promise = host.activate("test-plugin", manifest, "/url");
        const worker = mockFactory.workers[0];
        worker.sendToHost({ type: "activated" });
        await promise;

        // Start a call that the worker will never respond to.
        const callPromise = host.callMethod("test-plugin", "executeCommand", ["slow-cmd"]);

        // Crash the worker before it responds.
        worker.onerror?.(new Error("Worker crashed"));

        await expect(callPromise).rejects.toThrow("Worker crashed");
      });

      it("callMethod fails fast after crash", async () => {
        const host = new PluginSandboxHost(rpcHandler, {
          workerFactory: mockFactory.factory,
        });
        const manifest = makeManifest();
        const promise = host.activate("test-plugin", manifest, "/url");
        const worker = mockFactory.workers[0];
        worker.sendToHost({ type: "activated" });
        await promise;

        // Crash the worker.
        worker.onerror?.(new Error("OOM"));

        // Subsequent calls should reject immediately with the crash message.
        await expect(
          host.callMethod("test-plugin", "executeCommand", ["cmd"]),
        ).rejects.toThrow("Worker has crashed");
      });

      it("terminates the worker on crash", async () => {
        const host = new PluginSandboxHost(rpcHandler, {
          workerFactory: mockFactory.factory,
        });
        const manifest = makeManifest();
        const promise = host.activate("test-plugin", manifest, "/url");
        const worker = mockFactory.workers[0];
        worker.sendToHost({ type: "activated" });
        await promise;

        worker.onerror?.(new Error("crash"));

        expect(worker.terminated).toBe(true);
        expect(worker.receivedMessages.at(-1)).toEqual({ type: "terminate" });
        expect(worker.lifecycle.slice(-2)).toEqual(["post:terminate", "terminate"]);
      });

      it("waits for a shutdown acknowledgement after a runtime crash", async () => {
        const host = new PluginSandboxHost(rpcHandler, {
          workerFactory: mockFactory.factory,
        });
        const activation = host.activate("test-plugin", makeManifest(), "/url");
        const worker = mockFactory.workers[0];
        worker.sendToHost({ type: "activated" });
        await activation;
        worker.autoAcknowledgeTermination = false;

        worker.onerror?.(new Error("crash"));

        expect(worker.lifecycle).toEqual(["post:terminate"]);
        expect(worker.terminated).toBe(false);

        worker.sendToHost({ type: "terminated" });

        expect(worker.lifecycle).toEqual(["post:terminate", "terminate"]);
        expect(worker.terminated).toBe(true);
      });

      it("restarts after a runtime crash and restores activation", async () => {
        const host = new PluginSandboxHost(rpcHandler, {
          workerFactory: mockFactory.factory,
        });
        const activation = host.activate("test-plugin", makeManifest(), "/url");
        const firstWorker = mockFactory.workers[0];
        firstWorker.sendToHost({ type: "activated" });
        await activation;

        firstWorker.onerror?.(new Error("runtime crash"));
        await Promise.resolve();

        expect(mockFactory.workers).toHaveLength(2);
        const replacement = mockFactory.workers[1];
        expect(replacement.receivedMessages[0]).toMatchObject({
          type: "init",
          pluginUrl: "/url",
        });
        replacement.sendToHost({ type: "activated" });
        await host.activate("test-plugin", makeManifest(), "/url");

        expect(host.getHealth("test-plugin").status).toBe("running");
      });

      it("ignores a queued activated message from the crashed worker", async () => {
        const host = new PluginSandboxHost(rpcHandler, {
          workerFactory: mockFactory.factory,
        });
        const activation = host.activate("test-plugin", makeManifest(), "/url");
        const firstWorker = mockFactory.workers[0];
        firstWorker.sendToHost({ type: "activated" });
        await activation;

        const staleMessageHandler = firstWorker.onmessage;
        firstWorker.onerror?.(new Error("runtime crash"));
        const recovery = host.activate("test-plugin", makeManifest(), "/url");
        staleMessageHandler?.({ data: { type: "activated" } });
        await Promise.resolve();

        expect(mockFactory.workers).toHaveLength(2);
        expect(host.getHealth("test-plugin").status).toBe("activating");

        mockFactory.workers[1].sendToHost({ type: "activated" });
        await expect(recovery).resolves.toBeUndefined();
      });

      it("counts worker factory failures against the recovery budget", async () => {
        const workers: MockWorker[] = [];
        let factoryCalls = 0;
        const workerFactory = () => {
          factoryCalls += 1;
          if (factoryCalls === 2) throw new Error("replacement factory failed");
          const worker = new MockWorker();
          workers.push(worker);
          return worker;
        };
        const host = new PluginSandboxHost(rpcHandler, { workerFactory });
        const activation = host.activate("test-plugin", makeManifest(), "/url");
        workers[0].sendToHost({ type: "activated" });
        await activation;

        workers[0].onerror?.(new Error("runtime crash"));
        const recovery = host.activate("test-plugin", makeManifest(), "/url");
        await Promise.resolve();
        await Promise.resolve();

        expect(factoryCalls).toBe(3);
        expect(workers).toHaveLength(2);
        workers[1].sendToHost({ type: "activated" });
        await expect(recovery).resolves.toBeUndefined();
      });

      it("rejects recovery after three consecutive replacement factory failures", async () => {
        const workers: MockWorker[] = [];
        let factoryCalls = 0;
        const workerFactory = () => {
          factoryCalls += 1;
          if (factoryCalls > 1) {
            throw new Error(`replacement factory failed ${factoryCalls - 1}`);
          }
          const worker = new MockWorker();
          workers.push(worker);
          return worker;
        };
        const host = new PluginSandboxHost(rpcHandler, { workerFactory });
        const activation = host.activate("test-plugin", makeManifest(), "/url");
        workers[0].sendToHost({ type: "activated" });
        await activation;

        workers[0].onerror?.(new Error("runtime crash"));
        const recovery = host.activate("test-plugin", makeManifest(), "/url");

        await expect(recovery).rejects.toThrow(
          "Worker crashed after 3 restart attempts: replacement factory failed 3",
        );
        expect(factoryCalls).toBe(4);
        expect(workers).toHaveLength(1);
        expect(host.getHealth("test-plugin").status).toBe("crashed");
      });

      it.each(["terminate", "reset"] as const)(
        "does not create a replacement when %s follows a crash immediately",
        async (action) => {
          const host = new PluginSandboxHost(rpcHandler, {
            workerFactory: mockFactory.factory,
          });
          const activation = host.activate("test-plugin", makeManifest(), "/url");
          const worker = mockFactory.workers[0];
          worker.sendToHost({ type: "activated" });
          await activation;

          worker.onerror?.(new Error("runtime crash"));
          if (action === "terminate") {
            host.terminate("test-plugin");
          } else {
            host.reset();
          }
          await Promise.resolve();
          await Promise.resolve();

          expect(mockFactory.workers).toHaveLength(1);
          expect(host.has("test-plugin")).toBe(false);
          expect(host.getHealth("test-plugin").status).toBe("terminated");
        },
      );

      it("continues recovery after a replacement reports activation-error", async () => {
        const host = new PluginSandboxHost(rpcHandler, {
          workerFactory: mockFactory.factory,
        });
        const activation = host.activate("test-plugin", makeManifest(), "/url");
        mockFactory.workers[0].sendToHost({ type: "activated" });
        await activation;

        mockFactory.workers[0].onerror?.(new Error("runtime crash"));
        const recovery = host.activate("test-plugin", makeManifest(), "/url");
        await Promise.resolve();

        const firstReplacement = mockFactory.workers[1];
        firstReplacement.sendToHost({
          type: "activation-error",
          error: "replacement failed to activate",
        });
        await Promise.resolve();

        expect(mockFactory.workers).toHaveLength(3);
        mockFactory.workers[2].sendToHost({ type: "activated" });

        await expect(recovery).resolves.toBeUndefined();
        expect(host.getHealth("test-plugin").status).toBe("running");
      });

      it("rejects recovery only after three replacement activation errors", async () => {
        const host = new PluginSandboxHost(rpcHandler, {
          workerFactory: mockFactory.factory,
        });
        const activation = host.activate("test-plugin", makeManifest(), "/url");
        mockFactory.workers[0].sendToHost({ type: "activated" });
        await activation;

        mockFactory.workers[0].onerror?.(new Error("runtime crash"));
        const recovery = host.activate("test-plugin", makeManifest(), "/url");
        const rejection = expect(recovery).rejects.toThrow(
          "Worker crashed after 3 restart attempts",
        );
        await Promise.resolve();

        for (let attempt = 1; attempt <= 3; attempt += 1) {
          expect(mockFactory.workers).toHaveLength(attempt + 1);
          mockFactory.workers[attempt].sendToHost({
            type: "activation-error",
            error: `replacement activation failed ${attempt}`,
          });
          await Promise.resolve();
        }

        await rejection;
        expect(mockFactory.workers).toHaveLength(4);
        expect(host.getHealth("test-plugin").status).toBe("crashed");
      });

      it("treats message deserialization errors as recoverable crashes", async () => {
        const host = new PluginSandboxHost(rpcHandler, {
          workerFactory: mockFactory.factory,
        });
        const activation = host.activate("test-plugin", makeManifest(), "/url");
        mockFactory.workers[0].sendToHost({ type: "activated" });
        await activation;

        mockFactory.workers[0].onmessageerror?.(new Error("bad structured clone"));
        await Promise.resolve();

        expect(mockFactory.workers).toHaveLength(2);
        expect(mockFactory.workers[0].terminated).toBe(true);
        expect(mockFactory.workers[0].lifecycle.slice(-2)).toEqual([
          "post:terminate",
          "terminate",
        ]);
      });

      it("stops automatically restarting after three recovery attempts", async () => {
        const host = new PluginSandboxHost(rpcHandler, {
          workerFactory: mockFactory.factory,
        });
        const activation = host.activate("test-plugin", makeManifest(), "/url");
        mockFactory.workers[0].sendToHost({ type: "activated" });
        await activation;

        for (let attempt = 0; attempt < 3; attempt += 1) {
          mockFactory.workers[attempt].onerror?.(new Error(`crash-${attempt}`));
          await Promise.resolve();
          expect(mockFactory.workers).toHaveLength(attempt + 2);
          expect(mockFactory.workers[attempt].lifecycle.slice(-2)).toEqual([
            "post:terminate",
            "terminate",
          ]);
        }

        mockFactory.workers[3].onerror?.(new Error("final crash"));
        await Promise.resolve();

        expect(mockFactory.workers).toHaveLength(4);
        expect(mockFactory.workers[3].lifecycle.slice(-2)).toEqual([
          "post:terminate",
          "terminate",
        ]);
        expect(host.getHealth("test-plugin").status).toBe("crashed");
      });

      it("notifies health listeners on crash", async () => {
        const host = new PluginSandboxHost(rpcHandler, {
          workerFactory: mockFactory.factory,
        });
        const manifest = makeManifest();
        const listener = vi.fn();
        host.onHealthChange(listener);

        const promise = host.activate("test-plugin", manifest, "/url");
        const worker = mockFactory.workers[0];
        worker.sendToHost({ type: "activated" });
        await promise;

        // Listener should have been called with "running" on activation.
        expect(listener).toHaveBeenCalledWith(
          "test-plugin",
          expect.objectContaining({ status: "running" }),
        );

        // Crash the worker.
        worker.onerror?.(new Error("crash"));

        // Listener should have been called with "crashed".
        expect(listener).toHaveBeenCalledWith(
          "test-plugin",
          expect.objectContaining({
            status: "crashed",
            lastError: "crash",
          }),
        );
      });

      it("onHealthChange returns an unsubscribe function", async () => {
        const host = new PluginSandboxHost(rpcHandler, {
          workerFactory: mockFactory.factory,
        });
        const listener = vi.fn();
        const unsubscribe = host.onHealthChange(listener);

        expect(typeof unsubscribe).toBe("function");
        unsubscribe();

        // After unsubscribe, the listener should not be called.
        const manifest = makeManifest();
        const promise = host.activate("test-plugin", manifest, "/url");
        mockFactory.workers[0].sendToHost({ type: "activated" });
        await promise;

        expect(listener).not.toHaveBeenCalled();
      });

      it("records lastError on activation-error", async () => {
        const host = new PluginSandboxHost(rpcHandler, {
          workerFactory: mockFactory.factory,
        });
        const manifest = makeManifest();

        const promise = host.activate("test-plugin", manifest, "/url");
        mockFactory.workers[0].sendToHost({
          type: "activation-error",
          error: "Plugin failed to load: missing export",
        });

        await expect(promise).rejects.toThrow("Plugin failed to load");

        const health = host.getHealth("test-plugin");
        expect(health.status).toBe("crashed");
        expect(health.lastError).toContain("missing export");
      });

      it("notifies health listeners on terminate", async () => {
        const host = new PluginSandboxHost(rpcHandler, {
          workerFactory: mockFactory.factory,
        });
        const manifest = makeManifest();
        const listener = vi.fn();
        host.onHealthChange(listener);

        const promise = host.activate("test-plugin", manifest, "/url");
        mockFactory.workers[0].sendToHost({ type: "activated" });
        await promise;
        listener.mockClear();

        host.terminate("test-plugin");

        expect(listener).toHaveBeenCalledWith(
          "test-plugin",
          expect.objectContaining({ status: "terminated" }),
        );
      });
    });
  });
});
