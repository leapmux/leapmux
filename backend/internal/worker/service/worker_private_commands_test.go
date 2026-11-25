package service

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// privateEventLog is one WatchWorkerPrivateEvents subscription of the
// worker's owner, dispatched the way a frontend opens it, so the events pass
// the visibility filter and the bootstrap that production runs.
type privateEventLog struct {
	writer *testResponseWriter
}

// watchPrivateEvents subscribes as user-1 and returns once the bus holds the
// subscriber, so an event published after it returns is never lost.
func watchPrivateEvents(t *testing.T, svc *Service, d testRPCDispatcher) *privateEventLog {
	t.Helper()
	w := newTestWriter()
	done := make(chan struct{})
	go func() {
		defer close(done)
		dispatch(d, leapmuxv1connect.WorkerPrivateServiceWatchWorkerPrivateEventsProcedure, &leapmuxv1.WatchWorkerPrivateEventsRequest{}, w)
	}()
	// Registered after setupTestService's own cleanups, so it runs first:
	// Stop releases the subscription, and the wait joins its goroutine.
	t.Cleanup(func() {
		svc.PrivateEvents.Stop()
		<-done
	})
	require.Eventually(t, func() bool {
		svc.PrivateEvents.mu.RLock()
		defer svc.PrivateEvents.mu.RUnlock()
		return len(svc.PrivateEvents.subscribers["user-1"]) == 1
	}, 30*time.Second, 5*time.Millisecond, "the subscription must register")
	return &privateEventLog{writer: w}
}

// events decodes every event that the subscription received so far.
func (l *privateEventLog) events(t *testing.T) []*leapmuxv1.WorkerPrivateEvent {
	t.Helper()
	var out []*leapmuxv1.WorkerPrivateEvent
	for _, frame := range l.writer.streamsSnapshot() {
		if len(frame.GetPayload()) == 0 {
			continue
		}
		var evt leapmuxv1.WorkerPrivateEvent
		require.NoError(t, proto.Unmarshal(frame.GetPayload(), &evt))
		out = append(out, &evt)
	}
	return out
}

// waitFor returns the events once the subscription received n of them.
func (l *privateEventLog) waitFor(t *testing.T, n int) []*leapmuxv1.WorkerPrivateEvent {
	t.Helper()
	require.Eventually(t, func() bool { return len(l.events(t)) >= n }, 30*time.Second, 5*time.Millisecond,
		"the subscription must receive %d events", n)
	return l.events(t)
}

// waitForSentinel publishes a reveal of sentinelTabID and returns every event
// before it, once it arrived. The bus keeps the order of one subscriber, so
// this proves that nothing else was published before the sentinel.
func (l *privateEventLog) waitForSentinel(t *testing.T, svc *Service) []*leapmuxv1.WorkerPrivateEvent {
	t.Helper()
	const sentinelTabID = "sentinel"
	svc.PrivateEvents.PublishTabRevealCommand(userid.MustNew("user-1"), leapmuxv1.TabType_TAB_TYPE_AGENT, sentinelTabID)
	var events []*leapmuxv1.WorkerPrivateEvent
	require.Eventually(t, func() bool {
		events = l.events(t)
		return len(events) > 0 && events[len(events)-1].GetTabRevealCommand().GetTabId() == sentinelTabID
	}, 30*time.Second, 5*time.Millisecond, "the sentinel must arrive")
	return events[:len(events)-1]
}

// dispatchFromTab dispatches as the worker's owner through the control token
// of tabID, as the local IPC router does for a process in that tab.
func dispatchFromTab(d testRPCDispatcher, tabID, method string, req proto.Message, w *testResponseWriter) {
	payload, err := proto.Marshal(req)
	if err != nil {
		panic(err)
	}
	caller := channel.LocalAgentCaller(userid.MustNew("user-1"))
	caller.SourceTabID = tabID
	dispatchTestRequest(d, context.Background(), caller, &leapmuxv1.InnerRpcRequest{Method: method, Payload: payload}, w)
}

// requireOK fails the test unless the handler answered with a response and no
// error.
func requireOK(t *testing.T, w *testResponseWriter) {
	t.Helper()
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
}

// requireRefused fails the test unless the handler answered with exactly one
// error of code, and returns its message.
func requireRefused(t *testing.T, w *testResponseWriter, code codes.Code) string {
	t.Helper()
	require.Len(t, w.errors, 1)
	assert.Equal(t, int32(code), w.errors[0].code, "message: %s", w.errors[0].message)
	assert.Empty(t, w.responses)
	return w.errors[0].message
}

func setTabStatus(d testRPCDispatcher, tabID string, status *leapmuxv1.TabStatus) *testResponseWriter {
	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerPrivateServiceSetTabStatusProcedure, &leapmuxv1.SetTabStatusRequest{TabId: tabID, Status: status}, w)
	return w
}

func revealTab(d testRPCDispatcher, tabID string) *testResponseWriter {
	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerPrivateServiceRevealTabProcedure, &leapmuxv1.RevealTabRequest{TabId: tabID}, w)
	return w
}

func (s *TabStatusStore) get(key ownedTabKey) *leapmuxv1.TabStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, statuses := range s.byOwner {
		if entries, ok := statuses.tabs[key]; ok {
			return latestTabStatus(entries)
		}
	}
	return nil
}

func progress(n uint32) *uint32 { return &n }

// The notification identifies the tab whose control token made the call, which
// the router stamps on the caller. The text is trimmed, so a trailing newline
// from a pipe does not reach the toast.
func TestNotify_ShowsTheNotificationWithTheCallersTab(t *testing.T) {
	t.Parallel()
	svc, d, _ := setupTestService(t)
	log := watchPrivateEvents(t, svc, d)

	w := newTestWriter()
	dispatchFromTab(d, "agent-1", leapmuxv1connect.WorkerPrivateServiceNotifyProcedure,
		&leapmuxv1.NotifyRequest{Title: "  Build finished\n", Body: "All 42 tests passed\n"}, w)
	requireOK(t, w)

	events := log.waitFor(t, 1)
	require.Len(t, events, 1)
	got := events[0].GetUserNotification()
	require.NotNil(t, got)
	assert.Equal(t, "Build finished", got.GetTitle())
	assert.Equal(t, "All 42 tests passed", got.GetBody())
	assert.Equal(t, "agent-1", got.GetSourceTabId())

	// The response states what the Worker published, so a client prints the
	// trimmed text without trimming it by a rule of its own.
	answer := decodeResponse(t, w, &leapmuxv1.NotifyResponse{})
	assert.Equal(t, "Build finished", answer.GetTitle())
	assert.Equal(t, "All 42 tests passed", answer.GetBody())
	assert.Len(t, answer.GetId(), 48)
	assert.Equal(t, answer.GetId(), got.GetId())
}

