package cmd

import (
	"context"
	"fmt"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/remote"
	"github.com/leapmux/leapmux/internal/cli/remote/resolve"
)

// RunTabClose tombstones a tab via OrgCRDT.SubmitOps and dispatches
// worker-side teardown (CloseAgent / CloseTerminal). The hub's
// remove-wins semantics mean the tab id is dead afterward; recreating
// at the UI level mints a fresh id. The resolver derives workspace +
// tab-type from --tab-id when only the id is given.
//
// --worktree governs what happens to a tab's git worktree when this
// is the last tab on that worktree (mirrors the frontend's last-tab
// close dialog):
//
//   - keep    — close the tab, leave the worktree directory intact.
//   - push    — git push (creating a WIP commit if dirty) first, then
//     close. Worktree directory is kept.
//   - discard — close the tab AND remove the worktree directory.
//
// When `tab close` would be a last-tab close for a worktree, OR the
// last tab on a non-worktree branch that still has uncommitted /
// unpushed changes, --worktree is REQUIRED — omitting it fails with
// invalid_request. This mirrors the frontend's forced-choice dialog:
// the worker doesn't pick a default, so neither does the CLI.
//
// Closing the calling tab itself kills its PTY mid-response, so
// `guardTabClose` rejects that case unless --force is supplied.
func RunTabClose(rawCtx any, args []string) error {
	cmd := asCtx(rawCtx)
	var hub, worktree string
	var force bool
	var in resolve.Inputs
	fs := flagSet(cmd, &hub)
	resolve.BindEntityFlags(fs, &in, resolve.FlagOptions{HideOrg: true, HideUser: true})
	fs.BoolVar(&force, "force", false, "close even if the target is the calling tab (would kill the caller's own PTY)")
	fs.StringVar(&worktree, "worktree", "", `worktree disposition: "keep" / "push" / "discard". Required when this is the last tab for a worktree, or the last tab on a non-worktree branch with uncommitted / unpushed changes.`)
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}
	wt, err := parseTabCloseWorktree(worktree)
	if err != nil {
		return remote.EmitError("invalid_request", err.Error())
	}
	return resolveAndEmit(hub, resolve.Need{TabID: true, WorkspaceID: true, WorkerID: true}, in, func(ctx context.Context, c *remote.Client, got resolve.Resolved) error {
		if err := guardTabClose(got.TabID, force); err != nil {
			return err
		}
		tt := got.TabType
		if tt == leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED {
			return remote.EmitError("invalid_request", "could not determine tab type for "+got.TabID+"; pass --tab-type explicitly")
		}
		cc, err := openCRDTCall(hub, got.WorkspaceID)
		if err != nil {
			return err
		}
		defer cc.close()
		if err := preflightTab(cc.bs.State, got.WorkspaceID, got.TabID, tt); err != nil {
			return err
		}

		// Last-tab worktree gate. Only agent / terminal tabs have worker-
		// side worktree state; file tabs skip both the inspect and the
		// worker-close dispatch (they're path registrations, no PTY).
		worktreeAction := wt.worktreeAction()
		if tt != leapmuxv1.TabType_TAB_TYPE_FILE {
			inspected, ierr := inspectLastTabCloseBest(ctx, c, got.WorkerID, tt, got.TabID)
			if ierr != nil {
				// Worker unreachable / not found is the same fallback the
				// frontend uses: skip the dialog, proceed with implicit
				// KEEP. The CRDT tombstone still runs and the worker's
				// reconciler eventually catches up.
				if !isWorkerUnreachable(ierr) {
					return remote.EmitErrorWith("inspect_failed", ierr)
				}
			} else if inspected.GetShouldPrompt() {
				if wt == closeWorktreeUnspecified {
					return remote.EmitError("invalid_request", lastTabPromptMessage(inspected))
				}
				if wt == closeWorktreePush {
					if !inspected.GetGitState().GetCanPush() {
						return remote.EmitError("invalid_request", "cannot push: "+pushBlockedReason(inspected))
					}
					if err := callInnerRPC(ctx, c, got.WorkerID, "PushBranch", &leapmuxv1.PushBranchRequest{TabType: tt, TabId: got.TabID}, &leapmuxv1.PushBranchResponse{}); err != nil {
						return err
					}
				}
			}
		}

		if err := cc.submitOps([]*leapmuxv1.OrgOp{opTombstoneTab(cc.bs, tt, got.TabID)}); err != nil {
			return err
		}

		// Worker-side teardown. Mirrors useTabOperations.handleTabClose:
		// the CRDT tombstone is the authoritative removal, and these
		// inner-RPCs are fire-and-forget cleanup (PTY teardown, optional
		// worktree removal). callInnerRPC failures are demoted to envelope
		// fields so the user sees both the close success and the cleanup
		// outcome.
		closeErr := dispatchWorkerClose(ctx, c, got, tt, worktreeAction)

		out := map[string]any{
			"tab_id":     got.TabID,
			"tab_type":   tabTypeName(got.TabType),
			"tombstoned": true,
		}
		if wt != closeWorktreeUnspecified {
			out["worktree"] = string(wt)
		}
		if closeErr != nil {
			out["worker_close_error"] = closeErr.Error()
		}
		return remote.EmitData(out)
	})
}

