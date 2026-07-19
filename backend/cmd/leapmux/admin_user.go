package main

import (
	"context"
	"flag"
	"fmt"
	"strconv"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/usernames"
	"github.com/leapmux/leapmux/internal/util/timefmt"
	"github.com/leapmux/leapmux/util/validate"
)

func runUserList(cmd adminCmdCtx, args []string) error {
	var query *string
	var limit *int64
	var cursor *string
	return withAdminStore(cmd, args, func(fs *flag.FlagSet) {
		query = fs.String("query", "", "search query (matches username, display name, email)")
		limit, cursor = addListFlags(fs)
	}, func(ctx context.Context, _ *config.Config, st store.Store) error {
		if err := validateListLimit(*limit); err != nil {
			return err
		}
		var page store.Page[store.User]
		var err error

		if *query != "" {
			page, err = st.Users().Search(ctx, store.SearchUsersParams{
				Query:      query,
				PageParams: store.PageParams{Cursor: *cursor, Limit: *limit},
			})
		} else {
			page, err = st.Users().ListAll(ctx, store.ListAllUsersParams{
				PageParams: store.PageParams{Cursor: *cursor, Limit: *limit},
			})
		}
		if err != nil {
			return classifyListError("list users", err)
		}

		if len(page.Rows) == 0 {
			fmt.Println("No users found.")
			return nil
		}

		fmt.Printf("%-48s %-20s %-24s %-30s %-8s %-8s\n", "ID", "USERNAME", "DISPLAY_NAME", "EMAIL", "ADMIN", "CREATED")
		for _, u := range page.Rows {
			fmt.Printf("%-48s %-20s %-24s %-30s %-8s %-8s\n",
				u.ID, u.Username, u.DisplayName, u.Email, yesNo(u.IsAdmin), timefmt.Format(u.CreatedAt))
		}

		maybePrintNextCursor(page)
		return nil
	})
}

func runUserGet(cmd adminCmdCtx, args []string) error {
	var userID *string
	var username *string
	return withAdminStore(cmd, args, func(fs *flag.FlagSet) {
		userID = fs.String("id", "", "user ID")
		username = fs.String("username", "", "username")
	}, func(ctx context.Context, _ *config.Config, st store.Store) error {
		user, err := resolveUser(ctx, st, *userID, *username)
		if err != nil {
			return err
		}

		fmt.Printf("ID:              %s\n", user.ID)
		fmt.Printf("Org ID:          %s\n", user.OrgID)
		fmt.Printf("Username:        %s\n", user.Username)
		fmt.Printf("Display name:    %s\n", user.DisplayName)
		fmt.Printf("Email:           %s\n", user.Email)
		fmt.Printf("Email verified:  %s\n", yesNo(user.EmailVerified))
		fmt.Printf("Password set:    %s\n", yesNo(user.PasswordSet))
		fmt.Printf("Admin:           %s\n", yesNo(user.IsAdmin))
		fmt.Printf("Created at:      %s\n", timefmt.Format(user.CreatedAt))
		fmt.Printf("Updated at:      %s\n", timefmt.Format(user.UpdatedAt))
		return nil
	})
}

