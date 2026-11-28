package skills

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v4"
)

func createSymlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink creation unavailable on Windows without privileges: %v", err)
		}
		require.NoError(t, err)
	}
}

// verifyDirectoryMatchesCatalog compares every file under repoSkillsDir against
// the embedded catalog, returning an error if any skill or file is missing,
// extra, differs in bytes, or uses an underscore prefix that //go:embed omits.
func verifyDirectoryMatchesCatalog(repoSkillsDir string) error {
	cat := Catalog()
	seenSkills := make(map[string]bool, len(cat))
	for _, s := range cat {
		seenSkills[s.Name] = true
		skillRoot := filepath.Join(repoSkillsDir, s.Name)
		if err := filepath.WalkDir(skillRoot, func(p string, d fs.DirEntry, wErr error) error {
			if wErr != nil {
				return wErr
			}
			if p == skillRoot {
				return nil
			}
			if strings.HasPrefix(d.Name(), ".") {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasPrefix(d.Name(), "_") {
				rel, _ := filepath.Rel(skillRoot, p)
				return fmt.Errorf("repo skill %q has underscore-prefixed entry %s which //go:embed omits", s.Name, filepath.ToSlash(rel))
			}
			return nil
		}); err != nil {
			return err
		}

		expected := make(map[string][]byte, len(s.Files))
		for _, f := range s.Files {
			expected[f.RelPath] = []byte(f.Content)
		}
		found := make(map[string]bool, len(s.Files))
		if err := walkSkillFiles(os.DirFS(skillRoot), ".", func(rel string, raw []byte) error {
			want, ok := expected[rel]
			if !ok {
				return fmt.Errorf("repo skill %q has extra file %s missing from embedded catalog", s.Name, rel)
			}
			if !bytes.Equal(raw, want) {
				return fmt.Errorf("embedded catalog for %q (%s) differs from %s", s.Name, rel, filepath.Join(skillRoot, filepath.FromSlash(rel)))
			}
			found[rel] = true
			return nil
		}); err != nil {
			return err
		}
		for _, f := range s.Files {
			if !found[f.RelPath] {
				return fmt.Errorf("embedded catalog file %q/%s is missing from %s", s.Name, f.RelPath, skillRoot)
			}
		}
	}
	entries, err := os.ReadDir(repoSkillsDir)
	if err != nil {
		return fmt.Errorf("read repo skills dir %s: %w", repoSkillsDir, err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if strings.HasPrefix(e.Name(), "_") {
			return fmt.Errorf("repo skills dir has underscore-prefixed entry %q which //go:embed omits", e.Name())
		}
		if !e.IsDir() {
			continue
		}
		if !seenSkills[e.Name()] {
			return fmt.Errorf("repo skill %q is missing from embedded backend/internal/skills/generated/catalog", e.Name())
		}
	}
	return nil
}

func TestBundledSkills_MatchRepoRoot(t *testing.T) {
	// Ensure repo-root skills/ (used by `npx skills add leapmux/leapmux` and
	// Hugo site) stays byte-for-byte identical to the generated embedded catalog.
	repoSkills := filepath.Join("..", "..", "..", "skills")
	require.NoError(t, verifyDirectoryMatchesCatalog(repoSkills))

	cat := Catalog()
	require.Len(t, cat, 6)
	assert.Equal(t, "leapmux", cat[0].Name, "foundational leapmux skill must sort first")

	review, ok := Lookup("leapmux-review")
	require.True(t, ok)
	assert.True(t, review.UserInvocable)
	assert.Contains(t, review.References, "review-schemas")
	ref, ok := review.Reference("review-schemas")
	require.True(t, ok)
	assert.Equal(t, "references/review-schemas.md", ref.RelPath)
	ref2, ok := review.Reference("references/review-schemas.md")
	require.True(t, ok)
	assert.Equal(t, ref.ContentHash, ref2.ContentHash)

	handoff, ok := Lookup("leapmux-handoff")
	require.True(t, ok)
	assert.NotContains(t, handoff.Content(), "keep full conversation history")
	assert.NotContains(t, handoff.Content(), "conversation fork")
	assert.NotContains(t, handoff.Content(), "Fork with conversation context")
}

func TestVerifyDirectoryMatchesCatalog_RejectsUnderscoreEntries(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, s := range Catalog() {
		for _, f := range s.Files {
			dst := filepath.Join(root, s.Name, filepath.FromSlash(f.RelPath))
			require.NoError(t, os.MkdirAll(filepath.Dir(dst), 0o755))
			require.NoError(t, os.WriteFile(dst, []byte(f.Content), 0o644))
		}
	}
	require.NoError(t, verifyDirectoryMatchesCatalog(root))

	// Underscore-prefixed file inside a skill fails parity verification.
	badFile := filepath.Join(root, "leapmux", "_draft.md")
	require.NoError(t, os.WriteFile(badFile, []byte("draft\n"), 0o644))
	require.Error(t, verifyDirectoryMatchesCatalog(root))
	require.NoError(t, os.Remove(badFile))

	// Underscore-prefixed empty directory inside a skill fails parity verification.
	badDir := filepath.Join(root, "leapmux", "_hidden")
	require.NoError(t, os.MkdirAll(badDir, 0o755))
	require.Error(t, verifyDirectoryMatchesCatalog(root))
	require.NoError(t, os.Remove(badDir))

	// Underscore-prefixed top-level directory fails parity verification.
	topBadDir := filepath.Join(root, "_wip")
	require.NoError(t, os.MkdirAll(topBadDir, 0o755))
	require.Error(t, verifyDirectoryMatchesCatalog(root))
}

var skillRelativeRefLink = regexp.MustCompile(`references/[a-zA-Z0-9._-]+\.md`)

// TestBundledSkills_ContractAndMetadata verifies frontmatter discipline,
// SKILL.md line budgets, Codex agents/openai.yaml metadata, and relative link
// integrity across every bundled skill.
func TestBundledSkills_ContractAndMetadata(t *testing.T) {
	const maxSkillLines = 150

	type openAISkillYAML struct {
		Interface struct {
			DisplayName      string `yaml:"display_name"`
			ShortDescription string `yaml:"short_description"`
			DefaultPrompt    string `yaml:"default_prompt"`
		} `yaml:"interface"`
	}

	for _, s := range Catalog() {
		lines := strings.Split(strings.TrimSuffix(s.Content(), "\n"), "\n")
		assert.LessOrEqual(t, len(lines), maxSkillLines, "skill %s SKILL.md exceeds %d-line budget (%d lines)", s.Name, maxSkillLines, len(lines))

		if s.Name != "leapmux" {
			assert.True(t, s.UserInvocable, "workflow/help skill %s must set user-invocable: true", s.Name)
			assert.NotEmpty(t, s.ArgumentHint, "workflow/help skill %s must set argument-hint", s.Name)
		}

		var openAIFile *SkillFile
		fileSet := make(map[string]bool, len(s.Files))
		for i := range s.Files {
			f := &s.Files[i]
			fileSet[f.RelPath] = true
			if f.RelPath == "agents/openai.yaml" {
				openAIFile = f
			}
		}
		require.NotNil(t, openAIFile, "skill %s must include agents/openai.yaml", s.Name)
		var meta openAISkillYAML
		dec := yaml.NewDecoder(strings.NewReader(openAIFile.Content))
		dec.KnownFields(true)
		require.NoError(t, dec.Decode(&meta), "parse %s/agents/openai.yaml", s.Name)
		assert.NotEmpty(t, meta.Interface.DisplayName, "%s/agents/openai.yaml missing display_name", s.Name)
		assert.NotEmpty(t, meta.Interface.ShortDescription, "%s/agents/openai.yaml missing short_description", s.Name)
		assert.NotEmpty(t, meta.Interface.DefaultPrompt, "%s/agents/openai.yaml missing default_prompt", s.Name)

		for _, match := range skillRelativeRefLink.FindAllString(s.Content(), -1) {
			assert.True(t, fileSet[match], "skill %s refers to missing file %q", s.Name, match)
		}
	}
}

func TestAggregateState_SeverityOrdering(t *testing.T) {
	t.Parallel()

	assert.Equal(t, StateNotInstalled, AggregateState(nil))
	assert.Equal(t, StateNotInstalled, AggregateState([]State{}))
	assert.Equal(t, StateUpToDate, AggregateState([]State{StateUpToDate, StateUpToDate}))
	assert.Equal(t, StateNotInstalled, AggregateState([]State{StateUpToDate, StateNotInstalled}))
	assert.Equal(t, StateNotInstalled, AggregateState([]State{StateNotInstalled, StateNotInstalled}))
	assert.Equal(t, StateOutdated, AggregateState([]State{StateUpToDate, StateNotInstalled, StateOutdated}))
	assert.Equal(t, StateModified, AggregateState([]State{StateOutdated, StateModified, StateNotInstalled}))
	assert.Equal(t, StateUnmanaged, AggregateState([]State{StateModified, StateUnmanaged, StateUpToDate}))
}

func TestInstallStatusDriftAndUninstall(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".agents", "skills")

	// Initially everything is not_installed.
	for _, st := range InspectAll(dir) {
		assert.Equal(t, StateNotInstalled, st.State)
	}

	// Install two skills (including one with references/ + agents/openai.yaml).
	outcomes, err := Install(dir, []string{"leapmux", "leapmux-review"}, Options{})
	require.NoError(t, err)
	require.Len(t, outcomes, 2)
	assert.Equal(t, ActionInstalled, outcomes[0].Action)
	assert.Equal(t, ActionInstalled, outcomes[1].Action)
	assert.FileExists(t, filepath.Join(dir, "leapmux-review", "SKILL.md"))
	assert.FileExists(t, filepath.Join(dir, "leapmux-review", "agents", "openai.yaml"))
	assert.FileExists(t, filepath.Join(dir, "leapmux-review", "references", "review-schemas.md"))

	s, ok := Lookup("leapmux")
	require.True(t, ok)
	assert.Equal(t, StateUpToDate, InspectOne(dir, s).State)

	review, ok := Lookup("leapmux-review")
	require.True(t, ok)
	assert.Equal(t, StateUpToDate, InspectOne(dir, review).State)

	// Re-installing is idempotent (unchanged).
	outcomes, err = Install(dir, []string{"leapmux"}, Options{})
	require.NoError(t, err)
	assert.Equal(t, ActionUnchanged, outcomes[0].Action)

	// Simulate user editing a companion reference file -> detected as modified.
	refFile := filepath.Join(dir, "leapmux-review", "references", "review-schemas.md")
	require.NoError(t, os.WriteFile(refFile, []byte("# local schema tweak\n"), 0o644))
	assert.Equal(t, StateModified, InspectOne(dir, review).State)

	// Simulate user editing SKILL.md locally -> detected as modified and
	// refused without Force.
	skillFile := filepath.Join(dir, "leapmux", "SKILL.md")
	require.NoError(t, os.WriteFile(skillFile, []byte("# custom user edit\n"), 0o644))
	assert.Equal(t, StateModified, InspectOne(dir, s).State)

	_, err = Install(dir, []string{"leapmux"}, Options{Force: false})
	require.ErrorIs(t, err, ErrSkillModified)
	assert.Contains(t, err.Error(), "local edits")
	assert.NotContains(t, err.Error(), "--force")

	// Unknown skill returns ErrUnknownSkill.
	_, err = Install(dir, []string{"no-such-skill"}, Options{})
	require.ErrorIs(t, err, ErrUnknownSkill)

	// Empty skill name list fails.
	_, err = Install(dir, nil, Options{})
	require.Error(t, err)
	_, err = Install(dir, []string{"  "}, Options{})
	require.Error(t, err)

	// SyncEnabled skips modified skills without clobbering user edits.
	syncOut, err := SyncEnabled(dir, []string{"leapmux"}, false)
	require.NoError(t, err)
	require.Len(t, syncOut, 1)
	assert.Equal(t, ActionSkippedModified, syncOut[0].Action)

	// Empty SyncEnabled returns nil, nil.
	emptyOut, err := SyncEnabled(dir, nil, false)
	require.NoError(t, err)
	assert.Nil(t, emptyOut)

	// Force overwrites and restores up_to_date.
	outcomes, err = Install(dir, []string{"leapmux"}, Options{Force: true})
	require.NoError(t, err)
	assert.Equal(t, ActionUpdated, outcomes[0].Action)
	assert.Equal(t, StateUpToDate, InspectOne(dir, s).State)

	// Uninstall removes cleanly.
	rmOut, err := Uninstall(dir, []string{"leapmux"}, Options{})
	require.NoError(t, err)
	assert.Equal(t, ActionRemoved, rmOut[0].Action)
	assert.Equal(t, StateNotInstalled, InspectOne(dir, s).State)

	// Uninstalling an absent skill reports ActionNotInstalled.
	rmOut, err = Uninstall(dir, []string{"leapmux"}, Options{})
	require.NoError(t, err)
	assert.Equal(t, ActionNotInstalled, rmOut[0].Action)
}

func TestObsoleteFilePruningAndUserFilePreservation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".agents", "skills")
	review, ok := Lookup("leapmux-review")
	require.True(t, ok)

	_, err := Install(dir, []string{review.Name}, Options{})
	require.NoError(t, err)

	// Simulate an obsolete previously-managed nested file alongside a user-added file.
	skillDir := filepath.Join(dir, review.Name)
	obsoletePath := filepath.Join(skillDir, "legacy", "nested", "old-removed.md")
	obsoleteContent := []byte("# removed in newer version\n")
	require.NoError(t, os.MkdirAll(filepath.Dir(obsoletePath), 0o755))
	require.NoError(t, os.WriteFile(obsoletePath, obsoleteContent, 0o644))
	userNotePath := filepath.Join(skillDir, "user-notes.md")
	require.NoError(t, os.WriteFile(userNotePath, []byte("# keep me\n"), 0o644))

	// Record obsolete file in marker as previously managed and remove
	// references/review-schemas.md from disk and marker so state is outdated.
	skillRoot, err := os.OpenRoot(skillDir)
	require.NoError(t, err)
	m, err := readSkillMarkerInRoot(skillRoot, review.Name)
	require.NoError(t, err)
	require.NoError(t, skillRoot.Close())

	m.Files["legacy/nested/old-removed.md"] = sha256Hex(obsoleteContent)
	require.NoError(t, os.Remove(filepath.Join(skillDir, "references", "review-schemas.md")))
	delete(m.Files, "references/review-schemas.md")
	rawMarker, err := json.Marshal(m)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, MarkerFileName), rawMarker, 0o644))

	assert.Equal(t, StateOutdated, InspectOne(dir, review).State)
	outcomes, err := Install(dir, []string{review.Name}, Options{})
	require.NoError(t, err)
	assert.Equal(t, ActionUpdated, outcomes[0].Action)
	_, statErr := os.Stat(obsoletePath)
	assert.ErrorIs(t, statErr, os.ErrNotExist, "unmodified retired managed file should be pruned")
	assert.NoDirExists(t, filepath.Join(skillDir, "legacy"), "empty retired parent directories must be pruned")
	assert.FileExists(t, userNotePath, "user-added extra file must be preserved")
}

