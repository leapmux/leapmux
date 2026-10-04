package letta

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
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
	if err := pending.waiter.wait(a.Context(), a.ProcessDone(), timeout); err != nil {
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

func (a *Agent) clearModelPending(pending *lettaModelUpdatePending) {
	a.Mu.Lock()
	if a.modelPending == pending {
		a.modelPending = nil
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
			Handle string `json:"handle"`
			Label  string `json:"label"`
		} `json:"entries"`
		AvailableHandles *[]string `json:"available_handles"`
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
		seen := make(map[string]bool, len(response.Entries))
		for _, entry := range response.Entries {
			if entry.Handle == "" || seen[entry.Handle] || available != nil && !available[entry.Handle] {
				continue
			}
			seen[entry.Handle] = true
			label := entry.Label
			if label == "" {
				label = entry.Handle
			}
			models = append(models, lettaModel{id: entry.Handle, displayName: label})
		}
		a.catalog.models = models
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

// handleUpdateModelResponse settles one model write from the native reply.
func (a *Agent) handleUpdateModelResponse(line []byte) {
	var response struct {
		RequestID   string `json:"request_id"`
		Success     bool   `json:"success"`
		ModelHandle string `json:"model_handle"`
		Error       string `json:"error"`
	}
	if err := json.Unmarshal(line, &response); err != nil {
		return
	}
	a.Mu.Lock()
	pending := a.modelPending
	if pending == nil || pending.requestID != response.RequestID {
		a.Mu.Unlock()
		return
	}
	a.modelPending = nil
	if response.Success {
		model := response.ModelHandle
		if model == "" {
			model = pending.model
		}
		a.settings.model = model
		if pending.effortPresent {
			a.settings.reasoningLevel = pending.effort
		}
	}
	a.Mu.Unlock()
	if !response.Success {
		slog.Warn("letta: model update refused", "agent_id", a.AgentID(), "model", pending.model, "error", response.Error)
		if response.Error == "" {
			pending.waiter.settle(errors.New("the App Server refused the model update"))
		} else {
			pending.waiter.settle(fmt.Errorf("the App Server refused the model update: %s", response.Error))
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