func runUserCreate(cmd adminCmdCtx, args []string) error {
	var username *string
	var pw *string
	var displayName *string
	var email *string
	var emailVerified *bool
	var admin *bool
	return withAdminStore(cmd, args, func(fs *flag.FlagSet) {
		username = fs.String("username", "", "username (required)")
		pw = fs.String("password", "", "password (prompted if omitted)")
		displayName = fs.String("display-name", "", "display name")
		email = fs.String("email", "", "email address")
		emailVerified = fs.Bool("email-verified", false, "mark email as verified")
		admin = fs.Bool("admin", false, "grant admin privileges")
	}, func(ctx context.Context, _ *config.Config, st store.Store) error {
		if *username == "" {
			return fmt.Errorf("--username is required")
		}

		pwValue, err := requirePassword(*pw, "Password: ")
		if err != nil {
			return err
		}

		slug, err := validate.SanitizeSlug("username", *username)
		if err != nil {
			return err
		}

		if usernames.IsReservedSystem(slug) {
			return fmt.Errorf("%q is a reserved username", slug)
		}

		if err := validate.ValidatePassword(pwValue); err != nil {
			return err
		}

		if *email != "" {
			if err := validate.ValidateEmail(*email); err != nil {
				return err
			}
		}

		dispName, err := validate.SanitizeDisplayName(*displayName, slug)
		if err != nil {
			return fmt.Errorf("display name: %w", err)
		}

		hash, err := password.Hash(pwValue)
		if err != nil {
			return fmt.Errorf("hash password: %w", err)
		}

		user, err := service.CreateUserWithOrg(ctx, st, service.CreateUserParams{
			Username:      slug,
			PasswordHash:  hash,
			DisplayName:   dispName,
			Email:         *email,
			EmailVerified: *emailVerified,
			PasswordSet:   true,
			IsAdmin:       *admin,
		})
		if err != nil {
			return friendlyConstraintError(err, slug, *email)
		}

		fmt.Printf("Created user %q (id: %s)\n", slug, user.ID)
		return nil
	})
}

func runUserUpdate(cmd adminCmdCtx, args []string) error {
	var flagSet *flag.FlagSet
	var userID *string
	var username *string
	var displayName *string
	var email *string
	var clearPendingEmail *bool
	var emailVerifiedFlag *bool
	return withAdminStore(cmd, args, func(fs *flag.FlagSet) {
		flagSet = fs
		userID = fs.String("id", "", "user ID")
		username = fs.String("username", "", "username (for lookup)")
		displayName = fs.String("display-name", "", "new display name")
		email = fs.String("email", "", "new email address")
		clearPendingEmail = fs.Bool("clear-pending-email", false, "clear any in-flight email verification (token + attempt counter)")
		fs.Func("email-verified", "mark email as verified (true/false)", func(s string) error {
			b, err := strconv.ParseBool(s)
			if err != nil {
				return fmt.Errorf("must be 'true' or 'false'")
			}
			emailVerifiedFlag = &b
			return nil
		})
	}, func(ctx context.Context, _ *config.Config, st store.Store) error {
		user, err := resolveUser(ctx, st, *userID, *username)
		if err != nil {
			return err
		}

		setFlags := map[string]bool{}
		flagSet.Visit(func(f *flag.Flag) { setFlags[f.Name] = true })

		updateDisplayName := setFlags["display-name"]
		updateEmail := setFlags["email"]
		updateEmailVerified := emailVerifiedFlag != nil
		updateClearPendingEmail := *clearPendingEmail

		if !updateDisplayName && !updateEmail && !updateEmailVerified && !updateClearPendingEmail {
			return fmt.Errorf("no fields to update (use --display-name, --email, --email-verified, or --clear-pending-email)")
		}

		// Validate inputs before starting the transaction.
		var sanitizedDisplayName string
		if updateDisplayName {
			sanitizedDisplayName, err = validate.SanitizeDisplayName(*displayName, user.Username)
			if err != nil {
				return fmt.Errorf("display name: %w", err)
			}
		}

		if updateEmail && *email != "" {
			if err := validate.ValidateEmail(*email); err != nil {
				return err
			}
		}

		return st.RunInTransaction(ctx, func(tx store.Store) error {
			if updateDisplayName {
				if err := tx.Users().UpdateProfile(ctx, store.UpdateUserProfileParams{
					Username:    user.Username,
					DisplayName: sanitizedDisplayName,
					ID:          user.ID,
				}); err != nil {
					return fmt.Errorf("update display name: %w", err)
				}
			}

			if updateEmail {
				verified := user.EmailVerified
				if emailVerifiedFlag != nil {
					verified = *emailVerifiedFlag
				}
				if err := service.SetEmailAndClearCompeting(ctx, tx, user.ID, *email, verified); err != nil {
					return friendlyConstraintError(err, user.Username, *email)
				}
			} else if updateEmailVerified {
				if err := tx.Users().UpdateEmailVerified(ctx, store.UpdateUserEmailVerifiedParams{
					EmailVerified: *emailVerifiedFlag,
					ID:            user.ID,
				}); err != nil {
					return fmt.Errorf("update email verified: %w", err)
				}
			}

			if updateClearPendingEmail {
				if err := tx.Users().ClearPendingEmail(ctx, user.ID); err != nil {
					return fmt.Errorf("clear pending email: %w", err)
				}
			}

			fmt.Printf("Updated user %q (id: %s)\n", user.Username, user.ID)
			return nil
		})
	})
}

