package cmd

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/hub/oauth"
)

func newAdminWorkerList() *cobra.Command {
	var page adminPageFlags
	var owner userFilterFlags
	var status string
	statusChoices := append(enumTokens(leapmuxv1.WorkerStatus(0)), "all")
	return cli.NewLeaf(cli.Leaf{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the workers of every user",
		Examples: []cli.Example{
			{Command: "leapmux admin worker list"},
			{Comment: "Include the workers that are gone.", Command: "leapmux admin worker list --status all"},
		},
		Flags: func(fs *pflag.FlagSet) {
			page.bind(fs)
			owner.bind(fs)
			fs.StringVar(&status, "status", "active", "worker status filter")
			cli.SetEnum(fs, "status", statusChoices...)
		},
		Run: func(inv *cli.Invocation) (any, error) {
			if err := page.validate(); err != nil {
				return nil, err
			}
			ownerTarget, err := owner.parse(inv)
			if err != nil {
				return nil, err
			}
			var statusEnum leapmuxv1.WorkerStatus
			if strings.EqualFold(status, "all") {
				statusEnum = leapmuxv1.WorkerStatus_WORKER_STATUS_UNSPECIFIED
			} else {
				num, ok := parseEnumToken(leapmuxv1.WorkerStatus(0), status)
				if !ok {
					return nil, cli.Usagef("unknown worker status %q", status).
						WithHint("use %s", strings.Join(statusChoices, ", "))
				}
				statusEnum = leapmuxv1.WorkerStatus(num)
			}
			c, err := requireAdminClient(inv)
			if err != nil {
				return nil, err
			}
			resp, err := callWithAdminUserTarget(inv.Context, c, ownerTarget, func(sel *leapmuxv1.UserSelector) (*leapmuxv1.AdminWorkerServiceListWorkersResponse, error) {
				return c.AdminWorkerService().ListWorkers(inv.Context, &leapmuxv1.AdminWorkerServiceListWorkersRequest{
					User: sel, Status: statusEnum, Page: page.request(),
				})
			})
			if err != nil {
				return nil, err
			}
			rows := make([]adminWorkerRow, 0, len(resp.GetWorkers()))
			for _, w := range resp.GetWorkers() {
				rows = append(rows, adminWorkerRowOf(w))
			}
			return adminWorkerListResult{Workers: rows, NextCursor: resp.GetPage().GetNextCursor()}, nil
		},
	})
}

type adminWorkerListResult struct {
	Workers    []adminWorkerRow `json:"workers"`
	NextCursor string           `json:"next_cursor"`
}

func (r adminWorkerListResult) QuietLines() []string {
	out := make([]string, 0, len(r.Workers))
	for _, w := range r.Workers {
		out = append(out, w.ID)
	}
	return out
}

func resolveAdminWorker(ctx context.Context, c *control.HubClient, raw string) (string, *leapmuxv1.AdminWorker, error) {
	byID := map[string]*leapmuxv1.AdminWorker{}
	resolved, err := resolvePagedAdminID(ctx, "worker", raw, false,
		func(ctx context.Context, page *leapmuxv1.PageRequest) ([]*leapmuxv1.AdminWorker, *leapmuxv1.PageResponse, error) {
			resp, err := c.AdminWorkerService().ListWorkers(ctx, &leapmuxv1.AdminWorkerServiceListWorkersRequest{
				Status: leapmuxv1.WorkerStatus_WORKER_STATUS_UNSPECIFIED,
				Page:   page,
			})
			return resp.GetWorkers(), resp.GetPage(), err
		},
		(*leapmuxv1.AdminWorker).GetId,
		func(w *leapmuxv1.AdminWorker) cli.Candidate {
			byID[w.GetId()] = w
			return cli.Candidate{ID: w.GetId(), Kind: "worker", Title: w.GetOwnerUsername()}
		},
	)
	if err != nil {
		return "", nil, err
	}
	return resolved, byID[resolved], nil
}

func resolveAdminWorkerID(ctx context.Context, c *control.HubClient, raw string) (string, error) {
	resolved, _, err := resolveAdminWorker(ctx, c, raw)
	return resolved, err
}

