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

func (publication *kimiConversationTurnOutput) toolCallState(itemID string) (opened, closed bool) {
	_, opened = publication.requests[itemID]
	_, closed = publication.results[itemID]
	return opened, closed
}

// reportProgressUpdate routes turn-scoped progress during the active turn and
// session-scoped progress for a cross-turn open tool whose owning turn ended.
// Turn completion rotates the sink's turn scope, so the turn's captured
// TranscriptPublication rejects updates once the turn finishes even though the
// native session remains current.
func (publication *kimiConversationTurnOutput) reportProgressUpdate(update agent.ProgressUpdate) {
	if !publication.completed {
		publication.progress.ReportProgress(update)
		return
	}
	if publication.writer == nil {
		return
	}
	if owner := publication.progress.Owner(); owner == nil || !owner.IsSessionCurrent() {
		return
	}
	publication.writer.ReportProgress(update)
}

// Delta publication reads only the accumulator needed by that field. In
// particular, output deltas never encode the growing native tool item.
func (publication *kimiConversationTurnOutput) reportDelta(frame kimiConversationFrame, state *kimiConversationItemState) {
	if publication.progress == nil || publication.historical || frame.Delta == nil {
		return
	}
	isTool := state.identity.kind == contracts.KimiConversationItemKindToolCall
	var opened, closed bool
	if isTool {
		opened, closed = publication.toolCallState(state.identity.itemID)
		if closed {
			return
		}
	}
	if publication.completed && (!isTool || !opened || frame.Field != "output") {
		return
	}
	scopeID := kimiConversationProgressID(frame, state.identity.itemID, state.identity.kind)
	switch frame.Field {
	case "text", "input_text":
		publication.reportProgressUpdate(agent.ModelTextProgress(scopeID, *frame.Delta))
	case "output":
		if state.output != nil {
			publication.reportOutput(scopeID, state.output)
		}
	}
}

func (publication *kimiConversationTurnOutput) reportOutput(scopeID string, output *kimiConversationToolOutput) {
	tail, clipped := agent.ClipTailBytes(output.text, kimiLiveOutputLimit)
	publication.reportProgressUpdate(agent.OutputExactTotalProgress(scopeID, output.total))
	publication.reportProgressUpdate(agent.OutputTailProgress(scopeID, tail, clipped || output.truncated()))
}

// A full item replaces a prior live view. Its first text includes the first
// streamed chunk, while later deltas carry only their appended text.
func (publication *kimiConversationTurnOutput) reportItem(frame kimiConversationFrame, item kimiConversationItem, output *kimiConversationToolOutput) {
	if publication.progress == nil || publication.historical {
		return
	}
	isTool := item.Type == contracts.KimiConversationItemKindToolCall
	var opened, closed bool
	if isTool {
		opened, closed = publication.toolCallState(item.ItemID)
		if closed {
			return
		}
	}
	if publication.completed && (!isTool || !opened) {
		return
	}
	scopeID := kimiConversationProgressID(frame, item.ItemID, item.Type)
	full := frame.Type == "item.started" || frame.Type == "item.ended"
	if !publication.completed && full && item.Text != nil {
		publication.reportProgressUpdate(agent.ResetModelScopeProgress(scopeID))
		publication.reportProgressUpdate(agent.ModelTextProgress(scopeID, *item.Text))
	}
	if isTool {
		if !publication.completed {
			_, inputChanged := frame.Set["input_text"]
			if item.InputText != nil && (full || inputChanged) {
				publication.reportProgressUpdate(agent.ResetModelScopeProgress(scopeID))
				publication.reportProgressUpdate(agent.ModelTextProgress(scopeID, *item.InputText))
			}
		}
		_, outputChanged := frame.Set["output"]
		if output != nil && (full || outputChanged) {
			publication.reportOutput(scopeID, output)
		}
	}
	if item.Status == contracts.KimiConversationStatusEnded {
		publication.reportProgressUpdate(agent.CompleteModelProgress(scopeID))
		if isTool {
			publication.reportProgressUpdate(agent.CompleteOutputProgress(scopeID))
		}
	}
}