func runUserDelete(cmd adminCmdCtx, args []string) error {
	var userID *string
	var username *string
	var force *bool
	return withAdminStore(cmd, args, func(fs *flag.FlagSet) {
		userID = fs.String("id", "", "user ID")
		username = fs.String("username", "", "username")
		force = fs.Bool("force", false, "required to delete an admin user")
	}, func(ctx context.Context, _ *config.Config, st store.Store) error {
		user, err := resolveUser(ctx, st, *userID, *username)
		if err != nil {
			return err
		}

		if user.IsAdmin && !*force {
			return fmt.Errorf("user %q is an admin; pass --force to confirm deletion", user.Username)
		}

		err = st.RunInUserAuthTransaction(ctx, user.ID, func(tx store.Store) error {
			if err := tx.Workers().MarkAllDeletedByUser(ctx, user.ID); err != nil {
				return fmt.Errorf("mark workers deleted: %w", err)
			}
			if err := tx.Workspaces().SoftDeleteAllByUser(ctx, user.ID); err != nil {
				return fmt.Errorf("soft-delete workspaces: %w", err)
			}
			if err := tx.Sessions().DeleteByUser(ctx, user.ID); err != nil {
				return fmt.Errorf("delete sessions: %w", err)
			}
			// User deletion implies every credential the user had —
			// CLI api tokens, agent delegation tokens, browser
			// sessions — must die. The store records durable
			// revocation events in this transaction, so the hub's
			// in-memory bearer cache and any open channels (cookie
			// or bearer) are torn down on the watcher's next sweep —
			// no IPC from this admin CLI required.
			if _, _, err := auth.RevokeAllUserCredentials(ctx, tx, user.ID); err != nil {
				return err
			}
			// Users().Delete soft-deletes the personal org too, so the org name is
			// freed for a future re-signup without a separate, easy-to-forget call.
			if err := tx.Users().Delete(ctx, user.ID); err != nil {
				return fmt.Errorf("delete user: %w", err)
			}
			return nil
		})
		if err != nil {
			return err
		}

		fmt.Printf("Deleted user %q (id: %s) and personal org %s\n", user.Username, user.ID, user.OrgID)
		return nil
	})
}

