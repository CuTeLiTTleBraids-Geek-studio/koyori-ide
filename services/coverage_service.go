package services

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxIstanbulCoverageBytes int64 = 16 << 20

const (
	CoverageStatusCovered   = "covered"
	CoverageStatusUncovered = "uncovered"
	CoverageStatusPartial   = "partial"
)

// CoverageHit is a simplified per-line coverage flag for gutter UI (prompt-9/10/11).
// File is always stored as a cleaned slash-normalized path (may still be package-relative).
type CoverageHit struct {
	File         string `json:"file"`
	Line         int    `json:"line"`
	Covered      bool   `json:"covered"`
	Status       string `json:"status,omitempty"`
	CoveredCount int    `json:"coveredCount,omitempty"`
	TotalCount   int    `json:"totalCount,omitempty"`
}

// CoverageMetric is the common Istanbul summary shape for one metric kind.
type CoverageMetric struct {
	Total   int     `json:"total"`
	Covered int     `json:"covered"`
	Skipped int     `json:"skipped"`
	Pct     float64 `json:"pct"`
}

// CoverageFile contains aggregate metrics and optional source-line hits.
// json-summary reports only aggregates; coverage-final also populates Hits.
type CoverageFile struct {
	File       string         `json:"file"`
	Statements CoverageMetric `json:"statements"`
	Branches   CoverageMetric `json:"branches"`
	Functions  CoverageMetric `json:"functions"`
	Lines      CoverageMetric `json:"lines"`
	Hits       []CoverageHit  `json:"hits"`
}

// CoverageReport is the normalized representation of either Istanbul JSON format.
type CoverageReport struct {
	Format     string         `json:"format"`
	Files      []CoverageFile `json:"files"`
	Statements CoverageMetric `json:"statements"`
	Branches   CoverageMetric `json:"branches"`
	Functions  CoverageMetric `json:"functions"`
	Lines      CoverageMetric `json:"lines"`
}

// CoverageCommand exposes the exact argv used for a Vitest coverage run.
type CoverageCommand struct {
	Executable     string   `json:"executable"`
	Args           []string `json:"args"`
	Dir            string   `json:"dir"`
	PackageManager string   `json:"packageManager"`
}

// VitestCoverageResult combines process output with the parsed report.
type VitestCoverageResult struct {
	Success      bool            `json:"success"`
	Output       string          `json:"output"`
	Report       CoverageReport  `json:"report"`
	Command      CoverageCommand `json:"command"`
	NotInstalled bool            `json:"notInstalled"`
	TimedOut     bool            `json:"timedOut"`
	Cancelled    bool            `json:"cancelled"`
	Duration     int64           `json:"durationMs"`
}

// NormalizeCoveragePath cleans and slash-normalizes a coverprofile path so
// same-basename files under different directories do not collide (prompt-11 11-B).
func NormalizeCoveragePath(p string) string {
	if p == "" {
		return ""
	}
	// Windows drive paths: keep as-is after Clean; always use forward slashes.
	p = filepath.Clean(p)
	p = strings.ReplaceAll(p, "\\", "/")
	// strip redundant ./ prefix
	p = strings.TrimPrefix(p, "./")
	return p
}

// CoveragePathsMatch reports whether a cover hit path refers to the same file
// as editorPath. Never matches on basename alone when either side has directories
// (prompt-11 11-B — avoid cross-package gutter bleed).
func CoveragePathsMatch(hitPath, editorPath string) bool {
	h := NormalizeCoveragePath(hitPath)
	e := NormalizeCoveragePath(editorPath)
	if h == "" || e == "" {
		return false
	}
	if strings.EqualFold(h, e) {
		return true
	}
	hParts := strings.Split(h, "/")
	eParts := strings.Split(e, "/")
	// Basename-only paths only match other basename-only paths.
	if len(hParts) == 1 || len(eParts) == 1 {
		return len(hParts) == 1 && len(eParts) == 1 && strings.EqualFold(h, e)
	}
	// Prefer full relative suffix (pkg/a/foo.go vs /abs/pkg/a/foo.go).
	hl, el := strings.ToLower(h), strings.ToLower(e)
	if strings.HasSuffix(el, "/"+hl) || strings.HasSuffix(hl, "/"+el) {
		return true
	}
	// Require last two path segments (dir + file) to match.
	return strings.EqualFold(hParts[len(hParts)-1], eParts[len(eParts)-1]) &&
		strings.EqualFold(hParts[len(hParts)-2], eParts[len(eParts)-2])
}

