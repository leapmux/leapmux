package agent

import (
	"sort"
	"strings"
)

// effortLabels is the display label for each reasoning-effort id, shared by
// every provider.
//
// Which ids a provider offers IS provider-specific, and so are the
// DESCRIPTIONS: Claude's mirror the CLI's own /effort copy, Codex's come from
// the live model/list response, and each `auto` sentinel identifies its own agent.
// The LABEL is not -- "xhigh" reads "Extra High" whoever offers it. Before this
// table claude.go, codex.go and pi_settings.go each spelled it out and cursor.go
// special-cased it to "XHigh", so the four could disagree and did: correcting
// one of them by hand is exactly what this table makes unnecessary.
var effortLabels = map[string]string{
	EffortAuto: "Auto",
	"off":      "Off",
	"none":     "None",
	// ZCode's on/off toggle for a model that offers no ladder: GLM-5-Turbo
	// declares `enabled` and `off` where GLM-5.3 declares low/high/max.
	"enabled":   "Enabled",
	"minimal":   "Minimal",
	"low":       "Low",
	"medium":    "Medium",
	EffortHigh:  "High",
	EffortXHigh: "Extra High",
	"max":       "Max",
	"ultra":     "Ultra",
	"ultracode": "Ultracode",
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

// effortStrength ranks a reasoning level by how much thinking it asks for.
//
// The app lists the levels STRONGEST FIRST. Codex's own catalog
// (codexDefaultEfforts) states that order by hand, and this table repeats its
// ladder for the providers whose levels arrive as DATA and therefore in whatever
// order the source wrote them: ZCode reads them from the user's `config.json`,
// which ships GLM-5.3 as `["low", "max", "high"]`, and listing that verbatim put
// the weakest level at the top of the menu and the ladder out of order under it.
//
// Only the order matters, not the numbers; the gaps leave room for a level a CLI
// adds between two of these.
//
// `enabled` is ZCode's on/off toggle for a model that offers no tiers
// (GLM-5-Turbo gives `enabled` and `off`). Only its position ABOVE `off` is
// load-bearing -- a model offers the ladder or the toggle, never both.
var effortStrength = map[string]int{
	"ultracode": 100,
	"ultra":     90,
	"max":       80,
	EffortXHigh: 70,
	EffortHigh:  60,
	"medium":    50,
	"low":       40,
	"enabled":   30,
	"minimal":   20,
	"none":      10,
	"off":       0,
}

// sortEffortsByStrength orders levels strongest first, in place.
//
// A level the ladder does not rank keeps its given position relative to the
// other unranked ones and sorts BEHIND every ranked one: a level a CLI adds
// mid-release has no claim to a place in the middle of the ladder, and a stable
// sort keeps the source's own order among such levels.
//
// `EffortAuto` is not on the ladder and must not be passed here. It is not a
// strength -- it means "send no level at all" -- and every caller puts it first
// by hand, ahead of whatever this returns.
func sortEffortsByStrength(efforts []*EffortInfo) {
	sort.SliceStable(efforts, func(i, j int) bool {
		left, leftOK := effortStrength[strings.ToLower(efforts[i].GetId())]
		right, rightOK := effortStrength[strings.ToLower(efforts[j].GetId())]
		if leftOK != rightOK {
			return leftOK
		}
		if !leftOK {
			return false
		}
		return left > right
	})
}
