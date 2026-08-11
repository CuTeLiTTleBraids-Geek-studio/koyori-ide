package services

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// WorkflowStep is a single step in a multi-step workflow (N-19).
// Steps can depend on other steps via dependsOn, enabling build → test →
// deploy pipelines. The condition field (optional) is a shell command that
// must exit 0 for the step to run; a non-zero exit skips the step.
//
// Plan 11 Task 11 Step 1: extended with Type (command/ai/git/file/mcp/skill),
// OnFailure (abort/continue/skip/retry), and Timeout.
type WorkflowStep struct {
	Name      string   `json:"name" yaml:"name"`
	Command   string   `json:"command" yaml:"command"`
	Args      []string `json:"args,omitempty" yaml:"args,omitempty"`
	Cwd       string   `json:"cwd,omitempty" yaml:"cwd,omitempty"`
	DependsOn []string `json:"dependsOn,omitempty" yaml:"dependsOn,omitempty"`
	Condition string   `json:"condition,omitempty" yaml:"condition,omitempty"`
	// ExpectSuccess controls whether a non-zero exit code aborts the
	// workflow. When nil (not set in the workflow file), the frontend
	// treats it as true — failures are fatal. Set to false to allow a
	// step to fail without blocking subsequent steps (Plan 61 / N-24).
	ExpectSuccess *bool `json:"expectSuccess,omitempty" yaml:"expectSuccess,omitempty"`
	// Plan 11 Task 11 Step 1: Type specifies the step kind.
	// Supported: "command" (default), "ai", "git", "file", "mcp", "skill".
	Type WorkflowStepType `json:"type,omitempty" yaml:"type,omitempty"`
	// Plan 11 Task 11 Step 1: OnFailure controls behavior when the step fails.
	// Supported: "abort" (default), "continue", "skip", "retry".
	OnFailure OnFailureAction `json:"onFailure,omitempty" yaml:"onFailure,omitempty"`
	// Plan 11 Task 11 Step 1: Timeout is the maximum execution time in seconds.
	// 0 means no timeout.
	Timeout int `json:"timeout,omitempty" yaml:"timeout,omitempty"`
}

// WorkflowStepType specifies the kind of a workflow step (Step 1).
type WorkflowStepType string

const (
	WorkflowStepCommand WorkflowStepType = "command" // shell command (default)
	WorkflowStepAI      WorkflowStepType = "ai"      // AI generate/review/commit-msg (Step 4)
	WorkflowStepGit     WorkflowStepType = "git"     // git operation
	WorkflowStepFile    WorkflowStepType = "file"    // file read/write
	WorkflowStepMCP     WorkflowStepType = "mcp"     // MCP tool call
	WorkflowStepSkill   WorkflowStepType = "skill"   // Skills system
)

// OnFailureAction controls step failure behavior (Step 1).
type OnFailureAction string

const (
	OnFailureAbort    OnFailureAction = "abort"    // abort entire workflow (default)
	OnFailureContinue OnFailureAction = "continue" // continue to next step
	OnFailureSkip     OnFailureAction = "skip"     // skip dependent steps
	OnFailureRetry    OnFailureAction = "retry"    // retry the step
)

