package muse

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// One entry of a workflow item's children fold: the durable child of the run,
// identified by its childId and attempt. A separate subagent item can supply the
// child's addressable session and controls.
type nativeWorkflowChild struct {
	ChildID     string          `json:"childId"`
	Attempt     int64           `json:"attempt"`
	Status      string          `json:"status"`
	Outcome     string          `json:"terminal"`
	Label       string          `json:"label"`
	FailureKind json.RawMessage `json:"failureKind"`
}

func (child *nativeWorkflowChild) UnmarshalJSON(raw []byte) error {
	var required struct {
		Attempt *int64 `json:"attempt"`
	}
	if err := json.Unmarshal(raw, &required); err != nil {
		return err
	}
	if required.Attempt == nil {
		return fmt.Errorf("the Muse workflow child has no attempt")
	}
	type plainWorkflowChild nativeWorkflowChild
	var decoded plainWorkflowChild
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	*child = nativeWorkflowChild(decoded)
	return nil
}

type workflowChildPublication struct {
	acceptedAttempt       int64
	hasAcceptedAttempt    bool
	pendingRestartAttempt int64
	hasPendingRestart     bool
}

func latestWorkflowChildren(raw json.RawMessage) ([]nativeWorkflowChild, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var decoded *[]nativeWorkflowChild
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("decode the Muse workflow children: %w", err)
	}
	if decoded == nil {
		return nil, fmt.Errorf("the Muse workflow children are null")
	}
	children := *decoded
	type identity struct {
		childID string
		attempt int64
	}
	seen := make(map[identity]bool, len(children))
	latest := make(map[string]nativeWorkflowChild, len(children))
	var order []string
	for _, child := range children {
		if child.ChildID == "" {
			return nil, fmt.Errorf("the Muse workflow child has no identity")
		}
		key := identity{child.ChildID, child.Attempt}
		if seen[key] {
			return nil, fmt.Errorf("the Muse workflow repeats a child attempt")
		}
		seen[key] = true
		prior, exists := latest[child.ChildID]
		if !exists {
			order = append(order, child.ChildID)
		}
		if !exists || child.Attempt > prior.Attempt {
			latest[child.ChildID] = child
		}
	}
	result := make([]nativeWorkflowChild, 0, len(order))
	for _, id := range order {
		result = append(result, latest[id])
	}
	return result, nil
}

func validateWorkflowAttemptAdvance(previous, next nativeItem) error {
	if next.Kind != contracts.MuseItemKindWorkflow {
		return nil
	}
	children, err := latestWorkflowChildren(next.Children)
	if err != nil {
		return err
	}
	if previous.Kind != next.Kind || previous.WorkflowRunID != next.WorkflowRunID {
		return nil
	}
	prior, err := latestWorkflowChildren(previous.Children)
	if err != nil {
		return nil
	}
	attempts := make(map[string]int64, len(prior))
	for _, child := range prior {
		attempts[child.ChildID] = child.Attempt
	}
	for _, child := range children {
		if attempt, exists := attempts[child.ChildID]; exists && child.Attempt < attempt {
			return fmt.Errorf("the Muse workflow child attempt moved backward")
		}
	}
	return nil
}

// The lifecycle words a workflow child's fold states, from the workflow item
// fold of the installed host. The words are the host's own vocabulary.
const (
	workflowChildScheduled = "scheduled"
	workflowChildStarted   = "started"
	workflowChildUsage     = "usage"
	workflowChildFinished  = "terminal"
)

