package cmd

import (
	"context"
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"
	"sync"

	"connectrpc.com/connect/v2"
	"golang.org/x/sync/errgroup"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

// resolveDeps wires the universal ID resolver to the actual hub /
// worker RPC calls a `leapmux` handler can make. Every
// derivation goes through hubCallUnary so the same Deps work for
// both hub-bound (laptop CLI) and local-IPC (worker-spawned)
// transports — the worker-side ControlIPC router proxies hub.* calls
// to the hub on our behalf, keeping the wire-level transport
// invisible to the resolver.
func resolveDeps(c control.Peer) resolve.Deps {
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
				if code := connect.CodeOf(err); code == connect.CodeNotFound || code == connect.CodePermissionDenied {
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
func targetListing(c control.Peer, deps resolve.Deps) resolve.Listing {
	var selfIdentity func(ctx context.Context) (resolve.SelfInfo, error)
	if ipcClient, ok := c.(*control.WorkerIPCClient); ok {
		selfIdentity = func(ctx context.Context) (resolve.SelfInfo, error) {
			ipc, err := ipcClient.ControlIPCService()
			if err != nil {
				return resolve.SelfInfo{}, err
			}
			identity, err := ipc.Whoami(ctx, &leapmuxv1.WhoamiRequest{})
			if err != nil {
				return resolve.SelfInfo{}, err
			}
			return resolve.SelfInfo{
				TabID:    identity.GetTabId(),
				TabType:  identity.GetTabType(),
				WorkerID: identity.GetWorkerId(),
			}, nil
		}
	}
	return resolve.Listing{
		SelfIdentity: selfIdentity,
		Tabs: func(ctx context.Context) ([]*leapmuxv1.WorkspaceTab, error) {
			return listTabsInWorkspaces(ctx, c, nil)
		},
		TabsByIDPrefix: func(ctx context.Context, idPrefix string) ([]*leapmuxv1.WorkspaceTab, error) {
			return listTabsInWorkspacesWithPrefix(ctx, c, nil, idPrefix)
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
			return listWorkspacesWithPrefix(ctx, c, "")
		},
		WorkspacesByIDPrefix: func(ctx context.Context, idPrefix string) ([]*leapmuxv1.Workspace, error) {
			return listWorkspacesWithPrefix(ctx, c, idPrefix)
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
		WorkersByIDPrefix: func(ctx context.Context, idPrefix string) ([]*leapmuxv1.Worker, error) {
			return listWorkersByIDPrefix(ctx, c, idPrefix)
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
		ParentAgent: func(ctx context.Context, workerID, selfAgentID string) (string, error) {
			var resp leapmuxv1.ListAgentsResponse
			if err := callInnerRPC(ctx, c, workerID, leapmuxv1connect.WorkerAgentServiceListAgentsProcedure, &leapmuxv1.ListAgentsRequest{TabIds: []string{selfAgentID}}, &resp); err != nil {
				return "", err
			}
			for _, a := range resp.GetAgents() {
				if a.GetId() == selfAgentID {
					return a.GetParentAgentId(), nil
				}
			}
			return "", nil
		},
	}
}

// listWorkspacesWithPrefix lists readable workspaces, optionally narrowed by a
// server-side id_prefix filter.
func listWorkspacesWithPrefix(ctx context.Context, c control.Peer, idPrefix string) ([]*leapmuxv1.Workspace, error) {
	var resp leapmuxv1.ListWorkspacesResponse
	if err := hubCallUnary(ctx, c, leapmuxv1connect.WorkspaceServiceListWorkspacesProcedure, &leapmuxv1.ListWorkspacesRequest{
		IdPrefix: idPrefix,
	}, &resp); err != nil {
		return nil, err
	}
	return resp.GetWorkspaces(), nil
}

// listTabsInWorkspaces lists visible tabs. Nil selects every readable workspace.
func listTabsInWorkspaces(ctx context.Context, c control.Peer, workspaceIDs []string) ([]*leapmuxv1.WorkspaceTab, error) {
	return listTabsInWorkspacesWithPrefix(ctx, c, workspaceIDs, "")
}

// listTabsInWorkspacesWithPrefix lists visible tabs, optionally narrowed by
// workspaceIDs and a server-side id_prefix filter.
func listTabsInWorkspacesWithPrefix(ctx context.Context, c control.Peer, workspaceIDs []string, idPrefix string) ([]*leapmuxv1.WorkspaceTab, error) {
	var resp leapmuxv1.ListTabsResponse
	if err := hubCallUnary(ctx, c, leapmuxv1connect.WorkspaceServiceListTabsProcedure, &leapmuxv1.ListTabsRequest{
		WorkspaceIds: workspaceIDs,
		IdPrefix:     idPrefix,
	}, &resp); err != nil {
		return nil, err
	}
	return resp.GetTabs(), nil
}

// listWorkersByIDPrefix fetches one page of up to prefixResolutionLimit Workers
// whose id starts with idPrefix. One page is sufficient to identify an exact
// match, a unique prefix match, or ambiguity.
func listWorkersByIDPrefix(ctx context.Context, c control.Peer, idPrefix string) ([]*leapmuxv1.Worker, error) {
	var resp leapmuxv1.ListWorkersResponse
	if err := hubCallUnary(ctx, c, leapmuxv1connect.WorkerManagementServiceListWorkersProcedure, &leapmuxv1.ListWorkersRequest{
		IdPrefix: idPrefix,
		Page:     &leapmuxv1.PageRequest{Limit: prefixResolutionLimit},
	}, &resp); err != nil {
		return nil, err
	}
	return resp.GetWorkers(), nil
}

// tabDetail is what a Worker reports about one of its tabs.
type tabDetail struct {
	Title string
	// State is the agent's activity state, or the terminal's status.
	State    string
	Agent    *leapmuxv1.AgentInfo
	Terminal *leapmuxv1.TerminalInfo
	Payload  *leapmuxv1.TabPayload
}

// workerTabDataOptions controls which optional Worker-side RPCs
// fetchWorkerTabData runs on each Worker's shared channel.
type workerTabDataOptions struct {
	IncludeDescendants       bool
	IncludeTerminalProcesses bool
	IncludeControlRequests   bool
	IncludeWorkerNames       bool
}

// workerTabData holds the Worker-side details gathered by fetchWorkerTabData.
type workerTabData struct {
	details     map[string]tabDetail
	workerNames map[string]string
	agentWorker map[string]string
	termProcs   map[string][]string
	pendingReqs map[string][]string
}

// detailFanoutLimit caps the Workers that one command asks at once.
const detailFanoutLimit = 8

// fetchTabDetails asks each Worker about its tabs, with independent Workers
// served concurrently. The hub's rows hold only the placement of a tab. Its
// title and its state live on the Worker. A Worker that cannot answer leaves
// its tabs out, so a list still prints the tabs of the Workers that can.
func fetchTabDetails(ctx context.Context, c control.Peer, tabs []*leapmuxv1.WorkspaceTab) map[string]tabDetail {
	return fetchWorkerTabData(ctx, c, tabs, workerTabDataOptions{}).details
}

// fetchTabDetailsAndWorkers opens ONE Noise_NK E2EE channel per Worker and
// fetches agent details, terminal details, viewer payloads (concurrently), and
// optionally the Worker's display name on that single channel.
func fetchTabDetailsAndWorkers(ctx context.Context, c control.Peer, tabs []*leapmuxv1.WorkspaceTab, includeWorkerNames bool) (map[string]tabDetail, map[string]string) {
	data := fetchWorkerTabData(ctx, c, tabs, workerTabDataOptions{IncludeWorkerNames: includeWorkerNames})
	return data.details, data.workerNames
}

// fetchWorkerTabData opens ONE Noise_NK E2EE channel per Worker and gathers
// agent details, terminal details, viewer payloads, and any requested optional
// data (descendants, terminal processes, pending control requests, and worker
// names) concurrently on that channel.
func fetchWorkerTabData(ctx context.Context, c control.Peer, tabs []*leapmuxv1.WorkspaceTab, opts workerTabDataOptions) workerTabData {
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
	res := workerTabData{
		details:     map[string]tabDetail{},
		workerNames: map[string]string{},
		agentWorker: map[string]string{},
		termProcs:   map[string][]string{},
		pendingReqs: map[string][]string{},
	}
	var mu sync.Mutex
	var g errgroup.Group
	g.SetLimit(detailFanoutLimit)
	for workerID, wt := range byWorker {
		if !opts.IncludeWorkerNames && len(wt.agents) == 0 && len(wt.terminals) == 0 && len(wt.viewers) == 0 {
			continue
		}
		g.Go(func() error {
			_ = withWorkerChannel(ctx, c, workerID, func(w workerCall) error {
				var inner errgroup.Group
				inner.SetLimit(detailFanoutLimit)
				if len(wt.agents) > 0 {
					inner.Go(func() error {
						var resp leapmuxv1.ListAgentsResponse
						if w.Call(ctx, leapmuxv1connect.WorkerAgentServiceListAgentsProcedure, &leapmuxv1.ListAgentsRequest{
							TabIds:             wt.agents,
							IncludeDescendants: opts.IncludeDescendants,
						}, &resp) == nil {
							var waitingAgents []string
							mu.Lock()
							for _, a := range resp.GetAgents() {
								res.details[a.GetId()] = tabDetail{Title: a.GetTitle(), State: enumToken(a.GetActivityState()), Agent: a}
								res.agentWorker[a.GetId()] = workerID
								if opts.IncludeControlRequests && a.GetActivityState() == leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER {
									waitingAgents = append(waitingAgents, a.GetId())
								}
							}
							mu.Unlock()
							// Launch control-request lookups concurrently on the same
							// channel as soon as ListAgents returns.
							for _, aid := range waitingAgents {
								inner.Go(func() error {
									var reqResp leapmuxv1.ListControlRequestsResponse
									if w.Call(ctx, leapmuxv1connect.WorkerAgentServiceListControlRequestsProcedure, &leapmuxv1.ListControlRequestsRequest{AgentId: aid}, &reqResp) == nil {
										var handles []string
										for _, cr := range reqResp.GetRequests() {
											if h := cr.GetClaimToken(); h != "" {
												handles = append(handles, cli.ShortID(h))
											}
										}
										if len(handles) > 0 {
											mu.Lock()
											res.pendingReqs[aid] = handles
											mu.Unlock()
										}
									}
									return nil
								})
							}
						}
						return nil
					})
				}
				if len(wt.terminals) > 0 {
					inner.Go(func() error {
						var resp leapmuxv1.ListTerminalsResponse
						if w.Call(ctx, leapmuxv1connect.WorkerTerminalServiceListTerminalsProcedure, &leapmuxv1.ListTerminalsRequest{TabIds: wt.terminals}, &resp) == nil {
							mu.Lock()
							for _, t := range resp.GetTerminals() {
								res.details[t.GetTerminalId()] = tabDetail{Title: t.GetTitle(), State: enumToken(t.GetStatus()), Terminal: t}
							}
							mu.Unlock()
						}
						return nil
					})
					if opts.IncludeTerminalProcesses {
						inner.Go(func() error {
							var resp leapmuxv1.InspectTerminalProcessesResponse
							if w.Call(ctx, leapmuxv1connect.WorkerTerminalServiceInspectTerminalProcessesProcedure, &leapmuxv1.InspectTerminalProcessesRequest{TerminalIds: wt.terminals}, &resp) == nil {
								mu.Lock()
								for _, tp := range resp.GetTerminals() {
									var names []string
									for _, proc := range tp.GetProcesses() {
										n := strings.TrimSpace(proc.GetName())
										if n == "" {
											n = fmt.Sprintf("pid:%d", proc.GetPid())
										}
										if !slices.Contains(names, n) {
											names = append(names, n)
										}
									}
									if len(names) > 0 {
										sort.Strings(names)
										res.termProcs[tp.GetTerminalId()] = names
									}
								}
								mu.Unlock()
							}
							return nil
						})
					}
				}
				for _, viewer := range wt.viewers {
					inner.Go(func() error {
						var resp leapmuxv1.GetTabPayloadResponse
						id := viewer.GetTabId()
						if w.Call(ctx, leapmuxv1connect.WorkerPrivateServiceGetTabPayloadProcedure, &leapmuxv1.GetTabPayloadRequest{TabId: id}, &resp) != nil {
							return nil
						}
						if title, known := viewerTabTitle(viewer.GetTabType(), resp.GetPayload()); known {
							mu.Lock()
							res.details[id] = tabDetail{Title: title, Payload: resp.GetPayload()}
							mu.Unlock()
						}
						return nil
					})
				}
				if opts.IncludeWorkerNames {
					inner.Go(func() error {
						var resp leapmuxv1.GetWorkerSystemInfoResponse
						if w.Call(ctx, leapmuxv1connect.WorkerSystemServiceGetWorkerSystemInfoProcedure, &leapmuxv1.GetWorkerSystemInfoRequest{}, &resp) == nil {
							mu.Lock()
							res.workerNames[workerID] = resp.GetName()
							mu.Unlock()
						}
						return nil
					})
				}
				_ = inner.Wait()
				return nil
			})
			return nil
		})
	}
	_ = g.Wait()
	return res
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
func fetchWorkerNames(ctx context.Context, c control.Peer, workerIDs []string) map[string]string {
	var mu sync.Mutex
	out := map[string]string{}
	var g errgroup.Group
	g.SetLimit(detailFanoutLimit)
	for _, workerID := range workerIDs {
		g.Go(func() error {
			var resp leapmuxv1.GetWorkerSystemInfoResponse
			if callInnerRPC(ctx, c, workerID, leapmuxv1connect.WorkerSystemServiceGetWorkerSystemInfoProcedure, &leapmuxv1.GetWorkerSystemInfoRequest{}, &resp) == nil {
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
