import { flushPromises, mount } from "@vue/test-utils";
import { describe, expect, it } from "vitest";
import FocusTrapDialog from "./FocusTrapDialog.vue";

describe("FocusTrapDialog", () => {
  it("focuses the first control, traps Tab, closes on Escape, and restores focus", async () => {
    const opener = document.createElement("button");
    document.body.appendChild(opener);
    opener.focus();

    const wrapper = mount(FocusTrapDialog, {
      attachTo: document.body,
      slots: {
        default: '<button data-test="first" autofocus>First</button><button data-test="last">Last</button>',
      },
    });
    await flushPromises();

    const first = wrapper.get('[data-test="first"]').element as HTMLButtonElement;
    const last = wrapper.get('[data-test="last"]').element as HTMLButtonElement;
    expect(document.activeElement).toBe(first);

    last.focus();
    await wrapper.trigger("keydown", { key: "Tab" });
    expect(document.activeElement).toBe(first);

    first.focus();
    await wrapper.trigger("keydown", { key: "Tab", shiftKey: true });
    expect(document.activeElement).toBe(last);

    opener.focus();
    await wrapper.trigger("keydown", { key: "Tab" });
    expect(document.activeElement).toBe(first);

    await wrapper.trigger("keydown", { key: "Escape" });
    expect(wrapper.emitted("close")).toHaveLength(1);

    wrapper.unmount();
    expect(document.activeElement).toBe(opener);
    opener.remove();
  });

  it("recaptures Tab dispatched by an element outside the dialog", async () => {
    const outside = document.createElement("button");
    document.body.appendChild(outside);
    const wrapper = mount(FocusTrapDialog, {
      attachTo: document.body,
      slots: {
        default: '<button data-test="inside">Inside</button>',
      },
    });
    await flushPromises();

    outside.focus();
    const event = new KeyboardEvent("keydown", {
      key: "Tab",
      bubbles: true,
      cancelable: true,
    });
    outside.dispatchEvent(event);

    expect(event.defaultPrevented).toBe(true);
    expect(document.activeElement).toBe(wrapper.get('[data-test="inside"]').element);
    wrapper.unmount();
    outside.remove();
  });

  it("only lets the topmost dialog handle keys and restores the dialog below", async () => {
    const opener = document.createElement("button");
    document.body.appendChild(opener);
    opener.focus();

    const outer = mount(FocusTrapDialog, {
      attachTo: document.body,
      slots: { default: '<button data-test="outer">Outer</button>' },
    });
    await flushPromises();
    const outerButton = outer.get('[data-test="outer"]').element as HTMLButtonElement;

    const inner = mount(FocusTrapDialog, {
      attachTo: document.body,
      props: { dialogRole: "alertdialog" },
      slots: { default: '<button data-test="inner">Inner</button>' },
    });
    await flushPromises();
    expect(inner.attributes("role")).toBe("alertdialog");

    outerButton.focus();
    outerButton.dispatchEvent(new KeyboardEvent("keydown", {
      key: "Escape",
      bubbles: true,
      cancelable: true,
    }));
    expect(inner.emitted("close")).toHaveLength(1);
    expect(outer.emitted("close")).toBeUndefined();

    inner.unmount();
    expect(document.activeElement).toBe(outerButton);
    outer.unmount();
    expect(document.activeElement).toBe(opener);
    opener.remove();
  });
});