func TestSymlinkSafety(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	require.NoError(t, os.MkdirAll(outside, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "SKILL.md"), []byte("outside\n"), 0o644))

	skillsDir := filepath.Join(base, ".agents", "skills")
	require.NoError(t, os.MkdirAll(skillsDir, 0o755))
	createSymlinkOrSkip(t, outside, filepath.Join(skillsDir, "leapmux"))

	s, ok := Lookup("leapmux")
	require.True(t, ok)
	assert.Equal(t, StateUnmanaged, InspectOne(skillsDir, s).State)

	// Install refuses symlinked skill directory even with Force.
	_, err := Install(skillsDir, []string{"leapmux"}, Options{Force: true})
	require.ErrorIs(t, err, ErrUnsafeSymlink)
	assert.Contains(t, err.Error(), "symlink")

	// Uninstall also refuses symlinked skill directory even with Force.
	_, err = Uninstall(skillsDir, []string{"leapmux"}, Options{Force: true})
	require.ErrorIs(t, err, ErrUnsafeSymlink)
	assert.Contains(t, err.Error(), "symlink")
}

func TestRootConfinement_RejectsInRootRelativeSymlinks(t *testing.T) {
	t.Parallel()
	review, ok := Lookup("leapmux-review")
	require.True(t, ok)

	t.Run("in-root relative file symlink is rejected", func(t *testing.T) {
		dir := t.TempDir()
		_, err := Install(dir, []string{review.Name}, Options{})
		require.NoError(t, err)

		refPath := filepath.Join(dir, review.Name, "references", "review-schemas.md")
		require.NoError(t, os.Remove(refPath))
		// Relative symlink that stays inside skillRoot (which os.Root alone permits).
		createSymlinkOrSkip(t, filepath.Join("..", "SKILL.md"), refPath)

		assert.Equal(t, StateUnmanaged, InspectOne(dir, review).State)
		_, err = Install(dir, []string{review.Name}, Options{Force: true})
		require.ErrorIs(t, err, ErrUnsafeSymlink)
	})

	t.Run("in-root relative directory symlink is rejected", func(t *testing.T) {
		dir := t.TempDir()
		_, err := Install(dir, []string{review.Name}, Options{})
		require.NoError(t, err)

		refsDir := filepath.Join(dir, review.Name, "references")
		require.NoError(t, os.RemoveAll(refsDir))
		createSymlinkOrSkip(t, "agents", refsDir)

		assert.Equal(t, StateUnmanaged, InspectOne(dir, review).State)
		_, err = Install(dir, []string{review.Name}, Options{Force: true})
		require.ErrorIs(t, err, ErrUnsafeSymlink)
	})

	t.Run("force uninstall removes read-only nested files via root", func(t *testing.T) {
		dir := t.TempDir()
		_, err := Install(dir, []string{review.Name}, Options{})
		require.NoError(t, err)

		refFile := filepath.Join(dir, review.Name, "references", "review-schemas.md")
		require.NoError(t, os.WriteFile(refFile, []byte("# edited\n"), 0o644))
		require.NoError(t, os.Chmod(refFile, 0o444))
		refsDir := filepath.Join(dir, review.Name, "references")
		require.NoError(t, os.Chmod(refsDir, 0o555))

		outcomes, err := Uninstall(dir, []string{review.Name}, Options{Force: true})
		require.NoError(t, err)
		require.Len(t, outcomes, 1)
		assert.Equal(t, ActionRemoved, outcomes[0].Action)
		assert.NoDirExists(t, filepath.Join(dir, review.Name))
	})
}

