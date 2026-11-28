package cmd

import (
	"context"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/hub/ratelimit"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
)

// adminCompletionClient builds a short-lived admin client for shell completion.
// Admin commands never run inside a LeapMux tab, so completion degrades cleanly
// to no candidates when LEAPMUX_CONTROL_SOCK is set or no Hub is configured.
func adminCompletionClient(cmd *cobra.Command) (*control.HubClient, context.Context, context.CancelFunc, bool) {
	if os.Getenv(controlenv.SocketVariable) != "" || blankTargetFlag(cmd, "hub") {
		return nil, nil, nil, false
	}
	hub, err := hubURLFor(cmd)
	if err != nil {
		return nil, nil, nil, false
	}
	c, err := control.NewClientOrAnonymous(hub)
	if err != nil {
		return nil, nil, nil, false
	}
	ctx, cancel := completionContext(cmd)
	return c, ctx, cancel, true
}

type completionItem struct {
	value string
	desc  string
}

func completeAdminItems(
	cmd *cobra.Command,
	toComplete string,
	fetch func(ctx context.Context, c *control.HubClient) ([]completionItem, error),
) ([]string, cobra.ShellCompDirective) {
	c, ctx, cancel, ok := adminCompletionClient(cmd)
	if !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	defer cancel()
	items, err := fetch(ctx, c)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		val := cli.SanitizeSingleLine(item.value)
		if val != "" && strings.HasPrefix(val, toComplete) {
			out = append(out, formatCompletionCandidate(val, item.desc))
		}
	}
	slices.Sort(out)
	return out, cobra.ShellCompDirectiveNoFileComp
}

func completeAdminList[T any](
	cmd *cobra.Command,
	toComplete string,
	list func(ctx context.Context, c *control.HubClient) ([]T, error),
	itemOf func(T) (value, desc string),
) ([]string, cobra.ShellCompDirective) {
	return completeAdminItems(cmd, toComplete, func(ctx context.Context, c *control.HubClient) ([]completionItem, error) {
		rows, err := list(ctx, c)
		if err != nil {
			return nil, err
		}
		items := make([]completionItem, 0, len(rows))
		for _, row := range rows {
			val, desc := itemOf(row)
			items = append(items, completionItem{value: val, desc: desc})
		}
		return items, nil
	})
}

func completeRateLimitOperations(_ *cobra.Command, _ cli.ArgSpec, toComplete string) ([]string, cobra.ShellCompDirective) {
	var out []string
	for _, op := range ratelimit.KnownOperations() {
		s := string(op)
		if strings.HasPrefix(s, toComplete) {
			out = append(out, formatCompletionCandidate(s, ""))
		}
	}
	slices.Sort(out)
	return out, cobra.ShellCompDirectiveNoFileComp
}

func completeAdminSettingsKeys(cmd *cobra.Command, _ cli.ArgSpec, toComplete string) ([]string, cobra.ShellCompDirective) {
	return completeAdminItems(cmd, toComplete, func(ctx context.Context, c *control.HubClient) ([]completionItem, error) {
		resp, err := c.AdminSettingsService().ListSettings(ctx, &leapmuxv1.ListSettingsRequest{})
		if err != nil {
			return nil, err
		}
		summaries := make(map[string]string, len(resp.GetDescriptors()))
		for _, d := range resp.GetDescriptors() {
			summaries[d.GetKey()] = d.GetSummary()
		}
		items := make([]completionItem, 0, len(resp.GetValues()))
		for _, v := range resp.GetValues() {
			items = append(items, completionItem{value: v.GetKey(), desc: summaries[v.GetKey()]})
		}
		return items, nil
	})
}

func completeAdminUsers(cmd *cobra.Command, _ cli.ArgSpec, toComplete string) ([]string, cobra.ShellCompDirective) {
	return completeAdminItems(cmd, toComplete, func(ctx context.Context, c *control.HubClient) ([]completionItem, error) {
		resp, err := c.AdminUserService().ListUsers(ctx, &leapmuxv1.ListUsersRequest{})
		if err != nil {
			return nil, err
		}
		items := make([]completionItem, 0, len(resp.GetUsers())*2)
		for _, u := range resp.GetUsers() {
			if u.GetUsername() != "" {
				items = append(items, completionItem{value: u.GetUsername(), desc: u.GetDisplayName()})
			}
			if u.GetId() != "" {
				items = append(items, completionItem{value: "id:" + u.GetId(), desc: u.GetUsername()})
			}
		}
		return items, nil
	})
}

