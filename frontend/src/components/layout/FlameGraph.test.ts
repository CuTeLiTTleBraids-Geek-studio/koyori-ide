import { mount } from "@vue/test-utils";
import { describe, expect, it } from "vitest";
import FlameGraph from "./FlameGraph.vue";

const root = {
  id: "0",
  name: "all",
  value: 100,
  children: [
    {
      id: "0.0",
      name: "root",
      value: 100,
      children: [
        { id: "0.0.0", name: "left", value: 70, children: [] },
        { id: "0.0.1", name: "right", value: 30, children: [] },
      ],
    },
  ],
};

describe("FlameGraph", () => {
  it("renders frames, highlights search matches, and supports zoom reset", async () => {
    const wrapper = mount(FlameGraph, { props: { root, unit: "nanoseconds" } });
    expect(wrapper.findAll(".flame-graph__frame")).toHaveLength(4);
    expect(wrapper.get(".flame-graph__svg").attributes("role")).toBe("group");
    expect(wrapper.findAll('.flame-graph__frame[tabindex="0"]')).toHaveLength(1);
    expect(wrapper.findAll('.flame-graph__frame[tabindex="-1"]')).toHaveLength(1);
    expect(wrapper.find('[data-frame-id="0.0.0"]').attributes("role")).toBeUndefined();

    await wrapper.get('.flame-graph__frame[tabindex="0"]').trigger("keydown", { key: "ArrowRight" });
    expect(wrapper.get('[data-frame-id="0.0"]').attributes("tabindex")).toBe("0");

    await wrapper.get(".flame-graph__search").setValue("left");
    expect(wrapper.findAll(".flame-graph__frame--match")).toHaveLength(1);

    await wrapper.get('[data-frame-id="0.0"]').trigger("click");
    expect(wrapper.text()).toContain("root");
    expect(wrapper.find(".flame-graph__reset").attributes("disabled")).toBeUndefined();

    await wrapper.get(".flame-graph__reset").trigger("click");
    expect(wrapper.find(".flame-graph__reset").attributes("disabled")).toBeDefined();
  });
});
