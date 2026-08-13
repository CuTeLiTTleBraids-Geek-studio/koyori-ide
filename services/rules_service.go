package services

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RulesFile describes a project-level AI rules file loaded from disk.
// The content is appended to the system prompt so the AI obeys project
// conventions (coding style, architecture decisions, forbidden APIs, etc.).
type RulesFile struct {
	// Path is the rules file path relative to the project root.
	Path string `json:"path"`
	// Content is the raw text of the rules file.
	Content string `json:"content"`
	// Source identifies the rules file format/family.
	// One of: "koyoriIde", "cursor", "agents", "ai", or a custom source label.
	Source string `json:"source"`
}

// RulesCandidateConfig describes one rules file location to probe, as
// configured by the user. Paths may be relative to the project root or
// contain glob metacharacters (e.g. "docs/**/*.rules.md").
type RulesCandidateConfig struct {
	Path   string `json:"path"`
	Source string `json:"source"`
}

// RulesConfig controls how rules files are discovered and combined (N-18).
type RulesConfig struct {
	// Candidates is the custom list of rules file paths to probe. If
	// empty, the built-in defaults are used.
	Candidates []RulesCandidateConfig `json:"candidates,omitempty"`
	// Mode controls how multiple rules files are combined:
	//   "first" (default) — only the first existing file is used
	//   "merge"            — all existing files are concatenated in
	//                        priority order, separated by a header
	Mode string `json:"mode,omitempty"`
}

// rulesConfigSource identifies where a RulesConfig was loaded from.
type rulesConfigSource string

const (
	rulesConfigSourceBuiltin rulesConfigSource = "builtin"
	rulesConfigSourceUser    rulesConfigSource = "user"
	rulesConfigSourceProject rulesConfigSource = "project"
)

// RulesConfigWithSource is a RulesConfig annotated with its source layer,
// returned by LoadRulesConfigWithSource for UI display.
type RulesConfigWithSource struct {
	RulesConfig
	Source rulesConfigSource `json:"source"`
}

// rulesCandidateEntry is the internal representation of a candidate,
// tracking which config layer it came from.
type rulesCandidateEntry struct {
	Path   string
	Source string
}

// defaultRulesCandidates returns the built-in rules file locations in
// priority order. The first existing file wins. Paths are relative to the
// project root.
func defaultRulesCandidates() []rulesCandidateEntry {
	return []rulesCandidateEntry{
		{".koyori-ide/rules.md", "koyoriIde"},
		{".cursorrules", "cursor"},
		{"AGENTS.md", "agents"},
		{".ai/rules.md", "ai"},
	}
}

// defaultRulesPath is used when saving a new rules file via SaveRules with
// an empty relPath argument.
const defaultRulesPath = ".koyori-ide/rules.md"

// rulesConfigFileName is the on-disk config file name (N-18).
const rulesConfigFileName = "rules-config.json"

// projectRulesConfigPath is the project-level config path (relative to root).
const projectRulesConfigPath = ".koyori-ide/" + rulesConfigFileName

// userRulesConfigDir is the user-global config subdirectory.
const userRulesConfigDir = "koyori-ide"

// RulesService loads and saves project-level AI rules files (#25, N-18).
// Supported formats (auto-detected by filename):
//   - .koyori-ide/rules.md   — koyori-ide native (Markdown)
//   - .cursorrules      — Cursor-compatible (plain text)
//   - AGENTS.md         — Agent-style (Markdown)
//   - .ai/rules.md      — Alternative location (Markdown)
//
// Custom candidate paths and merge mode are configurable via RulesConfig
// (project `.koyori-ide/rules-config.json` and user-global
// `<configDir>/koyori-ide/rules-config.json`).
type RulesService struct {
	// configDir is the user-level config directory (e.g. ~/.config on Linux,
	// %APPDATA% on Windows). If empty, the user config layer is skipped.
	configDir string
}

// NewRulesService constructs a RulesService. configDir is the user config
// directory; pass empty to disable the user-global rules config layer.
func NewRulesService(configDir string) *RulesService {
	return &RulesService{configDir: configDir}
}

