package cmd

import (
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
)

// `agent sessions` lists the provider sessions that `agent open --resume`
// continues. It takes the provider, the Worker and the directory with the
// flags of `agent open`, and the same defaults, so the list it prints is the
// list that the same open accepts.

func newAgentSessionsCommand() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:   "sessions",
		Short: `List the earlier sessions that "agent open --resume" continues`,
		Long: `List the earlier sessions of a provider in one directory, newest first: the
sessions that "agent open --resume" continues with the same --provider,
--worker and --dir.

The Worker lists the sessions that LeapMux started and the sessions that the
provider's own store holds, such as a session that you started in a terminal.
It leaves out each session that an open agent runs, because a second process
on one session damages the session. It returns only the newest sessions, up
to a limit of its own.

A session's title is empty when the provider stores none.`,
		Examples: []cli.Example{
			{Comment: "List the sessions of this tab's provider in this tab's directory.", Command: "leapmux agent sessions"},
			{Comment: "List the Claude Code sessions of a directory on another worker.", Command: "leapmux agent sessions --provider claude --worker name:build-box --dir ~/src/app"},
			{Comment: "Keep the id of the newest session, for agent open --resume.", Command: "S=$(leapmux agent sessions --provider claude -q | head -1)"},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.String("provider", "", `agent provider; when neither this flag nor the variable gives one, the only provider that the worker offers`)
			cli.SetEnvDefault(fs, "provider", controlenv.AgentProviderVariable)
			addWorkerFlag(fs)
			addWorkingDirFlag(fs, "the directory that the sessions ran in")
		},
		Run: runAgentSessions,
	})
}

// agentSessionRow is one session that `agent open --resume` continues.
type agentSessionRow struct {
	// ID is the session's handle, which --resume takes as it is. A table
	// prints it whole, because the provider issued it, and no prefix of it
	// resolves.
	ID string `json:"id" table:"ID"`
	// UpdatedAt is the session's last activity, in UTC, or empty when the
	// provider's store recorded none.
	UpdatedAt string `json:"updated_at,omitempty" table:"UPDATED,dim"`
	// Title is the session's summary. It is empty when the provider stores
	// none.
	Title string `json:"title,omitempty" table:"TITLE"`
}

// agentSessionList is the result of `agent sessions`.
type agentSessionList struct {
	Sessions []agentSessionRow `json:"sessions"`
}

// runAgentSessions lists the sessions of one provider in one directory of one
// Worker. The provider resolution and the listing share one channel, as they
// do for `agent open`.
func runAgentSessions(inv *cli.Invocation) (any, error) {
	dir := workingDir(inv)
	if strings.TrimSpace(dir) == "" {
		return nil, cli.Usagef("no directory selected").
			WithHint("pass --dir <dir>, or run the command inside a LeapMux tab")
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	workerID, err := s.workerScope()
	if err != nil {
		return nil, err
	}
	if err := maybePreflightWorker(s.ctx, s.c, workerID); err != nil {
		return nil, err
	}
	provider := defaultProviderInput(inv)
	var resolved leapmuxv1.AgentProvider
	var resp leapmuxv1.ListAgentSessionsResponse
	err = withWorkerChannel(s.ctx, s.c, workerID, func(w workerCall) error {
		var err error
		if resolved, err = resolveProvider(s.ctx, provider, w.Call); err != nil {
			return err
		}
		return w.Call(s.ctx, leapmuxv1connect.WorkerAgentServiceListAgentSessionsProcedure, &leapmuxv1.ListAgentSessionsRequest{
			AgentProvider: resolved,
			WorkingDir:    dir,
		}, &resp)
	})
	if err != nil {
		return nil, err
	}
	rows := make([]agentSessionRow, 0, len(resp.GetSessions()))
	for _, session := range resp.GetSessions() {
		rows = append(rows, agentSessionRow{
			ID:        session.GetSessionId(),
			UpdatedAt: session.GetUpdatedAt(),
			Title:     session.GetTitle(),
		})
	}
	if len(rows) == 0 && inv.Printer.Format == cli.FormatText {
		inv.Printer.Notice("%s has no session to resume in %s", agentProviderName(resolved), dir)
	}
	return agentSessionList{Sessions: rows}, nil
}
