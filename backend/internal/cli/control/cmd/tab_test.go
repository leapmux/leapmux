package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/controlipc"
)

// TestSpawnOptions_SeedsPermissionModeWithModelAndEffort guards S6: the permission mode rides into
// the OpenAgent options alongside model and effort (applied at launch), instead of a redundant
// post-spawn UpdateAgentSettings round-trip. Empty flags are omitted so the worker fills defaults.
func TestSpawnOptions_SeedsPermissionModeWithModelAndEffort(t *testing.T) {
	assert.Equal(t, map[string]string{
		optionids.Model:          "opus",
		optionids.Effort:         "high",
		optionids.PermissionMode: "plan",
	}, spawnOptions("opus", "high", "plan"))

	// Each flag is independent and an empty one is omitted (no "no change" sentinel leaks through).
	assert.Empty(t, spawnOptions("", "", ""))
	assert.Equal(t, map[string]string{optionids.PermissionMode: "plan"}, spawnOptions("", "", "plan"))
	assert.Equal(t, map[string]string{optionids.Model: "sonnet"}, spawnOptions("sonnet", "", ""))
}

// TestFilterTabsByType_DropsNonMatchingRows pins the central guarantee
// of `tab list --tab-type X`: only rows whose tab_type matches the
// flag survive. Without this, the user runs `tab list --tab-type agent`
// inside a terminal spawn and sees every tab in the workspace -- the
// bug that motivated the filter in the first place.
func TestFilterTabsByType_DropsNonMatchingRows(t *testing.T) {
	in := []*leapmuxv1.WorkspaceTab{
		{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "a1"}},
		{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabId: "t1"}},
		{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "a2"}},
		{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabId: "f1"}},
	}
	got := filterTabsByType(in, leapmuxv1.TabType_TAB_TYPE_AGENT)
	ids := make([]string, 0, len(got))
	for _, t := range got {
		ids = append(ids, t.GetTab().GetTabId())
	}
	assert.Equal(t, []string{"a1", "a2"}, ids)
}

// TestFilterTabsByType_UnspecifiedReturnsAll documents the "no filter"
// behaviour. A bare `tab list` invocation parses --tab-type to
// TAB_TYPE_UNSPECIFIED, which means "no filter" -- the entire response
// passes through. This is the contract callers rely on to avoid a
// nil/empty special case at the call site.
func TestFilterTabsByType_UnspecifiedReturnsAll(t *testing.T) {
	in := []*leapmuxv1.WorkspaceTab{
		{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "a1"}},
		{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabId: "t1"}},
	}
	got := filterTabsByType(in, leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED)
	assert.Len(t, got, 2)
	assert.Equal(t, "a1", got[0].GetTab().GetTabId())
	assert.Equal(t, "t1", got[1].GetTab().GetTabId())
}

// TestFilterTabsByType_NoMatchesYieldsEmpty pins that a filter with no
// matching rows returns a length-zero slice, not a nil-shaped surprise.
func TestFilterTabsByType_NoMatchesYieldsEmpty(t *testing.T) {
	in := []*leapmuxv1.WorkspaceTab{
		{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabId: "t1"}},
	}
	got := filterTabsByType(in, leapmuxv1.TabType_TAB_TYPE_AGENT)
	assert.Empty(t, got)
}

// `terminal read` prints the retained bytes as they are, escape sequences
// included, and --json carries them as a string, not as base64.
func TestRawScreen_KeepsTheBytes(t *testing.T) {
	screen := rawScreen{ID: "term-1", Cols: 80, Rows: 25, ScreenEndOffset: 17, Screen: "hello\x1b[31mworld\x1b[m\n"}
	var text strings.Builder
	require.NoError(t, screen.RenderText(&text))
	assert.Equal(t, "hello\x1b[31mworld\x1b[m\n", text.String())

	encoded, err := json.Marshal(screen)
	require.NoError(t, err)
	// encoding/json escapes the ESC byte (0x1b) as the six-character
	// sequence backslash-u-0-0-1-b; the "hello" / "world" tokens stay
	// ASCII. The base64 form (aGVsbG8b...) must not appear in the
	// payload -- that would mean a regression back to the default
	// []byte JSON encoding.
	assert.Contains(t, string(encoded), `"screen":"hello\u001b[31mworld\u001b[m\n"`)
	assert.NotContains(t, string(encoded), "aGVsbG8")
}

// `terminal send` refuses an empty input, so a script cannot ship a
// zero-byte write to the PTY by accident.
func TestTerminalSend_RequiresTheText(t *testing.T) {
	clearRemoteEnv(t)
	for name, args := range map[string][]string{
		"no text":    {"terminal", "send", fullTestID("term-1")},
		"empty file": {"terminal", "send", fullTestID("term-1"), "--file", "/dev/null"},
	} {
		t.Run(name, func(t *testing.T) {
			o := runControl(t, append(args, "--hub", "https://stub.invalid")...)
			o.requireFailure(t, "usage")
		})
	}
}

// `terminal send` keeps whitespace: a lone newline is the Enter key, not an
// empty message.
func TestTerminalSend_KeepsWhitespace(t *testing.T) {
	id := fullTestID("term-1")
	disp := &sendInputDispatcher{}
	startSpawnIPC(t, &recordingHub{locateTab: &leapmuxv1.WorkspaceTab{
		Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabId: id},
		TileId:      "root-1",
		WorkerId:    "worker-A",
		WorkspaceId: "ws-1",
	}}, disp)

	data := runControl(t, "terminal", "send", id, "\n").Data(t)

	assert.EqualValues(t, 1, data["bytes_sent"])
	require.Len(t, disp.inputs, 1)
	assert.Equal(t, []byte("\n"), disp.inputs[0].GetData())
	assert.Equal(t, id, disp.inputs[0].GetTerminalId())
}

// sendInputDispatcher records the SendInput requests that reach the worker.
type sendInputDispatcher struct {
	inputs []*leapmuxv1.SendInputRequest
}

func (d *sendInputDispatcher) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter) {
	var in leapmuxv1.SendInputRequest
	if req.GetMethod() != leapmuxv1connect.WorkerTerminalServiceSendInputProcedure || proto.Unmarshal(req.GetPayload(), &in) != nil {
		_ = w.SendError(int32(codes.Unimplemented), "unexpected method: "+req.GetMethod())
		return
	}
	d.inputs = append(d.inputs, &in)
	writeProtoResponse(w, &leapmuxv1.SendInputResponse{})
}

// TestFirstLiveLeaf_RootIsLeafReturnsRoot pins the simplest happy
// path: a workspace whose root_node_id is itself a leaf resolves to
// that node. `tab move --target-workspace-id` relies on this for the
// common case of freshly-seeded workspaces.
func TestFirstLiveLeaf_RootIsLeafReturnsRoot(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "root-1").
		leafNode("root-1", "", "").
		st
	assert.Equal(t, "root-1", firstLiveLeaf(state, "root-1"))
}

// TestFirstLiveLeaf_PrefersLowerPosition pins the ordering contract:
// children are visited in (position, node_id) order, so the leaf with
// the smallest LexoRank wins even when its node_id sorts later.
func TestFirstLiveLeaf_PrefersLowerPosition(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "root-1").
		splitNode("root-1", "", "").
		leafNode("leaf-z", "root-1", "0|aaaaaa:"). // smaller position
		leafNode("leaf-a", "root-1", "0|bbbbbb:"). // node-id sorts earlier but position is later
		st
	assert.Equal(t, "leaf-z", firstLiveLeaf(state, "root-1"))
}

// TestFirstLiveLeaf_DescendsIntoSplits pins that non-leaf nodes don't
// satisfy the leaf-only contract — the walk recurses into the first
// SPLIT child until it finds a real leaf.
func TestFirstLiveLeaf_DescendsIntoSplits(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "root-1").
		splitNode("root-1", "", "").
		splitNode("split-a", "root-1", "0|aaaaaa:").
		leafNode("leaf-a1", "split-a", "0|aaaaaa:").
		leafNode("leaf-a2", "split-a", "0|bbbbbb:").
		leafNode("leaf-b", "root-1", "0|bbbbbb:").
		st
	assert.Equal(t, "leaf-a1", firstLiveLeaf(state, "root-1"))
}

