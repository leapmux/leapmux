package service

import (
	"cmp"
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"

	"github.com/leapmux/leapmux/internal/worker/bgtask"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/util/validate"
	"github.com/leapmux/leapmux/worker/busywork"
)

const busyWorkTitleBatchSize = 500

// InspectBusyWork inspects all live local agents and terminals for active work
// without running git status or acquiring database write locks.
func (svc *Service) InspectBusyWork(ctx context.Context) busywork.Snapshot {
	if svc == nil {
		return busywork.Snapshot{}
	}
	if ctx == nil {
		ctx = context.Background()
	}

	var snapshot busywork.Snapshot
	var wg sync.WaitGroup

	if svc.Agents != nil && svc.Output != nil {
		wg.Go(func() {
			for _, id := range svc.Agents.ListAgentIDs() {
				activity, rows := svc.Output.agentActivityWithRowsCtx(ctx, id, id)
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
				snapshot.Agents = append(snapshot.Agents, busywork.Agent{
					TabID:                 id,
					ActiveBackgroundTasks: activeTasks,
					ActiveChildAgents:     activeChildren,
				})
			}
			if len(snapshot.Agents) > 0 {
				ids := make([]string, len(snapshot.Agents))
				for i, a := range snapshot.Agents {
					ids[i] = a.TabID
				}
				titles := svc.busyAgentTitles(ctx, ids)
				for i := range snapshot.Agents {
					if title := titles[snapshot.Agents[i].TabID]; title != "" {
						snapshot.Agents[i].Title = title
					} else {
						snapshot.Agents[i].Title = "Agent"
					}
				}
			}
		})
	}

	if svc.Terminals != nil {
		if ids := svc.Terminals.ListTerminalIDs(); len(ids) > 0 {
			wg.Go(func() {
				scanCtx, cancel := context.WithTimeout(ctx, terminalProcessScanTimeout)
				running, err := svc.Terminals.DescendantProcesses(scanCtx, ids)
				cancel()
				if err != nil {
					slog.Warn("inspect busy terminal processes failed", "terminal_ids", len(ids), "error", err)
					return
				}
				var unresolvedIDs []string
				for _, t := range running {
					if len(t.Processes) == 0 {
						continue
					}
					procs := make([]busywork.TerminalProcess, 0, len(t.Processes))
					for _, proc := range t.Processes {
						procs = append(procs, busywork.TerminalProcess{
							PID:  proc.PID,
							Name: cleanBusyProcessName(proc.Name),
						})
					}
					var title string
					if meta, ok := svc.Terminals.GetMeta(t.TerminalID); ok {
						title = validate.CleanName(meta.Title)
					}
					if title == "" {
						unresolvedIDs = append(unresolvedIDs, t.TerminalID)
					}
					snapshot.Terminals = append(snapshot.Terminals, busywork.Terminal{
						TabID:      t.TerminalID,
						Title:      title,
						Processes:  procs,
						TotalCount: int32(t.Total),
					})
				}
				if len(unresolvedIDs) > 0 {
					exitStates := svc.busyTerminalExitStates(ctx, unresolvedIDs)
					for i := range snapshot.Terminals {
						if snapshot.Terminals[i].Title != "" {
							continue
						}
						tabID := snapshot.Terminals[i].TabID
						if q, ok := svc.QuakeTerminals.Get(tabID); ok {
							if cleaned := validate.CleanName(q.Title); cleaned != "" {
								snapshot.Terminals[i].Title = cleaned
							} else {
								snapshot.Terminals[i].Title = "Quake Terminal"
							}
							continue
						}
						if state, ok := exitStates[tabID]; ok {
							if cleaned := validate.CleanName(state.Title); cleaned != "" {
								snapshot.Terminals[i].Title = cleaned
								continue
							}
						}
						snapshot.Terminals[i].Title = "Terminal"
					}
				}
			})
		}
	}

	wg.Wait()

	slices.SortFunc(snapshot.Agents, func(a, b busywork.Agent) int {
		return compareBusyTab(a.Title, a.TabID, b.Title, b.TabID)
	})
	slices.SortFunc(snapshot.Terminals, func(a, b busywork.Terminal) int {
		return compareBusyTab(a.Title, a.TabID, b.Title, b.TabID)
	})
	return snapshot
}

func (svc *Service) busyAgentTitles(ctx context.Context, agentIDs []string) map[string]string {
	titles := make(map[string]string, len(agentIDs))
	if svc.Queries == nil || ctx.Err() != nil {
		return titles
	}
	for start := 0; start < len(agentIDs); start += busyWorkTitleBatchSize {
		if ctx.Err() != nil {
			break
		}
		end := min(start+busyWorkTitleBatchSize, len(agentIDs))
		rows, err := svc.Queries.ListAgentTitlesByIDs(ctx, agentIDs[start:end])
		if err != nil {
			slog.Warn("inspect busy agent titles failed", "count", end-start, "error", err)
			continue
		}
		for _, row := range rows {
			if cleaned := validate.CleanName(row.Title); cleaned != "" {
				titles[row.ID] = cleaned
			}
		}
	}
	return titles
}

func (svc *Service) busyTerminalExitStates(ctx context.Context, terminalIDs []string) map[string]db.ListTerminalExitStatesByIDsRow {
	states := make(map[string]db.ListTerminalExitStatesByIDsRow, len(terminalIDs))
	if svc.Queries == nil || ctx.Err() != nil {
		return states
	}
	for start := 0; start < len(terminalIDs); start += busyWorkTitleBatchSize {
		if ctx.Err() != nil {
			break
		}
		end := min(start+busyWorkTitleBatchSize, len(terminalIDs))
		rows, err := svc.Queries.ListTerminalExitStatesByIDs(ctx, terminalIDs[start:end])
		if err != nil {
			slog.Warn("inspect busy terminal titles failed", "count", end-start, "error", err)
			continue
		}
		for _, row := range rows {
			states[row.ID] = row
		}
	}
	return states
}

func cleanBusyTaskLabel(row bgtask.Item) string {
	for _, candidate := range [...]string{row.Title, row.Description, row.RowKey} {
		if cleaned := validate.CleanName(candidate); cleaned != "" {
			return cleaned
		}
	}
	return "Untitled"
}

func cleanBusyProcessName(raw string) string {
	return validate.CleanName(raw)
}

func compareBusyTab(titleA, idA, titleB, idB string) int {
	return cmp.Or(
		strings.Compare(strings.ToLower(titleA), strings.ToLower(titleB)),
		strings.Compare(titleA, titleB),
		strings.Compare(idA, idB),
	)
}
