// Package cmd implements `leapmux control` and every command under it.
//
// NewCommand builds the tree. Each group lives in its own file and declares
// its commands with cli.NewLeaf, so the parsing, the help, the completion,
// `leapmux commands` and the reference page of the docs site all read one
// declaration.
package cmd

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
	"github.com/leapmux/leapmux/locallisten"
)

func init() {
	cli.SetDefaultStateClassifier(leapmuxStateClassifier)
}

// leapmuxStateClassifier maps LeapMux agent, terminal, worker, and health
// state tokens to their table cell tones.
func leapmuxStateClassifier(value string) cli.StateTone {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "idle", "ready", "active", "online", "ok", "pass", "running", "completed":
		return cli.StateToneGreen
	case "working", "starting", "pending", "warn", "queued", "dispatching":
		return cli.StateToneYellow
	case "waiting_for_user":
		return cli.StateToneMagenta
	case "error", "exited", "startup_failed", "failed", "offline", "fail", "closed", "canceled":
		return cli.StateToneRed
	default:
		return cli.StateToneNone
	}
}

// guideFooter is the last line of each group's help. It points an agent that
// reads the help at the guide, as Herdr's help does.
const guideFooter = `An agent that drives LeapMux reads "leapmux guide" first (or "leapmux guide --topics"); skip if a LeapMux skill is already in your context.`

// Attach registers all control commands and persistent flags directly onto
// root (used by `leapmux`). versionOverride optionally overrides the CLI
// version string reported by `leapmux version` (for binary tests).
func Attach(root *cobra.Command, versionOverride func() string) {
	cli.SetDefaultStateClassifier(leapmuxStateClassifier)
	controlCmds := []*cobra.Command{
		newTreeCommand(),
		newWorkspaceCommand(),
		newSectionCommand(),
		newTabCommand(),
		newAgentCommand(),
		newTerminalCommand(),
		newEventsCommand(),
		newNotifyCommand(),
		newNotificationCommand(),
		newFileCommand(),
		newGitCommand(),
		newWorktreeCommand(),
		newTileCommand(),
		newWindowCommand(),
		newLayoutCommand(),
		newWhoamiCommand(),
		newDoctorCommand(),
		newAuthCommand(),
		newContextCommand(),
		newSettingsCommand(),
		newWorkerCommand(),
		newTunnelCommand(),
		newAdminCommand(),
		newRPCCommand(),
		newGuideCommand(),
		newSkillCommand(),
		newVersionCommandWithOverride(versionOverride),
	}
	root.AddCommand(controlCmds...)
	flags := root.PersistentFlags()
	flags.String("hub", "", "hub URL (default: tab socket, else $LEAPMUX_HUB, else context/default credential, else local solo/dev Hub)")
	_ = root.RegisterFlagCompletionFunc("hub", completeHubFlag)
	cli.AddOutputFlags(root)
	for _, c := range controlCmds {
		configureTargetArguments(c)
		addHubHints(c)
	}
}

