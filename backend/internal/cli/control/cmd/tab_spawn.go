package cmd

import (
	"context"
	"errors"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"google.golang.org/protobuf/types/known/emptypb"
)

// openAgentArgs is the resolved input to openAgentAndAddTab. All
// fields are set by the caller after env-default + tile→workspace
// derivation; the helper does not consult os.Getenv itself.
type openAgentArgs struct {
	WorkspaceID string
	WorkerID    string
	TileID      string
	Provider    leapmuxv1.AgentProvider
	Title       string
	// Options holds the initial provider option map (model, effort,
	// permission mode, and provider-specific axes) applied at launch.
	Options        map[string]string
	WorkingDir     string
	InitialMessage *string
	// SessionID resumes the agent's earlier session (`--resume`).
	SessionID string
	// ForkSource identifies the source agent whose conversation history is forked.
	ForkSource *leapmuxv1.AgentForkSource
	// GitSetup prepares the working directory; nil uses it as it is.
	GitSetup *leapmuxv1.GitSetup
	// Position spec captures the caller's --first / --last /
	// --before / --after intent. The Hub resolves its committed rank.
	Position positionSpec
	// RequestID derives the tab ID so the Worker can recognize a retry.
	RequestID string
	// RequestScope scopes RequestID derivation; empty defaults to requestScopeAgentOpen.
	RequestScope string
}

// spawnOptions builds the OpenAgent initial option map from the CLI's --model / --effort /
// --permission-mode flags, omitting empty values so the worker fills provider defaults. The
// permission mode rides in here -- applied at LAUNCH alongside model/effort (resolveProviderDefaults
// for the catalog providers, the startup permission-mode apply for ACP) -- rather than via a
// redundant post-spawn UpdateAgentSettings that would re-set the already-applied mode (and, for a
// provider that applies the mode via restart, force a spawn-time relaunch). This treats permission
// mode uniformly with every other axis, closing the last special-cased seam in the spawn path.
func spawnOptions(model, effort, permissionMode string) map[string]string {
	options := map[string]string{}
	if model != "" {
		options[optionids.Model] = model
	}
	if effort != "" {
		options[optionids.Effort] = effort
	}
	if permissionMode != "" {
		options[optionids.PermissionMode] = permissionMode
	}
	return options
}

// openAgentResult is what openAgentAndAddTab emits on success.
type openAgentResult struct {
	Agent    *leapmuxv1.AgentInfo
	TileID   string
	Position string
}

// resolveTabCreation captures placement before the Worker admits creation.
// Only a reference without an explicit tile needs a bootstrap to find its tile.
// The Hub validates the intent and calculates its canonical rank at commit.
//
// tabID is the id of the new tab. The Worker takes a repeated creation of one
// tab id as the same creation, so the caller chooses it: a fresh id, or one
// that a request id derives.
func resolveTabCreation(ctx context.Context, c control.Peer, tabID, workspaceID, tileID string, spec positionSpec) (*leapmuxv1.TabCreation, error) {
	placement := &leapmuxv1.TabPlacement{WorkspaceId: workspaceID, RelativeTabId: spec.refID}
	switch spec.kind {
	case positionFirst:
		placement.Insertion = leapmuxv1.TabInsertion_TAB_INSERTION_FIRST
	case positionLast:
		placement.Insertion = leapmuxv1.TabInsertion_TAB_INSERTION_LAST
	case positionBefore, positionAfter:
		if spec.refID == "" {
			return nil, cli.Usagef("%s needs a tab", flagLabelForPositionKind(spec.kind))
		}
		placement.Insertion = leapmuxv1.TabInsertion_TAB_INSERTION_BEFORE
		if spec.kind == positionAfter {
			placement.Insertion = leapmuxv1.TabInsertion_TAB_INSERTION_AFTER
		}
		if tileID == "" {
			bootstrap, err := crdtBootstrap(ctx, c, []string{workspaceID})
			if err != nil {
				return nil, err
			}
			if err := preflightTab(bootstrap.State, workspaceID, spec.refID, leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED); err != nil {
				return nil, err
			}
			tileID = bootstrap.State.GetTabs()[spec.refID].GetTileId().GetValue()
		}
	default:
		return nil, cli.Usagef("unknown position kind")
	}
	if tileID == "" {
		placement.Destination = &leapmuxv1.TabPlacement_FirstLeaf{FirstLeaf: &emptypb.Empty{}}
	} else {
		placement.Destination = &leapmuxv1.TabPlacement_TileId{TileId: tileID}
	}
	return &leapmuxv1.TabCreation{TabId: tabID, Placement: placement}, nil
}

// validateTabPublication checks the result before the CLI reports a usable tab.
func validateTabPublication(creation *leapmuxv1.TabCreation, kind leapmuxv1.TabType, publication *leapmuxv1.PublishedTab) error {
	if publication.GetTab().GetTabId() != creation.GetTabId() || publication.GetTab().GetTabType() != kind || publication.GetWorkspaceId() != creation.GetPlacement().GetWorkspaceId() {
		return errors.New("the Worker returned a publication for a different tab")
	}
	if publication.GetTileId() == "" || publication.GetPosition() == "" {
		return errors.New("the Worker returned an incomplete tab publication")
	}
	return nil
}

