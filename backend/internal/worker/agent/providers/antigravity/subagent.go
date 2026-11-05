package antigravity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

func (a *Agent) ensureChildren(parent *cascadeState) {
	a.Mu.Lock()
	records := make([]*childKey, 0, len(parent.childKeys))
	for _, record := range parent.childKeys {
		if record.state == nil && !record.refused {
			records = append(records, record)
		}
	}
	a.Mu.Unlock()
	for range records {
		a.ensureOneChild(parent)
	}
	a.linkPendingChildSpans(parent)
}

func (a *Agent) ensureOneChild(parent *cascadeState) {
	a.Mu.Lock()
	var childID string
	var record *childKey
	for id, candidate := range parent.childKeys {
		if candidate.state == nil && !candidate.refused {
			childID, record = id, candidate
			break
		}
	}
	if record == nil {
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
	state := newCascadeState(childID, parent.sink.ChildSink(childAgentID), parent.generation, stored)
	state.parent = parent
	state.childAgentID = childAgentID
	state.rowOpen = true
	a.Mu.Lock()
	if parent.childKeys[childID] != record || parent.generation != state.generation {
		a.Mu.Unlock()
		return
	}
	record.state = state
	a.cascades[childID] = state
	a.Mu.Unlock()
	a.persistChildPromptOnce(record, state)
	providerkit.LogRegistryRefusal("antigravity", "upsert", a.sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: childID, ChildAgentID: childAgentID, Kind: bgtask.KindSubagent,
		Title: role, Description: role, Status: bgtask.StatusRunning,
	}))
	if stored && idle {
		a.closeChildRow(state, bgtask.StatusEndedWithUnknownOutcome)
	}
	if stored {
		go a.startStoredChild(state)
		return
	}
	a.startCascadeStream(state)
}

func (a *Agent) startStoredChild(state *cascadeState) {
	var err error
	for attempt := range 3 {
		var count int
		attemptCtx, cancel := context.WithTimeout(a.streamCtx, a.rpc.timeout)
		count, err = a.rpc.readStepCount(attemptCtx, state.id)
		cancel()
		if err == nil {
			a.Mu.Lock()
			if state.closed || a.cascades[state.id] != state {
				a.Mu.Unlock()
				return
			}
			state.seededCount = count
			a.Mu.Unlock()
			a.startCascadeStream(state)
			return
		}
		if isStreamNotFound(err) {
			break
		}
		if attempt < 2 {
			timer := a.clock.NewTimer(time.Duration(attempt+1)*100*time.Millisecond, antigravityStreamReconnectTimerTag)
			select {
			case <-a.streamCtx.Done():
				timer.Stop(antigravityStreamReconnectTimerTag)
				return
			case <-timer.C:
			}
		}
	}
	a.fatalCascade(state, err)
}

func (a *Agent) linkSubagentResults(parent *cascadeState, index int, step antigravityStep) {
	if step.Type != "CORTEX_STEP_TYPE_INVOKE_SUBAGENT" {
		return
	}
	var arguments struct {
		Prompt string `json:"Prompt"`
	}
	_ = json.Unmarshal([]byte(step.Metadata.ToolCall.ArgumentsJSON), &arguments)
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
		if arguments.Prompt != "" {
			record.prompt = arguments.Prompt
		}
		state := record.state
		a.Mu.Unlock()
		if state != nil {
			a.persistChildPromptOnce(record, state)
		}
	}
}

func (a *Agent) persistChildPromptOnce(record *childKey, state *cascadeState) {
	a.Mu.Lock()
	prompt := record.prompt
	if prompt == "" || record.promptPersisted {
		a.Mu.Unlock()
		return
	}
	record.promptPersisted = true
	a.Mu.Unlock()
	raw, _ := json.Marshal(map[string]any{
		"type": contracts.AntigravityStepTypeUserInput, "status": contracts.AntigravityStepStatusDone,
		"userInput": map[string]any{"userResponse": prompt},
	})
	err := state.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{
		Original: raw, AgentSessionID: state.id, IdempotencyKey: "antigravity:" + state.id + ":initial-prompt",
	}, agent.SpanInfo{})
	if err == nil {
		return
	}
	a.Mu.Lock()
	record.promptPersisted = false
	a.Mu.Unlock()
	slog.Error("antigravity persist child prompt", "agent_id", a.AgentID(), "cascade_id", state.id, "error", err)
}

