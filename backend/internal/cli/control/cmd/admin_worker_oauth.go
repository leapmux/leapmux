package cmd

import (
	"context"
	"errors"
	"flag"
	"strconv"

	"connectrpc.com/connect"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/control"
)

// Flag names, defaults, and validation messages carry over verbatim from
// the old offline admin verbs; output uses the control JSON envelope.

func RunAdminWorkerList(rawCtx any, args []string) error {
	var page adminPageFlags
	var userID, username, status string
	var statusEnum leapmuxv1.WorkerStatus
	return adminVerb(rawCtx, args, adminVerbSpec{
		Page: &page,
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&userID, "user-id", "", "filter by user ID")
			fs.StringVar(&username, "username", "", "filter by username")
			fs.StringVar(&status, "status", "active", "worker status filter (active, deregistering, deleted, all)")
		},
		BeforeDial: func(adminArgs) error {
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
				return control.EmitError("invalid_request", "unknown worker status: "+status+" (use: active, deregistering, deleted, all)")
			}
			return nil
		},
		Run: func(c *control.Client, _ adminArgs) error {
			resp, err := c.AdminWorkerService().ListWorkers(context.Background(), connect.NewRequest(&leapmuxv1.AdminWorkerServiceListWorkersRequest{
				UserId: userID, Username: username, Status: statusEnum, Limit: page.Limit, Cursor: page.Cursor,
			}))
			if err != nil {
				return adminRPCError("rpc_failed", err)
			}
			rows := make([]map[string]any, 0, len(resp.Msg.GetWorkers()))
			for _, w := range resp.Msg.GetWorkers() {
				rows = append(rows, adminWorkerJSON(w))
			}
			return control.EmitData(map[string]any{"workers": rows, "next_cursor": resp.Msg.GetNextCursor()})
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

func RunAdminWorkerGet(rawCtx any, args []string) error {
	var id string
	return adminVerb(rawCtx, args, adminVerbSpec{
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&id, "id", "", "worker ID (required)")
		},
		BeforeDial: requireFlag(&id, "id"),
		Run: func(c *control.Client, _ adminArgs) error {
			resp, err := c.AdminWorkerService().GetWorker(context.Background(), connect.NewRequest(&leapmuxv1.AdminWorkerServiceGetWorkerRequest{Id: id}))
			if err != nil {
				return adminRPCError("rpc_failed", err)
			}
			return control.EmitData(adminWorkerJSON(resp.Msg.GetWorker()))
		},
	})
}

func RunAdminWorkerDeregister(rawCtx any, args []string) error {
	var id string
	return adminVerb(rawCtx, args, adminVerbSpec{
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&id, "id", "", "worker ID (required)")
		},
		BeforeDial: requireFlag(&id, "id"),
		Run: func(c *control.Client, _ adminArgs) error {
			if _, err := c.AdminWorkerService().DeregisterWorker(context.Background(), connect.NewRequest(&leapmuxv1.AdminWorkerServiceDeregisterWorkerRequest{Id: id})); err != nil {
				return adminRPCError("deregister_failed", err)
			}
			return control.EmitData(map[string]any{"deregistered": id})
		},
	})
}

