// Koyori IDE 模块 · Automation；交互服务：对话历史（ConversationService）、任务（TaskService）。
// 喵，这是 Koyori IDE 的 Automation 模块（前端实现）~
import * as ConversationServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/conversationservice.js";
import * as TaskServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/taskservice.js";
import * as WorkflowServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/workflowservice.js";
import * as AgentServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/agentservice.js";
import * as RulesServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/rulesservice.js";
import * as LogLevelServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/loglevelservice.js";
import * as PluginServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/pluginservice.js";
import * as ProfileServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/profileservice.js";
import * as LayoutServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/layoutservice.js";
import * as ToolchainServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/toolchainservice.js";
import {
  OnFailureAction as BindingOnFailureAction,
  WorkflowStepType as BindingWorkflowStepType,
} from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/models.js";
import type {
  CommandCheck, Conversation, ConversationSaveInput, ExecResult, GoTargetState,
  PluginInfo, ProfileExport, RulesConfig, RulesFile, TaskDef, ToolBudgetStatus,
  ToolchainCommand, ToolchainResult, WorkflowDef, WorkflowValidationResult,
} from "@/types";
import type { PluginPermission } from "@/types";
import {
  isRecord, normalizeOptionalStringMap, optionalBoolean, optionalInteger,
  optionalString, optionalStringArray, optionalUnknownRecord, requiredInteger, requiredString,
  safeRecordFromEntries, unwrapNullable, requireNonNull,
  warnInvalidBoundaryValue,
} from "./boundary";

type BindingConversation = Awaited<ReturnType<typeof ConversationServiceBindings.Load>>;
type BindingConversationSave = Parameters<typeof ConversationServiceBindings.Save>[0];

function fromBindingConversation(conversation: BindingConversation): Conversation {
  return {
    ...conversation,
    messages: (conversation.messages ?? []).map((message) => ({
      role: message.role,
      content: message.content,
      toolCalls: message.toolCalls?.map((call) => ({
        id: call.id,
        name: call.name,
        arguments: call.arguments,
      })) ?? undefined,
      toolResults: message.toolResults?.map((result) => ({
        toolCallId: result.toolCallId,
        content: result.content,
        isError: result.isError,
      })) ?? undefined,
    })),
    tags: conversation.tags ?? undefined,
    revision: requiredInteger(conversation.revision, "Conversation.revision", 0),
    updated_at: requiredInteger(conversation.updated_at, "Conversation.updated_at", 0),
    expected_revision: conversation.expected_revision ?? undefined,
  };
}

function toBindingConversation(conversation: ConversationSaveInput): BindingConversationSave {
  return {
    ...conversation,
    revision:
      conversation.revision === undefined
        ? 0
        : requiredInteger(conversation.revision, "Conversation.revision", 0),
    updated_at:
      conversation.updated_at === undefined
        ? 0
        : requiredInteger(conversation.updated_at, "Conversation.updated_at", 0),
  };
}

export const conversationService = {
  save: (conversation: ConversationSaveInput) =>
    ConversationServiceBindings.Save(toBindingConversation(conversation)) as Promise<void>,
  load: async (id: string) =>
    fromBindingConversation(await ConversationServiceBindings.Load(id)),
  list: (offset = 0, limit = 50) =>
    unwrapNullable(ConversationServiceBindings.ListPage(offset, limit), [])
      .then((conversations) => conversations.map(fromBindingConversation)),
  listWithFilter: (
    filter: Parameters<typeof ConversationServiceBindings.ListWithFilterPage>[0],
    offset = 0,
    limit = 50,
  ) => unwrapNullable(
    ConversationServiceBindings.ListWithFilterPage(filter, offset, limit),
    [],
  ).then((conversations) => conversations.map(fromBindingConversation)),
  delete: (id: string) =>
    ConversationServiceBindings.Delete(id) as Promise<void>,
  generateId: () =>
    ConversationServiceBindings.GenerateConversationID() as Promise<string>,
  generateTitle: (firstMessage: string) =>
    ConversationServiceBindings.GenerateTitle(firstMessage) as Promise<string>,
  updateTitle: (id: string, title: string) =>
    ConversationServiceBindings.UpdateTitle(id, title) as Promise<void>,
};

type BindingTaskDef = NonNullable<
  Awaited<ReturnType<typeof TaskServiceBindings.LoadTasks>>
