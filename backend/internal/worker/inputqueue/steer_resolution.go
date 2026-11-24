package inputqueue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

type resolvedSteer struct {
	accepted AcceptedTranscript
	matched  bool
	inFlight bool
	snapshot Snapshot
}

// resolveSteer matches a native report against the reservation that sent it.
// Reports for a removed input or a previous attempt have no effect.
func (s *Store) resolveSteer(ctx context.Context, agentID string, report agent.SteerResolution, spanLines, inFlightID string) (resolvedSteer, error) {
	if !validateIdentity(report.InputID) || report.AttemptID == "" {
		return resolvedSteer{}, fmt.Errorf("the steer report requires an input and dispatch identity")
	}
	switch report.Outcome {
	case agent.SteerOutcomeEchoed, agent.SteerOutcomeDropped, agent.SteerOutcomeUncertain:
	default:
		return resolvedSteer{}, fmt.Errorf("invalid identified steer outcome %d", report.Outcome)
	}
	if report.Receipt != (agent.NativeInputReceipt{}) &&
		(report.Outcome != agent.SteerOutcomeEchoed || report.Receipt.InputID == "" || report.Receipt.SessionID == "") {
		return resolvedSteer{}, errors.New("a steer receipt requires an echo, session, and native input ID")
	}
	q, err := s.beginTx(ctx, agentID)
	if err != nil {
		return resolvedSteer{}, err
	}
	defer q.rollback()
	if q.staged.reservedSeqOf(report.InputID) != 0 {
		return resolvedSteer{}, nil
	}
	var state leapmuxv1.AgentInputState
	err = q.tx.QueryRowContext(ctx, `SELECT state FROM agent_input_queue_items
		WHERE agent_id = ? AND id = ? AND steer_attempt_id = ? AND removed_at IS NULL`,
		agentID, report.InputID, report.AttemptID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return resolvedSteer{}, nil
	}
	if err != nil {
		return resolvedSteer{}, err
	}
	inFlight := state == leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING && inFlightID == report.InputID
	if !inFlight && state != leapmuxv1.AgentInputState_AGENT_INPUT_STATE_AWAITING_ECHO {
		return resolvedSteer{}, nil
	}
	if err := q.ensureState(ctx); err != nil {
		return resolvedSteer{}, err
	}
	result := resolvedSteer{matched: true, inFlight: inFlight}
	if report.Outcome == agent.SteerOutcomeEchoed {
		result.accepted, err = acceptEchoedSteer(ctx, q.tx, agentID, report.InputID, DispatchResult{
			SpanLines: spanLines, NativeReceipt: report.Receipt,
		}, &q.staged)
		if err != nil {
			return resolvedSteer{}, err
		}
		if err := compactAfterSteerEcho(ctx, q.tx, agentID); err != nil {
			return resolvedSteer{}, err
		}
	} else if report.Outcome == agent.SteerOutcomeUncertain {
		if err := markSteerIDsUncertain(ctx, q.tx, agentID, []string{report.InputID}, &q.staged); err != nil {
			return resolvedSteer{}, err
		}
	} else if !inFlight {
		// finishSteer handles a removal that precedes the dispatch reply.
		if err := requeueSteerIDs(ctx, q.tx, agentID, []string{report.InputID}, &q.staged); err != nil {
			return resolvedSteer{}, err
		}
		if err := q.bumpRevision(ctx); err != nil {
			return resolvedSteer{}, err
		}
	}
	result.snapshot, err = q.commitSnapshot(ctx)
	return result, err
}

// ResolveSteer applies the reported outcome, even if the provider reports it
// before returning from the dispatch call.
func (m *Manager) ResolveSteer(ctx context.Context, agentID string, report agent.SteerResolution, spanLines string) (bool, error) {
	if !m.beginActivity() {
		return false, ErrManagerStopped
	}
	defer m.endActivity()
	c := m.coordinator(agentID)
	c.mu.Lock()
	if c.retired {
		m.unlockAndPublish(c)
		return false, nil
	}
	inFlightID := ""
	if c.echoSteer != nil && c.echoSteer.outcome == agent.SteerOutcomePending {
		inFlightID = c.echoSteer.inputID
	}
	result, err := m.store.resolveSteer(ctx, agentID, report, spanLines, inFlightID)
	if err == nil && result.matched {
		if result.inFlight {
			c.echoSteer.outcome = report.Outcome
		}
		if result.accepted.ID != "" {
			result.accepted.Content = slices.Clone(result.accepted.Content)
			m.enqueuePublicationLocked(c, queuePublication{
				snapshot: cloneQueueSnapshot(result.snapshot), accepted: []AcceptedTranscript{result.accepted},
			})
		} else if !result.inFlight || report.Outcome == agent.SteerOutcomeUncertain {
			m.enqueueSnapshotLocked(c, result.snapshot)
		}
	}
	m.unlockAndPublish(c)
	if err == nil && result.matched {
		m.drainIfReleased(agentID, result.snapshot)
	}
	return err == nil && result.matched, err
}
