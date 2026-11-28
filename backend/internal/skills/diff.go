package skills

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// File status values reported in SkillFileDiff.Status.
const (
	FileDiffAdded     = "added"
	FileDiffRemoved   = "removed"
	FileDiffModified  = "modified"
	FileDiffUntracked = "untracked"
)

// SkillFileDiff describes the difference between the on-disk copy and the
// bundled catalog for one file inside a skill directory.
type SkillFileDiff struct {
	RelPath string `json:"rel_path"`
	Status  string `json:"status"`
	Diff    string `json:"diff,omitempty"`
}

// SkillDiff describes the on-disk state and per-file unified diffs of one
// bundled skill in a target directory.
type SkillDiff struct {
	Name   string          `json:"name"`
	Target string          `json:"target,omitempty"`
	State  State           `json:"state"`
	Path   string          `json:"path"`
	Files  []SkillFileDiff `json:"files,omitempty"`
}

// DiffOne inspects one bundled skill in targetDir under *os.Root confinement
// and produces unified diffs comparing the on-disk files (a/) against the
// bundled catalog (b/). Unknown skills return ErrUnknownSkill and symlinks
// return ErrUnsafeSymlink.
func DiffOne(targetDir, name string) (SkillDiff, error) {
	s, ok := Lookup(name)
	if !ok {
		return SkillDiff{}, fmt.Errorf("%w %q (available: %s)", ErrUnknownSkill, strings.TrimSpace(name), strings.Join(Names(), ", "))
	}

	targetRoot, openErr := os.OpenRoot(targetDir)
	if openErr != nil {
		if errors.Is(openErr, os.ErrNotExist) {
			return diffNotInstalled(targetDir, s), nil
		}
		return SkillDiff{}, fmt.Errorf("open skills directory %s: %w", targetDir, openErr)
	}
	defer func() { _ = targetRoot.Close() }()

	return diffOneInTargetRoot(targetRoot, targetDir, s)
}

