package service

import (
	"database/sql"
	"errors"
	"log/slog"
	"math"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

const (
	autoContinueInitialDelay = 10 * time.Second
	autoContinueMaxDelay     = 180 * time.Second
	autoContinueMultiplier   = 2.0
	autoContinueJitterFrac   = 0.2

	autoContinueContent = "Continue."

	autoContinueStateActive    = "active"
	autoContinueStateCancelled = "cancelled"
	autoContinueStateFired     = "fired"

	rateLimitJitterMin = 5 * time.Second
	rateLimitJitterMax = 30 * time.Second
	rateLimitJitterPct = 0.05
)

type autoContinueKey struct {
	AgentID string
	Reason  agent.AutoContinueReason
}

type autoContinueTimerState struct {
	mu    sync.Mutex
	timer *time.Timer
	dueAt time.Time
}

func (h *OutputHandler) restoreAutoContinueSchedules() {
	schedules, err := h.queries.ListActiveAutoContinueSchedules(bgCtx())
	if err != nil {
		slog.Error("auto-continue restore failed", "error", err)
		return
	}
	for _, schedule := range schedules {
		key := autoContinueKey{AgentID: schedule.AgentID, Reason: agent.AutoContinueReason(schedule.Reason)}
		h.armAutoContinueTimer(key, schedule.DueAt)
	}
}

func (h *OutputHandler) scheduleAutoContinue(agentID string, schedule agent.AutoContinueSchedule) {
	now := time.Now().UTC()
	if schedule.Content == "" {
		schedule.Content = autoContinueContent
	}

	record, err := h.buildAutoContinueRecord(agentID, schedule, now)
	if err != nil {
		slog.Error("auto-continue schedule build failed", "agent_id", agentID, "reason", schedule.Reason, "error", err)
		return
	}

	if err := h.queries.UpsertAutoContinueSchedule(bgCtx(), record); err != nil {
		slog.Error("auto-continue schedule persist failed", "agent_id", agentID, "reason", schedule.Reason, "error", err)
		return
	}

	key := autoContinueKey{AgentID: agentID, Reason: schedule.Reason}
	h.armAutoContinueTimer(key, record.DueAt)
}

func (h *OutputHandler) cancelAutoContinue(agentID string, reason agent.AutoContinueReason) {
	if err := h.queries.CancelAutoContinueSchedule(bgCtx(), db.CancelAutoContinueScheduleParams{
		AgentID: agentID,
		Reason:  string(reason),
	}); err != nil {
		slog.Error("auto-continue cancel failed", "agent_id", agentID, "reason", reason, "error", err)
	}
	h.stopAutoContinueTimer(autoContinueKey{AgentID: agentID, Reason: reason}, false)
}

func (h *OutputHandler) cleanupAutoContinue(agentID string) {
	if err := h.queries.CancelAllAutoContinueSchedulesByAgent(bgCtx(), agentID); err != nil {
		slog.Error("auto-continue cleanup failed", "agent_id", agentID, "error", err)
	}
	for _, reason := range []agent.AutoContinueReason{
		agent.AutoContinueReasonAPIError,
		agent.AutoContinueReasonRateLimit,
	} {
		h.stopAutoContinueTimer(autoContinueKey{AgentID: agentID, Reason: reason}, true)
	}
}

func (h *OutputHandler) buildAutoContinueRecord(agentID string, schedule agent.AutoContinueSchedule, now time.Time) (db.UpsertAutoContinueScheduleParams, error) {
	switch schedule.Reason {
	case agent.AutoContinueReasonAPIError:
		return h.buildAPIErrorScheduleRecord(agentID, schedule, now)
	case agent.AutoContinueReasonRateLimit:
		return h.buildRateLimitScheduleRecord(agentID, schedule, now), nil
	default:
		return db.UpsertAutoContinueScheduleParams{}, errors.New("unknown auto-continue reason")
	}
}

func (h *OutputHandler) buildAPIErrorScheduleRecord(agentID string, schedule agent.AutoContinueSchedule, now time.Time) (db.UpsertAutoContinueScheduleParams, error) {
	delay := autoContinueInitialDelay
	nextBackoff := time.Duration(float64(autoContinueInitialDelay) * autoContinueMultiplier)

	existing, err := h.queries.GetAutoContinueSchedule(bgCtx(), db.GetAutoContinueScheduleParams{
		AgentID: agentID,
		Reason:  string(schedule.Reason),
	})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return db.UpsertAutoContinueScheduleParams{}, err
	}
	if err == nil && existing.State == autoContinueStateActive && existing.NextBackoffMs > 0 {
		delay = time.Duration(existing.NextBackoffMs) * time.Millisecond
		nextBackoff = time.Duration(float64(delay) * autoContinueMultiplier)
	}

	if delay > autoContinueMaxDelay {
		delay = autoContinueMaxDelay
	}
	if nextBackoff > autoContinueMaxDelay {
		nextBackoff = autoContinueMaxDelay
	}

	jitter := symmetricJitter(delay, autoContinueJitterFrac)
	dueAt := now.Add(delay + jitter)
	return db.UpsertAutoContinueScheduleParams{
		AgentID:       agentID,
		Reason:        string(schedule.Reason),
		Content:       schedule.Content,
		DueAt:         dueAt,
		JitterMs:      jitter.Milliseconds(),
		NextBackoffMs: nextBackoff.Milliseconds(),
		SourcePayload: cloneBytes(schedule.SourcePayload),
	}, nil
}

