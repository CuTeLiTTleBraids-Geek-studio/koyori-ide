// Koyori IDE 模块 · Refactor。
// 喵，这是 Koyori IDE 的 Refactor 模块（前端实现）~
import { reactive } from "vue";
import * as LSPServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/lspservice.js";
import { getLSPCodeActions } from "@/stores/lsp";
import { editorState, markSaved, updateContent } from "@/stores/editor";

export const REFACTOR_ONLY = [
  "refactor.extract",
  "refactor.inline",
  "refactor.rewrite",
] as const;

export type RefactorCommandID =
  | "extract-method"
  | "extract-variable"
  | "inline"
  | "move"
  | "change-signature"
  | "extract-interface"
  | "extract-superclass";

export interface RefactorRequest {
  language: string;
  filePath: string;
  line: number;
  column: number;
  endLine?: number;
  endColumn?: number;
  content: string;
}

export interface WorkspaceEditPreviewFile {
  filePath: string;
  version?: number;
  baselineHash: string;
  originalContent: string;
  modifiedContent: string;
}

export interface WorkspaceEditPreview {
  files: WorkspaceEditPreviewFile[];
}

export interface RefactorAction {
  title: string;
  kind?: string;
  command?: string;
  commandArguments?: unknown[];
  disabled?: boolean;
  disabledReason?: string;
  preview?: WorkspaceEditPreview;
}

interface RefactorState {
  loading: boolean;
  applying: boolean;
  error: string;
  request: RefactorRequest | null;
  available: Record<RefactorCommandID, boolean>;
  actions: Partial<Record<RefactorCommandID, RefactorAction>>;
  selectedCommand: RefactorCommandID | null;
  selectedAction: RefactorAction | null;
  previewVisible: boolean;
}

export const REFACTOR_COMMANDS: ReadonlyArray<{
  id: RefactorCommandID;
  labelKey: string;
}> = [
  { id: "extract-method", labelKey: "refactor.extractMethod" },
  { id: "extract-variable", labelKey: "refactor.extractVariable" },
  { id: "inline", labelKey: "refactor.inline" },
  { id: "move", labelKey: "refactor.move" },
  { id: "change-signature", labelKey: "refactor.changeSignature" },
  { id: "extract-interface", labelKey: "refactor.extractInterface" },
  { id: "extract-superclass", labelKey: "refactor.extractSuperclass" },
];

function emptyAvailability(): Record<RefactorCommandID, boolean> {
  return {
    "extract-method": false,
    "extract-variable": false,
    inline: false,
    move: false,
    "change-signature": false,
    "extract-interface": false,
    "extract-superclass": false,
  };
}

export const refactorState = reactive<RefactorState>({
  loading: false,
  applying: false,
  error: "",
  request: null,
  available: emptyAvailability(),
  actions: {},
  selectedCommand: null,
  selectedAction: null,
  previewVisible: false,
});

let requestGeneration = 0;
let applyGeneration = 0;
let activeApplyController: AbortController | null = null;

function actionText(action: RefactorAction): string {
  return `${action.kind ?? ""} ${action.title} ${action.command ?? ""}`.toLowerCase();
}

function matchesCommand(
  id: RefactorCommandID,
  action: RefactorAction,
): boolean {
  const text = actionText(action);
  switch (id) {
    case "extract-method":
      return (
        text.includes("refactor.extract") && /\b(method|function)\b/.test(text)
      );
    case "extract-variable":
      return (
        text.includes("refactor.extract") &&
        /\b(variable|constant)\b/.test(text)
      );
    case "inline":
      return text.includes("refactor.inline") || /\binline\b/.test(text);
    case "move":
      return (
        text.includes("refactor.move") ||
        (text.includes("refactor.rewrite") && /\bmove\b/.test(text))
      );
    case "change-signature":
      return (
        text.includes("refactor.rewrite") && /change[._ -]?signature/.test(text)
      );
    case "extract-interface":
      return text.includes("refactor.extract") && /\binterface\b/.test(text);
    case "extract-superclass":
      return (
        text.includes("refactor.extract") &&
        /\b(superclass|base[ _-]?class)\b/.test(text)
      );
  }
}

function isExecutableAction(action: RefactorAction): boolean {
  return (
    !action.disabled && (!!action.preview?.files.length || !!action.command)
  );
}

