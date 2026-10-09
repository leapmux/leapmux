package cline

import (
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// Agent teams.
//
// With teams enabled, the lead starts teammates and assigns background work through team_run_task.
// team.progress carries the team summary and the lifecycle event for each change.
// Each run event supplies the run ID, teammate, and task.
//
// Each teammate run owns one workflow row grouped under its team from queueing through completion.
// Cline then stores its conversation as <root>__teamtask__<agent>__....
// The worker reconstructs its transcript from that session. See subagent.go.
//
// Teammate live output also supplies no agent identity.
// During a lead turn, it arrives among lead output and remains in the lead transcript, as in Cline's own CLI.
// Outside a lead turn, an active teammate run identifies the untagged output as teammate work.
// Discard that live output until the stored run transcript becomes available after completion.
// The run's lifecycle notices still reach the lead transcript.

// teamTaskSessionMarker is the part of a stored session id that marks a
// teammate task's session.
const teamTaskSessionMarker = "__teamtask__"

// teamRowPrefix identifies teammate-run registry rows, and teamGroupPrefix identifies their team groups.
const (
	teamRowPrefix   = "cline-team-run:"
	teamGroupPrefix = "cline-team:"
)

// teamState tracks this session's teammate runs.
// Agent.Mu protects its state.
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

// teamRunStatus maps a recognized run-lifecycle event to its registry status.
// It also reports whether the event belongs to that lifecycle.
func teamRunStatus(eventType string) (bgtask.Status, bool) {
	switch eventType {
	case contracts.ClineTeamRunEventRunQueued:
		return bgtask.StatusPending, true
	case contracts.ClineTeamRunEventRunStarted:
		return bgtask.StatusRunning, true
	case contracts.ClineTeamRunEventRunCompleted:
		return bgtask.StatusSucceeded, true
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

// handleTeamProgress maintains teammate-run registry rows and persists each run event as a lead-transcript notice.
// Other team events, including teammate messages and output, change no registry row.
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
		if _, err := a.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: event.Raw}); err != nil {
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
	// No worker-visible tool call starts this run.
	// Its row key therefore supplies its independent transcript span.
	childID, err := a.sink.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: run.rowKey(), ProviderChildKey: run.rowKey(), Title: title})
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

// closeTeamRuns closes each active run row with status when the owning process ends or its session changes.
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