// WorkflowTrigger describes an event that auto-runs a workflow (Proposal B).
// When the event fires and the changed file matches Glob, the workflow is
// triggered automatically. Supported events: "file-saved", "startup",
// "workflow-completed" (Proposal R / N-58).
//
// Plan 11 Task 11 Step 2: extended with Condition (branch/language/glob)
// and additional runOn events (fileChange/manual/schedule/shortcut).
type WorkflowTrigger struct {
	// Event is the trigger event name. Supported: "file-saved",
	// "startup", "workflow-completed".
	// Plan 11 Task 11 Step 2: also "fileChange", "manual", "schedule", "shortcut".
	Event string `json:"event,omitempty" yaml:"event,omitempty"`
	// Glob is a glob pattern matched against the file path relative to
	// the project root (forward slashes). Supports "*" within a segment
	// and "**" across segments. Empty matches all files.
	// Only used by the "file-saved" event.
	Glob string `json:"glob,omitempty" yaml:"glob,omitempty"`
	// WorkflowName, when set with event "workflow-completed", restricts
	// the trigger to fire only when the completed workflow's name matches.
	// Empty means any workflow completion triggers this workflow.
	// Proposal R / N-58.
	WorkflowName string `json:"workflowName,omitempty" yaml:"workflowName,omitempty"`
	// Plan 11 Task 11 Step 2: Condition restricts when the trigger fires.
	// Branch matches git branch name; Language matches file language.
	Condition *WorkflowTriggerCondition `json:"condition,omitempty" yaml:"condition,omitempty"`
}

// WorkflowTriggerCondition restricts when a trigger fires (Step 2).
type WorkflowTriggerCondition struct {
	// Branch is a glob pattern matched against the current git branch.
	// Empty matches all branches.
	Branch string `json:"branch,omitempty" yaml:"branch,omitempty"`
	// Language is a language ID (e.g. "go", "typescript") matched against
	// the changed file's language. Empty matches all languages.
	Language string `json:"language,omitempty" yaml:"language,omitempty"`
	// FileGlob is an additional glob pattern for file matching (alias
	// for Glob on the trigger, allowing more complex conditions).
	FileGlob string `json:"fileGlob,omitempty" yaml:"fileGlob,omitempty"`
}

// WorkflowDef describes a multi-step workflow loaded from
// .koyori-ide/workflows/*.yml (or .yaml/.json). A workflow is an ordered list of
// steps with optional dependencies, conditions, and file-watch triggers.
// Single-command tasks (.koyori-ide/tasks.json) are a degenerate case of a
// workflow with one step and no dependencies.
type WorkflowDef struct {
	Name        string         `json:"name" yaml:"name"`
	Description string         `json:"description,omitempty" yaml:"description,omitempty"`
	Steps       []WorkflowStep `json:"steps" yaml:"steps"`
	Watch       []string       `json:"watch,omitempty" yaml:"watch,omitempty"`
	// RunOn, when set, auto-triggers the workflow on an IDE event
	// (e.g. file-saved). The frontend listens for the event and matches
	// the changed file path against RunOn.Glob (Proposal B).
	RunOn *WorkflowTrigger `json:"runOn,omitempty" yaml:"runOn,omitempty"`
	// G-SEC-03: RequiresConfirmation indicates the workflow needs explicit
	// user approval before execution. Project-level workflows (.koyori-ide/) default
	// to true so that untrusted startup workflows in cloned repositories cannot
	// auto-run shell commands. The flag is forced true for project sources
	// regardless of the file's explicit setting so a malicious repo cannot
	// bypass the confirmation gate.
	RequiresConfirmation bool   `json:"requiresConfirmation,omitempty" yaml:"requiresConfirmation,omitempty"`
	Source               string `json:"source" yaml:"-"` // relative file path
}

// WorkflowService loads workflow definitions from the project's
// .koyori-ide/workflows/ directory (N-19). Supported file extensions: .yml, .yaml,
// .json. Files are sorted alphabetically by filename so the UI shows a
// stable order.
type WorkflowService struct{}

// allowedRunOnEvents is the whitelist of valid runOn.event values (N-55).
// "file-saved" triggers when a matching file is saved (Proposal B).
// "startup" triggers once at IDE startup (Proposal J).
// "workflow-completed" triggers when another workflow finishes (Proposal R).
// Plan 11 Task 11 Step 2: added fileChange/manual/schedule/shortcut.
var allowedRunOnEvents = map[string]bool{
	"file-saved":         true,
	"startup":            true,
	"workflow-completed": true,
	"fileChange":         true,
	"manual":             true,
	"schedule":           true,
	"shortcut":           true,
}

