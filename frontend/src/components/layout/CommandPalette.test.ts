import { describe, it, expect, vi } from "vitest";
import { mount } from "@vue/test-utils";
import type { Command } from "@/types";

// 使用 vi.hoisted 让 mock 工厂能引用这些变量——vi.mock 调用会被
// 提升到文件顶部，在所有 const 声明之前执行，普通顶层 const 会处于
// 暂时性死区。CommandPalette 只依赖 @/lib/i18n（@/types 仅为类型），
// 因此只需 mock i18n 提供 t 函数（返回 key 本身即可）。
const { tMock } = vi.hoisted(() => ({
  tMock: vi.fn((key: string) => key),
}));

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({
    t: tMock,
    locale: { value: "en" },
  }),
}));

// 在 mock 设置完成后再动态导入组件，确保组件模块求值时拿到的就是 mock。
const CommandPaletteModule = await import("./CommandPalette.vue");
const CommandPalette = CommandPaletteModule.default;

// 构造一个最小可用的 Command 对象。
function makeCommand(overrides?: Partial<Command>): Command {
  return {
    id: "cmd-test",
    label: "Test Command",
    action: vi.fn(),
    ...overrides,
  };
}

function mountPalette(visible = true, commands: Command[] = []) {
  return mount(CommandPalette, {
    props: { visible, commands },
  });
}

