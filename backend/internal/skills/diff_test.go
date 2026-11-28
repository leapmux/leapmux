package skills

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiffOne_States(t *testing.T) {
	t.Parallel()

	review, ok := Lookup("leapmux-review")
	require.True(t, ok)

	t.Run("unknown or empty skill returns ErrUnknownSkill", func(t *testing.T) {
		t.Parallel()
		_, err := DiffOne(t.TempDir(), "does-not-exist")
		require.ErrorIs(t, err, ErrUnknownSkill)
		_, err = DiffOne(t.TempDir(), "   ")
		require.ErrorIs(t, err, ErrUnknownSkill)
	})

	t.Run("not_installed reports all bundled files as added", func(t *testing.T) {
		t.Parallel()
		dir := filepath.Join(t.TempDir(), "missing-skills-dir")
		d, err := DiffOne(dir, review.Name)
		require.NoError(t, err)
		assert.Equal(t, review.Name, d.Name)
		assert.Equal(t, StateNotInstalled, d.State)
		assert.Equal(t, filepath.Join(dir, review.Name, "SKILL.md"), d.Path)
		require.Len(t, d.Files, len(review.Files))
		for i, f := range review.Files {
			assert.Equal(t, f.RelPath, d.Files[i].RelPath)
			assert.Equal(t, FileDiffAdded, d.Files[i].Status)
			assert.Contains(t, d.Files[i].Diff, "--- /dev/null\n+++ b/"+f.RelPath+"\n")
			assert.Contains(t, d.Files[i].Diff, "@@ -0,0 +1,")
		}
	})

	t.Run("up_to_date returns empty file diffs", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		_, err := Install(dir, []string{review.Name}, Options{})
		require.NoError(t, err)

		d, err := DiffOne(dir, review.Name)
		require.NoError(t, err)
		assert.Equal(t, StateUpToDate, d.State)
		assert.Empty(t, d.Files)
	})

	t.Run("modified file produces readable unified diff with context", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		_, err := Install(dir, []string{review.Name}, Options{})
		require.NoError(t, err)

		lines := strings.Split(review.Content(), "\n")
		require.Greater(t, len(lines), 10)
		editedLines := append([]string(nil), lines...)
		editedLines[5] = "# LOCAL USER TWEAK"
		edited := strings.Join(editedLines, "\n")
		require.NoError(t, os.WriteFile(filepath.Join(dir, review.Name, "SKILL.md"), []byte(edited), 0o644))

		d, err := DiffOne(dir, review.Name)
		require.NoError(t, err)
		assert.Equal(t, StateModified, d.State)
		require.Len(t, d.Files, 1)
		assert.Equal(t, "SKILL.md", d.Files[0].RelPath)
		assert.Equal(t, FileDiffModified, d.Files[0].Status)
		assert.Contains(t, d.Files[0].Diff, "--- a/SKILL.md\n+++ b/SKILL.md\n")
		assert.Contains(t, d.Files[0].Diff, "@@ -")
		assert.Contains(t, d.Files[0].Diff, "-# LOCAL USER TWEAK\n")
		assert.Contains(t, d.Files[0].Diff, "+"+lines[5]+"\n")
	})

	t.Run("missing bundled file and extra untracked file", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		_, err := Install(dir, []string{review.Name}, Options{})
		require.NoError(t, err)

		skillDir := filepath.Join(dir, review.Name)
		require.NoError(t, os.Remove(filepath.Join(skillDir, "references", "review-schemas.md")))
		require.NoError(t, os.WriteFile(filepath.Join(skillDir, "user-notes.md"), []byte("# my notes\n"), 0o644))

		d, err := DiffOne(dir, review.Name)
		require.NoError(t, err)
		assert.Equal(t, StateModified, d.State)

		byPath := make(map[string]SkillFileDiff, len(d.Files))
		for _, f := range d.Files {
			byPath[f.RelPath] = f
		}
		require.Len(t, byPath, 2)
		assert.Equal(t, FileDiffAdded, byPath["references/review-schemas.md"].Status)
		assert.Contains(t, byPath["references/review-schemas.md"].Diff, "+++ b/references/review-schemas.md\n")
		assert.Equal(t, FileDiffUntracked, byPath["user-notes.md"].Status)
		assert.Empty(t, byPath["user-notes.md"].Diff)
	})

	t.Run("outdated with retired manifest file and updated content", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		_, err := Install(dir, []string{review.Name}, Options{})
		require.NoError(t, err)

		skillDir := filepath.Join(dir, review.Name)
		oldMD := []byte("---\nname: leapmux-review\ndescription: old version\n---\n# Old body\n")
		require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), oldMD, 0o644))

		retiredPath := filepath.Join(skillDir, "legacy", "old.md")
		retiredContent := []byte("# retired file\n")
		require.NoError(t, os.MkdirAll(filepath.Dir(retiredPath), 0o755))
		require.NoError(t, os.WriteFile(retiredPath, retiredContent, 0o644))

		// Update marker hashes to match disk so state is outdated (not modified).
		rawMarker, err := os.ReadFile(filepath.Join(skillDir, MarkerFileName))
		require.NoError(t, err)
		var m skillMarker
		require.NoError(t, json.Unmarshal(rawMarker, &m))
		m.Files["SKILL.md"] = sha256Hex(oldMD)
		m.Files["legacy/old.md"] = sha256Hex(retiredContent)
		updatedMarker, err := json.Marshal(m)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(skillDir, MarkerFileName), updatedMarker, 0o644))

		d, err := DiffOne(dir, review.Name)
		require.NoError(t, err)
		assert.Equal(t, StateOutdated, d.State)

		byPath := make(map[string]SkillFileDiff, len(d.Files))
		for _, f := range d.Files {
			byPath[f.RelPath] = f
		}
		require.Len(t, byPath, 2)
		assert.Equal(t, FileDiffModified, byPath["SKILL.md"].Status)
		assert.Contains(t, byPath["SKILL.md"].Diff, "-# Old body\n")
		assert.Equal(t, FileDiffRemoved, byPath["legacy/old.md"].Status)
		assert.Contains(t, byPath["legacy/old.md"].Diff, "--- a/legacy/old.md\n+++ /dev/null\n")
		assert.Contains(t, byPath["legacy/old.md"].Diff, "-# retired file\n")
	})

	t.Run("unmanaged directory diffs existing and missing files without error", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		skillDir := filepath.Join(dir, review.Name)
		require.NoError(t, os.MkdirAll(skillDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# unmanaged skill\n"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(skillDir, "custom.txt"), []byte("extra\n"), 0o644))

		d, err := DiffOne(dir, review.Name)
		require.NoError(t, err)
		assert.Equal(t, StateUnmanaged, d.State)

		byPath := make(map[string]SkillFileDiff, len(d.Files))
		for _, f := range d.Files {
			byPath[f.RelPath] = f
		}
		assert.Equal(t, FileDiffModified, byPath["SKILL.md"].Status)
		assert.Equal(t, FileDiffAdded, byPath["agents/openai.yaml"].Status)
		assert.Equal(t, FileDiffUntracked, byPath["custom.txt"].Status)
	})

	t.Run("targetDir as regular file returns error", func(t *testing.T) {
		t.Parallel()
		filePath := filepath.Join(t.TempDir(), "not-a-dir")
		require.NoError(t, os.WriteFile(filePath, []byte("plain file\n"), 0o644))

		_, err := DiffOne(filePath, review.Name)
		require.Error(t, err)
		assert.ErrorContains(t, err, "open skills directory")
	})

	t.Run("existing empty skill directory returns StateNotInstalled", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, review.Name), 0o755))

		d, err := DiffOne(dir, review.Name)
		require.NoError(t, err)
		assert.Equal(t, StateNotInstalled, d.State)
		require.Len(t, d.Files, len(review.Files))
		for _, f := range d.Files {
			assert.Equal(t, FileDiffAdded, f.Status)
		}
	})

	t.Run("bundled file replaced by directory returns ErrSkillUnmanaged", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		_, err := Install(dir, []string{review.Name}, Options{})
		require.NoError(t, err)

		bundledPath := filepath.Join(dir, review.Name, "references", "review-schemas.md")
		require.NoError(t, os.Remove(bundledPath))
		require.NoError(t, os.MkdirAll(bundledPath, 0o755))

		_, err = DiffOne(dir, review.Name)
		require.ErrorIs(t, err, ErrSkillUnmanaged)
	})

	t.Run("retired manifest entry already deleted from disk is skipped", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		_, err := Install(dir, []string{review.Name}, Options{})
		require.NoError(t, err)

		skillDir := filepath.Join(dir, review.Name)
		rawMarker, err := os.ReadFile(filepath.Join(skillDir, MarkerFileName))
		require.NoError(t, err)
		var m skillMarker
		require.NoError(t, json.Unmarshal(rawMarker, &m))
		m.Files["legacy/already-deleted.md"] = sha256Hex([]byte("# deleted earlier\n"))
		updatedMarker, err := json.Marshal(m)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(skillDir, MarkerFileName), updatedMarker, 0o644))

		d, err := DiffOne(dir, review.Name)
		require.NoError(t, err)
		for _, f := range d.Files {
			assert.NotEqual(t, "legacy/already-deleted.md", f.RelPath)
		}
		assert.Empty(t, d.Files)
	})
}

