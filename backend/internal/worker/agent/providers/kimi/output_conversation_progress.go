package kimi

import (
	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

func kimiConversationProgressID(frame kimiConversationFrame, itemID, kind string) string {
	role := contracts.KimiConversationRecordRoleItem
	if kind == contracts.KimiConversationItemKindToolCall {
		role = contracts.KimiConversationRecordRoleRequest
	}
	view := kimiConversationRecordView{
		record: contracts.KimiConversationRecord{Type: contracts.KimiConversationRecordKindItem,
			SessionID: frame.SessionID, AgentID: frame.AgentID, TurnID: frame.TurnID, Role: role},
		item: &kimiConversationItem{ItemID: itemID},
	}
	return view.key()
}

// Delta publication reads only the accumulator needed by that field. In
// particular, output deltas never encode the growing native tool item.
func (publication *kimiConversationTurnOutput) reportDelta(frame kimiConversationFrame, state *kimiConversationItemState) {
	if publication.progress == nil || publication.historical || publication.completed || frame.Delta == nil {
		return
	}
	scopeID := kimiConversationProgressID(frame, state.identity.itemID, state.identity.kind)
	switch frame.Field {
	case "text", "input_text":
		publication.progress.ReportProgress(agent.ModelTextProgress(scopeID, *frame.Delta))
	case "output":
		if state.output != nil {
			publication.reportOutput(scopeID, state.output)
		}
	}
}

func (publication *kimiConversationTurnOutput) reportOutput(scopeID string, output *kimiConversationToolOutput) {
	tail, clipped := agent.ClipTailBytes(output.text, kimiLiveOutputLimit)
	publication.progress.ReportProgress(agent.OutputExactTotalProgress(scopeID, output.total))
	publication.progress.ReportProgress(agent.OutputTailProgress(scopeID, tail, clipped || output.truncated()))
}

// A full item replaces a prior live view. Its first text includes the first
// streamed chunk, while later deltas carry only their appended text.
func (publication *kimiConversationTurnOutput) reportItem(frame kimiConversationFrame, item kimiConversationItem, output *kimiConversationToolOutput) {
	if publication.progress == nil || publication.historical || publication.completed {
		return
	}
	scopeID := kimiConversationProgressID(frame, item.ItemID, item.Type)
	full := frame.Type == "item.started" || frame.Type == "item.ended"
	if full && item.Text != nil {
		publication.progress.ReportProgress(agent.ResetModelScopeProgress(scopeID))
		publication.progress.ReportProgress(agent.ModelTextProgress(scopeID, *item.Text))
	}
	if item.Type == contracts.KimiConversationItemKindToolCall {
		_, inputChanged := frame.Set["input_text"]
		if item.InputText != nil && (full || inputChanged) {
			publication.progress.ReportProgress(agent.ResetModelScopeProgress(scopeID))
			publication.progress.ReportProgress(agent.ModelTextProgress(scopeID, *item.InputText))
		}
		_, outputChanged := frame.Set["output"]
		if output != nil && (full || outputChanged) {
			publication.reportOutput(scopeID, output)
		}
	}
	if item.Status == contracts.KimiConversationStatusEnded {
		publication.progress.ReportProgress(agent.CompleteModelProgress(scopeID))
		if item.Type == contracts.KimiConversationItemKindToolCall {
			publication.progress.ReportProgress(agent.CompleteOutputProgress(scopeID))
		}
	}
}
