package cmd

import (
	"context"
	"sort"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// TestParseWithTabsPolicy pins the flag-parser table: "" decodes to
// the sentinel (handler decides whether the operation legitimately
// needs the flag); "close" / "move" decode to the matching policy;
// anything else is an invalid_request envelope.
func TestParseWithTabsPolicy(t *testing.T) {
	cases := []struct {
		in   string
		want withTabsPolicy
		ok   bool
	}{
		{"", withTabsUnspecified, true},
		{"close", withTabsClose, true},
		{"move", withTabsMove, true},
	}
	for _, c := range cases {
		got, err := parseWithTabsPolicy(c.in)
		assert.NoError(t, err, "parseWithTabsPolicy(%q)", c.in)
		assert.Equal(t, c.want, got, "parseWithTabsPolicy(%q)", c.in)
	}

	// Unknown values surface as an emitted envelope. captureEmit
	// drains stdout and recovers code + message.
	code, msg := captureEmit(t, func() error {
		_, err := parseWithTabsPolicy("delete")
		return err
	})
	assert.Equal(t, "usage", code)
	assert.Contains(t, msg, "close")
	assert.Contains(t, msg, "move")
}

// TestKindLabel covers the user-facing strings used in error
// messages. UNSPECIFIED and a kind this build does not know print as
// "unknown", so a message never loses the kind to an empty string.
func TestKindLabel(t *testing.T) {
	assert.Equal(t, "leaf", kindLabel(leapmuxv1.NodeKind_NODE_KIND_LEAF))
	assert.Equal(t, "split", kindLabel(leapmuxv1.NodeKind_NODE_KIND_SPLIT))
	assert.Equal(t, "grid", kindLabel(leapmuxv1.NodeKind_NODE_KIND_GRID))
	assert.Equal(t, "unknown", kindLabel(leapmuxv1.NodeKind_NODE_KIND_UNSPECIFIED))
	assert.Equal(t, "unknown", kindLabel(leapmuxv1.NodeKind(99)))
}

// TestFindHeirTileID_TwoLeafSplit covers the canonical case: a SPLIT
// root with two leaf children. Closing either child should land tabs
// on the other.
func TestFindHeirTileID_TwoLeafSplit(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "T").
		splitNode("T", "", "").
		leafNode("A", "T", "a").
		leafNode("B", "T", "b").
		st

	assert.Equal(t, "B", crdt.FindHeirTileID(state, "A", "T"), "left leaf's heir is the right leaf")
	assert.Equal(t, "A", crdt.FindHeirTileID(state, "B", "T"), "right leaf's heir prefers the left neighbour")
}

// TestFindHeirTileID_NestedSplit walks up past a parent that doesn't
// have an adjacent sibling, then descends the first leaf of the
// next-level sibling. Mirrors the frontend's algorithm for
// "innermost ancestor with a sibling subtree".
func TestFindHeirTileID_NestedSplit(t *testing.T) {
	// Root SPLIT R contains:
	//   inner SPLIT S (position "a") with leaves SA, SB.
	//   leaf C (position "b").
	// Closing SA should heir to SB (its same-parent neighbour).
	// Closing C should heir to the first leaf of S, which is SA.
	state := newStateBuilder().
		workspace("ws-1", "R").
		splitNode("R", "", "").
		splitNode("S", "R", "a").
		leafNode("SA", "S", "a").
		leafNode("SB", "S", "b").
		leafNode("C", "R", "b").
		st

	assert.Equal(t, "SB", crdt.FindHeirTileID(state, "SA", "R"))
	assert.Equal(t, "SA", crdt.FindHeirTileID(state, "C", "R"), "C's heir descends into S's first leaf")
}

// TestFindHeirTileID_SingleLeaf returns "" when there's no sibling
// subtree anywhere on the path. The caller treats "" as "reject
// --with-tabs=move" rather than silently dropping the tabs.
func TestFindHeirTileID_SingleLeaf(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "root").
		leafNode("root", "", "").
		st

	assert.Equal(t, "", crdt.FindHeirTileID(state, "root", "root"))
}

// TestFindHeirTileID_SkipsTombstoned ensures heir-finding ignores
// already-tombstoned siblings — otherwise the move would target a
// dead tile and the validator would reject the whole batch.
func TestFindHeirTileID_SkipsTombstoned(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "T").
		splitNode("T", "", "").
		leafNode("A", "T", "a").
		tombstonedNode("B", "T"). // dead sibling
		leafNode("C", "T", "c").
		st

	assert.Equal(t, "C", crdt.FindHeirTileID(state, "A", "T"), "tombstoned B is skipped; live C wins")
}

// TestBuildCloseSubtreeOps_Tombstone covers the cascade-close path
// with `--with-tabs=close` semantics: every tab in the subtree is
// tombstoned and every node (including the root) gets a
// tombstoneNode op. Order is leaves-first so the validator never
// sees a parent dead before its child.
func TestBuildCloseSubtreeOps_Tombstone(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "T").
		splitNode("T", "", "").
		leafNode("A", "T", "a").
		leafNode("B", "T", "b").
		tab("tab-1", "A", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
		tab("tab-2", "B", leapmuxv1.TabType_TAB_TYPE_AGENT).
		st

	bs := testBootstrap(state)
	ops := buildCloseSubtreeOps(bs, "T", "", false)
	cases := opCases(ops)
	sort.Strings(cases) // order-independent assertion

	assert.Contains(t, cases, "tombstoneTab:tab-1")
	assert.Contains(t, cases, "tombstoneTab:tab-2")
	assert.Contains(t, cases, "tombstoneNode:A")
	assert.Contains(t, cases, "tombstoneNode:B")
	assert.Contains(t, cases, "tombstoneNode:T")
	// No migration ops in the tombstone path.
	for _, c := range cases {
		assert.NotContains(t, c, "setTabTileId", "tombstone path must not migrate")
		assert.NotContains(t, c, "setTabPosition", "tombstone path must not re-stamp positions")
	}
}