>[number];

function fromBindingTask(task: BindingTaskDef): TaskDef {
  return {
    ...task,
    args: task.args ?? undefined,
    env: normalizeOptionalStringMap(task.env),
    dependsOn: task.dependsOn ?? undefined,
    problemMatcher: task.problemMatcher ?? undefined,
  };
}

export const taskService = {
  loadTasks: (projectRoot: string) =>
    unwrapNullable(TaskServiceBindings.LoadTasks(projectRoot), [])
      .then((tasks) => tasks.map(fromBindingTask)),
  requestWorkflowStepApproval: (sessionID: string, workflowName: string, stepName: string) =>
    TaskServiceBindings.RequestWorkflowStepApproval(
      sessionID,
      workflowName,
      stepName,
    ) as Promise<string>,
  executeApprovedWorkflowStep: (
    sessionID: string,
    workflowName: string,
    stepName: string,
    approvalToken: string,
  ) =>
    TaskServiceBindings.ExecuteApprovedWorkflowStep(
      sessionID,
      workflowName,
      stepName,
      approvalToken,
    ) as Promise<ExecResult>,
  requestExecutionApproval: (executionId: string, command: string, cwd: string) =>
    TaskServiceBindings.RequestExecutionApproval(executionId, command, cwd) as Promise<string>,
  executeApproved: (executionId: string, command: string, cwd: string, approvalToken: string) =>
    TaskServiceBindings.ExecuteApproved(executionId, command, cwd, approvalToken) as Promise<ExecResult>,
  stop: (executionId: string) =>
    TaskServiceBindings.Stop(executionId) as Promise<void>,
	beginWorkflowExecution: (workflowName: string) =>
		TaskServiceBindings.BeginWorkflowExecution(workflowName) as Promise<string>,
	completeWorkflowExecution: (sessionId: string) =>
		TaskServiceBindings.CompleteWorkflowExecution(sessionId) as Promise<void>,
	failWorkflowExecution: (sessionId: string, reason: string) =>
		TaskServiceBindings.FailWorkflowExecution(sessionId, reason) as Promise<void>,
	resumeWorkflowExecution: (sessionId: string) =>
		TaskServiceBindings.ResumeWorkflowExecution(sessionId) as Promise<void>,
};

type BindingWorkflowDef = NonNullable<
  Awaited<ReturnType<typeof WorkflowServiceBindings.LoadWorkflow>>
>;
type BindingWorkflowStep = NonNullable<BindingWorkflowDef["steps"]>[number];
type BindingWorkflowTrigger = NonNullable<BindingWorkflowDef["runOn"]>;
type BindingWorkflowTriggerCondition = NonNullable<BindingWorkflowTrigger["condition"]>;
type FrontendWorkflowStep = WorkflowDef["steps"][number];
type FrontendWorkflowTrigger = NonNullable<WorkflowDef["runOn"]>;
type FrontendWorkflowTriggerCondition = NonNullable<FrontendWorkflowTrigger["condition"]>;

function fromBindingWorkflowStepType(
  value: unknown,
  path: string,
): FrontendWorkflowStep["type"] {
  switch (value) {
    case undefined:
    case null:
    case "":
      return undefined;
    case BindingWorkflowStepType.WorkflowStepCommand:
      return "command";
    case BindingWorkflowStepType.WorkflowStepAI:
      return "ai";
    case BindingWorkflowStepType.WorkflowStepGit:
      return "git";
    case BindingWorkflowStepType.WorkflowStepFile:
      return "file";
    case BindingWorkflowStepType.WorkflowStepMCP:
      return "mcp";
    case BindingWorkflowStepType.WorkflowStepSkill:
      return "skill";
    default:
      throw new Error(`${path} must be a supported workflow step type`);
  }
}

function toBindingWorkflowStepType(
  value: unknown,
  path: string,
): BindingWorkflowStep["type"] {
  switch (value) {
    case undefined:
    case null:
    case "":
      return undefined;
    case "command":
      return BindingWorkflowStepType.WorkflowStepCommand;
    case "ai":
      return BindingWorkflowStepType.WorkflowStepAI;
    case "git":
      return BindingWorkflowStepType.WorkflowStepGit;
    case "file":
      return BindingWorkflowStepType.WorkflowStepFile;
    case "mcp":
      return BindingWorkflowStepType.WorkflowStepMCP;
    case "skill":
      return BindingWorkflowStepType.WorkflowStepSkill;
    default:
      throw new Error(`${path} must be a supported workflow step type`);
  }
}