func (h *OutputHandler) buildRateLimitScheduleRecord(agentID string, schedule agent.AutoContinueSchedule, now time.Time) db.UpsertAutoContinueScheduleParams {
	jitter := positiveRateLimitJitter(schedule.DueAt.Sub(now))
	dueAt := schedule.DueAt.UTC().Add(jitter)
	return db.UpsertAutoContinueScheduleParams{
		AgentID:       agentID,
		Reason:        string(schedule.Reason),
		Content:       schedule.Content,
		DueAt:         dueAt,
		JitterMs:      jitter.Milliseconds(),
		NextBackoffMs: 0,
		SourcePayload: cloneBytes(schedule.SourcePayload),
	}
}

func (h *OutputHandler) armAutoContinueTimer(key autoContinueKey, dueAt time.Time) {
	v, _ := h.autoContinue.LoadOrStore(key, &autoContinueTimerState{})
	state := v.(*autoContinueTimerState)

	state.mu.Lock()
	defer state.mu.Unlock()

	if state.timer != nil {
		state.timer.Stop()
	}
	state.dueAt = dueAt

	delay := time.Until(dueAt)
	if delay < 0 {
		delay = 0
	}

	state.timer = time.AfterFunc(delay, func() {
		h.fireAutoContinue(key, dueAt)
	})

	slog.Info("auto-continue scheduled",
		"agent_id", key.AgentID,
		"reason", key.Reason,
		"due_at", dueAt,
	)
}

func (h *OutputHandler) stopAutoContinueTimer(key autoContinueKey, remove bool) {
	v, ok := h.autoContinue.Load(key)
	if !ok {
		return
	}
	state := v.(*autoContinueTimerState)
	state.mu.Lock()
	if state.timer != nil {
		state.timer.Stop()
		state.timer = nil
	}
	state.mu.Unlock()
	if remove {
		h.autoContinue.Delete(key)
	}
}

func (h *OutputHandler) fireAutoContinue(key autoContinueKey, dueAt time.Time) {
	defer h.stopAutoContinueTimer(key, false)

	schedule, err := h.queries.GetAutoContinueSchedule(bgCtx(), db.GetAutoContinueScheduleParams{
		AgentID: key.AgentID,
		Reason:  string(key.Reason),
	})
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Error("auto-continue fire read failed", "agent_id", key.AgentID, "reason", key.Reason, "error", err)
		}
		return
	}
	if schedule.State != autoContinueStateActive || !schedule.DueAt.Equal(dueAt) {
		return
	}

	isOpen, err := h.queries.IsAgentOpen(bgCtx(), key.AgentID)
	if err != nil {
		slog.Error("auto-continue fire open-check failed", "agent_id", key.AgentID, "reason", key.Reason, "error", err)
		return
	}
	if isOpen == 0 {
		_ = h.queries.CancelAutoContinueSchedule(bgCtx(), db.CancelAutoContinueScheduleParams{
			AgentID: key.AgentID,
			Reason:  string(key.Reason),
		})
		return
	}

	if err := h.queries.MarkAutoContinueScheduleFired(bgCtx(), db.MarkAutoContinueScheduleFiredParams{
		AgentID: key.AgentID,
		Reason:  string(key.Reason),
	}); err != nil {
		slog.Error("auto-continue fire mark failed", "agent_id", key.AgentID, "reason", key.Reason, "error", err)
		return
	}
	if h.sendMessageFunc == nil {
		slog.Warn("auto-continue: sendMessageFunc not set", "agent_id", key.AgentID, "reason", key.Reason)
		return
	}

	slog.Info("auto-continue firing", "agent_id", key.AgentID, "reason", key.Reason)
	h.sendMessageFunc(key.AgentID, schedule.Content)
}

func symmetricJitter(delay time.Duration, frac float64) time.Duration {
	if delay <= 0 || frac <= 0 {
		return 0
	}
	window := float64(delay) * frac
	return time.Duration(window * (2*rand.Float64() - 1))
}

func positiveRateLimitJitter(remaining time.Duration) time.Duration {
	if remaining < 0 {
		remaining = 0
	}
	target := time.Duration(math.Round(float64(remaining) * rateLimitJitterPct))
	if target < rateLimitJitterMin {
		target = rateLimitJitterMin
	}
	if target > rateLimitJitterMax {
		target = rateLimitJitterMax
	}
	if target <= 0 {
		return rateLimitJitterMin
	}
	return time.Duration(rand.Int64N(int64(target))) + 1
}

func cloneBytes(src []byte) []byte {
	if len(src) == 0 {
		return []byte{}
	}
	dst := make([]byte, len(src))
	copy(dst, src)
	return dst
}
