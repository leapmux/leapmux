package cmd

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
)

// TestResolveProvider_KnownAliasShortCircuits pins the happy path:
// when the user passes a recognised --provider value the helper maps
// to the enum without calling the worker. Without this, every
// `agent open --provider=...` invocation would pay an extra
// ListAvailableProviders RPC for no benefit.
func TestResolveProvider_KnownAliasShortCircuits(t *testing.T) {
	called := false
	list := func(context.Context, string, proto.Message, proto.Message) error {
		called = true
		return nil
	}
	got, err := resolveProvider(context.Background(), "claude-code", list)
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, got)
	assert.False(t, called, "known alias must skip the worker RPC")
}

// TestResolveProvider_UnknownAliasReturnsInvalidRequest pins the
// hard-fail path: a typo'd --provider must NOT silently fall back to
// Claude Code (the legacy behaviour) and must NOT trigger a worker
// query. The error envelope returns the `usage` code and lists every
// alias the parser accepts so the user can fix the typo without
// grepping the source.
func TestResolveProvider_UnknownAliasReturnsInvalidRequest(t *testing.T) {
	called := false
	list := func(context.Context, string, proto.Message, proto.Message) error {
		called = true
		return nil
	}
	_, err := resolveProvider(context.Background(), "not-a-provider", list)
	assert.False(t, called, "unknown alias must not fire the worker RPC")
	env := failureOf(t, err)
	assert.Equal(t, "usage", env.Error["code"])
	assert.Contains(t, env.Error["message"], "not-a-provider")
	// The hint must list the accepted values so the user can recover
	// without an extra round-trip.
	assert.Contains(t, env.Error["hints"], "claude-code")
	assert.Contains(t, env.Error["hints"], "codex")
}

// TestResolveProvider_EmptyAutoPicksWhenWorkerHasOne pins the silent
// success path: a single installed provider on the worker means
// `agent open` needs no --provider flag at all. Mirrors the frontend
// picker auto-selecting when only one option exists.
func TestResolveProvider_EmptyAutoPicksWhenWorkerHasOne(t *testing.T) {
	list := func(_ context.Context, method string, _ proto.Message, out proto.Message) error {
		assert.Equal(t, leapmuxv1connect.WorkerAgentServiceListAvailableProvidersProcedure, method)
		resp, ok := out.(*leapmuxv1.ListAvailableProvidersResponse)
		require.True(t, ok)
		resp.Providers = []leapmuxv1.AgentProvider{leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX}
		return nil
	}
	got, err := resolveProvider(context.Background(), "", list)
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, got)
}

// TestResolveProvider_EmptyAmbiguousReturnsTypedError pins the disambig
// path: when the worker reports more than one installed provider the
// CLI must NOT pick one arbitrarily. The envelope's `code` is `usage`
// so scripts can branch without parsing the message, and the message
// itself lists the candidate display names.
func TestResolveProvider_EmptyAmbiguousReturnsTypedError(t *testing.T) {
	list := func(_ context.Context, _ string, _ proto.Message, out proto.Message) error {
		resp := out.(*leapmuxv1.ListAvailableProvidersResponse)
		resp.Providers = []leapmuxv1.AgentProvider{
			leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
			leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		}
		return nil
	}
	_, err := resolveProvider(context.Background(), "", list)
	env := failureOf(t, err)
	assert.Equal(t, "usage", env.Error["code"])
	assert.Contains(t, env.Error["message"], "Claude Code")
	assert.Contains(t, env.Error["message"], "Codex")
}

// TestResolveProvider_EmptyEmptyWorkerReturnsTypedError pins the
// no-providers-installed case. The worker has nothing to spawn from;
// the CLI must say so explicitly with `conflict` rather than letting
// `agent open` fail downstream with a generic worker-side error.
func TestResolveProvider_EmptyEmptyWorkerReturnsTypedError(t *testing.T) {
	list := func(_ context.Context, _ string, _ proto.Message, out proto.Message) error {
		resp := out.(*leapmuxv1.ListAvailableProvidersResponse)
		resp.Providers = nil
		return nil
	}
	_, err := resolveProvider(context.Background(), "", list)
	env := failureOf(t, err)
	assert.Equal(t, "conflict", env.Error["code"])
}

