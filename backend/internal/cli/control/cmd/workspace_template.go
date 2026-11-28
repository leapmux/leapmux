package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"go.yaml.in/yaml/v4"
	"golang.org/x/sync/errgroup"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/util/agentlabels"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/lexorank"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/util/atomicfile"
	"github.com/leapmux/leapmux/util/repopaths"
)

func newWorkspaceExportCommand() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:              "export [workspace]",
		Short:            "Export a workspace's layout, floating windows, and tab definitions as a declarative YAML/JSON template",
		QuietUnsupported: true,
		Long: `Export a workspace's main tile tree, floating windows, and live tab definitions
(agents with provider/model/options/working_dir, terminals, and file viewers)
into a declarative YAML or JSON template that "workspace apply" can reproduce.`,
		Examples: []cli.Example{
			{Command: "leapmux workspace export self > workspace.yaml"},
			{Command: "leapmux workspace export 9f3kQ2aB --output team-dev.yaml"},
			{Command: "leapmux workspace export 9f3kQ2aB --style json"},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.StringP("output", "o", "", "write the exported template to this file instead of stdout")
			fs.String("style", "yaml", "text serialization format when --json is not set")
			cli.SetEnum(fs, "style", "yaml", "json")
		},
		Complete: completeTargets,
		Run:      runWorkspaceExport,
	})
}

func newWorkspaceApplyCommand() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:     "apply <file>",
		Short:   "Create or update a workspace from a declarative YAML/JSON template",
		Mutates: true,
		Long: `Apply a declarative workspace template produced by "workspace export" (or
written by hand). Reads YAML or JSON from <file> ("-" reads stdin).

Creates a new workspace unless --workspace targets an existing one, applies the
main tile layout and any floating windows, and opens each declared agent,
terminal, and file tab in its mapped tile.`,
		Examples: []cli.Example{
			{Command: "leapmux workspace apply workspace.yaml"},
			{Command: `leapmux workspace apply workspace.yaml --title "Feature Sprint" --worker name:build-box`},
			{Command: "leapmux workspace apply workspace.yaml --dry-run"},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.String("workspace", "", "apply the template onto this existing workspace instead of creating a new one")
			fs.String("title", "", "override the workspace title from the template")
			addWorkerFlag(fs)
			fs.Bool("dry-run", false, "validate the template and print the planned layout and tabs without mutating anything")
		},
		Run: runWorkspaceApply,
	})
}

type workspaceTemplate struct {
	Version         int                      `json:"version" yaml:"version"`
	Title           string                   `json:"title" yaml:"title"`
	Tree            *templateLayoutNode      `json:"tree" yaml:"tree"`
	FloatingWindows []templateFloatingWindow `json:"floating_windows,omitempty" yaml:"floating_windows,omitempty"`
	Tabs            []templateTab            `json:"tabs,omitempty" yaml:"tabs,omitempty"`

	// style selects RenderText serialization ("yaml" or "json"); omitted from wire output.
	style      string
	parsedTree *layoutNode
}

type templateLayoutNode struct {
	TileRef   string                `json:"tile_ref,omitempty" yaml:"tile_ref,omitempty"`
	Kind      string                `json:"kind" yaml:"kind"`
	Direction string                `json:"direction,omitempty" yaml:"direction,omitempty"`
	Ratios    []float64             `json:"ratios,omitempty" yaml:"ratios,omitempty"`
	Rows      uint32                `json:"rows,omitempty" yaml:"rows,omitempty"`
	Cols      uint32                `json:"cols,omitempty" yaml:"cols,omitempty"`
	RowRatios []float64             `json:"row_ratios,omitempty" yaml:"row_ratios,omitempty"`
	ColRatios []float64             `json:"col_ratios,omitempty" yaml:"col_ratios,omitempty"`
	Children  []*templateLayoutNode `json:"children,omitempty" yaml:"children,omitempty"`
}

type templateFloatingWindow struct {
	X       float64             `json:"x" yaml:"x"`
	Y       float64             `json:"y" yaml:"y"`
	Width   float64             `json:"width" yaml:"width"`
	Height  float64             `json:"height" yaml:"height"`
	Opacity *float64            `json:"opacity,omitempty" yaml:"opacity,omitempty"`
	Tree    *templateLayoutNode `json:"tree" yaml:"tree"`

	parsedTree *layoutNode
}

func (fw templateFloatingWindow) effectiveOpacity() float64 {
	if fw.Opacity == nil {
		return 1.0
	}
	return *fw.Opacity
}

