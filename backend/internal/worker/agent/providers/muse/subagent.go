package muse

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
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

// A ready result ends the subagent's current task even while its native item
// remains alive. A control word alone supplies no outcome.
func resolvedChildStatus(item nativeItem) bgtask.Status {
	status := childStatus(item.Status)
	if item.Kind != museItemSubagent || item.Status != contracts.MuseItemStatusInProgress || item.ControlStatus != "resultReady" {
		return status
	}
	var result struct {
		Summary      *string         `json:"summary"`
		EvidenceRefs *[]*string      `json:"evidenceRefs"`
		ArtifactRefs *[]*string      `json:"artifactRefs"`
		ErrorKind    json.RawMessage `json:"errorKind"`
	}
	if json.Unmarshal(item.Result, &result) != nil || result.Summary == nil || result.EvidenceRefs == nil || result.ArtifactRefs == nil {
		return status
	}
	for _, refs := range [][]*string{*result.EvidenceRefs, *result.ArtifactRefs} {
		for _, ref := range refs {
			if ref == nil {
				return status
			}
		}
	}
	errorKind := ""
	if len(result.ErrorKind) > 0 {
		var value *string
		if json.Unmarshal(result.ErrorKind, &value) != nil || value == nil {
			return status
		}
		errorKind = *value
	}
	switch errorKind {
	case "":
		return bgtask.StatusSucceeded
	case "generated_child_cancelled":
		return bgtask.StatusStopped
	default:
		return bgtask.StatusFailed
	}
}

func museChildRowKey(parentSessionID, subagentID string) string {
	return bgtask.NormalizeRowKey(parentSessionID + ":child:" + subagentID)
}

// One entry of a workflow item's children fold: the durable child of the run,
// identified by its childId alone. A separate subagent item can supply the
// child's addressable session and controls.
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
// each fold child, grouped under the run's entry name. An addressable subagent
// item joins that same child row when it supplies the native session.
func (a *Agent) observeWorkflow(params itemParams, item nativeItem) error {
	if item.WorkflowRunID == "" {
		return nil
	}
	group := bgtask.NormalizeRowKey(params.SessionID + ":workflow:" + item.WorkflowRunID)
	name := item.EntryID
	if name == "" {
		name = item.WorkflowRunID
	}
	if err := a.sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: group, Kind: bgtask.KindWorkflow, GroupKey: group, GroupLabel: name, Title: name, Status: childStatus(item.Status)}); err != nil {
		return err
	}
	var children []nativeWorkflowChild
	if json.Unmarshal(item.Children, &children) != nil {
		return nil
	}
	var publishErr error
	for _, child := range children {
		if child.ChildID == "" {
			continue
		}
		key := museChildRowKey(params.SessionID, child.ChildID)
		title := child.Label
		if title == "" && a.childForKey(key) == nil {
			title = "Child " + child.ChildID
		}
		if err := a.sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: key, Kind: bgtask.KindSubagent, GroupKey: group, GroupLabel: name, Title: title, Status: workflowChildStatus(child)}); err != nil {
			publishErr = errors.Join(publishErr, err)
		}
	}
	return publishErr
}

