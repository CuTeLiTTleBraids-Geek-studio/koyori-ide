/**
 * API surface restriction tests (G-SEC-12 requirement 4).
 *
 * Verifies:
 *   - Trusted level: only read-only APIs are accessible.
 *   - Reviewed level: adds file-write APIs.
 *   - Restricted level: adds shell/network APIs (with confirmation).
 *   - Unknown methods are denied by default.
 *   - Permission gates: methods require their declared permission.
 *   - Dangerous commands always require confirmation.
 *   - Wildcard dangerous command patterns match correctly.
 */
import { describe, it, expect } from "vitest";
import {
  DANGEROUS_COMMANDS,
  checkApiAccess,
  isDangerousCommand,
  shouldConfirmCommand,
  allowedMethodsFor,
  apiSurfaceSummary,
  EXPOSED_NAMESPACES,
} from "@/lib/extensionHost/apiSurface";
import type { ExtensionPermission } from "@/stores/extensionSecurity";

const TRUSTED_PERMS: ExtensionPermission[] = ["fs.read", "ui.notifications"];
const REVIEWED_PERMS: ExtensionPermission[] = ["fs.read", "fs.write", "ui.notifications"];
const RESTRICTED_PERMS: ExtensionPermission[] = [
  "fs.read",
  "fs.write",
  "shell.execute",
  "network",
  "ui.notifications",
];

describe("API surface — Trusted level (read-only)", () => {
  it("allows fs.readFile with fs.read permission", () => {
    const r = checkApiAccess("fs.readFile", "trusted", TRUSTED_PERMS);
    expect(r.allowed).toBe(true);
    expect(r.requiresConfirmation).toBe(false);
  });

  it("allows commands.registerCommand (no permission required)", () => {
    const r = checkApiAccess("commands.registerCommand", "trusted", []);
    expect(r.allowed).toBe(true);
  });

  it("allows languages.registerCompletionItemProvider", () => {
    const r = checkApiAccess(
      "languages.registerCompletionItemProvider",
      "trusted",
      [],
    );
    expect(r.allowed).toBe(true);
  });

  it("denies fs.writeFile at trusted level", () => {
    const r = checkApiAccess("fs.writeFile", "trusted", TRUSTED_PERMS);
    expect(r.allowed).toBe(false);
    expect(r.reason).toContain("reviewed");
  });

  it("denies shell.execute at trusted level", () => {
    const r = checkApiAccess("shell.execute", "trusted", TRUSTED_PERMS);
    expect(r.allowed).toBe(false);
  });

  it("denies fs.readFile when fs.read permission not declared", () => {
    const r = checkApiAccess("fs.readFile", "trusted", []);
    expect(r.allowed).toBe(false);
    expect(r.reason).toContain("fs.read");
  });
});

describe("API surface — Reviewed level (file write)", () => {
  it("allows fs.writeFile with fs.write permission", () => {
    const r = checkApiAccess("fs.writeFile", "reviewed", REVIEWED_PERMS);
    expect(r.allowed).toBe(true);
    expect(r.requiresConfirmation).toBe(false);
  });

  it("allows fs.readFile (inherited from trusted)", () => {
    const r = checkApiAccess("fs.readFile", "reviewed", REVIEWED_PERMS);
    expect(r.allowed).toBe(true);
  });

  it("denies shell.execute at reviewed level", () => {
    const r = checkApiAccess("shell.execute", "reviewed", REVIEWED_PERMS);
    expect(r.allowed).toBe(false);
    expect(r.reason).toContain("restricted");
  });

  it("denies network.request at reviewed level", () => {
    const r = checkApiAccess("network.request", "reviewed", REVIEWED_PERMS);
    expect(r.allowed).toBe(false);
  });

  it("denies fs.writeFile when fs.write permission not declared", () => {
    const r = checkApiAccess("fs.writeFile", "reviewed", ["fs.read"]);
    expect(r.allowed).toBe(false);
    expect(r.reason).toContain("fs.write");
  });
});

describe("API surface — Restricted level (network + shell)", () => {
  it("allows shell.execute with confirmation", () => {
    const r = checkApiAccess("shell.execute", "restricted", RESTRICTED_PERMS);
    expect(r.allowed).toBe(true);
    expect(r.requiresConfirmation).toBe(true);
    expect(r.confirmLabel).toBeTruthy();
  });

  it("allows network.request with confirmation", () => {
    const r = checkApiAccess("network.request", "restricted", RESTRICTED_PERMS);
    expect(r.allowed).toBe(true);
    expect(r.requiresConfirmation).toBe(true);
  });

  it("denies shell.execute when shell.execute permission not declared", () => {
    const r = checkApiAccess(
      "shell.execute",
      "restricted",
      ["fs.read", "network"],
    );
    expect(r.allowed).toBe(false);
    expect(r.reason).toContain("shell.execute");
  });

  it("allows fs.writeFile (inherited from reviewed)", () => {
    const r = checkApiAccess("fs.writeFile", "restricted", RESTRICTED_PERMS);
    expect(r.allowed).toBe(true);
  });
});

describe("API surface — deny by default", () => {
  it("denies unknown methods", () => {
    const r = checkApiAccess("fs.deleteEverything", "restricted", RESTRICTED_PERMS);
    expect(r.allowed).toBe(false);
    expect(r.reason).toContain("not exposed");
  });

  it("denies a method that looks similar but isn't registered", () => {
    const r = checkApiAccess("workspace.writeFiles", "restricted", RESTRICTED_PERMS);
    expect(r.allowed).toBe(false);
  });
});

