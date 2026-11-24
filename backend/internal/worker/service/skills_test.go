package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/skills"
)

func TestWorkerSkills_GetUpdateAndStartupSync(t *testing.T) {
	svc, d, _ := setupTestService(t)
	svc.HomeDir = t.TempDir()
	svc.DataDir = t.TempDir()

	// Initial state: auto-sync off, all bundled skills not_installed.
	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerSystemServiceGetWorkerSkillsProcedure, &leapmuxv1.GetWorkerSkillsRequest{}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var initial leapmuxv1.GetWorkerSkillsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &initial))
	assert.False(t, initial.GetAutoSync())
	assert.Equal(t, []string{skills.TargetAgents}, initial.GetTargets())
	require.Len(t, initial.GetSkills(), len(skills.Catalog()))
	for _, s := range initial.GetSkills() {
		assert.False(t, s.GetEnabled())
		assert.Equal(t, string(skills.StateNotInstalled), s.GetState())
	}

	// Enable auto-sync + two skills across agents and claude targets.
	autoSync := true
	w = newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerSystemServiceUpdateWorkerSkillsProcedure, &leapmuxv1.UpdateWorkerSkillsRequest{
		AutoSync:            &autoSync,
		UpdateEnabledSkills: true,
		EnabledSkills:       []string{"leapmux", "leapmux-review"},
		Targets:             []string{"agents", "claude"},
		SyncNow:             true,
	}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var updated leapmuxv1.UpdateWorkerSkillsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &updated))
	assert.True(t, updated.GetState().GetAutoSync())
	assert.Equal(t, []string{"agents", "claude"}, updated.GetState().GetTargets())

	agentsSkill := filepath.Join(svc.HomeDir, ".agents", "skills", "leapmux", "SKILL.md")
	claudeSkill := filepath.Join(svc.HomeDir, ".claude", "skills", "leapmux-review", "SKILL.md")
	assert.FileExists(t, agentsSkill)
	assert.FileExists(t, claudeSkill)

	// Simulate startup auto-sync restoring a deleted enabled skill while
	// preserving a user-edited skill.
	require.NoError(t, os.RemoveAll(filepath.Join(svc.HomeDir, ".agents", "skills", "leapmux")))
	require.NoError(t, os.WriteFile(claudeSkill, []byte("# user custom\n"), 0o644))

	svc.SyncConfiguredSkills()
	assert.FileExists(t, agentsSkill, "startup auto-sync must reinstall enabled skill")
	gotCustom, err := os.ReadFile(claudeSkill)
	require.NoError(t, err)
	assert.Equal(t, "# user custom\n", string(gotCustom), "startup auto-sync must not overwrite local edits")

	// Disabling a clean managed skill removes it from disk.
	w = newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerSystemServiceUpdateWorkerSkillsProcedure, &leapmuxv1.UpdateWorkerSkillsRequest{
		UpdateEnabledSkills: true,
		EnabledSkills:       []string{"leapmux-review"},
	}, w)
	require.Empty(t, w.errors)
	_, statErr := os.Stat(agentsSkill)
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}
