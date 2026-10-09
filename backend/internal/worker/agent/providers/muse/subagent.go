package muse

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

func childStatus(status string) bgtask.Status {
	switch status {
	case contracts.MuseItemStatusInProgress:
		return bgtask.StatusRunning
	case contracts.MuseItemStatusFailed, contracts.MuseItemStatusRejected, contracts.MuseItemStatusTimedOut:
		return bgtask.StatusFailed
	case contracts.MuseItemStatusCancelled:
		return bgtask.StatusStopped
	case contracts.MuseItemStatusCompleted:
		return bgtask.StatusSucceeded
	default:
		return bgtask.StatusEndedWithUnknownOutcome
	}
}

// The generic native status determines finality independently of child control state.
func resolvedChildStatus(item nativeItem) bgtask.Status { return childStatus(item.Status) }
func (a *Agent) observeChildOrTask(params itemParams, parent *sessionState) {
	item := params.Item
	if item.Kind == contracts.MuseItemKindToolCall && item.Background {
		key := bgtask.NormalizeRowKey(params.SessionID + ":task:" + item.ID)
		if err := a.sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: key, Kind: bgtask.KindShell, Title: item.Tool, Status: resolvedChildStatus(item)}); err != nil {
			slog.Warn("publish a Muse background task", "error", err)
		}
		return
	}
	if item.Kind == contracts.MuseItemKindWorkflow {
		var children []nativeItem
		if json.Unmarshal(item.Children, &children) == nil {
			for _, child := range children {
				if child.ChildSessionID != "" {
					child.WorkflowRunID = item.WorkflowRunID
					a.observeChildOrTask(itemParams{SessionID: params.SessionID, Item: child}, parent)
				}
			}
		}
		return
	}
	if item.ChildSessionID == "" || item.SubagentID == "" {
		return
	}
	key := bgtask.NormalizeRowKey(params.SessionID + ":child:" + item.SubagentID)
	a.stateMu.Lock()
	child := a.sessions[item.ChildSessionID]
	a.stateMu.Unlock()
	if child == nil {
		id, err := parent.sink.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: item.ID, ProviderChildKey: key, AgentSessionID: item.ChildSessionID, Title: item.Objective, Options: optionmap.Map{}})
		if err != nil {
			slog.Warn("open a Muse child transcript", "error", err)
			return
		}
		child = &sessionState{sink: parent.sink.ChildSink(id), items: make(map[string]*itemState), completed: make(map[string]bool), log: newNativeLog(item.ChildSessionID), childKey: key, childID: id, parentSessionID: params.SessionID, subagentID: item.SubagentID}
		a.stateMu.Lock()
		a.sessions[item.ChildSessionID] = child
		a.stateMu.Unlock()
		if err := parent.sink.PersistChildPrompt(id, item.Objective); err != nil {
			slog.Warn("persist a Muse child prompt", "error", err)
		}
		go a.subscribeChild(item.ChildSessionID)
	}
	status := resolvedChildStatus(item)
	group := ""
	if item.WorkflowRunID != "" {
		group = bgtask.NormalizeRowKey(params.SessionID + ":workflow:" + item.WorkflowRunID)
	}
	if err := a.sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: key, Kind: bgtask.KindSubagent, ChildAgentID: child.childID, ParentAgentID: parent.childID, GroupKey: group, GroupLabel: item.WorkflowRunID, Title: item.Objective, Description: item.Role, Status: status}); err != nil {
		slog.Warn("publish a Muse child task", "error", err)
	}
}
func (a *Agent) subscribeChild(id string) {
	_, err := a.request(methodViewSubscribe, map[string]string{"sessionId": id}, a.APITimeout(), nil)
	if err != nil {
		slog.Warn("subscribe to a Muse child transcript", "session", id, "error", err)
		return
	}
	_ = a.subscribeLog(id, a.APITimeout())
	// Read all durable history in native order before the live tail.
	events, err := a.readViewPages(id, "", "")
	if err != nil {
		slog.Warn("read a Muse child transcript", "error", err)
		return
	}
	for _, event := range events {
		a.HandleOutput(event)
	}
}
func (a *Agent) childForKey(key string) *sessionState {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	for _, state := range a.sessions {
		if state.childKey == key {
			return state
		}
	}
	return nil
}
func (a *Agent) ActiveChildTurnState(key string) agent.TurnState {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	for _, state := range a.sessions {
		if state.childKey == key {
			active := state.turnID != ""
			return agent.TurnState{Active: active, Steerable: active}
		}
	}
	return agent.TurnState{}
}
func (a *Agent) SendChildInput(key, text string, attachments []*leapmuxv1.Attachment) error {
	return a.childInput(key, text, attachments, false)
}
func (a *Agent) SteerChildInput(key, text string, attachments []*leapmuxv1.Attachment) error {
	return a.childInput(key, text, attachments, true)
}
func (a *Agent) childInput(key, text string, attachments []*leapmuxv1.Attachment, steer bool) error {
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	child := a.childForKey(key)
	if child == nil {
		return agent.ErrChildRouteNotReady
	}
	for _, attachment := range agent.ClassifyAttachments(attachments) {
		if attachment.Kind != agent.AttachmentKindText {
			return fmt.Errorf("the Muse host child messages accept text attachments only")
		}
		text += "\n\n" + providerkit.BuildInlineTextAttachmentBlock(attachment)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("the Muse child message is empty")
	}
	a.stateMu.Lock()
	busy := child.turnID != ""
	a.stateMu.Unlock()
	method := methodChildFollowup
	if steer {
		if !busy {
			return agent.ErrNoActiveTurn
		}
		method = methodChildSend
	} else if busy {
		return agent.ErrAgentBusy
	}
	_, err := a.command(method, map[string]any{"sessionId": child.parentSessionID, "subagentId": child.subagentID, "body": text}, a.APITimeout(), nil)
	return err
}
func (a *Agent) InterruptChild(key string, stop agent.StopContext) error {
	child := a.childForKey(key)
	if child == nil {
		return agent.ErrChildRouteNotReady
	}
	_, err := a.command(methodChildInterrupt, map[string]any{"sessionId": child.parentSessionID, "subagentId": child.subagentID}, a.APITimeout(), nil)
	return err
}
