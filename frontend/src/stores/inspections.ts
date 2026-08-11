// Koyori IDE 模块 · Inspections；交互服务：文件系统（FileService）、离线 LSP（LSPService）。
// 喵，这是 Koyori IDE 的 Inspections 模块（前端实现）~
import { reactive } from "vue";
import { fileService, lspService } from "@/api/services";
import { errorMessage } from "@/lib/errors";
import { detectLanguage } from "@/lib/language";
import { matchesStructuralFileGlobs } from "@/lib/structuralSearch";
import { editorState, openFileFromPath, updateContent } from "@/stores/editor";
import {
  diagnosticServerLanguages,
  ensureLSPRunning,
  getLSPCodeActions,
  monacoLanguageToLSP,
} from "@/stores/lsp";
import type { LSPTextEdit } from "@/types";

export type InspectionSeverity = 1 | 2 | 3 | 4;

export interface InspectionSourceRule {
  enabled: boolean;
  severity?: InspectionSeverity;
}

export interface InspectionProfile {
  id: string;
  name: string;
  enabled: boolean;
  severityThreshold: InspectionSeverity;
  includeGlobs: string[];
  excludeGlobs: string[];
  sourceRules: Record<string, InspectionSourceRule>;
}

export interface InspectionFinding {
  id: string;
  path: string;
  filePath: string;
  language: string;
  server: string;
  line: number;
  column: number;
  endLine: number;
  endColumn: number;
  severity: InspectionSeverity;
  message: string;
  source: string;
  ruleId: string;
  documentContent: string;
}

export interface InspectionCodeAction {
  findingId: string;
  title: string;
  kind?: string;
  command?: string;
  tooltip?: string;
  edit?: Array<{ filePath: string; edits: LSPTextEdit[] }> | null;
  isPreferred?: boolean;
  disabled?: boolean;
}

export interface InspectionQuickFixPreview {
  findingId: string;
  title: string;
  filePath: string;
  originalContent: string;
  modifiedContent: string;
}

interface InspectionState {
  workspaceRoot: string;
  profile: InspectionProfile;
  findings: InspectionFinding[];
  quickFixes: InspectionCodeAction[];
  quickFixPreview: InspectionQuickFixPreview | null;
  loading: boolean;
  quickFixLoading: boolean;
  applying: boolean;
  error: string | null;
  truncated: boolean;
  scannedFiles: number;
  skippedFiles: number;
}

export const MAX_INSPECTION_FILES = 2000;
export const MAX_INSPECTION_FINDINGS = 2000;
const INSPECTION_CONCURRENCY = 4;
const PROFILE_STORAGE_PREFIX = "koyori-ide.inspections.profile:";

function defaultInspectionProfile(): InspectionProfile {
  return {
    id: "project",
    name: "Project",
    enabled: true,
    severityThreshold: 3,
    includeGlobs: [],
    excludeGlobs: [],
    sourceRules: {},
  };
}

export const inspectionState = reactive<InspectionState>({
  workspaceRoot: "",
  profile: defaultInspectionProfile(),
  findings: [],
  quickFixes: [],
  quickFixPreview: null,
  loading: false,
  quickFixLoading: false,
  applying: false,
  error: null,
  truncated: false,
  scannedFiles: 0,
  skippedFiles: 0,
});

let inspectionGeneration = 0;

function normalizePath(path: string): string {
  return path.replace(/\\/g, "/").replace(/\/$/, "").toLowerCase();
}

function samePath(left: string, right: string): boolean {
  return normalizePath(left) === normalizePath(right);
}

function workspaceFilePath(root: string, relativePath: string): string {
  return `${root.replace(/[\\/]+$/, "")}/${relativePath.replace(/^[\\/]+/, "")}`;
}

function profileStorageKey(root: string): string {
  return `${PROFILE_STORAGE_PREFIX}${normalizePath(root)}`;
}

function validGlobs(value: unknown): string[] {
  if (!Array.isArray(value)) return [];
  return value
    .filter((item): item is string => typeof item === "string")
    .map((item) => item.trim())
    .filter((item) => item.length > 0 && item.length <= 1024)
    .slice(0, 64);
}

function validSeverity(value: unknown, fallback: InspectionSeverity): InspectionSeverity {
  return value === 1 || value === 2 || value === 3 || value === 4 ? value : fallback;
}

