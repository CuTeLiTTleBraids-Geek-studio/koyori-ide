package services

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// SearchMatch describes a single match within a file.
type SearchMatch struct {
	Line    int    `json:"line"`
	Column  int    `json:"column"`
	Preview string `json:"preview"`
}

// SearchResult groups all matches in a single file.
type SearchResult struct {
	Path    string        `json:"path"`
	Matches []SearchMatch `json:"matches"`
}

// SearchService exposes file-content search to the frontend.
// N-67: when workspaceRoot is set via SetWorkspaceRoot, all search/replace
// path arguments are validated to be within the workspace. This prevents
// the frontend from searching or replacing in files outside the open project.
type SearchService struct {
	mu               sync.RWMutex
	workspaceRoot    string
	workspaceRoots   []string
	enforceWorkspace bool
	workspaceContext *WorkspaceContext
	walkDir          func(string, fs.WalkDirFunc) error
}

func NewSearchService() *SearchService {
	return &SearchService{enforceWorkspace: true, walkDir: filepath.WalkDir}
}

// setWorkspaceContext injects the shared workspace identity used by renderer
// batch mutations. The context resolves the root at call time.
//
//wails:ignore
func (s *SearchService) setWorkspaceContext(ctx *WorkspaceContext) {
	s.mu.Lock()
	s.workspaceContext = ctx
	s.mu.Unlock()
}

// setWorkspaceRoot sets the directory within which search and replace
// operations are allowed. Pass an empty string to disable sandboxing.
//
//wails:ignore
func (s *SearchService) setWorkspaceRoot(root string) error {
	if root == "" {
		s.mu.Lock()
		s.workspaceRoot = ""
		s.workspaceRoots = nil
		s.mu.Unlock()
		return nil
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("workspace root is not a directory: %s", abs)
	}
	// 与 setWorkspaceRoots/canonicalizeExistingWorkspaceRoots 保持同一规范化
	// 形态（Windows 8.3 短名、符号链接前缀），避免单根与多根模式容器行为
	// 随路径拼写漂移。
	if resolved, resolveErr := filepath.EvalSymlinks(abs); resolveErr == nil {
		abs = filepath.Clean(resolved)
	}
	s.mu.Lock()
	s.workspaceRoot = abs
	s.workspaceRoots = nil
	s.mu.Unlock()
	return nil
}

// setWorkspaceRoots installs an all-or-none multi-root search boundary.
// A single root degrades to SetWorkspaceRoot semantics.
//
//wails:ignore
func (s *SearchService) setWorkspaceRoots(roots []string) error {
	if len(roots) == 0 {
		return s.setWorkspaceRoot("")
	}
	cleaned, err := canonicalizeExistingWorkspaceRoots(roots)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.workspaceRoot = cleaned[0]
	if len(cleaned) > 1 {
		s.workspaceRoots = append([]string(nil), cleaned...)
	} else {
		s.workspaceRoots = nil
	}
	s.mu.Unlock()
	return nil
}

func (s *SearchService) WorkspaceRoots() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.workspaceRoots) > 0 {
		return append([]string(nil), s.workspaceRoots...)
	}
	if s.workspaceRoot == "" {
		return nil
	}
	return []string{s.workspaceRoot}
}

func (s *SearchService) workspaceLeaseAndRoots() (workspaceLease, []string, error) {
	s.mu.RLock()
	root := s.workspaceRoot
	roots := append([]string(nil), s.workspaceRoots...)
	ctx := s.workspaceContext
	enforce := s.enforceWorkspace
	s.mu.RUnlock()

	if ctx == nil {
		if root == "" && enforce {
			return workspaceLease{}, nil, fmt.Errorf("search workspace root is not configured: %w", ErrNotAllowed)
		}
		if len(roots) == 0 && root != "" {
			roots = []string{root}
		}
		return workspaceLease{root: root}, roots, nil
	}

	lease, err := acquireWorkspaceLease(ctx, "", 0)
	if err != nil {
		return workspaceLease{}, nil, err
	}
	if root == "" || !sameWorkspaceIdentityPath(root, lease.root) {
		return workspaceLease{}, nil, fmt.Errorf("search workspace switch is not committed: %w", ErrNotAllowed)
	}
	if len(roots) == 0 {
		roots = []string{root}
	}
	return lease, roots, nil
}