func TestNotify_TargetedRecordsFollowTheTabReadScope(t *testing.T) {
	t.Parallel()
	svc, dispatcher, _ := setupTestService(t)
	seedAgent(t, svc, "agent-1")
	seedTerminal(t, svc, "term-1")
	log := watchPrivateEvents(t, svc, dispatcher)
	notify := func(id string, kind leapmuxv1.TabType) *testResponseWriter {
		w := newTestWriter()
		dispatchFromTab(dispatcher, "term-1", leapmuxv1connect.WorkerPrivateServiceNotifyProcedure, &leapmuxv1.NotifyRequest{
			Title: "Build complete",
			Destination: &leapmuxv1.NotificationDestination{Target: &leapmuxv1.NotificationDestination_Tab{
				Tab: &leapmuxv1.TabIdent{TabId: id, TabType: kind},
			}},
		}, w)
		return w
	}
	requireRefused(t, notify("agent-1", leapmuxv1.TabType_TAB_TYPE_TERMINAL), codes.NotFound)
	agentNotice := decodeResponse(t, notify("agent-1", leapmuxv1.TabType_TAB_TYPE_AGENT), &leapmuxv1.NotifyResponse{})
	terminalNotice := decodeResponse(t, notify("term-1", leapmuxv1.TabType_TAB_TYPE_TERMINAL), &leapmuxv1.NotifyResponse{})
	require.NotEqual(t, agentNotice.GetId(), terminalNotice.GetId())

	list := func() []*leapmuxv1.UserNotification {
		w := newTestWriter()
		dispatchScoped(dispatcher, mustScopes("workspace:read agent:read"),
			leapmuxv1connect.WorkerPrivateServiceListNotificationsProcedure, &leapmuxv1.ListNotificationsRequest{}, w)
		return decodeResponse(t, w, &leapmuxv1.ListNotificationsResponse{}).GetNotifications()
	}
	visible := list()
	require.Len(t, visible, 1)
	assert.Equal(t, agentNotice.GetId(), visible[0].GetId())
	assert.Equal(t, "term-1", visible[0].GetSourceTabId())
	assert.Equal(t, "agent-1", visible[0].GetDestination().GetTab().GetTabId())

	request := &leapmuxv1.ClearNotificationRequest{Id: agentNotice.GetId()}
	denied := newTestWriter()
	dispatchAs(dispatcher, userid.MustNew("user-2"), leapmuxv1connect.WorkerPrivateServiceClearNotificationProcedure, request, denied)
	requireRefused(t, denied, codes.PermissionDenied)
	for _, want := range []bool{true, false} {
		w := newTestWriter()
		dispatch(dispatcher, leapmuxv1connect.WorkerPrivateServiceClearNotificationProcedure, request, w)
		assert.Equal(t, want, decodeResponse(t, w, &leapmuxv1.ClearNotificationResponse{}).GetCleared())
	}
	assert.Empty(t, list())
	events := log.waitForSentinel(t, svc)
	require.Len(t, events, 3)
	clear := events[2].GetUserNotificationCleared()
	require.NotNil(t, clear)
	assert.Equal(t, agentNotice.GetId(), clear.GetId())
	assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_AGENT, clear.GetDestination().GetTab().GetTabType())
	assert.False(t, privateEventVisible(channel.NewCaller(userid.MustNew("user-1"), mustScopes("workspace:read terminal:read")), events[2]))
}

func TestNotify_KeepsARecentListWithoutReplayingToasts(t *testing.T) {
	t.Parallel()
	svc, dispatcher, _ := setupTestService(t)
	owner := userid.MustNew("user-1")
	for i := range recentNotificationLimit + 1 {
		svc.publishNotification(owner, &leapmuxv1.UserNotification{Id: fmt.Sprint(i), Title: "Recent activity"})
	}
	w := newTestWriter()
	dispatch(dispatcher, leapmuxv1connect.WorkerPrivateServiceListNotificationsProcedure, &leapmuxv1.ListNotificationsRequest{}, w)
	notices := decodeResponse(t, w, &leapmuxv1.ListNotificationsResponse{}).GetNotifications()
	require.Len(t, notices, recentNotificationLimit)
	assert.Equal(t, fmt.Sprint(recentNotificationLimit), notices[0].GetId())
	assert.Equal(t, "1", notices[len(notices)-1].GetId())
	assert.False(t, svc.clearNotification(owner, "0"))
	assert.Empty(t, watchPrivateEvents(t, svc, dispatcher).waitForSentinel(t, svc))
}

// decodeResponse decodes the one response that the handler sent into into.
func decodeResponse[T proto.Message](t *testing.T, w *testResponseWriter, into T) T {
	t.Helper()
	requireOK(t, w)
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), into))
	return into
}

// A caller in no tab (the browser, the CLI on a laptop) states no source, and
// the body stays optional.
func TestNotify_StampsNoTabForACallerInNoTab(t *testing.T) {
	t.Parallel()
	svc, d, w := setupTestService(t)
	log := watchPrivateEvents(t, svc, d)

	dispatch(d, leapmuxv1connect.WorkerPrivateServiceNotifyProcedure, &leapmuxv1.NotifyRequest{Title: "Deploy started"}, w)
	requireOK(t, w)

	got := log.waitFor(t, 1)[0].GetUserNotification()
	assert.Equal(t, "Deploy started", got.GetTitle())
	assert.Empty(t, got.GetBody())
	assert.Empty(t, got.GetSourceTabId())
}

func TestNotify_RefusesAnInvalidNotification(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		req     *leapmuxv1.NotifyRequest
		message string
	}{
		{"an empty title", &leapmuxv1.NotifyRequest{Body: "text"}, "title is required"},
		{"a title of spaces", &leapmuxv1.NotifyRequest{Title: " \n\t "}, "title is required"},
		{"a title over the limit", &leapmuxv1.NotifyRequest{Title: strings.Repeat("t", notificationTitleMaxBytes+1)}, "the limit is 256 bytes"},
		{"a body over the limit", &leapmuxv1.NotifyRequest{Title: "t", Body: strings.Repeat("b", notificationBodyMaxBytes+1)}, "the limit is 4096 bytes"},
		// Bytes, not characters: 86 three-byte characters are 258 bytes.
		{"a multibyte title over the limit", &leapmuxv1.NotifyRequest{Title: strings.Repeat("한", 86)}, "title is 258 bytes long"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc, d, w := setupTestService(t)
			log := watchPrivateEvents(t, svc, d)

			dispatch(d, leapmuxv1connect.WorkerPrivateServiceNotifyProcedure, tc.req, w)
			assert.Contains(t, requireRefused(t, w, codes.InvalidArgument), tc.message)
			assert.Empty(t, log.waitForSentinel(t, svc), "a refused notification reaches no frontend")
		})
	}
}

// The limits are inclusive: a text of exactly the limit is accepted.
func TestNotify_AcceptsATitleAndABodyOfExactlyTheLimit(t *testing.T) {
	t.Parallel()
	svc, d, w := setupTestService(t)
	log := watchPrivateEvents(t, svc, d)

	title := strings.Repeat("t", notificationTitleMaxBytes)
	body := strings.Repeat("b", notificationBodyMaxBytes)
	dispatch(d, leapmuxv1connect.WorkerPrivateServiceNotifyProcedure, &leapmuxv1.NotifyRequest{Title: title, Body: body}, w)
	requireOK(t, w)

	got := log.waitFor(t, 1)[0].GetUserNotification()
	assert.Equal(t, title, got.GetTitle())
	assert.Equal(t, body, got.GetBody())
}

// The limits measure the trimmed text, which is the text that the frontends
// show. A title and a body of exactly the limit pass with the white space that
// a pipe or a heredoc adds around them.
func TestNotify_MeasuresTheLimitsAfterTheTrim(t *testing.T) {
	t.Parallel()
	svc, d, w := setupTestService(t)
	log := watchPrivateEvents(t, svc, d)

	title := strings.Repeat("t", notificationTitleMaxBytes)
	body := strings.Repeat("b", notificationBodyMaxBytes)
	dispatch(d, leapmuxv1connect.WorkerPrivateServiceNotifyProcedure, &leapmuxv1.NotifyRequest{Title: "  " + title + "\n", Body: "\n" + body + " \n"}, w)
	requireOK(t, w)

	got := log.waitFor(t, 1)[0].GetUserNotification()
	assert.Equal(t, title, got.GetTitle())
	assert.Equal(t, body, got.GetBody())
}