// TestResolveProvider_EmptyPropagatesCodedRPCError pins that an RPC
// failure during ListAvailableProviders maps `channel_open_failed` to
// `worker_offline` and keeps the cause message, so network and
// authorization failures stay distinct in scripts.
func TestResolveProvider_EmptyPropagatesCodedRPCError(t *testing.T) {
	list := func(context.Context, string, proto.Message, proto.Message) error {
		return &codedRPCError{Code: "channel_open_failed", Cause: errors.New("noise handshake failed")}
	}
	_, err := resolveProvider(context.Background(), "", list)
	env := failureOf(t, err)
	assert.Equal(t, "worker_offline", env.Error["code"])
	assert.Contains(t, env.Error["message"], "noise handshake failed")
}

// A command that acts on an agent requires the agent: a bare one must never
// act on the agent that runs it. The refusal is a usage error, before any
// transport exists.
func TestAgentInterrupt_RequiresTheAgent(t *testing.T) {
	clearRemoteEnv(t)
	o := runControl(t, "agent", "interrupt", "--hub", "https://stub.invalid")
	f := o.requireFailure(t, "usage")
	assert.Contains(t, f.Message, "<agent>")
}

// `agent send` refuses an empty message, so a script cannot send an empty
// turn by accident.
func TestAgentSend_RequiresTheText(t *testing.T) {
	clearRemoteEnv(t)
	for name, args := range map[string][]string{
		"no text":    {"agent", "send", fullTestID("ag-1")},
		"empty text": {"agent", "send", fullTestID("ag-1"), "   "},
	} {
		t.Run(name, func(t *testing.T) {
			o := runControl(t, append(args, "--hub", "https://stub.invalid")...)
			o.requireFailure(t, "usage")
		})
	}
}

// The text is an argument or --file, never both.
func TestAgentSend_RefusesTextAndFileTogether(t *testing.T) {
	clearRemoteEnv(t)
	o := runControl(t, "agent", "send", fullTestID("ag-1"), "hi", "--file", "prompt.md", "--hub", "https://stub.invalid")
	f := o.requireFailure(t, "usage")
	assert.Contains(t, f.Message, "not both")
}

// A word that starts with a dash is a flag, and an unknown flag fails: a
// misspelled flag never reaches the agent as text.
func TestAgentSend_AnUnknownFlagNeverBecomesText(t *testing.T) {
	clearRemoteEnv(t)
	o := runControl(t, "agent", "send", fullTestID("ag-1"), "--wiat", "--hub", "https://stub.invalid")
	f := o.requireFailure(t, "usage")
	assert.Contains(t, f.Message, "--wiat")
}

// `tab rename` requires the title.
func TestTabRename_RequiresTheTitle(t *testing.T) {
	clearRemoteEnv(t)
	o := runControl(t, "tab", "rename", "self", "--hub", "https://stub.invalid")
	f := o.requireFailure(t, "usage")
	assert.Contains(t, f.Message, "<title>")
}

type interruptBarrierDispatcher struct {
	mu          sync.Mutex
	interrupted []string
	arrived     atomic.Int32
	bothArrived chan struct{}
	closeOnce   sync.Once
}

