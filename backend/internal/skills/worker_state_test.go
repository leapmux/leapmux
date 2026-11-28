package skills

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyWorkerStateUpdate_RemoveTargetsUninstallsManagedSkills(t *testing.T) {
	t.Parallel()

	t.Run("RemoveTargets uninstalls all previously enabled skills from removed target and reports outcomes", func(t *testing.T) {
		t.Parallel()
		dataDir := t.TempDir()
		homeDir := t.TempDir()

		_, _, err := ApplyWorkerStateUpdate(dataDir, homeDir, WorkerStateUpdate{
			AddSkills:       []string{"leapmux", "leapmux-review"},
			AddTargets:      []Target{TargetClaude},
			SyncNow:         true,
			StrictConflicts: true,
		})
		require.NoError(t, err)

		agentsLeapmux := filepath.Join(homeDir, ".agents", "skills", "leapmux", "SKILL.md")
		agentsReview := filepath.Join(homeDir, ".agents", "skills", "leapmux-review", "SKILL.md")
		claudeLeapmux := filepath.Join(homeDir, ".claude", "skills", "leapmux", "SKILL.md")
		claudeReview := filepath.Join(homeDir, ".claude", "skills", "leapmux-review", "SKILL.md")
		require.FileExists(t, agentsLeapmux)
		require.FileExists(t, agentsReview)
		require.FileExists(t, claudeLeapmux)
		require.FileExists(t, claudeReview)

		st, outcomes, err := ApplyWorkerStateUpdate(dataDir, homeDir, WorkerStateUpdate{
			RemoveTargets:   []Target{TargetClaude},
			StrictConflicts: true,
		})
		require.NoError(t, err)
		assert.Equal(t, []Target{TargetAgents}, st.Targets)
		assert.Equal(t, []string{"leapmux", "leapmux-review"}, st.Enabled)

		// All enabled skills must be removed from the dropped claude target and reported.
		assert.NoDirExists(t, filepath.Join(homeDir, ".claude", "skills", "leapmux"))
		assert.NoDirExists(t, filepath.Join(homeDir, ".claude", "skills", "leapmux-review"))
		assert.FileExists(t, agentsLeapmux)
		assert.FileExists(t, agentsReview)

		require.Len(t, outcomes, 2)
		assert.Equal(t, TargetOutcome{
			Name:   "leapmux",
			Target: TargetClaude,
			Action: ActionRemoved,
			Path:   claudeLeapmux,
		}, outcomes[0])
		assert.Equal(t, TargetOutcome{
			Name:   "leapmux-review",
			Target: TargetClaude,
			Action: ActionRemoved,
			Path:   claudeReview,
		}, outcomes[1])
	})

	t.Run("Targets replacement uninstalls previously enabled skills from dropped target", func(t *testing.T) {
		t.Parallel()
		dataDir := t.TempDir()
		homeDir := t.TempDir()

		_, _, err := ApplyWorkerStateUpdate(dataDir, homeDir, WorkerStateUpdate{
			Targets:             []Target{TargetAgents, TargetClaude},
			UpdateEnabledSkills: true,
			EnabledSkills:       []string{"leapmux"},
			SyncNow:             true,
		})
		require.NoError(t, err)
		claudeLeapmux := filepath.Join(homeDir, ".claude", "skills", "leapmux", "SKILL.md")
		require.FileExists(t, claudeLeapmux)

		st, outcomes, err := ApplyWorkerStateUpdate(dataDir, homeDir, WorkerStateUpdate{
			Targets: []Target{TargetAgents},
		})
		require.NoError(t, err)
		assert.Equal(t, []Target{TargetAgents}, st.Targets)
		assert.NoDirExists(t, filepath.Join(homeDir, ".claude", "skills", "leapmux"))
		assert.FileExists(t, filepath.Join(homeDir, ".agents", "skills", "leapmux", "SKILL.md"))
		require.Len(t, outcomes, 1)
		assert.Equal(t, TargetOutcome{
			Name:   "leapmux",
			Target: TargetClaude,
			Action: ActionRemoved,
			Path:   claudeLeapmux,
		}, outcomes[0])
	})

	t.Run("RemoveTargets respects StrictConflicts, SkipProtected, and Force for modified skills", func(t *testing.T) {
		t.Parallel()
		dataDir := t.TempDir()
		homeDir := t.TempDir()

		_, _, err := ApplyWorkerStateUpdate(dataDir, homeDir, WorkerStateUpdate{
			AddSkills:  []string{"leapmux", "leapmux-review"},
			AddTargets: []Target{TargetClaude},
			SyncNow:    true,
		})
		require.NoError(t, err)

		claudeReview := filepath.Join(homeDir, ".claude", "skills", "leapmux-review", "SKILL.md")
		require.NoError(t, os.WriteFile(claudeReview, []byte("# local claude edit\n"), 0o644))

		// StrictConflicts without Force fails before mutating disk or state.
		_, _, err = ApplyWorkerStateUpdate(dataDir, homeDir, WorkerStateUpdate{
			RemoveTargets:   []Target{TargetClaude},
			StrictConflicts: true,
		})
		require.ErrorIs(t, err, ErrSkillModified)
		assert.FileExists(t, filepath.Join(homeDir, ".claude", "skills", "leapmux", "SKILL.md"))
		loaded, err := LoadWorkerState(dataDir)
		require.NoError(t, err)
		assert.Equal(t, []Target{TargetAgents, TargetClaude}, loaded.Targets)

		// Non-strict skips modified skill while removing clean skill and dropping target.
		st, outcomes, err := ApplyWorkerStateUpdate(dataDir, homeDir, WorkerStateUpdate{
			RemoveTargets: []Target{TargetClaude},
		})
		require.NoError(t, err)
		assert.Equal(t, []Target{TargetAgents}, st.Targets)
		assert.NoDirExists(t, filepath.Join(homeDir, ".claude", "skills", "leapmux"))
		assert.FileExists(t, claudeReview)
		require.Len(t, outcomes, 2)
		assert.Equal(t, ActionRemoved, outcomes[0].Action)
		assert.Equal(t, ActionSkippedModified, outcomes[1].Action)

		// Re-add claude (with Force to restore), re-modify, and Force-remove target.
		_, _, err = ApplyWorkerStateUpdate(dataDir, homeDir, WorkerStateUpdate{
			AddTargets: []Target{TargetClaude},
			Force:      true,
		})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(claudeReview, []byte("# local claude edit 2\n"), 0o644))

		st, outcomes, err = ApplyWorkerStateUpdate(dataDir, homeDir, WorkerStateUpdate{
			RemoveTargets: []Target{TargetClaude},
			Force:         true,
		})
		require.NoError(t, err)
		assert.Equal(t, []Target{TargetAgents}, st.Targets)
		assert.NoDirExists(t, filepath.Join(homeDir, ".claude", "skills", "leapmux-review"))
		require.Len(t, outcomes, 2)
		assert.Equal(t, ActionRemoved, outcomes[0].Action)
		assert.Equal(t, ActionRemoved, outcomes[1].Action)
	})

	t.Run("combining RemoveSkills and RemoveTargets removes disabled skills everywhere and all enabled skills from removed target", func(t *testing.T) {
		t.Parallel()
		dataDir := t.TempDir()
		homeDir := t.TempDir()

		_, _, err := ApplyWorkerStateUpdate(dataDir, homeDir, WorkerStateUpdate{
			AddSkills:  []string{"leapmux", "leapmux-review"},
			AddTargets: []Target{TargetClaude},
			SyncNow:    true,
		})
		require.NoError(t, err)

		// Remove leapmux globally AND drop claude target.
		// agents keeps leapmux-review and removes leapmux; claude removes both.
		st, outcomes, err := ApplyWorkerStateUpdate(dataDir, homeDir, WorkerStateUpdate{
			RemoveSkills:    []string{"leapmux"},
			RemoveTargets:   []Target{TargetClaude},
			StrictConflicts: true,
		})
		require.NoError(t, err)
		assert.Equal(t, []Target{TargetAgents}, st.Targets)
		assert.Equal(t, []string{"leapmux-review"}, st.Enabled)

		assert.NoDirExists(t, filepath.Join(homeDir, ".agents", "skills", "leapmux"))
		assert.FileExists(t, filepath.Join(homeDir, ".agents", "skills", "leapmux-review", "SKILL.md"))
		assert.NoDirExists(t, filepath.Join(homeDir, ".claude", "skills", "leapmux"))
		assert.NoDirExists(t, filepath.Join(homeDir, ".claude", "skills", "leapmux-review"))

		require.Len(t, outcomes, 3)
		assert.Equal(t, TargetOutcome{
			Name:   "leapmux",
			Target: TargetAgents,
			Action: ActionRemoved,
			Path:   filepath.Join(homeDir, ".agents", "skills", "leapmux", "SKILL.md"),
		}, outcomes[0])
		assert.Equal(t, TargetOutcome{
			Name:   "leapmux",
			Target: TargetClaude,
			Action: ActionRemoved,
			Path:   filepath.Join(homeDir, ".claude", "skills", "leapmux", "SKILL.md"),
		}, outcomes[1])
		assert.Equal(t, TargetOutcome{
			Name:   "leapmux-review",
			Target: TargetClaude,
			Action: ActionRemoved,
			Path:   filepath.Join(homeDir, ".claude", "skills", "leapmux-review", "SKILL.md"),
		}, outcomes[2])
	})

	t.Run("removing an unconfigured target does not touch its disk directory", func(t *testing.T) {
		t.Parallel()
		dataDir := t.TempDir()
		homeDir := t.TempDir()

		// Install leapmux into claude outside WorkerState (WorkerState only tracks agents).
		claudeDir := filepath.Join(homeDir, ".claude", "skills")
		_, err := Install(claudeDir, []string{"leapmux"}, Options{})
		require.NoError(t, err)
		_, _, err = ApplyWorkerStateUpdate(dataDir, homeDir, WorkerStateUpdate{
			AddSkills: []string{"leapmux"},
			SyncNow:   true,
		})
		require.NoError(t, err)

		st, outcomes, err := ApplyWorkerStateUpdate(dataDir, homeDir, WorkerStateUpdate{
			RemoveTargets: []Target{TargetClaude},
		})
		require.NoError(t, err)
		assert.Equal(t, []Target{TargetAgents}, st.Targets)
		assert.Empty(t, outcomes)
		assert.FileExists(t, filepath.Join(claudeDir, "leapmux", "SKILL.md"))
	})
}
