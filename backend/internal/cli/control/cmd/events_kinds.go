package cmd

import (
	"slices"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
)

// The vocabulary of `leapmux events`. Each kind is "<family>.<event>",
// and this table is its one declaration: the help, the completion of --kind,
// the check of --kind, checkpoint replay capability, and the "lost" list of a
// gap all read it.
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
//       - GoalChanged: agent.goal (live-only).
//       - InputQueueChanged and InputQueueRevision: agent.queue (live-only).
//       - control_response_changed: prints nothing. It is the progress of an
//         answer's delivery, and agent.request_ended reports its outcome.
//       - TerminalStatusChange: terminal.status, when the status changes.
//       - TerminalClosed: terminal.exit.
//       - TerminalBell, TerminalNotification, TerminalTitleChanged and
//         TerminalProgress: terminal.bell, terminal.notification,
//         terminal.title and terminal.progress.
//   - Each Worker's WatchWorkerPrivateEvents stream (live-only):
//       - TabRenamed: tab.renamed.
//       - TabStatusChanged: tab.status.
//       - UserNotification: notification.created.
//       - UserNotificationCleared: notification.cleared.
//   - The stream itself: stream.ready, stream.unavailable, stream.gap,
//     stream.heartbeat and stream.retired.

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

// replayJournal identifies which retained journal stores an event kind for
// checkpointed replay. Live-only and stream-meta kinds have replayNone.
type replayJournal int

const (
	replayNone replayJournal = iota
	replayHubLayout
	replayWorkerAgent
	replayWorkerTerminal
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
	// journal identifies the retained journal that stores this kind, or
	// replayNone if the kind is live-only or stream-meta.
	journal replayJournal
	// help is one sentence for the help of the command.
	help string
}

// eventKinds lists the vocabulary in the order of the help.
var eventKinds = []eventKind{
	{kindWorkspaceCreated, sourceHub, true, replayHubLayout, "a workspace was created"},
	{kindWorkspaceRenamed, sourceHub, false, replayHubLayout, "a workspace has a new title"},
	{kindWorkspaceDeleted, sourceHub, true, replayHubLayout, "a workspace was deleted"},
	{kindTabOpened, sourceHub, true, replayHubLayout, "a tab opened, or it moved into the workspace that --workspace selects"},
	{kindTabClosed, sourceHub, true, replayHubLayout, "a tab closed"},
	{kindTabMoved, sourceHub, true, replayHubLayout, "a tab moved to another workspace"},
	{kindTabRenamed, sourceWorker, false, replayNone, "a tab was renamed on its Worker"},
	{kindTabStatus, sourceWorker, false, replayNone, "a tab's status pill was set or cleared"},
	{kindAgentActivity, sourceWorker, true, replayWorkerAgent, "an agent became idle, working or waiting for the user"},
	{kindAgentStatus, sourceWorker, true, replayWorkerAgent, "the status of an agent's process changed to starting, active, inactive or startup_failed"},
	{kindAgentTurnEnd, sourceWorker, false, replayWorkerAgent, "an agent ended a turn"},
	{kindAgentRequest, sourceWorker, false, replayWorkerAgent, "an agent asks a question or a permission; agent approve, deny and answer take its request"},
	{kindAgentRequestEnded, sourceWorker, false, replayWorkerAgent, "a request of an agent was answered or withdrawn"},
	{kindAgentTodos, sourceWorker, false, replayWorkerAgent, "an agent's to-do list changed"},
	{kindAgentBackgroundTasks, sourceWorker, false, replayWorkerAgent, "an agent's background tasks changed"},
	{kindAgentGoal, sourceWorker, false, replayNone, "an agent's standing goal was set, paused, resumed, or cleared"},
	{kindAgentQueue, sourceWorker, false, replayNone, "an agent's queued input changed"},
	{kindTerminalStatus, sourceWorker, false, replayWorkerTerminal, "the status of a terminal's shell changed to starting, ready or startup_failed, or to exited without a terminal.exit"},
	{kindTerminalTitle, sourceWorker, false, replayWorkerTerminal, "a program set the title of a terminal"},
	{kindTerminalBell, sourceWorker, false, replayWorkerTerminal, "a terminal rang its bell"},
	{kindTerminalNotification, sourceWorker, false, replayWorkerTerminal, "a program in a terminal asked for a desktop notification"},
	{kindTerminalProgress, sourceWorker, false, replayWorkerTerminal, "a program in a terminal reported its progress"},
	{kindTerminalExit, sourceWorker, false, replayWorkerTerminal, "a terminal's shell exited, with its exit code"},
	{kindNotificationCreated, sourceWorker, false, replayNone, "a live toast notification was published on a Worker"},
	{kindNotificationCleared, sourceWorker, false, replayNone, "a notification was cleared on a Worker"},
	{kindStreamReady, sourceStream, false, replayNone, "the stream watches every source of its scope"},
	{kindStreamUnavailable, sourceStream, false, replayNone, "the hub, a Worker, or a Worker for one tab cannot deliver events; retrying says whether the stream tries again"},
	{kindStreamGap, sourceStream, false, replayNone, "a source delivers again; events of the kinds in lost, between since and time, may be lost"},
	{kindStreamHeartbeat, sourceStream, false, replayNone, "the stream runs; only --heartbeat prints it"},
	{kindStreamRetired, sourceStream, false, replayNone, "the Worker finished the checkpointed tab's event history"},
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

// tabReplayJournal returns the retained Worker journal for tabType.
func tabReplayJournal(tabType leapmuxv1.TabType) replayJournal {
	switch tabType {
	case leapmuxv1.TabType_TAB_TYPE_AGENT:
		return replayWorkerAgent
	case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
		return replayWorkerTerminal
	default:
		return replayNone
	}
}

// kindFilter selects the kinds that the stream prints on stdout.
type kindFilter struct {
	// selected holds each selected kind. Nil selects every kind.
	selected map[string]bool
	// explicit holds the trimmed values passed to --kind in order.
	explicit []string
}

// parseKindFilter reads the values of --kind. Each value is a kind or a
// family, which selects every kind of the family. No value selects every
// kind.
func parseKindFilter(values []string) (kindFilter, error) {
	if len(values) == 0 {
		return kindFilter{}, nil
	}
	selected := map[string]bool{}
	explicit := make([]string, 0, len(values))
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
		explicit = append(explicit, value)
	}
	return kindFilter{selected: selected, explicit: explicit}, nil
}

