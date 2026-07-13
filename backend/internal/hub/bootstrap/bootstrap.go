package bootstrap

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/usernames"
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

	// Route through the same personal-org + user pairing every other create path
	// uses (SignUp, OAuth signup, admin user create) rather than re-inlining the
	// transaction: the org name mirrors the username, the conflict wrapping is
	// identical, and CreateUserWithOrg is the one place that pairing lives, so a
	// future change to it (a new invariant, extra cleanup) reaches bootstrap too
	// instead of drifting. usernames.Solo is a routable slug, so the store-level
	// CreateUserParams.Validate accepts it; the empty email makes the helper's
	// ClearCompetingPendingEmails a no-op.
	user, err := service.CreateUserWithOrg(ctx, st, service.CreateUserParams{
		Username:    usernames.Solo,
		DisplayName: "Solo",
		PasswordSet: true,
		IsAdmin:     true,
	})
	if err != nil {
		return err
	}

	slog.Info("bootstrap: created personal org and user",
		"org_id", user.OrgID,
		"user_id", user.ID,
		"username", usernames.Solo,
	)

	return nil
}
