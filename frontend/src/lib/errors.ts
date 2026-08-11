/**
 * errorMessage coerces a caught value (typed as `unknown` under strict
 * TypeScript catch variables) into a human-readable string. This is the
 * shared helper for the `catch (e: unknown)` refactor (N-4) — every
 * catch site that previously did `e?.message ?? String(e)` should call
 * this instead, so the narrowing lives in one place.
 *
 * Behavior:
 *   - Error instances → their .message
 *   - strings → returned as-is
 *   - anything else → String(value), which handles numbers, objects, etc.
 *
 * The function never throws: if .message access fails for any reason
 * (e.g. a malformed object throwing in a getter), it falls back to
 * String(e).
 */
// Koyori IDE 模块 · Errors。
// 喵，这是 Koyori IDE 的 Errors 模块（前端实现）~
export function errorMessage(e: unknown): string {
  if (e instanceof Error) {
    return e.message;
  }
  if (typeof e === "string") {
    return e;
  }
  try {
    return String(e);
  } catch {
    return "(unknown error)";
  }
}

/**
 * Returns true when the caught value represents a user-initiated
 * cancellation that should be silently ignored (not surfaced as an error).
 *
 * Covers three distinct cancellation shapes:
 *
 * 1. ElMessageBox rejection strings — Element Plus's ElMessageBox.prompt/
 *    confirm rejects with the bare strings "cancel", "close", or "esc"
 *    when the user dismisses the dialog.
 *
 * 2. Wails RuntimeError — when a native dialog (e.g. PickDirectory) is
 *    cancelled, Wails rejects with an Error whose .message is a JSON
 *    string like:
 *      {"message":"cancelled by user","cause":{},"kind":"RuntimeError"}
 *    The message may also be the plain string "cancelled by user".
 *
 * 3. Go context cancellation — backend calls cancelled via context
 *    surface as errors containing "context canceled".
 *
 * Without this filter, every file-picker cancel or dialog dismiss would
 * produce a confusing error notification for the user.
 */
export function isCancellationError(e: unknown): boolean {
  // ElMessageBox bare-string rejections.
  if (e === "cancel" || e === "close" || e === "esc") {
    return true;
  }
  const msg = errorMessage(e);
  if (!msg) return false;
  // Wails RuntimeError (JSON or plain) and Go context cancellation.
  return (
    msg.includes("cancelled by user") ||
    msg.includes("context canceled") ||
    msg.includes("user canceled")
  );
}
