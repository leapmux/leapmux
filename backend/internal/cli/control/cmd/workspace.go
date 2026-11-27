package cmd

import (
	"context"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/sync/errgroup"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

func newWorkspaceCommand() *cobra.Command {
	return cli.SetAliases(cli.Group("workspace", "List, create, organize, export, apply, and delete workspaces",
		cli.NewLeaf(cli.Leaf{
			Use:     "list",
			Aliases: []string{"ls"},
			Short:   "List your workspaces",
			Examples: []cli.Example{
				{Command: "leapmux workspace list"},
				{Comment: "Include archived workspaces.", Command: "leapmux workspace list --archived"},
				{Comment: "Print only the ids.", Command: "leapmux workspace list -q"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.Bool("archived", false, "include archived workspaces")
			},
			Run: runWorkspaceList,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:   "get [workspace]",
			Short: "Show one workspace (default: the workspace of this tab)",
			Examples: []cli.Example{
				{Command: "leapmux workspace get"},
				{Command: `leapmux workspace get "title:Release 1.4"`},
			},
			Complete: completeTargets,
			Run:      runWorkspaceGet,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "create <title>",
			Mutates: true,
			Short:   "Create a workspace",
			Next: []string{
				`leapmux agent open --workspace <id>`,
				`leapmux terminal open --workspace <id>`,
				`leapmux tree --workspace <id>`,
			},
			Examples: []cli.Example{
				{Comment: "Create a workspace and keep its id.", Command: `W=$(leapmux workspace create "Release 1.4" -q)`},
			},
			Run: runWorkspaceCreate,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "rename <workspace> <title>",
			Short:   "Rename a workspace",
			Mutates: true,
			Examples: []cli.Example{
				{Command: `leapmux workspace rename self "Release 1.5"`},
			},
			Complete: completeTargets,
			Run:      runWorkspaceRename,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "archive <workspace>",
			Short:   "Archive a workspace (moves it to the Archived section and pauses its tabs)",
			Mutates: true,
			Examples: []cli.Example{
				{Command: "leapmux workspace archive 9f3kQ2aB"},
			},
			Complete: completeTargets,
			Run:      runWorkspaceArchive,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "unarchive <workspace>",
			Short:   "Restore an archived workspace back to an active section",
			Mutates: true,
			Examples: []cli.Example{
				{Command: "leapmux workspace unarchive 9f3kQ2aB"},
				{Command: `leapmux workspace unarchive 9f3kQ2aB --section "In progress"`},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.String("section", "", `destination active section (default: "In progress")`)
			},
			FlagComplete: map[string]cobra.CompletionFunc{
				"section": completeSectionFlag,
			},
			Complete: completeTargets,
			Run:      runWorkspaceUnarchive,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "move <workspace>",
			Aliases: []string{"mv"},
			Short:   "Move a workspace to another sidebar section or reorder it within a section",
			Mutates: true,
			Examples: []cli.Example{
				{Command: `leapmux workspace move 9f3kQ2aB --section "Client Work" --last`},
				{Command: `leapmux workspace move 9f3kQ2aB --section "In progress" --first`},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.String("section", "", "destination sidebar section (id, prefix, or name)")
				cli.MarkRequired(fs, "section")
				fs.Bool("first", false, "put the workspace first in the section")
				fs.Bool("last", false, "put the workspace last in the section")
				fs.String("before", "", "put the workspace before this workspace in the section")
				fs.String("after", "", "put the workspace after this workspace in the section")
			},
			FlagComplete: map[string]cobra.CompletionFunc{
				"section": completeSectionFlag,
				"before":  completeWorkspacePositionFlag,
				"after":   completeWorkspacePositionFlag,
			},
			Complete: completeTargets,
			Run:      runWorkspaceMove,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:         "delete <workspace>",
			Aliases:     []string{"rm"},
			Short:       "Delete a workspace and close every tab in it",
			Mutates:     true,
			Destructive: true,
			Examples: []cli.Example{
				{Command: "leapmux workspace delete 9f3kQ2aB"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.Bool("allow-busy", false, allowBusyFlagHelp)
				fs.Bool(allowSelfFlag, false, "delete the workspace even when this tab lives in it, which ends this tab's process too")
			},
			Complete: completeTargets,
			Run:      runWorkspaceDelete,
		}),
		newWorkspaceExportCommand(),
		newWorkspaceApplyCommand(),
	), "ws")
}

// workspaceRow is one workspace in a result.
type workspaceRow struct {
	ID        string `json:"id" table:"ID,short"`
	Title     string `json:"title" table:"TITLE"`
	Archived  bool   `json:"archived,omitempty"`
	CreatedBy string `json:"created_by,omitempty"`
	CreatedAt string `json:"created_at,omitempty" table:"CREATED,dim"`
}

func workspaceRowOf(ws *leapmuxv1.Workspace) workspaceRow {
	return workspaceRow{
		ID:        ws.GetId(),
		Title:     ws.GetTitle(),
		CreatedBy: ws.GetCreatedBy(),
		CreatedAt: formatTime(ws.GetCreatedAt()),
	}
}

// runWorkspaceList enumerates the caller's own workspaces. The hub takes the
// tenant from the session, so the command needs no target at all. Archived
// workspaces are hidden unless --archived is passed.
func runWorkspaceList(inv *cli.Invocation) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	var (
		resp     leapmuxv1.ListWorkspacesResponse
		sections *sectionIndex
	)
	g, gctx := errgroup.WithContext(s.ctx)
	g.Go(func() error {
		return hubCallUnary(gctx, s.c, leapmuxv1connect.WorkspaceServiceListWorkspacesProcedure, &leapmuxv1.ListWorkspacesRequest{}, &resp)
	})
	g.Go(func() error {
		gs := *s
		gs.ctx = gctx
		var serr error
		sections, serr = gs.loadArchivedSectionIndex()
		return serr
	})
	if err := g.Wait(); err != nil {
		return nil, err
	}
	includeArchived := boolFlag(inv, "archived")
	rows := make([]workspaceRow, 0, len(resp.GetWorkspaces()))
	for _, ws := range resp.GetWorkspaces() {
		isArchived := sections.isArchived(ws.GetId())
		if isArchived && !includeArchived {
			continue
		}
		row := workspaceRowOf(ws)
		row.Archived = isArchived
		rows = append(rows, row)
	}
	return struct {
		Workspaces []workspaceRow `json:"workspaces"`
	}{rows}, nil
}

func runWorkspaceGet(inv *cli.Invocation) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	wsInput := inv.Arg("workspace")
	wsID := resolve.ExplicitID(resolve.KindWorkspace, wsInput)
	if wsID == "" {
		target, err := s.resolveWorkspaceOrDefault(wsInput)
		if err != nil {
			return nil, err
		}
		wsID = target.ID
	}
	var resp leapmuxv1.GetWorkspaceResponse
	if err := hubCallUnary(s.ctx, s.c, leapmuxv1connect.WorkspaceServiceGetWorkspaceProcedure, &leapmuxv1.GetWorkspaceRequest{WorkspaceId: wsID}, &resp); err != nil {
		return nil, err
	}
	return workspaceRowOf(resp.GetWorkspace()), nil
}

// runWorkspaceCreate provisions a new workspace for the authenticated user.
// The hub takes the owner from the session.
func runWorkspaceCreate(inv *cli.Invocation) (any, error) {
	title := strings.TrimSpace(inv.Arg("title"))
	if title == "" {
		return nil, cli.Usagef("the title is empty")
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	var resp leapmuxv1.CreateWorkspaceResponse
	if err := hubCallUnary(s.ctx, s.c, leapmuxv1connect.WorkspaceServiceCreateWorkspaceProcedure, &leapmuxv1.CreateWorkspaceRequest{Title: title}, &resp); err != nil {
		return nil, err
	}
	return workspaceRow{ID: resp.GetWorkspaceId(), Title: title}, nil
}

func runWorkspaceRename(inv *cli.Invocation) (any, error) {
	title := strings.TrimSpace(inv.Arg("title"))
	if title == "" {
		return nil, cli.Usagef("the title is empty")
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	target, err := s.resolveOne(resolve.KindWorkspace, inv.Arg("workspace"))
	if err != nil {
		return nil, err
	}
	var resp leapmuxv1.RenameWorkspaceResponse
	if err := hubCallUnary(s.ctx, s.c, leapmuxv1connect.WorkspaceServiceRenameWorkspaceProcedure, &leapmuxv1.RenameWorkspaceRequest{WorkspaceId: target.ID, Title: title}, &resp); err != nil {
		return nil, err
	}
	return workspaceRow{ID: target.ID, Title: title}, nil
}

type workspaceMoveResult struct {
	ID          string `json:"id"`
	SectionID   string `json:"section_id"`
	SectionName string `json:"section_name,omitempty"`
	Position    string `json:"position,omitempty"`
	Archived    bool   `json:"archived"`
}

func findSectionByType(sections []*leapmuxv1.Section, want leapmuxv1.SectionType) *leapmuxv1.Section {
	for _, sec := range sections {
		if sec.GetSectionType() == want {
			return sec
		}
	}
	return nil
}

func (s *session) resolveWorkspaceAndSections(input string, verifyExists bool) (resolve.Target, *leapmuxv1.ListSectionsResponse, error) {
	var (
		target resolve.Target
		list   *leapmuxv1.ListSectionsResponse
	)
	g, gctx := errgroup.WithContext(s.ctx)
	g.Go(func() error {
		gs := *s
		gs.ctx = gctx
		var err error
		if verifyExists {
			target, err = gs.resolveExistingWorkspace(input)
		} else {
			target, err = gs.resolveOne(resolve.KindWorkspace, input)
		}
		return err
	})
	g.Go(func() error {
		gs := *s
		gs.ctx = gctx
		var err error
		list, err = gs.listSections()
		return err
	})
	if err := g.Wait(); err != nil {
		return resolve.Target{}, nil, err
	}
	return target, list, nil
}

func (s *session) moveWorkspace(wsID string, destSec *leapmuxv1.Section, pos string, curState leapmuxv1.WorkspaceArchiveState) (workspaceMoveResult, error) {
	var resp leapmuxv1.MoveWorkspaceResponse
	if err := hubCallUnary(s.ctx, s.c, leapmuxv1connect.WorkspaceServiceMoveWorkspaceProcedure, &leapmuxv1.MoveWorkspaceRequest{
		WorkspaceId:          wsID,
		SectionId:            destSec.GetId(),
		Position:             pos,
		ExpectedArchiveState: curState,
	}, &resp); err != nil {
		return workspaceMoveResult{}, err
	}
	return workspaceMoveResult{
		ID:          wsID,
		SectionID:   destSec.GetId(),
		SectionName: destSec.GetName(),
		Position:    pos,
		Archived:    destSec.GetSectionType() == leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED,
	}, nil
}

func runWorkspaceArchive(inv *cli.Invocation) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	target, list, err := s.resolveWorkspaceAndSections(inv.Arg("workspace"), false)
	if err != nil {
		return nil, err
	}
	archivedSec := findSectionByType(list.GetSections(), leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED)
	if archivedSec == nil {
		return nil, cli.Newf(cli.CodeNotFound, "no Archived sidebar section exists for this account")
	}
	secIdx := indexSections(list)
	curState := secIdx.archiveState(target.ID)
	if curState == leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED {
		return workspaceMoveResult{ID: target.ID, SectionID: archivedSec.GetId(), SectionName: archivedSec.GetName(), Archived: true}, nil
	}
	return s.moveWorkspace(target.ID, archivedSec, "", curState)
}

func runWorkspaceUnarchive(inv *cli.Invocation) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	target, list, err := s.resolveWorkspaceAndSections(inv.Arg("workspace"), true)
	if err != nil {
		return nil, err
	}
	var destSec *leapmuxv1.Section
	if inv.Changed("section") {
		destSec, err = resolveSection(list.GetSections(), stringFlag(inv, "section"))
		if err != nil {
			return nil, err
		}
		if destSec.GetSectionType() == leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED {
			return nil, cli.Usagef("destination section %q is the Archived section", destSec.GetName())
		}
	} else {
		destSec = findSectionByType(list.GetSections(), leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS)
		if destSec == nil {
			return nil, cli.Newf(cli.CodeNotFound, "no In progress sidebar section exists for this account")
		}
	}
	secIdx := indexSections(list)
	curState := secIdx.archiveState(target.ID)
	if curState == leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ACTIVE && !inv.Changed("section") {
		if curSec := secIdx.sectionOf(target.ID); curSec != nil {
			destSec = curSec
		}
		return workspaceMoveResult{ID: target.ID, SectionID: destSec.GetId(), SectionName: destSec.GetName(), Archived: false}, nil
	}
	return s.moveWorkspace(target.ID, destSec, "", curState)
}

func runWorkspaceMove(inv *cli.Invocation) (any, error) {
	first := boolFlag(inv, "first")
	last := boolFlag(inv, "last")
	beforeRaw := strings.TrimSpace(stringFlag(inv, "before"))
	afterRaw := strings.TrimSpace(stringFlag(inv, "after"))
	if inv.Changed("before") && beforeRaw == "" {
		return nil, cli.Usagef("--before must not be empty")
	}
	if inv.Changed("after") && afterRaw == "" {
		return nil, cli.Usagef("--after must not be empty")
	}
	hasPosition := first || last || beforeRaw != "" || afterRaw != ""
	spec, err := parsePositionSpec(first, last, beforeRaw, afterRaw)
	if err != nil {
		return nil, cli.Usagef("%s", err.Error())
	}

	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()

	var (
		target resolve.Target
		list   *leapmuxv1.ListSectionsResponse
		refWS  resolve.Target
	)
	g, gctx := errgroup.WithContext(s.ctx)
	g.Go(func() error {
		gs := *s
		gs.ctx = gctx
		var rerr error
		target, rerr = gs.resolveOne(resolve.KindWorkspace, inv.Arg("workspace"))
		return rerr
	})
	g.Go(func() error {
		gs := *s
		gs.ctx = gctx
		var lerr error
		list, lerr = gs.listSections()
		return lerr
	})
	if spec.refID != "" {
		refInput := spec.refID
		g.Go(func() error {
			gs := *s
			gs.ctx = gctx
			var rerr error
			refWS, rerr = gs.resolveOne(resolve.KindWorkspace, refInput)
			return rerr
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	destSec, err := resolveSection(list.GetSections(), stringFlag(inv, "section"))
	if err != nil {
		return nil, err
	}
	if spec.refID != "" {
		spec.refID = refWS.ID
	}
	pos := ""
	if hasPosition {
		var peers []rankedItem
		for _, item := range list.GetItems() {
			if item.GetSectionId() == destSec.GetId() && item.GetWorkspaceId() != target.ID {
				peers = append(peers, rankedItem{ID: item.GetWorkspaceId(), Position: item.GetPosition()})
			}
		}
		pos, err = computeRankedPosition(peers, target.ID, spec, "workspace")
		if err != nil {
			return nil, err
		}
	}
	secIdx := indexSections(list)
	return s.moveWorkspace(target.ID, destSec, pos, secIdx.archiveState(target.ID))
}

// workspaceDeleteResult is the result of `workspace delete`.
type workspaceDeleteResult struct {
	ID string `json:"id"`
	// Status is "ok" when every Worker cleaned up, and "partial" when at
	// least one failed.
	Status    string          `json:"status"`
	WorkerIDs []string        `json:"worker_ids"`
	Cleanup   []workerCleanup `json:"cleanup"`
}

// workerCleanup is the outcome of one Worker's cleanup.
type workerCleanup struct {
	WorkerID string `json:"worker_id" table:"WORKER,short"`
	Status   string `json:"status" table:"STATUS,state"`
	Error    string `json:"error,omitempty" table:"ERROR"`
}

// runWorkspaceDelete drops the workspace row and fans out CleanupWorkspace
// to every worker that hosted a tab.
//
// The hub-side delete cascades into a worker-side CleanupWorkspace fan-out
// that kills every PTY in the workspace — so running this from a terminal
// tab inside the target workspace will sever the caller's own shell
// mid-response. `guardWorkspaceDelete` rejects that case unless
// --allow-self is supplied.
func runWorkspaceDelete(inv *cli.Invocation) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	target, err := s.resolveOne(resolve.KindWorkspace, inv.Arg("workspace"))
	if err != nil {
		return nil, err
	}
	if err := guardWorkspaceDelete(s.ctx, s.c, target.ID, boolFlag(inv, allowSelfFlag)); err != nil {
		return nil, err
	}
	if !boolFlag(inv, "allow-busy") {
		cc, err := s.openCRDT(target.ID)
		if err != nil {
			return nil, err
		}
		wsTabs := liveTabsOfWorkspace(cc.bs.State, target.ID)
		if err := guardBulkCloseTabs(cc, wsTabs, false); err != nil {
			return nil, err
		}
	}
	var resp leapmuxv1.DeleteWorkspaceResponse
	if err := hubCallUnary(s.ctx, s.c, leapmuxv1connect.WorkspaceServiceDeleteWorkspaceProcedure, &leapmuxv1.DeleteWorkspaceRequest{WorkspaceId: target.ID}, &resp); err != nil {
		return nil, err
	}

	// Mirror the frontend's two-step delete: the hub drops the workspace
	// row and returns each hosting worker WITH the tabs it must tear down
	// (read inside the delete transaction), then we fan out
	// CleanupWorkspace over E2EE. The fan-out logic is in
	// runWorkspaceCleanupFanout so it can be exercised by unit tests
	// without standing up a real E2EE worker harness.
	//
	// No pre-delete ListTabs read any more: this command used to resolve
	// its own list beforehand and swallow that read's error to nil, which
	// silently degraded the whole fan-out to "close nothing".
	status, entries := runWorkspaceCleanupFanout(s.ctx, resp.GetWorkerTabs(), cliCleanupCaller(s.c))

	workerIDs := make([]string, 0, len(resp.GetWorkerTabs()))
	for _, wt := range resp.GetWorkerTabs() {
		workerIDs = append(workerIDs, wt.GetWorkerId())
	}
	return workspaceDeleteResult{ID: target.ID, Status: status, WorkerIDs: workerIDs, Cleanup: entries}, nil
}

// cleanupCaller is the seam workspace-delete uses to fan out a
// `CleanupWorkspace` inner-RPC per worker. The CLI binds it to the
// real Client+E2EE channel; unit tests pass a fake that exercises the
// surrounding aggregation logic without spinning up worker plumbing.
type cleanupCaller func(ctx context.Context, workerID string, tabs []*leapmuxv1.TabIdent) (*leapmuxv1.CleanupWorkspaceResponse, error)

// cliCleanupCaller wires runWorkspaceCleanupFanout to the production
// transport: every worker_id gets its own E2EE channel and a
// CleanupWorkspace inner-RPC.
func cliCleanupCaller(c control.Peer) cleanupCaller {
	return func(ctx context.Context, workerID string, tabs []*leapmuxv1.TabIdent) (*leapmuxv1.CleanupWorkspaceResponse, error) {
		req := &leapmuxv1.CleanupWorkspaceRequest{Tabs: tabs}
		var resp leapmuxv1.CleanupWorkspaceResponse
		if err := callInnerRPC(ctx, c, workerID, leapmuxv1connect.WorkerTabServiceCleanupWorkspaceProcedure, req, &resp); err != nil {
			return nil, err
		}
		return &resp, nil
	}
}

// liveTabsOfWorkspace collects every live tab placed in wsID (both its main
// tile tree and any live floating windows anchored in wsID), sorted by TabID.
func liveTabsOfWorkspace(state *leapmuxv1.UserMaterialized, wsID string) []crdt.TabRef {
	var out []crdt.TabRef
	for _, t := range state.GetTabs() {
		if t == nil || !crdt.HLCIsZero(t.GetTombstoneAt()) {
			continue
		}
		tileID := t.GetTileId().GetValue()
		if tileID == "" {
			continue
		}
		if nodeWorkspaceFromState(state, tileID) == wsID {
			out = append(out, crdt.TabRef{TabID: t.GetTabId(), TabType: t.GetTabType()})
		}
	}
	slices.SortFunc(out, func(a, b crdt.TabRef) int {
		return strings.Compare(a.TabID, b.TabID)
	})
	return out
}

// runWorkspaceCleanupFanout invokes call per worker in parallel and
// assembles the per-worker status entries the CLI emits, preserving the
// input workerIDs order. Returns ("ok", …) when every call succeeds,
// ("partial", …) when at least one fails, and ("ok", []) when there
// are no workers (the workspace had no tabs — the hub-side delete is
// the only step).
//
// workerTabs pairs each worker with the tabs it hosts, as the hub read them
// inside the delete transaction -- so a worker in this list always arrives with
// its own tab list rather than depending on a separate, racier read.
//
// Failures DO NOT short-circuit: the user needs per-worker visibility
// so they can decide whether to retry only the failures or rerun the
// whole delete. errgroup.Group (no context cancellation) is used so
// one worker's failure doesn't cancel the others' in-flight calls.
func runWorkspaceCleanupFanout(ctx context.Context, workerTabs []*leapmuxv1.WorkerTabs, call cleanupCaller) (string, []workerCleanup) {
	entries := make([]workerCleanup, len(workerTabs))
	var failed atomic.Bool
	var g errgroup.Group
	g.SetLimit(8)
	for i, wt := range workerTabs {
		g.Go(func() error {
			entry := workerCleanup{WorkerID: wt.GetWorkerId(), Status: "ok"}
			if _, err := call(ctx, wt.GetWorkerId(), wt.GetTabs()); err != nil {
				entry.Status = "failed"
				entry.Error = err.Error()
				failed.Store(true)
			}
			entries[i] = entry
			return nil
		})
	}
	_ = g.Wait()
	status := "ok"
	if failed.Load() {
		status = "partial"
	}
	return status, entries
}
