import { beforeEach, describe, expect, it, vi } from "vitest";

const { coverageServiceMock, appStateMock, notifyErrorMock } = vi.hoisted(() => ({
  coverageServiceMock: {
    runPackageCoverage: vi.fn(),
    runVitestCoverage: vi.fn(),
  },
  appStateMock: { currentProject: "C:/workspace" as string | null },
  notifyErrorMock: vi.fn(),
}));

vi.mock("@/api/services", () => ({ coverageService: coverageServiceMock }));
vi.mock("@/stores/app", () => ({ appState: appStateMock }));
vi.mock("@/stores/output", () => ({ pushOutput: vi.fn() }));
vi.mock("@/lib/notifications", () => ({
  notifyError: notifyErrorMock,
  notifySuccess: vi.fn(),
}));

import {
  applyCoverageReport,
  cancelVitestCoverage,
  clearCoverage,
  coverageHitsForFile,
  coveragePathsMatch,
  coverageState,
  lineCoverageStatus,
  normalizeCoveragePath,
  parseLcovToHits,
  runVitestCoverage,
} from "./coverage";

describe("coverage path match", () => {
  it("normalizes slashes", () => {
    expect(normalizeCoveragePath(`pkg\\a\\foo.go`).includes("\\")).toBe(false);
  });

  it("matches package-relative suffix without basename collision", () => {
    expect(coveragePathsMatch("pkg/a/foo.go", "E:/proj/pkg/a/foo.go")).toBe(true);
    expect(coveragePathsMatch("pkg/a/foo.go", "E:/proj/pkg/b/foo.go")).toBe(false);
    expect(coveragePathsMatch("foo.go", "E:/proj/pkg/a/foo.go")).toBe(false);
  });

  it("parses legacy lcov hits", () => {
    const hits = parseLcovToHits("SF:src/x.ts\nDA:1,1\nDA:2,0\nend_of_record\n");
    expect(hits).toHaveLength(2);
    expect(lineCoverageStatus(hits[0])).toBe("covered");
    expect(lineCoverageStatus(hits[1])).toBe("uncovered");
  });
});

describe("Istanbul coverage state", () => {
  beforeEach(() => {
    cancelVitestCoverage();
    clearCoverage();
    coverageServiceMock.runVitestCoverage.mockReset();
    notifyErrorMock.mockReset();
    appStateMock.currentProject = "C:/workspace";
  });

  it("keeps covered, uncovered, and partial line status", () => {
    applyCoverageReport({
      format: "coverage-final",
      statements: { total: 2, covered: 1, skipped: 0, pct: 50 },
      branches: { total: 2, covered: 1, skipped: 0, pct: 50 },
      functions: { total: 1, covered: 1, skipped: 0, pct: 100 },
      lines: { total: 2, covered: 1, skipped: 0, pct: 50 },
      files: [{
        file: "src/example.ts",
        statements: { total: 2, covered: 1, skipped: 0, pct: 50 },
        branches: { total: 2, covered: 1, skipped: 0, pct: 50 },
        functions: { total: 1, covered: 1, skipped: 0, pct: 100 },
        lines: { total: 2, covered: 1, skipped: 0, pct: 50 },
        hits: [
          { file: "src/example.ts", line: 1, covered: true, status: "covered", coveredCount: 2, totalCount: 2 },
          { file: "src/example.ts", line: 2, covered: false, status: "uncovered", coveredCount: 0, totalCount: 1 },
          { file: "src/example.ts", line: 3, covered: true, status: "partial", coveredCount: 1, totalCount: 2 },
        ],
      }],
    });

    const hits = coverageHitsForFile("C:/workspace/src/example.ts");
    expect(hits.map(lineCoverageStatus)).toEqual(["covered", "uncovered", "partial"]);
    expect(coverageState.report?.format).toBe("coverage-final");
  });

  it("loads the structured report through CoverageService", async () => {
    coverageServiceMock.runVitestCoverage.mockResolvedValue({
      success: true,
      output: "ok",
      report: {
        format: "coverage-final",
        statements: { total: 1, covered: 1, skipped: 0, pct: 100 },
        branches: { total: 0, covered: 0, skipped: 0, pct: 0 },
        functions: { total: 0, covered: 0, skipped: 0, pct: 0 },
        lines: { total: 1, covered: 1, skipped: 0, pct: 100 },
        files: [{
          file: "src/run.ts",
          statements: { total: 1, covered: 1, skipped: 0, pct: 100 },
          branches: { total: 0, covered: 0, skipped: 0, pct: 0 },
          functions: { total: 0, covered: 0, skipped: 0, pct: 0 },
          lines: { total: 1, covered: 1, skipped: 0, pct: 100 },
          hits: [{ file: "src/run.ts", line: 4, covered: true, status: "covered" }],
        }],
      },
    });

    await runVitestCoverage();

    expect(coverageServiceMock.runVitestCoverage).toHaveBeenCalledWith(
      "C:/workspace",
      300,
      expect.any(AbortSignal),
    );
    expect(coverageHitsForFile("C:/workspace/src/run.ts")).toMatchObject([
      { line: 4, status: "covered" },
    ]);
    expect(coverageState.loading).toBe(false);
  });

  it("cancels an in-flight run without applying a late result", async () => {
    let rejectFirst!: (reason: Error) => void;
    coverageServiceMock.runVitestCoverage
      .mockImplementationOnce((_root: string, _timeout: number, signal: AbortSignal) =>
        new Promise((_resolve, reject) => {
          rejectFirst = reject;
          signal.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError")), { once: true });
        }),
      )
      .mockResolvedValueOnce({ success: false, output: "second run", report: null });

    const first = runVitestCoverage();
    const second = runVitestCoverage();
    await Promise.all([first, second]);

    expect(rejectFirst).toBeTypeOf("function");
    expect(coverageState.lastOutput).toBe("second run");
    expect(notifyErrorMock).toHaveBeenCalledTimes(1);
  });
});
