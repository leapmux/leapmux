package bootstrap

import (
	"context"
	"fmt"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/usernames"
	"github.com/leapmux/leapmux/internal/util/id"
)

// Run creates the personal org and passwordless solo user when the database
// is empty and soloMode is true. In hub or dev mode this is a no-op — the
// first admin is registered interactively via the /setup page, which invokes
// the SignUp RPC's setup-mode branch.
func Run(ctx context.Context, st store.Store, soloMode bool) error {
	if !soloMode {
		slog.Info("bootstrap: skipped (hub/dev mode uses interactive setup)")
		return nil
	}

	hasUsers, err := st.Users().HasAny(ctx)
	if err != nil {
		return fmt.Errorf("check users: %w", err)
	}
	if hasUsers {
		slog.Info("bootstrap: skipped (already initialized)")
		return nil
	}

	orgID := id.Generate()
	userID := id.Generate()

	if err := st.RunInTransaction(ctx, func(tx store.Store) error {
		if err := tx.Orgs().Create(ctx, store.CreateOrgParams{
			ID:         orgID,
			Name:       usernames.Solo,
			IsPersonal: true,
		}); err != nil {
			return fmt.Errorf("create personal org: %w", store.NewConflictError(err, store.ConflictEntityOrg))
		}

		if err := tx.Users().Create(ctx, store.CreateUserParams{
			ID:          userID,
			OrgID:       orgID,
			Username:    usernames.Solo,
			DisplayName: "Solo",
			Email:       "",
			PasswordSet: true,
			IsAdmin:     true,
		}); err != nil {
			return fmt.Errorf("create user: %w", store.NewConflictError(err, store.ConflictEntityUser))
		}

		if err := tx.OrgMembers().Create(ctx, store.CreateOrgMemberParams{
			OrgID:  orgID,
			UserID: userID,
			Role:   leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_OWNER,
		}); err != nil {
			return fmt.Errorf("create org member: %w", err)
		}

		return nil
	}); err != nil {
		return err
	}

	slog.Info("bootstrap: created personal org and user",
		"org_id", orgID,
		"user_id", userID,
		"username", usernames.Solo,
	)

	return nil
}
