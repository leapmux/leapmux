package cmd

import (
	"context"
	"errors"
	"strconv"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Flag names, defaults, and validation messages carry over verbatim from
// the old offline admin verbs; output uses the control JSON envelope.

func newAdminWorkerList() *cobra.Command {
	var page adminPageFlags
	var owner userFilterFlags
	var status string
	var statusEnum leapmuxv1.WorkerStatus
	return adminLeaf(adminVerbSpec{
		Use:   "list",
		Short: "List the workers of every user",
		Examples: []cli.Example{
			{Command: "leapmux control admin worker list"},
			{Comment: "Include the workers that are gone.", Command: "leapmux control admin worker list --status all"},
		},
		Page: &page,
		Flags: func(fs *pflag.FlagSet) {
			owner.bind(fs)
			fs.StringVar(&status, "status", "active", "worker status filter (active, deregistering, deleted, all)")
		},
		BeforeDial: func(a adminArgs) error {
			if err := owner.resolve(a); err != nil {
				return err
			}
			switch status {
			case "active":
				statusEnum = leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE
			case "deregistering":
				statusEnum = leapmuxv1.WorkerStatus_WORKER_STATUS_DEREGISTERING
			case "deleted":
				statusEnum = leapmuxv1.WorkerStatus_WORKER_STATUS_DELETED
			case "all", "":
				statusEnum = leapmuxv1.WorkerStatus_WORKER_STATUS_UNSPECIFIED
			default:
				return cli.Usagef("unknown worker status %q", status).
					WithHint("use active, deregistering, deleted or all")
			}
			return nil
		},
		Run: func(c *control.Client, a adminArgs) error {
			resp, err := c.AdminWorkerService().ListWorkers(context.Background(), &leapmuxv1.AdminWorkerServiceListWorkersRequest{
				User: owner.Selector(), Status: statusEnum, Page: page.request(),
			})
			if err != nil {
				return err
			}
			rows := make([]map[string]any, 0, len(resp.GetWorkers()))
			for _, w := range resp.GetWorkers() {
				rows = append(rows, adminWorkerJSON(w))
			}
			return a.Result(map[string]any{"workers": rows, "next_cursor": resp.GetPage().GetNextCursor()})
		},
	})
}

// adminWorkerJSON renders one worker row.
//
// owner_deleted is what tells an operator that `username` is empty because
// the account is gone, rather than because the worker has no owner: the
// hub blanks the name for a soft-deleted owner and reports the fact in
// this flag alone.
func adminWorkerJSON(w *leapmuxv1.AdminWorker) map[string]any {
	row := map[string]any{
		"id":              w.GetId(),
		"registered_by":   w.GetRegisteredBy(),
		"username":        w.GetOwnerUsername(),
		"owner_deleted":   w.GetOwnerDeleted(),
		"status":          w.GetStatus().String(),
		"auto_registered": w.GetAutoRegistered(),
	}
	putTime(row, "created_at", w.GetCreatedAt())
	putTime(row, "last_seen_at", w.GetLastSeenAt())
	return row
}

func newAdminWorkerGet() *cobra.Command {
	var id string
	return adminLeaf(adminVerbSpec{
		Use:   "get <worker>",
		Short: "Show a worker of any user",
		Examples: []cli.Example{
			{Command: "leapmux control admin worker get 5f2c9a1d"},
		},
		BeforeDial: argInto(&id, "worker"),
		Run: func(c *control.Client, a adminArgs) error {
			resp, err := c.AdminWorkerService().GetWorker(context.Background(), &leapmuxv1.AdminWorkerServiceGetWorkerRequest{Id: id})
			if err != nil {
				return err
			}
			return a.Result(adminWorkerJSON(resp.GetWorker()))
		},
	})
}

func newAdminWorkerDeregister() *cobra.Command {
	var id string
	return adminLeaf(adminVerbSpec{
		Use:   "deregister <worker>",
		Short: "Deregister a worker of any user",
		Examples: []cli.Example{
			{Command: "leapmux control admin worker deregister 5f2c9a1d"},
		},
		Mutates:    true,
		BeforeDial: argInto(&id, "worker"),
		Run: func(c *control.Client, a adminArgs) error {
			if _, err := c.AdminWorkerService().DeregisterWorker(context.Background(), &leapmuxv1.AdminWorkerServiceDeregisterWorkerRequest{Id: id}); err != nil {
				return err
			}
			return a.Result(map[string]any{"deregistered": id})
		},
	})
}

func newAdminWorkerRegKeyList() *cobra.Command {
	var page adminPageFlags
	var includeExpired bool
	return adminLeaf(adminVerbSpec{
		Use:   "list",
		Short: "List worker registration keys",
		Examples: []cli.Example{
			{Command: "leapmux control admin worker reg-key list"},
		},
		Page: &page,
		Flags: func(fs *pflag.FlagSet) {
			fs.BoolVar(&includeExpired, "include-expired", false, "include revoked or expired keys (forensics; default shows only live keys)")
		},
		Run: func(c *control.Client, a adminArgs) error {
			resp, err := c.AdminWorkerService().ListRegistrationKeys(context.Background(), &leapmuxv1.ListRegistrationKeysRequest{
				IncludeExpired: includeExpired, Page: page.request(),
			})
			if err != nil {
				return err
			}
			rows := make([]map[string]any, 0, len(resp.GetKeys()))
			for _, k := range resp.GetKeys() {
				rows = append(rows, adminRegistrationKeyJSON(k))
			}
			return a.Result(map[string]any{"keys": rows, "next_cursor": resp.GetPage().GetNextCursor()})
		},
	})
}

// adminRegistrationKeyJSON renders one worker registration-key row.
//
// The owner column is spelled `username`, as it is on every other admin
// row, so one filter or script reads them all. creator_deleted is what
// tells an operator that the name is empty because the account is gone
// rather than because the key has no creator. Both stamps go through
// putTime for the reason tokenLifecycleJSON states.
func adminRegistrationKeyJSON(k *leapmuxv1.AdminRegistrationKey) map[string]any {
	row := map[string]any{
		"id":              k.GetId(),
		"created_by":      k.GetCreatedBy(),
		"username":        k.GetCreatorUsername(),
		"creator_deleted": k.GetCreatorDeleted(),
	}
	putTime(row, "created_at", k.GetCreatedAt())
	putTime(row, "expires_at", k.GetExpiresAt())
	return row
}

func newAdminWorkerRegKeyRevoke() *cobra.Command {
	var id string
	return adminLeaf(adminVerbSpec{
		Use:   "revoke <reg-key>",
		Short: "Revoke a worker registration key",
		Examples: []cli.Example{
			{Command: "leapmux control admin worker reg-key revoke 9Zt4Kp"},
		},
		Mutates:    true,
		BeforeDial: argInto(&id, "reg-key"),
		Run: func(c *control.Client, a adminArgs) error {
			if _, err := c.AdminWorkerService().RevokeRegistrationKey(context.Background(), &leapmuxv1.AdminWorkerServiceRevokeRegistrationKeyRequest{Id: id}); err != nil {
				return err
			}
			return a.Result(map[string]any{"revoked": id})
		},
	})
}

func newAdminWorkerRegKeyPurgeExpired() *cobra.Command {
	return adminLeaf(adminVerbSpec{
		Use:   "purge-expired",
		Short: "Delete every expired or revoked registration key",
		Examples: []cli.Example{
			{Command: "leapmux control admin worker reg-key purge-expired"},
		},
		Mutates: true,
		Run: func(c *control.Client, a adminArgs) error {
			resp, err := c.AdminWorkerService().PurgeExpiredRegistrationKeys(context.Background(), &leapmuxv1.PurgeExpiredRegistrationKeysRequest{})
			if err != nil {
				return err
			}
			return a.Result(map[string]any{"purged": resp.GetPurged()})
		},
	})
}

// adminOAuthProviderJSON renders one OAuth provider row. Its stamp goes
// through putTime for the reason tokenLifecycleJSON states.
func adminOAuthProviderJSON(p *leapmuxv1.AdminOAuthProvider) map[string]any {
	row := map[string]any{
		"id":          p.GetId(),
		"type":        p.GetProviderType(),
		"name":        p.GetName(),
		"issuer_url":  p.GetIssuerUrl(),
		"client_id":   p.GetClientId(),
		"scopes":      p.GetScopes(),
		"trust_email": p.GetTrustEmail(),
		"enabled":     p.GetEnabled(),
	}
	putTime(row, "created_at", p.GetCreatedAt())
	return row
}

func newAdminOAuthProviderAdd() *cobra.Command {
	var providerType, name, clientID, clientSecret, issuerURL, scopes string
	var trustEmail string
	var req *leapmuxv1.AddOAuthProviderRequest
	return adminLeaf(adminVerbSpec{
		Use:   "add",
		Short: "Add an OAuth or OIDC sign-in provider",
		Examples: []cli.Example{
			{Command: "leapmux control admin idp add --type github --client-id Iv1.abc --client-secret s3cr3t"},
		},
		Mutates: true,
		Flags: func(fs *pflag.FlagSet) {
			fs.StringVar(&providerType, "type", "", "provider type (github, google, apple, oidc)")
			fs.StringVar(&name, "name", "", "display name (defaults to the preset name)")
			fs.StringVar(&clientID, "client-id", "", "OAuth client ID")
			fs.StringVar(&clientSecret, "client-secret", "", "OAuth client secret")
			fs.StringVar(&issuerURL, "issuer-url", "", "OIDC issuer URL")
			fs.StringVar(&scopes, "scopes", "", "space-separated scopes")
			fs.StringVar(&trustEmail, "trust-email", "", "trust verified provider emails (true/false; required for generic OIDC)")
		},
		BeforeDial: func(adminArgs) error {
			req = &leapmuxv1.AddOAuthProviderRequest{
				ProviderType: providerType, Name: name, ClientId: clientID,
				ClientSecret: clientSecret, IssuerUrl: issuerURL, Scopes: scopes,
			}
			if trustEmail != "" {
				b, err := parseBoolFlag(trustEmail, "trust-email")
				if err != nil {
					return cli.Usagef("%s", err.Error())
				}
				req.TrustEmail = &b
			}
			return nil
		},
		Run: func(c *control.Client, a adminArgs) error {
			resp, err := c.AdminIdPService().AddOAuthProvider(context.Background(), req)
			if err != nil {
				return err
			}
			return a.Result(adminOAuthProviderJSON(resp.GetProvider()))
		},
	})
}

func newAdminOAuthProviderList() *cobra.Command {
	return adminLeaf(adminVerbSpec{
		Use:   "list",
		Short: "List the sign-in providers",
		Examples: []cli.Example{
			{Command: "leapmux control admin idp list"},
		},
		Run: func(c *control.Client, a adminArgs) error {
			resp, err := c.AdminIdPService().ListOAuthProviders(context.Background(), &leapmuxv1.ListOAuthProvidersRequest{})
			if err != nil {
				return err
			}
			providers := make([]map[string]any, 0, len(resp.GetProviders()))
			for _, p := range resp.GetProviders() {
				providers = append(providers, adminOAuthProviderJSON(p))
			}
			return a.Result(map[string]any{"providers": providers})
		},
	})
}

// newAdminOAuthProviderRemove builds `control admin idp
// remove`.
//
// Deleting the provider row cascades every account link to it away. An
// account with no password whose only link was this provider then has no
// login method left, and only the offline `leapmux recover` restores it.
// The hub refuses that removal, and --force removes the provider anyway.
//
// locked_out_users counts those accounts. The reply carries it either
// way, and it is the only report an operator gets of how many accounts a
// forced removal locked out.
func newAdminOAuthProviderRemove() *cobra.Command {
	var id string
	var force bool
	return adminLeaf(adminVerbSpec{
		Use:   "remove <idp>",
		Short: "Remove a sign-in provider",
		Examples: []cli.Example{
			{Command: "leapmux control admin idp remove 3hKd9s"},
		},
		Mutates: true,
		Flags: func(fs *pflag.FlagSet) {
			fs.BoolVar(&force, "force", false, "required to remove a provider that is the last login method of an account")
		},
		BeforeDial: argInto(&id, "idp"),
		Run: func(c *control.Client, a adminArgs) error {
			resp, err := c.AdminIdPService().RemoveOAuthProvider(context.Background(), &leapmuxv1.RemoveOAuthProviderRequest{
				Id: id, Force: force,
			})
			if err != nil {
				return err
			}
			return a.Result(map[string]any{"removed": id, "locked_out_users": resp.GetLockedOutUsers()})
		},
	})
}

// newAdminOAuthProviderSetEnabled builds `control admin idp
// enable|disable`.
func newAdminOAuthProviderSetEnabled(enabled bool, use, short string, examples []cli.Example) *cobra.Command {
	var id string
	return adminLeaf(adminVerbSpec{
		Use:        use,
		Short:      short,
		Examples:   examples,
		Mutates:    true,
		BeforeDial: argInto(&id, "idp"),
		Run: func(c *control.Client, a adminArgs) error {
			if _, err := c.AdminIdPService().SetOAuthProviderEnabled(context.Background(), &leapmuxv1.SetOAuthProviderEnabledRequest{
				Id: id, Enabled: enabled,
			}); err != nil {
				return err
			}
			return a.Result(map[string]any{"id": id, "enabled": enabled})
		},
	})
}

// parseBoolFlag parses a tri-state string flag ("true"/"false").
func parseBoolFlag(raw, name string) (bool, error) {
	b, err := strconv.ParseBool(raw)
	if err != nil {
		return false, errors.New(name + " must be 'true' or 'false'")
	}
	return b, nil
}
