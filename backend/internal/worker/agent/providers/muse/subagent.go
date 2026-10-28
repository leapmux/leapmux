package muse

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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
	if len(result.ErrorKind) == 0 {
		return bgtask.StatusSucceeded
	}
	var errorKind *string
	if json.Unmarshal(result.ErrorKind, &errorKind) != nil || errorKind == nil {
		return status
	}
	if *errorKind == "generated_child_cancelled" {
		return bgtask.StatusStopped
	}
	return bgtask.StatusFailed
}

func museChildRowKey(parentSessionID, subagentID string) string {
	return bgtask.NormalizeRowKey(parentSessionID + ":child:" + subagentID)
}

func museWorkflowRowKey(sessionID, runID string) string {
	return bgtask.NormalizeRowKey(sessionID + ":workflow:" + runID)
}

func museBackgroundTaskRowKey(sessionID, itemID string) string {
	return bgtask.NormalizeRowKey(sessionID + ":task:" + itemID)
}

func museRegistryItem(item nativeItem) bool {
	return item.Kind == museItemSubagent || item.Kind == contracts.MuseItemKindWorkflow || item.Kind == contracts.MuseItemKindToolCall && item.Background
}

func (a *Agent) observeChildOrTask(params itemParams, parent *sessionState, previous nativeItem) error {
	a.stateMu.Lock()
	item := parent.items[params.Item.ID]
	if item == nil || item.params.Item.Revision != params.Item.Revision {
		a.stateMu.Unlock()
		return fmt.Errorf("the Muse registry item is not canonical")
	}
	publication := item.registryState()
	a.stateMu.Unlock()
	publication.mu.Lock()
	head := publication.registryHeadRevision == params.Item.Revision && publication.registryReconciledRevision < params.Item.Revision
	alreadyCommitted := publication.registryRevision >= params.Item.Revision
	alreadyQueued := publication.queuedRevision >= params.Item.Revision
	headQueued := publication.queuedHeadRevision >= params.Item.Revision
	publication.mu.Unlock()
	if (alreadyCommitted || alreadyQueued) && (!head || headQueued) {
		return nil
	}
	plan, err := a.prepareChildOrTaskLocked(params, parent, publication, previous, head)
	if err != nil {
		return err
	}
	admissionErr := a.queueRegistryBatchLocked(&nativeRegistryBatch{writer: a.sink, publication: publication, revision: params.Item.Revision, head: head, rows: plan.rows})
	return errors.Join(plan.setupErr, admissionErr)
}

