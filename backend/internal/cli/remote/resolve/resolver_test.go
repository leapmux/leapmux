package resolve_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/remote/resolve"
)

// stubDeps lets tests record which dependency was hit and return a
// scripted response. A nil function on a Deps field means "not
// configured for this test" — Resolve will skip that derivation
// even if a corresponding input is supplied, which matches the
// production wiring (each cmd handler passes only the deps it
// actually needs).
type stubDeps struct {
	locateTab     func(ctx context.Context, tabType leapmuxv1.TabType, tabID string) (leapmuxv1.TabType, string, string, string, error)
	getWorkspace  func(ctx context.Context, workspaceID string) (string, string, error)
	getWorker     func(ctx context.Context, workerID string) (string, string, error)
	locateTile    func(ctx context.Context, tileID string) (string, string, error)
	getUser       func(ctx context.Context, userID string) (string, error)
	getWorkingDir func(ctx context.Context, workerID string, tabType leapmuxv1.TabType, tabID string) (string, error)
}

func (s stubDeps) toDeps() resolve.Deps {
	return resolve.Deps{
		LocateTab:     s.locateTab,
		GetWorkspace:  s.getWorkspace,
		GetWorker:     s.getWorker,
		LocateTile:    s.locateTile,
		GetUser:       s.getUser,
		GetWorkingDir: s.getWorkingDir,
	}
}

// TestResolve_TabID_DerivesWorkspaceTileWorker pins the canonical
// worker-spawned path: the CLI inherits its tab-id from the env var
// (LEAPMUX_REMOTE_TAB_ID), the resolver issues one LocateTab call,
// and workspace_id / tile_id / worker_id come back populated. This
// is the single most-exercised derivation in production — every
// worker-spawned `leapmux remote` invocation that needs workspace
// context relies on it.
func TestResolve_TabID_DerivesWorkspaceTileWorker(t *testing.T) {
	deps := stubDeps{
		locateTab: func(_ context.Context, tabType leapmuxv1.TabType, tabID string) (leapmuxv1.TabType, string, string, string, error) {
			assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_AGENT, tabType)
			assert.Equal(t, "tab-1", tabID)
			return leapmuxv1.TabType_TAB_TYPE_AGENT, "ws-1", "tile-1", "worker-A", nil
		},
	}.toDeps()

	got, err := resolve.Resolve(context.Background(), deps,
		resolve.Need{WorkspaceID: true, TileID: true, WorkerID: true},
		resolve.Inputs{TabID: "tab-1", FixedTabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
	)
	require.NoError(t, err)
	assert.Equal(t, "ws-1", got.WorkspaceID)
	assert.Equal(t, "tile-1", got.TileID)
	assert.Equal(t, "worker-A", got.WorkerID)
	assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_AGENT, got.TabType)
}

// TestResolve_WorkspaceID_DerivesOrgAndOwner pins the workspace
// fan-out. Scripts that already know the workspace id (laptop CLI
// after `workspace list`) shouldn't have to pass --org-id;
// GetWorkspace fills it.
func TestResolve_WorkspaceID_DerivesOrgAndOwner(t *testing.T) {
	deps := stubDeps{
		getWorkspace: func(_ context.Context, workspaceID string) (string, string, error) {
			assert.Equal(t, "ws-1", workspaceID)
			return "org-1", "user-alice", nil
		},
	}.toDeps()

	got, err := resolve.Resolve(context.Background(), deps,
		resolve.Need{OrgID: true, UserID: true},
		resolve.Inputs{WorkspaceID: "ws-1"},
	)
	require.NoError(t, err)
	assert.Equal(t, "org-1", got.OrgID)
	assert.Equal(t, "user-alice", got.UserID)
}

