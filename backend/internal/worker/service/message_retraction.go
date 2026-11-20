package service

import (
	"errors"
	"fmt"
	"slices"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// RetractMessagesByIdempotencyKey deletes the exact stored rows of the given
// native keys and records one eviction receipt per key, so a replayed frame
// never reinserts a retracted message at a fresh sequence. It takes the
// immutable publication captured at native frame admission and takes no new
// transcript authority: a revoked capture (a replaced process or child lease)
// consumes its claim without deleting a current row.
//
// The scoped receipt-and-delete runs in one store transaction under the owning
// root's transcript mutation lock. One sorted AgentMessagesRetracted event goes
// out after the commit, and the drain runs after the lock releases. Keys that
// name no stored row still record their receipt, which is what suppresses a
// late insertion for an admitted held frame.
func (s *agentOutputSink) RetractMessagesByIdempotencyKey(captured agent.MessageContent, keys []string) error {
	claims := retractionClaimKeys(keys)
	if len(claims) == 0 {
		return nil
	}
	owner, err := s.capturedOwner(captured)
	if err != nil {
		return err
	}
	if owner == nil {
		return errors.New("the retraction claim has no captured transcript owner")
	}
	sessionID := captured.AgentSessionID
	mutation := s.h.transcriptMutationMutex(s.rootAgentID)
	mutation.Lock()
	var seqs []int64
	if !s.retractionAuthorityLocked(owner) {
		// The claim's native lifetime was replaced. Consume it without deletion.
		mutation.Unlock()
		return nil
	}
	seqs, err = s.retractKeysUnderLock(sessionID, claims)
	if err == nil && len(seqs) > 0 {
		s.h.watcher.EnqueueAgentEvent(s.agentID, &leapmuxv1.AgentEvent{
			AgentId: s.agentID,
			Event: &leapmuxv1.AgentEvent_MessagesRetracted{
				MessagesRetracted: &leapmuxv1.AgentMessagesRetracted{Seqs: seqs},
			},
		})
	}
	mutation.Unlock()
	if err != nil {
		return err
	}
	if len(seqs) > 0 {
		s.h.watcher.DrainAgentEvents(s.agentID)
	}
	return nil
}

// IsMessageRetracted reports whether the captured scope already retains an
// eviction receipt for one key. It reads the durable receipt through the
// supplied captured owner and takes no new transcript authority. An empty key
// is never retracted. A storage failure is reported for retry.
func (s *agentOutputSink) IsMessageRetracted(captured agent.MessageContent, key string) (bool, error) {
	if key == "" {
		return false, nil
	}
	sessionID, err := s.retractionSessionScope(captured)
	if err != nil {
		return false, err
	}
	present, err := s.h.queries.HasMessageRetraction(bgCtx(), db.HasMessageRetractionParams{
		AgentID: s.agentID, AgentSessionID: sessionID, IdempotencyKey: key,
	})
	if err != nil {
		return false, fmt.Errorf("read message retraction: %w", err)
	}
	return present, nil
}

// retractionSessionScope validates the captured destination identity and
// returns the stored session fact the claim or lookup is scoped to.
func (s *agentOutputSink) retractionSessionScope(captured agent.MessageContent) (string, error) {
	owner, err := s.capturedOwner(captured)
	if err != nil {
		return "", err
	}
	if owner == nil {
		return "", errors.New("the retraction claim has no captured transcript owner")
	}
	return captured.AgentSessionID, nil
}

// retractionAuthorityLocked reports whether the claim's captured lifetime still
// owns this sink. The caller holds the root mutation lock, which excludes a
// concurrent source replacement. The checks read the captured physical process,
// the root publisher's native session fact, the destination stored session fact
// and the exact child lease -- independently of the normal turn activity scope,
// because an eviction arrives with no turn to settle.
func (s *agentOutputSink) retractionAuthorityLocked(owner *transcriptOwner) bool {
	if owner.sink != s || owner.session == nil || owner.publisherSession == nil {
		return false
	}
	if !s.registeredCurrent() {
		return false
	}
	if s.currentMessageSessionFact() != owner.session {
		return false
	}
	if s.turnPublisherSink().currentMessageSessionFact() != owner.publisherSession {
		return false
	}
	if s.childLifetime != owner.childLease || owner.sessionID != owner.session.id {
		return false
	}
	return true
}

// retractKeysUnderLock records one receipt per key and deletes the exact
// stored rows in one transaction, returning their positive sequences sorted.
// Deleting rows moves no sequence high-water, so the next message keeps a fresh
// sequence above every prior one.
func (s *agentOutputSink) retractKeysUnderLock(sessionID string, keys []string) ([]int64, error) {
	ctx := bgCtx()
	var seqs []int64
	collect := func(queries *db.Queries) error {
		for _, key := range keys {
			if _, err := queries.RecordMessageRetraction(ctx, db.RecordMessageRetractionParams{
				AgentID: s.agentID, AgentSessionID: sessionID, IdempotencyKey: key,
			}); err != nil {
				return fmt.Errorf("record message retraction: %w", err)
			}
			deleted, err := queries.DeleteMessageByAgentSessionAndKey(ctx, db.DeleteMessageByAgentSessionAndKeyParams{
				AgentID: s.agentID, AgentSessionID: sessionID, IdempotencyKey: key,
			})
			if err != nil {
				return fmt.Errorf("delete retracted message: %w", err)
			}
			seqs = append(seqs, deleted...)
		}
		return nil
	}
	if s.h.db == nil {
		if err := collect(s.h.queries); err != nil {
			return nil, err
		}
	} else {
		tx, err := s.h.db.BeginTx(ctx, nil)
		if err != nil {
			return nil, fmt.Errorf("start message retraction transaction: %w", err)
		}
		defer func() { _ = tx.Rollback() }()
		if err := collect(s.h.queries.WithTx(tx)); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit message retraction: %w", err)
		}
	}
	slices.Sort(seqs)
	return seqs, nil
}

// retractionClaimKeys drops empty keys and repeats, preserving order.
func retractionClaimKeys(keys []string) []string {
	claims := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if key == "" {
			continue
		}
		if _, repeat := seen[key]; repeat {
			continue
		}
		seen[key] = struct{}{}
		claims = append(claims, key)
	}
	return claims
}
