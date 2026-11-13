package store

import "context"

// AdminMembershipGuard serializes reductions of administrator membership before
// locking a target user. Dialects supply current locking reads, so an older
// transaction snapshot cannot authorize removal of the final administrator.
type AdminMembershipGuard[C any] struct {
	InTransaction func(context.Context, func(C) error) error
	Lock          func(context.Context, C) error
	ReadTarget    func(context.Context, C, string) (UserInfoCacheFields, bool, error)
	HasOtherAdmin func(context.Context, C, string) (bool, error)
}

// Remove runs account deletion or administrator demotion with the same guard.
// A missing or already deleted target retains the mutation's existing behavior.
func (g AdminMembershipGuard[C]) Remove(ctx context.Context, userID string, mutate func(context.Context, C) error) error {
	return g.InTransaction(ctx, func(conn C) error {
		if err := g.Lock(ctx, conn); err != nil {
			return err
		}
		current, exists, err := g.ReadTarget(ctx, conn, userID)
		if err != nil {
			return err
		}
		if exists && current.IsAdmin {
			other, err := g.HasOtherAdmin(ctx, conn, userID)
			if err != nil {
				return err
			}
			if !other {
				return ErrLastAdministrator
			}
		}
		return mutate(ctx, conn)
	})
}