// WorkflowValidationError describes a single validation problem found in
// a workflow definition (N-55). Multiple errors may be returned per workflow.
type WorkflowValidationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// WorkflowValidationResult holds the validation outcome for a single
// workflow (N-55). If Errors is empty, the workflow is valid and can be run.
// The frontend uses this to mark invalid workflows with a red badge and
// prevent execution.
type WorkflowValidationResult struct {
	WorkflowName string                    `json:"workflowName"`
	Valid        bool                      `json:"valid"`
	Errors       []WorkflowValidationError `json:"errors,omitempty"`
}

// ValidateWorkflow runs all validation checks on a single workflow and
// returns a structured result (N-55). This combines:
//   - workflowIsValid (basic name/command presence)
//   - duplicate step name detection
//   - runOn.event whitelist
//   - ValidateDependencies (unknown deps + cycles)
//
// The frontend calls this via the Wails binding to mark invalid workflows
// in the UI before the user tries to run them.
func (s *WorkflowService) ValidateWorkflow(wf *WorkflowDef) WorkflowValidationResult {
	var errs []WorkflowValidationError
	if wf == nil {
		return WorkflowValidationResult{Valid: false, Errors: []WorkflowValidationError{
			{Field: "workflow", Message: "workflow is nil"},
		}}
	}
	// Basic: must have at least one step with name + command.
	hasValidStep := false
	seenNames := make(map[string]bool)
	for i, step := range wf.Steps {
		if step.Name == "" {
			errs = append(errs, WorkflowValidationError{
				Field:   fmt.Sprintf("steps[%d].name", i),
				Message: "step name is empty",
			})
			continue
		}
		if seenNames[step.Name] {
			errs = append(errs, WorkflowValidationError{
				Field:   fmt.Sprintf("steps[%d].name", i),
				Message: fmt.Sprintf("duplicate step name %q", step.Name),
			})
		}
		seenNames[step.Name] = true
		if step.Command == "" {
			errs = append(errs, WorkflowValidationError{
				Field:   fmt.Sprintf("steps[%d].command", i),
				Message: fmt.Sprintf("step %q has empty command", step.Name),
			})
		}
		if step.Name != "" && step.Command != "" {
			hasValidStep = true
		}
	}
	if !hasValidStep {
		errs = append(errs, WorkflowValidationError{
			Field:   "steps",
			Message: "workflow has no valid steps",
		})
	}
	// runOn.event whitelist (N-55).
	if wf.RunOn != nil && wf.RunOn.Event != "" {
		if !allowedRunOnEvents[wf.RunOn.Event] {
			errs = append(errs, WorkflowValidationError{
				Field:   "runOn.event",
				Message: fmt.Sprintf("unknown runOn event %q (allowed: file-saved, startup, workflow-completed, fileChange, manual, schedule, shortcut)", wf.RunOn.Event),
			})
		}
	}
	// Dependency graph: unknown deps + cycles.
	if depErr := s.ValidateDependencies(wf); depErr != nil {
		errs = append(errs, WorkflowValidationError{
			Field:   "dependsOn",
			Message: depErr.Error(),
		})
	}
	// G-SEC-03: Project-level workflows (.koyori-ide/) require explicit user
	// confirmation before execution. This is forced on regardless of the
	// file's explicit setting so a malicious cloned repo cannot bypass the
	// confirmation gate by setting requiresConfirmation: false. Startup
	// workflows in particular must never auto-execute.
	if isProjectSource(wf.Source) {
		wf.RequiresConfirmation = true
	}
	return WorkflowValidationResult{
		WorkflowName: wf.Name,
		Valid:        len(errs) == 0,
		Errors:       errs,
	}
}

