/**
 * G-VSC-04: Unified command aggregation.
 *
 * The IDE hosts two coexisting extension systems (see vscodeExtensions.ts):
 *   1. koyori-ide native plugins (pluginRegistry.ts) — higher priority.
 *   2. VS Code extensions (vscodeExtensions.ts) — supplementary.
 *
 * This module merges commands from both sources into a single list for the
 * command palette. Priority rules:
 *   - Native plugin commands always come BEFORE VS Code extension commands.
 *   - When a native plugin and a VS Code extension register commands with the
 *     same display label, the native command wins and the VS Code command is
 *     kept as a fallback (still listed, but disambiguated by its source badge
 *     in the palette). A warning is logged to the console + Output panel so
 *     conflicts are visible.
 *   - Built-in IDE commands (handled by the caller, MainLayout) take absolute
 *     priority over both; this module only aggregates extension-contributed
 *     commands. The caller is responsible for de-duplicating against built-ins.
 *
 * Reactivity: getUnifiedCommands() reads the reactive version counters from
 * both registries, so a Vue computed that calls it re-evaluates whenever
 * either registry mutates.
 */
// Koyori IDE 模块 · Unified Commands。
// 喵，这是 Koyori IDE 的 Unified Commands 模块（前端实现）~

import { listPluginCommands, executePluginCommand } from "./pluginRegistry";
import {
  listVscodeExtensionCommands,
  executeVscodeExtensionCommand,
} from "./vscodeExtensions";
import type { Command } from "@/types";

export interface UnifiedCommand {
  id: string;
  label: string;
  source: "native" | "vscode";
  category?: string;
  icon?: string;
  handler: () => void | Promise<void>;
}

/**
 * Aggregate commands from native plugins and VS Code extensions. Native
 * commands come first (higher priority); VS Code extension commands follow.
 *
 * Conflict handling: if a native command and a VS Code extension command share
 * the same label, both are retained (the VS Code one acts as a fallback) but a
 * warning is logged so the conflict is discoverable. The palette disambiguates
 * them by source badge.
 */
export function getUnifiedCommands(): UnifiedCommand[] {
  const commands: UnifiedCommand[] = [];

  // Native plugins first (higher priority).
  for (const rc of listPluginCommands()) {
    commands.push({
      id: `native.${rc.pluginName}.${rc.id}`,
      label: rc.category ? `${rc.category}: ${rc.title}` : rc.title,
      source: "native",
      category: rc.category,
      handler: () => {
        // Fire-and-forget the async handler. Errors surface via
        // executePluginCommand's Output-panel logging path.
        executePluginCommand(rc.id).catch((e: unknown) => {
          console.error(`Native plugin command "${rc.id}" failed:`, e);
        });
      },
    });
  }

  // VS Code extensions (supplementary). Track native labels to detect
  // conflicts and log a warning when an extension shadows a native command.
  const nativeLabels = new Set(
    commands.filter((c) => c.source === "native").map((c) => c.label),
  );
  for (const vc of listVscodeExtensionCommands()) {
    const label = vc.category ? `${vc.category}: ${vc.label}` : vc.label;
    if (nativeLabels.has(label)) {
      // G-VSC-04 Step 5: log a warning when a native plugin and a VS Code
      // extension provide the same capability. The native command takes
      // priority (better performance, stricter sandbox); the extension is
      // retained as a fallback and disambiguated by source in the palette.
      logConflictWarning(label, vc.extensionId);
    }
    commands.push({
      id: `vscode.${vc.extensionId}.${vc.id}`,
      label,
      source: "vscode",
      category: vc.category,
      handler: () => {
        executeVscodeExtensionCommand(vc.id).catch((e: unknown) => {
          console.error(`VS Code extension command "${vc.id}" failed:`, e);
        });
      },
    });
  }

  return commands;
}

/**
 * Convert a UnifiedCommand into the palette's Command shape. Sets the `source`
 * field so CommandPalette.vue can render a source badge. The caller merges
 * these with built-in commands (which have no `source`).
 */
export function toPaletteCommand(uc: UnifiedCommand): Command {
  return {
    id: uc.id,
    label: uc.label,
    source: uc.source,
    action: () => {
      void uc.handler();
    },
  };
}

/**
 * Bulk-convert unified commands to palette commands. Convenience wrapper.
 */
export function getUnifiedPaletteCommands(): Command[] {
  return getUnifiedCommands().map(toPaletteCommand);
}

/**
 * Log a conflict warning when a native plugin and a VS Code extension provide
 * a command with the same label. Best-effort: if the Output store is
 * unavailable (e.g. in tests), the warning is only written to the console.
 */
async function logConflictWarning(label: string, extensionId: string): Promise<void> {
  const message =
    `G-VSC-04: command conflict — native plugin and VS Code extension "${extensionId}" ` +
    `both provide a command labeled "${label}". Native plugin takes priority; ` +
    `the extension command is available as a fallback.`;
  console.warn(message);
  try {
    const { pushOutput } = await import("@/stores/output");
    pushOutput("Plugins", "warn", message);
  } catch {
    // Output store unavailable (e.g. test environment) — console warning suffices.
  }
}
