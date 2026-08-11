package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSearchService_StructuralReplacePreviewUsesUTF16Ranges(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "symbols.ts")
	content := "// 😀 getName\nclass User {\n  getName() {}\n}\n"
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	svc := &SearchService{}
	edits := []StructuralReplaceEdit{
		{
			StartLine: 0, StartCharacter: 6, EndLine: 0, EndCharacter: 13,
			ExpectedText: "getName", Replacement: "displayName",
		},
		{
			StartLine: 2, StartCharacter: 2, EndLine: 2, EndCharacter: 9,
			ExpectedText: "getName", Replacement: "displayName",
		},
	}

	preview, err := svc.PreviewStructuralReplace(filePath, edits)
	if err != nil {
		t.Fatalf("PreviewStructuralReplace failed: %v", err)
	}
	if preview.Replacements != 2 || preview.OriginalHash == "" {
		t.Fatalf("unexpected preview metadata: %+v", preview)
	}
	want := "// 😀 displayName\nclass User {\n  displayName() {}\n}\n"
	if preview.ModifiedContent != want {
		t.Fatalf("unexpected structural preview:\n got %q\nwant %q", preview.ModifiedContent, want)
	}
	if data, _ := os.ReadFile(filePath); string(data) != content {
		t.Fatalf("preview wrote the file: %q", data)
	}

	result, err := svc.ApplyStructuralReplacePreview(filePath, preview.OriginalHash, edits)
	if err != nil {
		t.Fatalf("ApplyStructuralReplacePreview failed: %v", err)
	}
	if result.Replacements != 2 {
		t.Fatalf("expected two replacements, got %+v", result)
	}
	if data, _ := os.ReadFile(filePath); string(data) != want {
		t.Fatalf("unexpected applied content: %q", data)
	}
}

func TestSearchService_StructuralReplaceRejectsStaleOrInvalidEdits(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "symbols.go")
	if err := os.WriteFile(filePath, []byte("func Alpha() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	svc := &SearchService{}
	edit := StructuralReplaceEdit{
		StartLine: 0, StartCharacter: 5, EndLine: 0, EndCharacter: 10,
		ExpectedText: "Alpha", Replacement: "Beta",
	}
	preview, err := svc.PreviewStructuralReplace(filePath, []StructuralReplaceEdit{edit})
	if err != nil {
		t.Fatalf("PreviewStructuralReplace failed: %v", err)
	}

	if err := os.WriteFile(filePath, []byte("func Changed() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyStructuralReplacePreview(filePath, preview.OriginalHash, []StructuralReplaceEdit{edit}); err == nil || !strings.Contains(err.Error(), "changed since preview") {
		t.Fatalf("expected stale preview conflict, got %v", err)
	}

	if err := os.WriteFile(filePath, []byte("func Alpha() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	overlap := []StructuralReplaceEdit{
		edit,
		{StartLine: 0, StartCharacter: 7, EndLine: 0, EndCharacter: 10, ExpectedText: "pha", Replacement: "x"},
	}
	if _, err := svc.PreviewStructuralReplace(filePath, overlap); err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("expected overlap rejection, got %v", err)
	}

	wrongText := edit
	wrongText.ExpectedText = "Omega"
	if _, err := svc.PreviewStructuralReplace(filePath, []StructuralReplaceEdit{wrongText}); err == nil || !strings.Contains(err.Error(), "expected text") {
		t.Fatalf("expected selection text rejection, got %v", err)
	}
}
