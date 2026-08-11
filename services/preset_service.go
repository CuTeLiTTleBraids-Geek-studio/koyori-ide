package services

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PresetSource identifies where a preset was loaded from.
type PresetSource string

const (
	// PresetSourceBuiltin is the built-in preset set (compiled into the binary).
	PresetSourceBuiltin PresetSource = "builtin"
	// PresetSourceProject is a project-level preset file in .koyori-ide/presets/.
	PresetSourceProject PresetSource = "project"
	// PresetSourceUser is a user-global preset file in the config directory.
	PresetSourceUser PresetSource = "user"
)

// PresetFile is the on-disk JSON format for a custom preset (N-17).
// Stored as `<name>.json` in either `.koyori-ide/presets/` (project) or
// `<configDir>/koyori-ide/presets/` (user global).
type PresetFile struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Icon        string `json:"icon,omitempty"`
	Prompt      string `json:"prompt"`
}

// PresetWithSource is a PresetFile annotated with its source layer.
// Used by ListPresets to report which layer each preset came from.
type PresetWithSource struct {
	PresetFile
	Source PresetSource `json:"source"`
}

// PresetService merges preset definitions from three layers (N-17):
//  1. Built-in presets (compiled into the binary)
//  2. Project presets (`.koyori-ide/presets/*.json`)
//  3. User presets (`<configDir>/koyori-ide/presets/*.json`)
//
// Merge order: built-in first, then user, then project. Later layers override
// earlier ones by name (so a project preset can customize a built-in, and a
// user preset can customize both). Display order is: overridden presets keep
// their built-in position; new presets are appended alphabetically.
type PresetService struct {
	// configDir is the user-level config directory (e.g. ~/.config on Linux,
	// %APPDATA% on Windows). If empty, the user preset layer is skipped.
	configDir string
}

// NewPresetService constructs a PresetService. configDir is the user config
// directory; pass empty to disable the user-global preset layer.
func NewPresetService(configDir string) *PresetService {
	return &PresetService{configDir: configDir}
}

// projectPresetsDir is the subdirectory of the project root holding
// project-level preset JSON files.
const projectPresetsDir = ".koyori-ide/presets"

// userPresetsSubdir is the subdirectory of configDir holding user-global
// preset JSON files.
const userPresetsSubdir = "koyori-ide/presets"

// ListPresets returns all presets from all layers, merged by name.
// Built-in presets appear first in their defined order; new custom presets
// are appended alphabetically by name. Used by the AI service to expose
// presets to the frontend.
func (s *PresetService) ListPresets(projectRoot string) []PresetMeta {
	merged := s.loadMergedPresets(projectRoot)
	result := make([]PresetMeta, 0, len(merged))
	for _, p := range merged {
		result = append(result, PresetMeta{
			Name:        p.Name,
			Label:       p.Label,
			Description: p.Description,
			Icon:        p.Icon,
		})
	}
	return result
}

// ListPresetsWithSource is like ListPresets but also reports each preset's
// source layer. Used by the preset manager UI to show where each preset
// came from.
func (s *PresetService) ListPresetsWithSource(projectRoot string) []PresetWithSource {
	return s.loadMergedPresets(projectRoot)
}

// GetPresetPrompt returns the instruction template for the named preset,
// searching all layers. Returns an error if the preset is not found.
func (s *PresetService) GetPresetPrompt(name, projectRoot string) (string, error) {
	merged := s.loadMergedPresets(projectRoot)
	for _, p := range merged {
		if p.Name == name {
			return p.Prompt, nil
		}
	}
	return "", fmt.Errorf("unknown preset: %s", name)
}

// SaveProjectPreset writes a preset JSON file to the project's
// `.koyori-ide/presets/<name>.json`. Parent directories are created as needed.
// The preset's Name field determines the filename.
func (s *PresetService) SaveProjectPreset(projectRoot string, preset PresetFile) error {
	if projectRoot == "" {
		return fmt.Errorf("project root is required")
	}
	if preset.Name == "" {
		return fmt.Errorf("preset name is required")
	}
	return writePresetFile(filepath.Join(projectRoot, projectPresetsDir), preset)
}

// SaveUserPreset writes a preset JSON file to the user-global
// `<configDir>/koyori-ide/presets/<name>.json`. Requires configDir to be set.
func (s *PresetService) SaveUserPreset(preset PresetFile) error {
	if s.configDir == "" {
		return fmt.Errorf("user config directory is not configured")
	}
	if preset.Name == "" {
		return fmt.Errorf("preset name is required")
	}
	return writePresetFile(filepath.Join(s.configDir, userPresetsSubdir), preset)
}

