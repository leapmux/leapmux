package cmd

import (
	"context"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"google.golang.org/protobuf/types/known/timestamppb"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/hub/service"
)

// CredentialLifetime holds the identity, owner, and timestamps of each
// bearer token. Both token listings report whether the owner was deleted.
type CredentialLifetime struct {
	ID           string `json:"id" table:"ID,short"`
	UserID       string `json:"user_id"`
	Username     string `json:"username" table:"USERNAME"`
	OwnerDeleted bool   `json:"owner_deleted"`
	CreatedAt    string `json:"created_at,omitempty"`
	LastUsedAt   string `json:"last_used_at,omitempty"`
	ExpiresAt    string `json:"expires_at,omitempty" table:"EXPIRES,dim"`
	RevokedAt    string `json:"revoked_at,omitempty"`
}

func tokenLifecycle(
	id, userID, username string,
	ownerDeleted bool,
	createdAt, lastUsedAt, expiresAt, revokedAt *timestamppb.Timestamp,
) CredentialLifetime {
	return CredentialLifetime{
		ID: id, UserID: userID, Username: username, OwnerDeleted: ownerDeleted,
		CreatedAt: formatTime(createdAt), LastUsedAt: formatTime(lastUsedAt),
		ExpiresAt: formatTime(expiresAt), RevokedAt: formatTime(revokedAt),
	}
}

type adminAPITokenRow struct {
	CredentialLifetime
	ClientID         string   `json:"client_id"`
	ClientName       string   `json:"client_name" table:"APP"`
	InstallationName string   `json:"installation_name" table:"INSTALLATION"`
	GrantedScopes    []string `json:"granted_scopes"`
}

func adminAPITokenRowOf(token *leapmuxv1.AdminAPIToken) adminAPITokenRow {
	return adminAPITokenRow{
		CredentialLifetime: tokenLifecycle(token.GetId(), token.GetUserId(), token.GetUsername(), token.GetOwnerDeleted(),
			token.GetCreatedAt(), token.GetLastUsedAt(), token.GetExpiresAt(), token.GetRevokedAt()),
		ClientID: token.GetClientId(), ClientName: token.GetClientName(),
		InstallationName: token.GetInstallationName(), GrantedScopes: nonNilStrings(token.GetGrantedScopes()),
	}
}

type adminDelegationTokenRow struct {
	CredentialLifetime
	WorkerID string `json:"worker_id" table:"WORKER,short"`
}

func adminDelegationTokenRowOf(token *leapmuxv1.AdminDelegationToken) adminDelegationTokenRow {
	return adminDelegationTokenRow{
		CredentialLifetime: tokenLifecycle(token.GetId(), token.GetUserId(), token.GetUsername(), token.GetOwnerDeleted(),
			token.GetCreatedAt(), token.GetLastUsedAt(), token.GetExpiresAt(), token.GetRevokedAt()),
		WorkerID: token.GetWorkerId(),
	}
}

type adminAPITokenListResult struct {
	Tokens     []adminAPITokenRow `json:"tokens"`
	NextCursor string             `json:"next_cursor"`
}

func (r adminAPITokenListResult) QuietLines() []string {
	out := make([]string, 0, len(r.Tokens))
	for _, t := range r.Tokens {
		out = append(out, t.ID)
	}
	return out
}

type adminDelegationTokenListResult struct {
	Tokens     []adminDelegationTokenRow `json:"tokens"`
	NextCursor string                    `json:"next_cursor"`
}

func (r adminDelegationTokenListResult) QuietLines() []string {
	out := make([]string, 0, len(r.Tokens))
	for _, t := range r.Tokens {
		out = append(out, t.ID)
	}
	return out
}

func resolveAdminAPITokenID(ctx context.Context, c *control.HubClient, raw string) (string, error) {
	return resolveLiveThenAllPagedAdminID(ctx, "API token", raw, true,
		func(ctx context.Context, includeRevoked bool, idPrefix string, page *leapmuxv1.PageRequest) ([]*leapmuxv1.AdminAPIToken, *leapmuxv1.PageResponse, error) {
			resp, err := c.AdminUserService().ListAPITokens(ctx, &leapmuxv1.ListAPITokensRequest{
				IncludeRevoked: includeRevoked,
				IdPrefix:       idPrefix,
				Page:           page,
			})
			return resp.GetTokens(), resp.GetPage(), err
		},
		(*leapmuxv1.AdminAPIToken).GetId,
		func(t *leapmuxv1.AdminAPIToken) cli.Candidate {
			return cli.Candidate{ID: t.GetId(), Kind: "api-token", Title: t.GetInstallationName()}
		},
	)
}