function fromBindingOnFailure(
  value: unknown,
  path: string,
): FrontendWorkflowStep["onFailure"] {
  switch (value) {
    case undefined:
    case null:
    case "":
      return undefined;
    case BindingOnFailureAction.OnFailureAbort:
      return "abort";
    case BindingOnFailureAction.OnFailureContinue:
      return "continue";
    case BindingOnFailureAction.OnFailureSkip:
      return "skip";
    case BindingOnFailureAction.OnFailureRetry:
      return "retry";
    default:
      warnInvalidBoundaryValue(path, "a supported workflow failure action", "undefined");
      return undefined;
  }
}

function toBindingOnFailure(
  value: unknown,
  path: string,
): BindingWorkflowStep["onFailure"] {
  switch (value) {
    case undefined:
    case null:
    case "":
      return undefined;
    case "abort":
      return BindingOnFailureAction.OnFailureAbort;
    case "continue":
      return BindingOnFailureAction.OnFailureContinue;
    case "skip":
      return BindingOnFailureAction.OnFailureSkip;
    case "retry":
      return BindingOnFailureAction.OnFailureRetry;
    default:
      warnInvalidBoundaryValue(path, "a supported workflow failure action", "undefined");
      return undefined;
  }
}

function fromBindingWorkflowStep(value: unknown, path: string): FrontendWorkflowStep {
  const step = isRecord(value) ? value : {};
  if (!isRecord(value)) {
    warnInvalidBoundaryValue(path, "a workflow step object", "an empty step");
  }
  return {
    name: requiredString(step.name, `${path}.name`),
    command: requiredString(step.command, `${path}.command`),
    args: optionalStringArray(step.args, `${path}.args`),
    cwd: optionalString(step.cwd, `${path}.cwd`),
    dependsOn: optionalStringArray(step.dependsOn, `${path}.dependsOn`),
    condition: optionalString(step.condition, `${path}.condition`),
    expectSuccess: optionalBoolean(step.expectSuccess, `${path}.expectSuccess`),
    type: fromBindingWorkflowStepType(step.type, `${path}.type`),
    tool: optionalString(step.tool, `${path}.tool`),
    input: optionalUnknownRecord(step.input, `${path}.input`),
    onFailure: fromBindingOnFailure(step.onFailure, `${path}.onFailure`),
    timeout: optionalInteger(step.timeout, `${path}.timeout`),
  };
}

function toBindingWorkflowStep(value: unknown, path: string): BindingWorkflowStep {
  const step = isRecord(value) ? value : {};
  if (!isRecord(value)) {
    warnInvalidBoundaryValue(path, "a workflow step object", "an empty step");
  }
  return {
    name: requiredString(step.name, `${path}.name`),
    command: requiredString(step.command, `${path}.command`),
    args: optionalStringArray(step.args, `${path}.args`),
    cwd: optionalString(step.cwd, `${path}.cwd`),
    dependsOn: optionalStringArray(step.dependsOn, `${path}.dependsOn`),
    condition: optionalString(step.condition, `${path}.condition`),
    expectSuccess: optionalBoolean(step.expectSuccess, `${path}.expectSuccess`),
    type: toBindingWorkflowStepType(step.type, `${path}.type`),
    tool: optionalString(step.tool, `${path}.tool`),
    input: optionalUnknownRecord(step.input, `${path}.input`),
    onFailure: toBindingOnFailure(step.onFailure, `${path}.onFailure`),
    timeout: optionalInteger(step.timeout, `${path}.timeout`),
  };
}

function fromBindingWorkflowTriggerCondition(
  value: unknown,
  path: string,
): FrontendWorkflowTriggerCondition | undefined {
  if (value === undefined || value === null) return undefined;
  if (!isRecord(value)) {
    warnInvalidBoundaryValue(path, "a workflow trigger condition", "undefined");
    return undefined;
  }
  return {
    branch: optionalString(value.branch, `${path}.branch`),
    language: optionalString(value.language, `${path}.language`),
    fileGlob: optionalString(value.fileGlob, `${path}.fileGlob`),
  };
}