export async function refreshRefactorActions(
  request: RefactorRequest,
): Promise<void> {
  const generation = ++requestGeneration;
  refactorState.loading = true;
  refactorState.error = "";
  refactorState.request = { ...request };
  refactorState.available = emptyAvailability();
  refactorState.actions = {};
  try {
    const actions = (await getLSPCodeActions(
      request.language,
      request.filePath,
      request.line,
      request.column,
      request.content,
      {
        endLine: request.endLine,
        endColumn: request.endColumn,
        only: [...REFACTOR_ONLY],
      },
    )) as RefactorAction[];
    if (generation !== requestGeneration) return;
    for (const command of REFACTOR_COMMANDS) {
      const action = actions.find(
        (candidate) =>
          isExecutableAction(candidate) &&
          matchesCommand(command.id, candidate),
      );
      if (!action) continue;
      refactorState.available[command.id] = true;
      refactorState.actions[command.id] = action;
    }
  } catch (error) {
    if (generation !== requestGeneration) return;
    refactorState.error =
      error instanceof Error ? error.message : String(error);
  } finally {
    if (generation === requestGeneration) refactorState.loading = false;
  }
}

export function cancelRefactorRequest(): void {
  requestGeneration += 1;
  refactorState.loading = false;
  refactorState.available = emptyAvailability();
  refactorState.actions = {};
}

export function selectRefactorCommand(id: RefactorCommandID): boolean {
  const action = refactorState.actions[id];
  if (!refactorState.available[id] || !action) return false;
  refactorState.selectedCommand = id;
  refactorState.selectedAction = action;
  refactorState.previewVisible = true;
  refactorState.error = "";
  return true;
}

export function openRefactorActionPreview(
  request: RefactorRequest,
  action: RefactorAction,
): boolean {
  if (!isExecutableAction(action)) return false;
  requestGeneration += 1;
  refactorState.loading = false;
  refactorState.request = { ...request };
  refactorState.selectedCommand = null;
  refactorState.selectedAction = action;
  refactorState.previewVisible = true;
  refactorState.error = "";
  return true;
}

export function cancelRefactorPreview(): void {
  applyGeneration += 1;
  activeApplyController?.abort("refactor preview cancelled");
  activeApplyController = null;
  refactorState.applying = false;
  refactorState.previewVisible = false;
  refactorState.selectedCommand = null;
  refactorState.selectedAction = null;
  refactorState.error = "";
}

export async function applySelectedRefactor(): Promise<boolean> {
  const action = refactorState.selectedAction;
  const request = refactorState.request;
  if (!action || !request || refactorState.applying) return false;
  const generation = ++applyGeneration;
  const controller = new AbortController();
  activeApplyController = controller;
  refactorState.applying = true;
  refactorState.error = "";
  try {
    if (action.preview) {
      const call = LSPServiceBindings.ApplyRefactorWorkspaceEdit(
        request.language,
        action.preview,
      );
      if (typeof call.cancelOn === "function") call.cancelOn(controller.signal);
      const result = await call;
      if (generation !== applyGeneration) return false;
      if (!result.applied) {
        refactorState.error =
          result.conflicts?.join("\n") ||
          result.failureReason ||
          "Workspace edit was rejected";
        return false;
      }
      for (const file of action.preview.files) {
        if (editorState.openFiles.some((open) => open.path === file.filePath)) {
          updateContent(file.filePath, file.modifiedContent);
          markSaved(file.filePath);
        }
      }
    }
    if (action.command) {
      const call = LSPServiceBindings.ExecuteRefactorCommand(
        request.language,
        action.command,
        action.commandArguments ?? [],
      );
      if (typeof call.cancelOn === "function") call.cancelOn(controller.signal);
      await call;
      if (generation !== applyGeneration) return false;
    }
    cancelRefactorPreview();
    return true;
  } catch (error) {
    if (generation !== applyGeneration) return false;
    refactorState.error =
      error instanceof Error ? error.message : String(error);
    return false;
  } finally {
    if (generation === applyGeneration) {
      refactorState.applying = false;
      activeApplyController = null;
    }
  }
}

export function resetRefactorState(): void {
  requestGeneration += 1;
  applyGeneration += 1;
  activeApplyController?.abort("refactor state reset");
  activeApplyController = null;
  refactorState.loading = false;
  refactorState.applying = false;
  refactorState.error = "";
  refactorState.request = null;
  refactorState.available = emptyAvailability();
  refactorState.actions = {};
  refactorState.selectedCommand = null;
  refactorState.selectedAction = null;
  refactorState.previewVisible = false;
}
