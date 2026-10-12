package junie

import (
	"encoding/json"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

// junieLiveOutputLimit is the longest live text that one running command keeps
// for its row. The sink caps the tail again before it broadcasts.
const junieLiveOutputLimit = 8192

// junieToolOutput reads the live output of a running shell command.
//
// Junie streams that output in the cumulative `rawOutput.output` of the
// in_progress updates of the call, and states no content in them. The shared
// content path reads only content, so without this hook the byte counter of a
// running command stays at zero until the command ends. The snapshot is the
// whole output so far, so its length is the exact total and its end is the tail.
//
// A completed update is not live output. The shared path stores it as the
// result of the call. An update with no `output` text is not claimed.
func (a *Agent) junieToolOutput(update acp.ToolCallUpdateEnvelope) (acp.ToolOutputObservation, bool) {
	if acp.StatusIsFinal(update.Status) || len(update.RawOutput) == 0 {
		return acp.ToolOutputObservation{}, false
	}
	var raw struct {
		Output string `json:"output"`
	}
	if json.Unmarshal(update.RawOutput, &raw) != nil || raw.Output == "" {
		return acp.ToolOutputObservation{}, false
	}
	observed := a.ObserveCumulativeOutput(update.ToolCallID, raw.Output, false)
	tail, clipped := agent.ClipTailBytes(raw.Output, junieLiveOutputLimit)
	return acp.ToolOutputObservation{
		Total: observed.Total, TotalIsMinimum: observed.Minimum,
		Tail: tail, TailLost: clipped,
	}, true
}