func TestResolveStandardTargetDir_Gemini(t *testing.T) {
	dir, err := ResolveStandardTargetDir("/home/dev", TargetGemini)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("/home/dev", ".gemini", "skills"), dir)

	_, err = ResolveStandardTargetDir("/home/dev", Target("unknown"))
	require.ErrorIs(t, err, ErrInvalidTarget)

	_, err = ResolveStandardTargetDir("   ", TargetGemini)
	require.Error(t, err)
}

func TestWorkerState_RoundTrip(t *testing.T) {
	dataDir := t.TempDir()
	st, err := LoadWorkerState(dataDir)
	require.NoError(t, err)
	assert.False(t, st.AutoSync)
	assert.Empty(t, st.Enabled)
	assert.Equal(t, []Target{TargetAgents}, st.Targets)

	want := WorkerState{
		AutoSync: true,
		Enabled:  []string{" leapmux ", "leapmux", "leapmux-review", "leapmux-help", "unknown-skill"},
		Targets:  []Target{TargetAgents, TargetClaude, TargetGemini, Target("invalid-target")},
	}
	require.NoError(t, saveWorkerState(dataDir, want))

	got, err := LoadWorkerState(dataDir)
	require.NoError(t, err)
	assert.True(t, got.AutoSync)
	assert.Equal(t, []string{"leapmux", "leapmux-review", "leapmux-help"}, got.Enabled)
	assert.Equal(t, []Target{TargetAgents, TargetClaude, TargetGemini}, got.Targets)
}

func TestUninstall_SkipProtectedPreservesModifiedSkills(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, err := Install(dir, []string{"leapmux", "leapmux-review"}, Options{})
	require.NoError(t, err)
	modifiedPath := filepath.Join(dir, "leapmux-review", "SKILL.md")
	require.NoError(t, os.WriteFile(modifiedPath, []byte("# local edit\n"), 0o644))

	outcomes, err := Uninstall(dir, []string{"leapmux", "leapmux-review"}, Options{SkipProtected: true})
	require.NoError(t, err)
	require.Len(t, outcomes, 2)
	assert.Equal(t, ActionRemoved, outcomes[0].Action)
	assert.Equal(t, ActionSkippedModified, outcomes[1].Action)
	assert.NoDirExists(t, filepath.Join(dir, "leapmux"))
	assert.FileExists(t, modifiedPath)
}

func TestApplyWorkerStateUpdate_ValidationAndSkipProtected(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	homeDir := t.TempDir()

	_, _, err := ApplyWorkerStateUpdate("   ", homeDir, WorkerStateUpdate{})
	require.Error(t, err)
	_, _, err = ApplyWorkerStateUpdate(dataDir, "  ", WorkerStateUpdate{})
	require.Error(t, err)

	_, _, err = ApplyWorkerStateUpdate(dataDir, homeDir, WorkerStateUpdate{
		UpdateEnabledSkills: true,
		EnabledSkills:       []string{"no-such-skill"},
	})
	require.ErrorIs(t, err, ErrInvalidWorkerStateUpdate)
	require.ErrorIs(t, err, ErrUnknownSkill)

	_, _, err = ApplyWorkerStateUpdate(dataDir, homeDir, WorkerStateUpdate{
		Targets: []Target{""},
	})
	require.ErrorIs(t, err, ErrInvalidWorkerStateUpdate)
	require.ErrorIs(t, err, ErrInvalidTarget)

	// Reject combining replacement and delta fields.
	_, _, err = ApplyWorkerStateUpdate(dataDir, homeDir, WorkerStateUpdate{
		UpdateEnabledSkills: true,
		AddSkills:           []string{"leapmux"},
	})
	require.ErrorIs(t, err, ErrInvalidWorkerStateUpdate)

	_, _, err = ApplyWorkerStateUpdate(dataDir, homeDir, WorkerStateUpdate{
		AddAllSkills:    true,
		RemoveAllSkills: true,
	})
	require.ErrorIs(t, err, ErrInvalidWorkerStateUpdate)

	_, _, err = ApplyWorkerStateUpdate(dataDir, homeDir, WorkerStateUpdate{
		AddAllSkills: true,
		RemoveSkills: []string{"leapmux"},
	})
	require.ErrorIs(t, err, ErrInvalidWorkerStateUpdate)

	_, _, err = ApplyWorkerStateUpdate(dataDir, homeDir, WorkerStateUpdate{
		RemoveAllSkills: true,
		AddSkills:       []string{"leapmux"},
	})
	require.ErrorIs(t, err, ErrInvalidWorkerStateUpdate)

	_, _, err = ApplyWorkerStateUpdate(dataDir, homeDir, WorkerStateUpdate{
		Targets:    []Target{TargetAgents},
		AddTargets: []Target{TargetClaude},
	})
	require.ErrorIs(t, err, ErrInvalidWorkerStateUpdate)

	autoSync := true
	st, outcomes, err := ApplyWorkerStateUpdate(dataDir, homeDir, WorkerStateUpdate{
		AutoSync:            &autoSync,
		Targets:             []Target{TargetAgents},
		UpdateEnabledSkills: true,
		EnabledSkills:       []string{"leapmux", "leapmux-review"},
		SyncNow:             true,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"leapmux", "leapmux-review"}, st.Enabled)
	require.Len(t, outcomes, 2)
	assert.Equal(t, TargetAgents, outcomes[0].Target)
	assert.Equal(t, ActionInstalled, outcomes[0].Action)

	modifiedPath := filepath.Join(homeDir, ".agents", "skills", "leapmux-review", "SKILL.md")
	require.NoError(t, os.WriteFile(modifiedPath, []byte("# user edit\n"), 0o644))

	// Disabling both removes clean leapmux and skips modified leapmux-review.
	st, outcomes, err = ApplyWorkerStateUpdate(dataDir, homeDir, WorkerStateUpdate{
		UpdateEnabledSkills: true,
		EnabledSkills:       []string{},
	})
	require.NoError(t, err)
	assert.Empty(t, st.Enabled)
	require.Len(t, outcomes, 2)
	assert.Equal(t, ActionRemoved, outcomes[0].Action)
	assert.Equal(t, ActionSkippedModified, outcomes[1].Action)
	assert.NoDirExists(t, filepath.Join(homeDir, ".agents", "skills", "leapmux"))
	assert.FileExists(t, modifiedPath)
}

