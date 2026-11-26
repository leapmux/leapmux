package cmd

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strings"

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
			Use:     "split <tile>",
			Short:   "Split a tile in two; its tabs stay in the first half",
			Mutates: true,
			Examples: []cli.Example{
				{Comment: "Split the tile of this tab, with the new tile on the right.", Command: "leapmux tile split self"},
				{Comment: "Split a tile with the new tile below.", Command: "leapmux tile split ab12cd34 --direction horizontal"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.String("direction", "vertical", `the divider: "vertical" puts the new tile on the right, "horizontal" puts it below`)
				cli.SetEnum(fs, "direction", "vertical", "horizontal")
			},
			Complete: completeTargets,
			Run:      runTileSplit,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:         "close <tile>",
			Aliases:     []string{"rm"},
			Short:       "Close a tile and give its space to its neighbour",
			Mutates:     true,
			Destructive: true,
			Examples: []cli.Example{
				{Command: "leapmux tile close ab12cd34"},
				{Comment: "Close a tile and move its tabs to the neighbouring tile.", Command: "leapmux tile close ab12cd34 --with-tabs move"},
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
				{Command: "leapmux tile make-grid ab12cd34 --rows 2 --cols 2"},
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
			Use:         "remove-grid <tile>",
			Short:       "Remove a grid and every tile in it",
			Mutates:     true,
			Destructive: true,
			Examples: []cli.Example{
				{Comment: "Turn a grid back into one tile that keeps its tabs.", Command: "leapmux tile remove-grid ab12cd34 --with-tabs move"},
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
				{Command: "leapmux tile set-ratios ab12cd34 --ratios 0.3,0.7"},
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
				{Command: "leapmux tile set-grid-ratios ab12cd34 --row-ratios 0.6,0.4"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.String("row-ratios", "", "the size of each row, separated by commas")
				fs.String("col-ratios", "", "the size of each column, separated by commas")
			},
			Complete: completeTargets,
			Run:      runTileSetGridRatios,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "resize <tile>",
			Short:   "Resize a tile inside its parent split to a target ratio (0 < ratio < 1)",
			Mutates: true,
			Examples: []cli.Example{
				{Command: "leapmux tile resize self --ratio 0.65"},
				{Command: "leapmux tile resize ab12cd34 --ratio 0.3"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.Float64("ratio", 0, "new share of the parent split for this tile, between 0 and 1 (exclusive)")
				cli.MarkRequired(fs, "ratio")
			},
			Complete: completeTargets,
			Run:      runTileResize,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "swap <tile-a> <tile-b>",
			Short:   "Swap the positions of two tiles in the same workspace",
			Mutates: true,
			Args: map[string]string{
				"tile-a": resolve.KindTile.Help(),
				"tile-b": resolve.KindTile.Help(),
			},
			Examples: []cli.Example{
				{Command: "leapmux tile swap ab12cd34 ef56gh78"},
				{Command: "leapmux tile swap self ef56gh78"},
			},
			Complete: completeSwapTiles,
			Run:      runTileSwap,
		}),
	)
}