describe("CommandPalette", () => {
  it("visible 为 false 时不渲染面板", () => {
    const wrapper = mountPalette(false);
    expect(wrapper.find(".command-palette-overlay").exists()).toBe(false);
  });

  it("visible 为 true 时渲染遮罩与输入框", () => {
    const wrapper = mountPalette(true);
    expect(wrapper.find(".command-palette-overlay").exists()).toBe(true);
    expect(wrapper.find(".command-palette__input").exists()).toBe(true);
  });

  it("显示传入的命令列表", () => {
    const commands = [
      makeCommand({ id: "1", label: "Save" }),
      makeCommand({ id: "2", label: "Open" }),
    ];
    const wrapper = mountPalette(true, commands);
    const items = wrapper.findAll(".command-palette__item");
    expect(items).toHaveLength(2);
    expect(items[0].text()).toContain("Save");
    expect(items[1].text()).toContain("Open");
  });

  it("命令带 shortcut 时显示快捷键", () => {
    const commands = [
      makeCommand({ id: "1", label: "Save", shortcut: "Ctrl+S" }),
    ];
    const wrapper = mountPalette(true, commands);
    expect(wrapper.find(".command-palette__shortcut").text()).toBe("Ctrl+S");
  });

  it("初始默认选中第一项", () => {
    const commands = [
      makeCommand({ id: "1", label: "Save" }),
      makeCommand({ id: "2", label: "Open" }),
    ];
    const wrapper = mountPalette(true, commands);
    const active = wrapper.findAll(".command-palette__item--active");
    expect(active).toHaveLength(1);
    expect(active[0].text()).toContain("Save");
  });

  it("ArrowDown 向下移动选中项", async () => {
    const commands = [
      makeCommand({ id: "1", label: "Save" }),
      makeCommand({ id: "2", label: "Open" }),
      makeCommand({ id: "3", label: "Close" }),
    ];
    const wrapper = mountPalette(true, commands);
    const input = wrapper.find(".command-palette__input");
    await input.trigger("keydown", { key: "ArrowDown" });
    const active = wrapper.findAll(".command-palette__item--active");
    expect(active).toHaveLength(1);
    expect(active[0].text()).toContain("Open");
  });

  it("ArrowDown 到末尾后不再越界", async () => {
    const commands = [
      makeCommand({ id: "1", label: "Save" }),
      makeCommand({ id: "2", label: "Open" }),
    ];
    const wrapper = mountPalette(true, commands);
    const input = wrapper.find(".command-palette__input");
    // 连续按下三次，应停留在最后一项。
    await input.trigger("keydown", { key: "ArrowDown" });
    await input.trigger("keydown", { key: "ArrowDown" });
    await input.trigger("keydown", { key: "ArrowDown" });
    const active = wrapper.findAll(".command-palette__item--active");
    expect(active).toHaveLength(1);
    expect(active[0].text()).toContain("Open");
  });

  it("ArrowUp 向上移动选中项且不会超过第一项", async () => {
    const commands = [
      makeCommand({ id: "1", label: "Save" }),
      makeCommand({ id: "2", label: "Open" }),
    ];
    const wrapper = mountPalette(true, commands);
    const input = wrapper.find(".command-palette__input");
    // 先下移到第二项，再上移回第一项。
    await input.trigger("keydown", { key: "ArrowDown" });
    await input.trigger("keydown", { key: "ArrowUp" });
    let active = wrapper.findAll(".command-palette__item--active");
    expect(active[0].text()).toContain("Save");
    // 继续上移应被钳制在第一项。
    await input.trigger("keydown", { key: "ArrowUp" });
    active = wrapper.findAll(".command-palette__item--active");
    expect(active).toHaveLength(1);
    expect(active[0].text()).toContain("Save");
  });

  it("Enter 触发 run 事件并携带当前选中命令", async () => {
    const cmd = makeCommand({ id: "1", label: "Save" });
    const wrapper = mountPalette(true, [cmd]);
    await wrapper
      .find(".command-palette__input")
      .trigger("keydown", { key: "Enter" });
    expect(wrapper.emitted("run")).toBeTruthy();
    expect(wrapper.emitted("run")![0]).toEqual([cmd]);
  });

  it("Enter 在无匹配项时不触发 run", async () => {
    const cmd = makeCommand({ id: "1", label: "Save" });
    const wrapper = mountPalette(true, [cmd]);
    await wrapper.find(".command-palette__input").setValue("zzzzz");
    await wrapper
      .find(".command-palette__input")
      .trigger("keydown", { key: "Enter" });
    expect(wrapper.emitted("run")).toBeFalsy();
  });

  it("disabled 命令可见但点击和 Enter 都不触发 run", async () => {
    const cmd = makeCommand({
      id: "refactor-extract-method",
      label: "Refactor: Extract Method",
      disabled: true,
      disabledReason: "No server action available",
    });
    const wrapper = mountPalette(true, [cmd]);
    const item = wrapper.get(".command-palette__item");
    expect(item.attributes("disabled")).toBeDefined();
    expect(item.attributes("title")).toBe("No server action available");
    await item.trigger("click");
    await wrapper
      .get(".command-palette__input")
      .trigger("keydown", { key: "Enter" });
    expect(wrapper.emitted("run")).toBeFalsy();
  });

  it("Escape 触发 close 事件", async () => {
    const wrapper = mountPalette(true, [makeCommand()]);
    await wrapper
      .find(".command-palette__input")
      .trigger("keydown", { key: "Escape" });
    expect(wrapper.emitted("close")).toBeTruthy();
  });

  it("点击命令项触发 run 事件", async () => {
    const cmd = makeCommand({ id: "1", label: "Save" });
    const wrapper = mountPalette(true, [cmd]);
    await wrapper.find(".command-palette__item").trigger("click");
    expect(wrapper.emitted("run")).toBeTruthy();
    expect(wrapper.emitted("run")![0]).toEqual([cmd]);
  });

  it("点击遮罩层触发 close 事件", async () => {
    const wrapper = mountPalette(true, [makeCommand()]);
    await wrapper.find(".dialog-backdrop-button").trigger("click");
    expect(wrapper.emitted("close")).toBeTruthy();
  });

  it("keeps listbox options out of the Tab sequence", () => {
    const wrapper = mountPalette(true, [makeCommand(), makeCommand({ id: "two" })]);
    const items = wrapper.findAll('[role="option"]');
    expect(items).toHaveLength(2);
    expect(items.every((item) => item.attributes("tabindex") === "-1")).toBe(true);
  });

  it("根据输入过滤命令（不区分大小写）", async () => {
    const commands = [
      makeCommand({ id: "1", label: "Save File" }),
      makeCommand({ id: "2", label: "Open Folder" }),
      makeCommand({ id: "3", label: "Close Tab" }),
    ];
    const wrapper = mountPalette(true, commands);
    await wrapper.find(".command-palette__input").setValue("OPEN");
    expect(wrapper.findAll(".command-palette__item")).toHaveLength(3);
    await new Promise((resolve) => setTimeout(resolve, 320));
    const items = wrapper.findAll(".command-palette__item");
    expect(items).toHaveLength(1);
    expect(items[0].text()).toContain("Open Folder");
  });

  it("无匹配命令时显示空状态", async () => {
    const commands = [makeCommand({ id: "1", label: "Save" })];
    const wrapper = mountPalette(true, commands);
    await wrapper.find(".command-palette__input").setValue("zzzzz");
    await new Promise((resolve) => setTimeout(resolve, 320));
    expect(wrapper.find(".command-palette__empty").exists()).toBe(true);
    expect(wrapper.text()).toContain("commandPalette.noMatches");
  });

  it("过滤后选中项重置为第一项", async () => {
    const commands = [
      makeCommand({ id: "1", label: "Save" }),
      makeCommand({ id: "2", label: "Open" }),
    ];
    const wrapper = mountPalette(true, commands);
    const input = wrapper.find(".command-palette__input");
    // 先下移到第二项。
    await input.trigger("keydown", { key: "ArrowDown" });
    // 过滤到只剩 "Open"，watch(filtered) 应把选中项重置为 0。
    await input.setValue("open");
    await new Promise((resolve) => setTimeout(resolve, 320));
    const active = wrapper.findAll(".command-palette__item--active");
    expect(active).toHaveLength(1);
    expect(active[0].text()).toContain("Open");
  });

  it("按 source 优先级排序：内置 > native > vscode", () => {
    const commands = [
      makeCommand({ id: "v", label: "Run", source: "vscode" }),
      makeCommand({ id: "n", label: "Run", source: "native" }),
      makeCommand({ id: "b", label: "Run" }),
    ];
    const wrapper = mountPalette(true, commands);
    const items = wrapper.findAll(".command-palette__item");
    expect(items).toHaveLength(3);
    // 三个 label 均为 "Run" 属于重复，故都显示 source 徽章。
    expect(items[0].find(".command-palette__source-badge").text()).toBe(
      "commandPalette.sourceBuiltin",
    );
    expect(items[1].find(".command-palette__source-badge").text()).toBe(
      "commandPalette.sourceNative",
    );
    expect(items[2].find(".command-palette__source-badge").text()).toBe(
      "commandPalette.sourceVscode",
    );
  });

  it("native/vscode 命令始终显示 source 徽章，内置无重复时不显示", () => {
    const commands = [
      makeCommand({ id: "1", label: "Unique Native", source: "native" }),
      makeCommand({ id: "2", label: "Unique Vscode", source: "vscode" }),
      makeCommand({ id: "3", label: "Unique Builtin" }),
    ];
    const wrapper = mountPalette(true, commands);
    const items = wrapper.findAll(".command-palette__item");
    // 排序后：内置(0) -> native(1) -> vscode(2)
    expect(items[0].find(".command-palette__source-badge").exists()).toBe(
      false,
    );
    expect(items[1].find(".command-palette__source-badge").exists()).toBe(true);
    expect(items[2].find(".command-palette__source-badge").exists()).toBe(true);
  });
});
