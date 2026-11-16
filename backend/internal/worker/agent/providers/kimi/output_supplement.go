package kimi

import (
	"encoding/json"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// kimiRetainedTranscriptToolContent preserves a native record and scopes its collected output.
// An explicit native output, including null or an empty string, remains authoritative.
func kimiRetainedTranscriptToolContent(record []byte, output string, truncated bool) (agent.MessageContent, bool) {
	content := agent.MessageContent{Original: record}
	row, native := readKimiTranscriptRecord(record)
	if !native {
		return content, false
	}
	op := row.operation
	var frame kimiTranscriptToolFrame
	if op.Op != contracts.KimiTranscriptOperationFrameUpsert || json.Unmarshal(op.Frame, &frame) != nil || frame.Kind != contracts.KimiTranscriptFrameTool || len(frame.Output) != 0 || output == "" {
		return content, true
	}
	identity := kimiTranscriptToolIdentity{sessionID: row.sessionID, agentID: row.agentID, turnID: op.TurnID, stepID: op.StepID, frameID: frame.FrameID, toolCallID: frame.ToolCallID}
	supplement, err := identity.supplement(contracts.KimiTranscriptSupplementToolOutput, map[string]any{
		contracts.KimiTranscriptSupplementText:      output,
		contracts.KimiTranscriptSupplementTruncated: truncated,
	}, agent.LiveMaxMessageSize()-len(record)-1024)
	if err != nil {
		slog.Warn("retain Kimi tool output", "error", err)
		return content, true
	}
	content.Supplemental = supplement
	return content, true
}