// The notification goes to the caller's subscribers only. The Worker's owner
// gate already refuses another user's call, so this pins the bus side: a
// subscriber of another user hears nothing.
func TestNotify_ReachesOnlyTheCallersSubscribers(t *testing.T) {
	t.Parallel()
	svc, d, w := setupTestService(t)
	mine := watchPrivateEvents(t, svc, d)

	others := make(chan *leapmuxv1.WorkerPrivateEvent, 4)
	registered := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = svc.PrivateEvents.SnapshotAndSubscribe(ctx, userid.MustNew("user-2"),
			func(userid.UserID) []*leapmuxv1.WorkerPrivateEvent {
				close(registered)
				return nil
			},
			func(evt *leapmuxv1.WorkerPrivateEvent) error {
				others <- evt
				return nil
			})
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	<-registered

	dispatch(d, leapmuxv1connect.WorkerPrivateServiceNotifyProcedure, &leapmuxv1.NotifyRequest{Title: "Mine"}, w)
	requireOK(t, w)
	assert.Equal(t, "Mine", mine.waitFor(t, 1)[0].GetUserNotification().GetTitle())

	svc.PrivateEvents.PublishTabRevealCommand(userid.MustNew("user-2"), leapmuxv1.TabType_TAB_TYPE_AGENT, "sentinel")
	select {
	case evt := <-others:
		assert.Equal(t, "sentinel", evt.GetTabRevealCommand().GetTabId(),
			"the other user's first event is the sentinel, so the notification never reached them")
	case <-time.After(30 * time.Second):
		t.Fatal("the other user's sentinel never arrived")
	}
}

// Every tab kind that this Worker hosts can be revealed.
func TestRevealTab_RevealsEachKindOfHostedTab(t *testing.T) {
	t.Parallel()
	svc, d, _ := setupTestService(t)
	seedAgent(t, svc, "agent-1")
	seedTerminal(t, svc, "term-1")
	createFileTabForPath(t, svc, "user-1", "file-1", t.TempDir(), "notes.txt")
	log := watchPrivateEvents(t, svc, d)

	for _, tabID := range []string{"agent-1", "term-1", "file-1"} {
		requireOK(t, revealTab(d, tabID))
	}

	// The bootstrap also replays the file tab's payload, so only the reveals
	// count here. Each reveal states the kind of its tab, which decides the
	// subscribers that may see it.
	type reveal struct {
		tabID   string
		tabType leapmuxv1.TabType
	}
	var revealed []reveal
	for _, evt := range log.waitForSentinel(t, svc) {
		if got := evt.GetTabRevealCommand(); got != nil {
			revealed = append(revealed, reveal{got.GetTabId(), got.GetTabType()})
		}
	}
	assert.Equal(t, []reveal{
		{"agent-1", leapmuxv1.TabType_TAB_TYPE_AGENT},
		{"term-1", leapmuxv1.TabType_TAB_TYPE_TERMINAL},
		{"file-1", leapmuxv1.TabType_TAB_TYPE_FILE},
	}, revealed)
}

// A tab that this Worker does not host fails instead of answering success for
// a command that no frontend can act on, and no event is published.
func TestRevealTab_RefusesATabThatTheWorkerDoesNotHost(t *testing.T) {
	t.Parallel()
	svc, d, _ := setupTestService(t)
	ctx := context.Background()
	seedAgent(t, svc, "closed-agent")
	require.NoError(t, closeErr(svc.Queries.CloseAgent(ctx, "closed-agent")))
	seedTerminal(t, svc, "closed-term")
	require.NoError(t, closeErr(svc.Queries.CloseTerminal(ctx, "closed-term")))
	_, created := svc.QuakeTerminals.RegisterOrAdopt(QuakeTerminal{ID: "quake-1", WorkingDir: t.TempDir()})
	require.True(t, created)
	// Another user's file tab: a FILE id is unique only within a user.
	createFileTabForPath(t, svc, "user-2", "their-file", t.TempDir(), "theirs.txt")
	log := watchPrivateEvents(t, svc, d)

	for _, tabID := range []string{"no-such-tab", "closed-agent", "closed-term", "quake-1", "their-file"} {
		t.Run(tabID, func(t *testing.T) {
			assert.Contains(t, requireRefused(t, revealTab(d, tabID), codes.NotFound), "tab not found")
		})
	}
	assert.Empty(t, log.waitForSentinel(t, svc), "a refused reveal reaches no frontend")
}

func TestRevealTab_RefusesAnEmptyTabID(t *testing.T) {
	t.Parallel()
	_, d, _ := setupTestService(t)
	assert.Contains(t, requireRefused(t, revealTab(d, ""), codes.InvalidArgument), "tab_id is required")
}

// A set stores the validated status and publishes it; a clear removes it and
// publishes an unset status.
func TestSetTabStatus_SetsAndClearsTheStatus(t *testing.T) {
	t.Parallel()
	svc, d, _ := setupTestService(t)
	seedAgent(t, svc, "agent-1")
	log := watchPrivateEvents(t, svc, d)
	key := newOwnedTabKey(leapmuxv1.TabType_TAB_TYPE_AGENT, "agent-1", "user-1")

	setAnswer := decodeResponse(t, setTabStatus(d, "agent-1", &leapmuxv1.TabStatus{
		Text:            "  Running the tests\n",
		ProgressPercent: progress(40),
		Tone:            leapmuxv1.TabStatusTone_TAB_STATUS_TONE_WARNING,
	}), &leapmuxv1.SetTabStatusResponse{})
	stored := svc.TabStatuses.get(key)
	require.NotNil(t, stored)
	assert.Equal(t, "Running the tests", stored.GetText(), "the text is trimmed")
	assert.Equal(t, uint32(40), stored.GetProgressPercent())
	assert.Equal(t, leapmuxv1.TabStatusTone_TAB_STATUS_TONE_WARNING, stored.GetTone())
	assert.True(t, proto.Equal(stored, setAnswer.GetStatus()), "the response states the stored status")

	clearAnswer := decodeResponse(t, setTabStatus(d, "agent-1", nil), &leapmuxv1.SetTabStatusResponse{})
	assert.Nil(t, svc.TabStatuses.get(key))
	assert.Nil(t, clearAnswer.GetStatus(), "the response of a clear states no status")

	events := log.waitFor(t, 2)
	require.Len(t, events, 2)
	set := events[0].GetTabStatusChanged()
	assert.Equal(t, "agent-1", set.GetTabId())
	assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_AGENT, set.GetTabType(), "the event states the kind of the tab")
	assert.True(t, proto.Equal(stored, set.GetStatus()), "the event carries the stored status")
	cleared := events[1].GetTabStatusChanged()
	assert.Equal(t, "agent-1", cleared.GetTabId())
	assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_AGENT, cleared.GetTabType())
	assert.Nil(t, cleared.GetStatus(), "a cleared status is unset")
}

func TestListTabStatuses_RequiresTheOwnersTabReadScope(t *testing.T) {
	t.Parallel()
	svc, dispatcher, _ := setupTestService(t)
	seedAgent(t, svc, "agent-1")
	requireOK(t, setTabStatus(dispatcher, "agent-1", &leapmuxv1.TabStatus{Text: "Running"}))
	request := &leapmuxv1.ListTabStatusesRequest{TabId: "agent-1"}
	method := leapmuxv1connect.WorkerPrivateServiceListTabStatusesProcedure

	wrongOwner := newTestWriter()
	dispatchAs(dispatcher, userid.MustNew("user-2"), method, request, wrongOwner)
	requireRefused(t, wrongOwner, codes.PermissionDenied)
	for _, scopes := range []string{"workspace:read", "agent:read", "workspace:read terminal:read"} {
		missing := newTestWriter()
		dispatchScoped(dispatcher, mustScopes(scopes), method, request, missing)
		requireRefused(t, missing, codes.PermissionDenied)
	}
	allowed := newTestWriter()
	dispatchScoped(dispatcher, mustScopes("workspace:read agent:read"), method, request, allowed)
	answer := decodeResponse(t, allowed, &leapmuxv1.ListTabStatusesResponse{})
	require.Len(t, answer.GetStatuses(), 1)
	assert.Equal(t, defaultTabStatusKey, answer.GetStatuses()[0].GetKey())
	assert.Equal(t, "Running", answer.GetStatuses()[0].GetStatus().GetText())
}

