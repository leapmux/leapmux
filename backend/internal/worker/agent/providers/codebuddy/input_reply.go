package codebuddy

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/google/uuid"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// inputMeta supplies the turn identity in CodeBuddy's headless input protocol.
type inputMeta struct {
	ConversationRequestID string `json:"codebuddy.ai/conversationRequestId"`
}

func newInputTurnID() (string, error) {
	value, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("create the CodeBuddy turn ID: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

// inputReply follows one ordinary input. Agent.mu protects it.
type inputReply struct {
	receipt      agent.NativeInputReceipt
	admitted     bool
	incomplete   bool
	streamedText bool
	tools        map[string]struct{}
	steers       []agent.NativeInputReceipt
}

type codebuddyPendingSteer struct {
	sessionID  string
	turnID     string
	resolution agent.SteerResolution
	early      *codebuddyEarlySteer
}

type codebuddyEarlySteer struct {
	write    func() error
	publish  func() error
	receipt  agent.NativeInputReceipt
	reply    *inputReply
	accepted bool
	ready    atomic.Bool
}

func (e *codebuddyEarlySteer) run(agent.TranscriptSequence) error {
	if !e.ready.Load() {
		return agent.ErrOutputInFlight
	}
	if !e.accepted {
		return nil
	}
	return e.write()
}

func (e *codebuddyEarlySteer) finish(accepted bool) error {
	e.accepted = accepted
	e.ready.Store(true)
	return e.publish()
}

func (r *inputReply) matches(sessionID string, requestID json.RawMessage) bool {
	if r == nil || r.receipt.SessionID != sessionID {
		return false
	}
	id, _ := providerkit.RawString(requestID)
	return r.receipt.TurnID == id
}

func (r *inputReply) owns(sessionID string, requestID json.RawMessage) bool {
	return r.matches(sessionID, requestID) && r.admitted
}

func (r *inputReply) outcome(result resultMessage, interrupted bool) leapmuxv1.AgentInputTurnOutcome {
	if !r.owns(result.SessionID, result.RequestID) || r.incomplete {
		return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED
	}
	switch {
	case interrupted || result.statesAbortedTurn():
		return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED
	case result.failed():
		return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED
	case result.IsError != nil && result.Subtype == "success":
		return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED
	default:
		return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED
	}
}

// consumeInputEcho observes durable user echoes. The first matching echo for
// the active turn admits the lead prompt; subsequent echoes resolve pending
// mid-turn steers in FIFO order at the exact transcript position where
// CodeBuddy injected them.
func (a *Agent) consumeInputEcho(raw []byte) bool {
	var echo struct {
		UUID            string          `json:"uuid"`
		IsReplay        bool            `json:"isReplay"`
		SessionID       string          `json:"session_id"`
		RequestID       json.RawMessage `json:"_requestId"`
		ParentToolUseID string          `json:"parent_tool_use_id"`
	}
	if json.Unmarshal(raw, &echo) != nil || !echo.IsReplay || echo.ParentToolUseID != "" {
		return false
	}
	echoRequestID, _ := providerkit.RawString(echo.RequestID)

	a.mu.Lock()
	if reply := a.inputReply; reply.matches(echo.SessionID, echo.RequestID) && !reply.admitted {
		reply.admitted = true
		a.mu.Unlock()
		return true
	}

	var matched *codebuddyPendingSteer
	var inFlight *codebuddyPendingSteer
	for i, candidate := range a.pendingSteers {
		if codebuddySteerMatchesEcho(candidate, echo.SessionID, echoRequestID) {
			matched = candidate
			a.pendingSteers = append(a.pendingSteers[:i], a.pendingSteers[i+1:]...)
			break
		}
	}
	if matched == nil && a.inFlightSteer != nil && a.inFlightSteer.early == nil &&
		codebuddySteerMatchesEcho(a.inFlightSteer, echo.SessionID, echoRequestID) {
		inFlight = a.inFlightSteer
	}
	if matched == nil && inFlight == nil {
		a.mu.Unlock()
		return true
	}

	var receipt agent.NativeInputReceipt
	reply := a.inputReply
	if reply != nil && reply.admitted && reply.matches(echo.SessionID, echo.RequestID) {
		nativeInputID := echo.UUID
		if nativeInputID == "" {
			if generated, err := newInputTurnID(); err == nil {
				nativeInputID = generated
			}
		}
		if nativeInputID != "" {
			receipt = agent.NativeInputReceipt{
				SessionID: echo.SessionID,
				InputID:   nativeInputID,
				TurnID:    reply.receipt.TurnID,
			}
			if matched != nil {
				reply.steers = append(reply.steers, receipt)
			}
		}
	}
	a.mu.Unlock()

	if matched != nil {
		resolution := matched.resolution
		resolution.Outcome = agent.SteerOutcomeEchoed
		resolution.Receipt = receipt
		a.retainCodebuddySteerResolution(resolution)
		return true
	}

	resolution := inFlight.resolution
	resolution.Outcome = agent.SteerOutcomeEchoed
	resolution.Receipt = receipt
	early := &codebuddyEarlySteer{
		write:   a.sink.CaptureSteerResolution(resolution),
		receipt: receipt,
		reply:   reply,
	}
	publish, err := agent.ReserveTranscriptSequence(a.sink, "record early CodeBuddy steer echo", early.run)
	if err != nil {
		a.reportCodebuddySteerError(err)
		return true
	}
	early.publish = publish
	a.mu.Lock()
	if a.inFlightSteer != inFlight {
		a.mu.Unlock()
		a.reportCodebuddySteerError(early.finish(false))
		return true
	}
	inFlight.early = early
	a.mu.Unlock()
	return true
}

func codebuddySteerMatchesEcho(steer *codebuddyPendingSteer, sessionID, requestID string) bool {
	if steer == nil || steer.sessionID != sessionID {
		return false
	}
	return steer.turnID == "" || steer.turnID == requestID
}

func (a *Agent) finishInFlightSteer(steer *codebuddyPendingSteer, accepted bool) {
	a.mu.Lock()
	if a.inFlightSteer != steer {
		a.mu.Unlock()
		return
	}
	a.inFlightSteer = nil
	early := steer.early
	steer.early = nil
	if accepted && early == nil {
		a.pendingSteers = append(a.pendingSteers, steer)
	}
	if accepted && early != nil && early.receipt.InputID != "" && early.reply != nil && a.inputReply == early.reply {
		early.reply.steers = append(early.reply.steers, early.receipt)
	}
	a.mu.Unlock()
	if early != nil {
		a.reportCodebuddySteerError(early.finish(accepted))
	}
}

func (a *Agent) cancelInFlightSteer(steer *codebuddyPendingSteer) {
	a.finishInFlightSteer(steer, false)
}

func (a *Agent) resolvePendingSteers(steers []*codebuddyPendingSteer, outcome agent.SteerOutcome) {
	for _, steer := range steers {
		resolution := steer.resolution
		resolution.Outcome = outcome
		a.retainCodebuddySteerResolution(resolution)
	}
}

func (a *Agent) retainCodebuddySteerResolution(resolution agent.SteerResolution) {
	write := a.sink.CaptureSteerResolution(resolution)
	publish, err := agent.ReserveTranscriptSequence(a.sink, "record CodeBuddy steer outcome", func(agent.TranscriptSequence) error {
		return write()
	})
	if err == nil {
		err = publish()
	}
	a.reportCodebuddySteerError(err)
}

func (a *Agent) reportCodebuddySteerError(err error) {
	if err != nil && !agent.OutputRetained(err) {
		slog.Warn("CodeBuddy could not record the steer outcome", "agent_id", a.AgentID(), "error", err)
	}
}

func (a *Agent) retireInputReply(reply *inputReply) {
	if reply == nil {
		return
	}
	if err := a.sink.RetireNativeInputTurn(reply.receipt, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED); !agent.OutputRetained(err) {
		slog.Warn("CodeBuddy could not retire the input reply", "agent_id", a.AgentID(), "error", err)
	}
	for _, steerReceipt := range reply.steers {
		if err := a.sink.RetireNativeInputTurn(steerReceipt, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED); !agent.OutputRetained(err) {
			slog.Warn("CodeBuddy could not retire the steer reply", "agent_id", a.AgentID(), "error", err)
		}
	}
}