// DiffAll computes diffs for the requested bundled skills in targetDir. When
// names is empty, every bundled skill in Catalog() is diffed.
func DiffAll(targetDir string, names []string) ([]SkillDiff, error) {
	var selected []Skill
	if len(names) == 0 {
		selected = Catalog()
	} else {
		var err error
		selected, err = resolveSkillList(names)
		if err != nil {
			return nil, err
		}
	}

	targetRoot, openErr := os.OpenRoot(targetDir)
	if openErr != nil {
		if errors.Is(openErr, os.ErrNotExist) {
			out := make([]SkillDiff, len(selected))
			for i, s := range selected {
				out[i] = diffNotInstalled(targetDir, s)
			}
			return out, nil
		}
		return nil, fmt.Errorf("open skills directory %s: %w", targetDir, openErr)
	}
	defer func() { _ = targetRoot.Close() }()

	out := make([]SkillDiff, 0, len(selected))
	for _, s := range selected {
		d, err := diffOneInTargetRoot(targetRoot, targetDir, s)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

func diffNotInstalled(targetDir string, s Skill) SkillDiff {
	files := make([]SkillFileDiff, 0, len(s.Files))
	for _, f := range s.Files {
		files = append(files, SkillFileDiff{
			RelPath: f.RelPath,
			Status:  FileDiffAdded,
			Diff:    unifiedDiff("/dev/null", "b/"+f.RelPath, "", f.Content),
		})
	}
	return SkillDiff{
		Name:  s.Name,
		State: StateNotInstalled,
		Path:  filepath.Join(targetDir, s.Name, "SKILL.md"),
		Files: files,
	}
}

func diffOneInTargetRoot(targetRoot *os.Root, targetDir string, s Skill) (SkillDiff, error) {
	skillRoot, exists, openErr := openSkillRoot(targetRoot, s.Name)
	if openErr != nil {
		return SkillDiff{}, openErr
	}
	if !exists {
		return diffNotInstalled(targetDir, s), nil
	}
	defer func() { _ = skillRoot.Close() }()

	// Verify the marker path is not a symlink before inspecting state.
	if _, mPathErr := checkSafeManagedInRoot(skillRoot, MarkerFileName); mPathErr != nil && errors.Is(mPathErr, ErrUnsafeSymlink) {
		return SkillDiff{}, mPathErr
	}

	// Walk the skill directory under *os.Root confinement to reject any symlink
	// and collect regular non-dot files on disk.
	diskFiles, walkErr := collectSkillDiskFiles(skillRoot)
	if walkErr != nil {
		return SkillDiff{}, walkErr
	}

	st := inspectSkillRoot(skillRoot, s, defaultSkillStatus(targetDir, s))
	if st.State == StateNotInstalled {
		return diffNotInstalled(targetDir, s), nil
	}

	marker, markerErr := readSkillMarkerInRoot(skillRoot, s.Name)
	if markerErr != nil && errors.Is(markerErr, ErrUnsafeSymlink) {
		return SkillDiff{}, markerErr
	}

	bundledByPath := make(map[string]SkillFile, len(s.Files))
	candidateSet := make(map[string]struct{}, len(s.Files)+len(diskFiles))
	for _, f := range s.Files {
		bundledByPath[f.RelPath] = f
		candidateSet[f.RelPath] = struct{}{}
	}

	trackedSet := make(map[string]bool)
	if marker != nil {
		for rel := range marker.Files {
			cleanRel, cleanErr := cleanManagedRelPath(rel)
			if cleanErr != nil {
				continue
			}
			if _, safeErr := checkSafeManagedInRoot(skillRoot, cleanRel); safeErr != nil && errors.Is(safeErr, ErrUnsafeSymlink) {
				return SkillDiff{}, safeErr
			}
			trackedSet[cleanRel] = true
			candidateSet[cleanRel] = struct{}{}
		}
	}

	for rel := range diskFiles {
		candidateSet[rel] = struct{}{}
	}

	candidates := make([]string, 0, len(candidateSet))
	for rel := range candidateSet {
		candidates = append(candidates, rel)
	}
	slices.Sort(candidates)

	var fileDiffs []SkillFileDiff
	for _, rel := range candidates {
		if bundled, inBundled := bundledByPath[rel]; inBundled {
			diskBytes, readErr := readSafeManagedInRoot(skillRoot, rel)
			if readErr != nil {
				if errors.Is(readErr, ErrUnsafeSymlink) {
					return SkillDiff{}, readErr
				}
				if errors.Is(readErr, os.ErrNotExist) {
					fileDiffs = append(fileDiffs, SkillFileDiff{
						RelPath: rel,
						Status:  FileDiffAdded,
						Diff:    unifiedDiff("/dev/null", "b/"+rel, "", bundled.Content),
					})
					continue
				}
				return SkillDiff{}, readErr
			}
			if bytes.Equal(diskBytes, []byte(bundled.Content)) {
				continue
			}
			fileDiffs = append(fileDiffs, SkillFileDiff{
				RelPath: rel,
				Status:  FileDiffModified,
				Diff:    unifiedDiff("a/"+rel, "b/"+rel, string(diskBytes), bundled.Content),
			})
			continue
		}

		if trackedSet[rel] {
			diskBytes, readErr := readSafeManagedInRoot(skillRoot, rel)
			if readErr != nil {
				if errors.Is(readErr, ErrUnsafeSymlink) {
					return SkillDiff{}, readErr
				}
				if errors.Is(readErr, os.ErrNotExist) {
					continue
				}
				return SkillDiff{}, readErr
			}
			fileDiffs = append(fileDiffs, SkillFileDiff{
				RelPath: rel,
				Status:  FileDiffRemoved,
				Diff:    unifiedDiff("a/"+rel, "/dev/null", string(diskBytes), ""),
			})
			continue
		}

		if _, onDisk := diskFiles[rel]; onDisk {
			fileDiffs = append(fileDiffs, SkillFileDiff{
				RelPath: rel,
				Status:  FileDiffUntracked,
			})
		}
	}

	return SkillDiff{
		Name:  s.Name,
		State: st.State,
		Path:  st.Path,
		Files: fileDiffs,
	}, nil
}

func collectSkillDiskFiles(skillRoot *os.Root) (map[string]struct{}, error) {
	files := make(map[string]struct{})
	err := fs.WalkDir(skillRoot.FS(), ".", func(p string, d fs.DirEntry, wErr error) error {
		if wErr != nil {
			return wErr
		}
		if p == "." {
			return nil
		}
		osRel := filepath.FromSlash(p)
		fi, lErr := skillRoot.Lstat(osRel)
		if lErr != nil {
			return lErr
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s", ErrUnsafeSymlink, filepath.Join(skillRoot.Name(), osRel))
		}
		if fi.IsDir() {
			if strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		if fi.Mode().IsRegular() {
			files[p] = struct{}{}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

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

func myersDiff(a, b []string) []diffEdit {
	n, m := len(a), len(b)
	maxD := n + m
	offset := maxD
	v := make([]int, 2*maxD+1)
	trace := make([][]int, 0, min(maxD+1, 64))

	for d := 0; d <= maxD; d++ {
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
	return nil
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
