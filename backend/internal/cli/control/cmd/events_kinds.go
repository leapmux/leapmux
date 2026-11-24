package cmd

import (
	"slices"
	"strings"

	"github.com/leapmux/leapmux/internal/cli"
)

// The vocabulary of `leapmux events`. Each kind is "<family>.<event>",
// and this table is its one declaration: the help, the completion of --kind,
// the check of --kind and the "lost" list of a gap all read it.
//
// What each source delivers, and the kind that each delivery becomes:
//
//   - The hub's user events (`/ws/userevents`, WatchUserEvent). The stream
//     keeps the CRDT state that the frames describe and projects its tabs with
//     crdt.Project, so a tab event states what changed in the layout, not
//     which frame carried it.
//       - initial (UserMaterialized): the scope. The first one prints nothing.
//         One after a reconnect prints what changed during the gap.
//       - batch, entity_materialized, entity_removed: the CRDT state. The
//         stream compares the projection at each batch_end, which closes every
//         batch, and prints tab.opened, tab.closed and tab.moved.
//       - created, renamed, deleted: workspace.created, workspace.renamed and
//         workspace.deleted.
//       - workers_changed: prints nothing. It wakes the reconnect of every
//         Worker that cannot be reached, because one of them may be online now.
//       - presence: prints nothing. It says which browser has the input focus,
//         which no script acts on; `layout watch` prints it.
//       - delta: never arrives, because the stream sends no resume cursor.
//   - Each Worker's WatchEvents in notify mode, for the agent and terminal
//     tabs in scope. A notify watch carries the notification class only (see
//     agentEventClass and terminalEventClass in the Worker's service).
//       - AgentActivityChanged: agent.activity.
//       - AgentStatusChange: agent.status, when the status changes. The Worker
//         also sends one for a change of the options or of the git status.
//       - AgentTurnEnd: agent.turn_end.
//       - AgentControlRequest: agent.request, for a request instance (a claim
//         token) that waits for an answer and that the stream did not print
//         yet.
//       - AgentControlCancelRequest: agent.request_ended.
//       - AgentTodoSnapshot: agent.todos, when an item or its status changes.
//       - AgentBackgroundTaskSnapshot: agent.background_tasks, when a task or
//         its status changes.
//       - control_response_changed: prints nothing. It is the progress of an
//         answer's delivery, and agent.request_ended reports its outcome.
//       - TerminalStatusChange: terminal.status, when the status changes.
//       - TerminalClosed: terminal.exit.
//       - TerminalBell, TerminalNotification, TerminalTitleChanged and
//         TerminalProgress: terminal.bell, terminal.notification,
//         terminal.title and terminal.progress.
//   - The stream itself: stream.ready, stream.unavailable, stream.gap and
//     stream.heartbeat.

// The kinds of the vocabulary.
const (
	kindWorkspaceCreated = "workspace.created"
	kindWorkspaceRenamed = "workspace.renamed"
	kindWorkspaceDeleted = "workspace.deleted"

	kindTabOpened  = "tab.opened"
	kindTabClosed  = "tab.closed"
	kindTabMoved   = "tab.moved"
	kindTabRenamed = "tab.renamed"
	kindTabStatus  = "tab.status"

	kindAgentActivity        = "agent.activity"
	kindAgentStatus          = "agent.status"
	kindAgentTurnEnd         = "agent.turn_end"
	kindAgentRequest         = "agent.request"
	kindAgentRequestEnded    = "agent.request_ended"
	kindAgentTodos           = "agent.todos"
	kindAgentBackgroundTasks = "agent.background_tasks"
	kindAgentGoal            = "agent.goal"
	kindAgentQueue           = "agent.queue"

	kindTerminalStatus       = "terminal.status"
	kindTerminalTitle        = "terminal.title"
	kindTerminalBell         = "terminal.bell"
	kindTerminalNotification = "terminal.notification"
	kindTerminalProgress     = "terminal.progress"
	kindTerminalExit         = "terminal.exit"

	kindNotificationCreated = "notification.created"
	kindNotificationCleared = "notification.cleared"

	kindStreamReady       = "stream.ready"
	kindStreamUnavailable = "stream.unavailable"
	kindStreamGap         = "stream.gap"
	kindStreamHeartbeat   = "stream.heartbeat"
	kindStreamRetired     = "stream.retired"
)

// eventSource is where the stream learns of an event.
type eventSource int

const (
	// sourceHub is the hub's user events.
	sourceHub eventSource = iota + 1
	// sourceWorker is the notify watch of one Worker.
	sourceWorker
	// sourceStream is the stream itself.
	sourceStream
)

// eventKind is one kind of the vocabulary.
type eventKind struct {
	name   string
	source eventSource
	// reconciled marks a kind that the stream prints again after a gap: it
	// reads the state of the source anew and prints each difference from the
	// state that it held. stream.gap lists every other kind of the source as
	// lost.
	reconciled bool
	// help is one sentence for the help of the command.
	help string
}

