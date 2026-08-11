import { describe, expect, it } from "vitest";
import {
  isFullIDERuntimeRole,
  normalizeRuntimeRole,
  readRuntimeRoleToken,
  resolveRuntimeRoleToken,
} from "./runtimeRole";

describe("runtime role resolver", () => {
  it("reads only the backend token query parameter", () => {
    expect(readRuntimeRoleToken("http://wails.local/?koyori-ide_runtime_role=abc#/ai-window")).toBe("abc");
    expect(readRuntimeRoleToken("http://wails.local/#/ai-window?koyori-ide_runtime_role=abc")).toBe("");
  });

  it("normalizes unknown backend values to minimal", () => {
    expect(normalizeRuntimeRole("ai")).toBe("ai");
    expect(normalizeRuntimeRole("main")).toBe("main");
    expect(normalizeRuntimeRole("forged")).toBe("minimal");
    expect(isFullIDERuntimeRole("ai")).toBe(false);
    expect(isFullIDERuntimeRole("main")).toBe(true);
  });

  it("fails closed when backend resolution rejects", async () => {
    await expect(resolveRuntimeRoleToken(async () => "forged", "token")).resolves.toBe("minimal");
    await expect(resolveRuntimeRoleToken(async () => { throw new Error("offline"); }, "token")).resolves.toBe("minimal");
    await expect(resolveRuntimeRoleToken(async () => "ai", "")).resolves.toBe("minimal");
  });
});
