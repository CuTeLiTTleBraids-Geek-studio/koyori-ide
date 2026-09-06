// @ts-expect-error -- frontend tsconfig omits Node types; Vitest runs on Node.
import { spawnSync } from "node:child_process";
// @ts-expect-error -- frontend tsconfig omits Node types; Vitest runs on Node.
import { createHash, webcrypto } from "node:crypto";
// @ts-expect-error -- frontend tsconfig omits Node types; Vitest runs on Node.
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
// @ts-expect-error -- frontend tsconfig omits Node types; Vitest runs on Node.
import { tmpdir } from "node:os";
// @ts-expect-error -- frontend tsconfig omits Node types; Vitest runs on Node.
import { isAbsolute, join as joinPath, relative as relativePath, resolve as resolvePath } from "node:path";
// @ts-expect-error -- frontend tsconfig omits Node types; Vitest runs on Node.
import { inflateRawSync } from "node:zlib";
// @ts-expect-error -- frontend tsconfig omits Node types; Vitest runs on Node.
import { Worker as NodeWorker } from "node:worker_threads";
import {
  afterEach,
  beforeEach,
  describe,
  expect,
  it,
  vi,
} from "vitest";
import type { VSCodeExtensionManifest } from "@/types";
import type { ExtensionSecurityInfo } from "@/stores/extensionSecurity";
import type { Selection } from "@/lib/extensionHost/vscodeApi";
import { showExtensionInputBox } from "@/lib/extensionHostUiBridge";

const mocks = vi.hoisted(() => ({
  getManifests: vi.fn(),
  getManifest: vi.fn(),
  triggerLanguage: vi.fn(),
  triggerCommand: vi.fn(),
  triggerEager: vi.fn(),
  reportActivation: vi.fn(),
  reportDeactivated: vi.fn(),
  readExtensionFile: vi.fn(),
  syncGrammars: vi.fn(),
  syncSnippets: vi.fn().mockResolvedValue(undefined),
  loadSecurityInfos: vi.fn().mockResolvedValue(undefined),
  getSecurityInfo: vi.fn(),
  refreshSecurityInfo: vi.fn().mockResolvedValue(undefined),
  removeSecurityInfo: vi.fn(),
  createObjectURL: vi.fn((_blob: Blob) => "blob:extension-worker"),
  defineTheme: vi.fn(),
  setTheme: vi.fn(),
  revokeObjectURL: vi.fn(),
  callById: vi.fn<(...args: unknown[]) => Promise<unknown>>(),
  secretValues: new Map<string, string>(),
  translate: vi.fn(
    (_key: string, params?: Record<string, string | number>) =>
      String(params?.operation ?? "confirm"),
  ),
  startTerminal: vi.fn<
    (id: string, cwd: string, shell: string) => Promise<void>
  >(),
  writeTerminal: vi.fn<(id: string, input: string) => Promise<void>>(),
  killTerminal: vi.fn<(id: string) => Promise<void>>(),
  readFile: vi.fn<(path: string) => Promise<string>>(),
  writeFile: vi.fn<(path: string, content: string) => Promise<void>>(),
  launchDebug: vi.fn<(config: Record<string, unknown>) => Promise<void>>(),
}));

function callExtensionBinding(...args: unknown[]): Promise<unknown> {
  const handler = (
    globalThis as typeof globalThis & {
      __koyoriIdeExtensionBindingCall?: (...values: unknown[]) => Promise<unknown>;
    }
  ).__koyoriIdeExtensionBindingCall;
  return handler
    ? handler(...args)
    : Promise.reject(new Error("Extension binding test handler unavailable"));
}

vi.mock("@wailsio/runtime", () => ({
  Call: {
    ByID: (...args: unknown[]) => mocks.callById(...args),
    ByName: vi.fn(),
  },
  Create: {
    Any: (value: unknown) => value,
    Array: () => (value: unknown) => value,
    Map: () => (value: unknown) => value,
    Nullable: () => (value: unknown) => value,
    Struct: () => (value: unknown) => value,
  },
}));

vi.mock("../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/taskservice.js", () => ({
  Execute: (...args: unknown[]) => callExtensionBinding(2_308_805_722, ...args),
  ExecuteApproved: (...args: unknown[]) => callExtensionBinding(93_571_653, ...args),
  LoadTasks: (...args: unknown[]) => callExtensionBinding(3_032_347_519, ...args),
  RequestExecutionApproval: (...args: unknown[]) =>
    callExtensionBinding(1_113_621_941, ...args),
  Shutdown: (...args: unknown[]) => callExtensionBinding(2_220_420_061, ...args),
  Stop: (...args: unknown[]) => callExtensionBinding(1_861_972_639, ...args),
}));

vi.mock("@/lib/i18n", () => ({
  translate: mocks.translate,
}));

vi.mock("monaco-editor", () => {
  const register = () => ({ dispose: () => undefined });
  return {
    editor: {
      defineTheme: mocks.defineTheme,
      setTheme: mocks.setTheme,
    },
    languages: new Proxy({}, {
      get: (_target, property) =>
        typeof property === "string" && property.startsWith("register")
          ? register
          : undefined,
    }),
  };
});

vi.mock("@/api/services", () => ({
  marketplaceService: {
    getInstalledExtensionManifests: mocks.getManifests,
    getExtensionManifest: mocks.getManifest,
    triggerActivationOnLanguage: mocks.triggerLanguage,
    triggerActivationOnCommand: mocks.triggerCommand,
    triggerActivationEager: mocks.triggerEager,
    reportExtensionActivation: mocks.reportActivation,
    reportExtensionDeactivated: mocks.reportDeactivated,
    readExtensionFile: mocks.readExtensionFile,
  },
  terminalService: {
    startSession: mocks.startTerminal,
    writeSession: mocks.writeTerminal,
    killSession: mocks.killTerminal,
  },
  fileService: {
    readFile: mocks.readFile,
    writeFile: mocks.writeFile,
  },
  debugService: {
    launchWithConfig: mocks.launchDebug,
  },
}));

vi.mock("@/stores/app", () => ({
  appState: {
    currentProject: "C:/workspace",
    language: "en",
  },
}));

vi.mock("@/lib/monacoExtensionContributes", () => ({
  syncExtensionGrammarsToMonaco: mocks.syncGrammars,
  syncExtensionSnippetsToMonaco: mocks.syncSnippets,
}));

vi.mock("@/stores/extensionSecurity", () => ({
  loadExtensionSecurityInfos: mocks.loadSecurityInfos,
  getExtensionSecurityInfo: mocks.getSecurityInfo,
  refreshExtensionSecurityInfo: mocks.refreshSecurityInfo,
  removeExtensionSecurityInfo: mocks.removeSecurityInfo,
}));

import {
  activateEager,
  activateOnLanguage,
  deactivateExtension,
  getActivatedExtensions,
  getExtensionActivationError,
  getManifestCache,
  setExtensionHostActiveEditorCallback,
  setExtensionHostDecorationCallback,
  setExtensionHostInputCallback,
  setExtensionHostOutputCallback,
  setExtensionHostStatusBarCallback,
  setExtensionHostWorkspaceFoldersCallback,
  isExtensionActivated,
  invalidateExtensionCaches,
  loadInstalledExtensionManifests,
  refreshExtensionCaches,
  resetActivationState,
  restoreExtensionContributions,
} from "./vscodeExtensionActivation";
import {
  executeVscodeExtensionCommand,
  getActiveVscodeExtensionTheme,
  listVscodeExtensionCommands,
  listVscodeExtensionGrammars,
  listVscodeExtensionSnippets,
  listVscodeExtensionThemes,
  listVscodeExtensionViews,
} from "./vscodeExtensions";
import { applyVscodeExtensionTheme } from "./monaco-themes";

type WorkerBehavior =
  | "success"
  | "activation-error-after-register"
  | "incompatible-protocol"
  | "message-before-negotiation"
  | "no-health-response"
  | "secrets"
  | "secret-permission-denied"
  | "executable-resources"
  | "active-task"
  | "task-completion"
  | "hung-registration"
  | "no-deactivation-ack"
  | "scm-lifecycle"
  | "full-api-surface";

class MockExtensionWorker {
  static instances: MockExtensionWorker[] = [];
  static behavior: WorkerBehavior = "success";
  static crashOnInitCount = 0;

  onmessage: ((event: MessageEvent<unknown>) => void) | null = null;
  onerror: ((event: ErrorEvent) => void) | null = null;
  onmessageerror: ((event: MessageEvent<unknown>) => void) | null = null;
  readonly messages: Record<string, unknown>[] = [];
  readonly rpcResults: Record<string, unknown>[] = [];
  readonly lifecycle: string[] = [];
  terminated = false;
  deactivationRequested = false;
  private token = "";
  private registrationRequestId: number | undefined;
  private terminalHandle: number | undefined;
  private webviewHandle: number | undefined;
  private sourceControlHandle: number | undefined;
  private sourceControlGroupHandle: number | undefined;

  constructor(
    readonly url: string,
    readonly options?: WorkerOptions,
  ) {
    MockExtensionWorker.instances.push(this);
  }

  postMessage(message: unknown): void {
    if (!isRecord(message)) return;
    this.messages.push(message);
    if (message.type === "terminate") this.lifecycle.push("post:terminate");
    if (message.type === "init" && typeof message.token === "string") {
      this.token = message.token;
      queueMicrotask(() => {
        if (MockExtensionWorker.crashOnInitCount > 0) {
          MockExtensionWorker.crashOnInitCount -= 1;
          this.crash("restart activation crash");
          return;
        }
        if (MockExtensionWorker.behavior === "incompatible-protocol") {
          this.emit({
            type: "protocol-error",
            token: this.token,
            error: "No compatible Extension Worker protocol version",
            supportedProtocolVersions: ["2.0"],
          });
          return;
        }
        if (MockExtensionWorker.behavior === "message-before-negotiation") {
          this.emitRPC(1, "commands.registerCommand", ["acme.demo.hello", 7]);
          return;
        }
        const offeredProtocolVersions = Array.isArray(message.protocolVersions)
          ? message.protocolVersions
          : [];
        if (!offeredProtocolVersions.includes("1.0")) {
          this.emit({
            type: "protocol-error",
            token: this.token,
            error: "No compatible Extension Worker protocol version",
            supportedProtocolVersions: ["1.0"],
          });
          return;
        }
        this.emit({
          type: "protocol-ready",
          token: this.token,
          protocolVersion: "1.0",
        });
        if (MockExtensionWorker.behavior === "secrets") {
          this.emitRPC(1, "secrets.store", ["token", "worker-secret"]);
          return;
        }
        if (MockExtensionWorker.behavior === "secret-permission-denied") {
          this.emitRPC(1, "secrets.get", ["token"]);
          return;
        }
        if (
          MockExtensionWorker.behavior === "executable-resources" ||
          MockExtensionWorker.behavior === "active-task"
          || MockExtensionWorker.behavior === "task-completion"
        ) {
          this.emitRPC(1, "tasks.executeTask", [
            {
              name: "Worker task",
              source: "acme.demo",
              definition: { type: "shell" },
              execution: {
                command: "npm",
                args: ["test"],
                cwd: "C:/workspace",
              },
            },
          ]);
          return;
        }
        if (MockExtensionWorker.behavior === "full-api-surface") {
          this.emitRPC(1, "languages.registerProvider", [
            "hover",
            { language: "typescript" },
            undefined,
            { provideHover: 91 },
          ]);
          return;
        }
        if (MockExtensionWorker.behavior === "scm-lifecycle") {
          this.emitRPC(1, "scm.createSourceControl", [
            "acme-scm",
            "Acme SCM",
            { scheme: "file", fsPath: "C:/workspace" },
          ]);
          return;
        }
        this.registrationRequestId = 1;
        this.emitRPC(1, "commands.registerCommand", ["acme.demo.hello", 7]);
      });
      return;
    }
    if (message.type === "health-check" && typeof message.id === "number") {
      if (MockExtensionWorker.behavior === "no-health-response") return;
      queueMicrotask(() => {
        this.emit({
          type: "health-response",
          token: this.token,
          id: message.id,
        });
      });
      return;
    }
    if (message.type === "rpc-result" && typeof message.id === "number") {
      this.rpcResults.push(message);
      if (MockExtensionWorker.behavior === "secrets") {
        this.handleSecretsResult(message);
        return;
      }
      if (MockExtensionWorker.behavior === "secret-permission-denied") {
        queueMicrotask(() => {
          this.emit({
            type: "activation-error",
            token: this.token,
            error:
              typeof message.error === "string"
                ? message.error
                : "secret permission gate unexpectedly allowed access",
          });
        });
        return;
      }
      if (MockExtensionWorker.behavior === "executable-resources") {
        this.handleExecutableResourceResult(message);
        return;
      }
      if (MockExtensionWorker.behavior === "active-task") {
        queueMicrotask(() => {
          if (typeof message.error === "string") {
            this.emit({
              type: "activation-error",
              token: this.token,
              error: message.error,
            });
          } else {
            this.emit({ type: "activated", token: this.token });
          }
        });
        return;
      }
      if (MockExtensionWorker.behavior === "task-completion") {
        if (message.id === 1) {
          queueMicrotask(() => {
            this.emit({ type: "activated", token: this.token });
          });
        }
        return;
      }
      if (MockExtensionWorker.behavior === "full-api-surface") {
        this.handleFullApiSurfaceResult(message);
        return;
      }
      if (MockExtensionWorker.behavior === "scm-lifecycle") {
        this.handleScmLifecycleResult(message);
        return;
      }
      if (MockExtensionWorker.behavior === "hung-registration") return;
      if (message.id !== this.registrationRequestId) return;
      queueMicrotask(() => {
        if (MockExtensionWorker.behavior === "activation-error-after-register") {
          this.emit({
            type: "activation-error",
            token: this.token,
            error: "worker activate failed",
          });
        } else {
          this.emit({ type: "activated", token: this.token });
        }
      });
      return;
    }
    if (
      message.type === "invoke-callback" &&
      typeof message.id === "number"
    ) {
      const args = Array.isArray(message.args) ? message.args : [];
      queueMicrotask(() => {
        this.emit({
          type: "callback-result",
          token: this.token,
          id: message.id,
          result: "worker:" + args.join("|"),
        });
      });
      return;
    }
    if (
      message.type === "task-completed"
      && typeof message.handleId === "number"
      && MockExtensionWorker.behavior === "task-completion"
    ) {
      this.emitRPC(2, "tasks.terminate", [message.handleId]);
      return;
    }
    if (message.type === "deactivate" && typeof message.id === "number") {
      this.deactivationRequested = true;
      if (MockExtensionWorker.behavior === "no-deactivation-ack") return;
      queueMicrotask(() => {
        this.emit({
          type: "deactivated",
          token: this.token,
          id: message.id,
        });
      });
    }
  }