func validateSearchLease(lease workspaceLease) error {
	if lease.context == nil && lease.root == "" {
		return nil
	}
	return lease.validateCurrent()
}

// validatePath returns nil only when path is within the committed workspace.
// Renderer-facing instances fail closed when the shared context is empty.
func (s *SearchService) validatePath(path string) error {
	lease, roots, err := s.workspaceLeaseAndRoots()
	if err != nil {
		return err
	}
	if path == "" && lease.root != "" {
		path = lease.root
	}
	if _, err := ValidatePathWithinRoots(roots, path); err != nil {
		return err
	}
	return validateSearchLease(lease)
}

func (s *SearchService) validateMutatingPath(path string) error {
	lease, roots, err := s.workspaceLeaseAndRoots()
	if err != nil {
		return err
	}
	if path == "" && lease.root != "" {
		path = lease.root
	}
	var lastErr error
	for _, candidate := range roots {
		if _, err := ValidateMutatingPathWithinRoot(candidate, path); err == nil {
			return validateSearchLease(lease)
		} else {
			lastErr = err
		}
	}
	if len(roots) == 0 {
		s.mu.RLock()
		legacyUnscoped := s.workspaceContext == nil && !s.enforceWorkspace
		s.mu.RUnlock()
		if legacyUnscoped {
			return nil
		}
		return fmt.Errorf("search workspace root is not configured: %w", ErrNotAllowed)
	}
	return lastErr
}

// ignoredDirs are directory basenames skipped during search.
var ignoredDirs = map[string]bool{
	"node_modules": true,
	".git":         true,
	".hg":          true,
	".svn":         true,
	"dist":         true,
	"build":        true,
	"out":          true,
	".next":        true,
	".nuxt":        true,
	"target":       true,
	"vendor":       true,
}

const searchPatternMaxBytes = 4096
const searchGlobMaxBytes = 1024
const searchGlobMaxCount = 64

const (
	searchTimeout           = 30 * time.Second
	searchMaxFiles          = 100_000
	searchMaxFileBytes      = 20 * 1024 * 1024
	searchMaxAggregateBytes = 512 * 1024 * 1024
	searchMaxMatches        = 10_000
	searchMaxPreviewBytes   = 16 * 1024 * 1024
)

var ErrSearchBudgetExceeded = errors.New("search budget exceeded")

type searchBudget struct {
	files        int
	bytes        int64
	matches      int
	previewBytes int
}

type searchGlobSet struct {
	patterns []*regexp.Regexp
}