func TestSetTabStatus_ValidatesAndNormalizesTheKey(t *testing.T) {
	t.Parallel()
	svc, dispatcher, _ := setupTestService(t)
	seedAgent(t, svc, "agent-1")
	set := func(key string, status *leapmuxv1.TabStatus) *testResponseWriter {
		w := newTestWriter()
		dispatch(dispatcher, leapmuxv1connect.WorkerPrivateServiceSetTabStatusProcedure,
			&leapmuxv1.SetTabStatusRequest{TabId: "agent-1", Key: &key, Status: status}, w)
		return w
	}
	status := &leapmuxv1.TabStatus{Text: "Checking the build"}
	for _, key := range []string{"", "  ", strings.Repeat("a", tabStatusKeyMaxBytes+1), "build\x1bkey"} {
		requireRefused(t, set(key, status), codes.InvalidArgument)
		requireRefused(t, set(key, nil), codes.InvalidArgument)
	}
	scope := newOwnedTabKey(leapmuxv1.TabType_TAB_TYPE_AGENT, "agent-1", "user-1")
	owner := userid.MustNew("user-1")
	require.Empty(t, svc.TabStatuses.list(scope, owner))
	allowed := strings.Repeat("a", tabStatusKeyMaxBytes)
	response := decodeResponse(t, set("  "+allowed+" ", status), &leapmuxv1.SetTabStatusResponse{})
	assert.Equal(t, allowed, response.GetKey())
	require.Len(t, svc.TabStatuses.list(scope, owner), 1)
	cleared := decodeResponse(t, set(allowed, nil), &leapmuxv1.SetTabStatusResponse{})
	assert.Equal(t, allowed, cleared.GetKey())
	assert.Empty(t, svc.TabStatuses.list(scope, owner))
}

// A status without a progress keeps it unset, so the frontend draws no bar.
// Progress 0 and 100 are set values, at both ends of the range.
func TestSetTabStatus_KeepsTheProgressPresence(t *testing.T) {
	t.Parallel()
	svc, d, _ := setupTestService(t)
	seedTerminal(t, svc, "term-1")
	key := newOwnedTabKey(leapmuxv1.TabType_TAB_TYPE_TERMINAL, "term-1", "user-1")

	requireOK(t, setTabStatus(d, "term-1", &leapmuxv1.TabStatus{Text: "Waiting"}))
	assert.Nil(t, svc.TabStatuses.get(key).ProgressPercent)
	assert.Equal(t, leapmuxv1.TabStatusTone_TAB_STATUS_TONE_UNSPECIFIED, svc.TabStatuses.get(key).GetTone(), "an unset tone is neutral")

	for _, value := range []uint32{0, tabStatusMaxProgress} {
		requireOK(t, setTabStatus(d, "term-1", &leapmuxv1.TabStatus{Text: "Working", ProgressPercent: progress(value)}))
		got := svc.TabStatuses.get(key).ProgressPercent
		require.NotNil(t, got)
		assert.Equal(t, value, *got)
	}
}

func TestSetTabStatus_RefusesAnInvalidStatus(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		status  *leapmuxv1.TabStatus
		message string
	}{
		{"an empty status", &leapmuxv1.TabStatus{}, "status.text is required"},
		{"a text of spaces", &leapmuxv1.TabStatus{Text: "  \n "}, "status.text is required"},
		{"a text over the limit", &leapmuxv1.TabStatus{Text: strings.Repeat("x", tabStatusTextMaxBytes+1)}, "the limit is 128 bytes"},
		{"a progress over 100", &leapmuxv1.TabStatus{Text: "x", ProgressPercent: progress(tabStatusMaxProgress + 1)}, "from 0 to 100"},
		{"the largest progress", &leapmuxv1.TabStatus{Text: "x", ProgressPercent: progress(math.MaxUint32)}, "from 0 to 100"},
		{"an unknown tone", &leapmuxv1.TabStatus{Text: "x", Tone: leapmuxv1.TabStatusTone(99)}, "tone 99"},
		// An open enum carries a negative number too.
		{"a negative tone", &leapmuxv1.TabStatus{Text: "x", Tone: leapmuxv1.TabStatusTone(-1)}, "tone -1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc, d, _ := setupTestService(t)
			seedAgent(t, svc, "agent-1")
			log := watchPrivateEvents(t, svc, d)

			assert.Contains(t, requireRefused(t, setTabStatus(d, "agent-1", tc.status), codes.InvalidArgument), tc.message)
			assert.Nil(t, svc.TabStatuses.get(newOwnedTabKey(leapmuxv1.TabType_TAB_TYPE_AGENT, "agent-1", "user-1")))
			assert.Empty(t, log.waitForSentinel(t, svc), "a refused status reaches no frontend")
		})
	}
}

// The request is checked before the tab, so an invalid status fails the same
// way for a tab that does not exist.
func TestSetTabStatus_ChecksTheStatusBeforeTheTab(t *testing.T) {
	t.Parallel()
	_, d, _ := setupTestService(t)
	assert.Contains(t, requireRefused(t, setTabStatus(d, "no-such-tab", &leapmuxv1.TabStatus{}), codes.InvalidArgument), "status.text is required")
}

func TestSetTabStatus_AcceptsATextOfExactlyTheLimit(t *testing.T) {
	t.Parallel()
	svc, d, _ := setupTestService(t)
	seedAgent(t, svc, "agent-1")
	text := strings.Repeat("x", tabStatusTextMaxBytes)

	requireOK(t, setTabStatus(d, "agent-1", &leapmuxv1.TabStatus{Text: text}))
	assert.Equal(t, text, svc.TabStatuses.get(newOwnedTabKey(leapmuxv1.TabType_TAB_TYPE_AGENT, "agent-1", "user-1")).GetText())

	// The limit measures the trimmed text, which is the text that the
	// frontends show.
	requireOK(t, setTabStatus(d, "agent-1", &leapmuxv1.TabStatus{Text: "\t" + text + " \n"}))
	assert.Equal(t, text, svc.TabStatuses.get(newOwnedTabKey(leapmuxv1.TabType_TAB_TYPE_AGENT, "agent-1", "user-1")).GetText())
}

// Each tone that the proto declares passes and keeps its value, the last one
// included.
func TestSetTabStatus_AcceptsEveryToneThatTheWorkerKnows(t *testing.T) {
	t.Parallel()
	svc, d, _ := setupTestService(t)
	seedAgent(t, svc, "agent-1")
	key := newOwnedTabKey(leapmuxv1.TabType_TAB_TYPE_AGENT, "agent-1", "user-1")

	require.Contains(t, leapmuxv1.TabStatusTone_name, int32(leapmuxv1.TabStatusTone_TAB_STATUS_TONE_ERROR), "precondition: the loop reaches the last tone")
	for number := range leapmuxv1.TabStatusTone_name {
		tone := leapmuxv1.TabStatusTone(number)
		requireOK(t, setTabStatus(d, "agent-1", &leapmuxv1.TabStatus{Text: "Working", Tone: tone}))
		assert.Equal(t, tone, svc.TabStatuses.get(key).GetTone())
	}
}

// The Worker stores a new message, not the request's. A field that a newer
// client added reaches the Worker as an unknown field, and it must not reach
// the frontends, which no check of this Worker vouches for.
func TestSetTabStatus_DropsTheFieldsThatTheWorkerDoesNotKnow(t *testing.T) {
	t.Parallel()
	svc, d, _ := setupTestService(t)
	seedAgent(t, svc, "agent-1")
	log := watchPrivateEvents(t, svc, d)

	status := &leapmuxv1.TabStatus{Text: "Running", ProgressPercent: progress(10)}
	status.ProtoReflect().SetUnknown(protowire.AppendVarint(protowire.AppendTag(nil, 99, protowire.VarintType), 7))
	answer := decodeResponse(t, setTabStatus(d, "agent-1", status), &leapmuxv1.SetTabStatusResponse{})

	stored := svc.TabStatuses.get(newOwnedTabKey(leapmuxv1.TabType_TAB_TYPE_AGENT, "agent-1", "user-1"))
	require.NotNil(t, stored)
	assert.Empty(t, stored.ProtoReflect().GetUnknown(), "the stored status holds no unknown field")
	assert.Equal(t, "Running", stored.GetText())
	assert.Equal(t, uint32(10), stored.GetProgressPercent())
	assert.Empty(t, answer.GetStatus().ProtoReflect().GetUnknown(), "the response holds no unknown field")
	published := log.waitFor(t, 1)[0].GetTabStatusChanged().GetStatus()
	require.NotNil(t, published)
	assert.Empty(t, published.ProtoReflect().GetUnknown(), "the event holds no unknown field")
}

