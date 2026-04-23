package main

import (
	"context"
	"flag"
	"fmt"
	"strconv"
	"time"

	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/usernames"
	"github.com/leapmux/leapmux/internal/util/timefmt"
	"github.com/leapmux/leapmux/internal/util/validate"
)

func runAdminUser(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: leapmux admin user <command> [flags]\n\nCommands:\n  list              List users\n  get               Get user details\n  create            Create a new user\n  update            Update user fields\n  delete            Delete a user\n  reset-password    Reset a user's password\n  grant-admin       Grant admin privileges\n  revoke-admin      Revoke admin privileges\n  list-sessions     List a user's active sessions")
	}

	switch args[0] {
	case "list":
		return runUserList(args[1:])
	case "get":
		return runUserGet(args[1:])
	case "create":
		return runUserCreate(args[1:])
	case "update":
		return runUserUpdate(args[1:])
	case "delete":
		return runUserDelete(args[1:])
	case "reset-password":
		return runUserResetPassword(args[1:])
	case "grant-admin":
		return runUserGrantAdmin(args[1:])
	case "revoke-admin":
		return runUserRevokeAdmin(args[1:])
	case "list-sessions":
		return runUserListSessions(args[1:])
	default:
		return fmt.Errorf("unknown user command: %s", args[0])
	}
}

func runUserList(args []string) error {
	var query *string
	var limit *int64
	var cursor *string
	return withAdminStore("user list", args, func(fs *flag.FlagSet) {
		query = fs.String("query", "", "search query (matches username, display name, email)")
		limit = fs.Int64("limit", 50, "maximum number of results")
		cursor = fs.String("cursor", "", "cursor for pagination (created_at in RFC3339Nano)")
	}, func(ctx context.Context, _ *config.Config, st store.Store) error {
		var users []store.User
		var err error

		if *query != "" {
			users, err = st.Users().Search(ctx, store.SearchUsersParams{
				Query:  query,
				Limit:  *limit,
				Cursor: *cursor,
			})
		} else {
			users, err = st.Users().ListAll(ctx, store.ListAllUsersParams{
				Limit:  *limit,
				Cursor: *cursor,
			})
		}
		if err != nil {
			return fmt.Errorf("list users: %w", err)
		}

		if len(users) == 0 {
			fmt.Println("No users found.")
			return nil
		}

		fmt.Printf("%-48s %-20s %-24s %-30s %-8s %-8s\n", "ID", "USERNAME", "DISPLAY_NAME", "EMAIL", "ADMIN", "CREATED")
		for _, u := range users {
			fmt.Printf("%-48s %-20s %-24s %-30s %-8s %-8s\n",
				u.ID, u.Username, u.DisplayName, u.Email, yesNo(u.IsAdmin), timefmt.Format(u.CreatedAt))
		}

		maybePrintNextCursor(users, *limit, func(u store.User) time.Time { return u.CreatedAt })
		return nil
	})
}

