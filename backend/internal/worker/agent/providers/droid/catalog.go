package droid

import (
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// defaultModels is the static model catalog. It holds one BYOK-shaped entry so
// a session that never reached the CLI still has a model axis; a running agent
// replaces it with the list its session reports (settings.go).
var defaultModels = []*agent.ModelInfo{
	{
		Id:          "custom:Mock-0",
		DisplayName: "Custom model",
	},
}

// droidModelGroup builds the model option group from the session's catalog.
func droidModelGroup(models []droidModel, current string) *leapmuxv1.AvailableOptionGroup {
	group := &leapmuxv1.AvailableOptionGroup{
		Id:           agent.OptionIDModel,
		Label:        "Model",
		CurrentValue: current,
		Mutable:      true,
		Order:        agent.OptionOrderModel,
	}
	for _, m := range models {
		group.Options = append(group.Options, &leapmuxv1.AvailableOption{
			Id:   m.id,
			Name: m.displayName,
		})
	}
	if group.CurrentValue == "" && len(group.Options) > 0 {
		group.CurrentValue = group.Options[0].GetId()
	}
	return group
}

// droidEffortGroup builds the effort group. A model that states its own ladder
// narrows the choices; a model that states none takes the full static set.
func droidEffortGroup(current string, models []droidModel, modelID string) *leapmuxv1.AvailableOptionGroup {
	ladder := droidEffortLadder(models, modelID)
	if len(ladder) == 0 {
		group := droidEffortGroupFromOptions(effortGroup.GetOptions(), current, contracts.DroidEffortMedium)
		return group
	}
	return droidEffortGroupFromOptions(droidEffortOptions(ladder), current, contracts.DroidEffortMedium)
}

// droidEffortOptions builds the option list for a ladder.
func droidEffortOptions(ladder []string) []*leapmuxv1.AvailableOption {
	options := make([]*leapmuxv1.AvailableOption, 0, len(ladder))
	for _, e := range ladder {
		name := e
		switch e {
		case contracts.DroidEffortNone:
			name = "None"
		case contracts.DroidEffortLow:
			name = "Low"
		case contracts.DroidEffortMedium:
			name = "Medium"
		case contracts.DroidEffortHigh:
			name = "High"
		}
		options = append(options, &leapmuxv1.AvailableOption{Id: e, Name: name})
	}
	return options
}

// droidEffortGroupFromOptions builds a fresh effort group. It never copies a
// proto message, which holds a lock.
func droidEffortGroupFromOptions(options []*leapmuxv1.AvailableOption, current, fallback string) *leapmuxv1.AvailableOptionGroup {
	if current == "" {
		current = fallback
	}
	return &leapmuxv1.AvailableOptionGroup{
		Id:           agent.OptionIDEffort,
		Label:        "Effort",
		CurrentValue: current,
		Mutable:      true,
		Order:        agent.OptionOrderEffort,
		Options:      options,
	}
}

// droidEffortLadder returns the efforts one model states.
func droidEffortLadder(models []droidModel, modelID string) []string {
	for _, m := range models {
		if m.id == modelID {
			return m.efforts
		}
	}
	return nil
}
