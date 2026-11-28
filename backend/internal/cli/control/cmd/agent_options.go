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

// TextValue formats each option group as a human-readable section marking the
// currently active choice with "*".
func (catalog optionCatalog) TextValue() string {
	var out strings.Builder
	catalog.appendText(&out, "")
	return out.String()
}

func (catalog optionCatalog) appendText(out *strings.Builder, indent string) {
	for _, group := range catalog {
		title := group.Label
		if title == "" {
			title = group.ID
		} else if group.ID != "" && !strings.EqualFold(title, group.ID) {
			title = fmt.Sprintf("%s (%s)", title, group.ID)
		}
		var meta []string
		if group.Current != "" {
			meta = append(meta, "current: "+group.Current)
		}
		if group.Default != "" && group.Default != group.Current {
			meta = append(meta, "default: "+group.Default)
		}
		if !group.Mutable {
			if group.ReadOnlyReason != "" {
				meta = append(meta, "read-only: "+group.ReadOnlyReason)
			} else {
				meta = append(meta, "read-only")
			}
		}
		fmt.Fprintf(out, "\n%s%s", indent, title)
		if len(meta) > 0 {
			fmt.Fprintf(out, " [%s]", strings.Join(meta, ", "))
		}
		idWidth := 0
		for _, choice := range group.Choices {
			if len(choice.ID) > idWidth {
				idWidth = len(choice.ID)
			}
		}
		for _, choice := range group.Choices {
			marker := " "
			if choice.ID != "" && choice.ID == group.Current {
				marker = "*"
			}
			pad := strings.Repeat(" ", idWidth-len(choice.ID))
			fmt.Fprintf(out, "\n%s  %s %s%s", indent, marker, choice.ID, pad)
			if choice.Name != "" && choice.Name != choice.ID {
				fmt.Fprintf(out, "  %s", choice.Name)
			}
			if choice.ContextWindow != 0 {
				fmt.Fprintf(out, " (%dk ctx)", choice.ContextWindow/1000)
			}
			if choice.Description != "" {
				fmt.Fprintf(out, " — %s", choice.Description)
			}
			if len(choice.Clears) > 0 {
				effects := make([]string, 0, len(choice.Clears))
				for _, effect := range choice.Clears {
					effects = append(effects, fmt.Sprintf("%s=%s", effect.GroupID, effect.Value))
				}
				fmt.Fprintf(out, " (clears %s)", strings.Join(effects, ", "))
			}
			choice.SubGroups.appendText(out, indent+"      ")
		}
	}
}
