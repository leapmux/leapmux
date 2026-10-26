package agent

import (
	"slices"

	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
)

// defaultModelIDForList resolves which model id should carry the default badge for a
// (possibly account-specific) model list, given the list's ids, the id the list already
// designates as default (marked) and the highest-preference entry present (first) --
// both "" when the list designates none. It is the ONE place the badge ladder lives;
// withModelGroupDefaultMarked reduces the projected "model" option group to these three
// arguments on every OptionGroups read. Priority:
//  1. The explicit LEAPMUX_*_DEFAULT_MODEL operator override.
//  2. The DefaultModelSentinel entry, for a provider that reports the sentinel in
//     its own catalog (Claude Code today). It tracks the account's own default
//     across plan tiers (e.g. Sonnet vs Fable).
//  3. The provider's configured default, when the list contains it. Codex's
//     configured default is the sentinel too, and reconcileModelCatalog keeps the
//     sentinel in the live catalog, so Codex badges the sentinel at this step for
//     a stopped AND a running agent.
//  4. A default the list itself designates -- what queryAvailableModels copied
//     from the CLI's own isDefault. Reached when the configured default is absent
//     from this account's list, so a stale registry default cannot move the badge
//     off the model the CLI marked.
//  5. The highest-preference entry present, so the picker always shows a badge.
//
// Returning "" means "don't touch the list's existing default": that's the case for a
// provider with no configured default at all (ACP providers registered with nil
// defaultModels, which self-mark the currently-selected model in buildACPModels). The
// steps 4 and 5 run only for a non-empty configured default, precisely so they don't
// clobber that per-agent marking.
func (r *Registry) defaultModelIDForList(ids []string, marked, first string, provider leapmuxv1.AgentProvider) string {
	if env := r.DefaultModelEnvOverride(provider); env != "" {
		// Honor the operator override only when it actually names a model in this
		// (possibly account-specific) list, matching by exact id or provider-
		// normalized alias. A stale or differently-spelled override -- a fully-
		// qualified "claude-opus-4-8[1m]" against the catalog's "opus[1m]", or a
		// model the account simply doesn't offer -- falls through to the rest of
		// the ladder so the picker still shows a default badge. Returning an absent
		// id unconditionally would leave withModelGroupDefaultMarked pointing the
		// group's DefaultValue at an id none of its options carry, badging nothing.
		//
		// This step outranks the sentinel (step 2) deliberately: an explicit operator
		// override naming the account's resolved concrete model (which
		// ensureSettledModelListed surfaces into the list once startup settles it)
		// badges that concrete model rather than the "default" placeholder -- the
		// sentinel does not retain the badge once a concrete identity the operator
		// pinned is present.
		if id := r.matchModelID(ids, provider, env); id != "" {
			return id
		}
	}
	// Each provider states for itself whether its catalog reports the sentinel, so
	// this ladder stays provider-neutral. Only Claude Code answers true today.
	if r.Plugin(provider).ReportsDefaultModelSentinel() && slices.Contains(ids, DefaultModelSentinel) {
		return DefaultModelSentinel
	}
	configured := r.DefaultModel(provider)
	if configured == "" {
		// No configured default: preserve whatever IsDefault the per-agent list
		// already set (e.g. buildACPModels marking the current model) instead of
		// moving the badge to the first entry.
		return ""
	}
	if slices.Contains(ids, configured) {
		return configured
	}
	// The configured default isn't in this (account-specific) list. Respect a
	// default the provider already designated on the list itself -- e.g. Codex's
	// queryAvailableModels copies the CLI's isDefault onto an entry -- before
	// falling back, so a stale registry default (configured but absent from the
	// live list) doesn't move the badge off the model the CLI actually marked.
	if marked != "" {
		return marked
	}
	// Nothing designated: mark the highest-preference entry the list does contain
	// so the picker always shows a default badge (e.g. a Claude CLI reporting
	// concrete models but no "default" sentinel falls back to its first model).
	return first
}