func TestDiffOne_RejectsSymlinks(t *testing.T) {
	t.Parallel()

	review, ok := Lookup("leapmux-review")
	require.True(t, ok)

	t.Run("symlinked skill root", func(t *testing.T) {
		t.Parallel()
		base := t.TempDir()
		outside := filepath.Join(base, "outside")
		require.NoError(t, os.MkdirAll(outside, 0o755))
		skillsDir := filepath.Join(base, "skills")
		require.NoError(t, os.MkdirAll(skillsDir, 0o755))
		createSymlinkOrSkip(t, outside, filepath.Join(skillsDir, review.Name))

		_, err := DiffOne(skillsDir, review.Name)
		require.ErrorIs(t, err, ErrUnsafeSymlink)
	})

	t.Run("in-root relative symlink at bundled file", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		_, err := Install(dir, []string{review.Name}, Options{})
		require.NoError(t, err)

		refPath := filepath.Join(dir, review.Name, "references", "review-schemas.md")
		require.NoError(t, os.Remove(refPath))
		createSymlinkOrSkip(t, filepath.Join("..", "SKILL.md"), refPath)

		_, err = DiffOne(dir, review.Name)
		require.ErrorIs(t, err, ErrUnsafeSymlink)
	})

	t.Run("symlink at extra untracked file", func(t *testing.T) {
		t.Parallel()
		base := t.TempDir()
		outside := filepath.Join(base, "secret.txt")
		require.NoError(t, os.WriteFile(outside, []byte("secret\n"), 0o644))
		dir := filepath.Join(base, "skills")
		_, err := Install(dir, []string{review.Name}, Options{})
		require.NoError(t, err)

		createSymlinkOrSkip(t, outside, filepath.Join(dir, review.Name, "extra-link.md"))
		_, err = DiffOne(dir, review.Name)
		require.ErrorIs(t, err, ErrUnsafeSymlink)
	})

	t.Run("symlink at marker file", func(t *testing.T) {
		t.Parallel()
		base := t.TempDir()
		outside := filepath.Join(base, "marker.json")
		require.NoError(t, os.WriteFile(outside, []byte("{}\n"), 0o644))
		dir := filepath.Join(base, "skills")
		_, err := Install(dir, []string{review.Name}, Options{})
		require.NoError(t, err)

		markerPath := filepath.Join(dir, review.Name, MarkerFileName)
		require.NoError(t, os.Remove(markerPath))
		createSymlinkOrSkip(t, outside, markerPath)

		_, err = DiffOne(dir, review.Name)
		require.ErrorIs(t, err, ErrUnsafeSymlink)
	})
}