func (d *interruptBarrierDispatcher) DispatchWith(ctx context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
	if req.GetMethod() != leapmuxv1connect.WorkerAgentServiceInterruptAgentProcedure {
		_ = rw.SendError(int32(codes.Unimplemented), "unexpected method: "+req.GetMethod())
		return
	}
	var in leapmuxv1.InterruptAgentRequest
	_ = proto.Unmarshal(req.GetPayload(), &in)
	d.mu.Lock()
	d.interrupted = append(d.interrupted, in.GetAgentId())
	d.mu.Unlock()
	if d.bothArrived != nil {
		if d.arrived.Add(1) >= 2 {
			d.closeOnce.Do(func() { close(d.bothArrived) })
		}
		select {
		case <-d.bothArrived:
		case <-ctx.Done():
			_ = rw.SendError(int32(codes.DeadlineExceeded), "interrupt workers did not run concurrently")
			return
		}
	}
	writeProtoResponse(rw, &leapmuxv1.InterruptAgentResponse{})
}

func TestRunAgentInterrupt_PreflightsAllWorkersBeforeInterruptingAny(t *testing.T) {
	disp := &interruptBarrierDispatcher{}
	hub := &recordingHub{
		workers: workerRows("worker-A"),
		tabs: []*leapmuxv1.WorkspaceTab{
			{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "ag-aaaa1111"}, WorkerId: "worker-A", WorkspaceId: "ws-1"},
			{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "ag-bbbb2222"}, WorkerId: "worker-missing", WorkspaceId: "ws-1"},
		},
	}
	startSpawnIPC(t, hub, disp)

	f := runControl(t, "agent", "interrupt", "ag-aaaa1111", "ag-bbbb2222").requireFailure(t, cli.CodeNotFound)
	assert.Contains(t, f.Message, "no such worker: worker-missing")
	disp.mu.Lock()
	defer disp.mu.Unlock()
	assert.Empty(t, disp.interrupted, "preflight must fail before interrupting earlier workers")
}

func TestRunAgentInterrupt_FansOutAcrossWorkersConcurrentlyInDeterministicOrder(t *testing.T) {
	disp := &interruptBarrierDispatcher{bothArrived: make(chan struct{})}
	hub := &recordingHub{
		workers: workerRows("worker-A", "worker-B"),
		tabs: []*leapmuxv1.WorkspaceTab{
			{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "ag-aaaa1111"}, WorkerId: "worker-A", WorkspaceId: "ws-1"},
			{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "ag-bbbb2222"}, WorkerId: "worker-B", WorkspaceId: "ws-1"},
		},
	}
	h := startHubChannelHarness(t, hub, disp)
	h.dispatcher.Handle(leapmuxv1connect.WorkerAgentServiceInterruptAgentProcedure, channel.Registration{}, disp.DispatchWith)

	data := runControl(t, "agent", "interrupt", "ag-aaaa1111", "ag-bbbb2222", "--hub", h.url).Data(t)
	agents, ok := data["agents"].([]any)
	require.True(t, ok)
	require.Len(t, agents, 2)
	assert.Equal(t, "ag-aaaa1111", agents[0].(map[string]any)["id"])
	assert.Equal(t, "ag-bbbb2222", agents[1].(map[string]any)["id"])
	assert.Equal(t, 2, h.channelOpens())
}

type sendDeliveryDispatcher struct {
	mu      sync.Mutex
	methods []string
}

func (d *sendDeliveryDispatcher) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
	d.mu.Lock()
	d.methods = append(d.methods, req.GetMethod())
	d.mu.Unlock()
	switch req.GetMethod() {
	case leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure:
		writeProtoResponse(rw, &leapmuxv1.EnqueueAgentInputResponse{})
	case leapmuxv1connect.WorkerAgentInputServiceSteerQueuedAgentInputProcedure:
		writeProtoResponse(rw, &leapmuxv1.SteerQueuedAgentInputResponse{})
	case leapmuxv1connect.WorkerAgentInputServicePreemptQueuedAgentInputProcedure:
		writeProtoResponse(rw, &leapmuxv1.PreemptQueuedAgentInputResponse{})
	default:
		_ = rw.SendError(int32(codes.Unimplemented), "unexpected method: "+req.GetMethod())
	}
}

