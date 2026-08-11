package services

import (
	"runtime"
	"strings"
	"testing"
)

func TestMyersDiff_IdenticalTexts(t *testing.T) {
	text := "line1\nline2\nline3\n"
	diff := myersDiff("file.txt", text, text)
	// Should have the header but no - or + lines.
	if !strings.Contains(diff, "diff --git") {
		t.Errorf("missing diff header")
	}
	if strings.Contains(diff, "-") && strings.Contains(diff, "+") {
		// Check there are no actual change lines (only context " " lines).
		for _, line := range strings.Split(diff, "\n") {
			if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
				t.Errorf("unexpected delete line in identical diff: %q", line)
			}
			if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
				t.Errorf("unexpected insert line in identical diff: %q", line)
			}
		}
	}
}

func TestMyersDiff_EmptyToContent(t *testing.T) {
	old := ""
	new := "line1\nline2\n"
	diff := myersDiff("file.txt", old, new)
	if !strings.Contains(diff, "+line1") {
		t.Errorf("expected +line1 in diff: %s", diff)
	}
	if !strings.Contains(diff, "+line2") {
		t.Errorf("expected +line2 in diff: %s", diff)
	}
}

func TestMyersDiff_ContentToEmpty(t *testing.T) {
	old := "line1\nline2\n"
	new := ""
	diff := myersDiff("file.txt", old, new)
	if !strings.Contains(diff, "-line1") {
		t.Errorf("expected -line1 in diff: %s", diff)
	}
	if !strings.Contains(diff, "-line2") {
		t.Errorf("expected -line2 in diff: %s", diff)
	}
}

func TestMyersDiff_InsertInMiddle(t *testing.T) {
	old := "line1\nline2\nline3\n"
	new := "line1\ninserted\nline2\nline3\n"
	diff := myersDiff("file.txt", old, new)
	if !strings.Contains(diff, "+inserted") {
		t.Errorf("expected +inserted in diff: %s", diff)
	}
	// Should NOT delete line1 or line2 — they're unchanged.
	if strings.Contains(diff, "-line1\n") {
		t.Errorf("should not delete line1 (it's unchanged): %s", diff)
	}
	if strings.Contains(diff, "-line2\n") {
		t.Errorf("should not delete line2 (it's unchanged): %s", diff)
	}
}

func TestMyersDiff_DeleteInMiddle(t *testing.T) {
	old := "line1\ndeleted\nline2\nline3\n"
	new := "line1\nline2\nline3\n"
	diff := myersDiff("file.txt", old, new)
	if !strings.Contains(diff, "-deleted") {
		t.Errorf("expected -deleted in diff: %s", diff)
	}
	// Should NOT delete line1, line2, or line3 — they're unchanged.
	if strings.Contains(diff, "-line1\n") {
		t.Errorf("should not delete line1: %s", diff)
	}
	if strings.Contains(diff, "-line2\n") {
		t.Errorf("should not delete line2: %s", diff)
	}
}

func TestMyersDiff_ModifyLine(t *testing.T) {
	old := "line1\nold line\nline3\n"
	new := "line1\nnew line\nline3\n"
	diff := myersDiff("file.txt", old, new)
	if !strings.Contains(diff, "-old line") {
		t.Errorf("expected -old line in diff: %s", diff)
	}
	if !strings.Contains(diff, "+new line") {
		t.Errorf("expected +new line in diff: %s", diff)
	}
	// line1 and line3 should be context, not deleted.
	if strings.Contains(diff, "-line1\n") {
		t.Errorf("should not delete line1: %s", diff)
	}
	if strings.Contains(diff, "-line3\n") {
		t.Errorf("should not delete line3: %s", diff)
	}
}

func TestMyersDiff_BothEmpty(t *testing.T) {
	diff := myersDiff("file.txt", "", "")
	if !strings.Contains(diff, "diff --git") {
		t.Errorf("missing diff header")
	}
	// Should have no change lines.
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			t.Errorf("unexpected delete line: %q", line)
		}
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			t.Errorf("unexpected insert line: %q", line)
		}
	}
}

func TestMyersDiff_HasHunkHeader(t *testing.T) {
	old := "line1\nline2\n"
	new := "line1\nchanged\n"
	diff := myersDiff("file.txt", old, new)
	if !strings.Contains(diff, "@@ -") {
		t.Errorf("expected hunk header @@ -... in diff: %s", diff)
	}
}

func TestMyersDiff_ContextLinesIncluded(t *testing.T) {
	// With 3 lines of context, unchanged lines adjacent to changes
	// should appear as context (" " prefix).
	old := "ctx1\nctx2\nctx3\nchanged\nctx4\nctx5\nctx6\n"
	new := "ctx1\nctx2\nctx3\nmodified\nctx4\nctx5\nctx6\n"
	diff := myersDiff("file.txt", old, new)
	// Context lines should appear with " " prefix.
	if !strings.Contains(diff, " ctx1") {
		t.Errorf("expected ctx1 as context: %s", diff)
	}
	if !strings.Contains(diff, " ctx6") {
		t.Errorf("expected ctx6 as context: %s", diff)
	}
}

