package cmd

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/sync/errgroup"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/util/agentlabels"
)

func newAgentTreeCommand() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:   "tree [agent]",
		Short: "Show an agent and its descendant subagents and spawned agents (default: this agent)",
		Examples: []cli.Example{
			{Command: "leapmux agent tree"},
			{Command: "leapmux agent tree 87a19d5f"},
		},
		Flags:    addTargetWorkspaceFlag,
		Complete: completeTargets,
		Run:      runAgentTree,
	})
}

type agentTreeNode struct {
	ID                string          `json:"id"`
	Title             string          `json:"title,omitempty"`
	Provider          string          `json:"provider,omitempty"`
	State             string          `json:"state,omitempty"`
	Status            string          `json:"status,omitempty"`
	ParentAgentID     string          `json:"parent_agent_id,omitempty"`
	RootAgentID       string          `json:"root_agent_id,omitempty"`
	SpawnSpanID       string          `json:"spawn_span_id,omitempty"`
	SpawnedByAgentID  string          `json:"spawned_by_agent_id,omitempty"`
	ForkedFromAgentID string          `json:"forked_from_agent_id,omitempty"`
	Relation          string          `json:"relation,omitempty"`
	OpenInTab         bool            `json:"open_in_tab"`
	Children          []agentTreeNode `json:"children,omitempty"`
}

type agentTreeResult struct {
	Root agentTreeNode `json:"root"`
}