function toBindingWorkflowTriggerCondition(
  value: unknown,
  path: string,
): BindingWorkflowTriggerCondition | undefined {
  const condition = fromBindingWorkflowTriggerCondition(value, path);
  if (!condition) return undefined;
  return {
    branch: condition.branch,
    language: condition.language,
    fileGlob: condition.fileGlob,
  };
}

function fromBindingWorkflowTrigger(
  value: unknown,
  path: string,
): FrontendWorkflowTrigger | undefined {
  if (value === undefined || value === null) return undefined;
  if (!isRecord(value)) {
    warnInvalidBoundaryValue(path, "a workflow trigger", "undefined");
    return undefined;
  }
  return {
    event: requiredString(value.event, `${path}.event`),
    glob: optionalString(value.glob, `${path}.glob`),
    workflowName: optionalString(value.workflowName, `${path}.workflowName`),
    condition: fromBindingWorkflowTriggerCondition(value.condition, `${path}.condition`),
  };
}

function toBindingWorkflowTrigger(
  value: unknown,
  path: string,
): BindingWorkflowTrigger | undefined {
  if (value === undefined || value === null) return undefined;
  if (!isRecord(value)) {
    warnInvalidBoundaryValue(path, "a workflow trigger", "undefined");
    return undefined;
  }
  return {
    event: requiredString(value.event, `${path}.event`),
    glob: optionalString(value.glob, `${path}.glob`),
    workflowName: optionalString(value.workflowName, `${path}.workflowName`),
    condition: toBindingWorkflowTriggerCondition(value.condition, `${path}.condition`),
  };
}

function normalizeWorkflowSteps<T>(
  value: unknown,
  path: string,
  convert: (step: unknown, path: string) => T,
): T[] {
  if (!Array.isArray(value)) {
    warnInvalidBoundaryValue(path, "an array of workflow steps", "an empty array");
    return [];
  }
  return value.map((step, index) => convert(step, `${path}[${index}]`));
}

export function fromBindingWorkflow(workflow: BindingWorkflowDef): WorkflowDef {
  return {
    name: requiredString(workflow.name, "Workflow.name"),
    description: optionalString(workflow.description, "Workflow.description"),
    steps: normalizeWorkflowSteps(workflow.steps, "Workflow.steps", fromBindingWorkflowStep),
    watch: optionalStringArray(workflow.watch, "Workflow.watch"),
    runOn: fromBindingWorkflowTrigger(workflow.runOn, "Workflow.runOn"),
    requiresConfirmation: optionalBoolean(
      workflow.requiresConfirmation,
      "Workflow.requiresConfirmation",
    ),
    source: requiredString(workflow.source, "Workflow.source"),
  };
}

export function toBindingWorkflow(workflow: WorkflowDef): BindingWorkflowDef {
  return {
    name: requiredString(workflow.name, "Workflow.name"),
    description: optionalString(workflow.description, "Workflow.description"),
    steps: normalizeWorkflowSteps(workflow.steps, "Workflow.steps", toBindingWorkflowStep),
    watch: optionalStringArray(workflow.watch, "Workflow.watch"),
    runOn: toBindingWorkflowTrigger(workflow.runOn, "Workflow.runOn"),
    requiresConfirmation: optionalBoolean(
      workflow.requiresConfirmation,
      "Workflow.requiresConfirmation",
    ),
    source: requiredString(workflow.source, "Workflow.source"),
  };
}

function fromBindingWorkflowValidation(
  result: Awaited<ReturnType<typeof WorkflowServiceBindings.ValidateWorkflow>>,
): WorkflowValidationResult {
  return {
    ...result,
    errors: result.errors ?? undefined,
  };
}

