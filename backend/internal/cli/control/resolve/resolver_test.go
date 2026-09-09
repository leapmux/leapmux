package resolve_test

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
)

// stubDeps lets tests record which dependency was hit and return a
// scripted response. A nil function on a Deps field means "not
// configured for this test" — Resolve will skip that derivation
// even if a corresponding input is supplied, which matches the
// production wiring (each cmd handler passes only the deps it
// actually needs).
type stubDeps struct {
	locateTab    func(ctx context.Context, tabType leapmuxv1.TabType, tabID string) (leapmuxv1.TabType, string, string, string, error)
	getWorkspace func(ctx context.Context, workspaceID string) error
	locateTile   func(ctx context.Context, tileID string) (string, error)
}

func (s stubDeps) toDeps() resolve.Deps {
	return resolve.Deps{
		LocateTab:    s.locateTab,
		GetWorkspace: s.getWorkspace,
		LocateTile:   s.locateTile,
	}
}

// TestResolve_TabID_DerivesWorkspaceTileWorker pins the canonical
// worker-spawned path: the CLI inherits its tab-id from the env var
// (LEAPMUX_CONTROL_TAB_ID), the resolver issues one LocateTab call,
// and workspace_id / tile_id / worker_id come back populated. This
// is the single most-exercised derivation in production — every
// worker-spawned `leapmux control` invocation that needs workspace
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

// TestResolve_WorkspaceID_ChecksExistence pins the workspace leg:
// --workspace-id derives nothing, but it must still be validated so a
// typo fails here with a clear error instead of landing downstream as
// an empty CRDT projection.
func TestResolve_WorkspaceID_ChecksExistence(t *testing.T) {
	called := false
	deps := stubDeps{
		getWorkspace: func(_ context.Context, workspaceID string) error {
			called = true
			assert.Equal(t, "ws-1", workspaceID)
			return nil
		},
	}.toDeps()

	got, err := resolve.Resolve(context.Background(), deps,
		resolve.Need{WorkspaceID: true},
		resolve.Inputs{WorkspaceID: "ws-1"},
	)
	require.NoError(t, err)
	assert.True(t, called, "a supplied --workspace-id must be checked against the hub")
	assert.Equal(t, "ws-1", got.WorkspaceID)
}

