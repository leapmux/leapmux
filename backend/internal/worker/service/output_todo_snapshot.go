package service

import (
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
)

type capturedTodoSnapshot struct {
	source nativeSessionSource
	items  []todoevents.Item
}

func (sink *agentOutputSink) PrepareTodoSnapshot(captured agent.CapturedTranscript, items []todoevents.Item) (agent.CapturedTodoSnapshot, error) {
	source, err := sink.capturedNativeSessionSource(captured)
	if err != nil {
		return nil, err
	}
	prepared, err := todoevents.PrepareSnapshot(items)
	if err != nil {
		return nil, err
	}
	return &capturedTodoSnapshot{source: source, items: prepared}, nil
}

func (snapshot *capturedTodoSnapshot) Persist() (bool, error) {
	source := snapshot.source
	release := source.lock()
	if !source.currentLocked() {
		release()
		return false, nil
	}
	sink := source.sink
	cache := sink.h.todoCache(sink.agentID)
	cache.Mu.Lock()
	reg := cache.on(sink.h.queries, sink.agentID)
	err := reg.ensureSeededLocked(bgCtx())
	var items []todoevents.Item
	var changed bool
	if err == nil {
		items, changed, err = applyTodoSnapshotLocked(bgCtx(), reg, sink.h.db, snapshot.items)
	}
	if err == nil && changed {
		sink.h.enqueueTodoChange(sink.agentID, items)
	}
	cache.Mu.Unlock()
	release()
	if err != nil {
		return false, err
	}
	if changed {
		sink.h.watcher.DrainAgentEvents(sink.agentID)
	}
	return true, nil
}
