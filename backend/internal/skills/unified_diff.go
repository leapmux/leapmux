package skills

import (
	"fmt"
	"slices"
	"strings"
)

// --- Pure-Go unified diff (Myers shortest edit script) ---

const unifiedDiffContextLines = 3

type diffOp uint8

const (
	diffEqual diffOp = iota
	diffDelete
	diffInsert
)

type diffEdit struct {
	op   diffOp
	line string
}

// unifiedDiff produces a GNU unified diff between oldText (fromName) and
// newText (toName) with 3 lines of context. It returns "" when both texts are
// byte-identical.
func unifiedDiff(fromName, toName, oldText, newText string) string {
	if oldText == newText {
		return ""
	}
	oldLines := splitLinesKeepNewline(oldText)
	newLines := splitLinesKeepNewline(newText)
	edits := computeDiffEdits(oldLines, newLines)

	type indexedEdit struct {
		op   diffOp
		line string
		oldN int // 1-based line number in oldLines (0 if insert before line 1)
		newN int // 1-based line number in newLines (0 if delete before line 1)
	}
	seq := make([]indexedEdit, 0, len(edits))
	oldPos, newPos := 1, 1
	for _, e := range edits {
		switch e.op {
		case diffEqual:
			seq = append(seq, indexedEdit{op: diffEqual, line: e.line, oldN: oldPos, newN: newPos})
			oldPos++
			newPos++
		case diffDelete:
			seq = append(seq, indexedEdit{op: diffDelete, line: e.line, oldN: oldPos, newN: newPos})
			oldPos++
		case diffInsert:
			seq = append(seq, indexedEdit{op: diffInsert, line: e.line, oldN: oldPos, newN: newPos})
			newPos++
		}
	}

	// Locate maximal hunk spans around changed edits, merging spans whose
	// context windows touch or overlap.
	type hunkSpan struct {
		start int
		end   int // exclusive
	}
	var spans []hunkSpan
	for i, e := range seq {
		if e.op == diffEqual {
			continue
		}
		lo := max(0, i-unifiedDiffContextLines)
		hi := min(len(seq), i+unifiedDiffContextLines+1)
		if len(spans) > 0 && lo <= spans[len(spans)-1].end {
			spans[len(spans)-1].end = hi
		} else {
			spans = append(spans, hunkSpan{start: lo, end: hi})
		}
	}
	if len(spans) == 0 {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "--- %s\n+++ %s\n", fromName, toName)
	for _, sp := range spans {
		oldStart, oldCount := 0, 0
		newStart, newCount := 0, 0
		for _, e := range seq[sp.start:sp.end] {
			if e.op != diffInsert {
				if oldCount == 0 {
					oldStart = e.oldN
				}
				oldCount++
			}
			if e.op != diffDelete {
				if newCount == 0 {
					newStart = e.newN
				}
				newCount++
			}
		}
		if oldCount == 0 {
			if len(oldLines) == 0 {
				oldStart = 0
			} else {
				oldStart = seq[sp.start].oldN - 1
			}
		}
		if newCount == 0 {
			if len(newLines) == 0 {
				newStart = 0
			} else {
				newStart = seq[sp.start].newN - 1
			}
		}
		fmt.Fprintf(&b, "@@ -%s +%s @@\n", formatHunkRange(oldStart, oldCount), formatHunkRange(newStart, newCount))
		for _, e := range seq[sp.start:sp.end] {
			var prefix byte
			switch e.op {
			case diffEqual:
				prefix = ' '
			case diffDelete:
				prefix = '-'
			case diffInsert:
				prefix = '+'
			}
			hasNL := strings.HasSuffix(e.line, "\n")
			b.WriteByte(prefix)
			b.WriteString(strings.TrimSuffix(e.line, "\n"))
			b.WriteByte('\n')
			if !hasNL {
				b.WriteString("\\ No newline at end of file\n")
			}
		}
	}
	return b.String()
}

