import { beforeEach, describe, expect, it, vi } from "vitest";
import { mount } from "@vue/test-utils";

const mocks = vi.hoisted(() => ({
  run: vi.fn(),
  cancelRun: vi.fn(),
  setWorkspace: vi.fn(),
  updateProfile: vi.fn(),
  muteSource: vi.fn(),
  loadFixes: vi.fn(),
  previewFix: vi.fn(),
  applyFix: vi.fn(),
  cancelFix: vi.fn(),
  openFile: vi.fn(),
  state: {
    workspaceRoot: "/repo",
    profile: {
      id: "project",
      name: "Project",
      enabled: true,
      severityThreshold: 3,
      includeGlobs: ["src/**/*.ts"],
      excludeGlobs: ["**/*.test.ts"],
      sourceRules: {},
    },
    findings: [] as Array<Record<string, unknown>>,
    quickFixes: [] as Array<Record<string, unknown>>,
    quickFixPreview: null as Record<string, unknown> | null,
    loading: false,
    quickFixLoading: false,
    applying: false,
    error: null as string | null,
    truncated: false,
    scannedFiles: 0,
    skippedFiles: 0,
  },
}));

vi.mock("@/stores/inspections", () => ({
  inspectionState: mocks.state,
  runInspections: mocks.run,
  cancelInspectionRun: mocks.cancelRun,
  setInspectionWorkspace: mocks.setWorkspace,
  updateInspectionProfile: mocks.updateProfile,
  setInspectionSourceEnabled: mocks.muteSource,
  loadInspectionQuickFixes: mocks.loadFixes,
  previewInspectionQuickFix: mocks.previewFix,
  applyInspectionQuickFix: mocks.applyFix,
  cancelInspectionQuickFix: mocks.cancelFix,
}));

vi.mock("@/stores/editor", () => ({ openFileFromPath: mocks.openFile }));
vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => ({
      "inspections.run": "Run inspections",
      "inspections.quickFix": "Quick fix",
      "inspections.applyFix": "Apply quick fix",
      "search.cancelPreview": "Cancel",
    }[key] ?? (
      key === "inspections.muteSource"
        ? `Mute ${params?.source ?? ""}`
        : key === "inspections.enableSource" ? `Enable ${params?.source ?? ""}` : key
    )),
  }),
}));

import InspectionToolWindow from "./InspectionToolWindow.vue";
import { appState } from "@/stores/app";

function finding() {
  return {
    id: "finding-1",
    path: "src/a.ts",
    filePath: "/repo/src/a.ts",
    language: "typescript",
    server: "typescript",
    line: 4,
    column: 2,
    endLine: 4,
    endColumn: 8,
    severity: 1,
    message: "Type mismatch",
    source: "typescript",
    ruleId: "typescript",
    documentContent: "const value = 1;\n",
  };
}

describe("InspectionToolWindow", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.state.findings = [];
    mocks.state.quickFixes = [];
    mocks.state.quickFixPreview = null;
    mocks.state.loading = false;
    mocks.state.error = null;
    mocks.state.profile.enabled = true;
    mocks.state.profile.sourceRules = {};
    mocks.run.mockResolvedValue(undefined);
    mocks.openFile.mockResolvedValue(undefined);
    mocks.applyFix.mockResolvedValue(true);
  });

  function mountWindow() {
    return mount(InspectionToolWindow, {
      props: { repoPath: "/repo" },
      global: {
        stubs: { "el-icon": true },
      },
    });
  }

  it("loads the project profile and starts a batch run", async () => {
    const wrapper = mountWindow();
    expect(mocks.setWorkspace).toHaveBeenCalledWith("/repo");

    const run = wrapper.get('button[title="Run inspections"]');
    await run.trigger("click");
    expect(mocks.run).toHaveBeenCalledWith("/repo");
    wrapper.unmount();
  });

  it("renders findings, jumps to their range and can mute the source", async () => {
    mocks.state.findings = [finding()];
    const wrapper = mountWindow();

    expect(wrapper.text()).toContain("Type mismatch");
    await wrapper.get(".inspection-tool-window__target").trigger("click");
    expect(mocks.openFile).toHaveBeenCalledWith("/repo/src/a.ts");
    expect(appState.cursorLine).toBe(5);
    expect(appState.cursorColumn).toBe(3);

    const mute = wrapper.get('button[title="Mute typescript"]');
    expect(mute.attributes("aria-label")).toBe("Mute typescript");
    await mute.trigger("click");
    expect(mocks.muteSource).toHaveBeenCalledWith("typescript", false);
    wrapper.unmount();
  });

  it("loads quick fixes and previews a selected action", async () => {
    mocks.state.findings = [finding()];
    mocks.state.quickFixes = [{ findingId: "finding-1", title: "Fix type", kind: "quickfix" }];
    const wrapper = mountWindow();

    await wrapper.get('button[title="Quick fix"]').trigger("click");
    expect(mocks.loadFixes).toHaveBeenCalledWith("finding-1");
    await wrapper.get(".inspection-tool-window__actions button").trigger("click");
    expect(mocks.previewFix).toHaveBeenCalledWith("finding-1", 0);
    wrapper.unmount();
  });

  it("allows a muted inspection source to be enabled again", async () => {
    mocks.state.profile.sourceRules = { eslint: { enabled: false } };
    const wrapper = mountWindow();

    await wrapper.get('button[title="Enable eslint"]').trigger("click");
    expect(mocks.muteSource).toHaveBeenCalledWith("eslint", true);
    wrapper.unmount();
  });

  it("shows a before/after preview and applies it", async () => {
    mocks.state.quickFixPreview = {
      findingId: "finding-1",
      title: "Fix type",
      filePath: "/repo/src/a.ts",
      originalContent: "const value = 1;",
      modifiedContent: "const value: number = 1;",
    };
    const wrapper = mountWindow();

    expect(wrapper.text()).toContain("const value: number = 1;");
    const apply = wrapper.get('button[title="Apply quick fix"]');
    expect(apply.attributes("aria-label")).toBe("Apply quick fix");
    await apply.trigger("click");
    expect(mocks.applyFix).toHaveBeenCalledTimes(1);
    wrapper.unmount();
  });
});