func RunAdminWorkerRegKeyList(rawCtx any, args []string) error {
	var page adminPageFlags
	var includeExpired bool
	return adminVerb(rawCtx, args, adminVerbSpec{
		Page: &page,
		Flags: func(fs *flag.FlagSet) {
			fs.BoolVar(&includeExpired, "include-expired", false, "include revoked or expired keys (forensics; default shows only live keys)")
		},
		Run: func(c *control.Client, _ adminArgs) error {
			resp, err := c.AdminWorkerService().ListRegistrationKeys(context.Background(), connect.NewRequest(&leapmuxv1.ListRegistrationKeysRequest{
				IncludeExpired: includeExpired, Limit: page.Limit, Cursor: page.Cursor,
			}))
			if err != nil {
				return adminRPCError("rpc_failed", err)
			}
			rows := make([]map[string]any, 0, len(resp.Msg.GetKeys()))
			for _, k := range resp.Msg.GetKeys() {
				rows = append(rows, adminRegistrationKeyJSON(k))
			}
			return control.EmitData(map[string]any{"keys": rows, "next_cursor": resp.Msg.GetNextCursor()})
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

func RunAdminWorkerRegKeyRevoke(rawCtx any, args []string) error {
	var id string
	return adminVerb(rawCtx, args, adminVerbSpec{
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&id, "id", "", "registration key ID (required)")
		},
		BeforeDial: requireFlag(&id, "id"),
		Run: func(c *control.Client, _ adminArgs) error {
			if _, err := c.AdminWorkerService().RevokeRegistrationKey(context.Background(), connect.NewRequest(&leapmuxv1.RevokeRegistrationKeyRequest{Id: id})); err != nil {
				return adminRPCError("revoke_failed", err)
			}
			return control.EmitData(map[string]any{"revoked": id})
		},
	})
}

func RunAdminWorkerRegKeyPurgeExpired(rawCtx any, args []string) error {
	return adminVerb(rawCtx, args, adminVerbSpec{
		Run: func(c *control.Client, _ adminArgs) error {
			resp, err := c.AdminWorkerService().PurgeExpiredRegistrationKeys(context.Background(), connect.NewRequest(&leapmuxv1.PurgeExpiredRegistrationKeysRequest{}))
			if err != nil {
				return adminRPCError("purge_failed", err)
			}
			return control.EmitData(map[string]any{"purged": resp.Msg.GetPurged()})
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

func RunAdminOAuthProviderAdd(rawCtx any, args []string) error {
	var providerType, name, clientID, clientSecret, issuerURL, scopes string
	var trustEmail string
	var req *leapmuxv1.AddOAuthProviderRequest
	return adminVerb(rawCtx, args, adminVerbSpec{
		Flags: func(fs *flag.FlagSet) {
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
					return control.EmitError("invalid_request", err.Error())
				}
				req.TrustEmail = &b
			}
			return nil
		},
		Run: func(c *control.Client, _ adminArgs) error {
			resp, err := c.AdminOAuthService().AddOAuthProvider(context.Background(), connect.NewRequest(req))
			if err != nil {
				return adminRPCError("add_failed", err)
			}
			return control.EmitData(adminOAuthProviderJSON(resp.Msg.GetProvider()))
		},
	})
}

func RunAdminOAuthProviderList(rawCtx any, args []string) error {
	return adminVerb(rawCtx, args, adminVerbSpec{
		Run: func(c *control.Client, _ adminArgs) error {
			resp, err := c.AdminOAuthService().ListOAuthProviders(context.Background(), connect.NewRequest(&leapmuxv1.ListOAuthProvidersRequest{}))
			if err != nil {
				return adminRPCError("rpc_failed", err)
			}
			providers := make([]map[string]any, 0, len(resp.Msg.GetProviders()))
			for _, p := range resp.Msg.GetProviders() {
				providers = append(providers, adminOAuthProviderJSON(p))
			}
			return control.EmitData(providers)
		},
	})
}

// RunAdminOAuthProviderRemove implements `control admin oauth-provider
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
func RunAdminOAuthProviderRemove(rawCtx any, args []string) error {
	var id string
	var force bool
	return adminVerb(rawCtx, args, adminVerbSpec{
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&id, "id", "", "provider ID")
			fs.BoolVar(&force, "force", false, "required to remove a provider that is the last login method of an account")
		},
		BeforeDial: requireFlag(&id, "id"),
		Run: func(c *control.Client, _ adminArgs) error {
			resp, err := c.AdminOAuthService().RemoveOAuthProvider(context.Background(), connect.NewRequest(&leapmuxv1.RemoveOAuthProviderRequest{
				Id: id, Force: force,
			}))
			if err != nil {
				return adminRPCError("remove_failed", err)
			}
			return control.EmitData(map[string]any{"removed": id, "locked_out_users": resp.Msg.GetLockedOutUsers()})
		},
	})
}

// RunAdminOAuthProviderSetEnabled implements `control admin oauth-provider
// enable|disable`.
func RunAdminOAuthProviderSetEnabled(rawCtx any, args []string, enabled bool) error {
	var id string
	return adminVerb(rawCtx, args, adminVerbSpec{
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&id, "id", "", "provider ID")
		},
		BeforeDial: requireFlag(&id, "id"),
		Run: func(c *control.Client, _ adminArgs) error {
			if _, err := c.AdminOAuthService().SetOAuthProviderEnabled(context.Background(), connect.NewRequest(&leapmuxv1.SetOAuthProviderEnabledRequest{
				Id: id, Enabled: enabled,
			})); err != nil {
				return adminRPCError("update_failed", err)
			}
			return control.EmitData(map[string]any{"id": id, "enabled": enabled})
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
