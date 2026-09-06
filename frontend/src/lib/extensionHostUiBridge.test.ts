import { afterEach, describe, expect, it, vi } from "vitest";
import { nextTick } from "vue";
import {
  showExtensionInputBox,
  showExtensionQuickPick,
} from "@/lib/extensionHostUiBridge";

function dialog(): HTMLElement {
  const dialogs = Array.from(document.querySelectorAll<HTMLElement>(".el-message-box"));
  const current = dialogs.at(-1);
  if (!current) throw new Error("Message-box dialog not found");
  return current;
}

function button(label: string): HTMLButtonElement {
  const match = Array.from(dialog().querySelectorAll<HTMLButtonElement>("button"))
    .find((candidate) => candidate.textContent?.trim() === label);
  if (!match) throw new Error("Message-box button not found: " + label);
  return match;
}

afterEach(async () => {
  await nextTick();
  await new Promise<void>((resolve) => setTimeout(resolve, 0));
  document.body.querySelectorAll(".el-overlay, .el-message-box__wrapper").forEach((node) => node.remove());
});

describe("extension host Element Plus dialogs", () => {
  it("renders a real input box and returns the typed value", async () => {
    const result = showExtensionInputBox({ prompt: "Column number", placeHolder: "1" });
    await vi.waitFor(() => {
      expect(dialog().querySelector(".el-message-box__input input")).not.toBeNull();
    });
    const input = dialog().querySelector<HTMLInputElement>(".el-message-box__input input")!;
    input.value = "2";
    input.dispatchEvent(new Event("input", { bubbles: true }));
    button("OK").click();
    await expect(result).resolves.toBe("2");
  });

  it("keeps an async validation error visible until the value is valid", async () => {
    const validateInput = vi.fn(async (value: string) => value === "ok" ? undefined : "Try again");
    const result = showExtensionInputBox({ prompt: "Value", validateInput });
    await vi.waitFor(() => expect(dialog().querySelector(".el-message-box__input input")).not.toBeNull());
    const input = dialog().querySelector<HTMLInputElement>(".el-message-box__input input")!;
    input.value = "bad";
    input.dispatchEvent(new Event("input", { bubbles: true }));
    button("OK").click();
    await vi.waitFor(() => expect(validateInput).toHaveBeenCalled());
    await vi.waitFor(() => {
      expect(dialog().querySelector(".el-message-box__errormsg")?.textContent).toContain("Try again");
    });
    input.value = "ok";
    input.dispatchEvent(new Event("input", { bubbles: true }));
    button("OK").click();
    await expect(result).resolves.toBe("ok");
    expect(validateInput).toHaveBeenCalledWith("bad");
    expect(validateInput).toHaveBeenCalledWith("ok");
  });

  it("returns undefined when input is cancelled", async () => {
    const result = showExtensionInputBox({ value: "default-is-not-a-result" });
    await vi.waitFor(() => expect(dialog().querySelector(".el-message-box__input input")).not.toBeNull());
    button("Cancel").click();
    await expect(result).resolves.toBeUndefined();
  });

  it("requires a real quick-pick selection and returns the chosen item", async () => {
    const result = showExtensionQuickPick(["first", "second"], { placeHolder: "Choose" });
    await vi.waitFor(() => expect(dialog().querySelector("select")).not.toBeNull());
    const select = dialog().querySelector<HTMLSelectElement>("select")!;
    expect(select.value).toBe("");
    select.value = "1";
    select.dispatchEvent(new Event("change", { bubbles: true }));
    button("Select").click();
    await expect(result).resolves.toBe("second");
  });

  it("returns undefined rather than the first item when quick pick is cancelled", async () => {
    const result = showExtensionQuickPick(["first", "second"]);
    await vi.waitFor(() => expect(dialog().querySelector("select")).not.toBeNull());
    button("Cancel").click();
    await expect(result).resolves.toBeUndefined();
  });
});
