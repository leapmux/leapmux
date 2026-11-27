package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/skills"
)

func getWorkerSkills(t *testing.T, d *testServiceDispatcher) *leapmuxv1.GetWorkerSkillsResponse {
	t.Helper()
	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerSystemServiceGetWorkerSkillsProcedure, &leapmuxv1.GetWorkerSkillsRequest{}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.GetWorkerSkillsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	return &resp
}

func updateWorkerSkillsOK(t *testing.T, d *testServiceDispatcher, req *leapmuxv1.UpdateWorkerSkillsRequest) *leapmuxv1.UpdateWorkerSkillsResponse {
	t.Helper()
	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerSystemServiceUpdateWorkerSkillsProcedure, req, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.UpdateWorkerSkillsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	return &resp
}

func findSkillStatus(t *testing.T, resp *leapmuxv1.GetWorkerSkillsResponse, name string) *leapmuxv1.WorkerSkillStatus {
	t.Helper()
	for _, s := range resp.GetSkills() {
		if s.GetName() == name {
			return s
		}
	}
	t.Fatalf("skill %q not found in response", name)
	return nil
}

func TestWorkerSkills_GetUpdateAndStartupSync(t *testing.T) {
	svc, d, _ := setupTestService(t)
	svc.HomeDir = t.TempDir()
	svc.DataDir = t.TempDir()

	// Initial state: auto-sync off, all bundled skills not_installed.
	initial := getWorkerSkills(t, d)
	assert.False(t, initial.GetAutoSync())
	assert.Equal(t, []string{string(skills.TargetAgents)}, initial.GetTargets())
	require.Len(t, initial.GetSkills(), len(skills.Catalog()))
	for _, s := range initial.GetSkills() {
		assert.False(t, s.GetEnabled())
		assert.Equal(t, string(skills.StateNotInstalled), s.GetState())
	}

	// Enable auto-sync + two skills across agents and claude targets.
	autoSync := true
	updated := updateWorkerSkillsOK(t, d, &leapmuxv1.UpdateWorkerSkillsRequest{
		AutoSync:            &autoSync,
		UpdateEnabledSkills: true,
		EnabledSkills:       []string{"leapmux", "leapmux-review"},
		Targets:             []string{"agents", "claude"},
		SyncNow:             true,
	})
	assert.True(t, updated.GetState().GetAutoSync())
	assert.Equal(t, []string{"agents", "claude"}, updated.GetState().GetTargets())
	assert.Len(t, updated.GetOutcomes(), 4)

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
	updateWorkerSkillsOK(t, d, &leapmuxv1.UpdateWorkerSkillsRequest{
		UpdateEnabledSkills: true,
		EnabledSkills:       []string{"leapmux-review"},
	})
	_, statErr := os.Stat(agentsSkill)
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestWorkerSkills_GetReportsReferencesAndAggregatesPerTargetDrift(t *testing.T) {
	svc, d, _ := setupTestService(t)
	svc.HomeDir = t.TempDir()
	svc.DataDir = t.TempDir()

	updateWorkerSkillsOK(t, d, &leapmuxv1.UpdateWorkerSkillsRequest{
		AddSkills:  []string{"leapmux", "leapmux-review"},
		AddTargets: []string{"claude"},
	})

	// Edit leapmux-review only in the secondary target (claude) while keeping
	// the primary target (agents) up to date.
	agentsReviewPath := filepath.Join(svc.HomeDir, ".agents", "skills", "leapmux-review", "SKILL.md")
	claudeReviewPath := filepath.Join(svc.HomeDir, ".claude", "skills", "leapmux-review", "SKILL.md")
	require.NoError(t, os.WriteFile(claudeReviewPath, []byte("# edited in claude\n"), 0o644))

	resp := getWorkerSkills(t, d)
	assert.Equal(t, []string{"agents", "claude"}, resp.GetTargets())
	assert.Equal(t, filepath.Join(svc.HomeDir, ".agents", "skills"), resp.GetPrimarySkillsDir())

	// Every skill must carry its bundled references, and at least one bundled
	// skill has companion references.
	cat := skills.Catalog()
	require.Len(t, resp.GetSkills(), len(cat))
	sawRefs := false
	for i, want := range cat {
		got := resp.GetSkills()[i]
		assert.Equal(t, want.Name, got.GetName())
		assert.Equal(t, want.References, got.GetReferences())
		if len(want.References) > 0 {
			sawRefs = true
		}
	}
	assert.True(t, sawRefs, "catalog should include at least one skill with references")

	// Secondary-target drift surfaces as aggregate worst-case state on the row
	// while top-level Path and InstalledVersion remain anchored to primary.
	review := findSkillStatus(t, resp, "leapmux-review")
	assert.Equal(t, string(skills.StateModified), review.GetState())
	assert.Equal(t, agentsReviewPath, review.GetPath())
	assert.Equal(t, skills.BundledVersion(), review.GetInstalledVersion())
	require.Len(t, review.GetTargets(), 2)
	assert.Equal(t, "agents", review.GetTargets()[0].GetTarget())
	assert.Equal(t, string(skills.StateUpToDate), review.GetTargets()[0].GetState())
	assert.Equal(t, agentsReviewPath, review.GetTargets()[0].GetPath())
	assert.Equal(t, skills.BundledVersion(), review.GetTargets()[0].GetInstalledVersion())
	assert.Equal(t, "claude", review.GetTargets()[1].GetTarget())
	assert.Equal(t, string(skills.StateModified), review.GetTargets()[1].GetState())
	assert.Equal(t, claudeReviewPath, review.GetTargets()[1].GetPath())
	assert.Equal(t, skills.BundledVersion(), review.GetTargets()[1].GetInstalledVersion())

	// An unmodified installed skill stays up_to_date across both targets.
	core := findSkillStatus(t, resp, "leapmux")
	assert.Equal(t, string(skills.StateUpToDate), core.GetState())
	require.Len(t, core.GetTargets(), 2)
	assert.Equal(t, string(skills.StateUpToDate), core.GetTargets()[0].GetState())
	assert.Equal(t, string(skills.StateUpToDate), core.GetTargets()[1].GetState())
}

func TestWorkerSkills_AtomicDeltasAndOutcomes(t *testing.T) {
	svc, d, _ := setupTestService(t)
	svc.HomeDir = t.TempDir()
	svc.DataDir = t.TempDir()

	agentsDir := filepath.Join(svc.HomeDir, ".agents", "skills")
	claudeDir := filepath.Join(svc.HomeDir, ".claude", "skills")

	// AddSkills installs one skill and reports its exact outcome.
	addResp := updateWorkerSkillsOK(t, d, &leapmuxv1.UpdateWorkerSkillsRequest{
		AddSkills: []string{"leapmux"},
	})
	require.Len(t, addResp.GetOutcomes(), 1)
	assert.Equal(t, "leapmux", addResp.GetOutcomes()[0].GetName())
	assert.Equal(t, "agents", addResp.GetOutcomes()[0].GetTarget())
	assert.Equal(t, string(skills.ActionInstalled), addResp.GetOutcomes()[0].GetAction())
	assert.Equal(t, filepath.Join(agentsDir, "leapmux", "SKILL.md"), addResp.GetOutcomes()[0].GetPath())

	// AddTargets syncs already-enabled skills into the newly added target.
	addTargetResp := updateWorkerSkillsOK(t, d, &leapmuxv1.UpdateWorkerSkillsRequest{
		AddTargets: []string{"claude"},
	})
	assert.Equal(t, []string{"agents", "claude"}, addTargetResp.GetState().GetTargets())
	require.Len(t, addTargetResp.GetOutcomes(), 1)
	assert.Equal(t, "leapmux", addTargetResp.GetOutcomes()[0].GetName())
	assert.Equal(t, "claude", addTargetResp.GetOutcomes()[0].GetTarget())
	assert.Equal(t, string(skills.ActionInstalled), addTargetResp.GetOutcomes()[0].GetAction())
	assert.FileExists(t, filepath.Join(claudeDir, "leapmux", "SKILL.md"))

	// AddAllSkills enables and installs all remaining bundled skills across targets.
	allNames := skills.Names()
	addAllResp := updateWorkerSkillsOK(t, d, &leapmuxv1.UpdateWorkerSkillsRequest{
		AddAllSkills: true,
	})
	for _, s := range addAllResp.GetState().GetSkills() {
		assert.True(t, s.GetEnabled(), "skill %s should be enabled", s.GetName())
		assert.Equal(t, string(skills.StateUpToDate), s.GetState(), "skill %s should be up_to_date", s.GetName())
	}
	assert.Len(t, addAllResp.GetOutcomes(), (len(allNames)-1)*2)

	// RemoveTargets drops claude from configured targets while keeping agents.
	remTargetResp := updateWorkerSkillsOK(t, d, &leapmuxv1.UpdateWorkerSkillsRequest{
		RemoveTargets: []string{"claude"},
	})
	assert.Equal(t, []string{"agents"}, remTargetResp.GetState().GetTargets())

	// Modify leapmux-review on disk, disable it without Force (skipped), then
	// force-remove the already-disabled modified skill via RemoveSkills + Force.
	agentsReviewPath := filepath.Join(agentsDir, "leapmux-review", "SKILL.md")
	require.NoError(t, os.WriteFile(agentsReviewPath, []byte("# local edit\n"), 0o644))

	softRemove := updateWorkerSkillsOK(t, d, &leapmuxv1.UpdateWorkerSkillsRequest{
		RemoveSkills: []string{"leapmux-review"},
	})
	assert.False(t, findSkillStatus(t, softRemove.GetState(), "leapmux-review").GetEnabled())
	require.Len(t, softRemove.GetOutcomes(), 1)
	assert.Equal(t, string(skills.ActionSkippedModified), softRemove.GetOutcomes()[0].GetAction())
	assert.FileExists(t, agentsReviewPath)

	forceRemove := updateWorkerSkillsOK(t, d, &leapmuxv1.UpdateWorkerSkillsRequest{
		RemoveSkills: []string{"leapmux-review"},
		Force:        true,
	})
	require.Len(t, forceRemove.GetOutcomes(), 1)
	assert.Equal(t, "leapmux-review", forceRemove.GetOutcomes()[0].GetName())
	assert.Equal(t, "agents", forceRemove.GetOutcomes()[0].GetTarget())
	assert.Equal(t, string(skills.ActionRemoved), forceRemove.GetOutcomes()[0].GetAction())
	_, statErr := os.Stat(agentsReviewPath)
	assert.ErrorIs(t, statErr, os.ErrNotExist)

	// RemoveAllSkills disables and removes all bundled skills.
	removeAllResp := updateWorkerSkillsOK(t, d, &leapmuxv1.UpdateWorkerSkillsRequest{
		RemoveAllSkills: true,
	})
	assert.Len(t, removeAllResp.GetOutcomes(), len(allNames))
	for _, s := range removeAllResp.GetState().GetSkills() {
		assert.False(t, s.GetEnabled(), "skill %s should be disabled", s.GetName())
		assert.Equal(t, string(skills.StateNotInstalled), s.GetState(), "skill %s should be not_installed", s.GetName())
	}
}

func TestWorkerSkills_StrictConflictsAndErrorCodes(t *testing.T) {
	svc, d, _ := setupTestService(t)

	// Unconfigured HomeDir/DataDir returns FailedPrecondition.
	svc.HomeDir = ""
	svc.DataDir = ""
	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerSystemServiceUpdateWorkerSkillsProcedure, &leapmuxv1.UpdateWorkerSkillsRequest{
		AddSkills: []string{"leapmux"},
	}, w)
	require.Len(t, w.errors, 1)
	assert.Equal(t, int32(codes.FailedPrecondition), w.errors[0].code)

	svc.HomeDir = t.TempDir()
	svc.DataDir = t.TempDir()

	// Unknown skill returns NotFound.
	w = newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerSystemServiceUpdateWorkerSkillsProcedure, &leapmuxv1.UpdateWorkerSkillsRequest{
		AddSkills: []string{"no-such-skill"},
	}, w)
	require.Len(t, w.errors, 1)
	assert.Equal(t, int32(codes.NotFound), w.errors[0].code)

	// Invalid target returns InvalidArgument.
	w = newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerSystemServiceUpdateWorkerSkillsProcedure, &leapmuxv1.UpdateWorkerSkillsRequest{
		AddTargets: []string{"not-a-target"},
	}, w)
	require.Len(t, w.errors, 1)
	assert.Equal(t, int32(codes.InvalidArgument), w.errors[0].code)

	// Combining replacement and delta fields returns InvalidArgument.
	w = newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerSystemServiceUpdateWorkerSkillsProcedure, &leapmuxv1.UpdateWorkerSkillsRequest{
		UpdateEnabledSkills: true,
		AddSkills:           []string{"leapmux"},
	}, w)
	require.Len(t, w.errors, 1)
	assert.Equal(t, int32(codes.InvalidArgument), w.errors[0].code)

	// Seed a locally modified skill (leapmux-review) and verify StrictConflicts
	// returns Aborted when targeting it, while adding an unrelated skill succeeds
	// without clobbering the modified skill.
	updateWorkerSkillsOK(t, d, &leapmuxv1.UpdateWorkerSkillsRequest{
		AddSkills: []string{"leapmux-review"},
	})
	reviewPath := filepath.Join(svc.HomeDir, ".agents", "skills", "leapmux-review", "SKILL.md")
	require.NoError(t, os.WriteFile(reviewPath, []byte("# user modified\n"), 0o644))

	w = newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerSystemServiceUpdateWorkerSkillsProcedure, &leapmuxv1.UpdateWorkerSkillsRequest{
		AddSkills:       []string{"leapmux-review"},
		StrictConflicts: true,
	}, w)
	require.Len(t, w.errors, 1)
	assert.Equal(t, int32(codes.Aborted), w.errors[0].code)

	// Adding an unrelated skill with StrictConflicts must succeed and keep the
	// modified skill untouched.
	updateWorkerSkillsOK(t, d, &leapmuxv1.UpdateWorkerSkillsRequest{
		AddSkills:       []string{"leapmux"},
		StrictConflicts: true,
	})
	gotReview, err := os.ReadFile(reviewPath)
	require.NoError(t, err)
	assert.Equal(t, "# user modified\n", string(gotReview))
	assert.FileExists(t, filepath.Join(svc.HomeDir, ".agents", "skills", "leapmux", "SKILL.md"))

	// StrictConflicts on RemoveSkills also returns Aborted for a modified skill.
	w = newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerSystemServiceUpdateWorkerSkillsProcedure, &leapmuxv1.UpdateWorkerSkillsRequest{
		RemoveSkills:    []string{"leapmux-review"},
		StrictConflicts: true,
	}, w)
	require.Len(t, w.errors, 1)
	assert.Equal(t, int32(codes.Aborted), w.errors[0].code)
	assert.True(t, findSkillStatus(t, getWorkerSkills(t, d), "leapmux-review").GetEnabled())

	// StrictConflicts on an unmanaged skill directory returns Aborted without
	// overwriting user content.
	updateWorkerSkillsOK(t, d, &leapmuxv1.UpdateWorkerSkillsRequest{
		RemoveSkills: []string{"leapmux"},
	})
	unmanagedDir := filepath.Join(svc.HomeDir, ".agents", "skills", "leapmux")
	require.NoError(t, os.MkdirAll(unmanagedDir, 0o755))
	unmanagedPath := filepath.Join(unmanagedDir, "SKILL.md")
	require.NoError(t, os.WriteFile(unmanagedPath, []byte("# unmanaged\n"), 0o644))

	w = newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerSystemServiceUpdateWorkerSkillsProcedure, &leapmuxv1.UpdateWorkerSkillsRequest{
		AddSkills:       []string{"leapmux"},
		StrictConflicts: true,
	}, w)
	require.Len(t, w.errors, 1)
	assert.Equal(t, int32(codes.Aborted), w.errors[0].code)
	gotUnmanaged, err := os.ReadFile(unmanagedPath)
	require.NoError(t, err)
	assert.Equal(t, "# unmanaged\n", string(gotUnmanaged))

	t.Run("unsafe symlink returns Aborted even when Force is true", func(t *testing.T) {
		requireSymlinkSupport(t)
		require.NoError(t, os.RemoveAll(unmanagedDir))
		outsideDir := t.TempDir()
		require.NoError(t, os.Symlink(outsideDir, unmanagedDir))

		w := newTestWriter()
		dispatch(d, leapmuxv1connect.WorkerSystemServiceUpdateWorkerSkillsProcedure, &leapmuxv1.UpdateWorkerSkillsRequest{
			AddSkills: []string{"leapmux"},
			Force:     true,
		}, w)
		require.Len(t, w.errors, 1)
		assert.Equal(t, int32(codes.Aborted), w.errors[0].code)
	})
}

func TestWorkerSkills_EmptyHomeDirAndNilRequest(t *testing.T) {
	svc, d, _ := setupTestService(t)
	svc.HomeDir = ""
	svc.DataDir = t.TempDir()

	// GetWorkerSkills without HomeDir falls back to not_installed and empty PrimarySkillsDir.
	resp := getWorkerSkills(t, d)
	assert.Empty(t, resp.GetPrimarySkillsDir())
	assert.Equal(t, []string{string(skills.TargetAgents)}, resp.GetTargets())
	require.Len(t, resp.GetSkills(), len(skills.Catalog()))
	for _, s := range resp.GetSkills() {
		assert.Equal(t, string(skills.StateNotInstalled), s.GetState())
		require.Len(t, s.GetTargets(), 1)
		assert.Equal(t, string(skills.StateNotInstalled), s.GetTargets()[0].GetState())
	}

	// updateWorkerSkills tolerates a nil request once HomeDir and DataDir are set.
	svc.HomeDir = t.TempDir()
	got, code, err := svc.updateWorkerSkills(nil)
	require.NoError(t, err)
	assert.Equal(t, codes.OK, code)
	assert.Empty(t, got.GetOutcomes())
	assert.False(t, got.GetState().GetAutoSync())
}
