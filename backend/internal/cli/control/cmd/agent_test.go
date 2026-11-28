package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
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
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
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

// launchWaitDispatcher answers OpenAgent, WatchEvents, ListAgents,
// EnqueueAgentInput, ListAgentInputQueue, and ListAgentMessages so `agent open`
// and `agent fork` with `--message ... --wait` run end to end over IPC.
type launchWaitDispatcher struct {
	mu      sync.Mutex
	agents  map[string]*leapmuxv1.AgentInfo
	reply   string
	wsID    string
	tileID  string
	lastReq *leapmuxv1.OpenAgentRequest
}

func (d *launchWaitDispatcher) DispatchWith(ctx context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
	switch req.GetMethod() {
	case leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure:
		var watchReq leapmuxv1.WatchEventsRequest
		_ = proto.Unmarshal(req.GetPayload(), &watchReq)
		ack, _ := proto.Marshal(&leapmuxv1.WatchEventsResponse{
			Event: &leapmuxv1.WatchEventsResponse_UpdateAck{
				UpdateAck: &leapmuxv1.WatchUpdateAck{UpdateId: watchReq.GetUpdateId()},
			},
		})
		_ = rw.SendStream(&leapmuxv1.InnerStreamMessage{Payload: ack})
		<-ctx.Done()
	case leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure:
		var in leapmuxv1.OpenAgentRequest
		_ = proto.Unmarshal(req.GetPayload(), &in)
		tabID := in.GetCreation().GetTabId()
		info := &leapmuxv1.AgentInfo{
			Id:                     tabID,
			WorkerId:               "worker-A",
			Title:                  in.GetTitle(),
			AgentProvider:          in.GetAgentProvider(),
			ActivityState:          leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE,
			PublishedActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE,
		}
		d.mu.Lock()
		d.lastReq = &in
		if d.agents == nil {
			d.agents = map[string]*leapmuxv1.AgentInfo{}
		}
		d.agents[tabID] = info
		d.mu.Unlock()
		writeProtoResponse(rw, &leapmuxv1.OpenAgentResponse{
			Agent: info,
			Publication: &leapmuxv1.PublishedTab{
				Tab:         &leapmuxv1.TabIdent{TabId: tabID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
				WorkspaceId: d.wsID,
				TileId:      d.tileID,
				Position:    "a1",
			},
		})
	case leapmuxv1connect.WorkerAgentServiceListAgentsProcedure:
		var in leapmuxv1.ListAgentsRequest
		_ = proto.Unmarshal(req.GetPayload(), &in)
		d.mu.Lock()
		var out []*leapmuxv1.AgentInfo
		for _, id := range in.GetTabIds() {
			if a, ok := d.agents[id]; ok {
				out = append(out, a)
			}
		}
		d.mu.Unlock()
		writeProtoResponse(rw, &leapmuxv1.ListAgentsResponse{Agents: out})
	case leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure:
		writeProtoResponse(rw, &leapmuxv1.EnqueueAgentInputResponse{AcceptedSeq: 1})
	case leapmuxv1connect.WorkerAgentInputServiceListAgentInputQueueProcedure:
		writeProtoResponse(rw, &leapmuxv1.ListAgentInputQueueResponse{
			Snapshot: &leapmuxv1.AgentInputQueueSnapshot{},
			InputReply: &leapmuxv1.AgentInputTurnReply{
				AcceptedSeq: 1,
				Outcome:     leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
			},
		})
	case leapmuxv1connect.WorkerAgentServiceListAgentMessagesProcedure:
		replyText := d.reply
		writeProtoResponse(rw, &leapmuxv1.ListAgentMessagesResponse{
			Messages: []*leapmuxv1.AgentChatMessage{
				{
					Seq:       2,
					Id:        "reply-2",
					Source:    leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
					ReplyText: &replyText,
				},
			},
		})
	default:
		_ = rw.SendError(int32(codes.Unimplemented), "unexpected method: "+req.GetMethod())
	}
}

func TestAgentOpenAndFork_WaitQuietPrintsAgentIDAndTextEmitsNotice(t *testing.T) {
	const wsID = "ws-1"
	tileID := fullTestID("tl-wait")
	srcID := fullTestID("ag-srcw")

	newEnv := func(t *testing.T) *launchWaitDispatcher {
		t.Helper()
		srcTab := &leapmuxv1.WorkspaceTab{
			Tab:         &leapmuxv1.TabIdent{TabId: srcID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
			WorkspaceId: wsID,
			TileId:      tileID,
			WorkerId:    "worker-A",
		}
		hub := &recordingHub{
			workers:      workerRows("worker-A"),
			locateTab:    srcTab,
			tabs:         []*leapmuxv1.WorkspaceTab{srcTab},
			materialized: newStateBuilder().workspace(wsID, tileID).leafNode(tileID, "", "m").st,
		}
		disp := &launchWaitDispatcher{
			wsID:   wsID,
			tileID: tileID,
			reply:  "All green.",
			agents: map[string]*leapmuxv1.AgentInfo{
				srcID: {
					Id:                     srcID,
					Title:                  "Source",
					WorkerId:               "worker-A",
					WorkingDir:             "/repo",
					AgentProvider:          leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
					ActivityState:          leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE,
					PublishedActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE,
				},
			},
		}
		startSpawnIPC(t, hub, disp)
		return disp
	}

	t.Run("agent open --message --wait -q prints created agent ID", func(t *testing.T) {
		newEnv(t)
		const reqID = "open-wait-q"
		wantID := requestScopedID(reqID, requestScopeAgentOpen, "worker-A")
		inputID := requestScopedID(reqID, requestScopeAgentSend, wantID)

		o := runControlText(t, "agent", "open", "--provider", "claude", "--workspace", wsID, "--tile", tileID,
			"--worker", "worker-A", "--message", "Run tests", "--wait", "--request-id", reqID, "--no-focus", "-q")
		require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
		assert.Equal(t, wantID+"\n", o.Stdout)
		assert.NotContains(t, o.Stdout, inputID)
		assert.Empty(t, o.Stderr)
	})

	t.Run("agent open --message --wait text mode emits opened agent notice on stderr", func(t *testing.T) {
		newEnv(t)
		const reqID = "open-wait-text"
		wantID := requestScopedID(reqID, requestScopeAgentOpen, "worker-A")

		o := runControlText(t, "agent", "open", "--provider", "claude", "--workspace", wsID, "--tile", tileID,
			"--worker", "worker-A", "--message", "Run tests", "--wait", "--request-id", reqID, "--no-focus")
		require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
		assert.Equal(t, "All green.\n", o.Stdout)
		assert.Contains(t, o.Stderr, "opened agent "+cli.ShortID(wantID))
	})

	t.Run("agent fork --message --wait -q and text mode emit forked agent ID", func(t *testing.T) {
		newEnv(t)
		const reqID = "fork-wait-q"
		wantID := requestScopedID(reqID, requestScopeAgentFork, "worker-A")

		quiet := runControlText(t, "agent", "fork", srcID, "--message", "Check diff", "--wait", "--request-id", reqID, "--no-focus", "-q")
		require.Equal(t, cli.ExitOK, quiet.Code, quiet.Stderr)
		assert.Equal(t, wantID+"\n", quiet.Stdout)
		assert.Empty(t, quiet.Stderr)

		text := runControlText(t, "agent", "fork", srcID, "--message", "Check diff", "--wait", "--request-id", "fork-wait-text", "--no-focus")
		require.Equal(t, cli.ExitOK, text.Code, text.Stderr)
		assert.Equal(t, "All green.\n", text.Stdout)
		wantTextID := requestScopedID("fork-wait-text", requestScopeAgentFork, "worker-A")
		assert.Contains(t, text.Stderr, "forked agent "+cli.ShortID(wantTextID))
	})
}

func TestAgentOpen_PositionalPromptAndShortMessageFlag(t *testing.T) {
	state := newStateBuilder().workspace("ws-1", "root-1").leafNode("root-1", "", "m").st

	t.Run("positional prompt and -m populate InitialMessage", func(t *testing.T) {
		hub, disp := &recordingHub{workers: workerRows("worker-A"), materialized: state}, &creationPublicationDispatcher{}
		startSpawnIPC(t, hub, disp)

		_ = runControl(t, "agent", "open", "--provider", "claude", "--workspace", "ws-1", "--tile", "root-1",
			"--worker", "worker-A", "Review the diff").Data(t)
		_ = runControl(t, "agent", "open", "--provider", "claude", "--workspace", "ws-1", "--tile", "root-1",
			"--worker", "worker-A", "-m", "Run tests").Data(t)

		calls := disp.recorded()
		require.Len(t, calls, 2)
		assert.Equal(t, "Review the diff", calls[0].in.(*leapmuxv1.OpenAgentRequest).GetInitialMessage())
		assert.Equal(t, "Run tests", calls[1].in.(*leapmuxv1.OpenAgentRequest).GetInitialMessage())
	})

	t.Run("rejects blank positional prompt and conflicting prompt sources", func(t *testing.T) {
		hub, disp := &recordingHub{workers: workerRows("worker-A"), materialized: state}, &creationPublicationDispatcher{}
		startSpawnIPC(t, hub, disp)

		f := runControl(t, "agent", "open", "--provider", "claude", "   ").requireFailure(t, cli.CodeUsage)
		assert.Equal(t, "prompt is empty", f.Message)

		f = runControl(t, "agent", "open", "--provider", "claude", "Positional", "-m", "Flag").requireFailure(t, cli.CodeUsage)
		assert.Contains(t, f.Message, "not more than one")

		f = runControl(t, "agent", "open", "--provider", "claude", "Positional", "--file", "prompt.md").requireFailure(t, cli.CodeUsage)
		assert.Contains(t, f.Message, "not more than one")
		assert.Empty(t, disp.recorded())
	})

	t.Run("positional dash reads stdin, rejects whitespace-only stdin, and guards shared stdin with --output-schema -", func(t *testing.T) {
		hub, disp := &recordingHub{workers: workerRows("worker-A"), materialized: state}, &creationPublicationDispatcher{}
		startSpawnIPC(t, hub, disp)

		_ = runControlIn(t, "Prompt from stdin\n", "agent", "open", "--provider", "claude", "--workspace", "ws-1", "--tile", "root-1",
			"--worker", "worker-A", "-", "--json").Data(t)
		calls := disp.recorded()
		require.Len(t, calls, 1)
		assert.Equal(t, "Prompt from stdin\n", calls[0].in.(*leapmuxv1.OpenAgentRequest).GetInitialMessage())

		f := runControlIn(t, "   \n\t ", "agent", "open", "--provider", "claude", "-", "--json").requireFailure(t, cli.CodeUsage)
		assert.Equal(t, "prompt is empty", f.Message)

		f = runControlIn(t, "Prompt text\n", "agent", "open", "--provider", "claude", "-", "--wait", "--output-schema", "-", "--json").requireFailure(t, cli.CodeUsage)
		assert.Contains(t, f.Message, "--output-schema cannot read stdin because the prompt already reads stdin")
		assert.Len(t, disp.recorded(), 1, "failed stdin validations must not open an agent")
	})
}

func TestSpawnedAgentLineage_ParentChildrenAndList(t *testing.T) {
	t.Run("agentRowOf exposes spawned_by_agent_id, forked_from_agent_id, and supports_fork", func(t *testing.T) {
		row := agentRowOf(&leapmuxv1.AgentInfo{
			Id:                "ag-child",
			SpawnedByAgentId:  "ag-spawner",
			ForkedFromAgentId: "ag-source",
			SupportsFork:      true,
		}, false)
		assert.Equal(t, "ag-spawner", row.SpawnedByAgentID)
		assert.Equal(t, "ag-source", row.ForkedFromAgentID)
		assert.True(t, row.SupportsFork)
	})

	t.Run("agent list --parent matches both parent_agent_id and spawned_by_agent_id", func(t *testing.T) {
		leadID := fullTestID("ag-lead")
		subID := fullTestID("ag-sub")
		spawnID := fullTestID("ag-spawn")
		otherID := fullTestID("ag-other")

		hub := &recordingHub{
			tabs: []*leapmuxv1.WorkspaceTab{
				{Tab: &leapmuxv1.TabIdent{TabId: leadID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}, WorkerId: "worker-A", WorkspaceId: "ws-1"},
				{Tab: &leapmuxv1.TabIdent{TabId: subID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}, WorkerId: "worker-A", WorkspaceId: "ws-1"},
				{Tab: &leapmuxv1.TabIdent{TabId: spawnID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}, WorkerId: "worker-A", WorkspaceId: "ws-1"},
				{Tab: &leapmuxv1.TabIdent{TabId: otherID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}, WorkerId: "worker-A", WorkspaceId: "ws-1"},
			},
		}
		disp := &tabMetadataDispatcher{
			agents: []*leapmuxv1.AgentInfo{
				{Id: leadID, Title: "Lead", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE},
				{Id: subID, Title: "Sub", ParentAgentId: leadID, AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE},
				{Id: spawnID, Title: "Spawned", SpawnedByAgentId: leadID, AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE},
				{Id: otherID, Title: "Other", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE},
			},
		}
		startSpawnIPC(t, hub, disp)

		data := runControl(t, "agent", "list", "--parent", "Lead").Data(t)
		agents := data["agents"].([]any)
		require.Len(t, agents, 2)
		gotIDs := []string{agents[0].(map[string]any)["id"].(string), agents[1].(map[string]any)["id"].(string)}
		assert.ElementsMatch(t, []string{subID, spawnID}, gotIDs)
	})

	t.Run("parent selector resolves cross-worker spawned_by_agent_id via Hub LocateTab", func(t *testing.T) {
		spawnerID := fullTestID("ag-spawner")
		childID := fullTestID("ag-spawned")

		dispA := &codedWorkerDispatcher{fn: func(method string, _ []byte) (proto.Message, error) {
			if method == leapmuxv1connect.WorkerAgentServiceListAgentsProcedure {
				return &leapmuxv1.ListAgentsResponse{
					Agents: []*leapmuxv1.AgentInfo{{
						Id:               childID,
						WorkerId:         "worker-A",
						SpawnedByAgentId: spawnerID,
					}},
				}, nil
			}
			return nil, connect.NewError(connect.CodeUnimplemented, method)
		}}
		hub := &recordingHub{
			workers: workerRows("worker-A", "worker-B"),
			locateTab: &leapmuxv1.WorkspaceTab{
				Tab:         &leapmuxv1.TabIdent{TabId: spawnerID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
				WorkspaceId: "ws-remote",
				TileId:      "tile-remote",
				WorkerId:    "worker-B",
			},
		}
		startSpawnIPC(t, hub, dispA)
		t.Setenv(controlenv.TabIDVariable, childID)
		t.Setenv(controlenv.TabTypeVariable, "agent")
		t.Setenv(controlenv.WorkerIDVariable, "worker-A")

		s, err := openSession(&cli.Invocation{Context: t.Context(), Command: NewCommand()})
		require.NoError(t, err)
		defer s.Close()

		got, err := s.resolveTab(resolve.KindAgent, "parent", resolve.Need{WorkerID: true, WorkspaceID: true})
		require.NoError(t, err)
		assert.Equal(t, spawnerID, got.ID)
		assert.Equal(t, "worker-B", got.WorkerID)
		assert.Equal(t, "ws-remote", got.WorkspaceID)
	})

	t.Run("parent selector falls back to calling worker for unopened virtual subagent parent", func(t *testing.T) {
		parentID := fullTestID("ag-unopen-par")
		childID := fullTestID("ag-sub-child")

		dispA := &codedWorkerDispatcher{fn: func(method string, _ []byte) (proto.Message, error) {
			if method == leapmuxv1connect.WorkerAgentServiceListAgentsProcedure {
				return &leapmuxv1.ListAgentsResponse{
					Agents: []*leapmuxv1.AgentInfo{{
						Id:            childID,
						WorkerId:      "worker-A",
						ParentAgentId: parentID,
					}},
				}, nil
			}
			return nil, connect.NewError(connect.CodeUnimplemented, method)
		}}
		hub := &recordingHub{workers: workerRows("worker-A")}
		startSpawnIPC(t, hub, dispA)
		t.Setenv(controlenv.TabIDVariable, childID)
		t.Setenv(controlenv.TabTypeVariable, "agent")
		t.Setenv(controlenv.WorkerIDVariable, "worker-A")

		s, err := openSession(&cli.Invocation{Context: t.Context(), Command: NewCommand()})
		require.NoError(t, err)
		defer s.Close()

		got, err := s.resolveTab(resolve.KindAgent, "parent", resolve.Need{WorkerID: true})
		require.NoError(t, err)
		assert.Equal(t, parentID, got.ID)
		assert.Equal(t, "worker-A", got.WorkerID)
	})

	t.Run("children selector returns deduplicated union of virtual subagents and spawned peer agents", func(t *testing.T) {
		parentID := fullTestID("ag-par")
		virtualID := fullTestID("ag-virt")
		spawnedID := fullTestID("ag-peer")

		hub := &recordingHub{
			workers: workerRows("worker-A"),
			tabs: []*leapmuxv1.WorkspaceTab{
				{Tab: &leapmuxv1.TabIdent{TabId: parentID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}, WorkerId: "worker-A", WorkspaceId: "ws-1"},
				{Tab: &leapmuxv1.TabIdent{TabId: virtualID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}, WorkerId: "worker-A", WorkspaceId: "ws-1"},
				{Tab: &leapmuxv1.TabIdent{TabId: spawnedID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}, WorkerId: "worker-A", WorkspaceId: "ws-1"},
			},
		}
		disp := &interruptTestDispatcher{
			agents: []*leapmuxv1.AgentInfo{
				{Id: parentID, WorkerId: "worker-A"},
				{Id: virtualID, ParentAgentId: parentID, WorkerId: "worker-A"},
				{Id: spawnedID, SpawnedByAgentId: parentID, WorkerId: "worker-A"},
			},
		}
		startSpawnIPC(t, hub, disp)
		t.Setenv(controlenv.TabIDVariable, parentID)
		t.Setenv(controlenv.TabTypeVariable, "agent")
		t.Setenv(controlenv.WorkerIDVariable, "worker-A")

		data := runControl(t, "agent", "interrupt", "children").Data(t)
		agents := data["agents"].([]any)
		require.Len(t, agents, 2)
		assert.Equal(t, virtualID, agents[0].(map[string]any)["id"])
		assert.Equal(t, spawnedID, agents[1].(map[string]any)["id"])
	})
}

func TestAgentCommand_HelpGroupsAndConsistentShortFlags(t *testing.T) {
	root := NewCommand()

	t.Run("every agent subcommand is assigned to a command group with no Other bucket", func(t *testing.T) {
		agentCmd, _, err := root.Find([]string{"agent"})
		require.NoError(t, err)
		for _, child := range agentCmd.Commands() {
			if !child.IsAvailableCommand() {
				continue
			}
			assert.NotEmptyf(t, cli.CommandGroupOf(child), "agent subcommand %q must belong to a command group", child.Name())
		}
		helpOut := runControlText(t, "agent", "--help")
		require.Equal(t, cli.ExitOK, helpOut.Code, helpOut.Stderr)
		helpText := helpOut.Stdout
		assert.Contains(t, helpText, "Core:")
		assert.Contains(t, helpText, "Prompts & Control:")
		assert.Contains(t, helpText, "Transcript & Inspection:")
		assert.Contains(t, helpText, "Queue & Goals:")
		assert.Contains(t, helpText, "Discovery:")
		assert.NotContains(t, helpText, "\nOther:\n")
	})

	t.Run("consistent shorthand flags are registered across commands", func(t *testing.T) {
		checks := []struct {
			path      []string
			flagName  string
			shorthand string
		}{
			{[]string{"agent", "open"}, "message", "m"},
			{[]string{"agent", "fork"}, "message", "m"},
			{[]string{"agent", "messages"}, "follow", "f"},
			{[]string{"agent", "messages"}, "limit", "n"},
			{[]string{"agent", "search"}, "limit", "n"},
			{[]string{"file", "search"}, "limit", "n"},
			{[]string{"file", "glob"}, "limit", "n"},
			{[]string{"events"}, "limit", "n"},
			{[]string{"terminal", "read"}, "lines", "n"},
			{[]string{"terminal", "read"}, "follow", "f"},
			{[]string{"terminal", "quake", "read"}, "lines", "n"},
			{[]string{"terminal", "quake", "read"}, "follow", "f"},
		}
		for _, tc := range checks {
			cmd, _, err := root.Find(tc.path)
			require.NoErrorf(t, err, "find %v", tc.path)
			f := cmd.Flags().Lookup(tc.flagName)
			require.NotNilf(t, f, "%v missing --%s", tc.path, tc.flagName)
			assert.Equalf(t, tc.shorthand, f.Shorthand, "%v --%s shorthand", tc.path, tc.flagName)
		}
	})

	t.Run("runtime parsing of -n and -f shorthands across commands", func(t *testing.T) {
		var searchLimit int32
		disp := &codedWorkerDispatcher{fn: func(method string, body []byte) (proto.Message, error) {
			if method == leapmuxv1connect.WorkerAgentServiceSearchAgentMessagesProcedure {
				var req leapmuxv1.SearchAgentMessagesRequest
				require.NoError(t, proto.Unmarshal(body, &req))
				searchLimit = req.GetLimit()
				return &leapmuxv1.SearchAgentMessagesResponse{}, nil
			}
			return nil, connect.NewError(connect.CodeUnimplemented, method)
		}}
		startSpawnIPC(t, &recordingHub{workers: []*leapmuxv1.Worker{{Id: "worker-A", Online: true}}}, disp)
		_ = runControl(t, "agent", "search", "needle", "-n", "5").Data(t)
		assert.Equal(t, int32(5), searchLimit)

		evOpts, err := eventsOptionsFor(t, "-n", "5")
		require.NoError(t, err)
		assert.Equal(t, 5, evOpts.limit)

		for _, path := range [][]string{
			{"terminal", "read"},
			{"terminal", "quake", "read"},
		} {
			cmd, _, err := NewCommand().Find(path)
			require.NoError(t, err)
			require.NoError(t, cmd.ParseFlags([]string{"-n", "5", "-f"}))
			inv := &cli.Invocation{Command: cmd}
			readOpts, err := screenReadOptionsOf(inv)
			require.NoError(t, err)
			assert.Equal(t, 5, readOpts.lines, "path %v", path)
			assert.True(t, boolFlag(inv, "follow"), "path %v", path)
		}
	})
}

func TestNoticeIgnoredHubEnv(t *testing.T) {
	t.Run("emits notice in text mode on WorkerIPCClient when LEAPMUX_HUB is set", func(t *testing.T) {
		t.Setenv("LEAPMUX_HUB", "https://hub.example")
		var stderr bytes.Buffer
		inv := &cli.Invocation{
			Printer: &cli.Printer{Out: io.Discard, Err: &stderr, Format: cli.FormatText},
		}
		noticeIgnoredHubEnv(inv, &control.WorkerIPCClient{SocketURL: "unix:/tmp/tab.sock"})
		assert.Contains(t, stderr.String(), "connected via tab socket (ignoring LEAPMUX_HUB; pass --hub to override)")
	})

	t.Run("stays silent in quiet, json, or ndjson mode, for empty/whitespace LEAPMUX_HUB, on HubClient, or with explicit --hub", func(t *testing.T) {
		t.Setenv("LEAPMUX_HUB", "https://hub.example")
		for _, tc := range []struct {
			name   string
			format cli.Format
			peer   control.Peer
		}{
			{"json", cli.FormatJSON, &control.WorkerIPCClient{SocketURL: "unix:/tmp/tab.sock"}},
			{"ndjson", cli.FormatNDJSON, &control.WorkerIPCClient{SocketURL: "unix:/tmp/tab.sock"}},
			{"quiet", cli.FormatQuiet, &control.WorkerIPCClient{SocketURL: "unix:/tmp/tab.sock"}},
			{"hub client", cli.FormatText, &control.HubClient{HubURL: "https://hub.example"}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				var stderr bytes.Buffer
				inv := &cli.Invocation{
					Printer: &cli.Printer{Out: io.Discard, Err: &stderr, Format: tc.format},
				}
				noticeIgnoredHubEnv(inv, tc.peer)
				assert.Empty(t, stderr.String())
			})
		}

		for name, envVal := range map[string]string{
			"empty LEAPMUX_HUB":      "",
			"whitespace LEAPMUX_HUB": "   \t ",
		} {
			t.Run(name, func(t *testing.T) {
				t.Setenv("LEAPMUX_HUB", envVal)
				var stderr bytes.Buffer
				inv := &cli.Invocation{
					Printer: &cli.Printer{Out: io.Discard, Err: &stderr, Format: cli.FormatText},
				}
				noticeIgnoredHubEnv(inv, &control.WorkerIPCClient{SocketURL: "unix:/tmp/tab.sock"})
				assert.Empty(t, stderr.String())
			})
		}

		t.Run("explicit --hub connects via HubClient and emits no ignored-hub notice", func(t *testing.T) {
			hub := &recordingHub{workers: workerRows("worker-A")}
			disp := &codedWorkerDispatcher{fn: func(method string, _ []byte) (proto.Message, error) {
				return nil, connect.NewError(connect.CodeUnimplemented, method)
			}}
			h := startHubChannelHarness(t, hub, disp)
			startSpawnIPC(t, hub, disp)
			t.Setenv("LEAPMUX_HUB", "https://ignored.example.test")

			viaTab := runControlText(t, "worker", "list")
			require.Equal(t, cli.ExitOK, viaTab.Code, viaTab.Stderr)
			assert.Contains(t, viaTab.Stderr, "ignoring LEAPMUX_HUB")

			viaHub := runControlText(t, "worker", "list", "--hub", h.url)
			require.Equal(t, cli.ExitOK, viaHub.Code, viaHub.Stderr)
			assert.NotContains(t, viaHub.Stderr, "ignoring LEAPMUX_HUB")
		})
	})
}
