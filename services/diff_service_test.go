package services

import (
	"strings"
	"testing"
)

func TestDiffService_ComputeFileDiff(t *testing.T) {
	s := NewDiffService()
	old := "line1\nline2\nline3"
	new := "line1\nline2-modified\nline3\nline4"

	fd := s.ComputeFileDiff("test.txt", old, new)

	if fd.Path != "test.txt" {
		t.Errorf("expected path test.txt, got %s", fd.Path)
	}
	if fd.OldContent != old {
		t.Error("old content mismatch")
	}
	if fd.NewContent != new {
		t.Error("new content mismatch")
	}
	if len(fd.Hunks) == 0 {
		t.Error("expected at least 1 hunk, got 0")
	}
	// 应该有新增行和删除行
	if fd.AddedLines == 0 {
		t.Error("expected added lines > 0")
	}
	if fd.RemovedLines == 0 {
		t.Error("expected removed lines > 0")
	}
}

func TestDiffService_ComputeFileDiff_NoChange(t *testing.T) {
	s := NewDiffService()
	content := "line1\nline2\nline3"

	fd := s.ComputeFileDiff("test.txt", content, content)

	if fd.AddedLines != 0 || fd.RemovedLines != 0 {
		t.Errorf("expected 0 changes, got +%d/-%d", fd.AddedLines, fd.RemovedLines)
	}
}

func TestDiffService_ComputeMultiFileDiff(t *testing.T) {
	s := NewDiffService()
	files := []FileInput{
		{Path: "a.txt", OldContent: "old", NewContent: "new"},
		{Path: "b.txt", OldContent: "line1\nline2", NewContent: "line1\nline2\nline3"},
	}

	diff := s.ComputeMultiFileDiff(files)

	if len(diff.Files) != 2 {
		t.Errorf("expected 2 files, got %d", len(diff.Files))
	}
	if diff.TotalAdded == 0 {
		t.Error("expected total added > 0")
	}
}

func TestDiffService_ThreeWayMerge_NoConflict(t *testing.T) {
	base := "line1\nline2\nline3"
	ours := "line1\nline2\nline3"
	theirs := "line1\nline2\nline3"

	result := ThreeWayMerge(base, ours, theirs)

	if result.HasConflict {
		t.Error("expected no conflict")
	}
	if result.Conflicts != 0 {
		t.Errorf("expected 0 conflicts, got %d", result.Conflicts)
	}
}

func TestDiffService_ThreeWayMerge_OursChanged(t *testing.T) {
	base := "line1\nline2\nline3"
	ours := "line1\nline2-modified\nline3"
	theirs := "line1\nline2\nline3"

	result := ThreeWayMerge(base, ours, theirs)

	if result.HasConflict {
		t.Error("expected no conflict when only ours changed")
	}
	if !strings.Contains(result.Merged, "line2-modified") {
		t.Error("expected merged to contain ours change")
	}
}

func TestDiffService_ThreeWayMerge_TheirsChanged(t *testing.T) {
	base := "line1\nline2\nline3"
	ours := "line1\nline2\nline3"
	theirs := "line1\nline2-modified\nline3"

	result := ThreeWayMerge(base, ours, theirs)

	if result.HasConflict {
		t.Error("expected no conflict when only theirs changed")
	}
	if !strings.Contains(result.Merged, "line2-modified") {
		t.Error("expected merged to contain theirs change")
	}
}

func TestDiffService_ThreeWayMerge_Conflict(t *testing.T) {
	base := "line1\nline2\nline3"
	ours := "line1\nline2-ours\nline3"
	theirs := "line1\nline2-theirs\nline3"

	result := ThreeWayMerge(base, ours, theirs)

	if !result.HasConflict {
		t.Error("expected conflict")
	}
	if result.Conflicts != 1 {
		t.Errorf("expected 1 conflict, got %d", result.Conflicts)
	}
	if !strings.Contains(result.Merged, "<<<<<<< ours") {
		t.Error("expected conflict marker <<<<<<< ours")
	}
	if !strings.Contains(result.Merged, "=======") {
		t.Error("expected conflict separator =======")
	}
	if !strings.Contains(result.Merged, ">>>>>>> theirs") {
		t.Error("expected conflict marker >>>>>>> theirs")
	}
}