func TestApplyWorkerStateUpdate_AtomicDeltasAndStrictConflicts(t *testing.T) {
	t.Parallel()

	t.Run("AddSkills with StrictConflicts fails on modified target skill without failing on unrelated modified skill", func(t *testing.T) {
		dataDir := t.TempDir()
		homeDir := t.TempDir()

		// Install leapmux and modify it locally.
		st, outcomes, err := ApplyWorkerStateUpdate(dataDir, homeDir, WorkerStateUpdate{
			AddSkills:       []string{"leapmux"},
			SyncNow:         true,
			StrictConflicts: true,
		})
		require.NoError(t, err)
		assert.Equal(t, []string{"leapmux"}, st.Enabled)
		require.Len(t, outcomes, 1)
		assert.Equal(t, TargetOutcome{
			Name:   "leapmux",
			Target: TargetAgents,
			Action: ActionInstalled,
			Path:   filepath.Join(homeDir, ".agents", "skills", "leapmux", "SKILL.md"),
		}, outcomes[0])

		leapmuxMD := filepath.Join(homeDir, ".agents", "skills", "leapmux", "SKILL.md")
		require.NoError(t, os.WriteFile(leapmuxMD, []byte("# user edit\n"), 0o644))

		// Adding leapmux-review with StrictConflicts succeeds without touching modified leapmux.
		st, outcomes, err = ApplyWorkerStateUpdate(dataDir, homeDir, WorkerStateUpdate{
			AddSkills:       []string{"leapmux-review"},
			SyncNow:         true,
			StrictConflicts: true,
		})
		require.NoError(t, err)
		assert.Equal(t, []string{"leapmux", "leapmux-review"}, st.Enabled)
		require.Len(t, outcomes, 1)
		assert.Equal(t, "leapmux-review", outcomes[0].Name)
		assert.Equal(t, ActionInstalled, outcomes[0].Action)
		raw, err := os.ReadFile(leapmuxMD)
		require.NoError(t, err)
		assert.Equal(t, "# user edit\n", string(raw))

		// Adding leapmux again with StrictConflicts fails because leapmux itself is modified.
		_, _, err = ApplyWorkerStateUpdate(dataDir, homeDir, WorkerStateUpdate{
			AddSkills:       []string{"leapmux"},
			SyncNow:         true,
			StrictConflicts: true,
		})
		require.ErrorIs(t, err, ErrSkillModified)

		// Unmanaged directory at leapmux-help fails with ErrSkillUnmanaged and does not persist leapmux-help.
		unmanagedDir := filepath.Join(homeDir, ".agents", "skills", "leapmux-help")
		require.NoError(t, os.MkdirAll(unmanagedDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(unmanagedDir, "SKILL.md"), []byte("# unmanaged\n"), 0o644))
		_, _, err = ApplyWorkerStateUpdate(dataDir, homeDir, WorkerStateUpdate{
			AddSkills:       []string{"leapmux-help"},
			SyncNow:         true,
			StrictConflicts: true,
		})
		require.ErrorIs(t, err, ErrSkillUnmanaged)
		loaded, err := LoadWorkerState(dataDir)
		require.NoError(t, err)
		assert.Equal(t, []string{"leapmux", "leapmux-review"}, loaded.Enabled)
	})

	t.Run("RemoveSkills removes already-disabled or locally-installed skills on disk (RIPPLE-2)", func(t *testing.T) {
		dataDir := t.TempDir()
		homeDir := t.TempDir()

		// Install leapmux-help directly on disk without enabling it in WorkerState.
		agentsDir := filepath.Join(homeDir, ".agents", "skills")
		_, err := Install(agentsDir, []string{"leapmux-help"}, Options{})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(agentsDir, "leapmux-help", "SKILL.md"), []byte("# edited\n"), 0o644))

		// StrictConflicts without Force refuses to remove the modified skill.
		_, _, err = ApplyWorkerStateUpdate(dataDir, homeDir, WorkerStateUpdate{
			RemoveSkills:    []string{"leapmux-help"},
			StrictConflicts: true,
		})
		require.ErrorIs(t, err, ErrSkillModified)
		assert.FileExists(t, filepath.Join(agentsDir, "leapmux-help", "SKILL.md"))

		// Force removes it even though it was never in WorkerState.Enabled.
		st, outcomes, err := ApplyWorkerStateUpdate(dataDir, homeDir, WorkerStateUpdate{
			RemoveSkills:    []string{"leapmux-help"},
			Force:           true,
			StrictConflicts: true,
		})
		require.NoError(t, err)
		assert.Empty(t, st.Enabled)
		require.Len(t, outcomes, 1)
		assert.Equal(t, ActionRemoved, outcomes[0].Action)
		assert.NoDirExists(t, filepath.Join(agentsDir, "leapmux-help"))
	})

	t.Run("AddAllSkills, RemoveAllSkills, AddTargets, and RemoveTargets", func(t *testing.T) {
		dataDir := t.TempDir()
		homeDir := t.TempDir()

		st, outcomes, err := ApplyWorkerStateUpdate(dataDir, homeDir, WorkerStateUpdate{
			AddAllSkills:    true,
			AddTargets:      []Target{TargetClaude},
			SyncNow:         true,
			StrictConflicts: true,
		})
		require.NoError(t, err)
		assert.Equal(t, Names(), st.Enabled)
		assert.Equal(t, []Target{TargetAgents, TargetClaude}, st.Targets)
		assert.Len(t, outcomes, len(Names())*2)

		// Removing one target succeeds.
		st, _, err = ApplyWorkerStateUpdate(dataDir, homeDir, WorkerStateUpdate{
			RemoveTargets: []Target{TargetAgents},
		})
		require.NoError(t, err)
		assert.Equal(t, []Target{TargetClaude}, st.Targets)

		// Removing the last remaining target fails with ErrInvalidWorkerStateUpdate.
		_, _, err = ApplyWorkerStateUpdate(dataDir, homeDir, WorkerStateUpdate{
			RemoveTargets: []Target{TargetClaude},
		})
		require.ErrorIs(t, err, ErrInvalidWorkerStateUpdate)

		// RemoveAllSkills removes every bundled skill from configured targets.
		st, outcomes, err = ApplyWorkerStateUpdate(dataDir, homeDir, WorkerStateUpdate{
			RemoveAllSkills: true,
			StrictConflicts: true,
		})
		require.NoError(t, err)
		assert.Empty(t, st.Enabled)
		assert.Len(t, outcomes, len(Names()))
		for _, o := range outcomes {
			assert.Equal(t, ActionRemoved, o.Action)
		}
	})

	t.Run("multi-target strict conflict fails before mutating any target or state", func(t *testing.T) {
		dataDir := t.TempDir()
		homeDir := t.TempDir()

		// Place an unmanaged leapmux skill in claude while agents is empty.
		claudeSkillDir := filepath.Join(homeDir, ".claude", "skills", "leapmux")
		require.NoError(t, os.MkdirAll(claudeSkillDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(claudeSkillDir, "SKILL.md"), []byte("# unmanaged\n"), 0o644))

		_, _, err := ApplyWorkerStateUpdate(dataDir, homeDir, WorkerStateUpdate{
			AddSkills:       []string{"leapmux"},
			AddTargets:      []Target{TargetClaude},
			SyncNow:         true,
			StrictConflicts: true,
		})
		require.ErrorIs(t, err, ErrSkillUnmanaged)
		assert.NoDirExists(t, filepath.Join(homeDir, ".agents", "skills", "leapmux"), "first target must not be written when second target fails strict preflight")
		loaded, err := LoadWorkerState(dataDir)
		require.NoError(t, err)
		assert.Empty(t, loaded.Enabled)
		assert.Equal(t, []Target{TargetAgents}, loaded.Targets)
	})
}

