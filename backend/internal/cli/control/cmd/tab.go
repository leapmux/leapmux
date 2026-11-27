package cmd

import (
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/util/agentlabels"
)

// The `tab` group holds the commands that work on a tab of any type. Each
// tab type owns its creation command: `agent open`, `terminal open` and
// `file open`.
//
// `tab list` and `tab get` read the rendered rows of workspace_tab_owned,
// served by the WorkspaceService ListTabs and LocateTab RPCs, and join each
// row with what its Worker reports: the title and the state.
//
// `tab reveal` and `tab status` reach the open frontends through the tab's
// Worker, on its private-event stream.
func newTabCommand() *cobra.Command {
	return cli.Group("tab", "List, close, rename, move and reveal tabs of every type, and set their status",
		cli.NewLeaf(cli.Leaf{
			Use:     "list",
			Aliases: []string{"ls"},
			Short:   "List your tabs with their titles and states",
			Examples: []cli.Example{
				{Command: "leapmux tab list"},
				{Comment: "List the agent tabs of the workspace that this tab lives in.", Command: "leapmux tab list --workspace self --type agent"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.String("workspace", "", `list only the tabs of this workspace (an id, a prefix, "title:<title>" or "self")`)
				fs.String("worker", "", `list only the tabs hosted by this worker (an id, a prefix, "name:<name>" or "self")`)
				fs.String("type", "", "list only the tabs of this type")
				cli.SetEnum(fs, "type", tabTypeTokens()...)
				fs.String("state", "", "list only the tabs in this state (such as idle, working, waiting_for_user, ready, exited)")
			},
			Run: runTabList,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:   "get [tab]",
			Short: "Show one tab (default: this tab)",
			Examples: []cli.Example{
				{Command: "leapmux tab get"},
				{Command: "leapmux tab get 87a19d5f"},
			},
			Flags:    addTargetWorkspaceFlag,
			Complete: completeTargets,
			Run:      runTabGet,
		}),
		newTabCloseLeafFor(resolve.KindTab),
		newTabRenameLeafFor(resolve.KindTab),
		cli.NewLeaf(cli.Leaf{
			Use:     "move <tab>",
			Aliases: []string{"mv"},
			Short:   "Move a tab to another tile or workspace, or reorder it",
			Mutates: true,
			Examples: []cli.Example{
				{Comment: "Move a tab to the first tile of another workspace.", Command: `leapmux tab move 87a19d5f --workspace "title:Release 1.4"`},
				{Comment: "Put a tab before another tab of the same tile.", Command: "leapmux tab move 87a19d5f --before 4cbb3f34"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.String("tile", "", "destination tile")
				fs.String("workspace", "", "destination workspace; without --tile, the tab lands on its first tile")
				addPositionFlags(fs, "put the tab")
			},
			FlagComplete: tabPositionFlagCompletions(),
			Complete:     completeTargets,
			Run:          runTabMove,
		}),
		newTabRevealLeafFor(resolve.KindTab),
		newTabOpenImageLeaf(),
		newTabStatusCommand(),
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
	// Provider is present for an agent whose Worker reported its details.
	Provider string `json:"provider,omitempty" table:"PROVIDER"`
	// State is the agent's activity state ("idle", "working",
	// "waiting_for_user") or the terminal's status ("ready", "exited").
	State string `json:"state,omitempty" table:"STATE,state"`
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
		Type:        resolve.TabTypeWireName(t.GetTab().GetTabType()),
		Title:       d.Title,
		Provider:    agentlabels.CLIAlias(d.Agent.GetAgentProvider()),
		State:       d.State,
		Worker:      workerName,
		WorkerID:    t.GetWorkerId(),
		WorkspaceID: t.GetWorkspaceId(),
		TileID:      t.GetTileId(),
		Position:    t.GetPosition(),
	}
}

// tabRows joins rows with what their Workers report on a single Noise_NK
// channel per Worker. A Worker that cannot answer leaves the title and the
// state of its tabs empty.
func (s *session) tabRows(tabs []*leapmuxv1.WorkspaceTab) []tabRow {
	details, names := fetchTabDetailsAndWorkers(s.ctx, s.c, tabs, true)
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
	tabs, err := s.listWorkspaceTabs(wanted)
	if err != nil {
		return nil, err
	}
	if inv.Changed("worker") {
		w, err := s.resolveOne(resolve.KindWorker, stringFlag(inv, "worker"))
		if err != nil {
			return nil, err
		}
		tabs = filterTabsByWorker(tabs, w.ID)
	}
	rows := s.tabRows(tabs)
	if state := strings.TrimSpace(stringFlag(inv, "state")); state != "" {
		filtered := make([]tabRow, 0, len(rows))
		for _, row := range rows {
			if strings.EqualFold(row.State, state) {
				filtered = append(filtered, row)
			}
		}
		rows = filtered
	}
	return tabList{Tabs: rows}, nil
}

// filterTabsByWorker drops tabs whose worker_id does not match workerID,
// allocating a fresh slice so callers never mutate the Hub response slice.
func filterTabsByWorker(in []*leapmuxv1.WorkspaceTab, workerID string) []*leapmuxv1.WorkspaceTab {
	out := make([]*leapmuxv1.WorkspaceTab, 0, len(in))
	for _, t := range in {
		if t.GetWorkerId() == workerID {
			out = append(out, t)
		}
	}
	return out
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

// newTabRenameLeafFor builds `tab rename`, `agent rename`, or `terminal rename`.
func newTabRenameLeafFor(kind resolve.Kind) *cobra.Command {
	noun := kind.String()
	short := "Rename an agent or terminal tab"
	use := "rename <" + noun + "> <title>"
	examples := []cli.Example{
		{Command: `leapmux ` + noun + ` rename self "Release notes"`},
	}
	switch kind {
	case resolve.KindAgent:
		short = "Rename an agent tab or set its unique --name slug"
		use = "rename <agent> [title]"
		examples = append(examples, cli.Example{
			Comment: "Assign a unique short slug so commands can target the agent by name.",
			Command: "leapmux agent rename 87a19d5f --name reviewer",
		})
	case resolve.KindTerminal:
		short = "Rename a terminal tab"
	case resolve.KindTab, resolve.KindTile, resolve.KindWorkspace, resolve.KindWorker:
	}
	return cli.NewLeaf(cli.Leaf{
		Use:      use,
		Short:    short,
		Mutates:  true,
		Examples: examples,
		Flags: func(fs *pflag.FlagSet) {
			if kind == resolve.KindAgent {
				fs.String("name", "", "set a unique short agent slug (lowercase letters, digits, hyphens, underscores)")
			}
			addTargetWorkspaceFlag(fs)
		},
		Complete: completeTargets,
		Run: func(inv *cli.Invocation) (any, error) {
			return runTabRenameFor(inv, kind)
		},
	})
}

// runTabRenameFor retitles an agent or terminal tab. LocateTab reports the
// tab's type, and the command calls the Worker's rename for that type.
func runTabRenameFor(inv *cli.Invocation, kind resolve.Kind) (any, error) {
	hasTitle := inv.HasArg("title")
	hasSlug := kind == resolve.KindAgent && inv.Changed("name")
	title := inv.Arg("title")
	slug := ""
	switch {
	case hasSlug && hasTitle:
		return nil, cli.Usagef("give a title or --name, not both")
	case hasSlug:
		var err error
		slug, err = parseAgentSlugFlag(inv)
		if err != nil {
			return nil, err
		}
		title = slug
	case strings.TrimSpace(title) == "":
		if kind == resolve.KindAgent {
			return nil, cli.Usagef("missing <title> or --name")
		}
		return nil, cli.Usagef("the title is empty")
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	got, err := s.resolveTab(kind, inv.Arg(kind.String()), resolve.Need{WorkerID: true, Want: resolve.Wants{TabType: true}})
	if err != nil {
		return nil, err
	}
	if slug != "" {
		if err := s.ensureUniqueAgentSlug(slug, got.ID); err != nil {
			return nil, err
		}
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
		return tabRenameResult{ID: got.ID, Type: resolve.TabTypeWireName(got.Type), Title: resp.GetTitle()}, nil
	case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
		var resp leapmuxv1.RenameTerminalResponse
		if err := callInnerRPC(s.ctx, s.c, got.WorkerID, leapmuxv1connect.WorkerTerminalServiceRenameTerminalProcedure, &leapmuxv1.RenameTerminalRequest{TerminalId: got.ID, Title: title}, &resp); err != nil {
			return nil, err
		}
		return tabRenameResult{ID: got.ID, Type: resolve.TabTypeWireName(got.Type), Title: resp.GetTitle()}, nil
	default:
		return nil, cli.Usagef("a %s tab has no title to rename; only agent and terminal tabs do", resolve.TabTypeWireName(got.Type))
	}
}
