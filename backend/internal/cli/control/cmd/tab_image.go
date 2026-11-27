package cmd

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/util/id"
)

func newTabOpenImageLeaf() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:     "open-image <agent> <seq>",
		Short:   "Open an image from an agent message in its own tab",
		Mutates: true,
		Args: map[string]string{
			"seq": "Positive message sequence number in the agent's transcript.",
		},
		Examples: []cli.Example{
			{Command: "leapmux tab open-image 87a19d5f 42"},
			{Command: `leapmux tab open-image 87a19d5f 42 --index 1 --title "Homepage screenshot"`},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.Int32("index", 0, "0-based image index within the message")
			fs.String("title", "", "tab title (default: Image #<seq>)")
			for _, name := range []string{"index", "title"} {
				cli.SetFlagGroup(fs, name, "Image")
			}
			addWorkingDirFlag(fs, "working directory context of the tab")
			cli.SetFlagGroup(fs, "dir", "Git & Directory")
			addPlacementFlags(fs)
		},
		FlagComplete: tabPositionFlagCompletions(),
		Complete:     completeTargets,
		Run:          runTabOpenImage,
	})
}

func runTabOpenImage(inv *cli.Invocation) (any, error) {
	if err := validateFocusFlags(inv); err != nil {
		return nil, err
	}
	seq, err := strconv.ParseInt(strings.TrimSpace(inv.Arg("seq")), 10, 64)
	if err != nil || seq <= 0 {
		return nil, cli.Usagef("<seq> must be a positive integer; got %q", inv.Arg("seq"))
	}
	imgIdx, err := inv.Command.Flags().GetInt32("index")
	if err != nil || imgIdx < 0 {
		return nil, cli.Usagef("--index must be 0 or greater")
	}
	title := strings.TrimSpace(stringFlag(inv, "title"))
	if title == "" {
		title = fmt.Sprintf("Image #%d", seq)
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	agentTarget, err := s.resolveTab(resolve.KindAgent, inv.Arg("agent"), resolve.Need{WorkerID: true, Want: resolve.Wants{WorkspaceID: true, TileID: true}})
	if err != nil {
		return nil, err
	}
	p, err := s.placement()
	if err != nil {
		if agentTarget.WorkspaceID == "" {
			return nil, err
		}
		spec, specErr := s.positionSpec()
		if specErr != nil {
			return nil, specErr
		}
		p = placement{
			WorkspaceID: agentTarget.WorkspaceID,
			TileID:      agentTarget.TileID,
			WorkerID:    agentTarget.WorkerID,
			Position:    spec,
		}
	}
	// ImageTabPayload references an agent transcript stored on agentTarget's
	// Worker, so the payload must always be registered on that Worker.
	workerID := agentTarget.WorkerID
	if workerID == "" {
		workerID = p.WorkerID
	}
	cc, err := s.openCRDT(p.WorkspaceID)
	if err != nil {
		return nil, err
	}
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
	if err := maybePreflightWorker(cc.ctx, cc.c, workerID); err != nil {
		return nil, err
	}
	creation, err := resolveTabCreation(cc.ctx, cc.c, id.Generate(), p.WorkspaceID, resolvedTileID, p.Position)
	if err != nil {
		return nil, err
	}
	req := &leapmuxv1.RegisterTabPayloadRequest{
		Creation: creation,
		Payload: &leapmuxv1.TabPayload{
			WorkingDir: workingDir(inv),
			Kind: &leapmuxv1.TabPayload_Image{
				Image: &leapmuxv1.ImageTabPayload{
					AgentId:    agentTarget.ID,
					Seq:        seq,
					ImageIndex: imgIdx,
					Title:      title,
				},
			},
		},
	}
	var resp leapmuxv1.RegisterTabPayloadResponse
	if err := callInnerRPCBest(cc.ctx, cc.c, workerID, leapmuxv1connect.WorkerPrivateServiceRegisterTabPayloadProcedure, req, &resp); err != nil {
		return nil, err
	}
	if err := validateTabPublication(creation, leapmuxv1.TabType_TAB_TYPE_IMAGE, resp.GetPublication()); err != nil {
		return nil, err
	}
	if shouldFocusNewTab(inv) {
		_ = callInnerRPCBest(cc.ctx, cc.c, workerID, leapmuxv1connect.WorkerPrivateServiceRevealTabProcedure, &leapmuxv1.RevealTabRequest{TabId: creation.GetTabId()}, nil)
	}
	return openedTab{
		ID:          creation.GetTabId(),
		Type:        tabTypeName(leapmuxv1.TabType_TAB_TYPE_IMAGE),
		Title:       title,
		WorkspaceID: p.WorkspaceID,
		WorkerID:    workerID,
		TileID:      resp.GetPublication().GetTileId(),
		Position:    resp.GetPublication().GetPosition(),
	}, nil
}