func TestInspectOne_MarkerlessForeignAndInvalidMarkersAreUnmanaged(t *testing.T) {
	t.Parallel()
	s, ok := Lookup("leapmux")
	require.True(t, ok)

	t.Run("empty skill directory is not_installed", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, s.Name), 0o755))
		assert.Equal(t, StateNotInstalled, InspectOne(dir, s).State)
	})

	t.Run("markerless directory with identical bundled bytes", func(t *testing.T) {
		dir := t.TempDir()
		_, err := Install(dir, []string{s.Name}, Options{})
		require.NoError(t, err)
		require.NoError(t, os.Remove(filepath.Join(dir, s.Name, MarkerFileName)))
		assert.Equal(t, StateUnmanaged, InspectOne(dir, s).State)
	})

	t.Run("foreign managed_by with identical bundled bytes", func(t *testing.T) {
		dir := t.TempDir()
		_, err := Install(dir, []string{s.Name}, Options{})
		require.NoError(t, err)
		foreign, err := json.Marshal(skillMarker{
			Name:      s.Name,
			ManagedBy: "other-tool",
			Version:   "1.0.0",
			Files:     map[string]string{"SKILL.md": sha256Hex([]byte(s.Content()))},
		})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, s.Name, MarkerFileName), foreign, 0o644))
		assert.Equal(t, StateUnmanaged, InspectOne(dir, s).State)
	})

	t.Run("non-empty directory without SKILL.md or marker", func(t *testing.T) {
		dir := t.TempDir()
		skillDir := filepath.Join(dir, s.Name)
		require.NoError(t, os.MkdirAll(skillDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(skillDir, "notes.txt"), []byte("user file\n"), 0o644))
		assert.Equal(t, StateUnmanaged, InspectOne(dir, s).State)
	})

	t.Run("marker with mismatched name", func(t *testing.T) {
		dir := t.TempDir()
		_, err := Install(dir, []string{s.Name}, Options{})
		require.NoError(t, err)
		wrongName, err := json.Marshal(skillMarker{
			Name:      "leapmux-review",
			ManagedBy: "leapmux",
			Version:   "1.0.0",
			Files:     map[string]string{"SKILL.md": sha256Hex([]byte(s.Content()))},
		})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, s.Name, MarkerFileName), wrongName, 0o644))
		assert.Equal(t, StateUnmanaged, InspectOne(dir, s).State)
	})

	t.Run("marker with empty files map (no v1 fallback)", func(t *testing.T) {
		dir := t.TempDir()
		skillDir := filepath.Join(dir, s.Name)
		require.NoError(t, os.MkdirAll(skillDir, 0o755))
		oldMD := []byte("---\nname: leapmux\ndescription: old\n---\n# old\n")
		require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), oldMD, 0o644))
		emptyFiles, err := json.Marshal(skillMarker{
			Name:        s.Name,
			ManagedBy:   "leapmux",
			Version:     "0.1.0",
			ContentHash: sha256Hex(oldMD),
		})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(skillDir, MarkerFileName), emptyFiles, 0o644))
		assert.Equal(t, StateUnmanaged, InspectOne(dir, s).State)
	})

	t.Run("marker with traversal key in files map", func(t *testing.T) {
		dir := t.TempDir()
		_, err := Install(dir, []string{s.Name}, Options{})
		require.NoError(t, err)
		badKey, err := json.Marshal(skillMarker{
			Name:      s.Name,
			ManagedBy: "leapmux",
			Version:   "1.0.0",
			Files:     map[string]string{"../escape.md": "abc"},
		})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, s.Name, MarkerFileName), badKey, 0o644))
		assert.Equal(t, StateUnmanaged, InspectOne(dir, s).State)
	})
}

func TestInspectOne_NewBundledFileCollisionReturnsModified(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	review, ok := Lookup("leapmux-review")
	require.True(t, ok)

	_, err := Install(dir, []string{review.Name}, Options{})
	require.NoError(t, err)

	// Simulate an older install whose marker did not track references/review-schemas.md,
	// while the user placed their own file at that path.
	skillDir := filepath.Join(dir, review.Name)
	rawMarker, err := os.ReadFile(filepath.Join(skillDir, MarkerFileName))
	require.NoError(t, err)
	var m skillMarker
	require.NoError(t, json.Unmarshal(rawMarker, &m))
	delete(m.Files, "references/review-schemas.md")
	updatedMarker, err := json.Marshal(m)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, MarkerFileName), updatedMarker, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "references", "review-schemas.md"), []byte("# user custom file\n"), 0o644))

	assert.Equal(t, StateModified, InspectOne(dir, review).State)
}

func TestInspectOne_LateUnsafeSymlinkAndNonRegularFileNotMasked(t *testing.T) {
	t.Parallel()
	review, ok := Lookup("leapmux-review")
	require.True(t, ok)

	t.Run("late symlink not masked by earlier mismatch", func(t *testing.T) {
		dir := t.TempDir()
		_, err := Install(dir, []string{review.Name}, Options{})
		require.NoError(t, err)

		skillDir := filepath.Join(dir, review.Name)
		require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# edited\n"), 0o644))
		outside := filepath.Join(t.TempDir(), "secret.md")
		require.NoError(t, os.WriteFile(outside, []byte("secret\n"), 0o644))
		refPath := filepath.Join(skillDir, "references", "review-schemas.md")
		require.NoError(t, os.Remove(refPath))
		createSymlinkOrSkip(t, outside, refPath)

		assert.Equal(t, StateUnmanaged, InspectOne(dir, review).State)
	})

	t.Run("late directory at bundled file path returns unmanaged", func(t *testing.T) {
		dir := t.TempDir()
		_, err := Install(dir, []string{review.Name}, Options{})
		require.NoError(t, err)

		skillDir := filepath.Join(dir, review.Name)
		require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# edited\n"), 0o644))
		refPath := filepath.Join(skillDir, "references", "review-schemas.md")
		require.NoError(t, os.Remove(refPath))
		require.NoError(t, os.MkdirAll(refPath, 0o755))

		assert.Equal(t, StateUnmanaged, InspectOne(dir, review).State)
	})
}

func TestLoadWorkerState_RejectsJSONNull(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, workerStateFileName), []byte("null\n"), 0o600))
	_, err := LoadWorkerState(dataDir)
	require.Error(t, err)
}

func TestWriteSkillFiles_NonCanonicalKeyAndSkipsUnchangedFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	review, ok := Lookup("leapmux-review")
	require.True(t, ok)
	_, err := Install(dir, []string{review.Name}, Options{})
	require.NoError(t, err)

	skillDir := filepath.Join(dir, review.Name)
	rawMarker, err := os.ReadFile(filepath.Join(skillDir, MarkerFileName))
	require.NoError(t, err)
	var m skillMarker
	require.NoError(t, json.Unmarshal(rawMarker, &m))
	// Store SKILL.md under a non-canonical "./SKILL.md" key with the current hash.
	skillHash := m.Files["SKILL.md"]
	delete(m.Files, "SKILL.md")
	m.Files["./SKILL.md"] = skillHash
	updated, err := json.Marshal(m)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, MarkerFileName), updated, 0o644))

	// Stamp an old mtime on an unchanged file to verify writeSkillFiles skips rewriting it.
	openAIPath := filepath.Join(skillDir, "agents", "openai.yaml")
	oldTime := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	require.NoError(t, os.Chtimes(openAIPath, oldTime, oldTime))

	require.NoError(t, writeSkillFiles(dir, review))
	assert.FileExists(t, filepath.Join(skillDir, "SKILL.md"))
	fi, err := os.Stat(openAIPath)
	require.NoError(t, err)
	assert.True(t, fi.ModTime().Equal(oldTime), "unchanged bundled file should not be rewritten")
}

func TestUninstall_PreservesUserFilesWithoutForce(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	review, ok := Lookup("leapmux-review")
	require.True(t, ok)
	_, err := Install(dir, []string{review.Name}, Options{})
	require.NoError(t, err)

	userNote := filepath.Join(dir, review.Name, "user-notes.md")
	require.NoError(t, os.WriteFile(userNote, []byte("# personal notes\n"), 0o644))

	outcomes, err := Uninstall(dir, []string{review.Name}, Options{})
	require.NoError(t, err)
	require.Len(t, outcomes, 1)
	assert.Equal(t, ActionRemoved, outcomes[0].Action)
	assert.NoFileExists(t, filepath.Join(dir, review.Name, "SKILL.md"))
	assert.NoFileExists(t, filepath.Join(dir, review.Name, MarkerFileName))
	assert.NoDirExists(t, filepath.Join(dir, review.Name, "references"))
	assert.FileExists(t, userNote, "non-force uninstall must preserve user-added files")
}

