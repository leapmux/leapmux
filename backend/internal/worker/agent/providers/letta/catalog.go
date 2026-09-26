package letta

import (
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// defaultModels is the static model catalog. A running server replaces it with
// the list its provider configuration exposes.
var defaultModels = []*agent.ModelInfo{
	{
		Id:          "openai-compatible/mock-model",
		DisplayName: "Configured model",
	},
}

// lettaModelGroup builds the model option group from the server's catalog.
func lettaModelGroup(models []lettaModel, current string) *leapmuxv1.AvailableOptionGroup {
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
