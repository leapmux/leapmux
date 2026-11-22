// Package busywork defines the snapshot types that describe active work inside
// a local Worker that quitting or switching mode would stop.
package busywork

import "context"

// Snapshot summarizes active work running inside the local Worker that quitting
// or switching mode would stop.
type Snapshot struct {
	Agents    []Agent
	Terminals []Terminal
}

// Agent describes one live local agent whose active turn, pending prompt,
// background task, or child agent would be stopped.
type Agent struct {
	TabID                 string
	Title                 string
	ActiveBackgroundTasks []string
	ActiveChildAgents     []string
}

// Terminal describes one live local terminal with running descendant processes.
type Terminal struct {
	TabID      string
	Title      string
	Processes  []TerminalProcess
	TotalCount int32
}

// TerminalProcess describes one descendant process running inside a terminal.
type TerminalProcess struct {
	PID  int32
	Name string
}

// Inspector inspects a live embedded Worker's active work without exposing
// internal worker packages.
type Inspector func(ctx context.Context) Snapshot
