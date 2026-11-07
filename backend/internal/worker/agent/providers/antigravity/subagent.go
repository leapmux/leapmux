package antigravity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

func (a *Agent) ensureChildren(parent *cascadeState) {
	a.Mu.Lock()
	type pendingChild struct {
		id     string
		record *childKey
	}
	records := make([]pendingChild, 0, len(parent.childKeys))
	for id, record := range parent.childKeys {
		if record.state == nil && !record.refused {
			records = append(records, pendingChild{id: id, record: record})
		}
	}
	a.Mu.Unlock()
	for _, child := range records {
		a.ensureOneChild(parent, child.id, child.record)
	}
	a.linkPendingChildSpans(parent)
}

func (a *Agent) ensureOneChild(parent *cascadeState, childID string, expectedRecord *childKey) {
	a.childSetupMu.Lock()
	defer a.childSetupMu.Unlock()
	a.Mu.Lock()
	if !a.cascadeCurrentLocked(parent) || parent.closed {
		a.Mu.Unlock()
		return
	}
	record := parent.childKeys[childID]
	if record == nil || record != expectedRecord || record.state != nil || record.refused {
		a.Mu.Unlock()
		return
	}
	role, spawnSpan, stored, idle := record.role, record.spawnSpanID, record.stored, record.idle
	a.Mu.Unlock()
	childAgentID, err := parent.sink.EnsureChildAgent(agent.ChildAgentSpec{
		ProviderChildKey: childID, AgentSessionID: childID, Title: role, SpawnSpanID: spawnSpan,
	})
	if err != nil {
		if errors.Is(err, agent.ErrChildIdentityRefused) {
			a.Mu.Lock()
			if !record.refused {
				slog.Warn("antigravity child identity refused", "agent_id", a.AgentID(), "cascade_id", childID)
			}
			record.refused = true
			a.Mu.Unlock()
			return
		}
		slog.Warn("antigravity ensure child", "agent_id", a.AgentID(), "cascade_id", childID, "error", err)
		return
	}
	a.Mu.Lock()
	parentCurrent := a.cascadeCurrentLocked(parent) && !parent.closed && parent.childKeys[childID] == record
	a.Mu.Unlock()
	if !parentCurrent {
		a.queueChildRowClose(childID, bgtask.StatusStopped)
		return
	}
	state := newCascadeState(childID, parent.sink.ChildSink(childAgentID), parent.generation, stored)
	state.parent = parent
	state.childAgentID = childAgentID
	state.rowOpen = true
	a.Mu.Lock()
	if !a.cascadeCurrentLocked(parent) || parent.closed || parent.childKeys[childID] != record {
		a.Mu.Unlock()
		a.queueChildRowClose(childID, bgtask.StatusStopped)
		return
	}
	record.state = state
	a.cascades[childID] = state
	a.Mu.Unlock()
	a.Mu.Lock()
	current := a.cascadeCurrentLocked(state)
	a.Mu.Unlock()
	if !current {
		return
	}
	providerkit.LogRegistryRefusal("antigravity", "upsert", a.sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: childID, ChildAgentID: childAgentID, Kind: bgtask.KindSubagent,
		Title: role, Description: role, Status: bgtask.StatusRunning,
	}))
	a.Mu.Lock()
	current = a.cascadeCurrentLocked(state)
	a.Mu.Unlock()
	if !current {
		a.queueChildRowClose(childID, bgtask.StatusStopped)
		return
	}
	if stored && idle {
		a.closeChildRow(state, bgtask.StatusEndedWithUnknownOutcome)
	}
	if stored {
		a.startCascadeWorker(state, a.startStoredChild)
		return
	}
	a.startCascadeStream(state)
}

func (a *Agent) startStoredChild(ctx context.Context, state *cascadeState) {
	var err error
	for attempt := range 3 {
		var count int
		attemptCtx, cancel := context.WithTimeout(ctx, a.rpc.timeout)
		count, err = a.rpc.readStepCount(attemptCtx, state.id)
		cancel()
		if err == nil {
			a.Mu.Lock()
			if state.closed || !a.cascadeCurrentLocked(state) {
				a.Mu.Unlock()
				return
			}
			state.seededCount = count
			a.Mu.Unlock()
			a.runCascadeStream(ctx, state)
			return
		}
		if isStreamNotFound(err) {
			break
		}
		if attempt < 2 {
			timer := a.clock.NewTimer(time.Duration(attempt+1)*100*time.Millisecond, antigravityStreamReconnectTimerTag)
			select {
			case <-ctx.Done():
				timer.Stop(antigravityStreamReconnectTimerTag)
				return
			case <-timer.C:
			}
		}
	}
	if ctx.Err() == nil {
		a.fatalCascade(state, err)
	}
}