func faultHostedAgentReads() (setupOption, *atomic.Bool) {
	var faulted atomic.Bool
	return withQueryRewrite(func(query string) string {
		if faulted.Load() && strings.HasPrefix(query, "-- name: CanWatchAgentHistory :one") {
			return "SELECT EXISTS (SELECT 1 FROM no_such_table WHERE id = ?)"
		}
		return query
	}), &faulted
}

// A failure to read the tab is not a tab that the Worker does not host: the
// caller gets INTERNAL, which it may retry, and not NOT_FOUND. Nothing is
// stored, and nothing reaches the frontends.
func TestWorkerPrivateCommands_ReportAFailedTabLookupAsInternal(t *testing.T) {
	t.Parallel()
	for name, send := range map[string]func(d testRPCDispatcher) *testResponseWriter{
		"reveal": func(d testRPCDispatcher) *testResponseWriter { return revealTab(d, "agent-1") },
		"status": func(d testRPCDispatcher) *testResponseWriter {
			return setTabStatus(d, "agent-1", &leapmuxv1.TabStatus{Text: "Running"})
		},
		"clear": func(d testRPCDispatcher) *testResponseWriter { return setTabStatus(d, "agent-1", nil) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			option, faulted := faultHostedAgentReads()
			svc, d, _ := setupTestService(t, option)
			seedAgent(t, svc, "agent-1")
			log := watchPrivateEvents(t, svc, d)

			faulted.Store(true)
			w := send(d)
			faulted.Store(false)

			assert.Equal(t, "failed to look up the tab", requireRefused(t, w, codes.Internal))
			assert.Nil(t, svc.TabStatuses.get(newOwnedTabKey(leapmuxv1.TabType_TAB_TYPE_AGENT, "agent-1", "user-1")))
			assert.Empty(t, log.waitForSentinel(t, svc), "a failed lookup reaches no frontend")
		})
	}
}

// The second read of the tab, after the set (triggered when another tab closes
// during the call), can fail to read. That failure does not say that the tab
// closed, so the status stays: it already reached the frontends, and the tab's
// own close clears it if the tab is gone.
func TestSetTabStatus_KeepsTheStatusWhenTheSecondReadFails(t *testing.T) {
	t.Parallel()
	option, faulted := faultHostedAgentReads()
	svc, d, _ := setupTestService(t, option)
	seedAgent(t, svc, "agent-1")
	log := watchPrivateEvents(t, svc, d)
	svc.beforeTabStatusSetFn = func() {
		svc.TabStatuses.forgetClosed(newOwnedTabKey(leapmuxv1.TabType_TAB_TYPE_AGENT, "other-agent", ""))
		faulted.Store(true)
	}

	w := setTabStatus(d, "agent-1", &leapmuxv1.TabStatus{Text: "Running"})
	faulted.Store(false)

	answer := decodeResponse(t, w, &leapmuxv1.SetTabStatusResponse{})
	assert.Equal(t, "Running", answer.GetStatus().GetText())
	stored := svc.TabStatuses.get(newOwnedTabKey(leapmuxv1.TabType_TAB_TYPE_AGENT, "agent-1", "user-1"))
	require.NotNil(t, stored, "the status stays")
	assert.Equal(t, "Running", stored.GetText())
	events := log.waitForSentinel(t, svc)
	require.Len(t, events, 1, "the frontends hear the set and no clear")
	assert.Equal(t, "Running", events[0].GetTabStatusChanged().GetStatus().GetText())
}

// When no tab closes between hostedTabKey and TabStatuses.set, the closeEpoch
// fast-path skips the second SQLite liveness check completely.
func TestSetTabStatus_SkipsSecondDBReadWhenNoTabClosed(t *testing.T) {
	t.Parallel()
	var reads atomic.Int64
	option := withQueryRewrite(func(query string) string {
		if strings.HasPrefix(query, "-- name: CanWatchAgentHistory :one") {
			reads.Add(1)
		}
		return query
	})
	svc, d, _ := setupTestService(t, option)
	seedAgent(t, svc, "agent-1")
	reads.Store(0)

	w := setTabStatus(d, "agent-1", &leapmuxv1.TabStatus{Text: "Running"})
	requireOK(t, w)
	assert.Equal(t, int64(1), reads.Load(), "common-path SetTabStatus must run only one CanWatchAgentHistory read")
}

func TestSetTabStatus_RefusesATabThatTheWorkerDoesNotHost(t *testing.T) {
	t.Parallel()
	svc, d, _ := setupTestService(t)
	ctx := context.Background()
	seedAgent(t, svc, "closed-agent")
	require.NoError(t, closeErr(svc.Queries.CloseAgent(ctx, "closed-agent")))
	_, created := svc.QuakeTerminals.RegisterOrAdopt(QuakeTerminal{ID: "quake-1", WorkingDir: t.TempDir()})
	require.True(t, created)
	status := &leapmuxv1.TabStatus{Text: "Running"}

	for _, tabID := range []string{"no-such-tab", "closed-agent", "quake-1"} {
		t.Run(tabID, func(t *testing.T) {
			assert.Contains(t, requireRefused(t, setTabStatus(d, tabID, status), codes.NotFound), "tab not found")
			assert.Contains(t, requireRefused(t, setTabStatus(d, tabID, nil), codes.NotFound), "tab not found",
				"a clear refuses the same tabs as a set")
		})
	}
	assert.Empty(t, svc.TabStatuses.snapshotFor(userid.MustNew("user-1")))
}

func TestSetTabStatus_RefusesAnEmptyTabID(t *testing.T) {
	t.Parallel()
	_, d, _ := setupTestService(t)
	assert.Contains(t, requireRefused(t, setTabStatus(d, "", &leapmuxv1.TabStatus{Text: "x"}), codes.InvalidArgument), "tab_id is required")
}

// A clear publishes even when no status is set: a frontend that missed an
// earlier event (the bus drops one for a slow subscriber) agrees again.
func TestSetTabStatus_PublishesAClearOfAnUnsetStatus(t *testing.T) {
	t.Parallel()
	svc, d, _ := setupTestService(t)
	seedAgent(t, svc, "agent-1")
	log := watchPrivateEvents(t, svc, d)

	requireOK(t, setTabStatus(d, "agent-1", nil))

	got := log.waitFor(t, 1)[0].GetTabStatusChanged()
	assert.Equal(t, "agent-1", got.GetTabId())
	assert.Nil(t, got.GetStatus())
}