// TestResolve_WorkspaceID_NotFound is the miss half of the same leg:
// an unknown workspace fails the whole resolve rather than passing the
// unchecked flag value through.
func TestResolve_WorkspaceID_NotFound(t *testing.T) {
	deps := stubDeps{
		getWorkspace: func(_ context.Context, _ string) error {
			return errors.New("not found")
		},
	}.toDeps()

	_, err := resolve.Resolve(context.Background(), deps,
		resolve.Need{WorkspaceID: true},
		resolve.Inputs{WorkspaceID: "ws-missing"},
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get workspace ws-missing")
	assert.Contains(t, err.Error(), "not found")
}

// TestResolve_WorkerID_CostsNoRPC is the deliberate anti-twin of
// TestResolve_WorkspaceID_ChecksExistence: --worker-id passes straight
// through, unconfirmed and free.
//
// The workspace axis keeps its existence check because nothing
// downstream catches a bogus workspace id (GetMaterialized answers with
// an empty projection, not an error). The worker axis has no such gap
// -- and a confirmation RPC there is actively harmful, because every
// worker-spawned agent inherits $LEAPMUX_CONTROL_WORKER_ID and the only
// RPCs that could serve the check (GetWorker / ListWorkers) are
// intentionally off the hub's delegation-bearer allowlist. The leg
// that used to be here failed every agent-issued command with
// `resolve_failed: get worker ...: permission_denied`.
func TestResolve_WorkerID_CostsNoRPC(t *testing.T) {
	// Every dep panics: reaching any of them on a bare --worker-id
	// input is the regression.
	deps := stubDeps{
		locateTab: func(context.Context, leapmuxv1.TabType, string) (leapmuxv1.TabType, string, string, string, error) {
			panic("--worker-id must not trigger LocateTab")
		},
		getWorkspace: func(context.Context, string) error {
			panic("--worker-id must not trigger GetWorkspace")
		},
		locateTile: func(context.Context, string) (string, error) {
			panic("--worker-id must not trigger LocateTile")
		},
	}.toDeps()

	got, err := resolve.Resolve(context.Background(), deps,
		resolve.Need{WorkerID: true},
		resolve.Inputs{WorkerID: "worker-A"},
	)
	require.NoError(t, err)
	assert.Equal(t, "worker-A", got.WorkerID)
}

// TestResolve_TileID_DerivesWorkspace pins the global tile lookup
// enabled by LocateTile. A script that knows only a tile id (e.g.,
// from a layout_changed event) can ask the resolver for the owning
// workspace without standing up a CRDT bootstrap of its own.
func TestResolve_TileID_DerivesWorkspace(t *testing.T) {
	deps := stubDeps{
		locateTile: func(_ context.Context, tileID string) (string, error) {
			assert.Equal(t, "tile-1", tileID)
			return "ws-1", nil
		},
	}.toDeps()

	got, err := resolve.Resolve(context.Background(), deps,
		resolve.Need{WorkspaceID: true},
		resolve.Inputs{TileID: "tile-1"},
	)
	require.NoError(t, err)
	assert.Equal(t, "ws-1", got.WorkspaceID)
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
		getWorkspace: func(_ context.Context, _ string) error {
			return nil
		},
	}.toDeps()

	_, err := resolve.Resolve(context.Background(), deps,
		resolve.Need{WorkspaceID: true},
		resolve.Inputs{
			// BOTH explicit, as BindEntityFlags marks a typed flag. Two typed
			// ids that disagree is the conflict a user can actually make, and
			// the derivation runs for an explicit --tab-id whatever else is
			// supplied -- see wantsTabPlacement.
			TabID:               "tab-1",
			ExplicitTabID:       true,
			WorkspaceID:         "ws-B",
			ExplicitWorkspaceID: true,
			FixedTabType:        leapmuxv1.TabType_TAB_TYPE_AGENT,
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
		getWorkspace: func(_ context.Context, _ string) error {
			return nil
		},
	}.toDeps()

	got, err := resolve.Resolve(context.Background(), deps,
		resolve.Need{WorkspaceID: true},
		resolve.Inputs{
			TabID:        "tab-1",
			WorkspaceID:  "ws-1", // agrees with the LocateTab derivation
			FixedTabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
		},
	)
	require.NoError(t, err)
	assert.Equal(t, "ws-1", got.WorkspaceID)
}

// TestResolve_MissingRequiredSurfacesAllFields confirms a single
// invocation lists every unmet Need in one error envelope — scripts
// shouldn't have to fix-and-retry one flag at a time.
func TestResolve_MissingRequiredSurfacesAllFields(t *testing.T) {
	_, err := resolve.Resolve(context.Background(), resolve.Deps{},
		resolve.Need{WorkspaceID: true, TileID: true, WorkerID: true},
		resolve.Inputs{},
	)
	require.Error(t, err)
	var re *resolve.ResolveError
	require.ErrorAs(t, err, &re)
	assert.Equal(t, "invalid_request", re.Code)
	assert.Contains(t, re.Message, "--workspace-id")
	assert.Contains(t, re.Message, "--tile-id")
	assert.Contains(t, re.Message, "--worker-id")
	assert.NotContains(t, re.Message, "--user-id",
		"a user id is never required: the hub derives the tenant from the session")
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

// The contradiction message prints BOTH sides through TabTypeWireName, so a
// kind that function does not name reads as `--tab-type "" contradicts this
// command's implicit type ""`. FILE has been in the enum all along and IMAGE
// joined it, and neither was stated -- so this pins the round trip for every
// kind rather than the two the message happened to cover.
func TestResolve_ContradictionNamesEveryTabKind(t *testing.T) {
	kinds := []struct {
		enum leapmuxv1.TabType
		wire string
	}{
		{leapmuxv1.TabType_TAB_TYPE_AGENT, "agent"},
		{leapmuxv1.TabType_TAB_TYPE_TERMINAL, "terminal"},
		{leapmuxv1.TabType_TAB_TYPE_FILE, "file"},
		{leapmuxv1.TabType_TAB_TYPE_IMAGE, "image"},
	}
	for _, k := range kinds {
		assert.Equal(t, k.wire, resolve.TabTypeWireName(k.enum))
		parsed, ok := resolve.ParseTabType(k.wire)
		require.True(t, ok, "%s must parse back", k.wire)
		assert.Equal(t, k.enum, parsed, "%s must round trip", k.wire)
	}
	assert.Empty(t, resolve.TabTypeWireName(leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED),
		"the zero value stays empty, which is how a caller guards the env-var emit")

	// The message a user actually reads, with neither side an empty string.
	_, err := resolve.Resolve(context.Background(), resolve.Deps{},
		resolve.Need{},
		resolve.Inputs{
			TabID:        "tab-1",
			TabType:      "image",
			FixedTabType: leapmuxv1.TabType_TAB_TYPE_FILE,
		},
	)
	require.Error(t, err)
	var re *resolve.ResolveError
	require.ErrorAs(t, err, &re)
	assert.Contains(t, re.Message, `"image"`)
	assert.Contains(t, re.Message, `"file"`)
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
		resolve.Need{WorkspaceID: true},
		resolve.Inputs{WorkspaceID: "ws-1"},
	)
	require.NoError(t, err)
	assert.Equal(t, "ws-1", got.WorkspaceID)
}

// TestResolve_TrimsWhitespaceOnInputs catches a class of input bugs
// where a quoted env var (e.g. "$LEAPMUX_CONTROL_TAB_ID\n" with a
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
// `leapmux control tile close --tile-id X` work even when
// $LEAPMUX_CONTROL_TAB_ID points at a tab on a DIFFERENT tile.
// Without this, the env-defaulted --tab-id's LocateTab derivation
// (tile_id=Y) collides with explicit --tile-id=X and the resolver
// errors with "conflicting inputs". With it, the explicit flag wins
// silently.
func TestResolve_ExplicitFlagBeatsEnvDerivation(t *testing.T) {
	deps := stubDeps{
		locateTab: func(_ context.Context, _ leapmuxv1.TabType, _ string) (leapmuxv1.TabType, string, string, string, error) {
			return leapmuxv1.TabType_TAB_TYPE_TERMINAL, "ws-1", "tile-Y", "worker-A", nil
		},
		locateTile: func(_ context.Context, tileID string) (string, error) {
			assert.Equal(t, "tile-X", tileID, "LocateTile must be called with the explicit --tile-id")
			return "ws-1", nil
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
		locateTile: func(_ context.Context, _ string) (string, error) {
			return "ws-1", nil
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

// TestResolve_NoNeedsWithNoInputs pins the invocation shape the three
// session-scoped commands rely on: `workspace create --title X`,
// `events`, and `tab list` all run with no entity flags at all, from a
// laptop where no LEAPMUX_CONTROL_*_ID env var is set. They must resolve
// cleanly rather than being rejected for a missing id.
//
// This is a regression guard. These commands used to declare
// Need{UserID: true} even though no hub RPC takes a user id any more
// (the session implies the tenant) -- so a flag-less invocation failed
// with "missing required ID(s): --user-id", and the commands that hid
// the flag left the operator no way to satisfy the requirement at all.
func TestResolve_NoNeedsWithNoInputs(t *testing.T) {
	deps := stubDeps{}.toDeps()

	got, err := resolve.Resolve(context.Background(), deps,
		resolve.Need{},
		resolve.Inputs{},
	)
	require.NoError(t, err, "a flag-less, session-scoped command must resolve without any entity id")
	assert.Empty(t, got.WorkspaceID)
}

// TestResolve_HasNoUserIDAxis is a structural guard against
// reintroducing the user-id axis anywhere in the resolver's surface.
// The hub derives the tenant from the session: no RPC takes a user id.
// The one that did (UserService.GetUser) was self-only, echoed the
// caller's own session, and was not callable by a worker-spawned
// agent's delegation bearer -- so a --user-id input could only turn a
// legal command into `resolve_failed` (which is exactly what
// `workspace list` inside a spawn used to do, since the worker injects
// $LEAPMUX_CONTROL_USER_ID into every agent). It has since been deleted.
func TestResolve_HasNoUserIDAxis(t *testing.T) {
	for _, tc := range []struct {
		name  string
		typ   reflect.Type
		field string
	}{
		{"Need", reflect.TypeOf(resolve.Need{}), "UserID"},
		{"Inputs", reflect.TypeOf(resolve.Inputs{}), "UserID"},
		{"Inputs", reflect.TypeOf(resolve.Inputs{}), "ExplicitUserID"},
		{"Resolved", reflect.TypeOf(resolve.Resolved{}), "UserID"},
		{"Deps", reflect.TypeOf(resolve.Deps{}), "GetUser"},
		{"FlagOptions", reflect.TypeOf(resolve.FlagOptions{}), "HideUser"},
	} {
		_, found := tc.typ.FieldByName(tc.field)
		assert.False(t, found, "%s must not carry a %s field", tc.name, tc.field)
	}
}

// TestResolve_HasNoWorkerExistenceCheck is the same structural guard
// one axis over. --worker-id itself stays (it is a required output for
// most commands and LocateTab derives it); what must not come back is
// a dep that CONFIRMS it against the hub.
//
// Both procedures that could implement such a dep --
// WorkerManagementService's GetWorker and ListWorkers -- need
// worker:read, and a delegation bearer's ceiling
// (auth.CeilingFor(BearerKindDelegation)) admits that scope. So the leg
// would usually succeed, which is worse than a clean refusal: since the
// worker injects $LEAPMUX_CONTROL_WORKER_ID into every agent it spawns,
// a resolver leg on this axis fires on essentially every agent-issued
// command, and its only observable effect is a hub round trip that turns
// a transient failure into `resolve_failed`.
func TestResolve_HasNoWorkerExistenceCheck(t *testing.T) {
	depsType := reflect.TypeOf(resolve.Deps{})
	for _, name := range []string{"GetWorker", "ListWorkers"} {
		_, found := depsType.FieldByName(name)
		assert.False(t, found, "Deps must not carry a %s field", name)
	}
}

// Every value the proto enum carries must have a wire token, and every token
// must parse back to it.
//
// This is what the contract buys and what eight hand-written tables kept
// losing: FILE sat in the enum unnamed, IMAGE joined it, and the gap surfaced
// as `--tab-type "" contradicts this command's implicit type ""`. The table is
// generated from contracts/tab-types.json now, and generate-contracts fails the
// build for an enum value with no entry -- so this test walks the ENUM rather
// than a list a reader has to remember to extend.
func TestTabTypeVocabularyCoversEveryEnumValue(t *testing.T) {
	for value, name := range leapmuxv1.TabType_name {
		kind := leapmuxv1.TabType(value)
		token := resolve.TabTypeWireName(kind)

		if kind == leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED {
			assert.Empty(t, token, "the zero value stays empty: it is how an omitted --type reads")
		} else {
			assert.NotEmpty(t, token, "%s has no wire token, so no user can name it on the command line", name)
		}

		parsed, ok := resolve.ParseTabType(token)
		require.True(t, ok, "the token for %s must parse back", name)
		assert.Equal(t, kind, parsed, "%s must round trip through its token", name)

		// The proto-canonical spelling parses too, so a tab_type read out of a
		// JSON envelope goes straight back into a flag.
		canonical, ok := resolve.ParseTabType(name)
		require.True(t, ok, "%s must parse in its canonical spelling", name)
		assert.Equal(t, kind, canonical)
	}
}

// A QUAKE terminal has no CRDT tab, so LocateTab can never place its id. The
// commands that address it -- `terminal send`, `terminal get` -- need only the
// WORKER, which a worker-spawned CLI already has from its env, so the miss
// costs them nothing and the command must run.
//
// This is the regression test for the whole class: LocateTab fires eagerly for
// any supplied tab id, so a fatal miss made EVERY `leapmux control` command
// typed inside a quake panel fail before reaching its own body -- including the
// `terminal quake` commands that exist to be typed there.
func TestResolve_UnlocatableTabIsToleratedWhenNothingNeedsIt(t *testing.T) {
	deps := stubDeps{
		locateTab: func(_ context.Context, _ leapmuxv1.TabType, tabID string) (leapmuxv1.TabType, string, string, string, error) {
			return leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED, "", "", "",
				fmt.Errorf("%w: no such tab", resolve.ErrTabNotLocatable)
		},
	}.toDeps()

	got, err := resolve.Resolve(context.Background(), deps,
		resolve.Need{TabID: true, WorkerID: true},
		resolve.Inputs{TabID: "quake-1", WorkerID: "w1", FixedTabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL},
	)

	require.NoError(t, err, "a tab the hub cannot place must not fail a command that only needs the worker")
	assert.Equal(t, "quake-1", got.TabID)
	assert.Equal(t, "w1", got.WorkerID, "the worker still comes from its own input")
}

// The other half of the rule: the miss becomes fatal the moment it is what
// leaves a required field empty, and it says so rather than reporting a bare
// "missing required ID(s)".
func TestResolve_UnlocatableTabIsFatalWhenAFieldNeededIt(t *testing.T) {
	deps := stubDeps{
		locateTab: func(_ context.Context, _ leapmuxv1.TabType, tabID string) (leapmuxv1.TabType, string, string, string, error) {
			return leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED, "", "", "",
				fmt.Errorf("%w: no such tab", resolve.ErrTabNotLocatable)
		},
	}.toDeps()

	_, err := resolve.Resolve(context.Background(), deps,
		resolve.Need{WorkspaceID: true},
		resolve.Inputs{TabID: "quake-1", WorkerID: "w1"},
	)

	require.Error(t, err)
	assert.ErrorIs(t, err, resolve.ErrTabNotLocatable,
		"the cause stays reachable, so a caller need not match on the message")
	assert.Contains(t, err.Error(), "--workspace-id",
		"and it still gives the flag that would satisfy what the miss left empty")
}

// A LocateTab failure that is NOT "no such tab" stays fatal. A transport error
// or a 5xx means the Hub could not be ASKED, which says nothing about whether
// the tab exists -- tolerating those would turn an outage into a confusing
// worker-side failure much later.
func TestResolve_LocateTabTransportFailureStaysFatal(t *testing.T) {
	deps := stubDeps{
		locateTab: func(_ context.Context, _ leapmuxv1.TabType, tabID string) (leapmuxv1.TabType, string, string, string, error) {
			return leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED, "", "", "", errors.New("connection refused")
		},
	}.toDeps()

	// The workspace is required and unsupplied, so only the hub can fill it and
	// the call genuinely runs. A command that needed nothing from it skips the
	// call outright -- see TestResolve_SkipsLocateTabWhenNothingItFillsIsRead.
	_, err := resolve.Resolve(context.Background(), deps,
		resolve.Need{TabID: true, WorkspaceID: true},
		resolve.Inputs{TabID: "t1", FixedTabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL},
	)

	require.Error(t, err, "an unreachable hub must not read as an absent tab")
	assert.NotErrorIs(t, err, resolve.ErrTabNotLocatable)
	assert.Contains(t, err.Error(), "connection refused")
}

// The `terminal ...` subgroup's --tab-id default, across the three spawn
// shapes a `leapmux control` invocation can find itself in.
//
// The quake row is the one that needed the new source: there TAB_ID identifies a
// NEIGHBOURING tab, so the old type gate refused it (agent != terminal) and a
// bare `terminal send` had no target at all.
func TestBindEntityFlags_TerminalTabIDDefault(t *testing.T) {
	for _, tc := range []struct {
		name        string
		tabID       string
		tabType     string
		terminalID  string
		wantDefault string
	}{
		{
			name: "inside a quake panel, the shell is the only id there is",
			// What a quake spawn exports: NO ambient tab -- the panel belongs
			// to a directory, and no tab in it is "the tab you are in" -- and
			// the panel's own shell as the terminal.
			tabID: "", tabType: "", terminalID: "quake-1",
			wantDefault: "quake-1",
		},
		{
			name:  "inside an ordinary terminal tab, both name the same id",
			tabID: "term-1", tabType: "terminal", terminalID: "term-1",
			wantDefault: "term-1",
		},
		{
			name: "inside an agent, there is no terminal to default to",
			// The type gate still decides here, and it refuses: an agent tab
			// is not a terminal, so `terminal send` must ask for --tab-id
			// rather than target the agent.
			tabID: "agent-1", tabType: "agent", terminalID: "",
			wantDefault: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LEAPMUX_CONTROL_TAB_ID", tc.tabID)
			t.Setenv("LEAPMUX_CONTROL_TAB_TYPE", tc.tabType)
			t.Setenv("LEAPMUX_CONTROL_TERMINAL_ID", tc.terminalID)

			var in resolve.Inputs
			fs := flag.NewFlagSet("terminal send", flag.ContinueOnError)
			resolve.BindEntityFlags(fs, &in, resolve.FlagOptions{
				FixedTabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL,
			})
			require.NoError(t, fs.Parse(nil))

			assert.Equal(t, tc.wantDefault, in.TabID)
		})
	}
}

// The AGENT subgroup never reads TERMINAL_ID: an agent command must not default
// to a terminal id, whatever the surrounding spawn is.
//
// The two rows are the two spawns that set it. Inside an ordinary terminal TAB
// the ambient tab is that terminal, so `agent send` still has no default and
// asks for --tab-id. Inside a QUAKE panel there is no ambient tab at all, which
// is the deliberate outcome: the panel belongs to a directory, so the user
// names the agent they mean rather than having one picked for them.
func TestBindEntityFlags_AgentTabIDDefaultIgnoresTheTerminalID(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tabID   string
		tabType string
	}{
		{name: "inside an ordinary terminal tab", tabID: "term-1", tabType: "terminal"},
		{name: "inside a quake panel", tabID: "", tabType: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LEAPMUX_CONTROL_TAB_ID", tc.tabID)
			t.Setenv("LEAPMUX_CONTROL_TAB_TYPE", tc.tabType)
			t.Setenv("LEAPMUX_CONTROL_TERMINAL_ID", "quake-1")

			var in resolve.Inputs
			fs := flag.NewFlagSet("agent send", flag.ContinueOnError)
			resolve.BindEntityFlags(fs, &in, resolve.FlagOptions{
				FixedTabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
			})
			require.NoError(t, fs.Parse(nil))

			assert.Empty(t, in.TabID, "an agent command must never default to a terminal")
		})
	}
}

// The tolerance is for an AMBIENT tab id only. A tab id the user TYPED says
// "act on this tab", so a hub that refuses it has refused the request they
// made -- and continuing would run the command against whatever worker the
// environment names, on the wrong machine, reporting success.
//
// The distinction matters because every `Need{WorkerID: true}` command
// (`terminal quake`, `worker get`, `git status`, `file ...`, `agent providers`)
// is already satisfied by $LEAPMUX_CONTROL_WORKER_ID inside any spawn, so
// without it a foreign --tab-id was swallowed on all of them.
func TestResolve_ExplicitUnlocatableTabIsFatalEvenWhenNothingNeedsIt(t *testing.T) {
	deps := stubDeps{
		locateTab: func(_ context.Context, _ leapmuxv1.TabType, tabID string) (leapmuxv1.TabType, string, string, string, error) {
			return leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED, "", "", "",
				fmt.Errorf("%w: no such tab", resolve.ErrTabNotLocatable)
		},
	}.toDeps()

	_, err := resolve.Resolve(context.Background(), deps,
		resolve.Need{TabID: true, WorkerID: true},
		resolve.Inputs{
			TabID:         "tab-on-another-worker",
			ExplicitTabID: true,
			WorkerID:      "w1",
			FixedTabType:  leapmuxv1.TabType_TAB_TYPE_TERMINAL,
		},
	)

	require.Error(t, err, "a --tab-id the user typed and the hub refused must fail the command")
	assert.ErrorIs(t, err, resolve.ErrTabNotLocatable,
		"and it reports the hub's own refusal rather than a worker-side error later")
}

// The ambient counterpart of the case above, stated on the same inputs so the
// pair reads as one rule: identical everywhere except who supplied the tab id.
func TestResolve_AmbientUnlocatableTabStaysTolerated(t *testing.T) {
	deps := stubDeps{
		locateTab: func(_ context.Context, _ leapmuxv1.TabType, tabID string) (leapmuxv1.TabType, string, string, string, error) {
			return leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED, "", "", "",
				fmt.Errorf("%w: no such tab", resolve.ErrTabNotLocatable)
		},
	}.toDeps()

	got, err := resolve.Resolve(context.Background(), deps,
		resolve.Need{TabID: true, WorkerID: true},
		resolve.Inputs{
			TabID:         "tab-on-another-worker",
			ExplicitTabID: false,
			WorkerID:      "w1",
			FixedTabType:  leapmuxv1.TabType_TAB_TYPE_TERMINAL,
		},
	)

	require.NoError(t, err, "the quake panel's inherited tab id must still resolve")
	assert.Equal(t, "w1", got.WorkerID)
}

// The round trip is SKIPPED when the caller already holds everything it reads.
//
// Resolve used to fire LocateTab on the mere presence of a tab id, so every
// command run inside a spawn -- where both TAB_ID and WORKER_ID are exported --
// paid a hub call whose four outputs it discarded, and a transport failure on
// it failed a command whose inputs were complete.
func TestResolve_SkipsLocateTabWhenNothingItFillsIsRead(t *testing.T) {
	calls := 0
	deps := stubDeps{
		locateTab: func(_ context.Context, _ leapmuxv1.TabType, _ string) (leapmuxv1.TabType, string, string, string, error) {
			calls++
			return leapmuxv1.TabType_TAB_TYPE_TERMINAL, "ws-1", "tile-1", "worker-A", nil
		},
	}.toDeps()

	got, err := resolve.Resolve(context.Background(), deps,
		resolve.Need{TabID: true, WorkerID: true},
		resolve.Inputs{TabID: "term-1", WorkerID: "w1", FixedTabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL},
	)

	require.NoError(t, err)
	assert.Zero(t, calls, "the worker id was supplied, so nothing needed the tab's placement")
	assert.Equal(t, "w1", got.WorkerID)
}

// The other side of the same rule: a field the caller READS but does not
// require still fires the derivation, because an empty value would silently
// widen what the command acts on. RunTabList and RunEvents both scope by
// workspace this way.
func TestResolve_WantedFieldStillFiresTheDerivation(t *testing.T) {
	calls := 0
	deps := stubDeps{
		locateTab: func(_ context.Context, _ leapmuxv1.TabType, _ string) (leapmuxv1.TabType, string, string, string, error) {
			calls++
			return leapmuxv1.TabType_TAB_TYPE_TERMINAL, "ws-1", "tile-1", "w1", nil
		},
	}.toDeps()

	got, err := resolve.Resolve(context.Background(), deps,
		resolve.Need{WorkerID: true, Want: resolve.Wants{WorkspaceID: true}},
		resolve.Inputs{TabID: "term-1", WorkerID: "w1", FixedTabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL},
	)

	require.NoError(t, err)
	assert.Equal(t, 1, calls, "the workspace is read, and only the hub can supply it")
	assert.Equal(t, "ws-1", got.WorkspaceID)
}

// A tab type the caller switches on comes ONLY from LocateTab, so a command
// that reads it must still pay the call even with every id in hand. Without
// this, `tab rename` fell to its default arm and reported not_found for a tab
// that exists.
func TestResolve_WantedTabTypeStillFiresTheDerivation(t *testing.T) {
	calls := 0
	deps := stubDeps{
		locateTab: func(_ context.Context, _ leapmuxv1.TabType, _ string) (leapmuxv1.TabType, string, string, string, error) {
			calls++
			return leapmuxv1.TabType_TAB_TYPE_TERMINAL, "ws-1", "tile-1", "w1", nil
		},
	}.toDeps()

	got, err := resolve.Resolve(context.Background(), deps,
		resolve.Need{TabID: true, WorkerID: true, Want: resolve.Wants{TabType: true}},
		resolve.Inputs{TabID: "term-1", WorkerID: "w1"},
	)

	require.NoError(t, err)
	assert.Equal(t, 1, calls)
	assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_TERMINAL, got.TabType)
}

// The cost of skipping the derivation, stated so it is a decision rather than a
// surprise: two AMBIENT ids that disagree are no longer reported as conflicting.
//
// Inside a real spawn they cannot disagree -- one EnvVars call writes both, from
// one spawn -- so this needs a hand-assembled or stale environment. A tab id the
// user TYPED still fires the derivation and still conflicts, which is the case
// they can actually produce.
func TestResolve_AmbientIDsThatDisagreeAreNotCrossChecked(t *testing.T) {
	calls := 0
	deps := stubDeps{
		locateTab: func(_ context.Context, _ leapmuxv1.TabType, _ string) (leapmuxv1.TabType, string, string, string, error) {
			calls++
			return leapmuxv1.TabType_TAB_TYPE_TERMINAL, "ws-1", "tile-1", "worker-from-tab", nil
		},
	}.toDeps()

	got, err := resolve.Resolve(context.Background(), deps,
		resolve.Need{WorkerID: true},
		// Both from the environment, and they disagree.
		resolve.Inputs{TabID: "term-1", WorkerID: "worker-from-env", FixedTabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL},
	)

	require.NoError(t, err)
	assert.Zero(t, calls, "the worker id was already supplied, so the hub is not asked")
	assert.Equal(t, "worker-from-env", got.WorkerID, "the ambient value stands")
}