func completeAdminSessions(cmd *cobra.Command, _ cli.ArgSpec, toComplete string) ([]string, cobra.ShellCompDirective) {
	return completeAdminList(cmd, toComplete,
		func(ctx context.Context, c *control.HubClient) ([]*leapmuxv1.AdminSession, error) {
			resp, err := c.AdminUserService().ListSessions(ctx, &leapmuxv1.ListSessionsRequest{})
			return resp.GetSessions(), err
		},
		func(s *leapmuxv1.AdminSession) (string, string) { return s.GetId(), s.GetUsername() },
	)
}

func completeAdminWorkers(cmd *cobra.Command, _ cli.ArgSpec, toComplete string) ([]string, cobra.ShellCompDirective) {
	return completeAdminList(cmd, toComplete,
		func(ctx context.Context, c *control.HubClient) ([]*leapmuxv1.AdminWorker, error) {
			resp, err := c.AdminWorkerService().ListWorkers(ctx, &leapmuxv1.AdminWorkerServiceListWorkersRequest{})
			return resp.GetWorkers(), err
		},
		func(w *leapmuxv1.AdminWorker) (string, string) { return w.GetId(), w.GetOwnerUsername() },
	)
}

func completeAdminRegKeys(cmd *cobra.Command, _ cli.ArgSpec, toComplete string) ([]string, cobra.ShellCompDirective) {
	return completeAdminList(cmd, toComplete,
		func(ctx context.Context, c *control.HubClient) ([]*leapmuxv1.AdminRegistrationKey, error) {
			resp, err := c.AdminWorkerService().ListRegistrationKeys(ctx, &leapmuxv1.ListRegistrationKeysRequest{})
			return resp.GetKeys(), err
		},
		func(k *leapmuxv1.AdminRegistrationKey) (string, string) { return k.GetId(), k.GetCreatorUsername() },
	)
}

func completeAdminApps(cmd *cobra.Command, _ cli.ArgSpec, toComplete string) ([]string, cobra.ShellCompDirective) {
	return completeAdminList(cmd, toComplete,
		func(ctx context.Context, c *control.HubClient) ([]*leapmuxv1.App, error) {
			resp, err := c.AppService().ListApps(ctx, &leapmuxv1.ListAppsRequest{})
			return resp.GetApps(), err
		},
		func(app *leapmuxv1.App) (string, string) { return app.GetClientId(), app.GetClientName() },
	)
}

func completeAdminIdPs(cmd *cobra.Command, _ cli.ArgSpec, toComplete string) ([]string, cobra.ShellCompDirective) {
	return completeAdminList(cmd, toComplete,
		func(ctx context.Context, c *control.HubClient) ([]*leapmuxv1.AdminOAuthProvider, error) {
			resp, err := c.AdminIdPService().ListOAuthProviders(ctx, &leapmuxv1.ListOAuthProvidersRequest{})
			return resp.GetProviders(), err
		},
		func(p *leapmuxv1.AdminOAuthProvider) (string, string) { return p.GetId(), p.GetName() },
	)
}

func completeAdminAPITokens(cmd *cobra.Command, _ cli.ArgSpec, toComplete string) ([]string, cobra.ShellCompDirective) {
	return completeAdminList(cmd, toComplete,
		func(ctx context.Context, c *control.HubClient) ([]*leapmuxv1.AdminAPIToken, error) {
			resp, err := c.AdminUserService().ListAPITokens(ctx, &leapmuxv1.ListAPITokensRequest{})
			return resp.GetTokens(), err
		},
		func(tok *leapmuxv1.AdminAPIToken) (string, string) { return tok.GetId(), tok.GetInstallationName() },
	)
}

func completeAdminDelegationTokens(cmd *cobra.Command, _ cli.ArgSpec, toComplete string) ([]string, cobra.ShellCompDirective) {
	return completeAdminList(cmd, toComplete,
		func(ctx context.Context, c *control.HubClient) ([]*leapmuxv1.AdminDelegationToken, error) {
			resp, err := c.AdminUserService().ListDelegationTokens(ctx, &leapmuxv1.ListDelegationTokensRequest{})
			return resp.GetTokens(), err
		},
		func(tok *leapmuxv1.AdminDelegationToken) (string, string) { return tok.GetId(), tok.GetUsername() },
	)
}
