package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/internal/agentcore"
	"github.com/wailsapp/wails/v3/pkg/application"
)

var dynamicAgentToolSlugPattern = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

const (
	agentCatalogRefreshBeforeWorkflowPublish = "before-workflow-publish"

	workflowAdapterCommand       = "command"
	workflowAdapterAI            = "ai"
	workflowAdapterFileRead      = "file.read"
	workflowAdapterFileWrite     = "file.write"
	workflowAdapterGitStatus     = "git.status"
	workflowAdapterMCP           = "mcp.call"
	workflowAdapterSkillActivate = "skill.activate"

	workflowSourcePathMetadata                = "sourcePath"
	workflowSourceHashMetadata                = "sourceHash"
	workflowSourceWorkspaceGenerationMetadata = "sourceWorkspaceGeneration"
	workflowSourceFileGenerationMetadata      = "sourceFileGeneration"
)

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

// WireAgentWorkflowTools connects project workflow definitions to the same
// catalog and runtime used by builtins, MCP, and skills. WorkflowService stays
// renderer-facing for CRUD; execution authority remains in AgentService.
func WireAgentWorkflowTools(agent *AgentService, workflow *WorkflowService) error {
	if agent == nil {
		return fmt.Errorf("agent service is required: %w", ErrInvalidInput)
	}
	if workflow != nil {
		workflow.setOnMutationChange(func() error {
			return agent.refreshWorkflowAgentToolsAfterMutation(context.Background())
		})
	}
	deps := executionDependenciesFor(agent)
	deps.mu.Lock()
	deps.workflow = workflow
	deps.mu.Unlock()
	return agent.refreshDynamicAgentTools(context.Background())
}

// WireAgentExecutionAI injects the backend AI provider into the unified
// execution core. It is trusted bootstrap wiring and is never renderer
// callable; workflow AI ToolDefs remain absent until this boundary exists.
func WireAgentExecutionAI(agent *AgentService, ai *AIService) error {
	if agent == nil || ai == nil {
		return fmt.Errorf("agent and AI services are required: %w", ErrInvalidInput)
	}
	deps := executionDependenciesFor(agent)
	deps.mu.Lock()
	deps.ai = ai
	deps.mu.Unlock()
	return agent.refreshDynamicAgentTools(context.Background())
}

// refreshWorkflowAgentToolsAfterMutation invalidates the complete dynamic
// catalog before rebuilding it. Clearing first guarantees that even a
// mutation which leaves the resulting ToolDefs byte-for-byte identical burns
// outstanding capabilities. Clearing all sources in one revision prevents
// readers from observing a mixed MCP/workflow/Skill snapshot while rebuilding.
func (s *AgentService) refreshWorkflowAgentToolsAfterMutation(ctx context.Context) error {
	unlock, admitted := s.lockDynamicAgentCatalogRefresh()
	if !admitted {
		return nil
	}
	defer unlock()

	runtime, err := s.coreRuntime()
	if err != nil {
		return err
	}
	if err := clearDynamicAgentTools(runtime); err != nil {
		return fmt.Errorf("clear dynamic agent tools before workflow refresh: %w", err)
	}
	if err := s.refreshDynamicAgentToolsLocked(ctx); err != nil {
		clearErr := clearDynamicAgentTools(runtime)
		return errors.Join(err, clearErr)
	}
	return nil
}

// refreshSkillAgentToolsAfterMutation invalidates the complete dynamic catalog
// before rebuilding it. Skill files and project approval state can change
// while a capability is waiting for redemption; clearing all sources burns the
// old revision without exposing a partially cleared dynamic snapshot.
func (s *AgentService) refreshSkillAgentToolsAfterMutation(ctx context.Context) error {
	unlock, admitted := s.lockDynamicAgentCatalogRefresh()
	if !admitted {
		return nil
	}
	defer unlock()

	runtime, err := s.coreRuntime()
	if err != nil {
		return err
	}
	if err := clearDynamicAgentTools(runtime); err != nil {
		return fmt.Errorf("clear dynamic agent tools before skill refresh: %w", err)
	}
	if err := s.refreshDynamicAgentToolsLocked(ctx); err != nil {
		clearErr := clearDynamicAgentTools(runtime)
		return errors.Join(err, clearErr)
	}
	return nil
}

func (s *AgentService) buildWorkflowAgentTools(
	runtime *agentcore.Runtime,
	workflow *WorkflowService,
	mcpDefinitions []agentcore.ToolDef,
	skills *SkillsService,
	loadedSkills []Skill,
) ([]agentcore.ToolDef, error) {
	if workflow == nil || s.agentWorkspaceGeneration() == 0 {
		return nil, nil
	}
	deps := executionDependenciesFor(s)
	deps.mu.RLock()
	fileService := deps.file
	deps.mu.RUnlock()
	if fileService == nil {
		return nil, fmt.Errorf("FileService is required for Agent workflow loading: %w", ErrNotAllowed)
	}
	sources, err := workflow.loadAgentWorkflowSources(fileService)
	if err != nil {
		return nil, err
	}
	definitions := make([]agentcore.ToolDef, 0)
	hasAI := false
	for index := range sources {
		source := &sources[index]
		definition := &source.Definition
		validation := workflow.ValidateWorkflow(definition)
		if !validation.Valid {
			return nil, fmt.Errorf("workflow %q is invalid: %v", definition.Name, validation.Errors)
		}
		for _, step := range definition.Steps {
			if step.Type == WorkflowStepAI && workflowStepHasExecutableDefinition(step) {
				aiDefinition, aiErr := workflowAIToolDef(definition.Name, step)
				if aiErr != nil {
					return nil, fmt.Errorf("workflow %q step %q AI adapter: %w", definition.Name, step.Name, aiErr)
				}
				if bindErr := bindAgentWorkflowSource(&aiDefinition, *source); bindErr != nil {
					return nil, bindErr
				}
				definitions = append(definitions, aiDefinition)
				hasAI = true
				continue
			}
			if step.Type == WorkflowStepSkill {
				skillDefinition, skillErr := s.workflowSkillActivationToolDef(definition.Name, step, skills, loadedSkills)
				if skillErr != nil {
					return nil, fmt.Errorf("workflow %q step %q Skill adapter: %w", definition.Name, step.Name, skillErr)
				}
				if bindErr := bindAgentWorkflowSource(&skillDefinition, *source); bindErr != nil {
					return nil, bindErr
				}
				definitions = append(definitions, skillDefinition)
				continue
			}
			if step.Type == WorkflowStepGit {
				gitDefinition, gitErr := workflowGitStatusToolDef(definition.Name, step)
				if gitErr != nil {
					return nil, fmt.Errorf("workflow %q step %q git adapter: %w", definition.Name, step.Name, gitErr)
				}
				if bindErr := bindAgentWorkflowSource(&gitDefinition, *source); bindErr != nil {
					return nil, bindErr
				}
				definitions = append(definitions, gitDefinition)
				continue
			}
			if step.Type == WorkflowStepFile {
				var fileDefinition agentcore.ToolDef
				var fileErr error
				switch step.Tool {
				case "read":
					fileDefinition, fileErr = workflowFileReadToolDef(definition.Name, step)
				case "write":
					fileDefinition, fileErr = workflowFileWriteToolDef(definition.Name, step)
				default:
					fileErr = fmt.Errorf("unsupported file workflow tool %q: %w", step.Tool, ErrInvalidInput)
				}
				if fileErr != nil {
					return nil, fmt.Errorf("workflow %q step %q file adapter: %w", definition.Name, step.Name, fileErr)
				}
				if bindErr := bindAgentWorkflowSource(&fileDefinition, *source); bindErr != nil {
					return nil, bindErr
				}
				definitions = append(definitions, fileDefinition)
				continue
			}
			if step.Type == WorkflowStepMCP {
				mcpDefinition, mcpErr := s.workflowMCPToolDef(mcpDefinitions, definition.Name, step)
				if mcpErr != nil {
					return nil, fmt.Errorf("workflow %q step %q MCP adapter: %w", definition.Name, step.Name, mcpErr)
				}
				if bindErr := bindAgentWorkflowSource(&mcpDefinition, *source); bindErr != nil {
					return nil, bindErr
				}
				definitions = append(definitions, mcpDefinition)
				continue
			}
			if step.Type != "" && step.Type != WorkflowStepCommand {
				// Unsupported typed adapters stay absent from the catalog.
				// In particular, they must never fall back to shell command
				// execution or inherit command/args semantics.
				continue
			}
			commandLine := step.ComposeStepCommandLine()
			if _, parseErr := parseCommand(commandLine); parseErr != nil {
				return nil, fmt.Errorf("workflow %q step %q command: %w", definition.Name, step.Name, parseErr)
			}
			commandDefinition := agentcore.ToolDef{
				ID:          dynamicAgentToolID("workflow", definition.Name, step.Name),
				Description: fmt.Sprintf("Run workflow %q step %q.", definition.Name, step.Name),
				InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
				Source:      agentcore.SourceWorkflow, Risk: workflowCommandRisk(s, commandLine),
				Approval: agentcore.ApprovalManual, Mutation: agentcore.MutationExternal,
				ExecuteKey: "workflow.command",
				Metadata: map[string]string{
					"workflow": definition.Name, "step": step.Name,
					"adapter": workflowAdapterCommand,
					"command": commandLine, "cwd": step.Cwd,
				},
			}
			if bindErr := bindAgentWorkflowSource(&commandDefinition, *source); bindErr != nil {
				return nil, bindErr
			}
			definitions = append(definitions, commandDefinition)
		}
	}
	if err := ensureWorkflowAgentHandler(s, runtime); err != nil {
		return nil, err
	}
	if err := ensureWorkflowFileReadHandler(s, runtime); err != nil {
		return nil, err
	}
	if err := ensureWorkflowFileWriteHandler(s, runtime); err != nil {
		return nil, err
	}
	if err := ensureWorkflowGitStatusHandler(s, runtime); err != nil {
		return nil, err
	}
	if err := ensureWorkflowMCPHandler(s, runtime); err != nil {
		return nil, err
	}
	if hasAI {
		if err := ensureWorkflowAIHandler(s, runtime); err != nil {
			return nil, err
		}
	}
	if err := ensureSkillAgentHandler(s, runtime); err != nil {
		return nil, err
	}
	runAgentCatalogRefreshHook(s, agentCatalogRefreshBeforeWorkflowPublish)
	return definitions, nil
}