// LoadRules scans the project root for rules files in priority order and
// returns the first one found. Returns an empty RulesFile (empty Path and
// Content) when no rules file exists — this is not an error.
//
// Note: this is the legacy single-file API. For merge mode (N-18), use
// LoadRulesMerge which returns all existing files.
func (s *RulesService) LoadRules(projectRoot string) (RulesFile, error) {
	if projectRoot == "" {
		return RulesFile{}, nil
	}
	merged, err := s.loadMergedCandidates(projectRoot)
	if err != nil {
		return RulesFile{}, err
	}
	for _, c := range merged {
		matches, err := expandGlob(projectRoot, c.Path)
		if err != nil {
			return RulesFile{}, fmt.Errorf("glob rules path %s: %w", c.Path, err)
		}
		for _, rel := range matches {
			full := filepath.Join(projectRoot, rel)
			data, err := os.ReadFile(full)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return RulesFile{}, fmt.Errorf("read rules file %s: %w", rel, err)
			}
			return RulesFile{
				Path:    rel,
				Content: string(data),
				Source:  c.Source,
			}, nil
		}
	}
	return RulesFile{}, nil
}

// LoadRulesMerge returns all existing rules files in priority order (N-18).
// In "first" mode the result contains at most one entry (the first found).
// In "merge" mode the result contains every existing file. When the config
// is unset, behavior defaults to "first" for backward compatibility.
func (s *RulesService) LoadRulesMerge(projectRoot string) ([]RulesFile, error) {
	if projectRoot == "" {
		return nil, nil
	}
	cfg, err := s.LoadRulesConfig(projectRoot)
	if err != nil {
		return nil, err
	}
	mode := cfg.Mode
	if mode == "" {
		mode = "first"
	}
	merged, err := s.loadMergedCandidates(projectRoot)
	if err != nil {
		return nil, err
	}
	var out []RulesFile
	for _, c := range merged {
		matches, err := expandGlob(projectRoot, c.Path)
		if err != nil {
			return nil, fmt.Errorf("glob rules path %s: %w", c.Path, err)
		}
		for _, rel := range matches {
			full := filepath.Join(projectRoot, rel)
			data, err := os.ReadFile(full)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return nil, fmt.Errorf("read rules file %s: %w", rel, err)
			}
			out = append(out, RulesFile{
				Path:    rel,
				Content: string(data),
				Source:  c.Source,
			})
			if mode == "first" {
				return out, nil
			}
		}
	}
	return out, nil
}

// LoadRulesConfig returns the merged rules configuration (N-18):
// built-in defaults → user global → project. Later layers override the
// mode; candidate lists are merged by path (later layers add new paths
// or override the source of an existing path, preserving order).
func (s *RulesService) LoadRulesConfig(projectRoot string) (RulesConfig, error) {
	merged, _, err := s.loadMergedConfig(projectRoot)
	return merged, err
}

// LoadRulesConfigWithSource returns the merged rules configuration along
// with the source layer that contributed each part. Used by the UI to
// show where the config comes from.
func (s *RulesService) LoadRulesConfigWithSource(projectRoot string) (RulesConfig, rulesConfigSource, error) {
	return s.loadMergedConfig(projectRoot)
}

// SaveRulesConfig writes the project-level rules config (N-18).
func (s *RulesService) SaveRulesConfig(projectRoot string, cfg RulesConfig) error {
	if projectRoot == "" {
		return fmt.Errorf("project root is required")
	}
	full := filepath.Join(projectRoot, projectRulesConfigPath)
	// G-SEC-09: atomic write (temp file + rename).
	if err := atomicWriteJSON(full, cfg, 0644); err != nil {
		return fmt.Errorf("write rules config: %w", err)
	}
	return nil
}

// SaveUserRulesConfig writes the user-global rules config (N-18).
// Returns an error if the service was constructed without a configDir.
func (s *RulesService) SaveUserRulesConfig(cfg RulesConfig) error {
	if s.configDir == "" {
		return fmt.Errorf("user config directory is not configured")
	}
	full := filepath.Join(s.configDir, userRulesConfigDir, rulesConfigFileName)
	// G-SEC-09: atomic write (temp file + rename).
	if err := atomicWriteJSON(full, cfg, 0644); err != nil {
		return fmt.Errorf("write user rules config: %w", err)
	}
	return nil
}

