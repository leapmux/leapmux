package cmd

import (
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
)

// The `tab` group holds the commands that work on a tab of any type. Each
// tab type owns its creation command: `agent open`, `terminal open` and
// `file open`.
//
// `tab list` and `tab get` read the rendered rows of workspace_tab_owned,
// served by the WorkspaceService ListTabs and LocateTab RPCs, and join each
// row with what its Worker reports: the title and the state.
func newTabCommand() *cobra.Command {
	return cli.Group("tab", "List, close, rename and move tabs of every type",
		cli.NewLeaf(cli.Leaf{
			Use:   "list",
			Short: "List your tabs with their titles and states",
			Examples: []cli.Example{
				{Command: "leapmux control tab list"},
				{Comment: "List the agent tabs of the workspace that this tab lives in.", Command: "leapmux control tab list --workspace self --type agent"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.String("workspace", "", `list only the tabs of this workspace (an id, a prefix, "title:<title>" or "self")`)
				fs.String("type", "", "list only the tabs of this type")
				cli.SetEnum(fs, "type", tabTypeTokens()...)
			},
			Run: runTabList,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:   "get [tab]",
			Short: "Show one tab (default: this tab)",
			Examples: []cli.Example{
				{Command: "leapmux control tab get"},
				{Command: "leapmux control tab get 87a19d5f"},
			},
			Complete: completeTargets,
			Run:      runTabGet,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "close <tab>",
			Short:   "Close a tab and stop what runs in it",
			Mutates: true,
			Examples: []cli.Example{
				{Command: "leapmux control tab close 87a19d5f"},
				{Comment: "Close the last tab of a worktree and keep the worktree.", Command: "leapmux control tab close 87a19d5f --worktree keep"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.String("worktree", "", "what happens to the git worktree when this is its last tab; required in that case")
				cli.SetEnum(fs, "worktree", "keep", "push", "discard")
				fs.Bool("allow-busy", false, allowBusyFlagHelp)
				fs.Bool(allowSelfFlag, false, "close the tab even when it is this tab, which ends this command's own process")
			},
			Complete: completeTargets,
			Run:      runTabClose,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "rename <tab> <title>",
			Short:   "Rename an agent or terminal tab",
			Mutates: true,
			Examples: []cli.Example{
				{Command: `leapmux control tab rename self "Release notes"`},
			},
			Complete: completeTargets,
			Run:      runTabRename,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "move <tab>",
			Short:   "Move a tab to another tile or workspace, or reorder it",
			Mutates: true,
			Examples: []cli.Example{
				{Comment: "Move a tab to the first tile of another workspace.", Command: `leapmux control tab move 87a19d5f --workspace "title:Release 1.4"`},
				{Comment: "Put a tab before another tab of the same tile.", Command: "leapmux control tab move 87a19d5f --before 4cbb3f34"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.String("tile", "", "destination tile")
				fs.String("workspace", "", "destination workspace; without --tile, the tab lands on its first tile")
				addPositionFlags(fs, "put the tab")
			},
			Complete: completeTargets,
			Run:      runTabMove,
		}),
	)
}

// tabTypeTokens lists the tab types that a flag accepts.
func tabTypeTokens() []string {
	return enumTokens(leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED)
}

// tabRow is one tab in a result: the hub's placement joined with what the
// tab's Worker reports.
type tabRow struct {
	ID    string `json:"id" table:"ID,short"`
	Type  string `json:"type" table:"TYPE"`
	Title string `json:"title,omitempty" table:"TITLE"`
	// State is the agent's activity state ("idle", "working",
	// "waiting_for_user") or the terminal's status ("ready", "exited").
	State string `json:"state,omitempty" table:"STATE"`
	// Worker is the Worker's name, when the Worker reported one.
	Worker      string `json:"worker,omitempty" table:"WORKER"`
	WorkerID    string `json:"worker_id"`
	WorkspaceID string `json:"workspace_id" table:"WORKSPACE,short"`
	TileID      string `json:"tile_id"`
	Position    string `json:"position"`
}

func tabRowOf(t *leapmuxv1.WorkspaceTab, d tabDetail, workerName string) tabRow {
	return tabRow{
		ID:          t.GetTab().GetTabId(),
		Type:        tabTypeName(t.GetTab().GetTabType()),
		Title:       d.Title,
		State:       d.State,
		Worker:      workerName,
		WorkerID:    t.GetWorkerId(),
		WorkspaceID: t.GetWorkspaceId(),
		TileID:      t.GetTileId(),
		Position:    t.GetPosition(),
	}
}

// tabRows joins rows with what their Workers report. A Worker that cannot
// answer leaves the title and the state of its tabs empty.
func (s *session) tabRows(tabs []*leapmuxv1.WorkspaceTab) []tabRow {
	details := fetchTabDetails(s.ctx, s.c, tabs)
	seen := map[string]bool{}
	var workerIDs []string
	for _, t := range tabs {
		if id := t.GetWorkerId(); id != "" && !seen[id] {
			seen[id] = true
			workerIDs = append(workerIDs, id)
		}
	}
	names := fetchWorkerNames(s.ctx, s.c, workerIDs)
	rows := make([]tabRow, 0, len(tabs))
	for _, t := range tabs {
		rows = append(rows, tabRowOf(t, details[t.GetTab().GetTabId()], names[t.GetWorkerId()]))
	}
	return rows
}

// tabList is the result of `tab list`.
type tabList struct {
	Tabs []tabRow `json:"tabs"`
}

// runTabList enumerates the caller's tabs. Without --workspace it lists
// every workspace: a list never narrows itself to the caller's own
// workspace, so the same command prints the same tabs inside and outside a
// tab.
func runTabList(inv *cli.Invocation) (any, error) {
	wanted := leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED
	if raw := stringFlag(inv, "type"); raw != "" {
		t, ok := resolve.ParseTabType(raw)
		if !ok || t == leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED {
			return nil, cli.Usagef("--type must be one of %s; got %q", strings.Join(tabTypeTokens(), ", "), raw)
		}
		wanted = t
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	req := &leapmuxv1.ListTabsRequest{}
	if inv.Changed("workspace") {
		ws, err := s.resolveOne(resolve.KindWorkspace, stringFlag(inv, "workspace"))
		if err != nil {
			return nil, err
		}
		req.WorkspaceIds = []string{ws.ID}
	}
	var resp leapmuxv1.ListTabsResponse
	if err := hubCallUnary(s.ctx, s.c, leapmuxv1connect.WorkspaceServiceListTabsProcedure, req, &resp); err != nil {
		return nil, err
	}
	return tabList{Tabs: s.tabRows(filterTabsByType(resp.GetTabs(), wanted))}, nil
}

// filterTabsByType drops tabs whose type doesn't match wanted. A zero
// wanted (TAB_TYPE_UNSPECIFIED) returns the input slice unchanged.
// The non-unspecified path allocates a fresh slice rather than reusing
// the input's backing array — `in` aliases the response's `tabs` slice
// and overwriting it in place while iterating would corrupt any future
// reader of the response.
func filterTabsByType(in []*leapmuxv1.WorkspaceTab, wanted leapmuxv1.TabType) []*leapmuxv1.WorkspaceTab {
	if wanted == leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED {
		return in
	}
	out := make([]*leapmuxv1.WorkspaceTab, 0, len(in))
	for _, t := range in {
		if t.GetTab().GetTabType() == wanted {
			out = append(out, t)
		}
	}
	return out
}

// runTabGet prints one tab: the row that LocateTab answered, joined with
// what its Worker reports. A tab that the hub cannot place, such as a quake
// terminal, has no row and answers not_found.
func runTabGet(inv *cli.Invocation) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	got, err := s.resolveTab(resolve.KindTab, inv.Arg("tab"), resolve.Need{Want: resolve.Wants{WorkspaceID: true, TabType: true}})
	if err != nil {
		return nil, err
	}
	if got.Row == nil {
		return nil, cli.Newf(cli.CodeNotFound, "the hub has no tab %s", got.ID)
	}
	return s.tabRows([]*leapmuxv1.WorkspaceTab{got.Row})[0], nil
}

// tabRenameResult is the result of `tab rename`.
type tabRenameResult struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	// Title is the title that the Worker stored, which is not always the
	// title the command sent: the Worker cleans it.
	Title string `json:"title"`
}

// runTabRename retitles an agent or terminal tab. LocateTab reports the
// tab's type, and the command calls the Worker's rename for that type.
func runTabRename(inv *cli.Invocation) (any, error) {
	title := inv.Arg("title")
	if strings.TrimSpace(title) == "" {
		return nil, cli.Usagef("the title is empty")
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	got, err := s.resolveTab(resolve.KindTab, inv.Arg("tab"), resolve.Need{WorkerID: true, Want: resolve.Wants{TabType: true}})
	if err != nil {
		return nil, err
	}
	switch got.Type {
	case leapmuxv1.TabType_TAB_TYPE_AGENT:
		// Report the worker's title, not the one this command sent. The
		// worker cleans it with the rule that validate.CleanName
		// documents. A title that cleans to nothing leaves the tab with
		// the name it already had. Echoing the argument back therefore
		// printed a name that the tab does not carry.
		var resp leapmuxv1.RenameAgentResponse
		if err := callInnerRPC(s.ctx, s.c, got.WorkerID, leapmuxv1connect.WorkerAgentServiceRenameAgentProcedure, &leapmuxv1.RenameAgentRequest{AgentId: got.ID, Title: title}, &resp); err != nil {
			return nil, err
		}
		return tabRenameResult{ID: got.ID, Type: tabTypeName(got.Type), Title: resp.GetTitle()}, nil
	case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
		var resp leapmuxv1.RenameTerminalResponse
		if err := callInnerRPC(s.ctx, s.c, got.WorkerID, leapmuxv1connect.WorkerTerminalServiceRenameTerminalProcedure, &leapmuxv1.RenameTerminalRequest{TerminalId: got.ID, Title: title}, &resp); err != nil {
			return nil, err
		}
		return tabRenameResult{ID: got.ID, Type: tabTypeName(got.Type), Title: resp.GetTitle()}, nil
	default:
		return nil, cli.Usagef("a %s tab has no title to rename; only agent and terminal tabs do", tabTypeName(got.Type))
	}
}
