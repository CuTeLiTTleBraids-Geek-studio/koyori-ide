package services

import (
	"bytes"
	"fmt"
	"strings"
)

// myersDiff computes a unified diff between oldText and newText using the
// Myers diff algorithm (Plan 60 / N-27). This produces much cleaner diffs
// than the previous naive line-by-line comparison, which treated every
// changed line as a delete+insert pair even when a small edit was made
// within a line.
//
// The output follows the standard unified diff format:
//
//	diff --git a/<path> b/<path>
//	--- a/<path>
//	+++ b/<path>
//	 context line
//	-removed line
//	+added line
//
// The edit script is recovered with a linear-space LCS decomposition so
// large edit distances do not require retaining every search frontier.
func myersDiff(filePath string, oldText string, newText string) string {
	oldLines := splitLines(oldText)
	newLines := splitLines(newText)

	diff := computeDiff(oldLines, newLines)

	var buf bytes.Buffer
	buf.WriteString(fmt.Sprintf("diff --git a/%s b/%s\n", filePath, filePath))
	buf.WriteString(fmt.Sprintf("--- a/%s\n", filePath))
	buf.WriteString(fmt.Sprintf("+++ b/%s\n", filePath))

	// Generate hunks with 3 lines of context.
	writeHunks(&buf, diff, oldLines, newLines, 3)

	return buf.String()
}

// diffOp represents a single edit operation in the diff script.
type diffOp struct {
	kind   diffKind
	oldIdx int // index into oldLines (for equal and delete)
	newIdx int // index into newLines (for equal and insert)
}

type diffKind int

const (
	diffEqual  diffKind = iota
	diffDelete          // line present in old, not in new
	diffInsert          // line present in new, not in old
)

// computeDiff returns a shortest edit script using Hirschberg's linear-space
// LCS decomposition. Unlike trace-based Myers backtracking, it does not retain
// an O((N+M)^2) matrix for inputs with a large edit distance.
func computeDiff(oldLines, newLines []string) []diffOp {
	ops := make([]diffOp, 0, len(oldLines)+len(newLines))
	appendLinearDiff(&ops, oldLines, newLines, 0, 0)
	return ops
}

func appendLinearDiff(ops *[]diffOp, oldLines, newLines []string, oldStart, newStart int) {
	prefix := 0
	for prefix < len(oldLines) && prefix < len(newLines) && oldLines[prefix] == newLines[prefix] {
		*ops = append(*ops, diffOp{kind: diffEqual, oldIdx: oldStart + prefix, newIdx: newStart + prefix})
		prefix++
	}
	oldLines = oldLines[prefix:]
	newLines = newLines[prefix:]
	oldStart += prefix
	newStart += prefix

	suffix := 0
	for suffix < len(oldLines) && suffix < len(newLines) && oldLines[len(oldLines)-1-suffix] == newLines[len(newLines)-1-suffix] {
		suffix++
	}
	oldMiddle := oldLines[:len(oldLines)-suffix]
	newMiddle := newLines[:len(newLines)-suffix]

	switch {
	case len(oldMiddle) == 0:
		for i := range newMiddle {
			*ops = append(*ops, diffOp{kind: diffInsert, oldIdx: -1, newIdx: newStart + i})
		}
	case len(newMiddle) == 0:
		for i := range oldMiddle {
			*ops = append(*ops, diffOp{kind: diffDelete, oldIdx: oldStart + i, newIdx: -1})
		}
	case len(oldMiddle) == 1:
		match := -1
		for i := range newMiddle {
			if oldMiddle[0] == newMiddle[i] {
				match = i
				break
			}
		}
		if match < 0 {
			*ops = append(*ops, diffOp{kind: diffDelete, oldIdx: oldStart, newIdx: -1})
			for i := range newMiddle {
				*ops = append(*ops, diffOp{kind: diffInsert, oldIdx: -1, newIdx: newStart + i})
			}
			break
		}
		for i := 0; i < match; i++ {
			*ops = append(*ops, diffOp{kind: diffInsert, oldIdx: -1, newIdx: newStart + i})
		}
		*ops = append(*ops, diffOp{kind: diffEqual, oldIdx: oldStart, newIdx: newStart + match})
		for i := match + 1; i < len(newMiddle); i++ {
			*ops = append(*ops, diffOp{kind: diffInsert, oldIdx: -1, newIdx: newStart + i})
		}
	default:
		oldSplit := len(oldMiddle) / 2
		forward := lcsLengths(oldMiddle[:oldSplit], newMiddle, false)
		backward := lcsLengths(oldMiddle[oldSplit:], newMiddle, true)
		newSplit := 0
		best := -1
		for i := 0; i <= len(newMiddle); i++ {
			if score := forward[i] + backward[len(newMiddle)-i]; score > best {
				best = score
				newSplit = i
			}
		}
		appendLinearDiff(ops, oldMiddle[:oldSplit], newMiddle[:newSplit], oldStart, newStart)
		appendLinearDiff(ops, oldMiddle[oldSplit:], newMiddle[newSplit:], oldStart+oldSplit, newStart+newSplit)
	}

	for i := 0; i < suffix; i++ {
		*ops = append(*ops, diffOp{
			kind:   diffEqual,
			oldIdx: oldStart + len(oldMiddle) + i,
			newIdx: newStart + len(newMiddle) + i,
		})
	}
}