// isProjectSource returns true if the workflow was loaded from the project's
// .koyori-ide/workflows directory. Such workflows are untrusted (G-SEC-03) because
// they ship with the repository and may be malicious. The check normalizes
// path separators so it works on all platforms.
func isProjectSource(source string) bool {
	if source == "" {
		return false
	}
	norm := strings.ReplaceAll(source, "\\", "/")
	return strings.HasPrefix(norm, ".koyori-ide/workflows")
}

// ValidateAllWorkflows validates a slice of workflows and returns a result
// per workflow (N-55). Used by the frontend after LoadWorkflows to mark
// invalid workflows in the UI.
func (s *WorkflowService) ValidateAllWorkflows(wfs []WorkflowDef) []WorkflowValidationResult {
	out := make([]WorkflowValidationResult, len(wfs))
	for i := range wfs {
		out[i] = s.ValidateWorkflow(&wfs[i])
	}
	return out
}

// PendingStartupWorkflows returns workflows with runOn.event == "startup"
// that require explicit user confirmation before execution (G-SEC-03). These
// workflows must NOT be auto-executed on project load; the UI should list
// them as "Pending Confirmation" and require the user to click "Run".
func (s *WorkflowService) PendingStartupWorkflows(wfs []WorkflowDef) []WorkflowDef {
	var out []WorkflowDef
	for i := range wfs {
		wf := wfs[i]
		if wf.RunOn != nil && wf.RunOn.Event == "startup" {
			out = append(out, wf)
		}
	}
	return out
}

// NewWorkflowService creates a new WorkflowService.
func NewWorkflowService() *WorkflowService {
	return &WorkflowService{}
}

// workflowFileExtensions are the file extensions recognized as workflow
// definitions, checked in order. .yml and .yaml are parsed as YAML; .json
// is parsed as JSON.
var workflowFileExtensions = []string{".yml", ".yaml", ".json"}

// LoadWorkflows reads all workflow definition files from
// .koyori-ide/workflows/ under the given project root. Returns an empty list
// (not an error) when the directory does not exist, so the frontend can
// always render the Workflows panel. Invalid files are skipped with a
// best-effort approach — a parse error in one file does not prevent the
// rest from loading.
func (s *WorkflowService) LoadWorkflows(projectRoot string) ([]WorkflowDef, error) {
	if projectRoot == "" {
		return nil, fmt.Errorf("projectRoot is required")
	}
	dir := filepath.Join(projectRoot, ".koyori-ide", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []WorkflowDef{}, nil
		}
		return nil, fmt.Errorf("read workflows dir: %w", err)
	}

	// Sort entries alphabetically for a stable display order.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	var out []WorkflowDef
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if !hasWorkflowExt(ext) {
			continue
		}
		fullPath := filepath.Join(dir, entry.Name())
		data, err := readFileLimited(fullPath, maxReadableFileBytes)
		if err != nil {
			continue // skip unreadable files
		}
		wf, err := parseWorkflow(data, ext)
		if err != nil {
			continue // skip unparseable files
		}
		// Derive name from filename if not set.
		if wf.Name == "" {
			wf.Name = strings.TrimSuffix(entry.Name(), ext)
		}
		// Validate: must have at least one step with a name and command.
		if !workflowIsValid(wf) {
			continue
		}
		wf.Source = filepath.Join(".koyori-ide", "workflows", entry.Name())
		// G-SEC-03: all workflows loaded from .koyori-ide/workflows are
		// project-level and therefore untrusted. Mark them as requiring
		// explicit user confirmation so the frontend does not auto-run
		// startup triggers without the user's consent.
		wf.RequiresConfirmation = true
		out = append(out, *wf)
	}
	return out, nil
}