export const workflowService = {
  loadWorkflows: async (projectRoot: string) => {
    const workflows = await unwrapNullable(
      WorkflowServiceBindings.LoadWorkflows(projectRoot),
      [],
    );
    return workflows.map(fromBindingWorkflow);
  },
  loadWorkflow: async (projectRoot: string, name: string) =>
    fromBindingWorkflow(await requireNonNull(
      WorkflowServiceBindings.LoadWorkflow(projectRoot, name),
      "WorkflowService.LoadWorkflow",
    )),
  validateDependencies: (wf: WorkflowDef) =>
    WorkflowServiceBindings.ValidateDependencies(toBindingWorkflow(wf)),
  // N-55: Validate a single workflow and return structured errors.
  validateWorkflow: async (wf: WorkflowDef) =>
    fromBindingWorkflowValidation(
      await WorkflowServiceBindings.ValidateWorkflow(toBindingWorkflow(wf)),
    ),
  // N-55: Validate all workflows and return per-workflow results.
  validateAllWorkflows: async (wfs: WorkflowDef[]) => {
    const results = await unwrapNullable(
      WorkflowServiceBindings.ValidateAllWorkflows(wfs.map(toBindingWorkflow)),
      [],
    );
    return results.map(fromBindingWorkflowValidation);
  },
  // prompt-6 Task 7: workflow CRUD uses regenerated ByID bindings.
  createWorkflow: (projectRoot: string, name: string, def: WorkflowDef) =>
    WorkflowServiceBindings.CreateWorkflow(
      projectRoot,
      name,
      toBindingWorkflow(def),
    ),
  saveWorkflow: (projectRoot: string, name: string, def: WorkflowDef) =>
    WorkflowServiceBindings.SaveWorkflow(
      projectRoot,
      name,
      toBindingWorkflow(def),
    ),
  deleteWorkflow: (projectRoot: string, name: string) =>
    WorkflowServiceBindings.DeleteWorkflow(projectRoot, name) as Promise<void>,
  renameWorkflow: (projectRoot: string, oldName: string, newName: string) =>
    WorkflowServiceBindings.RenameWorkflow(projectRoot, oldName, newName) as Promise<void>,
};

	export const agentService = {
	createSession: (kind: "chat" | "plan" | "goal" | "workflow") =>
		AgentServiceBindings.CreateAgentSessionForCaller(kind) as Promise<string>,
	closeSession: (sessionId: string) =>
		AgentServiceBindings.CloseAgentSessionForCaller(sessionId) as Promise<void>,
	getToolCatalog: async (): Promise<AgentToolCatalog> => {
		const catalog = await AgentServiceBindings.GetAgentToolCatalog();
		return {
			revision: catalog.revision,
			tools: (catalog.tools ?? []).map((tool) => normalizeAgentToolDefinition(tool)),
		};
	},
	requestAgentToolCapability: async (
		request: AgentToolExecutionRequest,
	): Promise<AgentToolCapability> => AgentServiceBindings.RequestAgentToolCapability(request),
	executeApprovedAgentTool: async (
		request: AgentToolCapabilityExecution,
	): Promise<AgentToolExecutionResult> => normalizeAgentToolExecutionResult(
		await AgentServiceBindings.ExecuteApprovedAgentTool(request),
	),
	executeAgentTool: async (
		request: AgentToolExecutionRequest,
	): Promise<AgentToolExecutionResult> => normalizeAgentToolExecutionResult(
		await AgentServiceBindings.ExecuteAgentTool(request),
	),
  checkCommand: (command: string) =>
    AgentServiceBindings.CheckCommand(command) as Promise<CommandCheck>,
  // GOAL-P1-02: the tool-call budget is enforced in the backend. These two
  // calls are the renderer's only access to it — read the state, or ask the
  // user's behalf for a new epoch. There is deliberately no setter for the
  // limit, so a compromised renderer can misdisplay the ceiling but not raise it.
  getToolBudget: () =>
    AgentServiceBindings.GetToolBudget() as Promise<ToolBudgetStatus>,
  startNewToolBudgetEpoch: (limit: number) =>
    AgentServiceBindings.StartNewToolBudgetEpoch(limit) as Promise<ToolBudgetStatus>,
};

export interface AgentToolDefinition {
	id: string;
	wireName: string;
	description: string;
	inputSchema: Record<string, unknown>;
	source: "builtin" | "mcp" | "workflow" | "skill";
	risk: "read-only" | "elevated" | "dangerous";
	approval: "manual" | "backend-policy";
	mutation: "none" | "workspace-transaction" | "external";
	metadata?: Record<string, string>;
}

export interface AgentToolCatalog {
	revision: number;
	tools: AgentToolDefinition[];
}

export interface AgentToolExecutionRequest {
	sessionId: string;
	catalogRevision: number;
	toolId: string;
	arguments: Record<string, unknown>;
}

export interface AgentToolCapability {
	token: string;
	toolId: string;
	argumentsHash: string;
	catalogRevision: number;
	budgetEpoch: number;
	workspaceGeneration: number;
	expiresAt: unknown;
}

