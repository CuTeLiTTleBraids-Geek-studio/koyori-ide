import { defineComponent, h } from "vue";
import { mount } from "@vue/test-utils";
import {
  afterEach,
  beforeEach,
  describe,
  expect,
  it,
  type Mock,
  vi,
} from "vitest";

const activationMocks = vi.hoisted(() => ({
  loadInstalledExtensionManifests: vi.fn(),
  activateEager: vi.fn(),
  setExtensionHostActiveEditorCallback: vi.fn(),
  setExtensionHostWorkspaceFoldersCallback: vi.fn(),
  setExtensionHostDecorationCallback: vi.fn(),
  setExtensionHostConfigurationCallback: vi.fn(),
  notifyExtensionHostConfigurationChange: vi.fn(),
  setExtensionHostSaveAllCallback: vi.fn(),
  setExtensionHostNotifyCallback: vi.fn(),
  setExtensionHostInputCallback: vi.fn(),
  setExtensionHostQuickPickCallback: vi.fn(),
  setExtensionHostStatusBarCallback: vi.fn(),
  setExtensionHostOutputCallback: vi.fn(),
  setExtensionHostProgressCallback: vi.fn(),
}));

const recoveryMocks = vi.hoisted(() => ({
  scanRecoverable: vi.fn(),
}));

vi.mock("vue-router", () => ({
  useRoute: () => ({ meta: {}, path: "/" }),
}));
vi.mock("@/components/layout/MainLayout.vue", () => ({
  default: { template: "<main><slot /></main>" },
}));
vi.mock("@/lib/notifications", () => ({ notifyError: vi.fn() }));
vi.mock("@/stores/output", () => ({ pushOutput: vi.fn() }));
vi.mock("@/stores/editor", () => ({
  activeFile: { value: undefined },
  saveAllFilesDetailed: vi.fn().mockResolvedValue({ savedCount: 0, failedPaths: [] }),
}));
vi.mock("@/stores/app", async () => {
  const { reactive } = await import("vue");
  return {
    settingsStore: reactive({}),
    appState: reactive<{ currentProject: string | null }>({ currentProject: "/workspace" }),
  };
});
vi.mock("@/lib/vscodeExtensionActivation", () => activationMocks);
vi.mock("@/stores/recovery", () => ({
  recoveryState: { visible: false },
  scanRecoverable: recoveryMocks.scanRecoverable,
}));
vi.mock("@/components/modals/RecoveryDialog.vue", () => ({
  default: { template: '<div class="recovery-dialog-stub" />' },
}));

import App from "./App.vue";

type FrontendRuntimeGlobal = typeof globalThis & {
  __koyoriIdeFrontendRuntimeOwner?: symbol | null;
  __koyoriIdeRuntimeRole?: "main" | "ai" | "settings" | "e2e" | "minimal";
  __koyoriIdeAcquireFrontendRuntime?: (owner: symbol) => () => void;
};

const frontendRuntimeGlobal = globalThis as FrontendRuntimeGlobal;
const EmptyRoute = defineComponent({ render: () => h("div") });
const RouterViewStub = defineComponent({
  setup(_props, { slots }) {
    return () => slots.default?.({ Component: EmptyRoute });
  },
});

function mountApp() {
  return mount(App, {
    global: {
      stubs: {
        RouterView: RouterViewStub,
        Transition: false,
      },
    },
  });
}

describe("App runtime lifecycle", () => {
  let owner: symbol;
  let releaseRuntime: Mock<() => void>;

  beforeEach(async () => {
    vi.clearAllMocks();
    const { appState } = await import("@/stores/app");
    appState.currentProject = "/workspace";
    activationMocks.loadInstalledExtensionManifests.mockResolvedValue(undefined);
    activationMocks.activateEager.mockResolvedValue([]);
    recoveryMocks.scanRecoverable.mockResolvedValue({
      workspaceRoot: "/workspace", files: [], corrupt: [], totalBytes: 0,
    });
    frontendRuntimeGlobal.__koyoriIdeRuntimeRole = "main";
    owner = Symbol("app-runtime-owner");
    releaseRuntime = vi.fn<() => void>();
    frontendRuntimeGlobal.__koyoriIdeFrontendRuntimeOwner = owner;
    frontendRuntimeGlobal.__koyoriIdeAcquireFrontendRuntime = vi.fn(
      (requestedOwner: symbol): (() => void) =>
        requestedOwner === owner
          ? () => {
              releaseRuntime();
            }
          : () => undefined,
    );
  });

  afterEach(() => {
    delete frontendRuntimeGlobal.__koyoriIdeFrontendRuntimeOwner;
    delete frontendRuntimeGlobal.__koyoriIdeRuntimeRole;
    delete frontendRuntimeGlobal.__koyoriIdeAcquireFrontendRuntime;
  });

  it("releases the owner-bound lease captured during setup", () => {
    const wrapper = mountApp();
    const successorAcquire = vi.fn(() => () => undefined);
    frontendRuntimeGlobal.__koyoriIdeFrontendRuntimeOwner = Symbol("successor");
    frontendRuntimeGlobal.__koyoriIdeAcquireFrontendRuntime = successorAcquire;

    wrapper.unmount();

    expect(releaseRuntime).toHaveBeenCalledOnce();
    expect(successorAcquire).not.toHaveBeenCalled();
  });

  it("does not continue eager activation after unmount", async () => {
    let finishManifestLoad: () => void = () => undefined;
    activationMocks.loadInstalledExtensionManifests.mockReturnValueOnce(
      new Promise<void>((resolve) => {
        finishManifestLoad = resolve;
      }),
    );
    const wrapper = mountApp();
    await vi.waitFor(() => {
      expect(activationMocks.loadInstalledExtensionManifests).toHaveBeenCalledOnce();
    });

    wrapper.unmount();
    finishManifestLoad();
    await Promise.resolve();
    await Promise.resolve();

    expect(releaseRuntime).toHaveBeenCalledOnce();
    expect(activationMocks.activateEager).not.toHaveBeenCalled();
  });

  it("scans recovery records when the first workspace is ready", async () => {
    const { appState } = await import("@/stores/app");
    appState.currentProject = null;
    const wrapper = mountApp();

    await Promise.resolve();
    expect(recoveryMocks.scanRecoverable).not.toHaveBeenCalled();

    appState.currentProject = "/workspace";
    await vi.waitFor(() => expect(recoveryMocks.scanRecoverable).toHaveBeenCalledOnce());

    expect(wrapper.find(".recovery-dialog-stub").exists()).toBe(true);
    wrapper.unmount();
  });

  it("scans recovery records again after each workspace switch", async () => {
    const { appState } = await import("@/stores/app");
    appState.currentProject = null;
    const wrapper = mountApp();

    appState.currentProject = "/workspace-a";
    await vi.waitFor(() => {
      expect(recoveryMocks.scanRecoverable).toHaveBeenCalledTimes(1);
    });

    appState.currentProject = "/workspace-b";
    await vi.waitFor(() => {
      expect(recoveryMocks.scanRecoverable).toHaveBeenCalledTimes(2);
    });

    wrapper.unmount();
  });
});