// TestFirstLiveLeaf_SkipsTombstonedLeaves pins that tombstoned nodes
// are filtered out of the DFS. The first live leaf wins, not the
// first leaf in document order.
func TestFirstLiveLeaf_SkipsTombstonedLeaves(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "root-1").
		splitNode("root-1", "", "").
		tombstonedNode("leaf-dead", "root-1").
		leafNode("leaf-alive", "root-1", "0|bbbbbb:").
		st
	assert.Equal(t, "leaf-alive", firstLiveLeaf(state, "root-1"))
}

// TestFirstLiveLeaf_NoLiveLeafReturnsEmpty covers the degenerate case
// where every leaf under the root is tombstoned. `tab move` surfaces
// this as `not_found` so a typo on --target-workspace-id (resolving
// to an empty-tree workspace) doesn't silently misroute the tab.
func TestFirstLiveLeaf_NoLiveLeafReturnsEmpty(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "root-1").
		splitNode("root-1", "", "").
		tombstonedNode("leaf-dead", "root-1").
		st
	assert.Empty(t, firstLiveLeaf(state, "root-1"))
}

// TestFirstLiveLeaf_EmptyRootReturnsEmpty covers the caller-side
// guard: an empty rootNodeID (workspace not yet seeded with a root)
// resolves to "" rather than panicking on the nil node lookup.
func TestFirstLiveLeaf_EmptyRootReturnsEmpty(t *testing.T) {
	state := newStateBuilder().st
	assert.Empty(t, firstLiveLeaf(state, ""))
}

// TestParsePositionSpec_DefaultIsLast pins the documented default:
// when no placement flag is set, the spec is positionLast. Without
// this, callers running bare `tab open --type=agent` would land at
// `lexorank.First()` and overlap whatever else is on the tile.
func TestParsePositionSpec_DefaultIsLast(t *testing.T) {
	spec, err := parsePositionSpec(false, false, "", "")
	require.NoError(t, err)
	assert.Equal(t, positionLast, spec.kind)
	assert.Empty(t, spec.refID)
}

// TestParsePositionSpec_AcceptsEachKind pins that each flag selects
// the matching kind, and that --before / --after carry their refID
// through to the spec.
func TestParsePositionSpec_AcceptsEachKind(t *testing.T) {
	cases := []struct {
		name      string
		first     bool
		last      bool
		beforeRef string
		afterRef  string
		want      positionSpec
	}{
		{"first", true, false, "", "", positionSpec{kind: positionFirst}},
		{"last", false, true, "", "", positionSpec{kind: positionLast}},
		{"before", false, false, "ref-a", "", positionSpec{kind: positionBefore, refID: "ref-a"}},
		{"after", false, false, "", "ref-b", positionSpec{kind: positionAfter, refID: "ref-b"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spec, err := parsePositionSpec(c.first, c.last, c.beforeRef, c.afterRef)
			require.NoError(t, err)
			assert.Equal(t, c.want, spec)
		})
	}
}

// TestParsePositionSpec_RejectsMutualExclusion covers every pairwise
// combination of placement flags. The user instruction was explicit:
// fail if more than one flag is set.
func TestParsePositionSpec_RejectsMutualExclusion(t *testing.T) {
	cases := []struct {
		name      string
		first     bool
		last      bool
		beforeRef string
		afterRef  string
	}{
		{"first+last", true, true, "", ""},
		{"first+before", true, false, "ref-a", ""},
		{"first+after", true, false, "", "ref-b"},
		{"last+before", false, true, "ref-a", ""},
		{"last+after", false, true, "", "ref-b"},
		{"before+after", false, false, "ref-a", "ref-b"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := parsePositionSpec(c.first, c.last, c.beforeRef, c.afterRef)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "mutually exclusive")
		})
	}
}

// resolvePositionSpec test helper that wraps captureEmit so test
// authors can assert against the structured envelope instead of the
// raw error.
func runResolvePositionSpec(t *testing.T, state *leapmuxv1.UserMaterialized, destTileID, movingTabID string, spec positionSpec) (tileID, position, code, message string) {
	t.Helper()
	code, message = captureEmit(t, func() error {
		var rTile, rPos string
		var err error
		rTile, rPos, err = resolvePositionSpec(state, destTileID, movingTabID, spec)
		tileID, position = rTile, rPos
		return err
	})
	return tileID, position, code, message
}

// TestResolvePositionSpec_LastOnEmptyTileSeedsFirstRank pins the
// empty-tile case: `--last` on a tile with no live tabs returns
// `lexorank.First()` so the very first tab on a tile gets a
// well-defined rank.
func TestResolvePositionSpec_LastOnEmptyTileSeedsFirstRank(t *testing.T) {
	state := newStateBuilder().workspace("ws-1", "root-1").leafNode("root-1", "", "").st
	tile, pos, code, _ := runResolvePositionSpec(t, state, "root-1", "", positionSpec{kind: positionLast})
	assert.Empty(t, code)
	assert.Equal(t, "root-1", tile)
	assert.Equal(t, "n", pos)
}

// TestResolvePositionSpec_LastAppendsAfterLastTab pins the happy
// "append" path: with two existing tabs, --last must produce a rank
// strictly greater than the larger existing position.
func TestResolvePositionSpec_LastAppendsAfterLastTab(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "root-1").
		leafNode("root-1", "", "").
		tabAt("a", "root-1", "b", leapmuxv1.TabType_TAB_TYPE_AGENT).
		tabAt("b", "root-1", "n", leapmuxv1.TabType_TAB_TYPE_AGENT).
		st
	_, pos, code, _ := runResolvePositionSpec(t, state, "root-1", "", positionSpec{kind: positionLast})
	assert.Empty(t, code)
	assert.Greater(t, pos, "n", "--last must produce a rank greater than the current max")
}

// TestResolvePositionSpec_FirstPrependsBeforeFirstTab mirrors the
// last-tab test for the other end.
func TestResolvePositionSpec_FirstPrependsBeforeFirstTab(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "root-1").
		leafNode("root-1", "", "").
		tabAt("a", "root-1", "n", leapmuxv1.TabType_TAB_TYPE_AGENT).
		st
	_, pos, code, _ := runResolvePositionSpec(t, state, "root-1", "", positionSpec{kind: positionFirst})
	assert.Empty(t, code)
	assert.Less(t, pos, "n", "--first must produce a rank less than the current min")
}

// TestResolvePositionSpec_BeforeDerivesTileFromRef pins the docstring
// behaviour: --before without an explicit dest tile inherits the
// ref tab's tile. Without this, --before would refuse to run unless
// the caller redundantly typed --tile-id.
func TestResolvePositionSpec_BeforeDerivesTileFromRef(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "root-1").
		leafNode("root-1", "", "").
		tabAt("ref", "root-1", "n", leapmuxv1.TabType_TAB_TYPE_AGENT).
		st
	tile, pos, code, _ := runResolvePositionSpec(t, state, "", "", positionSpec{kind: positionBefore, refID: "ref"})
	assert.Empty(t, code)
	assert.Equal(t, "root-1", tile)
	assert.Less(t, pos, "n")
}

// TestResolvePositionSpec_AfterBetweenSiblings pins the
// "insert between two siblings" math: --after a should yield a rank
// strictly between a's and b's positions on the same tile.
func TestResolvePositionSpec_AfterBetweenSiblings(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "root-1").
		leafNode("root-1", "", "").
		tabAt("a", "root-1", "b", leapmuxv1.TabType_TAB_TYPE_AGENT).
		tabAt("b", "root-1", "x", leapmuxv1.TabType_TAB_TYPE_AGENT).
		st
	_, pos, code, _ := runResolvePositionSpec(t, state, "", "", positionSpec{kind: positionAfter, refID: "a"})
	assert.Empty(t, code)
	assert.Greater(t, pos, "b", "rank must be greater than ref tab a's position")
	assert.Less(t, pos, "x", "rank must be less than next tab b's position")
}

