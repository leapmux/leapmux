package zcode

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// Workflow runs.
//
// A `CreateWorkflow` run reaches the session events as ONE background task, so
// LeapMux draws one workflow row for it. The run's subagents (`agent(...)` in the
// script) and each task they take (`ask`) reach a client only through the
// conversation subscription (conversation.go): its snapshot states each run's
// actors and nodes, and `workflowRun.updated` states what changed. LeapMux
// therefore keeps the subscription open while a run works, and draws one row for
// each ask node under the workflow row's group, so the Background tasks sidebar
// shows the run's work under one heading.
//
// The background task's final status can arrive before the frame that settles
// the run's last node, so a run keeps the subscription until the frames report
// the run's own end. A snapshot lists every run of the session, so a tracked run
// that a snapshot does not list is one the frames will never report: it ends
// there, and its single workflow row stays.

// The node kind of a subagent's task. A `world-read` node is a deterministic
// read of the world (`world.run`), which is no agent's work, so it gets no row.
const zcodeWorkflowNodeAsk = "ask"

// The phases and outcomes of a workflow node.
const (
	zcodeWorkflowPhaseQueued     = "queued"
	zcodeWorkflowPhaseDispatched = "dispatched"
	zcodeWorkflowPhaseSettled    = "settled"

	zcodeWorkflowOutcomeOK        = "ok"
	zcodeWorkflowOutcomeFailed    = "failed"
	zcodeWorkflowOutcomeCancelled = "cancelled"
)

// The statuses of a workflow run that still works. Every other status ends it.
const (
	zcodeWorkflowRunPending = "pending"
	zcodeWorkflowRunRunning = "running"
)

// zcodeWorkflowRunRemoved is LeapMux's own status for a run that a
// `workflowRun.removed` change removed, which states no status of its own.
const zcodeWorkflowRunRemoved = "removed"

// zcodeWorkflowRunsState is a full list of the session's workflow runs.
type zcodeWorkflowRunsState struct {
	Runs []zcodeWorkflowRunState `json:"runs"`
}

// zcodeWorkflowRunState is one run, or the part of a run that a change states.
type zcodeWorkflowRunState struct {
	RunID      string                    `json:"runId"`
	ToolCallID string                    `json:"toolCallId"`
	Status     string                    `json:"status"`
	Actors     []zcodeWorkflowActorState `json:"actors"`
	Nodes      []zcodeWorkflowNodeState  `json:"nodes"`
}

// zcodeWorkflowActorState is one subagent of a run.
type zcodeWorkflowActorState struct {
	SiteID  string `json:"siteId"`
	Ordinal int    `json:"ordinal"`
	Name    string `json:"name"`
}

// zcodeWorkflowNodeState is one task of a run.
type zcodeWorkflowNodeState struct {
	SiteID           string `json:"siteId"`
	Ordinal          int    `json:"ordinal"`
	Kind             string `json:"kind"`
	Phase            string `json:"phase"`
	Outcome          string `json:"outcome"`
	ActorSiteID      string `json:"actorSiteId"`
	ActorOrdinal     int    `json:"actorOrdinal"`
	InstructionsHead string `json:"instructionsHead"`
}

// zcodeWorkflowSiteRef states one actor or node that a change removed.
type zcodeWorkflowSiteRef struct {
	SiteID  string `json:"siteId"`
	Ordinal int    `json:"ordinal"`
}

// zcodeWorkflowSiteKey identifies an actor or a node within its run.
func zcodeWorkflowSiteKey(siteID string, ordinal int) string {
	return fmt.Sprintf("%s#%d", siteID, ordinal)
}

// zcodeWorkflowRuns tracks the workflow runs of the current session. Guarded by
// a.Mu.
type zcodeWorkflowRuns struct {
	runs map[string]*zcodeWorkflowRun
}

// zcodeWorkflowRun is one tracked run.
type zcodeWorkflowRun struct {
	// row is the newest row that the background task stated. Its key is also the
	// group key of the rows of its nodes.
	row bgtask.Upsert
	// ended is true once the frames reported the run's end.
	ended bool
	// incompleteStatus is the outcome a session or process teardown gives a node
	// that did not settle. A frame that ends a run states no such outcome.
	incompleteStatus bgtask.Status
	actors           map[string]string
	nodes            map[string]zcodeWorkflowNodeState
	// order lists the node keys in the order that ZCode first stated them, so
	// the rows reach the sidebar in that order.
	order []string
	// rows holds the newest row of each node, so an unchanged node writes
	// nothing.
	rows map[string]bgtask.Upsert
}

