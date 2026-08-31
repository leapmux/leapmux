package main

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/usernames"
	"github.com/leapmux/leapmux/util/validate"
)

// runBootstrapCreateAdmin creates the FIRST administrator on an empty
// hub — the one identity operation that cannot require an authenticated
// admin to already exist. It refuses once any admin exists; every later
// user (admin included) is created online through
// `leapmux control admin user create`.
func runBootstrapCreateAdmin(cmd cmdCtx, args []string) error {
	var username *string
	var pw *string
	var displayName *string
	return withRecoverStore(cmd, args, func(fs *flag.FlagSet) {
		username = fs.String("username", "", "username (required)")
		pw = fs.String("password", "", "password (prompted if omitted)")
		displayName = fs.String("display-name", "", "display name")
	}, func(ctx context.Context, _ *config.Config, st store.Store) error {
		if _, err := st.Users().GetFirstAdmin(ctx); err == nil {
			return fmt.Errorf("an administrator already exists; use `leapmux control admin user create --admin` instead")
		} else if !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("look up existing admins: %w", err)
		}

		if *username == "" {
			return fmt.Errorf("--username is required")
		}
		pwValue, err := control.RequirePassword(*pw, "Password: ")
		if err != nil {
			return err
		}
		slug, err := validate.SanitizeSlug("username", *username)
		if err != nil {
			return err
		}
		// Every creation path refuses a reserved system username, and this
		// one is offline, so nothing else stands behind it. A row named
		// "solo" in a non-solo database is auto-authenticated for EVERY
		// request if the same data dir is later opened with `leapmux solo`
		// — the interceptor's solo short-circuit runs before the email gate
		// and before the admin gate, so that row's identity is handed out
		// with no credential at all.
		if usernames.IsReservedSystem(slug) {
			return fmt.Errorf("%q is a reserved username", slug)
		}
		if err := validate.ValidatePassword(pwValue); err != nil {
			return err
		}
		dispName, err := validate.SanitizeDisplayName(*displayName, slug)
		if err != nil {
			return fmt.Errorf("display name: %w", err)
		}
		hash, err := password.Hash(pwValue)
		if err != nil {
			return fmt.Errorf("hash password: %w", err)
		}

		user, err := service.CreateUser(ctx, st, service.CreateUserParams{
			Username:     slug,
			PasswordHash: hash,
			DisplayName:  dispName,
			PasswordSet:  true,
			IsAdmin:      true,
		})
		if err != nil {
			// The bootstrap admin carries no email, and every dialect's
			// unique email index skips a blank one. So a conflict here can
			// only be the username.
			if errors.Is(err, store.ErrConflict) {
				return &service.FieldTakenError{Field: "username", Value: slug}
			}
			return err
		}
		fmt.Printf("Created administrator %q (id: %s)\n", slug, user.ID)
		return nil
	})
}

// runPasswordReset resets a user's password with the hub stopped — the
// break-glass path for a hub whose only admin forgot their password.
//
// `leapmux control admin user reset-password` is the online twin, and it is
// the one to reach for while the hub serves: it goes through the
// administrator gate, and it tears the user's credentials down in the
// serving hub's process instead of leaving that to the revocation watcher's
// next sweep. This verb is what remains when the hub cannot start. It opens
// the database directly, which a running hub must not do.
func runPasswordReset(cmd cmdCtx, args []string) error {
	var userID *string
	var username *string
	var pw *string
	return withRecoverStore(cmd, args, func(fs *flag.FlagSet) {
		userID = fs.String("id", "", "user ID")
		username = fs.String("username", "", "username")
		pw = fs.String("password", "", "new password (prompted if omitted)")
	}, func(ctx context.Context, _ *config.Config, st store.Store) error {
		user, err := resolveUser(ctx, st, *userID, *username)
		if err != nil {
			return err
		}

		pwValue, err := control.RequirePassword(*pw, "New password: ")
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

		// Mint once and refuse a blank id rather than letting the credential
		// revocation below report success having revoked nothing.
		revokeUID, err := mintResolvedUserID(user)
		if err != nil {
			return err
		}

		err = st.RunInUserAuthTransaction(ctx, revokeUID, func(tx store.Store) error {
			if err := tx.Users().UpdatePassword(ctx, store.UpdateUserPasswordParams{
				PasswordHash: hash,
				ID:           user.ID,
			}); err != nil {
				return fmt.Errorf("update password: %w", err)
			}

			// Offline break-glass matches online admin reset and self-service
			// CompleteAccountRecoveryPassword: passkeys, ceremonies, and any pending
			// recovery link die with the password rotation.
			if err := service.RevokePasskeyAuthState(ctx, tx, user.ID); err != nil {
				return err
			}

			// The password rotation ends every credential that predates it
			// (sessions, api tokens, delegation tokens, channels): the store
			// records durable revocation events in this transaction so the
			// hub's revocation watcher picks this up cross-process and fires
			// CloseChannelsByUserRevocation without an IPC.
			if _, _, _, err := service.RevokeCredentialsAfterRotation(ctx, tx, revokeUID, false); err != nil {
				return err
			}

			return nil
		})
		if err != nil {
			return err
		}

		fmt.Printf("Password reset for user %q (id: %s). All sessions and passkeys revoked.\n", user.Username, user.ID)
		return nil
	})
}
