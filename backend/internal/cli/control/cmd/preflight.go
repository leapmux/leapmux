package cmd

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/control"
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
	return control.EmitError("invalid_request", msg)
}

// maybePreflightWorker verifies workerID names a worker the caller can
// use, when that question can be answered at all. Empty workerID is a
// no-op (the flag is optional on every caller; some resolve the worker
// later from a configured default or an agent lookup). An unknown or
// unauthorised id surfaces as `not_found`. One ListWorkers round-trip
// per call.
//
// A FAILED lookup is not fatal: the command proceeds unvalidated.
// This check is advisory (see the file header -- the hub independently
// rejects bogus worker refs), so its whole job is to trade an opaque
// downstream error for a friendly "no such worker" one. Aborting
// because the nicety itself couldn't run would be strictly worse than
// not having it.
//
// That is not hypothetical. Every worker-spawned agent inherits
// $LEAPMUX_CONTROL_WORKER_ID, which BindEntityFlags binds as the
// --worker-id default -- so this preflight runs on essentially every
// `leapmux control` command an agent issues, over a delegation bearer
// the hub limits with `auth.CeilingFor(BearerKindDelegation)`. That
// ceiling admits worker:read, so ListWorkers usually answers -- but the
// grant is a property of the bearer rather than of this code, and a
// narrower one refuses. Tolerating the denial is what keeps the command
// working under either.
//
// The tolerance is scoped to the DENIAL, not to every error. It used to be
// "tolerate anything", because controlipc.Router.CallInner flattened every hub
// failure to connect Internal and a CodePermissionDenied test could not fire on
// the transport where the denial actually happens. That flattening is fixed
// (see controlipc.relayError), so the check can be narrow again: a denial is
// expected and tolerated, while a transport failure surfaces as
// preflight_failed instead of silently downgrading the check to "unvalidated".
func maybePreflightWorker(ctx context.Context, c *control.Client, workerID string) error {
	if workerID == "" {
		return nil
	}
	workers, err := listAccessibleWorkers(ctx, c)
	if err != nil {
		if isPreflightDenied(err) {
			// The designed outcome for a delegation bearer: an advisory check
			// this caller is not allowed to run must not fail the command.
			return nil
		}
		return control.EmitErrorWith("preflight_failed", err)
	}
	if _, ok := workers[workerID]; !ok {
		return control.EmitError("not_found", "no such worker: "+workerID)
	}
	return nil
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

// listAccessibleWorkers fetches the set of workers the authenticated
// user can use, indexed by worker_id. The error is returned raw, NOT
// emitted as a JSON envelope: its only caller treats a failed lookup
// as "unvalidated" and keeps going, so emitting here would print a
// stray error envelope ahead of the command's real output.
func listAccessibleWorkers(ctx context.Context, c *control.Client) (map[string]*leapmuxv1.Worker, error) {
	var resp leapmuxv1.ListWorkersResponse
	if err := hubCallUnary(ctx, c, "ListWorkers", &leapmuxv1.ListWorkersRequest{}, &resp); err != nil {
		return nil, err
	}
	out := make(map[string]*leapmuxv1.Worker, len(resp.GetWorkers()))
	for _, w := range resp.GetWorkers() {
		out[w.GetId()] = w
	}
	return out, nil
}

// preflightTile returns nil when tileID names a live node belonging
// to workspaceID. Uses the materialized state already in hand from
// the CRDT bootstrap — no extra round-trip.
func preflightTile(state *leapmuxv1.UserMaterialized, workspaceID, tileID string) error {
	if tileID == "" {
		return control.EmitError("invalid_request", "--tile-id is required")
	}
	rec, ok := state.GetNodes()[tileID]
	if !ok || rec == nil {
		return control.EmitError("not_found", "no such tile: "+tileID)
	}
	if !crdt.HLCIsZero(rec.GetTombstoneAt()) {
		return control.EmitError("not_found", "tile is tombstoned: "+tileID)
	}
	if workspaceID != "" {
		if ws := nodeWorkspaceFromState(state, tileID); ws != workspaceID {
			return control.EmitError("not_found", fmt.Sprintf("tile %s does not belong to workspace %s", tileID, workspaceID))
		}
	}
	return nil
}

// preflightTab returns nil when tabID names a live tab of the given
// tabType placed under workspaceID. workspaceID == "" skips the
// placement check (callers that don't know the workspace pass "").
func preflightTab(state *leapmuxv1.UserMaterialized, workspaceID, tabID string, tabType leapmuxv1.TabType) error {
	if tabID == "" {
		return control.EmitError("invalid_request", "--tab-id is required")
	}
	rec, ok := state.GetTabs()[tabID]
	if !ok || rec == nil {
		return control.EmitError("not_found", "no such tab: "+tabID)
	}
	if !crdt.HLCIsZero(rec.GetTombstoneAt()) {
		return control.EmitError("not_found", "tab is tombstoned: "+tabID)
	}
	if tabType != leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED && rec.GetTabType() != tabType {
		return control.EmitError(
			"invalid_request",
			fmt.Sprintf("tab %s has type %s; expected %s",
				tabID, tabTypeName(rec.GetTabType()), tabTypeName(tabType)),
		)
	}
	if workspaceID != "" {
		tileID := rec.GetTileId().GetValue()
		if ws := nodeWorkspaceFromState(state, tileID); ws != workspaceID {
			return control.EmitError(
				"not_found",
				fmt.Sprintf("tab %s does not belong to workspace %s", tabID, workspaceID),
			)
		}
	}
	return nil
}

// nodeWorkspaceFromState walks node parents until a workspace root
// matches (workspaces.root_node_id). Returns "" when the chain
// doesn't terminate at a known workspace — usually means the node is
// orphaned or belongs to a workspace the caller can't see.
func nodeWorkspaceFromState(state *leapmuxv1.UserMaterialized, nodeID string) string {
	return crdt.FindRootWorkspace(state.GetNodes(), state.GetWorkspaces(), nodeID)
}

// isNotFoundOrForbidden returns true for connect errors that the hub
// uses to mean "the entity is absent from the caller's view". The hub
// deliberately conflates NotFound and PermissionDenied so an
// unauthorised caller can't probe for existence by status code.
func isNotFoundOrForbidden(err error) bool {
	code := connect.CodeOf(err)
	return code == connect.CodeNotFound || code == connect.CodePermissionDenied
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
