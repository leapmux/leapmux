package service

import (
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"google.golang.org/protobuf/proto"
)

// overlayOptionGroupCurrents applies persisted selections to the catalog.
// Clone only changed groups, so the shared catalog stays unchanged.
// Keep the catalog selection when a persisted value is absent from its choices.
// A stale catalog can list choices for a different model.
// Its live replacement will resolve that difference.
func overlayOptionGroupCurrents(groups []*leapmuxv1.AvailableOptionGroup, current map[string]string) []*leapmuxv1.AvailableOptionGroup {
	if len(groups) == 0 {
		return groups
	}
	out := make([]*leapmuxv1.AvailableOptionGroup, len(groups))
	for i, g := range groups {
		if v, ok := current[g.GetId()]; ok && v != g.GetCurrentValue() && optionValueInGroup(g, v) {
			c := proto.Clone(g).(*leapmuxv1.AvailableOptionGroup)
			c.CurrentValue = v
			out[i] = c
		} else {
			out[i] = g
		}
	}
	return out
}

// optionValueInGroup accepts a listed choice or a value in an empty catalog.
func optionValueInGroup(g *leapmuxv1.AvailableOptionGroup, v string) bool {
	opts := g.GetOptions()
	if len(opts) == 0 {
		return true
	}
	for _, o := range opts {
		if o.GetId() == v {
			return true
		}
	}
	return false
}

// optionGroupsView combines the catalog with this row's stored selections.
// A root fills absent selections from provider defaults.
// A child keeps absent selections unknown because its native configuration can differ.
func optionGroupsView(agents *agent.Manager, a *db.Agent, overrides map[string]string) []*leapmuxv1.AvailableOptionGroup {
	persisted := parseOptionGroups(a.OptionGroups)
	current := parseOptions(a.Options)
	if a.ParentAgentID.Valid {
		for _, group := range persisted {
			if _, present := current[group.GetId()]; !present && group.GetCurrentValue() != "" {
				current[group.GetId()] = group.GetCurrentValue()
			}
		}
	} else {
		current = resolveProviderDefaults(agents.Registry(), current, a.AgentProvider)
	}
	for key, value := range overrides {
		if value != "" {
			current[key] = value
		}
	}
	// The row's last live catalog preserves dynamic choices and display order.
	// A running provider still supplies its current catalog through the manager.
	groups := agents.OptionGroupsForRow(a.ID, a.AgentProvider, current[agent.OptionIDModel], persisted)
	if !a.ParentAgentID.Valid {
		return overlayOptionGroupCurrents(groups, current)
	}
	// A child reports its actual selections. Catalog defaults cannot replace them.
	// A custom native model can be absent from the parent's selectable choices.
	out := make([]*leapmuxv1.AvailableOptionGroup, len(groups))
	for index, group := range groups {
		if group == nil {
			continue
		}
		out[index] = proto.Clone(group).(*leapmuxv1.AvailableOptionGroup)
		value := current[group.GetId()]
		out[index].CurrentValue = value
		if value != "" {
			listed := false
			for _, option := range group.GetOptions() {
				if option.GetId() == value {
					listed = true
					break
				}
			}
			if !listed {
				out[index].Options = append(out[index].Options, &leapmuxv1.AvailableOption{Id: value, Name: value})
			}
		}
	}
	return out
}

// optionGroupsForAgent returns the agent's option groups overlaid with its
// persisted current selections.
func (svc *Service) optionGroupsForAgent(a *db.Agent) []*leapmuxv1.AvailableOptionGroup {
	return optionGroupsView(svc.Agents, a, nil)
}
