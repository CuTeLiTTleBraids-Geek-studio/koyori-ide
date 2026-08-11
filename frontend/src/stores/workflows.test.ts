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
}));

vi.mock("@/stores/app", () => ({
  appState: { terminalVisible: false, currentProject: null },
}));

vi.mock("@/stores/terminal", () => ({
  createSession: vi.fn().mockResolvedValue("session-1"),
  writeToSession: vi.fn().mockResolvedValue(undefined),
  runCommandInSession: vi.fn().mockResolvedValue(0),
  runCommandInSessionCapturing: vi.fn().mockResolvedValue({ exitCode: 0, output: "" }),
  killSession: vi.fn().mockResolvedValue(undefined),
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
import { workflowService } from "@/api/services";
import { createSession, runCommandInSession, runCommandInSessionCapturing, killSession } from "@/stores/terminal";
import { pushOutput } from "@/stores/output";
import { notifyError } from "@/lib/notifications";
import type { WorkflowDef, WorkflowStep } from "@/types";

function makeStep(name: string, command = "echo", extra: Partial<WorkflowStep> = {}): WorkflowStep {
  return { name, command, ...extra };
}

function makeWorkflow(name: string, steps: WorkflowStep[]): WorkflowDef {
  return { name, steps, source: `${name}.yml` };
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
      expect(createSession).not.toHaveBeenCalled();
    });

    it("settles the shell delay and does not execute after runtime cleanup", async () => {
      vi.useFakeTimers();
      const wf = makeWorkflow("wf", [makeStep("first", "echo")]);

      const run = runWorkflow(wf, "/proj");
      await Promise.resolve();
      await Promise.resolve();

      expect(createSession).toHaveBeenCalledWith("/proj");
      expect(vi.getTimerCount()).toBe(1);

      cleanupWorkflowRuntime();
      await run;

      expect(vi.getTimerCount()).toBe(0);
      expect(runCommandInSession).not.toHaveBeenCalled();
      expect(killSession).toHaveBeenCalledWith("session-1");
      expect(workflowState.running.wf).toBe(false);
    });

    it("creates a session and runs each step command", async () => {
      const wf = makeWorkflow("wf", [
        makeStep("first", "echo", { args: ["1"] }),
        makeStep("second", "echo", { args: ["2"] }),
      ]);
      await runWorkflow(wf, "/proj");
      expect(createSession).toHaveBeenCalledWith("/proj");
      await new Promise((r) => setTimeout(r, 50));
      expect(runCommandInSession).toHaveBeenCalledWith("session-1", "echo '1'");
      expect(runCommandInSession).toHaveBeenCalledWith("session-1", "echo '2'");
      expect(workflowState.running["wf"]).toBe(false);
      // All steps should be marked success (runCommandInSession returns 0 by default).
      const states = workflowState.stepStates["wf"];
      expect(states.length).toBe(2);
      expect(states[0].status).toBe("success");
      expect(states[1].status).toBe("success");
    });

    it("runs steps in dependency order", async () => {
      const wf = makeWorkflow("wf", [
        makeStep("build", "go", { args: ["build"], dependsOn: ["lint"] }),
        makeStep("lint", "golangci-lint", { args: ["run"] }),
      ]);
      await runWorkflow(wf, "/proj");
      await new Promise((r) => setTimeout(r, 50));
      // Lint should be run before build.
      const lintCall = (runCommandInSession as any).mock.calls.find((c: any[]) => c[1] === "golangci-lint 'run'");
      const buildCall = (runCommandInSession as any).mock.calls.find((c: any[]) => c[1] === "go 'build'");
      expect(lintCall).toBeTruthy();
      expect(buildCall).toBeTruthy();
      expect((runCommandInSession as any).mock.invocationCallOrder[
        (runCommandInSession as any).mock.calls.indexOf(lintCall)
      ]).toBeLessThan(
        (runCommandInSession as any).mock.invocationCallOrder[
          (runCommandInSession as any).mock.calls.indexOf(buildCall)
        ],
      );
    });

    it("skips steps with falsy condition", async () => {
      const wf = makeWorkflow("wf", [
        makeStep("runs", "echo", { args: ["yes"] }),
        makeStep("skipped", "echo", { args: ["no"], condition: "false" }),
      ]);
      await runWorkflow(wf, "/proj");
      await new Promise((r) => setTimeout(r, 50));
      expect(runCommandInSession).toHaveBeenCalledWith("session-1", "echo 'yes'");
      // The skipped step should not be run.
      expect(runCommandInSession).not.toHaveBeenCalledWith("session-1", "echo 'no'");
      const states = workflowState.stepStates["wf"];
      expect(states[0].status).toBe("success");
      expect(states[1].status).toBe("skipped");
    });

    it("surfaces error when session creation fails", async () => {
      (createSession as any).mockResolvedValueOnce("");
      const wf = makeWorkflow("wf", [makeStep("s")]);
      await runWorkflow(wf, "/proj");
      expect(notifyError).toHaveBeenCalled();
      expect(runCommandInSession).not.toHaveBeenCalled();
      expect(workflowState.running["wf"]).toBe(false);
    });

    it("marks step as failed on non-zero exit code", async () => {
      (runCommandInSession as any).mockResolvedValueOnce(1);
      const wf = makeWorkflow("wf", [
        makeStep("fail", "false"),
        makeStep("after", "echo", { args: ["after"] }),
      ]);
      await runWorkflow(wf, "/proj");
      await new Promise((r) => setTimeout(r, 50));
      const states = workflowState.stepStates["wf"];
      expect(states[0].status).toBe("failed");
      expect(states[0].error).toBe("Exit code: 1");
      // Second step should be skipped (failed = true aborts).
      expect(states[1].status).toBe("skipped");
      // Should not have run the second step.
      expect(runCommandInSession).toHaveBeenCalledTimes(1);
      expect(runCommandInSession).not.toHaveBeenCalledWith("session-1", "echo 'after'");
      // Workflow failure notification.
      expect(notifyError).toHaveBeenCalledWith(expect.stringContaining("failed"));
    });

    it("continues after failed step when expectSuccess is false", async () => {
      (runCommandInSession as any)
        .mockResolvedValueOnce(1) // first step fails
        .mockResolvedValueOnce(0); // second step succeeds
      const wf = makeWorkflow("wf", [
        makeStep("maybe-fail", "false", { expectSuccess: false }),
        makeStep("after", "echo", { args: ["after"] }),
      ]);
      await runWorkflow(wf, "/proj");
      await new Promise((r) => setTimeout(r, 50));
      const states = workflowState.stepStates["wf"];
      expect(states[0].status).toBe("failed");
      expect(states[0].error).toBe("Exit code: 1");
      // Second step should still run (expectSuccess: false = non-fatal).
      expect(states[1].status).toBe("success");
      expect(runCommandInSession).toHaveBeenCalledTimes(2);
      expect(runCommandInSession).toHaveBeenCalledWith("session-1", "echo 'after'");
      // Workflow is still marked as failed (a step failed).
      expect(notifyError).toHaveBeenCalledWith(expect.stringContaining("failed"));
    });

    it("marks step as failed on timeout (-1)", async () => {
      (runCommandInSession as any).mockResolvedValueOnce(-1);
      const wf = makeWorkflow("wf", [makeStep("hang", "sleep", { args: ["9999"] })]);
      await runWorkflow(wf, "/proj");
      await new Promise((r) => setTimeout(r, 50));
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
      expect(createSession).not.toHaveBeenCalled();
      // The early-return path never sets running[wf] to true, so it stays
      // undefined — which is falsy and correctly indicates "not running".
      expect(workflowState.running["wf"]).toBeFalsy();
    });

    it("emits workflow lifecycle output entries", async () => {
      const wf = makeWorkflow("wf", [makeStep("s", "echo")]);
      await runWorkflow(wf, "/proj");
      await new Promise((r) => setTimeout(r, 200));
      // Should have at least: "Starting workflow", step info, "completed".
      const sources = (pushOutput as any).mock.calls.map((c: any[]) => c[0]);
      expect(sources.every((s: string) => s === "workflow")).toBe(true);
      const messages = (pushOutput as any).mock.calls.map((c: any[]) => c[2]);
      expect(messages.some((m: string) => m.includes("Starting workflow"))).toBe(true);
      expect(messages.some((m: string) => m.includes("completed"))).toBe(true);
    });

    it("N-46: kills the terminal session after workflow completes", async () => {
      const wf = makeWorkflow("wf", [makeStep("s", "echo")]);
      await runWorkflow(wf, "/proj");
      await new Promise((r) => setTimeout(r, 200));
      expect(killSession).toHaveBeenCalledWith("session-1");
    });

    it("N-46: kills the terminal session even when workflow fails", async () => {
      (runCommandInSession as any).mockResolvedValueOnce(1);
      const wf = makeWorkflow("wf", [makeStep("fail", "false")]);
      await runWorkflow(wf, "/proj");
      await new Promise((r) => setTimeout(r, 200));
      expect(killSession).toHaveBeenCalledWith("session-1");
    });

    it("N-46: does not kill session when session creation fails", async () => {
      (createSession as any).mockResolvedValueOnce("");
      const wf = makeWorkflow("wf", [makeStep("s")]);
      await runWorkflow(wf, "/proj");
      await new Promise((r) => setTimeout(r, 50));
      expect(killSession).not.toHaveBeenCalled();
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
      expect(runCommandInSession).not.toHaveBeenCalled();
    });
  });

  // --- N-58 (Proposal R): workflow chain triggers ---

  describe("findChainTriggeredWorkflows", () => {
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
      // Reset default mock implementations (clearAllMocks clears history but
      // not implementations set via mockResolvedValueOnce; reset to defaults).
      (runCommandInSession as any).mockResolvedValue(0);
      (runCommandInSessionCapturing as any).mockResolvedValue({ exitCode: 0, output: "" });
      workflowState.workflows = [];
      workflowState.running = {};
      workflowState.stepStates = {};
      // Wait for any background runWorkflow calls from previous tests
      // (e.g. chain triggers fire runWorkflow without awaiting) to
      // complete so their mock calls don't leak into this test's assertions.
      await new Promise((r) => setTimeout(r, 50));
    });

    it("uses runCommandInSessionCapturing when step declares outputs", async () => {
      (runCommandInSessionCapturing as any).mockResolvedValue({
        exitCode: 0,
        output: "v1.2.3",
      });
      const wf = makeWorkflow("wf", [
        makeStep("version", "git describe --tags", {
          outputs: { tag: "{{stdout}}" },
        }),
      ]);
      await runWorkflow(wf, "/proj");
      await new Promise((r) => setTimeout(r, 50));
      expect(runCommandInSessionCapturing).toHaveBeenCalledWith(
        "session-1",
        "git describe --tags",
      );
      // The non-capturing variant should NOT be used for the outputs step.
      // (We check the specific command rather than "not called at all"
      // because background workflows from previous tests may still be
      // settling and call runCommandInSession with their own commands.)
      expect(runCommandInSession).not.toHaveBeenCalledWith(
        "session-1",
        "git describe --tags",
      );
      const states = workflowState.stepStates["wf"];
      expect(states[0].status).toBe("success");
      expect(states[0].outputs).toEqual({ tag: "v1.2.3" });
    });

    it("uses runCommandInSession when step has no outputs", async () => {
      const wf = makeWorkflow("wf", [makeStep("build", "go build")]);
      await runWorkflow(wf, "/proj");
      await new Promise((r) => setTimeout(r, 50));
      expect(runCommandInSession).toHaveBeenCalledWith("session-1", "go build");
      expect(runCommandInSessionCapturing).not.toHaveBeenCalled();
    });

    it("substitutes {{steps.x.outputs.y}} in later step commands", async () => {
      (runCommandInSessionCapturing as any).mockResolvedValue({
        exitCode: 0,
        output: "v2.0.0",
      });
      const wf = makeWorkflow("wf", [
        makeStep("version", "git describe --tags", {
          outputs: { tag: "{{stdout}}" },
        }),
        makeStep("tag", "docker build -t app:{{steps.version.outputs.tag}} ."),
      ]);
      await runWorkflow(wf, "/proj");
      await new Promise((r) => setTimeout(r, 50));
      // First step uses capturing variant.
      expect(runCommandInSessionCapturing).toHaveBeenCalledWith(
        "session-1",
        "git describe --tags",
      );
      // Second step has no outputs template, uses non-capturing variant,
      // AND its command should have the placeholder substituted.
      expect(runCommandInSession).toHaveBeenCalledWith(
        "session-1",
        "docker build -t app:v2.0.0 .",
      );
    });

    it("does not extract outputs when step fails", async () => {
      (runCommandInSessionCapturing as any).mockResolvedValue({
        exitCode: 1,
        output: "error output",
      });
      const wf = makeWorkflow("wf", [
        makeStep("version", "git describe", {
          outputs: { tag: "{{stdout}}" },
        }),
      ]);
      await runWorkflow(wf, "/proj");
      await new Promise((r) => setTimeout(r, 50));
      const states = workflowState.stepStates["wf"];
      expect(states[0].status).toBe("failed");
      expect(states[0].outputs).toBeUndefined();
    });

    it("extracts outputs via regex template", async () => {
      (runCommandInSessionCapturing as any).mockResolvedValue({
        exitCode: 0,
        output: "BUILD v3.1.0 RELEASE",
      });
      const wf = makeWorkflow("wf", [
        makeStep("build", "make version", {
          outputs: {
            full: "{{stdout}}",
            major: "{{regex:v(\\d+)}}",
          },
        }),
      ]);
      await runWorkflow(wf, "/proj");
      await new Promise((r) => setTimeout(r, 50));
      const states = workflowState.stepStates["wf"];
      expect(states[0].status).toBe("success");
      expect(states[0].outputs?.full).toBe("BUILD v3.1.0 RELEASE");
      expect(states[0].outputs?.major).toBe("3");
    });

    it("leaves placeholder when referenced output is missing", async () => {
      (runCommandInSessionCapturing as any).mockResolvedValue({
        exitCode: 0,
        output: "",
      });
      const wf = makeWorkflow("wf", [
        makeStep("empty", "echo", { outputs: { tag: "{{stdout}}" } }),
        makeStep("next", "echo {{steps.empty.outputs.nonexistent}}"),
      ]);
      await runWorkflow(wf, "/proj");
      await new Promise((r) => setTimeout(r, 50));
      // Placeholder should be left as-is (empty output → empty value, but
      // the key 'nonexistent' was never declared, so it stays as placeholder).
      expect(runCommandInSession).toHaveBeenCalledWith(
        "session-1",
        "echo {{steps.empty.outputs.nonexistent}}",
      );
    });

    it("skips step whose condition references a missing output", async () => {
      (runCommandInSessionCapturing as any).mockResolvedValue({
        exitCode: 0,
        output: "v1.0.0",
      });
      const wf = makeWorkflow("wf", [
        makeStep("version", "git describe", {
          outputs: { tag: "{{stdout}}" },
        }),
        makeStep("deploy", "deploy", {
          condition: "steps.version.outputs.missing_key",
        }),
      ]);
      await runWorkflow(wf, "/proj");
      await new Promise((r) => setTimeout(r, 50));
      const states = workflowState.stepStates["wf"];
      expect(states[0].status).toBe("success");
      expect(states[1].status).toBe("skipped");
      expect(runCommandInSession).not.toHaveBeenCalled();
    });

    it("runs step when condition output is non-empty", async () => {
      (runCommandInSessionCapturing as any).mockResolvedValue({
        exitCode: 0,
        output: "v1.0.0",
      });
      const wf = makeWorkflow("wf", [
        makeStep("version", "git describe", {
          outputs: { tag: "{{stdout}}" },
        }),
        makeStep("deploy", "deploy", {
          condition: "steps.version.outputs.tag",
        }),
      ]);
      await runWorkflow(wf, "/proj");
      await new Promise((r) => setTimeout(r, 50));
      const states = workflowState.stepStates["wf"];
      expect(states[0].status).toBe("success");
      expect(states[1].status).toBe("success");
      expect(runCommandInSession).toHaveBeenCalledWith("session-1", "deploy");
    });
  });
});