// TestResolvePositionSpec_BeforeRejectsCrossTileMismatch pins the
// consistency guard for the "before tab X on tile T" case when the
// caller also names a different tile via --target-tile-id. Silently
// overriding either input is worse than erroring.
func TestResolvePositionSpec_BeforeRejectsCrossTileMismatch(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "root-1").
		leafNode("root-1", "", "").
		leafNode("other-tile", "", "").
		tabAt("ref", "root-1", "n", leapmuxv1.TabType_TAB_TYPE_AGENT).
		st
	_, _, code, msg := runResolvePositionSpec(t, state, "other-tile", "", positionSpec{kind: positionBefore, refID: "ref"})
	assert.Equal(t, "usage", code)
	assert.Contains(t, msg, "other-tile")
}

// TestResolvePositionSpec_BeforeRejectsMissingRef covers the simple
// "typoed tab id" case.
func TestResolvePositionSpec_BeforeRejectsMissingRef(t *testing.T) {
	state := newStateBuilder().workspace("ws-1", "root-1").leafNode("root-1", "", "").st
	_, _, code, msg := runResolvePositionSpec(t, state, "", "", positionSpec{kind: positionBefore, refID: "ghost"})
	assert.Equal(t, "not_found", code)
	assert.Contains(t, msg, "ghost")
}

// TestResolvePositionSpec_BeforeRejectsTombstonedRef pins that a
// tombstoned ref is treated as "doesn't exist" rather than silently
// landing the new tab next to a dead row.
func TestResolvePositionSpec_BeforeRejectsTombstonedRef(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "root-1").
		leafNode("root-1", "", "").
		tombstonedTab("dead", "root-1", leapmuxv1.TabType_TAB_TYPE_AGENT).
		st
	_, _, code, msg := runResolvePositionSpec(t, state, "", "", positionSpec{kind: positionAfter, refID: "dead"})
	assert.Equal(t, "not_found", code)
	assert.Contains(t, msg, "tombstoned")
}

// TestResolvePositionSpec_BeforeRejectsSelfMove pins that `tab move`
// can't reference itself in --before / --after. The CRDT op would
// overwrite the tab's own position mid-computation, which is more
// likely a user error than an intended noop.
func TestResolvePositionSpec_BeforeRejectsSelfMove(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "root-1").
		leafNode("root-1", "", "").
		tabAt("mover", "root-1", "n", leapmuxv1.TabType_TAB_TYPE_AGENT).
		st
	_, _, code, msg := runResolvePositionSpec(t, state, "", "mover", positionSpec{kind: positionBefore, refID: "mover"})
	assert.Equal(t, "usage", code)
	assert.Contains(t, msg, "tab being moved")
}

// TestResolvePositionSpec_LastWithoutDestTileErrors covers the
// caller-side contract: --first / --last need a destination tile to
// scan for siblings, and we error rather than guess.
func TestResolvePositionSpec_LastWithoutDestTileErrors(t *testing.T) {
	state := newStateBuilder().st
	_, _, code, _ := runResolvePositionSpec(t, state, "", "", positionSpec{kind: positionLast})
	assert.Equal(t, "usage", code)
}

// TestResolvePositionSpec_LastExcludesMovingTab pins the move-time
// optimization: the moving tab's current position must not influence
// its own destination rank. With three tabs at b/n/x on the same
// tile, moving the last tab to --last should produce a rank above
// `n` but should not also count its existing `x` as a sibling.
func TestResolvePositionSpec_LastExcludesMovingTab(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "root-1").
		leafNode("root-1", "", "").
		tabAt("a", "root-1", "b", leapmuxv1.TabType_TAB_TYPE_AGENT).
		tabAt("b", "root-1", "n", leapmuxv1.TabType_TAB_TYPE_AGENT).
		tabAt("c", "root-1", "x", leapmuxv1.TabType_TAB_TYPE_AGENT).
		st
	_, pos, code, _ := runResolvePositionSpec(t, state, "root-1", "c", positionSpec{kind: positionLast})
	assert.Empty(t, code)
	// After excluding the moving tab "c" (at "x"), the surviving max
	// is "n" — the new rank must beat that, not "x".
	assert.Greater(t, pos, "n")
	assert.Less(t, pos, "x", "moving tab's own position must not influence the new rank")
}

// TestParseTabCloseWorktree_KnownValues pins keep / push / discard /
// remove as the accepted --worktree values for `tab close`. "remove"
// is an alias for "discard" so scripts that already used "remove" for
// the WorktreeAction enum keep working without modification.
func TestParseTabCloseWorktree_KnownValues(t *testing.T) {
	for _, c := range []struct {
		in   string
		want tabCloseWorktree
	}{
		{"", closeWorktreeUnspecified},
		{"keep", closeWorktreeKeep},
		{"push", closeWorktreePush},
		{"discard", closeWorktreeDiscard},
		{"remove", closeWorktreeDiscard},
	} {
		got, err := parseTabCloseWorktree(c.in)
		require.NoErrorf(t, err, "parseTabCloseWorktree(%q)", c.in)
		assert.Equalf(t, c.want, got, "parseTabCloseWorktree(%q)", c.in)
	}
}

// TestParseTabCloseWorktree_RejectsUnknown ensures typos / unsupported
// values surface as invalid_request rather than silently falling to
// the unspecified default — silent defaulting would defeat the
// forced-choice gate at the last-tab decision point.
func TestParseTabCloseWorktree_RejectsUnknown(t *testing.T) {
	for _, in := range []string{"force", "delete", "yes", "KEEP"} {
		_, err := parseTabCloseWorktree(in)
		require.Errorf(t, err, "parseTabCloseWorktree(%q) should error", in)
	}
}

// TestTabCloseWorktree_WorktreeAction maps each parsed choice to the
// CloseTab WorktreeAction enum. push collapses to KEEP
// because the push side-effect ran before close — the worktree stays.
func TestTabCloseWorktree_WorktreeAction(t *testing.T) {
	assert.Equal(t, leapmuxv1.WorktreeAction_WORKTREE_ACTION_UNSPECIFIED, closeWorktreeUnspecified.worktreeAction())
	assert.Equal(t, leapmuxv1.WorktreeAction_WORKTREE_ACTION_KEEP, closeWorktreeKeep.worktreeAction())
	assert.Equal(t, leapmuxv1.WorktreeAction_WORKTREE_ACTION_KEEP, closeWorktreePush.worktreeAction())
	assert.Equal(t, leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE, closeWorktreeDiscard.worktreeAction())
}

// TestLastTabPromptMessage_WorktreeWithDirty surfaces the worktree
// path and diff stats so the user can decide whether to push or
// discard without re-running inspect manually.
func TestLastTabPromptMessage_WorktreeWithDirty(t *testing.T) {
	env := failureOf(t, lastTabPromptError(&leapmuxv1.InspectLastTabCloseResponse{
		Target:       leapmuxv1.LastTabCloseTarget_LAST_TAB_CLOSE_TARGET_WORKTREE,
		WorktreePath: "/repo/wt-foo",
		GitState: &leapmuxv1.BranchGitState{
			HasUncommittedChanges: true,
			DiffAdded:             3,
			DiffDeleted:           1,
			DiffUntracked:         2,
			UnpushedCommitCount:   2,
		},
	}))
	msg := env.Error["message"]
	assert.Equal(t, "usage", env.Error["code"])
	assert.Contains(t, msg, "/repo/wt-foo")
	assert.Contains(t, msg, "3 added / 1 deleted / 2 untracked")
	assert.Contains(t, msg, "2 unpushed commits")
	assert.Contains(t, env.Error["hints"], "--worktree=keep|push|discard")
}

// TestLastTabPromptMessage_BranchUnpushedSingular pluralizes commit
// count correctly. Mirrors the frontend dialog's `pluralize` call.
func TestLastTabPromptMessage_BranchUnpushedSingular(t *testing.T) {
	env := failureOf(t, lastTabPromptError(&leapmuxv1.InspectLastTabCloseResponse{
		Target:     leapmuxv1.LastTabCloseTarget_LAST_TAB_CLOSE_TARGET_BRANCH,
		BranchName: "feat-x",
		GitState:   &leapmuxv1.BranchGitState{UnpushedCommitCount: 1},
	}))
	msg := env.Error["message"]
	assert.Contains(t, msg, "feat-x")
	assert.Contains(t, msg, "1 unpushed commit")
	assert.NotContains(t, msg, "1 unpushed commits")
	assert.Contains(t, env.Error["hints"], "--worktree=keep|push ")
	assert.NotContains(t, env.Error["hints"], "discard", "a branch has no worktree to discard")
}