type templateTab struct {
	TileRef        string            `json:"tile_ref,omitempty" yaml:"tile_ref,omitempty"`
	Type           string            `json:"type" yaml:"type"`
	Title          string            `json:"title,omitempty" yaml:"title,omitempty"`
	WorkerID       string            `json:"worker_id,omitempty" yaml:"worker_id,omitempty"`
	WorkingDir     string            `json:"working_dir,omitempty" yaml:"working_dir,omitempty"`
	Provider       string            `json:"provider,omitempty" yaml:"provider,omitempty"`
	Model          string            `json:"model,omitempty" yaml:"model,omitempty"`
	Effort         string            `json:"effort,omitempty" yaml:"effort,omitempty"`
	PermissionMode string            `json:"permission_mode,omitempty" yaml:"permission_mode,omitempty"`
	Options        map[string]string `json:"options,omitempty" yaml:"options,omitempty"`
	InitialMessage string            `json:"initial_message,omitempty" yaml:"initial_message,omitempty"`
	Shell          string            `json:"shell,omitempty" yaml:"shell,omitempty"`
	InitialCommand string            `json:"initial_command,omitempty" yaml:"initial_command,omitempty"`
	Worktree       string            `json:"worktree,omitempty" yaml:"worktree,omitempty"`
	BaseBranch     string            `json:"base_branch,omitempty" yaml:"base_branch,omitempty"`
	Path           string            `json:"path,omitempty" yaml:"path,omitempty"`
	DisplayMode    string            `json:"display_mode,omitempty" yaml:"display_mode,omitempty"`
	ViewMode       string            `json:"view_mode,omitempty" yaml:"view_mode,omitempty"`
	DiffBase       string            `json:"diff_base,omitempty" yaml:"diff_base,omitempty"`

	presentation *leapmuxv1.TabPresentation
}

func (t workspaceTemplate) marshalStyled() ([]byte, error) {
	if strings.EqualFold(t.style, "json") {
		raw, err := json.MarshalIndent(t, "", "  ")
		if err != nil {
			return nil, err
		}
		return append(raw, '\n'), nil
	}
	return yaml.Marshal(t)
}

func (t workspaceTemplate) RenderText(w io.Writer) error {
	raw, err := t.marshalStyled()
	if err != nil {
		return err
	}
	_, err = w.Write(raw)
	return err
}

type workspaceExportFileResult struct {
	WorkspaceID string `json:"workspace_id"`
	Title       string `json:"title"`
	Output      string `json:"output"`
	Style       string `json:"style"`
	Tabs        int    `json:"tabs"`
}

