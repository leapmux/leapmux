package agenttest

import (
	"errors"
	"slices"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
)

type sinkTodoSnapshot struct {
	sink  *Sink
	owner *testTranscriptOwner
	items []todoevents.Item
}

func (sink *Sink) PrepareTodoSnapshot(captured agent.CapturedTranscript, items []todoevents.Item) (agent.CapturedTodoSnapshot, error) {
	content := captured.FrozenContent()
	if content.Publication == nil {
		return nil, errors.New("the captured todo snapshot has no publication owner")
	}
	owner, ok := content.Publication.Owner().(*testTranscriptOwner)
	if !ok || owner == nil || owner.sink != sink || owner.key.session == nil || content.AgentSessionID != owner.key.sessionID || content.AgentSessionID != owner.key.session.id {
		return nil, errors.New("the captured todo snapshot belongs to another source")
	}
	prepared, err := todoevents.PrepareSnapshot(items)
	if err != nil {
		return nil, err
	}
	return &sinkTodoSnapshot{sink: sink, owner: owner, items: prepared}, nil
}

func (snapshot *sinkTodoSnapshot) Persist() (bool, error) {
	release := snapshot.sink.lockNativeSessionSource()
	defer release()
	if !snapshot.owner.currentNativeStateSourceLocked() {
		return false, nil
	}
	snapshot.sink.todoItems = slices.Clone(snapshot.items)
	return true, nil
}

func (sink *Sink) TodoSnapshot() []todoevents.Item {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return slices.Clone(sink.todoItems)
}

type nopTodoSnapshot struct{}

func (nopTodoSnapshot) Persist() (bool, error) { return false, nil }
func (nop) PrepareTodoSnapshot(agent.CapturedTranscript, []todoevents.Item) (agent.CapturedTodoSnapshot, error) {
	return nopTodoSnapshot{}, nil
}