export interface AgentToolCapabilityExecution extends AgentToolExecutionRequest {
	token: string;
}

export interface AgentToolExecutionResult {
	observation: string;
	metadata?: Record<string, string>;
	usage: {
		unitId: string;
		sessionId: string;
		unitKind: string;
		operation: string;
		cost: number;
		costBasis: string;
		estimated: boolean;
		success: boolean;
		externalReceiptId?: string;
		externalReceiptReversible?: boolean;
		externalCompensation?: string;
		pending?: boolean;
		error?: string;
	};
}

function normalizeAgentToolDefinition(value: unknown): AgentToolDefinition {
	if (!isRecord(value)) throw new Error("AgentService returned a non-object ToolDef");
	const inputSchema = isRecord(value.inputSchema) ? value.inputSchema : null;
	if (!inputSchema) throw new Error("AgentService returned a ToolDef without inputSchema");
	const source = requiredString(value.source, "AgentToolDefinition.source");
	const risk = requiredString(value.risk, "AgentToolDefinition.risk");
	const approval = requiredString(value.approval, "AgentToolDefinition.approval");
	const mutation = requiredString(value.mutation, "AgentToolDefinition.mutation");
	if (source !== "builtin" && source !== "mcp" && source !== "workflow" && source !== "skill") {
		throw new Error(`AgentService returned invalid ToolDef source: ${source}`);
	}
	if (risk !== "read-only" && risk !== "elevated" && risk !== "dangerous") {
		throw new Error(`AgentService returned invalid ToolDef risk: ${risk}`);
	}
	if (approval !== "manual" && approval !== "backend-policy") {
		throw new Error(`AgentService returned invalid ToolDef approval: ${approval}`);
	}
	if (mutation !== "none" && mutation !== "workspace-transaction" && mutation !== "external") {
		throw new Error(`AgentService returned invalid ToolDef mutation: ${mutation}`);
	}
	return {
		id: requiredString(value.id, "AgentToolDefinition.id"),
		wireName: requiredString(value.wireName, "AgentToolDefinition.wireName"),
		description: requiredString(value.description, "AgentToolDefinition.description"),
		inputSchema,
		source,
		risk,
		approval,
		mutation,
		metadata: normalizeAgentMetadata(value.metadata),
	};
}

function normalizeAgentMetadata(value: unknown): Record<string, string> | undefined {
	if (value === undefined || value === null) return undefined;
	if (!isRecord(value)) throw new Error("AgentService returned invalid tool metadata");
	const metadata: Record<string, string> = {};
	for (const [key, item] of Object.entries(value)) {
		if (typeof item !== "string") throw new Error(`AgentService returned invalid metadata value for ${key}`);
		metadata[key] = item;
	}
	return metadata;
}

function normalizeAgentToolExecutionResult(
	value: Awaited<ReturnType<typeof AgentServiceBindings.ExecuteAgentTool>>,
): AgentToolExecutionResult {
	return {
		observation: value.observation,
		metadata: normalizeAgentMetadata(value.metadata),
		usage: {
			unitId: value.usage.unitId,
			sessionId: value.usage.sessionId,
			unitKind: value.usage.unitKind,
			operation: value.usage.operation,
			cost: value.usage.cost,
			costBasis: value.usage.costBasis,
			estimated: value.usage.estimated,
			success: value.usage.success,
			externalReceiptId: value.usage.externalReceiptId,
			externalReceiptReversible: value.usage.externalReceiptReversible,
			externalCompensation: value.usage.externalCompensation,
			pending: value.usage.pending,
			error: value.usage.error,
		},
	};
}

export const rulesService = {
  loadRules: (projectRoot: string) =>
    RulesServiceBindings.LoadRules(projectRoot) as Promise<RulesFile>,
  loadRulesMerge: (projectRoot: string) =>
    unwrapNullable(RulesServiceBindings.LoadRulesMerge(projectRoot), []),
  saveRules: (projectRoot: string, relPath: string, content: string) =>
    RulesServiceBindings.SaveRules(projectRoot, relPath, content) as Promise<void>,
  listCandidates: (projectRoot: string) =>
    unwrapNullable(RulesServiceBindings.ListRulesCandidates(projectRoot), []),
  loadRulesConfig: (projectRoot: string) =>
    RulesServiceBindings.LoadRulesConfig(projectRoot) as Promise<RulesConfig>,
  saveRulesConfig: (projectRoot: string, cfg: RulesConfig) =>
    RulesServiceBindings.SaveRulesConfig(projectRoot, cfg) as Promise<void>,
  saveUserRulesConfig: (cfg: RulesConfig) =>
    RulesServiceBindings.SaveUserRulesConfig(cfg) as Promise<void>,
};