func (r agentTreeResult) QuietLines() []string {
	var ids []string
	var walk func(n agentTreeNode)
	walk = func(n agentTreeNode) {
		if n.ID != "" {
			ids = append(ids, n.ID)
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(r.Root)
	return ids
}

func (r agentTreeResult) RenderText(w io.Writer) error {
	return renderAgentTreeNode(w, r.Root, "", true, true)
}

func renderAgentTreeNode(w io.Writer, n agentTreeNode, prefix string, last, isRoot bool) error {
	var line string
	if isRoot {
		line = formatAgentTreeNode(n)
	} else {
		conn := "├─ "
		if last {
			conn = "└─ "
		}
		line = prefix + conn + formatAgentTreeNode(n)
	}
	if _, err := fmt.Fprintln(w, line); err != nil {
		return err
	}
	nextPrefix := prefix
	if !isRoot {
		if last {
			nextPrefix += "   "
		} else {
			nextPrefix += "│  "
		}
	}
	for i, child := range n.Children {
		if err := renderAgentTreeNode(w, child, nextPrefix, i == len(n.Children)-1, false); err != nil {
			return err
		}
	}
	return nil
}

func formatAgentTreeNode(n agentTreeNode) string {
	var parts []string
	parts = append(parts, cli.ShortID(n.ID))
	if n.Provider != "" {
		parts = append(parts, n.Provider)
	}
	if n.State != "" {
		parts = append(parts, n.State)
	} else if n.Status != "" {
		parts = append(parts, n.Status)
	}
	if n.Title != "" {
		parts = append(parts, fmt.Sprintf("%q", cli.SanitizeSingleLine(n.Title)))
	}
	if n.Relation != "" {
		parts = append(parts, "["+n.Relation+"]")
	}
	if !n.OpenInTab {
		parts = append(parts, "(unopened)")
	}
	return strings.Join(parts, "  ")
}

func agentTreeNodeOf(a *leapmuxv1.AgentInfo, openInTab bool, relation string) *agentTreeNode {
	return &agentTreeNode{
		ID:                a.GetId(),
		Title:             a.GetTitle(),
		Provider:          agentlabels.CLIAlias(a.GetAgentProvider()),
		State:             enumToken(a.GetActivityState()),
		Status:            enumToken(a.GetStatus()),
		ParentAgentID:     a.GetParentAgentId(),
		RootAgentID:       a.GetRootAgentId(),
		SpawnSpanID:       a.GetSpawnSpanId(),
		SpawnedByAgentID:  a.GetSpawnedByAgentId(),
		ForkedFromAgentID: a.GetForkedFromAgentId(),
		Relation:          relation,
		OpenInTab:         openInTab,
	}
}

func runAgentTree(inv *cli.Invocation) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	got, err := s.resolveTab(resolve.KindAgent, inv.Arg("agent"), resolve.Need{WorkerID: true})
	if err != nil {
		return nil, err
	}
	var (
		resp leapmuxv1.ListAgentsResponse
		tabs []*leapmuxv1.WorkspaceTab
	)
	g, gctx := errgroup.WithContext(s.ctx)
	g.Go(func() error {
		return callInnerRPC(gctx, s.c, got.WorkerID, leapmuxv1connect.WorkerAgentServiceListAgentsProcedure,
			&leapmuxv1.ListAgentsRequest{TabIds: []string{got.ID}, IncludeDescendants: true}, &resp)
	})
	g.Go(func() error {
		var listErr error
		tabs, listErr = s.listAllTabs(gctx)
		return listErr
	})
	if err := g.Wait(); err != nil {
		return nil, err
	}
	if len(resp.GetAgents()) == 0 {
		return nil, cli.Newf(cli.CodeNotFound, "the worker has no agent %s", got.ID)
	}

	openTabs := map[string]bool{}
	tabWorker := map[string]string{}
	agentTabs := filterTabsByType(tabs, leapmuxv1.TabType_TAB_TYPE_AGENT)
	for _, t := range agentTabs {
		tid := t.GetTab().GetTabId()
		openTabs[tid] = true
		if wk := t.GetWorkerId(); wk != "" {
			tabWorker[tid] = wk
		}
	}

	byID := map[string]*agentTreeNode{}
	childrenByParent := map[string][]string{}
	seenEdge := map[string]bool{}
	addEdge := func(parentID, childID string) {
		key := parentID + "\x00" + childID
		if seenEdge[key] {
			return
		}
		seenEdge[key] = true
		childrenByParent[parentID] = append(childrenByParent[parentID], childID)
	}

	queue := make([]string, 0, len(resp.GetAgents()))
	enqueued := map[string]bool{}
	enqueue := func(id string) {
		if id == "" || enqueued[id] {
			return
		}
		enqueued[id] = true
		queue = append(queue, id)
	}

	for _, a := range resp.GetAgents() {
		rel := ""
		if a.GetParentAgentId() != "" && a.GetId() != got.ID {
			rel = "subagent"
		}
		node := agentTreeNodeOf(a, openTabs[a.GetId()], rel)
		byID[node.ID] = node
		if rel == "subagent" {
			addEdge(node.ParentAgentID, node.ID)
		}
		enqueue(node.ID)
	}
	if _, ok := byID[got.ID]; !ok {
		return nil, cli.Newf(cli.CodeNotFound, "the worker has no agent %s", got.ID)
	}

	if len(agentTabs) > 0 {
		details := s.fetchTabDetailsCached(s.ctx, agentTabs)
		spawnedByParent := map[string][]*leapmuxv1.AgentInfo{}
		for _, t := range agentTabs {
			d := details[t.GetTab().GetTabId()]
			if d.Agent == nil {
				continue
			}
			if spawner := d.Agent.GetSpawnedByAgentId(); spawner != "" {
				spawnedByParent[spawner] = append(spawnedByParent[spawner], d.Agent)
			}
		}

		for len(queue) > 0 {
			var nextQueue []string
			spawnedByWorker := map[string][]string{}
			for _, pid := range queue {
				for _, child := range spawnedByParent[pid] {
					cid := child.GetId()
					if cid == "" || cid == got.ID {
						continue
					}
					if _, exists := byID[cid]; exists {
						continue
					}
					byID[cid] = agentTreeNodeOf(child, openTabs[cid], "spawned")
					addEdge(pid, cid)
					if !enqueued[cid] {
						enqueued[cid] = true
						nextQueue = append(nextQueue, cid)
					}
					if wk := tabWorker[cid]; wk != "" {
						spawnedByWorker[wk] = append(spawnedByWorker[wk], cid)
					}
				}
			}
			if len(spawnedByWorker) > 0 {
				var mu sync.Mutex
				var subGroup errgroup.Group
				subGroup.SetLimit(detailFanoutLimit)
				for wk, ids := range spawnedByWorker {
					wk, ids := wk, ids
					subGroup.Go(func() error {
						var subResp leapmuxv1.ListAgentsResponse
						if err := callInnerRPC(s.ctx, s.c, wk, leapmuxv1connect.WorkerAgentServiceListAgentsProcedure,
							&leapmuxv1.ListAgentsRequest{TabIds: ids, IncludeDescendants: true}, &subResp); err != nil {
							return nil
						}
						mu.Lock()
						for _, a := range subResp.GetAgents() {
							aid := a.GetId()
							if aid == "" || aid == got.ID || a.GetParentAgentId() == "" {
								continue
							}
							if _, exists := byID[aid]; exists {
								continue
							}
							byID[aid] = agentTreeNodeOf(a, openTabs[aid], "subagent")
							addEdge(a.GetParentAgentId(), aid)
							if !enqueued[aid] {
								enqueued[aid] = true
								nextQueue = append(nextQueue, aid)
							}
						}
						mu.Unlock()
						return nil
					})
				}
				_ = subGroup.Wait()
			}
			queue = nextQueue
		}
	}

	visited := map[string]bool{}
	var build func(id string) agentTreeNode
	build = func(id string) agentTreeNode {
		node := *byID[id]
		if visited[id] {
			return node
		}
		visited[id] = true
		childIDs := childrenByParent[id]
		sort.Strings(childIDs)
		for _, cid := range childIDs {
			if visited[cid] {
				continue
			}
			node.Children = append(node.Children, build(cid))
		}
		return node
	}
	return agentTreeResult{Root: build(got.ID)}, nil
}

// --- agent open-tab ---

func newAgentOpenTabCommand() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:     "open-tab <subagent>",
		Short:   "Open or revive a subagent's transcript as a tab in a workspace",
		Mutates: true,
		Args:    map[string]string{"subagent": `The subagent id (from "agent tree" or "agent tasks").`},
		Examples: []cli.Example{
			{Command: "leapmux agent open-tab 5e9a01bc"},
			{Command: "leapmux agent open-tab 5e9a01bc --workspace self"},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.String("workspace", "", `workspace to open the tab in (default: this tab's workspace, or sole active workspace)`)
			fs.String("tile", "", "tile to open the tab in (default: the workspace's first tile)")
			addWorkerFlag(fs)
		},
		Run: runAgentOpenTab,
	})
}

