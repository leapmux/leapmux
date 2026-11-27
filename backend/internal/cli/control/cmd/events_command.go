package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/coder/quartz"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
)

// minEventHeartbeat is the shortest interval that --heartbeat accepts. A
// shorter one prints more heartbeats than events.
const minEventHeartbeat = time.Second

func newEventsCommand() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:              "events",
		Aliases:          []string{"ev"},
		Short:            "Print what happens in your workspaces as it happens, one event per line",
		Long:             eventsLongHelp(),
		QuietUnsupported: true,
		Examples: []cli.Example{
			{Command: "leapmux events"},
			{Comment: "Print the turn ends and the requests of the agents in this workspace, as JSON.", Command: "leapmux events --workspace self --kind agent.turn_end --kind agent.request --json"},
			{Comment: "Wait for the next request of any agent, and print it.", Command: "leapmux events --kind agent.request --limit 1 --json"},
			{Comment: "Watch every terminal, with a heartbeat that shows the stream runs.", Command: "leapmux events --kind terminal --heartbeat 30s"},
			{Comment: "Watch a single terminal as it moves between workspaces.", Command: "leapmux events --tab 87a19d5f --kind terminal --json"},
			{Comment: "Resume one terminal's event history across CLI runs.", Command: "leapmux events --tab 87a19d5f --checkpoint terminal-events.json --json"},
			{Comment: "Resume permission and question events for one agent.", Command: "leapmux events --tab 87a19d5f --checkpoint agent-events.json --kind agent.request,agent.request_ended --json"},
			{Comment: "Resume terminal events from every Worker in the account.", Command: "leapmux events --checkpoint account-events.json --kind terminal --json"},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.String("workspace", "", `watch only this workspace: an id, a prefix, "title:<title>" or "self" (default: every workspace)`)
			fs.String("tab", "", `watch only this tab: an id, a prefix, "id:<id>", "title:<title>" or "self"`)
			fs.StringSlice("kind", nil, `print only this kind of event, or every kind of this family, such as "agent"; repeat it, or separate the values with commas (default: every kind)`)
			cli.SetEnum(fs, "kind", kindFlagValues()...)
			for _, name := range []string{"workspace", "tab", "kind"} {
				cli.SetFlagGroup(fs, name, "Scope")
			}
			fs.Int("limit", 0, "end the stream after this many events; 0 runs without a limit")
			fs.Duration("timeout", 0, "stop the stream cleanly after this long, such as 30s or 5m; 0 runs without a limit")
			fs.Bool("snapshot", false, "emit stream.ready (after any checkpointed backlog) and exit cleanly")
			fs.Duration("heartbeat", 0, "print a stream.heartbeat event at this interval, such as 30s; 0 prints none")
			fs.Bool("reconnect", true, "reconnect automatically if the Hub or a Worker disconnects")
			for _, name := range []string{"limit", "timeout", "snapshot", "heartbeat", "reconnect"} {
				cli.SetFlagGroup(fs, name, "Stream")
			}
			fs.String("checkpoint", "", "save and resume retained events for a tab, workspace, or account")
			cli.SetFlagGroup(fs, "checkpoint", "Checkpoint")
		},
		Run: runEvents,
	})
}