func (a *Agent) prepareChildOrTaskLocked(params itemParams, parent *sessionState, publication *registryPublication, previous nativeItem, head bool) (nativeRegistryPlan, error) {
	item := params.Item
	if item.Kind == contracts.MuseItemKindToolCall && item.Background {
		key := museBackgroundTaskRowKey(params.SessionID, item.ID)
		status := resolvedChildStatus(item)
		return nativeRegistryPlan{rows: []nativeRegistryRow{{upsert: bgtask.Upsert{RowKey: key, Kind: bgtask.KindShell, ParentAgentID: parent.childID, Title: item.Tool, Status: status}, revive: head && status.IsWorking()}}}, nil
	}
	if item.Kind == contracts.MuseItemKindWorkflow {
		return a.prepareWorkflowRegistryLocked(params, parent, publication, previous, head)
	}
	if item.ChildSessionID == "" || item.SubagentID == "" {
		return nativeRegistryPlan{}, nil
	}
	authorities, err := a.workflowChildAuthorities(params.SessionID, parent)
	if err != nil {
		return nativeRegistryPlan{}, err
	}
	authority, hasFold := authorities[item.SubagentID]
	if hasFold && item.WorkflowRunID != "" && item.WorkflowRunID != authority.runID {
		return nativeRegistryPlan{}, fmt.Errorf("the Muse child disagrees with its workflow owner")
	}
	group, label := authority.group, authority.label
	key := museChildRowKey(params.SessionID, item.SubagentID)
	a.stateMu.Lock()
	child := a.sessions[item.ChildSessionID]
	a.stateMu.Unlock()
	created := child == nil
	var promptErr error
	if created {
		id, err := parent.sink.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: item.ID, ProviderChildKey: key, AgentSessionID: item.ChildSessionID, Title: item.Objective, Options: optionmap.Map{}})
		if err != nil {
			return nativeRegistryPlan{}, err
		}
		child = &sessionState{sink: parent.sink.ChildSink(id), items: make(map[string]*itemState), completed: make(map[string]turnCompletionState), log: newNativeLog(item.ChildSessionID), childKey: key, childID: id, parentSessionID: params.SessionID, subagentID: item.SubagentID, childViewPrefix: childViewPrefixPending, viewRecovery: &viewRecovery{}, openingPrompt: &childOpeningPrompt{writer: parent.sink, childAgentID: id, text: item.Objective}}
		a.stateMu.Lock()
		a.sessions[item.ChildSessionID] = child
		a.stateMu.Unlock()
		promptErr = parent.sink.PersistChildPrompt(id, item.Objective)
		if agent.OutputRetained(promptErr) {
			a.stateMu.Lock()
			child.openingPrompt = nil
			a.stateMu.Unlock()
			promptErr = nil
		} else {
			a.stateMu.Lock()
			child.viewRecovery = nil
			child.childViewRequested = false
			a.stateMu.Unlock()
			a.failChildProjectionLocked(item.ChildSessionID, child, promptErr, nil)
		}
	}
	if group == "" && item.WorkflowRunID != "" {
		group = museWorkflowRowKey(params.SessionID, item.WorkflowRunID)
	}
	status := resolvedChildStatus(item)
	if hasFold {
		status = workflowChildStatus(authority.child)
	}
	plan := nativeRegistryPlan{setupErr: promptErr, rows: []nativeRegistryRow{{upsert: bgtask.Upsert{RowKey: key, Kind: bgtask.KindSubagent, ChildAgentID: child.childID, ParentAgentID: parent.childID, GroupKey: group, GroupLabel: label, Title: item.Objective, Description: item.Role, Status: status}, revive: head && !hasFold && status.IsWorking()}}}
	if promptErr == nil && (created || previous.Kind != "" && (previous.ChildSessionID != item.ChildSessionID || previous.ControlStatus != item.ControlStatus)) {
		a.requestChildView(item.ChildSessionID)
	}
	return plan, nil
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
	active := state != nil && state.childTurnActive()
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
	busy := child != nil && child.childTurnActive()
	projectionReady := child != nil && child.childViewPrefix == childViewPrefixReady && (child.projection == nil || child.projection.failed == nil && len(child.projection.frames) == 0 && len(child.projection.heads) == 0)
	a.stateMu.Unlock()
	if child == nil || a.IsStopped() {
		return agent.ErrChildRouteNotReady
	}
	if !projectionReady {
		a.requestChildView(id)
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
	// Muse 1.4.13 can refuse a human follow-up after a parent-driven follow-up,
	// even when the result is drained. Reading that drained result again does
	// not change eligibility, so the input queue must retain the native refusal.
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
func childRouteHistoryItems(raw json.RawMessage, head string) ([]json.RawMessage, bool) {
	var history struct {
		Mode     string             `json:"mode"`
		Items    *[]json.RawMessage `json:"items"`
		Snapshot *struct {
			SchemaVersion int    `json:"schemaVersion"`
			ViewCursor    string `json:"viewCursor"`
			State         struct {
				Items *[]json.RawMessage `json:"items"`
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

// restoreChildRoutes installs the complete native fold before projecting rows.
// Folded objects supply metadata, never invented notification frames.
func (a *Agent) restoreChildRoutes(id string, history json.RawMessage, head string) error {
	objects, served := childRouteHistoryItems(history, head)
	if !served {
		events, err := a.readViewPages(id, "", head)
		if err != nil {
			return err
		}
		reached := head == ""
		for _, raw := range events {
			var event struct {
				Method string `json:"method"`
				Params struct {
					SessionID string          `json:"sessionId"`
					Cursor    string          `json:"viewCursor"`
					Item      json.RawMessage `json:"item"`
				} `json:"params"`
			}
			if json.Unmarshal(raw, &event) != nil || event.Params.SessionID != id {
				return fmt.Errorf("the native Muse child history is invalid")
			}
			switch event.Method {
			case contracts.MuseMethodItemStarted, contracts.MuseMethodItemUpdated, contracts.MuseMethodItemCompleted:
				if len(event.Params.Item) == 0 {
					return fmt.Errorf("the native Muse history event has no item")
				}
				objects = append(objects, slices.Clone(event.Params.Item))
			}
			if head != "" && event.Params.Cursor == head {
				reached = true
			}
		}
		if !reached {
			return fmt.Errorf("the native Muse history did not reach its head")
		}
	}
	a.dispatcher.mu.Lock()
	a.stateMu.Lock()
	parent := a.sessions[id]
	root := id == a.sessionID && a.currentSessionState(id, parent)
	a.stateMu.Unlock()
	if !root {
		a.dispatcher.mu.Unlock()
		return agent.ErrChildRouteNotReady
	}
	err := a.installRegistryHeadLocked(id, parent, objects)
	a.dispatcher.mu.Unlock()
	a.drainDeferredOutputActions()
	a.drainControls()
	return err
}

// installRegistryHeadLocked reconciles a complete native head for one owned
// session. The caller holds dispatcher.mu and supplies its captured session state.
func (a *Agent) installRegistryHeadLocked(id string, parent *sessionState, objects []json.RawMessage) error {
	type restoredItem struct {
		item nativeItem
		raw  json.RawMessage
	}
	latest := make(map[string]restoredItem)
	var order []string
	for _, raw := range objects {
		var item nativeItem
		if json.Unmarshal(raw, &item) != nil {
			return fmt.Errorf("the native Muse history item is invalid")
		}
		if item.ID == "" || item.Revision <= 0 || !museRegistryItem(item) {
			continue
		}
		prior, seen := latest[item.ID]
		if !seen {
			order = append(order, item.ID)
		}
		if seen && item.Revision == prior.item.Revision && !agent.JSONCanonicalEqual(prior.raw, raw) {
			return fmt.Errorf("the native Muse history repeats an item revision with different content")
		}
		if seen && item.Revision != prior.item.Revision {
			older, newer := prior.item, item
			if older.Revision > newer.Revision {
				older, newer = newer, older
			}
			if err := validateWorkflowAttemptAdvance(older, newer); err != nil {
				return err
			}
		}
		if !seen || item.Revision > prior.item.Revision {
			latest[item.ID] = restoredItem{item, slices.Clone(raw)}
		}
	}
	a.stateMu.Lock()
	if !a.currentSessionState(id, parent) {
		a.stateMu.Unlock()
		return agent.ErrChildRouteNotReady
	}
	for _, key := range order {
		restored := latest[key]
		current := parent.items[key]
		if current != nil && current.params.Item.Revision == restored.item.Revision && !agent.JSONCanonicalEqual(current.nativeObject(), restored.raw) {
			a.stateMu.Unlock()
			return fmt.Errorf("the native Muse history conflicts with the current item revision")
		}
		if current != nil && restored.item.Revision > current.params.Item.Revision {
			if err := validateWorkflowAttemptAdvance(current.params.Item, restored.item); err != nil {
				a.stateMu.Unlock()
				return err
			}
		} else if current == nil {
			if err := validateWorkflowAttemptAdvance(nativeItem{}, restored.item); err != nil {
				a.stateMu.Unlock()
				return err
			}
		}
	}
	previous := make(map[string]nativeItem, len(order))
	var publish []string
	for _, key := range order {
		restored := latest[key]
		current := parent.items[key]
		if current != nil {
			if current.params.Item.Revision > restored.item.Revision {
				continue
			}
			previous[key] = current.params.Item
		} else {
			current = &itemState{deltas: make(map[string]string), cursors: make(map[string]bool), order: parent.nextItemOrder}
			parent.nextItemOrder++
			parent.items[key] = current
		}
		prior := current.params
		if restored.item.Revision > current.params.Item.Revision {
			current.params = cloneItemParams(itemParams{SessionID: id, Item: restored.item})
			current.nativeItemRaw = slices.Clone(restored.raw)
			current.raw = nil
			publication := current.registryState()
			publication.mu.Lock()
			publication.registryHeadRevision = 0
			publication.mu.Unlock()
			parent.ensureNativeItemIndex().replace(prior, current)
		}
		publication := current.registryState()
		publication.mu.Lock()
		needsHead := publication.registryReconciledRevision < restored.item.Revision && publication.queuedHeadRevision < restored.item.Revision
		if needsHead {
			publication.registryHeadRevision = restored.item.Revision
		}
		publication.mu.Unlock()
		if needsHead {
			publish = append(publish, key)
		}
	}
	a.stateMu.Unlock()
	var restoreErr error
	for _, key := range publish {
		a.stateMu.Lock()
		params := cloneItemParams(parent.items[key].params)
		a.stateMu.Unlock()
		restoreErr = errors.Join(restoreErr, a.observeChildOrTask(params, parent, previous[key]))
	}
	return restoreErr
}

// sessionRegistryRowKeys derives all rows from the current native items.
// The caller holds stateMu.
func sessionRegistryRowKeys(sessionID string, state *sessionState) []string {
	keys := make(map[string]bool)
	add := func(key string) {
		if key != "" {
			keys[key] = true
		}
	}
	add(state.childKey)
	for _, stored := range state.items {
		item := stored.params.Item
		switch item.Kind {
		case contracts.MuseItemKindWorkflow:
			if item.WorkflowRunID == "" {
				continue
			}
			add(museWorkflowRowKey(sessionID, item.WorkflowRunID))
			var children []struct {
				ChildID string `json:"childId"`
			}
			if json.Unmarshal(item.Children, &children) == nil {
				for _, child := range children {
					if child.ChildID != "" {
						add(museChildRowKey(sessionID, child.ChildID))
					}
				}
			}
		case museItemSubagent:
			if item.SubagentID != "" {
				add(museChildRowKey(sessionID, item.SubagentID))
			}
		case contracts.MuseItemKindToolCall:
			if item.Background {
				add(museBackgroundTaskRowKey(sessionID, item.ID))
			}
		}
	}
	result := make([]string, 0, len(keys))
	for key := range keys {
		result = append(result, key)
	}
	slices.Sort(result)
	return result
}