// openAgentAndAddTab asks the Worker to create and publish an agent. A successful
// reply supplies the committed placement. The Worker commits any initial message
// with the agent, so a retry cannot change it or queue it twice.
func openAgentAndAddTab(ctx context.Context, c control.Peer, w workerCall, args openAgentArgs) (*openAgentResult, error) {
	if args.WorkspaceID == "" || args.WorkerID == "" {
		return nil, cli.Usagef("an agent needs a workspace and a worker")
	}
	scope := args.RequestScope
	if scope == "" {
		scope = requestScopeAgentOpen
	}
	creation, err := resolveTabCreation(ctx, c, requestScopedID(args.RequestID, scope, args.WorkerID), args.WorkspaceID, args.TileID, args.Position)
	if err != nil {
		return nil, err
	}
	opts := args.Options
	if opts == nil {
		opts = map[string]string{}
	}
	request := &leapmuxv1.OpenAgentRequest{
		Creation:       creation,
		AgentProvider:  args.Provider,
		Options:        opts,
		Title:          args.Title,
		WorkingDir:     args.WorkingDir,
		AgentSessionId: args.SessionID,
		ForkSource:     args.ForkSource,
		GitSetup:       args.GitSetup,
		InitialMessage: args.InitialMessage,
	}
	if args.InitialMessage != nil && strings.TrimSpace(args.RequestID) != "" {
		initMsgID := requestScopedID(args.RequestID, requestScopeAgentSend, creation.GetTabId())
		request.InitialMessageId = &initMsgID
	}
	var response leapmuxv1.OpenAgentResponse
	if err := w.Call(ctx, leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure, request, &response); err != nil {
		return nil, err
	}
	if err := validateTabPublication(creation, leapmuxv1.TabType_TAB_TYPE_AGENT, response.GetPublication()); err != nil {
		return nil, err
	}
	if response.GetAgent().GetId() != creation.GetTabId() || response.GetAgent().GetWorkerId() != args.WorkerID {
		return nil, cli.Newf(cli.CodeInternal, "the Worker returned a different agent identity")
	}
	return &openAgentResult{Agent: response.GetAgent(), TileID: response.GetPublication().GetTileId(), Position: response.GetPublication().GetPosition()}, nil
}

// openTerminalArgs mirrors openAgentArgs for terminal spawns. Shell
// and ShellStartDir are terminal-only. The PTY's initial dimensions
// are not exposed at the CLI: the worker defaults to 80x25 and the
// frontend immediately resizes once the user attaches, so any caller-
// supplied value would be overwritten in milliseconds.
type openTerminalArgs struct {
	WorkspaceID   string
	WorkerID      string
	TileID        string
	WorkingDir    string
	Shell         string
	ShellStartDir string
	// Title is optional, and empty means "you pick one": the worker then takes
	// a random `Terminal <Name>` from the shared pool, the same answer it gives
	// the quick-open buttons. It reaches the same field OpenAgentRequest has, so
	// `--title` means one thing for both tab types.
	Title string
	// GitSetup prepares the working directory; nil uses it as it is.
	GitSetup *leapmuxv1.GitSetup
	// InitialCommand is the command line that the new shell runs once it is
	// ready (`--command`). Empty runs nothing.
	InitialCommand string
	Position       positionSpec
}

// terminalOpenRequest carries one immutable ordinary-tab creation intent.
// Dimensions remain zero so the Worker applies its default before attachment.
func terminalOpenRequest(args openTerminalArgs, creation *leapmuxv1.TabCreation) *leapmuxv1.OpenTerminalRequest {
	return &leapmuxv1.OpenTerminalRequest{WorkingDir: args.WorkingDir, Shell: args.Shell, ShellStartDir: args.ShellStartDir, Title: args.Title, GitSetup: args.GitSetup, InitialCommand: args.InitialCommand, Target: &leapmuxv1.OpenTerminalRequest_Creation{Creation: creation}}
}

type openTerminalResult struct {
	TerminalID string
	Title      string
	TileID     string
	Position   string
}

// openTerminalAndAddTab returns the placement committed by the Worker operation.
func openTerminalAndAddTab(ctx context.Context, c control.Peer, w workerCall, args openTerminalArgs) (*openTerminalResult, error) {
	if args.WorkspaceID == "" || args.WorkerID == "" {
		return nil, cli.Usagef("a terminal needs a workspace and a worker")
	}
	creation, err := resolveTabCreation(ctx, c, id.Generate(), args.WorkspaceID, args.TileID, args.Position)
	if err != nil {
		return nil, err
	}
	var response leapmuxv1.OpenTerminalResponse
	if err := w.Call(ctx, leapmuxv1connect.WorkerTerminalServiceOpenTerminalProcedure, terminalOpenRequest(args, creation), &response); err != nil {
		return nil, err
	}
	if err := validateTabPublication(creation, leapmuxv1.TabType_TAB_TYPE_TERMINAL, response.GetPublication()); err != nil {
		return nil, err
	}
	if response.GetTerminalId() != creation.GetTabId() {
		return nil, cli.Newf(cli.CodeInternal, "the Worker returned a different terminal identity")
	}
	return &openTerminalResult{TerminalID: response.GetTerminalId(), Title: response.GetTitle(), TileID: response.GetPublication().GetTileId(), Position: response.GetPublication().GetPosition()}, nil
}