func runWorkspaceExport(inv *cli.Invocation) (any, error) {
	style := strings.ToLower(strings.TrimSpace(stringFlag(inv, "style")))
	if style != "yaml" && style != "json" {
		return nil, cli.Usagef("--style must be yaml or json; got %q", style)
	}
	outPath := strings.TrimSpace(stringFlag(inv, "output"))
	if inv.Changed("output") && outPath == "" {
		return nil, cli.Usagef("--output is empty")
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()

	wsInput := inv.Arg("workspace")
	wsID := resolve.ExplicitID(resolve.KindWorkspace, wsInput)
	if wsID == "" {
		wsTarget, err := s.resolveWorkspaceOrDefault(wsInput)
		if err != nil {
			return nil, err
		}
		wsID = wsTarget.ID
	}
	var (
		wsResp leapmuxv1.GetWorkspaceResponse
		cc     *crdtCall
	)
	g, gctx := errgroup.WithContext(s.ctx)
	g.Go(func() error {
		return hubCallUnary(gctx, s.c, leapmuxv1connect.WorkspaceServiceGetWorkspaceProcedure, &leapmuxv1.GetWorkspaceRequest{WorkspaceId: wsID}, &wsResp)
	})
	g.Go(func() error {
		gs := *s
		gs.ctx = gctx
		var cerr error
		cc, cerr = gs.openCRDT(wsID)
		return cerr
	})
	if err := g.Wait(); err != nil {
		return nil, err
	}
	lt := buildLayoutTreeForWorkspace(cc.bs.State, wsID)

	// Assign stable human-readable tile_ref labels ("t1", "t2", ...) in traversal order.
	tileRefByID := map[string]string{}
	tileOrder := []string{}
	leafSeq := 0
	var convertNode func(n *layoutNode) *templateLayoutNode
	convertNode = func(n *layoutNode) *templateLayoutNode {
		if n == nil {
			return nil
		}
		out := &templateLayoutNode{
			Kind:      n.Kind,
			Direction: n.Direction,
			Ratios:    n.Ratios,
			Rows:      n.Rows,
			Cols:      n.Cols,
			RowRatios: n.RowRatios,
			ColRatios: n.ColRatios,
		}
		if n.Kind == "leaf" && n.NodeID != "" {
			leafSeq++
			ref := fmt.Sprintf("t%d", leafSeq)
			tileRefByID[n.NodeID] = ref
			tileOrder = append(tileOrder, n.NodeID)
			out.TileRef = ref
		}
		for _, ch := range n.Children {
			out.Children = append(out.Children, convertNode(ch))
		}
		return out
	}

	tmpl := workspaceTemplate{
		Version: 1,
		Title:   wsResp.GetWorkspace().GetTitle(),
		Tree:    convertNode(lt.Tree),
		style:   style,
	}

	for _, fw := range lt.FloatingWindows {
		rec := cc.bs.State.GetFloatingWindows()[fw.ID]
		var opacity *float64
		if rec.GetOpacity() != nil {
			v := rec.GetOpacity().GetValue()
			opacity = &v
		}
		tmpl.FloatingWindows = append(tmpl.FloatingWindows, templateFloatingWindow{
			X:       rec.GetX().GetValue(),
			Y:       rec.GetY().GetValue(),
			Width:   rec.GetWidth().GetValue(),
			Height:  rec.GetHeight().GetValue(),
			Opacity: opacity,
			Tree:    convertNode(fw.Tree),
		})
	}

	// Build the workspace tab list directly from the CRDT snapshot (sorted by
	// TabID) and hydrate agents, terminals, and file payloads in one Worker pass.
	var wsTabs []*leapmuxv1.WorkspaceTab
	for _, t := range cc.bs.State.GetTabs() {
		if t == nil || !crdt.HLCIsZero(t.GetTombstoneAt()) {
			continue
		}
		tileID := t.GetTileId().GetValue()
		if tileID == "" || tileRefByID[tileID] == "" {
			continue
		}
		wsTabs = append(wsTabs, &leapmuxv1.WorkspaceTab{
			Tab:         &leapmuxv1.TabIdent{TabId: t.GetTabId(), TabType: t.GetTabType()},
			WorkspaceId: wsID,
			TileId:      tileID,
			WorkerId:    t.GetWorkerId().GetValue(),
		})
	}
	slices.SortFunc(wsTabs, func(a, b *leapmuxv1.WorkspaceTab) int {
		return strings.Compare(a.GetTab().GetTabId(), b.GetTab().GetTabId())
	})
	details := fetchTabDetails(s.ctx, s.c, wsTabs)

	// Emit tabs ordered by numeric leaf traversal order then LexoRank position.
	for _, tid := range tileOrder {
		for _, ltTab := range lt.TabsByTile[tid] {
			rec := cc.bs.State.GetTabs()[ltTab.ID]
			workerID := ""
			if rec != nil {
				workerID = rec.GetWorkerId().GetValue()
			}
			det := details[ltTab.ID]
			tabDef := templateTab{
				TileRef:  tileRefByID[tid],
				Type:     ltTab.Type,
				Title:    det.Title,
				WorkerID: workerID,
			}
			switch ltTab.Type {
			case "agent":
				if det.Agent == nil {
					return nil, cli.Newf(cli.CodeUnavailable, "agent tab %s could not be read from its worker; cannot export a valid template", cli.ShortID(ltTab.ID))
				}
				if alias := agentlabels.CLIAlias(det.Agent.GetAgentProvider()); alias != "" {
					tabDef.Provider = alias
				} else {
					tabDef.Provider = agentProviderName(det.Agent.GetAgentProvider())
				}
				tabDef.WorkingDir = det.Agent.GetWorkingDir()
				opts := clonedAgentOptions(det.Agent.GetOptionGroups())
				tabDef.Model = opts[optionids.Model]
				tabDef.Effort = opts[optionids.Effort]
				tabDef.PermissionMode = opts[optionids.PermissionMode]
				delete(opts, optionids.Model)
				delete(opts, optionids.Effort)
				delete(opts, optionids.PermissionMode)
				if len(opts) > 0 {
					tabDef.Options = opts
				}
			case "terminal":
				if det.Terminal != nil {
					tabDef.WorkingDir = det.Terminal.GetWorkingDir()
				}
			case "file":
				if det.Payload != nil {
					tabDef.WorkingDir = det.Payload.GetWorkingDir()
					tabDef.Path = det.Payload.GetFile().GetFilePath()
				}
				if tabDef.Path == "" {
					return nil, cli.Newf(cli.CodeUnavailable, "file tab %s has no readable path on its worker; cannot export a valid template", cli.ShortID(ltTab.ID))
				}
				if rec != nil {
					if dm := leapmuxv1.TabDisplayMode(rec.GetDisplayMode().GetValue()); dm != leapmuxv1.TabDisplayMode_TAB_DISPLAY_MODE_UNSPECIFIED {
						tabDef.DisplayMode = enumToken(dm)
					}
					if vm := leapmuxv1.TabFileViewMode(rec.GetFileViewMode().GetValue()); vm != leapmuxv1.TabFileViewMode_TAB_FILE_VIEW_MODE_UNSPECIFIED {
						tabDef.ViewMode = enumToken(vm)
					}
					if db := rec.GetFileDiffBase().GetValue(); db != "" {
						tabDef.DiffBase = db
					}
				}
			default:
				// Skip image tabs that refer to transient message seqs.
				continue
			}
			tmpl.Tabs = append(tmpl.Tabs, tabDef)
		}
	}

	if outPath != "" {
		raw, err := tmpl.marshalStyled()
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			return nil, cli.Wrap(cli.CodeInternal, err, "create directory for exported workspace template")
		}
		_ = repopaths.EnsureTmpExcludedIfInside(outPath)
		if err := atomicfile.WriteFile(outPath, raw, 0o600); err != nil {
			return nil, cli.Wrap(cli.CodeInternal, err, "write exported workspace template")
		}
		return workspaceExportFileResult{
			WorkspaceID: wsID,
			Title:       tmpl.Title,
			Output:      outPath,
			Style:       style,
			Tabs:        len(tmpl.Tabs),
		}, nil
	}
	return tmpl, nil
}

