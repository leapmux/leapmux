package qwen

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/quartz"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// Workflow agent rows.
//
// A Qwen workflow run is one tool call, and Qwen reports its agents to a
// client only through the task list: `qwen/status/session/tasks` with
// `includeWorkflows: true` states each run's dispatches (0.26.0 probes in
// .tmp/qwen-wf-probe/). LeapMux therefore polls that list while a workflow
// row is open, and draws one row per dispatch under the run row's group, so
// the Background tasks sidebar shows the run's work under one heading.
//
// The dispatches stay in the list after the run ends, so one final poll reads
// each agent's settled state. A run that the tool call's own frames end first
// (a stop, a CLI that reports no history) takes its agents' rows down with
// the run's own final status.

// workflowPollInterval paces one run's task-list polls.
const workflowPollInterval = 700 * time.Millisecond

// qwenWorkflowEntry is the workflow part of one task-list answer.
type qwenWorkflowEntry struct {
	Kind         string                 `json:"kind"`
	ID           string                 `json:"id"`
	ToolUseID    string                 `json:"toolUseId"`
	Label        string                 `json:"label"`
	Status       string                 `json:"status"`
	CurrentPhase string                 `json:"currentPhase"`
	Dispatches   []qwenWorkflowDispatch `json:"dispatches"`
}

// qwenWorkflowDispatch is one agent of a run.
type qwenWorkflowDispatch struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Prompt string `json:"prompt"`
	Status string `json:"status"`
}

// qwenWorkflowStatusFinal reports whether a status settles a run or dispatch.
func qwenWorkflowStatusFinal(status string) bool {
	switch status {
	case "completed", "failed", "cancelled":
		return true
	default:
		return false
	}
}

// qwenWorkflowRowStatus maps a run's or a dispatch's status word onto a row
// status. An unfamiliar word stays a working row: the next poll states a word
// this mapping knows.
func qwenWorkflowRowStatus(status string) bgtask.Status {
	switch status {
	case "completed":
		return bgtask.StatusSucceeded
	case "failed":
		return bgtask.StatusFailed
	case "cancelled":
		return bgtask.StatusStopped
	case "queued", "pending":
		return bgtask.StatusPending
	default:
		return bgtask.StatusRunning
	}
}

// workflowPoller polls the task list of one workflow run and applies its rows.
type workflowPoller struct {
	agent      *Agent
	sessionID  string
	toolCallID string
	title      string

	// mu protects snapshots and row application, including the final read.
	mu   sync.Mutex
	rows map[string]bgtask.Status
	// settled records that a poll saw the run's entry settle or vanish, so a
	// closing stop needs no further read.
	settled     bool
	started     atomic.Bool
	stopOnce    sync.Once
	finishOnce  sync.Once
	finalStatus chan bgtask.Status
	done        chan struct{}
	stopped     chan struct{}
}

// newWorkflowPoller tracks the run of one workflow tool call. title is the run
// row's own group label.
func newWorkflowPoller(a *Agent, toolCallID, title string) *workflowPoller {
	return &workflowPoller{
		agent:       a,
		sessionID:   a.CurrentSessionID(),
		toolCallID:  toolCallID,
		title:       title,
		rows:        make(map[string]bgtask.Status),
		done:        make(chan struct{}),
		stopped:     make(chan struct{}),
		finalStatus: make(chan bgtask.Status, 1),
	}
}

// start runs the poll loop. It runs once: a second call does nothing.
func (p *workflowPoller) start(clock quartz.Clock) {
	if !p.started.CompareAndSwap(false, true) {
		return
	}
	if p.stopRequested() {
		p.agent.retireWorkflowPoller(p)
		close(p.stopped)
		return
	}
	ticker := clock.NewTicker(workflowPollInterval, "qwen", "workflow")
	go func() {
		defer close(p.stopped)
		defer ticker.Stop()
		defer p.agent.retireWorkflowPoller(p)
		ticks := ticker.C
		for {
			select {
			case <-p.done:
				return
			case final := <-p.finalStatus:
				p.finishRun(final)
				return
			case <-ticks:
				if p.poll() {
					return
				}
				p.mu.Lock()
				settled := p.settled
				p.mu.Unlock()
				if settled {
					ticker.Stop()
					ticks = nil
				}
			}
		}
	}()
}

// stop ends the poll loop. It returns after the loop ended, so no poll runs
// after it.
func (p *workflowPoller) stop() {
	p.requestStop()
	if p.started.Load() {
		<-p.stopped
	}
}

