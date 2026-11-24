package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/inputqueue"
)

func (s *agentOutputSink) BindNativeInputTurn(receipt agent.NativeInputReceipt, nativeTurnID string) error {
	if receipt.SessionID == "" || receipt.InputID == "" || nativeTurnID == "" {
		return errors.New("a native turn binding needs a session, input and turn")
	}
	changed, err := s.h.queries.BindNativeInputTurn(bgCtx(), db.BindNativeInputTurnParams{
		AgentID: s.agentID, AgentSessionID: receipt.SessionID, NativeInputID: receipt.InputID, NativeTurnID: nativeTurnID,
	})
	if err := expectNativeReplyWrite(changed, err); err != nil {
		return err
	}
	s.publishNativeInputReplyChanged()
	return nil
}

func (s *agentOutputSink) RetireNativeInputTurn(receipt agent.NativeInputReceipt, outcome leapmuxv1.AgentInputTurnOutcome) error {
	if receipt.SessionID == "" || receipt.InputID == "" {
		return errors.New("retiring native input needs a session and input")
	}
	if outcome != leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED &&
		outcome != leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED &&
		outcome != leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED {
		return errors.New("a native retirement requires an interrupted, failed, or unproved outcome")
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

func (s *agentOutputSink) PublishNativeReplyTurn(turn agent.NativeReplyTurn) error {
	if turn.SessionID == "" || turn.TurnID == "" || len(turn.Inputs) == 0 {
		return errors.New("a canonical reply needs a session, turn, and at least one input")
	}
	if turn.Outcome < leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED ||
		turn.Outcome > leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED {
		return errors.New("a canonical reply needs a native turn outcome")
	}
	if s.h.db == nil {
		return errors.New("publishing a canonical reply requires a Worker database")
	}
	ctx := bgCtx()
	tx, err := s.h.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	queries := db.New(tx)
	changed, err := queries.PublishNativeReplyTurn(ctx, db.PublishNativeReplyTurnParams{
		AgentID: s.agentID, AgentSessionID: turn.SessionID, NativeTurnID: turn.TurnID,
		Outcome: int64(turn.Outcome), ReplyCount: int64(len(turn.Items)),
	})
	if err != nil {
		return err
	}
	fresh := changed == 1
	if !fresh {
		stored, err := queries.GetNativeReplyTurn(ctx, db.GetNativeReplyTurnParams{
			AgentID: s.agentID, AgentSessionID: turn.SessionID, NativeTurnID: turn.TurnID,
		})
		if err != nil {
			return err
		}
		if stored.Outcome != int64(turn.Outcome) || stored.ReplyCount != int64(len(turn.Items)) {
			return errNativeReplyConflict
		}
	}
	var previous int64
	for _, item := range turn.Items {
		if item.Ordinal <= previous || item.MessageKey == "" {
			return errors.New("canonical reply items must have ordered positions and message keys")
		}
		previous = item.Ordinal
		message, err := queries.GetMessageByIdempotencyKey(ctx, db.GetMessageByIdempotencyKeyParams{
			AgentID: s.agentID, AgentSessionID: turn.SessionID, IdempotencyKey: item.MessageKey,
		})
		if err != nil {
			return fmt.Errorf("find the stored native reply item: %w", err)
		}
		if message.Source != leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT {
			return errors.New("a native reply item must identify an agent transcript row")
		}
		if turn.ExclusiveItems {
			changed, err := queries.AssignExclusiveReplyItem(ctx, db.AssignExclusiveReplyItemParams{
				ID: message.ID, AgentID: s.agentID, AgentSessionID: turn.SessionID, NativeTurnID: turn.TurnID,
			})
			if err := expectNativeReplyWrite(changed, err); err != nil {
				return err
			}
		}
		if !fresh {
			stored, err := queries.GetNativeReplyItem(ctx, db.GetNativeReplyItemParams{
				AgentID: s.agentID, AgentSessionID: turn.SessionID, NativeTurnID: turn.TurnID, Ordinal: item.Ordinal,
			})
			if errors.Is(err, sql.ErrNoRows) || err == nil && (!stored.MessageID.Valid || stored.MessageID.String != message.ID) {
				return errNativeReplyConflict
			}
			if err != nil {
				return err
			}
			continue
		}
		changed, err := queries.PublishNativeReplyItem(ctx, db.PublishNativeReplyItemParams{
			AgentID: s.agentID, AgentSessionID: turn.SessionID, NativeTurnID: turn.TurnID,
			Ordinal: item.Ordinal, MessageID: sql.NullString{String: message.ID, Valid: true},
		})
		if err := expectNativeReplyWrite(changed, err); err != nil {
			return err
		}
	}
	for _, input := range turn.Inputs {
		if input.InputID == "" || input.AfterOrdinal <= 0 {
			return errors.New("a canonical reply boundary needs an input and item position")
		}
		changed, err := queries.PublishNativeReplyBoundary(ctx, db.PublishNativeReplyBoundaryParams{
			AgentID: s.agentID, AgentSessionID: turn.SessionID, NativeInputID: input.InputID, NativeTurnID: turn.TurnID,
			ReplyAfterOrdinal: sql.NullInt64{Int64: input.AfterOrdinal, Valid: true},
		})
		if err := expectNativeReplyWrite(changed, err); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.publishNativeInputReplyChanged()
	return nil
}

var errNativeReplyConflict = errors.New("the native reply conflicts with an already retained turn")

func expectNativeReplyWrite(rows int64, err error) error {
	if err != nil {
		return err
	}
	if rows != 1 {
		return errNativeReplyConflict
	}
	return nil
}

func (s *agentOutputSink) SetNativeControlInputs(control agent.NativeControlInputs) error {
	if control.SessionID == "" || control.RequestID == "" || control.InstanceID == "" {
		return errors.New("a native control binding needs a session, request, and instance")
	}
	inputIDs, err := normalizeNativeControlInputIDs(control.InputIDs)
	if err != nil {
		return err
	}
	s.h.controls.setBlockingNativeControlInputs(s.agentID, control.SessionID, control.RequestID, control.InstanceID, inputIDs)
	s.publishNativeInputReplyChanged()
	return nil
}

func normalizeNativeControlInputIDs(inputIDs []string) ([]string, error) {
	if len(inputIDs) == 0 {
		return nil, nil
	}
	normalized := make([]string, 0, len(inputIDs))
	for _, inputID := range inputIDs {
		if inputID == "" {
			return nil, errors.New("a native control input ID is empty")
		}
		if !slices.Contains(normalized, inputID) {
			normalized = append(normalized, inputID)
		}
	}
	return normalized, nil
}

func (svc *Service) inputWaitingForUser(agentID string, state db.GetInputReplyStateRow) bool {
	if state.AgentSessionID == "" || state.AgentSessionID != state.CurrentAgentSessionID {
		return false
	}
	return svc.Output.controls.hasBlockingControlForInput(agentID, state.AgentSessionID, state.NativeTurnID, state.NativeInputID)
}

func (svc *Service) listExactInputReply(ctx context.Context, agentID, inputID string, cursor, limit int64) ([]db.Message, int64, error) {
	userSource, keyPattern := leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, inputqueue.InputRequestKeyPrefix+"*"
	canonical, err := svc.Queries.HasCanonicalInputReply(ctx, db.HasCanonicalInputReplyParams{
		AgentID: agentID, InputID: inputID, UserSource: userSource, InputKeyPattern: keyPattern,
	})
	if err != nil {
		return nil, 0, err
	}
	if canonical {
		rows, err := svc.Queries.ListCanonicalInputReplyMessages(ctx, db.ListCanonicalInputReplyMessagesParams{
			AgentID: agentID, InputID: inputID, UserSource: userSource, InputKeyPattern: keyPattern,
			CursorOrdinal: cursor, PageSize: limit + 1,
		})
		if err != nil {
			return nil, 0, err
		}
		var next int64
		if int64(len(rows)) > limit {
			next, err = svc.Queries.GetCanonicalInputReplyCursor(ctx, db.GetCanonicalInputReplyCursorParams{
				AgentID: agentID, InputID: inputID, MessageID: sql.NullString{String: rows[limit-1].ID, Valid: true},
			})
		}
		return rows, next, err
	}
	rows, err := svc.Queries.ListInputReplyMessages(ctx, db.ListInputReplyMessagesParams{
		AgentID: agentID, InputID: inputID, UserSource: userSource, InputKeyPattern: keyPattern,
		CursorSeq: cursor, PageSize: limit + 1,
	})
	var next int64
	if err == nil && int64(len(rows)) > limit {
		next = rows[limit-1].Seq
	}
	return rows, next, err
}

func (s *agentOutputSink) publishNativeInputReplyChanged() {
	s.h.enqueueInputReplyChanged(s.agentID)
	s.h.watcher.DrainAgentEvents(s.agentID)
}
