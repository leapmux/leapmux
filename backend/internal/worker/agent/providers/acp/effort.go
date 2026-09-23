package acp

// This file is the single home for the ACP reasoning-effort axis: how an effort config option is
// recognized (by the `thought_level` category, the well-known "effort" id, or the id the running
// provider declares), how its values are
// ranked and ordered strongest-first, how a "none"/"off" default is raised to a real level on a
// model switch, and how the env-effort override is mapped onto the daemon's actual axis id. The
// catalog-effort projection for the model-dependent providers (Claude/Codex/Pi) lives separately
// in options.go; this file covers only the server-driven ACP config-option axis.

import (
	"maps"
	"slices"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// isEffortConfigOption reports whether option is a reasoning-effort axis -- by its ACP
// `category` ("thought_level"), or, for a provider that omits category, by its id: the
// well-known "effort" (OpenCode, Kilo) or the provider-convention id the running provider
// declares as effortConfigID (Goose "thinking_effort"; "" when it declares none). Mirrors
// acpConfigOptionByCategory's category-then-id matching so the effort override
// (raiseEffortOffNone) and the strongest-first sort (buildOptionGroup) never depend on the
// daemon supplying category when its id already identifies the axis.
//
// The convention id is the provider's own, so ACP code names none of them. An uncategorized
// option under another provider's convention id is therefore not an effort axis here: Cursor
// or Reasonix would not sort or raise a `thinking_effort` option, and no daemon sends one.
func isEffortConfigOption(option ConfigOption, effortConfigID string) bool {
	return option.Category == acpConfigOptionCategoryThoughtLevel ||
		option.ID == agent.OptionIDEffort ||
		(effortConfigID != "" && option.ID == effortConfigID)
}

// acpEffortConfigOption returns the selectable reasoning-effort axis from a configOptions
// payload (matched by isEffortConfigOption), or a zero option and false when none is present.
func acpEffortConfigOption(options []ConfigOption, effortConfigID string) (ConfigOption, bool) {
	for _, option := range options {
		if isEffortConfigOption(option, effortConfigID) && isSelectableConfigOption(option) {
			return option, true
		}
	}
	return ConfigOption{}, false
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
func chooseDefaultEffort(option ConfigOption) string {
	highRank := providerkit.EffortRank["high"]
	closestValue := ""
	closestRank := -1
	for _, o := range option.Options {
		rank, ok := providerkit.EffortRankOf(o.Value)
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
// reasoning_effort / thinking_effort is declared on Base instead, so a well-known effort id the
// running provider did not claim can't be mistaken for its axis and get the override double-pushed.
// Iterates templates in sorted id order so a (pathological) daemon tagging two axes thought_level
// resolves deterministically. Caller holds the owning Base.Mu.
func (g *optionState) thoughtLevelConfigOptionID() string {
	for _, id := range slices.Sorted(maps.Keys(g.templates)) {
		if id != agent.OptionIDEffort && g.templates[id].Category == acpConfigOptionCategoryThoughtLevel {
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
// common case takes no lock; only the generic-daemon fallback reads templates under b.Mu.
func (b *Base) startupEffortConfigID() string {
	if b.hooks.EffortConfigID != "" {
		return b.hooks.EffortConfigID
	}
	b.Mu.Lock()
	defer b.Mu.Unlock()
	return b.options.thoughtLevelConfigOptionID()
}

// raiseEffortOffNone replaces a reasoning-effort axis sitting at the daemon's "none"/"off"
// default with a strong, sensible level (chooseDefaultEffort), pushing the choice through
// session/set_config_option so the running session and the surfaced group agree -- a
// display-only rewrite would leave the daemon reasoning-disabled while the UI claimed
// otherwise. options is the configOptions payload just folded by the model write; the
// effort axis is matched by its ACP `category` ("thought_level"), the well-known "effort" id,
// or the provider's declared effortConfigID (isEffortConfigOption), the signal shared by every
// provider that has one (OpenCode and Kilo "effort", Goose "thinking_effort").
//
// Scope: only the model-write path (SetModelViaConfigOption) calls this, so the override
// fires when a model SWITCH surfaces or resets the axis -- never on an explicit effort edit
// (those go through setConfigOption), so a user deliberately selecting "none" is honored. On
// a ClearContext reapply the model is re-pushed first (raising "none" here), then
// reapplyOptions re-pushes the stored selection on top, so a persisted choice still wins.
// A no-op unless the axis surfaced at a known rank-0 value ("none"/"off"); an empty or any
// real level is left untouched.
func (b *Base) raiseEffortOffNone(options []ConfigOption) {
	option, ok := acpEffortConfigOption(options, b.hooks.EffortConfigID)
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
	// on the loser's value. optionWriteMu orders before b.Mu (the documented lock order); the
	// RPC inside applyConfigOptionGuarded re-locks b.Mu per call, so b.Mu is released before the
	// push. No caller holds optionWriteMu when reaching the model write (UpdateSettings/reapply run
	// the model write before their own batch), so this never self-deadlocks.
	b.optionWriteMu.Lock()
	defer b.optionWriteMu.Unlock()
	// Re-check the rank-0 "none"/"off" precondition under the WRITE's OWN b.Mu acquisition (inside
	// setConfigOptionGuarded, right before the send) rather than a separate earlier read -- this is
	// the latest point we can evaluate it without holding b.Mu across the RPC. A server-initiated
	// config_option_update (handleACPConfigOptionUpdate takes b.Mu) that folds a real level either
	// lands before this precondition runs (so it skips the raise and respects the daemon's level) or
	// after the send (the daemon's own ordering resolves the two writes). providerkit.EffortRankOf reports
	// (0, true) for none/off and (0, false) for an empty/unsurfaced or unranked current, so the
	// rank-0 test matches none/off exactly and leaves every real level (or an unsurfaced axis)
	// alone; target==current short-circuits an already-correct axis. The async RPC send itself
	// remains an irreducible window we deliberately do not close by holding b.Mu across it.
	//
	// Best-effort, like the other startup/reapply config-option writes: a rejected write is
	// logged and the session keeps the daemon's value rather than aborting the model switch.
	b.applyConfigOptionGuarded(option.ID, target, func() bool {
		current := b.options.values[option.ID] // caller holds b.Mu
		if current == target {
			return false
		}
		rank, ok := providerkit.EffortRankOf(current)
		return ok && rank == 0
	})
}