func resolveAdminDelegationTokenID(ctx context.Context, c *control.HubClient, raw string) (string, error) {
	return resolveLiveThenAllPagedAdminID(ctx, "delegation token", raw, false,
		func(ctx context.Context, includeRevoked bool, idPrefix string, page *leapmuxv1.PageRequest) ([]*leapmuxv1.AdminDelegationToken, *leapmuxv1.PageResponse, error) {
			resp, err := c.AdminUserService().ListDelegationTokens(ctx, &leapmuxv1.ListDelegationTokensRequest{
				IncludeRevoked: includeRevoked,
				IdPrefix:       idPrefix,
				Page:           page,
			})
			return resp.GetTokens(), resp.GetPage(), err
		},
		(*leapmuxv1.AdminDelegationToken).GetId,
		func(t *leapmuxv1.AdminDelegationToken) cli.Candidate {
			return cli.Candidate{ID: t.GetId(), Kind: "delegation-token", Title: t.GetUsername()}
		},
	)
}

// newAdminAPITokenList builds `admin api-token list`.
func newAdminAPITokenList() *cobra.Command {
	var page adminPageFlags
	var owner userFilterFlags
	var clientID string
	var includeRevoked bool
	return cli.NewLeaf(cli.Leaf{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List API tokens",
		Examples: []cli.Example{
			{Command: "leapmux admin api-token list"},
			{Comment: "List one user's tokens.", Command: "leapmux admin api-token list --user alice"},
		},
		Flags: func(fs *pflag.FlagSet) {
			page.bind(fs)
			owner.bind(fs)
			fs.StringVar(&clientID, "client-id", "", "filter by app (empty = all apps)")
			fs.BoolVar(&includeRevoked, "include-revoked", false, "include revoked tokens (forensics; default lists live tokens only)")
		},
		Run: func(inv *cli.Invocation) (any, error) {
			if err := page.validate(); err != nil {
				return nil, err
			}
			ownerTarget, err := owner.parse(inv)
			if err != nil {
				return nil, err
			}
			c, err := requireAdminClient(inv)
			if err != nil {
				return nil, err
			}
			resp, err := callWithAdminUserTarget(inv.Context, c, ownerTarget, func(sel *leapmuxv1.UserSelector) (*leapmuxv1.ListAPITokensResponse, error) {
				return c.AdminUserService().ListAPITokens(inv.Context, &leapmuxv1.ListAPITokensRequest{
					User: sel, ClientId: clientID,
					IncludeRevoked: includeRevoked, Page: page.request(),
				})
			})
			if err != nil {
				return nil, err
			}
			rows := make([]adminAPITokenRow, 0, len(resp.GetTokens()))
			for _, t := range resp.GetTokens() {
				rows = append(rows, adminAPITokenRowOf(t))
			}
			return adminAPITokenListResult{Tokens: rows, NextCursor: resp.GetPage().GetNextCursor()}, nil
		},
	})
}

type adminAPITokenIssueResult struct {
	TokenID       string   `json:"token_id"`
	AccessToken   string   `json:"access_token"`
	RefreshToken  string   `json:"refresh_token"`
	ClientID      string   `json:"client_id"`
	GrantedScopes []string `json:"granted_scopes"`
	Note          string   `json:"note"`
}

func (r adminAPITokenIssueResult) QuietLines() []string { return []string{r.TokenID} }

// newAdminAPITokenIssue builds `admin api-token issue`.
func newAdminAPITokenIssue() *cobra.Command {
	var clientID, installationName, scopeFlag string
	var ttlSeconds int64
	return cli.NewLeaf(cli.Leaf{
		Use:     "issue <user>",
		Short:   "Issue an API token for a script or a service account",
		Long:    "Issue an API token that <user> owns.",
		Mutates: true,
		Examples: []cli.Example{
			{Command: `leapmux admin api-token issue ci-bot --installation-name ci-runner-3 --scope "workspace:read"`},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.StringVar(&clientID, "client-id", "",
				"the app this credential belongs to (empty = the built-in service-account registration)")
			fs.StringVar(&installationName, "installation-name", "",
				"human-visible name for this installation, e.g. ci-runner-3")
			cli.MarkRequired(fs, "installation-name")
			fs.Int64Var(&ttlSeconds, "ttl", 0,
				"fixed lifetime in seconds, with no refresh token (0 = the renewing credential: 1h access + refresh)")
			fs.StringVar(&scopeFlag, "scope", "",
				"space- or comma-separated permissions, e.g. \"file:read git:read\" (empty = everything except admin:*)")
		},
		Complete: completeAdminUsers,
		Run: func(inv *cli.Invocation) (any, error) {
			user, err := parseAdminUserTarget(inv.Arg("user"))
			if err != nil {
				return nil, err
			}
			if strings.TrimSpace(installationName) == "" {
				return nil, cli.Usagef("--installation-name is empty")
			}
			if ttlSeconds < 0 || ttlSeconds > service.MaxAPITokenTTLSeconds {
				return nil, cli.Usagef("--ttl must be between 0 and %d (got %d)", service.MaxAPITokenTTLSeconds, ttlSeconds)
			}
			scopeList, err := splitScopeFlag(scopeFlag)
			if err != nil {
				return nil, err
			}
			c, err := requireAdminClient(inv)
			if err != nil {
				return nil, err
			}
			resp, err := callWithAdminUserTarget(inv.Context, c, user, func(sel *leapmuxv1.UserSelector) (*leapmuxv1.IssueAPITokenResponse, error) {
				return c.AdminUserService().IssueAPIToken(inv.Context, &leapmuxv1.IssueAPITokenRequest{
					User: sel, ClientId: clientID,
					InstallationName: installationName,
					TtlSeconds:       ttlSeconds, Scopes: scopeList,
				})
			})
			if err != nil {
				return nil, err
			}
			return adminAPITokenIssueResult{
				TokenID: resp.GetTokenId(), AccessToken: resp.GetAccessToken(), RefreshToken: resp.GetRefreshToken(),
				ClientID: resp.GetClientId(), GrantedScopes: nonNilStrings(resp.GetGrantedScopes()),
				Note: "Capture the tokens now. They cannot be retrieved later.",
			}, nil
		},
	})
}