type workspaceApplyResult struct {
	WorkspaceID     string              `json:"workspace_id,omitempty"`
	Title           string              `json:"title"`
	Created         bool                `json:"created,omitempty"`
	DryRun          bool                `json:"dry_run,omitempty"`
	FloatingWindows int                 `json:"floating_windows,omitempty"`
	Tabs            []openedTab         `json:"tabs,omitempty"`
	PlannedTabs     int                 `json:"planned_tabs,omitempty"`
	PlannedTree     *templateLayoutNode `json:"planned_tree,omitempty"`
	PlannedTabDefs  []templateTab       `json:"planned_tab_defs,omitempty"`
}

func (r workspaceApplyResult) QuietLines() []string {
	if r.WorkspaceID == "" {
		return nil
	}
	return []string{r.WorkspaceID}
}

func collectTemplateTileRefs(n *templateLayoutNode, seen map[string]bool) error {
	if n == nil {
		return nil
	}
	if n.TileRef != "" {
		if !(&layoutNode{Kind: n.Kind}).isLeaf() {
			return fmt.Errorf("tile_ref %q is only allowed on leaf nodes, not %q", n.TileRef, n.Kind)
		}
		if seen[n.TileRef] {
			return fmt.Errorf("duplicate tile_ref %q", n.TileRef)
		}
		seen[n.TileRef] = true
	}
	for _, ch := range n.Children {
		if err := collectTemplateTileRefs(ch, seen); err != nil {
			return err
		}
	}
	return nil
}

func validateFiniteFloat(v float64, field string) error {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return cli.Usagef("%s must be a finite number", field)
	}
	return nil
}

func (n *templateLayoutNode) toLayoutNode() *layoutNode {
	if n == nil {
		return nil
	}
	out := &layoutNode{
		TileRef:   n.TileRef,
		Kind:      n.Kind,
		Direction: n.Direction,
		Ratios:    n.Ratios,
		Rows:      n.Rows,
		Cols:      n.Cols,
		RowRatios: n.RowRatios,
		ColRatios: n.ColRatios,
	}
	for _, ch := range n.Children {
		out.Children = append(out.Children, ch.toLayoutNode())
	}
	return out
}

