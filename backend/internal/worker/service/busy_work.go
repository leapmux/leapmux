package service

import (
	"cmp"
	"context"
	"log/slog"
	"slices"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/leapmux/leapmux/util/validate"
)

// BusyWorkSnapshot summarizes active work running inside the local Worker that
// quitting or switching mode would stop.
type BusyWorkSnapshot struct {
	Agents    []BusyAgent
	Terminals []BusyTerminal
}

// BusyAgent describes one live local agent whose active turn, pending prompt,
// background task, or child agent would be stopped.
type BusyAgent struct {
	TabID                 string
	Title                 string
	ActiveBackgroundTasks []string
	ActiveChildAgents     []string
}

// BusyTerminal describes one live local terminal with running descendant
// processes.
type BusyTerminal struct {
	TabID      string
	Title      string
	Processes  []BusyTerminalProcess
	TotalCount int32
}

// BusyTerminalProcess describes one descendant process running inside a
// terminal.
type BusyTerminalProcess struct {
	PID  int32
	Name string
}

// InspectBusyWork inspects all live local agents and terminals for active work
// without running git status or acquiring database write locks.
func (svc *Service) InspectBusyWork(ctx context.Context) BusyWorkSnapshot {
	if svc == nil {
		return BusyWorkSnapshot{}
	}
	if ctx == nil {
		ctx = context.Background()
	}

	var snapshot BusyWorkSnapshot
	if svc.Agents != nil && svc.Output != nil {
		for _, id := range svc.Agents.ListAgentIDs() {
			activity, rows := svc.Output.agentActivityWithRows(id, id)
			if !activity.InterruptsWork() {
				continue
			}
			var activeTasks, activeChildren []string
			for _, row := range rows {
				if !row.Status.IsWorking() {
					continue
				}
				label := cleanBusyTaskLabel(row)
				switch {
				case row.Role.CountsAsTask():
					activeTasks = append(activeTasks, label)
				case row.Role.IsCanonicalChildActivity():
					activeChildren = append(activeChildren, label)
				}
			}
			snapshot.Agents = append(snapshot.Agents, BusyAgent{
				TabID:                 id,
				Title:                 svc.busyAgentTitle(ctx, id),
				ActiveBackgroundTasks: activeTasks,
				ActiveChildAgents:     activeChildren,
			})
		}
	}

	if svc.Terminals != nil {
		if ids := svc.Terminals.ListTerminalIDs(); len(ids) > 0 {
			scanCtx, cancel := context.WithTimeout(ctx, terminalProcessScanTimeout)
			running, err := svc.Terminals.DescendantProcesses(scanCtx, ids)
			cancel()
			if err != nil {
				slog.Warn("inspect busy terminal processes failed", "terminal_ids", len(ids), "error", err)
			} else {
				for _, t := range running {
					if len(t.Processes) == 0 {
						continue
					}
					procs := make([]BusyTerminalProcess, 0, len(t.Processes))
					for _, proc := range t.Processes {
						procs = append(procs, BusyTerminalProcess{
							PID:  proc.PID,
							Name: validate.WireString(proc.Name),
						})
					}
					snapshot.Terminals = append(snapshot.Terminals, BusyTerminal{
						TabID:      t.TerminalID,
						Title:      svc.busyTerminalTitle(ctx, t.TerminalID),
						Processes:  procs,
						TotalCount: int32(t.Total),
					})
				}
			}
		}
	}

	slices.SortFunc(snapshot.Agents, func(a, b BusyAgent) int {
		return compareBusyTab(a.Title, a.TabID, b.Title, b.TabID)
	})
	slices.SortFunc(snapshot.Terminals, func(a, b BusyTerminal) int {
		return compareBusyTab(a.Title, a.TabID, b.Title, b.TabID)
	})
	return snapshot
}

func (svc *Service) busyAgentTitle(ctx context.Context, agentID string) string {
	if svc.Queries != nil && ctx.Err() == nil {
		if raw, err := svc.Queries.GetAgentTitle(ctx, agentID); err == nil {
			if cleaned := validate.CleanName(raw); cleaned != "" {
				return cleaned
			}
		}
	}
	return "Agent"
}

func (svc *Service) busyTerminalTitle(ctx context.Context, terminalID string) string {
	if meta, ok := svc.Terminals.GetMeta(terminalID); ok {
		if cleaned := validate.CleanName(meta.Title); cleaned != "" {
			return cleaned
		}
	}
	if svc.Queries != nil && ctx.Err() == nil {
		if stored, err := svc.Queries.GetTerminalExitState(ctx, terminalID); err == nil {
			if cleaned := validate.CleanName(stored.Title); cleaned != "" {
				return cleaned
			}
			if stored.IsQuake {
				return "Quake Terminal"
			}
		}
	}
	return "Terminal"
}

func cleanBusyTaskLabel(row bgtask.Item) string {
	for _, candidate := range [...]string{row.Title, row.Description, row.RowKey} {
		if cleaned := validate.CleanName(candidate); cleaned != "" {
			return cleaned
		}
	}
	return "Untitled"
}

func compareBusyTab(titleA, idA, titleB, idB string) int {
	return cmp.Or(
		strings.Compare(strings.ToLower(titleA), strings.ToLower(titleB)),
		strings.Compare(titleA, titleB),
		strings.Compare(idA, idB),
	)
}
