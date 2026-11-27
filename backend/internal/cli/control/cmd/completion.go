package cmd

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
	"github.com/leapmux/leapmux/locallisten"
)

// completionTimeout limits the time a shell waits on Hub or pin-store reads.
const completionTimeout = 750 * time.Millisecond

const (
	minShortenIDLen = 16
	minUniquePrefix = 8
)

func controlClientForCompletion(cmd *cobra.Command) (control.Peer, error) {
	if blankTargetFlag(cmd, "hub") {
		return nil, cli.Usagef("--hub is empty")
	}
	return control.NewClientFromEnv(explicitHub(cmd))
}

func completionContext(cmd *cobra.Command) (context.Context, context.CancelFunc) {
	parent := context.Background()
	if cmd != nil && cmd.Context() != nil {
		parent = cmd.Context()
	}
	return context.WithTimeout(parent, completionTimeout)
}

func formatCompletionCandidate(value, desc string) string {
	v := cli.SanitizeSingleLine(value)
	d := cli.SanitizeSingleLine(desc)
	if d == "" {
		return v
	}
	return v + "\t" + d
}

// completionIDToken returns the token to emit for id when the user already
// typed toComplete. If toComplete is longer than shortestTok, it emits the full
// id so the candidate still starts with toComplete instead of being dropped by
// the shell.
func completionIDToken(id, shortestTok, toComplete string) string {
	if len(toComplete) > len(shortestTok) && strings.HasPrefix(id, toComplete) {
		return id
	}
	return shortestTok
}

func commandSubPath(cmd *cobra.Command) string {
	path := strings.TrimSpace(cmd.CommandPath())
	if idx := strings.IndexByte(path, ' '); idx >= 0 {
		return strings.TrimSpace(path[idx+1:])
	}
	return path
}

func completeHubFlag(cmd *cobra.Command, _ []string, word string) ([]string, cobra.ShellCompDirective) {
	return completeStoredHubs(cmd, cli.ArgSpec{}, word)
}

func completeProviderFlag(_ *cobra.Command, _ []string, word string) ([]string, cobra.ShellCompDirective) {
	var matches []string
	for _, alias := range allProviderAliases() {
		if strings.HasPrefix(alias, word) {
			matches = append(matches, formatCompletionCandidate(alias, ""))
		}
	}
	slices.Sort(matches)
	return matches, cobra.ShellCompDirectiveNoFileComp
}

func completeSectionFlag(cmd *cobra.Command, _ []string, word string) ([]string, cobra.ShellCompDirective) {
	return completeSections(cmd, cli.ArgSpec{Name: "section"}, word)
}

func completeShellFlag(cmd *cobra.Command, _ []string, word string) ([]string, cobra.ShellCompDirective) {
	return completeWorkerShells(cmd, word)
}

func completeGitBranchFlag(cmd *cobra.Command, _ []string, word string) ([]string, cobra.ShellCompDirective) {
	return completeGitBranches(cmd, word)
}

func completeGitWorktreeFlag(cmd *cobra.Command, _ []string, word string) ([]string, cobra.ShellCompDirective) {
	return completeGitWorktrees(cmd, word)
}

func completeAgentOptionFlag(flagName string) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, word string) ([]string, cobra.ShellCompDirective) {
		return completeAgentOptionChoices(cmd, args, flagName, word)
	}
}

func completeWorkspacePositionFlag(cmd *cobra.Command, _ []string, word string) ([]string, cobra.ShellCompDirective) {
	return completeTargetsOfKind(cmd, resolve.KindWorkspace, word, true)
}

func completeSectionPositionFlag(cmd *cobra.Command, _ []string, word string) ([]string, cobra.ShellCompDirective) {
	return completeSections(cmd, cli.ArgSpec{Name: "section"}, word)
}

func completeQueueInputPositionFlag(cmd *cobra.Command, args []string, word string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return completeQueuedInputsForAgent(cmd, args[0], word)
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}

func completeTabPositionFlag(cmd *cobra.Command, _ []string, word string) ([]string, cobra.ShellCompDirective) {
	return completeTargetsOfKind(cmd, resolve.KindTab, word, true)
}

