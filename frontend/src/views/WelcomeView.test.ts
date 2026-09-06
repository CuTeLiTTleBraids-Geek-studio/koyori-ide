import { describe, expect, it, vi } from "vitest";
import { mount } from "@vue/test-utils";

vi.mock("vue-router", () => ({
  useRouter: () => ({ push: vi.fn() }),
}));

vi.mock("@/api/services", () => ({
  fileService: { pickDirectory: vi.fn() },
}));

vi.mock("@/stores/app", () => ({
  openProject: vi.fn(),
}));

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({ t: (key: string) => key }),
}));

vi.mock("@/lib/errors", () => ({
  isCancellationError: () => false,
}));

const { alertMock } = vi.hoisted(() => ({
  alertMock: vi.fn().mockResolvedValue(undefined),
}));

vi.mock("element-plus", () => ({
  ElMessageBox: { alert: alertMock },
}));

import WelcomeView from "./WelcomeView.vue";

describe("WelcomeView version SSOT (P13-G01 / UI-1)", () => {
  it("renders hero and footer from the same __APP_VERSION__ source", () => {
    const wrapper = mount(WelcomeView);
    const hero = wrapper.get('[data-testid="welcome-version"]').text();
    const footer = wrapper.get('[data-testid="welcome-footer-version"]').text();
    expect(hero).toBe("v" + __APP_VERSION__);
    expect(footer).toBe("v" + __APP_VERSION__);
    expect(hero).not.toBe("v0.1.0");
    expect(footer).not.toBe("v0.1.0");
    expect(wrapper.html()).not.toContain("v0.1.0");
  });

  it("does not open the Wails site as project docs (P13-G02 / UI-2)", async () => {
    const openSpy = vi.spyOn(window, "open").mockImplementation(() => null);
    const wrapper = mount(WelcomeView);
    const docsButton = wrapper
      .findAll("button")
      .find((btn) => btn.attributes("aria-label") === "welcome.documentationAria");
    expect(docsButton).toBeTruthy();
    await docsButton!.trigger("click");
    expect(openSpy).not.toHaveBeenCalled();
    expect(alertMock).toHaveBeenCalled();
    const [message] = alertMock.mock.calls[0];
    expect(message).toBe("welcome.docsLocalPath");
    expect(String(message)).not.toMatch(/v3\.wails\.io/);
    openSpy.mockRestore();
  });
});
