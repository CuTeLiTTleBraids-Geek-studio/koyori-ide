import { reactive, ref } from "vue";
import type { Disposable, StatusBarItem } from "@/lib/extensionHost/vscodeApi";
import { clearOutputsBySource, pushOutput } from "@/stores/output";
import { appState } from "@/stores/app";

export interface ExtensionStatusBarEntry {
  id: number;
  text: string;
  tooltip?: string;
  command?: string;
  visible: boolean;
}

const nextStatusId = (() => {
  let value = 0;
  return () => ++value;
})();

export const extensionStatusBarEntries = reactive<ExtensionStatusBarEntry[]>([]);
export const extensionProgress = ref<{ title?: string; message?: string; increment?: number } | null>(null);

export function setExtensionStatusBarMessage(text: string, hideAfter?: number): Disposable {
  const id = nextStatusId();
  const entry: ExtensionStatusBarEntry = { id, text, visible: true };
  extensionStatusBarEntries.push(entry);
  let timer: ReturnType<typeof setTimeout> | undefined;
  if (hideAfter && hideAfter > 0) timer = setTimeout(() => dispose(), hideAfter);
  function dispose(): void {
    if (timer) clearTimeout(timer);
    const index = extensionStatusBarEntries.findIndex((item) => item.id === id);
    if (index >= 0) extensionStatusBarEntries.splice(index, 1);
  }
  return { dispose };
}

export function createExtensionStatusBarItem(): StatusBarItem {
  const id = nextStatusId();
  const entry: ExtensionStatusBarEntry = { id, text: "", visible: false };
  extensionStatusBarEntries.push(entry);
  const item: StatusBarItem = {
    get text() { return entry.text; },
    set text(value: string) { entry.text = value; },
    get tooltip() { return entry.tooltip; },
    set tooltip(value: string | undefined) { entry.tooltip = value; },
    get command() { return entry.command; },
    set command(value: string | undefined) { entry.command = value; },
    show() { entry.visible = true; },
    hide() { entry.visible = false; },
    dispose() {
      const index = extensionStatusBarEntries.indexOf(entry);
      if (index >= 0) extensionStatusBarEntries.splice(index, 1);
    },
  };
  return item;
}

export function writeExtensionOutput(
  channel: string,
  action: "append" | "appendLine" | "clear" | "show" | "hide" | "dispose",
  value?: string,
): void {
  if (action === "clear") {
    clearOutputsBySource(channel);
    return;
  }
  if (action === "dispose") {
    clearOutputsBySource(channel);
    return;
  }
  if (action === "show") {
    appState.bottomPanelView = "output";
    appState.terminalVisible = true;
  }
  if (action === "append" || action === "appendLine") {
    pushOutput(channel, "info", action === "appendLine" ? (value ?? "") + "\n" : value ?? "");
  }
}

