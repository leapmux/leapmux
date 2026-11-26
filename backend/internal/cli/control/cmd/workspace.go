package cmd

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/proto"

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
	var resp leapmuxv1.ListWorkspacesResponse
	if err := hubCallUnary(s.ctx, s.c, leapmuxv1connect.WorkspaceServiceListWorkspacesProcedure, &leapmuxv1.ListWorkspacesRequest{}, &resp); err != nil {
		return nil, err
	}
	archived := archivedWorkspaceIDs(s.ctx, s.c)
	includeArchived := boolFlag(inv, "archived")
	rows := make([]workspaceRow, 0, len(resp.GetWorkspaces()))
	for _, ws := range resp.GetWorkspaces() {
		isArchived := archived[ws.GetId()]
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
	if wsInput == "" && callingTabID() == "" {
		if def, derr := s.defaultWorkspaceID(); derr != nil {
			return nil, derr
		} else if def != "" {
			wsInput = def
		}
	}
	target, err := s.resolveOne(resolve.KindWorkspace, wsInput)
	if err != nil {
		return nil, err
	}
	var resp leapmuxv1.GetWorkspaceResponse
	if err := hubCallUnary(s.ctx, s.c, leapmuxv1connect.WorkspaceServiceGetWorkspaceProcedure, &leapmuxv1.GetWorkspaceRequest{WorkspaceId: target.ID}, &resp); err != nil {
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

func currentWorkspaceArchiveState(list *leapmuxv1.ListSectionsResponse, wsID string) leapmuxv1.WorkspaceArchiveState {
	archivedSections := map[string]bool{}
	for _, sec := range list.GetSections() {
		if sec.GetSectionType() == leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED {
			archivedSections[sec.GetId()] = true
		}
	}
	for _, item := range list.GetItems() {
		if item.GetWorkspaceId() == wsID && archivedSections[item.GetSectionId()] {
			return leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED
		}
	}
	return leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ACTIVE
}

func findSectionByType(sections []*leapmuxv1.Section, want leapmuxv1.SectionType) *leapmuxv1.Section {
	for _, sec := range sections {
		if sec.GetSectionType() == want {
			return sec
		}
	}
	return nil
}

func runWorkspaceArchive(inv *cli.Invocation) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	target, err := s.resolveOne(resolve.KindWorkspace, inv.Arg("workspace"))
	if err != nil {
		return nil, err
	}
	list, err := s.listSections()
	if err != nil {
		return nil, err
	}
	archivedSec := findSectionByType(list.GetSections(), leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED)
	if archivedSec == nil {
		return nil, cli.Newf(cli.CodeNotFound, "no Archived sidebar section exists for this account")
	}
	curState := currentWorkspaceArchiveState(list, target.ID)
	if curState == leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED {
		return workspaceMoveResult{ID: target.ID, SectionID: archivedSec.GetId(), SectionName: archivedSec.GetName(), Archived: true}, nil
	}
	var resp leapmuxv1.MoveWorkspaceResponse
	if err := hubCallUnary(s.ctx, s.c, leapmuxv1connect.WorkspaceServiceMoveWorkspaceProcedure, &leapmuxv1.MoveWorkspaceRequest{
		WorkspaceId:          target.ID,
		SectionId:            archivedSec.GetId(),
		ExpectedArchiveState: curState,
	}, &resp); err != nil {
		return nil, err
	}
	return workspaceMoveResult{ID: target.ID, SectionID: archivedSec.GetId(), SectionName: archivedSec.GetName(), Archived: true}, nil
}

func runWorkspaceUnarchive(inv *cli.Invocation) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	target, err := s.resolveOne(resolve.KindWorkspace, inv.Arg("workspace"))
	if err != nil {
		return nil, err
	}
	list, err := s.listSections()
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
	curState := currentWorkspaceArchiveState(list, target.ID)
	if curState == leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ACTIVE && !inv.Changed("section") {
		curSec := currentWorkspaceSection(list, target.ID)
		if curSec != nil {
			destSec = curSec
		}
		return workspaceMoveResult{ID: target.ID, SectionID: destSec.GetId(), SectionName: destSec.GetName(), Archived: false}, nil
	}
	var resp leapmuxv1.MoveWorkspaceResponse
	if err := hubCallUnary(s.ctx, s.c, leapmuxv1connect.WorkspaceServiceMoveWorkspaceProcedure, &leapmuxv1.MoveWorkspaceRequest{
		WorkspaceId:          target.ID,
		SectionId:            destSec.GetId(),
		ExpectedArchiveState: curState,
	}, &resp); err != nil {
		return nil, err
	}
	return workspaceMoveResult{ID: target.ID, SectionID: destSec.GetId(), SectionName: destSec.GetName(), Archived: false}, nil
}

