package acptest

import (
	"encoding/json"
	"fmt"
	"slices"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// ModelSwitchAgent is the part of an ACP agent that a model-switch test drives.
// Every ACP provider gets these methods from its embedded base.
type ModelSwitchAgent interface {
	UpdateSettings(options optionmap.Map) agent.SettingsApplyResult
	HandleConfigOptionUpdateForTest(update json.RawMessage)
	SetSinkForTest(sink agent.ProviderServices)
	OptionGroups() []*leapmuxv1.AvailableOptionGroup
}

// ModelSwitchServer is a fake ACP server for the tests of a model switch. It keeps
// one model and one thought-level axis. A model write changes the model and
// decides the new thought level through Reset, as each real server does in its own
// way. A write of the thought level is refused when the current model does not offer
// the level, as every real server refuses it. The refusal makes a base that writes a
// level for a model without the axis fail the update.
//
// The server answers a write with the complete configOptions snapshot, as the ACP
// specification requires. Notify, when set, sends the same snapshot first as a
// config_option_update notification, as OpenCode, Reasonix, Goose, Grok Build and
// Dirac do before they reply to a model write.
type ModelSwitchServer struct {
	// EffortID is the config-option id of the thought-level axis.
	EffortID string
	// Models lists the model ids that the server offers.
	Models []string
	// Levels maps a model id to the thought levels that the model offers. A model
	// with no entry, or an empty entry, has no thought-level axis.
	Levels map[string][]string
	// Reset returns the thought level that the server applies when it writes model.
	// previous is the level before the write. Nil keeps previous when the new model
	// offers it, and takes the first level of the new model otherwise.
	Reset func(model, previous string) string
	// Notify, when set, receives the config_option_update that the server sends
	// before it replies to a model write.
	Notify func(update json.RawMessage)

	model  string
	effort string
}

// Start sets the model and the thought level that the session runs.
func (s *ModelSwitchServer) Start(model, effort string) {
	s.model, s.effort = model, effort
}

// ConfigOptions returns the configOptions array of the current state.
func (s *ModelSwitchServer) ConfigOptions() json.RawMessage {
	models := make([]map[string]any, len(s.Models))
	for i, id := range s.Models {
		models[i] = map[string]any{"value": id, "name": id}
	}
	options := []map[string]any{{
		"id": "model", "name": "Model", "category": "model", "type": "select",
		"currentValue": s.model, "options": models,
	}}
	if levels := s.Levels[s.model]; len(levels) > 0 {
		values := make([]map[string]any, len(levels))
		for i, level := range levels {
			values[i] = map[string]any{"value": level, "name": level}
		}
		options = append(options, map[string]any{
			"id": s.EffortID, "name": "Reasoning Effort", "category": "thought_level", "type": "select",
			"currentValue": s.effort, "options": values,
		})
	}
	encoded, err := json.Marshal(options)
	if err != nil {
		panic(fmt.Sprintf("encode the fake configOptions: %v", err))
	}
	return encoded
}

// snapshot is the reply of a successful write.
func (s *ModelSwitchServer) snapshot() json.RawMessage {
	return json.RawMessage(`{"configOptions":` + string(s.ConfigOptions()) + `}`)
}

// update is the config_option_update notification of the current state.
func (s *ModelSwitchServer) update() json.RawMessage {
	return json.RawMessage(`{"sessionUpdate":"config_option_update","configOptions":` + string(s.ConfigOptions()) + `}`)
}

// Seed gives the agent a recording sink and brings it to the state of the server, as
// the config_option_update of a running session does. notify makes the server send
// each later model-write snapshot to the agent as a notification before it replies.
// It returns the sink. The seed persists one refresh, so a test that counts
// refreshes reads the count after Seed.
func (s *ModelSwitchServer) Seed(ag ModelSwitchAgent, notify bool) *agenttest.Sink {
	sink := &agenttest.Sink{}
	ag.SetSinkForTest(agent.NewProviderServices(sink))
	if notify {
		s.Notify = ag.HandleConfigOptionUpdateForTest
	}
	ag.HandleConfigOptionUpdateForTest(s.update())
	return sink
}

// ConfigWrites lists the session/set_config_option requests as "id=value", in the
// order that the agent sent them.
func ConfigWrites(requests []agenttest.RecordedRequest) []string {
	var writes []string
	for _, r := range requests {
		if r.Method == "session/set_config_option" {
			id, _ := r.Params["configId"].(string)
			value, _ := r.Params["value"].(string)
			writes = append(writes, id+"="+value)
		}
	}
	return writes
}

// Respond answers one request of the agent.
func (s *ModelSwitchServer) Respond(req agenttest.RecordedRequest) agenttest.RPCReply {
	if req.Method != "session/set_config_option" {
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	}
	id, _ := req.Params["configId"].(string)
	value, _ := req.Params["value"].(string)
	switch id {
	case "model":
		if !slices.Contains(s.Models, value) {
			return refusal(fmt.Sprintf("model not found: %s", value))
		}
		previous := s.effort
		s.model = value
		s.effort = s.resetLevel(value, previous)
		if s.Notify != nil {
			s.Notify(s.update())
		}
	case s.EffortID:
		if !slices.Contains(s.Levels[s.model], value) {
			return refusal(fmt.Sprintf("effort not found: %s", value))
		}
		s.effort = value
	default:
		return refusal(fmt.Sprintf("unknown config option: %s", id))
	}
	return agenttest.RPCReply{Result: s.snapshot()}
}

// resetLevel returns the thought level after a write of model.
func (s *ModelSwitchServer) resetLevel(model, previous string) string {
	levels := s.Levels[model]
	if len(levels) == 0 {
		return ""
	}
	if s.Reset != nil {
		return s.Reset(model, previous)
	}
	if slices.Contains(levels, previous) {
		return previous
	}
	return levels[0]
}

func refusal(message string) agenttest.RPCReply {
	encoded, err := json.Marshal(map[string]any{"code": -32602, "message": message})
	if err != nil {
		panic(fmt.Sprintf("encode the fake refusal: %v", err))
	}
	return agenttest.RPCReply{Error: encoded}
}
