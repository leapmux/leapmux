package cmd

import (
	"context"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/sync/errgroup"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
)

func newTerminalPSLeaf() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:   "ps [terminal...]",
		Short: "List foreground and background processes running inside one or more terminals",
		Examples: []cli.Example{
			{Command: "leapmux terminal ps"},
			{Command: "leapmux terminal ps 4cbb3f34"},
			{Comment: "Inspect every terminal in the workspace.", Command: "leapmux terminal ps --all"},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.Bool("all", false, "inspect every open terminal instead of a single target")
			fs.String("workspace", "", `inspect terminals in this workspace (implies --all when no targets are given)`)
		},
		Complete: completeTargets,
		Run:      runTerminalPS,
	})
}

type terminalProcessRow struct {
	TerminalID string `json:"terminal_id" table:"TERMINAL,short"`
	PID        int32  `json:"pid" table:"PID"`
	Name       string `json:"name" table:"PROCESS"`
}

type terminalPSResult struct {
	Processes []terminalProcessRow `json:"processes"`
	Truncated map[string]int32     `json:"truncated_extra,omitempty"`
}

func (r terminalPSResult) QuietLines() []string {
	out := make([]string, 0, len(r.Processes))
	for _, p := range r.Processes {
		out = append(out, fmt.Sprintf("%s\t%d\t%s", p.TerminalID, p.PID, p.Name))
	}
	return out
}

// resolveVariadicTerminalTargets resolves terminal selectors in order while
// memoizing workspace scope, ListTabs, and TabTitles across inputs and
// deduplicating by tab ID.
func (s *session) resolveVariadicTerminalTargets(inputs []string) ([]tabTarget, error) {
	var workspaceIDs []string
	if s.hasTargetWorkspaceScope() {
		ws, err := s.resolveOne(resolve.KindWorkspace, stringFlag(s.inv, "workspace"))
		if err != nil {
			return nil, err
		}
		workspaceIDs = []string{ws.ID}
	}

	listing := s.listing()
	var (
		tabsLoaded   bool
		cachedTabs   []*leapmuxv1.WorkspaceTab
		titlesLoaded bool
		cachedTitles map[string]string
	)
	listing.Tabs = func(ctx context.Context) ([]*leapmuxv1.WorkspaceTab, error) {
		if tabsLoaded {
			return cachedTabs, nil
		}
		tabs, err := listTabsInWorkspaces(ctx, s.c, workspaceIDs)
		if err != nil {
			return nil, err
		}
		cachedTabs = tabs
		tabsLoaded = true
		return tabs, nil
	}
	rawTitles := listing.TabTitles
	listing.TabTitles = func(ctx context.Context, tabs []*leapmuxv1.WorkspaceTab) map[string]string {
		if titlesLoaded {
			return cachedTitles
		}
		cachedTitles = rawTitles(ctx, tabs)
		titlesLoaded = true
		return cachedTitles
	}

	deps := s.deps()
	envWorkerID := strings.TrimSpace(os.Getenv(controlenv.WorkerIDVariable))
	seen := make(map[string]struct{}, len(inputs))
	targets := make([]tabTarget, 0, len(inputs))
	for _, input := range inputs {
		if input == "" {
			input = resolve.SelfTarget
		}
		t, err := resolve.Canonical(s.ctx, listing, resolve.KindTerminal, input)
		if err != nil {
			return nil, err
		}
		if t.Tab != nil {
			s.located[t.ID] = t.Tab
		}
		got, err := resolve.Resolve(s.ctx, deps, resolve.Need{TabID: true, WorkerID: true}, resolve.Inputs{
			TabID:         t.ID,
			ExplicitTabID: !t.Self,
			WorkerID:      envWorkerID,
			FixedTabType:  leapmuxv1.TabType_TAB_TYPE_TERMINAL,
		})
		if err != nil {
			return nil, err
		}
		if _, dup := seen[got.TabID]; dup {
			continue
		}
		seen[got.TabID] = struct{}{}
		targets = append(targets, tabTarget{
			ID:       got.TabID,
			Type:     got.TabType,
			WorkerID: got.WorkerID,
			Self:     t.Self,
			Row:      got.Tab,
		})
	}
	return targets, nil
}

func runTerminalPS(inv *cli.Invocation) (any, error) {
	args := inv.Variadic("terminal")
	allFlag := boolFlag(inv, "all")
	if allFlag && len(args) > 0 {
		return nil, cli.Usagef("pass either --all or terminal targets, not both")
	}

	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()

	all := allFlag || (len(args) == 0 && inv.Changed("workspace"))
	byWorker := map[string][]string{}
	seenByWorker := map[string]map[string]struct{}{}
	addTarget := func(workerID, terminalID string) {
		if workerID == "" || terminalID == "" {
			return
		}
		seen := seenByWorker[workerID]
		if seen == nil {
			seen = map[string]struct{}{}
			seenByWorker[workerID] = seen
		}
		if _, dup := seen[terminalID]; dup {
			return
		}
		seen[terminalID] = struct{}{}
		byWorker[workerID] = append(byWorker[workerID], terminalID)
	}

	if all {
		tabs, err := s.listWorkspaceTabs(leapmuxv1.TabType_TAB_TYPE_TERMINAL)
		if err != nil {
			return nil, err
		}
		for _, t := range tabs {
			addTarget(t.GetWorkerId(), t.GetTab().GetTabId())
		}
	} else {
		if len(args) == 0 {
			args = []string{""}
		}
		targets, err := s.resolveVariadicTerminalTargets(args)
		if err != nil {
			return nil, err
		}
		for _, got := range targets {
			addTarget(got.WorkerID, got.ID)
		}
	}

	workerIDs := slices.Sorted(maps.Keys(byWorker))
	results := make([]*leapmuxv1.InspectTerminalProcessesResponse, len(workerIDs))
	g, gctx := errgroup.WithContext(s.ctx)
	g.SetLimit(detailFanoutLimit)
	for i, wk := range workerIDs {
		ids := byWorker[wk]
		g.Go(func() error {
			if err := maybePreflightWorker(gctx, s.c, wk); err != nil {
				return err
			}
			var resp leapmuxv1.InspectTerminalProcessesResponse
			if err := callInnerRPC(gctx, s.c, wk, leapmuxv1connect.WorkerTerminalServiceInspectTerminalProcessesProcedure, &leapmuxv1.InspectTerminalProcessesRequest{
				TerminalIds: ids,
			}, &resp); err != nil {
				return err
			}
			results[i] = &resp
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

	rows := []terminalProcessRow{}
	truncated := map[string]int32{}
	for _, resp := range results {
		for _, tp := range resp.GetTerminals() {
			for _, proc := range tp.GetProcesses() {
				name := proc.GetName()
				if name == "" {
					name = "(unknown)"
				}
				rows = append(rows, terminalProcessRow{
					TerminalID: tp.GetTerminalId(),
					PID:        proc.GetPid(),
					Name:       name,
				})
			}
			if extra := tp.GetTotalCount() - int32(len(tp.GetProcesses())); extra > 0 {
				truncated[tp.GetTerminalId()] = extra
			}
		}
	}
	res := terminalPSResult{Processes: rows}
	if len(truncated) > 0 {
		res.Truncated = truncated
	}
	return res, nil
}
