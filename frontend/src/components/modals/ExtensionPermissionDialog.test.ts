/**
 * ExtensionPermissionDialog tests (G-VSC-03 / G-SEC-12).
 *
 * Verifies the permission approval dialog:
 *   - Lists all requested permissions with descriptions.
 *   - Shows a prominent warning for Restricted extensions.
 *   - Requires explicit confirmation (checkbox) for Restricted extensions.
 *   - Enables directly for Reviewed extensions (no checkbox).
 *   - Emits "approve" with the extension ID on confirm.
 *   - Emits "close" on cancel / overlay click / Escape.
 *   - Disables the Enable button for unverified extensions.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { mount } from "@vue/test-utils";
import { nextTick } from "vue";
import ExtensionPermissionDialog from "./ExtensionPermissionDialog.vue";
import type { ExtensionSecurityInfo } from "@/stores/extensionSecurity";
import { appState } from "@/stores/app";

// LOW-05: i18n 行为验证 — 保存/恢复 appState.language 以免影响其他测试。
let originalLang: string;

// Mock the store helpers so the dialog test is isolated from the store's
// appState dependency. The real functions are pure; we provide minimal
// implementations that return predictable strings.
vi.mock("@/stores/extensionSecurity", () => ({
  permissionDescription: (perm: string) => `Description for ${perm}`,
  permissionRisk: (perm: string) => {
    if (perm === "network" || perm === "shell.execute") return "high";
    if (perm === "fs.write") return "medium";
    return "low";
  },
}));

function makeInfo(overrides?: Partial<ExtensionSecurityInfo>): ExtensionSecurityInfo {
  return {
    extensionId: "pub.test-ext",
    level: "reviewed",
    permissions: ["fs.read", "fs.write"],
    sha256: "abc123",
    verified: true,
    enabled: false,
    blacklisted: false,
    pendingReview: true,
    ...overrides,
  };
}

describe("ExtensionPermissionDialog (G-VSC-03 / G-SEC-12)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    // LOW-05: 固定英文 locale 作为默认，便于断言。
    originalLang = appState.language;
    appState.language = "en";
  });

  afterEach(() => {
    // LOW-05: 恢复 language，避免污染其他测试套件。
    appState.language = originalLang;
  });

  it("does not render when visible is false", () => {
    const wrapper = mount(ExtensionPermissionDialog, {
      props: { visible: false, info: makeInfo() },
    });
    expect(wrapper.find(".epd").exists()).toBe(false);
  });

  it("lists all requested permissions with descriptions", () => {
    const info = makeInfo({
      permissions: ["fs.read", "fs.write", "network"],
    });
    const wrapper = mount(ExtensionPermissionDialog, {
      props: { visible: true, info },
    });
    const perms = wrapper.findAll(".epd__perm-id");
    expect(perms).toHaveLength(3);
    const labels = perms.map((p) => p.text());
    expect(labels).toContain("fs.read");
    expect(labels).toContain("fs.write");
    expect(labels).toContain("network");
    // Descriptions are rendered.
    const descs = wrapper.findAll(".epd__perm-desc");
    expect(descs.some((d) => d.text().includes("Description for network"))).toBe(true);
  });

  it("shows warning banner for restricted extensions", () => {
    const info = makeInfo({
      level: "restricted",
      permissions: ["network", "shell.execute"],
    });
    const wrapper = mount(ExtensionPermissionDialog, {
      props: { visible: true, info },
    });
    expect(wrapper.find(".epd__warning").exists()).toBe(true);
    expect(wrapper.find(".epd__confirm").exists()).toBe(true);
  });

  it("does not show warning banner for reviewed extensions", () => {
    const info = makeInfo({ level: "reviewed" });
    const wrapper = mount(ExtensionPermissionDialog, {
      props: { visible: true, info },
    });
    expect(wrapper.find(".epd__warning").exists()).toBe(false);
    expect(wrapper.find(".epd__confirm").exists()).toBe(false);
  });

  it("enables directly for reviewed extensions (no checkbox required)", async () => {
    const info = makeInfo({ level: "reviewed", verified: true });
    const wrapper = mount(ExtensionPermissionDialog, {
      props: { visible: true, info },
    });
    const enableBtn = wrapper.find(".epd__btn--primary");
    expect(enableBtn.attributes("disabled")).toBeUndefined();
    await enableBtn.trigger("click");
    expect(wrapper.emitted("approve")).toBeTruthy();
    expect(wrapper.emitted("approve")![0]).toEqual(["pub.test-ext"]);
  });

  it("requires confirmation checkbox for restricted extensions", async () => {
    const info = makeInfo({
      level: "restricted",
      permissions: ["network"],
      verified: true,
    });
    const wrapper = mount(ExtensionPermissionDialog, {
      props: { visible: true, info },
    });
    const enableBtn = wrapper.find(".epd__btn--primary");
    // Enable is disabled until the checkbox is checked.
    expect(enableBtn.attributes("disabled")).toBeDefined();
    // Check the confirmation checkbox.
    const checkbox = wrapper.find(".epd__confirm input");
    await checkbox.setValue(true);
    await nextTick();
    expect(enableBtn.attributes("disabled")).toBeUndefined();
    await enableBtn.trigger("click");
    expect(wrapper.emitted("approve")).toBeTruthy();
    expect(wrapper.emitted("approve")![0]).toEqual(["pub.test-ext"]);
  });

  it("disables Enable button for unverified extensions", () => {
    const info = makeInfo({ level: "trusted", verified: false });
    const wrapper = mount(ExtensionPermissionDialog, {
      props: { visible: true, info },
    });
    const enableBtn = wrapper.find(".epd__btn--primary");
    expect(enableBtn.attributes("disabled")).toBeDefined();
    // Shows the unverified warning.
    expect(wrapper.find(".epd__unverified").exists()).toBe(true);
  });

  it("emits close on Cancel button", async () => {
    const wrapper = mount(ExtensionPermissionDialog, {
      props: { visible: true, info: makeInfo() },
    });
    const cancelBtn = wrapper.find(".epd__btn:not(.epd__btn--primary)");
    await cancelBtn.trigger("click");
    expect(wrapper.emitted("close")).toBeTruthy();
  });

  it("emits close on overlay click", async () => {
    const wrapper = mount(ExtensionPermissionDialog, {
      props: { visible: true, info: makeInfo() },
    });
    await wrapper.find(".dialog-backdrop-button").trigger("click");
    expect(wrapper.emitted("close")).toBeTruthy();
  });

  it("keeps the backdrop button separate from the dialog semantics", () => {
    const wrapper = mount(ExtensionPermissionDialog, {
      props: { visible: true, info: makeInfo() },
    });
    const overlay = wrapper.get(".epd-overlay");
    const backdrop = wrapper.get(".dialog-backdrop-button");
    const dialog = wrapper.get('[role="dialog"]');
    expect(overlay.attributes("role")).toBeUndefined();
    expect(backdrop.element.tagName).toBe("BUTTON");
    expect(backdrop.attributes("tabindex")).toBe("-1");
    expect(backdrop.attributes("aria-label")).toBe("Close dialog");
    expect(backdrop.element.contains(dialog.element)).toBe(false);
  });

  it("focuses the first action and restores the opener when hidden", async () => {
    const opener = document.createElement("button");
    document.body.appendChild(opener);
    opener.focus();
    const wrapper = mount(ExtensionPermissionDialog, {
      attachTo: document.body,
      props: { visible: true, info: makeInfo() },
    });
    await nextTick();
    await nextTick();

    const cancel = wrapper.get(".epd__btn:not(.epd__btn--primary)").element;
    expect(document.activeElement).toBe(cancel);

    await wrapper.setProps({ visible: false });
    await nextTick();
    expect(document.activeElement).toBe(opener);
    wrapper.unmount();
    opener.remove();
  });

  it("emits close on Escape key", async () => {
    const wrapper = mount(ExtensionPermissionDialog, {
      props: { visible: true, info: makeInfo() },
    });
    await wrapper.find(".epd").trigger("keydown", { key: "Escape" });
    expect(wrapper.emitted("close")).toBeTruthy();
  });

  it("displays the extension ID and security level", () => {
    const info = makeInfo({ extensionId: "acme.super-tool", level: "restricted" });
    const wrapper = mount(ExtensionPermissionDialog, {
      props: { visible: true, info },
    });
    expect(wrapper.find(".epd__ext-name").text()).toContain("acme.super-tool");
    expect(wrapper.find(".epd__level--restricted").exists()).toBe(true);
  });

  it("sorts permissions by risk (high first)", () => {
    const info = makeInfo({
      permissions: ["fs.read", "network", "fs.write"],
    });
    const wrapper = mount(ExtensionPermissionDialog, {
      props: { visible: true, info },
    });
    const perms = wrapper.findAll(".epd__perm-id");
    // network (high) should come before fs.write (medium) before fs.read (low)
    expect(perms[0].text()).toBe("network");
    expect(perms[1].text()).toBe("fs.write");
    expect(perms[2].text()).toBe("fs.read");
  });

  it("shows no-permissions message when list is empty", () => {
    const info = makeInfo({ permissions: [] });
    const wrapper = mount(ExtensionPermissionDialog, {
      props: { visible: true, info },
    });
    expect(wrapper.find(".epd__perm--none").exists()).toBe(true);
  });

  // --- LOW-05: i18n 行为验证 ---

  it("renders English text when locale is en", () => {
    appState.language = "en";
    const wrapper = mount(ExtensionPermissionDialog, {
      props: { visible: true, info: makeInfo({ level: "reviewed" }) },
    });
    // 标题与按钮使用英文翻译。
    expect(wrapper.find(".epd__title").text()).toBe("Enable extension?");
    const buttons = wrapper.findAll(".epd__btn");
    const texts = buttons.map((b) => b.text());
    expect(texts).toContain("Cancel");
    expect(texts).toContain("Enable");
  });

  it("switches to Chinese text when locale is zh", async () => {
    appState.language = "zh";
    const wrapper = mount(ExtensionPermissionDialog, {
      props: { visible: true, info: makeInfo({ level: "reviewed" }) },
    });
    await nextTick();
    // 标题与按钮切换为中文翻译。
    expect(wrapper.find(".epd__title").text()).toBe("启用扩展？");
    const buttons = wrapper.findAll(".epd__btn");
    const texts = buttons.map((b) => b.text());
    expect(texts).toContain("取消");
    expect(texts).toContain("启用");
  });

  it("switches to Japanese text when locale is ja", async () => {
    appState.language = "ja";
    const wrapper = mount(ExtensionPermissionDialog, {
      props: { visible: true, info: makeInfo({ level: "reviewed" }) },
    });
    await nextTick();
    // 标题非空且不为英文（验证走了 ja 字典而非回退到 en）。
    const title = wrapper.find(".epd__title").text();
    expect(title).not.toBe("");
    expect(title).not.toBe("Enable extension?");
  });

  it("updates dynamically when language changes after mount", async () => {
    appState.language = "en";
    const wrapper = mount(ExtensionPermissionDialog, {
      props: { visible: true, info: makeInfo({ level: "reviewed" }) },
    });
    expect(wrapper.find(".epd__title").text()).toBe("Enable extension?");
    // 切换语言后响应式重渲染。
    appState.language = "zh";
    await nextTick();
    expect(wrapper.find(".epd__title").text()).toBe("启用扩展？");
  });

  it("shows restricted enable label for restricted extensions", () => {
    appState.language = "en";
    const info = makeInfo({ level: "restricted", verified: true });
    const wrapper = mount(ExtensionPermissionDialog, {
      props: { visible: true, info },
    });
    const primary = wrapper.find(".epd__btn--primary");
    expect(primary.text()).toBe("Enable restricted extension");
  });
});
