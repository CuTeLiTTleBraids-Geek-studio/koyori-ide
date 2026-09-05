import { afterEach, describe, expect, it, vi } from "vitest";
import type { Settings, WorkflowDef } from "@/types";

vi.mock("@wailsio/runtime", () => ({
  Call: { ByID: vi.fn(), ByName: vi.fn() },
  Create: {
    Any: (value: unknown) => value,
    Array: () => (value: unknown) => value,
    Map: () => (value: unknown) => value,
    Nullable: () => (value: unknown) => value,
    Struct: () => (value: unknown) => value,
  },
}));

import { fromBindingExtensionManifest } from "./extensions";
import { fromBindingSettings, toBindingSettings } from "./workspace";
import { fromBindingWorkflow, toBindingWorkflow } from "./automation";

function createSettings(): Settings {
  return {
    schemaVersion: 1,
    version: 7,
    expectedVersion: 7,
    language: "zh",
    theme: "dark",
    fontSize: 15,
    fontFamily: "JetBrains Mono",
    tabSize: 2,
    wordWrap: true,
    lineNumbers: true,
    minimap: false,
    aiApiKey: "",
    aiApiKeyConfigured: true,
    aiApiKeyStorageMethod: "keyring",
    aiBaseUrl: "https://api.example.com",
    aiModel: "example-model",
    aiSystemPrompt: "system",
    aiAgentSystemPrompt: "agent",
    aiConversationTitlePrompt: "title",
    aiInlineCompletionPrompt: "inline",
    cursorBlinking: "smooth",
    cursorStyle: "line",
    bracketColorization: true,
    autoSave: false,
    autoSaveDelay: "afterDelay",
    aiProvider: "openai",
    temperature: 0.6,
    maxTokens: 8192,
    defaultShell: "pwsh",
    terminalFontSize: 13,
    terminalCursorStyle: "block",
    scrollback: 10000,
    uiDensity: "comfortable",
    fontSizeScaling: 100,
    inlineCompletionEnabled: true,
    formatOnSave: true,
    trimTrailingWhitespace: false,
    insertSpaces: true,
    insertFinalNewline: true,
    gitBlameEnabled: false,
    emmetEnabled: true,
    emmetIncludeLanguages: { vue: "html" },
    customShortcuts: {
      Save: { key: "s", ctrl: true, shift: false, alt: false },
    },
    aiChatPosition: "right",
    activityBarVisible: true,
    agentPermissionMode: "always-ask",
    accentTheme: "blue",
    customAccent: null,
    enablePluginSandbox: true,
    designLanguage: "apple",
    aiProviderConfigs: [
      {
        id: "primary",
        name: "Primary",
        provider: "openai",
        protocol: "openai",
        apiKey: "",
        apiKeyConfigured: true,
        baseUrl: "https://api.example.com",
        model: "example-model",
        temperature: 0.6,
        maxTokens: 8192,
        systemPrompt: "system",
      },
    ],
    activeAIConfigId: "primary",
    toolPaths: { eslint: "C:/tools/eslint" },
    personalization: {
      codeEditorBgImage: "assets/editor.png",
      codeEditorBgOpacity: 0.35,
      codeEditorBgBlur: 4,
      chatBgImage: "assets/chat.png",
      chatBgOpacity: 0.4,
      chatBgBlur: 6,
      userAvatar: "assets/user.png",
      aiAvatar: "assets/ai.png",
      personaAvatars: { reviewer: "assets/reviewer.png" },
      fontFamily: "Inter",
      fontSize: 15,
      bubbleStyle: "rounded",
      bubbleOpacity: 0.9,
      messageSpacing: 12,
    },
    openAIWindowOnStartup: false,
    aiWindowTheme: "claude-dark",
    aiSidebarWidth: 320,
    aiTerminalWidth: 480,
    lspConfigs: {
      gopls: { "ui.completion.usePlaceholders": true },
    },
  };
}