// TestLastTabPromptMessage_CleanWorktree pins the clean-worktree
// message: no diff / unpushed details, but the user still has to
// pass --worktree because the dialog (and now the CLI) forces an
// explicit keep / discard choice when target=WORKTREE.
func TestLastTabPromptMessage_CleanWorktree(t *testing.T) {
	env := failureOf(t, lastTabPromptError(&leapmuxv1.InspectLastTabCloseResponse{
		Target:       leapmuxv1.LastTabCloseTarget_LAST_TAB_CLOSE_TARGET_WORKTREE,
		WorktreePath: "/repo/wt-clean",
	}))
	msg := env.Error["message"]
	assert.Contains(t, msg, "/repo/wt-clean")
	assert.Contains(t, env.Error["hints"], "--worktree=keep|push|discard")
	assert.NotContains(t, msg, "added")
	assert.NotContains(t, msg, "unpushed")
}

// TestDiscardRefusalMessage_BlocksOnlyDiscard pins the disposition gate: the
// worker's reason describes the REMOVAL, so it must refuse `discard` and leave
// `keep` / `push` alone -- both of those leave the worktree in place, and a
// locked worktree is no reason to stop them.
func TestDiscardRefusalMessage_BlocksOnlyDiscard(t *testing.T) {
	resp := &leapmuxv1.InspectLastTabCloseResponse{
		ShouldPrompt:                 true,
		Target:                       leapmuxv1.LastTabCloseTarget_LAST_TAB_CLOSE_TARGET_WORKTREE,
		WorktreeRemovalBlockedReason: "This worktree is locked (held for review). Unlock it with `git worktree unlock` first.",
	}

	msg := discardRefusalMessage(closeWorktreeDiscard, resp)
	assert.Contains(t, msg, "cannot discard: ")
	assert.Contains(t, msg, "held for review",
		"the worker's reason is the actionable half; the CLI only frames it")

	for _, wt := range []tabCloseWorktree{closeWorktreeKeep, closeWorktreePush, closeWorktreeUnspecified} {
		assert.Emptyf(t, discardRefusalMessage(wt, resp),
			"--worktree=%q keeps the worktree, so a removal refusal must not block it", string(wt))
	}
}

// The mirror case: an empty reason must let the discard through. The preflight
// reports only the refusals it can state, so "no known refusal" has to read as
// "proceed" -- otherwise every discard would be refused.
func TestDiscardRefusalMessage_EmptyReasonAllowsDiscard(t *testing.T) {
	assert.Empty(t, discardRefusalMessage(closeWorktreeDiscard, &leapmuxv1.InspectLastTabCloseResponse{
		ShouldPrompt: true,
		Target:       leapmuxv1.LastTabCloseTarget_LAST_TAB_CLOSE_TARGET_WORKTREE,
	}))
	// An absent Worker response cannot authorize a discard.
	assert.NotEmpty(t, discardRefusalMessage(closeWorktreeDiscard, nil))
}

// closeDispatcher answers the worker-local inner RPCs `tab close` issues, and
// records every method it was asked for. `inspect` is the InspectLastTabClose
// verdict; every other method fails, so a test can prove which ones the command
// reached.
type closeDispatcher struct {
	inspect *leapmuxv1.InspectLastTabCloseResponse
	// agents / terminals arm the busy gate's two reads. Both empty by default,
	// which is what an idle tab looks like.
	agents    []*leapmuxv1.AgentInfo
	terminals []*leapmuxv1.TerminalProcesses
	// closeTab, when non-nil, answers CloseTab, for a test that needs the whole
	// close to succeed. Otherwise CloseTab fails as any other method does.
	closeTab *leapmuxv1.CloseTabResponse

	mu      sync.Mutex
	methods []string
}

func (d *closeDispatcher) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter) {
	d.mu.Lock()
	d.methods = append(d.methods, req.GetMethod())
	d.mu.Unlock()
	var reply proto.Message
	switch req.GetMethod() {
	case leapmuxv1connect.WorkerGitServiceInspectLastTabCloseProcedure:
		reply = d.inspect
	case leapmuxv1connect.WorkerAgentServiceListAgentsProcedure:
		// The busy gate's agent branch. Empty (an idle agent) unless a test arms it.
		reply = &leapmuxv1.ListAgentsResponse{Agents: d.agents}
	case leapmuxv1connect.WorkerTerminalServiceInspectTerminalProcessesProcedure:
		reply = &leapmuxv1.InspectTerminalProcessesResponse{Terminals: d.terminals}
	case leapmuxv1connect.WorkerTabServiceCloseTabProcedure:
		if d.closeTab == nil {
			_ = w.SendError(int32(codes.Unimplemented), "unexpected method: "+req.GetMethod())
			return
		}
		reply = d.closeTab
	default:
		_ = w.SendError(int32(codes.Unimplemented), "unexpected method: "+req.GetMethod())
		return
	}
	payload, err := proto.Marshal(reply)
	if err != nil {
		_ = w.SendError(int32(codes.Internal), err.Error())
		return
	}
	_ = w.SendResponse(channelwire.NewPayloadResponse(payload))
}

func (d *closeDispatcher) called() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.methods...)
}

// TestRunTabClose_BlockedDiscardRefusesBeforeTheTombstone drives the whole
// command against a worker that reports the removal blocked. The unit tests
// above cover discardRefusalMessage as a function; this covers the thing that
// makes it worth anything, which is WHERE the command calls it.
//
// The removal runs after the tab is gone, so a refusal that arrives afterwards
// reaches a command that already reported success and a tab that is already
// destroyed. Only the ORDER prevents that, and only an end-to-end run can see
// it: the CRDT tombstone (SubmitOps) and the worker teardown (CloseTab) must
// both be absent from what the command issued.
func TestRunTabClose_BlockedDiscardRefusesBeforeTheTombstone(t *testing.T) {
	disp := &closeDispatcher{inspect: &leapmuxv1.InspectLastTabCloseResponse{
		ShouldPrompt:                 true,
		Target:                       leapmuxv1.LastTabCloseTarget_LAST_TAB_CLOSE_TARGET_WORKTREE,
		WorktreePath:                 "/tmp/wt",
		WorktreeRemovalBlockedReason: "This worktree is locked (held for review). Unlock it with `git worktree unlock` first.",
	}}
	hub := &recordingHub{
		workers: workerRows("worker-A"),
		materialized: newStateBuilder().
			workspace("ws-1", "root-1").
			leafNode("root-1", "", "m").
			tab("agent-2", "root-1", leapmuxv1.TabType_TAB_TYPE_AGENT).
			st,
	}
	startSpawnIPC(t, hub, disp)

	// agent-2, not the caller's own agent-1: guardTabClose refuses a
	// self-close without --allow-self, which would mask the refusal under test.
	env := runControl(t, "tab", "close", "agent-2", "--worktree", "discard").envelope(t)
	require.NotNil(t, env.Error, "a blocked discard must fail the command")
	assert.Equal(t, "usage", env.Error["code"])
	assert.Contains(t, env.Error["message"], "cannot discard: ")
	assert.Contains(t, env.Error["message"], "held for review",
		"the worker's reason is the actionable half; the CLI only frames it")

	assert.NotContains(t, hub.called(), leapmuxv1connect.UserCRDTSubmitOpsProcedure,
		"the refusal must land BEFORE the CRDT tombstone, or the tab is destroyed and the refusal has nowhere to go")
	assert.Equal(t, []string{leapmuxv1connect.WorkerGitServiceInspectLastTabCloseProcedure}, disp.called(),
		"the command must stop after the inspect: no CloseTab, no worktree teardown")
}

