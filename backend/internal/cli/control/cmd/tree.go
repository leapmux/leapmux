package cmd

import (
	"context"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/sync/errgroup"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
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
			wsLast := wIdx == len(sec.Workspaces)-1
			wsConn := "├─ "
			wsPrefix := "│  "
			if wsLast {
				wsConn = "└─ "
				wsPrefix = "   "
			}
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
				tileLast := tIdx == len(ws.Tiles)-1
				tileConn := "├─ "
				tilePrefix := wsPrefix + "│  "
				if tileLast {
					tileConn = "└─ "
					tilePrefix = wsPrefix + "   "
				}
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
		if _, err := fmt.Fprintf(w, "Archived (%d %s; pass --archived to show)\n", r.ArchivedWorkspaces, pluralizeWord(r.ArchivedWorkspaces, "workspace", "workspaces")); err != nil {
			return err
		}
	}
	return nil
}

func pluralizeWord(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}

func renderTreeTabs(w io.Writer, tabs []treeTab, prefix string) error {
	for i, tab := range tabs {
		last := i == len(tabs)-1
		conn := "├─ "
		childPrefix := prefix + "│  "
		if last {
			conn = "└─ "
			childPrefix = prefix + "   "
		}
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

func runFleetTree(inv *cli.Invocation) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()

	var wsFilterID string
	if inv.Changed("workspace") {
		ws, err := s.resolveOne(resolve.KindWorkspace, stringFlag(inv, "workspace"))
		if err != nil {
			return nil, err
		}
		wsFilterID = ws.ID
	}
	var workerFilterID string
	if inv.Changed("worker") {
		w, err := s.resolveOne(resolve.KindWorker, stringFlag(inv, "worker"))
		if err != nil {
			return nil, err
		}
		workerFilterID = w.ID
	}

	var wsResp leapmuxv1.ListWorkspacesResponse
	if err := hubCallUnary(s.ctx, s.c, leapmuxv1connect.WorkspaceServiceListWorkspacesProcedure, &leapmuxv1.ListWorkspacesRequest{}, &wsResp); err != nil {
		return nil, err
	}

	// Best-effort sidebar sections for grouping, ordering, and archive detection.
	var secResp leapmuxv1.ListSectionsResponse
	_ = hubCallUnary(s.ctx, s.c, leapmuxv1connect.SectionServiceListSectionsProcedure, &leapmuxv1.ListSectionsRequest{}, &secResp)

	sectionByID := map[string]*leapmuxv1.Section{}
	for _, sec := range secResp.GetSections() {
		sectionByID[sec.GetId()] = sec
	}
	wsSection := map[string]*leapmuxv1.Section{}
	for _, item := range secResp.GetItems() {
		if sec, ok := sectionByID[item.GetSectionId()]; ok {
			wsSection[item.GetWorkspaceId()] = sec
		}
	}

	includeArchived := boolFlag(inv, "archived")
	var workspaces []*leapmuxv1.Workspace
	archivedHidden := 0
	for _, ws := range wsResp.GetWorkspaces() {
		if wsFilterID != "" && ws.GetId() != wsFilterID {
			continue
		}
		sec := wsSection[ws.GetId()]
		isArchived := sec != nil && sec.GetSectionType() == leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED
		if isArchived && !includeArchived && wsFilterID == "" {
			archivedHidden++
			continue
		}
		workspaces = append(workspaces, ws)
	}

	wsIDs := make([]string, 0, len(workspaces))
	for _, ws := range workspaces {
		wsIDs = append(wsIDs, ws.GetId())
	}
	var tabs []*leapmuxv1.WorkspaceTab
	if len(wsIDs) > 0 {
		tabs, err = listTabsInWorkspaces(s.ctx, s.c, wsIDs)
		if err != nil {
			return nil, err
		}
	}
	if workerFilterID != "" {
		filtered := make([]*leapmuxv1.WorkspaceTab, 0, len(tabs))
		for _, t := range tabs {
			if t.GetWorkerId() == workerFilterID {
				filtered = append(filtered, t)
			}
		}
		tabs = filtered
	}

	// Best-effort CRDT bootstrap to identify floating-window tiles accurately.
	floatingTiles := map[string]bool{}
	if len(wsIDs) > 0 {
		if bs, berr := crdtBootstrap(s.ctx, s.c, wsIDs); berr == nil && bs != nil && bs.State != nil {
			for _, fw := range bs.State.GetFloatingWindows() {
				if fw == nil || !crdt.HLCIsZero(fw.GetTombstoneAt()) {
					continue
				}
				rootID := fw.GetRootNodeId()
				if rootID == "" {
					continue
				}
				floatingTiles[rootID] = true
				for _, nid := range crdt.DescendantsLeavesFirst(bs.State, rootID) {
					floatingTiles[nid] = true
				}
			}
		}
	}

	// Fetch all Worker-side data (agents + descendants, terminals, processes,
	// viewer titles, worker names, and pending control requests) over ONE
	// Noise_NK channel per Worker.
	wdata := fetchTreeWorkerData(s.ctx, s.c, tabs)
	selfTab := callingTabID()

	tabByID := make(map[string]*treeTab, len(tabs))
	tabWorkspace := make(map[string]string, len(tabs))
	flatRows := make([]treeTab, 0, len(tabs))

	for _, t := range tabs {
		id := t.GetTab().GetTabId()
		d := wdata.details[id]
		row := treeTab{
			ID:              id,
			Type:            tabTypeName(t.GetTab().GetTabType()),
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
		flatRows = append(flatRows, row)
	}

	// Include unopened descendant subagents returned by IncludeDescendants=true.
	childIDsByParent := map[string][]string{}
	nestedChild := map[string]bool{}
	var unopenedFlat []treeTab
	for aid, det := range wdata.details {
		if det.Agent == nil {
			continue
		}
		parentID := det.Agent.GetParentAgentId()
		if parentID == "" {
			continue
		}
		if _, hasParent := wdata.details[parentID]; !hasParent {
			continue
		}
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
				WorkspaceID:     tabWorkspace[parentID],
				Here:            selfTab != "" && aid == selfTab,
				PendingRequests: wdata.pendingReqs[aid],
			}
			tabByID[aid] = row
			unopenedFlat = append(unopenedFlat, *row)
		} else {
			if tabWorkspace[parentID] != tabWorkspace[aid] {
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

	if boolFlag(inv, "flat") {
		sort.Slice(unopenedFlat, func(i, j int) bool { return unopenedFlat[i].ID < unopenedFlat[j].ID })
		flatRows = append(flatRows, unopenedFlat...)
		return struct {
			Tabs []treeTab `json:"tabs"`
		}{Tabs: flatRows}, nil
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

	// Group top-level tabs by workspace -> tile.
	type tileKey struct{ wsID, tileID string }
	tabsByTile := map[tileKey][]treeTab{}
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
		key := tileKey{wsID: wsID, tileID: tileID}
		tabsByTile[key] = append(tabsByTile[key], row)
		if tileSeenByWS[wsID] == nil {
			tileSeenByWS[wsID] = map[string]bool{}
		}
		if !tileSeenByWS[wsID][tileID] {
			tileSeenByWS[wsID][tileID] = true
			tileOrderByWS[wsID] = append(tileOrderByWS[wsID], tileID)
		}
	}

	// Group workspaces into sections keyed by section ID, preserving the Hub's
	// sidebar section order from secResp.GetSections().
	sectionMap := map[string]*treeSection{}
	for _, ws := range workspaces {
		secKey := ""
		secName := "Workspaces"
		secArchived := false
		if sec := wsSection[ws.GetId()]; sec != nil {
			secKey = sec.GetId()
			if sec.GetName() != "" {
				secName = sec.GetName()
			}
			secArchived = sec.GetSectionType() == leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED
		}
		var tiles []treeTile
		for _, tileID := range tileOrderByWS[ws.GetId()] {
			tiles = append(tiles, treeTile{
				ID:       tileID,
				Floating: floatingTiles[tileID],
				Tabs:     tabsByTile[tileKey{wsID: ws.GetId(), tileID: tileID}],
			})
		}
		if workerFilterID != "" && len(tiles) == 0 {
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
	for _, sec := range secResp.GetSections() {
		if ts, ok := sectionMap[sec.GetId()]; ok {
			sections = append(sections, *ts)
			emitted[sec.GetId()] = true
		}
	}
	if ts, ok := sectionMap[""]; ok && !emitted[""] {
		sections = append(sections, *ts)
	}
	return treeResult{Sections: sections, ArchivedWorkspaces: archivedHidden}, nil
}

func gitRepoIsDirty(g *leapmuxv1.GitRepoStatus) bool {
	return g.GetModified() || g.GetAdded() || g.GetDeleted() || g.GetRenamed() || g.GetTypeChanged() || g.GetUntracked() || g.GetConflicted()
}

type treeWorkerData struct {
	details     map[string]tabDetail
	workerNames map[string]string
	agentWorker map[string]string
	termProcs   map[string][]string
	pendingReqs map[string][]string
}

// fetchTreeWorkerData opens ONE Noise_NK E2EE channel per Worker and gathers
// agent info (with descendants), terminal info, running processes, viewer
// titles, worker names, and pending control request handles concurrently.
func fetchTreeWorkerData(ctx context.Context, c control.Peer, tabs []*leapmuxv1.WorkspaceTab) treeWorkerData {
	type workerTabs struct {
		agents, terminals []string
		viewers           []*leapmuxv1.TabIdent
	}
	byWorker := map[string]*workerTabs{}
	for _, t := range tabs {
		wk := t.GetWorkerId()
		if wk == "" {
			continue
		}
		wt := byWorker[wk]
		if wt == nil {
			wt = &workerTabs{}
			byWorker[wk] = wt
		}
		switch t.GetTab().GetTabType() {
		case leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED:
			// Ignore unspecified tab types.
		case leapmuxv1.TabType_TAB_TYPE_AGENT:
			wt.agents = append(wt.agents, t.GetTab().GetTabId())
		case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
			wt.terminals = append(wt.terminals, t.GetTab().GetTabId())
		case leapmuxv1.TabType_TAB_TYPE_FILE, leapmuxv1.TabType_TAB_TYPE_IMAGE:
			wt.viewers = append(wt.viewers, t.GetTab())
		}
	}
	res := treeWorkerData{
		details:     map[string]tabDetail{},
		workerNames: map[string]string{},
		agentWorker: map[string]string{},
		termProcs:   map[string][]string{},
		pendingReqs: map[string][]string{},
	}
	var mu sync.Mutex
	var g errgroup.Group
	g.SetLimit(detailFanoutLimit)
	for workerID, wt := range byWorker {
		workerID, wt := workerID, wt
		g.Go(func() error {
			_ = withWorkerChannel(ctx, c, workerID, func(w workerCall) error {
				var inner errgroup.Group
				inner.SetLimit(detailFanoutLimit)
				var waitingAgents []string
				if len(wt.agents) > 0 {
					inner.Go(func() error {
						var resp leapmuxv1.ListAgentsResponse
						if w.Call(ctx, leapmuxv1connect.WorkerAgentServiceListAgentsProcedure, &leapmuxv1.ListAgentsRequest{
							TabIds:             wt.agents,
							IncludeDescendants: true,
						}, &resp) == nil {
							mu.Lock()
							for _, a := range resp.GetAgents() {
								res.details[a.GetId()] = tabDetail{Title: a.GetTitle(), State: enumToken(a.GetActivityState()), Agent: a}
								res.agentWorker[a.GetId()] = workerID
								if a.GetActivityState() == leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER {
									waitingAgents = append(waitingAgents, a.GetId())
								}
							}
							mu.Unlock()
						}
						return nil
					})
				}
				if len(wt.terminals) > 0 {
					inner.Go(func() error {
						var resp leapmuxv1.ListTerminalsResponse
						if w.Call(ctx, leapmuxv1connect.WorkerTerminalServiceListTerminalsProcedure, &leapmuxv1.ListTerminalsRequest{TabIds: wt.terminals}, &resp) == nil {
							mu.Lock()
							for _, t := range resp.GetTerminals() {
								res.details[t.GetTerminalId()] = tabDetail{Title: t.GetTitle(), State: enumToken(t.GetStatus()), Terminal: t}
							}
							mu.Unlock()
						}
						return nil
					})
					inner.Go(func() error {
						var resp leapmuxv1.InspectTerminalProcessesResponse
						if w.Call(ctx, leapmuxv1connect.WorkerTerminalServiceInspectTerminalProcessesProcedure, &leapmuxv1.InspectTerminalProcessesRequest{TerminalIds: wt.terminals}, &resp) == nil {
							mu.Lock()
							for _, tp := range resp.GetTerminals() {
								var names []string
								for _, proc := range tp.GetProcesses() {
									n := strings.TrimSpace(proc.GetName())
									if n == "" {
										n = fmt.Sprintf("pid:%d", proc.GetPid())
									}
									if !slices.Contains(names, n) {
										names = append(names, n)
									}
								}
								if len(names) > 0 {
									sort.Strings(names)
									res.termProcs[tp.GetTerminalId()] = names
								}
							}
							mu.Unlock()
						}
						return nil
					})
				}
				for _, viewer := range wt.viewers {
					viewer := viewer
					inner.Go(func() error {
						var resp leapmuxv1.GetTabPayloadResponse
						id := viewer.GetTabId()
						if w.Call(ctx, leapmuxv1connect.WorkerPrivateServiceGetTabPayloadProcedure, &leapmuxv1.GetTabPayloadRequest{TabId: id}, &resp) != nil {
							return nil
						}
						if title, known := viewerTabTitle(viewer.GetTabType(), resp.GetPayload()); known {
							mu.Lock()
							res.details[id] = tabDetail{Title: title}
							mu.Unlock()
						}
						return nil
					})
				}
				inner.Go(func() error {
					var resp leapmuxv1.GetWorkerSystemInfoResponse
					if w.Call(ctx, leapmuxv1connect.WorkerSystemServiceGetWorkerSystemInfoProcedure, &leapmuxv1.GetWorkerSystemInfoRequest{}, &resp) == nil {
						mu.Lock()
						res.workerNames[workerID] = resp.GetName()
						mu.Unlock()
					}
					return nil
				})
				_ = inner.Wait()

				// Reuse the same channel for any agent waiting on a control request.
				for _, aid := range waitingAgents {
					var reqResp leapmuxv1.ListControlRequestsResponse
					if w.Call(ctx, leapmuxv1connect.WorkerAgentServiceListControlRequestsProcedure, &leapmuxv1.ListControlRequestsRequest{AgentId: aid}, &reqResp) == nil {
						var handles []string
						for _, cr := range reqResp.GetRequests() {
							if h := cr.GetClaimToken(); h != "" {
								handles = append(handles, cli.ShortID(h))
							}
						}
						if len(handles) > 0 {
							mu.Lock()
							res.pendingReqs[aid] = handles
							mu.Unlock()
						}
					}
				}
				return nil
			})
			return nil
		})
	}
	_ = g.Wait()
	return res
}
