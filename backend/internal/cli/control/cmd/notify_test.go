package cmd

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

// startNotifyWorker serves w as the Worker of the tab that runs the command,
// as a spawned agent's socket does: LEAPMUX_CONTROL_WORKER_ID is worker-A, and
// the token belongs to the tab agent-1 (see startSpawnIPC).
func startNotifyWorker(t *testing.T, w *privateCommandWorker) *recordingHub {
	t.Helper()
	hub := &recordingHub{workers: workerRows("worker-A")}
	startSpawnIPC(t, hub, w)
	t.Setenv("LEAPMUX_CONTROL_WORKER_ID", "worker-A")
	return hub
}

// requireOneNotify returns the one Notify request that the fake Worker
// received, and the caller that the router dispatched it under.
func requireOneNotify(t *testing.T, w *privateCommandWorker) (*leapmuxv1.NotifyRequest, privateCommandCall) {
	t.Helper()
	calls := w.recorded()
	require.Len(t, calls, 1)
	require.Equal(t, leapmuxv1connect.WorkerPrivateServiceNotifyProcedure, calls[0].method)
	return calls[0].request.(*leapmuxv1.NotifyRequest), calls[0]
}

// The command goes to the Worker of the tab that runs it, and the router
// stamps that tab from the control token: the CLI sends no tab, so it cannot
// specify another one.
func TestNotifyOverIPC_SendsTheNotificationToTheTabsWorker(t *testing.T) {
	w := &privateCommandWorker{}
	startNotifyWorker(t, w)

	data := runControl(t, "notify", "Tests passed", "--body", "All 42 tests passed").Data(t)
	assert.Equal(t, map[string]any{"id": "test-notification", "worker_id": "worker-A", "title": "Tests passed", "body": "All 42 tests passed"}, data)

	req, call := requireOneNotify(t, w)
	assert.True(t, proto.Equal(&leapmuxv1.NotifyRequest{Title: "Tests passed", Body: "All 42 tests passed"}, req), "request: %v", req)
	assert.Equal(t, "agent-1", call.caller.SourceTabID, "the router stamps the tab of the token")
}

// A body read from stdin goes to the Worker as the pipe wrote it. The Worker
// owns the trim, so the CLI sends the line ending that the pipe left and
// prints the text that the Worker answered with.
func TestNotifyOverIPC_ReadsTheBodyFromStdin(t *testing.T) {
	w := &privateCommandWorker{answers: map[string]proto.Message{
		leapmuxv1connect.WorkerPrivateServiceNotifyProcedure: &leapmuxv1.NotifyResponse{
			Id:    "stored-id",
			Title: "Build failed",
			Body:  "error: 3 tests failed\nFAIL ./internal/cli",
		},
	}}
	startNotifyWorker(t, w)

	o := runControlIn(t, "error: 3 tests failed\nFAIL ./internal/cli\n", "notify", " Build failed ", "--body", "-", "--json")
	data := o.Data(t)
	assert.Equal(t, "Build failed", data["title"])
	assert.Equal(t, "error: 3 tests failed\nFAIL ./internal/cli", data["body"])

	req, _ := requireOneNotify(t, w)
	assert.Equal(t, " Build failed ", req.GetTitle())
	assert.Equal(t, "error: 3 tests failed\nFAIL ./internal/cli\n", req.GetBody())
}

// The result is the Worker's answer, not the command line: the Worker decides
// what it stored, and the CLI keeps no copy of that rule.
func TestNotifyOverIPC_PrintsTheWorkersAnswer(t *testing.T) {
	w := &privateCommandWorker{answers: map[string]proto.Message{
		leapmuxv1connect.WorkerPrivateServiceNotifyProcedure: &leapmuxv1.NotifyResponse{Id: "stored-id", Title: "Stored title"},
	}}
	startNotifyWorker(t, w)

	data := runControl(t, "notify", "Sent title", "--body", "Sent body").Data(t)
	assert.Equal(t, map[string]any{"id": "stored-id", "worker_id": "worker-A", "title": "Stored title"}, data,
		"a body that the Worker did not store is not printed")
}