func mergeFlagCompletions(groups ...map[string]cobra.CompletionFunc) map[string]cobra.CompletionFunc {
	total := 0
	for _, g := range groups {
		total += len(g)
	}
	out := make(map[string]cobra.CompletionFunc, total)
	for _, g := range groups {
		for k, v := range g {
			if _, exists := out[k]; exists {
				panic(fmt.Sprintf("cmd: duplicate flag completion registration for %q", k))
			}
			out[k] = v
		}
	}
	return out
}

func tabPositionFlagCompletions() map[string]cobra.CompletionFunc {
	return map[string]cobra.CompletionFunc{
		"before": completeTabPositionFlag,
		"after":  completeTabPositionFlag,
	}
}

func gitSetupFlagCompletions() map[string]cobra.CompletionFunc {
	return map[string]cobra.CompletionFunc{
		"base":         completeGitBranchFlag,
		"checkout":     completeGitBranchFlag,
		"use-worktree": completeGitWorktreeFlag,
	}
}

func agentOptionFlagCompletions() map[string]cobra.CompletionFunc {
	return map[string]cobra.CompletionFunc{
		"model":           completeAgentOptionFlag("model"),
		"effort":          completeAgentOptionFlag("effort"),
		"permission-mode": completeAgentOptionFlag("permission-mode"),
	}
}

func registerScopeCompletions(cmd *cobra.Command) {
	scopeKinds := []struct {
		name string
		kind resolve.Kind
	}{
		{name: "workspace", kind: resolve.KindWorkspace},
		{name: "worker", kind: resolve.KindWorker},
		{name: "tile", kind: resolve.KindTile},
		{name: "tab", kind: resolve.KindTab},
		{name: "parent", kind: resolve.KindAgent},
	}
	for _, entry := range scopeKinds {
		if cmd.Flags().Lookup(entry.name) == nil {
			continue
		}
		k := entry.kind
		err := cmd.RegisterFlagCompletionFunc(entry.name, func(cmd *cobra.Command, _ []string, word string) ([]string, cobra.ShellCompDirective) {
			return completeTargetsOfKind(cmd, k, word, true)
		})
		if err != nil {
			panic(fmt.Sprintf("complete scope flag %q of %q: %v", entry.name, cmd.Use, err))
		}
	}
}

func completeStoredHubs(_ *cobra.Command, _ cli.ArgSpec, word string) ([]string, cobra.ShellCompDirective) {
	files, err := control.ListCredentialFiles()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var matches []string
	for _, f := range files {
		url := cli.SanitizeSingleLine(f.HubURL)
		if url != "" && strings.HasPrefix(url, word) {
			matches = append(matches, formatCompletionCandidate(url, f.Username))
		}
	}
	slices.Sort(matches)
	return matches, cobra.ShellCompDirectiveNoFileComp
}