func parseWorkspaceTemplate(data []byte) (*workspaceTemplate, error) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, cli.Usagef("template input is empty")
	}
	var tmpl workspaceTemplate
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&tmpl); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, cli.Usagef("template input is empty")
		}
		return nil, cli.Wrap(cli.CodeUsage, err, "parse workspace template")
	}
	if tmpl.Version != 0 && tmpl.Version != 1 {
		return nil, cli.Usagef("unsupported workspace template version %d (supported: 1)", tmpl.Version)
	}
	if tmpl.Tree == nil {
		tmpl.Tree = &templateLayoutNode{Kind: "leaf", TileRef: "t1"}
	}
	mainTree := tmpl.Tree.toLayoutNode()
	if err := validateAndNormalizeTree(mainTree, "tree"); err != nil {
		return nil, cli.Usagef("%v", err)
	}
	tmpl.parsedTree = mainTree
	tileRefs := map[string]bool{}
	if err := collectTemplateTileRefs(tmpl.Tree, tileRefs); err != nil {
		return nil, cli.Usagef("%v", err)
	}
	for i := range tmpl.FloatingWindows {
		fw := &tmpl.FloatingWindows[i]
		if err := validateFiniteFloat(fw.X, fmt.Sprintf("floating_windows[%d].x", i)); err != nil {
			return nil, err
		}
		if err := validateFiniteFloat(fw.Y, fmt.Sprintf("floating_windows[%d].y", i)); err != nil {
			return nil, err
		}
		if err := validateFiniteFloat(fw.Width, fmt.Sprintf("floating_windows[%d].width", i)); err != nil {
			return nil, err
		}
		if err := validateFiniteFloat(fw.Height, fmt.Sprintf("floating_windows[%d].height", i)); err != nil {
			return nil, err
		}
		if fw.Width <= 0 || fw.Height <= 0 {
			return nil, cli.Usagef("floating_windows[%d]: width and height must be > 0", i)
		}
		if fw.Opacity != nil {
			if err := validateFiniteFloat(*fw.Opacity, fmt.Sprintf("floating_windows[%d].opacity", i)); err != nil {
				return nil, err
			}
			if *fw.Opacity < 0 || *fw.Opacity > 1 {
				return nil, cli.Usagef("floating_windows[%d]: opacity must be between 0.0 and 1.0; got %v", i, *fw.Opacity)
			}
		}
		if fw.Tree == nil {
			fw.Tree = &templateLayoutNode{Kind: "leaf"}
		}
		fwTree := fw.Tree.toLayoutNode()
		if err := validateAndNormalizeTree(fwTree, fmt.Sprintf("floating_windows[%d].tree", i)); err != nil {
			return nil, cli.Usagef("%v", err)
		}
		fw.parsedTree = fwTree
		if err := collectTemplateTileRefs(fw.Tree, tileRefs); err != nil {
			return nil, cli.Usagef("floating_windows[%d]: %v", i, err)
		}
	}
	for i := range tmpl.Tabs {
		tab := &tmpl.Tabs[i]
		if tab.TileRef != "" && !tileRefs[tab.TileRef] {
			return nil, cli.Usagef("tabs[%d]: unknown tile_ref %q (not declared in tree or floating_windows)", i, tab.TileRef)
		}
		switch strings.ToLower(strings.TrimSpace(tab.Type)) {
		case "agent":
			if p := strings.TrimSpace(tab.Provider); p != "" {
				if _, ok := agentlabels.ParseProvider(p); !ok {
					return nil, cli.Usagef("tabs[%d]: unknown provider %q", i, p)
				}
			}
		case "terminal":
		case "file":
			if strings.TrimSpace(tab.Path) == "" {
				return nil, cli.Usagef("tabs[%d]: file tab requires path", i)
			}
			pres, err := parseTabPresentation(tab.DisplayMode, tab.ViewMode, tab.DiffBase, tab.DiffBase != "")
			if err != nil {
				return nil, cli.Usagef("tabs[%d]: %s", i, cli.FromError(err).Message)
			}
			tab.presentation = pres
		default:
			return nil, cli.Usagef("tabs[%d]: unsupported tab type %q (want agent, terminal, or file)", i, tab.Type)
		}
	}
	return &tmpl, nil
}

// postOrderLiveSubtree lists every live node in rootID's subtree leaves-first,
// using a precomputed LiveChildrenByParent map to avoid repeated scans.
func postOrderLiveSubtree(state *leapmuxv1.UserMaterialized, children map[string][]string, rootID string) []string {
	var out []string
	visited := map[string]bool{}
	var walk func(nodeID string)
	walk = func(nodeID string) {
		if nodeID == "" || visited[nodeID] {
			return
		}
		rec := state.GetNodes()[nodeID]
		if rec == nil || !crdt.HLCIsZero(rec.GetTombstoneAt()) {
			return
		}
		visited[nodeID] = true
		kids := slices.Clone(children[nodeID])
		slices.Sort(kids)
		for _, childID := range kids {
			walk(childID)
		}
		out = append(out, nodeID)
	}
	walk(rootID)
	return out
}

