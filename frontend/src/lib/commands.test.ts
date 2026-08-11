import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Command } from "@/types";
import {
  CommandRegistry,
  commandRegistry,
  rankPaletteCommands,
  syncPaletteCommands,
} from "./commands";

describe("command registry", () => {
  beforeEach(() => commandRegistry.clear());

  it("stores the complete command contract and executes handlers", async () => {
    const registry = new CommandRegistry();
    const handler = vi.fn();
    registry.register({
      id: "editor.test",
      title: "Editor Test",
      keybinding: "Ctrl+E",
      category: "editor",
      handler,
    });

    expect(registry.get("editor.test")).toMatchObject({
      id: "editor.test",
      title: "Editor Test",
      keybinding: "Ctrl+E",
      category: "editor",
      handler,
    });
    expect(await registry.execute("editor.test")).toBe(true);
    expect(handler).toHaveBeenCalledTimes(1);
  });

  it("replaces commands owned by one source without touching other sources", () => {
    const registry = new CommandRegistry();
    registry.register({ id: "runtime", title: "Runtime", category: "general", handler: vi.fn() });
    registry.replaceSource("palette", [
      { id: "old", title: "Old", category: "file", handler: vi.fn() },
    ]);
    registry.replaceSource("palette", [
      { id: "new", title: "New", category: "file", handler: vi.fn() },
    ]);

    expect(registry.list().map((command) => command.id).sort()).toEqual(["new", "runtime"]);
  });

  it("fuzzy-searches titles, categories and keybindings", () => {
    const registry = new CommandRegistry();
    registry.register({
      id: "git.commit",
      title: "Create Commit",
      keybinding: "Ctrl+Enter",
      category: "git",
      handler: vi.fn(),
    });
    registry.register({
      id: "file.open",
      title: "Open File",
      category: "file",
      handler: vi.fn(),
    });

    expect(registry.search("git").map((command) => command.id)).toEqual(["git.commit"]);
    expect(registry.search("opf").map((command) => command.id)).toEqual(["file.open"]);
    expect(registry.search("ctre").map((command) => command.id)).toEqual(["git.commit"]);
  });

  it("syncs palette commands and preserves fuzzy ranking", () => {
    const commands: Command[] = [
      { id: "save", label: "Save File", shortcut: "Ctrl+S", action: vi.fn() },
      { id: "open", label: "Open Folder", action: vi.fn() },
    ];
    syncPaletteCommands(commands);

    expect(commandRegistry.get("save")).toMatchObject({
      title: "Save File",
      keybinding: "Ctrl+S",
      category: "file",
    });
    expect(rankPaletteCommands(commands, "opf").map((command) => command.id)).toEqual(["open"]);
  });
});
