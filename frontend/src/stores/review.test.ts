import { describe, it, expect, beforeEach, vi } from "vitest";

vi.mock("@wailsio/runtime", () => ({
  Events: { On: vi.fn() },
}));

vi.mock("@/api/services", () => ({
  gitService: {
    getFullDiff: vi.fn(),
  },
  aiService: {
    getPresetPrompt: vi.fn(),
    send: vi.fn(),
  },
}));

vi.mock("@/stores/rules", () => ({
  rulesForPrompt: { value: "" },
}));

vi.mock("@/stores/output", () => ({
  pushOutput: vi.fn(),
}));

vi.mock("@/lib/notifications", () => ({
  notifyError: vi.fn(),
  notifySuccess: vi.fn(),
}));

import {
  reviewState,
  hasReview,
  runReview,
  clearReview,
} from "./review";
import { gitService, aiService } from "@/api/services";
import { pushOutput } from "@/stores/output";
import { notifyError, notifySuccess } from "@/lib/notifications";

describe("review store", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    reviewState.result = null;
    reviewState.loading = false;
    reviewState.error = null;
    reviewState.reviewedFiles = [];
    reviewState.reviewedAt = null;
  });

  describe("initial state", () => {
    it("starts with no review", () => {
      expect(reviewState.result).toBeNull();
      expect(reviewState.loading).toBe(false);
      expect(reviewState.error).toBeNull();
      expect(reviewState.reviewedFiles).toEqual([]);
      expect(reviewState.reviewedAt).toBeNull();
    });

    it("hasReview is false when result is null", () => {
      expect(hasReview.value).toBe(false);
    });

    it("hasReview is false when result is empty string", () => {
      reviewState.result = "";
      expect(hasReview.value).toBe(false);
    });

    it("hasReview is true when result is non-empty", () => {
      reviewState.result = "## Code Review\n- looks good";
      expect(hasReview.value).toBe(true);
    });
  });

  describe("runReview", () => {
    it("returns early and notifies when projectRoot is empty", async () => {
      await runReview("");
      expect(reviewState.error).toBe("No project open");
      expect(notifyError).toHaveBeenCalledWith("No project open");
      expect(gitService.getFullDiff).not.toHaveBeenCalled();
    });

    it("sets error when diff is empty (no changes)", async () => {
      (gitService.getFullDiff as any).mockResolvedValue("");

      await runReview("/proj");

      expect(gitService.getFullDiff).toHaveBeenCalledWith("/proj");
      expect(reviewState.result).toBeNull();
      expect(reviewState.error).toBe("No changes to review");
      expect(reviewState.loading).toBe(false);
      expect(pushOutput).toHaveBeenCalledWith(
        "review",
        "info",
        "Code review skipped: no changes",
      );
      expect(aiService.send).not.toHaveBeenCalled();
    });

    it("sets error when diff is only whitespace", async () => {
      (gitService.getFullDiff as any).mockResolvedValue("   \n\t  ");

      await runReview("/proj");

      expect(reviewState.error).toBe("No changes to review");
      expect(aiService.send).not.toHaveBeenCalled();
    });

    it("runs full review flow with AI response", async () => {
      const fakeDiff = "=== a.ts ===\ndiff --git a/a.ts b/a.ts\n+new code\n=== b.ts ===\ndiff --git a/b.ts b/b.ts\n+more code\n";
      (gitService.getFullDiff as any).mockResolvedValue(fakeDiff);
      (aiService.getPresetPrompt as any).mockResolvedValue("Review this code.");
      (aiService.send as any).mockResolvedValue({
        Content: "## Findings\n- Critical: bug on line 5",
        FinishReason: "stop",
      });

      await runReview("/proj");

      expect(reviewState.reviewedFiles).toEqual(["a.ts", "b.ts"]);
      expect(reviewState.result).toBe("## Findings\n- Critical: bug on line 5");
      expect(reviewState.reviewedAt).not.toBeNull();
      expect(reviewState.error).toBeNull();
      expect(reviewState.loading).toBe(false);
      expect(aiService.send).toHaveBeenCalledWith([
        { role: "system", content: expect.stringContaining("Code Reviewer") },
        { role: "user", content: expect.stringContaining("Review this code.") },
      ]);
      expect(notifySuccess).toHaveBeenCalledWith("Code review completed");
    });

    it("falls back to default instruction when getPresetPrompt fails", async () => {
      (gitService.getFullDiff as any).mockResolvedValue("=== a.ts ===\n+code\n");
      (aiService.getPresetPrompt as any).mockRejectedValue(new Error("not found"));
      (aiService.send as any).mockResolvedValue({ Content: "ok", FinishReason: "stop" });

      await runReview("/proj");

      expect(aiService.send).toHaveBeenCalled();
      const userMsg = (aiService.send as any).mock.calls[0][0][1];
      expect(userMsg.content).toContain("Review this code as a senior engineer");
    });

    it("includes project rules in system prompt", async () => {
      // Override the rulesForPrompt mock for this test
      const rulesModule = await import("@/stores/rules");
      (rulesModule as any).rulesForPrompt.value = "\n\n# Project Rules\nBe strict.";

      (gitService.getFullDiff as any).mockResolvedValue("=== a.ts ===\n+code\n");
      (aiService.getPresetPrompt as any).mockResolvedValue("review");
      (aiService.send as any).mockResolvedValue({ Content: "ok", FinishReason: "stop" });

      await runReview("/proj");

      const sysMsg = (aiService.send as any).mock.calls[0][0][0];
      expect(sysMsg.content).toContain("Be strict.");
      // Reset for other tests
      (rulesModule as any).rulesForPrompt.value = "";
    });

    it("sets error when AI returns null response", async () => {
      (gitService.getFullDiff as any).mockResolvedValue("=== a.ts ===\n+code\n");
      (aiService.getPresetPrompt as any).mockResolvedValue("review");
      (aiService.send as any).mockResolvedValue(null);

      await runReview("/proj");

      expect(reviewState.result).toBeNull();
      expect(reviewState.error).toBe("AI returned an empty response");
      expect(notifyError).toHaveBeenCalledWith("Code review returned an empty response");
    });

    it("sets error when AI returns empty Content", async () => {
      (gitService.getFullDiff as any).mockResolvedValue("=== a.ts ===\n+code\n");
      (aiService.getPresetPrompt as any).mockResolvedValue("review");
      (aiService.send as any).mockResolvedValue({ Content: "", FinishReason: "stop" });

      await runReview("/proj");

      expect(reviewState.error).toBe("AI returned an empty response");
    });

    it("sets error and notifies on getFullDiff failure", async () => {
      (gitService.getFullDiff as any).mockRejectedValue(new Error("git error"));

      await runReview("/proj");

      expect(reviewState.result).toBeNull();
      expect(reviewState.error).toBe("git error");
      expect(notifyError).toHaveBeenCalledWith("Code review failed: git error");
      expect(pushOutput).toHaveBeenCalledWith(
        "review",
        "error",
        "Code review failed: git error",
      );
      expect(reviewState.loading).toBe(false);
    });

    it("sets error and notifies on aiService.send failure", async () => {
      (gitService.getFullDiff as any).mockResolvedValue("=== a.ts ===\n+code\n");
      (aiService.getPresetPrompt as any).mockResolvedValue("review");
      (aiService.send as any).mockRejectedValue(new Error("AI API down"));

      await runReview("/proj");

      expect(reviewState.result).toBeNull();
      expect(reviewState.error).toBe("AI API down");
      expect(notifyError).toHaveBeenCalledWith("Code review failed: AI API down");
    });

    it("coerces non-Error rejection to string", async () => {
      (gitService.getFullDiff as any).mockRejectedValue("string error");

      await runReview("/proj");

      expect(reviewState.error).toBe("string error");
    });

    it("sets loading true during operation and false after", async () => {
      let resolveDiff: (v: any) => void = () => {};
      (gitService.getFullDiff as any).mockReturnValue(
        new Promise((r) => {
          resolveDiff = r;
        }),
      );

      const promise = runReview("/proj");
      expect(reviewState.loading).toBe(true);

      resolveDiff("");
      await promise;

      expect(reviewState.loading).toBe(false);
    });

    it("parses reviewed files from diff headers", async () => {
      const diff = "=== src/main.ts ===\n+code\n=== src/util.ts ===\n+code\n=== README.md ===\n+code\n";
      (gitService.getFullDiff as any).mockResolvedValue(diff);
      (aiService.getPresetPrompt as any).mockResolvedValue("review");
      (aiService.send as any).mockResolvedValue({ Content: "ok", FinishReason: "stop" });

      await runReview("/proj");

      expect(reviewState.reviewedFiles).toEqual(["src/main.ts", "src/util.ts", "README.md"]);
    });

    it("handles diff with no recognizable headers", async () => {
      (gitService.getFullDiff as any).mockResolvedValue("some diff without headers");
      (aiService.getPresetPrompt as any).mockResolvedValue("review");
      (aiService.send as any).mockResolvedValue({ Content: "ok", FinishReason: "stop" });

      await runReview("/proj");

      expect(reviewState.reviewedFiles).toEqual([]);
    });
  });

  describe("clearReview", () => {
    it("resets all state fields", () => {
      reviewState.result = "some review";
      reviewState.error = "err";
      reviewState.reviewedFiles = ["a.ts"];
      reviewState.reviewedAt = 12345;
      reviewState.loading = true;

      clearReview();

      expect(reviewState.result).toBeNull();
      expect(reviewState.error).toBeNull();
      expect(reviewState.reviewedFiles).toEqual([]);
      expect(reviewState.reviewedAt).toBeNull();
      expect(reviewState.loading).toBe(false);
    });
  });

  describe("reactivity", () => {
    it("hasReview updates when result changes", () => {
      expect(hasReview.value).toBe(false);
      reviewState.result = "review text";
      expect(hasReview.value).toBe(true);
      reviewState.result = null;
      expect(hasReview.value).toBe(false);
    });
  });
});
