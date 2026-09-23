package providerkit

import (
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The menu reads top to bottom, so the strongest level belongs at the top. A
// provider whose levels arrive as DATA gets them in whatever order the source
// wrote them: ZCode's own `config.json` states GLM-5.3 as `["low", "max",
// "high"]`, which led the menu with the weakest level.
func TestSortEffortsDescending_OrdersTheLadderStrongestFirst(t *testing.T) {
	t.Parallel()

	efforts := []*agent.EffortInfo{EffortTier("low"), EffortTier("max"), EffortTier(agent.EffortHigh)}
	SortEffortsDescending(efforts)
	assert.Equal(t, []string{"max", "high", "low"}, agenttest.OptionIDs(efforts))

	full := []*agent.EffortInfo{
		EffortTier("none"), EffortTier("minimal"), EffortTier("low"), EffortTier("medium"),
		EffortTier(agent.EffortHigh), EffortTier(agent.EffortXHigh), EffortTier("max"), EffortTier("ultra"),
		EffortTier("ultracode"),
	}
	SortEffortsDescending(full)
	assert.Equal(t, []string{
		"ultracode", "ultra", "max", "xhigh", "high", "medium", "low", "minimal", "none",
	}, agenttest.OptionIDs(full))
}

// A model that offers a toggle rather than a ladder: ZCode gives GLM-5-Turbo
// `enabled` and `off`, and "thinking on" must not sort under "thinking off".
func TestSortEffortsDescending_RanksTheOnOffToggle(t *testing.T) {
	t.Parallel()

	efforts := []*agent.EffortInfo{EffortTier("off"), EffortTier("enabled")}
	SortEffortsDescending(efforts)
	assert.Equal(t, []string{"enabled", "off"}, agenttest.OptionIDs(efforts))
}

// A level a CLI adds mid-release has no claim to a place inside the ladder, and
// the source's own order is the only thing left to rank such levels by.
func TestSortEffortsDescending_KeepsUnrankedLevelsLastAndInOrder(t *testing.T) {
	t.Parallel()

	efforts := []*agent.EffortInfo{
		EffortTier("zeta"), EffortTier("low"), EffortTier("alpha"), EffortTier("max"),
	}
	SortEffortsDescending(efforts)
	assert.Equal(t, []string{"max", "low", "zeta", "alpha"}, agenttest.OptionIDs(efforts))
}

func TestSortEffortsDescending_MatchesTheLadderWithoutRegardToCase(t *testing.T) {
	t.Parallel()

	efforts := []*agent.EffortInfo{{Id: "LOW"}, {Id: "Max"}}
	SortEffortsDescending(efforts)
	assert.Equal(t, []string{"Max", "LOW"}, agenttest.OptionIDs(efforts))
}

// The label table and the ladder must cover the same levels, and the check runs
// in BOTH directions because each direction catches a different defect.
//
// Ranked with no label renders a raw token in a menu that sorts correctly.
// Labelled with no rank is the one this diff was written for: `enabled` reached
// the label table first, and a level that nothing ranks sorts silently into the
// tail -- under `off`, for the toggle model where "off" is the other option.
// Only the second direction fails on that, so only it would have caught it.
func TestEffortRank_AgreesWithTheLabelTableInBothDirections(t *testing.T) {
	t.Parallel()

	for id := range EffortRank {
		// The ladder carries separator and spelling variants that no menu draws
		// (`x-high` for `xhigh`, `med` for `medium`); a variant needs no label
		// of its own, only a rank equal to the spelling it stands in for.
		if _, isVariant := effortLabels[id]; !isVariant {
			rank, ok := EffortRankOf(id)
			require.True(t, ok, "unreachable: id came from EffortRank")
			var canonical []string
			for labelled := range effortLabels {
				if r, ranked := EffortRankOf(labelled); ranked && r == rank {
					canonical = append(canonical, labelled)
				}
			}
			assert.NotEmptyf(t, canonical,
				"level %q is ranked %d, has no label, and shares its rank with no labelled level, so nothing draws it", id, rank)
			continue
		}
		assert.Containsf(t, effortLabels, id, "level %q is ranked but has no shared label", id)
	}

	for id := range effortLabels {
		if id == agent.EffortAuto {
			// Auto is not a strength -- it means "send no level at all" -- and
			// SortEffortsDescending must never see it. See its doc.
			continue
		}
		_, ranked := EffortRankOf(id)
		assert.Truef(t, ranked,
			"level %q has a label but no rank, so it sorts below every ranked level, `off` included", id)
	}
}

// The shared table spells "xhigh" once, for every provider.
func TestEffortLabelSpellsExtraHighOnce(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "Extra High", EffortLabel(agent.EffortXHigh))
}

// The ladder is the single declaration of the axis, and these are the
// properties every derived table needs from it. Each one used to be a second
// hand-written statement that could fall behind.
func TestEffortLadder_IsTheOneDeclarationOfTheAxis(t *testing.T) {
	t.Parallel()

	seen := map[string]bool{}
	for i, rung := range effortLadder {
		require.NotEmptyf(t, rung, "rung %d is empty, so it ranks nothing", i)
		require.NotEmptyf(t, rung[0].Label,
			"the canonical id of rung %d draws no label, but a menu shows it", i)
		for _, level := range rung {
			assert.Falsef(t, seen[level.ID], "id %q appears on two rungs, so its rank is ambiguous", level.ID)
			seen[level.ID] = true
			assert.Equal(t, strings.ToLower(level.ID), level.ID,
				"EffortRankOf lowercases before the lookup, so a capitalized key is unreachable")
		}
	}

	// Rank counts UP from the last rung, and rank 0 is the "thinking off"
	// sentinel chooseDefaultEffort and raiseEffortOffNone both test for.
	for _, level := range effortLadder[len(effortLadder)-1] {
		rank, ok := EffortRankOf(level.ID)
		require.True(t, ok)
		assert.Zerof(t, rank, "%q is on the last rung, which MUST be the thinking-off rank", level.ID)
	}
	for _, rung := range effortLadder[:len(effortLadder)-1] {
		rank, _ := EffortRankOf(rung[0].ID)
		assert.Positivef(t, rank, "%q is a level that thinks, so it must not carry the thinking-off rank", rung[0].ID)
	}

	// Every id on one rung ranks the same, and every rung outranks the next.
	for i, rung := range effortLadder {
		want, _ := EffortRankOf(rung[0].ID)
		for _, level := range rung[1:] {
			got, ok := EffortRankOf(level.ID)
			require.Truef(t, ok, "%q is on the ladder but unranked", level.ID)
			assert.Equalf(t, want, got, "%q shares a rung with %q, so it must share its rank", level.ID, rung[0].ID)
		}
		if i > 0 {
			above, _ := EffortRankOf(effortLadder[i-1][0].ID)
			assert.Greaterf(t, above, want, "the ladder reads strongest first, so rung %d must outrank rung %d", i-1, i)
		}
	}
}