// all reports whether the filter selects every kind without restriction.
func (f kindFilter) all() bool {
	return f.selected == nil
}

// selects reports whether the filter prints kind on stdout.
func (f kindFilter) selects(kind string) bool {
	return f.selected == nil || f.selected[kind]
}

// selectsJournal reports whether the filter selects at least one retained kind
// stored in journal.
func (f kindFilter) selectsJournal(journal replayJournal) bool {
	if journal == replayNone {
		return false
	}
	for _, k := range eventKinds {
		if k.journal == journal && f.selects(k.name) {
			return true
		}
	}
	return false
}

// retainedKinds returns the selected retained kinds belonging to journals, in
// vocabulary order.
func (f kindFilter) retainedKinds(journals ...replayJournal) []string {
	out := make([]string, 0)
	for _, k := range eventKinds {
		if k.journal != replayNone && slices.Contains(journals, k.journal) && f.selects(k.name) {
			out = append(out, k.name)
		}
	}
	return out
}

// selectsStreamKind reports whether the filter selects any stream.* kind
// (excluding stream.retired unless includeRetired is true).
func (f kindFilter) selectsStreamKind(includeRetired bool) bool {
	for _, k := range eventKinds {
		if k.source != sourceStream {
			continue
		}
		if !includeRetired && k.name == kindStreamRetired {
			continue
		}
		if f.selects(k.name) {
			return true
		}
	}
	return false
}

// layoutCheckpointKinds returns the Hub layout journal kinds selected by filter.
func layoutCheckpointKinds(filter kindFilter) []string {
	return filter.retainedKinds(replayHubLayout)
}

// validateLayoutCheckpointFilter validates filter for a Hub layout checkpoint
// and returns the sorted retained Hub kinds stored in the checkpoint file.
func validateLayoutCheckpointFilter(filter kindFilter) ([]string, error) {
	return validateCheckpointFilter(filter, "layout", leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED, []replayJournal{replayHubLayout}, false)
}