func TestNotifyOverIPC_RejectsAResponseWithoutANotificationID(t *testing.T) {
	w := &privateCommandWorker{answers: map[string]proto.Message{
		leapmuxv1connect.WorkerPrivateServiceNotifyProcedure: &leapmuxv1.NotifyResponse{Title: "Stored title"},
	}}
	startNotifyWorker(t, w)
	failure := runControl(t, "notify", "Stored title").requireFailure(t, cli.CodeInternal)
	assert.Contains(t, failure.Message, "returned no notification ID")
}

// The body is optional, and an absent body leaves the result without one.
func TestNotifyOverIPC_SendsATitleAlone(t *testing.T) {
	w := &privateCommandWorker{}
	startNotifyWorker(t, w)

	data := runControl(t, "notify", "Done").Data(t)
	assert.NotContains(t, data, "body")

	req, _ := requireOneNotify(t, w)
	assert.True(t, proto.Equal(&leapmuxv1.NotifyRequest{Title: "Done"}, req), "request: %v", req)
}

// --worker selects another Worker. Over the socket of a tab, that call
// travels to the other Worker; the socket's own Worker serves the call here,
// because the selected Worker is the socket's own.
func TestNotifyOverIPC_SendsToTheSelectedWorker(t *testing.T) {
	w := &privateCommandWorker{}
	startSpawnIPC(t, &recordingHub{workers: workerRows("worker-A")}, w)

	data := runControl(t, "notify", "Deploy finished", "--worker", "worker-A").Data(t)
	assert.Equal(t, "worker-A", data["worker_id"])
	requireOneNotify(t, w)
}

func TestNotifyOverIPC_PrintsTheNotificationAsText(t *testing.T) {
	w := &privateCommandWorker{}
	startNotifyWorker(t, w)

	o := runControlText(t, "notify", "Tests passed", "--body", "All green")
	require.Equal(t, cli.ExitOK, o.Code, "stderr: %s", o.Stderr)
	assert.Equal(t, "id: test-notification  worker_id: worker-A  title: \"Tests passed\"  body: \"All green\"\n", o.Stdout)
}

func TestNotifyOverIPC_KeepsTheAddressApartFromTheSender(t *testing.T) {
	w := &privateCommandWorker{}
	tab := terminalTab()
	startTabWorker(t, w, tab)
	targetID := tab.GetTab().GetTabId()
	data := runControl(t, "notify", "Deployment ready", "--tab", targetID).Data(t)
	assert.Equal(t, "test-notification", data["id"])
	assert.Equal(t, targetID, data["tab_id"])
	request, call := requireOneNotify(t, w)
	assert.True(t, proto.Equal(tab.GetTab(), request.GetDestination().GetTab()))
	assert.Equal(t, "agent-1", call.caller.SourceTabID)
}

func TestNotifyListAndClearUseTheSelectedWorker(t *testing.T) {
	w := &privateCommandWorker{answers: map[string]proto.Message{
		leapmuxv1connect.WorkerPrivateServiceListNotificationsProcedure: &leapmuxv1.ListNotificationsResponse{
			Notifications: []*leapmuxv1.UserNotification{{Id: "recent-id", Title: "Deployment ready", SourceTabId: "agent-1"}},
		},
		leapmuxv1connect.WorkerPrivateServiceClearNotificationProcedure: &leapmuxv1.ClearNotificationResponse{Cleared: true},
	}}
	startNotifyWorker(t, w)
	list := runControl(t, "notification", "list").Data(t)
	assert.Equal(t, map[string]any{
		"worker_id": "worker-A",
		"notifications": []any{
			map[string]any{"id": "recent-id", "title": "Deployment ready", "source_tab_id": "agent-1"},
		},
	}, list)
	cleared := runControl(t, "notification", "clear", "recent-id").Data(t)
	assert.Equal(t, map[string]any{"id": "recent-id", "worker_id": "worker-A", "cleared": true}, cleared)
	calls := w.recorded()
	require.Len(t, calls, 3)
	assert.Equal(t, leapmuxv1connect.WorkerPrivateServiceListNotificationsProcedure, calls[0].method)
	assert.Equal(t, leapmuxv1connect.WorkerPrivateServiceListNotificationsProcedure, calls[1].method)
	assert.True(t, proto.Equal(&leapmuxv1.ClearNotificationRequest{Id: "recent-id"}, calls[2].request))

	// Unique prefix matching works against ListNotifications too.
	clearedPrefix := runControl(t, "notification", "clear", "rece").Data(t)
	assert.Equal(t, map[string]any{"id": "recent-id", "worker_id": "worker-A", "cleared": true}, clearedPrefix)
}

