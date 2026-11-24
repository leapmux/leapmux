package cmd

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/util/agentlabels"
	"github.com/leapmux/leapmux/internal/util/id"
)

func newAgentTreeCommand() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:   "tree [agent]",
		Short: "Show an agent and all its descendant subagents, including unopened ones (default: this agent)",
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
	ID            string          `json:"id"`
	Title         string          `json:"title,omitempty"`
	Provider      string          `json:"provider,omitempty"`
	State         string          `json:"state,omitempty"`
	Status        string          `json:"status,omitempty"`
	ParentAgentID string          `json:"parent_agent_id,omitempty"`
	RootAgentID   string          `json:"root_agent_id,omitempty"`
	SpawnSpanID   string          `json:"spawn_span_id,omitempty"`
	OpenInTab     bool            `json:"open_in_tab"`
	Children      []agentTreeNode `json:"children,omitempty"`
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
	if !n.OpenInTab {
		parts = append(parts, "(unopened)")
	}
	return strings.Join(parts, "  ")
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
	var resp leapmuxv1.ListAgentsResponse
	if err := callInnerRPC(s.ctx, s.c, got.WorkerID, leapmuxv1connect.WorkerAgentServiceListAgentsProcedure,
		&leapmuxv1.ListAgentsRequest{TabIds: []string{got.ID}, IncludeDescendants: true}, &resp); err != nil {
		return nil, err
	}
	if len(resp.GetAgents()) == 0 {
		return nil, cli.Newf(cli.CodeNotFound, "the worker has no agent %s", got.ID)
	}

	// Best-effort check of which agent IDs currently have open workspace tabs.
	openTabs := map[string]bool{}
	if tabs, err := listTabsInWorkspaces(s.ctx, s.c, nil); err == nil {
		for _, t := range tabs {
			if t.GetTab().GetTabType() == leapmuxv1.TabType_TAB_TYPE_AGENT {
				openTabs[t.GetTab().GetTabId()] = true
			}
		}
	}

	byID := map[string]*agentTreeNode{}
	childrenByParent := map[string][]string{}
	for _, a := range resp.GetAgents() {
		node := &agentTreeNode{
			ID:            a.GetId(),
			Title:         a.GetTitle(),
			Provider:      agentlabels.CLIAlias(a.GetAgentProvider()),
			State:         enumToken(a.GetActivityState()),
			Status:        enumToken(a.GetStatus()),
			ParentAgentID: a.GetParentAgentId(),
			RootAgentID:   a.GetRootAgentId(),
			SpawnSpanID:   a.GetSpawnSpanId(),
			OpenInTab:     openTabs[a.GetId()],
		}
		byID[node.ID] = node
		if node.ParentAgentID != "" && node.ID != got.ID {
			childrenByParent[node.ParentAgentID] = append(childrenByParent[node.ParentAgentID], node.ID)
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
	if _, ok := byID[got.ID]; !ok {
		return nil, cli.Newf(cli.CodeNotFound, "the worker has no agent %s", got.ID)
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
		got, err := resolve.Resolve(s.ctx, s.deps(), resolve.Need{WorkspaceID: true, TileID: true}, resolve.Inputs{TileID: t.ID, ExplicitTileID: true})
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
		tileID = firstLeafOf(cc.bs.State, rec.GetRootNodeId())
		if tileID == "" {
			return nil, cli.Newf(cli.CodeNotFound, "workspace %s has no leaf tile", wsID)
		}
	}

	// If workerID wasn't found via live descendant scan (e.g. tombstoned tab
	// still present in CRDT), fall back to the existing CRDT record or --worker.
	if workerID == "" {
		if existing := cc.bs.State.GetTabs()[subagentID]; existing != nil {
			workerID = existing.GetWorkerId().GetValue()
		}
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
	if !verified && cc.bs.State.GetTabs()[subagentID] == nil {
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

	tabIdent := &leapmuxv1.TabIdent{TabId: subagentID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}
	ops := []*leapmuxv1.CrdtOp{
		{
			OpId:      id.Generate(),
			ClientHlc: cc.bs.Clock.Tick(nowMillis()),
			Body: &leapmuxv1.CrdtOp_ReviveTab{
				ReviveTab: &leapmuxv1.ReviveTabOp{Tab: tabIdent},
			},
		},
		{
			OpId:      id.Generate(),
			ClientHlc: cc.bs.Clock.Tick(nowMillis()),
			Body: &leapmuxv1.CrdtOp_SetTabRegister{
				SetTabRegister: &leapmuxv1.SetTabRegisterOp{
					Tab:   tabIdent,
					Field: &leapmuxv1.SetTabRegisterOp_TileId{TileId: resolvedTileID},
				},
			},
		},
		{
			OpId:      id.Generate(),
			ClientHlc: cc.bs.Clock.Tick(nowMillis()),
			Body: &leapmuxv1.CrdtOp_SetTabRegister{
				SetTabRegister: &leapmuxv1.SetTabRegisterOp{
					Tab:   tabIdent,
					Field: &leapmuxv1.SetTabRegisterOp_WorkerId{WorkerId: workerID},
				},
			},
		},
		{
			OpId:      id.Generate(),
			ClientHlc: cc.bs.Clock.Tick(nowMillis()),
			Body: &leapmuxv1.CrdtOp_SetTabRegister{
				SetTabRegister: &leapmuxv1.SetTabRegisterOp{
					Tab:   tabIdent,
					Field: &leapmuxv1.SetTabRegisterOp_Position{Position: resolvedPos},
				},
			},
		},
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

// resolveSubagentTarget resolves an exact ID, "id:<id>", or unique prefix of a
// subagent across the scoped or all online Workers by querying live root agent
// tabs with IncludeDescendants=true. The third return value reports whether the
// subagent was confirmed on its hosting Worker.
func resolveSubagentTarget(s *session, raw string) (string, string, bool, error) {
	literal, isExplicit := strings.CutPrefix(raw, "id:")
	if isExplicit {
		raw = strings.TrimSpace(literal)
		if raw == "" {
			return "", "", false, cli.Usagef(`"id:" needs an id after the colon`)
		}
	}
	tabs, err := s.listWorkspaceTabs(leapmuxv1.TabType_TAB_TYPE_AGENT)
	if err != nil {
		// Fall back to explicit full ID if listing fails and caller gave a full ID.
		if isExplicit || len(raw) >= 36 {
			return raw, "", false, nil
		}
		return "", "", false, err
	}
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
	type subMatch struct {
		id       string
		workerID string
		title    string
	}
	var matches []subMatch
	seen := map[string]bool{}
	for wk, rootIDs := range byWorker {
		var resp leapmuxv1.ListAgentsResponse
		if err := callInnerRPC(s.ctx, s.c, wk, leapmuxv1connect.WorkerAgentServiceListAgentsProcedure, &leapmuxv1.ListAgentsRequest{
			TabIds:             rootIDs,
			IncludeDescendants: true,
		}, &resp); err != nil {
			continue
		}
		for _, a := range resp.GetAgents() {
			aid := a.GetId()
			if seen[aid] {
				continue
			}
			if aid == raw {
				return aid, wk, true, nil
			}
			if !isExplicit && len(raw) >= resolve.MinPrefixLength && strings.HasPrefix(aid, raw) {
				seen[aid] = true
				matches = append(matches, subMatch{id: aid, workerID: wk, title: a.GetTitle()})
			}
		}
	}
	if len(matches) == 1 {
		return matches[0].id, matches[0].workerID, true, nil
	}
	if len(matches) > 1 {
		cands := make([]cli.Candidate, 0, len(matches))
		for _, m := range matches {
			cands = append(cands, cli.Candidate{ID: m.id, Kind: "agent", Title: m.title})
		}
		sort.Slice(cands, func(i, j int) bool { return cands[i].ID < cands[j].ID })
		e := cli.Newf(cli.CodeAmbiguousTarget, "%d subagents match prefix %q", len(matches), raw).
			WithHint("type more characters of the subagent id, or the full id")
		e.Candidates = cands
		return "", "", false, e
	}
	if isExplicit || len(raw) >= 36 {
		return raw, explicitWorker, false, nil
	}
	if len(raw) < resolve.MinPrefixLength {
		return "", "", false, cli.Usagef("%q is too short for a subagent id prefix; type at least %d characters or use id:<id>", raw, resolve.MinPrefixLength)
	}
	return "", "", false, cli.Newf(cli.CodeNotFound, "no subagent matches %q", raw).
		WithHint(`run "leapmux agent tree" to list subagent ids`)
}

func firstLeafOf(state *leapmuxv1.UserMaterialized, nodeID string) string {
	childrenByParent := crdt.LiveChildrenByParent(state)
	var walk func(id string) string
	walk = func(id string) string {
		node := state.GetNodes()[id]
		if node == nil || !crdt.HLCIsZero(node.GetTombstoneAt()) {
			return ""
		}
		if node.GetKind().GetValue() == leapmuxv1.NodeKind_NODE_KIND_LEAF {
			return id
		}
		for _, childID := range childrenByParent[id] {
			if found := walk(childID); found != "" {
				return found
			}
		}
		return ""
	}
	return walk(nodeID)
}
