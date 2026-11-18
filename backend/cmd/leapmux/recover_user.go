package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/usernames"
	"github.com/leapmux/leapmux/util/validate"
)

// newBootstrapCreateAdminCommand builds `leapmux recover bootstrap
// create-admin`. It creates an administrator when no live administrator
// exists — the one identity operation that cannot require an authenticated
// admin to already exist. It refuses once any admin exists; every later
// user (admin included) is created online through
// `leapmux control admin user create`.
func newBootstrapCreateAdminCommand() *cobra.Command {
	var pw, displayName string
	return recoverStoreLeaf(recoverSpec{
		Leaf: cli.Leaf{
			Use:     "create-admin <username>",
			Short:   "Create the initial administrator (refuses once any admin exists)",
			Mutates: true,
			Args:    map[string]string{"username": "The username of the new administrator."},
			Examples: []cli.Example{
				{Comment: "Create the first administrator; the command prompts for the password.", Command: "leapmux recover bootstrap create-admin alice"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.StringVar(&pw, "password", "", "password; a terminal prompt asks for it when this flag is absent")
				fs.StringVar(&displayName, "display-name", "", "display name; the username when this flag is absent")
			},
		},
		BeforeOpen: func(inv *cli.Invocation) error {
			if strings.TrimSpace(inv.Arg("username")) == "" {
				return cli.Usagef("<username> is empty")
			}
			return nil
		},
	}, func(inv *cli.Invocation, _ *config.Config, st store.Store) error {
		ctx := inv.Context
		if _, err := st.Users().GetFirstAdmin(ctx); err == nil {
			return cli.Newf(cli.CodeConflict, "an administrator already exists").
				WithHint("create every later user online: leapmux control admin user create --admin")
		} else if !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("look up existing admins: %w", err)
		}

		pwValue, err := control.RequirePassword(ctx, pw, "Password: ")
		if err != nil {
			return err
		}
		slug, err := validate.SanitizeSlug("username", inv.Arg("username"))
		if err != nil {
			return err
		}
		// Every creation path refuses a reserved system username, and this
		// one is offline, so nothing else stands behind it. A row named
		// "solo" in a non-solo database BECOMES the administrator if the
		// same data dir is later opened with `leapmux solo` — the solo rung
		// runs before the email gate and before the admin gate, so that
		// row's identity is handed out against the password this command
		// set, and with no credential at all when it set none.
		if usernames.IsReservedSystem(slug) {
			return fmt.Errorf("%q is a reserved username", slug)
		}
		if err := validate.ValidatePassword(pwValue); err != nil {
			return err
		}
		dispName, err := validate.SanitizeDisplayName(displayName, slug)
		if err != nil {
			return fmt.Errorf("display name: %w", err)
		}
		hash, err := password.Hash(pwValue)
		if err != nil {
			return fmt.Errorf("hash password: %w", err)
		}

		user, err := service.CreateUser(ctx, st, service.CreateUserParams{
			Username:              slug,
			PasswordHash:          hash,
			DisplayName:           dispName,
			FirstCredentialExempt: true,
			IsAdmin:               true,
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
		_, err = fmt.Fprintf(inv.Printer.Out, "Created administrator %q (id: %s)\n", slug, user.ID)
		return err
	})
}

// newPasswordResetCommand builds `leapmux recover password reset`. It resets
// a user's password with the hub stopped — the break-glass path for a hub
// whose only admin forgot their password.
//
// `leapmux control admin user reset-password` is the online twin, and it is
// the one to reach for while the hub serves: it goes through the
// administrator gate, and it tears the user's credentials down in the
// serving hub's process instead of leaving that to the revocation watcher's
// next sweep. This verb is what remains when the hub cannot start. It opens
// the database directly, which a running hub must not do.
func newPasswordResetCommand() *cobra.Command {
	var pw string
	var sel *leapmuxv1.UserSelector
	return recoverStoreLeaf(recoverSpec{
		Leaf: cli.Leaf{
			Use:     "reset <user>",
			Short:   "Reset a user's password and revoke all their sessions",
			Mutates: true,
			Args:    map[string]string{"user": control.UserArgumentHelp},
			Examples: []cli.Example{
				{Comment: "Reset the password of alice; the command prompts for the new password.", Command: "leapmux recover password reset alice"},
				{Comment: "Select the user by id.", Command: "leapmux recover password reset id:0193c0de-7a1b-7c2d-8e3f-4a5b6c7d8e9f"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.StringVar(&pw, "password", "", "new password; a terminal prompt asks for it when this flag is absent")
			},
		},
		BeforeOpen: func(inv *cli.Invocation) (err error) {
			sel, err = control.ParseUserArgument(inv.Arg("user"))
			return err
		},
	}, func(inv *cli.Invocation, _ *config.Config, st store.Store) error {
		ctx := inv.Context
		user, err := resolveUser(ctx, st, sel, inv.Arg("user"))
		if err != nil {
			return err
		}

		pwValue, err := control.RequirePassword(ctx, pw, "New password: ")
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

		_, err = fmt.Fprintf(inv.Printer.Out, "Password reset for user %q (id: %s). All sessions and passkeys revoked.\n", user.Username, user.ID)
		return err
	})
}