func workflowChildStatus(child nativeWorkflowChild) bgtask.Status {
	switch child.Status {
	case workflowChildScheduled:
		return bgtask.StatusPending
	case workflowChildStarted, workflowChildUsage:
		return bgtask.StatusRunning
	case workflowChildFinished:
		if child.Outcome == contracts.MuseItemStatusCompleted {
			if len(child.FailureKind) > 0 {
				var failure *string
				if json.Unmarshal(child.FailureKind, &failure) != nil || failure == nil {
					return bgtask.StatusEndedWithUnknownOutcome
				}
				return bgtask.StatusFailed
			}
			return bgtask.StatusSucceeded
		}
		if child.Outcome == contracts.MuseItemStatusCancelled {
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
func (a *Agent) prepareWorkflowRegistryLocked(params itemParams, parent *sessionState, publication *registryPublication, previous nativeItem, head bool) (nativeRegistryPlan, error) {
	item := params.Item
	if item.WorkflowRunID == "" {
		return nativeRegistryPlan{}, nil
	}
	children, err := latestWorkflowChildren(item.Children)
	if err != nil {
		return nativeRegistryPlan{}, err
	}
	// Validate all claims before publishing any part of this fold.
	authorities, err := a.workflowChildAuthorities(params.SessionID, parent)
	if err != nil {
		return nativeRegistryPlan{}, err
	}
	for _, child := range children {
		if authority, found := authorities[child.ChildID]; !found || authority.itemID != item.ID {
			return nativeRegistryPlan{}, fmt.Errorf("the Muse workflow child has no unique current owner")
		}
	}
	var prior []nativeWorkflowChild
	if previous.Kind == contracts.MuseItemKindWorkflow && previous.WorkflowRunID == item.WorkflowRunID {
		// A rejected malformed fold supplies no attempt evidence.
		prior, _ = latestWorkflowChildren(previous.Children)
	}
	previousAttempts := make(map[string]int64, len(prior))
	for _, child := range prior {
		previousAttempts[child.ChildID] = child.Attempt
	}
	publication.mu.Lock()
	if publication.workflowPublications == nil {
		publication.workflowPublications = make(map[string]workflowChildPublication)
	}
	// Retain transition evidence even if publishing the run itself fails.
	for _, child := range children {
		receipt := publication.workflowPublications[child.ChildID]
		attempt, known := previousAttempts[child.ChildID]
		if receipt.hasAcceptedAttempt && (!known || receipt.acceptedAttempt > attempt) {
			attempt, known = receipt.acceptedAttempt, true
		}
		if known && child.Attempt > attempt {
			receipt.pendingRestartAttempt, receipt.hasPendingRestart = child.Attempt, true
			publication.workflowPublications[child.ChildID] = receipt
		}
	}
	publication.mu.Unlock()
	group := museWorkflowRowKey(params.SessionID, item.WorkflowRunID)
	name := item.EntryID
	if name == "" {
		name = item.WorkflowRunID
	}
	status := childStatus(item.Status)
	plan := nativeRegistryPlan{rows: []nativeRegistryRow{{upsert: bgtask.Upsert{RowKey: group, Kind: bgtask.KindWorkflow, ParentAgentID: parent.childID, GroupKey: group, GroupLabel: name, Title: name, Status: status}, revive: head && status.IsWorking()}}}
	for _, child := range children {
		publication.mu.Lock()
		receipt := publication.workflowPublications[child.ChildID]
		publication.mu.Unlock()
		if receipt.hasAcceptedAttempt && receipt.acceptedAttempt > child.Attempt {
			continue
		}
		status := workflowChildStatus(child)
		key := museChildRowKey(params.SessionID, child.ChildID)

		title := child.Label
		if title == "" && a.childForKey(key) == nil {
			title = "Child " + child.ChildID
		}
		plan.rows = append(plan.rows, nativeRegistryRow{
			upsert:  bgtask.Upsert{RowKey: key, Kind: bgtask.KindSubagent, ParentAgentID: parent.childID, GroupKey: group, GroupLabel: name, Title: title, Status: status},
			revive:  status.IsWorking() && (head || receipt.hasPendingRestart && receipt.pendingRestartAttempt == child.Attempt),
			childID: child.ChildID, attempt: child.Attempt, hasAttempt: true,
		})
	}
	return plan, nil
}

type workflowChildAuthority struct {
	itemID, runID, group, label string
	child                       nativeWorkflowChild
}

// workflowChildAuthorities derives unique ownership from canonical native
// items. Independent item revisions cannot resolve conflicting owners.
func (a *Agent) workflowChildAuthorities(sessionID string, parent *sessionState) (map[string]workflowChildAuthority, error) {
	a.stateMu.Lock()
	var workflows []nativeItem
	for _, state := range parent.items {
		if item := state.params.Item; item.Kind == contracts.MuseItemKindWorkflow && item.WorkflowRunID != "" {
			item.Children = slices.Clone(item.Children)
			workflows = append(workflows, item)
		}
	}
	a.stateMu.Unlock()
	slices.SortFunc(workflows, func(left, right nativeItem) int { return strings.Compare(left.ID, right.ID) })
	authorities := make(map[string]workflowChildAuthority)
	for _, item := range workflows {
		children, err := latestWorkflowChildren(item.Children)
		if err != nil {
			return nil, err
		}
		label := item.EntryID
		if label == "" {
			label = item.WorkflowRunID
		}
		for _, child := range children {
			if _, found := authorities[child.ChildID]; found {
				return nil, fmt.Errorf("the Muse child has conflicting workflow ownership")
			}
			authorities[child.ChildID] = workflowChildAuthority{itemID: item.ID, runID: item.WorkflowRunID, group: museWorkflowRowKey(sessionID, item.WorkflowRunID), label: label, child: child}
		}
	}
	return authorities, nil
}
