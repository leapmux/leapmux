package cline

import (
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// Agent teams.
//
// With agent teams on, the lead can start teammates and hand each one a task
// to run in the background (`team_run_task`). Cline reports every change of
// the team with `team.progress`: a summary of the team, and the lifecycle event
// that changed it. The run events carry the run's id, its teammate and its
// task.
//
// Each teammate run becomes a workflow row, grouped under its team, from the
// moment that Cline queues it until it ends. When it ends, Cline stores the teammate's
// conversation as a session of its own (`<root>__teamtask__<agent>__...`),
// and the worker writes the run's transcript from it (subagent.go).
//
// Cline states no agent on a teammate's live output either. While the lead
// runs a turn, the teammates' output reaches the lead's stream among the
// lead's, as it does in Cline's own CLI, and the worker cannot tell it apart:
// it stays in the lead's transcript. While no lead turn runs, the output of a
// stream with an active run is a teammate's, and the worker drops it live; the
// run's own transcript holds it once the run ends. A run's lifecycle events
// reach the lead's transcript as notices.

// teamTaskSessionMarker is the part of a stored session id that marks a
// teammate task's session.
const teamTaskSessionMarker = "__teamtask__"

// teamRowPrefix and teamGroupPrefix key the registry rows of teammate runs and
// their group.
const (
	teamRowPrefix   = "cline-team-run:"
	teamGroupPrefix = "cline-team:"
)

// teamState follows the teammate runs of the session. Guarded by Agent.Mu.
type teamState struct {
	runs map[string]*teamRun
}

// busy reports whether a teammate run is active.
func (s teamState) busy() bool {
	for _, run := range s.runs {
		if !run.ended {
			return true
		}
	}
	return false
}

// teamRun is one teammate run.
type teamRun struct {
	id       string
	agentID  string
	teamName string
	childID  string
	// rootSession is the session the run belongs to.
	rootSession string
	// startedAtMs is when Cline queued the run, in milliseconds since the Unix
	// epoch.
	startedAtMs int64
	ended       bool
}

// rowKey is the registry row of the run.
func (r *teamRun) rowKey() string { return teamRowPrefix + r.id }

// teamProgress is the part of a `team.progress` event that the worker reads.
type teamProgress struct {
	SessionID string `json:"sessionId"`
	LastEvent struct {
		TeamName  string `json:"teamName"`
		EventType string `json:"eventType"`
		AgentID   string `json:"agentId"`
		TaskID    string `json:"taskId"`
		RunID     string `json:"runId"`
		Message   string `json:"message"`
	} `json:"lastEvent"`
}

// teamRunStatus maps a run event onto the row's status, and reports whether
// the event is one of a run's lifecycle.
func teamRunStatus(eventType string) (bgtask.Status, bool) {
	switch eventType {
	case contracts.ClineTeamRunEventRunQueued:
		return bgtask.StatusPending, true
	case contracts.ClineTeamRunEventRunStarted:
		return bgtask.StatusRunning, true
	case contracts.ClineTeamRunEventRunCompleted:
		return bgtask.StatusCompleted, true
	case contracts.ClineTeamRunEventRunFailed:
		return bgtask.StatusFailed, true
	case contracts.ClineTeamRunEventRunCancelled:
		return bgtask.StatusStopped, true
	case contracts.ClineTeamRunEventRunInterrupted:
		return bgtask.StatusInterrupted, true
	default:
		return bgtask.StatusUnspecified, false
	}
}

// handleTeamProgress keeps the rows of the teammate runs, and persists each run
// event as a notice of the lead's transcript. Every other team event -- a
// message between teammates, each teammate's output -- changes no row.
func (a *Agent) handleTeamProgress(event hubEvent) {
	var progress teamProgress
	if json.Unmarshal(event.Payload, &progress) != nil {
		return
	}
	last := progress.LastEvent
	status, lifecycle := teamRunStatus(last.EventType)
	if !lifecycle || last.RunID == "" {
		return
	}
	run := a.teamRunFor(event, last.RunID, last.AgentID, last.TeamName)
	title := strings.TrimSpace(last.AgentID)
	if title == "" {
		title = "Teammate"
	}
	if !status.IsFinished() {
		providerkit.LogRegistryRefusal("cline", "open teammate run", a.sink.UpsertBackgroundTask(bgtask.Upsert{
			RowKey:       run.rowKey(),
			Kind:         bgtask.KindWorkflow,
			ChildAgentID: run.childID,
			GroupKey:     teamGroupPrefix + run.rootSession + ":" + run.teamName,
			GroupLabel:   run.teamName,
			Title:        title,
			Description:  strings.TrimSpace(last.TaskID),
			Status:       status,
		}))
	} else {
		a.endTeamRun(run, status)
	}
	if !a.IsDiscardingOutput() {
		if _, err := a.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, event.Raw); err != nil {
			slog.Error("cline persist team progress", "agent_id", a.AgentID(), "error", err)
		}
	}
}