// LoadWorkflow returns a single workflow by name. The name is matched
// against the workflow's Name field (case-sensitive) or, failing that,
// the filename without extension. Returns an error if not found.
func (s *WorkflowService) LoadWorkflow(projectRoot, name string) (*WorkflowDef, error) {
	if projectRoot == "" {
		return nil, fmt.Errorf("projectRoot is required")
	}
	if name == "" {
		return nil, fmt.Errorf("workflow name is required")
	}
	workflows, err := s.LoadWorkflows(projectRoot)
	if err != nil {
		return nil, err
	}
	for i := range workflows {
		wf := &workflows[i]
		if wf.Name == name {
			return wf, nil
		}
		// Also match by source filename (without extension).
		base := filepath.Base(wf.Source)
		stem := strings.TrimSuffix(base, filepath.Ext(base))
		if stem == name {
			return wf, nil
		}
	}
	return nil, fmt.Errorf("workflow %q not found", name)
}

// parseWorkflow parses a workflow definition from raw bytes using the
// appropriate format based on the file extension. .yml/.yaml use YAML;
// .json uses JSON.
func parseWorkflow(data []byte, ext string) (*WorkflowDef, error) {
	var wf WorkflowDef
	switch ext {
	case ".yml", ".yaml":
		if err := yaml.Unmarshal(data, &wf); err != nil {
			return nil, fmt.Errorf("parse yaml: %w", err)
		}
	case ".json":
		if err := json.Unmarshal(data, &wf); err != nil {
			return nil, fmt.Errorf("parse json: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported extension: %s", ext)
	}
	return &wf, nil
}

// workflowIsValid checks that a workflow has at least one step, and each
// step has a non-empty name and command. Steps with empty names or
// commands are silently dropped; the workflow is invalid only if no
// valid steps remain.
func workflowIsValid(wf *WorkflowDef) bool {
	if wf == nil {
		return false
	}
	valid := make([]WorkflowStep, 0, len(wf.Steps))
	for _, s := range wf.Steps {
		if s.Name == "" || s.Command == "" {
			continue
		}
		valid = append(valid, s)
	}
	wf.Steps = valid
	return len(valid) > 0
}

// hasWorkflowExt returns true if ext is one of the recognized workflow
// file extensions.
func hasWorkflowExt(ext string) bool {
	for _, e := range workflowFileExtensions {
		if ext == e {
			return true
		}
	}
	return false
}

// ValidateDependencies checks that every step's dependsOn references an
// existing step name, and that there are no circular dependencies.
// Returns an error describing the first problem found, or nil if the
// dependency graph is valid. This is called by the frontend before
// running a workflow to give the user early feedback.
func (s *WorkflowService) ValidateDependencies(wf *WorkflowDef) error {
	if wf == nil {
		return fmt.Errorf("workflow is nil")
	}
	names := make(map[string]bool, len(wf.Steps))
	for _, step := range wf.Steps {
		names[step.Name] = true
	}
	// Check all dependsOn references exist.
	for _, step := range wf.Steps {
		for _, dep := range step.DependsOn {
			if !names[dep] {
				return fmt.Errorf("step %q depends on unknown step %q", step.Name, dep)
			}
		}
	}
	// Check for cycles via DFS.
	visited := make(map[string]int) // 0=unvisited, 1=in-progress, 2=done
	var visit func(name string) error
	visit = func(name string) error {
		if visited[name] == 1 {
			return fmt.Errorf("circular dependency involving step %q", name)
		}
		if visited[name] == 2 {
			return nil
		}
		visited[name] = 1
		for _, step := range wf.Steps {
			if step.Name == name {
				for _, dep := range step.DependsOn {
					if err := visit(dep); err != nil {
						return err
					}
				}
				break
			}
		}
		visited[name] = 2
		return nil
	}
	for _, step := range wf.Steps {
		if err := visit(step.Name); err != nil {
			return err
		}
	}
	return nil
}

// ComposeStepCommandLine builds a shell-ready command line from a
// WorkflowStep, reusing the same quoting logic as TaskDef.
func (s WorkflowStep) ComposeStepCommandLine() string {
	out := s.Command
	for _, a := range s.Args {
		out += " " + shellQuote(a)
	}
	return out
}

// ---- prompt-4 Task 12: 软件内创建 / 保存 / 删除 / 重命名工作流 ----

// sanitizeWorkflowName validates and cleans a workflow name for use as a
// flat filename stem under .koyori-ide/workflows/. Rejects path traversal and
// empty names (G-SEC-06 / IsRelativePathSafe).
func sanitizeWorkflowName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("workflow name is required")
	}
	// Strip extension if the UI passed one.
	ext := strings.ToLower(filepath.Ext(name))
	if hasWorkflowExt(ext) {
		name = strings.TrimSuffix(name, ext)
	}
	if err := ValidateNameForFlatDir(name); err != nil {
		return "", fmt.Errorf("invalid workflow name: %w", err)
	}
	return name, nil
}