func compileSearchGlobs(kind string, globs []string) (searchGlobSet, error) {
	if len(globs) > searchGlobMaxCount {
		return searchGlobSet{}, fmt.Errorf("too many %s globs (maximum %d): %w", kind, searchGlobMaxCount, ErrInvalidInput)
	}
	set := searchGlobSet{patterns: make([]*regexp.Regexp, 0, len(globs))}
	for _, glob := range globs {
		glob = strings.TrimSpace(strings.ReplaceAll(glob, `\`, "/"))
		glob = strings.TrimPrefix(glob, "./")
		if glob == "" {
			continue
		}
		if len(glob) > searchGlobMaxBytes {
			return searchGlobSet{}, fmt.Errorf("%s glob exceeds %d bytes: %w", kind, searchGlobMaxBytes, ErrInvalidInput)
		}
		re, err := compileSearchGlob(glob)
		if err != nil {
			return searchGlobSet{}, fmt.Errorf("invalid %s glob %q: %w", kind, glob, err)
		}
		set.patterns = append(set.patterns, re)
	}
	return set, nil
}

func compileSearchGlob(glob string) (*regexp.Regexp, error) {
	runes := []rune(glob)
	var pattern strings.Builder
	pattern.WriteByte('^')
	for i := 0; i < len(runes); i++ {
		switch runes[i] {
		case '*':
			if i+1 < len(runes) && runes[i+1] == '*' {
				i++
				if i+1 < len(runes) && runes[i+1] == '/' {
					i++
					pattern.WriteString(`(?:.*/)?`)
				} else {
					pattern.WriteString(`.*`)
				}
			} else {
				pattern.WriteString(`[^/]*`)
			}
		case '?':
			pattern.WriteString(`[^/]`)
		case '[':
			end := i + 1
			for end < len(runes) && runes[end] != ']' {
				end++
			}
			if end >= len(runes) || end == i+1 {
				return nil, fmt.Errorf("unterminated character class")
			}
			content := runes[i+1 : end]
			pattern.WriteByte('[')
			if content[0] == '!' {
				pattern.WriteByte('^')
				content = content[1:]
			}
			if len(content) == 0 {
				return nil, fmt.Errorf("empty character class")
			}
			for index, char := range content {
				if char == '\\' || char == ']' || (char == '^' && index == 0) {
					pattern.WriteByte('\\')
				}
				pattern.WriteRune(char)
			}
			pattern.WriteByte(']')
			i = end
		default:
			pattern.WriteString(regexp.QuoteMeta(string(runes[i])))
		}
	}
	pattern.WriteByte('$')
	return regexp.Compile(pattern.String())
}

func (s searchGlobSet) matches(path string) bool {
	for _, pattern := range s.patterns {
		if pattern.MatchString(path) {
			return true
		}
	}
	return false
}

// isBinary returns true if the file content contains a null byte in the first 4KB.
func isBinary(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return true
	}
	defer f.Close()
	buf := make([]byte, 4096)
	n, _ := f.Read(buf)
	return bytes.IndexByte(buf[:n], 0) >= 0
}

// Search walks path recursively and returns files whose content matches the query.
// If ignoreCase is true, the match is case-insensitive. The query is treated as a
// regular expression.
func (s *SearchService) Search(root, query string, ignoreCase bool) ([]SearchResult, error) {
	return s.SearchWithGlobs(context.Background(), root, query, ignoreCase, nil, nil)
}

// SearchWithGlobs searches files filtered by workspace-relative slash globs.
// An empty include list includes all files; any matching exclude wins.
func (s *SearchService) SearchWithGlobs(ctx context.Context, root, query string, ignoreCase bool, includeGlobs, excludeGlobs []string) ([]SearchResult, error) {
	ctx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()
	return s.searchWithGlobsContext(ctx, root, query, ignoreCase, includeGlobs, excludeGlobs)
}

func (s *SearchService) searchWithGlobsContext(ctx context.Context, root, query string, ignoreCase bool, includeGlobs, excludeGlobs []string) ([]SearchResult, error) {
	lease, roots, err := s.workspaceLeaseAndRoots()
	if err != nil {
		return nil, err
	}
	if root == "" && lease.root != "" {
		root = lease.root
	}
	root, err = ValidatePathWithinRoots(roots, root)
	if err != nil {
		return nil, err
	}
	if err := validateSearchLease(lease); err != nil {
		return nil, err
	}
	if len(query) > searchPatternMaxBytes {
		return nil, fmt.Errorf("search pattern exceeds maximum length of %d bytes: %w", searchPatternMaxBytes, ErrInvalidInput)
	}
	includes, err := compileSearchGlobs("include", includeGlobs)
	if err != nil {
		return nil, err
	}
	excludes, err := compileSearchGlobs("exclude", excludeGlobs)
	if err != nil {
		return nil, err
	}
	flags := ""
	if ignoreCase {
		flags = "(?i)"
	}
	re, err := regexp.Compile(flags + query)
	if err != nil {
		return nil, fmt.Errorf("invalid search regex: %w", err)
	}

	var results []SearchResult
	budget := searchBudget{}
	s.mu.RLock()
	walkDir := s.walkDir
	s.mu.RUnlock()
	if walkDir == nil {
		walkDir = filepath.WalkDir
	}
	err = walkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := validateSearchLease(lease); err != nil {
			return err
		}
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			if ignoredDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == "" || strings.HasPrefix(d.Name(), ".") && d.Name() != ".env" {
			// Skip dotfiles except .env
			return nil
		}
		relPath, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		relPath = filepath.ToSlash(relPath)
		if len(includes.patterns) > 0 && !includes.matches(relPath) {
			return nil
		}
		if excludes.matches(relPath) {
			return nil
		}
		budget.files++
		if budget.files > searchMaxFiles {
			return fmt.Errorf("maximum file count of %d exceeded: %w", searchMaxFiles, ErrSearchBudgetExceeded)
		}
		info, statErr := d.Info()
		if statErr != nil || !info.Mode().IsRegular() {
			return nil
		}
		if info.Size() > searchMaxFileBytes {
			return fmt.Errorf("file %q exceeds maximum searchable size of %d bytes: %w", relPath, searchMaxFileBytes, ErrSearchBudgetExceeded)
		}
		budget.bytes += info.Size()
		if budget.bytes > searchMaxAggregateBytes {
			return fmt.Errorf("maximum aggregate size of %d bytes exceeded: %w", searchMaxAggregateBytes, ErrSearchBudgetExceeded)
		}
		if isBinary(p) {
			return nil
		}
		matches, searchErr := searchFile(ctx, p, re, &budget)
		if searchErr != nil {
			return searchErr
		}
		if len(matches) > 0 {
			results = append(results, SearchResult{
				Path:    relPath,
				Matches: matches,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := validateSearchLease(lease); err != nil {
		return nil, err
	}
	return results, nil
}

func searchFile(ctx context.Context, path string, re *regexp.Regexp, budget *searchBudget) ([]SearchMatch, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil
	}
	defer f.Close()

	var matches []SearchMatch
	scanner := bufio.NewScanner(f)
	// Allow longer lines (default 64KB is too small for minified files)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	lineNum := 0
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		lineNum++
		line := scanner.Text()
		loc := re.FindStringIndex(line)
		if loc != nil {
			if budget.matches >= searchMaxMatches {
				return nil, fmt.Errorf("maximum match count of %d exceeded: %w", searchMaxMatches, ErrSearchBudgetExceeded)
			}
			if budget.previewBytes+len(line) > searchMaxPreviewBytes {
				return nil, fmt.Errorf("maximum preview size of %d bytes exceeded: %w", searchMaxPreviewBytes, ErrSearchBudgetExceeded)
			}
			budget.matches++
			budget.previewBytes += len(line)
			matches = append(matches, SearchMatch{
				Line:    lineNum,
				Column:  loc[0] + 1,
				Preview: line,
			})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan search file: %w", err)
	}
	return matches, nil
}

// ReplaceResult reports the outcome of a replace operation.
type ReplaceResult struct {
	Replacements int `json:"replacements"`
}

// ReplacePreview is an immutable replacement proposal tied to a content hash.
type ReplacePreview struct {
	Path            string `json:"path"`
	OriginalHash    string `json:"originalHash"`
	OriginalContent string `json:"originalContent"`
	ModifiedContent string `json:"modifiedContent"`
	Replacements    int    `json:"replacements"`
}

// StructuralReplaceEdit is an exact LSP selectionRange replacement. Positions
// are zero-based and characters are UTF-16 code units, as required by LSP.
type StructuralReplaceEdit struct {
	StartLine      int    `json:"startLine"`
	StartCharacter int    `json:"startCharacter"`
	EndLine        int    `json:"endLine"`
	EndCharacter   int    `json:"endCharacter"`
	ExpectedText   string `json:"expectedText"`
	Replacement    string `json:"replacement"`
}

type resolvedStructuralEdit struct {
	start       int
	end         int
	expected    string
	replacement string
}

const maxStructuralReplaceEdits = 1000

func compileReplacePattern(pattern string, caseSensitive bool) (*regexp.Regexp, error) {
	if strings.TrimSpace(pattern) == "" {
		return nil, errors.New("pattern cannot be empty")
	}
	if len(pattern) > searchPatternMaxBytes {
		return nil, fmt.Errorf("replace pattern exceeds maximum length of %d bytes: %w", searchPatternMaxBytes, ErrInvalidInput)
	}
	flags := ""
	if !caseSensitive {
		flags = "(?i)"
	}
	re, err := regexp.Compile(flags + pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid regex: %w", err)
	}
	return re, nil
}

func replaceContent(content string, re *regexp.Regexp, replacement string) (string, int) {
	count := len(re.FindAllStringIndex(content, -1))
	if count == 0 {
		return content, 0
	}
	return re.ReplaceAllString(content, replacement), count
}

func contentHash(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func lspPositionByteOffset(data []byte, line, character int) (int, error) {
	if line < 0 || character < 0 {
		return 0, fmt.Errorf("negative LSP position: %w", ErrInvalidInput)
	}
	lineStart := 0
	currentLine := 0
	for currentLine < line {
		next := bytes.IndexByte(data[lineStart:], '\n')
		if next < 0 {
			return 0, fmt.Errorf("LSP line %d is outside the file: %w", line, ErrInvalidInput)
		}
		lineStart += next + 1
		currentLine++
	}
	lineEnd := len(data)
	if next := bytes.IndexByte(data[lineStart:], '\n'); next >= 0 {
		lineEnd = lineStart + next
	}
	if lineEnd > lineStart && data[lineEnd-1] == '\r' {
		lineEnd--
	}

	utf16Units := 0
	for offset := lineStart; offset < lineEnd; {
		if utf16Units == character {
			return offset, nil
		}
		r, width := utf8.DecodeRune(data[offset:lineEnd])
		if r == utf8.RuneError && width == 1 {
			return 0, fmt.Errorf("file contains invalid UTF-8 at byte %d: %w", offset, ErrInvalidInput)
		}
		units := 1
		if r > 0xffff {
			units = 2
		}
		if utf16Units+units > character {
			return 0, fmt.Errorf("LSP character %d splits a UTF-16 surrogate pair: %w", character, ErrInvalidInput)
		}
		utf16Units += units
		offset += width
	}
	if utf16Units == character {
		return lineEnd, nil
	}
	return 0, fmt.Errorf("LSP character %d is outside line %d: %w", character, line, ErrInvalidInput)
}

func resolveStructuralEdits(data []byte, edits []StructuralReplaceEdit) ([]resolvedStructuralEdit, error) {
	if len(edits) == 0 {
		return nil, fmt.Errorf("at least one structural edit is required: %w", ErrInvalidInput)
	}
	if len(edits) > maxStructuralReplaceEdits {
		return nil, fmt.Errorf("too many structural edits (maximum %d): %w", maxStructuralReplaceEdits, ErrInvalidInput)
	}
	resolved := make([]resolvedStructuralEdit, 0, len(edits))
	for _, edit := range edits {
		if edit.ExpectedText == "" {
			return nil, fmt.Errorf("structural edit expected text cannot be empty: %w", ErrInvalidInput)
		}
		if len(edit.Replacement) > searchPatternMaxBytes {
			return nil, fmt.Errorf("structural replacement exceeds %d bytes: %w", searchPatternMaxBytes, ErrInvalidInput)
		}
		start, err := lspPositionByteOffset(data, edit.StartLine, edit.StartCharacter)
		if err != nil {
			return nil, err
		}
		end, err := lspPositionByteOffset(data, edit.EndLine, edit.EndCharacter)
		if err != nil {
			return nil, err
		}
		if start >= end {
			return nil, fmt.Errorf("structural edit range must be non-empty: %w", ErrInvalidInput)
		}
		actual := string(data[start:end])
		if actual != edit.ExpectedText {
			return nil, fmt.Errorf("structural edit expected text %q but found %q: %w", edit.ExpectedText, actual, ErrInvalidInput)
		}
		resolved = append(resolved, resolvedStructuralEdit{
			start: start, end: end, expected: edit.ExpectedText, replacement: edit.Replacement,
		})
	}
	sort.Slice(resolved, func(i, j int) bool {
		if resolved[i].start == resolved[j].start {
			return resolved[i].end < resolved[j].end
		}
		return resolved[i].start < resolved[j].start
	})
	for i := 1; i < len(resolved); i++ {
		if resolved[i].start < resolved[i-1].end {
			return nil, fmt.Errorf("structural edit ranges overlap: %w", ErrInvalidInput)
		}
	}
	return resolved, nil
}

func applyResolvedStructuralEdits(data []byte, edits []resolvedStructuralEdit) string {
	var output strings.Builder
	output.Grow(len(data))
	last := 0
	for _, edit := range edits {
		output.Write(data[last:edit.start])
		output.WriteString(edit.replacement)
		last = edit.end
	}
	output.Write(data[last:])
	return output.String()
}

func structuralReplacePreview(filePath string, data []byte, edits []StructuralReplaceEdit) (*ReplacePreview, error) {
	resolved, err := resolveStructuralEdits(data, edits)
	if err != nil {
		return nil, err
	}
	return &ReplacePreview{
		Path:            filePath,
		OriginalHash:    contentHash(data),
		OriginalContent: string(data),
		ModifiedContent: applyResolvedStructuralEdits(data, resolved),
		Replacements:    len(resolved),
	}, nil
}

// PreviewStructuralReplace computes exact symbol-name edits without writing.
func (s *SearchService) PreviewStructuralReplace(filePath string, edits []StructuralReplaceEdit) (*ReplacePreview, error) {
	if err := s.validatePath(filePath); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	return structuralReplacePreview(filePath, data, edits)
}

// ApplyStructuralReplacePreview writes exact symbol edits only if the file
// still has the hash used by the preview.
func (s *SearchService) ApplyStructuralReplacePreview(filePath, expectedHash string, edits []StructuralReplaceEdit) (*ReplaceResult, error) {
	if err := s.validateMutatingPath(filePath); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	if expectedHash == "" || contentHash(data) != expectedHash {
		return nil, fmt.Errorf("file changed since preview: %s", filePath)
	}
	preview, err := structuralReplacePreview(filePath, data, edits)
	if err != nil {
		return nil, err
	}
	if preview.ModifiedContent != string(data) {
		if err := atomicWriteFile(filePath, []byte(preview.ModifiedContent), 0644); err != nil {
			return nil, err
		}
	}
	return &ReplaceResult{Replacements: preview.Replacements}, nil
}

// PreviewReplace computes replacement output without writing the file.
func (s *SearchService) PreviewReplace(filePath, pattern, replacement string, caseSensitive bool) (*ReplacePreview, error) {
	if err := s.validatePath(filePath); err != nil {
		return nil, err
	}
	re, err := compileReplacePattern(pattern, caseSensitive)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	modified, count := replaceContent(string(data), re, replacement)
	return &ReplacePreview{
		Path:            filePath,
		OriginalHash:    contentHash(data),
		OriginalContent: string(data),
		ModifiedContent: modified,
		Replacements:    count,
	}, nil
}

// ApplyReplacePreview writes only when the file still matches the preview hash.
func (s *SearchService) ApplyReplacePreview(filePath, expectedHash, pattern, replacement string, caseSensitive bool) (*ReplaceResult, error) {
	if err := s.validateMutatingPath(filePath); err != nil {
		return nil, err
	}
	re, err := compileReplacePattern(pattern, caseSensitive)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	if expectedHash == "" || contentHash(data) != expectedHash {
		return nil, fmt.Errorf("file changed since preview: %s", filePath)
	}
	modified, count := replaceContent(string(data), re, replacement)
	if count > 0 {
		if err := atomicWriteFile(filePath, []byte(modified), 0644); err != nil {
			return nil, err
		}
	}
	return &ReplaceResult{Replacements: count}, nil
}

// Replace replaces all occurrences of pattern in the file at filePath with
// replacement. If caseSensitive is false, the match is case-insensitive.
// The pattern is treated as a regular expression. The replacement string
// supports capture group references (e.g., $1).
func (s *SearchService) Replace(filePath string, pattern string, replacement string, caseSensitive bool) (*ReplaceResult, error) {
	preview, err := s.PreviewReplace(filePath, pattern, replacement, caseSensitive)
	if err != nil {
		return nil, err
	}
	return s.ApplyReplacePreview(filePath, preview.OriginalHash, pattern, replacement, caseSensitive)
}

// ---------------------------------------------------------------------------
// GOAL-P1-04R: Unified edit transaction entry point for search-replace batch
// ---------------------------------------------------------------------------

// applyMultiFileReplaceTransaction atomically writes a set of pre-computed
// ReplacePreview entries to disk via the unified workspace edit transaction
// (workspace_edit_transaction.go).
//
// Each ReplacePreview must carry the OriginalHash computed at preview time;
// if the file has changed on disk since then the transaction aborts with a
// hash-conflict error before any file is written.
//
// isDirty may be nil (disables the dirty-buffer check).
// wsCtx must be non-nil and have an active root; otherwise the call fails.
//
//wails:ignore
func (s *SearchService) applyMultiFileReplaceTransaction(
	ctx context.Context,
	previews []ReplacePreview,
	wsCtx *WorkspaceContext,
	isDirty func(path string) bool,
) WorkspaceEditApplyResult {
	if wsCtx == nil {
		err := fmt.Errorf("workspace context required for multi-file replace: %w", ErrNotAllowed)
		return WorkspaceEditApplyResult{Err: err, FailureReason: err.Error()}
	}
	root, err := wsCtx.RequireRoot()
	if err != nil {
		return WorkspaceEditApplyResult{Err: err, FailureReason: err.Error()}
	}

	preview := WorkspaceEditPreview{
		Files: make([]WorkspaceEditPreviewFile, 0, len(previews)),
	}
	for _, rp := range previews {
		preview.Files = append(preview.Files, WorkspaceEditPreviewFile{
			FilePath:        rp.Path,
			BaselineHash:    rp.OriginalHash,
			OriginalContent: rp.OriginalContent,
			ModifiedContent: rp.ModifiedContent,
		})
	}

	read := func(path string) (string, error) {
		data, e := os.ReadFile(path)
		if e != nil {
			return "", e
		}
		return string(data), nil
	}
	write := func(path, content string) error {
		return atomicWriteFile(path, []byte(content), 0644)
	}

	return applyEditTransaction(ctx, EditTransaction{TextEdits: preview}, EditTransactionOptions{
		Root:    root,
		Read:    read,
		Write:   write,
		IsDirty: isDirty,
	})
}

// ApplyMultiFileReplace is the renderer-facing adapter for the unified edit
// transaction. Renderer input never supplies or selects the workspace root.
func (s *SearchService) ApplyMultiFileReplace(previews []ReplacePreview) WorkspaceEditApplyResult {
	s.mu.RLock()
	wsCtx := s.workspaceContext
	s.mu.RUnlock()
	return s.applyMultiFileReplaceTransaction(context.Background(), previews, wsCtx, nil)
}