func TestUninstall_SkipProtectedOnSymlinkAndRefuseNonDir(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	require.NoError(t, os.MkdirAll(outside, 0o755))
	skillsDir := filepath.Join(base, "skills")
	require.NoError(t, os.MkdirAll(skillsDir, 0o755))
	createSymlinkOrSkip(t, outside, filepath.Join(skillsDir, "leapmux"))

	nonDirPath := filepath.Join(skillsDir, "leapmux-review")
	require.NoError(t, os.WriteFile(nonDirPath, []byte("not a dir\n"), 0o644))

	outcomes, err := Uninstall(skillsDir, []string{"leapmux", "leapmux-review"}, Options{SkipProtected: true})
	require.NoError(t, err)
	require.Len(t, outcomes, 2)
	assert.Equal(t, ActionSkippedUnmanaged, outcomes[0].Action)
	assert.Equal(t, ActionSkippedUnmanaged, outcomes[1].Action)

	// Even with Force: true, non-directory skill path must not be removed.
	_, err = Uninstall(skillsDir, []string{"leapmux-review"}, Options{Force: true})
	require.ErrorIs(t, err, ErrSkillUnmanaged)
	assert.FileExists(t, nonDirPath)
}

func TestInstallAndUninstall_BatchPreflightAtomic(t *testing.T) {
	t.Parallel()

	t.Run("install preflight prevents partial install", func(t *testing.T) {
		dir := t.TempDir()
		_, err := Install(dir, []string{"leapmux-review"}, Options{})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "leapmux-review", "SKILL.md"), []byte("# edit\n"), 0o644))

		_, err = Install(dir, []string{"leapmux", "leapmux-review"}, Options{})
		require.ErrorIs(t, err, ErrSkillModified)
		assert.NoDirExists(t, filepath.Join(dir, "leapmux"), "first skill must not be installed when second skill fails preflight")
	})

	t.Run("uninstall preflight prevents partial removal", func(t *testing.T) {
		dir := t.TempDir()
		_, err := Install(dir, []string{"leapmux", "leapmux-review"}, Options{})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "leapmux-review", "SKILL.md"), []byte("# edit\n"), 0o644))

		_, err = Uninstall(dir, []string{"leapmux", "leapmux-review"}, Options{})
		require.ErrorIs(t, err, ErrSkillModified)
		assert.FileExists(t, filepath.Join(dir, "leapmux", "SKILL.md"), "first skill must not be removed when second skill fails preflight")
	})
}

func TestInstall_SkipProtectedSkipsModifiedAndUnmanaged(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, err := Install(dir, []string{"leapmux"}, Options{})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "leapmux", "SKILL.md"), []byte("# user edit\n"), 0o644))

	unmanagedDir := filepath.Join(dir, "leapmux-review")
	require.NoError(t, os.MkdirAll(unmanagedDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(unmanagedDir, "SKILL.md"), []byte("# unmanaged\n"), 0o644))

	outcomes, err := Install(dir, []string{"leapmux", "leapmux-review", "leapmux-help"}, Options{SkipProtected: true})
	require.NoError(t, err)
	require.Len(t, outcomes, 3)
	assert.Equal(t, ActionSkippedModified, outcomes[0].Action)
	assert.Equal(t, ActionSkippedUnmanaged, outcomes[1].Action)
	assert.Equal(t, ActionInstalled, outcomes[2].Action)
}

func TestTargets_NormalizeAndResolveHelpers(t *testing.T) {
	t.Parallel()

	// StandardTargets returns an immutable copy.
	targets := StandardTargets()
	require.Equal(t, []Target{TargetAgents, TargetClaude, TargetCodex, TargetCursor, TargetGemini}, targets)
	targets[0] = "mutated"
	assert.Equal(t, TargetAgents, StandardTargets()[0])

	assert.True(t, IsCustomTarget("dir:/tmp/skills"))
	assert.True(t, IsCustomTarget("  dir:./foo"))
	assert.False(t, IsCustomTarget("agents"))

	// Empty and "auto" are rejected with ErrInvalidTarget.
	for _, bad := range []string{"", "   ", "auto", "AUTO", "bogus"} {
		_, err := NormalizeTarget(bad)
		require.ErrorIs(t, err, ErrInvalidTarget, "target %q must fail", bad)
	}

	// NormalizeTargets deduplicates in order and fails on invalid items.
	norm, err := NormalizeTargets([]string{"AGENTS", "claude", "agents", " gemini "})
	require.NoError(t, err)
	assert.Equal(t, []Target{TargetAgents, TargetClaude, TargetGemini}, norm)

	_, err = NormalizeTargets([]string{"agents", "auto"})
	require.ErrorIs(t, err, ErrInvalidTarget)

	// ResolveLocalTargetDirs validates upfront and deduplicates equivalent paths.
	baseDir := t.TempDir()
	dirs, labels, err := ResolveLocalTargetDirs(baseDir, []string{
		"AGENTS",
		"dir:" + filepath.Join(baseDir, ".agents", "skills"),
		"claude",
	})
	require.NoError(t, err)
	assert.Equal(t, []string{
		filepath.Join(baseDir, ".agents", "skills"),
		filepath.Join(baseDir, ".claude", "skills"),
	}, dirs)
	assert.Equal(t, []string{"agents", "claude"}, labels)

	_, _, err = ResolveLocalTargetDirs(baseDir, []string{"agents", "dir:"})
	require.ErrorIs(t, err, ErrInvalidTarget)
}

func TestResolveLocalTargetDir_RelativeAndTilde(t *testing.T) {
	t.Parallel()
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	projectDir := filepath.Join(t.TempDir(), "project")
	got, label, err := ResolveLocalTargetDir(projectDir, "dir:~/custom-skills")
	require.NoError(t, err)
	wantTilde := filepath.Join(home, "custom-skills")
	assert.Equal(t, wantTilde, got)
	assert.Equal(t, "dir:"+wantTilde, label)

	gotHome, homeLabel, err := ResolveLocalTargetDir(projectDir, "dir:~")
	require.NoError(t, err)
	assert.Equal(t, filepath.Clean(home), gotHome)
	assert.Equal(t, "dir:"+filepath.Clean(home), homeLabel)

	// Relative dir:<rel> stays CWD-relative and is NOT joined with baseDir.
	gotRel, relLabel, err := ResolveLocalTargetDir(projectDir, "dir:./custom/skills")
	require.NoError(t, err)
	wantRel := filepath.Join("custom", "skills")
	assert.Equal(t, wantRel, gotRel)
	assert.Equal(t, "dir:"+wantRel, relLabel)
}

func TestCheckSafeManagedInRoot_RejectsUnsafePaths(t *testing.T) {
	t.Parallel()
	skillDir := filepath.Join(t.TempDir(), "leapmux")
	require.NoError(t, os.MkdirAll(filepath.Join(skillDir, "dir-as-file"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "file-as-dir"), []byte("x"), 0o644))

	skillRoot, err := os.OpenRoot(skillDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = skillRoot.Close() })

	for _, bad := range []string{"", ".", "..", "../outside", "/abs/path"} {
		_, err := checkSafeManagedInRoot(skillRoot, bad)
		require.Error(t, err, "path %q must be rejected", bad)
	}

	_, err = checkSafeManagedInRoot(skillRoot, "dir-as-file")
	require.ErrorIs(t, err, ErrSkillUnmanaged)

	_, err = checkSafeManagedInRoot(skillRoot, "file-as-dir/sub.md")
	require.ErrorIs(t, err, ErrSkillUnmanaged)
}

func TestCatalogAndLookup_ReturnClonedSlices(t *testing.T) {
	cat := Catalog()
	require.NotEmpty(t, cat)
	origRel := cat[0].Files[0].RelPath
	cat[0].Files[0].RelPath = "mutated"
	assert.Equal(t, origRel, Catalog()[0].Files[0].RelPath)

	review, ok := Lookup("leapmux-review")
	require.True(t, ok)
	require.NotEmpty(t, review.References)
	origRef := review.References[0]
	review.References[0] = "mutated"
	review2, ok := Lookup("leapmux-review")
	require.True(t, ok)
	assert.Equal(t, origRef, review2.References[0])
}