// workflowPath returns the absolute path for a workflow YAML file, validated
// to stay within projectRoot/.koyori-ide/workflows/.
func workflowPath(projectRoot, name string) (string, error) {
	safe, err := sanitizeWorkflowName(name)
	if err != nil {
		return "", err
	}
	if projectRoot == "" {
		return "", fmt.Errorf("projectRoot is required")
	}
	dir := filepath.Join(projectRoot, ".koyori-ide", "workflows")
	full := filepath.Join(dir, safe+".yml")
	// Ensure the resolved path stays under the workflows directory.
	if _, err := ValidatePathWithinRoot(dir, full); err != nil {
		return "", fmt.Errorf("workflow path outside workflows dir: %w", err)
	}
	return full, nil
}

// atomicWriteYAML marshals data to YAML and writes it atomically (G-SEC-09).
func atomicWriteYAML(path string, data interface{}, perm os.FileMode) error {
	if perm == 0 {
		perm = 0600
	}
	raw, err := yaml.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal yaml: %w", err)
	}
	return atomicWriteFile(path, raw, perm)
}

// workflowFileDTO is the on-disk YAML shape. Source is omitted (yaml:"-").
// We re-encode from WorkflowDef while forcing RequiresConfirmation for
// project-level files (G-SEC-03).
type workflowFileDTO struct {
	Name                 string           `yaml:"name"`
	Description          string           `yaml:"description,omitempty"`
	Steps                []WorkflowStep   `yaml:"steps"`
	Watch                []string         `yaml:"watch,omitempty"`
	RunOn                *WorkflowTrigger `yaml:"runOn,omitempty"`
	RequiresConfirmation bool             `yaml:"requiresConfirmation,omitempty"`
}

func toWorkflowFileDTO(wf *WorkflowDef) workflowFileDTO {
	return workflowFileDTO{
		Name:                 wf.Name,
		Description:          wf.Description,
		Steps:                wf.Steps,
		Watch:                wf.Watch,
		RunOn:                wf.RunOn,
		RequiresConfirmation: true, // G-SEC-03: always true for project workflows
	}
}

// CreateWorkflow writes a new workflow file at .koyori-ide/workflows/<name>.yml.
// Fails if a file with the same name already exists. New workflows default
// to RequiresConfirmation = true (G-SEC-03).
func (s *WorkflowService) CreateWorkflow(projectRoot, name string, def *WorkflowDef) error {
	if def == nil {
		return fmt.Errorf("workflow definition is required")
	}
	safe, err := sanitizeWorkflowName(name)
	if err != nil {
		return err
	}
	// Prefer the name argument; fall back to def.Name.
	if def.Name == "" {
		def.Name = safe
	} else {
		// Keep def.Name sanitized as well.
		if n, nerr := sanitizeWorkflowName(def.Name); nerr == nil {
			def.Name = n
		}
	}
	def.RequiresConfirmation = true
	// Validate before write.
	result := s.ValidateWorkflow(def)
	if !result.Valid {
		msgs := make([]string, 0, len(result.Errors))
		for _, e := range result.Errors {
			msgs = append(msgs, e.Field+": "+e.Message)
		}
		return fmt.Errorf("invalid workflow: %s", strings.Join(msgs, "; "))
	}
	path, err := workflowPath(projectRoot, safe)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("workflow %q already exists", safe)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat workflow: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create workflows dir: %w", err)
	}
	dto := toWorkflowFileDTO(def)
	dto.Name = safe
	if err := atomicWriteYAML(path, dto, 0600); err != nil {
		return fmt.Errorf("write workflow: %w", err)
	}
	return nil
}

