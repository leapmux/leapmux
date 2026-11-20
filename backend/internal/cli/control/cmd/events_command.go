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
		Use:   "events",
		Short: "Print what happens in your workspaces as it happens, one event per line",
		Long:  eventsLongHelp(),
		Examples: []cli.Example{
			{Command: "leapmux control events"},
			{Comment: "Print the turn ends and the requests of the agents in this workspace, as JSON.", Command: "leapmux control events --workspace self --kind agent.turn_end --kind agent.request --json"},
			{Comment: "Wait for the next request of any agent, and print it.", Command: "leapmux control events --kind agent.request --limit 1 --json"},
			{Comment: "Watch every terminal, with a heartbeat that shows the stream runs.", Command: "leapmux control events --kind terminal --heartbeat 30s"},
			{Comment: "Watch a single terminal as it moves between workspaces.", Command: "leapmux control events --tab 87a19d5f --kind terminal --json"},
			{Comment: "Resume one terminal's event history across CLI runs.", Command: "leapmux control events --tab 87a19d5f --checkpoint terminal-events.json --json"},
			{Comment: "Resume permission and question events for one agent.", Command: "leapmux control events --tab 87a19d5f --checkpoint agent-events.json --kind agent.request,agent.request_ended --json"},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.String("workspace", "", `watch only this workspace: an id, a prefix, "title:<title>" or "self" (default: every workspace)`)
			fs.String("tab", "", `watch only this tab: an id, a prefix, "id:<id>", "title:<title>" or "self"`)
			fs.StringSlice("kind", nil, `print only this kind of event, or every kind of this family, such as "agent"; repeat it, or separate the values with commas (default: every kind)`)
			cli.SetEnum(fs, "kind", kindFlagValues()...)
			fs.Int("limit", 0, "end the stream after this many events; 0 runs without a limit")
			fs.Duration("heartbeat", 0, "print a stream.heartbeat event at this interval, such as 30s; 0 prints none")
			fs.String("checkpoint", "", "save and resume the event position for the agent or terminal selected by --tab")
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
"leapmux control agent list". A new run without --checkpoint starts from
the present.

Use --tab with --checkpoint to resume the retained events of one agent or
terminal. The command saves its position after writing each selected event.
Reuse the same file and --kind selection on the next run. A record can
appear again if the process stops after writing its line and before saving
its cursor. Use journal_id, tab_stream_id and sequence to deduplicate.
The Worker reports gaps when it cannot supply retained history. A replayed
agent request has a request_instance that correlates it with its end. It
has a request handle only while that exact instance still needs an answer.
With --checkpoint, --limit counts only the printed tab events. This mode
follows the tab across workspace moves and does not take --workspace.
After the Worker retires an agent or terminal, resume with its full ID
and the same checkpoint. The stream exits after stream.retired confirms
the final Worker position. Closing a subagent transcript keeps its
history open. A terminal's shell can also exit while its tab stays open.

When the hub or a Worker cannot deliver, the stream prints
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
	filter    kindFilter
	limit     int
	heartbeat time.Duration
	// workspace is the value of --workspace, or "" for every workspace.
	workspace string
	// tab is the value of --tab, or "" for every tab in the workspace scope.
	tab        string
	checkpoint string
}

// eventsOptionsOf reads the flags of `events`, before anything is sent.
func eventsOptionsOf(inv *cli.Invocation) (eventsOptions, error) {
	if inv.Printer.Quiet {
		return eventsOptions{}, cli.Usagef("--quiet does not apply to events, which have no single id").
			WithHint("use --json, or the text form")
	}
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
	opts := eventsOptions{filter: filter, limit: limit, heartbeat: heartbeat, workspace: stringFlag(inv, "workspace"), tab: stringFlag(inv, "tab")}
	if inv.Changed("checkpoint") {
		opts.checkpoint = inv.Command.Flag("checkpoint").Value.String()
		if opts.checkpoint == "" || opts.tab == "" || opts.workspace != "" {
			return eventsOptions{}, cli.Usagef("--checkpoint requires a file and --tab, without --workspace")
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
		return nil, s.runCheckpointedEvents(opts)
	}
	workspaceID, tabID, err := resolveEventScope(s, opts)
	if err != nil {
		return nil, err
	}
	var workspaceIDs []string
	if workspaceID != "" {
		workspaceIDs = []string{workspaceID}
	}
	stream := newEventStream(eventStreamConfig{
		printer:    &eventPrinter{p: inv.Printer, filter: opts.filter, limit: opts.limit},
		clock:      quartz.NewReal(),
		workspace:  workspaceID,
		tabID:      tabID,
		heartbeat:  opts.heartbeat,
		hubSession: hubUserEvents(s.c, workspaceIDs),
		workerSource: func(workerID string) workerFeedSource {
			return workerFeedSourceOf(s.c, workerID)
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
	bounded := s.bounded()
	defer bounded.Close()
	if opts.workspace != "" {
		workspace, err := bounded.resolveOne(resolve.KindWorkspace, opts.workspace)
		if err != nil {
			return "", "", err
		}
		workspaceID = workspace.ID
	}
	if opts.tab != "" {
		listing := bounded.listing()
		if workspaceID != "" {
			listing.Tabs = func(ctx context.Context) ([]*leapmuxv1.WorkspaceTab, error) {
				return listTabsInWorkspaces(ctx, s.c, []string{workspaceID})
			}
		}
		tab, err := resolve.Canonical(bounded.ctx, listing, resolve.KindTab, opts.tab)
		if err != nil {
			return "", "", err
		}
		tabID = tab.ID
	}
	return workspaceID, tabID, nil
}