// The mirror case over the same end-to-end path: an empty reason must let the
// discard through. A gate that refused every discard would pass the test above
// and break the feature.
func TestRunTabClose_UnblockedDiscardProceedsToTheTombstone(t *testing.T) {
	disp := &closeDispatcher{inspect: &leapmuxv1.InspectLastTabCloseResponse{
		ShouldPrompt: true,
		Target:       leapmuxv1.LastTabCloseTarget_LAST_TAB_CLOSE_TARGET_WORKTREE,
		WorktreePath: "/tmp/wt",
	}}
	hub := &recordingHub{
		workers: workerRows("worker-A"),
		materialized: newStateBuilder().
			workspace("ws-1", "root-1").
			leafNode("root-1", "", "m").
			tab("agent-2", "root-1", leapmuxv1.TabType_TAB_TYPE_AGENT).
			st,
	}
	startSpawnIPC(t, hub, disp)

	// The hub stub denies SubmitOps, so the command fails AFTER the gate.
	// What this pins is that it got that far at all.
	_ = runControl(t, "tab", "close", "agent-2", "--worktree", "discard")

	assert.Contains(t, hub.called(), leapmuxv1connect.UserCRDTSubmitOpsProcedure,
		"an empty reason is not a refusal; the close must reach the tombstone")
}

// tabCloseHub builds the hub for a `tab close` of agent-2. It commits the
// tombstone, which a hub-transport test needs to reach the end of the command.
func tabCloseHub() *recordingHub {
	return &recordingHub{
		workers: workerRows("worker-A"),
		materialized: newStateBuilder().
			workspace("ws-1", "root-1").
			leafNode("root-1", "", "m").
			tab("agent-2", "root-1", leapmuxv1.TabType_TAB_TYPE_AGENT).
			st,
		onSubmitOps: func(context.Context) ([]byte, error) {
			return proto.Marshal(&leapmuxv1.SubmitOpsResponse{Results: []*leapmuxv1.BatchResult{{}}})
		},
	}
}

// tabCloseArgs returns the arguments of that `tab close` for a hub client.
func tabCloseArgs(hubURL string) []string {
	return []string{"tab", "close", "agent-2", "--hub", hubURL}
}

// A hub client runs the whole close over the channel that it hoists: the
// inspect, the busy probe and the teardown. The channel closes when the body of
// withBestEffortWorkerChannel returns, so a call that runs after the body
// fails with "channel closed". The command then reported inspect_failed and left
// the tab open on every online worker. Only the hub transport has a channel, so
// no test over local IPC can see this.
func TestRunTabClose_HubClientRunsTheCloseOverOneChannel(t *testing.T) {
	disp := &closeDispatcher{
		inspect:  &leapmuxv1.InspectLastTabCloseResponse{},
		closeTab: &leapmuxv1.CloseTabResponse{},
	}
	hub := tabCloseHub()
	harness := startHubChannelHarness(t, hub, disp)

	o := runControl(t, tabCloseArgs(harness.url)...)
	require.Len(t, o.lines(), 1, "exactly one envelope: %s", o.Stdout)
	data := o.Data(t)
	assert.Equal(t, true, data["tombstoned"])
	assert.NotContains(t, data, "worker_close_error", "the teardown ran on the same live channel")

	assert.Contains(t, hub.called(), leapmuxv1connect.UserCRDTSubmitOpsProcedure)
	assert.Equal(t, []string{
		leapmuxv1connect.WorkerGitServiceInspectLastTabCloseProcedure,
		leapmuxv1connect.WorkerAgentServiceListAgentsProcedure,
		leapmuxv1connect.WorkerTabServiceCloseTabProcedure,
	}, disp.called())
	assert.Equal(t, 1, harness.channelOpens(), "the close pays for one handshake")
}

// A refusal from an online worker is not an unreachable worker. It reaches the
// operator with the worker's code, and the tab stays open.
func TestRunTabClose_HubClientKeepsTheTabWhenTheWorkerRefusesTheInspect(t *testing.T) {
	hub := tabCloseHub()
	harness := startHubChannelHarness(t, hub, inBandErrorDispatcher{code: codes.Internal, message: "git failed"})

	o := runControl(t, tabCloseArgs(harness.url)...)
	require.Len(t, o.lines(), 1, "exactly one envelope: %s", o.Stdout)
	f := o.requireFailure(t, "internal")
	assert.Equal(t, "git failed", f.Message)
	assert.NotContains(t, hub.called(), leapmuxv1connect.UserCRDTSubmitOpsProcedure)
}

// A tab whose worker is gone must still close. The shared channel fails to
// open, so each call opens its own and reports channel_open_failed, which
// allows the close through the CRDT alone.
func TestRunTabClose_HubClientTombstonesTheTabOfAnUnreachableWorker(t *testing.T) {
	disp := &closeDispatcher{inspect: &leapmuxv1.InspectLastTabCloseResponse{}, closeTab: &leapmuxv1.CloseTabResponse{}}
	hub := tabCloseHub()
	hub.refuseChannelOpen = connect.NewError(connect.CodeUnavailable, "worker is offline")
	harness := startHubChannelHarness(t, hub, disp)

	o := runControl(t, tabCloseArgs(harness.url)...)
	require.Len(t, o.lines(), 1, "exactly one envelope: %s", o.Stdout)
	data := o.Data(t)
	assert.Equal(t, true, data["tombstoned"])
	assert.Contains(t, data["worker_close_error"], "worker is offline", "the operator learns that the teardown did not run")

	assert.Contains(t, hub.called(), leapmuxv1connect.UserCRDTSubmitOpsProcedure)
	assert.Empty(t, disp.called(), "no call reached the worker")
	assert.Zero(t, harness.channelOpens())
}

// recordedWorkerCall is one inner-RPC dispatchWorkerClose issued.
type recordedWorkerCall struct {
	method string
	in     proto.Message
}

// recordingWorkerCaller returns a workerCaller that records instead of
// dispatching, so the teardown routing can be asserted without a worker.
func recordingWorkerCaller(calls *[]recordedWorkerCall) workerCaller {
	return func(method string, in, _ proto.Message) error {
		*calls = append(*calls, recordedWorkerCall{method: method, in: in})
		return nil
	}
}

// replyingWorkerCaller records like the above and also writes a canned reply
// into the caller's `out` message, so a response field can be asserted.
func replyingWorkerCaller(calls *[]recordedWorkerCall, reply proto.Message) workerCaller {
	return func(method string, in, out proto.Message) error {
		*calls = append(*calls, recordedWorkerCall{method: method, in: in})
		if reply != nil {
			proto.Reset(out)
			proto.Merge(out, reply)
		}
		return nil
	}
}

// The subagent tabs an agent close retires come from the WORKER: an agent id is
// also its tab id, and the CRDT's TabRecord carries no parent link, so the CLI
// cannot derive the subtree itself. Dropping the response on the floor is what
// left `leapmux control tab close <root>` retiring the root and leaving every
// subagent tab behind.
func TestDispatchWorkerClose_AgentReturnsTheSubagentTabsToRetire(t *testing.T) {
	var calls []recordedWorkerCall
	subagents, err := dispatchWorkerClose(
		replyingWorkerCaller(&calls, &leapmuxv1.CloseTabResponse{
			DescendantAgentIds: []string{"grandchild-1", "child-1"},
		}),
		"w1", "root-1",
		leapmuxv1.TabType_TAB_TYPE_AGENT,
		leapmuxv1.WorktreeAction_WORKTREE_ACTION_KEEP,
	)
	require.NoError(t, err)
	require.Len(t, calls, 1)
	assert.Equal(t, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, calls[0].method)
	assert.Equal(t, []string{"grandchild-1", "child-1"}, subagents,
		"the worker's list rides back out, deepest first, for the caller to tombstone")
}

