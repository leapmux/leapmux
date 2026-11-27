package cmd

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"connectrpc.com/connect/v2"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/sync/errgroup"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/util/agentlabels"
)

func newTreeCommand() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:     "tree",
		Aliases: []string{"overview", "ps"},
		Short:   "Show a hierarchical overview of sections, workspaces, tiles, agents, terminals and files",
		Next: []string{
			"leapmux agent get <id>",
			`leapmux agent send <id> "..." --wait`,
			"leapmux terminal read <id>",
		},
		Examples: []cli.Example{
			{Command: "leapmux tree"},
			{Comment: "Show only one workspace.", Command: "leapmux tree --workspace self"},
			{Comment: "Include archived workspaces.", Command: "leapmux tree --archived"},
			{Comment: "Print a flat table of every tab.", Command: "leapmux tree --flat"},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.String("workspace", "", `show only this workspace (an id, a prefix, "title:<title>" or "self")`)
			fs.String("worker", "", `show only tabs hosted by this worker (an id, a prefix, "name:<name>" or "self")`)
			fs.Bool("archived", false, "include archived workspaces")
			fs.Bool("flat", false, "print a flat table of tabs instead of a hierarchy")
		},
		Run: runFleetTree,
	})
}

type treeTab struct {
	ID              string    `json:"id" table:"ID,short"`
	Type            string    `json:"type" table:"TYPE"`
	Title           string    `json:"title,omitempty" table:"TITLE"`
	Provider        string    `json:"provider,omitempty" table:"PROVIDER"`
	State           string    `json:"state,omitempty" table:"STATE,state"`
	Branch          string    `json:"branch,omitempty" table:"BRANCH"`
	Dirty           bool      `json:"dirty,omitempty"`
	Worker          string    `json:"worker,omitempty" table:"WORKER"`
	WorkerID        string    `json:"worker_id"`
	WorkspaceID     string    `json:"workspace_id" table:"WORKSPACE,short"`
	TileID          string    `json:"tile_id,omitempty"`
	Here            bool      `json:"here,omitempty"`
	RunningCommands []string  `json:"running_commands,omitempty"`
	PendingRequests []string  `json:"pending_requests,omitempty"`
	Children        []treeTab `json:"children,omitempty"`
}

type treeTile struct {
	ID       string    `json:"id"`
	Floating bool      `json:"floating,omitempty"`
	Tabs     []treeTab `json:"tabs"`
}

type treeWorkspace struct {
	ID       string     `json:"id"`
	Title    string     `json:"title"`
	Section  string     `json:"section,omitempty"`
	Archived bool       `json:"archived,omitempty"`
	Tiles    []treeTile `json:"tiles"`
}

type treeSection struct {
	Name       string          `json:"name"`
	Archived   bool            `json:"archived,omitempty"`
	Workspaces []treeWorkspace `json:"workspaces"`
}

type treeResult struct {
	Sections           []treeSection `json:"sections"`
	ArchivedWorkspaces int           `json:"archived_workspaces,omitempty"`
}

func (r treeResult) QuietLines() []string {
	var ids []string
	var walk func(tabs []treeTab)
	walk = func(tabs []treeTab) {
		for _, t := range tabs {
			if t.ID != "" {
				ids = append(ids, t.ID)
			}
			walk(t.Children)
		}
	}
	for _, sec := range r.Sections {
		for _, ws := range sec.Workspaces {
			for _, tile := range ws.Tiles {
				walk(tile.Tabs)
			}
		}
	}
	return ids
}

