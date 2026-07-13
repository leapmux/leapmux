package store

import (
	"context"
)

// DeleteUserWithPersonalOrg soft-deletes a user's personal org and deletes the
// user in ONE transaction, keeping the pairing mechanical across every SQL
// dialect rather than restating it (and its rationale) in each dialect's
// userStore.Delete.
//
// The atomicity is the whole point: a user delete can never leave the org name
// occupying the partial unique index and break a later re-signup of the freed
// username, and -- because the two run in one transaction -- a failure or
// context cancellation between them can never leave a live user pointing at a
// soft-deleted org (whose /o/ slug, OrgCRDT scope, and org-scoped RPCs would all
// resolve NotFound). Org first, then the user, though the order is immaterial:
// the org query resolves org_id from the user row without a deleted_at guard.
//
// The dialect passes its withTransaction and the two mapErr-wrapped queries as
// closures, mirroring RunCredentialMutation, so the sequencing invariant has one
// home instead of three.
func DeleteUserWithPersonalOrg[C any](
	ctx context.Context,
	inTransaction func(context.Context, func(C) error) error,
	softDeletePersonalOrg func(context.Context, C) error,
	deleteUser func(context.Context, C) error,
) error {
	return inTransaction(ctx, func(conn C) error {
		if err := softDeletePersonalOrg(ctx, conn); err != nil {
			return err
		}
		return deleteUser(ctx, conn)
	})
}