// active reports whether a run works that the frames have not reported ended.
func (w *zcodeWorkflowRuns) active() bool {
	for _, run := range w.runs {
		if !run.ended {
			return true
		}
	}
	return false
}

// clear ends every active run with the teardown's outcome and forgets every
// run. A row that already reached a final result keeps it.
func (w *zcodeWorkflowRuns) clear(status bgtask.Status) (upserts []bgtask.Upsert) {
	for _, runID := range slices.Sorted(maps.Keys(w.runs)) {
		run := w.runs[runID]
		if !run.ended {
			upserts = append(upserts, run.end(status, status)...)
		}
	}
	clear(w.runs)
	return upserts
}

// track records the run of a workflow background task. It reports whether the
// run is new, so the caller opens the subscription for it.
func (w *zcodeWorkflowRuns) track(runID string, row bgtask.Upsert) bool {
	if runID == "" || row.RowKey == "" {
		return false
	}
	label := row.GroupLabel
	if label == "" {
		label = row.Title
	}
	if row.GroupKey == "" {
		row.GroupKey = row.RowKey
	}
	row.GroupLabel = label
	if run := w.runs[runID]; run != nil {
		if label != "" {
			run.row = row
		} else {
			run.row.Status = row.Status
		}
		return false
	}
	if w.runs == nil {
		w.runs = map[string]*zcodeWorkflowRun{}
	}
	w.runs[runID] = &zcodeWorkflowRun{
		row:    row,
		actors: map[string]string{},
		nodes:  map[string]zcodeWorkflowNodeState{},
		rows:   map[string]bgtask.Upsert{},
	}
	return true
}

// apply reads the workflow runs of one frame. It returns the rows that changed,
// and whether a run ended, so the caller may close the subscription.
func (w *zcodeWorkflowRuns) apply(frame zcodeConversationFrame) (upserts []bgtask.Upsert, ended bool) {
	states := []zcodeWorkflowRunState{}
	if frame.Payload.Kind == zcodeConversationSnapshot {
		listed := map[string]bool{}
		if snapshot := frame.Payload.Snapshot.WorkflowRuns; snapshot != nil {
			states = append(states, snapshot.Runs...)
			for _, run := range snapshot.Runs {
				listed[run.RunID] = true
			}
		}
		for _, runID := range slices.Sorted(maps.Keys(w.runs)) {
			run := w.runs[runID]
			if !listed[runID] && !run.ended {
				upserts = append(upserts, run.end(bgtask.StatusEndedWithUnknownOutcome, bgtask.StatusEndedWithUnknownOutcome)...)
				ended = true
			}
		}
	}
	for _, delta := range frame.Payload.Deltas {
		if patch := delta.Patch.WorkflowRuns; patch != nil {
			states = append(states, patch.Runs...)
		}
		switch delta.Op {
		case "workflowRun.updated":
			state := zcodeWorkflowRunState{RunID: delta.RunID, Actors: delta.Actors, Nodes: delta.Nodes}
			if delta.Run != nil {
				state.Status = delta.Run.Status
			}
			states = append(states, state)
			if run := w.runs[delta.RunID]; run != nil {
				for _, removed := range delta.RemovedNodes {
					run.forgetNode(zcodeWorkflowSiteKey(removed.SiteID, removed.Ordinal))
				}
			}
		case "workflowRun.removed":
			states = append(states, zcodeWorkflowRunState{RunID: delta.RunID, Status: zcodeWorkflowRunRemoved})
		}
	}
	for _, state := range states {
		run := w.runs[state.RunID]
		if run == nil {
			// A run that no background task stated has no row to group under.
			continue
		}
		for _, actor := range state.Actors {
			if actor.Name != "" {
				run.actors[zcodeWorkflowSiteKey(actor.SiteID, actor.Ordinal)] = actor.Name
			}
		}
		for _, node := range state.Nodes {
			key := zcodeWorkflowSiteKey(node.SiteID, node.Ordinal)
			previous, known := run.nodes[key]
			if !known {
				run.order = append(run.order, key)
			}
			run.nodes[key] = mergeZCodeWorkflowNode(previous, node)
		}
		if state.Status != "" && state.Status != zcodeWorkflowRunPending && state.Status != zcodeWorkflowRunRunning && !run.ended {
			upserts = append(upserts, run.end(zcodeWorkflowRunFinalStatus(state.Status), bgtask.StatusEndedWithUnknownOutcome)...)
			ended = true
			continue
		}
		upserts = append(upserts, run.changedRows()...)
	}
	return upserts, ended
}

