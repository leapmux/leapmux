package cmd

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/leapmux/leapmux/internal/cli"
)

// streamEvent is one event of `leapmux events`: one line of text, or
// one {"data": …} envelope with --json.
//
// Every event has a kind, a time and a summary. The other fields belong to
// the kinds that eventKinds lists; each one says which kinds set it. A field
// that a kind does not set is absent from its JSON.
type streamEvent struct {
	Kind string `json:"kind"`
	// Time is the Worker observation time for a journal record. For live
	// notifications without a journal, it is when the stream received the event.
	Time time.Time `json:"time"`
	// WorkspaceID, TabID and TabType identify the tab of a tab, agent or
	// terminal event, and the workspace of a workspace event.
	WorkspaceID string `json:"workspace_id,omitempty"`
	TabID       string `json:"tab_id,omitempty"`
	TabType     string `json:"tab_type,omitempty"`
	// WorkerID is the Worker of a tab, or the Worker that a stream event is
	// about.
	WorkerID string `json:"worker_id,omitempty"`
	// Summary states the event in one line of prose.
	Summary string `json:"summary"`
	// Reconciled marks an event that the stream printed after a gap, from
	// the state that it read anew, rather than from the event itself. The
	// change happened during the gap, at an unknown time.
	Reconciled bool `json:"reconciled,omitempty"`

	// State is the activity of agent.activity ("idle", "working" or
	// "waiting_for_user"), and the delivery state of the answer of
	// agent.request_ended, as `agent requests` prints it.
	State string `json:"state,omitempty"`
	// ToolCalls is the count of tool calls of the turn that an
	// agent.activity settles, when the provider reports one.
	ToolCalls *int32 `json:"tool_calls,omitempty"`
	// Status is the status of agent.status ("starting", "active", "inactive"
	// or "startup_failed") and of terminal.status ("starting", "ready",
	// "startup_failed" or "exited").
	Status string `json:"status,omitempty"`
	// Error is why an agent or a terminal failed to start.
	Error string `json:"error,omitempty"`
	// Request is the handle of the request of agent.request and
	// agent.request_ended: what `agent approve`, `agent deny` and `agent
	// answer` take. RequestID is the provider's own id of the request.
	Request   string `json:"request,omitempty"`
	RequestID string `json:"request_id,omitempty"`
	// RequestInstance correlates the historical request and its end.
	RequestInstance string `json:"request_instance,omitempty"`
	// Prompt is what an agent.request asks: "permission", "plan",
	// "question", "elicitation", "dialog" or "unreadable".
	Prompt string `json:"prompt,omitempty"`
	// Blocking reports whether the agent of an agent.request waits for the
	// answer.
	Blocking *bool `json:"blocking,omitempty"`
	// Todos counts the items of agent.todos.
	Todos *todoCounts `json:"todos,omitempty"`
	// Tasks counts the background tasks of agent.background_tasks.
	Tasks *taskCounts `json:"tasks,omitempty"`
	// Title is the title of terminal.title, terminal.notification, tab.renamed,
	// notification.created, workspace.created and workspace.renamed. Body is
	// the text of terminal.notification and notification.created.
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
	// NotificationID identifies the notification of notification.created and
	// notification.cleared.
	NotificationID string `json:"notification_id,omitempty"`
	// QueueRevision is the queue revision of agent.queue.
	QueueRevision *uint64 `json:"queue_revision,omitempty"`
	// GoalActive and GoalPaused describe the standing goal of agent.goal.
	GoalActive *bool `json:"goal_active,omitempty"`
	GoalPaused *bool `json:"goal_paused,omitempty"`
	// Progress is the report of terminal.progress.
	Progress *progressReport `json:"progress,omitempty"`
	// ExitCode is the exit code of the shell of terminal.exit.
	ExitCode *int32 `json:"exit_code,omitempty"`
	// FromWorkspaceID is the workspace that the tab of tab.moved left. A
	// tab.moved without WorkspaceID left the workspace that --workspace
	// selects for one that the stream does not watch.
	FromWorkspaceID string `json:"from_workspace_id,omitempty"`
	// Source is what a stream.unavailable or a stream.gap is about: "hub" or
	// "worker".
	Source string `json:"source,omitempty"`
	// Code and Message state why a source of stream.unavailable cannot
	// deliver, with the codes of the error envelope.
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	// Retrying reports whether the stream tries the source of a
	// stream.unavailable again. A source that refused the stream for good
	// is not tried again.
	Retrying *bool `json:"retrying,omitempty"`
	// Since is when the source of a stream.gap stopped delivering. Time is
	// when it delivered again.
	Since *time.Time `json:"since,omitempty"`
	// Lost lists the kinds of a stream.gap that the stream cannot print
	// again. The stream printed the change of every other kind of the source
	// as a reconciled event.
	Lost []string `json:"lost,omitempty"`
	// TabIDs lists the tabs of the Worker of a stream.gap.
	TabIDs []string `json:"tab_ids,omitempty"`
	// Scope counts what stream.ready and stream.heartbeat watch.
	Scope *scopeCounts `json:"scope,omitempty"`
	// JournalID, TabStreamID and Sequence identify an event in retained history.
	JournalID   string `json:"journal_id,omitempty"`
	TabStreamID string `json:"tab_stream_id,omitempty"`
	Sequence    *int64 `json:"sequence,omitempty"`
	// Replayed reports whether the source had committed the record when the stream opened.
	Replayed *bool `json:"replayed,omitempty"`
	// GapReason and AfterSequence describe the interval that a journal gap advances over.
	GapReason     string `json:"gap_reason,omitempty"`
	AfterSequence *int64 `json:"after_sequence,omitempty"`
}

