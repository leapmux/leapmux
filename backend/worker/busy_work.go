package worker

import (
	"context"
	"slices"
	"time"

	"github.com/leapmux/leapmux/internal/worker/service"
)

const busyWorkInspectDrainTimeout = 5 * time.Second

// BusyWorkSnapshot summarizes active work running inside the local Worker that
// quitting or switching mode would stop.
type BusyWorkSnapshot struct {
	Agents    []BusyAgent
	Terminals []BusyTerminal
}

// IsEmpty reports whether neither live agents nor live terminals have active
// work.
func (s BusyWorkSnapshot) IsEmpty() bool {
	return len(s.Agents) == 0 && len(s.Terminals) == 0
}

// BusyAgent describes one live local agent with active work.
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

// BusyWorkInspector inspects a live embedded Worker's active work without
// exposing internal worker packages.
type BusyWorkInspector func(ctx context.Context) BusyWorkSnapshot

func busyWorkFromService(src service.BusyWorkSnapshot) BusyWorkSnapshot {
	var out BusyWorkSnapshot
	if len(src.Agents) > 0 {
		out.Agents = make([]BusyAgent, len(src.Agents))
		for i, a := range src.Agents {
			out.Agents[i] = BusyAgent{
				TabID:                 a.TabID,
				Title:                 a.Title,
				ActiveBackgroundTasks: slices.Clone(a.ActiveBackgroundTasks),
				ActiveChildAgents:     slices.Clone(a.ActiveChildAgents),
			}
		}
	}
	if len(src.Terminals) > 0 {
		out.Terminals = make([]BusyTerminal, len(src.Terminals))
		for i, t := range src.Terminals {
			var procs []BusyTerminalProcess
			if len(t.Processes) > 0 {
				procs = make([]BusyTerminalProcess, len(t.Processes))
				for j, p := range t.Processes {
					procs[j] = BusyTerminalProcess{
						PID:  p.PID,
						Name: p.Name,
					}
				}
			}
			out.Terminals[i] = BusyTerminal{
				TabID:      t.TabID,
				Title:      t.Title,
				Processes:  procs,
				TotalCount: t.TotalCount,
			}
		}
	}
	return out
}