func TestRunAgentSend_SharesOneChannelForSteerAndPreempt(t *testing.T) {
	agID := fullTestID("ag-send-delivery")
	for _, tc := range []struct {
		flag   string
		method string
	}{
		{flag: "--steer", method: leapmuxv1connect.WorkerAgentInputServiceSteerQueuedAgentInputProcedure},
		{flag: "--preempt", method: leapmuxv1connect.WorkerAgentInputServicePreemptQueuedAgentInputProcedure},
	} {
		t.Run(tc.flag, func(t *testing.T) {
			disp := &sendDeliveryDispatcher{}
			hub := &recordingHub{
				workers: workerRows("worker-A"),
				locateTab: &leapmuxv1.WorkspaceTab{
					Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: agID},
					TileId:      "root-1",
					WorkerId:    "worker-A",
					WorkspaceId: "ws-1",
				},
			}
			h := startHubChannelHarness(t, hub, disp)
			h.dispatcher.Handle(leapmuxv1connect.WorkerAgentInputServiceSteerQueuedAgentInputProcedure, channel.Registration{}, disp.DispatchWith)
			h.dispatcher.Handle(leapmuxv1connect.WorkerAgentInputServicePreemptQueuedAgentInputProcedure, channel.Registration{}, disp.DispatchWith)

			data := runControl(t, "agent", "send", agID, "urgent message", tc.flag, "--hub", h.url).Data(t)
			assert.Equal(t, agID, data["id"])
			assert.Equal(t, 1, h.channelOpens(), "EnqueueAgentInput and %s must share one E2EE channel", tc.flag)
			assert.Equal(t, []string{
				leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure,
				tc.method,
			}, disp.methods)
		})
	}
}

func TestRunAgentList_ParentFilterReusesSingleFetch(t *testing.T) {
	parentID := "11111111-1111-1111-1111-111111111111"
	childID := "22222222-2222-2222-2222-222222222222"
	hub := &recordingHub{
		tabs: []*leapmuxv1.WorkspaceTab{
			{Tab: &leapmuxv1.TabIdent{TabId: parentID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}, WorkerId: "worker-A", WorkspaceId: "ws-1"},
			{Tab: &leapmuxv1.TabIdent{TabId: childID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}, WorkerId: "worker-A", WorkspaceId: "ws-1"},
		},
	}
	disp := &tabMetadataDispatcher{
		agents: []*leapmuxv1.AgentInfo{
			{Id: parentID, Title: "Lead Agent", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING},
			{Id: childID, Title: "Worker Child", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE, ParentAgentId: parentID},
		},
	}
	startSpawnIPC(t, hub, disp)

	data := runControl(t, "agent", "list", "--parent", "Lead Agent").Data(t)
	agents, ok := data["agents"].([]any)
	require.True(t, ok)
	require.Len(t, agents, 1)
	assert.Equal(t, childID, agents[0].(map[string]any)["id"])

	listTabsCalls := 0
	for _, proc := range hub.called() {
		if proc == leapmuxv1connect.WorkspaceServiceListTabsProcedure {
			listTabsCalls++
		}
	}
	assert.Equal(t, 1, listTabsCalls, "agent list --parent must call ListTabs once")
	disp.mu.Lock()
	assert.Len(t, disp.agentRequests, 1, "agent list --parent must fetch agent details once")
	disp.mu.Unlock()
}

func TestRunAgentList_RejectsBlankParentFlag(t *testing.T) {
	clearRemoteEnv(t)
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
	f := runControl(t, "agent", "list", "--parent", "  ", "--hub", "https://stub.invalid").requireFailure(t, cli.CodeUsage)
	assert.Contains(t, f.Message, "--parent is empty")
}