func (p *workflowPoller) requestStop() {
	p.stopOnce.Do(func() { close(p.done) })
}

func (p *workflowPoller) stopRequested() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// finish ends the poller and settles its rows with the run's own final
// status. A run that no poll caught up with — its work can finish inside one
// tick — gets one last read: the task list keeps each settled run's
// dispatches, so the final snapshot still states what its agents did. A read
// that fails (a stopped process, a vanished session) leaves the rows as they
// held.
//
// The poll loop performs the last read. The notification reader must remain
// free to deliver its reply, and teardown must still own and join that read.
func (p *workflowPoller) finish(final bgtask.Status) {
	p.finishOnce.Do(func() { p.finalStatus <- final })
}

func (p *workflowPoller) finishRun(final bgtask.Status) {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.withOwningSession(func(sessionID string) error {
		var readErr error
		if !p.settled {
			var entry *qwenWorkflowEntry
			entry, readErr = p.listRun(sessionID)
			if p.stopRequested() {
				return nil
			}
			if readErr == nil && entry != nil {
				p.apply(entry)
				if qwenWorkflowStatusFinal(entry.Status) {
					final = qwenWorkflowRowStatus(entry.Status)
				}
			}
		}
		if final != bgtask.StatusUnspecified {
			p.closeUnsettled(final)
		}
		return readErr
	})
	if err != nil {
		slog.Debug("qwen final workflow task list failed", "agent_id", p.agent.AgentID(), "tool_call_id", p.toolCallID, "error", err)
	}
}

// poll asks Qwen for the session's task list with its workflow entries, and
// applies the rows of this poller's run. It reports whether the reader can
// retire. A vanished run with unfinished rows still needs its final frame.
func (p *workflowPoller) poll() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	retired, err := p.withOwningSession(func(sessionID string) error {
		entry, err := p.listRun(sessionID)
		if err != nil {
			return err
		}
		if p.stopRequested() {
			return nil
		}
		if entry == nil {
			p.settled = true
			return nil
		}
		p.apply(entry)
		if qwenWorkflowStatusFinal(entry.Status) {
			p.settled = true
			p.closeUnsettled(qwenWorkflowRowStatus(entry.Status))
		}
		return nil
	})
	if err != nil {
		// A stop closes the process context and fails the request; a transient
		// failure answers itself on the next tick.
		slog.Debug("qwen workflow task list failed", "agent_id", p.agent.AgentID(), "tool_call_id", p.toolCallID, "error", err)
	}
	return retired || p.stopRequested() || p.settled && !p.hasUnsettledRows()
}

// The caller holds mu while reading the dispatch states.
func (p *workflowPoller) hasUnsettledRows() bool {
	for _, status := range p.rows {
		if !status.IsFinished() {
			return true
		}
	}
	return false
}

// Keep identity validation, the native read and row application in one session.
func (p *workflowPoller) withOwningSession(operation func(string) error) (bool, error) {
	retired := false
	err := p.agent.WithSessionID(func(sessionID string) error {
		if sessionID == "" || sessionID != p.sessionID || p.stopRequested() {
			retired = true
			return nil
		}
		return operation(sessionID)
	})
	return retired, err
}

// listRun reads the task list and returns this run's entry.
func (p *workflowPoller) listRun(sessionID string) (*qwenWorkflowEntry, error) {
	params, err := json.Marshal(map[string]any{"sessionId": sessionID, "includeWorkflows": true})
	if err != nil {
		return nil, err
	}
	response, err := p.agent.SendRequest(qwenTaskListMethod, params, p.agent.APITimeout())
	if err != nil {
		return nil, fmt.Errorf("list the Qwen tasks: %w", err)
	}
	var list struct {
		Tasks []qwenWorkflowEntry `json:"tasks"`
	}
	if err := json.Unmarshal(response, &list); err != nil {
		return nil, fmt.Errorf("read the Qwen task list: %w", err)
	}
	for index := range list.Tasks {
		if list.Tasks[index].Kind == qwenBackgroundWorkflow && list.Tasks[index].ToolUseID == p.toolCallID {
			return &list.Tasks[index], nil
		}
	}
	return nil, nil
}

