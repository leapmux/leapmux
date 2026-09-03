package agent

import (
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// effortIDs reads the ids off a list of levels, for an assertion about ORDER.
//
// Generic over the same constraint sortEffortsDescending takes, because the two
// element types that carry a level both reach these tests: `*EffortInfo` for a
// catalog entry and `*leapmuxv1.AvailableOption` for an ACP config option.
func effortIDs[T interface{ GetId() string }](efforts []T) []string {
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
func TestSortEffortsDescending_OrdersTheLadderStrongestFirst(t *testing.T) {
	t.Parallel()

	efforts := []*EffortInfo{effortTier("low"), effortTier("max"), effortTier(EffortHigh)}
	sortEffortsDescending(efforts)
	assert.Equal(t, []string{"max", "high", "low"}, effortIDs(efforts))

	full := []*EffortInfo{
		effortTier("none"), effortTier("minimal"), effortTier("low"), effortTier("medium"),
		effortTier(EffortHigh), effortTier(EffortXHigh), effortTier("max"), effortTier("ultra"),
		effortTier("ultracode"),
	}
	sortEffortsDescending(full)
	assert.Equal(t, []string{
		"ultracode", "ultra", "max", "xhigh", "high", "medium", "low", "minimal", "none",
	}, effortIDs(full))
}

// The sort and Codex's static menu must agree, and now they agree BY
// CONSTRUCTION: both read effortLadder. The test stays because the two reach it
// by different routes -- the sort through effortRank, the menu through
// effortLadderIDs -- and a change to either derivation could still part them.
func TestSortEffortsDescending_AgreesWithTheCodexCatalogOrder(t *testing.T) {
	t.Parallel()

	// Auto is not a strength and never reaches the sort, so it is dropped first.
	tail := append([]*EffortInfo(nil), codexDefaultEfforts[1:]...)
	want := effortIDs(tail)

	shuffled := slices.Clone(tail)
	slices.Reverse(shuffled)
	sortEffortsDescending(shuffled)
	assert.Equal(t, want, effortIDs(shuffled))
}

// A model that offers a toggle rather than a ladder: ZCode gives GLM-5-Turbo
// `enabled` and `off`, and "thinking on" must not sort under "thinking off".
func TestSortEffortsDescending_RanksTheOnOffToggle(t *testing.T) {
	t.Parallel()

	efforts := []*EffortInfo{effortTier("off"), effortTier("enabled")}
	sortEffortsDescending(efforts)
	assert.Equal(t, []string{"enabled", "off"}, effortIDs(efforts))
}

// A level a CLI adds mid-release has no claim to a place inside the ladder, and
// the source's own order is the only thing left to rank such levels by.
func TestSortEffortsDescending_KeepsUnrankedLevelsLastAndInOrder(t *testing.T) {
	t.Parallel()

	efforts := []*EffortInfo{
		effortTier("zeta"), effortTier("low"), effortTier("alpha"), effortTier("max"),
	}
	sortEffortsDescending(efforts)
	assert.Equal(t, []string{"max", "low", "zeta", "alpha"}, effortIDs(efforts))
}

func TestSortEffortsDescending_MatchesTheLadderWithoutRegardToCase(t *testing.T) {
	t.Parallel()

	efforts := []*EffortInfo{{Id: "LOW"}, {Id: "Max"}}
	sortEffortsDescending(efforts)
	assert.Equal(t, []string{"Max", "LOW"}, effortIDs(efforts))
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

	for id := range effortRank {
		// The ladder carries separator and spelling variants that no menu draws
		// (`x-high` for `xhigh`, `med` for `medium`); a variant needs no label
		// of its own, only a rank equal to the spelling it stands in for.
		if _, isVariant := effortLabels[id]; !isVariant {
			rank, ok := effortRankOf(id)
			require.True(t, ok, "unreachable: id came from effortRank")
			var canonical []string
			for labelled := range effortLabels {
				if r, ranked := effortRankOf(labelled); ranked && r == rank {
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
		if id == EffortAuto {
			// Auto is not a strength -- it means "send no level at all" -- and
			// sortEffortsDescending must never see it. See its doc.
			continue
		}
		_, ranked := effortRankOf(id)
		assert.Truef(t, ranked,
			"level %q has a label but no rank, so it sorts below every ranked level, `off` included", id)
	}
}

// `enabled` is ZCode's on/off toggle for a model that offers no ladder, and its
// rank carries two obligations that are easy to break independently.
func TestEffortRank_RanksTheOnOffToggleAboveOff(t *testing.T) {
	t.Parallel()

	enabled, ok := effortRankOf("enabled")
	require.True(t, ok)
	assert.Positivef(t, enabled,
		"rank 0 means THINKING OFF: chooseDefaultEffort skips it and raiseEffortOffNone replaces it, "+
			"so a model already thinking would read as one that is not")
	assert.Less(t, effortRank["off"], enabled, "thinking on must not sort under thinking off")
	assert.Less(t, enabled, effortRank[EffortHigh],
		"it must stay under a real level, so chooseDefaultEffort prefers one whenever a ladder offers it")

	// The pick a real ladder makes must not change because `enabled` joined the
	// table: it is only ever offered beside `off`.
	assert.Equal(t, EffortHigh, chooseDefaultEffort(acpConfigOption{Options: []acpConfigOptionValue{
		{Value: "low"}, {Value: EffortHigh}, {Value: "max"},
	}}))
	assert.Equal(t, "enabled", chooseDefaultEffort(acpConfigOption{Options: []acpConfigOptionValue{
		{Value: "off"}, {Value: "enabled"},
	}}), "a toggle axis stuck at off must be raised to on, not left there")
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
				"effortRankOf lowercases before the lookup, so a capitalized key is unreachable")
		}
	}

	// Rank counts UP from the last rung, and rank 0 is the "thinking off"
	// sentinel chooseDefaultEffort and raiseEffortOffNone both test for.
	for _, level := range effortLadder[len(effortLadder)-1] {
		rank, ok := effortRankOf(level.ID)
		require.True(t, ok)
		assert.Zerof(t, rank, "%q is on the last rung, which MUST be the thinking-off rank", level.ID)
	}
	for _, rung := range effortLadder[:len(effortLadder)-1] {
		rank, _ := effortRankOf(rung[0].ID)
		assert.Positivef(t, rank, "%q is a level that thinks, so it must not carry the thinking-off rank", rung[0].ID)
	}

	// Every id on one rung ranks the same, and every rung outranks the next.
	for i, rung := range effortLadder {
		want, _ := effortRankOf(rung[0].ID)
		for _, level := range rung[1:] {
			got, ok := effortRankOf(level.ID)
			require.Truef(t, ok, "%q is on the ladder but unranked", level.ID)
			assert.Equalf(t, want, got, "%q shares a rung with %q, so it must share its rank", level.ID, rung[0].ID)
		}
		if i > 0 {
			above, _ := effortRankOf(effortLadder[i-1][0].ID)
			assert.Greaterf(t, above, want, "the ladder reads strongest first, so rung %d must outrank rung %d", i-1, i)
		}
	}

	// Codex states WHICH levels it offers; the ladder states their order.
	for id := range codexEffortIDs {
		assert.Containsf(t, seen, id, "codex offers %q, which is not on the ladder, so nothing ranks it", id)
	}
	assert.Equal(t, EffortAuto, codexDefaultEfforts[0].GetId(), "the LeapMux auto sentinel leads the menu")
	want := []string{}
	for _, id := range effortLadderIDs() {
		if codexEffortIDs[id] {
			want = append(want, id)
		}
	}
	assert.Equal(t, want, effortIDs(codexDefaultEfforts[1:]),
		"the static Codex catalog must read in ladder order")
}