// tabCloseWorktree is the parsed --worktree flag. Unspecified is
// distinct from KEEP — at the last-tab decision point the CLI rejects
// unspecified so the user makes an explicit choice.
type tabCloseWorktree string

const (
	closeWorktreeUnspecified tabCloseWorktree = ""
	closeWorktreeKeep        tabCloseWorktree = "keep"
	closeWorktreePush        tabCloseWorktree = "push"
	closeWorktreeDiscard     tabCloseWorktree = "discard"
)

var tabCloseWorktreeMap = map[string]tabCloseWorktree{
	"keep":    closeWorktreeKeep,
	"push":    closeWorktreePush,
	"discard": closeWorktreeDiscard,
	"remove":  closeWorktreeDiscard, // "remove" is an accepted synonym
}

func parseTabCloseWorktree(s string) (tabCloseWorktree, error) {
	if s == "" {
		return closeWorktreeUnspecified, nil
	}
	v, ok := parseEnumFlag(s, tabCloseWorktreeMap)
	if !ok {
		return closeWorktreeUnspecified, fmt.Errorf(`--worktree must be one of "keep", "push", "discard"; got %q`, s)
	}
	return v, nil
}

func (c tabCloseWorktree) worktreeAction() leapmuxv1.WorktreeAction {
	switch c {
	case closeWorktreeDiscard:
		return leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE
	case closeWorktreeKeep, closeWorktreePush:
		return leapmuxv1.WorktreeAction_WORKTREE_ACTION_KEEP
	}
	return leapmuxv1.WorktreeAction_WORKTREE_ACTION_UNSPECIFIED
}

func inspectLastTabCloseBest(ctx context.Context, c *remote.Client, workerID string, tabType leapmuxv1.TabType, tabID string) (*leapmuxv1.InspectLastTabCloseResponse, error) {
	resp := &leapmuxv1.InspectLastTabCloseResponse{}
	if err := callInnerRPCBest(ctx, c, workerID, "InspectLastTabClose", &leapmuxv1.InspectLastTabCloseRequest{TabType: tabType, TabId: tabID}, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// lastTabPromptMessage builds the "must pass --worktree" error so the
// user sees exactly what state the close would lose. Mirrors the
// frontend dialog body: worktree path / branch name / diff stats /
// unpushed commit count, in plain text.
func lastTabPromptMessage(r *leapmuxv1.InspectLastTabCloseResponse) string {
	var b strings.Builder
	switch r.GetTarget() {
	case leapmuxv1.LastTabCloseTarget_LAST_TAB_CLOSE_TARGET_WORKTREE:
		b.WriteString("last tab for worktree ")
		b.WriteString(r.GetWorktreePath())
	case leapmuxv1.LastTabCloseTarget_LAST_TAB_CLOSE_TARGET_BRANCH:
		b.WriteString("last tab for branch ")
		b.WriteString(r.GetBranchName())
	default:
		b.WriteString("last tab on tracked branch")
	}
	gs := r.GetGitState()
	var details []string
	if gs.GetHasUncommittedChanges() {
		details = append(details, fmt.Sprintf("%d added / %d deleted / %d untracked", gs.GetDiffAdded(), gs.GetDiffDeleted(), gs.GetDiffUntracked()))
	}
	if n := gs.GetUnpushedCommitCount(); n > 0 {
		noun := "commit"
		if n != 1 {
			noun = "commits"
		}
		details = append(details, fmt.Sprintf("%d unpushed %s", n, noun))
	}
	if gs.GetRemoteBranchMissing() {
		details = append(details, "branch not pushed to remote")
	}
	if len(details) > 0 {
		b.WriteString(" (")
		b.WriteString(strings.Join(details, ", "))
		b.WriteString(")")
	}
	b.WriteString(`; pass --worktree=keep|push|discard`)
	return b.String()
}

func pushBlockedReason(r *leapmuxv1.InspectLastTabCloseResponse) string {
	if !r.GetGitState().GetOriginExists() {
		return "remote origin does not exist"
	}
	if r.GetTarget() != leapmuxv1.LastTabCloseTarget_LAST_TAB_CLOSE_TARGET_WORKTREE && r.GetTarget() != leapmuxv1.LastTabCloseTarget_LAST_TAB_CLOSE_TARGET_BRANCH {
		return "no pushable branch"
	}
	return "branch is not pushable"
}

func dispatchWorkerClose(ctx context.Context, c *remote.Client, got resolve.Resolved, tt leapmuxv1.TabType, action leapmuxv1.WorktreeAction) error {
	if got.WorkerID == "" {
		return nil
	}
	switch tt {
	case leapmuxv1.TabType_TAB_TYPE_AGENT:
		return callInnerRPCBest(ctx, c, got.WorkerID, "CloseAgent",
			&leapmuxv1.CloseAgentRequest{AgentId: got.TabID, WorktreeAction: action},
			&leapmuxv1.CloseAgentResponse{})
	case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
		return callInnerRPCBest(ctx, c, got.WorkerID, "CloseTerminal",
			&leapmuxv1.CloseTerminalRequest{
				OrgId:          got.OrgID,
				WorkspaceId:    got.WorkspaceID,
				TerminalId:     got.TabID,
				WorktreeAction: action,
			},
			&leapmuxv1.CloseTerminalResponse{})
	}
	return nil
}
