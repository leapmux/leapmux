package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/coder/quartz"
	"github.com/spf13/pflag"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/hub/crdt"
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
	if !isTerminalReader(inv.Stdin) || !isTerminalWriter(inv.Command.OutOrStdout()) {
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
	for _, key := range []string{
		"CLAUDECODE",
		"CODEX_THREAD_ID",
		"CURSOR_AGENT",
		"AIDER",
		"OPENCODE",
		"GEMINI_CLI",
		"ANTIGRAVITY_AGENT",
		"CI",
	} {
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
	case resolve.SelfTarget, "parent", "children":
		return cli.Usagef("--name %q is a reserved target keyword", slug)
	}
	return nil
}

// ensureUniqueAgentSlug verifies that no other live agent on the account
// already uses slug (case-insensitively).
func (s *session) ensureUniqueAgentSlug(slug, excludeAgentID string) error {
	tabs, err := listTabsInWorkspaces(s.ctx, s.c, nil)
	if err != nil {
		return err
	}
	agents := filterTabsByType(tabs, leapmuxv1.TabType_TAB_TYPE_AGENT)
	if len(agents) == 0 {
		return nil
	}
	details := fetchTabDetails(s.ctx, s.c, agents)
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

// placement resolves the placement flags of a creation command.
//
// Without --workspace and --tile, the new tab lands beside the tab that
// runs the command: the resolver derives that tab's workspace and tile from
// $LEAPMUX_CONTROL_TAB_ID. With either flag, the anchor is not used for the
// tile, so `--workspace W` puts the tab on W's first tile instead of on a
// tile of another workspace. The Worker is --worker, else the Worker of the
// tab that runs the command.
func (s *session) placement() (placement, error) {
	in := resolve.Inputs{}
	if s.inv.Changed("tile") {
		t, err := s.resolveOne(resolve.KindTile, stringFlag(s.inv, "tile"))
		if err != nil {
			return placement{}, err
		}
		in.TileID, in.ExplicitTileID = t.ID, true
	}
	if s.inv.Changed("workspace") {
		w, err := s.resolveOne(resolve.KindWorkspace, stringFlag(s.inv, "workspace"))
		if err != nil {
			return placement{}, err
		}
		in.WorkspaceID, in.ExplicitWorkspaceID = w.ID, true
	}
	if s.inv.Changed(workerFlag) {
		k, err := s.workerScope()
		if err != nil {
			return placement{}, err
		}
		in.WorkerID, in.ExplicitWorkerID = k, true
	} else {
		in.WorkerID = strings.TrimSpace(os.Getenv(controlenv.WorkerIDVariable))
		if in.WorkerID == "" {
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
		in.TabID = strings.TrimSpace(os.Getenv(controlenv.TabIDVariable))
		if in.TabID == "" {
			defWS, err := s.defaultWorkspaceID()
			if err != nil {
				return placement{}, err
			}
			if defWS != "" {
				in.WorkspaceID = defWS
			}
		}
	}
	spec, err := s.positionSpec()
	if err != nil {
		return placement{}, err
	}
	got, err := resolve.Resolve(s.ctx, s.deps(), resolve.Need{WorkspaceID: true, Want: resolve.Wants{TileID: true, WorkerID: true}}, in)
	if err != nil {
		return placement{}, err
	}
	return placement{WorkspaceID: got.WorkspaceID, TileID: got.TileID, WorkerID: got.WorkerID, Position: spec}, nil
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
		selector, err := parseWorktreeSelector(rawValue, true)
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

// readAgentOpenMessage reads --message (supporting "-") or --file, plus
// repeatable --attach files, before any network connection is opened.
func readAgentOpenMessage(inv *cli.Invocation) (*userMessage, error) {
	hasMsg := inv.Changed("message")
	hasFile := inv.Changed("file")
	attachPaths := inv.StringArray("attach")
	if hasMsg && hasFile {
		return nil, cli.Usagef("give --message or --file, not both")
	}
	if len(attachPaths) > contracts.MaxAgentInputAttachmentsPerItem {
		return nil, cli.Usagef("a message holds at most %d attachments; --attach gives %d", contracts.MaxAgentInputAttachmentsPerItem, len(attachPaths))
	}
	if !hasMsg && !hasFile && len(attachPaths) == 0 {
		return nil, nil
	}
	var text string
	switch {
	case hasFile:
		path := stringFlag(inv, "file")
		var data []byte
		var err error
		if path == "-" {
			data, err = readStdinGuarded(inv.Stdin)
		} else {
			data, err = os.ReadFile(path)
		}
		if err != nil {
			if path == "-" {
				return nil, err
			}
			return nil, cli.Wrap(cli.CodeUsage, err, "read --file")
		}
		text = string(data)
		if strings.TrimSpace(text) == "" {
			return nil, cli.Usagef("--file is empty")
		}
	case hasMsg:
		raw := stringFlag(inv, "message")
		if raw == "-" {
			data, err := readStdinGuarded(inv.Stdin)
			if err != nil {
				return nil, err
			}
			raw = string(data)
		}
		if strings.TrimSpace(raw) == "" {
			return nil, cli.Usagef("--message is empty")
		}
		text = raw
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

// runAgentOpen starts a new agent in a new tab with an optional first message.
func runAgentOpen(inv *cli.Invocation) (any, error) {
	if err := validateFocusFlags(inv); err != nil {
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
	title := stringFlag(inv, "title")
	slug := ""
	if inv.Changed("name") {
		if inv.Changed("title") {
			return nil, cli.Usagef("give --name or --title, not both")
		}
		slug = strings.TrimSpace(stringFlag(inv, "name"))
		if err := validateAgentSlug(slug); err != nil {
			return nil, err
		}
		title = slug
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
	if wait && boolFlag(inv, "all") && msg == nil {
		return nil, cli.Usagef("--all needs --message, --file, or --attach with --wait")
	}
	promptUsedStdin := (inv.Changed("message") && stringFlag(inv, "message") == "-") || (inv.Changed("file") && stringFlag(inv, "file") == "-")
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
	open := openSession
	if wait {
		open = openStreamSession
	}
	s, err := open(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	bounded := s
	if wait {
		bounded = s.bounded()
		defer bounded.Close()
	}
	p, err := bounded.placement()
	if err != nil {
		return nil, err
	}
	if err := bounded.requireWorker(p); err != nil {
		return nil, err
	}
	if slug != "" {
		if err := bounded.ensureUniqueAgentSlug(slug, requestScopedID(requestID, requestScopeAgentOpen, p.WorkerID)); err != nil {
			return nil, err
		}
	}
	dir := workingDir(inv)
	if selector := setup.GetUseWorktree(); selector != nil {
		dir, err = bounded.worktreeRepoDir(p.WorkerID, selector)
		if err != nil {
			return nil, err
		}
	}
	if err := maybePreflightWorker(bounded.ctx, s.c, p.WorkerID); err != nil {
		return nil, err
	}
	provider := stringFlag(inv, "provider")
	if !inv.Changed("provider") {
		provider = os.Getenv(controlenv.AgentProviderVariable)
		if provider == "" {
			if ctxCfg, err := control.LoadContext(); err == nil && ctxCfg.Provider != "" {
				provider = ctxCfg.Provider
			}
		}
	}
	// Pass initial_message atomically in OpenAgent when there are no attachments
	// and --wait is not requested. When --attach or --wait is used, send via
	// EnqueueAgentInput so attachments and exact reply tracking work uniformly.
	var atomicInitialMessage *string
	postOpenMsg := false
	if msg != nil {
		if len(msg.attachments) == 0 && !wait {
			text := msg.text
			atomicInitialMessage = &text
		} else {
			postOpenMsg = true
		}
	}
	focus := shouldFocusNewTab(inv)
	var result *openAgentResult
	stage := retryStageBeforeSend
	err = withWorkerChannel(bounded.ctx, s.c, p.WorkerID, func(w workerCall) error {
		resolvedProvider, err := resolveProvider(bounded.ctx, provider, w.Call)
		if err != nil {
			return err
		}
		stage = retryStageDuringSend
		result, err = openAgentAndAddTab(bounded.ctx, s.c, w, openAgentArgs{
			WorkspaceID:    p.WorkspaceID,
			WorkerID:       p.WorkerID,
			TileID:         p.TileID,
			Provider:       resolvedProvider,
			Model:          stringFlag(inv, "model"),
			Effort:         stringFlag(inv, "effort"),
			Title:          title,
			PermissionMode: stringFlag(inv, "permission-mode"),
			Options:        opts,
			WorkingDir:     dir,
			InitialMessage: atomicInitialMessage,
			SessionID:      stringFlag(inv, "resume"),
			GitSetup:       setup,
			Position:       p.Position,
			RequestID:      requestID,
		})
		if err != nil {
			return err
		}
		if focus {
			_ = w.Call(bounded.ctx, leapmuxv1connect.WorkerPrivateServiceRevealTabProcedure, &leapmuxv1.RevealTabRequest{TabId: result.Agent.GetId()}, nil)
		}
		return nil
	})
	if err != nil {
		return nil, wrapRetryReceipt(err, stage, retryReceipt{
			RequestID:    requestID,
			Explicit:     explicitRequestID,
			RetryCommand: buildAgentOpenRetryCmd(inv, requestID),
		})
	}
	agentTarget := tabTarget{
		ID:          result.Agent.GetId(),
		Type:        leapmuxv1.TabType_TAB_TYPE_AGENT,
		WorkerID:    p.WorkerID,
		WorkspaceID: p.WorkspaceID,
		TileID:      result.TileID,
	}
	if postOpenMsg {
		inputID := requestScopedID(requestID, requestScopeAgentSend, agentTarget.ID)
		if !wait {
			if _, err := enqueueAgentInput(bounded.ctx, s.c, agentTarget, msg.request(agentTarget.ID, inputID)); err != nil {
				return nil, wrapRetryReceipt(err, retryStageDuringSend, retryReceipt{
					RequestID:    requestID,
					Explicit:     explicitRequestID,
					QueryCommand: buildAgentRequestStatusCmd(agentTarget.ID, requestID),
					RetryCommand: buildAgentOpenRetryCmd(inv, requestID),
				})
			}
		} else {
			var (
				replyRes agentReplyResult
				outcome  replyWaitOutcome
				wErr     error
			)
			if schemaSpec != nil {
				replyRes, outcome, _, wErr = sendAndWaitWithSchema(inv, s, agentTarget, *msg, requestID, inputID, sendDeliveryNormal, boolFlag(inv, "all"), timeout, schemaSpec)
			} else {
				replyRes, outcome, wErr = sendAndWait(s, agentTarget, *msg, inputID, boolFlag(inv, "all"), timeout)
			}
			if wErr != nil {
				return nil, wrapRetryReceipt(wErr, retryStageDuringWait, retryReceipt{
					RequestID:    requestID,
					Explicit:     explicitRequestID,
					QueryCommand: buildAgentRequestStatusCmd(agentTarget.ID, requestID),
					RetryCommand: buildAgentOpenRetryCmd(inv, requestID),
				})
			}
			replyRes.RequestID = requestID
			if outcome == replyWaitSettled {
				return replyRes, nil
			}
			return nil, agentReplyEnded(inv.Printer, replyRes, outcome, timeout)
		}
	} else if wait {
		return waitNewAgentSettled(inv, s, agentTarget, timeout, openedTab{
			ID:          result.Agent.GetId(),
			Type:        tabTypeName(leapmuxv1.TabType_TAB_TYPE_AGENT),
			Title:       result.Agent.GetTitle(),
			WorkspaceID: p.WorkspaceID,
			WorkerID:    p.WorkerID,
			TileID:      result.TileID,
			Position:    result.Position,
			RequestID:   requestID,
		})
	}
	return openedTab{
		ID:          result.Agent.GetId(),
		Type:        tabTypeName(leapmuxv1.TabType_TAB_TYPE_AGENT),
		Title:       result.Agent.GetTitle(),
		WorkspaceID: p.WorkspaceID,
		WorkerID:    p.WorkerID,
		TileID:      result.TileID,
		Position:    result.Position,
		RequestID:   requestID,
	}, nil
}

func waitNewAgentSettled(inv *cli.Invocation, s *session, agentTarget tabTarget, timeout time.Duration, tab openedTab) (any, error) {
	spec := agentWaitSpec{
		agentIDs: []string{agentTarget.ID},
		until:    waitConditions{waitUntilSettled},
		timeout:  timeout,
	}
	waitCtx, waitCancel := context.WithCancel(s.ctx)
	defer waitCancel()
	events := make(chan activityEvent, 16)
	startActivityFeed(waitCtx, workerActivitySource(s.c, agentTarget.WorkerID), []string{agentTarget.ID}, events)
	queues := &queueWatch{
		read: func(ctx context.Context, agentID string) (*leapmuxv1.AgentInputQueueSnapshot, error) {
			response, err := workerReplyReader(s.c, agentTarget.WorkerID, agentID).readQueue(ctx, "")
			return response.GetSnapshot(), err
		},
		notice: inv.Printer.Notice,
		now:    time.Now,
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
	wait := boolFlag(inv, "wait")
	var waitSpec terminalWaitSpec
	if !wait {
		for _, name := range []string{"exit", "text", "regex", "change", "after-offset", "scrollback", "timeout"} {
			if inv.Changed(name) {
				return nil, cli.Usagef("--%s needs --wait", name)
			}
		}
	} else {
		var err error
		waitSpec, err = terminalWaitSpecOf(inv)
		if err != nil {
			return nil, err
		}
	}
	setup, err := gitSetup(inv)
	if err != nil {
		return nil, err
	}
	initialCommand, err := initialCommandOf(inv)
	if err != nil {
		return nil, err
	}
	open := openSession
	if wait {
		open = openStreamSession
	}
	s, err := open(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	bounded := s
	if wait {
		bounded = s.bounded()
		defer bounded.Close()
	}
	p, err := bounded.placement()
	if err != nil {
		return nil, err
	}
	if err := bounded.requireWorker(p); err != nil {
		return nil, err
	}
	dir := workingDir(inv)
	if selector := setup.GetUseWorktree(); selector != nil {
		dir, err = bounded.worktreeRepoDir(p.WorkerID, selector)
		if err != nil {
			return nil, err
		}
	}
	if err := maybePreflightWorker(bounded.ctx, s.c, p.WorkerID); err != nil {
		return nil, err
	}
	focus := shouldFocusNewTab(inv)
	var result *openTerminalResult
	err = withWorkerChannel(bounded.ctx, s.c, p.WorkerID, func(w workerCall) error {
		var err error
		result, err = openTerminalAndAddTab(bounded.ctx, s.c, w, openTerminalArgs{
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
		if focus {
			_ = w.Call(bounded.ctx, leapmuxv1connect.WorkerPrivateServiceRevealTabProcedure, &leapmuxv1.RevealTabRequest{TabId: result.TerminalID}, nil)
		}
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
		Type:        tabTypeName(leapmuxv1.TabType_TAB_TYPE_TERMINAL),
		Title:       result.Title,
		WorkspaceID: p.WorkspaceID,
		WorkerID:    p.WorkerID,
		TileID:      result.TileID,
		Position:    result.Position,
	}, nil
}

// runFileOpen opens a file in a new viewer tab. The path travels to the
// Worker over the encrypted channel; the hub stores only the tab.
func runFileOpen(inv *cli.Invocation) (any, error) {
	if err := validateFocusFlags(inv); err != nil {
		return nil, err
	}
	filePath := inv.Arg("path")
	presentation := &leapmuxv1.TabPresentation{}
	if raw := stringFlag(inv, "display-mode"); raw != "" {
		n, ok := parseEnumToken(leapmuxv1.TabDisplayMode_TAB_DISPLAY_MODE_UNSPECIFIED, raw)
		if !ok {
			return nil, cli.Usagef("--display-mode must be one of %s; got %q", strings.Join(enumTokens(leapmuxv1.TabDisplayMode_TAB_DISPLAY_MODE_UNSPECIFIED), ", "), raw)
		}
		presentation.DisplayMode = leapmuxv1.TabDisplayMode(n).Enum()
	}
	if raw := stringFlag(inv, "view-mode"); raw != "" {
		n, ok := parseEnumToken(leapmuxv1.TabFileViewMode_TAB_FILE_VIEW_MODE_UNSPECIFIED, raw)
		if !ok {
			return nil, cli.Usagef("--view-mode must be one of %s; got %q", strings.Join(enumTokens(leapmuxv1.TabFileViewMode_TAB_FILE_VIEW_MODE_UNSPECIFIED), ", "), raw)
		}
		presentation.FileViewMode = leapmuxv1.TabFileViewMode(n).Enum()
	}
	if inv.Changed("diff-base") {
		diffBase := stringFlag(inv, "diff-base")
		presentation.FileDiffBase = &diffBase
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
	// --first and --last need a tile to be first or last on: the placement's
	// tile, else the first tile of the workspace. Relative insertion derives
	// the tile from the reference tab instead.
	tileID := p.TileID
	if tileID == "" && (p.Position.kind == positionFirst || p.Position.kind == positionLast) {
		ws, ok := cc.bs.State.GetWorkspaces()[p.WorkspaceID]
		if !ok || ws == nil {
			return nil, cli.Newf(cli.CodeNotFound, "no such workspace: %s", p.WorkspaceID)
		}
		tileID = firstLiveLeaf(cc.bs.State, ws.GetRootNodeId())
	}
	resolvedTileID, _, err := resolvePositionSpec(cc.bs.State, tileID, "", p.Position)
	if err != nil {
		return nil, err
	}
	if err := preflightTile(cc.bs.State, p.WorkspaceID, resolvedTileID); err != nil {
		return nil, err
	}
	if err := maybePreflightWorker(cc.ctx, cc.c, p.WorkerID); err != nil {
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
	publication, err := registerFileTabPayload(cc.ctx, cc.c, workerID, creation, filePath, workingDir(inv))
	if err != nil {
		return nil, err
	}
	if shouldFocusNewTab(inv) {
		_ = callInnerRPCBest(cc.ctx, cc.c, workerID, leapmuxv1connect.WorkerPrivateServiceRevealTabProcedure, &leapmuxv1.RevealTabRequest{TabId: creation.GetTabId()}, nil)
	}
	return openedTab{
		ID:          creation.GetTabId(),
		Type:        tabTypeName(leapmuxv1.TabType_TAB_TYPE_FILE),
		WorkspaceID: p.WorkspaceID,
		WorkerID:    workerID,
		TileID:      publication.GetTileId(),
		Position:    publication.GetPosition(),
		Path:        filePath,
	}, nil
}

// registerFileTabPayload keeps the path encrypted and returns the committed tab.
func registerFileTabPayload(ctx context.Context, c *control.Client, workerID string, creation *leapmuxv1.TabCreation, filePath, workingDir string) (*leapmuxv1.PublishedTab, error) {
	request := &leapmuxv1.RegisterTabPayloadRequest{Creation: creation, Payload: &leapmuxv1.TabPayload{WorkingDir: workingDir, Kind: &leapmuxv1.TabPayload_File{File: &leapmuxv1.FileTabPayload{FilePath: filePath}}}}
	var response leapmuxv1.RegisterTabPayloadResponse
	if err := callInnerRPCBest(ctx, c, workerID, leapmuxv1connect.WorkerPrivateServiceRegisterTabPayloadProcedure, request, &response); err != nil {
		return nil, err
	}
	if err := validateTabPublication(creation, leapmuxv1.TabType_TAB_TYPE_FILE, response.GetPublication()); err != nil {
		return nil, err
	}
	return response.GetPublication(), nil
}

// findWorkerForWorkspaceInState returns the first worker_id hosting a
// live tab in the given workspace, reading from the already-bootstrapped
// UserMaterialized state. Used by `file open` when neither --worker nor the
// tab that runs the command selects a worker — a workspace that has at least
// one live tab tells us which worker owns its filesystem context.
// Returns "" when no live worker is on file.
func findWorkerForWorkspaceInState(state *leapmuxv1.UserMaterialized, workspaceID string) string {
	for _, t := range state.GetTabs() {
		if t == nil || !crdt.HLCIsZero(t.GetTombstoneAt()) {
			continue
		}
		w := t.GetWorkerId().GetValue()
		if w == "" {
			continue
		}
		ws := crdt.FindRootWorkspace(state.GetNodes(), state.GetWorkspaces(), t.GetTileId().GetValue())
		if ws == workspaceID {
			return w
		}
	}
	return ""
}