func runWorkspaceMove(inv *cli.Invocation) (any, error) {
	first := boolFlag(inv, "first")
	last := boolFlag(inv, "last")
	beforeRaw := strings.TrimSpace(stringFlag(inv, "before"))
	afterRaw := strings.TrimSpace(stringFlag(inv, "after"))
	placeCount := 0
	for _, set := range []bool{first, last, inv.Changed("before"), inv.Changed("after")} {
		if set {
			placeCount++
		}
	}
	if placeCount > 1 {
		return nil, cli.Usagef("--first, --last, --before, and --after exclude each other")
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
	list, err := s.listSections()
	if err != nil {
		return nil, err
	}
	destSec, err := resolveSection(list.GetSections(), stringFlag(inv, "section"))
	if err != nil {
		return nil, err
	}
	var refID string
	if inv.Changed("before") {
		ref, err := s.resolveOne(resolve.KindWorkspace, beforeRaw)
		if err != nil {
			return nil, err
		}
		refID = ref.ID
	} else if inv.Changed("after") {
		ref, err := s.resolveOne(resolve.KindWorkspace, afterRaw)
		if err != nil {
			return nil, err
		}
		refID = ref.ID
	}
	var peers []rankedItem
	for _, item := range list.GetItems() {
		if item.GetSectionId() == destSec.GetId() && item.GetWorkspaceId() != target.ID {
			peers = append(peers, rankedItem{ID: item.GetWorkspaceId(), Position: item.GetPosition()})
		}
	}
	pos := ""
	if placeCount > 0 {
		pos, err = computeLexoRankPosition(peers, first, last, inv.Changed("before"), inv.Changed("after"), refID, "")
		if err != nil {
			return nil, err
		}
	}
	curState := currentWorkspaceArchiveState(list, target.ID)
	var resp leapmuxv1.MoveWorkspaceResponse
	if err := hubCallUnary(s.ctx, s.c, leapmuxv1connect.WorkspaceServiceMoveWorkspaceProcedure, &leapmuxv1.MoveWorkspaceRequest{
		WorkspaceId:          target.ID,
		SectionId:            destSec.GetId(),
		Position:             pos,
		ExpectedArchiveState: curState,
	}, &resp); err != nil {
		return nil, err
	}
	return workspaceMoveResult{
		ID:          target.ID,
		SectionID:   destSec.GetId(),
		SectionName: destSec.GetName(),
		Position:    pos,
		Archived:    destSec.GetSectionType() == leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED,
	}, nil
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
		if err := guardTabsBusy(cc, wsTabs, false); err != nil {
			return nil, err
		}
		if err := guardWorkspaceDirtyWorktrees(cc, wsTabs); err != nil {
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
		if err := callInnerRPCBest(ctx, c, workerID, leapmuxv1connect.WorkerTabServiceCleanupWorkspaceProcedure, req, &resp); err != nil {
			return nil, err
		}
		return &resp, nil
	}
}

// guardWorkspaceDirtyWorktrees inspects each live tab's git checkout via
// InspectLastTabClose (and GetGitFileStatus when sibling tabs in the same
// workspace trigger InspectLastTabClose's sibling fast path) on one E2EE
// channel per Worker and refuses workspace deletion when any worktree or branch
// holds uncommitted changes, unpushed commits, or an unpushed local branch,
// unless the caller passed --allow-busy.
func guardWorkspaceDirtyWorktrees(cc *crdtCall, tabs []crdt.TabRef) error {
	if len(tabs) == 0 {
		return nil
	}
	var order []string
	byWorker := map[string][]crdt.TabRef{}
	for _, t := range tabs {
		workerID := cc.bs.State.GetTabs()[t.TabID].GetWorkerId().GetValue()
		if workerID == "" {
			continue
		}
		if _, seen := byWorker[workerID]; !seen {
			order = append(order, workerID)
		}
		byWorker[workerID] = append(byWorker[workerID], t)
	}
	var dirty []tabBusyReason
	seenPath := map[string]bool{}
	for _, workerID := range order {
		refs := byWorker[workerID]
		chanErr := withBestEffortWorkerChannel(cc.ctx, cc.c, workerID, func(w workerCall) error {
			callWorker := func(method string, in, out proto.Message) error {
				return w.Call(cc.ctx, method, in, out)
			}
			for _, ref := range refs {
				inspected, err := inspectLastTabCloseBest(callWorker, ref.TabType, ref.TabID)
				if err != nil {
					if cli.FromError(err).Code == cli.CodeUnavailable || cli.FromError(err).Code == cli.CodeWorkerOffline {
						dirty = append(dirty, tabBusyReason{
							tabID:  ref.TabID,
							detail: "worker unreachable; cannot verify uncommitted git changes",
						})
					}
					continue
				}
				if inspected == nil {
					continue
				}
				pathKey := inspected.GetWorktreePath()
				if pathKey == "" {
					pathKey = inspected.GetWorkingDir()
				}
				if pathKey != "" && seenPath[pathKey] {
					continue
				}
				if inspected.GetShouldPrompt() {
					gs := inspected.GetGitState()
					var parts []string
					if gs.GetHasUncommittedChanges() {
						parts = append(parts, "uncommitted changes")
					}
					if n := gs.GetUnpushedCommitCount(); n > 0 {
						parts = append(parts, pluralize(int(n), "unpushed commit"))
					}
					if gs.GetRemoteBranchMissing() && gs.GetUnpushedCommitCount() == 0 {
						parts = append(parts, "unpushed local branch")
					}
					if len(parts) == 0 {
						continue
					}
					if pathKey != "" {
						seenPath[pathKey] = true
					}
					dirty = append(dirty, tabBusyReason{
						tabID:  ref.TabID,
						detail: formatDirtyCheckoutDetail(inspected.GetBranchName(), pathKey, parts),
					})
					continue
				}
				// Sibling fast path: when 2+ live tabs share a worktree or branch
				// inside the workspace being deleted, InspectLastTabClose returns
				// ShouldPrompt=false and GitState=nil while still populating
				// WorkingDir/WorktreePath. Inspect that checkout directly so
				// deleting the whole workspace cannot silently drop dirty state.
				if inspected.GetGitState() == nil && pathKey != "" {
					seenPath[pathKey] = true
					var statusResp leapmuxv1.GetGitFileStatusResponse
					if err := callWorker(leapmuxv1connect.WorkerGitServiceGetGitFileStatusProcedure, &leapmuxv1.GetGitFileStatusRequest{Path: pathKey}, &statusResp); err != nil {
						continue
					}
					var parts []string
					if len(statusResp.GetFiles()) > 0 || statusResp.GetStatus().GetConflicted() {
						parts = append(parts, "uncommitted changes")
					}
					if ahead := statusResp.GetStatus().GetAhead(); ahead > 0 {
						parts = append(parts, pluralize(int(ahead), "unpushed commit"))
					}
					if len(parts) > 0 {
						dirty = append(dirty, tabBusyReason{
							tabID:  ref.TabID,
							detail: formatDirtyCheckoutDetail(inspected.GetBranchName(), pathKey, parts),
						})
					}
				}
			}
			return nil
		})
		if chanErr != nil {
			dirty = append(dirty, tabBusyReason{
				tabID:  refs[0].TabID,
				detail: fmt.Sprintf("worker %s unreachable; cannot verify uncommitted git changes", cli.ShortID(workerID)),
			})
		}
	}
	if len(dirty) == 0 {
		return nil
	}
	return errTabBusyRefused(dirty)
}

func formatDirtyCheckoutDetail(branch, pathKey string, parts []string) string {
	label := "git checkout"
	if branch != "" {
		label = "branch " + branch
	}
	if pathKey != "" {
		label += " at " + pathKey
	}
	return label + " has " + strings.Join(parts, " and ")
}

func currentWorkspaceSection(list *leapmuxv1.ListSectionsResponse, wsID string) *leapmuxv1.Section {
	secByID := map[string]*leapmuxv1.Section{}
	for _, sec := range list.GetSections() {
		secByID[sec.GetId()] = sec
	}
	for _, item := range list.GetItems() {
		if item.GetWorkspaceId() == wsID {
			if sec := secByID[item.GetSectionId()]; sec != nil {
				return sec
			}
		}
	}
	return nil
}

// liveTabsOfWorkspace collects every live tab placed in wsID (both its main
// tile tree and any live floating windows anchored in wsID).
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
