import { beforeEach, describe, expect, it, vi } from "vitest";

const aiBindings = vi.hoisted(() => ({
  Send: vi.fn(),
}));

const updateBindings = vi.hoisted(() => ({
  CheckForUpdates: vi.fn(),
}));

vi.mock("@wailsio/runtime", () => ({
  Call: { ByID: vi.fn(), ByName: vi.fn() },
  Create: {
    Any: (value: unknown) => value,
    Array: () => (value: unknown) => value,
    Map: () => (value: unknown) => value,
    Nullable: () => (value: unknown) => value,
    Struct: () => (value: unknown) => value,
  },
}));

vi.mock("../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/aiservice.js", () => aiBindings);
vi.mock("../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/updateservice.js", () => updateBindings);

import { aiService } from "./ai";
import { updateService } from "./platform";

describe("non-LSP service binding normalization", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("passes through non-null AI and update responses", async () => {
    const chatResponse = { Content: "ok", FinishReason: "stop" };
    const updateInfo = {
      hasUpdate: true,
      latestVersion: "2.0.0",
      currentVersion: "1.0.0",
      releaseNotes: "notes",
      downloadUrl: "https://github.com/example/release.zip",
      releaseDate: "2026-07-17",
    };
    aiBindings.Send.mockResolvedValue(chatResponse);
    updateBindings.CheckForUpdates.mockResolvedValue(updateInfo);

    await expect(
      aiService.send([{ role: "user", content: "ping" }]),
    ).resolves.toBe(chatResponse);
    await expect(
      updateService.checkForUpdates("1.0.0", ""),
    ).resolves.toBe(updateInfo);
  });

  it("rejects unexpected null business objects", async () => {
    aiBindings.Send.mockResolvedValue(null);
    updateBindings.CheckForUpdates.mockResolvedValue(null);

    await expect(
      aiService.send([{ role: "user", content: "ping" }]),
    ).rejects.toThrow("AIService.Send returned no result");
    await expect(
      updateService.checkForUpdates("1.0.0", ""),
    ).rejects.toThrow("UpdateService.CheckForUpdates returned no result");
  });
});
