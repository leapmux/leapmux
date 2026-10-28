package qwen

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/quartz"

	"github.com/leapmux/leapmux/generated/contracts"
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

// workflowPollInterval paces the session's shared task-list reads.
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
	rowKey     string
	title      string
	// reader owns this run's asynchronous reads. Agent.stateMu protects it.
	reader *workflowSessionReader

	// mu protects snapshots and row application, including the final read.
	mu   sync.Mutex
	rows map[string]bgtask.Status
	// settled records that a poll saw the run's entry settle, so a
	// closing stop needs no further read.
	settled      bool
	pendingFinal bgtask.Status
	started      atomic.Bool
	stopOnce     sync.Once
	finishOnce   sync.Once
	endOnce      sync.Once
	finalStatus  chan bgtask.Status
	done         chan struct{}
	stopped      chan struct{}
}

// newWorkflowPoller tracks the run of one workflow tool call. title is the run
// row's own group label.
func newWorkflowPoller(a *Agent, toolCallID, title string) *workflowPoller {
	sessionID := a.CurrentSessionID()
	return &workflowPoller{
		agent:       a,
		sessionID:   sessionID,
		toolCallID:  toolCallID,
		rowKey:      qwenWorkflowRowKey(sessionID, toolCallID),
		title:       title,
		rows:        make(map[string]bgtask.Status),
		done:        make(chan struct{}),
		stopped:     make(chan struct{}),
		finalStatus: make(chan bgtask.Status, 1),
	}
}

// start joins the session-owned reader. Every run receives the same snapshot.
func (p *workflowPoller) start(clock quartz.Clock) {
	if !p.started.CompareAndSwap(false, true) {
		return
	}
	if p.stopRequested() {
		p.agent.retireWorkflowPoller(p)
		return
	}
	a := p.agent
	a.stateMu.Lock()
	reader := a.workflowReader
	created := reader == nil || reader.sessionID != p.sessionID
	if created {
		reader = &workflowSessionReader{agent: a, sessionID: p.sessionID, wake: make(chan struct{}, 1), pollers: make(map[*workflowPoller]struct{})}
		a.workflowReader = reader
	}
	reader.pollers[p] = struct{}{}
	p.reader = reader
	a.stateMu.Unlock()
	if created {
		ticker := clock.NewTicker(workflowPollInterval, "qwen", "workflow")
		go func() {
			defer ticker.Stop()
			for {
				tick := false
				select {
				case <-ticker.C:
					tick = true
				case <-reader.wake:
				}
				if !reader.refresh(tick) {
					return
				}
			}
		}()
	}
	p.wakeReader()
}

type workflowSessionReader struct {
	agent     *Agent
	sessionID string
	wake      chan struct{}
	// pollers retains every admitted read until it finishes, including runs
	// removed from the public lookup by teardown. Agent.stateMu protects it.
	pollers map[*workflowPoller]struct{}
}

func (p *workflowPoller) wakeReader() {
	p.agent.stateMu.Lock()
	reader := p.reader
	p.agent.stateMu.Unlock()
	if reader != nil && reader.sessionID == p.sessionID {
		select {
		case reader.wake <- struct{}{}:
		default:
		}
	}
}