// NewCommand builds a standalone command tree containing all control commands.
// Unit tests in this package run commands against it directly.
func NewCommand() *cobra.Command {
	root := cli.Group("leapmux", "Control LeapMux from a script or from an agent")
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
	Attach(root, nil)
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

// errOutOf returns inv.Printer.Err when available, or nil so control helpers
// fall back to os.Stderr.
func errOutOf(inv *cli.Invocation) io.Writer {
	if inv != nil && inv.Printer != nil {
		return inv.Printer.Err
	}
	return nil
}

// attachErrOut wires inv.Printer.Err into c so interactive ceremonies (such as
// browser step-up) write to the invocation's error stream rather than a global.
func attachErrOut(c *control.HubClient, inv *cli.Invocation) *control.HubClient {
	if c != nil {
		if w := errOutOf(inv); w != nil {
			c.SetErrOut(w)
		}
	}
	return c
}

// noticeDiscoveredLocalHub writes a one-line stderr notice in text mode when a
// command connected to an auto-discovered local Hub IPC socket rather than an
// explicit --hub flag, LEAPMUX_HUB, or a saved context default.
func noticeDiscoveredLocalHub(inv *cli.Invocation, explicit, hubURL string) {
	if explicit != "" || strings.TrimSpace(os.Getenv("LEAPMUX_HUB")) != "" {
		return
	}
	if inv == nil || inv.Printer == nil || inv.Printer.Format != cli.FormatText {
		return
	}
	if !locallisten.IsLocal(hubURL) {
		return
	}
	saved, _ := control.LoadContext()
	if !control.SameHubURL(saved.Hub, hubURL) {
		_ = inv.Printer.Notice("using discovered local Hub %s (override with --hub or LEAPMUX_HUB)", hubURL)
	}
}

// newClient builds the client of an invocation, with the precedence that
// control.NewClientFromEnv states. When connecting outside a tab by
// auto-discovering a local solo/dev Hub, it prints a one-line stderr notice
// in text mode so the operator knows which Hub answered.
func newClient(inv *cli.Invocation) (control.Peer, error) {
	explicit := explicitHub(inv.Command)
	c, err := control.NewClientFromEnv(explicit)
	if err != nil {
		return nil, err
	}
	if hub, ok := c.(*control.HubClient); ok {
		attachErrOut(hub, inv)
		noticeDiscoveredLocalHub(inv, explicit, hub.HubURL)
	}
	return c, nil
}

// session is one invocation's connection: the client, the context that
// limits its calls, and the cache of the tabs it located.
type session struct {
	inv    *cli.Invocation
	c      control.Peer
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

// newCompletionSession wraps an already-opened peer and deadline-limited
// context in a session so shell completion can reuse session resolution
// helpers without re-dialing.
func newCompletionSession(ctx context.Context, cmd *cobra.Command, c control.Peer) *session {
	var inv *cli.Invocation
	if cmd != nil {
		inv = &cli.Invocation{Command: cmd, Context: ctx}
	}
	return &session{
		inv:     inv,
		c:       c,
		ctx:     ctx,
		cancel:  func() {},
		located: map[string]*leapmuxv1.WorkspaceTab{},
	}
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

// listWorkspaceTabs lists tabs in the workspace selected by --workspace (when
// set) or across every readable workspace, filtered by tabType (or all types
// when TAB_TYPE_UNSPECIFIED).
func (s *session) listWorkspaceTabs(tabType leapmuxv1.TabType) ([]*leapmuxv1.WorkspaceTab, error) {
	var workspaceIDs []string
	if s.inv.Changed("workspace") {
		ws, err := s.resolveOne(resolve.KindWorkspace, stringFlag(s.inv, "workspace"))
		if err != nil {
			return nil, err
		}
		workspaceIDs = []string{ws.ID}
	}
	tabs, err := listTabsInWorkspaces(s.ctx, s.c, workspaceIDs)
	if err != nil {
		return nil, err
	}
	return filterTabsByType(tabs, tabType), nil
}

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
// When kind accepts an agent and input is "parent", it resolves the calling
// agent's parent_agent_id on the calling Worker.
//
// When the command declares a scoping --workspace flag (not a destination
// flag like `tab move --workspace`), tab listings are narrowed to that
// workspace so bare titles disambiguate cleanly without touching unrelated
// Workers.
func (s *session) resolveTab(kind resolve.Kind, input string, need resolve.Need) (tabTarget, error) {
	if input == "" {
		input = resolve.SelfTarget
	}
	listing := s.listing()
	if s.hasTargetWorkspaceScope() {
		ws, err := s.resolveOne(resolve.KindWorkspace, stringFlag(s.inv, "workspace"))
		if err != nil {
			return tabTarget{}, err
		}
		listing.Tabs = func(ctx context.Context) ([]*leapmuxv1.WorkspaceTab, error) {
			return listTabsInWorkspaces(ctx, s.c, []string{ws.ID})
		}
	}
	t, err := resolve.Canonical(s.ctx, listing, kind, input)
	if err != nil {
		return tabTarget{}, err
	}
	if t.Tab != nil {
		s.located[t.ID] = t.Tab
	}
	need.TabID = true
	fixedType := kind.TabType()
	if t.Parent && fixedType == leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED {
		fixedType = leapmuxv1.TabType_TAB_TYPE_AGENT
	}
	workerID := t.WorkerID
	if workerID == "" {
		workerID = strings.TrimSpace(os.Getenv(controlenv.WorkerIDVariable))
	}
	got, err := resolve.Resolve(s.ctx, s.deps(), need, resolve.Inputs{
		TabID:         t.ID,
		ExplicitTabID: !t.Self && !t.Parent,
		WorkerID:      workerID,
		FixedTabType:  fixedType,
		TabType:       t.TabType,
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

// hasTargetWorkspaceScope reports whether the command supplied --workspace as
// a target-disambiguation scope (excluding commands where --workspace selects
// a destination, a creation target, or a listing filter).
func (s *session) hasTargetWorkspaceScope() bool {
	if s.inv == nil || s.inv.Command == nil || !s.inv.Changed("workspace") {
		return false
	}
	switch commandSubPath(s.inv.Command) {
	case "tab move", "tab open-image", "tab list",
		"agent open", "agent fork", "agent list", "agent open-tab",
		"terminal open", "terminal list",
		"file open", "workspace apply", "window create", "tree", "events":
		return false
	}
	return true
}

// addTargetWorkspaceFlag registers the optional --workspace target-scope flag
// on a tab, agent, or terminal command so bare titles and prefixes can be
// narrowed to one workspace.
func addTargetWorkspaceFlag(fs *pflag.FlagSet) {
	fs.String("workspace", "", `narrow target lookup to this workspace (an id, a prefix, "title:<title>" or "self")`)
}

// childrenTarget selects all direct child subagents of the calling agent on
// multi-agent commands.
const childrenTarget = "children"

// resolveChildAgents resolves the "children" relational selector to all direct
// child subagents of parentID on workerID.
func (s *session) resolveChildAgents(parentID, workerID string) ([]tabTarget, error) {
	if parentID == "" || workerID == "" {
		return nil, cli.Usagef(`"children" selects subagents of the calling agent, and this shell does not run inside a LeapMux agent tab`)
	}
	var resp leapmuxv1.ListAgentsResponse
	if err := callInnerRPC(s.ctx, s.c, workerID, leapmuxv1connect.WorkerAgentServiceListAgentsProcedure, &leapmuxv1.ListAgentsRequest{
		TabIds:             []string{parentID},
		IncludeDescendants: true,
	}, &resp); err != nil {
		return nil, err
	}
	var out []tabTarget
	for _, a := range resp.GetAgents() {
		if a.GetParentAgentId() == parentID {
			out = append(out, tabTarget{
				ID:       a.GetId(),
				Type:     leapmuxv1.TabType_TAB_TYPE_AGENT,
				WorkerID: workerID,
			})
		}
	}
	if len(out) == 0 {
		return nil, cli.Newf(cli.CodeNotFound, "agent %s has no child agents", parentID)
	}
	return out, nil
}

// resolveVariadicAgentTargets resolves a list of agent selectors in order,
// deduplicating by tab ID while preserving first-seen order. "children"
// (case-insensitive) expands to all direct child subagents of the calling
// agent. Workspace scope, ListTabs, and TabTitles are memoized across inputs
// so multi-target commands execute each discovery call at most once.
func (s *session) resolveVariadicAgentTargets(inputs []string, need resolve.Need) ([]tabTarget, error) {
	need.TabID = true
	need.WorkerID = true

	var workspaceIDs []string
	if s.hasTargetWorkspaceScope() {
		ws, err := s.resolveOne(resolve.KindWorkspace, stringFlag(s.inv, "workspace"))
		if err != nil {
			return nil, err
		}
		workspaceIDs = []string{ws.ID}
	}

	listing := s.listing()
	var (
		tabsLoaded     bool
		cachedTabs     []*leapmuxv1.WorkspaceTab
		titlesLoaded   bool
		cachedTitles   map[string]string
		childrenLoaded bool
		cachedChildren []tabTarget
	)
	listing.Tabs = func(ctx context.Context) ([]*leapmuxv1.WorkspaceTab, error) {
		if tabsLoaded {
			return cachedTabs, nil
		}
		tabs, err := listTabsInWorkspaces(ctx, s.c, workspaceIDs)
		if err != nil {
			return nil, err
		}
		cachedTabs = tabs
		tabsLoaded = true
		return tabs, nil
	}
	rawTitles := listing.TabTitles
	listing.TabTitles = func(ctx context.Context, tabs []*leapmuxv1.WorkspaceTab) map[string]string {
		if titlesLoaded {
			return cachedTitles
		}
		cachedTitles = rawTitles(ctx, tabs)
		titlesLoaded = true
		return cachedTitles
	}

	deps := s.deps()
	envWorkerID := strings.TrimSpace(os.Getenv(controlenv.WorkerIDVariable))
	needsPlacement := need.WorkspaceID || need.TileID || need.Want.WorkspaceID || need.Want.TileID

	seen := make(map[string]struct{}, len(inputs))
	targets := make([]tabTarget, 0, len(inputs))
	appendTarget := func(t tabTarget) {
		if _, dup := seen[t.ID]; dup {
			return
		}
		seen[t.ID] = struct{}{}
		targets = append(targets, t)
	}

	for _, input := range inputs {
		if strings.EqualFold(strings.TrimSpace(input), childrenTarget) {
			if !childrenLoaded {
				children, err := s.resolveChildAgents(listing.SelfTabID(resolve.KindAgent), envWorkerID)
				if err != nil {
					return nil, err
				}
				if needsPlacement {
					resolvedChildren := make([]tabTarget, 0, len(children))
					for _, child := range children {
						got, err := resolve.Resolve(s.ctx, deps, need, resolve.Inputs{
							TabID:         child.ID,
							ExplicitTabID: false,
							WorkerID:      child.WorkerID,
							FixedTabType:  leapmuxv1.TabType_TAB_TYPE_AGENT,
						})
						if err != nil {
							return nil, err
						}
						resolvedChildren = append(resolvedChildren, tabTarget{
							ID:          got.TabID,
							Type:        got.TabType,
							WorkerID:    got.WorkerID,
							WorkspaceID: got.WorkspaceID,
							TileID:      got.TileID,
							Row:         got.Tab,
						})
					}
					children = resolvedChildren
				}
				cachedChildren = children
				childrenLoaded = true
			}
			for _, child := range cachedChildren {
				appendTarget(child)
			}
			continue
		}
		if input == "" {
			input = resolve.SelfTarget
		}
		t, err := resolve.Canonical(s.ctx, listing, resolve.KindAgent, input)
		if err != nil {
			return nil, err
		}
		if t.Tab != nil {
			s.located[t.ID] = t.Tab
		}
		workerID := t.WorkerID
		if workerID == "" {
			workerID = envWorkerID
		}
		got, err := resolve.Resolve(s.ctx, deps, need, resolve.Inputs{
			TabID:         t.ID,
			ExplicitTabID: !t.Self && !t.Parent,
			WorkerID:      workerID,
			FixedTabType:  leapmuxv1.TabType_TAB_TYPE_AGENT,
			TabType:       t.TabType,
		})
		if err != nil {
			return nil, err
		}
		appendTarget(tabTarget{
			ID:          got.TabID,
			Type:        got.TabType,
			WorkerID:    got.WorkerID,
			WorkspaceID: got.WorkspaceID,
			TileID:      got.TileID,
			Self:        t.Self,
			Row:         got.Tab,
		})
	}
	return targets, nil
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

// explicitOrTabWorker resolves a worker from the explicit --worker flag,
// $LEAPMUX_CONTROL_WORKER_ID, or the worker hosting $LEAPMUX_CONTROL_TAB_ID.
// It returns ("", nil) when none of those sources identifies a worker.
func (s *session) explicitOrTabWorker() (string, error) {
	if s.inv != nil && s.inv.Command != nil && s.inv.Changed(workerFlag) {
		input := s.inv.Command.Flag(workerFlag).Value.String()
		if strings.TrimSpace(input) == "" {
			return "", cli.Usagef("--worker is empty")
		}
		t, err := s.resolveOne(resolve.KindWorker, input)
		if err != nil {
			return "", err
		}
		return t.ID, nil
	}
	if envWorkerID := strings.TrimSpace(os.Getenv(controlenv.WorkerIDVariable)); envWorkerID != "" {
		return envWorkerID, nil
	}
	if tabID := strings.TrimSpace(os.Getenv(controlenv.TabIDVariable)); tabID != "" {
		tab, err := s.deps().LocateTab(s.ctx, leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED, tabID)
		if err == nil && tab.GetWorkerId() != "" {
			return tab.GetWorkerId(), nil
		}
		if err != nil && !errors.Is(err, resolve.ErrTabNotLocatable) {
			return "", err
		}
	}
	return "", nil
}

// workerScope resolves the --worker flag of a command that runs on one
// worker. Resolution order when --worker is omitted:
//  1. $LEAPMUX_CONTROL_WORKER_ID (inside a LeapMux tab)
//  2. The Worker of $LEAPMUX_CONTROL_TAB_ID (when inside a tab without a worker id)
//  3. context.json's saved worker
//  4. The sole online Worker on the account (when outside a tab)
func (s *session) workerScope() (string, error) {
	workerID, err := s.explicitOrTabWorker()
	if err != nil || workerID != "" {
		return workerID, err
	}
	if ctxCfg, err := control.LoadContext(); err == nil && strings.TrimSpace(ctxCfg.Worker) != "" {
		t, err := s.resolveOne(resolve.KindWorker, strings.TrimSpace(ctxCfg.Worker))
		if err != nil {
			return "", err
		}
		return t.ID, nil
	}
	return s.autoSelectSoleWorker()
}

// autoSelectSoleWorker returns the only online Worker when outside a tab, or
// a clear usage error listing online Workers when 2+ are online.
func (s *session) autoSelectSoleWorker() (string, error) {
	workers, err := listAllWorkers(s.ctx, s.c)
	if err != nil {
		return "", cli.Usagef("no worker selected").
			WithHint("pass --worker <worker>, or run the command inside a LeapMux tab")
	}
	var online []*leapmuxv1.Worker
	for _, w := range workers {
		if w.GetOnline() {
			online = append(online, w)
		}
	}
	switch len(online) {
	case 1:
		return online[0].GetId(), nil
	case 0:
		return "", cli.Usagef("no worker selected").
			WithHint("pass --worker <worker>, or run the command inside a LeapMux tab")
	default:
		ids := make([]string, 0, len(online))
		for _, w := range online {
			ids = append(ids, w.GetId())
		}
		names := fetchWorkerNames(s.ctx, s.c, ids)
		labels := make([]string, 0, len(online))
		for _, w := range online {
			if n := names[w.GetId()]; n != "" {
				labels = append(labels, n+" ("+cli.ShortID(w.GetId())+")")
			} else {
				labels = append(labels, cli.ShortID(w.GetId()))
			}
		}
		return "", cli.Usagef("no worker selected (%d workers online: %s)", len(online), strings.Join(labels, ", ")).
			WithHint("pass --worker <worker>, or save a default with \"leapmux context use --worker <worker>\"")
	}
}

// archivedWorkspaceIDs returns the set of workspace IDs assigned to an
// archived sidebar section. If the Hub does not answer ListSections (for
// example, in minimal mock hubs), it returns an empty set.
func archivedWorkspaceIDs(ctx context.Context, c control.Peer) map[string]bool {
	var resp leapmuxv1.ListSectionsResponse
	if err := hubCallUnary(ctx, c, leapmuxv1connect.SectionServiceListSectionsProcedure, &leapmuxv1.ListSectionsRequest{}, &resp); err != nil {
		return nil
	}
	archivedSections := map[string]bool{}
	for _, sec := range resp.GetSections() {
		if sec.GetSectionType() == leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED {
			archivedSections[sec.GetId()] = true
		}
	}
	if len(archivedSections) == 0 {
		return nil
	}
	archived := map[string]bool{}
	for _, item := range resp.GetItems() {
		if archivedSections[item.GetSectionId()] {
			archived[item.GetWorkspaceId()] = true
		}
	}
	return archived
}

// defaultWorkspaceID resolves a default workspace outside a LeapMux tab:
// context.json's workspace, else the sole active (non-archived) workspace on
// the account. Returns "" if no unique default applies.
func (s *session) defaultWorkspaceID() (string, error) {
	if ctxCfg, err := control.LoadContext(); err == nil && ctxCfg.Workspace != "" {
		ws, err := s.resolveOne(resolve.KindWorkspace, ctxCfg.Workspace)
		if err != nil {
			return "", err
		}
		return ws.ID, nil
	}
	var resp leapmuxv1.ListWorkspacesResponse
	if err := hubCallUnary(s.ctx, s.c, leapmuxv1connect.WorkspaceServiceListWorkspacesProcedure, &leapmuxv1.ListWorkspacesRequest{}, &resp); err != nil {
		return "", nil
	}
	archived := archivedWorkspaceIDs(s.ctx, s.c)
	var active []*leapmuxv1.Workspace
	for _, w := range resp.GetWorkspaces() {
		if !archived[w.GetId()] {
			active = append(active, w)
		}
	}
	if len(active) == 1 {
		return active[0].GetId(), nil
	}
	return "", nil
}

// stringFlag returns the value of the flag name.
func stringFlag(inv *cli.Invocation, name string) string {
	return inv.Command.Flag(name).Value.String()
}

// boolFlag returns the value of the boolean flag name.
func boolFlag(inv *cli.Invocation, name string) bool {
	return inv.Command.Flag(name).Value.String() == "true"
}
