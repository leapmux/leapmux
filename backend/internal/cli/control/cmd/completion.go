package cmd

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/spf13/cobra"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
)

// completionTimeout limits the time a shell waits on Hub or pin-store reads.
const completionTimeout = 750 * time.Millisecond

func controlClientForCompletion(cmd *cobra.Command) (*control.Client, error) {
	if blankTargetFlag(cmd, "hub") {
		return nil, cli.Usagef("--hub is empty")
	}
	return control.NewClientFromEnv(explicitHub(cmd))
}

func completionContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), completionTimeout)
}

func commandSubPath(cmd *cobra.Command) string {
	path := strings.TrimSpace(cmd.CommandPath())
	if idx := strings.IndexByte(path, ' '); idx >= 0 {
		return strings.TrimSpace(path[idx+1:])
	}
	return path
}

func registerScopeCompletions(cmd *cobra.Command) {
	scopeKinds := map[string]resolve.Kind{
		"workspace": resolve.KindWorkspace,
		"worker":    resolve.KindWorker,
		"tile":      resolve.KindTile,
		"tab":       resolve.KindTab,
		"parent":    resolve.KindAgent,
	}
	for name, kind := range scopeKinds {
		if cmd.Flags().Lookup(name) == nil {
			continue
		}
		k := kind
		err := cmd.RegisterFlagCompletionFunc(name, func(cmd *cobra.Command, _ []string, word string) ([]string, cobra.ShellCompDirective) {
			return completeTargetsOfKind(cmd, k, word, true)
		})
		if err != nil {
			panic(fmt.Sprintf("complete scope flag %q of %q: %v", name, cmd.Use, err))
		}
	}
	for _, relFlag := range []string{"before", "after"} {
		if cmd.Flags().Lookup(relFlag) == nil {
			continue
		}
		_ = cmd.RegisterFlagCompletionFunc(relFlag, func(cmd *cobra.Command, args []string, word string) ([]string, cobra.ShellCompDirective) {
			switch commandSubPath(cmd) {
			case "workspace move":
				return completeTargetsOfKind(cmd, resolve.KindWorkspace, word, true)
			case "section move":
				return completeSections(cmd, cli.ArgSpec{Name: "section"}, word)
			case "agent queue move":
				if len(args) > 0 {
					return completeQueuedInputsForAgent(cmd, args[0], word)
				}
				return nil, cobra.ShellCompDirectiveNoFileComp
			default:
				return completeTargetsOfKind(cmd, resolve.KindTab, word, true)
			}
		})
	}
	if cmd.Flags().Lookup("hub") != nil {
		_ = cmd.RegisterFlagCompletionFunc("hub", func(cmd *cobra.Command, _ []string, word string) ([]string, cobra.ShellCompDirective) {
			return completeStoredHubs(cmd, cli.ArgSpec{}, word)
		})
	}
	if cmd.Flags().Lookup("section") != nil {
		_ = cmd.RegisterFlagCompletionFunc("section", func(cmd *cobra.Command, _ []string, word string) ([]string, cobra.ShellCompDirective) {
			return completeSections(cmd, cli.ArgSpec{Name: "section"}, word)
		})
	}
	if cmd.Flags().Lookup("provider") != nil {
		_ = cmd.RegisterFlagCompletionFunc("provider", func(_ *cobra.Command, _ []string, word string) ([]string, cobra.ShellCompDirective) {
			var matches []string
			for _, alias := range allProviderAliases() {
				if strings.HasPrefix(alias, word) {
					matches = append(matches, alias)
				}
			}
			slices.Sort(matches)
			return matches, cobra.ShellCompDirectiveNoFileComp
		})
	}
	if cmd.Flags().Lookup("shell") != nil {
		_ = cmd.RegisterFlagCompletionFunc("shell", func(cmd *cobra.Command, _ []string, word string) ([]string, cobra.ShellCompDirective) {
			return completeWorkerShells(cmd, word)
		})
	}
	for _, optFlag := range []string{"model", "effort", "permission-mode"} {
		if cmd.Flags().Lookup(optFlag) == nil {
			continue
		}
		flagName := optFlag
		_ = cmd.RegisterFlagCompletionFunc(flagName, func(cmd *cobra.Command, args []string, word string) ([]string, cobra.ShellCompDirective) {
			return completeAgentOptionChoices(cmd, args, flagName, word)
		})
	}
	for _, brFlag := range []string{"base", "checkout"} {
		if cmd.Flags().Lookup(brFlag) == nil {
			continue
		}
		_ = cmd.RegisterFlagCompletionFunc(brFlag, func(cmd *cobra.Command, _ []string, word string) ([]string, cobra.ShellCompDirective) {
			return completeGitBranches(cmd, word)
		})
	}
	if cmd.Flags().Lookup("use-worktree") != nil {
		_ = cmd.RegisterFlagCompletionFunc("use-worktree", func(cmd *cobra.Command, _ []string, word string) ([]string, cobra.ShellCompDirective) {
			return completeGitWorktrees(cmd, word)
		})
	}
}