// TestResolve_WorkerID_DerivesUserAndOrg pins the worker-side
// fan-out enabled by the new Worker.org_id field. Pre-Phase-2,
// --worker-id couldn't derive org_id; this test guards against a
// regression that would force the user back to also passing
// --org-id when they only know the worker.
func TestResolve_WorkerID_DerivesUserAndOrg(t *testing.T) {
	deps := stubDeps{
		getWorker: func(_ context.Context, workerID string) (string, string, error) {
			assert.Equal(t, "worker-A", workerID)
			return "user-alice", "org-1", nil
		},
	}.toDeps()

	got, err := resolve.Resolve(context.Background(), deps,
		resolve.Need{UserID: true, OrgID: true},
		resolve.Inputs{WorkerID: "worker-A"},
	)
	require.NoError(t, err)
	assert.Equal(t, "user-alice", got.UserID)
	assert.Equal(t, "org-1", got.OrgID)
}

// TestResolve_TileID_DerivesWorkspaceAndOrg pins the global tile
// lookup enabled by LocateTile. A script that knows only a tile id
// (e.g., from a layout_changed event) can ask the resolver for the
// owning workspace + org without standing up a CRDT bootstrap of
// its own.
func TestResolve_TileID_DerivesWorkspaceAndOrg(t *testing.T) {
	deps := stubDeps{
		locateTile: func(_ context.Context, tileID string) (string, string, error) {
			assert.Equal(t, "tile-1", tileID)
			return "ws-1", "org-1", nil
		},
	}.toDeps()

	got, err := resolve.Resolve(context.Background(), deps,
		resolve.Need{WorkspaceID: true, OrgID: true},
		resolve.Inputs{TileID: "tile-1"},
	)
	require.NoError(t, err)
	assert.Equal(t, "ws-1", got.WorkspaceID)
	assert.Equal(t, "org-1", got.OrgID)
}

// TestResolve_UserID_DerivesOrg pins the user→org edge enabled by
// the new GetUser RPC. With --user-id alone, --org-id is filled.
func TestResolve_UserID_DerivesOrg(t *testing.T) {
	deps := stubDeps{
		getUser: func(_ context.Context, userID string) (string, error) {
			assert.Equal(t, "user-alice", userID)
			return "org-1", nil
		},
	}.toDeps()

	got, err := resolve.Resolve(context.Background(), deps,
		resolve.Need{OrgID: true},
		resolve.Inputs{UserID: "user-alice"},
	)
	require.NoError(t, err)
	assert.Equal(t, "org-1", got.OrgID)
}

// TestResolve_ConflictBetweenTabAndWorkspace catches the canonical
// user-input conflict: passing --tab-id from workspace A together
// with --workspace-id=B must error out citing both flags. Without
// this check, a typo'd --workspace-id would silently route the
// command at the wrong workspace.
func TestResolve_ConflictBetweenTabAndWorkspace(t *testing.T) {
	deps := stubDeps{
		locateTab: func(_ context.Context, _ leapmuxv1.TabType, _ string) (leapmuxv1.TabType, string, string, string, error) {
			return leapmuxv1.TabType_TAB_TYPE_AGENT, "ws-A", "tile-1", "worker-A", nil
		},
		getWorkspace: func(_ context.Context, _ string) (string, string, error) {
			return "org-1", "user-alice", nil
		},
	}.toDeps()

	_, err := resolve.Resolve(context.Background(), deps,
		resolve.Need{WorkspaceID: true},
		resolve.Inputs{
			TabID:        "tab-1",
			WorkspaceID:  "ws-B",
			FixedTabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
		},
	)
	require.Error(t, err)
	var re *resolve.ResolveError
	require.ErrorAs(t, err, &re)
	assert.Equal(t, "invalid_request", re.Code)
	assert.Contains(t, re.Message, "workspace_id")
	assert.Contains(t, re.Message, "ws-A")
	assert.Contains(t, re.Message, "ws-B")
	assert.Contains(t, re.Message, "--tab-id")
	assert.Contains(t, re.Message, "--workspace-id")
}