// What the CLI refuses, it refuses before it sends anything.
func TestNotify_RefusesAnInvalidCommandLine(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		message string
	}{
		{"an empty title", []string{"notify", ""}, "the title is empty"},
		{"a title of spaces", []string{"notify", "  \t"}, "the title is empty"},
		{"no title", []string{"notify"}, ""},
		{"both destinations", []string{"notify", "Ready", "--tab", "agent-1", "--workspace", "ws-1"}, "--tab and --workspace"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := &privateCommandWorker{}
			startNotifyWorker(t, w)

			f := runControl(t, tc.args...).requireFailure(t, cli.CodeUsage)
			assert.Contains(t, f.Message, tc.message)
			assert.Empty(t, w.recorded(), "a refused command line sends nothing")
		})
	}
}

// Outside a tab, nothing identifies a Worker, so the command asks for --worker.
func TestNotify_NeedsAWorkerOutsideATab(t *testing.T) {
	w := &privateCommandWorker{}
	startSpawnIPC(t, &recordingHub{workers: workerRows("worker-A")}, w)

	f := runControl(t, "notify", "Tests passed").requireFailure(t, cli.CodeUsage)
	assert.Equal(t, "no worker selected", f.Message)
	assert.Contains(t, f.Hints, "pass --worker <worker>, or run the command inside a LeapMux tab")
	assert.Empty(t, w.recorded())
}

// The Worker owns the length limits, so its refusal reaches the caller with
// its own words and code.
func TestNotifyOverIPC_ReportsTheWorkersRefusal(t *testing.T) {
	w := &privateCommandWorker{refusals: map[string]privateCommandRefusal{
		leapmuxv1connect.WorkerPrivateServiceNotifyProcedure: {codes.InvalidArgument, "title is 300 bytes long; the limit is 256 bytes"},
	}}
	startNotifyWorker(t, w)

	f := runControl(t, "notify", "A long title").requireFailure(t, cli.CodeInvalidArgument)
	assert.Contains(t, f.Message, "the limit is 256 bytes")
}

func TestNotificationClear_ValidatesAllSelectorsBeforeDialing(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		message string
	}{
		{"prefix followed by empty id", []string{"notification", "clear", "abcd", ""}, "the notification ID is empty"},
		{"prefix followed by bare id colon", []string{"notification", "clear", "abcd", "id:"}, `"id:" needs a notification ID after the colon`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := &privateCommandWorker{}
			hub := startNotifyWorker(t, w)

			f := runControl(t, tc.args...).requireFailure(t, cli.CodeUsage)
			assert.Contains(t, f.Message, tc.message)
			assert.Empty(t, w.recorded(), "invalid selectors must fail before contacting the Worker")
			assert.Empty(t, hub.called(), "invalid selectors must fail before contacting the Hub")
		})
	}
}