// completeTargets completes a positional target with "self", "children" (for
// variadic agent targets inside an agent tab), and the ids of the live objects
// of its kind. Each candidate carries its title as the description that zsh and
// fish show.
func completeTargets(cmd *cobra.Command, spec cli.ArgSpec, toComplete string) ([]string, cobra.ShellCompDirective) {
	kind, ok := targetArguments[spec.Name]
	if !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return completeTargetsWithOptions(cmd, kind, toComplete, targetCompletionOptions{
		allowSelf:     true,
		allowChildren: kind == resolve.KindAgent && spec.Variadic,
	})
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
		return completeTargetsWithOptions(cmd, resolve.KindAgent, toComplete, targetCompletionOptions{
			allowSelf:     false,
			allowChildren: spec.Variadic,
		})
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

func withCompletionWorker(
	cmd *cobra.Command,
	fn func(ctx context.Context, c control.Peer, workerID string) []string,
) ([]string, cobra.ShellCompDirective) {
	c, err := controlClientForCompletion(cmd)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	ctx, cancel := completionContext(cmd)
	defer cancel()
	workerID, err := completionWorkerID(ctx, cmd, c)
	if err != nil || workerID == "" {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	out := fn(ctx, c, workerID)
	slices.Sort(out)
	return out, cobra.ShellCompDirectiveNoFileComp
}

func withCompletionAgent(
	cmd *cobra.Command,
	agentArg string,
	fn func(ctx context.Context, c control.Peer, tabID, workerID string) []string,
) ([]string, cobra.ShellCompDirective) {
	c, err := controlClientForCompletion(cmd)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	ctx, cancel := completionContext(cmd)
	defer cancel()
	tabID, workerID, ok := resolveCompletionAgent(ctx, cmd, c, agentArg)
	if !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	out := fn(ctx, c, tabID, workerID)
	slices.Sort(out)
	return out, cobra.ShellCompDirectiveNoFileComp
}

func completeNotifications(cmd *cobra.Command, _ cli.ArgSpec, toComplete string) ([]string, cobra.ShellCompDirective) {
	return withCompletionWorker(cmd, func(ctx context.Context, c control.Peer, workerID string) []string {
		var resp leapmuxv1.ListNotificationsResponse
		if err := callInnerRPC(ctx, c, workerID, leapmuxv1connect.WorkerPrivateServiceListNotificationsProcedure, &leapmuxv1.ListNotificationsRequest{}, &resp); err != nil {
			return nil
		}
		ids := make([]string, 0, len(resp.GetNotifications()))
		for _, n := range resp.GetNotifications() {
			ids = append(ids, n.GetId())
		}
		prefixes := shortestUniquePrefixes(ids)
		var out []string
		for _, n := range resp.GetNotifications() {
			id := n.GetId()
			if id == "" {
				continue
			}
			tok := prefixes[id]
			if strings.HasPrefix(id, toComplete) || strings.HasPrefix(tok, toComplete) {
				out = append(out, formatCompletionCandidate(completionIDToken(id, tok, toComplete), n.GetTitle()))
			}
		}
		return out
	})
}

func completeWindows(cmd *cobra.Command, _ cli.ArgSpec, toComplete string) ([]string, cobra.ShellCompDirective) {
	c, err := controlClientForCompletion(cmd)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	ctx, cancel := completionContext(cmd)
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
			out = append(out, formatCompletionCandidate(completionIDToken(id, tok, toComplete), "workspace "+cli.ShortID(wsID)))
		}
	}
	slices.Sort(out)
	return out, cobra.ShellCompDirectiveNoFileComp
}

