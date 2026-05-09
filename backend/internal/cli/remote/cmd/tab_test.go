package cmd

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// TestFilterTabsByType_DropsNonMatchingRows pins the central guarantee
// of `tab list --tab-type X`: only rows whose tab_type matches the
// flag survive. Without this, the user runs `tab list --tab-type agent`
// inside a terminal spawn and sees every tab in the workspace -- the
// bug that motivated the filter in the first place.
func TestFilterTabsByType_DropsNonMatchingRows(t *testing.T) {
	in := []*leapmuxv1.WorkspaceTab{
		{TabId: "a1", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
		{TabId: "t1", TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL},
		{TabId: "a2", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
		{TabId: "f1", TabType: leapmuxv1.TabType_TAB_TYPE_FILE},
	}
	got := filterTabsByType(in, leapmuxv1.TabType_TAB_TYPE_AGENT)
	ids := make([]string, 0, len(got))
	for _, t := range got {
		ids = append(ids, t.GetTabId())
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
		{TabId: "a1", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
		{TabId: "t1", TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL},
	}
	got := filterTabsByType(in, leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED)
	assert.Len(t, got, 2)
	assert.Equal(t, "a1", got[0].GetTabId())
	assert.Equal(t, "t1", got[1].GetTabId())
}

// TestFilterTabsByType_NoMatchesYieldsEmpty pins that a filter with no
// matching rows returns a length-zero slice, not a nil-shaped surprise.
func TestFilterTabsByType_NoMatchesYieldsEmpty(t *testing.T) {
	in := []*leapmuxv1.WorkspaceTab{
		{TabId: "t1", TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL},
	}
	got := filterTabsByType(in, leapmuxv1.TabType_TAB_TYPE_AGENT)
	assert.Empty(t, got)
}

// TestTerminalInfoToMap_ScreenEmittedAsString pins the readability
// contract: the PTY screen buffer ships as a JSON string of the raw
// bytes (ANSI escapes preserved), not as base64. Without this, a user
// running `leapmux remote terminal get` sees an opaque blob that no
// jq pipeline can render as actual terminal output.
func TestTerminalInfoToMap_ScreenEmittedAsString(t *testing.T) {
	in := &leapmuxv1.TerminalInfo{
		TerminalId:      "term-1",
		Cols:            80,
		Rows:            25,
		Screen:          []byte("hello\x1b[31mworld\x1b[m\n"),
		ScreenEndOffset: 17,
		Title:           "demo",
		Status:          leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY,
	}
	got := terminalInfoToMap(in)
	assert.Equal(t, "term-1", got["terminal_id"])
	assert.Equal(t, "hello\x1b[31mworld\x1b[m\n", got["screen"], "screen must be a string, not []byte")
	assert.Equal(t, int64(17), got["screen_end_offset"])
	assert.Equal(t, "demo", got["title"])

	encoded, err := json.Marshal(got)
	require.NoError(t, err)
	// encoding/json escapes the ESC byte (0x1b) as the six-character
	// sequence backslash-u-0-0-1-b; the "hello" / "world" tokens stay
	// ASCII. The base64 form (aGVsbG8b...) must not appear in the
	// payload -- that would mean a regression back to the default
	// []byte JSON encoding.
	assert.Contains(t, string(encoded), `"screen":"hello\u001b[31mworld\u001b[m\n"`)
	assert.NotContains(t, string(encoded), "aGVsbG8")
}

// TestRunTerminalSend_RequiresDataOrStdin pins the early-validation
// path on `terminal send`: missing --data without --stdin must surface
// invalid_request rather than silently shipping a zero-byte write to
// the PTY. Mirrors TestRunAgentSend_RequiresMessageOrStdin.
func TestRunTerminalSend_RequiresDataOrStdin(t *testing.T) {
	t.Setenv("LEAPMUX_HUB", "")
	t.Setenv("LEAPMUX_REMOTE_TAB_ID", "")
	t.Setenv("LEAPMUX_REMOTE_TAB_TYPE", "")
	out := withCapturedStdout(t, func() {
		err := RunTerminalSend(fakeCmdCtx{}, []string{"--hub", "https://stub", "--tab-id", "term-1"})
		require.Error(t, err)
	})
	var env struct {
		Error map[string]string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out, &env))
	assert.Equal(t, "invalid_request", env.Error["code"])
	assert.Contains(t, env.Error["message"], "--data")
	assert.Contains(t, env.Error["message"], "stdin")
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
func runResolvePositionSpec(t *testing.T, state *leapmuxv1.OrgMaterialized, destTileID, movingTabID string, spec positionSpec) (tileID, position, code, message string) {
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
	assert.Equal(t, "invalid_request", code)
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
	assert.Equal(t, "invalid_request", code)
	assert.Contains(t, msg, "tab being moved")
}

// TestResolvePositionSpec_LastWithoutDestTileErrors covers the
// caller-side contract: --first / --last need a destination tile to
// scan for siblings, and we error rather than guess.
func TestResolvePositionSpec_LastWithoutDestTileErrors(t *testing.T) {
	state := newStateBuilder().st
	_, _, code, _ := runResolvePositionSpec(t, state, "", "", positionSpec{kind: positionLast})
	assert.Equal(t, "invalid_request", code)
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
// CloseAgent/CloseTerminal WorktreeAction enum. push collapses to KEEP
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
	msg := lastTabPromptMessage(&leapmuxv1.InspectLastTabCloseResponse{
		Target:                leapmuxv1.LastTabCloseTarget_LAST_TAB_CLOSE_TARGET_WORKTREE,
		WorktreePath:          "/repo/wt-foo",
		HasUncommittedChanges: true,
		DiffAdded:             3,
		DiffDeleted:           1,
		DiffUntracked:         2,
		UnpushedCommitCount:   2,
	})
	assert.Contains(t, msg, "/repo/wt-foo")
	assert.Contains(t, msg, "3 added / 1 deleted / 2 untracked")
	assert.Contains(t, msg, "2 unpushed commits")
	assert.Contains(t, msg, "--worktree=keep|push|discard")
}

// TestLastTabPromptMessage_BranchUnpushedSingular pluralizes commit
// count correctly. Mirrors the frontend dialog's `pluralize` call.
func TestLastTabPromptMessage_BranchUnpushedSingular(t *testing.T) {
	msg := lastTabPromptMessage(&leapmuxv1.InspectLastTabCloseResponse{
		Target:              leapmuxv1.LastTabCloseTarget_LAST_TAB_CLOSE_TARGET_BRANCH,
		BranchName:          "feat-x",
		UnpushedCommitCount: 1,
	})
	assert.Contains(t, msg, "feat-x")
	assert.Contains(t, msg, "1 unpushed commit")
	assert.NotContains(t, msg, "1 unpushed commits")
}

// TestLastTabPromptMessage_CleanWorktree pins the clean-worktree
// message: no diff / unpushed details, but the user still has to
// pass --worktree because the dialog (and now the CLI) forces an
// explicit keep / discard choice when target=WORKTREE.
func TestLastTabPromptMessage_CleanWorktree(t *testing.T) {
	msg := lastTabPromptMessage(&leapmuxv1.InspectLastTabCloseResponse{
		Target:       leapmuxv1.LastTabCloseTarget_LAST_TAB_CLOSE_TARGET_WORKTREE,
		WorktreePath: "/repo/wt-clean",
	})
	assert.Contains(t, msg, "/repo/wt-clean")
	assert.Contains(t, msg, "--worktree=keep|push|discard")
	assert.NotContains(t, msg, "added")
	assert.NotContains(t, msg, "unpushed")
}
