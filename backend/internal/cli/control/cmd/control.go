// Package cmd implements `leapmux control` and every command under it.
//
// NewCommand builds the tree. Each group lives in its own file and declares
// its commands with cli.NewLeaf, so the parsing, the help, the completion,
// `leapmux commands` and the reference page of the docs site all read one
// declaration.
package cmd

import (
	"context"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
)

// guideFooter is the last line of each group's help. It points an agent that
// reads the help at the guide, as Herdr's help does.
const guideFooter = `An agent that drives LeapMux reads "leapmux control guide" first.`

// NewCommand builds `leapmux control`.
func NewCommand() *cobra.Command {
	root := cli.Group("control", "Control LeapMux from a script or from an agent",
		newWhoamiCommand(),
		newVersionCommand(),
		newGuideCommand(),
		newAuthCommand(),
		newWorkspaceCommand(),
		newTabCommand(),
		newAgentCommand(),
		newTerminalCommand(),
		newFileCommand(),
		newGitCommand(),
		newTileCommand(),
		newLayoutCommand(),
		newWorkerCommand(),
		newEventsCommand(),
		newNotifyCommand(),
		newRPCCommand(),
		newAdminCommand(),
	)
	root.Long = `Control LeapMux from a script or from an agent.

A command acts on the object that its first argument gives. Use a full id, a
unique id prefix of at least 4 characters, or "id:<id>" for a literal id.
"title:<title>" selects a tab or workspace. "name:<name>" selects a worker.
"self" selects the tab, tile, workspace or worker that runs the command.
A command that only reads defaults to "self". A command that changes or
removes an object needs an explicit target.

Without --hub, the CLI reaches the hub through the socket of the LeapMux tab
that runs it. Outside a tab, it uses $LEAPMUX_HUB, then the default hub of
the stored credentials.`
	flags := root.PersistentFlags()
	flags.String("hub", "", "hub URL (default: the socket of this tab, else $LEAPMUX_HUB, else the default hub of the stored credentials)")
	cli.AddOutputFlags(root)
	configureTargetArguments(root)
	addHubHints(root)
	return root
}

// explicitHub returns the --hub value when the user typed it, and "" when
// the flag kept its default.
func explicitHub(cmd *cobra.Command) string {
	if flag := cmd.Flag("hub"); flag != nil && flag.Changed {
		return strings.TrimSpace(flag.Value.String())
	}
	return ""
}

// hubURLFor returns the hub of a command that always talks to a hub
// directly, never through a tab's socket: an explicit --hub, else the
// default hub (see control.DefaultHub).
func hubURLFor(cmd *cobra.Command) (string, error) {
	if hub := explicitHub(cmd); hub != "" {
		return hub, nil
	}
	return control.DefaultHub()
}

// newClient builds the client of an invocation, with the precedence that
// control.NewClientFromEnv states.
func newClient(inv *cli.Invocation) (*control.Client, error) {
	return control.NewClientFromEnv(explicitHub(inv.Command))
}

// session is one invocation's connection: the client, the context that
// limits its calls, and the cache of the tabs it located.
type session struct {
	inv    *cli.Invocation
	c      *control.Client
	ctx    context.Context
	cancel context.CancelFunc
	// located caches the hub's row of each tab that the session resolved,
	// so a prefix that already listed a tab does not locate it again.
	located map[string]*leapmuxv1.WorkspaceTab
}

// openSession builds the client and a context limited by rpcDeadline.
func openSession(inv *cli.Invocation) (*session, error) {
	c, err := newClient(inv)
	if err != nil {
		return nil, err
	}
	ctx, cancel := rpcDeadline(inv.Context)
	return &session{inv: inv, c: c, ctx: ctx, cancel: cancel, located: map[string]*leapmuxv1.WorkspaceTab{}}, nil
}

// openStreamSession builds the client and a context that only the process
// ends, for a command that streams until the user stops it.
func openStreamSession(inv *cli.Invocation) (*session, error) {
	c, err := newClient(inv)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(inv.Context)
	return &session{inv: inv, c: c, ctx: ctx, cancel: cancel, located: map[string]*leapmuxv1.WorkspaceTab{}}, nil
}

// Close releases the session's context.
func (s *session) Close() { s.cancel() }

// bounded returns a session that shares this one's client and cache, with a
// context that rpcDeadline limits. A streaming command resolves its target
// through it, so the resolution cannot hang while the stream itself runs
// until the user stops it. The caller closes the returned session.
func (s *session) bounded() *session {
	ctx, cancel := rpcDeadline(s.ctx)
	return &session{inv: s.inv, c: s.c, ctx: ctx, cancel: cancel, located: s.located}
}