func TestRunAgentList_StatusColumnVisibilityAndStability(t *testing.T) {
	activeID := "11111111-1111-1111-1111-111111111111"
	inactiveID := "22222222-2222-2222-2222-222222222222"

	setupAllActive := func(t *testing.T) {
		t.Helper()
		hub := &recordingHub{
			tabs: []*leapmuxv1.WorkspaceTab{
				{Tab: &leapmuxv1.TabIdent{TabId: activeID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}, WorkerId: "worker-A", WorkspaceId: "ws-1"},
			},
		}
		disp := &tabMetadataDispatcher{
			agents: []*leapmuxv1.AgentInfo{
				{
					Id:            activeID,
					Title:         "Alpha",
					AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
					ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE,
					Status:        leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE,
				},
			},
		}
		startSpawnIPC(t, hub, disp)
	}

	setupWithInactive := func(t *testing.T) {
		t.Helper()
		hub := &recordingHub{
			tabs: []*leapmuxv1.WorkspaceTab{
				{Tab: &leapmuxv1.TabIdent{TabId: activeID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}, WorkerId: "worker-A", WorkspaceId: "ws-1"},
				{Tab: &leapmuxv1.TabIdent{TabId: inactiveID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}, WorkerId: "worker-A", WorkspaceId: "ws-1"},
			},
		}
		disp := &tabMetadataDispatcher{
			agents: []*leapmuxv1.AgentInfo{
				{
					Id:            activeID,
					Title:         "Alpha",
					AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
					ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE,
					Status:        leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE,
				},
				{
					Id:            inactiveID,
					Title:         "Beta",
					AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
					ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE,
					Status:        leapmuxv1.AgentStatus_AGENT_STATUS_INACTIVE,
				},
			},
		}
		startSpawnIPC(t, hub, disp)
	}

	t.Run("default text table omits STATUS when all active and shows it with --all or inactive agent", func(t *testing.T) {
		setupAllActive(t)

		plain := runControlText(t, "agent", "list")
		require.Equal(t, cli.ExitOK, plain.Code, plain.Stderr)
		plainHeader := strings.Fields(strings.Split(strings.TrimRight(plain.Stdout, "\n"), "\n")[0])
		assert.NotContains(t, plainHeader, "STATUS")

		withAll := runControlText(t, "agent", "list", "--all")
		require.Equal(t, cli.ExitOK, withAll.Code, withAll.Stderr)
		allHeader := strings.Fields(strings.Split(strings.TrimRight(withAll.Stdout, "\n"), "\n")[0])
		assert.Contains(t, allHeader, "STATUS")

		setupWithInactive(t)
		mixed := runControlText(t, "agent", "list")
		require.Equal(t, cli.ExitOK, mixed.Code, mixed.Stderr)
		mixedHeader := strings.Fields(strings.Split(strings.TrimRight(mixed.Stdout, "\n"), "\n")[0])
		assert.Contains(t, mixedHeader, "STATUS")
		assert.Contains(t, mixed.Stdout, "inactive")
	})

	t.Run("format tsv keeps stable columns including STATUS regardless of agent statuses", func(t *testing.T) {
		setupAllActive(t)
		activeTSV := runControlText(t, "agent", "list", "--format", "tsv")
		require.Equal(t, cli.ExitOK, activeTSV.Code, activeTSV.Stderr)
		activeLines := strings.Split(strings.TrimRight(activeTSV.Stdout, "\n"), "\n")
		require.Len(t, activeLines, 2)
		activeCols := strings.Split(activeLines[0], "\t")
		assert.Contains(t, activeCols, "STATUS")

		setupWithInactive(t)
		mixedTSV := runControlText(t, "agent", "list", "--format", "tsv")
		require.Equal(t, cli.ExitOK, mixedTSV.Code, mixedTSV.Stderr)
		mixedLines := strings.Split(strings.TrimRight(mixedTSV.Stdout, "\n"), "\n")
		require.Len(t, mixedLines, 3)
		mixedCols := strings.Split(mixedLines[0], "\t")
		assert.Equal(t, activeCols, mixedCols, "TSV header columns must stay identical whether all agents are active or not")
	})

	t.Run("explicit --fields id,status retains state ANSI styling even when all active", func(t *testing.T) {
		setupAllActive(t)
		out := runControlText(t, "agent", "list", "--fields", "id,status", "--color", "always")
		require.Equal(t, cli.ExitOK, out.Code, out.Stderr)
		lines := strings.Split(strings.TrimRight(out.Stdout, "\n"), "\n")
		require.Len(t, lines, 2)
		assert.Equal(t, []string{"ID", "STATUS"}, strings.Fields(cli.SanitizeTerminalText(lines[0])))
		assert.Contains(t, lines[1], "\x1b[32mactive\x1b[0m", "status column must retain ,state green ANSI styling")
	})

	t.Run("empty result hides STATUS by default, shows it with --all, and keeps STATUS in tsv", func(t *testing.T) {
		startSpawnIPC(t, &recordingHub{tabs: []*leapmuxv1.WorkspaceTab{}}, &tabMetadataDispatcher{})

		plain := runControlText(t, "agent", "list")
		require.Equal(t, cli.ExitOK, plain.Code, plain.Stderr)
		plainHeader := strings.Fields(strings.Split(strings.TrimRight(plain.Stdout, "\n"), "\n")[0])
		assert.NotContains(t, plainHeader, "STATUS")

		withAll := runControlText(t, "agent", "list", "--all")
		require.Equal(t, cli.ExitOK, withAll.Code, withAll.Stderr)
		allHeader := strings.Fields(strings.Split(strings.TrimRight(withAll.Stdout, "\n"), "\n")[0])
		assert.Contains(t, allHeader, "STATUS")

		tsv := runControlText(t, "agent", "list", "--format", "tsv")
		require.Equal(t, cli.ExitOK, tsv.Code, tsv.Stderr)
		tsvCols := strings.Split(strings.TrimRight(tsv.Stdout, "\n"), "\t")
		assert.Contains(t, tsvCols, "STATUS")

		jsonData := runControl(t, "agent", "list").Data(t)
		assert.Equal(t, []any{}, jsonData["agents"], "empty list must serialize as an empty array, not null")
	})
}