// apply upserts the run row's activity and one row per dispatch. A dispatch
// that settled closes its row with its own outcome.
func (p *workflowPoller) apply(entry *qwenWorkflowEntry) {
	// The entry's label identifies the workflow and the group shared by the
	// run's row and its dispatch rows. It corrects the generic title of a run whose
	// opening frame stated no arguments.
	label := entry.Label
	if label == "" {
		label = p.title
	}
	// The shared applier defaults an unspecified kind to a subagent row, so the
	// run's own upsert restates the workflow kind its row opened with.
	p.agent.ApplySubagentObservation(&acp.SubagentObservation{
		RowKey:     p.toolCallID,
		Kind:       bgtask.KindWorkflow,
		GroupKey:   p.toolCallID,
		GroupLabel: label,
		Title:      entry.Label,
		Activity:   entry.CurrentPhase,
		Status:     qwenWorkflowRowStatus(entry.Status),
	})
	for _, dispatch := range entry.Dispatches {
		if dispatch.ID == "" {
			continue
		}
		status := qwenWorkflowRowStatus(dispatch.Status)
		observation := &acp.SubagentObservation{
			RowKey:     p.toolCallID + "/" + dispatch.ID,
			Kind:       bgtask.KindSubagent,
			GroupKey:   p.toolCallID,
			GroupLabel: label,
			Title:      dispatch.Label,
			Activity:   bgtask.FirstLine(dispatch.Prompt),
			Status:     status,
		}
		if status.IsFinished() {
			observation.CloseRow = true
		}
		p.rows[dispatch.ID] = status
		p.agent.ApplySubagentObservation(observation)
	}
}

// closeUnsettled closes every dispatch row that no snapshot settled. The
// caller holds p.mu.
func (p *workflowPoller) closeUnsettled(final bgtask.Status) {
	for dispatchID, status := range p.rows {
		if status.IsFinished() {
			continue
		}
		p.agent.ApplySubagentObservation(&acp.SubagentObservation{
			RowKey:   p.toolCallID + "/" + dispatchID,
			Kind:     bgtask.KindSubagent,
			Status:   final,
			CloseRow: true,
		})
		p.rows[dispatchID] = final
	}
}

// startWorkflowPoller tracks the workflow run that the tool call toolCallID
// started. The run row's title identifies the group that its dispatch rows join.
func (a *Agent) startWorkflowPoller(toolCallID string, rawInput json.RawMessage) {
	poller := newWorkflowPoller(a, toolCallID, workflowObservation(toolCallID, rawInput).Title)
	a.stateMu.Lock()
	if a.workflowStopping {
		a.stateMu.Unlock()
		return
	}
	if a.children.workflows == nil {
		a.children.workflows = make(map[string]*workflowPoller)
	}
	if _, running := a.children.workflows[toolCallID]; running {
		a.stateMu.Unlock()
		return
	}
	a.children.workflows[toolCallID] = poller
	clock := a.clock
	a.stateMu.Unlock()
	poller.start(clock)
}

// stopWorkflowPoller ends the poller of one workflow run and closes its
// unsettled dispatch rows with the run's final status. A run that no poller
// tracks changes nothing.
func (a *Agent) stopWorkflowPoller(toolCallID string, final bgtask.Status) {
	a.stateMu.Lock()
	poller := a.children.workflows[toolCallID]
	a.stateMu.Unlock()
	if poller != nil {
		poller.finish(final)
	}
}

// Signal before stopping the process; its exit releases an outstanding RPC.
func (a *Agent) signalWorkflowStop() {
	a.stateMu.Lock()
	a.workflowStopping = true
	pollers := make([]*workflowPoller, 0, len(a.children.workflows))
	for _, poller := range a.children.workflows {
		pollers = append(pollers, poller)
	}
	a.stateMu.Unlock()
	for _, poller := range pollers {
		poller.requestStop()
	}
}

// retireWorkflowPoller drops a reader after its last read and row application.
func (a *Agent) retireWorkflowPoller(poller *workflowPoller) {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	if a.children.workflows[poller.toolCallID] == poller {
		delete(a.children.workflows, poller.toolCallID)
	}
}

// stopAllWorkflowPollers ends every poller, when the session or the process
// goes. Each unsettled dispatch row keeps what it held: a teardown states no
// outcome of its own.
func (a *Agent) stopAllWorkflowPollers() {
	a.stateMu.Lock()
	pollers := a.children.workflows
	a.children.workflows = nil
	a.stateMu.Unlock()
	for _, poller := range pollers {
		poller.stop()
	}
}
