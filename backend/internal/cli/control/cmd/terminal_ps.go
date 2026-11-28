package cmd

import (
	"fmt"
	"maps"
	"slices"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/sync/errgroup"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
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
			addTargetWorkspaceFlagWithUsage(fs, `inspect terminals in this workspace (implies --all when no targets are given)`)
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
		targets, err := s.resolveVariadicTabs(resolve.KindTerminal, args, resolve.Need{WorkerID: true})
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
