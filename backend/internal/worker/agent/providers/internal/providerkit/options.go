package providerkit

import (
	"github.com/leapmux/leapmux/internal/worker/agent"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// LiveGroup overlays an agent's confirmed current value onto a provider's static
// option-group template. Used by providers that define their option lists
// statically (Codex) and supply the current value at read time. The id, label,
// default, display order, option list, AND mutability are taken from the template
// (shared, immutable data); an empty current falls back to the template's DefaultValue,
// so a group the caller forgot to supply a current for still renders a valid (in-list)
// selection rather than a blank one -- which holds because every selectable template a
// provider registers sets a non-empty DefaultValue (a template with options but no default
// would still render blank here; the fallback can only point at what the template names).
// Honoring the template's Mutable lets a provider project an agent-controlled, read-only
// axis through LiveGroup without it being shown as user-editable.
func LiveGroup(static *leapmuxv1.AvailableOptionGroup, current string) *leapmuxv1.AvailableOptionGroup {
	if static == nil {
		return nil
	}
	if current == "" {
		current = static.GetDefaultValue()
	}
	g := cloneOptionGroupTemplate(static)
	g.CurrentValue = current
	return g
}

// cloneOptionGroupTemplate returns a shallow copy of a static option-group template,
// SHARING its (immutable) Options slice. It copies every scalar field explicitly here, so a
// field added to AvailableOptionGroup must be added in THIS one place -- but only here, not
// at each projection site below (LiveGroup, which then overrides only CurrentValue, and
// FilterGroupOptions, which overrides only Options). Centralizing the copy keeps the two
// projections from drifting in which fields they carry.
func cloneOptionGroupTemplate(static *leapmuxv1.AvailableOptionGroup) *leapmuxv1.AvailableOptionGroup {
	return &leapmuxv1.AvailableOptionGroup{
		Id:           static.GetId(),
		Label:        static.GetLabel(),
		Options:      static.GetOptions(),
		CurrentValue: static.GetCurrentValue(),
		DefaultValue: static.GetDefaultValue(),
		Mutable:      static.GetMutable(),
		Order:        static.GetOrder(),
	}
}

// FilterGroupOptions returns a shallow copy of static with its Options narrowed to those
// satisfying keep, preserving id/label/default/mutability/order. Returns nil for a nil
// input. It drops an option a provider can't currently offer (e.g. Claude hiding the "auto"
// permission mode when the startup probe rejected it) WITHOUT mutating the shared static
// template, so it composes with LiveGroup (which then overlays the live current value)
// instead of each caller re-implementing the template copy.
func FilterGroupOptions(static *leapmuxv1.AvailableOptionGroup, keep func(*leapmuxv1.AvailableOption) bool) *leapmuxv1.AvailableOptionGroup {
	if static == nil {
		return nil
	}
	opts := make([]*leapmuxv1.AvailableOption, 0, len(static.GetOptions()))
	for _, o := range static.GetOptions() {
		if keep(o) {
			opts = append(opts, o)
		}
	}
	g := cloneOptionGroupTemplate(static)
	g.Options = opts
	return g
}

// modelThenEffort assembles the leading "model group, then effort group" slice, omitting either
// when it is nil. The model-first/effort-second ordering and the omit-when-absent rule live here so
// the mutable (ModelAndEffortGroups) and read-only (ReadOnlyModelAndEffortGroups) builders share one
// definition and can't drift on them.
func modelThenEffort(modelGroup, effortGroup *leapmuxv1.AvailableOptionGroup) []*leapmuxv1.AvailableOptionGroup {
	var groups []*leapmuxv1.AvailableOptionGroup
	if modelGroup != nil {
		groups = append(groups, modelGroup)
	}
	if effortGroup != nil {
		groups = append(groups, effortGroup)
	}
	return groups
}

// ReadOnlyModelAndEffortGroups builds the read-only model group (with a humanized
// display name) and, when effort is a concrete non-auto value, the read-only effort
// group, for a session whose model/effort UI is hidden (a third-party Claude session or
// can_change_model_and_effort=false). It mirrors ModelAndEffortGroups for the mutable
// case so the EffortAuto-suppression rule lives here next to its sibling rather than
// inline at the call site. modelName is the model's humanized display label.
func ReadOnlyModelAndEffortGroups(model, modelName, effort string) []*leapmuxv1.AvailableOptionGroup {
	var modelGroup, effortGroup *leapmuxv1.AvailableOptionGroup
	if model != "" {
		modelGroup = agent.ReadOnlyValueGroup(agent.OptionIDModel, agent.ModelGroupLabel, agent.OptionOrderModel, model, modelName)
	}
	if effort != "" && effort != agent.EffortAuto {
		effortGroup = agent.ReadOnlyValueGroup(agent.OptionIDEffort, agent.EffortGroupLabel, agent.OptionOrderEffort, effort, "")
	}
	return modelThenEffort(modelGroup, effortGroup)
}

// ModelAndEffortGroups returns the model group followed by the current model's
// effort group, omitting either when the catalog yields none. Shared by every
// provider that exposes model and effort as its two leading top-level groups
// (Codex, Pi, Claude). EffortLabel names the effort axis (EffortGroupLabel for Codex
// and Claude, pi.ThinkingLevelLabel for Pi) and is applied to both the top-level group
// and -- via the default modelSubGroups -- the per-model sub_groups, so a model switch
// keeps the label consistent. modelSubGroups overrides the per-model sub_groups func: pass
// nil for the default (effort sub_groups only), or claudeModelSubGroups to also carry the
// model-dependent extended-thinking group Claude swaps in on a model switch.
func ModelAndEffortGroups(models []*agent.ModelInfo, model, effort, effortLabel string, modelSubGroups agent.ModelSubGroupsFunc) []*leapmuxv1.AvailableOptionGroup {
	if modelSubGroups == nil {
		modelSubGroups = agent.EffortSubGroupsLabeled(effortLabel)
	}
	// agent.EffortGroupForModel resolves the current model in the catalog (FindAvailableModel) before
	// building its effort group, so a model switch carries the new model's effort tiers.
	return modelThenEffort(
		agent.ModelOptionGroup(models, model, modelSubGroups),
		agent.EffortGroupForModel(agent.FindAvailableModel(models, model), effort, effortLabel),
	)
}