// todoCounts counts the items of a to-do list.
type todoCounts struct {
	Total      int `json:"total"`
	Completed  int `json:"completed"`
	InProgress int `json:"in_progress"`
}

// taskCounts counts the background tasks of an agent. Active counts the
// tasks that did not end yet.
type taskCounts struct {
	Total  int `json:"total"`
	Active int `json:"active"`
}

// progressReport is a progress report of a program in a terminal.
type progressReport struct {
	// State is "normal", "error", "indeterminate" or "paused", and "" for a
	// report that clears the progress.
	State   string `json:"state,omitempty"`
	Percent int32  `json:"percent"`
}

// scopeCounts counts what the stream watches.
type scopeCounts struct {
	Workspaces int `json:"workspaces"`
	Tabs       int `json:"tabs"`
	// Workers counts the Workers that host a tab of the scope, and
	// WorkersLive the ones that deliver events now.
	Workers     int  `json:"workers"`
	WorkersLive int  `json:"workers_live"`
	HubLive     bool `json:"hub_live"`
}

// eventTimeLayout is the time of a text line: RFC 3339 with milliseconds
// and the offset of the local time zone.
const eventTimeLayout = "2006-01-02T15:04:05.000Z07:00"

// TextLine is the event as one line: the local time, the kind, the ids, and
// the summary.
//
//	2026-11-19T19:00:01.123+09:00 agent.activity workspace=9f3kQ2aB tab=87a19d5f worker=1a2b3c4d idle (3 tool calls)
func (e streamEvent) TextLine() string {
	var b strings.Builder
	b.WriteString(e.Time.Local().Format(eventTimeLayout))
	b.WriteByte(' ')
	b.WriteString(e.Kind)
	for _, id := range []struct{ key, value string }{
		{"workspace", e.WorkspaceID},
		{"tab", e.TabID},
		{"worker", e.WorkerID},
		{"source", e.Source},
	} {
		if id.value == "" {
			continue
		}
		fmt.Fprintf(&b, " %s=%s", id.key, cli.ShortID(id.value))
	}
	if e.Sequence != nil {
		fmt.Fprintf(&b, " sequence=%d", *e.Sequence)
	}
	if e.Replayed != nil && *e.Replayed {
		b.WriteString(" replayed")
	}
	if e.Summary != "" {
		b.WriteByte(' ')
		for _, char := range oneLine(e.Summary) {
			if unicode.IsControl(char) {
				fmt.Fprintf(&b, "\\u%04x", char)
			} else {
				b.WriteRune(char)
			}
		}
	}
	if e.Reconciled {
		b.WriteString(" (after a gap)")
	}
	return b.String()
}

// tokenWords is an enum token as words: "waiting_for_user" is
// "waiting for user".
func tokenWords(token string) string {
	return strings.ReplaceAll(token, "_", " ")
}

// boolPtr returns a pointer to v, for an optional field that must state
// false too.
func boolPtr(v bool) *bool { return &v }