func bindAgentWorkflowSource(definition *agentcore.ToolDef, source agentWorkflowSource) error {
	if definition == nil || source.RelativePath == "" || len(source.ContentHash) != sha256.Size*2 ||
		source.WorkspaceGeneration == 0 || source.FileGeneration == 0 {
		return fmt.Errorf("Agent workflow source identity is incomplete: %w", ErrNotAllowed)
	}
	if _, err := hex.DecodeString(source.ContentHash); err != nil {
		return fmt.Errorf("Agent workflow source hash is invalid: %w", ErrNotAllowed)
	}
	if definition.Metadata == nil {
		definition.Metadata = make(map[string]string)
	}
	definition.Metadata[workflowSourcePathMetadata] = source.RelativePath
	definition.Metadata[workflowSourceHashMetadata] = source.ContentHash
	definition.Metadata[workflowSourceWorkspaceGenerationMetadata] = strconv.FormatUint(source.WorkspaceGeneration, 10)
	definition.Metadata[workflowSourceFileGenerationMetadata] = strconv.FormatUint(source.FileGeneration, 10)
	return nil
}

func (s *AgentService) currentAgentWorkflowStep(metadata map[string]string) (WorkflowStep, error) {
	if s == nil || strings.TrimSpace(metadata["workflow"]) == "" || strings.TrimSpace(metadata["step"]) == "" {
		return WorkflowStep{}, fmt.Errorf("Agent workflow ownership metadata is invalid: %w", ErrNotAllowed)
	}
	sourcePath := metadata[workflowSourcePathMetadata]
	canonicalSourcePath := filepath.ToSlash(filepath.Clean(filepath.FromSlash(sourcePath)))
	if sourcePath == "" || canonicalSourcePath != sourcePath ||
		!strings.HasPrefix(sourcePath, agentWorkflowDirectory+"/") || !IsRelativePathSafe(sourcePath) {
		return WorkflowStep{}, fmt.Errorf("Agent workflow source path is invalid: %w", ErrNotAllowed)
	}
	sourceHash := metadata[workflowSourceHashMetadata]
	if len(sourceHash) != sha256.Size*2 {
		return WorkflowStep{}, fmt.Errorf("Agent workflow source hash is invalid: %w", ErrNotAllowed)
	}
	if _, err := hex.DecodeString(sourceHash); err != nil {
		return WorkflowStep{}, fmt.Errorf("Agent workflow source hash is invalid: %w", ErrNotAllowed)
	}
	workspaceGeneration, err := strconv.ParseUint(metadata[workflowSourceWorkspaceGenerationMetadata], 10, 64)
	if err != nil || workspaceGeneration == 0 || workspaceGeneration != s.agentWorkspaceGeneration() {
		return WorkflowStep{}, fmt.Errorf("Agent workflow workspace generation changed: %w", ErrNotAllowed)
	}
	fileGeneration, err := strconv.ParseUint(metadata[workflowSourceFileGenerationMetadata], 10, 64)
	if err != nil || fileGeneration == 0 {
		return WorkflowStep{}, fmt.Errorf("Agent workflow file generation is invalid: %w", ErrNotAllowed)
	}

	deps := executionDependenciesFor(s)
	deps.mu.RLock()
	workflow := deps.workflow
	fileService := deps.file
	deps.mu.RUnlock()
	if workflow == nil || fileService == nil {
		return WorkflowStep{}, fmt.Errorf("Agent workflow loader is not wired: %w", ErrNotAllowed)
	}
	sources, err := workflow.loadAgentWorkflowSources(fileService)
	if err != nil {
		return WorkflowStep{}, err
	}
	var selected *agentWorkflowSource
	for index := range sources {
		candidate := &sources[index]
		if candidate.Definition.Name != metadata["workflow"] {
			continue
		}
		if selected != nil {
			return WorkflowStep{}, fmt.Errorf("Agent workflow %q is duplicated: %w", metadata["workflow"], ErrNotAllowed)
		}
		selected = candidate
	}
	if selected == nil || selected.RelativePath != sourcePath || selected.ContentHash != sourceHash ||
		selected.WorkspaceGeneration != workspaceGeneration || selected.FileGeneration != fileGeneration {
		return WorkflowStep{}, fmt.Errorf("Agent workflow source changed after catalog publication: %w", ErrNotAllowed)
	}
	var step *WorkflowStep
	for index := range selected.Definition.Steps {
		candidate := &selected.Definition.Steps[index]
		if candidate.Name != metadata["step"] {
			continue
		}
		if step != nil {
			return WorkflowStep{}, fmt.Errorf("Agent workflow step %q is duplicated: %w", metadata["step"], ErrNotAllowed)
		}
		step = candidate
	}
	if step == nil {
		return WorkflowStep{}, fmt.Errorf("Agent workflow step changed after catalog publication: %w", ErrNotAllowed)
	}
	return *step, nil
}

func (s *AgentService) validateCurrentAgentWorkflowTool(metadata map[string]string) (WorkflowStep, error) {
	step, err := s.currentAgentWorkflowStep(metadata)
	if err != nil {
		return WorkflowStep{}, err
	}
	switch metadata["adapter"] {
	case workflowAdapterCommand:
		if (step.Type != "" && step.Type != WorkflowStepCommand) ||
			step.ComposeStepCommandLine() != metadata["command"] || step.Cwd != metadata["cwd"] {
			return WorkflowStep{}, fmt.Errorf("Agent workflow command changed after catalog publication: %w", ErrNotAllowed)
		}
	case workflowAdapterFileRead:
		pathValue, _ := step.Input["path"].(string)
		canonicalPath, pathErr := normalizeWorkflowFileReadPath(pathValue)
		if step.Type != WorkflowStepFile || !workflowStepHasExecutableDefinition(step) || pathErr != nil || canonicalPath != metadata["path"] {
			return WorkflowStep{}, fmt.Errorf("Agent workflow file read changed after catalog publication: %w", ErrNotAllowed)
		}
	case workflowAdapterFileWrite:
		pathValue, content, writeErr := workflowFileWriteInput(step)
		contentHash := hashWorkflowFileContent(content)
		if writeErr != nil || !workflowFileWriteInputIsValid(step) || pathValue != metadata["path"] ||
			contentHash != metadata["contentHash"] || strconv.Itoa(len([]byte(content))) != metadata["contentBytes"] {
			return WorkflowStep{}, fmt.Errorf("Agent workflow file write changed after catalog publication: %w", ErrNotAllowed)
		}
	case workflowAdapterGitStatus:
		if step.Type != WorkflowStepGit || !workflowGitStatusInputIsValid(step) {
			return WorkflowStep{}, fmt.Errorf("Agent workflow Git status changed after catalog publication: %w", ErrNotAllowed)
		}
	case workflowAdapterMCP:
		inputHash, hashErr := workflowInputHash(step.Input)
		if step.Type != WorkflowStepMCP || !workflowMCPInputIsValid(step) ||
			step.Tool != metadata["delegatedTool"] || hashErr != nil || inputHash != metadata["inputHash"] {
			return WorkflowStep{}, fmt.Errorf("Agent workflow MCP step changed after catalog publication: %w", ErrNotAllowed)
		}
	case workflowAdapterSkillActivate:
		skillID, _ := step.Input["id"].(string)
		canonicalSkillID, skillErr := normalizeWorkflowSkillID(skillID)
		if step.Type != WorkflowStepSkill || !workflowSkillActivationInputIsValid(step) ||
			skillErr != nil || canonicalSkillID != metadata["skillId"] {
			return WorkflowStep{}, fmt.Errorf("Agent workflow Skill step changed after catalog publication: %w", ErrNotAllowed)
		}
	case workflowAdapterAI:
		operation, prompt, aiErr := workflowAIInput(step)
		promptHash := hashWorkflowAIPrompt(prompt)
		if !workflowAIInputIsValid(step) || aiErr != nil || operation != metadata["operation"] || promptHash != metadata["promptHash"] {
			return WorkflowStep{}, fmt.Errorf("Agent workflow AI step changed after catalog publication: %w", ErrNotAllowed)
		}
	default:
		return WorkflowStep{}, fmt.Errorf("Agent workflow adapter %q is unsupported: %w", metadata["adapter"], ErrNotAllowed)
	}
	return step, nil
}

func workflowAIInput(step WorkflowStep) (string, string, error) {
	if !workflowAIInputIsValid(step) {
		return "", "", fmt.Errorf("AI workflow step input is invalid: %w", ErrInvalidInput)
	}
	prompt, _ := step.Input["prompt"].(string)
	return step.Tool, prompt, nil
}

func hashWorkflowAIPrompt(prompt string) string {
	sum := sha256.Sum256([]byte(prompt))
	return hex.EncodeToString(sum[:])
}

