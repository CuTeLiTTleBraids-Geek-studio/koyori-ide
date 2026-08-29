import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";

vi.mock("@wailsio/runtime", () => ({
  Events: { On: vi.fn(), Emit: vi.fn() },
}));

vi.mock("@/api/services", () => ({
  workflowService: {
    loadWorkflows: vi.fn(),
    loadWorkflow: vi.fn(),
    validateDependencies: vi.fn(),
  },
  taskService: {
    requestExecutionApproval: vi.fn().mockResolvedValue("approval-1"),
    executeApproved: vi.fn().mockResolvedValue({
      command: "", cwd: "", stdout: "", stderr: "", exitCode: 0,
      durationMs: 1, riskLevel: "safe", blocked: false,
    }),
    stop: vi.fn().mockResolvedValue(undefined),
		beginWorkflowExecution: vi.fn().mockResolvedValue("workflow:wf:run"),
		completeWorkflowExecution: vi.fn().mockResolvedValue(undefined),
		failWorkflowExecution: vi.fn().mockResolvedValue(undefined),
		resumeWorkflowExecution: vi.fn().mockResolvedValue(undefined),
  },
}));

vi.mock("@/stores/app", () => ({
  appState: { terminalVisible: false, currentProject: null },
}));

vi.mock("@/stores/output", () => ({
  pushOutput: vi.fn(),
}));

vi.mock("@/lib/notifications", () => ({
  notifyError: vi.fn(),
  notifySuccess: vi.fn(),
  notifyWarning: vi.fn(),
}));

import {
  workflowState,
  loadWorkflows,
  hasWorkflows,
  composeStepCommandLine,
  resolveStepCwd,
  evaluateCondition,
  topologicalSort,
  runWorkflow,
  matchGlob,
  relativizePath,
  findTriggeredWorkflows,
  findStartupWorkflows,
  findChainTriggeredWorkflows,
  extractStepOutputs,
  substituteOutputRefs,
  cleanupWorkflowRuntime,
} from "./workflows";
import { taskService, workflowService } from "@/api/services";
import { pushOutput } from "@/stores/output";
import { notifyError } from "@/lib/notifications";
import type { ExecResult, WorkflowDef, WorkflowStep } from "@/types";

function makeStep(name: string, command = "echo", extra: Partial<WorkflowStep> = {}): WorkflowStep {
  return { name, command, ...extra };
}

function makeWorkflow(name: string, steps: WorkflowStep[]): WorkflowDef {
  return { name, steps, source: `${name}.yml` };
}

function taskResult(overrides: Partial<ExecResult> = {}): ExecResult {
  return {
    command: "", cwd: "", stdout: "", stderr: "", exitCode: 0,
    durationMs: 1, riskLevel: "safe", blocked: false,
    ...overrides,
  };
}

