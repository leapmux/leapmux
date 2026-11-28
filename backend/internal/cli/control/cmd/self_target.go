package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"connectrpc.com/connect/v2"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
)

// Self-target guards block destructive `leapmux control` commands from
// silently tearing down the very tab the user is running them in. The
// only spawn-time anchor the worker injects is LEAPMUX_CONTROL_TAB_ID
// (workspace / tile env vars are intentionally omitted — they go stale
// on cross-workspace move and tile drag; see
// internal/worker/controlipc/router_test.go's `forbidden` list). The
// guards therefore key off the tab anchor and look up its current
// workspace / tile at call time.
//
// Each guard pairs with the command's --allow-self flag, which states the
// hazard it overrides. The refusal specifies the flag, as tab_busy_refused
// specifies --allow-busy.

// callingTabID returns the LEAPMUX_CONTROL_TAB_ID spawn anchor, or ""
// when the CLI was not invoked from inside a remote tab. Trimming
// guards against trailing newlines from shell substitutions.
func callingTabID() string {
	return strings.TrimSpace(os.Getenv(controlenv.TabIDVariable))
}

// callingWorkspace resolves the calling tab's current workspace. A missing
// anchor or a confirmed absent tab leaves nothing for the guard to protect.
func callingWorkspace(ctx context.Context, c control.Peer) (string, error) {
	self := callingTabID()
	if self == "" {
		return "", nil
	}
	req := &leapmuxv1.LocateTabRequest{TabId: self}
	var resp leapmuxv1.LocateTabResponse
	if err := hubCallUnary(ctx, c, leapmuxv1connect.WorkspaceServiceLocateTabProcedure, req, &resp); err != nil {
		if connect.CodeOf(err) == connect.CodeNotFound {
			return "", nil
		}
		return "", err
	}
	if workspace := resp.GetTab().GetWorkspaceId(); workspace != "" {
		return workspace, nil
	}
	return "", cli.Newf(cli.CodeInternal, "the Hub returned no workspace for the calling tab %s", self)
}

// allowSelfFlag overrides every self-target guard.
const allowSelfFlag = "allow-self"

// errSelfTargetRefused builds the error that a guard returns.
// `self_target_refused` is a distinct code, so a script can decide whether
// to run again with --allow-self.
func errSelfTargetRefused(detail string) error {
	return cli.Newf(cli.CodeSelfTargetRefused, "%s", detail).
		WithHint("pass --%s to do it anyway", allowSelfFlag)
}

// guardWorkspaceDelete blocks `workspace delete` when the calling
// tab lives inside the target workspace and --allow-self was not passed.
// Returns nil to proceed, or an error to abort.
func guardWorkspaceDelete(ctx context.Context, c control.Peer, targetWorkspaceID string, allowSelf bool) error {
	if allowSelf || targetWorkspaceID == "" {
		return nil
	}
	selfWS, err := callingWorkspace(ctx, c)
	if err != nil {
		cause := fmt.Errorf("check the calling tab before deleting workspace %s: %w", targetWorkspaceID, err)
		return &hubHintError{
			cause: withHubHintForPeer(c, cause),
			hints: []string{fmt.Sprintf("pass --%s to delete the workspace even if the command cannot verify the calling tab's location", allowSelfFlag)},
		}
	}
	return rejectIfSelfWorkspace(selfWS, targetWorkspaceID)
}

// rejectIfSelfWorkspace is the pure-decision core of
// guardWorkspaceDelete: given the already-resolved self workspace id
// and the target, return the error envelope when they coincide
// (nil otherwise). Split out so the comparison + envelope is unit-
// testable without standing up a hub client.
func rejectIfSelfWorkspace(selfWS, targetWS string) error {
	if selfWS == "" || targetWS == "" || selfWS != targetWS {
		return nil
	}
	return errSelfTargetRefused(
		"refusing to delete workspace " + targetWS +
			" because the calling tab lives in it",
	)
}

// guardTabClose blocks `tab close` when the target tab is the calling
// tab itself and --allow-self was not passed. Pure env-var comparison —
// no RPC.
func guardTabClose(targetTabID string, allowSelf bool) error {
	if allowSelf || targetTabID == "" {
		return nil
	}
	if callingTabID() != targetTabID {
		return nil
	}
	return errSelfTargetRefused(
		"refusing to close the calling tab " + targetTabID,
	)
}

// guardLayoutClose protects the caller when a tile or grid close removes its
// tab. The layout operation passes the set of tabs that it removes.
func guardLayoutClose(target string, tabs []crdt.TabRef, allowSelf bool) error {
	if allowSelf {
		return nil
	}
	self := callingTabID()
	if self == "" {
		return nil
	}
	for _, tab := range tabs {
		if tab.TabID == self {
			return errSelfTargetRefused(fmt.Sprintf("refusing to close %s because it contains the calling tab %s", target, self))
		}
	}
	return nil
}