// CoverageRunResult is returned after go test -coverprofile (prompt-10 10-H).
type CoverageRunResult struct {
	Success  bool          `json:"success"`
	Output   string        `json:"output"`
	Hits     []CoverageHit `json:"hits"`
	Profile  string        `json:"profile"`
	Duration int64         `json:"durationMs"`
}

// CoverageService parses go cover profiles and can run coverage for a package.
type CoverageService struct {
	mu                          sync.RWMutex
	workspaceRoot               string
	workspaceContext            *WorkspaceContext
	lookPath                    func(string) (string, error)
	runVitest                   func(context.Context, CoverageCommand) ([]byte, error)
	beforeWorkspaceCommandStart func()
}

// NewCoverageService creates the coverage helper.
func NewCoverageService() *CoverageService {
	return newCoverageService(nil)
}

// NewCoverageServiceWithWorkspaceContext creates the renderer-facing service.
// Command roots are resolved from the shared context at call time.
func NewCoverageServiceWithWorkspaceContext(workspaceContext *WorkspaceContext) *CoverageService {
	return newCoverageService(workspaceContext)
}

func newCoverageService(workspaceContext *WorkspaceContext) *CoverageService {
	return &CoverageService{
		workspaceContext: workspaceContext,
		lookPath:         exec.LookPath,
		runVitest:        executeVitestCoverageCommand,
	}
}