func TestMyersDiff_MultipleChanges(t *testing.T) {
	old := "a\nb\nc\nd\ne\n"
	new := "a\nB\nc\nD\ne\n"
	diff := myersDiff("file.txt", old, new)
	if !strings.Contains(diff, "-b") {
		t.Errorf("expected -b: %s", diff)
	}
	if !strings.Contains(diff, "+B") {
		t.Errorf("expected +B: %s", diff)
	}
	if !strings.Contains(diff, "-d") {
		t.Errorf("expected -d: %s", diff)
	}
	if !strings.Contains(diff, "+D") {
		t.Errorf("expected +D: %s", diff)
	}
	// a, c, e should be context.
	if strings.Contains(diff, "-a\n") {
		t.Errorf("should not delete a: %s", diff)
	}
	if strings.Contains(diff, "-c\n") {
		t.Errorf("should not delete c: %s", diff)
	}
	if strings.Contains(diff, "-e\n") {
		t.Errorf("should not delete e: %s", diff)
	}
}

func TestMyersDiff_LargeFile(t *testing.T) {
	// Generate a large file with a small change in the middle.
	var oldLines, newLines []string
	for i := 0; i < 100; i++ {
		if i == 50 {
			oldLines = append(oldLines, "old line")
			newLines = append(newLines, "new line")
		} else {
			line := "line" + string(rune('a'+(i%26)))
			oldLines = append(oldLines, line)
			newLines = append(newLines, line)
		}
	}
	old := strings.Join(oldLines, "\n")
	new := strings.Join(newLines, "\n")
	diff := myersDiff("file.txt", old, new)
	if !strings.Contains(diff, "-old line") {
		t.Errorf("expected -old line: %s", diff)
	}
	if !strings.Contains(diff, "+new line") {
		t.Errorf("expected +new line: %s", diff)
	}
	// The diff should be compact (not 100 delete + 100 insert).
	deleteCount := strings.Count(diff, "\n-")
	insertCount := strings.Count(diff, "\n+")
	if deleteCount > 5 || insertCount > 5 {
		t.Errorf("diff too large: %d deletes, %d inserts (expected ~1 each)", deleteCount, insertCount)
	}
}

func TestSplitLines(t *testing.T) {
	cases := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"one line", 1},
		{"line1\nline2", 2},
		{"line1\nline2\n", 2}, // trailing newline stripped
		{"line1\nline2\n\n", 3},
	}
	for _, c := range cases {
		got := splitLines(c.input)
		if len(got) != c.want {
			t.Errorf("splitLines(%q) = %d lines, want %d: %v", c.input, len(got), c.want, got)
		}
	}
}

func TestComputeDiff_AllEqual(t *testing.T) {
	old := []string{"a", "b", "c"}
	new := []string{"a", "b", "c"}
	ops := computeDiff(old, new)
	for _, op := range ops {
		if op.kind != diffEqual {
			t.Errorf("expected all equal ops, got %v", op.kind)
		}
	}
}

func TestComputeDiff_AllInsert(t *testing.T) {
	old := []string{}
	new := []string{"a", "b", "c"}
	ops := computeDiff(old, new)
	for _, op := range ops {
		if op.kind != diffInsert {
			t.Errorf("expected all insert ops, got %v", op.kind)
		}
	}
}

func TestComputeDiff_AllDelete(t *testing.T) {
	old := []string{"a", "b", "c"}
	new := []string{}
	ops := computeDiff(old, new)
	for _, op := range ops {
		if op.kind != diffDelete {
			t.Errorf("expected all delete ops, got %v", op.kind)
		}
	}
}

func TestComputeDiff_LargeDUsesLinearSpace(t *testing.T) {
	const lineCount = 960
	payload := strings.Repeat("x", 1100)
	oldLines := make([]string, lineCount)
	newLines := make([]string, lineCount)
	for i := range lineCount {
		oldLines[i] = "old-" + payload
		newLines[i] = "new-" + payload
	}

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	ops := computeDiff(oldLines, newLines)
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(ops)

	const maxAllocated = 16 << 20
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > maxAllocated {
		t.Fatalf("computeDiff allocated %d bytes, want at most %d", allocated, maxAllocated)
	}

	got := make([]string, 0, len(newLines))
	for _, op := range ops {
		switch op.kind {
		case diffEqual, diffInsert:
			got = append(got, newLines[op.newIdx])
		case diffDelete:
			// Deleted lines are not part of the reconstructed result.
		default:
			t.Fatalf("unexpected diff kind %d", op.kind)
		}
	}
	if len(got) != len(newLines) {
		t.Fatalf("reconstructed %d lines, want %d", len(got), len(newLines))
	}
	for i := range got {
		if got[i] != newLines[i] {
			t.Fatalf("reconstructed line %d does not match new input", i)
		}
	}
}