// The bootstrap replays each status that is set, in tab id order, and none
// of the commands: a reconnect must not show an old notification or move the
// user to another tab.
func TestWatchWorkerPrivateEvents_ReplaysEachSetStatus(t *testing.T) {
	t.Parallel()
	svc, d, _ := setupTestService(t)
	seedAgent(t, svc, "agent-b")
	seedTerminal(t, svc, "term-a")
	seedAgent(t, svc, "agent-cleared")
	requireOK(t, setTabStatus(d, "agent-b", &leapmuxv1.TabStatus{Text: "Reviewing", Tone: leapmuxv1.TabStatusTone_TAB_STATUS_TONE_SUCCESS}))
	requireOK(t, setTabStatus(d, "term-a", &leapmuxv1.TabStatus{Text: "Building", ProgressPercent: progress(75)}))
	requireOK(t, setTabStatus(d, "agent-cleared", &leapmuxv1.TabStatus{Text: "Gone soon"}))
	requireOK(t, setTabStatus(d, "agent-cleared", nil))
	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerPrivateServiceNotifyProcedure, &leapmuxv1.NotifyRequest{Title: "Old news"}, w)
	requireOK(t, w)
	requireOK(t, revealTab(d, "agent-b"))

	log := watchPrivateEvents(t, svc, d)

	events := log.waitForSentinel(t, svc)
	require.Len(t, events, 2, "the bootstrap holds the two set statuses and nothing else")
	first := events[0].GetTabStatusChanged()
	assert.Equal(t, "agent-b", first.GetTabId())
	assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_AGENT, first.GetTabType(), "a replayed status states the kind of its tab")
	assert.Equal(t, "Reviewing", first.GetStatus().GetText())
	assert.Equal(t, leapmuxv1.TabStatusTone_TAB_STATUS_TONE_SUCCESS, first.GetStatus().GetTone())
	second := events[1].GetTabStatusChanged()
	assert.Equal(t, "term-a", second.GetTabId())
	assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_TERMINAL, second.GetTabType())
	assert.Equal(t, uint32(75), second.GetStatus().GetProgressPercent())
}

// The statuses live in memory, so a database that cannot answer the payload
// read must not hide them from the bootstrap.
func TestPrivateEventsSnapshot_KeepsTheStatusesWhenThePayloadReadFails(t *testing.T) {
	t.Parallel()
	svc, d, _ := setupTestService(t)
	seedAgent(t, svc, "agent-1")
	requireOK(t, setTabStatus(d, "agent-1", &leapmuxv1.TabStatus{Text: "Running"}))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	events := svc.privateEventsSnapshot(ctx, userid.MustNew("user-1"))

	require.Len(t, events, 1)
	assert.Equal(t, "agent-1", events[0].GetTabStatusChanged().GetTabId())
}

// Each kind of tab close clears the tab's status and tells the frontends.
func TestCloseTab_ClearsTheTabStatus(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		seed  func(t *testing.T, svc *Service)
		tabID string
		ident *leapmuxv1.TabIdent
		key   ownedTabKey
	}{
		{
			name:  "agent",
			seed:  func(t *testing.T, svc *Service) { seedAgent(t, svc, "agent-1") },
			tabID: "agent-1",
			ident: agentTabIdent("agent-1"),
			key:   newOwnedTabKey(leapmuxv1.TabType_TAB_TYPE_AGENT, "agent-1", ""),
		},
		{
			name:  "terminal",
			seed:  func(t *testing.T, svc *Service) { seedTerminal(t, svc, "term-1") },
			tabID: "term-1",
			ident: terminalTabIdent("term-1"),
			key:   newOwnedTabKey(leapmuxv1.TabType_TAB_TYPE_TERMINAL, "term-1", ""),
		},
		{
			name: "file",
			seed: func(t *testing.T, svc *Service) {
				createFileTabForPath(t, svc, "user-1", "file-1", t.TempDir(), "notes.txt")
			},
			tabID: "file-1",
			ident: fileTabIdent("file-1"),
			key:   newOwnedTabKey(leapmuxv1.TabType_TAB_TYPE_FILE, "file-1", "user-1"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc, d, _ := setupTestService(t)
			defer drainAllInFlight(svc)
			tc.seed(t, svc)
			log := watchPrivateEvents(t, svc, d)
			requireOK(t, setTabStatus(d, tc.tabID, &leapmuxv1.TabStatus{Text: "Running"}))
			require.NotNil(t, svc.TabStatuses.get(tc.key))

			w := newTestWriter()
			dispatch(d, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, &leapmuxv1.CloseTabRequest{Tab: tc.ident}, w)
			require.Empty(t, w.errors)

			assert.Nil(t, svc.TabStatuses.get(tc.key), "the status goes with the tab")
			var cleared bool
			for _, evt := range log.waitForSentinel(t, svc) {
				if changed := evt.GetTabStatusChanged(); changed != nil && changed.GetTabId() == tc.tabID && changed.GetStatus() == nil {
					cleared = true
				}
			}
			assert.True(t, cleared, "the frontends hear that the status was cleared")
		})
	}
}

