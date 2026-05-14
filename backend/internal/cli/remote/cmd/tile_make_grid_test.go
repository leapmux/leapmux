package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/util/lexorank"
)

// lexorankAt returns the Nth LexoRank from First(), used by the tests
// in this file to assemble the expected migration-position chain that
// the production code builds inline.
func lexorankAt(i int) string {
	pos := lexorank.First()
	for j := 0; j < i; j++ {
		pos = lexorank.After(pos)
	}
	return pos
}

// tabAtPos registers a live tab on a tile with an explicit position.
// Pre-existing `tab(...)` helper leaves Position nil; ordering tests
// need real positions so the sort key is exercised.
func (b *stateBuilder) tabAtPos(tabID, tileID, position string, tabType leapmuxv1.TabType) *stateBuilder {
	b.st.Tabs[tabID] = &leapmuxv1.TabRecord{
		TabId:    tabID,
		TabType:  tabType,
		TileId:   &leapmuxv1.LWWString{Value: tileID},
		Position: &leapmuxv1.LWWString{Value: position},
	}
	return b
}

// TestTabsOnTile_PositionOrder pins the position-sorted invariant
// every callsite relies on. Pre-fix the helper iterated state.GetTabs()
// in map order (non-deterministic in Go); migrate-and-re-stamp loops
// in tile-split / make-grid / inverse-split would produce different
// orderings between runs against the same fixture. The fix sorts by
// LexoRank position, with tab_id as a stable tiebreak.
func TestTabsOnTile_PositionOrder(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "T").
		leafNode("T", "", "").
		// Inserted in deliberately scrambled order so map iteration
		// order definitely doesn't match position order. With three
		// short tab ids and three positions a/b/c, the chance the map
		// happens to iterate in the right order is 1/6.
		tabAtPos("tab-mid", "T", "b", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
		tabAtPos("tab-last", "T", "c", leapmuxv1.TabType_TAB_TYPE_AGENT).
		tabAtPos("tab-first", "T", "a", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
		st

	got := crdt.TabsOnTile(state, "T")
	require.Len(t, got, 3)
	assert.Equal(t, "tab-first", got[0].TabID)
	assert.Equal(t, "tab-mid", got[1].TabID)
	assert.Equal(t, "tab-last", got[2].TabID)
}

// TestTabsOnTile_TiebreakByTabID covers the rare case where two tabs
// share a position (e.g. two clients raced to addTab at the same
// rank). The sort must be deterministic so the migration ops are
// identical across runs even under this degenerate fixture.
func TestTabsOnTile_TiebreakByTabID(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "T").
		leafNode("T", "", "").
		tabAtPos("tab-z", "T", "same", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
		tabAtPos("tab-a", "T", "same", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
		tabAtPos("tab-m", "T", "same", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
		st

	got := crdt.TabsOnTile(state, "T")
	require.Len(t, got, 3)
	assert.Equal(t, []string{"tab-a", "tab-m", "tab-z"}, []string{got[0].TabID, got[1].TabID, got[2].TabID})
}

// TestTabsOnTile_EmptyPositionsTreatedAsEqual: tabs without a
// position register (newly minted, not yet positioned) all sort to
// the front grouped together. Within that group, tab_id breaks ties
// deterministically. Tests the legacy-state path where pre-CRDT tab
// records exist without positions.
func TestTabsOnTile_EmptyPositionsTreatedAsEqual(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "T").
		leafNode("T", "", "").
		tabAtPos("tab-with-pos", "T", "z", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
		// `tab` helper leaves Position nil — i.e. GetPosition().GetValue() == "".
		tab("tab-b-no-pos", "T", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
		tab("tab-a-no-pos", "T", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
		st

	got := crdt.TabsOnTile(state, "T")
	require.Len(t, got, 3)
	// Empty positions sort before "z"; within the empty group, tab_id
	// ascending.
	assert.Equal(t, []string{"tab-a-no-pos", "tab-b-no-pos", "tab-with-pos"},
		[]string{got[0].TabID, got[1].TabID, got[2].TabID})
}

// TestMakeGridMigration_PreservesTabOrder walks the make-grid
// migration path end-to-end at the op level: build the same op
// sequence RunTileMakeGrid emits (kind/rows/cols/ratios + cell
// creations + tab migrations) and assert the SetTabPosition ops
// for the migrated tabs are emitted in strictly-increasing position
// order matching the pre-grid tab order on the leaf.
//
// Without the tabsOnTile sort, the SetTabPosition ops were emitted
// in Go-map-iteration order — different across runs — so a user's
// pre-make-grid tab order wouldn't survive the migration even
// though every tab kept its tile_id correctly.
func TestMakeGridMigration_PreservesTabOrder(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "T").
		leafNode("T", "", "").
		// Three tabs in a known order — note they're inserted into
		// the state map in scrambled order.
		tabAtPos("tab-mid", "T", "m", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
		tabAtPos("tab-last", "T", "z", leapmuxv1.TabType_TAB_TYPE_AGENT).
		tabAtPos("tab-first", "T", "a", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
		st
	bs := testBootstrap(state)

	// Mimic the RunTileMakeGrid migration loop exactly (the rest of
	// the handler emits node ops we don't need to inspect for this
	// invariant).
	tabs := crdt.TabsOnTile(state, "T")
	dest := "cell-0-0"
	ops := []*leapmuxv1.OrgOp{}
	for i, tref := range tabs {
		ops = append(ops, opSetTabTileID(bs, tref.TabType, tref.TabID, dest))
		ops = append(ops, opSetTabPosition(bs, tref.TabType, tref.TabID, lexorankAt(i)))
	}

	// Pull out (tabID, newPosition) pairs in emit order.
	type assignment struct {
		tabID string
		pos   string
	}
	assignments := []assignment{}
	for _, op := range ops {
		body, ok := op.GetBody().(*leapmuxv1.OrgOp_SetTabRegister)
		if !ok {
			continue
		}
		f, ok := body.SetTabRegister.GetField().(*leapmuxv1.SetTabRegisterOp_Position)
		if !ok {
			continue
		}
		assignments = append(assignments, assignment{
			tabID: body.SetTabRegister.GetTabId(),
			pos:   f.Position,
		})
	}
	require.Len(t, assignments, 3, "one SetTabPosition per migrated tab")

	// Order on the new cell follows the pre-grid order.
	assert.Equal(t, "tab-first", assignments[0].tabID)
	assert.Equal(t, "tab-mid", assignments[1].tabID)
	assert.Equal(t, "tab-last", assignments[2].tabID)

	// Positions are strictly increasing (LexoRank is lex-sortable, so
	// `<` is the rank comparison the projection uses when it sorts
	// tabs by position).
	assert.Less(t, assignments[0].pos, assignments[1].pos)
	assert.Less(t, assignments[1].pos, assignments[2].pos)
}

// TestInverseSplitMigration_PreservesSiblingTabOrder covers the
// other re-stamp site: when `tile close --tile-id <X>` collapses
// the parent SPLIT, the sibling's tabs migrate to the parent and
// get fresh positions via lexorankAt. The same position-sort
// guarantee must hold there — without it, two tabs on the sibling
// would land on the parent in non-deterministic order.
func TestInverseSplitMigration_PreservesSiblingTabOrder(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "P").
		splitNode("P", "", "").
		leafNode("childA", "P", "a"). // closing this leaf
		leafNode("childB", "P", "b"). // the sibling whose tabs migrate to P
		tabAtPos("sib-mid", "childB", "m", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
		tabAtPos("sib-last", "childB", "z", leapmuxv1.TabType_TAB_TYPE_AGENT).
		tabAtPos("sib-first", "childB", "a", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
		st
	bs := testBootstrap(state)

	ops := buildCloseTileOps(bs, "childA")

	// Walk SetTabPosition ops for sibling tabs.
	posOrder := []string{}
	posValues := []string{}
	for _, op := range ops {
		body, ok := op.GetBody().(*leapmuxv1.OrgOp_SetTabRegister)
		if !ok {
			continue
		}
		f, ok := body.SetTabRegister.GetField().(*leapmuxv1.SetTabRegisterOp_Position)
		if !ok {
			continue
		}
		posOrder = append(posOrder, body.SetTabRegister.GetTabId())
		posValues = append(posValues, f.Position)
	}
	require.Len(t, posOrder, 3, "inverse-split must re-stamp positions for every migrated sibling tab")
	assert.Equal(t, []string{"sib-first", "sib-mid", "sib-last"}, posOrder)
	assert.Less(t, posValues[0], posValues[1])
	assert.Less(t, posValues[1], posValues[2])
}