func formatHunkRange(start, count int) string {
	if count == 1 {
		return fmt.Sprintf("%d", start)
	}
	return fmt.Sprintf("%d,%d", start, count)
}

func splitLinesKeepNewline(s string) []string {
	if s == "" {
		return nil
	}
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i+1])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func computeDiffEdits(a, b []string) []diffEdit {
	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(a)-prefix && suffix < len(b)-prefix && a[len(a)-1-suffix] == b[len(b)-1-suffix] {
		suffix++
	}

	out := make([]diffEdit, 0, len(a)+len(b))
	for i := range prefix {
		out = append(out, diffEdit{op: diffEqual, line: a[i]})
	}

	aMid := a[prefix : len(a)-suffix]
	bMid := b[prefix : len(b)-suffix]
	switch {
	case len(aMid) == 0:
		for _, line := range bMid {
			out = append(out, diffEdit{op: diffInsert, line: line})
		}
	case len(bMid) == 0:
		for _, line := range aMid {
			out = append(out, diffEdit{op: diffDelete, line: line})
		}
	default:
		out = append(out, myersDiff(aMid, bMid)...)
	}

	for i := len(a) - suffix; i < len(a); i++ {
		out = append(out, diffEdit{op: diffEqual, line: a[i]})
	}
	return out
}

// maxDiffEdits caps the Myers edit distance before falling back to a
// whole-middle replacement so heavily diverged files cannot trigger O(D^2)
// trace memory growth.
const maxDiffEdits = 2048

func myersDiff(a, b []string) []diffEdit {
	n, m := len(a), len(b)
	if n-m > maxDiffEdits || m-n > maxDiffEdits {
		return fallbackReplaceEdits(a, b)
	}
	limit := min(n+m, maxDiffEdits)
	offset := limit
	v := make([]int, 2*limit+1)
	trace := make([][]int, 0, min(limit+1, 64))

	for d := 0; d <= limit; d++ {
		for k := -d; k <= d; k += 2 {
			var x int
			switch {
			case d == 0:
				x = 0
			case k == -d || (k != d && v[offset+k-1] < v[offset+k+1]):
				x = v[offset+k+1]
			default:
				x = v[offset+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[offset+k] = x
			if x >= n && y >= m {
				snap := make([]int, 2*d+1)
				copy(snap, v[offset-d:offset+d+1])
				trace = append(trace, snap)
				return backtrackMyers(a, b, trace)
			}
		}
		snap := make([]int, 2*d+1)
		copy(snap, v[offset-d:offset+d+1])
		trace = append(trace, snap)
	}
	return fallbackReplaceEdits(a, b)
}

func fallbackReplaceEdits(a, b []string) []diffEdit {
	edits := make([]diffEdit, 0, len(a)+len(b))
	for _, line := range a {
		edits = append(edits, diffEdit{op: diffDelete, line: line})
	}
	for _, line := range b {
		edits = append(edits, diffEdit{op: diffInsert, line: line})
	}
	return edits
}

func backtrackMyers(a, b []string, trace [][]int) []diffEdit {
	x, y := len(a), len(b)
	rev := make([]diffEdit, 0, len(a)+len(b))

	for d := len(trace) - 1; d > 0; d-- {
		prev := trace[d-1]
		k := x - y
		var prevK int
		if k == -d || (k != d && prev[k-1+(d-1)] < prev[k+1+(d-1)]) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := prev[prevK+(d-1)]
		prevY := prevX - prevK

		for x > prevX && y > prevY {
			x--
			y--
			rev = append(rev, diffEdit{op: diffEqual, line: a[x]})
		}
		if x == prevX {
			rev = append(rev, diffEdit{op: diffInsert, line: b[prevY]})
		} else {
			rev = append(rev, diffEdit{op: diffDelete, line: a[prevX]})
		}
		x, y = prevX, prevY
	}
	for x > 0 && y > 0 {
		x--
		y--
		rev = append(rev, diffEdit{op: diffEqual, line: a[x]})
	}
	slices.Reverse(rev)
	return rev
}
