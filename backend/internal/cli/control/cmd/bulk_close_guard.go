package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/sync/errgroup"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

// guardBulkCloseTabs checks active work and dirty or unpushed git checkouts for
// every tab in a bulk close over ONE concurrent per-Worker E2EE channel pass.
// Busy tabs take priority over dirty checkouts, and dirty checkouts take
// priority over probe failures. Any verification failure fails closed unless
// allowBusy is true.
func guardBulkCloseTabs(cc *crdtCall, tabs []crdt.TabRef, allowBusy bool) error {
	if allowBusy || len(tabs) == 0 {
		return nil
	}
	byWorker, order, workerless := groupTabsByWorker(tabs, cc.bs.State)

	var probeErrors []error
	for _, t := range workerless {
		if tabNeedsBusyProbe(t.TabType) {
			probeErrors = append(probeErrors, cli.Newf(cli.CodeUnavailable, "tab %s has no Worker to verify its activity", t.TabID))
		}
	}

	type workerGuardResult struct {
		busy     []tabBusyReason
		dirty    []tabBusyReason
		probeErr error
	}
	results := make([]workerGuardResult, len(order))
	var g errgroup.Group
	g.SetLimit(detailFanoutLimit)
	for i, workerID := range order {
		refs := byWorker[workerID]
		g.Go(func() error {
			chanErr := withBestEffortWorkerChannel(cc.ctx, cc.c, workerID, func(w workerCall) error {
				if w.openErr != nil {
					return w.openErr
				}
				var (
					busy          []tabBusyReason
					busyProbeErr  error
					dirty         []tabBusyReason
					dirtyProbeErr error
				)
				var wg errgroup.Group
				wg.Go(func() error {
					busy, busyProbeErr = inspectTabsBusyOnChannel(cc.ctx, w, refs)
					return nil
				})
				wg.Go(func() error {
					dirty, dirtyProbeErr = inspectTabsCloseDirtyOnChannel(cc.ctx, w, refs)
					return nil
				})
				_ = wg.Wait()
				results[i] = workerGuardResult{
					busy:     busy,
					dirty:    dirty,
					probeErr: errors.Join(busyProbeErr, dirtyProbeErr),
				}
				return nil
			})
			if chanErr != nil {
				results[i].probeErr = fmt.Errorf("worker %s unreachable; cannot verify active tabs or uncommitted git changes: %w", cli.ShortID(workerID), chanErr)
			}
			return nil
		})
	}
	_ = g.Wait()

	var allBusy []tabBusyReason
	var allDirty []tabBusyReason
	for _, r := range results {
		allBusy = append(allBusy, r.busy...)
		allDirty = append(allDirty, r.dirty...)
		if r.probeErr != nil {
			probeErrors = append(probeErrors, r.probeErr)
		}
	}
	if len(allBusy) > 0 {
		return errTabBusyRefused(allBusy)
	}
	if len(allDirty) > 0 {
		return errTabBusyRefused(allDirty)
	}
	return busyCloseRefusal(nil, errors.Join(probeErrors...))
}

// inspectTabsCloseOnChannel calls WorkerGitService.InspectTabsClose over an
// already-open Worker channel for all tabs in refs.
func inspectTabsCloseOnChannel(ctx context.Context, w workerCall, refs []crdt.TabRef) (*leapmuxv1.InspectTabsCloseResponse, error) {
	idents := make([]*leapmuxv1.TabIdent, 0, len(refs))
	for _, ref := range refs {
		idents = append(idents, &leapmuxv1.TabIdent{
			TabType: ref.TabType,
			TabId:   ref.TabID,
		})
	}
	resp := &leapmuxv1.InspectTabsCloseResponse{}
	if err := w.Call(ctx, leapmuxv1connect.WorkerGitServiceInspectTabsCloseProcedure, &leapmuxv1.InspectTabsCloseRequest{
		Tabs: idents,
	}, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// inspectTabsCloseDirtyOnChannel inspects all closing tabs on one Worker for
// uncommitted changes, unpushed commits, or an unpushed local branch. RPC
// errors and non-empty error_hint values are returned so callers fail closed.
func inspectTabsCloseDirtyOnChannel(ctx context.Context, w workerCall, refs []crdt.TabRef) ([]tabBusyReason, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	resp, err := inspectTabsCloseOnChannel(ctx, w, refs)
	if err != nil {
		return nil, fmt.Errorf("inspect git status for closing tabs: %w", err)
	}
	return dirtyCloseReasonsFromInspection(resp.GetItems())
}

// dirtyCloseReasonsFromInspection converts InspectTabsClose items from one
// Worker into deduplicated refusal reasons and probe errors. seenPath stays
// local to this Worker so identical paths on distinct Workers are verified
// independently.
func dirtyCloseReasonsFromInspection(items []*leapmuxv1.TabCloseInspection) ([]tabBusyReason, error) {
	var dirty []tabBusyReason
	var probeErrors []error
	seenPath := map[string]bool{}
	for _, item := range items {
		if item == nil {
			continue
		}
		tabID := item.GetTab().GetTabId()
		if hint := strings.TrimSpace(item.GetErrorHint()); hint != "" {
			probeErrors = append(probeErrors, cli.Newf(cli.CodeUnavailable, "cannot verify git status for tab %s: %s", tabID, hint))
			continue
		}
		if !item.GetShouldPrompt() {
			continue
		}
		parts := dirtyStateParts(item.GetGitState())
		if len(parts) == 0 {
			continue
		}
		pathKey := item.GetWorktreePath()
		if pathKey == "" {
			pathKey = item.GetWorkingDir()
		}
		if pathKey != "" {
			if seenPath[pathKey] {
				continue
			}
			seenPath[pathKey] = true
		}
		dirty = append(dirty, tabBusyReason{
			tabID:  tabID,
			detail: formatDirtyCheckoutDetail(item.GetBranchName(), pathKey, parts),
		})
	}
	return dirty, errors.Join(probeErrors...)
}

func dirtyStateParts(gs *leapmuxv1.BranchGitState) []string {
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
	return parts
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