// zcodeWorkflowRunFinalStatus maps a frame's terminal run status. Removal and
// an unfamiliar terminal word state no outcome of their own.
func zcodeWorkflowRunFinalStatus(status string) bgtask.Status {
	switch status {
	case "completed":
		return bgtask.StatusSucceeded
	case "failed":
		return bgtask.StatusFailed
	case "cancelled":
		return bgtask.StatusStopped
	default:
		return bgtask.StatusEndedWithUnknownOutcome
	}
}

// end closes the run's own row unless the background task already closed it,
// and closes every ask row whose status changed. The first end writes once.
func (r *zcodeWorkflowRun) end(status, incompleteStatus bgtask.Status) (upserts []bgtask.Upsert) {
	if r.ended {
		return nil
	}
	r.ended = true
	r.incompleteStatus = incompleteStatus
	if !r.row.Status.IsFinished() {
		r.row.Status = status
		upserts = append(upserts, r.row)
	}
	return append(upserts, r.changedRows()...)
}

// mergeZCodeWorkflowNode applies a change to a node: a change states the fields
// that changed, so a blank field keeps the node's value.
func mergeZCodeWorkflowNode(node, change zcodeWorkflowNodeState) zcodeWorkflowNodeState {
	node.SiteID, node.Ordinal = change.SiteID, change.Ordinal
	if change.Kind != "" {
		node.Kind = change.Kind
	}
	if change.Phase != "" {
		node.Phase = change.Phase
	}
	if change.Outcome != "" {
		node.Outcome = change.Outcome
	}
	if change.ActorSiteID != "" {
		node.ActorSiteID, node.ActorOrdinal = change.ActorSiteID, change.ActorOrdinal
	}
	if change.InstructionsHead != "" {
		node.InstructionsHead = change.InstructionsHead
	}
	return node
}

// forgetNode drops a node that a change removed. Its row stays, as the record
// of work that ran.
func (r *zcodeWorkflowRun) forgetNode(key string) {
	delete(r.nodes, key)
	r.order = slices.DeleteFunc(r.order, func(candidate string) bool { return candidate == key })
}

// changedRows returns the row of each ask node whose row changed.
func (r *zcodeWorkflowRun) changedRows() []bgtask.Upsert {
	var upserts []bgtask.Upsert
	for _, key := range r.order {
		node := r.nodes[key]
		if node.Kind != zcodeWorkflowNodeAsk {
			continue
		}
		row := r.nodeRow(key, node)
		if previous, written := r.rows[key]; written && previous == row {
			continue
		}
		r.rows[key] = row
		upserts = append(upserts, row)
	}
	return upserts
}

// nodeRow is the row of one ask node: the subagent's name over the head of its
// instructions, in the group of the run's row.
func (r *zcodeWorkflowRun) nodeRow(key string, node zcodeWorkflowNodeState) bgtask.Upsert {
	instructions := strings.TrimSpace(node.InstructionsHead)
	title := r.actors[zcodeWorkflowSiteKey(node.ActorSiteID, node.ActorOrdinal)]
	if title == "" {
		// An unnamed subagent: its instructions state the work.
		title = bgtask.CleanTitleRunes(bgtask.FirstLine(instructions), 120)
	}
	status := zcodeWorkflowNodeStatus(node, r.ended)
	if r.ended && node.Phase != zcodeWorkflowPhaseSettled && r.incompleteStatus != bgtask.StatusUnspecified {
		status = r.incompleteStatus
	}
	return bgtask.Upsert{
		RowKey:      r.row.RowKey + "/" + key,
		Kind:        bgtask.KindSubagent,
		GroupKey:    r.row.RowKey,
		GroupLabel:  r.row.GroupLabel,
		Title:       title,
		Description: instructions,
		Status:      status,
	}
}

// zcodeWorkflowNodeStatus maps a node's phase and outcome onto a row status. A
// node that did not settle before its run ended has no outcome to state.
func zcodeWorkflowNodeStatus(node zcodeWorkflowNodeState, runEnded bool) bgtask.Status {
	switch {
	case node.Phase == zcodeWorkflowPhaseSettled:
		switch node.Outcome {
		case zcodeWorkflowOutcomeOK:
			return bgtask.StatusSucceeded
		case zcodeWorkflowOutcomeFailed:
			return bgtask.StatusFailed
		case zcodeWorkflowOutcomeCancelled:
			return bgtask.StatusStopped
		}
		return bgtask.StatusEndedWithUnknownOutcome
	case runEnded:
		return bgtask.StatusEndedWithUnknownOutcome
	case node.Phase == zcodeWorkflowPhaseQueued || node.Phase == zcodeWorkflowPhaseDispatched:
		return bgtask.StatusPending
	default:
		return bgtask.StatusRunning
	}
}
