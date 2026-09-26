package junie

import (
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

// junieSubagentFromToolCall maps Junie's `spawn_subagent` call to a SUBAGENT
// registry row. Junie stamps no tool name on an ACP tool call -- `title` is
// the call's display label -- so this reads the `agent` field of `rawInput`,
// which only `spawn_subagent` carries. The `handle` field is the continuation
// key of a follow-up spawn; a call that carries one is a new turn of the same
// child and opens no second row.
func junieSubagentFromToolCall(tc acp.ToolCallEnvelope) *acp.SubagentObservation {
	input := parseJunieSpawnInput(tc.RawInput)
	if input.Agent == "" {
		return nil
	}
	title := tc.Title
	if title == "" {
		title = contracts.JunieToolSpawnSubagent
	}
	return &acp.SubagentObservation{
		RowKey:   tc.ToolCallID,
		Title:    title,
		Activity: title,
		Prompt:   input.ExtraContext,
	}
}

// junieSpawnInput is the subset of a `spawn_subagent` raw input that the
// mapping reads.
type junieSpawnInput struct {
	Agent        string `json:"agent"`
	Handle       string `json:"handle"`
	ExtraContext string `json:"extraContext"`
}

func parseJunieSpawnInput(raw json.RawMessage) junieSpawnInput {
	var input junieSpawnInput
	if len(raw) == 0 {
		return input
	}
	_ = json.Unmarshal(raw, &input)
	return input
}