function safeProfile(value: unknown): InspectionProfile {
  const fallback = defaultInspectionProfile();
  if (!value || typeof value !== "object") return fallback;
  const input = value as Partial<InspectionProfile>;
  const sourceRules: Record<string, InspectionSourceRule> = {};
  if (input.sourceRules && typeof input.sourceRules === "object") {
    for (const [source, rawRule] of Object.entries(input.sourceRules).slice(0, 128)) {
      if (!source || source.length > 128 || !rawRule || typeof rawRule !== "object") continue;
      const rule = rawRule as Partial<InspectionSourceRule>;
      const normalized = source.trim().toLowerCase();
      if (!normalized) continue;
      sourceRules[normalized] = {
        enabled: rule.enabled !== false,
        ...(rule.severity == null ? {} : { severity: validSeverity(rule.severity, 2) }),
      };
    }
  }
  return {
    id: "project",
    name: typeof input.name === "string" && input.name.trim()
      ? input.name.trim().slice(0, 80)
      : fallback.name,
    enabled: input.enabled !== false,
    severityThreshold: validSeverity(input.severityThreshold, fallback.severityThreshold),
    includeGlobs: validGlobs(input.includeGlobs),
    excludeGlobs: validGlobs(input.excludeGlobs),
    sourceRules,
  };
}

function persistInspectionProfile(): void {
  if (!inspectionState.workspaceRoot) return;
  try {
    localStorage.setItem(
      profileStorageKey(inspectionState.workspaceRoot),
      JSON.stringify(inspectionState.profile),
    );
  } catch {
    // Profile persistence is best-effort; the active in-memory profile remains usable.
  }
}

function loadInspectionProfile(root: string): InspectionProfile {
  try {
    const raw = localStorage.getItem(profileStorageKey(root));
    return raw ? safeProfile(JSON.parse(raw) as unknown) : defaultInspectionProfile();
  } catch {
    return defaultInspectionProfile();
  }
}

function normalizeSeverity(value: number): InspectionSeverity {
  return validSeverity(value, 2);
}

function sourceRuleId(source: string): string {
  return (source.trim().toLowerCase() || "lsp").slice(0, 128);
}

function profileAllows(source: string, severity: InspectionSeverity): InspectionSeverity | null {
  const rule = inspectionState.profile.sourceRules[sourceRuleId(source)];
  if (rule?.enabled === false) return null;
  const effective = rule?.severity ?? severity;
  return effective <= inspectionState.profile.severityThreshold ? effective : null;
}

async function currentDocumentContent(filePath: string): Promise<string> {
  const open = editorState.openFiles.find((file) => samePath(file.path, filePath));
  return open?.content ?? fileService.readFile(filePath);
}

function findingById(id: string): InspectionFinding | null {
  return inspectionState.findings.find((finding) => finding.id === id) ?? null;
}

export function setInspectionWorkspace(root: string): void {
  inspectionGeneration += 1;
  inspectionState.workspaceRoot = root;
  inspectionState.profile = root ? loadInspectionProfile(root) : defaultInspectionProfile();
  inspectionState.findings = [];
  inspectionState.quickFixes = [];
  inspectionState.quickFixPreview = null;
  inspectionState.loading = false;
  inspectionState.quickFixLoading = false;
  inspectionState.error = null;
  inspectionState.truncated = false;
  inspectionState.scannedFiles = 0;
  inspectionState.skippedFiles = 0;
}

export function updateInspectionProfile(update: Partial<Omit<InspectionProfile, "id" | "sourceRules">>): void {
  inspectionState.profile = safeProfile({
    ...inspectionState.profile,
    ...update,
    sourceRules: inspectionState.profile.sourceRules,
  });
  persistInspectionProfile();
}

export function setInspectionSourceEnabled(source: string, enabled: boolean): void {
  const ruleId = sourceRuleId(source);
  inspectionState.profile.sourceRules[ruleId] = {
    ...inspectionState.profile.sourceRules[ruleId],
    enabled,
  };
  inspectionState.findings = inspectionState.findings.filter(
    (finding) => enabled || finding.ruleId !== ruleId,
  );
  persistInspectionProfile();
}

export function cancelInspectionRun(): void {
  inspectionGeneration += 1;
  inspectionState.loading = false;
}