func completionWorkerID(ctx context.Context, cmd *cobra.Command, c control.Peer) (string, error) {
	s := newCompletionSession(ctx, cmd, c)
	if workerID, err := s.explicitOrTabWorker(); err != nil || workerID != "" {
		return workerID, err
	}
	var (
		workersLoaded bool
		cachedWorkers []*leapmuxv1.Worker
		workersErr    error
	)
	loadWorkers := func(ctx context.Context) ([]*leapmuxv1.Worker, error) {
		if !workersLoaded {
			cachedWorkers, workersErr = listAllWorkers(ctx, c)
			workersLoaded = true
		}
		return cachedWorkers, workersErr
	}
	if saved, err := control.LoadContext(); err == nil && strings.TrimSpace(saved.Worker) != "" {
		listing := s.listing()
		listing.Workers = loadWorkers
		if t, err := resolve.Canonical(ctx, listing, resolve.KindWorker, saved.Worker); err == nil {
			return t.ID, nil
		}
	}
	workers, err := loadWorkers(ctx)
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

func flagChanged(cmd *cobra.Command, name string) bool {
	if cmd == nil {
		return false
	}
	f := cmd.Flags().Lookup(name)
	return f != nil && f.Changed
}

func isLocalCompletionWorker(cmd *cobra.Command, c control.Peer, workerID string) bool {
	switch client := c.(type) {
	case *control.WorkerIPCClient:
		envWorker := strings.TrimSpace(os.Getenv(controlenv.WorkerIDVariable))
		return envWorker != "" && envWorker == workerID
	case *control.HubClient:
		return locallisten.IsLocal(client.HubURL) && !flagChanged(cmd, workerFlag)
	default:
		return false
	}
}

func completionGitDir(cmd *cobra.Command, c control.Peer, workerID string) string {
	if cmd != nil {
		if f := cmd.Flags().Lookup("dir"); f != nil && f.Changed {
			return strings.TrimSpace(f.Value.String())
		}
	}
	if dir := tabWorkingDirForWorker(workerID, flagChanged(cmd, workerFlag)); dir != "" {
		return dir
	}
	if isLocalCompletionWorker(cmd, c, workerID) {
		if cwd, err := os.Getwd(); err == nil {
			return cwd
		}
	}
	return ""
}

func completeWorkerShells(cmd *cobra.Command, word string) ([]string, cobra.ShellCompDirective) {
	return withCompletionWorker(cmd, func(ctx context.Context, c control.Peer, workerID string) []string {
		var resp leapmuxv1.ListAvailableShellsResponse
		if err := callInnerRPC(ctx, c, workerID, leapmuxv1connect.WorkerTerminalServiceListAvailableShellsProcedure, &leapmuxv1.ListAvailableShellsRequest{}, &resp); err != nil {
			return nil
		}
		var out []string
		for _, sh := range resp.GetShells() {
			clean := cli.SanitizeSingleLine(sh)
			if clean != "" && strings.HasPrefix(clean, word) {
				out = append(out, formatCompletionCandidate(clean, ""))
			}
		}
		return out
	})
}

func completeGitBranches(cmd *cobra.Command, word string) ([]string, cobra.ShellCompDirective) {
	return withCompletionWorker(cmd, func(ctx context.Context, c control.Peer, workerID string) []string {
		dir := completionGitDir(cmd, c, workerID)
		if dir == "" {
			return nil
		}
		var resp leapmuxv1.ListGitBranchesResponse
		if err := callInnerRPC(ctx, c, workerID, leapmuxv1connect.WorkerGitServiceListGitBranchesProcedure, &leapmuxv1.ListGitBranchesRequest{Path: dir}, &resp); err != nil {
			return nil
		}
		var out []string
		for _, b := range resp.GetBranches() {
			clean := cli.SanitizeSingleLine(b.GetName())
			if clean != "" && strings.HasPrefix(clean, word) {
				out = append(out, formatCompletionCandidate(clean, ""))
			}
		}
		return out
	})
}

func completeGitWorktrees(cmd *cobra.Command, word string) ([]string, cobra.ShellCompDirective) {
	return withCompletionWorker(cmd, func(ctx context.Context, c control.Peer, workerID string) []string {
		dir := completionGitDir(cmd, c, workerID)
		if dir == "" {
			return nil
		}
		var resp leapmuxv1.ListGitWorktreesResponse
		if err := callInnerRPC(ctx, c, workerID, leapmuxv1connect.WorkerGitServiceListGitWorktreesProcedure, &leapmuxv1.ListGitWorktreesRequest{Path: dir}, &resp); err != nil {
			return nil
		}
		var out []string
		for _, wt := range resp.GetWorktrees() {
			cleanPath := cli.SanitizeSingleLine(wt.GetPath())
			if cleanPath != "" && strings.HasPrefix(cleanPath, word) {
				out = append(out, formatCompletionCandidate(cleanPath, wt.GetBranch()))
			}
		}
		return out
	})
}

func resolveCompletionAgent(ctx context.Context, cmd *cobra.Command, c control.Peer, agentArg string) (tabID, workerID string, ok bool) {
	if strings.TrimSpace(agentArg) == "" {
		return "", "", false
	}
	got, err := newCompletionSession(ctx, cmd, c).resolveTab(resolve.KindAgent, agentArg, resolve.Need{WorkerID: true})
	if err != nil || got.WorkerID == "" {
		return "", "", false
	}
	return got.ID, got.WorkerID, true
}

func agentOptionGroupID(flagName string) string {
	switch flagName {
	case "model":
		return optionids.Model
	case "effort":
		return optionids.Effort
	case "permission-mode":
		return optionids.PermissionMode
	default:
		return strings.ReplaceAll(flagName, "-", "_")
	}
}

func completeAgentOptionChoices(cmd *cobra.Command, args []string, flagName, word string) ([]string, cobra.ShellCompDirective) {
	agentArg := resolve.SelfTarget
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		agentArg = args[0]
	}
	return withCompletionAgent(cmd, agentArg, func(ctx context.Context, c control.Peer, tabID, workerID string) []string {
		var resp leapmuxv1.ListAgentsResponse
		if err := callInnerRPC(ctx, c, workerID, leapmuxv1connect.WorkerAgentServiceListAgentsProcedure, &leapmuxv1.ListAgentsRequest{TabIds: []string{tabID}}, &resp); err != nil {
			return nil
		}
		wantKey := agentOptionGroupID(flagName)
		var out []string
		for _, a := range resp.GetAgents() {
			if a.GetId() != tabID {
				continue
			}
			g := optionids.GroupByID(a.GetOptionGroups(), wantKey)
			if g == nil && wantKey != flagName {
				g = optionids.GroupByID(a.GetOptionGroups(), flagName)
			}
			if g == nil {
				continue
			}
			for _, ch := range g.GetOptions() {
				cleanID := cli.SanitizeSingleLine(ch.GetId())
				if cleanID != "" && strings.HasPrefix(cleanID, word) {
					out = append(out, formatCompletionCandidate(cleanID, ch.GetName()))
				}
			}
		}
		return out
	})
}