function expectPrototypeSafeRecord(
  value: { [_ in string]?: unknown } | null | undefined,
  expectedPrototypeValue: unknown,
): void {
  expect(value).toBeDefined();
  if (!value) throw new Error("expected a normalized record");
  expect(Object.getPrototypeOf(value)).toBe(Object.prototype);
  expect(Object.hasOwn(value, "__proto__")).toBe(true);
  expect(value["__proto__"]).toEqual(expectedPrototypeValue);
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe("Settings DTO conversion", () => {
  it("round-trips every field represented by the current generated binding", () => {
    const frontend = createSettings();
    const { designLanguage, ...bindingSupportedSettings } = frontend;
    const binding = toBindingSettings(frontend);

    expect(designLanguage).toBe("apple");
    expect(binding).not.toHaveProperty("designLanguage");
    expect(binding).toEqual(bindingSupportedSettings);
    expect(fromBindingSettings(binding)).toEqual(bindingSupportedSettings);
  });

  it("warns and supplies stable defaults for malformed required fields", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    const malformed = createSettings();
    Object.defineProperty(malformed, "language", { value: undefined, enumerable: true });
    Object.defineProperty(malformed, "fontSize", { value: Number.NaN, enumerable: true });

    const binding = toBindingSettings(malformed);

    expect(binding.language).toBe("en");
    expect(binding.fontSize).toBe(14);
    expect(warn).toHaveBeenCalledWith(
      expect.stringContaining("Settings.language must be a string"),
    );
    expect(warn).toHaveBeenCalledWith(
      expect.stringContaining("Settings.fontSize must be an integer"),
    );
  });

  it("preserves __proto__ as an own data key across normalized settings maps", () => {
    const frontend = createSettings();
    const shortcut = { key: "p", ctrl: true, shift: false, alt: false };
    const lspConfig = { enabled: true };

    frontend.emmetIncludeLanguages = Object.fromEntries([
      ["__proto__", "html"],
      ["vue", "html"],
    ]);
    frontend.toolPaths = Object.fromEntries([
      ["__proto__", "C:/tools/proto"],
      ["eslint", "C:/tools/eslint"],
    ]);
    frontend.customShortcuts = Object.fromEntries([
      ["__proto__", shortcut],
      ["Save", { key: "s", ctrl: true, shift: false, alt: false }],
    ]);
    frontend.personalization = {
      ...frontend.personalization,
      personaAvatars: Object.fromEntries([
        ["__proto__", "assets/proto.png"],
        ["reviewer", "assets/reviewer.png"],
      ]),
    };
    frontend.lspConfigs = Object.fromEntries([
      ["__proto__", lspConfig],
      ["gopls", { enabled: false }],
    ]);

    const binding = toBindingSettings(frontend);

    expectPrototypeSafeRecord(binding.emmetIncludeLanguages ?? undefined, "html");
    expectPrototypeSafeRecord(binding.toolPaths ?? undefined, "C:/tools/proto");
    expectPrototypeSafeRecord(binding.customShortcuts ?? undefined, shortcut);
    expectPrototypeSafeRecord(
      binding.personalization?.personaAvatars ?? undefined,
      "assets/proto.png",
    );
    expectPrototypeSafeRecord(binding.lspConfigs ?? undefined, lspConfig);
  });
});