func newLayoutCommand() *cobra.Command {
	return cli.Group("layout", "Read, replace and watch the whole tile tree of a workspace",
		cli.NewLeaf(cli.Leaf{
			Use:   "get [workspace]",
			Short: "Print the tile tree of a workspace with its tabs (default: the workspace of this tab)",
			Examples: []cli.Example{
				{Comment: "Save a layout, and restore it later.", Command: "leapmux layout get --json > layout.json"},
			},
			Complete: completeTargets,
			Run: func(inv *cli.Invocation) (any, error) {
				return runLayoutTree(inv, true)
			},
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "set <workspace> [file]",
			Short:   "Replace the main tile tree of a workspace; main-tree tabs move to its first tile",
			Mutates: true,
			Long: `Replace the main tile tree of a workspace. Main-tree tabs move to the new first
tile, and tabs inside floating windows stay in place. The file is "-" or absent
for stdin. It holds a tree as "layout get" prints it: the whole --json
envelope, its data, or the tree alone.`,
			Examples: []cli.Example{
				{Command: "leapmux layout set self layout.json"},
				{Command: "leapmux layout get --json | leapmux layout set 9f3kQ2aB -"},
			},
			Complete: completeTargets,
			Run:      runLayoutSet,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:              "watch [workspace]",
			Short:            "Print every change of your layouts as it happens, one event per line",
			QuietUnsupported: true,
			Long: `Print every change of your layouts as it happens: the CRDT operations, the
presence of other clients, and the creation, renaming and deletion of
workspaces. The first event is the snapshot. Without a workspace, it watches
every workspace.`,
			Examples: []cli.Example{
				{Command: "leapmux layout watch --json"},
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

// layoutFloatingWindow is one floating window belonging to a workspace.
type layoutFloatingWindow struct {
	ID         string      `json:"id"`
	RootNodeID string      `json:"root_node_id"`
	Tree       *layoutNode `json:"tree"`
}

// layoutTree is the result of `tile list` and `layout get`.
type layoutTree struct {
	WorkspaceID string      `json:"workspace_id"`
	RootNodeID  string      `json:"root_node_id"`
	Tree        *layoutNode `json:"tree"`
	// FloatingWindows holds live floating windows anchored in this workspace.
	FloatingWindows []layoutFloatingWindow `json:"floating_windows,omitempty"`
	// TabsByTile maps each live leaf tile in this workspace to its ordered live tabs.
	// Only `layout get` prints it.
	TabsByTile map[string][]layoutTab `json:"tabs_by_tile,omitempty"`
}

// QuietLines prints the live leaf tile IDs of the workspace.
func (l layoutTree) QuietLines() []string {
	var ids []string
	var walk func(n *layoutNode)
	walk = func(n *layoutNode) {
		if n == nil {
			return
		}
		if n.Kind == "leaf" && n.NodeID != "" {
			ids = append(ids, n.NodeID)
		}
		for _, child := range n.Children {
			walk(child)
		}
	}
	walk(l.Tree)
	for _, fw := range l.FloatingWindows {
		walk(fw.Tree)
	}
	return ids
}

// RenderText prints the tree with Unicode box-drawing connectors, percentage
// split ratios, live tabs under each leaf, and any floating windows.
func (l layoutTree) RenderText(w io.Writer) error {
	if _, err := fmt.Fprintf(w, "workspace %s\n", l.WorkspaceID); err != nil {
		return err
	}
	hasFloating := len(l.FloatingWindows) > 0
	if err := l.renderNode(w, l.Tree, "", !hasFloating); err != nil {
		return err
	}
	for i, fw := range l.FloatingWindows {
		lastFW := i == len(l.FloatingWindows)-1
		connector := "├─ "
		childPrefix := "│  "
		if lastFW {
			connector = "└─ "
			childPrefix = "   "
		}
		if _, err := fmt.Fprintf(w, "%sfloating-window %s\n", connector, cli.ShortID(fw.ID)); err != nil {
			return err
		}
		if err := l.renderNode(w, fw.Tree, childPrefix, true); err != nil {
			return err
		}
	}
	return nil
}

func formatRatiosPercent(ratios []float64) string {
	if len(ratios) == 0 {
		return ""
	}
	parts := make([]string, len(ratios))
	for i, r := range ratios {
		parts[i] = fmt.Sprintf("%d%%", int(math.Round(r*100)))
	}
	return strings.Join(parts, " / ")
}

func (l layoutTree) renderNode(w io.Writer, node *layoutNode, prefix string, last bool) error {
	if node == nil {
		return nil
	}
	connector := "├─ "
	nextPrefix := prefix + "│  "
	if last {
		connector = "└─ "
		nextPrefix = prefix + "   "
	}
	line := prefix + connector + node.Kind
	if node.NodeID != "" {
		line += " " + cli.ShortID(node.NodeID)
	}
	switch node.Kind {
	case "split":
		if pct := formatRatiosPercent(node.Ratios); pct != "" {
			line += fmt.Sprintf(" (%s %s)", node.Direction, pct)
		} else if node.Direction != "" {
			line += fmt.Sprintf(" (%s)", node.Direction)
		}
	case "grid":
		line += fmt.Sprintf(" (%dx%d)", node.Rows, node.Cols)
	}
	tabs := l.TabsByTile[node.NodeID]
	if len(tabs) > 0 {
		line += " (" + pluralize(len(tabs), "tab") + ")"
	}
	if _, err := fmt.Fprintln(w, line); err != nil {
		return err
	}
	totalChildren := len(tabs) + len(node.Children)
	for i, tab := range tabs {
		tabConn := "├─ "
		if i == totalChildren-1 {
			tabConn = "└─ "
		}
		tabLine := fmt.Sprintf("[%s %s]", tab.Type, cli.ShortID(tab.ID))
		if tab.Title != "" {
			tabLine += fmt.Sprintf(" %q", cli.SanitizeSingleLine(tab.Title))
		}
		if tab.State != "" {
			tabLine += " (" + cli.SanitizeSingleLine(tab.State) + ")"
		}
		if _, err := fmt.Fprintf(w, "%s%s%s\n", nextPrefix, tabConn, tabLine); err != nil {
			return err
		}
	}
	for i, child := range node.Children {
		childLast := len(tabs)+i == totalChildren-1
		if err := l.renderNode(w, child, nextPrefix, childLast); err != nil {
			return err
		}
	}
	return nil
}

// layoutTab is one tab of a tile in a layout.
type layoutTab struct {
	ID       string `json:"id" table:"ID,short"`
	Type     string `json:"type" table:"TYPE"`
	Title    string `json:"title,omitempty" table:"TITLE"`
	State    string `json:"state,omitempty" table:"STATE,state"`
	Position string `json:"position,omitempty"`
}

// buildLayoutTreeForWorkspace projects one workspace's main tile tree, its
// live floating windows, and (when includeTabs is true) only the live tabs
// placed on nodes belonging to that workspace, ordered by LexoRank position.
func buildLayoutTreeForWorkspace(state *leapmuxv1.UserMaterialized, wsID string, includeTabs bool) layoutTree {
	rec := state.GetWorkspaces()[wsID]
	childrenByParent := crdt.LiveChildrenByParent(state)
	out := layoutTree{
		WorkspaceID: wsID,
		RootNodeID:  rec.GetRootNodeId(),
		Tree:        projectLayoutNode(state, childrenByParent, rec.GetRootNodeId()),
	}

	wsNodes := map[string]bool{}
	var collectNodes func(n *layoutNode)
	collectNodes = func(n *layoutNode) {
		if n == nil {
			return
		}
		if n.NodeID != "" {
			wsNodes[n.NodeID] = true
		}
		for _, child := range n.Children {
			collectNodes(child)
		}
	}
	collectNodes(out.Tree)

	var floatIDs []string
	for id, fw := range state.GetFloatingWindows() {
		if fw == nil || !crdt.HLCIsZero(fw.GetTombstoneAt()) {
			continue
		}
		if fw.GetWorkspaceId().GetValue() != wsID {
			continue
		}
		floatIDs = append(floatIDs, id)
	}
	sort.Strings(floatIDs)
	for _, id := range floatIDs {
		fw := state.GetFloatingWindows()[id]
		fwTree := projectLayoutNode(state, childrenByParent, fw.GetRootNodeId())
		collectNodes(fwTree)
		out.FloatingWindows = append(out.FloatingWindows, layoutFloatingWindow{
			ID:         id,
			RootNodeID: fw.GetRootNodeId(),
			Tree:       fwTree,
		})
	}

	if includeTabs {
		out.TabsByTile = map[string][]layoutTab{}
		for _, t := range state.GetTabs() {
			if t == nil || !crdt.HLCIsZero(t.GetTombstoneAt()) {
				continue
			}
			tile := t.GetTileId().GetValue()
			if tile == "" || !wsNodes[tile] {
				continue
			}
			out.TabsByTile[tile] = append(out.TabsByTile[tile], layoutTab{
				ID:       t.GetTabId(),
				Type:     tabTypeName(t.GetTabType()),
				Position: t.GetPosition().GetValue(),
			})
		}
		for tile := range out.TabsByTile {
			tabs := out.TabsByTile[tile]
			sort.Slice(tabs, func(i, j int) bool {
				if tabs[i].Position != tabs[j].Position {
					return tabs[i].Position < tabs[j].Position
				}
				return tabs[i].ID < tabs[j].ID
			})
			out.TabsByTile[tile] = tabs
		}
	}
	return out
}

// runLayoutTree prints the projected tile tree of a workspace, and with
// includeTabs the live tabs of each tile.
func runLayoutTree(inv *cli.Invocation, includeTabs bool) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	wsInput := inv.Arg("workspace")
	if wsInput == "" && callingTabID() == "" {
		if def, derr := s.defaultWorkspaceID(); derr != nil {
			return nil, derr
		} else if def != "" {
			wsInput = def
		}
	}
	ws, err := s.resolveOne(resolve.KindWorkspace, wsInput)
	if err != nil {
		return nil, err
	}
	cc, err := s.openCRDT(ws.ID)
	if err != nil {
		return nil, err
	}
	if rec := cc.bs.State.GetWorkspaces()[ws.ID]; rec == nil {
		return nil, cli.Newf(cli.CodeNotFound, "no such workspace: %s", ws.ID)
	}
	tree := buildLayoutTreeForWorkspace(cc.bs.State, ws.ID, includeTabs)
	if includeTabs && len(tree.TabsByTile) > 0 {
		var wsTabs []*leapmuxv1.WorkspaceTab
		for _, t := range cc.bs.State.GetTabs() {
			if t == nil || !crdt.HLCIsZero(t.GetTombstoneAt()) {
				continue
			}
			tile := t.GetTileId().GetValue()
			if _, ok := tree.TabsByTile[tile]; !ok {
				continue
			}
			wsTabs = append(wsTabs, &leapmuxv1.WorkspaceTab{
				Tab:         &leapmuxv1.TabIdent{TabId: t.GetTabId(), TabType: t.GetTabType()},
				WorkspaceId: ws.ID,
				TileId:      tile,
				WorkerId:    t.GetWorkerId().GetValue(),
			})
		}
		if len(wsTabs) > 0 {
			details, _ := fetchTabDetailsAndWorkers(s.ctx, s.c, wsTabs, false)
			for tile, tabs := range tree.TabsByTile {
				for i, lt := range tabs {
					d := details[lt.ID]
					tabs[i].Title = d.Title
					switch {
					case d.Agent != nil:
						tabs[i].State = enumToken(d.Agent.GetActivityState())
					case d.Terminal != nil:
						tabs[i].State = enumToken(d.Terminal.GetStatus())
					}
				}
				tree.TabsByTile[tile] = tabs
			}
		}
	}
	return tree, nil
}
