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

func floatingWindowNodes(state *leapmuxv1.UserMaterialized, rootNodeID string) map[string]bool {
	nodes := map[string]bool{}
	if rootNodeID == "" {
		return nodes
	}
	children := crdt.LiveChildrenByParent(state)
	var walk func(nodeID string)
	walk = func(nodeID string) {
		n := state.GetNodes()[nodeID]
		if n == nil || !crdt.HLCIsZero(n.GetTombstoneAt()) {
			return
		}
		nodes[nodeID] = true
		for _, childID := range children[nodeID] {
			walk(childID)
		}
	}
	walk(rootNodeID)
	return nodes
}

func resolveFloatingWindow(state *leapmuxv1.UserMaterialized, raw string) (*leapmuxv1.FloatingWindowRecord, error) {
	q := strings.TrimSpace(raw)
	if q == "" {
		return nil, cli.Usagef("window id is empty")
	}
	if rest, ok := strings.CutPrefix(q, "id:"); ok {
		q = strings.TrimSpace(rest)
		if q == "" {
			return nil, cli.Usagef(`"id:" needs a window id after the colon`)
		}
	}
	var live []*leapmuxv1.FloatingWindowRecord
	for _, fw := range state.GetFloatingWindows() {
		if fw == nil || !crdt.HLCIsZero(fw.GetTombstoneAt()) {
			continue
		}
		if fw.GetWindowId() == q {
			return fw, nil
		}
		live = append(live, fw)
	}
	if len(q) >= resolve.MinPrefixLength {
		var matches []*leapmuxv1.FloatingWindowRecord
		for _, fw := range live {
			if strings.HasPrefix(fw.GetWindowId(), q) {
				matches = append(matches, fw)
			}
		}
		switch len(matches) {
		case 1:
			return matches[0], nil
		case 0:
		default:
			cands := make([]cli.Candidate, 0, len(matches))
			for _, m := range matches {
				cands = append(cands, cli.Candidate{ID: m.GetWindowId(), Kind: "window"})
			}
			err := cli.Newf(cli.CodeAmbiguousTarget, "%d floating windows match %q", len(matches), q)
			err.Candidates = cands
			return nil, err
		}
	}
	return nil, cli.Newf(cli.CodeNotFound, "no floating window matches %q", raw).
		WithHint(`list floating windows with "leapmux window list"`)
}

func openCRDTForWorkspaces(s *session, wsIDs []string) (*crdtCall, error) {
	bs, err := crdtBootstrap(s.ctx, s.c, wsIDs)
	if err != nil {
		return nil, err
	}
	ws := ""
	if len(wsIDs) > 0 {
		ws = wsIDs[0]
	}
	return &crdtCall{ctx: s.ctx, c: s.c, bs: bs, workspaceID: ws}, nil
}

