package providerkit

import (
	"github.com/leapmux/leapmux/internal/worker/agent"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// LiveGroup puts an agent's confirmed current value on a copy of a provider's static
// option-group template. A provider that declares its option lists statically (Codex)
// supplies the current value at read time through it.
//
// The copy takes every other field from the template, which is shared and immutable data:
// the id, the label, the default, the display order, the option list, the mutability, and
// the read-only reason. Because Mutable comes from the template, a provider can show an axis
// that the agent controls through LiveGroup, and the UI does not offer that axis for edit.
//
// An empty current falls back to the template's DefaultValue. A group that the caller gave
// no current value then still shows a valid selection from its list, not a blank one. This
// holds because every selectable template that a provider registers sets a non-empty
// DefaultValue. A template with options and no default still shows a blank selection here,
// because the fallback can only point at a value that the template holds.
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

// cloneOptionGroupTemplate returns a shallow copy of a static option-group template. The
// copy SHARES the immutable Options slice of the template. This function is the one place
// that copies the fields of AvailableOptionGroup, so a new field needs one line here and no
// change at the two projections: LiveGroup then replaces CurrentValue only, and
// FilterGroupOptions then replaces Options only. The two projections thus cannot drift in
// the fields that they carry. TestCloneOptionGroupTemplate_CopiesEveryField fails for a
// field that this function does not copy.
func cloneOptionGroupTemplate(static *leapmuxv1.AvailableOptionGroup) *leapmuxv1.AvailableOptionGroup {
	return &leapmuxv1.AvailableOptionGroup{
		Id:             static.GetId(),
		Label:          static.GetLabel(),
		Options:        static.GetOptions(),
		CurrentValue:   static.GetCurrentValue(),
		DefaultValue:   static.GetDefaultValue(),
		Mutable:        static.GetMutable(),
		Order:          static.GetOrder(),
		ReadOnlyReason: static.GetReadOnlyReason(),
	}
}

// FilterGroupOptions returns a shallow copy of static that keeps only the options that
// satisfy keep. Every other field comes from the template. It returns nil for a nil input.
// It drops an option that a provider cannot offer now. For example, Claude drops the "auto"
// permission mode when the startup probe rejected it. It NEVER changes the shared static
// template, so it composes with LiveGroup, which then puts the live current value on the
// copy. No caller has to copy the template itself.
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

// modelThenEffort returns the model group, then the effort group, and omits a group that is
// nil. The mutable builder (ModelAndEffortGroups) and the read-only builder
// (ReadOnlyModelAndEffortGroups) both call it, so they share one order and one omission rule
// and cannot drift apart on them.
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

// ReadOnlyModelAndEffortGroups builds the read-only model group, with a display name for a
// human reader. When effort is a concrete value other than auto, it also builds the
// read-only effort group. A session that hides its model and effort controls uses it: a
// third-party Claude session, or a session with can_change_model_and_effort=false. It is the
// read-only sibling of ModelAndEffortGroups, so the rule that drops EffortAuto lives here
// beside that sibling and not at the call site. modelName is the display label of the model.
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

// ModelAndEffortGroups returns the model group, then the effort group of the current model.
// It omits a group when the catalog gives none. Every provider that shows model and effort
// as its first two top-level groups calls it: Codex, Pi, and Claude.
//
// effortLabel is the label of the effort axis: EffortGroupLabel for Codex and Claude, and
// pi.ThinkingLevelLabel for Pi. The top-level group takes it. The default modelSubGroups
// gives it to the sub_groups of each model also, so a model switch keeps the same label.
// modelSubGroups replaces the function that builds the sub_groups of each model. Pass nil
// for the default, which gives effort sub_groups only. Pass claudeModelSubGroups to add the
// extended-thinking group that depends on the model, which Claude swaps in on a model switch.
func ModelAndEffortGroups(models []*agent.ModelInfo, model, effort, effortLabel string, modelSubGroups agent.ModelSubGroupsFunc) []*leapmuxv1.AvailableOptionGroup {
	if modelSubGroups == nil {
		modelSubGroups = agent.EffortSubGroupsLabeled(effortLabel)
	}
	// FindAvailableModel resolves the current model in the catalog before EffortGroupForModel
	// builds its effort group, so a model switch carries the effort tiers of the new model.
	return modelThenEffort(
		agent.ModelOptionGroup(models, model, modelSubGroups),
		agent.EffortGroupForModel(agent.FindAvailableModel(models, model), effort, effortLabel),
	)
}
