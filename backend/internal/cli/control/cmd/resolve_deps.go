package cmd

import (
	"context"
	"fmt"
	"path"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/hub/crdt"
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
		LocateTab: func(ctx context.Context, tabType leapmuxv1.TabType, tabID string) (*leapmuxv1.WorkspaceTab, error) {
			var resp leapmuxv1.LocateTabResponse
			if err := hubCallUnary(ctx, c, leapmuxv1connect.WorkspaceServiceLocateTabProcedure, &leapmuxv1.LocateTabRequest{TabType: tabType, TabId: tabID}, &resp); err != nil {
				// The one failure the resolver must tell apart from every
				// other: "the Hub has no placement for this id" rather than
				// "the Hub could not be asked". A quake terminal is the id
				// that reaches here -- it has no CRDT tab at all -- and a
				// command needing only the worker must still run. See
				// resolve.ErrTabNotLocatable.
				//
				// PermissionDenied comes along because the Hub deliberately
				// conflates it with NotFound (see classifyHubError): a caller
				// who may not see a tab learns nothing further by having the
				// command fail here rather than at the worker.
				if isNotFoundOrForbidden(err) {
					return nil, fmt.Errorf("%w: %w", resolve.ErrTabNotLocatable, err)
				}
				return nil, err
			}
			return resp.GetTab(), nil
		},
		GetWorkspace: func(ctx context.Context, workspaceID string) error {
			var resp leapmuxv1.GetWorkspaceResponse
			return hubCallUnary(ctx, c, leapmuxv1connect.WorkspaceServiceGetWorkspaceProcedure, &leapmuxv1.GetWorkspaceRequest{WorkspaceId: workspaceID}, &resp)
		},
		LocateTile: func(ctx context.Context, tileID string) (string, error) {
			var resp leapmuxv1.LocateTileResponse
			if err := hubCallUnary(ctx, c, leapmuxv1connect.WorkspaceServiceLocateTileProcedure, &leapmuxv1.LocateTileRequest{TileId: tileID}, &resp); err != nil {
				return "", err
			}
			return resp.GetWorkspaceId(), nil
		},
	}
}

// targetListing wires resolve.Canonical to the hub's list calls and to the
// Workers that know each tab's title and each Worker's name.
func targetListing(c *control.Client, deps resolve.Deps) resolve.Listing {
	return resolve.Listing{
		Tabs: func(ctx context.Context) ([]*leapmuxv1.WorkspaceTab, error) {
			return listAllTabs(ctx, c)
		},
		TabTitles: func(ctx context.Context, tabs []*leapmuxv1.WorkspaceTab) map[string]string {
			details := fetchTabDetails(ctx, c, tabs)
			titles := make(map[string]string, len(details))
			for id, d := range details {
				titles[id] = d.Title
			}
			return titles
		},
		Workspaces: func(ctx context.Context) ([]*leapmuxv1.Workspace, error) {
			var resp leapmuxv1.ListWorkspacesResponse
			if err := hubCallUnary(ctx, c, leapmuxv1connect.WorkspaceServiceListWorkspacesProcedure, &leapmuxv1.ListWorkspacesRequest{}, &resp); err != nil {
				return nil, err
			}
			return resp.GetWorkspaces(), nil
		},
		Tiles: func(ctx context.Context) (map[string]string, error) {
			state, err := fetchMaterialized(ctx, c, nil)
			if err != nil {
				return nil, err
			}
			out := make(map[string]string, len(state.GetNodes()))
			for id, node := range state.GetNodes() {
				if !crdt.HLCIsZero(node.GetTombstoneAt()) {
					continue
				}
				out[id] = kindLabel(node.GetKind().GetValue())
			}
			return out, nil
		},
		Workers: func(ctx context.Context) ([]*leapmuxv1.Worker, error) {
			return listAllWorkers(ctx, c)
		},
		WorkerNames: func(ctx context.Context, workers []*leapmuxv1.Worker) map[string]string {
			ids := make([]string, 0, len(workers))
			for _, w := range workers {
				if w.GetOnline() {
					ids = append(ids, w.GetId())
				}
			}
			return fetchWorkerNames(ctx, c, ids)
		},
		LocateTab: func(ctx context.Context, tabID string) (*leapmuxv1.WorkspaceTab, error) {
			return deps.LocateTab(ctx, leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED, tabID)
		},
	}
}

// listAllTabs lists every tab of every workspace that the caller can read.
func listAllTabs(ctx context.Context, c *control.Client) ([]*leapmuxv1.WorkspaceTab, error) {
	var resp leapmuxv1.ListTabsResponse
	if err := hubCallUnary(ctx, c, leapmuxv1connect.WorkspaceServiceListTabsProcedure, &leapmuxv1.ListTabsRequest{}, &resp); err != nil {
		return nil, err
	}
	return resp.GetTabs(), nil
}

// tabDetail is what a Worker reports about one of its tabs.
type tabDetail struct {
	Title string
	// State is the agent's activity state, or the terminal's status.
	State    string
	Agent    *leapmuxv1.AgentInfo
	Terminal *leapmuxv1.TerminalInfo
}

// detailFanoutLimit caps the Workers that one command asks at once.
const detailFanoutLimit = 8