describe("Dangerous commands (G-SEC-12 req. 4)", () => {
  it("flags workbench.action.terminal.sendSequence as dangerous", () => {
    expect(isDangerousCommand("workbench.action.terminal.sendSequence")).toBe(true);
  });

  it("flags workbench.action.files.save as dangerous", () => {
    expect(isDangerousCommand("workbench.action.files.save")).toBe(true);
  });

  it("flags _workbench.* prefix as dangerous (wildcard)", () => {
    expect(isDangerousCommand("_workbench.action.internal")).toBe(true);
    expect(isDangerousCommand("_workbench.anything")).toBe(true);
  });

  it("does not flag safe commands as dangerous", () => {
    expect(isDangerousCommand("workbench.action.terminal.new")).toBe(false);
    expect(isDangerousCommand("editor.action.format")).toBe(false);
  });

  it("DANGEROUS_COMMANDS includes the required entries", () => {
    expect(DANGEROUS_COMMANDS).toContain("workbench.action.terminal.sendSequence");
    expect(DANGEROUS_COMMANDS).toContain("workbench.action.files.save");
    expect(DANGEROUS_COMMANDS.some((c) => c.startsWith("_workbench."))).toBe(true);
  });

  // H-15: 验证扩充后的危险命令黑名单能拦截新增的高危命令
  it("H-15: flags newly-added dangerous workbench commands", () => {
    const expanded = [
      "workbench.action.files.saveAll",
      "workbench.action.terminal.kill",
      "workbench.action.reloadWindow",
      "workbench.action.openSettings",
      "workbench.action.closeFolder",
      "workbench.action.quit",
      "workbench.action.closeWindow",
      "workbench.action.newWindow",
      "workbench.action.zoomIn",
      "workbench.action.zoomOut",
      "workbench.action.toggleSidebarVisibility",
    ];
    for (const cmd of expanded) {
      expect(isDangerousCommand(cmd)).toBe(true);
    }
  });

  it("H-15: shouldConfirmCommand requires confirmation for expanded dangerous commands at any level", () => {
    expect(shouldConfirmCommand("workbench.action.files.saveAll", "trusted")).toBe(true);
    expect(shouldConfirmCommand("workbench.action.reloadWindow", "reviewed")).toBe(true);
    expect(shouldConfirmCommand("workbench.action.quit", "restricted")).toBe(true);
  });

  it("H-15: safe commands remain unflagged after expansion", () => {
    expect(isDangerousCommand("editor.action.clipCopyAction")).toBe(false);
    expect(isDangerousCommand("workbench.action.terminal.new")).toBe(false);
    expect(isDangerousCommand("cursorMove")).toBe(false);
  });

  it("shouldConfirmCommand returns true for dangerous commands at any level", () => {
    expect(
      shouldConfirmCommand("workbench.action.terminal.sendSequence", "trusted"),
    ).toBe(true);
    expect(
      shouldConfirmCommand("workbench.action.files.save", "restricted"),
    ).toBe(true);
  });

  it("shouldConfirmCommand returns true for shell/network at restricted level", () => {
    expect(shouldConfirmCommand("shell.execute", "restricted")).toBe(true);
    expect(shouldConfirmCommand("network.request", "restricted")).toBe(true);
  });

  it("shouldConfirmCommand returns false for safe commands at trusted level", () => {
    expect(shouldConfirmCommand("editor.action.format", "trusted")).toBe(false);
  });
});

describe("allowedMethodsFor", () => {
  it("returns only read-only methods for trusted level", () => {
    const methods = allowedMethodsFor("trusted", TRUSTED_PERMS);
    expect(methods).toContain("fs.readFile");
    expect(methods).toContain("commands.registerCommand");
    expect(methods).not.toContain("fs.writeFile");
    expect(methods).not.toContain("shell.execute");
  });

  it("includes write methods for reviewed level", () => {
    const methods = allowedMethodsFor("reviewed", REVIEWED_PERMS);
    expect(methods).toContain("fs.writeFile");
    expect(methods).toContain("fs.readFile");
    expect(methods).not.toContain("shell.execute");
  });

  it("includes shell/network methods for restricted level", () => {
    const methods = allowedMethodsFor("restricted", RESTRICTED_PERMS);
    expect(methods).toContain("shell.execute");
    expect(methods).toContain("network.request");
    expect(methods).toContain("fs.writeFile");
  });
});

describe("EXPOSED_NAMESPACES", () => {
  it("trusted has commands, languages, window, workspace", () => {
    expect(EXPOSED_NAMESPACES.trusted).toContain("commands");
    expect(EXPOSED_NAMESPACES.trusted).toContain("languages");
    expect(EXPOSED_NAMESPACES.trusted).not.toContain("shell");
    expect(EXPOSED_NAMESPACES.trusted).not.toContain("network");
  });

  it("restricted has shell and network", () => {
    expect(EXPOSED_NAMESPACES.restricted).toContain("shell");
    expect(EXPOSED_NAMESPACES.restricted).toContain("network");
  });

  it("reviewed is a subset of restricted", () => {
    for (const ns of EXPOSED_NAMESPACES.reviewed) {
      expect(EXPOSED_NAMESPACES.restricted).toContain(ns);
    }
  });
});

describe("apiSurfaceSummary", () => {
  it("returns a non-empty summary for each level", () => {
    expect(apiSurfaceSummary("trusted")).toBeTruthy();
    expect(apiSurfaceSummary("reviewed")).toBeTruthy();
    expect(apiSurfaceSummary("restricted")).toBeTruthy();
  });

  it("mentions network/shell for restricted", () => {
    expect(apiSurfaceSummary("restricted")).toMatch(/network|shell/i);
  });
});