func resolveAdminRegKeyID(ctx context.Context, c *control.HubClient, raw string) (string, error) {
	return resolveLiveThenAllPagedAdminID(ctx, "registration key", raw, false,
		func(ctx context.Context, includeExpired bool, page *leapmuxv1.PageRequest) ([]*leapmuxv1.AdminRegistrationKey, *leapmuxv1.PageResponse, error) {
			resp, err := c.AdminWorkerService().ListRegistrationKeys(ctx, &leapmuxv1.ListRegistrationKeysRequest{
				IncludeExpired: includeExpired,
				Page:           page,
			})
			return resp.GetKeys(), resp.GetPage(), err
		},
		(*leapmuxv1.AdminRegistrationKey).GetId,
		func(k *leapmuxv1.AdminRegistrationKey) cli.Candidate {
			return cli.Candidate{ID: k.GetId(), Kind: "reg-key", Title: k.GetCreatorUsername()}
		},
	)
}

func resolveAdminIdPID(ctx context.Context, c *control.HubClient, raw string) (string, error) {
	return resolveAdminTarget(ctx, raw, "sign-in provider", true, func(ctx context.Context) ([]cli.Candidate, error) {
		resp, err := c.AdminIdPService().ListOAuthProviders(ctx, &leapmuxv1.ListOAuthProvidersRequest{})
		if err != nil {
			return nil, err
		}
		out := make([]cli.Candidate, 0, len(resp.GetProviders()))
		for _, p := range resp.GetProviders() {
			out = append(out, cli.Candidate{ID: p.GetId(), Kind: p.GetProviderType(), Title: p.GetName()})
		}
		return out, nil
	})
}

// adminWorkerRow reports a soft-deleted owner in OwnerDeleted. The Hub
// clears Username in that case, so the flag distinguishes deletion from
// a worker without an owner.
type adminWorkerRow struct {
	ID             string `json:"id" table:"ID,short"`
	RegisteredBy   string `json:"registered_by"`
	Username       string `json:"username" table:"USERNAME"`
	OwnerDeleted   bool   `json:"owner_deleted"`
	Status         string `json:"status" table:"STATUS,state"`
	AutoRegistered bool   `json:"auto_registered"`
	CreatedAt      string `json:"created_at,omitempty"`
	LastSeenAt     string `json:"last_seen_at,omitempty" table:"LAST SEEN,dim"`
}

func (r adminWorkerRow) QuietLines() []string { return []string{r.ID} }

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
	return cli.NewLeaf(cli.Leaf{
		Use:   "get <worker>",
		Short: "Show a worker of any user",
		Examples: []cli.Example{
			{Command: "leapmux admin worker get 5f2c9a1d"},
		},
		Complete: completeAdminWorkers,
		Run: func(inv *cli.Invocation) (any, error) {
			id, err := requireNonEmptyArg(inv, "worker")
			if err != nil {
				return nil, err
			}
			c, err := requireAdminClient(inv)
			if err != nil {
				return nil, err
			}
			resolved, worker, err := resolveAdminWorker(inv.Context, c, id)
			if err != nil {
				return nil, err
			}
			if worker != nil {
				return adminWorkerRowOf(worker), nil
			}
			resp, err := c.AdminWorkerService().GetWorker(inv.Context, &leapmuxv1.AdminWorkerServiceGetWorkerRequest{Id: resolved})
			if err != nil {
				return nil, err
			}
			return adminWorkerRowOf(resp.GetWorker()), nil
		},
	})
}

type adminDeregisterResult struct {
	Deregistered string `json:"deregistered"`
}

func (r adminDeregisterResult) QuietLines() []string { return []string{r.Deregistered} }

func newAdminWorkerDeregister() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:         "deregister <worker>",
		Short:       "Deregister a worker of any user",
		Mutates:     true,
		Destructive: true,
		Examples: []cli.Example{
			{Command: "leapmux admin worker deregister 5f2c9a1d"},
		},
		Complete: completeAdminWorkers,
		Run: func(inv *cli.Invocation) (any, error) {
			id, err := requireNonEmptyArg(inv, "worker")
			if err != nil {
				return nil, err
			}
			c, err := requireAdminClient(inv)
			if err != nil {
				return nil, err
			}
			resolved, err := resolveAdminWorkerID(inv.Context, c, id)
			if err != nil {
				return nil, err
			}
			if _, err := c.AdminWorkerService().DeregisterWorker(inv.Context, &leapmuxv1.AdminWorkerServiceDeregisterWorkerRequest{Id: resolved}); err != nil {
				return nil, err
			}
			return adminDeregisterResult{Deregistered: resolved}, nil
		},
	})
}

