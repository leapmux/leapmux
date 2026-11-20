package service

import (
	"errors"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

func (s *agentOutputSink) BindNativeInputTurn(receipt agent.NativeInputReceipt, nativeTurnID string) error {
	if receipt.SessionID == "" || receipt.InputID == "" || nativeTurnID == "" {
		return errors.New("a native turn binding needs a session, input and turn")
	}
	err := s.h.queries.BindNativeInputTurn(bgCtx(), db.BindNativeInputTurnParams{
		AgentID: s.agentID, AgentSessionID: receipt.SessionID, NativeInputID: receipt.InputID, NativeTurnID: nativeTurnID,
	})
	if err == nil {
		s.publishNativeInputReplyChanged()
	}
	return err
}

func (s *agentOutputSink) RetireNativeInputTurn(receipt agent.NativeInputReceipt, outcome leapmuxv1.AgentInputTurnOutcome) error {
	if receipt.SessionID == "" || receipt.InputID == "" {
		return errors.New("retiring native input needs a session and input")
	}
	if outcome != leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED &&
		outcome != leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED {
		return errors.New("a native retirement requires an interrupted or unproved outcome")
	}
	err := s.h.queries.RetireNativeInputTurn(bgCtx(), db.RetireNativeInputTurnParams{
		AgentID: s.agentID, AgentSessionID: receipt.SessionID, NativeInputID: receipt.InputID,
		RetiredOutcome: workerdb.OptionalStorageEnum(outcome),
	})
	if err == nil {
		s.publishNativeInputReplyChanged()
	}
	return err
}

func (s *agentOutputSink) publishNativeInputReplyChanged() {
	s.h.enqueueInputReplyChanged(s.agentID)
	s.h.watcher.DrainAgentEvents(s.agentID)
}
