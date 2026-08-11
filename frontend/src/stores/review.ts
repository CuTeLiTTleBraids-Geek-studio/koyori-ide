// Code review store (#27): orchestrates gathering the full git diff, calling
// the AI with the "review" preset, and exposing the result for the UI.
// Koyori IDE 模块 · Review；交互服务：Git 集成（GitService）、AI 对话（AIService）。
// 喵，这是 Koyori IDE 的 Review 模块（前端实现）~
import { reactive, computed } from "vue";
import { gitService, aiService } from "@/api/services";
import { rulesForPrompt } from "@/stores/rules";
import { pushOutput } from "@/stores/output";
import { notifyError, notifySuccess } from "@/lib/notifications";
import { errorMessage } from "@/lib/errors";

export interface ReviewState {
  // The raw markdown review text returned by the AI, or null when no review
  // has been run yet.
  result: string | null;
  // True while gathering diff or waiting for AI response.
  loading: boolean;
  // Error message from the last review attempt, or null.
  error: string | null;
  // List of file paths included in the reviewed diff (parsed from headers).
  reviewedFiles: string[];
  // Timestamp (ms) of the last successful review.
  reviewedAt: number | null;
}

export const reviewState = reactive<ReviewState>({
  result: null,
  loading: false,
  error: null,
  reviewedFiles: [],
  reviewedAt: null,
});

export const hasReview = computed(
  () => reviewState.result !== null && reviewState.result.length > 0,
);

/**
 * Parses `=== filePath ===` headers from the combined diff to list which
 * files were included in the review.
 */
function parseReviewedFiles(diff: string): string[] {
  const files: string[] = [];
  const re = /^=== (.+?) ===$/gm;
  let m: RegExpExecArray | null;
  while ((m = re.exec(diff)) !== null) {
    files.push(m[1]);
  }
  return files;
}

/**
 * Runs an AI code review on all uncommitted changes in the project.
 * Fetches the full diff, prepends the "review" preset instruction, sends
 * it to the AI, and stores the markdown response.
 */
export async function runReview(projectRoot: string): Promise<void> {
  if (!projectRoot) {
    reviewState.error = "No project open";
    notifyError("No project open");
    return;
  }
  reviewState.loading = true;
  reviewState.error = null;
  try {
    // 1. Gather the full diff
    const diff = await gitService.getFullDiff(projectRoot);
    if (!diff || diff.trim().length === 0) {
      reviewState.result = null;
      reviewState.reviewedFiles = [];
      reviewState.reviewedAt = null;
      reviewState.error = "No changes to review";
      pushOutput("review", "info", "Code review skipped: no changes");
      return;
    }
    reviewState.reviewedFiles = parseReviewedFiles(diff);
    pushOutput(
      "review",
      "info",
      `Reviewing ${reviewState.reviewedFiles.length} file(s): ${reviewState.reviewedFiles.join(", ")}`,
    );

    // 2. Fetch the "review" preset instruction
    let instruction: string;
    try {
      instruction = await aiService.getPresetPrompt("review");
    } catch {
      instruction = "Review this code as a senior engineer. Format as a list of findings with severity (Critical/Warning/Suggestion).";
    }

    // 3. Build the user message with the diff in a fenced block
    const userContent = `${instruction}

# Git Diff Under Review

\`\`\`diff
${diff}
\`\`\``;

    // 4. Send to AI (non-streaming, separate from chat conversation)
    const systemPrompt =
      "You are Koyori IDE Code Reviewer, an expert AI engineer that reviews git diffs." +
      rulesForPrompt.value;
    const response = await aiService.send([
      { role: "system", content: systemPrompt },
      { role: "user", content: userContent },
    ]);

    if (response && response.Content) {
      reviewState.result = response.Content;
      reviewState.reviewedAt = Date.now();
      reviewState.error = null;
      pushOutput("review", "info", "Code review completed");
      notifySuccess("Code review completed");
    } else {
      reviewState.result = null;
      reviewState.reviewedAt = null;
      reviewState.error = "AI returned an empty response";
      notifyError("Code review returned an empty response");
    }
  } catch (e: unknown) {
    const msg = errorMessage(e);
    reviewState.result = null;
    reviewState.reviewedAt = null;
    reviewState.error = msg;
    notifyError(`Code review failed: ${msg}`);
    pushOutput("review", "error", `Code review failed: ${msg}`);
  } finally {
    reviewState.loading = false;
  }
}

/**
 * Clears the review state.
 */
export function clearReview(): void {
  reviewState.result = null;
  reviewState.loading = false;
  reviewState.error = null;
  reviewState.reviewedFiles = [];
  reviewState.reviewedAt = null;
}