// TestResolve_ConflictBetweenWorkerAndUser catches a different
// shape of contradiction: --worker-id resolves to user X, but the
// caller also passed --user-id=Y. Surfacing this catches the
// "borrowed someone else's worker id" failure mode where a script
// picks the wrong worker for the wrong user.
func TestResolve_ConflictBetweenWorkerAndUser(t *testing.T) {
	deps := stubDeps{
		getWorker: func(_ context.Context, _ string) (string, string, error) {
			return "user-alice", "org-1", nil
		},
		getUser: func(_ context.Context, _ string) (string, error) {
			return "org-1", nil
		},
	}.toDeps()

	_, err := resolve.Resolve(context.Background(), deps,
		resolve.Need{UserID: true},
		resolve.Inputs{WorkerID: "worker-A", UserID: "user-bob"},
	)
	require.Error(t, err)
	var re *resolve.ResolveError
	require.ErrorAs(t, err, &re)
	assert.Equal(t, "invalid_request", re.Code)
	assert.Contains(t, re.Message, "user_id")
	assert.Contains(t, re.Message, "user-alice")
	assert.Contains(t, re.Message, "user-bob")
}

// TestResolve_AgreementAcrossSourcesIsOK is the inverse of the
// conflict tests: every multi-source field that *agrees* must
// pass through cleanly. Without this coverage a conflict-detection
// regression that always returned an error would still pass the
// "single-source" tests above.
func TestResolve_AgreementAcrossSourcesIsOK(t *testing.T) {
	deps := stubDeps{
		locateTab: func(_ context.Context, _ leapmuxv1.TabType, _ string) (leapmuxv1.TabType, string, string, string, error) {
			return leapmuxv1.TabType_TAB_TYPE_AGENT, "ws-1", "tile-1", "worker-A", nil
		},
		getWorkspace: func(_ context.Context, _ string) (string, string, error) {
			return "org-1", "user-alice", nil
		},
	}.toDeps()

	got, err := resolve.Resolve(context.Background(), deps,
		resolve.Need{WorkspaceID: true, OrgID: true},
		resolve.Inputs{
			TabID:        "tab-1",
			WorkspaceID:  "ws-1", // agrees with the LocateTab derivation
			FixedTabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
		},
	)
	require.NoError(t, err)
	assert.Equal(t, "ws-1", got.WorkspaceID)
	assert.Equal(t, "org-1", got.OrgID)
}

// TestResolve_MissingRequiredSurfacesAllFields confirms a single
// invocation lists every unmet Need in one error envelope — scripts
// shouldn't have to fix-and-retry one flag at a time.
func TestResolve_MissingRequiredSurfacesAllFields(t *testing.T) {
	_, err := resolve.Resolve(context.Background(), resolve.Deps{},
		resolve.Need{WorkspaceID: true, WorkerID: true, OrgID: true},
		resolve.Inputs{},
	)
	require.Error(t, err)
	var re *resolve.ResolveError
	require.ErrorAs(t, err, &re)
	assert.Equal(t, "invalid_request", re.Code)
	assert.Contains(t, re.Message, "--workspace-id")
	assert.Contains(t, re.Message, "--worker-id")
	assert.Contains(t, re.Message, "--org-id")
}

// TestResolve_TabIDWithoutTypeIsAllowed pins the wildcard contract:
// passing --tab-id without a type is OK because the resolver
// forwards TAB_TYPE_UNSPECIFIED to LocateTab, and the server treats
// 0 as a wildcard. The dep intentionally returns AGENT so we also
// verify the resolver backfills Resolved.TabType from the response —
// otherwise a downstream agent-only RPC dispatch wouldn't know
// which proto to send.
func TestResolve_TabIDWithoutTypeIsAllowed(t *testing.T) {
	deps := stubDeps{
		locateTab: func(_ context.Context, tabType leapmuxv1.TabType, _ string) (leapmuxv1.TabType, string, string, string, error) {
			assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED, tabType,
				"resolver must forward UNSPECIFIED when the caller didn't pin a type")
			return leapmuxv1.TabType_TAB_TYPE_AGENT, "ws-1", "tile-1", "worker-A", nil
		},
	}.toDeps()
	got, err := resolve.Resolve(context.Background(), deps,
		resolve.Need{WorkspaceID: true, WorkerID: true},
		resolve.Inputs{TabID: "tab-1"},
	)
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_AGENT, got.TabType,
		"resolver must backfill TabType from the LocateTab response when the input was unspecified")
	assert.Equal(t, "ws-1", got.WorkspaceID)
	assert.Equal(t, "worker-A", got.WorkerID)
}