// teamRunFor returns the run of runID, and creates it with its child
// transcript at its first event.
func (a *Agent) teamRunFor(event hubEvent, runID, agentID, teamName string) *teamRun {
	a.Mu.Lock()
	if run := a.team.runs[runID]; run != nil {
		a.Mu.Unlock()
		return run
	}
	a.Mu.Unlock()
	run := &teamRun{
		id:          runID,
		agentID:     agentID,
		teamName:    strings.TrimSpace(teamName),
		rootSession: event.SessionID,
		startedAtMs: event.Timestamp,
	}
	if run.teamName == "" {
		run.teamName = "Team"
	}
	title := agentID
	if title == "" {
		title = "Teammate"
	}
	// A run starts from no tool call the worker can name, so its transcript
	// hangs off a span of its own: the run's row key.
	childID, err := a.sink.EnsureChildAgent(run.rowKey(), run.rowKey(), title)
	if err != nil {
		slog.Warn("cline teammate ensure child failed", "agent_id", a.AgentID(), "run_id", runID, "error", err)
	}
	run.childID = childID
	a.Mu.Lock()
	if a.team.runs == nil {
		a.team.runs = make(map[string]*teamRun)
	}
	a.team.runs[runID] = run
	a.Mu.Unlock()
	return run
}

// endTeamRun closes a run's row once the worker wrote its transcript from
// Cline's store.
func (a *Agent) endTeamRun(run *teamRun, status bgtask.Status) {
	a.Mu.Lock()
	if run.ended {
		a.Mu.Unlock()
		return
	}
	run.ended = true
	a.Mu.Unlock()
	finalize := func() {
		providerkit.LogRegistryRefusal("cline", "close teammate run", a.sink.CloseBackgroundTask(run.rowKey(), status))
		if run.childID != "" {
			a.sink.CleanupChildAgent(run.childID)
		}
	}
	if run.childID == "" || !status.IsFinished() {
		finalize()
		return
	}
	agentID := run.agentID
	a.startBackfill(backfillJob{
		target: newChildTranscript(a.sink, run.childID),
		match: func(stored storedSession) bool {
			return stored.Metadata.ParentSessionID == run.rootSession &&
				strings.Contains(stored.SessionID, teamTaskSessionMarker) &&
				stored.Metadata.AgentID == agentID
		},
		notBefore: run.startedAtMs,
		prompt:    true,
		parent:    a.sink,
		finalize:  finalize,
		label:     "teammate run " + run.id,
	})
}

// closeTeamRuns closes the row of every run that is still active, with status,
// because the process that ran it ended or the session changed.
func (a *Agent) closeTeamRuns(status bgtask.Status) {
	a.Mu.Lock()
	var open []*teamRun
	for _, run := range a.team.runs {
		if !run.ended {
			run.ended = true
			open = append(open, run)
		}
	}
	a.Mu.Unlock()
	for _, run := range open {
		providerkit.LogRegistryRefusal("cline", "close teammate run", a.sink.CloseBackgroundTask(run.rowKey(), status))
		if run.childID != "" {
			a.sink.CleanupChildAgent(run.childID)
		}
	}
}