// refresh owns at most one native request. A final frame captured before the
// request shares its snapshot with every active run. A later final frame wakes
// another read, so an earlier snapshot cannot consume its final-read obligation.
func (reader *workflowSessionReader) refresh(tick bool) bool {
	a := reader.agent
	a.stateMu.Lock()
	pollers := make([]*workflowPoller, 0, len(reader.pollers))
	for poller := range reader.pollers {
		pollers = append(pollers, poller)
	}
	a.stateMu.Unlock()
	finals := make(map[*workflowPoller]bgtask.Status)
	active := pollers[:0]
	for _, poller := range pollers {
		if poller.stopRequested() {
			a.retireWorkflowPoller(poller)
			continue
		}
		active = append(active, poller)
		poller.mu.Lock()
		select {
		case final := <-poller.finalStatus:
			poller.pendingFinal = final
		default:
		}
		if poller.pendingFinal != bgtask.StatusUnspecified {
			finals[poller] = poller.pendingFinal
		}
		poller.mu.Unlock()
	}
	if len(active) > 0 && (tick || len(finals) > 0) {
		err := a.WithSessionID(func(sessionID string) error {
			if sessionID != reader.sessionID || sessionID == "" {
				for _, poller := range active {
					a.retireWorkflowPoller(poller)
				}
				return nil
			}
			entries, readErr := a.listWorkflowRuns(sessionID)
			for _, poller := range active {
				poller.mu.Lock()
				final, finishing := finals[poller]
				stopped := poller.stopRequested()
				var admissionErr error
				if !stopped {
					if entry := entries[poller.toolCallID]; readErr == nil && entry != nil {
						admissionErr = poller.apply(entry)
						if admissionErr == nil && qwenWorkflowStatusFinal(entry.Status) {
							poller.settled = true
							final = qwenWorkflowRowStatus(entry.Status)
						}
					}
					if admissionErr == nil && (finishing || poller.settled) {
						admissionErr = poller.closeUnsettled(final)
					}
					if admissionErr == nil && finishing {
						poller.pendingFinal = bgtask.StatusUnspecified
					}
				}
				retire := stopped || admissionErr == nil && (finishing || poller.settled && !poller.hasUnsettledRows())
				poller.mu.Unlock()
				if admissionErr != nil {
					slog.Warn("admit Qwen workflow registry output", "tool_call_id", poller.toolCallID, "error", admissionErr)
				}
				if retire {
					a.retireWorkflowPoller(poller)
				}
			}
			return readErr
		})
		if err != nil {
			slog.Debug("qwen workflow task list failed", "agent_id", a.AgentID(), "session_id", reader.sessionID, "error", err)
		}
	}
	a.FlushRegistryObservations()
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	if len(reader.pollers) > 0 {
		return true
	}
	if a.workflowReader == reader {
		a.workflowReader = nil
	}
	return false
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
	p.wakeReader()
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
	p.wakeReader()
}

func (p *workflowPoller) finishRun(final bgtask.Status) {
	p.mu.Lock()
	defer func() {
		p.mu.Unlock()
		p.agent.FlushRegistryObservations()
	}()
	_, err := p.withOwningSession(func(sessionID string) error {
		var readErr error
		if !p.settled {
			var entry *qwenWorkflowEntry
			entry, readErr = p.listRun(sessionID)
			if p.stopRequested() {
				return nil
			}
			if readErr == nil && entry != nil {
				if err := p.apply(entry); err != nil {
					p.pendingFinal = final
					return err
				}
				if qwenWorkflowStatusFinal(entry.Status) {
					final = qwenWorkflowRowStatus(entry.Status)
				}
			}
		}
		if final != bgtask.StatusUnspecified {
			if err := p.closeUnsettled(final); err != nil {
				p.pendingFinal = final
				return err
			}
		}
		return readErr
	})
	if err != nil {
		slog.Debug("qwen final workflow task list failed", "agent_id", p.agent.AgentID(), "tool_call_id", p.toolCallID, "error", err)
	}
}

// poll asks Qwen for the session's task list with its workflow entries, and
// applies the rows of this poller's run. It reports whether the reader can
// retire. A run absent from the list still needs registration or its final frame.
func (p *workflowPoller) poll() bool {
	p.mu.Lock()
	defer func() {
		p.mu.Unlock()
		p.agent.FlushRegistryObservations()
	}()
	retired, err := p.withOwningSession(func(sessionID string) error {
		entry, err := p.listRun(sessionID)
		if err != nil {
			return err
		}
		if p.stopRequested() {
			return nil
		}
		if entry == nil {
			return nil
		}
		if err := p.apply(entry); err != nil {
			return err
		}
		if qwenWorkflowStatusFinal(entry.Status) {
			p.settled = true
			return p.closeUnsettled(qwenWorkflowRowStatus(entry.Status))
		}
		return nil
	})
	if err != nil {
		// A stop closes the process context and fails the request; a transient
		// failure answers itself on the next tick.
		slog.Debug("qwen workflow task list failed", "agent_id", p.agent.AgentID(), "tool_call_id", p.toolCallID, "error", err)
	}
	return retired || p.stopRequested() || err == nil && p.settled && !p.hasUnsettledRows()
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
	entries, err := p.agent.listWorkflowRuns(sessionID)
	return entries[p.toolCallID], err
}