func (a *Agent) linkPendingChildSpans(parent *cascadeState) {
	a.Mu.Lock()
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
	}
}

func (a *Agent) applyBackgroundTasks(state *cascadeState, patch *backgroundTasksPatch, firstOnAttempt bool) {
	if patch == nil && !firstOnAttempt {
		return
	}
	a.Mu.Lock()
	previous := state.background
	current := map[int]backgroundTask{}
	if !firstOnAttempt {
		for index, task := range previous {
			current[index] = task
		}
	}
	if patch != nil {
		for position, snapshot := range patch.BackgroundTasks {
			stepIndex := snapshot.TaskSnapshot.StepIndex
			slot := position
			if position < len(patch.Indices) {
				slot = patch.Indices[position]
			}
			logPath := snapshot.TaskSnapshot.LogURI
			if parsed, err := url.Parse(logPath); err == nil && parsed.Scheme == "file" {
				logPath = parsed.Path
			}
			current[stepIndex] = backgroundTask{
				rowKey: snapshot.TaskSnapshot.TaskID, title: snapshot.TaskSnapshot.Description,
				description: logPath, stepIndex: stepIndex, slot: slot,
			}
		}
		for index, task := range current {
			if patch.TotalLength > 0 && task.slot >= patch.TotalLength {
				delete(current, index)
			}
		}
	}
	state.background = current
	var removed []backgroundTask
	for index, task := range previous {
		if _, keeps := current[index]; !keeps {
			removed = append(removed, task)
		}
	}
	a.Mu.Unlock()
	for _, task := range current {
		providerkit.LogRegistryRefusal("antigravity", "upsert shell", state.sink.UpsertBackgroundTask(bgtask.Upsert{
			RowKey: task.rowKey, Kind: bgtask.KindShell, Title: task.title,
			TitleIsCommand: true, Description: task.description, Status: bgtask.StatusRunning,
		}))
	}
	for _, task := range removed {
		a.finishRemovedBackgroundTask(state, task)
	}
}

func (a *Agent) finishRemovedBackgroundTask(state *cascadeState, task backgroundTask) {
	a.Mu.Lock()
	record := state.steps[task.stepIndex]
	open := record != nil && isOpenStepStatus(record.status)
	a.Mu.Unlock()
	if !open {
		return
	}
	steps, err := a.rpc.steps(a.Context(), state.id, task.stepIndex)
	if err != nil || len(steps) == 0 {
		if err == nil {
			err = fmt.Errorf("the language server returned no step at index %d", task.stepIndex)
		}
		slog.Warn("antigravity read finished background step", "agent_id", a.AgentID(), "error", err)
		providerkit.LogRegistryRefusal("antigravity", "close shell", state.sink.CloseBackgroundTask(task.rowKey, bgtask.StatusEndedWithUnknownOutcome))
		return
	}
	step, err := decodeStep(steps[0])
	if err != nil {
		a.persistUnknownStep(state, task.stepIndex, steps[0])
		return
	}
	a.applyStep(state, task.stepIndex, step, steps[0])
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
	providerkit.LogRegistryRefusal("antigravity", "close shell", state.sink.CloseBackgroundTask(task.rowKey, status))
}

func (a *Agent) closeBackgroundTasks(state *cascadeState, status bgtask.Status) {
	a.Mu.Lock()
	tasks := make([]backgroundTask, 0, len(state.background))
	for _, task := range state.background {
		tasks = append(tasks, task)
	}
	clear(state.background)
	a.Mu.Unlock()
	for _, task := range tasks {
		providerkit.LogRegistryRefusal("antigravity", "close shell", state.sink.CloseBackgroundTask(task.rowKey, status))
	}
}

func (a *Agent) InterruptChild(childKey string, stop agent.StopContext) error {
	a.Mu.Lock()
	state := a.cascades[childKey]
	if state == nil || state.closed {
		a.Mu.Unlock()
		return nil
	}
	state.stopped = true
	state.interrupted = true
	a.Mu.Unlock()
	if err := a.rpc.cancel(a.Context(), childKey); err != nil {
		return fmt.Errorf("cancel the Antigravity subagent: %w", err)
	}
	return nil
}
