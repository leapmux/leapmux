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
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"strings"
	"sync"

	"connectrpc.com/connect/v2"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/sync/errgroup"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
)

// leapmuxStateClassifier maps LeapMux agent, terminal, worker, skill, and
// health state tokens to their table cell tones.
func leapmuxStateClassifier(value string) cli.StateTone {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "idle", "ready", "active", "online", "ok", "pass", "running", "completed", "up_to_date":
		return cli.StateToneGreen
	case "working", "starting", "pending", "warn", "queued", "dispatching", "outdated", "modified":
		return cli.StateToneYellow
	case "waiting_for_user":
		return cli.StateToneMagenta
	case "error", "exited", "startup_failed", "failed", "offline", "fail", "closed", "canceled", "unmanaged":
		return cli.StateToneRed
	case "not_installed":
		return cli.StateToneDim
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
	cli.SetStateClassifier(root, leapmuxStateClassifier)
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
	}
}

// NewCommand builds a standalone command tree containing all control commands.
// Unit tests in this package run commands against it directly.
func NewCommand() *cobra.Command {
	root := cli.Group("leapmux", "Control LeapMux from a script or from an agent")
	root.Long = `Control LeapMux from a script or from an agent.

A command acts on the object that its first argument gives. Use a full id, a
unique id prefix of at least 4 characters, or "id:<id>" for an id or id prefix.
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

// hubURLWithSource returns the hub address and its HubSource provenance for a
// command that talks to a hub directly, never through a tab's socket: an
// explicit --hub, else control.DefaultHubWithSource.
func hubURLWithSource(cmd *cobra.Command) (string, control.HubSource, error) {
	if hub := explicitHub(cmd); hub != "" {
		return hub, control.HubSourceFlag, nil
	}
	return control.DefaultHubWithSource()
}

// hubURLFor returns the hub of a command that always talks to a hub
// directly, never through a tab's socket: an explicit --hub, else the
// default hub (see control.DefaultHub).
func hubURLFor(cmd *cobra.Command) (string, error) {
	hub, _, err := hubURLWithSource(cmd)
	return hub, err
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

// configurePeer attaches invocation stderr and transport-owned error decoration
// to c.
func configurePeer(c control.Peer, inv *cli.Invocation) {
	switch peer := c.(type) {
	case *control.HubClient:
		attachErrOut(peer, inv)
		peer.SetErrorDecorator(decoratePeerError)
	case *control.WorkerIPCClient:
		peer.SetErrorDecorator(decoratePeerError)
	}
}

// noticeDiscoveredLocalHub writes a one-line stderr notice in text mode when a
// command connected to an auto-discovered local Hub IPC socket rather than an
// explicit --hub flag, LEAPMUX_HUB, a saved default, or a sole stored credential.
func noticeDiscoveredLocalHub(inv *cli.Invocation, c control.Peer) {
	if inv == nil || inv.Printer == nil || inv.Printer.Format != cli.FormatText {
		return
	}
	hub, ok := c.(*control.HubClient)
	if !ok || hub == nil || hub.Source != control.HubSourceDiscovered {
		return
	}
	_ = inv.Printer.Notice("using discovered local Hub %s (override with --hub or LEAPMUX_HUB)", hub.HubURL)
}

// noticeIgnoredHubEnv writes a one-line stderr notice in text mode when the
// CLI runs inside a LeapMux tab and connects via LEAPMUX_CONTROL_SOCK while
// LEAPMUX_HUB is also set in the environment.
func noticeIgnoredHubEnv(inv *cli.Invocation, c control.Peer) {
	if inv == nil || inv.Printer == nil || inv.Printer.Format != cli.FormatText {
		return
	}
	if _, ok := c.(*control.WorkerIPCClient); !ok {
		return
	}
	if strings.TrimSpace(os.Getenv("LEAPMUX_HUB")) == "" {
		return
	}
	_ = inv.Printer.Notice("connected via tab socket (ignoring LEAPMUX_HUB; pass --hub to override)")
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
	configurePeer(c, inv)
	noticeDiscoveredLocalHub(inv, c)
	noticeIgnoredHubEnv(inv, c)
	return c, nil
}

// locatedTabCache stores located WorkspaceTab rows and per-session memoized
// lookup data safely across goroutines and derived sessions (such as
// session.timed).
type locatedTabCache struct {
	mu   sync.Mutex
	byID map[string]*leapmuxv1.WorkspaceTab

	workerScopeCached bool
	workerScopeKey    string
	workerScopeID     string
	workerScopeErr    error

	allTabsLoaded     bool
	allTabs           []*leapmuxv1.WorkspaceTab
	tabDetails        map[string]tabDetail
	tabDetailsQueried map[string]bool

	allWorkspacesLoaded bool
	allWorkspaces       []*leapmuxv1.Workspace

	allWorkersLoaded bool
	allWorkers       []*leapmuxv1.Worker
}

func newLocatedTabCache() *locatedTabCache {
	return &locatedTabCache{
		byID:              map[string]*leapmuxv1.WorkspaceTab{},
		tabDetails:        map[string]tabDetail{},
		tabDetailsQueried: map[string]bool{},
	}
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
	located *locatedTabCache
}

func (s *session) ensureLocatedCache() *locatedTabCache {
	if s.located == nil {
		s.located = newLocatedTabCache()
	}
	return s.located
}

func (s *session) cacheLocatedTab(tabID string, row *leapmuxv1.WorkspaceTab) {
	if tabID == "" || row == nil {
		return
	}
	cache := s.ensureLocatedCache()
	cache.mu.Lock()
	if cache.byID == nil {
		cache.byID = map[string]*leapmuxv1.WorkspaceTab{}
	}
	cache.byID[tabID] = row
	cache.mu.Unlock()
}

func (s *session) cacheLocatedTabs(tabs []*leapmuxv1.WorkspaceTab) {
	if len(tabs) == 0 {
		return
	}
	cache := s.ensureLocatedCache()
	cache.mu.Lock()
	if cache.byID == nil {
		cache.byID = map[string]*leapmuxv1.WorkspaceTab{}
	}
	for _, t := range tabs {
		if id := t.GetTab().GetTabId(); id != "" && t != nil {
			cache.byID[id] = t
		}
	}
	cache.mu.Unlock()
}

func (s *session) lookupLocatedTab(tabType leapmuxv1.TabType, tabID string) (*leapmuxv1.WorkspaceTab, bool) {
	if s.located == nil || tabID == "" {
		return nil, false
	}
	s.located.mu.Lock()
	row, ok := s.located.byID[tabID]
	s.located.mu.Unlock()
	if !ok || row == nil {
		return nil, false
	}
	if tabType != leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED && row.GetTab().GetTabType() != tabType {
		return nil, false
	}
	return row, true
}

// invalidateLocatedTab removes tabID from the session's LocateTab and detail
// caches after a mutation (such as closing or moving a tab) changes its state.
func (s *session) invalidateLocatedTab(tabID string) {
	if s.located == nil || tabID == "" {
		return
	}
	s.located.mu.Lock()
	delete(s.located.byID, tabID)
	s.located.allTabsLoaded = false
	s.located.allTabs = nil
	delete(s.located.tabDetails, tabID)
	delete(s.located.tabDetailsQueried, tabID)
	s.located.mu.Unlock()
}

// listAllTabs lists every visible tab across all workspaces, memoizing the
// result on the session so target resolution and uniqueness checks share one
// ListTabs RPC.
func (s *session) listAllTabs(ctx context.Context) ([]*leapmuxv1.WorkspaceTab, error) {
	cache := s.ensureLocatedCache()
	cache.mu.Lock()
	if cache.allTabsLoaded {
		tabs := cache.allTabs
		cache.mu.Unlock()
		return tabs, nil
	}
	cache.mu.Unlock()

	tabs, err := listTabsInWorkspaces(ctx, s.c, nil)
	if err != nil {
		return nil, err
	}
	cache.mu.Lock()
	cache.allTabs = tabs
	cache.allTabsLoaded = true
	if cache.byID == nil {
		cache.byID = map[string]*leapmuxv1.WorkspaceTab{}
	}
	for _, t := range tabs {
		if id := t.GetTab().GetTabId(); id != "" && t != nil {
			cache.byID[id] = t
		}
	}
	cache.mu.Unlock()
	return tabs, nil
}

// listTabsByIDPrefix filters the session's cached full tab list when already
// populated; otherwise it runs a server-side id_prefix query without
// overwriting the full-list cache.
func (s *session) listTabsByIDPrefix(ctx context.Context, workspaceIDs []string, idPrefix string) ([]*leapmuxv1.WorkspaceTab, error) {
	cache := s.ensureLocatedCache()
	cache.mu.Lock()
	if cache.allTabsLoaded {
		tabs := filterTabsByWorkspaceAndIDPrefix(cache.allTabs, workspaceIDs, idPrefix)
		cache.mu.Unlock()
		return tabs, nil
	}
	cache.mu.Unlock()

	tabs, err := listTabsInWorkspacesWithPrefix(ctx, s.c, workspaceIDs, idPrefix)
	if err != nil {
		return nil, err
	}
	s.cacheLocatedTabs(tabs)
	return tabs, nil
}

func filterTabsByWorkspaceAndIDPrefix(tabs []*leapmuxv1.WorkspaceTab, workspaceIDs []string, idPrefix string) []*leapmuxv1.WorkspaceTab {
	out := make([]*leapmuxv1.WorkspaceTab, 0)
	for _, t := range tabs {
		if idPrefix != "" && !strings.HasPrefix(t.GetTab().GetTabId(), idPrefix) {
			continue
		}
		if len(workspaceIDs) > 0 && !slices.Contains(workspaceIDs, t.GetWorkspaceId()) {
			continue
		}
		out = append(out, t)
	}
	return out
}

// listAllWorkspaces lists every readable workspace, memoizing the full list on
// the session.
func (s *session) listAllWorkspaces(ctx context.Context) ([]*leapmuxv1.Workspace, error) {
	cache := s.ensureLocatedCache()
	cache.mu.Lock()
	if cache.allWorkspacesLoaded {
		workspaces := cache.allWorkspaces
		cache.mu.Unlock()
		return workspaces, nil
	}
	cache.mu.Unlock()

	workspaces, err := listWorkspacesWithPrefix(ctx, s.c, "")
	if err != nil {
		return nil, err
	}
	cache.mu.Lock()
	cache.allWorkspaces = workspaces
	cache.allWorkspacesLoaded = true
	cache.mu.Unlock()
	return workspaces, nil
}

// listWorkspacesByIDPrefix filters the session's cached full workspace list
// when already populated; otherwise it runs a server-side id_prefix query
// without overwriting the full-list cache.
func (s *session) listWorkspacesByIDPrefix(ctx context.Context, idPrefix string) ([]*leapmuxv1.Workspace, error) {
	cache := s.ensureLocatedCache()
	cache.mu.Lock()
	if cache.allWorkspacesLoaded {
		workspaces := filterWorkspacesByIDPrefix(cache.allWorkspaces, idPrefix)
		cache.mu.Unlock()
		return workspaces, nil
	}
	cache.mu.Unlock()

	return listWorkspacesWithPrefix(ctx, s.c, idPrefix)
}

func filterWorkspacesByIDPrefix(workspaces []*leapmuxv1.Workspace, idPrefix string) []*leapmuxv1.Workspace {
	out := make([]*leapmuxv1.Workspace, 0)
	for _, w := range workspaces {
		if idPrefix == "" || strings.HasPrefix(w.GetId(), idPrefix) {
			out = append(out, w)
		}
	}
	return out
}

// listAllWorkersCached lists every Worker on the account, memoizing the full
// list on the session.
func (s *session) listAllWorkersCached(ctx context.Context) ([]*leapmuxv1.Worker, error) {
	cache := s.ensureLocatedCache()
	cache.mu.Lock()
	if cache.allWorkersLoaded {
		workers := cache.allWorkers
		cache.mu.Unlock()
		return workers, nil
	}
	cache.mu.Unlock()

	workers, err := listAllWorkers(ctx, s.c)
	if err != nil {
		return nil, err
	}
	cache.mu.Lock()
	cache.allWorkers = workers
	cache.allWorkersLoaded = true
	cache.mu.Unlock()
	return workers, nil
}

// listWorkersByIDPrefixCached filters the session's cached full Worker list
// when already populated; otherwise it runs a single-page server-side
// id_prefix query without overwriting the full-list cache.
func (s *session) listWorkersByIDPrefixCached(ctx context.Context, idPrefix string) ([]*leapmuxv1.Worker, error) {
	cache := s.ensureLocatedCache()
	cache.mu.Lock()
	if cache.allWorkersLoaded {
		workers := filterWorkersByIDPrefix(cache.allWorkers, idPrefix)
		cache.mu.Unlock()
		return workers, nil
	}
	cache.mu.Unlock()

	return listWorkersByIDPrefix(ctx, s.c, idPrefix)
}

func filterWorkersByIDPrefix(workers []*leapmuxv1.Worker, idPrefix string) []*leapmuxv1.Worker {
	out := make([]*leapmuxv1.Worker, 0)
	for _, w := range workers {
		if idPrefix == "" || strings.HasPrefix(w.GetId(), idPrefix) {
			out = append(out, w)
		}
	}
	return out
}

// fetchTabDetailsCached returns Worker-reported details for tabs, fetching
// only the tabs that this session has not already queried.
func (s *session) fetchTabDetailsCached(ctx context.Context, tabs []*leapmuxv1.WorkspaceTab) map[string]tabDetail {
	if len(tabs) == 0 {
		return map[string]tabDetail{}
	}
	cache := s.ensureLocatedCache()
	var missing []*leapmuxv1.WorkspaceTab
	cache.mu.Lock()
	for _, t := range tabs {
		id := t.GetTab().GetTabId()
		if id == "" || cache.tabDetailsQueried[id] {
			continue
		}
		missing = append(missing, t)
	}
	cache.mu.Unlock()

	if len(missing) > 0 {
		fetched := fetchTabDetails(ctx, s.c, missing)
		cache.mu.Lock()
		if cache.tabDetails == nil {
			cache.tabDetails = map[string]tabDetail{}
		}
		if cache.tabDetailsQueried == nil {
			cache.tabDetailsQueried = map[string]bool{}
		}
		for _, t := range missing {
			if id := t.GetTab().GetTabId(); id != "" {
				cache.tabDetailsQueried[id] = true
			}
		}
		for id, d := range fetched {
			cache.tabDetails[id] = d
		}
		cache.mu.Unlock()
	}

	out := make(map[string]tabDetail, len(tabs))
	cache.mu.Lock()
	for _, t := range tabs {
		id := t.GetTab().GetTabId()
		if d, ok := cache.tabDetails[id]; ok {
			out[id] = d
		}
	}
	cache.mu.Unlock()
	return out
}

func openSessionWith(inv *cli.Invocation, limit func(context.Context) (context.Context, context.CancelFunc)) (*session, error) {
	c, err := newClient(inv)
	if err != nil {
		return nil, err
	}
	ctx, cancel := limit(inv.Context)
	return &session{inv: inv, c: c, ctx: ctx, cancel: cancel, located: newLocatedTabCache()}, nil
}

// openSession builds the client and a context limited by rpcDeadline.
func openSession(inv *cli.Invocation) (*session, error) {
	return openSessionWith(inv, rpcDeadline)
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
		located: newLocatedTabCache(),
	}
}

// openStreamSession builds the client and a context that only the process
// ends, for a command that streams until the user stops it.
func openStreamSession(inv *cli.Invocation) (*session, error) {
	return openSessionWith(inv, context.WithCancel)
}

// openWaitableSession opens a session pair for a command with a --wait mode.
// When wait is true, stream runs without an RPC deadline and call carries
// rpcDeadline for setup RPCs; when wait is false, both refer to one
// deadline-limited session. closeAll releases every opened context.
func openWaitableSession(inv *cli.Invocation, wait bool) (stream, call *session, closeAll func(), err error) {
	if !wait {
		s, err := openSession(inv)
		if err != nil {
			return nil, nil, nil, err
		}
		return s, s, s.Close, nil
	}
	stream, err = openStreamSession(inv)
	if err != nil {
		return nil, nil, nil, err
	}
	call = stream.timed()
	return stream, call, func() {
		call.Close()
		stream.Close()
	}, nil
}

// Close releases the session's context.
func (s *session) Close() { s.cancel() }

// ensureWorkspaceExists confirms that workspaceID exists on the Hub and is
// readable by the caller.
func (s *session) ensureWorkspaceExists(workspaceID string) error {
	var resp leapmuxv1.GetWorkspaceResponse
	if err := hubCallUnary(s.ctx, s.c, leapmuxv1connect.WorkspaceServiceGetWorkspaceProcedure, &leapmuxv1.GetWorkspaceRequest{WorkspaceId: workspaceID}, &resp); err != nil {
		return fmt.Errorf("get workspace %s: %w", workspaceID, err)
	}
	return nil
}

// resolveExistingWorkspace resolves input to a workspace ID and confirms its
// existence on the Hub when input bypassed ListWorkspaces (a full ID or
// "id:<id>").
func (s *session) resolveExistingWorkspace(input string) (resolve.Target, error) {
	ws, err := s.resolveOne(resolve.KindWorkspace, input)
	if err != nil {
		return resolve.Target{}, err
	}
	if resolve.ExplicitID(resolve.KindWorkspace, input) != "" {
		if err := s.ensureWorkspaceExists(ws.ID); err != nil {
			return resolve.Target{}, err
		}
	}
	return ws, nil
}

// listWorkspaceTabs lists tabs in the workspace selected by --workspace (when
// set) or across every readable workspace, filtered by tabType (or all types
// when TAB_TYPE_UNSPECIFIED).
func (s *session) listWorkspaceTabs(tabType leapmuxv1.TabType) ([]*leapmuxv1.WorkspaceTab, error) {
	var workspaceIDs []string
	if s.inv.Changed("workspace") {
		ws, err := s.resolveExistingWorkspace(stringFlag(s.inv, "workspace"))
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

// timed returns a session that shares this one's client and cache, with a
// context that rpcDeadline limits. A streaming command resolves its target
// through it, so the resolution cannot hang while the stream itself runs
// until the user stops it. The caller closes the returned session.
func (s *session) timed() *session {
	ctx, cancel := rpcDeadline(s.ctx)
	return &session{inv: s.inv, c: s.c, ctx: ctx, cancel: cancel, located: s.ensureLocatedCache()}
}

// deps returns the resolver's dependencies, with LocateTab answered from the
// session's cache first.
func (s *session) deps() resolve.Deps {
	deps := resolveDeps(s.c)
	locate := deps.LocateTab
	deps.LocateTab = func(ctx context.Context, tabType leapmuxv1.TabType, tabID string) (*leapmuxv1.WorkspaceTab, error) {
		if row, ok := s.lookupLocatedTab(tabType, tabID); ok {
			return row, nil
		}
		row, err := locate(ctx, tabType, tabID)
		if err == nil && row != nil {
			s.cacheLocatedTab(tabID, row)
		}
		return row, err
	}
	return deps
}

// listing returns what resolve.Canonical needs to resolve a short form,
// memoizing full-account listings and Worker tab details on the session while
// routing id-prefix queries to server-side id_prefix RPCs.
func (s *session) listing() resolve.Listing {
	l := targetListing(s.c, s.deps())
	l.Tabs = s.listAllTabs
	l.TabsByIDPrefix = func(ctx context.Context, idPrefix string) ([]*leapmuxv1.WorkspaceTab, error) {
		return s.listTabsByIDPrefix(ctx, nil, idPrefix)
	}
	l.Workspaces = s.listAllWorkspaces
	l.WorkspacesByIDPrefix = s.listWorkspacesByIDPrefix
	l.Workers = s.listAllWorkersCached
	l.WorkersByIDPrefix = s.listWorkersByIDPrefixCached
	l.TabTitles = func(ctx context.Context, tabs []*leapmuxv1.WorkspaceTab) map[string]string {
		details := s.fetchTabDetailsCached(ctx, tabs)
		titles := make(map[string]string, len(details))
		for id, d := range details {
			titles[id] = d.Title
		}
		return titles
	}
	return l
}

// selfTabID returns the calling tab's ID from LEAPMUX_CONTROL_TAB_ID, falling
// back to the local Worker IPC Whoami RPC when the env var is absent.
func (s *session) selfTabID() string {
	if id := callingTabID(); id != "" {
		return id
	}
	if selfIdentity := s.listing().SelfIdentity; selfIdentity != nil {
		if info, err := selfIdentity(s.ctx); err == nil {
			return strings.TrimSpace(info.TabID)
		}
	}
	return ""
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

// verifyTabInWorkspace ensures a tab resolved under a --workspace target scope
// belongs to scopedWS.
func verifyTabInWorkspace(tabID, actualWS, scopedWS string) error {
	if scopedWS == "" || actualWS == scopedWS {
		return nil
	}
	if actualWS == "" {
		return cli.Newf(cli.CodeNotFound, "tab %s is not in workspace %s", tabID, scopedWS)
	}
	return cli.Newf(cli.CodeNotFound, "tab %s is in workspace %s, not %s", tabID, actualWS, scopedWS)
}

// resolveTabInputs resolves inputs through resolve.Resolve. When parent is
// true, the calling Worker is not pinned up front so Hub LocateTab can locate
// a cross-Worker spawner (spawned_by_agent_id); if the Hub holds no placement
// (an unopened virtual subagent on the calling Worker), it retries with
// fallbackWorkerID.
func resolveTabInputs(ctx context.Context, deps resolve.Deps, need resolve.Need, in resolve.Inputs, parent bool, fallbackWorkerID string) (resolve.Resolved, error) {
	if parent {
		in.WorkerID = ""
	}
	got, err := resolve.Resolve(ctx, deps, need, in)
	if err != nil && parent && fallbackWorkerID != "" && errors.Is(err, resolve.ErrTabNotLocatable) {
		in.WorkerID = fallbackWorkerID
		return resolve.Resolve(ctx, deps, need, in)
	}
	return got, err
}

// resolveTab resolves the tab target input of kind and derives what need
// asks for. An empty input selects the caller's own tab. The command parser
// rejects explicit blank targets, so empty input here means an omitted target.
//
// When kind accepts an agent and input is "parent", it resolves the calling
// agent's parent_agent_id or spawned_by_agent_id.
//
// When the command declares a scoping --workspace flag (not a destination
// flag like `tab move --workspace`), tab listings are narrowed to that
// workspace and every resolved target — including explicit IDs, "self", and
// "parent" — is verified to belong to that workspace.
func (s *session) resolveTab(kind resolve.Kind, input string, need resolve.Need) (tabTarget, error) {
	if input == "" {
		input = resolve.SelfTarget
	}
	listing := s.listing()
	var scopedWS string
	if s.hasTargetWorkspaceScope() {
		ws, err := s.resolveExistingWorkspace(stringFlag(s.inv, "workspace"))
		if err != nil {
			return tabTarget{}, err
		}
		scopedWS = ws.ID
		listing.Tabs = func(ctx context.Context) ([]*leapmuxv1.WorkspaceTab, error) {
			tabs, err := listTabsInWorkspaces(ctx, s.c, []string{ws.ID})
			if err != nil {
				return nil, err
			}
			s.cacheLocatedTabs(tabs)
			return tabs, nil
		}
		listing.TabsByIDPrefix = func(ctx context.Context, idPrefix string) ([]*leapmuxv1.WorkspaceTab, error) {
			return s.listTabsByIDPrefix(ctx, []string{ws.ID}, idPrefix)
		}
	}
	t, err := resolve.Canonical(s.ctx, listing, kind, input)
	if err != nil {
		return tabTarget{}, err
	}
	if t.Tab != nil {
		s.cacheLocatedTab(t.ID, t.Tab)
	}
	need.TabID = true
	if scopedWS != "" {
		need.Want.WorkspaceID = true
	}
	fixedType := kind.TabType()
	if t.Parent && fixedType == leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED {
		fixedType = leapmuxv1.TabType_TAB_TYPE_AGENT
	}
	workerID := t.WorkerID
	if workerID == "" {
		workerID = strings.TrimSpace(os.Getenv(controlenv.WorkerIDVariable))
	}
	got, err := resolveTabInputs(s.ctx, s.deps(), need, resolve.Inputs{
		TabID:         t.ID,
		ExplicitTabID: !t.Self && !t.Parent,
		WorkerID:      workerID,
		FixedTabType:  fixedType,
		TabType:       t.TabType,
	}, t.Parent, workerID)
	if err != nil {
		return tabTarget{}, err
	}
	if err := verifyTabInWorkspace(got.TabID, got.WorkspaceID, scopedWS); err != nil {
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

// targetWorkspaceFlagAnnotation marks --workspace flags that narrow target
// lookup, distinguishing them from destination, creation, or listing filters.
const targetWorkspaceFlagAnnotation = "leapmux_target_workspace_scope"

// hasTargetWorkspaceScope reports whether the command supplied a target-scoping
// --workspace flag (annotated by addTargetWorkspaceFlag or
// addTargetWorkspaceFlagWithUsage).
func (s *session) hasTargetWorkspaceScope() bool {
	if s.inv == nil || s.inv.Command == nil || !s.inv.Changed("workspace") {
		return false
	}
	flag := s.inv.Command.Flag("workspace")
	if flag == nil {
		return false
	}
	vals := flag.Annotations[targetWorkspaceFlagAnnotation]
	return len(vals) > 0 && vals[0] == "true"
}

// addTargetWorkspaceFlag registers the optional --workspace target-scope flag
// on a tab, agent, or terminal command so bare titles and prefixes can be
// narrowed to one workspace.
func addTargetWorkspaceFlag(fs *pflag.FlagSet) {
	addTargetWorkspaceFlagWithUsage(fs, `narrow target lookup to this workspace (an id, a prefix, "title:<title>" or "self")`)
}

// addTargetWorkspaceFlagWithUsage registers a target-scoping --workspace flag
// with custom help text and marks it for hasTargetWorkspaceScope.
func addTargetWorkspaceFlagWithUsage(fs *pflag.FlagSet, usage string) {
	fs.String("workspace", "", usage)
	_ = fs.SetAnnotation("workspace", targetWorkspaceFlagAnnotation, []string{"true"})
}

// childrenTarget selects all direct child subagents of the calling agent on
// multi-agent commands.
const childrenTarget = "children"

// resolveSelfAgentIdentity resolves the calling agent's (tabID, workerID) from
// environment variables, falling back to local Worker IPC Whoami when absent.
func (s *session) resolveSelfAgentIdentity(listing resolve.Listing) (tabID, workerID string, err error) {
	tabID = listing.SelfTabID(resolve.KindAgent)
	workerID = strings.TrimSpace(os.Getenv(controlenv.WorkerIDVariable))
	envTabID := strings.TrimSpace(os.Getenv(controlenv.TabIDVariable))
	envTabType, _ := resolve.ParseTabType(os.Getenv(controlenv.TabTypeVariable))
	explicitMismatch := envTabID != "" && envTabType != leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED && envTabType != leapmuxv1.TabType_TAB_TYPE_AGENT
	if !explicitMismatch && (tabID == "" || workerID == "") && listing.SelfIdentity != nil {
		info, idErr := listing.SelfIdentity(s.ctx)
		if idErr != nil {
			return "", "", idErr
		}
		info.TabID = strings.TrimSpace(info.TabID)
		info.WorkerID = strings.TrimSpace(info.WorkerID)
		if info.TabID != "" && info.TabType == leapmuxv1.TabType_TAB_TYPE_AGENT {
			if tabID == "" {
				tabID = info.TabID
			}
			if workerID == "" {
				workerID = info.WorkerID
			}
		}
	}
	return tabID, workerID, nil
}

// resolveChildAgents resolves the "children" relational selector to the
// deduplicated union of direct virtual child subagents (parent_agent_id) on
// the calling Worker and open spawned peer agents (spawned_by_agent_id) across
// online Workers.
func (s *session) resolveChildAgents(listing resolve.Listing) ([]tabTarget, error) {
	parentID, workerID, err := s.resolveSelfAgentIdentity(listing)
	if err != nil {
		return nil, err
	}
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
	seen := map[string]bool{}
	var out []tabTarget
	for _, a := range resp.GetAgents() {
		if a.GetParentAgentId() == parentID && !seen[a.GetId()] {
			seen[a.GetId()] = true
			out = append(out, tabTarget{
				ID:       a.GetId(),
				Type:     leapmuxv1.TabType_TAB_TYPE_AGENT,
				WorkerID: workerID,
			})
		}
	}

	tabs, tabsErr := s.listAllTabs(s.ctx)
	if tabsErr != nil {
		code := connect.CodeOf(tabsErr)
		if code != connect.CodePermissionDenied && code != connect.CodeUnimplemented {
			return nil, tabsErr
		}
	} else {
		agentTabs := filterTabsByType(tabs, leapmuxv1.TabType_TAB_TYPE_AGENT)
		details := s.fetchTabDetailsCached(s.ctx, agentTabs)
		var spawned []tabTarget
		for _, t := range agentTabs {
			id := t.GetTab().GetTabId()
			if id == "" || id == parentID || seen[id] {
				continue
			}
			if d := details[id]; d.Agent != nil && d.Agent.GetSpawnedByAgentId() == parentID {
				seen[id] = true
				spawned = append(spawned, tabTarget{
					ID:          id,
					Type:        leapmuxv1.TabType_TAB_TYPE_AGENT,
					WorkerID:    t.GetWorkerId(),
					WorkspaceID: t.GetWorkspaceId(),
					TileID:      t.GetTileId(),
					Row:         t,
				})
			}
		}
		slices.SortFunc(spawned, func(a, b tabTarget) int {
			return strings.Compare(a.ID, b.ID)
		})
		out = append(out, spawned...)
	}

	if len(out) == 0 {
		return nil, cli.Newf(cli.CodeNotFound, "agent %s has no child agents", parentID)
	}
	return out, nil
}

// variadicTabOptions tunes edge-case handling in resolveVariadicTabsWithOptions.
type variadicTabOptions struct {
	// SkipUnopenedChildren drops child subagents expanded from "children" that
	// have no CRDT tab placement on the Hub instead of failing resolution.
	SkipUnopenedChildren bool
}

// resolveVariadicTabs resolves a list of tab selectors of kind in order,
// deduplicating by tab ID while preserving first-seen order. When kind accepts
// an agent (KindAgent or KindTab), "children" (case-insensitive) expands to
// all direct child subagents of the calling agent. Workspace scope, ListTabs,
// and TabTitles are memoized across inputs, and uncached LocateTab calls run
// concurrently.
func (s *session) resolveVariadicTabs(kind resolve.Kind, inputs []string, need resolve.Need) ([]tabTarget, error) {
	return s.resolveVariadicTabsWithOptions(kind, inputs, need, variadicTabOptions{})
}

func (s *session) resolveVariadicTabsWithOptions(kind resolve.Kind, inputs []string, need resolve.Need, opts variadicTabOptions) ([]tabTarget, error) {
	need.TabID = true

	var (
		scopedWS     string
		workspaceIDs []string
	)
	if s.hasTargetWorkspaceScope() {
		ws, err := s.resolveExistingWorkspace(stringFlag(s.inv, "workspace"))
		if err != nil {
			return nil, err
		}
		scopedWS = ws.ID
		workspaceIDs = []string{ws.ID}
		need.Want.WorkspaceID = true
	}

	listing := s.listing()
	allowChildren := kind == resolve.KindAgent || kind == resolve.KindTab
	distinctListable := make(map[string]struct{}, len(inputs))
	for _, raw := range inputs {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || trimmed == resolve.SelfTarget || resolve.ExplicitID(kind, trimmed) != "" {
			continue
		}
		if allowChildren && (strings.EqualFold(trimmed, resolve.ParentTarget) || strings.EqualFold(trimmed, childrenTarget)) {
			continue
		}
		distinctListable[trimmed] = struct{}{}
	}
	var (
		tabsLoaded     bool
		cachedTabs     []*leapmuxv1.WorkspaceTab
		prefixCache    map[string][]*leapmuxv1.WorkspaceTab
		titlesLoaded   bool
		cachedTitles   map[string]string
		childrenLoaded bool
		cachedChildren []tabTarget
	)
	listing.Tabs = func(ctx context.Context) ([]*leapmuxv1.WorkspaceTab, error) {
		if tabsLoaded {
			return cachedTabs, nil
		}
		var (
			tabs []*leapmuxv1.WorkspaceTab
			err  error
		)
		if len(workspaceIDs) == 0 {
			tabs, err = s.listAllTabs(ctx)
		} else {
			tabs, err = listTabsInWorkspaces(ctx, s.c, workspaceIDs)
			if err == nil {
				s.cacheLocatedTabs(tabs)
			}
		}
		if err != nil {
			return nil, err
		}
		cachedTabs = tabs
		tabsLoaded = true
		return tabs, nil
	}
	if len(distinctListable) > 1 {
		// Multiple distinct targets share one cached ListTabs call instead of
		// issuing N sequential server-side id_prefix round trips.
		listing.TabsByIDPrefix = nil
	} else {
		listing.TabsByIDPrefix = func(ctx context.Context, idPrefix string) ([]*leapmuxv1.WorkspaceTab, error) {
			if tabsLoaded {
				return filterTabsByWorkspaceAndIDPrefix(cachedTabs, nil, idPrefix), nil
			}
			if cached, ok := prefixCache[idPrefix]; ok {
				return cached, nil
			}
			tabs, err := s.listTabsByIDPrefix(ctx, workspaceIDs, idPrefix)
			if err != nil {
				return nil, err
			}
			if prefixCache == nil {
				prefixCache = map[string][]*leapmuxv1.WorkspaceTab{}
			}
			prefixCache[idPrefix] = tabs
			return tabs, nil
		}
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

	type pendingTarget struct {
		tabID         string
		explicitTabID bool
		workerID      string
		fixedTabType  leapmuxv1.TabType
		tabType       leapmuxv1.TabType
		self          bool
		parent        bool
		fromChildren  bool
	}

	envWorkerID := strings.TrimSpace(os.Getenv(controlenv.WorkerIDVariable))
	seen := make(map[string]struct{}, len(inputs))
	pending := make([]pendingTarget, 0, len(inputs))
	addPending := func(p pendingTarget) {
		if _, dup := seen[p.tabID]; dup {
			return
		}
		seen[p.tabID] = struct{}{}
		pending = append(pending, p)
	}

	for _, input := range inputs {
		if allowChildren && strings.EqualFold(strings.TrimSpace(input), childrenTarget) {
			if !childrenLoaded {
				children, err := s.resolveChildAgents(listing)
				if err != nil {
					return nil, err
				}
				cachedChildren = children
				childrenLoaded = true
			}
			for _, child := range cachedChildren {
				addPending(pendingTarget{
					tabID:         child.ID,
					explicitTabID: false,
					workerID:      child.WorkerID,
					fixedTabType:  leapmuxv1.TabType_TAB_TYPE_AGENT,
					tabType:       leapmuxv1.TabType_TAB_TYPE_AGENT,
					fromChildren:  true,
				})
			}
			continue
		}
		if input == "" {
			input = resolve.SelfTarget
		}
		t, err := resolve.Canonical(s.ctx, listing, kind, input)
		if err != nil {
			return nil, err
		}
		if t.Tab != nil {
			s.cacheLocatedTab(t.ID, t.Tab)
		}
		fixedType := kind.TabType()
		if t.Parent && fixedType == leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED {
			fixedType = leapmuxv1.TabType_TAB_TYPE_AGENT
		}
		workerID := t.WorkerID
		if workerID == "" {
			workerID = envWorkerID
		}
		addPending(pendingTarget{
			tabID:         t.ID,
			explicitTabID: !t.Self && !t.Parent,
			workerID:      workerID,
			fixedTabType:  fixedType,
			tabType:       t.TabType,
			self:          t.Self,
			parent:        t.Parent,
		})
	}

	deps := s.deps()
	resolved := make([]tabTarget, len(pending))
	skipped := make([]bool, len(pending))
	g, gctx := errgroup.WithContext(s.ctx)
	g.SetLimit(detailFanoutLimit)
	for i, p := range pending {
		g.Go(func() error {
			got, err := resolveTabInputs(gctx, deps, need, resolve.Inputs{
				TabID:         p.tabID,
				ExplicitTabID: p.explicitTabID,
				WorkerID:      p.workerID,
				FixedTabType:  p.fixedTabType,
				TabType:       p.tabType,
			}, p.parent, p.workerID)
			if err != nil {
				if p.fromChildren && opts.SkipUnopenedChildren && errors.Is(err, resolve.ErrTabNotLocatable) {
					skipped[i] = true
					return nil
				}
				return err
			}
			if err := verifyTabInWorkspace(got.TabID, got.WorkspaceID, scopedWS); err != nil {
				return err
			}
			resolved[i] = tabTarget{
				ID:          got.TabID,
				Type:        got.TabType,
				WorkerID:    got.WorkerID,
				WorkspaceID: got.WorkspaceID,
				TileID:      got.TileID,
				Self:        p.self,
				Row:         got.Tab,
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

	targets := make([]tabTarget, 0, len(resolved))
	for i, t := range resolved {
		if !skipped[i] {
			targets = append(targets, t)
		}
	}
	return targets, nil
}

// resolveOne resolves a target of a kind that is not a tab: a tile, a
// workspace or a worker. An empty input selects the caller's own.
func (s *session) resolveOne(kind resolve.Kind, input string) (resolve.Target, error) {
	if input == "" {
		input = resolve.SelfTarget
	}
	t, err := resolve.Canonical(s.ctx, s.listing(), kind, input)
	if err != nil {
		return resolve.Target{}, err
	}
	if t.Tab != nil {
		s.cacheLocatedTab(t.ID, t.Tab)
	}
	return t, nil
}

// resolveWorkspaceOrDefault resolves an optional workspace selector. When
// input is omitted outside a tab, it falls back to defaultWorkspaceID; inside
// a tab it resolves the calling tab's workspace. Explicit full IDs and "id:"
// selectors are verified to exist on the Hub.
func (s *session) resolveWorkspaceOrDefault(input string) (resolve.Target, error) {
	if strings.TrimSpace(input) == "" && s.selfTabID() == "" {
		def, err := s.defaultWorkspaceID()
		if err != nil {
			return resolve.Target{}, err
		}
		if def != "" {
			return resolve.Target{ID: def}, nil
		}
	}
	return s.resolveExistingWorkspace(input)
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

func (s *session) workerScopeCacheKey() string {
	if s.inv != nil && s.inv.Command != nil && s.inv.Changed(workerFlag) {
		return "flag:" + s.inv.Command.Flag(workerFlag).Value.String()
	}
	return "env:" + strings.TrimSpace(os.Getenv(controlenv.WorkerIDVariable)) +
		"|tab:" + strings.TrimSpace(os.Getenv(controlenv.TabIDVariable))
}

// workerScope resolves the --worker flag of a command that runs on one
// worker. Resolution order when --worker is omitted:
//  1. $LEAPMUX_CONTROL_WORKER_ID (inside a LeapMux tab)
//  2. The Worker of $LEAPMUX_CONTROL_TAB_ID (when inside a tab without a worker id)
//  3. context.json's saved worker
//  4. The sole online Worker on the account (when outside a tab)
func (s *session) workerScope() (string, error) {
	key := s.workerScopeCacheKey()
	cache := s.ensureLocatedCache()
	cache.mu.Lock()
	if cache.workerScopeCached && cache.workerScopeKey == key {
		id, err := cache.workerScopeID, cache.workerScopeErr
		cache.mu.Unlock()
		return id, err
	}
	cache.mu.Unlock()

	id, err := s.resolveWorkerScope()
	if s.ctx == nil || s.ctx.Err() == nil {
		cache.mu.Lock()
		cache.workerScopeCached = true
		cache.workerScopeKey = key
		cache.workerScopeID = id
		cache.workerScopeErr = err
		cache.mu.Unlock()
	}
	return id, err
}

func (s *session) resolveWorkerScope() (string, error) {
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
// PermissionDenied from a narrow delegation bearer that cannot list workers
// falls back to the "no worker selected" usage prompt; every other Hub or
// transport error propagates directly.
func (s *session) autoSelectSoleWorker() (string, error) {
	workers, err := listAllWorkers(s.ctx, s.c)
	if err != nil {
		if connect.CodeOf(err) == connect.CodePermissionDenied {
			return "", cli.Usagef("no worker selected").
				WithHint("pass --worker <worker>, or run the command inside a LeapMux tab")
		}
		return "", err
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

// sectionIndex indexes sidebar sections by ID and maps each workspace ID to
// its assigned section. A nil receiver is valid and reports every workspace as
// unassigned and active.
type sectionIndex struct {
	byID             map[string]*leapmuxv1.Section
	workspaceSection map[string]*leapmuxv1.Section
}

func indexSections(list *leapmuxv1.ListSectionsResponse) *sectionIndex {
	idx := &sectionIndex{
		byID:             make(map[string]*leapmuxv1.Section, len(list.GetSections())),
		workspaceSection: make(map[string]*leapmuxv1.Section, len(list.GetItems())),
	}
	for _, sec := range list.GetSections() {
		idx.byID[sec.GetId()] = sec
	}
	for _, item := range list.GetItems() {
		if sec := idx.byID[item.GetSectionId()]; sec != nil {
			idx.workspaceSection[item.GetWorkspaceId()] = sec
		}
	}
	return idx
}

func (idx *sectionIndex) sectionOf(wsID string) *leapmuxv1.Section {
	if idx == nil {
		return nil
	}
	return idx.workspaceSection[wsID]
}

func (idx *sectionIndex) isArchived(wsID string) bool {
	sec := idx.sectionOf(wsID)
	return sec != nil && sec.GetSectionType() == leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED
}

func (idx *sectionIndex) archiveState(wsID string) leapmuxv1.WorkspaceArchiveState {
	if idx.isArchived(wsID) {
		return leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED
	}
	return leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ACTIVE
}

// loadSectionIndex fetches sidebar sections from the Hub and indexes them.
func (s *session) loadSectionIndex() (*sectionIndex, error) {
	var resp leapmuxv1.ListSectionsResponse
	if err := hubCallUnary(s.ctx, s.c, leapmuxv1connect.SectionServiceListSectionsProcedure, &leapmuxv1.ListSectionsRequest{}, &resp); err != nil {
		return nil, err
	}
	return indexSections(&resp), nil
}

// loadArchivedSectionIndex loads sidebar sections to classify archived
// workspaces. It tolerates PermissionDenied from a narrow delegation bearer
// that can list workspaces without reading sidebar sections, and propagates
// every other failure (such as Unavailable or Unauthenticated).
func (s *session) loadArchivedSectionIndex() (*sectionIndex, error) {
	idx, err := s.loadSectionIndex()
	if err != nil {
		if connect.CodeOf(err) == connect.CodePermissionDenied {
			return &sectionIndex{}, nil
		}
		return nil, err
	}
	return idx, nil
}

// defaultWorkspaceID resolves a default workspace outside a LeapMux tab:
// context.json's workspace, else the sole active (non-archived) workspace on
// the account. Returns "" if no unique default applies.
func (s *session) defaultWorkspaceID() (string, error) {
	if ctxCfg, err := control.LoadContext(); err == nil && ctxCfg.Workspace != "" {
		ws, err := s.resolveExistingWorkspace(ctxCfg.Workspace)
		if err != nil {
			return "", err
		}
		return ws.ID, nil
	}
	var resp leapmuxv1.ListWorkspacesResponse
	if err := hubCallUnary(s.ctx, s.c, leapmuxv1connect.WorkspaceServiceListWorkspacesProcedure, &leapmuxv1.ListWorkspacesRequest{}, &resp); err != nil {
		return "", err
	}
	sections, err := s.loadArchivedSectionIndex()
	if err != nil {
		return "", err
	}
	var active []*leapmuxv1.Workspace
	for _, w := range resp.GetWorkspaces() {
		if !sections.isArchived(w.GetId()) {
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

// floatFlag reads a floating-point flag and rejects NaN or Inf values.
func floatFlag(inv *cli.Invocation, name string) (float64, error) {
	v, err := inv.Command.Flags().GetFloat64(name)
	if err != nil {
		return 0, cli.Wrap(cli.CodeUsage, err, "read --"+name)
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, cli.Usagef("--%s must be a finite number", name)
	}
	return v, nil
}