func (a *Agent) linkSubagentResults(parent *cascadeState, index int, step antigravityStep) {
	if step.Type != "CORTEX_STEP_TYPE_INVOKE_SUBAGENT" {
		return
	}
	for position, result := range step.InvokeSubagent.Results {
		if result.ConversationID == "" {
			continue
		}
		a.Mu.Lock()
		record := parent.childKeys[result.ConversationID]
		if record == nil {
			record = &childKey{}
			parent.childKeys[result.ConversationID] = record
		}
		if position < len(step.InvokeSubagent.Subagents) && step.InvokeSubagent.Subagents[position].Role != "" {
			record.role = step.InvokeSubagent.Subagents[position].Role
		}
		record.spawnSpanID = step.Metadata.ToolCall.ID
		a.Mu.Unlock()
	}
}

func (a *Agent) linkPendingChildSpans(parent *cascadeState) {
	a.childSetupMu.Lock()
	defer a.childSetupMu.Unlock()
	a.Mu.Lock()
	if !a.cascadeCurrentLocked(parent) || parent.closed {
		a.Mu.Unlock()
		return
	}
	type link struct {
		id, role, span string
		state          *cascadeState
	}
	var links []link
	for id, record := range parent.childKeys {
		if record.state != nil && record.spawnSpanID != "" && !record.linked {
			record.linked = true
			links = append(links, link{id: id, role: record.role, span: record.spawnSpanID, state: record.state})
		}
	}
	a.Mu.Unlock()
	for _, item := range links {
		a.Mu.Lock()
		current := a.cascadeCurrentLocked(parent) && a.cascadeCurrentLocked(item.state)
		a.Mu.Unlock()
		if !current {
			return
		}
		if _, err := parent.sink.EnsureChildAgent(agent.ChildAgentSpec{
			ProviderChildKey: item.id, AgentSessionID: item.id, Title: item.role, SpawnSpanID: item.span,
		}); err != nil {
			a.Mu.Lock()
			if record := parent.childKeys[item.id]; record != nil {
				record.linked = false
			}
			a.Mu.Unlock()
			slog.Warn("antigravity link child spawn span", "agent_id", a.AgentID(), "error", err)
		}
		a.Mu.Lock()
		current = a.cascadeCurrentLocked(parent) && a.cascadeCurrentLocked(item.state)
		a.Mu.Unlock()
		if !current {
			a.queueChildRowClose(item.id, bgtask.StatusStopped)
		}
	}
}

func (a *Agent) applyBackgroundTasks(state *cascadeState, patch *backgroundTasksPatch, firstOnAttempt bool) error {
	if patch == nil && !firstOnAttempt {
		return nil
	}
	a.Mu.Lock()
	if !a.cascadeCurrentLocked(state) || state.closed {
		a.Mu.Unlock()
		return nil
	}
	previous := state.background
	slots := map[int]backgroundTask{}
	if !firstOnAttempt {
		for _, task := range previous {
			slots[task.slot] = task
		}
	}
	if patch != nil {
		for position, snapshot := range patch.BackgroundTasks {
			if position >= len(patch.Indices) {
				break
			}
			slot := patch.Indices[position]
			if slot < 0 || slot >= patch.TotalLength {
				continue
			}
			logPath := snapshot.TaskSnapshot.LogURI
			if parsed, err := url.Parse(logPath); err == nil && parsed.Scheme == "file" {
				logPath = parsed.Path
				if parsed.Host != "" && parsed.Host != "localhost" {
					logPath = "//" + parsed.Host + logPath
				}
			}
			slots[slot] = backgroundTask{rowKey: snapshot.TaskSnapshot.TaskID, title: snapshot.TaskSnapshot.Description, description: logPath, stepIndex: snapshot.TaskSnapshot.StepIndex, slot: slot}
		}
		for slot := range slots {
			if slot >= patch.TotalLength {
				delete(slots, slot)
			}
		}
	}
	current := make(map[int]backgroundTask, len(slots))
	publish := make([]backgroundTask, 0, len(slots))
	for _, task := range slots {
		current[task.stepIndex] = task
		publish = append(publish, task)
	}
	state.background = current
	retained := make(map[string]struct{}, len(current))
	for _, task := range current {
		retained[task.rowKey] = struct{}{}
	}
	var removed []backgroundTask
	for _, task := range previous {
		if _, keeps := retained[task.rowKey]; !keeps {
			state.closingBackground[task.rowKey] = task
			removed = append(removed, task)
		}
	}
	a.Mu.Unlock()
	for _, task := range publish {
		if !a.acceptsCascadeUpdate(state) {
			return nil
		}
		providerkit.LogRegistryRefusal("antigravity", "upsert shell", state.sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: task.rowKey, Kind: bgtask.KindShell, Title: task.title, TitleIsCommand: true, Description: task.description, Status: bgtask.StatusRunning}))
		if !a.acceptsCascadeUpdate(state) {
			a.queueBackgroundTaskCloses(state, []backgroundTask{task}, bgtask.StatusStopped)
			return nil
		}
	}
	return a.finishRemovedBackgroundTasks(state, removed)
}

