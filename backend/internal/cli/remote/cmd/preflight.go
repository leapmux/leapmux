package cmd

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/remote"
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

// preflightTileKind returns the canonical "tile X is a Y node; <verb>
// only operates on <wantLabel> tiles" envelope when the state's node
// for `tileID` doesn't match `want`. Returns nil when the kind matches.
// Used by every tile verb that mutates a register specific to one
// NodeKind (set-ratios on SPLIT, set-grid-ratios on GRID, remove-grid
// on GRID, …) so each call site avoids the per-verb `fmt.Sprintf`
// boilerplate.
func preflightTileKind(state *leapmuxv1.OrgMaterialized, tileID string, want leapmuxv1.NodeKind, verb, wantLabel, extra string) error {
	kind := state.GetNodes()[tileID].GetKind().GetValue()
	if kind == want {
		return nil
	}
	msg := fmt.Sprintf("tile %s is a %s node; %s only operates on %s tiles", tileID, kindLabel(kind), verb, wantLabel)
	if extra != "" {
		msg = msg + " " + extra
	}
	return remote.EmitError("invalid_request", msg)
}

// preflightWorker verifies workerID names a worker the authenticated
// user can use. Empty workerID is rejected as `invalid_request`;
// unknown / unauthorised IDs surface as `not_found`. One ListWorkers
// round-trip per call — callers that need to check several workers
// should hoist the listAccessibleWorkers call.
func preflightWorker(ctx context.Context, c *remote.Client, workerID string) error {
	if workerID == "" {
		return remote.EmitError("invalid_request", "--worker-id is required")
	}
	workers, err := listAccessibleWorkers(ctx, c)
	if err != nil {
		return err
	}
	if _, ok := workers[workerID]; !ok {
		return remote.EmitError("not_found", "no such worker: "+workerID)
	}
	return nil
}

// maybePreflightWorker validates workerID when non-empty, no-op
// otherwise. Used by commands where the flag is optional and may be
// resolved later (env var / configured default / agent lookup); the
// explicit-flag path still gets a fail-fast check.
func maybePreflightWorker(ctx context.Context, c *remote.Client, workerID string) error {
	if workerID == "" {
		return nil
	}
	return preflightWorker(ctx, c, workerID)
}

// listAccessibleWorkers fetches the set of workers the authenticated
// user can use, indexed by worker_id. Returns an already-wrapped
// `preflight_failed` error envelope on transport failure.
func listAccessibleWorkers(ctx context.Context, c *remote.Client) (map[string]*leapmuxv1.Worker, error) {
	var resp leapmuxv1.ListWorkersResponse
	if err := hubCallUnary(ctx, c, "ListWorkers", "", &leapmuxv1.ListWorkersRequest{}, &resp); err != nil {
		return nil, remote.EmitErrorWith("preflight_failed", err)
	}
	out := make(map[string]*leapmuxv1.Worker, len(resp.GetWorkers()))
	for _, w := range resp.GetWorkers() {
		out[w.GetId()] = w
	}
	return out, nil
}

// preflightWorkspace verifies workspaceID exists and the user can
// read it. NotFound / PermissionDenied collapse to `not_found` so a
// caller can't distinguish "no such workspace" from "no access" — the
// hub already deliberately conflates these to avoid info-leak.
//
// Most CRDT-bound commands go through openCRDTCall → resolveOrgID,
// which already calls GetWorkspace and returns NotFound on miss; this
// helper is the explicit version for non-CRDT commands.
func preflightWorkspace(ctx context.Context, c *remote.Client, workspaceID string) error {
	if workspaceID == "" {
		return remote.EmitError("invalid_request", "--workspace-id is required")
	}
	var resp leapmuxv1.GetWorkspaceResponse
	if err := hubCallUnary(ctx, c, "GetWorkspace", workspaceID, &leapmuxv1.GetWorkspaceRequest{WorkspaceId: workspaceID}, &resp); err != nil {
		if isNotFoundOrForbidden(err) {
			return remote.EmitError("not_found", "no such workspace: "+workspaceID)
		}
		return remote.EmitErrorWith("preflight_failed", err)
	}
	return nil
}

// preflightTile returns nil when tileID names a live node belonging
// to workspaceID. Uses the materialized state already in hand from
// the CRDT bootstrap — no extra round-trip.
func preflightTile(state *leapmuxv1.OrgMaterialized, workspaceID, tileID string) error {
	if tileID == "" {
		return remote.EmitError("invalid_request", "--tile-id is required")
	}
	rec, ok := state.GetNodes()[tileID]
	if !ok || rec == nil {
		return remote.EmitError("not_found", "no such tile: "+tileID)
	}
	if !crdt.HLCIsZero(rec.GetTombstoneAt()) {
		return remote.EmitError("not_found", "tile is tombstoned: "+tileID)
	}
	if workspaceID != "" {
		if ws := nodeWorkspaceFromState(state, tileID); ws != workspaceID {
			return remote.EmitError("not_found", fmt.Sprintf("tile %s does not belong to workspace %s", tileID, workspaceID))
		}
	}
	return nil
}

// preflightTab returns nil when tabID names a live tab of the given
// tabType placed under workspaceID. workspaceID == "" skips the
// placement check (callers that don't know the workspace pass "").
func preflightTab(state *leapmuxv1.OrgMaterialized, workspaceID, tabID string, tabType leapmuxv1.TabType) error {
	if tabID == "" {
		return remote.EmitError("invalid_request", "--tab-id is required")
	}
	rec, ok := state.GetTabs()[tabID]
	if !ok || rec == nil {
		return remote.EmitError("not_found", "no such tab: "+tabID)
	}
	if !crdt.HLCIsZero(rec.GetTombstoneAt()) {
		return remote.EmitError("not_found", "tab is tombstoned: "+tabID)
	}
	if tabType != leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED && rec.GetTabType() != tabType {
		return remote.EmitError(
			"invalid_request",
			fmt.Sprintf("tab %s has type %s; expected %s",
				tabID, tabTypeName(rec.GetTabType()), tabTypeName(tabType)),
		)
	}
	if workspaceID != "" {
		tileID := rec.GetTileId().GetValue()
		if ws := nodeWorkspaceFromState(state, tileID); ws != workspaceID {
			return remote.EmitError(
				"not_found",
				fmt.Sprintf("tab %s does not belong to workspace %s", tabID, workspaceID),
			)
		}
	}
	return nil
}

// preflightAgent / preflightTerminal are typed shortcuts over
// preflightTab. The CRDT records every spawned agent / terminal as a
// tab; the worker-side state can drift transiently but the canonical
// "this id is reachable from the CLI" check is the tab record.
func preflightAgent(state *leapmuxv1.OrgMaterialized, workspaceID, agentID string) error {
	return preflightTab(state, workspaceID, agentID, leapmuxv1.TabType_TAB_TYPE_AGENT)
}

func preflightTerminal(state *leapmuxv1.OrgMaterialized, workspaceID, terminalID string) error {
	return preflightTab(state, workspaceID, terminalID, leapmuxv1.TabType_TAB_TYPE_TERMINAL)
}

// nodeWorkspaceFromState walks node parents until a workspace root
// matches (workspaces.root_node_id). Returns "" when the chain
// doesn't terminate at a known workspace — usually means the node is
// orphaned or belongs to a workspace the caller can't see.
func nodeWorkspaceFromState(state *leapmuxv1.OrgMaterialized, nodeID string) string {
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
	}
	return false
}