// SaveWorkflow overwrites an existing workflow (or creates it if missing).
// Always forces RequiresConfirmation = true for project sources.
func (s *WorkflowService) SaveWorkflow(projectRoot, name string, def *WorkflowDef) error {
	if def == nil {
		return fmt.Errorf("workflow definition is required")
	}
	safe, err := sanitizeWorkflowName(name)
	if err != nil {
		return err
	}
	if def.Name == "" {
		def.Name = safe
	}
	def.RequiresConfirmation = true
	result := s.ValidateWorkflow(def)
	if !result.Valid {
		msgs := make([]string, 0, len(result.Errors))
		for _, e := range result.Errors {
			msgs = append(msgs, e.Field+": "+e.Message)
		}
		return fmt.Errorf("invalid workflow: %s", strings.Join(msgs, "; "))
	}
	path, err := workflowPath(projectRoot, safe)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create workflows dir: %w", err)
	}
	dto := toWorkflowFileDTO(def)
	dto.Name = safe
	if err := atomicWriteYAML(path, dto, 0600); err != nil {
		return fmt.Errorf("write workflow: %w", err)
	}
	return nil
}

// DeleteWorkflow removes .koyori-ide/workflows/<name>.yml (and .yaml/.json variants).
func (s *WorkflowService) DeleteWorkflow(projectRoot, name string) error {
	safe, err := sanitizeWorkflowName(name)
	if err != nil {
		return err
	}
	if projectRoot == "" {
		return fmt.Errorf("projectRoot is required")
	}
	dir := filepath.Join(projectRoot, ".koyori-ide", "workflows")
	var lastErr error
	found := false
	for _, ext := range workflowFileExtensions {
		full := filepath.Join(dir, safe+ext)
		if _, err := ValidatePathWithinRoot(dir, full); err != nil {
			return fmt.Errorf("workflow path outside workflows dir: %w", err)
		}
		if _, err := os.Stat(full); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			lastErr = err
			continue
		}
		if err := os.Remove(full); err != nil {
			return fmt.Errorf("delete workflow: %w", err)
		}
		found = true
	}
	if !found {
		if lastErr != nil {
			return lastErr
		}
		return fmt.Errorf("workflow %q not found", safe)
	}
	return nil
}

// RenameWorkflow renames a workflow file from oldName to newName.
// Fails if newName already exists or oldName is missing.
func (s *WorkflowService) RenameWorkflow(projectRoot, oldName, newName string) error {
	oldSafe, err := sanitizeWorkflowName(oldName)
	if err != nil {
		return fmt.Errorf("old name: %w", err)
	}
	newSafe, err := sanitizeWorkflowName(newName)
	if err != nil {
		return fmt.Errorf("new name: %w", err)
	}
	if oldSafe == newSafe {
		return nil
	}
	// Load existing definition so we can rewrite Name field.
	wf, err := s.LoadWorkflow(projectRoot, oldSafe)
	if err != nil {
		return err
	}
	wf.Name = newSafe
	if err := s.CreateWorkflow(projectRoot, newSafe, wf); err != nil {
		return err
	}
	if err := s.DeleteWorkflow(projectRoot, oldSafe); err != nil {
		// Best-effort rollback of the new file if delete fails.
		_ = s.DeleteWorkflow(projectRoot, newSafe)
		return fmt.Errorf("delete old workflow after rename: %w", err)
	}
	return nil
}