// eventsLongHelp is the help of `events`, with one line for each kind of
// the vocabulary.
func eventsLongHelp() string {
	var b strings.Builder
	b.WriteString(`Print what happens in your workspaces as it happens: tabs that open, close
and move, what each agent does and asks, what each terminal reports, and the
health of the stream itself. Each line is the local time, the kind, the ids,
and a summary; --json prints one {"data": …} envelope per line instead, with
the full ids and a field for each fact. Ctrl-C ends the stream with exit
status 0.

The stream watches every workspace, or the one that --workspace selects. A
tab that opens while the stream runs joins it, and a tab that closes leaves
it. With --tab, the stream watches that tab only and follows its moves between
workspaces. If --workspace also selects a workspace, the stream ends when
the tab leaves it. The stream also ends when the selected tab closes or
the selected workspace is deleted.

The first stream.ready line reports the sources that the stream watches and
which of them can deliver. Read the state that you need after it, such as
"leapmux agent list". A new run without --checkpoint starts from
the present.

Use --tab with --checkpoint to resume the retained events of one agent or
terminal. The command saves its position after writing each selected event.
Reuse the same file and --kind selection on the next run. A record can
appear again if the process stops after writing its line and before saving
its cursor. Use journal_id, tab_stream_id and sequence to deduplicate.
The Worker reports gaps when it cannot supply retained history. A replayed
agent request has a request_instance that correlates it with its end. It
has a request handle only while that exact instance still needs an answer.
With --checkpoint, --limit counts only the printed non-stream events. A --tab
checkpoint follows the tab across workspace moves and does not take --workspace.
After the Worker retires an agent or terminal, resume with its full ID
and the same checkpoint. The stream exits after stream.retired confirms
the final Worker position. Closing a subagent transcript keeps its
history open. A terminal's shell can also exit while its tab stays open.

Use --checkpoint without --tab or --workspace to resume account events when
--kind selects Worker event families (or when --kind is omitted). The
checkpoint stores a separate position for each selected Worker event family
and for the Hub layout when selected. A membership change can leave a gap in
Worker history. The stream reports the gap before recording the new inventory.
When --kind selects only Hub layout kinds (workspace.* or tab.*), an unscoped
--checkpoint uses a Hub-wide layout checkpoint so a workspace:read credential
works without worker:read.

Use --workspace with --checkpoint to resume that workspace's committed
Hub events. Resume with the same scope and kinds. For a deleted
workspace, pass its full ID. The command exits after it reaches the
final layout position. The Hub retains tabs that opened, moved, or
closed between runs.

In a stream without --checkpoint, when the Hub or a Worker cannot deliver, it prints
stream.unavailable and retries. When the source delivers again, it prints
stream.gap with the kinds that it may have lost, then each change of an
agent's activity and status, and of the tabs and workspaces, that happened
during the gap, marked as reconciled. --kind does not hide these lines: a
stream event that it does not select prints on stderr.

Kinds:
`)
	// One bullet per kind, as the other commands write a list, so the
	// generated reference page renders it as a list too.
	for _, k := range eventKinds {
		fmt.Fprintf(&b, "  - %s: %s\n", k.name, k.help)
	}
	return strings.TrimRight(b.String(), "\n")
}

// eventsOptions is what the flags of `events` select.
type eventsOptions struct {
	filter      kindFilter
	limit       int
	timeout     time.Duration
	snapshot    bool
	noReconnect bool
	heartbeat   time.Duration
	// workspace is the value of --workspace, or "" for every workspace.
	workspace string
	// tab is the value of --tab, or "" for every tab in the workspace scope.
	tab        string
	checkpoint string
}

// checkpointDispatch selects which checkpoint scope runs for opts. Unscoped
// Hub-only kind selections stay on layout so credentials with only
// workspace:read do not require worker:read.
func checkpointDispatch(opts eventsOptions) string {
	if opts.tab != "" {
		return tabCheckpointScope
	}
	if opts.workspace == "" && len(accountWorkerKinds(opts.filter)) != 0 {
		return accountCheckpointScope
	}
	return layoutCheckpointScope
}

// withClockTimeout returns a context canceled after d on clock. Using
// clock.AfterFunc instead of context.WithTimeout lets tests drive replay
// timeouts deterministically with quartz.Mock.
func withClockTimeout(parent context.Context, clock quartz.Clock, d time.Duration, tag string) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return parent, func() {}
	}
	if clock == nil {
		clock = quartz.NewReal()
	}
	ctx, cancel := context.WithCancelCause(parent)
	timer := clock.AfterFunc(d, func() {
		cancel(context.DeadlineExceeded)
	}, tag)
	return ctx, func() {
		timer.Stop(tag)
		cancel(nil)
	}
}

