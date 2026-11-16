package kimi

import (
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
)

type kimiTodoSnapshot struct {
	sessionID string
	items     []todoevents.Item
	publisher agent.TranscriptOwner
}

func kimiTodoStatus(word string) todoevents.Status {
	switch word {
	case contracts.KimiTodoStatusPending:
		return todoevents.StatusPending
	case contracts.KimiTodoStatusInProgress:
		return todoevents.StatusInProgress
	case contracts.KimiTodoStatusDone:
		return todoevents.StatusCompleted
	default:
		return todoevents.StatusUnspecified
	}
}

// The flat native feed owns current state. Tool cards retain their original
// list, but older transcript replay cannot replace this unsequenced snapshot.
func (a *Agent) handleNativeTodo(frame kimiFrame) {
	if !a.nativeTranscriptSession(frame.SessionID) {
		return
	}
	var wire struct {
		AgentID string             `json:"agent_id"`
		Items   *[]json.RawMessage `json:"items"`
	}
	if json.Unmarshal(frame.Raw, &wire) != nil || kimiCheckID("agent", wire.AgentID) != nil || wire.Items == nil {
		return
	}
	items := make([]todoevents.Item, 0, min(len(*wire.Items), todoevents.MaxTodos))
	for _, raw := range *wire.Items {
		var item struct {
			Title  *string `json:"title"`
			Status *string `json:"status"`
		}
		if json.Unmarshal(raw, &item) != nil || item.Title == nil || item.Status == nil || strings.TrimSpace(*item.Title) == "" {
			return
		}
		status := kimiTodoStatus(*item.Status)
		if status == todoevents.StatusUnspecified {
			return
		}
		if len(items) < todoevents.MaxTodos {
			items = append(items, todoevents.Item{Content: *item.Title, Status: status})
		}
	}
	origin := agent.CaptureTranscript(a.sink, agent.MessageContent{}, agent.SpanInfo{}).FrozenContent()
	if origin.Publication == nil || origin.AgentSessionID != frame.SessionID {
		return
	}
	run := a.run(wire.AgentID)
	run.pendingTodo = &kimiTodoSnapshot{sessionID: frame.SessionID, items: items, publisher: origin.Publication.Owner()}
	a.publishNativeTodo(run)
}

// nativeStateSink reaches saved children without activating transcript progress.
// A finished transcript lifetime can retire while the native state feed stays live.
func (a *Agent) nativeStateSink(run *kimiRun) agent.ProviderServices {
	if run.agentID == kimiMainAgentID {
		return a.sink
	}
	if !a.ensureChildLifetime(run) {
		return nil
	}
	return a.runSink(run)
}

func (a *Agent) queueIdleStateCleanup(run *kimiRun) {
	seen := make(map[string]bool)
	for run.agentID != kimiMainAgentID && !seen[run.agentID] {
		seen[run.agentID] = true
		a.Mu.Lock()
		active := run.turnActive
		a.Mu.Unlock()
		if !active {
			a.queueChildCleanup(run)
		}
		if run.childParentAgentID == "" {
			return
		}
		run = a.run(run.childParentAgentID)
	}
}

func (a *Agent) publishNativeTodo(run *kimiRun) {
	snapshot := run.pendingTodo
	if snapshot == nil {
		return
	}
	if !a.nativeTranscriptSession(snapshot.sessionID) || snapshot.publisher == nil || !snapshot.publisher.IsSessionCurrent() {
		run.pendingTodo = nil
		return
	}
	sink := a.nativeStateSink(run)
	if sink == nil {
		return
	}
	captured := agent.CaptureTranscript(sink, agent.MessageContent{}, agent.SpanInfo{})
	// A root replacement between the initial check and child capture cannot
	// lend its authority to the older unlinked snapshot. Later replacements
	// expire the child capture's own immutable publisher fact at persistence.
	if !snapshot.publisher.IsSessionCurrent() {
		run.pendingTodo = nil
		a.queueIdleStateCleanup(run)
		return
	}
	prepared, err := sink.PrepareTodoSnapshot(captured, snapshot.items)
	if err == nil {
		err = a.queueRegistryWrite(sink, "publish Kimi native todo snapshot", func(agent.BackgroundTaskServices) error { _, err := prepared.Persist(); return err })
		if agent.OutputRetained(err) {
			run.pendingTodo = nil
			a.queueIdleStateCleanup(run)
			return
		}
	}
	slog.Warn("retain Kimi todo snapshot", "agent_id", a.AgentID(), "native_agent_id", run.agentID, "error", err)
}