export const logLevelService = {
  getLogPath: () =>
    LogLevelServiceBindings.GetLogPath() as Promise<string>,
  readLog: (maxBytes = 0) =>
    LogLevelServiceBindings.ReadLog(maxBytes) as Promise<string>,
};

type BindingPluginInfo = Awaited<ReturnType<typeof PluginServiceBindings.GetPlugin>>;

function isPluginPermission(value: string): value is PluginPermission {
  return value === "fs.read"
    || value === "fs.write"
    || value === "shell.exec"
    || value === "net"
    || value === "ai.send";
}

function fromBindingPluginInfo(plugin: BindingPluginInfo): PluginInfo {
  let source: PluginInfo["source"];
  switch (plugin.source) {
    case "user":
    case "project":
      source = plugin.source;
      break;
    default:
      throw new Error(`PluginService returned invalid source: ${plugin.source}`);
  }

  const permissions = (plugin.manifest.permissions ?? []).map((permission) => {
    if (!isPluginPermission(permission)) {
      throw new Error(`PluginService returned invalid permission: ${permission}`);
    }
    return permission;
  });
  const contributes = plugin.manifest.contributes
    ? {
        commands: plugin.manifest.contributes.commands ?? undefined,
        views: (plugin.manifest.contributes.views ?? []).map((view) => {
          let location: "sidebar" | "panel" | "statusbar" | undefined;
          switch (view.location) {
            case undefined:
            case "sidebar":
            case "panel":
            case "statusbar":
              location = view.location;
              break;
            default:
              throw new Error(`PluginService returned invalid view location: ${view.location}`);
          }
          return {
            ...view,
            location,
          };
        }),
      }
    : undefined;

  return {
    ...plugin,
    source,
    manifest: {
      ...plugin.manifest,
      permissions: permissions.length > 0 ? permissions : undefined,
      activationEvents: plugin.manifest.activationEvents ?? undefined,
      contributes,
    },
  };
}

export const pluginService = {
  listPlugins: (projectRoot: string) =>
    unwrapNullable(PluginServiceBindings.ListPlugins(projectRoot), [])
      .then((plugins) => plugins.map(fromBindingPluginInfo)),
  getPlugin: async (name: string, projectRoot: string) =>
    fromBindingPluginInfo(await PluginServiceBindings.GetPlugin(name, projectRoot)),
  setPluginEnabled: (name: string, enabled: boolean) =>
    PluginServiceBindings.SetPluginEnabled(name, enabled) as Promise<void>,
  readPluginFile: (pluginName: string, relPath: string, projectRoot: string) =>
    PluginServiceBindings.ReadPluginFile(pluginName, relPath, projectRoot) as Promise<string | null>,
};

export const profileService = {
  listProfiles: () =>
    unwrapNullable(ProfileServiceBindings.ListProfiles(), []),
  getActiveProfile: () =>
    ProfileServiceBindings.GetActiveProfile() as Promise<string>,
  setActiveProfile: (name: string) =>
    ProfileServiceBindings.SetActiveProfile(name) as Promise<void>,
  createProfile: (name: string, fromCurrent: boolean) =>
    ProfileServiceBindings.CreateProfile(name, fromCurrent) as Promise<void>,
  deleteProfile: (name: string) =>
    ProfileServiceBindings.DeleteProfile(name) as Promise<void>,
  renameProfile: (oldName: string, newName: string) =>
    ProfileServiceBindings.RenameProfile(oldName, newName) as Promise<void>,
  setProfileDescription: (name: string, description: string) =>
    ProfileServiceBindings.SetProfileDescription(name, description) as Promise<void>,
  exportProfile: (name: string) =>
    ProfileServiceBindings.ExportProfile(name) as Promise<ProfileExport>,
  importProfile: (exportData: ProfileExport) =>
    ProfileServiceBindings.ImportProfile(exportData) as Promise<string>,
};