// matchModelID returns the id in the list the given id refers to, matched first by exact id
// then by provider-normalized alias (so a fully-qualified spelling like "claude-opus-4-8[1m]"
// resolves to the catalog's "opus[1m]"). Returns "" when the list contains no such id. Used
// to resolve an operator default-model override against an account-specific catalog.
func (r *Registry) matchModelID(ids []string, provider leapmuxv1.AgentProvider, id string) string {
	if slices.Contains(ids, id) {
		return id
	}
	want := r.NormalizeModelID(provider, id)
	for _, mid := range ids {
		if r.NormalizeModelID(provider, mid) == want {
			return mid
		}
	}
	return ""
}

// optionGroupsFromCached projects a not-running (or transiently-empty-live) agent's cached catalog
// into the served option groups: the cache as-is when it is usable for the requested model, a
// model-dependent rebuild when ONLY the per-model groups are stale, else the static fallback.
// Shared by OptionGroups (which sources `cached` from the shared per-agent cache) and
// OptionGroupsForRow (which sources it from a caller's own row snapshot), so both resolve a
// not-running catalog identically. Does NOT mark the model default -- the caller does.
func (r *Registry) optionGroupsFromCached(cached cachedCatalog, provider leapmuxv1.AgentProvider, currentModel string) []*leapmuxv1.AvailableOptionGroup {
	switch {
	case r.cachedCatalogUsable(cached, currentModel, provider):
		return cached.groups
	case len(cached.groups) > 0 && r.providerHasModelDependentGroups(provider) && currentModel != "":
		// The cache exists but is stale ONLY by model: its per-model effort/thinking groups were
		// built for a different model (an offline model edit moved the model column without
		// re-persisting the catalog -- see optionGroupsView). Rebuild just those for currentModel
		// and keep every other cached group, so dynamically-discovered model-INDEPENDENT groups
		// (Claude's Output Style / Fast Mode, surfaced only at runtime and absent from the static
		// templates) and any live-filtered group survive the edit instead of vanishing from the
		// settings popover until relaunch.
		return r.withModelDependentGroupsRebuilt(cached.groups, provider, currentModel)
	default:
		return r.fallbackOptionGroups(provider, currentModel)
	}
}

// ensureModelGroup guarantees the catalog surfaces the model axis when the row knows a model but
// the projected groups carry no model group -- a dynamic-model ACP provider configured with a
// LEAPMUX_*_DEFAULT_MODEL override but never run, so it has no discovered model list to build a
// selectable group from. It prepends a read-only model group carrying that value, so the stored
// model stays visible to a by-id reader (the remote CLI reads the model from the option groups,
// not the options map). A no-op when a model group already exists or no model is known.
func ensureModelGroup(groups []*leapmuxv1.AvailableOptionGroup, currentModel string) []*leapmuxv1.AvailableOptionGroup {
	if currentModel == "" || optionids.GroupByID(groups, OptionIDModel) != nil {
		return groups
	}
	model := ReadOnlyValueGroup(OptionIDModel, ModelGroupLabel, OptionOrderModel, currentModel, "")
	return append([]*leapmuxv1.AvailableOptionGroup{model}, groups...)
}