func (a *Agent) observeChildOrTask(params itemParams, parent *sessionState, previous nativeItem) error {
	item := params.Item
	if item.Kind == contracts.MuseItemKindToolCall && item.Background {
		key := bgtask.NormalizeRowKey(params.SessionID + ":task:" + item.ID)
		return a.sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: key, Kind: bgtask.KindShell, Title: item.Tool, Status: resolvedChildStatus(item)})
	}
	if item.Kind == contracts.MuseItemKindWorkflow {
		return a.observeWorkflow(params, item)
	}
	if item.ChildSessionID == "" || item.SubagentID == "" {
		return nil
	}
	key := museChildRowKey(params.SessionID, item.SubagentID)
	a.stateMu.Lock()
	child := a.sessions[item.ChildSessionID]
	a.stateMu.Unlock()
	created := child == nil
	var promptErr error
	if created {
		id, err := parent.sink.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: item.ID, ProviderChildKey: key, AgentSessionID: item.ChildSessionID, Title: item.Objective, Options: optionmap.Map{}})
		if err != nil {
			return err
		}
		child = &sessionState{sink: parent.sink.ChildSink(id), items: make(map[string]*itemState), completed: make(map[string]bool), log: newNativeLog(item.ChildSessionID), childKey: key, childID: id, parentSessionID: params.SessionID, subagentID: item.SubagentID}
		a.stateMu.Lock()
		a.sessions[item.ChildSessionID] = child
		a.stateMu.Unlock()
		promptErr = parent.sink.PersistChildPrompt(id, item.Objective)
		if agent.OutputRetained(promptErr) {
			promptErr = nil
		}
	}
	group := ""
	if item.WorkflowRunID != "" {
		group = bgtask.NormalizeRowKey(params.SessionID + ":workflow:" + item.WorkflowRunID)
	}
	publishErr := a.sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: key, Kind: bgtask.KindSubagent, ChildAgentID: child.childID, ParentAgentID: parent.childID, GroupKey: group, Title: item.Objective, Description: item.Role, Status: resolvedChildStatus(item)})
	if created || previous.Kind != "" && (previous.ChildSessionID != item.ChildSessionID || previous.ControlStatus != item.ControlStatus) {
		a.requestChildView(item.ChildSessionID)
	}
	return errors.Join(promptErr, publishErr)
}

func (a *Agent) requestChildView(id string) {
	a.stateMu.Lock()
	state := a.sessions[id]
	if !a.currentSessionState(id, state) || state.childID == "" {
		a.stateMu.Unlock()
		return
	}
	state.childViewRequested = true
	if state.childViewPrefix == childViewPrefixUnrequested {
		state.childViewPrefix = childViewPrefixPending
	}
	a.stateMu.Unlock()
	a.viewWork.Run(id, func() { a.syncView(id) })
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

// The caller holds stateMu. Retired parents cannot address their former children.
func (a *Agent) ownedChildRoute(key string) (string, *sessionState) {
	for id, state := range a.sessions {
		if state.childKey == key && a.currentSessionState(id, state) && a.currentSessionState(state.parentSessionID, a.sessions[state.parentSessionID]) {
			return id, state
		}
	}
	return "", nil
}
func (a *Agent) ActiveChildTurnState(key string) agent.TurnState {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	_, state := a.ownedChildRoute(key)
	active := state != nil && state.turnID != ""
	return agent.TurnState{Active: active, Steerable: active}
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
	a.stateMu.Lock()
	id, child := a.ownedChildRoute(key)
	busy := child != nil && child.turnID != ""
	a.stateMu.Unlock()
	if child == nil || a.IsStopped() {
		return agent.ErrChildRouteNotReady
	}
	for _, attachment := range agent.ClassifyAttachments(attachments) {
		if attachment.Kind != contracts.AttachmentMediaKindText {
			return fmt.Errorf("the Muse host child messages accept text attachments only")
		}
		text += "\n\n" + providerkit.BuildInlineTextAttachmentBlock(attachment)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("the Muse child message is empty")
	}
	method := methodChildFollowup
	if steer {
		if !busy {
			return agent.ErrNoActiveTurn
		}
		method = methodChildSend
	} else if busy {
		return agent.ErrAgentBusy
	}
	err := a.commandChild(key, method, map[string]any{"sessionId": child.parentSessionID, "subagentId": child.subagentID, "body": text})
	if err == nil {
		a.requestChildView(id)
	}
	return err
}

// The caller holds sendMu. A retry with the same target, method and body keeps
// the native command UUID until the host confirms admission or refusal.
func (a *Agent) commandChild(key, method string, params map[string]any) error {
	encoded, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("encode the Muse child command: %w", err)
	}
	digest := fmt.Sprintf("%s:%x", method, sha256.Sum256(encoded))
	payload, commandID, err := commandParams(params)
	if err != nil {
		return err
	}
	if prior, ok := a.uncertainChildCommands[key]; ok && prior.digest == digest {
		commandID = prior.commandID
	}
	delete(a.uncertainChildCommands, key)
	_, err = a.commandWithID(method, payload, commandID, a.APITimeout(), nil)
	if err != nil {
		err = providerkit.ClassifyJSONRPCDeliveryError(method, err)
		if errors.Is(err, agent.ErrDeliveryUncertain) {
			if a.uncertainChildCommands == nil {
				a.uncertainChildCommands = make(map[string]museUncertainInput)
			}
			a.uncertainChildCommands[key] = museUncertainInput{digest: digest, commandID: commandID}
		}
	}
	return err
}
func (a *Agent) InterruptChild(key string, stop agent.StopContext) error {
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	a.stateMu.Lock()
	_, child := a.ownedChildRoute(key)
	a.stateMu.Unlock()
	if child == nil || a.IsStopped() {
		return agent.ErrChildRouteNotReady
	}
	return a.commandChild(key, methodChildInterrupt, map[string]any{"sessionId": child.parentSessionID, "subagentId": child.subagentID})
}

