package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/coder/quartz"
	"github.com/spf13/pflag"
	"golang.org/x/sync/errgroup"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/util/agentenv"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
)

// The creation commands -- `agent open`, `terminal open` and `file open` --
// share one placement: the scope flags --workspace, --tile and --worker, and
// the position flags --first, --last, --before and --after.
//
// Each ordinary tab type uses Worker-owned creation and Hub publication. FILE
// payloads travel through RegisterTabPayload over the encrypted channel, so
// the hub never sees a file path -- only the opaque tab id and the
// presentation registers.

// addPlacementFlags registers the placement of a new tab.
func addPlacementFlags(fs *pflag.FlagSet) {
	fs.String("workspace", "", `workspace of the new tab (default: the workspace of this tab); the tab lands on its first tile unless --tile says otherwise`)
	fs.String("tile", "", "tile of the new tab (default: the tile of this tab)")
	addWorkerFlag(fs)
	addPositionFlags(fs, "put the new tab")
	fs.Bool("focus", false, "reveal and focus the new tab in open LeapMux windows")
	fs.Bool("no-focus", false, "keep the new tab in the background without focusing it")
	for _, name := range []string{"workspace", "tile", workerFlag, "first", "last", "before", "after", "focus", "no-focus"} {
		cli.SetFlagGroup(fs, name, "Placement")
	}
}

func validateFocusFlags(inv *cli.Invocation) error {
	if boolFlag(inv, "focus") && boolFlag(inv, "no-focus") {
		return cli.Usagef("--focus and --no-focus exclude each other")
	}
	return nil
}

// validatePositionFlags checks mutual exclusion among --first, --last,
// --before, and --after before any network session is opened.
func validatePositionFlags(inv *cli.Invocation) error {
	_, err := parsePositionSpec(
		boolFlag(inv, "first"),
		boolFlag(inv, "last"),
		stringFlag(inv, "before"),
		stringFlag(inv, "after"),
	)
	if err != nil {
		return cli.Usagef("%s", err.Error())
	}
	return nil
}

// shouldFocusNewTab decides whether a newly opened tab should be revealed in
// open LeapMux windows: explicit --focus / --no-focus wins, then
// $LEAPMUX_FOCUS_NEW, then automatic TTY + non-agent environment detection.
func shouldFocusNewTab(inv *cli.Invocation) bool {
	if inv.Changed("focus") && boolFlag(inv, "focus") {
		return true
	}
	if inv.Changed("no-focus") && boolFlag(inv, "no-focus") {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LEAPMUX_FOCUS_NEW"))) {
	case "1", "true", "yes":
		return true
	case "0", "false", "no":
		return false
	}
	if !isTerminalIO(inv.Stdin) || !isTerminalIO(inv.Command.OutOrStdout()) {
		return false
	}
	return !isAgentEnvironment()
}

// isAgentEnvironment reports whether the CLI runs inside an agent tab, CI, or
// another known coding-agent process where stealing window focus is disruptive.
func isAgentEnvironment() bool {
	if strings.EqualFold(strings.TrimSpace(os.Getenv(controlenv.TabTypeVariable)), "agent") {
		return true
	}
	if strings.TrimSpace(os.Getenv("CI")) != "" {
		return true
	}
	for _, key := range agentenv.HarnessDetectionMarkers {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return true
		}
	}
	return false
}

// agentSlugPattern validates optional short agent slugs passed via --name.
var agentSlugPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

func validateAgentSlug(slug string) error {
	if !agentSlugPattern.MatchString(slug) {
		return cli.Usagef("--name %q must start with a lowercase letter and hold 1 to 32 lowercase letters, digits, hyphens, or underscores", slug)
	}
	switch slug {
	case resolve.SelfTarget, resolve.ParentTarget, childrenTarget:
		return cli.Usagef("--name %q is a reserved target keyword", slug)
	}
	return nil
}

func parseAgentSlugFlag(inv *cli.Invocation) (string, error) {
	slug := strings.TrimSpace(stringFlag(inv, "name"))
	if err := validateAgentSlug(slug); err != nil {
		return "", err
	}
	return slug, nil
}

// parseAgentTitleAndSlug parses the mutually exclusive --title and --name
// flags shared by `agent open` and `agent fork`.
func parseAgentTitleAndSlug(inv *cli.Invocation) (title, slug string, err error) {
	title = stringFlag(inv, "title")
	if !inv.Changed("name") {
		return title, "", nil
	}
	if inv.Changed("title") {
		return "", "", cli.Usagef("give --name or --title, not both")
	}
	slug, err = parseAgentSlugFlag(inv)
	if err != nil {
		return "", "", err
	}
	return slug, slug, nil
}

// ensureUniqueAgentSlug verifies that no other live agent on the account
// already uses slug (case-insensitively), reusing any tab listing and Worker
// details already cached on the session.
func (s *session) ensureUniqueAgentSlug(slug, excludeAgentID string) error {
	return s.ensureUniqueAgentSlugContext(s.ctx, slug, excludeAgentID)
}

