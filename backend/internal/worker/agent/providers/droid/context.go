package droid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/coder/quartz"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

type droidCompaction struct {
	requestID      string
	sessionID      string
	done           chan struct{}
	replySucceeded bool
}

// Droid keeps compact_session open while its model writes the summary.
const droidCompactionTimeout = 20 * time.Minute

// CompactContext starts an in-place native compaction and owns its turn.
func (a *Agent) CompactContext() error {
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	id := a.nextRequestID()
	a.Mu.Lock()
	sessionID, active, compacting, stopped := a.sessionID, a.turnActive, a.compaction != nil, a.stopped
	pending := &droidCompaction{requestID: id, sessionID: sessionID, done: make(chan struct{})}
	if sessionID != "" && !active && !compacting && !stopped {
		a.compaction = pending
	}
	a.Mu.Unlock()
	if stopped {
		return errAgentStopped
	}
	if sessionID == "" {
		return agent.ErrCompactionUnsupported
	}
	if active || compacting {
		return agent.ErrAgentBusy
	}
	if err := a.writeRequest(id, droidMethodCompactSession, map[string]string{"sessionId": sessionID}); err != nil {
		a.finishCompaction(pending, false)
		return err
	}
	go a.awaitCompactionResult(pending)
	return nil
}

// awaitCompactionResult catches a missing completion frame or a stopped process.
func (a *Agent) awaitCompactionResult(pending *droidCompaction) {
	ctx := a.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	clock := a.clock
	if clock == nil {
		clock = quartz.NewReal()
	}
	timeout := max(droidCompactionTimeout, a.APITimeout())
	timer := clock.NewTimer(timeout, "droid", "compaction")
	defer timer.Stop()
	select {
	case <-pending.done:
	case <-a.ProcessDone():
		a.dispatchMu.Lock()
		a.finishCompaction(pending, false)
		a.dispatchMu.Unlock()
	case <-ctx.Done():
		a.finishCompactionAfterWait(pending, ctx.Err())
	case <-timer.C:
		a.finishCompactionAfterWait(pending, context.DeadlineExceeded)
	}
}

// finishCompactionAfterWait serializes a missing native result with reader frames.
func (a *Agent) finishCompactionAfterWait(pending *droidCompaction, reason error) {
	if a.IsStopped() {
		return
	}
	a.dispatchMu.Lock()
	a.Mu.Lock()
	current := a.compaction == pending
	waitingForCompletion := current && pending.replySucceeded
	a.Mu.Unlock()
	if !current {
		a.dispatchMu.Unlock()
		return
	}
	phase := "reply"
	if waitingForCompletion {
		phase = "completion"
	}
	a.reportCompactionFailure(fmt.Errorf("droid compaction %s: %w", phase, reason))
	a.finishCompaction(pending, false)
	a.DiscardOutput()
	a.dispatchMu.Unlock()
	a.Stop()
}