// eventKinds lists the vocabulary in the order of the help.
var eventKinds = []eventKind{
	{kindWorkspaceCreated, sourceHub, true, "a workspace was created"},
	{kindWorkspaceRenamed, sourceHub, false, "a workspace has a new title"},
	{kindWorkspaceDeleted, sourceHub, true, "a workspace was deleted"},
	{kindTabOpened, sourceHub, true, "a tab opened, or it moved into the workspace that --workspace selects"},
	{kindTabClosed, sourceHub, true, "a tab closed"},
	{kindTabMoved, sourceHub, true, "a tab moved to another workspace"},
	{kindTabRenamed, sourceWorker, false, "a tab was renamed on its Worker"},
	{kindTabStatus, sourceWorker, false, "a tab's status pill was set or cleared"},
	{kindAgentActivity, sourceWorker, true, "an agent became idle, working or waiting for the user"},
	{kindAgentStatus, sourceWorker, true, "the status of an agent's process changed to starting, active, inactive or startup_failed"},
	{kindAgentTurnEnd, sourceWorker, false, "an agent ended a turn"},
	{kindAgentRequest, sourceWorker, false, "an agent asks a question or a permission; agent approve, deny and answer take its request"},
	{kindAgentRequestEnded, sourceWorker, false, "a request of an agent was answered or withdrawn"},
	{kindAgentTodos, sourceWorker, false, "an agent's to-do list changed"},
	{kindAgentBackgroundTasks, sourceWorker, false, "an agent's background tasks changed"},
	{kindAgentGoal, sourceWorker, false, "an agent's standing goal was set, paused, resumed, or cleared"},
	{kindAgentQueue, sourceWorker, false, "an agent's queued input changed"},
	{kindTerminalStatus, sourceWorker, false, "the status of a terminal's shell changed to starting, ready or startup_failed, or to exited without a terminal.exit"},
	{kindTerminalTitle, sourceWorker, false, "a program set the title of a terminal"},
	{kindTerminalBell, sourceWorker, false, "a terminal rang its bell"},
	{kindTerminalNotification, sourceWorker, false, "a program in a terminal asked for a desktop notification"},
	{kindTerminalProgress, sourceWorker, false, "a program in a terminal reported its progress"},
	{kindTerminalExit, sourceWorker, false, "a terminal's shell exited, with its exit code"},
	{kindNotificationCreated, sourceWorker, false, "a live toast notification was published on a Worker"},
	{kindNotificationCleared, sourceWorker, false, "a notification was cleared on a Worker"},
	{kindStreamReady, sourceStream, false, "the stream watches every source of its scope"},
	{kindStreamUnavailable, sourceStream, false, "the hub, a Worker, or a Worker for one tab cannot deliver events; retrying says whether the stream tries again"},
	{kindStreamGap, sourceStream, false, "a source delivers again; events of the kinds in lost, between since and time, may be lost"},
	{kindStreamHeartbeat, sourceStream, false, "the stream runs; only --heartbeat prints it"},
	{kindStreamRetired, sourceStream, false, "the Worker finished the checkpointed tab's event history"},
}

// eventKindNames lists the name of every kind, in the order of the table.
func eventKindNames() []string {
	out := make([]string, len(eventKinds))
	for i, k := range eventKinds {
		out[i] = k.name
	}
	return out
}

// kindFamily returns the family of a kind: the part before its dot.
func kindFamily(kind string) string {
	family, _, _ := strings.Cut(kind, ".")
	return family
}

// eventFamilies lists every family of the vocabulary, in the order of the
// table.
func eventFamilies() []string {
	var out []string
	for _, k := range eventKinds {
		if family := kindFamily(k.name); !slices.Contains(out, family) {
			out = append(out, family)
		}
	}
	return out
}

// lostKinds lists the kinds of source that the stream cannot print again
// after a gap. withReconciled adds the kinds that it would print again, for a
// gap whose new state the stream could not read.
func lostKinds(source eventSource, withReconciled bool) []string {
	var out []string
	for _, k := range eventKinds {
		if k.source == source && (withReconciled || !k.reconciled) {
			out = append(out, k.name)
		}
	}
	return out
}

// kindFilter selects the kinds that the stream prints on stdout.
type kindFilter struct {
	// selected holds each selected kind. Nil selects every kind.
	selected map[string]bool
}

// parseKindFilter reads the values of --kind. Each value is a kind or a
// family, which selects every kind of the family. No value selects every
// kind.
func parseKindFilter(values []string) (kindFilter, error) {
	if len(values) == 0 {
		return kindFilter{}, nil
	}
	selected := map[string]bool{}
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		matched := false
		for _, k := range eventKinds {
			if k.name == value || kindFamily(k.name) == value {
				selected[k.name] = true
				matched = true
			}
		}
		if !matched {
			return kindFilter{}, cli.Usagef("unknown event kind %q", raw).
				WithHint("pass a family (%s) or a kind: %s", strings.Join(eventFamilies(), ", "), strings.Join(eventKindNames(), ", "))
		}
	}
	return kindFilter{selected: selected}, nil
}

// selects reports whether the filter prints kind on stdout.
func (f kindFilter) selects(kind string) bool {
	return f.selected == nil || f.selected[kind]
}

// kindFlagValues lists what --kind accepts: every family, then every kind.
func kindFlagValues() []string {
	return append(eventFamilies(), eventKindNames()...)
}