// Every other tab type owns no subagents, and neither does a tab whose worker
// is unknown -- a non-empty answer there would tombstone something at random.
func TestDispatchWorkerClose_ReportsNoSubagentsForOtherTabs(t *testing.T) {
	for _, tc := range []struct {
		name     string
		tabType  leapmuxv1.TabType
		workerID string
	}{
		{"terminal", leapmuxv1.TabType_TAB_TYPE_TERMINAL, "w1"},
		{"file", leapmuxv1.TabType_TAB_TYPE_FILE, "w1"},
		{"agent with no worker", leapmuxv1.TabType_TAB_TYPE_AGENT, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []recordedWorkerCall
			subagents, err := dispatchWorkerClose(
				recordingWorkerCaller(&calls),
				tc.workerID, "t-1",
				tc.tabType,
				leapmuxv1.WorktreeAction_WORKTREE_ACTION_KEEP,
			)
			require.NoError(t, err)
			assert.Empty(t, subagents)
		})
	}
}

// The last-tab inspect identifies its tab by the full identity, type and id. The
// worker reads an absent tab as an empty id and an unspecified type, and
// answers that no prompt is needed, so an inspect without the identity would
// skip the worktree prompt for every close.
func TestInspectLastTabCloseBest_SendsTheTabIdentity(t *testing.T) {
	var calls []recordedWorkerCall
	resp, err := inspectLastTabCloseBest(
		replyingWorkerCaller(&calls, &leapmuxv1.InspectLastTabCloseResponse{WorkingDir: "/src/wt"}),
		leapmuxv1.TabType_TAB_TYPE_TERMINAL, "term-1",
	)
	require.NoError(t, err)
	assert.Equal(t, "/src/wt", resp.GetWorkingDir(), "the worker's answer rides back out")

	require.Len(t, calls, 1)
	assert.Equal(t, leapmuxv1connect.WorkerGitServiceInspectLastTabCloseProcedure, calls[0].method)
	req, ok := calls[0].in.(*leapmuxv1.InspectLastTabCloseRequest)
	require.True(t, ok, "unexpected request type %T", calls[0].in)
	assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_TERMINAL, req.GetTab().GetTabType())
	assert.Equal(t, "term-1", req.GetTab().GetTabId())
}

// A failed inspect returns no response, so the caller cannot read a
// half-filled answer as the worker's verdict.
func TestInspectLastTabCloseBest_ReturnsNoResponseWhenTheCallFails(t *testing.T) {
	boom := errors.New("worker unreachable")
	resp, err := inspectLastTabCloseBest(func(_ string, _, out proto.Message) error {
		proto.Merge(out, &leapmuxv1.InspectLastTabCloseResponse{WorkingDir: "/half"})
		return boom
	}, leapmuxv1.TabType_TAB_TYPE_AGENT, "agent-1")
	require.ErrorIs(t, err, boom)
	assert.Nil(t, resp)
}

// A failed agent close reports no subagents. The command tombstones whatever
// this returns, and the ids in a half-filled response describe a close that did
// not happen.
func TestDispatchWorkerClose_AgentReportsNoSubagentsWhenTheCloseFails(t *testing.T) {
	subagents, err := dispatchWorkerClose(
		func(_ string, _, out proto.Message) error {
			proto.Merge(out, &leapmuxv1.CloseTabResponse{DescendantAgentIds: []string{"child-1"}})
			return errors.New("worker unreachable")
		},
		"w1", "root-1",
		leapmuxv1.TabType_TAB_TYPE_AGENT,
		leapmuxv1.WorktreeAction_WORKTREE_ACTION_KEEP,
	)
	require.Error(t, err)
	assert.Empty(t, subagents)
}

// A FILE tab's teardown is an RPC, not a no-op. `worker_tab_payloads` and the
// worktree_tabs link are worker-side rows that the CRDT tombstone does not
// touch, and CloseTab is what drives the shared closeTabCommon flow
// -- so a `--worktree=discard` on a file tab has to reach the worker or the
// worktree the user asked to remove simply stays.
func TestDispatchWorkerClose_FileTabRevokesPathWithAction(t *testing.T) {
	var calls []recordedWorkerCall
	_, err := dispatchWorkerClose(
		recordingWorkerCaller(&calls),
		"w1", "file-1",
		leapmuxv1.TabType_TAB_TYPE_FILE,
		leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE,
	)
	require.NoError(t, err)
	require.Len(t, calls, 1, "a file tab close must dispatch worker-side teardown")
	assert.Equal(t, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, calls[0].method)
	req, ok := calls[0].in.(*leapmuxv1.CloseTabRequest)
	require.True(t, ok, "unexpected request type %T", calls[0].in)
	assert.Equal(t, "file-1", req.GetTab().GetTabId())
	assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_FILE, req.GetTab().GetTabType())
	assert.Equal(t, leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE, req.GetWorktreeAction(),
		"the user's --worktree choice must ride along, or discard silently keeps the worktree")
}

// Every type goes through the one CloseTab RPC with its own type, and a tab
// whose worker is unknown dispatches nothing at all -- there is nowhere to send
// it, and the CRDT tombstone still removed the tab.
func TestDispatchWorkerClose_SendsEveryTabTypeThroughCloseTab(t *testing.T) {
	for _, tc := range []struct {
		name     string
		tabType  leapmuxv1.TabType
		workerID string
		method   string
	}{
		{"agent", leapmuxv1.TabType_TAB_TYPE_AGENT, "w1", leapmuxv1connect.WorkerTabServiceCloseTabProcedure},
		{"terminal", leapmuxv1.TabType_TAB_TYPE_TERMINAL, "w1", leapmuxv1connect.WorkerTabServiceCloseTabProcedure},
		{"file", leapmuxv1.TabType_TAB_TYPE_FILE, "w1", leapmuxv1connect.WorkerTabServiceCloseTabProcedure},
		{"image", leapmuxv1.TabType_TAB_TYPE_IMAGE, "w1", leapmuxv1connect.WorkerTabServiceCloseTabProcedure},
		{"no worker", leapmuxv1.TabType_TAB_TYPE_FILE, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []recordedWorkerCall
			_, err := dispatchWorkerClose(
				recordingWorkerCaller(&calls),
				tc.workerID, "t-1",
				tc.tabType,
				leapmuxv1.WorktreeAction_WORKTREE_ACTION_KEEP,
			)
			require.NoError(t, err)
			if tc.method == "" {
				assert.Empty(t, calls)
				return
			}
			require.Len(t, calls, 1)
			assert.Equal(t, tc.method, calls[0].method)
			req, ok := calls[0].in.(*leapmuxv1.CloseTabRequest)
			require.True(t, ok, "unexpected request type %T", calls[0].in)
			assert.Equal(t, tc.tabType, req.GetTab().GetTabType())
			assert.Equal(t, "t-1", req.GetTab().GetTabId())
		})
	}
}

// stateWithLiveTabs builds a snapshot holding one untombstoned AGENT tab per id.
func stateWithLiveTabs(ids ...string) *leapmuxv1.UserMaterialized {
	tabs := make(map[string]*leapmuxv1.TabRecord, len(ids))
	for _, id := range ids {
		tabs[id] = &leapmuxv1.TabRecord{TabId: id, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}
	}
	return &leapmuxv1.UserMaterialized{Tabs: tabs}
}

// The tabs a `tab close` retires under an agent are the worker's answer, turned
// into one TombstoneTab op each.
//
// Every one is an AGENT tab: a subagent IS an agent, so its agent id is its tab
// id. Taking the type from the tab being closed instead would tombstone a
// terminal or file tab that happens to share the id.
func TestSubagentTombstoneOps_TombstonesEachIdAsAnAgentTab(t *testing.T) {
	bs := testBootstrap(stateWithLiveTabs("grandchild-1", "child-1"))

	ops := subagentTombstoneOps(bs, []string{"grandchild-1", "child-1"})

	require.Len(t, ops, 2)
	cases := make([]string, 0, len(ops))
	for _, op := range ops {
		cases = append(cases, opCase(op))
		body, ok := op.GetBody().(*leapmuxv1.CrdtOp_TombstoneTab)
		require.True(t, ok, "unexpected op body %T", op.GetBody())
		assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_AGENT, body.TombstoneTab.GetTab().GetTabType())
	}
	assert.Equal(t, []string{"tombstoneTab:grandchild-1", "tombstoneTab:child-1"}, cases,
		"the worker's order rides through unchanged")
}