func runUserGet(args []string) error {
	var userID *string
	var username *string
	return withAdminStore("user get", args, func(fs *flag.FlagSet) {
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

func runUserCreate(args []string) error {
	var username *string
	var pw *string
	var displayName *string
	var email *string
	var emailVerified *bool
	var admin *bool
	return withAdminStore("user create", args, func(fs *flag.FlagSet) {
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

func runUserUpdate(args []string) error {
	var flagSet *flag.FlagSet
	var userID *string
	var username *string
	var displayName *string
	var email *string
	var emailVerifiedFlag *bool
	return withAdminStore("user update", args, func(fs *flag.FlagSet) {
		flagSet = fs
		userID = fs.String("id", "", "user ID")
		username = fs.String("username", "", "username (for lookup)")
		displayName = fs.String("display-name", "", "new display name")
		email = fs.String("email", "", "new email address")
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

		if !updateDisplayName && !updateEmail && !updateEmailVerified {
			return fmt.Errorf("no fields to update (use --display-name, --email, or --email-verified)")
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

			fmt.Printf("Updated user %q (id: %s)\n", user.Username, user.ID)
			return nil
		})
	})
}

func runUserDelete(args []string) error {
	var userID *string
	var username *string
	var force *bool
	return withAdminStore("user delete", args, func(fs *flag.FlagSet) {
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

		err = st.RunInTransaction(ctx, func(tx store.Store) error {
			if err := tx.Workers().MarkAllDeletedByUser(ctx, user.ID); err != nil {
				return fmt.Errorf("mark workers deleted: %w", err)
			}
			if err := tx.WorkerAccessGrants().DeleteByUser(ctx, user.ID); err != nil {
				return fmt.Errorf("delete worker access grants: %w", err)
			}
			if err := tx.Workspaces().SoftDeleteAllByUser(ctx, user.ID); err != nil {
				return fmt.Errorf("soft-delete workspaces: %w", err)
			}
			if err := tx.Sessions().DeleteByUser(ctx, user.ID); err != nil {
				return fmt.Errorf("delete sessions: %w", err)
			}
			if err := tx.OrgMembers().Delete(ctx, store.DeleteOrgMemberParams{
				OrgID:  user.OrgID,
				UserID: user.ID,
			}); err != nil {
				return fmt.Errorf("delete org member: %w", err)
			}
			if err := tx.Users().Delete(ctx, user.ID); err != nil {
				return fmt.Errorf("delete user: %w", err)
			}
			if err := tx.Orgs().SoftDelete(ctx, user.OrgID); err != nil {
				return fmt.Errorf("delete personal org: %w", err)
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

func runUserResetPassword(args []string) error {
	var userID *string
	var username *string
	var pw *string
	return withAdminStore("user reset-password", args, func(fs *flag.FlagSet) {
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

		err = st.RunInTransaction(ctx, func(tx store.Store) error {
			if err := tx.Users().UpdatePassword(ctx, store.UpdateUserPasswordParams{
				PasswordHash: hash,
				ID:           user.ID,
			}); err != nil {
				return fmt.Errorf("update password: %w", err)
			}

			if err := tx.Sessions().DeleteByUser(ctx, user.ID); err != nil {
				return fmt.Errorf("delete sessions: %w", err)
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

func runUserGrantAdmin(args []string) error {
	return runUserSetAdmin(args, true)
}

func runUserRevokeAdmin(args []string) error {
	return runUserSetAdmin(args, false)
}

func runUserSetAdmin(args []string, admin bool) error {
	verb := "grant-admin"
	if !admin {
		verb = "revoke-admin"
	}
	var userID *string
	var username *string
	return withAdminStore("user "+verb, args, func(fs *flag.FlagSet) {
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

func runUserListSessions(args []string) error {
	var userID *string
	var username *string
	return withAdminStore("user list-sessions", args, func(fs *flag.FlagSet) {
		userID = fs.String("id", "", "user ID")
		username = fs.String("username", "", "username")
	}, func(ctx context.Context, _ *config.Config, st store.Store) error {
		user, err := resolveUser(ctx, st, *userID, *username)
		if err != nil {
			return err
		}

		sessions, err := st.Sessions().ListByUserID(ctx, user.ID)
		if err != nil {
			return fmt.Errorf("list sessions: %w", err)
		}

		if len(sessions) == 0 {
			fmt.Printf("No active sessions for user %q.\n", user.Username)
			return nil
		}

		fmt.Printf("%-48s %-24s %-24s %-24s %-16s %s\n", "ID", "CREATED", "LAST_ACTIVE", "EXPIRES", "IP", "USER_AGENT")
		for _, s := range sessions {
			fmt.Printf("%-48s %-24s %-24s %-24s %-16s %s\n",
				s.ID, timefmt.Format(s.CreatedAt), timefmt.Format(s.LastActiveAt),
				timefmt.Format(s.ExpiresAt), s.IPAddress, truncate(s.UserAgent, 60))
		}
		return nil
	})
}
