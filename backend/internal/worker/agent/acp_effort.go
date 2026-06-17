package agent

// This file is the single home for the ACP reasoning-effort axis: how an effort config option is
// recognized (by the `thought_level` category or a provider-convention id), how its values are
// ranked and ordered strongest-first, how a "none"/"off" default is raised to a real level on a
// model switch, and how the env-effort override is mapped onto the daemon's actual axis id. The
// catalog-effort projection for the model-dependent providers (Claude/Codex/Pi) lives separately
// in options.go; this file covers only the server-driven ACP config-option axis.

import (
	"maps"
	"slices"
	"sort"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// acpEffortConfigOptionIDs are the well-known ids ACP providers use for a reasoning-effort
// axis: OpenCode/Kilo "effort", Copilot "reasoning_effort", Goose "thinking_effort". They
// are the id-fallback for the effort axis, mirroring the model/mode channels' well-known-id
// fallback (acpConfigOptionIDModel/Mode) -- so the effort override and the strongest-first
// sort fire whether or not the daemon sets the `thought_level` category.
var acpEffortConfigOptionIDs = []string{OptionIDEffort, CopilotConfigReasoningEffort, GooseConfigThinkingEffort}

// isEffortConfigOption reports whether option is a reasoning-effort axis -- by its ACP
// `category` ("thought_level"), or, for a provider that omits category, by a well-known
// effort id. Mirrors acpConfigOptionByCategory's category-then-id matching so the effort
// override (raiseEffortOffNone) and the strongest-first sort (buildOptionGroup) never depend
// on the daemon supplying category when its id already identifies the axis.
func isEffortConfigOption(option acpConfigOption) bool {
	return option.Category == acpConfigOptionCategoryThoughtLevel ||
		slices.Contains(acpEffortConfigOptionIDs, option.ID)
}

// acpEffortConfigOption returns the selectable reasoning-effort axis from a configOptions
// payload (matched by isEffortConfigOption), or a zero option and false when none is present.
func acpEffortConfigOption(options []acpConfigOption) (acpConfigOption, bool) {
	for _, option := range options {
		if isEffortConfigOption(option) && isSelectableConfigOption(option) {
			return option, true
		}
	}
	return acpConfigOption{}, false
}

// effortRank ranks reasoning-effort option values by intensity (higher = more effort),
// so a thought_level select can be ordered strongest-first. Covers the standard CLI
// names plus their common separator/spelling variants; a value absent here sorts after
// every ranked value (see sortEffortOptionsDescending). Keys are lowercase; lookups go
// through effortRankOf, which lowercases first so a server reporting "High"/"LOW" is
// still ranked rather than dumped into the unranked tail.
var effortRank = map[string]int{
	"off": 0, "none": 0,
	"minimal": 1, "min": 1,
	"low":    2,
	"medium": 3, "med": 3, "moderate": 3, "balanced": 3, "standard": 3,
	"high":  4,
	"xhigh": 5, "x-high": 5, "very-high": 5, "very_high": 5, "extra-high": 5,
	"max":       6,
	"ultracode": 7,
}

// effortRankOf looks up a value's intensity rank case-insensitively. Returns
// (rank, true) when the (lowercased) value is known, (0, false) otherwise.
func effortRankOf(id string) (int, bool) {
	r, ok := effortRank[strings.ToLower(id)]
	return r, ok
}

// sortEffortOptionsDescending reorders effort options in place strongest-first by
// effortRank. A value absent from effortRank (e.g. a provider-specific variant like
// "default") sorts after every ranked value, preserving its order among other
// unranked values.
//
// The comparator is a strict weak ordering -- ranked-before-unranked is decided
// directly rather than via "incomparable" -- so an unranked value sitting between two
// ranked ones can no longer act as a barrier that leaves the ranked pair mis-ordered
// (e.g. [low, default, high] now yields [high, low, default], not [low, default, high]).
func sortEffortOptionsDescending(opts []*leapmuxv1.AvailableOption) {
	sort.SliceStable(opts, func(i, j int) bool {
		ri, oki := effortRankOf(opts[i].GetId())
		rj, okj := effortRankOf(opts[j].GetId())
		if oki != okj {
			return oki // a ranked value sorts before an unranked one
		}
		if !oki {
			return false // both unranked: keep their relative (server-reported) order
		}
		return ri > rj // both ranked: strongest first
	})
}

// chooseDefaultEffort picks the reasoning-effort value to install when a model first
// surfaces its effort axis at the daemon's "none"/"off" default (see raiseEffortOffNone).
// A surfaced "none" leaves the strongest models reasoning-disabled, so we replace it with
// a strong, sensible level: "high" when the axis offers it, otherwise the offered level
// CLOSEST TO "high", breaking ties toward the higher (stronger) rank. ("high if offered"
// is simply the closest-to-high pick at distance 0, so one rule expresses both.)
//
// Only ranked levels above none/off are considered -- an unranked provider-specific value
// (e.g. "default") is skipped, and "none"/"off" are never chosen (that is the value we are
// replacing). Returns "" when the axis offers no such level, so the caller leaves the
// daemon's value untouched rather than inventing one.
func chooseDefaultEffort(option acpConfigOption) string {
	highRank := effortRank["high"]
	closestValue := ""
	closestRank := -1
	for _, o := range option.Options {
		rank, ok := effortRankOf(o.Value)
		if !ok || rank == 0 {
			continue // skip none/off and unranked provider-specific values
		}
		if closestRank == -1 || effortCloserToHigh(rank, closestRank, highRank) {
			closestRank, closestValue = rank, o.Value
		}
	}
	return closestValue
}

// effortCloserToHigh reports whether candidate rank a is a better "closest to high" pick
// than the incumbent b: the rank nearer high wins, and an exact tie breaks toward the
// higher (stronger) rank.
func effortCloserToHigh(a, b, high int) bool {
	da, db := a-high, b-high
	if da < 0 {
		da = -da
	}
	if db < 0 {
		db = -db
	}
	if da != db {
		return da < db
	}
	return a > b // tie -> stronger level
}

// thoughtLevelConfigOptionID returns the advertised config-option id of a reasoning-effort axis a
// daemon tags with the ACP `thought_level` category under a NON-"effort" id, or "" when none is
// advertised. startupEffortConfigID uses it to map the well-known env-effort override (stored under
// "effort") onto a generic daemon's spec-categorized effort axis that no provider declares
// explicitly via effortConfigID. It matches by category ALONE -- a provider-convention id like
// reasoning_effort / thinking_effort is declared on acpBase instead, so a well-known effort id the
// running provider did not claim can't be mistaken for its axis and get the override double-pushed.
// Iterates templates in sorted id order so a (pathological) daemon tagging two axes thought_level
// resolves deterministically. Caller holds the owning acpBase.mu.
func (g *optionState) thoughtLevelConfigOptionID() string {
	for _, id := range slices.Sorted(maps.Keys(g.templates)) {
		if id != OptionIDEffort && g.templates[id].Category == acpConfigOptionCategoryThoughtLevel {
			return id
		}
	}
	return ""
}

// startupEffortConfigID resolves the daemon config-option id the env-effort override (stored
// under the well-known "effort") maps onto: the provider's declared effortConfigID, or -- for a
// generic daemon no provider wires explicitly -- a thought_level-categorized axis discovered from
// the live option set. It deliberately does NOT match by well-known effort id: a well-known id the
// provider did not declare is not authoritatively its effort axis (it may be a coincidental second
// axis), and mapping the override onto it would double-push. Returns "" when the axis is "effort"
// (mapped directly in applyStartupOptions's loop) or absent. The declared id is immutable, so the
// common case takes no lock; only the generic-daemon fallback reads templates under b.mu.
func (b *acpBase) startupEffortConfigID() string {
	if b.effortConfigID != "" {
		return b.effortConfigID
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.options.thoughtLevelConfigOptionID()
}

// raiseEffortOffNone replaces a reasoning-effort axis sitting at the daemon's "none"/"off"
// default with a strong, sensible level (chooseDefaultEffort), pushing the choice through
// session/set_config_option so the running session and the surfaced group agree -- a
// display-only rewrite would leave the daemon reasoning-disabled while the UI claimed
// otherwise. options is the configOptions payload just folded by the model write; the
// effort axis is matched by its ACP `category` ("thought_level") or a well-known effort id
// (isEffortConfigOption), the signal shared by every provider that has one (OpenCode/Kilo
// "effort", Copilot "reasoning_effort", Goose "thinking_effort").
//
// Scope: only the model-write path (setModelViaConfigOption) calls this, so the override
// fires when a model SWITCH surfaces or resets the axis -- never on an explicit effort edit
// (those go through setConfigOption), so a user deliberately selecting "none" is honored. On
// a ClearContext reapply the model is re-pushed first (raising "none" here), then
// reapplyOptions re-pushes the stored selection on top, so a persisted choice still wins.
// A no-op unless the axis surfaced at a known rank-0 value ("none"/"off"); an empty or any
// real level is left untouched.
func (b *acpBase) raiseEffortOffNone(options []acpConfigOption) {
	option, ok := acpEffortConfigOption(options)
	if !ok || option.ID == "" {
		return
	}
	// The target is a pure function of the just-folded payload (the axis's offered levels), so
	// compute it before taking any lock; an axis offering no real level above none/off yields "".
	target := chooseDefaultEffort(option)
	if target == "" {
		return
	}
	// Serialize the effort push against the option-write batches (applyOptionUpdates /
	// reapplyOptions / applyStartupOptions): without optionWriteMu a concurrent batch's write
	// to this same axis could interleave with the check-write below and leave the daemon
	// on the loser's value. optionWriteMu orders before b.mu (the documented lock order); the
	// RPC inside applyConfigOptionGuarded re-locks b.mu per call, so b.mu is released before the
	// push. No caller holds optionWriteMu when reaching the model write (UpdateSettings/reapply run
	// the model write before their own batch), so this never self-deadlocks.
	b.optionWriteMu.Lock()
	defer b.optionWriteMu.Unlock()
	// Re-check the rank-0 "none"/"off" precondition under the WRITE's OWN b.mu acquisition (inside
	// setConfigOptionGuarded, right before the send) rather than a separate earlier read -- this is
	// the latest point we can evaluate it without holding b.mu across the RPC. A server-initiated
	// config_option_update (handleACPConfigOptionUpdate takes b.mu) that folds a real level either
	// lands before this precondition runs (so it skips the raise and respects the daemon's level) or
	// after the send (the daemon's own ordering resolves the two writes). effortRankOf reports
	// (0, true) for none/off and (0, false) for an empty/unsurfaced or unranked current, so the
	// rank-0 test matches none/off exactly and leaves every real level (or an unsurfaced axis)
	// alone; target==current short-circuits an already-correct axis. The async RPC send itself
	// remains an irreducible window we deliberately do not close by holding b.mu across it.
	//
	// Best-effort, like the other startup/reapply config-option writes: a rejected write is
	// logged and the session keeps the daemon's value rather than aborting the model switch.
	b.applyConfigOptionGuarded(option.ID, target, func() bool {
		current := b.options.values[option.ID] // caller holds b.mu
		if current == target {
			return false
		}
		rank, ok := effortRankOf(current)
		return ok && rank == 0
	})
}
