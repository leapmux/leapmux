package claude

import (
	"bytes"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// claudeInputReply owns one ordinary stdin input until the root result arrives.
// Claude identifies its acknowledged user input by UUID. The Worker uses that
// UUID as the turn key for the following root frames.
type claudeInputReply struct {
	receipt      agent.NativeInputReceipt
	acknowledged bool
	steers       []agent.NativeInputReceipt
}

type claudePendingSteer struct {
	resolution   agent.SteerResolution
	written      bool
	acknowledged bool
}

// acknowledgeClaudeSteer runs on a native replay echo. A result read before
// this echo already detached the earlier turn. Otherwise the steer shares
// the acknowledged lead's result and its own user row marks the reply start.
func (a *Agent) acknowledgeClaudeSteer(sessionID string, env *messageEnvelope) bool {
	if sessionID == "" || env.UUID == "" || env.Message.Role != "user" || env.Origin.Kind != "" {
		return false
	}
	a.Mu.Lock()
	steer := a.pendingSteers[env.UUID]
	if !a.liveClaudeOutputLocked() || steer == nil || steer.resolution.Receipt.SessionID != sessionID {
		a.Mu.Unlock()
		return false
	}
	delete(a.pendingSteers, env.UUID)
	steer.acknowledged = true
	resolution := steer.resolution
	resolution.Outcome = agent.SteerOutcomeEchoed
	resolution.Receipt.TurnID = env.UUID
	var superseded *claudeInputReply
	if reply := a.inputReply; reply != nil && reply.acknowledged && reply.receipt.SessionID == sessionID {
		resolution.Receipt.TurnID = reply.receipt.InputID
		reply.steers = append(reply.steers, resolution.Receipt)
	} else {
		superseded = a.inputReply
		a.inputReply = &claudeInputReply{receipt: resolution.Receipt, acknowledged: true}
	}
	steer.resolution = resolution
	a.Mu.Unlock()
	a.retireUnprovedClaudeReply(superseded)

	const label = "record Claude steer echo"
	route, err := a.reserveRoutedClaudeRootOutput(label, func(func()) claudeRootSequence {
		record := a.rootTranscript().sink.CaptureSteerResolution(resolution)
		return func(agent.TranscriptSequence) error { return record() }
	})
	if err != nil {
		a.reportClaudeSequenceError(label, err)
		a.reportUnconfirmedClaudeSteer(steer)
		return true
	}
	route()
	return true
}

// observedClaudeSteersLocked captures writes that preceded the native idle
// event. A later input cannot belong to that idle even if output is delayed.
func (a *Agent) observedClaudeSteersLocked() []*claudePendingSteer {
	var observed []*claudePendingSteer
	for _, steer := range a.pendingSteers {
		if steer.written {
			observed = append(observed, steer)
		}
	}
	return observed
}

func (a *Agent) takeObservedClaudeSteersLocked(observed []*claudePendingSteer) []*claudePendingSteer {
	var pending []*claudePendingSteer
	for _, steer := range observed {
		id := steer.resolution.Receipt.InputID
		if a.pendingSteers[id] == steer {
			delete(a.pendingSteers, id)
			pending = append(pending, steer)
		}
	}
	return pending
}

func (steer *claudePendingSteer) unconfirmedResolution() agent.SteerResolution {
	resolution := steer.resolution
	resolution.Outcome = agent.SteerOutcomeUncertain
	resolution.Receipt = agent.NativeInputReceipt{}
	return resolution
}

func (a *Agent) reportUnconfirmedClaudeSteer(steer *claudePendingSteer) {
	if err := agent.RetainSteerResolution(a.sink, steer.unconfirmedResolution()); !agent.OutputRetained(err) {
		slog.Warn("retain the unconfirmed Claude steer", "error", err)
	}
}

func (a *Agent) acknowledgeClaudeInputReply(sessionID string, env *messageEnvelope) bool {
	if sessionID == "" || env.UUID == "" || env.Message.Role != "user" || env.Origin.Kind == "peer" {
		return false
	}
	a.Mu.Lock()
	reply := a.inputReply
	if !a.liveClaudeOutputLocked() || reply == nil || reply.receipt.SessionID != sessionID || reply.receipt.InputID != env.UUID {
		a.Mu.Unlock()
		return false
	}
	first := !reply.acknowledged
	reply.acknowledged = true
	a.Mu.Unlock()
	if first {
		writer := agent.DirectProviderServices(a.sink)
		if err := writer.BindNativeInputTurn(reply.receipt, env.UUID); err != nil {
			slog.Warn("retain the Claude input binding", "error", err)
			a.retireUnprovedClaudeReply(reply)
		}
	}
	return true
}

// claudeReplayedPrompt discards stdin echoes that the Worker already stores.
// Keep the native user envelopes carrying tool results and handback records.
func claudeReplayedPrompt(env *messageEnvelope) bool {
	if env.Message.Role != "user" || env.Origin.Kind != "" {
		return false
	}
	if raw := bytes.TrimSpace(env.Message.RawContent); len(raw) > 0 && raw[0] == '"' {
		return true
	}
	blocks := env.ContentBlocks()
	if len(blocks) == 0 {
		return false
	}
	for _, block := range blocks {
		switch block.Type {
		case "text", "image", "document":
		default:
			return false
		}
	}
	return true
}

func (a *Agent) currentClaudeReplyTurn(sessionID string) agent.NativeInputReceipt {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return a.claudeReplyTurnLocked(sessionID)
}

func (a *Agent) claudeReplyTurnLocked(sessionID string) agent.NativeInputReceipt {
	reply := a.inputReply
	if reply != nil && reply.acknowledged && reply.receipt.SessionID == sessionID {
		return reply.receipt
	}
	return agent.NativeInputReceipt{}
}

func (a *Agent) retireClaudeInputReply() {
	a.Mu.Lock()
	reply := a.inputReply
	a.inputReply = nil
	pending := a.pendingSteers
	a.pendingSteers = nil
	a.Mu.Unlock()
	a.retireUnprovedClaudeReply(reply)
	for _, steer := range pending {
		a.reportUnconfirmedClaudeSteer(steer)
	}
}

func (a *Agent) retireUnprovedClaudeReply(reply *claudeInputReply) {
	if reply == nil {
		return
	}
	writer := agent.DirectProviderServices(a.sink)
	if err := writer.RetireNativeInputTurn(reply.receipt, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED); err != nil {
		slog.Warn("retain the unproved Claude reply", "error", err)
	}
	for _, receipt := range reply.steers {
		if err := writer.RetireNativeInputTurn(receipt, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED); err != nil {
			slog.Warn("retain the unproved Claude steer reply", "error", err)
		}
	}
}

func claudeReplyOutcome(env *messageEnvelope, subtype string, interruptRequested bool) leapmuxv1.AgentInputTurnOutcome {
	if (env.TerminalReason != "" || interruptRequested) && env.statesAbortedTurn() {
		return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED
	}
	if env.IsError {
		return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED
	}
	if subtype == "success" {
		return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED
	}
	return leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED
}