// setWorkspaceRoot sets the default package directory for RunPackageCoverage.
//
//wails:ignore
func (c *CoverageService) setWorkspaceRoot(root string) error {
	if strings.TrimSpace(root) == "" {
		c.mu.Lock()
		c.workspaceRoot = ""
		c.mu.Unlock()
		return nil
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve coverage workspace: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("open coverage workspace: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("coverage workspace is not a directory: %s", root)
	}
	c.mu.Lock()
	c.workspaceRoot = filepath.Clean(abs)
	c.mu.Unlock()
	return nil
}

type istanbulPosition struct {
	Line   int `json:"line"`
	Column int `json:"column"`
}

type istanbulLocation struct {
	Start istanbulPosition `json:"start"`
	End   istanbulPosition `json:"end"`
}

type istanbulFunction struct {
	Decl istanbulLocation `json:"decl"`
	Loc  istanbulLocation `json:"loc"`
}

type istanbulBranch struct {
	Loc       istanbulLocation   `json:"loc"`
	Locations []istanbulLocation `json:"locations"`
}

type istanbulFinalFile struct {
	Path         string                      `json:"path"`
	StatementMap map[string]istanbulLocation `json:"statementMap"`
	FunctionMap  map[string]istanbulFunction `json:"fnMap"`
	BranchMap    map[string]istanbulBranch   `json:"branchMap"`
	Statements   map[string]int              `json:"s"`
	Functions    map[string]int              `json:"f"`
	Branches     map[string][]int            `json:"b"`
}

type istanbulSummaryMetric struct {
	Total   int `json:"total"`
	Covered int `json:"covered"`
	Skipped int `json:"skipped"`
}

type istanbulSummaryFile struct {
	Statements istanbulSummaryMetric `json:"statements"`
	Branches   istanbulSummaryMetric `json:"branches"`
	Functions  istanbulSummaryMetric `json:"functions"`
	Lines      istanbulSummaryMetric `json:"lines"`
}

type lineCoverageAccumulator struct {
	covered int
	total   int
}

func metric(total, covered, skipped int) CoverageMetric {
	pct := 0.0
	if total > 0 {
		pct = math.Round((float64(covered)*100/float64(total))*100) / 100
	}
	return CoverageMetric{Total: total, Covered: covered, Skipped: skipped, Pct: pct}
}

func addMetric(dst *CoverageMetric, src CoverageMetric) {
	dst.Total += src.Total
	dst.Covered += src.Covered
	dst.Skipped += src.Skipped
	dst.Pct = metric(dst.Total, dst.Covered, dst.Skipped).Pct
}

func addFileMetrics(report *CoverageReport, file CoverageFile) {
	addMetric(&report.Statements, file.Statements)
	addMetric(&report.Branches, file.Branches)
	addMetric(&report.Functions, file.Functions)
	addMetric(&report.Lines, file.Lines)
}

func normalizeWorkspaceCoveragePath(root, reportedPath string) (string, bool) {
	reportedPath = strings.TrimSpace(reportedPath)
	if reportedPath == "" || strings.ContainsRune(reportedPath, '\x00') {
		return "", false
	}
	path := filepath.FromSlash(strings.ReplaceAll(reportedPath, "\\", "/"))
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	abs, err := ValidatePathWithinRoot(root, path)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return NormalizeCoveragePath(rel), true
}

func (c *CoverageService) coverageWorkspace() (string, error) {
	lease, err := c.acquireWorkspaceLease()
	if err != nil {
		return "", err
	}
	return lease.root, nil
}

func (c *CoverageService) acquireWorkspaceLease() (workspaceLease, error) {
	c.mu.RLock()
	root := c.workspaceRoot
	c.mu.RUnlock()
	return acquireWorkspaceLease(c.workspaceContext, root, 0)
}

func readBoundedCoverageJSON(root, reportPath string) ([]byte, error) {
	if !filepath.IsAbs(reportPath) {
		reportPath = filepath.Join(root, reportPath)
	}
	abs, err := ValidatePathWithinRoot(root, reportPath)
	if err != nil {
		return nil, fmt.Errorf("coverage report path is outside the workspace: %w", err)
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, fmt.Errorf("open coverage report: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat coverage report: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("coverage report must be a regular file")
	}
	if info.Size() > maxIstanbulCoverageBytes {
		return nil, fmt.Errorf("coverage report is too large: maximum is %d bytes", maxIstanbulCoverageBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxIstanbulCoverageBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read coverage report: %w", err)
	}
	if int64(len(data)) > maxIstanbulCoverageBytes {
		return nil, fmt.Errorf("coverage report is too large: maximum is %d bytes", maxIstanbulCoverageBytes)
	}
	return data, nil
}

// ParseIstanbulCoverage reads coverage-final.json or coverage-summary.json.
// The report itself must be inside the configured workspace. File entries that
// resolve outside it, including symlink escapes, are ignored.
func (c *CoverageService) ParseIstanbulCoverage(reportPath string) (CoverageReport, error) {
	root, err := c.coverageWorkspace()
	if err != nil {
		return CoverageReport{}, err
	}
	data, err := readBoundedCoverageJSON(root, reportPath)
	if err != nil {
		return CoverageReport{}, err
	}
	var records map[string]json.RawMessage
	if err := json.Unmarshal(data, &records); err != nil {
		return CoverageReport{}, fmt.Errorf("parse Istanbul coverage JSON: %w", err)
	}

	format := "coverage-final"
	if strings.Contains(strings.ToLower(filepath.Base(reportPath)), "summary") {
		format = "json-summary"
	}
	for key, raw := range records {
		if key == "total" {
			continue
		}
		if strings.TrimSpace(string(raw)) == "null" {
			return CoverageReport{}, fmt.Errorf("coverage record %q is null", key)
		}
		var probe struct {
			StatementMap json.RawMessage `json:"statementMap"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil {
			return CoverageReport{}, fmt.Errorf("parse coverage record %q: %w", key, err)
		}
		if probe.StatementMap != nil {
			format = "coverage-final"
			break
		}
	}
	if format == "coverage-final" {
		return parseIstanbulFinalRecords(root, records)
	}
	return parseIstanbulSummaryRecords(root, records)
}

func parseIstanbulSummaryRecords(root string, records map[string]json.RawMessage) (CoverageReport, error) {
	report := CoverageReport{Format: "json-summary", Files: []CoverageFile{}}
	for reportedPath, raw := range records {
		if reportedPath == "total" {
			continue
		}
		filePath, ok := normalizeWorkspaceCoveragePath(root, reportedPath)
		if !ok {
			continue
		}
		var source istanbulSummaryFile
		if err := json.Unmarshal(raw, &source); err != nil {
			return CoverageReport{}, fmt.Errorf("parse coverage summary record %q: %w", reportedPath, err)
		}
		file := CoverageFile{
			File:       filePath,
			Statements: metric(source.Statements.Total, source.Statements.Covered, source.Statements.Skipped),
			Branches:   metric(source.Branches.Total, source.Branches.Covered, source.Branches.Skipped),
			Functions:  metric(source.Functions.Total, source.Functions.Covered, source.Functions.Skipped),
			Lines:      metric(source.Lines.Total, source.Lines.Covered, source.Lines.Skipped),
			Hits:       []CoverageHit{},
		}
		report.Files = append(report.Files, file)
		addFileMetrics(&report, file)
	}
	sort.Slice(report.Files, func(i, j int) bool { return report.Files[i].File < report.Files[j].File })
	return report, nil
}

func parseIstanbulFinalRecords(root string, records map[string]json.RawMessage) (CoverageReport, error) {
	report := CoverageReport{Format: "coverage-final", Files: []CoverageFile{}}
	for recordPath, raw := range records {
		if recordPath == "total" {
			continue
		}
		var source istanbulFinalFile
		if err := json.Unmarshal(raw, &source); err != nil {
			return CoverageReport{}, fmt.Errorf("parse coverage-final record %q: %w", recordPath, err)
		}
		reportedPath := source.Path
		if reportedPath == "" {
			reportedPath = recordPath
		}
		filePath, ok := normalizeWorkspaceCoveragePath(root, reportedPath)
		if !ok {
			continue
		}
		file := normalizeIstanbulFinalFile(filePath, source)
		report.Files = append(report.Files, file)
		addFileMetrics(&report, file)
	}
	sort.Slice(report.Files, func(i, j int) bool { return report.Files[i].File < report.Files[j].File })
	return report, nil
}

func normalizeIstanbulFinalFile(filePath string, source istanbulFinalFile) CoverageFile {
	lineUnits := make(map[int]*lineCoverageAccumulator)
	lineStatementCovered := make(map[int]bool)

	statementCovered := 0
	for id, loc := range source.StatementMap {
		count := source.Statements[id]
		if loc.Start.Line > 0 {
			if _, exists := lineStatementCovered[loc.Start.Line]; !exists {
				lineStatementCovered[loc.Start.Line] = false
			}
			if count > 0 {
				lineStatementCovered[loc.Start.Line] = true
			}
		}
		if count > 0 {
			statementCovered++
		}
		addLineCoverageUnit(lineUnits, loc.Start.Line, count > 0)
	}

	functionCovered := 0
	for id, fn := range source.FunctionMap {
		count := source.Functions[id]
		if count > 0 {
			functionCovered++
		}
		line := fn.Decl.Start.Line
		if line <= 0 {
			line = fn.Loc.Start.Line
		}
		addLineCoverageUnit(lineUnits, line, count > 0)
	}

	branchTotal, branchCovered := 0, 0
	for id, branch := range source.BranchMap {
		counts := source.Branches[id]
		total := len(branch.Locations)
		if len(counts) > total {
			total = len(counts)
		}
		branchTotal += total
		for index := 0; index < total; index++ {
			covered := index < len(counts) && counts[index] > 0
			if covered {
				branchCovered++
			}
			line := branch.Loc.Start.Line
			if index < len(branch.Locations) && branch.Locations[index].Start.Line > 0 {
				line = branch.Locations[index].Start.Line
			}
			addLineCoverageUnit(lineUnits, line, covered)
		}
	}

	lineCovered := 0
	for _, covered := range lineStatementCovered {
		if covered {
			lineCovered++
		}
	}
	hits := make([]CoverageHit, 0, len(lineUnits))
	for line, units := range lineUnits {
		status := CoverageStatusPartial
		switch {
		case units.covered == 0:
			status = CoverageStatusUncovered
		case units.covered == units.total:
			status = CoverageStatusCovered
		}
		hits = append(hits, CoverageHit{
			File:         filePath,
			Line:         line,
			Covered:      units.covered > 0,
			Status:       status,
			CoveredCount: units.covered,
			TotalCount:   units.total,
		})
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Line < hits[j].Line })
	return CoverageFile{
		File:       filePath,
		Statements: metric(len(source.StatementMap), statementCovered, 0),
		Branches:   metric(branchTotal, branchCovered, 0),
		Functions:  metric(len(source.FunctionMap), functionCovered, 0),
		Lines:      metric(len(lineStatementCovered), lineCovered, 0),
		Hits:       hits,
	}
}

func addLineCoverageUnit(lines map[int]*lineCoverageAccumulator, line int, covered bool) {
	if line <= 0 {
		return
	}
	entry := lines[line]
	if entry == nil {
		entry = &lineCoverageAccumulator{}
		lines[line] = entry
	}
	entry.total++
	if covered {
		entry.covered++
	}
}

func detectCoveragePackageManager(root string) (string, error) {
	packagePath := filepath.Join(root, "package.json")
	data, err := os.ReadFile(packagePath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", errors.New("package.json was not found in the coverage workspace")
		}
		return "", fmt.Errorf("read package.json: %w", err)
	}
	var pkg struct {
		PackageManager string `json:"packageManager"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return "", fmt.Errorf("parse package.json: %w", err)
	}
	if value := strings.TrimSpace(pkg.PackageManager); value != "" {
		name := value
		if at := strings.IndexByte(name, '@'); at >= 0 {
			name = name[:at]
		}
		switch name {
		case "npm", "pnpm", "yarn", "bun":
			return name, nil
		}
	}
	for _, candidate := range []struct {
		manager string
		files   []string
	}{
		{manager: "pnpm", files: []string{"pnpm-lock.yaml"}},
		{manager: "yarn", files: []string{"yarn.lock"}},
		{manager: "bun", files: []string{"bun.lock", "bun.lockb"}},
		{manager: "npm", files: []string{"package-lock.json", "npm-shrinkwrap.json"}},
	} {
		for _, name := range candidate.files {
			if info, statErr := os.Stat(filepath.Join(root, name)); statErr == nil && info.Mode().IsRegular() {
				return candidate.manager, nil
			}
		}
	}
	return "npm", nil
}

// BuildVitestCoverageCommand returns a fixed executable and argv. No shell
// command string or project-controlled script content is evaluated.
func BuildVitestCoverageCommand(workspaceRoot string) (CoverageCommand, error) {
	abs, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return CoverageCommand{}, fmt.Errorf("resolve coverage workspace: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return CoverageCommand{}, fmt.Errorf("open coverage workspace: %w", err)
	}
	if !info.IsDir() {
		return CoverageCommand{}, errors.New("coverage workspace is not a directory")
	}
	manager, err := detectCoveragePackageManager(abs)
	if err != nil {
		return CoverageCommand{}, err
	}
	vitestArgs := []string{
		"vitest", "run", "--coverage", "--coverage.reporter=json", "--coverage.reportsDirectory=coverage",
	}
	prefix := []string{"exec"}
	if manager == "npm" {
		prefix = []string{"exec", "--"}
	} else if manager == "bun" {
		prefix = []string{"x"}
	}
	return CoverageCommand{
		Executable:     manager,
		Args:           append(prefix, vitestArgs...),
		Dir:            filepath.Clean(abs),
		PackageManager: manager,
	}, nil
}

// BuildVitestCoverageCommand exposes the validated command specification to the UI.
func (c *CoverageService) BuildVitestCoverageCommand(workspaceRoot string) (CoverageCommand, error) {
	root, err := c.workspaceForVitest(workspaceRoot)
	if err != nil {
		return CoverageCommand{}, err
	}
	return BuildVitestCoverageCommand(root)
}

func (c *CoverageService) workspaceForVitest(requested string) (string, error) {
	root, _, err := c.workspaceForVitestLease(requested)
	return root, err
}

func (c *CoverageService) workspaceForVitestLease(requested string) (string, workspaceLease, error) {
	requested = strings.TrimSpace(requested)
	lease, err := c.acquireWorkspaceLease()
	if err != nil {
		return "", workspaceLease{}, err
	}
	configured := lease.root
	if requested == "" {
		return configured, lease, nil
	}
	validated, err := ValidatePathWithinRoot(configured, requested)
	if err != nil {
		return "", workspaceLease{}, fmt.Errorf("Vitest workspace is outside the active workspace: %w", err)
	}
	requested = validated
	abs, err := filepath.Abs(requested)
	if err != nil {
		return "", workspaceLease{}, fmt.Errorf("resolve Vitest workspace: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", workspaceLease{}, fmt.Errorf("open Vitest workspace: %w", err)
	}
	if !info.IsDir() {
		return "", workspaceLease{}, errors.New("Vitest workspace is not a directory")
	}
	return filepath.Clean(abs), lease, nil
}

func coverageProviderInstallHint(manager string) string {
	switch manager {
	case "pnpm":
		return "pnpm add -D @vitest/coverage-v8"
	case "yarn":
		return "yarn add -D @vitest/coverage-v8"
	case "bun":
		return "bun add -d @vitest/coverage-v8"
	default:
		return "npm install -D @vitest/coverage-v8"
	}
}

func appendCoverageHint(output, hint string) string {
	output = strings.TrimSpace(output)
	if output != "" {
		output += "\n\n"
	}
	return output + hint
}

// RunVitestCoverage runs Vitest with a cancellable, bounded, no-shell argv
// invocation and parses the generated coverage-final.json.
func (c *CoverageService) RunVitestCoverage(ctx context.Context, workspaceRoot string, timeoutSeconds int) (VitestCoverageResult, error) {
	root, lease, err := c.workspaceForVitestLease(workspaceRoot)
	if err != nil {
		return VitestCoverageResult{}, err
	}
	spec, err := BuildVitestCoverageCommand(root)
	if err != nil {
		return VitestCoverageResult{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if timeoutSeconds <= 0 {
		timeoutSeconds = 300
	}
	if timeoutSeconds > 1800 {
		timeoutSeconds = 1800
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	lookPath := c.lookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	resolved, err := lookPath(spec.Executable)
	if err != nil {
		hint := fmt.Sprintf("%s was not found; install %s, then retry Vitest coverage.", spec.Executable, spec.Executable)
		return VitestCoverageResult{Command: spec, NotInstalled: true, Output: hint}, nil
	}
	spec.Executable = resolved
	reportPath := filepath.Join(root, "coverage", "coverage-final.json")
	previousReport, hadPreviousReport := coverageReportFileStamp(reportPath)

	runner := c.runVitest
	if runner == nil {
		runner = executeVitestCoverageCommand
	}
	if c.beforeWorkspaceCommandStart != nil {
		c.beforeWorkspaceCommandStart()
	}
	if err := lease.validateCurrent(); err != nil {
		return VitestCoverageResult{}, err
	}
	start := time.Now()
	output, runErr := runner(runCtx, spec)
	result := VitestCoverageResult{
		Command:  spec,
		Output:   string(output),
		Duration: time.Since(start).Milliseconds(),
	}
	if runErr != nil {
		result.TimedOut = errors.Is(runCtx.Err(), context.DeadlineExceeded)
		result.Cancelled = errors.Is(runCtx.Err(), context.Canceled)
		reason := "Vitest coverage failed."
		if result.TimedOut {
			reason = "Vitest coverage timed out and its process tree was terminated."
		} else if result.Cancelled {
			reason = "Vitest coverage was cancelled and its process tree was terminated."
		}
		result.Output = appendCoverageHint(result.Output, reason+" If the coverage provider is missing, run: "+coverageProviderInstallHint(spec.PackageManager))
		return result, nil
	}

	parseService := NewCoverageService()
	if err := parseService.setWorkspaceRoot(root); err != nil {
		return VitestCoverageResult{}, err
	}
	if currentReport, exists := coverageReportFileStamp(reportPath); hadPreviousReport && exists && currentReport == previousReport {
		result.Output = appendCoverageHint(result.Output, "Vitest completed but coverage/coverage-final.json was not refreshed. Check the reporter configuration and retry.")
		return result, nil
	}
	report, parseErr := parseService.ParseIstanbulCoverage(reportPath)
	if parseErr != nil {
		result.Output = appendCoverageHint(result.Output, "Vitest completed but a valid coverage/coverage-final.json was not produced: "+parseErr.Error()+". Install the provider with: "+coverageProviderInstallHint(spec.PackageManager))
		return result, nil
	}
	result.Success = true
	result.Report = report
	return result, nil
}

type coverageReportStamp struct {
	size    int64
	modTime int64
}

func coverageReportFileStamp(path string) (coverageReportStamp, bool) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return coverageReportStamp{}, false
	}
	return coverageReportStamp{size: info.Size(), modTime: info.ModTime().UnixNano()}, true
}

const maxCoverageCommandOutputBytes = 4 << 20

type limitedCoverageOutput struct {
	data      []byte
	truncated bool
}

func (w *limitedCoverageOutput) Write(p []byte) (int, error) {
	remaining := maxCoverageCommandOutputBytes - len(w.data)
	if remaining > 0 {
		if remaining > len(p) {
			remaining = len(p)
		}
		w.data = append(w.data, p[:remaining]...)
	}
	if remaining < len(p) {
		w.truncated = true
	}
	return len(p), nil
}

func (w *limitedCoverageOutput) Bytes() []byte {
	if !w.truncated {
		return w.data
	}
	return append(w.data, []byte("\n[coverage output truncated]\n")...)
}

func executeVitestCoverageCommand(ctx context.Context, spec CoverageCommand) ([]byte, error) {
	cmd := commandContext(ctx, spec.Executable, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = os.Environ()
	configureCoverageProcessTree(cmd)
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return terminateCoverageProcessTree(cmd.Process)
	}
	cmd.WaitDelay = 2 * time.Second
	var output limitedCoverageOutput
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	return output.Bytes(), err
}

// ParseCoverProfile reads a go cover profile and returns per-line hits.
// Format: file:startLine.startCol,endLine.endCol numStmt count
func (c *CoverageService) ParseCoverProfile(profilePath string) ([]CoverageHit, error) {
	f, err := os.Open(profilePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []CoverageHit
	sc := bufio.NewScanner(f)
	// skip mode line
	if sc.Scan() {
		_ = sc.Text() // 跳过 mode 行
	}
	for sc.Scan() {
		line := sc.Text()
		parts := strings.Fields(line)
		if len(parts) < 3 {
			continue
		}
		count, _ := strconv.Atoi(parts[2])
		loc := parts[0]
		colon := strings.LastIndex(loc, ":")
		if colon < 0 {
			continue
		}
		file := loc[:colon]
		rangePart := loc[colon+1:]
		comma := strings.Index(rangePart, ",")
		if comma < 0 {
			continue
		}
		start := rangePart[:comma]
		end := rangePart[comma+1:]
		dot := strings.Index(start, ".")
		if dot < 0 {
			continue
		}
		startLine, _ := strconv.Atoi(start[:dot])
		endDot := strings.Index(end, ".")
		endLine := startLine
		if endDot >= 0 {
			endLine, _ = strconv.Atoi(end[:endDot])
		}
		covered := count > 0
		normFile := NormalizeCoveragePath(file)
		for ln := startLine; ln <= endLine; ln++ {
			out = append(out, CoverageHit{
				File:    normFile,
				Line:    ln,
				Covered: covered,
			})
		}
	}
	if err := sc.Err(); err != nil {
		return out, fmt.Errorf("scan cover profile: %w", err)
	}
	return out, nil
}

func createCoverageProfile() (*os.File, string, error) {
	tmp, err := os.CreateTemp("", "koyori-cover-*.out")
	if err != nil {
		return nil, "", err
	}
	path := tmp.Name()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(path)
		return nil, "", err
	}
	return tmp, path, nil
}

// RunPackageCoverage runs `go test -coverprofile=<tmp> .` in packageDir and parses hits.
func (c *CoverageService) RunPackageCoverage(packageDir string) (CoverageRunResult, error) {
	lease, err := c.acquireWorkspaceLease()
	if err != nil {
		return CoverageRunResult{}, err
	}
	root := lease.root
	dir := packageDir
	if dir == "" {
		dir = root
	}
	resolvedDir, err := lease.resolve(dir)
	if err != nil {
		return CoverageRunResult{}, err
	}
	dir = resolvedDir
	goBin, err := exec.LookPath("go")
	if err != nil {
		return CoverageRunResult{Success: false, Output: "go not found"}, nil
	}
	tmp, profile, err := createCoverageProfile()
	if err != nil {
		return CoverageRunResult{}, err
	}
	defer os.Remove(profile)
	if err := tmp.Close(); err != nil {
		return CoverageRunResult{}, fmt.Errorf("close coverage profile: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := commandContext(ctx, goBin, "test", "-count=1", "-coverprofile="+profile, ".")
	cmd.Dir = dir
	if c.beforeWorkspaceCommandStart != nil {
		c.beforeWorkspaceCommandStart()
	}
	if err := lease.validateCurrent(); err != nil {
		return CoverageRunResult{}, err
	}
	start := time.Now()
	out, runErr := cmd.CombinedOutput()
	hits, perr := c.ParseCoverProfile(profile)
	if perr != nil {
		hits = nil
	}
	profileData, readErr := os.ReadFile(profile)
	if readErr != nil {
		profileData = nil
	}
	return CoverageRunResult{
		Success:  runErr == nil,
		Output:   string(out),
		Hits:     hits,
		Profile:  string(profileData),
		Duration: time.Since(start).Milliseconds(),
	}, nil
}
