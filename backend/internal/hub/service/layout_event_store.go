package service

import (
	"context"
	"crypto/rand"
	"fmt"

	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
)

const retainedLayoutEvents = 32_768

func ensureLayoutEventHead(ctx context.Context, events store.LayoutEventStore, owner userid.UserID) error {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return fmt.Errorf("create layout journal key: %w", err)
	}
	return events.EnsureHead(ctx, owner, id.Generate(), key)
}

func marshalLayoutFacts(facts []*leapmuxv1.LayoutEventFact) ([][]byte, error) {
	payloads := make([][]byte, len(facts))
	for i, fact := range facts {
		payload, err := proto.Marshal(fact)
		if err != nil {
			return nil, fmt.Errorf("encode a layout event: %w", err)
		}
		payloads[i] = payload
	}
	return payloads, nil
}

// appendLayoutEvents writes through the caller's canonical transaction.
func appendLayoutEvents(ctx context.Context, events store.LayoutEventStore, owner userid.UserID, observed int64, payloads [][]byte, closedWorkspace string) error {
	if len(payloads) == 0 {
		return nil
	}
	if err := ensureLayoutEventHead(ctx, events, owner); err != nil {
		return err
	}
	count := int64(len(payloads))
	head, err := events.ReservePositions(ctx, owner, count)
	if err != nil {
		return err
	}
	after := head.HighSequence - count
	for i, payload := range payloads {
		if err := events.Insert(ctx, owner, store.LayoutEventRow{
			Sequence: after + int64(i) + 1, ObservedUnixMillis: observed, Fact: payload,
		}); err != nil {
			return err
		}
	}
	if closedWorkspace != "" {
		if err := events.MarkWorkspaceClosed(ctx, owner, closedWorkspace, head.HighSequence); err != nil {
			return err
		}
	}
	if through := head.HighSequence - retainedLayoutEvents; through > head.FloorSequence {
		return events.PruneThrough(ctx, owner, through)
	}
	return nil
}