func (s *session) ensureUniqueAgentSlugContext(ctx context.Context, slug, excludeAgentID string) error {
	tabs, err := s.listAllTabs(ctx)
	if err != nil {
		return err
	}
	agents := filterTabsByType(tabs, leapmuxv1.TabType_TAB_TYPE_AGENT)
	if len(agents) == 0 {
		return nil
	}
	details := s.fetchTabDetailsCached(ctx, agents)
	for _, t := range agents {
		id := t.GetTab().GetTabId()
		if id == excludeAgentID {
			continue
		}
		if d := details[id]; strings.EqualFold(d.Title, slug) {
			return cli.Newf(cli.CodeConflict, "agent name %q is already in use by agent %s", slug, cli.ShortID(id)).
				WithHint("choose a different --name or rename agent %s", cli.ShortID(id))
		}
	}
	return nil
}

// addWorkingDirFlag registers --dir, the working directory of a new tab.
func addWorkingDirFlag(fs *pflag.FlagSet, usage string) {
	fs.String("dir", "", usage)
	cli.SetEnvDefault(fs, "dir", controlenv.WorkingDirVariable)
}

// workingDir returns --dir, or $LEAPMUX_CONTROL_WORKING_DIR when the
// command line omits it.
func workingDir(inv *cli.Invocation) string {
	if inv.Changed("dir") {
		return stringFlag(inv, "dir")
	}
	return workingDirEnv()
}

// placement is where a creation command puts the new tab.
type placement struct {
	WorkspaceID string
	// TileID is empty when the tab lands on the first tile of the
	// workspace.
	TileID   string
	WorkerID string
	Position positionSpec
}

// placementDefaults supplies fallback placement fields when explicit flags and
// ambient context leave them empty (for example, `tab open-image` falling back
// to the source agent's placement without running Worker discovery).
type placementDefaults struct {
	WorkspaceID string
	TileID      string
	WorkerID    string
}

// placement resolves the placement flags of a creation command.
//
// Without --workspace and --tile, the new tab lands beside the tab that
// runs the command: the resolver derives that tab's workspace and tile from
// the calling tab. When --before or --after is set without --workspace or
// --tile, the new tab follows the reference tab's workspace and tile instead.
// With --workspace or --tile, the ambient anchor is not used for the tile, so
// `--workspace W` puts the tab on W's first tile instead of on a tile of
// another workspace. The Worker is --worker, else the Worker of the tab that
// runs the command.
func (s *session) placement() (placement, error) {
	return s.placementWithDefaults(placementDefaults{})
}

// explicitPlacement holds the placement flags explicitly specified by the
// caller, plus the reference tab's workspace and tile when --before or --after
// was passed without explicit --workspace or --tile.
type explicitPlacement struct {
	Position     positionSpec
	Inputs       resolve.Inputs
	HasRefTarget bool
}

// resolveExplicitPlacement parses the position spec, explicit --tile,
// --workspace, and --worker flags, and (when neither --workspace nor --tile is
// set) resolves the reference tab's workspace and tile for --before / --after.
func (s *session) resolveExplicitPlacement() (explicitPlacement, error) {
	spec, err := s.positionSpec()
	if err != nil {
		return explicitPlacement{}, err
	}
	out := explicitPlacement{Position: spec}
	if s.inv.Changed("tile") {
		t, err := s.resolveOne(resolve.KindTile, stringFlag(s.inv, "tile"))
		if err != nil {
			return explicitPlacement{}, err
		}
		out.Inputs.TileID, out.Inputs.ExplicitTileID = t.ID, true
	}
	if s.inv.Changed("workspace") {
		w, err := s.resolveOne(resolve.KindWorkspace, stringFlag(s.inv, "workspace"))
		if err != nil {
			return explicitPlacement{}, err
		}
		out.Inputs.WorkspaceID, out.Inputs.ExplicitWorkspaceID = w.ID, true
	}
	if s.inv.Changed(workerFlag) {
		k, err := s.workerScope()
		if err != nil {
			return explicitPlacement{}, err
		}
		out.Inputs.WorkerID, out.Inputs.ExplicitWorkerID = k, true
	}
	if !out.Inputs.ExplicitTileID && !out.Inputs.ExplicitWorkspaceID && (spec.kind == positionBefore || spec.kind == positionAfter) {
		// Relative placement without explicit --tile or --workspace follows
		// the reference tab's workspace and tile instead of pinning to the
		// caller's own ambient tab or source agent.
		refGot, err := resolve.Resolve(s.ctx, s.deps(), resolve.Need{
			TabID:       true,
			WorkspaceID: true,
			TileID:      true,
		}, resolve.Inputs{
			TabID:         spec.refID,
			ExplicitTabID: true,
		})
		if err != nil {
			return explicitPlacement{}, err
		}
		out.Inputs.WorkspaceID = refGot.WorkspaceID
		out.Inputs.TileID = refGot.TileID
		out.HasRefTarget = true
	}
	return out, nil
}