// TestBuildCloseSubtreeOps_Migrate is the cascade-close path with
// `--with-tabs=move`: every tab is re-pointed at the heir and given
// a fresh LexoRank position, every node is still tombstoned.
func TestBuildCloseSubtreeOps_Migrate(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "R").
		splitNode("R", "", "").
		splitNode("T", "R", "a").
		leafNode("A", "T", "a").
		leafNode("B", "T", "b").
		leafNode("heir", "R", "b").
		tab("tab-1", "A", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
		tab("tab-2", "B", leapmuxv1.TabType_TAB_TYPE_AGENT).
		st

	bs := testBootstrap(state)
	ops := buildCloseSubtreeOps(bs, "T", "heir", false)
	cases := opCases(ops)

	assert.Contains(t, cases, "setTabTileId:tab-1=heir")
	assert.Contains(t, cases, "setTabTileId:tab-2=heir")
	assert.Contains(t, cases, "setTabPosition:tab-1")
	assert.Contains(t, cases, "setTabPosition:tab-2")
	assert.Contains(t, cases, "tombstoneNode:A")
	assert.Contains(t, cases, "tombstoneNode:B")
	assert.Contains(t, cases, "tombstoneNode:T")
	// Migration path must not tombstone the tabs.
	for _, c := range cases {
		assert.NotContains(t, c, "tombstoneTab", "migrate path must not tombstone tabs")
	}
}

// TestLiveTabsInSubtree returns every tab in the subtree, skipping
// tabs on tombstoned tiles and tombstoned tabs themselves. Anchors
// the policy decision in tile close --recursive: zero tabs in the
// subtree → --with-tabs is not required; >0 → required.
func TestLiveTabsInSubtree(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "T").
		splitNode("T", "", "").
		leafNode("A", "T", "a").
		leafNode("B", "T", "b").
		tombstonedNode("C", "T").
		tab("live-1", "A", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
		tab("live-2", "B", leapmuxv1.TabType_TAB_TYPE_AGENT).
		tombstonedTab("dead-tab", "A", leapmuxv1.TabType_TAB_TYPE_AGENT).
		tab("orphan", "C", leapmuxv1.TabType_TAB_TYPE_TERMINAL). // tile is tombstoned
		st

	got := crdt.LiveTabsInSubtree(state, "T")
	ids := make([]string, 0, len(got))
	for _, t := range got {
		ids = append(ids, t.TabID)
	}
	sort.Strings(ids)
	assert.Equal(t, []string{"live-1", "live-2"}, ids)
}

func TestRunTileCloseRecursiveProtectsTheCallingTabInADescendant(t *testing.T) {
	for _, override := range []bool{false, true} {
		name := "refuses the close"
		if override {
			name = "allows the explicit override"
		}
		t.Run(name, func(t *testing.T) {
			hub := &recordingHub{
				onSubmitOps: func(context.Context) ([]byte, error) {
					return proto.Marshal(&leapmuxv1.SubmitOpsResponse{Results: []*leapmuxv1.BatchResult{{}}})
				},
				materialized: newStateBuilder().
					workspace("ws-1", "root").
					splitNode("root", "", "m").
					splitNode("inner", "root", "a").
					leafNode("own-leaf", "inner", "a").
					leafNode("other-leaf", "inner", "b").
					leafNode("survivor", "root", "b").
					tabOnWorker("agent-1", "own-leaf", "worker-A", leapmuxv1.TabType_TAB_TYPE_AGENT).
					st,
			}
			startSpawnIPC(t, hub, &deleteGuardDispatcher{})
			t.Setenv("LEAPMUX_CONTROL_TAB_ID", "agent-1")
			args := []string{"tile", "close", "inner", "--recursive", "--with-tabs", "close"}
			if override {
				out := runControl(t, append(args, "--allow-self")...)
				assert.Equal(t, float64(1), out.Data(t)["tabs_closed"])
				assert.Contains(t, hub.called(), leapmuxv1connect.UserCRDTSubmitOpsProcedure)
				return
			}
			out := runControl(t, args...)
			assert.NotContains(t, hub.called(), leapmuxv1connect.UserCRDTSubmitOpsProcedure)
			failure := out.requireFailure(t, cli.CodeSelfTargetRefused)
			assert.Contains(t, failure.Message, "inner")
			assert.Contains(t, failure.Hints, "pass --allow-self to do it anyway")
		})
	}
}

// opCaptureHub wraps a recordingHub and captures every SubmitOpsRequest sent
// to UserCRDT.SubmitOps so tests can inspect the exact ops and replay them
// through crdt.ValidateBatch.
type opCaptureHub struct {
	*recordingHub
	submitMu  sync.Mutex
	submitted [][]*leapmuxv1.CrdtOp
}

func (h *opCaptureHub) CallInner(ctx context.Context, uid userid.UserID, procedure string, payload []byte) ([]byte, error) {
	if procedure == leapmuxv1connect.UserCRDTSubmitOpsProcedure {
		var req leapmuxv1.SubmitOpsRequest
		if err := proto.Unmarshal(payload, &req); err != nil {
			return nil, err
		}
		h.submitMu.Lock()
		for _, batch := range req.GetBatches() {
			h.submitted = append(h.submitted, batch.GetOps())
		}
		h.submitMu.Unlock()
		return proto.Marshal(&leapmuxv1.SubmitOpsResponse{Results: []*leapmuxv1.BatchResult{{}}})
	}
	return h.recordingHub.CallInner(ctx, uid, procedure, payload)
}

