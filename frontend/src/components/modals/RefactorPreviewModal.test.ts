import { mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  apply: vi.fn(),
  cancel: vi.fn(),
  state: {
    previewVisible: true,
    applying: false,
    error: "",
    selectedAction: {
      title: "Extract function",
      preview: {
        files: [
          {
            filePath: "/workspace/main.go",
            baselineHash: "abc",
            originalContent: "func main() {}",
            modifiedContent: "func extracted() {}",
          },
        ],
      },
    },
  },
}));

vi.mock("@/stores/refactor", () => ({
  refactorState: mocks.state,
  applySelectedRefactor: mocks.apply,
  cancelRefactorPreview: mocks.cancel,
}));

import RefactorPreviewModal from "./RefactorPreviewModal.vue";
import { appState } from "@/stores/app";

describe("RefactorPreviewModal", () => {
  beforeEach(() => {
    mocks.apply.mockReset();
    mocks.cancel.mockReset();
    appState.language = "en";
  });

  it("shows every changed file and delegates apply/cancel", async () => {
    const wrapper = mount(RefactorPreviewModal);
    expect(wrapper.text()).toContain("Extract function");
    expect(wrapper.text()).toContain("/workspace/main.go");
    expect(wrapper.text()).toContain("func main() {}");
    expect(wrapper.text()).toContain("func extracted() {}");

    await wrapper.get('[data-test="refactor-apply"]').trigger("click");
    await wrapper.get('[data-test="refactor-cancel"]').trigger("click");
    expect(mocks.apply).toHaveBeenCalledTimes(1);
    expect(mocks.cancel).toHaveBeenCalledTimes(1);
  });

  it("uses the active locale for preview controls", async () => {
    appState.language = "zh";
    const wrapper = mount(RefactorPreviewModal);
    expect(wrapper.text()).toContain("修改前");
    expect(wrapper.text()).toContain("修改后");
    expect(wrapper.get('[data-test="refactor-cancel"]').text()).toBe("取消");
    expect(wrapper.get('[data-test="refactor-apply"]').text()).toBe("应用");
  });
});