func TestDiffService_ThreeWayMergeFile_Wrapper(t *testing.T) {
	// 验证 service wrapper 转发到包级 ThreeWayMerge（Step 2，供 Wails bindings 暴露）。
	s := NewDiffService()
	base := "line1\nline2\nline3"
	ours := "line1\nline2-ours\nline3"
	theirs := "line1\nline2-theirs\nline3"

	result := s.ThreeWayMergeFile(base, ours, theirs)

	if !result.HasConflict {
		t.Error("expected conflict via wrapper")
	}
	if result.Conflicts != 1 {
		t.Errorf("expected 1 conflict, got %d", result.Conflicts)
	}
	// 与直接调用包级函数结果一致
	direct := ThreeWayMerge(base, ours, theirs)
	if result.Merged != direct.Merged {
		t.Error("wrapper result should equal direct package-level call")
	}
}

func TestDiffService_AddAIComment(t *testing.T) {
	s := NewDiffService()
	diff := MultiFileDiff{
		Files: []FileDiff{
			{Path: "test.txt", Hunks: []Hunk{{OldStart: 1, NewStart: 1}}},
		},
	}

	comment := AIComment{
		Severity:   AISeverityWarning,
		Message:    "Consider using a constant",
		Suggestion: "const NAME = 'value'",
	}

	s.AddAIComment(&diff, 0, 0, comment)

	if len(diff.Files[0].Hunks[0].AIComments) != 1 {
		t.Error("expected 1 AI comment")
	}
	if diff.Files[0].Hunks[0].AIComments[0].Message != "Consider using a constant" {
		t.Error("AI comment message mismatch")
	}
}

func TestDiffService_AddInlineComment(t *testing.T) {
	s := NewDiffService()
	diff := MultiFileDiff{
		Files: []FileDiff{
			{Path: "test.txt", Hunks: []Hunk{{
				Lines: []DiffLine{{Type: DiffLineAdded, Content: "new line"}},
			}}},
		},
	}

	comment := InlineComment{
		Author: "user",
		Body:   "This looks good",
	}

	s.AddInlineComment(&diff, 0, 0, 0, comment)

	if len(diff.Files[0].Hunks[0].Lines[0].Comments) != 1 {
		t.Error("expected 1 inline comment")
	}
}

func TestDiffService_ApplyFile(t *testing.T) {
	s := NewDiffService()
	fd := FileDiff{
		Path:       "test.txt",
		OldContent: "old content",
		NewContent: "new content",
	}

	result := s.ApplyFile(fd)
	if result != "new content" {
		t.Errorf("expected 'new content', got %s", result)
	}
}

func TestDiffService_ApplyAll(t *testing.T) {
	s := NewDiffService()
	diff := MultiFileDiff{
		Files: []FileDiff{
			{Path: "a.txt", NewContent: "content-a"},
			{Path: "b.txt", NewContent: "content-b"},
		},
	}

	result := s.ApplyAll(diff)
	if result["a.txt"] != "content-a" {
		t.Error("a.txt content mismatch")
	}
	if result["b.txt"] != "content-b" {
		t.Error("b.txt content mismatch")
	}
}