func TestDiffAll(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	_, err := Install(dir, []string{"leapmux"}, Options{})
	require.NoError(t, err)

	// Empty names diffs all bundled skills.
	allDiffs, err := DiffAll(dir, nil, DiffOptions{})
	require.NoError(t, err)
	require.Len(t, allDiffs, len(Catalog()))
	assert.Equal(t, "leapmux", allDiffs[0].Name)
	assert.Equal(t, StateUpToDate, allDiffs[0].State)
	assert.Empty(t, allDiffs[0].Files)
	assert.Equal(t, StateNotInstalled, allDiffs[1].State)
	assert.NotEmpty(t, allDiffs[1].Files)

	// SkipNotInstalled omits uninstalled skills without synthesizing /dev/null diffs.
	installedOnly, err := DiffAll(dir, nil, DiffOptions{SkipNotInstalled: true})
	require.NoError(t, err)
	require.Len(t, installedOnly, 1)
	assert.Equal(t, "leapmux", installedOnly[0].Name)
	assert.Equal(t, StateUpToDate, installedOnly[0].State)

	// SkipNotInstalled on a missing targetDir returns empty slice without error.
	missingOnly, err := DiffAll(filepath.Join(t.TempDir(), "missing"), nil, DiffOptions{SkipNotInstalled: true})
	require.NoError(t, err)
	assert.Empty(t, missingOnly)

	// Explicit subset diffs only requested skills in order.
	sub, err := DiffAll(dir, []string{"leapmux-review", "leapmux"}, DiffOptions{})
	require.NoError(t, err)
	require.Len(t, sub, 2)
	assert.Equal(t, "leapmux-review", sub[0].Name)
	assert.Equal(t, StateNotInstalled, sub[0].State)
	assert.Equal(t, "leapmux", sub[1].Name)
	assert.Equal(t, StateUpToDate, sub[1].State)

	// Unknown skill returns ErrUnknownSkill.
	_, err = DiffAll(dir, []string{"leapmux", "bogus-skill"}, DiffOptions{})
	require.ErrorIs(t, err, ErrUnknownSkill)

	// targetDir as regular file returns an error.
	filePath := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(filePath, []byte("plain file\n"), 0o644))
	_, err = DiffAll(filePath, nil, DiffOptions{})
	require.Error(t, err)
	assert.ErrorContains(t, err, "open skills directory")
}