func (s *session) placementWithDefaults(defaults placementDefaults) (placement, error) {
	exp, err := s.resolveExplicitPlacement()
	if err != nil {
		return placement{}, err
	}
	in := exp.Inputs
	if !in.ExplicitWorkerID {
		if envWorker := strings.TrimSpace(os.Getenv(controlenv.WorkerIDVariable)); envWorker != "" {
			in.WorkerID = envWorker
		} else if defaults.WorkerID != "" {
			in.WorkerID = defaults.WorkerID
		} else {
			// Best-effort zero-config default outside a LeapMux tab: context
			// worker or sole online worker. Leaves WorkerID empty if ambiguous
			// so `file open` can still infer the worker from the workspace,
			// while `agent open` / `terminal open` report the full error via
			// requireWorker.
			if k, err := s.workerScope(); err == nil {
				in.WorkerID = k
			}
		}
	}
	if in.TileID == "" && in.WorkspaceID == "" {
		if selfID := s.selfTabID(); selfID != "" {
			in.TabID = selfID
		} else if defaults.WorkspaceID != "" || defaults.TileID != "" {
			in.WorkspaceID = defaults.WorkspaceID
			in.TileID = defaults.TileID
		} else {
			defWS, err := s.defaultWorkspaceID()
			if err != nil {
				return placement{}, err
			}
			if defWS != "" {
				in.WorkspaceID = defWS
			}
		}
	}
	got, err := resolve.Resolve(s.ctx, s.deps(), resolve.Need{WorkspaceID: true, Want: resolve.Wants{TileID: true, WorkerID: true}}, in)
	if err != nil && in.TabID != "" && (defaults.WorkspaceID != "" || defaults.TileID != "") {
		// Ambient tab could not supply a workspace (for example, a quake
		// terminal); fall back to the caller-supplied defaults.
		in.TabID = ""
		in.WorkspaceID = defaults.WorkspaceID
		in.TileID = defaults.TileID
		got, err = resolve.Resolve(s.ctx, s.deps(), resolve.Need{WorkspaceID: true, Want: resolve.Wants{TileID: true, WorkerID: true}}, in)
	}
	if err != nil {
		return placement{}, err
	}
	return placement{WorkspaceID: got.WorkspaceID, TileID: got.TileID, WorkerID: got.WorkerID, Position: exp.Position}, nil
}

// requireWorker refuses a placement without a Worker, for a tab that runs
// on one.
func (s *session) requireWorker(p placement) error {
	if p.WorkerID != "" {
		return nil
	}
	if _, err := s.workerScope(); err != nil {
		return err
	}
	return cli.Usagef("no worker selected").
		WithHint("pass --worker <worker>, or run the command inside a LeapMux tab")
}

// addGitSetupFlags registers how a new agent or terminal prepares its
// working directory. At most one of the four modes applies.
func addGitSetupFlags(fs *pflag.FlagSet) {
	fs.String("worktree", "", "create a git worktree on this new branch and start in it")
	fs.String("use-worktree", "", "start in an existing git worktree by path, path:<path>, or branch:<branch>")
	fs.String("checkout", "", "check out this existing branch first")
	fs.String("new-branch", "", "create this branch and check it out first")
	fs.String("base", "", "the branch that --worktree or --new-branch starts from (default: the current branch)")
	for _, name := range []string{"worktree", "use-worktree", "checkout", "new-branch", "base"} {
		cli.SetFlagGroup(fs, name, "Git & Directory")
	}
}

// gitSetup reads the git flags of the invocation. It returns nil when none
// is set.
func gitSetup(inv *cli.Invocation) (*leapmuxv1.GitSetup, error) {
	var set []string
	for _, name := range []string{"worktree", "use-worktree", "checkout", "new-branch"} {
		if inv.Changed(name) {
			set = append(set, "--"+name)
		}
	}
	base := stringFlag(inv, "base")
	switch {
	case len(set) > 1:
		return nil, cli.Usagef("%s exclude each other", strings.Join(set, " and "))
	case len(set) == 0 && inv.Changed("base"):
		return nil, cli.Usagef("--base needs --worktree or --new-branch")
	case len(set) == 0:
		return nil, nil
	}
	rawValue := stringFlag(inv, strings.TrimPrefix(set[0], "--"))
	value := strings.TrimSpace(rawValue)
	if value == "" {
		return nil, cli.Usagef("%s is empty", set[0])
	}
	if base != "" && set[0] != "--worktree" && set[0] != "--new-branch" {
		return nil, cli.Usagef("--base applies only to --worktree and --new-branch")
	}
	switch set[0] {
	case "--worktree":
		return &leapmuxv1.GitSetup{Mode: &leapmuxv1.GitSetup_CreateWorktree{CreateWorktree: &leapmuxv1.GitCreateWorktree{Branch: value, BaseBranch: base}}}, nil
	case "--use-worktree":
		selector, err := parseWorktreeSelector(rawValue)
		if err != nil {
			return nil, err
		}
		return &leapmuxv1.GitSetup{Mode: &leapmuxv1.GitSetup_UseWorktree{UseWorktree: selector}}, nil
	case "--checkout":
		return &leapmuxv1.GitSetup{Mode: &leapmuxv1.GitSetup_CheckoutBranch{CheckoutBranch: value}}, nil
	default:
		return &leapmuxv1.GitSetup{Mode: &leapmuxv1.GitSetup_CreateBranch{CreateBranch: &leapmuxv1.GitCreateBranch{Name: value, BaseBranch: base}}}, nil
	}
}

// openedTab is the result of a creation command.
type openedTab struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Title       string `json:"title,omitempty"`
	WorkspaceID string `json:"workspace_id"`
	WorkerID    string `json:"worker_id"`
	TileID      string `json:"tile_id"`
	Position    string `json:"position"`
	Path        string `json:"path,omitempty"`
	RequestID   string `json:"request_id,omitempty"`
}