func workflowAIToolDef(workflowName string, step WorkflowStep) (agentcore.ToolDef, error) {
	operation, prompt, err := workflowAIInput(step)
	if err != nil {
		return agentcore.ToolDef{}, err
	}
	return agentcore.ToolDef{
		ID:          dynamicAgentToolID("workflow-ai", workflowName, step.Name),
		Description: fmt.Sprintf("Run workflow AI operation %q for workflow %q step %q.", operation, workflowName, step.Name),
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Source:      agentcore.SourceWorkflow, Risk: agentcore.RiskElevated,
		Approval: agentcore.ApprovalManual, Mutation: agentcore.MutationExternal,
		ExecuteKey: "workflow.ai",
		Metadata: map[string]string{
			"workflow": workflowName, "step": step.Name, "adapter": workflowAdapterAI,
			"operation": operation, "promptHash": hashWorkflowAIPrompt(prompt),
		},
	}, nil
}

func (s *AgentService) workflowMCPToolDef(mcpDefinitions []agentcore.ToolDef, workflowName string, step WorkflowStep) (agentcore.ToolDef, error) {
	if !workflowMCPInputIsValid(step) {
		return agentcore.ToolDef{}, fmt.Errorf("MCP step input is invalid: %w", ErrInvalidInput)
	}
	var selected *agentcore.ToolDef
	for _, definition := range mcpDefinitions {
		candidate := definition
		if candidate.Source != agentcore.SourceMCP || candidate.ID != strings.TrimSpace(step.Tool) {
			continue
		}
		if selected != nil {
			return agentcore.ToolDef{}, fmt.Errorf("MCP tool %q is duplicated in the connected catalog: %w", step.Tool, agentcore.ErrDuplicateTool)
		}
		selected = &candidate
	}
	if selected == nil {
		return agentcore.ToolDef{}, fmt.Errorf("MCP tool %q is not available in the connected catalog: %w", step.Tool, ErrNotFound)
	}
	if selected.ExecuteKey != "mcp.call" || selected.Mutation != agentcore.MutationExternal ||
		selected.Approval != agentcore.ApprovalManual || selected.Metadata["server"] == "" || selected.Metadata["tool"] == "" {
		return agentcore.ToolDef{}, fmt.Errorf("MCP tool %q has an invalid execution contract: %w", step.Tool, ErrNotAllowed)
	}
	if err := validateWorkflowMCPArguments(selected.InputSchema, step.Input); err != nil {
		return agentcore.ToolDef{}, err
	}
	inputHash, err := workflowInputHash(step.Input)
	if err != nil {
		return agentcore.ToolDef{}, err
	}
	return agentcore.ToolDef{
		ID:          dynamicAgentToolID("workflow-mcp", workflowName, step.Name),
		Description: fmt.Sprintf("Call MCP tool %q for workflow %q step %q.", selected.ID, workflowName, step.Name),
		InputSchema: append(json.RawMessage(nil), selected.InputSchema...), Source: agentcore.SourceWorkflow, Risk: selected.Risk,
		Approval: agentcore.ApprovalManual, Mutation: agentcore.MutationExternal, ExecuteKey: "workflow.mcp.call",
		Metadata: map[string]string{
			"workflow": workflowName, "step": step.Name, "adapter": workflowAdapterMCP,
			"delegatedTool": selected.ID, "server": selected.Metadata["server"], "tool": selected.Metadata["tool"],
			// Only a digest is projected to the renderer; workflow input may
			// contain provider secrets and is reloaded by the trusted backend.
			"inputHash": inputHash,
		},
	}, nil
}

func workflowInputHash(input map[string]interface{}) (string, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("encode workflow input hash: %w", ErrInvalidInput)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func validateWorkflowMCPArguments(schema json.RawMessage, input map[string]interface{}) error {
	encoded, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("encode MCP workflow input: %w", ErrInvalidInput)
	}
	registry, err := agentcore.NewRegistry([]agentcore.ToolDef{{
		ID: "workflow-input", Description: "workflow input validation", InputSchema: schema,
		Source: agentcore.SourceBuiltin, Risk: agentcore.RiskReadOnly, Approval: agentcore.ApprovalBackendPolicy,
		Mutation: agentcore.MutationNone, ExecuteKey: "workflow-input",
	}})
	if err != nil {
		return fmt.Errorf("compile MCP workflow input schema: %w", err)
	}
	if _, err := registry.Resolve(1, "workflow-input", encoded); err != nil {
		return fmt.Errorf("MCP workflow input does not match tool schema: %w", err)
	}
	return nil
}

func workflowFileReadToolDef(workflowName string, step WorkflowStep) (agentcore.ToolDef, error) {
	pathValue, _ := step.Input["path"].(string)
	canonicalPath, err := normalizeWorkflowFileReadPath(pathValue)
	if err != nil {
		return agentcore.ToolDef{}, err
	}
	return agentcore.ToolDef{
		ID:          dynamicAgentToolID("workflow-file", workflowName, step.Name),
		Description: fmt.Sprintf("Read the file selected by workflow %q step %q.", workflowName, step.Name),
		InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","minLength":1}},"required":["path"],"additionalProperties":false}`),
		Source:      agentcore.SourceWorkflow,
		Risk:        agentcore.RiskReadOnly,
		Approval:    agentcore.ApprovalBackendPolicy,
		Mutation:    agentcore.MutationNone,
		ExecuteKey:  "workflow.file.read",
		Metadata: map[string]string{
			"workflow": workflowName,
			"step":     step.Name,
			"adapter":  workflowAdapterFileRead,
			"path":     canonicalPath,
		},
	}, nil
}

func workflowGitStatusToolDef(workflowName string, step WorkflowStep) (agentcore.ToolDef, error) {
	if !workflowGitStatusInputIsValid(step) {
		return agentcore.ToolDef{}, fmt.Errorf("git status step input is invalid: %w", ErrInvalidInput)
	}
	return agentcore.ToolDef{
		ID:          dynamicAgentToolID("workflow-git", workflowName, step.Name),
		Description: fmt.Sprintf("Read Git status for workflow %q step %q.", workflowName, step.Name),
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Source:      agentcore.SourceWorkflow,
		Risk:        agentcore.RiskReadOnly,
		Approval:    agentcore.ApprovalBackendPolicy,
		Mutation:    agentcore.MutationNone,
		ExecuteKey:  "workflow.git.status",
		Metadata: map[string]string{
			"workflow": workflowName,
			"step":     step.Name,
			"adapter":  workflowAdapterGitStatus,
		},
	}, nil
}

func (s *AgentService) workflowSkillActivationToolDef(
	workflowName string,
	step WorkflowStep,
	skills *SkillsService,
	loadedSkills []Skill,
) (agentcore.ToolDef, error) {
	if !workflowSkillActivationInputIsValid(step) {
		return agentcore.ToolDef{}, fmt.Errorf("Skill activation step input is invalid: %w", ErrInvalidInput)
	}
	skillID, err := normalizeWorkflowSkillID(step.Input["id"].(string))
	if err != nil {
		return agentcore.ToolDef{}, err
	}
	if skills == nil {
		return agentcore.ToolDef{}, fmt.Errorf("skills service is not wired: %w", ErrNotAllowed)
	}
	var skill *Skill
	for index := range loadedSkills {
		candidate := &loadedSkills[index]
		if candidate.ID != skillID {
			continue
		}
		if skill != nil {
			return agentcore.ToolDef{}, fmt.Errorf("skill %q appears more than once: %w", skillID, ErrNotAllowed)
		}
		skill = candidate
	}
	if skill == nil {
		return agentcore.ToolDef{}, fmt.Errorf("skill %q: %w", skillID, ErrNotFound)
	}
	if !isAgentSkillScopeValid(skill.Scope) {
		return agentcore.ToolDef{}, fmt.Errorf("skill %q has invalid scope %q: %w", skill.ID, skill.Scope, ErrNotAllowed)
	}
	fingerprint, err := skillFingerprint(*skill)
	if err != nil {
		return agentcore.ToolDef{}, err
	}
	description := strings.TrimSpace(skill.Description)
	if description == "" {
		description = fmt.Sprintf("Activate skill %q.", skill.Name)
	}
	return agentcore.ToolDef{
		ID:          dynamicAgentToolID("workflow-skill", workflowName, step.Name),
		Description: fmt.Sprintf("Activate skill %q for workflow %q step %q. %s", skill.ID, workflowName, step.Name, description),
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Source:      agentcore.SourceWorkflow,
		Risk:        agentcore.RiskElevated,
		Approval:    agentcore.ApprovalManual,
		Mutation:    agentcore.MutationExternal,
		ExecuteKey:  "skill.activate",
		Metadata: map[string]string{
			"workflow": workflowName, "step": step.Name, "adapter": workflowAdapterSkillActivate,
			"skillId": skill.ID, "scope": string(skill.Scope), "fingerprint": fingerprint,
		},
	}, nil
}

func (s *AgentService) buildSkillAgentTools(runtime *agentcore.Runtime, skills *SkillsService, loaded []Skill) ([]agentcore.ToolDef, error) {
	if skills == nil || s.agentWorkspaceGeneration() == 0 {
		return nil, nil
	}
	definitions := make([]agentcore.ToolDef, 0, len(loaded))
	for _, skill := range loaded {
		if strings.TrimSpace(skill.ID) == "" || strings.TrimSpace(skill.Name) == "" {
			return nil, fmt.Errorf("skill ID and name are required: %w", ErrInvalidInput)
		}
		if !isAgentSkillScopeValid(skill.Scope) {
			return nil, fmt.Errorf("skill %q has invalid scope %q: %w", skill.ID, skill.Scope, ErrInvalidInput)
		}
		fingerprint, fingerprintErr := skillFingerprint(skill)
		if fingerprintErr != nil {
			return nil, fingerprintErr
		}
		description := strings.TrimSpace(skill.Description)
		if description == "" {
			description = fmt.Sprintf("Activate skill %q.", skill.Name)
		}
		definitions = append(definitions, agentcore.ToolDef{
			ID:          dynamicAgentToolID("skill", skill.ID, "activate"),
			Description: description,
			InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
			Source:      agentcore.SourceSkill, Risk: agentcore.RiskElevated,
			Approval: agentcore.ApprovalManual, Mutation: agentcore.MutationExternal,
			ExecuteKey: "skill.activate",
			Metadata: map[string]string{
				"skillId": skill.ID, "scope": string(skill.Scope), "fingerprint": fingerprint,
			},
		})
	}
	if err := ensureSkillAgentHandler(s, runtime); err != nil {
		return nil, err
	}
	return definitions, nil
}