func newAdminWorkerRegKeyList() *cobra.Command {
	var page adminPageFlags
	var includeExpired bool
	return cli.NewLeaf(cli.Leaf{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List worker registration keys",
		Examples: []cli.Example{
			{Command: "leapmux admin worker reg-key list"},
		},
		Flags: func(fs *pflag.FlagSet) {
			page.bind(fs)
			fs.BoolVar(&includeExpired, "include-expired", false, "include revoked or expired keys (forensics; default shows only live keys)")
		},
		Run: func(inv *cli.Invocation) (any, error) {
			if err := page.validate(); err != nil {
				return nil, err
			}
			c, err := requireAdminClient(inv)
			if err != nil {
				return nil, err
			}
			resp, err := c.AdminWorkerService().ListRegistrationKeys(inv.Context, &leapmuxv1.ListRegistrationKeysRequest{
				IncludeExpired: includeExpired, Page: page.request(),
			})
			if err != nil {
				return nil, err
			}
			rows := make([]adminRegistrationKeyRow, 0, len(resp.GetKeys()))
			for _, k := range resp.GetKeys() {
				rows = append(rows, adminRegistrationKeyRowOf(k))
			}
			return adminRegKeyListResult{Keys: rows, NextCursor: resp.GetPage().GetNextCursor()}, nil
		},
	})
}

type adminRegKeyListResult struct {
	Keys       []adminRegistrationKeyRow `json:"keys"`
	NextCursor string                    `json:"next_cursor"`
}

func (r adminRegKeyListResult) QuietLines() []string {
	out := make([]string, 0, len(r.Keys))
	for _, k := range r.Keys {
		out = append(out, k.ID)
	}
	return out
}

// adminRegistrationKeyRow uses Username for the creator, matching the
// other admin rows. CreatorDeleted distinguishes a deleted creator
// from a key without a creator.
type adminRegistrationKeyRow struct {
	ID             string `json:"id" table:"ID,short"`
	CreatedBy      string `json:"created_by"`
	Username       string `json:"username" table:"USERNAME"`
	CreatorDeleted bool   `json:"creator_deleted"`
	CreatedAt      string `json:"created_at,omitempty" table:"CREATED,dim"`
	ExpiresAt      string `json:"expires_at,omitempty" table:"EXPIRES,dim"`
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
	return newAdminRevokeLeaf(adminRevokeLeafConfig{
		argName:  "reg-key",
		short:    "Revoke a worker registration key",
		example:  "leapmux admin worker reg-key revoke 9Zt4Kp",
		complete: completeAdminRegKeys,
		resolve:  resolveAdminRegKeyID,
		revoke: func(ctx context.Context, c *control.HubClient, id string) error {
			_, err := c.AdminWorkerService().RevokeRegistrationKey(ctx, &leapmuxv1.AdminWorkerServiceRevokeRegistrationKeyRequest{Id: id})
			return err
		},
	})
}

func newAdminWorkerRegKeyPurgeExpired() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:              "purge-expired",
		Short:            "Delete every expired or revoked registration key",
		Mutates:          true,
		Destructive:      true,
		QuietUnsupported: true,
		Examples: []cli.Example{
			{Command: "leapmux admin worker reg-key purge-expired"},
		},
		Run: func(inv *cli.Invocation) (any, error) {
			c, err := requireAdminClient(inv)
			if err != nil {
				return nil, err
			}
			resp, err := c.AdminWorkerService().PurgeExpiredRegistrationKeys(inv.Context, &leapmuxv1.PurgeExpiredRegistrationKeysRequest{})
			if err != nil {
				return nil, err
			}
			return adminPurgeResult{Purged: resp.GetPurged()}, nil
		},
	})
}

type adminOAuthProviderRow struct {
	ID           string `json:"id" table:"ID,short"`
	Type         string `json:"type" table:"TYPE"`
	Name         string `json:"name" table:"NAME"`
	IssuerURL    string `json:"issuer_url"`
	ClientID     string `json:"client_id" table:"CLIENT ID"`
	Scopes       string `json:"scopes"`
	TrustEmail   bool   `json:"trust_email"`
	ResponseMode string `json:"response_mode,omitempty"`
	Enabled      bool   `json:"enabled" table:"ENABLED"`
	CreatedAt    string `json:"created_at,omitempty"`
}

func (r adminOAuthProviderRow) QuietLines() []string { return []string{r.ID} }

type adminOAuthProviderListResult struct {
	Providers []adminOAuthProviderRow `json:"providers"`
}

func (r adminOAuthProviderListResult) QuietLines() []string {
	out := make([]string, 0, len(r.Providers))
	for _, p := range r.Providers {
		out = append(out, p.ID)
	}
	return out
}

