package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

// newTabCloseLeafFor builds `tab close`, `agent close`, or `terminal close`.
func newTabCloseLeafFor(kind resolve.Kind) *cobra.Command {
	noun := kind.String()
	short := "Close one or more tabs and stop what runs in them"
	switch kind {
	case resolve.KindAgent:
		short = "Close one or more agent tabs and stop their processes"
	case resolve.KindTerminal:
		short = "Close one or more terminal tabs and stop their shells"
	case resolve.KindTab, resolve.KindTile, resolve.KindWorkspace, resolve.KindWorker:
	}
	return cli.NewLeaf(cli.Leaf{
		Use:         "close <" + noun + ">...",
		Aliases:     []string{"rm"},
		Short:       short,
		Mutates:     true,
		Destructive: true,
		Examples: []cli.Example{
			{Command: "leapmux " + noun + " close 87a19d5f"},
			{Comment: "Close the last tab of a worktree and keep the worktree.", Command: "leapmux " + noun + " close 87a19d5f --worktree keep"},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.String("worktree", "", "what happens to the git worktree when this is its last tab; required in that case")
			cli.SetEnum(fs, "worktree", "keep", "push", "discard")
			cli.SetFlagGroup(fs, "worktree", "Worktree")
			fs.Bool("allow-busy", false, allowBusyFlagHelp)
			fs.Bool(allowSelfFlag, false, "close the tab even when it is this tab, which ends this command's own process")
			cli.SetFlagGroup(fs, "allow-busy", "Safety")
			cli.SetFlagGroup(fs, allowSelfFlag, "Safety")
			addTargetWorkspaceFlag(fs)
			cli.SetFlagGroup(fs, "workspace", "Target")
		},
		Complete: completeTargets,
		Run: func(inv *cli.Invocation) (any, error) {
			return runTabCloseFor(inv, kind)
		},
	})
}

type tabCloseBatchResult struct {
	Closed []*tabCloseResult `json:"closed"`
}

func (r tabCloseBatchResult) QuietLines() []string {
	out := make([]string, 0, len(r.Closed))
	for _, c := range r.Closed {
		out = append(out, c.ID)
	}
	return out
}

func (r tabCloseBatchResult) RenderText(w io.Writer) error {
	for _, c := range r.Closed {
		if err := c.RenderText(w); err != nil {
			return err
		}
	}
	return nil
}