func isAgentSkillScopeValid(scope SkillScope) bool {
	switch scope {
	case SkillScopeProject, SkillScopeUser, SkillScopeGlobal:
		return true
	default:
		return false
	}
}

func ensureWorkflowAgentHandler(agent *AgentService, runtime *agentcore.Runtime) error {
	deps := executionDependenciesFor(agent)
	deps.mu.Lock()
	defer deps.mu.Unlock()
	if deps.workflowHandlerRegistered {
		return nil
	}
	if err := runtime.RegisterHandler("workflow.command", &agentWorkflowCommandHandler{agent: agent}); err != nil {
		return err
	}
	deps.workflowHandlerRegistered = true
	return nil
}

func ensureWorkflowFileReadHandler(agent *AgentService, runtime *agentcore.Runtime) error {
	deps := executionDependenciesFor(agent)
	deps.mu.Lock()
	defer deps.mu.Unlock()
	if deps.workflowFileReadHandlerRegistered {
		return nil
	}
	if err := runtime.RegisterHandler("workflow.file.read", &agentWorkflowFileReadHandler{agent: agent}); err != nil {
		return err
	}
	deps.workflowFileReadHandlerRegistered = true
	return nil
}

func ensureWorkflowFileWriteHandler(agent *AgentService, runtime *agentcore.Runtime) error {
	deps := executionDependenciesFor(agent)
	deps.mu.Lock()
	defer deps.mu.Unlock()
	if deps.workflowFileWriteHandlerRegistered {
		return nil
	}
	if err := runtime.RegisterHandler("workflow.file.write", &agentWorkflowFileWriteHandler{agent: agent}); err != nil {
		return err
	}
	deps.workflowFileWriteHandlerRegistered = true
	return nil
}

func ensureWorkflowGitStatusHandler(agent *AgentService, runtime *agentcore.Runtime) error {
	deps := executionDependenciesFor(agent)
	deps.mu.Lock()
	defer deps.mu.Unlock()
	if deps.workflowGitStatusHandlerRegistered {
		return nil
	}
	if err := runtime.RegisterHandler("workflow.git.status", &agentWorkflowGitStatusHandler{agent: agent}); err != nil {
		return err
	}
	deps.workflowGitStatusHandlerRegistered = true
	return nil
}

func ensureWorkflowMCPHandler(agent *AgentService, runtime *agentcore.Runtime) error {
	deps := executionDependenciesFor(agent)
	deps.mu.Lock()
	defer deps.mu.Unlock()
	if deps.workflowMCPHandlerRegistered {
		return nil
	}
	if err := runtime.RegisterHandler("workflow.mcp.call", &agentWorkflowMCPHandler{agent: agent}); err != nil {
		return err
	}
	deps.workflowMCPHandlerRegistered = true
	return nil
}

func ensureWorkflowAIHandler(agent *AgentService, runtime *agentcore.Runtime) error {
	deps := executionDependenciesFor(agent)
	deps.mu.Lock()
	defer deps.mu.Unlock()
	if deps.workflowAIHandlerRegistered {
		return nil
	}
	if deps.ai == nil {
		return fmt.Errorf("AI service is required for workflow AI execution: %w", ErrNotAllowed)
	}
	if err := runtime.RegisterHandler("workflow.ai", &agentWorkflowAIHandler{agent: agent}); err != nil {
		return err
	}
	deps.workflowAIHandlerRegistered = true
	return nil
}

// agentWorkflowFileReadHandler adapts the typed workflow file step to the
// existing builtin.read implementation. The workflow catalog owns the path;
// invocation arguments must match that catalog metadata and cannot redirect
// the read to a renderer-selected location.
type agentWorkflowFileReadHandler struct{ agent *AgentService }

func (*agentWorkflowFileReadHandler) MutationMode() agentcore.MutationMode {
	return agentcore.MutationNone
}

func (h *agentWorkflowFileReadHandler) Prepare(ctx context.Context, invocation agentcore.Invocation) (agentcore.PreparedExecution, error) {
	if invocation.Tool.Metadata["adapter"] != workflowAdapterFileRead {
		return agentcore.PreparedExecution{}, fmt.Errorf("workflow file read adapter metadata is invalid: %w", ErrNotAllowed)
	}
	if _, err := h.agent.validateCurrentAgentWorkflowTool(invocation.Tool.Metadata); err != nil {
		return agentcore.PreparedExecution{}, err
	}
	pathValue, err := normalizeWorkflowFileReadPath(invocation.Tool.Metadata["path"])
	if err != nil || pathValue != invocation.Tool.Metadata["path"] {
		return agentcore.PreparedExecution{}, fmt.Errorf("workflow file read path is not canonical in the catalog: %w", ErrNotAllowed)
	}
	var args pathToolArguments
	if err := json.Unmarshal(invocation.Arguments, &args); err != nil {
		return agentcore.PreparedExecution{}, err
	}
	argumentPath, err := normalizeWorkflowFileReadPath(args.Path)
	if err != nil || argumentPath != pathValue || args.Path != argumentPath {
		return agentcore.PreparedExecution{}, fmt.Errorf("workflow file read path differs from catalog: %w", ErrNotAllowed)
	}
	return (&agentReadHandler{agent: h.agent}).Prepare(ctx, invocation)
}

func (h *agentWorkflowFileReadHandler) Execute(ctx context.Context, invocation agentcore.Invocation, prepared agentcore.PreparedExecution) (agentcore.ExecutionOutput, error) {
	if _, err := h.agent.validateCurrentAgentWorkflowTool(invocation.Tool.Metadata); err != nil {
		return agentcore.ExecutionOutput{}, err
	}
	return (&agentReadHandler{agent: h.agent}).Execute(ctx, invocation, prepared)
}

type preparedWorkflowGitStatus struct {
	RootGeneration uint64 `json:"rootGeneration"`
	Root           string `json:"root"`
}

type agentWorkflowGitStatusHandler struct{ agent *AgentService }

func (*agentWorkflowGitStatusHandler) MutationMode() agentcore.MutationMode {
	return agentcore.MutationNone
}

func (h *agentWorkflowGitStatusHandler) Prepare(_ context.Context, invocation agentcore.Invocation) (agentcore.PreparedExecution, error) {
	if invocation.Tool.Metadata["adapter"] != workflowAdapterGitStatus || invocation.Tool.Metadata["workflow"] == "" || invocation.Tool.Metadata["step"] == "" {
		return agentcore.PreparedExecution{}, fmt.Errorf("workflow Git status adapter metadata is invalid: %w", ErrNotAllowed)
	}
	if _, err := h.agent.validateCurrentAgentWorkflowTool(invocation.Tool.Metadata); err != nil {
		return agentcore.PreparedExecution{}, err
	}
	deps := executionDependenciesFor(h.agent)
	deps.mu.RLock()
	git := deps.git
	deps.mu.RUnlock()
	if git == nil {
		return agentcore.PreparedExecution{}, fmt.Errorf("Git service is not wired: %w", ErrNotAllowed)
	}
	root := h.agent.currentWorkspaceRoot()
	if root == "" {
		return agentcore.PreparedExecution{}, fmt.Errorf("workspace root is required: %w", ErrNotAllowed)
	}
	state := preparedWorkflowGitStatus{Root: root, RootGeneration: h.agent.agentWorkspaceGeneration()}
	opaque, err := json.Marshal(state)
	if err != nil {
		return agentcore.PreparedExecution{}, err
	}
	return agentcore.PreparedExecution{Summary: "Read Git status", Opaque: opaque}, nil
}

func (h *agentWorkflowGitStatusHandler) Execute(_ context.Context, invocation agentcore.Invocation, prepared agentcore.PreparedExecution) (agentcore.ExecutionOutput, error) {
	if _, err := h.agent.validateCurrentAgentWorkflowTool(invocation.Tool.Metadata); err != nil {
		return agentcore.ExecutionOutput{}, err
	}
	var state preparedWorkflowGitStatus
	if err := json.Unmarshal(prepared.Opaque, &state); err != nil {
		return agentcore.ExecutionOutput{}, err
	}
	if state.Root == "" || state.RootGeneration == 0 || state.Root != h.agent.currentWorkspaceRoot() || state.RootGeneration != h.agent.agentWorkspaceGeneration() {
		return agentcore.ExecutionOutput{}, fmt.Errorf("workspace changed after Git status approval: %w", ErrNotAllowed)
	}
	deps := executionDependenciesFor(h.agent)
	deps.mu.RLock()
	git := deps.git
	file := deps.file
	deps.mu.RUnlock()
	if git == nil || file == nil {
		return agentcore.ExecutionOutput{}, fmt.Errorf("Git status workspace capabilities are unavailable: %w", ErrNotAllowed)
	}
	capability, err := file.acquireCapability(state.Root, false)
	if err != nil {
		return agentcore.ExecutionOutput{}, err
	}
	defer capability.releaseCapability()
	if capability.relative != "." {
		return agentcore.ExecutionOutput{}, fmt.Errorf("Git status must use the workspace root capability: %w", ErrNotAllowed)
	}
	var changes []GitFileChange
	if err := capability.withCurrent(func() error {
		if err := capability.verifyRootPathIdentity(); err != nil {
			return err
		}
		var statusErr error
		changes, statusErr = git.GetStatus(state.Root)
		if statusErr != nil {
			return statusErr
		}
		return capability.verifyRootPathIdentity()
	}); err != nil {
		return agentcore.ExecutionOutput{}, err
	}
	encoded, metadata, err := encodeWorkflowGitStatusObservation(changes)
	if err != nil {
		return agentcore.ExecutionOutput{}, err
	}
	return agentcore.ExecutionOutput{Observation: encoded, Metadata: metadata}, nil
}

