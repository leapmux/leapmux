package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/coder/quartz"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

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
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.String("workspace", "", `watch only this workspace: an id, a prefix, "title:<title>" or "self" (default: every workspace)`)
			fs.StringSlice("kind", nil, `print only this kind of event, or every kind of this family, such as "agent"; repeat it, or separate the values with commas (default: every kind)`)
			cli.SetEnum(fs, "kind", kindFlagValues()...)
			fs.Int("limit", 0, "end the stream after this many events; 0 runs without a limit")
			fs.Duration("heartbeat", 0, "print a stream.heartbeat event at this interval, such as 30s; 0 prints none")
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
it. The stream ends when the workspace that --workspace selects is deleted.

The first stream.ready line says that the stream watches every source. Read
the state that you need after it, such as "leapmux control agent list": every
change after that read reaches the stream. The stream has no cursor, because
a Worker keeps no record of these events, so a new run starts from the
present.

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
	workspace := ""
	if inv.Changed("workspace") {
		workspace = stringFlag(inv, "workspace")
		if strings.TrimSpace(workspace) == "" {
			return eventsOptions{}, cli.Usagef("--workspace is empty")
		}
	}
	return eventsOptions{filter: filter, limit: limit, heartbeat: heartbeat, workspace: workspace}, nil
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
	workspaceID, err := eventsWorkspace(s, opts.workspace)
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
		heartbeat:  opts.heartbeat,
		hubSession: hubUserEvents(s.c, workspaceIDs),
		workerSource: func(workerID string) workerFeedSource {
			return workerFeedSourceOf(s.c, workerID)
		},
	})
	return nil, stream.run(s.ctx)
}

// eventsWorkspace resolves the input of --workspace, or returns "" for every
// workspace when the input is "". The resolution has a deadline, so it
// cannot hang while the stream itself runs until the user stops it.
func eventsWorkspace(s *session, input string) (string, error) {
	if input == "" {
		return "", nil
	}
	bounded := s.bounded()
	defer bounded.Close()
	ws, err := bounded.resolveOne(resolve.KindWorkspace, input)
	if err != nil {
		return "", err
	}
	return ws.ID, nil
}
