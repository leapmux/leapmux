package cmd

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

func TestParseSinceFlag(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)

	got, err := parseSinceFlag("", now)
	require.NoError(t, err)
	assert.Empty(t, got)

	got, err = parseSinceFlag("2026-11-23T09:00:00+09:00", now)
	require.NoError(t, err)
	assert.Equal(t, "2026-11-23T00:00:00.000Z", got)

	for raw, want := range map[string]string{
		"24h":  "2026-11-22T12:00:00.000Z",
		"-24h": "2026-11-22T12:00:00.000Z",
		"30m":  "2026-11-23T11:30:00.000Z",
	} {
		got, err := parseSinceFlag(raw, now)
		require.NoError(t, err, raw)
		assert.Equal(t, want, got, raw)
	}

	for _, invalid := range []string{"+24h", "+30m", "-+24h", "0s", "-0s", "--24h", "yesterday"} {
		_, err := parseSinceFlag(invalid, now)
		require.Error(t, err, invalid)
		assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code, invalid)
	}
}

func TestSortAgentSearchHits_ParsesInstantsAndBreaksTiesDeterministically(t *testing.T) {
	// 11:30:00-05:00 equals 16:30:00Z, which is AFTER 15:00:00Z even though
	// raw string comparison puts "15:00:00Z" ahead of "11:30:00-05:00".
	hits := []agentSearchHit{
		{AgentID: "ag-b", WorkerID: "w-2", Seq: 5, CreatedAt: "bad-ts-a"},
		{AgentID: "ag-d", WorkerID: "w-1", Seq: 8, CreatedAt: "bad-ts-a"},
		{AgentID: "ag-e", WorkerID: "w-1", Seq: 2, CreatedAt: "bad-ts-b"},
		{AgentID: "ag-b", WorkerID: "w-2", Seq: 10, CreatedAt: "2026-11-23T15:00:00Z"},
		{AgentID: "ag-a", WorkerID: "w-2", Seq: 10, CreatedAt: "2026-11-23T15:00:00Z"},
		{AgentID: "ag-a", WorkerID: "w-1", Seq: 10, CreatedAt: "2026-11-23T15:00:00Z"},
		{AgentID: "ag-c", WorkerID: "w-1", Seq: 12, CreatedAt: "2026-11-23T15:00:00Z"},
		{AgentID: "ag-tz", WorkerID: "w-1", Seq: 1, CreatedAt: "2026-11-23T11:30:00-05:00"},
		{AgentID: "ag-empty", WorkerID: "w-1", Seq: 99, CreatedAt: ""},
	}

	sortAgentSearchHits(hits)

	type key struct {
		agentID  string
		workerID string
		seq      int64
	}
	got := make([]key, len(hits))
	for i, h := range hits {
		got[i] = key{h.AgentID, h.WorkerID, h.Seq}
	}
	want := []key{
		{"ag-tz", "w-1", 1},     // 16:30:00Z (newest instant)
		{"ag-c", "w-1", 12},     // 15:00:00Z, highest seq
		{"ag-a", "w-1", 10},     // 15:00:00Z, seq 10, lowest agentID + workerID
		{"ag-a", "w-2", 10},     // 15:00:00Z, seq 10, same agentID, higher workerID
		{"ag-b", "w-2", 10},     // 15:00:00Z, seq 10, higher agentID
		{"ag-e", "w-1", 2},      // unparseable "bad-ts-b" > "bad-ts-a"
		{"ag-d", "w-1", 8},      // unparseable "bad-ts-a", higher seq
		{"ag-b", "w-2", 5},      // unparseable "bad-ts-a", lower seq
		{"ag-empty", "w-1", 99}, // empty timestamp last
	}
	assert.Equal(t, want, got)
}