func completeStoredHubs(_ *cobra.Command, _ cli.ArgSpec, word string) ([]string, cobra.ShellCompDirective) {
	files, err := control.ListCredentialFiles()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var matches []string
	for _, f := range files {
		if strings.HasPrefix(f.HubURL, word) {
			cand := f.HubURL
			if f.Username != "" {
				cand += "\t" + f.Username
			}
			matches = append(matches, cand)
		}
	}
	slices.Sort(matches)
	return matches, cobra.ShellCompDirectiveNoFileComp
}

// completeTargets completes a positional target with "self" and the ids of
// the live objects of its kind. Each candidate carries its title as the
// description that zsh and fish show.
func completeTargets(cmd *cobra.Command, spec cli.ArgSpec, toComplete string) ([]string, cobra.ShellCompDirective) {
	kind, ok := targetArguments[spec.Name]
	if !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return completeTargetsOfKind(cmd, kind, toComplete, true)
}

func completeOtherAgents(cmd *cobra.Command, spec cli.ArgSpec, toComplete string) ([]string, cobra.ShellCompDirective) {
	switch spec.Name {
	case "request":
		// Positional request handle for `agent approve|deny|answer <agent> [request]`.
		args := cmd.Flags().Args()
		if len(args) > 0 {
			return completePendingRequestsForAgent(cmd, args[0], toComplete)
		}
		return nil, cobra.ShellCompDirectiveNoFileComp
	case "text":
		return nil, cobra.ShellCompDirectiveNoFileComp
	default:
		return completeTargetsOfKind(cmd, resolve.KindAgent, toComplete, false)
	}
}

