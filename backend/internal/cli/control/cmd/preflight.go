package cmd

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect/v2"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

// Preflight helpers validate that user-supplied entity IDs refer to
// real, accessible entities BEFORE the CLI submits a mutation. They
// fail fast with clear error envelopes (`not_found`, `invalid_request`)
// instead of letting a CRDT batch land with garbage references and
// produce orphan rows.
//
// Defense in depth: the hub also validates `worker_id` references in
// `crdt.ValidateBatch` (`BATCH_REJECTION_INVALID_WORKER_REF`), so a
// trustless client that bypasses the CLI still can't write a tab
// pointing at a non-existent worker. The CLI preflight exists for UX
// — a friendlier error message and one fewer round-trip.
//
// One exception to "fail fast": the worker check is best-effort and
// proceeds unvalidated when it cannot run at all. See
// maybePreflightWorker for why. Every other helper here fails closed.

// preflightTileKind returns the canonical "tile X is a Y node; <verb>
// only operates on <wantLabel> tiles" envelope when the state's node
// for `tileID` doesn't match `want`. Returns nil when the kind matches.
// Used by every tile verb that mutates a register specific to one
// NodeKind (set-ratios on SPLIT, set-grid-ratios on GRID, remove-grid
// on GRID, …) so each call site avoids the per-verb `fmt.Sprintf`
// boilerplate.
func preflightTileKind(state *leapmuxv1.UserMaterialized, tileID string, want leapmuxv1.NodeKind, verb, wantLabel, extra string) error {
	kind := state.GetNodes()[tileID].GetKind().GetValue()
	if kind == want {
		return nil
	}
	msg := fmt.Sprintf("tile %s is a %s node; %s only operates on %s tiles", tileID, kindLabel(kind), verb, wantLabel)
	if extra != "" {
		msg = msg + " " + extra
	}
	return cli.Usagef("%s", msg)
}

// maybePreflightWorker verifies that workerID identifies a worker the caller
// owns, when that question can be answered at all. Empty workerID is a no-op
// (the flag is optional on every caller; some resolve the worker later from a
// configured default or an agent lookup). An unknown or unauthorised id
// surfaces as `not_found`, because the hub's GetWorker answers NotFound for a
// worker that is not the caller's. One GetWorker round-trip per call.
//
// The check reads only whether GetWorker finds the row. GetWorker hides a
// deleted worker as ListWorkers does, so such a worker answers not_found here
// too. The hub stays the authority on whether the worker is usable now: its
// reach gate refuses a channel to a worker that is not ACTIVE, and the CRDT
// validator refuses a tab bound to one.
//
// A FAILED lookup is not fatal: the command proceeds unvalidated.
// This check is advisory (see the file header -- the hub independently
// rejects bogus worker refs), so its whole job is to trade an opaque
// downstream error for a friendly "no such worker" one. Aborting
// because the nicety itself couldn't run would be strictly worse than
// not having it.
//
// That is not hypothetical. Every worker-spawned agent inherits
// $LEAPMUX_CONTROL_WORKER_ID, which the session takes as the Worker of
// the calling tab -- so this preflight runs on essentially every
// `leapmux control` command an agent issues, over a delegation bearer
// the hub limits with `auth.CeilingFor(BearerKindDelegation)`. That
// ceiling admits worker:read, so GetWorker usually answers -- but the
// grant is a property of the bearer rather than of this code, and a
// narrower one refuses. Tolerating the denial is what keeps the command
// working under either.
//
// The tolerance is scoped to the DENIAL, not to every error. It used to be
// "tolerate anything", because controlipc.Router.CallInner flattened every hub
// failure to connect Internal and a CodePermissionDenied test could not fire on
// the transport where the denial actually happens. That flattening is fixed
// (see controlipc.relayError), so the check can be narrow again: a denial is
// expected and tolerated, while a transport failure surfaces with its own
// code instead of silently downgrading the check to "unvalidated".
//
// The error of the lookup is never emitted as it is: this function decides how
// to report it, so the command prints one envelope.
func maybePreflightWorker(ctx context.Context, c control.Peer, workerID string) error {
	if workerID == "" {
		return nil
	}
	var resp leapmuxv1.GetWorkerResponse
	err := hubCallUnary(ctx, c, leapmuxv1connect.WorkerManagementServiceGetWorkerProcedure, &leapmuxv1.GetWorkerRequest{WorkerId: workerID}, &resp)
	switch {
	case err == nil:
		return nil
	case connect.CodeOf(err) == connect.CodeNotFound:
		return cli.Newf(cli.CodeNotFound, "no such worker: %s", workerID)
	case isPreflightDenied(err):
		// The designed outcome for a delegation bearer: an advisory check
		// this caller is not allowed to run must not fail the command.
		return nil
	default:
		return err
	}
}