// fetchTabDetails asks each Worker about its tabs, with independent Workers
// served concurrently. The hub's rows hold only the placement of a tab. Its
// title and its state live on the Worker. A Worker that cannot answer leaves
// its tabs out, so a list still prints the tabs of the Workers that can.
func fetchTabDetails(ctx context.Context, c *control.Client, tabs []*leapmuxv1.WorkspaceTab) map[string]tabDetail {
	type workerTabs struct {
		agents, terminals []string
		viewers           []*leapmuxv1.TabIdent
	}
	byWorker := map[string]*workerTabs{}
	for _, t := range tabs {
		workerID := t.GetWorkerId()
		if workerID == "" {
			continue
		}
		wt := byWorker[workerID]
		if wt == nil {
			wt = &workerTabs{}
			byWorker[workerID] = wt
		}
		switch t.GetTab().GetTabType() {
		case leapmuxv1.TabType_TAB_TYPE_AGENT:
			wt.agents = append(wt.agents, t.GetTab().GetTabId())
		case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
			wt.terminals = append(wt.terminals, t.GetTab().GetTabId())
		case leapmuxv1.TabType_TAB_TYPE_FILE, leapmuxv1.TabType_TAB_TYPE_IMAGE:
			wt.viewers = append(wt.viewers, t.GetTab())
		case leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED:
			// The Worker reports nothing for a tab of an unknown type.
		}
	}
	var mu sync.Mutex
	out := map[string]tabDetail{}
	var g errgroup.Group
	g.SetLimit(detailFanoutLimit)
	for workerID, wt := range byWorker {
		if len(wt.agents) == 0 && len(wt.terminals) == 0 && len(wt.viewers) == 0 {
			continue
		}
		g.Go(func() error {
			_ = withWorkerChannel(ctx, c, workerID, func(w workerCall) error {
				if len(wt.agents) > 0 {
					var resp leapmuxv1.ListAgentsResponse
					if w.Call(ctx, leapmuxv1connect.WorkerAgentServiceListAgentsProcedure, &leapmuxv1.ListAgentsRequest{TabIds: wt.agents}, &resp) == nil {
						mu.Lock()
						for _, a := range resp.GetAgents() {
							out[a.GetId()] = tabDetail{Title: a.GetTitle(), State: enumToken(a.GetActivityState()), Agent: a}
						}
						mu.Unlock()
					}
				}
				if len(wt.terminals) > 0 {
					var resp leapmuxv1.ListTerminalsResponse
					if w.Call(ctx, leapmuxv1connect.WorkerTerminalServiceListTerminalsProcedure, &leapmuxv1.ListTerminalsRequest{TabIds: wt.terminals}, &resp) == nil {
						mu.Lock()
						for _, t := range resp.GetTerminals() {
							out[t.GetTerminalId()] = tabDetail{Title: t.GetTitle(), State: enumToken(t.GetStatus()), Terminal: t}
						}
						mu.Unlock()
					}
				}
				for _, viewer := range wt.viewers {
					var resp leapmuxv1.GetTabPayloadResponse
					id := viewer.GetTabId()
					if w.Call(ctx, leapmuxv1connect.WorkerPrivateServiceGetTabPayloadProcedure, &leapmuxv1.GetTabPayloadRequest{TabId: id}, &resp) != nil {
						continue
					}
					if title, known := viewerTabTitle(viewer.GetTabType(), resp.GetPayload()); known {
						mu.Lock()
						out[id] = tabDetail{Title: title}
						mu.Unlock()
					}
				}
				return nil
			})
			return nil
		})
	}
	_ = g.Wait()
	return out
}

// viewerTabTitle derives labels from the same private payloads the browser
// uses. A missing or unexpected payload leaves the title unknown.
func viewerTabTitle(kind leapmuxv1.TabType, payload *leapmuxv1.TabPayload) (string, bool) {
	if file := payload.GetFile(); kind == leapmuxv1.TabType_TAB_TYPE_FILE && file != nil {
		if title := fileTabBasename(file.GetFilePath()); title != "" {
			return title, true
		}
		return "File", true
	}
	if image := payload.GetImage(); kind == leapmuxv1.TabType_TAB_TYPE_IMAGE && image != nil {
		if title := image.GetTitle(); title != "" {
			return title, true
		}
		return "Image", true
	}
	return "", false
}

// fileTabBasename splits a path as the Worker would. A backslash in a POSIX
// filename remains part of its title, even when the CLI runs on Windows.
func fileTabBasename(name string) string {
	if name == "" {
		return ""
	}
	driveLetter := (name[0] >= 'A' && name[0] <= 'Z') || (name[0] >= 'a' && name[0] <= 'z')
	if strings.HasPrefix(name, "\\") || (driveLetter && len(name) > 1 && name[1] == ':') {
		name = strings.TrimRight(name, `/\`)
		return name[strings.LastIndexAny(name, `/\`)+1:]
	}
	if base := path.Base(name); base != "/" {
		return base
	}
	return ""
}

// fetchWorkerNames asks each Worker for its name, all Workers at once. The
// hub does not store a Worker's name; the Worker reports it. A Worker that
// cannot answer has no entry.
func fetchWorkerNames(ctx context.Context, c *control.Client, workerIDs []string) map[string]string {
	var mu sync.Mutex
	out := map[string]string{}
	var g errgroup.Group
	g.SetLimit(detailFanoutLimit)
	for _, workerID := range workerIDs {
		g.Go(func() error {
			var resp leapmuxv1.GetWorkerSystemInfoResponse
			if callInnerRPCBest(ctx, c, workerID, leapmuxv1connect.WorkerSystemServiceGetWorkerSystemInfoProcedure, &leapmuxv1.GetWorkerSystemInfoRequest{}, &resp) == nil {
				mu.Lock()
				out[workerID] = resp.GetName()
				mu.Unlock()
			}
			return nil
		})
	}
	_ = g.Wait()
	return out
}
