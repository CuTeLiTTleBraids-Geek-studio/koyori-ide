import { describe, expect, it } from "vitest";
import {
  apiCapabilityMatrix,
  capabilityOf,
  EXT_API_UNSUPPORTED_CODE,
} from "./apiCapability";
import { ExtensionApiUnsupportedError } from "./extensionHost";

describe("G13 extension API capability matrix", () => {
  it("tracks every G13-relevant API with a unique entry", () => {
    const apis = apiCapabilityMatrix.map((entry) => entry.api);
    expect(new Set(apis).size).toBe(apis.length);
    for (const api of [
      "workspace.saveAll",
      "workspace.getConfiguration",
      "workspace.onDidChangeConfiguration",
      "window.showInformationMessage",
      "window.showWarningMessage",
      "window.showErrorMessage",
      "window.showInputBox",
      "window.showQuickPick",
      "window.createOutputChannel",
      "window.createTerminal",
      "window.createWebviewPanel",
    ]) {
      expect(capabilityOf(api), `missing matrix entry for ${api}`).toBeDefined();
    }
  });

  it("marks saveAll implemented (real save + failure propagation)", () => {
    expect(capabilityOf("workspace.saveAll")?.status).toBe("implemented");
  });

  it("marks InputBox/QuickPick unsupported (no fake success)", () => {
    expect(capabilityOf("window.showInputBox")?.status).toBe("unsupported");
    expect(capabilityOf("window.showQuickPick")?.status).toBe("unsupported");
  });

  it("the unsupported error code matches the matrix constant", () => {
    const err = new ExtensionApiUnsupportedError("window.showInputBox");
    expect(err.code).toBe(EXT_API_UNSUPPORTED_CODE);
    expect(err.apiVersion).toBe("v1");
    expect(err.message).toContain("KOYORI_IDE_EXT_API_UNSUPPORTED");
  });

  it("does not claim a fake success for unsupported APIs in the matrix", () => {
    for (const entry of apiCapabilityMatrix) {
      if (entry.status === "unsupported") {
        expect(entry.note.toLowerCase()).toMatch(/fail|not implemented|unsupported/);
        expect(entry.note.toLowerCase()).not.toMatch(/returns (the )?default|returns the first item/);
      }
    }
  });
});