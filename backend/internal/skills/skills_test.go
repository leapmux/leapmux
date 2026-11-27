package skills

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBundledSkills_MatchRepoRoot(t *testing.T) {
	// Ensure repo-root skills/ (used by `npx skills add leapmux/leapmux` and
	// Hugo site) stays byte-for-byte identical to backend/internal/skills/catalog.
	repoSkills := filepath.Join("..", "..", "..", "skills")
	require.NoError(t, VerifyDirectoryMatchesCatalog(repoSkills))

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
	assert.NotContains(t, handoff.Content, "keep full conversation history")
	assert.NotContains(t, handoff.Content, "conversation fork")
	assert.NotContains(t, handoff.Content, "Fork with conversation context")
}

func TestInstallStatusDriftAndUninstall(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".agents", "skills")

	// Initially everything is not_installed.
	for _, st := range InspectAll(dir) {
		assert.Equal(t, StateNotInstalled, st.State)
	}

	// Install two skills (including one with references/ + agents/openai.yaml).
	outcomes, err := Install(dir, []string{"leapmux", "leapmux-review"}, InstallOptions{})
	require.NoError(t, err)
	require.Len(t, outcomes, 2)
	assert.Equal(t, "installed", outcomes[0].Action)
	assert.Equal(t, "installed", outcomes[1].Action)
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
	outcomes, err = Install(dir, []string{"leapmux"}, InstallOptions{})
	require.NoError(t, err)
	assert.Equal(t, "unchanged", outcomes[0].Action)

	// Simulate user editing a companion reference file -> detected as modified.
	refFile := filepath.Join(dir, "leapmux-review", "references", "review-schemas.md")
	require.NoError(t, os.WriteFile(refFile, []byte("# local schema tweak\n"), 0o644))
	assert.Equal(t, StateModified, InspectOne(dir, review).State)

	// Simulate user editing SKILL.md locally -> detected as modified and
	// refused without --force.
	skillFile := filepath.Join(dir, "leapmux", "SKILL.md")
	require.NoError(t, os.WriteFile(skillFile, []byte("# custom user edit\n"), 0o644))
	assert.Equal(t, StateModified, InspectOne(dir, s).State)

	_, err = Install(dir, []string{"leapmux"}, InstallOptions{Force: false})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "local edits")

	// SyncEnabled skips modified skills without clobbering user edits.
	syncOut, err := SyncEnabled(dir, []string{"leapmux"}, false)
	require.NoError(t, err)
	require.Len(t, syncOut, 1)
	assert.Equal(t, "skipped_modified", syncOut[0].Action)

	// --force overwrites and restores up_to_date.
	outcomes, err = Install(dir, []string{"leapmux"}, InstallOptions{Force: true})
	require.NoError(t, err)
	assert.Equal(t, "updated", outcomes[0].Action)
	assert.Equal(t, StateUpToDate, InspectOne(dir, s).State)

	// Uninstall removes cleanly.
	rmOut, err := Uninstall(dir, []string{"leapmux"}, InstallOptions{})
	require.NoError(t, err)
	assert.Equal(t, "removed", rmOut[0].Action)
	assert.Equal(t, StateNotInstalled, InspectOne(dir, s).State)
}

func TestLegacyV1MarkerAndObsoleteFilePruning(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".agents", "skills")
	review, ok := Lookup("leapmux-review")
	require.True(t, ok)

	// Simulate an older v1 install that had only SKILL.md and a v1 marker
	// without the Files map.
	skillDir := filepath.Join(dir, review.Name)
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	oldSkillMD := []byte("---\nname: leapmux-review\ndescription: old\n---\n# old\n")
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), oldSkillMD, 0o644))
	v1Marker, err := json.Marshal(skillMarker{
		Name:        review.Name,
		ManagedBy:   "leapmux",
		Version:     "0.1.0",
		ContentHash: sha256Hex(oldSkillMD),
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, MarkerFileName), v1Marker, 0o644))

	// Untouched v1 install classifies as outdated and upgrades cleanly without --force.
	assert.Equal(t, StateOutdated, InspectOne(dir, review).State)
	outcomes, err := Install(dir, []string{review.Name}, InstallOptions{})
	require.NoError(t, err)
	assert.Equal(t, "updated", outcomes[0].Action)
	assert.Equal(t, StateUpToDate, InspectOne(dir, review).State)

	// Simulate an obsolete previously-managed file alongside a user-added file.
	obsoletePath := filepath.Join(skillDir, "references", "old-removed.md")
	obsoleteContent := []byte("# removed in newer version\n")
	require.NoError(t, os.WriteFile(obsoletePath, obsoleteContent, 0o644))
	userNotePath := filepath.Join(skillDir, "user-notes.md")
	require.NoError(t, os.WriteFile(userNotePath, []byte("# keep me\n"), 0o644))

	// Record obsolete file in marker as previously managed and downgrade version/hash.
	m, err := readSkillMarker(skillDir)
	require.NoError(t, err)
	m.Files["references/old-removed.md"] = sha256Hex(obsoleteContent)
	// Also corrupt bundled hash expectation by making references/review-schemas.md absent
	// while removing it from m.Files so state is outdated.
	require.NoError(t, os.Remove(filepath.Join(skillDir, "references", "review-schemas.md")))
	delete(m.Files, "references/review-schemas.md")
	rawMarker, err := json.Marshal(m)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, MarkerFileName), rawMarker, 0o644))

	assert.Equal(t, StateOutdated, InspectOne(dir, review).State)
	outcomes, err = Install(dir, []string{review.Name}, InstallOptions{})
	require.NoError(t, err)
	assert.Equal(t, "updated", outcomes[0].Action)
	_, statErr := os.Stat(obsoletePath)
	assert.ErrorIs(t, statErr, os.ErrNotExist, "unmodified retired managed file should be pruned")
	assert.FileExists(t, userNotePath, "user-added extra file must be preserved")
}

