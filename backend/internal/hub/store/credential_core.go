package store

import (
	"context"
	"time"
)

// CredentialEvent is the dialect-neutral result of a credential mutation.
// SQL adapters normalize generated row types before the shared transaction
// core emits the matching durable lifecycle event.
type CredentialEvent struct {
	// Kind is the durable revocation-event kind the mutation emits.
	Kind      string
	SubjectID string
	UserID    string
	At        time.Time
	// UserAuthGeneration carries the committed user credential epoch for
	// user-wide (user_tokens) events; it is zero for per-credential events.
	UserAuthGeneration int64
}

// RunCredentialMutation keeps the credential row change and lifecycle event
// in one transaction. A nil event represents a compare-and-swap miss or an
// already-revoked credential and returns zero affected rows.
func RunCredentialMutation[C any](
	ctx context.Context,
	inTransaction func(context.Context, func(C) error) error,
	mutate func(context.Context, C) (*CredentialEvent, error),
	emit func(context.Context, C, CredentialEvent) error,
) (int64, error) {
	var affected int64
	err := inTransaction(ctx, func(conn C) error {
		event, err := mutate(ctx, conn)
		if err != nil || event == nil {
			return err
		}
		if err := emit(ctx, conn, *event); err != nil {
			return err
		}
		affected = 1
		return nil
	})
	return affected, err
}