// lcsLengths returns LCS lengths for every prefix of b. When reverse is
// true, both inputs are traversed from the end, yielding suffix lengths.
func lcsLengths(a, b []string, reverse bool) []int {
	previous := make([]int, len(b)+1)
	current := make([]int, len(b)+1)
	for i := 0; i < len(a); i++ {
		current[0] = 0
		ai := i
		if reverse {
			ai = len(a) - 1 - i
		}
		for j := 1; j <= len(b); j++ {
			bj := j - 1
			if reverse {
				bj = len(b) - j
			}
			if a[ai] == b[bj] {
				current[j] = previous[j-1] + 1
			} else if previous[j] >= current[j-1] {
				current[j] = previous[j]
			} else {
				current[j] = current[j-1]
			}
		}
		previous, current = current, previous
	}
	return previous
}

// writeHunks groups the edit operations into unified-diff hunks with the
// given number of context lines, and writes them to buf.
func writeHunks(buf *bytes.Buffer, ops []diffOp, oldLines, newLines []string, context int) {
	if len(ops) == 0 {
		return
	}

	i := 0
	for i < len(ops) {
		// Find the next change (non-equal op).
		for i < len(ops) && ops[i].kind == diffEqual {
			i++
		}
		if i >= len(ops) {
			break
		}

		// Start of a hunk: go back `context` lines.
		hunkStart := i - context
		if hunkStart < 0 {
			hunkStart = 0
		}

		// Find the end of the hunk: scan forward until we've seen `context`
		// consecutive equal lines after the last change.
		j := i
		lastChange := i
		for j < len(ops) {
			if ops[j].kind != diffEqual {
				lastChange = j
				j++
			} else {
				// Count consecutive equal lines.
				eqStart := j
				for j < len(ops) && ops[j].kind == diffEqual {
					j++
				}
				eqCount := j - eqStart
				if eqCount >= context*2 || j >= len(ops) {
					// Enough context to split, or end of ops.
					break
				}
			}
		}

		// End of hunk: go forward `context` lines after the last change.
		hunkEnd := lastChange + context + 1
		if hunkEnd > len(ops) {
			hunkEnd = len(ops)
		}

		// Compute the hunk header (old start, old count, new start, new count).
		oldStart, oldCount, newStart, newCount := computeHunkBounds(ops, hunkStart, hunkEnd, oldLines, newLines)
		buf.WriteString(fmt.Sprintf("@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount))

		// Write the hunk lines.
		for k := hunkStart; k < hunkEnd; k++ {
			op := ops[k]
			switch op.kind {
			case diffEqual:
				buf.WriteString(" " + lineAt(oldLines, newLines, op) + "\n")
			case diffDelete:
				buf.WriteString("-" + oldLines[op.oldIdx] + "\n")
			case diffInsert:
				buf.WriteString("+" + newLines[op.newIdx] + "\n")
			}
		}

		i = hunkEnd
	}
}

// computeHunkBounds calculates the (oldStart, oldCount, newStart, newCount)
// for a hunk header, where starts are 1-based.
func computeHunkBounds(ops []diffOp, hunkStart, hunkEnd int, oldLines, newLines []string) (int, int, int, int) {
	oldStart, newStart := 0, 0
	if hunkStart < len(ops) {
		op := ops[hunkStart]
		if op.kind == diffEqual {
			oldStart = op.oldIdx
			newStart = op.newIdx
		} else if op.kind == diffDelete {
			oldStart = op.oldIdx
			// For delete at hunkStart, newStart is the newIdx of the
			// next equal or insert op, or 0 if none.
			newStart = findNewStart(ops, hunkStart)
		} else {
			newStart = op.newIdx
			oldStart = findOldStart(ops, hunkStart)
		}
	}
	if oldStart < 0 {
		oldStart = 0
	}
	if newStart < 0 {
		newStart = 0
	}

	oldCount, newCount := 0, 0
	for k := hunkStart; k < hunkEnd; k++ {
		op := ops[k]
		switch op.kind {
		case diffEqual:
			oldCount++
			newCount++
		case diffDelete:
			oldCount++
		case diffInsert:
			newCount++
		}
	}

	// 1-based starts.
	return oldStart + 1, oldCount, newStart + 1, newCount
}

func findNewStart(ops []diffOp, from int) int {
	for i := from; i < len(ops); i++ {
		if ops[i].kind == diffEqual || ops[i].kind == diffInsert {
			return ops[i].newIdx
		}
	}
	return 0
}

func findOldStart(ops []diffOp, from int) int {
	for i := from; i < len(ops); i++ {
		if ops[i].kind == diffEqual || ops[i].kind == diffDelete {
			return ops[i].oldIdx
		}
	}
	return 0
}

// lineAt returns the line content for an equal op (same in old and new).
func lineAt(oldLines, newLines []string, op diffOp) string {
	if op.oldIdx >= 0 && op.oldIdx < len(oldLines) {
		return oldLines[op.oldIdx]
	}
	if op.newIdx >= 0 && op.newIdx < len(newLines) {
		return newLines[op.newIdx]
	}
	return ""
}

// splitLines splits text into lines, preserving the content without the
// trailing newline (the diff writer re-adds "\n" for each line).
func splitLines(text string) []string {
	if text == "" {
		return []string{}
	}
	lines := strings.Split(text, "\n")
	// Remove the trailing empty string if the text ends with "\n".
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