type interruptTestDispatcher struct {
	mu       sync.Mutex
	agents   []*leapmuxv1.AgentInfo
	requests []*leapmuxv1.InterruptAgentRequest
	failByID map[string]codes.Code
}

func (d *interruptTestDispatcher) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
	switch req.GetMethod() {
	case leapmuxv1connect.WorkerAgentServiceListAgentsProcedure:
		writeProtoResponse(rw, &leapmuxv1.ListAgentsResponse{Agents: d.agents})
	case leapmuxv1connect.WorkerAgentServiceInterruptAgentProcedure:
		var in leapmuxv1.InterruptAgentRequest
		_ = proto.Unmarshal(req.GetPayload(), &in)
		d.mu.Lock()
		d.requests = append(d.requests, &in)
		code, shouldFail := d.failByID[in.GetAgentId()]
		d.mu.Unlock()
		if shouldFail {
			_ = rw.SendError(int32(code), "worker refused interrupt for "+in.GetAgentId())
			return
		}
		writeProtoResponse(rw, &leapmuxv1.InterruptAgentResponse{})
	default:
		_ = rw.SendError(int32(codes.Unimplemented), "unexpected method: "+req.GetMethod())
	}
}

func (d *interruptTestDispatcher) recordedRequests() []*leapmuxv1.InterruptAgentRequest {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]*leapmuxv1.InterruptAgentRequest(nil), d.requests...)
}

