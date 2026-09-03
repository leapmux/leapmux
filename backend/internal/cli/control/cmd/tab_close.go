package cmd

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

// RunTabClose tombstones a tab via UserCRDT.SubmitOps and dispatches
// worker-side teardown (CloseAgent / CloseTerminal / RevokeTabPayload).
// The hub's remove-wins semantics mean the tab id is dead afterward;
// recreating at the UI level mints a fresh id. The resolver derives
// workspace + tab-type from --tab-id when only the id is given.
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
// the worker doesn't pick a default, so neither does the CLI. It
// applies to file tabs too: a file tab holds a worktree open and sits
// on a branch exactly like the other two types do.
//
// `discard` is refused up front when the inspect reports that git would
// refuse the removal (a locked worktree, a path git will not remove),
// exactly as the frontend disables the same choice — see the
// worktree_removal_blocked_reason handling below.
//
// Closing the calling tab itself kills its PTY mid-response, so
// `guardTabClose` rejects that case unless --force is supplied.
func RunTabClose(rawCtx any, args []string) error {
	cmd := asCtx(rawCtx)
	var hub, worktree string
	var force bool
	var in resolve.Inputs
	fs := flagSet(cmd, &hub)
	resolve.BindEntityFlags(fs, &in, resolve.FlagOptions{})
	fs.BoolVar(&force, "force", false, "close even if the target is the calling tab (would kill the caller's own PTY)")
	fs.StringVar(&worktree, "worktree", "", `worktree disposition: "keep" / "push" / "discard". Required when this is the last tab for a worktree, or the last tab on a non-worktree branch with uncommitted / unpushed changes.`)
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}
	wt, err := parseTabCloseWorktree(worktree)
	if err != nil {
		return control.EmitError("invalid_request", err.Error())
	}
	return resolveAndEmit(hub, resolve.Need{TabID: true, WorkspaceID: true, WorkerID: true}, in, func(ctx context.Context, c *control.Client, got resolve.Resolved) error {
		if err := guardTabClose(got.TabID, force); err != nil {
			return err
		}
		tt := got.TabType
		if tt == leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED {
			return control.EmitError("invalid_request", "could not determine tab type for "+got.TabID+"; pass --tab-type explicitly")
		}
		cc, err := openCRDTCall(hub, got.WorkspaceID)
		if err != nil {
			return err
		}
		defer cc.close()
		if err := preflightTab(cc.bs.State, got.WorkspaceID, got.TabID, tt); err != nil {
			return err
		}

		// `tab close` issues up to three inner-RPCs against the SAME worker --
		// InspectLastTabClose, an optional PushBranch, and the
		// CloseAgent/CloseTerminal/RevokeTabPayload teardown -- with the CRDT
		// tombstone in between. Hoisting one Noise_NK channel over the sequence
		// pays the handshake once rather than per call.
		//
		// Opening is best-effort on purpose: a failure must not abort the
		// close, because a tab whose worker is gone still has to be
		// tombstoneable. withBestEffortWorkerChannel falls back to opening per
		// call, which reproduces the exact channel_open_failed code this
		// command's unreachable-worker fallback keys on.
		var w workerCall
		if err := withBestEffortWorkerChannel(ctx, c, got.WorkerID, func(bound workerCall) error {
			w = bound
			return nil
		}); err != nil {
			return err
		}
		callWorker := func(method string, in, out proto.Message) error {
			return w.Call(ctx, method, in, out)
		}

		// Last-tab worktree gate, for every tab type. A file tab holds a
		// worktree open exactly like an agent or terminal does (the worker
		// ref-counts worktree_tabs type-agnostically) and answers branch
		// questions from its own working dir, so skipping the inspect for it
		// meant the CLI closed the last tab on a worktree without the
		// forced --worktree choice the UI puts in front of the same close.
		worktreeAction := wt.worktreeAction()
		var inspectHint string
		inspected, ierr := inspectLastTabCloseBest(callWorker, tt, got.TabID)
		if ierr != nil {
			// Worker unreachable / not found is the same fallback the
			// frontend uses: skip the dialog, proceed with implicit
			// KEEP. The CRDT tombstone still runs and the worker's
			// reconciler eventually catches up.
			if !isWorkerUnreachable(ierr) {
				return control.EmitErrorWith("inspect_failed", ierr)
			}
		} else if inspected.GetShouldPrompt() {
			if wt == closeWorktreeUnspecified {
				return control.EmitError("invalid_request", lastTabPromptMessage(inspected))
			}
			// Refuse a discard git will not honour, BEFORE the tombstone. The
			// removal runs after the tab is gone -- the worker stops the
			// subprocess and deletes the whole directory, which takes seconds --
			// so a refusal that arrives afterwards reaches a command that
			// already reported success and a tab that is already destroyed.
			// The frontend's last-tab dialog disables its own Delete button from
			// this same field, so both surfaces refuse the same removals for the
			// same stated reason. An empty reason is not a promise the removal
			// succeeds; whatever the preflight cannot state still comes back in
			// worker_close_error below.
			if msg := discardRefusalMessage(wt, inspected); msg != "" {
				return control.EmitError("invalid_request", msg)
			}
			if wt == closeWorktreePush {
				if !inspected.GetGitState().GetCanPush() {
					return control.EmitError("invalid_request", "cannot push: "+pushBlockedReason(inspected))
				}
				if err := callWorker("PushBranch", &leapmuxv1.PushBranchRequest{WorkingDir: inspected.GetWorkingDir()}, &leapmuxv1.PushBranchResponse{}); err != nil {
					return emitInnerRPCError(err)
				}
			}
		}
		// Carry the degraded-close hint into the output so a CLI user
		// sees the close proceeded without the git-state check (mirrors
		// the frontend's warn toast on the same field). Safe on the
		// unreachable-worker path above: `inspected` is nil there, and a
		// generated getter on a nil message answers the zero value.
		inspectHint = inspected.GetErrorHint()

		if err := cc.submitOps(closeBatchOps(cc.bs, tt, got.TabID, inspected.GetDescendantAgentIds())); err != nil {
			return err
		}

		// Worker-side teardown. Mirrors useTabOperations.handleTabClose:
		// the CRDT tombstone is the authoritative removal, and these
		// inner-RPCs are fire-and-forget cleanup (PTY teardown, optional
		// worktree removal). callInnerRPC failures are demoted to envelope
		// fields so the user sees both the close success and the cleanup
		// outcome.
		subagents, closeErr := dispatchWorkerClose(callWorker, got, tt, worktreeAction)

		// Anything the inspect did not already cover. The worker reads the tree
		// again AFTER its teardown, so this catches a subagent the provider
		// spawned while it drained -- which the inspect, taken before any of
		// that, could not have seen.
		//
		// The ids the first batch carried are excluded: they are tombstoned
		// already, and the hub rejects a tombstone for a record whose tile id a
		// tombstone stripped -- which would fail this whole batch.
		//
		// Best-effort: the parent is already tombstoned and its worker-side
		// teardown already ran, so a rejected batch here must not turn a
		// completed close into a command failure. It is reported instead.
		var subagentErr error
		if ops := subagentTombstoneOps(cc.bs, exceptAlreadySubmitted(subagents, inspected.GetDescendantAgentIds())); len(ops) > 0 {
			// trySubmitOps, not submitOps: the emitting variant would print an
			// error envelope over the success this command already earned.
			// It reports a rejection as an error too, so one branch covers both.
			if _, _, err := cc.trySubmitOps(ops); err != nil {
				subagentErr = err
			}
		}

		out := map[string]any{
			"tab_id":     got.TabID,
			"tab_type":   tabTypeName(got.TabType),
			"tombstoned": true,
		}
		if wt != closeWorktreeUnspecified {
			out["worktree"] = string(wt)
		}
		if inspectHint != "" {
			out["inspect_hint"] = inspectHint
		}
		if closeErr != nil {
			out["worker_close_error"] = closeErr.Error()
		}
		if len(subagents) > 0 {
			out["closed_subagent_tab_ids"] = subagents
		}
		if subagentErr != nil {
			out["subagent_close_error"] = subagentErr.Error()
		}
		return control.EmitData(out)
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
	default:
		return leapmuxv1.WorktreeAction_WORKTREE_ACTION_UNSPECIFIED
	}
}

