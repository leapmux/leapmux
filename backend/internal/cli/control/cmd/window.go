package cmd

import (
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/util/id"
)

func newWindowCommand() *cobra.Command {
	return cli.SetAliases(cli.Group("window", "List, create, position, resize, and close floating windows",
		cli.NewLeaf(cli.Leaf{
			Use:     "list [workspace]",
			Aliases: []string{"ls"},
			Short:   "List live floating windows (default: all workspaces)",
			Examples: []cli.Example{
				{Command: "leapmux window list"},
				{Command: "leapmux window list self"},
			},
			Complete: completeTargets,
			Run:      runWindowList,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "create <workspace>",
			Short:   "Create a floating window in a workspace",
			Mutates: true,
			Next: []string{
				"leapmux agent open --tile <root_node_id>",
				"leapmux terminal open --tile <root_node_id>",
			},
			Examples: []cli.Example{
				{Command: "leapmux window create self --x 120 --y 80 --width 640 --height 420"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.Float64("x", 100, "horizontal position in pixels")
				fs.Float64("y", 100, "vertical position in pixels")
				fs.Float64("width", 640, "width in pixels (must be > 0)")
				fs.Float64("height", 420, "height in pixels (must be > 0)")
				fs.Float64("opacity", 1.0, "window opacity from 0.0 to 1.0")
			},
			Complete: completeTargets,
			Run:      runWindowCreate,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "move <window>",
			Aliases: []string{"mv"},
			Short:   "Move a floating window to new coordinates or another workspace",
			Mutates: true,
			Examples: []cli.Example{
				{Command: "leapmux window move 7aB1c2d3 --x 200 --y 150"},
				{Command: `leapmux window move 7aB1c2d3 --workspace "Release 1.4"`},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.Float64("x", 0, "new horizontal position in pixels")
				fs.Float64("y", 0, "new vertical position in pixels")
				fs.String("workspace", "", "move the window to this workspace")
			},
			Complete: completeWindows,
			Run:      runWindowMove,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "resize <window>",
			Short:   "Resize a floating window",
			Mutates: true,
			Examples: []cli.Example{
				{Command: "leapmux window resize 7aB1c2d3 --width 800 --height 600"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.Float64("width", 0, "new width in pixels (must be > 0)")
				fs.Float64("height", 0, "new height in pixels (must be > 0)")
			},
			Complete: completeWindows,
			Run:      runWindowResize,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "set-opacity <window> <opacity>",
			Short:   "Set the opacity of a floating window (0.0 to 1.0)",
			Mutates: true,
			Examples: []cli.Example{
				{Command: "leapmux window set-opacity 7aB1c2d3 0.85"},
			},
			Complete: completeWindows,
			Run:      runWindowSetOpacity,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:         "close <window>",
			Aliases:     []string{"rm"},
			Short:       "Close a floating window and its inner tiles",
			Mutates:     true,
			Destructive: true,
			Examples: []cli.Example{
				{Command: "leapmux window close 7aB1c2d3"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.Bool("allow-busy", false, allowBusyFlagHelp)
				fs.Bool(allowSelfFlag, false, "close the window even when this tab lives in it")
			},
			Complete: completeWindows,
			Run:      runWindowClose,
		}),
	), "win")
}

type windowRow struct {
	ID          string  `json:"id" table:"ID,short"`
	WorkspaceID string  `json:"workspace_id" table:"WORKSPACE,short"`
	RootNodeID  string  `json:"root_node_id" table:"TILE,short"`
	X           float64 `json:"x" table:"X"`
	Y           float64 `json:"y" table:"Y"`
	Width       float64 `json:"width" table:"WIDTH"`
	Height      float64 `json:"height" table:"HEIGHT"`
	Opacity     float64 `json:"opacity" table:"OPACITY"`
	Tabs        int     `json:"tabs" table:"TABS"`
}

type windowListResult struct {
	Windows []windowRow `json:"windows"`
}

func (r windowListResult) QuietLines() []string {
	out := make([]string, 0, len(r.Windows))
	for _, w := range r.Windows {
		out = append(out, w.ID)
	}
	return out
}

func windowRowOf(fw *leapmuxv1.FloatingWindowRecord, tabCount int) windowRow {
	return windowRow{
		ID:          fw.GetWindowId(),
		WorkspaceID: fw.GetWorkspaceId().GetValue(),
		RootNodeID:  fw.GetRootNodeId(),
		X:           fw.GetX().GetValue(),
		Y:           fw.GetY().GetValue(),
		Width:       fw.GetWidth().GetValue(),
		Height:      fw.GetHeight().GetValue(),
		Opacity:     fw.GetOpacity().GetValue(),
		Tabs:        tabCount,
	}
}

func newSetFloatingWindowOp(bs *CRDTBootstrap, windowID string) (*leapmuxv1.CrdtOp, *leapmuxv1.SetFloatingWindowRegisterOp) {
	op := envelope(bs)
	inner := &leapmuxv1.SetFloatingWindowRegisterOp{WindowId: windowID}
	op.Body = &leapmuxv1.CrdtOp_SetFloatingWindowRegister{SetFloatingWindowRegister: inner}
	return op, inner
}

func opSetWindowX(bs *CRDTBootstrap, windowID string, v float64) *leapmuxv1.CrdtOp {
	op, inner := newSetFloatingWindowOp(bs, windowID)
	inner.Field = &leapmuxv1.SetFloatingWindowRegisterOp_X{X: v}
	return op
}

func opSetWindowY(bs *CRDTBootstrap, windowID string, v float64) *leapmuxv1.CrdtOp {
	op, inner := newSetFloatingWindowOp(bs, windowID)
	inner.Field = &leapmuxv1.SetFloatingWindowRegisterOp_Y{Y: v}
	return op
}

func opSetWindowWidth(bs *CRDTBootstrap, windowID string, v float64) *leapmuxv1.CrdtOp {
	op, inner := newSetFloatingWindowOp(bs, windowID)
	inner.Field = &leapmuxv1.SetFloatingWindowRegisterOp_Width{Width: v}
	return op
}

func opSetWindowHeight(bs *CRDTBootstrap, windowID string, v float64) *leapmuxv1.CrdtOp {
	op, inner := newSetFloatingWindowOp(bs, windowID)
	inner.Field = &leapmuxv1.SetFloatingWindowRegisterOp_Height{Height: v}
	return op
}

func opSetWindowOpacity(bs *CRDTBootstrap, windowID string, v float64) *leapmuxv1.CrdtOp {
	op, inner := newSetFloatingWindowOp(bs, windowID)
	inner.Field = &leapmuxv1.SetFloatingWindowRegisterOp_Opacity{Opacity: v}
	return op
}

func opSetWindowRootNodeID(bs *CRDTBootstrap, windowID, rootNodeID string) *leapmuxv1.CrdtOp {
	op, inner := newSetFloatingWindowOp(bs, windowID)
	inner.Field = &leapmuxv1.SetFloatingWindowRegisterOp_RootNodeId{RootNodeId: rootNodeID}
	return op
}

func opSetWindowWorkspaceID(bs *CRDTBootstrap, windowID, workspaceID string) *leapmuxv1.CrdtOp {
	op, inner := newSetFloatingWindowOp(bs, windowID)
	inner.Field = &leapmuxv1.SetFloatingWindowRegisterOp_WorkspaceId{WorkspaceId: workspaceID}
	return op
}

func opTombstoneWindow(bs *CRDTBootstrap, windowID string) *leapmuxv1.CrdtOp {
	op := envelope(bs)
	op.Body = &leapmuxv1.CrdtOp_TombstoneFloatingWindow{
		TombstoneFloatingWindow: &leapmuxv1.TombstoneFloatingWindowOp{WindowId: windowID},
	}
	return op
}

// appendCreateFloatingWindowOps appends the seven SetFloatingWindowRegister
// ops that initialize windowID in workspaceID with rootNodeID and geometry.
// Callers emit rootNodeID's node shape ops separately.
func appendCreateFloatingWindowOps(ops []*leapmuxv1.CrdtOp, bs *CRDTBootstrap, windowID, workspaceID, rootNodeID string, x, y, width, height, opacity float64) []*leapmuxv1.CrdtOp {
	return append(ops,
		opSetWindowRootNodeID(bs, windowID, rootNodeID),
		opSetWindowWorkspaceID(bs, windowID, workspaceID),
		opSetWindowX(bs, windowID, x),
		opSetWindowY(bs, windowID, y),
		opSetWindowWidth(bs, windowID, width),
		opSetWindowHeight(bs, windowID, height),
		opSetWindowOpacity(bs, windowID, opacity),
	)
}

func resolveFloatingWindow(state *leapmuxv1.UserMaterialized, raw string) (*leapmuxv1.FloatingWindowRecord, error) {
	handle, err := normalizeIDHandle(raw, "window id")
	if err != nil {
		return nil, err
	}
	live := make([]*leapmuxv1.FloatingWindowRecord, 0, len(state.GetFloatingWindows()))
	for _, fw := range state.GetFloatingWindows() {
		if fw == nil || !crdt.HLCIsZero(fw.GetTombstoneAt()) {
			continue
		}
		live = append(live, fw)
	}
	slices.SortFunc(live, func(a, b *leapmuxv1.FloatingWindowRecord) int {
		return strings.Compare(a.GetWindowId(), b.GetWindowId())
	})
	if matched, ambiguous, ok := lookupByIDPrefixMin(live, handle, resolve.MinPrefixLength, (*leapmuxv1.FloatingWindowRecord).GetWindowId); ok {
		return matched, nil
	} else if len(ambiguous) > 0 {
		cands := make([]cli.Candidate, 0, len(ambiguous))
		for _, m := range ambiguous {
			cands = append(cands, cli.Candidate{ID: m.GetWindowId(), Kind: "window"})
		}
		err := cli.Newf(cli.CodeAmbiguousTarget, "%d floating windows match %q", len(ambiguous), handle)
		err.Candidates = cands
		return nil, err
	}
	return nil, cli.Newf(cli.CodeNotFound, "no floating window matches %q", raw).
		WithHint(`list floating windows with "leapmux window list"`)
}

// openFloatingWindow opens a session, bootstraps the user CRDT, and resolves
// the positional <window> argument to a live floating window. On error it
// closes any opened session before returning.
func openFloatingWindow(inv *cli.Invocation) (*session, *crdtCall, *leapmuxv1.FloatingWindowRecord, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, nil, nil, err
	}
	cc, err := s.openCRDT()
	if err != nil {
		s.Close()
		return nil, nil, nil, err
	}
	fw, err := resolveFloatingWindow(cc.bs.State, inv.Arg("window"))
	if err != nil {
		s.Close()
		return nil, nil, nil, err
	}
	return s, cc, fw, nil
}

func countSubtreeTabs(state *leapmuxv1.UserMaterialized, children map[string][]string, tabCountByTile map[string]int, rootNodeID string) int {
	if rootNodeID == "" {
		return 0
	}
	nodes := state.GetNodes()
	visited := map[string]bool{}
	total := 0
	var walk func(nodeID string)
	walk = func(nodeID string) {
		if nodeID == "" || visited[nodeID] {
			return
		}
		n := nodes[nodeID]
		if n == nil || !crdt.HLCIsZero(n.GetTombstoneAt()) {
			return
		}
		visited[nodeID] = true
		total += tabCountByTile[nodeID]
		for _, childID := range children[nodeID] {
			walk(childID)
		}
	}
	walk(rootNodeID)
	return total
}

func runWindowList(inv *cli.Invocation) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	var wsIDs []string
	var filterWS string
	if inv.HasArg("workspace") {
		ws, err := s.resolveExistingWorkspace(inv.Arg("workspace"))
		if err != nil {
			return nil, err
		}
		wsIDs = []string{ws.ID}
		filterWS = ws.ID
	}
	cc, err := s.openCRDT(wsIDs...)
	if err != nil {
		return nil, err
	}
	children := crdt.LiveChildrenByParent(cc.bs.State)
	tabCountByTile := make(map[string]int, len(cc.bs.State.GetTabs()))
	for _, t := range cc.bs.State.GetTabs() {
		if t == nil || !crdt.HLCIsZero(t.GetTombstoneAt()) {
			continue
		}
		if tileID := t.GetTileId().GetValue(); tileID != "" {
			tabCountByTile[tileID]++
		}
	}
	rows := make([]windowRow, 0, len(cc.bs.State.GetFloatingWindows()))
	for _, fw := range cc.bs.State.GetFloatingWindows() {
		if fw == nil || !crdt.HLCIsZero(fw.GetTombstoneAt()) {
			continue
		}
		if filterWS != "" && fw.GetWorkspaceId().GetValue() != filterWS {
			continue
		}
		tabs := countSubtreeTabs(cc.bs.State, children, tabCountByTile, fw.GetRootNodeId())
		rows = append(rows, windowRowOf(fw, tabs))
	}
	slices.SortFunc(rows, func(a, b windowRow) int {
		if a.WorkspaceID != b.WorkspaceID {
			return strings.Compare(a.WorkspaceID, b.WorkspaceID)
		}
		return strings.Compare(a.ID, b.ID)
	})
	return windowListResult{Windows: rows}, nil
}

func runWindowCreate(inv *cli.Invocation) (any, error) {
	x, err := floatFlag(inv, "x")
	if err != nil {
		return nil, err
	}
	y, err := floatFlag(inv, "y")
	if err != nil {
		return nil, err
	}
	width, err := floatFlag(inv, "width")
	if err != nil {
		return nil, err
	}
	if width <= 0 {
		return nil, cli.Usagef("--width must be greater than 0")
	}
	height, err := floatFlag(inv, "height")
	if err != nil {
		return nil, err
	}
	if height <= 0 {
		return nil, cli.Usagef("--height must be greater than 0")
	}
	opacity, err := floatFlag(inv, "opacity")
	if err != nil {
		return nil, err
	}
	if opacity < 0 || opacity > 1 {
		return nil, cli.Usagef("--opacity must be between 0.0 and 1.0")
	}

	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	ws, err := s.resolveExistingWorkspace(inv.Arg("workspace"))
	if err != nil {
		return nil, err
	}
	wsID := ws.ID
	cc, err := s.openCRDT(wsID)
	if err != nil {
		return nil, err
	}
	windowID := id.Generate()
	rootNodeID := id.Generate()
	ops := []*leapmuxv1.CrdtOp{
		opSetNodeKind(cc.bs, rootNodeID, leapmuxv1.NodeKind_NODE_KIND_LEAF),
	}
	ops = appendCreateFloatingWindowOps(ops, cc.bs, windowID, wsID, rootNodeID, x, y, width, height, opacity)
	if err := cc.submitOps(ops); err != nil {
		return nil, err
	}
	return windowRow{
		ID:          windowID,
		WorkspaceID: wsID,
		RootNodeID:  rootNodeID,
		X:           x,
		Y:           y,
		Width:       width,
		Height:      height,
		Opacity:     opacity,
	}, nil
}

func runWindowMove(inv *cli.Invocation) (any, error) {
	if !inv.Changed("x") && !inv.Changed("y") && !inv.Changed("workspace") {
		return nil, cli.Usagef("give at least one of --x, --y, or --workspace")
	}
	var x, y float64
	var err error
	if inv.Changed("x") {
		if x, err = floatFlag(inv, "x"); err != nil {
			return nil, err
		}
	}
	if inv.Changed("y") {
		if y, err = floatFlag(inv, "y"); err != nil {
			return nil, err
		}
	}
	s, cc, fw, err := openFloatingWindow(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	var destWS string
	if inv.Changed("workspace") {
		ws, err := s.resolveExistingWorkspace(stringFlag(inv, "workspace"))
		if err != nil {
			return nil, err
		}
		destWS = ws.ID
	}
	var ops []*leapmuxv1.CrdtOp
	tabCount := len(crdt.LiveTabsInSubtree(cc.bs.State, fw.GetRootNodeId()))
	row := windowRowOf(fw, tabCount)
	if inv.Changed("x") {
		ops = append(ops, opSetWindowX(cc.bs, fw.GetWindowId(), x))
		row.X = x
	}
	if inv.Changed("y") {
		ops = append(ops, opSetWindowY(cc.bs, fw.GetWindowId(), y))
		row.Y = y
	}
	if destWS != "" {
		ops = append(ops, opSetWindowWorkspaceID(cc.bs, fw.GetWindowId(), destWS))
		row.WorkspaceID = destWS
	}
	if err := cc.submitOps(ops); err != nil {
		return nil, err
	}
	return row, nil
}

func runWindowResize(inv *cli.Invocation) (any, error) {
	if !inv.Changed("width") && !inv.Changed("height") {
		return nil, cli.Usagef("give --width, --height, or both")
	}
	var width, height float64
	var err error
	if inv.Changed("width") {
		if width, err = floatFlag(inv, "width"); err != nil {
			return nil, err
		}
		if width <= 0 {
			return nil, cli.Usagef("--width must be greater than 0")
		}
	}
	if inv.Changed("height") {
		if height, err = floatFlag(inv, "height"); err != nil {
			return nil, err
		}
		if height <= 0 {
			return nil, cli.Usagef("--height must be greater than 0")
		}
	}
	s, cc, fw, err := openFloatingWindow(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	var ops []*leapmuxv1.CrdtOp
	tabCount := len(crdt.LiveTabsInSubtree(cc.bs.State, fw.GetRootNodeId()))
	row := windowRowOf(fw, tabCount)
	if inv.Changed("width") {
		ops = append(ops, opSetWindowWidth(cc.bs, fw.GetWindowId(), width))
		row.Width = width
	}
	if inv.Changed("height") {
		ops = append(ops, opSetWindowHeight(cc.bs, fw.GetWindowId(), height))
		row.Height = height
	}
	if err := cc.submitOps(ops); err != nil {
		return nil, err
	}
	return row, nil
}

func runWindowSetOpacity(inv *cli.Invocation) (any, error) {
	opacity, err := strconv.ParseFloat(strings.TrimSpace(inv.Arg("opacity")), 64)
	if err != nil || math.IsNaN(opacity) || math.IsInf(opacity, 0) || opacity < 0 || opacity > 1 {
		return nil, cli.Usagef("<opacity> must be a number from 0.0 to 1.0; got %q", inv.Arg("opacity"))
	}
	s, cc, fw, err := openFloatingWindow(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	if err := cc.submitOps([]*leapmuxv1.CrdtOp{opSetWindowOpacity(cc.bs, fw.GetWindowId(), opacity)}); err != nil {
		return nil, err
	}
	tabCount := len(crdt.LiveTabsInSubtree(cc.bs.State, fw.GetRootNodeId()))
	row := windowRowOf(fw, tabCount)
	row.Opacity = opacity
	return row, nil
}

func runWindowClose(inv *cli.Invocation) (any, error) {
	s, cc, fw, err := openFloatingWindow(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	tabs := crdt.LiveTabsInSubtree(cc.bs.State, fw.GetRootNodeId())
	slices.SortFunc(tabs, func(a, b crdt.TabRef) int {
		return strings.Compare(a.TabID, b.TabID)
	})
	if err := guardLayoutClose("floating window "+fw.GetWindowId(), tabs, boolFlag(inv, allowSelfFlag)); err != nil {
		return nil, err
	}
	if err := guardBulkCloseTabs(cc, tabs, boolFlag(inv, "allow-busy")); err != nil {
		return nil, err
	}
	var ops []*leapmuxv1.CrdtOp
	if fw.GetRootNodeId() != "" {
		ops = append(ops, buildCloseSubtreeOps(cc.bs, fw.GetRootNodeId(), "", false)...)
	}
	ops = append(ops, opTombstoneWindow(cc.bs, fw.GetWindowId()))
	if err := cc.submitOps(ops); err != nil {
		return nil, err
	}
	return struct {
		ID         string `json:"id"`
		Closed     bool   `json:"closed"`
		TabsClosed int    `json:"tabs_closed"`
	}{ID: fw.GetWindowId(), Closed: true, TabsClosed: len(tabs)}, nil
}