const maxWorkflowGitStatusEntries = 256

func encodeWorkflowGitStatusObservation(changes []GitFileChange) (string, map[string]string, error) {
	sort.SliceStable(changes, func(i, j int) bool {
		if changes[i].Path != changes[j].Path {
			return changes[i].Path < changes[j].Path
		}
		return changes[i].Status < changes[j].Status
	})
	total := len(changes)
	selected := make([]GitFileChange, 0, min(total, maxWorkflowGitStatusEntries))
	encodedSize := 2 // []
	for _, change := range changes {
		if len(selected) >= maxWorkflowGitStatusEntries {
			break
		}
		item, err := json.Marshal(change)
		if err != nil {
			return "", nil, err
		}
		additional := len(item)
		if len(selected) > 0 {
			additional++ // comma
		}
		if encodedSize+additional > maxAgentObservationBytes {
			break
		}
		selected = append(selected, change)
		encodedSize += additional
	}
	encoded, err := json.Marshal(selected)
	if err != nil {
		return "", nil, err
	}
	metadata := map[string]string{
		"totalEntries":    strconv.Itoa(total),
		"returnedEntries": strconv.Itoa(len(selected)),
	}
	if len(selected) != total {
		metadata["truncated"] = "true"
	}
	return string(encoded), metadata, nil
}

type preparedWorkflowMCP struct {
	Workflow  string                      `json:"workflow"`
	Step      string                      `json:"step"`
	Server    string                      `json:"server"`
	Tool      string                      `json:"tool"`
	InputHash string                      `json:"inputHash"`
	Delegated agentcore.PreparedExecution `json:"delegated"`
}

type agentWorkflowMCPHandler struct{ agent *AgentService }

var _ agentcore.ExternalMutationTransactionHandler = (*agentWorkflowMCPHandler)(nil)

func (*agentWorkflowMCPHandler) MutationMode() agentcore.MutationMode {
	return agentcore.MutationExternal
}

func (h *agentWorkflowMCPHandler) Prepare(ctx context.Context, invocation agentcore.Invocation) (agentcore.PreparedExecution, error) {
	metadata := invocation.Tool.Metadata
	if metadata["adapter"] != workflowAdapterMCP || metadata["workflow"] == "" || metadata["step"] == "" ||
		metadata["server"] == "" || metadata["tool"] == "" || metadata["delegatedTool"] != "mcp."+metadata["server"]+"."+metadata["tool"] {
		return agentcore.PreparedExecution{}, fmt.Errorf("workflow MCP adapter metadata is invalid: %w", ErrNotAllowed)
	}
	args, err := workflowMCPInvocationArguments(h.agent, invocation)
	if err != nil {
		return agentcore.PreparedExecution{}, err
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		return agentcore.PreparedExecution{}, fmt.Errorf("encode workflow MCP arguments: %w", ErrInvalidInput)
	}
	delegated := invocation
	delegated.Tool = agentcore.ToolDef{ID: metadata["delegatedTool"], Description: invocation.Tool.Description,
		InputSchema: invocation.Tool.InputSchema, Source: agentcore.SourceMCP, Risk: invocation.Tool.Risk,
		Approval: agentcore.ApprovalManual, Mutation: agentcore.MutationExternal, ExecuteKey: "mcp.call",
		Metadata: map[string]string{"server": metadata["server"], "tool": metadata["tool"]}}
	delegated.Arguments = encoded
	prepared, err := (&agentMCPHandler{agent: h.agent}).Prepare(ctx, delegated)
	if err != nil {
		return agentcore.PreparedExecution{}, err
	}
	opaque, err := json.Marshal(preparedWorkflowMCP{Workflow: metadata["workflow"], Step: metadata["step"],
		Server: metadata["server"], Tool: metadata["tool"], InputHash: metadata["inputHash"], Delegated: prepared})
	if err != nil {
		return agentcore.PreparedExecution{}, err
	}
	return agentcore.PreparedExecution{Summary: prepared.Summary, Opaque: opaque,
		Metadata: map[string]string{"workflow": metadata["workflow"], "step": metadata["step"]}}, nil
}

func workflowMCPInvocationArguments(agent *AgentService, invocation agentcore.Invocation) (map[string]interface{}, error) {
	metadata := invocation.Tool.Metadata
	expected, err := workflowMCPArgumentsForMetadata(agent, metadata)
	if err != nil {
		return nil, err
	}
	expectedEncoded, err := json.Marshal(expected)
	if err != nil {
		return nil, fmt.Errorf("encode workflow MCP arguments: %w", ErrInvalidInput)
	}
	if string(expectedEncoded) != string(invocation.Arguments) {
		return nil, fmt.Errorf("workflow MCP arguments differ from catalog-owned input: %w", ErrNotAllowed)
	}
	return expected, nil
}

func workflowMCPArgumentsForMetadata(agent *AgentService, metadata map[string]string) (map[string]interface{}, error) {
	step, err := agent.validateCurrentAgentWorkflowTool(metadata)
	if err != nil {
		return nil, err
	}
	inputHash, err := workflowInputHash(step.Input)
	if err != nil || inputHash != metadata["inputHash"] {
		return nil, fmt.Errorf("workflow MCP input changed after catalog publication: %w", ErrNotAllowed)
	}
	return step.Input, nil
}

func (h *agentWorkflowMCPHandler) decode(ctx context.Context, invocation agentcore.Invocation, prepared agentcore.PreparedExecution) (agentcore.Invocation, preparedWorkflowMCP, error) {
	var state preparedWorkflowMCP
	if err := json.Unmarshal(prepared.Opaque, &state); err != nil {
		return agentcore.Invocation{}, state, err
	}
	metadata := invocation.Tool.Metadata
	if state.Workflow != metadata["workflow"] || state.Step != metadata["step"] || state.Server != metadata["server"] || state.Tool != metadata["tool"] || state.InputHash != metadata["inputHash"] {
		return agentcore.Invocation{}, state, fmt.Errorf("workflow MCP prepared identity changed: %w", agentcore.ErrExternalMutationContract)
	}
	args, err := workflowMCPInvocationArguments(h.agent, invocation)
	if err != nil {
		return agentcore.Invocation{}, state, err
	}
	encoded, _ := json.Marshal(args)
	delegated := invocation
	delegated.Tool = agentcore.ToolDef{ID: metadata["delegatedTool"], Description: invocation.Tool.Description, InputSchema: invocation.Tool.InputSchema, Source: agentcore.SourceMCP, Risk: invocation.Tool.Risk, Approval: agentcore.ApprovalManual, Mutation: agentcore.MutationExternal, ExecuteKey: "mcp.call", Metadata: map[string]string{"server": metadata["server"], "tool": metadata["tool"]}}
	delegated.Arguments = encoded
	return delegated, state, nil
}

func (h *agentWorkflowMCPHandler) Execute(ctx context.Context, invocation agentcore.Invocation, prepared agentcore.PreparedExecution) (agentcore.ExecutionOutput, error) {
	output, _, err := h.ExecuteExternalTransaction(ctx, invocation, prepared)
	return output, err
}

func (h *agentWorkflowMCPHandler) BeginExternalMutation(ctx context.Context, invocation agentcore.Invocation, prepared agentcore.PreparedExecution) (agentcore.ExternalMutationReceipt, error) {
	delegated, state, err := h.decode(ctx, invocation, prepared)
	if err != nil {
		return agentcore.ExternalMutationReceipt{}, err
	}
	receipt, err := (&agentMCPHandler{agent: h.agent}).BeginExternalMutation(ctx, delegated, state.Delegated)
	if err != nil {
		return agentcore.ExternalMutationReceipt{}, err
	}
	if receipt.Metadata == nil {
		receipt.Metadata = make(map[string]string)
	}
	receipt.Metadata["workflow"] = state.Workflow
	receipt.Metadata["step"] = state.Step
	return receipt, nil
}

func (h *agentWorkflowMCPHandler) ExecuteExternalTransaction(ctx context.Context, invocation agentcore.Invocation, prepared agentcore.PreparedExecution) (agentcore.ExecutionOutput, agentcore.ExternalMutationReceipt, error) {
	receipt, err := h.BeginExternalMutation(ctx, invocation, prepared)
	if err != nil {
		return agentcore.ExecutionOutput{}, agentcore.ExternalMutationReceipt{}, err
	}
	output, executeErr := h.ExecuteExternalTransactionWithReceipt(ctx, invocation, prepared, receipt)
	return output, receipt, executeErr
}

func (h *agentWorkflowMCPHandler) ExecuteExternalTransactionWithReceipt(ctx context.Context, invocation agentcore.Invocation, prepared agentcore.PreparedExecution, receipt agentcore.ExternalMutationReceipt) (agentcore.ExecutionOutput, error) {
	delegated, state, err := h.decode(ctx, invocation, prepared)
	if err != nil {
		return agentcore.ExecutionOutput{}, err
	}
	if receipt.ID == "" || receipt.Reversible || receipt.Metadata["workflow"] != state.Workflow || receipt.Metadata["step"] != state.Step {
		return agentcore.ExecutionOutput{}, fmt.Errorf("workflow MCP receipt does not match its step: %w", agentcore.ErrExternalMutationContract)
	}
	return (&agentMCPHandler{agent: h.agent}).ExecuteExternalTransactionWithReceipt(ctx, delegated, state.Delegated, receipt)
}

