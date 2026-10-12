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

// One entry of a workflow item's children fold: the durable child of the run,
// identified by its childId alone. The fold carries no child session, so no
// transcript of the child's own exists to subscribe to.
type nativeWorkflowChild struct {
	ChildID  string `json:"childId"`
	Attempt  int64  `json:"attempt"`
	Status   string `json:"status"`
	Terminal string `json:"terminal"`
	Label    string `json:"label"`
}

// The lifecycle words a workflow child's fold states, from the workflow item
// fold of the installed host. The words are the host's own vocabulary.
const (
	workflowChildScheduled = "scheduled"
	workflowChildStarted   = "started"
	workflowChildUsage     = "usage"
	workflowChildTerminal  = "terminal"
)

func workflowChildStatus(child nativeWorkflowChild) bgtask.Status {
	switch child.Status {
	case workflowChildScheduled:
		return bgtask.StatusPending
	case workflowChildStarted, workflowChildUsage:
		return bgtask.StatusRunning
	case workflowChildTerminal:
		if child.Terminal == contracts.MuseItemStatusCompleted {
			return bgtask.StatusSucceeded
		}
		if child.Terminal == contracts.MuseItemStatusCancelled {
			return bgtask.StatusStopped
		}
		return bgtask.StatusFailed
	default:
		return bgtask.StatusEndedWithUnknownOutcome
	}
}

// A workflow runs inside its own item: one registry row for the run, one for
// each fold child, grouped under the run's entry name. The fold's children
// own no session, so their rows open no transcript.
func (a *Agent) observeWorkflow(params itemParams, item nativeItem) {
	if item.WorkflowRunID == "" {
		return
	}
	group := bgtask.NormalizeRowKey(params.SessionID + ":workflow:" + item.WorkflowRunID)
	name := item.EntryID
	if name == "" {
		name = item.WorkflowRunID
	}
	if err := a.sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: group, Kind: bgtask.KindWorkflow, GroupKey: group, GroupLabel: name, Title: name, Status: childStatus(item.Status)}); err != nil {
		slog.Warn("publish a Muse workflow row", "error", err)
	}
	var children []nativeWorkflowChild
	if json.Unmarshal(item.Children, &children) != nil {
		return
	}
	for _, child := range children {
		if child.ChildID == "" {
			continue
		}
		title := child.Label
		if title == "" {
			title = "Child " + child.ChildID
		}
		key := bgtask.NormalizeRowKey(params.SessionID + ":wchild:" + child.ChildID)
		if err := a.sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: key, Kind: bgtask.KindWorkflow, GroupKey: group, GroupLabel: name, Title: title, Status: workflowChildStatus(child)}); err != nil {
			slog.Warn("publish a Muse workflow child row", "error", err)
		}
	}
}

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
		a.observeWorkflow(params, item)
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