// TestResolve_FixedTabTypeRejectsContradictingFlag locks in the
// agent / terminal subgroup contract: those commands pin
// FixedTabType, and a user passing --tab-type for a different kind
// must error out (not silently override the pin).
func TestResolve_FixedTabTypeRejectsContradictingFlag(t *testing.T) {
	_, err := resolve.Resolve(context.Background(), resolve.Deps{},
		resolve.Need{},
		resolve.Inputs{
			TabID:        "tab-1",
			TabType:      "terminal",
			FixedTabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
		},
	)
	require.Error(t, err)
	var re *resolve.ResolveError
	require.ErrorAs(t, err, &re)
	assert.Equal(t, "invalid_request", re.Code)
	assert.Contains(t, re.Message, "contradicts")
}

// TestResolve_FixedTabTypeAgreeingFlagIsOK is the inverse: passing
// --tab-type explicitly to an agent-subgroup command must be
// accepted as long as it matches. Catches a regression that
// reject-the-agreement would produce.
func TestResolve_FixedTabTypeAgreeingFlagIsOK(t *testing.T) {
	deps := stubDeps{
		locateTab: func(_ context.Context, _ leapmuxv1.TabType, _ string) (leapmuxv1.TabType, string, string, string, error) {
			return leapmuxv1.TabType_TAB_TYPE_AGENT, "ws-1", "tile-1", "worker-A", nil
		},
	}.toDeps()
	got, err := resolve.Resolve(context.Background(), deps,
		resolve.Need{WorkerID: true},
		resolve.Inputs{TabID: "tab-1", TabType: "agent", FixedTabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
	)
	require.NoError(t, err)
	assert.Equal(t, "worker-A", got.WorkerID)
}

// TestResolve_UnknownTabTypeIsInvalidArgument guards against the
// "user typed a typo'd tab type" case. The resolver must not
// silently map "agnt" to AGENT or TabTypeUnspecified — it has to
// reject so the user sees the fix.
func TestResolve_UnknownTabTypeIsInvalidArgument(t *testing.T) {
	_, err := resolve.Resolve(context.Background(), resolve.Deps{},
		resolve.Need{},
		resolve.Inputs{TabID: "tab-1", TabType: "agnt"},
	)
	require.Error(t, err)
	var re *resolve.ResolveError
	require.ErrorAs(t, err, &re)
	assert.Equal(t, "invalid_request", re.Code)
}

// TestResolve_WorkingDirIsBestEffort proves that a failed working-
// dir lookup doesn't bring down the whole resolve. Orphan tabs
// (worker gone) must still resolve workspace/tile/worker — the
// caller decides what to do with the empty working_dir.
func TestResolve_WorkingDirIsBestEffort(t *testing.T) {
	deps := stubDeps{
		locateTab: func(_ context.Context, _ leapmuxv1.TabType, _ string) (leapmuxv1.TabType, string, string, string, error) {
			return leapmuxv1.TabType_TAB_TYPE_AGENT, "ws-1", "tile-1", "worker-A", nil
		},
		getWorkingDir: func(_ context.Context, _ string, _ leapmuxv1.TabType, _ string) (string, error) {
			return "", errors.New("worker unreachable")
		},
	}.toDeps()

	got, err := resolve.Resolve(context.Background(), deps,
		resolve.Need{WorkerID: true, WorkingDir: true},
		resolve.Inputs{TabID: "tab-1", FixedTabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
	)
	require.NoError(t, err)
	assert.Equal(t, "worker-A", got.WorkerID)
	assert.Empty(t, got.WorkingDir, "failed inner-RPC must leave WorkingDir empty, not error out")
}

// TestResolve_WorkingDirFetchedOnlyWhenNeeded confirms the
// best-effort knob doesn't fire unless Need.WorkingDir is true. Run
// it via a panic-on-call stub so a regression that always invoked
// GetWorkingDir would crash here.
func TestResolve_WorkingDirFetchedOnlyWhenNeeded(t *testing.T) {
	calls := 0
	deps := stubDeps{
		locateTab: func(_ context.Context, _ leapmuxv1.TabType, _ string) (leapmuxv1.TabType, string, string, string, error) {
			return leapmuxv1.TabType_TAB_TYPE_AGENT, "ws-1", "tile-1", "worker-A", nil
		},
		getWorkingDir: func(_ context.Context, _ string, _ leapmuxv1.TabType, _ string) (string, error) {
			calls++
			return "/home/agent", nil
		},
	}.toDeps()

	_, err := resolve.Resolve(context.Background(), deps,
		resolve.Need{WorkerID: true}, // working dir NOT requested
		resolve.Inputs{TabID: "tab-1", FixedTabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
	)
	require.NoError(t, err)
	assert.Zero(t, calls, "GetWorkingDir must only fire when Need.WorkingDir is true")
}

// TestResolve_RpcErrorPropagates ensures a transport / coding
// failure on a derivation surfaces verbatim (wrapped with the
// caller-friendly prefix). Conflicts and missing-required are
// resolver-specific, but RPC failures pass through so the caller's
// existing error envelope wrapper can attach a code.
func TestResolve_RpcErrorPropagates(t *testing.T) {
	deps := stubDeps{
		locateTab: func(_ context.Context, _ leapmuxv1.TabType, _ string) (leapmuxv1.TabType, string, string, string, error) {
			return leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED, "", "", "", errors.New("not found")
		},
	}.toDeps()
	_, err := resolve.Resolve(context.Background(), deps,
		resolve.Need{WorkspaceID: true},
		resolve.Inputs{TabID: "tab-1", FixedTabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
	)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "locate tab tab-1"),
		"transport error must be wrapped with the input that triggered it")
}

// TestResolve_NoDerivations_ReturnsSeededInputs covers the trivial
// case where every required field came in via flags (no hub round-
// trips). The resolver still validates Need and surfaces any
// missing inputs, but happy-path data passes through.
func TestResolve_NoDerivations_ReturnsSeededInputs(t *testing.T) {
	got, err := resolve.Resolve(context.Background(), resolve.Deps{},
		resolve.Need{WorkspaceID: true, OrgID: true},
		resolve.Inputs{WorkspaceID: "ws-1", OrgID: "org-1"},
	)
	require.NoError(t, err)
	assert.Equal(t, "ws-1", got.WorkspaceID)
	assert.Equal(t, "org-1", got.OrgID)
}

// TestResolve_TrimsWhitespaceOnInputs catches a class of input bugs
// where a quoted env var (e.g. "$LEAPMUX_REMOTE_TAB_ID\n" with a
// trailing newline from a misquoted shell read) would otherwise
// look like a contradicting value vs. the trimmed flag form.
func TestResolve_TrimsWhitespaceOnInputs(t *testing.T) {
	deps := stubDeps{
		locateTab: func(_ context.Context, _ leapmuxv1.TabType, _ string) (leapmuxv1.TabType, string, string, string, error) {
			return leapmuxv1.TabType_TAB_TYPE_AGENT, "ws-1", "tile-1", "worker-A", nil
		},
	}.toDeps()
	got, err := resolve.Resolve(context.Background(), deps,
		resolve.Need{WorkerID: true},
		resolve.Inputs{TabID: "  tab-1\n", FixedTabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
	)
	require.NoError(t, err)
	assert.Equal(t, "worker-A", got.WorkerID)
}

// TestResolve_ExplicitFlagBeatsEnvDerivation pins the
// explicit-vs-env priority contract that makes
// `leapmux remote tile close --tile-id X` work even when
// $LEAPMUX_REMOTE_TAB_ID points at a tab on a DIFFERENT tile.
// Without this, the env-defaulted --tab-id's LocateTab derivation
// (tile_id=Y) collides with explicit --tile-id=X and the resolver
// errors with "conflicting inputs". With it, the explicit flag wins
// silently.
func TestResolve_ExplicitFlagBeatsEnvDerivation(t *testing.T) {
	deps := stubDeps{
		locateTab: func(_ context.Context, _ leapmuxv1.TabType, _ string) (leapmuxv1.TabType, string, string, string, error) {
			return leapmuxv1.TabType_TAB_TYPE_TERMINAL, "ws-1", "tile-Y", "worker-A", nil
		},
		locateTile: func(_ context.Context, tileID string) (string, string, error) {
			assert.Equal(t, "tile-X", tileID, "LocateTile must be called with the explicit --tile-id")
			return "ws-1", "org-1", nil
		},
	}.toDeps()

	got, err := resolve.Resolve(context.Background(), deps,
		resolve.Need{TileID: true, WorkspaceID: true},
		resolve.Inputs{
			TabID:          "tab-env",
			TileID:         "tile-X",
			ExplicitTileID: true, // user typed --tile-id; --tab-id is env-defaulted
		},
	)
	require.NoError(t, err)
	assert.Equal(t, "tile-X", got.TileID, "explicit --tile-id wins over env tab's tile derivation")
	assert.Equal(t, "ws-1", got.WorkspaceID, "agreeing workspace_id stays")
}

// TestResolve_TwoExplicitInputsStillConflict guards the asymmetry:
// when *both* inputs are typed explicitly, a disagreement is a
// genuine user error and must still surface. The priority rule
// only suppresses env-vs-explicit conflicts.
func TestResolve_TwoExplicitInputsStillConflict(t *testing.T) {
	deps := stubDeps{
		locateTab: func(_ context.Context, _ leapmuxv1.TabType, _ string) (leapmuxv1.TabType, string, string, string, error) {
			return leapmuxv1.TabType_TAB_TYPE_TERMINAL, "ws-1", "tile-Y", "worker-A", nil
		},
	}.toDeps()
	_, err := resolve.Resolve(context.Background(), deps,
		resolve.Need{TileID: true},
		resolve.Inputs{
			TabID:          "tab-1",
			TileID:         "tile-X",
			ExplicitTabID:  true,
			ExplicitTileID: true,
		},
	)
	require.Error(t, err)
	var re *resolve.ResolveError
	require.ErrorAs(t, err, &re)
	assert.Equal(t, "invalid_request", re.Code)
	assert.Contains(t, re.Message, "tile_id")
	assert.Contains(t, re.Message, "tile-X")
	assert.Contains(t, re.Message, "tile-Y")
}

// TestResolve_ExplicitInputShadowsEnvOnlyForConflictingField pins
// the fine-grained version of the priority rule: when explicit
// --tile-id and env --tab-id disagree on tile_id, the env tab's
// OTHER derivations (worker_id, agreeing workspace_id) still flow
// through. We don't throw out the env tab entirely just because one
// of its derivations clashed.
func TestResolve_ExplicitInputShadowsEnvOnlyForConflictingField(t *testing.T) {
	deps := stubDeps{
		locateTab: func(_ context.Context, _ leapmuxv1.TabType, _ string) (leapmuxv1.TabType, string, string, string, error) {
			return leapmuxv1.TabType_TAB_TYPE_TERMINAL, "ws-1", "tile-Y", "worker-from-env-tab", nil
		},
		locateTile: func(_ context.Context, _ string) (string, string, error) {
			return "ws-1", "org-1", nil
		},
	}.toDeps()
	got, err := resolve.Resolve(context.Background(), deps,
		resolve.Need{TileID: true, WorkerID: true},
		resolve.Inputs{
			TabID:          "tab-env",
			TileID:         "tile-X",
			ExplicitTileID: true,
		},
	)
	require.NoError(t, err)
	assert.Equal(t, "tile-X", got.TileID)
	assert.Equal(t, "ws-1", got.WorkspaceID)
	assert.Equal(t, "worker-from-env-tab", got.WorkerID,
		"non-conflicting derivations from the env tab survive even when its tile_id was shadowed")
}