// The worker answers from the `agents` table, where a row exists for every
// subagent the provider ever spawned -- `EnsureChildAgent` creates one on first
// sight of a spawn, whether or not anyone opened that transcript as a tab.
//
// The hub resolves a tombstone's workspace THROUGH the tab record, so an id it
// has no record for is rejected as UNKNOWN_WORKSPACE, and that rejection is
// fatal for the WHOLE batch. Passing the list through unfiltered would
// therefore not merely waste an op: one never-opened subagent would take every
// real tombstone down with it, and `tab close` would leave every subagent tab
// open while reporting a failure.
func TestSubagentTombstoneOps_SkipsIdsThisAccountHasNoTabFor(t *testing.T) {
	bs := testBootstrap(stateWithLiveTabs("child-1"))

	ops := subagentTombstoneOps(bs, []string{"never-opened", "child-1"})

	require.Len(t, ops, 1, "the id with no tab record is dropped, not batched")
	assert.Equal(t, "tombstoneTab:child-1", opCase(ops[0]))
}

// An already-tombstoned tab fails the hub's workspace resolution the same way:
// `applyTombstoneTab` strips the tile id, so the record no longer resolves to a
// workspace. The browser's optimistic sweep retires these moments earlier, so
// this is the common case, not an edge one.
func TestSubagentTombstoneOps_SkipsAnAlreadyTombstonedTab(t *testing.T) {
	state := stateWithLiveTabs("child-1", "child-2")
	state.Tabs["child-2"].TombstoneAt = &leapmuxv1.HLC{Physical: 1, ClientId: "peer"}
	bs := testBootstrap(state)

	ops := subagentTombstoneOps(bs, []string{"child-1", "child-2"})

	require.Len(t, ops, 1)
	assert.Equal(t, "tombstoneTab:child-1", opCase(ops[0]))
}

// The tab a `tab close` retires goes LAST, behind its own subagents, in one
// batch.
//
// Order and batching are both load-bearing. A parent tombstoned first -- which
// is all a client could do if it learned the ids only from the CloseTab
// response -- leaves every peer promoting the orphaned children to top-level
// rows for the length of the worker teardown. Two batches would reopen the same
// window, narrower.
func TestCloseBatchOps_RetiresTheSubagentsBeforeTheTabItself(t *testing.T) {
	bs := testBootstrap(stateWithLiveTabs("root-1", "child-1", "grandchild-1"))

	ops := closeBatchOps(bs, leapmuxv1.TabType_TAB_TYPE_AGENT, "root-1", []string{"grandchild-1", "child-1"})

	cases := make([]string, 0, len(ops))
	for _, op := range ops {
		cases = append(cases, opCase(op))
	}
	assert.Equal(t, []string{
		"tombstoneTab:grandchild-1",
		"tombstoneTab:child-1",
		"tombstoneTab:root-1",
	}, cases, "the tab the user closed is the last op in the batch")
}

// A tab with no subagents still closes -- the batch is just the one op. This is
// every terminal and file close, and every agent that spawned nothing.
func TestCloseBatchOps_IsJustTheTabWhenItOwnsNoSubagents(t *testing.T) {
	bs := testBootstrap(stateWithLiveTabs("t-1"))

	ops := closeBatchOps(bs, leapmuxv1.TabType_TAB_TYPE_TERMINAL, "t-1", nil)

	require.Len(t, ops, 1)
	assert.Equal(t, "tombstoneTab:t-1", opCase(ops[0]))
	body, ok := ops[0].GetBody().(*leapmuxv1.CrdtOp_TombstoneTab)
	require.True(t, ok)
	assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_TERMINAL, body.TombstoneTab.GetTab().GetTabType(),
		"the tab keeps its own type; only the subagents are forced to AGENT")
}

// A never-opened subagent must not take the parent's tombstone with it. The
// hub cannot resolve a workspace for an id it has no record for and rejects the
// WHOLE batch, so an unfiltered list would leave the tab the user closed open.
func TestCloseBatchOps_DropsAnUnopenedSubagentWithoutLosingTheTab(t *testing.T) {
	bs := testBootstrap(stateWithLiveTabs("root-1", "child-1"))

	ops := closeBatchOps(bs, leapmuxv1.TabType_TAB_TYPE_AGENT, "root-1", []string{"never-opened", "child-1"})

	cases := make([]string, 0, len(ops))
	for _, op := range ops {
		cases = append(cases, opCase(op))
	}
	assert.Equal(t, []string{"tombstoneTab:child-1", "tombstoneTab:root-1"}, cases)
}

// The second batch carries only what the first did not.
//
// A close learns the subtree twice: from the inspect, before the parent's
// tombstone, and from CloseTab, after the teardown. The second answer is a
// superset. Re-tombstoning an id the first batch retired would fail the WHOLE
// second batch, because the hub cannot resolve a workspace for a record whose
// tile id the first tombstone stripped.
func TestExceptAlreadySubmitted_KeepsOnlyWhatTheFirstBatchMissed(t *testing.T) {
	// `late` is a child the provider spawned while it drained, which the inspect
	// (taken before any teardown) could not have seen.
	assert.Equal(t,
		[]string{"late"},
		exceptAlreadySubmitted([]string{"child-1", "child-2", "late"}, []string{"child-1", "child-2"}))
}

// Nothing submitted yet means everything is still to submit -- the
// worker-unreachable path, where the inspect reported no ids at all.
func TestExceptAlreadySubmitted_KeepsEverythingWhenNothingWentFirst(t *testing.T) {
	assert.Equal(t, []string{"child-1"}, exceptAlreadySubmitted([]string{"child-1"}, nil))
}

// No subagents means no batch at all -- submitting an empty one would spend a
// round-trip to say nothing, and the command's success does not depend on it.
func TestSubagentTombstoneOps_BuildsNothingForAnEmptyList(t *testing.T) {
	bs := testBootstrap(stateWithLiveTabs())
	assert.Empty(t, subagentTombstoneOps(bs, nil))
	assert.Empty(t, subagentTombstoneOps(bs, []string{}))
}

// TestTerminalOpenRequest_CarriesTheTitle pins the field the CLI dropped.
//
// `OpenTerminalRequest` gained a `title`, and `--title` was registered for
// `--type=agent` alone, so `leapmux control tab open --type=terminal --title X`
// accepted the flag and silently discarded it: the tab came up with a pooled
// name and the user's title was gone with no report.
func TestTerminalOpenRequest_CarriesTheTitle(t *testing.T) {
	req := terminalOpenRequest(openTerminalArgs{
		WorkerID:      "w-1",
		WorkingDir:    "/src/app",
		Shell:         "/bin/zsh",
		ShellStartDir: "/src/app/sub",
		Title:         "Deploy log",
	}, &leapmuxv1.TabCreation{TabId: "terminal-creation"})

	assert.Equal(t, "Deploy log", req.GetTitle())
	assert.Equal(t, "/src/app", req.GetWorkingDir())
	// The CLI opens a terminal in the directory as it is.
	assert.Nil(t, req.GetGitSetup())
	assert.Equal(t, "/bin/zsh", req.GetShell())
	assert.Equal(t, "/src/app/sub", req.GetShellStartDir())
	// Zero so the worker applies its own 80x25 default; the frontend resizes
	// the PTY as soon as the user attaches.
	assert.Zero(t, req.GetCols())
	assert.Zero(t, req.GetRows())
}

// An omitted --title stays empty on the wire, which is what the field's own
// contract calls "you pick one": the worker then takes a pooled
// `Terminal <Name>`, the same answer the quick-open buttons get.
func TestTerminalOpenRequest_EmptyTitleMeansTheWorkerPicks(t *testing.T) {
	req := terminalOpenRequest(openTerminalArgs{WorkerID: "w-1", WorkingDir: "/src"}, &leapmuxv1.TabCreation{TabId: "terminal-creation"})
	assert.Empty(t, req.GetTitle())
}

// rollbackDispatcher stands in for the worker during a spawn that unwinds.
// It opens a terminal as term-9 and an agent as agent-9, accepts CloseTab,
// and records each method with the CloseTab request it decoded.
type rollbackDispatcher struct {
	// refuseClose makes CloseTab fail after it records the request.
	refuseClose bool
	// holdOpenAgent, when non-nil, delays the answer to OpenAgent until the
	// channel closes, for a test that needs the bootstrap to go first.
	holdOpenAgent <-chan struct{}
	// refuseOpenAgent makes OpenAgent fail in-band with NotFound.
	refuseOpenAgent bool

	mu      sync.Mutex
	methods []string
	closes  []*leapmuxv1.CloseTabRequest
}