func TestRunAgentInterrupt_ChildrenDeduplicationAndWorkerFailure(t *testing.T) {
	t.Run("expands direct children, deduplicates, and forwards reason", func(t *testing.T) {
		parentID := "int-parent1"
		child1 := "int-child01"
		child2 := "int-child02"
		grandchild := "int-grand01"
		disp := &interruptTestDispatcher{
			agents: []*leapmuxv1.AgentInfo{
				{Id: parentID},
				{Id: child1, ParentAgentId: parentID},
				{Id: child2, ParentAgentId: parentID},
				{Id: grandchild, ParentAgentId: child1},
			},
		}
		hub := &recordingHub{
			workers: workerRows("worker-A"),
			tabs: []*leapmuxv1.WorkspaceTab{
				{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: parentID}, WorkerId: "worker-A", WorkspaceId: "ws-1"},
				{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: child1}, WorkerId: "worker-A", WorkspaceId: "ws-1"},
				{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: child2}, WorkerId: "worker-A", WorkspaceId: "ws-1"},
				{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: grandchild}, WorkerId: "worker-A", WorkspaceId: "ws-1"},
			},
		}
		startSpawnIPC(t, hub, disp)
		t.Setenv(controlenv.TabIDVariable, parentID)
		t.Setenv(controlenv.TabTypeVariable, "agent")
		t.Setenv(controlenv.WorkerIDVariable, "worker-A")

		data := runControl(t, "agent", "interrupt", child1, "children", "--reason", "operator pause").Data(t)
		agents, ok := data["agents"].([]any)
		require.True(t, ok)
		require.Len(t, agents, 2)
		assert.Equal(t, child1, agents[0].(map[string]any)["id"])
		assert.Equal(t, child2, agents[1].(map[string]any)["id"])

		reqs := disp.recordedRequests()
		require.Len(t, reqs, 2)
		assert.Equal(t, child1, reqs[0].GetAgentId())
		assert.Equal(t, "operator pause", reqs[0].GetReason())
		assert.Equal(t, child2, reqs[1].GetAgentId())
		assert.Equal(t, "operator pause", reqs[1].GetReason())
	})

	t.Run("propagates error when one worker fails during fanout", func(t *testing.T) {
		disp := &interruptTestDispatcher{
			failByID: map[string]codes.Code{
				"ag-bbbb2222": codes.Unavailable,
			},
		}
		hub := &recordingHub{
			workers: workerRows("worker-A", "worker-B"),
			tabs: []*leapmuxv1.WorkspaceTab{
				{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "ag-aaaa1111"}, WorkerId: "worker-A", WorkspaceId: "ws-1"},
				{Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "ag-bbbb2222"}, WorkerId: "worker-B", WorkspaceId: "ws-1"},
			},
		}
		h := startHubChannelHarness(t, hub, disp)
		h.dispatcher.Handle(leapmuxv1connect.WorkerAgentServiceInterruptAgentProcedure, channel.Registration{}, disp.DispatchWith)

		f := runControl(t, "agent", "interrupt", "ag-aaaa1111", "ag-bbbb2222", "--hub", h.url).requireFailure(t, cli.CodeUnavailable)
		assert.Contains(t, f.Message, "worker refused interrupt for ag-bbbb2222")
	})
}