export function clearInspectionState(): void {
  inspectionGeneration += 1;
  inspectionState.workspaceRoot = "";
  inspectionState.profile = defaultInspectionProfile();
  inspectionState.findings = [];
  inspectionState.quickFixes = [];
  inspectionState.quickFixPreview = null;
  inspectionState.loading = false;
  inspectionState.quickFixLoading = false;
  inspectionState.applying = false;
  inspectionState.error = null;
  inspectionState.truncated = false;
  inspectionState.scannedFiles = 0;
  inspectionState.skippedFiles = 0;
}

export async function runInspections(root: string): Promise<void> {
  if (root !== inspectionState.workspaceRoot) setInspectionWorkspace(root);
  const generation = ++inspectionGeneration;
  inspectionState.findings = [];
  inspectionState.quickFixes = [];
  inspectionState.quickFixPreview = null;
  inspectionState.error = null;
  inspectionState.truncated = false;
  inspectionState.scannedFiles = 0;
  inspectionState.skippedFiles = 0;
  if (!root || !inspectionState.profile.enabled) {
    inspectionState.loading = false;
    return;
  }

  inspectionState.loading = true;
  try {
    const files = await fileService.listAllFiles(root);
    if (generation !== inspectionGeneration) return;
    const candidates = files.flatMap((path) => {
      if (!matchesStructuralFileGlobs(
        path,
        inspectionState.profile.includeGlobs,
        inspectionState.profile.excludeGlobs,
      )) return [];
      const filePath = workspaceFilePath(root, path);
      const language = monacoLanguageToLSP(detectLanguage(path), filePath);
      if (!language || language === "eslint") return [];
      const servers = diagnosticServerLanguages(language, filePath);
      return servers.length ? [{ path, filePath, language, servers }] : [];
    });
    const servers = [...new Set(candidates.flatMap((candidate) => candidate.servers))];
    const availability = await Promise.all(servers.map(async (server) => [
      server,
      await ensureLSPRunning(server),
    ] as const));
    if (generation !== inspectionGeneration) return;
    const availableServers = new Set(
      availability.filter(([, available]) => available).map(([server]) => server),
    );
    const availableCandidates = candidates.flatMap((candidate) => {
      const routed = candidate.servers.filter((server) => availableServers.has(server));
      return routed.length ? [{ ...candidate, servers: routed }] : [];
    });
    const limited = availableCandidates.slice(0, MAX_INSPECTION_FILES);
    const byFile: InspectionFinding[][] = Array.from({ length: limited.length }, () => []);
    let nextIndex = 0;
    let skippedFiles = candidates.length - availableCandidates.length;

    async function worker(): Promise<void> {
      while (generation === inspectionGeneration) {
        const index = nextIndex;
        nextIndex += 1;
        if (index >= limited.length) return;
        const candidate = limited[index];
        try {
          const content = await currentDocumentContent(candidate.filePath);
          if (generation !== inspectionGeneration) return;
          const findings: InspectionFinding[] = [];
          let successfulServers = 0;
          for (const server of candidate.servers) {
            try {
              const diagnostics = await lspService.getDiagnostics({
                language: server,
                filePath: candidate.filePath,
                line: 0,
                column: 0,
                content,
              });
              successfulServers += 1;
              if (generation !== inspectionGeneration) return;
              diagnostics.forEach((diagnostic, diagnosticIndex) => {
                const source = diagnostic.source || server;
                const severity = profileAllows(source, normalizeSeverity(diagnostic.severity));
                if (severity == null) return;
                findings.push({
                  id: `${candidate.path}:${server}:${diagnostic.line}:${diagnostic.column}:${diagnosticIndex}`,
                  path: candidate.path,
                  filePath: candidate.filePath,
                  language: candidate.language,
                  server,
                  line: diagnostic.line,
                  column: diagnostic.column,
                  endLine: diagnostic.endLine,
                  endColumn: diagnostic.endColumn,
                  severity,
                  message: diagnostic.message,
                  source,
                  ruleId: sourceRuleId(source),
                  documentContent: content,
                });
              });
            } catch {
              // One optional server (for example ESLint) must not discard primary diagnostics.
            }
          }
          if (successfulServers === 0) skippedFiles += 1;
          byFile[index] = findings;
        } catch {
          skippedFiles += 1;
        }
      }
    }

    await Promise.all(Array.from(
      { length: Math.min(INSPECTION_CONCURRENCY, limited.length) },
      () => worker(),
    ));
    if (generation !== inspectionGeneration) return;
    const findings = byFile.flat().sort((left, right) => (
      left.path.localeCompare(right.path)
      || left.line - right.line
      || left.column - right.column
      || left.severity - right.severity
      || left.message.localeCompare(right.message)
    ));
    inspectionState.findings = findings.slice(0, MAX_INSPECTION_FINDINGS);
    inspectionState.truncated = availableCandidates.length > limited.length
      || findings.length > MAX_INSPECTION_FINDINGS;
    inspectionState.scannedFiles = limited.length;
    inspectionState.skippedFiles = skippedFiles;
  } catch (error: unknown) {
    if (generation === inspectionGeneration) inspectionState.error = errorMessage(error);
  } finally {
    if (generation === inspectionGeneration) inspectionState.loading = false;
  }
}