describe("Workflow DTO conversion", () => {
  it("round-trips binding-supported fields and exposes the outputs binding gap", () => {
    const workflow: WorkflowDef = {
      name: "release",
      description: "Build and publish",
      steps: [
        {
          name: "build",
          command: "npm",
          args: ["run", "build"],
          cwd: "frontend",
          dependsOn: [],
          condition: "test -f package.json",
          expectSuccess: false,
          type: "command",
          onFailure: "retry",
          timeout: 120,
          outputs: { artifact: "{{stdout}}" },
        },
      ],
      watch: ["src/**"],
      runOn: {
        event: "file-saved",
        glob: "src/**",
        workflowName: "build",
        condition: {
          branch: "main",
          language: "typescript",
          fileGlob: "src/**/*.ts",
        },
      },
      requiresConfirmation: true,
      source: ".koyori-ide/workflows/release.yml",
    };

    const binding = toBindingWorkflow(workflow);
    const { outputs, ...bindingSupportedStep } = workflow.steps[0];
    const bindingSupportedWorkflow = {
      ...workflow,
      steps: [bindingSupportedStep],
    };

    expect(outputs).toEqual({ artifact: "{{stdout}}" });
    expect(binding.steps?.[0]).not.toHaveProperty("outputs");
    expect(binding).toEqual(bindingSupportedWorkflow);
    expect(fromBindingWorkflow(binding)).toEqual(bindingSupportedWorkflow);
  });

  it.each([
    ["command", "abort"],
    ["ai", "continue"],
    ["git", "skip"],
    ["file", "retry"],
    ["mcp", "abort"],
    ["skill", "continue"],
  ] as const)("round-trips workflow enum values %s / %s", (type, onFailure) => {
    const workflow: WorkflowDef = {
      name: `${type}-${onFailure}`,
      steps: [{
        name: "step",
        command: "echo",
        expectSuccess: false,
        type,
        onFailure,
      }],
      source: ".koyori-ide/workflows/enums.yml",
    };

    expect(fromBindingWorkflow(toBindingWorkflow(workflow))).toEqual(workflow);
  });

  it("round-trips typed workflow adapter fields", () => {
    const workflow: WorkflowDef = {
      name: "read-notes",
      steps: [{
        name: "read",
        command: "",
        type: "file",
        tool: "read",
        input: { path: "notes.txt" },
      }],
      source: ".koyori-ide/workflows/read-notes.yml",
    };

    expect(fromBindingWorkflow(toBindingWorkflow(workflow))).toEqual(workflow);
  });

  it("rejects an unknown binding workflow step type", () => {
    const binding = toBindingWorkflow({
      name: "unknown-binding-type",
      steps: [{ name: "step", command: "echo" }],
      source: ".koyori-ide/workflows/unknown.yml",
    });
    const step = binding.steps?.[0];
    expect(step).toBeDefined();
    if (!step) throw new Error("test binding is missing its workflow step");
    Reflect.set(step, "type", "shell");

    expect(() => fromBindingWorkflow(binding)).toThrow(
      /Workflow\.steps\[0\]\.type.*supported workflow step type/,
    );
  });

  it("rejects an unknown frontend workflow step type", () => {
    const workflow: WorkflowDef = {
      name: "unknown-frontend-type",
      steps: [{ name: "step", command: "echo" }],
      source: ".koyori-ide/workflows/unknown.yml",
    };
    Reflect.set(workflow.steps[0], "type", "shell");

    expect(() => toBindingWorkflow(workflow)).toThrow(
      /Workflow\.steps\[0\]\.type.*supported workflow step type/,
    );
  });

  it("normalizes a nullable binding step slice to an empty array", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    const binding = toBindingWorkflow({
      name: "empty",
      steps: [],
      source: ".koyori-ide/workflows/empty.yml",
    });
    binding.steps = null as unknown as typeof binding.steps;

    expect(fromBindingWorkflow(binding).steps).toEqual([]);
    expect(warn).toHaveBeenCalledWith(
      expect.stringContaining("Workflow.steps must be an array of workflow steps"),
    );
  });
});

describe("extension manifest DTO conversion", () => {
  it("normalizes nullable contribution slices and nested maps", () => {
    const prototypeView = [{ id: "prototype.view", name: "Prototype View" }];
    const prototypeMenu = [{ command: "sample.prototype" }];
    const manifest = fromBindingExtensionManifest({
      name: "sample",
      publisher: "koyori-ide",
      version: "1.0.0",
      displayName: "Sample",
      description: "Sample extension",
      main: "./dist/extension.cjs",
      browser: "./dist/extension.js",
      koyoriIde: { permissions: ["fs.read", "network"] },
      engines: Object.fromEntries([
        ["__proto__", "^1.0.0"],
        ["vscode", "^1.90.0"],
      ]),
      activationEvents: null,
      contributes: { commands: [] },
      capabilities: {},
      parsedContributes: {
        languages: [
          {
            id: "sample",
            aliases: null,
            extensions: [".sample"],
          },
        ],
        commands: null,
        views: Object.fromEntries([
          ["__proto__", prototypeView],
          ["explorer", null],
        ]),
        menus: Object.fromEntries([
          ["__proto__", prototypeMenu],
          ["view/title", [{ command: "sample.open", when: "view == sample" }]],
        ]),
      },
    } as unknown as Parameters<typeof fromBindingExtensionManifest>[0]);

    expect(manifest.activationEvents).toEqual([]);
    expect(manifest.main).toBe("./dist/extension.cjs");
    expect(manifest.browser).toBe("./dist/extension.js");
    expect(manifest.koyoriIde?.permissions).toEqual(["fs.read", "network"]);
    expect(manifest.parsedContributes).toMatchObject({
      languages: [{ id: "sample", extensions: [".sample"] }],
      commands: undefined,
      views: { explorer: [] },
      menus: {
        "view/title": [{ command: "sample.open", when: "view == sample" }],
      },
    });
    expectPrototypeSafeRecord(manifest.engines, "^1.0.0");
    expectPrototypeSafeRecord(manifest.parsedContributes?.views, prototypeView);
    expectPrototypeSafeRecord(manifest.parsedContributes?.menus, prototypeMenu);
  });
});