func (h *agentWorkflowMCPHandler) CompensateExternalTransaction(ctx context.Context, invocation agentcore.Invocation, prepared agentcore.PreparedExecution, receipt agentcore.ExternalMutationReceipt) error {
	delegated, state, err := h.decode(ctx, invocation, prepared)
	if err != nil {
		return err
	}
	if receipt.Metadata["workflow"] != state.Workflow || receipt.Metadata["step"] != state.Step {
		return fmt.Errorf("workflow MCP compensation receipt does not match its step: %w", agentcore.ErrExternalMutationContract)
	}
	return (&agentMCPHandler{agent: h.agent}).CompensateExternalTransaction(ctx, delegated, state.Delegated, receipt)
}

func ensureSkillAgentHandler(agent *AgentService, runtime *agentcore.Runtime) error {
	deps := executionDependenciesFor(agent)
	deps.mu.Lock()
	defer deps.mu.Unlock()
	if deps.skillHandlerRegistered {
		return nil
	}
	if err := runtime.RegisterHandler("skill.activate", &agentSkillActivationHandler{agent: agent}); err != nil {
		return err
	}
	deps.skillHandlerRegistered = true
	return nil
}

func workflowCommandRisk(agent *AgentService, command string) agentcore.Risk {
	check := agent.CheckCommand(command)
	if check.RiskLevel == RiskDangerous || check.Blocked {
		return agentcore.RiskDangerous
	}
	return agentcore.RiskElevated
}

func dynamicAgentToolID(prefix string, values ...string) string {
	raw := strings.Join(values, "\x00")
	slug := strings.Trim(dynamicAgentToolSlugPattern.ReplaceAllString(strings.Join(values, "-"), "-"), "-_")
	if slug == "" {
		slug = "action"
	}
	if len(slug) > 40 {
		slug = slug[:40]
	}
	sum := sha256.Sum256([]byte(raw))
	return prefix + "." + slug + "." + hex.EncodeToString(sum[:6])
}

func (s *AgentService) approveWorkflowAgentTool(request agentcore.ApprovalRequest, prompt ...bool) (bool, error) {
	showPrompt := true
	if len(prompt) > 0 {
		showPrompt = prompt[0]
	}
	if _, err := s.validateCurrentAgentWorkflowTool(request.Invocation.Tool.Metadata); err != nil {
		return false, err
	}
	switch request.Invocation.Tool.Metadata["adapter"] {
	case workflowAdapterFileRead:
		if request.Invocation.Tool.Approval != agentcore.ApprovalBackendPolicy ||
			request.Invocation.Tool.Risk != agentcore.RiskReadOnly ||
			request.Invocation.Tool.Mutation != agentcore.MutationNone {
			return false, fmt.Errorf("workflow file read policy metadata is invalid: %w", ErrNotAllowed)
		}
		pathValue, err := normalizeWorkflowFileReadPath(request.Invocation.Tool.Metadata["path"])
		if err != nil || pathValue != request.Invocation.Tool.Metadata["path"] {
			return false, fmt.Errorf("workflow file read path is invalid: %w", ErrNotAllowed)
		}
		return true, nil
	case workflowAdapterFileWrite:
		tool := request.Invocation.Tool
		if tool.Approval != agentcore.ApprovalManual || tool.Risk != agentcore.RiskElevated ||
			tool.Mutation != agentcore.MutationWorkspaceTransaction || tool.ExecuteKey != "workflow.file.write" ||
			tool.Metadata["command"] != "" || tool.Metadata["cwd"] != "" || tool.Metadata["path"] == "" ||
			len(tool.Metadata["contentHash"]) != 64 {
			return false, fmt.Errorf("workflow file write policy metadata is invalid: %w", ErrNotAllowed)
		}
		pathValue, err := normalizeWorkflowFileReadPath(tool.Metadata["path"])
		if err != nil || pathValue != tool.Metadata["path"] {
			return false, fmt.Errorf("workflow file write path is invalid: %w", ErrNotAllowed)
		}
		if _, err := hex.DecodeString(tool.Metadata["contentHash"]); err != nil {
			return false, fmt.Errorf("workflow file write content hash is invalid: %w", ErrNotAllowed)
		}
		contentBytes, err := strconv.Atoi(tool.Metadata["contentBytes"])
		if err != nil || contentBytes < 0 || contentBytes > maxWorkflowFileWriteBytes {
			return false, fmt.Errorf("workflow file write content size is invalid: %w", ErrNotAllowed)
		}
		step, err := s.validateCurrentAgentWorkflowTool(tool.Metadata)
		if err != nil {
			return false, err
		}
		_, content, err := workflowFileWriteInput(step)
		if err != nil || hashWorkflowFileContent(content) != tool.Metadata["contentHash"] || len([]byte(content)) != contentBytes {
			return false, fmt.Errorf("workflow file write content changed after catalog publication: %w", ErrNotAllowed)
		}
		root := s.currentWorkspaceRoot()
		absPath, err := ValidateMutatingPathWithinRoot(root, filepath.Join(root, filepath.FromSlash(pathValue)))
		if err != nil {
			return false, err
		}
		if !showPrompt {
			return true, nil
		}
		if s.approveWrite == nil {
			return false, fmt.Errorf("workflow file write approval is unavailable: %w", ErrNotAllowed)
		}
		return s.approveWrite(absPath, int64(contentBytes)), nil
	case workflowAdapterGitStatus:
		if request.Invocation.Tool.Approval != agentcore.ApprovalBackendPolicy ||
			request.Invocation.Tool.Risk != agentcore.RiskReadOnly ||
			request.Invocation.Tool.Mutation != agentcore.MutationNone {
			return false, fmt.Errorf("workflow Git status policy metadata is invalid: %w", ErrNotAllowed)
		}
		return true, nil
	case workflowAdapterSkillActivate:
		tool := request.Invocation.Tool
		if tool.Approval != agentcore.ApprovalManual || tool.Risk != agentcore.RiskElevated ||
			tool.Mutation != agentcore.MutationExternal || tool.ExecuteKey != "skill.activate" ||
			strings.TrimSpace(tool.Metadata["workflow"]) == "" || strings.TrimSpace(tool.Metadata["step"]) == "" ||
			tool.Metadata["command"] != "" || tool.Metadata["cwd"] != "" || tool.Metadata["path"] != "" {
			return false, fmt.Errorf("workflow Skill activation policy metadata is invalid: %w", ErrNotAllowed)
		}
		if skillID, err := normalizeWorkflowSkillID(tool.Metadata["skillId"]); err != nil || skillID != tool.Metadata["skillId"] {
			return false, fmt.Errorf("workflow Skill activation id is invalid: %w", ErrNotAllowed)
		}
		if !isAgentSkillScopeValid(SkillScope(tool.Metadata["scope"])) || !isValidSkillFingerprint(tool.Metadata["fingerprint"]) {
			return false, fmt.Errorf("workflow Skill activation identity is invalid: %w", ErrNotAllowed)
		}
		return s.approveSkillAgentTool(request, showPrompt)
	case workflowAdapterMCP:
		server := request.Invocation.Tool.Metadata["server"]
		tool := request.Invocation.Tool.Metadata["tool"]
		delegated := request.Invocation.Tool.Metadata["delegatedTool"]
		if server == "" || tool == "" || delegated != "mcp."+server+"."+tool || request.Invocation.Tool.Mutation != agentcore.MutationExternal {
			return false, fmt.Errorf("workflow MCP policy metadata is invalid: %w", ErrNotAllowed)
		}
		deps := executionDependenciesFor(s)
		deps.mu.RLock()
		mcp := deps.mcp
		deps.mu.RUnlock()
		if mcp == nil {
			return false, fmt.Errorf("MCP approval service is unavailable: %w", ErrNotAllowed)
		}
		if !showPrompt {
			return true, nil
		}
		if mcp.approveTool == nil {
			return false, fmt.Errorf("MCP approval service is unavailable: %w", ErrNotAllowed)
		}
		risk := RiskElevated
		if request.Invocation.Tool.Risk == agentcore.RiskDangerous {
			risk = RiskDangerous
		}
		return mcp.approveTool(server, tool, string(request.Invocation.Arguments), risk), nil
	case workflowAdapterAI:
		tool := request.Invocation.Tool
		if tool.Approval != agentcore.ApprovalManual || tool.Risk != agentcore.RiskElevated ||
			tool.Mutation != agentcore.MutationExternal || tool.ExecuteKey != "workflow.ai" ||
			tool.Metadata["operation"] == "" || tool.Metadata["promptHash"] == "" ||
			tool.Metadata["command"] != "" || tool.Metadata["cwd"] != "" || tool.Metadata["path"] != "" {
			return false, fmt.Errorf("workflow AI policy metadata is invalid: %w", ErrNotAllowed)
		}
		deps := executionDependenciesFor(s)
		deps.mu.RLock()
		ai := deps.ai
		approveAI := s.approveAI
		deps.mu.RUnlock()
		if ai == nil || approveAI == nil {
			return false, fmt.Errorf("workflow AI approval service is unavailable: %w", ErrNotAllowed)
		}
		if _, _, err := ai.resolveAgentOperation(AIOperation(tool.Metadata["operation"])); err != nil {
			return false, err
		}
		if !showPrompt {
			return true, nil
		}
		return approveAI(tool.Metadata["operation"]), nil
	case workflowAdapterCommand:
		// Continue through the command-specific approval below.
	default:
		return false, fmt.Errorf("workflow adapter %q has no approval policy: %w", request.Invocation.Tool.Metadata["adapter"], ErrNotAllowed)
	}
	command := request.Invocation.Tool.Metadata["command"]
	cwd := request.Invocation.Tool.Metadata["cwd"]
	if command == "" {
		return false, fmt.Errorf("workflow ToolDef command is missing: %w", ErrNotAllowed)
	}
	check := s.CheckCommand(command)
	if check.Blocked {
		return false, fmt.Errorf("workflow command blocked: %s", check.BlockReason)
	}
	if cwd == "" {
		cwd = s.currentWorkspaceRoot()
	}
	validatedCwd, err := s.validateCwd(cwd)
	if err != nil {
		return false, err
	}
	if !showPrompt {
		return true, nil
	}
	if s.approveCommand == nil {
		return false, fmt.Errorf("workflow command approval is unavailable: %w", ErrNotAllowed)
	}
	return s.approveCommand(command, validatedCwd, check.RiskLevel), nil
}