func completePendingRequestsForAgent(cmd *cobra.Command, agentArg, word string) ([]string, cobra.ShellCompDirective) {
	return withCompletionAgent(cmd, agentArg, func(ctx context.Context, c control.Peer, tabID, workerID string) []string {
		var resp leapmuxv1.ListControlRequestsResponse
		if err := callInnerRPC(ctx, c, workerID, leapmuxv1connect.WorkerAgentServiceListControlRequestsProcedure, &leapmuxv1.ListControlRequestsRequest{AgentId: tabID}, &resp); err != nil {
			return nil
		}
		var out []string
		for _, r := range resp.GetRequests() {
			h := cli.SanitizeSingleLine(r.GetClaimToken())
			if h != "" && strings.HasPrefix(h, word) {
				out = append(out, formatCompletionCandidate(h, controlPromptKind(r.GetPrompt())))
			}
		}
		return out
	})
}

func completeQueuedInputsForAgent(cmd *cobra.Command, agentArg, word string) ([]string, cobra.ShellCompDirective) {
	return withCompletionAgent(cmd, agentArg, func(ctx context.Context, c control.Peer, tabID, workerID string) []string {
		var resp leapmuxv1.ListAgentInputQueueResponse
		if err := callInnerRPC(ctx, c, workerID, leapmuxv1connect.WorkerAgentInputServiceListAgentInputQueueProcedure, &leapmuxv1.ListAgentInputQueueRequest{AgentId: tabID}, &resp); err != nil {
			return nil
		}
		items := resp.GetSnapshot().GetItems()
		ids := make([]string, 0, len(items))
		for _, item := range items {
			ids = append(ids, item.GetId())
		}
		prefixes := shortestUniquePrefixes(ids)
		var out []string
		for _, item := range items {
			id := item.GetId()
			if id == "" {
				continue
			}
			tok := prefixes[id]
			if strings.HasPrefix(id, word) || strings.HasPrefix(tok, word) {
				out = append(out, formatCompletionCandidate(completionIDToken(id, tok, word), item.GetText()))
			}
		}
		return out
	})
}

func commonPrefixLen(a, b string) int {
	n := min(len(a), len(b))
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return i
}

// shortestUniquePrefixes maps each ID to its shortest unique prefix of at
// least 8 characters when the ID is a long generated identifier (>= 16 chars),
// preserving short IDs verbatim.
func shortestUniquePrefixes(ids []string) map[string]string {
	out := make(map[string]string, len(ids))
	if len(ids) == 0 {
		return out
	}
	sorted := slices.Clone(ids)
	slices.Sort(sorted)
	sorted = slices.Compact(sorted)
	for i, id := range sorted {
		if len(id) < minShortenIDLen {
			out[id] = id
			continue
		}
		maxLCP := 0
		if i > 0 {
			maxLCP = commonPrefixLen(id, sorted[i-1])
		}
		if i+1 < len(sorted) {
			if nextLCP := commonPrefixLen(id, sorted[i+1]); nextLCP > maxLCP {
				maxLCP = nextLCP
			}
		}
		prefixLen := min(len(id), max(minUniquePrefix, maxLCP+1))
		out[id] = id[:prefixLen]
	}
	return out
}

type targetCompletionOptions struct {
	allowSelf     bool
	allowChildren bool
}

func completeTargetsOfKind(cmd *cobra.Command, kind resolve.Kind, toComplete string, allowSelf bool) ([]string, cobra.ShellCompDirective) {
	return completeTargetsWithOptions(cmd, kind, toComplete, targetCompletionOptions{allowSelf: allowSelf})
}

