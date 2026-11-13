package store

import "context"

// AdminMembershipGuard checks current membership under the target-user lock.
// Administrator removal requires an already-held administrator mutex; the outer
// transaction restarts to acquire it first when needed. Ordinary users do not
// require the administrator mutex.
type AdminMembershipGuard[C any] struct {
	InTransaction func(context.Context, func(C) error) error
	RequireLock   func(C) error
	ReadTarget    func(context.Context, C, string) (UserInfoCacheFields, bool, error)
	HasOtherAdmin func(context.Context, C, string) (bool, error)
}

// Remove runs account deletion or administrator demotion with the same guard.
// A missing or already deleted target retains the mutation's existing behavior.
func (g AdminMembershipGuard[C]) Remove(ctx context.Context, userID string, mutate func(context.Context, C) error) error {
	return g.InTransaction(ctx, func(conn C) error {
		if _, err := g.LockForRemoval(ctx, conn, userID); err != nil {
			return err
		}
		return mutate(ctx, conn)
	})
}

// LockForRemoval locks the current target and reports its administrator role.
// The caller must hold the transaction through cleanup and deletion.
func (g AdminMembershipGuard[C]) LockForRemoval(ctx context.Context, conn C, userID string) (bool, error) {
	current, exists, err := g.ReadTarget(ctx, conn, userID)
	if err != nil {
		return false, err
	}
	if !exists || !current.IsAdmin {
		return false, nil
	}
	if err := g.RequireLock(conn); err != nil {
		return false, err
	}
	other, err := g.HasOtherAdmin(ctx, conn, userID)
	if err != nil {
		return false, err
	}
	if !other {
		return false, ErrLastAdministrator
	}
	return true, nil
}