func TestRejectHunk_PreservesOtherHunks(t *testing.T) {
	s := NewDiffService()
	oldContent := strings.Join([]string{
		"line 01", "line 02", "line 03", "line 04",
		"line 05", "line 06", "line 07", "line 08",
		"line 09", "line 10", "line 11", "line 12",
		"line 13", "line 14", "line 15", "line 16",
	}, "\n")
	newContent := strings.Join([]string{
		"line 01", "line 02 changed", "line 03", "line 04",
		"line 05", "line 06", "line 07", "line 08",
		"line 09", "line 10", "line 11", "line 12",
		"line 13", "line 14", "line 15 changed", "line 16",
	}, "\n")
	want := strings.Join([]string{
		"line 01", "line 02", "line 03", "line 04",
		"line 05", "line 06", "line 07", "line 08",
		"line 09", "line 10", "line 11", "line 12",
		"line 13", "line 14", "line 15 changed", "line 16",
	}, "\n")

	fd := s.ComputeFileDiff("test.txt", oldContent, newContent)
	if len(fd.Hunks) != 2 {
		t.Fatalf("expected two non-adjacent hunks, got %d", len(fd.Hunks))
	}
	if got := s.RejectHunk(fd, 0); got != want {
		t.Fatalf("RejectHunk content mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestRejectHunk_PreservesEOFNewlineFromRetainedHunk(t *testing.T) {
	oldBody := strings.Join([]string{
		"line 01", "line 02", "line 03", "line 04",
		"line 05", "line 06", "line 07", "line 08",
		"line 09", "line 10", "line 11", "line 12",
		"line 13", "line 14", "line 15", "line 16",
	}, "\n")
	newBody := strings.Join([]string{
		"line 01", "line 02 changed", "line 03", "line 04",
		"line 05", "line 06", "line 07", "line 08",
		"line 09", "line 10", "line 11", "line 12",
		"line 13", "line 14", "line 15 changed", "line 16",
	}, "\n")
	keepEOFBody := strings.Replace(oldBody, "line 15", "line 15 changed", 1)
	keepFirstBody := strings.Replace(oldBody, "line 02", "line 02 changed", 1)

	tests := []struct {
		name       string
		oldSuffix  string
		newSuffix  string
		rejectHunk int
		wantBody   string
		wantSuffix string
	}{
		{name: "retained EOF hunk adds newline", newSuffix: "\n", rejectHunk: 0, wantBody: keepEOFBody, wantSuffix: "\n"},
		{name: "retained EOF hunk removes newline", oldSuffix: "\n", rejectHunk: 0, wantBody: keepEOFBody},
		{name: "rejected EOF hunk does not add newline", newSuffix: "\n", rejectHunk: 1, wantBody: keepFirstBody},
		{name: "rejected EOF hunk does not remove newline", oldSuffix: "\n", rejectHunk: 1, wantBody: keepFirstBody, wantSuffix: "\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fd := NewDiffService().ComputeFileDiff("test.txt", oldBody+tt.oldSuffix, newBody+tt.newSuffix)
			if len(fd.Hunks) != 2 {
				t.Fatalf("expected two non-adjacent hunks, got %d", len(fd.Hunks))
			}
			want := tt.wantBody + tt.wantSuffix
			if got := NewDiffService().RejectHunk(fd, tt.rejectHunk); got != want {
				t.Fatalf("RejectHunk content mismatch:\n got: %q\nwant: %q", got, want)
			}
		})
	}
}

func TestDiffService_RejectFile(t *testing.T) {
	s := NewDiffService()
	fd := FileDiff{
		Path:       "test.txt",
		OldContent: "old content",
		NewContent: "new content",
	}

	result := s.RejectFile(fd)
	if result != "old content" {
		t.Errorf("expected 'old content', got %s", result)
	}
}

func TestDiffService_RejectAll(t *testing.T) {
	s := NewDiffService()
	diff := MultiFileDiff{
		Files: []FileDiff{
			{Path: "a.txt", OldContent: "old-a", NewContent: "new-a"},
			{Path: "b.txt", OldContent: "old-b", NewContent: "new-b"},
		},
	}

	result := s.RejectAll(diff)
	if result["a.txt"] != "old-a" {
		t.Error("a.txt content mismatch")
	}
	if result["b.txt"] != "old-b" {
		t.Error("b.txt content mismatch")
	}
}

func TestDiffService_ReviewPR(t *testing.T) {
	s := NewDiffService()
	diff := MultiFileDiff{
		Files: []FileDiff{
			{Path: "a.go", AddedLines: 10, RemovedLines: 5},
		},
		TotalAdded:   10,
		TotalRemoved: 5,
	}
	reviews := []FileReview{
		{
			Path: "a.go",
			Comments: []AIComment{
				{Severity: AISeverityCritical, Message: "SQL injection risk"},
				{Severity: AISeverityWarning, Message: "Missing error handling"},
				{Severity: AISeverityInfo, Message: "Good naming"},
			},
		},
	}

	result := s.ReviewPR(diff, reviews)

	if result.Stats.FilesReviewed != 1 {
		t.Errorf("expected 1 file reviewed, got %d", result.Stats.FilesReviewed)
	}
	if result.Stats.TotalComments != 3 {
		t.Errorf("expected 3 comments, got %d", result.Stats.TotalComments)
	}
	if result.Stats.Critical != 1 {
		t.Errorf("expected 1 critical, got %d", result.Stats.Critical)
	}
	if result.Stats.Warnings != 1 {
		t.Errorf("expected 1 warning, got %d", result.Stats.Warnings)
	}
}

func TestDiffService_ExportMarkdown(t *testing.T) {
	s := NewDiffService()
	diff := MultiFileDiff{
		Files: []FileDiff{
			{
				Path:         "test.go",
				OldContent:   "old",
				NewContent:   "new",
				AddedLines:   1,
				RemovedLines: 1,
				Hunks: []Hunk{
					{
						OldStart: 1, OldCount: 1, NewStart: 1, NewCount: 1,
						Lines: []DiffLine{
							{Type: DiffLineRemoved, Content: "old"},
							{Type: DiffLineAdded, Content: "new"},
						},
					},
				},
			},
		},
		TotalAdded: 1, TotalRemoved: 1,
	}

	md := s.ExportMarkdown(diff, nil)

	if !strings.Contains(md, "# Diff Review Report") {
		t.Error("expected markdown header")
	}
	if !strings.Contains(md, "test.go") {
		t.Error("expected file path in markdown")
	}
	if !strings.Contains(md, "```diff") {
		t.Error("expected diff code block")
	}
}

func TestDiffService_ExportUnifiedDiff(t *testing.T) {
	s := NewDiffService()
	diff := MultiFileDiff{
		Files: []FileDiff{
			{
				Path: "test.go",
				Hunks: []Hunk{
					{
						OldStart: 1, OldCount: 1, NewStart: 1, NewCount: 1,
						Lines: []DiffLine{
							{Type: DiffLineRemoved, Content: "old"},
							{Type: DiffLineAdded, Content: "new"},
						},
					},
				},
			},
		},
	}

	ud := s.ExportUnifiedDiff(diff)

	if !strings.Contains(ud, "--- a/test.go") {
		t.Error("expected --- a/ prefix")
	}
	if !strings.Contains(ud, "+++ b/test.go") {
		t.Error("expected +++ b/ prefix")
	}
	if !strings.Contains(ud, "-old") {
		t.Error("expected -old line")
	}
	if !strings.Contains(ud, "+new") {
		t.Error("expected +new line")
	}
}

func TestDiffService_ExportHTML(t *testing.T) {
	s := NewDiffService()
	diff := MultiFileDiff{
		Files: []FileDiff{
			{
				Path: "test.go",
				Hunks: []Hunk{
					{
						OldStart: 1, OldCount: 1, NewStart: 1, NewCount: 1,
						Lines: []DiffLine{
							{Type: DiffLineAdded, Content: "new"},
						},
					},
				},
			},
		},
	}

	html := s.ExportHTML(diff)

	if !strings.Contains(html, "<!DOCTYPE html>") {
		t.Error("expected DOCTYPE")
	}
	if !strings.Contains(html, "class=\"added\"") {
		t.Error("expected added class")
	}
}

func TestDiffService_HunkLineNumbers(t *testing.T) {
	s := NewDiffService()
	old := "line1\nline2\nline3"
	new := "line1\nline2-modified\nline3"

	fd := s.ComputeFileDiff("test.txt", old, new)

	// 验证 hunk 行号被正确设置
	for _, h := range fd.Hunks {
		for _, l := range h.Lines {
			switch l.Type {
			case DiffLineContext:
				if l.OldNum == 0 || l.NewNum == 0 {
					t.Errorf("context line should have oldNum and newNum, got %d/%d", l.OldNum, l.NewNum)
				}
			case DiffLineAdded:
				if l.NewNum == 0 {
					t.Error("added line should have newNum")
				}
			case DiffLineRemoved:
				if l.OldNum == 0 {
					t.Error("removed line should have oldNum")
				}
			}
		}
	}
}

func TestSeverityEmoji(t *testing.T) {
	cases := map[AICommentSeverity]string{
		AISeverityCritical: "🔴",
		AISeverityError:    "🟠",
		AISeverityWarning:  "🟡",
		AISeverityInfo:     "🔵",
	}
	for sev, expected := range cases {
		if got := severityEmoji(sev); got != expected {
			t.Errorf("severityEmoji(%s) = %s, expected %s", sev, got, expected)
		}
	}
}

func TestEscapeHTML(t *testing.T) {
	if got := escapeHTML("<script>"); !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("escapeHTML failed: %s", got)
	}
	if got := escapeHTML("a&b"); !strings.Contains(got, "a&amp;b") {
		t.Errorf("escapeHTML failed: %s", got)
	}
}
