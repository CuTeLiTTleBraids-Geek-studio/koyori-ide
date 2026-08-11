package services

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestPresetService_ListPresets_OnlyBuiltin(t *testing.T) {
	svc := NewPresetService("")
	tmp := t.TempDir()
	presets := svc.ListPresets(tmp)
	if len(presets) != len(builtinPresets) {
		t.Fatalf("expected %d presets, got %d", len(builtinPresets), len(presets))
	}
	// Built-in order is preserved.
	for i, p := range presets {
		if p.Name != builtinPresets[i].Name {
			t.Errorf("preset %d: expected name %q, got %q", i, builtinPresets[i].Name, p.Name)
		}
	}
}

func TestPresetService_ListPresets_EmptyProjectRoot(t *testing.T) {
	svc := NewPresetService("")
	presets := svc.ListPresets("")
	if len(presets) != len(builtinPresets) {
		t.Fatalf("expected %d presets with empty project root, got %d", len(builtinPresets), len(presets))
	}
}

func TestPresetService_ListPresets_ProjectOverridesBuiltin(t *testing.T) {
	svc := NewPresetService("")
	tmp := t.TempDir()
	// Create a project preset that overrides the built-in "explain".
	projDir := filepath.Join(tmp, ".koyori-ide", "presets")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	custom := PresetFile{
		Name:        "explain",
		Label:       "Explain (Custom)",
		Description: "Project-customized explain preset",
		Prompt:      "PROJECT EXPLAIN PROMPT",
	}
	data, _ := json.Marshal(custom)
	if err := os.WriteFile(filepath.Join(projDir, "explain.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	presets := svc.ListPresetsWithSource(tmp)
	var found PresetWithSource
	for _, p := range presets {
		if p.Name == "explain" {
			found = p
		}
	}
	if found.Name != "explain" {
		t.Fatal("explain preset not found")
	}
	if found.Source != PresetSourceProject {
		t.Errorf("expected source=project, got %s", found.Source)
	}
	if found.Prompt != "PROJECT EXPLAIN PROMPT" {
		t.Errorf("expected project prompt, got %q", found.Prompt)
	}
	if found.Label != "Explain (Custom)" {
		t.Errorf("expected custom label, got %q", found.Label)
	}
}

func TestPresetService_ListPresets_ProjectAddsNewPreset(t *testing.T) {
	svc := NewPresetService("")
	tmp := t.TempDir()
	projDir := filepath.Join(tmp, ".koyori-ide", "presets")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	custom := PresetFile{
		Name:        "translate",
		Label:       "Translate",
		Description: "Translate code comments",
		Prompt:      "TRANSLATE PROMPT",
	}
	data, _ := json.Marshal(custom)
	if err := os.WriteFile(filepath.Join(projDir, "translate.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	presets := svc.ListPresets(tmp)
	names := make([]string, len(presets))
	for i, p := range presets {
		names[i] = p.Name
	}
	// Built-in presets should still be present.
	if len(presets) != len(builtinPresets)+1 {
		t.Fatalf("expected %d presets, got %d", len(builtinPresets)+1, len(presets))
	}
	found := false
	for _, n := range names {
		if n == "translate" {
			found = true
		}
	}
	if !found {
		t.Error("translate preset not found in list")
	}
	// Built-in presets should appear first in order.
	for i := 0; i < len(builtinPresets); i++ {
		if presets[i].Name != builtinPresets[i].Name {
			t.Errorf("built-in order broken at %d: expected %q, got %q", i, builtinPresets[i].Name, presets[i].Name)
		}
	}
}

func TestPresetService_ListPresets_UserOverridesBuiltin(t *testing.T) {
	userDir := t.TempDir()
	svc := NewPresetService(userDir)
	tmp := t.TempDir()

	userPresetsDir := filepath.Join(userDir, "koyori-ide", "presets")
	if err := os.MkdirAll(userPresetsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	custom := PresetFile{
		Name:        "review",
		Label:       "Review (User)",
		Description: "User-global review preset",
		Prompt:      "USER REVIEW PROMPT",
	}
	data, _ := json.Marshal(custom)
	if err := os.WriteFile(filepath.Join(userPresetsDir, "review.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	presets := svc.ListPresetsWithSource(tmp)
	var found PresetWithSource
	for _, p := range presets {
		if p.Name == "review" {
			found = p
		}
	}
	if found.Source != PresetSourceUser {
		t.Errorf("expected source=user, got %s", found.Source)
	}
	if found.Prompt != "USER REVIEW PROMPT" {
		t.Errorf("expected user prompt, got %q", found.Prompt)
	}
}

func TestPresetService_ListPresets_ProjectOverridesUser(t *testing.T) {
	userDir := t.TempDir()
	svc := NewPresetService(userDir)
	tmp := t.TempDir()

	// User preset
	userPresetsDir := filepath.Join(userDir, "koyori-ide", "presets")
	if err := os.MkdirAll(userPresetsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	userPreset := PresetFile{
		Name:   "custom",
		Label:  "Custom (User)",
		Prompt: "USER CUSTOM PROMPT",
	}
	data, _ := json.Marshal(userPreset)
	if err := os.WriteFile(filepath.Join(userPresetsDir, "custom.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	// Project preset (same name, should override user)
	projDir := filepath.Join(tmp, ".koyori-ide", "presets")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	projPreset := PresetFile{
		Name:   "custom",
		Label:  "Custom (Project)",
		Prompt: "PROJECT CUSTOM PROMPT",
	}
	data, _ = json.Marshal(projPreset)
	if err := os.WriteFile(filepath.Join(projDir, "custom.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	presets := svc.ListPresetsWithSource(tmp)
	var found PresetWithSource
	for _, p := range presets {
		if p.Name == "custom" {
			found = p
		}
	}
	if found.Source != PresetSourceProject {
		t.Errorf("expected source=project, got %s", found.Source)
	}
	if found.Prompt != "PROJECT CUSTOM PROMPT" {
		t.Errorf("expected project prompt, got %q", found.Prompt)
	}
}

func TestPresetService_GetPresetPrompt_Builtin(t *testing.T) {
	svc := NewPresetService("")
	tmp := t.TempDir()
	prompt, err := svc.GetPresetPrompt("explain", tmp)
	if err != nil {
		t.Fatalf("GetPresetPrompt failed: %v", err)
	}
	if prompt == "" {
		t.Error("expected non-empty prompt for builtin explain")
	}
}

func TestPresetService_GetPresetPrompt_Custom(t *testing.T) {
	svc := NewPresetService("")
	tmp := t.TempDir()
	projDir := filepath.Join(tmp, ".koyori-ide", "presets")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	custom := PresetFile{
		Name:   "scaffold",
		Prompt: "SCAFFOLD PROMPT",
	}
	data, _ := json.Marshal(custom)
	if err := os.WriteFile(filepath.Join(projDir, "scaffold.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	prompt, err := svc.GetPresetPrompt("scaffold", tmp)
	if err != nil {
		t.Fatalf("GetPresetPrompt failed: %v", err)
	}
	if prompt != "SCAFFOLD PROMPT" {
		t.Errorf("expected scaffold prompt, got %q", prompt)
	}
}

func TestPresetService_GetPresetPrompt_Unknown(t *testing.T) {
	svc := NewPresetService("")
	tmp := t.TempDir()
	_, err := svc.GetPresetPrompt("nonexistent", tmp)
	if err == nil {
		t.Error("expected error for unknown preset")
	}
}

func TestPresetService_SaveProjectPreset_CreatesFile(t *testing.T) {
	svc := NewPresetService("")
	tmp := t.TempDir()
	preset := PresetFile{
		Name:        "migrate",
		Label:       "Migrate",
		Description: "Run database migration",
		Prompt:      "MIGRATE PROMPT",
	}
	if err := svc.SaveProjectPreset(tmp, preset); err != nil {
		t.Fatalf("SaveProjectPreset failed: %v", err)
	}
	// Verify the file was created.
	path := filepath.Join(tmp, ".koyori-ide", "presets", "migrate.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("preset file not created: %v", err)
	}
	var loaded PresetFile
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("preset file not valid JSON: %v", err)
	}
	if loaded.Name != "migrate" {
		t.Errorf("expected name=migrate, got %q", loaded.Name)
	}
	if loaded.Prompt != "MIGRATE PROMPT" {
		t.Errorf("expected prompt, got %q", loaded.Prompt)
	}
}

func TestPresetService_SaveProjectPreset_OverridesExisting(t *testing.T) {
	svc := NewPresetService("")
	tmp := t.TempDir()
	preset := PresetFile{Name: "test", Prompt: "V1"}
	if err := svc.SaveProjectPreset(tmp, preset); err != nil {
		t.Fatal(err)
	}
	preset.Prompt = "V2"
	if err := svc.SaveProjectPreset(tmp, preset); err != nil {
		t.Fatal(err)
	}
	prompt, err := svc.GetPresetPrompt("test", tmp)
	if err != nil {
		t.Fatal(err)
	}
	if prompt != "V2" {
		t.Errorf("expected V2, got %q", prompt)
	}
}

func TestPresetService_SaveProjectPreset_EmptyRoot(t *testing.T) {
	svc := NewPresetService("")
	err := svc.SaveProjectPreset("", PresetFile{Name: "x"})
	if err == nil {
		t.Error("expected error for empty project root")
	}
}

func TestPresetService_SaveProjectPreset_EmptyName(t *testing.T) {
	svc := NewPresetService("")
	tmp := t.TempDir()
	err := svc.SaveProjectPreset(tmp, PresetFile{})
	if err == nil {
		t.Error("expected error for empty preset name")
	}
}

func TestPresetService_SaveUserPreset_NoConfigDir(t *testing.T) {
	svc := NewPresetService("")
	err := svc.SaveUserPreset(PresetFile{Name: "x"})
	if err == nil {
		t.Error("expected error when configDir is empty")
	}
}

func TestPresetService_SaveUserPreset_CreatesFile(t *testing.T) {
	userDir := t.TempDir()
	svc := NewPresetService(userDir)
	preset := PresetFile{
		Name:   "global",
		Prompt: "GLOBAL PROMPT",
	}
	if err := svc.SaveUserPreset(preset); err != nil {
		t.Fatalf("SaveUserPreset failed: %v", err)
	}
	path := filepath.Join(userDir, "koyori-ide", "presets", "global.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("user preset file not created: %v", err)
	}
}

func TestPresetService_DeleteProjectPreset(t *testing.T) {
	svc := NewPresetService("")
	tmp := t.TempDir()
	preset := PresetFile{Name: "temp", Prompt: "x"}
	if err := svc.SaveProjectPreset(tmp, preset); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteProjectPreset(tmp, "temp"); err != nil {
		t.Fatalf("DeleteProjectPreset failed: %v", err)
	}
	// Verify the file is gone.
	path := filepath.Join(tmp, ".koyori-ide", "presets", "temp.json")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected file to be deleted, got err=%v", err)
	}
}

func TestPresetService_DeleteProjectPreset_NotFound(t *testing.T) {
	svc := NewPresetService("")
	tmp := t.TempDir()
	err := svc.DeleteProjectPreset(tmp, "nonexistent")
	if err == nil {
		t.Error("expected error for deleting non-existent preset")
	}
}

func TestPresetService_DeleteUserPreset(t *testing.T) {
	userDir := t.TempDir()
	svc := NewPresetService(userDir)
	preset := PresetFile{Name: "temp", Prompt: "x"}
	if err := svc.SaveUserPreset(preset); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteUserPreset("temp"); err != nil {
		t.Fatalf("DeleteUserPreset failed: %v", err)
	}
}

func TestPresetService_DeleteUserPreset_NoConfigDir(t *testing.T) {
	svc := NewPresetService("")
	err := svc.DeleteUserPreset("x")
	if err == nil {
		t.Error("expected error when configDir is empty")
	}
}

func TestPresetService_MalformedPresetFile_Skipped(t *testing.T) {
	svc := NewPresetService("")
	tmp := t.TempDir()
	projDir := filepath.Join(tmp, ".koyori-ide", "presets")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Write a malformed JSON file.
	if err := os.WriteFile(filepath.Join(projDir, "broken.json"), []byte("{not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Write a valid preset file.
	valid := PresetFile{Name: "valid", Prompt: "ok"}
	data, _ := json.Marshal(valid)
	if err := os.WriteFile(filepath.Join(projDir, "valid.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	presets := svc.ListPresets(tmp)
	names := make([]string, len(presets))
	for i, p := range presets {
		names[i] = p.Name
	}
	sort.Strings(names)
	// Should contain "valid" but not "broken".
	foundValid := false
	for _, n := range names {
		if n == "valid" {
			foundValid = true
		}
		if n == "broken" {
			t.Error("broken preset should not be loaded")
		}
	}
	if !foundValid {
		t.Error("valid preset should be loaded")
	}
}

func TestPresetService_PresetFileDerivesNameFromFilename(t *testing.T) {
	svc := NewPresetService("")
	tmp := t.TempDir()
	projDir := filepath.Join(tmp, ".koyori-ide", "presets")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Write a preset file without a "name" field — should derive from filename.
	preset := map[string]string{"prompt": "no name field"}
	data, _ := json.Marshal(preset)
	if err := os.WriteFile(filepath.Join(projDir, "derived.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	presets := svc.ListPresets(tmp)
	var found bool
	for _, p := range presets {
		if p.Name == "derived" {
			found = true
		}
	}
	if !found {
		t.Error("expected preset name to be derived from filename")
	}
}

func TestPresetService_ListPresetsWithSource_AllLayers(t *testing.T) {
	userDir := t.TempDir()
	svc := NewPresetService(userDir)
	tmp := t.TempDir()

	// User preset
	userPresetsDir := filepath.Join(userDir, "koyori-ide", "presets")
	if err := os.MkdirAll(userPresetsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	userPreset := PresetFile{Name: "user_tool", Prompt: "user"}
	data, _ := json.Marshal(userPreset)
	if err := os.WriteFile(filepath.Join(userPresetsDir, "user_tool.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	// Project preset
	projDir := filepath.Join(tmp, ".koyori-ide", "presets")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	projPreset := PresetFile{Name: "proj_tool", Prompt: "project"}
	data, _ = json.Marshal(projPreset)
	if err := os.WriteFile(filepath.Join(projDir, "proj_tool.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	presets := svc.ListPresetsWithSource(tmp)
	sources := make(map[string]PresetSource)
	for _, p := range presets {
		sources[p.Name] = p.Source
	}
	if sources["explain"] != PresetSourceBuiltin {
		t.Errorf("explain should be builtin, got %s", sources["explain"])
	}
	if sources["user_tool"] != PresetSourceUser {
		t.Errorf("user_tool should be user, got %s", sources["user_tool"])
	}
	if sources["proj_tool"] != PresetSourceProject {
		t.Errorf("proj_tool should be project, got %s", sources["proj_tool"])
	}
}

// N-92: Path traversal defense — SaveProjectPreset/SaveUserPreset must reject
// preset.Name values that contain "..", path separators, or absolute paths.
// Previously, preset.Name = "../../etc/evil" would write arbitrary .json files.
func TestPresetService_N92_SaveProjectPreset_RejectsTraversal(t *testing.T) {
	svc := NewPresetService("")
	tmp := t.TempDir()
	maliciousNames := []string{
		"../evil",
		"..\\evil",
		"../../etc/passwd",
		"/etc/passwd",
		"sub/file",
		"sub\\file",
		".",
		"..",
	}
	for _, name := range maliciousNames {
		t.Run(name, func(t *testing.T) {
			err := svc.SaveProjectPreset(tmp, PresetFile{
				Name:   name,
				Prompt: "evil",
			})
			if err == nil {
				t.Errorf("SaveProjectPreset(%q) should reject path traversal, got nil", name)
			}
		})
	}
}

func TestPresetService_N92_SaveUserPreset_RejectsTraversal(t *testing.T) {
	userDir := t.TempDir()
	svc := NewPresetService(userDir)
	maliciousNames := []string{
		"../evil",
		"..\\evil",
		"/etc/passwd",
		"sub/file",
		"..",
	}
	for _, name := range maliciousNames {
		t.Run(name, func(t *testing.T) {
			err := svc.SaveUserPreset(PresetFile{
				Name:   name,
				Prompt: "evil",
			})
			if err == nil {
				t.Errorf("SaveUserPreset(%q) should reject path traversal, got nil", name)
			}
		})
	}
}

// N-92: Delete methods must also reject traversal — otherwise a malicious
// name could delete arbitrary .json files outside the preset directory.
func TestPresetService_N92_DeleteProjectPreset_RejectsTraversal(t *testing.T) {
	svc := NewPresetService("")
	tmp := t.TempDir()
	// Place a file outside the preset dir to verify Delete does NOT remove it.
	parent := filepath.Dir(tmp)
	outsideTarget := filepath.Join(parent, "evil.json")
	if err := os.WriteFile(outsideTarget, []byte("sensitive"), 0644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(outsideTarget)

	err := svc.DeleteProjectPreset(tmp, "../evil")
	if err == nil {
		t.Error("DeleteProjectPreset should reject path traversal, got nil")
	}
	if _, err := os.Stat(outsideTarget); err != nil {
		t.Errorf("outside file should still exist after rejected Delete: %v", err)
	}
}

func TestPresetService_N92_DeleteUserPreset_RejectsTraversal(t *testing.T) {
	userDir := t.TempDir()
	svc := NewPresetService(userDir)
	// Place a file outside the user preset dir.
	parent := filepath.Dir(userDir)
	outsideTarget := filepath.Join(parent, "evil.json")
	if err := os.WriteFile(outsideTarget, []byte("sensitive"), 0644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(outsideTarget)

	err := svc.DeleteUserPreset("../evil")
	if err == nil {
		t.Error("DeleteUserPreset should reject path traversal, got nil")
	}
	if _, err := os.Stat(outsideTarget); err != nil {
		t.Errorf("outside file should still exist after rejected Delete: %v", err)
	}
}

// N-92: Sanity check — a legitimate preset name is still accepted.
func TestPresetService_N92_LegitimateNameStillWorks(t *testing.T) {
	svc := NewPresetService("")
	tmp := t.TempDir()
	err := svc.SaveProjectPreset(tmp, PresetFile{
		Name:   "my-custom-preset",
		Prompt: "CUSTOM PROMPT",
	})
	if err != nil {
		t.Fatalf("SaveProjectPreset with legitimate name failed: %v", err)
	}
	prompt, err := svc.GetPresetPrompt("my-custom-preset", tmp)
	if err != nil {
		t.Fatalf("GetPresetPrompt failed: %v", err)
	}
	if prompt != "CUSTOM PROMPT" {
		t.Errorf("expected custom prompt, got %q", prompt)
	}
	if err := svc.DeleteProjectPreset(tmp, "my-custom-preset"); err != nil {
		t.Fatalf("DeleteProjectPreset failed: %v", err)
	}
}