// Closing a root agent retires its subagent tabs, and their statuses go too.
// Closing a subagent tab alone hides it while its row stays open, and its
// status goes as well, with the statuses of its own subagent tabs.
func TestCloseTab_ClearsTheStatusesOfSubagentTabs(t *testing.T) {
	t.Parallel()
	svc, d, _ := setupTestService(t)
	defer drainAllInFlight(svc)
	ctx := context.Background()
	seedAgent(t, svc, "root")
	for _, child := range []string{"child-1", "child-2"} {
		require.NoError(t, svc.Queries.CreateChildAgent(ctx, db.CreateChildAgentParams{
			ID: child, ParentAgentID: sql.NullString{String: "root", Valid: true}, SpawnSpanID: "span-" + child,
			WorkingDir: t.TempDir(), Title: child, AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		}))
	}
	// A subagent of child-1, whose tab goes with child-1's tab.
	require.NoError(t, svc.Queries.CreateChildAgent(ctx, db.CreateChildAgentParams{
		ID: "grandchild-1", ParentAgentID: sql.NullString{String: "child-1", Valid: true}, SpawnSpanID: "span-grandchild-1",
		WorkingDir: t.TempDir(), Title: "grandchild-1", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	keyOf := func(id string) ownedTabKey { return newOwnedTabKey(leapmuxv1.TabType_TAB_TYPE_AGENT, id, "") }
	for _, id := range []string{"root", "child-1", "child-2", "grandchild-1"} {
		requireOK(t, setTabStatus(d, id, &leapmuxv1.TabStatus{Text: "Working on " + id}))
	}

	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, &leapmuxv1.CloseTabRequest{Tab: agentTabIdent("child-1")}, w)
	require.Empty(t, w.errors)
	assert.Nil(t, svc.TabStatuses.get(keyOf("child-1")), "a subagent tab's close clears its status")
	assert.Nil(t, svc.TabStatuses.get(keyOf("grandchild-1")), "and the statuses of the subagent tabs that go with it")
	assert.NotNil(t, svc.TabStatuses.get(keyOf("root")), "and leaves its parent's")
	assert.NotNil(t, svc.TabStatuses.get(keyOf("child-2")), "and its sibling's")

	w = newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, &leapmuxv1.CloseTabRequest{Tab: agentTabIdent("root")}, w)
	require.Empty(t, w.errors)
	assert.Empty(t, svc.TabStatuses.snapshotFor(userid.MustNew("user-1")), "the root's close clears every status of its tree")
}

// The tab can close between the lookup and the set. Its forgetClosed advances
// closeEpoch, so the handler detects the close race and reads the tab again
// after the set so the status does not outlive a close that already ran.
func TestSetTabStatus_ForgetsTheStatusOfATabThatClosedDuringTheSet(t *testing.T) {
	t.Parallel()
	svc, d, _ := setupTestService(t)
	seedAgent(t, svc, "agent-1")
	log := watchPrivateEvents(t, svc, d)
	svc.beforeTabStatusSetFn = func() {
		// What a concurrent close commits, followed by applyTabCloseEffects
		// calling forgetClosed against a store that did not hold this status yet.
		require.NoError(t, closeErr(svc.Queries.CloseAgent(context.Background(), "agent-1")))
		svc.TabStatuses.forgetClosed(newOwnedTabKey(leapmuxv1.TabType_TAB_TYPE_AGENT, "agent-1", ""))
	}

	w := setTabStatus(d, "agent-1", &leapmuxv1.TabStatus{Text: "Running"})

	assert.Contains(t, requireRefused(t, w, codes.NotFound), "tab not found")
	assert.Nil(t, svc.TabStatuses.get(newOwnedTabKey(leapmuxv1.TabType_TAB_TYPE_AGENT, "agent-1", "")))
	events := log.waitFor(t, 2)
	assert.Equal(t, "Running", events[0].GetTabStatusChanged().GetStatus().GetText())
	assert.Nil(t, events[1].GetTabStatusChanged().GetStatus(), "the frontends hear the status go")
}

// A notification, a reveal and a tab status act on the user's view of the
// workspace, so workspace:read governs them on the stream.
func TestPrivateEventVisible_WorkspaceReadGovernsTheViewEvents(t *testing.T) {
	t.Parallel()
	fileOnly := channel.Caller{UserID: userid.MustNew("u1"), Scopes: mustScopes("file:read")}
	workspaceReader := channel.Caller{UserID: userid.MustNew("u1"), Scopes: mustScopes("file:read workspace:read")}

	// A file tab's kind needs no scope beyond the stream's file:read floor, so
	// workspace:read alone decides here.
	events := map[string]*leapmuxv1.WorkerPrivateEvent{
		"notification": {Event: &leapmuxv1.WorkerPrivateEvent_UserNotification{UserNotification: &leapmuxv1.UserNotification{Title: "t"}}},
		"reveal":       revealEvent("f1", leapmuxv1.TabType_TAB_TYPE_FILE),
		"status":       statusEvent("f1", leapmuxv1.TabType_TAB_TYPE_FILE),
	}
	for name, evt := range events {
		assert.Falsef(t, privateEventVisible(fileOnly, evt), "a caller without workspace:read must not see the %s", name)
		assert.Truef(t, privateEventVisible(workspaceReader, evt), "a caller with workspace:read sees the %s", name)
	}
}

// A reveal and a status identify one tab, and the tab's kind governs them as it
// governs a rename: a status text is the same kind of data as a title. A
// caller that may read the view but not agents hears nothing of an agent tab.
func TestPrivateEventVisible_TheTabKindGovernsARevealAndAStatus(t *testing.T) {
	t.Parallel()
	terminalReader := channel.Caller{UserID: userid.MustNew("u1"), Scopes: mustScopes("file:read workspace:read terminal:read")}
	agentReader := channel.Caller{UserID: userid.MustNew("u1"), Scopes: mustScopes("file:read workspace:read agent:read")}
	kindOnly := channel.Caller{UserID: userid.MustNew("u1"), Scopes: mustScopes("file:read agent:read terminal:read")}

	cases := []struct {
		tabType          leapmuxv1.TabType
		terminalReaderOK bool
		agentReaderOK    bool
	}{
		{leapmuxv1.TabType_TAB_TYPE_AGENT, false, true},
		{leapmuxv1.TabType_TAB_TYPE_IMAGE, false, true},
		{leapmuxv1.TabType_TAB_TYPE_TERMINAL, true, false},
	}
	for _, tc := range cases {
		for name, evt := range map[string]*leapmuxv1.WorkerPrivateEvent{
			"reveal": revealEvent("t1", tc.tabType),
			"status": statusEvent("t1", tc.tabType),
		} {
			assert.Equalf(t, tc.terminalReaderOK, privateEventVisible(terminalReader, evt), "terminal:read and the %s of a %s tab", name, tc.tabType)
			assert.Equalf(t, tc.agentReaderOK, privateEventVisible(agentReader, evt), "agent:read and the %s of a %s tab", name, tc.tabType)
			assert.Falsef(t, privateEventVisible(kindOnly, evt), "without workspace:read, the %s of a %s tab stays hidden", name, tc.tabType)
		}
	}
}

// The Worker always states the kind of the tab. An event without it can only
// come from a bug, and it fails closed for every caller rather than skipping
// the per-kind check.
func TestPrivateEventVisible_RefusesAViewEventWithNoTabKind(t *testing.T) {
	t.Parallel()
	everything := channel.Caller{UserID: userid.MustNew("u1"), Scopes: mustScopes("file:read workspace:read agent:read terminal:read")}
	assert.False(t, privateEventVisible(everything, revealEvent("t1", leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED)))
	assert.False(t, privateEventVisible(everything, statusEvent("t1", leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED)))
}

func revealEvent(tabID string, tabType leapmuxv1.TabType) *leapmuxv1.WorkerPrivateEvent {
	return &leapmuxv1.WorkerPrivateEvent{Event: &leapmuxv1.WorkerPrivateEvent_TabRevealCommand{
		TabRevealCommand: &leapmuxv1.TabRevealCommand{TabId: tabID, TabType: tabType},
	}}
}

func statusEvent(tabID string, tabType leapmuxv1.TabType) *leapmuxv1.WorkerPrivateEvent {
	return &leapmuxv1.WorkerPrivateEvent{Event: &leapmuxv1.WorkerPrivateEvent_TabStatusChanged{
		TabStatusChanged: &leapmuxv1.TabStatusChanged{TabId: tabID, TabType: tabType, Status: &leapmuxv1.TabStatus{Text: "x"}},
	}}
}

// The three RPCs take workspace:write, as the proto declares: workspace:read
// alone is refused before the handler runs.
func TestWorkerPrivateCommands_NeedWorkspaceWrite(t *testing.T) {
	t.Parallel()
	requests := map[string]proto.Message{
		leapmuxv1connect.WorkerPrivateServiceNotifyProcedure:       &leapmuxv1.NotifyRequest{Title: "t"},
		leapmuxv1connect.WorkerPrivateServiceRevealTabProcedure:    &leapmuxv1.RevealTabRequest{TabId: "agent-1"},
		leapmuxv1connect.WorkerPrivateServiceSetTabStatusProcedure: &leapmuxv1.SetTabStatusRequest{TabId: "agent-1", Status: &leapmuxv1.TabStatus{Text: "x"}},
	}
	for method, req := range requests {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			svc, d, _ := setupTestService(t)
			seedAgent(t, svc, "agent-1")

			w := newTestWriter()
			dispatchScoped(d, mustScopes("worker:read workspace:read"), method, req, w)
			assert.Contains(t, requireRefused(t, w, codes.PermissionDenied), "workspace:write")

			w = newTestWriter()
			dispatchScoped(d, mustScopes("worker:read workspace:write"), method, req, w)
			requireOK(t, w)
		})
	}
}

// A close of a tab that has no status changes nothing that a frontend shows,
// so it publishes no TabStatusChanged. Every close of every tab reaches the
// clear, and a frontend would otherwise hear a clear for each one.
func TestCloseTab_PublishesNoStatusChangeForATabWithoutAStatus(t *testing.T) {
	t.Parallel()
	svc, d, _ := setupTestService(t)
	defer drainAllInFlight(svc)
	seedAgent(t, svc, "agent-1")
	seedAgent(t, svc, "agent-2")
	requireOK(t, setTabStatus(d, "agent-2", &leapmuxv1.TabStatus{Text: "Running"}))
	log := watchPrivateEvents(t, svc, d)

	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, &leapmuxv1.CloseTabRequest{Tab: agentTabIdent("agent-1")}, w)
	require.Empty(t, w.errors)

	var changed []string
	for _, evt := range log.waitForSentinel(t, svc) {
		if got := evt.GetTabStatusChanged(); got != nil {
			changed = append(changed, got.GetTabId())
		}
	}
	assert.Equal(t, []string{"agent-2"}, changed, "only the bootstrap's replay of the other tab's status")
	assert.NotNil(t, svc.TabStatuses.get(newOwnedTabKey(leapmuxv1.TabType_TAB_TYPE_AGENT, "agent-2", "user-1")), "the other tab keeps its status")
}

