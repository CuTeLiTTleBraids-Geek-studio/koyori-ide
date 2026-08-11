// Koyori IDE 模块 · Commands。
// 喵，这是 Koyori IDE 的 Commands 模块（前端实现）~
import type { Command } from "@/types";
import { matchIndices, scoreMatch } from "@/lib/fuzzy";

export type CommandCategory =
  | "editor"
  | "file"
  | "git"
  | "view"
  | "settings"
  | "debug"
  | "terminal"
  | "extensions"
  | "refactor"
  | "toolchain"
  | "general";

export interface RegisteredCommand {
  id: string;
  title: string;
  keybinding?: string;
  category: CommandCategory;
  handler: () => void | Promise<void>;
  keywords?: string[];
  disabled?: boolean;
  disabledReason?: string;
}

interface CommandRecord extends RegisteredCommand {
  source: string;
}

function withoutSource(command: CommandRecord): RegisteredCommand {
  const { source, ...registered } = command;
  void source;
  return registered;
}

export class CommandRegistry {
  private readonly commands = new Map<string, CommandRecord>();
  private readonly listeners = new Set<() => void>();

  register(command: RegisteredCommand, source = "runtime"): () => void {
    this.commands.set(command.id, { ...command, source });
    this.emitChange();
    return () => {
      const current = this.commands.get(command.id);
      if (current?.source !== source) return;
      this.commands.delete(command.id);
      this.emitChange();
    };
  }

  replaceSource(source: string, commands: readonly RegisteredCommand[]): void {
    for (const [id, command] of this.commands) {
      if (command.source === source) this.commands.delete(id);
    }
    for (const command of commands) {
      this.commands.set(command.id, { ...command, source });
    }
    this.emitChange();
  }

  get(id: string): RegisteredCommand | undefined {
    const command = this.commands.get(id);
    if (!command) return undefined;
    return withoutSource(command);
  }

  list(): RegisteredCommand[] {
    return [...this.commands.values()].map(withoutSource);
  }

  search(query: string): RegisteredCommand[] {
    const normalized = query.trim();
    if (!normalized) return this.list();

    return [...this.commands.values()]
      .map((command) => {
        const haystack = [
          command.title,
          command.category,
          command.keybinding ?? "",
          ...(command.keywords ?? []),
        ].join(" ");
        const indices = matchIndices(haystack, normalized);
        return indices === null
          ? null
          : { command, score: scoreMatch(haystack, indices) };
      })
      .filter((entry): entry is { command: CommandRecord; score: number } => entry !== null)
      .sort((left, right) => right.score - left.score || left.command.title.localeCompare(right.command.title))
      .map(({ command }) => withoutSource(command));
  }

  async execute(id: string): Promise<boolean> {
    const command = this.commands.get(id);
    if (!command || command.disabled) return false;
    await command.handler();
    return true;
  }

  subscribe(listener: () => void): () => void {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  }

  clear(): void {
    if (this.commands.size === 0) return;
    this.commands.clear();
    this.emitChange();
  }

  private emitChange(): void {
    for (const listener of this.listeners) listener();
  }
}

export const commandRegistry = new CommandRegistry();

export function inferCommandCategory(command: Pick<Command, "id" | "label">): CommandCategory {
  const value = `${command.id} ${command.label}`.toLowerCase();
  if (/git|stash|commit|branch|source-control/.test(value)) return "git";
  if (/debug|test|coverage/.test(value)) return "debug";
  if (/terminal|shell|repl/.test(value)) return "terminal";
  if (/setting|preference|shortcut|theme|appearance/.test(value)) return "settings";
  if (/extension|marketplace|plugin/.test(value)) return "extensions";
  if (/refactor/.test(value)) return "refactor";
  if (/toolchain|lint|build|format/.test(value)) return "toolchain";
  if (/save|open|file|import/.test(value)) return "file";
  if (/toggle|view|panel|sidebar|activity-bar|status-bar/.test(value)) return "view";
  if (/editor|cursor|selection|minimap/.test(value)) return "editor";
  return "general";
}

export function syncPaletteCommands(commands: readonly Command[]): void {
  commandRegistry.replaceSource("main-palette", commands.map((command) => ({
    id: command.id,
    title: command.label,
    keybinding: command.shortcut,
    category: inferCommandCategory(command),
    handler: command.action,
    disabled: command.disabled,
    disabledReason: command.disabledReason,
  })));
}

export function rankPaletteCommands(commands: readonly Command[], query: string): Command[] {
  const byId = new Map(commands.map((command) => [command.id, command]));
  return commandRegistry.search(query)
    .map((registered) => byId.get(registered.id))
    .filter((command): command is Command => Boolean(command));
}