func (r treeResult) RenderText(w io.Writer) error {
	if len(r.Sections) == 0 && r.ArchivedWorkspaces == 0 {
		_, err := fmt.Fprintln(w, "(no workspaces)")
		return err
	}
	for sIdx, sec := range r.Sections {
		if sIdx > 0 {
			if _, err := fmt.Fprintln(w); err != nil {
				return err
			}
		}
		if sec.Name != "" {
			if _, err := fmt.Fprintln(w, cli.SanitizeSingleLine(sec.Name)); err != nil {
				return err
			}
		}
		for wIdx, ws := range sec.Workspaces {
			wsConn, wsPrefix := treeBranch("", wIdx == len(sec.Workspaces)-1)
			header := fmt.Sprintf("%sworkspace %s  %q", wsConn, cli.ShortID(ws.ID), cli.SanitizeSingleLine(ws.Title))
			if ws.Archived {
				header += " [archived]"
			}
			if len(ws.Tiles) == 0 {
				header += " (empty)"
			}
			if _, err := fmt.Fprintln(w, header); err != nil {
				return err
			}
			for tIdx, tile := range ws.Tiles {
				tileConn, tilePrefix := treeBranch(wsPrefix, tIdx == len(ws.Tiles)-1)
				kind := "tile"
				if tile.Floating {
					kind = "floating tile"
				}
				if _, err := fmt.Fprintf(w, "%s%s%s %s\n", wsPrefix, tileConn, kind, cli.ShortID(tile.ID)); err != nil {
					return err
				}
				if err := renderTreeTabs(w, tile.Tabs, tilePrefix); err != nil {
					return err
				}
			}
		}
	}
	if r.ArchivedWorkspaces > 0 {
		if _, err := fmt.Fprintf(w, "Archived (%s; pass --archived to show)\n", pluralize(r.ArchivedWorkspaces, "workspace")); err != nil {
			return err
		}
	}
	return nil
}

func renderTreeTabs(w io.Writer, tabs []treeTab, prefix string) error {
	for i, tab := range tabs {
		conn, childPrefix := treeBranch(prefix, i == len(tabs)-1)
		line := prefix + conn + formatTreeTabLine(tab)
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
		if len(tab.Children) > 0 {
			if err := renderTreeTabs(w, tab.Children, childPrefix); err != nil {
				return err
			}
		}
	}
	return nil
}

func formatTreeTabLine(tab treeTab) string {
	var parts []string
	parts = append(parts, cli.ShortID(tab.ID), tab.Type)
	if tab.Provider != "" {
		parts = append(parts, tab.Provider)
	}
	if tab.State != "" {
		parts = append(parts, tab.State)
	}
	if tab.Branch != "" {
		br := cli.SanitizeSingleLine(tab.Branch)
		if tab.Dirty {
			br += " *"
		}
		parts = append(parts, "["+br+"]")
	}
	if tab.Title != "" {
		parts = append(parts, fmt.Sprintf("%q", cli.SanitizeSingleLine(tab.Title)))
	}
	if len(tab.PendingRequests) > 0 {
		parts = append(parts, fmt.Sprintf("[%s: %s]", pluralize(len(tab.PendingRequests), "request"), strings.Join(tab.PendingRequests, ", ")))
	}
	if len(tab.RunningCommands) > 0 {
		parts = append(parts, "(running: "+cli.SanitizeSingleLine(strings.Join(tab.RunningCommands, ", "))+")")
	}
	if len(tab.Children) > 0 {
		parts = append(parts, fmt.Sprintf("(%s)", pluralize(len(tab.Children), "subagent")))
	}
	if tab.Here {
		parts = append(parts, "◀ here")
	}
	return strings.Join(parts, "  ")
}

type treeTileKey struct {
	wsID, tileID string
}

type treeSnapshot struct {
	workspaces     []*leapmuxv1.Workspace
	sections       []*leapmuxv1.Section
	sectionIdx     *sectionIndex
	tabs           []*leapmuxv1.WorkspaceTab
	wdata          workerTabData
	floatingTiles  map[string]bool
	workerFilterID string
	archivedHidden int
}

type assembledTreeTabs struct {
	flatRows      []treeTab
	tabsByTile    map[treeTileKey][]treeTab
	tileOrderByWS map[string][]string
}