// isPreflightDenied reports whether err is the hub refusing to answer the
// preflight for this caller, as opposed to the preflight failing to run.
//
// Unauthenticated counts alongside PermissionDenied: a delegation bearer whose
// scope excludes WorkerManagementService can surface either, depending on
// whether the interceptor rejects the procedure or the credential.
func isPreflightDenied(err error) bool {
	switch connect.CodeOf(err) {
	case connect.CodePermissionDenied, connect.CodeUnauthenticated:
		return true
	default:
		return false
	}
}

// preflightTile returns nil when tileID names a live node belonging
// to workspaceID. Uses the materialized state already in hand from
// the CRDT bootstrap — no extra round-trip.
func preflightTile(state *leapmuxv1.UserMaterialized, workspaceID, tileID string) error {
	if tileID == "" {
		return cli.Usagef("no tile given")
	}
	rec, ok := state.GetNodes()[tileID]
	if !ok || rec == nil {
		return cli.Newf(cli.CodeNotFound, "no such tile: %s", tileID)
	}
	if !crdt.HLCIsZero(rec.GetTombstoneAt()) {
		return cli.Newf(cli.CodeNotFound, "tile is tombstoned: %s", tileID)
	}
	if workspaceID != "" {
		if ws := nodeWorkspaceFromState(state, tileID); ws != workspaceID {
			return cli.Newf(cli.CodeNotFound, "tile %s does not belong to workspace %s", tileID, workspaceID)
		}
	}
	return nil
}

// preflightTab returns nil when tabID names a live tab of the given
// tabType placed under workspaceID. workspaceID == "" skips the
// placement check (callers that don't know the workspace pass "").
func preflightTab(state *leapmuxv1.UserMaterialized, workspaceID, tabID string, tabType leapmuxv1.TabType) error {
	if tabID == "" {
		return cli.Usagef("no tab given")
	}
	rec, ok := state.GetTabs()[tabID]
	if !ok || rec == nil {
		return cli.Newf(cli.CodeNotFound, "no such tab: %s", tabID)
	}
	if !crdt.HLCIsZero(rec.GetTombstoneAt()) {
		return cli.Newf(cli.CodeNotFound, "tab is tombstoned: %s", tabID)
	}
	if tabType != leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED && rec.GetTabType() != tabType {
		return cli.Usagef("tab %s has type %s; expected %s",
			tabID, resolve.TabTypeWireName(rec.GetTabType()), resolve.TabTypeWireName(tabType))
	}
	if workspaceID != "" {
		tileID := rec.GetTileId().GetValue()
		if ws := nodeWorkspaceFromState(state, tileID); ws != workspaceID {
			return cli.Newf(cli.CodeNotFound, "tab %s does not belong to workspace %s", tabID, workspaceID)
		}
	}
	return nil
}

// nodeRootFromState walks node parents until a workspace root or live
// floating-window root matches. Returns ("", "", false) when the chain does
// not end at a known root.
func nodeRootFromState(state *leapmuxv1.UserMaterialized, nodeID string) (workspaceID, rootNodeID string, isFloating bool) {
	return crdt.FindNodeRoot(state.GetNodes(), state.GetWorkspaces(), state.GetFloatingWindows(), nodeID)
}

// nodeWorkspaceFromState walks node parents until a workspace root or live
// floating-window root matches. Returns "" when the chain does not end at a
// known workspace — usually means the node is orphaned or belongs to a
// workspace the caller cannot see.
func nodeWorkspaceFromState(state *leapmuxv1.UserMaterialized, nodeID string) string {
	wsID, _, _ := nodeRootFromState(state, nodeID)
	return wsID
}

// isWorkerUnreachable reports whether err describes a worker that
// can't be talked to — the worker row is gone, the bearer can't
// reach it, or the hub-side handshake refuses for an existence/auth
// reason. This is the carve-out close commands use to fall back to
// a CRDT-only tombstone: a tab whose worker no longer exists must
// still be removable.
//
// Conservative on purpose. Transient transport failures (timeouts,
// 5xx, gRPC Internal) do NOT match — falling back on those could
// leave a half-closed worker entity. Match only the codes that mean
// "this worker / channel really isn't available for you to call":
// NotFound, PermissionDenied, Unauthenticated, Unavailable.
//
// Only the channel-open path is treated as worker-unreachable: an
// existence-class connect code surfacing through any OTHER stage
// (marshal, unmarshal, rpc-inside-an-open-channel) is a bug
// elsewhere, not a missing worker, and must not silently tombstone.
func isWorkerUnreachable(err error) bool {
	if err == nil {
		return false
	}
	var coded *codedRPCError
	if errors.As(err, &coded) {
		return coded.Code == "channel_open_failed" && classifyConnectCode(coded.Cause)
	}
	return classifyConnectCode(err)
}

// classifyConnectCode returns true when err's connect.Code is one of
// the four existence/auth codes that warrant a CRDT-only fallback.
func classifyConnectCode(err error) bool {
	switch connect.CodeOf(err) {
	case connect.CodeNotFound,
		connect.CodePermissionDenied,
		connect.CodeUnauthenticated,
		connect.CodeUnavailable:
		return true
	default:
		return false
	}
}