func runWindowList(inv *cli.Invocation) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	var wsIDs []string
	if inv.HasArg("workspace") {
		ws, err := s.resolveOne(resolve.KindWorkspace, inv.Arg("workspace"))
		if err != nil {
			return nil, err
		}
		wsIDs = []string{ws.ID}
	}
	cc, err := openCRDTForWorkspaces(s, wsIDs)
	if err != nil {
		return nil, err
	}
	rows := make([]windowRow, 0, len(cc.bs.State.GetFloatingWindows()))
	for _, fw := range cc.bs.State.GetFloatingWindows() {
		if fw == nil || !crdt.HLCIsZero(fw.GetTombstoneAt()) {
			continue
		}
		nodeSet := floatingWindowNodes(cc.bs.State, fw.GetRootNodeId())
		tabs := 0
		for _, t := range cc.bs.State.GetTabs() {
			if t == nil || !crdt.HLCIsZero(t.GetTombstoneAt()) {
				continue
			}
			if nodeSet[t.GetTileId().GetValue()] {
				tabs++
			}
		}
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
	ws, err := s.resolveOne(resolve.KindWorkspace, inv.Arg("workspace"))
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
		opSetWindowRootNodeID(cc.bs, windowID, rootNodeID),
		opSetWindowWorkspaceID(cc.bs, windowID, wsID),
		opSetWindowX(cc.bs, windowID, x),
		opSetWindowY(cc.bs, windowID, y),
		opSetWindowWidth(cc.bs, windowID, width),
		opSetWindowHeight(cc.bs, windowID, height),
		opSetWindowOpacity(cc.bs, windowID, opacity),
	}
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
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	var destWS string
	if inv.Changed("workspace") {
		ws, err := s.resolveOne(resolve.KindWorkspace, stringFlag(inv, "workspace"))
		if err != nil {
			return nil, err
		}
		destWS = ws.ID
	}
	cc, err := openCRDTForWorkspaces(s, nil)
	if err != nil {
		return nil, err
	}
	fw, err := resolveFloatingWindow(cc.bs.State, inv.Arg("window"))
	if err != nil {
		return nil, err
	}
	var ops []*leapmuxv1.CrdtOp
	row := windowRowOf(fw, 0)
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
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	cc, err := openCRDTForWorkspaces(s, nil)
	if err != nil {
		return nil, err
	}
	fw, err := resolveFloatingWindow(cc.bs.State, inv.Arg("window"))
	if err != nil {
		return nil, err
	}
	var ops []*leapmuxv1.CrdtOp
	row := windowRowOf(fw, 0)
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
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	cc, err := openCRDTForWorkspaces(s, nil)
	if err != nil {
		return nil, err
	}
	fw, err := resolveFloatingWindow(cc.bs.State, inv.Arg("window"))
	if err != nil {
		return nil, err
	}
	if err := cc.submitOps([]*leapmuxv1.CrdtOp{opSetWindowOpacity(cc.bs, fw.GetWindowId(), opacity)}); err != nil {
		return nil, err
	}
	row := windowRowOf(fw, 0)
	row.Opacity = opacity
	return row, nil
}

func runWindowClose(inv *cli.Invocation) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	cc, err := openCRDTForWorkspaces(s, nil)
	if err != nil {
		return nil, err
	}
	fw, err := resolveFloatingWindow(cc.bs.State, inv.Arg("window"))
	if err != nil {
		return nil, err
	}
	nodeSet := floatingWindowNodes(cc.bs.State, fw.GetRootNodeId())
	var tabs []crdt.TabRef
	for _, t := range cc.bs.State.GetTabs() {
		if t == nil || !crdt.HLCIsZero(t.GetTombstoneAt()) {
			continue
		}
		if nodeSet[t.GetTileId().GetValue()] {
			if t.GetTabId() == callingTabID() && !boolFlag(inv, allowSelfFlag) {
				return nil, errSelfTargetRefused("refusing to close floating window " + fw.GetWindowId() + " because this tab lives inside it")
			}
			tabs = append(tabs, crdt.TabRef{TabID: t.GetTabId(), TabType: t.GetTabType()})
		}
	}
	if len(tabs) > 0 && !boolFlag(inv, "allow-busy") {
		if err := guardTabsBusy(cc, tabs, false); err != nil {
			return nil, err
		}
	}
	var ops []*leapmuxv1.CrdtOp
	for _, tref := range tabs {
		ops = append(ops, opTombstoneTab(cc.bs, tref.TabType, tref.TabID))
	}
	// Tombstone descendant nodes deterministically leaves-first, then the root
	// node, then the floating window.
	if fw.GetRootNodeId() != "" {
		for _, nodeID := range crdt.DescendantsLeavesFirst(cc.bs.State, fw.GetRootNodeId()) {
			ops = append(ops, opTombstoneNode(cc.bs, nodeID))
		}
		ops = append(ops, opTombstoneNode(cc.bs, fw.GetRootNodeId()))
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