describe("workflows store", () => {
  beforeEach(() => {
    cleanupWorkflowRuntime();
    vi.useRealTimers();
    vi.clearAllMocks();
    workflowState.workflows = [];
    workflowState.loading = false;
    workflowState.errorMessage = null;
    workflowState.running = {};
    workflowState.stepStates = {};
  });

  afterEach(() => {
    cleanupWorkflowRuntime();
    vi.useRealTimers();
  });

  describe("loadWorkflows", () => {
    it("clears workflows when root is empty", async () => {
      workflowState.workflows = [makeWorkflow("a", [])];
      await loadWorkflows("");
      expect(workflowState.workflows).toEqual([]);
      expect(workflowState.errorMessage).toBeNull();
      expect(workflowService.loadWorkflows).not.toHaveBeenCalled();
    });

    it("loads workflows from backend", async () => {
      (workflowService.loadWorkflows as any).mockResolvedValue([
        makeWorkflow("build", [makeStep("compile", "go", { args: ["build", "./..."] })]),
      ]);
      await loadWorkflows("/proj");
      expect(workflowState.workflows.length).toBe(1);
      expect(workflowState.workflows[0].name).toBe("build");
      expect(workflowState.loading).toBe(false);
      expect(workflowState.errorMessage).toBeNull();
    });

    it("surfaces backend errors", async () => {
      (workflowService.loadWorkflows as any).mockRejectedValue(new Error("parse failed"));
      await loadWorkflows("/proj");
      expect(workflowState.workflows).toEqual([]);
      expect(workflowState.errorMessage).toBe("parse failed");
      expect(workflowState.loading).toBe(false);
      expect(notifyError).toHaveBeenCalled();
    });

    it("handles non-Error rejection payload", async () => {
      (workflowService.loadWorkflows as any).mockRejectedValue("string err");
      await loadWorkflows("/proj");
      expect(workflowState.errorMessage).toBe("string err");
    });

    it("does not let an older workspace response overwrite a newer load", async () => {
      let resolveFirst!: (value: WorkflowDef[]) => void;
      let resolveSecond!: (value: WorkflowDef[]) => void;
      vi.mocked(workflowService.loadWorkflows)
        .mockImplementationOnce(() => new Promise((resolve) => { resolveFirst = resolve; }))
        .mockImplementationOnce(() => new Promise((resolve) => { resolveSecond = resolve; }));

      const first = loadWorkflows("/first");
      const second = loadWorkflows("/second");
      resolveSecond([makeWorkflow("second", [])]);
      await second;
      resolveFirst([makeWorkflow("first", [])]);
      await first;

      expect(workflowState.workflows.map((workflow) => workflow.name)).toEqual(["second"]);
      expect(workflowState.loading).toBe(false);
    });
  });

  describe("hasWorkflows computed", () => {
    it("is false when no workflows", () => {
      expect(hasWorkflows.value).toBe(false);
    });
    it("is true when workflows exist", () => {
      workflowState.workflows = [makeWorkflow("a", [])];
      expect(hasWorkflows.value).toBe(true);
    });
  });

  describe("composeStepCommandLine", () => {
    it("returns command alone when no args", () => {
      expect(composeStepCommandLine(makeStep("s", "ls"))).toBe("ls");
    });
    it("quotes args", () => {
      expect(composeStepCommandLine(makeStep("s", "go", { args: ["build", "./..."] })))
        .toBe("go 'build' './...'");
    });
    it("escapes embedded single quotes", () => {
      expect(composeStepCommandLine(makeStep("s", "echo", { args: ["it's"] })))
        .toBe("echo 'it'\\''s'");
    });
  });

  describe("resolveStepCwd", () => {
    it("returns project root when no cwd", () => {
      expect(resolveStepCwd(makeStep("s"), "/proj")).toBe("/proj");
    });
    it("joins relative cwd to root", () => {
      expect(resolveStepCwd(makeStep("s", "ls", { cwd: "src" }), "/proj")).toBe("/proj/src");
    });
    it("strips trailing slash from root before joining", () => {
      expect(resolveStepCwd(makeStep("s", "ls", { cwd: "src" }), "/proj/")).toBe("/proj/src");
    });
    it("uses absolute cwd as-is", () => {
      expect(resolveStepCwd(makeStep("s", "ls", { cwd: "/abs" }), "/proj")).toBe("/abs");
    });
    it("uses absolute windows cwd as-is", () => {
      expect(resolveStepCwd(makeStep("s", "ls", { cwd: "C:\\abs" }), "/proj")).toBe("C:\\abs");
    });
  });

  describe("evaluateCondition", () => {
    it("runs when condition is undefined", () => {
      expect(evaluateCondition(undefined)).toBe(true);
    });
    it("runs when condition is empty", () => {
      expect(evaluateCondition("")).toBe(true);
    });
    it("runs when condition is whitespace-only", () => {
      // Whitespace trims to empty, which is treated as "no condition".
      expect(evaluateCondition("   ")).toBe(true);
    });
    it("skips when condition is false", () => {
      expect(evaluateCondition("false")).toBe(false);
    });
    it("skips when condition is FALSE (case-insensitive)", () => {
      expect(evaluateCondition("FALSE")).toBe(false);
    });
    it("skips when condition is 0", () => {
      expect(evaluateCondition("0")).toBe(false);
    });
    it("skips when condition is no", () => {
      expect(evaluateCondition("no")).toBe(false);
    });
    it("runs when condition is true", () => {
      expect(evaluateCondition("true")).toBe(true);
    });
    it("runs when condition is arbitrary non-falsy string", () => {
      expect(evaluateCondition("$ENV == staging")).toBe(true);
    });

    // --- Expression language (N-28) ---
    const status = (s: Record<string, string>) => (name: string) => s[name];

    it("expression: steps.build.success when build succeeded", () => {
      expect(evaluateCondition("steps.build.success", status({ build: "success" }))).toBe(true);
    });
    it("expression: steps.build.success when build failed", () => {
      expect(evaluateCondition("steps.build.success", status({ build: "failed" }))).toBe(false);
    });
    it("expression: steps.build.failed when build failed", () => {
      expect(evaluateCondition("steps.build.failed", status({ build: "failed" }))).toBe(true);
    });
    it("expression: steps.build.skipped when build was skipped", () => {
      expect(evaluateCondition("steps.build.skipped", status({ build: "skipped" }))).toBe(true);
    });
    it("expression: steps.build.success when step is unknown", () => {
      expect(evaluateCondition("steps.build.success", status({}))).toBe(false);
    });
    it("expression: steps.build.success when no stepStatus provided", () => {
      expect(evaluateCondition("steps.build.success")).toBe(false);
    });
    it("expression: && operator (both true)", () => {
      expect(
        evaluateCondition(
          "steps.build.success && steps.test.success",
          status({ build: "success", test: "success" }),
        ),
      ).toBe(true);
    });
    it("expression: && operator (one false)", () => {
      expect(
        evaluateCondition(
          "steps.build.success && steps.test.success",
          status({ build: "success", test: "failed" }),
        ),
      ).toBe(false);
    });
    it("expression: || operator (both false)", () => {
      expect(
        evaluateCondition(
          "steps.build.success || steps.test.success",
          status({ build: "failed", test: "failed" }),
        ),
      ).toBe(false);
    });
    it("expression: || operator (one true)", () => {
      expect(
        evaluateCondition(
          "steps.build.success || steps.test.success",
          status({ build: "failed", test: "success" }),
        ),
      ).toBe(true);
    });
    it("expression: ! operator", () => {
      expect(
        evaluateCondition("!steps.lint.failed", status({ lint: "success" })),
      ).toBe(true);
    });
    it("expression: ! operator when lint failed", () => {
      expect(
        evaluateCondition("!steps.lint.failed", status({ lint: "failed" })),
      ).toBe(false);
    });
    it("expression: parentheses", () => {
      expect(
        evaluateCondition(
          "(steps.build.success || steps.build.failed) && !steps.build.skipped",
          status({ build: "failed" }),
        ),
      ).toBe(true);
    });
    it("expression: nested parentheses", () => {
      expect(
        evaluateCondition(
          "((steps.a.success))",
          status({ a: "success" }),
        ),
      ).toBe(true);
    });
    it("expression: true literal", () => {
      expect(evaluateCondition("true")).toBe(true);
    });
    it("expression: false literal with &&", () => {
      expect(
        evaluateCondition("false && steps.build.success", status({ build: "success" })),
      ).toBe(false);
    });
    it("expression: complex condition", () => {
      expect(
        evaluateCondition(
          "steps.build.success && (steps.test.success || steps.test.skipped) && !steps.lint.failed",
          status({ build: "success", test: "skipped", lint: "success" }),
        ),
      ).toBe(true);
    });
  });

  describe("topologicalSort", () => {
    it("returns steps in original order when no deps", () => {
      const steps = [makeStep("a"), makeStep("b"), makeStep("c")];
      const out = topologicalSort(steps);
      expect(out.map((s) => s.name)).toEqual(["a", "b", "c"]);
    });

    it("orders dependencies before dependents", () => {
      const steps = [
        makeStep("build", "go", { dependsOn: ["lint"] }),
        makeStep("lint", "golangci-lint"),
      ];
      const out = topologicalSort(steps);
      expect(out.map((s) => s.name)).toEqual(["lint", "build"]);
    });

    it("handles diamond dependencies", () => {
      const steps = [
        makeStep("d", "echo d", { dependsOn: ["b", "c"] }),
        makeStep("b", "echo b", { dependsOn: ["a"] }),
        makeStep("c", "echo c", { dependsOn: ["a"] }),
        makeStep("a", "echo a"),
      ];
      const out = topologicalSort(steps);
      const names = out.map((s) => s.name);
      expect(names.indexOf("a")).toBeLessThan(names.indexOf("b"));
      expect(names.indexOf("a")).toBeLessThan(names.indexOf("c"));
      expect(names.indexOf("b")).toBeLessThan(names.indexOf("d"));
      expect(names.indexOf("c")).toBeLessThan(names.indexOf("d"));
    });

    it("throws on circular dependency", () => {
      const steps = [
        makeStep("a", "echo a", { dependsOn: ["b"] }),
        makeStep("b", "echo b", { dependsOn: ["a"] }),
      ];
      expect(() => topologicalSort(steps)).toThrow(/Circular dependency/);
    });

    it("throws on self dependency", () => {
      const steps = [makeStep("a", "echo a", { dependsOn: ["a"] })];
      expect(() => topologicalSort(steps)).toThrow(/Circular dependency/);
    });

    it("throws on unknown dependency", () => {
      const steps = [makeStep("a", "echo a", { dependsOn: ["ghost"] })];
      expect(() => topologicalSort(steps)).toThrow(/unknown step "ghost"/);
    });
  });

  describe("runWorkflow", () => {
    it("rejects concurrent runs of the same workflow", async () => {
      workflowState.running["wf"] = true;
      const wf = makeWorkflow("wf", [makeStep("s")]);
      await runWorkflow(wf, "/proj");
      expect(taskService.requestExecutionApproval).not.toHaveBeenCalled();
    });

    it.each(["mcp", "ai", "git"] as const)(
      "fails closed for unsupported %s steps without requesting command authority",
      async (type) => {
        const wf = makeWorkflow("wf", [
          makeStep("unsafe", "payload-that-must-not-run", { type }),
        ]);

        await runWorkflow(wf, "/proj");

        expect(taskService.requestExecutionApproval).not.toHaveBeenCalled();
        expect(taskService.executeApproved).not.toHaveBeenCalled();
        expect(notifyError).toHaveBeenCalledWith(
          expect.stringContaining(`unsupported step definition "${type}"`),
        );
        expect(workflowState.running.wf).toBeFalsy();
      },
    );

    it("fails closed for a file step that carries command authority", async () => {
      const wf = makeWorkflow("wf", [makeStep("unsafe", "payload", {
        type: "file", tool: "read", input: { path: "notes.txt" },
      })]);

      await runWorkflow(wf, "/proj");

      expect(taskService.requestExecutionApproval).not.toHaveBeenCalled();
      expect(taskService.executeApproved).not.toHaveBeenCalled();
      expect(notifyError).toHaveBeenCalledWith(
        expect.stringContaining('unsupported step definition "file"'),
      );
    });

    it("fails closed for a Skill step that carries command authority", async () => {
      const wf = makeWorkflow("wf", [makeStep("unsafe", "payload", {
        type: "skill", tool: "activate", input: { id: "review" },
      })]);

      await runWorkflow(wf, "/proj");

      expect(taskService.requestExecutionApproval).not.toHaveBeenCalled();
      expect(taskService.executeApproved).not.toHaveBeenCalled();
      expect(notifyError).toHaveBeenCalledWith(
        expect.stringContaining('unsupported step definition "skill"'),
      );
    });

    it.each([
      { tool: "review", input: { id: "review" } },
      { tool: "activate", input: {} },
      { tool: "activate", input: { id: " review " } },
      { tool: "activate", input: { id: "review", scope: "user" } },
    ])("fails closed for a malformed typed Skill shape %#", async ({ tool, input }) => {
      const wf = makeWorkflow("wf", [makeStep("unsafe", "", {
        type: "skill", tool, input,
      })]);

      await runWorkflow(wf, "/proj");

      expect(taskService.requestExecutionApproval).not.toHaveBeenCalled();
      expect(taskService.executeApproved).not.toHaveBeenCalled();
      expect(notifyError).toHaveBeenCalledWith(
        expect.stringContaining('unsupported step definition "skill"'),
      );
    });

    it("stops and does not redeem after runtime cleanup while approval is pending", async () => {
      let resolveApproval: ((token: string) => void) | undefined;
      vi.mocked(taskService.requestExecutionApproval).mockImplementationOnce(
        () => new Promise<string>((resolve) => {
          resolveApproval = resolve;
        }),
      );
      const wf = makeWorkflow("wf", [makeStep("first", "echo")]);

      const run = runWorkflow(wf, "/proj");
      await Promise.resolve();
      await Promise.resolve();
      const [executionId] = vi.mocked(taskService.requestExecutionApproval).mock.calls[0];
      cleanupWorkflowRuntime();
      resolveApproval?.("approval-after-cleanup");
      await run;

      expect(taskService.stop).toHaveBeenCalledWith(executionId);
      expect(taskService.executeApproved).not.toHaveBeenCalled();
      expect(workflowState.running.wf).toBe(false);
    });

    it("runs each step through backend approval and tracked execution", async () => {
      const wf = makeWorkflow("wf", [
        makeStep("first", "echo", { args: ["1"] }),
        makeStep("second", "echo", { args: ["2"] }),
      ]);
      await runWorkflow(wf, "/proj");
      expect(taskService.requestExecutionApproval).toHaveBeenCalledWith(
		"workflow:wf:run", "echo '1'", "/proj",
      );
      expect(taskService.requestExecutionApproval).toHaveBeenCalledWith(
		"workflow:wf:run", "echo '2'", "/proj",
      );
      expect(taskService.executeApproved).toHaveBeenCalledTimes(2);
      expect(workflowState.running["wf"]).toBe(false);
      const states = workflowState.stepStates["wf"];
      expect(states.length).toBe(2);
      expect(states[0].status).toBe("success");
      expect(states[1].status).toBe("success");
    });

    it("uses the authoritative workflow catalog API when available", async () => {
      const requestWorkflowStepApproval = vi.fn().mockResolvedValue("catalog-approval");
      const executeApprovedWorkflowStep = vi.fn().mockResolvedValue(taskResult({
        stdout: "catalog output",
      }));
      Object.assign(taskService, {
        requestWorkflowStepApproval,
        executeApprovedWorkflowStep,
      });

      try {
        const wf = makeWorkflow("catalog-wf", [makeStep("catalog-step", "echo", {
          args: ["renderer-command-must-not-authorize"],
          cwd: "renderer-cwd-must-not-authorize",
        })]);
        await runWorkflow(wf, "/proj");

        expect(requestWorkflowStepApproval).toHaveBeenCalledWith(
          "workflow:wf:run",
          "catalog-wf",
          "catalog-step",
        );
        expect(executeApprovedWorkflowStep).toHaveBeenCalledWith(
          "workflow:wf:run",
          "catalog-wf",
          "catalog-step",
          "catalog-approval",
        );
        expect(taskService.requestExecutionApproval).not.toHaveBeenCalled();
        expect(taskService.executeApproved).not.toHaveBeenCalled();
      } finally {
        Reflect.deleteProperty(taskService, "requestWorkflowStepApproval");
        Reflect.deleteProperty(taskService, "executeApprovedWorkflowStep");
      }
    });

    it("runs a typed file read only through the authoritative catalog API", async () => {
      const requestWorkflowStepApproval = vi.fn().mockResolvedValue("file-approval");
      const executeApprovedWorkflowStep = vi.fn().mockResolvedValue(taskResult({
        command: "workflow-file.read",
        stdout: "catalog file content",
      }));
      Object.assign(taskService, {
        requestWorkflowStepApproval,
        executeApprovedWorkflowStep,
      });

      try {
        const wf = makeWorkflow("file-wf", [makeStep("read-notes", "", {
          type: "file",
          tool: "read",
          input: { path: "notes.txt" },
        })]);
        await runWorkflow(wf, "/proj");

        expect(requestWorkflowStepApproval).toHaveBeenCalledWith(
          "workflow:wf:run",
          "file-wf",
          "read-notes",
        );
        expect(executeApprovedWorkflowStep).toHaveBeenCalledWith(
          "workflow:wf:run",
          "file-wf",
          "read-notes",
          "file-approval",
        );
        expect(taskService.requestExecutionApproval).not.toHaveBeenCalled();
        expect(taskService.executeApproved).not.toHaveBeenCalled();
        expect(workflowState.stepStates["file-wf"][0].status).toBe("success");
      } finally {
        Reflect.deleteProperty(taskService, "requestWorkflowStepApproval");
        Reflect.deleteProperty(taskService, "executeApprovedWorkflowStep");
      }
    });

    it("runs a typed file write only through the authoritative catalog API", async () => {
      const requestWorkflowStepApproval = vi.fn().mockResolvedValue("write-approval");
      const executeApprovedWorkflowStep = vi.fn().mockResolvedValue(taskResult({
        command: "workflow-file.write",
        stdout: "Wrote notes.txt",
      }));
      Object.assign(taskService, {
        requestWorkflowStepApproval,
        executeApprovedWorkflowStep,
      });

      try {
        const wf = makeWorkflow("write-wf", [makeStep("write-notes", "", {
          type: "file",
          tool: "write",
          input: { path: "notes.txt", content: "backend-owned content" },
        })]);
        await runWorkflow(wf, "/proj");

        expect(requestWorkflowStepApproval).toHaveBeenCalledWith(
          "workflow:wf:run",
          "write-wf",
          "write-notes",
        );
        expect(executeApprovedWorkflowStep).toHaveBeenCalledWith(
          "workflow:wf:run",
          "write-wf",
          "write-notes",
          "write-approval",
        );
        expect(taskService.requestExecutionApproval).not.toHaveBeenCalled();
        expect(taskService.executeApproved).not.toHaveBeenCalled();
        expect(workflowState.stepStates["write-wf"][0].status).toBe("success");
        expect(pushOutput).toHaveBeenCalledWith(
          "workflow", "info", 'Step "write-notes": file.write',
        );
      } finally {
        Reflect.deleteProperty(taskService, "requestWorkflowStepApproval");
        Reflect.deleteProperty(taskService, "executeApprovedWorkflowStep");
      }
    });

    it("runs a typed Git status only through the authoritative catalog API", async () => {
      const requestWorkflowStepApproval = vi.fn().mockResolvedValue("git-approval");
      const executeApprovedWorkflowStep = vi.fn().mockResolvedValue(taskResult({
        command: "workflow-git.status",
        stdout: '[{"path":"notes.txt","status":"Modified"}]',
      }));
      Object.assign(taskService, {
        requestWorkflowStepApproval,
        executeApprovedWorkflowStep,
      });

      try {
        const wf = makeWorkflow("git-wf", [makeStep("status", "", {
          type: "git",
          tool: "status",
          input: {},
        })]);
        await runWorkflow(wf, "/proj");

        expect(requestWorkflowStepApproval).toHaveBeenCalledWith(
          "workflow:wf:run",
          "git-wf",
          "status",
        );
        expect(executeApprovedWorkflowStep).toHaveBeenCalledWith(
          "workflow:wf:run",
          "git-wf",
          "status",
          "git-approval",
        );
        expect(taskService.requestExecutionApproval).not.toHaveBeenCalled();
        expect(taskService.executeApproved).not.toHaveBeenCalled();
        expect(workflowState.stepStates["git-wf"][0].status).toBe("success");
      } finally {
        Reflect.deleteProperty(taskService, "requestWorkflowStepApproval");
        Reflect.deleteProperty(taskService, "executeApprovedWorkflowStep");
      }
    });

    it("runs a typed AI step only through the authoritative catalog API", async () => {
      const requestWorkflowStepApproval = vi.fn().mockResolvedValue("ai-approval");
      const executeApprovedWorkflowStep = vi.fn().mockResolvedValue(taskResult({
        command: "workflow-ai", stdout: "generated answer",
      }));
      Object.assign(taskService, { requestWorkflowStepApproval, executeApprovedWorkflowStep });

      try {
        const wf = makeWorkflow("ai-wf", [makeStep("generate", "", {
          type: "ai",
          tool: "generate",
          input: { prompt: "backend-owned prompt" },
        })]);
        await runWorkflow(wf, "/proj");

        expect(requestWorkflowStepApproval).toHaveBeenCalledWith(
          "workflow:wf:run", "ai-wf", "generate",
        );
        expect(executeApprovedWorkflowStep).toHaveBeenCalledWith(
          "workflow:wf:run", "ai-wf", "generate", "ai-approval",
        );
        expect(taskService.requestExecutionApproval).not.toHaveBeenCalled();
        expect(taskService.executeApproved).not.toHaveBeenCalled();
        expect(workflowState.stepStates["ai-wf"][0].status).toBe("success");
        expect(pushOutput).toHaveBeenCalledWith(
          "workflow", "info", 'Step "generate": ai.generate',
        );
      } finally {
        Reflect.deleteProperty(taskService, "requestWorkflowStepApproval");
        Reflect.deleteProperty(taskService, "executeApprovedWorkflowStep");
      }
    });

    it("runs a typed MCP step only through the authoritative catalog API", async () => {
      const requestWorkflowStepApproval = vi.fn().mockResolvedValue("mcp-approval");
      const executeApprovedWorkflowStep = vi.fn().mockResolvedValue(taskResult({
        command: "workflow-mcp.call",
        stdout: '{"content":[{"type":"text","text":"ok"}]}',
      }));
      Object.assign(taskService, {
        requestWorkflowStepApproval,
        executeApprovedWorkflowStep,
      });

      try {
        const wf = makeWorkflow("mcp-wf", [makeStep("lookup", "", {
          type: "mcp",
          tool: "mcp.docs.lookup",
          input: { query: "catalog-owned" },
        })]);
        await runWorkflow(wf, "/proj");

        expect(requestWorkflowStepApproval).toHaveBeenCalledWith(
          "workflow:wf:run",
          "mcp-wf",
          "lookup",
        );
        expect(executeApprovedWorkflowStep).toHaveBeenCalledWith(
          "workflow:wf:run",
          "mcp-wf",
          "lookup",
          "mcp-approval",
        );
        expect(taskService.requestExecutionApproval).not.toHaveBeenCalled();
        expect(taskService.executeApproved).not.toHaveBeenCalled();
        expect(workflowState.stepStates["mcp-wf"][0].status).toBe("success");
      } finally {
        Reflect.deleteProperty(taskService, "requestWorkflowStepApproval");
        Reflect.deleteProperty(taskService, "executeApprovedWorkflowStep");
      }
    });

    it("runs a typed Skill step only through the authoritative catalog API", async () => {
      const requestWorkflowStepApproval = vi.fn().mockResolvedValue("skill-approval");
      const executeApprovedWorkflowStep = vi.fn().mockResolvedValue(taskResult({
        command: "workflow-skill.activate",
        stdout: "Activated skill review.",
      }));
      Object.assign(taskService, {
        requestWorkflowStepApproval,
        executeApprovedWorkflowStep,
      });

      try {
        const wf = makeWorkflow("skill-wf", [makeStep("review", "", {
          type: "skill",
          tool: "activate",
          input: { id: "review" },
        })]);
        await runWorkflow(wf, "/proj");

        expect(requestWorkflowStepApproval).toHaveBeenCalledWith(
          "workflow:wf:run",
          "skill-wf",
          "review",
        );
        expect(executeApprovedWorkflowStep).toHaveBeenCalledWith(
          "workflow:wf:run",
          "skill-wf",
          "review",
          "skill-approval",
        );
        expect(taskService.requestExecutionApproval).not.toHaveBeenCalled();
        expect(taskService.executeApproved).not.toHaveBeenCalled();
        expect(workflowState.stepStates["skill-wf"][0].status).toBe("success");
        expect(pushOutput).toHaveBeenCalledWith(
          "workflow",
          "info",
          'Step "review": skill.activate:review',
        );
      } finally {
        Reflect.deleteProperty(taskService, "requestWorkflowStepApproval");
        Reflect.deleteProperty(taskService, "executeApprovedWorkflowStep");
      }
    });

		it("owns one backend lifecycle session for all steps", async () => {
			const wf = makeWorkflow("wf", [makeStep("first"), makeStep("second")]);
			await runWorkflow(wf, "/proj");
			expect(taskService.beginWorkflowExecution).toHaveBeenCalledWith("wf");
			expect(taskService.requestExecutionApproval).toHaveBeenNthCalledWith(
				1, "workflow:wf:run", "echo", "/proj",
			);
			expect(taskService.requestExecutionApproval).toHaveBeenNthCalledWith(
				2, "workflow:wf:run", "echo", "/proj",
			);
			expect(taskService.completeWorkflowExecution).toHaveBeenCalledWith("workflow:wf:run");
			expect(taskService.failWorkflowExecution).not.toHaveBeenCalled();
		});

    it("runs steps in dependency order", async () => {
      const wf = makeWorkflow("wf", [
        makeStep("build", "go", { args: ["build"], dependsOn: ["lint"] }),
        makeStep("lint", "golangci-lint", { args: ["run"] }),
      ]);
      await runWorkflow(wf, "/proj");
      const calls = vi.mocked(taskService.requestExecutionApproval).mock.calls;
      const lintCall = calls.find((call) => call[1] === "golangci-lint 'run'");
      const buildCall = calls.find((call) => call[1] === "go 'build'");
      expect(lintCall).toBeTruthy();
      expect(buildCall).toBeTruthy();
      expect(calls.indexOf(lintCall!)).toBeLessThan(calls.indexOf(buildCall!));
    });

    it("skips steps with falsy condition", async () => {
      const wf = makeWorkflow("wf", [
        makeStep("runs", "echo", { args: ["yes"] }),
        makeStep("skipped", "echo", { args: ["no"], condition: "false" }),
      ]);
      await runWorkflow(wf, "/proj");
      expect(taskService.requestExecutionApproval).toHaveBeenCalledWith(
        expect.any(String), "echo 'yes'", "/proj",
      );
      expect(taskService.requestExecutionApproval).not.toHaveBeenCalledWith(
        expect.any(String), "echo 'no'", "/proj",
      );
      const states = workflowState.stepStates["wf"];
      expect(states[0].status).toBe("success");
      expect(states[1].status).toBe("skipped");
    });

    it("surfaces error when backend approval fails", async () => {
      vi.mocked(taskService.requestExecutionApproval).mockRejectedValueOnce(new Error("denied"));
      const wf = makeWorkflow("wf", [makeStep("s")]);
      await runWorkflow(wf, "/proj");
      expect(notifyError).toHaveBeenCalled();
      expect(taskService.executeApproved).not.toHaveBeenCalled();
      expect(workflowState.running["wf"]).toBe(false);
    });

    it("marks step as failed on non-zero exit code", async () => {
      vi.mocked(taskService.executeApproved).mockResolvedValueOnce(taskResult({ exitCode: 1 }));
      const wf = makeWorkflow("wf", [
        makeStep("fail", "false"),
        makeStep("after", "echo", { args: ["after"] }),
      ]);
      await runWorkflow(wf, "/proj");
      const states = workflowState.stepStates["wf"];
      expect(states[0].status).toBe("failed");
      expect(states[0].error).toBe("Exit code: 1");
      // Second step should be skipped (failed = true aborts).
      expect(states[1].status).toBe("skipped");
      // Should not have run the second step.
      expect(taskService.executeApproved).toHaveBeenCalledTimes(1);
      expect(taskService.requestExecutionApproval).not.toHaveBeenCalledWith(
        expect.any(String), "echo 'after'", "/proj",
      );
      // Workflow failure notification.
      expect(notifyError).toHaveBeenCalledWith(expect.stringContaining("failed"));
			expect(taskService.failWorkflowExecution).toHaveBeenCalledWith(
				"workflow:wf:run", "one or more workflow steps failed",
			);
    });

    it("continues after failed step when expectSuccess is false", async () => {
      vi.mocked(taskService.executeApproved)
        .mockResolvedValueOnce(taskResult({ exitCode: 1 }))
        .mockResolvedValueOnce(taskResult());
      const wf = makeWorkflow("wf", [
        makeStep("maybe-fail", "false", { expectSuccess: false }),
        makeStep("after", "echo", { args: ["after"] }),
      ]);
      await runWorkflow(wf, "/proj");
      const states = workflowState.stepStates["wf"];
      expect(states[0].status).toBe("failed");
      expect(states[0].error).toBe("Exit code: 1");
      // Second step should still run (expectSuccess: false = non-fatal).
      expect(states[1].status).toBe("success");
      expect(taskService.executeApproved).toHaveBeenCalledTimes(2);
      expect(taskService.requestExecutionApproval).toHaveBeenCalledWith(
        expect.any(String), "echo 'after'", "/proj",
      );
      // Workflow is still marked as failed (a step failed).
      expect(notifyError).toHaveBeenCalledWith(expect.stringContaining("failed"));
    });

    it("marks step as failed on timeout (-1)", async () => {
      vi.mocked(taskService.executeApproved).mockResolvedValueOnce(taskResult({ exitCode: -1 }));
      const wf = makeWorkflow("wf", [makeStep("hang", "sleep", { args: ["9999"] })]);
      await runWorkflow(wf, "/proj");
      const states = workflowState.stepStates["wf"];
      expect(states[0].status).toBe("failed");
      expect(states[0].error).toBe("Timed out or session ended");
    });

    it("surfaces error when topological sort fails", async () => {
      const wf = makeWorkflow("wf", [
        makeStep("a", "echo a", { dependsOn: ["b"] }),
        makeStep("b", "echo b", { dependsOn: ["a"] }),
      ]);
      await runWorkflow(wf, "/proj");
      expect(notifyError).toHaveBeenCalledWith(expect.stringContaining("invalid"));
      expect(taskService.requestExecutionApproval).not.toHaveBeenCalled();
      // The early-return path never sets running[wf] to true, so it stays
      // undefined — which is falsy and correctly indicates "not running".
      expect(workflowState.running["wf"]).toBeFalsy();
    });

    it("emits workflow lifecycle output entries", async () => {
      const wf = makeWorkflow("wf", [makeStep("s", "echo")]);
      await runWorkflow(wf, "/proj");
      // Should have at least: "Starting workflow", step info, "completed".
      const sources = (pushOutput as any).mock.calls.map((c: any[]) => c[0]);
      expect(sources.every((s: string) => s === "workflow")).toBe(true);
      const messages = (pushOutput as any).mock.calls.map((c: any[]) => c[2]);
      expect(messages.some((m: string) => m.includes("Starting workflow"))).toBe(true);
      expect(messages.some((m: string) => m.includes("completed"))).toBe(true);
    });

    it("removes completed executions from the cleanup stop set", async () => {
      const wf = makeWorkflow("wf", [makeStep("s", "echo")]);
      await runWorkflow(wf, "/proj");
      cleanupWorkflowRuntime();
      expect(taskService.stop).not.toHaveBeenCalled();
    });

    it("removes failed executions from the cleanup stop set", async () => {
      vi.mocked(taskService.executeApproved).mockResolvedValueOnce(taskResult({ exitCode: 1 }));
      const wf = makeWorkflow("wf", [makeStep("fail", "false")]);
      await runWorkflow(wf, "/proj");
      cleanupWorkflowRuntime();
      expect(taskService.stop).not.toHaveBeenCalled();
    });

    it("does not leave an active execution when approval fails", async () => {
      vi.mocked(taskService.requestExecutionApproval).mockRejectedValueOnce(new Error("denied"));
      const wf = makeWorkflow("wf", [makeStep("s")]);
      await runWorkflow(wf, "/proj");
      cleanupWorkflowRuntime();
      expect(taskService.stop).not.toHaveBeenCalled();
    });
  });

  // --- Plan 65 / Proposal B: glob matching and file:saved triggers ---

  describe("matchGlob", () => {
    it("matches exact path", () => {
      expect(matchGlob("main.go", "main.go")).toBe(true);
      expect(matchGlob("main.go", "main.ts")).toBe(false);
    });
    it("matches * within a single segment", () => {
      expect(matchGlob("main.go", "*.go")).toBe(true);
      expect(matchGlob("util.go", "*.go")).toBe(true);
      expect(matchGlob("main.ts", "*.go")).toBe(false);
    });
    it("* does not cross segment boundary", () => {
      expect(matchGlob("src/main.go", "*.go")).toBe(false);
    });
    it("matches ** across segments", () => {
      expect(matchGlob("src/main.go", "**/*.go")).toBe(true);
      expect(matchGlob("src/util/helper.go", "**/*.go")).toBe(true);
      expect(matchGlob("main.go", "**/*.go")).toBe(true);
    });
    it("matches ** in the middle of a pattern", () => {
      expect(matchGlob("src/util/helper.ts", "src/**/*.ts")).toBe(true);
      expect(matchGlob("src/helper.ts", "src/**/*.ts")).toBe(true);
      expect(matchGlob("src/util/helper.ts", "src/*.ts")).toBe(false);
    });
    it("matches ? as single char", () => {
      expect(matchGlob("a.go", "?.go")).toBe(true);
      expect(matchGlob("ab.go", "?.go")).toBe(false);
    });
    it("matches **/* (catch-all)", () => {
      expect(matchGlob("anything.go", "**/*")).toBe(true);
      expect(matchGlob("src/deep/path/file.ts", "**/*")).toBe(true);
    });
    it("matches specific nested path", () => {
      expect(matchGlob("src/main.go", "src/main.go")).toBe(true);
      expect(matchGlob("src/main.ts", "src/main.go")).toBe(false);
    });
    it("matches prefix with **", () => {
      expect(matchGlob("src/a/b/c.go", "src/**")).toBe(true);
      expect(matchGlob("lib/a.go", "src/**")).toBe(false);
    });
    it("matches * in middle of segment", () => {
      expect(matchGlob("foo.test.js", "*.test.js")).toBe(true);
      expect(matchGlob("foo.js", "*.test.js")).toBe(false);
    });
  });

  describe("relativizePath", () => {
    it("strips project root prefix (forward slashes)", () => {
      expect(relativizePath("/proj/src/main.go", "/proj")).toBe("src/main.go");
    });
    it("strips trailing slash from root", () => {
      expect(relativizePath("/proj/src/main.go", "/proj/")).toBe("src/main.go");
    });
    it("normalizes backslashes to forward slashes", () => {
      expect(relativizePath("C:\\proj\\src\\main.go", "C:\\proj")).toBe("src/main.go");
    });
    it("returns empty for root itself", () => {
      expect(relativizePath("/proj", "/proj")).toBe("");
    });
    it("returns normalized path when not under root", () => {
      expect(relativizePath("/other/file.go", "/proj")).toBe("/other/file.go");
    });
  });

  describe("findTriggeredWorkflows", () => {
    it("does not auto-run workflows that require confirmation", () => {
      const wf: WorkflowDef = {
        ...makeWorkflow("guarded", [makeStep("run")]),
        requiresConfirmation: true,
        runOn: { event: "file-saved", glob: "**/*.go" },
      };

      expect(findTriggeredWorkflows([wf], "src/main.go", {})).toEqual([]);
    });

    it("returns workflow with matching glob", () => {
      const wf: WorkflowDef = {
        name: "auto-test",
        steps: [makeStep("test")],
        source: "auto-test.yml",
        runOn: { event: "file-saved", glob: "**/*.go" },
      };
      const result = findTriggeredWorkflows([wf], "src/main.go", {});
      expect(result.length).toBe(1);
      expect(result[0].name).toBe("auto-test");
    });
    it("skips workflow with non-matching glob", () => {
      const wf: WorkflowDef = {
        name: "auto-test",
        steps: [makeStep("test")],
        source: "auto-test.yml",
        runOn: { event: "file-saved", glob: "**/*.go" },
      };
      const result = findTriggeredWorkflows([wf], "src/main.ts", {});
      expect(result.length).toBe(0);
    });
    it("skips workflow without runOn", () => {
      const wf: WorkflowDef = {
        name: "manual",
        steps: [makeStep("build")],
        source: "manual.yml",
      };
      const result = findTriggeredWorkflows([wf], "src/main.go", {});
      expect(result.length).toBe(0);
    });
    it("skips workflow with different event", () => {
      const wf: WorkflowDef = {
        name: "on-startup",
        steps: [makeStep("build")],
        source: "on-startup.yml",
        runOn: { event: "startup", glob: "**/*.go" },
      };
      const result = findTriggeredWorkflows([wf], "src/main.go", {});
      expect(result.length).toBe(0);
    });
    it("skips already-running workflow", () => {
      const wf: WorkflowDef = {
        name: "auto-test",
        steps: [makeStep("test")],
        source: "auto-test.yml",
        runOn: { event: "file-saved", glob: "**/*.go" },
      };
      const result = findTriggeredWorkflows([wf], "src/main.go", { "auto-test": true });
      expect(result.length).toBe(0);
    });
    it("uses **/* as default glob when glob is empty", () => {
      const wf: WorkflowDef = {
        name: "auto-test",
        steps: [makeStep("test")],
        source: "auto-test.yml",
        runOn: { event: "file-saved" },
      };
      const result = findTriggeredWorkflows([wf], "any/file.txt", {});
      expect(result.length).toBe(1);
    });
    it("returns multiple matching workflows", () => {
      const wf1: WorkflowDef = {
        name: "test-go",
        steps: [makeStep("test")],
        source: "test-go.yml",
        runOn: { event: "file-saved", glob: "**/*.go" },
      };
      const wf2: WorkflowDef = {
        name: "lint-go",
        steps: [makeStep("lint")],
        source: "lint-go.yml",
        runOn: { event: "file-saved", glob: "**/*.go" },
      };
      const wf3: WorkflowDef = {
        name: "test-ts",
        steps: [makeStep("test")],
        source: "test-ts.yml",
        runOn: { event: "file-saved", glob: "**/*.ts" },
      };
      const result = findTriggeredWorkflows([wf1, wf2, wf3], "src/main.go", {});
      expect(result.length).toBe(2);
      expect(result.map((w) => w.name).sort()).toEqual(["lint-go", "test-go"]);
    });
    it("returns empty for empty relPath", () => {
      const wf: WorkflowDef = {
        name: "auto-test",
        steps: [makeStep("test")],
        source: "auto-test.yml",
        runOn: { event: "file-saved", glob: "**/*" },
      };
      const result = findTriggeredWorkflows([wf], "", {});
      expect(result.length).toBe(0);
    });
  });

  // Proposal J (prompt-4.md): runOn.event === "startup" workflows
  describe("findStartupWorkflows", () => {
    it("returns workflows with runOn.event startup", () => {
      const wf: WorkflowDef = {
        name: "bootstrap",
        steps: [makeStep("init")],
        source: "bootstrap.yml",
        runOn: { event: "startup" },
      };
      const result = findStartupWorkflows([wf], {});
      expect(result.length).toBe(1);
      expect(result[0].name).toBe("bootstrap");
    });

    it("skips workflows without runOn", () => {
      const wf: WorkflowDef = {
        name: "manual",
        steps: [makeStep("build")],
        source: "manual.yml",
      };
      const result = findStartupWorkflows([wf], {});
      expect(result.length).toBe(0);
    });

    it("skips workflows with file-saved event", () => {
      const wf: WorkflowDef = {
        name: "auto-test",
        steps: [makeStep("test")],
        source: "auto-test.yml",
        runOn: { event: "file-saved", glob: "**/*.go" },
      };
      const result = findStartupWorkflows([wf], {});
      expect(result.length).toBe(0);
    });

    it("skips already-running workflows", () => {
      const wf: WorkflowDef = {
        name: "bootstrap",
        steps: [makeStep("init")],
        source: "bootstrap.yml",
        runOn: { event: "startup" },
      };
      const result = findStartupWorkflows([wf], { bootstrap: true });
      expect(result.length).toBe(0);
    });

    it("returns multiple startup workflows", () => {
      const wf1: WorkflowDef = {
        name: "bootstrap",
        steps: [makeStep("init")],
        source: "bootstrap.yml",
        runOn: { event: "startup" },
      };
      const wf2: WorkflowDef = {
        name: "sync-deps",
        steps: [makeStep("sync")],
        source: "sync-deps.yml",
        runOn: { event: "startup" },
      };
      const wf3: WorkflowDef = {
        name: "auto-test",
        steps: [makeStep("test")],
        source: "auto-test.yml",
        runOn: { event: "file-saved", glob: "**/*.go" },
      };
      const result = findStartupWorkflows([wf1, wf2, wf3], {});
      expect(result.length).toBe(2);
      expect(result.map((w) => w.name).sort()).toEqual(["bootstrap", "sync-deps"]);
    });
  });

  describe("findStartupWorkflows", () => {
    // G-SEC-03: findStartupWorkflows is a pure lookup that lists startup
    // workflows for user confirmation. It must NOT auto-execute them.
    it("lists workflows for confirmation without executing them", () => {
      const wf: WorkflowDef = {
        name: "bootstrap",
        steps: [makeStep("init")],
        source: "bootstrap.yml",
        runOn: { event: "startup" },
      };
      const result = findStartupWorkflows([wf], {});
      expect(result.length).toBe(1);
      expect(taskService.requestExecutionApproval).not.toHaveBeenCalled();
    });
  });

  // --- N-58 (Proposal R): workflow chain triggers ---

  describe("findChainTriggeredWorkflows", () => {
    it("does not chain workflows that require confirmation", () => {
      const wf: WorkflowDef = {
        ...makeWorkflow("guarded", [makeStep("run")]),
        requiresConfirmation: true,
        runOn: { event: "workflow-completed", workflowName: "build" },
      };

      expect(findChainTriggeredWorkflows([wf], "build", {})).toEqual([]);
    });

    it("returns workflows with workflow-completed trigger", () => {
      const wf: WorkflowDef = {
        name: "deploy",
        steps: [makeStep("deploy")],
        source: "deploy.yml",
        runOn: { event: "workflow-completed" },
      };
      const result = findChainTriggeredWorkflows([wf], "build", {});
      expect(result).toHaveLength(1);
      expect(result[0].name).toBe("deploy");
    });

    it("filters by workflowName when set", () => {
      const wf: WorkflowDef = {
        name: "deploy",
        steps: [makeStep("deploy")],
        source: "deploy.yml",
        runOn: { event: "workflow-completed", workflowName: "build" },
      };
      expect(findChainTriggeredWorkflows([wf], "build", {})).toHaveLength(1);
      expect(findChainTriggeredWorkflows([wf], "test", {})).toHaveLength(0);
    });

    it("matches any workflow when workflowName is empty", () => {
      const wf: WorkflowDef = {
        name: "notify",
        steps: [makeStep("notify")],
        source: "notify.yml",
        runOn: { event: "workflow-completed" },
      };
      expect(findChainTriggeredWorkflows([wf], "build", {})).toHaveLength(1);
      expect(findChainTriggeredWorkflows([wf], "test", {})).toHaveLength(1);
      expect(findChainTriggeredWorkflows([wf], "anything", {})).toHaveLength(1);
    });

    it("skips workflows without workflow-completed trigger", () => {
      const wfs: WorkflowDef[] = [
        {
          name: "auto-test",
          steps: [makeStep("test")],
          source: "auto-test.yml",
          runOn: { event: "file-saved", glob: "**/*.go" },
        },
        {
          name: "bootstrap",
          steps: [makeStep("init")],
          source: "bootstrap.yml",
          runOn: { event: "startup" },
        },
      ];
      const result = findChainTriggeredWorkflows(wfs, "build", {});
      expect(result).toHaveLength(0);
    });

    it("skips already-running workflows", () => {
      const wf: WorkflowDef = {
        name: "deploy",
        steps: [makeStep("deploy")],
        source: "deploy.yml",
        runOn: { event: "workflow-completed" },
      };
      const result = findChainTriggeredWorkflows([wf], "build", { deploy: true });
      expect(result).toHaveLength(0);
    });

    it("prevents a workflow from triggering itself", () => {
      const wf: WorkflowDef = {
        name: "self-loop",
        steps: [makeStep("loop")],
        source: "loop.yml",
        runOn: { event: "workflow-completed", workflowName: "self-loop" },
      };
      const result = findChainTriggeredWorkflows([wf], "self-loop", {});
      expect(result).toHaveLength(0);
    });

    it("returns multiple matching workflows", () => {
      const wfs: WorkflowDef[] = [
        {
          name: "deploy",
          steps: [makeStep("deploy")],
          source: "deploy.yml",
          runOn: { event: "workflow-completed", workflowName: "build" },
        },
        {
          name: "notify",
          steps: [makeStep("notify")],
          source: "notify.yml",
          runOn: { event: "workflow-completed" },
        },
        {
          name: "unrelated",
          steps: [makeStep("noop")],
          source: "unrelated.yml",
          runOn: { event: "workflow-completed", workflowName: "test" },
        },
      ];
      const result = findChainTriggeredWorkflows(wfs, "build", {});
      expect(result).toHaveLength(2);
      expect(result.map((w) => w.name).sort()).toEqual(["deploy", "notify"]);
    });
  });

  // --- Proposal F (prompt-5.md): workflow outputs field support ---

  describe("extractStepOutputs", () => {
    it("returns empty object when templates is undefined", () => {
      expect(extractStepOutputs("any output", undefined)).toEqual({});
    });

    it("returns empty object when templates is empty", () => {
      expect(extractStepOutputs("any output", {})).toEqual({});
    });

    it("extracts {{stdout}} template as trimmed stdout", () => {
      const result = extractStepOutputs("  v1.2.3\n  ", { tag: "{{stdout}}" });
      expect(result.tag).toBe("v1.2.3");
    });

    it("extracts {{stdout}} from multi-line output (trimmed)", () => {
      const stdout = "Building...\nCompiling...\nv1.2.3\nDone";
      const result = extractStepOutputs(stdout, { version: "{{stdout}}" });
      expect(result.version).toBe(stdout.trim());
    });

    it("extracts {{regex:pattern}} with capturing group 1", () => {
      const stdout = "Version: v1.2.3 (release)";
      const result = extractStepOutputs(stdout, {
        major: "{{regex:v(\\d+)}}",
      });
      expect(result.major).toBe("1");
    });

    it("extracts {{regex:pattern}} with full match when no group", () => {
      const stdout = "commit abc123def456";
      const result = extractStepOutputs(stdout, {
        hash: "{{regex:[a-f0-9]{8,}}}",
      });
      expect(result.hash).toBe("abc123def456");
    });

    it("returns empty string when regex does not match", () => {
      const result = extractStepOutputs("nothing here", {
        tag: "{{regex:v(\\d+)}}",
      });
      expect(result.tag).toBe("");
    });

    it("returns empty string when regex is invalid", () => {
      const result = extractStepOutputs("anything", {
        bad: "{{regex:[invalid)}}}",
      });
      expect(result.bad).toBe("");
    });

    it("returns literal value for non-template strings", () => {
      const result = extractStepOutputs("anything", {
        literal: "static-value",
      });
      expect(result.literal).toBe("static-value");
    });

    it("extracts multiple outputs from same stdout", () => {
      const stdout = "branch=main\ncommit=abc123\nversion=1.0.0";
      const result = extractStepOutputs(stdout, {
        branch: "{{regex:branch=(\\w+)}}",
        commit: "{{regex:commit=(\\w+)}}",
        version: "{{regex:version=(\\S+)}}",
      });
      expect(result.branch).toBe("main");
      expect(result.commit).toBe("abc123");
      expect(result.version).toBe("1.0.0");
    });

    it("trims whitespace around template syntax", () => {
      const result = extractStepOutputs("v1.2.3", { tag: "  {{stdout}}  " });
      expect(result.tag).toBe("v1.2.3");
    });
  });

  describe("substituteOutputRefs", () => {
    const lookup = (name: string) => {
      const outputs: Record<string, Record<string, string>> = {
        version: { tag: "v1.2.3", major: "1" },
        build: { hash: "abc123" },
      };
      return outputs[name];
    };

    it("substitutes a single output reference", () => {
      expect(
        substituteOutputRefs("echo {{steps.version.outputs.tag}}", lookup),
      ).toBe("echo v1.2.3");
    });

    it("substitutes multiple references in one command", () => {
      expect(
        substituteOutputRefs(
          "docker build -t app:{{steps.version.outputs.tag}} --build-arg MAJOR={{steps.version.outputs.major}} .",
          lookup,
        ),
      ).toBe("docker build -t app:v1.2.3 --build-arg MAJOR=1 .");
    });

    it("substitutes references from different steps", () => {
      expect(
        substituteOutputRefs(
          "git tag {{steps.version.outputs.tag}} && git rev-parse {{steps.build.outputs.hash}}",
          lookup,
        ),
      ).toBe("git tag v1.2.3 && git rev-parse abc123");
    });

    it("leaves placeholder when output does not exist", () => {
      const emptyLookup = () => undefined;
      expect(
        substituteOutputRefs("echo {{steps.missing.outputs.tag}}", emptyLookup),
      ).toBe("echo {{steps.missing.outputs.tag}}");
    });

    it("leaves placeholder when key does not exist", () => {
      expect(
        substituteOutputRefs("echo {{steps.version.outputs.nonexistent}}", lookup),
      ).toBe("echo {{steps.version.outputs.nonexistent}}");
    });

    it("does not modify commands without placeholders", () => {
      expect(substituteOutputRefs("echo hello", lookup)).toBe("echo hello");
    });

    it("handles empty command", () => {
      expect(substituteOutputRefs("", lookup)).toBe("");
    });

    it("supports step names with hyphens and underscores", () => {
      const lookup2 = (name: string) =>
        name === "my-step_name" ? { value: "yes" } : undefined;
      expect(
        substituteOutputRefs("echo {{steps.my-step_name.outputs.value}}", lookup2),
      ).toBe("echo yes");
    });

    it("supports output keys with hyphens and underscores", () => {
      const lookup2 = () => ({ "my-key_name": "value" });
      expect(
        substituteOutputRefs("echo {{steps.s.outputs.my-key_name}}", lookup2),
      ).toBe("echo value");
    });
  });

  describe("evaluateCondition with outputs", () => {
    const outputs = (s: Record<string, Record<string, string>>) =>
      (name: string) => s[name];

    it("steps.x.outputs.y is true when output is non-empty", () => {
      expect(
        evaluateCondition(
          "steps.version.outputs.tag",
          undefined,
          outputs({ version: { tag: "v1.2.3" } }),
        ),
      ).toBe(true);
    });

    it("steps.x.outputs.y is false when output is empty string", () => {
      expect(
        evaluateCondition(
          "steps.version.outputs.tag",
          undefined,
          outputs({ version: { tag: "" } }),
        ),
      ).toBe(false);
    });

    it("steps.x.outputs.y is false when output key does not exist", () => {
      expect(
        evaluateCondition(
          "steps.version.outputs.tag",
          undefined,
          outputs({ version: { other: "x" } }),
        ),
      ).toBe(false);
    });

    it("steps.x.outputs.y is false when step does not exist", () => {
      expect(
        evaluateCondition(
          "steps.missing.outputs.tag",
          undefined,
          outputs({}),
        ),
      ).toBe(false);
    });

    it("steps.x.outputs.y is false when stepOutputs is undefined", () => {
      expect(evaluateCondition("steps.version.outputs.tag")).toBe(false);
    });

    it("combines outputs with status via &&", () => {
      expect(
        evaluateCondition(
          "steps.build.success && steps.version.outputs.tag",
          (name) => (name === "build" ? "success" : undefined),
          outputs({ version: { tag: "v1.0.0" } }),
        ),
      ).toBe(true);
    });

    it("combines outputs with status via || (one true)", () => {
      expect(
        evaluateCondition(
          "steps.build.failed || steps.version.outputs.tag",
          (name) => (name === "build" ? "success" : undefined),
          outputs({ version: { tag: "v1.0.0" } }),
        ),
      ).toBe(true);
    });

    it("combines outputs with status via || (both false)", () => {
      expect(
        evaluateCondition(
          "steps.build.failed || steps.version.outputs.tag",
          (name) => (name === "build" ? "success" : undefined),
          outputs({ version: { tag: "" } }),
        ),
      ).toBe(false);
    });

    it("supports ! on output reference", () => {
      expect(
        evaluateCondition(
          "!steps.version.outputs.tag",
          undefined,
          outputs({ version: { tag: "" } }),
        ),
      ).toBe(true);
    });

    it("supports ! on output reference when non-empty", () => {
      expect(
        evaluateCondition(
          "!steps.version.outputs.tag",
          undefined,
          outputs({ version: { tag: "v1" } }),
        ),
      ).toBe(false);
    });

    it("supports output reference in parentheses", () => {
      expect(
        evaluateCondition(
          "(steps.version.outputs.tag)",
          undefined,
          outputs({ version: { tag: "v1" } }),
        ),
      ).toBe(true);
    });
  });

  describe("runWorkflow with outputs (Proposal F integration)", () => {
    beforeEach(async () => {
      vi.clearAllMocks();
      vi.mocked(taskService.requestExecutionApproval).mockResolvedValue("approval-1");
      vi.mocked(taskService.executeApproved).mockResolvedValue(taskResult());
      vi.mocked(taskService.stop).mockResolvedValue(undefined);
      workflowState.workflows = [];
      workflowState.running = {};
      workflowState.stepStates = {};
      // Wait for any background runWorkflow calls from previous tests
      // (e.g. chain triggers fire runWorkflow without awaiting) to
      // complete so their mock calls don't leak into this test's assertions.
      await new Promise((r) => setTimeout(r, 50));
    });

    it("uses tracked command stdout when a step declares outputs", async () => {
      vi.mocked(taskService.executeApproved).mockResolvedValue(taskResult({ stdout: "v1.2.3" }));
      const wf = makeWorkflow("wf", [
        makeStep("version", "git describe --tags", {
          outputs: { tag: "{{stdout}}" },
        }),
      ]);
      await runWorkflow(wf, "/proj");
      expect(taskService.requestExecutionApproval).toHaveBeenCalledWith(
        expect.any(String), "git describe --tags", "/proj",
      );
      const states = workflowState.stepStates["wf"];
      expect(states[0].status).toBe("success");
      expect(states[0].outputs).toEqual({ tag: "v1.2.3" });
    });

    it("uses the same tracked executor when a step has no outputs", async () => {
      const wf = makeWorkflow("wf", [makeStep("build", "go build")]);
      await runWorkflow(wf, "/proj");
      expect(taskService.requestExecutionApproval).toHaveBeenCalledWith(
        expect.any(String), "go build", "/proj",
      );
      expect(taskService.executeApproved).toHaveBeenCalledTimes(1);
    });

    it("substitutes {{steps.x.outputs.y}} in later step commands", async () => {
      vi.mocked(taskService.executeApproved)
        .mockResolvedValueOnce(taskResult({ stdout: "v2.0.0" }))
        .mockResolvedValueOnce(taskResult());
      const wf = makeWorkflow("wf", [
        makeStep("version", "git describe --tags", {
          outputs: { tag: "{{stdout}}" },
        }),
        makeStep("tag", "docker build -t app:{{steps.version.outputs.tag}} ."),
      ]);
      await runWorkflow(wf, "/proj");
      expect(taskService.requestExecutionApproval).toHaveBeenCalledWith(
        expect.any(String), "git describe --tags", "/proj",
      );
      expect(taskService.requestExecutionApproval).toHaveBeenCalledWith(
        expect.any(String), "docker build -t app:v2.0.0 .", "/proj",
      );
    });

    it("does not extract outputs when step fails", async () => {
      vi.mocked(taskService.executeApproved).mockResolvedValue(
        taskResult({ exitCode: 1, stdout: "error output" }),
      );
      const wf = makeWorkflow("wf", [
        makeStep("version", "git describe", {
          outputs: { tag: "{{stdout}}" },
        }),
      ]);
      await runWorkflow(wf, "/proj");
      const states = workflowState.stepStates["wf"];
      expect(states[0].status).toBe("failed");
      expect(states[0].outputs).toBeUndefined();
    });

    it("extracts outputs via regex template", async () => {
      vi.mocked(taskService.executeApproved).mockResolvedValue(
        taskResult({ stdout: "BUILD v3.1.0 RELEASE" }),
      );
      const wf = makeWorkflow("wf", [
        makeStep("build", "make version", {
          outputs: {
            full: "{{stdout}}",
            major: "{{regex:v(\\d+)}}",
          },
        }),
      ]);
      await runWorkflow(wf, "/proj");
      const states = workflowState.stepStates["wf"];
      expect(states[0].status).toBe("success");
      expect(states[0].outputs?.full).toBe("BUILD v3.1.0 RELEASE");
      expect(states[0].outputs?.major).toBe("3");
    });

    it("leaves placeholder when referenced output is missing", async () => {
      vi.mocked(taskService.executeApproved).mockResolvedValue(taskResult());
      const wf = makeWorkflow("wf", [
        makeStep("empty", "echo", { outputs: { tag: "{{stdout}}" } }),
        makeStep("next", "echo {{steps.empty.outputs.nonexistent}}"),
      ]);
      await runWorkflow(wf, "/proj");
      // Placeholder should be left as-is (empty output → empty value, but
      // the key 'nonexistent' was never declared, so it stays as placeholder).
      expect(taskService.requestExecutionApproval).toHaveBeenCalledWith(
        expect.any(String), "echo {{steps.empty.outputs.nonexistent}}", "/proj",
      );
    });

    it("skips step whose condition references a missing output", async () => {
      vi.mocked(taskService.executeApproved).mockResolvedValue(
        taskResult({ stdout: "v1.0.0" }),
      );
      const wf = makeWorkflow("wf", [
        makeStep("version", "git describe", {
          outputs: { tag: "{{stdout}}" },
        }),
        makeStep("deploy", "deploy", {
          condition: "steps.version.outputs.missing_key",
        }),
      ]);
      await runWorkflow(wf, "/proj");
      const states = workflowState.stepStates["wf"];
      expect(states[0].status).toBe("success");
      expect(states[1].status).toBe("skipped");
      expect(taskService.executeApproved).toHaveBeenCalledTimes(1);
      expect(taskService.requestExecutionApproval).not.toHaveBeenCalledWith(
        expect.any(String), "deploy", "/proj",
      );
    });

    it("runs step when condition output is non-empty", async () => {
      vi.mocked(taskService.executeApproved)
        .mockResolvedValueOnce(taskResult({ stdout: "v1.0.0" }))
        .mockResolvedValueOnce(taskResult());
      const wf = makeWorkflow("wf", [
        makeStep("version", "git describe", {
          outputs: { tag: "{{stdout}}" },
        }),
        makeStep("deploy", "deploy", {
          condition: "steps.version.outputs.tag",
        }),
      ]);
      await runWorkflow(wf, "/proj");
      const states = workflowState.stepStates["wf"];
      expect(states[0].status).toBe("success");
      expect(states[1].status).toBe("success");
      expect(taskService.requestExecutionApproval).toHaveBeenCalledWith(
        expect.any(String), "deploy", "/proj",
      );
    });
  });
});