// workspaceApplyPartialError wraps a tab-opening failure after the layout was
// committed while preserving the cause's CLI code, hints, candidates, retry
// receipt, and custom exit code.
func workspaceApplyPartialError(wsID string, openedCount, totalTabs, tabIdx int, cause error) error {
	base := cli.FromError(cause)
	prefix := fmt.Sprintf("workspace %s applied layout and opened %d/%d tab(s), but tabs[%d] failed", wsID, openedCount, totalTabs, tabIdx)
	out := cli.Wrap(base.Code, cause, prefix)
	if base.Message != "" {
		out.Message = prefix + ": " + base.Message
	}
	out.Hints = append([]string(nil), base.Hints...)
	out.Candidates = append([]cli.Candidate(nil), base.Candidates...)
	out.Receipt = base.Receipt
	if code := base.ExitCode(); code != cli.ExitCode(base.Code) {
		out = out.WithExitCode(code)
	}
	return out.WithHint(`inspect workspace with "leapmux tab list --workspace %s"`, wsID)
}

func runWorkspaceApply(inv *cli.Invocation) (any, error) {
	data, err := readFileOrStdin(inv.Stdin, inv.Arg("file"), "template file")
	if err != nil {
		return nil, err
	}
	tmpl, err := parseWorkspaceTemplate(data)
	if err != nil {
		return nil, err
	}
	title := strings.TrimSpace(tmpl.Title)
	if inv.Changed("title") {
		title = strings.TrimSpace(stringFlag(inv, "title"))
		if title == "" {
			return nil, cli.Usagef("--title must not be empty")
		}
	}
	if title == "" {
		title = "Workspace"
	}

	if boolFlag(inv, "dry-run") {
		return workspaceApplyResult{
			Title:           title,
			DryRun:          true,
			FloatingWindows: len(tmpl.FloatingWindows),
			PlannedTabs:     len(tmpl.Tabs),
			PlannedTree:     tmpl.Tree,
			PlannedTabDefs:  tmpl.Tabs,
		}, nil
	}

	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()

	// Resolve default worker override (or fallback when tabs omit worker_id).
	defaultWorkerID := ""
	if inv.Changed(workerFlag) {
		defaultWorkerID, err = s.workerScope()
		if err != nil {
			return nil, err
		}
	} else if wid, werr := s.workerScope(); werr == nil {
		defaultWorkerID = wid
	}

	// Validate all tab worker assignments up front before mutating any workspace.
	resolvedTabWorkers := make([]string, len(tmpl.Tabs))
	for i, tab := range tmpl.Tabs {
		wid := tab.WorkerID
		if inv.Changed(workerFlag) || wid == "" {
			wid = defaultWorkerID
		}
		if wid == "" {
			return nil, cli.Usagef("tabs[%d]: no worker_id specified in template and no default worker selected", i).
				WithHint("pass --worker <worker>")
		}
		resolvedTabWorkers[i] = wid
	}

	// Verify that each target Worker exists on the Hub before creating or
	// mutating workspace state (online reachability is checked when channels open).
	preflighted := map[string]bool{}
	for _, wid := range resolvedTabWorkers {
		if preflighted[wid] {
			continue
		}
		preflighted[wid] = true
		if err := maybePreflightWorker(s.ctx, s.c, wid); err != nil {
			return nil, err
		}
	}

	var wsID string
	created := false
	if inv.Changed("workspace") {
		target, err := s.resolveOne(resolve.KindWorkspace, stringFlag(inv, "workspace"))
		if err != nil {
			return nil, err
		}
		wsID = target.ID
		if inv.Changed("title") && title != "" {
			if err := hubCallUnary(s.ctx, s.c, leapmuxv1connect.WorkspaceServiceRenameWorkspaceProcedure,
				&leapmuxv1.RenameWorkspaceRequest{WorkspaceId: wsID, Title: title}, &leapmuxv1.RenameWorkspaceResponse{}); err != nil {
				return nil, err
			}
		}
	} else {
		var createResp leapmuxv1.CreateWorkspaceResponse
		if err := hubCallUnary(s.ctx, s.c, leapmuxv1connect.WorkspaceServiceCreateWorkspaceProcedure, &leapmuxv1.CreateWorkspaceRequest{Title: title}, &createResp); err != nil {
			return nil, err
		}
		wsID = createResp.GetWorkspaceId()
		created = true
	}

	cc, err := s.openCRDT(wsID)
	if err != nil {
		return nil, err
	}

	tileMap, firstLeaf, err := submitLayoutRewriteWithRetry(cc, wsID, tmpl.parsedTree, func(bs *CRDTBootstrap, firstLeaf string, tileMap map[string]string) []*leapmuxv1.CrdtOp {
		var ops []*leapmuxv1.CrdtOp
		children := crdt.LiveChildrenByParent(bs.State)

		// Replace existing floating windows on --workspace <existing> so
		// applying twice does not duplicate them.
		var existingWinIDs []string
		for winID, fw := range bs.State.GetFloatingWindows() {
			if fw == nil || !crdt.HLCIsZero(fw.GetTombstoneAt()) || fw.GetWorkspaceId().GetValue() != wsID {
				continue
			}
			existingWinIDs = append(existingWinIDs, winID)
		}
		slices.Sort(existingWinIDs)

		tombstonedFWNodes := map[string]bool{}
		for _, winID := range existingWinIDs {
			fw := bs.State.GetFloatingWindows()[winID]
			if fwRoot := fw.GetRootNodeId(); fwRoot != "" {
				for _, nid := range postOrderLiveSubtree(bs.State, children, fwRoot) {
					tombstonedFWNodes[nid] = true
					ops = append(ops, opTombstoneNode(bs, nid))
				}
			}
			ops = append(ops, opTombstoneWindow(bs, winID))
		}
		if len(tombstonedFWNodes) > 0 {
			var migratedTabs []*leapmuxv1.TabRecord
			for _, t := range bs.State.GetTabs() {
				if t == nil || !crdt.HLCIsZero(t.GetTombstoneAt()) {
					continue
				}
				if tombstonedFWNodes[t.GetTileId().GetValue()] {
					migratedTabs = append(migratedTabs, t)
				}
			}
			slices.SortFunc(migratedTabs, func(a, b *leapmuxv1.TabRecord) int {
				return strings.Compare(a.GetTabId(), b.GetTabId())
			})
			for _, t := range migratedTabs {
				ops = append(ops, opSetTabTileID(bs, t.GetTabType(), t.GetTabId(), firstLeaf))
			}
		}

		for _, fw := range tmpl.FloatingWindows {
			winID := id.Generate()
			fwRootID := id.Generate()
			fwTree := fw.parsedTree
			ops = appendNodeShapeOps(ops, bs, fwRootID, fwTree)
			if fwTree.isLeaf() {
				if fwTree.TileRef != "" {
					tileMap[fwTree.TileRef] = fwRootID
				}
			} else {
				rank := lexorank.First()
				grid := fwTree.parsedKind() == leapmuxv1.NodeKind_NODE_KIND_GRID
				for index, child := range fwTree.Children {
					pos := rank
					if grid {
						cols := int(fwTree.Cols)
						pos = crdt.GridCellPosition(index/cols, index%cols)
					} else {
						rank = lexorank.After(rank)
					}
					subOps, _ := synthesizeSubtree(bs, child, fwRootID, pos, tileMap)
					ops = append(ops, subOps...)
				}
			}
			ops = appendCreateFloatingWindowOps(ops, bs, winID, wsID, fwRootID, fw.X, fw.Y, fw.Width, fw.Height, fw.effectiveOpacity())
		}
		return ops
	})
	if err != nil {
		return nil, err
	}

	// Group tab indices by Worker so each Worker opens ONE E2EE channel, run
	// distinct Workers concurrently, and preserve template tab ordering in the
	// result slice.
	opened := make([]openedTab, len(tmpl.Tabs))
	type indexedTab struct {
		index int
		tab   templateTab
	}
	var workerOrder []string
	tabsByWorker := map[string][]indexedTab{}
	for i, tab := range tmpl.Tabs {
		wid := resolvedTabWorkers[i]
		if _, seen := tabsByWorker[wid]; !seen {
			workerOrder = append(workerOrder, wid)
		}
		tabsByWorker[wid] = append(tabsByWorker[wid], indexedTab{index: i, tab: tab})
	}

	type workerFailure struct {
		tabIdx int
		err    error
	}
	failures := make([]workerFailure, len(workerOrder))
	var openedCount atomic.Int64
	g, gctx := errgroup.WithContext(s.ctx)
	g.SetLimit(detailFanoutLimit)
	for wi, workerID := range workerOrder {
		items := tabsByWorker[workerID]
		g.Go(func() error {
			failedIdx := items[0].index
			var (
				defaultProviderLoaded bool
				defaultProvider       leapmuxv1.AgentProvider
			)
			err := withWorkerChannel(gctx, s.c, workerID, func(w workerCall) error {
				for _, item := range items {
					tab := item.tab
					tileID := firstLeaf
					if tab.TileRef != "" {
						tileID = tileMap[tab.TileRef]
					}
					var gitSetup *leapmuxv1.GitSetup
					if tab.Worktree != "" {
						gitSetup = &leapmuxv1.GitSetup{
							Mode: &leapmuxv1.GitSetup_CreateWorktree{
								CreateWorktree: &leapmuxv1.GitCreateWorktree{
									Branch:     tab.Worktree,
									BaseBranch: tab.BaseBranch,
								},
							},
						}
					}
					switch strings.ToLower(strings.TrimSpace(tab.Type)) {
					case "agent":
						var provider leapmuxv1.AgentProvider
						var perr error
						if strings.TrimSpace(tab.Provider) == "" {
							if !defaultProviderLoaded {
								defaultProvider, perr = resolveProvider(gctx, "", w.Call)
								if perr != nil {
									failedIdx = item.index
									return perr
								}
								defaultProviderLoaded = true
							}
							provider = defaultProvider
						} else {
							provider, perr = resolveProvider(gctx, tab.Provider, w.Call)
							if perr != nil {
								failedIdx = item.index
								return perr
							}
						}
						opts := spawnOptions(tab.Model, tab.Effort, tab.PermissionMode)
						for k, v := range tab.Options {
							opts[k] = v
						}
						var initMsg *string
						if tab.InitialMessage != "" {
							m := tab.InitialMessage
							initMsg = &m
						}
						res, perr := openAgentAndAddTab(gctx, s.c, w, openAgentArgs{
							WorkspaceID:    wsID,
							WorkerID:       workerID,
							TileID:         tileID,
							Provider:       provider,
							Title:          tab.Title,
							Options:        opts,
							WorkingDir:     tab.WorkingDir,
							InitialMessage: initMsg,
							GitSetup:       gitSetup,
							Position:       positionSpec{kind: positionLast},
						})
						if perr != nil {
							failedIdx = item.index
							return perr
						}
						opened[item.index] = openedTab{
							ID:          res.Agent.GetId(),
							Type:        "agent",
							Title:       res.Agent.GetTitle(),
							WorkspaceID: wsID,
							WorkerID:    workerID,
							TileID:      res.TileID,
							Position:    res.Position,
						}
						openedCount.Add(1)
					case "terminal":
						res, terr := openTerminalAndAddTab(gctx, s.c, w, openTerminalArgs{
							WorkspaceID:    wsID,
							WorkerID:       workerID,
							TileID:         tileID,
							WorkingDir:     tab.WorkingDir,
							Shell:          tab.Shell,
							Title:          tab.Title,
							GitSetup:       gitSetup,
							InitialCommand: tab.InitialCommand,
							Position:       positionSpec{kind: positionLast},
						})
						if terr != nil {
							failedIdx = item.index
							return terr
						}
						opened[item.index] = openedTab{
							ID:          res.TerminalID,
							Type:        "terminal",
							Title:       res.Title,
							WorkspaceID: wsID,
							WorkerID:    workerID,
							TileID:      res.TileID,
							Position:    res.Position,
						}
						openedCount.Add(1)
					case "file":
						creation, cerr := resolveTabCreation(gctx, s.c, id.Generate(), wsID, tileID, positionSpec{kind: positionLast})
						if cerr != nil {
							failedIdx = item.index
							return cerr
						}
						creation.Presentation = tab.presentation
						regResp, ferr := registerTabPayload(gctx, w, leapmuxv1.TabType_TAB_TYPE_FILE, creation, &leapmuxv1.TabPayload{
							WorkingDir: tab.WorkingDir,
							Kind: &leapmuxv1.TabPayload_File{
								File: &leapmuxv1.FileTabPayload{FilePath: tab.Path},
							},
						}, false)
						if ferr != nil {
							failedIdx = item.index
							return ferr
						}
						pub := regResp.GetPublication()
						opened[item.index] = openedTab{
							ID:          creation.GetTabId(),
							Type:        "file",
							WorkspaceID: wsID,
							WorkerID:    workerID,
							TileID:      pub.GetTileId(),
							Position:    pub.GetPosition(),
							Path:        tab.Path,
						}
						openedCount.Add(1)
					}
				}
				return nil
			})
			if err != nil {
				failures[wi] = workerFailure{tabIdx: failedIdx, err: err}
				return err
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		failedIdx := 0
		for _, f := range failures {
			if f.err == err {
				failedIdx = f.tabIdx
				break
			}
		}
		return nil, workspaceApplyPartialError(wsID, int(openedCount.Load()), len(tmpl.Tabs), failedIdx, err)
	}

	return workspaceApplyResult{
		WorkspaceID:     wsID,
		Title:           title,
		Created:         created,
		FloatingWindows: len(tmpl.FloatingWindows),
		Tabs:            opened,
	}, nil
}