// validateTabCheckpointFilter validates filter for a single-tab checkpoint and
// returns the retained kinds for tabType stored in the checkpoint file.
func validateTabCheckpointFilter(filter kindFilter, tabType leapmuxv1.TabType) ([]string, error) {
	journal := tabReplayJournal(tabType)
	if journal == replayNone {
		return nil, cli.Usagef("--checkpoint requires an agent or terminal tab")
	}
	return validateCheckpointFilter(filter, "tab", tabType, []replayJournal{journal}, true)
}

// validateAccountCheckpointFilter validates filter for an account checkpoint
// and returns the retained kinds across Hub and Worker journals.
func validateAccountCheckpointFilter(filter kindFilter) ([]string, error) {
	return validateCheckpointFilter(filter, "account", leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED,
		[]replayJournal{replayHubLayout, replayWorkerAgent, replayWorkerTerminal}, false)
}

func validateCheckpointFilter(filter kindFilter, scope string, tabType leapmuxv1.TabType, allowed []replayJournal, allowRetired bool) ([]string, error) {
	for _, token := range filter.explicit {
		if strings.Contains(token, ".") {
			k, ok := lookupEventKind(token)
			if !ok {
				continue
			}
			if k.source == sourceStream {
				if k.name == kindStreamRetired && !allowRetired {
					return nil, cli.Usagef("event kind %q is only emitted by a --tab checkpoint", token)
				}
				continue
			}
			if k.journal == replayNone {
				return nil, cli.Usagef("event kind %q is live-only and unavailable with --checkpoint", token).
					WithHint("omit --checkpoint to watch live-only events")
			}
			if !slices.Contains(allowed, k.journal) {
				return nil, incompatibleCheckpointSelection("kind", token, scope, tabType, k.journal)
			}
			continue
		}
		if token == "stream" {
			continue
		}
		var anyRetained, anyAllowed bool
		var firstJournal replayJournal
		for _, k := range eventKinds {
			if kindFamily(k.name) != token || k.journal == replayNone {
				continue
			}
			if !anyRetained {
				firstJournal = k.journal
			}
			anyRetained = true
			if slices.Contains(allowed, k.journal) {
				anyAllowed = true
			}
		}
		if !anyRetained {
			return nil, cli.Usagef("event family %q is live-only and unavailable with --checkpoint", token).
				WithHint("omit --checkpoint to watch live-only events")
		}
		if !anyAllowed {
			return nil, incompatibleCheckpointSelection("family", token, scope, tabType, firstJournal)
		}
	}
	kinds := filter.retainedKinds(allowed...)
	if len(kinds) == 0 && !filter.selectsStreamKind(allowRetired) {
		return nil, cli.Usagef("the selected event kinds are unavailable in this checkpoint scope")
	}
	return kinds, nil
}

func incompatibleCheckpointSelection(noun, token, scope string, tabType leapmuxv1.TabType, journal replayJournal) error {
	switch scope {
	case "layout":
		return cli.Usagef("event %s %q is unavailable in a layout checkpoint", noun, token).
			WithHint("use an account checkpoint (without --workspace) or pass --tab for Worker events")
	case "tab":
		err := cli.Usagef("event %s %q is unavailable in a %s tab checkpoint", noun, token, tabTypeName(tabType))
		if journal == replayHubLayout {
			return err.WithHint("use a layout or account checkpoint for Hub events")
		}
		other := "terminal"
		if journal == replayWorkerAgent {
			other = "agent"
		}
		return err.WithHint("use an account checkpoint or pass a %s --tab", other)
	default:
		return cli.Usagef("event %s %q is unavailable in this checkpoint scope", noun, token)
	}
}

func lookupEventKind(name string) (eventKind, bool) {
	for _, k := range eventKinds {
		if k.name == name {
			return k, true
		}
	}
	return eventKind{}, false
}

// kindFlagValues lists what --kind accepts: every family, then every kind.
func kindFlagValues() []string {
	return append(eventFamilies(), eventKindNames()...)
}
