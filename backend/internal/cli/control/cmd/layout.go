package cmd

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

// Layout / tile-tree mutation commands. Every mutator follows the
// projection-driven CRDT contract:
//
//  1. Bootstrap the user CRDT via a unary GetMaterialized call.
//  2. Resolve parent_id chains from the materialized state when the op
//     batch needs them (e.g. replaceGridWithLeaf inherits the grid's
//     parent).
//  3. Mint fresh node ids for every new entity.
//  4. Submit a single OpBatch — atomic per-batch commit/reject.
//
// The in-place mutation model (T flips kind LEAF → SPLIT / GRID, two
// new children minted) matches the frontend layout-store helpers, so
// CLI-driven mutations converge with browser-driven mutations under
// the same plan-aligned op shape.

func newTileCommand() *cobra.Command {
	withTabsUsage := `what happens to the tabs on the tile: "close" closes them; "move" moves them to the neighbouring tile; required when the tile has tabs`
	return cli.Group("tile", "Split, close and arrange the tiles of a workspace",
		cli.NewLeaf(cli.Leaf{
			Use:   "list [workspace]",
			Short: "Print the tile tree of a workspace (default: the workspace of this tab)",
			Examples: []cli.Example{
				{Command: "leapmux control tile list"},
			},
			Complete: completeTargets,
			Run: func(inv *cli.Invocation) (any, error) {
				return runLayoutTree(inv, false)
			},
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "split <tile>",
			Short:   "Split a tile in two; its tabs stay in the first half",
			Mutates: true,
			Examples: []cli.Example{
				{Comment: "Split the tile of this tab, with the new tile on the right.", Command: "leapmux control tile split self"},
				{Comment: "Split a tile with the new tile below.", Command: "leapmux control tile split ab12cd34 --direction horizontal"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.String("direction", "vertical", `the divider: "vertical" puts the new tile on the right, "horizontal" puts it below`)
				cli.SetEnum(fs, "direction", "vertical", "horizontal")
			},
			Complete: completeTargets,
			Run:      runTileSplit,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "close <tile>",
			Short:   "Close a tile and give its space to its neighbour",
			Mutates: true,
			Examples: []cli.Example{
				{Command: "leapmux control tile close ab12cd34"},
				{Comment: "Close a tile and move its tabs to the neighbouring tile.", Command: "leapmux control tile close ab12cd34 --with-tabs move"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.String("with-tabs", "", withTabsUsage)
				cli.SetEnum(fs, "with-tabs", "close", "move")
				fs.Bool("recursive", false, "close a split or a grid with every tile in it")
				fs.Bool("allow-busy", false, allowBusyFlagHelp)
				fs.Bool(allowSelfFlag, false, "close the tile even when this tab sits on it, which ends this command's own process")
			},
			Complete: completeTargets,
			Run:      runTileClose,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "make-grid <tile>",
			Short:   "Turn a tile into a grid of rows and columns",
			Mutates: true,
			Examples: []cli.Example{
				{Command: "leapmux control tile make-grid ab12cd34 --rows 2 --cols 2"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.Int("rows", 0, fmt.Sprintf("rows of the grid, from 1 to %d", crdt.MaxGridDimension))
				fs.Int("cols", 0, fmt.Sprintf("columns of the grid, from 1 to %d", crdt.MaxGridDimension))
				cli.MarkRequired(fs, "rows")
				cli.MarkRequired(fs, "cols")
			},
			Complete: completeTargets,
			Run:      runTileMakeGrid,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "remove-grid <tile>",
			Short:   "Remove a grid and every tile in it",
			Mutates: true,
			Examples: []cli.Example{
				{Comment: "Turn a grid back into one tile that keeps its tabs.", Command: "leapmux control tile remove-grid ab12cd34 --with-tabs move"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.String("with-tabs", "", `what happens to the tabs in the grid: "close" closes them; "move" turns the grid into one tile that holds them; required when the grid has tabs`)
				cli.SetEnum(fs, "with-tabs", "close", "move")
				fs.Bool("allow-busy", false, allowBusyFlagHelp)
				fs.Bool(allowSelfFlag, false, "remove the grid even when this tab sits in it, which ends this command's own process")
			},
			Complete: completeTargets,
			Run:      runTileRemoveGrid,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "set-ratios <tile>",
			Short:   "Set the sizes of the parts of a split",
			Mutates: true,
			Examples: []cli.Example{
				{Command: "leapmux control tile set-ratios ab12cd34 --ratios 0.3,0.7"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.String("ratios", "", "the size of each part, separated by commas; the CLI scales them to sum to 1")
				cli.MarkRequired(fs, "ratios")
			},
			Complete: completeTargets,
			Run:      runTileSetRatios,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "set-grid-ratios <tile>",
			Short:   "Set the sizes of the rows or the columns of a grid",
			Mutates: true,
			Examples: []cli.Example{
				{Command: "leapmux control tile set-grid-ratios ab12cd34 --row-ratios 0.6,0.4"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.String("row-ratios", "", "the size of each row, separated by commas")
				fs.String("col-ratios", "", "the size of each column, separated by commas")
			},
			Complete: completeTargets,
			Run:      runTileSetGridRatios,
		}),
	)
}

func newLayoutCommand() *cobra.Command {
	return cli.Group("layout", "Read, replace and watch the whole tile tree of a workspace",
		cli.NewLeaf(cli.Leaf{
			Use:   "get [workspace]",
			Short: "Print the tile tree of a workspace with its tabs (default: the workspace of this tab)",
			Examples: []cli.Example{
				{Comment: "Save a layout, and restore it later.", Command: "leapmux control layout get --json > layout.json"},
			},
			Complete: completeTargets,
			Run: func(inv *cli.Invocation) (any, error) {
				return runLayoutTree(inv, true)
			},
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "set <workspace> [file]",
			Short:   "Replace the tile tree of a workspace; every tab moves to its first tile",
			Mutates: true,
			Long: `Replace the tile tree of a workspace. The file is "-" or absent for stdin. It
holds a tree as "layout get" prints it: the whole --json envelope, its data,
or the tree alone.`,
			Examples: []cli.Example{
				{Command: "leapmux control layout set self layout.json"},
				{Command: "leapmux control layout get --json | leapmux control layout set 9f3kQ2aB -"},
			},
			Complete: completeTargets,
			Run:      runLayoutSet,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:   "watch [workspace]",
			Short: "Print every change of your layouts as it happens, one event per line",
			Long: `Print every change of your layouts as it happens: the CRDT operations, the
presence of other clients, and the creation, renaming and deletion of
workspaces. The first event is the snapshot. Without a workspace, it watches
every workspace.`,
			Examples: []cli.Example{
				{Command: "leapmux control layout watch --json"},
			},
			Complete: completeTargets,
			Run:      runLayoutWatch,
		}),
	)
}

// openTile resolves the tile target input, bootstraps the CRDT of its
// workspace, and checks that the tile is live there. It returns the open
// CRDT call, the workspace id and the tile id.
func (s *session) openTile(input string) (cc *crdtCall, workspaceID, tileID string, err error) {
	target, err := s.resolveOne(resolve.KindTile, input)
	if err != nil {
		return nil, "", "", err
	}
	got, err := resolve.Resolve(s.ctx, s.deps(), resolve.Need{WorkspaceID: true, TileID: true}, resolve.Inputs{TileID: target.ID, ExplicitTileID: true})
	if err != nil {
		return nil, "", "", err
	}
	cc, err = s.openCRDT(got.WorkspaceID)
	if err != nil {
		return nil, "", "", err
	}
	if err := preflightTile(cc.bs.State, got.WorkspaceID, got.TileID); err != nil {
		return nil, "", "", err
	}
	return cc, got.WorkspaceID, got.TileID, nil
}

// parseSplitDirection maps the user-facing direction string to the
// proto enum. "vertical" / "v" -> SPLIT_DIRECTION_VERTICAL (a
// vertical divider line `|` between two side-by-side panes);
// "horizontal" / "h" -> SPLIT_DIRECTION_HORIZONTAL (a horizontal
// divider line `-` between two stacked panes). The mapping is
// direct — both the CLI flag and the proto enum use the
// divider-line convention.
func parseSplitDirection(s string) (leapmuxv1.SplitDirection, bool) {
	switch s {
	case "horizontal", "h":
		return leapmuxv1.SplitDirection_SPLIT_DIRECTION_HORIZONTAL, true
	case "vertical", "v":
		return leapmuxv1.SplitDirection_SPLIT_DIRECTION_VERTICAL, true
	}
	return leapmuxv1.SplitDirection_SPLIT_DIRECTION_UNSPECIFIED, false
}

// layoutNode is one node of a layout read or submitted to the CLI.
type layoutNode struct {
	// NodeID identifies the source node. layout set retains the workspace root ID.
	NodeID    string        `json:"node_id,omitempty"`
	Kind      string        `json:"kind"`
	Direction string        `json:"direction,omitempty"`
	Ratios    []float64     `json:"ratios,omitempty"`
	Rows      uint32        `json:"rows,omitempty"`
	Cols      uint32        `json:"cols,omitempty"`
	RowRatios []float64     `json:"row_ratios,omitempty"`
	ColRatios []float64     `json:"col_ratios,omitempty"`
	Children  []*layoutNode `json:"children,omitempty"`
}

// layoutTree is the result of `tile list` and `layout get`.
type layoutTree struct {
	WorkspaceID string      `json:"workspace_id"`
	RootNodeID  string      `json:"root_node_id"`
	Tree        *layoutNode `json:"tree"`
	// TabsByTile maps each leaf tile to its live tabs. Only `layout get`
	// prints it.
	TabsByTile map[string][]layoutTab `json:"tabs_by_tile,omitempty"`
}

// RenderText prints the tree indented, one tile per line, with the count
// of tabs on each leaf when the tree carries them.
func (l layoutTree) RenderText(w io.Writer) error {
	if _, err := fmt.Fprintf(w, "workspace %s\n", l.WorkspaceID); err != nil {
		return err
	}
	return l.renderNode(w, l.Tree, "  ")
}

func (l layoutTree) renderNode(w io.Writer, node *layoutNode, indent string) error {
	if node == nil {
		return nil
	}
	line := indent + node.Kind
	if node.NodeID != "" {
		line += " " + node.NodeID
	}
	switch node.Kind {
	case "split":
		line += fmt.Sprintf(" %v %v", node.Direction, node.Ratios)
	case "grid":
		line += fmt.Sprintf(" %vx%v", node.Rows, node.Cols)
	}
	if tabs := l.TabsByTile[node.NodeID]; len(tabs) > 0 {
		line += " " + pluralize(len(tabs), "tab")
	}
	if _, err := fmt.Fprintln(w, line); err != nil {
		return err
	}
	for _, child := range node.Children {
		if err := l.renderNode(w, child, indent+"  "); err != nil {
			return err
		}
	}
	return nil
}

// layoutTab is one tab of a tile in a layout.
type layoutTab struct {
	ID   string `json:"id" table:"ID,short"`
	Type string `json:"type" table:"TYPE"`
}

// runLayoutTree prints the projected tile tree of a workspace, and with
// includeTabs the live tabs of each tile.
func runLayoutTree(inv *cli.Invocation, includeTabs bool) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	ws, err := s.resolveOne(resolve.KindWorkspace, inv.Arg("workspace"))
	if err != nil {
		return nil, err
	}
	cc, err := s.openCRDT(ws.ID)
	if err != nil {
		return nil, err
	}
	rec := cc.bs.State.GetWorkspaces()[ws.ID]
	if rec == nil {
		return nil, cli.Newf(cli.CodeNotFound, "no such workspace: %s", ws.ID)
	}
	out := layoutTree{
		WorkspaceID: ws.ID,
		RootNodeID:  rec.GetRootNodeId(),
		Tree:        projectLayoutNode(cc.bs.State, crdt.LiveChildrenByParent(cc.bs.State), rec.GetRootNodeId()),
	}
	if includeTabs {
		out.TabsByTile = map[string][]layoutTab{}
		for _, t := range cc.bs.State.GetTabs() {
			if t == nil || !crdt.HLCIsZero(t.GetTombstoneAt()) {
				continue
			}
			tile := t.GetTileId().GetValue()
			if tile == "" {
				continue
			}
			out.TabsByTile[tile] = append(out.TabsByTile[tile], layoutTab{ID: t.GetTabId(), Type: tabTypeName(t.GetTabType())})
		}
	}
	return out, nil
}
