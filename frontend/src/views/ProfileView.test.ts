import { mount } from "@vue/test-utils";
import { describe, expect, it, vi } from "vitest";

const state = vi.hoisted(() => ({
  cpuProfiling: false,
  activeProfile: "block",
  lastKind: "trace",
  loading: false,
}));

vi.mock("@/stores/pprof", () => ({
  pprofState: state,
  refreshProfilingStatus: vi.fn(),
}));

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({ t: (key: string) => key }),
}));

import ProfileView from "./ProfileView.vue";

describe("ProfileView advanced sessions", () => {
  it("shows advanced kinds in the full-width status bar", () => {
    const wrapper = mount(ProfileView, {
      global: { stubs: { ProfilePanel: true } },
    });

    expect(wrapper.find(".profile-view__badge--recording").text()).toContain("Block");
    expect(wrapper.find(".profile-view__badge--idle").exists()).toBe(false);
    expect(wrapper.find(".profile-view__kind").text()).toContain("Trace");
  });
});