func TestSelectSearchFailure_IsDeterministicAcrossInputOrders(t *testing.T) {
	offlineErr := cli.Newf(cli.CodeWorkerOffline, "worker-a unreachable")
	unavailableErr := cli.Newf(cli.CodeUnavailable, "worker-a unavailable")
	invalidErr := cli.Newf(cli.CodeInvalidArgument, "invalid regex on worker-b")
	permErr := cli.Newf(cli.CodePermissionDenied, "denied on worker-c")
	usageErr := cli.Newf(cli.CodeUsage, "bad flag on worker-b")

	t.Run("client error wins over offline error regardless of order", func(t *testing.T) {
		a := selectSearchFailure([]workerSearchFailure{
			{workerID: "worker-a", err: offlineErr},
			{workerID: "worker-b", err: invalidErr},
			{workerID: "worker-c", err: permErr},
		})
		b := selectSearchFailure([]workerSearchFailure{
			{workerID: "worker-c", err: permErr},
			{workerID: "worker-b", err: invalidErr},
			{workerID: "worker-a", err: offlineErr},
		})
		assert.Equal(t, invalidErr, a)
		assert.Equal(t, a, b)
	})

	t.Run("permission_denied and usage win over unavailable in worker-id order", func(t *testing.T) {
		gotPerm := selectSearchFailure([]workerSearchFailure{
			{workerID: "worker-c", err: invalidErr},
			{workerID: "worker-a", err: unavailableErr},
			{workerID: "worker-b", err: permErr},
		})
		assert.Equal(t, permErr, gotPerm)

		gotUsage := selectSearchFailure([]workerSearchFailure{
			{workerID: "worker-a", err: unavailableErr},
			{workerID: "worker-b", err: usageErr},
		})
		assert.Equal(t, usageErr, gotUsage)
	})

	t.Run("all offline wraps lowest worker id deterministically", func(t *testing.T) {
		err1 := errors.New("dial worker-1 failed")
		err2 := errors.New("dial worker-2 failed")

		a := selectSearchFailure([]workerSearchFailure{
			{workerID: "worker-2", err: err2},
			{workerID: "worker-1", err: err1},
		})
		b := selectSearchFailure([]workerSearchFailure{
			{workerID: "worker-1", err: err1},
			{workerID: "worker-2", err: err2},
		})
		assert.Equal(t, cli.CodeWorkerOffline, cli.FromError(a).Code)
		assert.Equal(t, cli.FromError(a).Message, cli.FromError(b).Message)
		assert.Contains(t, cli.FromError(a).Message, "dial worker-1 failed")
	})
}

type searchTestDispatcher struct {
	hits    []*leapmuxv1.AgentMessageSearchHit
	errCode codes.Code
	errMsg  string
	calls   atomic.Int32
}

func (d *searchTestDispatcher) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
	if req.GetMethod() != leapmuxv1connect.WorkerAgentServiceSearchAgentMessagesProcedure {
		_ = rw.SendError(int32(codes.Unimplemented), "unexpected method: "+req.GetMethod())
		return
	}
	d.calls.Add(1)
	if d.errCode != codes.OK {
		_ = rw.SendError(int32(d.errCode), d.errMsg)
		return
	}
	writeProtoResponse(rw, &leapmuxv1.SearchAgentMessagesResponse{Hits: d.hits})
}

func mountWorkerSearchDispatcher(h *hubChannelHarness, workerID string, disp *searchTestDispatcher) {
	d := harnessDispatcher(disp)
	d.Handle(leapmuxv1connect.WorkerAgentServiceSearchAgentMessagesProcedure, channel.Registration{}, disp.DispatchWith)
	h.managerForWorker(workerID).SetDispatcher(d)
}

func TestRunAgentSearch_EmptyHitsAcrossWorkers(t *testing.T) {
	t.Run("zero online workers returns empty hits without dialing", func(t *testing.T) {
		disp := &searchTestDispatcher{}
		hub := &recordingHub{
			workers: []*leapmuxv1.Worker{{Id: "worker-offline", Online: false}},
		}
		h := startHubChannelHarness(t, hub, disp)

		data := runControl(t, "agent", "search", "needle", "--hub", h.url).Data(t)
		assert.Equal(t, "needle", data["query"])
		assert.Equal(t, []any{}, data["hits"])
		assert.Zero(t, h.channelOpens())
		assert.Zero(t, disp.calls.Load())
	})

	t.Run("multiple online workers returning no hits produce an empty list", func(t *testing.T) {
		dispA := &searchTestDispatcher{}
		dispB := &searchTestDispatcher{}
		hub := &recordingHub{
			workers: []*leapmuxv1.Worker{
				{Id: "worker-A", Online: true},
				{Id: "worker-B", Online: true},
			},
		}
		h := startHubChannelHarness(t, hub, dispA)
		mountWorkerSearchDispatcher(h, "worker-A", dispA)
		mountWorkerSearchDispatcher(h, "worker-B", dispB)

		data := runControl(t, "agent", "search", "nothing-here", "--hub", h.url).Data(t)
		assert.Equal(t, "nothing-here", data["query"])
		assert.Equal(t, []any{}, data["hits"])
		assert.Nil(t, data["truncated"])
		assert.Equal(t, int32(1), dispA.calls.Load())
		assert.Equal(t, int32(1), dispB.calls.Load())

		quiet := runControlText(t, "agent", "search", "nothing-here", "-q", "--hub", h.url)
		require.Equal(t, cli.ExitOK, quiet.Code, quiet.Stderr)
		assert.Empty(t, quiet.Stdout)
	})
}

