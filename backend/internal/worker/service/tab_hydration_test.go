package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// The whole point of the verdict is that the client can tell a TRANSIENT
// omission from a PERMANENT one. Collapsing them is what forced the client to
// retry every unanswered tab forever.
func TestTabHydrationVerdicts_DistinguishesHiddenFromAbsent(t *testing.T) {
	got := tabHydrationVerdicts(
		[]string{"served", "hidden", "gone"},
		map[string]bool{"served": true},
		map[string]bool{"hidden": true},
	)

	require.Len(t, got, 3, "one verdict per REQUESTED id, answered or not")
	byID := map[string]leapmuxv1.TabHydrationStatus{}
	for _, v := range got {
		byID[v.GetTabId()] = v.GetStatus()
	}
	assert.Equal(t, leapmuxv1.TabHydrationStatus_TAB_HYDRATION_STATUS_FOUND, byID["served"])
	assert.Equal(t, leapmuxv1.TabHydrationStatus_TAB_HYDRATION_STATUS_NOT_ACCESSIBLE, byID["hidden"],
		"a record we hold but may not serve yet is transient -- the client must keep asking")
	assert.Equal(t, leapmuxv1.TabHydrationStatus_TAB_HYDRATION_STATUS_ABSENT, byID["gone"],
		"no record at all is permanent -- the client must stop asking")
}

func TestTabHydrationVerdicts_FoundWinsOverHidden(t *testing.T) {
	// An id served from the in-memory manager is FOUND even if a stale DB row
	// for the same id would have been filtered by the accessible set.
	got := tabHydrationVerdicts(
		[]string{"t1"},
		map[string]bool{"t1": true},
		map[string]bool{"t1": true},
	)

	require.Len(t, got, 1)
	assert.Equal(t, leapmuxv1.TabHydrationStatus_TAB_HYDRATION_STATUS_FOUND, got[0].GetStatus())
}

func TestTabHydrationVerdicts_NoRequestedIDsAnswersNothing(t *testing.T) {
	// The list-everything form names no ids, so there is no requested set to
	// answer for -- and inventing verdicts would tell the caller about tabs it
	// never asked about.
	assert.Nil(t, tabHydrationVerdicts(nil, map[string]bool{"t1": true}, nil))
}

func TestTabHydrationVerdicts_PreservesRequestOrder(t *testing.T) {
	got := tabHydrationVerdicts([]string{"c", "a", "b"}, map[string]bool{"a": true}, nil)

	require.Len(t, got, 3)
	assert.Equal(t, []string{"c", "a", "b"}, []string{got[0].GetTabId(), got[1].GetTabId(), got[2].GetTabId()},
		"verdicts line up with the caller's list so it can zip them without a lookup")
}
