package cmd

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
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
	assert.Equal(t, "invalid_request", code)
	assert.Contains(t, msg, "close")
	assert.Contains(t, msg, "move")
}

// TestKindLabel covers the user-facing strings used in error
// messages. UNSPECIFIED falls back to the proto's enum string so an
// unknown future kind doesn't print as "" and erase context.
func TestKindLabel(t *testing.T) {
	assert.Equal(t, "LEAF", kindLabel(leapmuxv1.NodeKind_NODE_KIND_LEAF))
	assert.Equal(t, "SPLIT", kindLabel(leapmuxv1.NodeKind_NODE_KIND_SPLIT))
	assert.Equal(t, "GRID", kindLabel(leapmuxv1.NodeKind_NODE_KIND_GRID))
	assert.NotEmpty(t, kindLabel(leapmuxv1.NodeKind_NODE_KIND_UNSPECIFIED))
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