// handleCompactionResponse keeps a successful reply pending until the native
// session_compacted notification. An error reply ends the compact turn here.
func (a *Agent) handleCompactionResponse(response droidEnvelope) bool {
	a.Mu.Lock()
	pending := a.compaction
	a.Mu.Unlock()
	if pending == nil || pending.requestID != response.ID {
		return false
	}
	if response.Error != nil {
		a.reportCompactionFailure(response.Error)
		a.finishCompactionFromReader(pending, false)
		return true
	}
	var result struct {
		NewSessionID string `json:"newSessionId"`
		RemovedCount *int64 `json:"removedCount"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil || result.NewSessionID != pending.sessionID || result.RemovedCount == nil || *result.RemovedCount < 0 {
		a.reportCompactionFailure(errors.New("droid returned an invalid compaction result"))
		a.finishCompaction(pending, false)
		a.DiscardOutput()
		// Stop runs separately because child transcript tails can need dispatchMu.
		go a.Stop()
		return true
	}
	a.Mu.Lock()
	if a.compaction == pending {
		pending.replySucceeded = true
	}
	a.Mu.Unlock()
	return true
}

// finishCompaction clears only the native request that still owns the guard.
// Only a completed boundary ends the native turn; a refusal can leave one active.
func (a *Agent) finishCompaction(pending *droidCompaction, completed bool) bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if a.compaction != pending {
		return false
	}
	a.compaction = nil
	if completed {
		a.turnActive = false
	}
	close(pending.done)
	return true
}

// finishCompactionFromReader releases the input queue in native frame order.
func (a *Agent) finishCompactionFromReader(pending *droidCompaction, completed bool) {
	if a.finishCompaction(pending, completed) {
		a.PublishTurnActive()
	}
}

// finishCompactionFromNotification accepts the native completed boundary.
func (a *Agent) finishCompactionFromNotification() {
	a.Mu.Lock()
	pending := a.compaction
	a.Mu.Unlock()
	if pending != nil {
		a.finishCompactionFromReader(pending, true)
	}
}

func (a *Agent) reportCompactionFailure(err error) {
	if a.IsStopped() {
		return
	}
	a.sink.PersistLeapMuxNotification(map[string]any{
		contracts.NotificationFieldType:  contracts.NotificationTypeAgentError,
		contracts.NotificationFieldError: err.Error(),
	})
}

// droidContextStats is Droid's estimated context report after a turn.
type droidContextStats struct {
	Used      *int64 `json:"used"`
	Remaining *int64 `json:"remaining"`
	Limit     *int64 `json:"limit"`
}

// refreshContextUsage reads one native report without blocking stdout dispatch.
func (a *Agent) refreshContextUsage() {
	a.rpcMu.Lock()
	if a.contextRefreshPending {
		a.contextRefreshQueued = true
		a.rpcMu.Unlock()
		return
	}
	a.contextRefreshPending = true
	a.rpcMu.Unlock()
	go a.readContextUsage()
}

func (a *Agent) readContextUsage() {
	defer a.finishContextRefresh()
	id := a.nextRequestID()
	reply := a.registerReply(id)
	defer a.unregisterReply(id)
	if err := a.writeRequest(id, droidMethodGetContextStats, struct{}{}); err != nil {
		slog.Debug("droid context request failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	ctx := a.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, a.APITimeout())
	defer cancel()
	select {
	case response := <-reply:
		if response.Error != nil {
			slog.Debug("droid context reply failed", "agent_id", a.AgentID(), "error", response.Error)
			return
		}
		a.applyContextStats(response.Result)
	case <-a.ProcessDone():
	case <-ctx.Done():
		slog.Debug("droid context reply timed out", "agent_id", a.AgentID(), "error", ctx.Err())
	}
}

// finishContextRefresh keeps the newest turn from losing its context reading.
func (a *Agent) finishContextRefresh() {
	a.rpcMu.Lock()
	queued := a.contextRefreshQueued
	a.contextRefreshQueued = false
	if !queued {
		a.contextRefreshPending = false
	}
	a.rpcMu.Unlock()
	if queued {
		go a.readContextUsage()
	}
}

// applyContextStats broadcasts only a complete, nonnegative native reading.
func (a *Agent) applyContextStats(raw json.RawMessage) {
	var stats droidContextStats
	if json.Unmarshal(raw, &stats) != nil || stats.Used == nil || stats.Limit == nil {
		return
	}
	if *stats.Used < 0 || *stats.Limit <= 0 || (stats.Remaining != nil && *stats.Remaining < 0) {
		return
	}
	percent := math.Min(100, float64(*stats.Used)/float64(*stats.Limit)*100)
	a.sink.BroadcastSessionInfo(map[string]any{
		contracts.SessionInfoKeyContextUsage: map[string]any{
			contracts.ContextUsageFieldContextTokens: *stats.Used,
			contracts.ContextUsageFieldContextWindow: *stats.Limit,
			contracts.ContextUsageFieldUsagePercent:  percent,
		},
	})
}
