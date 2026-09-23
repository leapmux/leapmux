package codex

import (
	"slices"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The sort and Codex's static menu must agree, and now they agree BY
// CONSTRUCTION: both read effortLadder. The test stays because the two reach it
// by different routes -- the sort through providerkit.EffortRank, the menu through
// providerkit.EffortLadderIDs -- and a change to either derivation could still part them.
func TestSortEffortsDescending_AgreesWithTheCodexCatalogOrder(t *testing.T) {
	t.Parallel()

	// Auto is not a strength and never reaches the sort, so it is dropped first.
	tail := slices.Clone(codexDefaultEfforts[1:])
	want := agenttest.OptionIDs(tail)

	// A plain reversal is NOT a sufficient input: providerkit.SortEffortsDescending could be
	// implemented as slices.Reverse and still turn reverse(want) back into want.
	// Rotating leaves most adjacent pairs already in correct relative order, so
	// only a rank-aware sort recovers the full order.
	shuffled := slices.Clone(tail)
	shuffled = append(shuffled[3:], shuffled[:3]...)
	require.NotEqual(t, want, agenttest.OptionIDs(shuffled), "the input must start out of order")
	providerkit.SortEffortsDescending(shuffled)
	assert.Equal(t, want, agenttest.OptionIDs(shuffled))

	// The same input must defeat a reversal-only implementation, which is the
	// mistake a reversed input cannot catch.
	reverseOnly := slices.Clone(tail)
	reverseOnly = append(reverseOnly[3:], reverseOnly[:3]...)
	slices.Reverse(reverseOnly)
	assert.NotEqual(t, want, agenttest.OptionIDs(reverseOnly), "a reversal alone must not reproduce the order")
}

// Codex states WHICH levels it offers, and the ladder states their order. The
// static Codex catalog therefore holds only ranked levels, in ladder order.
func TestCodexEffortCatalogFollowsTheLadder(t *testing.T) {
	t.Parallel()

	for id := range codexEffortIDs {
		_, ranked := providerkit.EffortRankOf(id)
		assert.Truef(t, ranked, "codex offers %q, which is not on the ladder, so nothing ranks it", id)
	}
	assert.Equal(t, agent.EffortAuto, codexDefaultEfforts[0].GetId(), "the LeapMux auto sentinel leads the menu")
	want := []string{}
	for _, id := range providerkit.EffortLadderIDs() {
		if codexEffortIDs[id] {
			want = append(want, id)
		}
	}
	assert.Equal(t, want, agenttest.OptionIDs(codexDefaultEfforts[1:]),
		"the static Codex catalog must read in ladder order")
}

// Every tier of Codex's catalog must agree with the shared table on its label. This is the
// drift the table exists to remove: three files spelled "xhigh" out by hand and a
// fourth special-cased it.
func TestCodexEffortsUseSharedLabels(t *testing.T) {
	t.Parallel()

	for _, tier := range codexDefaultEfforts {
		assert.Equal(t, providerkit.EffortLabel(tier.Id), tier.Name, "effort %q must use the shared label", tier.Id)
	}
}