func TestSkillReference_AllowsDoubleDotsInFilename(t *testing.T) {
	t.Parallel()
	s := Skill{
		Name: "demo",
		Files: []SkillFile{
			{RelPath: "references/v1..v2.md", Content: "diff\n", ContentHash: "abc"},
		},
	}
	assert.Empty(t, s.Content(), "Content returns empty when SKILL.md is absent")
	s.Files = append(s.Files, SkillFile{RelPath: "SKILL.md", Content: "# demo\n", ContentHash: "def"})
	assert.Equal(t, "# demo\n", s.Content())

	f, ok := s.Reference("v1..v2")
	require.True(t, ok)
	assert.Equal(t, "references/v1..v2.md", f.RelPath)

	_, ok = s.Reference("..")
	assert.False(t, ok)
	_, ok = s.Reference(".")
	assert.False(t, ok)
	_, ok = s.Reference("../SKILL.md")
	assert.False(t, ok)
}

func TestParseFrontmatter_BOMAndEOFAndUnknownFields(t *testing.T) {
	t.Parallel()

	fm, err := parseFrontmatter([]byte("\xef\xbb\xbf---  \nname: demo\ndescription: hello\n---"))
	require.NoError(t, err)
	assert.Equal(t, "demo", fm.Name)
	assert.Equal(t, "hello", fm.Description)

	_, err = parseFrontmatter([]byte("---\nname: demo\ndescription: hello\nunknown-key: nope\n---\n"))
	require.Error(t, err, "unknown frontmatter field must be rejected")
}

func TestApplyWorkerStateUpdate_PureUninstallAndCanonicalNamesAndPrevalidation(t *testing.T) {
	t.Parallel()

	t.Run("pure uninstall with Force does not clobber unrelated modified skill", func(t *testing.T) {
		dataDir := t.TempDir()
		homeDir := t.TempDir()

		_, _, err := ApplyWorkerStateUpdate(dataDir, homeDir, WorkerStateUpdate{
			Targets:             []Target{TargetAgents},
			UpdateEnabledSkills: true,
			EnabledSkills:       []string{" leapmux ", "leapmux-review"},
			SyncNow:             true,
		})
		require.NoError(t, err)

		// Canonical name must be persisted without whitespace.
		loaded, err := LoadWorkerState(dataDir)
		require.NoError(t, err)
		assert.Equal(t, []string{"leapmux", "leapmux-review"}, loaded.Enabled)

		modifiedPath := filepath.Join(homeDir, ".agents", "skills", "leapmux-review", "SKILL.md")
		require.NoError(t, os.WriteFile(modifiedPath, []byte("# user custom\n"), 0o644))

		// Force-disable leapmux only; SyncNow is false so leapmux-review must NOT be overwritten.
		_, _, err = ApplyWorkerStateUpdate(dataDir, homeDir, WorkerStateUpdate{
			UpdateEnabledSkills: true,
			EnabledSkills:       []string{"leapmux-review"},
			Force:               true,
		})
		require.NoError(t, err)
		assert.NoDirExists(t, filepath.Join(homeDir, ".agents", "skills", "leapmux"))
		got, err := os.ReadFile(modifiedPath)
		require.NoError(t, err)
		assert.Equal(t, "# user custom\n", string(got))
	})

	t.Run("blocked sync target fails before uninstalling disabled skill", func(t *testing.T) {
		dataDir := t.TempDir()
		homeDir := t.TempDir()

		_, _, err := ApplyWorkerStateUpdate(dataDir, homeDir, WorkerStateUpdate{
			Targets:             []Target{TargetAgents},
			UpdateEnabledSkills: true,
			EnabledSkills:       []string{"leapmux"},
			SyncNow:             true,
		})
		require.NoError(t, err)

		// Block ~/.claude/skills with a regular file.
		require.NoError(t, os.MkdirAll(filepath.Join(homeDir, ".claude"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(homeDir, ".claude", "skills"), []byte("blocker"), 0o644))

		_, _, err = ApplyWorkerStateUpdate(dataDir, homeDir, WorkerStateUpdate{
			Targets:             []Target{TargetAgents, TargetClaude},
			UpdateEnabledSkills: true,
			EnabledSkills:       []string{"leapmux-review"},
		})
		require.Error(t, err)
		assert.FileExists(t, filepath.Join(homeDir, ".agents", "skills", "leapmux", "SKILL.md"), "disabled skill must not be deleted when sync target validation fails")
	})

	t.Run("adding a new target syncs enabled skills only to new target when SyncNow is false", func(t *testing.T) {
		dataDir := t.TempDir()
		homeDir := t.TempDir()

		_, _, err := ApplyWorkerStateUpdate(dataDir, homeDir, WorkerStateUpdate{
			Targets:             []Target{TargetAgents},
			UpdateEnabledSkills: true,
			EnabledSkills:       []string{"leapmux"},
			SyncNow:             true,
		})
		require.NoError(t, err)

		// Edit existing agents copy, then add claude target with Force: true and SyncNow: false.
		agentsSkill := filepath.Join(homeDir, ".agents", "skills", "leapmux", "SKILL.md")
		require.NoError(t, os.WriteFile(agentsSkill, []byte("# kept local edit\n"), 0o644))

		_, _, err = ApplyWorkerStateUpdate(dataDir, homeDir, WorkerStateUpdate{
			Targets: []Target{TargetAgents, TargetClaude},
			Force:   true,
		})
		require.NoError(t, err)
		assert.FileExists(t, filepath.Join(homeDir, ".claude", "skills", "leapmux", "SKILL.md"))
		got, err := os.ReadFile(agentsSkill)
		require.NoError(t, err)
		assert.Equal(t, "# kept local edit\n", string(got))
	})
}

func TestApplyWorkerStateUpdate_DeltaBoundariesAndNarrowAddNewTarget(t *testing.T) {
	t.Parallel()

	t.Run("rejects conflicting delta combinations", func(t *testing.T) {
		dataDir := t.TempDir()
		homeDir := t.TempDir()

		cases := []struct {
			name string
			req  WorkerStateUpdate
		}{
			{
				name: "AddAllSkills with RemoveAllSkills",
				req:  WorkerStateUpdate{AddAllSkills: true, RemoveAllSkills: true},
			},
			{
				name: "AddAllSkills with RemoveSkills",
				req:  WorkerStateUpdate{AddAllSkills: true, RemoveSkills: []string{"leapmux"}},
			},
			{
				name: "RemoveAllSkills with AddSkills",
				req:  WorkerStateUpdate{RemoveAllSkills: true, AddSkills: []string{"leapmux"}},
			},
			{
				name: "Targets with AddTargets",
				req:  WorkerStateUpdate{Targets: []Target{TargetAgents}, AddTargets: []Target{TargetClaude}},
			},
			{
				name: "overlapping AddTargets and RemoveTargets",
				req:  WorkerStateUpdate{AddTargets: []Target{TargetClaude}, RemoveTargets: []Target{TargetClaude}},
			},
			{
				name: "overlapping AddSkills and RemoveSkills",
				req:  WorkerStateUpdate{AddSkills: []string{"leapmux"}, RemoveSkills: []string{" leapmux "}},
			},
		}
		for _, tc := range cases {
			_, _, err := ApplyWorkerStateUpdate(dataDir, homeDir, tc.req)
			require.ErrorIs(t, err, ErrInvalidWorkerStateUpdate, tc.name)
		}
	})

	t.Run("narrow AddSkills with new target keeps unrelated modified skill non-strict", func(t *testing.T) {
		dataDir := t.TempDir()
		homeDir := t.TempDir()

		// Enable leapmux on agents first.
		_, _, err := ApplyWorkerStateUpdate(dataDir, homeDir, WorkerStateUpdate{
			AddSkills: []string{"leapmux"},
			SyncNow:   true,
		})
		require.NoError(t, err)

		// Place an unmanaged copy of leapmux in claude before adding claude.
		claudeLeapmux := filepath.Join(homeDir, ".claude", "skills", "leapmux")
		require.NoError(t, os.MkdirAll(claudeLeapmux, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(claudeLeapmux, "SKILL.md"), []byte("# custom claude leapmux\n"), 0o644))

		// Installing leapmux-review while adding claude with StrictConflicts: true
		// must install leapmux-review into both targets and skip unrelated unmanaged leapmux in claude.
		st, outcomes, err := ApplyWorkerStateUpdate(dataDir, homeDir, WorkerStateUpdate{
			AddSkills:       []string{"leapmux-review"},
			AddTargets:      []Target{TargetClaude},
			SyncNow:         true,
			StrictConflicts: true,
		})
		require.NoError(t, err)
		assert.Equal(t, []string{"leapmux", "leapmux-review"}, st.Enabled)
		assert.Equal(t, []Target{TargetAgents, TargetClaude}, st.Targets)
		assert.FileExists(t, filepath.Join(homeDir, ".agents", "skills", "leapmux-review", "SKILL.md"))
		assert.FileExists(t, filepath.Join(homeDir, ".claude", "skills", "leapmux-review", "SKILL.md"))

		var sawSkippedLeapmux bool
		for _, o := range outcomes {
			if o.Name == "leapmux" && o.Target == TargetClaude && o.Action == ActionSkippedUnmanaged {
				sawSkippedLeapmux = true
			}
		}
		assert.True(t, sawSkippedLeapmux, "unrelated unmanaged skill in new target should be skipped, got %+v", outcomes)
		got, err := os.ReadFile(filepath.Join(claudeLeapmux, "SKILL.md"))
		require.NoError(t, err)
		assert.Equal(t, "# custom claude leapmux\n", string(got))
	})
}

func TestDetectInstalledTargetsAndAutoResolution(t *testing.T) {
	t.Parallel()

	t.Run("fresh baseDir detects only agents", func(t *testing.T) {
		t.Parallel()
		baseDir := t.TempDir()
		assert.Equal(t, []Target{TargetAgents}, DetectInstalledTargets(baseDir))
		assert.Equal(t, []Target{TargetAgents}, DetectInstalledTargets(""))

		dirs, labels, err := ResolveLocalTargetDirs(baseDir, []string{"auto"})
		require.NoError(t, err)
		assert.Equal(t, []string{filepath.Join(baseDir, ".agents", "skills")}, dirs)
		assert.Equal(t, []string{"agents"}, labels)
	})

	t.Run("claude directory without skills subdir is detected, while others require skills subdir", func(t *testing.T) {
		t.Parallel()
		baseDir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(baseDir, ".claude"), 0o755))
		// .codex without skills/ must NOT trigger detection.
		require.NoError(t, os.MkdirAll(filepath.Join(baseDir, ".codex"), 0o755))
		// .cursor/skills and .gemini/skills DO trigger detection.
		require.NoError(t, os.MkdirAll(filepath.Join(baseDir, ".cursor", "skills"), 0o755))
		require.NoError(t, os.MkdirAll(filepath.Join(baseDir, ".gemini", "skills"), 0o755))

		assert.Equal(t, []Target{TargetAgents, TargetClaude, TargetCursor, TargetGemini}, DetectInstalledTargets(baseDir))

		// Case-insensitive AUTO deduplicates with explicit targets and custom dir: paths in first-seen order.
		dirs, labels, err := ResolveLocalTargetDirs(baseDir, []string{
			"claude",
			"AUTO",
			"dir:" + filepath.Join(baseDir, ".cursor", "skills"),
		})
		require.NoError(t, err)
		assert.Equal(t, []string{
			filepath.Join(baseDir, ".claude", "skills"),
			filepath.Join(baseDir, ".agents", "skills"),
			filepath.Join(baseDir, ".cursor", "skills"),
			filepath.Join(baseDir, ".gemini", "skills"),
		}, dirs)
		assert.Equal(t, []string{"claude", "agents", "cursor", "gemini"}, labels)
	})

	t.Run("file named .claude does not trigger TargetClaude", func(t *testing.T) {
		t.Parallel()
		baseDir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(baseDir, ".claude"), []byte("not a dir"), 0o644))
		assert.Equal(t, []Target{TargetAgents}, DetectInstalledTargets(baseDir))
	})

	t.Run("auto fails when baseDir is empty", func(t *testing.T) {
		t.Parallel()
		_, _, err := ResolveLocalTargetDirs("   ", []string{"auto"})
		require.Error(t, err)
	})
}