func runUserResetPassword(cmd adminCmdCtx, args []string) error {
	var userID *string
	var username *string
	var pw *string
	return withAdminStore(cmd, args, func(fs *flag.FlagSet) {
		userID = fs.String("id", "", "user ID")
		username = fs.String("username", "", "username")
		pw = fs.String("password", "", "new password (prompted if omitted)")
	}, func(ctx context.Context, _ *config.Config, st store.Store) error {
		user, err := resolveUser(ctx, st, *userID, *username)
		if err != nil {
			return err
		}

		pwValue, err := requirePassword(*pw, "New password: ")
		if err != nil {
			return err
		}

		if err := validate.ValidatePassword(pwValue); err != nil {
			return err
		}

		hash, err := password.Hash(pwValue)
		if err != nil {
			return fmt.Errorf("hash password: %w", err)
		}

		err = st.RunInUserAuthTransaction(ctx, user.ID, func(tx store.Store) error {
			if err := tx.Users().UpdatePassword(ctx, store.UpdateUserPasswordParams{
				PasswordHash: hash,
				ID:           user.ID,
			}); err != nil {
				return fmt.Errorf("update password: %w", err)
			}

			if err := tx.Sessions().DeleteByUser(ctx, user.ID); err != nil {
				return fmt.Errorf("delete sessions: %w", err)
			}

			// Admin password reset rotates the user's auth basis
			// globally; every credential predating the rotation
			// (api tokens, delegation tokens, sessions, channels)
			// must die. The store records durable revocation events
			// in this transaction so the hub's revocation watcher
			// picks this up cross-process and fires
			// CloseChannelsByUserRevocation without an IPC.
			if _, _, err := auth.RevokeAllUserCredentials(ctx, tx, user.ID); err != nil {
				return err
			}

			return nil
		})
		if err != nil {
			return err
		}

		fmt.Printf("Password reset for user %q (id: %s). All sessions revoked.\n", user.Username, user.ID)
		return nil
	})
}

func runUserGrantAdmin(cmd adminCmdCtx, args []string) error {
	return runUserSetAdmin(cmd, args, true)
}

func runUserRevokeAdmin(cmd adminCmdCtx, args []string) error {
	return runUserSetAdmin(cmd, args, false)
}

func runUserSetAdmin(cmd adminCmdCtx, args []string, admin bool) error {
	var userID *string
	var username *string
	return withAdminStore(cmd, args, func(fs *flag.FlagSet) {
		userID = fs.String("id", "", "user ID")
		username = fs.String("username", "", "username")
	}, func(ctx context.Context, _ *config.Config, st store.Store) error {
		user, err := resolveUser(ctx, st, *userID, *username)
		if err != nil {
			return err
		}

		if err := st.Users().UpdateAdmin(ctx, store.UpdateUserAdminParams{
			IsAdmin: admin,
			ID:      user.ID,
		}); err != nil {
			return fmt.Errorf("update admin: %w", err)
		}

		action := "Granted"
		if !admin {
			action = "Revoked"
		}
		fmt.Printf("%s admin privileges for user %q (id: %s)\n", action, user.Username, user.ID)
		return nil
	})
}

func runUserListSessions(cmd adminCmdCtx, args []string) error {
	var userID *string
	var username *string
	var limit *int64
	var cursor *string
	return withAdminStore(cmd, args, func(fs *flag.FlagSet) {
		userID = fs.String("id", "", "user ID")
		username = fs.String("username", "", "username")
		limit, cursor = addListFlags(fs)
	}, func(ctx context.Context, _ *config.Config, st store.Store) error {
		if err := validateListLimit(*limit); err != nil {
			return err
		}
		user, err := resolveUser(ctx, st, *userID, *username)
		if err != nil {
			return err
		}

		page, err := st.Sessions().ListByUserID(ctx, store.ListUserSessionsParams{
			UserID:     user.ID,
			PageParams: store.PageParams{Cursor: *cursor, Limit: *limit},
		})
		if err != nil {
			return classifyListError("list sessions", err)
		}

		if len(page.Rows) == 0 {
			fmt.Printf("No active sessions for user %q.\n", user.Username)
			return nil
		}

		fmt.Printf("%-48s %-24s %-24s %-24s %-16s %s\n", "ID", "CREATED", "LAST_ACTIVE", "EXPIRES", "IP", "USER_AGENT")
		for _, s := range page.Rows {
			fmt.Printf("%-48s %-24s %-24s %-24s %-16s %s\n",
				s.ID, timefmt.Format(s.CreatedAt), timefmt.Format(s.LastActiveAt),
				timefmt.Format(s.ExpiresAt), s.IPAddress, truncate(s.UserAgent, 60))
		}

		maybePrintNextCursor(page)
		return nil
	})
}
