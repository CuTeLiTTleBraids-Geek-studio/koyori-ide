// Koyori IDE 模块 · Coverage；交互服务：覆盖率（CoverageService）。
// 喵，这是 Koyori IDE 的 Coverage 模块（前端实现）~
import { reactive } from "vue";
import { coverageService } from "@/api/services";
import { appState } from "@/stores/app";
import { pushOutput } from "@/stores/output";
import { notifyError, notifySuccess } from "@/lib/notifications";

export type CoverageStatus = "covered" | "uncovered" | "partial";

export interface CoverageLineHit {
  file: string;
  line: number;
  covered: boolean;
  status?: CoverageStatus;
  coveredCount?: number;
  totalCount?: number;
}

export interface CoverageMetric {
  total: number;
  covered: number;
  skipped: number;
  pct: number;
}

export interface CoverageFileReport {
  file: string;
  statements: CoverageMetric;
  branches: CoverageMetric;
  functions: CoverageMetric;
  lines: CoverageMetric;
  hits: CoverageLineHit[];
}

export interface CoverageReport {
  format: "coverage-final" | "json-summary" | string;
  files: CoverageFileReport[];
  statements: CoverageMetric;
  branches: CoverageMetric;
  functions: CoverageMetric;
  lines: CoverageMetric;
}

interface VitestCoverageResult {
  success: boolean;
  output?: string;
  report?: CoverageReport | null;
  notInstalled?: boolean;
  timedOut?: boolean;
  cancelled?: boolean;
  durationMs?: number;
}

export const coverageState = reactive({
  hits: [] as CoverageLineHit[],
  loading: false,
  lastOutput: "",
  profile: "",
  report: null as CoverageReport | null,
  vitestEnabled: true,
});

let vitestRunSequence = 0;
let vitestRunController: AbortController | null = null;

export function normalizeCoveragePath(p: string): string {
  if (!p) return "";
  let s = p.replace(/\\/g, "/");
  s = s.replace(/^\.\//, "");
  s = s.replace(/([^:])\/+/g, "$1/");
  return s;
}

export function coveragePathsMatch(hitPath: string, editorPath: string): boolean {
  const h = normalizeCoveragePath(hitPath);
  const e = normalizeCoveragePath(editorPath);
  if (!h || !e) return false;
  if (h.toLowerCase() === e.toLowerCase()) return true;
  const hParts = h.split("/").filter(Boolean);
  const eParts = e.split("/").filter(Boolean);
  if (hParts.length === 1 || eParts.length === 1) {
    return hParts.length === 1 && eParts.length === 1 && h.toLowerCase() === e.toLowerCase();
  }
  const hl = h.toLowerCase();
  const el = e.toLowerCase();
  if (el.endsWith("/" + hl) || hl.endsWith("/" + el)) return true;
  return (
    hParts[hParts.length - 1].toLowerCase() === eParts[eParts.length - 1].toLowerCase() &&
    hParts[hParts.length - 2].toLowerCase() === eParts[eParts.length - 2].toLowerCase()
  );
}

export function lineCoverageStatus(hit: CoverageLineHit): CoverageStatus {
  if (hit.status === "covered" || hit.status === "uncovered" || hit.status === "partial") {
    return hit.status;
  }
  if (typeof hit.coveredCount === "number" && typeof hit.totalCount === "number" && hit.totalCount > 0) {
    if (hit.coveredCount <= 0) return "uncovered";
    if (hit.coveredCount < hit.totalCount) return "partial";
    return "covered";
  }
  return hit.covered ? "covered" : "uncovered";
}

export function coverageHitsForFile(filePath: string): CoverageLineHit[] {
  if (!filePath) return [];
  return coverageState.hits
    .filter((hit) => coveragePathsMatch(hit.file, filePath))
    .map((hit) => ({ ...hit, status: lineCoverageStatus(hit) }))
    .sort((a, b) => a.line - b.line);
}

export function applyCoverageReport(report: CoverageReport): void {
  coverageState.report = report;
  coverageState.hits = (report.files || []).flatMap((file) =>
    (file.hits || []).map((hit) => ({
      ...hit,
      file: normalizeCoveragePath(hit.file || file.file),
      status: lineCoverageStatus(hit),
    })),
  );
}

export async function runPackageCoverage(): Promise<void> {
  const dir = appState.currentProject || "";
  if (!dir) {
    notifyError("Open a project first");
    return;
  }
  coverageState.loading = true;
  try {
    const result = await coverageService.runPackageCoverage(dir);
    coverageState.lastOutput = result.output || "";
    coverageState.profile = result.profile || "";
    coverageState.report = null;
    coverageState.hits = (result.hits || []).map((hit) => ({
      file: normalizeCoveragePath(hit.file),
      line: hit.line,
      covered: hit.covered,
      status: hit.covered ? "covered" : "uncovered",
    }));
    pushOutput("Coverage", result.success ? "info" : "warn", result.output || "coverage done");
    notifySuccess(`Coverage: ${coverageState.hits.length} line hits`);
  } catch (error) {
    notifyError(error instanceof Error ? error.message : String(error));
  } finally {
    coverageState.loading = false;
  }
}

export function parseLcovToHits(lcovText: string): CoverageLineHit[] {
  const hits: CoverageLineHit[] = [];
  let file = "";
  for (const line of lcovText.split(/\r?\n/)) {
    if (line.startsWith("SF:")) {
      file = normalizeCoveragePath(line.slice(3).trim());
    } else if (line.startsWith("DA:") && file) {
      const [lineText, countText] = line.slice(3).split(",");
      const lineNumber = Number.parseInt(lineText, 10);
      const count = Number.parseInt(countText, 10);
      if (lineNumber > 0) {
        const covered = count > 0;
        hits.push({ file, line: lineNumber, covered, status: covered ? "covered" : "uncovered" });
      }
    } else if (line.startsWith("end_of_record")) {
      file = "";
    }
  }
  return hits;
}

export function cancelVitestCoverage(): void {
  vitestRunSequence += 1;
  vitestRunController?.abort();
  vitestRunController = null;
  coverageState.loading = false;
}

export async function runVitestCoverage(): Promise<void> {
  if (!coverageState.vitestEnabled) {
    notifyError("Vitest coverage gutter is disabled");
    return;
  }
  const dir = appState.currentProject || "";
  if (!dir) {
    notifyError("Open a project first");
    return;
  }

  vitestRunController?.abort();
  const controller = new AbortController();
  vitestRunController = controller;
  const runSequence = ++vitestRunSequence;
  coverageState.loading = true;
  try {
    const result = await coverageService.runVitestCoverage(
      dir,
      300,
      controller.signal,
    ) as VitestCoverageResult;
    if (runSequence !== vitestRunSequence) return;

    coverageState.lastOutput = result.output || "";
    pushOutput("Coverage", result.success ? "info" : "warn", result.output || "Vitest coverage finished");
    if (result.success && result.report) {
      applyCoverageReport(result.report);
      notifySuccess(`Vitest coverage: ${coverageState.hits.length} line hits`);
    } else {
      notifyError(result.output || "Vitest coverage did not produce an Istanbul JSON report");
    }
  } catch (error) {
    if (runSequence !== vitestRunSequence || controller.signal.aborted) return;
    notifyError(error instanceof Error ? error.message : String(error));
  } finally {
    if (runSequence === vitestRunSequence) {
      coverageState.loading = false;
      vitestRunController = null;
    }
  }
}

export function clearCoverage(): void {
  coverageState.hits = [];
  coverageState.profile = "";
  coverageState.report = null;
}
