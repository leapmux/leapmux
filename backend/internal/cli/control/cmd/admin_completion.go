package cmd

import (
	"context"
	"os"
	"slices"
	"strings"
	"unicode"

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
func adminCompletionClient(cmd *cobra.Command) (*control.Client, context.Context, context.CancelFunc, bool) {
	if os.Getenv(controlenv.SocketVariable) != "" || blankTargetFlag(cmd, "hub") {
		return nil, nil, nil, false
	}
	hub, err := hubURLFor(cmd)
	if err != nil {
		return nil, nil, nil, false
	}
	c, err := control.NewClientOrAnonymous(hub)
	if err != nil || c.IsWorkerIPC() {
		return nil, nil, nil, false
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), completionTimeout)
	return c, ctx, cancel, true
}

func formatCompletionCandidate(value, desc string) string {
	if desc == "" {
		return value
	}
	return value + "\t" + strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, desc)
}

func completeRateLimitOperations(_ *cobra.Command, _ cli.ArgSpec, toComplete string) ([]string, cobra.ShellCompDirective) {
	var out []string
	for _, op := range ratelimit.KnownOperations() {
		s := string(op)
		if strings.HasPrefix(s, toComplete) {
			out = append(out, s)
		}
	}
	slices.Sort(out)
	return out, cobra.ShellCompDirectiveNoFileComp
}

func completeAdminSettingsKeys(cmd *cobra.Command, _ cli.ArgSpec, toComplete string) ([]string, cobra.ShellCompDirective) {
	c, ctx, cancel, ok := adminCompletionClient(cmd)
	if !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	defer cancel()
	resp, err := c.AdminSettingsService().ListSettings(ctx, &leapmuxv1.ListSettingsRequest{})
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	summaries := map[string]string{}
	for _, d := range resp.GetDescriptors() {
		summaries[d.GetKey()] = d.GetSummary()
	}
	var out []string
	for _, v := range resp.GetValues() {
		if strings.HasPrefix(v.GetKey(), toComplete) {
			out = append(out, formatCompletionCandidate(v.GetKey(), summaries[v.GetKey()]))
		}
	}
	slices.Sort(out)
	return out, cobra.ShellCompDirectiveNoFileComp
}

func completeAdminUsers(cmd *cobra.Command, _ cli.ArgSpec, toComplete string) ([]string, cobra.ShellCompDirective) {
	c, ctx, cancel, ok := adminCompletionClient(cmd)
	if !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	defer cancel()
	resp, err := c.AdminUserService().ListUsers(ctx, &leapmuxv1.ListUsersRequest{})
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	for _, u := range resp.GetUsers() {
		if strings.HasPrefix(u.GetUsername(), toComplete) {
			out = append(out, formatCompletionCandidate(u.GetUsername(), u.GetDisplayName()))
		}
		idForm := "id:" + u.GetId()
		if strings.HasPrefix(idForm, toComplete) {
			out = append(out, formatCompletionCandidate(idForm, u.GetUsername()))
		}
	}
	slices.Sort(out)
	return out, cobra.ShellCompDirectiveNoFileComp
}

func completeAdminSessions(cmd *cobra.Command, _ cli.ArgSpec, toComplete string) ([]string, cobra.ShellCompDirective) {
	c, ctx, cancel, ok := adminCompletionClient(cmd)
	if !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	defer cancel()
	resp, err := c.AdminUserService().ListSessions(ctx, &leapmuxv1.ListSessionsRequest{})
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	for _, s := range resp.GetSessions() {
		if strings.HasPrefix(s.GetId(), toComplete) {
			out = append(out, formatCompletionCandidate(s.GetId(), s.GetUsername()))
		}
	}
	slices.Sort(out)
	return out, cobra.ShellCompDirectiveNoFileComp
}