  terminate(): void {
    this.lifecycle.push("terminate");
    this.terminated = true;
  }

  crash(message = "worker crashed"): void {
    this.onerror?.({ message } as ErrorEvent);
  }

  messageError(): void {
    this.onmessageerror?.({ data: undefined } as MessageEvent<unknown>);
  }

  emitForged(message: Record<string, unknown>): void {
    this.onmessage?.({
      data: { ...message, token: "forged-worker-token" },
    } as MessageEvent<unknown>);
  }

  emitMessageFlood(count = 1_001): void {
    for (let index = 0; index < count && !this.terminated; index += 1) {
      this.emit({
        type: "health-response",
        token: this.token,
        id: -1,
      });
    }
  }

  emitOversizedMessage(): void {
    this.emit({
      type: "health-response",
      token: this.token,
      id: -1,
      payload: "x".repeat(2_100_000),
    });
  }

  private emit(message: Record<string, unknown>): void {
    if (!this.terminated) {
      this.onmessage?.({ data: message } as MessageEvent<unknown>);
    }
  }

  private emitRPC(id: number, method: string, args: unknown[]): void {
    this.emit({
      type: "rpc",
      token: this.token,
      id,
      method,
      args,
    });
  }

  private handleSecretsResult(message: Record<string, unknown>): void {
    queueMicrotask(() => {
      if (message.error !== undefined) {
        this.emit({
          type: "activation-error",
          token: this.token,
          error: String(message.error),
        });
        return;
      }
      if (message.id === 1) {
        this.emitRPC(2, "secrets.get", ["token"]);
      } else if (message.id === 2) {
        this.emitRPC(3, "secrets.delete", ["token"]);
      } else if (message.id === 3) {
        this.emitRPC(4, "secrets.get", ["token"]);
      } else if (message.id === 4) {
        this.emit({ type: "activated", token: this.token });
      }
    });
  }

  private handleExecutableResourceResult(
    message: Record<string, unknown>,
  ): void {
    queueMicrotask(() => {
      if (message.error !== undefined) {
        this.emit({
          type: "activation-error",
          token: this.token,
          error: String(message.error),
        });
        return;
      }
      if (message.id === 1) {
        this.emitRPC(2, "tasks.terminate", [message.result]);
      } else if (message.id === 2) {
        this.emitRPC(3, "window.createTerminal", [
          { name: "Worker terminal", cwd: "C:/workspace" },
        ]);
      } else if (message.id === 3) {
        if (typeof message.result !== "number") {
          this.emit({
            type: "activation-error",
            token: this.token,
            error: "invalid terminal handle",
          });
          return;
        }
        this.terminalHandle = message.result;
        setTimeout(() => {
          this.emitRPC(4, "window.terminal.sendText", [
            this.terminalHandle,
            "npm test",
            true,
          ]);
        }, 10);
      } else if (message.id === 4) {
        setTimeout(() => {
          this.emitRPC(5, "window.terminal.show", [
            this.terminalHandle,
            false,
          ]);
        }, 10);
      } else if (message.id === 5) {
        this.emitRPC(6, "window.terminal.hide", [this.terminalHandle]);
      } else if (message.id === 6) {
        this.emitRPC(7, "window.terminal.dispose", [this.terminalHandle]);
      } else if (message.id === 7) {
        this.emit({ type: "activated", token: this.token });
      }
    });
  }

  private handleFullApiSurfaceResult(message: Record<string, unknown>): void {
    queueMicrotask(() => {
      if (message.error !== undefined) {
        this.emit({
          type: "activation-error",
          token: this.token,
          error: String(message.error),
        });
        return;
      }
      if (message.id === 1) {
        this.emitRPC(2, "workspace.fs.readFile", [{
          scheme: "file",
          fsPath: "C:/workspace/readme.md",
        }]);
      } else if (message.id === 2) {
        this.emitRPC(3, "workspace.fs.writeFile", [
          { scheme: "file", fsPath: "C:/workspace/readme.md" },
          new TextEncoder().encode("updated"),
        ]);
      } else if (message.id === 3) {
        this.emitRPC(4, "window.createWebviewPanel", [
          "acme.preview",
          "Preview",
          {},
          { enableScripts: true },
        ]);
      } else if (message.id === 4) {
        if (typeof message.result !== "number") {
          this.emit({
            type: "activation-error",
            token: this.token,
            error: "invalid webview handle",
          });
          return;
        }
        this.webviewHandle = message.result;
        this.emitRPC(5, "window.webview.setHtml", [
          this.webviewHandle,
          "<script>globalThis.ready = true</script><p>worker webview</p>",
        ]);
      } else if (message.id === 5) {
        this.emitRPC(6, "debug.startDebugging", [
          undefined,
          { type: "node", name: "Worker Debug", program: "index.js" },
        ]);
      } else if (message.id === 6) {
        this.emitRPC(7, "scm.createSourceControl", [
          "acme-scm",
          "Acme SCM",
          { scheme: "file", fsPath: "C:/workspace" },
        ]);
      } else if (message.id === 7) {
        this.emitRPC(8, "env.clipboard.writeText", ["worker clipboard"]);
      } else if (message.id === 8) {
        this.emitRPC(9, "env.clipboard.readText", []);
      } else if (message.id === 9) {
        this.emitRPC(10, "env.openExternal", [{
          scheme: "https",
          fsPath: "https://example.test/docs",
        }]);
      } else if (message.id === 10) {
        this.emit({ type: "activated", token: this.token });
      }
    });
  }

  private handleScmLifecycleResult(message: Record<string, unknown>): void {
    queueMicrotask(() => {
      if (message.id === 1 && typeof message.result === "number") {
        this.sourceControlHandle = message.result;
        this.emitRPC(2, "scm.createResourceGroup", [
          this.sourceControlHandle,
          "changes",
          "Changes",
        ]);
      } else if (message.id === 2 && typeof message.result === "number") {
        this.sourceControlGroupHandle = message.result;
        this.emitRPC(3, "scm.dispose", [this.sourceControlHandle]);
      } else if (message.id === 3 && message.error === undefined) {
        this.emitRPC(4, "scm.setResourceStates", [
          this.sourceControlGroupHandle,
          [],
        ]);
      } else if (
        message.id === 4
        && typeof message.error === "string"
        && message.error.includes("Unknown source control group handle")
      ) {
        this.emit({ type: "activated", token: this.token });
      } else {
        this.emit({
          type: "activation-error",
          token: this.token,
          error: typeof message.error === "string"
            ? message.error
            : "SCM lifecycle cleanup failed",
        });
      }
    });
  }