// DeleteProjectPreset removes a project-level preset file.
//
// N-92: name is validated via ValidateNameForFlatDir to reject path
// traversal, separators, and absolute paths before constructing the path.
func (s *PresetService) DeleteProjectPreset(projectRoot, name string) error {
	if projectRoot == "" {
		return fmt.Errorf("project root is required")
	}
	if err := ValidateNameForFlatDir(name); err != nil {
		return fmt.Errorf("invalid preset name: %w", err)
	}
	path := filepath.Join(projectRoot, projectPresetsDir, name+".json")
	return os.Remove(path)
}

// DeleteUserPreset removes a user-global preset file.
//
// N-92: name is validated via ValidateNameForFlatDir to reject path
// traversal, separators, and absolute paths before constructing the path.
func (s *PresetService) DeleteUserPreset(name string) error {
	if s.configDir == "" {
		return fmt.Errorf("user config directory is not configured")
	}
	if err := ValidateNameForFlatDir(name); err != nil {
		return fmt.Errorf("invalid preset name: %w", err)
	}
	path := filepath.Join(s.configDir, userPresetsSubdir, name+".json")
	return os.Remove(path)
}

// loadMergedPresets loads presets from all three layers and merges them.
// Merge order: built-in → user → project (later layers override by name).
// Display order: built-in order preserved; new presets appended alphabetically.
func (s *PresetService) loadMergedPresets(projectRoot string) []PresetWithSource {
	// Use a map for name → preset (last write wins) and a slice for order.
	byName := make(map[string]PresetWithSource)
	order := make([]string, 0, len(builtinPresets))

	// Layer 1: built-in presets.
	for _, p := range builtinPresets {
		name := p.Name
		if _, exists := byName[name]; !exists {
			order = append(order, name)
		}
		byName[name] = PresetWithSource{
			PresetFile: PresetFile{
				Name:        p.Name,
				Label:       p.Label,
				Description: p.Description,
				Icon:        p.Icon,
				Prompt:      p.Prompt,
			},
			Source: PresetSourceBuiltin,
		}
	}

	// Layer 2: user-global presets (override built-in by name).
	if s.configDir != "" {
		userDir := filepath.Join(s.configDir, userPresetsSubdir)
		userPresets := loadPresetDir(userDir, PresetSourceUser)
		// Sort user presets by name for deterministic display order.
		sort.Slice(userPresets, func(i, j int) bool {
			return userPresets[i].Name < userPresets[j].Name
		})
		for _, p := range userPresets {
			if _, exists := byName[p.Name]; !exists {
				order = append(order, p.Name)
			}
			byName[p.Name] = p
		}
	}

	// Layer 3: project presets (override user and built-in by name).
	if projectRoot != "" {
		projDir := filepath.Join(projectRoot, projectPresetsDir)
		projPresets := loadPresetDir(projDir, PresetSourceProject)
		sort.Slice(projPresets, func(i, j int) bool {
			return projPresets[i].Name < projPresets[j].Name
		})
		for _, p := range projPresets {
			if _, exists := byName[p.Name]; !exists {
				order = append(order, p.Name)
			}
			byName[p.Name] = p
		}
	}

	// Build result in display order.
	result := make([]PresetWithSource, 0, len(order))
	for _, name := range order {
		result = append(result, byName[name])
	}
	return result
}

// loadPresetDir reads all `*.json` preset files from dir. Returns an empty
// slice if dir doesn't exist or can't be read. Malformed files are skipped
// silently — a corrupt preset file should not break the whole list.
func loadPresetDir(dir string, source PresetSource) []PresetWithSource {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var result []PresetWithSource
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var pf PresetFile
		if err := json.Unmarshal(data, &pf); err != nil {
			continue
		}
		if pf.Name == "" {
			// Derive name from filename if not set in JSON.
			pf.Name = strings.TrimSuffix(name, ".json")
		}
		if pf.Label == "" {
			pf.Label = pf.Name
		}
		result = append(result, PresetWithSource{PresetFile: pf, Source: source})
	}
	return result
}

// writePresetFile writes a preset JSON file to dir/<name>.json, creating
// parent directories as needed. The output is pretty-printed JSON.
//
// N-92: previously this used filepath.Join(dir, preset.Name+".json")
// without any sanitization, allowing preset.Name values like "../../etc/evil"
// to write arbitrary .json files. We now use SafeNameJoin from pathsec.go
// which rejects path separators, parent traversal, and absolute paths.
func writePresetFile(dir string, preset PresetFile) error {
	path, err := SafeNameJoin(dir, preset.Name, ".json")
	if err != nil {
		return fmt.Errorf("invalid preset name: %w", err)
	}
	// G-SEC-09: atomic write (temp file + rename).
	if err := atomicWriteJSON(path, preset, 0644); err != nil {
		return fmt.Errorf("write preset file: %w", err)
	}
	return nil
}