// cachedCatalogUsable reports whether the cached catalog can be served for the requested
// model, rather than falling through to the static fallback (rebuilt for the requested
// model). It is NOT usable -- a model-dependent provider's cache is stale -- for a
// MODEL-DEPENDENT provider (Claude/Codex/Pi -- per-model effort tiers, Claude's extended
// thinking) whenever the requested model is known and the cache's stamp doesn't match it:
// either a concrete-but-different stamp (an offline model edit) OR an UNSTAMPED cache
// (model == "", e.g. a static-fallback catalog persisted before a model resolved, whose
// effort group was built for the provider default). Serving such a cache would show the
// wrong model's effort tiers.
//
// A stale-by-model cache is NOT discarded wholesale: OptionGroups rebuilds only the
// model-dependent groups for the requested model (withModelDependentGroupsRebuilt) and keeps
// the rest, because these providers CAN carry dynamically-discovered model-INDEPENDENT groups
// that live only in the cache -- Claude surfaces Output Style (from availableOutputStyles) and
// Fast Mode at runtime, neither of which fallbackOptionGroups reproduces. Dropping the
// whole cache for the bare static fallback would lose those groups from the popover until the
// agent relaunches. The caller then overlays the persisted currents.
//
// Providers WITHOUT model-dependent groups -- the ACP permission-mode / primary-agent
// providers, whose effort/reasoning axes ARE model-independent server-driven config options
// living only in the cache -- always keep serving the cache across a model edit (the caller
// overlays the new model as current); falling through to their degenerate static fallback
// would silently drop those option groups from the popover until relaunch. An unknown
// requested model (currentModel == "") is also trusted as-is for everyone.
func (r *Registry) cachedCatalogUsable(cached cachedCatalog, currentModel string, provider leapmuxv1.AgentProvider) bool {
	return len(cached.groups) > 0 &&
		(currentModel == "" || cached.model == currentModel || !r.providerHasModelDependentGroups(provider))
}

// providerHasModelDependentGroups reports whether a provider's catalog carries
// model-dependent sub-groups (per-model effort tiers, and for Claude the per-model
// extended-thinking group) that must be rebuilt when the model changes. A provider has
// them exactly when it owns a model-dependent effort catalog -- Registry.ManagesEffort
// (Claude/Codex/Pi, and native Copilot, whose account decides both the models and each
// model's effort tiers). The ACP permission-mode / primary-agent providers do NOT:
// although every provider shares the default EffortSubGroups builder, it produces nothing
// for a model with no SupportedEfforts, and their effort/reasoning axes are
// model-independent server-driven config options -- so a model change doesn't invalidate
// any cached group, and falling through to the static fallback would needlessly drop
// those config options.
func (r *Registry) providerHasModelDependentGroups(provider leapmuxv1.AgentProvider) bool {
	return r.ManagesEffort(provider)
}

// modelDependentGroups builds the model group plus the per-model sub-groups for currentModel:
// its effort tiers, and for Claude the extended-thinking group whose label ("Adaptive" vs "On")
// is per model. These are exactly the groups that must be rebuilt when the selected model
// changes; the provider's static option-group templates (sandbox/network/permission/...) and
// any dynamically-discovered group are model-INDEPENDENT and handled separately. Current values
// are empty here; the caller overlays DB selections. Returns nil for an unknown provider.
func (r *Registry) modelDependentGroups(provider leapmuxv1.AgentProvider, currentModel string) []*leapmuxv1.AvailableOptionGroup {
	reg, ok := r.byProvider[provider]
	if !ok {
		return nil
	}
	if currentModel == "" {
		currentModel = r.DefaultModel(provider)
	}
	var groups []*leapmuxv1.AvailableOptionGroup
	if mg := ModelOptionGroup(reg.DefaultModels, "", reg.ModelSubGroups); mg != nil {
		groups = append(groups, mg)
		// Emit the current model's model-dependent groups (its effort tiers, and
		// for Claude the extended-thinking group whose label is per model) as
		// top-level groups. This fallback is served while an agent is restarting
		// (not registered): without these, the settings popover briefly loses the
		// effort/thinking groups mid-restart, which flickers and can race a click.
		if reg.ModelSubGroups != nil {
			if m := FindAvailableModel(reg.DefaultModels, currentModel); m != nil {
				groups = append(groups, reg.ModelSubGroups(m)...)
			}
		}
	}
	return groups
}

