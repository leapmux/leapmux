package cmd

import (
	"fmt"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
)

type agentSetting struct {
	ID      string `json:"id" table:"SETTING"`
	Current string `json:"current" table:"CURRENT"`
}

// additionalAgentSettings reports the current values outside the three
// dedicated fields on agentRow. The Worker lists live groups at the top level.
// Subgroups describe the choices for each possible model.
func additionalAgentSettings(groups []*leapmuxv1.AvailableOptionGroup) []agentSetting {
	var settings []agentSetting
	for _, group := range groups {
		id := group.GetId()
		switch id {
		case "", optionids.Model, optionids.Effort, optionids.PermissionMode:
			continue
		}
		if current := group.GetCurrentValue(); current != "" {
			settings = append(settings, agentSetting{ID: id, Current: current})
		}
	}
	return settings
}

type optionCatalog []optionGroup

// optionGroup is one configuration axis with its choices.
type optionGroup struct {
	ID             string   `json:"id"`
	Label          string   `json:"label"`
	Current        string   `json:"current"`
	Default        string   `json:"default,omitempty"`
	Mutable        bool     `json:"mutable"`
	Order          int32    `json:"order,omitempty"`
	ReadOnlyReason string   `json:"read_only_reason,omitempty"`
	Choices        []option `json:"choices"`
}

// option is one choice of an option group. SubGroups are the axes that this
// choice determines, such as the efforts that one model offers.
type option struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Description   string         `json:"description,omitempty"`
	ContextWindow int64          `json:"context_window,omitempty"`
	SubGroups     optionCatalog  `json:"sub_groups,omitempty"`
	Clears        []optionEffect `json:"clears,omitempty"`
}

type optionEffect struct {
	GroupID string `json:"group_id"`
	Value   string `json:"value"`
}

func optionGroupsOf(groups []*leapmuxv1.AvailableOptionGroup) optionCatalog {
	out := make(optionCatalog, 0, len(groups))
	for _, group := range groups {
		choices := make([]option, 0, len(group.GetOptions()))
		for _, choice := range group.GetOptions() {
			var clears []optionEffect
			for _, effect := range choice.GetClears() {
				clears = append(clears, optionEffect{GroupID: effect.GetGroupId(), Value: effect.GetValue()})
			}
			choices = append(choices, option{
				ID:            choice.GetId(),
				Name:          choice.GetName(),
				Description:   choice.GetDescription(),
				ContextWindow: choice.GetContextWindow(),
				SubGroups:     optionGroupsOf(choice.GetSubGroups()),
				Clears:        clears,
			})
		}
		out = append(out, optionGroup{
			ID:             group.GetId(),
			Label:          group.GetLabel(),
			Current:        group.GetCurrentValue(),
			Default:        group.GetDefaultValue(),
			Mutable:        group.GetMutable(),
			Order:          group.GetOrder(),
			ReadOnlyReason: group.GetReadOnlyReason(),
			Choices:        choices,
		})
	}
	return out
}

// TextValue expands each choice and its dependent groups below the parent.
func (catalog optionCatalog) TextValue() string {
	var out strings.Builder
	catalog.appendText(&out, "")
	return out.String()
}

func (catalog optionCatalog) appendText(out *strings.Builder, indent string) {
	for _, group := range catalog {
		fmt.Fprintf(out, "\n%s%q %q current=%q default=%q mutable=%t",
			indent, group.ID, group.Label, group.Current, group.Default, group.Mutable)
		if group.ReadOnlyReason != "" {
			fmt.Fprintf(out, " read_only_reason=%q", group.ReadOnlyReason)
		}
		for _, choice := range group.Choices {
			fmt.Fprintf(out, "\n%s  %q %q", indent, choice.ID, choice.Name)
			if choice.ContextWindow != 0 {
				fmt.Fprintf(out, " context_window=%d", choice.ContextWindow)
			}
			if choice.Description != "" {
				fmt.Fprintf(out, " %q", choice.Description)
			}
			for _, effect := range choice.Clears {
				fmt.Fprintf(out, " clears %q=%q", effect.GroupID, effect.Value)
			}
			choice.SubGroups.appendText(out, indent+"    ")
		}
	}
}
