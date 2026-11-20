package cmd

import (
	"context"
	"os"
	"strings"

	"github.com/spf13/pflag"

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
	}
	if in.TileID == "" && in.WorkspaceID == "" {
		in.TabID = strings.TrimSpace(os.Getenv(controlenv.TabIDVariable))
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
func (p placement) requireWorker() error {
	if p.WorkerID == "" {
		return cli.Usagef("no worker selected").
			WithHint("pass --worker <worker>, or run the command inside a LeapMux tab")
	}
	return nil
}

// addGitSetupFlags registers how a new agent or terminal prepares its
// working directory. At most one of the four modes applies.
func addGitSetupFlags(fs *pflag.FlagSet) {
	fs.String("worktree", "", "create a git worktree on this new branch and start in it")
	fs.String("use-worktree", "", "start in an existing git worktree by path, path:<path>, or branch:<branch>")
	fs.String("checkout", "", "check out this existing branch first")
	fs.String("new-branch", "", "create this branch and check it out first")
	fs.String("base", "", "the branch that --worktree or --new-branch starts from (default: the current branch)")
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
}

// runAgentOpen starts a new agent in a new tab with an optional first message.
func runAgentOpen(inv *cli.Invocation) (any, error) {
	var initialMessage *string
	if inv.Changed("message") {
		text := stringFlag(inv, "message")
		if strings.TrimSpace(text) == "" {
			return nil, cli.Usagef("--message is empty")
		}
		initialMessage = &text
	}
	setup, err := gitSetup(inv)
	if err != nil {
		return nil, err
	}
	requestID, err := requestIDOf(inv)
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
	if err := p.requireWorker(); err != nil {
		return nil, err
	}
	dir := workingDir(inv)
	if selector := setup.GetUseWorktree(); selector != nil {
		dir, err = s.worktreeRepoDir(p.WorkerID, selector)
		if err != nil {
			return nil, err
		}
	}
	if err := maybePreflightWorker(s.ctx, s.c, p.WorkerID); err != nil {
		return nil, err
	}
	provider := stringFlag(inv, "provider")
	if !inv.Changed("provider") {
		provider = os.Getenv(controlenv.AgentProviderVariable)
	}
	var result *openAgentResult
	err = withWorkerChannel(s.ctx, s.c, p.WorkerID, func(w workerCall) error {
		resolvedProvider, err := resolveProvider(s.ctx, provider, w.Call)
		if err != nil {
			return err
		}
		result, err = openAgentAndAddTab(s.ctx, s.c, w, openAgentArgs{
			WorkspaceID:    p.WorkspaceID,
			WorkerID:       p.WorkerID,
			TileID:         p.TileID,
			Provider:       resolvedProvider,
			Model:          stringFlag(inv, "model"),
			Effort:         stringFlag(inv, "effort"),
			Title:          stringFlag(inv, "title"),
			PermissionMode: stringFlag(inv, "permission-mode"),
			WorkingDir:     dir,
			InitialMessage: initialMessage,
			SessionID:      stringFlag(inv, "resume"),
			GitSetup:       setup,
			Position:       p.Position,
			RequestID:      requestID,
		})
		return err
	})
	if err != nil {
		return nil, withRequestIDHint(err, requestID)
	}
	return openedTab{
		ID:          result.Agent.GetId(),
		Type:        tabTypeName(leapmuxv1.TabType_TAB_TYPE_AGENT),
		Title:       result.Agent.GetTitle(),
		WorkspaceID: p.WorkspaceID,
		WorkerID:    p.WorkerID,
		TileID:      result.TileID,
		Position:    result.Position,
	}, nil
}

// runTerminalOpen starts a new shell in a new tab, with the same channel
// ownership as `agent open`.
func runTerminalOpen(inv *cli.Invocation) (any, error) {
	setup, err := gitSetup(inv)
	if err != nil {
		return nil, err
	}
	initialCommand, err := initialCommandOf(inv)
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
	if err := p.requireWorker(); err != nil {
		return nil, err
	}
	dir := workingDir(inv)
	if selector := setup.GetUseWorktree(); selector != nil {
		dir, err = s.worktreeRepoDir(p.WorkerID, selector)
		if err != nil {
			return nil, err
		}
	}
	if err := maybePreflightWorker(s.ctx, s.c, p.WorkerID); err != nil {
		return nil, err
	}
	var result *openTerminalResult
	err = withWorkerChannel(s.ctx, s.c, p.WorkerID, func(w workerCall) error {
		var err error
		result, err = openTerminalAndAddTab(s.ctx, s.c, w, openTerminalArgs{
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
		return err
	})
	if err != nil {
		return nil, err
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