type agentOpenTabResult struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	TileID      string `json:"tile_id"`
	WorkerID    string `json:"worker_id,omitempty"`
	Position    string `json:"position,omitempty"`
}

func runAgentOpenTab(inv *cli.Invocation) (any, error) {
	subagentInput := strings.TrimSpace(inv.Arg("subagent"))
	if subagentInput == "" {
		return nil, cli.Usagef("<subagent> is empty")
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()

	// Resolve <subagent> (exact ID, id:<id>, or unique prefix) and locate its
	// owning Worker so ReviveTabOp includes the required worker_id register.
	subagentID, workerID, verified, err := resolveSubagentTarget(s, subagentInput)
	if err != nil {
		return nil, err
	}

	var wsID, tileID string
	if inv.Changed("tile") {
		t, err := s.resolveOne(resolve.KindTile, stringFlag(inv, "tile"))
		if err != nil {
			return nil, err
		}
		in := resolve.Inputs{TileID: t.ID, ExplicitTileID: true}
		if inv.Changed("workspace") {
			ws, err := s.resolveOne(resolve.KindWorkspace, stringFlag(inv, "workspace"))
			if err != nil {
				return nil, err
			}
			in.WorkspaceID, in.ExplicitWorkspaceID = ws.ID, true
		}
		got, err := resolve.Resolve(s.ctx, s.deps(), resolve.Need{WorkspaceID: true, TileID: true}, in)
		if err != nil {
			return nil, err
		}
		wsID, tileID = got.WorkspaceID, got.TileID
	} else {
		wsInput := stringFlag(inv, "workspace")
		if wsInput == "" && callingTabID() == "" {
			if def, derr := s.defaultWorkspaceID(); derr != nil {
				return nil, derr
			} else if def != "" {
				wsInput = def
			}
		}
		ws, err := s.resolveOne(resolve.KindWorkspace, wsInput)
		if err != nil {
			return nil, err
		}
		wsID = ws.ID
	}

	cc, err := s.openCRDT(wsID)
	if err != nil {
		return nil, err
	}
	if tileID == "" {
		rec := cc.bs.State.GetWorkspaces()[wsID]
		if rec == nil {
			return nil, cli.Newf(cli.CodeNotFound, "no such workspace: %s", wsID)
		}
		tileID = firstLiveLeaf(cc.bs.State, rec.GetRootNodeId())
		if tileID == "" {
			return nil, cli.Newf(cli.CodeNotFound, "workspace %s has no leaf tile", wsID)
		}
	}

	existing := cc.bs.State.GetTabs()[subagentID]
	if existing != nil && existing.GetTabType() != leapmuxv1.TabType_TAB_TYPE_AGENT {
		return nil, cli.Newf(cli.CodeInvalidArgument, "tab %s has type %s; expected %s",
			subagentID, resolve.TabTypeWireName(existing.GetTabType()), resolve.TabTypeWireName(leapmuxv1.TabType_TAB_TYPE_AGENT))
	}

	// If workerID wasn't found via live descendant scan (e.g. tombstoned tab
	// still present in CRDT), fall back to the existing CRDT record or --worker.
	if workerID == "" && existing != nil {
		workerID = existing.GetWorkerId().GetValue()
	}
	if workerID == "" {
		if wk, werr := s.workerScope(); werr == nil {
			workerID = wk
		}
	}
	if workerID == "" {
		return nil, cli.Newf(cli.CodeUsage, "could not determine the worker that hosts subagent %s", subagentID).
			WithHint("pass --worker <worker> to specify the hosting worker")
	}

	// Verify the subagent exists on workerID (or already exists in CRDT) before
	// submitting ReviveTabOp so a nonexistent ID cannot materialize a ghost tab.
	if !verified && existing == nil {
		var checkResp leapmuxv1.ListAgentsResponse
		if err := callInnerRPC(s.ctx, s.c, workerID, leapmuxv1connect.WorkerAgentServiceListAgentsProcedure, &leapmuxv1.ListAgentsRequest{
			TabIds: []string{subagentID},
		}, &checkResp); err != nil {
			return nil, err
		}
		found := false
		for _, a := range checkResp.GetAgents() {
			if a.GetId() == subagentID {
				found = true
				break
			}
		}
		if !found {
			return nil, cli.Newf(cli.CodeNotFound, "no subagent %s exists on worker %s", subagentID, cli.ShortID(workerID)).
				WithHint(`run "leapmux agent tree" to list subagent ids`)
		}
	}

	resolvedTileID, resolvedPos, err := resolvePositionSpec(cc.bs.State, tileID, "", positionSpec{kind: positionLast})
	if err != nil {
		return nil, err
	}

	ops := []*leapmuxv1.CrdtOp{
		opReviveTab(cc.bs, leapmuxv1.TabType_TAB_TYPE_AGENT, subagentID),
		opSetTabTileID(cc.bs, leapmuxv1.TabType_TAB_TYPE_AGENT, subagentID, resolvedTileID),
		opSetTabWorkerID(cc.bs, leapmuxv1.TabType_TAB_TYPE_AGENT, subagentID, workerID),
		opSetTabPosition(cc.bs, leapmuxv1.TabType_TAB_TYPE_AGENT, subagentID, resolvedPos),
	}
	if err := cc.submitOps(ops); err != nil {
		return nil, err
	}
	return agentOpenTabResult{
		ID:          subagentID,
		WorkspaceID: wsID,
		TileID:      resolvedTileID,
		WorkerID:    workerID,
		Position:    resolvedPos,
	}, nil
}

func opReviveTab(bs *CRDTBootstrap, tabType leapmuxv1.TabType, tabID string) *leapmuxv1.CrdtOp {
	op := envelope(bs)
	op.Body = &leapmuxv1.CrdtOp_ReviveTab{
		ReviveTab: &leapmuxv1.ReviveTabOp{Tab: &leapmuxv1.TabIdent{TabType: tabType, TabId: tabID}},
	}
	return op
}

func opSetTabWorkerID(bs *CRDTBootstrap, tabType leapmuxv1.TabType, tabID, workerID string) *leapmuxv1.CrdtOp {
	op, inner := newSetTabRegisterOp(bs, tabType, tabID)
	inner.Field = &leapmuxv1.SetTabRegisterOp_WorkerId{WorkerId: workerID}
	return op
}

// resolveSubagentTarget resolves an exact ID, "id:<id>" (exact ID or unique
// prefix, never matching titles), or bare unique prefix of a subagent across
// all readable workspaces (never restricted by destination --workspace) by
// querying live root agent tabs with IncludeDescendants=true. The third return
// value reports whether the subagent was confirmed on its hosting Worker.
func resolveSubagentTarget(s *session, raw string) (string, string, bool, error) {
	literal, isExplicit := strings.CutPrefix(raw, "id:")
	if isExplicit {
		raw = strings.TrimSpace(literal)
		if raw == "" {
			return "", "", false, cli.Usagef(`"id:" needs an id after the colon`)
		}
	}
	isFullID := resolve.ExplicitID(resolve.KindAgent, raw) != ""
	allTabs, err := listTabsInWorkspaces(s.ctx, s.c, nil)
	if err != nil {
		// Fall back to full ID if listing fails and caller gave a complete ID.
		if isFullID {
			return raw, "", false, nil
		}
		return "", "", false, err
	}
	tabs := filterTabsByType(allTabs, leapmuxv1.TabType_TAB_TYPE_AGENT)
	var explicitWorker string
	if s.inv.Changed(workerFlag) {
		wk, err := s.workerScope()
		if err != nil {
			return "", "", false, err
		}
		explicitWorker = wk
	}
	byWorker := map[string][]string{}
	for _, t := range tabs {
		wk := t.GetWorkerId()
		if wk == "" || (explicitWorker != "" && wk != explicitWorker) {
			continue
		}
		byWorker[wk] = append(byWorker[wk], t.GetTab().GetTabId())
	}
	matchedID, matchedWorker, verified, err := resolveSubagentAcrossWorkers(
		s.ctx, raw, byWorker,
		func(ctx context.Context, wk string, rootIDs []string) ([]*leapmuxv1.AgentInfo, error) {
			var resp leapmuxv1.ListAgentsResponse
			if err := callInnerRPC(ctx, s.c, wk, leapmuxv1connect.WorkerAgentServiceListAgentsProcedure, &leapmuxv1.ListAgentsRequest{
				TabIds:             rootIDs,
				IncludeDescendants: true,
			}, &resp); err != nil {
				return nil, err
			}
			return resp.GetAgents(), nil
		},
	)
	if err != nil {
		return "", "", false, err
	}
	if verified {
		return matchedID, matchedWorker, true, nil
	}
	if isFullID {
		return raw, explicitWorker, false, nil
	}
	if !isExplicit && len(raw) < resolve.MinPrefixLength {
		return "", "", false, cli.Usagef("%q is too short for a subagent id prefix; type at least %d characters or use id:<id>", raw, resolve.MinPrefixLength)
	}
	return "", "", false, cli.Newf(cli.CodeNotFound, "no subagent matches %q", raw).
		WithHint(`run "leapmux agent tree" to list subagent ids`)
}

// resolveSubagentAcrossWorkers fans out descendant agent queries across
// workers concurrently (capped at detailFanoutLimit) and selects an exact ID
// (at any length) or unique prefix match (>= resolve.MinPrefixLength)
// deterministically by worker ID and agent ID.
func resolveSubagentAcrossWorkers(
	ctx context.Context,
	raw string,
	byWorker map[string][]string,
	listWorkerAgents func(ctx context.Context, workerID string, tabIDs []string) ([]*leapmuxv1.AgentInfo, error),
) (string, string, bool, error) {
	workerIDs := make([]string, 0, len(byWorker))
	for wk := range byWorker {
		workerIDs = append(workerIDs, wk)
	}
	sort.Strings(workerIDs)

	results := make([][]*leapmuxv1.AgentInfo, len(workerIDs))
	var g errgroup.Group
	g.SetLimit(detailFanoutLimit)
	for i, wk := range workerIDs {
		rootIDs := append([]string(nil), byWorker[wk]...)
		sort.Strings(rootIDs)
		g.Go(func() error {
			agents, err := listWorkerAgents(ctx, wk, rootIDs)
			if err != nil {
				return nil
			}
			results[i] = agents
			return nil
		})
	}
	_ = g.Wait()

	type subMatch struct {
		id       string
		workerID string
		title    string
	}
	var exact []subMatch
	var prefix []subMatch
	seenExact := map[string]bool{}
	seenPrefix := map[string]bool{}
	for i, wk := range workerIDs {
		agents := append([]*leapmuxv1.AgentInfo(nil), results[i]...)
		sort.Slice(agents, func(a, b int) bool { return agents[a].GetId() < agents[b].GetId() })
		for _, a := range agents {
			aid := a.GetId()
			if aid == raw {
				if !seenExact[aid] {
					seenExact[aid] = true
					exact = append(exact, subMatch{id: aid, workerID: wk, title: a.GetTitle()})
				}
				continue
			}
			if len(raw) >= resolve.MinPrefixLength && strings.HasPrefix(aid, raw) && !seenPrefix[aid] {
				seenPrefix[aid] = true
				prefix = append(prefix, subMatch{id: aid, workerID: wk, title: a.GetTitle()})
			}
		}
	}
	if len(exact) > 0 {
		return exact[0].id, exact[0].workerID, true, nil
	}
	if len(prefix) == 1 {
		return prefix[0].id, prefix[0].workerID, true, nil
	}
	if len(prefix) > 1 {
		sort.Slice(prefix, func(i, j int) bool {
			if prefix[i].id != prefix[j].id {
				return prefix[i].id < prefix[j].id
			}
			return prefix[i].workerID < prefix[j].workerID
		})
		cands := make([]cli.Candidate, 0, len(prefix))
		for _, m := range prefix {
			cands = append(cands, cli.Candidate{ID: m.id, Kind: "agent", Title: m.title})
		}
		e := cli.Newf(cli.CodeAmbiguousTarget, "%d subagents match prefix %q", len(prefix), raw).
			WithHint("type more characters of the subagent id, or the full id")
		e.Candidates = cands
		return "", "", false, e
	}
	return "", "", false, nil
}