  static reset(): void {
    MockExtensionWorker.instances = [];
    MockExtensionWorker.behavior = "success";
    MockExtensionWorker.crashOnInitCount = 0;
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function encode(value: string): Uint8Array {
  return new TextEncoder().encode(value);
}

function readZipEntry(archive: Uint8Array, target: string): Uint8Array {
  const view = new DataView(archive.buffer, archive.byteOffset, archive.byteLength);
  let endOfCentralDirectory = -1;
  for (let offset = archive.byteLength - 22; offset >= Math.max(0, archive.byteLength - 65_557); offset -= 1) {
    if (view.getUint32(offset, true) === 0x06054b50) {
      endOfCentralDirectory = offset;
      break;
    }
  }
  if (endOfCentralDirectory < 0) throw new Error("VSIX archive has no ZIP central directory");
  const entryCount = view.getUint16(endOfCentralDirectory + 10, true);
  let cursor = view.getUint32(endOfCentralDirectory + 16, true);
  for (let index = 0; index < entryCount; index += 1) {
    if (view.getUint32(cursor, true) !== 0x02014b50) throw new Error("Invalid VSIX central directory entry");
    const compression = view.getUint16(cursor + 10, true);
    const compressedSize = view.getUint32(cursor + 20, true);
    const nameLength = view.getUint16(cursor + 28, true);
    const extraLength = view.getUint16(cursor + 30, true);
    const commentLength = view.getUint16(cursor + 32, true);
    const localOffset = view.getUint32(cursor + 42, true);
    const entryName = new TextDecoder().decode(archive.subarray(cursor + 46, cursor + 46 + nameLength));
    if (entryName === target) {
      if (view.getUint32(localOffset, true) !== 0x04034b50) throw new Error("Invalid VSIX local file header");
      const localNameLength = view.getUint16(localOffset + 26, true);
      const localExtraLength = view.getUint16(localOffset + 28, true);
      const dataStart = localOffset + 30 + localNameLength + localExtraLength;
      const compressed = archive.subarray(dataStart, dataStart + compressedSize);
      if (compression === 0) return new Uint8Array(compressed);
      if (compression === 8) return new Uint8Array(inflateRawSync(compressed));
      throw new Error("Unsupported VSIX ZIP compression method: " + compression);
    }
    cursor += 46 + nameLength + extraLength + commentLength;
  }
  throw new Error("VSIX entry not found: " + target);
}

let realCorpusWorkerBlob: Blob | undefined;

class RealCorpusWorker {
  static instances: RealCorpusWorker[] = [];

  onmessage: ((event: MessageEvent<unknown>) => void) | null = null;
  onerror: ((event: ErrorEvent) => void) | null = null;
  onmessageerror: ((event: MessageEvent<unknown>) => void) | null = null;
  private inner: InstanceType<typeof NodeWorker> | undefined;
  private queuedMessages: unknown[] = [];
  private token = "";
  terminated = false;

  constructor(_url: string, _options?: WorkerOptions) {
    const blob = realCorpusWorkerBlob;
    if (!blob) throw new Error("real VSIX Worker blob was not captured");
    RealCorpusWorker.instances.push(this);
    void blob.text().then((source) => {
      if (this.terminated) return;
      const workerSource = [
        'const { parentPort } = require("node:worker_threads");',
        'const vm = require("node:vm");',
        'const { webcrypto } = require("node:crypto");',
        "const context = vm.createContext({ console, crypto: webcrypto, TextEncoder, TextDecoder, setTimeout, clearTimeout, queueMicrotask });",
        "context.postMessage = (message) => parentPort.postMessage(message);",
        "context.addEventListener = (type, listener) => { if (type === 'message') parentPort.on('message', (data) => listener({ data })); };",
        "context.close = () => process.exit(0);",
        "context.self = context;",
        "vm.runInContext(" + JSON.stringify("eval(" + JSON.stringify(source) + ");") + ", context);",
      ].join("\n");
      this.inner = new NodeWorker(workerSource, { eval: true });
      this.inner.on("message", (data: unknown) => this.onmessage?.({ data } as MessageEvent<unknown>));
      this.inner.on("error", (error: Error) => this.onerror?.({ message: error.stack ?? error.message } as ErrorEvent));
      this.inner.on("messageerror", (error: unknown) => this.onmessageerror?.({ data: error } as MessageEvent<unknown>));
      const queued = this.queuedMessages;
      this.queuedMessages = [];
      for (const message of queued) this.inner.postMessage(message);
    }).catch((error: unknown) => {
      this.onerror?.({ message: error instanceof Error ? error.message : String(error) } as ErrorEvent);
    });
  }

  postMessage(message: unknown): void {
    if (isRecord(message) && message.type === "init" && typeof message.token === "string") {
      this.token = message.token;
    }
    if (this.terminated) return;
    if (this.inner) this.inner.postMessage(message);
    else this.queuedMessages.push(message);
  }

  crash(message = "real corpus Worker crash"): void {
    this.onerror?.({ message } as ErrorEvent);
  }

  emitMessageFlood(count = 1_100): void {
    for (let index = 0; index < count; index += 1) {
      this.onmessage?.({
        data: { type: "health-response", token: this.token, id: -index - 1 },
      } as MessageEvent<unknown>);
    }
  }

  terminate(): void {
    this.terminated = true;
    this.queuedMessages = [];
    void this.inner?.terminate();
  }

  static reset(): void {
    RealCorpusWorker.instances = [];
  }
}

interface RealCorpusPackage {
  file: string;
  sha256: string;
  publisher: string;
  name: string;
  manifest: VSCodeExtensionManifest;
  packageJSON: Record<string, unknown>;
  source: string;
}

// P19 CI repair: the fixed-SHA third-party corpus lives in the untracked
// build/e2e-evidence/p9-g20/ evidence directory (local run evidence by
// design — the same tree check-personal-paths.mjs deliberately ignores).
// Fresh checkouts such as CI runners do not carry it; the four real-corpus
// integration tests below then skip instead of failing on ENOENT. They still
// run wherever the evidence tree exists (developer machines), preserving the
// recorded real-Worker coverage.
const testProcessGlobal = globalThis as typeof globalThis & { process?: { cwd(): string } };
const realCorpusDir = resolvePath(testProcessGlobal.process?.cwd() ?? ".", "..", "build", "e2e-evidence", "p9-g20", "corpus");
const realCorpusAvailable = existsSync(realCorpusDir);

function readRealCorpusPackage(
  file: string,
  sha256: string,
  publisher: string,
  name: string,
  entry: string,
): RealCorpusPackage {
  const testProcess = globalThis as typeof globalThis & { process?: { cwd(): string } };
  const archive = readFileSync(resolvePath(testProcess.process?.cwd?.() ?? ".", "..", "build", "e2e-evidence", "p9-g20", "corpus", file));
  const actualSha256 = createHash("sha256").update(archive).digest("hex");
  if (actualSha256 !== sha256) throw new Error(file + " SHA-256 mismatch: " + actualSha256);
  const packageJSON = JSON.parse(new TextDecoder().decode(readZipEntry(archive, "extension/package.json"))) as Record<string, unknown>;
  const contributes = packageJSON.contributes ?? {};
  const manifest = {
    name,
    publisher,
    version: String(packageJSON.version ?? ""),
    displayName: String(packageJSON.displayName ?? name),
    description: String(packageJSON.description ?? ""),
    engines: (packageJSON.engines ?? {}) as Record<string, string>,
    activationEvents: Array.isArray(packageJSON.activationEvents) ? packageJSON.activationEvents as string[] : [],
    contributes,
    capabilities: packageJSON.capabilities ?? {},
    main: typeof packageJSON.main === "string" ? packageJSON.main : undefined,
    browser: typeof packageJSON.browser === "string" ? packageJSON.browser : undefined,
    parsedContributes: contributes as VSCodeExtensionManifest["parsedContributes"],
  } as VSCodeExtensionManifest;
  const sourcePath = "extension/" + entry.replace(/^\.\//, "");
  const source = new TextDecoder().decode(readZipEntry(archive, sourcePath));
  return { file, sha256, publisher, name, manifest, packageJSON, source };
}
type InstalledCorpusSpec = {
  file: string;
  sha256: string;
  publisher: string;
  name: string;
  version: string;
  entry: string;
};

function installRealCorpusPackages(specs: InstalledCorpusSpec[]): string {
  const configRoot = mkdtempSync(joinPath(tmpdir(), "koyori-vsix-install-"));
  const specPath = joinPath(configRoot, "spec.json");
  const processGlobal = globalThis as typeof globalThis & { process?: { cwd(): string } };
  const repoRoot = resolvePath(processGlobal.process?.cwd?.() ?? ".", "..");
  const archiveSpecs = specs.map((spec) => ({
    ...spec,
    file: resolvePath(repoRoot, "build", "e2e-evidence", "p9-g20", "corpus", spec.file),
  }));
  writeFileSync(specPath, JSON.stringify(archiveSpecs), "utf8");
  const result = spawnSync(
    "go",
    ["run", "./internal/vsixinstall", "-config", configRoot, "-spec", specPath],
    { cwd: repoRoot, encoding: "utf8", timeout: 300_000 },
  );
  if (result.error || result.status !== 0) {
    throw new Error(
      "production VSIX installer failed: " +
        (result.error?.message ?? String(result.status) + ": " + String(result.stderr ?? "")),
    );
  }
  return configRoot;
}

function readInstalledCorpusPackage(
  configRoot: string,
  spec: InstalledCorpusSpec,
): RealCorpusPackage {
  const extensionRoot = joinPath(
    configRoot,
    "koyori-ide",
    "extensions",
    spec.publisher + "." + spec.name,
    "extension",
  );
  const packageJSON = JSON.parse(
    readFileSync(joinPath(extensionRoot, "package.json"), "utf8"),
  ) as Record<string, unknown>;
  const contributes = packageJSON.contributes ?? {};
  const manifest = {
    name: spec.name,
    publisher: spec.publisher,
    version: String(packageJSON.version ?? spec.version),
    displayName: String(packageJSON.displayName ?? spec.name),
    description: String(packageJSON.description ?? ""),
    engines: (packageJSON.engines ?? {}) as Record<string, string>,
    activationEvents: Array.isArray(packageJSON.activationEvents)
      ? packageJSON.activationEvents as string[]
      : [],
    contributes,
    capabilities: packageJSON.capabilities ?? {},
    main: typeof packageJSON.main === "string" ? packageJSON.main : undefined,
    browser: typeof packageJSON.browser === "string" ? packageJSON.browser : undefined,
    parsedContributes: contributes as VSCodeExtensionManifest["parsedContributes"],
  } as VSCodeExtensionManifest;
  const source = readFileSync(joinPath(extensionRoot, spec.entry), "utf8");
  return {
    file: spec.file,
    sha256: spec.sha256,
    publisher: spec.publisher,
    name: spec.name,
    manifest,
    packageJSON,
    source,
  };
}

function deferred<T>(): {
  promise: Promise<T>;
  resolve(value: T): void;
} {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

function securityInfo(
  overrides: Partial<ExtensionSecurityInfo> = {},
): ExtensionSecurityInfo {
  return {
    extensionId: "acme.demo",
    level: "trusted",
    permissions: [],
    sha256: "integrity-checked-sha",
    integrityChecked: true,
    verified: true,
    enabled: true,
    blacklisted: false,
    pendingReview: false,
    ...overrides,
  };
}

const contributionOnlyManifest = {
  publisher: "acme",
  name: "demo",
  parsedContributes: {},
} as VSCodeExtensionManifest;

const executableManifest = {
  publisher: "acme",
  name: "demo",
  parsedContributes: {
    commands: [{ command: "acme.demo.hello", title: "Hello" }],
  },
} as VSCodeExtensionManifest;

const otherExecutableManifest = {
  publisher: "acme",
  name: "other",
  parsedContributes: {
    commands: [{ command: "acme.other.hello", title: "Other Hello" }],
  },
} as VSCodeExtensionManifest;

const snippetManifest = {
  publisher: "acme",
  name: "snippets",
  parsedContributes: {
    grammars: [{ language: "typescript", scopeName: "source.acme" }],
    snippets: [{ language: "typescript", path: "snippets.json" }],
  },
} as VSCodeExtensionManifest;

const staticContributesManifest = {
  publisher: "acme",
  name: "toolbox",
  parsedContributes: {
    views: {
      explorer: [{ id: "acme.toolbox.view", name: "Toolbox" }],
    },
    grammars: [{
      language: "typescript",
      scopeName: "source.acme.toolbox",
      path: "syntaxes/toolbox.tmLanguage.json",
    }],
    snippets: [{
      language: "typescript",
      path: "snippets/toolbox.json",
    }],
  },
} as unknown as VSCodeExtensionManifest;

const sharedContributesManifests = ["one", "two"].map((name) => ({
  publisher: "acme",
  name,
  parsedContributes: {
    views: {
      explorer: [{ id: "shared.view", name: `Shared ${name}` }],
    },
    grammars: [{
      language: "typescript",
      scopeName: "source.shared",
      path: "syntaxes/shared.tmLanguage.json",
    }],
    snippets: [{
      language: "typescript",
      path: "snippets/shared.json",
    }],
  },
})) as unknown as VSCodeExtensionManifest[];

function useContributionOnlyPackage(): void {
  mocks.readExtensionFile.mockImplementation(
    async (_publisher: string, _name: string, path: string) => {
      if (path === "extension/package.json") return encode("{}");
      throw new Error("unexpected extension path: " + path);
    },
  );
}

function useExecutablePackage(options?: {
  browserSource?: string;
  mainSource?: string;
}): void {
  const browserSource =
    options?.browserSource ??
    'module.exports = { activate() {}, deactivate() {} };';
  const mainSource =
    options?.mainSource ??
    'module.exports = { activate() {}, deactivate() {} };';
  mocks.readExtensionFile.mockImplementation(
    async (_publisher: string, _name: string, path: string) => {
      if (path === "extension/package.json") {
        return encode(
          JSON.stringify({
            browser: "./dist/browser.js",
            main: "./dist/main.js",
          }),
        );
      }
      if (path === "extension/dist/browser.js") return encode(browserSource);
      if (path === "extension/dist/main.js") return encode(mainSource);
      throw new Error("unexpected extension path: " + path);
    },
  );
}

describe("vscode extension activation", () => {
  beforeEach(async () => {
    await resetActivationState();
    vi.clearAllMocks();
    (
      globalThis as typeof globalThis & {
        __koyoriIdeExtensionBindingCall?: (...values: unknown[]) => Promise<unknown>;
      }
    ).__koyoriIdeExtensionBindingCall = (...args: unknown[]) => mocks.callById(...args);
    vi.stubGlobal("crypto", webcrypto as unknown as Crypto);
    vi.stubGlobal(
      "Worker",
      MockExtensionWorker as unknown as typeof Worker,
    );
    Object.defineProperty(URL, "createObjectURL", {
      configurable: true,
      value: mocks.createObjectURL,
    });
    Object.defineProperty(URL, "revokeObjectURL", {
      configurable: true,
      value: mocks.revokeObjectURL,
    });
    MockExtensionWorker.reset();
    RealCorpusWorker.reset();
    mocks.secretValues.clear();
    mocks.callById.mockReset();
    mocks.callById.mockImplementation(
      (methodId: unknown, ...args: unknown[]): Promise<unknown> => {
        if (methodId === 1189645321) {
          return Promise.resolve(
            mocks.secretValues.get(String(args[0])) ?? "",
          );
        }
        if (methodId === 1779126640) {
          mocks.secretValues.set(String(args[0]), String(args[1]));
          return Promise.resolve(undefined);
        }
        if (methodId === 1741758748) {
          mocks.secretValues.delete(String(args[0]));
          return Promise.resolve(undefined);
        }
        if (methodId === 1113621941) {
          return Promise.resolve("task-approval-token");
        }
        if (methodId === 93571653) {
          return new Promise(() => undefined);
        }
        return Promise.resolve(undefined);
      },
    );
    mocks.startTerminal.mockReset();
    mocks.startTerminal.mockResolvedValue(undefined);
    mocks.writeTerminal.mockReset();
    mocks.writeTerminal.mockResolvedValue(undefined);
    mocks.killTerminal.mockReset();
    mocks.killTerminal.mockResolvedValue(undefined);
    mocks.readFile.mockReset();
    mocks.readFile.mockResolvedValue("read from worker");
    mocks.writeFile.mockReset();
    mocks.writeFile.mockResolvedValue(undefined);
    mocks.launchDebug.mockReset();
    mocks.launchDebug.mockResolvedValue(undefined);
    mocks.translate.mockClear();
    vi.stubGlobal("confirm", vi.fn(() => true));
    mocks.getManifests.mockResolvedValue([contributionOnlyManifest]);
    mocks.getManifest.mockResolvedValue(contributionOnlyManifest);
    mocks.triggerLanguage.mockResolvedValue(["acme.demo"]);
    mocks.triggerCommand.mockResolvedValue(["acme.demo"]);
    mocks.reportActivation.mockResolvedValue(undefined);
    mocks.reportDeactivated.mockResolvedValue(undefined);
    mocks.loadSecurityInfos.mockResolvedValue(undefined);
    mocks.refreshSecurityInfo.mockResolvedValue(undefined);
    mocks.getSecurityInfo.mockImplementation((extensionId: string) =>
      securityInfo({ extensionId }),
    );
    useContributionOnlyPackage();
    vi.spyOn(console, "warn").mockImplementation(() => undefined);
  });

  afterEach(async () => {
    await resetActivationState();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it("deactivates one extension while retaining its manifest for reactivation", async () => {
    await activateOnLanguage("typescript");
    expect(getManifestCache().has("acme.demo")).toBe(true);
    expect(getActivatedExtensions()).toContain("acme.demo");
    expect(mocks.reportActivation).toHaveBeenCalledWith("acme.demo", true);

    await deactivateExtension("acme.demo");

    expect(getManifestCache().has("acme.demo")).toBe(true);
    expect(getActivatedExtensions()).not.toContain("acme.demo");
    expect(mocks.reportDeactivated).toHaveBeenCalledWith("acme.demo");

    await activateOnLanguage("typescript");

    expect(mocks.getManifests).toHaveBeenCalledTimes(1);
    expect(mocks.triggerLanguage).toHaveBeenCalledTimes(2);
    expect(getActivatedExtensions()).toContain("acme.demo");
  });

  it("reloads installed manifests only after activation state is reset", async () => {
    await loadInstalledExtensionManifests();
    await deactivateExtension("acme.demo");
    await loadInstalledExtensionManifests();

    expect(mocks.getManifests).toHaveBeenCalledTimes(1);
    expect(getManifestCache().has("acme.demo")).toBe(true);

    await resetActivationState();
    await loadInstalledExtensionManifests();

    expect(mocks.getManifests).toHaveBeenCalledTimes(2);
    expect(getManifestCache().has("acme.demo")).toBe(true);
  });

  it("invalidates only the replaced extension caches and contributions", async () => {
    mocks.getManifests.mockResolvedValue([
      executableManifest,
      otherExecutableManifest,
    ]);
    await loadInstalledExtensionManifests();

    invalidateExtensionCaches("acme.demo");

    expect(getManifestCache().has("acme.demo")).toBe(false);
    expect(getManifestCache().has("acme.other")).toBe(true);
    expect(mocks.removeSecurityInfo).toHaveBeenCalledWith("acme.demo");
    expect(
      listVscodeExtensionCommands().some(
        (command) => command.id === "acme.demo.hello",
      ),
    ).toBe(false);
    expect(
      listVscodeExtensionCommands().some(
        (command) => command.id === "acme.other.hello",
      ),
    ).toBe(true);
  });

  it("refreshes one updated manifest and security record without resetting others", async () => {
    const updatedManifest = {
      ...executableManifest,
      parsedContributes: {
        commands: [{ command: "acme.demo.updated", title: "Updated" }],
      },
    } as VSCodeExtensionManifest;
    mocks.getManifests.mockResolvedValue([
      executableManifest,
      otherExecutableManifest,
    ]);
    mocks.getManifest.mockResolvedValue(updatedManifest);
    await loadInstalledExtensionManifests();

    await refreshExtensionCaches("acme", "demo");

    expect(getManifestCache().get("acme.demo")).toBe(updatedManifest);
    expect(getManifestCache().has("acme.other")).toBe(true);
    expect(mocks.refreshSecurityInfo).toHaveBeenCalledWith("acme.demo");
    expect(
      listVscodeExtensionCommands().map((command) => command.id),
    ).toEqual(expect.arrayContaining(["acme.demo.updated", "acme.other.hello"]));
    expect(
      listVscodeExtensionCommands().some(
        (command) => command.id === "acme.demo.hello",
      ),
    ).toBe(false);
  });

  it("keeps old package state invalidated when updated manifest refresh fails", async () => {
    mocks.getManifests.mockResolvedValue([
      executableManifest,
      otherExecutableManifest,
    ]);
    mocks.getManifest.mockRejectedValueOnce(new Error("manifest unavailable"));
    await loadInstalledExtensionManifests();

    await expect(refreshExtensionCaches("acme", "demo")).rejects.toThrow(
      "manifest unavailable",
    );

    expect(getManifestCache().has("acme.demo")).toBe(false);
    expect(getManifestCache().has("acme.other")).toBe(true);
    expect(mocks.removeSecurityInfo).toHaveBeenCalledWith("acme.demo");
    expect(
      listVscodeExtensionCommands().some(
        (command) => command.extensionId === "acme.demo",
      ),
    ).toBe(false);
  });

  it("restores retained lazy commands after a failed replacement", async () => {
    mocks.getManifests.mockResolvedValue([executableManifest]);
    await loadInstalledExtensionManifests();
    await deactivateExtension("acme.demo");
    expect(
      listVscodeExtensionCommands().some(
        (command) => command.id === "acme.demo.hello",
      ),
    ).toBe(false);

    restoreExtensionContributions("acme.demo");

    expect(
      listVscodeExtensionCommands().some(
        (command) => command.id === "acme.demo.hello",
      ),
    ).toBe(true);
  });

  it("reloads manifests when reset completes while an activation trigger is in flight", async () => {
    const trigger = deferred<string[]>();
    mocks.triggerLanguage.mockReturnValueOnce(trigger.promise);

    const activation = activateOnLanguage("typescript");
    await vi.waitFor(() =>
      expect(mocks.triggerLanguage).toHaveBeenCalledTimes(1),
    );
    await resetActivationState();
    trigger.resolve(["acme.demo"]);
    await activation;

    expect(mocks.getManifests).toHaveBeenCalledTimes(2);
    expect(getActivatedExtensions()).toContain("acme.demo");
  });

  it("registers a contributed command before activation and lazily activates it on execution", async () => {
    mocks.getManifests.mockResolvedValue([executableManifest]);
    useExecutablePackage();

    await loadInstalledExtensionManifests();

    expect(
      listVscodeExtensionCommands().filter(
        (command) => command.id === "acme.demo.hello",
      ),
    ).toEqual([
      expect.objectContaining({
        extensionId: "acme.demo",
        label: "Hello",
      }),
    ]);
    expect(isExtensionActivated("acme.demo")).toBe(false);
    expect(MockExtensionWorker.instances).toHaveLength(0);
    expect(mocks.readExtensionFile).not.toHaveBeenCalled();

    await expect(
      executeVscodeExtensionCommand("acme.demo.hello", "lazy"),
    ).resolves.toBe("worker:lazy");

    expect(isExtensionActivated("acme.demo")).toBe(true);
    expect(mocks.triggerCommand).toHaveBeenCalledWith("acme.demo.hello");
    expect(mocks.reportActivation).toHaveBeenCalledWith("acme.demo", true);
    expect(MockExtensionWorker.instances).toHaveLength(1);
    expect(
      listVscodeExtensionCommands().filter(
        (command) => command.id === "acme.demo.hello",
      ),
    ).toHaveLength(1);
  });

  it("uses the shared activation path for implicit onCommand manifests", async () => {
    const implicitCommandManifest = {
      ...executableManifest,
      parsedContributes: {
        ...executableManifest.parsedContributes,
        grammars: [{ language: "typescript", scopeName: "source.acme.demo" }],
        snippets: [{ language: "typescript", path: "snippets/demo.json" }],
      },
    } as VSCodeExtensionManifest;
    mocks.getManifests.mockResolvedValue([implicitCommandManifest]);
    mocks.triggerCommand.mockResolvedValue([]);
    useExecutablePackage();

    await loadInstalledExtensionManifests();
    await expect(
      executeVscodeExtensionCommand("acme.demo.hello", "implicit"),
    ).resolves.toBe("worker:implicit");

    expect(isExtensionActivated("acme.demo")).toBe(true);
    expect(mocks.triggerCommand).toHaveBeenCalledWith("acme.demo.hello");
    expect(mocks.reportActivation).toHaveBeenCalledWith("acme.demo", true);
    expect(mocks.syncGrammars).toHaveBeenCalledTimes(1);
    expect(mocks.syncSnippets).toHaveBeenCalledTimes(1);
  });

  it("keeps repeated extension activation requests idempotent", async () => {
    mocks.getManifests.mockResolvedValue([executableManifest]);
    useExecutablePackage();

    await activateOnLanguage("typescript");
    const firstWorker = MockExtensionWorker.instances[0];
    await activateOnLanguage("typescript");

    expect(isExtensionActivated("acme.demo")).toBe(true);
    expect(MockExtensionWorker.instances).toEqual([firstWorker]);
    expect(
      listVscodeExtensionCommands().filter(
        (command) => command.id === "acme.demo.hello",
      ),
    ).toHaveLength(1);
    expect(mocks.readExtensionFile).toHaveBeenCalledTimes(2);
    expect(mocks.reportActivation).toHaveBeenNthCalledWith(
      1,
      "acme.demo",
      true,
    );
    expect(mocks.reportActivation).toHaveBeenNthCalledWith(
      2,
      "acme.demo",
      true,
    );
  });

  it("injects views, grammars, and snippets once during activation", async () => {
    mocks.getManifests.mockResolvedValue([staticContributesManifest]);
    mocks.triggerLanguage.mockResolvedValue(["acme.toolbox"]);

    await loadInstalledExtensionManifests();

    expect(listVscodeExtensionViews("explorer")).toHaveLength(0);
    expect(listVscodeExtensionGrammars("typescript")).toHaveLength(0);
    expect(listVscodeExtensionSnippets("typescript")).toHaveLength(0);
    expect(mocks.syncGrammars).not.toHaveBeenCalled();
    expect(mocks.syncSnippets).not.toHaveBeenCalled();

    await activateOnLanguage("typescript");

    expect(listVscodeExtensionViews("explorer")).toEqual([
      expect.objectContaining({ id: "acme.toolbox.view" }),
    ]);
    expect(listVscodeExtensionGrammars("typescript")).toEqual([
      expect.objectContaining({
        extensionId: "acme.toolbox",
        scopeName: "source.acme.toolbox",
      }),
    ]);
    expect(listVscodeExtensionSnippets("typescript")).toEqual([
      expect.objectContaining({
        extensionId: "acme.toolbox",
        path: "snippets/toolbox.json",
      }),
    ]);

    await activateOnLanguage("typescript");

    expect(listVscodeExtensionViews("explorer")).toHaveLength(1);
    expect(listVscodeExtensionGrammars("typescript")).toHaveLength(1);
    expect(listVscodeExtensionSnippets("typescript")).toHaveLength(1);
    expect(mocks.syncGrammars).toHaveBeenCalledTimes(1);
    expect(mocks.syncSnippets).toHaveBeenCalledTimes(1);
  });

  it("keeps same-named contributions isolated by owning extension", async () => {
    mocks.getManifests.mockResolvedValue(sharedContributesManifests);
    mocks.triggerLanguage.mockResolvedValue(["acme.one", "acme.two"]);

    await activateOnLanguage("typescript");

    expect(
      listVscodeExtensionViews("explorer").map((view) => view.extensionId),
    ).toEqual(["acme.one", "acme.two"]);
    expect(
      listVscodeExtensionGrammars("typescript").map(
        (grammar) => grammar.extensionId,
      ),
    ).toEqual(["acme.one", "acme.two"]);
    expect(
      listVscodeExtensionSnippets("typescript").map(
        (snippet) => snippet.extensionId,
      ),
    ).toEqual(["acme.one", "acme.two"]);

    await deactivateExtension("acme.one");

    expect(
      listVscodeExtensionViews("explorer").map((view) => view.extensionId),
    ).toEqual(["acme.two"]);
    expect(
      listVscodeExtensionGrammars("typescript").map(
        (grammar) => grammar.extensionId,
      ),
    ).toEqual(["acme.two"]);
    expect(
      listVscodeExtensionSnippets("typescript").map(
        (snippet) => snippet.extensionId,
      ),
    ).toEqual(["acme.two"]);
  });

  it("reconciles Monaco snippets after one extension is deactivated", async () => {
    mocks.getManifests.mockResolvedValue([snippetManifest]);
    mocks.triggerLanguage.mockResolvedValue(["acme.snippets"]);
    mocks.getSecurityInfo.mockReturnValue(
      securityInfo({ extensionId: "acme.snippets" }),
    );

    await activateOnLanguage("typescript");
    expect(mocks.syncSnippets).toHaveBeenCalledTimes(1);
    expect(mocks.syncGrammars).toHaveBeenCalledTimes(1);
    mocks.syncSnippets.mockClear();
    mocks.syncGrammars.mockClear();

    await deactivateExtension("acme.snippets");

    await vi.waitFor(() =>
      expect(mocks.syncSnippets).toHaveBeenCalledTimes(1),
    );
    expect(mocks.syncGrammars).toHaveBeenCalledTimes(1);
    expect(getManifestCache().has("acme.snippets")).toBe(true);
    expect(getActivatedExtensions()).not.toContain("acme.snippets");
  });

  it("activates executable code in a Worker and routes contributed commands through ExtensionHost", async () => {
    mocks.getManifests.mockResolvedValue([executableManifest]);
    useExecutablePackage();

    await activateOnLanguage("typescript");

    expect(getActivatedExtensions()).toContain("acme.demo");
    expect(MockExtensionWorker.instances).toHaveLength(1);
    const worker = MockExtensionWorker.instances[0];
    const init = worker.messages.find((message) => message.type === "init");
    expect(init).toMatchObject({
      extensionId: "acme.demo",
      mainPath: "extension/dist/browser.js",
    });
    const bootstrapSource = await mocks.createObjectURL.mock.calls[0][0].text();
    expect(bootstrapSource).toContain("subscriptions");
    expect(bootstrapSource).toContain("secrets.get");
    expect(bootstrapSource).toContain("tasks.executeTask");
    expect(bootstrapSource).toContain("window.createTerminal");
    expect(bootstrapSource).toContain("args.map(reviveWorkerValue)");
    expect(bootstrapSource).toContain("Object.getPrototypeOf(current)");
    expect(
      await executeVscodeExtensionCommand("acme.demo.hello", "one", 2),
    ).toBe("worker:one|2");
  });

  it("falls back from an incompatible browser entry to a bundled CommonJS main entry", async () => {
    mocks.getManifests.mockResolvedValue([executableManifest]);
    useExecutablePackage({
      browserSource: "export function activate() {}",
      mainSource: "module.exports = { activate() {} };",
    });

    await activateOnLanguage("typescript");

    expect(getActivatedExtensions()).toContain("acme.demo");
    const init = MockExtensionWorker.instances[0].messages.find(
      (message) => message.type === "init",
    );
    expect(init?.mainPath).toBe("extension/dist/main.js");
    expect(mocks.readExtensionFile).toHaveBeenCalledWith(
      "acme",
      "demo",
      "extension/dist/browser.js",
    );
    expect(mocks.readExtensionFile).toHaveBeenCalledWith(
      "acme",
      "demo",
      "extension/dist/main.js",
    );
  });

  it("falls back when the browser bundle requires a non-vscode CommonJS dependency", async () => {
    mocks.getManifests.mockResolvedValue([executableManifest]);
    useExecutablePackage({
      browserSource:
        'const path = require("path"); module.exports = { activate() { return path; } };',
      mainSource: 'module.exports = { activate() {} };',
    });

    await activateOnLanguage("typescript");

    expect(getActivatedExtensions()).toContain("acme.demo");
    const init = MockExtensionWorker.instances[0].messages.find(
      (message) => message.type === "init",
    );
    expect(init?.mainPath).toBe("extension/dist/main.js");
  });

  it("rejects commented dynamic imports in bundled CommonJS entries", async () => {
    mocks.getManifests.mockResolvedValue([executableManifest]);
    useExecutablePackage({
      browserSource: "export function activate() {}",
      mainSource:
        'module.exports = { activate() {} }; import/* remote */("https://attacker.invalid");',
    });

    await activateOnLanguage("typescript");

    expect(getActivatedExtensions()).not.toContain("acme.demo");
    expect(MockExtensionWorker.instances).toHaveLength(0);
  });

  it("rejects commented static imports in bundled CommonJS entries", async () => {
    mocks.getManifests.mockResolvedValue([executableManifest]);
    useExecutablePackage({
      browserSource: "export function activate() {}",
      mainSource:
        'module.exports = { activate() {} }; /* remote */ import "https://attacker.invalid/module.js";',
    });

    await activateOnLanguage("typescript");

    expect(getActivatedExtensions()).not.toContain("acme.demo");
    expect(MockExtensionWorker.instances).toHaveLength(0);
  });

  it("fails Worker secret RPC closed without a trusted backend identity", async () => {
    mocks.getManifests.mockResolvedValue([executableManifest]);
    mocks.getSecurityInfo.mockReturnValue(
      securityInfo({
        level: "reviewed",
        permissions: ["secrets.read", "secrets.write"],
      }),
    );
    useExecutablePackage();
    MockExtensionWorker.behavior = "secrets";

    await activateOnLanguage("typescript");

    expect(getActivatedExtensions()).not.toContain("acme.demo");
    const worker = MockExtensionWorker.instances[0];
    expect(worker.rpcResults[0]).toMatchObject({
      id: 1,
      error: expect.stringMatching(/ERR_EXTENSION_SECRET_STORAGE_UNAVAILABLE/),
    });
    expect(mocks.callById).not.toHaveBeenCalled();
  });

  it("rejects Worker secret access when the extension omitted its permission", async () => {
    mocks.getManifests.mockResolvedValue([executableManifest]);
    mocks.getSecurityInfo.mockReturnValue(securityInfo({ permissions: [] }));
    useExecutablePackage();
    MockExtensionWorker.behavior = "secret-permission-denied";

    await activateOnLanguage("typescript");

    expect(getActivatedExtensions()).not.toContain("acme.demo");
    expect(MockExtensionWorker.instances[0].rpcResults[0]).toMatchObject({
      id: 1,
      error: expect.stringMatching(/secrets\.read/i),
    });
    expect(
      mocks.callById.mock.calls.some((call) =>
        [1189645321, 1779126640, 1741758748].includes(Number(call[0])),
      ),
    ).toBe(false);
  });

  it("exposes executable task and terminal handles through Worker RPC", async () => {
    mocks.getManifests.mockResolvedValue([executableManifest]);
    mocks.getSecurityInfo.mockReturnValue(
      securityInfo({
        level: "reviewed",
        permissions: ["tasks.execute", "shell.execute"],
      }),
    );
    useExecutablePackage();
    MockExtensionWorker.behavior = "executable-resources";

    await activateOnLanguage("typescript");

    expect(getActivatedExtensions()).toContain("acme.demo");
    expect(
      mocks.callById.mock.calls.some((call) => call[0] === 1113621941),
    ).toBe(true);
    expect(
      mocks.callById.mock.calls.some((call) => call[0] === 93571653),
    ).toBe(true);
    expect(
      mocks.callById.mock.calls.some((call) => call[0] === 1861972639),
    ).toBe(true);
    await vi.waitFor(() =>
      expect(mocks.startTerminal).toHaveBeenCalledTimes(1),
    );
    await vi.waitFor(() =>
      expect(mocks.writeTerminal).toHaveBeenCalledWith(
        expect.any(String),
        "npm test\n",
      ),
    );
    await vi.waitFor(() =>
      expect(mocks.killTerminal).toHaveBeenCalledTimes(1),
    );
    expect(mocks.translate).toHaveBeenCalledWith(
      "extensionHost.confirmRuntimeOperation",
      expect.objectContaining({ operation: expect.any(String) }),
    );
    expect(
      mocks.translate.mock.calls.every(
        ([, params]) =>
          params !== undefined &&
          Object.keys(params).length === 1 &&
          typeof params.operation === "string",
      ),
    ).toBe(true);
    expect(JSON.stringify(vi.mocked(globalThis.confirm).mock.calls)).not.toContain(
      "npm test",
    );
  });

  it("releases Worker task handles after backend execution completes", async () => {
    mocks.getManifests.mockResolvedValue([executableManifest]);
    mocks.getSecurityInfo.mockReturnValue(
      securityInfo({
        level: "reviewed",
        permissions: ["tasks.execute"],
      }),
    );
    mocks.callById.mockResolvedValue(undefined);
    useExecutablePackage();
    MockExtensionWorker.behavior = "task-completion";

    await activateOnLanguage("typescript");

    const worker = MockExtensionWorker.instances[0];
    await vi.waitFor(() => {
      expect(worker.messages).toContainEqual(
        expect.objectContaining({ type: "task-completed" }),
      );
      expect(worker.rpcResults).toContainEqual(
        expect.objectContaining({
          id: 2,
          error: expect.stringContaining("Unknown task execution handle"),
        }),
      );
    });
  });

  it("routes the declared VS Code API surface through the real Worker RPC path", async () => {
    mocks.getManifests.mockResolvedValue([executableManifest]);
    mocks.getSecurityInfo.mockReturnValue(
      securityInfo({
        level: "restricted",
        permissions: [
          "fs.read",
          "fs.write",
          "ui.webview",
          "network",
          "debug.execute",
          "scm.read",
          "clipboard",
          "env.openExternal",
        ],
      }),
    );
    useExecutablePackage();
    MockExtensionWorker.behavior = "full-api-surface";
    const openExternal = vi.spyOn(window, "open").mockReturnValue(window);

    await activateOnLanguage("typescript");

    const worker = MockExtensionWorker.instances[0];
    expect(worker.rpcResults.find((entry) => entry.error !== undefined)).toBeUndefined();
    expect(getActivatedExtensions()).toContain("acme.demo");
    expect(mocks.readFile).toHaveBeenCalledWith("C:/workspace/readme.md");
    expect(mocks.writeFile).toHaveBeenCalledWith(
      "C:/workspace/readme.md",
      "updated",
    );
    expect(mocks.launchDebug).toHaveBeenCalledWith(
      expect.objectContaining({
        name: "Worker Debug",
        kind: "node",
        program: "index.js",
      }),
    );
    expect(openExternal).toHaveBeenCalledWith(
      "https://example.test/docs",
      "_blank",
    );

    expect(worker.rpcResults).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ id: 1, result: expect.any(Number) }),
        expect.objectContaining({ id: 7, result: expect.any(Number) }),
        expect.objectContaining({ id: 9, result: "worker clipboard" }),
        expect.objectContaining({ id: 10, result: true }),
      ]),
    );
    expect(worker.rpcResults.every((entry) => entry.error === undefined)).toBe(true);

    const iframe = document.body.querySelector("iframe");
    expect(iframe).not.toBeNull();
    const parsed = new DOMParser().parseFromString(iframe!.srcdoc, "text/html");
    const script = parsed.querySelector("script");
    const nonce = script?.getAttribute("nonce");
    const csp = parsed
      .querySelector('meta[http-equiv="Content-Security-Policy"]')
      ?.getAttribute("content");
    expect(script?.textContent).toContain("globalThis.ready");
    expect(nonce).toMatch(/^[a-f0-9]{32}$/);
    expect(csp).toContain(`script-src 'nonce-${nonce}'`);
  });

  it("removes Worker SCM group handles when their parent source control is disposed", async () => {
    mocks.getManifests.mockResolvedValue([executableManifest]);
    mocks.getSecurityInfo.mockReturnValue(
      securityInfo({ permissions: ["scm.read"] }),
    );
    useExecutablePackage();
    MockExtensionWorker.behavior = "scm-lifecycle";

    await activateOnLanguage("typescript");

    const worker = MockExtensionWorker.instances[0];
    expect(getActivatedExtensions()).toContain("acme.demo");
    expect(worker.rpcResults).toContainEqual(
      expect.objectContaining({
        id: 4,
        error: expect.stringContaining("Unknown source control group handle"),
      }),
    );
  });

  it.each(["deactivate", "crash"] as const)(
    "terminates an outstanding Worker task during %s cleanup",
    async (cleanup) => {
      mocks.getManifests.mockResolvedValue([executableManifest]);
      mocks.getSecurityInfo.mockReturnValue(
        securityInfo({
          level: "reviewed",
          permissions: ["tasks.execute"],
        }),
      );
      useExecutablePackage();
      MockExtensionWorker.behavior = "active-task";
      await activateOnLanguage("typescript");
      const worker = MockExtensionWorker.instances[0];

      expect(getActivatedExtensions()).toContain("acme.demo");
      expect(
        mocks.callById.mock.calls.some((call) => call[0] === 1861972639),
      ).toBe(false);

      if (cleanup === "deactivate") {
        await deactivateExtension("acme.demo");
      } else {
        worker.crash("task worker crash");
        await vi.waitFor(() => expect(worker.terminated).toBe(true));
      }

      await vi.waitFor(() =>
        expect(
          mocks.callById.mock.calls.some((call) => call[0] === 1861972639),
        ).toBe(true),
      );
    },
  );

  it.each([
    ["disabled", { enabled: false }],
    ["pending review", { pendingReview: true }],
    ["blacklisted", { blacklisted: true }],
    ["integrity unchecked", { integrityChecked: false, verified: true }],
  ] as const)(
    "fails closed for a %s extension",
    async (_label, overrides) => {
      mocks.getManifests.mockResolvedValue([executableManifest]);
      mocks.getSecurityInfo.mockReturnValue(securityInfo(overrides));
      useExecutablePackage();

      await activateOnLanguage("typescript");

      expect(getActivatedExtensions()).not.toContain("acme.demo");
      expect(MockExtensionWorker.instances).toHaveLength(0);
      expect(
        listVscodeExtensionCommands().some(
          (command) => command.extensionId === "acme.demo",
        ),
      ).toBe(false);
    },
  );

  it("approves a backend-enabled Restricted extension before host activation", async () => {
    mocks.getManifests.mockResolvedValue([executableManifest]);
    mocks.getSecurityInfo.mockReturnValue(
      securityInfo({
        level: "restricted",
        permissions: ["network"],
      }),
    );
    useExecutablePackage();

    await activateOnLanguage("typescript");

    expect(getActivatedExtensions()).toContain("acme.demo");
    expect(MockExtensionWorker.instances).toHaveLength(1);
  });

  it("deactivates the real host and terminates its Worker", async () => {
    mocks.getManifests.mockResolvedValue([executableManifest]);
    useExecutablePackage();
    await activateOnLanguage("typescript");
    const worker = MockExtensionWorker.instances[0];

    await deactivateExtension("acme.demo");

    expect(worker.deactivationRequested).toBe(true);
    expect(worker.terminated).toBe(true);
    expect(worker.lifecycle.slice(-2)).toEqual(["post:terminate", "terminate"]);
    expect(getActivatedExtensions()).not.toContain("acme.demo");
    await expect(
      executeVscodeExtensionCommand("acme.demo.hello"),
    ).rejects.toThrow(/not registered/i);
  });

  it("times out an unacknowledged Worker deactivation and releases listeners", async () => {
    mocks.getManifests.mockResolvedValue([executableManifest]);
    useExecutablePackage();
    await activateOnLanguage("typescript");
    const worker = MockExtensionWorker.instances[0];
    MockExtensionWorker.behavior = "no-deactivation-ack";
    vi.useFakeTimers();
    try {
      const deactivation = deactivateExtension("acme.demo");
      const rejection = expect(deactivation).rejects.toThrow(
        /deactivation timed out/i,
      );

      await vi.advanceTimersByTimeAsync(10_000);
      await rejection;

      expect(worker.deactivationRequested).toBe(true);
      expect(worker.terminated).toBe(true);
      expect(worker.onmessage).toBeNull();
      expect(worker.onerror).toBeNull();
      expect(worker.onmessageerror).toBeNull();
      expect(vi.getTimerCount()).toBe(0);
      expect(mocks.reportDeactivated).toHaveBeenCalledWith("acme.demo");
    } finally {
      vi.useRealTimers();
    }
  });

  it("terminates active Workers during reset and permits a clean successor state", async () => {
    mocks.getManifests.mockResolvedValue([executableManifest]);
    useExecutablePackage();
    await activateOnLanguage("typescript");
    const worker = MockExtensionWorker.instances[0];

    await resetActivationState();

    expect(worker.deactivationRequested).toBe(true);
    expect(worker.terminated).toBe(true);
    await loadInstalledExtensionManifests();
    expect(mocks.getManifests).toHaveBeenCalledTimes(2);
  });

  it("does not wait for the activation timeout when reset interrupts registration", async () => {
    mocks.getManifests.mockResolvedValue([executableManifest]);
    useExecutablePackage();
    MockExtensionWorker.behavior = "hung-registration";

    const activation = activateOnLanguage("typescript");
    await vi.waitFor(() => expect(MockExtensionWorker.instances).toHaveLength(1));
    const worker = MockExtensionWorker.instances[0];

    await resetActivationState();
    await activation;

    expect(worker.deactivationRequested).toBe(true);
    expect(worker.terminated).toBe(true);
    expect(getActivatedExtensions()).not.toContain("acme.demo");
  });

  it("rolls back host and static command registrations when Worker activation fails", async () => {
    mocks.getManifests.mockResolvedValue([executableManifest]);
    useExecutablePackage();
    MockExtensionWorker.behavior = "activation-error-after-register";

    await activateOnLanguage("typescript");

    expect(getActivatedExtensions()).not.toContain("acme.demo");
    expect(MockExtensionWorker.instances[0].terminated).toBe(true);
    expect(
      listVscodeExtensionCommands().some(
        (command) => command.extensionId === "acme.demo",
      ),
    ).toBe(false);
  });

  it("surfaces the original Worker activation error through a lazy command", async () => {
    mocks.getManifests.mockResolvedValue([executableManifest]);
    useExecutablePackage();
    MockExtensionWorker.behavior = "activation-error-after-register";

    await loadInstalledExtensionManifests();
    await expect(
      executeVscodeExtensionCommand("acme.demo.hello"),
    ).rejects.toThrow(/worker activate failed/i);

    expect(getActivatedExtensions()).not.toContain("acme.demo");
    expect(MockExtensionWorker.instances[0].terminated).toBe(true);
  });

  it("fails closed when the Worker reports no compatible ABI", async () => {
    mocks.getManifests.mockResolvedValue([executableManifest]);
    useExecutablePackage();
    MockExtensionWorker.behavior = "incompatible-protocol";

    await activateOnLanguage("typescript");

    expect(getActivatedExtensions()).not.toContain("acme.demo");
    expect(MockExtensionWorker.instances).toHaveLength(1);
    expect(MockExtensionWorker.instances[0].terminated).toBe(true);
    expect(
      listVscodeExtensionCommands().some(
        (command) => command.extensionId === "acme.demo",
      ),
    ).toBe(false);
  });

  it("rejects Worker RPC sent before ABI negotiation", async () => {
    mocks.getManifests.mockResolvedValue([executableManifest]);
    useExecutablePackage();
    MockExtensionWorker.behavior = "message-before-negotiation";

    await activateOnLanguage("typescript");

    expect(getActivatedExtensions()).not.toContain("acme.demo");
    expect(MockExtensionWorker.instances).toHaveLength(1);
    expect(MockExtensionWorker.instances[0].terminated).toBe(true);
    expect(
      listVscodeExtensionCommands().some(
        (command) => command.extensionId === "acme.demo",
      ),
    ).toBe(false);
  });

  it("ignores forged Worker tokens before dispatching privileged RPC", async () => {
    mocks.getManifests.mockResolvedValue([executableManifest]);
    useExecutablePackage();
    await activateOnLanguage("typescript");
    const worker = MockExtensionWorker.instances[0];

    worker.emitForged({
      type: "rpc",
      id: 99,
      method: "tasks.executeTask",
      args: [{ name: "forged", source: "forged" }],
    });
    await Promise.resolve();

    expect(worker.terminated).toBe(false);
    expect(getActivatedExtensions()).toContain("acme.demo");
    expect(
      mocks.callById.mock.calls.some((call) => call[0] === 1_113_621_941),
    ).toBe(false);
  });

  it("terminates and restarts a Worker that stops answering health checks", async () => {
    vi.useFakeTimers();
    try {
      mocks.getManifests.mockResolvedValue([executableManifest]);
      useExecutablePackage();
      MockExtensionWorker.behavior = "no-health-response";
      await activateOnLanguage("typescript");
      const worker = MockExtensionWorker.instances[0];
      expect(getActivatedExtensions()).toContain("acme.demo");

      MockExtensionWorker.behavior = "success";
      await vi.advanceTimersByTimeAsync(8_000);
      await vi.waitFor(() =>
        expect(MockExtensionWorker.instances).toHaveLength(2),
      );
      await vi.waitFor(() =>
        expect(getActivatedExtensions()).toContain("acme.demo"),
      );

      expect(worker.terminated).toBe(true);
      expect(worker.lifecycle.slice(-2)).toEqual(["post:terminate", "terminate"]);
      await resetActivationState();
      expect(vi.getTimerCount()).toBe(0);
    } finally {
      vi.useRealTimers();
    }
  });

  it("terminates and restarts a Worker that exceeds the message-rate quota", async () => {
    mocks.getManifests.mockResolvedValue([executableManifest]);
    useExecutablePackage();
    await activateOnLanguage("typescript");
    const worker = MockExtensionWorker.instances[0];

    worker.emitMessageFlood();

    await vi.waitFor(() => expect(MockExtensionWorker.instances).toHaveLength(2));
    await vi.waitFor(() => expect(getActivatedExtensions()).toContain("acme.demo"));
    expect(worker.terminated).toBe(true);
    expect(worker.lifecycle.slice(-2)).toEqual(["post:terminate", "terminate"]);
  });

  it("terminates and restarts a Worker that exceeds the message-size quota", async () => {
    mocks.getManifests.mockResolvedValue([executableManifest]);
    useExecutablePackage();
    await activateOnLanguage("typescript");
    const worker = MockExtensionWorker.instances[0];

    worker.emitOversizedMessage();

    await vi.waitFor(() => expect(MockExtensionWorker.instances).toHaveLength(2));
    await vi.waitFor(() => expect(getActivatedExtensions()).toContain("acme.demo"));
    expect(worker.terminated).toBe(true);
    expect(worker.lifecycle.slice(-2)).toEqual(["post:terminate", "terminate"]);
  });

  it("restarts an active Worker and restores extension activation state", async () => {
    mocks.getManifests.mockResolvedValue([executableManifest]);
    useExecutablePackage();
    await activateOnLanguage("typescript");
    const worker = MockExtensionWorker.instances[0];

    worker.crash("runtime crash");

    await vi.waitFor(() => expect(MockExtensionWorker.instances).toHaveLength(2));
    await vi.waitFor(() =>
      expect(getActivatedExtensions()).toContain("acme.demo"),
    );
    expect(worker.terminated).toBe(true);
    expect(worker.lifecycle.slice(-2)).toEqual(["post:terminate", "terminate"]);
    expect(
      listVscodeExtensionCommands().some(
        (command) => command.extensionId === "acme.demo",
      ),
    ).toBe(true);
  });

  it("resets the consecutive restart-failure count after a successful recovery", async () => {
    mocks.getManifests.mockResolvedValue([executableManifest]);
    useExecutablePackage();
    await activateOnLanguage("typescript");

    for (let recovery = 0; recovery < 4; recovery += 1) {
      const worker = MockExtensionWorker.instances.at(-1)!;
      worker.crash(`runtime crash ${recovery + 1}`);
      await vi.waitFor(() =>
        expect(MockExtensionWorker.instances).toHaveLength(recovery + 2),
      );
      await vi.waitFor(() =>
        expect(getActivatedExtensions()).toContain("acme.demo"),
      );
      expect(worker.terminated).toBe(true);
    }
  });

  it("recovers from Worker message errors through the same restart path", async () => {
    mocks.getManifests.mockResolvedValue([executableManifest]);
    useExecutablePackage();
    await activateOnLanguage("typescript");
    const worker = MockExtensionWorker.instances[0];

    worker.messageError();

    await vi.waitFor(() => expect(MockExtensionWorker.instances).toHaveLength(2));
    await vi.waitFor(() => expect(getActivatedExtensions()).toContain("acme.demo"));
    expect(worker.lifecycle.slice(-2)).toEqual(["post:terminate", "terminate"]);
  });

  it("does not restart a Worker that crashes during explicit deactivation", async () => {
    mocks.getManifests.mockResolvedValue([executableManifest]);
    useExecutablePackage();
    await activateOnLanguage("typescript");
    const worker = MockExtensionWorker.instances[0];

    const deactivation = deactivateExtension("acme.demo");
    worker.crash("deactivation crash");
    await expect(deactivation).rejects.toThrow("deactivation crash");
    await Promise.resolve();

    expect(MockExtensionWorker.instances).toHaveLength(1);
    expect(getActivatedExtensions()).not.toContain("acme.demo");
  });

  it("stops after three failed Worker restart activations", async () => {
    mocks.getManifests.mockResolvedValue([executableManifest]);
    useExecutablePackage();
    await activateOnLanguage("typescript");
    const firstWorker = MockExtensionWorker.instances[0];
    MockExtensionWorker.crashOnInitCount = 3;

    firstWorker.crash("runtime crash");

    await vi.waitFor(() => expect(MockExtensionWorker.instances).toHaveLength(4));
    await vi.waitFor(() =>
      expect(getActivatedExtensions()).not.toContain("acme.demo"),
    );
    await Promise.resolve();
    expect(MockExtensionWorker.instances).toHaveLength(4);
    for (const worker of MockExtensionWorker.instances) {
      expect(worker.lifecycle.slice(-2)).toEqual(["post:terminate", "terminate"]);
    }
  });

  it.skipIf(!realCorpusAvailable)("installs fixed-SHA VSIX packages through the production installer before real Worker activation", async () => {
    const specs: InstalledCorpusSpec[] = [
      {
        file: "Catppuccin.catppuccin-vsc-3.19.0.vsix",
        sha256: "ebf347664837edbe91c9920ff3d14c96d4a28beeec0b95137c76058326329780",
        publisher: "Catppuccin", name: "catppuccin-vsc", version: "3.19.0", entry: "dist/browser.cjs",
      },
      {
        file: "PKief.material-icon-theme-5.37.0.vsix",
        sha256: "ade9adefe3909cea92aed52850ddd00975d1dc1b62fe558831f6fb8b88f7c3ce",
        publisher: "PKief", name: "material-icon-theme", version: "5.37.0", entry: "dist/extension/web/extension.cjs",
      },
      {
        file: "mechatroner.rainbow-csv-3.24.1.vsix",
        sha256: "0ecb7da3fb2a54517cd41fce8e858d6276ea8523bed6fbfd64d5ed281bd7514a",
        publisher: "mechatroner", name: "rainbow-csv", version: "3.24.1", entry: "dist/web/extension.js",
      },
      {
        file: "redhat.vscode-yaml-1.25.2026080708.vsix",
        sha256: "23263c28e7b729656d6898f9f15d5190514decbe7ad38692f8888af9db3f0b78",
        publisher: "redhat", name: "vscode-yaml", version: "1.25.2026080708", entry: "dist/extension-web.js",
      },
    ];
    const initialSelection = {
      start: { line: 0, character: 0 },
      end: { line: 0, character: 0 },
      active: { line: 0, character: 0 },
      anchor: { line: 0, character: 0 },
      isEmpty: true,
    } as unknown as Selection;
    let selectionState = initialSelection;
    const revealedRanges: Array<{ range: unknown; revealType?: number }> = [];
    let configRoot = "";
    let goToColumn: Promise<unknown> | undefined;
    try {
      configRoot = installRealCorpusPackages(specs);
      const packages = specs.map((spec) => readInstalledCorpusPackage(configRoot, spec));
      const extensionRoots = new Map(specs.map((spec) => [
        spec.publisher + "." + spec.name,
        resolvePath(
          configRoot,
          "koyori-ide",
          "extensions",
          spec.publisher + "." + spec.name,
        ),
      ]));
      mocks.getManifests.mockResolvedValue(packages.map((pkg) => pkg.manifest));
      mocks.triggerEager.mockResolvedValue(packages.map((pkg) => pkg.publisher + "." + pkg.name));
      mocks.readExtensionFile.mockImplementation(async (publisher: string, name: string, path: string) => {
        const extensionId = publisher + "." + name;
        const extensionRoot = extensionRoots.get(extensionId);
        if (!extensionRoot) throw new Error("unknown installed package " + extensionId);
        const requestedPath = resolvePath(extensionRoot, path);
        const containedPath = relativePath(extensionRoot, requestedPath);
        if (
          isAbsolute(path) ||
          isAbsolute(containedPath) ||
          containedPath.split(/[\\/]/, 1)[0] === ".."
        ) {
          throw new Error("installed VSIX path escapes extension root: " + path);
        }
        return new Uint8Array(readFileSync(requestedPath));
      });
      mocks.getSecurityInfo.mockImplementation((id: string) => securityInfo({
        extensionId: id,
        ...(id === "redhat.vscode-yaml" ? {
          permissions: ["fs.read", "fs.write", "network", "ui.notifications"],
          level: "restricted",
        } : {}),
      }));
      setExtensionHostDecorationCallback((_extensionId, _options) => ({
        key: "installed-corpus-decoration",
        dispose: () => undefined,
      }));
      setExtensionHostInputCallback(showExtensionInputBox);
      setExtensionHostStatusBarCallback(
        () => ({ dispose: () => undefined }),
        () => ({
          text: "",
          tooltip: undefined,
          command: undefined,
          show: () => undefined,
          hide: () => undefined,
          dispose: () => undefined,
        }),
      );
      setExtensionHostOutputCallback(() => undefined);
      setExtensionHostActiveEditorCallback(() => ({
        document: {
          uri: { fsPath: "C:/workspace/sample.csv", path: "/workspace/sample.csv", scheme: "file" },
          fileName: "C:/workspace/sample.csv",
          languageId: "csv",
          lineCount: 2,
          lineAt: (line: number) => ({ text: ["a,b", "1,2"][line] ?? "" }),
          getText: () => "a,b\n1,2",
        },
        set selection(value) {
          selectionState = value;
        },
        get selection() {
          return selectionState;
        },
        revealRange: (range, revealType) => {
          revealedRanges.push({ range, revealType });
        },
        setDecorations: () => undefined,
      }));
      realCorpusWorkerBlob = undefined;
      mocks.createObjectURL.mockImplementation((blob: Blob) => {
        realCorpusWorkerBlob = blob;
        return "blob:installed-corpus-worker";
      });
      vi.stubGlobal("Worker", RealCorpusWorker as unknown as typeof Worker);

      await expect(activateEager()).resolves.toEqual(expect.arrayContaining([
        "Catppuccin.catppuccin-vsc",
        "PKief.material-icon-theme",
        "mechatroner.rainbow-csv",
      ]));
      expect(getActivatedExtensions()).toEqual(expect.arrayContaining([
        "Catppuccin.catppuccin-vsc",
        "PKief.material-icon-theme",
        "mechatroner.rainbow-csv",
      ]));

      const materialCommand = listVscodeExtensionCommands().find((command) =>
        command.extensionId === "PKief.material-icon-theme"
      );
      expect(materialCommand).toBeDefined();

      const catppuccinThemes = listVscodeExtensionThemes().filter((theme) =>
        theme.extensionId === "Catppuccin.catppuccin-vsc"
      );
      expect(catppuccinThemes).toHaveLength(4);
      const mocha = catppuccinThemes.find((theme) => theme.path === "./themes/mocha.json");
      expect(mocha).toEqual(expect.objectContaining({
        label: "Catppuccin Mocha",
        uiTheme: "vs-dark",
      }));
      expect(getActiveVscodeExtensionTheme()).toBeUndefined();
      mocks.defineTheme.mockImplementationOnce(() => {
        expect(getActiveVscodeExtensionTheme()).toBeUndefined();
      });
      await applyVscodeExtensionTheme(mocha!.key);
      expect(mocks.readExtensionFile).toHaveBeenCalledWith(
        "Catppuccin",
        "catppuccin-vsc",
        "extension/themes/mocha.json",
      );
      expect(mocks.defineTheme).toHaveBeenCalledWith(
        `koyoriIde-extension:${mocha!.key}`,
        expect.objectContaining({
          base: "vs-dark",
          inherit: true,
          colors: expect.objectContaining({
            "editor.background": "#1e1e2e",
            "editor.foreground": "#cdd6f4",
          }),
          rules: expect.arrayContaining([
            { token: "comment", foreground: "9399b2", fontStyle: "italic" },
            { token: "keyword", foreground: "cba6f7", fontStyle: "" },
          ]),
        }),
      );
      expect(getActiveVscodeExtensionTheme()).toEqual(mocha);
      expect(mocks.setTheme).toHaveBeenCalledWith(`koyoriIde-extension:${mocha!.key}`);

      goToColumn = executeVscodeExtensionCommand("rainbow-csv.GoToColumn");
      await vi.waitFor(() => {
        expect(document.querySelector(".el-message-box__input input")).not.toBeNull();
        expect(document.querySelector(".el-message-box__message")?.textContent).toContain("column number");
      });
      const input = document.querySelector<HTMLInputElement>(".el-message-box__input input")!;
      input.value = "1";
      input.dispatchEvent(new Event("input", { bubbles: true }));
      const confirm = Array.from(document.querySelectorAll<HTMLButtonElement>(".el-message-box button"))
        .find((button) => button.textContent?.trim() === "OK");
      expect(confirm).toBeDefined();
      confirm!.click();
      await goToColumn;
      await vi.waitFor(() => expect(revealedRanges).toHaveLength(1));
      expect(revealedRanges[0]).toEqual(expect.objectContaining({ revealType: 2 }));
      expect(selectionState.active).toEqual({ line: 0, character: 0 });

      const yamlError = getExtensionActivationError("redhat.vscode-yaml");
      expect(yamlError?.message).toBe(
        "KOYORI_IDE_EXT_API_UNSUPPORTED: vscode.CompletionItem is not implemented (koyori-ide extension API v1)",
      );
      expect(getActivatedExtensions()).not.toContain("redhat.vscode-yaml");
      expect(mocks.reportActivation).toHaveBeenCalledWith("redhat.vscode-yaml", false);
      await deactivateExtension("Catppuccin.catppuccin-vsc");
      expect(getActiveVscodeExtensionTheme()).toBeUndefined();
      expect(mocks.setTheme).toHaveBeenLastCalledWith("koyoriIde-blue");
    } finally {
      const cancel = Array.from(document.querySelectorAll<HTMLButtonElement>(".el-message-box button"))
        .find((button) => button.textContent?.trim() === "Cancel");
      if (cancel) {
        cancel.click();
        await goToColumn?.catch(() => undefined);
      }
      try {
        await resetActivationState();
      } finally {
        setExtensionHostActiveEditorCallback(() => undefined);
        setExtensionHostDecorationCallback(undefined);
        setExtensionHostInputCallback(undefined);
        setExtensionHostOutputCallback(undefined);
        setExtensionHostStatusBarCallback(undefined, undefined);
        document.querySelectorAll(".el-overlay, .el-message-box__wrapper").forEach((element) => element.remove());
        if (configRoot) rmSync(configRoot, { recursive: true, force: true });
      }
    }
  }, 180_000);

  it.skipIf(!realCorpusAvailable)("executes fixed-SHA third-party browser VSIX bundles in a real Worker", async () => {
    const packages = [
      readRealCorpusPackage(
        "Catppuccin.catppuccin-vsc-3.19.0.vsix",
        "ebf347664837edbe91c9920ff3d14c96d4a28beeec0b95137c76058326329780",
        "Catppuccin",
        "catppuccin-vsc",
        "dist/browser.cjs",
      ),
      readRealCorpusPackage(
        "PKief.material-icon-theme-5.37.0.vsix",
        "ade9adefe3909cea92aed52850ddd00975d1dc1b62fe558831f6fb8b88f7c3ce",
        "PKief",
        "material-icon-theme",
        "dist/extension/web/extension.cjs",
      ),
      readRealCorpusPackage(
        "mechatroner.rainbow-csv-3.24.1.vsix",
        "0ecb7da3fb2a54517cd41fce8e858d6276ea8523bed6fbfd64d5ed281bd7514a",
        "mechatroner",
        "rainbow-csv",
        "dist/web/extension.js",
      ),
    ];
    const byId = new Map(packages.map((pkg) => [pkg.publisher + "." + pkg.name, pkg]));
    mocks.getManifests.mockResolvedValue(packages.map((pkg) => pkg.manifest));
    mocks.triggerEager.mockResolvedValue(Array.from(byId.keys()));
    mocks.readExtensionFile.mockImplementation(async (publisher: string, name: string, path: string) => {
      const pkg = byId.get(publisher + "." + name);
      if (!pkg) throw new Error("unknown corpus package " + publisher + "." + name);
      if (path === "extension/package.json") return encode(JSON.stringify(pkg.packageJSON));
      const entry = typeof pkg.packageJSON.browser === "string" ? pkg.packageJSON.browser : pkg.packageJSON.main;
      const normalizedEntry = typeof entry === "string" ? entry.replace(/^\.\//, "") : "";
      if (path === "extension/" + normalizedEntry) return encode(pkg.source);
      throw new Error("unexpected real VSIX path: " + path);
    });
    mocks.getSecurityInfo.mockImplementation((extensionId: string) =>
      securityInfo({
        extensionId,
        permissions: ["ui.notifications"],
        level: "trusted",
      }),
    );
    realCorpusWorkerBlob = undefined;
    mocks.createObjectURL.mockImplementation((blob: Blob) => {
      realCorpusWorkerBlob = blob;
      return "blob:real-corpus-worker";
    });
    vi.stubGlobal("Worker", RealCorpusWorker as unknown as typeof Worker);
    const createdDecorations: Array<{ key: string; options: unknown; disposed: boolean }> = [];
    const appliedDecorations: Array<{ key: string; ranges: unknown }> = [];
    const statusBarTexts: string[] = [];
    const outputEvents: Array<{ channel: string; action: string; value?: string }> = [];
    const initialSelection = {
      start: { line: 0, character: 0 },
      end: { line: 0, character: 0 },
      active: { line: 0, character: 0 },
      anchor: { line: 0, character: 0 },
      isEmpty: true,
    } as unknown as Selection;
    let selectionState = initialSelection;
    const revealedRanges: Array<{ range: unknown; revealType?: number }> = [];
    setExtensionHostInputCallback(showExtensionInputBox);
    setExtensionHostOutputCallback((channel, action, value) => {
      outputEvents.push({ channel, action, value });
    });
    setExtensionHostStatusBarCallback(
      (text) => {
        statusBarTexts.push(text);
        return { dispose: () => undefined };
      },
      () => {
        let text = "";
        return {
          get text() { return text; },
          set text(value: string) { text = value; statusBarTexts.push(value); },
          show: () => undefined,
          hide: () => undefined,
          dispose: () => undefined,
        };
      },
    );
    setExtensionHostDecorationCallback((_extensionId, options) => {
      const record = {
        key: "real-test-decoration-" + (createdDecorations.length + 1),
        options,
        disposed: false,
      };
      createdDecorations.push(record);
      return {
        key: record.key,
        dispose: () => { record.disposed = true; },
      };
    });
    setExtensionHostWorkspaceFoldersCallback(() => [{
      uri: { fsPath: "C:/workspace", path: "/workspace", scheme: "file" },
      name: "workspace",
      index: 0,
    }]);
    setExtensionHostActiveEditorCallback(() => ({
      document: {
        uri: { fsPath: "C:/workspace/sample.csv", path: "/workspace/sample.csv", scheme: "file" },
        fileName: "C:/workspace/sample.csv",
        languageId: "csv",
        lineCount: 2,
        lineAt: (line: number) => ({ text: ["a,b", "1,2"][line] ?? "" }),
        getText: () => "a,b\n1,2",
      },
      set selection(value) {
        selectionState = value;
      },
      get selection() {
        return selectionState;
      },
      revealRange: (range, revealType) => {
        revealedRanges.push({ range, revealType });
      },
      setDecorations: (type, ranges) => {
        appliedDecorations.push({ key: type.key, ranges });
      },
    }));

    await expect(activateEager()).resolves.toEqual(expect.arrayContaining(Array.from(byId.keys())));

    await vi.waitFor(() => {
      expect(getActivatedExtensions()).toEqual(expect.arrayContaining([
        "Catppuccin.catppuccin-vsc",
        "PKief.material-icon-theme",
        "mechatroner.rainbow-csv",
      ]));
    }, { timeout: 10_000 });

    const visibleCommands = listVscodeExtensionCommands();
    expect(visibleCommands.some((command) => command.extensionId === "PKief.material-icon-theme")).toBe(true);
    expect(getActivatedExtensions()).toContain("mechatroner.rainbow-csv");
    expect(mocks.reportActivation).toHaveBeenCalledWith("Catppuccin.catppuccin-vsc", true);
    expect(mocks.reportActivation).toHaveBeenCalledWith("PKief.material-icon-theme", true);
    expect(mocks.reportActivation).toHaveBeenCalledWith("mechatroner.rainbow-csv", true);
    await vi.waitFor(() => expect(createdDecorations.length).toBeGreaterThanOrEqual(4));
    expect(createdDecorations.every((entry) => typeof entry.options === "object")).toBe(true);
    await executeVscodeExtensionCommand("rainbow-csv.ToggleRowBackground");
    await executeVscodeExtensionCommand("rainbow-csv.ToggleRowBackground");
    await vi.waitFor(() => expect(appliedDecorations.length).toBeGreaterThan(0));
    expect(statusBarTexts.length + outputEvents.length).toBeGreaterThan(0);

    const goToColumn = executeVscodeExtensionCommand("rainbow-csv.GoToColumn");
    await vi.waitFor(() => {
      expect(document.querySelector(".el-message-box__input input")).not.toBeNull();
      expect(document.querySelector(".el-message-box__message")?.textContent).toContain("column number");
    });
    const input = document.querySelector<HTMLInputElement>(".el-message-box__input input")!;
    input.value = "1";
    input.dispatchEvent(new Event("input", { bubbles: true }));
    const confirm = Array.from(document.querySelectorAll<HTMLButtonElement>(".el-message-box button"))
      .find((button) => button.textContent?.trim() === "OK");
    expect(confirm).toBeDefined();
    confirm!.click();
    await goToColumn;
    await vi.waitFor(() => expect(revealedRanges).toHaveLength(1));
    expect(revealedRanges[0]).toEqual(expect.objectContaining({ revealType: 2 }));
    expect(selectionState.active).toEqual({ line: 0, character: 0 });

    await deactivateExtension("mechatroner.rainbow-csv");
    expect(createdDecorations.every((entry) => entry.disposed)).toBe(true);
    setExtensionHostDecorationCallback(undefined);
    setExtensionHostInputCallback(undefined);
    setExtensionHostOutputCallback(undefined);
    setExtensionHostStatusBarCallback(undefined, undefined);
  }, 30_000);

  it.skipIf(!realCorpusAvailable)("recovers a fixed-SHA third-party VSIX after real Worker crash and rate faults", async () => {
    const pkg = readRealCorpusPackage(
      "Catppuccin.catppuccin-vsc-3.19.0.vsix",
      "ebf347664837edbe91c9920ff3d14c96d4a28beeec0b95137c76058326329780",
      "Catppuccin",
      "catppuccin-vsc",
      "dist/browser.cjs",
    );
    const extensionId = "Catppuccin.catppuccin-vsc";
    mocks.getManifests.mockResolvedValue([pkg.manifest]);
    mocks.triggerEager.mockResolvedValue([extensionId]);
    mocks.readExtensionFile.mockImplementation(
      async (_publisher: string, _name: string, path: string) => {
        if (path === "extension/package.json") {
          return encode(JSON.stringify(pkg.packageJSON));
        }
        if (path === "extension/dist/browser.cjs") return encode(pkg.source);
        throw new Error("unexpected Catppuccin VSIX path: " + path);
      },
    );
    mocks.getSecurityInfo.mockImplementation((id: string) =>
      securityInfo({
        extensionId: id,
        permissions: ["fs.read"],
        level: "trusted",
      }),
    );
    realCorpusWorkerBlob = undefined;
    mocks.createObjectURL.mockImplementation((blob: Blob) => {
      realCorpusWorkerBlob = blob;
      return "blob:catppuccin-recovery-worker";
    });
    vi.stubGlobal("Worker", RealCorpusWorker as unknown as typeof Worker);

    await activateEager();
    expect(getActivatedExtensions()).toContain(extensionId);
    expect(RealCorpusWorker.instances).toHaveLength(1);

    const crashed = RealCorpusWorker.instances[0];
    crashed.crash();
    await vi.waitFor(() => expect(RealCorpusWorker.instances).toHaveLength(2));
    await vi.waitFor(() => expect(getActivatedExtensions()).toContain(extensionId));
    expect(crashed.terminated).toBe(true);

    const rateLimited = RealCorpusWorker.instances[1];
    rateLimited.emitMessageFlood();
    await vi.waitFor(() => expect(RealCorpusWorker.instances).toHaveLength(3));
    await vi.waitFor(() => expect(getActivatedExtensions()).toContain(extensionId));
    expect(rateLimited.terminated).toBe(true);
    expect(mocks.reportActivation).toHaveBeenCalledWith(extensionId, true);
  }, 30_000);

  it.skipIf(!realCorpusAvailable)("fails closed for a fixed-SHA third-party VSIX that calls an unsupported API", async () => {
    const pkg = readRealCorpusPackage(
      "redhat.vscode-yaml-1.25.2026080708.vsix",
      "23263c28e7b729656d6898f9f15d5190514decbe7ad38692f8888af9db3f0b78",
      "redhat",
      "vscode-yaml",
      "dist/extension-web.js",
    );
    const extensionId = "redhat.vscode-yaml";
    mocks.getManifests.mockResolvedValue([pkg.manifest]);
    mocks.triggerLanguage.mockResolvedValue([extensionId]);
    mocks.readExtensionFile.mockImplementation(
      async (_publisher: string, _name: string, path: string) => {
        if (path === "extension/package.json") {
          return encode(JSON.stringify(pkg.packageJSON));
        }
        if (path === "extension/dist/extension-web.js") {
          return encode(pkg.source);
        }
        throw new Error("unexpected YAML VSIX path: " + path);
      },
    );
    mocks.getSecurityInfo.mockImplementation((id: string) =>
      securityInfo({
        extensionId: id,
        permissions: [
          "fs.read",
          "fs.write",
          "network",
          "ui.notifications",
        ],
        level: "restricted",
        enabled: true,
      }),
    );
    realCorpusWorkerBlob = undefined;
    mocks.createObjectURL.mockImplementation((blob: Blob) => {
      realCorpusWorkerBlob = blob;
      return "blob:yaml-unsupported-worker";
    });
    vi.stubGlobal("Worker", RealCorpusWorker as unknown as typeof Worker);

    await expect(activateOnLanguage("yaml")).resolves.toEqual([extensionId]);
    const activationError = getExtensionActivationError(extensionId);
    expect(activationError?.message).toContain(
      "KOYORI_IDE_EXT_API_UNSUPPORTED: vscode.CompletionItem",
    );
    expect(getActivatedExtensions()).not.toContain(extensionId);
    expect(mocks.reportActivation).toHaveBeenCalledWith(extensionId, false);
  }, 30_000);
});