func completeTargetsWithOptions(cmd *cobra.Command, kind resolve.Kind, toComplete string, opts targetCompletionOptions) ([]string, cobra.ShellCompDirective) {
	// Cobra completes flags and arguments without running Args validation.
	if blankTargetFlag(cmd, "hub") {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	self := completionSelfID(kind)
	var candidates []string
	if opts.allowSelf && self != "" && strings.HasPrefix(resolve.SelfTarget, toComplete) {
		candidates = append(candidates, resolve.SelfTarget)
	}
	if opts.allowChildren && self != "" && strings.TrimSpace(os.Getenv(controlenv.WorkerIDVariable)) != "" &&
		strings.HasPrefix(childrenTarget, toComplete) {
		candidates = append(candidates, childrenTarget)
	}
	c, err := control.NewClientFromEnv(explicitHub(cmd))
	if err != nil {
		slices.Sort(candidates)
		return candidates, cobra.ShellCompDirectiveNoFileComp
	}
	ctx, cancel := completionContext(cmd)
	defer cancel()
	listing := targetListing(c, resolveDeps(c))
	addID := func(id, shortestTok, desc string) {
		if id == "" || (!strings.HasPrefix(id, toComplete) && !strings.HasPrefix(shortestTok, toComplete)) || (!opts.allowSelf && id == self) {
			return
		}
		candidates = append(candidates, formatCompletionCandidate(completionIDToken(id, shortestTok, toComplete), desc))
	}
	addPrefixed := func(id, prefix, rawLabel string) {
		if !opts.allowSelf && id == self {
			return
		}
		cleanLabel := cli.SanitizeSingleLine(rawLabel)
		if cleanLabel == "" {
			return
		}
		tok := prefix + cleanLabel
		if !strings.HasPrefix(tok, toComplete) {
			return
		}
		candidates = append(candidates, formatCompletionCandidate(tok, cli.ShortID(id)))
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
		needTitles := matching
		if toComplete != "" && !strings.HasPrefix("title:", toComplete) && !strings.HasPrefix(toComplete, "title:") {
			needTitles = make([]*leapmuxv1.WorkspaceTab, 0, len(matching))
			for _, t := range matching {
				id := t.GetTab().GetTabId()
				if strings.HasPrefix(id, toComplete) && (opts.allowSelf || id != self) {
					needTitles = append(needTitles, t)
				}
			}
		}
		var titles map[string]string
		if len(needTitles) > 0 {
			titles = listing.TabTitles(ctx, needTitles)
		}
		for _, t := range needTitles {
			id := t.GetTab().GetTabId()
			title := titles[id]
			addID(id, prefixes[id], title)
			addPrefixed(id, "title:", title)
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
			addID(w.GetId(), prefixes[w.GetId()], w.GetTitle())
			addPrefixed(w.GetId(), "title:", w.GetTitle())
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
			addID(id, prefixes[id], nodeKind)
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
		needNames := workers
		if toComplete != "" && !strings.HasPrefix("name:", toComplete) && !strings.HasPrefix(toComplete, "name:") {
			needNames = make([]*leapmuxv1.Worker, 0, len(workers))
			for _, w := range workers {
				id := w.GetId()
				if strings.HasPrefix(id, toComplete) && (opts.allowSelf || id != self) {
					needNames = append(needNames, w)
				}
			}
		}
		var names map[string]string
		if len(needNames) > 0 {
			names = listing.WorkerNames(ctx, needNames)
		}
		for _, w := range needNames {
			id := w.GetId()
			name := names[id]
			addID(id, prefixes[id], name)
			addPrefixed(id, "name:", name)
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
	ctx, cancel := completionContext(cmd)
	defer cancel()
	entries, err := pins.List(ctx)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var ids []string
	for _, pin := range entries {
		clean := cli.SanitizeSingleLine(pin.WorkerID)
		if clean != "" && strings.HasPrefix(clean, word) {
			ids = append(ids, formatCompletionCandidate(clean, ""))
		}
	}
	slices.Sort(ids)
	return ids, cobra.ShellCompDirectiveNoFileComp
}