func TestInstallTargetsAndUninstallTargets_CrossTargetAtomicPreflight(t *testing.T) {
	t.Parallel()

	t.Run("InstallTargets leaves target 1 untouched when target 2 has conflict", func(t *testing.T) {
		t.Parallel()
		base := t.TempDir()
		dir1 := filepath.Join(base, ".agents", "skills")
		dir2 := filepath.Join(base, ".claude", "skills")

		// Seed target 2 with an unmanaged leapmux skill.
		require.NoError(t, os.MkdirAll(filepath.Join(dir2, "leapmux"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir2, "leapmux", "SKILL.md"), []byte("# unmanaged\n"), 0o644))

		_, err := InstallTargets([]string{dir1, dir2}, []string{"leapmux"}, Options{})
		require.ErrorIs(t, err, ErrSkillUnmanaged)
		assert.NoDirExists(t, dir1, "target 1 must not be created or mutated when target 2 fails preflight")
	})

	t.Run("InstallTargets installs cleanly across all targets when preflight passes", func(t *testing.T) {
		t.Parallel()
		base := t.TempDir()
		dir1 := filepath.Join(base, ".agents", "skills")
		dir2 := filepath.Join(base, ".claude", "skills")

		results, err := InstallTargets([]string{dir1, dir2}, []string{"leapmux", "leapmux-review"}, Options{})
		require.NoError(t, err)
		require.Len(t, results, 2)
		require.Len(t, results[0], 2)
		require.Len(t, results[1], 2)
		assert.Equal(t, ActionInstalled, results[0][0].Action)
		assert.Equal(t, ActionInstalled, results[1][1].Action)
		assert.FileExists(t, filepath.Join(dir1, "leapmux", "SKILL.md"))
		assert.FileExists(t, filepath.Join(dir2, "leapmux-review", "SKILL.md"))
	})

	t.Run("UninstallTargets leaves target 1 untouched when target 2 has local edits", func(t *testing.T) {
		t.Parallel()
		base := t.TempDir()
		dir1 := filepath.Join(base, ".agents", "skills")
		dir2 := filepath.Join(base, ".claude", "skills")

		_, err := InstallTargets([]string{dir1, dir2}, []string{"leapmux"}, Options{})
		require.NoError(t, err)

		// Modify target 2 copy locally.
		require.NoError(t, os.WriteFile(filepath.Join(dir2, "leapmux", "SKILL.md"), []byte("# local edit\n"), 0o644))

		_, err = UninstallTargets([]string{dir1, dir2}, []string{"leapmux"}, Options{})
		require.ErrorIs(t, err, ErrSkillModified)
		assert.FileExists(t, filepath.Join(dir1, "leapmux", "SKILL.md"), "target 1 must not be removed when target 2 fails preflight")

		// Force removes both targets cleanly.
		results, err := UninstallTargets([]string{dir1, dir2}, []string{"leapmux"}, Options{Force: true})
		require.NoError(t, err)
		require.Len(t, results, 2)
		assert.Equal(t, ActionRemoved, results[0][0].Action)
		assert.Equal(t, ActionRemoved, results[1][0].Action)
		assert.NoDirExists(t, filepath.Join(dir1, "leapmux"))
		assert.NoDirExists(t, filepath.Join(dir2, "leapmux"))
	})

	t.Run("empty dirs still validates skill names", func(t *testing.T) {
		t.Parallel()
		_, err := InstallTargets(nil, []string{"no-such-skill"}, Options{})
		require.ErrorIs(t, err, ErrUnknownSkill)

		res, err := InstallTargets(nil, []string{"leapmux"}, Options{})
		require.NoError(t, err)
		assert.Nil(t, res)

		_, err = UninstallTargets(nil, []string{"no-such-skill"}, Options{})
		require.ErrorIs(t, err, ErrUnknownSkill)

		res, err = UninstallTargets(nil, []string{"leapmux"}, Options{})
		require.NoError(t, err)
		assert.Nil(t, res)
	})
}