// RenderText prints a concise one-line creation summary with short IDs.
func (o openedTab) RenderText(w io.Writer) error {
	var b strings.Builder
	fmt.Fprintf(&b, "opened %s %s", o.Type, cli.ShortID(o.ID))
	if o.Title != "" {
		fmt.Fprintf(&b, " %q", cli.SanitizeSingleLine(o.Title))
	}
	if o.Path != "" {
		fmt.Fprintf(&b, " (%s)", cli.SanitizeSingleLine(o.Path))
	}
	if o.WorkspaceID != "" {
		fmt.Fprintf(&b, " in workspace %s", cli.ShortID(o.WorkspaceID))
	}
	if o.TileID != "" {
		fmt.Fprintf(&b, " tile %s", cli.ShortID(o.TileID))
	}
	_, err := fmt.Fprintln(w, b.String())
	return err
}

// readAgentOpenMessage reads an optional positional [prompt] (supporting "-"),
// --message/-m (supporting "-"), or --file, plus repeatable --attach files,
// before any network connection is opened.
func readInlineOrStdin(stdin io.Reader, raw, label string) (string, error) {
	if raw == "-" {
		data, err := readStdinGuarded(stdin)
		if err != nil {
			return "", err
		}
		raw = string(data)
	}
	if strings.TrimSpace(raw) == "" {
		return "", cli.Usagef("%s is empty", label)
	}
	return raw, nil
}

func readAgentOpenMessage(inv *cli.Invocation) (*userMessage, error) {
	hasPrompt := len(inv.Args) > 0 && inv.HasArg("prompt")
	hasMsg := inv.Changed("message")
	hasFile := inv.Changed("file")
	attachPaths := inv.StringArray("attach")
	if hasPrompt && (hasMsg || hasFile) {
		return nil, cli.Usagef("give [prompt], --message, or --file, not more than one")
	}
	if hasMsg && hasFile {
		return nil, cli.Usagef("give --message or --file, not both")
	}
	if len(attachPaths) > contracts.MaxAgentInputAttachmentsPerItem {
		return nil, cli.Usagef("a message holds at most %d attachments; --attach gives %d", contracts.MaxAgentInputAttachmentsPerItem, len(attachPaths))
	}
	if !hasPrompt && !hasMsg && !hasFile && len(attachPaths) == 0 {
		return nil, nil
	}
	var text string
	switch {
	case hasFile:
		data, err := readFileOrStdin(inv.Stdin, stringFlag(inv, "file"), "--file")
		if err != nil {
			return nil, err
		}
		text = string(data)
		if strings.TrimSpace(text) == "" {
			return nil, cli.Usagef("--file is empty")
		}
	case hasMsg:
		var err error
		text, err = readInlineOrStdin(inv.Stdin, stringFlag(inv, "message"), "--message")
		if err != nil {
			return nil, err
		}
	case hasPrompt:
		var err error
		text, err = readInlineOrStdin(inv.Stdin, inv.Arg("prompt"), "prompt")
		if err != nil {
			return nil, err
		}
	}
	if len(text) > contracts.MaxAgentInputItemBytes {
		return nil, cli.Usagef("the message holds %d bytes; one message holds at most %d bytes", len(text), contracts.MaxAgentInputItemBytes)
	}
	attachments, err := readAttachments(attachPaths, int64(contracts.MaxAgentInputItemBytes-len(text)))
	if err != nil {
		return nil, err
	}
	return &userMessage{text: text, attachments: attachments}, nil
}

// agentLaunchFlags holds the pre-validated CLI flags shared by `agent open`
// and `agent fork`.
type agentLaunchFlags struct {
	wait              bool
	all               bool
	timeout           time.Duration
	title             string
	slug              string
	opts              map[string]string
	msg               *userMessage
	schemaSpec        *outputSchemaSpec
	setup             *leapmuxv1.GitSetup
	requestID         string
	explicitRequestID bool
	atomicInitialMsg  *string
	postOpenMsg       bool
	focus             bool
}