// fallbackOptionGroups builds the fallback option groups for a provider
// that is not running and has no cached catalog: the model-dependent groups for
// currentModel (the agent's selected model, defaulting to the provider default when
// unknown -- so a Haiku agent shows no effort group while a Sonnet agent shows Sonnet's
// tiers), followed by the provider's static option-group templates. Current values are
// empty here; the caller overlays DB selections.
func (r *Registry) fallbackOptionGroups(provider leapmuxv1.AgentProvider, currentModel string) []*leapmuxv1.AvailableOptionGroup {
	reg, ok := r.byProvider[provider]
	if !ok {
		return nil
	}
	return append(r.modelDependentGroups(provider, currentModel), reg.OptionGroups...)
}

// withModelDependentGroupsRebuilt returns the cached catalog with its model-dependent groups
// (model, effort, and per-model sub-groups) rebuilt for currentModel, preserving every OTHER
// cached group untouched. Served on an OFFLINE model edit of a model-dependent provider
// (Claude/Codex/Pi): the cached catalog's per-model effort/thinking groups were built for the
// PRIOR model and are stale, but the catalog also carries dynamically-discovered, model-
// INDEPENDENT groups (Claude's Output Style / Fast Mode, surfaced only at runtime and absent
// from the static templates) and any live-filtered group. Falling through to the bare static
// fallback would drop those until the agent relaunches; this swaps only the stale per-model
// groups and keeps the rest. The caller overlays the persisted currents afterward.
//
// The cached model group is the first source of the new model's groups. It is the catalog of the
// last live run: it lists the models that the CLI reported, and each model option carries its own
// sub_groups. The static seed is only a fallback for a catalog that states no per-model groups,
// or does not list the new model. The seed can differ from the live list, and Pi and Cline seed a
// single placeholder model. A rebuild from the seed alone replaces the live model list with the
// seed, and it cannot describe a live model at all.
//
// The rebuild drops a stale group that the new model does not carry (the effort group after a
// switch to a model with no effort axis), but only when a source lists the new model. When no
// source lists it, the existing groups stay, as the browser does for the same case
// (withSelectedModelSubGroups).
func (r *Registry) withModelDependentGroupsRebuilt(cached []*leapmuxv1.AvailableOptionGroup, provider leapmuxv1.AgentProvider, currentModel string) []*leapmuxv1.AvailableOptionGroup {
	fresh := r.cachedModelDependentGroups(cached, provider, currentModel)
	known := fresh != nil
	if !known {
		fresh = r.modelDependentGroups(provider, currentModel)
		known = r.staticModelListed(provider, currentModel)
	}
	if len(fresh) == 0 {
		return cached
	}
	freshByID := make(map[string]*leapmuxv1.AvailableOptionGroup, len(fresh))
	for _, g := range fresh {
		freshByID[g.GetId()] = g
	}
	// A group that varies by model is a sub_group of some model option, in the source that built
	// `fresh` or in the cached catalog.
	dependent := subGroupIDs(optionids.GroupByID(fresh, OptionIDModel))
	for id := range subGroupIDs(optionids.GroupByID(cached, OptionIDModel)) {
		dependent[id] = true
	}
	out := make([]*leapmuxv1.AvailableOptionGroup, 0, len(cached)+len(fresh))
	rebuilt := make(map[string]bool, len(fresh))
	for _, g := range cached {
		if f, ok := freshByID[g.GetId()]; ok {
			out = append(out, f)
			rebuilt[g.GetId()] = true
		} else if known && dependent[g.GetId()] {
			continue
		} else {
			out = append(out, g)
		}
	}
	// A model-dependent group the stale cache lacked (e.g. the new model offers an effort
	// group the prior model had none of) must still appear; order is irrelevant since the
	// frontend sorts by each group's Order field.
	for _, f := range fresh {
		if !rebuilt[f.GetId()] {
			out = append(out, f)
		}
	}
	return out
}

