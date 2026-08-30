package services

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
)

type catalogPlanStep struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Tool        string `json:"tool"`
	Args        string `json:"args,omitempty"`
	Risk        string `json:"risk,omitempty"`
}

type goalPlanner func(ctx context.Context, agent *AgentService, goal, constraints string, catalogIDs []string) ([]catalogPlanStep, string, error)

var generateCatalogPlanHook goalPlanner

func catalogToolIDs(agent *AgentService) []string {
	if agent == nil {
		return nil
	}
	catalog, err := agent.GetAgentToolCatalog(context.Background())
	if err != nil {
		return nil
	}
	ids := make([]string, 0, len(catalog.Tools))
	for _, tool := range catalog.Tools {
		ids = append(ids, tool.ID)
	}
	return ids
}

func generateCatalogPlan(ctx context.Context, agent *AgentService, goal, constraints string) ([]catalogPlanStep, string, error) {
	ids := catalogToolIDs(agent)
	if generateCatalogPlanHook != nil {
		steps, reason, err := generateCatalogPlanHook(ctx, agent, goal, constraints, ids)
		if err != nil {
			return nil, "", err
		}
		return filterPlanStepsToCatalog(steps, ids), reason, nil
	}
	if len(ids) == 0 {
		return nil, "catalog unavailable", nil
	}
	deps := executionDependenciesFor(agent)
	deps.mu.RLock()
	ai := deps.ai
	deps.mu.RUnlock()
	if ai == nil {
		return nil, "no provider configured; empty plan", nil
	}
	steps, reason, err := planWithLLM(ctx, ai, goal, constraints, ids)
	if err != nil {
		return nil, "provider failed: " + err.Error(), nil
	}
	return filterPlanStepsToCatalog(steps, ids), reason, nil
}

func planWithLLM(ctx context.Context, ai *AIService, goal, constraints string, catalogIDs []string) ([]catalogPlanStep, string, error) {
	_ = ctx
	prompt := "Create a JSON object with a steps array of title, description, tool, args, and risk. " +
		"Use only these catalog tools: " + strings.Join(catalogIDs, ", ") +
		". Do not invent tools. Empty steps are allowed if you cannot plan. Goal: " + goal
	if strings.TrimSpace(constraints) != "" {
		prompt += " Constraints: " + constraints
	}
	resp, err := ai.Send([]ChatMessage{
		{Role: "system", Content: "You are Koyori IDE planner. Reply with JSON only. Never invent catalog tools."},
		{Role: "user", Content: prompt},
	})
	if err != nil {
		return nil, "", err
	}
	content := ""
	if resp != nil {
		content = resp.Content
	}
	steps := parsePlanSteps(content)
	if len(steps) == 0 {
		return nil, "provider returned no usable steps", nil
	}
	return steps, "", nil
}

func filterPlanStepsToCatalog(steps []catalogPlanStep, catalogIDs []string) []catalogPlanStep {
	allowed := make(map[string]struct{}, len(catalogIDs))
	for _, id := range catalogIDs {
		allowed[id] = struct{}{}
	}
	out := make([]catalogPlanStep, 0, len(steps))
	for _, step := range steps {
		if _, ok := allowed[step.Tool]; !ok {
			continue
		}
		out = append(out, step)
	}
	return out
}

func parsePlanSteps(raw string) []catalogPlanStep {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var wrapped struct {
		Steps []catalogPlanStep `json:"steps"`
	}
	if err := json.Unmarshal([]byte(raw), &wrapped); err == nil && len(wrapped.Steps) > 0 {
		return wrapped.Steps
	}
	var steps []catalogPlanStep
	if err := json.Unmarshal([]byte(raw), &steps); err == nil {
		return steps
	}
	return nil
}

func evaluateGoalOutcome(goal *Goal, observation string) (done bool, blocked bool, note string) {
	text := strings.ToLower(observation)
	if strings.Contains(text, "blocked") || strings.Contains(text, "rejected") || strings.Contains(text, "not allowed") {
		return false, true, "blocked by approval or policy"
	}
	if goal != nil && strings.TrimSpace(goal.SuccessCriteria) != "" && strings.Contains(text, strings.ToLower(goal.SuccessCriteria)) {
		return true, false, "success criteria observed"
	}
	if strings.Contains(text, "done") || strings.Contains(text, "completed") {
		return true, false, "executor reported done"
	}
	return false, false, "continue"
}

func executePlanStep(agent *AgentService, goalID, tool, argsJSON string) (AgentToolExecutionResult, error) {
	args := map[string]interface{}{}
	if strings.TrimSpace(argsJSON) != "" {
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return AgentToolExecutionResult{}, fmt.Errorf("plan step args: %w", err)
		}
	}
	return agent.executeInternalAgentTool(
		context.Background(),
		agent.executionSessionID(agentcore.SessionGoal, goalID),
		tool,
		args,
	)
}