// parseAgentLaunchFlags validates every flag shared by `agent open` and
// `agent fork` before any network connection is opened.
func parseAgentLaunchFlags(inv *cli.Invocation) (*agentLaunchFlags, error) {
	if err := validateFocusFlags(inv); err != nil {
		return nil, err
	}
	if err := validatePositionFlags(inv); err != nil {
		return nil, err
	}
	wait := boolFlag(inv, "wait")
	if !wait {
		for _, name := range []string{"all", "timeout", outputSchemaFlag, schemaRetriesFlag} {
			if inv.Changed(name) {
				return nil, cli.Usagef("--%s needs --wait", name)
			}
		}
	}
	timeout, err := waitTimeoutFlag(inv)
	if err != nil {
		return nil, err
	}
	title, slug, err := parseAgentTitleAndSlug(inv)
	if err != nil {
		return nil, err
	}
	opts, err := buildAgentSetOptions(
		stringFlag(inv, "model"),
		stringFlag(inv, "effort"),
		stringFlag(inv, "permission-mode"),
		inv.StringArray("option"),
	)
	if err != nil {
		return nil, cli.Wrap(cli.CodeUsage, err, "")
	}
	msg, err := readAgentOpenMessage(inv)
	if err != nil {
		return nil, err
	}
	all := boolFlag(inv, "all")
	if wait && all && msg == nil {
		return nil, cli.Usagef("--all needs [prompt], --message, --file, or --attach with --wait")
	}
	promptUsedStdin := (inv.HasArg("prompt") && inv.Arg("prompt") == "-") ||
		(inv.Changed("message") && stringFlag(inv, "message") == "-") ||
		(inv.Changed("file") && stringFlag(inv, "file") == "-")
	schemaSpec, err := readOutputSchemaSpec(inv, wait, msg != nil, promptUsedStdin)
	if err != nil {
		return nil, err
	}
	setup, err := gitSetup(inv)
	if err != nil {
		return nil, err
	}
	requestID, explicitRequestID, err := ensureRequestID(inv)
	if err != nil {
		return nil, err
	}
	// Pass initial_message atomically in OpenAgent when there are no attachments
	// and --wait is not requested. When --attach or --wait is used, send via
	// EnqueueAgentInput so attachments and exact reply tracking work uniformly.
	var atomicInitialMsg *string
	postOpenMsg := false
	if msg != nil {
		if len(msg.attachments) == 0 && !wait {
			text := msg.text
			atomicInitialMsg = &text
		} else {
			postOpenMsg = true
		}
	}
	return &agentLaunchFlags{
		wait:              wait,
		all:               all,
		timeout:           timeout,
		title:             title,
		slug:              slug,
		opts:              opts,
		msg:               msg,
		schemaSpec:        schemaSpec,
		setup:             setup,
		requestID:         requestID,
		explicitRequestID: explicitRequestID,
		atomicInitialMsg:  atomicInitialMsg,
		postOpenMsg:       postOpenMsg,
		focus:             shouldFocusNewTab(inv),
	}, nil
}

// prepareSpawnSession opens the session pair, resolves placement and Worker,
// and resolves the working directory (including --use-worktree lookup) for
// `agent open` and `terminal open`.
func prepareSpawnSession(inv *cli.Invocation, wait bool, setup *leapmuxv1.GitSetup) (stream, callSession *session, closeAll func(), p placement, dir string, err error) {
	stream, callSession, closeAll, err = openWaitableSession(inv, wait)
	if err != nil {
		return nil, nil, nil, placement{}, "", err
	}
	p, err = callSession.placement()
	if err != nil {
		closeAll()
		return nil, nil, nil, placement{}, "", err
	}
	if err = callSession.requireWorker(p); err != nil {
		closeAll()
		return nil, nil, nil, placement{}, "", err
	}
	dir = workingDir(inv)
	if selector := setup.GetUseWorktree(); selector != nil {
		dir, err = callSession.worktreeRepoDir(p.WorkerID, selector)
		if err != nil {
			closeAll()
			return nil, nil, nil, placement{}, "", err
		}
	}
	return stream, callSession, closeAll, p, dir, nil
}

// revealTabBestEffort asks the Worker to focus tabID in open frontends when
// focus is true. Reveal is a transient UI hint, so errors are ignored.
func revealTabBestEffort(ctx context.Context, w workerCall, focus bool, tabID string) {
	if !focus || tabID == "" {
		return
	}
	_ = w.Call(ctx, leapmuxv1connect.WorkerPrivateServiceRevealTabProcedure, &leapmuxv1.RevealTabRequest{TabId: tabID}, nil)
}

type agentLaunchMeta struct {
	verb          string
	buildRetryCmd func(*cli.Invocation, string) string
}

// finishAgentLaunch completes `agent open` or `agent fork` after the agent tab
// has been created: running sendAndWait for a waited message, waiting for the
// agent to settle when --wait has no message, or returning the openedTab.
func finishAgentLaunch(
	inv *cli.Invocation,
	s *session,
	p placement,
	result *openAgentResult,
	flags *agentLaunchFlags,
	meta agentLaunchMeta,
) (any, error) {
	tab := openedTab{
		ID:          result.Agent.GetId(),
		Type:        resolve.TabTypeWireName(leapmuxv1.TabType_TAB_TYPE_AGENT),
		Title:       result.Agent.GetTitle(),
		WorkspaceID: p.WorkspaceID,
		WorkerID:    p.WorkerID,
		TileID:      result.TileID,
		Position:    result.Position,
		RequestID:   flags.requestID,
	}
	agentTarget := tabTarget{
		ID:          tab.ID,
		Type:        leapmuxv1.TabType_TAB_TYPE_AGENT,
		WorkerID:    p.WorkerID,
		WorkspaceID: p.WorkspaceID,
		TileID:      result.TileID,
	}
	if flags.postOpenMsg && flags.wait {
		inputID := requestScopedID(flags.requestID, requestScopeAgentSend, agentTarget.ID)
		replyRes, outcome, msgStage, wErr := sendAndWait(s, sendAndWaitRequest{
			target:    agentTarget,
			message:   *flags.msg,
			requestID: flags.requestID,
			inputID:   inputID,
			delivery:  sendDeliveryNormal,
			all:       flags.all,
			timeout:   flags.timeout,
			schema:    flags.schemaSpec,
		})
		if wErr != nil {
			return nil, wrapRetryReceipt(wErr, msgStage, retryReceipt{
				RequestID:      flags.requestID,
				Explicit:       flags.explicitRequestID,
				CreatedAgentID: agentTarget.ID,
				QueryCommand:   buildAgentRequestStatusCmd(agentTarget.ID, flags.requestID),
				RetryCommand:   meta.buildRetryCmd(inv, flags.requestID),
			})
		}
		replyRes.RequestID = flags.requestID
		if inv != nil && inv.Printer != nil && inv.Printer.Format == cli.FormatText {
			verb := meta.verb
			if verb == "" {
				verb = "opened"
			}
			_ = inv.Printer.Notice("%s agent %s", verb, cli.ShortID(result.Agent.GetId()))
		}
		if outcome == replyWaitSettled {
			return agentLaunchReplyResult{agentReplyResult: replyRes}, nil
		}
		return nil, agentLaunchReplyEnded(inv.Printer, replyRes, outcome, flags.timeout)
	}
	if flags.wait {
		return waitNewAgentSettled(inv, s, agentTarget, flags.timeout, tab)
	}
	return tab, nil
}