func TestNotificationClear_DeduplicatesResolvedIDsInOrder(t *testing.T) {
	w := &privateCommandWorker{answers: map[string]proto.Message{
		leapmuxv1connect.WorkerPrivateServiceListNotificationsProcedure: &leapmuxv1.ListNotificationsResponse{
			Notifications: []*leapmuxv1.UserNotification{
				{Id: "aaaa1111", Title: "One"},
				{Id: "bbbb2222", Title: "Two"},
			},
		},
		leapmuxv1connect.WorkerPrivateServiceClearNotificationProcedure: &leapmuxv1.ClearNotificationResponse{Cleared: true},
	}}
	startNotifyWorker(t, w)

	data := runControl(t, "notification", "clear", "aaaa", "aaaa1111", "id:bbbb", "id:aaaa1111").Data(t)
	assert.Equal(t, map[string]any{
		"worker_id": "worker-A",
		"cleared":   []any{"aaaa1111", "bbbb2222"},
	}, data)

	var clearedIDs []string
	for _, call := range w.recorded() {
		if call.method == leapmuxv1connect.WorkerPrivateServiceClearNotificationProcedure {
			clearedIDs = append(clearedIDs, call.request.(*leapmuxv1.ClearNotificationRequest).GetId())
		}
	}
	assert.ElementsMatch(t, []string{"aaaa1111", "bbbb2222"}, clearedIDs)
	assert.Len(t, clearedIDs, 2, "duplicate selectors must dispatch ClearNotification only once per ID")
}

type clearBatchDispatcher struct {
	onClear func(ctx context.Context, id string) (*leapmuxv1.ClearNotificationResponse, *privateCommandRefusal)
}

func (d *clearBatchDispatcher) DispatchWith(ctx context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
	if req.GetMethod() != leapmuxv1connect.WorkerPrivateServiceClearNotificationProcedure {
		_ = rw.SendError(int32(codes.Unimplemented), "unexpected method: "+req.GetMethod())
		return
	}
	var in leapmuxv1.ClearNotificationRequest
	if err := proto.Unmarshal(req.GetPayload(), &in); err != nil {
		_ = rw.SendError(int32(codes.InvalidArgument), err.Error())
		return
	}
	resp, refusal := d.onClear(ctx, in.GetId())
	if refusal != nil {
		_ = rw.SendError(int32(refusal.code), refusal.message)
		return
	}
	writeProtoResponse(rw, resp)
}

func TestNotificationClear_ReportsPartialClearsOnMidBatchFailure(t *testing.T) {
	// Submit detailFanoutLimit+1 full IDs so errgroup's limit guarantees at
	// least one successful ClearNotification completes before the failing ID
	// starts, while bypassing ListNotifications on the full-ID fast path.
	failID := fullTestID(fmt.Sprintf("notif-fail-%02d", detailFanoutLimit+1))
	args := []string{"notification", "clear"}
	for i := 1; i <= detailFanoutLimit; i++ {
		args = append(args, "id:"+fullTestID(fmt.Sprintf("notif-ok-%02d", i)))
	}
	args = append(args, "id:"+failID)

	disp := &clearBatchDispatcher{
		onClear: func(_ context.Context, noticeID string) (*leapmuxv1.ClearNotificationResponse, *privateCommandRefusal) {
			if noticeID == failID {
				return nil, &privateCommandRefusal{code: codes.Internal, message: "worker storage error"}
			}
			return &leapmuxv1.ClearNotificationResponse{Cleared: true}, nil
		},
	}
	startSpawnIPC(t, &recordingHub{workers: workerRows("worker-A")}, disp)
	t.Setenv("LEAPMUX_CONTROL_WORKER_ID", "worker-A")

	f := runControl(t, args...).requireFailure(t, cli.CodeInternal)
	assert.Contains(t, f.Message, "worker storage error")

	var partialHint string
	for _, h := range f.Hints {
		if strings.HasPrefix(h, "already cleared: ") {
			partialHint = h
			break
		}
	}
	require.NotEmpty(t, partialHint, "hints must report notifications already cleared before failure: %v", f.Hints)
	assert.Contains(t, partialHint, "notif-ok-")
	assert.NotContains(t, partialHint, failID)
}

