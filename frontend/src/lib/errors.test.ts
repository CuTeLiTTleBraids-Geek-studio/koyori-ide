import { describe, it, expect } from "vitest";
import { errorMessage, isCancellationError } from "./errors";

describe("errorMessage", () => {
  it("returns the message of an Error instance", () => {
    expect(errorMessage(new Error("boom"))).toBe("boom");
  });

  it("returns the message of a subclass of Error", () => {
    expect(errorMessage(new TypeError("not a function"))).toBe("not a function");
  });

  it("returns strings as-is", () => {
    expect(errorMessage("plain string")).toBe("plain string");
  });

  it("returns the empty string for ''", () => {
    expect(errorMessage("")).toBe("");
  });

  it("coerces numbers to strings", () => {
    expect(errorMessage(42)).toBe("42");
  });

  it("coerces booleans to strings", () => {
    expect(errorMessage(true)).toBe("true");
    expect(errorMessage(false)).toBe("false");
  });

  it("coerces null to 'null'", () => {
    expect(errorMessage(null)).toBe("null");
  });

  it("coerces undefined to 'undefined'", () => {
    expect(errorMessage(undefined)).toBe("undefined");
  });

  it("coerces plain objects via String()", () => {
    expect(errorMessage({ foo: 1 })).toBe("[object Object]");
  });

  it("coerces arrays via String()", () => {
    expect(errorMessage([1, 2, 3])).toBe("1,2,3");
  });

  it("preserves a custom Error subclass message", () => {
    class MyErr extends Error {
      constructor(msg: string) {
        super(msg);
        this.name = "MyErr";
      }
    }
    expect(errorMessage(new MyErr("custom"))).toBe("custom");
  });

  it("falls back when String() throws (defensive)", () => {
    // An object whose toString throws — String() will propagate, but the
    // helper's try/catch returns the fallback.
    const tricky = {
      toString() {
        throw new Error("nope");
      },
    };
    expect(errorMessage(tricky)).toBe("(unknown error)");
  });
});

describe("isCancellationError", () => {
  it("detects ElMessageBox 'cancel' string", () => {
    expect(isCancellationError("cancel")).toBe(true);
  });

  it("detects ElMessageBox 'close' string", () => {
    expect(isCancellationError("close")).toBe(true);
  });

  it("detects ElMessageBox 'esc' string", () => {
    expect(isCancellationError("esc")).toBe(true);
  });

  it("detects Wails RuntimeError with 'cancelled by user' message", () => {
    const err = new Error('{"message":"cancelled by user","cause":{},"kind":"RuntimeError"}');
    expect(isCancellationError(err)).toBe(true);
  });

  it("detects plain 'cancelled by user' string", () => {
    expect(isCancellationError("cancelled by user")).toBe(true);
  });

  it("detects 'context canceled' in error message", () => {
    expect(isCancellationError(new Error("rpc error: context canceled"))).toBe(true);
  });

  it("returns false for real errors", () => {
    expect(isCancellationError(new Error("disk full"))).toBe(false);
  });

  it("returns false for non-cancellation strings", () => {
    expect(isCancellationError("something else")).toBe(false);
  });

  it("returns false for null/undefined", () => {
    expect(isCancellationError(null)).toBe(false);
    expect(isCancellationError(undefined)).toBe(false);
  });
});
