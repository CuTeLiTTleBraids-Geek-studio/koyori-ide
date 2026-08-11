import { describe, it, expect, beforeEach } from "vitest";
import {
  outputState,
  pushOutput,
  pushProblem,
  clearOutputs,
  clearProblems,
  clearAll,
  problemCounts,
} from "./output";

describe("output store", () => {
  beforeEach(() => {
    clearAll();
  });

  describe("pushOutput", () => {
    it("appends an entry to outputs", () => {
      pushOutput("git", "info", "Pulling from origin");
      expect(outputState.outputs).toHaveLength(1);
      expect(outputState.outputs[0].source).toBe("git");
      expect(outputState.outputs[0].severity).toBe("info");
      expect(outputState.outputs[0].message).toBe("Pulling from origin");
    });

    it("assigns a unique id and timestamp", () => {
      const id1 = pushOutput("a", "info", "first");
      const id2 = pushOutput("a", "info", "second");
      expect(id1).not.toBe(id2);
      expect(outputState.outputs[0].timestamp).toBeGreaterThan(0);
    });

    it("trims oldest entries when exceeding maxOutputs", () => {
      const original = outputState.maxOutputs;
      outputState.maxOutputs = 3;
      try {
        pushOutput("s", "info", "1");
        pushOutput("s", "info", "2");
        pushOutput("s", "info", "3");
        pushOutput("s", "info", "4");
        expect(outputState.outputs).toHaveLength(3);
        expect(outputState.outputs[0].message).toBe("2");
        expect(outputState.outputs[2].message).toBe("4");
      } finally {
        outputState.maxOutputs = original;
      }
    });
  });

  describe("pushProblem", () => {
    it("appends a problem entry", () => {
      pushProblem("error", "src/main.ts", 12, 5, "Type mismatch", "tsc");
      expect(outputState.problems).toHaveLength(1);
      const p = outputState.problems[0];
      expect(p.severity).toBe("error");
      expect(p.file).toBe("src/main.ts");
      expect(p.line).toBe(12);
      expect(p.column).toBe(5);
      expect(p.message).toBe("Type mismatch");
      expect(p.source).toBe("tsc");
    });

    it("allows undefined source", () => {
      pushProblem("warning", "a.ts", 1, 1, "Unused var");
      expect(outputState.problems[0].source).toBeUndefined();
    });

    it("trims oldest entries when exceeding maxProblems (N-15)", () => {
      const original = outputState.maxProblems;
      outputState.maxProblems = 3;
      try {
        pushProblem("error", "a.ts", 1, 1, "1");
        pushProblem("error", "a.ts", 2, 1, "2");
        pushProblem("error", "a.ts", 3, 1, "3");
        pushProblem("error", "a.ts", 4, 1, "4");
        expect(outputState.problems).toHaveLength(3);
        expect(outputState.problems[0].message).toBe("2");
        expect(outputState.problems[2].message).toBe("4");
      } finally {
        outputState.maxProblems = original;
      }
    });

    it("default maxProblems is 1000 (N-15)", () => {
      expect(outputState.maxProblems).toBe(1000);
    });
  });

  describe("clearOutputs", () => {
    it("removes all outputs but keeps problems", () => {
      pushOutput("s", "info", "x");
      pushProblem("error", "a.ts", 1, 1, "boom");
      clearOutputs();
      expect(outputState.outputs).toHaveLength(0);
      expect(outputState.problems).toHaveLength(1);
    });
  });

  describe("clearProblems", () => {
    it("removes all problems but keeps outputs", () => {
      pushOutput("s", "info", "x");
      pushProblem("error", "a.ts", 1, 1, "boom");
      clearProblems();
      expect(outputState.problems).toHaveLength(0);
      expect(outputState.outputs).toHaveLength(1);
    });
  });

  describe("clearAll", () => {
    it("removes both outputs and problems", () => {
      pushOutput("s", "info", "x");
      pushProblem("error", "a.ts", 1, 1, "boom");
      clearAll();
      expect(outputState.outputs).toHaveLength(0);
      expect(outputState.problems).toHaveLength(0);
    });
  });

  describe("problemCounts", () => {
    it("counts problems by severity", () => {
      pushProblem("error", "a.ts", 1, 1, "e1");
      pushProblem("error", "b.ts", 2, 1, "e2");
      pushProblem("warning", "c.ts", 3, 1, "w1");
      pushProblem("info", "d.ts", 4, 1, "i1");
      pushProblem("hint", "e.ts", 5, 1, "h1");
      const counts = problemCounts();
      expect(counts).toEqual({ error: 2, warning: 1, info: 1, hint: 1 });
    });

    it("returns zeros when no problems", () => {
      expect(problemCounts()).toEqual({
        error: 0,
        warning: 0,
        info: 0,
        hint: 0,
      });
    });
  });
});
