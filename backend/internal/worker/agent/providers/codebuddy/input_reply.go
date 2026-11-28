package codebuddy

import (
	"bytes"
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

// codebuddyTurn tracks one active turn's input reply and mid-turn steers.
// Agent.mu protects every field.
type codebuddyTurn struct {
	reply         *inputReply
	inFlightSteer *codebuddyPendingSteer
	pendingSteers []*codebuddyPendingSteer
	ended         bool
	replyRetained bool
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
	turn       *codebuddyTurn
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

func (a *Agent) activeTurnLocked() *codebuddyTurn {
	if !a.active {
		return nil
	}
	if a.turn == nil {
		a.turn = &codebuddyTurn{}
	}
	return a.turn
}

func (a *Agent) turnReplyLocked() *inputReply {
	if a.turn == nil {
		return nil
	}
	return a.turn.reply
}

func (a *Agent) awaitingInputEchoLocked() bool {
	if a.turn == nil {
		return false
	}
	if a.turn.reply != nil && !a.turn.reply.admitted {
		return true
	}
	return len(a.turn.pendingSteers) > 0 || a.turn.inFlightSteer != nil
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
	// Reject non-replay user frames (including multi-MB tool results) before
	// decoding JSON. Inside a tool result string, quotes are escaped as
	// \"isReplay\", so this substring appears only on a replay envelope key.
	if !bytes.Contains(raw, []byte(`"isReplay"`)) {
		return false
	}
	fields, ok := providerkit.RawObject(raw)
	if !ok || !providerkit.RawBool(fields["isReplay"]) {
		return false
	}
	if parentRaw := fields["parent_tool_use_id"]; providerkit.RawPresent(parentRaw) {
		if parentID, ok := providerkit.RawString(parentRaw); !ok || parentID != "" {
			return false
		}
	}
	echoUUID := providerkit.ObjectString(fields, "uuid")
	echoSessionID := providerkit.ObjectString(fields, "session_id")
	echoRequestIDRaw := fields["_requestId"]
	echoRequestID, _ := providerkit.RawString(echoRequestIDRaw)

	a.mu.Lock()
	if !a.awaitingInputEchoLocked() {
		a.mu.Unlock()
		return true
	}
	turn := a.turn
	if reply := turn.reply; reply.matches(echoSessionID, echoRequestIDRaw) && !reply.admitted {
		reply.admitted = true
		a.mu.Unlock()
		return true
	}

	var matched *codebuddyPendingSteer
	var inFlight *codebuddyPendingSteer
	for i, candidate := range turn.pendingSteers {
		if codebuddySteerMatchesEcho(candidate, echoSessionID, echoRequestID) {
			matched = candidate
			turn.pendingSteers = append(turn.pendingSteers[:i], turn.pendingSteers[i+1:]...)
			break
		}
	}
	if matched == nil && turn.inFlightSteer != nil && turn.inFlightSteer.early == nil &&
		codebuddySteerMatchesEcho(turn.inFlightSteer, echoSessionID, echoRequestID) {
		inFlight = turn.inFlightSteer
	}
	if matched == nil && inFlight == nil {
		a.mu.Unlock()
		return true
	}

	var receipt agent.NativeInputReceipt
	reply := turn.reply
	if reply != nil && reply.admitted && reply.matches(echoSessionID, echoRequestIDRaw) {
		nativeInputID := echoUUID
		if nativeInputID == "" {
			if generated, err := newInputTurnID(); err == nil {
				nativeInputID = generated
			}
		}
		if nativeInputID != "" {
			receipt = agent.NativeInputReceipt{
				SessionID: echoSessionID,
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
		a.reportCodebuddySteerError(agent.RetainSteerResolution(a.sink, resolution))
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
	if a.turn != turn || turn.ended || turn.inFlightSteer != inFlight {
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
	turn := steer.turn
	if turn == nil || turn.inFlightSteer != steer {
		a.mu.Unlock()
		return
	}
	turn.inFlightSteer = nil
	early := steer.early
	steer.early = nil
	ended := turn.ended
	replyRetained := turn.replyRetained
	if accepted && !ended && early == nil {
		turn.pendingSteers = append(turn.pendingSteers, steer)
	}
	if accepted && !ended && early != nil && early.receipt.InputID != "" && early.reply != nil && turn.reply == early.reply {
		early.reply.steers = append(early.reply.steers, early.receipt)
	}
	a.mu.Unlock()
	if early != nil {
		err := early.finish(accepted)
		a.reportCodebuddySteerError(err)
		if accepted && ended && !replyRetained && early.receipt.InputID != "" && agent.OutputRetained(err) {
			a.retireSteerReceipt(early.receipt)
		}
		return
	}
	if accepted && ended {
		resolution := steer.resolution
		resolution.Outcome = agent.SteerOutcomeUncertain
		a.reportCodebuddySteerError(agent.RetainSteerResolution(a.sink, resolution))
	}
}

func (a *Agent) cancelInFlightSteer(steer *codebuddyPendingSteer) {
	a.finishInFlightSteer(steer, false)
}

func (a *Agent) resolvePendingSteers(steers []*codebuddyPendingSteer, outcome agent.SteerOutcome) {
	for _, steer := range steers {
		resolution := steer.resolution
		resolution.Outcome = outcome
		a.reportCodebuddySteerError(agent.RetainSteerResolution(a.sink, resolution))
	}
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
		a.retireSteerReceipt(steerReceipt)
	}
}

func (a *Agent) retireSteerReceipt(receipt agent.NativeInputReceipt) {
	if err := a.sink.RetireNativeInputTurn(receipt, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED); !agent.OutputRetained(err) {
		slog.Warn("CodeBuddy could not retire the steer reply", "agent_id", a.AgentID(), "error", err)
	}
}
