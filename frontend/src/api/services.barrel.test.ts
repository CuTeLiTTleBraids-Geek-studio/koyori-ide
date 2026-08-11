import { describe, expect, it, vi } from "vitest";

const domains = vi.hoisted(() => ({
  pullRequests: { pullRequestService: {} },
  boundary: { unwrapNullable: vi.fn(), requireNonNull: vi.fn() },
  workspace: {
    fileService: {}, computerUseService: {}, projectService: {}, settingsService: {},
    windowService: {}, terminalService: {}, fromBindingSettings: vi.fn(),
    toBindingSettings: vi.fn(),
  },
  aiGitSearch: { aiService: {}, gitService: {}, searchService: {} },
  automation: {
    conversationService: {}, taskService: {}, workflowService: {}, agentService: {},
    rulesService: {}, logLevelService: {}, pluginService: {}, profileService: {},
    layoutService: {}, toolchainService: {}, fromBindingWorkflow: vi.fn(),
    toBindingWorkflow: vi.fn(),
  },
  lsp: {
    lspService: {}, fromBindingLSPCompletionItem: vi.fn(),
    toBindingLSPCompletionItem: vi.fn(), fromBindingSemanticToken: vi.fn(),
    fromBindingInlayHint: vi.fn(),
  },
  debugQuality: { debugService: {}, eslintService: {}, coverageService: {} },
  extensions: {
    marketplaceService: {}, symbolIndexService: {}, fromBindingExtensionManifest: vi.fn(),
  },
  platform: {
    databaseService: {}, pprofService: {}, updateService: {}, crashService: {},
    recoveryService: {}, remoteService: {},
  },
}));

vi.mock("./pullRequests", () => domains.pullRequests);
vi.mock("./boundary", () => domains.boundary);
vi.mock("./workspace", () => domains.workspace);
vi.mock("./aiGitSearch", () => domains.aiGitSearch);
vi.mock("./automation", () => domains.automation);
vi.mock("./lsp", () => domains.lsp);
vi.mock("./debugQuality", () => domains.debugQuality);
vi.mock("./extensions", () => domains.extensions);
vi.mock("./platform", () => domains.platform);

import * as services from "./services";

describe("services compatibility barrel", () => {
  it("preserves every runtime export from the original API module", () => {
    const expected = Object.assign({}, ...Object.values(domains));

    expect(Object.keys(services).sort()).toEqual(Object.keys(expected).sort());
    for (const [name, value] of Object.entries(expected)) {
      expect(services[name as keyof typeof services]).toBe(value);
    }
  });
});