// newAdminAPITokenRevoke builds `admin api-token revoke`.
func newAdminAPITokenRevoke() *cobra.Command {
	return newAdminRevokeLeaf(adminRevokeLeafConfig{
		argName:  "token",
		short:    "Revoke an API token",
		example:  "leapmux admin api-token revoke 7Qx2Lm",
		complete: completeAdminAPITokens,
		resolve:  resolveAdminAPITokenID,
		revoke: func(ctx context.Context, c *control.HubClient, id string) error {
			_, err := c.AdminUserService().RevokeAPIToken(ctx, &leapmuxv1.RevokeAPITokenRequest{Id: id})
			return err
		},
	})
}

// newAdminDelegationTokenList builds `admin delegation-token list`.
func newAdminDelegationTokenList() *cobra.Command {
	var page adminPageFlags
	var owner userFilterFlags
	var includeRevoked bool
	return cli.NewLeaf(cli.Leaf{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the delegation tokens that workers minted",
		Examples: []cli.Example{
			{Command: "leapmux admin delegation-token list"},
		},
		Flags: func(fs *pflag.FlagSet) {
			page.bind(fs)
			owner.bind(fs)
			fs.BoolVar(&includeRevoked, "include-revoked", false, "include revoked tokens (forensics; default lists live tokens only)")
		},
		Run: func(inv *cli.Invocation) (any, error) {
			if err := page.validate(); err != nil {
				return nil, err
			}
			ownerTarget, err := owner.parse(inv)
			if err != nil {
				return nil, err
			}
			c, err := requireAdminClient(inv)
			if err != nil {
				return nil, err
			}
			resp, err := callWithAdminUserTarget(inv.Context, c, ownerTarget, func(sel *leapmuxv1.UserSelector) (*leapmuxv1.ListDelegationTokensResponse, error) {
				return c.AdminUserService().ListDelegationTokens(inv.Context, &leapmuxv1.ListDelegationTokensRequest{
					User: sel, IncludeRevoked: includeRevoked, Page: page.request(),
				})
			})
			if err != nil {
				return nil, err
			}
			rows := make([]adminDelegationTokenRow, 0, len(resp.GetTokens()))
			for _, t := range resp.GetTokens() {
				rows = append(rows, adminDelegationTokenRowOf(t))
			}
			return adminDelegationTokenListResult{Tokens: rows, NextCursor: resp.GetPage().GetNextCursor()}, nil
		},
	})
}

// newAdminDelegationTokenRevoke builds `admin delegation-token revoke`.
func newAdminDelegationTokenRevoke() *cobra.Command {
	return newAdminRevokeLeaf(adminRevokeLeafConfig{
		argName:  "token",
		short:    "Revoke a delegation token",
		example:  "leapmux admin delegation-token revoke 7Qx2Lm",
		complete: completeAdminDelegationTokens,
		resolve:  resolveAdminDelegationTokenID,
		revoke: func(ctx context.Context, c *control.HubClient, id string) error {
			_, err := c.AdminUserService().RevokeDelegationToken(ctx, &leapmuxv1.AdminUserServiceRevokeDelegationTokenRequest{Id: id})
			return err
		},
	})
}

// splitScopeFlag splits a --scope value into its tokens.
func splitScopeFlag(raw string) ([]string, error) {
	tokens := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	if raw != "" && len(tokens) == 0 {
		return nil, cli.Usagef("--scope carried no permission names; pass real tokens or omit the flag for the default")
	}
	return tokens, nil
}