func BenchmarkComputeDiff_LargeFile(b *testing.B) {
	const lineCount = 12_000
	payload := strings.Repeat("x", 96)
	oldLines := make([]string, lineCount)
	newLines := make([]string, lineCount)
	for i := range lineCount {
		line := payload + string(rune('a'+i%26))
		oldLines[i] = line
		newLines[i] = line
	}
	newLines[lineCount/4] = "changed-a-" + payload
	newLines[lineCount/2] = "changed-b-" + payload
	newLines[3*lineCount/4] = "changed-c-" + payload

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		ops := computeDiff(oldLines, newLines)
		runtime.KeepAlive(ops)
	}
}

// Proposal AJ: verify hunk header line numbers are exactly correct.
// Old has 3 lines, new has 4 lines (one insertion at line 2). The hunk
// header should be "@@ -1,3 +1,4 @@".
func TestMyersDiff_AJ_HunkHeaderLineNumbers(t *testing.T) {
	old := "line1\nline2\nline3\n"
	new := "line1\nINSERTED\nline2\nline3\n"
	diff := myersDiff("file.txt", old, new)
	if !strings.Contains(diff, "@@ -1,3 +1,4 @@") {
		t.Errorf("expected hunk header '@@ -1,3 +1,4 @@', got: %s", diff)
	}
}

// Proposal AJ: two changes far apart should produce two separate hunks,
// each with its own @@ header. Lines 1-3 and lines 8-10 are changed,
// with enough context separation (lines 4-7 unchanged) to split into
// two hunks (default context is 3 lines).
func TestMyersDiff_AJ_MultipleHunks(t *testing.T) {
	old := "l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nl9\nl10\n"
	new := "L1\nL2\nL3\nl4\nl5\nl6\nl7\nL8\nL9\nL10\n"
	diff := myersDiff("file.txt", old, new)
	// Count the number of hunk headers.
	count := strings.Count(diff, "\n@@ -")
	// The first @@ doesn't have a preceding \n, so add 1 if there's at
	// least one hunk.
	if strings.Contains(diff, "@@ -") {
		count++
	}
	if count < 2 {
		t.Errorf("expected at least 2 hunks for far-apart changes, got %d: %s", count, diff)
	}
}

// Proposal AJ: input without a trailing newline should still produce a
// valid diff. The last line should appear in the diff even though it
// lacks a \n.
func TestMyersDiff_AJ_NoTrailingNewline(t *testing.T) {
	old := "line1\nline2"
	new := "line1\nchanged"
	diff := myersDiff("file.txt", old, new)
	if !strings.Contains(diff, "-line2") {
		t.Errorf("expected '-line2' in diff: %s", diff)
	}
	if !strings.Contains(diff, "+changed") {
		t.Errorf("expected '+changed' in diff: %s", diff)
	}
}

// Proposal AJ: lines starting with diff-special prefixes (+, -, @@)
// should not confuse the diff output. The diff should still be a valid
// unified diff that can be parsed by standard tools.
func TestMyersDiff_AJ_DiffSpecialPrefixLines(t *testing.T) {
	// Old content has a line starting with "+" and a line starting with "-".
	old := "+ original\n- original\n"
	// New content changes them.
	new := "+ modified\n- modified\n"
	diff := myersDiff("file.txt", old, new)
	// The diff itself should contain the original lines as deletions
	// (prefixed with another "-") and the new lines as additions
	// (prefixed with "+").
	if !strings.Contains(diff, "-+ original") {
		t.Errorf("expected '-+ original' (deleted line starting with +): %s", diff)
	}
	if !strings.Contains(diff, "++ modified") {
		t.Errorf("expected '++ modified' (added line starting with +): %s", diff)
	}
}

// Proposal AJ: single-line inputs (1 line old, 1 line new) should
// produce a correct diff without off-by-one errors.
func TestMyersDiff_AJ_SingleLineInputs(t *testing.T) {
	old := "only\n"
	new := "changed\n"
	diff := myersDiff("file.txt", old, new)
	if !strings.Contains(diff, "-only") {
		t.Errorf("expected '-only' in diff: %s", diff)
	}
	if !strings.Contains(diff, "+changed") {
		t.Errorf("expected '+changed' in diff: %s", diff)
	}
	if !strings.Contains(diff, "@@ -1,1 +1,1 @@") {
		t.Errorf("expected hunk header '@@ -1,1 +1,1 @@': %s", diff)
	}
}