func (a *Agent) listWorkflowRuns(sessionID string) (map[string]*qwenWorkflowEntry, error) {
	params, err := json.Marshal(map[string]any{"sessionId": sessionID, "includeWorkflows": true})
	if err != nil {
		return nil, err
	}
	response, err := a.SendRequest(qwenTaskListMethod, params, a.APITimeout())
	if err != nil {
		return nil, fmt.Errorf("list the Qwen tasks: %w", err)
	}
	var list struct {
		Tasks []qwenWorkflowEntry `json:"tasks"`
	}
	if err := json.Unmarshal(response, &list); err != nil {
		return nil, fmt.Errorf("read the Qwen task list: %w", err)
	}
	entries := make(map[string]*qwenWorkflowEntry)
	for index := range list.Tasks {
		entry := &list.Tasks[index]
		if entry.Kind == qwenBackgroundWorkflow {
			entries[entry.ToolUseID] = entry
		}
	}
	return entries, nil
}

// apply upserts the run row's activity and one row per dispatch. A dispatch
// that settled closes its row with its own outcome.
func (p *workflowPoller) apply(entry *qwenWorkflowEntry) error {
	// The caller keeps the native session lease. Record this source before
	// ACP admission, which can wait behind a progress hook that needs stateMu.
	p.agent.stateMu.Lock()
	if input, exists := p.agent.tools.byCall[p.toolCallID]; exists && input.name == contracts.QwenToolWorkflow {
		input.workflowMetadataObserved = true
		p.agent.tools.byCall[p.toolCallID] = input
	}
	p.agent.stateMu.Unlock()
	// The entry's label identifies the workflow and the group shared by the
	// run's row and its dispatch rows. It corrects the generic title of a run whose
	// opening frame stated no arguments.
	label := entry.Label
	if label == "" {
		label = p.title
	}
	// The shared applier defaults an unspecified kind to a subagent row, so the
	// run's own upsert restates the workflow kind its row opened with.
	if err := p.agent.ApplyRegistryObservation(&acp.SubagentObservation{
		RegistryOnly: true,
		RowKey:       p.rowKey,
		Kind:         bgtask.KindWorkflow,
		GroupKey:     p.rowKey,
		GroupLabel:   label,
		Title:        entry.Label,
		Activity:     entry.CurrentPhase,
		Status:       qwenWorkflowRowStatus(entry.Status),
	}); err != nil {
		return err
	}
	for _, dispatch := range entry.Dispatches {
		if dispatch.ID == "" {
			continue
		}
		status := qwenWorkflowRowStatus(dispatch.Status)
		observation := &acp.SubagentObservation{
			RegistryOnly: true,
			RowKey:       qwenWorkflowDispatchRowKey(p.sessionID, p.toolCallID, dispatch.ID),
			Kind:         bgtask.KindSubagent,
			GroupKey:     p.rowKey,
			GroupLabel:   label,
			Title:        dispatch.Label,
			Activity:     bgtask.FirstLine(dispatch.Prompt),
			Status:       status,
		}
		if status.IsFinished() {
			observation.CloseRow = true
		}
		if err := p.agent.ApplyRegistryObservation(observation); err != nil {
			return err
		}
		p.rows[dispatch.ID] = status
	}
	return nil
}

// closeUnsettled closes every dispatch row that no snapshot settled. The
// caller holds p.mu.
func (p *workflowPoller) closeUnsettled(final bgtask.Status) error {
	for dispatchID, status := range p.rows {
		if status.IsFinished() {
			continue
		}
		if err := p.agent.ApplyRegistryObservation(&acp.SubagentObservation{
			RegistryOnly: true,
			RowKey:       qwenWorkflowDispatchRowKey(p.sessionID, p.toolCallID, dispatchID),
			Kind:         bgtask.KindSubagent,
			Status:       final,
			CloseRow:     true,
		}); err != nil {
			return err
		}
		p.rows[dispatchID] = final
	}
	return nil
}

// startWorkflowPoller tracks the workflow run that the tool call toolCallID
// started. The run row's title identifies the group that its dispatch rows join.
func (a *Agent) startWorkflowPoller(toolCallID string, rawInput json.RawMessage) {
	poller := newWorkflowPoller(a, toolCallID, workflowObservation(a.CurrentSessionID(), toolCallID, rawInput).Title)
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
	if poller.reader != nil {
		delete(poller.reader.pollers, poller)
	}
	poller.endOnce.Do(func() { close(poller.stopped) })
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
