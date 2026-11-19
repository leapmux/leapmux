package kimi

import (
	"context"
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
)

// kimiAgentState is the flat native agent frame, including required mode flags.
type kimiAgentState struct {
	AgentID    string `json:"agent_id"`
	Status     string `json:"status"`
	Compacting *bool  `json:"compacting"`
	Model      string `json:"model"`
	Thinking   string `json:"thinking"`
	Permission string `json:"permission_mode"`
	PlanMode   *bool  `json:"plan_mode"`
	SwarmMode  *bool  `json:"swarm_mode"`
	TowerMode  *bool  `json:"tower_mode"`
}

type kimiUsageState struct {
	AgentID          string `json:"agent_id"`
	ContextTokens    *int64 `json:"context_tokens"`
	MaxContextTokens *int64 `json:"max_context_tokens"`
}

// kimiSessionPushState projects the push stream. Initial state follows the subscribe acknowledgement.
type kimiSessionPushState struct {
	agent *kimiAgentState
	usage *kimiUsageState
}

func decodeKimiAgentState(frame kimiFrame) (*kimiAgentState, bool) {
	var state kimiAgentState
	if frame.SessionID == "" || json.Unmarshal(frame.Raw, &state) != nil || state.AgentID == "" ||
		(state.Status != "idle" && state.Status != "running") || state.Compacting == nil || state.PlanMode == nil || state.SwarmMode == nil || state.TowerMode == nil ||
		(state.Permission != "" && !kimiValidPermission(state.Permission)) {
		return nil, false
	}
	return &state, true
}

func decodeKimiUsageState(frame kimiFrame) (*kimiUsageState, bool) {
	var state kimiUsageState
	if frame.SessionID == "" || json.Unmarshal(frame.Raw, &state) != nil || state.AgentID == "" || state.ContextTokens == nil || *state.ContextTokens < 0 ||
		(state.MaxContextTokens != nil && *state.MaxContextTokens <= 0) {
		return nil, false
	}
	return &state, true
}

func (state *kimiAgentState) status() kimiStatus {
	return kimiStatus{
		Busy: state.Status == "running", Model: state.Model, ThinkingLevel: state.Thinking,
		Permission: state.Permission, PlanMode: *state.PlanMode, SwarmMode: *state.SwarmMode,
	}
}

func (s *kimiStream) observeSessionState(frame kimiFrame) {
	var agentState *kimiAgentState
	var usageState *kimiUsageState
	switch frame.Type {
	case contracts.KimiEventAgent:
		state, valid := decodeKimiAgentState(frame)
		if !valid || state.AgentID != kimiMainAgentID {
			return
		}
		agentState = state
	case contracts.KimiEventUsage:
		state, valid := decodeKimiUsageState(frame)
		if !valid || state.AgentID != kimiMainAgentID {
			return
		}
		usageState = state
	default:
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, tracked := s.sessions[frame.SessionID]; s.closed || !tracked {
		return
	}
	state := s.states[frame.SessionID]
	if agentState != nil {
		if state.agent != nil && state.agent.Model != agentState.Model {
			state.usage = nil
		}
		state.agent = agentState
	}
	if usageState != nil {
		state.usage = usageState
	}
	s.states[frame.SessionID] = state
	s.signalStateChangeLocked()
}

func (s *kimiStream) signalStateChangeLocked() {
	if s.stateChanged != nil {
		close(s.stateChanged)
	}
	s.stateChanged = make(chan struct{})
}

func (s *kimiStream) sessionStatus(sessionID string) (kimiStatus, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionStatusLocked(sessionID)
}

func (s *kimiStream) tracksSession(sessionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, tracked := s.sessions[sessionID]
	return tracked
}

func (s *kimiStream) sessionStatusLocked(sessionID string) (kimiStatus, bool) {
	state := s.states[sessionID]
	if state.agent == nil {
		return kimiStatus{}, false
	}
	status := state.agent.status()
	if state.usage != nil {
		status.HasContextUsage = true
		status.ContextTokens = *state.usage.ContextTokens
		if state.usage.MaxContextTokens != nil {
			status.MaxContextTokens = *state.usage.MaxContextTokens
		}
	}
	return status, true
}

func (s *kimiStream) waitSessionStatus(ctx context.Context, sessionID string, accept func(kimiStatus) bool) (kimiStatus, error) {
	for {
		if err := ctx.Err(); err != nil {
			return kimiStatus{}, err
		}
		s.mu.Lock()
		status, exists := s.sessionStatusLocked(sessionID)
		changed, closed := s.stateChanged, s.closed
		_, tracked := s.sessions[sessionID]
		s.mu.Unlock()
		if closed {
			return kimiStatus{}, errKimiStreamClosed
		}
		if !tracked {
			return kimiStatus{}, errKimiSessionUnavailable
		}
		if exists && accept(status) {
			return status, nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return kimiStatus{}, ctx.Err()
		}
	}
}