func adminOAuthProviderRowOf(p *leapmuxv1.AdminOAuthProvider) adminOAuthProviderRow {
	return adminOAuthProviderRow{
		ID:           p.GetId(),
		Type:         p.GetProviderType(),
		Name:         p.GetName(),
		IssuerURL:    p.GetIssuerUrl(),
		ClientID:     p.GetClientId(),
		Scopes:       p.GetScopes(),
		TrustEmail:   p.GetTrustEmail(),
		ResponseMode: p.GetResponseMode(),
		Enabled:      p.GetEnabled(),
		CreatedAt:    formatTime(p.GetCreatedAt()),
	}
}

func newAdminOAuthProviderAdd() *cobra.Command {
	var providerType, name, clientID, clientSecret, issuerURL, scopes, responseMode string
	supportedTypes := slices.Sorted(maps.Keys(oauth.Presets))
	return cli.NewLeaf(cli.Leaf{
		Use:     "add",
		Short:   "Add an OAuth or OIDC sign-in provider",
		Mutates: true,
		Examples: []cli.Example{
			{Command: "leapmux admin idp add --type github --client-id Iv1.abc --client-secret s3cr3t"},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.StringVar(&providerType, "type", "", "provider type")
			cli.SetEnum(fs, "type", supportedTypes...)
			cli.MarkRequired(fs, "type")
			fs.StringVar(&name, "name", "", "display name (defaults to the preset name)")
			fs.StringVar(&clientID, "client-id", "", "OAuth client ID")
			fs.StringVar(&clientSecret, "client-secret", "", "OAuth client secret")
			fs.StringVar(&issuerURL, "issuer-url", "", "OIDC issuer URL")
			fs.StringVar(&scopes, "scopes", "", "space-separated scopes")
			fs.String("trust-email", "", "trust verified provider emails (required for generic OIDC)")
			cli.SetEnum(fs, "trust-email", "true", "false")
			fs.StringVar(&responseMode, "response-mode", "", "OAuth callback response mode (defaults to form_post for apple, query otherwise)")
			cli.SetEnum(fs, "response-mode", "query", "form_post")
			for _, f := range []string{"type", "name", "issuer-url"} {
				cli.SetFlagGroup(fs, f, "Provider")
			}
			for _, f := range []string{"client-id", "client-secret"} {
				cli.SetFlagGroup(fs, f, "Credentials")
			}
			for _, f := range []string{"scopes", "trust-email", "response-mode"} {
				cli.SetFlagGroup(fs, f, "Behavior")
			}
		},
		Run: func(inv *cli.Invocation) (any, error) {
			normalizedType := strings.TrimSpace(providerType)
			preset, ok := oauth.Presets[normalizedType]
			if !ok {
				return nil, cli.Usagef("unknown provider type %q", providerType).
					WithHint("use %s", strings.Join(supportedTypes, ", "))
			}
			if inv.Changed("response-mode") {
				parsedMode, err := oauth.ParseResponseMode(responseMode)
				if err != nil {
					return nil, cli.Wrap(cli.CodeUsage, err, "")
				}
				if preset.ProviderType == oauth.ProviderTypeGitHub && parsedMode == oauth.ResponseModeFormPost {
					return nil, cli.Usagef("form_post response_mode is only supported for OIDC providers")
				}
			}
			trustEmail, err := parseOptionalBoolFlag(inv, "trust-email")
			if err != nil {
				return nil, err
			}
			req := &leapmuxv1.AddOAuthProviderRequest{
				ProviderType: normalizedType, Name: name, ClientId: clientID,
				ClientSecret: clientSecret, IssuerUrl: issuerURL, Scopes: scopes,
				TrustEmail: trustEmail, ResponseMode: responseMode,
			}
			c, err := requireAdminClient(inv)
			if err != nil {
				return nil, err
			}
			resp, err := c.AdminIdPService().AddOAuthProvider(inv.Context, req)
			if err != nil {
				return nil, err
			}
			return adminOAuthProviderRowOf(resp.GetProvider()), nil
		},
	})
}

func newAdminOAuthProviderList() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the sign-in providers",
		Examples: []cli.Example{
			{Command: "leapmux admin idp list"},
		},
		Run: func(inv *cli.Invocation) (any, error) {
			c, err := requireAdminClient(inv)
			if err != nil {
				return nil, err
			}
			resp, err := c.AdminIdPService().ListOAuthProviders(inv.Context, &leapmuxv1.ListOAuthProvidersRequest{})
			if err != nil {
				return nil, err
			}
			providers := make([]adminOAuthProviderRow, 0, len(resp.GetProviders()))
			for _, p := range resp.GetProviders() {
				providers = append(providers, adminOAuthProviderRowOf(p))
			}
			return adminOAuthProviderListResult{Providers: providers}, nil
		},
	})
}

