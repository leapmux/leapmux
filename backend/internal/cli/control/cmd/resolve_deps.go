package cmd

import (
	"context"
	"errors"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
)

// resolveDeps wires the universal ID resolver to the actual hub /
// worker RPC calls a `leapmux control` handler can make. Every
// derivation goes through hubCallUnary so the same Deps work for
// both hub-bound (laptop CLI) and local-IPC (worker-spawned)
// transports — the worker-side ControlIPC router proxies hub.* calls
// to the hub on our behalf, keeping the wire-level transport
// invisible to the resolver.
//
// GetWorkingDir intentionally calls the worker via callInnerRPCBest
// (NOT callInnerRPC) so a failure surfaces as nil/empty rather than
// flooding stdout with an error envelope. The resolver treats
// working_dir as best-effort.
func resolveDeps(c *control.Client) resolve.Deps {
	return resolve.Deps{
		LocateTab: func(ctx context.Context, tabType leapmuxv1.TabType, tabID string) (leapmuxv1.TabType, string, string, string, error) {
			var resp leapmuxv1.LocateTabResponse
			if err := hubCallUnary(ctx, c, "LocateTab", &leapmuxv1.LocateTabRequest{TabType: tabType, TabId: tabID}, &resp); err != nil {
				return leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED, "", "", "", err
			}
			t := resp.GetTab()
			return t.GetTabType(), t.GetWorkspaceId(), t.GetTileId(), t.GetWorkerId(), nil
		},
		GetWorkspace: func(ctx context.Context, workspaceID string) error {
			var resp leapmuxv1.GetWorkspaceResponse
			return hubCallUnary(ctx, c, "GetWorkspace", &leapmuxv1.GetWorkspaceRequest{WorkspaceId: workspaceID}, &resp)
		},
		LocateTile: func(ctx context.Context, tileID string) (string, error) {
			var resp leapmuxv1.LocateTileResponse
			if err := hubCallUnary(ctx, c, "LocateTile", &leapmuxv1.LocateTileRequest{TileId: tileID}, &resp); err != nil {
				return "", err
			}
			return resp.GetWorkspaceId(), nil
		},
		GetWorkingDir: func(ctx context.Context, workerID string, tabType leapmuxv1.TabType, tabID string) (string, error) {
			switch tabType {
			case leapmuxv1.TabType_TAB_TYPE_AGENT:
				var resp leapmuxv1.ListAgentsResponse
				if err := callInnerRPCBest(ctx, c, workerID, "ListAgents", &leapmuxv1.ListAgentsRequest{TabIds: []string{tabID}}, &resp); err != nil {
					return "", err
				}
				return findWorkingDir(resp.GetAgents(), tabID,
					(*leapmuxv1.AgentInfo).GetId, (*leapmuxv1.AgentInfo).GetWorkingDir), nil
			case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
				var resp leapmuxv1.ListTerminalsResponse
				if err := callInnerRPCBest(ctx, c, workerID, "ListTerminals", &leapmuxv1.ListTerminalsRequest{TabIds: []string{tabID}}, &resp); err != nil {
					return "", err
				}
				return findWorkingDir(resp.GetTerminals(), tabID,
					(*leapmuxv1.TerminalInfo).GetTerminalId, (*leapmuxv1.TerminalInfo).GetWorkingDir), nil
			default:
				return "", nil
			}
		},
	}
}

// findWorkingDir returns the working dir of the entry whose id is tabID, or ""
// when no entry matches.
//
// GetWorkingDir's AGENT and TERMINAL branches differ only in the response type
// and these two accessors, so the scan lives here once instead of being
// copy-pasted per tab type -- the next tab type with a working dir adds a call,
// not another loop. The id accessor is a parameter rather than an interface
// because the generated types spell it differently (GetId vs GetTerminalId) and
// neither is going to grow a shared one.
//
// A miss returns "" and NOT an error, matching GetWorkingDir's contract: the
// resolver treats working_dir as best-effort, and a worker that has already
// forgotten the tab is an ordinary outcome, not a failure.
func findWorkingDir[T any](items []T, tabID string, id func(T) string, workingDir func(T) string) string {
	for _, it := range items {
		if id(it) == tabID {
			return workingDir(it)
		}
	}
	return ""
}

// runResolve is the canonical handler entry point: build a client,
// parse flags into the resolver's Inputs via BindEntityFlags, call
// resolve.Resolve, and surface errors through control.EmitErrorWith.
// Each handler then has a populated Resolved struct to work from
// instead of hand-rolling its own resolution code path.
func runResolve(ctx context.Context, c *control.Client, need resolve.Need, in resolve.Inputs) (resolve.Resolved, error) {
	got, err := resolve.Resolve(ctx, resolveDeps(c), need, in)
	if err != nil {
		var re *resolve.ResolveError
		if errors.As(err, &re) {
			return resolve.Resolved{}, control.EmitError(re.Code, re.Message)
		}
		return resolve.Resolved{}, control.EmitErrorWith("resolve_failed", err)
	}
	return got, nil
}

// hasAnyEntityInput returns true if the caller supplied at least one
// entity-id flag (or its env-var fallback). Used by handler scaffolds
// to short-circuit with invalid_request before loading credentials —
// otherwise a forgotten --tab-id falls through to requireClient and
// surfaces a confusing `not_logged_in` envelope.
func hasAnyEntityInput(in resolve.Inputs) bool {
	return in.TabID != "" || in.TileID != "" || in.WorkspaceID != "" ||
		in.WorkerID != ""
}