export async function loadInspectionQuickFixes(findingId: string): Promise<void> {
  const finding = findingById(findingId);
  inspectionState.quickFixes = [];
  inspectionState.quickFixPreview = null;
  inspectionState.error = null;
  if (!finding) return;
  inspectionState.quickFixLoading = true;
  try {
    const current = await currentDocumentContent(finding.filePath);
    if (current !== finding.documentContent) {
      throw new Error("document changed since inspection; run inspections again");
    }
    const actions = await getLSPCodeActions(
      finding.server,
      finding.filePath,
      finding.line,
      finding.column,
      current,
    );
    inspectionState.quickFixes = actions
      .filter((action) => !action.disabled && (!action.kind || action.kind.startsWith("quickfix")))
      .map((action) => ({ ...action, findingId }));
  } catch (error: unknown) {
    inspectionState.error = errorMessage(error);
  } finally {
    inspectionState.quickFixLoading = false;
  }
}

export async function previewInspectionQuickFix(findingId: string, actionIndex: number): Promise<void> {
  const finding = findingById(findingId);
  const action = inspectionState.quickFixes[actionIndex];
  inspectionState.quickFixPreview = null;
  inspectionState.error = null;
  if (!finding || !action || action.findingId !== findingId) return;
  try {
    const current = await currentDocumentContent(finding.filePath);
    if (current !== finding.documentContent) {
      throw new Error("document changed since inspection; run inspections again");
    }
    if (!action.edit || action.edit.length !== 1 || !samePath(action.edit[0].filePath, finding.filePath)) {
      throw new Error("this quick fix requires a multi-file or command edit; use the editor lightbulb");
    }
    const { applyTextEditsToContent } = await import("@/lib/lspCompletion");
    const modified = applyTextEditsToContent(current, action.edit[0].edits);
    inspectionState.quickFixPreview = {
      findingId,
      title: action.title,
      filePath: finding.filePath,
      originalContent: current,
      modifiedContent: modified,
    };
  } catch (error: unknown) {
    inspectionState.error = errorMessage(error);
  }
}

export function cancelInspectionQuickFix(): void {
  inspectionState.quickFixes = [];
  inspectionState.quickFixPreview = null;
  inspectionState.error = null;
}

export async function applyInspectionQuickFix(): Promise<boolean> {
  const preview = inspectionState.quickFixPreview;
  if (!preview) return false;
  inspectionState.applying = true;
  inspectionState.error = null;
  try {
    let open = editorState.openFiles.find((file) => samePath(file.path, preview.filePath));
    if (!open) {
      await openFileFromPath(preview.filePath);
      open = editorState.openFiles.find((file) => samePath(file.path, preview.filePath));
    }
    if (!open) throw new Error("quick-fix target could not be opened");
    if (open.content !== preview.originalContent) {
      throw new Error("document changed since quick-fix preview; preview the fix again");
    }
    if (!updateContent(preview.filePath, preview.modifiedContent)) {
      throw new Error("quick-fix target buffer could not be updated");
    }
    inspectionState.findings = inspectionState.findings.filter(
      (finding) => finding.id !== preview.findingId,
    );
    inspectionState.quickFixes = [];
    inspectionState.quickFixPreview = null;
    return true;
  } catch (error: unknown) {
    inspectionState.error = errorMessage(error);
    return false;
  } finally {
    inspectionState.applying = false;
  }
}