func runFleetTree(inv *cli.Invocation) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()

	flat := boolFlag(inv, "flat")
	snap, err := fetchTreeSnapshot(s, inv, flat)
	if err != nil {
		return nil, err
	}
	assembled := assembleTreeTabs(snap.tabs, snap.wdata, callingTabID())
	if flat {
		return struct {
			Tabs []treeTab `json:"tabs"`
		}{Tabs: assembled.flatRows}, nil
	}
	return groupTreeSections(
		snap.workspaces,
		snap.sections,
		snap.sectionIdx,
		assembled,
		snap.floatingTiles,
		snap.workerFilterID != "",
		snap.archivedHidden,
	), nil
}

// fetchTreeSnapshot gathers the Hub workspace/section lists, optional worker
// filter resolution, visible tabs, Worker-side details, and (in hierarchy
// mode) floating-window tiles with independent RPCs overlapped.
func fetchTreeSnapshot(s *session, inv *cli.Invocation, flat bool) (treeSnapshot, error) {
	var (
		wsResp         leapmuxv1.ListWorkspacesResponse
		secResp        leapmuxv1.ListSectionsResponse
		workerFilterID string
	)
	g, gctx := errgroup.WithContext(s.ctx)
	g.Go(func() error {
		return hubCallUnary(gctx, s.c, leapmuxv1connect.WorkspaceServiceListWorkspacesProcedure, &leapmuxv1.ListWorkspacesRequest{}, &wsResp)
	})
	g.Go(func() error {
		err := hubCallUnary(gctx, s.c, leapmuxv1connect.SectionServiceListSectionsProcedure, &leapmuxv1.ListSectionsRequest{}, &secResp)
		// Match loadArchivedSectionIndex: tolerate PermissionDenied from a
		// narrow delegation bearer that cannot read sidebar sections, while
		// propagating every other Hub or transport failure.
		if err != nil && connect.CodeOf(err) != connect.CodePermissionDenied {
			return err
		}
		return nil
	})
	if inv.Changed("worker") {
		workerInput := stringFlag(inv, "worker")
		if workerInput == "" {
			workerInput = resolve.SelfTarget
		}
		g.Go(func() error {
			w, err := resolve.Canonical(gctx, s.listing(), resolve.KindWorker, workerInput)
			if err != nil {
				return err
			}
			workerFilterID = w.ID
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return treeSnapshot{}, err
	}

	var wsFilterID string
	if inv.Changed("workspace") {
		listing := s.listing()
		listing.Workspaces = func(context.Context) ([]*leapmuxv1.Workspace, error) {
			return wsResp.GetWorkspaces(), nil
		}
		listing.WorkspacesByIDPrefix = nil
		wsInput := stringFlag(inv, "workspace")
		if wsInput == "" {
			wsInput = resolve.SelfTarget
		}
		ws, err := resolve.Canonical(s.ctx, listing, resolve.KindWorkspace, wsInput)
		if err != nil {
			return treeSnapshot{}, err
		}
		wsFilterID = ws.ID
	}

	secIdx := indexSections(&secResp)
	includeArchived := boolFlag(inv, "archived")
	var workspaces []*leapmuxv1.Workspace
	archivedHidden := 0
	for _, ws := range wsResp.GetWorkspaces() {
		if wsFilterID != "" && ws.GetId() != wsFilterID {
			continue
		}
		if secIdx.isArchived(ws.GetId()) && !includeArchived && wsFilterID == "" {
			archivedHidden++
			continue
		}
		workspaces = append(workspaces, ws)
	}

	wsIDs := make([]string, 0, len(workspaces))
	for _, ws := range workspaces {
		wsIDs = append(wsIDs, ws.GetId())
	}

	var (
		tabs          []*leapmuxv1.WorkspaceTab
		wdata         workerTabData
		floatingTiles map[string]bool
	)
	if len(wsIDs) > 0 {
		g2, g2ctx := errgroup.WithContext(s.ctx)
		g2.Go(func() error {
			var err error
			tabs, err = listTabsInWorkspaces(g2ctx, s.c, wsIDs)
			if err != nil {
				return err
			}
			if workerFilterID != "" {
				tabs = filterTabsByWorker(tabs, workerFilterID)
			}
			wdata = fetchWorkerTabData(g2ctx, s.c, tabs, workerTabDataOptions{
				IncludeDescendants:       true,
				IncludeTerminalProcesses: true,
				IncludeControlRequests:   true,
				IncludeWorkerNames:       true,
			})
			return nil
		})
		if !flat {
			g2.Go(func() error {
				// Best-effort CRDT bootstrap to identify floating-window tiles accurately.
				if bs, berr := crdtBootstrap(g2ctx, s.c, wsIDs); berr == nil && bs != nil && bs.State != nil {
					floatingTiles = collectFloatingTiles(bs.State)
				}
				return nil
			})
		}
		if err := g2.Wait(); err != nil {
			return treeSnapshot{}, err
		}
	}

	return treeSnapshot{
		workspaces:     workspaces,
		sections:       secResp.GetSections(),
		sectionIdx:     secIdx,
		tabs:           tabs,
		wdata:          wdata,
		floatingTiles:  floatingTiles,
		workerFilterID: workerFilterID,
		archivedHidden: archivedHidden,
	}, nil
}

// collectFloatingTiles returns every live tile node ID belonging to a live
// floating window, building the parent-to-children index once.
func collectFloatingTiles(state *leapmuxv1.UserMaterialized) map[string]bool {
	floating := map[string]bool{}
	fws := state.GetFloatingWindows()
	if len(fws) == 0 {
		return floating
	}
	children := crdt.LiveChildrenByParent(state)
	nodes := state.GetNodes()
	for _, fw := range fws {
		if fw == nil || !crdt.HLCIsZero(fw.GetTombstoneAt()) {
			continue
		}
		rootID := fw.GetRootNodeId()
		if rootID == "" {
			continue
		}
		floating[rootID] = true
		root := nodes[rootID]
		if root == nil || !crdt.HLCIsZero(root.GetTombstoneAt()) {
			continue
		}
		stack := append([]string(nil), children[rootID]...)
		for len(stack) > 0 {
			nid := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if floating[nid] {
				continue
			}
			floating[nid] = true
			stack = append(stack, children[nid]...)
		}
	}
	return floating
}

// assembleTreeTabs joins Hub tab placements with Worker-side details, links
// opened and unopened subagent chains across arbitrary depth, and returns both
// the flat row list and the per-tile hierarchical trees.
func assembleTreeTabs(tabs []*leapmuxv1.WorkspaceTab, wdata workerTabData, selfTab string) assembledTreeTabs {
	tabByID := make(map[string]*treeTab, len(tabs))
	tabWorkspace := make(map[string]string, len(tabs))
	openedOrder := make([]string, 0, len(tabs))

	for _, t := range tabs {
		id := t.GetTab().GetTabId()
		d := wdata.details[id]
		row := treeTab{
			ID:              id,
			Type:            resolve.TabTypeWireName(t.GetTab().GetTabType()),
			Title:           d.Title,
			State:           d.State,
			Worker:          wdata.workerNames[t.GetWorkerId()],
			WorkerID:        t.GetWorkerId(),
			WorkspaceID:     t.GetWorkspaceId(),
			TileID:          t.GetTileId(),
			Here:            selfTab != "" && id == selfTab,
			RunningCommands: wdata.termProcs[id],
			PendingRequests: wdata.pendingReqs[id],
		}
		if d.Agent != nil {
			row.Provider = agentlabels.CLIAlias(d.Agent.GetAgentProvider())
			if g := d.Agent.GetGitStatus(); g != nil {
				row.Branch = g.GetBranch()
				row.Dirty = gitRepoIsDirty(g)
			}
		} else if d.Terminal != nil {
			if g := d.Terminal.GetGitStatus(); g != nil {
				row.Branch = g.GetBranch()
				row.Dirty = gitRepoIsDirty(g)
			}
		}
		if row.Worker == "" {
			row.Worker = cli.ShortID(row.WorkerID)
		}
		rowCopy := row
		tabByID[id] = &rowCopy
		tabWorkspace[id] = t.GetWorkspaceId()
		openedOrder = append(openedOrder, id)
	}

	// Resolve workspace IDs transitively along ParentAgentId so 3+-level chains
	// with unopened intermediates (A -> B (unopened) -> C) inherit A's workspace
	// regardless of Go map iteration order.
	resolvedWS := make(map[string]string, len(wdata.details))
	resolving := map[string]bool{}
	var resolveWorkspace func(id string) string
	resolveWorkspace = func(id string) string {
		if ws, ok := tabWorkspace[id]; ok {
			return ws
		}
		if ws, ok := resolvedWS[id]; ok {
			return ws
		}
		if resolving[id] {
			return ""
		}
		det, ok := wdata.details[id]
		if !ok || det.Agent == nil {
			return ""
		}
		parentID := det.Agent.GetParentAgentId()
		if parentID == "" || parentID == id {
			return ""
		}
		resolving[id] = true
		ws := resolveWorkspace(parentID)
		delete(resolving, id)
		resolvedWS[id] = ws
		return ws
	}

	// Include unopened descendant subagents returned by IncludeDescendants=true
	// and mark opened subagents before materializing flatRows.
	childIDsByParent := map[string][]string{}
	nestedChild := map[string]bool{}
	var unopenedIDs []string
	for aid, det := range wdata.details {
		if det.Agent == nil {
			continue
		}
		parentID := det.Agent.GetParentAgentId()
		if parentID == "" || parentID == aid {
			continue
		}
		if _, hasParent := wdata.details[parentID]; !hasParent {
			continue
		}
		parentWS := resolveWorkspace(parentID)
		if _, exists := tabByID[aid]; !exists {
			wkID := wdata.agentWorker[aid]
			wkName := wdata.workerNames[wkID]
			if wkName == "" {
				wkName = cli.ShortID(wkID)
			}
			row := &treeTab{
				ID:              aid,
				Type:            "subagent",
				Title:           det.Title,
				Provider:        agentlabels.CLIAlias(det.Agent.GetAgentProvider()),
				State:           det.State,
				Worker:          wkName,
				WorkerID:        wkID,
				WorkspaceID:     parentWS,
				Here:            selfTab != "" && aid == selfTab,
				PendingRequests: wdata.pendingReqs[aid],
			}
			if g := det.Agent.GetGitStatus(); g != nil {
				row.Branch = g.GetBranch()
				row.Dirty = gitRepoIsDirty(g)
			}
			tabByID[aid] = row
			unopenedIDs = append(unopenedIDs, aid)
		} else {
			if parentWS != tabWorkspace[aid] {
				continue
			}
			tabByID[aid].Type = "subagent"
		}
		childIDsByParent[parentID] = append(childIDsByParent[parentID], aid)
		nestedChild[aid] = true
	}
	for pid := range childIDsByParent {
		sort.Strings(childIDsByParent[pid])
	}
	sort.Strings(unopenedIDs)

	flatRows := make([]treeTab, 0, len(openedOrder)+len(unopenedIDs))
	for _, id := range openedOrder {
		flatRows = append(flatRows, *tabByID[id])
	}
	for _, id := range unopenedIDs {
		flatRows = append(flatRows, *tabByID[id])
	}

	// Recursively materialize nested subagents so 3+-level chains (A -> B -> C) stay intact.
	var buildTreeTab func(id string, visited map[string]bool) treeTab
	buildTreeTab = func(id string, visited map[string]bool) treeTab {
		node := *tabByID[id]
		if visited[id] {
			return node
		}
		visited[id] = true
		for _, cid := range childIDsByParent[id] {
			if tabByID[cid] == nil {
				continue
			}
			node.Children = append(node.Children, buildTreeTab(cid, visited))
		}
		return node
	}

	tabsByTile := map[treeTileKey][]treeTab{}
	tileOrderByWS := map[string][]string{}
	tileSeenByWS := map[string]map[string]bool{}
	for _, t := range tabs {
		id := t.GetTab().GetTabId()
		if nestedChild[id] {
			continue
		}
		wsID := t.GetWorkspaceId()
		tileID := t.GetTileId()
		if tileID == "" {
			tileID = "default"
		}
		row := buildTreeTab(id, map[string]bool{})
		key := treeTileKey{wsID: wsID, tileID: tileID}
		tabsByTile[key] = append(tabsByTile[key], row)
		if tileSeenByWS[wsID] == nil {
			tileSeenByWS[wsID] = map[string]bool{}
		}
		if !tileSeenByWS[wsID][tileID] {
			tileSeenByWS[wsID][tileID] = true
			tileOrderByWS[wsID] = append(tileOrderByWS[wsID], tileID)
		}
	}

	return assembledTreeTabs{
		flatRows:      flatRows,
		tabsByTile:    tabsByTile,
		tileOrderByWS: tileOrderByWS,
	}
}

// groupTreeSections places workspaces and their tiles into sidebar sections,
// preserving the Hub's sidebar section order from sections.
func groupTreeSections(
	workspaces []*leapmuxv1.Workspace,
	sectionOrder []*leapmuxv1.Section,
	secIdx *sectionIndex,
	assembled assembledTreeTabs,
	floatingTiles map[string]bool,
	workerFiltered bool,
	archivedHidden int,
) treeResult {
	sectionMap := map[string]*treeSection{}
	for _, ws := range workspaces {
		secKey := ""
		secName := "Workspaces"
		secArchived := false
		if sec := secIdx.sectionOf(ws.GetId()); sec != nil {
			secKey = sec.GetId()
			if sec.GetName() != "" {
				secName = sec.GetName()
			}
			secArchived = sec.GetSectionType() == leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED
		}
		var tiles []treeTile
		for _, tileID := range assembled.tileOrderByWS[ws.GetId()] {
			tiles = append(tiles, treeTile{
				ID:       tileID,
				Floating: floatingTiles[tileID],
				Tabs:     assembled.tabsByTile[treeTileKey{wsID: ws.GetId(), tileID: tileID}],
			})
		}
		if workerFiltered && len(tiles) == 0 {
			continue
		}
		ts, ok := sectionMap[secKey]
		if !ok {
			ts = &treeSection{Name: secName, Archived: secArchived}
			sectionMap[secKey] = ts
		}
		ts.Workspaces = append(ts.Workspaces, treeWorkspace{
			ID:       ws.GetId(),
			Title:    ws.GetTitle(),
			Section:  secName,
			Archived: secArchived,
			Tiles:    tiles,
		})
	}
	sections := make([]treeSection, 0, len(sectionMap))
	emitted := map[string]bool{}
	for _, sec := range sectionOrder {
		if ts, ok := sectionMap[sec.GetId()]; ok {
			sections = append(sections, *ts)
			emitted[sec.GetId()] = true
		}
	}
	if ts, ok := sectionMap[""]; ok && !emitted[""] {
		sections = append(sections, *ts)
	}
	return treeResult{Sections: sections, ArchivedWorkspaces: archivedHidden}
}

func gitRepoIsDirty(g *leapmuxv1.GitRepoStatus) bool {
	return g.GetModified() || g.GetAdded() || g.GetDeleted() || g.GetRenamed() || g.GetTypeChanged() || g.GetUntracked() || g.GetConflicted()
}