// deps returns the resolver's dependencies, with LocateTab answered from the
// session's cache first.
func (s *session) deps() resolve.Deps {
	deps := resolveDeps(s.c)
	locate := deps.LocateTab
	deps.LocateTab = func(ctx context.Context, tabType leapmuxv1.TabType, tabID string) (*leapmuxv1.WorkspaceTab, error) {
		if row, ok := s.located[tabID]; ok && (tabType == leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED || row.GetTab().GetTabType() == tabType) {
			return row, nil
		}
		row, err := locate(ctx, tabType, tabID)
		if err == nil {
			s.located[tabID] = row
		}
		return row, err
	}
	return deps
}

// listing returns what resolve.Canonical needs to resolve a short form.
func (s *session) listing() resolve.Listing {
	return targetListing(s.c, s.deps())
}

// tabTarget is a tab target with its placement.
type tabTarget struct {
	ID          string
	Type        leapmuxv1.TabType
	WorkerID    string
	WorkspaceID string
	TileID      string
	// Self reports that the target is the caller's own tab.
	Self bool
	// Row is the hub's row for the tab, when the resolution read it.
	Row *leapmuxv1.WorkspaceTab
}

// resolveTab resolves the tab target input of kind and derives what need
// asks for. An empty input selects the caller's own tab. The command parser
// rejects explicit blank targets, so empty input here means an omitted target.
//
// The Worker defaults to $LEAPMUX_CONTROL_WORKER_ID. Inside a tab, "self"
// then needs no hub call at all when the command needs only the Worker, and
// a quake terminal, which the hub cannot place, still resolves.
func (s *session) resolveTab(kind resolve.Kind, input string, need resolve.Need) (tabTarget, error) {
	if input == "" {
		input = resolve.SelfTarget
	}
	t, err := resolve.Canonical(s.ctx, s.listing(), kind, input)
	if err != nil {
		return tabTarget{}, err
	}
	if t.Tab != nil {
		s.located[t.ID] = t.Tab
	}
	need.TabID = true
	got, err := resolve.Resolve(s.ctx, s.deps(), need, resolve.Inputs{
		TabID:         t.ID,
		ExplicitTabID: !t.Self,
		WorkerID:      strings.TrimSpace(os.Getenv(controlenv.WorkerIDVariable)),
		FixedTabType:  kind.TabType(),
	})
	if err != nil {
		return tabTarget{}, err
	}
	return tabTarget{
		ID:          got.TabID,
		Type:        got.TabType,
		WorkerID:    got.WorkerID,
		WorkspaceID: got.WorkspaceID,
		TileID:      got.TileID,
		Self:        t.Self,
		Row:         got.Tab,
	}, nil
}

// resolveOne resolves a target of a kind that is not a tab: a tile, a
// workspace or a worker. An empty input selects the caller's own.
func (s *session) resolveOne(kind resolve.Kind, input string) (resolve.Target, error) {
	if input == "" {
		input = resolve.SelfTarget
	}
	return resolve.Canonical(s.ctx, s.listing(), kind, input)
}

// workerFlag is the scope flag that selects a worker.
const workerFlag = "worker"

// addWorkerFlag registers --worker. The flag accepts every target form, and
// it defaults to the worker of the tab that runs the command.
func addWorkerFlag(fs *pflag.FlagSet) {
	fs.String(workerFlag, "", `worker: an id, a unique id prefix, "name:<name>" or "self"`)
	cli.SetEnvDefault(fs, workerFlag, controlenv.WorkerIDVariable)
}

// workerScope resolves the --worker flag of a command that runs on one
// worker. Without the flag, it selects the worker of the tab that runs the
// command.
func (s *session) workerScope() (string, error) {
	input := ""
	if s.inv.Changed(workerFlag) {
		input = s.inv.Command.Flag(workerFlag).Value.String()
		if strings.TrimSpace(input) == "" {
			return "", cli.Usagef("--worker is empty")
		}
	}
	if input == "" && os.Getenv(controlenv.WorkerIDVariable) == "" {
		return "", cli.Usagef("no worker selected").
			WithHint("pass --worker <worker>, or run the command inside a LeapMux tab")
	}
	t, err := s.resolveOne(resolve.KindWorker, input)
	if err != nil {
		return "", err
	}
	return t.ID, nil
}

// stringFlag returns the value of the flag name.
func stringFlag(inv *cli.Invocation, name string) string {
	return inv.Command.Flag(name).Value.String()
}

// boolFlag returns the value of the boolean flag name.
func boolFlag(inv *cli.Invocation, name string) bool {
	return inv.Command.Flag(name).Value.String() == "true"
}