func validateOpsAgainstMaterialized(t *testing.T, state *leapmuxv1.UserMaterialized, ops []*leapmuxv1.CrdtOp) *leapmuxv1.UserCrdtState {
	t.Helper()
	cloned := proto.Clone(state).(*leapmuxv1.UserMaterialized)
	for _, tab := range cloned.Tabs {
		if tab.WorkerId == nil {
			tab.WorkerId = &leapmuxv1.LWWString{Value: "worker-A"}
		}
		if tab.Position == nil {
			tab.Position = &leapmuxv1.LWWString{Value: "a"}
		}
	}
	for _, node := range cloned.Nodes {
		if node.GetKind().GetValue() == leapmuxv1.NodeKind_NODE_KIND_SPLIT {
			if node.Direction == nil {
				node.Direction = &leapmuxv1.LWWDirection{Value: leapmuxv1.SplitDirection_SPLIT_DIRECTION_VERTICAL}
			}
			if node.Ratios == nil {
				node.Ratios = &leapmuxv1.LWWDoubles{Value: &leapmuxv1.DoubleList{Values: []float64{0.5, 0.5}}}
			}
		}
	}
	for _, fw := range cloned.FloatingWindows {
		if fw.X == nil {
			fw.X = &leapmuxv1.LWWDouble{Value: 100}
		}
		if fw.Y == nil {
			fw.Y = &leapmuxv1.LWWDouble{Value: 100}
		}
		if fw.Width == nil {
			fw.Width = &leapmuxv1.LWWDouble{Value: 640}
		}
		if fw.Height == nil {
			fw.Height = &leapmuxv1.LWWDouble{Value: 420}
		}
		if fw.Opacity == nil {
			fw.Opacity = &leapmuxv1.LWWDouble{Value: 1}
		}
	}
	appliedOps := make([]*leapmuxv1.CrdtOp, len(ops))
	for i, op := range ops {
		cp := proto.Clone(op).(*leapmuxv1.CrdtOp)
		cp.CanonicalHlc = cp.ClientHlc
		appliedOps[i] = cp
	}
	full := &leapmuxv1.UserCrdtState{
		UserId:          cloned.UserId,
		Nodes:           cloned.Nodes,
		Tabs:            cloned.Tabs,
		FloatingWindows: cloned.FloatingWindows,
		Workspaces:      cloned.Workspaces,
	}
	res, next := crdt.ValidateBatch(t.Context(), full, appliedOps, true, "", nil)
	require.NoError(t, res.Err)
	require.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNSPECIFIED, res.Reason, "CRDT validator rejected batch (offending op=%s)", res.OffendingOpID)
	return next
}

// SCAN-1 + SWEEP-3: closing a leaf in a 2-leaf split with --with-tabs=move
// must migrate both the sibling's tabs and the closed tile's tabs onto the
// surviving collapsed ancestor (not the tombstoned sibling), without
// tombstoning any tab and with strictly increasing LexoRank positions.
func TestRunTileClose_MoveInTwoLeafSplitLandsOnCollapsedAncestor(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "root").
		splitNode("root", "", "").
		leafNode("leaf-a", "root", "a").
		leafNode("leaf-b", "root", "b").
		tabAtPos("tab-a1", "leaf-a", "n", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
		tabAtPos("tab-b1", "leaf-b", "n", leapmuxv1.TabType_TAB_TYPE_AGENT).
		st
	hub := &opCaptureHub{recordingHub: &recordingHub{materialized: state}}
	startCustomSpawnIPC(t, hub, nil)

	data := runControl(t, "tile", "close", "leaf-a", "--with-tabs", "move").Data(t)
	assert.Equal(t, float64(1), data["tabs_moved"])
	assert.Equal(t, "root", data["heir_tile_id"], "heir_tile_id must report the surviving collapsed ancestor")

	require.Len(t, hub.submitted, 1)
	ops := hub.submitted[0]
	cases := opCases(ops)
	for _, c := range cases {
		assert.NotContains(t, c, "tombstoneTab:", "move path must never tombstone tabs")
	}
	assert.Contains(t, cases, "setTabTileId:tab-b1=root")
	assert.Contains(t, cases, "setTabTileId:tab-a1=root")
	assert.Contains(t, cases, "tombstoneNode:leaf-a")
	assert.Contains(t, cases, "tombstoneNode:leaf-b")
	assert.Contains(t, cases, "setNodeKind:root=NODE_KIND_LEAF")

	next := validateOpsAgainstMaterialized(t, state, ops)
	assert.Equal(t, "root", next.Tabs["tab-a1"].GetTileId().GetValue())
	assert.Equal(t, "root", next.Tabs["tab-b1"].GetTileId().GetValue())
	assert.Less(t, next.Tabs["tab-b1"].GetPosition().GetValue(), next.Tabs["tab-a1"].GetPosition().GetValue(),
		"migrated closing-tile tab must sort strictly after surviving sibling tab")
}