func completeQueueArgs(cmd *cobra.Command, spec cli.ArgSpec, toComplete string) ([]string, cobra.ShellCompDirective) {
	switch spec.Name {
	case "agent":
		return completeTargetsOfKind(cmd, resolve.KindAgent, toComplete, true)
	case "input":
		args := cmd.Flags().Args()
		if len(args) > 0 {
			return completeQueuedInputsForAgent(cmd, args[0], toComplete)
		}
		return nil, cobra.ShellCompDirectiveNoFileComp
	default:
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
}

func completeNotifications(cmd *cobra.Command, _ cli.ArgSpec, toComplete string) ([]string, cobra.ShellCompDirective) {
	c, err := controlClientForCompletion(cmd)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	ctx, cancel := completionContext()
	defer cancel()
	workerID, err := completionWorkerID(ctx, cmd, c)
	if err != nil || workerID == "" {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var resp leapmuxv1.ListNotificationsResponse
	if err := callInnerRPC(ctx, c, workerID, leapmuxv1connect.WorkerPrivateServiceListNotificationsProcedure, &leapmuxv1.ListNotificationsRequest{}, &resp); err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	ids := make([]string, 0, len(resp.GetNotifications()))
	for _, n := range resp.GetNotifications() {
		ids = append(ids, n.GetId())
	}
	prefixes := shortestUniquePrefixes(ids)
	var out []string
	for _, n := range resp.GetNotifications() {
		tok := prefixes[n.GetId()]
		if strings.HasPrefix(n.GetId(), toComplete) || strings.HasPrefix(tok, toComplete) {
			out = append(out, tok+"\t"+sanitizeCompletionDesc(n.GetTitle()))
		}
	}
	slices.Sort(out)
	return out, cobra.ShellCompDirectiveNoFileComp
}

func completeWindows(cmd *cobra.Command, _ cli.ArgSpec, toComplete string) ([]string, cobra.ShellCompDirective) {
	c, err := controlClientForCompletion(cmd)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	ctx, cancel := completionContext()
	defer cancel()
	state, err := fetchMaterialized(ctx, c, nil)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var ids []string
	for id, fw := range state.GetFloatingWindows() {
		if fw == nil || !crdt.HLCIsZero(fw.GetTombstoneAt()) {
			continue
		}
		ids = append(ids, id)
	}
	prefixes := shortestUniquePrefixes(ids)
	var out []string
	for _, id := range ids {
		tok := prefixes[id]
		if strings.HasPrefix(id, toComplete) || strings.HasPrefix(tok, toComplete) {
			wsID := state.GetFloatingWindows()[id].GetWorkspaceId().GetValue()
			out = append(out, tok+"\tworkspace "+cli.ShortID(wsID))
		}
	}
	slices.Sort(out)
	return out, cobra.ShellCompDirectiveNoFileComp
}

func completionWorkerID(ctx context.Context, cmd *cobra.Command, c *control.Client) (string, error) {
	if f := cmd.Flags().Lookup(workerFlag); f != nil && f.Changed {
		t, err := resolve.Canonical(ctx, targetListing(c, resolveDeps(c)), resolve.KindWorker, strings.TrimSpace(f.Value.String()))
		if err != nil {
			return "", err
		}
		return t.ID, nil
	}
	if id := strings.TrimSpace(os.Getenv(controlenv.WorkerIDVariable)); id != "" {
		return id, nil
	}
	if saved, err := control.LoadContext(); err == nil && saved.Worker != "" {
		t, err := resolve.Canonical(ctx, targetListing(c, resolveDeps(c)), resolve.KindWorker, saved.Worker)
		if err == nil {
			return t.ID, nil
		}
	}
	workers, err := listAllWorkers(ctx, c)
	if err != nil {
		return "", err
	}
	var online []string
	for _, w := range workers {
		if w.GetOnline() {
			online = append(online, w.GetId())
		}
	}
	if len(online) == 1 {
		return online[0], nil
	}
	return "", nil
}

func completionGitDir(cmd *cobra.Command) string {
	if f := cmd.Flags().Lookup("dir"); f != nil && f.Changed {
		return strings.TrimSpace(f.Value.String())
	}
	if dir := workingDirEnv(); dir != "" {
		return dir
	}
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}
	return ""
}

func completeWorkerShells(cmd *cobra.Command, word string) ([]string, cobra.ShellCompDirective) {
	c, err := controlClientForCompletion(cmd)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	ctx, cancel := completionContext()
	defer cancel()
	workerID, err := completionWorkerID(ctx, cmd, c)
	if err != nil || workerID == "" {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var resp leapmuxv1.ListAvailableShellsResponse
	if err := callInnerRPC(ctx, c, workerID, leapmuxv1connect.WorkerTerminalServiceListAvailableShellsProcedure, &leapmuxv1.ListAvailableShellsRequest{}, &resp); err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	for _, sh := range resp.GetShells() {
		if strings.HasPrefix(sh, word) {
			out = append(out, sh)
		}
	}
	slices.Sort(out)
	return out, cobra.ShellCompDirectiveNoFileComp
}

func completeGitBranches(cmd *cobra.Command, word string) ([]string, cobra.ShellCompDirective) {
	c, err := controlClientForCompletion(cmd)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	dir := completionGitDir(cmd)
	if dir == "" {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	ctx, cancel := completionContext()
	defer cancel()
	workerID, err := completionWorkerID(ctx, cmd, c)
	if err != nil || workerID == "" {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var resp leapmuxv1.ListGitBranchesResponse
	if err := callInnerRPC(ctx, c, workerID, leapmuxv1connect.WorkerGitServiceListGitBranchesProcedure, &leapmuxv1.ListGitBranchesRequest{Path: dir}, &resp); err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	for _, b := range resp.GetBranches() {
		if strings.HasPrefix(b.GetName(), word) {
			out = append(out, b.GetName())
		}
	}
	slices.Sort(out)
	return out, cobra.ShellCompDirectiveNoFileComp
}

func completeGitWorktrees(cmd *cobra.Command, word string) ([]string, cobra.ShellCompDirective) {
	c, err := controlClientForCompletion(cmd)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	dir := completionGitDir(cmd)
	if dir == "" {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	ctx, cancel := completionContext()
	defer cancel()
	workerID, err := completionWorkerID(ctx, cmd, c)
	if err != nil || workerID == "" {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var resp leapmuxv1.ListGitWorktreesResponse
	if err := callInnerRPC(ctx, c, workerID, leapmuxv1connect.WorkerGitServiceListGitWorktreesProcedure, &leapmuxv1.ListGitWorktreesRequest{Path: dir}, &resp); err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	for _, wt := range resp.GetWorktrees() {
		if strings.HasPrefix(wt.GetPath(), word) {
			cand := wt.GetPath()
			if wt.GetBranch() != "" {
				cand += "\t" + sanitizeCompletionDesc(wt.GetBranch())
			}
			out = append(out, cand)
		}
	}
	slices.Sort(out)
	return out, cobra.ShellCompDirectiveNoFileComp
}

func completeAgentOptionChoices(cmd *cobra.Command, args []string, flagName, word string) ([]string, cobra.ShellCompDirective) {
	c, err := controlClientForCompletion(cmd)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	ctx, cancel := completionContext()
	defer cancel()
	agentArg := resolve.SelfTarget
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		agentArg = args[0]
	}
	t, err := resolve.Canonical(ctx, targetListing(c, resolveDeps(c)), resolve.KindAgent, agentArg)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	got, err := resolve.Resolve(ctx, resolveDeps(c), resolve.Need{TabID: true, WorkerID: true}, resolve.Inputs{
		TabID: t.ID, ExplicitTabID: !t.Self, WorkerID: strings.TrimSpace(os.Getenv(controlenv.WorkerIDVariable)), FixedTabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
	})
	if err != nil || got.WorkerID == "" {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var resp leapmuxv1.ListAgentsResponse
	if err := callInnerRPC(ctx, c, got.WorkerID, leapmuxv1connect.WorkerAgentServiceListAgentsProcedure, &leapmuxv1.ListAgentsRequest{TabIds: []string{got.TabID}}, &resp); err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	wantKey := strings.ReplaceAll(flagName, "-", "_")
	var out []string
	for _, a := range resp.GetAgents() {
		if a.GetId() != got.TabID {
			continue
		}
		for _, g := range a.GetOptionGroups() {
			if g.GetId() != wantKey && g.GetId() != flagName {
				continue
			}
			for _, ch := range g.GetOptions() {
				if strings.HasPrefix(ch.GetId(), word) {
					cand := ch.GetId()
					if ch.GetName() != "" {
						cand += "\t" + sanitizeCompletionDesc(ch.GetName())
					}
					out = append(out, cand)
				}
			}
		}
	}
	slices.Sort(out)
	return out, cobra.ShellCompDirectiveNoFileComp
}

func completePendingRequestsForAgent(cmd *cobra.Command, agentArg, word string) ([]string, cobra.ShellCompDirective) {
	c, err := controlClientForCompletion(cmd)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	ctx, cancel := completionContext()
	defer cancel()
	t, err := resolve.Canonical(ctx, targetListing(c, resolveDeps(c)), resolve.KindAgent, agentArg)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	got, err := resolve.Resolve(ctx, resolveDeps(c), resolve.Need{TabID: true, WorkerID: true}, resolve.Inputs{
		TabID: t.ID, ExplicitTabID: !t.Self, WorkerID: strings.TrimSpace(os.Getenv(controlenv.WorkerIDVariable)), FixedTabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
	})
	if err != nil || got.WorkerID == "" {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var resp leapmuxv1.ListControlRequestsResponse
	if err := callInnerRPC(ctx, c, got.WorkerID, leapmuxv1connect.WorkerAgentServiceListControlRequestsProcedure, &leapmuxv1.ListControlRequestsRequest{AgentId: got.TabID}, &resp); err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	for _, r := range resp.GetRequests() {
		h := r.GetClaimToken()
		if strings.HasPrefix(h, word) {
			out = append(out, h+"\t"+controlPromptKind(r.GetPrompt()))
		}
	}
	slices.Sort(out)
	return out, cobra.ShellCompDirectiveNoFileComp
}

func completeQueuedInputsForAgent(cmd *cobra.Command, agentArg, word string) ([]string, cobra.ShellCompDirective) {
	c, err := controlClientForCompletion(cmd)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	ctx, cancel := completionContext()
	defer cancel()
	t, err := resolve.Canonical(ctx, targetListing(c, resolveDeps(c)), resolve.KindAgent, agentArg)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	got, err := resolve.Resolve(ctx, resolveDeps(c), resolve.Need{TabID: true, WorkerID: true}, resolve.Inputs{
		TabID: t.ID, ExplicitTabID: !t.Self, WorkerID: strings.TrimSpace(os.Getenv(controlenv.WorkerIDVariable)), FixedTabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
	})
	if err != nil || got.WorkerID == "" {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var resp leapmuxv1.ListAgentInputQueueResponse
	if err := callInnerRPC(ctx, c, got.WorkerID, leapmuxv1connect.WorkerAgentInputServiceListAgentInputQueueProcedure, &leapmuxv1.ListAgentInputQueueRequest{AgentId: got.TabID}, &resp); err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	items := resp.GetSnapshot().GetItems()
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.GetId())
	}
	prefixes := shortestUniquePrefixes(ids)
	var out []string
	for _, item := range items {
		tok := prefixes[item.GetId()]
		if strings.HasPrefix(item.GetId(), word) || strings.HasPrefix(tok, word) {
			out = append(out, tok+"\t"+sanitizeCompletionDesc(item.GetText()))
		}
	}
	slices.Sort(out)
	return out, cobra.ShellCompDirectiveNoFileComp
}

func sanitizeCompletionDesc(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

// shortestUniquePrefixes maps each ID to its shortest unique prefix of at
// least 8 characters when the ID is a long generated identifier (>= 16 chars),
// preserving short IDs verbatim.
func shortestUniquePrefixes(ids []string) map[string]string {
	out := make(map[string]string, len(ids))
	for _, id := range ids {
		if len(id) < 16 {
			out[id] = id
			continue
		}
		prefixLen := 8
		for prefixLen < len(id) {
			p := id[:prefixLen]
			unique := true
			for _, other := range ids {
				if other != id && strings.HasPrefix(other, p) {
					unique = false
					break
				}
			}
			if unique {
				break
			}
			prefixLen++
		}
		out[id] = id[:prefixLen]
	}
	return out
}

func completeTargetsOfKind(cmd *cobra.Command, kind resolve.Kind, toComplete string, allowSelf bool) ([]string, cobra.ShellCompDirective) {
	// Cobra completes flags and arguments without running Args validation.
	if blankTargetFlag(cmd, "hub") {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	self := completionSelfID(kind)
	var candidates []string
	if allowSelf && self != "" && strings.HasPrefix(resolve.SelfTarget, toComplete) {
		candidates = append(candidates, resolve.SelfTarget)
	}
	c, err := control.NewClientFromEnv(explicitHub(cmd))
	if err != nil {
		return candidates, cobra.ShellCompDirectiveNoFileComp
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), completionTimeout)
	defer cancel()
	listing := targetListing(c, resolveDeps(c))
	add := func(id, tok, title string) {
		if (!strings.HasPrefix(id, toComplete) && !strings.HasPrefix(tok, toComplete)) || (!allowSelf && id == self) {
			return
		}
		cand := tok
		if title != "" {
			cand += "\t" + sanitizeCompletionDesc(title)
		}
		candidates = append(candidates, cand)
	}
	switch {
	case kind.IsTab():
		tabs, err := listing.Tabs(ctx)
		if err != nil {
			break
		}
		want := kind.TabType()
		var matching []*leapmuxv1.WorkspaceTab
		ids := make([]string, 0, len(tabs))
		for _, t := range tabs {
			if want == leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED || t.GetTab().GetTabType() == want {
				matching = append(matching, t)
				ids = append(ids, t.GetTab().GetTabId())
			}
		}
		prefixes := shortestUniquePrefixes(ids)
		titles := listing.TabTitles(ctx, matching)
		for _, t := range matching {
			id := t.GetTab().GetTabId()
			title := titles[id]
			add(id, prefixes[id], title)
			if title != "" && strings.HasPrefix("title:"+title, toComplete) {
				add(id, "title:"+title, cli.ShortID(id))
			}
		}
	case kind == resolve.KindWorkspace:
		workspaces, err := listing.Workspaces(ctx)
		if err != nil {
			break
		}
		ids := make([]string, 0, len(workspaces))
		for _, w := range workspaces {
			ids = append(ids, w.GetId())
		}
		prefixes := shortestUniquePrefixes(ids)
		for _, w := range workspaces {
			add(w.GetId(), prefixes[w.GetId()], w.GetTitle())
			if w.GetTitle() != "" && strings.HasPrefix("title:"+w.GetTitle(), toComplete) {
				add(w.GetId(), "title:"+w.GetTitle(), cli.ShortID(w.GetId()))
			}
		}
	case kind == resolve.KindTile:
		tiles, err := listing.Tiles(ctx)
		if err != nil {
			break
		}
		ids := make([]string, 0, len(tiles))
		for id := range tiles {
			ids = append(ids, id)
		}
		prefixes := shortestUniquePrefixes(ids)
		for id, nodeKind := range tiles {
			add(id, prefixes[id], nodeKind)
		}
	case kind == resolve.KindWorker:
		workers, err := listing.Workers(ctx)
		if err != nil {
			break
		}
		ids := make([]string, 0, len(workers))
		for _, w := range workers {
			ids = append(ids, w.GetId())
		}
		prefixes := shortestUniquePrefixes(ids)
		names := listing.WorkerNames(ctx, workers)
		for _, w := range workers {
			name := names[w.GetId()]
			add(w.GetId(), prefixes[w.GetId()], name)
			if name != "" && strings.HasPrefix("name:"+name, toComplete) {
				add(w.GetId(), "name:"+name, cli.ShortID(w.GetId()))
			}
		}
	}
	slices.Sort(candidates)
	return candidates, cobra.ShellCompDirectiveNoFileComp
}

func completionSelfID(kind resolve.Kind) string {
	if kind == resolve.KindWorker {
		return strings.TrimSpace(os.Getenv(controlenv.WorkerIDVariable))
	}
	return (resolve.Listing{}).SelfTabID(kind)
}

func completePinnedWorkers(cmd *cobra.Command, _ cli.ArgSpec, word string) ([]string, cobra.ShellCompDirective) {
	if blankTargetFlag(cmd, "hub") {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	pins, err := openPinStore(cmd)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), completionTimeout)
	defer cancel()
	entries, err := pins.List(ctx)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var ids []string
	for _, pin := range entries {
		if strings.HasPrefix(pin.WorkerID, word) {
			ids = append(ids, pin.WorkerID)
		}
	}
	return ids, cobra.ShellCompDirectiveNoFileComp
}
