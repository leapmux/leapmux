package cmd

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/sync/errgroup"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
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
	p, err := s.placementWithDefaults(placementDefaults{
		WorkspaceID: agentTarget.WorkspaceID,
		TileID:      agentTarget.TileID,
		WorkerID:    agentTarget.WorkerID,
	})
	if err != nil {
		return nil, err
	}
	// ImageTabPayload refers to an agent transcript stored on agentTarget's
	// Worker, so the payload must always be registered on that Worker.
	workerID := agentTarget.WorkerID
	if workerID == "" {
		workerID = p.WorkerID
	}

	var cc *crdtCall
	g, gctx := errgroup.WithContext(s.ctx)
	g.Go(func() error {
		var crdtErr error
		cc, crdtErr = s.openCRDT(p.WorkspaceID)
		return crdtErr
	})
	g.Go(func() error {
		return maybePreflightWorker(gctx, s.c, workerID)
	})
	if err := g.Wait(); err != nil {
		return nil, err
	}

	resolvedTileID, err := resolvePlacementTile(cc.bs.State, p)
	if err != nil {
		return nil, err
	}
	if err := preflightTile(cc.bs.State, p.WorkspaceID, resolvedTileID); err != nil {
		return nil, err
	}
	creation, err := resolveTabCreation(cc.ctx, cc.c, id.Generate(), p.WorkspaceID, resolvedTileID, p.Position)
	if err != nil {
		return nil, err
	}
	payload := &leapmuxv1.TabPayload{
		WorkingDir: workingDir(inv),
		Kind: &leapmuxv1.TabPayload_Image{
			Image: &leapmuxv1.ImageTabPayload{
				AgentId:    agentTarget.ID,
				Seq:        seq,
				ImageIndex: imgIdx,
				Title:      title,
			},
		},
	}
	focus := shouldFocusNewTab(inv)
	var resp *leapmuxv1.RegisterTabPayloadResponse
	err = withWorkerChannel(cc.ctx, cc.c, workerID, func(w workerCall) error {
		var regErr error
		resp, regErr = registerTabPayload(cc.ctx, w, leapmuxv1.TabType_TAB_TYPE_IMAGE, creation, payload, focus)
		return regErr
	})
	if err != nil {
		return nil, err
	}
	return openedTab{
		ID:          creation.GetTabId(),
		Type:        resolve.TabTypeWireName(leapmuxv1.TabType_TAB_TYPE_IMAGE),
		Title:       title,
		WorkspaceID: p.WorkspaceID,
		WorkerID:    workerID,
		TileID:      resp.GetPublication().GetTileId(),
		Position:    resp.GetPublication().GetPosition(),
	}, nil
}