// SaveRules writes the rules content to the given relative path inside the
// project root. If relPath is empty, the default koyori-ide location is
// used. Parent directories are created as needed.
//
// N-112: the previous lexical-only check (filepath.IsAbs + ".." prefix)
// could be bypassed by a symlink inside the project that points outside.
// We now resolve symlinks via IsPathOutsideRoot (which calls EvalSymlinks
// on both the target and the root) so a symlinked escape is rejected.
func (s *RulesService) SaveRules(projectRoot, relPath, content string) error {
	if projectRoot == "" {
		return fmt.Errorf("project root is required")
	}
	if relPath == "" {
		relPath = defaultRulesPath
	}
	// Reject absolute paths and parent traversal lexically first (cheap).
	if filepath.IsAbs(relPath) || strings.HasPrefix(filepath.Clean(relPath), "..") {
		return fmt.Errorf("rules path must be a relative path within the project: %s", relPath)
	}
	full := filepath.Join(projectRoot, relPath)
	// N-112: symlink-aware escape check. A project-internal symlink that
	// points outside the project root would pass the lexical check above
	// but write outside the project. IsPathOutsideRoot resolves symlinks
	// on both paths before comparing.
	absRoot, err := filepath.Abs(projectRoot)
	if err != nil {
		return fmt.Errorf("resolve project root: %w", err)
	}
	if IsPathOutsideRoot(absRoot, full) {
		return fmt.Errorf("rules path resolves outside the project root: %s", relPath)
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return fmt.Errorf("create rules directory: %w", err)
	}
	// M-5: atomic write (temp+rename) prevents half-written rules file.
	if err := atomicWriteFile(full, []byte(content), 0644); err != nil {
		return fmt.Errorf("write rules file: %w", err)
	}
	return nil
}

// ListRulesCandidates returns the list of rules file paths (relative to
// project root) and whether each one currently exists. Uses the merged
// config so user/project candidates appear alongside the defaults (N-18).
func (s *RulesService) ListRulesCandidates(projectRoot string) ([]RulesFileCandidate, error) {
	merged, err := s.loadMergedCandidates(projectRoot)
	if err != nil {
		return nil, err
	}
	// Deduplicate by path while preserving order, then stat each.
	seen := make(map[string]bool)
	result := make([]RulesFileCandidate, 0, len(merged))
	for _, c := range merged {
		if seen[c.Path] {
			continue
		}
		seen[c.Path] = true
		// For glob patterns, "exists" is true if at least one match exists.
		matches, _ := expandGlob(projectRoot, c.Path)
		exists := len(matches) > 0
		result = append(result, RulesFileCandidate{
			Path:   c.Path,
			Source: c.Source,
			Exists: exists,
		})
	}
	return result, nil
}

// RulesFileCandidate describes a possible rules file location.
type RulesFileCandidate struct {
	Path   string `json:"path"`
	Source string `json:"source"`
	Exists bool   `json:"exists"`
}

// loadMergedConfig merges the three config layers (N-18):
//  1. Built-in defaults (4 candidates, mode="first")
//  2. User global (`<configDir>/koyori-ide/rules-config.json`)
//  3. Project (`.koyori-ide/rules-config.json`)
//
// Mode: the last layer that sets a non-empty mode wins.
// Candidates: built-in first, then user additions (in order), then project
// additions. If a later layer uses the same path as an earlier one, the
// later source label overrides but the position is unchanged.
func (s *RulesService) loadMergedConfig(projectRoot string) (RulesConfig, rulesConfigSource, error) {
	var merged RulesConfig
	merged.Mode = "first"
	source := rulesConfigSourceBuiltin

	// Built-in defaults.
	for _, c := range defaultRulesCandidates() {
		merged.Candidates = append(merged.Candidates, RulesCandidateConfig(c))
	}

	// User global.
	if s.configDir != "" {
		userCfg, err := readRulesConfig(filepath.Join(s.configDir, userRulesConfigDir, rulesConfigFileName))
		if err == nil && userCfg != nil {
			applyConfigLayer(&merged, userCfg)
			if userCfg.Mode != "" {
				merged.Mode = userCfg.Mode
				source = rulesConfigSourceUser
			}
		}
	}

	// Project.
	if projectRoot != "" {
		projCfg, err := readRulesConfig(filepath.Join(projectRoot, projectRulesConfigPath))
		if err == nil && projCfg != nil {
			applyConfigLayer(&merged, projCfg)
			if projCfg.Mode != "" {
				merged.Mode = projCfg.Mode
				source = rulesConfigSourceProject
			}
		}
	}

	return merged, source, nil
}

// applyConfigLayer merges a layer's candidates into the merged config:
// new paths are appended; existing paths have their source label overridden.
func applyConfigLayer(merged *RulesConfig, layer *RulesConfig) {
	for _, c := range layer.Candidates {
		if c.Path == "" {
			continue
		}
		found := false
		for i := range merged.Candidates {
			if merged.Candidates[i].Path == c.Path {
				if c.Source != "" {
					merged.Candidates[i].Source = c.Source
				}
				found = true
				break
			}
		}
		if !found {
			merged.Candidates = append(merged.Candidates, c)
		}
	}
}