type adminIdPRemoveResult struct {
	Removed        string `json:"removed"`
	LockedOutUsers int64  `json:"locked_out_users"`
}

func (r adminIdPRemoveResult) QuietLines() []string { return []string{r.Removed} }

// newAdminOAuthProviderRemove builds `admin idp remove`.
func newAdminOAuthProviderRemove() *cobra.Command {
	var force bool
	return cli.NewLeaf(cli.Leaf{
		Use:         "remove <idp>",
		Aliases:     []string{"rm"},
		Short:       "Remove a sign-in provider",
		Mutates:     true,
		Destructive: true,
		Examples: []cli.Example{
			{Command: "leapmux admin idp remove 3hKd9s"},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.BoolVar(&force, "force", false, "required to remove a provider that is the last login method of an account")
		},
		Complete: completeAdminIdPs,
		Run: func(inv *cli.Invocation) (any, error) {
			id, err := requireNonEmptyArg(inv, "idp")
			if err != nil {
				return nil, err
			}
			c, err := requireAdminClient(inv)
			if err != nil {
				return nil, err
			}
			resolved, err := resolveAdminIdPID(inv.Context, c, id)
			if err != nil {
				return nil, err
			}
			resp, err := c.AdminIdPService().RemoveOAuthProvider(inv.Context, &leapmuxv1.RemoveOAuthProviderRequest{
				Id: resolved, Force: force,
			})
			if err != nil {
				return nil, err
			}
			return adminIdPRemoveResult{Removed: resolved, LockedOutUsers: resp.GetLockedOutUsers()}, nil
		},
	})
}

type adminIdPEnabledResult struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
}

func (r adminIdPEnabledResult) QuietLines() []string { return []string{r.ID} }

// newAdminOAuthProviderSetEnabled builds `admin idp enable|disable`.
func newAdminOAuthProviderSetEnabled(enabled bool, use, short string, examples []cli.Example) *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:      use,
		Short:    short,
		Examples: examples,
		Mutates:  true,
		Complete: completeAdminIdPs,
		Run: func(inv *cli.Invocation) (any, error) {
			id, err := requireNonEmptyArg(inv, "idp")
			if err != nil {
				return nil, err
			}
			c, err := requireAdminClient(inv)
			if err != nil {
				return nil, err
			}
			resolved, err := resolveAdminIdPID(inv.Context, c, id)
			if err != nil {
				return nil, err
			}
			if _, err := c.AdminIdPService().SetOAuthProviderEnabled(inv.Context, &leapmuxv1.SetOAuthProviderEnabledRequest{
				Id: resolved, Enabled: enabled,
			}); err != nil {
				return nil, err
			}
			return adminIdPEnabledResult{ID: resolved, Enabled: enabled}, nil
		},
	})
}

type adminIdPRotateSecretResult struct {
	ID string `json:"id"`
}

func (r adminIdPRotateSecretResult) QuietLines() []string { return []string{r.ID} }

// newAdminOAuthProviderRotateSecret builds `admin idp rotate-secret`.
func newAdminOAuthProviderRotateSecret() *cobra.Command {
	var clientSecret string
	return cli.NewLeaf(cli.Leaf{
		Use:     "rotate-secret <idp>",
		Short:   "Replace a sign-in provider's client secret without removing linked users",
		Mutates: true,
		Examples: []cli.Example{
			{Command: "leapmux admin idp rotate-secret 3hKd9s --client-secret new-s3cr3t"},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.StringVar(&clientSecret, "client-secret", "", "new OAuth client secret")
			cli.MarkRequired(fs, "client-secret")
		},
		Complete: completeAdminIdPs,
		Run: func(inv *cli.Invocation) (any, error) {
			id, err := requireNonEmptyArg(inv, "idp")
			if err != nil {
				return nil, err
			}
			if strings.TrimSpace(clientSecret) == "" {
				return nil, cli.Usagef("--client-secret is empty")
			}
			c, err := requireAdminClient(inv)
			if err != nil {
				return nil, err
			}
			resolved, err := resolveAdminIdPID(inv.Context, c, id)
			if err != nil {
				return nil, err
			}
			if _, err := c.AdminIdPService().UpdateOAuthProviderSecret(inv.Context, &leapmuxv1.UpdateOAuthProviderSecretRequest{
				Id: resolved, ClientSecret: clientSecret,
			}); err != nil {
				return nil, err
			}
			return adminIdPRotateSecretResult{ID: resolved}, nil
		},
	})
}