func TestRunAgentList_ParentFilterCrossWorkspaceFallback(t *testing.T) {
	wsParent := fullTestID("ws-parent")
	wsChild := fullTestID("ws-child")
	parentID := fullTestID("ag-parent")
	childID := fullTestID("ag-child")
	otherID := fullTestID("ag-other")

	allTabs := []*leapmuxv1.WorkspaceTab{
		{Tab: &leapmuxv1.TabIdent{TabId: parentID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}, WorkerId: "worker-A", WorkspaceId: wsParent},
		{Tab: &leapmuxv1.TabIdent{TabId: childID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}, WorkerId: "worker-A", WorkspaceId: wsChild},
		{Tab: &leapmuxv1.TabIdent{TabId: otherID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}, WorkerId: "worker-A", WorkspaceId: wsChild},
	}
	allAgents := map[string]*leapmuxv1.AgentInfo{
		parentID: {Id: parentID, Title: "Lead Agent", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING},
		childID:  {Id: childID, Title: "Child Agent", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE, ParentAgentId: parentID},
		otherID:  {Id: otherID, Title: "Unrelated Agent", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE},
	}

	startPhase3BIPC(t,
		func(method string, body []byte) (proto.Message, error) {
			switch method {
			case leapmuxv1connect.WorkspaceServiceListWorkspacesProcedure:
				return &leapmuxv1.ListWorkspacesResponse{
					Workspaces: []*leapmuxv1.Workspace{
						{Id: wsParent, Title: "Parent WS"},
						{Id: wsChild, Title: "Child WS"},
					},
				}, nil
			case leapmuxv1connect.WorkspaceServiceGetWorkspaceProcedure:
				var req leapmuxv1.GetWorkspaceRequest
				require.NoError(t, proto.Unmarshal(body, &req))
				return &leapmuxv1.GetWorkspaceResponse{Workspace: &leapmuxv1.Workspace{Id: req.GetWorkspaceId()}}, nil
			case leapmuxv1connect.WorkspaceServiceListTabsProcedure:
				var req leapmuxv1.ListTabsRequest
				require.NoError(t, proto.Unmarshal(body, &req))
				if len(req.GetWorkspaceIds()) == 0 {
					return &leapmuxv1.ListTabsResponse{Tabs: allTabs}, nil
				}
				wantWS := make(map[string]bool, len(req.GetWorkspaceIds()))
				for _, id := range req.GetWorkspaceIds() {
					wantWS[id] = true
				}
				var filtered []*leapmuxv1.WorkspaceTab
				for _, tab := range allTabs {
					if wantWS[tab.GetWorkspaceId()] {
						filtered = append(filtered, tab)
					}
				}
				return &leapmuxv1.ListTabsResponse{Tabs: filtered}, nil
			case leapmuxv1connect.WorkspaceServiceLocateTabProcedure:
				var req leapmuxv1.LocateTabRequest
				require.NoError(t, proto.Unmarshal(body, &req))
				for _, tab := range allTabs {
					if tab.GetTab().GetTabId() == req.GetTabId() {
						return &leapmuxv1.LocateTabResponse{Tab: tab}, nil
					}
				}
				return nil, connect.NewError(connect.CodeNotFound, "tab not found")
			default:
				return nil, connect.NewError(connect.CodeUnimplemented, "unexpected hub method: "+method)
			}
		},
		func(method string, body []byte) (proto.Message, error) {
			switch method {
			case leapmuxv1connect.WorkerAgentServiceListAgentsProcedure:
				var req leapmuxv1.ListAgentsRequest
				require.NoError(t, proto.Unmarshal(body, &req))
				var out []*leapmuxv1.AgentInfo
				for _, id := range req.GetTabIds() {
					if info, ok := allAgents[id]; ok {
						out = append(out, info)
					}
				}
				return &leapmuxv1.ListAgentsResponse{Agents: out}, nil
			case leapmuxv1connect.WorkerSystemServiceGetWorkerSystemInfoProcedure:
				return &leapmuxv1.GetWorkerSystemInfoResponse{Name: "box"}, nil
			default:
				return nil, connect.NewError(connect.CodeUnimplemented, "unexpected worker method: "+method)
			}
		},
	)

	// Parent lives in wsParent while listing is scoped to wsChild; fallback must resolve parent across workspaces.
	data := runControl(t, "agent", "list", "--workspace", wsChild, "--parent", "Lead Agent").Data(t)
	agents, ok := data["agents"].([]any)
	require.True(t, ok)
	require.Len(t, agents, 1)
	assert.Equal(t, childID, agents[0].(map[string]any)["id"])

	// When the parent exists in no workspace, the fallback propagates not_found.
	runControl(t, "agent", "list", "--workspace", wsChild, "--parent", "Missing Parent").requireFailure(t, cli.CodeNotFound)
}

func TestAgentAnswerCommand_RegistersTextFlag(t *testing.T) {
	cmd, _, err := newAgentCommand().Find([]string{"answer"})
	require.NoError(t, err)
	textFlag := cmd.Flags().Lookup("text")
	require.NotNil(t, textFlag, "agent answer must register --text")
	assert.Equal(t, "stringArray", textFlag.Value.Type())
	assert.Contains(t, cmd.Long, "--text")
}
