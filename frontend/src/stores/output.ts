// Koyori IDE 模块 · Output。
// 喵，这是 Koyori IDE 的 Output 模块（前端实现）~
import { reactive } from "vue";

export type OutputSeverity = "info" | "warn" | "error" | "success";

export interface OutputEntry {
  id: string;
  timestamp: number;
  source: string;
  severity: OutputSeverity;
  message: string;
}

export type ProblemSeverity = "error" | "warning" | "info" | "hint";

export interface ProblemEntry {
  id: string;
  severity: ProblemSeverity;
  file: string;
  line: number;
  column: number;
  message: string;
  source?: string;
}

interface OutputStoreState {
  outputs: OutputEntry[];
  problems: ProblemEntry[];
  maxOutputs: number;
  maxProblems: number;
}

export const outputState = reactive<OutputStoreState>({
  outputs: [],
  problems: [],
  maxOutputs: 500,
  // Cap problems to prevent rendering lag on large-scale lint runs (#25 / N-15).
  maxProblems: 1000,
});

let counter = 0;
function nextId(): string {
  counter += 1;
  return `o${Date.now()}-${counter}`;
}

export function pushOutput(
  source: string,
  severity: OutputSeverity,
  message: string,
): string {
  const id = nextId();
  outputState.outputs.push({
    id,
    timestamp: Date.now(),
    source,
    severity,
    message,
  });
  if (outputState.outputs.length > outputState.maxOutputs) {
    outputState.outputs.splice(
      0,
      outputState.outputs.length - outputState.maxOutputs,
    );
  }
  return id;
}

export function clearOutputs(): void {
  outputState.outputs = [];
}

export function pushProblem(
  severity: ProblemSeverity,
  file: string,
  line: number,
  column: number,
  message: string,
  source?: string,
): string {
  const id = nextId();
  outputState.problems.push({
    id,
    severity,
    file,
    line,
    column,
    message,
    source,
  });
  // Trim oldest entries when over the cap (#25 / N-15).
  if (outputState.problems.length > outputState.maxProblems) {
    outputState.problems.splice(
      0,
      outputState.problems.length - outputState.maxProblems,
    );
  }
  return id;
}

export function clearProblems(): void {
  outputState.problems = [];
}

export function clearAll(): void {
  clearOutputs();
  clearProblems();
}

export const problemCounts = (): {
  error: number;
  warning: number;
  info: number;
  hint: number;
} => {
  let error = 0;
  let warning = 0;
  let info = 0;
  let hint = 0;
  for (const p of outputState.problems) {
    switch (p.severity) {
      case "error":
        error += 1;
        break;
      case "warning":
        warning += 1;
        break;
      case "info":
        info += 1;
        break;
      case "hint":
        hint += 1;
        break;
    }
  }
  return { error, warning, info, hint };
};
