package dirac

import (
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

// diracSubagentFromToolCall maps Dirac's aggregate `use_subagents` card to a
// SUBAGENT registry row. Dirac stamps the model-facing tool name on
// `rawInput.tool`, which is the one field of the call that names the tool:
// `title` is the card's display label ("Run Subagents"). Dirac runs its
// children behind one card, so the row is registry-only: no child transcript
// opens, and the row's activity is the card's own title.
func diracSubagentFromToolCall(tc acp.ToolCallEnvelope) *acp.SubagentObservation {
	if diracRawInputTool(tc.RawInput) != contracts.DiracToolUseSubagents {
		return nil
	}
	title := tc.Title
	if title == "" {
		title = contracts.DiracToolUseSubagents
	}
	return &acp.SubagentObservation{
		RowKey:   tc.ToolCallID,
		Title:    title,
		Activity: title,
	}
}

// diracRawInputTool reads the `tool` field Dirac stamps on every raw input.
// An input that is not a Dirac tool payload answers "".
func diracRawInputTool(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var input struct {
		Tool string `json:"tool"`
	}
	if json.Unmarshal(raw, &input) != nil {
		return ""
	}
	return input.Tool
}
