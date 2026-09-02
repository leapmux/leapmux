package agent

import (
	"sort"
	"strings"
)

// effortLevel is one reasoning-effort id and how it reads.
type effortLevel struct {
	// ID is the spelling a provider reports.
	ID string
	// Label is how that id READS. Shared by every provider: "xhigh" is "Extra
	// High" whoever offers it. Empty for a SPELLING VARIANT no menu draws under
	// that spelling -- effortLabel capitalizes such an id instead.
	Label string
}

// effortRung is every id that asks for the SAME amount of thinking. The first is
// the canonical spelling; the rest are variants a provider may report instead,
// and a menu never offers two of them at once.
type effortRung []effortLevel

// effortLadder is the ONE declaration of the reasoning-effort axis: which levels
// exist, how each one reads, and which asks for more thinking than which.
//
// STRONGEST FIRST, and the order is load-bearing three times over. It is the
// menu order Codex's static catalog renders (codexDefaultEfforts), it is the
// rank every provider's levels sort by (effortRank), and it is the label table
// every provider draws from (effortLabels). All three used to be written out
// separately and two tests existed only to hold them together; a level added
// here now moves in every one of them at once.
//
// Which ids a provider offers IS provider-specific, and so are the
// DESCRIPTIONS: Claude's mirror the CLI's own /effort copy, Codex's come from
// the live model/list response, and each `auto` sentinel identifies its own
// agent. The LABEL is not. Before this ladder, claude.go, codex.go and
// pi_settings.go each spelled it out and cursor.go special-cased it to "XHigh",
// so the four could disagree and did.
//
// `EffortAuto` is deliberately absent. It is not a strength -- it means "send no
// level at all" -- so it must never sort, and each caller that offers it puts it
// first by hand. effortLabels carries its label separately.
var effortLadder = []effortRung{
	{{ID: "ultracode", Label: "Ultracode"}},
	{{ID: "ultra", Label: "Ultra"}},
	{{ID: "max", Label: "Max"}},
	{{ID: EffortXHigh, Label: "Extra High"}, {ID: "x-high"}, {ID: "very-high"}, {ID: "very_high"}, {ID: "extra-high"}},
	{{ID: EffortHigh, Label: "High"}},
	{{ID: "medium", Label: "Medium"}, {ID: "med"}, {ID: "moderate"}, {ID: "balanced"}, {ID: "standard"}},
	{{ID: "low", Label: "Low"}},
	// `enabled` is ZCode's on/off toggle for a model that offers no ladder:
	// GLM-5-Turbo declares `enabled` and `off` where GLM-5.3 declares
	// low/high/max. It shares this rung with `minimal` -- the weakest level that
	// is still THINKING -- because the rung below is rank 0, and rank 0 means
	// thinking off: chooseDefaultEffort skips it and raiseEffortOffNone treats
	// it as the state to replace. It keeps a label of its own, because a menu
	// does draw it.
	{{ID: "minimal", Label: "Minimal"}, {ID: "min"}, {ID: "enabled", Label: "Enabled"}},
	// The floor, and rank 0. `none` and `off` ask for the same amount of
	// thinking -- none -- but a menu draws whichever word its provider reports,
	// so both keep a label. Nothing may sort below this rung.
	{{ID: "none", Label: "None"}, {ID: "off", Label: "Off"}},
}

// effortLabels is the display label for each reasoning-effort id, derived from
// effortLadder plus the `auto` sentinel that is not on it.
//
// A spelling VARIANT carries no entry. A provider reports one spelling or the
// other, never both, and effortLabel falls back to a capitalized form -- so a
// daemon that reports "x-high" reads as "X-High" rather than as a raw token,
// while the canonical "xhigh" reads "Extra High".
var effortLabels = buildEffortLabels()

func buildEffortLabels() map[string]string {
	labels := map[string]string{EffortAuto: "Auto"}
	for _, rung := range effortLadder {
		for _, level := range rung {
			if level.Label != "" {
				labels[level.ID] = level.Label
			}
		}
	}
	return labels
}

// effortLadderIDs returns the canonical id of every rung, strongest first.
//
// The menu order for a provider that spells its own catalog by hand, so that
// catalog states WHICH levels it offers and this states the order they come in.
func effortLadderIDs() []string {
	ids := make([]string, 0, len(effortLadder))
	for _, rung := range effortLadder {
		ids = append(ids, rung[0].ID)
	}
	return ids
}

// effortLabel returns the shared display label for an effort id.
//
// An id no provider has declared yet falls back to a capitalized form of the id
// itself, so a level a CLI adds mid-release still reads as a label rather than a
// raw lowercase token.
func effortLabel(id string) string {
	if name, ok := effortLabels[strings.ToLower(id)]; ok {
		return name
	}
	return capitalizeFirst(id)
}

// effortTier builds a label-only tier. Use it for a provider whose catalog
// carries no per-level description; add the Description field explicitly where
// one exists.
func effortTier(id string) *EffortInfo {
	return &EffortInfo{Id: id, Name: effortLabel(id)}
}

// sortEffortsDescending orders levels strongest first, in place, by effortRank.
//
// ONE ladder ranks every provider's levels, and it lives with the rest of the
// effort axis in `acp_effort.go`. This function exists because two element types
// carry those levels -- `*EffortInfo` for a catalog entry and
// `*leapmuxv1.AvailableOption` for an ACP config option -- and both answer
// `GetId()`. A second table would order the same word differently in two menus:
// the pair that preceded this one already disagreed about `none` against `off`,
// and only one of them ranked `x-high` and `med`.
//
// The app lists the levels STRONGEST FIRST. Codex's own catalog
// (codexDefaultEfforts) states that order by hand; this sort supplies it for the
// providers whose levels arrive as DATA and therefore in whatever order the
// source wrote them. ZCode reads them from the user's `config.json`, which ships
// GLM-5.3 as `["low", "max", "high"]`, and listing that verbatim put the weakest
// level at the top of the menu and the ladder out of order under it.
//
// A level the ladder does not rank keeps its given position relative to the
// other unranked ones and sorts BEHIND every ranked one: a level a CLI adds
// mid-release has no claim to a place in the middle of the ladder, and a stable
// sort keeps the source's own order among such levels. The comparator is a
// strict weak ordering -- ranked-before-unranked is decided directly rather than
// through "incomparable" -- so an unranked level between two ranked ones cannot
// act as a barrier that leaves the ranked pair out of order.
//
// `EffortAuto` is not on the ladder and must not be passed here. It is not a
// strength -- it means "send no level at all" -- and every caller puts it first
// by hand, ahead of whatever this returns.
func sortEffortsDescending[T interface{ GetId() string }](items []T) {
	sort.SliceStable(items, func(i, j int) bool {
		left, leftOK := effortRankOf(items[i].GetId())
		right, rightOK := effortRankOf(items[j].GetId())
		if leftOK != rightOK {
			return leftOK // a ranked level sorts before an unranked one
		}
		if !leftOK {
			return false // both unranked: keep their given order
		}
		return left > right // both ranked: strongest first
	})
}