func TestNotificationClear_DispatchesConcurrentlyAndPreservesOrder(t *testing.T) {
	var inFlight atomic.Int32
	concurrentObserved := make(chan struct{})
	var once sync.Once

	disp := &clearBatchDispatcher{
		onClear: func(ctx context.Context, _ string) (*leapmuxv1.ClearNotificationResponse, *privateCommandRefusal) {
			if inFlight.Add(1) >= 2 {
				once.Do(func() { close(concurrentObserved) })
			}
			select {
			case <-concurrentObserved:
			case <-ctx.Done():
				return nil, &privateCommandRefusal{code: codes.Canceled, message: ctx.Err().Error()}
			}
			return &leapmuxv1.ClearNotificationResponse{Cleared: true}, nil
		},
	}
	startSpawnIPC(t, &recordingHub{workers: workerRows("worker-A")}, disp)
	t.Setenv("LEAPMUX_CONTROL_WORKER_ID", "worker-A")

	id1, id2, id3, id4 := fullTestID("n-01"), fullTestID("n-02"), fullTestID("n-03"), fullTestID("n-04")
	data := runControl(t, "notification", "clear", "id:"+id1, "id:"+id2, "id:"+id3, "id:"+id4).Data(t)
	assert.Equal(t, map[string]any{
		"worker_id": "worker-A",
		"cleared":   []any{id1, id2, id3, id4},
	}, data)
}

func TestNotificationSelector_GeneratedIDAlphabetAndSortedAmbiguity(t *testing.T) {
	validFull := fullTestID("notif-1")
	val, fullID, err := parseNotificationSelector(validFull)
	require.NoError(t, err)
	assert.Equal(t, validFull, val)
	assert.True(t, fullID)

	val, fullID, err = parseNotificationSelector("id:" + validFull)
	require.NoError(t, err)
	assert.Equal(t, validFull, val)
	assert.True(t, fullID)

	val, fullID, err = parseNotificationSelector("id:abcd")
	require.NoError(t, err)
	assert.Equal(t, "abcd", val)
	assert.False(t, fullID, "a short id: prefix must use ListNotifications prefix resolution")

	invalidAlphabet := strings.Repeat("a", id.GeneratedLength-1) + "!"
	val, fullID, err = parseNotificationSelector(invalidAlphabet)
	require.NoError(t, err)
	assert.Equal(t, invalidAlphabet, val)
	assert.False(t, fullID, "a 48-byte string with non-ID characters must not skip prefix/list resolution")

	items := []*leapmuxv1.UserNotification{
		{Id: "abcd-z", Title: "Z"},
		{Id: "abcd-a", Title: "A"},
		{Id: "abcd-m", Title: "M"},
		{Id: "uniq-1", Title: "Unique"},
		{Id: "n1", Title: "Short"},
	}

	got, err := resolveNotificationID(items, "id:uniq")
	require.NoError(t, err)
	assert.Equal(t, "uniq-1", got)

	got, err = resolveNotificationID(items, "id:n1")
	require.NoError(t, err)
	assert.Equal(t, "n1", got)

	for _, raw := range []string{"abcd", "id:abcd"} {
		_, err = resolveNotificationID(items, raw)
		require.Error(t, err, raw)
		cliErr := cli.FromError(err)
		require.Equal(t, cli.CodeAmbiguousTarget, cliErr.Code, raw)
		require.Len(t, cliErr.Candidates, 3, raw)
		assert.Equal(t, []cli.Candidate{
			{ID: "abcd-a", Kind: "notification", Title: "A"},
			{ID: "abcd-m", Kind: "notification", Title: "M"},
			{ID: "abcd-z", Kind: "notification", Title: "Z"},
		}, cliErr.Candidates, raw)
	}
}

func TestResolveNotificationID_NotFoundAndShortPrefix(t *testing.T) {
	items := []*leapmuxv1.UserNotification{
		{Id: "abcd-1", Title: "One"},
	}

	for _, tc := range []struct {
		name string
		raw  string
	}{
		{name: "missing ID", raw: "zzzz-9"},
		{name: "three-character non-exact prefix", raw: "abc"},
		{name: "three-character non-exact id: prefix", raw: "id:abc"},
		{name: "title after id: does not match", raw: "id:One"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveNotificationID(items, tc.raw)
			require.Error(t, err)
			cliErr := cli.FromError(err)
			assert.Equal(t, cli.CodeNotFound, cliErr.Code)
			assert.Contains(t, cliErr.Message, fmt.Sprintf("no notification matches %q", tc.raw))
			assert.Contains(t, cliErr.Hints, `list recent notifications with "leapmux notification list"`)
		})
	}
}