// The bootstrap sends the payloads first, then the statuses, so a frontend
// that learns a file tab from its payload already holds the tab when the
// tab's status arrives.
func TestWatchWorkerPrivateEvents_ReplaysAPayloadBeforeItsStatus(t *testing.T) {
	t.Parallel()
	svc, d, _ := setupTestService(t)
	createFileTabForPath(t, svc, "user-1", "file-1", t.TempDir(), "notes.txt")
	requireOK(t, setTabStatus(d, "file-1", &leapmuxv1.TabStatus{Text: "Reviewing"}))

	log := watchPrivateEvents(t, svc, d)

	events := log.waitForSentinel(t, svc)
	require.Len(t, events, 2)
	assert.Equal(t, "file-1", events[0].GetTabPayloadRegistered().GetTabId(), "the payload comes first")
	status := events[1].GetTabStatusChanged()
	assert.Equal(t, "file-1", status.GetTabId(), "the status comes after it")
	assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_FILE, status.GetTabType())
	assert.Equal(t, "Reviewing", status.GetStatus().GetText())
}

// The store keeps an owner's entry only while the owner holds a status, so the
// map cannot grow with owners who cleared theirs. An unminted owner stores
// nothing, because no subscriber could read it.
func TestTabStatusStore_KeepsAnOwnerOnlyWhileItHoldsAStatus(t *testing.T) {
	t.Parallel()
	store := NewTabStatusStore(NewPrivateEventsBus())
	owner := userid.MustNew("user-1")
	first := newOwnedTabKey(leapmuxv1.TabType_TAB_TYPE_AGENT, "agent-1", "user-1")
	second := newOwnedTabKey(leapmuxv1.TabType_TAB_TYPE_TERMINAL, "term-1", "user-1")
	status := &leapmuxv1.TabStatus{Text: "Running"}

	_, err := store.set(first, userid.UserID{}, defaultTabStatusKey, status, 0)
	require.NoError(t, err)
	assert.Nil(t, store.get(first), "an unminted owner stores nothing")
	assert.Empty(t, store.byOwner)
	assert.Nil(t, store.snapshotFor(userid.UserID{}), "an unminted owner owns nothing")

	_, err = store.set(first, owner, defaultTabStatusKey, status, 0)
	require.NoError(t, err)
	_, err = store.set(second, owner, defaultTabStatusKey, status, 0)
	require.NoError(t, err)
	store.clear(first, owner, defaultTabStatusKey)
	assert.Nil(t, store.get(first))
	assert.Same(t, status, store.get(second), "a clear removes one tab's status")
	assert.Len(t, store.byOwner, 1, "the owner still holds a status")

	store.forgetClosed(second)
	assert.Empty(t, store.byOwner, "the owner's last status took the entry with it")

	_, err = store.set(first, owner, defaultTabStatusKey, status, 0)
	require.NoError(t, err)
	store.clear(first, owner, defaultTabStatusKey)
	assert.Empty(t, store.byOwner, "a clear of the last status takes the entry too")
}

func TestTabStatusStore_KeepsConcurrentKeysAndUpdatesAtTheLimit(t *testing.T) {
	t.Parallel()
	store := NewTabStatusStore(NewPrivateEventsBus())
	owner := userid.MustNew("user-1")
	tab := newOwnedTabKey(leapmuxv1.TabType_TAB_TYPE_AGENT, "agent-1", "user-1")
	begin := make(chan struct{})
	var writers sync.WaitGroup
	for i := range tabStatusMaxKeys {
		writers.Go(func() {
			<-begin
			key := fmt.Sprintf("reporter-%d", i)
			_, err := store.set(tab, owner, key, &leapmuxv1.TabStatus{Text: key}, 0)
			assert.NoError(t, err)
		})
	}
	close(begin)
	writers.Wait()
	entries := store.list(tab, owner)
	require.Len(t, entries, tabStatusMaxKeys)
	seen := make(map[string]bool)
	for _, entry := range entries {
		require.False(t, seen[entry.GetKey()])
		assert.Equal(t, entry.GetKey(), entry.GetStatus().GetText())
		seen[entry.GetKey()] = true
	}
	originalVisible := entries[0]
	_, err := store.set(tab, owner, "extra", &leapmuxv1.TabStatus{Text: "Extra"}, 0)
	assert.ErrorContains(t, err, "status keys")
	assert.Same(t, originalVisible.GetStatus(), store.get(tab))
	updatedKey := entries[len(entries)-1].GetKey()
	replacement := &leapmuxv1.TabStatus{Text: "Done"}
	_, err = store.set(tab, owner, updatedKey, replacement, 0)
	require.NoError(t, err)
	assert.Same(t, replacement, store.get(tab))
	assert.Same(t, originalVisible, entries[0], "a later change cannot reorder the earlier list")
	store.clear(tab, owner, updatedKey)
	assert.Same(t, originalVisible.GetStatus(), store.get(tab))
	_, err = store.set(tab, owner, "extra", &leapmuxv1.TabStatus{Text: "Extra"}, 0)
	require.NoError(t, err)
	store.forgetClosed(tab)
	assert.Empty(t, store.list(tab, owner))
	assert.Empty(t, store.snapshotFor(owner))
}

// Concurrent changes of one tab reach a subscriber in the order that the store
// applied them, because the store publishes under its own lock. After each
// round of concurrent writes, the last event of the round therefore states the
// status that the store holds. A store that published after it released its
// lock could let an earlier write's event arrive last, and many short rounds
// give that order many chances to show. A reader races the writers in each
// round, so the race detector checks every access to the store.
func TestTabStatusStore_PublishesTheChangesOfOneTabInTheOrderThatItStoresThem(t *testing.T) {
	t.Parallel()
	const writers, rounds = 8, 500
	bus := NewPrivateEventsBus()
	// Room for one round and its sentinel, so the bus drops no event.
	bus.bufSize = 2 * (writers + 1)
	store := NewTabStatusStore(bus)
	owner := userid.MustNew("user-1")
	key := newOwnedTabKey(leapmuxv1.TabType_TAB_TYPE_AGENT, "agent-1", "user-1")

	received := make(chan *leapmuxv1.WorkerPrivateEvent, bus.bufSize)
	registered := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = bus.SnapshotAndSubscribe(ctx, owner,
			func(userid.UserID) []*leapmuxv1.WorkerPrivateEvent {
				close(registered)
				return nil
			},
			func(evt *leapmuxv1.WorkerPrivateEvent) error {
				received <- evt
				return nil
			})
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	<-registered
	deadline := testutil.DeadlineContext(t)

	for round := range rounds {
		begin := make(chan struct{})
		var wg sync.WaitGroup
		for writer := range writers {
			wg.Go(func() {
				<-begin
				// One writer clears, so a round can also end without a status.
				if writer == 0 {
					store.clear(key, owner, defaultTabStatusKey)
					return
				}
				_, err := store.set(key, owner, defaultTabStatusKey, &leapmuxv1.TabStatus{Text: fmt.Sprintf("round %d, writer %d", round, writer)}, 0)
				assert.NoError(t, err)
			})
		}
		wg.Go(func() {
			<-begin
			store.get(key)
			store.snapshotFor(owner)
		})
		close(begin)
		wg.Wait()

		bus.PublishTabRevealCommand(owner, leapmuxv1.TabType_TAB_TYPE_AGENT, "sentinel")
		var changes []*leapmuxv1.TabStatusChanged
		for sentinel := false; !sentinel; {
			select {
			case evt := <-received:
				if evt.GetTabRevealCommand() != nil {
					sentinel = true
				} else {
					changes = append(changes, evt.GetTabStatusChanged())
				}
			case <-deadline.Done():
				require.FailNow(t, "the sentinel never arrived", "round %d", round)
			}
		}
		require.Len(t, changes, writers, "round %d: the bus dropped no event", round)
		last := changes[len(changes)-1]
		if stored := store.get(key); stored == nil {
			require.Nil(t, last.GetStatus(), "round %d: the last event clears the status that the store cleared last", round)
		} else {
			require.Same(t, stored, last.GetStatus(), "round %d: the last event states the status that the store holds", round)
		}
	}
}
