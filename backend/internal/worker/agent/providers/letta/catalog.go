package letta

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

type lettaCatalogPending struct {
	requestID string
	waiter    *runtimeWaiter
}

// loadModelCatalog reads the native model handles before the settings menu
// receives its first snapshot.
func (a *Agent) loadModelCatalog(timeout time.Duration) error {
	command := newLettaCommand("list_models", a.nextRequestID())
	pending := &lettaCatalogPending{requestID: command.RequestID, waiter: newRuntimeWaiter()}
	a.Mu.Lock()
	a.catalogPending = pending
	a.Mu.Unlock()
	if err := a.sendCommand(command); err != nil {
		a.clearCatalogPending(pending)
		return err
	}
	if err := pending.waiter.wait(a.Context(), a.Clock(), a.ProcessDone(), timeout); err != nil {
		a.clearCatalogPending(pending)
		return err
	}
	return nil
}

func (a *Agent) clearCatalogPending(pending *lettaCatalogPending) {
	a.Mu.Lock()
	if a.catalogPending == pending {
		a.catalogPending = nil
	}
	a.Mu.Unlock()
}

// handleListModelsResponse reads the handles and labels that the running App
// Server offers. An absent availability list leaves the full entry list usable.
func (a *Agent) handleListModelsResponse(line []byte) {
	var response struct {
		RequestID string `json:"request_id"`
		Success   bool   `json:"success"`
		Error     string `json:"error"`
		Entries   []struct {
			ID         string `json:"id"`
			Handle     string `json:"handle"`
			Label      string `json:"label"`
			UpdateArgs struct {
				Effort       string `json:"reasoning_effort"`
				ProviderType string `json:"provider_type"`
			} `json:"updateArgs"`
		} `json:"entries"`
		AvailableHandles *[]string         `json:"available_handles"`
		ProviderAliases  map[string]string `json:"byok_provider_aliases"`
	}
	if err := json.Unmarshal(line, &response); err != nil {
		return
	}
	a.Mu.Lock()
	pending := a.catalogPending
	if pending != nil && pending.requestID == response.RequestID {
		a.catalogPending = nil
	} else {
		pending = nil
	}
	if response.Success {
		var available map[string]bool
		if response.AvailableHandles != nil {
			available = make(map[string]bool, len(*response.AvailableHandles))
			for _, handle := range *response.AvailableHandles {
				available[handle] = true
			}
		}
		models := make([]lettaModel, 0, len(response.Entries))
		indices := make(map[string]int, len(response.Entries))
		for _, entry := range response.Entries {
			if entry.Handle == "" || available != nil && !available[entry.Handle] {
				continue
			}
			index, exists := indices[entry.Handle]
			if !exists {
				index = len(models)
				indices[entry.Handle] = index
				label := entry.Label
				if label == "" {
					label = entry.Handle
				}
				models = append(models, lettaModel{id: entry.Handle, displayName: label})
			}
			models[index].variants = append(models[index].variants, lettaModelVariant{
				id: entry.ID, effort: entry.UpdateArgs.Effort, providerType: entry.UpdateArgs.ProviderType,
			})
		}
		a.catalog.models = models
		a.catalog.providerAliases = response.ProviderAliases
	}
	a.Mu.Unlock()
	if pending == nil {
		return
	}
	if !response.Success {
		if response.Error == "" {
			pending.waiter.settle(errors.New("the App Server refused the model catalog"))
		} else {
			pending.waiter.settle(fmt.Errorf("the App Server refused the model catalog: %s", response.Error))
		}
		return
	}
	pending.waiter.settle(nil)
}

// defaultModels is the static model catalog. A running server replaces it with
// the list its provider configuration exposes.
var defaultModels = []*agent.ModelInfo{
	{
		Id:          "openai-compatible/mock-model",
		DisplayName: "Configured model",
	},
}

// lettaModelInfo projects advertised variants without changing the native catalog.
func lettaModelInfo(model lettaModel) *agent.ModelInfo {
	info := &agent.ModelInfo{Id: model.id, DisplayName: model.displayName}
	seen := map[string]bool{agent.EffortAuto: true}
	for _, variant := range model.variants {
		if variant.effort == "" || seen[variant.effort] {
			continue
		}
		if len(info.SupportedEfforts) == 0 {
			info.DefaultEffort = agent.EffortAuto
			info.SupportedEfforts = append(info.SupportedEfforts, providerkit.EffortTier(agent.EffortAuto))
		}
		seen[variant.effort] = true
		info.SupportedEfforts = append(info.SupportedEfforts, providerkit.EffortTier(variant.effort))
	}
	return info
}
