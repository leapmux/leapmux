package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func effortIDs(efforts []*EffortInfo) []string {
	ids := make([]string, 0, len(efforts))
	for _, e := range efforts {
		ids = append(ids, e.GetId())
	}
	return ids
}

// The menu reads top to bottom, so the strongest level belongs at the top. A
// provider whose levels arrive as DATA gets them in whatever order the source
// wrote them: ZCode's own `config.json` states GLM-5.3 as `["low", "max",
// "high"]`, which led the menu with the weakest level.
func TestSortEffortsByStrength_OrdersTheLadderStrongestFirst(t *testing.T) {
	t.Parallel()

	efforts := []*EffortInfo{effortTier("low"), effortTier("max"), effortTier(EffortHigh)}
	sortEffortsByStrength(efforts)
	assert.Equal(t, []string{"max", "high", "low"}, effortIDs(efforts))

	full := []*EffortInfo{
		effortTier("none"), effortTier("minimal"), effortTier("low"), effortTier("medium"),
		effortTier(EffortHigh), effortTier(EffortXHigh), effortTier("max"), effortTier("ultra"),
		effortTier("ultracode"),
	}
	sortEffortsByStrength(full)
	assert.Equal(t, []string{
		"ultracode", "ultra", "max", "xhigh", "high", "medium", "low", "minimal", "none",
	}, effortIDs(full))
}

// The whole ladder, spelled by hand in codexDefaultEfforts, is the order this
// table exists to repeat. The two must not drift: a level moved in one and not
// the other would order the same word differently in two menus.
func TestSortEffortsByStrength_AgreesWithTheCodexCatalogOrder(t *testing.T) {
	t.Parallel()

	// Auto is not a strength and never reaches the sort, so it is dropped first.
	tail := append([]*EffortInfo(nil), codexDefaultEfforts[1:]...)
	want := effortIDs(tail)

	shuffled := []*EffortInfo{tail[3], tail[0], tail[6], tail[1], tail[5], tail[2], tail[7], tail[4]}
	sortEffortsByStrength(shuffled)
	assert.Equal(t, want, effortIDs(shuffled))
}

// A model that offers a toggle rather than a ladder: ZCode gives GLM-5-Turbo
// `enabled` and `off`, and "thinking on" must not sort under "thinking off".
func TestSortEffortsByStrength_RanksTheOnOffToggle(t *testing.T) {
	t.Parallel()

	efforts := []*EffortInfo{effortTier("off"), effortTier("enabled")}
	sortEffortsByStrength(efforts)
	assert.Equal(t, []string{"enabled", "off"}, effortIDs(efforts))
}

// A level a CLI adds mid-release has no claim to a place inside the ladder, and
// the source's own order is the only thing left to rank such levels by.
func TestSortEffortsByStrength_KeepsUnrankedLevelsLastAndInOrder(t *testing.T) {
	t.Parallel()

	efforts := []*EffortInfo{
		effortTier("zeta"), effortTier("low"), effortTier("alpha"), effortTier("max"),
	}
	sortEffortsByStrength(efforts)
	assert.Equal(t, []string{"max", "low", "zeta", "alpha"}, effortIDs(efforts))
}

func TestSortEffortsByStrength_MatchesTheLadderWithoutRegardToCase(t *testing.T) {
	t.Parallel()

	efforts := []*EffortInfo{{Id: "LOW"}, {Id: "Max"}}
	sortEffortsByStrength(efforts)
	assert.Equal(t, []string{"Max", "LOW"}, effortIDs(efforts))
}

// Every ranked level also has to READ as one: a level in the ladder that the
// label table never heard of would sort correctly and render as a raw token.
func TestEffortStrength_EveryRankedLevelHasALabel(t *testing.T) {
	t.Parallel()

	for id := range effortStrength {
		assert.Containsf(t, effortLabels, id, "level %q is ranked but has no shared label", id)
	}
}