func TestUnifiedDiff_EdgeCases(t *testing.T) {
	t.Parallel()

	// Identical texts yield empty string.
	assert.Empty(t, unifiedDiff("a/x", "b/x", "hello\n", "hello\n"))

	// Trailing newline change is detected and annotated.
	d := unifiedDiff("a/x", "b/x", "line1\nline2", "line1\nline2\n")
	assert.Contains(t, d, "-line2\n\\ No newline at end of file\n+line2\n")

	// Multi-hunk file splits distant edits into two hunks.
	oldLines := []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12"}
	newLines := []string{"1-edit", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12-edit"}
	multi := unifiedDiff("a/f", "b/f", strings.Join(oldLines, "\n")+"\n", strings.Join(newLines, "\n")+"\n")
	assert.Equal(t, 2, strings.Count(multi, "@@ -"), "distant edits should emit two separate hunks:\n%s", multi)
	assert.Contains(t, multi, "-1\n+1-edit\n")
	assert.Contains(t, multi, "-12\n+12-edit\n")
}

func TestDiffOne_OversizedManagedFileHandledGracefully(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	review, ok := Lookup("leapmux-review")
	require.True(t, ok)
	_, err := Install(dir, []string{review.Name}, Options{})
	require.NoError(t, err)

	// Write a >4 MiB SKILL.md that exceeds maxManagedFileBytes.
	oversized := strings.Repeat("a", 4<<20+64) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, review.Name, "SKILL.md"), []byte(oversized), 0o644))

	d, err := DiffOne(dir, review.Name)
	require.NoError(t, err)
	assert.Equal(t, StateModified, d.State)
	require.Len(t, d.Files, 1)
	assert.Equal(t, "SKILL.md", d.Files[0].RelPath)
	assert.Equal(t, FileDiffModified, d.Files[0].Status)
	assert.Contains(t, d.Files[0].Diff, "diff limit")
	assert.NotContains(t, d.Files[0].Diff, oversized)
}

func TestDiffOne_UpToDateWithUntrackedFileStillReportsUntracked(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	review, ok := Lookup("leapmux-review")
	require.True(t, ok)
	_, err := Install(dir, []string{review.Name}, Options{})
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(dir, review.Name, "notes.md"), []byte("user note\n"), 0o644))

	d, err := DiffOne(dir, review.Name)
	require.NoError(t, err)
	assert.Equal(t, StateUpToDate, d.State)
	require.Len(t, d.Files, 1)
	assert.Equal(t, "notes.md", d.Files[0].RelPath)
	assert.Equal(t, FileDiffUntracked, d.Files[0].Status)
}

func TestUnifiedDiff_LargeDivergedFileUsesCappedFallbackFast(t *testing.T) {
	t.Parallel()

	const changedLines = 5000
	var oldBuf, newBuf strings.Builder
	oldBuf.WriteString("common-header\n")
	newBuf.WriteString("common-header\n")
	for i := range changedLines {
		fmt.Fprintf(&oldBuf, "old-line-%d\n", i)
		fmt.Fprintf(&newBuf, "new-line-%d\n", i)
	}
	oldBuf.WriteString("common-footer\n")
	newBuf.WriteString("common-footer\n")

	start := time.Now()
	got := unifiedDiff("a/big.md", "b/big.md", oldBuf.String(), newBuf.String())
	elapsed := time.Since(start)

	assert.Less(t, elapsed, 5*time.Second, "large diverged diff must complete quickly via fallback")
	assert.Contains(t, got, "--- a/big.md\n+++ b/big.md\n")
	assert.Contains(t, got, " common-header\n")
	assert.Contains(t, got, "-old-line-0\n")
	assert.Contains(t, got, "+new-line-0\n")
	assert.Contains(t, got, " common-footer\n")
}