// runTabClose tombstones a tab via UserCRDT.SubmitOps and dispatches
// worker-side teardown (CloseTab).
// The hub's remove-wins semantics mean the tab id is dead afterward;
// recreating at the UI level mints a fresh id.
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
// unpushed changes, --worktree is REQUIRED — omitting it fails as a
// usage error. This mirrors the frontend's forced-choice dialog:
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
// `guardTabClose` rejects that case unless --allow-self is supplied.
func runTabCloseFor(inv *cli.Invocation, kind resolve.Kind) (any, error) {
	wt, err := parseTabCloseWorktree(stringFlag(inv, "worktree"))
	if err != nil {
		return nil, err
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	targets := inv.Variadic(kind.String())
	allowSelf := boolFlag(inv, allowSelfFlag)
	allowBusy := boolFlag(inv, "allow-busy")

	resolved, err := s.resolveVariadicTabsWithOptions(kind, targets, resolve.Need{
		WorkspaceID: true,
		TileID:      true,
		WorkerID:    true,
		Want:        resolve.Wants{TabType: true},
	}, variadicTabOptions{
		SkipUnopenedChildren: kind == resolve.KindAgent || kind == resolve.KindTab,
	})
	if err != nil {
		return nil, err
	}
	if len(resolved) == 0 {
		return nil, cli.Newf(cli.CodeNotFound, "no open child agent tabs to close")
	}

	// Evaluate safety guards across every target before opening the CRDT or
	// closing any tab, so a later self-target refusal never destroys earlier
	// targets in the same batch.
	workspaceIDs := make([]string, 0, len(resolved))
	for _, got := range resolved {
		if err := guardTabClose(got.ID, allowSelf); err != nil {
			return nil, err
		}
		if got.Type == leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED {
			return nil, cli.Newf(cli.CodeNotFound, "the hub reports no type for tab %s", got.ID)
		}
		workspaceIDs = append(workspaceIDs, got.WorkspaceID)
	}

	cc, err := s.openCRDT(workspaceIDs...)
	if err != nil {
		return nil, err
	}
	for _, got := range resolved {
		if err := preflightTab(cc.bs.State, got.WorkspaceID, got.ID, got.Type); err != nil {
			return nil, err
		}
	}

	// Group resolved targets by Worker in first-seen order so each Worker pays
	// for one Noise_NK channel across all its tabs, while preserving the
	// caller's target order in the returned results slice.
	type indexedTarget struct {
		index int
		tab   tabTarget
	}
	var workerOrder []string
	byWorker := make(map[string][]indexedTarget)
	for i, got := range resolved {
		if _, seen := byWorker[got.WorkerID]; !seen {
			workerOrder = append(workerOrder, got.WorkerID)
		}
		byWorker[got.WorkerID] = append(byWorker[got.WorkerID], indexedTarget{index: i, tab: got})
	}

	tabRefsFor := func(items []indexedTarget) []crdt.TabRef {
		refs := make([]crdt.TabRef, 0, len(items))
		for _, item := range items {
			refs = append(refs, crdt.TabRef{TabType: item.tab.Type, TabID: item.tab.ID})
		}
		return refs
	}

	// When closing 2+ tabs without --worktree, preflight dirty worktrees and
	// branches for the full closing set before closing any tab. Otherwise
	// closing two sibling tabs on a dirty worktree would destroy tab 1 (seeing
	// tab 2 as a surviving sibling) and only fail on tab 2.
	// When all tabs share one Worker, the preflight runs on that Worker's
	// channel below to avoid opening a second Noise_NK channel.
	preflightBatch := len(resolved) >= 2 && wt == closeWorktreeUnspecified
	if preflightBatch && len(workerOrder) > 1 {
		errs := make([]error, len(workerOrder))
		var g errgroup.Group
		g.SetLimit(detailFanoutLimit)
		for i, workerID := range workerOrder {
			refs := tabRefsFor(byWorker[workerID])
			g.Go(func() error {
				errs[i] = withBestEffortWorkerChannel(s.ctx, s.c, workerID, func(w workerCall) error {
					return preflightBatchWorktreesOnChannel(s.ctx, w, refs)
				})
				return nil
			})
		}
		_ = g.Wait()
		for _, perr := range errs {
			if perr != nil {
				return nil, perr
			}
		}
	}

	results := make([]*tabCloseResult, len(resolved))
	discardFailed := false
	for _, workerID := range workerOrder {
		items := byWorker[workerID]
		// Channel creation is best-effort so the operator can still remove a
		// tab on an unreachable Worker with the applicable override. Each call
		// can report why the channel could not open.
		err = withBestEffortWorkerChannel(s.ctx, s.c, workerID, func(w workerCall) error {
			if preflightBatch && len(workerOrder) == 1 {
				if perr := preflightBatchWorktreesOnChannel(s.ctx, w, tabRefsFor(items)); perr != nil {
					return perr
				}
			}
			for _, item := range items {
				res, closeErr := closeTabOnWorker(s.ctx, w, cc, item.tab, wt, allowBusy)
				if closeErr != nil {
					return closeErr
				}
				s.invalidateLocatedTab(item.tab.ID)
				if res.worktreeDiscardFailed() {
					discardFailed = true
				}
				results[item.index] = res
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	var payload any = tabCloseBatchResult{Closed: results}
	if len(results) == 1 {
		payload = results[0]
	}
	if discardFailed {
		if err := inv.Printer.Result(payload); err != nil {
			return nil, err
		}
		return nil, &cli.ExitStatus{Code: cli.ExitFailure}
	}
	return payload, nil
}

// tabCloseResult is the result of `tab close`.
type tabCloseResult struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Status     string `json:"status"`
	Tombstoned bool   `json:"tombstoned"`
	Worktree   string `json:"worktree,omitempty"`
	// BusyOverride is true when --allow-busy suppressed a refusal, so a
	// script can tell "closed an idle tab" from "closed over running
	// work".
	BusyOverride             bool     `json:"busy_override,omitempty"`
	InspectHint              string   `json:"inspect_hint,omitempty"`
	WorkerCloseError         string   `json:"worker_close_error,omitempty"`
	WorktreeRemoval          string   `json:"worktree_removal,omitempty"`
	WorktreePath             string   `json:"worktree_path,omitempty"`
	WorktreeID               string   `json:"worktree_id,omitempty"`
	WorkerCloseFailure       string   `json:"worker_close_failure,omitempty"`
	WorkerCloseFailureDetail string   `json:"worker_close_failure_detail,omitempty"`
	ClosedSubagentIDs        []string `json:"closed_subagent_ids,omitempty"`
	SubagentCloseError       string   `json:"subagent_close_error,omitempty"`
}

func (r *tabCloseResult) worktreeDiscardFailed() bool {
	return r.Worktree == string(closeWorktreeDiscard) &&
		(r.WorktreeRemoval == "failed" || r.WorkerCloseFailure != "" || r.WorkerCloseError != "")
}

// RenderText prints a concise close summary and any partial-failure warning details.
func (r *tabCloseResult) RenderText(w io.Writer) error {
	line := fmt.Sprintf("closed %s %s", r.Type, cli.ShortID(r.ID))
	if r.WorktreeRemoval != "" {
		line += fmt.Sprintf(" (worktree: %s)", r.WorktreeRemoval)
	}
	if r.Status == "partial_failure" {
		line += " [partial_failure]"
	}
	if _, err := fmt.Fprintln(w, cli.SanitizeSingleLine(line)); err != nil {
		return err
	}
	if r.WorkerCloseFailure != "" {
		msg := "warning: " + r.WorkerCloseFailure
		if r.WorkerCloseFailureDetail != "" {
			msg += ": " + r.WorkerCloseFailureDetail
		}
		if _, err := fmt.Fprintln(w, cli.SanitizeSingleLine(msg)); err != nil {
			return err
		}
	}
	if r.WorkerCloseError != "" {
		if _, err := fmt.Fprintf(w, "warning: worker teardown error: %s\n", cli.SanitizeSingleLine(r.WorkerCloseError)); err != nil {
			return err
		}
	}
	if r.SubagentCloseError != "" {
		if _, err := fmt.Fprintf(w, "warning: subagent teardown error: %s\n", cli.SanitizeSingleLine(r.SubagentCloseError)); err != nil {
			return err
		}
	}
	if r.InspectHint != "" {
		if _, err := fmt.Fprintf(w, "hint: %s\n", cli.SanitizeSingleLine(r.InspectHint)); err != nil {
			return err
		}
	}
	return nil
}

// closeTabOnWorker runs the close from the inspect to the result, and it
// makes every worker call on w.
//
// w is valid only inside the body of withBestEffortWorkerChannel, which closes
// the shared channel when it returns. A parameter keeps each use inside that
// body, where a variable outside the body would allow a call after the close.
func closeTabOnWorker(ctx context.Context, w workerCall, cc *crdtCall, got tabTarget, wt tabCloseWorktree, allowBusy bool) (*tabCloseResult, error) {
	tt := got.Type
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
	inspected, ierr := inspectLastTabCloseBest(callWorker, tt, got.ID)
	if ierr != nil {
		// Worker unreachable / not found is the same fallback the
		// frontend uses: skip the dialog, proceed with implicit
		// KEEP. The CRDT tombstone still runs and the worker's
		// reconciler eventually catches up.
		if !isWorkerUnreachable(ierr) {
			return nil, ierr
		}
	}

	// Refuse an ineligible discard before the tombstone, even when no dialog applies.
	if msg := discardRefusalMessage(wt, inspected); msg != "" {
		return nil, cli.Usagef("%s", msg)
	}

	if inspected.GetShouldPrompt() && wt == closeWorktreeUnspecified {
		return nil, lastTabPromptError(inspected)
	}

	// A worktree choice and permission to interrupt work apply to different
	// parts of the close. Check both before pushing or tombstoning the tab.
	busyOverridden := false
	if tabNeedsBusyProbe(tt) {
		var busy []tabBusyReason
		probeErr := ierr
		if probeErr == nil {
			busy, probeErr = inspectTabsBusy(callWorker, []tabRef{{tabType: tt, tabID: got.ID}})
		}
		if refusal := busyCloseRefusal(busy, probeErr); refusal != nil {
			if !allowBusy {
				return nil, refusal
			}
			busyOverridden = true
		}
	}

	if inspected.GetShouldPrompt() {
		if wt == closeWorktreePush {
			if !inspected.GetGitState().GetCanPush() {
				return nil, cli.Usagef("cannot push: %s", pushBlockedReason(inspected))
			}
			if err := callWorker(leapmuxv1connect.WorkerGitServicePushBranchProcedure, &leapmuxv1.PushBranchRequest{WorkingDir: inspected.GetWorkingDir()}, &leapmuxv1.PushBranchResponse{}); err != nil {
				return nil, err
			}
		}
	}
	// Carry the degraded-close hint into the output so a CLI user
	// sees the close proceeded without the git-state check (mirrors
	// the frontend's warn toast on the same field). Safe on the
	// unreachable-worker path above: `inspected` is nil there, and a
	// generated getter on a nil message answers the zero value.
	inspectHint = inspected.GetErrorHint()

	if err := cc.submitOps(closeBatchOps(cc.bs, tt, got.ID, inspected.GetDescendantAgentIds())); err != nil {
		return nil, err
	}

	// Worker-side teardown. Mirrors useTabOperations.handleTabClose:
	// the CRDT tombstone is the authoritative removal, and these
	// inner-RPCs are fire-and-forget cleanup (PTY teardown, optional
	// worktree removal). callInnerRPC failures are demoted to envelope
	// fields so the user sees both the close success and the cleanup
	// outcome.
	closeResp, closeErr := dispatchWorkerClose(callWorker, got.WorkerID, got.ID, tt, worktreeAction)
	subagents := closeResp.GetDescendantAgentIds()

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
		// The close already succeeded, so a failure here is a field of the
		// result, not the command's error. trySubmitOps reports a rejection
		// as an error too, so one branch covers both.
		if _, _, err := cc.trySubmitOps(ops); err != nil {
			subagentErr = err
		}
	}

	out := &tabCloseResult{
		ID:                       got.ID,
		Type:                     resolve.TabTypeWireName(got.Type),
		Status:                   "ok",
		Tombstoned:               true,
		Worktree:                 string(wt),
		BusyOverride:             busyOverridden,
		InspectHint:              inspectHint,
		WorktreeRemoval:          enumToken(closeResp.GetWorktreeRemoval()),
		WorktreePath:             closeResp.GetWorktreePath(),
		WorktreeID:               closeResp.GetWorktreeId(),
		WorkerCloseFailure:       closeResp.GetFailureMessage(),
		WorkerCloseFailureDetail: closeResp.GetFailureDetail(),
		ClosedSubagentIDs:        subagents,
	}
	if closeErr != nil {
		out.WorkerCloseError = closeErr.Error()
	}
	if subagentErr != nil {
		out.SubagentCloseError = subagentErr.Error()
	}
	if out.WorkerCloseError != "" || out.SubagentCloseError != "" || out.WorkerCloseFailure != "" || out.WorktreeRemoval == "failed" {
		out.Status = "partial_failure"
	}
	return out, nil
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
}

func parseTabCloseWorktree(s string) (tabCloseWorktree, error) {
	if s == "" {
		return closeWorktreeUnspecified, nil
	}
	v, ok := tabCloseWorktreeMap[s]
	if !ok {
		return closeWorktreeUnspecified, cli.Usagef(`--worktree must be one of "keep", "push", "discard"; got %q`, s)
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
	if err := call(leapmuxv1connect.WorkerGitServiceInspectLastTabCloseProcedure, &leapmuxv1.InspectLastTabCloseRequest{Tab: &leapmuxv1.TabIdent{TabType: tabType, TabId: tabID}}, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// preflightBatchWorktreesOnChannel checks the full closing set on one Worker
// via InspectTabsClose before any tab is tombstoned or closed. Unreachable
// Workers follow the same fallback as single-tab closeTabOnWorker.
func preflightBatchWorktreesOnChannel(ctx context.Context, w workerCall, refs []crdt.TabRef) error {
	if len(refs) == 0 {
		return nil
	}
	if w.openErr != nil {
		if isWorkerUnreachable(w.openErr) {
			return nil
		}
		return w.openErr
	}
	resp, err := inspectTabsCloseOnChannel(ctx, w, refs)
	if err != nil {
		if isWorkerUnreachable(err) {
			return nil
		}
		return err
	}
	for _, item := range resp.GetItems() {
		if item == nil {
			continue
		}
		if item.GetShouldPrompt() || (item.GetIsWorktree() && item.GetGitState() != nil) {
			return lastTabPromptError(tabCloseInspectionToLastTabResponse(item))
		}
	}
	return nil
}

func tabCloseInspectionToLastTabResponse(item *leapmuxv1.TabCloseInspection) *leapmuxv1.InspectLastTabCloseResponse {
	target := leapmuxv1.LastTabCloseTarget_LAST_TAB_CLOSE_TARGET_BRANCH
	if item.GetIsWorktree() {
		target = leapmuxv1.LastTabCloseTarget_LAST_TAB_CLOSE_TARGET_WORKTREE
	}
	return &leapmuxv1.InspectLastTabCloseResponse{
		Target:       target,
		ShouldPrompt: true,
		WorktreePath: item.GetWorktreePath(),
		WorkingDir:   item.GetWorkingDir(),
		BranchName:   item.GetBranchName(),
		GitState:     item.GetGitState(),
	}
}

// lastTabPromptError builds the "must pass --worktree" error so the user
// sees exactly what state the close would lose.
func lastTabPromptError(r *leapmuxv1.InspectLastTabCloseResponse) error {
	choices := "keep|push"
	if r.GetTarget() == leapmuxv1.LastTabCloseTarget_LAST_TAB_CLOSE_TARGET_WORKTREE {
		choices += "|discard"
	}
	return cli.Usagef("%s", lastTabPromptMessage(r)).
		WithHint("pass --worktree=%s to say what happens to it", choices)
}

// lastTabPromptMessage describes what the close would lose. Mirrors the
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
		details = append(details, pluralize(int(n), "unpushed commit"))
	}
	if gs.GetRemoteBranchMissing() {
		details = append(details, "branch not pushed to remote")
	}
	if len(details) > 0 {
		b.WriteString(" (")
		b.WriteString(strings.Join(details, ", "))
		b.WriteString(")")
	}
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
	if !r.GetShouldPrompt() || r.GetTarget() != leapmuxv1.LastTabCloseTarget_LAST_TAB_CLOSE_TARGET_WORKTREE {
		return "cannot discard: this tab is not eligible to delete a linked worktree"
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
// A client that learned the ids only from the CloseTab response would have to
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
// tombstone, and from CloseTab, after the teardown. The second answer is a
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

// dispatchWorkerClose runs the Worker-side CloseTab RPC and returns its full
// response (including descendant subagents, worktree removal outcome, and any
// structured partial failure).
func dispatchWorkerClose(call workerCaller, workerID, tabID string, tt leapmuxv1.TabType, action leapmuxv1.WorktreeAction) (*leapmuxv1.CloseTabResponse, error) {
	if workerID == "" {
		return nil, nil
	}
	// One RPC for every tab type. A FILE or IMAGE tab DOES have a worker-side
	// row: worker_tab_payloads, plus the worktree_tabs link that ref-counts its
	// worktree. Closing it drives the shared closeTabCommon flow, so
	// `--worktree=discard` actually removes the worktree and the link does not
	// linger as a strand for the orphan reconciler to sweep later. UNSPECIFIED
	// never reaches here -- the caller resolved a concrete tab before
	// dispatching -- and the worker refuses it.
	resp := &leapmuxv1.CloseTabResponse{}
	if err := call(leapmuxv1connect.WorkerTabServiceCloseTabProcedure,
		&leapmuxv1.CloseTabRequest{
			Tab:            &leapmuxv1.TabIdent{TabType: tt, TabId: tabID},
			WorktreeAction: action,
		},
		resp); err != nil {
		return nil, err
	}
	return resp, nil
}
