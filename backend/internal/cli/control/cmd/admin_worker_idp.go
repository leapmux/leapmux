package cmd

import (
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
			resp, err := c.AdminWorkerService().ListWorkers(a.Context(), &leapmuxv1.AdminWorkerServiceListWorkersRequest{
				User: owner.Selector(), Status: statusEnum, Page: page.request(),
			})
			if err != nil {
				return err
			}
			rows := make([]adminWorkerRow, 0, len(resp.GetWorkers()))
			for _, w := range resp.GetWorkers() {
				rows = append(rows, adminWorkerRowOf(w))
			}
			return a.Result(struct {
				Workers    []adminWorkerRow `json:"workers"`
				NextCursor string           `json:"next_cursor"`
			}{Workers: rows, NextCursor: resp.GetPage().GetNextCursor()})
		},
	})
}

// adminWorkerRow reports a soft-deleted owner in OwnerDeleted. The Hub
// clears Username in that case, so the flag distinguishes deletion from
// a worker without an owner.
type adminWorkerRow struct {
	ID             string `json:"id"`
	RegisteredBy   string `json:"registered_by"`
	Username       string `json:"username"`
	OwnerDeleted   bool   `json:"owner_deleted"`
	Status         string `json:"status"`
	AutoRegistered bool   `json:"auto_registered"`
	CreatedAt      string `json:"created_at,omitempty"`
	LastSeenAt     string `json:"last_seen_at,omitempty"`
}

func adminWorkerRowOf(w *leapmuxv1.AdminWorker) adminWorkerRow {
	return adminWorkerRow{
		ID:             w.GetId(),
		RegisteredBy:   w.GetRegisteredBy(),
		Username:       w.GetOwnerUsername(),
		OwnerDeleted:   w.GetOwnerDeleted(),
		Status:         enumToken(w.GetStatus()),
		AutoRegistered: w.GetAutoRegistered(),
		CreatedAt:      formatTime(w.GetCreatedAt()),
		LastSeenAt:     formatTime(w.GetLastSeenAt()),
	}
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
			resp, err := c.AdminWorkerService().GetWorker(a.Context(), &leapmuxv1.AdminWorkerServiceGetWorkerRequest{Id: id})
			if err != nil {
				return err
			}
			return a.Result(adminWorkerRowOf(resp.GetWorker()))
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
			if _, err := c.AdminWorkerService().DeregisterWorker(a.Context(), &leapmuxv1.AdminWorkerServiceDeregisterWorkerRequest{Id: id}); err != nil {
				return err
			}
			return a.Result(struct {
				Deregistered string `json:"deregistered"`
			}{Deregistered: id})
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
			resp, err := c.AdminWorkerService().ListRegistrationKeys(a.Context(), &leapmuxv1.ListRegistrationKeysRequest{
				IncludeExpired: includeExpired, Page: page.request(),
			})
			if err != nil {
				return err
			}
			rows := make([]adminRegistrationKeyRow, 0, len(resp.GetKeys()))
			for _, k := range resp.GetKeys() {
				rows = append(rows, adminRegistrationKeyRowOf(k))
			}
			return a.Result(struct {
				Keys       []adminRegistrationKeyRow `json:"keys"`
				NextCursor string                    `json:"next_cursor"`
			}{Keys: rows, NextCursor: resp.GetPage().GetNextCursor()})
		},
	})
}

// adminRegistrationKeyRow uses Username for the creator, matching the
// other admin rows. CreatorDeleted distinguishes a deleted creator
// from a key without a creator.
type adminRegistrationKeyRow struct {
	ID             string `json:"id"`
	CreatedBy      string `json:"created_by"`
	Username       string `json:"username"`
	CreatorDeleted bool   `json:"creator_deleted"`
	CreatedAt      string `json:"created_at,omitempty"`
	ExpiresAt      string `json:"expires_at,omitempty"`
}

func adminRegistrationKeyRowOf(k *leapmuxv1.AdminRegistrationKey) adminRegistrationKeyRow {
	return adminRegistrationKeyRow{
		ID:             k.GetId(),
		CreatedBy:      k.GetCreatedBy(),
		Username:       k.GetCreatorUsername(),
		CreatorDeleted: k.GetCreatorDeleted(),
		CreatedAt:      formatTime(k.GetCreatedAt()),
		ExpiresAt:      formatTime(k.GetExpiresAt()),
	}
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
			if _, err := c.AdminWorkerService().RevokeRegistrationKey(a.Context(), &leapmuxv1.AdminWorkerServiceRevokeRegistrationKeyRequest{Id: id}); err != nil {
				return err
			}
			return a.Result(adminRevocationResult{Revoked: id})
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
			resp, err := c.AdminWorkerService().PurgeExpiredRegistrationKeys(a.Context(), &leapmuxv1.PurgeExpiredRegistrationKeysRequest{})
			if err != nil {
				return err
			}
			return a.Result(adminPurgeResult{Purged: resp.GetPurged()})
		},
	})
}

type adminOAuthProviderRow struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Name       string `json:"name"`
	IssuerURL  string `json:"issuer_url"`
	ClientID   string `json:"client_id"`
	Scopes     string `json:"scopes"`
	TrustEmail bool   `json:"trust_email"`
	Enabled    bool   `json:"enabled"`
	CreatedAt  string `json:"created_at,omitempty"`
}

func adminOAuthProviderRowOf(p *leapmuxv1.AdminOAuthProvider) adminOAuthProviderRow {
	return adminOAuthProviderRow{
		ID:         p.GetId(),
		Type:       p.GetProviderType(),
		Name:       p.GetName(),
		IssuerURL:  p.GetIssuerUrl(),
		ClientID:   p.GetClientId(),
		Scopes:     p.GetScopes(),
		TrustEmail: p.GetTrustEmail(),
		Enabled:    p.GetEnabled(),
		CreatedAt:  formatTime(p.GetCreatedAt()),
	}
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
			resp, err := c.AdminIdPService().AddOAuthProvider(a.Context(), req)
			if err != nil {
				return err
			}
			return a.Result(adminOAuthProviderRowOf(resp.GetProvider()))
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
			resp, err := c.AdminIdPService().ListOAuthProviders(a.Context(), &leapmuxv1.ListOAuthProvidersRequest{})
			if err != nil {
				return err
			}
			providers := make([]adminOAuthProviderRow, 0, len(resp.GetProviders()))
			for _, p := range resp.GetProviders() {
				providers = append(providers, adminOAuthProviderRowOf(p))
			}
			return a.Result(struct {
				Providers []adminOAuthProviderRow `json:"providers"`
			}{Providers: providers})
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
			resp, err := c.AdminIdPService().RemoveOAuthProvider(a.Context(), &leapmuxv1.RemoveOAuthProviderRequest{
				Id: id, Force: force,
			})
			if err != nil {
				return err
			}
			return a.Result(struct {
				Removed        string `json:"removed"`
				LockedOutUsers int64  `json:"locked_out_users"`
			}{Removed: id, LockedOutUsers: resp.GetLockedOutUsers()})
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
			if _, err := c.AdminIdPService().SetOAuthProviderEnabled(a.Context(), &leapmuxv1.SetOAuthProviderEnabledRequest{
				Id: id, Enabled: enabled,
			}); err != nil {
				return err
			}
			return a.Result(struct {
				ID      string `json:"id"`
				Enabled bool   `json:"enabled"`
			}{ID: id, Enabled: enabled})
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