// runAgentOpen starts a new agent in a new tab with an optional first message.
func runAgentOpen(inv *cli.Invocation) (any, error) {
	flags, err := parseAgentLaunchFlags(inv)
	if err != nil {
		return nil, err
	}
	s, callSession, closeAll, p, dir, err := prepareSpawnSession(inv, flags.wait, flags.setup)
	if err != nil {
		return nil, err
	}
	defer closeAll()

	if flags.slug != "" {
		excludeID := requestScopedID(flags.requestID, requestScopeAgentOpen, p.WorkerID)
		g, gctx := errgroup.WithContext(callSession.ctx)
		g.Go(func() error {
			return callSession.ensureUniqueAgentSlugContext(gctx, flags.slug, excludeID)
		})
		g.Go(func() error {
			return maybePreflightWorker(gctx, s.c, p.WorkerID)
		})
		if err := g.Wait(); err != nil {
			return nil, err
		}
	} else if err := maybePreflightWorker(callSession.ctx, s.c, p.WorkerID); err != nil {
		return nil, err
	}

	provider := defaultProviderInput(inv)
	var (
		result *openAgentResult
		opened bool
	)
	stage := retryStageBeforeSend
	err = withWorkerChannel(callSession.ctx, s.c, p.WorkerID, func(w workerCall) error {
		resolvedProvider, err := resolveProvider(callSession.ctx, provider, w.Call)
		if err != nil {
			return err
		}
		stage = retryStageDuringSend
		result, err = openAgentAndAddTab(callSession.ctx, s.c, w, openAgentArgs{
			WorkspaceID:    p.WorkspaceID,
			WorkerID:       p.WorkerID,
			TileID:         p.TileID,
			Provider:       resolvedProvider,
			Title:          flags.title,
			Options:        flags.opts,
			WorkingDir:     dir,
			InitialMessage: flags.atomicInitialMsg,
			SessionID:      stringFlag(inv, "resume"),
			GitSetup:       flags.setup,
			Position:       p.Position,
			RequestID:      flags.requestID,
		})
		if err != nil {
			return err
		}
		opened = true
		revealTabBestEffort(callSession.ctx, w, flags.focus, result.Agent.GetId())
		if flags.postOpenMsg && !flags.wait {
			inputID := requestScopedID(flags.requestID, requestScopeAgentSend, result.Agent.GetId())
			if err := w.Call(callSession.ctx, leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure, flags.msg.request(result.Agent.GetId(), inputID), nil); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		receipt := retryReceipt{
			RequestID:    flags.requestID,
			Explicit:     flags.explicitRequestID,
			RetryCommand: buildAgentOpenRetryCmd(inv, flags.requestID),
		}
		if opened {
			receipt.CreatedAgentID = result.Agent.GetId()
			receipt.QueryCommand = buildAgentRequestStatusCmd(result.Agent.GetId(), flags.requestID)
		}
		return nil, wrapRetryReceipt(err, stage, receipt)
	}
	return finishAgentLaunch(inv, s, p, result, flags, agentLaunchMeta{
		verb:          "opened",
		buildRetryCmd: buildAgentOpenRetryCmd,
	})
}

func waitNewAgentSettled(inv *cli.Invocation, s *session, agentTarget tabTarget, timeout time.Duration, tab openedTab) (any, error) {
	spec := agentWaitSpec{
		agentIDs: []string{agentTarget.ID},
		until:    waitConditions{waitUntilSettled},
		timeout:  timeout,
	}
	waitCtx, waitCancel := context.WithCancel(s.ctx)
	events := make(chan activityEvent, 16)
	stopFeed := startActivityFeed(waitCtx, workerActivitySource(s.c, agentTarget.WorkerID), []string{agentTarget.ID}, events)
	defer stopFeed()
	defer waitCancel()
	ch := newPersistentWorkerChannel(s.c, agentTarget.WorkerID)
	defer ch.Close()
	queues := &queueWatch{
		read: func(ctx context.Context, agentID string) (*leapmuxv1.AgentInputQueueSnapshot, error) {
			response, err := channelReplyReader(ch, agentID).readQueue(ctx, "")
			return response.GetSnapshot(), err
		},
		notice: func(format string, args ...any) {
			_ = inv.Printer.Notice(format, args...)
		},
		now: time.Now,
	}
	waitRes, outcome, err := awaitAgents(waitCtx, quartz.NewReal(), events, queues.blocksWait, spec)
	if err != nil {
		return nil, err
	}
	if outcome != agentWaitHeld {
		return nil, agentWaitEnded(inv.Printer, waitRes, outcome, spec)
	}
	return tab, nil
}

// runTerminalOpen starts a new shell in a new tab, with the same channel
// ownership as `agent open`.
func runTerminalOpen(inv *cli.Invocation) (any, error) {
	if err := validateFocusFlags(inv); err != nil {
		return nil, err
	}
	if err := validatePositionFlags(inv); err != nil {
		return nil, err
	}
	wait, waitSpec, err := optionalTerminalWaitSpec(inv, false)
	if err != nil {
		return nil, err
	}
	waitSpec.preOpenBaseline = true
	setup, err := gitSetup(inv)
	if err != nil {
		return nil, err
	}
	initialCommand, err := initialCommandOf(inv)
	if err != nil {
		return nil, err
	}
	s, callSession, closeAll, p, dir, err := prepareSpawnSession(inv, wait, setup)
	if err != nil {
		return nil, err
	}
	defer closeAll()
	if err := maybePreflightWorker(callSession.ctx, s.c, p.WorkerID); err != nil {
		return nil, err
	}
	focus := shouldFocusNewTab(inv)
	var result *openTerminalResult
	err = withWorkerChannel(callSession.ctx, s.c, p.WorkerID, func(w workerCall) error {
		var err error
		result, err = openTerminalAndAddTab(callSession.ctx, s.c, w, openTerminalArgs{
			WorkspaceID:    p.WorkspaceID,
			WorkerID:       p.WorkerID,
			TileID:         p.TileID,
			WorkingDir:     dir,
			Shell:          stringFlag(inv, "shell"),
			ShellStartDir:  stringFlag(inv, "start-dir"),
			Title:          stringFlag(inv, "title"),
			GitSetup:       setup,
			InitialCommand: initialCommand,
			Position:       p.Position,
		})
		if err != nil {
			return err
		}
		revealTabBestEffort(callSession.ctx, w, focus, result.TerminalID)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if wait {
		target := tabTarget{
			ID:          result.TerminalID,
			Type:        leapmuxv1.TabType_TAB_TYPE_TERMINAL,
			WorkerID:    p.WorkerID,
			WorkspaceID: p.WorkspaceID,
			TileID:      result.TileID,
		}
		return waitTerminalForTarget(inv, s, target, waitSpec)
	}
	return openedTab{
		ID:          result.TerminalID,
		Type:        resolve.TabTypeWireName(leapmuxv1.TabType_TAB_TYPE_TERMINAL),
		Title:       result.Title,
		WorkspaceID: p.WorkspaceID,
		WorkerID:    p.WorkerID,
		TileID:      result.TileID,
		Position:    result.Position,
	}, nil
}

// parseTabPresentation validates and builds a TabPresentation from CLI flag
// values. Empty displayMode or viewMode leaves that register unset;
// hasDiffBase distinguishes an explicit --diff-base from an omitted flag.
func parseTabPresentation(displayMode, viewMode, diffBase string, hasDiffBase bool) (*leapmuxv1.TabPresentation, error) {
	presentation := &leapmuxv1.TabPresentation{}
	if displayMode != "" {
		n, ok := parseEnumToken(leapmuxv1.TabDisplayMode_TAB_DISPLAY_MODE_UNSPECIFIED, displayMode)
		if !ok {
			return nil, cli.Usagef("--display-mode must be one of %s; got %q", strings.Join(enumTokens(leapmuxv1.TabDisplayMode_TAB_DISPLAY_MODE_UNSPECIFIED), ", "), displayMode)
		}
		presentation.DisplayMode = leapmuxv1.TabDisplayMode(n).Enum()
	}
	if viewMode != "" {
		n, ok := parseEnumToken(leapmuxv1.TabFileViewMode_TAB_FILE_VIEW_MODE_UNSPECIFIED, viewMode)
		if !ok {
			return nil, cli.Usagef("--view-mode must be one of %s; got %q", strings.Join(enumTokens(leapmuxv1.TabFileViewMode_TAB_FILE_VIEW_MODE_UNSPECIFIED), ", "), viewMode)
		}
		presentation.FileViewMode = leapmuxv1.TabFileViewMode(n).Enum()
	}
	if hasDiffBase {
		presentation.FileDiffBase = &diffBase
	}
	return presentation, nil
}

// resolvePlacementTile resolves p's destination tile against state and runs
// preflightTile to confirm the tile exists in p.WorkspaceID.
func resolvePlacementTile(state *leapmuxv1.UserMaterialized, p placement) (string, error) {
	// --first and --last need a tile to be first or last on: the placement's
	// tile, else the first tile of the workspace. Relative insertion derives
	// the tile from the reference tab instead.
	tileID := p.TileID
	if tileID == "" && (p.Position.kind == positionFirst || p.Position.kind == positionLast) {
		ws, ok := state.GetWorkspaces()[p.WorkspaceID]
		if !ok || ws == nil {
			return "", cli.Newf(cli.CodeNotFound, "no such workspace: %s", p.WorkspaceID)
		}
		tileID = firstLiveLeaf(state, ws.GetRootNodeId())
	}
	resolvedTileID, _, err := resolvePositionSpec(state, tileID, "", p.Position)
	if err != nil {
		return "", err
	}
	if err := preflightTile(state, p.WorkspaceID, resolvedTileID); err != nil {
		return "", err
	}
	return resolvedTileID, nil
}

// registerTabPayload registers an encrypted FILE or IMAGE tab payload on w,
// validates the returned publication receipt, and optionally reveals the tab
// over the same Worker channel.
func registerTabPayload(ctx context.Context, w workerCall, tabType leapmuxv1.TabType, creation *leapmuxv1.TabCreation, payload *leapmuxv1.TabPayload, focus bool) (*leapmuxv1.RegisterTabPayloadResponse, error) {
	req := &leapmuxv1.RegisterTabPayloadRequest{Creation: creation, Payload: payload}
	var resp leapmuxv1.RegisterTabPayloadResponse
	if err := w.Call(ctx, leapmuxv1connect.WorkerPrivateServiceRegisterTabPayloadProcedure, req, &resp); err != nil {
		return nil, err
	}
	if err := validateTabPublication(creation, tabType, resp.GetPublication()); err != nil {
		return nil, err
	}
	revealTabBestEffort(ctx, w, focus, creation.GetTabId())
	return &resp, nil
}

// runFileOpen opens a file in a new viewer tab. The path travels to the
// Worker over the encrypted channel; the hub stores only the tab.
func runFileOpen(inv *cli.Invocation) (any, error) {
	if err := validateFocusFlags(inv); err != nil {
		return nil, err
	}
	if err := validatePositionFlags(inv); err != nil {
		return nil, err
	}
	filePath, err := requireNonEmptyArg(inv, "path")
	if err != nil {
		return nil, err
	}
	presentation, err := parseTabPresentation(
		stringFlag(inv, "display-mode"),
		stringFlag(inv, "view-mode"),
		stringFlag(inv, "diff-base"),
		inv.Changed("diff-base"),
	)
	if err != nil {
		return nil, err
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	p, err := s.placement()
	if err != nil {
		return nil, err
	}
	cc, err := s.openCRDT(p.WorkspaceID)
	if err != nil {
		return nil, err
	}
	resolvedTileID, err := resolvePlacementTile(cc.bs.State, p)
	if err != nil {
		return nil, err
	}
	workerID := p.WorkerID
	if workerID == "" {
		workerID = findWorkerForWorkspaceInState(cc.bs.State, p.WorkspaceID)
		if workerID == "" {
			return nil, cli.Usagef("no worker hosts a tab of this workspace yet").
				WithHint("pass --worker <worker>")
		}
	}
	if err := maybePreflightWorker(cc.ctx, cc.c, workerID); err != nil {
		return nil, err
	}
	// --dir defaults to $LEAPMUX_CONTROL_WORKING_DIR, so a `file open` run
	// from inside an agent or terminal forwards THAT tab's dir -- the same
	// "the file tab inherits the context it was opened from" the UI path
	// sends. Run from outside a LeapMux tab it is empty, and the worker
	// falls back to the file's own directory.
	creation, err := resolveTabCreation(cc.ctx, cc.c, id.Generate(), p.WorkspaceID, resolvedTileID, p.Position)
	if err != nil {
		return nil, err
	}
	creation.Presentation = presentation
	focus := shouldFocusNewTab(inv)
	var regResp *leapmuxv1.RegisterTabPayloadResponse
	err = withWorkerChannel(cc.ctx, cc.c, workerID, func(w workerCall) error {
		var regErr error
		regResp, regErr = registerTabPayload(cc.ctx, w, leapmuxv1.TabType_TAB_TYPE_FILE, creation, &leapmuxv1.TabPayload{
			WorkingDir: workingDir(inv),
			Kind: &leapmuxv1.TabPayload_File{
				File: &leapmuxv1.FileTabPayload{FilePath: filePath},
			},
		}, focus)
		return regErr
	})
	if err != nil {
		return nil, err
	}
	publication := regResp.GetPublication()
	return openedTab{
		ID:          creation.GetTabId(),
		Type:        resolve.TabTypeWireName(leapmuxv1.TabType_TAB_TYPE_FILE),
		WorkspaceID: p.WorkspaceID,
		WorkerID:    workerID,
		TileID:      publication.GetTileId(),
		Position:    publication.GetPosition(),
		Path:        filePath,
	}, nil
}

// findWorkerForWorkspaceInState returns the worker_id hosting the live tab
// with the lexicographically smallest tab_id in the given workspace, reading
// from the already-bootstrapped UserMaterialized state. Used by `file open`
// when neither --worker nor the tab that runs the command selects a worker —
// a workspace that has at least one live tab tells us which worker owns its
// filesystem context. Returns "" when no live worker is on file.
func findWorkerForWorkspaceInState(state *leapmuxv1.UserMaterialized, workspaceID string) string {
	tabs := state.GetTabs()
	if len(tabs) == 0 {
		return ""
	}
	ids := make([]string, 0, len(tabs))
	for tabID := range tabs {
		ids = append(ids, tabID)
	}
	sort.Strings(ids)
	for _, tabID := range ids {
		t := tabs[tabID]
		if t == nil || !crdt.HLCIsZero(t.GetTombstoneAt()) {
			continue
		}
		w := t.GetWorkerId().GetValue()
		if w == "" {
			continue
		}
		if nodeWorkspaceFromState(state, t.GetTileId().GetValue()) == workspaceID {
			return w
		}
	}
	return ""
}
