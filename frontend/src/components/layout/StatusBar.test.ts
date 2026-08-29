import { defineComponent } from "vue";
import { mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  appState: {
    currentProject: "/work/app",
    branchName: "main",
    errors: 0,
    warnings: 0,
    cursorLine: 1,
    cursorColumn: 1,
    encoding: "UTF-8",
    languageMode: "Go",
    inlineCompletionEnabled: true,
    terminalVisible: false,
  },
  goTargetState: {
    targets: [
      { goos: "windows", goarch: "amd64" },
      { goos: "linux", goarch: "arm64" },
    ],
    host: { goos: "windows", goarch: "amd64" },
    current: { goos: "windows", goarch: "amd64" },
    overridden: false,
    loading: false,
  },
  refreshGoTarget: vi.fn(),
  selectGoTarget: vi.fn(),
  restoreHostGoTarget: vi.fn(),
  lspState: {
    statuses: {
      json: {
        language: "json",
        available: true,
        running: true,
        serverPath: "/bin/json-ls",
        version: "1.0.0",
        serverKind: "vscode-json-languageserver",
      },
    },
    busy: false,
    enabled: true,
  },
  restartLSPServer: vi.fn(),
  setLSPEnabled: vi.fn(),
}));

vi.mock("@/stores/app", () => ({ appState: mocks.appState, toggleTerminal: vi.fn() }));
vi.mock("@/stores/editor", () => ({ editorState: { openFiles: [] }, activeFile: { value: null } }));
vi.mock("@/stores/inlineCompletion", () => ({
  toggleInlineCompletion: vi.fn(),
  inlineCompletionUnavailable: { value: false },
}));
vi.mock("@/lib/connectivity", () => ({ connectivityState: { online: true } }));
vi.mock("@/stores/lsp", () => ({
  lspState: mocks.lspState,
  lspStatusLabel: { value: "LSP" },
  lspStatusDetail: { value: "" },
  restartLSPServer: mocks.restartLSPServer,
  setLSPEnabled: mocks.setLSPEnabled,
}));
vi.mock("@/stores/toolchain", () => ({
  runtimeVersions: { goVersion: "go1.24", nodeVersion: "", hasGoWork: false },
  refreshRuntimeVersions: vi.fn(),
}));
vi.mock("@/stores/goTarget", () => ({
  goTargetState: mocks.goTargetState,
  refreshGoTarget: mocks.refreshGoTarget,
  selectGoTarget: mocks.selectGoTarget,
  restoreHostGoTarget: mocks.restoreHostGoTarget,
}));
vi.mock("@/stores/workspaceModules", () => ({ workspaceModulesState: { activeRoot: "" } }));
vi.mock("@/lib/i18n", () => ({ useI18n: () => ({ t: (key: string) => key }) }));

import StatusBar from "./StatusBar.vue";

const DropdownStub = defineComponent({
  emits: ["command"],
  template: `
    <div>
      <slot />
      <button data-test="choose-target" @click="$emit('command', 'linux/arm64')" />
      <button data-test="restore-host" @click="$emit('command', '__host__')" />
      <button data-test="toggle-lsp" @click="$emit('command', 'toggle')" />
      <button data-test="restart-json" @click="$emit('command', 'restart:json')" />
    </div>
  `,
});

describe("StatusBar Go target", () => {
  beforeEach(() => vi.clearAllMocks());

  it("shows the current target and supports selection plus host restore", async () => {
    const wrapper = mount(StatusBar, {
      global: {
        stubs: {
          "el-icon": true,
          "el-dropdown": DropdownStub,
          "el-dropdown-menu": true,
          "el-dropdown-item": true,
        },
      },
    });

    expect(mocks.refreshGoTarget).toHaveBeenCalledWith("/work/app");
    expect(wrapper.text()).toContain("windows/amd64");

    await wrapper.get('[data-test="choose-target"]').trigger("click");
    expect(mocks.selectGoTarget).toHaveBeenCalledWith("/work/app", { goos: "linux", goarch: "arm64" });

    await wrapper.get('[data-test="restore-host"]').trigger("click");
    expect(mocks.restoreHostGoTarget).toHaveBeenCalledWith("/work/app");
  });

  it("supports disabling all language servers and restarting one server", async () => {
    const wrapper = mount(StatusBar, {
      global: {
        stubs: {
          "el-icon": true,
          "el-dropdown": DropdownStub,
          "el-dropdown-menu": true,
          "el-dropdown-item": true,
        },
      },
    });
    const lspMenu = wrapper.get('[data-test="lsp-menu"]');

    await lspMenu.get('[data-test="toggle-lsp"]').trigger("click");
    expect(mocks.setLSPEnabled).toHaveBeenCalledWith(false);

    await lspMenu.get('[data-test="restart-json"]').trigger("click");
    expect(mocks.restartLSPServer).toHaveBeenCalledWith("json");
  });
});