func (a *Agent) finishRemovedBackgroundTasks(state *cascadeState, tasks []backgroundTask) error {
	a.Mu.Lock()
	if !a.cascadeCurrentLocked(state) || state.closed {
		a.Mu.Unlock()
		return nil
	}
	ctx := a.streamCtx
	minimum := -1
	for _, task := range tasks {
		record := state.steps[task.stepIndex]
		if record != nil && isOpenStepStatus(record.status) && (minimum < 0 || task.stepIndex < minimum) {
			minimum = task.stepIndex
		}
	}
	a.Mu.Unlock()
	var steps []json.RawMessage
	var readErr error
	if minimum >= 0 {
		steps, readErr = a.rpc.steps(ctx, state.id, minimum)
	}
	var persistErr error
	for _, task := range tasks {
		if !a.acceptsCascadeUpdate(state) {
			return nil
		}
		offset := task.stepIndex - minimum
		if readErr != nil || minimum < 0 || offset < 0 || offset >= len(steps) {
			a.queueBackgroundTaskCloses(state, []backgroundTask{task}, bgtask.StatusEndedWithUnknownOutcome)
			continue
		}
		raw := steps[offset]
		step, err := decodeStep(raw)
		if err != nil {
			persistErr = errors.Join(persistErr, a.persistUnknownStep(state, task.stepIndex, raw))
		} else {
			persistErr = errors.Join(persistErr, a.applyStep(state, task.stepIndex, step, raw))
		}
		if err != nil || isOpenStepStatus(step.Status) {
			a.queueBackgroundTaskCloses(state, []backgroundTask{task}, bgtask.StatusEndedWithUnknownOutcome)
		} else {
			a.finishBackgroundStep(state, task, step)
		}
	}
	return persistErr
}

func (a *Agent) backgroundForStep(state *cascadeState, index int) (backgroundTask, bool) {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	task, ok := state.background[index]
	return task, ok
}

func (a *Agent) finishBackgroundStep(state *cascadeState, task backgroundTask, step antigravityStep) {
	status := bgtask.StatusSucceeded
	switch {
	case step.Status == "CORTEX_STEP_STATUS_CANCELED":
		status = bgtask.StatusStopped
	case step.Status == "CORTEX_STEP_STATUS_ERROR" || step.RunCommand.ExitCode != 0:
		status = bgtask.StatusFailed
	}
	a.queueBackgroundTaskCloses(state, []backgroundTask{task}, status)
}

func (a *Agent) closeBackgroundTasks(state *cascadeState, status bgtask.Status) {
	a.Mu.Lock()
	owned := make(map[string]backgroundTask, len(state.background)+len(state.closingBackground))
	for _, task := range state.background {
		owned[task.rowKey] = task
	}
	for key, task := range state.closingBackground {
		owned[key] = task
	}
	clear(state.background)
	clear(state.closingBackground)
	tasks := make([]backgroundTask, 0, len(owned))
	for _, task := range owned {
		tasks = append(tasks, task)
	}
	a.Mu.Unlock()
	a.queueBackgroundTaskCloses(state, tasks, status)
}

func backgroundTaskCloseWrite(sink agent.ProviderServices, rowKey string, status bgtask.Status) agent.OutputWrite {
	return agent.OutputWrite{
		ScopeID: rowKey, RetainedBytes: len(rowKey), Label: "close an Antigravity background task",
		Run: func() error { return sink.CloseBackgroundTask(rowKey, status) },
	}
}

func (a *Agent) queueBackgroundTaskCloses(state *cascadeState, tasks []backgroundTask, status bgtask.Status) {
	if len(tasks) == 0 {
		return
	}
	queue := state.sink.OutputQueue()
	writes := make([]agent.OutputWrite, 0, len(tasks))
	for _, task := range tasks {
		writes = append(writes, backgroundTaskCloseWrite(state.sink, task.rowKey, status))
	}
	a.Mu.Lock()
	queue.Append(writes...)
	for _, task := range tasks {
		if current, ok := state.closingBackground[task.rowKey]; ok && current == task {
			delete(state.closingBackground, task.rowKey)
		}
	}
	a.Mu.Unlock()
	a.flushBackgroundTaskWrites(queue)
}

func (a *Agent) queueChildRowClose(childID string, status bgtask.Status) {
	queue := a.sink.OutputQueue()
	a.Mu.Lock()
	queue.Append(backgroundTaskCloseWrite(a.sink, childID, status))
	a.Mu.Unlock()
	a.flushBackgroundTaskWrites(queue)
}

func (a *Agent) flushBackgroundTaskWrites(queue *agent.OutputQueue) {
	if err := queue.Flush(); err != nil && !errors.Is(err, agent.ErrOutputInFlight) {
		slog.Warn("antigravity background task update remains queued", "agent_id", a.AgentID(), "error", err)
	}
}

func (a *Agent) InterruptChild(childKey string, stop agent.StopContext) error {
	a.Mu.Lock()
	state := a.cascades[childKey]
	if state == nil || state.closed {
		a.Mu.Unlock()
		return nil
	}
	state.interrupted = true
	a.Mu.Unlock()
	if err := a.rpc.cancel(a.Context(), childKey); err != nil {
		return fmt.Errorf("cancel the Antigravity subagent: %w", err)
	}
	return nil
}