// SCAN-1: closing a leaf inside a split floating window with --with-tabs=move
// resolves its heir against the floating window root rather than the workspace
// root.
func TestRunTileClose_MoveInsideSplitFloatingWindow(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "ws-root").
		leafNode("ws-root", "", "").
		splitNode("fw-root", "", "").
		leafNode("fw-leaf-a", "fw-root", "a").
		leafNode("fw-leaf-b", "fw-root", "b").
		tabAtPos("tab-fa", "fw-leaf-a", "n", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
		st
	state.FloatingWindows = map[string]*leapmuxv1.FloatingWindowRecord{
		"fw-1": {
			WindowId:    "fw-1",
			WorkspaceId: &leapmuxv1.LWWString{Value: "ws-1"},
			RootNodeId:  "fw-root",
		},
	}
	hub := &opCaptureHub{recordingHub: &recordingHub{materialized: state}}
	startCustomSpawnIPC(t, hub, nil)

	data := runControl(t, "tile", "close", "fw-leaf-a", "--with-tabs", "move").Data(t)
	assert.Equal(t, float64(1), data["tabs_moved"])
	assert.Equal(t, "fw-root", data["heir_tile_id"])

	require.Len(t, hub.submitted, 1)
	next := validateOpsAgainstMaterialized(t, state, hub.submitted[0])
	assert.Equal(t, "fw-root", next.Tabs["tab-fa"].GetTileId().GetValue())
	assert.Equal(t, leapmuxv1.NodeKind_NODE_KIND_LEAF, next.Nodes["fw-root"].GetKind().GetValue())
}

// SCAN-19: closing a leaf tile with --with-tabs=close must emit exactly one
// TombstoneTab op per tab, not duplicates.
func TestRunTileClose_WithTabsCloseEmitsSingleTombstonePerTab(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "root").
		splitNode("root", "", "").
		leafNode("leaf-a", "root", "a").
		leafNode("leaf-b", "root", "b").
		tabOnWorker("tab-a1", "leaf-a", "worker-A", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
		st
	hub := &opCaptureHub{recordingHub: &recordingHub{materialized: state}}
	startCustomSpawnIPC(t, hub, &closeDispatcher{})

	data := runControl(t, "tile", "close", "leaf-a", "--with-tabs", "close", "--allow-busy").Data(t)
	assert.Equal(t, float64(1), data["tabs_closed"])
	require.Len(t, hub.submitted, 1)

	tombstoneCount := 0
	for _, c := range opCases(hub.submitted[0]) {
		if c == "tombstoneTab:tab-a1" {
			tombstoneCount++
		}
	}
	assert.Equal(t, 1, tombstoneCount, "each tab must be tombstoned exactly once")
}

// SWEEP-1: tile split must reject non-leaf targets (SPLIT and GRID) with a
// usage error before submitting any CRDT ops.
func TestRunTileSplit_RejectsNonLeafTile(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "root").
		splitNode("root", "", "").
		leafNode("leaf-a", "root", "a").
		gridNode("grid-b", "root", "b").
		leafNode("cell-00", "grid-b", "0,0").
		st
	hub := &opCaptureHub{recordingHub: &recordingHub{materialized: state}}
	startCustomSpawnIPC(t, hub, nil)

	for _, target := range []string{"root", "grid-b"} {
		f := runControl(t, "tile", "split", target).requireFailure(t, cli.CodeUsage)
		assert.Contains(t, f.Message, "only leaf tiles can be split")
	}
	assert.Empty(t, hub.submitted, "rejected splits must submit no CRDT ops")
}

