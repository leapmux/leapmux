package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"go.yaml.in/yaml/v4"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/util/agentlabels"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/lexorank"
	"github.com/leapmux/leapmux/internal/util/optionids"
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
	style string
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
	Opacity float64             `json:"opacity,omitempty" yaml:"opacity,omitempty"`
	Tree    *templateLayoutNode `json:"tree" yaml:"tree"`
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
	wsTarget, err := s.resolveOne(resolve.KindWorkspace, wsInput)
	if err != nil {
		return nil, err
	}
	var wsResp leapmuxv1.GetWorkspaceResponse
	if err := hubCallUnary(s.ctx, s.c, leapmuxv1connect.WorkspaceServiceGetWorkspaceProcedure, &leapmuxv1.GetWorkspaceRequest{WorkspaceId: wsTarget.ID}, &wsResp); err != nil {
		return nil, err
	}
	cc, err := s.openCRDT(wsTarget.ID)
	if err != nil {
		return nil, err
	}
	lt := buildLayoutTreeForWorkspace(cc.bs.State, wsTarget.ID, true)

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
		tmpl.FloatingWindows = append(tmpl.FloatingWindows, templateFloatingWindow{
			X:       rec.GetX().GetValue(),
			Y:       rec.GetY().GetValue(),
			Width:   rec.GetWidth().GetValue(),
			Height:  rec.GetHeight().GetValue(),
			Opacity: rec.GetOpacity().GetValue(),
			Tree:    convertNode(fw.Tree),
		})
	}

	// Collect all live workspace tabs and hydrate from Workers.
	wsTabs, err := listTabsInWorkspaces(s.ctx, s.c, []string{wsTarget.ID})
	if err != nil {
		return nil, err
	}
	details := fetchTabDetails(s.ctx, s.c, wsTabs)
	wsTabByID := map[string]*leapmuxv1.WorkspaceTab{}
	fileTabsByWorker := map[string][]string{}
	for _, t := range wsTabs {
		tid := t.GetTab().GetTabId()
		wsTabByID[tid] = t
		if t.GetTab().GetTabType() == leapmuxv1.TabType_TAB_TYPE_FILE && t.GetWorkerId() != "" {
			fileTabsByWorker[t.GetWorkerId()] = append(fileTabsByWorker[t.GetWorkerId()], tid)
		}
	}

	// Batch file payload reads over ONE E2EE channel per Worker so paths are
	// preserved without N handshakes, and fail clearly if a file path cannot be hydrated.
	filePayloads := map[string]*leapmuxv1.TabPayload{}
	for wkID, tabIDs := range fileTabsByWorker {
		if err := withWorkerChannel(s.ctx, s.c, wkID, func(w workerCall) error {
			for _, tid := range tabIDs {
				var resp leapmuxv1.GetTabPayloadResponse
				if err := w.Call(s.ctx, leapmuxv1connect.WorkerPrivateServiceGetTabPayloadProcedure, &leapmuxv1.GetTabPayloadRequest{TabId: tid}, &resp); err != nil {
					return fmt.Errorf("read file tab %s payload on worker %s: %w", cli.ShortID(tid), cli.ShortID(wkID), err)
				}
				filePayloads[tid] = resp.GetPayload()
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}

	// Emit tabs ordered by numeric leaf traversal order then LexoRank position.
	for _, tid := range tileOrder {
		for _, ltTab := range lt.TabsByTile[tid] {
			rec := cc.bs.State.GetTabs()[ltTab.ID]
			wt := wsTabByID[ltTab.ID]
			workerID := ""
			if wt != nil {
				workerID = wt.GetWorkerId()
			} else if rec != nil {
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
				if payload := filePayloads[ltTab.ID]; payload != nil {
					tabDef.WorkingDir = payload.GetWorkingDir()
					tabDef.Path = payload.GetFile().GetFilePath()
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
				// Skip image tabs that reference transient message seqs.
				continue
			}
			tmpl.Tabs = append(tmpl.Tabs, tabDef)
		}
	}

	if outPath := strings.TrimSpace(stringFlag(inv, "output")); outPath != "" {
		raw, err := tmpl.marshalStyled()
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(outPath, raw, 0o600); err != nil {
			return nil, cli.Wrap(cli.CodeInternal, err, "write exported workspace template")
		}
		return workspaceExportFileResult{
			WorkspaceID: wsTarget.ID,
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

func (n *templateLayoutNode) toLayoutNode() *layoutNode {
	if n == nil {
		return nil
	}
	out := &layoutNode{
		NodeID:    n.TileRef,
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
	if err := yaml.Unmarshal(data, &tmpl); err != nil {
		return nil, cli.Wrap(cli.CodeUsage, err, "parse workspace template")
	}
	if tmpl.Version != 0 && tmpl.Version != 1 {
		return nil, cli.Usagef("unsupported workspace template version %d (supported: 1)", tmpl.Version)
	}
	if tmpl.Tree == nil {
		tmpl.Tree = &templateLayoutNode{Kind: "leaf", TileRef: "t1"}
	}
	if err := validateAndNormalizeTree(tmpl.Tree.toLayoutNode(), "tree"); err != nil {
		return nil, cli.Usagef("%v", err)
	}
	tileRefs := map[string]bool{}
	if err := collectTemplateTileRefs(tmpl.Tree, tileRefs); err != nil {
		return nil, cli.Usagef("%v", err)
	}
	for i, fw := range tmpl.FloatingWindows {
		if fw.Width <= 0 || fw.Height <= 0 {
			return nil, cli.Usagef("floating_windows[%d]: width and height must be > 0", i)
		}
		if fw.Tree == nil {
			tmpl.FloatingWindows[i].Tree = &templateLayoutNode{Kind: "leaf"}
		}
		if err := validateAndNormalizeTree(tmpl.FloatingWindows[i].Tree.toLayoutNode(), fmt.Sprintf("floating_windows[%d].tree", i)); err != nil {
			return nil, cli.Usagef("%v", err)
		}
		if err := collectTemplateTileRefs(tmpl.FloatingWindows[i].Tree, tileRefs); err != nil {
			return nil, cli.Usagef("floating_windows[%d]: %v", i, err)
		}
	}
	for i, tab := range tmpl.Tabs {
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
			if dm := strings.TrimSpace(tab.DisplayMode); dm != "" {
				if _, ok := parseEnumToken(leapmuxv1.TabDisplayMode_TAB_DISPLAY_MODE_UNSPECIFIED, dm); !ok {
					return nil, cli.Usagef("tabs[%d]: invalid display_mode %q", i, dm)
				}
			}
			if vm := strings.TrimSpace(tab.ViewMode); vm != "" {
				if _, ok := parseEnumToken(leapmuxv1.TabFileViewMode_TAB_FILE_VIEW_MODE_UNSPECIFIED, vm); !ok {
					return nil, cli.Usagef("tabs[%d]: invalid view_mode %q", i, vm)
				}
			}
		default:
			return nil, cli.Usagef("tabs[%d]: unsupported tab type %q (want agent, terminal, or file)", i, tab.Type)
		}
	}
	return &tmpl, nil
}

// synthesizeTemplateSubtree mirrors synthesizeSubtree while capturing the
// mapping from template tile_ref labels to generated CRDT node IDs.
func synthesizeTemplateSubtree(bs *CRDTBootstrap, parentID string, parent *layoutNode, tileMap map[string]string) (ops []*leapmuxv1.CrdtOp, firstLeaf string) {
	rank := lexorank.First()
	grid := parent.parsedKind() == leapmuxv1.NodeKind_NODE_KIND_GRID
	for index, child := range parent.Children {
		position := rank
		if grid {
			cols := int(parent.Cols)
			position = crdt.GridCellPosition(index/cols, index%cols)
		} else {
			rank = lexorank.After(rank)
		}
		childID := id.Generate()
		ops = append(ops,
			opSetNodeKind(bs, childID, child.parsedKind()),
			opSetNodeParentID(bs, childID, parentID),
			opSetNodePosition(bs, childID, position),
		)
		ops = appendNodeShapeOps(ops, bs, childID, child)
		if child.isLeaf() {
			if firstLeaf == "" {
				firstLeaf = childID
			}
			if child.NodeID != "" {
				tileMap[child.NodeID] = childID
			}
		}
		grandOps, deeperFirst := synthesizeTemplateSubtree(bs, childID, child, tileMap)
		ops = append(ops, grandOps...)
		if firstLeaf == "" && deeperFirst != "" {
			firstLeaf = deeperFirst
		}
	}
	return ops, firstLeaf
}

func runWorkspaceApply(inv *cli.Invocation) (any, error) {
	path := inv.Arg("file")
	var data []byte
	var err error
	if path == "-" {
		data, err = readStdinGuarded(inv.Stdin)
		if err != nil {
			return nil, err
		}
	} else {
		data, err = os.ReadFile(path)
		if err != nil {
			return nil, cli.Wrap(cli.CodeUsage, err, "read template file")
		}
	}
	tmpl, err := parseWorkspaceTemplate(data)
	if err != nil {
		return nil, err
	}
	title := strings.TrimSpace(tmpl.Title)
	if inv.Changed("title") {
		title = strings.TrimSpace(stringFlag(inv, "title"))
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

	// Preflight every target Worker before creating or mutating Hub workspace
	// state so an offline Worker never leaves behind a half-applied workspace.
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
	rec := cc.bs.State.GetWorkspaces()[wsID]
	if rec == nil || rec.GetRootNodeId() == "" {
		return nil, cli.Newf(cli.CodeConflict, "workspace %s has no root node", wsID)
	}
	rootID := rec.GetRootNodeId()

	mainTree := tmpl.Tree.toLayoutNode()
	_ = validateAndNormalizeTree(mainTree, "tree")

	tileMap := map[string]string{}
	ops := []*leapmuxv1.CrdtOp{}
	ops = appendNodeShapeOps(ops, cc.bs, rootID, mainTree)

	tombstones := crdt.DescendantsLeavesFirst(cc.bs.State, rootID)
	replacedNodes := map[string]bool{rootID: true}
	for _, nid := range tombstones {
		replacedNodes[nid] = true
		if nid == rootID {
			continue
		}
		ops = append(ops, opTombstoneNode(cc.bs, nid))
	}

	// Replace existing floating windows on --workspace <existing> so applying twice does not duplicate them.
	for winID, fw := range cc.bs.State.GetFloatingWindows() {
		if fw == nil || !crdt.HLCIsZero(fw.GetTombstoneAt()) || fw.GetWorkspaceId().GetValue() != wsID {
			continue
		}
		fwRoot := fw.GetRootNodeId()
		if fwRoot != "" {
			for _, nid := range crdt.DescendantsLeavesFirst(cc.bs.State, fwRoot) {
				replacedNodes[nid] = true
				ops = append(ops, opTombstoneNode(cc.bs, nid))
			}
		}
		ops = append(ops, opTombstoneWindow(cc.bs, winID))
	}

	firstLeaf := rootID
	if !mainTree.isLeaf() {
		subOps, fl := synthesizeTemplateSubtree(cc.bs, rootID, mainTree, tileMap)
		ops = append(ops, subOps...)
		firstLeaf = fl
	} else if mainTree.NodeID != "" {
		tileMap[mainTree.NodeID] = rootID
	}

	for _, t := range cc.bs.State.GetTabs() {
		if t == nil || !crdt.HLCIsZero(t.GetTombstoneAt()) {
			continue
		}
		if replacedNodes[t.GetTileId().GetValue()] {
			ops = append(ops, opSetTabTileID(cc.bs, t.GetTabType(), t.GetTabId(), firstLeaf))
		}
	}

	for _, fw := range tmpl.FloatingWindows {
		winID := id.Generate()
		fwRootID := id.Generate()
		fwTree := fw.Tree.toLayoutNode()
		_ = validateAndNormalizeTree(fwTree, "floating_window.tree")
		opacity := fw.Opacity
		if opacity <= 0 || opacity > 1 {
			opacity = 1.0
		}
		ops = append(ops,
			opSetNodeKind(cc.bs, fwRootID, fwTree.parsedKind()),
			opSetWindowWorkspaceID(cc.bs, winID, wsID),
			opSetWindowRootNodeID(cc.bs, winID, fwRootID),
			opSetWindowX(cc.bs, winID, fw.X),
			opSetWindowY(cc.bs, winID, fw.Y),
			opSetWindowWidth(cc.bs, winID, fw.Width),
			opSetWindowHeight(cc.bs, winID, fw.Height),
			opSetWindowOpacity(cc.bs, winID, opacity),
		)
		ops = appendNodeShapeOps(ops, cc.bs, fwRootID, fwTree)
		if fwTree.isLeaf() {
			if fwTree.NodeID != "" {
				tileMap[fwTree.NodeID] = fwRootID
			}
		} else {
			subOps, _ := synthesizeTemplateSubtree(cc.bs, fwRootID, fwTree, tileMap)
			ops = append(ops, subOps...)
		}
	}

	if err := cc.submitOps(ops); err != nil {
		return nil, err
	}

	// Group tab indices by Worker so we reuse ONE E2EE channel per Worker,
	// while still preserving template tab ordering in the result slice.
	opened := make([]openedTab, len(tmpl.Tabs))
	openedCount := 0
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

	partialApplyErr := func(tabIdx int, cause error) error {
		e := cli.FromError(cause)
		return cli.Wrap(e.Code, cause, fmt.Sprintf("workspace %s applied layout and opened %d/%d tab(s), but tabs[%d] failed", wsID, openedCount, len(tmpl.Tabs), tabIdx)).
			WithHint(`inspect workspace with "leapmux tab list --workspace %s"`, wsID)
	}

	for _, workerID := range workerOrder {
		if err := maybePreflightWorker(s.ctx, s.c, workerID); err != nil {
			return nil, partialApplyErr(tabsByWorker[workerID][0].index, err)
		}
		var failedIdx int
		err := withWorkerChannel(s.ctx, s.c, workerID, func(w workerCall) error {
			for _, item := range tabsByWorker[workerID] {
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
					provider, perr := resolveProvider(s.ctx, tab.Provider, w.Call)
					if perr != nil {
						failedIdx = item.index
						return perr
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
					res, perr := openAgentAndAddTab(s.ctx, s.c, w, openAgentArgs{
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
					openedCount++
				case "terminal":
					res, terr := openTerminalAndAddTab(s.ctx, s.c, w, openTerminalArgs{
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
					openedCount++
				case "file":
					creation, cerr := resolveTabCreation(s.ctx, s.c, id.Generate(), wsID, tileID, positionSpec{kind: positionLast})
					if cerr != nil {
						failedIdx = item.index
						return cerr
					}
					pres := &leapmuxv1.TabPresentation{}
					if tab.DisplayMode != "" {
						if n, ok := parseEnumToken(leapmuxv1.TabDisplayMode_TAB_DISPLAY_MODE_UNSPECIFIED, tab.DisplayMode); ok {
							pres.DisplayMode = leapmuxv1.TabDisplayMode(n).Enum()
						}
					}
					if tab.ViewMode != "" {
						if n, ok := parseEnumToken(leapmuxv1.TabFileViewMode_TAB_FILE_VIEW_MODE_UNSPECIFIED, tab.ViewMode); ok {
							pres.FileViewMode = leapmuxv1.TabFileViewMode(n).Enum()
						}
					}
					if tab.DiffBase != "" {
						db := tab.DiffBase
						pres.FileDiffBase = &db
					}
					creation.Presentation = pres
					var regResp leapmuxv1.RegisterTabPayloadResponse
					if ferr := w.Call(s.ctx, leapmuxv1connect.WorkerPrivateServiceRegisterTabPayloadProcedure, &leapmuxv1.RegisterTabPayloadRequest{
						Creation: creation,
						Payload: &leapmuxv1.TabPayload{
							WorkingDir: tab.WorkingDir,
							Kind: &leapmuxv1.TabPayload_File{
								File: &leapmuxv1.FileTabPayload{FilePath: tab.Path},
							},
						},
					}, &regResp); ferr != nil {
						failedIdx = item.index
						return ferr
					}
					if verr := validateTabPublication(creation, leapmuxv1.TabType_TAB_TYPE_FILE, regResp.GetPublication()); verr != nil {
						failedIdx = item.index
						return verr
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
					openedCount++
				}
			}
			return nil
		})
		if err != nil {
			return nil, partialApplyErr(failedIdx, err)
		}
	}

	return workspaceApplyResult{
		WorkspaceID:     wsID,
		Title:           title,
		Created:         created,
		FloatingWindows: len(tmpl.FloatingWindows),
		Tabs:            opened,
	}, nil
}

// Keep agentlabels import available if provider validation is added.
var _ = agentlabels.DisplayName