export const layoutService = {
  loadLayout: () =>
    LayoutServiceBindings.LoadLayout() as Promise<string>,
  saveLayout: (layoutJSON: string) =>
    LayoutServiceBindings.SaveLayout(layoutJSON) as Promise<void>,
  // Proposal H (prompt-4.md): Remove the persisted layout file.
  resetLayout: () =>
    LayoutServiceBindings.ResetLayout() as Promise<void>,
};

type BindingToolchainCommand = NonNullable<
  Awaited<ReturnType<typeof ToolchainServiceBindings.ListToolchainCommands>>
>[number];
type BindingToolchainResult = Awaited<
  ReturnType<typeof ToolchainServiceBindings.RunToolchainCommand>
>;

function fromBindingToolchainCommand(command: BindingToolchainCommand): ToolchainCommand {
  const language = command.language.trim();
  if (!language) throw new Error("ToolchainService returned an empty language");
  return {
    ...command,
    language,
    args: command.args ?? undefined,
  };
}

function fromBindingToolchainResult(result: BindingToolchainResult): ToolchainResult {
  return {
    ...result,
    errors: (result.errors ?? []).map((diagnostic) => {
      if (
        diagnostic.severity !== "error"
        && diagnostic.severity !== "warning"
        && diagnostic.severity !== "info"
      ) {
        throw new Error(`ToolchainService returned invalid severity: ${diagnostic.severity}`);
      }
      return {
        ...diagnostic,
        severity: diagnostic.severity,
      };
    }),
  };
}

export const toolchainService = {
	listGoTargets: () =>
		unwrapNullable(ToolchainServiceBindings.ListGoTargets(), []),
	getGoTarget: () =>
		ToolchainServiceBindings.GetGoTarget() as Promise<GoTargetState>,
	setGoTarget: (goos: string, goarch: string) =>
		ToolchainServiceBindings.SetGoTarget(goos, goarch) as Promise<GoTargetState>,
	resetGoTarget: () =>
		ToolchainServiceBindings.ResetGoTarget() as Promise<GoTargetState>,
  // G-FEAT-03: list available toolchain commands for the open workspace.
  listToolchainCommands: () =>
    unwrapNullable(ToolchainServiceBindings.ListToolchainCommands(), [])
      .then((commands) => commands.map(fromBindingToolchainCommand)),
  // G-FEAT-03: run a toolchain command by id. filePath, when provided,
  // runs the command in the file's directory (e.g. lint a single file).
  runToolchainCommand: async (cmdId: string, filePath: string) =>
    fromBindingToolchainResult(
      await ToolchainServiceBindings.RunToolchainCommand(cmdId, filePath),
    ),
  // G-FEAT-03: report which toolchain binaries are installed.
  detectToolchains: async (): Promise<Record<string, boolean>> => {
    const detected = await unwrapNullable(
      ToolchainServiceBindings.DetectToolchains(),
      {},
    );
    const entries: Array<[string, boolean]> = [];
    for (const [name, available] of Object.entries(detected)) {
      if (typeof available === "boolean") entries.push([name, available]);
    }
    return safeRecordFromEntries(entries);
  },
  // prompt-9 9-C / 9-H / 9-I
  runTestAtCursor: async (language: string, filePath: string, line: number, content: string) =>
    fromBindingToolchainResult(
      await ToolchainServiceBindings.RunTestAtCursor(language, filePath, line, content),
    ),
  cancelTestAtCursor: async (): Promise<boolean> =>
    ToolchainServiceBindings.CancelTestAtCursor(),
  detectRuntimeVersions: () =>
    ToolchainServiceBindings.DetectRuntimeVersions() as Promise<{
      goVersion: string;
      nodeVersion: string;
      goplsVersion: string;
      hasGoWork: boolean;
    }>,
  // prompt-11 11-F
  runGoTestsJSON: async (packageDir: string, runRegex: string) => {
    const result = await ToolchainServiceBindings.RunGoTestsJSON(packageDir, runRegex);
    const statusEntries: Array<[string, string]> = [];
    for (const [test, status] of Object.entries(result.statusByTest ?? {})) {
      if (typeof status === "string") statusEntries.push([test, status]);
    }
    return {
      ...result,
      events: result.events ?? [],
      statusByTest: safeRecordFromEntries(statusEntries),
    };
  },
};
