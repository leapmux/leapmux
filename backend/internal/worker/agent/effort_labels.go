package agent

import "strings"

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
	EffortAuto:  "Auto",
	"off":       "Off",
	"none":      "None",
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