// childRouteHistoryItems reads the native folded item forms. A budgeted or
// unsupported snapshot requires the protocol's paged view instead.
func childRouteHistoryItems(raw json.RawMessage, head string) ([]nativeItem, bool) {
	var history struct {
		Mode     string        `json:"mode"`
		Items    *[]nativeItem `json:"items"`
		Snapshot *struct {
			SchemaVersion int    `json:"schemaVersion"`
			ViewCursor    string `json:"viewCursor"`
			State         struct {
				Items *[]nativeItem `json:"items"`
			} `json:"state"`
		} `json:"snapshot"`
	}
	if json.Unmarshal(raw, &history) != nil {
		return nil, false
	}
	switch history.Mode {
	case "inline":
		if history.Items != nil {
			return *history.Items, true
		}
	case "snapshot", "anchoredSnapshot":
		if snapshot := history.Snapshot; snapshot != nil && snapshot.SchemaVersion == 1 && snapshot.ViewCursor == head && snapshot.State.Items != nil {
			return *snapshot.State.Items, true
		}
	}
	return nil, false
}

// restoreChildRoutes rebuilds child ownership from native metadata without
// replaying a parent conversation that the worker already stores.
func (a *Agent) restoreChildRoutes(id string, history json.RawMessage, head string) error {
	items, served := childRouteHistoryItems(history, head)
	if !served {
		events, err := a.readViewPages(id, "", head)
		if err != nil {
			return err
		}
		for _, raw := range events {
			var event struct {
				Params itemParams `json:"params"`
			}
			if json.Unmarshal(raw, &event) != nil {
				return fmt.Errorf("the native Muse child history is invalid")
			}
			items = append(items, event.Params.Item)
		}
	}
	latest := make(map[string]nativeItem)
	var order []string
	for _, item := range items {
		if item.ID == "" || item.Revision <= 0 || (item.Kind != museItemSubagent && item.Kind != contracts.MuseItemKindWorkflow) {
			continue
		}
		previous, seen := latest[item.ID]
		if !seen {
			order = append(order, item.ID)
		}
		if !seen || item.Revision > previous.Revision {
			latest[item.ID] = item
		}
	}
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	a.stateMu.Lock()
	parent := a.sessions[id]
	current := id == a.sessionID && a.currentSessionState(id, parent)
	a.stateMu.Unlock()
	if !current {
		return agent.ErrChildRouteNotReady
	}
	var restoreErr error
	for _, key := range order {
		item := latest[key]
		a.stateMu.Lock()
		previous := parent.items[key]
		if previous != nil && previous.params.Item.Revision >= item.Revision {
			a.stateMu.Unlock()
			continue
		}
		params := itemParams{SessionID: id, Item: item}
		// Subagent items are silent metadata. Rendered workflow items must still
		// arrive through their original native frames before a revision is stored.
		if item.Kind == museItemSubagent {
			parent.items[key] = &itemState{params: cloneItemParams(params), persistedRevision: item.Revision, deltas: make(map[string]string), cursors: make(map[string]bool), order: parent.nextItemOrder}
			parent.nextItemOrder++
		}
		a.stateMu.Unlock()
		var prior nativeItem
		if previous != nil {
			prior = previous.params.Item
		}
		restoreErr = errors.Join(restoreErr, a.observeChildOrTask(params, parent, prior))
	}
	return restoreErr
}
