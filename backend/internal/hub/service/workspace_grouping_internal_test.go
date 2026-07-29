package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// TestGroupTabsByWorker_GroupsInStableOrderAndDropsUnhostedTabs covers the two
// properties DeleteWorkspace's response depends on and its own test cannot see.
//
// The tab list is what each Worker is told to tear down, and tab ids are unique
// per USER, not per worker -- so a tab landing in the wrong group is a live tab on
// another machine that the cleanup would close. The ORDER matters for a different
// reason: it fixes the response's worker_ids and the per-worker status entries the
// CLI prints, so a map-iteration-order implementation would make both
// nondeterministic.
func TestGroupTabsByWorker_GroupsInStableOrderAndDropsUnhostedTabs(t *testing.T) {
	// Deliberately INTERLEAVED: w1's rows are not contiguous. The query does say
	// ORDER BY worker_id, but the grouping must not DEPEND on it -- that ordering
	// lives in three separate dialect files, and a single-pass implementation that
	// assumed contiguity would emit two groups for w1 the moment any one of them
	// lost the clause. The fixture used to be contiguous while this comment
	// claimed otherwise, so it never exercised the property it described.
	grouped := groupTabsByWorker([]store.OwnedTabRef{
		{WorkerID: "w1", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "a-1"},
		{WorkerID: "w2", TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabID: "f-2"},
		{WorkerID: "w1", TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabID: "t-1"},
	})

	require.Len(t, grouped, 2)
	assert.Equal(t, "w1", grouped[0].GetWorkerId(),
		"groups must follow first-appearance order, not map iteration order")
	assert.Equal(t, "w2", grouped[1].GetWorkerId())

	require.Len(t, grouped[0].GetTabs(), 2)
	assert.Equal(t, "a-1", grouped[0].GetTabs()[0].GetTabId())
	assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_AGENT, grouped[0].GetTabs()[0].GetTabType())
	assert.Equal(t, "t-1", grouped[0].GetTabs()[1].GetTabId())
	assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_TERMINAL, grouped[0].GetTabs()[1].GetTabType())

	require.Len(t, grouped[1].GetTabs(), 1)
	assert.Equal(t, "f-2", grouped[1].GetTabs()[0].GetTabId(),
		"w2 must not be handed w1's tab ids")
}

// A row with no worker_id names no machine, so there is nothing to tear down.
// Binding it would emit a group keyed on the empty string and send one caller's
// cleanup to nobody.
func TestGroupTabsByWorker_DropsTabsWithNoWorker(t *testing.T) {
	grouped := groupTabsByWorker([]store.OwnedTabRef{
		{WorkerID: "", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "a-orphan"},
		{WorkerID: "w1", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "a-1"},
	})

	require.Len(t, grouped, 1, "the unhosted tab must not become its own group")
	assert.Equal(t, "w1", grouped[0].GetWorkerId())
	require.Len(t, grouped[0].GetTabs(), 1)
	assert.Equal(t, "a-1", grouped[0].GetTabs()[0].GetTabId(),
		"and must not be folded into a real worker's list")
}

func TestGroupTabsByWorker_EmptyInputYieldsNoGroups(t *testing.T) {
	assert.Empty(t, groupTabsByWorker(nil))
	assert.Empty(t, groupTabsByWorker([]store.OwnedTabRef{}))
}
