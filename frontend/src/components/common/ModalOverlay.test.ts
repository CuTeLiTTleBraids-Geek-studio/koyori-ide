import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, describe, expect, it } from "vitest";
import ModalOverlay from "./ModalOverlay.vue";
import modalOverlaySource from "./ModalOverlay.vue?raw";

afterEach(() => {
  document.body.innerHTML = "";
});

describe("ModalOverlay", () => {
  it("teleports an accessible modal into a dedicated viewport backdrop", async () => {
    const wrapper = mount(ModalOverlay, {
      attachTo: document.body,
      props: { visible: true, title: "Recovery", maxWidth: "720px" },
      slots: { default: '<button data-test="action">Restore</button>' },
    });
    await flushPromises();

    const backdrop = document.body.querySelector<HTMLElement>(".dialog-backdrop-button");
    const dialog = backdrop?.querySelector<HTMLElement>(".modal-overlay");
    expect(backdrop).not.toBeNull();
    expect(dialog?.getAttribute("role")).toBe("dialog");
    expect(dialog?.getAttribute("aria-modal")).toBe("true");
    expect(dialog?.getAttribute("aria-label")).toBe("Recovery");
    expect(dialog?.style.maxWidth).toBe("720px");

    wrapper.unmount();
  });

  it("keeps the teleported backdrop fixed to the viewport", () => {
    const backdropRule = modalOverlaySource.match(
      /\.dialog-backdrop-button\s*\{([\s\S]*?)\}/,
    )?.[1] ?? "";
    expect(backdropRule).toContain("position: fixed");
    expect(backdropRule).toContain("inset: 0");
    expect(backdropRule).toContain("overflow: auto");
    expect(backdropRule).toContain("align-items: center");
    expect(backdropRule).toContain("justify-content: center");
  });
});