// readRulesConfig reads and parses a rules config JSON file. Returns
// (nil, nil) if the file does not exist — this is not an error.
func readRulesConfig(path string) (*RulesConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read rules config %s: %w", path, err)
	}
	var cfg RulesConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse rules config %s: %w", path, err)
	}
	return &cfg, nil
}

// loadMergedCandidates returns the merged candidate list (as internal
// entries) using the merged config.
func (s *RulesService) loadMergedCandidates(projectRoot string) ([]rulesCandidateEntry, error) {
	cfg, _, err := s.loadMergedConfig(projectRoot)
	if err != nil {
		return nil, err
	}
	out := make([]rulesCandidateEntry, 0, len(cfg.Candidates))
	for _, c := range cfg.Candidates {
		out = append(out, rulesCandidateEntry(c))
	}
	return out, nil
}

// expandGlob resolves a candidate path against the project root. If the
// path contains glob metacharacters, it is expanded and the matching
// relative paths (that actually exist) are returned. If it contains no
// metacharacters, the path is returned as-is (still relative) only if the
// file exists — otherwise an empty slice is returned. This makes the
// return value directly usable for "does this candidate exist?" checks.
//
// Patterns containing "**" (double-star) are handled via a directory walk
// because filepath.Glob does not support recursive matching. The "**"
// segment matches any number of path components (including zero).
func expandGlob(projectRoot, pattern string) ([]string, error) {
	if strings.Contains(pattern, "**") {
		return expandDoubleStarGlob(projectRoot, pattern)
	}
	if !strings.ContainsAny(pattern, "*?[]") {
		// Plain path — verify existence.
		full := filepath.Join(projectRoot, pattern)
		if _, err := os.Stat(full); err != nil {
			return nil, nil
		}
		return []string{pattern}, nil
	}
	// Standard glob (filepath.Glob only returns existing matches).
	full := filepath.Join(projectRoot, pattern)
	matches, err := filepath.Glob(full)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		rel, err := filepath.Rel(projectRoot, m)
		if err != nil {
			continue
		}
		// Skip parent traversal results from globs.
		if strings.HasPrefix(filepath.Clean(rel), "..") {
			continue
		}
		out = append(out, filepath.ToSlash(rel))
	}
	return out, nil
}

// expandDoubleStarGlob handles patterns containing "**" by walking the
// directory tree. The "**" segment matches any number of path components
// (including zero). The pattern is split at the first "**": the prefix is
// the starting directory, the suffix is matched against each file's path
// relative to the prefix. If the suffix contains no path separator, it is
// matched against the file basename; otherwise it is matched against the
// full relative path.
func expandDoubleStarGlob(projectRoot, pattern string) ([]string, error) {
	// Normalize the pattern to the OS path separator so that patterns
	// written with "/" work on Windows.
	pattern = filepath.FromSlash(pattern)
	idx := strings.Index(pattern, "**")
	prefix := pattern[:idx]
	suffix := pattern[idx+2:]
	prefix = strings.TrimSuffix(prefix, string(filepath.Separator))
	suffix = strings.TrimPrefix(suffix, string(filepath.Separator))

	startDir := filepath.Join(projectRoot, prefix)
	if _, err := os.Stat(startDir); err != nil {
		// Starting directory doesn't exist — no matches.
		return nil, nil
	}

	var out []string
	err := filepath.WalkDir(startDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(projectRoot, path)
		if err != nil {
			return nil
		}
		if strings.HasPrefix(filepath.Clean(rel), "..") {
			return nil
		}
		if suffix == "" {
			out = append(out, filepath.ToSlash(rel))
			return nil
		}
		if strings.Contains(suffix, string(filepath.Separator)) {
			// Suffix has subdirs — match against path relative to prefix.
			relToPrefix, err := filepath.Rel(prefix, rel)
			if err != nil {
				return nil
			}
			ok, _ := filepath.Match(suffix, relToPrefix)
			if ok {
				out = append(out, filepath.ToSlash(rel))
			}
		} else {
			// Suffix is a single segment — match against basename.
			ok, _ := filepath.Match(suffix, d.Name())
			if ok {
				out = append(out, filepath.ToSlash(rel))
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
