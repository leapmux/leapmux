package service

import (
	"log/slog"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// OutputQueue keeps the transcript's pending writes across provider replacements.
// Each captured write still uses its original sink and native session.
func (sink *agentOutputSink) OutputQueue() *agent.OutputQueue {
	handler := sink.h
	handler.outputQueueMu.Lock()
	defer handler.outputQueueMu.Unlock()
	if handler.outputQueues == nil {
		handler.outputQueues = make(map[string]*agent.OutputQueue)
	}
	queue := handler.outputQueues[sink.agentID]
	if queue == nil {
		queue = &agent.OutputQueue{}
		agentID := sink.agentID
		queue.ConfigureRetry(handler.shutdownCtx, handler.outputClock, func(err error) {
			slog.Error("retry pending transcript output", "agent_id", agentID, "error", err)
		})
		if handler.outputRetriesStopped {
			queue.StopRetries()
		}
		handler.outputQueues[sink.agentID] = queue
	}
	return queue
}

// StopOutputRetries joins retries before shutdown drains their publication effects.
// A final attempt can still use the open database. It cannot start another retry timer.
func (handler *OutputHandler) StopOutputRetries() {
	handler.outputQueueMu.Lock()
	if handler.outputRetriesStopped {
		handler.outputQueueMu.Unlock()
		return
	}
	handler.outputRetriesStopped = true
	queues := make(map[string]*agent.OutputQueue, len(handler.outputQueues))
	for id, queue := range handler.outputQueues {
		queues[id] = queue
	}
	handler.outputQueueMu.Unlock()
	for id, queue := range queues {
		queue.StopRetries()
		if err := queue.Flush(); err != nil {
			slog.Error("persist final queued output", "agent_id", id, "error", err)
		}
	}
}