func (s *AgentService) approveSkillAgentTool(request agentcore.ApprovalRequest, prompt ...bool) (bool, error) {
	showPrompt := true
	if len(prompt) > 0 {
		showPrompt = prompt[0]
	}
	skillID := request.Invocation.Tool.Metadata["skillId"]
	deps := executionDependenciesFor(s)
	deps.mu.RLock()
	skills := deps.skills
	approveSkill := deps.approveSkill
	deps.mu.RUnlock()
	if skills == nil || skillID == "" {
		return false, fmt.Errorf("skill approval service is unavailable: %w", ErrNotAllowed)
	}
	skill, err := skills.GetSkill(skillID)
	if err != nil {
		return false, err
	}
	if string(skill.Scope) != request.Invocation.Tool.Metadata["scope"] {
		return false, fmt.Errorf("skill scope changed after catalog publication: %w", ErrNotAllowed)
	}
	if expected := request.Invocation.Tool.Metadata["fingerprint"]; !isValidSkillFingerprint(expected) {
		return false, fmt.Errorf("skill fingerprint metadata is invalid: %w", ErrNotAllowed)
	} else if actual, fingerprintErr := skillFingerprint(skill); fingerprintErr != nil || actual != expected {
		return false, fmt.Errorf("skill changed after catalog publication: %w", ErrNotAllowed)
	}
	if !skill.IsProjectScoped() || skills.IsApproved(skillID) || !showPrompt {
		return true, nil
	}
	if approveSkill == nil {
		return false, fmt.Errorf("project skill approval is unavailable: %w", ErrNotAllowed)
	}
	return approveSkill(skill), nil
}

