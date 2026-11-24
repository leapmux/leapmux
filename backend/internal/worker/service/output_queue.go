package service

import (
	"context"
	"log/slog"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

func (handler *OutputHandler) waitForAgentOutput(ctx context.Context, agentID string) bool {
	handler.outputQueueMu.Lock()
	queue := handler.outputQueues[agentID]
	handler.outputQueueMu.Unlock()
	return queue == nil || queue.WaitForAdmittedWrites(ctx) == nil
}

// OutputQueue keeps the transcript's pending writes across provider replacements.
// Each captured write still uses its original sink and native session.
func (sink *agentOutputSink) OutputQueue() *agent.OutputQueue {
	return sink.h.outputQueue(sink.agentID)
}

func (handler *OutputHandler) outputQueue(agentID string) *agent.OutputQueue {
	handler.outputQueueMu.Lock()
	defer handler.outputQueueMu.Unlock()
	if handler.outputQueues == nil {
		handler.outputQueues = make(map[string]*agent.OutputQueue)
	}
	queue := handler.outputQueues[agentID]
	if queue == nil {
		queue = &agent.OutputQueue{}
		queue.ConfigureRetry(handler.shutdownCtx, handler.outputClock, func(err error) {
			slog.Error("retry pending transcript output", "agent_id", agentID, "error", err)
		})
		if handler.outputRetriesStopped {
			queue.StopRetries()
		}
		handler.outputQueues[agentID] = queue
	}
	return queue
}

// StopOutputRetries joins retries before shutdown drains their publication effects.
// A final attempt can still use the open database. It cannot start another retry timer.
func (handler *OutputHandler) StopOutputRetries() bool {
	handler.outputQueueMu.Lock()
	alreadyStopped := handler.outputRetriesStopped
	handler.outputRetriesStopped = true
	queues := make(map[string]*agent.OutputQueue, len(handler.outputQueues))
	for id, queue := range handler.outputQueues {
		queues[id] = queue
	}
	handler.outputQueueMu.Unlock()
	complete := true
	for id, queue := range queues {
		if !alreadyStopped {
			queue.StopRetries()
			if err := queue.Flush(); err != nil {
				slog.Error("persist final queued output", "agent_id", id, "error", err)
				complete = false
			}
		}
		if queue.HasPending() {
			complete = false
		}
	}
	return complete
}
