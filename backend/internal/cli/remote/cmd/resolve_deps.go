package cmd

import (
	"context"
	"errors"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/remote"
	"github.com/leapmux/leapmux/internal/cli/remote/resolve"
)

// resolveDeps wires the universal ID resolver to the actual hub /
// worker RPC calls a `leapmux remote` handler can make. Every
// derivation goes through hubCallUnary so the same Deps work for
// both hub-bound (laptop CLI) and local-IPC (worker-spawned)
// transports — the worker-side RemoteIPC router proxies hub.* calls
// to the hub on our behalf, keeping the wire-level transport
// invisible to the resolver.
//
// GetWorkingDir intentionally calls the worker via callInnerRPCBest
// (NOT callInnerRPC) so a failure surfaces as nil/empty rather than
// flooding stdout with an error envelope. The resolver treats
// working_dir as best-effort.
func resolveDeps(c *remote.Client) resolve.Deps {
	return resolve.Deps{
		LocateTab: func(ctx context.Context, tabType leapmuxv1.TabType, tabID string) (leapmuxv1.TabType, string, string, string, error) {
			var resp leapmuxv1.LocateTabResponse
			if err := hubCallUnary(ctx, c, "LocateTab", "", &leapmuxv1.LocateTabRequest{TabType: tabType, TabId: tabID}, &resp); err != nil {
				return leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED, "", "", "", err
			}
			t := resp.GetTab()
			return t.GetTabType(), t.GetWorkspaceId(), t.GetTileId(), t.GetWorkerId(), nil
		},
		GetWorkspace: func(ctx context.Context, workspaceID string) (string, string, error) {
			var resp leapmuxv1.GetWorkspaceResponse
			if err := hubCallUnary(ctx, c, "GetWorkspace", workspaceID, &leapmuxv1.GetWorkspaceRequest{WorkspaceId: workspaceID}, &resp); err != nil {
				return "", "", err
			}
			w := resp.GetWorkspace()
			return w.GetOrgId(), w.GetCreatedBy(), nil
		},
		GetWorker: func(ctx context.Context, workerID string) (string, string, error) {
			var resp leapmuxv1.GetWorkerResponse
			if err := hubCallUnary(ctx, c, "GetWorker", "", &leapmuxv1.GetWorkerRequest{WorkerId: workerID}, &resp); err != nil {
				return "", "", err
			}
			w := resp.GetWorker()
			return w.GetRegisteredBy(), w.GetOrgId(), nil
		},
		LocateTile: func(ctx context.Context, tileID string) (string, string, error) {
			var resp leapmuxv1.LocateTileResponse
			if err := hubCallUnary(ctx, c, "LocateTile", "", &leapmuxv1.LocateTileRequest{TileId: tileID}, &resp); err != nil {
				return "", "", err
			}
			return resp.GetWorkspaceId(), resp.GetOrgId(), nil
		},
		GetUser: func(ctx context.Context, userID string) (string, error) {
			var resp leapmuxv1.GetUserResponse
			if err := hubCallUnary(ctx, c, "GetUser", "", &leapmuxv1.GetUserRequest{UserId: userID}, &resp); err != nil {
				return "", err
			}
			return resp.GetOrgId(), nil
		},
		GetWorkingDir: func(ctx context.Context, workerID string, tabType leapmuxv1.TabType, tabID string) (string, error) {
			switch tabType {
			case leapmuxv1.TabType_TAB_TYPE_AGENT:
				var resp leapmuxv1.ListAgentsResponse
				if err := callInnerRPCBest(ctx, c, workerID, "ListAgents", &leapmuxv1.ListAgentsRequest{TabIds: []string{tabID}}, &resp); err != nil {
					return "", err
				}
				for _, a := range resp.GetAgents() {
					if a.GetId() == tabID {
						return a.GetWorkingDir(), nil
					}
				}
				return "", nil
			case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
				var resp leapmuxv1.ListTerminalsResponse
				if err := callInnerRPCBest(ctx, c, workerID, "ListTerminals", &leapmuxv1.ListTerminalsRequest{TabIds: []string{tabID}}, &resp); err != nil {
					return "", err
				}
				for _, t := range resp.GetTerminals() {
					if t.GetTerminalId() == tabID {
						return t.GetWorkingDir(), nil
					}
				}
				return "", nil
			default:
				return "", nil
			}
		},
	}
}

// runResolve is the canonical handler entry point: build a client,
// parse flags into the resolver's Inputs via BindEntityFlags, call
// resolve.Resolve, and surface errors through remote.EmitErrorWith.
// Each handler then has a populated Resolved struct to work from
// instead of hand-rolling its own resolution code path.
func runResolve(ctx context.Context, c *remote.Client, need resolve.Need, in resolve.Inputs) (resolve.Resolved, error) {
	got, err := resolve.Resolve(ctx, resolveDeps(c), need, in)
	if err != nil {
		var re *resolve.ResolveError
		if errors.As(err, &re) {
			return resolve.Resolved{}, remote.EmitError(re.Code, re.Message)
		}
		return resolve.Resolved{}, remote.EmitErrorWith("resolve_failed", err)
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
		in.WorkerID != "" || in.OrgID != "" || in.UserID != ""
}