func completeAdminWorkers(cmd *cobra.Command, _ cli.ArgSpec, toComplete string) ([]string, cobra.ShellCompDirective) {
	c, ctx, cancel, ok := adminCompletionClient(cmd)
	if !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	defer cancel()
	resp, err := c.AdminWorkerService().ListWorkers(ctx, &leapmuxv1.AdminWorkerServiceListWorkersRequest{})
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	for _, w := range resp.GetWorkers() {
		if strings.HasPrefix(w.GetId(), toComplete) {
			out = append(out, formatCompletionCandidate(w.GetId(), w.GetOwnerUsername()))
		}
	}
	slices.Sort(out)
	return out, cobra.ShellCompDirectiveNoFileComp
}

func completeAdminRegKeys(cmd *cobra.Command, _ cli.ArgSpec, toComplete string) ([]string, cobra.ShellCompDirective) {
	c, ctx, cancel, ok := adminCompletionClient(cmd)
	if !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	defer cancel()
	resp, err := c.AdminWorkerService().ListRegistrationKeys(ctx, &leapmuxv1.ListRegistrationKeysRequest{})
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	for _, k := range resp.GetKeys() {
		if strings.HasPrefix(k.GetId(), toComplete) {
			out = append(out, formatCompletionCandidate(k.GetId(), k.GetCreatorUsername()))
		}
	}
	slices.Sort(out)
	return out, cobra.ShellCompDirectiveNoFileComp
}

func completeAdminApps(cmd *cobra.Command, _ cli.ArgSpec, toComplete string) ([]string, cobra.ShellCompDirective) {
	c, ctx, cancel, ok := adminCompletionClient(cmd)
	if !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	defer cancel()
	resp, err := c.AppService().ListApps(ctx, &leapmuxv1.ListAppsRequest{})
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	for _, app := range resp.GetApps() {
		if strings.HasPrefix(app.GetClientId(), toComplete) {
			out = append(out, formatCompletionCandidate(app.GetClientId(), app.GetClientName()))
		}
	}
	slices.Sort(out)
	return out, cobra.ShellCompDirectiveNoFileComp
}

func completeAdminIdPs(cmd *cobra.Command, _ cli.ArgSpec, toComplete string) ([]string, cobra.ShellCompDirective) {
	c, ctx, cancel, ok := adminCompletionClient(cmd)
	if !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	defer cancel()
	resp, err := c.AdminIdPService().ListOAuthProviders(ctx, &leapmuxv1.ListOAuthProvidersRequest{})
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	for _, p := range resp.GetProviders() {
		if strings.HasPrefix(p.GetId(), toComplete) {
			out = append(out, formatCompletionCandidate(p.GetId(), p.GetName()))
		}
	}
	slices.Sort(out)
	return out, cobra.ShellCompDirectiveNoFileComp
}

func completeAdminAPITokens(cmd *cobra.Command, _ cli.ArgSpec, toComplete string) ([]string, cobra.ShellCompDirective) {
	c, ctx, cancel, ok := adminCompletionClient(cmd)
	if !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	defer cancel()
	resp, err := c.AdminUserService().ListAPITokens(ctx, &leapmuxv1.ListAPITokensRequest{})
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	for _, tok := range resp.GetTokens() {
		if strings.HasPrefix(tok.GetId(), toComplete) {
			out = append(out, formatCompletionCandidate(tok.GetId(), tok.GetInstallationName()))
		}
	}
	slices.Sort(out)
	return out, cobra.ShellCompDirectiveNoFileComp
}

func completeAdminDelegationTokens(cmd *cobra.Command, _ cli.ArgSpec, toComplete string) ([]string, cobra.ShellCompDirective) {
	c, ctx, cancel, ok := adminCompletionClient(cmd)
	if !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	defer cancel()
	resp, err := c.AdminUserService().ListDelegationTokens(ctx, &leapmuxv1.ListDelegationTokensRequest{})
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	for _, tok := range resp.GetTokens() {
		if strings.HasPrefix(tok.GetId(), toComplete) {
			out = append(out, formatCompletionCandidate(tok.GetId(), tok.GetUsername()))
		}
	}
	slices.Sort(out)
	return out, cobra.ShellCompDirectiveNoFileComp
}