// SWEEP-2: tile close --recursive on a workspace root SPLIT must keep the root
// alive and flip it back to LEAF instead of tombstoning it (which fails with
// BATCH_REJECTION_ROOT_NODE_PROTECTED).
func TestRunTileClose_RecursiveOnRootKeepsRootAndFlipsToLeaf(t *testing.T) {
	for _, mode := range []string{"close", "move"} {
		t.Run("with-tabs="+mode, func(t *testing.T) {
			state := newStateBuilder().
				workspace("ws-1", "root-split").
				splitNode("root-split", "", "").
				leafNode("leaf-a", "root-split", "a").
				leafNode("leaf-b", "root-split", "b").
				tabOnWorker("tab-a1", "leaf-a", "worker-A", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
				st
			hub := &opCaptureHub{recordingHub: &recordingHub{materialized: state}}
			startCustomSpawnIPC(t, hub, &closeDispatcher{})

			args := []string{"tile", "close", "root-split", "--recursive", "--with-tabs", mode}
			if mode == "close" {
				args = append(args, "--allow-busy")
			}
			out := runControl(t, args...).Data(t)
			require.Len(t, hub.submitted, 1)
			cases := opCases(hub.submitted[0])
			assert.NotContains(t, cases, "tombstoneNode:root-split", "root node must not be tombstoned")
			assert.Contains(t, cases, "tombstoneNode:leaf-a")
			assert.Contains(t, cases, "tombstoneNode:leaf-b")
			assert.Contains(t, cases, "setNodeKind:root-split=NODE_KIND_LEAF")

			next := validateOpsAgainstMaterialized(t, state, hub.submitted[0])
			assert.Equal(t, leapmuxv1.NodeKind_NODE_KIND_LEAF, next.Nodes["root-split"].GetKind().GetValue())
			if mode == "move" {
				assert.Equal(t, "root-split", out["heir_tile_id"])
				assert.Equal(t, "root-split", next.Tabs["tab-a1"].GetTileId().GetValue())
			} else {
				assert.False(t, crdt.HLCIsZero(next.Tabs["tab-a1"].GetTombstoneAt()))
			}
		})
	}
}

// SWEEP-3: migrating tabs onto an heir tile that already holds tabs appends
// them strictly after the heir's existing maximum LexoRank position.
func TestMigrateTabs_AppendsAfterExistingHeirTabs(t *testing.T) {
	t.Run("non-collapsing leaf close", func(t *testing.T) {
		state := newStateBuilder().
			workspace("ws-1", "root").
			splitNode("root", "", "").
			leafNode("leaf-a", "root", "a").
			leafNode("leaf-b", "root", "b").
			leafNode("leaf-c", "root", "c").
			tabAtPos("tab-a1", "leaf-a", "a", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
			tabAtPos("tab-b1", "leaf-b", "n", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
			tabAtPos("tab-b2", "leaf-b", "nn", leapmuxv1.TabType_TAB_TYPE_AGENT).
			st
		ops := buildCloseTileOpsWithHeir(testBootstrap(state), "leaf-a", "leaf-b")
		var movedPos string
		for _, op := range ops {
			set := op.GetSetTabRegister()
			if set != nil && set.GetTab().GetTabId() == "tab-a1" {
				if p, ok := set.GetField().(*leapmuxv1.SetTabRegisterOp_Position); ok {
					movedPos = p.Position
				}
			}
		}
		require.NotEmpty(t, movedPos)
		assert.Greater(t, movedPos, "nn", "migrated tab must sort strictly after heir's last tab")
	})

	t.Run("recursive subtree close", func(t *testing.T) {
		state := newStateBuilder().
			workspace("ws-1", "root").
			splitNode("root", "", "").
			splitNode("inner", "root", "a").
			leafNode("inner-a", "inner", "a").
			leafNode("inner-b", "inner", "b").
			leafNode("heir", "root", "b").
			tabAtPos("tab-h1", "heir", "p", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
			tabAtPos("tab-ia", "inner-a", "a", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
			tabAtPos("tab-ib", "inner-b", "a", leapmuxv1.TabType_TAB_TYPE_AGENT).
			st
		ops := buildCloseSubtreeOps(testBootstrap(state), "inner", "heir", false)
		positions := map[string]string{}
		for _, op := range ops {
			set := op.GetSetTabRegister()
			if set == nil {
				continue
			}
			if p, ok := set.GetField().(*leapmuxv1.SetTabRegisterOp_Position); ok {
				positions[set.GetTab().GetTabId()] = p.Position
			}
		}
		require.Len(t, positions, 2)
		assert.Greater(t, positions["tab-ia"], "p")
		assert.Greater(t, positions["tab-ib"], "p")
		assert.NotEqual(t, positions["tab-ia"], positions["tab-ib"])
	})
}

// REMOVALS-6: invalid local tile flags fail with CodeUsage before opening a
// session or contacting any Hub.
func TestTileCommands_ValidateFlagsBeforeOpeningSession(t *testing.T) {
	isolateCLIEnv(t)
	cases := [][]string{
		{"tile", "split", "ab12cd34", "--direction", "diagonal"},
		{"tile", "close", "ab12cd34", "--with-tabs", "bogus"},
		{"tile", "make-grid", "ab12cd34", "--rows", "0", "--cols", "2"},
		{"tile", "remove-grid", "ab12cd34", "--with-tabs", "bogus"},
		{"tile", "set-ratios", "ab12cd34", "--ratios", "abc"},
		{"tile", "set-grid-ratios", "ab12cd34"},
		{"tile", "set-grid-ratios", "ab12cd34", "--row-ratios", "-1,2"},
		{"tile", "resize", "ab12cd34", "--ratio", "1.5"},
	}
	for _, args := range cases {
		t.Run(args[1], func(t *testing.T) {
			runControl(t, args...).requireFailure(t, cli.CodeUsage)
		})
	}
}

// SCAN-2 + RIPPLE-1: hybrid `tile swap` swaps sibling tiles in place with
// reissued_ids=false and clones cross-parent subtrees with fresh IDs
// (reissued_ids=true), migrating tabs while preserving their positions and
// passing real Hub CRDT validation.
func TestRunTileSwap_HybridSiblingAndCrossParent(t *testing.T) {
	t.Run("sibling swap keeps IDs and emits two position ops", func(t *testing.T) {
		state := newStateBuilder().
			workspace("ws-1", "root").
			splitNode("root", "", "").
			leafNode("leaf-a", "root", "a").
			leafNode("leaf-b", "root", "b").
			tabAtPos("tab-a", "leaf-a", "m", leapmuxv1.TabType_TAB_TYPE_AGENT).
			tabAtPos("tab-b", "leaf-b", "n", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
			st
		hub := &opCaptureHub{recordingHub: &recordingHub{materialized: state}}
		startCustomSpawnIPC(t, hub, nil)

		data := runControl(t, "tile", "swap", "leaf-a", "leaf-b").Data(t)
		assert.Equal(t, "leaf-a", data["tile_a"])
		assert.Equal(t, "leaf-b", data["tile_b"])
		assert.Equal(t, "ws-1", data["workspace_id"])
		assert.Equal(t, false, data["reissued_ids"])

		require.Len(t, hub.submitted, 1)
		assert.Len(t, hub.submitted[0], 2, "sibling swap should emit only two SetNodePosition ops")
		next := validateOpsAgainstMaterialized(t, state, hub.submitted[0])
		assert.Equal(t, "b", next.Nodes["leaf-a"].GetPosition().GetValue())
		assert.Equal(t, "a", next.Nodes["leaf-b"].GetPosition().GetValue())
		assert.Equal(t, "leaf-a", next.Tabs["tab-a"].GetTileId().GetValue())
		assert.Equal(t, "leaf-b", next.Tabs["tab-b"].GetTileId().GetValue())
	})

	t.Run("cross-parent leaf-to-leaf across nested splits clones and migrates tabs", func(t *testing.T) {
		state := newStateBuilder().
			workspace("ws-1", "root").
			splitNode("root", "", "").
			splitNode("left-split", "root", "a").
			leafNode("left-top", "left-split", "a").
			leafNode("left-bot", "left-split", "b").
			splitNode("right-split", "root", "b").
			leafNode("right-top", "right-split", "a").
			leafNode("right-bot", "right-split", "b").
			tabAtPos("tab-lt-1", "left-top", "h", leapmuxv1.TabType_TAB_TYPE_AGENT).
			tabAtPos("tab-lt-2", "left-top", "s", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
			tabAtPos("tab-rb-1", "right-bot", "k", leapmuxv1.TabType_TAB_TYPE_FILE).
			st
		hub := &opCaptureHub{recordingHub: &recordingHub{materialized: state}}
		startCustomSpawnIPC(t, hub, nil)

		data := runControl(t, "tile", "swap", "left-top", "right-bot").Data(t)
		newA, _ := data["tile_a"].(string)
		newB, _ := data["tile_b"].(string)
		assert.Equal(t, true, data["reissued_ids"])
		assert.NotEmpty(t, newA)
		assert.NotEmpty(t, newB)
		assert.NotEqual(t, "left-top", newA)
		assert.NotEqual(t, "right-bot", newB)

		require.Len(t, hub.submitted, 1)
		next := validateOpsAgainstMaterialized(t, state, hub.submitted[0])

		// Cloned tiles sit in each other's parent and position.
		assert.Equal(t, "right-split", next.Nodes[newA].GetParentId())
		assert.Equal(t, "b", next.Nodes[newA].GetPosition().GetValue())
		assert.Equal(t, "left-split", next.Nodes[newB].GetParentId())
		assert.Equal(t, "a", next.Nodes[newB].GetPosition().GetValue())

		// Old leaves are tombstoned.
		assert.False(t, crdt.HLCIsZero(next.Nodes["left-top"].GetTombstoneAt()))
		assert.False(t, crdt.HLCIsZero(next.Nodes["right-bot"].GetTombstoneAt()))

		// Tabs migrated onto the cloned leaves with their original positions intact.
		assert.Equal(t, newA, next.Tabs["tab-lt-1"].GetTileId().GetValue())
		assert.Equal(t, "h", next.Tabs["tab-lt-1"].GetPosition().GetValue())
		assert.Equal(t, newA, next.Tabs["tab-lt-2"].GetTileId().GetValue())
		assert.Equal(t, "s", next.Tabs["tab-lt-2"].GetPosition().GetValue())
		assert.Equal(t, newB, next.Tabs["tab-rb-1"].GetTileId().GetValue())
		assert.Equal(t, "k", next.Tabs["tab-rb-1"].GetPosition().GetValue())
	})

	t.Run("cross-parent leaf-to-grid-cell preserves grid coordinate and split position", func(t *testing.T) {
		state := newStateBuilder().
			workspace("ws-1", "root").
			splitNode("root", "", "").
			leafNode("side-leaf", "root", "a").
			gridNode("grid-1", "root", "b").
			leafNode("cell-00", "grid-1", "0,0").
			leafNode("cell-01", "grid-1", "0,1").
			tabAtPos("tab-side", "side-leaf", "p", leapmuxv1.TabType_TAB_TYPE_AGENT).
			tabAtPos("tab-cell", "cell-01", "q", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
			st
		grid := state.Nodes["grid-1"]
		grid.Rows = &leapmuxv1.LWWUint32{Value: 1}
		grid.Cols = &leapmuxv1.LWWUint32{Value: 2}
		grid.RowRatios = &leapmuxv1.LWWDoubles{Value: &leapmuxv1.DoubleList{Values: []float64{1}}}
		grid.ColRatios = &leapmuxv1.LWWDoubles{Value: &leapmuxv1.DoubleList{Values: []float64{0.4, 0.6}}}

		hub := &opCaptureHub{recordingHub: &recordingHub{materialized: state}}
		startCustomSpawnIPC(t, hub, nil)

		data := runControl(t, "tile", "swap", "side-leaf", "cell-01").Data(t)
		newA, _ := data["tile_a"].(string)
		newB, _ := data["tile_b"].(string)
		assert.Equal(t, true, data["reissued_ids"])

		require.Len(t, hub.submitted, 1)
		next := validateOpsAgainstMaterialized(t, state, hub.submitted[0])
		assert.Equal(t, "grid-1", next.Nodes[newA].GetParentId())
		assert.Equal(t, "0,1", next.Nodes[newA].GetPosition().GetValue())
		assert.Equal(t, "root", next.Nodes[newB].GetParentId())
		assert.Equal(t, "a", next.Nodes[newB].GetPosition().GetValue())
		assert.Equal(t, newA, next.Tabs["tab-side"].GetTileId().GetValue())
		assert.Equal(t, "p", next.Tabs["tab-side"].GetPosition().GetValue())
		assert.Equal(t, newB, next.Tabs["tab-cell"].GetTileId().GetValue())
		assert.Equal(t, "q", next.Tabs["tab-cell"].GetPosition().GetValue())
		assert.False(t, crdt.HLCIsZero(next.Nodes["side-leaf"].GetTombstoneAt()))
		assert.False(t, crdt.HLCIsZero(next.Nodes["cell-01"].GetTombstoneAt()))
	})

	t.Run("cross-parent split-subtree-to-leaf preserves split ratios, children, and tabs", func(t *testing.T) {
		state := newStateBuilder().
			workspace("ws-1", "root").
			splitNode("root", "", "").
			splitNode("left-branch", "root", "a").
			splitNode("nested-split", "left-branch", "a").
			leafNode("nested-c1", "nested-split", "a").
			leafNode("nested-c2", "nested-split", "b").
			leafNode("left-other", "left-branch", "b").
			leafNode("right-leaf", "root", "b").
			tabAtPos("tab-c1", "nested-c1", "d", leapmuxv1.TabType_TAB_TYPE_AGENT).
			tabAtPos("tab-c2", "nested-c2", "e", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
			tabAtPos("tab-r", "right-leaf", "f", leapmuxv1.TabType_TAB_TYPE_FILE).
			st
		ns := state.Nodes["nested-split"]
		ns.Direction = &leapmuxv1.LWWDirection{Value: leapmuxv1.SplitDirection_SPLIT_DIRECTION_HORIZONTAL}
		ns.Ratios = &leapmuxv1.LWWDoubles{Value: &leapmuxv1.DoubleList{Values: []float64{0.3, 0.7}}}

		hub := &opCaptureHub{recordingHub: &recordingHub{materialized: state}}
		startCustomSpawnIPC(t, hub, nil)

		data := runControl(t, "tile", "swap", "nested-split", "right-leaf").Data(t)
		newNested, _ := data["tile_a"].(string)
		newRight, _ := data["tile_b"].(string)
		assert.Equal(t, true, data["reissued_ids"])

		require.Len(t, hub.submitted, 1)
		next := validateOpsAgainstMaterialized(t, state, hub.submitted[0])

		clonedSplit := next.Nodes[newNested]
		require.NotNil(t, clonedSplit)
		assert.Equal(t, "root", clonedSplit.GetParentId())
		assert.Equal(t, "b", clonedSplit.GetPosition().GetValue())
		assert.Equal(t, leapmuxv1.NodeKind_NODE_KIND_SPLIT, clonedSplit.GetKind().GetValue())
		assert.Equal(t, leapmuxv1.SplitDirection_SPLIT_DIRECTION_HORIZONTAL, clonedSplit.GetDirection().GetValue())
		assert.Equal(t, []float64{0.3, 0.7}, clonedSplit.GetRatios().GetValue().GetValues())

		assert.Equal(t, "left-branch", next.Nodes[newRight].GetParentId())
		assert.Equal(t, "a", next.Nodes[newRight].GetPosition().GetValue())
		assert.Equal(t, newRight, next.Tabs["tab-r"].GetTileId().GetValue())
		assert.Equal(t, "f", next.Tabs["tab-r"].GetPosition().GetValue())

		// Tabs on nested-c1 and nested-c2 moved onto the freshly cloned leaves under newNested.
		c1Tile := next.Tabs["tab-c1"].GetTileId().GetValue()
		c2Tile := next.Tabs["tab-c2"].GetTileId().GetValue()
		assert.NotEqual(t, "nested-c1", c1Tile)
		assert.NotEqual(t, "nested-c2", c2Tile)
		assert.Equal(t, newNested, next.Nodes[c1Tile].GetParentId())
		assert.Equal(t, "a", next.Nodes[c1Tile].GetPosition().GetValue())
		assert.Equal(t, newNested, next.Nodes[c2Tile].GetParentId())
		assert.Equal(t, "b", next.Nodes[c2Tile].GetPosition().GetValue())
		assert.Equal(t, "d", next.Tabs["tab-c1"].GetPosition().GetValue())
		assert.Equal(t, "e", next.Tabs["tab-c2"].GetPosition().GetValue())

		// Old subtree nodes are all tombstoned.
		assert.False(t, crdt.HLCIsZero(next.Nodes["nested-c1"].GetTombstoneAt()))
		assert.False(t, crdt.HLCIsZero(next.Nodes["nested-c2"].GetTombstoneAt()))
		assert.False(t, crdt.HLCIsZero(next.Nodes["nested-split"].GetTombstoneAt()))
		assert.False(t, crdt.HLCIsZero(next.Nodes["right-leaf"].GetTombstoneAt()))
	})

	t.Run("cross-parent retries once on BATCH_REJECTION_TAB_PLACEMENT_INVALID", func(t *testing.T) {
		state := newStateBuilder().
			workspace("ws-1", "root").
			splitNode("root", "", "").
			splitNode("left-split", "root", "a").
			leafNode("left-a", "left-split", "a").
			leafNode("left-b", "left-split", "b").
			leafNode("right-leaf", "root", "b").
			st
		attempts := 0
		hub := &recordingHub{
			materialized: state,
			onSubmitOps: func(_ context.Context) ([]byte, error) {
				attempts++
				if attempts == 1 {
					return proto.Marshal(&leapmuxv1.SubmitOpsResponse{
						Results: []*leapmuxv1.BatchResult{{
							Outcome: &leapmuxv1.BatchResult_Rejected{
								Rejected: &leapmuxv1.BatchRejection{
									Reason: leapmuxv1.BatchRejectionReason_BATCH_REJECTION_TAB_PLACEMENT_INVALID,
								},
							},
						}},
					})
				}
				return proto.Marshal(&leapmuxv1.SubmitOpsResponse{Results: []*leapmuxv1.BatchResult{{}}})
			},
		}
		startSpawnIPC(t, hub, nil)

		data := runControl(t, "tile", "swap", "left-a", "right-leaf").Data(t)
		assert.Equal(t, true, data["reissued_ids"])
		assert.Equal(t, 2, attempts)
	})
}

// ALTITUDE-3: `tile close --with-tabs=close` and `tile remove-grid --with-tabs=close`
// share guardBulkCloseTabs, refusing dirty worktrees and failing closed on
// InspectTabsClose error_hint before submitting any CRDT op.
func TestTileCloseAndRemoveGrid_DirtyWorktreeGuard(t *testing.T) {
	t.Run("tile close refuses dirty worktree unless --allow-busy", func(t *testing.T) {
		state := newStateBuilder().
			workspace("ws-1", "root").
			splitNode("root", "", "").
			leafNode("leaf-a", "root", "a").
			leafNode("leaf-b", "root", "b").
			tabOnWorker("ag-dirty", "leaf-a", "worker-A", leapmuxv1.TabType_TAB_TYPE_AGENT).
			st
		hub := &opCaptureHub{recordingHub: &recordingHub{workers: workerRows("worker-A"), materialized: state}}
		disp := &deleteGuardDispatcher{dirtyWorktree: true, sharedPath: "/repo/wt-tile"}
		startCustomSpawnIPC(t, hub, disp)

		f := runControl(t, "tile", "close", "leaf-a", "--with-tabs", "close").requireFailure(t, cli.CodeTabBusyRefused)
		assert.Contains(t, f.Message, "feature/wip")
		assert.Contains(t, f.Message, "/repo/wt-tile")
		assert.Empty(t, hub.submitted, "dirty worktree must refuse before submitting CRDT ops")

		data := runControl(t, "tile", "close", "leaf-a", "--with-tabs", "close", "--allow-busy").Data(t)
		assert.Equal(t, float64(1), data["tabs_closed"])
		assert.Len(t, hub.submitted, 1)
	})

	t.Run("tile remove-grid refuses dirty worktree and fails closed on error_hint", func(t *testing.T) {
		state := newStateBuilder().
			workspace("ws-1", "root").
			splitNode("root", "", "").
			leafNode("leaf-a", "root", "a").
			gridNode("grid-b", "root", "b").
			leafNode("cell-00", "grid-b", "0,0").
			tabOnWorker("ag-grid-dirty", "cell-00", "worker-A", leapmuxv1.TabType_TAB_TYPE_AGENT).
			st
		hub := &opCaptureHub{recordingHub: &recordingHub{workers: workerRows("worker-A"), materialized: state}}
		startCustomSpawnIPC(t, hub, &deleteGuardDispatcher{dirtyWorktree: true, sharedPath: "/repo/wt-grid"})

		f := runControl(t, "tile", "remove-grid", "grid-b", "--with-tabs", "close").requireFailure(t, cli.CodeTabBusyRefused)
		assert.Contains(t, f.Message, "/repo/wt-grid")
		assert.Empty(t, hub.submitted)

		startCustomSpawnIPC(t, hub, &deleteGuardDispatcher{inspectErrorHint: "corrupt git index"})
		f2 := runControl(t, "tile", "remove-grid", "grid-b", "--with-tabs", "close").requireFailure(t, cli.CodeUnavailable)
		assert.Contains(t, f2.Message, "corrupt git index")
		assert.Empty(t, hub.submitted)
	})
}

func TestDirtyCloseReasonsFromInspection_DeduplicatesAndFormatsEdgeCases(t *testing.T) {
	items := []*leapmuxv1.TabCloseInspection{
		nil,
		{
			Tab:          &leapmuxv1.TabIdent{TabId: "clean-1"},
			ShouldPrompt: false,
			GitState:     &leapmuxv1.BranchGitState{HasUncommittedChanges: true},
		},
		{
			Tab:          &leapmuxv1.TabIdent{TabId: "empty-state"},
			ShouldPrompt: true,
			GitState:     &leapmuxv1.BranchGitState{},
		},
		{
			Tab:          &leapmuxv1.TabIdent{TabId: "dirty-dup-1"},
			ShouldPrompt: true,
			BranchName:   "feat/a",
			WorktreePath: "/repo/wt-a",
			GitState:     &leapmuxv1.BranchGitState{HasUncommittedChanges: true, UnpushedCommitCount: 2, RemoteBranchMissing: true},
		},
		{
			Tab:          &leapmuxv1.TabIdent{TabId: "dirty-dup-2"},
			ShouldPrompt: true,
			BranchName:   "feat/a",
			WorktreePath: "/repo/wt-a",
			GitState:     &leapmuxv1.BranchGitState{HasUncommittedChanges: true},
		},
		{
			Tab:          &leapmuxv1.TabIdent{TabId: "unpushed-branch"},
			ShouldPrompt: true,
			WorkingDir:   "/repo/main",
			GitState:     &leapmuxv1.BranchGitState{RemoteBranchMissing: true},
		},
		{
			Tab:       &leapmuxv1.TabIdent{TabId: "broken-git"},
			ErrorHint: "  git status timed out  ",
		},
	}

	dirty, probeErr := dirtyCloseReasonsFromInspection(items)
	require.Len(t, dirty, 2)
	assert.Equal(t, "dirty-dup-1", dirty[0].tabID)
	assert.Equal(t, "branch feat/a at /repo/wt-a has uncommitted changes and 2 unpushed commits", dirty[0].detail)
	assert.Equal(t, "unpushed-branch", dirty[1].tabID)
	assert.Equal(t, "git checkout at /repo/main has unpushed local branch", dirty[1].detail)

	require.Error(t, probeErr)
	assert.Equal(t, cli.CodeUnavailable, cli.FromError(probeErr).Code)
	assert.Contains(t, probeErr.Error(), "broken-git")
	assert.Contains(t, probeErr.Error(), "git status timed out")
}