// eventsOptionsOf reads the flags of `events`, before anything is sent.
func eventsOptionsOf(inv *cli.Invocation) (eventsOptions, error) {
	kinds, err := inv.Command.Flags().GetStringSlice("kind")
	if err != nil {
		return eventsOptions{}, cli.Wrap(cli.CodeUsage, err, "read --kind")
	}
	filter, err := parseKindFilter(kinds)
	if err != nil {
		return eventsOptions{}, err
	}
	limit, err := inv.Command.Flags().GetInt("limit")
	if err != nil {
		return eventsOptions{}, cli.Wrap(cli.CodeUsage, err, "read --limit")
	}
	if limit < 0 {
		return eventsOptions{}, cli.Usagef("--limit must not be negative")
	}
	timeout, err := waitTimeoutFlag(inv)
	if err != nil {
		return eventsOptions{}, err
	}
	heartbeat, err := inv.Command.Flags().GetDuration("heartbeat")
	if err != nil {
		return eventsOptions{}, cli.Wrap(cli.CodeUsage, err, "read --heartbeat")
	}
	if heartbeat < 0 || (heartbeat > 0 && heartbeat < minEventHeartbeat) {
		return eventsOptions{}, cli.Usagef("--heartbeat must be 0 or at least %s", minEventHeartbeat)
	}
	for _, name := range []string{"workspace", "tab"} {
		if blankTargetFlag(inv.Command, name) {
			return eventsOptions{}, cli.Usagef("--%s is empty", name)
		}
	}
	opts := eventsOptions{
		filter:      filter,
		limit:       limit,
		timeout:     timeout,
		snapshot:    boolFlag(inv, "snapshot"),
		noReconnect: !boolFlag(inv, "reconnect"),
		heartbeat:   heartbeat,
		workspace:   stringFlag(inv, "workspace"),
		tab:         stringFlag(inv, "tab"),
	}
	if inv.Changed("checkpoint") {
		opts.checkpoint = strings.TrimSpace(inv.Command.Flag("checkpoint").Value.String())
		if opts.checkpoint == "" || (opts.tab != "" && opts.workspace != "") {
			return eventsOptions{}, cli.Usagef("--checkpoint requires a file and accepts one scope")
		}
	}
	return opts, nil
}

// runEvents streams the events of the scope until the user stops it.
func runEvents(inv *cli.Invocation) (any, error) {
	opts, err := eventsOptionsOf(inv)
	if err != nil {
		return nil, err
	}
	s, err := openStreamSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	if opts.checkpoint != "" {
		clock := quartz.NewReal()
		ctx, cancel := withClockTimeout(s.ctx, clock, opts.timeout, eventTimeoutTimerTag)
		defer cancel()
		switch checkpointDispatch(opts) {
		case tabCheckpointScope:
			return nil, s.runCheckpointedEvents(ctx, clock, opts)
		case accountCheckpointScope:
			return nil, s.runAccountCheckpointedEvents(ctx, clock, opts)
		default:
			return nil, s.runLayoutCheckpointedEvents(ctx, clock, opts)
		}
	}
	workspaceID, tabID, err := resolveEventScope(s, opts)
	if err != nil {
		return nil, err
	}
	var workspaceIDs []string
	if workspaceID != "" {
		workspaceIDs = []string{workspaceID}
	}
	channels := newSharedWorkerChannels(s.c)
	stream := newEventStream(eventStreamConfig{
		printer:     newEventPrinter(inv.Printer, opts.filter, opts.limit),
		clock:       quartz.NewReal(),
		workspace:   workspaceID,
		tabID:       tabID,
		heartbeat:   opts.heartbeat,
		timeout:     opts.timeout,
		snapshot:    opts.snapshot,
		noReconnect: opts.noReconnect,
		hubSession:  hubUserEvents(s.c, workspaceIDs),
		workerSource: func(workerID string) workerFeedSource {
			return workerFeedSourceWithChannels(s.c, workerID, channels)
		},
		privateSource: func(workerID string) workerPrivateSource {
			return workerPrivateSourceOf(s.c, workerID, channels)
		},
	})
	return nil, stream.run(s.ctx)
}

// resolveEventScope resolves the selectors with a deadline independent of the
// stream's lifetime. A tab title or prefix searches the selected workspace.
func resolveEventScope(s *session, opts eventsOptions) (workspaceID, tabID string, err error) {
	if opts.workspace == "" && opts.tab == "" {
		return "", "", nil
	}
	callSession := s.bounded()
	defer callSession.Close()
	if opts.workspace != "" {
		workspace, err := callSession.resolveOne(resolve.KindWorkspace, opts.workspace)
		if err != nil {
			return "", "", err
		}
		workspaceID = workspace.ID
	}
	if opts.tab != "" {
		listing := callSession.listing()
		if workspaceID != "" {
			listing.Tabs = func(ctx context.Context) ([]*leapmuxv1.WorkspaceTab, error) {
				return listTabsInWorkspaces(ctx, s.c, []string{workspaceID})
			}
		}
		tab, err := resolve.Canonical(callSession.ctx, listing, resolve.KindTab, opts.tab)
		if err != nil {
			return "", "", err
		}
		tabID = tab.ID
	}
	return workspaceID, tabID, nil
}