func isValidSkillFingerprint(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func nativeSkillApproval(skill Skill) bool {
	app := application.Get()
	if app == nil {
		return false
	}
	result := make(chan bool, 1)
	dialog := app.Dialog.Question().SetTitle("Approve project skill").SetMessage(
		fmt.Sprintf("Project skill: %s\n\n%s", skill.Name, skill.Description),
	)
	dialog.AddButton("Yes").SetAsDefault().OnClick(func() { result <- true })
	dialog.AddButton("No").SetAsCancel().OnClick(func() { result <- false })
	dialog.Show()
	select {
	case approved := <-result:
		return approved
	case <-time.After(5 * time.Minute):
		return false
	}
}

type agentWorkflowCommandHandler struct{ agent *AgentService }

var _ agentcore.ExternalMutationTransactionHandler = (*agentWorkflowCommandHandler)(nil)

func (*agentWorkflowCommandHandler) MutationMode() agentcore.MutationMode {
	return agentcore.MutationExternal
}

func (h *agentWorkflowCommandHandler) Prepare(ctx context.Context, invocation agentcore.Invocation) (agentcore.PreparedExecution, error) {
	if _, err := h.agent.validateCurrentAgentWorkflowTool(invocation.Tool.Metadata); err != nil {
		return agentcore.PreparedExecution{}, err
	}
	arguments, err := json.Marshal(preparedRun{
		Command: invocation.Tool.Metadata["command"],
		Cwd:     invocation.Tool.Metadata["cwd"],
	})
	if err != nil {
		return agentcore.PreparedExecution{}, err
	}
	invocation.Arguments = arguments
	return (&agentRunHandler{agent: h.agent}).Prepare(ctx, invocation)
}

func (h *agentWorkflowCommandHandler) Execute(ctx context.Context, invocation agentcore.Invocation, prepared agentcore.PreparedExecution) (agentcore.ExecutionOutput, error) {
	output, _, err := h.ExecuteExternalTransaction(ctx, invocation, prepared)
	return output, err
}

func (h *agentWorkflowCommandHandler) ExecuteExternalTransaction(ctx context.Context, invocation agentcore.Invocation, prepared agentcore.PreparedExecution) (agentcore.ExecutionOutput, agentcore.ExternalMutationReceipt, error) {
	receipt, err := h.BeginExternalMutation(ctx, invocation, prepared)
	if err != nil {
		return agentcore.ExecutionOutput{}, agentcore.ExternalMutationReceipt{}, err
	}
	output, executeErr := h.ExecuteExternalTransactionWithReceipt(ctx, invocation, prepared, receipt)
	return output, receipt, executeErr
}

func (h *agentWorkflowCommandHandler) BeginExternalMutation(ctx context.Context, invocation agentcore.Invocation, prepared agentcore.PreparedExecution) (agentcore.ExternalMutationReceipt, error) {
	if _, err := h.agent.validateCurrentAgentWorkflowTool(invocation.Tool.Metadata); err != nil {
		return agentcore.ExternalMutationReceipt{}, err
	}
	receipt, err := (&agentRunHandler{agent: h.agent}).BeginExternalMutation(ctx, invocation, prepared)
	if err != nil {
		return agentcore.ExternalMutationReceipt{}, err
	}
	if receipt.Metadata == nil {
		receipt.Metadata = make(map[string]string)
	}
	receipt.Metadata["workflow"] = invocation.Tool.Metadata["workflow"]
	receipt.Metadata["step"] = invocation.Tool.Metadata["step"]
	return receipt, nil
}

func (h *agentWorkflowCommandHandler) ExecuteExternalTransactionWithReceipt(ctx context.Context, invocation agentcore.Invocation, prepared agentcore.PreparedExecution, receipt agentcore.ExternalMutationReceipt) (agentcore.ExecutionOutput, error) {
	if _, err := h.agent.validateCurrentAgentWorkflowTool(invocation.Tool.Metadata); err != nil {
		return agentcore.ExecutionOutput{}, err
	}
	if receipt.Metadata["workflow"] != invocation.Tool.Metadata["workflow"] || receipt.Metadata["step"] != invocation.Tool.Metadata["step"] {
		return agentcore.ExecutionOutput{}, fmt.Errorf("workflow command receipt does not match the approved step: %w", agentcore.ErrExternalMutationContract)
	}
	return (&agentRunHandler{agent: h.agent}).ExecuteExternalTransactionWithReceipt(ctx, invocation, prepared, receipt)
}

func (h *agentWorkflowCommandHandler) CompensateExternalTransaction(ctx context.Context, invocation agentcore.Invocation, prepared agentcore.PreparedExecution, receipt agentcore.ExternalMutationReceipt) error {
	return (&agentRunHandler{agent: h.agent}).CompensateExternalTransaction(ctx, invocation, prepared, receipt)
}

type preparedSkillActivation struct {
	SkillID        string `json:"skillId"`
	Scope          string `json:"scope"`
	Fingerprint    string `json:"fingerprint"`
	Workflow       string `json:"workflow,omitempty"`
	Step           string `json:"step,omitempty"`
	RootGeneration uint64 `json:"rootGeneration"`
}

type agentSkillActivationHandler struct{ agent *AgentService }

var _ agentcore.ExternalMutationTransactionHandler = (*agentSkillActivationHandler)(nil)

func (*agentSkillActivationHandler) MutationMode() agentcore.MutationMode {
	return agentcore.MutationExternal
}

func (h *agentSkillActivationHandler) Prepare(_ context.Context, invocation agentcore.Invocation) (agentcore.PreparedExecution, error) {
	deps := executionDependenciesFor(h.agent)
	deps.mu.RLock()
	skills := deps.skills
	deps.mu.RUnlock()
	if skills == nil {
		return agentcore.PreparedExecution{}, fmt.Errorf("skills service is not wired: %w", ErrNotAllowed)
	}
	metadata := invocation.Tool.Metadata
	skillID := metadata["skillId"]
	if invocation.Tool.Source != agentcore.SourceSkill && invocation.Tool.Source != agentcore.SourceWorkflow {
		return agentcore.PreparedExecution{}, fmt.Errorf("skill activation source is invalid: %w", ErrNotAllowed)
	}
	if invocation.Tool.Source == agentcore.SourceWorkflow {
		if metadata["adapter"] != workflowAdapterSkillActivate || strings.TrimSpace(metadata["workflow"]) == "" || strings.TrimSpace(metadata["step"]) == "" {
			return agentcore.PreparedExecution{}, fmt.Errorf("workflow Skill activation ownership metadata is invalid: %w", ErrNotAllowed)
		}
		if metadata["command"] != "" || metadata["cwd"] != "" || metadata["path"] != "" {
			return agentcore.PreparedExecution{}, fmt.Errorf("workflow Skill activation cannot carry command metadata: %w", ErrNotAllowed)
		}
		if _, err := h.agent.validateCurrentAgentWorkflowTool(metadata); err != nil {
			return agentcore.PreparedExecution{}, err
		}
	}
	if skillID == "" || (invocation.Tool.Source == agentcore.SourceWorkflow && func() bool {
		canonical, err := normalizeWorkflowSkillID(skillID)
		return err != nil || canonical != skillID
	}()) {
		return agentcore.PreparedExecution{}, fmt.Errorf("skill activation id is invalid: %w", ErrNotAllowed)
	}
	skill, err := skills.GetSkill(skillID)
	if err != nil {
		return agentcore.PreparedExecution{}, err
	}
	fingerprint, err := skillFingerprint(skill)
	if err != nil {
		return agentcore.PreparedExecution{}, err
	}
	if !isAgentSkillScopeValid(skill.Scope) || metadata["scope"] != string(skill.Scope) ||
		!isValidSkillFingerprint(metadata["fingerprint"]) || metadata["fingerprint"] != fingerprint {
		return agentcore.PreparedExecution{}, fmt.Errorf("skill activation catalog identity is stale or invalid: %w", ErrNotAllowed)
	}
	state := preparedSkillActivation{
		SkillID: skill.ID, Scope: string(skill.Scope), Fingerprint: fingerprint,
		Workflow: metadata["workflow"], Step: metadata["step"],
		RootGeneration: h.agent.agentWorkspaceGeneration(),
	}
	opaque, _ := json.Marshal(state)
	return agentcore.PreparedExecution{
		Summary: fmt.Sprintf("Activate skill %q (%s)", skill.Name, skill.Scope),
		Opaque:  opaque, Metadata: map[string]string{"skillId": skill.ID, "scope": string(skill.Scope)},
	}, nil
}

func (h *agentSkillActivationHandler) Execute(ctx context.Context, invocation agentcore.Invocation, prepared agentcore.PreparedExecution) (agentcore.ExecutionOutput, error) {
	output, _, err := h.ExecuteExternalTransaction(ctx, invocation, prepared)
	return output, err
}

func (h *agentSkillActivationHandler) BeginExternalMutation(_ context.Context, invocation agentcore.Invocation, prepared agentcore.PreparedExecution) (agentcore.ExternalMutationReceipt, error) {
	var state preparedSkillActivation
	if err := json.Unmarshal(prepared.Opaque, &state); err != nil {
		return agentcore.ExternalMutationReceipt{}, err
	}
	if state.RootGeneration != h.agent.agentWorkspaceGeneration() {
		return agentcore.ExternalMutationReceipt{}, fmt.Errorf("workspace changed after skill approval: %w", ErrNotAllowed)
	}
	if state.Workflow != "" {
		if _, err := h.agent.validateCurrentAgentWorkflowTool(invocation.Tool.Metadata); err != nil {
			return agentcore.ExternalMutationReceipt{}, err
		}
	}
	deps := executionDependenciesFor(h.agent)
	deps.mu.RLock()
	skills := deps.skills
	deps.mu.RUnlock()
	if skills == nil {
		return agentcore.ExternalMutationReceipt{}, fmt.Errorf("skills service is not wired: %w", ErrNotAllowed)
	}
	skill, err := skills.GetSkill(state.SkillID)
	if err != nil {
		return agentcore.ExternalMutationReceipt{}, err
	}
	fingerprint, err := skillFingerprint(skill)
	if err != nil {
		return agentcore.ExternalMutationReceipt{}, err
	}
	if fingerprint != state.Fingerprint || string(skill.Scope) != state.Scope {
		return agentcore.ExternalMutationReceipt{}, fmt.Errorf("skill changed after approval: %w", ErrNotAllowed)
	}
	if state.Workflow != invocation.Tool.Metadata["workflow"] || state.Step != invocation.Tool.Metadata["step"] {
		return agentcore.ExternalMutationReceipt{}, fmt.Errorf("workflow Skill ownership changed after approval: %w", ErrNotAllowed)
	}
	priorApproved := skills.IsApproved(state.SkillID)
	priorBinding, priorBindingPresent := agentSkillBindingSnapshot(h.agent, invocation.SessionID, state.SkillID)
	return newAgentExternalMutationReceipt("skill", true, map[string]string{
		"skillId": state.SkillID, "sessionId": invocation.SessionID, "fingerprint": state.Fingerprint,
		"scope": state.Scope, "workflow": state.Workflow, "step": state.Step,
		"priorApproved":       boolString(priorApproved),
		"priorBindingPresent": boolString(priorBindingPresent), "priorBinding": priorBinding,
	})

}

func (h *agentSkillActivationHandler) ExecuteExternalTransaction(ctx context.Context, invocation agentcore.Invocation, prepared agentcore.PreparedExecution) (agentcore.ExecutionOutput, agentcore.ExternalMutationReceipt, error) {
	receipt, err := h.BeginExternalMutation(ctx, invocation, prepared)
	if err != nil {
		return agentcore.ExecutionOutput{}, agentcore.ExternalMutationReceipt{}, err
	}
	output, executeErr := h.ExecuteExternalTransactionWithReceipt(ctx, invocation, prepared, receipt)
	return output, receipt, executeErr
}

func (h *agentSkillActivationHandler) ExecuteExternalTransactionWithReceipt(_ context.Context, invocation agentcore.Invocation, prepared agentcore.PreparedExecution, receipt agentcore.ExternalMutationReceipt) (agentcore.ExecutionOutput, error) {
	var state preparedSkillActivation
	if err := json.Unmarshal(prepared.Opaque, &state); err != nil {
		return agentcore.ExecutionOutput{}, err
	}
	if state.RootGeneration != h.agent.agentWorkspaceGeneration() {
		return agentcore.ExecutionOutput{}, fmt.Errorf("workspace changed after skill approval: %w", ErrNotAllowed)
	}
	if state.Workflow != "" {
		if _, err := h.agent.validateCurrentAgentWorkflowTool(invocation.Tool.Metadata); err != nil {
			return agentcore.ExecutionOutput{}, err
		}
	}
	if state.Workflow != invocation.Tool.Metadata["workflow"] || state.Step != invocation.Tool.Metadata["step"] {
		return agentcore.ExecutionOutput{}, fmt.Errorf("workflow Skill ownership changed during execution: %w", ErrNotAllowed)
	}
	metadata := receipt.Metadata
	if receipt.ID == "" || !receipt.Reversible || metadata["skillId"] != state.SkillID || metadata["sessionId"] != invocation.SessionID || metadata["fingerprint"] != state.Fingerprint ||
		metadata["workflow"] != state.Workflow || metadata["step"] != state.Step {
		return agentcore.ExecutionOutput{}, fmt.Errorf("skill activation requires its preallocated receipt: %w", agentcore.ErrExternalMutationContract)
	}
	deps := executionDependenciesFor(h.agent)
	deps.mu.RLock()
	skills := deps.skills
	deps.mu.RUnlock()
	if skills == nil {
		return agentcore.ExecutionOutput{}, fmt.Errorf("skills service is not wired: %w", ErrNotAllowed)
	}
	skill, err := skills.GetSkill(state.SkillID)
	if err != nil {
		return agentcore.ExecutionOutput{}, err
	}
	fingerprint, err := skillFingerprint(skill)
	if err != nil {
		return agentcore.ExecutionOutput{}, err
	}
	if fingerprint != state.Fingerprint || string(skill.Scope) != state.Scope {
		return agentcore.ExecutionOutput{}, fmt.Errorf("skill changed after approval: %w", ErrNotAllowed)
	}
	if err := skills.activateSkillTrusted(state.SkillID); err != nil {
		return agentcore.ExecutionOutput{}, err
	}
	if err := bindAgentSkillSession(h.agent, invocation.SessionID, skill, state.Fingerprint); err != nil {
		return agentcore.ExecutionOutput{}, err
	}
	outputMetadata := map[string]string{"skillId": state.SkillID, "scope": state.Scope}
	if state.Workflow != "" {
		outputMetadata["workflow"] = state.Workflow
		outputMetadata["step"] = state.Step
	}
	return agentcore.ExecutionOutput{
		Observation: fmt.Sprintf("Activated skill %s.", state.SkillID),
		Metadata:    outputMetadata,
	}, nil
}

func (h *agentSkillActivationHandler) CompensateExternalTransaction(_ context.Context, invocation agentcore.Invocation, prepared agentcore.PreparedExecution, receipt agentcore.ExternalMutationReceipt) error {
	if receipt.ID == "" || !receipt.Reversible {
		return fmt.Errorf("skill receipt is not a reversible activation receipt: %w", agentcore.ErrExternalMutationContract)
	}
	var state preparedSkillActivation
	if err := json.Unmarshal(prepared.Opaque, &state); err != nil {
		return err
	}
	metadata := receipt.Metadata
	if metadata["skillId"] != state.SkillID || metadata["sessionId"] != invocation.SessionID || metadata["fingerprint"] != state.Fingerprint ||
		metadata["workflow"] != state.Workflow || metadata["step"] != state.Step {
		return fmt.Errorf("skill compensation receipt does not match activation: %w", agentcore.ErrExternalMutationContract)
	}
	priorBindingPresent := metadata["priorBindingPresent"] == "true"
	priorApproved := metadata["priorApproved"] == "true"
	return restoreAgentSkillActivation(h.agent, invocation.SessionID, state.SkillID, state.Fingerprint, metadata["priorBinding"], priorBindingPresent, priorApproved, SkillScope(state.Scope))
}

func skillFingerprint(skill Skill) (string, error) {
	encoded, err := json.Marshal(struct {
		ID           string
		Name         string
		Description  string
		SystemPrompt string
		AllowedTools []string
		AllowedMCP   []string
		Scope        SkillScope
	}{
		ID: skill.ID, Name: skill.Name, Description: skill.Description,
		SystemPrompt: skill.SystemPrompt, AllowedTools: skill.AllowedTools,
		AllowedMCP: skill.AllowedMCP, Scope: skill.Scope,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}
