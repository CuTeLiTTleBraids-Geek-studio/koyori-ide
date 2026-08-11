import { createHash, webcrypto } from "node:crypto";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  listDirectory: vi.fn(),
  readFile: vi.fn(),
  writeFileIfUnchanged: vi.fn(),
  computeBaseline: vi.fn(),
  clearDirtyBuffer: vi.fn(),
  aiComplete: vi.fn(),
}));

vi.mock("@/api/services", () => ({
  fileService: {
    listDirectory: mocks.listDirectory,
    readFile: mocks.readFile,
    writeFileIfUnchanged: mocks.writeFileIfUnchanged,
  },
  recoveryService: {
    computeBaseline: mocks.computeBaseline,
    clearDirtyBuffer: mocks.clearDirtyBuffer,
    saveDirtyBuffer: vi.fn(),
  },
  lspService: {
    organizeImports: vi.fn().mockResolvedValue([]),
  },
  aiService: {
    startStream: mocks.aiComplete,
    stopStream: vi.fn(),
    setConfig: vi.fn(),
  },
}));

vi.mock("@/lib/vscodeExtensionActivation", () => ({
  activateOnLanguage: vi.fn().mockResolvedValue(undefined),
}));

vi.mock("@/lib/notifications", () => ({
  notifyError: vi.fn(),
  notifyWarning: vi.fn(),
  notifySuccess: vi.fn(),
}));

vi.mock("@wailsio/runtime", () => ({
  Events: { On: vi.fn(() => () => undefined), Emit: vi.fn() },
}));

import { fileService, aiService } from "@/api/services";
import { appState } from "@/stores/app";
import {
  editorState,
  openFileFromPath,
  saveFilePath,
  updateContent,
} from "@/stores/editor";

describe("O-E2E core path", () => {
  beforeEach(() => {
    vi.stubGlobal("crypto", webcrypto);
    appState.currentProject = null;
    appState.formatOnSave = false;
    appState.organizeImportsOnSave = false;
    editorState.openFiles = [];
    editorState.activeFilePath = null;
    vi.clearAllMocks();
  });

  it("opens a directory, renders its tree entry, opens the file and saves it", async () => {
    const workspace = "/e2e-workspace";
    const filePath = `${workspace}/src/notes.txt`;
    mocks.listDirectory.mockResolvedValue([
      { name: "src", path: `${workspace}/src`, isDir: true, size: 0, modified: 0 },
    ]);
    mocks.readFile.mockResolvedValue("status: open\n");
    mocks.writeFileIfUnchanged.mockResolvedValue(undefined);
    mocks.computeBaseline.mockResolvedValue({ mtime: 1, hash: "disk-baseline" });
    mocks.clearDirtyBuffer.mockResolvedValue(undefined);
    mocks.aiComplete.mockResolvedValue({ text: "offline mock" });

    appState.currentProject = workspace;
    const tree = await fileService.listDirectory(workspace);
    expect(tree).toEqual([
      expect.objectContaining({ name: "src", path: `${workspace}/src`, isDir: true }),
    ]);

    await openFileFromPath(filePath);
    expect(editorState.activeFilePath).toBe(filePath);
    expect(editorState.openFiles[0]?.content).toBe("status: open\n");

    expect(updateContent(filePath, "status: saved\n")).toBe(true);
    await expect(saveFilePath(filePath, { skipFormat: true })).resolves.toBe(true);
    expect(mocks.writeFileIfUnchanged).toHaveBeenCalledWith(
      filePath,
      "status: saved\n",
      createHash("sha256").update("status: open\n").digest("hex"),
    );
    expect(editorState.openFiles[0]?.isDirty).toBe(false);

    await expect(aiService.startStream([])).resolves.toEqual({ text: "offline mock" });
  });
});
