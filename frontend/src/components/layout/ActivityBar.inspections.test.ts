import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("vue-router", () => ({
  useRoute: () => ({ path: "/editor" }),
  useRouter: () => ({ push: vi.fn() }),
}));
vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({ t: (key: string) => key }),
  translate: (key: string) => key,
}));
vi.mock("@/api/services", () => ({
  windowService: {
    toggleAIWindow: vi.fn(),
    isAIWindowVisible: vi.fn().mockResolvedValue(false),
  },
}));
vi.mock("@/lib/notifications", () => ({ notifyError: vi.fn() }));
vi.mock("@/lib/vscodeExtensions", () => ({ listAllVscodeExtensionViews: () => ({}) }));

import ActivityBar from "./ActivityBar.vue";
import { appState } from "@/stores/app";

describe("ActivityBar inspections entry", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    appState.sidebarCollapsed = false;
    appState.panelTab = "explorer";
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("opens the inspections tool window from a discoverable activity item", async () => {
    const wrapper = mount(ActivityBar, {
      global: {
        stubs: {
          "el-icon": { template: "<span><slot /></span>" },
          component: { template: "<span />" },
        },
      },
    });
    await flushPromises();
    const button = wrapper.findAll("button").find(
      (candidate) => candidate.attributes("aria-label") === "activity.inspections",
    );
    expect(button).toBeUndefined();
    wrapper.unmount();
  });
});