// workerCaller issues one inner-RPC against a worker already fixed by the
// caller -- either on a hoisted E2EE channel or through the per-call
// transport. Taking it as a parameter is what lets `tab close` share one
// channel across its inspect / push / teardown sequence without every helper
// in the chain having to know which transport it got.
type workerCaller func(method string, in proto.Message, out proto.Message) error

// The context rides on the bound caller, so this takes none of its own.
func inspectLastTabCloseBest(call workerCaller, tabType leapmuxv1.TabType, tabID string) (*leapmuxv1.InspectLastTabCloseResponse, error) {
	resp := &leapmuxv1.InspectLastTabCloseResponse{}
	if err := call("InspectLastTabClose", &leapmuxv1.InspectLastTabCloseRequest{TabType: tabType, TabId: tabID}, resp); err != nil {
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

// discardRefusalMessage returns the message that refuses a
// `--worktree=discard`, or "" when nothing refuses it.
//
// It tests the disposition as well as the reason, because the reason describes
// the REMOVAL only: `keep` and `push` leave the worktree in place, so a locked
// worktree must not stop either of them. The worker states the reason (git's
// rules are its to read) and this only frames it, the same split
// pushBlockedReason has.
func discardRefusalMessage(wt tabCloseWorktree, r *leapmuxv1.InspectLastTabCloseResponse) string {
	if wt != closeWorktreeDiscard {
		return ""
	}
	reason := r.GetWorktreeRemovalBlockedReason()
	if reason == "" {
		return ""
	}
	return "cannot discard: " + reason
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

// closeBatchOps builds the ops a `tab close` submits: the subagent tabs that go
// with this one, then the tab itself.
//
// The ORDER is the point, and it is why the inspect reports the subtree at all.
// A client that learned the ids only from the CloseAgent response would have to
// tombstone the parent first and the children a round trip later; every peer
// would then promote the orphaned children to top-level rows claiming a lineage
// the user can no longer see, for as long as the worker teardown takes -- which
// for a close that removes a worktree is seconds.
//
// ONE batch, so the two land together on every peer. `subagentTombstoneOps`
// keeps only the ids this account holds a live tab for, which is what makes one
// batch safe: a never-opened subagent would otherwise reject the batch and take
// the parent's own tombstone down with it.
func closeBatchOps(bs *CRDTBootstrap, tt leapmuxv1.TabType, tabID string, descendantIDs []string) []*leapmuxv1.CrdtOp {
	return append(subagentTombstoneOps(bs, descendantIDs), opTombstoneTab(bs, tt, tabID))
}

// exceptAlreadySubmitted returns the ids in `all` that `submitted` does not
// hold, preserving order.
//
// A `tab close` learns the subtree twice: from the inspect, before the parent's
// tombstone, and from CloseAgent, after the teardown. The second answer is a
// superset -- it also holds anything the provider spawned while it drained --
// so only the difference belongs in the second batch. Re-tombstoning an id the
// first batch already retired would fail the whole batch, because the hub
// cannot resolve a workspace for a record whose tile id the first tombstone
// stripped.
func exceptAlreadySubmitted(all, submitted []string) []string {
	if len(submitted) == 0 {
		return all
	}
	seen := make(map[string]struct{}, len(submitted))
	for _, id := range submitted {
		seen[id] = struct{}{}
	}
	out := make([]string, 0, len(all))
	for _, id := range all {
		if _, ok := seen[id]; !ok {
			out = append(out, id)
		}
	}
	return out
}

// subagentTombstoneOps turns the worker's descendant-agent list into one
// TombstoneTab op per subagent tab that this account actually HAS.
//
// The filter is not an optimization. The worker answers from the `agents`
// table, where a row exists for every subagent the provider ever spawned --
// `EnsureChildAgent` creates one on first sight of a spawn span, whether or not
// anyone opened that transcript as a tab. The hub resolves a tombstone's
// workspace through the tab record, so an id it has no record for resolves to
// none and the batch is rejected as UNKNOWN_WORKSPACE -- which is fatal for the
// WHOLE batch, not just that op. Unfiltered, one never-opened subagent would
// therefore leave every REAL subagent tab open and report a close failure. An
// already-tombstoned id fails the same way, so this drops those too.
//
// Every id it keeps is an AGENT tab: a subagent IS an agent, and its agent id
// is its tab id. A different type here would tombstone a terminal or file tab
// that happens to share the id, which is why this does not take the type from
// the tab being closed. Separate from the caller so that stays true under test.
func subagentTombstoneOps(bs *CRDTBootstrap, subagentIDs []string) []*leapmuxv1.CrdtOp {
	if len(subagentIDs) == 0 {
		return nil
	}
	tabs := bs.State.GetTabs()
	ops := make([]*leapmuxv1.CrdtOp, 0, len(subagentIDs))
	for _, id := range subagentIDs {
		rec, ok := tabs[id]
		if !ok || rec == nil || !crdt.HLCIsZero(rec.GetTombstoneAt()) {
			continue
		}
		ops = append(ops, opTombstoneTab(bs, leapmuxv1.TabType_TAB_TYPE_AGENT, id))
	}
	return ops
}

// Returns the subagent tabs the close retired underneath an AGENT tab, so the
// caller can tombstone them in the same command. Empty for every other type.
func dispatchWorkerClose(call workerCaller, got resolve.Resolved, tt leapmuxv1.TabType, action leapmuxv1.WorktreeAction) ([]string, error) {
	if got.WorkerID == "" {
		return nil, nil
	}
	switch tt {
	case leapmuxv1.TabType_TAB_TYPE_AGENT:
		resp := &leapmuxv1.CloseAgentResponse{}
		if err := call("CloseAgent",
			&leapmuxv1.CloseAgentRequest{AgentId: got.TabID, WorktreeAction: action},
			resp); err != nil {
			return nil, err
		}
		return resp.GetDescendantAgentIds(), nil
	case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
		return nil, call("CloseTerminal",
			&leapmuxv1.CloseTerminalRequest{
				TerminalId:     got.TabID,
				WorktreeAction: action,
			},
			&leapmuxv1.CloseTerminalResponse{})
	case leapmuxv1.TabType_TAB_TYPE_FILE, leapmuxv1.TabType_TAB_TYPE_IMAGE:
		// A payload-backed tab DOES have a worker-side row:
		// worker_tab_payloads, plus the worktree_tabs link that ref-counts its
		// worktree. Revoking is what drives the shared closeTabCommon flow --
		// the same one CloseAgent and CloseTerminal drive -- so
		// `--worktree=discard` actually removes the worktree and the link does
		// not linger as a strand for the orphan reconciler to sweep later. This
		// used to fall through to the default and dispatch nothing, on the
		// belief that the CRDT tombstone was the whole teardown; it is not, and
		// the frontend's own close has always called this RPC.
		return nil, call("RevokeTabPayload",
			&leapmuxv1.RevokeTabPayloadRequest{
				TabId:          got.TabID,
				WorktreeAction: action,
			},
			&leapmuxv1.RevokeTabPayloadResponse{})
	default:
		// UNSPECIFIED never reaches here -- the caller resolved a concrete tab
		// before dispatching.
		return nil, nil
	}
}