// cachedModelDependentGroups returns what modelDependentGroups returns, read from the cached
// model group: the model group without a current value, then the sub_groups of the option for
// currentModel. It returns nil when the cached catalog cannot state them, so that the caller falls
// back to the static seed. That holds when the catalog has no model group, when no model option
// carries sub_groups (a stub, or a provider that keeps no per-model groups), and when no option
// matches currentModel. An option that matches and carries no sub_groups is an answer: that model
// has no model-dependent groups.
func (r *Registry) cachedModelDependentGroups(cached []*leapmuxv1.AvailableOptionGroup, provider leapmuxv1.AgentProvider, currentModel string) []*leapmuxv1.AvailableOptionGroup {
	mg := optionids.GroupByID(cached, OptionIDModel)
	if len(subGroupIDs(mg)) == 0 {
		return nil
	}
	var option *leapmuxv1.AvailableOption
	for _, o := range mg.GetOptions() {
		if o.GetId() == currentModel {
			option = o
			break
		}
	}
	if option == nil {
		want := r.NormalizeModelID(provider, currentModel)
		for _, o := range mg.GetOptions() {
			if r.NormalizeModelID(provider, o.GetId()) == want {
				option = o
				break
			}
		}
	}
	if option == nil {
		return nil
	}
	model := proto.Clone(mg).(*leapmuxv1.AvailableOptionGroup)
	model.CurrentValue = ""
	return append([]*leapmuxv1.AvailableOptionGroup{model}, option.GetSubGroups()...)
}

// staticModelListed reports whether the static seed of the provider lists model. The seed states
// the model-dependent groups of a listed model, so a group that such a model lacks is really absent.
func (r *Registry) staticModelListed(provider leapmuxv1.AgentProvider, model string) bool {
	reg, ok := r.byProvider[provider]
	if !ok {
		return false
	}
	if model == "" {
		model = r.DefaultModel(provider)
	}
	return FindAvailableModel(reg.DefaultModels, model) != nil
}

// subGroupIDs collects the ids of the groups that the options of a model group carry as
// sub_groups. Each id names a group that varies by model.
func subGroupIDs(modelGroup *leapmuxv1.AvailableOptionGroup) map[string]bool {
	ids := map[string]bool{}
	for _, o := range modelGroup.GetOptions() {
		for _, g := range o.GetSubGroups() {
			ids[g.GetId()] = true
		}
	}
	return ids
}

// withModelGroupDefaultMarked re-derives the "model" group's DefaultValue via the
// LEAPMUX_*_DEFAULT_MODEL / sentinel / configured-default ladder (defaultModelIDForList).
// Returns the input unchanged when there is no model group or the default is
// already correct; otherwise returns a copy with only the model group replaced,
// leaving the shared catalog groups untouched.
func (r *Registry) withModelGroupDefaultMarked(groups []*leapmuxv1.AvailableOptionGroup, provider leapmuxv1.AgentProvider) []*leapmuxv1.AvailableOptionGroup {
	mg := optionids.GroupByID(groups, OptionIDModel)
	if mg == nil || len(mg.GetOptions()) == 0 {
		return groups
	}
	ids := make([]string, 0, len(mg.GetOptions()))
	for _, o := range mg.GetOptions() {
		ids = append(ids, o.GetId())
	}
	// The group's existing DefaultValue is the already-designated default (the option flagged
	// default), but only when it is actually one of the options; ids[0] is the highest-
	// preference present entry. Reducing the group to these three ids is what lets the hot
	// OptionGroups read path share the ladder without a proto<->ModelInfo round-trip.
	marked := ""
	if slices.Contains(ids, mg.GetDefaultValue()) {
		marked = mg.GetDefaultValue()
	}
	def := r.defaultModelIDForList(ids, marked, ids[0], provider)
	if def == "" || def == mg.GetDefaultValue() {
		return groups
	}
	out := make([]*leapmuxv1.AvailableOptionGroup, len(groups))
	for i, g := range groups {
		if g.GetId() == OptionIDModel {
			c := proto.Clone(g).(*leapmuxv1.AvailableOptionGroup)
			c.DefaultValue = def
			out[i] = c
		} else {
			out[i] = g
		}
	}
	return out
}
