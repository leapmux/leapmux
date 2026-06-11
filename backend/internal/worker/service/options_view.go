package service

import (
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"google.golang.org/protobuf/proto"
)

// overlayOptionGroupCurrents returns the groups with each group's CurrentValue
// replaced by the persisted/overridden selection (when present), so the read
// model reflects the agent's chosen values rather than just the catalog. Groups
// whose current value is unchanged pass through by reference; only the patched
// ones are cloned, keeping the shared cached catalog untouched.
//
// A persisted value is only overlaid when it is one of the group's options (or
// the group enumerates none): if a stale catalog built for a different model is
// being served, its option list and the persisted selection can disagree, and
// forcing an out-of-list value as CurrentValue would render an invalid selection.
// In that case the catalog's own (valid) current is kept until the live catalog
// for the persisted model arrives.
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

// optionValueInGroup reports whether v is selectable in g: either g enumerates no
// options (free-form / not-yet-populated) or one of its options has id == v.
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

// optionGroupsView returns an agent's configuration axes as config option
// groups: the manager's catalog (running provider, cached, or static fallback)
// overlaid with the agent's persisted current selections plus any additional
// overrides (e.g. a permission-mode change being broadcast before it is re-read
// from the DB). Package-level so both *Context and *OutputHandler can use it.
func optionGroupsView(agents *agent.Manager, a *db.Agent, overrides map[string]string) []*leapmuxv1.AvailableOptionGroup {
	current := loadOptions(a.Options, a.AgentProvider)
	for k, v := range overrides {
		if v != "" {
			current[k] = v
		}
	}
	// Build the not-running catalog from THIS row's persisted option_groups -- the last live
	// group set, carrying the dynamically discovered model list, the live-filtered groups (e.g.
	// Fast Mode / Output Style / a permission mode with 'auto' removed) and the provider's display
	// order, none of which the static registry reconstruction can reproduce. Passing the snapshot
	// to OptionGroupsForRow (rather than seeding the shared cache and reading it back) keeps this
	// read self-consistent with the row it loaded: a concurrent reader's older snapshot can't be
	// served here via the shared cache. The agent's current model lets the static fallback (used
	// only when the row has no persisted catalog either) build the effort group for THAT model. A
	// running agent's live catalog still takes precedence inside OptionGroupsForRow.
	groups := agents.OptionGroupsForRow(a.ID, a.AgentProvider, current[agent.OptionIDModel], parseOptionGroups(a.OptionGroups))
	return overlayOptionGroupCurrents(groups, current)
}

// optionGroupsForAgent returns the agent's option groups overlaid with its
// persisted current selections.
func (svc *Context) optionGroupsForAgent(a *db.Agent) []*leapmuxv1.AvailableOptionGroup {
	return optionGroupsView(svc.Agents, a, nil)
}