func (d *rollbackDispatcher) DispatchWith(ctx context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter) {
	d.mu.Lock()
	d.methods = append(d.methods, req.GetMethod())
	d.mu.Unlock()
	var reply proto.Message
	switch req.GetMethod() {
	case leapmuxv1connect.WorkerTerminalServiceOpenTerminalProcedure:
		reply = &leapmuxv1.OpenTerminalResponse{TerminalId: "term-9"}
	case leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure:
		if d.holdOpenAgent != nil {
			select {
			case <-d.holdOpenAgent:
			case <-ctx.Done():
			}
		}
		if d.refuseOpenAgent {
			_ = w.SendError(int32(codes.NotFound), "no such working directory")
			return
		}
		reply = &leapmuxv1.OpenAgentResponse{Agent: &leapmuxv1.AgentInfo{Id: "agent-9"}}
	case leapmuxv1connect.WorkerTabServiceCloseTabProcedure:
		var in leapmuxv1.CloseTabRequest
		if err := proto.Unmarshal(req.GetPayload(), &in); err != nil {
			_ = w.SendError(int32(codes.InvalidArgument), err.Error())
			return
		}
		d.mu.Lock()
		d.closes = append(d.closes, &in)
		d.mu.Unlock()
		if d.refuseClose {
			_ = w.SendError(int32(codes.Internal), "the worker could not stop the process")
			return
		}
		reply = &leapmuxv1.CloseTabResponse{}
	default:
		_ = w.SendError(int32(codes.Unimplemented), "unexpected method: "+req.GetMethod())
		return
	}
	payload, err := proto.Marshal(reply)
	if err != nil {
		_ = w.SendError(int32(codes.Internal), err.Error())
		return
	}
	_ = w.SendResponse(channelwire.NewPayloadResponse(payload))
}

func (d *rollbackDispatcher) recorded() ([]string, []*leapmuxv1.CloseTabRequest) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.methods...), append([]*leapmuxv1.CloseTabRequest(nil), d.closes...)
}

// tabOpenTransports lists the two transports that carry the worker calls of
// `tab open`. Each start function starts the hub and the worker, and returns the
// --hub value that selects the transport.
var tabOpenTransports = []struct {
	name  string
	start func(t *testing.T, hub *recordingHub, disp controlipc.LocalDispatcher) string
}{
	{"local IPC", func(t *testing.T, hub *recordingHub, disp controlipc.LocalDispatcher) string {
		startSpawnIPC(t, hub, disp)
		return ""
	}},
	{"hub channel", func(t *testing.T, hub *recordingHub, disp controlipc.LocalDispatcher) string {
		return startHubChannelHarness(t, hub, disp).url
	}},
}

func TestRunTabClose_NestedDiscardRefusesBeforeTombstone(t *testing.T) {
	for _, discard := range []bool{false, true} {
		t.Run(fmt.Sprint(discard), func(t *testing.T) {
			disp := &closeDispatcher{inspect: &leapmuxv1.InspectLastTabCloseResponse{}}
			hub := &recordingHub{
				workers:      workerRows("worker-A"),
				materialized: newStateBuilder().workspace("ws-1", "root-1").leafNode("root-1", "", "m").tab("agent-2", "root-1", leapmuxv1.TabType_TAB_TYPE_AGENT).st,
			}
			startSpawnIPC(t, hub, disp)
			args := []string{"tab", "close", "agent-2"}
			if discard {
				args = append(args, "--worktree", "discard")
			}
			_ = runControl(t, args...)
			if discard {
				assert.NotContains(t, hub.called(), leapmuxv1connect.UserCRDTSubmitOpsProcedure)
				assert.Equal(t, []string{leapmuxv1connect.WorkerGitServiceInspectLastTabCloseProcedure}, disp.called())
			} else {
				assert.Contains(t, hub.called(), leapmuxv1connect.UserCRDTSubmitOpsProcedure)
			}
		})
	}
}

// tabGetOutcome runs `tab get` with args against hub and returns the one
// envelope it printed.
func tabGetOutcome(t *testing.T, args ...string) (data, errEnvelope map[string]any) {
	t.Helper()
	o := runControl(t, append([]string{"tab", "get"}, args...)...)
	require.Len(t, o.lines(), 1, "exactly one envelope: %s", o.Stdout)
	env := o.envelope(t)
	return env.Data, env.Error
}

// countOf counts the occurrences of method in called.
func countOf(called []string, method string) int {
	n := 0
	for _, m := range called {
		if m == method {
			n++
		}
	}
	return n
}

// An ambient tab id that the hub cannot place (a quake terminal has no CRDT
// tab) is tolerated by the resolver, which then hands back no row. `tab get`
// has nothing to print and answers not_found.
func TestRunTabGet_AnswersNotFoundForATabTheHubCannotPlace(t *testing.T) {
	hub := &recordingHub{
		materialized:   newStateBuilder().workspace("ws-1", "root-1").leafNode("root-1", "", "m").st,
		unlocatableTab: true,
	}
	startSpawnIPC(t, hub, nil)
	t.Setenv("LEAPMUX_CONTROL_TAB_ID", "quake-1")

	data, errEnvelope := tabGetOutcome(t)

	require.NotNil(t, errEnvelope)
	assert.Equal(t, "not_found", errEnvelope["code"])
	assert.Equal(t, "the hub has no tab quake-1", errEnvelope["message"])
	assert.Nil(t, data)
	assert.Equal(t, 1, countOf(hub.called(), leapmuxv1connect.WorkspaceServiceLocateTabProcedure))
}

// `tab get` prints the row the resolver located, and makes ONE LocateTab call.
// It used to call LocateTab again after the resolver, which cost a second hub
// round trip, and refused a tab that moved between the two calls as not_found
// although the caller gave no workspace that it could be outside of. The stub
// answers any second lookup with the moved row, so a second call would show.
func TestRunTabGet_PrintsTheResolversRowFromOneLookup(t *testing.T) {
	hub := &recordingHub{
		materialized: newStateBuilder().workspace("ws-1", "root-1").leafNode("root-1", "", "m").st,
		relocatedTab: &leapmuxv1.WorkspaceTab{
			Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "agent-2"},
			TileId:      "elsewhere",
			WorkerId:    "worker-A",
			WorkspaceId: "ws-other",
		},
	}
	startSpawnIPC(t, hub, nil)

	data, errEnvelope := tabGetOutcome(t, fullTestID("agent-2"))

	require.Nil(t, errEnvelope)
	assert.Equal(t, 1, countOf(hub.called(), leapmuxv1connect.WorkspaceServiceLocateTabProcedure), "tab get must ask the hub once")
	assert.Equal(t, "ws-1", data["workspace_id"], "the printed row is the one the resolver located")
	assert.Equal(t, "root-1", data["tile_id"])
}

// The happy path prints the located row, with the tab identity flattened into
// the JSON fields every `tab` verb prints. The worker that the stub has no
// dispatcher for answers nothing, so the title and the state stay empty.
func TestRunTabGet_PrintsTheLocatedTab(t *testing.T) {
	hub := &recordingHub{
		materialized: newStateBuilder().workspace("ws-1", "root-1").leafNode("root-1", "", "m").st,
	}
	startSpawnIPC(t, hub, nil)

	id := fullTestID("agent-2")
	data, errEnvelope := tabGetOutcome(t, id)

	require.Nil(t, errEnvelope)
	assert.Equal(t, "agent-2", data["id"], "the row is the one that LocateTab answered")
	assert.Equal(t, "agent", data["type"])
	assert.Equal(t, "ws-1", data["workspace_id"])
	assert.Equal(t, "worker-A", data["worker_id"])
	assert.Equal(t, []string{leapmuxv1connect.WorkspaceServiceLocateTabProcedure}, hub.called(),
		"a full id costs one lookup and no list call")
}
