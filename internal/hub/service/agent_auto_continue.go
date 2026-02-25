package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v5"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/id"
	"github.com/leapmux/leapmux/internal/hub/msgcodec"
	"github.com/leapmux/leapmux/internal/util/timefmt"
)

// autoContinueState tracks backoff and cancellation for auto-continue
// retries on a single agent.
type autoContinueState struct {
	mu      sync.Mutex
	backoff *backoff.ExponentialBackOff
	cancel  context.CancelFunc // cancels the pending goroutine; nil if none
}

// newAutoContinueBackoff creates an exponential backoff for auto-continue:
// 10s → 180s, multiplier 2x, ±20% jitter.
var newAutoContinueBackoff = func() *backoff.ExponentialBackOff {
	b := backoff.NewExponentialBackOff()
	b.InitialInterval = 10 * time.Second
	b.MaxInterval = 180 * time.Second
	b.Multiplier = 2.0
	b.RandomizationFactor = 0.2
	b.Reset()
	return b
}

// isSyntheticAPIError checks whether an assistant message is a synthetic
// error emitted by Claude Code after an API 5xx failure.
//
// Detection criteria:
//  1. Top-level "error" field is non-empty
//  2. "message.model" is "<synthetic>"
//  3. First text content block starts with "API Error: 5"
func isSyntheticAPIError(content []byte) bool {
	var msg struct {
		Error   string `json:"error"`
		Message *struct {
			Model   string `json:"model"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(content, &msg); err != nil {
		return false
	}
	if msg.Error == "" {
		return false
	}
	if msg.Message == nil || msg.Message.Model != "<synthetic>" {
		return false
	}
	for _, block := range msg.Message.Content {
		if block.Type == "text" {
			return strings.HasPrefix(block.Text, "API Error: 5")
		}
	}
	return false
}

// scheduleAutoContinue schedules an automatic "Continue." message for the
// given agent after an exponential backoff delay. If a previous auto-continue
// is already pending, it is cancelled and the backoff advances.
func (s *AgentService) scheduleAutoContinue(agentID string) {
	v, _ := s.autoContinue.LoadOrStore(agentID, &autoContinueState{
		backoff: newAutoContinueBackoff(),
	})
	state := v.(*autoContinueState)

	state.mu.Lock()
	// Cancel any existing pending auto-continue.
	if state.cancel != nil {
		state.cancel()
	}
	interval := state.backoff.NextBackOff()
	ctx, cancel := context.WithCancel(context.Background())
	state.cancel = cancel
	state.mu.Unlock()

	slog.Info("scheduling auto-continue after API error",
		"agent_id", agentID,
		"delay", interval)

	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
		s.sendAutoContinueMessage(ctx, agentID)
	}()
}

// cancelAutoContinue cancels any pending auto-continue goroutine for the
// agent without resetting the backoff interval.
func (s *AgentService) cancelAutoContinue(agentID string) {
	v, ok := s.autoContinue.Load(agentID)
	if !ok {
		return
	}
	state := v.(*autoContinueState)
	state.mu.Lock()
	if state.cancel != nil {
		state.cancel()
		state.cancel = nil
	}
	state.mu.Unlock()
}

// resetAutoContinue cancels any pending auto-continue goroutine AND resets
// the backoff to the initial interval. Called when the agent produces a
// normal (non-error) assistant message.
func (s *AgentService) resetAutoContinue(agentID string) {
	v, ok := s.autoContinue.Load(agentID)
	if !ok {
		return
	}
	state := v.(*autoContinueState)
	state.mu.Lock()
	if state.cancel != nil {
		state.cancel()
		state.cancel = nil
	}
	state.backoff.Reset()
	state.mu.Unlock()
}

// cleanupAutoContinue cancels any pending auto-continue and removes the
// state entirely. Called when an agent is closed.
func (s *AgentService) cleanupAutoContinue(agentID string) {
	v, ok := s.autoContinue.LoadAndDelete(agentID)
	if !ok {
		return
	}
	state := v.(*autoContinueState)
	state.mu.Lock()
	if state.cancel != nil {
		state.cancel()
		state.cancel = nil
	}
	state.mu.Unlock()
}

// sendAutoContinueMessage persists and delivers a "Continue." user message
// to the given agent. This is the internal counterpart of SendAgentMessage,
// bypassing authentication and RPC.
func (s *AgentService) sendAutoContinueMessage(ctx context.Context, agentID string) {
	const content = "Continue."

	agent, err := s.queries.GetAgentByID(ctx, agentID)
	if err != nil {
		slog.Debug("auto-continue: agent not found, skipping", "agent_id", agentID, "error", err)
		return
	}
	if agent.Status != leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE {
		slog.Debug("auto-continue: agent not active, skipping", "agent_id", agentID, "status", agent.Status)
		return
	}

	wsInternal, err := s.queries.GetWorkspaceByIDInternal(ctx, agent.WorkspaceID)
	if err != nil {
		slog.Warn("auto-continue: workspace not found", "agent_id", agentID, "error", err)
		return
	}

	// Persist the user message.
	contentJSON, _ := json.Marshal(map[string]string{"content": content})
	wrapped := wrapContent(contentJSON)
	compressed, compressionType := msgcodec.Compress(wrapped)
	msgID := id.Generate()
	now := time.Now()
	seq, err := s.queries.CreateMessage(ctx, db.CreateMessageParams{
		ID:                 msgID,
		AgentID:            agentID,
		Role:               leapmuxv1.MessageRole_MESSAGE_ROLE_USER,
		Content:            compressed,
		ContentCompression: compressionType,
		ThreadID:           "",
		CreatedAt:          now,
	})
	if err != nil {
		slog.Warn("auto-continue: failed to persist message", "agent_id", agentID, "error", err)
		return
	}

	// Broadcast to watchers.
	s.broadcastMessage(agentID, &leapmuxv1.AgentChatMessage{
		Id:                 msgID,
		Role:               leapmuxv1.MessageRole_MESSAGE_ROLE_USER,
		Content:            compressed,
		ContentCompression: compressionType,
		Seq:                seq,
		CreatedAt:          timefmt.Format(now),
	})

	// Deliver to worker.
	msgCtx, msgCancel := context.WithTimeout(context.Background(), s.timeoutCfg.APITimeout())
	defer msgCancel()

	agentNotFound, deliveryErr := s.deliverMessageToWorker(msgCtx, agent.WorkerID, agent.WorkspaceID, agentID, content)
	if agentNotFound {
		slog.Info("auto-continue: agent process not found, restarting before retry", "agent_id", agentID)
		msgCancel()
		msgCtx, msgCancel = context.WithTimeout(context.Background(), s.timeoutCfg.AgentStartupTimeout()+s.timeoutCfg.APITimeout())
		defer msgCancel()
		if restartErr := s.ensureAgentActive(msgCtx, &agent, &wsInternal); restartErr == nil {
			_, deliveryErr = s.deliverMessageToWorker(msgCtx, agent.WorkerID, agent.WorkspaceID, agentID, content)
		}
	}
	if deliveryErr != nil {
		slog.Warn("auto-continue: delivery failed", "agent_id", agentID, "error", deliveryErr)
		s.setDeliveryError(context.Background(), agentID, msgID, deliveryErr.Error())
		return
	}

	slog.Info("auto-continue: sent Continue. message", "agent_id", agentID)
}
