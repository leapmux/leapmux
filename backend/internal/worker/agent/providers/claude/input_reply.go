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
	a.Mu.Unlock()
	a.retireUnprovedClaudeReply(reply)
}

func (a *Agent) retireUnprovedClaudeReply(reply *claudeInputReply) {
	if reply == nil {
		return
	}
	writer := agent.DirectProviderServices(a.sink)
	if err := writer.RetireNativeInputTurn(reply.receipt, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED); err != nil {
		slog.Warn("retain the unproved Claude reply", "error", err)
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