func TestSymlinkSafety(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	require.NoError(t, os.MkdirAll(outside, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "SKILL.md"), []byte("outside\n"), 0o644))

	skillsDir := filepath.Join(base, ".agents", "skills")
	require.NoError(t, os.MkdirAll(skillsDir, 0o755))
	require.NoError(t, os.Symlink(outside, filepath.Join(skillsDir, "leapmux")))

	s, ok := Lookup("leapmux")
	require.True(t, ok)
	assert.Equal(t, StateUnmanaged, InspectOne(skillsDir, s).State)

	// Install refuses symlinked skill directory even with --force.
	_, err := Install(skillsDir, []string{"leapmux"}, InstallOptions{Force: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symlink")

	// Uninstall also refuses symlinked skill directory even with --force.
	_, err = Uninstall(skillsDir, []string{"leapmux"}, InstallOptions{Force: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symlink")
}

func TestResolveTargetDir_Gemini(t *testing.T) {
	dir, err := ResolveTargetDir("/home/dev", "gemini")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("/home/dev", ".gemini", "skills"), dir)
}

func TestWorkerState_RoundTrip(t *testing.T) {
	dataDir := t.TempDir()
	st, err := LoadWorkerState(dataDir)
	require.NoError(t, err)
	assert.False(t, st.AutoSync)
	assert.Empty(t, st.Enabled)
	assert.Equal(t, []string{TargetAgents}, st.Targets)

	want := WorkerState{
		AutoSync: true,
		Enabled:  []string{"leapmux", "leapmux-review", "leapmux-help", "unknown-skill"},
		Targets:  []string{"agents", "claude", "gemini", "invalid-target"},
	}
	require.NoError(t, SaveWorkerState(dataDir, want))

	got, err := LoadWorkerState(dataDir)
	require.NoError(t, err)
	assert.True(t, got.AutoSync)
	assert.Equal(t, []string{"leapmux", "leapmux-review", "leapmux-help"}, got.Enabled)
	assert.Equal(t, []string{"agents", "claude", "gemini"}, got.Targets)
}

func TestUninstall_SkipProtectedPreservesModifiedSkills(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, err := Install(dir, []string{"leapmux", "leapmux-review"}, InstallOptions{})
	require.NoError(t, err)
	modifiedPath := filepath.Join(dir, "leapmux-review", "SKILL.md")
	require.NoError(t, os.WriteFile(modifiedPath, []byte("# local edit\n"), 0o644))

	outcomes, err := Uninstall(dir, []string{"leapmux", "leapmux-review"}, InstallOptions{SkipProtected: true})
	require.NoError(t, err)
	require.Len(t, outcomes, 2)
	assert.Equal(t, "removed", outcomes[0].Action)
	assert.Equal(t, "skipped_modified", outcomes[1].Action)
	assert.NoDirExists(t, filepath.Join(dir, "leapmux"))
	assert.FileExists(t, modifiedPath)
}

func TestApplyWorkerStateUpdate_ValidationAndSkipProtected(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	homeDir := t.TempDir()

	_, err := ApplyWorkerStateUpdate(dataDir, homeDir, WorkerStateUpdate{
		UpdateEnabledSkills: true,
		EnabledSkills:       []string{"no-such-skill"},
	})
	require.ErrorIs(t, err, ErrInvalidWorkerStateUpdate)

	autoSync := true
	st, err := ApplyWorkerStateUpdate(dataDir, homeDir, WorkerStateUpdate{
		AutoSync:            &autoSync,
		Targets:             []string{"agents"},
		UpdateEnabledSkills: true,
		EnabledSkills:       []string{"leapmux", "leapmux-review"},
		SyncNow:             true,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"leapmux", "leapmux-review"}, st.Enabled)

	modifiedPath := filepath.Join(homeDir, ".agents", "skills", "leapmux-review", "SKILL.md")
	require.NoError(t, os.WriteFile(modifiedPath, []byte("# user edit\n"), 0o644))

	// Disabling both removes clean leapmux and skips modified leapmux-review.
	st, err = ApplyWorkerStateUpdate(dataDir, homeDir, WorkerStateUpdate{
		UpdateEnabledSkills: true,
		EnabledSkills:       []string{},
	})
	require.NoError(t, err)
	assert.Empty(t, st.Enabled)
	assert.NoDirExists(t, filepath.Join(homeDir, ".agents", "skills", "leapmux"))
	assert.FileExists(t, modifiedPath)
}
