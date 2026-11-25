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

func newLayoutEventHead() (string, []byte, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return "", nil, fmt.Errorf("create layout journal key: %w", err)
	}
	return id.Generate(), key, nil
}

func ensureLayoutEventHead(ctx context.Context, events store.LayoutEventStore, owner userid.UserID) error {
	journalID, key, err := newLayoutEventHead()
	if err != nil {
		return err
	}
	return events.EnsureHead(ctx, owner, journalID, key)
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
	_, err := events.Append(ctx, store.AppendLayoutEventsParams{
		Owner:              owner,
		ObservedUnixMillis: observed,
		Facts:              payloads,
		ClosedWorkspaceID:  closedWorkspace,
		RetainCount:        retainedLayoutEvents,
		NewHead:            newLayoutEventHead,
	})
	return err
}
