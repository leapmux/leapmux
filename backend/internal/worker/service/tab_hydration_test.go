package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// The whole point of the verdict is that the client can tell "the worker holds
// no record for this id" from a bare omission. Collapsing them is what forced
// the client to retry every unanswered tab forever.
func TestTabHydrationVerdicts_DistinguishesFoundFromAbsent(t *testing.T) {
	t.Parallel()

	got := tabHydrationVerdicts([]string{"served", "gone"}, map[string]bool{"served": true})

	require.Len(t, got, 2, "one verdict per REQUESTED id, answered or not")
	byID := map[string]leapmuxv1.TabHydrationStatus{}
	for _, v := range got {
		byID[v.GetTabId()] = v.GetStatus()
	}
	assert.Equal(t, leapmuxv1.TabHydrationStatus_TAB_HYDRATION_STATUS_FOUND, byID["served"])
	assert.Equal(t, leapmuxv1.TabHydrationStatus_TAB_HYDRATION_STATUS_ABSENT, byID["gone"],
		"no record at all is permanent -- the client must stop asking")
}

func TestTabHydrationVerdicts_NoRequestedIDsAnswersNothing(t *testing.T) {
	t.Parallel()

	// The list-everything form names no ids, so there is no requested set to
	// answer for -- and inventing verdicts would tell the caller about tabs it
	// never asked about.
	assert.Nil(t, tabHydrationVerdicts(nil, map[string]bool{"t1": true}))
}

func TestTabHydrationVerdicts_PreservesRequestOrder(t *testing.T) {
	t.Parallel()

	got := tabHydrationVerdicts([]string{"c", "a", "b"}, map[string]bool{"a": true})

	require.Len(t, got, 3)
	assert.Equal(t, []string{"c", "a", "b"}, []string{got[0].GetTabId(), got[1].GetTabId(), got[2].GetTabId()},
		"verdicts line up with the caller's list so it can zip them without a lookup")
}