func TestRunAgentSearch_MultiWorkerFailurePriorityAndPartialWarning(t *testing.T) {
	t.Run("all workers failing prefers client error over unavailable", func(t *testing.T) {
		dispA := &searchTestDispatcher{errCode: codes.Unavailable, errMsg: "worker-A offline"}
		dispB := &searchTestDispatcher{errCode: codes.InvalidArgument, errMsg: "invalid regexp syntax"}
		dispC := &searchTestDispatcher{errCode: codes.PermissionDenied, errMsg: "worker-C denied"}
		hub := &recordingHub{
			workers: []*leapmuxv1.Worker{
				{Id: "worker-A", Online: true},
				{Id: "worker-B", Online: true},
				{Id: "worker-C", Online: true},
			},
		}
		h := startHubChannelHarness(t, hub, dispA)
		mountWorkerSearchDispatcher(h, "worker-A", dispA)
		mountWorkerSearchDispatcher(h, "worker-B", dispB)
		mountWorkerSearchDispatcher(h, "worker-C", dispC)

		f := runControl(t, "agent", "search", "[unclosed", "--regex", "--hub", h.url).requireFailure(t, cli.CodeInvalidArgument)
		assert.Contains(t, f.Message, "invalid regexp syntax")
	})

	t.Run("all workers unavailable wraps lowest worker id as worker_offline", func(t *testing.T) {
		dispA := &searchTestDispatcher{errCode: codes.Unavailable, errMsg: "worker-A unreachable"}
		dispB := &searchTestDispatcher{errCode: codes.Unavailable, errMsg: "worker-B unreachable"}
		hub := &recordingHub{
			workers: []*leapmuxv1.Worker{
				{Id: "worker-B", Online: true},
				{Id: "worker-A", Online: true},
			},
		}
		h := startHubChannelHarness(t, hub, dispA)
		mountWorkerSearchDispatcher(h, "worker-A", dispA)
		mountWorkerSearchDispatcher(h, "worker-B", dispB)

		f := runControl(t, "agent", "search", "timeout", "--hub", h.url).requireFailure(t, cli.CodeWorkerOffline)
		assert.Contains(t, f.Message, "failed on all 2 workers")
		assert.Contains(t, f.Message, "worker-A unreachable")
	})

	t.Run("partial worker failure returns surviving hits and warns on stderr in text mode", func(t *testing.T) {
		agID := fullTestID("ag-partial-hit")
		dispA := &searchTestDispatcher{errCode: codes.Unavailable, errMsg: "worker-A down"}
		dispB := &searchTestDispatcher{
			hits: []*leapmuxv1.AgentMessageSearchHit{{
				AgentId:    agID,
				AgentTitle: "Healthy Agent",
				Provider:   leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
				Seq:        3,
				Source:     leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
				Snippet:    "recovered hit",
				CreatedAt:  "2026-11-23T12:00:00Z",
			}},
		}
		hub := &recordingHub{
			workers: []*leapmuxv1.Worker{
				{Id: "worker-A", Online: true},
				{Id: "worker-B", Online: true},
			},
		}
		h := startHubChannelHarness(t, hub, dispA)
		mountWorkerSearchDispatcher(h, "worker-A", dispA)
		mountWorkerSearchDispatcher(h, "worker-B", dispB)

		out := runControlText(t, "agent", "search", "recovered", "--hub", h.url)
		require.Equal(t, cli.ExitOK, out.Code, out.Stderr)
		assert.Contains(t, out.Stdout, "recovered hit")
		assert.Contains(t, out.Stderr, "warning: skipped unreachable worker(s)")
		assert.Contains(t, out.Stderr, "worker-A down")
	})
}

func TestRunAgentSearch_EmitsCLIAliasProvider(t *testing.T) {
	agID := fullTestID("ag-search-alias")
	startPhase3BIPC(t,
		func(method string, _ []byte) (proto.Message, error) {
			if method == leapmuxv1connect.WorkerManagementServiceListWorkersProcedure {
				return &leapmuxv1.ListWorkersResponse{
					Workers: []*leapmuxv1.Worker{{Id: "worker-A", Online: true}},
				}, nil
			}
			return &leapmuxv1.ListWorkspacesResponse{}, nil
		},
		func(method string, _ []byte) (proto.Message, error) {
			if method != leapmuxv1connect.WorkerAgentServiceSearchAgentMessagesProcedure {
				return nil, assert.AnError
			}
			return &leapmuxv1.SearchAgentMessagesResponse{
				Hits: []*leapmuxv1.AgentMessageSearchHit{{
					AgentId:    agID,
					AgentTitle: "Reviewer",
					Provider:   leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
					Seq:        7,
					Source:     leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
					Snippet:    "found match",
					CreatedAt:  "2026-11-23T12:00:00Z",
				}},
			}, nil
		},
	)

	res := runControl(t, "agent", "search", "match").Data(t)
	hits, ok := res["hits"].([]any)
	require.True(t, ok)
	require.Len(t, hits, 1)
	assert.Equal(t, "claude-code", hits[0].(map[string]any)["provider"])
}