func TestNotificationClear_EdgeCases(t *testing.T) {
	t.Run("single full id returning Cleared false skips ListNotifications", func(t *testing.T) {
		w := &privateCommandWorker{answers: map[string]proto.Message{
			leapmuxv1connect.WorkerPrivateServiceClearNotificationProcedure: &leapmuxv1.ClearNotificationResponse{Cleared: false},
		}}
		startNotifyWorker(t, w)

		missingID := fullTestID("missing-1")
		data := runControl(t, "notification", "clear", "id:"+missingID).Data(t)
		assert.Equal(t, map[string]any{
			"id":        missingID,
			"worker_id": "worker-A",
			"cleared":   false,
		}, data)

		calls := w.recorded()
		require.Len(t, calls, 1)
		assert.Equal(t, leapmuxv1connect.WorkerPrivateServiceClearNotificationProcedure, calls[0].method)
		assert.True(t, proto.Equal(&leapmuxv1.ClearNotificationRequest{Id: missingID}, calls[0].request))
	})

	t.Run("batch with mixed Cleared true and false reports only cleared IDs", func(t *testing.T) {
		id1, id2, id3 := fullTestID("n-01"), fullTestID("n-02"), fullTestID("n-03")
		disp := &clearBatchDispatcher{
			onClear: func(_ context.Context, noticeID string) (*leapmuxv1.ClearNotificationResponse, *privateCommandRefusal) {
				return &leapmuxv1.ClearNotificationResponse{Cleared: noticeID != id2}, nil
			},
		}
		startSpawnIPC(t, &recordingHub{workers: workerRows("worker-A")}, disp)
		t.Setenv("LEAPMUX_CONTROL_WORKER_ID", "worker-A")

		data := runControl(t, "notification", "clear", "id:"+id1, "id:"+id2, "id:"+id3).Data(t)
		assert.Equal(t, map[string]any{
			"worker_id": "worker-A",
			"cleared":   []any{id1, id3},
		}, data)
	})

	t.Run("failing batch with zero cleared notifications omits already cleared hint", func(t *testing.T) {
		id1, id2 := fullTestID("n-01"), fullTestID("n-02")
		disp := &clearBatchDispatcher{
			onClear: func(_ context.Context, noticeID string) (*leapmuxv1.ClearNotificationResponse, *privateCommandRefusal) {
				if noticeID == id1 {
					return &leapmuxv1.ClearNotificationResponse{Cleared: false}, nil
				}
				return nil, &privateCommandRefusal{code: codes.Internal, message: "worker storage error"}
			},
		}
		startSpawnIPC(t, &recordingHub{workers: workerRows("worker-A")}, disp)
		t.Setenv("LEAPMUX_CONTROL_WORKER_ID", "worker-A")

		f := runControl(t, "notification", "clear", "id:"+id1, "id:"+id2).requireFailure(t, cli.CodeInternal)
		assert.Contains(t, f.Message, "worker storage error")
		for _, hint := range f.Hints {
			assert.False(t, strings.HasPrefix(hint, "already cleared:"), "unexpected partial-clear hint: %v", f.Hints)
		}
	})

	t.Run("clear all with empty ListNotifications returns empty cleared array without ClearNotification calls", func(t *testing.T) {
		w := &privateCommandWorker{answers: map[string]proto.Message{
			leapmuxv1connect.WorkerPrivateServiceListNotificationsProcedure: &leapmuxv1.ListNotificationsResponse{},
		}}
		startNotifyWorker(t, w)

		out := runControl(t, "notification", "clear", "--all")
		assert.Contains(t, out.Stdout, `"cleared":[]`)
		assert.Equal(t, map[string]any{
			"worker_id": "worker-A",
			"cleared":   []any{},
		}, out.Data(t))

		calls := w.recorded()
		require.Len(t, calls, 1)
		assert.Equal(t, leapmuxv1connect.WorkerPrivateServiceListNotificationsProcedure, calls[0].method)
	})
}
